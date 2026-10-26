package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const maximumIdentityProviderDocumentBytes = 1 << 20

var (
	ErrIdentityProviderInvocationInvalid = errors.New("identity provider invocation is invalid")
	ErrIdentityProviderUnavailable       = errors.New("identity provider runtime is unavailable")
	ErrIdentityProviderStale             = errors.New("identity provider exact contract is stale")
	ErrIdentityProviderAcceptFailed      = errors.New("identity provider Host acceptance failed")
)

// IdentityProviderInvocation contains only Host-selected workflow input. The
// executable declaration, timeout, failure policy, and Schema references are
// always resolved from the immutable Identity Registry publication.
type IdentityProviderInvocation struct {
	ProviderID  string
	Operation   string
	ActorUserID int64
	Input       map[string]any
}

// IdentityProviderInvocationResult is the exact proposal presented to the
// Host acceptance boundary. A provider response is never authority by itself.
type IdentityProviderInvocationResult struct {
	RegistryRevision uint64
	RegistryDigest   string
	Provider         identityregistry.ProviderContribution
	Operation        identityregistry.ProviderOperation
	Output           map[string]any
}

// IdentityProviderCommitFence is a one-shot final admission validation. Accept
// must call it inside its Host transaction immediately before committing the
// effect and audit. It does not provide a cross-system CAS: the Host store must
// keep its own exact-artifact checks and commit rollback-capable. The fence and
// transaction must remain synchronous and must not escape the Accept callback.
type IdentityProviderCommitFence func() error

// IdentityProviderAccept applies the Host-owned effect and audit while the
// exact runtime admission lease is still held. The callback must keep its own
// transaction rollback-capable when the fence rejects, honor ctx, and wait for
// every fence/effect operation before returning.
type IdentityProviderAccept func(
	context.Context,
	IdentityProviderInvocationResult,
	IdentityProviderCommitFence,
) error

// IdentityProviderRuntime binds one Manager to one authoritative Identity
// Registry. Callers cannot substitute a different Registry per invocation.
type IdentityProviderRuntime struct {
	manager  *Manager
	registry *identityregistry.Registry
}

func NewIdentityProviderRuntime(
	manager *Manager,
	registry *identityregistry.Registry,
) (*IdentityProviderRuntime, error) {
	if manager == nil || registry == nil {
		return nil, ErrIdentityProviderInvocationInvalid
	}
	return &IdentityProviderRuntime{manager: manager, registry: registry}, nil
}

type exactIdentityProviderInvoker interface {
	InvokeIdentityProviderInstance(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error)
}

// Invoke resolves and invokes one currently active Identity provider. It
// deliberately has no Core or generic-provider fallback. Callers that already
// hold an immutable Registry claim must use InvokeExact so a same-id artifact
// replacement is rejected before any subprocess call.
func (r *IdentityProviderRuntime) Invoke(
	ctx context.Context,
	input IdentityProviderInvocation,
	accept IdentityProviderAccept,
) (IdentityProviderInvocationResult, error) {
	return r.invoke(ctx, input, nil, accept)
}

// InvokeExact invokes only the supplied immutable provider claim. The current
// Registry must still contain that exact artifact and contract before Schema
// validation, runtime admission, or subprocess transport begins.
func (r *IdentityProviderRuntime) InvokeExact(
	ctx context.Context,
	expected identityregistry.ProviderContribution,
	input IdentityProviderInvocation,
	accept IdentityProviderAccept,
) (IdentityProviderInvocationResult, error) {
	frozen := cloneIdentityProviderContribution(expected)
	return r.invoke(ctx, input, &frozen, accept)
}

func (r *IdentityProviderRuntime) invoke(
	ctx context.Context,
	input IdentityProviderInvocation,
	expected *identityregistry.ProviderContribution,
	accept IdentityProviderAccept,
) (IdentityProviderInvocationResult, error) {
	if r == nil || r.manager == nil || r.registry == nil || ctx == nil || accept == nil || input.ActorUserID < 0 {
		return IdentityProviderInvocationResult{}, ErrIdentityProviderInvocationInvalid
	}
	if err := ctx.Err(); err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	input.Operation = strings.ToLower(strings.TrimSpace(input.Operation))
	if input.ProviderID == "" || input.Operation == "" {
		return IdentityProviderInvocationResult{}, ErrIdentityProviderInvocationInvalid
	}

	resolution, err := r.registry.ResolveProviderSnapshot(input.ProviderID)
	if err != nil {
		return IdentityProviderInvocationResult{}, mapIdentityProviderResolutionError(err)
	}
	if expected != nil {
		if strings.ToLower(strings.TrimSpace(expected.ID)) != input.ProviderID {
			return IdentityProviderInvocationResult{}, ErrIdentityProviderInvocationInvalid
		}
		if !sameIdentityProviderContract(resolution.Provider, *expected) {
			return IdentityProviderInvocationResult{}, ErrIdentityProviderStale
		}
	}
	provider := resolution.Provider
	operation, found := exactIdentityProviderOperation(provider, input.Operation)
	if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" || !found {
		return IdentityProviderInvocationResult{}, ErrIdentityProviderUnavailable
	}
	if err := validateIdentityProviderResolution(r.registry, resolution); err != nil {
		return IdentityProviderInvocationResult{}, err
	}

	requestInput, err := cloneIdentityProviderDocument(input.Input)
	if err != nil {
		if staleErr := validateIdentityProviderResolution(r.registry, resolution); staleErr != nil {
			return IdentityProviderInvocationResult{}, staleErr
		}
		return IdentityProviderInvocationResult{}, fmt.Errorf("%w: clone input: %v", ErrIdentityProviderInvocationInvalid, err)
	}
	claim := identityregistry.ProviderOperationSchemaClaim{
		ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
		Operation: operation.Name, Artifact: provider.Artifact,
	}
	if err := r.registry.ValidateProviderOperationInput(claim, requestInput); err != nil {
		if staleErr := validateIdentityProviderResolution(r.registry, resolution); staleErr != nil {
			return IdentityProviderInvocationResult{}, staleErr
		}
		return IdentityProviderInvocationResult{}, fmt.Errorf("%w: input Schema: %w", ErrIdentityProviderInvocationInvalid, err)
	}

	identity := RuntimeInstanceIdentity{
		ExtensionID: provider.Artifact.ExtensionID,
		InstanceID:  provider.Artifact.RuntimeInstanceID,
	}
	lease, err := r.manager.AcquireRuntimeCall(ctx, identity, RuntimeCallProvider)
	if err != nil {
		return IdentityProviderInvocationResult{}, errors.Join(ErrIdentityProviderUnavailable, err)
	}
	defer lease.Release()

	if cause := context.Cause(lease.Context); cause != nil {
		return IdentityProviderInvocationResult{}, cause
	}
	extension, err := r.manager.exactIdentityManagedRuntime(identity, provider.Artifact)
	if err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	if err := validateIdentityProviderResolution(r.registry, resolution); err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	if cause := context.Cause(lease.Context); cause != nil {
		return IdentityProviderInvocationResult{}, cause
	}

	invoker, ok := r.manager.starter.(exactIdentityProviderInvoker)
	if !ok {
		return IdentityProviderInvocationResult{}, ErrIdentityProviderUnavailable
	}
	callCtx, cancelCall := context.WithTimeout(
		lease.Context,
		time.Duration(operation.TimeoutMS)*time.Millisecond,
	)
	response, err := invoker.InvokeIdentityProviderInstance(
		callCtx,
		identity,
		extension,
		VersionedIdentityProviderRequest{
			ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
			Kind: provider.Kind, Handler: provider.Handler, Priority: provider.Priority,
			Operation:   operation.Name,
			InputSchema: operation.InputSchema, InputSchemaWireReference: operation.InputSchemaWireReference,
			OutputSchema: operation.OutputSchema, OutputSchemaWireReference: operation.OutputSchemaWireReference,
			Timeout:       time.Duration(operation.TimeoutMS) * time.Millisecond,
			FailurePolicy: operation.FailurePolicy, ActorUserID: input.ActorUserID,
			Input: requestInput,
		},
	)
	callCause := context.Cause(callCtx)
	cancelCall()
	if callCause != nil {
		return IdentityProviderInvocationResult{}, callCause
	}
	if err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	output, err := cloneIdentityProviderDocument(response.Output)
	if err != nil {
		if cause := context.Cause(lease.Context); cause != nil {
			return IdentityProviderInvocationResult{}, cause
		}
		if staleErr := validateIdentityProviderResolution(r.registry, resolution); staleErr != nil {
			return IdentityProviderInvocationResult{}, staleErr
		}
		return IdentityProviderInvocationResult{}, fmt.Errorf("%w: clone output: %v", ErrIdentityProviderInvocationInvalid, err)
	}
	if err := r.registry.ValidateProviderOperationOutput(claim, output); err != nil {
		if cause := context.Cause(lease.Context); cause != nil {
			return IdentityProviderInvocationResult{}, cause
		}
		if staleErr := validateIdentityProviderResolution(r.registry, resolution); staleErr != nil {
			return IdentityProviderInvocationResult{}, staleErr
		}
		return IdentityProviderInvocationResult{}, fmt.Errorf("%w: output Schema: %w", ErrIdentityProviderInvocationInvalid, err)
	}

	// Recheck all mutable Host-owned authority immediately before the effect and
	// audit callback. The lease keeps the exact process alive through Accept.
	if cause := context.Cause(lease.Context); cause != nil {
		return IdentityProviderInvocationResult{}, cause
	}
	if err := validateIdentityProviderResolution(r.registry, resolution); err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	if _, err := r.manager.exactIdentityManagedRuntime(identity, provider.Artifact); err != nil {
		return IdentityProviderInvocationResult{}, err
	}
	result := IdentityProviderInvocationResult{
		RegistryRevision: resolution.Revision,
		RegistryDigest:   resolution.Digest,
		Provider:         provider,
		Operation:        operation,
		Output:           output,
	}
	accepted := cloneIdentityProviderResult(result)
	fence := newIdentityProviderCommitFenceState(func() error {
		if cause := context.Cause(lease.Context); cause != nil {
			return cause
		}
		if err := validateIdentityProviderResolution(r.registry, resolution); err != nil {
			return err
		}
		_, err := r.manager.exactIdentityManagedRuntime(identity, provider.Artifact)
		return err
	})
	acceptErr, acceptPanic := runIdentityProviderAccept(accept, lease.Context, accepted, fence)
	fenceOutcome := fence.Finish()
	if acceptPanic != nil {
		panic(acceptPanic)
	}
	if fenceOutcome.panicValue != nil {
		panic(fenceOutcome.panicValue)
	}
	if combined := errors.Join(acceptErr, fenceOutcome.result, fenceOutcome.violation); combined != nil {
		return IdentityProviderInvocationResult{}, errors.Join(
			ErrIdentityProviderAcceptFailed,
			combined,
		)
	}
	// Accept success is the Host transaction's terminal result. A Registry or
	// admission change after its fence is ordered after this accepted call; do
	// not turn a committed effect into a retryable error with a post-commit read.
	return cloneIdentityProviderResult(result), nil
}

func (m *managerCore) exactIdentityManagedRuntime(
	identity RuntimeInstanceIdentity,
	artifact identityregistry.Artifact,
) (extensions.Extension, error) {
	snapshot, err := m.host.InspectRuntimeInstance(identity)
	if err != nil {
		return extensions.Extension{}, errors.Join(ErrIdentityProviderUnavailable, err)
	}
	extension, available := m.runningExtension(identity.ExtensionID)
	if !available || !snapshot.Active || snapshot.Admission.Draining || snapshot.Admission.Quarantined ||
		snapshot.Admission.Forced || snapshot.Identity.ExtensionID != artifact.ExtensionID ||
		snapshot.Identity.InstanceID != artifact.RuntimeInstanceID ||
		snapshot.ExtensionVersion != artifact.ExtensionVersion ||
		snapshot.ArtifactDigest != artifact.PackageDigest || snapshot.VersionID != artifact.VersionID ||
		extension.ID != artifact.ExtensionID || extension.Version != artifact.ExtensionVersion ||
		extension.PackageDigest != artifact.PackageDigest || extension.ActiveVersionID != artifact.VersionID ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != artifact.ExtensionID ||
		extension.Manifest.Version != artifact.ExtensionVersion || extension.Manifest.Type != extensions.TypePlugin ||
		extension.Manifest.Backend.ProtocolVersion != 2 {
		return extensions.Extension{}, ErrIdentityProviderStale
	}
	return extension, nil
}

func exactIdentityProviderOperation(
	provider identityregistry.ProviderContribution,
	name string,
) (identityregistry.ProviderOperation, bool) {
	for _, operation := range provider.Operations {
		if operation.Name == name {
			return operation, true
		}
	}
	return identityregistry.ProviderOperation{}, false
}

func validateIdentityProviderResolution(
	registry *identityregistry.Registry,
	resolution identityregistry.ProviderResolution,
) error {
	if registry == nil {
		return ErrIdentityProviderInvocationInvalid
	}
	if err := registry.ValidateProviderResolution(resolution); err != nil {
		return errors.Join(ErrIdentityProviderStale, err)
	}
	return nil
}

func mapIdentityProviderResolutionError(err error) error {
	if errors.Is(err, identityregistry.ErrSafeMode) {
		return errors.Join(ErrIdentityProviderUnavailable, identityregistry.ErrSafeMode)
	}
	if errors.Is(err, identityregistry.ErrNotFound) {
		return errors.Join(ErrIdentityProviderUnavailable, err)
	}
	return errors.Join(ErrIdentityProviderStale, err)
}

func sameIdentityProviderContract(
	left identityregistry.ProviderContribution,
	right identityregistry.ProviderContribution,
) bool {
	if left.Artifact != right.Artifact || left.ID != right.ID ||
		left.ContractVersion != right.ContractVersion || left.Kind != right.Kind ||
		left.Handler != right.Handler || left.Priority != right.Priority ||
		len(left.Operations) != len(right.Operations) {
		return false
	}
	for index := range left.Operations {
		leftOperation := left.Operations[index]
		rightOperation := right.Operations[index]
		if leftOperation.Name != rightOperation.Name ||
			leftOperation.InputSchema != rightOperation.InputSchema ||
			leftOperation.InputSchemaWireReference != rightOperation.InputSchemaWireReference ||
			leftOperation.InputSchemaDigest != rightOperation.InputSchemaDigest ||
			leftOperation.OutputSchema != rightOperation.OutputSchema ||
			leftOperation.OutputSchemaWireReference != rightOperation.OutputSchemaWireReference ||
			leftOperation.OutputSchemaDigest != rightOperation.OutputSchemaDigest ||
			leftOperation.TimeoutMS != rightOperation.TimeoutMS ||
			leftOperation.FailurePolicy != rightOperation.FailurePolicy {
			return false
		}
	}
	return true
}

func cloneIdentityProviderDocument(input map[string]any) (map[string]any, error) {
	if input == nil {
		input = map[string]any{}
	}
	if err := preflightComponentDocumentsBounded(maximumIdentityProviderDocumentBytes, input); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if len(raw) > maximumIdentityProviderDocumentBytes {
		return nil, fmt.Errorf("document exceeds %d bytes", maximumIdentityProviderDocumentBytes)
	}
	result := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloneIdentityProviderResult(input IdentityProviderInvocationResult) IdentityProviderInvocationResult {
	result := input
	result.Provider = cloneIdentityProviderContribution(input.Provider)
	result.Output, _ = cloneIdentityProviderDocument(input.Output)
	return result
}

func cloneIdentityProviderContribution(
	input identityregistry.ProviderContribution,
) identityregistry.ProviderContribution {
	result := input
	result.Operations = append([]identityregistry.ProviderOperation(nil), input.Operations...)
	return result
}

func (s *ProtocolStarter) InvokeIdentityProviderInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedIdentityProviderRequest,
) (VersionedIdentityProviderResponse, error) {
	client, err := s.exactIdentityRuntimeClient(ctx, identity, extension)
	if err != nil {
		return VersionedIdentityProviderResponse{}, err
	}
	return client.InvokeIdentityProvider(ctx, request)
}

func (s *ProtocolStarter) exactIdentityRuntimeClient(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
) (*protocolV2Client, error) {
	if s == nil || ctx == nil {
		return nil, ErrIdentityProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return nil, errors.Join(ErrIdentityProviderUnavailable, err)
	}
	var client *protocolV2Client
	if err := func() error {
		unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
		if err != nil {
			return err
		}
		defer unlock()
		instance := s.protocolInstance(identity)
		if instance == nil {
			return errors.Join(ErrIdentityProviderUnavailable, protocolInstanceNotFound(identity))
		}
		if instance.protocolVersion != 2 {
			return ErrIdentityProviderUnavailable
		}
		manifestDigest, digestErr := protocolRuntimeManifestDigest(extension.Manifest)
		if digestErr != nil {
			return errors.Join(ErrIdentityProviderStale, digestErr)
		}
		var ok bool
		client, ok = instance.protocol.(*protocolV2Client)
		if !ok || client.identity == nil ||
			client.identity.GetExtensionId() != identity.ExtensionID ||
			client.identity.GetInstanceId() != identity.InstanceID ||
			client.identity.GetExtensionVersion() != instance.extensionVersion ||
			client.identity.GetArtifactDigest() != instance.artifactDigest ||
			manifestDigest != instance.manifestDigest ||
			extension.ID != identity.ExtensionID || extension.Version != instance.extensionVersion ||
			extension.PackageDigest != instance.artifactDigest {
			return ErrIdentityProviderStale
		}
		s.mu.Lock()
		s.recordProtocolCallLocked(identity.ExtensionID)
		s.mu.Unlock()
		return nil
	}(); err != nil {
		return nil, err
	}
	return client, nil
}

var _ exactIdentityProviderInvoker = (*ProtocolStarter)(nil)
