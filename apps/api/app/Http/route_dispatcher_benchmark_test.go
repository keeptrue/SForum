package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	benchmarkRouteExtensionID = "benchmark.v3"
	benchmarkRouteTargetID    = "core.route.forum.topic"
	benchmarkRoutePath        = "/api/v1/topics/:topicID"
	benchmarkRouteRequestPath = "/api/v1/topics/42"
)

func BenchmarkRouteDispatcherV3ProductionPath(b *testing.B) {
	b.Run("V1NamespacedProxyComparable", func(b *testing.B) {
		fixture := newV1ComparableRouteBenchmark(b)
		fixture.warm(b, 32)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := fixture.dispatch(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("CoreBypass", func(b *testing.B) {
		fixture := newV3CoreBypassBenchmark(b)
		benchmarkV3Dispatch(b, fixture)
	})
	b.Run("SelectedPluginHTTP", func(b *testing.B) {
		fixture := newV3PluginRouteBenchmark(b, false)
		benchmarkV3Dispatch(b, fixture)
	})
	b.Run("ComposedChainHTTP", func(b *testing.B) {
		fixture := newV3PluginRouteBenchmark(b, true)
		benchmarkV3Dispatch(b, fixture)
	})
}

type v1RouteBenchmarkFixture struct {
	gateway    *extensionsruntime.RouteGateway
	gate       *extensionsruntime.RuntimeAdmissionGate
	request    *fasthttp.Request
	response   *fasthttp.Response
	targetBase string
}

func (f *v1RouteBenchmarkFixture) dispatch() error {
	lease, err := f.gate.Acquire(context.Background(), extensionsruntime.RuntimeCallRoute)
	if err != nil {
		return err
	}
	defer lease.Release()
	f.response.Reset()
	err = f.gateway.Proxy(&extensionsruntime.ProxyInput{
		Context: lease.Context, Request: f.request, Response: f.response,
		ExtensionID: benchmarkRouteExtensionID, TargetBase: f.targetBase,
		TargetPath: benchmarkRouteRequestPath, Timeout: 3 * time.Second,
	})
	if err != nil {
		return err
	}
	if f.response.StatusCode() != stdhttp.StatusOK || string(f.response.Body()) != `{"ok":true}` {
		return fmt.Errorf("v1 response status=%d body=%q", f.response.StatusCode(), f.response.Body())
	}
	return nil
}

func (f *v1RouteBenchmarkFixture) warm(tb testing.TB, count int) {
	tb.Helper()
	for range count {
		if err := f.dispatch(); err != nil {
			tb.Fatal(err)
		}
	}
}

func newV1ComparableRouteBenchmark(tb testing.TB) *v1RouteBenchmarkFixture {
	tb.Helper()
	server := newV3BenchmarkServer(tb)
	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: benchmarkRouteExtensionID, InstanceID: "runtime-benchmark",
	}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		tb.Fatal(err)
	}
	request := fasthttp.AcquireRequest()
	response := fasthttp.AcquireResponse()
	tb.Cleanup(func() {
		fasthttp.ReleaseRequest(request)
		fasthttp.ReleaseResponse(response)
	})
	request.Header.SetMethod(stdhttp.MethodGet)
	request.Header.SetContentType("application/json")
	request.SetRequestURI("/api/v1/extensions/" + benchmarkRouteExtensionID + benchmarkRouteRequestPath)
	request.SetBodyRaw([]byte(`{"ok":true}`))
	return &v1RouteBenchmarkFixture{
		gateway: extensionsruntime.NewRouteGateway(), gate: gate, request: request,
		response: response, targetBase: server.URL,
	}
}

// Allocation budgets are intentionally generous enough to remain stable across
// Go patch releases while still catching an accidental second snapshot, HTTP
// client, schema compiler, or unbounded trace allocation per request.
func TestRouteDispatcherV3AllocationBudgets(t *testing.T) {
	tests := []struct {
		name      string
		fixture   func(testing.TB) *v3RouteBenchmarkFixture
		maxAllocs float64
	}{
		{name: "core bypass", fixture: newV3CoreBypassBenchmark, maxAllocs: 64},
		{name: "selected plugin HTTP", fixture: func(tb testing.TB) *v3RouteBenchmarkFixture {
			return newV3PluginRouteBenchmark(tb, false)
		}, maxAllocs: 480},
		{name: "composed chain HTTP", fixture: func(tb testing.TB) *v3RouteBenchmarkFixture {
			return newV3PluginRouteBenchmark(tb, true)
		}, maxAllocs: 2100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			fixture.warm(t, 16)
			allocs := testing.AllocsPerRun(50, func() {
				if err := fixture.dispatch(); err != nil {
					t.Fatal(err)
				}
			})
			if allocs > test.maxAllocs {
				t.Fatalf("allocs/op=%.0f exceeds budget %.0f", allocs, test.maxAllocs)
			}
			t.Logf("allocs/op=%.0f budget=%.0f", allocs, test.maxAllocs)
		})
	}
}

func TestRoutePlanningInternalSnapshotDoesNotEscapeThroughPlan(t *testing.T) {
	registry := routes.NewRegistry()
	catalog := routes.CoreRouteCatalog()
	if _, err := registry.Publish(routes.Publication{Core: catalog}); err != nil {
		t.Fatal(err)
	}

	plan, err := registry.BuildExecutionPlan(stdhttp.MethodGet, "/api/v1/admin/attachment-settings")
	if err != nil {
		t.Fatal(err)
	}
	chain := plan.Chain()
	if len(chain) != 1 || len(chain[0].CoreGuard.Permissions) != 1 {
		t.Fatalf("unexpected guard chain: %#v", chain)
	}
	chain[0].CoreGuard.Permissions[0] = "mutated.permission"
	params := plan.Params()
	params["forged"] = "value"

	again, err := registry.BuildExecutionPlan(stdhttp.MethodGet, "/api/v1/admin/attachment-settings")
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Terminal().CoreGuard.Permissions; len(got) != 1 || got[0] != "attachment.settings.manage" {
		t.Fatalf("plan mutation escaped into registry snapshot: %#v", got)
	}
	if _, exists := again.Params()["forged"]; exists {
		t.Fatal("plan params mutation escaped into registry snapshot")
	}
}

func TestRoutePlanningInternalSnapshotIsRaceSafeDuringPublication(t *testing.T) {
	registry := routes.NewRegistry()
	catalog := routes.CoreRouteCatalog()
	if _, err := registry.Publish(routes.Publication{Core: catalog}); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, readers)
	var wait sync.WaitGroup
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for range 100 {
				plan, err := registry.BuildExecutionPlan(stdhttp.MethodGet, "/api/v1/admin/attachment-settings")
				if err != nil {
					errorsSeen <- err
					return
				}
				chain := plan.Chain()
				if len(chain) != 1 || len(chain[0].CoreGuard.Permissions) != 1 {
					errorsSeen <- fmt.Errorf("unexpected guard chain: %#v", chain)
					return
				}
				chain[0].CoreGuard.Permissions[0] = "caller.mutation"
			}
		}()
	}
	close(start)
	for range 100 {
		if _, err := registry.Publish(routes.Publication{Core: catalog}); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
}

type v3RouteBenchmarkFixture struct {
	dispatcher *routes.Dispatcher
	request    routes.DispatchRequest
	wantHandle bool
}

func (f *v3RouteBenchmarkFixture) dispatch() error {
	result, err := f.dispatcher.Dispatch(context.Background(), f.request, nil)
	if err != nil {
		return err
	}
	if result.Handled != f.wantHandle {
		return fmt.Errorf("handled=%v want=%v", result.Handled, f.wantHandle)
	}
	if f.wantHandle && (result.Response.Status != stdhttp.StatusOK || string(result.Response.Body) != `{"ok":true}`) {
		return fmt.Errorf("response=%#v", result.Response)
	}
	return nil
}

func (f *v3RouteBenchmarkFixture) warm(tb testing.TB, count int) {
	tb.Helper()
	for range count {
		if err := f.dispatch(); err != nil {
			tb.Fatal(err)
		}
	}
}

func benchmarkV3Dispatch(b *testing.B, fixture *v3RouteBenchmarkFixture) {
	b.Helper()
	fixture.warm(b, 32)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := fixture.dispatch(); err != nil {
			b.Fatal(err)
		}
	}
}

func newV3CoreBypassBenchmark(tb testing.TB) *v3RouteBenchmarkFixture {
	tb.Helper()
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Core: routes.CoreRouteCatalog()}); err != nil {
		tb.Fatal(err)
	}
	resolver := routes.NewProviderSelectionAPI(registry, &benchmarkRouteSelectionStore{})
	return &v3RouteBenchmarkFixture{
		dispatcher: routes.NewDispatcher(routes.DispatcherConfig{Plans: resolver}),
		request: routes.DispatchRequest{
			Method: stdhttp.MethodGet, Path: benchmarkRouteRequestPath,
		},
	}
}

func newV3PluginRouteBenchmark(tb testing.TB, composed bool) *v3RouteBenchmarkFixture {
	tb.Helper()
	artifactInput, declarations := buildV3BenchmarkArtifact(tb, composed)
	artifact := routes.PluginArtifact{
		ExtensionID: artifactInput.ExtensionID, ExtensionVersion: artifactInput.Version,
		PackageDigest: artifactInput.PackageDigest, RuntimeInstanceID: "runtime-benchmark",
	}
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{
		Core: routes.CoreRouteCatalog(), Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: declarations}},
	}); err != nil {
		tb.Fatal(err)
	}
	selectionStore := &benchmarkRouteSelectionStore{}
	resolver := routes.NewProviderSelectionAPI(registry, selectionStore)
	selectV3BenchmarkProvider(tb, resolver, registry, artifact)

	catalog, err := extensionopenapi.BuildRouteSchemaCatalog(extensionopenapi.BuildInput{
		Core: []extensionopenapi.CoreOperation{{
			RouteID: benchmarkRouteTargetID, Path: benchmarkRoutePath, Method: stdhttp.MethodGet,
			OperationID: "coreForumTopic",
		}},
		Artifacts: []extensionopenapi.Artifact{artifactInput},
	})
	if err != nil {
		tb.Fatal(err)
	}
	runtime := newV3BenchmarkRuntime(tb, artifact)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: resolver, Steps: NewBufferedRouteStepInvoker(runtime), Guard: HostRouteGuardAuthorizer{},
		Schemas: CatalogRouteSchemaValidator{Catalog: catalog}, Trace: routes.NewRouteTraceRing(4096),
	})
	return &v3RouteBenchmarkFixture{
		dispatcher: dispatcher,
		request: routes.DispatchRequest{
			Method: stdhttp.MethodGet, Path: benchmarkRouteRequestPath,
			Headers: stdhttp.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"ok":true}`),
		},
		wantHandle: true,
	}
}

func selectV3BenchmarkProvider(
	tb testing.TB,
	resolver *routes.ProviderSelectionAPI,
	registry *routes.Registry,
	artifact routes.PluginArtifact,
) {
	tb.Helper()
	pathSignature := ""
	for _, route := range registry.Snapshot().Routes {
		if route.ID == benchmarkRouteExtensionID+".replace" {
			pathSignature = route.PathSignature
			break
		}
	}
	if pathSignature == "" {
		tb.Fatal("benchmark replacement path signature is missing")
	}
	if _, err := resolver.Select(context.Background(), routes.SelectProviderRequest{
		Key: routes.ProviderSelectionKey{
			TargetRouteID: benchmarkRouteTargetID, TargetContractVersion: "sforum.route.forum.topic@1",
			Method: stdhttp.MethodGet, PathSignature: pathSignature,
		},
		ProviderRouteID:         benchmarkRouteExtensionID + ".replace",
		ProviderContractVersion: benchmarkRouteExtensionID + ".replace@1",
		ProviderArtifact:        artifact, ActorUserID: 1, AuditEventID: 1,
	}); err != nil {
		tb.Fatal(err)
	}
}

func buildV3BenchmarkArtifact(tb testing.TB, composed bool) (extensionopenapi.Artifact, []extensionmanifest.ManifestRoute) {
	tb.Helper()
	root := tb.TempDir()
	schemaBody := []byte(`{"Payload":{"type":"object","properties":{"ok":{"const":true}},"required":["ok"],"additionalProperties":false}}`)
	documentBody := []byte(v3BenchmarkOpenAPIDocument())
	writeV3BenchmarkFile(tb, root, "openapi/routes.yaml", documentBody)
	writeV3BenchmarkFile(tb, root, "openapi/schemas/payload.json", schemaBody)

	requestSchema := benchmarkRouteExtensionID + ".payload.request@1"
	responseSchema := benchmarkRouteExtensionID + ".payload.response@1"
	replacement := extensionmanifest.ManifestRoute{
		ID: benchmarkRouteExtensionID + ".replace", ContractVersion: benchmarkRouteExtensionID + ".replace@1",
		Action: extensionmanifest.RouteActionReplace, TargetID: benchmarkRouteTargetID,
		Path: benchmarkRoutePath, Methods: []string{stdhttp.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.replace",
		RequestSchema: requestSchema, ResponseSchema: responseSchema,
	}
	declarations := []extensionmanifest.ManifestRoute{replacement}
	if composed {
		declarations = append(declarations, v3BenchmarkCompositionRoutes(requestSchema, responseSchema)...)
	}
	manifest := extensionmanifest.Manifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              benchmarkRouteExtensionID, Name: "V3 Route Benchmark", Description: "Reproducible route benchmark fixture.",
		URL: "https://example.com/benchmark", Author: extensionmanifest.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensionmanifest.TypePlugin, SForumVersion: ">=0.1.0",
		Permissions: []string{}, Routes: declarations,
		OpenAPI: []extensionmanifest.ManifestOpenAPIFragment{{
			ID: benchmarkRouteExtensionID + ".openapi", ContractVersion: benchmarkRouteExtensionID + ".openapi@1",
			Path: "openapi/routes.yaml", Digest: v3BenchmarkDigest(documentBody), Namespace: benchmarkRouteExtensionID + ".api",
		}},
		PackageFiles: []extensionmanifest.ManifestPackageFile{
			{ID: benchmarkRouteExtensionID + ".file.openapi", Kind: "openapi", Path: "openapi/routes.yaml", Digest: v3BenchmarkDigest(documentBody)},
			{ID: benchmarkRouteExtensionID + ".file.schema", Kind: "schema", Path: "openapi/schemas/payload.json", Digest: v3BenchmarkDigest(schemaBody)},
		},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		tb.Fatal(err)
	}
	writeV3BenchmarkFile(tb, root, extensionmanifest.ManifestFileName, body)
	loaded, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		tb.Fatalf("load benchmark manifest: %v", err)
	}
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		tb.Fatal(err)
	}
	return extensionopenapi.Artifact{
		Root: root, ExtensionID: loaded.ID, Version: loaded.Version, PackageDigest: digest, Manifest: loaded,
		Policies: []extensionopenapi.RoutePolicy{{
			RouteID: replacement.ID, Method: stdhttp.MethodGet, RateLimit: "public.read@1",
			Idempotency: "disabled", Security: extensionopenapi.SecurityPublic,
		}},
	}, declarations
}

func v3BenchmarkCompositionRoutes(requestSchema, responseSchema string) []extensionmanifest.ManifestRoute {
	items := []extensionmanifest.ManifestRoute{
		{ID: benchmarkRouteExtensionID + ".global", ContractVersion: benchmarkRouteExtensionID + ".global@1", Action: extensionmanifest.RouteActionGlobalMiddleware},
		{ID: benchmarkRouteExtensionID + ".before", ContractVersion: benchmarkRouteExtensionID + ".before@1", Action: extensionmanifest.RouteActionBefore},
		{ID: benchmarkRouteExtensionID + ".filter", ContractVersion: benchmarkRouteExtensionID + ".filter@1", Action: extensionmanifest.RouteActionFilter},
		{ID: benchmarkRouteExtensionID + ".wrap", ContractVersion: benchmarkRouteExtensionID + ".wrap@1", Action: extensionmanifest.RouteActionWrap},
		{ID: benchmarkRouteExtensionID + ".after", ContractVersion: benchmarkRouteExtensionID + ".after@1", Action: extensionmanifest.RouteActionAfter},
	}
	for index := range items {
		items[index].Guard = extensionmanifest.GuardCorePublic
		items[index].Fallback = "closed"
		items[index].Mode = extensionmanifest.RouteModeHTTP
		items[index].Handler = "route." + strings.TrimPrefix(items[index].ID, benchmarkRouteExtensionID+".")
		items[index].Priority = 100 - index
		if items[index].Action != extensionmanifest.RouteActionGlobalMiddleware {
			items[index].TargetID = benchmarkRouteTargetID
			items[index].Path = benchmarkRoutePath
			items[index].Methods = []string{stdhttp.MethodGet}
		}
		switch items[index].Action {
		case extensionmanifest.RouteActionGlobalMiddleware:
			items[index].RequestSchema = requestSchema
			items[index].ResponseSchema = responseSchema
		case extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap:
			items[index].ResponseSchema = responseSchema
		}
	}
	return items
}

func v3BenchmarkOpenAPIDocument() string {
	return `openapi: 3.1.0
info:
  title: V3 Route Benchmark
  version: 1.0.0
paths:
  /api/v1/topics/{topicID}:
    get:
      operationId: benchmark.v3.api.getTopic
      x-sforum-route-id: benchmark.v3.replace
      x-sforum-contract-version: benchmark.v3.replace@1
      x-sforum-guard: core.guard.public
      x-sforum-request-schema: benchmark.v3.payload.request@1
      x-sforum-response-schema: benchmark.v3.payload.response@1
      x-sforum-rate-limit: public.read@1
      x-sforum-idempotency: disabled
      parameters:
        - name: topicID
          in: path
          required: true
          schema:
            type: string
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: 'schemas/payload.json#/Payload'
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: 'schemas/payload.json#/Payload'
`
}

func writeV3BenchmarkFile(tb testing.TB, root, name string, body []byte) {
	tb.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		tb.Fatal(err)
	}
}

func v3BenchmarkDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

type benchmarkRouteRuntime struct {
	snapshot extensionsruntime.RuntimeInstanceSnapshot
	gate     *extensionsruntime.RuntimeAdmissionGate
}

func newV3BenchmarkRuntime(tb testing.TB, artifact routes.PluginArtifact) *benchmarkRouteRuntime {
	tb.Helper()
	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID,
	}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		tb.Fatal(err)
	}
	return &benchmarkRouteRuntime{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: artifact.ExtensionVersion, ArtifactDigest: artifact.PackageDigest,
			Target: extensionsruntime.RouteTarget{InstanceID: artifact.RuntimeInstanceID}, Active: true,
		},
	}
}

func newV3BenchmarkServer(tb testing.TB) *httptest.Server {
	tb.Helper()
	server := httptest.NewServer(stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(stdhttp.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	tb.Cleanup(server.Close)
	return server
}

func (r *benchmarkRouteRuntime) InspectRuntimeInstance(identity extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if r == nil || identity != r.snapshot.Identity {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.snapshot, nil
}

func (r *benchmarkRouteRuntime) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	if r == nil || identity != r.snapshot.Identity || class != extensionsruntime.RuntimeCallRoute {
		return nil, extensionsruntime.ErrRuntimeInstanceNotActive
	}
	return r.gate.Acquire(ctx, class)
}

func (r *benchmarkRouteRuntime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if r == nil || identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	if request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageHandler {
		return extensionsruntime.ProtocolV2RouteResponse{}, nil
	}
	return extensionsruntime.ProtocolV2RouteResponse{
		StatusCode: stdhttp.StatusOK,
		Headers:    stdhttp.Header{"Content-Type": []string{"application/json"}},
		Body:       map[string]any{"ok": true}, BodyPresent: true,
	}, nil
}

type benchmarkRouteSelectionStore struct {
	selection routes.ProviderSelection
}

func (s *benchmarkRouteSelectionStore) Desired(ctx context.Context, key routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	return s.Selected(ctx, key)
}

func (s *benchmarkRouteSelectionStore) Selected(_ context.Context, key routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	if s.selection.Revision == 0 {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionNotFound
	}
	if s.selection.Key != key {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionStale
	}
	return s.selection, nil
}

func (s *benchmarkRouteSelectionStore) Select(_ context.Context, request routes.SelectProviderRequest) (routes.ProviderSelection, error) {
	s.selection = routes.ProviderSelection{
		Key: request.Key, ProviderRouteID: request.ProviderRouteID,
		ProviderContractVersion:  request.ProviderContractVersion,
		ProviderExtensionID:      request.ProviderArtifact.ExtensionID,
		ProviderExtensionVersion: request.ProviderArtifact.ExtensionVersion,
		ProviderPackageDigest:    request.ProviderArtifact.PackageDigest,
		SelectedByUserID:         request.ActorUserID, SelectionAuditEventID: request.AuditEventID,
		Revision: 1, SelectedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
	return s.selection, nil
}

func (*benchmarkRouteSelectionStore) Reset(context.Context, routes.ResetProviderRequest) error {
	return nil
}

func (*benchmarkRouteSelectionStore) InvalidateExtension(context.Context, routes.InvalidateProviderRequest) (int64, error) {
	return 0, nil
}

func (*benchmarkRouteSelectionStore) ListEvents(context.Context, routes.ProviderSelectionKey, int) ([]routes.ProviderSelectionEvent, error) {
	return []routes.ProviderSelectionEvent{}, nil
}
