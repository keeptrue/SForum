package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrProtocolV2GuardInvalid       = errors.New("protocol v2 guard invocation is invalid")
	ErrProtocolV2GuardDenied        = errors.New("protocol v2 guard denied the request")
	ErrProtocolV2GuardRuntimeFailed = errors.New("protocol v2 guard runtime failed")
)

type ProtocolV2GuardFailureKind string

const (
	ProtocolV2GuardFailureDenied   ProtocolV2GuardFailureKind = "denied"
	ProtocolV2GuardFailureCrash    ProtocolV2GuardFailureKind = "crash"
	ProtocolV2GuardFailureTimeout  ProtocolV2GuardFailureKind = "timeout"
	ProtocolV2GuardFailureProtocol ProtocolV2GuardFailureKind = "protocol"
	ProtocolV2GuardFailureCanceled ProtocolV2GuardFailureKind = "canceled"
)

// ProtocolV2GuardCallFailure is created only after InvokeRoute has been called.
// Error text is deliberately detached from plugin-controlled error details.
type ProtocolV2GuardCallFailure struct {
	kind  ProtocolV2GuardFailureKind
	cause error
}

func NewProtocolV2GuardCallFailure(kind ProtocolV2GuardFailureKind, cause error) *ProtocolV2GuardCallFailure {
	switch kind {
	case ProtocolV2GuardFailureDenied:
		cause = ErrProtocolV2GuardDenied
	case ProtocolV2GuardFailureTimeout:
		cause = errors.Join(ErrProtocolV2GuardRuntimeFailed, context.DeadlineExceeded)
	case ProtocolV2GuardFailureProtocol:
		cause = ErrProtocolV2GuardInvalid
	case ProtocolV2GuardFailureCanceled:
		cause = context.Canceled
	case ProtocolV2GuardFailureCrash:
		// gRPC status details are plugin-controlled. Keep only the Host sentinel so
		// future errors.Unwrap logging cannot disclose a plugin reason.
		cause = ErrProtocolV2GuardRuntimeFailed
	default:
		cause = ErrProtocolV2GuardRuntimeFailed
	}
	return &ProtocolV2GuardCallFailure{kind: kind, cause: cause}
}

func (e *ProtocolV2GuardCallFailure) Kind() ProtocolV2GuardFailureKind {
	if e == nil {
		return ""
	}
	return e.kind
}

func (e *ProtocolV2GuardCallFailure) RuntimeExecutionObserved() bool { return e != nil }

func (e *ProtocolV2GuardCallFailure) Error() string {
	if e == nil {
		return ""
	}
	switch e.kind {
	case ProtocolV2GuardFailureDenied:
		return "protocol v2 guard denied the request"
	case ProtocolV2GuardFailureCrash:
		return "protocol v2 guard runtime failed"
	case ProtocolV2GuardFailureTimeout:
		return "protocol v2 guard runtime timed out"
	case ProtocolV2GuardFailureProtocol:
		return "protocol v2 guard returned an invalid response"
	case ProtocolV2GuardFailureCanceled:
		return "protocol v2 guard invocation was canceled"
	default:
		return "protocol v2 guard invocation failed"
	}
}

func (e *ProtocolV2GuardCallFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ProtocolV2GuardRequest binds a custom guard to one exact frozen route. The
// request headers are projected only after the exact frozen guard and explicit
// Host authority agree.
type ProtocolV2GuardRequest struct {
	GuardID              string
	GuardContractVersion string
	RouteID              string
	RouteContractVersion string
	Method               string
	Path                 string
	Headers              http.Header
	PathParameters       map[string]string
	QueryParameters      map[string]string
	QueryParameterValues map[string][]string
	RequestSchema        string
	Body                 map[string]any
	BodyPresent          bool
	Authority            ProtocolV2RequestAuthority
	Actor                *ProtocolV2RouteActor
	CorrelationID        string
	Timeout              time.Duration
}

type protocolV2GuardContextInvoker interface {
	InvokeGuardContext(context.Context, ProtocolV2GuardRequest) error
}

func (c *protocolV2Client) InvokeGuardContext(parent context.Context, input ProtocolV2GuardRequest) error {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return ErrProtocolV2GuardInvalid
	}
	guard, route, err := c.validateFrozenGuard(input)
	if err != nil {
		return err
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = DefaultProtocolV2RequestTimeout
	}
	ctx, cancel := protocolV2Deadline(parent, timeout)
	defer cancel()

	requestContext := c.requestContext(ctx, input.CorrelationID)
	if input.Actor != nil {
		requestContext.Actor = &protocolv2.Actor{
			UserId: input.Actor.UserID, PermissionKeys: append([]string(nil), input.Actor.PermissionKeys...),
		}
	}
	var body *protocolv2.TypedDocument
	if input.BodyPresent {
		schemaID, schemaVersion, err := protocolV2SchemaRef(input.RequestSchema)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrProtocolV2GuardInvalid, err)
		}
		body, err = protocolV2Document(schemaID, schemaVersion, input.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrProtocolV2GuardInvalid, err)
		}
	}
	headers, err := protocolV2GuardHeaders(input.Headers, input.Authority)
	if err != nil {
		return wrapProtocolV2AuthorityError(ErrProtocolV2GuardInvalid, err)
	}
	authorityMode, guardKind, err := protocolV2WireRequestAuthority(input.Authority)
	if err != nil {
		return wrapProtocolV2AuthorityError(ErrProtocolV2GuardInvalid, err)
	}
	queryParameters, queryParameterValues, err := protocolV2RouteQueryParameters(
		input.QueryParameters, input.QueryParameterValues,
	)
	if err != nil {
		return fmt.Errorf("%w: invalid query parameters", ErrProtocolV2GuardInvalid)
	}
	headers.Set("X-SForum-Guard-Route-ID", route.ID)
	headers.Set("X-SForum-Guard-Route-Contract", route.ContractVersion)
	headers.Set("X-SForum-Guard-Kind", guard.Kind)
	response, err := c.client.InvokeRoute(ctx, &pluginv2.RouteRequest{
		Context: requestContext, RouteId: guard.ID, ContractVersion: guard.ContractVersion,
		Method: input.Method, Path: input.Path, Headers: protocolV2RouteHeaders(headers),
		RequestAuthorityMode: authorityMode, GuardKind: guardKind,
		PathParameters:  cloneProtocolV2RouteParameters(input.PathParameters),
		QueryParameters: queryParameters, QueryParameterValues: queryParameterValues, Body: body,
	})
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
			return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureTimeout, err)
		case errors.Is(ctx.Err(), context.Canceled), status.Code(err) == codes.Canceled:
			return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureCanceled, err)
		default:
			return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureCrash, err)
		}
	}
	if err := validateProtocolV2RouteResponseContext(response.GetContext(), requestContext); err != nil {
		return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureProtocol, err)
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureProtocol, nil)
	}
	if response.GetStreamFollows() || response.GetBody() != nil || len(response.GetHeaders()) != 0 {
		return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureProtocol, nil)
	}
	switch int(response.GetStatusCode()) {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureDenied, nil)
	default:
		return NewProtocolV2GuardCallFailure(ProtocolV2GuardFailureProtocol, nil)
	}
}

func (c *protocolV2Client) validateFrozenGuard(
	input ProtocolV2GuardRequest,
) (extensions.ManifestGuard, extensions.ManifestRoute, error) {
	if strings.TrimSpace(input.GuardID) == "" || strings.TrimSpace(input.GuardContractVersion) == "" ||
		strings.TrimSpace(input.RouteID) == "" || strings.TrimSpace(input.RouteContractVersion) == "" ||
		strings.TrimSpace(input.Method) == "" || !strings.HasPrefix(input.Path, "/") ||
		input.BodyPresent && strings.TrimSpace(input.RequestSchema) == "" ||
		input.Actor != nil && input.Actor.UserID <= 0 {
		return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
	}
	if _, _, err := protocolV2RouteQueryParameters(input.QueryParameters, input.QueryParameterValues); err != nil {
		return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
	}
	var guard *extensions.ManifestGuard
	for index := range c.guards {
		candidate := &c.guards[index]
		if candidate.ID == input.GuardID && candidate.ContractVersion == input.GuardContractVersion {
			if guard != nil {
				return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
			}
			guard = candidate
		}
	}
	if guard == nil || (guard.Kind != "custom" && guard.Kind != "raw_request") {
		return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
	}
	var route *extensions.ManifestRoute
	for index := range c.routes {
		candidate := &c.routes[index]
		if candidate.ID != input.RouteID || candidate.ContractVersion != input.RouteContractVersion ||
			candidate.Guard != guard.ID || candidate.RequestSchema != input.RequestSchema ||
			!protocolV2RouteMethodMatches(*candidate, input.Method) {
			continue
		}
		if route != nil {
			return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
		}
		route = candidate
	}
	if route == nil {
		return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
	}
	expected, err := c.frozenRouteAuthority(*route)
	if err != nil || input.Authority != expected {
		return extensions.ManifestGuard{}, extensions.ManifestRoute{}, ErrProtocolV2GuardInvalid
	}
	return *guard, *route, nil
}

func protocolV2RouteMethodMatches(route extensions.ManifestRoute, method string) bool {
	if route.Action == "global_middleware" && len(route.Methods) == 0 {
		return true
	}
	for _, declared := range route.Methods {
		if declared == "*" || strings.EqualFold(declared, method) ||
			route.Mode == "http" && strings.EqualFold(method, http.MethodHead) && strings.EqualFold(declared, http.MethodGet) {
			return true
		}
	}
	return false
}

func protocolV2GuardHeaders(source http.Header, authority ProtocolV2RequestAuthority) (http.Header, error) {
	if err := validateProtocolV2RequestAuthority(authority); err != nil {
		return nil, err
	}
	for name := range source {
		canonical := strings.ToLower(strings.TrimSpace(name))
		if strings.HasPrefix(canonical, "x-sforum-") {
			return nil, fmt.Errorf("%w: reserved guard header", ErrProtocolV2GuardInvalid)
		}
	}
	return protocolV2AuthorizedRequestHeaders(source, authority)
}

func cloneProtocolV2Guards(values []extensions.ManifestGuard) []extensions.ManifestGuard {
	result := append([]extensions.ManifestGuard(nil), values...)
	for index := range result {
		result[index].Permissions = append([]string(nil), values[index].Permissions...)
	}
	return result
}

var _ protocolV2GuardContextInvoker = (*protocolV2Client)(nil)
