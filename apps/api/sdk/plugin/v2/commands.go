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
	ErrInvalidCommandDefinition = errors.New("invalid plugin command definition")

	commandIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	commandSchemaPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

// CommandDefinition 对齐可执行 ManifestCommand + InvokeCommand。
// ContractVersion 必须是 id@positiveVersion。
type CommandDefinition struct {
	ID              string
	ContractVersion string
	Handler         string
	Permission      string
	InputSchema     string
	ResultSchema    string
	Description     string
	RecoverySafe    bool
	TimeoutMS       int
	// Execute 是作者侧业务 handler（非 Manifest 字段）。
	Execute CommandHandler
}

// CommandCall 是校验后的 CLI 调用输入。
type CommandCall struct {
	Context         *protocolwire.RequestContext
	ID              string
	ContractVersion string
	Handler         string
	Input           *protocolwire.TypedDocument
}

type CommandHandler func(context.Context, *CommandCall) (*protocolwire.TypedDocument, error)

type registeredCommand struct {
	definition CommandDefinition
}

// CommandRegistry 分发 Host → plugin 的 InvokeCommand。
type CommandRegistry struct {
	byKey map[string]registeredCommand
	order []CommandDefinition
}

func NewCommandRegistry(definitions ...CommandDefinition) (*CommandRegistry, error) {
	registry := &CommandRegistry{byKey: make(map[string]registeredCommand, len(definitions))}
	for _, definition := range definitions {
		prepared, err := prepareCommandDefinition(definition)
		if err != nil {
			return nil, err
		}
		key := commandRegistryKey(prepared.ID, prepared.ContractVersion)
		if _, exists := registry.byKey[key]; exists {
			return nil, fmt.Errorf("%w: duplicate command %s@%s", ErrInvalidCommandDefinition, prepared.ID, prepared.ContractVersion)
		}
		registry.byKey[key] = registeredCommand{definition: prepared}
		registry.order = append(registry.order, prepared)
	}
	sort.Slice(registry.order, func(i, j int) bool {
		if registry.order[i].ID != registry.order[j].ID {
			return registry.order[i].ID < registry.order[j].ID
		}
		return registry.order[i].ContractVersion < registry.order[j].ContractVersion
	})
	return registry, nil
}

func (r *CommandRegistry) Definitions() []CommandDefinition {
	if r == nil {
		return nil
	}
	out := make([]CommandDefinition, len(r.order))
	for i, item := range r.order {
		copyDef := item
		copyDef.Execute = nil
		out[i] = copyDef
	}
	return out
}

func (r *CommandRegistry) InvokeCommand(ctx context.Context, request *pluginwire.CommandInvocationRequest) (*pluginwire.CommandInvocationResponse, error) {
	response := &pluginwire.CommandInvocationResponse{Context: responseContext(commandRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	result, err := definition.Execute(handlerCtx, &CommandCall{
		Context: cloneRequestContext(request.GetContext()),
		ID:      definition.ID, ContractVersion: definition.ContractVersion, Handler: definition.Handler,
		Input: cloneTypedDocument(request.GetInput()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "command.handler_failed", "Plugin command handler failed.")
		return response, nil
	}
	if schemaErr := validateBoundDocument(result, definition.ResultSchema, "command", "result"); schemaErr != nil {
		response.Error = familyErrorDetail(schemaErr, "command.handler_failed", "Plugin command handler failed.")
		return response, nil
	}
	response.Result = cloneTypedDocument(result)
	return response, nil
}

func prepareCommandDefinition(definition CommandDefinition) (CommandDefinition, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.ContractVersion = strings.TrimSpace(definition.ContractVersion)
	definition.Handler = strings.TrimSpace(definition.Handler)
	definition.Permission = strings.TrimSpace(definition.Permission)
	definition.InputSchema = strings.TrimSpace(definition.InputSchema)
	definition.ResultSchema = strings.TrimSpace(definition.ResultSchema)
	definition.Description = strings.TrimSpace(definition.Description)
	if !commandIDPattern.MatchString(definition.ID) {
		return CommandDefinition{}, fmt.Errorf("%w: invalid command id %q", ErrInvalidCommandDefinition, definition.ID)
	}
	if !validContractVersion(definition.ContractVersion) {
		return CommandDefinition{}, fmt.Errorf("%w: command %q contract version must be id@positiveVersion", ErrInvalidCommandDefinition, definition.ID)
	}
	if !validManifestHandler(definition.Handler) {
		return CommandDefinition{}, fmt.Errorf("%w: command %q requires executable handler", ErrInvalidCommandDefinition, definition.ID)
	}
	if !commandSchemaPattern.MatchString(definition.InputSchema) || !commandSchemaPattern.MatchString(definition.ResultSchema) {
		return CommandDefinition{}, fmt.Errorf("%w: command %q requires versioned input/result schemas", ErrInvalidCommandDefinition, definition.ID)
	}
	// Manifest V3 platform normalize：缺省 timeout 为上限。
	if definition.TimeoutMS == 0 {
		definition.TimeoutMS = extensionmanifest.PluginCommandMaximumTimeoutMS
	}
	if definition.TimeoutMS <= 0 || definition.TimeoutMS > extensionmanifest.PluginCommandMaximumTimeoutMS {
		return CommandDefinition{}, fmt.Errorf("%w: command %q timeout must be 1..%d",
			ErrInvalidCommandDefinition, definition.ID, extensionmanifest.PluginCommandMaximumTimeoutMS)
	}
	if definition.Execute == nil {
		return CommandDefinition{}, fmt.Errorf("%w: command %q has no execute handler", ErrInvalidCommandDefinition, definition.ID)
	}
	return definition, nil
}

func (r *CommandRegistry) resolve(request *pluginwire.CommandInvocationRequest) (CommandDefinition, *protocolwire.ErrorDetail) {
	if r == nil {
		return CommandDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason: "command.registry_unavailable", Message: "Plugin command registry is unavailable.",
		}
	}
	if request == nil {
		return CommandDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "command.request_required", Message: "A command invocation request is required.",
		}
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "command"); detail != nil {
		return CommandDefinition{}, detail
	}
	commandID := strings.TrimSpace(request.GetCommandId())
	contract := strings.TrimSpace(request.GetContractVersion())
	registered, ok := r.byKey[commandRegistryKey(commandID, contract)]
	if !ok {
		return CommandDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			Reason: "command.not_found", Message: "The requested plugin command is not registered.",
		}
	}
	definition := registered.definition
	if handler := strings.TrimSpace(request.GetHandler()); handler != "" && handler != definition.Handler {
		return CommandDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "command.handler_mismatch", Message: "Command handler does not match the registered declaration.",
		}
	}
	if err := validateBoundDocument(request.GetInput(), definition.InputSchema, "command", "input"); err != nil {
		return CommandDefinition{}, familyErrorDetail(err, "command.schema_mismatch", "Command input schema mismatch.")
	}
	return definition, nil
}

func commandRegistryKey(id, contract string) string { return id + "\x00" + contract }

func commandRequestContext(request *pluginwire.CommandInvocationRequest) *protocolwire.RequestContext {
	if request == nil {
		return nil
	}
	return request.GetContext()
}
