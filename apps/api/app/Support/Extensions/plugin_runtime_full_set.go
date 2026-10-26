package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	// ErrPluginRuntimeFullSetInvalid 表示 full-set 适配器入参或 durable publication 不合法。
	ErrPluginRuntimeFullSetInvalid = errors.New("extensions: plugin runtime full set is invalid")
	// ErrPluginRuntimeFullSetConflict 表示 exact artifact / Manager 指针与期望 full set 不一致。
	ErrPluginRuntimeFullSetConflict = errors.New("extensions: plugin runtime full set conflict")
)

const RecommendedPluginRuntimeFullSetDrainTimeout = 30 * time.Second

// PluginRuntimeFullSetInventory 提供 exact-artifact 元数据，不信任当前活动版本字段。
// Get 只用于扩展身份元数据；不可变包内容必须以 GetExtensionVersion 为准。
type PluginRuntimeFullSetInventory interface {
	Get(context.Context, string) (extensions.Extension, error)
	GetExtensionVersion(context.Context, extensions.ExactExtensionVersionInput) (extensions.ExtensionVersion, error)
	LatestPluginRuntimePublication(context.Context) (extensions.PluginRuntimePublication, error)
}

// ManagerPluginRuntimeFullSetApplier 是 process-local 的 PluginRuntimeFullSetApplier。
// 它把一条 durable full-set publication 收敛到 *Manager 的 exact 活动集合。
type ManagerPluginRuntimeFullSetApplier struct {
	manager      *Manager
	inventory    PluginRuntimeFullSetInventory
	drainTimeout time.Duration
	tierOrder    map[string]int
}

var _ extensions.PluginRuntimeFullSetApplier = (*ManagerPluginRuntimeFullSetApplier)(nil)

// ApplyPluginRuntimeFullSet 将 Manager 收敛到 publication 描述的完整 exact 集合。
// 成功时返回完整 applied evidence（含 node-local runtime instance id）。
func (a *ManagerPluginRuntimeFullSetApplier) ApplyPluginRuntimeFullSet(
	ctx context.Context,
	publication extensions.PluginRuntimePublication,
) (applied []extensions.PluginRuntimeAppliedMember, err error) {
	if a == nil || a.manager == nil || a.inventory == nil || ctx == nil {
		return nil, ErrPluginRuntimeFullSetInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	desired, err := a.resolvePluginRuntimeFullSet(ctx, publication)
	if err != nil {
		return nil, err
	}
	// Inventory/数据库解析不占用运行时写事务；从读取 Manager 状态开始，所有
	// applier 与 legacy lifecycle 共用同一把 Manager 级 transition barrier。
	unlock, err := a.manager.lockRuntimeSetTransition(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A revoke holds this same Manager barrier through its durable R+1 commit.
	// Any older coordinator waiter must therefore re-read Latest after waking;
	// otherwise it could reopen the just-revoked member before observing R+1.
	if err := a.requireLatestPluginRuntimePublication(ctx, publication); err != nil {
		return nil, err
	}
	if err := a.requireNonRegressivePluginRuntimePublication(publication.Revision); err != nil {
		return nil, err
	}

	plan, err := a.buildPluginRuntimeFullSetPlan(publication.Revision, desired)
	if err != nil {
		return nil, err
	}
	if err := a.recheckReusedPluginRuntimes(ctx, plan); err != nil {
		return nil, err
	}
	// 正常的歧义重试是只读校验：允许复用实例继续承载任意数量的在途请求。
	if plan.needsOnlyCommit() && a.pluginRuntimeFullSetVisible(ctx, plan) {
		return plan.appliedMembers(), nil
	}

	if err := a.stageAndHealthPluginRuntimeFullSet(ctx, plan); err != nil {
		a.discardPluginRuntimeFullSetCandidates(context.Background(), plan)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		a.discardPluginRuntimeFullSetCandidates(context.Background(), plan)
		return nil, err
	}

	if err := a.drainPluginRuntimeFullSet(ctx, plan); err != nil {
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	if err := a.publishPluginRuntimeFullSetProtocols(ctx, plan); err != nil {
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	hookSet, err := a.preparePluginRuntimeHookSet(plan)
	if err != nil {
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	if err := ctx.Err(); err != nil {
		hookSet.abort()
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	// Keep the last process/readiness probe immediately adjacent to the
	// aggregate commit. Candidate gates and the protocol/service lease remain
	// closed while the prepared HookBus graph is held off-reader.
	if err := a.revalidatePublishedPluginRuntimeFullSet(ctx, plan); err != nil {
		hookSet.abort()
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	if err := a.commitPluginRuntimeFullSet(plan, hookSet); err != nil {
		hookSet.abort()
		return nil, errors.Join(err, a.rollbackPluginRuntimeFullSet(ctx, plan))
	}
	plan.releaseProtocolSetLease()

	// Registry 已在 commit 中移除，进程停止失败只保留不可调用的旧 artifact，
	// 不会遗留 fail-closed hook，也不会篡改已提交的 applied evidence。
	a.cleanupPluginRuntimeFullSet(plan)
	return plan.appliedMembers(), nil
}

func (a *ManagerPluginRuntimeFullSetApplier) requireLatestPluginRuntimePublication(
	ctx context.Context,
	requested extensions.PluginRuntimePublication,
) error {
	latest, err := a.inventory.LatestPluginRuntimePublication(ctx)
	if err != nil {
		return fmt.Errorf("load latest plugin runtime publication before apply: %w", err)
	}
	if latest.Revision != requested.Revision || latest.MemberCount != requested.MemberCount ||
		latest.MembersDigest != requested.MembersDigest || latest.Reason != requested.Reason ||
		latest.ActorUserID != requested.ActorUserID {
		return fmt.Errorf(
			"%w: requested revision %d, current revision %d",
			extensions.ErrPluginRuntimePublicationSuperseded, requested.Revision, latest.Revision,
		)
	}
	return nil
}

func (a *ManagerPluginRuntimeFullSetApplier) requireNonRegressivePluginRuntimePublication(
	requestedRevision int64,
) error {
	if a == nil || a.manager == nil || a.manager.hooks == nil || requestedRevision <= 0 {
		return ErrPluginRuntimeFullSetInvalid
	}
	a.manager.hooks.mu.RLock()
	appliedRevision := a.manager.hooks.runtimeSetPublicationRevision
	a.manager.hooks.mu.RUnlock()
	if requestedRevision < appliedRevision {
		return fmt.Errorf(
			"%w: requested revision %d is older than process revision %d",
			extensions.ErrPluginRuntimePublicationSuperseded, requestedRevision, appliedRevision,
		)
	}
	return nil
}

type pluginRuntimeFullSetDesired struct {
	member    extensions.PluginRuntimeMember
	extension extensions.Extension
}

type pluginRuntimeFullSetMemberPlan struct {
	member    extensions.PluginRuntimeMember
	extension extensions.Extension

	reuse             bool
	old               RuntimeInstanceIdentity
	hadOld            bool
	candidate         RuntimeInstanceIdentity
	staged            bool
	publishAttempted  bool
	protocolPublished bool
	drainStarted      bool
	drainCompleted    bool
}

type pluginRuntimeFullSetRemoval struct {
	identity       RuntimeInstanceIdentity
	extension      extensions.Extension
	drainStarted   bool
	drainCompleted bool
}

type pluginRuntimeFullSetPlan struct {
	publicationRevision  int64
	desired              []pluginRuntimeFullSetMemberPlan
	removals             []pluginRuntimeFullSetRemoval
	protocolSetPublished bool
	protocolSetLease     *ProtocolRuntimeSetLease
}

func (p *pluginRuntimeFullSetPlan) needsOnlyCommit() bool {
	if p == nil {
		return false
	}
	for _, item := range p.desired {
		if !item.reuse {
			return false
		}
	}
	return len(p.removals) == 0
}

func (p *pluginRuntimeFullSetPlan) appliedMembers() []extensions.PluginRuntimeAppliedMember {
	if p == nil {
		return nil
	}
	applied := make([]extensions.PluginRuntimeAppliedMember, 0, len(p.desired))
	for _, item := range p.desired {
		applied = append(applied, extensions.PluginRuntimeAppliedMember{
			PluginRuntimeMember: item.member,
			RuntimeInstanceID:   item.finalInstanceID(),
		})
	}
	sort.Slice(applied, func(i, j int) bool {
		return applied[i].ExtensionID < applied[j].ExtensionID
	})
	return applied
}

func (p *pluginRuntimeFullSetPlan) finalIdentities() []RuntimeInstanceIdentity {
	if p == nil {
		return nil
	}
	identities := make([]RuntimeInstanceIdentity, 0, len(p.desired))
	for _, item := range p.desired {
		identities = append(identities, item.finalIdentity())
	}
	return identities
}

func (p *pluginRuntimeFullSetPlan) previousIdentities() []RuntimeInstanceIdentity {
	if p == nil {
		return nil
	}
	identities := make([]RuntimeInstanceIdentity, 0, len(p.desired)+len(p.removals))
	for _, item := range p.desired {
		if item.reuse || item.hadOld {
			identities = append(identities, item.old)
		}
	}
	for _, removal := range p.removals {
		identities = append(identities, removal.identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		return identities[i].ExtensionID < identities[j].ExtensionID
	})
	return identities
}

func (p *pluginRuntimeFullSetPlan) releaseProtocolSetLease() {
	if p == nil || p.protocolSetLease == nil {
		return
	}
	p.protocolSetLease.Release()
	p.protocolSetLease = nil
}

func (m pluginRuntimeFullSetMemberPlan) finalIdentity() RuntimeInstanceIdentity {
	if m.reuse {
		return m.old
	}
	return m.candidate
}

func (m pluginRuntimeFullSetMemberPlan) finalInstanceID() string {
	return m.finalIdentity().InstanceID
}

func (a *ManagerPluginRuntimeFullSetApplier) resolvePluginRuntimeFullSet(
	ctx context.Context,
	publication extensions.PluginRuntimePublication,
) ([]pluginRuntimeFullSetDesired, error) {
	if publication.Revision <= 0 {
		return nil, fmt.Errorf("%w: publication revision is required", ErrPluginRuntimeFullSetInvalid)
	}
	members := append([]extensions.PluginRuntimeMember(nil), publication.Members...)
	digest, err := extensions.PluginRuntimeMembersDigest(members)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPluginRuntimeFullSetInvalid, err)
	}
	if publication.MemberCount != len(members) || publication.MembersDigest != digest {
		return nil, fmt.Errorf("%w: publication members digest mismatch", ErrPluginRuntimeFullSetConflict)
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].ExtensionID < members[j].ExtensionID
	})

	desired := make([]pluginRuntimeFullSetDesired, 0, len(members))
	for _, member := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base, err := a.inventory.Get(ctx, member.ExtensionID)
		if err != nil {
			return nil, fmt.Errorf("load extension metadata %s: %w", member.ExtensionID, err)
		}
		if strings.TrimSpace(base.ID) != member.ExtensionID {
			return nil, fmt.Errorf("%w: extension id mismatch for %s", ErrPluginRuntimeFullSetConflict, member.ExtensionID)
		}
		version, err := a.inventory.GetExtensionVersion(ctx, extensions.ExactExtensionVersionInput{
			ExtensionID:   member.ExtensionID,
			Version:       member.ExtensionVersion,
			PackageDigest: member.PackageDigest,
		})
		if err != nil {
			return nil, fmt.Errorf("load extension version %s@%s: %w", member.ExtensionID, member.ExtensionVersion, err)
		}
		if version.ID != member.ExtensionVersionID {
			return nil, fmt.Errorf(
				"%w: extension version id mismatch for %s: got %d want %d",
				ErrPluginRuntimeFullSetConflict, member.ExtensionID, version.ID, member.ExtensionVersionID,
			)
		}
		if version.Version != member.ExtensionVersion || version.PackageDigest != member.PackageDigest {
			return nil, fmt.Errorf("%w: extension version artifact mismatch for %s", ErrPluginRuntimeFullSetConflict, member.ExtensionID)
		}
		exact := pluginRuntimeExactExtension(base, version, member)
		if err := validatePluginRuntimeFullSetDesiredExtension(exact); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPluginRuntimeFullSetInvalid, err)
		}
		desired = append(desired, pluginRuntimeFullSetDesired{member: member, extension: exact})
	}
	a.sortDesiredByTier(desired)
	return desired, nil
}

func validatePluginRuntimeFullSetDesiredExtension(extension extensions.Extension) error {
	return validateManagedStagedExtension(extension)
}

// pluginRuntimeExactExtension 用不可变版本快照构造 exact Extension，不信任当前活动版本字段。
func pluginRuntimeExactExtension(
	base extensions.Extension,
	version extensions.ExtensionVersion,
	member extensions.PluginRuntimeMember,
) extensions.Extension {
	name := strings.TrimSpace(version.Manifest.Name)
	if name == "" {
		name = member.ExtensionID
	}
	extensionType := version.Manifest.Type
	if extensionType == "" {
		extensionType = extensions.TypePlugin
	}
	return extensions.Extension{
		ID:                  member.ExtensionID,
		Name:                name,
		Version:             version.Version,
		Type:                extensionType,
		Status:              extensions.StatusEnabled,
		Source:              base.Source,
		IsSystem:            base.IsSystem,
		IsDeletable:         base.IsDeletable,
		Manifest:            version.Manifest,
		CapabilityGrants:    extensionmanifest.CapabilityGrants(version.Manifest),
		PackageDigest:       version.PackageDigest,
		AdminFrontendDigest: version.AdminFrontendDigest,
		PackagePath:         version.PackagePath,
		ActiveVersionID:     version.ID,
		InstalledAt:         version.InstalledAt,
		UpdatedAt:           base.UpdatedAt,
	}
}

func (a *ManagerPluginRuntimeFullSetApplier) buildPluginRuntimeFullSetPlan(
	publicationRevision int64,
	desired []pluginRuntimeFullSetDesired,
) (*pluginRuntimeFullSetPlan, error) {
	m := a.manager
	m.mu.RLock()
	defer m.mu.RUnlock()

	plan := &pluginRuntimeFullSetPlan{
		publicationRevision: publicationRevision,
		desired:             make([]pluginRuntimeFullSetMemberPlan, 0, len(desired)),
	}
	desiredIDs := make(map[string]struct{}, len(desired))
	for _, item := range desired {
		desiredIDs[item.member.ExtensionID] = struct{}{}
		memberPlan := pluginRuntimeFullSetMemberPlan{
			member:    item.member,
			extension: item.extension,
		}
		activeID := m.activeInstances[item.member.ExtensionID]
		if activeID != "" {
			identity := RuntimeInstanceIdentity{ExtensionID: item.member.ExtensionID, InstanceID: activeID}
			instance, err := m.runtimeInstanceLocked(identity)
			if err != nil {
				return nil, err
			}
			memberPlan.old = identity
			memberPlan.hadOld = true
			if pluginRuntimeActiveMatchesDesired(instance, item.extension, item.member) {
				memberPlan.reuse = true
			}
		}
		plan.desired = append(plan.desired, memberPlan)
	}

	activeIDs := make([]string, 0, len(m.activeInstances))
	for extensionID := range m.activeInstances {
		activeIDs = append(activeIDs, extensionID)
	}
	sort.Strings(activeIDs)
	for _, extensionID := range activeIDs {
		if _, keep := desiredIDs[extensionID]; keep {
			continue
		}
		instanceID := m.activeInstances[extensionID]
		identity := RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: instanceID}
		instance, err := m.runtimeInstanceLocked(identity)
		if err != nil {
			return nil, err
		}
		plan.removals = append(plan.removals, pluginRuntimeFullSetRemoval{
			identity:  identity,
			extension: instance.extension,
		})
	}
	return plan, nil
}

func pluginRuntimeActiveMatchesDesired(
	instance *managedRuntimeInstance,
	desired extensions.Extension,
	member extensions.PluginRuntimeMember,
) bool {
	if instance == nil {
		return false
	}
	if instance.extension.ID != member.ExtensionID ||
		instance.extension.ActiveVersionID != member.ExtensionVersionID ||
		instance.extensionVersion != member.ExtensionVersion ||
		instance.artifactDigest != member.PackageDigest ||
		instance.extension.PackageDigest != member.PackageDigest ||
		instance.extension.Version != member.ExtensionVersion {
		return false
	}
	wantDigest, err := protocolRuntimeManifestDigest(desired.Manifest)
	if err != nil {
		return false
	}
	haveDigest, err := protocolRuntimeManifestDigest(instance.extension.Manifest)
	if err != nil {
		return false
	}
	return wantDigest == haveDigest
}

// recheckReusedPluginRuntimes never drains or resumes an unchanged instance.
// A failed health/readiness check converts only that member into a replacement
// so ambiguous durable retries cannot attest a dead exact process.
func (a *ManagerPluginRuntimeFullSetApplier) recheckReusedPluginRuntimes(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	for index := range plan.desired {
		item := &plan.desired[index]
		if !item.reuse {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := a.healthPluginRuntimeFullSetIdentity(ctx, item.extension, item.old)
		if err != nil || snapshot.Identity != item.old {
			item.reuse = false
			continue
		}
		a.manager.mu.RLock()
		instance, inspectErr := a.manager.runtimeInstanceLocked(item.old)
		activeID := a.manager.activeInstances[item.old.ExtensionID]
		var admission RuntimeAdmissionSnapshot
		transitioning := false
		if inspectErr == nil {
			admission = instance.gate.Snapshot()
			transitioning = instance.transitioning
		}
		a.manager.mu.RUnlock()
		if inspectErr != nil || activeID != item.old.InstanceID || transitioning || admission.Draining || admission.Forced {
			item.reuse = false
		}
	}
	return nil
}

func (a *ManagerPluginRuntimeFullSetApplier) revalidatePublishedPluginRuntimeFullSet(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	if plan == nil || plan.protocolSetLease == nil {
		return ErrPluginRuntimeFullSetConflict
	}
	for _, item := range plan.desired {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity := item.finalIdentity()
		snapshot, err := a.healthPluginRuntimeFullSetIdentity(ctx, item.extension, identity)
		if err != nil {
			return fmt.Errorf("revalidate published runtime %s/%s: %w", identity.ExtensionID, identity.InstanceID, err)
		}
		if err := validatePluginRuntimeFullSetProtocolSnapshot(snapshot, item.extension, identity, ProtocolRuntimePublished); err != nil {
			return fmt.Errorf("revalidate published runtime %s/%s: %w", identity.ExtensionID, identity.InstanceID, err)
		}
	}
	if err := plan.protocolSetLease.Validate(ctx, plan.finalIdentities()); err != nil {
		return fmt.Errorf("validate final published runtime set: %w", err)
	}
	return nil
}

func (a *ManagerPluginRuntimeFullSetApplier) healthPluginRuntimeFullSetIdentity(
	ctx context.Context,
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
) (ProtocolRuntimeInstanceSnapshot, error) {
	return a.manager.HealthRuntimeInstance(ctx, identity)
}

func validatePluginRuntimeFullSetProtocolSnapshot(
	snapshot ProtocolRuntimeInstanceSnapshot,
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
	wantState ProtocolRuntimeInstanceState,
) error {
	return validateManagedProtocolSnapshot(snapshot, extension, identity, wantState)
}

// pluginRuntimeFullSetVisible verifies the normal no-op replay without writing
// Manager status, reopening gates, or incrementing Registry revisions.
func (a *ManagerPluginRuntimeFullSetApplier) pluginRuntimeFullSetVisible(ctx context.Context, plan *pluginRuntimeFullSetPlan) bool {
	if plan == nil || !plan.needsOnlyCommit() {
		return false
	}
	m := a.manager
	m.mu.RLock()
	for _, item := range plan.desired {
		instance, err := m.runtimeInstanceLocked(item.old)
		if err != nil || instance.transitioning || m.activeInstances[item.old.ExtensionID] != item.old.InstanceID ||
			!pluginRuntimeActiveMatchesDesired(instance, item.extension, item.member) {
			m.mu.RUnlock()
			return false
		}
		admission := instance.gate.Snapshot()
		if admission.Draining || admission.Forced {
			m.mu.RUnlock()
			return false
		}
	}
	if len(m.activeInstances) != len(plan.desired) {
		m.mu.RUnlock()
		return false
	}
	m.mu.RUnlock()
	starter, ok := m.starter.(StagedRuntimeSetStarter)
	return ok && starter.RuntimeInstanceSetVisible(ctx, plan.finalIdentities()) && pluginRuntimeHookSetMatchesPlan(m.hooks, plan)
}

func (a *ManagerPluginRuntimeFullSetApplier) stageAndHealthPluginRuntimeFullSet(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	for index := range plan.desired {
		item := &plan.desired[index]
		if item.reuse {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		staged, err := a.manager.stageRuntimeInstanceRuntimeSetLocked(ctx, item.extension)
		if err != nil {
			return fmt.Errorf("stage %s@%s: %w", item.member.ExtensionID, item.member.ExtensionVersion, err)
		}
		item.candidate = staged.Identity
		item.staged = true
		if _, err := a.manager.HealthRuntimeInstance(ctx, staged.Identity); err != nil {
			return fmt.Errorf("health %s@%s: %w", item.member.ExtensionID, item.member.ExtensionVersion, err)
		}
	}
	return nil
}

func (a *ManagerPluginRuntimeFullSetApplier) drainPluginRuntimeFullSet(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	timeout := a.drainTimeout
	if timeout <= 0 {
		timeout = RecommendedPluginRuntimeFullSetDrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// 先 drain 全部变更/移除的旧实例，再发布任何替换，保证不存在可调用的旧+新混合集。
	for index := range plan.desired {
		item := &plan.desired[index]
		if item.reuse || !item.hadOld {
			continue
		}
		if err := drainCtx.Err(); err != nil {
			return err
		}
		if _, err := a.manager.beginDrainRuntimeSetLocked(drainCtx, item.old); err != nil {
			return fmt.Errorf("drain old %s/%s: %w", item.old.ExtensionID, item.old.InstanceID, err)
		}
		item.drainStarted = true
		if err := a.manager.WaitDrain(drainCtx, item.old); err != nil {
			return fmt.Errorf("wait drain old %s/%s: %w", item.old.ExtensionID, item.old.InstanceID, err)
		}
		item.drainCompleted = true
	}
	for index := range plan.removals {
		removal := &plan.removals[index]
		if err := drainCtx.Err(); err != nil {
			return err
		}
		if _, err := a.manager.beginDrainRuntimeSetLocked(drainCtx, removal.identity); err != nil {
			return fmt.Errorf("drain removed %s/%s: %w", removal.identity.ExtensionID, removal.identity.InstanceID, err)
		}
		removal.drainStarted = true
		if err := a.manager.WaitDrain(drainCtx, removal.identity); err != nil {
			return fmt.Errorf("wait drain removed %s/%s: %w", removal.identity.ExtensionID, removal.identity.InstanceID, err)
		}
		removal.drainCompleted = true
	}
	return nil
}

// publishPluginRuntimeFullSetProtocols swaps ProtocolStarter and its Protocol
// V2 ServiceRegistry once for the complete desired set. Manager pointers,
// gates and HookBus remain on the old complete set until commit.
func (a *ManagerPluginRuntimeFullSetApplier) publishPluginRuntimeFullSetProtocols(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	starter, ok := a.manager.starter.(StagedRuntimeSetStarter)
	if !ok {
		return ErrProtocolInstanceUnsupported
	}
	type transitioningRuntime struct {
		identity RuntimeInstanceIdentity
		instance *managedRuntimeInstance
	}
	transitioning := make([]transitioningRuntime, 0, len(plan.desired))
	clearTransitioning := func() {
		m := a.manager
		m.mu.Lock()
		for _, runtime := range transitioning {
			if current := m.runtimeInstances[runtime.identity.ExtensionID][runtime.identity.InstanceID]; current == runtime.instance {
				current.transitioning = false
			}
		}
		m.mu.Unlock()
	}
	defer clearTransitioning()
	for index := range plan.desired {
		item := &plan.desired[index]
		if item.reuse {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m := a.manager
		m.mu.Lock()
		instance, err := m.runtimeInstanceLocked(item.candidate)
		if err != nil || instance.transitioning || m.activeInstances[item.candidate.ExtensionID] == item.candidate.InstanceID {
			m.mu.Unlock()
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: candidate runtime pointer drifted", ErrPluginRuntimeFullSetConflict)
		}
		admission := instance.gate.BeginDrain()
		if admission.Forced || admission.ActiveTotal != 0 {
			m.mu.Unlock()
			return fmt.Errorf("%w: candidate %s/%s is not idle", ErrRuntimeInstanceBusy, item.candidate.ExtensionID, item.candidate.InstanceID)
		}
		instance.transitioning = true
		transitioning = append(transitioning, transitioningRuntime{identity: item.candidate, instance: instance})
		item.publishAttempted = true
		m.mu.Unlock()
	}

	published, protocolSetLease, publishErr := starter.PublishInstanceSet(ctx, plan.finalIdentities())
	if publishErr != nil {
		// Set publication is atomic on error, so candidates remain discardable.
		for index := range plan.desired {
			plan.desired[index].publishAttempted = false
		}
		return fmt.Errorf("publish complete protocol runtime set: %w", publishErr)
	}
	// From this point rollback must restore the previous complete set even if
	// the starter returned malformed evidence.
	plan.protocolSetPublished = true
	plan.protocolSetLease = protocolSetLease
	byIdentity := make(map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot, len(published))
	for _, snapshot := range published {
		byIdentity[snapshot.Identity] = snapshot
	}
	for index := range plan.desired {
		item := &plan.desired[index]
		identity := item.finalIdentity()
		snapshot, exists := byIdentity[identity]
		if !exists {
			return fmt.Errorf("%w: published protocol set omitted %s/%s", ErrPluginRuntimeFullSetConflict, identity.ExtensionID, identity.InstanceID)
		}
		if err := validatePluginRuntimeFullSetProtocolSnapshot(snapshot, item.extension, identity, ProtocolRuntimePublished); err != nil {
			return fmt.Errorf("publish protocol %s/%s: %w", identity.ExtensionID, identity.InstanceID, err)
		}
		if !item.reuse {
			item.protocolPublished = true
		}
	}
	return nil
}

func (a *ManagerPluginRuntimeFullSetApplier) discardPluginRuntimeFullSetCandidates(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) {
	if plan == nil {
		return
	}
	cleanupCtx, cancel := pluginRuntimeFullSetCleanupContext(ctx)
	defer cancel()
	for index := range plan.desired {
		item := &plan.desired[index]
		if !item.staged || item.candidate.InstanceID == "" || item.publishAttempted {
			continue
		}
		if err := a.manager.removeManagedProtocolRuntimeSetLocked(cleanupCtx, item.candidate, true); err != nil {
			// 丢弃失败时保留候选，避免误杀已发布路径。
			continue
		}
		item.staged = false
		item.candidate = RuntimeInstanceIdentity{}
	}
}

func (a *ManagerPluginRuntimeFullSetApplier) rollbackPluginRuntimeFullSet(
	ctx context.Context,
	plan *pluginRuntimeFullSetPlan,
) error {
	if plan == nil {
		return nil
	}
	cleanupCtx, cancel := pluginRuntimeFullSetCleanupContext(ctx)
	defer cancel()
	var rollbackErrs []error

	protocolRestored := !plan.protocolSetPublished
	if plan.protocolSetPublished {
		previous := plan.previousIdentities()
		var published []ProtocolRuntimeInstanceSnapshot
		var publishErr error
		if plan.protocolSetLease == nil {
			publishErr = ErrPluginRuntimeFullSetConflict
		} else {
			published, publishErr = plan.protocolSetLease.Restore(cleanupCtx, previous)
		}
		if publishErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore complete protocol runtime set: %w", publishErr))
		} else {
			byIdentity := make(map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot, len(published))
			for _, snapshot := range published {
				byIdentity[snapshot.Identity] = snapshot
			}
			for _, identity := range previous {
				extension, inspectErr := a.manager.managedRuntimeExtension(identity)
				if inspectErr != nil {
					publishErr = errors.Join(publishErr, fmt.Errorf("inspect rollback runtime %s: %w", identity.ExtensionID, inspectErr))
					continue
				}
				snapshot, exists := byIdentity[identity]
				if !exists {
					publishErr = errors.Join(publishErr, fmt.Errorf("%w: rollback set omitted %s/%s", ErrPluginRuntimeFullSetConflict, identity.ExtensionID, identity.InstanceID))
					continue
				}
				publishErr = errors.Join(publishErr, validatePluginRuntimeFullSetProtocolSnapshot(snapshot, extension, identity, ProtocolRuntimePublished))
			}
			if publishErr != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("validate restored protocol runtime set: %w", publishErr))
			} else {
				protocolRestored = true
				plan.protocolSetPublished = false
			}
		}
	}

	// Reopen old admission only after the complete old protocol/service graph is
	// proven restored. Otherwise the node stays fail-closed for forward repair.
	if protocolRestored {
		if err := a.resumePluginRuntimeFullSetPreviousAtomically(plan); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("resume previous runtime set: %w", err))
		}
	}
	plan.releaseProtocolSetLease()

	// Candidates are stopped only after the whole old protocol/service graph
	// was restored. A failed restore preserves every candidate for recovery.
	if protocolRestored {
		for index := range plan.desired {
			item := &plan.desired[index]
			if item.reuse || !item.staged || item.candidate.InstanceID == "" {
				continue
			}
			if item.publishAttempted {
				if err := a.stopRetainedRuntimeInstance(cleanupCtx, item.candidate); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("stop rolled-back candidate %s/%s: %w", item.candidate.ExtensionID, item.candidate.InstanceID, err))
					continue
				}
			} else if err := a.manager.removeManagedProtocolRuntimeSetLocked(cleanupCtx, item.candidate, true); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("discard rolled-back candidate %s/%s: %w", item.candidate.ExtensionID, item.candidate.InstanceID, err))
				continue
			}
			item.protocolPublished = false
			item.publishAttempted = false
			item.staged = false
			item.candidate = RuntimeInstanceIdentity{}
		}
	}

	a.discardPluginRuntimeFullSetCandidates(cleanupCtx, plan)
	return errors.Join(rollbackErrs...)
}

func (a *ManagerPluginRuntimeFullSetApplier) cleanupPluginRuntimeFullSet(plan *pluginRuntimeFullSetPlan) {
	if plan == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, item := range plan.desired {
		if item.reuse || !item.hadOld || item.old.InstanceID == "" {
			continue
		}
		// 旧替换实例已 retired；失败则安全保留。
		_ = a.stopRetainedRuntimeInstance(cleanupCtx, item.old)
	}
	for _, removal := range plan.removals {
		// Hook/Provider/Command/Admin registrations were removed inside the
		// atomic desired-set commit, before applied evidence can be returned.
		if a.manager.resilience != nil {
			a.manager.resilience.remove(removal.identity.ExtensionID)
		}
		_ = a.stopRetainedRuntimeInstance(cleanupCtx, removal.identity)
	}
}

func (a *ManagerPluginRuntimeFullSetApplier) stopRetainedRuntimeInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
) error {
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return nil
	}
	if _, err := a.manager.InspectRuntimeInstance(identity); errors.Is(err, ErrRuntimeInstanceNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := a.manager.beginDrainRuntimeSetLocked(ctx, identity); err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
		// 已 draining 时 BeginDrain 仍成功；其它错误保留实例。
		snapshot, inspectErr := a.manager.InspectRuntimeInstance(identity)
		if inspectErr != nil {
			return inspectErr
		}
		if !snapshot.Admission.Draining {
			return err
		}
	}
	if err := a.manager.WaitDrain(ctx, identity); err != nil {
		return err
	}
	if err := a.manager.removeManagedProtocolRuntimeSetLocked(ctx, identity, false); err != nil {
		return err
	}
	return nil
}

func pluginRuntimeFullSetCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx != nil && ctx.Err() == nil {
		return context.WithTimeout(ctx, 30*time.Second)
	}
	return context.WithTimeout(context.Background(), 30*time.Second)
}
