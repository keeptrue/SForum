package hostapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strings"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

type protocolV2CommandError struct {
	detail *protocolv2.ErrorDetail
}

func (e *protocolV2CommandError) Error() string {
	if e == nil || e.detail == nil {
		return "Host command failed"
	}
	return e.detail.GetMessage()
}

func newProtocolV2CommandError(code protocolv2.ErrorCode, reason, message string, retryable bool) error {
	return &protocolV2CommandError{detail: commandError(code, reason, message, retryable)}
}

func protocolV2CommandErrorDetail(err error, fallbackReason string) *protocolv2.ErrorDetail {
	var commandErr *protocolV2CommandError
	if errors.As(err, &commandErr) && commandErr.detail != nil {
		return cloneProtocolV2CommandError(commandErr.detail)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return commandError(protocolv2.ErrorCode_ERROR_CODE_CANCELLED, "host.command_cancelled", "The Host command was cancelled.", true)
	case errors.Is(err, context.DeadlineExceeded):
		return commandError(protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, "host.command_deadline_exceeded", "The Host command deadline expired.", true)
	default:
		return commandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, fallbackReason, "The Host command failed.", fallbackReason == "host.command_rolled_back")
	}
}

func validateProtocolV2CommandDocument(document *protocolv2.TypedDocument, schemaID, schemaVersion, label string) *protocolv2.ErrorDetail {
	if schemaID == "" && schemaVersion == "" && document == nil {
		return nil
	}
	if document == nil || document.GetValue() == nil || document.GetSchemaId() != schemaID || document.GetSchemaVersion() != schemaVersion {
		return commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_schema_mismatch", "The command "+label+" does not match its registered schema.", false)
	}
	return nil
}

func protocolV2CommandIdempotencyKey(request *hostv2.CommandRequest, required bool) (string, *protocolv2.ErrorDetail) {
	requestKey := request.GetIdempotencyKey()
	contextKey := request.GetContext().GetIdempotencyKey()
	if requestKey != "" && contextKey != "" && requestKey != contextKey {
		return "", commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_idempotency_mismatch", "Command and request-context idempotency keys must match.", false)
	}
	key := requestKey
	if key == "" {
		key = contextKey
	}
	if key == "" && required {
		return "", commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_idempotency_required", "An idempotency key is required for this command.", false)
	}
	if key != "" && !validProtocolV2CommandIdempotencyKey(key) {
		return "", commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_idempotency_invalid", "The idempotency key must contain 1 to 128 visible ASCII characters.", false)
	}
	return key, nil
}

func validProtocolV2CommandIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func protocolV2CommandFingerprint(request *hostv2.CommandRequest) (string, error) {
	return protocolV2CommandFingerprintWithBinding(request, request.GetContext().GetExtension(), 0)
}

func protocolV2CommandExecutionFingerprint(
	ctx context.Context,
	request *hostv2.CommandRequest,
	delegation *protocolV2VerifiedActorDelegation,
) (string, error) {
	extension := ProtocolV2RuntimeIdentityFromContext(ctx)
	if extension == nil {
		extension = request.GetContext().GetExtension()
	}
	actorUserID := int64(0)
	if delegation != nil {
		actorUserID = delegation.ActorUserID
	}
	return protocolV2CommandFingerprintWithBinding(request, extension, actorUserID)
}

func protocolV2CommandFingerprintWithBinding(
	request *hostv2.CommandRequest,
	extension *protocolv2.ExtensionIdentity,
	actorUserID int64,
) (string, error) {
	contextBinding := &protocolv2.RequestContext{
		Extension: &protocolv2.ExtensionIdentity{
			ExtensionId: extension.GetExtensionId(), ExtensionVersion: extension.GetExtensionVersion(),
			ArtifactDigest: extension.GetArtifactDigest(), TrustGrantId: extension.GetTrustGrantId(),
		},
		GrantedAuthority: cloneProtocolV2Authority(request.GetContext().GetGrantedAuthority()),
	}
	if actorUserID > 0 {
		contextBinding.Actor = &protocolv2.Actor{UserId: actorUserID}
	}
	queryInvalidationTags := slices.Clone(request.GetQueryInvalidationTags())
	sort.Strings(queryInvalidationTags)
	bound := &hostv2.CommandRequest{
		Context: contextBinding, CommandId: strings.TrimSpace(request.GetCommandId()),
		CommandVersion:        strings.TrimSpace(request.GetCommandVersion()),
		ExpectedRevision:      strings.TrimSpace(request.GetExpectedRevision()),
		Input:                 cloneProtocolV2Document(request.GetInput()),
		QueryInvalidationTags: queryInvalidationTags,
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(bound)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func protocolV2CommandPlanID(request *hostv2.CommandRequest, plan *hostv2.CommandPlan) string {
	fingerprint, err := protocolV2CommandFingerprint(request)
	if err != nil {
		return ""
	}
	stable := proto.Clone(plan).(*hostv2.CommandPlan)
	stable.Context = nil
	stable.PlanId = ""
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(stable)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(append([]byte(fingerprint), encoded...))
	return "plan_" + hex.EncodeToString(digest[:12])
}

func newProtocolV2CommandID(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}

func unavailableProtocolV2CommandPlan(request *hostv2.CommandRequest) *hostv2.CommandPlan {
	return &hostv2.CommandPlan{
		Context: protocolV2ResponseContext(request.GetContext()), CommandId: request.GetCommandId(), CommandVersion: request.GetCommandVersion(),
		Error: protocolV2CommandUnavailable(),
	}
}

func protocolV2CommandUnavailable() *protocolv2.ErrorDetail {
	return commandError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.command_backend_unavailable", "Transactional Host Commands are not configured.", true)
}

func commandError(code protocolv2.ErrorCode, reason, message string, retryable bool) *protocolv2.ErrorDetail {
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Retryable: retryable}
}

func cloneProtocolV2Document(value *protocolv2.TypedDocument) *protocolv2.TypedDocument {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.TypedDocument)
}

func cloneProtocolV2CommandError(value *protocolv2.ErrorDetail) *protocolv2.ErrorDetail {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.ErrorDetail)
}

func cloneProtocolV2Authority(values []*protocolv2.AuthorityGrant) []*protocolv2.AuthorityGrant {
	cloned := make([]*protocolv2.AuthorityGrant, 0, len(values))
	for _, value := range values {
		if value != nil {
			cloned = append(cloned, proto.Clone(value).(*protocolv2.AuthorityGrant))
		}
	}
	return cloned
}

func cloneProtocolV2Policies(values []*hostv2.PolicyDecision) []*hostv2.PolicyDecision {
	cloned := make([]*hostv2.PolicyDecision, 0, len(values))
	for _, value := range values {
		if value != nil {
			cloned = append(cloned, proto.Clone(value).(*hostv2.PolicyDecision))
		} else {
			cloned = append(cloned, nil)
		}
	}
	return cloned
}

func cloneProtocolV2Impact(values []*hostv2.ImpactItem) []*hostv2.ImpactItem {
	cloned := make([]*hostv2.ImpactItem, 0, len(values))
	for _, value := range values {
		if value != nil {
			cloned = append(cloned, proto.Clone(value).(*hostv2.ImpactItem))
		}
	}
	return cloned
}
