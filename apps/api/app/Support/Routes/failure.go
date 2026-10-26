package routes

import (
	"context"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type RouteFailureCode string

const (
	RouteFailureGuardDenied            RouteFailureCode = "guard_denied"
	RouteFailureRequestSchemaRejected  RouteFailureCode = "request_schema_rejected"
	RouteFailureTransportFailed        RouteFailureCode = "transport_failed"
	RouteFailureResponseSchemaRejected RouteFailureCode = "response_schema_rejected"
)

// RouteCommittedAfterFailure contains only stable execution identity. Request
// headers, query, body, raw errors, and response payloads must never enter it.
type RouteCommittedAfterFailure struct {
	Revision                 uint64
	StepIndex                int
	Phase                    RouteExecutionPhase
	InvocationStage          InvocationStage `json:"invocationStage"`
	Action                   string
	RouteID                  string
	ContractVersion          string
	Method                   string
	PathSignature            string
	FailureCode              RouteFailureCode
	RuntimeExecutionObserved bool
	ActorID                  int64
	ResponseStatus           int
	CommitState              RouteExecutionCommitState
	Artifact                 PluginArtifact
}

// ValidInvocationStage is shared by internal evidence producers and external
// adapters. Missing stages are invalid; callers must preserve the exact point
// in the request/handler/response execution cycle.
func ValidInvocationStage(stage InvocationStage) bool {
	switch stage {
	case InvocationStageRequest, InvocationStageHandler, InvocationStageResponse:
		return true
	default:
		return false
	}
}

// ValidInvocationStageForStep rejects evidence whose phase, action and stage
// could not occur in the frozen two-way route execution model.
func ValidInvocationStageForStep(phase RouteExecutionPhase, action string, stage InvocationStage) bool {
	switch phase {
	case RoutePhaseGlobal:
		return action == extensionmanifest.RouteActionGlobalMiddleware && stage == InvocationStageRequest
	case RoutePhaseBefore:
		return action == extensionmanifest.RouteActionBefore && stage == InvocationStageRequest
	case RoutePhaseFilter:
		return action == extensionmanifest.RouteActionFilter &&
			(stage == InvocationStageRequest || stage == InvocationStageResponse)
	case RoutePhaseWrap:
		return action == extensionmanifest.RouteActionWrap &&
			(stage == InvocationStageRequest || stage == InvocationStageResponse)
	case RoutePhaseHandler:
		switch action {
		case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionAlias,
			extensionmanifest.RouteActionRedirect, extensionmanifest.RouteActionRewrite,
			extensionmanifest.RouteActionReplace:
			return stage == InvocationStageHandler
		}
	case RoutePhaseAfter:
		return action == extensionmanifest.RouteActionAfter && stage == InvocationStageResponse
	}
	return false
}

// RouteFailureSink must return promptly. Production implementations close exact
// admission synchronously and enqueue only bounded audit work.
type RouteFailureSink interface {
	RecordCommittedAfterFailure(context.Context, RouteCommittedAfterFailure)
}
