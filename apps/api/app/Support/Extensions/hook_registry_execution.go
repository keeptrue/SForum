package extensionsruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type HookDocumentRevalidator func(context.Context, string, map[string]any) error

func (m *Manager) HookBus() *HookBus {
	return m.eventsProviders.HookBus()
}

func (m *Manager) EmitHook(ctx context.Context, name string, payload map[string]any) {
	m.eventsProviders.EmitHook(ctx, name, payload)
}

func (m *Manager) Emit(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	return m.eventsProviders.Emit(ctx, envelope)
}

func (m *Manager) Deliver(ctx context.Context, extensionID string, deliveryID int64, envelope appevents.Envelope) appevents.Result {
	return m.eventsProviders.Deliver(ctx, extensionID, deliveryID, envelope)
}

type VersionedHookInvocation struct {
	HookID          string
	ContractVersion string
	CorrelationID   string
	Payload         map[string]any
	Revalidate      HookDocumentRevalidator
}

type VersionedHookResult struct {
	OK      bool
	Reason  string
	Message string
	Payload map[string]any
	Results []map[string]any
	Queued  int
}

// InvokeVersionedHook composes one frozen listener chain. A filter cannot run
// without a Host revalidator, and every accepted patch is rechecked before the
// next listener observes it.
func (m *RuntimeEventsProviders) InvokeVersionedHook(ctx context.Context, input VersionedHookInvocation) VersionedHookResult {
	if m == nil || ctx == nil || m.hooks == nil || m.hooks.registry == nil {
		return versionedHookFailure(ErrHookRegistryInvalid)
	}
	contract, listeners, err := m.hooks.registry.Resolve(input.HookID, input.ContractVersion)
	if err != nil {
		return versionedHookFailure(err)
	}
	payload, err := cloneHookDocument(input.Payload)
	if err != nil {
		return versionedHookFailure(fmt.Errorf("hook input clone: %w", err))
	}
	if input.Revalidate != nil {
		if err := input.Revalidate(ctx, contract.InputSchema, payload); err != nil {
			return versionedHookFailure(fmt.Errorf("hook input revalidation: %w", err))
		}
	} else if contract.Kind == "filter" {
		return versionedHookFailure(fmt.Errorf("%w: filter %s requires Host revalidation", ErrHookRegistryInvalid, contract.ID))
	}
	result := VersionedHookResult{OK: true, Payload: payload}
	for _, listener := range listeners {
		if contract.Execution == "async" {
			if err := m.enqueueVersionedHook(ctx, contract, listener, input.CorrelationID, payload); err != nil {
				if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
					continue
				}
				return versionedHookFailure(err)
			}
			result.Queued++
			continue
		}
		deliveryID := m.beginVersionedHookDelivery(ctx, listener.Artifact.ExtensionID, contract, input.CorrelationID, extensions.DeliveryRunning)
		current := m.invokeVersionedHookListener(ctx, contract, listener, input.CorrelationID, payload, deliveryID)
		if !current.OK {
			if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
				continue
			}
			return VersionedHookResult{OK: false, Reason: current.Reason, Message: current.Message, Payload: payload, Results: result.Results}
		}
		if current.Result != nil {
			if input.Revalidate != nil && contract.ResultSchema != "" {
				if err := input.Revalidate(ctx, contract.ResultSchema, current.Result); err != nil {
					m.failVersionedHookDelivery(ctx, deliveryID, "extension.hook_result_invalid", err)
					if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
						continue
					}
					return versionedHookFailure(fmt.Errorf("hook result revalidation: %w", err))
				}
			}
			cloned, err := cloneHookDocument(current.Result)
			if err != nil {
				m.failVersionedHookDelivery(ctx, deliveryID, "extension.hook_result_invalid", err)
				if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
					continue
				}
				return versionedHookFailure(fmt.Errorf("hook result clone: %w", err))
			}
			result.Results = append(result.Results, cloned)
		}
		if contract.Kind == "filter" && len(current.Patch) > 0 {
			patch, err := cloneHookDocument(current.Patch)
			if err != nil {
				m.failVersionedHookDelivery(ctx, deliveryID, "extension.hook_patch_invalid", err)
				if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
					continue
				}
				return versionedHookFailure(fmt.Errorf("hook patch clone: %w", err))
			}
			if !patchAllowed(patch, contract.MutableFields) {
				m.failVersionedHookDelivery(ctx, deliveryID, "extension.patch_forbidden", ErrHookRegistryInvalid)
				if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
					continue
				}
				return versionedHookFailure(fmt.Errorf("%w: listener %s returned a forbidden patch", ErrHookRegistryInvalid, listener.ID))
			}
			candidate, err := cloneHookDocument(payload)
			if err != nil {
				return versionedHookFailure(fmt.Errorf("hook payload clone: %w", err))
			}
			for key, value := range patch {
				candidate[key] = value
			}
			if err := input.Revalidate(ctx, contract.InputSchema, candidate); err != nil {
				m.failVersionedHookDelivery(ctx, deliveryID, "extension.hook_patch_invalid", err)
				if contract.FailurePolicy == appevents.FailurePolicyFailOpen {
					continue
				}
				return versionedHookFailure(fmt.Errorf("hook patch revalidation: %w", err))
			}
			payload = candidate
			result.Payload = payload
		}
	}
	return result
}

func (m *managerCore) enqueueVersionedHook(
	ctx context.Context,
	contract VersionedHookContract,
	listener VersionedHookListener,
	correlationID string,
	payload map[string]any,
) error {
	deliveryID := m.beginVersionedHookDelivery(ctx, listener.Artifact.ExtensionID, contract, correlationID, extensions.DeliveryQueued)
	if m.dispatcher == nil {
		current := m.invokeVersionedHookListener(ctx, contract, listener, correlationID, payload, deliveryID)
		if !current.OK {
			return fmt.Errorf("versioned hook delivery failed: %s", current.Reason)
		}
		return nil
	}
	queuedPayload, err := cloneHookDocument(payload)
	if err != nil {
		return fmt.Errorf("clone queued hook payload: %w", err)
	}
	args := EventDeliveryArgs{
		DeliveryID: deliveryID, ExtensionID: listener.Artifact.ExtensionID,
		EventName: contract.Name, EventKind: contract.Kind, CorrelationID: correlationID,
		Payload: queuedPayload, DeclarationID: listener.ID, HookID: contract.ID,
		ContractVersion: contract.ContractVersion, RuntimeInstanceID: listener.Artifact.RuntimeInstanceID,
		PackageDigest: listener.Artifact.PackageDigest,
	}
	if err := m.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
		failure := appevents.Result{OK: false, Reason: "extension.delivery_enqueue_failed", Message: err.Error()}
		m.finishDelivery(ctx, deliveryID, extensions.DeliveryFailed, failure, 0)
		return err
	}
	return nil
}

func (m *managerCore) deliverVersionedHook(ctx context.Context, args EventDeliveryArgs) appevents.Result {
	contract, listeners, err := m.hooks.registry.Resolve(args.HookID, args.ContractVersion)
	if err != nil {
		result := appevents.Result{OK: false, Reason: "extension.hook_contract_unavailable", Message: err.Error()}
		m.finishDelivery(ctx, args.DeliveryID, extensions.DeliveryFailed, result, 1)
		return result
	}
	for _, listener := range listeners {
		if listener.ID != args.DeclarationID || listener.Artifact.ExtensionID != args.ExtensionID ||
			listener.Artifact.RuntimeInstanceID != args.RuntimeInstanceID || listener.Artifact.PackageDigest != args.PackageDigest {
			continue
		}
		m.updateDelivery(ctx, extensions.EventDeliveryUpdateInput{ID: args.DeliveryID, Status: extensions.DeliveryRunning, AttemptCount: 1})
		current := m.invokeVersionedHookListener(ctx, contract, listener, args.CorrelationID, args.Payload, args.DeliveryID)
		return hookResultToEventResult(current)
	}
	result := appevents.Result{OK: false, Reason: "extension.hook_runtime_stale", Message: "Queued hook no longer belongs to the active exact runtime."}
	m.finishDelivery(ctx, args.DeliveryID, extensions.DeliveryFailed, result, 1)
	return result
}

func (m *managerCore) beginVersionedHookDelivery(
	ctx context.Context,
	extensionID string,
	contract VersionedHookContract,
	correlationID, status string,
) int64 {
	if m.deliveryStore == nil {
		return 0
	}
	delivery, err := m.deliveryStore.CreateEventDelivery(ctx, extensions.EventDeliveryInput{
		ExtensionID: extensionID, EventName: contract.Name, EventKind: contract.Kind,
		Status: status, CorrelationID: correlationID,
	})
	if err != nil {
		return 0
	}
	return delivery.ID
}

func (m *managerCore) failVersionedHookDelivery(ctx context.Context, deliveryID int64, reason string, err error) {
	m.finishDelivery(ctx, deliveryID, extensions.DeliveryFailed, appevents.Result{
		OK: false, Reason: reason, Message: err.Error(),
	}, 1)
}

func (m *managerCore) invokeVersionedHookListener(
	ctx context.Context,
	contract VersionedHookContract,
	listener VersionedHookListener,
	correlationID string,
	payload map[string]any,
	deliveryID int64,
) HookResult {
	extension, ok := m.runningExtension(listener.Artifact.ExtensionID)
	if !ok || extension.Version != listener.Artifact.ExtensionVersion || extension.PackageDigest != listener.Artifact.PackageDigest {
		return HookResult{OK: false, Reason: "extension.runtime_unavailable", Message: "Plugin runtime is not available."}
	}
	started := time.Now()
	invocationPayload, err := cloneHookDocument(payload)
	if err != nil {
		return HookResult{OK: false, Reason: "extension.hook_payload_invalid", Message: err.Error()}
	}
	current := m.invoke(ctx, extension, HookInput{
		DeclarationID: listener.ID, Name: contract.Name, Kind: contract.Kind,
		ContractVersion: contract.ContractVersion, InputSchema: contract.InputSchema,
		ResultSchema: contract.ResultSchema, FailurePolicy: contract.FailurePolicy,
		DeliveryID: deliveryID, CorrelationID: correlationID,
		Timeout: time.Duration(contract.TimeoutMS) * time.Millisecond,
		Payload: invocationPayload, PatchFields: append([]string(nil), contract.MutableFields...),
	})
	current = eventResultToHookResult(annotateSlowOrTimeout(hookResultToEventResult(current), time.Since(started), ctx.Err()), current)
	if deliveryID != 0 {
		status := extensions.DeliverySucceeded
		if !current.OK {
			status = extensions.DeliveryFailed
		}
		m.finishDelivery(ctx, deliveryID, status, hookResultToEventResult(current), 1)
	}
	return current
}

func eventResultToHookResult(event appevents.Result, original HookResult) HookResult {
	original.OK, original.Reason, original.Message = event.OK, event.Reason, event.Message
	return original
}

func versionedHookFailure(err error) VersionedHookResult {
	return VersionedHookResult{OK: false, Reason: "extension.hook_registry_rejected", Message: err.Error()}
}

func cloneHookDocument(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) InvokeVersionedHook(ctx context.Context, input VersionedHookInvocation) VersionedHookResult {
	return m.eventsProviders.InvokeVersionedHook(ctx, input)
}
