package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type HookInput struct {
	DeclarationID   string
	Name            string
	Kind            string
	ContractVersion string
	InputSchema     string
	ResultSchema    string
	FailurePolicy   string
	DeliveryID      int64
	CorrelationID   string
	Timeout         time.Duration
	Payload         map[string]any
	PatchFields     []string
}

type HookResult struct {
	OK      bool
	Reason  string
	Message string
	Patch   map[string]any
	Result  map[string]any
}

type HookInvoker interface {
	InvokeHook(ctx context.Context, extension extensions.Extension, input HookInput) HookResult
}

type HookInvokerFunc func(context.Context, extensions.Extension, HookInput) HookResult

func (fn HookInvokerFunc) InvokeHook(ctx context.Context, extension extensions.Extension, input HookInput) HookResult {
	return fn(ctx, extension, input)
}

type HookBusConfig struct {
	Invoker HookInvoker
}

type HookBus struct {
	mu                            sync.RWMutex
	runtimeSetGeneration          uint64
	runtimeSetPublicationRevision int64
	invoker                       HookInvoker
	plugins                       map[string]hookRuntimeRegistration
	registry                      *VersionedHookRegistry
	providerSlots                 *VersionedProviderSlotRegistry
	commands                      *PluginCommandRegistry
	adminSurfaces                 *AdminSurfaceRegistry
}

type hookRuntimeRegistration struct {
	extension  extensions.Extension
	instanceID string
}

// HookRuntimeSnapshot binds the visible hook declarations to one exact
// runtime. InstanceID is empty only for the legacy compatibility path.
type HookRuntimeSnapshot struct {
	Extension  extensions.Extension
	InstanceID string
}

// RuntimeRegistryGenerationSnapshot is one outer-lock observation of every
// Manager-owned HookBus registry family. Individual registries stay available
// for compatibility, while coordinators and inspectors can pin one generation.
type RuntimeRegistryGenerationSnapshot struct {
	Generation           uint64
	PublicationRevision  int64
	RuntimeMembers       []RuntimeInstanceIdentity
	HookMembers          []RuntimeInstanceIdentity
	ProviderMembers      []RuntimeInstanceIdentity
	CommandMembers       []RuntimeInstanceIdentity
	AdminSurfaceMembers  []RuntimeInstanceIdentity
	HookRevision         uint64
	ProviderRevision     uint64
	CommandRevision      uint64
	AdminSurfaceRevision uint64
}

func NewHookBus(config HookBusConfig) *HookBus {
	bus := &HookBus{
		invoker: config.Invoker, plugins: map[string]hookRuntimeRegistration{},
		registry: NewVersionedHookRegistry(), providerSlots: NewVersionedProviderSlotRegistry(),
		commands: NewPluginCommandRegistry(), adminSurfaces: NewAdminSurfaceRegistry(),
	}
	bus.registry.generationBarrier = &bus.mu
	bus.providerSlots.generationBarrier = &bus.mu
	bus.commands.generationBarrier = &bus.mu
	bus.adminSurfaces.generationBarrier = &bus.mu
	return bus
}

func (b *HookBus) Register(extension extensions.Extension) {
	if extension.Status != extensions.StatusEnabled {
		return
	}
	_ = b.RegisterRuntime(extension, "")
}

// RegisterRuntime atomically replaces one extension's hook declarations with
// the declarations owned by an exact process. Manager admission remains the
// execution gate, so registering a drained target does not open ordinary calls.
func (b *HookBus) RegisterRuntime(extension extensions.Extension, instanceID string) error {
	if extension.Type != extensions.TypePlugin {
		return nil
	}
	// HookBus.mu is the outer transaction lock for every runtime registry
	// family. Full-set publication uses the same order, so a delayed single-
	// runtime Register cannot overwrite only plugins after a desired-set commit.
	b.mu.Lock()
	defer b.mu.Unlock()
	previous, hadPrevious := b.plugins[extension.ID]
	if publishesVersionedHookSnapshot(extension, instanceID) {
		if err := b.providerSlots.ReplaceRuntime(extension, instanceID); err != nil {
			return err
		}
		if err := b.registry.ReplaceRuntime(extension, instanceID); err != nil {
			if hadPrevious && publishesVersionedHookSnapshot(previous.extension, previous.instanceID) {
				_ = b.providerSlots.ReplaceRuntime(previous.extension, previous.instanceID)
			} else {
				_, _ = b.providerSlots.RemoveRuntime(extension.ID, instanceID)
			}
			return err
		}
		if err := b.commands.ReplaceRuntime(extension, instanceID); err != nil {
			if hadPrevious && publishesVersionedHookSnapshot(previous.extension, previous.instanceID) {
				_ = b.providerSlots.ReplaceRuntime(previous.extension, previous.instanceID)
				_ = b.registry.ReplaceRuntime(previous.extension, previous.instanceID)
			} else {
				_, _ = b.providerSlots.RemoveRuntime(extension.ID, instanceID)
				_, _ = b.registry.RemoveRuntime(extension.ID, instanceID)
			}
			return err
		}
		if err := b.adminSurfaces.ReplaceRuntime(extension, instanceID); err != nil {
			if hadPrevious && publishesVersionedHookSnapshot(previous.extension, previous.instanceID) {
				_ = b.providerSlots.ReplaceRuntime(previous.extension, previous.instanceID)
				_ = b.registry.ReplaceRuntime(previous.extension, previous.instanceID)
				_ = b.commands.ReplaceRuntime(previous.extension, previous.instanceID)
			} else {
				_, _ = b.providerSlots.RemoveRuntime(extension.ID, instanceID)
				_, _ = b.registry.RemoveRuntime(extension.ID, instanceID)
				_, _ = b.commands.RemoveRuntime(extension.ID, instanceID)
			}
			return err
		}
	}
	b.plugins[extension.ID] = hookRuntimeRegistration{extension: cloneHookExtension(extension), instanceID: instanceID}
	b.runtimeSetGeneration++
	b.runtimeSetPublicationRevision = 0
	return nil
}

func (b *HookBus) Unregister(extensionID string) {
	b.mu.RLock()
	current, ok := b.plugins[extensionID]
	b.mu.RUnlock()
	if ok {
		_ = b.UnregisterRuntime(extensionID, current.instanceID)
	}
}

// UnregisterRuntime removes declarations only while they still belong to the
// stopping process. A retained source can therefore never clear its published
// replacement.
func (b *HookBus) UnregisterRuntime(extensionID, instanceID string) bool {
	removed, _ := b.unregisterRuntime(extensionID, instanceID)
	return removed
}

func (b *HookBus) unregisterRuntime(extensionID, instanceID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.plugins[extensionID]
	if !ok || current.instanceID != instanceID {
		return false, nil
	}
	if publishesVersionedHookSnapshot(current.extension, current.instanceID) {
		if err := b.providerSlots.ValidateRemoveRuntime(extensionID, instanceID); err != nil {
			return false, err
		}
		if err := b.registry.ValidateRemoveRuntime(extensionID, instanceID); err != nil {
			return false, err
		}
		if err := b.commands.ValidateRemoveRuntime(extensionID, instanceID); err != nil {
			return false, err
		}
		if err := b.adminSurfaces.ValidateRemoveRuntime(extensionID, instanceID); err != nil {
			return false, err
		}
		adminRemoved, adminErr := b.adminSurfaces.RemoveRuntime(extensionID, instanceID)
		if adminErr != nil || !adminRemoved {
			return false, adminErr
		}
		commandRemoved, commandErr := b.commands.RemoveRuntime(extensionID, instanceID)
		if commandErr != nil || !commandRemoved {
			_ = b.adminSurfaces.ReplaceRuntime(current.extension, current.instanceID)
			return false, commandErr
		}
		removed, err := b.registry.RemoveRuntime(extensionID, instanceID)
		if err != nil || !removed {
			_ = b.commands.ReplaceRuntime(current.extension, current.instanceID)
			_ = b.adminSurfaces.ReplaceRuntime(current.extension, current.instanceID)
			return false, err
		}
		providerRemoved, providerErr := b.providerSlots.RemoveRuntime(extensionID, instanceID)
		if providerErr != nil || !providerRemoved {
			_ = b.registry.ReplaceRuntime(current.extension, current.instanceID)
			_ = b.commands.ReplaceRuntime(current.extension, current.instanceID)
			_ = b.adminSurfaces.ReplaceRuntime(current.extension, current.instanceID)
			return false, providerErr
		}
	}
	delete(b.plugins, extensionID)
	b.runtimeSetGeneration++
	b.runtimeSetPublicationRevision = 0
	return true, nil
}

func (b *HookBus) validateUnregisterRuntime(extension extensions.Extension, instanceID string) error {
	b.mu.RLock()
	current, ok := b.plugins[extension.ID]
	b.mu.RUnlock()
	// Deactivation removes registries before uninstall hooks finish and the
	// retained exact process is stopped. An absent registration is already
	// fail-closed; only a registration owned by another instance is a conflict.
	if !ok {
		return nil
	}
	if current.instanceID != instanceID {
		if publishesVersionedHookSnapshot(extension, instanceID) {
			return fmt.Errorf("%w: exact hook runtime %s/%s is not published", ErrHookRegistryConflict, extension.ID, instanceID)
		}
		return nil
	}
	if !publishesVersionedHookSnapshot(current.extension, current.instanceID) {
		return nil
	}
	return errors.Join(
		b.registry.ValidateRemoveRuntime(extension.ID, instanceID),
		b.providerSlots.ValidateRemoveRuntime(extension.ID, instanceID),
		b.commands.ValidateRemoveRuntime(extension.ID, instanceID),
		b.adminSurfaces.ValidateRemoveRuntime(extension.ID, instanceID),
	)
}

func (b *HookBus) restoreRuntime(targetID, targetInstanceID string, previous HookRuntimeSnapshot, hadPrevious bool) error {
	if hadPrevious {
		return b.RegisterRuntime(previous.Extension, previous.InstanceID)
	}
	_, err := b.unregisterRuntime(targetID, targetInstanceID)
	return err
}

func (b *HookBus) VersionedRegistry() *VersionedHookRegistry { return b.registry }

func (b *HookBus) ProviderSlots() *VersionedProviderSlotRegistry { return b.providerSlots }

func (b *HookBus) Commands() *PluginCommandRegistry { return b.commands }

func (b *HookBus) AdminSurfaces() *AdminSurfaceRegistry { return b.adminSurfaces }

func (b *HookBus) RuntimeSnapshot(extensionID string) (HookRuntimeSnapshot, bool) {
	b.mu.RLock()
	current, ok := b.plugins[extensionID]
	b.mu.RUnlock()
	if !ok {
		return HookRuntimeSnapshot{}, false
	}
	return HookRuntimeSnapshot{Extension: current.extension, InstanceID: current.instanceID}, true
}

// RuntimeRegistryGeneration returns one coherent cross-family observation.
// Every production HookBus writer owns b.mu for its whole multi-registry
// transaction, so this method never exposes an in-progress family swap.
func (b *HookBus) RuntimeRegistryGeneration() RuntimeRegistryGenerationSnapshot {
	if b == nil {
		return RuntimeRegistryGenerationSnapshot{}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	hooks := b.registry.load()
	providers := b.providerSlots.load()
	commands := b.commands.load()
	admin := b.adminSurfaces.load()
	snapshot := RuntimeRegistryGenerationSnapshot{
		Generation: b.runtimeSetGeneration, PublicationRevision: b.runtimeSetPublicationRevision,
		HookRevision: hooks.revision, ProviderRevision: providers.revision,
		CommandRevision: commands.revision, AdminSurfaceRevision: admin.revision,
		RuntimeMembers:      runtimeRegistrationIdentities(b.plugins),
		HookMembers:         runtimeRegistrationIdentities(hooks.extensions),
		ProviderMembers:     runtimeRegistrationIdentities(providers.extensions),
		CommandMembers:      commandRegistrationIdentities(commands.registrations),
		AdminSurfaceMembers: adminSurfaceRegistrationIdentities(admin.registrations),
	}
	return snapshot
}

func runtimeRegistrationIdentities(registrations map[string]hookRuntimeRegistration) []RuntimeInstanceIdentity {
	identities := make([]RuntimeInstanceIdentity, 0, len(registrations))
	for extensionID, registration := range registrations {
		identities = append(identities, RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: registration.instanceID})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].ExtensionID < identities[j].ExtensionID })
	return identities
}

func commandRegistrationIdentities(registrations map[string]pluginCommandRuntimeRegistration) []RuntimeInstanceIdentity {
	identities := make([]RuntimeInstanceIdentity, 0, len(registrations))
	for extensionID, registration := range registrations {
		identities = append(identities, RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: registration.instanceID})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].ExtensionID < identities[j].ExtensionID })
	return identities
}

func adminSurfaceRegistrationIdentities(registrations map[string]adminSurfaceRuntimeRegistration) []RuntimeInstanceIdentity {
	identities := make([]RuntimeInstanceIdentity, 0, len(registrations))
	for extensionID, registration := range registrations {
		identities = append(identities, RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: registration.instanceID})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].ExtensionID < identities[j].ExtensionID })
	return identities
}

func hasVersionedPluginHooks(extension extensions.Extension) bool {
	for _, hook := range extension.Manifest.Hooks {
		if isVersionedPluginHook(hook) {
			return true
		}
	}
	return false
}

func hasVersionedProviderSlots(extension extensions.Extension) bool {
	for _, provider := range extension.Manifest.Providers {
		if isVersionedProviderSlot(provider) {
			return true
		}
	}
	return false
}

func hasVersionedRuntimeContracts(extension extensions.Extension) bool {
	return hasVersionedPluginHooks(extension) || hasVersionedProviderSlots(extension) ||
		len(extension.Manifest.Commands) > 0 || len(extension.Manifest.AdminSurfaces) > 0 ||
		hasQueryRegistryPublication(extension.Manifest)
}

func publishesVersionedHookSnapshot(extension extensions.Extension, instanceID string) bool {
	return instanceID != "" && extension.Manifest.Backend.ProtocolVersion == 2 &&
		(strings.TrimSpace(extension.PackageDigest) != "" || hasVersionedRuntimeContracts(extension))
}

func runtimeManifestCounts(manifest extensions.Manifest) (hooks, events int) {
	// events 不吸收 legacy hooks，避免同一声明同时出现在两个运行状态计数。
	return len(manifest.Hooks), len(manifest.Events)
}

func (b *HookBus) Emit(ctx context.Context, input HookInput) []HookResult {
	plugins := b.listeners(input.Name)
	results := []HookResult{}
	for _, plugin := range plugins {
		if b.invoker == nil {
			continue
		}
		results = append(results, b.invoker.InvokeHook(ctx, plugin, input))
	}
	return results
}

func (b *HookBus) Listeners(name string) []extensions.Extension {
	return b.listeners(name)
}

func (b *HookBus) listeners(name string) []extensions.Extension {
	b.mu.RLock()
	plugins := make([]extensions.Extension, 0, len(b.plugins))
	for _, registration := range b.plugins {
		if declaresHook(registration.extension, name) {
			plugins = append(plugins, registration.extension)
		}
	}
	b.mu.RUnlock()
	sort.Slice(plugins, func(left int, right int) bool {
		return plugins[left].ID < plugins[right].ID
	})
	return plugins
}

func declaresHook(extension extensions.Extension, name string) bool {
	for _, event := range extensions.DeclaredManifestEvents(extension.Manifest) {
		if event.Name == name {
			return true
		}
	}
	return false
}

func hookInputFromEnvelope(envelope appevents.Envelope, deliveryID int64) HookInput {
	timeout := time.Duration(0)
	if definition, ok := appevents.FindDefinition(envelope.Name); ok && definition.TimeoutMS > 0 {
		timeout = time.Duration(definition.TimeoutMS) * time.Millisecond
	}
	return HookInput{
		Name:          envelope.Name,
		Kind:          envelope.Kind,
		DeliveryID:    deliveryID,
		CorrelationID: envelope.CorrelationID,
		Timeout:       timeout,
		Payload:       envelope.Payload,
		PatchFields:   envelope.PatchFields,
	}
}
