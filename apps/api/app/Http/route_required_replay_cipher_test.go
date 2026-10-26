package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRequiredRouteReplayMissingCipherRejectsMutablePlanBeforeAcquisitionAndProtocolV2(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("idempotency.mutable", '8')
	handler := routeDispatcherManifestRoute(
		"idempotency.mutable.handler", extensionmanifest.RouteActionAdd,
		"/required-replay-mutable", stdhttp.MethodPost,
	)
	handler.RequestSchema = handler.ID + ".request@1"
	before := routeDispatcherManifestRoute(
		"idempotency.mutable.before", extensionmanifest.RouteActionBefore,
		handler.Path, stdhttp.MethodPost,
	)
	before.TargetID = handler.ID
	before.MutableRequestFields = []string{"/query/tag"}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler, before},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodPost, handler.Path)
	if err != nil {
		t.Fatal(err)
	}
	chain := plan.Chain()
	if len(chain) != 2 || len(chain[0].MutableRequestFields) == 0 || chain[1].RouteID != handler.ID {
		t.Fatalf("test did not publish a mutable required-replay plan: %#v", chain)
	}

	backend := &requiredReplayCipherAccessBackend{inner: idempotency.NewMemoryBackend()}
	store := idempotency.NewStore(backend, idempotency.DefaultTTL)
	controller := NewRequiredRouteIdempotency(store)
	if controller.MutationReplayAvailable() || store.RequiredReplayCipherEnabled() {
		t.Fatal("missing APP_OPTION_ENC_KEY unexpectedly enabled mutation replay")
	}
	runtime := newRouteDispatcherV2RuntimeForArtifact(t, artifact)
	guard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans:       routeRegistryPlanResolver{registry: registry},
		Steps:       NewBufferedRouteStepInvoker(runtime),
		Guard:       guard,
		Schemas:     CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
		Policies:    requiredRoutePolicyResolver{},
		Idempotency: controller,
	})

	result, err := dispatcher.Dispatch(t.Context(), routes.DispatchRequest{
		Method:   stdhttp.MethodPost,
		Path:     handler.Path,
		Query:    "tag=private",
		Headers:  stdhttp.Header{idempotency.HeaderName: {"missing-cipher-key"}},
		ClientIP: "127.0.0.1",
	}, nil)
	if result.Handled || !errors.Is(err, routes.ErrDispatchIdempotencyUnavailable) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if backend.snapshot() != (requiredReplayCipherBackendCalls{}) || guard.calls != 0 || runtime.calls != 0 {
		t.Fatalf(
			"missing cipher crossed the pre-execution fence: backend=%#v guard=%d protocol_v2=%d",
			backend.snapshot(), guard.calls, runtime.calls,
		)
	}
}

func TestRequiredRouteReplayV1ResponseOnlyCompatibilityAndMutableFailClosed(t *testing.T) {
	t.Run("response-only V1 replay remains compatible", func(t *testing.T) {
		registry := routes.NewRegistry()
		artifact := routeDispatcherArtifact("idempotency.v1.response", '9')
		handler := routeDispatcherManifestRoute(
			"idempotency.v1.response.handler", extensionmanifest.RouteActionAdd,
			"/required-replay-v1-response", stdhttp.MethodPost,
		)
		handler.RequestSchema = handler.ID + ".request@1"
		if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler},
		}}}); err != nil {
			t.Fatal(err)
		}
		plan, err := registry.BuildExecutionPlan(stdhttp.MethodPost, handler.Path)
		if err != nil {
			t.Fatal(err)
		}
		request := requiredReplayV1DispatchRequest(handler.Path, "v1-response-key")
		request.Params = plan.Params()
		_, legacyFingerprint, _, err := routeReplayFingerprints(plan, request)
		if err != nil {
			t.Fatal(err)
		}
		backend := &requiredReplayV1RecordBackend{
			raw: requiredReplayV1CompletedRecord(t, legacyFingerprint, "legacy-response"),
		}
		runtime := newRouteDispatcherV2RuntimeForArtifact(t, artifact)
		guard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
		dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
			Plans:    routeRegistryPlanResolver{registry: registry},
			Steps:    NewBufferedRouteStepInvoker(runtime),
			Guard:    guard,
			Schemas:  CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
			Policies: requiredRoutePolicyResolver{},
			Idempotency: NewRequiredRouteIdempotency(
				idempotency.NewStore(backend, idempotency.DefaultTTL),
			),
		})
		result, err := dispatcher.Dispatch(t.Context(), request, nil)
		if err != nil || !result.Handled || result.Response.Status != stdhttp.StatusCreated ||
			string(result.Response.Body) != "legacy-response" ||
			result.Response.Headers.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if backend.getCalls != 1 || backend.mutationCalls != 0 || guard.calls != 1 || runtime.calls != 0 {
			t.Fatalf(
				"V1 response replay calls: get=%d mutation=%d guard=%d protocol_v2=%d",
				backend.getCalls, backend.mutationCalls, guard.calls, runtime.calls,
			)
		}
	})

	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("idempotency.v1.mutable", 'a')
	handler := routeDispatcherManifestRoute(
		"idempotency.v1.mutable.handler", extensionmanifest.RouteActionAdd,
		"/required-replay-v1-mutable", stdhttp.MethodPost,
	)
	handler.RequestSchema = handler.ID + ".request@1"
	before := routeDispatcherManifestRoute(
		"idempotency.v1.mutable.before", extensionmanifest.RouteActionBefore,
		handler.Path, stdhttp.MethodPost,
	)
	before.TargetID = handler.ID
	before.MutableRequestFields = []string{"/query/tag"}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler, before},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodPost, handler.Path)
	if err != nil {
		t.Fatal(err)
	}
	request := requiredReplayV1DispatchRequest(handler.Path, "v1-mutable-key")
	request.Params = plan.Params()
	currentFingerprint, legacyFingerprint, _, err := routeReplayFingerprints(plan, request)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		fingerprint string
		wantErr     error
	}{
		{name: "legacy fingerprint is not compatible with a composed plan", fingerprint: legacyFingerprint, wantErr: routes.ErrDispatchIdempotencyConflict},
		{name: "forged current fingerprint has no mutation transcript", fingerprint: currentFingerprint, wantErr: routes.ErrDispatchIdempotencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &requiredReplayV1RecordBackend{
				raw: requiredReplayV1CompletedRecord(t, test.fingerprint, "must-not-replay"),
			}
			cipher, err := idempotency.NewRequiredReplayCipher(strings.Repeat("08", 32))
			if err != nil {
				t.Fatal(err)
			}
			runtime := newRouteDispatcherV2RuntimeForArtifact(t, artifact)
			guard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
			dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
				Plans:    routeRegistryPlanResolver{registry: registry},
				Steps:    NewBufferedRouteStepInvoker(runtime),
				Guard:    guard,
				Schemas:  CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
				Policies: requiredRoutePolicyResolver{},
				Idempotency: NewRequiredRouteIdempotency(
					idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipher),
				),
			})
			result, dispatchErr := dispatcher.Dispatch(t.Context(), request, nil)
			if result.Handled || !errors.Is(dispatchErr, test.wantErr) {
				t.Fatalf("result=%#v error=%v", result, dispatchErr)
			}
			if backend.getCalls != 1 || backend.mutationCalls != 0 || guard.calls != 0 || runtime.calls != 0 {
				t.Fatalf(
					"V1 mutable replay crossed fence: get=%d mutation=%d guard=%d protocol_v2=%d",
					backend.getCalls, backend.mutationCalls, guard.calls, runtime.calls,
				)
			}
		})
	}
}

func TestRequiredRouteReplayMutableTranscriptSurvivesSameArtifactRuntimeRestart(t *testing.T) {
	artifact := routeDispatcherArtifact("idempotency.restart", 'b')
	firstRegistry, path := requiredReplayRestartRegistry(t, artifact)
	backend := idempotency.NewMemoryBackend()
	cipher, err := idempotency.NewRequiredReplayCipher(strings.Repeat("09", 32))
	if err != nil {
		t.Fatal(err)
	}
	store := idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipher)
	firstRuntime := &requiredReplayRestartRuntime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact),
	}
	firstDispatcher := requiredReplayRestartDispatcher(firstRegistry, firstRuntime, store)
	request := requiredReplayV1DispatchRequest(path, "runtime-restart-key")
	request.Query = "tag=original&keep=one"
	first, err := firstDispatcher.Dispatch(t.Context(), request, nil)
	if err != nil || !first.Handled || first.Response.Status != stdhttp.StatusCreated ||
		string(first.Response.Body) != `{"created":true}` || firstRuntime.calls != 2 ||
		first.Response.Headers.Get(idempotency.ReplayedHeader) != "" {
		t.Fatalf("first result=%#v runtime_calls=%d error=%v", first, firstRuntime.calls, err)
	}

	restartedArtifact := artifact
	restartedArtifact.RuntimeInstanceID = "runtime-2"
	restartedRegistry, restartedPath := requiredReplayRestartRegistry(t, restartedArtifact)
	if restartedPath != path {
		t.Fatalf("restart path changed from %q to %q", path, restartedPath)
	}
	restartedRuntime := &requiredReplayRestartRuntime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, restartedArtifact),
	}
	restartedDispatcher := requiredReplayRestartDispatcher(restartedRegistry, restartedRuntime, store)
	replayed, err := restartedDispatcher.Dispatch(t.Context(), request, nil)
	if err != nil || !replayed.Handled || replayed.Response.Status != stdhttp.StatusCreated ||
		string(replayed.Response.Body) != `{"created":true}` ||
		replayed.Response.Headers.Get(idempotency.ReplayedHeader) != "true" {
		t.Fatalf("restart replay=%#v error=%v", replayed, err)
	}
	if firstRuntime.calls != 2 || restartedRuntime.calls != 0 {
		t.Fatalf("replay re-executed a plugin: first=%d restarted=%d", firstRuntime.calls, restartedRuntime.calls)
	}
}

func TestRequiredRouteReplayWrongCipherFailsClosedBeforeAuthorizationAndExecution(t *testing.T) {
	for _, test := range []struct {
		name             string
		extensionID      string
		fixture          func(*testing.T, routes.PluginArtifact) (*routes.Registry, routes.DispatchRequest)
		newRuntime       func(*testing.T, routes.PluginArtifact) (requiredReplayCipherRuntime, func() int)
		wantRuntimeCalls int
		wantReplayGuards int
	}{
		{
			name: "response only", extensionID: "idempotency.cipherresponse",
			fixture: func(t *testing.T, artifact routes.PluginArtifact) (*routes.Registry, routes.DispatchRequest) {
				registry, path := requiredReplayResponseOnlyRegistry(t, artifact)
				return registry, requiredReplayV1DispatchRequest(path, "wrong-cipher-response")
			},
			newRuntime: func(t *testing.T, artifact routes.PluginArtifact) (requiredReplayCipherRuntime, func() int) {
				runtime := newRouteDispatcherV2RuntimeForArtifact(t, artifact)
				runtime.response.Headers.Set("Content-Type", "application/json")
				return runtime, func() int { return runtime.calls }
			},
			wantRuntimeCalls: 1,
			wantReplayGuards: 1,
		},
		{
			name: "mutable request", extensionID: "idempotency.restart",
			fixture: func(t *testing.T, artifact routes.PluginArtifact) (*routes.Registry, routes.DispatchRequest) {
				registry, path := requiredReplayRestartRegistry(t, artifact)
				request := requiredReplayV1DispatchRequest(path, "wrong-cipher-mutable")
				request.Query = "tag=original&keep=one"
				return registry, request
			},
			newRuntime: func(t *testing.T, artifact routes.PluginArtifact) (requiredReplayCipherRuntime, func() int) {
				runtime := &requiredReplayRestartRuntime{
					routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact),
				}
				return runtime, func() int { return runtime.calls }
			},
			wantRuntimeCalls: 2,
			wantReplayGuards: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := routeDispatcherArtifact(test.extensionID, 'c')
			registry, request := test.fixture(t, artifact)
			backend := &requiredReplayCipherAccessBackend{inner: idempotency.NewMemoryBackend()}
			cipherA, err := idempotency.NewRequiredReplayCipher(strings.Repeat("0a", 32))
			if err != nil {
				t.Fatal(err)
			}
			cipherB, err := idempotency.NewRequiredReplayCipher(strings.Repeat("0b", 32))
			if err != nil {
				t.Fatal(err)
			}

			seedRuntime, seedRuntimeCalls := test.newRuntime(t, artifact)
			seedGuard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
			seed := requiredReplayCipherDispatcher(
				registry, seedRuntime, seedGuard,
				idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipherA),
			)
			first, err := seed.Dispatch(t.Context(), request, nil)
			if err != nil || !first.Handled || first.Response.Headers.Get(idempotency.ReplayedHeader) != "" ||
				seedRuntimeCalls() != test.wantRuntimeCalls {
				t.Fatalf("seed=%#v runtime=%d error=%v", first, seedRuntimeCalls(), err)
			}

			backend.reset()
			wrongRuntime, wrongRuntimeCalls := test.newRuntime(t, artifact)
			wrongGuard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
			wrong := requiredReplayCipherDispatcher(
				registry, wrongRuntime, wrongGuard,
				idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipherB),
			)
			result, err := wrong.Dispatch(t.Context(), request, nil)
			if result.Handled || !errors.Is(err, routes.ErrDispatchIdempotencyUnavailable) ||
				!errors.Is(err, idempotency.ErrRequiredReplayCipherInvalid) {
				t.Fatalf("wrong-key result=%#v error=%v", result, err)
			}
			if backend.snapshot() != (requiredReplayCipherBackendCalls{Get: 1}) ||
				wrongGuard.calls != 0 || wrongRuntimeCalls() != 0 {
				t.Fatalf("wrong-key crossed fence: backend=%#v guard=%d runtime=%d",
					backend.snapshot(), wrongGuard.calls, wrongRuntimeCalls())
			}

			backend.reset()
			replayRuntime, replayRuntimeCalls := test.newRuntime(t, artifact)
			replayGuard := &requiredReplayCipherGuard{inner: NewProductionRouteGuardAuthorizer()}
			replayDispatcher := requiredReplayCipherDispatcher(
				registry, replayRuntime, replayGuard,
				idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipherA),
			)
			replayed, err := replayDispatcher.Dispatch(t.Context(), request, nil)
			if err != nil || !replayed.Handled || replayed.Response.Status != first.Response.Status ||
				!reflect.DeepEqual(replayed.Response.Body, first.Response.Body) ||
				replayed.Response.CanonicalPath != first.Response.CanonicalPath ||
				replayed.Response.Headers.Get(idempotency.ReplayedHeader) != "true" {
				t.Fatalf("right-key replay=%#v first=%#v error=%v", replayed, first, err)
			}
			gotHeaders := replayed.Response.Headers.Clone()
			gotHeaders.Del(idempotency.ReplayedHeader)
			if !reflect.DeepEqual(gotHeaders, first.Response.Headers) ||
				backend.snapshot() != (requiredReplayCipherBackendCalls{Get: 1}) ||
				replayRuntimeCalls() != 0 || replayGuard.calls != test.wantReplayGuards {
				t.Fatalf("right-key replay drift: headers=%#v want=%#v backend=%#v guard=%d runtime=%d",
					gotHeaders, first.Response.Headers, backend.snapshot(), replayGuard.calls, replayRuntimeCalls())
			}
		})
	}
}

type requiredReplayCipherAccessBackend struct {
	inner idempotency.Backend
	calls requiredReplayCipherBackendCalls
}

type requiredReplayCipherBackendCalls struct {
	Get            int
	SetNX          int
	Set            int
	Delete         int
	CompareAndSwap int
}

type requiredReplayV1RecordBackend struct {
	raw           []byte
	getCalls      int
	mutationCalls int
}

func (b *requiredReplayV1RecordBackend) Get(context.Context, string) ([]byte, bool, error) {
	b.getCalls++
	return append([]byte(nil), b.raw...), true, nil
}

func (b *requiredReplayV1RecordBackend) SetNX(
	context.Context,
	string,
	[]byte,
	time.Duration,
) (bool, error) {
	b.mutationCalls++
	return false, nil
}

func (b *requiredReplayV1RecordBackend) Set(context.Context, string, []byte, time.Duration) error {
	b.mutationCalls++
	return nil
}

func (b *requiredReplayV1RecordBackend) Delete(context.Context, ...string) error {
	b.mutationCalls++
	return nil
}

func (b *requiredReplayV1RecordBackend) CompareAndSwap(
	context.Context,
	string,
	[]byte,
	[]byte,
	time.Duration,
) (bool, error) {
	b.mutationCalls++
	return false, nil
}

func (b *requiredReplayCipherAccessBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b.calls.Get++
	return b.inner.Get(ctx, key)
}

func (b *requiredReplayCipherAccessBackend) SetNX(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) (bool, error) {
	b.calls.SetNX++
	return b.inner.SetNX(ctx, key, value, ttl)
}

func (b *requiredReplayCipherAccessBackend) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	b.calls.Set++
	return b.inner.Set(ctx, key, value, ttl)
}

func (b *requiredReplayCipherAccessBackend) Delete(ctx context.Context, keys ...string) error {
	b.calls.Delete++
	return b.inner.Delete(ctx, keys...)
}

func (b *requiredReplayCipherAccessBackend) CompareAndSwap(
	ctx context.Context,
	key string,
	expected, replacement []byte,
	ttl time.Duration,
) (bool, error) {
	b.calls.CompareAndSwap++
	return b.inner.CompareAndSwap(ctx, key, expected, replacement, ttl)
}

func (b *requiredReplayCipherAccessBackend) snapshot() requiredReplayCipherBackendCalls {
	if b == nil {
		return requiredReplayCipherBackendCalls{}
	}
	return b.calls
}

func (b *requiredReplayCipherAccessBackend) reset() {
	if b != nil {
		b.calls = requiredReplayCipherBackendCalls{}
	}
}

type requiredReplayCipherGuard struct {
	inner ProductionRouteGuardAuthorizer
	calls int
}

type requiredReplayRestartRuntime struct {
	*routeDispatcherV2Runtime
	calls int
}

func (r *requiredReplayRestartRuntime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.calls++
	switch request.InvocationStage {
	case extensionsruntime.ProtocolV2RouteInvocationStageRequest:
		return extensionsruntime.ProtocolV2RouteResponse{
			RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
				Kind:  extensionsruntime.ProtocolV2RoutePatchReplace,
				Path:  "/query/tag",
				Value: []byte(`["patched",""]`),
			}},
		}, nil
	case extensionsruntime.ProtocolV2RouteInvocationStageHandler:
		return extensionsruntime.ProtocolV2RouteResponse{
			StatusCode:  stdhttp.StatusCreated,
			Headers:     stdhttp.Header{"Content-Type": {"application/json"}},
			Body:        map[string]any{"created": true},
			BodyPresent: true,
		}, nil
	default:
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
}

func (g *requiredReplayCipherGuard) Authorize(
	ctx context.Context,
	plan routes.RouteExecutionPlan,
	step routes.RouteExecutionStep,
	request routes.DispatchRequest,
) error {
	g.calls++
	return g.inner.Authorize(ctx, plan, step, request)
}

func (g *requiredReplayCipherGuard) AuthorizeRoute(
	ctx context.Context,
	plan routes.RouteExecutionPlan,
	stepIndex int,
	step routes.RouteExecutionStep,
	request routes.DispatchRequest,
) (routes.RouteGuardAuthorization, error) {
	g.calls++
	return g.inner.AuthorizeRoute(ctx, plan, stepIndex, step, request)
}

func requiredReplayV1DispatchRequest(path, key string) routes.DispatchRequest {
	return routes.DispatchRequest{
		Method: stdhttp.MethodPost,
		Path:   path,
		Query:  "tag=one",
		Headers: stdhttp.Header{
			idempotency.HeaderName: {key},
			"Content-Type":         {"application/json"},
		},
		Body:     []byte(`{}`),
		ClientIP: "127.0.0.1",
	}
}

func requiredReplayV1CompletedRecord(t *testing.T, fingerprint, body string) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		Schema      string                              `json:"schema"`
		State       string                              `json:"state"`
		Fingerprint string                              `json:"fingerprint"`
		Response    *idempotency.RequiredReplayResponse `json:"response"`
	}{
		Schema:      "sforum.required-route-replay@1",
		State:       "completed",
		Fingerprint: fingerprint,
		Response: &idempotency.RequiredReplayResponse{
			Status:  stdhttp.StatusCreated,
			Headers: stdhttp.Header{"Content-Type": {"application/json"}},
			Body:    []byte(body),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requiredReplayRestartRegistry(t *testing.T, artifact routes.PluginArtifact) (*routes.Registry, string) {
	t.Helper()
	handler := routeDispatcherManifestRoute(
		"idempotency.restart.handler", extensionmanifest.RouteActionAdd,
		"/required-replay-runtime-restart", stdhttp.MethodPost,
	)
	handler.RequestSchema = handler.ID + ".request@1"
	before := routeDispatcherManifestRoute(
		"idempotency.restart.before", extensionmanifest.RouteActionBefore,
		handler.Path, stdhttp.MethodPost,
	)
	before.TargetID = handler.ID
	before.MutableRequestFields = []string{"/query/tag"}
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler, before},
	}}}); err != nil {
		t.Fatal(err)
	}
	return registry, handler.Path
}

func requiredReplayResponseOnlyRegistry(t *testing.T, artifact routes.PluginArtifact) (*routes.Registry, string) {
	t.Helper()
	handler := routeDispatcherManifestRoute(
		"idempotency.cipherresponse.handler", extensionmanifest.RouteActionAdd,
		"/required-replay-wrong-cipher-response", stdhttp.MethodPost,
	)
	handler.RequestSchema = handler.ID + ".request@1"
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler},
	}}}); err != nil {
		t.Fatal(err)
	}
	return registry, handler.Path
}

type requiredReplayCipherRuntime interface {
	ExactRouteRuntime
	exactRouteV2Runtime
}

func requiredReplayCipherDispatcher(
	registry *routes.Registry,
	runtime requiredReplayCipherRuntime,
	guard routes.GuardAuthorizer,
	store *idempotency.Store,
) *routes.Dispatcher {
	return routes.NewDispatcher(routes.DispatcherConfig{
		Plans:       routeRegistryPlanResolver{registry: registry},
		Steps:       NewBufferedRouteStepInvoker(runtime),
		Guard:       guard,
		Schemas:     CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
		Policies:    requiredRoutePolicyResolver{},
		Idempotency: NewRequiredRouteIdempotency(store),
	})
}

func requiredReplayRestartDispatcher(
	registry *routes.Registry,
	runtime *requiredReplayRestartRuntime,
	store *idempotency.Store,
) *routes.Dispatcher {
	return requiredReplayCipherDispatcher(registry, runtime, NewProductionRouteGuardAuthorizer(), store)
}
