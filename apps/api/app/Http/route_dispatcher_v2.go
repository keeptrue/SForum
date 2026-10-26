package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type exactRouteV2Runtime interface {
	InvokeRouteInstance(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.ProtocolV2RouteRequest) (extensionsruntime.ProtocolV2RouteResponse, error)
}

func (i *BufferedRouteStepInvoker) invokeProtocolV2(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	input routes.RouteInvocation,
	authority routes.ResolvedRequestAuthority,
) (routes.RouteInvocationResult, error) {
	runtime, ok := i.Runtime.(exactRouteV2Runtime)
	if !ok {
		return routes.RouteInvocationResult{}, ErrRouteRuntimeTarget
	}
	query, queryValues, err := exactProtocolV2RouteQuery(input.Request.Query)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	body, present, err := exactProtocolV2RouteRequestBody(input)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	headers := make(stdhttp.Header)
	if err := copyRouteRequestHeaders(headers, input.Request.Headers, authority); err != nil {
		return routes.RouteInvocationResult{}, err
	}
	wireAuthority, err := protocolV2RequestAuthority(authority)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	idempotencyKey, err := exactProtocolV2RouteIdempotencyKey(input.Request.Headers)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	stage, err := exactProtocolV2RouteInvocationStage(input.Stage)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	priorResponse, err := exactProtocolV2PriorRouteResponse(input)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	evidence := &routeTransportEvidence{commit: input.Commit}
	// A unary gRPC error cannot prove that the plugin did not receive the call.
	// Fence fallback before dispatch so a crash cannot create a second writer.
	evidence.markRequestStarted()
	response, err := runtime.InvokeRouteInstance(ctx, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID: input.Step.RouteID, ContractVersion: input.Step.ContractVersion,
		RouteAction: input.Step.Action, InvocationStage: stage,
		Method: input.Request.Method, Path: input.Request.Path, Headers: headers,
		PathParameters: input.Request.Params, QueryParameters: query, QueryParameterValues: queryValues,
		RequestSchema: input.Step.RequestSchema, ResponseSchema: input.Step.ResponseSchema,
		MutableRequestFields: input.Step.MutableRequestFields, MutableResponseFields: input.Step.MutableResponseFields,
		Body: body, BodyPresent: present, PriorResponse: priorResponse,
		Authority: wireAuthority,
		Actor: extensionsruntime.NewProtocolV2RouteActor(
			input.Request.ActorID, input.Request.Authenticated, input.Request.Permissions,
		),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return evidence.result(), err
	}
	if err := validateProtocolV2BridgeResponse(input.Stage, response); err != nil {
		return evidence.result(), err
	}
	result := evidence.result()
	switch input.Stage {
	case routes.InvocationStageRequest:
		result.RequestPatch, err = protocolV2RoutePatchOperations(response.RequestPatch)
		return result, err
	case routes.InvocationStageResponse:
		result.ResponsePatch, err = protocolV2RoutePatchOperations(response.ResponsePatch)
		return result, err
	case routes.InvocationStageHandler:
	default:
		return result, ErrRouteRuntimeTarget
	}
	if response.StreamFollows {
		return result, fmt.Errorf("%w: buffered route returned a stream preflight", ErrRouteRuntimeTarget)
	}
	evidence.markResponseStarted()
	var responseBody []byte
	if response.BodyPresent {
		responseBody, err = json.Marshal(response.Body)
		if err != nil {
			return evidence.result(), fmt.Errorf("%w: encode typed route response: %v", ErrRouteRuntimeTarget, err)
		}
	}
	limit := i.ResponseLimit
	if limit <= 0 {
		limit = defaultRouteResponseLimit
	}
	if int64(len(responseBody)) > limit {
		return evidence.result(), ErrRouteResponseTooLarge
	}
	value := routes.DispatchResponse{
		Status: response.StatusCode, Headers: filteredRouteResponseHeaders(response.Headers), Body: responseBody,
	}
	result = evidence.result()
	result.Response = &value
	return result, nil
}

func validateProtocolV2BridgeResponse(
	stage routes.InvocationStage,
	response extensionsruntime.ProtocolV2RouteResponse,
) error {
	hasTerminal := response.StatusCode != 0 || len(response.Headers) != 0 || response.Body != nil ||
		response.BodyPresent || response.StreamFollows
	switch stage {
	case routes.InvocationStageRequest:
		if hasTerminal || len(response.ResponsePatch) != 0 {
			return fmt.Errorf("%w: request stage returned terminal or response patch data", ErrRouteRuntimeTarget)
		}
	case routes.InvocationStageResponse:
		if hasTerminal || len(response.RequestPatch) != 0 {
			return fmt.Errorf("%w: response stage returned terminal or request patch data", ErrRouteRuntimeTarget)
		}
	case routes.InvocationStageHandler:
		if len(response.RequestPatch) != 0 || len(response.ResponsePatch) != 0 ||
			!routes.ValidTerminalResponseStatus(extensionmanifest.RouteModeHTTP, response.StatusCode) ||
			response.BodyPresent != (response.Body != nil) {
			return fmt.Errorf("%w: handler stage returned an invalid terminal response", ErrRouteRuntimeTarget)
		}
	default:
		return ErrRouteRuntimeTarget
	}
	return nil
}

func exactProtocolV2RouteIdempotencyKey(headers stdhttp.Header) (string, error) {
	values := headers.Values("Idempotency-Key")
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return values[0], nil
	default:
		return "", fmt.Errorf("%w: exactly one Idempotency-Key is allowed", ErrRouteRuntimeTarget)
	}
}

func exactProtocolV2RouteQuery(raw string) (map[string]string, map[string][]string, error) {
	if raw == "" {
		return nil, nil, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: invalid query parameters", ErrRouteRuntimeTarget)
	}
	legacy := make(map[string]string, len(values))
	lossless := make(map[string][]string, len(values))
	for key, items := range values {
		if len(items) == 0 {
			return nil, nil, fmt.Errorf("%w: query parameter %q has no value", ErrRouteRuntimeTarget, key)
		}
		legacy[key] = items[0]
		lossless[key] = append([]string(nil), items...)
	}
	return legacy, lossless, nil
}

func exactProtocolV2RouteRequestBody(input routes.RouteInvocation) (map[string]any, bool, error) {
	if strings.TrimSpace(input.Step.RequestSchema) == "" {
		if input.Stage == routes.InvocationStageHandler && len(input.Request.Body) != 0 {
			return nil, false, fmt.Errorf("%w: typed JSON body requires a request schema", ErrRouteRuntimeTarget)
		}
		return nil, false, nil
	}
	return exactProtocolV2RouteBody(input.Request.Body, input.Step.RequestSchema, "request")
}

func exactProtocolV2PriorRouteResponse(input routes.RouteInvocation) (*extensionsruntime.ProtocolV2RouteResponseDocument, error) {
	if input.Stage != routes.InvocationStageResponse {
		return nil, nil
	}
	if input.Response == nil {
		return nil, ErrRouteRuntimeTarget
	}
	var body map[string]any
	present := false
	if strings.TrimSpace(input.Step.ResponseSchema) != "" {
		var err error
		body, present, err = exactProtocolV2RouteBody(input.Response.Body, input.Step.ResponseSchema, "response")
		if err != nil {
			return nil, err
		}
	}
	return &extensionsruntime.ProtocolV2RouteResponseDocument{
		StatusCode: input.Response.Status, Headers: input.Response.Headers.Clone(),
		Body: body, BodyPresent: present,
	}, nil
}

func exactProtocolV2RouteBody(raw []byte, schema, label string) (map[string]any, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if strings.TrimSpace(schema) == "" {
		return nil, false, fmt.Errorf("%w: typed JSON body requires a %s schema", ErrRouteRuntimeTarget, label)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, false, fmt.Errorf("%w: unary v2 body must be a JSON object", ErrRouteRuntimeTarget)
	}
	return body, true, nil
}

func exactProtocolV2RouteInvocationStage(stage routes.InvocationStage) (extensionsruntime.ProtocolV2RouteInvocationStage, error) {
	switch stage {
	case routes.InvocationStageRequest:
		return extensionsruntime.ProtocolV2RouteInvocationStageRequest, nil
	case routes.InvocationStageHandler:
		return extensionsruntime.ProtocolV2RouteInvocationStageHandler, nil
	case routes.InvocationStageResponse:
		return extensionsruntime.ProtocolV2RouteInvocationStageResponse, nil
	default:
		return "", ErrRouteRuntimeTarget
	}
}

func protocolV2RoutePatchOperations(source []extensionsruntime.ProtocolV2RoutePatchOperation) ([]routes.RoutePatchOperation, error) {
	result := make([]routes.RoutePatchOperation, 0, len(source))
	for _, operation := range source {
		kind := routes.RoutePatchOperationKind(operation.Kind)
		switch kind {
		case routes.RoutePatchAdd, routes.RoutePatchReplace, routes.RoutePatchRemove:
		default:
			return nil, ErrRouteRuntimeTarget
		}
		result = append(result, routes.RoutePatchOperation{
			Kind: kind, Path: operation.Path, Value: append(json.RawMessage(nil), operation.Value...),
		})
	}
	return result, nil
}
