package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteStreamRuntimeIncidentReachesRecorder(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.recorder.join", 'd')
	declaration := routeDispatcherManifestRoute(
		"stream.recorder.join.handler", extensionmanifest.RouteActionAdd,
		"/stream-recorder-join", http.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime := &recordingRouteIncidentRuntime{}
	incidents := &recordingRouteRuntimeIncidentStore{}
	auditor := &recordingRouteFailureAuditor{}
	recorder, err := newRouteFailureRecorder(
		runtime, incidents, auditor, discardRouteFailureLogger(), 4, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRouteFailureRecorderForTest(t, recorder) })
	session := &streamHTTPTestSession{recvErr: errors.New("runtime crashed")}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: http.StatusOK}, Session: session,
		}},
		Guard: HostRouteGuardAuthorizer{}, StreamFailures: recorder,
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: http.MethodGet, Path: "/stream-recorder-join"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared.Dispatch.ResponseStarted()
	streamRouteResponse(bufio.NewWriter(bytes.NewBuffer(nil)), start.Session, prepared.Dispatch)

	incidents.mu.Lock()
	created := append([]RouteRuntimeIncidentEvidence(nil), incidents.created...)
	resolved := append([]recordedRouteRuntimeIncidentResolution(nil), incidents.resolved...)
	incidents.mu.Unlock()
	if len(created) != 1 || len(resolved) != 1 ||
		created[0].CauseClass != string(routes.RouteStreamFailureRuntimeTransport) ||
		created[0].Artifact != artifact || created[0].IncidentKey == "" ||
		resolved[0].key != created[0].IncidentKey || resolved[0].result != RouteRuntimeIncidentQuarantined {
		t.Fatalf("created=%#v resolved=%#v", created, resolved)
	}
	runtime.mu.Lock()
	quarantines := append([]recordedRouteIncident(nil), runtime.calls...)
	runtime.mu.Unlock()
	if len(quarantines) != 1 || !errors.Is(quarantines[0].cause, extensionsruntime.ErrRuntimeRouteIncident) {
		t.Fatalf("quarantines=%#v", quarantines)
	}
	auditor.mu.Lock()
	ordinaryAudits := len(auditor.events)
	auditor.mu.Unlock()
	if ordinaryAudits != 0 || recorder.IncidentPersistenceFailures() != 0 {
		t.Fatalf("ordinary audits=%d persistence failures=%d", ordinaryAudits, recorder.IncidentPersistenceFailures())
	}
}

func TestRouteFailureRecorderPersistsStreamIncidentBeforeQuarantineAndResolution(t *testing.T) {
	order := &recordingRouteIncidentOrder{}
	runtime := &recordingRouteIncidentRuntime{order: order}
	incidents := &recordingRouteRuntimeIncidentStore{order: order}
	recorder, err := newRouteFailureRecorder(
		runtime, incidents, &recordingRouteFailureAuditor{}, discardRouteFailureLogger(), 4, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	classes := []routes.RouteStreamFailureClass{
		routes.RouteStreamFailureRuntimeTransport,
		routes.RouteStreamFailureHostBudget,
		routes.RouteStreamFailureInvalidPreflight,
		routes.RouteStreamFailureMissingTerminal,
	}
	for index, class := range classes {
		event := routeFailureRecorderStreamEvent(class)
		if index == 2 {
			event.ResponseStatus = 700
		}
		recorder.RecordStreamFailure(context.Background(), event)
	}
	closeRouteFailureRecorderForTest(t, recorder)

	incidents.mu.Lock()
	created := append([]RouteRuntimeIncidentEvidence(nil), incidents.created...)
	resolved := append([]recordedRouteRuntimeIncidentResolution(nil), incidents.resolved...)
	incidents.mu.Unlock()
	if len(created) != len(classes) || len(resolved) != len(classes) {
		t.Fatalf("created=%#v resolved=%#v", created, resolved)
	}
	for index, evidence := range created {
		wantStatus := http.StatusOK
		if index == 2 {
			wantStatus = 0
		}
		if evidence.CauseClass != string(classes[index]) || evidence.IncidentKey == "" ||
			evidence.ResponseStatus != wantStatus ||
			resolved[index].key != evidence.IncidentKey || resolved[index].result != RouteRuntimeIncidentQuarantined {
			t.Fatalf("created[%d]=%#v resolved=%#v", index, evidence, resolved[index])
		}
	}
	if got := order.snapshot(); len(got) != len(classes)*3 {
		t.Fatalf("order=%#v", got)
	} else {
		for index := range classes {
			triplet := got[index*3 : index*3+3]
			if strings.Join(triplet, ",") != "persist,quarantine,resolve" {
				t.Fatalf("order[%d]=%#v", index, triplet)
			}
		}
	}
	if recorder.IncidentPersistenceFailures() != 0 {
		t.Fatalf("persistence failures=%d", recorder.IncidentPersistenceFailures())
	}
}

func TestRouteFailureRecorderQuarantinesWhenIncidentPersistenceFails(t *testing.T) {
	runtime := &recordingRouteIncidentRuntime{}
	incidents := &recordingRouteRuntimeIncidentStore{createErr: errors.New("database unavailable")}
	recorder, err := newRouteFailureRecorder(
		runtime, incidents, &recordingRouteFailureAuditor{}, discardRouteFailureLogger(), 2, 20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder.RecordStreamFailure(context.Background(), routeFailureRecorderStreamEvent(
		routes.RouteStreamFailureRuntimeTransport,
	))
	closeRouteFailureRecorderForTest(t, recorder)
	runtime.mu.Lock()
	quarantines := len(runtime.calls)
	runtime.mu.Unlock()
	incidents.mu.Lock()
	created, resolved := len(incidents.created), len(incidents.resolved)
	incidents.mu.Unlock()
	if quarantines != 1 || created != 1 || resolved != 0 || recorder.IncidentPersistenceFailures() != 1 {
		t.Fatalf("quarantines=%d created=%d resolved=%d failures=%d", quarantines, created, resolved, recorder.IncidentPersistenceFailures())
	}
}

func TestRouteFailureRecorderMapsEveryLocalQuarantineResult(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want RouteRuntimeIncidentLocalResult
	}{
		{name: "quarantined", want: RouteRuntimeIncidentQuarantined},
		{name: "missing", err: extensionsruntime.ErrRuntimeInstanceNotFound, want: RouteRuntimeIncidentStaleMissing},
		{name: "artifact drift", err: extensionsruntime.ErrRuntimeInstanceConflict, want: RouteRuntimeIncidentStaleArtifact},
		{name: "failure", err: errors.New("local quarantine failed"), want: RouteRuntimeIncidentFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incidents := &recordingRouteRuntimeIncidentStore{}
			recorder, err := newRouteFailureRecorder(
				&recordingRouteIncidentRuntime{err: test.err}, incidents,
				&recordingRouteFailureAuditor{}, discardRouteFailureLogger(), 2, time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			recorder.RecordStreamFailure(context.Background(), routeFailureRecorderStreamEvent(
				routes.RouteStreamFailureRuntimeTransport,
			))
			closeRouteFailureRecorderForTest(t, recorder)
			incidents.mu.Lock()
			defer incidents.mu.Unlock()
			if len(incidents.resolved) != 1 || incidents.resolved[0].result != test.want {
				t.Fatalf("resolved=%#v", incidents.resolved)
			}
		})
	}
}

func TestRouteFailureRecorderCloseHonorsDeadlineWithIncidentInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	incidents := &recordingRouteRuntimeIncidentStore{createStarted: started, createRelease: release}
	recorder, err := newRouteFailureRecorder(
		&recordingRouteIncidentRuntime{}, incidents,
		&recordingRouteFailureAuditor{}, discardRouteFailureLogger(), 2, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	recorded := make(chan struct{})
	go func() {
		recorder.RecordStreamFailure(context.Background(), routeFailureRecorderStreamEvent(
			routes.RouteStreamFailureRuntimeTransport,
		))
		close(recorded)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("incident persistence did not start")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if err := recorder.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("close ignored deadline for %s", elapsed)
	}
	close(release)
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("incident did not finish after persistence release")
	}
	closeRouteFailureRecorderForTest(t, recorder)
}

func routeFailureRecorderStreamEvent(class routes.RouteStreamFailureClass) routes.RouteStreamFailure {
	return routes.RouteStreamFailure{
		Revision: 7, StepIndex: 0, Phase: routes.RoutePhaseHandler,
		InvocationStage: routes.InvocationStageHandler, Action: extensionmanifest.RouteActionAdd,
		Mode: extensionmanifest.RouteModeStream, RouteID: "failure.plugin.stream",
		ContractVersion: "failure.plugin.stream@1", Method: http.MethodGet,
		PathSignature: "/s:stream", FailureCode: routes.RouteFailureTransportFailed,
		CauseClass: class, RuntimeExecutionObserved: true, ActorID: 42,
		ResponseStatus: http.StatusOK, CommitState: routes.RouteCommitResponseStarted,
		Artifact: routes.PluginArtifact{
			ExtensionID: "failure.plugin", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-1",
		},
	}
}

func closeRouteFailureRecorderForTest(t *testing.T, recorder *RouteFailureRecorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type recordedRouteRuntimeIncidentResolution struct {
	key    string
	result RouteRuntimeIncidentLocalResult
}

type recordingRouteRuntimeIncidentStore struct {
	mu            sync.Mutex
	created       []RouteRuntimeIncidentEvidence
	resolved      []recordedRouteRuntimeIncidentResolution
	createErr     error
	resolveErr    error
	order         *recordingRouteIncidentOrder
	createStarted chan struct{}
	createRelease chan struct{}
	createOnce    sync.Once
}

func (s *recordingRouteRuntimeIncidentStore) CreatePending(
	_ context.Context,
	evidence RouteRuntimeIncidentEvidence,
) (RouteRuntimeIncidentRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, evidence)
	if s.order != nil {
		s.order.append("persist")
	}
	if s.createStarted != nil {
		s.createOnce.Do(func() { close(s.createStarted) })
	}
	if s.createRelease != nil {
		<-s.createRelease
	}
	if s.createErr != nil {
		return RouteRuntimeIncidentRecord{}, false, s.createErr
	}
	return RouteRuntimeIncidentRecord{Evidence: evidence, LocalResult: RouteRuntimeIncidentPending}, true, nil
}

func (s *recordingRouteRuntimeIncidentStore) Resolve(
	_ context.Context,
	key string,
	result RouteRuntimeIncidentLocalResult,
) (RouteRuntimeIncidentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved = append(s.resolved, recordedRouteRuntimeIncidentResolution{key: key, result: result})
	if s.order != nil {
		s.order.append("resolve")
	}
	return RouteRuntimeIncidentRecord{LocalResult: result}, s.resolveErr
}

type recordingRouteIncidentOrder struct {
	mu    sync.Mutex
	steps []string
}

func (o *recordingRouteIncidentOrder) append(step string) {
	o.mu.Lock()
	o.steps = append(o.steps, step)
	o.mu.Unlock()
}

func (o *recordingRouteIncidentOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

var _ RouteRuntimeIncidentStore = (*recordingRouteRuntimeIncidentStore)(nil)
