package extensionsruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrProviderSlotInvalid       = errors.New("extension provider slot declaration is invalid")
	ErrProviderSlotConflict      = errors.New("extension provider slot contract conflicts with the active snapshot")
	ErrProviderSlotNotFound      = errors.New("extension provider slot is not found")
	ErrProviderSlotDenied        = errors.New("extension provider slot caller is denied")
	ErrProviderSlotNoProvider    = errors.New("extension provider slot has no available provider")
	ErrProviderSlotInputInvalid  = errors.New("extension provider slot input is invalid")
	ErrProviderSlotOutputInvalid = errors.New("extension provider slot output is invalid")
)

type ProviderSlotContract struct {
	ID                   string       `json:"id"`
	Slot                 string       `json:"slot"`
	ContractVersion      string       `json:"contractVersion"`
	RequestSchema        string       `json:"requestSchema"`
	ResponseSchema       string       `json:"responseSchema"`
	RequestSchemaDigest  string       `json:"requestSchemaDigest,omitempty"`
	ResponseSchemaDigest string       `json:"responseSchemaDigest,omitempty"`
	Fallback             string       `json:"fallback"`
	TimeoutMS            int          `json:"timeoutMs"`
	Artifact             HookArtifact `json:"artifact"`
	requestValidator     providerDocumentValidator
	responseValidator    providerDocumentValidator
}

type ProviderSlotCandidate struct {
	ID       string       `json:"id"`
	TargetID string       `json:"targetId"`
	Label    string       `json:"label"`
	Handler  string       `json:"handler"`
	Priority int          `json:"priority"`
	Artifact HookArtifact `json:"artifact"`
	manifest extensions.ManifestProvider
}

type ProviderSlotCaller struct {
	ExtensionID       string
	ExtensionVersion  string
	ArtifactDigest    string
	RuntimeInstanceID string
	Attested          bool
}

type ProviderSlotResolution struct {
	Revision   uint64
	Contract   ProviderSlotContract
	Candidates []ProviderSlotCandidate
}

type ProviderSlotRegistrySnapshot struct {
	Revision   uint64                  `json:"revision"`
	Contracts  []ProviderSlotContract  `json:"contracts"`
	Candidates []ProviderSlotCandidate `json:"candidates"`
}

type providerSlotRegistryState struct {
	revision       uint64
	extensions     map[string]hookRuntimeRegistration
	contractsByID  map[string]ProviderSlotContract
	contractBySlot map[string]string
	candidatesByID map[string][]ProviderSlotCandidate
}

type VersionedProviderSlotRegistry struct {
	mu                sync.Mutex
	generationBarrier *sync.RWMutex
	state             atomic.Pointer[providerSlotRegistryState]
}

func NewVersionedProviderSlotRegistry() *VersionedProviderSlotRegistry {
	r := &VersionedProviderSlotRegistry{}
	r.state.Store(emptyProviderSlotRegistryState())
	return r
}

func emptyProviderSlotRegistryState() *providerSlotRegistryState {
	return &providerSlotRegistryState{
		extensions: map[string]hookRuntimeRegistration{}, contractsByID: map[string]ProviderSlotContract{},
		contractBySlot: map[string]string{}, candidatesByID: map[string][]ProviderSlotCandidate{},
	}
}

func (r *VersionedProviderSlotRegistry) ReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if err := validateVersionedHookRuntime(extension, instanceID); err != nil {
		return ErrProviderSlotInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registrations := cloneProviderSlotRegistrations(current.extensions)
	registrations[extension.ID] = hookRuntimeRegistration{extension: cloneProviderSlotExtension(extension), instanceID: instanceID}
	next, err := buildProviderSlotRegistryState(current.revision+1, registrations)
	if err != nil {
		return err
	}
	if err := validateProviderSlotSchemaContinuity(current, next, extension.ID); err != nil {
		return err
	}
	r.state.Store(next)
	return nil
}

func (r *VersionedProviderSlotRegistry) ValidateReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if err := validateVersionedHookRuntime(extension, instanceID); err != nil {
		return ErrProviderSlotInvalid
	}
	current := r.load()
	registrations := cloneProviderSlotRegistrations(current.extensions)
	registrations[extension.ID] = hookRuntimeRegistration{extension: cloneProviderSlotExtension(extension), instanceID: instanceID}
	next, err := buildProviderSlotRegistryState(current.revision+1, registrations)
	if err != nil {
		return err
	}
	return validateProviderSlotSchemaContinuity(current, next, extension.ID)
}

func validateProviderSlotSchemaContinuity(current, next *providerSlotRegistryState, extensionID string) error {
	for id, oldContract := range current.contractsByID {
		if oldContract.Artifact.ExtensionID != extensionID {
			continue
		}
		newContract, ok := next.contractsByID[id]
		if !ok || oldContract.ContractVersion != newContract.ContractVersion {
			continue
		}
		if oldContract.RequestSchemaDigest != "" && newContract.RequestSchemaDigest != "" &&
			(oldContract.RequestSchemaDigest != newContract.RequestSchemaDigest ||
				oldContract.ResponseSchemaDigest != newContract.ResponseSchemaDigest) {
			return fmt.Errorf("%w: provider slot %s changed schema bytes without a contract version", ErrProviderSlotConflict, id)
		}
	}
	return nil
}

func (r *VersionedProviderSlotRegistry) RemoveRuntime(extensionID, instanceID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registration, ok := current.extensions[extensionID]
	if !ok || registration.instanceID != instanceID {
		return false, nil
	}
	registrations := cloneProviderSlotRegistrations(current.extensions)
	delete(registrations, extensionID)
	next, err := buildProviderSlotRegistryState(current.revision+1, registrations)
	if err != nil {
		return false, err
	}
	r.state.Store(next)
	return true, nil
}

func (r *VersionedProviderSlotRegistry) ValidateRemoveRuntime(extensionID, instanceID string) error {
	current := r.load()
	registration, ok := current.extensions[extensionID]
	if !ok || registration.instanceID != instanceID {
		return ErrProviderSlotConflict
	}
	registrations := cloneProviderSlotRegistrations(current.extensions)
	delete(registrations, extensionID)
	_, err := buildProviderSlotRegistryState(current.revision+1, registrations)
	return err
}

func (r *VersionedProviderSlotRegistry) Discover(
	caller ProviderSlotCaller,
	id, contractVersion string,
) (ProviderSlotResolution, error) {
	unlock := r.lockGenerationRead()
	defer unlock()
	state := r.load()
	contract, ok := state.contractsByID[strings.TrimSpace(id)]
	if !ok {
		if target := state.contractBySlot[strings.TrimSpace(id)]; target != "" {
			contract, ok = state.contractsByID[target]
		}
	}
	if !ok || (contractVersion != "" && contract.ContractVersion != strings.TrimSpace(contractVersion)) {
		return ProviderSlotResolution{}, ErrProviderSlotNotFound
	}
	if err := authorizeProviderSlotCaller(state, caller, contract); err != nil {
		return ProviderSlotResolution{}, err
	}
	candidates := cloneProviderSlotCandidates(state.candidatesByID[contract.ID])
	if len(candidates) == 0 {
		return ProviderSlotResolution{}, ErrProviderSlotNoProvider
	}
	return ProviderSlotResolution{
		Revision: state.revision, Contract: cloneProviderSlotContract(contract), Candidates: candidates,
	}, nil
}

func (r *VersionedProviderSlotRegistry) Snapshot() ProviderSlotRegistrySnapshot {
	unlock := r.lockGenerationRead()
	defer unlock()
	state := r.load()
	result := ProviderSlotRegistrySnapshot{Revision: state.revision}
	for _, contract := range state.contractsByID {
		result.Contracts = append(result.Contracts, cloneProviderSlotContract(contract))
		result.Candidates = append(result.Candidates, cloneProviderSlotCandidates(state.candidatesByID[contract.ID])...)
	}
	sort.Slice(result.Contracts, func(i, j int) bool { return result.Contracts[i].ID < result.Contracts[j].ID })
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].TargetID != result.Candidates[j].TargetID {
			return result.Candidates[i].TargetID < result.Candidates[j].TargetID
		}
		return providerCandidateBefore(result.Candidates[i], result.Candidates[j])
	})
	return result
}

func (r *VersionedProviderSlotRegistry) lockGenerationRead() func() {
	if r == nil || r.generationBarrier == nil {
		return func() {}
	}
	r.generationBarrier.RLock()
	return r.generationBarrier.RUnlock
}

func (r *VersionedProviderSlotRegistry) load() *providerSlotRegistryState {
	if r == nil {
		return emptyProviderSlotRegistryState()
	}
	if state := r.state.Load(); state != nil {
		return state
	}
	return emptyProviderSlotRegistryState()
}

func buildProviderSlotRegistryState(
	revision uint64,
	registrations map[string]hookRuntimeRegistration,
) (*providerSlotRegistryState, error) {
	state := emptyProviderSlotRegistryState()
	state.revision = revision
	for id, registration := range registrations {
		state.extensions[id] = hookRuntimeRegistration{extension: cloneProviderSlotExtension(registration.extension), instanceID: registration.instanceID}
		artifact := hookArtifact(registration)
		for _, declaration := range registration.extension.Manifest.Providers {
			if !isVersionedProviderSlot(declaration) || declaration.TargetID != "" {
				continue
			}
			if declaration.TimeoutMS <= 0 || declaration.TimeoutMS > 5000 ||
				(declaration.Fallback != "next" && declaration.Fallback != "closed") {
				return nil, fmt.Errorf("%w: provider slot %s policy", ErrProviderSlotInvalid, declaration.ID)
			}
			if _, duplicate := state.contractsByID[declaration.ID]; duplicate || state.contractBySlot[declaration.Slot] != "" {
				return nil, fmt.Errorf("%w: duplicate provider slot %s", ErrProviderSlotConflict, declaration.ID)
			}
			contract := providerSlotContract(declaration, artifact)
			requestValidator, responseValidator, requestDigest, responseDigest, schemaErr := compileProviderSlotSchemas(registration.extension, declaration)
			if schemaErr != nil {
				return nil, fmt.Errorf("%w: provider slot %s schemas: %v", ErrProviderSlotInvalid, declaration.ID, schemaErr)
			}
			contract.requestValidator = requestValidator
			contract.responseValidator = responseValidator
			contract.RequestSchemaDigest = requestDigest
			contract.ResponseSchemaDigest = responseDigest
			state.contractsByID[contract.ID] = contract
			state.contractBySlot[contract.Slot] = contract.ID
		}
	}
	for _, registration := range state.extensions {
		for _, declaration := range registration.extension.Manifest.Providers {
			if !isVersionedProviderSlot(declaration) || declaration.Handler == "" {
				continue
			}
			targetID := declaration.TargetID
			if targetID == "" {
				targetID = declaration.ID
			}
			contract, ok := state.contractsByID[targetID]
			if !ok {
				dependency, declared := hookDependencyForTarget(registration.extension, targetID)
				if declared && dependency.Kind == "optional" {
					continue
				}
				return nil, fmt.Errorf("%w: provider %s target %s", ErrProviderSlotConflict, declaration.ID, targetID)
			}
			if contract.Artifact.ExtensionID != registration.extension.ID {
				dependency, declared, compatible := hookDependency(
					registration.extension, contract.Artifact.ExtensionID, contract.Artifact.ExtensionVersion,
				)
				if !declared || !compatible {
					if declared && dependency.Kind == "optional" {
						continue
					}
					return nil, fmt.Errorf("%w: provider %s dependency", ErrProviderSlotConflict, declaration.ID)
				}
				if !providerDeclarationMatchesContract(registration.extension, declaration, contract) {
					if dependency.Kind == "optional" {
						continue
					}
					return nil, fmt.Errorf("%w: provider %s contract", ErrProviderSlotConflict, declaration.ID)
				}
			} else if !providerDeclarationMatchesContract(registration.extension, declaration, contract) {
				return nil, fmt.Errorf("%w: provider %s contract", ErrProviderSlotConflict, declaration.ID)
			}
			candidate := ProviderSlotCandidate{
				ID: declaration.ID, TargetID: targetID, Label: declaration.Label, Handler: declaration.Handler,
				Priority: declaration.Priority, Artifact: hookArtifact(registration), manifest: cloneManifestProvider(declaration),
			}
			state.candidatesByID[targetID] = append(state.candidatesByID[targetID], candidate)
		}
	}
	for id := range state.candidatesByID {
		sort.Slice(state.candidatesByID[id], func(i, j int) bool {
			return providerCandidateBefore(state.candidatesByID[id][i], state.candidatesByID[id][j])
		})
	}
	return state, nil
}

func authorizeProviderSlotCaller(
	state *providerSlotRegistryState,
	caller ProviderSlotCaller,
	contract ProviderSlotContract,
) error {
	if caller.ExtensionID == "" && caller.RuntimeInstanceID == "" {
		return nil
	}
	registration, ok := state.extensions[caller.ExtensionID]
	if !caller.Attested || !ok || registration.instanceID != caller.RuntimeInstanceID ||
		registration.extension.Version != caller.ExtensionVersion ||
		registration.extension.PackageDigest != caller.ArtifactDigest {
		return ErrProviderSlotDenied
	}
	if caller.ExtensionID == contract.Artifact.ExtensionID {
		return nil
	}
	_, declared, compatible := hookDependency(
		registration.extension, contract.Artifact.ExtensionID, contract.Artifact.ExtensionVersion,
	)
	if !declared || !compatible {
		return ErrProviderSlotDenied
	}
	return nil
}

func isVersionedProviderSlot(value extensions.ManifestProvider) bool {
	return value.ID != "" && value.ContractVersion != "" && value.RequestSchema != "" && value.ResponseSchema != ""
}

func providerSlotContract(value extensions.ManifestProvider, artifact HookArtifact) ProviderSlotContract {
	return ProviderSlotContract{
		ID: value.ID, Slot: value.Slot, ContractVersion: value.ContractVersion,
		RequestSchema: value.RequestSchema, ResponseSchema: value.ResponseSchema,
		Fallback: value.Fallback, TimeoutMS: value.TimeoutMS, Artifact: artifact,
	}
}

func providerDeclarationMatchesContract(extension extensions.Extension, value extensions.ManifestProvider, contract ProviderSlotContract) bool {
	digestsMatch := true
	if extension.ID == contract.Artifact.ExtensionID {
		requestDigest, requestOK := providerSchemaDigest(extension, value.RequestSchema)
		responseDigest, responseOK := providerSchemaDigest(extension, value.ResponseSchema)
		digestsMatch = contract.RequestSchemaDigest == "" && contract.ResponseSchemaDigest == "" ||
			requestOK && responseOK && requestDigest == contract.RequestSchemaDigest && responseDigest == contract.ResponseSchemaDigest
	} else {
		requestDigest, requestOK := providerSchemaDigest(extension, value.RequestSchema)
		responseDigest, responseOK := providerSchemaDigest(extension, value.ResponseSchema)
		digestsMatch = contract.RequestSchemaDigest == "" && contract.ResponseSchemaDigest == "" ||
			requestOK && responseOK && requestDigest == contract.RequestSchemaDigest && responseDigest == contract.ResponseSchemaDigest
	}
	return digestsMatch && value.Slot == contract.Slot && value.ContractVersion == contract.ContractVersion &&
		value.RequestSchema == contract.RequestSchema && value.ResponseSchema == contract.ResponseSchema &&
		value.Fallback == contract.Fallback && value.TimeoutMS == contract.TimeoutMS
}

func providerCandidateBefore(left, right ProviderSlotCandidate) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Artifact.ExtensionID != right.Artifact.ExtensionID {
		return left.Artifact.ExtensionID < right.Artifact.ExtensionID
	}
	return left.ID < right.ID
}

func cloneProviderSlotRegistrations(source map[string]hookRuntimeRegistration) map[string]hookRuntimeRegistration {
	result := make(map[string]hookRuntimeRegistration, len(source))
	for id, registration := range source {
		result[id] = registration
	}
	return result
}

func cloneProviderSlotExtension(extension extensions.Extension) extensions.Extension {
	providers := extension.Manifest.Providers
	extension.Manifest.Providers = make([]extensions.ManifestProvider, len(providers))
	copy(extension.Manifest.Providers, providers)
	extension.Manifest.Dependencies = append([]extensions.ManifestDependency(nil), extension.Manifest.Dependencies...)
	return extension
}

func cloneManifestProvider(value extensions.ManifestProvider) extensions.ManifestProvider {
	return value
}

func cloneProviderSlotContract(value ProviderSlotContract) ProviderSlotContract { return value }

func cloneProviderSlotCandidates(values []ProviderSlotCandidate) []ProviderSlotCandidate {
	result := make([]ProviderSlotCandidate, len(values))
	copy(result, values)
	return result
}
