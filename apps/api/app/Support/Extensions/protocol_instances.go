package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/go-plugin"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

var (
	ErrProtocolInstanceUnsupported = errors.New("protocol runtime instance operation is unsupported")
	ErrProtocolInstancePublished   = errors.New("protocol runtime instance has been published")
	ErrProtocolInstanceUnhealthy   = errors.New("protocol runtime instance is unhealthy")
	ErrProtocolInstanceNotReady    = errors.New("protocol runtime instance is not ready")
)

type ProtocolRuntimeInstanceState string

const (
	ProtocolRuntimeStaged    ProtocolRuntimeInstanceState = "staged"
	ProtocolRuntimePublished ProtocolRuntimeInstanceState = "published"
	ProtocolRuntimeRetained  ProtocolRuntimeInstanceState = "retained"
)

// ProtocolRuntimeInstanceSnapshot 只暴露宿主可安全检查的 exact process 元数据。
type ProtocolRuntimeInstanceSnapshot struct {
	Identity         RuntimeInstanceIdentity
	ExtensionVersion string
	ArtifactDigest   string
	ManifestDigest   string
	ProtocolVersion  int
	Target           RouteTarget
	State            ProtocolRuntimeInstanceState
	Healthy          bool
	Ready            bool
	ReadinessChecked bool
	StartedAt        time.Time
}

// StagedRuntimeStarter 是 lifecycle coordinator 使用的 V2 exact-process 最小契约。
type StagedRuntimeStarter interface {
	StartInstance(context.Context, extensions.Extension) (RouteTarget, error)
	InspectInstance(RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error)
	HealthInstance(context.Context, RuntimeInstanceIdentity) (PluginHealth, error)
	PublishInstance(context.Context, RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error)
	RunLifecycleInstance(context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation) (LifecycleRunResult, error)
	StopInstance(context.Context, RuntimeInstanceIdentity) error
	DiscardInstance(context.Context, RuntimeInstanceIdentity) error
}

// StagedRuntimeSetStarter publishes the complete desired V2 process set at
// one ProtocolStarter and ServiceRegistry linearization boundary.
type StagedRuntimeSetStarter interface {
	StagedRuntimeStarter
	PublishInstanceSet(context.Context, []RuntimeInstanceIdentity) ([]ProtocolRuntimeInstanceSnapshot, *ProtocolRuntimeSetLease, error)
	RuntimeInstanceSetVisible(context.Context, []RuntimeInstanceIdentity) bool
}

// ProtocolRuntimeSetLease keeps ProtocolStarter membership and the HostAPI
// service writer fence continuous across an aggregate Manager commit/rollback.
type ProtocolRuntimeSetLease struct {
	mu       sync.Mutex
	closed   bool
	release  func()
	restore  func(context.Context, []RuntimeInstanceIdentity) ([]ProtocolRuntimeInstanceSnapshot, error)
	validate func(context.Context, []RuntimeInstanceIdentity) error
}

// Validate proves that the complete published process set is still the exact
// live set while the ProtocolStarter and ServiceRegistry writer fences remain
// held. It is the final candidate fence immediately before Manager commit.
func (l *ProtocolRuntimeSetLease) Validate(ctx context.Context, identities []RuntimeInstanceIdentity) error {
	if l == nil || ctx == nil {
		return ErrRuntimeAdmissionInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.validate == nil {
		return ErrProtocolInstanceNotReady
	}
	return l.validate(ctx, identities)
}

func (l *ProtocolRuntimeSetLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	release := l.release
	l.mu.Unlock()
	if release != nil {
		release()
	}
}

func (l *ProtocolRuntimeSetLease) Restore(
	ctx context.Context,
	identities []RuntimeInstanceIdentity,
) ([]ProtocolRuntimeInstanceSnapshot, error) {
	if l == nil || ctx == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.restore == nil {
		return nil, ErrProtocolInstanceNotReady
	}
	return l.restore(ctx, identities)
}

type protocolRuntimeInstance struct {
	identity         RuntimeInstanceIdentity
	extensionVersion string
	artifactDigest   string
	manifestDigest   string
	protocolVersion  int
	target           RouteTarget
	client           *plugin.Client
	protocol         PluginProtocol
	registrations    []hostapi.ServiceRegistration
	serviceRuntime   hostapi.ServiceRuntimePublication
	databaseLease    *protocolDatabaseLease
	healthy          bool
	ready            bool
	readinessChecked bool
	published        bool
	everPublished    bool
	startedAt        time.Time
}

// Start 内部完成 stage+publish。需要跨 health/drain 边界编排时，调用方应使用
// StartInstance + PublishInstance。
func (s *ProtocolStarter) Start(ctx context.Context, extension extensions.Extension) (RouteTarget, error) {
	if s == nil {
		return RouteTarget{}, extensions.ErrRuntimeUnavailable
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RouteTarget{}, err
	}
	defer unlockSet()
	unlock, err := s.lockExtensionLifecycleContext(ctx, extension.ID)
	if err != nil {
		return RouteTarget{}, err
	}
	defer unlock()
	return s.startProtocolInstanceLocked(ctx, extension, true)
}

// StartInstance 启动并验证一个未发布的 V2 进程；不会改变活动指针或 Registry。
func (s *ProtocolStarter) StartInstance(ctx context.Context, extension extensions.Extension) (RouteTarget, error) {
	if s == nil {
		return RouteTarget{}, extensions.ErrRuntimeUnavailable
	}
	if manifestProtocolVersion(extension) != 2 {
		return RouteTarget{}, ErrProtocolInstanceUnsupported
	}
	unlock, err := s.lockExtensionLifecycleContext(ctx, extension.ID)
	if err != nil {
		return RouteTarget{}, err
	}
	defer unlock()
	return s.startProtocolInstanceLocked(ctx, extension, false)
}

func (s *ProtocolStarter) InspectInstance(identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	if s == nil {
		return ProtocolRuntimeInstanceSnapshot{}, extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	instance := s.runtimeInstanceLocked(identity)
	if instance == nil || instance.client == nil || instance.client.Exited() {
		return ProtocolRuntimeInstanceSnapshot{}, protocolInstanceNotFound(identity)
	}
	return protocolRuntimeSnapshot(instance), nil
}

// HealthInstance 对 exact V2 进程重新执行 health+readiness，不读取活动回退实例。
func (s *ProtocolStarter) HealthInstance(ctx context.Context, identity RuntimeInstanceIdentity) (PluginHealth, error) {
	if s == nil {
		return PluginHealth{}, extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return PluginHealth{}, err
	}
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return PluginHealth{}, err
	}
	defer unlock()
	instance := s.protocolInstance(identity)
	if instance == nil {
		return PluginHealth{}, protocolInstanceNotFound(identity)
	}
	if instance.protocolVersion != 2 {
		return PluginHealth{}, ErrProtocolInstanceUnsupported
	}
	return s.healthProtocolInstanceLocked(ctx, instance)
}

// PublishInstance 健康检查后原子替换 V2 Service Registry，再切换活动 transport 指针。
// 调用方在此边界外负责关闭 Manager admission，避免跨 Registry 的新 ordinary 调用。
func (s *ProtocolStarter) PublishInstance(ctx context.Context, identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	if s == nil {
		return ProtocolRuntimeInstanceSnapshot{}, extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	defer unlockSet()
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	instance := s.protocolInstance(identity)
	if instance == nil {
		return ProtocolRuntimeInstanceSnapshot{}, protocolInstanceNotFound(identity)
	}
	if instance.protocolVersion != 2 {
		return ProtocolRuntimeInstanceSnapshot{}, ErrProtocolInstanceUnsupported
	}
	return s.publishProtocolInstanceLocked(ctx, identity, true)
}

// PublishInstanceSet validates and publishes exactly identities. Service
// discovery/dependency readers and ProtocolStarter pointer readers can observe
// only the complete previous set or the complete desired set.
func (s *ProtocolStarter) PublishInstanceSet(
	ctx context.Context,
	identities []RuntimeInstanceIdentity,
) ([]ProtocolRuntimeInstanceSnapshot, *ProtocolRuntimeSetLease, error) {
	if s == nil {
		return nil, nil, extensions.ErrRuntimeUnavailable
	}
	if ctx == nil {
		return nil, nil, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return nil, nil, err
	}

	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return nil, nil, err
	}
	releaseSet := true
	defer func() {
		if releaseSet {
			unlockSet()
		}
	}()

	// Lock every desired and currently active owner in stable order. The set
	// mutex prevents active membership drift while locks are acquired.
	s.mu.Lock()
	extensionIDs := make(map[string]struct{}, len(desired)+len(s.activeRuntimeInstances))
	for extensionID := range s.activeRuntimeInstances {
		extensionIDs[extensionID] = struct{}{}
	}
	s.mu.Unlock()
	for _, identity := range desired {
		extensionIDs[identity.ExtensionID] = struct{}{}
	}
	lockedIDs := make([]string, 0, len(extensionIDs))
	for extensionID := range extensionIDs {
		lockedIDs = append(lockedIDs, extensionID)
	}
	sort.Strings(lockedIDs)
	unlockers := make([]func(), 0, len(lockedIDs))
	for _, extensionID := range lockedIDs {
		unlock, lockErr := s.lockExtensionLifecycleContext(ctx, extensionID)
		if lockErr != nil {
			for index := len(unlockers) - 1; index >= 0; index-- {
				unlockers[index]()
			}
			return nil, nil, lockErr
		}
		unlockers = append(unlockers, unlock)
	}
	defer func() {
		for index := len(unlockers) - 1; index >= 0; index-- {
			unlockers[index]()
		}
	}()

	s.mu.Lock()
	instances := make([]*protocolRuntimeInstance, 0, len(desired))
	publications := make([]hostapi.ServiceRuntimePublication, 0, len(desired))
	for extensionID, instanceID := range s.activeRuntimeInstances {
		instance := s.runtimeInstances[extensionID][instanceID]
		if instance == nil || instance.protocolVersion != 2 {
			s.mu.Unlock()
			return nil, nil, ErrProtocolInstanceUnsupported
		}
	}
	for _, identity := range desired {
		instance := s.runtimeInstanceLocked(identity)
		if instance == nil {
			s.mu.Unlock()
			return nil, nil, protocolInstanceNotFound(identity)
		}
		if instance.protocolVersion != 2 {
			s.mu.Unlock()
			return nil, nil, ErrProtocolInstanceUnsupported
		}
		if instance.client == nil || instance.client.Exited() || !instance.healthy {
			s.mu.Unlock()
			return nil, nil, ErrProtocolInstanceUnhealthy
		}
		if !instance.ready || !instance.readinessChecked {
			s.mu.Unlock()
			return nil, nil, ErrProtocolInstanceNotReady
		}
		instances = append(instances, instance)
		publications = append(publications, instance.serviceRuntime)
	}
	s.mu.Unlock()

	registry := protocolV2ServiceRegistryFor(s.hostAPI)
	var serviceSet *hostapi.ServiceRuntimeSetTransaction
	if registry == nil {
		for _, instance := range instances {
			if len(instance.registrations) > 0 {
				return nil, nil, fmt.Errorf("protocol v2 service registry is not configured")
			}
		}
	} else {
		serviceSet, err = registry.PrepareProtocolV2ServiceRuntimeSet(publications)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare protocol v2 service runtime set: %w", err)
		}
		defer serviceSet.Abort()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// All ProtocolStarter set writers share the set-transition barrier. Keep s.mu held across
	// the ServiceRegistry linearization point so pointer readers cannot observe
	// a service graph whose complete active transport set is still old.
	s.mu.Lock()
	type publicationState struct {
		published     bool
		everPublished bool
	}
	previousStates := make(map[*protocolRuntimeInstance]publicationState, len(instances)+len(s.activeRuntimeInstances))
	rememberState := func(instance *protocolRuntimeInstance) {
		if instance == nil {
			return
		}
		if _, exists := previousStates[instance]; !exists {
			previousStates[instance] = publicationState{published: instance.published, everPublished: instance.everPublished}
		}
	}
	previousActive := s.activeRuntimeInstances
	previousClients := s.clients
	previousProtocols := s.protocols
	desiredByExtension := make(map[string]*protocolRuntimeInstance, len(instances))
	for _, instance := range instances {
		rememberState(instance)
		desiredByExtension[instance.identity.ExtensionID] = instance
	}
	for extensionID, instanceID := range s.activeRuntimeInstances {
		if desiredInstance := desiredByExtension[extensionID]; desiredInstance != nil && desiredInstance.identity.InstanceID == instanceID {
			continue
		}
		if previous := s.runtimeInstances[extensionID][instanceID]; previous != nil {
			rememberState(previous)
			previous.published = false
		}
	}
	nextActive := make(map[string]string, len(instances))
	nextClients := make(map[string]*plugin.Client, len(instances))
	nextProtocols := make(map[string]PluginProtocol, len(instances))
	snapshots := make([]ProtocolRuntimeInstanceSnapshot, 0, len(instances))
	for _, instance := range instances {
		if previousID := s.activeRuntimeInstances[instance.identity.ExtensionID]; previousID != "" && previousID != instance.identity.InstanceID {
			if previous := s.runtimeInstances[instance.identity.ExtensionID][previousID]; previous != nil {
				rememberState(previous)
				previous.published = false
			}
		}
		instance.published = true
		instance.everPublished = true
		nextActive[instance.identity.ExtensionID] = instance.identity.InstanceID
		nextClients[instance.identity.ExtensionID] = instance.client
		nextProtocols[instance.identity.ExtensionID] = instance.protocol
		snapshots = append(snapshots, protocolRuntimeSnapshot(instance))
	}
	s.activeRuntimeInstances = nextActive
	s.clients = nextClients
	s.protocols = nextProtocols
	var serviceLease *hostapi.ServiceRuntimeSetLease
	if serviceSet != nil {
		serviceLease, err = serviceSet.CommitAndAcquireContext(ctx)
		if err != nil {
			s.activeRuntimeInstances = previousActive
			s.clients = previousClients
			s.protocols = previousProtocols
			for instance, state := range previousStates {
				instance.published = state.published
				instance.everPublished = state.everPublished
			}
			s.mu.Unlock()
			return nil, nil, fmt.Errorf("publish protocol v2 service runtime set: %w", err)
		}
	}
	s.mu.Unlock()
	releaseSet = false
	lease := &ProtocolRuntimeSetLease{
		release: func() {
			serviceLease.Release()
			unlockSet()
		},
	}
	lease.restore = func(restoreCtx context.Context, restoreIdentities []RuntimeInstanceIdentity) ([]ProtocolRuntimeInstanceSnapshot, error) {
		return s.restoreRuntimeInstanceSetUnderLease(restoreCtx, serviceLease, restoreIdentities)
	}
	lease.validate = func(validateCtx context.Context, validateIdentities []RuntimeInstanceIdentity) error {
		return s.validateRuntimeInstanceSetUnderLease(validateCtx, validateIdentities)
	}
	return snapshots, lease, nil
}

func (s *ProtocolStarter) validateRuntimeInstanceSetUnderLease(
	ctx context.Context,
	identities []RuntimeInstanceIdentity,
) error {
	if s == nil || ctx == nil {
		return ErrRuntimeAdmissionInvalid
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activeRuntimeInstances) != len(desired) {
		return ErrProtocolInstanceNotReady
	}
	for _, identity := range desired {
		instance := s.runtimeInstanceLocked(identity)
		if instance == nil || s.activeRuntimeInstances[identity.ExtensionID] != identity.InstanceID ||
			!instance.published || instance.client == nil || instance.client.Exited() ||
			!instance.healthy || !instance.ready ||
			(instance.protocolVersion == 2 && !instance.readinessChecked) {
			return fmt.Errorf("%w: %s/%s", ErrProtocolInstanceNotReady, identity.ExtensionID, identity.InstanceID)
		}
	}
	return nil
}

// restoreRuntimeInstanceSetUnderLease assumes the set-transition token and the
// ServiceRegistry writer fence returned by PublishInstanceSet are still held.
func (s *ProtocolStarter) restoreRuntimeInstanceSetUnderLease(
	ctx context.Context,
	serviceLease *hostapi.ServiceRuntimeSetLease,
	identities []RuntimeInstanceIdentity,
) ([]ProtocolRuntimeInstanceSnapshot, error) {
	if s == nil || ctx == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	extensionIDs := make(map[string]struct{}, len(desired)+len(s.activeRuntimeInstances))
	for extensionID := range s.activeRuntimeInstances {
		extensionIDs[extensionID] = struct{}{}
	}
	s.mu.Unlock()
	for _, identity := range desired {
		extensionIDs[identity.ExtensionID] = struct{}{}
	}
	lockedIDs := make([]string, 0, len(extensionIDs))
	for extensionID := range extensionIDs {
		lockedIDs = append(lockedIDs, extensionID)
	}
	sort.Strings(lockedIDs)
	unlockers := make([]func(), 0, len(lockedIDs))
	for _, extensionID := range lockedIDs {
		unlock, lockErr := s.lockExtensionLifecycleContext(ctx, extensionID)
		if lockErr != nil {
			for index := len(unlockers) - 1; index >= 0; index-- {
				unlockers[index]()
			}
			return nil, lockErr
		}
		unlockers = append(unlockers, unlock)
	}
	defer func() {
		for index := len(unlockers) - 1; index >= 0; index-- {
			unlockers[index]()
		}
	}()

	s.mu.Lock()
	instances := make([]*protocolRuntimeInstance, 0, len(desired))
	publications := make([]hostapi.ServiceRuntimePublication, 0, len(desired))
	for _, identity := range desired {
		instance := s.runtimeInstanceLocked(identity)
		if instance == nil {
			s.mu.Unlock()
			return nil, protocolInstanceNotFound(identity)
		}
		if instance.protocolVersion != 2 {
			s.mu.Unlock()
			return nil, ErrProtocolInstanceUnsupported
		}
		instances = append(instances, instance)
		publications = append(publications, instance.serviceRuntime)
	}
	s.mu.Unlock()
	for _, instance := range instances {
		_, err := s.healthProtocolInstanceLocked(ctx, instance)
		if err != nil {
			return nil, err
		}
	}
	if serviceLease == nil {
		for _, publication := range publications {
			if len(publication.Registrations) > 0 {
				return nil, fmt.Errorf("protocol v2 service registry lease is not configured")
			}
		}
	}

	s.mu.Lock()
	type publicationState struct {
		published     bool
		everPublished bool
	}
	previousStates := make(map[*protocolRuntimeInstance]publicationState, len(instances)+len(s.activeRuntimeInstances))
	rememberState := func(instance *protocolRuntimeInstance) {
		if instance == nil {
			return
		}
		if _, exists := previousStates[instance]; !exists {
			previousStates[instance] = publicationState{published: instance.published, everPublished: instance.everPublished}
		}
	}
	previousActive := s.activeRuntimeInstances
	previousClients := s.clients
	previousProtocols := s.protocols
	desiredByExtension := make(map[string]*protocolRuntimeInstance, len(instances))
	for _, instance := range instances {
		if instance.client == nil || instance.client.Exited() || !instance.healthy || !instance.ready {
			s.mu.Unlock()
			return nil, ErrProtocolInstanceNotReady
		}
		rememberState(instance)
		desiredByExtension[instance.identity.ExtensionID] = instance
	}
	for extensionID, instanceID := range s.activeRuntimeInstances {
		if desiredInstance := desiredByExtension[extensionID]; desiredInstance != nil && desiredInstance.identity.InstanceID == instanceID {
			continue
		}
		if previous := s.runtimeInstances[extensionID][instanceID]; previous != nil {
			rememberState(previous)
			previous.published = false
		}
	}
	nextActive := make(map[string]string, len(instances))
	nextClients := make(map[string]*plugin.Client, len(instances))
	nextProtocols := make(map[string]PluginProtocol, len(instances))
	snapshots := make([]ProtocolRuntimeInstanceSnapshot, 0, len(instances))
	for _, instance := range instances {
		instance.published = true
		instance.everPublished = true
		nextActive[instance.identity.ExtensionID] = instance.identity.InstanceID
		nextClients[instance.identity.ExtensionID] = instance.client
		nextProtocols[instance.identity.ExtensionID] = instance.protocol
		snapshots = append(snapshots, protocolRuntimeSnapshot(instance))
	}
	s.activeRuntimeInstances = nextActive
	s.clients = nextClients
	s.protocols = nextProtocols
	if serviceLease != nil {
		if err := serviceLease.ReplaceRuntimeSet(publications); err != nil {
			s.activeRuntimeInstances = previousActive
			s.clients = previousClients
			s.protocols = previousProtocols
			for instance, state := range previousStates {
				instance.published = state.published
				instance.everPublished = state.everPublished
			}
			s.mu.Unlock()
			return nil, fmt.Errorf("restore protocol v2 service runtime set: %w", err)
		}
	}
	s.mu.Unlock()
	return snapshots, nil
}

// RuntimeInstanceSetVisible checks both active process pointers and the exact
// Protocol V2 service/dependency snapshot for idempotent full-set replay.
func (s *ProtocolStarter) RuntimeInstanceSetVisible(ctx context.Context, identities []RuntimeInstanceIdentity) bool {
	if s == nil || ctx == nil {
		return false
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return false
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return false
	}
	defer unlockSet()

	s.mu.Lock()
	if len(s.activeRuntimeInstances) != len(desired) {
		s.mu.Unlock()
		return false
	}
	publications := make([]hostapi.ServiceRuntimePublication, 0, len(desired))
	for _, identity := range desired {
		instance := s.runtimeInstanceLocked(identity)
		if instance == nil || !instance.published || instance.protocolVersion != 2 ||
			s.activeRuntimeInstances[identity.ExtensionID] != identity.InstanceID {
			s.mu.Unlock()
			return false
		}
		publications = append(publications, instance.serviceRuntime)
	}
	s.mu.Unlock()
	registry := protocolV2ServiceRegistryFor(s.hostAPI)
	if registry == nil {
		for _, publication := range publications {
			if len(publication.Registrations) > 0 {
				return false
			}
		}
		return true
	}
	matches, err := registry.ProtocolV2ServiceRuntimeSetMatches(publications)
	return err == nil && matches
}

// StopInstance 停止任意 staged/published/retained V2 exact process。
func (s *ProtocolStarter) StopInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	if s == nil {
		return extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlockSet()
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return err
	}
	defer unlock()
	return s.stopProtocolInstanceLocked(identity, false)
}

// StopRetainedInstance stops one exact V2 process only after complete-set
// publication has made it unreachable. It cannot stop an active or staged candidate.
func (s *ProtocolStarter) StopRetainedInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	if s == nil {
		return extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlockSet()
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return err
	}
	defer unlock()

	s.mu.Lock()
	instance := s.runtimeInstanceLocked(identity)
	if instance == nil {
		s.mu.Unlock()
		return protocolInstanceNotFound(identity)
	}
	active := s.activeRuntimeInstances[identity.ExtensionID] == identity.InstanceID
	retained := instance.everPublished && !instance.published && !active
	supported := instance.protocolVersion == 2
	s.mu.Unlock()
	if !supported {
		return ErrProtocolInstanceUnsupported
	}
	if !retained {
		return fmt.Errorf("%w: %s/%s is not retained", ErrProtocolInstancePublished, identity.ExtensionID, identity.InstanceID)
	}
	return s.stopProtocolInstanceLocked(identity, true)
}

// DiscardInstance 只销毁从未发布的 V2 候选，不能误删活动或 rollback 保留实例。
func (s *ProtocolStarter) DiscardInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	if s == nil {
		return extensions.ErrRuntimeUnavailable
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return err
	}
	defer unlock()
	instance := s.protocolInstance(identity)
	if instance == nil {
		return protocolInstanceNotFound(identity)
	}
	if instance.protocolVersion != 2 {
		return ErrProtocolInstanceUnsupported
	}
	if instance.published || instance.everPublished {
		return fmt.Errorf("%w: %s/%s", ErrProtocolInstancePublished, identity.ExtensionID, identity.InstanceID)
	}
	s.removeProtocolInstanceLocked(identity)
	if instance.databaseLease != nil {
		instance.databaseLease.stopHeartbeat()
	}
	instance.client.Kill()
	return s.revokeProtocolDatabaseLease(instance)
}

func (s *ProtocolStarter) retainProtocolInstanceLocked(instance *protocolRuntimeInstance) error {
	if instance == nil || instance.client == nil || instance.protocol == nil {
		return ErrRuntimeAdmissionInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeInstances == nil {
		s.runtimeInstances = make(map[string]map[string]*protocolRuntimeInstance)
	}
	instances := s.runtimeInstances[instance.identity.ExtensionID]
	if instances == nil {
		instances = make(map[string]*protocolRuntimeInstance)
		s.runtimeInstances[instance.identity.ExtensionID] = instances
	}
	if instances[instance.identity.InstanceID] != nil {
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceConflict, instance.identity.ExtensionID, instance.identity.InstanceID)
	}
	instances[instance.identity.InstanceID] = instance
	return nil
}

// Caller holds the extension lifecycle lock.
func (s *ProtocolStarter) publishProtocolInstanceLocked(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	recheckHealth bool,
) (ProtocolRuntimeInstanceSnapshot, error) {
	if ctx == nil {
		return ProtocolRuntimeInstanceSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	instance := s.protocolInstance(identity)
	if instance == nil {
		return ProtocolRuntimeInstanceSnapshot{}, protocolInstanceNotFound(identity)
	}
	if instance.published && s.activeProtocolInstanceID(identity.ExtensionID) == identity.InstanceID {
		if recheckHealth {
			if _, err := s.healthProtocolInstanceLocked(ctx, instance); err != nil {
				return ProtocolRuntimeInstanceSnapshot{}, err
			}
		}
		// Registry compensation can remove this exact service set while the
		// retained process remains published. Idempotent publication must rebuild
		// it from the startup-frozen handshake instead of trusting process state.
		registry := protocolV2ServiceRegistryFor(s.hostAPI)
		if registry == nil {
			if len(instance.registrations) > 0 {
				return ProtocolRuntimeInstanceSnapshot{}, fmt.Errorf("protocol v2 service registry is not configured")
			}
		} else if err := registry.PublishProtocolV2ServiceRuntime(instance.serviceRuntime); err != nil {
			return ProtocolRuntimeInstanceSnapshot{}, fmt.Errorf("reconcile protocol v2 services: %w", err)
		}
		return s.InspectInstance(identity)
	}
	if instance.protocolVersion != 2 {
		return ProtocolRuntimeInstanceSnapshot{}, ErrProtocolInstanceUnsupported
	}
	if recheckHealth {
		if _, err := s.healthProtocolInstanceLocked(ctx, instance); err != nil {
			return ProtocolRuntimeInstanceSnapshot{}, err
		}
	}
	registry := protocolV2ServiceRegistryFor(s.hostAPI)
	if registry == nil {
		if len(instance.registrations) > 0 {
			return ProtocolRuntimeInstanceSnapshot{}, fmt.Errorf("protocol v2 service registry is not configured")
		}
	} else if err := registry.PublishProtocolV2ServiceRuntime(instance.serviceRuntime); err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, fmt.Errorf("register protocol v2 services: %w", err)
	}

	var previous *protocolRuntimeInstance
	s.mu.Lock()
	current := s.runtimeInstanceLocked(identity)
	if current != instance {
		s.mu.Unlock()
		return ProtocolRuntimeInstanceSnapshot{}, protocolInstanceNotFound(identity)
	}
	previousID := s.activeRuntimeInstances[identity.ExtensionID]
	if previousID != "" && previousID != identity.InstanceID {
		previous = s.runtimeInstances[identity.ExtensionID][previousID]
		if previous != nil {
			previous.published = false
		}
	}
	instance.published = true
	instance.everPublished = true
	s.activeRuntimeInstances[identity.ExtensionID] = identity.InstanceID
	s.clients[identity.ExtensionID] = instance.client
	s.protocols[identity.ExtensionID] = instance.protocol
	snapshot := protocolRuntimeSnapshot(instance)
	s.mu.Unlock()

	return snapshot, nil
}

// Caller holds the extension lifecycle lock.
func (s *ProtocolStarter) healthProtocolInstanceLocked(ctx context.Context, instance *protocolRuntimeInstance) (PluginHealth, error) {
	if ctx == nil {
		return PluginHealth{}, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return PluginHealth{}, err
	}
	if instance == nil || instance.client == nil || instance.client.Exited() {
		return PluginHealth{}, ErrProtocolInstanceUnhealthy
	}
	health, healthErr := instance.protocol.Health()
	ready := false
	readinessChecked := false
	var readinessErr error
	if healthErr == nil && health.OK {
		readiness, ok := instance.protocol.(interface{ Readiness(context.Context) error })
		if !ok {
			readinessErr = ErrProtocolInstanceUnsupported
		} else {
			readinessChecked = true
			readinessErr = readiness.Readiness(ctx)
			ready = readinessErr == nil
		}
	}
	healthy := healthErr == nil && health.OK
	s.mu.Lock()
	if current := s.runtimeInstanceLocked(instance.identity); current == instance {
		current.healthy = healthy
		current.ready = ready
		current.readinessChecked = readinessChecked
	}
	s.mu.Unlock()
	if !healthy {
		if healthErr != nil {
			return health, fmt.Errorf("%w: %v", ErrProtocolInstanceUnhealthy, healthErr)
		}
		return health, ErrProtocolInstanceUnhealthy
	}
	if !ready {
		if readinessErr != nil {
			return health, fmt.Errorf("%w: %v", ErrProtocolInstanceNotReady, readinessErr)
		}
		return health, ErrProtocolInstanceNotReady
	}
	return health, nil
}

// Caller holds the extension lifecycle lock.
func (s *ProtocolStarter) stopProtocolInstanceLocked(identity RuntimeInstanceIdentity, allowV1 bool) error {
	instance := s.protocolInstance(identity)
	if instance == nil {
		return protocolInstanceNotFound(identity)
	}
	if instance.protocolVersion != 2 && !allowV1 {
		return ErrProtocolInstanceUnsupported
	}
	s.removeProtocolInstanceLocked(identity)
	if instance.databaseLease != nil {
		instance.databaseLease.stopHeartbeat()
	}
	if instance.protocolVersion == 2 {
		s.unregisterProtocolV2Services(identity.ExtensionID, instance.protocol)
	} else if s.hostAPI != nil {
		s.hostAPI.UnregisterExtension(identity.ExtensionID)
	}
	instance.client.Kill()
	return s.revokeProtocolDatabaseLease(instance)
}

func (s *ProtocolStarter) revokeProtocolDatabaseLease(instance *protocolRuntimeInstance) error {
	if s == nil || instance == nil || instance.databaseLease == nil {
		return nil
	}
	timeout := s.databaseLeaseTimeout
	if timeout <= 0 {
		timeout = RecommendedProtocolDatabaseLeaseOperationTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return instance.databaseLease.revoke(ctx)
}

func (s *ProtocolStarter) protocolInstance(identity RuntimeInstanceIdentity) *protocolRuntimeInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeInstanceLocked(identity)
}

// Caller holds s.mu.
func (s *ProtocolStarter) runtimeInstanceLocked(identity RuntimeInstanceIdentity) *protocolRuntimeInstance {
	return s.runtimeInstances[identity.ExtensionID][identity.InstanceID]
}

func (s *ProtocolStarter) removeProtocolInstanceLocked(identity RuntimeInstanceIdentity) *protocolRuntimeInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance := s.runtimeInstanceLocked(identity)
	if instance == nil {
		return nil
	}
	delete(s.runtimeInstances[identity.ExtensionID], identity.InstanceID)
	if len(s.runtimeInstances[identity.ExtensionID]) == 0 {
		delete(s.runtimeInstances, identity.ExtensionID)
	}
	if s.activeRuntimeInstances[identity.ExtensionID] == identity.InstanceID {
		delete(s.activeRuntimeInstances, identity.ExtensionID)
		delete(s.clients, identity.ExtensionID)
		delete(s.protocols, identity.ExtensionID)
	}
	return instance
}

func (s *ProtocolStarter) activeProtocolInstanceID(extensionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRuntimeInstances[extensionID]
}

func protocolRuntimeSnapshot(instance *protocolRuntimeInstance) ProtocolRuntimeInstanceSnapshot {
	state := ProtocolRuntimeStaged
	if instance.published {
		state = ProtocolRuntimePublished
	} else if instance.everPublished {
		state = ProtocolRuntimeRetained
	}
	return ProtocolRuntimeInstanceSnapshot{
		Identity: instance.identity, ExtensionVersion: instance.extensionVersion,
		ArtifactDigest: instance.artifactDigest, ManifestDigest: instance.manifestDigest, ProtocolVersion: instance.protocolVersion,
		Target: instance.target, State: state, Healthy: instance.healthy,
		Ready: instance.ready, ReadinessChecked: instance.readinessChecked, StartedAt: instance.startedAt,
	}
}

func normalizeProtocolRuntimeInstanceSet(identities []RuntimeInstanceIdentity) ([]RuntimeInstanceIdentity, error) {
	result := make([]RuntimeInstanceIdentity, 0, len(identities))
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		normalized, err := normalizeRuntimeInstanceIdentity(identity)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized.ExtensionID]; exists {
			return nil, fmt.Errorf("%w: runtime set declares extension %q more than once", ErrRuntimeInstanceConflict, normalized.ExtensionID)
		}
		seen[normalized.ExtensionID] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ExtensionID < result[j].ExtensionID
	})
	return result, nil
}

func manifestProtocolVersion(extension extensions.Extension) int {
	return extension.Manifest.Backend.ProtocolVersion
}

func protocolInstanceNotFound(identity RuntimeInstanceIdentity) error {
	return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotFound, identity.ExtensionID, identity.InstanceID)
}

func protocolRuntimeManifestDigest(manifest extensions.Manifest) (string, error) {
	document, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

var _ StagedRuntimeStarter = (*ProtocolStarter)(nil)
var _ StagedRuntimeSetStarter = (*ProtocolStarter)(nil)
