package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

var (
	ErrLifecycleAuthorityNotFound   = errors.New("extensions: successful lifecycle authority not found")
	ErrLifecycleCleanupFinalization = errors.New("extensions: lifecycle cleanup finalization failed")
	ErrLifecycleCleanupNotFinalized = errors.New("extensions: lifecycle cleanup was not finalized")
)

// LifecycleCoordinatorRunner keeps Models independent from the production
// runtime/Host adapters assembled under Support/Extensions.
type LifecycleCoordinatorRunner interface {
	Run(context.Context, LifecycleCoordinatorRunInput) (LifecycleCoordinatorRunResult, error)
}

// LifecycleStaticPreflight is deliberately a callback: bootstrap may adapt the
// Support implementation without Models importing the process-owning package.
type LifecycleStaticPreflight func(
	context.Context,
	LifecycleMachineOperation,
	*Extension,
	Extension,
) error

// LifecycleCleanupFinalizer 是 terminal-success 之后唯一允许触发物理删除的 Host 边界。
// Support 层适配器必须校验 durable tombstone 与 exact purge receipt。
type LifecycleCleanupFinalizer func(context.Context, int64) (LifecycleCleanupFinalization, error)

func WithLifecycleCleanupFinalizer(finalizer LifecycleCleanupFinalizer) ServiceOption {
	return func(service *Service) {
		service.lifecycleFinalizer = finalizer
	}
}

// ExtensionAttachmentReferenceReaper 释放某个扩展拥有的附件引用（公开图标等）。
//
// 插件被卸载后若引用悬空，附件永远不会被孤儿回收。禁用不释放：禁用期间图标
// 保持可读是预期行为。实现由 attachments 域提供，bootstrap 装配注入。
type ExtensionAttachmentReferenceReaper interface {
	ReleaseExtensionAttachmentReferences(ctx context.Context, extensionID string) (int, error)
}

func WithExtensionAttachmentReferenceReaper(reaper ExtensionAttachmentReferenceReaper) ServiceOption {
	return func(service *Service) {
		service.attachmentReferences = reaper
	}
}

// LifecycleAuthorityRepository exposes only the immutable successful snapshot
// needed for deactivation and historical rollback.
type LifecycleAuthorityRepository interface {
	LastSuccessfulLifecycleAuthority(context.Context, ExactExtensionVersionInput) (LifecycleAuthoritySnapshot, error)
	OperationByIdempotencyKey(context.Context, string, string) (LifecycleOperation, error)
}

type lifecycleServiceRequest struct {
	operation         LifecycleMachineOperation
	source            *Extension
	target            Extension
	idempotencyKey    string
	confirmationToken string
	frozenAuthority   bool
	removalMode       string
	forced            bool
}

func usesLifecycleV2(extension Extension) bool {
	return extension.Type == TypePlugin && extension.Manifest.Backend.ProtocolVersion == 2 &&
		extension.Manifest.Lifecycle != nil && strings.TrimSpace(extension.Manifest.Lifecycle.ContractVersion) != ""
}

// UsesLifecycleV2 reports whether the exact artifact declares the Lifecycle V2
// contract. It is the exported form used by the focused builtin auto-update
// package, which must not duplicate the contract predicate.
func UsesLifecycleV2(extension Extension) bool {
	return usesLifecycleV2(extension)
}

func (s *serviceCore) enableLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	input EnableInput,
) (Extension, error) {
	if replayed, found, err := s.replayLifecycleV2(ctx, actor, extension, input.IdempotencyKey); found || err != nil {
		return replayed, err
	}
	request := lifecycleServiceRequest{
		operation: LifecycleMachineEnable, target: extension,
		idempotencyKey: input.IdempotencyKey, confirmationToken: input.ConfirmationToken,
	}
	switch extension.Status {
	case StatusInstalled:
		request.operation = LifecycleMachineInstall
		if staged, ok := extension.StagedArtifact(); ok {
			request.target = staged
		}
	case StatusDisabled:
		request.operation = LifecycleMachineEnable
	case StatusEnabled:
		if staged, ok := extension.StagedArtifact(); ok {
			request.operation = LifecycleMachineUpgrade
			request.source = exactLifecycleCopy(extension)
			request.target = staged
		}
	default:
		return Extension{}, fmt.Errorf("%w: unsupported extension status", ErrLifecycleCoordinatorInvalid)
	}
	return s.runLifecycleV2(ctx, actor, request)
}

func (s *serviceCore) disableLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	input LifecycleRequestInput,
) (Extension, error) {
	if replayed, found, err := s.replayLifecycleV2(ctx, actor, extension, input.IdempotencyKey); found || err != nil {
		return replayed, err
	}
	if extension.Status != StatusEnabled {
		return Extension{}, fmt.Errorf("%w: only an enabled plugin can be disabled", ErrLifecycleCoordinatorInvalid)
	}
	return s.runLifecycleV2(ctx, actor, lifecycleServiceRequest{
		operation: LifecycleMachineDisable, source: exactLifecycleCopy(extension), target: extension,
		idempotencyKey: input.IdempotencyKey, frozenAuthority: true,
	})
}

// Upgrade activates the exact currently staged artifact. Upload remains inert;
// this method is the first operation allowed to execute candidate code.
func (s *LifecycleService) Upgrade(ctx context.Context, actor identity.Actor, id string, input UpgradeInput) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return Extension{}, err
	}
	if extension.Type != TypePlugin {
		return Extension{}, ErrThemeActivationRequired
	}
	if !usesLifecycleV2(extension) {
		return Extension{}, fmt.Errorf("%w: lifecycle V2 is required", ErrLifecycleCoordinatorInvalid)
	}
	if replayed, found, err := s.replayLifecycleV2(ctx, actor, extension, input.IdempotencyKey); found || err != nil {
		return replayed, err
	}
	if extension.Status != StatusEnabled {
		return Extension{}, fmt.Errorf("%w: staged upgrade requires an enabled source", ErrLifecycleCoordinatorInvalid)
	}
	target, ok := extension.StagedArtifact()
	if !ok || !usesLifecycleV2(target) {
		return Extension{}, ErrStagedVersionNotFound
	}
	if err := requireEnablePreflight(target); err != nil {
		return Extension{}, err
	}
	return s.runLifecycleV2(ctx, actor, lifecycleServiceRequest{
		operation: LifecycleMachineUpgrade, source: exactLifecycleCopy(extension), target: target,
		idempotencyKey: input.IdempotencyKey, confirmationToken: input.ConfirmationToken,
	})
}

// Rollback activates an exact historical artifact and reuses only that
// artifact's last successful frozen authority snapshot.
func (s *LifecycleService) Rollback(ctx context.Context, actor identity.Actor, id string, input RollbackInput) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return Extension{}, err
	}
	if extension.Type != TypePlugin {
		return Extension{}, ErrThemeActivationRequired
	}
	if !usesLifecycleV2(extension) {
		return Extension{}, fmt.Errorf("%w: rollback requires an enabled lifecycle V2 source", ErrLifecycleCoordinatorInvalid)
	}
	if replayed, found, err := s.replayLifecycleV2(ctx, actor, extension, input.IdempotencyKey); found || err != nil {
		return replayed, err
	}
	if extension.Status != StatusEnabled {
		return Extension{}, fmt.Errorf("%w: rollback requires an enabled lifecycle V2 source", ErrLifecycleCoordinatorInvalid)
	}
	versions, ok := s.store.(ExactExtensionVersionRepository)
	if !ok {
		return Extension{}, ErrLifecycleCoordinatorUnavailable
	}
	version, err := versions.GetExtensionVersion(ctx, ExactExtensionVersionInput{
		ExtensionID: extension.ID, Version: input.TargetVersion, PackageDigest: input.TargetPackageDigest,
	})
	if err != nil {
		if errors.Is(err, ErrExtensionVersionNotFound) || errors.Is(err, ErrExtensionVersionInvalid) {
			return Extension{}, err
		}
		return Extension{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	target := extensionFromExactVersion(extension, version)
	if !usesLifecycleV2(target) {
		return Extension{}, fmt.Errorf("%w: rollback target has no lifecycle V2 contract", ErrLifecycleCoordinatorInvalid)
	}
	if err := requireEnablePreflight(target); err != nil {
		return Extension{}, err
	}
	return s.runLifecycleV2(ctx, actor, lifecycleServiceRequest{
		operation: LifecycleMachineRollback, source: exactLifecycleCopy(extension), target: target,
		idempotencyKey: input.IdempotencyKey, frozenAuthority: true,
	})
}

func (s *serviceCore) uninstallLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	input UninstallInput,
) (UninstallResult, error) {
	removalMode, err := normalizeLifecycleRemovalMode(input.RemovalMode)
	if err != nil {
		return UninstallResult{}, err
	}
	request := lifecycleServiceRequest{
		operation: LifecycleMachineUninstall, source: exactLifecycleCopy(extension), target: extension,
		idempotencyKey: input.IdempotencyKey, frozenAuthority: true, removalMode: removalMode,
	}
	_, result, err := s.runLifecycleV2Operation(ctx, actor, request)
	if err != nil {
		return UninstallResult{}, err
	}
	return s.finalizeLifecycleUninstall(ctx, extension.ID, removalMode, result.Operation, result.Replayed)
}

func (s *serviceCore) runLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	request lifecycleServiceRequest,
) (Extension, error) {
	item, _, err := s.runLifecycleV2Operation(ctx, actor, request)
	return item, err
}

func (s *serviceCore) runLifecycleV2Operation(
	ctx context.Context,
	actor identity.Actor,
	request lifecycleServiceRequest,
) (Extension, LifecycleCoordinatorRunResult, error) {
	if s.safeMode {
		return Extension{}, LifecycleCoordinatorRunResult{}, ErrSafeModeActive
	}
	if !validLifecycleServiceIdempotencyKey(request.idempotencyKey) {
		return Extension{}, LifecycleCoordinatorRunResult{}, fmt.Errorf("%w: stable Idempotency-Key is required", ErrLifecycleCoordinatorInvalid)
	}
	if s.lifecycleCoordinator == nil || s.lifecyclePreflight == nil || s.lifecycleAuthority == nil {
		return Extension{}, LifecycleCoordinatorRunResult{}, ErrLifecycleCoordinatorUnavailable
	}
	if err := validateLifecycleSourceArtifact(request.operation, request.target, request.source); err != nil {
		return Extension{}, LifecycleCoordinatorRunResult{}, err
	}

	authority, err := s.lifecycleServiceAuthority(ctx, actor, request)
	if err != nil {
		return Extension{}, LifecycleCoordinatorRunResult{}, err
	}
	auditEventID, err := s.appendLifecycleRequestAudit(ctx, actor, request)
	if err != nil {
		return Extension{}, LifecycleCoordinatorRunResult{}, err
	}
	// This callback runs before coordinator position zero, so no Host adapter can
	// stage a runtime instance until all static facts have passed.
	if err := s.lifecyclePreflight(ctx, request.operation, request.source, request.target); err != nil {
		// 依赖图/信任/兼容性任一子检查失败都归并为同一句，台账里难以区分；
		// 这里保留完整链（errors.Join 的多行）并标注阶段名，并把 candidate 身份
		// 与完整 manifest 落到 /tmp 供 invalid manifest 类失败现场定位。
		slog.WarnContext(ctx, "lifecycle static preflight failed",
			"operation", string(request.operation), "targetID", request.target.ID,
			"targetVersion", request.target.Version, "targetDigest", request.target.PackageDigest,
			"error", err.Error())
		return Extension{}, LifecycleCoordinatorRunResult{}, fmt.Errorf("%w: static preflight: %w", ErrPreflightFailed, err)
	}
	runInput, err := BuildLifecycleCoordinatorRunInput(request.target, actor, authority, LifecycleOperationIntent{
		Operation: request.operation, IdempotencyKey: request.idempotencyKey,
		SourceExtension: request.source, AuditEventID: auditEventID,
		FrozenAuthority: request.frozenAuthority, RemovalMode: request.removalMode, Forced: request.forced,
	})
	if err != nil {
		return Extension{}, LifecycleCoordinatorRunResult{}, err
	}
	result, err := s.lifecycleCoordinator.Run(ctx, runInput)
	if err != nil {
		return Extension{}, result, lifecycleCoordinatorServiceError(err)
	}
	item, err := s.finishLifecycleV2(ctx, actor, request, result)
	return item, result, err
}

func (s *serviceCore) finishLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	request lifecycleServiceRequest,
	result LifecycleCoordinatorRunResult,
) (Extension, error) {
	if result.Operation.TerminalResult != LifecycleTerminalSucceeded &&
		result.Operation.TerminalResult != LifecycleTerminalSkipped {
		return Extension{}, ErrLifecycleCoordinatorActionFailed
	}
	current, err := s.store.Get(ctx, request.target.ID)
	if err != nil {
		return Extension{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	if result.Operation.TerminalResult == LifecycleTerminalSucceeded && request.operation == LifecycleMachineDisable {
		if err := s.clearPluginProviderSelectionsWithAudit(
			ctx, request.target.ID, lifecycleOperationActorUserID(result.Operation),
			lifecycleOperationAuditEventID(result.Operation), "extension_disabled", keepMailProviderSelection,
		); err != nil {
			return Extension{}, errors.Join(ErrLifecycleCoordinatorUnavailable, fmt.Errorf("clear disabled provider selections: %w", err))
		}
	}
	if result.Operation.TerminalResult == LifecycleTerminalSucceeded && !result.Replayed {
		s.emitLifecycleCompatibilityEvent(ctx, actor, request.operation, current)
		// 升级/启用成功后恢复 Schema，并在升级时跑 SettingsLifecycle 迁移（失败不改 revision）。
		switch request.operation {
		case LifecycleMachineEnable, LifecycleMachineUpgrade, LifecycleMachineRollback:
			if regErr := s.host.RegisterSettingsLifecycleFromManifest(current); regErr != nil {
				return Extension{}, regErr
			}
			if request.operation == LifecycleMachineUpgrade {
				sourceExt := current
				if request.source != nil {
					sourceExt = *request.source
				}
				if migErr := s.migrateSettingsOnUpgrade(ctx, actor, current); migErr != nil {
					// 失败迁移：RuntimeRollout 标记 fail，且不推进设置 revision。
					if s.runtimeRollout != nil {
						_, _ = s.host.DriveRuntimeRolloutForStagedUpgrade(ctx, actor, sourceExt, current, false, migErr)
					}
					return Extension{}, migErr
				}
				// 真实 staged version → 迁移就绪 → 节点确认 → 晋升。
				if _, rollErr := s.host.DriveRuntimeRolloutForStagedUpgrade(ctx, actor, sourceExt, current, true, nil); rollErr != nil {
					return Extension{}, rollErr
				}
			}
		}
	}
	return s.decorateRuntime(ctx, current), nil
}

// replayLifecycleV2 reconstructs the immutable request before consulting the
// extension's now-mutated status/staged pointer. This is what makes a network
// retry stable after state publication has already committed.
func (s *serviceCore) replayLifecycleV2(
	ctx context.Context,
	actor identity.Actor,
	current Extension,
	idempotencyKey string,
) (Extension, bool, error) {
	if !validLifecycleServiceIdempotencyKey(idempotencyKey) {
		return Extension{}, true, fmt.Errorf("%w: stable Idempotency-Key is required", ErrLifecycleCoordinatorInvalid)
	}
	if s.lifecycleCoordinator == nil || s.lifecyclePreflight == nil || s.lifecycleAuthority == nil {
		return Extension{}, true, ErrLifecycleCoordinatorUnavailable
	}
	operation, err := s.lifecycleAuthority.OperationByIdempotencyKey(ctx, current.ID, idempotencyKey)
	if errors.Is(err, ErrLifecycleOperationNotFound) {
		return Extension{}, false, nil
	}
	if err != nil {
		return Extension{}, true, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	if operation.RequestedByUserID > 0 && operation.RequestedByUserID != actor.ID {
		return Extension{}, true, ErrLifecycleFingerprintConflict
	}
	if operation.CompletedAt != nil {
		switch operation.TerminalResult {
		case LifecycleTerminalSucceeded, LifecycleTerminalSkipped:
			if operation.TerminalResult == LifecycleTerminalSucceeded &&
				LifecycleMachineOperation(operation.Operation) == LifecycleMachineDisable {
				if cleanupErr := s.clearPluginProviderSelectionsWithAudit(
					ctx, current.ID, lifecycleOperationActorUserID(operation),
					lifecycleOperationAuditEventID(operation), "extension_disabled", keepMailProviderSelection,
				); cleanupErr != nil {
					return Extension{}, true, errors.Join(
						ErrLifecycleCoordinatorUnavailable,
						fmt.Errorf("clear disabled provider selections: %w", cleanupErr),
					)
				}
			}
			// A completed replay has no coordinator work and therefore no process
			// staging boundary to preflight again. Return the published state directly.
			item, loadErr := s.store.Get(ctx, current.ID)
			if loadErr != nil {
				return Extension{}, true, errors.Join(ErrLifecycleCoordinatorUnavailable, loadErr)
			}
			return s.decorateRuntime(ctx, item), true, nil
		case LifecycleTerminalFailed, LifecycleTerminalCancelled:
			// Continue so coordinator returns the explicit-recovery requirement.
		default:
			return Extension{}, true, ErrLifecycleCoordinatorInvalid
		}
	}
	request, input, err := s.rebuildLifecycleReplay(ctx, current, operation)
	if err != nil {
		return Extension{}, true, err
	}
	if err := s.lifecyclePreflight(ctx, request.operation, request.source, request.target); err != nil {
		return Extension{}, true, errors.Join(ErrPreflightFailed, err)
	}
	result, err := s.lifecycleCoordinator.Run(ctx, input)
	if err != nil {
		return Extension{}, true, lifecycleCoordinatorServiceError(err)
	}
	// Existing logical requests never emit compatibility events. Coordinator
	// creation owns that one-shot side effect even when this caller finishes it.
	result.Replayed = true
	item, err := s.finishLifecycleV2(ctx, actor, request, result)
	return item, true, err
}

func (s *serviceCore) rebuildLifecycleReplay(
	ctx context.Context,
	current Extension,
	operation LifecycleOperation,
) (lifecycleServiceRequest, LifecycleCoordinatorRunInput, error) {
	operationKind := LifecycleMachineOperation(operation.Operation)
	if _, err := RecommendedLifecyclePath(operationKind); err != nil {
		return lifecycleServiceRequest{}, LifecycleCoordinatorRunInput{}, ErrLifecycleCoordinatorInvalid
	}
	target, err := s.lifecycleExactArtifact(ctx, current, ExactExtensionVersionInput{
		ExtensionID: operation.ExtensionID, Version: operation.ExtensionVersion, PackageDigest: operation.PackageDigest,
	})
	if err != nil {
		return lifecycleServiceRequest{}, LifecycleCoordinatorRunInput{}, err
	}
	request := lifecycleServiceRequest{
		operation: operationKind, target: target, idempotencyKey: operation.IdempotencyKey,
		frozenAuthority: operationKind == LifecycleMachineDisable || operationKind == LifecycleMachineRollback ||
			operationKind == LifecycleMachineUninstall,
		removalMode: operation.RemovalMode, forced: operation.Forced,
	}
	switch operationKind {
	case LifecycleMachineDisable, LifecycleMachineUninstall:
		request.source = exactLifecycleCopy(target)
	case LifecycleMachineUpgrade, LifecycleMachineRollback:
		source, sourceErr := s.lifecycleReplaySource(ctx, current, operation)
		if sourceErr != nil {
			return lifecycleServiceRequest{}, LifecycleCoordinatorRunInput{}, sourceErr
		}
		request.source = &source
	}
	input := LifecycleCoordinatorRunInput{
		Extension: target, SourceExtension: request.source,
		Acquire: AcquireLifecycleOperationInput{
			ExtensionID: operation.ExtensionID, ExtensionVersion: operation.ExtensionVersion,
			PackageDigest: operation.PackageDigest, ArtifactDigests: cloneLifecycleJSON(operation.ArtifactDigests),
			Operation: operation.Operation, PlanVersion: operation.PlanVersion,
			IdempotencyKey: operation.IdempotencyKey, RequestFingerprint: operation.RequestFingerprint,
			AuthorityType: operation.AuthorityType, TrustGrantID: operation.TrustGrantID,
			AuthoritySnapshot: cloneLifecycleJSON(operation.AuthoritySnapshot),
			RequestedByUserID: operation.RequestedByUserID, AuditEventID: operation.AuditEventID,
			RemovalMode: operation.RemovalMode, Forced: operation.Forced, ExistingOnly: true,
		},
	}
	return request, input, nil
}

func (s *serviceCore) lifecycleReplaySource(ctx context.Context, current Extension, operation LifecycleOperation) (Extension, error) {
	if len(operation.Progress) > 0 && string(operation.Progress) != "{}" {
		machine, err := decodeLifecycleCoordinatorMachine(operation.Progress)
		if err != nil {
			return Extension{}, err
		}
		binding := machine.SourceBinding
		if binding.ExtensionID != "" && binding.ExtensionVersion != "" && binding.PackageDigest != "" {
			return s.lifecycleExactArtifact(ctx, current, ExactExtensionVersionInput{
				ExtensionID: binding.ExtensionID, Version: binding.ExtensionVersion, PackageDigest: binding.PackageDigest,
			})
		}
	}
	if current.ID == operation.ExtensionID &&
		(current.Version != operation.ExtensionVersion || current.PackageDigest != operation.PackageDigest) {
		copy := current
		copy.StagedVersion = nil
		return copy, nil
	}
	return Extension{}, fmt.Errorf("%w: exact lifecycle source is unavailable", ErrLifecycleCoordinatorInvalid)
}

func (s *serviceCore) lifecycleExactArtifact(
	ctx context.Context,
	current Extension,
	input ExactExtensionVersionInput,
) (Extension, error) {
	if current.ID == input.ExtensionID && current.Version == input.Version && current.PackageDigest == input.PackageDigest {
		copy := current
		copy.StagedVersion = nil
		return copy, nil
	}
	versions, ok := s.store.(ExactExtensionVersionRepository)
	if !ok {
		return Extension{}, ErrLifecycleCoordinatorUnavailable
	}
	version, err := versions.GetExtensionVersion(ctx, input)
	if err != nil {
		if errors.Is(err, ErrExtensionVersionNotFound) || errors.Is(err, ErrExtensionVersionInvalid) {
			return Extension{}, err
		}
		return Extension{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	return extensionFromExactVersion(current, version), nil
}

func (s *serviceCore) lifecycleServiceAuthority(
	ctx context.Context,
	actor identity.Actor,
	request lifecycleServiceRequest,
) (LifecycleAuthoritySnapshot, error) {
	if request.frozenAuthority {
		if s.lifecycleAuthority == nil {
			return LifecycleAuthoritySnapshot{}, ErrLifecycleCoordinatorUnavailable
		}
		authority, err := s.lifecycleAuthority.LastSuccessfulLifecycleAuthority(ctx, ExactExtensionVersionInput{
			ExtensionID: request.target.ID, Version: request.target.Version,
			PackageDigest: request.target.PackageDigest,
		})
		if err != nil && !errors.Is(err, ErrLifecycleAuthorityNotFound) {
			return LifecycleAuthoritySnapshot{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
		}
		return authority, err
	}
	if s.executableTrust == nil {
		return LifecycleAuthoritySnapshot{}, ErrTrustChallengeRequired
	}
	authority, err := s.executableTrust.ConfirmLifecycleAuthority(ctx, actor, request.target, request.confirmationToken)
	if err != nil {
		return LifecycleAuthoritySnapshot{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	return authority, nil
}

func (s *serviceCore) appendLifecycleRequestAudit(
	ctx context.Context,
	actor identity.Actor,
	request lifecycleServiceRequest,
) (int64, error) {
	writer, ok := s.auditor.(audit.IDWriter)
	if !ok || writer == nil {
		return 0, ErrLifecycleCoordinatorUnavailable
	}
	action := audit.ActionExtensionEnable
	switch request.operation {
	case LifecycleMachineDisable:
		action = audit.ActionExtensionDisable
	case LifecycleMachineUpgrade:
		action = audit.ActionExtensionUpgraded
	case LifecycleMachineRollback:
		action = audit.ActionExtensionRollback
	case LifecycleMachineUninstall:
		action = audit.ActionExtensionUninstalled
	}
	id, err := writer.AppendReturningID(ctx, audit.Event{
		ActorUserID: actor.ID,
		Action:      action,
		Metadata: map[string]any{
			"extensionId":   request.target.ID,
			"operation":     request.operation,
			"version":       request.target.Version,
			"packageDigest": request.target.PackageDigest,
			"removalMode":   request.removalMode,
			"forced":        request.forced,
		},
	})
	if err != nil {
		return 0, errors.Join(ErrLifecycleCoordinatorUnavailable, fmt.Errorf("write lifecycle request audit: %w", err))
	}
	if id <= 0 {
		return 0, ErrLifecycleCoordinatorUnavailable
	}
	return id, nil
}

func lifecycleCoordinatorServiceError(err error) error {
	if errors.Is(err, ErrLifecycleCoordinatorInvalid) ||
		errors.Is(err, ErrLifecycleCoordinatorUnavailable) ||
		errors.Is(err, ErrLifecycleCoordinatorRetryRequired) ||
		errors.Is(err, ErrLifecycleCoordinatorActionFailed) ||
		errors.Is(err, ErrLifecycleFingerprintConflict) ||
		errors.Is(err, ErrLifecycleOperationInProgress) ||
		errors.Is(err, ErrLifecycleNotRecoverable) ||
		errors.Is(err, ErrLifecycleRevisionConflict) ||
		errors.Is(err, ErrLifecycleOperationClosed) ||
		errors.Is(err, ErrLifecycleStateTransitionDenied) {
		return err
	}
	return errors.Join(ErrLifecycleCoordinatorUnavailable, err)
}

func (s *serviceCore) emitLifecycleCompatibilityEvent(
	ctx context.Context,
	actor identity.Actor,
	operation LifecycleMachineOperation,
	extension Extension,
) {
	action, message, hook := EventEnabled, "Extension enabled.", appevents.ExtensionEnabled
	switch operation {
	case LifecycleMachineDisable:
		action, message, hook = EventDisabled, "Extension disabled.", appevents.ExtensionDisabled
	case LifecycleMachineUpgrade:
		action, message = EventUpgraded, "Staged extension upgrade activated."
	case LifecycleMachineRollback:
		action, message = EventRolledBack, "Historical extension version restored."
	case LifecycleMachineUninstall:
		action, message, hook = EventUninstalled, "Extension uninstall cleanup completed.", ""
	}
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: extension.ID, ActorUserID: actor.ID, Action: action, Message: message,
	})
	if s.runtime != nil && hook != "" {
		s.runtime.EmitHook(ctx, hook, map[string]any{"extensionId": extension.ID})
	}
}

func normalizeLifecycleRemovalMode(value string) (string, error) {
	if value == "" {
		return LifecycleRemovalPreserve, nil
	}
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%w: invalid uninstall removal mode", ErrLifecycleCoordinatorInvalid)
	}
	switch value {
	case LifecycleRemovalPreserve, LifecycleRemovalExportThenRemove, LifecycleRemovalComplete:
		return value, nil
	default:
		return "", fmt.Errorf("%w: invalid uninstall removal mode", ErrLifecycleCoordinatorInvalid)
	}
}

func (s *serviceCore) finalizeLifecycleUninstall(
	ctx context.Context,
	extensionID string,
	removalMode string,
	operation LifecycleOperation,
	replayed bool,
) (UninstallResult, error) {
	if operation.ID <= 0 || operation.Operation != string(LifecycleMachineUninstall) ||
		operation.TerminalResult != LifecycleTerminalSucceeded || operation.CompletedAt == nil ||
		operation.RemovalMode != removalMode {
		return UninstallResult{}, lifecycleCleanupNotFinalized(operation.ID, false)
	}
	if s.lifecycleFinalizer == nil {
		return UninstallResult{}, lifecycleCleanupFinalizationFailure(
			operation.ID, LifecycleCleanupStepPhysicalPurge,
			errors.New("lifecycle cleanup finalizer is not configured"))
	}
	s.assetPublicationMu.Lock()
	assetErr := s.quarantineLifecycleAssetPublication(operation)
	s.assetPublicationMu.Unlock()
	if assetErr != nil {
		return UninstallResult{}, lifecycleCleanupFinalizationFailure(operation.ID,
			LifecycleCleanupStepQuarantineAssetPublication,
			fmt.Errorf("quarantine exact lifecycle asset publication (forced=%t): %w", operation.Forced, assetErr),
		)
	}
	// 卸载是附件引用的唯一释放点（禁用不释放）。必须在物理 purge 之前释放，
	// 失败时保留精确制品与身份，让同一次卸载可以幂等重试。
	if s.attachmentReferences != nil {
		if _, err := s.attachmentReferences.ReleaseExtensionAttachmentReferences(ctx, extensionID); err != nil {
			return UninstallResult{}, lifecycleCleanupFinalizationFailure(operation.ID,
				LifecycleCleanupStepReleaseAttachmentRefs,
				fmt.Errorf("release uninstalled extension attachment references: %w", err),
			)
		}
	}
	// Provider selections are Host registrations, not plugin-owned data. Clear
	// them before physical purge so a failed options write leaves the exact
	// package/identity available for an idempotent retry.
	if err := s.clearPluginProviderSelectionsWithAudit(
		ctx, extensionID, lifecycleOperationActorUserID(operation),
		lifecycleOperationAuditEventID(operation), "extension_uninstalled", clearMailProviderSelection,
	); err != nil {
		return UninstallResult{}, lifecycleCleanupFinalizationFailure(operation.ID,
			LifecycleCleanupStepClearProviderSelections,
			fmt.Errorf("clear uninstalled provider selections: %w", err),
		)
	}
	cleanup, err := s.lifecycleFinalizer(ctx, operation.ID)
	if err != nil {
		return UninstallResult{}, lifecycleCleanupFinalizationFailure(
			operation.ID, LifecycleCleanupStepPhysicalPurge, err)
	}
	if cleanup.OperationID != operation.ID || cleanup.Status != "finalized" || !cleanup.PhysicalPurgeComplete {
		return UninstallResult{}, lifecycleCleanupNotFinalized(operation.ID, true)
	}
	return UninstallResult{
		Uninstalled: true, ExtensionID: extensionID, OperationID: operation.ID,
		RemovalMode: removalMode, Replayed: replayed, Cleanup: &cleanup,
	}, nil
}

func exactLifecycleCopy(extension Extension) *Extension {
	copy := extension
	copy.StagedVersion = nil
	return &copy
}

func extensionFromExactVersion(base Extension, version ExtensionVersion) Extension {
	base.Name = version.Manifest.Name
	base.Version = version.Version
	base.Manifest = version.Manifest
	base.PackageDigest = version.PackageDigest
	base.AdminFrontendDigest = version.AdminFrontendDigest
	base.PackagePath = version.PackagePath
	base.ActiveVersionID = version.ID
	base.InstalledAt = version.InstalledAt
	base.StagedVersion = nil
	return base
}

func validLifecycleServiceIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func decodeLifecycleAuthoritySnapshot(value json.RawMessage) (LifecycleAuthoritySnapshot, error) {
	var authority LifecycleAuthoritySnapshot
	if err := json.Unmarshal(value, &authority); err != nil {
		return LifecycleAuthoritySnapshot{}, fmt.Errorf("decode lifecycle authority snapshot: %w", err)
	}
	if authority.SchemaVersion != LifecycleAuthoritySnapshotSchemaV1 || authority.ActorUserID <= 0 {
		return LifecycleAuthoritySnapshot{}, ErrLifecycleAuthorityNotFound
	}
	return authority, nil
}

// LifecycleCleanupStep 标记 finalizeLifecycleUninstall 失败时所处的具体步骤，
// 通过 503 响应的 data.details.failedStep 暴露给管理端与诊断工具。
type LifecycleCleanupStep string

const (
	LifecycleCleanupStepQuarantineAssetPublication LifecycleCleanupStep = "quarantine_asset_publication"
	LifecycleCleanupStepReleaseAttachmentRefs      LifecycleCleanupStep = "release_attachment_references"
	LifecycleCleanupStepClearProviderSelections    LifecycleCleanupStep = "clear_provider_selections"
	LifecycleCleanupStepPhysicalPurge              LifecycleCleanupStep = "physical_purge"
	LifecycleCleanupStepNotFinalized               LifecycleCleanupStep = "not_finalized"
)

// LifecycleCleanupFailure 是卸载物理清理失败的结构化载体。Err 保留完整的
// errors.Join 链（含 ErrLifecycleCleanupFinalization 哨兵）供日志与 errors.Is
// 判定使用；OperationID/Step/Retriable 由 controller 经 errors.As 提取后写入
// 503 响应的 data.details，让操作者不必依赖服务端日志即可判断卡在哪一步。
type LifecycleCleanupFailure struct {
	OperationID int64
	Step        LifecycleCleanupStep
	Retriable   bool
	Err         error
}

func (e *LifecycleCleanupFailure) Error() string {
	if e == nil || e.Err == nil {
		return "extensions: lifecycle cleanup failure"
	}
	return e.Err.Error()
}

func (e *LifecycleCleanupFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// lifecycleCleanupFinalizationFailure 包装 finalize 阶段的可重试失败，
// 保留 ErrLifecycleCleanupFinalization 哨兵以兼容既有 errors.Is 映射。
func lifecycleCleanupFinalizationFailure(
	operationID int64, step LifecycleCleanupStep, err error,
) *LifecycleCleanupFailure {
	return &LifecycleCleanupFailure{
		OperationID: operationID, Step: step, Retriable: true,
		Err: errors.Join(ErrLifecycleCleanupFinalization, err),
	}
}

func lifecycleCleanupNotFinalized(operationID int64, retriable bool) *LifecycleCleanupFailure {
	return &LifecycleCleanupFailure{
		OperationID: operationID, Step: LifecycleCleanupStepNotFinalized, Retriable: retriable,
		Err: fmt.Errorf("%w: operation %d", ErrLifecycleCleanupNotFinalized, operationID),
	}
}
