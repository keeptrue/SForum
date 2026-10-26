package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// recordingProtocolV2SessionDelegationIssuer 记录签发/收回，并可注入失败。
type recordingProtocolV2SessionDelegationIssuer struct {
	authority *hostapi.ProtocolV2SessionDelegationAuthority
	mu        sync.Mutex
	minted    []hostapi.ProtocolV2SessionScope
	retired   []string
	err       error
}

func (i *recordingProtocolV2SessionDelegationIssuer) MintProtocolV2SessionDelegation(
	ctx context.Context,
	runtime *protocolwire.ExtensionIdentity,
	scope hostapi.ProtocolV2SessionScope,
) (string, error) {
	if i.err != nil {
		return "", i.err
	}
	i.mu.Lock()
	i.minted = append(i.minted, scope)
	i.mu.Unlock()
	return i.authority.MintProtocolV2SessionDelegation(ctx, runtime, scope)
}

func (i *recordingProtocolV2SessionDelegationIssuer) RetireProtocolV2SessionDelegation(token string) error {
	i.mu.Lock()
	i.retired = append(i.retired, token)
	i.mu.Unlock()
	return i.authority.RetireProtocolV2SessionDelegation(token)
}

func (i *recordingProtocolV2SessionDelegationIssuer) snapshot() ([]hostapi.ProtocolV2SessionScope, []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]hostapi.ProtocolV2SessionScope(nil), i.minted...), append([]string(nil), i.retired...)
}

func sessionDelegationTestIdentity(instanceID string) *protocolwire.ExtensionIdentity {
	return &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("a", 64), TrustGrantId: "grant-1",
		RuntimeEpoch: 1, InstanceId: instanceID,
	}
}

func newSessionDelegationTestClient(
	t *testing.T,
	issuer hostapi.ProtocolV2SessionDelegationIssuer,
	invoke func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error),
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2RouteTestServer{invoke: invoke})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///session-delegation-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: sessionDelegationTestIdentity("instance-a"), instance: "instance-a",
		sessionDelegations: issuer,
		routes: []extensions.ManifestRoute{{
			ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionAdd,
			Methods: []string{http.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
			RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		}},
	})
}

func sessionDelegationRouteResponse(request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	return protocolV2RouteTestResponse(request, map[string]any{"ok": true})
}

func sessionDelegationScope() authsession.RequestScope {
	now := time.Now().UTC()
	return authsession.RequestScope{
		SessionFingerprint: strings.Repeat("b", 64),
		IdleExpiresAt:      now.Add(time.Hour),
		AbsoluteExpiresAt:  now.Add(24 * time.Hour),
		ActorUserID:        42,
	}
}

// S9 的签发侧：宿主在自己的 HTTP 请求里发起插件调用时才签发，且调用结束后收回。
func TestInvocationMintsAndRetiresSessionDelegation(t *testing.T) {
	authority, err := hostapi.NewProtocolV2SessionDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	issuer := &recordingProtocolV2SessionDelegationIssuer{authority: authority}
	var received string
	client := newSessionDelegationTestClient(t, issuer, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request.GetContext().GetHostSessionDelegation().GetToken()
		return sessionDelegationRouteResponse(request), nil
	})

	ctx := authsession.WithPluginSessionScope(context.Background(), sessionDelegationScope())
	response, err := client.InvokeRouteContext(ctx, ProtocolV2RouteRequest{
		RouteID: "demo.route", ContractVersion: "demo.route@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet, Path: "/demo",
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}
	if strings.TrimSpace(received) == "" {
		t.Fatal("the invocation carried no session delegation")
	}
	minted, retired := issuer.snapshot()
	if len(minted) != 1 {
		t.Fatalf("minted %d delegations, want exactly 1", len(minted))
	}
	if minted[0].SessionFingerprint != sessionDelegationScope().SessionFingerprint || minted[0].ActorUserID != 42 {
		t.Fatalf("minted scope drifted: %#v", minted[0])
	}
	if len(retired) != 1 || retired[0] != received {
		t.Fatalf("the delegation was not retired after the invocation: %#v", retired)
	}
	// 单次调用语义：调用结束后同一个令牌必须失效（复用 fail closed）。
	if _, err := authority.Resolve(received, sessionDelegationTestIdentity("instance-a")); err == nil {
		t.Fatal("a delegation stayed valid after its invocation settled")
	}
}

func TestInvocationWithoutSessionScopeMintsNothing(t *testing.T) {
	authority, err := hostapi.NewProtocolV2SessionDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	issuer := &recordingProtocolV2SessionDelegationIssuer{authority: authority}
	var received string
	client := newSessionDelegationTestClient(t, issuer, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request.GetContext().GetHostSessionDelegation().GetToken()
		return sessionDelegationRouteResponse(request), nil
	})

	// 后台调用：context 上没有会话范围。
	if _, err := client.InvokeRouteContext(context.Background(), ProtocolV2RouteRequest{
		RouteID: "demo.route", ContractVersion: "demo.route@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet, Path: "/demo",
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	minted, retired := issuer.snapshot()
	if len(minted) != 0 || len(retired) != 0 || received != "" {
		t.Fatalf("a scoped invocation happened without a scope: minted=%d retired=%d token=%q", len(minted), len(retired), received)
	}
}

func TestInvocationFailsClosedWhenSessionDelegationCannotBeMinted(t *testing.T) {
	issuer := &recordingProtocolV2SessionDelegationIssuer{err: errors.New("ledger unavailable")}
	called := false
	client := newSessionDelegationTestClient(t, issuer, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		called = true
		return sessionDelegationRouteResponse(request), nil
	})
	ctx := authsession.WithPluginSessionScope(context.Background(), sessionDelegationScope())
	_, err := client.InvokeRouteContext(ctx, ProtocolV2RouteRequest{
		RouteID: "demo.route", ContractVersion: "demo.route@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet, Path: "/demo",
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	})
	if !errors.Is(err, ErrProtocolV2SessionDelegationUnavailable) {
		t.Fatalf("mint failure = %v, want ErrProtocolV2SessionDelegationUnavailable", err)
	}
	if called {
		t.Fatal("the plugin ran even though the session delegation could not be minted")
	}
}

// 入站方向：宿主交付的会话委派可以被插件回带（S9 反转了方向），而 actor 委派仍然
// 只能由宿主发给插件。
func TestHostContextAcceptsInboundSessionDelegationOnly(t *testing.T) {
	binding := protocolV2HostBinding{identity: sessionDelegationTestIdentity("instance-a")}
	base := func() *protocolwire.RequestContext {
		return &protocolwire.RequestContext{
			RequestId: "request-1", Extension: sessionDelegationTestIdentity("instance-a"),
			Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		}
	}

	accepted := base()
	accepted.HostSessionDelegation = &protocolwire.HostSessionDelegation{Token: "opaque-token"}
	if err := validateProtocolV2HostContext(accepted, binding); err != nil {
		t.Fatalf("inbound session delegation was rejected: %v", err)
	}

	malformed := base()
	malformed.HostSessionDelegation = &protocolwire.HostSessionDelegation{Token: ""}
	if err := validateProtocolV2HostContext(malformed, binding); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty session delegation = %v, want InvalidArgument", err)
	}

	command := base()
	command.HostCommandDelegations = []*protocolwire.HostCommandDelegation{{Token: "t"}}
	if err := validateProtocolV2HostContext(command, binding); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("inbound command delegation = %v, want PermissionDenied", err)
	}

	query := base()
	query.HostQueryDelegations = []*protocolwire.HostQueryDelegation{{Token: "t"}}
	if err := validateProtocolV2HostContext(query, binding); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("inbound query delegation = %v, want PermissionDenied", err)
	}
}
