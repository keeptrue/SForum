package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	stdhttp "net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const routeIdempotencyCleanupTimeout = 2 * time.Second

type RequiredRouteIdempotency struct {
	store *idempotency.Store
}

func NewRequiredRouteIdempotency(store *idempotency.Store) *RequiredRouteIdempotency {
	return &RequiredRouteIdempotency{store: store}
}

func (r *RequiredRouteIdempotency) MutationReplayAvailable() bool {
	return r != nil && r.store != nil && r.store.RequiredReplayCipherEnabled()
}

func (r *RequiredRouteIdempotency) Begin(
	ctx context.Context,
	plan routes.RouteExecutionPlan,
	step routes.RouteExecutionStep,
	policy routes.RouteExecutionPolicy,
	request routes.DispatchRequest,
) (routes.RouteIdempotencyLease, *routes.RouteIdempotencyReplay, error) {
	if r == nil || r.store == nil || ctx == nil || !policy.IdempotencyRequired ||
		step.Provider.Kind != routes.ProviderPlugin {
		return nil, nil, routes.ErrDispatchIdempotencyUnavailable
	}
	values := request.Headers.Values(idempotency.HeaderName)
	if len(values) != 1 || values[0] == "" {
		return nil, nil, routes.ErrDispatchIdempotencyKeyInvalid
	}
	actorScope, err := routeReplayActorScope(request)
	if err != nil {
		return nil, nil, err
	}
	fingerprint, legacyFingerprint, planDigest, err := routeReplayFingerprints(plan, request)
	if err != nil {
		return nil, nil, routes.ErrDispatchIdempotencyKeyInvalid
	}
	compatible := []string(nil)
	if routeReplayLegacyFingerprintCompatible(plan, request.Query) {
		compatible = append(compatible, legacyFingerprint)
	}
	artifact := step.Provider.Artifact
	lease, replay, err := r.store.BeginRequiredReplayBound(ctx, idempotency.RequiredReplayScope{
		ActorScope: actorScope, ExtensionID: artifact.ExtensionID,
		ExtensionVersion: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		RouteID: step.RouteID, ContractVersion: step.ContractVersion, Method: request.Method,
	}, values[0], idempotency.RequiredReplayBinding{
		Fingerprint: fingerprint, PlanDigest: planDigest, CompatibleFingerprints: compatible,
	})
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrRequiredReplayInvalid):
			return nil, nil, routes.ErrDispatchIdempotencyKeyInvalid
		case errors.Is(err, idempotency.ErrRequiredReplayInProgress):
			return nil, nil, routes.ErrDispatchIdempotencyInProgress
		case errors.Is(err, idempotency.ErrRequiredReplayFingerprintConflict):
			return nil, nil, routes.ErrDispatchIdempotencyConflict
		default:
			return nil, nil, errors.Join(routes.ErrDispatchIdempotencyUnavailable, err)
		}
	}
	if replay != nil {
		// 历史记录也必须经过当前插件响应头策略；Host 证据随后重建，
		// canonical Link 则只从结构化 CanonicalPath 生成。
		headers := routes.FilterPluginResponseHeaders(replay.Headers)
		headers.Set(idempotency.ReplayedHeader, "true")
		return nil, &routes.RouteIdempotencyReplay{
			Response: routes.DispatchResponse{
				Status: replay.Status, Headers: headers, Body: append([]byte(nil), replay.Body...),
				CanonicalPath: replay.CanonicalPath,
			},
			Authorization:         routeReplayAuthorizationFromStored(replay.Authorization),
			ResponseContractKnown: replay.ResponseContractKnown,
			ResponseContract:      routeReplayResponseContractFromStored(replay.ResponseContract),
		}, nil
	}
	return &requiredRouteIdempotencyLease{store: r.store, lease: lease}, nil, nil
}

type requiredRouteIdempotencyLease struct {
	store *idempotency.Store
	lease idempotency.RequiredReplayLease
}

func (l *requiredRouteIdempotencyLease) Complete(
	ctx context.Context,
	completion routes.RouteIdempotencyCompletion,
) error {
	if l == nil || l.store == nil {
		return routes.ErrDispatchIdempotencyUnavailable
	}
	cleanupCtx, cancel := routeIdempotencyCleanupContext(ctx)
	defer cancel()
	headers := completion.Response.Headers.Clone()
	headers.Del(idempotency.ReplayedHeader)
	return l.store.CompleteRequiredReplay(cleanupCtx, l.lease, idempotency.RequiredReplayResponse{
		Status: completion.Response.Status, Headers: headers,
		Body:                  append([]byte(nil), completion.Response.Body...),
		CanonicalPath:         completion.Response.CanonicalPath,
		ResponseContractKnown: completion.ResponseContractKnown,
		ResponseContract:      routeReplayResponseContractForStorage(completion.ResponseContract),
		Authorization:         routeReplayAuthorizationForStorage(completion.Authorization),
	})
}

func (l *requiredRouteIdempotencyLease) Abort(ctx context.Context) error {
	if l == nil || l.store == nil {
		return routes.ErrDispatchIdempotencyUnavailable
	}
	cleanupCtx, cancel := routeIdempotencyCleanupContext(ctx)
	defer cancel()
	return l.store.AbortRequiredReplay(cleanupCtx, l.lease)
}

func routeIdempotencyCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), routeIdempotencyCleanupTimeout)
}

func routeReplayActorScope(request routes.DispatchRequest) (string, error) {
	if request.Authenticated {
		if request.ActorID <= 0 ||
			request.CredentialSource != routes.DispatchCredentialCookie && request.CredentialSource != routes.DispatchCredentialBearer {
			return "", routes.ErrDispatchIdempotencyUnavailable
		}
		return "actor:" + strconv.FormatInt(request.ActorID, 10) + ":" + string(request.CredentialSource), nil
	}
	if request.ActorID != 0 {
		return "", routes.ErrDispatchIdempotencyUnavailable
	}
	address, err := netip.ParseAddr(strings.TrimSpace(request.ClientIP))
	if err != nil {
		return "", routes.ErrDispatchIdempotencyUnavailable
	}
	digest := sha256.Sum256([]byte(address.Unmap().String()))
	return "anonymous:" + hex.EncodeToString(digest[:]), nil
}

func routeReplayFingerprints(plan routes.RouteExecutionPlan, request routes.DispatchRequest) (string, string, string, error) {
	query, err := canonicalRouteReplayQuery(request.Query)
	if err != nil {
		return "", "", "", err
	}
	contentType, err := canonicalRouteReplayContentType(request.Headers)
	if err != nil {
		return "", "", "", err
	}
	binding, err := routes.BuildRouteReplayBinding(plan, request)
	if err != nil {
		return "", "", "", err
	}
	document, err := json.Marshal(struct {
		Schema      string `json:"schema"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		Query       string `json:"query"`
		ContentType string `json:"contentType"`
		Body        []byte `json:"body"`
		PlanDigest  string `json:"planDigest"`
		BaseDigest  string `json:"baseDigest"`
	}{
		Schema: "sforum.required-route-fingerprint@2",
		Method: request.Method, Path: request.Path, Query: query,
		ContentType: contentType, Body: request.Body,
		PlanDigest: binding.PlanDigest, BaseDigest: binding.BaseDigest,
	})
	if err != nil {
		return "", "", "", err
	}
	digest := sha256.Sum256(document)
	legacy, err := routeReplayLegacyFingerprint(request)
	if err != nil {
		return "", "", "", err
	}
	return hex.EncodeToString(digest[:]), legacy, binding.PlanDigest, nil
}

func canonicalRouteReplayQuery(raw string) (string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", err
	}
	return values.Encode(), nil
}

func routeReplayLegacyFingerprint(request routes.DispatchRequest) (string, error) {
	query, err := canonicalLegacyRouteReplayQuery(request.Query)
	if err != nil {
		return "", err
	}
	contentType, err := canonicalRouteReplayContentType(request.Headers)
	if err != nil {
		return "", err
	}
	document, err := json.Marshal(struct {
		Method      string `json:"method"`
		Path        string `json:"path"`
		Query       string `json:"query"`
		ContentType string `json:"contentType"`
		Body        []byte `json:"body"`
	}{
		Method: request.Method, Path: request.Path, Query: query,
		ContentType: contentType, Body: request.Body,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalLegacyRouteReplayQuery(raw string) (string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", err
	}
	for key := range values {
		sort.Strings(values[key])
	}
	return values.Encode(), nil
}

func routeReplayLegacyFingerprintCompatible(plan routes.RouteExecutionPlan, rawQuery string) bool {
	if len(plan.Chain()) != 1 {
		return false
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	for _, items := range values {
		if len(items) > 1 {
			return false
		}
	}
	return true
}

func routeReplayAuthorizationForStorage(
	value *routes.RouteReplayAuthorization,
) *idempotency.RequiredReplayAuthorization {
	if value == nil {
		return nil
	}
	result := &idempotency.RequiredReplayAuthorization{
		Schema: value.Schema, PlanDigest: value.PlanDigest, BaseDigest: value.BaseDigest,
		RequestMutations: make([]idempotency.RequiredReplayRequestMutation, len(value.RequestMutations)),
	}
	for index, mutation := range value.RequestMutations {
		result.RequestMutations[index].StepIndex = mutation.StepIndex
		result.RequestMutations[index].BeforeDigest = mutation.BeforeDigest
		result.RequestMutations[index].AfterDigest = mutation.AfterDigest
		result.RequestMutations[index].Operations = make([]idempotency.RequiredReplayPatchOperation, len(mutation.Operations))
		for operationIndex, operation := range mutation.Operations {
			result.RequestMutations[index].Operations[operationIndex] = idempotency.RequiredReplayPatchOperation{
				Kind: string(operation.Kind), Path: operation.Path, Value: append([]byte(nil), operation.Value...),
			}
		}
	}
	return result
}

func routeReplayAuthorizationFromStored(
	value *idempotency.RequiredReplayAuthorization,
) *routes.RouteReplayAuthorization {
	if value == nil {
		return nil
	}
	result := &routes.RouteReplayAuthorization{
		Schema: value.Schema, PlanDigest: value.PlanDigest, BaseDigest: value.BaseDigest,
		RequestMutations: make([]routes.RouteReplayRequestMutation, len(value.RequestMutations)),
	}
	for index, mutation := range value.RequestMutations {
		result.RequestMutations[index].StepIndex = mutation.StepIndex
		result.RequestMutations[index].BeforeDigest = mutation.BeforeDigest
		result.RequestMutations[index].AfterDigest = mutation.AfterDigest
		result.RequestMutations[index].Operations = make([]routes.RoutePatchOperation, len(mutation.Operations))
		for operationIndex, operation := range mutation.Operations {
			result.RequestMutations[index].Operations[operationIndex] = routes.RoutePatchOperation{
				Kind: routes.RoutePatchOperationKind(operation.Kind), Path: operation.Path,
				Value: append([]byte(nil), operation.Value...),
			}
		}
	}
	return result
}

func routeReplayResponseContractForStorage(
	value *routes.RouteReplayResponseContract,
) *idempotency.RequiredReplayResponseContract {
	if value == nil {
		return nil
	}
	return &idempotency.RequiredReplayResponseContract{
		StepIndex: value.StepIndex, InvocationStage: string(value.InvocationStage),
		RouteID: value.RouteID, ContractVersion: value.ContractVersion,
		ResponseSchema: value.ResponseSchema,
	}
}

func routeReplayResponseContractFromStored(
	value *idempotency.RequiredReplayResponseContract,
) *routes.RouteReplayResponseContract {
	if value == nil {
		return nil
	}
	return &routes.RouteReplayResponseContract{
		StepIndex: value.StepIndex, InvocationStage: routes.InvocationStage(value.InvocationStage),
		RouteID: value.RouteID, ContractVersion: value.ContractVersion,
		ResponseSchema: value.ResponseSchema,
	}
}

func canonicalRouteReplayContentType(headers stdhttp.Header) (string, error) {
	values := headers.Values("Content-Type")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", routes.ErrDispatchIdempotencyKeyInvalid
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil {
		return "", err
	}
	return mime.FormatMediaType(strings.ToLower(mediaType), parameters), nil
}

var _ routes.RouteIdempotencyController = (*RequiredRouteIdempotency)(nil)
var _ routes.RouteIdempotencyLease = (*requiredRouteIdempotencyLease)(nil)
var _ routes.RouteMutationReplayCapability = (*RequiredRouteIdempotency)(nil)
