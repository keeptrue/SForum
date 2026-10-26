package extensionsruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	semver "github.com/Masterminds/semver/v3"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	ErrHookRegistryInvalid          = errors.New("extension hook registry declaration is invalid")
	ErrHookRegistryConflict         = errors.New("extension hook registry contract conflicts with the active snapshot")
	ErrHookRegistryDependency       = errors.New("extension hook registry dependency is not declared")
	ErrHookRegistryContractNotFound = errors.New("extension hook registry contract is not found")
)

type HookArtifact struct {
	ExtensionID       string `json:"extensionId"`
	ExtensionVersion  string `json:"extensionVersion"`
	PackageDigest     string `json:"packageDigest"`
	RuntimeInstanceID string `json:"runtimeInstanceId"`
}

type VersionedHookContract struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	ContractVersion string       `json:"contractVersion"`
	Kind            string       `json:"kind"`
	InputSchema     string       `json:"inputSchema"`
	ResultSchema    string       `json:"resultSchema,omitempty"`
	Execution       string       `json:"execution"`
	FailurePolicy   string       `json:"failurePolicy"`
	TimeoutMS       int          `json:"timeoutMs"`
	MutableFields   []string     `json:"mutableFields,omitempty"`
	Artifact        HookArtifact `json:"artifact"`
}

type VersionedHookListener struct {
	ID       string       `json:"id"`
	TargetID string       `json:"targetId"`
	Handler  string       `json:"handler"`
	Priority int          `json:"priority"`
	Artifact HookArtifact `json:"artifact"`
	manifest extensions.ManifestHook
}

type VersionedHookRegistrySnapshot struct {
	Revision  uint64                  `json:"revision"`
	Contracts []VersionedHookContract `json:"contracts"`
	Listeners []VersionedHookListener `json:"listeners"`
}

type versionedHookRegistryState struct {
	revision       uint64
	extensions     map[string]hookRuntimeRegistration
	contractsByID  map[string]VersionedHookContract
	contractByName map[string]string
	listenersByID  map[string][]VersionedHookListener
}

// VersionedHookRegistry publishes one immutable, exact-runtime hook graph.
// Readers never observe a provider without all of its listeners or vice versa.
type VersionedHookRegistry struct {
	mu                sync.Mutex
	generationBarrier *sync.RWMutex
	state             atomic.Pointer[versionedHookRegistryState]
}

func NewVersionedHookRegistry() *VersionedHookRegistry {
	registry := &VersionedHookRegistry{}
	registry.state.Store(emptyVersionedHookRegistryState())
	return registry
}

func emptyVersionedHookRegistryState() *versionedHookRegistryState {
	return &versionedHookRegistryState{
		extensions: map[string]hookRuntimeRegistration{}, contractsByID: map[string]VersionedHookContract{},
		contractByName: map[string]string{}, listenersByID: map[string][]VersionedHookListener{},
	}
}

func (r *VersionedHookRegistry) ReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if err := validateVersionedHookRuntime(extension, instanceID); err != nil {
		return ErrHookRegistryInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registrations := make(map[string]hookRuntimeRegistration, len(current.extensions)+1)
	for id, registration := range current.extensions {
		registrations[id] = registration
	}
	registrations[extension.ID] = hookRuntimeRegistration{extension: cloneHookExtension(extension), instanceID: instanceID}
	next, err := buildVersionedHookRegistryState(current.revision+1, registrations)
	if err != nil {
		return err
	}
	r.state.Store(next)
	return nil
}

func (r *VersionedHookRegistry) ValidateReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if err := validateVersionedHookRuntime(extension, instanceID); err != nil {
		return err
	}
	current := r.load()
	registrations := make(map[string]hookRuntimeRegistration, len(current.extensions)+1)
	for id, registration := range current.extensions {
		registrations[id] = registration
	}
	registrations[extension.ID] = hookRuntimeRegistration{extension: cloneHookExtension(extension), instanceID: instanceID}
	_, err := buildVersionedHookRegistryState(current.revision+1, registrations)
	return err
}

func (r *VersionedHookRegistry) ValidateRemoveRuntime(extensionID, instanceID string) error {
	current := r.load()
	registration, ok := current.extensions[extensionID]
	if !ok || registration.instanceID != instanceID {
		return ErrHookRegistryConflict
	}
	registrations := make(map[string]hookRuntimeRegistration, len(current.extensions)-1)
	for id, item := range current.extensions {
		if id != extensionID {
			registrations[id] = item
		}
	}
	_, err := buildVersionedHookRegistryState(current.revision+1, registrations)
	return err
}

func (r *VersionedHookRegistry) RemoveRuntime(extensionID, instanceID string) (bool, error) {
	if r == nil || strings.TrimSpace(extensionID) == "" || strings.TrimSpace(instanceID) == "" {
		return false, ErrHookRegistryInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registration, ok := current.extensions[extensionID]
	if !ok || registration.instanceID != instanceID {
		return false, nil
	}
	registrations := make(map[string]hookRuntimeRegistration, len(current.extensions)-1)
	for id, item := range current.extensions {
		if id != extensionID {
			registrations[id] = item
		}
	}
	next, err := buildVersionedHookRegistryState(current.revision+1, registrations)
	if err != nil {
		return false, err
	}
	r.state.Store(next)
	return true, nil
}

func (r *VersionedHookRegistry) Resolve(id, contractVersion string) (VersionedHookContract, []VersionedHookListener, error) {
	unlock := r.lockGenerationRead()
	defer unlock()
	state := r.load()
	contract, ok := state.contractsByID[strings.TrimSpace(id)]
	if !ok {
		if target := state.contractByName[strings.TrimSpace(id)]; target != "" {
			contract, ok = state.contractsByID[target]
		}
	}
	if !ok || (contractVersion != "" && contract.ContractVersion != strings.TrimSpace(contractVersion)) {
		return VersionedHookContract{}, nil, ErrHookRegistryContractNotFound
	}
	return cloneVersionedHookContract(contract), cloneVersionedHookListeners(state.listenersByID[contract.ID]), nil
}

func (r *VersionedHookRegistry) Snapshot() VersionedHookRegistrySnapshot {
	unlock := r.lockGenerationRead()
	defer unlock()
	state := r.load()
	snapshot := VersionedHookRegistrySnapshot{Revision: state.revision}
	for _, contract := range state.contractsByID {
		snapshot.Contracts = append(snapshot.Contracts, cloneVersionedHookContract(contract))
		snapshot.Listeners = append(snapshot.Listeners, cloneVersionedHookListeners(state.listenersByID[contract.ID])...)
	}
	sort.Slice(snapshot.Contracts, func(i, j int) bool { return snapshot.Contracts[i].ID < snapshot.Contracts[j].ID })
	sort.Slice(snapshot.Listeners, func(i, j int) bool {
		if snapshot.Listeners[i].TargetID != snapshot.Listeners[j].TargetID {
			return snapshot.Listeners[i].TargetID < snapshot.Listeners[j].TargetID
		}
		return hookListenerBefore(snapshot.Listeners[i], snapshot.Listeners[j])
	})
	return snapshot
}

func (r *VersionedHookRegistry) lockGenerationRead() func() {
	if r == nil || r.generationBarrier == nil {
		return func() {}
	}
	r.generationBarrier.RLock()
	return r.generationBarrier.RUnlock
}

func (r *VersionedHookRegistry) load() *versionedHookRegistryState {
	if r == nil {
		return emptyVersionedHookRegistryState()
	}
	if state := r.state.Load(); state != nil {
		return state
	}
	return emptyVersionedHookRegistryState()
}

func buildVersionedHookRegistryState(revision uint64, registrations map[string]hookRuntimeRegistration) (*versionedHookRegistryState, error) {
	state := emptyVersionedHookRegistryState()
	state.revision = revision
	for id, registration := range registrations {
		state.extensions[id] = hookRuntimeRegistration{extension: cloneHookExtension(registration.extension), instanceID: registration.instanceID}
		artifact := hookArtifact(registration)
		for _, declaration := range registration.extension.Manifest.Hooks {
			if !isVersionedPluginHook(declaration) || declaration.TargetID != "" {
				continue
			}
			if declaration.Execution == "async" && declaration.FailurePolicy != appevents.FailurePolicyFailOpen {
				return nil, fmt.Errorf("%w: async hook %s must fail open", ErrHookRegistryInvalid, declaration.ID)
			}
			if declaration.TimeoutMS <= 0 || declaration.TimeoutMS > extensionmanifest.HookMaximumTimeoutMS {
				return nil, fmt.Errorf("%w: hook %s timeout exceeds Host policy", ErrHookRegistryInvalid, declaration.ID)
			}
			if _, duplicate := state.contractsByID[declaration.ID]; duplicate || state.contractByName[declaration.Name] != "" {
				return nil, fmt.Errorf("%w: duplicate hook %s", ErrHookRegistryConflict, declaration.ID)
			}
			contract := hookContract(declaration, artifact)
			state.contractsByID[contract.ID] = contract
			state.contractByName[contract.Name] = contract.ID
		}
	}
	for _, registration := range state.extensions {
		for _, declaration := range registration.extension.Manifest.Hooks {
			if !isVersionedPluginHook(declaration) || declaration.Handler == "" {
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
				return nil, fmt.Errorf("%w: listener %s target %s", ErrHookRegistryConflict, declaration.ID, targetID)
			}
			if contract.Artifact.ExtensionID != registration.extension.ID {
				dependency, declared, compatible := hookDependency(
					registration.extension, contract.Artifact.ExtensionID, contract.Artifact.ExtensionVersion,
				)
				if !declared || !compatible {
					if declared && dependency.Kind == "optional" {
						continue
					}
					return nil, fmt.Errorf("%w: %s requires %s@%s", ErrHookRegistryDependency,
						registration.extension.ID, contract.Artifact.ExtensionID, contract.Artifact.ExtensionVersion)
				}
				if !hookDeclarationMatchesContract(declaration, contract) {
					if dependency.Kind == "optional" {
						continue
					}
					return nil, fmt.Errorf("%w: listener %s target %s", ErrHookRegistryConflict, declaration.ID, targetID)
				}
			} else if !hookDeclarationMatchesContract(declaration, contract) {
				return nil, fmt.Errorf("%w: listener %s target %s", ErrHookRegistryConflict, declaration.ID, targetID)
			}
			listener := VersionedHookListener{
				ID: declaration.ID, TargetID: targetID, Handler: declaration.Handler, Priority: declaration.Priority,
				Artifact: hookArtifact(registration), manifest: cloneManifestHook(declaration),
			}
			state.listenersByID[targetID] = append(state.listenersByID[targetID], listener)
		}
	}
	for id := range state.listenersByID {
		sort.Slice(state.listenersByID[id], func(i, j int) bool {
			return hookListenerBefore(state.listenersByID[id][i], state.listenersByID[id][j])
		})
	}
	return state, nil
}

func isVersionedPluginHook(hook extensions.ManifestHook) bool {
	return hook.ID != "" && hook.ContractVersion != "" && hook.InputSchema != "" && !appevents.Known(hook.Name)
}

func hookArtifact(registration hookRuntimeRegistration) HookArtifact {
	return HookArtifact{
		ExtensionID: registration.extension.ID, ExtensionVersion: registration.extension.Version,
		PackageDigest: registration.extension.PackageDigest, RuntimeInstanceID: registration.instanceID,
	}
}

func hookContract(hook extensions.ManifestHook, artifact HookArtifact) VersionedHookContract {
	return VersionedHookContract{
		ID: hook.ID, Name: hook.Name, ContractVersion: hook.ContractVersion, Kind: hook.Kind,
		InputSchema: hook.InputSchema, ResultSchema: hook.ResultSchema, Execution: hook.Execution,
		FailurePolicy: hook.FailurePolicy, TimeoutMS: hook.TimeoutMS,
		MutableFields: append([]string(nil), hook.MutableFields...), Artifact: artifact,
	}
}

func hookDeclarationMatchesContract(hook extensions.ManifestHook, contract VersionedHookContract) bool {
	return hook.Name == contract.Name && hook.ContractVersion == contract.ContractVersion && hook.Kind == contract.Kind &&
		hook.InputSchema == contract.InputSchema && hook.ResultSchema == contract.ResultSchema &&
		hook.Execution == contract.Execution && hook.FailurePolicy == contract.FailurePolicy &&
		hook.TimeoutMS == contract.TimeoutMS && stringSlicesEqual(hook.MutableFields, contract.MutableFields)
}

func hookDependency(
	extension extensions.Extension,
	providerID, providerVersion string,
) (extensions.ManifestDependency, bool, bool) {
	for _, dependency := range extension.Manifest.Dependencies {
		if dependency.ID == providerID && (dependency.Kind == "required" || dependency.Kind == "optional") {
			constraint, constraintErr := semver.NewConstraint(dependency.Version)
			version, versionErr := semver.StrictNewVersion(providerVersion)
			return dependency, true, constraintErr == nil && versionErr == nil && constraint.Check(version)
		}
	}
	return extensions.ManifestDependency{}, false, false
}

func hookDependencyForTarget(extension extensions.Extension, targetID string) (extensions.ManifestDependency, bool) {
	var selected extensions.ManifestDependency
	for _, dependency := range extension.Manifest.Dependencies {
		if dependency.ID == "" || (dependency.Kind != "required" && dependency.Kind != "optional") ||
			!strings.HasPrefix(targetID, dependency.ID+".") {
			continue
		}
		if len(dependency.ID) > len(selected.ID) {
			selected = dependency
		}
	}
	return selected, selected.ID != ""
}

func hookListenerBefore(left, right VersionedHookListener) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Artifact.ExtensionID != right.Artifact.ExtensionID {
		return left.Artifact.ExtensionID < right.Artifact.ExtensionID
	}
	return left.ID < right.ID
}

func cloneHookExtension(extension extensions.Extension) extensions.Extension {
	hooks := extension.Manifest.Hooks
	extension.Manifest.Hooks = make([]extensions.ManifestHook, len(hooks))
	for index, hook := range hooks {
		extension.Manifest.Hooks[index] = cloneManifestHook(hook)
	}
	extension.Manifest.Dependencies = append([]extensions.ManifestDependency(nil), extension.Manifest.Dependencies...)
	extension.Manifest.Commands = append([]extensions.ManifestCommand(nil), extension.Manifest.Commands...)
	extension.Manifest.AdminSurfaces = append([]extensions.ManifestAdminSurface(nil), extension.Manifest.AdminSurfaces...)
	extension.Manifest.PackageFiles = append([]extensions.ManifestPackageFile(nil), extension.Manifest.PackageFiles...)
	return extension
}

func cloneManifestHook(hook extensions.ManifestHook) extensions.ManifestHook {
	hook.MutableFields = append([]string(nil), hook.MutableFields...)
	return hook
}

func cloneVersionedHookContract(contract VersionedHookContract) VersionedHookContract {
	contract.MutableFields = append([]string(nil), contract.MutableFields...)
	return contract
}

func cloneVersionedHookListeners(items []VersionedHookListener) []VersionedHookListener {
	result := make([]VersionedHookListener, len(items))
	for index, item := range items {
		item.manifest = cloneManifestHook(item.manifest)
		result[index] = item
	}
	return result
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateVersionedHookRuntime(extension extensions.Extension, instanceID string) error {
	if extension.Type != extensions.TypePlugin ||
		strings.TrimSpace(extension.ID) == "" || strings.TrimSpace(extension.Version) == "" ||
		strings.TrimSpace(extension.PackageDigest) == "" || strings.TrimSpace(instanceID) == "" ||
		extension.Manifest.Backend.ProtocolVersion != 2 {
		return ErrHookRegistryInvalid
	}
	return nil
}
