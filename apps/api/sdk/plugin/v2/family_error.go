package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"time"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FamilyError 是 hooks/providers/commands/jobs 作者 handler 的稳定应用错误。
// 字段与 ServiceError 对齐：Code/Reason/Message/Retryable/RetryAfter/Metadata。
type FamilyError struct {
	Code       protocolwire.ErrorCode
	Reason     string
	Message    string
	Retryable  bool
	RetryAfter time.Time
	Metadata   map[string]string
}

func (e *FamilyError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return e.Reason + ": " + e.Message
	}
	return e.Message
}

func newFamilyError(code protocolwire.ErrorCode, reason, message string) *FamilyError {
	return &FamilyError{Code: code, Reason: reason, Message: message}
}

func familyErrorDetail(err error, fallbackReason, fallbackMessage string) *protocolwire.ErrorDetail {
	var familyErr *FamilyError
	if !errors.As(err, &familyErr) || familyErr == nil {
		// 非 FamilyError：清洗为稳定内部错误，不泄露实现细节。
		familyErr = newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL, fallbackReason, fallbackMessage)
	}
	code := familyErr.Code
	if code == protocolwire.ErrorCode_ERROR_CODE_UNSPECIFIED {
		code = protocolwire.ErrorCode_ERROR_CODE_INTERNAL
	}
	detail := &protocolwire.ErrorDetail{
		Code: code, Reason: familyErr.Reason, Message: familyErr.Message, Retryable: familyErr.Retryable,
	}
	if !familyErr.RetryAfter.IsZero() {
		detail.RetryAfter = timestamppb.New(familyErr.RetryAfter)
	}
	if len(familyErr.Metadata) > 0 {
		detail.Metadata = make(map[string]string, len(familyErr.Metadata))
		for key, value := range familyErr.Metadata {
			detail.Metadata[key] = value
		}
	}
	return detail
}

func validateFamilyRequestContext(request *protocolwire.RequestContext, family string) *protocolwire.ErrorDetail {
	if request == nil || request.GetRequestId() == "" || request.GetExtension() == nil {
		return &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: family + ".context_required", Message: "Request id and exact extension identity are required.",
		}
	}
	deadline := request.GetDeadline()
	if deadline == nil || !deadline.IsValid() {
		return &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: family + ".deadline_required", Message: "A valid request deadline is required.",
		}
	}
	if !deadline.AsTime().After(time.Now()) {
		return &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED,
			Reason: family + ".deadline_expired", Message: "The request deadline has expired.",
		}
	}
	return nil
}

// bindRequestContextDeadline 将 RequestContext.deadline 与 gRPC ctx 取更严（更早）的一侧。
// 调用方必须 defer cancel。
func bindRequestContextDeadline(ctx context.Context, request *protocolwire.RequestContext) (context.Context, context.CancelFunc) {
	if request == nil {
		return context.WithCancel(ctx)
	}
	deadlineTS := request.GetDeadline()
	if deadlineTS == nil || !deadlineTS.IsValid() {
		return context.WithCancel(ctx)
	}
	deadline := deadlineTS.AsTime()
	if ctxDeadline, ok := ctx.Deadline(); ok && !ctxDeadline.After(deadline) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

func validateBoundDocument(document *protocolwire.TypedDocument, schemaRef, family, label string) error {
	if schemaRef == "" {
		if document == nil {
			return nil
		}
		return newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			family+".schema_unexpected", fmt.Sprintf("%s %s was not expected.", family, label))
	}
	if !DocumentMatchesSchema(document, schemaRef) {
		code := protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if label == "result" || label == "output" || label == "patch" {
			code = protocolwire.ErrorCode_ERROR_CODE_INTERNAL
		}
		return newFamilyError(code, family+".schema_mismatch",
			fmt.Sprintf("%s %s must match schema %s.", family, label, schemaRef))
	}
	return nil
}

// filterPatchSchemaRef 对齐 Host protocolV2PatchSchemaRef：resultSchemaID + ".patch@" + version。
// Manifest 没有独立 PatchSchema 字段；filter patch 由 resultSchema 派生。
func filterPatchSchemaRef(resultSchema string) (string, bool) {
	schemaID, version, ok := SplitSchemaRef(resultSchema)
	if !ok {
		return "", false
	}
	return schemaID + ".patch@" + version, true
}

// stringSlicesEqual 要求顺序与内容完全一致（Host 冻结列表按声明顺序重放）。
func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
