package main

import (
	"context"
	"errors"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	surfacePropsSchema  = "sforum.admin-surface-reference.surface.props@1"
	surfaceResultSchema = "sforum.admin-surface-reference.surface.result@1"
)

type surfaceDeclaration struct {
	id        string
	handler   string
	kind      string
	operation string
}

var surfaceDeclarations = declarationsByID(
	querySurface("navigation", "navigation"),
	querySurface("dashboard", "dashboard"),
	querySurface("list-column", "list_column"),
	querySurface("list-filter", "list_filter"),
	querySurface("row-action-view", "row_action"),
	commandSurface("row-action-command", "row_action"),
	querySurface("bulk-action-view", "bulk_action"),
	commandSurface("bulk-action-command", "bulk_action"),
	querySurface("form-view", "form"),
	commandSurface("form-command", "form"),
	querySurface("notice", "notice"),
	querySurface("editor-view", "editor_panel"),
	commandSurface("editor-command", "editor_panel"),
	querySurface("detail", "detail_region"),
	querySurface("importer-view", "importer"),
	commandSurface("importer-command", "importer"),
	querySurface("exporter", "exporter"),
)

type adminSurfaceReferencePlugin struct {
	*pluginv2.Server
}

func newAdminSurfaceReferencePlugin() (*adminSurfaceReferencePlugin, error) {
	return &adminSurfaceReferencePlugin{Server: pluginv2.NewServer()}, nil
}

func (p *adminSurfaceReferencePlugin) InvokeHook(
	ctx context.Context,
	request *pluginwire.HookRequest,
) (*pluginwire.HookResponse, error) {
	response := &pluginwire.HookResponse{Context: responseContext(request), Accepted: false}
	if request == nil || request.GetContext() == nil {
		return surfaceFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"admin_reference.context_required", "The admin surface request context is required."), nil
	}
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}
	declaration, ok := surfaceDeclarations[request.GetHookId()]
	if !ok {
		return surfaceFailure(response, protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			"admin_reference.surface_not_found", "The requested admin surface is not declared."), nil
	}
	if request.GetHookKind() != "admin_surface" || request.GetHookName() != declaration.handler ||
		request.GetContractVersion() != declaration.id+"@1" || !documentMatches(request.GetPayload(), surfacePropsSchema) {
		return surfaceFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"admin_reference.contract_mismatch", "The admin surface request does not match its exact declaration."), nil
	}
	if declaration.operation == "command" {
		if err := validateCommandAuthority(request.GetContext()); err != nil {
			return surfaceFailure(response, protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"admin_reference.command_authority_required", err.Error()), nil
		}
	}
	input := map[string]any{}
	if request.GetPayload().GetValue() != nil {
		input = request.GetPayload().GetValue().AsMap()
	}
	result, err := renderAdminSurface(declaration, input, request.GetContext())
	if err != nil {
		return surfaceFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"admin_reference.input_invalid", err.Error()), nil
	}
	value, err := structpb.NewStruct(result)
	if err != nil {
		return nil, err
	}
	response.Accepted = true
	response.Result = typedDocument(surfaceResultSchema, value)
	return response, nil
}

func validateCommandAuthority(request *protocolwire.RequestContext) error {
	if request == nil || request.GetActor().GetUserId() <= 0 {
		return errors.New("an authenticated actor is required")
	}
	if strings.TrimSpace(request.GetIdempotencyKey()) == "" {
		return errors.New("an idempotency key is required")
	}
	return nil
}

func surfaceFailure(
	response *pluginwire.HookResponse,
	code protocolwire.ErrorCode,
	reason, message string,
) *pluginwire.HookResponse {
	response.Accepted = false
	response.Result = nil
	response.Patch = nil
	response.Error = &protocolwire.ErrorDetail{Code: code, Reason: reason, Message: message}
	return response
}

func responseContext(request *pluginwire.HookRequest) *protocolwire.ResponseContext {
	if request == nil || request.GetContext() == nil {
		return &protocolwire.ResponseContext{ServerTime: timestamppb.Now()}
	}
	return &protocolwire.ResponseContext{
		RequestId:  request.GetContext().GetRequestId(),
		Trace:      request.GetContext().GetTrace(),
		ServerTime: timestamppb.Now(),
		Extension:  request.GetContext().GetExtension(),
	}
}

func typedDocument(schemaRef string, value *structpb.Struct) *protocolwire.TypedDocument {
	id, version, ok := strings.Cut(schemaRef, "@")
	if !ok {
		return &protocolwire.TypedDocument{Value: value}
	}
	return &protocolwire.TypedDocument{SchemaId: id, SchemaVersion: version, Value: value}
}

func documentMatches(document *protocolwire.TypedDocument, schemaRef string) bool {
	id, version, ok := strings.Cut(schemaRef, "@")
	return ok && document != nil && document.GetSchemaId() == id && document.GetSchemaVersion() == version &&
		document.GetValue() != nil
}

func querySurface(suffix, kind string) surfaceDeclaration {
	return newSurfaceDeclaration(suffix, kind, "query")
}

func commandSurface(suffix, kind string) surfaceDeclaration {
	return newSurfaceDeclaration(suffix, kind, "command")
}

func newSurfaceDeclaration(suffix, kind, operation string) surfaceDeclaration {
	id := "sforum.admin-surface-reference.surface." + suffix
	handlerSuffix := strings.ReplaceAll(suffix, "-", "_")
	return surfaceDeclaration{id: id, handler: "admin." + handlerSuffix, kind: kind, operation: operation}
}

func declarationsByID(items ...surfaceDeclaration) map[string]surfaceDeclaration {
	result := make(map[string]surfaceDeclaration, len(items))
	for _, item := range items {
		result[item.id] = item
	}
	return result
}
