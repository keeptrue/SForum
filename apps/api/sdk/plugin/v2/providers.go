package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var (
	ErrInvalidProviderDefinition = errors.New("invalid plugin provider definition")
	// ErrProviderOperationRejected 表示非 invoke 的 versioned provider 操作。
	ErrProviderOperationRejected = errors.New("plugin provider operation is not invoke")

	providerIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	providerSlotPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	providerSchemaPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

// VersionedProviderOperationInvoke 是 versioned ManifestProvider 唯一合法 operation。
// 与 Host InvokeVersionedProvider / provider_slot_execution 一致。
// 遗留 known-slot probe/send 必须覆盖生成的 ProviderCall RPC 或使用兼容 API，
// 不得经本 typed ProviderRegistry 伪装。
const VersionedProviderOperationInvoke = "invoke"

// ProviderDefinition 对齐可执行 versioned ManifestProvider。
// 被动/目录-only provider 不得注册到本 runtime registry。
type ProviderDefinition struct {
	ID              string
	Slot            string
	ContractVersion string
	TargetID        string
	Label           string
	Handler         string
	RequestSchema   string
	ResponseSchema  string
	Fallback        string
	Priority        int
	TimeoutMS       int
	// Execute 是作者侧业务 handler（非 Manifest 字段）。
	Execute ProviderHandler
}

// ProviderCall 是校验后的 ProviderCall 业务输入（operation 恒为 invoke）。
type ProviderCall struct {
	Context         *protocolwire.RequestContext
	ID              string
	Slot            string
	ContractVersion string
	Operation       string
	Input           *protocolwire.TypedDocument
}

type ProviderHandler func(context.Context, *ProviderCall) (*protocolwire.TypedDocument, error)

type registeredProvider struct {
	definition ProviderDefinition
}

// ProviderRegistry 分发 Host → plugin 的 versioned ProviderCall（仅 invoke）。
type ProviderRegistry struct {
	byID  map[string]registeredProvider
	order []ProviderDefinition
}

func NewProviderRegistry(definitions ...ProviderDefinition) (*ProviderRegistry, error) {
	registry := &ProviderRegistry{byID: make(map[string]registeredProvider, len(definitions))}
	for _, definition := range definitions {
		prepared, err := prepareProviderDefinition(definition)
		if err != nil {
			return nil, err
		}
		if _, exists := registry.byID[prepared.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate provider id %q", ErrInvalidProviderDefinition, prepared.ID)
		}
		registry.byID[prepared.ID] = registeredProvider{definition: prepared}
		registry.order = append(registry.order, prepared)
	}
	sort.Slice(registry.order, func(i, j int) bool {
		left, right := registry.order[i], registry.order[j]
		if left.Slot != right.Slot {
			return left.Slot < right.Slot
		}
		if left.ContractVersion != right.ContractVersion {
			return left.ContractVersion < right.ContractVersion
		}
		return left.ID < right.ID
	})
	return registry, nil
}

func (r *ProviderRegistry) Definitions() []ProviderDefinition {
	if r == nil {
		return nil
	}
	out := make([]ProviderDefinition, len(r.order))
	for i, item := range r.order {
		copyDef := item
		copyDef.Execute = nil
		out[i] = copyDef
	}
	return out
}

func (r *ProviderRegistry) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: responseContext(providerRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	output, err := definition.Execute(handlerCtx, &ProviderCall{
		Context: cloneRequestContext(request.GetContext()),
		ID:      definition.ID, Slot: definition.Slot, ContractVersion: definition.ContractVersion,
		Operation: VersionedProviderOperationInvoke, Input: cloneTypedDocument(request.GetInput()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "provider.handler_failed", "Plugin provider handler failed.")
		return response, nil
	}
	if schemaErr := validateBoundDocument(output, definition.ResponseSchema, "provider", "output"); schemaErr != nil {
		response.Error = familyErrorDetail(schemaErr, "provider.handler_failed", "Plugin provider handler failed.")
		return response, nil
	}
	response.Output = cloneTypedDocument(output)
	return response, nil
}

func prepareProviderDefinition(definition ProviderDefinition) (ProviderDefinition, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Slot = strings.TrimSpace(definition.Slot)
	definition.ContractVersion = strings.TrimSpace(definition.ContractVersion)
	definition.TargetID = strings.TrimSpace(definition.TargetID)
	definition.Label = strings.TrimSpace(definition.Label)
	definition.Handler = strings.TrimSpace(definition.Handler)
	definition.RequestSchema = strings.TrimSpace(definition.RequestSchema)
	definition.ResponseSchema = strings.TrimSpace(definition.ResponseSchema)
	definition.Fallback = strings.ToLower(strings.TrimSpace(definition.Fallback))
	if !providerIDPattern.MatchString(definition.ID) {
		return ProviderDefinition{}, fmt.Errorf("%w: invalid provider id %q", ErrInvalidProviderDefinition, definition.ID)
	}
	if !providerSlotPattern.MatchString(definition.Slot) {
		return ProviderDefinition{}, fmt.Errorf("%w: invalid provider slot %q", ErrInvalidProviderDefinition, definition.Slot)
	}
	if definition.Slot == IdentityRuntimeProviderSlot {
		return ProviderDefinition{}, fmt.Errorf("%w: provider slot %q is reserved", ErrInvalidProviderDefinition, definition.Slot)
	}
	if !validContractVersion(definition.ContractVersion) {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q contract version must be id@positiveVersion", ErrInvalidProviderDefinition, definition.ID)
	}
	if definition.Label == "" {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q requires label", ErrInvalidProviderDefinition, definition.ID)
	}
	// 可执行 registry：必须具备 Manifest handler（被动目录项不得注册）。
	if !validManifestHandler(definition.Handler) {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q requires executable handler", ErrInvalidProviderDefinition, definition.ID)
	}
	if definition.TargetID != "" && !providerIDPattern.MatchString(definition.TargetID) {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q has invalid target id %q", ErrInvalidProviderDefinition, definition.ID, definition.TargetID)
	}
	if !providerSchemaPattern.MatchString(definition.RequestSchema) || !providerSchemaPattern.MatchString(definition.ResponseSchema) {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q requires versioned request/response schemas", ErrInvalidProviderDefinition, definition.ID)
	}
	// 与 Manifest V3 normalize 对齐：versioned provider 默认 fallback/timeout。
	if definition.Fallback == "" {
		definition.Fallback = "next"
	}
	if definition.Fallback != "next" && definition.Fallback != "closed" {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q has invalid fallback %q", ErrInvalidProviderDefinition, definition.ID, definition.Fallback)
	}
	if definition.TimeoutMS == 0 {
		definition.TimeoutMS = extensionmanifest.ProviderSlotMaximumTimeoutMS
	}
	if definition.TimeoutMS <= 0 || definition.TimeoutMS > extensionmanifest.ProviderSlotMaximumTimeoutMS {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q timeout must be 1..%d",
			ErrInvalidProviderDefinition, definition.ID, extensionmanifest.ProviderSlotMaximumTimeoutMS)
	}
	if definition.Execute == nil {
		return ProviderDefinition{}, fmt.Errorf("%w: provider %q has no execute handler", ErrInvalidProviderDefinition, definition.ID)
	}
	return definition, nil
}

func (r *ProviderRegistry) resolve(request *pluginwire.ProviderCallRequest) (ProviderDefinition, *protocolwire.ErrorDetail) {
	if r == nil {
		return ProviderDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason: "provider.registry_unavailable", Message: "Plugin provider registry is unavailable.",
		}
	}
	if request == nil {
		return ProviderDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "provider.request_required", Message: "A provider call request is required.",
		}
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "provider"); detail != nil {
		return ProviderDefinition{}, detail
	}
	// versioned ManifestProvider 仅接受 invoke；probe/send 属遗留兼容路径。
	operation := strings.TrimSpace(request.GetOperation())
	if operation != VersionedProviderOperationInvoke {
		return ProviderDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "provider.operation_not_invoke",
			Message: "Versioned ProviderRegistry accepts only operation \"invoke\". " +
				"Legacy probe/send must override ProviderCall or use provider-specific compatibility APIs.",
		}
	}
	slot := strings.TrimSpace(request.GetSlotId())
	contract := strings.TrimSpace(request.GetContractVersion())
	declarationID := strings.TrimSpace(request.GetDeclarationId())
	var definition ProviderDefinition
	if declarationID != "" {
		registered, ok := r.byID[declarationID]
		if !ok {
			return ProviderDefinition{}, &protocolwire.ErrorDetail{
				Code:   protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
				Reason: "provider.not_found", Message: "The requested provider declaration is not registered.",
			}
		}
		definition = registered.definition
		if definition.Slot != slot || definition.ContractVersion != contract {
			return ProviderDefinition{}, &protocolwire.ErrorDetail{
				Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				Reason: "provider.identity_mismatch", Message: "Provider slot/contract does not match the registered declaration.",
			}
		}
	} else {
		var match *ProviderDefinition
		for i := range r.order {
			candidate := &r.order[i]
			if candidate.Slot == slot && candidate.ContractVersion == contract {
				if match != nil {
					return ProviderDefinition{}, &protocolwire.ErrorDetail{
						Code:   protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
						Reason: "provider.declaration_ambiguous", Message: "Multiple provider declarations match; declaration_id is required.",
					}
				}
				match = candidate
			}
		}
		if match == nil {
			return ProviderDefinition{}, &protocolwire.ErrorDetail{
				Code:   protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
				Reason: "provider.not_found", Message: "The requested provider slot/contract is not registered.",
			}
		}
		definition = *match
	}
	if err := validateBoundDocument(request.GetInput(), definition.RequestSchema, "provider", "input"); err != nil {
		return ProviderDefinition{}, familyErrorDetail(err, "provider.schema_mismatch", "Provider input schema mismatch.")
	}
	return definition, nil
}

func providerRequestContext(request *pluginwire.ProviderCallRequest) *protocolwire.RequestContext {
	if request == nil {
		return nil
	}
	return request.GetContext()
}
