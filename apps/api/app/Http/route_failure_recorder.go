package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	defaultRouteFailureQueueSize    = 128
	defaultRouteFailureAuditTimeout = 2 * time.Second
)

type ExactRuntimeIncidentQuarantiner interface {
	QuarantineRuntimeInstance(
		extensionsruntime.RuntimeInstanceArtifactIdentity,
		error,
	) (extensionsruntime.RuntimeAdmissionSnapshot, error)
}

type RouteFailureRecorder struct {
	runtime                     ExactRuntimeIncidentQuarantiner
	incidents                   RouteRuntimeIncidentStore
	auditor                     audit.Writer
	logger                      *slog.Logger
	timeout                     time.Duration
	queue                       chan recordedRouteFailure
	stop                        chan struct{}
	done                        chan struct{}
	enqueueMu                   sync.RWMutex
	incidentWG                  sync.WaitGroup
	stopOnce                    sync.Once
	closed                      bool
	dropped                     atomic.Uint64
	incidentPersistenceFailures atomic.Uint64
}

type recordedRouteFailure struct {
	event      routes.RouteCommittedAfterFailure
	quarantine string
}

func NewRouteFailureRecorder(
	runtime ExactRuntimeIncidentQuarantiner,
	incidents RouteRuntimeIncidentStore,
	auditor audit.Writer,
	logger *slog.Logger,
) (*RouteFailureRecorder, error) {
	return newRouteFailureRecorder(runtime, incidents, auditor, logger, defaultRouteFailureQueueSize, defaultRouteFailureAuditTimeout)
}

func newRouteFailureRecorder(
	runtime ExactRuntimeIncidentQuarantiner,
	incidents RouteRuntimeIncidentStore,
	auditor audit.Writer,
	logger *slog.Logger,
	queueSize int,
	timeout time.Duration,
) (*RouteFailureRecorder, error) {
	if runtime == nil || incidents == nil || auditor == nil || queueSize <= 0 || timeout <= 0 {
		return nil, fmt.Errorf("route failure recorder is not configured")
	}
	if logger == nil {
		logger = slog.Default()
	}
	recorder := &RouteFailureRecorder{
		runtime: runtime, incidents: incidents, auditor: auditor, logger: logger, timeout: timeout,
		queue: make(chan recordedRouteFailure, queueSize), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go recorder.run()
	return recorder, nil
}

func (r *RouteFailureRecorder) RecordCommittedAfterFailure(
	_ context.Context,
	event routes.RouteCommittedAfterFailure,
) {
	if r == nil {
		return
	}
	if event.InvocationStage != routes.InvocationStageResponse ||
		!routes.ValidInvocationStageForStep(event.Phase, event.Action, event.InvocationStage) {
		return
	}
	if routeFailureRequiresQuarantine(event) {
		if !r.beginRuntimeIncident() {
			return
		}
		defer r.incidentWG.Done()
		r.recordRuntimeIncident(routeCommittedFailureIncidentEvidence(event))
		return
	}
	r.enqueueMu.RLock()
	defer r.enqueueMu.RUnlock()
	if r.closed {
		return
	}
	item := recordedRouteFailure{event: event, quarantine: "not_required"}
	if event.FailureCode == routes.RouteFailureTransportFailed {
		item.quarantine = "execution_not_observed"
	}
	select {
	case r.queue <- item:
	default:
		r.dropped.Add(1)
		r.logger.Error("route failure audit queue is full",
			"extension_id", event.Artifact.ExtensionID,
			"runtime_instance_id", event.Artifact.RuntimeInstanceID,
			"failure_code", event.FailureCode,
			"quarantine", item.quarantine,
		)
	}
}

func (r *RouteFailureRecorder) RecordStreamFailure(_ context.Context, event routes.RouteStreamFailure) {
	if r == nil || !routes.ValidRouteStreamFailure(event) {
		return
	}
	if !r.beginRuntimeIncident() {
		return
	}
	defer r.incidentWG.Done()
	r.recordRuntimeIncident(routeStreamFailureIncidentEvidence(event))
}

func (r *RouteFailureRecorder) beginRuntimeIncident() bool {
	r.enqueueMu.Lock()
	defer r.enqueueMu.Unlock()
	if r.closed {
		return false
	}
	r.incidentWG.Add(1)
	return true
}

func (r *RouteFailureRecorder) recordRuntimeIncident(evidence RouteRuntimeIncidentEvidence) {
	deadline := time.Now().Add(r.timeout)
	key, err := NewRouteRuntimeIncidentKey()
	if err == nil {
		evidence.IncidentKey = key
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		_, _, err = r.incidents.CreatePending(ctx, evidence)
		cancel()
	}
	if err != nil {
		r.incidentPersistenceFailures.Add(1)
		r.logger.Error("persist route runtime incident failed",
			"extension_id", evidence.Artifact.ExtensionID,
			"runtime_instance_id", evidence.Artifact.RuntimeInstanceID,
			"failure_code", evidence.FailureCode,
			"cause_class", evidence.CauseClass,
			"error", err,
		)
	}

	result := r.quarantineRuntimeIncident(evidence)
	if evidence.IncidentKey == "" || err != nil {
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	_, resolveErr := r.incidents.Resolve(ctx, evidence.IncidentKey, result)
	cancel()
	if resolveErr != nil {
		r.incidentPersistenceFailures.Add(1)
		r.logger.Error("resolve route runtime incident failed",
			"extension_id", evidence.Artifact.ExtensionID,
			"runtime_instance_id", evidence.Artifact.RuntimeInstanceID,
			"failure_code", evidence.FailureCode,
			"cause_class", evidence.CauseClass,
			"local_quarantine_result", result,
			"error", resolveErr,
		)
	}
}

func (r *RouteFailureRecorder) quarantineRuntimeIncident(evidence RouteRuntimeIncidentEvidence) RouteRuntimeIncidentLocalResult {
	exact := extensionsruntime.RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: extensionsruntime.RuntimeInstanceIdentity{
			ExtensionID: evidence.Artifact.ExtensionID, InstanceID: evidence.Artifact.RuntimeInstanceID,
		},
		ExtensionVersion: evidence.Artifact.ExtensionVersion,
		ArtifactDigest:   evidence.Artifact.PackageDigest,
	}
	cause := fmt.Errorf("%w: %s", extensionsruntime.ErrRuntimeRouteIncident, evidence.CauseClass)
	_, err := r.runtime.QuarantineRuntimeInstance(exact, cause)
	switch {
	case err == nil:
		return RouteRuntimeIncidentQuarantined
	case errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound):
		return RouteRuntimeIncidentStaleMissing
	case errors.Is(err, extensionsruntime.ErrRuntimeInstanceConflict):
		return RouteRuntimeIncidentStaleArtifact
	default:
		r.logger.Error("route failure runtime quarantine failed",
			"extension_id", evidence.Artifact.ExtensionID,
			"runtime_instance_id", evidence.Artifact.RuntimeInstanceID,
			"failure_code", evidence.FailureCode,
			"cause_class", evidence.CauseClass,
			"error", err,
		)
		return RouteRuntimeIncidentFailed
	}
}

func routeCommittedFailureIncidentEvidence(event routes.RouteCommittedAfterFailure) RouteRuntimeIncidentEvidence {
	causeClass := "runtime_transport"
	if event.FailureCode == routes.RouteFailureResponseSchemaRejected {
		causeClass = "response_schema"
	}
	return RouteRuntimeIncidentEvidence{
		RouteRevision: event.Revision, StepIndex: event.StepIndex, Phase: event.Phase,
		InvocationStage: event.InvocationStage, Action: event.Action, Mode: extensionmanifest.RouteModeHTTP,
		RouteID: event.RouteID, ContractVersion: event.ContractVersion,
		Method: event.Method, PathSignature: event.PathSignature,
		FailureCode: event.FailureCode, CauseClass: causeClass,
		RuntimeExecutionObserved: event.RuntimeExecutionObserved, ActorID: event.ActorID,
		ResponseStatus: normalizedRouteRuntimeIncidentStatus(event.ResponseStatus),
		CommitState:    event.CommitState, Artifact: event.Artifact,
	}
}

func routeStreamFailureIncidentEvidence(event routes.RouteStreamFailure) RouteRuntimeIncidentEvidence {
	return RouteRuntimeIncidentEvidence{
		RouteRevision: event.Revision, StepIndex: event.StepIndex, Phase: event.Phase,
		InvocationStage: event.InvocationStage, Action: event.Action, Mode: event.Mode,
		RouteID: event.RouteID, ContractVersion: event.ContractVersion,
		Method: event.Method, PathSignature: event.PathSignature,
		FailureCode: event.FailureCode, CauseClass: string(event.CauseClass),
		RuntimeExecutionObserved: event.RuntimeExecutionObserved, ActorID: event.ActorID,
		ResponseStatus: normalizedRouteRuntimeIncidentStatus(event.ResponseStatus),
		CommitState:    event.CommitState, Artifact: event.Artifact,
	}
}

func normalizedRouteRuntimeIncidentStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func (r *RouteFailureRecorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

func (r *RouteFailureRecorder) IncidentPersistenceFailures() uint64 {
	if r == nil {
		return 0
	}
	return r.incidentPersistenceFailures.Load()
}

func (r *RouteFailureRecorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("route failure recorder close context is nil")
	}
	r.enqueueMu.Lock()
	if !r.closed {
		r.closed = true
		r.stopOnce.Do(func() { close(r.stop) })
	}
	r.enqueueMu.Unlock()
	finished := make(chan struct{})
	go func() {
		r.incidentWG.Wait()
		<-r.done
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *RouteFailureRecorder) run() {
	defer close(r.done)
	for {
		select {
		case item := <-r.queue:
			r.appendAudit(item)
		case <-r.stop:
			for {
				select {
				case item := <-r.queue:
					r.appendAudit(item)
				default:
					return
				}
			}
		}
	}
}

func (r *RouteFailureRecorder) appendAudit(item recordedRouteFailure) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	event := item.event
	err := r.auditor.Append(ctx, audit.Event{
		ActorUserID: event.ActorID,
		Action:      audit.ActionRouteCommittedAfterFailure,
		Metadata: map[string]any{
			"revision": event.Revision, "stepIndex": event.StepIndex,
			"phase": event.Phase, "invocationStage": event.InvocationStage, "action": event.Action,
			"routeId": event.RouteID, "contractVersion": event.ContractVersion,
			"method": event.Method, "pathSignature": event.PathSignature,
			"failureCode": event.FailureCode, "runtimeExecutionObserved": event.RuntimeExecutionObserved,
			"responseStatus": event.ResponseStatus, "commitState": event.CommitState,
			"extensionId": event.Artifact.ExtensionID, "extensionVersion": event.Artifact.ExtensionVersion,
			"packageDigest": event.Artifact.PackageDigest, "runtimeInstanceId": event.Artifact.RuntimeInstanceID,
			"quarantineResult": item.quarantine,
		},
	})
	if err != nil {
		r.logger.Error("record route committed-after failure audit failed",
			"extension_id", event.Artifact.ExtensionID,
			"runtime_instance_id", event.Artifact.RuntimeInstanceID,
			"failure_code", event.FailureCode,
			"error", err,
		)
	}
}

func routeFailureRequiresQuarantine(event routes.RouteCommittedAfterFailure) bool {
	return event.RuntimeExecutionObserved &&
		(event.FailureCode == routes.RouteFailureResponseSchemaRejected ||
			event.FailureCode == routes.RouteFailureTransportFailed)
}

var _ routes.RouteFailureSink = (*RouteFailureRecorder)(nil)
var _ routes.RouteStreamFailureSink = (*RouteFailureRecorder)(nil)
