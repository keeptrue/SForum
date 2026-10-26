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
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

const (
	defaultPluginRuntimeCoordinatorStopTimeout = 10 * time.Second
	// 启动时若 lifecycle 操作尚未结束，genesis ensure 可短暂重试；超时后仍
	// fail-closed，绝不跳过开放中的 lifecycle 行。
	defaultPluginRuntimeCoordinatorGenesisWaitTimeout   = 30 * time.Second
	defaultPluginRuntimeCoordinatorGenesisRetryInterval = 200 * time.Millisecond
	// 首轮收敛就绪同样必须有界。开放中的 lifecycle operation 会让 fence 持续
	// 拒绝 apply，若这里无限等待，调用方就再也拿不到错误、也不会进入
	// recovery-only 模式，进程只会静默挂起（无监听、无日志、无插件进程）。
	defaultPluginRuntimeCoordinatorStartupWaitTimeout = 30 * time.Second
	// 每轮 genesis 重试前回收孤儿 lifecycle operation 的批量上限；与仓库层
	// 默认一致（16，仓库层自身还有 64 的硬上限）。
	defaultPluginRuntimeOrphanReclaimLimit = 16
)

// pluginRuntimeLifecycleOrphanReclaimer 是可选的孤儿回收窄接口：只有生产
// PostgresStore 实现它，测试 double 不实现时行为与既有重试循环完全一致。
type pluginRuntimeLifecycleOrphanReclaimer interface {
	ReclaimOrphanedLifecycleOperations(context.Context, int) ([]int64, error)
}

var (
	errPluginRuntimeCoordinatorBootstrapInvalid   = errors.New("bootstrap: plugin runtime coordinator is invalid")
	errPluginRuntimeCoordinatorStoppedBeforeReady = errors.New("bootstrap: plugin runtime coordinator stopped before readiness")
	// errPluginRuntimeCoordinatorStartupPending 是「有界等待到期仍未就绪」的
	// 类型化信号：调用方据此进入 recovery-only 而不是终止进程。
	errPluginRuntimeCoordinatorStartupPending = errors.New("bootstrap: plugin runtime coordinator did not converge before the startup deadline")
)

// pluginRuntimeCoordinatorRunner keeps the bootstrap lifetime wrapper testable
// without weakening the production constructor's exact concrete dependencies.
type pluginRuntimeCoordinatorRunner interface {
	Run(context.Context) error
}

type pluginRuntimeCoordinatorBuilder func(
	extensions.PluginRuntimeNodeIdentity,
	func(),
	func(error),
) (pluginRuntimeCoordinatorRunner, error)

type pluginRuntimeCoordinatorBootstrapConfig struct {
	SafeMode    bool
	ProcessRole extensions.PluginRuntimeProcessRole
	Store       *extensions.PostgresStore
	Manager     *extensionsruntime.Manager
	Logger      *slog.Logger

	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	StopTimeout       time.Duration
	TierOrder         []string
	// StartupWaitTimeout 是首轮收敛就绪的上界；<=0 用生产默认。
	StartupWaitTimeout time.Duration
	// AIToolRefresh 是期望集应用成功之后的 AI 工具登记表重建槽位。它可以为 nil
	// （测试与 Safe Mode），此时协调器行为与从前完全一致。
	AIToolRefresh *aiToolRefreshHook
}

type pluginRuntimeCoordinatorLaunchConfig struct {
	SafeMode    bool
	Identity    extensions.PluginRuntimeNodeIdentity
	Ensurer     extensions.InitialPluginRuntimePublicationEnsurer
	Build       pluginRuntimeCoordinatorBuilder
	StopTimeout time.Duration
	// GenesisWaitTimeout 限制 ErrLifecycleOperationInProgress 的重试总时长；
	// <=0 时使用生产默认（约 30s）。测试可注入极小值做确定性截止。
	GenesisWaitTimeout time.Duration
	// GenesisRetryInterval 是两次 genesis ensure 之间的等待；<=0 用生产默认。
	GenesisRetryInterval time.Duration
	// StartupWaitTimeout 限制 builder/runner 启动后「首轮就绪」的总时长；
	// <=0 用生产默认（约 30s）。到期返回 errPluginRuntimeCoordinatorStartupPending，
	// 由调用方降级启动，绝不无限等待。
	StartupWaitTimeout time.Duration
	// PendingReason 返回最近一次未就绪的原因（通常为开放中的 lifecycle
	// operation），仅用于把根因带进启动超时错误。
	PendingReason func() error
	// OnOrphanReclaim 上报每轮 genesis 重试前的孤儿回收结果：
	// reclaimed 为回收成功的 operation id；err 非空表示回收本身失败
	// （失败只告警，绝不改变重试节奏）。nil 时回收仍执行、只是不留日志。
	OnOrphanReclaim func(reclaimed []int64, err error)
}

// pluginRuntimeCoordinatorRuntime owns the coordinator goroutine. Failures is
// the fail-closed process signal: a non-nil value means the durable boot lease
// can no longer authorize this process-local Manager.
type pluginRuntimeCoordinatorRuntime struct {
	identity    extensions.PluginRuntimeNodeIdentity
	active      bool
	stopTimeout time.Duration

	cancel   context.CancelFunc
	done     chan struct{}
	failures chan error
	stopOnce sync.Once
	stopping atomic.Bool
	ready    atomic.Bool

	errMu sync.RWMutex
	err   error
}

// startPluginRuntimeCoordinator constructs the production exact full-set
// coordinator. Safe Mode returns before hostname lookup, genesis import, node
// registration, LISTEN, or any plugin process start.
func startPluginRuntimeCoordinator(
	ctx context.Context,
	config pluginRuntimeCoordinatorBootstrapConfig,
) (*pluginRuntimeCoordinatorRuntime, error) {
	if ctx == nil {
		return nil, errPluginRuntimeCoordinatorBootstrapInvalid
	}
	if config.SafeMode {
		return newInactivePluginRuntimeCoordinatorRuntime(), nil
	}
	if config.Store == nil || config.Manager == nil ||
		(config.ProcessRole != extensions.PluginRuntimeProcessAPI &&
			config.ProcessRole != extensions.PluginRuntimeProcessWorker) {
		return nil, errPluginRuntimeCoordinatorBootstrapInvalid
	}

	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("resolve plugin runtime node hostname: %w", err)
	}
	identity, err := newPluginRuntimeCoordinatorIdentity(
		hostname, config.ProcessRole, extensions.NewActivationBootID(),
	)
	if err != nil {
		return nil, err
	}

	report := func(message string, err error) {
		if err != nil && config.Logger != nil {
			config.Logger.Warn(message, "error", err)
		}
	}
	// pending 状态跨 Build 闭包与 launch 的启动等待共享：日志按原因去重，
	// 最新原因用于有界超时时的错误信息。
	var pendingMu sync.Mutex
	var lastPending error
	reportPending := func(err error) {
		if err == nil {
			return
		}
		pendingMu.Lock()
		changed := lastPending == nil || lastPending.Error() != err.Error()
		if changed {
			lastPending = err
		}
		pendingMu.Unlock()
		if changed {
			report("plugin runtime convergence is pending", err)
		}
	}
	pendingReason := func() error {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		return lastPending
	}
	return launchPluginRuntimeCoordinator(ctx, pluginRuntimeCoordinatorLaunchConfig{
		Identity:           identity,
		Ensurer:            config.Store,
		StopTimeout:        config.StopTimeout,
		StartupWaitTimeout: config.StartupWaitTimeout,
		PendingReason:      pendingReason,
		OnOrphanReclaim: func(reclaimed []int64, err error) {
			if config.Logger == nil {
				return
			}
			if err != nil {
				config.Logger.Warn("plugin runtime orphan lifecycle reclaim failed; continuing genesis retry", "error", err)
				return
			}
			if len(reclaimed) > 0 {
				config.Logger.Info("reclaimed orphaned lifecycle operations before genesis retry", "operationIds", reclaimed)
			}
		},
		Build: func(
			identity extensions.PluginRuntimeNodeIdentity,
			onReady func(),
			onError func(error),
		) (pluginRuntimeCoordinatorRunner, error) {
			// API 与 standalone worker 共用同一个 Protocol V2 full-set 收敛路径。
			productionApplier, err := newProductionPluginRuntimeFullSetApplier(
				config.Manager, config.Store,
				config.TierOrder,
			)
			if err != nil {
				return nil, fmt.Errorf("construct exact plugin runtime full-set applier: %w", err)
			}
			// 期望集应用成功后，顺带重建 AI 工具登记表：插件的启用/停用/升级因此
			// 不必等一次 API 重启才被模型看见。装饰器只加一个 after-apply 动作，
			// 收敛语义（fence、ack、失败判定）全部留在原适配器里。
			applier := withAIToolRefreshApplier(productionApplier, config.AIToolRefresh)
			notifications := extensions.NewPostgresPluginRuntimeNotifications(
				config.Store,
				extensions.DefaultPluginRuntimeReconnectDelay,
				func(err error) { report("plugin runtime LISTEN degraded; durable polling remains active", err) },
			)
			return extensions.NewPluginRuntimeCoordinator(
				config.Store,
				applier,
				notifications,
				extensions.PluginRuntimeCoordinatorConfig{
					Identity:          identity,
					LeaseDuration:     config.LeaseDuration,
					HeartbeatInterval: config.HeartbeatInterval,
					PollInterval:      config.PollInterval,
					OnReady:           onReady,
					OnPending:         reportPending,
					OnError: func(err error) {
						report("plugin runtime convergence degraded", err)
						onError(err)
					},
				},
			)
		},
	})
}

func launchPluginRuntimeCoordinator(
	ctx context.Context,
	config pluginRuntimeCoordinatorLaunchConfig,
) (*pluginRuntimeCoordinatorRuntime, error) {
	if ctx == nil {
		return nil, errPluginRuntimeCoordinatorBootstrapInvalid
	}
	if config.SafeMode {
		return newInactivePluginRuntimeCoordinatorRuntime(), nil
	}
	if config.Ensurer == nil || config.Build == nil ||
		strings.TrimSpace(config.Identity.NodeID) == "" ||
		strings.TrimSpace(config.Identity.BootID) == "" ||
		(config.Identity.ProcessRole != extensions.PluginRuntimeProcessAPI &&
			config.Identity.ProcessRole != extensions.PluginRuntimeProcessWorker) {
		return nil, errPluginRuntimeCoordinatorBootstrapInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Immutable genesis 必须先成功：builder/runner 不得在 fence 通过前启动。
	// 仅对 ErrLifecycleOperationInProgress 做有界、可取消重试；其它错误立即失败。
	if _, err := ensurePluginRuntimeCoordinatorGenesis(ctx, config); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ready := make(chan struct{})
	startupErrors := make(chan error, 1)
	var readyOnce sync.Once
	runtime := &pluginRuntimeCoordinatorRuntime{
		identity:    config.Identity,
		active:      true,
		stopTimeout: normalizedPluginRuntimeCoordinatorStopTimeout(config.StopTimeout),
		done:        make(chan struct{}),
		failures:    make(chan error, 1),
	}
	runner, err := config.Build(
		config.Identity,
		func() {
			if runtime.stopping.Load() {
				return
			}
			readyOnce.Do(func() {
				runtime.ready.Store(true)
				close(ready)
			})
		},
		func(err error) {
			if err == nil || runtime.ready.Load() || runtime.stopping.Load() {
				return
			}
			select {
			case startupErrors <- err:
			default:
			}
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct plugin runtime coordinator: %w", err)
	}
	if runner == nil {
		return nil, errPluginRuntimeCoordinatorBootstrapInvalid
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runtime.cancel = cancel
	go runtime.run(runCtx, runner)

	abort := func(cause error) (*pluginRuntimeCoordinatorRuntime, error) {
		stopCtx, stopCancel := context.WithTimeout(
			context.Background(), runtime.stopTimeout,
		)
		defer stopCancel()
		if stopErr := runtime.Stop(stopCtx); stopErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("stop plugin runtime coordinator after startup failure: %w", stopErr))
		}
		return nil, cause
	}

	// 首轮就绪必须有界：无界等待会让 API 永久停在启动阶段（不监听端口、
	// 不打日志、不启插件），调用方连降级启动的机会都没有。
	startupDeadline := time.NewTimer(
		normalizedPluginRuntimeCoordinatorStartupWaitTimeout(config.StartupWaitTimeout),
	)
	defer startupDeadline.Stop()

	select {
	case <-ready:
		// 首轮失败可能已入队，而 coordinator 随即重试成功；启动边界仍应
		// 返回第一次错误，让进程重新走一遍干净的 genesis/boot lease。
		select {
		case startupErr := <-startupErrors:
			return abort(fmt.Errorf("initial plugin runtime convergence: %w", startupErr))
		default:
		}
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		// Readiness and terminal failure may race. Never return a handle that has
		// already lost its lease or stopped before the caller can install a monitor.
		select {
		case <-runtime.done:
			if runErr := runtime.Err(); runErr != nil {
				return nil, runErr
			}
			return nil, errPluginRuntimeCoordinatorStoppedBeforeReady
		default:
			return runtime, nil
		}
	case startupErr := <-startupErrors:
		return abort(fmt.Errorf("initial plugin runtime convergence: %w", startupErr))
	case <-runtime.done:
		if runErr := runtime.Err(); runErr != nil {
			return nil, runErr
		}
		return nil, errPluginRuntimeCoordinatorStoppedBeforeReady
	case <-startupDeadline.C:
		// 有界等待到期：fail-closed 停止协调器，把根因交给调用方降级启动。
		return abort(startupPendingError(config))
	case <-ctx.Done():
		return abort(ctx.Err())
	}
}

// startupPendingError 把「首轮未就绪」转成可诊断错误：带上最近一次 pending
// 原因（通常是开放中的 lifecycle operation），避免只有超时没有根因。
func startupPendingError(config pluginRuntimeCoordinatorLaunchConfig) error {
	if config.PendingReason != nil {
		if reason := config.PendingReason(); reason != nil {
			return fmt.Errorf("%w: %w", errPluginRuntimeCoordinatorStartupPending, reason)
		}
	}
	return errPluginRuntimeCoordinatorStartupPending
}

func (runtime *pluginRuntimeCoordinatorRuntime) run(
	ctx context.Context,
	runner pluginRuntimeCoordinatorRunner,
) {
	err := runner.Run(ctx)
	if err == nil && !runtime.stopping.Load() {
		err = errPluginRuntimeCoordinatorStoppedBeforeReady
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

func (runtime *pluginRuntimeCoordinatorRuntime) Stop(ctx context.Context) error {
	if runtime == nil || !runtime.active {
		return nil
	}
	if ctx == nil {
		return errPluginRuntimeCoordinatorBootstrapInvalid
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

func (runtime *pluginRuntimeCoordinatorRuntime) Failures() <-chan error {
	if runtime == nil {
		return nil
	}
	return runtime.failures
}

func (runtime *pluginRuntimeCoordinatorRuntime) Done() <-chan struct{} {
	if runtime == nil {
		return nil
	}
	return runtime.done
}

func (runtime *pluginRuntimeCoordinatorRuntime) Err() error {
	if runtime == nil {
		return nil
	}
	runtime.errMu.RLock()
	defer runtime.errMu.RUnlock()
	return runtime.err
}

func (runtime *pluginRuntimeCoordinatorRuntime) Identity() extensions.PluginRuntimeNodeIdentity {
	if runtime == nil {
		return extensions.PluginRuntimeNodeIdentity{}
	}
	return runtime.identity
}

func (runtime *pluginRuntimeCoordinatorRuntime) Active() bool {
	return runtime != nil && runtime.active
}

func newInactivePluginRuntimeCoordinatorRuntime() *pluginRuntimeCoordinatorRuntime {
	done := make(chan struct{})
	failures := make(chan error)
	close(done)
	close(failures)
	return &pluginRuntimeCoordinatorRuntime{done: done, failures: failures}
}

func newPluginRuntimeCoordinatorIdentity(
	hostname string,
	role extensions.PluginRuntimeProcessRole,
	bootID string,
) (extensions.PluginRuntimeNodeIdentity, error) {
	nodeID := strings.TrimSpace(hostname)
	bootID = strings.TrimSpace(bootID)
	if nodeID == "" || bootID == "" || len([]byte(bootID)) > 128 ||
		(role != extensions.PluginRuntimeProcessAPI && role != extensions.PluginRuntimeProcessWorker) {
		return extensions.PluginRuntimeNodeIdentity{}, errPluginRuntimeCoordinatorBootstrapInvalid
	}
	if len([]byte(nodeID)) > 128 {
		digest := sha256.Sum256([]byte(nodeID))
		nodeID = "host-" + hex.EncodeToString(digest[:])
	}
	return extensions.PluginRuntimeNodeIdentity{
		NodeID: nodeID, ProcessRole: role, BootID: bootID,
	}, nil
}

func normalizedPluginRuntimeCoordinatorStopTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultPluginRuntimeCoordinatorStopTimeout
	}
	return value
}

func normalizedPluginRuntimeCoordinatorGenesisWaitTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultPluginRuntimeCoordinatorGenesisWaitTimeout
	}
	return value
}

func normalizedPluginRuntimeCoordinatorStartupWaitTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultPluginRuntimeCoordinatorStartupWaitTimeout
	}
	return value
}

func normalizedPluginRuntimeCoordinatorGenesisRetryInterval(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultPluginRuntimeCoordinatorGenesisRetryInterval
	}
	return value
}

// ensurePluginRuntimeCoordinatorGenesis 在启动 builder/runner 之前完成 immutable
// genesis ensure。仅 lifecycle in-progress 可在 waitTimeout 内按 interval 重试。
func ensurePluginRuntimeCoordinatorGenesis(
	ctx context.Context,
	config pluginRuntimeCoordinatorLaunchConfig,
) (extensions.PluginRuntimePublication, error) {
	if ctx == nil || config.Ensurer == nil {
		return extensions.PluginRuntimePublication{}, errPluginRuntimeCoordinatorBootstrapInvalid
	}
	waitTimeout := normalizedPluginRuntimeCoordinatorGenesisWaitTimeout(config.GenesisWaitTimeout)
	retryInterval := normalizedPluginRuntimeCoordinatorGenesisRetryInterval(config.GenesisRetryInterval)
	deadline := time.Now().Add(waitTimeout)

	var lastInProgress error
	for {
		if err := ctx.Err(); err != nil {
			return extensions.PluginRuntimePublication{}, err
		}

		publication, err := config.Ensurer.EnsureInitialPluginRuntimePublication(ctx)
		if err == nil {
			if !validPluginRuntimeCoordinatorGenesis(publication) {
				return extensions.PluginRuntimePublication{}, fmt.Errorf(
					"%w: genesis publication is not exact",
					errPluginRuntimeCoordinatorBootstrapInvalid,
				)
			}
			return publication, nil
		}
		if !errors.Is(err, extensions.ErrLifecycleOperationInProgress) {
			return extensions.PluginRuntimePublication{}, fmt.Errorf(
				"ensure initial plugin runtime publication: %w", err,
			)
		}
		lastInProgress = err

		// 每轮重试前先尝试回收孤儿操作：执行进程中途崩溃会让 durable 行永远
		// 停在开放态，正常重试永远等不到它结束。回收本身 fail-closed——只关闭
		// 所有 lease 均已过期的操作，失败仅告警，不改变重试节奏与 30s 预算。
		if reclaimer, ok := config.Ensurer.(pluginRuntimeLifecycleOrphanReclaimer); ok {
			reclaimed, reclaimErr := reclaimer.ReclaimOrphanedLifecycleOperations(
				ctx, defaultPluginRuntimeOrphanReclaimLimit,
			)
			if config.OnOrphanReclaim != nil {
				config.OnOrphanReclaim(reclaimed, reclaimErr)
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return extensions.PluginRuntimePublication{}, fmt.Errorf(
				"ensure initial plugin runtime publication: %w", lastInProgress,
			)
		}
		wait := retryInterval
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return extensions.PluginRuntimePublication{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func validPluginRuntimeCoordinatorGenesis(publication extensions.PluginRuntimePublication) bool {
	if publication.Revision <= 0 || publication.MemberCount != len(publication.Members) ||
		publication.CreatedAt.IsZero() || publication.ActorUserID < 0 {
		return false
	}
	digest, err := extensions.PluginRuntimeMembersDigest(publication.Members)
	return err == nil && digest == publication.MembersDigest
}

// newProductionPluginRuntimeFullSetApplier 是 API 与 worker 共用的 production
// Protocol V2 full-set 适配器构造点。
func newProductionPluginRuntimeFullSetApplier(
	manager *extensionsruntime.Manager,
	inventory extensionsruntime.PluginRuntimeFullSetInventory,
	tierOrder ...[]string,
) (*extensionsruntime.ManagerPluginRuntimeFullSetApplier, error) {
	return extensionsruntime.NewManagerPluginRuntimeFullSetApplier(manager, inventory, tierOrder...)
}

// aiToolRefreshApplier 在期望集应用成功之后触发 AI 工具登记表重建。
//
// 用装饰而不是改写适配器：应用语义（exact artifact 解析、Manager 级 transition
// barrier、applied 证据）属于运行时收敛，AI 登记表只是它的一个下游读者。失败
// 语义也刻意分开——应用失败原样返回，登记表重建失败只降级为「旧表 + 告警」。
type aiToolRefreshApplier struct {
	inner extensions.PluginRuntimeFullSetApplier
	hook  *aiToolRefreshHook
}

var _ extensions.PluginRuntimeFullSetApplier = (*aiToolRefreshApplier)(nil)

// withAIToolRefreshApplier 在 hook 为空时原样返回适配器：没有重建需求时不额外
// 加一层间接，测试里对 applier 的类型与行为断言因此保持原状。
func withAIToolRefreshApplier(
	inner extensions.PluginRuntimeFullSetApplier,
	hook *aiToolRefreshHook,
) extensions.PluginRuntimeFullSetApplier {
	if inner == nil || hook == nil {
		return inner
	}
	return &aiToolRefreshApplier{inner: inner, hook: hook}
}

func (a *aiToolRefreshApplier) ApplyPluginRuntimeFullSet(
	ctx context.Context,
	publication extensions.PluginRuntimePublication,
) ([]extensions.PluginRuntimeAppliedMember, error) {
	applied, err := a.inner.ApplyPluginRuntimeFullSet(ctx, publication)
	if err != nil {
		return applied, err
	}
	a.hook.AfterApply(ctx, publication)
	return applied, nil
}
