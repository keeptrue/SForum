package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var apiThemeRuntimeHostname = os.Hostname

var (
	errAPIThemeRuntimeWatcherInvalid = errors.New("bootstrap: API theme runtime watcher is invalid")
	errAPIThemeRuntimeWatcherStopped = errors.New("bootstrap: API theme runtime watcher stopped unexpectedly")
)

type apiThemeRuntimeWatcherRunner interface {
	Run(context.Context) error
}

type apiThemeRuntimeWatcherBuilder func(func()) (apiThemeRuntimeWatcherRunner, error)

type apiThemeRuntimeWatcherLaunchConfig struct {
	Build       apiThemeRuntimeWatcherBuilder
	Fallback    func(context.Context) error
	StopTimeout time.Duration
}

type apiThemeRuntimeWatcherRuntime struct {
	active      bool
	stopTimeout time.Duration
	fallback    func(context.Context) error

	cancel   context.CancelFunc
	done     chan struct{}
	failures chan error
	stopOnce sync.Once
	stopping atomic.Bool

	errMu sync.RWMutex
	err   error
}

func newAPIThemeRuntimeWatcher(
	store *extensions.PostgresStore,
	service *extensions.Service,
	logger *slog.Logger,
	onReady func(),
	stopTimeout time.Duration,
) (*extensions.ThemeRuntimeWatcher, error) {
	hostname, err := apiThemeRuntimeHostname()
	if err != nil {
		return nil, fmt.Errorf("resolve theme runtime node hostname: %w", err)
	}
	nodeID, err := normalizeThemeRuntimeNodeID(hostname)
	if err != nil {
		return nil, err
	}
	report := func(err error) {
		if err != nil && logger != nil {
			logger.Warn("theme runtime watcher degraded", "error", err)
		}
	}
	notifications := extensions.NewPostgresThemeRuntimeNotifications(
		store, extensions.DefaultThemeRuntimeReconnectDelay, report,
	)
	return extensions.NewThemeRuntimeWatcher(store, service, notifications, extensions.ThemeRuntimeWatcherConfig{
		Identity: extensions.ThemeRuntimeNodeIdentity{
			NodeID: nodeID,
			BootID: extensions.NewActivationBootID(),
		},
		StopTimeout: stopTimeout,
		OnError:     report,
		OnReady:     onReady,
	})
}

func startAPIThemeRuntimeWatcher(
	ctx context.Context,
	store *extensions.PostgresStore,
	service *extensions.Service,
	logger *slog.Logger,
	stopTimeout time.Duration,
) (*apiThemeRuntimeWatcherRuntime, error) {
	if ctx == nil || store == nil || service == nil {
		return nil, errAPIThemeRuntimeWatcherInvalid
	}
	stopTimeout = normalizedPluginRuntimeCoordinatorStopTimeout(stopTimeout)
	return launchAPIThemeRuntimeWatcher(ctx, apiThemeRuntimeWatcherLaunchConfig{
		StopTimeout: stopTimeout,
		Fallback:    service.FailClosedThemeRuntime,
		Build: func(onReady func()) (apiThemeRuntimeWatcherRunner, error) {
			return newAPIThemeRuntimeWatcher(store, service, logger, onReady, stopTimeout)
		},
	})
}

func launchAPIThemeRuntimeWatcher(
	ctx context.Context,
	config apiThemeRuntimeWatcherLaunchConfig,
) (*apiThemeRuntimeWatcherRuntime, error) {
	if ctx == nil || config.Build == nil {
		return nil, errAPIThemeRuntimeWatcherInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stopTimeout := normalizedPluginRuntimeCoordinatorStopTimeout(config.StopTimeout)
	ready := make(chan struct{})
	var readyOnce sync.Once
	runner, err := config.Build(func() { readyOnce.Do(func() { close(ready) }) })
	if err != nil {
		return nil, fmt.Errorf("construct API theme runtime watcher: %w", err)
	}
	if runner == nil {
		return nil, errAPIThemeRuntimeWatcherInvalid
	}
	runtime := &apiThemeRuntimeWatcherRuntime{
		active: true, stopTimeout: stopTimeout, fallback: config.Fallback,
		done: make(chan struct{}), failures: make(chan error, 1),
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runtime.cancel = cancel
	go runtime.run(runCtx, runner)

	abort := func(cause error) (*apiThemeRuntimeWatcherRuntime, error) {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
		defer stopCancel()
		if stopErr := runtime.Stop(stopCtx); stopErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("stop API theme runtime watcher: %w", stopErr))
		}
		return nil, cause
	}
	select {
	case <-ready:
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		select {
		case runErr, ok := <-runtime.failures:
			if !ok || runErr == nil {
				runErr = errAPIThemeRuntimeWatcherStopped
			}
			return abort(runErr)
		case <-runtime.done:
			if runErr := runtime.Err(); runErr != nil {
				return nil, runErr
			}
			return nil, errAPIThemeRuntimeWatcherStopped
		default:
			return runtime, nil
		}
	case runErr, ok := <-runtime.failures:
		if !ok || runErr == nil {
			runErr = errAPIThemeRuntimeWatcherStopped
		}
		return abort(runErr)
	case <-ctx.Done():
		return abort(ctx.Err())
	}
}

func (runtime *apiThemeRuntimeWatcherRuntime) run(
	ctx context.Context,
	runner apiThemeRuntimeWatcherRunner,
) {
	err := runner.Run(ctx)
	if err == nil && !runtime.stopping.Load() {
		err = errAPIThemeRuntimeWatcherStopped
	}
	if err != nil && !runtime.stopping.Load() && runtime.fallback != nil {
		fallbackCtx, cancel := context.WithTimeout(context.Background(), runtime.stopTimeout)
		fallbackErr := runtime.fallback(fallbackCtx)
		cancel()
		if fallbackErr != nil {
			err = errors.Join(err, fmt.Errorf("restore default theme after watcher failure: %w", fallbackErr))
		}
	}
	runtime.errMu.Lock()
	runtime.err = err
	runtime.errMu.Unlock()
	if err != nil && !runtime.stopping.Load() {
		runtime.failures <- err
	}
	close(runtime.failures)
	close(runtime.done)
}

func (runtime *apiThemeRuntimeWatcherRuntime) Stop(ctx context.Context) error {
	if runtime == nil || !runtime.active {
		return nil
	}
	if ctx == nil {
		return errAPIThemeRuntimeWatcherInvalid
	}
	runtime.stopOnce.Do(func() {
		runtime.stopping.Store(true)
		if runtime.cancel != nil {
			runtime.cancel()
		}
	})
	waitCtx, cancel := context.WithTimeout(ctx, runtime.stopTimeout)
	defer cancel()
	select {
	case <-runtime.done:
		return nil
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

func (runtime *apiThemeRuntimeWatcherRuntime) Failures() <-chan error {
	if runtime == nil || !runtime.active {
		return nil
	}
	return runtime.failures
}

func (runtime *apiThemeRuntimeWatcherRuntime) Done() <-chan struct{} {
	if runtime == nil {
		return nil
	}
	return runtime.done
}

func (runtime *apiThemeRuntimeWatcherRuntime) Err() error {
	if runtime == nil {
		return nil
	}
	runtime.errMu.RLock()
	defer runtime.errMu.RUnlock()
	return runtime.err
}

func normalizeThemeRuntimeNodeID(hostname string) (string, error) {
	nodeID := strings.TrimSpace(hostname)
	if nodeID == "" {
		return "", extensions.ErrThemeRuntimeNodeInvalid
	}
	if len([]byte(nodeID)) > 128 {
		digest := sha256.Sum256([]byte(nodeID))
		nodeID = "host-" + hex.EncodeToString(digest[:])
	}
	return nodeID, nil
}
