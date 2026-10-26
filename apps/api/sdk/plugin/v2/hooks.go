package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var (
	ErrInvalidHookDefinition = errors.New("invalid plugin hook definition")

	hookIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	hookNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	hookSchemaPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

// HookDefinition 对齐可执行 ManifestHook + Host InvokeHook。
// 同一 event name 可有多条声明；分发键是 hook id。
// 被动/目录-only 声明不得注册到本 runtime registry。
// filter patch 按 Host 约定绑定 resultSchemaID+".patch@"+version。
type HookDefinition struct {
	ID              string
	Name            string
	Kind            string
	ContractVersion string
	TargetID        string
	Handler         string
	InputSchema     string
	ResultSchema    string
	Priority        int
	Execution       string
	FailurePolicy   string
	TimeoutMS       int
	MutableFields   []string
	// Execute 是作者侧业务 handler（非 Manifest 字段）。
	Execute HookHandler
}

// HookCall 是校验后的 InvokeHook 业务输入。
type HookCall struct {
	Context         *protocolwire.RequestContext
	ID              string
	Name            string
	Kind            string
	ContractVersion string
	Handler         string
	DeliveryID      string
	Payload         *protocolwire.TypedDocument
	MutableFields   []string
}

// HookResult 是作者 handler 的业务输出。
type HookResult struct {
	Accepted bool
	Result   *protocolwire.TypedDocument
	Patch    *protocolwire.TypedDocument
}

type HookHandler func(context.Context, *HookCall) (*HookResult, error)

// HookError 是 Hook handler 的稳定应用错误别名。
type HookError = FamilyError

type registeredHook struct {
	definition HookDefinition
}

// HookRegistry 在握手后不可变；byID 按声明身份分发。
type HookRegistry struct {
	byID  map[string]registeredHook
	order []HookDefinition
}

func NewHookRegistry(definitions ...HookDefinition) (*HookRegistry, error) {
	registry := &HookRegistry{byID: make(map[string]registeredHook, len(definitions))}
	for _, definition := range definitions {
		prepared, err := prepareHookDefinition(definition)
		if err != nil {
			return nil, err
		}
		if _, exists := registry.byID[prepared.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate hook id %q", ErrInvalidHookDefinition, prepared.ID)
		}
		registry.byID[prepared.ID] = registeredHook{definition: prepared}
		registry.order = append(registry.order, prepared)
	}
	sort.Slice(registry.order, func(i, j int) bool {
		if registry.order[i].Name != registry.order[j].Name {
			return registry.order[i].Name < registry.order[j].Name
		}
		return registry.order[i].ID < registry.order[j].ID
	})
	return registry, nil
}

func (r *HookRegistry) Definitions() []HookDefinition {
	if r == nil {
		return nil
	}
	out := make([]HookDefinition, len(r.order))
	for i, item := range r.order {
		copyDef := item
		copyDef.MutableFields = append([]string(nil), item.MutableFields...)
		copyDef.Execute = nil
		out[i] = copyDef
	}
	return out
}

func (r *HookRegistry) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	response := &pluginwire.HookResponse{
		Context: responseContext(hookRequestContext(request), time.Now().UTC()), Accepted: true,
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, detail := r.resolve(request)
	if detail != nil {
		response.Accepted = false
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	result, err := definition.Execute(handlerCtx, &HookCall{
		Context: cloneRequestContext(request.GetContext()),
		ID:      definition.ID, Name: definition.Name, Kind: definition.Kind,
		ContractVersion: definition.ContractVersion, Handler: definition.Handler,
		DeliveryID: request.GetDeliveryId(), Payload: cloneTypedDocument(request.GetPayload()),
		MutableFields: append([]string(nil), definition.MutableFields...),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Accepted = false
		response.Error = familyErrorDetail(err, "hook.handler_failed", "Plugin hook handler failed.")
		return response, nil
	}
	if result == nil {
		response.Accepted = false
		response.Error = familyErrorDetail(newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"hook.handler_nil_result", "Hook handler returned a nil result."),
			"hook.handler_failed", "Plugin hook handler failed.")
		return response, nil
	}
	response.Accepted = result.Accepted
	if result.Result != nil {
		if schemaErr := validateBoundDocument(result.Result, definition.ResultSchema, "hook", "result"); schemaErr != nil {
			response.Accepted = false
			response.Error = familyErrorDetail(schemaErr, "hook.handler_failed", "Plugin hook handler failed.")
			return response, nil
		}
		response.Result = cloneTypedDocument(result.Result)
	} else if definition.Kind != "observe" && definition.ResultSchema != "" && result.Accepted {
		response.Accepted = false
		response.Error = familyErrorDetail(newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"hook.result_required", "Accepted non-observe hooks must return a typed result."),
			"hook.handler_failed", "Plugin hook handler failed.")
		return response, nil
	}
	if result.Patch != nil {
		if definition.Kind != "filter" {
			response.Accepted = false
			response.Result = nil
			response.Error = familyErrorDetail(newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"hook.patch_not_allowed", "Only filter hooks may return a patch."),
				"hook.handler_failed", "Plugin hook handler failed.")
			return response, nil
		}
		patchSchema, ok := filterPatchSchemaRef(definition.ResultSchema)
		if !ok {
			response.Accepted = false
			response.Result = nil
			response.Error = familyErrorDetail(newFamilyError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
				"hook.patch_schema_invalid", "Filter result schema cannot derive a patch schema."),
				"hook.handler_failed", "Plugin hook handler failed.")
			return response, nil
		}
		if schemaErr := validateBoundDocument(result.Patch, patchSchema, "hook", "patch"); schemaErr != nil {
			response.Accepted = false
			response.Result = nil
			response.Error = familyErrorDetail(schemaErr, "hook.handler_failed", "Plugin hook handler failed.")
			return response, nil
		}
		response.Patch = cloneTypedDocument(result.Patch)
	}
	return response, nil
}

func prepareHookDefinition(definition HookDefinition) (HookDefinition, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Kind = strings.TrimSpace(definition.Kind)
	definition.ContractVersion = strings.TrimSpace(definition.ContractVersion)
	definition.TargetID = strings.TrimSpace(definition.TargetID)
	definition.Handler = strings.TrimSpace(definition.Handler)
	definition.InputSchema = strings.TrimSpace(definition.InputSchema)
	definition.ResultSchema = strings.TrimSpace(definition.ResultSchema)
	definition.Execution = strings.ToLower(strings.TrimSpace(definition.Execution))
	definition.FailurePolicy = strings.ToLower(strings.TrimSpace(definition.FailurePolicy))
	if !hookIDPattern.MatchString(definition.ID) {
		return HookDefinition{}, fmt.Errorf("%w: invalid hook id %q", ErrInvalidHookDefinition, definition.ID)
	}
	if !hookNamePattern.MatchString(definition.Name) {
		return HookDefinition{}, fmt.Errorf("%w: invalid hook name %q", ErrInvalidHookDefinition, definition.Name)
	}
	switch definition.Kind {
	case "action", "filter", "observe":
	default:
		return HookDefinition{}, fmt.Errorf("%w: invalid hook kind %q", ErrInvalidHookDefinition, definition.Kind)
	}
	if !validContractVersion(definition.ContractVersion) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q contract version must be id@positiveVersion", ErrInvalidHookDefinition, definition.ID)
	}
	// 可执行 registry：必须具备 Manifest handler。
	if !validManifestHandler(definition.Handler) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q requires executable handler", ErrInvalidHookDefinition, definition.ID)
	}
	if definition.TargetID != "" && !hookIDPattern.MatchString(definition.TargetID) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q has invalid target id %q", ErrInvalidHookDefinition, definition.ID, definition.TargetID)
	}
	if !hookSchemaPattern.MatchString(definition.InputSchema) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q requires versioned input schema", ErrInvalidHookDefinition, definition.ID)
	}
	if definition.Kind != "observe" && !hookSchemaPattern.MatchString(definition.ResultSchema) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q requires versioned result schema", ErrInvalidHookDefinition, definition.ID)
	}
	if definition.Kind == "observe" && definition.ResultSchema != "" && !hookSchemaPattern.MatchString(definition.ResultSchema) {
		return HookDefinition{}, fmt.Errorf("%w: hook %q has invalid result schema", ErrInvalidHookDefinition, definition.ID)
	}
	// 与 Manifest V3 normalize 对齐的默认值。
	if definition.Execution == "" {
		if definition.Kind == "observe" {
			definition.Execution = "async"
		} else {
			definition.Execution = "sync"
		}
	}
	if definition.Execution != "sync" && definition.Execution != "async" {
		return HookDefinition{}, fmt.Errorf("%w: hook %q has invalid execution %q", ErrInvalidHookDefinition, definition.ID, definition.Execution)
	}
	if definition.FailurePolicy == "" {
		if definition.Execution == "async" {
			definition.FailurePolicy = "fail_open"
		} else {
			definition.FailurePolicy = "fail_closed"
		}
	}
	if definition.FailurePolicy != "fail_closed" && definition.FailurePolicy != "fail_open" {
		return HookDefinition{}, fmt.Errorf("%w: hook %q has invalid failure policy %q", ErrInvalidHookDefinition, definition.ID, definition.FailurePolicy)
	}
	if definition.Execution == "async" && definition.Kind == "filter" {
		return HookDefinition{}, fmt.Errorf("%w: hook %q cannot be async filter", ErrInvalidHookDefinition, definition.ID)
	}
	if definition.Execution == "async" && definition.FailurePolicy != "fail_open" {
		return HookDefinition{}, fmt.Errorf("%w: async hook %q requires fail_open", ErrInvalidHookDefinition, definition.ID)
	}
	if definition.TimeoutMS == 0 {
		if definition.Execution == "async" {
			definition.TimeoutMS = 5000
		} else {
			definition.TimeoutMS = 2000
		}
	}
	if definition.TimeoutMS <= 0 || definition.TimeoutMS > extensionmanifest.HookMaximumTimeoutMS {
		return HookDefinition{}, fmt.Errorf("%w: hook %q timeout must be 1..%d",
			ErrInvalidHookDefinition, definition.ID, extensionmanifest.HookMaximumTimeoutMS)
	}
	if definition.Kind != "filter" && len(definition.MutableFields) > 0 {
		return HookDefinition{}, fmt.Errorf("%w: only filter hooks may declare mutable fields", ErrInvalidHookDefinition)
	}
	seen := map[string]struct{}{}
	fields := make([]string, 0, len(definition.MutableFields))
	for _, field := range definition.MutableFields {
		field = strings.TrimSpace(field)
		if field == "" {
			return HookDefinition{}, fmt.Errorf("%w: hook %q has empty mutable field", ErrInvalidHookDefinition, definition.ID)
		}
		if _, exists := seen[field]; exists {
			return HookDefinition{}, fmt.Errorf("%w: hook %q duplicates mutable field %q", ErrInvalidHookDefinition, definition.ID, field)
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}
	definition.MutableFields = fields
	if definition.Execute == nil {
		return HookDefinition{}, fmt.Errorf("%w: hook %q has no execute handler", ErrInvalidHookDefinition, definition.ID)
	}
	return definition, nil
}

func (r *HookRegistry) resolve(request *pluginwire.HookRequest) (HookDefinition, *protocolwire.ErrorDetail) {
	if r == nil {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason: "hook.registry_unavailable", Message: "Plugin hook registry is unavailable.",
		}
	}
	if request == nil {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "hook.request_required", Message: "A hook request is required.",
		}
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "hook"); detail != nil {
		return HookDefinition{}, detail
	}
	hookID := strings.TrimSpace(request.GetHookId())
	registered, ok := r.byID[hookID]
	if !ok {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			Reason: "hook.not_found", Message: "The requested hook is not registered.",
		}
	}
	definition := registered.definition
	if request.GetHookName() != definition.Name {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "hook.name_mismatch", Message: "Hook name does not match the registered declaration.",
		}
	}
	if request.GetHookKind() != definition.Kind {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "hook.kind_mismatch", Message: "Hook kind does not match the registered declaration.",
		}
	}
	if request.GetContractVersion() != definition.ContractVersion {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "hook.contract_mismatch", Message: "Hook contract version does not match the registered declaration.",
		}
	}
	// Host 生产路径重放冻结的 mutableFields；拒绝声明漂移与未声明字段。
	if !stringSlicesEqual(request.GetMutableFields(), definition.MutableFields) {
		return HookDefinition{}, &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "hook.mutable_fields_mismatch",
			Message: "Hook mutable fields must exactly match the frozen declaration (Host delivery contract).",
		}
	}
	if err := validateBoundDocument(request.GetPayload(), definition.InputSchema, "hook", "payload"); err != nil {
		return HookDefinition{}, familyErrorDetail(err, "hook.schema_mismatch", "Hook payload schema mismatch.")
	}
	return definition, nil
}

func hookRequestContext(request *pluginwire.HookRequest) *protocolwire.RequestContext {
	if request == nil {
		return nil
	}
	return request.GetContext()
}
