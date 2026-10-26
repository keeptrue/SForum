package main

import (
	"context"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	contentPolicyHookKind         = "filter"
	contentPolicyHookInputSchema  = "sforum.content-policy.hook-input@1"
	contentPolicyHookResultSchema = "sforum.content-policy.hook-result@1"
	contentPolicyHookPatchSchema  = "sforum.content-policy.hook-result.patch@1"

	contentPolicyServiceID             = "sforum.content-policy.service.evaluate"
	contentPolicyServiceVersion        = "1.0.0"
	contentPolicyServiceRequestSchema  = "sforum.content-policy.service.evaluate.request@1"
	contentPolicyServiceResponseSchema = "sforum.content-policy.service.evaluate.response@1"
)

type contentPolicyHookDeclaration struct {
	id              string
	name            string
	kind            string
	contractVersion string
	inputSchema     string
	resultSchema    string
}

var contentPolicyHooks = map[string]contentPolicyHookDeclaration{
	"topic.before_create": {
		id: "sforum.content-policy.event.topic-before-create", name: "topic.before_create", kind: contentPolicyHookKind,
		contractVersion: "sforum.content-policy.event.topic-before-create@1",
		inputSchema:     contentPolicyHookInputSchema, resultSchema: contentPolicyHookResultSchema,
	},
	"topic.before_update": {
		id: "sforum.content-policy.event.topic-before-update", name: "topic.before_update", kind: contentPolicyHookKind,
		contractVersion: "sforum.content-policy.event.topic-before-update@1",
		inputSchema:     contentPolicyHookInputSchema, resultSchema: contentPolicyHookResultSchema,
	},
	"comment.before_create": {
		id: "sforum.content-policy.event.comment-before-create", name: "comment.before_create", kind: contentPolicyHookKind,
		contractVersion: "sforum.content-policy.event.comment-before-create@1",
		inputSchema:     contentPolicyHookInputSchema, resultSchema: contentPolicyHookResultSchema,
	},
}

// contentPolicyPluginV2 是默认 gRPC 运行时；纯策略判断仍与 v1 共用，便于显式回滚。
type contentPolicyPluginV2 struct {
	*pluginv2.Server
}

func newContentPolicyPluginV2() (*contentPolicyPluginV2, error) {
	services, err := pluginv2.NewServiceRegistry(pluginv2.ServiceDefinition{
		ServiceID: contentPolicyServiceID, Version: contentPolicyServiceVersion,
		RequestSchemaID: contentPolicyServiceRequestSchema, ResponseSchemaID: contentPolicyServiceResponseSchema,
		Operations: []pluginv2.ServiceOperation{{Name: "evaluate", Unary: evaluateContentPolicyService}},
	})
	if err != nil {
		return nil, err
	}
	return &contentPolicyPluginV2{Server: pluginv2.NewServer().WithServiceRegistry(services)}, nil
}

func (p *contentPolicyPluginV2) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	response := &pluginwire.HookResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(),
			Extension: request.GetContext().GetExtension(),
		},
		Accepted: true,
	}
	// SDK 的默认 Health 校验与握手绑定的精确制品身份，避免覆盖 RPC 后绕过 stale-runtime 检查。
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Accepted = false
		response.Error = health.GetError()
		return response, nil
	}

	declaration, ok := contentPolicyHooks[request.GetHookName()]
	if !ok {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			"content_policy.hook_not_found", "Content policy does not declare the requested hook."), nil
	}
	if request.GetHookId() != declaration.id {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"content_policy.hook_id_mismatch", "Hook id does not match the content-policy manifest declaration."), nil
	}
	if request.GetHookKind() != declaration.kind {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"content_policy.hook_kind_mismatch", "Hook kind does not match the content-policy manifest declaration."), nil
	}
	if request.GetContractVersion() != declaration.contractVersion {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"content_policy.hook_contract_mismatch", "Hook contract does not match the content-policy manifest declaration."), nil
	}
	if !contentPolicyDocumentMatches(request.GetPayload(), declaration.inputSchema) {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"content_policy.hook_input_schema_mismatch", "Hook payload does not match the content-policy manifest input schema."), nil
	}
	if request.GetPayload().GetValue() == nil {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"content_policy.hook_payload_required", "Hook payload requires a typed object value."), nil
	}
	if !contentPolicyEvent(declaration.name) {
		// 声明表与策略支持列表必须一起变更；运行时遇到漂移时拒绝而不是静默放行。
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"content_policy.hook_declaration_invalid", "Content-policy hook declaration is not implemented."), nil
	}
	payload := map[string]any{}
	if request.GetPayload().GetValue() != nil {
		payload = request.GetPayload().GetValue().AsMap()
	}
	decision := evaluateContent(
		loadPolicyConfigFromEnv(),
		request.GetHookName(),
		payloadString(payload, "title"),
		payloadString(payload, "content"),
	)
	response.Accepted = decision.OK
	result, err := structpb.NewStruct(map[string]any{
		"reason":  decision.Reason,
		"message": decision.Message,
	})
	if err != nil {
		return nil, err
	}
	response.Result = contentPolicyDocument(declaration.resultSchema, result)
	if decision.PatchTag == "" {
		return response, nil
	}
	if !stringListContains(request.GetMutableFields(), "tagSlugs") {
		return contentPolicyHookFailure(response, protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"content_policy.hook_patch_forbidden", "Host did not authorize the tagSlugs hook patch."), nil
	}

	patch, err := structpb.NewStruct(map[string]any{
		"tagSlugs": stringSliceValues(mergeTagSlugs(payload["tagSlugs"], decision.PatchTag)),
	})
	if err != nil {
		return nil, err
	}
	response.Patch = contentPolicyDocument(contentPolicyHookPatchSchema, patch)
	return response, nil
}

// evaluateContentPolicyService 让其他可信插件复用同一策略判断，而不必复制关键词规则。
func evaluateContentPolicyService(_ context.Context, call *pluginv2.ServiceCall) (*protocolwire.TypedDocument, error) {
	payload := map[string]any{}
	if call != nil && call.Input != nil && call.Input.GetValue() != nil {
		payload = call.Input.GetValue().AsMap()
	}
	eventName := payloadString(payload, "eventName")
	if !contentPolicyEvent(eventName) {
		return nil, &pluginv2.ServiceError{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "content_policy.event_invalid", Message: "Content policy service requires a supported eventName.",
		}
	}
	decision := evaluateContent(
		loadPolicyConfigFromEnv(), eventName,
		payloadString(payload, "title"), payloadString(payload, "content"),
	)
	result := map[string]any{
		"accepted": decision.OK,
		"reason":   decision.Reason,
		"message":  decision.Message,
	}
	if decision.PatchTag != "" {
		result["patch"] = map[string]any{
			"tagSlugs": stringSliceValues(mergeTagSlugs(payload["tagSlugs"], decision.PatchTag)),
		}
	}
	value, err := structpb.NewStruct(result)
	if err != nil {
		return nil, err
	}
	return contentPolicyDocument(contentPolicyServiceResponseSchema, value), nil
}

func contentPolicyHookFailure(response *pluginwire.HookResponse, code protocolwire.ErrorCode, reason, message string) *pluginwire.HookResponse {
	response.Accepted = false
	response.Result = nil
	response.Patch = nil
	response.Error = &protocolwire.ErrorDetail{Code: code, Reason: reason, Message: message}
	return response
}

func contentPolicyDocument(schemaRef string, value *structpb.Struct) *protocolwire.TypedDocument {
	id, version, ok := strings.Cut(schemaRef, "@")
	if !ok {
		return &protocolwire.TypedDocument{Value: value}
	}
	return &protocolwire.TypedDocument{SchemaId: id, SchemaVersion: version, Value: value}
}

func contentPolicyDocumentMatches(document *protocolwire.TypedDocument, schemaRef string) bool {
	id, version, ok := strings.Cut(schemaRef, "@")
	return ok && document != nil && document.GetSchemaId() == id && document.GetSchemaVersion() == version
}

func stringListContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func stringSliceValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func contentPolicyEvent(name string) bool {
	switch name {
	case "topic.before_create", "topic.before_update", "comment.before_create":
		return true
	default:
		return false
	}
}
