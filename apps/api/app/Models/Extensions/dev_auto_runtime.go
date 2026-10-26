package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/systemactor"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// DevAutoRuntime 是「彻底 0 命令」dev 模式的 host 侧自动激活服务。
//
// 语义（操作者 2026-10-17 拍板，见 knowledge/decisions/2026-10-17-dev-zero-command.md）：
// APP_ENV=development 且非 Safe Mode 时，dev 来源根（EXTERNAL_EXTENSION_ROOTS ∪
// extensions/dev 脚手架树）下的插件由 host 自己完成 安装→启用→逐摘要升级，
// 不需要任何登录身份、信任会话或人工确认。生产装配不注入本服务，全部路径
// fail closed（super_admin 门原样）。
//
// 合成身份：dev-auto 系统账号（无密码凭据，不可登录）。lifecycle/authority/audit
// 链要求合法 UserID（V2 authority 多处 ActorUserID>0 校验），因此不用 ID=0。
// 该账号在管理端可见，是「dev 自动机」的显式审计归因；在管理端禁用/删除它
// 即等于关闭本通道。
const (
	DevAutoUsername    = "dev-auto"
	DevAutoEmail       = "dev-auto@sforum.invalid"
	DevAutoDisplayName = "SForum dev auto"
)

// devAutoMaxFailedUpgradeAttempts 限制 dev 自动机对同一冻结制品的自动恢复次数。
// lifecycle 的 attempt 是「这份制品被真正执行过几次」，一次自动恢复 + 一次重试
// 就 +1；不设上限时任何确定性失败都会被文件事件喂成无限重试（现场曾把同一
// upgrade operation 推到 attempt=98、97 条恢复决定）。作者产出新摘要即重新计数。
const devAutoMaxFailedUpgradeAttempts = 3

// devAutoRecoveryScanLimit 是查找「当前摘要的失败升级」时回看的操作条数。
const devAutoRecoveryScanLimit = 5

// errDevAutoGiveUp 表示 dev 自动机主动放弃一次激活：同一冻结制品再重试没有意义。
// 调用方只记一条台账，不再恢复、不再重试。
var errDevAutoGiveUp = errors.New("dev auto: giving up on staged artifact")

// DevIdentityStore 是本服务需要的最小身份写入边界（identity.Store 的窄投影）。
type DevIdentityStore interface {
	identity.ActorStore
	AnyUserExists(ctx context.Context) (bool, error)
	GetCurrentUserByEmail(ctx context.Context, email string) (identity.CurrentUser, error)
	GetCredentialByLogin(ctx context.Context, login string) (identity.CredentialUser, error)
	WithBootstrapTx(ctx context.Context, fn func(context.Context, identity.TxStore) error) error
}

// DevAutoPromoter 是升级/启用的窄边界（*Service 自身实现，包内直连避免环）。
type DevAutoPromoter interface {
	Enable(ctx context.Context, actor identity.Actor, id string, input EnableInput) (Extension, error)
	Upgrade(ctx context.Context, actor identity.Actor, id string, input UpgradeInput) (Extension, error)
	// 上一次失败的 upgrade 会留下 terminal-failed operation，fence 使下一次
	// 升级报 requires-explicit-retry；dev 通道自动走一轮 recovery(retry)。
	LifecycleOperations(ctx context.Context, actor identity.Actor, extensionID string, limit int) ([]LifecycleOperationSummary, error)
	RecoverLifecycleOperation(ctx context.Context, actor identity.Actor, extensionID string, operationID int64, input LifecycleRecoveryInput) (LifecycleOperationDetail, error)
}

// DevTrustGrantWriter 是 exact live grant 的写入边界（*ExecutableTrustService）。
type DevTrustGrantWriter interface {
	EnsureLiveGrantForDevAuto(ctx context.Context, actor identity.Actor, extension Extension) error
}

type DevAutoRuntime struct {
	service    *Service
	promoter   DevAutoPromoter
	identities DevIdentityStore
	grants     DevTrustGrantWriter
	// sourceRoots 与 CatalogService.externalRoots 同源；用于日志与防御性过滤。
	sourceRoots []string
	logger      *slog.Logger
	// operatorUser 缓存 dev-auto 账号 id（进程内；不存在则惰性创建）。
	operatorUserID int64
	// giveUpNotes 记录已写过「放弃」台账的 (extensionID, digest)，避免每轮文件
	// 事件重复刷同一条记录。watcher 单协程驱动，仍加锁以免 PromoteAll 被并发调用。
	giveUpMu    sync.Mutex
	giveUpNotes map[string]struct{}
}

func NewDevAutoRuntime(
	service *Service,
	promoter DevAutoPromoter,
	identities DevIdentityStore,
	grants DevTrustGrantWriter,
	sourceRoots []string,
	logger *slog.Logger,
) *DevAutoRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	return &DevAutoRuntime{
		service: service, promoter: promoter, identities: identities,
		grants: grants, sourceRoots: sourceRoots, logger: logger,
	}
}

// PromoteAll 是 watcher 的唯一入口：重扫外部源 → 对每个 dev 来源包确保
// grant + 启用/升级。单包失败只记日志，不中断其余包（dev 容错语义）。
func (r *DevAutoRuntime) PromoteAll(ctx context.Context) {
	if r == nil || r.service == nil {
		return
	}
	result, err := r.service.SyncExternalSources(ctx)
	if err != nil {
		r.logger.WarnContext(ctx, "dev auto: external source resync failed", "error", err)
		return
	}
	for _, diagnostic := range result.Diagnostics {
		r.logger.WarnContext(ctx, "dev auto: external source skipped",
			"code", diagnostic.Code, "packagePath", diagnostic.PackagePath,
			"extensionId", diagnostic.ExtensionID, "reason", diagnostic.Message)
	}
	items := append([]Extension(nil), result.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	promote := func(items []Extension) []Extension {
		var failed []Extension
		for _, item := range items {
			if err := r.promoteOne(ctx, item.ID); err != nil {
				r.logger.WarnContext(ctx, "dev auto: promote failed",
					"extensionId", item.ID, "error", err)
				// 失败落台账：air 场景下 watcher 日志可能进了交互终端，
				// 台账是作者唯一确定可见的地方。
				_, _ = r.service.store.CreateEvent(ctx, EventInput{
					ExtensionID: item.ID,
					Action:      EventEnabled,
					Message: fmt.Sprintf("dev auto promote failed (%s @ %s): %s",
						item.ID, item.StagedVersionOrActiveDigestForDev(), err.Error()),
				})
				failed = append(failed, item)
				continue
			}
		}
		return failed
	}
	failed := promote(items)
	if len(failed) > 0 {
		// 一轮立即重试：preflight 失败常因快照竞态（构建/暂存窗口）——重跑
		// SyncExternalSources 会重新快照一致态并产生新 staged digest，孤儿行
		// 自然被绕开。重试仍失败才交给下一次文件事件。
		retry, err := r.service.SyncExternalSources(ctx)
		if err == nil {
			promote(retry.Items)
		}
	}
}

func (r *DevAutoRuntime) promoteOne(ctx context.Context, extensionID string) error {
	actor, err := r.ensureDevAutoActor(ctx)
	if err != nil {
		return fmt.Errorf("ensure dev-auto actor: %w", err)
	}
	extension, err := r.service.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return err
	}
	if extension.Source == SourceBuiltin || extension.IsSystem {
		return nil
	}
	// 操作者显式禁用的包尊重其状态：不 grant、不升级（重新启用即恢复自动化）。
	if extension.Status == StatusDisabled {
		return nil
	}
	// 精确制品 grant：dev 直通的信任语义仍然是「逐摘要」，只是执行者是 dev 自动机。
	// 精确制品 grant 按原始 staged 视图写入，不做任何视图改写：作者在 manifest
	// 里声明的组件契约版本必须与它引用的 L2/props 制品同步变更（见
	// validateComponentUpgrade），dev 通道不会替作者晋升契约版本。
	if RequiresExecutableTrust(extension) && r.grants != nil {
		target := extension
		if staged, ok := extension.StagedArtifact(); ok && extension.Status != StatusDisabled {
			target = staged
		}
		if err := r.grants.EnsureLiveGrantForDevAuto(ctx, actor, target); err != nil {
			return fmt.Errorf("ensure dev grant: %w", err)
		}
	}
	switch extension.Status {
	case StatusInstalled:
		// Enable 也要过 lifecycle coordinator，而它要求稳定幂等键——与 Upgrade
		// 同一条规则。键由 extensionID + 目标摘要派生：同一制品重放安全，换摘要
		// 即新键。缺了它，任何声明 lifecycle V2 的新装插件都无法被 dev auto 启用
		// （表现为状态一直停在 installed，只有台账里一条 promote failed）。
		_, err = r.promoter.Enable(ctx, actor, extension.ID, EnableInput{
			ConfirmCapabilities: true,
			IdempotencyKey:      DevIdempotencyKey(extension.ID, extension.StagedVersionOrActiveDigestForDev()),
		})
	case StatusEnabled:
		if extension.StagedVersion == nil {
			return nil
		}
		if !usesLifecycleV2(extension) {
			// 非 V2 包（未声明 lifecycle）没有可编程的 staged 升级路径；硬调
			// Upgrade 只会得到误导性错误。如实告知作者需走管理端 restart bridge。
			return fmt.Errorf("staged digest %s requires a manual admin restart bridge (extension does not declare lifecycle V2)",
				extension.StagedVersion.PackageDigest)
		}
		// V2 upgrade 要求 stable Idempotency-Key（HTTP 从请求头取）。

		// extensionID+stagedDigest 派生确定性键：同摘要重放安全，新摘要即新键。
		stagedDigest := extension.StagedVersion.PackageDigest
		idempotencyKey := DevIdempotencyKey(extension.ID, stagedDigest)
		_, err = r.promoter.Upgrade(ctx, actor, extension.ID, UpgradeInput{
			IdempotencyKey: idempotencyKey,
		})
		if errors.Is(err, ErrLifecycleCoordinatorRetryRequired) {
			// 先把闸设在这里：不是所有终态失败都值得自动恢复。冻结制品不可装载
			// 时每一轮重试都只会复现同一失败，必须直接放弃（见
			// errDevAutoGiveUp 与 devAutoMaxFailedUpgradeAttempts 的注释）。
			staged, ok := extension.StagedArtifact()
			if !ok {
				r.noteDevAutoGiveUp(ctx, extension, stagedDigest,
					fmt.Errorf("%w: staged artifact %s is not selectable", ErrArtifactUnusable, stagedDigest))
				return nil
			}
			if usableErr := stagedArtifactUsable(staged); usableErr != nil {
				r.noteDevAutoGiveUp(ctx, extension, stagedDigest, usableErr)
				return nil
			}
			if recoverErr := r.recoverLatestFailedUpgrade(ctx, actor, extension.ID, stagedDigest); recoverErr != nil {
				if errors.Is(recoverErr, errDevAutoGiveUp) {
					r.noteDevAutoGiveUp(ctx, extension, stagedDigest, recoverErr)
					return nil
				}
				return fmt.Errorf("auto recovery of last failed upgrade: %w", recoverErr)
			}
			_, err = r.promoter.Upgrade(ctx, actor, extension.ID, UpgradeInput{
				IdempotencyKey: idempotencyKey,
			})
		}
	default:
		return nil
	}
	if err != nil {
		return err
	}
	r.logger.InfoContext(ctx, "dev auto: extension promoted",
		"extensionId", extension.ID, "digest", func() string {
			if extension.StagedVersion != nil {
				return extension.StagedVersion.PackageDigest
			}
			return extension.PackageDigest
		}())
	return nil
}

// recoverLatestFailedUpgrade 对最近一次属于当前冻结摘要的 terminal-failed 或
// terminal-cancelled upgrade 执行一轮 decision=retry recovery，清除 fence 使下一次
// 升级可以继续。coordinator 对这两种终态都要求显式 retry（见 LifecycleCoordinator.Run），
// Recovery 服务也接受两者——这里若只认 failed，cancelled 的升级会让 dev auto
// 永远撞 ErrLifecycleCoordinatorRetryRequired 且找不到可恢复对象。
//
// 两个新闸门（现场依据：operation 697 曾在同一摘要上被自动恢复 97 次）：
//   - 只恢复 PackageDigest 等于当前 staged 摘要的操作，避免回头恢复别的制品的
//     历史失败；
//   - AttemptCount 达到 devAutoMaxFailedUpgradeAttempts 即放弃，不再自动恢复。
func (r *DevAutoRuntime) recoverLatestFailedUpgrade(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	stagedDigest string,
) error {
	operations, err := r.promoter.LifecycleOperations(ctx, actor, extensionID, devAutoRecoveryScanLimit)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.Operation != string(LifecycleMachineUpgrade) {
			continue
		}
		if operation.TerminalResult != LifecycleTerminalFailed && operation.TerminalResult != LifecycleTerminalCancelled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(operation.PackageDigest), strings.TrimSpace(stagedDigest)) {
			continue
		}
		if operation.AttemptCount >= devAutoMaxFailedUpgradeAttempts {
			return fmt.Errorf("%w: upgrade of %s already ran %d time(s) without success",
				errDevAutoGiveUp, stagedDigest, operation.AttemptCount)
		}
		_, err := r.promoter.RecoverLifecycleOperation(ctx, actor, extensionID, operation.ID, LifecycleRecoveryInput{
			Decision: "retry", Reason: "dev auto activation retry after failed or cancelled upgrade",
		})
		return err
	}
	return fmt.Errorf("%w: no failed upgrade operation for staged artifact %s", errDevAutoGiveUp, stagedDigest)
}

// noteDevAutoGiveUp 记录一次主动放弃：日志 + 每个冻结摘要只写一条台账。
// 台账是作者在 air 交互终端之外唯一稳定可见的地方，所以宁可留痕也不静默。
func (r *DevAutoRuntime) noteDevAutoGiveUp(ctx context.Context, extension Extension, stagedDigest string, reason error) {
	if r == nil || r.service == nil || r.service.store == nil {
		return
	}
	key := extension.ID + "|" + stagedDigest
	r.giveUpMu.Lock()
	if r.giveUpNotes == nil {
		r.giveUpNotes = make(map[string]struct{})
	}
	_, alreadyNoted := r.giveUpNotes[key]
	if !alreadyNoted {
		r.giveUpNotes[key] = struct{}{}
	}
	r.giveUpMu.Unlock()
	if alreadyNoted {
		return
	}
	if r.logger != nil {
		r.logger.WarnContext(ctx, "dev auto: giving up on staged artifact",
			"extensionId", extension.ID, "digest", stagedDigest, "reason", reason)
	}
	_, _ = r.service.store.CreateEvent(ctx, EventInput{
		ExtensionID: extension.ID,
		Action:      EventDevAutoSkipped,
		Message: fmt.Sprintf("dev auto gave up activating %s @ %s: %s",
			extension.ID, stagedDigest, reason),
	})
}

// ensureDevAutoActor 返回（必要时创建）dev-auto 系统账号的 actor。
// 账号无密码凭据（不调用 CreateCredential），不可登录；幂等。
func (r *DevAutoRuntime) ensureDevAutoActor(ctx context.Context) (identity.Actor, error) {
	if r.operatorUserID > 0 {
		return r.identities.LoadActor(ctx, r.operatorUserID)
	}
	actor, err := systemactor.Ensure(ctx, r.identities, systemactor.Spec{
		Username:    DevAutoUsername,
		Email:       DevAutoEmail,
		DisplayName: DevAutoDisplayName,
		RoleKey:     identity.RoleSuperAdmin,
		// dev 自动机保留旧行为：没有人类账号时也可以创建自己的执行者。
		RequireExistingUser: false,
	})
	if err != nil {
		return identity.Actor{}, err
	}
	r.operatorUserID = actor.ID
	return actor, nil
}

// withStoreForTests 返回共享同一 store 的副本（测试专用；生产 store 随装配持有）。
func (s *Service) withStoreForTests(store Store) *Service {
	clone := *s
	clone.store = store
	return &clone
}

// DevAutoIdempotencyKeyPrefix 是 dev 自动升级 Idempotency-Key 的保留前缀；
// LifecycleService.Upgrade 据此识别 dev 通道并晋升组件契约版本。
const DevAutoIdempotencyKeyPrefix = "dev-auto/"

// DevIdempotencyKey 派生一次 dev 升级的确定性幂等键。
func DevIdempotencyKey(extensionID, stagedDigest string) string {
	return DevAutoIdempotencyKeyPrefix + extensionID + "/" + stagedDigest
}

// StagedVersionOrActiveDigestForDev 返回 staged 摘要（无 staged 时 active），
// 供失败事件的现场定位。
func (e Extension) StagedVersionOrActiveDigestForDev() string {
	if e.StagedVersion != nil && e.StagedVersion.PackageDigest != "" {
		return e.StagedVersion.PackageDigest
	}
	return e.PackageDigest
}
