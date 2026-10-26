package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type ProtocolV2ProviderBroker struct {
	manager    *Manager
	revalidate HookDocumentRevalidator
}

func NewProtocolV2ProviderBroker(manager *Manager, revalidate HookDocumentRevalidator) (*ProtocolV2ProviderBroker, error) {
	if manager == nil || revalidate == nil {
		return nil, fmt.Errorf("%w: manager and Host revalidator are required", ErrProviderSlotInvalid)
	}
	return &ProtocolV2ProviderBroker{manager: manager, revalidate: revalidate}, nil
}

func (m *RuntimeEventsProviders) ProtocolV2ProviderBroker() (hostapi.ProtocolV2ProviderBroker, error) {
	return NewProtocolV2ProviderBroker(m.Manager, BoundedProviderDocumentRevalidator)
}

func BoundedProviderDocumentRevalidator(ctx context.Context, schema string, document map[string]any) error {
	if ctx == nil {
		return ErrProviderSlotInvalid
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if _, _, err := protocolV2SchemaRef(strings.TrimSpace(schema)); err != nil {
		return fmt.Errorf("provider schema identity: %w", err)
	}
	cloned, err := cloneHookDocument(document)
	if err != nil {
		return fmt.Errorf("provider document is not JSON: %w", err)
	}
	payload, err := json.Marshal(cloned)
	if err != nil || len(payload) > DefaultProtocolV2MaxMessageBytes {
		return fmt.Errorf("provider document exceeds Host bounds")
	}
	return nil
}

func (b *ProtocolV2ProviderBroker) InvokeProtocolV2Provider(
	ctx context.Context,
	input hostapi.ProtocolV2ProviderInvocation,
) (hostapi.ProtocolV2ProviderResult, error) {
	if b == nil || b.manager == nil || b.revalidate == nil {
		return hostapi.ProtocolV2ProviderResult{}, ErrProviderSlotInvalid
	}
	if !b.manager.hooks.providerSlots.HasContract(input.SlotID, input.ContractVersion) {
		err := fmt.Errorf("%w: exact provider contract is unavailable", ErrProviderSlotNotFound)
		return hostapi.ProtocolV2ProviderResult{}, &hostapi.ProtocolV2ProviderError{Reason: "host.provider_not_found", Err: err}
	}
	if !b.manager.hooks.providerSlots.HasCompiledSchemas(input.SlotID, input.ContractVersion) {
		err := fmt.Errorf("%w: exact provider schemas are unavailable", ErrProviderSlotInvalid)
		return hostapi.ProtocolV2ProviderResult{}, &hostapi.ProtocolV2ProviderError{Reason: "host.provider_request_invalid", Err: err}
	}
	result, err := b.manager.InvokeVersionedProvider(ctx, VersionedProviderInvocation{
		Caller: ProviderSlotCaller{
			ExtensionID: input.Caller.ExtensionID, ExtensionVersion: input.Caller.ExtensionVersion,
			ArtifactDigest: input.Caller.ArtifactDigest, RuntimeInstanceID: input.Caller.RuntimeInstanceID,
			Attested: true,
		},
		SlotID: input.SlotID, ContractVersion: input.ContractVersion, Operation: input.Operation,
		InputSchema: input.InputSchema, Input: input.Input, Revalidate: b.revalidate,
	})
	if err != nil {
		reason := "host.provider_invoke_failed"
		switch {
		case errors.Is(err, ErrProviderSlotInputInvalid), errors.Is(err, ErrProviderSlotInvalid):
			reason = "host.provider_request_invalid"
		case errors.Is(err, ErrProviderSlotOutputInvalid):
			reason = "host.provider_response_invalid"
		case errors.Is(err, ErrProviderSlotDenied):
			reason = "host.provider_caller_denied"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "host.provider_timeout"
		case errors.Is(err, context.Canceled):
			reason = "host.provider_cancelled"
		case errors.Is(err, ErrProviderSlotNotFound), errors.Is(err, ErrProviderSlotNoProvider):
			reason = "host.provider_not_found"
		}
		return hostapi.ProtocolV2ProviderResult{}, &hostapi.ProtocolV2ProviderError{Reason: reason, Err: err, Attempts: result.Attempts}
	}
	return hostapi.ProtocolV2ProviderResult{
		ProviderID: result.ProviderID, ProviderExtension: result.ExtensionID,
		RuntimeInstanceID: result.RuntimeInstanceID, ResponseSchema: result.ResponseSchema,
		Output: result.Output, Attempts: result.Attempts,
	}, nil
}

var _ hostapi.ProtocolV2ProviderBroker = (*ProtocolV2ProviderBroker)(nil)

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) ProtocolV2ProviderBroker() (hostapi.ProtocolV2ProviderBroker, error) {
	return m.eventsProviders.ProtocolV2ProviderBroker()
}
