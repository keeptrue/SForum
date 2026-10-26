package hostapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Fixtures: an in-memory Backend plus one real gRPC server per runtime binding.
// The store's real Redis backend and the real 4096-key ceiling are covered by
// the opt-in tests in app/Support/PluginSessionState; these tests cover the S9
// delegation and the service boundary, which is where the authority lives.
// ---------------------------------------------------------------------------

type sessionTestBackend struct {
	mu     sync.Mutex
	values map[string]sessionTestValue
	sets   map[string]map[string]bool
	now    func() time.Time
	fail   error
}

type sessionTestValue struct {
	value     []byte
	expiresAt time.Time
}

func newSessionTestBackend() *sessionTestBackend {
	return &sessionTestBackend{
		values: map[string]sessionTestValue{}, sets: map[string]map[string]bool{}, now: time.Now,
	}
}

func (b *sessionTestBackend) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, false, b.fail
	}
	item, ok := b.values[key]
	if !ok || (!item.expiresAt.IsZero() && !b.now().Before(item.expiresAt)) {
		return nil, false, nil
	}
	return append([]byte(nil), item.value...), true, nil
}

func (b *sessionTestBackend) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.values[key] = sessionTestValue{value: append([]byte(nil), value...), expiresAt: b.now().Add(ttl)}
	return nil
}

func (b *sessionTestBackend) Delete(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return false, b.fail
	}
	_, ok := b.values[key]
	delete(b.values, key)
	delete(b.sets, key)
	return ok, nil
}

func (b *sessionTestBackend) SetAdd(_ context.Context, key string, member string, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return false, b.fail
	}
	if b.sets[key] == nil {
		b.sets[key] = map[string]bool{}
	}
	added := !b.sets[key][member]
	b.sets[key][member] = true
	_ = ttl
	return added, nil
}

func (b *sessionTestBackend) SetMembers(_ context.Context, key string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	members := make([]string, 0, len(b.sets[key]))
	for member := range b.sets[key] {
		members = append(members, member)
	}
	return members, nil
}

func (b *sessionTestBackend) SetRemove(_ context.Context, key string, members ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	for _, member := range members {
		delete(b.sets[key], member)
	}
	return nil
}

// sessionTestAdmission records the exact runtime it admitted.
type sessionTestAdmission struct {
	mu       sync.Mutex
	admitted []ServiceProviderIdentity
	err      error
}

func (a *sessionTestAdmission) AcquireServiceProvider(
	ctx context.Context,
	identity ServiceProviderIdentity,
) (ServiceProviderAdmissionLease, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.mu.Lock()
	a.admitted = append(a.admitted, identity)
	a.mu.Unlock()
	return sessionTestLease{ctx: ctx}, nil
}

type sessionTestLease struct{ ctx context.Context }

func (l sessionTestLease) Context() context.Context { return l.ctx }
func (l sessionTestLease) Release()                 {}

const sessionTestInstallationID = "installation-session-test"

type sessionTestFixture struct {
	store     *pluginsessionstate.Store
	authority *ProtocolV2SessionDelegationAuthority
	admission *sessionTestAdmission
	backend   *sessionTestBackend
	server    *ProtocolV2SessionServiceServer
	client    hostv2.PluginSessionServiceClient
	identity  *protocolv2.ExtensionIdentity
	clock     *time.Time
}

func sessionTestRuntimeIdentity(extensionID, instanceID string) *protocolv2.ExtensionIdentity {
	return &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("a", 64), TrustGrantId: "grant-" + extensionID,
		RuntimeEpoch: 7, InstanceId: instanceID,
	}
}

func newSessionTestFixture(
	t *testing.T,
	identity *protocolv2.ExtensionIdentity,
	options ...ProtocolV2SessionServiceOption,
) *sessionTestFixture {
	t.Helper()
	backend := newSessionTestBackend()
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	store.WithClock(func() time.Time { return clock })
	key := []byte("0123456789abcdef0123456789abcdef")
	authority, err := newProtocolV2SessionDelegationAuthority(key, func() time.Time { return clock }, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	admission := &sessionTestAdmission{}
	all := append([]ProtocolV2SessionServiceOption{
		WithProtocolV2SessionAdmission(admission),
	}, options...)
	server, err := NewProtocolV2SessionServiceServer(store, authority, sessionTestInstallationID, all...)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(
		ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		// Mirror the production broker interceptor: the attested identity comes
		// from the per-runtime binding, never from the request.
		return handler(ContextWithProtocolV2RuntimeIdentity(ctx, identity), request)
	}))
	hostv2.RegisterPluginSessionServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient(
		"passthrough:///plugin-session-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return &sessionTestFixture{
		store: store, authority: authority, admission: admission, backend: backend, server: server,
		client: hostv2.NewPluginSessionServiceClient(connection), identity: identity,
		clock: &clock,
	}
}

// mint mints one delegation for the fixture's own runtime.
func (f *sessionTestFixture) mint(t *testing.T, scope ProtocolV2SessionScope) string {
	t.Helper()
	token, err := f.authority.MintProtocolV2SessionDelegation(context.Background(), f.identity, scope)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func sessionTestScope(fingerprint string, now time.Time) ProtocolV2SessionScope {
	return ProtocolV2SessionScope{
		SessionFingerprint: fingerprint,
		IdleExpiresAt:      now.Add(30 * 24 * time.Hour),
		AbsoluteExpiresAt:  now.Add(180 * 24 * time.Hour),
		ActorUserID:        42,
	}
}

func (f *sessionTestFixture) requestContext(token string) *protocolv2.RequestContext {
	return &protocolv2.RequestContext{
		RequestId: "request-1", Extension: f.identity,
		Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		HostSessionDelegation: &protocolv2.HostSessionDelegation{
			Token: token,
		},
	}
}

// ---------------------------------------------------------------------------
// The S9 authority: mint / resolve / retire, and what a token may reveal.
// ---------------------------------------------------------------------------

func TestProtocolV2SessionDelegationRoundTripAndRetire(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	scope := sessionTestScope(strings.Repeat("b", 64), *fixture.clock)
	token := fixture.mint(t, scope)

	resolved, err := fixture.authority.Resolve(token, fixture.identity)
	if err != nil {
		t.Fatalf("resolve minted delegation: %v", err)
	}
	if resolved.SessionFingerprint != scope.SessionFingerprint || resolved.ActorUserID != scope.ActorUserID ||
		!resolved.IdleExpiresAt.Equal(scope.IdleExpiresAt) || !resolved.AbsoluteExpiresAt.Equal(scope.AbsoluteExpiresAt) {
		t.Fatalf("resolved scope drifted: %#v", resolved)
	}
	// One invocation may perform several operations, so resolving twice is
	// allowed while the invocation is in flight.
	if _, err := fixture.authority.Resolve(token, fixture.identity); err != nil {
		t.Fatalf("second resolve inside one invocation: %v", err)
	}

	if err := fixture.authority.RetireProtocolV2SessionDelegation(token); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.authority.Resolve(token, fixture.identity); !errors.Is(err, ErrProtocolV2SessionDelegationRetired) {
		t.Fatalf("reuse after retire = %v, want ErrProtocolV2SessionDelegationRetired", err)
	}
	// Retire is idempotent: every transport defers it.
	if err := fixture.authority.RetireProtocolV2SessionDelegation(token); err != nil {
		t.Fatalf("second retire: %v", err)
	}
	if err := fixture.authority.RetireProtocolV2SessionDelegation("not-a-token"); err != nil {
		t.Fatalf("retiring an unknown token: %v", err)
	}
}

func TestProtocolV2SessionDelegationTokenCarriesNoSessionIdentifier(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	fingerprint := strings.Repeat("c", 64)
	token := fixture.mint(t, sessionTestScope(fingerprint, *fixture.clock))

	claims := decodeSessionTestJWT(t, token)
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, forbidden := range []string{fingerprint, "session_fingerprint", "session_id", "sid", "idle_expires", "absolute_expires"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("session delegation token leaks %q: %s", forbidden, payload)
		}
	}
	// The token still names the exact runtime it was minted for.
	if claims["extension_id"] != "acme.session" || claims["instance_id"] != "instance-a" {
		t.Fatalf("token is not bound to the runtime: %v", claims)
	}
}

func TestProtocolV2SessionDelegationFailsClosedOnTamperExpiryAndBindingDrift(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	scope := sessionTestScope(strings.Repeat("d", 64), *fixture.clock)
	token := fixture.mint(t, scope)

	otherArtifact := sessionTestRuntimeIdentity("acme.session", "instance-a")
	otherArtifact.ArtifactDigest = strings.Repeat("e", 64)
	crossRuntime := sessionTestRuntimeIdentity("acme.session", "instance-b")
	crossExtension := sessionTestRuntimeIdentity("acme.other", "instance-a")
	noGrant := sessionTestRuntimeIdentity("acme.session", "instance-a")
	noGrant.TrustGrantId = ""
	otherEpoch := sessionTestRuntimeIdentity("acme.session", "instance-a")
	otherEpoch.RuntimeEpoch = 8

	unsigned := strings.Split(token, ".")
	if len(unsigned) != 3 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	tampered := unsigned[0] + "." + unsigned[1] + "." + strings.Repeat("A", len(unsigned[2]))

	tests := []struct {
		name    string
		token   string
		runtime *protocolv2.ExtensionIdentity
	}{
		{"tampered signature", tampered, fixture.identity},
		{"unknown token", "not-a-jwt", fixture.identity},
		{"cross runtime instance", token, crossRuntime},
		{"cross extension", token, crossExtension},
		{"artifact digest drift", token, otherArtifact},
		{"runtime epoch drift", token, otherEpoch},
		{"missing trust grant", token, noGrant},
		{"nil runtime", token, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fixture.authority.Resolve(test.token, test.runtime); err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}

	// Expiry: the authority clock is the authority, not the token's own claim.
	*fixture.clock = fixture.clock.Add(3 * time.Minute)
	if _, err := fixture.authority.Resolve(token, fixture.identity); err == nil {
		t.Fatal("expired delegation was accepted")
	}
}

// ---------------------------------------------------------------------------
// The S7/S9 service boundary over a real gRPC transport.
// ---------------------------------------------------------------------------

func TestProtocolV2SessionServiceAllowedRoundTrip(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	token := fixture.mint(t, sessionTestScope(strings.Repeat("f", 64), *fixture.clock))
	requestContext := fixture.requestContext(token)

	set, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: "workflow/step-1", Value: []byte("payload"), TtlMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.GetError() != nil || set.GetEffectiveTtlMs() == 0 {
		t.Fatalf("set failed: %#v", set)
	}
	get, err := fixture.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: requestContext, NamespaceKey: "workflow/step-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if get.GetError() != nil || !get.GetFound() || string(get.GetValue()) != "payload" {
		t.Fatalf("get failed: %#v", get)
	}
	miss, err := fixture.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: requestContext, NamespaceKey: "workflow/absent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if miss.GetError() != nil || miss.GetFound() {
		t.Fatalf("absent key was not a clean miss: %#v", miss)
	}
	deleted, err := fixture.client.Delete(context.Background(), &hostv2.PluginSessionDeleteRequest{
		Context: requestContext, NamespaceKey: "workflow/step-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.GetError() != nil || !deleted.GetDeleted() {
		t.Fatalf("delete failed: %#v", deleted)
	}
	afterDelete, err := fixture.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: requestContext, NamespaceKey: "workflow/step-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.GetFound() {
		t.Fatal("deleted key was still readable")
	}
	if len(fixture.admission.admitted) == 0 {
		t.Fatal("the exact runtime was never admitted")
	}
	for _, admitted := range fixture.admission.admitted {
		if admitted.ExtensionID != "acme.session" || admitted.InstanceID != "instance-a" {
			t.Fatalf("admitted a different runtime: %#v", admitted)
		}
	}
}

func TestProtocolV2SessionServiceDeniesWithoutValidDelegation(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	valid := fixture.mint(t, sessionTestScope(strings.Repeat("1", 64), *fixture.clock))
	retired := fixture.mint(t, sessionTestScope(strings.Repeat("2", 64), *fixture.clock))
	if err := fixture.authority.RetireProtocolV2SessionDelegation(retired); err != nil {
		t.Fatal(err)
	}
	foreign := &protocolv2.RequestContext{
		RequestId: "request-1",
		Extension: sessionTestRuntimeIdentity("acme.other", "instance-a"),
		Deadline:  timestamppb.New(time.Now().Add(time.Minute)),
		HostSessionDelegation: &protocolv2.HostSessionDelegation{
			Token: valid,
		},
	}
	withActor := fixture.requestContext(valid)
	withActor.Actor = &protocolv2.Actor{UserId: 7}

	tests := []struct {
		name    string
		request *protocolv2.RequestContext
	}{
		{"no delegation", &protocolv2.RequestContext{
			RequestId: "request-1", Extension: fixture.identity,
			Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		}},
		{"empty token", fixture.requestContext("")},
		{"retired token", fixture.requestContext(retired)},
		{"unknown token", fixture.requestContext("not-a-jwt")},
		{"extension identity mismatch", foreign},
		{"caller supplied actor", withActor},
		{"nil context", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
				Context: test.request, NamespaceKey: "workflow/step", Value: []byte("x"), TtlMs: 1000,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.GetError().GetReason() != "host.plugin_session_scope_unattested" {
				t.Fatalf("%s was not denied: %#v", test.name, response.GetError())
			}
			// Nothing was written.
			members, err := fixture.backend.SetMembers(context.Background(), "")
			if err != nil || len(members) != 0 {
				t.Fatalf("denied call wrote state: %v %v", members, err)
			}
		})
	}
}

func TestProtocolV2SessionServiceIsolatesExtensionsAndSessions(t *testing.T) {
	first := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.first", "instance-a"))
	second := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.second", "instance-b"))
	sessionOne := strings.Repeat("3", 64)
	sessionTwo := strings.Repeat("4", 64)

	// Cross-plugin: the same session fingerprint under a different extension is
	// a different namespace even when both runtimes are live.
	firstToken := first.mint(t, sessionTestScope(sessionOne, *first.clock))
	if _, err := first.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: first.requestContext(firstToken), NamespaceKey: "shared-key", Value: []byte("first"), TtlMs: 60_000,
	}); err != nil {
		t.Fatal(err)
	}
	secondToken := second.mint(t, sessionTestScope(sessionOne, *second.clock))
	crossPlugin, err := second.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: second.requestContext(secondToken), NamespaceKey: "shared-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if crossPlugin.GetFound() {
		t.Fatal("plugin B read plugin A's session state")
	}

	// Cross-session: a second session of the same plugin cannot see the first.
	otherSession := first.mint(t, sessionTestScope(sessionTwo, *first.clock))
	crossSession, err := first.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: first.requestContext(otherSession), NamespaceKey: "shared-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if crossSession.GetFound() {
		t.Fatal("a second session read the first session's state")
	}
}

func TestProtocolV2SessionServiceSafeModeMissesReadsAndRefusesWrites(t *testing.T) {
	identity := sessionTestRuntimeIdentity("acme.session", "instance-a")
	live := newSessionTestFixture(t, identity)
	token := live.mint(t, sessionTestScope(strings.Repeat("5", 64), *live.clock))
	if _, err := live.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: live.requestContext(token), NamespaceKey: "k", Value: []byte("v"), TtlMs: 60_000,
	}); err != nil {
		t.Fatal(err)
	}

	safe := newSessionTestFixture(t, identity, WithProtocolV2SessionSafeMode(true))
	safeToken := safe.mint(t, sessionTestScope(strings.Repeat("5", 64), *safe.clock))
	read, err := safe.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: safe.requestContext(safeToken), NamespaceKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if read.GetFound() || read.GetError() != nil {
		t.Fatalf("safe mode read was not a silent miss: %#v", read)
	}
	write, err := safe.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: safe.requestContext(safeToken), NamespaceKey: "k", Value: []byte("v2"), TtlMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if write.GetError().GetReason() != "host.plugin_session_safe_mode" {
		t.Fatalf("safe mode write was not refused: %#v", write.GetError())
	}
	remove, err := safe.client.Delete(context.Background(), &hostv2.PluginSessionDeleteRequest{
		Context: safe.requestContext(safeToken), NamespaceKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if remove.GetError().GetReason() != "host.plugin_session_safe_mode" {
		t.Fatalf("safe mode delete was not refused: %#v", remove.GetError())
	}
}

func TestProtocolV2SessionServiceEnforcesCapsAndTTLBounds(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	now := *fixture.clock
	token := fixture.mint(t, sessionTestScope(strings.Repeat("6", 64), now))
	requestContext := fixture.requestContext(token)

	overValue, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: "big",
		Value: make([]byte, pluginsessionstate.ValueMaxBytes+1), TtlMs: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overValue.GetError().GetReason() != "host.plugin_session_value_too_large" {
		t.Fatalf("oversized value: %#v", overValue.GetError())
	}
	overKey, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: strings.Repeat("k", pluginsessionstate.KeyMaxBytes+1),
		Value: []byte("v"), TtlMs: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overKey.GetError().GetReason() != "host.plugin_session_key_too_long" {
		t.Fatalf("oversized key: %#v", overKey.GetError())
	}
	overTTL, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: "ttl", Value: []byte("v"),
		TtlMs: uint32((25 * time.Hour).Milliseconds()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if overTTL.GetError().GetReason() != "host.plugin_session_ttl_out_of_range" {
		t.Fatalf("out-of-range ttl: %#v", overTTL.GetError())
	}
	// A request that outlives the session is tightened, never widened.
	tight := fixture.mint(t, ProtocolV2SessionScope{
		SessionFingerprint: strings.Repeat("7", 64),
		IdleExpiresAt:      now.Add(90 * time.Second),
		AbsoluteExpiresAt:  now.Add(2 * time.Minute),
	})
	applied, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: fixture.requestContext(tight), NamespaceKey: "ttl", Value: []byte("v"),
		TtlMs: uint32((12 * time.Hour).Milliseconds()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.GetError() != nil {
		t.Fatalf("tightened ttl was rejected: %#v", applied.GetError())
	}
	if applied.GetEffectiveTtlMs() > uint32((90 * time.Second).Milliseconds()) {
		t.Fatalf("effective ttl %dms extended past the session's idle bound", applied.GetEffectiveTtlMs())
	}

	// The per-session ceiling: new keys stop at MaxKeysPerSession, and updating
	// an existing key still works at the ceiling.
	for index := 0; index < pluginsessionstate.MaxKeysPerSession; index++ {
		response, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
			Context: requestContext, NamespaceKey: "cap/" + string(rune('a'+index%26)) + string(rune('a'+index/26)),
			Value: []byte("v"), TtlMs: 60_000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.GetError() != nil {
			t.Fatalf("write %d below the ceiling failed: %#v", index, response.GetError())
		}
	}
	over, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: "cap/overflow", Value: []byte("v"), TtlMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if over.GetError().GetReason() != "host.plugin_session_key_limit" {
		t.Fatalf("per-session ceiling: %#v", over.GetError())
	}
	update, err := fixture.client.Set(context.Background(), &hostv2.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: "cap/aa", Value: []byte("v2"), TtlMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if update.GetError() != nil {
		t.Fatalf("updating an existing key at the ceiling was refused: %#v", update.GetError())
	}
}

func TestProtocolV2SessionServiceDeniesStaleRuntimeAndHidesBackendErrors(t *testing.T) {
	fixture := newSessionTestFixture(t, sessionTestRuntimeIdentity("acme.session", "instance-a"))
	token := fixture.mint(t, sessionTestScope(strings.Repeat("8", 64), *fixture.clock))

	fixture.admission.err = errors.Join(ErrServiceProviderStale, errors.New("runtime instance not active"))
	stale, err := fixture.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: fixture.requestContext(token), NamespaceKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GetError().GetReason() != "host.plugin_session_runtime_stale" {
		t.Fatalf("stale runtime: %#v", stale.GetError())
	}
	if strings.Contains(stale.GetError().GetMessage(), "not active") {
		t.Fatalf("admission detail leaked: %q", stale.GetError().GetMessage())
	}

	fixture.admission.err = nil
	fixture.backend.fail = errors.New("redis: dial tcp 127.0.0.1:16379: connect: connection refused")
	broken, err := fixture.client.Get(context.Background(), &hostv2.PluginSessionGetRequest{
		Context: fixture.requestContext(token), NamespaceKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := broken.GetError()
	if detail.GetReason() != "host.plugin_session_unavailable" || !detail.GetRetryable() {
		t.Fatalf("backend failure: %#v", detail)
	}
	for _, leak := range []string{"redis", "127.0.0.1", "dial", "connection refused"} {
		if strings.Contains(detail.GetMessage(), leak) || strings.Contains(detail.GetReason(), leak) {
			t.Fatalf("backend error text leaked %q: %#v", leak, detail)
		}
	}
}

func TestProtocolV2SessionServiceFailsClosedWithoutAdmission(t *testing.T) {
	backend := newSessionTestBackend()
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := newProtocolV2SessionDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), time.Now, 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolV2SessionServiceServer(store, authority, sessionTestInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	identity := sessionTestRuntimeIdentity("acme.session", "instance-a")
	token, err := authority.MintProtocolV2SessionDelegation(
		context.Background(), identity, sessionTestScope(strings.Repeat("9", 64), time.Now()),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Get(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity),
		&hostv2.PluginSessionGetRequest{
			Context: &protocolv2.RequestContext{
				RequestId: "request-1", Extension: identity,
				Deadline:              timestamppb.New(time.Now().Add(time.Minute)),
				HostSessionDelegation: &protocolv2.HostSessionDelegation{Token: token},
			},
			NamespaceKey: "k",
		})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetError().GetReason() != "host.plugin_session_runtime_stale" {
		t.Fatalf("a service without admission must deny: %#v", response.GetError())
	}
}

func TestProtocolV2SessionServiceRejectsMissingConstructionInputs(t *testing.T) {
	backend := newSessionTestBackend()
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := newProtocolV2SessionDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), time.Now, 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProtocolV2SessionServiceServer(nil, authority, sessionTestInstallationID); err == nil {
		t.Fatal("nil store was accepted")
	}
	if _, err := NewProtocolV2SessionServiceServer(store, nil, sessionTestInstallationID); err == nil {
		t.Fatal("nil authority was accepted")
	}
	if _, err := NewProtocolV2SessionServiceServer(store, authority, "bad:id"); err == nil {
		t.Fatal("invalid installation id was accepted")
	}
}

func decodeSessionTestJWT(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// ---------------------------------------------------------------------------
// The Gateway is the production S9 issuer. bootstrap/app.go hands the bare
// Gateway to ProtocolStarter, which type-asserts this exact capability; a
// Gateway without it fails every Host-initiated invocation that carries a
// session scope. These tests pin both sides: fail closed before binding, real
// mint → service → retire round trip after binding.
// ---------------------------------------------------------------------------

func TestGatewaySessionDelegationIssuerFailsClosedBeforeBinding(t *testing.T) {
	gateway := NewGateway(New(Config{}))
	var _ ProtocolV2SessionDelegationIssuer = gateway
	scope := sessionTestScope(strings.Repeat("c", 64), time.Now().UTC())
	_, err := gateway.MintProtocolV2SessionDelegation(
		context.Background(), sessionTestRuntimeIdentity("acme.session", "instance-a"), scope)
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("unbound gateway mint = (%q, %v), want a named not-bound failure", "", err)
	}
	if err := gateway.RetireProtocolV2SessionDelegation("any-token"); err == nil {
		t.Fatal("unbound gateway retire must fail")
	}
}

func TestGatewaySessionDelegationIssuerRoundTripAfterBinding(t *testing.T) {
	backend := newSessionTestBackend()
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	store.WithClock(func() time.Time { return clock })
	authority, err := newProtocolV2SessionDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return clock }, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity := sessionTestRuntimeIdentity("acme.session", "instance-gateway")
	service, err := NewProtocolV2SessionServiceServer(
		store, authority, sessionTestInstallationID, WithProtocolV2SessionAdmission(&sessionTestAdmission{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2SessionService(service); err != nil {
		t.Fatal(err)
	}

	scope := sessionTestScope(strings.Repeat("d", 64), clock)
	token, err := gateway.MintProtocolV2SessionDelegation(context.Background(), identity, scope)
	if err != nil {
		t.Fatalf("gateway mint: %v", err)
	}
	if token == "" {
		t.Fatal("gateway mint returned an empty token")
	}

	// Mint 后同一令牌必须能在真实的已服务边界上工作（在生产这是同一个 Gateway）。
	response, err := service.Get(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), identity),
		&hostv2.PluginSessionGetRequest{Context: fixtureGatewayRequestContext(identity, token), NamespaceKey: "gateway/roundtrip"},
	)
	if err != nil || response.GetError() != nil {
		t.Fatalf("gateway-minted token rejected by the served boundary: %#v / %v", response, err)
	}
	if response.GetFound() {
		t.Fatalf("fresh token unexpectedly read state: %#v", response)
	}

	if err := gateway.RetireProtocolV2SessionDelegation(token); err != nil {
		t.Fatalf("gateway retire: %v", err)
	}
	// 收回后同一个令牌必须被拒（单次调用语义，经真实服务结算路径复用 fail closed）。
	replayed, err := service.Get(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), identity),
		&hostv2.PluginSessionGetRequest{Context: fixtureGatewayRequestContext(identity, token), NamespaceKey: "gateway/roundtrip"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GetError().GetReason() != "host.plugin_session_scope_unattested" {
		t.Fatalf("a retired delegation was accepted: %#v", replayed)
	}
}

func fixtureGatewayRequestContext(identity *protocolv2.ExtensionIdentity, token string) *protocolv2.RequestContext {
	return &protocolv2.RequestContext{
		RequestId: "gateway-request", Extension: identity,
		Deadline:              timestamppb.New(time.Now().Add(time.Minute)),
		HostSessionDelegation: &protocolv2.HostSessionDelegation{Token: token},
	}
}
