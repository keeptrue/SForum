package hostapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hosthttp "github.com/keeptrue/sforum/apps/api/app/Support/HostHTTP"
	pluginfiles "github.com/keeptrue/sforum/apps/api/app/Support/PluginFiles"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProtocolV2SecretServiceServer exposes SecretStore.Resolve to exact runtimes and the
// settings write half (Put/Clear) to runtimes that declare the same secret fields.
type ProtocolV2SecretServiceServer struct {
	hostv2.UnimplementedSecretServiceServer
	secrets *secretstore.Service
	// settings is the document authority behind Put/Clear; nil disables writes only.
	settings *settingslifecycle.Service
	// capabilities is late-bound by bootstrap (the extension service is built after the
	// platform services), so a nil source denies writes instead of failing them open.
	capabilities CapabilitySource
}

// NewProtocolV2SecretServiceServer builds the Protocol V2 Secret boundary.
func NewProtocolV2SecretServiceServer(secrets *secretstore.Service, settings *settingslifecycle.Service) (*ProtocolV2SecretServiceServer, error) {
	if secrets == nil {
		return nil, errors.New("hostapi: secret store is required")
	}
	return &ProtocolV2SecretServiceServer{secrets: secrets, settings: settings}, nil
}

// BindCapabilitySource late-binds the capability resolver used to gate secret writes.
func (s *ProtocolV2SecretServiceServer) BindCapabilitySource(source CapabilitySource) {
	if s != nil {
		s.capabilities = source
	}
}

// Resolve decrypts a secret for the broker-attested extension namespace only.
func (s *ProtocolV2SecretServiceServer) Resolve(
	ctx context.Context,
	request *hostv2.SecretResolveRequest,
) (*hostv2.SecretResolveResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.SecretResolveResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if s == nil || s.secrets == nil {
		response.Error = protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_unavailable", "Secret Store is not configured.")
		return response, nil
	}
	secretID := strings.TrimSpace(request.GetSecretId())
	purpose := strings.TrimSpace(request.GetPurpose())
	if secretID == "" || purpose == "" {
		response.Error = protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_request_invalid", "secret_id and purpose are required.")
		return response, nil
	}
	// secret_id may be bare id (namespace = extension) or sforum.secret://ns/id.
	ref, err := parsePluginSecretRef(identity.GetExtensionId(), secretID)
	if err != nil {
		response.Error = protocolV2ResourceFailure(err, "host.secret_request_invalid", "Secret reference is invalid.")
		return response, nil
	}
	ttl := secretstore.DefaultResolveTTL
	if request.GetRequestedTtl() != nil {
		ttl = request.GetRequestedTtl().AsDuration()
	}
	// 绑定请求 deadline：剩余时间更短时收紧 lease TTL。
	if deadline, ok := ctx.Deadline(); ok {
		remain := time.Until(deadline)
		if remain > 0 && remain < ttl {
			ttl = remain
		}
	}
	lease, err := s.secrets.Resolve(ctx, secretstore.Caller{
		ExtensionID: identity.GetExtensionId(),
		Actor:       identity.GetExtensionId(),
	}, ref, purpose, ttl)
	if err != nil {
		response.Error = protocolV2SecretFailure(err)
		return response, nil
	}
	response.LeaseId = lease.LeaseID
	response.Value = append([]byte(nil), lease.Value...)
	response.MediaType = lease.MediaType
	return response, nil
}

// Put stores one settings secret the caller's manifest declares.
//
// The write goes through SettingsLifecycle so the Secret Store reference, the settings
// document CAS revision and the migration path stay in one place: a plugin writing its own
// token must be indistinguishable, to the admin settings page, from an operator typing it.
func (s *ProtocolV2SecretServiceServer) Put(
	ctx context.Context,
	request *hostv2.SecretPutRequest,
) (*hostv2.SecretPutResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.SecretPutResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	secretID, detail := s.secretWriteTarget(ctx, identity, request.GetSecretId(), len(request.GetValue()))
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if len(request.GetValue()) == 0 {
		// An empty Put is a Clear with extra steps; refuse it so a truncated body cannot
		// silently delete a working credential.
		response.Error = protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_request_invalid", "value is required; use Clear to remove a secret.")
		return response, nil
	}
	revision, err := s.settings.PutSecret(
		ctx, identity.GetExtensionId(), settingsActor(identity.GetExtensionId()),
		secretID, string(request.GetValue()), strings.TrimSpace(request.GetMediaType()),
	)
	if err != nil {
		response.Error = protocolV2SecretWriteFailure(err)
		return response, nil
	}
	response.SecretSet = true
	response.Version = revision
	return response, nil
}

// Clear removes one declared settings secret and leaves every other field untouched.
func (s *ProtocolV2SecretServiceServer) Clear(
	ctx context.Context,
	request *hostv2.SecretClearRequest,
) (*hostv2.SecretClearResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.SecretClearResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	secretID, detail := s.secretWriteTarget(ctx, identity, request.GetSecretId(), 0)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	revision, err := s.settings.PutSecret(
		ctx, identity.GetExtensionId(), settingsActor(identity.GetExtensionId()), secretID, "", "",
	)
	if err != nil {
		response.Error = protocolV2SecretWriteFailure(err)
		return response, nil
	}
	response.SecretSet = false
	response.Version = revision
	return response, nil
}

// secretWriteTarget runs every gate that is identical for Put and Clear: attested runtime,
// configured settings authority, capability, bare-key shape, size and manifest declaration.
func (s *ProtocolV2SecretServiceServer) secretWriteTarget(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	rawSecretID string,
	valueBytes int,
) (string, *protocolv2.ErrorDetail) {
	if s == nil || s.secrets == nil || s.settings == nil {
		return "", protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_unavailable", "Secret Store is not configured.")
	}
	secretID := strings.TrimSpace(rawSecretID)
	if secretID == "" {
		return "", protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_request_invalid", "secret_id is required.")
	}
	// Bare settings key only: the sforum.secret:// form carries a namespace, and a runtime
	// must never be able to address another extension's document through this call.
	if strings.HasPrefix(secretID, secretstore.ReferenceScheme) || strings.Contains(secretID, "/") {
		return "", protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_request_invalid", "secret_id must be a bare settings key.")
	}
	if valueBytes > secretstore.MaxPlaintextBytes {
		return "", protocolV2ResourceFailure(secretstore.ErrInvalid, "host.secret_request_invalid", "value exceeds the Secret Store size limit.")
	}
	if detail := s.requireSettingsOwn(ctx, identity.GetExtensionId()); detail != nil {
		return "", detail
	}
	if !s.settings.HasSecretField(identity.GetExtensionId(), secretID) {
		return "", &protocolv2.ErrorDetail{
			Code:    protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "host.secret_field_not_declared",
			Message: "The manifest does not declare this key as a secret setting.",
		}
	}
	return secretID, nil
}

// requireSettingsOwn re-checks the capability the read path already requires: writing a
// settings secret is the same power as reading one, and both come from `settings.own`.
func (s *ProtocolV2SecretServiceServer) requireSettingsOwn(
	ctx context.Context,
	extensionID string,
) *protocolv2.ErrorDetail {
	if s.capabilities == nil {
		return &protocolv2.ErrorDetail{
			Code:    protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason:  "host.capability_unavailable",
			Message: "The Host capability resolver is not configured.",
		}
	}
	caps, err := s.capabilities.CapabilitiesFor(ctx, extensionID)
	if err != nil {
		return &protocolv2.ErrorDetail{
			Code:    protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason:  "host.capability_unavailable",
			Message: "The Host capability resolver is unavailable.",
		}
	}
	if err := caps.Require(capabilities.SettingsOwn); err != nil {
		return &protocolv2.ErrorDetail{
			Code:    protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			Reason:  "host.secret_denied",
			Message: "The exact runtime does not hold settings.own.",
		}
	}
	return nil
}

// settingsActor is the audit actor for a plugin-initiated settings write. It names the
// extension, never a user: the Host has no actor context on this boundary.
func settingsActor(extensionID string) string {
	return "extension:" + strings.ToLower(strings.TrimSpace(extensionID))
}

// ProtocolV2FileServiceServer exposes PluginFiles under exact runtime ownership.
type ProtocolV2FileServiceServer struct {
	hostv2.UnimplementedFileServiceServer
	files *pluginfiles.Service
}

// NewProtocolV2FileServiceServer builds the Protocol V2 File boundary.
func NewProtocolV2FileServiceServer(files *pluginfiles.Service) (*ProtocolV2FileServiceServer, error) {
	if files == nil {
		return nil, errors.New("hostapi: plugin files service is required")
	}
	return &ProtocolV2FileServiceServer{files: files}, nil
}

// Read streams file bytes for the attested extension only.
func (s *ProtocolV2FileServiceServer) Read(
	request *hostv2.FileReadRequest,
	stream hostv2.FileService_ReadServer,
) error {
	ctx := stream.Context()
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	if detail != nil {
		return statusFromResourceDetail(detail)
	}
	kind, rel, userID, err := parseFileID(request.GetFileId())
	if err != nil {
		return statusFromResourceDetail(protocolV2ResourceFailure(err, "host.file_request_invalid", "file_id is invalid."))
	}
	data, _, err := s.files.Read(pluginfiles.ReadRequest{
		ExtensionID: identity.GetExtensionId(), Kind: kind, RelativePath: rel, UserID: userID,
	})
	if err != nil {
		return statusFromResourceDetail(protocolV2FileFailure(err))
	}
	offset := int(request.GetOffset())
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	chunk := data[offset:]
	if limit := int(request.GetLimit()); limit > 0 && limit < len(chunk) {
		chunk = chunk[:limit]
	}
	const maxChunk = 64 * 1024
	var seq uint64
	for len(chunk) > 0 {
		n := len(chunk)
		if n > maxChunk {
			n = maxChunk
		}
		seq++
		final := n == len(chunk)
		if err := stream.Send(&protocolv2.DataChunk{
			Sequence: seq, Data: append([]byte(nil), chunk[:n]...), Final: final,
		}); err != nil {
			return err
		}
		chunk = chunk[n:]
	}
	if seq == 0 {
		// Empty file: one final empty chunk.
		return stream.Send(&protocolv2.DataChunk{Sequence: 1, Final: true})
	}
	return nil
}

// Write accepts a client stream and stores under the extension namespace.
func (s *ProtocolV2FileServiceServer) Write(stream hostv2.FileService_WriteServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return statusFromResourceDetail(protocolV2ResourceFailure(pluginfiles.ErrInvalid, "host.file_write_open_required", "Write open frame is required."))
	}
	identity, detail := protocolV2ResourceCaller(ctx, open.GetContext())
	if detail != nil {
		return statusFromResourceDetail(detail)
	}
	kind, rel, userID, err := parseFileID(open.GetFileId())
	if err != nil {
		return statusFromResourceDetail(protocolV2ResourceFailure(err, "host.file_request_invalid", "file_id is invalid."))
	}
	var buf bytes.Buffer
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if chunk := frame.GetChunk(); chunk != nil {
			if _, wErr := buf.Write(chunk.GetData()); wErr != nil {
				return wErr
			}
			if int64(buf.Len()) > pluginfiles.MaxWriteBytes {
				return statusFromResourceDetail(protocolV2FileFailure(pluginfiles.ErrTooLarge))
			}
		}
	}
	info, err := s.files.Write(pluginfiles.WriteRequest{
		ExtensionID: identity.GetExtensionId(), Kind: kind, RelativePath: rel, UserID: userID,
		Data: buf.Bytes(), Actor: identity.GetExtensionId(),
	})
	if err != nil {
		return statusFromResourceDetail(protocolV2FileFailure(err))
	}
	return stream.SendAndClose(&hostv2.FileWriteResponse{
		Context: protocolV2ResourceResponseContext(open.GetContext(), identity),
		Size:    uint64(info.Size),
	})
}

// Delete removes a relative path for the attested extension.
func (s *ProtocolV2FileServiceServer) Delete(
	ctx context.Context,
	request *hostv2.FileDeleteRequest,
) (*hostv2.FileDeleteResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.FileDeleteResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	kind, rel, userID, err := parseFileID(request.GetFileId())
	if err != nil {
		response.Error = protocolV2ResourceFailure(err, "host.file_request_invalid", "file_id is invalid.")
		return response, nil
	}
	if err := s.files.Delete(pluginfiles.DeleteRequest{
		ExtensionID: identity.GetExtensionId(), Kind: kind, RelativePath: rel, UserID: userID,
		Actor: identity.GetExtensionId(),
	}); err != nil {
		response.Error = protocolV2FileFailure(err)
		return response, nil
	}
	return response, nil
}

// Stat returns metadata without absolute host paths.
func (s *ProtocolV2FileServiceServer) Stat(
	ctx context.Context,
	request *hostv2.FileStatRequest,
) (*hostv2.FileStatResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.FileStatResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	kind, rel, userID, err := parseFileID(request.GetFileId())
	if err != nil {
		response.Error = protocolV2ResourceFailure(err, "host.file_request_invalid", "file_id is invalid.")
		return response, nil
	}
	_, info, err := s.files.Read(pluginfiles.ReadRequest{
		ExtensionID: identity.GetExtensionId(), Kind: kind, RelativePath: rel, UserID: userID,
	})
	if err != nil {
		response.Error = protocolV2FileFailure(err)
		return response, nil
	}
	response.Exists = true
	response.Size = uint64(info.Size)
	return response, nil
}

// ProtocolV2HttpServiceServer exposes HostHTTP with SSRF and SecretStore injection.
type ProtocolV2HttpServiceServer struct {
	hostv2.UnimplementedHttpServiceServer
	http *hosthttp.Client
}

// NewProtocolV2HttpServiceServer builds the Protocol V2 HTTP boundary.
func NewProtocolV2HttpServiceServer(client *hosthttp.Client) (*ProtocolV2HttpServiceServer, error) {
	if client == nil {
		return nil, errors.New("hostapi: host http client is required")
	}
	return &ProtocolV2HttpServiceServer{http: client}, nil
}

// Do executes a policy-checked outbound request.
// PolicyId may be a secret ref (sforum.secret://ns/id) for credential injection.
func (s *ProtocolV2HttpServiceServer) Do(
	ctx context.Context,
	request *hostv2.HttpRequest,
) (*hostv2.HttpResponse, error) {
	identity, detail := protocolV2ResourceCaller(ctx, request.GetContext())
	response := &hostv2.HttpResponse{Context: protocolV2ResourceResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	headers := map[string]string{}
	for _, h := range request.GetHeaders() {
		if h == nil {
			continue
		}
		name := strings.TrimSpace(h.GetName())
		if name == "" {
			continue
		}
		// 插件不得自带 Authorization；凭证只允许 SecretStore 注入。
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		values := h.GetValues()
		if len(values) == 0 {
			continue
		}
		headers[name] = values[0]
	}
	req := hosthttp.Request{
		Method:      request.GetMethod(),
		URL:         request.GetUrl(),
		Headers:     headers,
		Body:        append([]byte(nil), request.GetBody()...),
		ExtensionID: identity.GetExtensionId(),
		Actor:       identity.GetExtensionId(),
		TraceID:     request.GetContext().GetRequestId(),
		Authority:   hosthttp.AuthoritySafe,
	}
	if request.GetTimeout() != nil {
		req.Timeout = request.GetTimeout().AsDuration()
	}
	if policy := strings.TrimSpace(request.GetPolicyId()); policy != "" {
		if strings.HasPrefix(policy, secretstore.ReferenceScheme) {
			req.SecretRef = policy
			req.SecretPurpose = "http.credential"
		} else if policy == hosthttp.AuthorityRaw {
			req.Authority = hosthttp.AuthorityRaw
		}
	}
	result, err := s.http.Do(ctx, req)
	if err != nil {
		response.Error = protocolV2HTTPFailure(err)
		response.StatusCode = uint32(result.StatusCode)
		return response, nil
	}
	response.StatusCode = uint32(result.StatusCode)
	response.Body = append([]byte(nil), result.Body...)
	for k, v := range result.Headers {
		response.Headers = append(response.Headers, &protocolv2.Header{Name: k, Values: []string{v}})
	}
	return response, nil
}

// Stream is not implemented for the unary-safe production path (fail closed).
func (s *ProtocolV2HttpServiceServer) Stream(_ hostv2.HttpService_StreamServer) error {
	return statusFromResourceDetail(protocolV2ResourceFailure(
		hosthttp.ErrInvalid, "host.http_stream_unsupported", "HTTP streaming is not enabled on this Host.",
	))
}

func protocolV2ResourceCaller(
	ctx context.Context,
	request *protocolv2.RequestContext,
) (*protocolv2.ExtensionIdentity, *protocolv2.ErrorDetail) {
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if identity == nil || strings.TrimSpace(identity.GetExtensionId()) == "" {
		return nil, &protocolv2.ErrorDetail{
			Code:    protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			Reason:  "host.runtime_unattested",
			Message: "The call is not bound to an exact runtime identity.",
		}
	}
	// 请求内 Extension 若与 attested 不一致则 stale（防伪造）。
	if request != nil && request.GetExtension() != nil {
		claimed := request.GetExtension()
		if claimed.GetExtensionId() != identity.GetExtensionId() ||
			claimed.GetArtifactDigest() != identity.GetArtifactDigest() ||
			claimed.GetInstanceId() != identity.GetInstanceId() {
			return identity, &protocolv2.ErrorDetail{
				Code:    protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME,
				Reason:  "host.runtime_stale",
				Message: "The request extension identity does not match the exact runtime.",
			}
		}
	}
	return identity, nil
}

func protocolV2ResourceResponseContext(
	request *protocolv2.RequestContext,
	identity *protocolv2.ExtensionIdentity,
) *protocolv2.ResponseContext {
	response := &protocolv2.ResponseContext{ServerTime: timestamppb.Now()}
	if request != nil {
		response.RequestId = request.GetRequestId()
		if request.GetTrace() != nil {
			response.Trace = proto.Clone(request.GetTrace()).(*protocolv2.TraceContext)
		}
	}
	if identity != nil {
		response.Extension = proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	}
	return response
}

func protocolV2ResourceFailure(err error, reason, message string) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: reason, Message: message}
	switch {
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	}
	return detail
}

func protocolV2SecretFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: "host.secret_failed", Message: "The secret operation failed."}
	switch {
	case errors.Is(err, secretstore.ErrPermissionDenied), errors.Is(err, secretstore.ErrNamespaceDenied), errors.Is(err, secretstore.ErrPurposeDenied):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.secret_denied"
		detail.Message = "The exact runtime is not allowed to resolve this secret."
	case errors.Is(err, secretstore.ErrNotFound), errors.Is(err, secretstore.ErrRevoked):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND
		detail.Reason = "host.secret_not_found"
		detail.Message = "The secret is not found or has been revoked."
	case errors.Is(err, secretstore.ErrInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.secret_request_invalid"
	case errors.Is(err, secretstore.ErrCipher), errors.Is(err, secretstore.ErrCipherRequired):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.secret_cipher_failed"
		detail.Message = "Secret decryption failed."
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	}
	return detail
}

// protocolV2SecretWriteFailure maps settings/secret write failures. The reason codes are
// part of the contract a plugin branches on: a conflict is retryable, a missing schema is
// not (the package and the enabled schema disagree).
func protocolV2SecretWriteFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: "host.secret_write_failed", Message: "The secret write failed."}
	switch {
	case errors.Is(err, settingslifecycle.ErrConflict):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CONFLICT
		detail.Reason = "host.settings_conflict"
		detail.Message = "The settings document changed underneath this write; retry once."
		detail.Retryable = true
	case errors.Is(err, settingslifecycle.ErrValidation):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.secret_field_not_declared"
		detail.Message = "The manifest does not declare this key as a secret setting."
	case errors.Is(err, settingslifecycle.ErrInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.secret_request_invalid"
	case errors.Is(err, settingslifecycle.ErrNotFound):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.secret_settings_unavailable"
		detail.Message = "The extension has no registered settings schema."
	case errors.Is(err, settingslifecycle.ErrPermissionDenied):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.secret_denied"
	case errors.Is(err, secretstore.ErrCipher), errors.Is(err, secretstore.ErrCipherRequired):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.secret_cipher_failed"
		detail.Message = "Secret encryption failed."
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	}
	return detail
}

func protocolV2FileFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: "host.file_failed", Message: "The file operation failed."}
	switch {
	case errors.Is(err, pluginfiles.ErrPermissionDenied):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.file_denied"
	case errors.Is(err, pluginfiles.ErrNotFound):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND
		detail.Reason = "host.file_not_found"
	case errors.Is(err, pluginfiles.ErrTraversal), errors.Is(err, pluginfiles.ErrSymlink):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.file_path_denied"
		detail.Message = "The file path escapes the plugin namespace."
	case errors.Is(err, pluginfiles.ErrQuotaExceeded), errors.Is(err, pluginfiles.ErrTooLarge):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE
		detail.Reason = "host.file_quota"
	case errors.Is(err, pluginfiles.ErrInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.file_request_invalid"
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	}
	return detail
}

func protocolV2HTTPFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: "host.http_failed", Message: "The outbound HTTP call failed."}
	switch {
	case errors.Is(err, hosthttp.ErrSSRF):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.http_ssrf"
		detail.Message = "The URL is not allowed by Host SSRF policy."
	case errors.Is(err, hosthttp.ErrRawDenied):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.http_raw_denied"
	case errors.Is(err, hosthttp.ErrSecret):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.http_secret_denied"
	case errors.Is(err, hosthttp.ErrResponseTooLarge):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE
		detail.Reason = "host.http_response_too_large"
	case errors.Is(err, hosthttp.ErrInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE
		detail.Retryable = true
	}
	return detail
}

func statusFromResourceDetail(detail *protocolv2.ErrorDetail) error {
	if detail == nil {
		return status.Error(codes.Internal, "host resource failure")
	}
	code := codes.Internal
	switch detail.GetCode() {
	case protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT:
		code = codes.InvalidArgument
	case protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED:
		code = codes.PermissionDenied
	case protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND:
		code = codes.NotFound
	case protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME:
		code = codes.FailedPrecondition
	case protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		code = codes.DeadlineExceeded
	case protocolv2.ErrorCode_ERROR_CODE_CANCELLED:
		code = codes.Canceled
	case protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE:
		code = codes.Unavailable
	case protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE:
		code = codes.ResourceExhausted
	}
	msg := detail.GetMessage()
	if msg == "" {
		msg = detail.GetReason()
	}
	return status.Error(code, msg)
}

func parsePluginSecretRef(extensionID, secretID string) (secretstore.Ref, error) {
	secretID = strings.TrimSpace(secretID)
	if strings.HasPrefix(secretID, secretstore.ReferenceScheme) {
		return secretstore.ParseReference(secretID)
	}
	// Bare id resolves in the caller's own namespace.
	return secretstore.Ref{Namespace: strings.ToLower(strings.TrimSpace(extensionID)), SecretID: secretID}, nil
}

// file_id formats:
//   - "private/rel/path"
//   - "temp/rel/path"
//   - "static/rel/path"
//   - "user/<userId>/rel/path"
func parseFileID(fileID string) (kind, rel, userID string, err error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", "", "", pluginfiles.ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(fileID, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", pluginfiles.ErrInvalid
	}
	kind = strings.ToLower(parts[0])
	switch kind {
	case pluginfiles.KindPrivate, pluginfiles.KindTemp, pluginfiles.KindStatic:
		rel = strings.Join(parts[1:], "/")
	case pluginfiles.KindUser:
		if len(parts) < 3 {
			return "", "", "", pluginfiles.ErrInvalid
		}
		userID = parts[1]
		rel = strings.Join(parts[2:], "/")
	default:
		return "", "", "", pluginfiles.ErrInvalid
	}
	return kind, rel, userID, nil
}

var (
	_ hostv2.SecretServiceServer = (*ProtocolV2SecretServiceServer)(nil)
	_ hostv2.FileServiceServer   = (*ProtocolV2FileServiceServer)(nil)
	_ hostv2.HttpServiceServer   = (*ProtocolV2HttpServiceServer)(nil)
)
