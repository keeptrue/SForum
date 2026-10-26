package extensionsruntime

import (
	"fmt"
	"sort"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// NewManagerPluginRuntimeFullSetApplier 构造 Protocol V2 full-set 适配器。
func NewManagerPluginRuntimeFullSetApplier(
	manager *Manager,
	inventory PluginRuntimeFullSetInventory,
	tierOrders ...[]string,
) (*ManagerPluginRuntimeFullSetApplier, error) {
	if manager == nil || inventory == nil {
		return nil, ErrPluginRuntimeFullSetInvalid
	}
	order := map[string]int{}
	if len(tierOrders) > 0 {
		for index, id := range tierOrders[0] {
			id = strings.ToLower(strings.TrimSpace(id))
			if id != "" {
				order[id] = index
			}
		}
	}
	return &ManagerPluginRuntimeFullSetApplier{
		manager: manager, inventory: inventory,
		drainTimeout: RecommendedPluginRuntimeFullSetDrainTimeout,
		tierOrder:    order,
	}, nil
}

func (a *ManagerPluginRuntimeFullSetApplier) sortDesiredByTier(desired []pluginRuntimeFullSetDesired) {
	if a == nil || len(a.tierOrder) == 0 {
		return
	}
	sort.SliceStable(desired, func(i, j int) bool {
		iRank, iOK := a.tierOrder[strings.ToLower(desired[i].member.ExtensionID)]
		jRank, jOK := a.tierOrder[strings.ToLower(desired[j].member.ExtensionID)]
		if iOK != jOK {
			return iOK
		}
		if iOK && iRank != jRank {
			return iRank < jRank
		}
		return desired[i].member.ExtensionID < desired[j].member.ExtensionID
	})
}

// pluginRuntimeHookSetMatchesPlan verifies every Manager-owned runtime
// registry family against the same exact desired set. It is the read-only fast
// path for an ambiguous acknowledgement replay.
func pluginRuntimeHookSetMatchesPlan(bus *HookBus, plan *pluginRuntimeFullSetPlan) bool {
	if bus == nil || plan == nil {
		return false
	}
	want := make(map[string]pluginRuntimeFullSetMemberPlan, len(plan.desired))
	wantVersioned := make(map[string]pluginRuntimeFullSetMemberPlan, len(plan.desired))
	for _, item := range plan.desired {
		want[item.member.ExtensionID] = item
		if publishesVersionedHookSnapshot(item.extension, item.finalIdentity().InstanceID) {
			wantVersioned[item.member.ExtensionID] = item
		}
	}
	matchesHookRegistration := func(extensionID, instanceID string, extension extensions.Extension) bool {
		item, ok := want[extensionID]
		if !ok || item.finalIdentity().InstanceID != instanceID {
			return false
		}
		return extension.Version == item.member.ExtensionVersion &&
			extension.PackageDigest == item.member.PackageDigest &&
			extension.ActiveVersionID == item.member.ExtensionVersionID
	}

	bus.mu.RLock()
	defer bus.mu.RUnlock()
	if bus.runtimeSetPublicationRevision != plan.publicationRevision || len(bus.plugins) != len(want) {
		return false
	}
	for id, registration := range bus.plugins {
		if !matchesHookRegistration(id, registration.instanceID, registration.extension) {
			return false
		}
	}
	hooks := bus.registry.load()
	providers := bus.providerSlots.load()
	commands := bus.commands.load()
	admin := bus.adminSurfaces.load()
	if len(hooks.extensions) != len(wantVersioned) || len(providers.extensions) != len(wantVersioned) ||
		len(commands.registrations) != len(wantVersioned) || len(admin.registrations) != len(wantVersioned) {
		return false
	}
	for id, registration := range hooks.extensions {
		if !matchesHookRegistration(id, registration.instanceID, registration.extension) {
			return false
		}
	}
	for id, registration := range providers.extensions {
		if !matchesHookRegistration(id, registration.instanceID, registration.extension) {
			return false
		}
	}
	for id, registration := range commands.registrations {
		if !matchesHookRegistration(id, registration.instanceID, registration.extension) {
			return false
		}
	}
	for id, registration := range admin.registrations {
		if !matchesHookRegistration(id, registration.instanceID, registration.extension) {
			return false
		}
	}
	return true
}

type pluginRuntimeHookSetTransaction struct {
	bus                 *HookBus
	publicationRevision int64
	plugins             map[string]hookRuntimeRegistration
	hooks               *versionedHookRegistryState
	providers           *providerSlotRegistryState
	commands            *pluginCommandRegistryState
	admin               *adminSurfaceRegistryState
	locked              bool
}

func (a *ManagerPluginRuntimeFullSetApplier) preparePluginRuntimeHookSet(
	plan *pluginRuntimeFullSetPlan,
) (*pluginRuntimeHookSetTransaction, error) {
	bus := a.manager.hooks
	if bus == nil || bus.registry == nil || bus.providerSlots == nil || bus.commands == nil || bus.adminSurfaces == nil {
		return nil, ErrPluginRuntimeFullSetInvalid
	}

	plugins := make(map[string]hookRuntimeRegistration, len(plan.desired))
	versionedPlugins := make(map[string]hookRuntimeRegistration, len(plan.desired))
	commandRegistrations := make(map[string]pluginCommandRuntimeRegistration, len(plan.desired))
	adminRegistrations := make(map[string]adminSurfaceRuntimeRegistration, len(plan.desired))
	for _, item := range plan.desired {
		identity := item.finalIdentity()
		registration := hookRuntimeRegistration{
			extension: cloneHookExtension(item.extension), instanceID: identity.InstanceID,
		}
		plugins[item.member.ExtensionID] = registration
		if !publishesVersionedHookSnapshot(item.extension, identity.InstanceID) {
			continue
		}
		versionedPlugins[item.member.ExtensionID] = registration
		commandRegistrations[item.member.ExtensionID] = pluginCommandRuntimeRegistration{
			extension: cloneHookExtension(item.extension), instanceID: identity.InstanceID,
		}
		adminRegistrations[item.member.ExtensionID] = adminSurfaceRuntimeRegistration{
			extension: cloneAdminSurfaceExtension(item.extension), instanceID: identity.InstanceID,
		}
	}

	// 与 HookBus.unregisterRuntime 保持相同的外层顺序；读者继续使用旧的
	// immutable states，直到四个完整 desired graphs 都构建成功。
	bus.mu.Lock()
	bus.providerSlots.mu.Lock()
	bus.registry.mu.Lock()
	bus.commands.mu.Lock()
	bus.adminSurfaces.mu.Lock()
	tx := &pluginRuntimeHookSetTransaction{
		bus: bus, publicationRevision: plan.publicationRevision, plugins: plugins, locked: true,
	}
	fail := func(err error) (*pluginRuntimeHookSetTransaction, error) {
		tx.abort()
		return nil, err
	}

	currentHooks := bus.registry.load()
	nextHooks, err := buildVersionedHookRegistryState(currentHooks.revision+1, versionedPlugins)
	if err != nil {
		return fail(err)
	}
	currentProviders := bus.providerSlots.load()
	nextProviders, err := buildProviderSlotRegistryState(currentProviders.revision+1, versionedPlugins)
	if err != nil {
		return fail(err)
	}
	for extensionID := range currentProviders.extensions {
		if _, remains := versionedPlugins[extensionID]; remains {
			if err := validateProviderSlotSchemaContinuity(currentProviders, nextProviders, extensionID); err != nil {
				return fail(err)
			}
		}
	}
	currentCommands := bus.commands.load()
	for extensionID, next := range commandRegistrations {
		if previous, ok := currentCommands.registrations[extensionID]; ok {
			if err := validatePluginCommandUpgrade(previous.extension, next.extension); err != nil {
				return fail(err)
			}
		}
	}
	nextCommands, err := buildPluginCommandRegistryState(currentCommands.revision+1, commandRegistrations)
	if err != nil {
		return fail(err)
	}
	currentAdmin := bus.adminSurfaces.load()
	for extensionID, next := range adminRegistrations {
		if previous, ok := currentAdmin.registrations[extensionID]; ok {
			if err := validateAdminSurfaceUpgrade(previous.extension, next.extension); err != nil {
				return fail(err)
			}
		}
	}
	nextAdmin, err := buildAdminSurfaceRegistryState(currentAdmin.revision+1, adminRegistrations)
	if err != nil {
		return fail(err)
	}
	tx.hooks = nextHooks
	tx.providers = nextProviders
	tx.commands = nextCommands
	tx.admin = nextAdmin
	return tx, nil
}

func (t *pluginRuntimeHookSetTransaction) storeLocked() {
	t.bus.registry.state.Store(t.hooks)
	t.bus.providerSlots.state.Store(t.providers)
	t.bus.commands.state.Store(t.commands)
	t.bus.adminSurfaces.state.Store(t.admin)
	t.bus.plugins = t.plugins
	t.bus.runtimeSetGeneration++
	t.bus.runtimeSetPublicationRevision = t.publicationRevision
}

func (t *pluginRuntimeHookSetTransaction) abort() {
	if t == nil || !t.locked {
		return
	}
	t.locked = false
	t.bus.adminSurfaces.mu.Unlock()
	t.bus.commands.mu.Unlock()
	t.bus.registry.mu.Unlock()
	t.bus.providerSlots.mu.Unlock()
	t.bus.mu.Unlock()
}

// commitPluginRuntimeFullSet validates the old complete set, opens only new
// candidates, then swaps Manager pointers and all HookBus family snapshots.
// Reused runtimes stay live and may retain arbitrary ordinary calls.
func (a *ManagerPluginRuntimeFullSetApplier) commitPluginRuntimeFullSet(
	plan *pluginRuntimeFullSetPlan,
	hookSet *pluginRuntimeHookSetTransaction,
) error {
	m := a.manager
	if hookSet == nil || !hookSet.locked {
		return ErrPluginRuntimeFullSetInvalid
	}
	m.mu.Lock()

	expectedBefore := make(map[string]string, len(plan.desired)+len(plan.removals))
	for _, item := range plan.desired {
		identity := item.finalIdentity()
		if identity.ExtensionID == "" || identity.InstanceID == "" {
			m.mu.Unlock()
			return fmt.Errorf("%w: missing desired runtime identity for %s", ErrPluginRuntimeFullSetConflict, item.member.ExtensionID)
		}
		instance, err := m.runtimeInstanceLocked(identity)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		if instance.transitioning || !pluginRuntimeActiveMatchesDesired(instance, item.extension, item.member) {
			m.mu.Unlock()
			return fmt.Errorf("%w: desired instance does not match exact artifact %s", ErrPluginRuntimeFullSetConflict, item.member.ExtensionID)
		}
		admission := instance.gate.Snapshot()
		if admission.Forced {
			m.mu.Unlock()
			return fmt.Errorf("%w: %s/%s", ErrRuntimeAdmissionForced, identity.ExtensionID, identity.InstanceID)
		}
		activeID := m.activeInstances[identity.ExtensionID]
		if item.reuse {
			if activeID != identity.InstanceID || admission.Draining {
				m.mu.Unlock()
				return fmt.Errorf("%w: reused instance is not active for %s", ErrPluginRuntimeFullSetConflict, identity.ExtensionID)
			}
		} else {
			if !item.protocolPublished || !admission.Draining || admission.ActiveTotal != 0 {
				m.mu.Unlock()
				return fmt.Errorf("%w: candidate %s/%s is not closed and published", ErrPluginRuntimeFullSetConflict, identity.ExtensionID, identity.InstanceID)
			}
			if (item.hadOld && activeID != item.old.InstanceID) || (!item.hadOld && activeID != "") {
				m.mu.Unlock()
				return fmt.Errorf("%w: active pointer drifted for %s", ErrPluginRuntimeFullSetConflict, identity.ExtensionID)
			}
		}
		if item.reuse || item.hadOld {
			expectedBefore[identity.ExtensionID] = item.old.InstanceID
		}
	}

	for _, removal := range plan.removals {
		instance, err := m.runtimeInstanceLocked(removal.identity)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		admission := instance.gate.Snapshot()
		if instance.transitioning || !admission.Draining || admission.ActiveTotal != 0 {
			m.mu.Unlock()
			return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotDrained, removal.identity.ExtensionID, removal.identity.InstanceID)
		}
		if activeID := m.activeInstances[removal.identity.ExtensionID]; activeID != removal.identity.InstanceID {
			m.mu.Unlock()
			return fmt.Errorf("%w: removal active pointer drifted for %s", ErrPluginRuntimeFullSetConflict, removal.identity.ExtensionID)
		}
		expectedBefore[removal.identity.ExtensionID] = removal.identity.InstanceID
	}

	if len(m.activeInstances) != len(expectedBefore) {
		m.mu.Unlock()
		return fmt.Errorf("%w: active runtime set size drifted", ErrPluginRuntimeFullSetConflict)
	}
	for extensionID, instanceID := range m.activeInstances {
		if want, ok := expectedBefore[extensionID]; !ok || want != instanceID {
			m.mu.Unlock()
			return fmt.Errorf("%w: unplanned active runtime %s/%s", ErrPluginRuntimeFullSetConflict, extensionID, instanceID)
		}
	}

	// 候选尚未成为活动指针，先全部开门；任一失败时可重新关闭，且没有
	// caller 能观察到部分新 set。复用实例从不 Resume。
	resumed := make([]*managedRuntimeInstance, 0, len(plan.desired))
	for _, item := range plan.desired {
		if item.reuse {
			continue
		}
		instance := m.runtimeInstances[item.candidate.ExtensionID][item.candidate.InstanceID]
		if _, err := instance.gate.Resume(); err != nil {
			for _, opened := range resumed {
				opened.gate.BeginDrain()
			}
			m.mu.Unlock()
			return fmt.Errorf("resume %s/%s: %w", item.candidate.ExtensionID, item.candidate.InstanceID, err)
		}
		resumed = append(resumed, instance)
	}

	now := time.Now().UTC()
	for index := range plan.desired {
		item := &plan.desired[index]
		if item.reuse {
			continue
		}
		identity := item.finalIdentity()
		instance := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]
		instance.extension = item.extension
		instance.extensionVersion = item.extension.Version
		instance.artifactDigest = item.extension.PackageDigest
		instance.target.InstanceID = identity.InstanceID
		m.activeInstances[identity.ExtensionID] = identity.InstanceID
		m.targets[identity.ExtensionID] = instance.target
		m.running[identity.ExtensionID] = item.extension
		m.statuses[identity.ExtensionID] = managedRuntimeStatus(item.extension, extensions.RuntimeRunning, &now)
	}
	for _, removal := range plan.removals {
		delete(m.activeInstances, removal.identity.ExtensionID)
		delete(m.targets, removal.identity.ExtensionID)
		delete(m.running, removal.identity.ExtensionID)
		m.statuses[removal.identity.ExtensionID] = managedRuntimeStatus(removal.extension, extensions.RuntimeStopped, nil)
	}
	hookSet.storeLocked()
	m.mu.Unlock()
	hookSet.abort()
	return nil
}

// resumePluginRuntimeFullSetPreviousAtomically reopens the complete previous
// Manager admission set under one pointer lock. Any validation/resume failure
// closes every member again so an unsuccessful rollback cannot expose a
// partially callable graph.
func (a *ManagerPluginRuntimeFullSetApplier) resumePluginRuntimeFullSetPreviousAtomically(
	plan *pluginRuntimeFullSetPlan,
) error {
	if a == nil || a.manager == nil || plan == nil {
		return ErrPluginRuntimeFullSetInvalid
	}
	type restoreTarget struct {
		identity RuntimeInstanceIdentity
		instance *managedRuntimeInstance
	}
	targets := make([]restoreTarget, 0, len(plan.desired)+len(plan.removals))
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	failClosed := func() {
		for _, target := range targets {
			target.instance.gate.BeginDrain()
		}
	}
	appendTarget := func(identity RuntimeInstanceIdentity) error {
		instance, err := m.runtimeInstanceLocked(identity)
		if err != nil {
			return err
		}
		targets = append(targets, restoreTarget{identity: identity, instance: instance})
		if m.activeInstances[identity.ExtensionID] != identity.InstanceID {
			return fmt.Errorf("%w: rollback target %s/%s is not active", ErrPluginRuntimeFullSetConflict, identity.ExtensionID, identity.InstanceID)
		}
		admission := instance.gate.Snapshot()
		if !admission.Draining || admission.Forced || admission.ActiveByClass[RuntimeCallLifecycleCleanup] != 0 {
			return fmt.Errorf("%w: rollback target %s/%s cannot resume", ErrRuntimeAdmissionBusy, identity.ExtensionID, identity.InstanceID)
		}
		return nil
	}
	for _, item := range plan.desired {
		if item.reuse || !item.hadOld || !item.drainStarted {
			continue
		}
		if err := appendTarget(item.old); err != nil {
			failClosed()
			return err
		}
	}
	for _, removal := range plan.removals {
		if !removal.drainStarted {
			continue
		}
		if err := appendTarget(removal.identity); err != nil {
			failClosed()
			return err
		}
	}
	for _, target := range targets {
		if _, err := target.instance.gate.Resume(); err != nil {
			failClosed()
			return fmt.Errorf("resume rollback target %s/%s: %w", target.identity.ExtensionID, target.identity.InstanceID, err)
		}
	}
	for index := range plan.desired {
		item := &plan.desired[index]
		if item.reuse || !item.hadOld || !item.drainStarted {
			continue
		}
		item.drainStarted = false
		item.drainCompleted = false
	}
	for index := range plan.removals {
		removal := &plan.removals[index]
		if !removal.drainStarted {
			continue
		}
		removal.drainStarted = false
		removal.drainCompleted = false
	}
	return nil
}
