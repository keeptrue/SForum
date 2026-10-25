package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
	capabilities "github.com/zhuchunshu/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

// Restart 是 Host 拥有的可恢复重启入口。普通重启先完整停用旧 runtime，
// legacy -> Lifecycle V2 则在停用后以 exact CAS 晋升 staged 制品，再启用目标。
// 任一步失败都保持已提交的安全状态；同一 Idempotency-Key 可继续未完成流程。
func (s *LifecycleService) Restart(
	ctx context.Context,
	actor identity.Actor,
	id string,
	input RestartInput,
) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	if !validLifecycleServiceIdempotencyKey(input.IdempotencyKey) {
		return Extension{}, fmt.Errorf("%w: stable Idempotency-Key is required", ErrLifecycleCoordinatorInvalid)
	}

	current, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return Extension{}, err
	}
	if current.Type != TypePlugin {
		return Extension{}, ErrThemeActivationRequired
	}
	if err := requireEnablePreflight(current); err != nil {
		return Extension{}, err
	}

	target, hasStaged := current.StagedArtifact()
	if !hasStaged {
		target = current
	}
	if err := s.preflightRestartTarget(ctx, target); err != nil {
		return Extension{}, err
	}
	if current.Status == StatusInstalled {
		return Extension{}, fmt.Errorf("%w: only an enabled or recoverably disabled plugin can restart", ErrLifecycleCoordinatorInvalid)
	}
	if current.Status != StatusEnabled && current.Status != StatusDisabled {
		return Extension{}, fmt.Errorf("%w: unsupported restart status", ErrLifecycleCoordinatorInvalid)
	}
	if !RequiresExecutableTrust(target) && restartRequiresCapabilityConfirmation(target) &&
		!input.ConfirmCapabilities && (hasStaged || current.Status != StatusEnabled) {
		// 暂存目标可能扩大 Host 能力；必须在任何 V2/legacy 停机分支前取得确认。
		return Extension{}, ErrCapabilityConfirmationRequired
	}

	// 正常 V2 staged 更新继续使用其原生 upgrade 账本，不降级成停机桥接。
	if current.Status == StatusEnabled && hasStaged && usesLifecycleV2(current) && usesLifecycleV2(target) {
		updated, upgradeErr := s.Upgrade(ctx, actor, current.ID, UpgradeInput{
			ConfirmationToken: input.ConfirmationToken,
			IdempotencyKey:    input.IdempotencyKey,
		})
		if upgradeErr != nil {
			return Extension{}, upgradeErr
		}
		s.auditRestart(ctx, actor, current, updated, true)
		return updated, nil
	}

	enableKey := restartPhaseIdempotencyKey(input.IdempotencyKey, "enable")
	if usesLifecycleV2(current) && !hasStaged {
		if replayed, found, replayErr := s.replayLifecycleV2(ctx, actor, current, enableKey); found || replayErr != nil {
			return replayed, replayErr
		}
	}

	if err := s.prepareRestartTargetAuthority(ctx, actor, target, input); err != nil {
		return Extension{}, err
	}

	source := current
	if current.Status == StatusEnabled {
		current, err = s.DisableWithInput(ctx, actor, current.ID, LifecycleRequestInput{
			IdempotencyKey: restartPhaseIdempotencyKey(input.IdempotencyKey, "disable"),
		})
		if err != nil {
			return Extension{}, err
		}
	}

	if hasStaged {
		current, err = s.promoteRestartTarget(ctx, current, target)
		if err != nil {
			return Extension{}, err
		}
	} else if !sameRestartArtifact(current, target) {
		return Extension{}, ErrStagedVersionConflict
	}

	confirmCapabilities := input.ConfirmCapabilities || (source.Status == StatusEnabled && !hasStaged)
	enabled, err := s.Enable(ctx, actor, current.ID, EnableInput{
		ConfirmCapabilities: confirmCapabilities,
		ConfirmationToken:   input.ConfirmationToken,
		IdempotencyKey:      enableKey,
	})
	if err != nil {
		return Extension{}, err
	}
	s.auditRestart(ctx, actor, source, enabled, hasStaged)
	return enabled, nil
}

func restartRequiresCapabilityConfirmation(target Extension) bool {
	keys, _ := extensionmanifest.ResolvedCapabilities(target.Manifest)
	return capabilities.RequiresConfirmation(keys)
}

func (s *serviceCore) preflightRestartTarget(ctx context.Context, target Extension) error {
	if err := requireEnablePreflight(target); err != nil {
		return err
	}
	if err := s.ensureRequiredFeatures(ctx, target.Manifest.RequiresFeatures); err != nil {
		return err
	}
	if _, err := s.preflightActivationDependencies(ctx, target); err != nil {
		return err
	}
	return s.verifyExtension(ctx, target)
}

func (s *serviceCore) prepareRestartTargetAuthority(
	ctx context.Context,
	actor identity.Actor,
	target Extension,
	input RestartInput,
) error {
	if !RequiresExecutableTrust(target) {
		return nil
	}
	if s.executableTrust == nil {
		return ErrTrustChallengeRequired
	}
	if trusted, err := s.executableTrust.TrustedArtifact(ctx, target); err == nil && trusted {
		return nil
	} else if err != nil && !errors.Is(err, ErrTrustGrantNotFound) {
		return err
	}
	if s.trustChallengesEnabled {
		// 先确认目标授权再停用旧 runtime；确认只写 exact grant，不执行目标代码。
		_, err := s.executableTrust.ConfirmLifecycleAuthority(ctx, actor, target, input.ConfirmationToken)
		return err
	}
	if !input.ConfirmCapabilities {
		return ErrCapabilityConfirmationRequired
	}
	if err := requireSuperAdminForUntrustedBackend(actor, target.Source, target.Manifest); err != nil {
		return err
	}
	return s.executableTrust.EnsureCompatibilityGrant(ctx, actor, target)
}

func (s *serviceCore) promoteRestartTarget(
	ctx context.Context,
	current Extension,
	target Extension,
) (Extension, error) {
	if current.Status != StatusDisabled || current.StagedVersion == nil {
		return Extension{}, ErrStagedVersionConflict
	}
	promoted, err := s.store.PromoteStagedVersion(ctx, StagedVersionCASInput{
		ExtensionID:                 current.ID,
		ExpectedActiveVersionID:     current.ActiveVersionID,
		ExpectedActiveVersion:       current.Version,
		ExpectedActivePackageDigest: current.PackageDigest,
		ExpectedStagedVersionID:     target.ActiveVersionID,
		ExpectedStagedVersion:       target.Version,
		ExpectedPackageDigest:       target.PackageDigest,
	})
	if err == nil {
		return promoted, nil
	}
	if !errors.Is(err, ErrStagedVersionConflict) {
		return Extension{}, err
	}
	// 允许同一请求在晋升提交后、响应返回前中断：精确目标已成为 disabled active
	// 即视为该阶段完成，绝不根据“最新版本”猜测。
	reloaded, loadErr := s.store.Get(ctx, current.ID)
	if loadErr != nil {
		return Extension{}, errors.Join(err, loadErr)
	}
	if reloaded.Status == StatusDisabled && reloaded.StagedVersion == nil && sameRestartArtifact(reloaded, target) {
		return reloaded, nil
	}
	return Extension{}, err
}

func restartPhaseIdempotencyKey(base string, phase string) string {
	digest := sha256.Sum256([]byte("sforum.extension.restart\x00" + phase + "\x00" + base))
	return "restart-" + phase + "-" + hex.EncodeToString(digest[:])
}

func sameRestartArtifact(left Extension, right Extension) bool {
	return left.ID == right.ID &&
		left.ActiveVersionID == right.ActiveVersionID &&
		left.Version == right.Version &&
		left.PackageDigest == right.PackageDigest
}

func (s *serviceCore) auditRestart(
	ctx context.Context,
	actor identity.Actor,
	source Extension,
	target Extension,
	stagedApplied bool,
) {
	s.appendAudit(ctx, actor, audit.ActionExtensionRestart, map[string]any{
		"extensionId":         target.ID,
		"sourceVersion":       source.Version,
		"sourcePackageDigest": source.PackageDigest,
		"targetVersion":       target.Version,
		"targetPackageDigest": target.PackageDigest,
		"stagedApplied":       stagedApplied,
	})
}
