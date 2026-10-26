package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type exactLifecycleCoordinatorRunner interface {
	RunLifecycleInstance(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		LifecycleInvocation,
	) (LifecycleRunResult, error)
}

// LifecycleCoordinatorRuntimeAdmission is the Manager-owned exact-instance
// admission boundary required before lifecycle cleanup reaches a process.
type LifecycleCoordinatorRuntimeAdmission interface {
	AcquireRuntimeCall(context.Context, RuntimeInstanceIdentity, RuntimeCallClass) (*RuntimeAdmissionLease, error)
}

// ExactLifecycleCoordinatorRuntimeAdapter dispatches lifecycle work only to
// the process identity persisted by the Host gate. It deliberately has no
// extension-level runner, so a stale binding can never fall back to the active
// process for the same extension id.
type ExactLifecycleCoordinatorRuntimeAdapter struct {
	admission LifecycleCoordinatorRuntimeAdmission
	runner    exactLifecycleCoordinatorRunner
}

func NewExactLifecycleCoordinatorRuntimeAdapter(
	admission LifecycleCoordinatorRuntimeAdmission,
	starter *ProtocolStarter,
) *ExactLifecycleCoordinatorRuntimeAdapter {
	return &ExactLifecycleCoordinatorRuntimeAdapter{admission: admission, runner: starter}
}

// NewExactLifecycleCoordinatorRuntimeAdapter returns the only production
// coordinator adapter allowed to use this Manager's private process starter.
// Bootstrap therefore cannot accidentally pair admission from one Manager
// with process execution from another ProtocolStarter.
func (m *InstanceAdmission) NewExactLifecycleCoordinatorRuntimeAdapter() (*ExactLifecycleCoordinatorRuntimeAdapter, error) {
	if m == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	runner, ok := m.starter.(exactLifecycleCoordinatorRunner)
	if !ok || runner == nil {
		return nil, ErrProtocolInstanceUnsupported
	}
	return &ExactLifecycleCoordinatorRuntimeAdapter{admission: m, runner: runner}, nil
}

func (a *ExactLifecycleCoordinatorRuntimeAdapter) RunLifecycleAction(
	ctx context.Context,
	request extensions.LifecycleCoordinatorActionRequest,
	onProgress func(extensions.LifecycleCoordinatorActionProgress) error,
) (extensions.LifecycleCoordinatorActionResult, error) {
	if a == nil || a.admission == nil || a.runner == nil {
		return extensions.LifecycleCoordinatorActionResult{}, extensions.ErrRuntimeUnavailable
	}
	if ctx == nil {
		return extensions.LifecycleCoordinatorActionResult{}, exactCoordinatorInvalid("caller context is required")
	}
	if err := ctx.Err(); err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	selected, err := validateExactCoordinatorRequest(request)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	action, err := lifecycleCoordinatorAction(request.Action)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	input, err := lifecycleCoordinatorInput(selected.planVersion, request.InputDocument)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	lease, err := a.admission.AcquireRuntimeCall(ctx, selected.identity, RuntimeCallLifecycleCleanup)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	if lease == nil || lease.Context == nil || lease.Class != RuntimeCallLifecycleCleanup {
		if lease != nil {
			lease.Release()
		}
		return extensions.LifecycleCoordinatorActionResult{}, exactCoordinatorInvalid("Manager returned an invalid lifecycle cleanup lease")
	}
	defer lease.Release()
	if err := lease.Context.Err(); err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, exactCoordinatorLeaseError(err, lease.Context)
	}

	var latest LifecycleProgress
	run, runErr := a.runner.RunLifecycleInstance(lease.Context, selected.identity, selected.extension, LifecycleInvocation{
		Action: action, PlanVersion: selected.planVersion, StepID: request.StepID,
		Checkpoint: request.Checkpoint, Input: input, Forced: request.Forced,
		OnProgress: func(progress LifecycleProgress) error {
			latest = cloneLifecycleProgress(progress)
			if isLifecycleTerminal(progress.State) || onProgress == nil {
				return nil
			}
			mapped, mapErr := lifecycleCoordinatorProgressUpdate(progress)
			if mapErr != nil {
				return mapErr
			}
			return onProgress(mapped)
		},
	})
	result, mapErr := lifecycleCoordinatorResult(run, latest)
	if mapErr != nil {
		return extensions.LifecycleCoordinatorActionResult{}, mapErr
	}
	runErr = exactCoordinatorLeaseError(runErr, lease.Context)
	var remote *LifecycleRemoteError
	if errors.As(runErr, &remote) {
		result.Error = remote.LifecycleCoordinatorFailure()
	}
	return result, runErr
}

func exactCoordinatorLeaseError(runErr error, leaseContext context.Context) error {
	if leaseContext == nil {
		return runErr
	}
	cause := context.Cause(leaseContext)
	if cause == nil {
		return runErr
	}
	if runErr == nil {
		return cause
	}
	if errors.Is(runErr, cause) {
		return runErr
	}
	return errors.Join(runErr, cause)
}

type exactCoordinatorSelection struct {
	identity    RuntimeInstanceIdentity
	extension   extensions.Extension
	planVersion string
}

func validateExactCoordinatorRequest(request extensions.LifecycleCoordinatorActionRequest) (exactCoordinatorSelection, error) {
	if request.OperationID <= 0 || request.Attempt <= 0 || request.ActorUserID <= 0 || request.AuditEventID <= 0 {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("operation, attempt, actor, and audit identities are required")
	}
	position, expectedRole, err := exactCoordinatorActionContext(request.Operation, request.Action)
	if err != nil {
		return exactCoordinatorSelection{}, err
	}
	if request.RuntimeRole != expectedRole {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("action %q requires runtime role %q, got %q", request.Action, expectedRole, request.RuntimeRole)
	}
	expectedStepID := fmt.Sprintf("lifecycle.%s.%02d.%s", request.Operation, position, request.Action)
	if request.StepID != expectedStepID {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("stable step id %q does not match %q", request.StepID, expectedStepID)
	}
	if request.PlanVersion == "" || strings.TrimSpace(request.PlanVersion) != request.PlanVersion {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("exact operation plan version is required")
	}
	if _, _, err := protocolV2SchemaRef(request.PlanVersion); err != nil {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("invalid operation plan version: %v", err)
	}
	if err := validateExactCoordinatorRemovalContext(request); err != nil {
		return exactCoordinatorSelection{}, err
	}
	if err := validateExactCoordinatorArtifact("target", request.TargetExtension); err != nil {
		return exactCoordinatorSelection{}, err
	}
	targetInstanceRequired := request.RuntimeRole == extensions.LifecycleRuntimeTarget ||
		request.Operation == extensions.LifecycleMachineUpgrade || request.Operation == extensions.LifecycleMachineRollback
	if err := validateExactCoordinatorBinding("target", request.TargetBinding, request.TargetExtension, targetInstanceRequired); err != nil {
		return exactCoordinatorSelection{}, err
	}

	source, err := exactCoordinatorSourceExtension(request)
	if err != nil {
		return exactCoordinatorSelection{}, err
	}
	if source == nil {
		if request.SourceBinding != (extensions.LifecycleRuntimeBinding{}) {
			return exactCoordinatorSelection{}, exactCoordinatorInvalid("operation %q cannot carry a source runtime binding", request.Operation)
		}
	} else {
		if err := validateExactCoordinatorArtifact("source", *source); err != nil {
			return exactCoordinatorSelection{}, err
		}
		sourceInstanceRequired := request.RuntimeRole == extensions.LifecycleRuntimeSource ||
			request.Operation == extensions.LifecycleMachineUpgrade || request.Operation == extensions.LifecycleMachineRollback
		if err := validateExactCoordinatorBinding("source", request.SourceBinding, *source, sourceInstanceRequired); err != nil {
			return exactCoordinatorSelection{}, err
		}
	}

	selected := request.TargetExtension
	binding := request.TargetBinding
	if request.RuntimeRole == extensions.LifecycleRuntimeSource {
		if source == nil {
			return exactCoordinatorSelection{}, exactCoordinatorInvalid("source runtime role has no exact source artifact")
		}
		selected = *source
		binding = request.SourceBinding
	}
	if err := validateExactCoordinatorSelectedExtension(request.Extension, selected); err != nil {
		return exactCoordinatorSelection{}, err
	}
	if err := validateExactCoordinatorAuthority(request, request.TargetExtension); err != nil {
		return exactCoordinatorSelection{}, err
	}

	// The operation ledger records the target plan. Source cleanup executes the
	// source artifact's own frozen contract so upgrades may cross lifecycle
	// contract versions without weakening exact process selection.
	planVersion := selected.Manifest.Lifecycle.ContractVersion
	if request.RuntimeRole == extensions.LifecycleRuntimeTarget && planVersion != request.PlanVersion {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("target lifecycle contract %q does not match operation plan %q", planVersion, request.PlanVersion)
	}
	if _, _, err := protocolV2SchemaRef(planVersion); err != nil {
		return exactCoordinatorSelection{}, exactCoordinatorInvalid("invalid selected lifecycle contract: %v", err)
	}
	return exactCoordinatorSelection{
		identity:  RuntimeInstanceIdentity{ExtensionID: binding.ExtensionID, InstanceID: binding.RuntimeInstanceID},
		extension: selected, planVersion: planVersion,
	}, nil
}

func exactCoordinatorActionContext(
	operation extensions.LifecycleMachineOperation,
	action extensions.LifecycleMachineAction,
) (int, extensions.LifecycleCoordinatorRuntimeRole, error) {
	path, err := extensions.RecommendedLifecyclePath(operation)
	if err != nil {
		return 0, "", exactCoordinatorInvalid("unsupported lifecycle operation %q", operation)
	}
	position := -1
	for index, step := range path {
		if step.Action == action {
			position = index
			break
		}
	}
	if position < 0 || action == "" {
		return 0, "", exactCoordinatorInvalid("action %q does not belong to operation %q", action, operation)
	}
	// upgrade.after kept its original durable position when the Host activation
	// gate was added before it.
	if operation == extensions.LifecycleMachineUpgrade && action == extensions.LifecycleMachineUpgradeAfter {
		position = 8
	}
	switch action {
	case extensions.LifecycleMachineDisableAction,
		extensions.LifecycleMachineUpgradeBefore,
		extensions.LifecycleMachineUninstallPlan,
		extensions.LifecycleMachineUninstallStep,
		extensions.LifecycleMachineUninstallAfter:
		return position, extensions.LifecycleRuntimeSource, nil
	default:
		return position, extensions.LifecycleRuntimeTarget, nil
	}
}

func exactCoordinatorSourceExtension(request extensions.LifecycleCoordinatorActionRequest) (*extensions.Extension, error) {
	if request.SourceExtension != nil {
		value := *request.SourceExtension
		return &value, nil
	}
	switch request.Operation {
	case extensions.LifecycleMachineEnable, extensions.LifecycleMachineDisable, extensions.LifecycleMachineUninstall:
		value := request.TargetExtension
		return &value, nil
	case extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		return nil, exactCoordinatorInvalid("operation %q requires an exact source artifact", request.Operation)
	default:
		return nil, nil
	}
}

func validateExactCoordinatorArtifact(label string, extension extensions.Extension) error {
	if extension.ID == "" || extension.Version == "" || extension.PackageDigest == "" ||
		strings.TrimSpace(extension.ID) != extension.ID || strings.TrimSpace(extension.Version) != extension.Version ||
		strings.TrimSpace(extension.PackageDigest) != extension.PackageDigest || extension.Type != extensions.TypePlugin ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version ||
		extension.Manifest.Type != extensions.TypePlugin || extension.Manifest.Backend.ProtocolVersion != 2 ||
		extension.Manifest.Lifecycle == nil || strings.TrimSpace(extension.Manifest.Lifecycle.ContractVersion) == "" {
		return exactCoordinatorInvalid("%s artifact is not an exact protocol-v2 lifecycle plugin", label)
	}
	return nil
}

func validateExactCoordinatorBinding(
	label string,
	binding extensions.LifecycleRuntimeBinding,
	extension extensions.Extension,
	requireInstance bool,
) error {
	if binding.ExtensionID != extension.ID || binding.ExtensionVersion != extension.Version ||
		binding.PackageDigest != extension.PackageDigest || binding.VersionID != extension.ActiveVersionID {
		return exactCoordinatorInvalid("%s runtime binding does not match the exact artifact", label)
	}
	if binding.RuntimeInstanceID != strings.TrimSpace(binding.RuntimeInstanceID) ||
		(requireInstance && binding.RuntimeInstanceID == "") {
		return exactCoordinatorInvalid("%s runtime binding has no exact instance identity", label)
	}
	return nil
}

func validateExactCoordinatorSelectedExtension(actual, selected extensions.Extension) error {
	if actual.ID != selected.ID || actual.Version != selected.Version || actual.PackageDigest != selected.PackageDigest ||
		actual.ActiveVersionID != selected.ActiveVersionID || actual.Type != selected.Type || actual.Source != selected.Source {
		return exactCoordinatorInvalid("selected extension does not match runtime role artifact")
	}
	actualManifest, err := protocolRuntimeManifestDigest(actual.Manifest)
	if err != nil {
		return exactCoordinatorInvalid("encode selected extension manifest: %v", err)
	}
	selectedManifest, err := protocolRuntimeManifestDigest(selected.Manifest)
	if err != nil {
		return exactCoordinatorInvalid("encode role extension manifest: %v", err)
	}
	if actualManifest != selectedManifest {
		return exactCoordinatorInvalid("selected extension manifest does not match runtime role artifact")
	}
	return nil
}

func validateExactCoordinatorRemovalContext(request extensions.LifecycleCoordinatorActionRequest) error {
	if request.Forced && request.Operation != extensions.LifecycleMachineUninstall {
		return exactCoordinatorInvalid("forced execution is uninstall-only")
	}
	if request.Operation == extensions.LifecycleMachineUninstall {
		switch request.RemovalMode {
		case extensions.LifecycleRemovalPreserve,
			extensions.LifecycleRemovalExportThenRemove,
			extensions.LifecycleRemovalComplete:
			return nil
		default:
			return exactCoordinatorInvalid("uninstall requires an explicit removal mode")
		}
	}
	if request.RemovalMode != "" {
		return exactCoordinatorInvalid("removal mode is uninstall-only")
	}
	return nil
}

func validateExactCoordinatorAuthority(request extensions.LifecycleCoordinatorActionRequest, target extensions.Extension) error {
	if len(bytes.TrimSpace(request.AuthoritySnapshot)) == 0 {
		return exactCoordinatorInvalid("frozen lifecycle authority is required")
	}
	var authority extensions.LifecycleAuthoritySnapshot
	if err := json.Unmarshal(request.AuthoritySnapshot, &authority); err != nil {
		return exactCoordinatorInvalid("decode frozen lifecycle authority: %v", err)
	}
	impact := authority.Impact
	authorityActorUserID := request.AuthorityActorUserID
	if authorityActorUserID == 0 {
		// Compatibility for callers created before recovery actors were split
		// from the immutable exact-artifact authority.
		authorityActorUserID = request.ActorUserID
	}
	if authorityActorUserID <= 0 {
		return exactCoordinatorInvalid("frozen lifecycle authority actor is required")
	}
	if authority.SchemaVersion != extensions.LifecycleAuthoritySnapshotSchemaV1 ||
		authority.AuthorityType != request.AuthorityType || authority.ActorUserID != authorityActorUserID ||
		impact.SchemaVersion != extensions.TrustImpactSchemaV2 || impact.Action != extensions.TrustActionEnable ||
		impact.ExtensionID != target.ID || impact.ExtensionVersion != target.Version ||
		impact.ExtensionType != target.Type || impact.Source != target.Source ||
		impact.PackageDigest != target.PackageDigest || impact.Digest == "" ||
		impact.ArtifactDigests["package"] != target.PackageDigest {
		return exactCoordinatorInvalid("frozen lifecycle authority does not match the target authority actor and artifact")
	}
	switch request.AuthorityType {
	case extensions.LifecycleAuthorityBuiltin:
		if request.TrustGrantID != 0 || authority.Grant != nil || target.Source != extensions.SourceBuiltin {
			return exactCoordinatorInvalid("invalid builtin lifecycle authority")
		}
	case extensions.LifecycleAuthorityTrustGrant:
		grant := authority.Grant
		if request.TrustGrantID <= 0 || grant == nil || grant.ID != request.TrustGrantID ||
			grant.ExtensionID != target.ID || grant.ExtensionVersion != target.Version ||
			grant.PackageDigest != target.PackageDigest || grant.Action != extensions.TrustActionEnable ||
			grant.ImpactDigest != impact.Digest || grant.RevokedAt != nil {
			return exactCoordinatorInvalid("invalid exact-artifact lifecycle trust grant")
		}
	default:
		return exactCoordinatorInvalid("unsupported lifecycle authority %q", request.AuthorityType)
	}
	return nil
}

func exactCoordinatorInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidLifecycleRun, fmt.Sprintf(format, args...))
}

var _ extensions.LifecycleCoordinatorRuntime = (*ExactLifecycleCoordinatorRuntimeAdapter)(nil)

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) NewExactLifecycleCoordinatorRuntimeAdapter() (*ExactLifecycleCoordinatorRuntimeAdapter, error) {
	if m == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	return m.admission.NewExactLifecycleCoordinatorRuntimeAdapter()
}
