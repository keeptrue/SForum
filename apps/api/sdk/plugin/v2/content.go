package pluginv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	ContentRuntimeProviderSlot    = "sforum.content"
	ContentRuntimeFeatureName     = "content.runtime"
	ContentRuntimeFeatureVersion  = "1"
	ContentRuntimeFeatureContract = ContentRuntimeFeatureName + "@" + ContentRuntimeFeatureVersion
	contentCallRequestSchema      = "sforum.content.call.request@1"
	contentCallResponseSchema     = "sforum.content.call.response@1"
	ContentEditorDocumentSchema   = "sforum.editor-document@1"
	ContentSerializedSchema       = "sforum.serialized-content@1"
	ContentRenderSegmentsSchema   = "sforum.render-segments@1"
	ContentKindBlock              = "block"
	ContentKindShortcode          = "shortcode"
	ContentKindEmbed              = "embed"
	ContentKindNode               = "node"
	ContentKindMark               = "mark"
	ContentKindRenderFilter       = "render_filter"
	ContentKindSanitizer          = "sanitizer"
	ContentActionAdd              = "add"
	ContentActionBefore           = "before"
	ContentActionAfter            = "after"
	ContentActionWrap             = "wrap"
	ContentActionReplace          = "replace"
	ContentActionFilter           = "filter"
	ContentOperationEditor        = "editor"
	ContentOperationValidator     = "validator"
	ContentOperationSerializer    = "serializer"
	ContentOperationRenderer      = "renderer"
	ContentOperationFilter        = "filter"
	ContentSegmentHTML            = "html"
	ContentSegmentText            = "text"
	ContentSegmentUnsupported     = "unsupported"
)

var (
	ErrInvalidContentDefinition = errors.New("invalid plugin content definition")
	contentIDPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	contentContractPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

func ContentRuntimeProtocolFeature() *protocolwire.ProtocolFeature {
	return &protocolwire.ProtocolFeature{Name: ContentRuntimeFeatureName, Version: ContentRuntimeFeatureVersion}
}

type ContentDeclaration struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Kind            string `json:"kind"`
	Handler         string `json:"handler"`
	Schema          string `json:"schema"`
	Renderer        string `json:"renderer,omitempty"`
	Migration       string `json:"migration,omitempty"`
}

type ContentEditorDocument struct {
	SchemaVersion   string          `json:"schemaVersion"`
	ContentID       string          `json:"contentId"`
	ContractVersion string          `json:"contractVersion"`
	Schema          string          `json:"schema"`
	StorageVersion  string          `json:"storageVersion"`
	Value           json.RawMessage `json:"value"`
}

type ContentSerializedDocument struct {
	SchemaVersion   string `json:"schemaVersion"`
	ContentID       string `json:"contentId"`
	ContractVersion string `json:"contractVersion"`
	StorageVersion  string `json:"storageVersion"`
	MediaType       string `json:"mediaType"`
	Data            []byte `json:"data"`
}

type ContentRenderSegment struct {
	Kind string `json:"kind"`
	HTML string `json:"html,omitempty"`
	Text string `json:"text,omitempty"`
}

type ContentRenderSegments struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	ContentID       string                 `json:"contentId"`
	ContractVersion string                 `json:"contractVersion"`
	TextEncoding    string                 `json:"textEncoding,omitempty"`
	Segments        []ContentRenderSegment `json:"segments"`
	PlainText       string                 `json:"plainText"`
}

type ContentCall struct {
	Context    *protocolwire.RequestContext
	Target     ContentDeclaration
	Provider   ContentDeclaration
	Action     string
	Operation  string
	Document   ContentEditorDocument
	Serialized ContentSerializedDocument
	Render     ContentRenderSegments
	ResourceID string
	Locale     string
	Scope      string
}

type ContentResult struct {
	Document   *ContentEditorDocument     `json:"document,omitempty"`
	Serialized *ContentSerializedDocument `json:"serialized,omitempty"`
	Render     *ContentRenderSegments     `json:"render,omitempty"`
}

type ContentHandler func(context.Context, *ContentCall) (ContentResult, error)

type ContentDefinition struct {
	ID              string
	ContractVersion string
	Kind            string
	Handler         string
	Schema          string
	Renderer        string
	Migration       string
	Execute         ContentHandler
}

type contentCallRequest struct {
	Target     ContentDeclaration        `json:"target"`
	Provider   ContentDeclaration        `json:"provider"`
	Action     string                    `json:"action"`
	Operation  string                    `json:"operation"`
	Document   ContentEditorDocument     `json:"document"`
	Serialized ContentSerializedDocument `json:"serialized"`
	Render     ContentRenderSegments     `json:"render"`
	ResourceID string                    `json:"resourceId"`
	Locale     string                    `json:"locale"`
	Scope      string                    `json:"scope"`
}

type ContentRegistry struct {
	byID  map[string]ContentDefinition
	order []ContentDefinition
}

func NewContentRegistry(definitions ...ContentDefinition) (*ContentRegistry, error) {
	registry := &ContentRegistry{byID: make(map[string]ContentDefinition, len(definitions))}
	for _, input := range definitions {
		definition, err := normalizeContentDefinition(input)
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.byID[definition.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate declaration %q", ErrInvalidContentDefinition, definition.ID)
		}
		registry.byID[definition.ID] = definition
		registry.order = append(registry.order, definition)
	}
	sort.Slice(registry.order, func(i, j int) bool { return registry.order[i].ID < registry.order[j].ID })
	return registry, nil
}

func (r *ContentRegistry) Definitions() []ContentDefinition {
	if r == nil {
		return nil
	}
	result := append([]ContentDefinition(nil), r.order...)
	for index := range result {
		result[index].Execute = nil
	}
	return result
}

func (r *ContentRegistry) ProviderCall(
	ctx context.Context,
	request *pluginwire.ProviderCallRequest,
) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: responseContext(providerRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, input, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	result, err := definition.Execute(handlerCtx, &ContentCall{
		Context: cloneRequestContext(request.GetContext()), Target: input.Target, Provider: input.Provider,
		Action: input.Action, Operation: input.Operation, Document: input.Document,
		Serialized: input.Serialized, Render: input.Render, ResourceID: input.ResourceID,
		Locale: input.Locale, Scope: input.Scope,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "content.handler_failed", "Plugin content handler failed.")
		return response, nil
	}
	if err := validateContentResult(input.Operation, result); err != nil {
		response.Error = familyErrorDetail(err, "content.output_invalid", "Plugin content output is invalid.")
		return response, nil
	}
	values, err := strictContentMap(result)
	if err != nil {
		response.Error = familyErrorDetail(err, "content.output_invalid", "Plugin content output is invalid.")
		return response, nil
	}
	response.Output, err = NewTypedDocument(contentCallResponseSchema, values)
	if err != nil {
		response.Error = familyErrorDetail(err, "content.output_invalid", "Plugin content output is invalid.")
	}
	return response, nil
}

func (r *ContentRegistry) resolve(
	request *pluginwire.ProviderCallRequest,
) (ContentDefinition, contentCallRequest, *protocolwire.ErrorDetail) {
	invalid := func(reason, message string) (ContentDefinition, contentCallRequest, *protocolwire.ErrorDetail) {
		return ContentDefinition{}, contentCallRequest{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason, Message: message,
		}
	}
	if r == nil || request == nil {
		return invalid("content.request_required", "A content call request is required.")
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "content"); detail != nil {
		return ContentDefinition{}, contentCallRequest{}, detail
	}
	if request.GetContext().GetActor() != nil || len(request.GetContext().GetGrantedAuthority()) != 0 ||
		request.GetContext().GetIdempotencyKey() != "" || len(request.GetContext().GetHostCommandDelegations()) != 0 ||
		request.GetContext().GetHostSessionDelegation() != nil ||
		!preflightContentHostQueryDelegations(request.GetContext()) {
		return invalid("content.context_authority_forbidden", "Content calls cannot carry actor, session, authority, or delegation material.")
	}
	if request.GetSlotId() != ContentRuntimeProviderSlot || !validContentOperation(request.GetOperation()) {
		return invalid("content.transport_mismatch", "Content handlers require the reserved sforum.content transport.")
	}
	definition, found := r.byID[strings.TrimSpace(request.GetDeclarationId())]
	if !found {
		return ContentDefinition{}, contentCallRequest{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "content.declaration_not_found",
			Message: "The requested content declaration is not registered.",
		}
	}
	if request.GetContractVersion() != definition.ContractVersion {
		return invalid("content.contract_mismatch", "Content declaration contract version mismatch.")
	}
	if err := validateBoundDocument(request.GetInput(), contentCallRequestSchema, "content", "input"); err != nil {
		return ContentDefinition{}, contentCallRequest{}, familyErrorDetail(err, "content.schema_mismatch", "Content input schema mismatch.")
	}
	input := contentCallRequest{}
	if err := strictContentDecode(TypedDocumentValues(request.GetInput()), &input); err != nil {
		return invalid("content.input_invalid", "Content input has unknown or invalid fields.")
	}
	if input.Operation != request.GetOperation() || input.Provider != contentDeclarationFor(definition) ||
		input.Provider.ID != request.GetDeclarationId() || !validContentDeclaration(input.Target) ||
		!validContentAction(input.Action) || strings.TrimSpace(input.ResourceID) != input.ResourceID ||
		strings.TrimSpace(input.Locale) != input.Locale || strings.TrimSpace(input.Scope) != input.Scope {
		return invalid("content.declaration_drift", "Content input does not match the frozen declaration.")
	}
	if !validContentHostQueryDelegations(request.GetContext(), protectedShortcodeContentCall(definition, input)) {
		return invalid("content.context_authority_forbidden", "Content calls cannot carry actor, session, authority, or delegation material.")
	}
	return definition, input, nil
}

func protectedShortcodeContentCall(definition ContentDefinition, input contentCallRequest) bool {
	if input.Operation != ContentOperationRenderer || input.Scope != "protected" {
		return false
	}
	switch definition.ID {
	case shortcodeLoginContentID, shortcodeReplyContentID, shortcodeOnlyAuthorContentID:
		return true
	default:
		return false
	}
}

func normalizeContentDefinition(input ContentDefinition) (ContentDefinition, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Handler = strings.TrimSpace(input.Handler)
	input.Schema = strings.TrimSpace(input.Schema)
	input.Renderer = strings.TrimSpace(input.Renderer)
	input.Migration = strings.TrimSpace(input.Migration)
	if !contentIDPattern.MatchString(input.ID) || !contentContractPattern.MatchString(input.ContractVersion) ||
		!validContentKind(input.Kind) || !validContentHandler(input.Handler) ||
		!contentContractPattern.MatchString(input.Schema) || input.Execute == nil {
		return ContentDefinition{}, ErrInvalidContentDefinition
	}
	return input, nil
}

func contentDeclarationFor(input ContentDefinition) ContentDeclaration {
	return ContentDeclaration{
		ID: input.ID, ContractVersion: input.ContractVersion, Kind: input.Kind,
		Handler: input.Handler, Schema: input.Schema, Renderer: input.Renderer, Migration: input.Migration,
	}
}

func validContentDeclaration(input ContentDeclaration) bool {
	return contentIDPattern.MatchString(input.ID) && contentContractPattern.MatchString(input.ContractVersion) &&
		validContentKind(input.Kind) && validContentHandler(input.Handler) && contentContractPattern.MatchString(input.Schema)
}

func validContentKind(value string) bool {
	switch value {
	case ContentKindBlock, ContentKindShortcode, ContentKindEmbed, ContentKindNode,
		ContentKindMark, ContentKindRenderFilter, ContentKindSanitizer:
		return true
	default:
		return false
	}
}

func validContentHandler(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validContentOperation(value string) bool {
	switch value {
	case ContentOperationEditor, ContentOperationValidator, ContentOperationSerializer,
		ContentOperationRenderer, ContentOperationFilter:
		return true
	default:
		return false
	}
}

func validContentAction(value string) bool {
	switch value {
	case ContentActionAdd, ContentActionBefore, ContentActionAfter,
		ContentActionWrap, ContentActionReplace, ContentActionFilter:
		return true
	default:
		return false
	}
}

func validateContentResult(operation string, result ContentResult) error {
	switch operation {
	case ContentOperationEditor:
		if result.Document == nil || result.Serialized != nil || result.Render != nil {
			return ErrInvalidContentDefinition
		}
	case ContentOperationValidator:
		if result.Document != nil || result.Serialized != nil || result.Render != nil {
			return ErrInvalidContentDefinition
		}
	case ContentOperationSerializer:
		if result.Document != nil || result.Serialized == nil || result.Render != nil {
			return ErrInvalidContentDefinition
		}
	case ContentOperationRenderer, ContentOperationFilter:
		if result.Document != nil || result.Serialized != nil || result.Render == nil {
			return ErrInvalidContentDefinition
		}
	default:
		return ErrInvalidContentDefinition
	}
	return nil
}

func strictContentMap(input any) (map[string]any, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func strictContentDecode(values map[string]any, target any) error {
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func hasExactContentRuntimeFeature(features []*protocolwire.ProtocolFeature) bool {
	for _, feature := range features {
		if feature.GetName() == ContentRuntimeFeatureName && feature.GetVersion() == ContentRuntimeFeatureVersion {
			return true
		}
	}
	return false
}
