package extensionsruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrLifecycleBoundaryFenceInvalid  = errors.New("extension lifecycle boundary fence is invalid")
	ErrLifecycleBoundaryFenceConflict = errors.New("extension lifecycle boundary exact fence conflict")
)

type lifecycleBoundaryCallFence struct {
	OperationID  int64
	Operation    extensions.LifecycleMachineOperation
	State        extensions.LifecycleMachineState
	Position     int
	StepID       string
	Attempt      int
	ActorUserID  int64
	AuditEventID int64
	Source       lifecyclePublicationArtifact
	Target       lifecyclePublicationArtifact
}

func lifecycleBoundaryCallFenceFor(request LifecycleBoundaryRequest) (lifecycleBoundaryCallFence, error) {
	if request.OperationID <= 0 || request.Attempt <= 0 || request.ActorUserID <= 0 ||
		request.AuditEventID <= 0 || request.StepID == "" ||
		request.StepID != strings.TrimSpace(request.StepID) || len(request.StepID) > 512 {
		return lifecycleBoundaryCallFence{}, ErrLifecycleBoundaryFenceInvalid
	}
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil || request.Position < 0 || request.Position >= len(path) || path[request.Position].Action != "" {
		return lifecycleBoundaryCallFence{}, ErrLifecycleBoundaryFenceInvalid
	}
	wantStepID := fmt.Sprintf(
		"lifecycle.%s.%02d.host.%s", request.Operation, request.Position, path[request.Position].State,
	)
	if request.StepID != wantStepID {
		return lifecycleBoundaryCallFence{}, ErrLifecycleBoundaryFenceInvalid
	}
	if err := validateExactCoordinatorRemovalContext(extensions.LifecycleCoordinatorActionRequest{
		Operation: request.Operation, RemovalMode: request.RemovalMode, Forced: request.Forced,
	}); err != nil {
		return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: %v", ErrLifecycleBoundaryFenceInvalid, err)
	}

	target, err := lifecycleBoundaryFenceArtifact(
		"target", request.TargetExtension, request.TargetBinding,
		lifecycleHostRequiresTarget(request.Operation),
	)
	if err != nil {
		return lifecycleBoundaryCallFence{}, err
	}
	fence := lifecycleBoundaryCallFence{
		OperationID: request.OperationID, Operation: request.Operation,
		State:    path[request.Position].State,
		Position: request.Position, StepID: request.StepID, Attempt: request.Attempt,
		ActorUserID: request.ActorUserID, AuditEventID: request.AuditEventID,
		Target: target,
	}

	if lifecycleHostRequiresSource(request.Operation) {
		if request.SourceExtension == nil {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: source artifact is required", ErrLifecycleBoundaryFenceInvalid)
		}
		source, sourceErr := lifecycleBoundaryFenceArtifact(
			"source", *request.SourceExtension, request.SourceBinding, true,
		)
		if sourceErr != nil {
			return lifecycleBoundaryCallFence{}, sourceErr
		}
		if source.ExtensionID != target.ExtensionID {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: source and target extension ids differ", ErrLifecycleBoundaryFenceInvalid)
		}
		fence.Source = source
	}

	switch request.Operation {
	case extensions.LifecycleMachineInstall:
		if request.SourceExtension != nil {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: install cannot carry a source", ErrLifecycleBoundaryFenceInvalid)
		}
	case extensions.LifecycleMachineEnable:
		if request.SourceExtension != nil {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: enable cannot carry a distinct source", ErrLifecycleBoundaryFenceInvalid)
		}
	case extensions.LifecycleMachineDisable, extensions.LifecycleMachineUninstall:
		if !fence.Source.sameArtifact(fence.Target) {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: deactivation source changed", ErrLifecycleBoundaryFenceInvalid)
		}
	case extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		if fence.Source.sameArtifact(fence.Target) {
			return lifecycleBoundaryCallFence{}, fmt.Errorf("%w: version transition needs distinct artifacts", ErrLifecycleBoundaryFenceInvalid)
		}
	default:
		return lifecycleBoundaryCallFence{}, ErrLifecycleBoundaryFenceInvalid
	}
	return fence, nil
}

func lifecycleBoundaryFenceArtifact(
	label string,
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	requireRuntime bool,
) (lifecyclePublicationArtifact, error) {
	if err := validateExactCoordinatorArtifact(label, extension); err != nil ||
		extension.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(extension.PackageDigest) {
		return lifecyclePublicationArtifact{}, fmt.Errorf("%w: %s artifact is not exact", ErrLifecycleBoundaryFenceInvalid, label)
	}
	if err := validateExactCoordinatorBinding(label, binding, extension, requireRuntime); err != nil ||
		len(binding.RuntimeInstanceID) > 512 {
		return lifecyclePublicationArtifact{}, fmt.Errorf("%w: %s binding is not exact", ErrLifecycleBoundaryFenceInvalid, label)
	}
	return lifecyclePublicationArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID, Present: true,
	}, nil
}

func validateLifecycleBoundaryPostgresFence(
	ctx context.Context,
	tx pgx.Tx,
	fence lifecycleBoundaryCallFence,
	write bool,
) error {
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	var operation, state, currentStepID, extensionID, extensionVersion, packageDigest string
	var terminalResult sql.NullString
	var completedAt sql.NullTime
	err := tx.QueryRow(ctx, `
			SELECT operation, state, current_step_id,
			       extension_id, extension_version, package_digest,
			       terminal_result, completed_at
		FROM extension_lifecycle_operations
		WHERE id = $1`+lock,
		fence.OperationID,
	).Scan(
		&operation, &state, &currentStepID,
		&extensionID, &extensionVersion, &packageDigest, &terminalResult, &completedAt,
	)
	if err != nil {
		return mapLifecycleBoundaryFenceError("load lifecycle operation fence", err)
	}
	if operation != string(fence.Operation) || state != string(fence.State) || currentStepID != fence.StepID ||
		extensionID != fence.Target.ExtensionID ||
		extensionVersion != fence.Target.ExtensionVersion || packageDigest != fence.Target.PackageDigest ||
		terminalResult.Valid || completedAt.Valid {
		return fmt.Errorf(
			"%w: operation row operation=%s state=%s step=%s extension=%s version=%s terminal=%t completed=%t",
			ErrLifecycleBoundaryFenceConflict, operation, state, currentStepID, extensionID,
			extensionVersion, terminalResult.Valid, completedAt.Valid,
		)
	}

	var action, status string
	var attempt int
	var actorUserID, auditEventID int64
	err = tx.QueryRow(ctx, `
			SELECT attempt, lifecycle_action, status,
			       COALESCE(actor_user_id, 0), COALESCE(audit_event_id, 0)
			FROM extension_lifecycle_steps
			WHERE operation_id = $1 AND step_id = $2
			ORDER BY attempt DESC
			LIMIT 1
			FOR SHARE
		`, fence.OperationID, fence.StepID).Scan(
		&attempt, &action, &status, &actorUserID, &auditEventID,
	)
	if err != nil {
		return mapLifecycleBoundaryFenceError("load lifecycle step fence", err)
	}
	if attempt != fence.Attempt || action != "host.gate" ||
		(status != "running" && status != "waiting" && status != "succeeded") ||
		actorUserID != fence.ActorUserID || auditEventID != fence.AuditEventID {
		return fmt.Errorf(
			"%w: step row attempt=%d action=%s status=%s actor=%d audit=%d",
			ErrLifecycleBoundaryFenceConflict, attempt, action, status, actorUserID, auditEventID,
		)
	}
	return nil
}

func mapLifecycleBoundaryFenceError(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrLifecycleBoundaryFenceConflict, action)
	}
	return fmt.Errorf("%s: %w", action, err)
}
