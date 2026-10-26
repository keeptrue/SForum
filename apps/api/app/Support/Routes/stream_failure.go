package routes

import (
	"context"
	"errors"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type routeStreamDispositionError struct {
	cause    error
	class    RouteStreamFailureClass
	incident bool
}

func (e *routeStreamDispositionError) Error() string { return "routes: classified stream failure" }
func (e *routeStreamDispositionError) Unwrap() error { return e.cause }

// WithRouteStreamIncident carries a Host-classified runtime failure across an
// internal adapter boundary without putting raw error text into durable evidence.
func WithRouteStreamIncident(err error, class RouteStreamFailureClass) error {
	if err == nil {
		err = ErrDispatchTransport
	}
	if !ValidRouteStreamFailureClass(class) {
		class = RouteStreamFailureRuntimeTransport
	}
	return &routeStreamDispositionError{cause: err, class: class, incident: true}
}

// WithRouteStreamAbort marks caller/Host/lifecycle termination as non-incident.
func WithRouteStreamAbort(err error) error {
	if err == nil {
		err = context.Canceled
	}
	return &routeStreamDispositionError{cause: err}
}

func routeStreamFailureDisposition(err error) (RouteStreamFailureClass, bool, bool) {
	var classified *routeStreamDispositionError
	if !errors.As(err, &classified) {
		return "", false, false
	}
	if !classified.incident {
		return "", false, true
	}
	if !ValidRouteStreamFailureClass(classified.class) {
		return RouteStreamFailureRuntimeTransport, true, true
	}
	return classified.class, true, true
}

// InspectRouteStreamFailureDisposition lets Host transport adapters preserve a
// previously assigned incident/abort decision before applying protocol-specific
// sentinels such as EOF. Only the private Host wrapper is recognized.
func InspectRouteStreamFailureDisposition(err error) (RouteStreamFailureClass, bool, bool) {
	return routeStreamFailureDisposition(err)
}

// RouteStreamFailureClass is Host-observed provenance only. Raw transport
// errors, request metadata, and payload bytes must never enter durable evidence.
type RouteStreamFailureClass string

const (
	RouteStreamFailureRuntimeTransport RouteStreamFailureClass = "runtime_transport"
	RouteStreamFailureHostBudget       RouteStreamFailureClass = "host_budget"
	RouteStreamFailureInvalidPreflight RouteStreamFailureClass = "invalid_preflight"
	RouteStreamFailureMissingTerminal  RouteStreamFailureClass = "missing_terminal"
)

// RouteStreamFailure pins one stream incident to the exact route snapshot and
// executable artifact. It deliberately contains no raw error or request data.
type RouteStreamFailure struct {
	Revision                 uint64
	StepIndex                int
	Phase                    RouteExecutionPhase
	InvocationStage          InvocationStage `json:"invocationStage"`
	Action                   string
	Mode                     string
	RouteID                  string
	ContractVersion          string
	Method                   string
	PathSignature            string
	FailureCode              RouteFailureCode
	CauseClass               RouteStreamFailureClass
	RuntimeExecutionObserved bool
	ActorID                  int64
	ResponseStatus           int
	CommitState              RouteExecutionCommitState
	Artifact                 PluginArtifact
}

func ValidRouteStreamFailureClass(class RouteStreamFailureClass) bool {
	switch class {
	case RouteStreamFailureRuntimeTransport, RouteStreamFailureHostBudget,
		RouteStreamFailureInvalidPreflight, RouteStreamFailureMissingTerminal:
		return true
	default:
		return false
	}
}

func ValidRouteStreamFailure(event RouteStreamFailure) bool {
	validMode := event.Mode == extensionmanifest.RouteModeMultipart ||
		event.Mode == extensionmanifest.RouteModeStream || event.Mode == extensionmanifest.RouteModeSSE ||
		event.Mode == extensionmanifest.RouteModeWebSocket
	return event.Revision > 0 && event.StepIndex >= 0 &&
		event.Phase == RoutePhaseHandler && event.InvocationStage == InvocationStageHandler &&
		(event.Action == extensionmanifest.RouteActionAdd || event.Action == extensionmanifest.RouteActionReplace) &&
		validMode && event.FailureCode == RouteFailureTransportFailed &&
		ValidRouteStreamFailureClass(event.CauseClass) && event.RuntimeExecutionObserved &&
		(event.CommitState == RouteCommitSideEffectStarted || event.CommitState == RouteCommitResponseStarted) &&
		event.RouteID != "" && event.ContractVersion != "" && event.Method != "" && event.PathSignature != "" &&
		event.Artifact.ExtensionID != "" && event.Artifact.ExtensionVersion != "" &&
		event.Artifact.PackageDigest != "" && event.Artifact.RuntimeInstanceID != ""
}

// RouteStreamFailureSink must return promptly. Production first persists
// payload-free evidence, then closes exact local admission and resolves the
// local quarantine result.
type RouteStreamFailureSink interface {
	RecordStreamFailure(context.Context, RouteStreamFailure)
}
