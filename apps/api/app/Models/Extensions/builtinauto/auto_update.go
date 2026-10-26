package builtinauto

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/systemactor"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// IdempotencyKeyPrefix 是 Host 自动升级内置插件的保留前缀。
// 同一 extensionId + staged digest 永远派生出同一个键，进程崩溃后重放安全。
const IdempotencyKeyPrefix = "builtin-auto/"

// IdempotencyKey 派生一次自动升级的确定性幂等键。
func IdempotencyKey(extensionID, stagedDigest string) string {
	return IdempotencyKeyPrefix + extensionID + "/" + stagedDigest
}

// Summary 是一次启动扫描的结果，用于日志与测试。
type Summary struct {
	Updated []string
	Skipped []string
	Failed  []string
}

// Empty 表示没有候选，调用方可以跳过“已执行”日志。
func (s Summary) Empty() bool {
	return len(s.Updated) == 0 && len(s.Skipped) == 0 && len(s.Failed) == 0
}

// Inventory 是自动更新器需要的最小读取/事件边界。
type Inventory interface {
	List(ctx context.Context) ([]extensions.Extension, error)
	Get(ctx context.Context, id string) (extensions.Extension, error)
	CreateEvent(ctx context.Context, input extensions.EventInput) (extensions.ExtensionEvent, error)
}

// Promoter 是自动更新器需要的生命周期窄边界。
type Promoter interface {
	Upgrade(ctx context.Context, actor identity.Actor, id string, input extensions.UpgradeInput) (extensions.Extension, error)
	Restart(ctx context.Context, actor identity.Actor, id string, input extensions.RestartInput) (extensions.Extension, error)
}

// Policy 读取运营开关。
type Policy interface {
	BuiltinAutoUpdateEnabled(ctx context.Context) (bool, error)
}

// ActorProvider 返回自动更新的系统 actor。
type ActorProvider interface {
	EnsureBuiltinAutoActor(ctx context.Context) (identity.Actor, error)
}

// ProviderProbe 判断扩展当前是否被选为任一 provider。
// 探测失败必须按“已选中”处理，避免在无法证明安全时重启 provider。
type ProviderProbe interface {
	ProviderSelected(ctx context.Context, extensionID string) (bool, error)
}

// Updater 在 Host 启动时把受保护内置插件提升到发行版版本。
// 它只处理 source=builtin、isSystem=true、status=enabled 的插件；上传/第三方
// 包和主题不进入本通道。
type Updater struct {
	inventory Inventory
	promoter  Promoter
	policy    Policy
	actors    ActorProvider
	providers ProviderProbe
	logger    *slog.Logger

	safeMode       bool
	recoveryActive bool
}

func New(
	inventory Inventory,
	promoter Promoter,
	policy Policy,
	actors ActorProvider,
	providers ProviderProbe,
	logger *slog.Logger,
	safeMode bool,
	recoveryActive bool,
) *Updater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Updater{
		inventory:      inventory,
		promoter:       promoter,
		policy:         policy,
		actors:         actors,
		providers:      providers,
		logger:         logger,
		safeMode:       safeMode,
		recoveryActive: recoveryActive,
	}
}

// Run 执行一次启动扫描。策略/存储读取失败会返回 error 供调用方记录；
// 单个扩展的升级失败只写事件并继续，不中断其余扩展。
func (u *Updater) Run(ctx context.Context) (Summary, error) {
	summary := Summary{}
	if u == nil || u.inventory == nil || u.promoter == nil || u.policy == nil || u.actors == nil {
		return summary, nil
	}
	if u.safeMode || u.recoveryActive {
		u.logger.InfoContext(ctx, "builtin plugin auto-update skipped",
			"reason", "safe_mode_or_recovery")
		return summary, nil
	}
	enabled, err := u.policy.BuiltinAutoUpdateEnabled(ctx)
	if err != nil {
		return summary, fmt.Errorf("read builtin auto-update policy: %w", err)
	}
	if !enabled {
		u.logger.InfoContext(ctx, "builtin plugin auto-update disabled by policy")
		return summary, nil
	}

	items, err := u.inventory.List(ctx)
	if err != nil {
		return summary, fmt.Errorf("list builtin plugins: %w", err)
	}
	candidates := make([]extensions.Extension, 0, len(items))
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Source != extensions.SourceBuiltin || !item.IsSystem {
			continue
		}
		if item.Status != extensions.StatusEnabled || item.StagedVersion == nil {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return summary, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	actor, err := u.actors.EnsureBuiltinAutoActor(ctx)
	if err != nil {
		if errors.Is(err, systemactor.ErrNoUsers) {
			u.logger.InfoContext(ctx, "builtin plugin auto-update deferred until the first user exists")
			return summary, nil
		}
		return summary, fmt.Errorf("ensure builtin auto actor: %w", err)
	}
	if !actor.IsActive() {
		u.logger.WarnContext(ctx, "builtin plugin auto-update actor is inactive",
			"actorUserId", actor.ID)
		return summary, nil
	}

	for _, item := range candidates {
		u.promoteOne(ctx, actor, item, &summary)
	}
	return summary, nil
}

func (u *Updater) promoteOne(
	ctx context.Context,
	actor identity.Actor,
	item extensions.Extension,
	summary *Summary,
) {
	staged, ok := item.StagedArtifact()
	if !ok || strings.TrimSpace(staged.PackageDigest) == "" {
		u.recordSkip(ctx, actor, item, "staged_artifact_missing", "")
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	if err := extensions.StagedArtifactUsable(staged); err != nil {
		u.recordSkip(ctx, actor, item, "artifact_unusable", err.Error())
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	action, reason := updateAction(item, staged)
	if action == "" {
		u.recordSkip(ctx, actor, item, reason, "")
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	// Lifecycle V2 的 Upgrade 不经过 disable/enable，不会清理 provider 选择。
	// legacy Restart 会；因此只在 legacy 路径上要求 provider 未被选中。
	// Re-read immediately before the lifecycle call. A legacy Restart accepts a
	// disabled+staged plugin and would re-enable it; if an operator disabled the
	// plugin after List, the auto-update must respect that choice.
	current, getErr := u.inventory.Get(ctx, item.ID)
	if getErr != nil {
		u.recordSkip(ctx, actor, item, "state_changed", getErr.Error())
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	currentStaged, hasCurrentStaged := current.StagedArtifact()
	if current.Status != extensions.StatusEnabled || !hasCurrentStaged ||
		!strings.EqualFold(strings.TrimSpace(currentStaged.PackageDigest), strings.TrimSpace(staged.PackageDigest)) {
		u.recordSkip(ctx, actor, item, "state_changed", "")
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	// Recompute the version decision against the fresh active artifact. A manual
	// upgrade/rollback between List and here can turn this candidate into a
	// downgrade or a no-op; never execute a stale decision.
	currentAction, currentReason := updateAction(current, currentStaged)
	if currentAction == "" || currentAction != action {
		u.recordSkip(ctx, actor, item, "state_changed", currentReason)
		summary.Skipped = append(summary.Skipped, item.ID)
		return
	}
	if action == actionRestart && u.providers != nil {
		selected, probeErr := u.providers.ProviderSelected(ctx, item.ID)
		if probeErr != nil {
			u.recordSkip(ctx, actor, item, "provider_probe_failed", probeErr.Error())
			summary.Skipped = append(summary.Skipped, item.ID)
			return
		}
		if selected {
			u.recordSkip(ctx, actor, item, "provider_in_use", "")
			summary.Skipped = append(summary.Skipped, item.ID)
			return
		}
	}

	key := IdempotencyKey(item.ID, staged.PackageDigest)
	var err error
	switch action {
	case actionUpgrade:
		_, err = u.promoter.Upgrade(ctx, actor, item.ID, extensions.UpgradeInput{
			IdempotencyKey: key,
		})
	default:
		_, err = u.promoter.Restart(ctx, actor, item.ID, extensions.RestartInput{
			ConfirmCapabilities: true,
			IdempotencyKey:      key,
		})
	}
	if err != nil {
		if errors.Is(err, extensions.ErrLifecycleOperationInProgress) ||
			errors.Is(err, extensions.ErrStagedVersionConflict) {
			u.recordSkip(ctx, actor, item, "operation_in_progress", err.Error())
			summary.Skipped = append(summary.Skipped, item.ID)
			return
		}
		u.recordFailed(ctx, actor, item, action, err)
		summary.Failed = append(summary.Failed, item.ID)
		return
	}
	u.recordUpdated(ctx, actor, item, staged, action)
	summary.Updated = append(summary.Updated, item.ID)
}

const (
	actionUpgrade = "upgrade"
	actionRestart = "restart"
)

// updateAction 返回空字符串表示不执行，并给出稳定原因码。
func updateAction(active, staged extensions.Extension) (string, string) {
	activeVersion, err := semver.StrictNewVersion(strings.TrimSpace(active.Version))
	if err != nil {
		return "", "invalid_active_version"
	}
	stagedVersion, err := semver.StrictNewVersion(strings.TrimSpace(staged.Version))
	if err != nil {
		return "", "invalid_staged_version"
	}
	switch {
	case stagedVersion.LessThan(activeVersion):
		return "", "downgrade"
	case stagedVersion.Equal(activeVersion):
		if !strings.EqualFold(strings.TrimSpace(staged.PackageDigest), strings.TrimSpace(active.PackageDigest)) {
			return "", "same_version_digest_drift"
		}
		return "", "same_version"
	}
	if extensions.UsesLifecycleV2(active) && extensions.UsesLifecycleV2(staged) {
		return actionUpgrade, ""
	}
	return actionRestart, ""
}

func (u *Updater) recordUpdated(
	ctx context.Context,
	actor identity.Actor,
	item extensions.Extension,
	staged extensions.Extension,
	action string,
) {
	message := fmt.Sprintf("builtin auto-update: %s %s -> %s (%s, digest=%s)",
		item.ID, item.Version, staged.Version, action, staged.PackageDigest)
	u.logger.InfoContext(ctx, "builtin plugin auto-updated",
		"extensionId", item.ID, "fromVersion", item.Version,
		"toVersion", staged.Version, "digest", staged.PackageDigest, "action", action)
	_, _ = u.inventory.CreateEvent(ctx, extensions.EventInput{
		ExtensionID: item.ID,
		ActorUserID: actor.ID,
		Action:      extensions.EventBuiltinAutoUpdated,
		Message:     message,
	})
}

func (u *Updater) recordSkip(
	ctx context.Context,
	actor identity.Actor,
	item extensions.Extension,
	reason string,
	detail string,
) {
	stagedVersion := ""
	if item.StagedVersion != nil {
		stagedVersion = item.StagedVersion.Version
	}
	message := fmt.Sprintf("builtin auto-update skipped (%s): active=%s staged=%s",
		reason, item.Version, stagedVersion)
	if strings.TrimSpace(detail) != "" {
		message += ": " + detail
	}
	u.logger.InfoContext(ctx, "builtin plugin auto-update skipped",
		"extensionId", item.ID, "reason", reason, "detail", detail)
	_, _ = u.inventory.CreateEvent(ctx, extensions.EventInput{
		ExtensionID: item.ID,
		ActorUserID: actor.ID,
		Action:      extensions.EventBuiltinAutoUpdateSkipped,
		Message:     message,
	})
}

func (u *Updater) recordFailed(
	ctx context.Context,
	actor identity.Actor,
	item extensions.Extension,
	action string,
	err error,
) {
	stagedVersion := ""
	if item.StagedVersion != nil {
		stagedVersion = item.StagedVersion.Version
	}
	message := fmt.Sprintf("builtin auto-update failed (%s): active=%s staged=%s: %v",
		action, item.Version, stagedVersion, err)
	u.logger.WarnContext(ctx, "builtin plugin auto-update failed",
		"extensionId", item.ID, "action", action, "error", err)
	_, _ = u.inventory.CreateEvent(ctx, extensions.EventInput{
		ExtensionID: item.ID,
		ActorUserID: actor.ID,
		Action:      extensions.EventBuiltinAutoUpdateFailed,
		Message:     message,
	})
}
