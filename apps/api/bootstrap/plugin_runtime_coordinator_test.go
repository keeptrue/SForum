package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

var errPluginRuntimeCoordinatorBootstrapTest = errors.New("plugin runtime bootstrap test failure")

type pluginRuntimeCoordinatorBootstrapTestEnsurer struct {
	publication extensions.PluginRuntimePublication
	err         error
	// errs 按调用次序返回；用尽后回落到 err / publication。nil 条目表示该次成功。
	errs   []error
	calls  atomic.Int32
	onCall func(call int32)
}

func (ensurer *pluginRuntimeCoordinatorBootstrapTestEnsurer) EnsureInitialPluginRuntimePublication(
	ctx context.Context,
) (extensions.PluginRuntimePublication, error) {
	call := ensurer.calls.Add(1)
	if ensurer.onCall != nil {
		ensurer.onCall(call)
	}
	if err := ctx.Err(); err != nil {
		return extensions.PluginRuntimePublication{}, err
	}
	if int(call) <= len(ensurer.errs) {
		if err := ensurer.errs[call-1]; err != nil {
			return extensions.PluginRuntimePublication{}, err
		}
		return ensurer.publication, nil
	}
	if ensurer.err != nil {
		return extensions.PluginRuntimePublication{}, ensurer.err
	}
	return ensurer.publication, nil
}

type pluginRuntimeCoordinatorBootstrapTestRunner func(context.Context) error

func (runner pluginRuntimeCoordinatorBootstrapTestRunner) Run(ctx context.Context) error {
	return runner(ctx)
}

type pluginRuntimeCoordinatorBootstrapStartResult struct {
	runtime *pluginRuntimeCoordinatorRuntime
	err     error
}

type pluginRuntimeCoordinatorBootstrapWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *pluginRuntimeCoordinatorBootstrapWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestPluginRuntimeCoordinatorBootstrapWaitsForDurableConvergence(t *testing.T) {
	ensurer := newPluginRuntimeCoordinatorBootstrapTestEnsurer()
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("wait", extensions.PluginRuntimeProcessAPI)
	entered := make(chan struct{})
	converge := make(chan struct{})
	runnerDone := make(chan struct{})
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(
				got extensions.PluginRuntimeNodeIdentity,
				onReady func(),
				_ func(error),
			) (pluginRuntimeCoordinatorRunner, error) {
				if got != identity {
					return nil, errPluginRuntimeCoordinatorBootstrapTest
				}
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					defer close(runnerDone)
					close(entered)
					select {
					case <-ctx.Done():
						return nil
					case <-converge:
						onReady()
					}
					<-ctx.Done()
					return nil
				}), nil
			},
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()

	waitPluginRuntimeCoordinatorBootstrapSignal(t, entered)
	select {
	case premature := <-result:
		t.Fatalf("bootstrap returned before durable convergence: %#v", premature)
	default:
	}
	close(converge)
	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.err != nil || started.runtime == nil || !started.runtime.Active() ||
		started.runtime.Identity() != identity || ensurer.calls.Load() != 1 {
		t.Fatalf("runtime=%#v err=%v ensure calls=%d", started.runtime, started.err, ensurer.calls.Load())
	}
	if err := started.runtime.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runnerDone)
	if err, ok := <-started.runtime.Failures(); ok || err != nil {
		t.Fatalf("normal stop failure=%v open=%v", err, ok)
	}
}

func TestPluginRuntimeCoordinatorBootstrapSafeModeDoesNotSeedOrStart(t *testing.T) {
	for _, start := range []func() (*pluginRuntimeCoordinatorRuntime, error){
		func() (*pluginRuntimeCoordinatorRuntime, error) {
			return startPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorBootstrapConfig{SafeMode: true})
		},
		func() (*pluginRuntimeCoordinatorRuntime, error) {
			return launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{SafeMode: true})
		},
	} {
		runtime, err := start()
		if err != nil || runtime == nil || runtime.Active() || runtime.Identity() != (extensions.PluginRuntimeNodeIdentity{}) {
			t.Fatalf("safe runtime=%#v err=%v", runtime, err)
		}
		for range 3 {
			if err := runtime.Stop(t.Context()); err != nil {
				t.Fatalf("safe repeated stop: %v", err)
			}
		}
		select {
		case <-runtime.Done():
		default:
			t.Fatal("safe mode runtime is not already stopped")
		}
		if err, ok := <-runtime.Failures(); ok || err != nil {
			t.Fatalf("safe mode failure=%v open=%v", err, ok)
		}
	}
}

func TestPluginRuntimeCoordinatorBootstrapRetriesLifecycleInProgressThenSucceeds(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-retry-ok", extensions.PluginRuntimeProcessAPI)
	inProgress := fmt.Errorf(
		"%w: initial plugin runtime publication waits for lifecycle completion",
		extensions.ErrLifecycleOperationInProgress,
	)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
		errs:        []error{inProgress, nil},
	}
	var buildCalls atomic.Int32
	runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  ensurer,
		Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
			buildCalls.Add(1)
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
				onReady()
				<-ctx.Done()
				return nil
			}), nil
		},
		// 注入极小重试间隔；截止仅作上界，成功路径由 errs 序列驱动。
		GenesisWaitTimeout:   time.Second,
		GenesisRetryInterval: time.Millisecond,
		StopTimeout:          time.Second,
	})
	if err != nil || runtime == nil || !runtime.Active() {
		t.Fatalf("runtime=%#v err=%v ensure calls=%d", runtime, err, ensurer.calls.Load())
	}
	if ensurer.calls.Load() != 2 || buildCalls.Load() != 1 {
		t.Fatalf("ensure calls=%d build calls=%d", ensurer.calls.Load(), buildCalls.Load())
	}
	if stopErr := runtime.Stop(t.Context()); stopErr != nil {
		t.Fatalf("stop: %v", stopErr)
	}
}

func TestPluginRuntimeCoordinatorBootstrapLifecycleInProgressHitsBound(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-retry-bound", extensions.PluginRuntimeProcessWorker)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
		err:         extensions.ErrLifecycleOperationInProgress,
	}
	var buildCalls atomic.Int32
	// 1ns 截止：首次 in-progress 返回后 remaining 已耗尽，无需 sleep 即可触顶。
	_, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  ensurer,
		Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
			buildCalls.Add(1)
			t.Fatal("builder must not run before genesis succeeds")
			return nil, nil
		},
		GenesisWaitTimeout:   time.Nanosecond,
		GenesisRetryInterval: time.Hour,
	})
	if !errors.Is(err, extensions.ErrLifecycleOperationInProgress) {
		t.Fatalf("error=%v want ErrLifecycleOperationInProgress", err)
	}
	if ensurer.calls.Load() < 1 || buildCalls.Load() != 0 {
		t.Fatalf("ensure calls=%d build calls=%d", ensurer.calls.Load(), buildCalls.Load())
	}
}

func TestPluginRuntimeCoordinatorBootstrapLifecycleWaitHonorsCancellation(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-retry-cancel", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		err: extensions.ErrLifecycleOperationInProgress,
	}
	var buildCalls atomic.Int32
	var runnerCalls atomic.Int32
	baseCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waitEntered := make(chan struct{})
	ctx := &pluginRuntimeCoordinatorBootstrapWaitContext{
		Context: baseCtx,
		entered: waitEntered,
	}
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(ctx, pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				buildCalls.Add(1)
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(context.Context) error {
					runnerCalls.Add(1)
					return nil
				}), nil
			},
			// 长截止与重试间隔确保返回只能由父 context 取消触发。
			GenesisWaitTimeout:   time.Hour,
			GenesisRetryInterval: time.Hour,
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()

	// Done() 首次被求值时，in-progress 已返回且 retry timer 已创建。
	waitPluginRuntimeCoordinatorBootstrapSignal(t, waitEntered)
	cancel()
	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if !errors.Is(started.err, context.Canceled) || started.runtime != nil {
		t.Fatalf("runtime=%#v error=%v want context.Canceled", started.runtime, started.err)
	}
	if ensurer.calls.Load() != 1 || buildCalls.Load() != 0 || runnerCalls.Load() != 0 {
		t.Fatalf(
			"ensure calls=%d build calls=%d runner calls=%d",
			ensurer.calls.Load(), buildCalls.Load(), runnerCalls.Load(),
		)
	}
}

func TestPluginRuntimeCoordinatorBootstrapNonRetryableGenesisErrorIsImmediate(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-no-retry", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
		err:         errPluginRuntimeCoordinatorBootstrapTest,
	}
	var buildCalls atomic.Int32
	// 故意配置较长 wait，证明非 in-progress 错误不会进入重试。
	_, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  ensurer,
		Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
			buildCalls.Add(1)
			t.Fatal("builder must not run after non-retryable genesis failure")
			return nil, nil
		},
		GenesisWaitTimeout:   30 * time.Second,
		GenesisRetryInterval: time.Millisecond,
	})
	if !errors.Is(err, errPluginRuntimeCoordinatorBootstrapTest) {
		t.Fatalf("error=%v want=%v", err, errPluginRuntimeCoordinatorBootstrapTest)
	}
	if ensurer.calls.Load() != 1 || buildCalls.Load() != 0 {
		t.Fatalf("ensure calls=%d build calls=%d", ensurer.calls.Load(), buildCalls.Load())
	}
	if errors.Is(err, extensions.ErrLifecycleOperationInProgress) {
		t.Fatalf("non-retryable error must not carry lifecycle in-progress: %v", err)
	}
}

func TestPluginRuntimeCoordinatorBootstrapRejectsStartupErrors(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("errors", extensions.PluginRuntimeProcessWorker)
	tests := []struct {
		name       string
		ensurer    *pluginRuntimeCoordinatorBootstrapTestEnsurer
		build      pluginRuntimeCoordinatorBuilder
		want       error
		buildCalls int32
	}{
		{
			name: "genesis",
			ensurer: &pluginRuntimeCoordinatorBootstrapTestEnsurer{
				publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
				err:         errPluginRuntimeCoordinatorBootstrapTest,
			},
			build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				t.Fatal("builder ran after genesis failure")
				return nil, nil
			},
			want: errPluginRuntimeCoordinatorBootstrapTest,
		},
		{
			name:    "builder",
			ensurer: newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
			build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return nil, errPluginRuntimeCoordinatorBootstrapTest
			},
			want:       errPluginRuntimeCoordinatorBootstrapTest,
			buildCalls: 1,
		},
		{
			name:    "run",
			ensurer: newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
			build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(context.Context) error {
					return errPluginRuntimeCoordinatorBootstrapTest
				}), nil
			},
			want:       errPluginRuntimeCoordinatorBootstrapTest,
			buildCalls: 1,
		},
		{
			name:    "initial reconciliation",
			ensurer: newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
			build: func(_ extensions.PluginRuntimeNodeIdentity, _ func(), onError func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					onError(errPluginRuntimeCoordinatorBootstrapTest)
					<-ctx.Done()
					return nil
				}), nil
			},
			want:       errPluginRuntimeCoordinatorBootstrapTest,
			buildCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			build := test.build
			_, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
				Identity: identity,
				Ensurer:  test.ensurer,
				Build: func(identity extensions.PluginRuntimeNodeIdentity, ready func(), failed func(error)) (pluginRuntimeCoordinatorRunner, error) {
					calls.Add(1)
					return build(identity, ready, failed)
				},
				StopTimeout: time.Second,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if calls.Load() != test.buildCalls || test.ensurer.calls.Load() != 1 {
				t.Fatalf("build calls=%d want=%d ensure calls=%d", calls.Load(), test.buildCalls, test.ensurer.calls.Load())
			}
		})
	}

	invalidGenesis := newPluginRuntimeCoordinatorBootstrapTestEnsurer()
	invalidGenesis.publication.Revision = 0
	if _, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  invalidGenesis,
		Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
			t.Fatal("builder ran for invalid genesis")
			return nil, nil
		},
	}); !errors.Is(err, errPluginRuntimeCoordinatorBootstrapInvalid) {
		t.Fatalf("invalid genesis error=%v", err)
	}
}

// API 与 standalone worker 都经 startPluginRuntimeCoordinator 进入同一 production
// applier 构造点。此处只断言 wiring 绑定 Protocol V2 full-set applier 且可构造；
// cold-start / disarm / 回滚语义由 Support/Extensions InitialBootstrap 行为测试覆盖。
func TestNewProductionPluginRuntimeFullSetApplierWiresInitialBootstrapConstructor(t *testing.T) {
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{})
	inventory := pluginRuntimeCoordinatorBootstrapTestInventory{}
	production, err := newProductionPluginRuntimeFullSetApplier(manager, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if production == nil {
		t.Fatal("production applier must be constructed via NewManagerPluginRuntimeFullSetApplier")
	}
	// 对照：普通构造器仍可用；两者类型相同，差异仅在首轮 Apply 行为（Extensions 包证明）。
	ordinary, err := extensionsruntime.NewManagerPluginRuntimeFullSetApplier(manager, inventory)
	if err != nil || ordinary == nil {
		t.Fatalf("ordinary applier: %v %#v", err, ordinary)
	}
}

// pluginRuntimeCoordinatorBootstrapTestInventory 仅满足 applier 构造依赖。
type pluginRuntimeCoordinatorBootstrapTestInventory struct{}

func (pluginRuntimeCoordinatorBootstrapTestInventory) LatestPluginRuntimePublication(
	context.Context,
) (extensions.PluginRuntimePublication, error) {
	return extensions.PluginRuntimePublication{}, extensions.ErrPluginRuntimePublicationNotFound
}

func (pluginRuntimeCoordinatorBootstrapTestInventory) Get(context.Context, string) (extensions.Extension, error) {
	return extensions.Extension{}, errors.New("bootstrap test inventory has no extensions")
}

func (pluginRuntimeCoordinatorBootstrapTestInventory) GetExtensionVersion(
	context.Context, extensions.ExactExtensionVersionInput,
) (extensions.ExtensionVersion, error) {
	return extensions.ExtensionVersion{}, errors.New("bootstrap test inventory has no versions")
}

func TestPluginRuntimeCoordinatorBootstrapPollConvergesAfterMissedNotification(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("missed-notify", extensions.PluginRuntimeProcessAPI)
	ensurer := newPluginRuntimeCoordinatorBootstrapTestEnsurer()
	runnerStarted := make(chan struct{})
	var durableRevision atomic.Int64
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					close(runnerStarted)
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return nil
						case <-ticker.C:
							if durableRevision.Load() == 2 {
								onReady()
								<-ctx.Done()
								return nil
							}
						}
					}
				}), nil
			},
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runnerStarted)
	// No wake is emitted. Only the coordinator's durable poll observes this.
	durableRevision.Store(2)
	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.err != nil || started.runtime == nil {
		t.Fatalf("runtime=%#v err=%v", started.runtime, started.err)
	}
	if err := started.runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPluginRuntimeCoordinatorBootstrapCancellationBeforeReadyStopsRunner(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("cancel", extensions.PluginRuntimeProcessWorker)
	ensurer := newPluginRuntimeCoordinatorBootstrapTestEnsurer()
	runnerStarted := make(chan struct{})
	runnerDone := make(chan struct{})
	startCtx, cancelStart := context.WithCancel(t.Context())
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(startCtx, pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					defer close(runnerDone)
					close(runnerStarted)
					<-ctx.Done()
					return ctx.Err()
				}), nil
			},
			StopTimeout: time.Second,
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runnerStarted)
	cancelStart()
	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.runtime != nil || !errors.Is(started.err, context.Canceled) {
		t.Fatalf("runtime=%#v err=%v", started.runtime, started.err)
	}
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runnerDone)
}

func TestPluginRuntimeCoordinatorBootstrapHeartbeatLossSignalsFailClosed(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("heartbeat", extensions.PluginRuntimeProcessAPI)
	leaseLost := make(chan struct{})
	runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
		Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(context.Context) error {
				onReady()
				<-leaseLost
				return extensions.ErrPluginRuntimeNodeLeaseLost
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	close(leaseLost)
	select {
	case failure, ok := <-runtime.Failures():
		if !ok || !errors.Is(failure, extensions.ErrPluginRuntimeNodeLeaseLost) {
			t.Fatalf("failure=%v open=%v", failure, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat lease loss did not emit fail-closed signal")
	}
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runtime.Done())
	if !errors.Is(runtime.Err(), extensions.ErrPluginRuntimeNodeLeaseLost) {
		t.Fatalf("terminal error=%v", runtime.Err())
	}
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPluginRuntimeCoordinatorBootstrapIdentitySeparatesAPIWorkerAndBoots(t *testing.T) {
	hostname := "  " + strings.Repeat("production-node-", 20) + "  "
	firstBoot := extensions.NewActivationBootID()
	secondBoot := extensions.NewActivationBootID()
	api, err := newPluginRuntimeCoordinatorIdentity(hostname, extensions.PluginRuntimeProcessAPI, firstBoot)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := newPluginRuntimeCoordinatorIdentity(hostname, extensions.PluginRuntimeProcessWorker, secondBoot)
	if err != nil {
		t.Fatal(err)
	}
	if api.NodeID == "" || len([]byte(api.NodeID)) > 128 || api.NodeID != worker.NodeID ||
		api.ProcessRole != extensions.PluginRuntimeProcessAPI ||
		worker.ProcessRole != extensions.PluginRuntimeProcessWorker ||
		api.BootID == worker.BootID || api.BootID != firstBoot || worker.BootID != secondBoot {
		t.Fatalf("api=%#v worker=%#v", api, worker)
	}
	if _, err := newPluginRuntimeCoordinatorIdentity(" ", extensions.PluginRuntimeProcessAPI, firstBoot); !errors.Is(err, errPluginRuntimeCoordinatorBootstrapInvalid) {
		t.Fatalf("blank host error=%v", err)
	}
	if _, err := newPluginRuntimeCoordinatorIdentity("node", "unknown", firstBoot); !errors.Is(err, errPluginRuntimeCoordinatorBootstrapInvalid) {
		t.Fatalf("invalid role error=%v", err)
	}
}

func TestPluginRuntimeCoordinatorBootstrapStopIsBoundedAndIdempotent(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("bounded-stop", extensions.PluginRuntimeProcessWorker)
	cancelled := make(chan struct{})
	release := make(chan struct{})
	runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
		Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
				onReady()
				<-ctx.Done()
				close(cancelled)
				<-release
				return nil
			}), nil
		},
		StopTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stopErr := runtime.Stop(t.Context()); !errors.Is(stopErr, context.DeadlineExceeded) {
		t.Fatalf("bounded stop error=%v", stopErr)
	}
	waitPluginRuntimeCoordinatorBootstrapSignal(t, cancelled)
	close(release)
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runtime.Done())
	for range 4 {
		if stopErr := runtime.Stop(t.Context()); stopErr != nil {
			t.Fatalf("idempotent stop error=%v", stopErr)
		}
	}
}

func TestPluginRuntimeCoordinatorBootstrapConcurrentStopHasNoLeak(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("race-stop", extensions.PluginRuntimeProcessAPI)
	runnerDone := make(chan struct{})
	runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  newPluginRuntimeCoordinatorBootstrapTestEnsurer(),
		Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
				defer close(runnerDone)
				onReady()
				<-ctx.Done()
				return nil
			}), nil
		},
		StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 64
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsCh := make(chan error, callers)
	identities := make(chan extensions.PluginRuntimeNodeIdentity, callers)
	for range callers {
		go func() {
			defer wait.Done()
			identities <- runtime.Identity()
			errorsCh <- runtime.Stop(t.Context())
		}()
	}
	wait.Wait()
	close(errorsCh)
	close(identities)
	for stopErr := range errorsCh {
		if stopErr != nil {
			t.Fatalf("concurrent stop error=%v", stopErr)
		}
	}
	for got := range identities {
		if !reflect.DeepEqual(got, identity) {
			t.Fatalf("identity drift=%#v want=%#v", got, identity)
		}
	}
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runnerDone)
	waitPluginRuntimeCoordinatorBootstrapSignal(t, runtime.Done())
	if err, ok := <-runtime.Failures(); ok || err != nil {
		t.Fatalf("normal concurrent stop failure=%v open=%v", err, ok)
	}
}

func newPluginRuntimeCoordinatorBootstrapTestEnsurer() *pluginRuntimeCoordinatorBootstrapTestEnsurer {
	return &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
	}
}

func pluginRuntimeCoordinatorBootstrapTestPublication() extensions.PluginRuntimePublication {
	digest, err := extensions.PluginRuntimeMembersDigest(nil)
	if err != nil {
		panic(err)
	}
	return extensions.PluginRuntimePublication{
		Revision: 1, MemberCount: 0, MembersDigest: digest,
		Reason:    extensions.PluginRuntimePublicationStartupReconcile,
		CreatedAt: time.Now().UTC(),
	}
}

func pluginRuntimeCoordinatorBootstrapTestIdentity(
	label string,
	role extensions.PluginRuntimeProcessRole,
) extensions.PluginRuntimeNodeIdentity {
	identity, err := newPluginRuntimeCoordinatorIdentity(
		"test-node-"+label, role, extensions.NewActivationBootID(),
	)
	if err != nil {
		panic(err)
	}
	return identity
}

func waitPluginRuntimeCoordinatorBootstrapStart(
	t *testing.T,
	result <-chan pluginRuntimeCoordinatorBootstrapStartResult,
) pluginRuntimeCoordinatorBootstrapStartResult {
	t.Helper()
	select {
	case started := <-result:
		return started
	case <-time.After(2 * time.Second):
		t.Fatal("plugin runtime coordinator bootstrap did not return")
		return pluginRuntimeCoordinatorBootstrapStartResult{}
	}
}

func waitPluginRuntimeCoordinatorBootstrapSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin runtime coordinator bootstrap signal timed out")
	}
}

// 首轮就绪必须有界。开放中的 lifecycle operation 会让 fence 持续拒绝 apply；
// 若无界等待，API 就永久停在启动阶段：不监听端口、不打日志、不启插件，
// 调用方也拿不到错误、无法降级启动。
func TestPluginRuntimeCoordinatorBootstrapStartupWaitIsBounded(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("startup-deadline", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
	}
	pending := errors.New("extensions: plugin runtime lifecycle operation is pending")
	var runnerStopped atomic.Bool
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
				// 永不 onReady：等价于 fence 因开放中的 lifecycle operation 拒绝 apply。
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					<-ctx.Done()
					runnerStopped.Store(true)
					return nil
				}), nil
			},
			StartupWaitTimeout: 20 * time.Millisecond,
			PendingReason:      func() error { return pending },
			StopTimeout:        time.Second,
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()

	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.runtime != nil {
		t.Fatalf("runtime=%#v want nil after startup deadline", started.runtime)
	}
	if !errors.Is(started.err, errPluginRuntimeCoordinatorStartupPending) {
		t.Fatalf("error=%v want errPluginRuntimeCoordinatorStartupPending", started.err)
	}
	// 根因必须随错误返回，否则运维只知道超时、不知道是谁挡住了收敛。
	if started.err == nil || !strings.Contains(started.err.Error(), pending.Error()) {
		t.Fatalf("error=%v must carry pending reason %q", started.err, pending.Error())
	}
	if !runnerStopped.Load() {
		t.Fatal("startup deadline must stop the runner (fail-closed)")
	}
}

// 有界等待不能把「稍后就会就绪」的正常启动判死。
func TestPluginRuntimeCoordinatorBootstrapStartupWaitAllowsLateReadiness(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("startup-late-ready", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapTestEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
	}
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					timer := time.NewTimer(15 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return nil
					case <-timer.C:
					}
					onReady()
					<-ctx.Done()
					return nil
				}), nil
			},
			StartupWaitTimeout: time.Second,
			StopTimeout:        time.Second,
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()

	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.err != nil || started.runtime == nil || !started.runtime.Active() {
		t.Fatalf("runtime=%#v err=%v want active runtime", started.runtime, started.err)
	}
	if err := started.runtime.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// pluginRuntimeCoordinatorBootstrapReclaimingEnsurer 是实现了孤儿回收窄接口
// 的 genesis double：首次 Ensure 永远 in-progress，回收成功后开放态被清除，
// 后续 Ensure 才成功。未实现该接口的既有 double 行为保持不变。
type pluginRuntimeCoordinatorBootstrapReclaimingEnsurer struct {
	publication extensions.PluginRuntimePublication
	errs        []error

	ensureCalls  atomic.Int32
	reclaimCalls atomic.Int32
	reclaimErr   error
	// reclaimed 由回收函数翻转：false=开放态仍在（Ensure 继续报
	// in-progress），true=孤儿已回收（Ensure 下一次返回成功）。
	reclaimed atomic.Bool
}

func (ensurer *pluginRuntimeCoordinatorBootstrapReclaimingEnsurer) EnsureInitialPluginRuntimePublication(
	ctx context.Context,
) (extensions.PluginRuntimePublication, error) {
	if err := ctx.Err(); err != nil {
		return extensions.PluginRuntimePublication{}, err
	}
	call := ensurer.ensureCalls.Add(1)
	if int(call) <= len(ensurer.errs) {
		if err := ensurer.errs[call-1]; err != nil {
			return extensions.PluginRuntimePublication{}, err
		}
		return ensurer.publication, nil
	}
	if !ensurer.reclaimed.Load() {
		return extensions.PluginRuntimePublication{}, fmt.Errorf(
			"%w: initial plugin runtime publication waits for lifecycle completion",
			extensions.ErrLifecycleOperationInProgress,
		)
	}
	return ensurer.publication, nil
}

func (ensurer *pluginRuntimeCoordinatorBootstrapReclaimingEnsurer) ReclaimOrphanedLifecycleOperations(
	ctx context.Context,
	limit int,
) ([]int64, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ensurer.reclaimCalls.Add(1)
	if ensurer.reclaimErr != nil {
		return nil, ensurer.reclaimErr
	}
	ensurer.reclaimed.Store(true)
	return []int64{115}, nil
}

// 用例 8：genesis 遇到 in-progress 后先回收，注入的 reclaimer 清掉开放态 →
// ensure 在有界预算内成功，builder 只跑一次。
func TestPluginRuntimeCoordinatorBootstrapReclaimsOrphanBeforeGenesisRetry(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-orphan-reclaim", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapReclaimingEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
	}
	var buildCalls atomic.Int32
	var reclaimNotices atomic.Int32
	runtime, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  ensurer,
		Build: func(_ extensions.PluginRuntimeNodeIdentity, onReady func(), _ func(error)) (pluginRuntimeCoordinatorRunner, error) {
			buildCalls.Add(1)
			return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
				onReady()
				<-ctx.Done()
				return nil
			}), nil
		},
		// 1s 预算：成功路径由"回收清掉开放态"驱动，未耗尽即证明不靠超时兜底。
		GenesisWaitTimeout:   time.Second,
		GenesisRetryInterval: time.Millisecond,
		StopTimeout:          time.Second,
		OnOrphanReclaim: func(reclaimed []int64, err error) {
			if err != nil || len(reclaimed) != 1 || reclaimed[0] != 115 {
				t.Errorf("onOrphanReclaim reclaimed=%v err=%v", reclaimed, err)
			}
			reclaimNotices.Add(1)
		},
	})
	if err != nil || runtime == nil || !runtime.Active() {
		t.Fatalf("runtime=%#v err=%v ensure=%d reclaim=%d", runtime, err, ensurer.ensureCalls.Load(), ensurer.reclaimCalls.Load())
	}
	if ensurer.ensureCalls.Load() != 2 || ensurer.reclaimCalls.Load() != 1 ||
		buildCalls.Load() != 1 || reclaimNotices.Load() < 1 {
		t.Fatalf("ensure=%d reclaim=%d build=%d notices=%d",
			ensurer.ensureCalls.Load(), ensurer.reclaimCalls.Load(), buildCalls.Load(), reclaimNotices.Load())
	}
	if stopErr := runtime.Stop(t.Context()); stopErr != nil {
		t.Fatalf("stop: %v", stopErr)
	}
}

// 回收失败只影响日志，绝不改变重试行为：ensure 依旧按既有节奏重试并按预算
// 触顶（recovery-only 降级语义不变）。
func TestPluginRuntimeCoordinatorBootstrapReclaimFailureDoesNotChangeRetry(t *testing.T) {
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity("genesis-orphan-fail", extensions.PluginRuntimeProcessAPI)
	ensurer := &pluginRuntimeCoordinatorBootstrapReclaimingEnsurer{
		publication: pluginRuntimeCoordinatorBootstrapTestPublication(),
		reclaimErr:  errPluginRuntimeCoordinatorBootstrapTest,
	}
	var buildCalls atomic.Int32
	_, err := launchPluginRuntimeCoordinator(t.Context(), pluginRuntimeCoordinatorLaunchConfig{
		Identity: identity,
		Ensurer:  ensurer,
		Build: func(extensions.PluginRuntimeNodeIdentity, func(), func(error)) (pluginRuntimeCoordinatorRunner, error) {
			buildCalls.Add(1)
			t.Fatal("builder must not run while genesis keeps failing")
			return nil, nil
		},
		GenesisWaitTimeout:   50 * time.Millisecond,
		GenesisRetryInterval: time.Millisecond,
	})
	if !errors.Is(err, extensions.ErrLifecycleOperationInProgress) {
		t.Fatalf("error=%v want ErrLifecycleOperationInProgress root", err)
	}
	if buildCalls.Load() != 0 {
		t.Fatalf("build calls=%d", buildCalls.Load())
	}
	if ensurer.reclaimCalls.Load() < 2 {
		t.Fatalf("reclaim calls=%d, want repeated attempts across retries", ensurer.reclaimCalls.Load())
	}
}
