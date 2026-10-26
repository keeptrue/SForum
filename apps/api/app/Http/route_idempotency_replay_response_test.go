package http

import (
	"encoding/json"
	stdhttp "net/http"
	"reflect"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteReplayResponseContractStorageRoundTrip(t *testing.T) {
	want := &routes.RouteReplayResponseContract{
		StepIndex: 3, InvocationStage: routes.InvocationStageResponse,
		RouteID: "demo.route.after", ContractVersion: "demo.route.after@1",
		ResponseSchema: "demo.route.after.response@1",
	}
	stored := routeReplayResponseContractForStorage(want)
	got := routeReplayResponseContractFromStored(stored)
	if !reflect.DeepEqual(got, want) || stored == nil {
		t.Fatalf("round trip=%#v stored=%#v want=%#v", got, stored, want)
	}
	stored.RouteID = "mutated"
	if want.RouteID != "demo.route.after" {
		t.Fatalf("storage conversion retained caller pointer: %#v", want)
	}
}

func TestRequiredRouteReplayReappliesCurrentResponseHeaderPolicy(t *testing.T) {
	const key = "legacy-header-policy"
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("idempotency.route", '1')
	handler := routeDispatcherManifestRoute(
		"idempotency.route.create", extensionmanifest.RouteActionAdd,
		requiredReplayTestPath, stdhttp.MethodPost,
	)
	handler.RequestSchema = handler.ID + ".request@1"
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodPost, requiredReplayTestPath)
	if err != nil {
		t.Fatal(err)
	}
	dispatchRequest := requiredReplayV1DispatchRequest(requiredReplayTestPath, key)
	dispatchRequest.Params = plan.Params()
	_, legacyFingerprint, _, err := routeReplayFingerprints(plan, dispatchRequest)
	if err != nil {
		t.Fatal(err)
	}
	backend := &requiredReplayV1RecordBackend{raw: requiredReplayV1ResponseRecord(t, legacyFingerprint, idempotency.RequiredReplayResponse{
		Status: stdhttp.StatusCreated,
		Headers: stdhttp.Header{
			"Content-Type":             {"application/json"},
			"X-Public":                 {"kept"},
			"Link":                     {`<https://evil.example/>; rel="canonical"`, `</asset.js>; rel="preload"`},
			"Connection":               {"X-Hop, Keep-Alive"},
			"X-Hop":                    {"forged"},
			"Keep-Alive":               {"timeout=5"},
			"Proxy-Connection":         {"keep-alive"},
			"X-SForum-Actor-ID":        {"forged"},
			idempotency.ReplayedHeader: {"forged"},
		},
		Body:          []byte(`{"created":true}`),
		CanonicalPath: "/host-canonical",
	})}
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{backend: backend})

	response := requiredReplayRequest(t, app, requiredReplayRequestInput{
		KeyValues: []string{key}, Query: "tag=one", ContentType: "application/json", Body: `{}`,
	})
	defer response.Body.Close()
	if response.StatusCode != stdhttp.StatusCreated || calls.Load() != 0 ||
		backend.getCalls != 1 || backend.mutationCalls != 0 {
		t.Fatalf(
			"status=%d plugin calls=%d backend get=%d mutations=%d",
			response.StatusCode, calls.Load(), backend.getCalls, backend.mutationCalls,
		)
	}
	if response.Header.Get(idempotency.ReplayedHeader) != "true" ||
		response.Header.Get("Content-Type") != "application/json" || response.Header.Get("X-Public") != "kept" {
		t.Fatalf("Host/allowed headers=%#v", response.Header)
	}
	if got, want := response.Header.Values("Link"), []string{`</host-canonical>; rel="canonical"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical Link=%v want=%v", got, want)
	}
	for _, name := range []string{
		"Connection", "X-Hop", "Keep-Alive", "Proxy-Connection", "X-SForum-Actor-ID",
	} {
		if value := response.Header.Values(name); len(value) != 0 {
			t.Fatalf("forbidden replay header %s=%v", name, value)
		}
	}
}

func requiredReplayV1ResponseRecord(
	t *testing.T,
	fingerprint string,
	response idempotency.RequiredReplayResponse,
) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		Schema      string                              `json:"schema"`
		State       string                              `json:"state"`
		Fingerprint string                              `json:"fingerprint"`
		Response    *idempotency.RequiredReplayResponse `json:"response"`
	}{
		Schema: "sforum.required-route-replay@1", State: "completed",
		Fingerprint: fingerprint, Response: &response,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
