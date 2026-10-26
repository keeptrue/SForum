package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/config"
)

var errWorkerRuntimeCoordinatorTerminalTest = errors.New("worker runtime coordinator lease lost")

func TestWorkerRuntimeOwnerTerminalFailureClosesRuntimeBeforeReporting(t *testing.T) {
	release := make(chan struct{})
	coordinator := startWorkerRuntimeOwnerTestCoordinator(t, func(context.Context) error {
		<-release
		return errWorkerRuntimeCoordinatorTerminalTest
	})
	events := &workerRuntimeOwnerTestEvents{}
	runtime := &workerRuntimeOwnerTestRuntime{
		countingWorkerRuntime: &countingWorkerRuntime{}, events: events, closed: make(chan struct{}),
	}
	gateway := &workerRuntimeOwnerTestGateway{events: events}
	owner := newWorkerRuntimeOwner(runtime, gateway, coordinator, nil, time.Second)
	worker := &Worker{failures: owner.Failures(), close: owner.Close}

	close(release)
	select {
	case err, ok := <-worker.Failures():
		if !ok || !errors.Is(err, errWorkerRuntimeCoordinatorTerminalTest) {
			t.Fatalf("terminal failure = %v, open=%v", err, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker runtime failure")
	}
	select {
	case <-runtime.closed:
	default:
		t.Fatal("runtime admission was not closed before failure publication")
	}
	if gateway.closeCalls.Load() != 0 {
		t.Fatal("terminal monitor must leave Gateway ownership to graceful shutdown")
	}

	worker.Close()
	worker.Close()
	if runtime.closeCalls.Load() != 1 {
		t.Fatalf("runtime close calls = %d", runtime.closeCalls.Load())
	}
	if gateway.closeCalls.Load() != 1 {
		t.Fatalf("gateway close calls = %d", gateway.closeCalls.Load())
	}
}

func TestWorkerRuntimeOwnerStopsCoordinatorBeforeRuntimeAndGateway(t *testing.T) {
	events := &workerRuntimeOwnerTestEvents{}
	coordinator := startWorkerRuntimeOwnerTestCoordinator(t, func(ctx context.Context) error {
		<-ctx.Done()
		events.add("coordinator")
		return nil
	})
	runtime := &workerRuntimeOwnerTestRuntime{
		countingWorkerRuntime: &countingWorkerRuntime{}, events: events, closed: make(chan struct{}),
	}
	gateway := &workerRuntimeOwnerTestGateway{events: events}
	owner := newWorkerRuntimeOwner(runtime, gateway, coordinator, nil, time.Second)

	owner.Close()
	owner.Close()
	if got := events.snapshot(); len(got) != 3 ||
		got[0] != "coordinator" || got[1] != "runtime" || got[2] != "gateway" {
		t.Fatalf("shutdown order = %#v", got)
	}
	if runtime.closeCalls.Load() != 1 || gateway.closeCalls.Load() != 1 {
		t.Fatalf("close calls runtime=%d gateway=%d", runtime.closeCalls.Load(), gateway.closeCalls.Load())
	}
}

func TestWorkerRuntimeOwnerCloseRemainsBoundedWhenCoordinatorDoesNotStop(t *testing.T) {
	release := make(chan struct{})
	coordinator := startWorkerRuntimeOwnerTestCoordinator(t, func(context.Context) error {
		<-release
		return nil
	})
	runtime := &workerRuntimeOwnerTestRuntime{
		countingWorkerRuntime: &countingWorkerRuntime{}, closed: make(chan struct{}),
	}
	gateway := &workerRuntimeOwnerTestGateway{}
	owner := newWorkerRuntimeOwner(runtime, gateway, coordinator, nil, 20*time.Millisecond)

	started := time.Now()
	owner.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded close took %s", elapsed)
	}
	if runtime.closeCalls.Load() != 1 || gateway.closeCalls.Load() != 1 {
		t.Fatalf("bounded close calls runtime=%d gateway=%d", runtime.closeCalls.Load(), gateway.closeCalls.Load())
	}
	close(release)
	select {
	case <-coordinator.Done():
	case <-time.After(time.Second):
		t.Fatal("coordinator runner did not finish after release")
	}
}

func TestStandaloneWorkerCoordinatorSafeModeNeedsNoProductionDependencies(t *testing.T) {
	coordinator, err := startStandaloneWorkerRuntimeCoordinator(
		context.Background(),
		config.Config{SafeMode: true},
		&bootstrapExtensionSettingsStore{},
		&countingWorkerRuntime{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator == nil || coordinator.Active() {
		t.Fatalf("safe mode coordinator = %#v", coordinator)
	}
	owner := newWorkerRuntimeOwner(&countingWorkerRuntime{}, &noopHostGateway{}, coordinator, nil, time.Second)
	if owner.Failures() != nil {
		t.Fatal("safe mode must not expose a runtime failure channel")
	}
}

func TestStandaloneWorkerCoordinatorStartupFailureBlocksRuntimeReturn(t *testing.T) {
	originalManager := newStandaloneWorkerRuntimeManager
	originalCoordinator := startStandaloneWorkerRuntimeCoordinator
	t.Cleanup(func() {
		newStandaloneWorkerRuntimeManager = originalManager
		startStandaloneWorkerRuntimeCoordinator = originalCoordinator
	})
	runtime := &countingWorkerRuntime{}
	newStandaloneWorkerRuntimeManager = func(
		extensions.Store,
		extensionsruntime.HostAPIRegistrar,
		extensionsruntime.PluginSettings,
		extensionsruntime.RuntimeTrustSource,
		extensionsruntime.RuntimeDatabaseLeaseRegistry,
	) workerExtensionRuntime {
		return runtime
	}
	startStandaloneWorkerRuntimeCoordinator = func(
		context.Context,
		config.Config,
		extensions.Store,
		workerExtensionRuntime,
		*slog.Logger,
	) (*pluginRuntimeCoordinatorRuntime, error) {
		return nil, errWorkerRuntimeCoordinatorTerminalTest
	}

	bootstrapCtx, cancel := context.WithCancel(context.Background())
	cancel()
	built, gateway, coordinator, err := buildStandaloneWorkerExtensionRuntime(
		bootstrapCtx, config.Config{ExtensionRoot: t.TempDir()},
		recordingDatabaseBinderFactory(nil), newRecordingCommandRuntimeBinder(nil),
		&bootstrapExtensionSettingsStore{}, nil, nil, nil, "", nil, nil,
	)
	if !errors.Is(err, errWorkerRuntimeCoordinatorTerminalTest) {
		t.Fatalf("startup failure = %v", err)
	}
	if built != nil || gateway != nil || coordinator != nil {
		t.Fatalf("failed startup returned runtime=%T gateway=%T coordinator=%#v", built, gateway, coordinator)
	}
	if runtime.closeCalls != 1 {
		t.Fatalf("failed startup runtime close calls = %d", runtime.closeCalls)
	}
	if len(runtime.reconciledItems) != 0 {
		t.Fatalf("failed startup used direct Reconcile: %#v", runtime.reconciledItems)
	}
}

func startWorkerRuntimeOwnerTestCoordinator(
	t *testing.T,
	run func(context.Context) error,
) *pluginRuntimeCoordinatorRuntime {
	t.Helper()
	coordinator, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: pluginRuntimeCoordinatorBootstrapTestIdentity(
			"worker-owner", extensions.PluginRuntimeProcessWorker,
		),
		Ensurer: newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
		Build: func(
			_ extensions.PluginRuntimeNodeIdentity,
			onReady func(),
			_ func(error),
		) (pluginRuntimeCoordinatorRunner, error) {
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
				onReady()
				return run(ctx)
			}), nil
		},
		StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

type workerRuntimeOwnerTestEvents struct {
	mu     sync.Mutex
	values []string
}

func (events *workerRuntimeOwnerTestEvents) add(value string) {
	if events == nil {
		return
	}
	events.mu.Lock()
	events.values = append(events.values, value)
	events.mu.Unlock()
}

func (events *workerRuntimeOwnerTestEvents) snapshot() []string {
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]string(nil), events.values...)
}

type workerRuntimeOwnerTestRuntime struct {
	*countingWorkerRuntime
	events     *workerRuntimeOwnerTestEvents
	closed     chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func (runtime *workerRuntimeOwnerTestRuntime) Close(context.Context) {
	runtime.closeCalls.Add(1)
	runtime.events.add("runtime")
	runtime.closeOnce.Do(func() { close(runtime.closed) })
}

type workerRuntimeOwnerTestGateway struct {
	events     *workerRuntimeOwnerTestEvents
	closeCalls atomic.Int32
}

func (gateway *workerRuntimeOwnerTestGateway) Close() error {
	gateway.closeCalls.Add(1)
	gateway.events.add("gateway")
	return nil
}
