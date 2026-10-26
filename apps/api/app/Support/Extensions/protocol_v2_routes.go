package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"golang.org/x/net/http/httpguts"
	"google.golang.org/protobuf/proto"
)

var (
	ErrProtocolV2RouteInvalid         = errors.New("protocol v2 route invocation is invalid")
	ErrProtocolV2RouteResponseInvalid = errors.New("protocol v2 route response is invalid")
	ErrProtocolV2RouteUnsupported     = errors.New("protocol v2 unary route is unsupported")
	ErrProtocolV2RouteStream          = errors.New("protocol v2 route requires the streaming transport")
)

// ProtocolV2InvocationActor is Host-authored request authority shared by route
// and admin invocations. Plugin output can never add actor or permission data.
type ProtocolV2InvocationActor struct {
	UserID         int64
	PermissionKeys []string
}

// ProtocolV2RouteActor preserves the route transport API name.
type ProtocolV2RouteActor = ProtocolV2InvocationActor

type ProtocolV2RouteInvocationStage string

const (
	ProtocolV2RouteInvocationStageHandler  ProtocolV2RouteInvocationStage = "handler"
	ProtocolV2RouteInvocationStageRequest  ProtocolV2RouteInvocationStage = "request"
	ProtocolV2RouteInvocationStageResponse ProtocolV2RouteInvocationStage = "response"
)

type ProtocolV2RoutePatchKind string

const (
	ProtocolV2RoutePatchAdd     ProtocolV2RoutePatchKind = "add"
	ProtocolV2RoutePatchReplace ProtocolV2RoutePatchKind = "replace"
	ProtocolV2RoutePatchRemove  ProtocolV2RoutePatchKind = "remove"
)

// ProtocolV2RoutePatchOperation is decoded transport data. Routes remains
// authoritative for applying the ordered operations to Host-owned documents.
type ProtocolV2RoutePatchOperation struct {
	Kind  ProtocolV2RoutePatchKind
	Path  string
	Value json.RawMessage
}

type ProtocolV2RouteResponseDocument struct {
	StatusCode  int
	Headers     http.Header
	Body        map[string]any
	BodyPresent bool
}

func protocolV2SchemaRef(reference string) (string, string, error) {
	reference = strings.TrimSpace(reference)
	index := strings.LastIndexByte(reference, '@')
	if index <= 0 || index == len(reference)-1 {
		return "", "", fmt.Errorf("protocol v2 schema reference %q is invalid", reference)
	}
	version := reference[index+1:]
	if version[0] == '0' {
		return "", "", fmt.Errorf("protocol v2 schema reference %q is invalid", reference)
	}
	for _, value := range version {
		if value < '0' || value > '9' {
			return "", "", fmt.Errorf("protocol v2 schema reference %q is invalid", reference)
		}
	}
	return reference[:index], version, nil
}

func protocolV2PatchSchemaRef(resultSchema string) (string, error) {
	schemaID, version, err := protocolV2SchemaRef(resultSchema)
	if err != nil {
		return "", err
	}
	return schemaID + ".patch@" + version, nil
}

func validateProtocolV2DocumentRef(document *protocolv2.TypedDocument, reference, label string) error {
	schemaID, version, err := protocolV2SchemaRef(reference)
	if err != nil {
		return err
	}
	if document == nil || document.GetSchemaId() != schemaID || document.GetSchemaVersion() != version {
		return fmt.Errorf("protocol v2 %s must match schema %q", label, reference)
	}
	return nil
}

func NewProtocolV2InvocationActor(userID int64, authenticated bool, permissions map[string]bool) *ProtocolV2InvocationActor {
	if !authenticated || userID <= 0 {
		return nil
	}
	keys := make([]string, 0, len(permissions))
	for key, allowed := range permissions {
		key = strings.TrimSpace(key)
		if allowed && key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return &ProtocolV2InvocationActor{UserID: userID, PermissionKeys: keys}
}

// NewProtocolV2RouteActor converts an authenticated Host dispatch snapshot to
// the wire actor. Anonymous and inactive actors are represented by nil.
func NewProtocolV2RouteActor(userID int64, authenticated bool, permissions map[string]bool) *ProtocolV2RouteActor {
	return NewProtocolV2InvocationActor(userID, authenticated, permissions)
}

// ProtocolV2RouteRequest is transport-neutral and binds one unary call to the
// immutable Route Registry step selected by the Host.
type ProtocolV2RouteRequest struct {
	RouteID               string
	ContractVersion       string
	RouteAction           string
	InvocationStage       ProtocolV2RouteInvocationStage
	Method                string
	Path                  string
	Headers               http.Header
	PathParameters        map[string]string
	QueryParameters       map[string]string
	QueryParameterValues  map[string][]string
	RequestSchema         string
	ResponseSchema        string
	MutableRequestFields  []string
	MutableResponseFields []string
	Body                  map[string]any
	BodyPresent           bool
	PriorResponse         *ProtocolV2RouteResponseDocument
	Authority             ProtocolV2RequestAuthority
	Actor                 *ProtocolV2RouteActor
	IdempotencyKey        string
	CorrelationID         string
	Timeout               time.Duration
}

type ProtocolV2RouteResponse struct {
	StatusCode    int
	Headers       http.Header
	Body          map[string]any
	BodyPresent   bool
	StreamFollows bool
	RequestPatch  []ProtocolV2RoutePatchOperation
	ResponsePatch []ProtocolV2RoutePatchOperation
}

type protocolV2RouteContextInvoker interface {
	InvokeRouteContext(context.Context, ProtocolV2RouteRequest) (ProtocolV2RouteResponse, error)
}

func (c *protocolV2Client) InvokeRouteContext(parent context.Context, input ProtocolV2RouteRequest) (ProtocolV2RouteResponse, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return ProtocolV2RouteResponse{}, ErrProtocolV2RouteInvalid
	}
	// Freeze the field authority for both the preflight and the response parser.
	input.MutableRequestFields = append([]string(nil), input.MutableRequestFields...)
	input.MutableResponseFields = append([]string(nil), input.MutableResponseFields...)
	if err := validateProtocolV2RouteRequest(input); err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	if err := c.validateFrozenRoute(input); err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	requestHeaders, err := protocolV2AuthorizedRequestHeaders(input.Headers, input.Authority)
	if err != nil {
		return ProtocolV2RouteResponse{}, wrapProtocolV2AuthorityError(ErrProtocolV2RouteInvalid, err)
	}
	authorityMode, guardKind, err := protocolV2WireRequestAuthority(input.Authority)
	if err != nil {
		return ProtocolV2RouteResponse{}, wrapProtocolV2AuthorityError(ErrProtocolV2RouteInvalid, err)
	}
	queryParameters, queryParameterValues, err := protocolV2RouteQueryParameters(
		input.QueryParameters, input.QueryParameterValues,
	)
	if err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = DefaultProtocolV2RequestTimeout
	}
	ctx, cancel := protocolV2Deadline(parent, timeout)
	defer cancel()

	requestContext, retireSessionDelegation, err := c.invocationRequestContext(
		ctx, input.CorrelationID, input.Actor, input.IdempotencyKey,
	)
	if err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	defer retireSessionDelegation()
	var body *protocolv2.TypedDocument
	if input.BodyPresent {
		schemaID, schemaVersion, err := protocolV2SchemaRef(input.RequestSchema)
		if err != nil {
			return ProtocolV2RouteResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2RouteInvalid, err)
		}
		body, err = protocolV2Document(schemaID, schemaVersion, input.Body)
		if err != nil {
			return ProtocolV2RouteResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2RouteInvalid, err)
		}
	}
	priorResponse, err := protocolV2WireRouteResponseDocument(input.PriorResponse, input.ResponseSchema, input.Authority)
	if err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	response, err := c.client.InvokeRoute(ctx, &pluginv2.RouteRequest{
		Context: requestContext, RouteId: input.RouteID, ContractVersion: input.ContractVersion,
		Method: input.Method, Path: input.Path, Headers: protocolV2RouteHeaders(requestHeaders),
		RequestAuthorityMode: authorityMode, GuardKind: guardKind, RouteAction: input.RouteAction,
		InvocationStage:       protocolV2WireRouteInvocationStage(input.InvocationStage),
		MutableRequestFields:  append([]string(nil), input.MutableRequestFields...),
		MutableResponseFields: append([]string(nil), input.MutableResponseFields...),
		PriorResponse:         priorResponse,
		PathParameters:        cloneProtocolV2RouteParameters(input.PathParameters),
		QueryParameters:       queryParameters, QueryParameterValues: queryParameterValues, Body: body,
	})
	if err != nil {
		return ProtocolV2RouteResponse{}, protocolV2OperationCause(ctx, err)
	}
	if err := validateProtocolV2RouteResponseContext(response.GetContext(), requestContext); err != nil {
		return ProtocolV2RouteResponse{}, errors.Join(ErrProtocolV2RouteResponseInvalid, err)
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	switch input.InvocationStage {
	case ProtocolV2RouteInvocationStageHandler:
		result, err := protocolV2RouteTerminalResponse(response, input.ResponseSchema)
		if err != nil {
			return ProtocolV2RouteResponse{}, errors.Join(ErrProtocolV2RouteResponseInvalid, err)
		}
		return result, nil
	case ProtocolV2RouteInvocationStageRequest:
		if protocolV2RouteResponseHasTerminal(response) || len(response.GetResponsePatch()) > 0 {
			return ProtocolV2RouteResponse{}, fmt.Errorf("%w: request-stage response contains terminal or response patch data", ErrProtocolV2RouteInvalid)
		}
		patch, err := protocolV2RoutePatchOperations(response.GetRequestPatch(), input.MutableRequestFields)
		if err != nil {
			return ProtocolV2RouteResponse{}, err
		}
		return ProtocolV2RouteResponse{RequestPatch: patch}, nil
	case ProtocolV2RouteInvocationStageResponse:
		if protocolV2RouteResponseHasTerminal(response) || len(response.GetRequestPatch()) > 0 {
			return ProtocolV2RouteResponse{}, fmt.Errorf("%w: response-stage response contains terminal or request patch data", ErrProtocolV2RouteInvalid)
		}
		patch, err := protocolV2RoutePatchOperations(response.GetResponsePatch(), input.MutableResponseFields)
		if err != nil {
			return ProtocolV2RouteResponse{}, err
		}
		return ProtocolV2RouteResponse{ResponsePatch: patch}, nil
	default:
		return ProtocolV2RouteResponse{}, ErrProtocolV2RouteInvalid
	}
}

func protocolV2RouteTerminalResponse(response *pluginv2.RouteResponse, responseSchema string) (ProtocolV2RouteResponse, error) {
	if len(response.GetRequestPatch()) > 0 || len(response.GetResponsePatch()) > 0 {
		return ProtocolV2RouteResponse{}, fmt.Errorf("%w: handler response cannot include patches", ErrProtocolV2RouteInvalid)
	}
	status := int(response.GetStatusCode())
	if !validProtocolV2TerminalStatus(status, response.GetStreamFollows()) {
		return ProtocolV2RouteResponse{}, fmt.Errorf("%w: invalid response status %d", ErrProtocolV2RouteInvalid, status)
	}
	if response.GetStreamFollows() && response.GetBody() != nil {
		return ProtocolV2RouteResponse{}, fmt.Errorf("%w: streaming response cannot include a buffered body", ErrProtocolV2RouteInvalid)
	}
	if response.GetStreamFollows() {
		// Stream routes carry opaque DataChunk bytes only. Unary preflight checks
		// status/headers; Host never validates chunk payloads against responseSchema.
		// Manifest responseSchema remains catalog/OpenAPI identity for non-http modes.
	} else if responseSchema == "" {
		if response.GetBody() != nil {
			return ProtocolV2RouteResponse{}, fmt.Errorf("%w: undeclared response body", ErrProtocolV2RouteInvalid)
		}
	} else if err := validateProtocolV2DocumentRef(response.GetBody(), responseSchema, "route response"); err != nil {
		return ProtocolV2RouteResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2RouteInvalid, err)
	}
	headers, err := protocolV2RouteHTTPHeaders(response.GetHeaders())
	if err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	result := ProtocolV2RouteResponse{
		StatusCode: status, Headers: headers, BodyPresent: response.GetBody() != nil,
		StreamFollows: response.GetStreamFollows(),
	}
	if result.BodyPresent {
		result.Body = protocolV2Values(response.GetBody())
	}
	return result, nil
}

func validateProtocolV2RouteRequest(input ProtocolV2RouteRequest) error {
	if strings.TrimSpace(input.RouteID) == "" || strings.TrimSpace(input.ContractVersion) == "" ||
		strings.TrimSpace(input.RouteAction) == "" || strings.TrimSpace(input.Method) == "" || !strings.HasPrefix(input.Path, "/") {
		return ErrProtocolV2RouteInvalid
	}
	if input.BodyPresent && strings.TrimSpace(input.RequestSchema) == "" {
		return fmt.Errorf("%w: typed request body requires a schema", ErrProtocolV2RouteInvalid)
	}
	if input.Actor != nil && input.Actor.UserID <= 0 {
		return fmt.Errorf("%w: authenticated actor id is invalid", ErrProtocolV2RouteInvalid)
	}
	if input.IdempotencyKey != "" && !validProtocolV2InvocationIdempotencyKey(input.IdempotencyKey) {
		return fmt.Errorf("%w: idempotency key is invalid", ErrProtocolV2RouteInvalid)
	}
	if err := validateProtocolV2RequestAuthority(input.Authority); err != nil {
		return wrapProtocolV2AuthorityError(ErrProtocolV2RouteInvalid, err)
	}
	if _, _, err := protocolV2RouteQueryParameters(input.QueryParameters, input.QueryParameterValues); err != nil {
		return err
	}
	switch input.InvocationStage {
	case ProtocolV2RouteInvocationStageHandler:
		if input.RouteAction != extensionmanifest.RouteActionAdd && input.RouteAction != extensionmanifest.RouteActionReplace ||
			input.PriorResponse != nil || len(input.MutableRequestFields) > 0 || len(input.MutableResponseFields) > 0 {
			return fmt.Errorf("%w: invalid handler-stage route action or mutation authority", ErrProtocolV2RouteInvalid)
		}
	case ProtocolV2RouteInvocationStageRequest:
		if !protocolV2RequestStageRouteAction(input.RouteAction) || input.PriorResponse != nil {
			return fmt.Errorf("%w: invalid request-stage route action or prior response", ErrProtocolV2RouteInvalid)
		}
	case ProtocolV2RouteInvocationStageResponse:
		if !protocolV2ResponseStageRouteAction(input.RouteAction) || input.PriorResponse == nil {
			return fmt.Errorf("%w: invalid response-stage route action or prior response", ErrProtocolV2RouteInvalid)
		}
		if err := validateProtocolV2PriorRouteResponse(input.PriorResponse, input.ResponseSchema); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: invalid route invocation stage", ErrProtocolV2RouteInvalid)
	}
	return nil
}

func (c *protocolV2Client) validateFrozenRoute(input ProtocolV2RouteRequest) error {
	for _, route := range c.routes {
		if route.ID != input.RouteID || route.ContractVersion != input.ContractVersion ||
			route.Action != input.RouteAction || route.RequestSchema != input.RequestSchema ||
			route.ResponseSchema != input.ResponseSchema ||
			!slices.Equal(route.MutableRequestFields, input.MutableRequestFields) ||
			!slices.Equal(route.MutableResponseFields, input.MutableResponseFields) {
			continue
		}
		if route.Action == extensionmanifest.RouteActionGlobalMiddleware && len(route.Methods) == 0 {
			if err := c.validateFrozenRouteAuthority(route, input.Authority); err != nil {
				return wrapProtocolV2AuthorityError(ErrProtocolV2RouteInvalid, err)
			}
			return nil
		}
		for _, method := range route.Methods {
			if method == "*" || strings.EqualFold(method, input.Method) ||
				route.Mode == extensionmanifest.RouteModeHTTP && strings.EqualFold(input.Method, http.MethodHead) && strings.EqualFold(method, http.MethodGet) {
				if err := c.validateFrozenRouteAuthority(route, input.Authority); err != nil {
					return wrapProtocolV2AuthorityError(ErrProtocolV2RouteInvalid, err)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("%w: route %q contract %q is not frozen for method %q", ErrProtocolV2RouteInvalid, input.RouteID, input.ContractVersion, input.Method)
}

func protocolV2RequestStageRouteAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionGlobalMiddleware, extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap:
		return true
	default:
		return false
	}
}

func protocolV2ResponseStageRouteAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap, extensionmanifest.RouteActionAfter:
		return true
	default:
		return false
	}
}

func validateProtocolV2PriorRouteResponse(document *ProtocolV2RouteResponseDocument, responseSchema string) error {
	if !validProtocolV2TerminalStatus(document.StatusCode, false) {
		return fmt.Errorf("%w: invalid prior response status %d", ErrProtocolV2RouteInvalid, document.StatusCode)
	}
	if _, err := protocolV2RouteHTTPHeaders(protocolV2RouteHeaders(document.Headers)); err != nil {
		return fmt.Errorf("%w: invalid prior response headers", ErrProtocolV2RouteInvalid)
	}
	if document.BodyPresent && strings.TrimSpace(responseSchema) == "" {
		return fmt.Errorf("%w: typed prior response body requires a schema", ErrProtocolV2RouteInvalid)
	}
	return nil
}

func validProtocolV2TerminalStatus(status int, allowSwitchingProtocols bool) bool {
	if allowSwitchingProtocols && status == http.StatusSwitchingProtocols {
		return true
	}
	return status >= http.StatusOK && status <= 599
}

func protocolV2WireRouteInvocationStage(stage ProtocolV2RouteInvocationStage) pluginv2.RouteInvocationStage {
	switch stage {
	case ProtocolV2RouteInvocationStageHandler:
		return pluginv2.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER
	case ProtocolV2RouteInvocationStageRequest:
		return pluginv2.RouteInvocationStage_ROUTE_INVOCATION_STAGE_REQUEST
	case ProtocolV2RouteInvocationStageResponse:
		return pluginv2.RouteInvocationStage_ROUTE_INVOCATION_STAGE_RESPONSE
	default:
		return pluginv2.RouteInvocationStage_ROUTE_INVOCATION_STAGE_UNSPECIFIED
	}
}

func protocolV2WireRouteResponseDocument(
	document *ProtocolV2RouteResponseDocument,
	responseSchema string,
	authority ProtocolV2RequestAuthority,
) (*pluginv2.RouteResponseDocument, error) {
	if document == nil {
		return nil, nil
	}
	var body *protocolv2.TypedDocument
	if document.BodyPresent {
		schemaID, schemaVersion, err := protocolV2SchemaRef(responseSchema)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtocolV2RouteInvalid, err)
		}
		body, err = protocolV2Document(schemaID, schemaVersion, document.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtocolV2RouteInvalid, err)
		}
	}
	headers, err := protocolV2AuthorizedPriorResponseHeaders(document.Headers, authority)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid prior response headers", ErrProtocolV2RouteInvalid)
	}
	return &pluginv2.RouteResponseDocument{
		StatusCode: uint32(document.StatusCode), Headers: protocolV2RouteHeaders(headers), Body: body,
	}, nil
}

func protocolV2AuthorizedPriorResponseHeaders(
	source http.Header,
	authority ProtocolV2RequestAuthority,
) (http.Header, error) {
	if err := validateProtocolV2RequestAuthority(authority); err != nil {
		return nil, err
	}
	connectionHeaders := protocolV2ConnectionHeaderTokens(source)
	result := make(http.Header)
	for name, values := range source {
		canonical := strings.ToLower(strings.TrimSpace(name))
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, ErrProtocolV2RequestAuthorityInvalid
		}
		if strings.HasPrefix(canonical, "x-sforum-") || protocolV2PriorResponseHeaderBlocked(canonical, connectionHeaders) {
			continue
		}
		if protocolV2PriorResponseCredentialHeader(canonical) && authority.Mode != ProtocolV2RequestAuthorityRaw {
			continue
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return nil, ErrProtocolV2RequestAuthorityInvalid
			}
			result.Add(name, value)
		}
	}
	return result, nil
}

func protocolV2PriorResponseCredentialHeader(canonical string) bool {
	return canonical == "set-cookie" || protocolV2RequestCredentialHeader(canonical)
}

func protocolV2PriorResponseHeaderBlocked(canonical string, connectionHeaders map[string]struct{}) bool {
	if _, blocked := connectionHeaders[canonical]; blocked {
		return true
	}
	switch canonical {
	case "", "content-length", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"proxy-connection", "x-csrf-token", "idempotency-replayed", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func protocolV2RouteResponseHasTerminal(response *pluginv2.RouteResponse) bool {
	return response.GetStatusCode() != 0 || len(response.GetHeaders()) > 0 ||
		response.GetBody() != nil || response.GetStreamFollows()
}

func protocolV2RoutePatchOperations(
	operations []*pluginv2.RoutePatchOperation,
	allowlist []string,
) ([]ProtocolV2RoutePatchOperation, error) {
	if len(operations) > extensionmanifest.RouteMutableFieldsMaximumCount {
		return nil, fmt.Errorf("%w: route patch exceeds the operation limit", ErrProtocolV2RouteInvalid)
	}
	result := make([]ProtocolV2RoutePatchOperation, 0, len(operations))
	for _, operation := range operations {
		if operation == nil || !slices.Contains(allowlist, operation.GetPath()) {
			return nil, fmt.Errorf("%w: route patch path is outside the frozen allowlist", ErrProtocolV2RouteInvalid)
		}
		if operation.GetValue() != nil {
			return nil, fmt.Errorf("%w: legacy route patch value is forbidden", ErrProtocolV2RouteInvalid)
		}
		item := ProtocolV2RoutePatchOperation{Path: operation.GetPath()}
		switch operation.GetKind() {
		case pluginv2.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD:
			item.Kind = ProtocolV2RoutePatchAdd
		case pluginv2.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REPLACE:
			item.Kind = ProtocolV2RoutePatchReplace
		case pluginv2.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REMOVE:
			item.Kind = ProtocolV2RoutePatchRemove
			if len(operation.GetValueJson()) != 0 {
				return nil, fmt.Errorf("%w: remove patch cannot include a value", ErrProtocolV2RouteInvalid)
			}
			result = append(result, item)
			continue
		default:
			return nil, fmt.Errorf("%w: unsupported route patch operation", ErrProtocolV2RouteInvalid)
		}
		if len(operation.GetValueJson()) == 0 || !json.Valid(operation.GetValueJson()) {
			return nil, fmt.Errorf("%w: add and replace patches require a value", ErrProtocolV2RouteInvalid)
		}
		// Copy the exact JSON bytes. Converting through protobuf Value or interface{}
		// would round integers above 2^53 and leak transport representation details.
		item.Value = append(json.RawMessage(nil), operation.GetValueJson()...)
		result = append(result, item)
	}
	return result, nil
}

func cloneProtocolV2Routes(values []extensions.ManifestRoute) []extensions.ManifestRoute {
	result := make([]extensions.ManifestRoute, len(values))
	copy(result, values)
	for index := range result {
		result[index].Methods = append([]string(nil), values[index].Methods...)
		result[index].MutableRequestFields = append([]string(nil), values[index].MutableRequestFields...)
		result[index].MutableResponseFields = append([]string(nil), values[index].MutableResponseFields...)
	}
	return result
}

func validateProtocolV2RouteResponseContext(response *protocolv2.ResponseContext, request *protocolv2.RequestContext) error {
	if response == nil || request == nil || response.GetRequestId() != request.GetRequestId() ||
		!proto.Equal(response.GetTrace(), request.GetTrace()) || !proto.Equal(response.GetExtension(), request.GetExtension()) {
		return fmt.Errorf("%w: response context does not match the exact runtime request", ErrProtocolV2RouteInvalid)
	}
	if response.GetServerTime() == nil || !response.GetServerTime().IsValid() {
		return fmt.Errorf("%w: response server time is invalid", ErrProtocolV2RouteInvalid)
	}
	return nil
}

func protocolV2RouteHeaders(headers http.Header) []*protocolv2.Header {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]*protocolv2.Header, 0, len(names))
	for _, name := range names {
		result = append(result, &protocolv2.Header{Name: name, Values: append([]string(nil), headers.Values(name)...)})
	}
	return result
}

func protocolV2RouteHTTPHeaders(headers []*protocolv2.Header) (http.Header, error) {
	result := make(http.Header)
	for _, header := range headers {
		if header == nil || !httpguts.ValidHeaderFieldName(header.GetName()) {
			return nil, fmt.Errorf("%w: invalid response header name", ErrProtocolV2RouteInvalid)
		}
		for _, value := range header.GetValues() {
			if !httpguts.ValidHeaderFieldValue(value) {
				return nil, fmt.Errorf("%w: invalid response header value", ErrProtocolV2RouteInvalid)
			}
			result.Add(header.GetName(), value)
		}
	}
	return result, nil
}

func cloneProtocolV2RouteParameters(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func protocolV2RouteQueryParameters(
	legacy map[string]string,
	all map[string][]string,
) (map[string]string, []*pluginv2.RouteQueryParameter, error) {
	valuesByKey := make(map[string][]string, len(legacy)+len(all))
	for key, values := range all {
		if len(values) == 0 {
			return nil, nil, fmt.Errorf("%w: query key %q has no values", ErrProtocolV2RouteInvalid, key)
		}
		valuesByKey[key] = append([]string(nil), values...)
	}
	for key, value := range legacy {
		values, exists := valuesByKey[key]
		if exists {
			if values[0] != value {
				return nil, nil, fmt.Errorf("%w: query key %q has conflicting legacy and repeated values", ErrProtocolV2RouteInvalid, key)
			}
			continue
		}
		valuesByKey[key] = []string{value}
	}
	if len(valuesByKey) == 0 {
		return nil, nil, nil
	}

	keys := make([]string, 0, len(valuesByKey))
	for key := range valuesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	legacyResult := make(map[string]string, len(keys))
	result := make([]*pluginv2.RouteQueryParameter, 0, len(keys))
	for _, key := range keys {
		values := valuesByKey[key]
		legacyResult[key] = values[0]
		result = append(result, &pluginv2.RouteQueryParameter{Key: key, Values: append([]string(nil), values...)})
	}
	return legacyResult, result, nil
}

var _ protocolV2RouteContextInvoker = (*protocolV2Client)(nil)
