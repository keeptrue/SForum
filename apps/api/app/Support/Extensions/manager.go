package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

var ErrVersionedRuntimeContractInvalid = errors.New("extension versioned runtime contract is invalid")

type Starter interface {
	Start(ctx context.Context, extension extensions.Extension) (RouteTarget, error)
	Stop(ctx context.Context, extension extensions.Extension) error
}

type MailProviderInvoker interface {
	SendMail(context.Context, string, MailProviderRequest) (MailProviderResponse, error)
}

// StorageProviderInvoker 见 storage_invoker.go（E6.2）。

type ManagerConfig struct {
	Starter       Starter
	HookBus       *HookBus
	DeliveryStore DeliveryStore
	Dispatcher    EventDispatcher
	// Resilience 可选；nil 使用默认 F2.3 参数。
	Resilience ResilienceConfig
	Activation *extensions.ActivationCoordinator
	BootID     string
}

type runtimeAdmissionState struct {
	mu                   sync.RWMutex
	runtimeSetTransition chan struct{}
	statuses             map[string]extensions.RuntimeStatus
	targets              map[string]RouteTarget
	running              map[string]extensions.Extension
	runtimeInstances     map[string]map[string]*managedRuntimeInstance
	activeInstances      map[string]string
	runtimeLifecycleMu   sync.Mutex
	runtimeLifecycle     map[string]chan struct{}
}

type runtimeEventsProviderState struct {
	hooks              *HookBus
	deliveryStore      DeliveryStore
	dispatcher         EventDispatcher
	resilience         *resilienceHub
	providerSelections *ProviderSlotSelectionAPI
}

type managerCore struct {
	*runtimeAdmissionState
	*runtimeEventsProviderState
	host          *Manager
	starter       Starter
	activation    *extensions.ActivationCoordinator
	bootID        string
	startPreparer func(context.Context, extensions.Extension) error
}

// Manager 保留历史 runtime 契约，并将业务入口委托给 focused collaborators。
type Manager struct {
	*managerCore
	supervisor      *RuntimeSupervisor
	admission       *InstanceAdmission
	invoker         *RuntimeInvoker
	eventsProviders *RuntimeEventsProviders
}

type RuntimeSupervisor struct{ *Manager }

type InstanceAdmission struct {
	*Manager
	state *runtimeAdmissionState
}

type RuntimeInvoker struct{ *Manager }

type RuntimeEventsProviders struct {
	*Manager
	state *runtimeEventsProviderState
}

// BindProviderSlotSelections attaches the durable exact-artifact choice to the
// already constructed registry. API and worker bootstrap call this before any
// provider invocation; registry publication itself remains database-free.
func (m *Manager) BindProviderSlotSelections(store ProviderSlotSelectionStore) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if store == nil {
		m.providerSelections = nil
		return
	}
	m.providerSelections = NewProviderSlotSelectionAPI(m.hooks.ProviderSlots(), store)
}

func (m *RuntimeEventsProviders) ProviderSlotSelections() *ProviderSlotSelectionAPI {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerSelections
}

func (m *Manager) SetStartPreparer(preparer func(context.Context, extensions.Extension) error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.startPreparer = preparer
	m.mu.Unlock()
}

func (m *managerCore) prepareRuntimeStart(ctx context.Context, extension extensions.Extension) error {
	m.mu.RLock()
	preparer := m.startPreparer
	m.mu.RUnlock()
	if preparer == nil {
		return nil
	}
	return preparer(ctx, extension)
}

type DeliveryStore interface {
	CreateEventDelivery(ctx context.Context, input extensions.EventDeliveryInput) (extensions.ExtensionEventDelivery, error)
	UpdateEventDelivery(ctx context.Context, input extensions.EventDeliveryUpdateInput) error
}

type EventDispatcher interface {
	Enqueue(ctx context.Context, args EventDeliveryArgs, opts supportjobs.EnqueueOptions) error
}

func NewManager(config ManagerConfig) *Manager {
	starter := config.Starter
	if starter == nil {
		starter = localStarter{}
	}
	hooks := config.HookBus
	if hooks == nil {
		var invoker HookInvoker
		if candidate, ok := starter.(HookInvoker); ok {
			invoker = candidate
		}
		hooks = NewHookBus(HookBusConfig{Invoker: invoker})
	}
	bootID := config.BootID
	if config.Activation != nil && bootID == "" {
		bootID = extensions.NewActivationBootID()
	}
	runtimeSetTransition := make(chan struct{}, 1)
	runtimeSetTransition <- struct{}{}
	admission := &runtimeAdmissionState{
		runtimeSetTransition: runtimeSetTransition,
		statuses:             map[string]extensions.RuntimeStatus{},
		targets:              map[string]RouteTarget{},
		running:              map[string]extensions.Extension{},
		runtimeInstances:     map[string]map[string]*managedRuntimeInstance{},
		activeInstances:      map[string]string{},
		runtimeLifecycle:     map[string]chan struct{}{},
	}
	eventsProviders := &runtimeEventsProviderState{
		hooks:         hooks,
		deliveryStore: config.DeliveryStore,
		dispatcher:    config.Dispatcher,
		resilience:    newResilienceHub(config.Resilience),
	}
	manager := &Manager{managerCore: &managerCore{
		runtimeAdmissionState:      admission,
		runtimeEventsProviderState: eventsProviders,
		starter:                    starter,
		activation:                 config.Activation,
		bootID:                     bootID,
	}}
	manager.managerCore.host = manager
	manager.supervisor = &RuntimeSupervisor{Manager: manager}
	manager.admission = &InstanceAdmission{Manager: manager, state: admission}
	manager.invoker = &RuntimeInvoker{Manager: manager}
	manager.eventsProviders = &RuntimeEventsProviders{Manager: manager, state: eventsProviders}
	return manager
}

func (m *Manager) WithActivation(coordinator *extensions.ActivationCoordinator, bootID string) *Manager {
	m.activation = coordinator
	m.bootID = bootID
	if coordinator != nil && m.bootID == "" {
		m.bootID = extensions.NewActivationBootID()
	}
	return m
}

func (m *RuntimeSupervisor) Check(_ context.Context, extension extensions.Extension) error {
	if extension.Manifest.Backend.Entry == "" {
		return nil
	}
	path, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
	if !ok {
		return extensions.ErrInvalidManifest
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fmt.Errorf("backend entry %s is not available", extension.Manifest.Backend.Entry)
	}
	return nil
}

func (m *RuntimeSupervisor) Start(ctx context.Context, extension extensions.Extension) error {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.startRuntimeSetLocked(ctx, extension)
}

// startRuntimeSetLocked requires the Manager-wide runtime-set transition lock.
// Aggregate lifecycle operations use it instead of re-entering the outer lock.
func (m *managerCore) startRuntimeSetLocked(ctx context.Context, extension extensions.Extension) error {
	unlock, err := m.lockRuntimeLifecycleContext(ctx, extension.ID)
	if err != nil {
		return err
	}
	defer unlock()

	hookCount, eventCount := runtimeManifestCounts(extension.Manifest)
	m.mu.Lock()
	previousStatus, hadPreviousStatus := m.statuses[extension.ID]
	previousInstanceID := m.activeInstances[extension.ID]
	m.statuses[extension.ID] = extensions.RuntimeStatus{
		State:         extensions.RuntimeStarting,
		RouteCount:    len(extension.Manifest.Routes),
		HookCount:     hookCount,
		EventCount:    eventCount,
		ProviderCount: len(extension.Manifest.Providers),
	}
	m.mu.Unlock()
	if err := m.prepareRuntimeStart(ctx, extension); err != nil {
		m.recordRuntimeStartFailure(extension, previousInstanceID, previousStatus, hadPreviousStatus, err)
		return err
	}
	target, err := m.starter.Start(ctx, extension)
	if err != nil {
		m.recordRuntimeStartFailure(extension, previousInstanceID, previousStatus, hadPreviousStatus, err)
		return err
	}
	if hasVersionedRuntimeContracts(extension) && strings.TrimSpace(target.InstanceID) == "" {
		err := fmt.Errorf("%w: exact runtime instance is required", ErrVersionedRuntimeContractInvalid)
		stopErr := m.starter.Stop(ctx, extension)
		m.recordRuntimeStartFailure(extension, previousInstanceID, previousStatus, hadPreviousStatus, err)
		return errors.Join(err, stopErr)
	}
	previousHooks, hadPreviousHooks := m.hooks.RuntimeSnapshot(extension.ID)
	publishedVersionedHooks := publishesVersionedHookSnapshot(extension, target.InstanceID)
	if publishedVersionedHooks {
		if err := m.hooks.RegisterRuntime(extension, target.InstanceID); err != nil {
			stopErr := m.starter.Stop(ctx, extension)
			m.recordRuntimeStartFailure(extension, previousInstanceID, previousStatus, hadPreviousStatus, err)
			return errors.Join(err, stopErr)
		}
	}
	now := time.Now().UTC()
	m.mu.Lock()
	target, err = m.activateRuntimeInstanceLocked(extension, target)
	if err != nil {
		m.mu.Unlock()
		_ = m.starter.Stop(ctx, extension)
		if publishedVersionedHooks {
			_ = m.hooks.restoreRuntime(extension.ID, target.InstanceID, previousHooks, hadPreviousHooks)
		}
		m.recordRuntimeStartFailure(extension, previousInstanceID, previousStatus, hadPreviousStatus, err)
		return err
	}
	m.targets[extension.ID] = target
	m.running[extension.ID] = extension
	m.statuses[extension.ID] = extensions.RuntimeStatus{
		State:         extensions.RuntimeRunning,
		StartedAt:     &now,
		RouteCount:    len(extension.Manifest.Routes),
		HookCount:     hookCount,
		EventCount:    eventCount,
		ProviderCount: len(extension.Manifest.Providers),
	}
	m.mu.Unlock()
	if !publishedVersionedHooks {
		if err := m.hooks.RegisterRuntime(extension, target.InstanceID); err != nil {
			return err
		}
	}
	return nil
}

func (m *RuntimeSupervisor) Stop(ctx context.Context, extension extensions.Extension) error {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.stopRuntimeSetLocked(ctx, extension)
}

// stopRuntimeSetLocked requires the Manager-wide runtime-set transition lock.
func (m *managerCore) stopRuntimeSetLocked(ctx context.Context, extension extensions.Extension) error {
	unlock, err := m.lockRuntimeLifecycleContext(ctx, extension.ID)
	if err != nil {
		return err
	}
	defer unlock()

	m.mu.RLock()
	instanceID := m.activeInstances[extension.ID]
	m.mu.RUnlock()
	if err := m.hooks.validateUnregisterRuntime(extension, instanceID); err != nil {
		return err
	}
	err = m.starter.Stop(ctx, extension)
	m.mu.Lock()
	if m.deactivateRuntimeInstanceLocked(RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: instanceID}) {
		delete(m.targets, extension.ID)
		delete(m.running, extension.ID)
		m.statuses[extension.ID] = extensions.RuntimeStatus{
			State:         extensions.RuntimeStopped,
			RouteCount:    len(extension.Manifest.Routes),
			HookCount:     len(extension.Manifest.Hooks),
			EventCount:    len(extension.Manifest.Events),
			ProviderCount: len(extension.Manifest.Providers),
		}
	} else if instanceID == "" {
		delete(m.targets, extension.ID)
		delete(m.running, extension.ID)
		m.statuses[extension.ID] = extensions.RuntimeStatus{
			State:         extensions.RuntimeStopped,
			RouteCount:    len(extension.Manifest.Routes),
			HookCount:     len(extension.Manifest.Hooks),
			EventCount:    len(extension.Manifest.Events),
			ProviderCount: len(extension.Manifest.Providers),
		}
	}
	m.mu.Unlock()
	_, hookErr := m.hooks.unregisterRuntime(extension.ID, instanceID)
	if m.resilience != nil {
		m.resilience.remove(extension.ID)
	}
	return errors.Join(err, hookErr)
}

func (m *RuntimeSupervisor) Status(_ context.Context, extension extensions.Extension) extensions.RuntimeStatus {
	m.mu.RLock()
	status, ok := m.statuses[extension.ID]
	m.mu.RUnlock()
	if !ok {
		return extensions.RuntimeStatus{
			State:         extensions.RuntimeStopped,
			RouteCount:    len(extension.Manifest.Routes),
			HookCount:     len(extension.Manifest.Hooks),
			EventCount:    len(extension.Manifest.Events),
			ProviderCount: len(extension.Manifest.Providers),
		}
	}
	if status.State == extensions.RuntimeRunning || status.State == extensions.RuntimeDegraded {
		if _, err := m.ActiveRuntimeInstance(extension.ID); err != nil {
			status.State = extensions.RuntimeFailed
			status.LastError = err.Error()
		}
	}
	// F2.3：把闸门/熔断快照合并进状态，熔断打开时标 degraded。
	return m.decorateStatus(extension.ID, status)
}

func (m *RuntimeSupervisor) RouteTarget(extensionID string) (RouteTarget, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	target, ok := m.targets[extensionID]
	return target, ok
}

// HookBus exposes the Manager-owned exact hook snapshot to the lifecycle
// aggregate. Callers must still use Manager admission for execution.
func (m *RuntimeEventsProviders) HookBus() *HookBus {
	if m == nil {
		return nil
	}
	return m.hooks
}

func (m *RuntimeInvoker) SendMail(ctx context.Context, extensionID string, request MailProviderRequest) (MailProviderResponse, error) {
	invoker, ok := m.starter.(MailProviderInvoker)
	if !ok {
		return MailProviderResponse{}, extensions.ErrRuntimeUnavailable
	}
	// F2.3：出站邮件也走并发/熔断闸门，并施加默认超时。
	timeout := m.resilience.cfg.DefaultMailTimeout
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	_, admission, err := m.AcquireActiveRuntimeCall(ctx, extensionID, RuntimeCallProvider)
	if err != nil {
		return MailProviderResponse{}, errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	ctx = admission.Context

	release, rejected := m.resilience.tryEnter(ctx, extensionID)
	if rejected != "" {
		return MailProviderResponse{
			OK:             false,
			Classification: "temporary",
			Reason:         rejected,
			Message:        circuitMessage(rejected),
		}, nil
	}
	response, err := invoker.SendMail(ctx, extensionID, request)
	success := err == nil && response.OK
	reason := response.Reason
	if err != nil {
		reason = "extension.mail_failed"
	}
	if ctx.Err() != nil && !success {
		reason = "extension.hook_timeout"
	}
	release(success, reason)
	return response, err
}

// ExecutePluginJob rechecks the running manifest immediately before transport
// dispatch so an upgrade cannot race a worker's earlier database resolution.
func (m *RuntimeInvoker) ExecutePluginJob(ctx context.Context, invocation supportjobs.PluginJobInvocation) error {
	extension, ok := m.runningExtension(invocation.Contract.ExtensionID)
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	contract, err := extensions.PluginJobContractForExtension(extension, invocation.Contract.JobName)
	if err != nil || !contract.Equal(invocation.Contract) {
		return supportjobs.ErrPluginJobRuntimeStale
	}
	invoker, ok := m.starter.(pluginJobContextInvoker)
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	instance, admission, err := m.AcquireActiveRuntimeCall(ctx, extension.ID, RuntimeCallJob)
	if err != nil {
		return errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	if !runtimeInstanceMatchesExtension(instance, extension) {
		return supportjobs.ErrPluginJobRuntimeStale
	}
	ctx = admission.Context
	release, rejected := m.resilience.tryEnter(ctx, extension.ID)
	if rejected != "" {
		return fmt.Errorf("%s: %s", rejected, circuitMessage(rejected))
	}
	err = invoker.ExecutePluginJob(ctx, invocation)
	release(err == nil, "extension.plugin_job_failed")
	return err
}

func (m *RuntimeInvoker) RefreshMailProvider(ctx context.Context, extensionID string) error {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.refreshMailProviderRuntimeSetLocked(ctx, extensionID)
}

func (m *managerCore) refreshMailProviderRuntimeSetLocked(ctx context.Context, extensionID string) error {
	m.mu.RLock()
	extension, ok := m.running[extensionID]
	m.mu.RUnlock()
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	if err := m.starter.Stop(ctx, extension); err != nil {
		return err
	}
	_, err := m.starter.Start(ctx, extension)
	return err
}

func (m *RuntimeSupervisor) Reconcile(ctx context.Context, items []extensions.Extension) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return
	}
	defer unlock()
	m.reconcileRuntimeSetLocked(ctx, items)
}

func (m *managerCore) reconcileRuntimeSetLocked(ctx context.Context, items []extensions.Extension) {
	enabled := map[string]extensions.Extension{}
	runtime := runtimeSetLockedManager{manager: m.host}
	for _, item := range items {
		if item.Type == extensions.TypePlugin && item.Status == extensions.StatusEnabled && item.Manifest.Backend.Entry != "" {
			if m.activation != nil {
				skip, err := m.activation.ShouldSkipStartup(ctx, item, m.bootID)
				if err != nil {
					m.setStatus(item, extensions.RuntimeStatus{State: extensions.RuntimeFailed, LastError: err.Error()})
					continue
				}
				if skip {
					m.setStatus(item, extensions.RuntimeStatus{State: extensions.RuntimeStopped, LastError: "extension.boot_loop_skipped"})
					continue
				}
			}
			enabled[item.ID] = item
			if m.activation != nil {
				_ = m.activation.Start(ctx, runtime, item, extensions.ActivationTriggerStartup, 0, m.bootID)
			} else {
				_ = m.startRuntimeSetLocked(ctx, item)
			}
		}
	}
	m.mu.RLock()
	running := make([]extensions.Extension, 0, len(m.running))
	for id, item := range m.running {
		if _, ok := enabled[id]; !ok {
			running = append(running, item)
		}
	}
	m.mu.RUnlock()
	for _, item := range running {
		_ = m.stopRuntimeSetLocked(ctx, item)
	}
}

func (m *RuntimeSupervisor) Close(ctx context.Context) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return
	}
	defer unlock()
	m.closeRuntimeSetLocked(ctx)
}

func (m *managerCore) closeRuntimeSetLocked(ctx context.Context) {
	m.mu.RLock()
	running := make([]extensions.Extension, 0, len(m.running))
	for _, item := range m.running {
		running = append(running, item)
	}
	m.mu.RUnlock()
	for _, item := range running {
		_ = m.stopRuntimeSetLocked(ctx, item)
	}
}

// runtimeSetLockedManager preserves ActivationCoordinator's RuntimeManager
// contract while the aggregate caller already owns runtimeSetTransition.
type runtimeSetLockedManager struct {
	manager *Manager
}

func (m runtimeSetLockedManager) Check(ctx context.Context, extension extensions.Extension) error {
	return m.manager.Check(ctx, extension)
}

func (m runtimeSetLockedManager) Start(ctx context.Context, extension extensions.Extension) error {
	return m.manager.startRuntimeSetLocked(ctx, extension)
}

func (m runtimeSetLockedManager) Stop(ctx context.Context, extension extensions.Extension) error {
	return m.manager.stopRuntimeSetLocked(ctx, extension)
}

func (m runtimeSetLockedManager) Status(ctx context.Context, extension extensions.Extension) extensions.RuntimeStatus {
	return m.manager.Status(ctx, extension)
}

func (m runtimeSetLockedManager) EmitHook(ctx context.Context, name string, payload map[string]any) {
	m.manager.EmitHook(ctx, name, payload)
}

func (m *RuntimeEventsProviders) EmitHook(ctx context.Context, name string, payload map[string]any) {
	m.Emit(ctx, appevents.NewEnvelope(name, payload))
}

func (m *RuntimeEventsProviders) Emit(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	envelope = normalizeEnvelope(envelope)
	definition, ok := appevents.FindDefinition(envelope.Name)
	if !ok {
		return appevents.Result{OK: false, Reason: "extension.event_unknown", Message: "Unknown extension event."}
	}
	if envelope.Kind == "" {
		envelope.Kind = definition.Kind
	}
	if envelope.Kind != definition.Kind {
		return appevents.Result{OK: false, Reason: "extension.event_kind_invalid", Message: "Extension event kind does not match the event definition."}
	}
	if envelope.Kind == appevents.KindObserve {
		return m.emitObserve(ctx, envelope)
	}
	return m.emitSync(ctx, envelope)
}

func (m *RuntimeEventsProviders) Deliver(ctx context.Context, extensionID string, deliveryID int64, envelope appevents.Envelope) appevents.Result {
	envelope = normalizeEnvelope(envelope)
	extension, ok := m.runningExtension(extensionID)
	if !ok {
		result := appevents.Result{OK: false, Reason: "extension.runtime_unavailable", Message: "Plugin runtime is not available."}
		m.finishDelivery(ctx, deliveryID, extensions.DeliveryFailed, result, 1)
		return result
	}
	m.updateDelivery(ctx, extensions.EventDeliveryUpdateInput{
		ID:           deliveryID,
		Status:       extensions.DeliveryRunning,
		AttemptCount: 1,
	})
	result := hookResultToEventResult(m.invoke(ctx, extension, hookInputFromEnvelope(envelope, deliveryID)))
	status := extensions.DeliverySucceeded
	if !result.OK {
		status = extensions.DeliveryFailed
	}
	m.finishDelivery(ctx, deliveryID, status, result, 1)
	return result
}

func (m *managerCore) emitObserve(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	listeners := m.hooks.Listeners(envelope.Name)
	for _, listener := range listeners {
		deliveryID := int64(0)
		if m.deliveryStore != nil {
			delivery, err := m.deliveryStore.CreateEventDelivery(ctx, extensions.EventDeliveryInput{
				ExtensionID:   listener.ID,
				EventName:     envelope.Name,
				EventKind:     envelope.Kind,
				Status:        extensions.DeliveryQueued,
				CorrelationID: envelope.CorrelationID,
			})
			if err != nil {
				return appevents.Result{OK: false, Reason: "extension.delivery_record_failed", Message: err.Error()}
			}
			deliveryID = delivery.ID
		}
		args := EventDeliveryArgs{
			DeliveryID:    deliveryID,
			ExtensionID:   listener.ID,
			EventName:     envelope.Name,
			EventKind:     envelope.Kind,
			CorrelationID: envelope.CorrelationID,
			Payload:       envelope.Payload,
			PatchFields:   envelope.PatchFields,
		}
		if m.dispatcher != nil {
			if err := m.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
				result := appevents.Result{OK: false, Reason: "extension.delivery_enqueue_failed", Message: err.Error()}
				m.finishDelivery(ctx, deliveryID, extensions.DeliveryFailed, result, 0)
				return result
			}
			continue
		}
		m.host.Deliver(ctx, listener.ID, deliveryID, envelope)
	}
	return appevents.Result{OK: true}
}

func (m *managerCore) emitSync(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	definition, _ := appevents.FindDefinition(envelope.Name)
	failOpen := definition.FailurePolicy == appevents.FailurePolicyFailOpen
	result := appevents.Result{OK: true}

	for _, listener := range m.hooks.Listeners(envelope.Name) {
		// F1.3：sync filter/validate 也写 delivery，便于事件日志查看慢/失败投递。
		deliveryID := m.beginSyncDelivery(ctx, listener.ID, envelope)
		input := hookInputFromEnvelope(envelope, deliveryID)
		started := time.Now()
		current := hookResultToEventResult(m.invoke(ctx, listener, input))
		elapsed := time.Since(started)
		current = annotateSlowOrTimeout(current, elapsed, ctx.Err())

		if current.OK && envelope.Kind == appevents.KindFilter && len(current.Patch) > 0 {
			if !patchAllowed(current.Patch, envelope.PatchFields) {
				current = appevents.Result{OK: false, Reason: "extension.patch_forbidden", Message: "Plugin returned a patch field that is not allowed for this event."}
			}
		}

		status := extensions.DeliverySucceeded
		if !current.OK {
			status = extensions.DeliveryFailed
		}
		m.finishDelivery(ctx, deliveryID, status, current, 1)

		if !current.OK {
			if failOpen {
				// fail_open：记录失败后继续后续 listener，不阻断业务。
				continue
			}
			return current
		}
		if envelope.Kind == appevents.KindFilter && len(current.Patch) > 0 {
			if result.Patch == nil {
				result.Patch = map[string]any{}
			}
			for key, value := range current.Patch {
				result.Patch[key] = value
			}
		}
	}
	return result
}

func (m *managerCore) beginSyncDelivery(ctx context.Context, extensionID string, envelope appevents.Envelope) int64 {
	if m.deliveryStore == nil {
		return 0
	}
	delivery, err := m.deliveryStore.CreateEventDelivery(ctx, extensions.EventDeliveryInput{
		ExtensionID:   extensionID,
		EventName:     envelope.Name,
		EventKind:     envelope.Kind,
		Status:        extensions.DeliveryRunning,
		CorrelationID: envelope.CorrelationID,
	})
	if err != nil {
		return 0
	}
	return delivery.ID
}

// annotateSlowOrTimeout 把超时与慢调用映射到稳定 reason，方便事件日志筛选。
func annotateSlowOrTimeout(result appevents.Result, elapsed time.Duration, ctxErr error) appevents.Result {
	if ctxErr != nil && (errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(ctxErr, context.Canceled)) {
		if result.OK {
			result.OK = false
		}
		if result.Reason == "" {
			result.Reason = "extension.hook_timeout"
		}
		if result.Message == "" {
			result.Message = "Plugin hook exceeded the host timeout. Heavy work must enqueue a job."
		}
		return result
	}
	// 成功但过慢：保留 OK，reason 标记 slow 供运维可见（不阻断）。
	if result.OK && elapsed >= time.Duration(appevents.SlowDeliveryMS)*time.Millisecond {
		if result.Reason == "" {
			result.Reason = "extension.hook_slow"
		}
		if result.Message == "" {
			result.Message = "Plugin hook was slow; move heavy work to a background job."
		}
	}
	return result
}

func (m *managerCore) invoke(ctx context.Context, extension extensions.Extension, input HookInput) HookResult {
	if m.hooks == nil || m.hooks.invoker == nil {
		return HookResult{OK: true}
	}
	// 始终为 sync 调用施加超时：目录未配置时用 DefaultSyncTimeoutMS。
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = time.Duration(appevents.DefaultSyncTimeoutMS) * time.Millisecond
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	instance, admission, err := m.host.AcquireActiveRuntimeCall(ctx, extension.ID, RuntimeCallHook)
	if err != nil || !runtimeInstanceMatchesExtension(instance, extension) {
		return HookResult{
			OK:      false,
			Reason:  "extension.runtime_unavailable",
			Message: "Plugin runtime is not available.",
		}
	}
	defer admission.Release()
	ctx = admission.Context

	// F2.3：熔断/并发闸门。observe 类 fail_open 事件在熔断时跳过而非阻断。
	definition, _ := appevents.FindDefinition(input.Name)
	failurePolicy := input.FailurePolicy
	if failurePolicy == "" {
		failurePolicy = definition.FailurePolicy
	}
	failOpen := failurePolicy == appevents.FailurePolicyFailOpen || input.Kind == appevents.KindObserve

	release, rejected := m.resilience.tryEnter(ctx, extension.ID)
	if rejected != "" {
		if failOpen && rejected == "extension.circuit_open" {
			return HookResult{
				OK:      true,
				Reason:  "extension.circuit_open_skipped",
				Message: "Plugin circuit is open; observe/fail-open delivery skipped.",
			}
		}
		return HookResult{
			OK:      false,
			Reason:  rejected,
			Message: circuitMessage(rejected),
		}
	}

	result := m.hooks.invoker.InvokeHook(ctx, extension, input)
	// invoker 可能忽略 ctx；若已超时仍返回 OK，宿主强制失败（fail_closed 路径）。
	if err := ctx.Err(); err != nil && result.OK {
		result = HookResult{
			OK:      false,
			Reason:  "extension.hook_timeout",
			Message: "Plugin hook exceeded the host timeout. Heavy work must enqueue a job.",
		}
	}
	if err := ctx.Err(); err != nil && !result.OK && result.Reason == "" {
		result.Reason = "extension.hook_timeout"
		if result.Message == "" {
			result.Message = "Plugin hook exceeded the host timeout. Heavy work must enqueue a job."
		}
	}

	// 慢成功不记失败；仅真正失败推进熔断。
	success := result.OK
	reason := result.Reason
	if !success && reason == "" {
		reason = "extension.hook_failed"
	}
	release(success, reason)
	return result
}

func (m *managerCore) decorateStatus(extensionID string, status extensions.RuntimeStatus) extensions.RuntimeStatus {
	if source, ok := m.starter.(ProtocolTelemetrySource); ok {
		telemetry := source.ProtocolTelemetry(extensionID)
		status.ProtocolVersion = telemetry.ProtocolVersion
		status.ProtocolTransport = telemetry.Transport
		status.ProtocolDeprecated = telemetry.Deprecated
		status.ProtocolStartCount = telemetry.StartCount
		status.ProtocolCallCount = telemetry.CallCount
		status.ProtocolLastCallAt = telemetry.LastCallAt
	}
	if m.resilience == nil {
		return status
	}
	// 仅 running/degraded 需要叠加闸门信息；failed/stopped 保持原样。
	if status.State != extensions.RuntimeRunning && status.State != extensions.RuntimeDegraded && status.State != extensions.RuntimeStarting {
		return status
	}
	snap := m.resilience.snapshot(extensionID)
	status.CircuitOpen = snap.CircuitOpen
	status.CircuitOpenUntil = snap.CircuitOpenUntil
	status.ConsecutiveFailures = snap.ConsecutiveFailures
	status.LastFailureReason = snap.LastFailureReason
	status.LastFailureAt = snap.LastFailureAt
	status.ActiveRPCCalls = snap.ActiveCalls
	status.MaxConcurrentRPC = snap.MaxConcurrent
	if snap.CircuitOpen {
		status.State = extensions.RuntimeDegraded
		if status.LastError == "" {
			status.LastError = circuitMessage("extension.circuit_open")
		}
	} else if snap.ConsecutiveFailures > 0 && status.State == extensions.RuntimeRunning {
		// 有连续失败但未熔断：仍标 degraded，便于管理端早发现。
		status.State = extensions.RuntimeDegraded
		if status.LastError == "" && snap.LastFailureReason != "" {
			status.LastError = snap.LastFailureReason
		}
	} else if status.State == extensions.RuntimeDegraded && snap.ConsecutiveFailures == 0 && !snap.CircuitOpen {
		status.State = extensions.RuntimeRunning
		status.LastError = ""
	}
	return status
}

func circuitMessage(reason string) string {
	switch reason {
	case "extension.circuit_open":
		return "Plugin circuit is open after repeated failures. Calls are rejected until cooldown ends."
	case "extension.hook_timeout":
		return "Plugin RPC concurrency limit or deadline exceeded."
	default:
		return "Plugin RPC was rejected by the host resilience gate."
	}
}

func (m *managerCore) runningExtension(extensionID string) (extensions.Extension, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	extension, ok := m.running[extensionID]
	return extension, ok
}

func (m *managerCore) updateDelivery(ctx context.Context, input extensions.EventDeliveryUpdateInput) {
	if m.deliveryStore == nil || input.ID == 0 {
		return
	}
	_ = m.deliveryStore.UpdateEventDelivery(ctx, input)
}

func (m *managerCore) finishDelivery(ctx context.Context, deliveryID int64, status string, result appevents.Result, attempts int) {
	m.updateDelivery(ctx, extensions.EventDeliveryUpdateInput{
		ID:           deliveryID,
		Status:       status,
		Reason:       result.Reason,
		Message:      result.Message,
		AttemptCount: attempts,
		Completed:    true,
	})
}

func (m *managerCore) setStatus(extension extensions.Extension, status extensions.RuntimeStatus) {
	m.mu.Lock()
	m.statuses[extension.ID] = status
	m.mu.Unlock()
}

func normalizeEnvelope(envelope appevents.Envelope) appevents.Envelope {
	if envelope.ID == "" {
		envelope.ID = appevents.NewID()
	}
	if envelope.CorrelationID == "" {
		envelope.CorrelationID = envelope.ID
	}
	if envelope.OccurredAt.IsZero() {
		envelope.OccurredAt = time.Now().UTC()
	}
	if envelope.Kind == "" {
		if definition, ok := appevents.FindDefinition(envelope.Name); ok {
			envelope.Kind = definition.Kind
			envelope.PatchFields = append(envelope.PatchFields, definition.PatchFields...)
		}
	}
	return envelope
}

func hookResultToEventResult(result HookResult) appevents.Result {
	return appevents.Result{
		OK:      result.OK,
		Reason:  result.Reason,
		Message: result.Message,
		Patch:   result.Patch,
	}
}

func patchAllowed(patch map[string]any, allowed []string) bool {
	if len(patch) == 0 {
		return true
	}
	allowedSet := map[string]bool{}
	for _, field := range allowed {
		allowedSet[field] = true
	}
	for field := range patch {
		if !allowedSet[field] {
			return false
		}
	}
	return true
}

type localStarter struct{}

func (localStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{BaseURL: "http://127.0.0.1:0"}, nil
}

func (localStarter) Stop(context.Context, extensions.Extension) error {
	return nil
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) Check(arg0 context.Context, extension extensions.Extension) error {
	return m.supervisor.Check(arg0, extension)
}

func (m *Manager) Start(ctx context.Context, extension extensions.Extension) error {
	return m.supervisor.Start(ctx, extension)
}

func (m *Manager) Stop(ctx context.Context, extension extensions.Extension) error {
	return m.supervisor.Stop(ctx, extension)
}

func (m *Manager) Status(arg0 context.Context, extension extensions.Extension) extensions.RuntimeStatus {
	return m.supervisor.Status(arg0, extension)
}

func (m *Manager) RouteTarget(extensionID string) (RouteTarget, bool) {
	return m.supervisor.RouteTarget(extensionID)
}

func (m *Manager) Reconcile(ctx context.Context, items []extensions.Extension) {
	m.supervisor.Reconcile(ctx, items)
}

func (m *Manager) Close(ctx context.Context) {
	m.supervisor.Close(ctx)
}
