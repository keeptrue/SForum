package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// RecoverLifecycleOperation 只恢复指定 durable operation。原 authority、artifact、
// idempotency key 和 fingerprint 保持冻结；当前操作者仅作为新恢复决定的权限与审计主体。
func (s *LifecycleService) RecoverLifecycleOperation(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	operationID int64,
	input LifecycleRecoveryInput,
) (LifecycleOperationDetail, error) {
	if !canManagePlugins(actor) {
		return LifecycleOperationDetail{}, identity.ErrPermissionDenied
	}
	if s == nil || s.lifecycleInspector == nil || s.lifecycleCoordinator == nil ||
		s.lifecyclePreflight == nil || s.lifecycleAuthority == nil {
		return LifecycleOperationDetail{}, ErrLifecycleCoordinatorUnavailable
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" || operationID <= 0 {
		return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
	}
	decision, reason, err := validateLifecycleRecoveryRequest(actor, input)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	operation, err := s.lifecycleInspector.Operation(ctx, extensionID, operationID)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	if operation.ID != operationID || operation.ExtensionID != extensionID {
		return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
	}
	if operation.CompletedAt == nil ||
		(operation.TerminalResult != LifecycleTerminalFailed && operation.TerminalResult != LifecycleTerminalCancelled) {
		return LifecycleOperationDetail{}, ErrLifecycleNotRecoverable
	}
	operationKind := LifecycleMachineOperation(operation.Operation)
	if input.EscalateForced && operationKind != LifecycleMachineUninstall {
		return LifecycleOperationDetail{}, fmt.Errorf("%w: forced recovery is uninstall-only", ErrLifecycleCoordinatorInvalid)
	}

	current, err := s.store.Get(ctx, extensionID)
	if err != nil {
		return LifecycleOperationDetail{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	request, runInput, err := s.rebuildLifecycleReplay(ctx, current, operation)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	auditEventID, err := s.appendLifecycleRecoveryAudit(ctx, actor, operation, decision, reason, input.EscalateForced)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	if err := s.lifecyclePreflight(ctx, request.operation, request.source, request.target); err != nil {
		return LifecycleOperationDetail{}, errors.Join(ErrPreflightFailed, err)
	}
	runInput.Retry = true
	runInput.SkipFailedStep = decision == LifecycleRecoverySkipStep
	if runInput.SkipFailedStep {
		runInput.SkipReason = reason
	} else {
		runInput.RecoveryReason = reason
	}
	runInput.RecoveryActorUserID = actor.ID
	runInput.RecoveryAuditEventID = auditEventID
	runInput.EscalateForced = input.EscalateForced

	result, err := s.lifecycleCoordinator.Run(ctx, runInput)
	if err != nil {
		return LifecycleOperationDetail{}, lifecycleCoordinatorServiceError(err)
	}
	// Recovery belongs to an existing logical request, so compatibility events
	// remain one-shot and are never emitted by this mutation.
	result.Replayed = true
	if _, err := s.finishLifecycleV2(ctx, actor, request, result); err != nil {
		return LifecycleOperationDetail{}, err
	}
	if operationKind == LifecycleMachineUninstall {
		if _, err := s.finalizeLifecycleUninstall(ctx, extensionID, operation.RemovalMode, result.Operation, true); err != nil {
			return LifecycleOperationDetail{}, err
		}
	}
	return s.lifecycleOperationDetail(ctx, extensionID, operationID)
}

func validateLifecycleRecoveryRequest(actor identity.Actor, input LifecycleRecoveryInput) (string, string, error) {
	reason := strings.TrimSpace(input.Reason)
	if reason != input.Reason || len(reason) > 4096 {
		return "", "", fmt.Errorf("%w: invalid recovery reason", ErrLifecycleCoordinatorInvalid)
	}
	switch input.Decision {
	case LifecycleRecoveryRetry:
	case LifecycleRecoverySkipStep:
		if reason == "" {
			return "", "", fmt.Errorf("%w: skip-step requires a reason", ErrLifecycleCoordinatorInvalid)
		}
	default:
		return "", "", fmt.Errorf("%w: invalid recovery decision", ErrLifecycleCoordinatorInvalid)
	}
	if input.EscalateForced {
		if !actor.IsSuperAdmin() {
			return "", "", identity.ErrPermissionDenied
		}
		if reason == "" {
			return "", "", fmt.Errorf("%w: forced recovery requires a residual-risk reason", ErrLifecycleCoordinatorInvalid)
		}
	}
	return input.Decision, reason, nil
}

func (s *serviceCore) appendLifecycleRecoveryAudit(
	ctx context.Context,
	actor identity.Actor,
	operation LifecycleOperation,
	decision string,
	reason string,
	escalateForced bool,
) (int64, error) {
	writer, ok := s.auditor.(audit.IDWriter)
	if !ok || writer == nil {
		return 0, ErrLifecycleCoordinatorUnavailable
	}
	id, err := writer.AppendReturningID(ctx, audit.Event{
		ActorUserID: actor.ID,
		Action:      audit.ActionExtensionLifecycleRecovery,
		Metadata: map[string]any{
			"extensionId": operation.ExtensionID, "operationId": operation.ID,
			"operation": operation.Operation, "decision": decision,
			"reason": reason, "escalateForced": escalateForced,
		},
	})
	if err != nil {
		return 0, errors.Join(ErrLifecycleCoordinatorUnavailable, fmt.Errorf("write lifecycle recovery audit: %w", err))
	}
	if id <= 0 {
		return 0, ErrLifecycleCoordinatorUnavailable
	}
	return id, nil
}
