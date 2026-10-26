package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteRequestPatchImmediatelyRevalidatesSchema(t *testing.T) {
	const (
		path   = "/api/v1/request-patch-schema"
		coreID = "core.route.request.patch.schema"
	)
	artifact := routeDispatcherArtifact("request.patch.schema", 'b')
	first := routeDispatcherManifestRoute(
		"request.patch.schema.first", extensionmanifest.RouteActionBefore, path, stdhttp.MethodPost,
	)
	first.TargetID = coreID
	first.Priority = 20
	first.RequestSchema = "request.patch.schema.first.request@1"
	first.MutableRequestFields = []string{"/body/title"}
	second := routeDispatcherManifestRoute(
		"request.patch.schema.second", extensionmanifest.RouteActionBefore, path, stdhttp.MethodPost,
	)
	second.TargetID = coreID
	second.Priority = 10
	second.RequestSchema = "request.patch.schema.second.request@1"

	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{{
			ID: coreID, ContractVersion: "sforum.route.request.patch.schema@1",
			Method: stdhttp.MethodPost, Path: path,
		}},
		Plugins: []routes.PluginRouteSet{{
			Artifact: artifact,
			Routes:   []extensionmanifest.ManifestRoute{first, second},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &requestPatchSchemaRuntime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact),
		firstRouteID:             first.ID,
		secondRouteID:            second.ID,
	}
	schemas := &requestPatchSchemaCatalog{firstRouteID: first.ID}
	traces := routes.NewRouteTraceRing(8)
	failures := &requestPatchSchemaFailureSink{}
	resolver := routeRegistryPlanResolver{registry: registry}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: resolver, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: schemas},
		Trace: traces, Failures: failures,
	})
	core := &requestPatchSchemaCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RoutePlans: resolver, RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{core},
	})

	request := httptest.NewRequest(
		stdhttp.MethodPost, path, strings.NewReader(`{"title":"valid","note":"keep"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != stdhttp.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}

	wantBodies := []map[string]any{
		{"title": "valid", "note": "keep"},
		{"title": json.Number("42"), "note": "keep"},
	}
	if schemas.calls != 2 || !reflect.DeepEqual(schemas.bodies, wantBodies) {
		t.Fatalf("schema calls=%d routes=%#v bodies=%#v", schemas.calls, schemas.routeIDs, schemas.bodies)
	}
	if !reflect.DeepEqual(schemas.routeIDs, []string{first.ID, first.ID}) {
		t.Fatalf("schema routes=%#v", schemas.routeIDs)
	}
	if runtime.firstCalls != 1 || runtime.secondCalls != 0 || runtime.activeAtInvoke != 1 || core.calls != 0 {
		t.Fatalf("runtime first=%d second=%d active=%d core=%d", runtime.firstCalls, runtime.secondCalls, runtime.activeAtInvoke, core.calls)
	}
	if active := runtime.gate.Snapshot().ActiveTotal; active != 0 {
		t.Fatalf("runtime admission leaked: active=%d", active)
	}
	if failures.calls != 0 {
		t.Fatalf("committed-after failure events=%d", failures.calls)
	}
	records := traces.RouteTraces(0)
	if len(records) != 1 || records[0].StepIndex != 0 || records[0].RouteID != first.ID ||
		records[0].Action != extensionmanifest.RouteActionBefore ||
		records[0].InvocationStage != routes.InvocationStageRequest ||
		records[0].Outcome != routes.RouteTraceSchemaRejected ||
		records[0].CommitState != routes.RouteCommitSideEffectStarted ||
		records[0].Provider.Artifact == nil || *records[0].Provider.Artifact != artifact {
		t.Fatalf("traces=%#v", records)
	}
}

type requestPatchSchemaCatalog struct {
	firstRouteID string
	calls        int
	routeIDs     []string
	bodies       []map[string]any
}

func (c *requestPatchSchemaCatalog) ValidateRouteSchema(
	_ context.Context,
	_ routes.PluginArtifact,
	direction string,
	routeID string,
	_, _, _, _, _, _ string,
	_ int,
	body []byte,
) error {
	if direction != "request" || routeID != c.firstRouteID {
		return errors.New("unexpected route schema validation")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	c.calls++
	c.routeIDs = append(c.routeIDs, routeID)
	c.bodies = append(c.bodies, payload)
	if c.calls == 2 {
		return errors.New("patched request violates schema")
	}
	return nil
}

type requestPatchSchemaRuntime struct {
	*routeDispatcherV2Runtime
	firstRouteID   string
	secondRouteID  string
	firstCalls     int
	secondCalls    int
	activeAtInvoke int
}

func (r *requestPatchSchemaRuntime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity || request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
	r.activeAtInvoke = r.gate.Snapshot().ActiveTotal
	switch request.RouteID {
	case r.firstRouteID:
		r.firstCalls++
		return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
			Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/body/title", Value: []byte(`42`),
		}}}, nil
	case r.secondRouteID:
		r.secondCalls++
		return extensionsruntime.ProtocolV2RouteResponse{}, nil
	default:
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
}

type requestPatchSchemaCoreProvider struct{ calls int }

func (p *requestPatchSchemaCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Post("/request-patch-schema", func(c fiber.Ctx) error {
		p.calls++
		return c.SendStatus(stdhttp.StatusNoContent)
	})
}

type requestPatchSchemaFailureSink struct{ calls int }

func (s *requestPatchSchemaFailureSink) RecordCommittedAfterFailure(context.Context, routes.RouteCommittedAfterFailure) {
	s.calls++
}

var _ routes.RouteFailureSink = (*requestPatchSchemaFailureSink)(nil)
