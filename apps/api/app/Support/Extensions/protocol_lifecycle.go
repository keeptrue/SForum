package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

type LifecycleAction = protocolv2.LifecycleAction
type LifecycleProgressState = protocolv2.ProgressState

const (
	LifecycleActionInstallPlan    = protocolv2.LifecycleAction_LIFECYCLE_ACTION_INSTALL_PLAN
	LifecycleActionInstall        = protocolv2.LifecycleAction_LIFECYCLE_ACTION_INSTALL
	LifecycleActionEnable         = protocolv2.LifecycleAction_LIFECYCLE_ACTION_ENABLE
	LifecycleActionDisable        = protocolv2.LifecycleAction_LIFECYCLE_ACTION_DISABLE
	LifecycleActionUpgradePlan    = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_PLAN
	LifecycleActionUpgradeBefore  = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_BEFORE
	LifecycleActionUpgradeAfter   = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_AFTER
	LifecycleActionRollback       = protocolv2.LifecycleAction_LIFECYCLE_ACTION_ROLLBACK
	LifecycleActionUninstallPlan  = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_PLAN
	LifecycleActionUninstall      = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL
	LifecycleActionUninstallAfter = protocolv2.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER

	LifecycleProgressPlanned   = protocolv2.ProgressState_PROGRESS_STATE_PLANNED
	LifecycleProgressRunning   = protocolv2.ProgressState_PROGRESS_STATE_RUNNING
	LifecycleProgressWaiting   = protocolv2.ProgressState_PROGRESS_STATE_WAITING
	LifecycleProgressSucceeded = protocolv2.ProgressState_PROGRESS_STATE_SUCCEEDED
	LifecycleProgressFailed    = protocolv2.ProgressState_PROGRESS_STATE_FAILED
	LifecycleProgressCancelled = protocolv2.ProgressState_PROGRESS_STATE_CANCELLED
)

var (
	ErrLifecycleV2Unsupported = errors.New("protocol v2 lifecycle is not supported by this runtime")
	ErrInvalidLifecycleRun    = errors.New("invalid protocol v2 lifecycle run")
	ErrInvalidLifecycleStream = errors.New("invalid protocol v2 lifecycle progress stream")
)

// LifecycleInvocation is one stable, resumable lifecycle step. Checkpoint is
// opaque to the Host; the owning state machine persists and returns it.
type LifecycleInvocation struct {
	Action      LifecycleAction
	PlanVersion string
	StepID      string
	Checkpoint  string
	Input       *protocolv2.TypedDocument
	DryRun      bool
	Forced      bool
	OnProgress  func(LifecycleProgress) error
}

type LifecycleProgress struct {
	State          LifecycleProgressState
	CompletedUnits uint32
	TotalUnits     uint32
	Checkpoint     string
	Message        string
	Result         *protocolv2.TypedDocument
}

type LifecycleRunResult struct {
	StepID     string
	State      LifecycleProgressState
	Checkpoint string
	// CheckpointSchema preserves the frozen manifest association. Checkpoint is
	// still an opaque wire string and is not structurally validated against it.
	CheckpointSchema string
	Result           *protocolv2.TypedDocument
	Progress         []LifecycleProgress
}

// LifecycleRemoteError preserves a plugin's terminal typed failure while
// allowing cancellation/deadline handling through errors.Is.
type LifecycleRemoteError struct {
	StepID     string
	Checkpoint string
	State      LifecycleProgressState
	Code       protocolv2.ErrorCode
	Reason     string
	Message    string
	Retryable  bool
	RetryAfter time.Time
	Metadata   map[string]string
}

func (e *LifecycleRemoteError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return e.Message
	}
	return e.Reason + ": " + e.Message
}

func (e *LifecycleRemoteError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch {
	case e.State == LifecycleProgressCancelled || e.Code == protocolv2.ErrorCode_ERROR_CODE_CANCELLED:
		return context.Canceled
	case e.Code == protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (e *LifecycleRemoteError) LifecycleCoordinatorFailure() extensions.LifecycleExecutionError {
	if e == nil {
		return extensions.LifecycleExecutionError{}
	}
	var retryAfter *time.Time
	if !e.RetryAfter.IsZero() {
		value := e.RetryAfter
		retryAfter = &value
	}
	var metadata json.RawMessage
	if len(e.Metadata) > 0 {
		metadata, _ = json.Marshal(e.Metadata)
	}
	return extensions.LifecycleExecutionError{
		Code: e.Code.String(), Reason: e.Reason, Message: e.Message,
		Retryable: e.Retryable, RetryAfter: retryAfter, Metadata: metadata,
	}
}

type pluginLifecycleContextInvoker interface {
	RunLifecycleContext(context.Context, LifecycleInvocation) (LifecycleRunResult, error)
}

// RunLifecycle invokes only the typed v2 lifecycle surface. Protocol-v1
// packages remain runnable, but never report lifecycle v2 as supported.
func (s *ProtocolStarter) RunLifecycle(ctx context.Context, extension extensions.Extension, invocation LifecycleInvocation) (LifecycleRunResult, error) {
	if s == nil {
		return LifecycleRunResult{}, extensions.ErrRuntimeUnavailable
	}
	if ctx == nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: caller context is required", ErrInvalidLifecycleRun)
	}
	protocol := s.protocolFor(extension.ID)
	if protocol == nil {
		return LifecycleRunResult{}, extensions.ErrRuntimeUnavailable
	}
	runner, ok := protocol.(pluginLifecycleContextInvoker)
	if !ok {
		return LifecycleRunResult{}, ErrLifecycleV2Unsupported
	}
	return runner.RunLifecycleContext(ctx, cloneLifecycleInvocation(invocation))
}

// RunLifecycleInstance invokes lifecycle code on one staged/published/retained
// V2 process. It never falls back to the extension's current active instance.
func (s *ProtocolStarter) RunLifecycleInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	invocation LifecycleInvocation,
) (LifecycleRunResult, error) {
	if s == nil {
		return LifecycleRunResult{}, extensions.ErrRuntimeUnavailable
	}
	if ctx == nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: caller context is required", ErrInvalidLifecycleRun)
	}
	if err := ctx.Err(); err != nil {
		return LifecycleRunResult{}, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return LifecycleRunResult{}, err
	}
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return LifecycleRunResult{}, err
	}
	defer unlock()
	instance := s.protocolInstance(identity)
	if instance == nil {
		return LifecycleRunResult{}, protocolInstanceNotFound(identity)
	}
	if instance.protocolVersion != 2 {
		return LifecycleRunResult{}, ErrLifecycleV2Unsupported
	}
	version := extension.Version
	if version == "" {
		version = extension.Manifest.Version
	}
	manifestDigest, err := protocolRuntimeManifestDigest(extension.Manifest)
	if err != nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: encode caller manifest: %v", ErrInvalidLifecycleRun, err)
	}
	v2, ok := instance.protocol.(*protocolV2Client)
	if !ok || v2.identity == nil ||
		identity.ExtensionID != extension.ID || instance.extensionVersion != version ||
		instance.artifactDigest != extension.PackageDigest || instance.manifestDigest != manifestDigest ||
		v2.identity.GetExtensionId() != identity.ExtensionID || v2.identity.GetInstanceId() != identity.InstanceID ||
		v2.identity.GetExtensionVersion() != version || v2.identity.GetArtifactDigest() != extension.PackageDigest {
		return LifecycleRunResult{}, fmt.Errorf("%w: lifecycle caller does not match the frozen exact runtime", ErrInvalidLifecycleRun)
	}
	runner, ok := instance.protocol.(pluginLifecycleContextInvoker)
	if !ok {
		return LifecycleRunResult{}, ErrLifecycleV2Unsupported
	}
	s.mu.Lock()
	s.recordProtocolCallLocked(identity.ExtensionID)
	s.mu.Unlock()
	return runner.RunLifecycleContext(ctx, cloneLifecycleInvocation(invocation))
}

func lifecycleOperationContract(lifecycle *extensions.ManifestLifecycle, action LifecycleAction) (string, string, string, error) {
	if lifecycle == nil {
		return "", "", "", fmt.Errorf("%w: the frozen manifest declares no lifecycle contract", ErrInvalidLifecycleRun)
	}
	operation := lifecycle.Install
	switch action {
	case LifecycleActionInstallPlan, LifecycleActionInstall:
		operation = lifecycle.Install
	case LifecycleActionEnable:
		operation = lifecycle.Enable
	case LifecycleActionDisable:
		operation = lifecycle.Disable
	case LifecycleActionUpgradePlan, LifecycleActionUpgradeBefore, LifecycleActionUpgradeAfter:
		operation = lifecycle.Upgrade
	case LifecycleActionRollback:
		operation = lifecycle.Rollback
	case LifecycleActionUninstallPlan, LifecycleActionUninstall, LifecycleActionUninstallAfter:
		operation = lifecycle.Uninstall
	default:
		return "", "", "", fmt.Errorf("%w: lifecycle action %s is unsupported", ErrInvalidLifecycleRun, action)
	}
	if operation == nil {
		return "", "", "", fmt.Errorf("%w: lifecycle action %s is not declared by the frozen manifest", ErrInvalidLifecycleRun, action)
	}
	return lifecycle.ContractVersion, operation.ProgressSchema, operation.CheckpointSchema, nil
}

func cloneManifestLifecycle(value *extensions.ManifestLifecycle) *extensions.ManifestLifecycle {
	if value == nil {
		return nil
	}
	result := *value
	if value.Install != nil {
		cloned := *value.Install
		result.Install = &cloned
	}
	if value.Enable != nil {
		cloned := *value.Enable
		result.Enable = &cloned
	}
	if value.Disable != nil {
		cloned := *value.Disable
		result.Disable = &cloned
	}
	if value.Upgrade != nil {
		cloned := *value.Upgrade
		result.Upgrade = &cloned
	}
	if value.Rollback != nil {
		cloned := *value.Rollback
		result.Rollback = &cloned
	}
	if value.Uninstall != nil {
		cloned := *value.Uninstall
		result.Uninstall = &cloned
	}
	return &result
}

func cloneLifecycleInvocation(value LifecycleInvocation) LifecycleInvocation {
	if value.Input != nil {
		value.Input = proto.Clone(value.Input).(*protocolv2.TypedDocument)
	}
	return value
}

func cloneLifecycleDocument(value *protocolv2.TypedDocument) *protocolv2.TypedDocument {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.TypedDocument)
}

func cloneLifecycleProgress(value LifecycleProgress) LifecycleProgress {
	value.Result = cloneLifecycleDocument(value.Result)
	return value
}

var _ error = (*LifecycleRemoteError)(nil)
var _ extensions.LifecycleCoordinatorFailureCarrier = (*LifecycleRemoteError)(nil)
