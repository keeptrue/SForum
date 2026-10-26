package hostapi

import (
	"context"
	"errors"
	"strings"
	"time"

	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

// ProtocolV2SessionServiceServer serves the S7 surface: three operations whose
// requests carry only a namespace key (+ value/ttl on Set). The extension
// identity comes from the broker-attested runtime, the session scope from the
// S9 delegation, and the installation from the Host. A caller-supplied
// extension id, user id, or session id is not merely ignored — the request
// shape has no such field, so it cannot be smuggled in.
type ProtocolV2SessionServiceServer struct {
	hostv2.UnimplementedPluginSessionServiceServer

	store          *pluginsessionstate.Store
	delegations    *ProtocolV2SessionDelegationAuthority
	installationID string
	// safeMode is the boot-time Safe Mode flag. It is deliberately a field and
	// not a live lookup: Safe Mode is decided before any plugin runtime starts,
	// so a plugin can never observe a window in which it reads state.
	safeMode bool
	// admission re-checks the exact runtime before every operation. A nil
	// admission is a wiring error and denies, never "no admission required".
	admission ServiceProviderAdmission
}

type ProtocolV2SessionServiceOption func(*ProtocolV2SessionServiceServer) error

// WithProtocolV2SessionSafeMode marks the whole service Safe Mode: reads miss,
// writes are refused.
func WithProtocolV2SessionSafeMode(enabled bool) ProtocolV2SessionServiceOption {
	return func(s *ProtocolV2SessionServiceServer) error {
		s.safeMode = enabled
		return nil
	}
}

// WithProtocolV2SessionAdmission installs the exact-runtime admission check.
func WithProtocolV2SessionAdmission(admission ServiceProviderAdmission) ProtocolV2SessionServiceOption {
	return func(s *ProtocolV2SessionServiceServer) error {
		s.admission = admission
		return nil
	}
}

func NewProtocolV2SessionServiceServer(
	store *pluginsessionstate.Store,
	delegations *ProtocolV2SessionDelegationAuthority,
	installationID string,
	options ...ProtocolV2SessionServiceOption,
) (*ProtocolV2SessionServiceServer, error) {
	if store == nil || delegations == nil || !validIdentityPartForSessionService(installationID) {
		return nil, pluginsessionstate.ErrInvalid
	}
	server := &ProtocolV2SessionServiceServer{
		store: store, delegations: delegations, installationID: installationID,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(server); err != nil {
			return nil, err
		}
	}
	return server, nil
}

func (s *ProtocolV2SessionServiceServer) Get(
	ctx context.Context,
	request *hostv2.PluginSessionGetRequest,
) (*hostv2.PluginSessionGetResponse, error) {
	requestContext := request.GetContext()
	response := &hostv2.PluginSessionGetResponse{
		Context: protocolV2SessionResponseContext(requestContext, ProtocolV2RuntimeIdentityFromContext(ctx)),
	}
	scope, identity, detail := s.resolveCall(ctx, requestContext)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	response.Context = protocolV2SessionResponseContext(requestContext, identity)
	if s.safeMode {
		// Safe Mode is a read miss, never an error and never a leak.
		return response, nil
	}
	callCtx, release, err := s.admit(ctx, identity)
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	defer release()
	value, found, err := s.store.Get(callCtx, scope, request.GetNamespaceKey())
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	response.Found = found
	if found {
		response.Value = append([]byte(nil), value...)
	}
	return response, nil
}

func (s *ProtocolV2SessionServiceServer) Set(
	ctx context.Context,
	request *hostv2.PluginSessionSetRequest,
) (*hostv2.PluginSessionSetResponse, error) {
	requestContext := request.GetContext()
	response := &hostv2.PluginSessionSetResponse{
		Context: protocolV2SessionResponseContext(requestContext, ProtocolV2RuntimeIdentityFromContext(ctx)),
	}
	scope, identity, detail := s.resolveCall(ctx, requestContext)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	response.Context = protocolV2SessionResponseContext(requestContext, identity)
	if s.safeMode {
		response.Error = protocolV2SessionFailure(ErrProtocolV2SessionSafeMode)
		return response, nil
	}
	ttl, err := protocolV2SessionRequestedTTL(request.GetTtlMs())
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	callCtx, release, err := s.admit(ctx, identity)
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	defer release()
	effective, err := s.store.Set(callCtx, scope, request.GetNamespaceKey(), request.GetValue(), ttl)
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	response.EffectiveTtlMs = uint32(effective.Milliseconds())
	return response, nil
}

func (s *ProtocolV2SessionServiceServer) Delete(
	ctx context.Context,
	request *hostv2.PluginSessionDeleteRequest,
) (*hostv2.PluginSessionDeleteResponse, error) {
	requestContext := request.GetContext()
	response := &hostv2.PluginSessionDeleteResponse{
		Context: protocolV2SessionResponseContext(requestContext, ProtocolV2RuntimeIdentityFromContext(ctx)),
	}
	scope, identity, detail := s.resolveCall(ctx, requestContext)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	response.Context = protocolV2SessionResponseContext(requestContext, identity)
	if s.safeMode {
		response.Error = protocolV2SessionFailure(ErrProtocolV2SessionSafeMode)
		return response, nil
	}
	callCtx, release, err := s.admit(ctx, identity)
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	defer release()
	deleted, err := s.store.Delete(callCtx, scope, request.GetNamespaceKey())
	if err != nil {
		response.Error = protocolV2SessionFailure(err)
		return response, nil
	}
	response.Deleted = deleted
	return response, nil
}

// ErrProtocolV2SessionSafeMode is returned to a write while the Host is in Safe
// Mode. Reads intentionally do not surface it: they miss.
var ErrProtocolV2SessionSafeMode = errors.New("hostapi: plugin session state is read-only in Safe Mode")

// resolveCall derives the extension identity (broker-attested) and the session
// scope (Host mint record) for one request. Every failure is a denial.
func (s *ProtocolV2SessionServiceServer) resolveCall(
	ctx context.Context,
	request *protocolv2.RequestContext,
) (pluginsessionstate.Scope, *protocolv2.ExtensionIdentity, *protocolv2.ErrorDetail) {
	if s == nil || s.store == nil || s.delegations == nil {
		return pluginsessionstate.Scope{}, nil, protocolV2SessionFailure(ErrProtocolV2SessionDelegationInvalid)
	}
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if !validProtocolV2SessionRuntimeBinding(identity) {
		return pluginsessionstate.Scope{}, nil, protocolV2SessionFailure(ErrProtocolV2SessionDelegationInvalid)
	}
	if request == nil || !proto.Equal(request.GetExtension(), identity) {
		return pluginsessionstate.Scope{}, identity, protocolV2SessionFailure(ErrProtocolV2SessionDelegationInvalid)
	}
	// A plugin may never supply an actor: the session scope is the authority.
	if request.GetActor() != nil {
		return pluginsessionstate.Scope{}, identity, protocolV2SessionFailure(ErrProtocolV2SessionDelegationInvalid)
	}
	delegation := request.GetHostSessionDelegation()
	if delegation == nil {
		return pluginsessionstate.Scope{}, identity, protocolV2SessionFailure(ErrProtocolV2SessionDelegationInvalid)
	}
	hostScope, err := s.delegations.Resolve(delegation.GetToken(), identity)
	if err != nil {
		return pluginsessionstate.Scope{}, identity, protocolV2SessionFailure(err)
	}
	scope := pluginsessionstate.Scope{
		InstallationID: s.installationID, ExtensionID: identity.GetExtensionId(),
		RuntimeInstanceID: identity.GetInstanceId(), SessionFingerprint: hostScope.SessionFingerprint,
		SessionIDleExpiresAt: hostScope.IdleExpiresAt, SessionAbsoluteExpiresAt: hostScope.AbsoluteExpiresAt,
	}
	if err := scope.Validate(); err != nil {
		return pluginsessionstate.Scope{}, identity, protocolV2SessionFailure(err)
	}
	return scope, identity, nil
}

// admit holds one exact-runtime admission lease across the store call, so a
// runtime that is draining, quarantined, or no longer active cannot touch
// session state. A missing admission is a wiring error and denies.
func (s *ProtocolV2SessionServiceServer) admit(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
) (context.Context, func(), error) {
	if s == nil || s.admission == nil || identity == nil {
		return nil, func() {}, ErrServiceProviderAdmissionUnavailable
	}
	lease, err := s.admission.AcquireServiceProvider(ctx, ServiceProviderIdentity{
		ExtensionID: identity.GetExtensionId(), InstanceID: identity.GetInstanceId(),
	})
	if err != nil {
		return nil, func() {}, err
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, func() {}, ErrServiceProviderAdmissionUnavailable
	}
	return lease.Context(), lease.Release, nil
}

func protocolV2SessionRequestedTTL(ttlMs uint32) (time.Duration, error) {
	if ttlMs == 0 {
		// 0 means "session-scoped": ask for the Host's own ceiling and let the
		// session bounds and the S6 cap tighten it. Plugin state still never
		// outlives the Core session.
		return pluginsessionstate.MaxTTL, nil
	}
	return time.Duration(ttlMs) * time.Millisecond, nil
}

func protocolV2SessionResponseContext(
	request *protocolv2.RequestContext,
	identity *protocolv2.ExtensionIdentity,
) *protocolv2.ResponseContext {
	response := protocolV2ResponseContext(request)
	if identity == nil {
		response.Extension = nil
		return response
	}
	response.Extension = proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	return response
}

func protocolV2SessionFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{
		Code:   protocolv2.ErrorCode_ERROR_CODE_INTERNAL,
		Reason: "host.plugin_session_failed", Message: "The plugin session-state operation failed.",
	}
	switch {
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
		detail.Reason = "host.plugin_session_cancelled"
		detail.Message = "The plugin session-state operation was cancelled."
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
		detail.Reason = "host.plugin_session_deadline"
		detail.Message = "The plugin session-state operation exceeded its deadline."
	case errors.Is(err, ErrProtocolV2SessionDelegationInvalid), errors.Is(err, ErrProtocolV2SessionDelegationRetired):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.plugin_session_scope_unattested"
		detail.Message = "This call carries no valid Host session delegation."
	case errors.Is(err, ErrServiceProviderStale), errors.Is(err, ErrServiceProviderDraining),
		errors.Is(err, ErrServiceProviderAdmissionUnavailable):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME
		detail.Reason = "host.plugin_session_runtime_stale"
		detail.Message = "The exact runtime is not admitted for Host session state."
	case errors.Is(err, ErrProtocolV2SessionSafeMode):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.plugin_session_safe_mode"
		detail.Message = "Plugin session state is read-only while the Host is in Safe Mode."
	case errors.Is(err, pluginsessionstate.ErrKeyTooLong):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.plugin_session_key_too_long"
		detail.Message = "The namespace key exceeds the Host limit."
	case errors.Is(err, pluginsessionstate.ErrValueTooLarge):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.plugin_session_value_too_large"
		detail.Message = "The value exceeds the Host limit."
	case errors.Is(err, pluginsessionstate.ErrTTLOutOfRange):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.plugin_session_ttl_out_of_range"
		detail.Message = "The requested lifetime is outside the Host range."
	case errors.Is(err, pluginsessionstate.ErrKeyLimit):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.plugin_session_key_limit"
		detail.Message = "This session already holds the maximum number of keys for this extension."
	case errors.Is(err, pluginsessionstate.ErrExtensionLimit):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.plugin_session_extension_limit"
		detail.Message = "This extension installation already holds the maximum number of session keys."
	case errors.Is(err, pluginsessionstate.ErrInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.plugin_session_request_invalid"
		detail.Message = "The request does not satisfy the declared contract."
	case errors.Is(err, pluginsessionstate.ErrBackendUnavailable):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE
		detail.Reason = "host.plugin_session_unavailable"
		detail.Message = "Host session state is temporarily unavailable."
		detail.Retryable = true
	}
	return detail
}

func validIdentityPartForSessionService(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	if strings.ContainsAny(value, ":[]*? \t\n\r") {
		return false
	}
	return len(value) <= 255
}

var _ hostv2.PluginSessionServiceServer = (*ProtocolV2SessionServiceServer)(nil)
