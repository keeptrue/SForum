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

const (
	IdentityRuntimeProviderSlot    = "sforum.identity"
	IdentityRuntimeFeatureName     = "identity.runtime"
	IdentityRuntimeFeatureVersion  = "1"
	IdentityRuntimeFeatureContract = IdentityRuntimeFeatureName + "@" + IdentityRuntimeFeatureVersion
)

var (
	ErrInvalidIdentityProviderDefinition = errors.New("invalid plugin identity provider definition")
	identityProviderIDPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
)

// IdentityRuntimeProtocolFeature returns the independently negotiated
// identity.runtime@1 feature declaration.
func IdentityRuntimeProtocolFeature() *protocolwire.ProtocolFeature {
	return &protocolwire.ProtocolFeature{Name: IdentityRuntimeFeatureName, Version: IdentityRuntimeFeatureVersion}
}

// IdentityProviderOperationDefinition binds one executable operation to the
// canonical id@version Schema references carried by TypedDocument.
type IdentityProviderOperationDefinition struct {
	Name          string
	InputSchema   string
	OutputSchema  string
	TimeoutMS     int
	FailurePolicy string
}

// IdentityProviderDefinition mirrors one executable Manifest identity
// provider. Inspect-only providers with no operations do not belong here.
type IdentityProviderDefinition struct {
	ID              string
	ContractVersion string
	Kind            string
	Handler         string
	Priority        int
	Operations      []IdentityProviderOperationDefinition
	Execute         IdentityProviderHandler
}

// IdentityProviderCall is the validated Host-to-plugin business input. Host
// identity effects and final authorization remain outside this handler.
type IdentityProviderCall struct {
	Context         *protocolwire.RequestContext
	ID              string
	ContractVersion string
	Kind            string
	Handler         string
	Operation       string
	Input           *protocolwire.TypedDocument
}

type IdentityProviderHandler func(context.Context, *IdentityProviderCall) (*protocolwire.TypedDocument, error)

type registeredIdentityProvider struct {
	definition IdentityProviderDefinition
	operations map[string]IdentityProviderOperationDefinition
}

// IdentityProviderRegistry owns the reserved sforum.identity namespace. It is
// intentionally independent from the public invoke-only ProviderRegistry.
type IdentityProviderRegistry struct {
	byID  map[string]registeredIdentityProvider
	order []IdentityProviderDefinition
}

func NewIdentityProviderRegistry(definitions ...IdentityProviderDefinition) (*IdentityProviderRegistry, error) {
	registry := &IdentityProviderRegistry{byID: make(map[string]registeredIdentityProvider, len(definitions))}
	for _, input := range definitions {
		definition, operations, err := prepareIdentityProviderDefinition(input)
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.byID[definition.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate provider id %q", ErrInvalidIdentityProviderDefinition, definition.ID)
		}
		registry.byID[definition.ID] = registeredIdentityProvider{definition: definition, operations: operations}
		registry.order = append(registry.order, definition)
	}
	sort.Slice(registry.order, func(i, j int) bool { return registry.order[i].ID < registry.order[j].ID })
	return registry, nil
}

func (r *IdentityProviderRegistry) Definitions() []IdentityProviderDefinition {
	if r == nil {
		return nil
	}
	result := make([]IdentityProviderDefinition, len(r.order))
	for index, definition := range r.order {
		result[index] = cloneIdentityProviderDefinition(definition)
		result[index].Execute = nil
	}
	return result
}

func (r *IdentityProviderRegistry) ProviderCall(
	ctx context.Context,
	request *pluginwire.ProviderCallRequest,
) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: responseContext(providerRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider, operation, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	output, err := provider.Execute(handlerCtx, &IdentityProviderCall{
		Context: cloneRequestContext(request.GetContext()), ID: provider.ID,
		ContractVersion: provider.ContractVersion, Kind: provider.Kind, Handler: provider.Handler,
		Operation: operation.Name, Input: cloneTypedDocument(request.GetInput()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "identity_provider.handler_failed", "Plugin identity provider handler failed.")
		return response, nil
	}
	if err := validateBoundDocument(output, operation.OutputSchema, "identity_provider", "output"); err != nil {
		response.Error = familyErrorDetail(err, "identity_provider.output_invalid", "Plugin identity provider output is invalid.")
		return response, nil
	}
	response.Output = cloneTypedDocument(output)
	return response, nil
}

func (r *IdentityProviderRegistry) resolve(
	request *pluginwire.ProviderCallRequest,
) (IdentityProviderDefinition, IdentityProviderOperationDefinition, *protocolwire.ErrorDetail) {
	invalid := func(reason, message string) (IdentityProviderDefinition, IdentityProviderOperationDefinition, *protocolwire.ErrorDetail) {
		return IdentityProviderDefinition{}, IdentityProviderOperationDefinition{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason, Message: message,
		}
	}
	if r == nil || request == nil {
		return invalid("identity_provider.request_required", "An identity provider call request is required.")
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "identity_provider"); detail != nil {
		return IdentityProviderDefinition{}, IdentityProviderOperationDefinition{}, detail
	}
	if request.GetSlotId() != IdentityRuntimeProviderSlot {
		return invalid("identity_provider.slot_mismatch", "Identity providers require the reserved sforum.identity slot.")
	}
	if detail := validateIdentityProviderContext(request.GetContext()); detail != nil {
		return IdentityProviderDefinition{}, IdentityProviderOperationDefinition{}, detail
	}
	providerID := strings.TrimSpace(request.GetDeclarationId())
	registered, found := r.byID[providerID]
	if !found {
		return IdentityProviderDefinition{}, IdentityProviderOperationDefinition{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "identity_provider.not_found",
			Message: "The requested identity provider declaration is not registered.",
		}
	}
	if request.GetContractVersion() != registered.definition.ContractVersion {
		return invalid("identity_provider.contract_mismatch", "Identity provider contract version mismatch.")
	}
	operation, found := registered.operations[strings.TrimSpace(request.GetOperation())]
	if !found {
		return invalid("identity_provider.operation_not_declared", "Identity provider operation is not registered.")
	}
	if err := validateBoundDocument(request.GetInput(), operation.InputSchema, "identity_provider", "input"); err != nil {
		return IdentityProviderDefinition{}, IdentityProviderOperationDefinition{}, familyErrorDetail(
			err, "identity_provider.input_invalid", "Identity provider input is invalid.",
		)
	}
	return registered.definition, operation, nil
}

func prepareIdentityProviderDefinition(
	input IdentityProviderDefinition,
) (IdentityProviderDefinition, map[string]IdentityProviderOperationDefinition, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Handler = strings.TrimSpace(input.Handler)
	if !identityProviderIDPattern.MatchString(input.ID) || !validContractVersion(input.ContractVersion) ||
		!validIdentityProviderKind(input.Kind) || !validManifestHandler(input.Handler) || input.Execute == nil ||
		len(input.Operations) == 0 || len(input.Operations) > extensionmanifest.ManifestIdentityProviderMaximumOperations {
		return IdentityProviderDefinition{}, nil, ErrInvalidIdentityProviderDefinition
	}
	operations := make(map[string]IdentityProviderOperationDefinition, len(input.Operations))
	prepared := make([]IdentityProviderOperationDefinition, 0, len(input.Operations))
	for _, operation := range input.Operations {
		operation.Name = strings.ToLower(strings.TrimSpace(operation.Name))
		operation.InputSchema = strings.TrimSpace(operation.InputSchema)
		operation.OutputSchema = strings.TrimSpace(operation.OutputSchema)
		operation.FailurePolicy = strings.ToLower(strings.TrimSpace(operation.FailurePolicy))
		expectedPolicy, known := identityProviderOperationPolicy(input.Kind, operation.Name)
		if !known || !validContractVersion(operation.InputSchema) || !validContractVersion(operation.OutputSchema) ||
			operation.TimeoutMS <= 0 || operation.TimeoutMS > extensionmanifest.ManifestIdentityProviderMaximumTimeoutMS ||
			operation.FailurePolicy != expectedPolicy {
			return IdentityProviderDefinition{}, nil, ErrInvalidIdentityProviderDefinition
		}
		if _, duplicate := operations[operation.Name]; duplicate {
			return IdentityProviderDefinition{}, nil, ErrInvalidIdentityProviderDefinition
		}
		operations[operation.Name] = operation
		prepared = append(prepared, operation)
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Name < prepared[j].Name })
	input.Operations = prepared
	return input, operations, nil
}

func validateIdentityProviderContext(request *protocolwire.RequestContext) *protocolwire.ErrorDetail {
	if len(request.GetGrantedAuthority()) != 0 || request.GetIdempotencyKey() != "" ||
		len(request.GetHostCommandDelegations()) != 0 || len(request.GetHostQueryDelegations()) != 0 ||
		request.GetHostSessionDelegation() != nil {
		return &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Reason: "identity_provider.context_authority_forbidden",
			Message: "Identity provider calls cannot carry subprocess authority, idempotency, or delegation material.",
		}
	}
	actor := request.GetActor()
	if actor != nil && (actor.GetUserId() <= 0 || actor.GetSessionId() != "" || actor.GetClientIp() != "" ||
		actor.GetUserAgent() != "" || len(actor.GetRoleIds()) != 0 || len(actor.GetPermissionKeys()) != 0) {
		return &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Reason: "identity_provider.context_actor_unsafe",
			Message: "Identity provider calls accept only a safe actor projection.",
		}
	}
	return nil
}

func validIdentityProviderKind(value string) bool {
	switch value {
	case "auth", "profile", "recovery", "session", "risk":
		return true
	default:
		return false
	}
}

func identityProviderOperationPolicy(kind, operation string) (string, bool) {
	switch kind + ":" + operation {
	case "profile:sections.list", "profile:section.read":
		return extensionmanifest.IdentityProviderFailureOmit, true
	case "auth:registration.start", "auth:registration.complete",
		"auth:login.start", "auth:login.complete", "auth:link.start", "auth:link.complete",
		// provider.probe：有界配置/可达性探测；fail_closed。
		"auth:provider.probe",
		"profile:section.update", "profile:account.read", "profile:account.update",
		"recovery:recovery.start", "recovery:recovery.complete",
		"session:session.evaluate", "risk:risk.evaluate":
		return extensionmanifest.IdentityProviderFailureFailClosed, true
	default:
		return "", false
	}
}

func cloneIdentityProviderDefinition(input IdentityProviderDefinition) IdentityProviderDefinition {
	input.Operations = append([]IdentityProviderOperationDefinition(nil), input.Operations...)
	return input
}

func hasExactIdentityRuntimeFeature(features []*protocolwire.ProtocolFeature) bool {
	for _, feature := range features {
		if feature.GetName() == IdentityRuntimeFeatureName && feature.GetVersion() == IdentityRuntimeFeatureVersion {
			return true
		}
	}
	return false
}
