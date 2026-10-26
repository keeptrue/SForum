package http

import (
	"bytes"
	"context"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

const requiredReplayTestPath = "/api/v1/idempotent-route"

func TestRequiredRouteReplayCanonicalizesRequestsAndRejectsConflicts(t *testing.T) {
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{})

	first := requiredReplayRequest(t, app, requiredReplayRequestInput{
		KeyValues: []string{"request-42"}, Query: "b=2&a=1&a=0",
		ContentType: "Application/JSON; Charset=UTF-8", Body: `{"name":"first"}`,
	})
	if first.StatusCode != stdhttp.StatusCreated || first.Header.Get(idempotency.ReplayedHeader) != "" {
		t.Fatalf("first status=%d replayed=%q", first.StatusCode, first.Header.Get(idempotency.ReplayedHeader))
	}
	first.Body.Close()

	replayed := requiredReplayRequest(t, app, requiredReplayRequestInput{
		KeyValues: []string{"request-42"}, Query: "a=1&a=0&b=2",
		ContentType: "Application/JSON; Charset=UTF-8", Body: `{"name":"first"}`,
	})
	if replayed.StatusCode != stdhttp.StatusCreated || replayed.Header.Get(idempotency.ReplayedHeader) != "true" || calls.Load() != 1 {
		t.Fatalf("replay status=%d replayed=%q calls=%d", replayed.StatusCode, replayed.Header.Get(idempotency.ReplayedHeader), calls.Load())
	}
	replayed.Body.Close()

	conflicts := []requiredReplayRequestInput{
		{KeyValues: []string{"request-42"}, Query: "a=0&a=1&b=2", ContentType: "Application/JSON; Charset=UTF-8", Body: `{"name":"first"}`},
		{KeyValues: []string{"request-42"}, Query: "a=1&a=0&b=3", ContentType: "Application/JSON; Charset=UTF-8", Body: `{"name":"first"}`},
		{KeyValues: []string{"request-42"}, Query: "a=1&a=0&b=2", ContentType: "Application/JSON; Charset=UTF-8", Body: `{"name":"changed"}`},
		{KeyValues: []string{"request-42"}, Query: "a=1&a=0&b=2", ContentType: "application/merge-patch+json", Body: `{"name":"first"}`},
	}
	for index, input := range conflicts {
		response := requiredReplayRequest(t, app, input)
		if response.StatusCode != stdhttp.StatusConflict {
			t.Fatalf("conflict[%d] status=%d", index, response.StatusCode)
		}
		response.Body.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("conflicts invoked plugin %d times", calls.Load())
	}
}

func TestRequiredRouteReplayRejectsMissingInvalidAndMultipleKeys(t *testing.T) {
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{})
	tests := []struct {
		name string
		keys []string
	}{
		{name: "missing"},
		{name: "invalid", keys: []string{"contains space"}},
		{name: "multiple", keys: []string{"one", "two"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requiredReplayRequest(t, app, requiredReplayRequestInput{
				KeyValues: test.keys, ContentType: "application/json", Body: `{}`,
			})
			if response.StatusCode != stdhttp.StatusBadRequest {
				t.Fatalf("status=%d", response.StatusCode)
			}
			response.Body.Close()
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid keys invoked plugin %d times", calls.Load())
	}
}

func TestRequiredRouteReplaySeparatesCredentialAndAnonymousScopesWithoutPersistingSecrets(t *testing.T) {
	backend := &recordingRequiredReplayBackend{inner: idempotency.NewMemoryBackend()}
	actorLoader := func(c fiber.Ctx) (identity.Actor, error) {
		switch c.Get("X-Test-Credential") {
		case "cookie":
			return identity.Actor{ID: 7, Status: identity.UserStatusActive}, nil
		case "bearer":
			c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: 7, TokenID: 91}))
			return identity.Actor{ID: 7, Status: identity.UserStatusActive}, nil
		default:
			return identity.Actor{}, nil
		}
	}
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
		backend: backend, actorLoader: actorLoader,
		configure: func(cfg *config.Config) {
			// Fiber's in-memory test connection uses 0.0.0.0. Trust only that
			// synthetic peer so X-Forwarded-For can model two anonymous clients.
			cfg.TrustProxy = true
			cfg.TrustedProxies = []string{"0.0.0.0"}
		},
	})
	scopes := []requiredReplayRequestInput{
		{KeyValues: []string{"shared-key"}, ContentType: "application/json", Body: `{}`, Credential: "cookie", Cookie: "sforum_session=cookie-secret"},
		{KeyValues: []string{"shared-key"}, ContentType: "application/json", Body: `{}`, Credential: "bearer", Authorization: "Bearer bearer-secret"},
		{KeyValues: []string{"shared-key"}, ContentType: "application/json", Body: `{}`, ForwardedFor: "203.0.113.10"},
		{KeyValues: []string{"shared-key"}, ContentType: "application/json", Body: `{}`, ForwardedFor: "203.0.113.11"},
	}
	for pass := range 2 {
		for index, input := range scopes {
			response := requiredReplayRequest(t, app, input)
			if response.StatusCode != stdhttp.StatusCreated {
				t.Fatalf("pass=%d scope=%d status=%d", pass, index, response.StatusCode)
			}
			wantReplay := ""
			if pass == 1 {
				wantReplay = "true"
			}
			if response.Header.Get(idempotency.ReplayedHeader) != wantReplay {
				t.Fatalf("pass=%d scope=%d replayed=%q", pass, index, response.Header.Get(idempotency.ReplayedHeader))
			}
			response.Body.Close()
		}
	}
	if calls.Load() != int64(len(scopes)) {
		t.Fatalf("plugin calls=%d", calls.Load())
	}
	stored := backend.observedText()
	for _, secret := range []string{"cookie-secret", "bearer-secret", "Authorization", "Cookie"} {
		if strings.Contains(stored, secret) {
			t.Fatalf("required replay storage contains %q", secret)
		}
	}
}

func TestRequiredRouteReplayReauthorizesBeforeReturningStoredResponse(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	actorLoader := func(fiber.Ctx) (identity.Actor, error) {
		permissions := map[string]bool{}
		if allowed.Load() {
			permissions["route.replay.execute"] = true
		}
		return identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: permissions}, nil
	}
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
		actorLoader: actorLoader, guard: extensionmanifest.GuardCorePermission,
		permission: "route.replay.execute",
	})
	input := requiredReplayRequestInput{KeyValues: []string{"permission-key"}, ContentType: "application/json", Body: `{}`}
	first := requiredReplayRequest(t, app, input)
	if first.StatusCode != stdhttp.StatusCreated {
		t.Fatalf("first status=%d", first.StatusCode)
	}
	first.Body.Close()
	allowed.Store(false)
	replay := requiredReplayRequest(t, app, input)
	if replay.StatusCode != stdhttp.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("revoked replay status=%d calls=%d", replay.StatusCode, calls.Load())
	}
	replay.Body.Close()
}

func TestRequiredRouteReplayFailsClosedForInProgressAndUnavailableStorage(t *testing.T) {
	t.Run("in progress", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
			handler: func(writer stdhttp.ResponseWriter, _ *stdhttp.Request, call int64) {
				if call == 1 {
					close(started)
					<-release
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(stdhttp.StatusCreated)
			},
		})
		input := requiredReplayRequestInput{KeyValues: []string{"pending-key"}, ContentType: "application/json", Body: `{}`}
		firstResult := make(chan *stdhttp.Response, 1)
		firstError := make(chan error, 1)
		go func() {
			response, err := app.Test(newRequiredReplayHTTPRequest(input))
			firstResult <- response
			firstError <- err
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("first plugin request did not start")
		}
		second := requiredReplayRequest(t, app, input)
		if second.StatusCode != stdhttp.StatusConflict || calls.Load() != 1 {
			t.Fatalf("second status=%d calls=%d", second.StatusCode, calls.Load())
		}
		second.Body.Close()
		close(release)
		if err := <-firstError; err != nil {
			t.Fatal(err)
		}
		first := <-firstResult
		if first.StatusCode != stdhttp.StatusCreated {
			t.Fatalf("first status=%d", first.StatusCode)
		}
		first.Body.Close()
	})

	t.Run("unavailable", func(t *testing.T) {
		app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
			backend: idempotency.NewRedisBackend(nil),
		})
		response := requiredReplayRequest(t, app, requiredReplayRequestInput{
			KeyValues: []string{"redis-key"}, ContentType: "application/json", Body: `{}`,
		})
		if response.StatusCode != stdhttp.StatusServiceUnavailable || calls.Load() != 0 {
			t.Fatalf("status=%d calls=%d", response.StatusCode, calls.Load())
		}
		response.Body.Close()
	})
}

func TestRequiredRouteReplayPreservesPendingWhenResponseCannotBeStored(t *testing.T) {
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
		handler: func(writer stdhttp.ResponseWriter, _ *stdhttp.Request, _ int64) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("X-Oversized", strings.Repeat("x", idempotency.MaxRequiredReplayHeaders+1))
			writer.WriteHeader(stdhttp.StatusCreated)
		},
	})
	input := requiredReplayRequestInput{KeyValues: []string{"oversized-response"}, ContentType: "application/json", Body: `{}`}
	first := requiredReplayRequest(t, app, input)
	if first.StatusCode != stdhttp.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("first status=%d calls=%d", first.StatusCode, calls.Load())
	}
	first.Body.Close()
	second := requiredReplayRequest(t, app, input)
	if second.StatusCode != stdhttp.StatusConflict || calls.Load() != 1 {
		t.Fatalf("second status=%d calls=%d", second.StatusCode, calls.Load())
	}
	second.Body.Close()
}

func TestUnsafePluginRouteUsesHostWriteLimiter(t *testing.T) {
	app, calls := newRequiredReplayRouteApp(t, requiredReplayRouteOptions{
		configure: func(cfg *config.Config) {
			cfg.LimiterWriteMax = 1
			cfg.LimiterWindow = time.Minute
		},
	})
	for index, want := range []int{stdhttp.StatusCreated, stdhttp.StatusTooManyRequests} {
		response := requiredReplayRequest(t, app, requiredReplayRequestInput{
			KeyValues: []string{"limit-key-" + string(rune('a'+index))}, ContentType: "application/json", Body: `{}`,
		})
		if response.StatusCode != want {
			t.Fatalf("request=%d status=%d want=%d", index, response.StatusCode, want)
		}
		response.Body.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("limited route plugin calls=%d", calls.Load())
	}
}

type requiredReplayRouteOptions struct {
	backend     idempotency.Backend
	actorLoader RouteActorLoader
	guard       string
	permission  string
	configure   func(*config.Config)
	handler     func(stdhttp.ResponseWriter, *stdhttp.Request, int64)
}

func newRequiredReplayRouteApp(t *testing.T, options requiredReplayRouteOptions) (*fiber.App, *atomic.Int64) {
	t.Helper()
	if options.backend == nil {
		options.backend = idempotency.NewMemoryBackend()
	}
	if options.guard == "" {
		options.guard = extensionmanifest.GuardCorePublic
	}
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("idempotency.route", '1')
	route := routeDispatcherManifestRoute("idempotency.route.create", extensionmanifest.RouteActionAdd, requiredReplayTestPath, stdhttp.MethodPost)
	route.Guard = options.guard
	route.Permission = options.permission
	route.RequestSchema = route.ID + ".request@1"
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime, target := newRouteDispatcherRuntime(t, artifact)
	calls := &atomic.Int64{}
	target.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
		call := calls.Add(1)
		if options.handler != nil {
			options.handler(writer, request, call)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(idempotency.ReplayedHeader, "forged")
		writer.WriteHeader(stdhttp.StatusCreated)
		_, _ = writer.Write([]byte(`{"created":true}`))
	})
	cipher, err := idempotency.NewRequiredReplayCipher(strings.Repeat("0d", 32))
	if err != nil {
		t.Fatal(err)
	}
	store := idempotency.NewStore(options.backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipher)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
		Policies: requiredRoutePolicyResolver{}, Idempotency: NewRequiredRouteIdempotency(store),
	})
	cfg := routeDispatcherConfig()
	if options.configure != nil {
		options.configure(&cfg)
	}
	return NewApp(cfg, slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteActors: options.actorLoader,
	}), calls
}

type requiredRoutePolicyResolver struct{}

func (requiredRoutePolicyResolver) ResolveRouteExecutionPolicy(routes.RouteExecutionStep) (routes.RouteExecutionPolicy, error) {
	return routes.RouteExecutionPolicy{
		RateLimit: "host.ip_write@1", Idempotency: idempotency.RequiredReplayPolicy, IdempotencyRequired: true,
	}, nil
}

type requiredReplayRequestInput struct {
	KeyValues     []string
	Query         string
	ContentType   string
	Body          string
	Credential    string
	Cookie        string
	Authorization string
	ForwardedFor  string
}

func requiredReplayRequest(t *testing.T, app *fiber.App, input requiredReplayRequestInput) *stdhttp.Response {
	t.Helper()
	response, err := app.Test(newRequiredReplayHTTPRequest(input))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newRequiredReplayHTTPRequest(input requiredReplayRequestInput) *stdhttp.Request {
	target := requiredReplayTestPath
	if input.Query != "" {
		target += "?" + input.Query
	}
	request := httptest.NewRequest(stdhttp.MethodPost, target, bytes.NewBufferString(input.Body))
	for _, key := range input.KeyValues {
		request.Header.Add(idempotency.HeaderName, key)
	}
	if input.ContentType != "" {
		request.Header.Set("Content-Type", input.ContentType)
	}
	if input.Credential != "" {
		request.Header.Set("X-Test-Credential", input.Credential)
	}
	if input.Cookie != "" {
		request.Header.Set("Cookie", input.Cookie)
	}
	if input.Authorization != "" {
		request.Header.Set("Authorization", input.Authorization)
	}
	if input.ForwardedFor != "" {
		request.Header.Set("X-Forwarded-For", input.ForwardedFor)
	}
	return request
}

type recordingRequiredReplayBackend struct {
	inner idempotency.Backend
	mu    sync.Mutex
	seen  [][]byte
}

func (b *recordingRequiredReplayBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, found, err := b.inner.Get(ctx, key)
	if found {
		b.record(key, value)
	}
	return value, found, err
}

func (b *recordingRequiredReplayBackend) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	b.record(key, value)
	return b.inner.SetNX(ctx, key, value, ttl)
}

func (b *recordingRequiredReplayBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	b.record(key, value)
	return b.inner.Set(ctx, key, value, ttl)
}

func (b *recordingRequiredReplayBackend) Delete(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		b.record(key, nil)
	}
	return b.inner.Delete(ctx, keys...)
}

func (b *recordingRequiredReplayBackend) CompareAndSwap(
	ctx context.Context,
	key string,
	expected []byte,
	replacement []byte,
	ttl time.Duration,
) (bool, error) {
	b.record(key, expected)
	b.record(key, replacement)
	return b.inner.CompareAndSwap(ctx, key, expected, replacement, ttl)
}

func (b *recordingRequiredReplayBackend) record(key string, value []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, append([]byte(key+"\n"), value...))
}

func (b *recordingRequiredReplayBackend) observedText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var output strings.Builder
	for _, value := range b.seen {
		_, _ = output.Write(value)
	}
	return output.String()
}
