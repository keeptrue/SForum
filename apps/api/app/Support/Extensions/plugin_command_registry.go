package extensionsruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrPluginCommandRegistryInvalid  = errors.New("plugin command registry declaration is invalid")
	ErrPluginCommandRegistryConflict = errors.New("plugin command registry contract conflicts with the active snapshot")
	ErrPluginCommandNotFound         = errors.New("plugin command is not found")
	ErrPluginCommandSafeMode         = errors.New("plugin command is unavailable in safe mode")
)

type PluginCommandContract struct {
	ID                 string
	ContractVersion    string
	ExtensionID        string
	ExtensionVersion   string
	ArtifactDigest     string
	InstanceID         string
	Handler            string
	Permission         string
	InputSchema        string
	ResultSchema       string
	InputSchemaDigest  string
	ResultSchemaDigest string
	Description        string
	RecoverySafe       bool
	Timeout            time.Duration

	inputValidator  providerDocumentValidator
	resultValidator providerDocumentValidator
}

type PluginCommandRegistrySnapshot struct {
	Revision uint64
	Commands []PluginCommandContract
}

type pluginCommandRuntimeRegistration struct {
	extension  extensions.Extension
	instanceID string
}

type pluginCommandRegistryState struct {
	revision      uint64
	registrations map[string]pluginCommandRuntimeRegistration
	commands      map[string]PluginCommandContract
}

// PluginCommandRegistry publishes one immutable command namespace across all
// exact protocol-v2 runtime instances.
type PluginCommandRegistry struct {
	mu                sync.Mutex
	generationBarrier *sync.RWMutex
	state             atomic.Pointer[pluginCommandRegistryState]
}

func NewPluginCommandRegistry() *PluginCommandRegistry {
	r := &PluginCommandRegistry{}
	r.state.Store(emptyPluginCommandRegistryState())
	return r
}

func emptyPluginCommandRegistryState() *pluginCommandRegistryState {
	return &pluginCommandRegistryState{
		registrations: make(map[string]pluginCommandRuntimeRegistration),
		commands:      make(map[string]PluginCommandContract),
	}
}

func (r *PluginCommandRegistry) ReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if r == nil {
		return ErrPluginCommandRegistryInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registrations := clonePluginCommandRegistrations(current.registrations)
	if previous, ok := registrations[extension.ID]; ok {
		if err := validatePluginCommandUpgrade(previous.extension, extension); err != nil {
			return err
		}
	}
	registrations[extension.ID] = pluginCommandRuntimeRegistration{extension: cloneHookExtension(extension), instanceID: instanceID}
	next, err := buildPluginCommandRegistryState(current.revision+1, registrations)
	if err != nil {
		return err
	}
	r.state.Store(next)
	return nil
}

func (r *PluginCommandRegistry) ValidateReplaceRuntime(extension extensions.Extension, instanceID string) error {
	if r == nil {
		return ErrPluginCommandRegistryInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registrations := clonePluginCommandRegistrations(current.registrations)
	if previous, ok := registrations[extension.ID]; ok {
		if err := validatePluginCommandUpgrade(previous.extension, extension); err != nil {
			return err
		}
	}
	registrations[extension.ID] = pluginCommandRuntimeRegistration{extension: cloneHookExtension(extension), instanceID: instanceID}
	_, err := buildPluginCommandRegistryState(current.revision+1, registrations)
	return err
}

func (r *PluginCommandRegistry) ValidateRemoveRuntime(extensionID, instanceID string) error {
	current := r.load()
	registration, ok := current.registrations[strings.TrimSpace(extensionID)]
	if !ok {
		return nil
	}
	if registration.instanceID != strings.TrimSpace(instanceID) {
		return ErrPluginCommandRegistryConflict
	}
	registrations := clonePluginCommandRegistrations(current.registrations)
	delete(registrations, extensionID)
	_, err := buildPluginCommandRegistryState(current.revision+1, registrations)
	return err
}

func (r *PluginCommandRegistry) RemoveRuntime(extensionID, instanceID string) (bool, error) {
	if r == nil {
		return false, ErrPluginCommandRegistryInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	registration, ok := current.registrations[strings.TrimSpace(extensionID)]
	if !ok {
		return false, nil
	}
	if registration.instanceID != strings.TrimSpace(instanceID) {
		return false, ErrPluginCommandRegistryConflict
	}
	registrations := clonePluginCommandRegistrations(current.registrations)
	delete(registrations, extensionID)
	next, err := buildPluginCommandRegistryState(current.revision+1, registrations)
	if err != nil {
		return false, err
	}
	r.state.Store(next)
	return true, nil
}

func (r *PluginCommandRegistry) Resolve(id string, safeMode bool) (PluginCommandContract, error) {
	unlock := r.lockGenerationRead()
	defer unlock()
	contract, ok := r.load().commands[strings.TrimSpace(id)]
	if !ok {
		return PluginCommandContract{}, ErrPluginCommandNotFound
	}
	if safeMode && !contract.RecoverySafe {
		return PluginCommandContract{}, ErrPluginCommandSafeMode
	}
	return contract, nil
}

func (r *PluginCommandRegistry) ValidateInput(contract PluginCommandContract, document map[string]any) error {
	return validatePluginCommandDocument(contract.inputValidator, document)
}

func (r *PluginCommandRegistry) ValidateResult(contract PluginCommandContract, document map[string]any) error {
	return validatePluginCommandDocument(contract.resultValidator, document)
}

func validatePluginCommandDocument(validator providerDocumentValidator, document map[string]any) error {
	if validator == nil {
		return nil
	}
	if err := validator.Validate(document); err != nil {
		return fmt.Errorf("%w: %v", ErrPluginCommandRegistryInvalid, err)
	}
	return nil
}

func (r *PluginCommandRegistry) Snapshot() PluginCommandRegistrySnapshot {
	unlock := r.lockGenerationRead()
	defer unlock()
	state := r.load()
	result := PluginCommandRegistrySnapshot{Revision: state.revision, Commands: make([]PluginCommandContract, 0, len(state.commands))}
	for _, command := range state.commands {
		command.inputValidator = nil
		command.resultValidator = nil
		result.Commands = append(result.Commands, command)
	}
	sort.Slice(result.Commands, func(i, j int) bool { return result.Commands[i].ID < result.Commands[j].ID })
	return result
}

func (r *PluginCommandRegistry) lockGenerationRead() func() {
	if r == nil || r.generationBarrier == nil {
		return func() {}
	}
	r.generationBarrier.RLock()
	return r.generationBarrier.RUnlock
}

func (r *PluginCommandRegistry) load() *pluginCommandRegistryState {
	if r == nil {
		return emptyPluginCommandRegistryState()
	}
	if state := r.state.Load(); state != nil {
		return state
	}
	return emptyPluginCommandRegistryState()
}

func buildPluginCommandRegistryState(revision uint64, registrations map[string]pluginCommandRuntimeRegistration) (*pluginCommandRegistryState, error) {
	state := emptyPluginCommandRegistryState()
	state.revision = revision
	state.registrations = clonePluginCommandRegistrations(registrations)
	for extensionID, registration := range registrations {
		extension := registration.extension
		if extension.ID != extensionID || extension.Type != extensions.TypePlugin ||
			strings.TrimSpace(registration.instanceID) == "" || extension.Manifest.Backend.ProtocolVersion != 2 ||
			strings.TrimSpace(extension.PackageDigest) == "" {
			return nil, ErrPluginCommandRegistryInvalid
		}
		for _, declaration := range extension.Manifest.Commands {
			if !strings.HasPrefix(declaration.ID, extension.ID+".") || declaration.ContractVersion == "" || declaration.Handler == "" ||
				declaration.InputSchema == "" || declaration.ResultSchema == "" || declaration.TimeoutMS <= 0 ||
				declaration.TimeoutMS > 5000 {
				return nil, fmt.Errorf("%w: command %s", ErrPluginCommandRegistryInvalid, declaration.ID)
			}
			if _, duplicate := state.commands[declaration.ID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate command %s", ErrPluginCommandRegistryConflict, declaration.ID)
			}
			inputValidator, inputDigest, err := compilePluginCommandSchema(extension, declaration.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("%w: command %s input schema: %v", ErrPluginCommandRegistryInvalid, declaration.ID, err)
			}
			resultValidator, resultDigest, err := compilePluginCommandSchema(extension, declaration.ResultSchema)
			if err != nil {
				return nil, fmt.Errorf("%w: command %s result schema: %v", ErrPluginCommandRegistryInvalid, declaration.ID, err)
			}
			state.commands[declaration.ID] = PluginCommandContract{
				ID: declaration.ID, ContractVersion: declaration.ContractVersion,
				ExtensionID: extension.ID, ExtensionVersion: extension.Version,
				ArtifactDigest: extension.PackageDigest, InstanceID: registration.instanceID,
				Handler: declaration.Handler, Permission: declaration.Permission,
				InputSchema: declaration.InputSchema, ResultSchema: declaration.ResultSchema,
				InputSchemaDigest: inputDigest, ResultSchemaDigest: resultDigest,
				Description: declaration.Description, RecoverySafe: declaration.RecoverySafe,
				Timeout:        time.Duration(declaration.TimeoutMS) * time.Millisecond,
				inputValidator: inputValidator, resultValidator: resultValidator,
			}
		}
	}
	return state, nil
}

func compilePluginCommandSchema(extension extensions.Extension, reference string) (providerDocumentValidator, string, error) {
	if strings.TrimSpace(extension.PackagePath) == "" {
		digest, _ := providerSchemaDigest(extension, reference)
		return nil, digest, nil
	}
	return compileExactProviderSchema(extension, reference)
}

func validatePluginCommandUpgrade(previous, next extensions.Extension) error {
	oldCommands := make(map[string]extensions.ManifestCommand, len(previous.Manifest.Commands))
	for _, command := range previous.Manifest.Commands {
		oldCommands[command.ID] = command
	}
	for _, command := range next.Manifest.Commands {
		old, ok := oldCommands[command.ID]
		if !ok || old.ContractVersion != command.ContractVersion {
			continue
		}
		oldInput, oldInputOK := providerSchemaDigest(previous, old.InputSchema)
		newInput, newInputOK := providerSchemaDigest(next, command.InputSchema)
		oldResult, oldResultOK := providerSchemaDigest(previous, old.ResultSchema)
		newResult, newResultOK := providerSchemaDigest(next, command.ResultSchema)
		if old.InputSchema != command.InputSchema || old.ResultSchema != command.ResultSchema ||
			old.Handler != command.Handler || old.Permission != command.Permission ||
			old.RecoverySafe != command.RecoverySafe || old.TimeoutMS != command.TimeoutMS ||
			oldInputOK != newInputOK || oldResultOK != newResultOK ||
			oldInputOK && oldInput != newInput || oldResultOK && oldResult != newResult {
			return fmt.Errorf("%w: command %s changed without contract version", ErrPluginCommandRegistryConflict, command.ID)
		}
	}
	return nil
}

func clonePluginCommandRegistrations(values map[string]pluginCommandRuntimeRegistration) map[string]pluginCommandRuntimeRegistration {
	result := make(map[string]pluginCommandRuntimeRegistration, len(values))
	for id, registration := range values {
		result[id] = pluginCommandRuntimeRegistration{
			extension: cloneHookExtension(registration.extension), instanceID: registration.instanceID,
		}
	}
	return result
}
