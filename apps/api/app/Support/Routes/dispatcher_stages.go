package routes

import (
	"fmt"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type routeInvocationExecution struct {
	index int
	stage InvocationStage
}

// bufferedRouteInvocationSequence expands one immutable plan into the two-way
// call order. Request stages enter high-to-low; response stages unwind each
// composable family low-to-high, which keeps the highest-priority wrap outermost.
func bufferedRouteInvocationSequence(plan RouteExecutionPlan) ([]routeInvocationExecution, error) {
	if !plan.Valid() {
		return nil, ErrInvalidExecutionPlan
	}
	chain := plan.chain
	sequence := make([]routeInvocationExecution, 0, len(chain)*2)
	for index := 0; index < plan.terminalIndex; index++ {
		step := chain[index]
		phase, ok := executionPhaseForContribution(step.Action)
		if step.Provider.Kind != ProviderPlugin || !ok || phase != step.Phase || !requestStageAction(step.Action) {
			return nil, fmt.Errorf("%w: invalid request-stage contribution", ErrInvalidExecutionPlan)
		}
		if !routeMutableBodySchemasValid(step) {
			return nil, fmt.Errorf("%w: mutable request or response body has no matching schema", ErrInvalidExecutionPlan)
		}
		sequence = append(sequence, routeInvocationExecution{index: index, stage: InvocationStageRequest})
	}
	terminal := chain[plan.terminalIndex]
	if !validBufferedTerminalStep(terminal) {
		return nil, fmt.Errorf("%w: invalid buffered handler", ErrInvalidExecutionPlan)
	}
	if !routeMutableBodySchemasValid(terminal) {
		return nil, fmt.Errorf("%w: mutable request or response body has no matching schema", ErrInvalidExecutionPlan)
	}
	sequence = append(sequence, routeInvocationExecution{index: plan.terminalIndex, stage: InvocationStageHandler})
	for _, phase := range []RouteExecutionPhase{RoutePhaseWrap, RoutePhaseFilter, RoutePhaseAfter} {
		for index := len(chain) - 1; index >= 0; index-- {
			step := chain[index]
			if step.Phase != phase {
				continue
			}
			resolvedPhase, ok := executionPhaseForContribution(step.Action)
			if step.Provider.Kind != ProviderPlugin || !ok || resolvedPhase != step.Phase ||
				!responseStageAction(step.Action) || !routeMutableBodySchemasValid(step) {
				return nil, fmt.Errorf("%w: invalid response-stage contribution", ErrInvalidExecutionPlan)
			}
			sequence = append(sequence, routeInvocationExecution{index: index, stage: InvocationStageResponse})
		}
	}
	return sequence, nil
}

func validBufferedTerminalStep(step RouteExecutionStep) bool {
	if step.Phase != RoutePhaseHandler || !ValidInvocationStageForStep(step.Phase, step.Action, InvocationStageHandler) {
		return false
	}
	switch step.Provider.Kind {
	case ProviderCore:
		return step.Action == extensionmanifest.RouteActionAdd
	case ProviderPlugin:
		switch step.Action {
		case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionAlias,
			extensionmanifest.RouteActionRedirect, extensionmanifest.RouteActionRewrite,
			extensionmanifest.RouteActionReplace:
			return true
		}
	}
	return false
}

func requestStageAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionGlobalMiddleware, extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap:
		return true
	default:
		return false
	}
}

func responseStageAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap, extensionmanifest.RouteActionAfter:
		return true
	default:
		return false
	}
}

func pairedResponseStageAction(action string) bool {
	return action == extensionmanifest.RouteActionFilter || action == extensionmanifest.RouteActionWrap
}

func routeChainHasResponseModifiers(chain []RouteExecutionStep) bool {
	for _, step := range chain {
		if step.Provider.Kind == ProviderPlugin && responseStageAction(step.Action) {
			return true
		}
	}
	return false
}

func routeChainHasMutableRequestFields(chain []RouteExecutionStep) bool {
	for _, step := range chain {
		if step.Provider.Kind == ProviderPlugin && requestStageAction(step.Action) && len(step.MutableRequestFields) > 0 {
			return true
		}
	}
	return false
}

func routeMutableBodySchemasValid(step RouteExecutionStep) bool {
	return (!routeMutableFieldsTargetBody(step.MutableRequestFields) || strings.TrimSpace(step.RequestSchema) != "") &&
		(!routeMutableFieldsTargetBody(step.MutableResponseFields) || strings.TrimSpace(step.ResponseSchema) != "")
}

func routeMutableFieldsTargetBody(fields []string) bool {
	for _, field := range fields {
		if field == "/body" || strings.HasPrefix(field, "/body/") {
			return true
		}
	}
	return false
}

func validateRouteInvocationResult(stage InvocationStage, result RouteInvocationResult) error {
	switch stage {
	case InvocationStageRequest:
		if result.Request != nil || result.Response != nil || len(result.ResponsePatch) != 0 {
			return fmt.Errorf("%w: request stage returned an invalid result shape", ErrDispatchSchema)
		}
	case InvocationStageHandler:
		if result.Request != nil || result.Response == nil || len(result.RequestPatch) != 0 || len(result.ResponsePatch) != 0 {
			return fmt.Errorf("%w: handler stage returned an invalid result shape", ErrDispatchSchema)
		}
	case InvocationStageResponse:
		if result.Request != nil || result.Response != nil || len(result.RequestPatch) != 0 {
			return fmt.Errorf("%w: response stage returned an invalid result shape", ErrDispatchSchema)
		}
	default:
		return fmt.Errorf("%w: invalid invocation stage", ErrDispatchSchema)
	}
	return nil
}

func rawMutationAuthority(authority routeInvocationAuthority) bool {
	return authority.issued && authority.authorization.issued &&
		authority.authorization.mode == RequestAuthorityRaw &&
		authority.authorization.guardKind == RequestGuardRawRequest
}

func routeRequestPatchMutatesParams(operations []RoutePatchOperation) bool {
	for _, operation := range operations {
		if operation.Path == "/params" || strings.HasPrefix(operation.Path, "/params/") {
			return true
		}
	}
	return false
}

func planTerminalUsesFrozenPathParams(plan RouteExecutionPlan) bool {
	terminal := plan.Terminal()
	return terminal.Provider.Kind == ProviderCore ||
		terminal.Action == extensionmanifest.RouteActionAlias ||
		terminal.Action == extensionmanifest.RouteActionRedirect ||
		terminal.Action == extensionmanifest.RouteActionRewrite
}
