package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	protocolwire "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2RawAuthorityRequiresExactFrozenRoute(t *testing.T) {
	var received *pluginwire.RouteRequest
	calls := 0
	client := newProtocolV2RouteTestClient(t, "runtime-raw-route", func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		calls++
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	// 决定 R1（2026-10-16）：raw 路由不再由 Host 的 core.guard.raw_request 提供，
	// 改用包内声明的 kind=raw_request 守卫；本用例继续钉住「精确冻结路由 + 凭证转发」。
	useProtocolV2DeclaredRawGuard(client)
	request := protocolV2RouteTestRequest()
	request.Authority = protocolV2RawRequestAuthority()
	request.Headers = http.Header{
		"Cookie":        {"session=one", "preferences=two"},
		"Authorization": {"Bearer one", "Bearer two"},
		"X-Api-Key":     {"api-key-one", "api-key-two"},
		"X-Auth-Token":  {"auth-token-one", "auth-token-two"},
		"X-Test":        {"one", "two"},
	}
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || !reflect.DeepEqual(headers.Values("X-Test"), request.Headers.Values("X-Test")) {
		t.Fatalf("raw headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, true)

	request.Authority = protocolV2FilteredHostRequestAuthority()
	if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteInvalid) || calls != 1 {
		t.Fatalf("filtered downgrade error=%v calls=%d", err, calls)
	}
	client.routes[0].Guard = extensionmanifest.GuardCorePublic
	request.Authority = protocolV2RawRequestAuthority()
	if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteInvalid) || calls != 1 {
		t.Fatalf("raw escalation error=%v calls=%d", err, calls)
	}
	request.Authority = protocolV2FilteredHostRequestAuthority()
	received = nil
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil || calls != 2 {
		t.Fatalf("filtered route error=%v calls=%d", err, calls)
	}
	headers, err = protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || !reflect.DeepEqual(headers.Values("X-Test"), request.Headers.Values("X-Test")) {
		t.Fatalf("filtered headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, false)
}

// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 曾允许零守卫代码的路由取得
// raw 请求权限（cookie/authorization 等凭证会被转发给插件处理器），运维方判定这是漏洞。
// 本用例固定运行时拒绝侧：权限解析、路由调用、流开启三条路径都必须 fail closed，且插件
// 一次都收不到调用；同时在同一客户端上证明包内声明的 kind=raw_request 守卫立刻恢复 raw，
// 也就是说拒绝来自守卫来源，而不是 raw 权限被整体移除。
func TestProtocolV2CoreRawGuardNoLongerResolvesToRawAuthority(t *testing.T) {
	calls := 0
	client := newProtocolV2RouteTestClient(t, "runtime-core-raw-refused", func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		calls++
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	client.routes[0].Guard = extensionmanifest.GuardCoreRaw

	authority, err := client.frozenRouteAuthority(client.routes[0])
	if !errors.Is(err, ErrProtocolV2RequestAuthorityInvalid) || authority.Mode == ProtocolV2RequestAuthorityRaw ||
		authority.GuardKind == ProtocolV2RequestGuardRawRequest {
		t.Fatalf("core raw guard froze authority=%#v error=%v; want refusal", authority, err)
	}

	request := protocolV2RouteTestRequest()
	request.Authority = protocolV2RawRequestAuthority()
	if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteInvalid) ||
		!errors.Is(err, ErrProtocolV2RequestAuthorityInvalid) || calls != 0 {
		t.Fatalf("core raw route invocation error=%v calls=%d", err, calls)
	}

	useProtocolV2DeclaredRawGuard(client)
	declared, err := client.frozenRouteAuthority(client.routes[0])
	if err != nil || declared != protocolV2RawRequestAuthority() {
		t.Fatalf("declared raw guard authority=%#v error=%v", declared, err)
	}
	client.routes[0].Guard = extensionmanifest.GuardCoreRaw

	opens := make(chan struct{}, 1)
	streamClient := newProtocolV2RouteStreamTestClient(t, func(
		_ grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame],
	) error {
		opens <- struct{}{}
		return nil
	})
	streamClient.routes[0].Guard = extensionmanifest.GuardCoreRaw
	if _, err := streamClient.OpenRouteStreamContext(context.Background(), ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2RawRequestAuthority(),
		Headers:   http.Header{"Cookie": {"session=stream"}, "Authorization": {"Bearer stream"}},
	}); !errors.Is(err, ErrProtocolV2RouteStreamInvalid) || !errors.Is(err, ErrProtocolV2RequestAuthorityInvalid) {
		t.Fatalf("core raw stream open error=%v", err)
	}
	select {
	case <-opens:
		t.Fatal("core raw stream authority reached the plugin")
	default:
	}
}

func TestProtocolV2RawAuthorityRequiresExactFrozenGuard(t *testing.T) {
	var received *pluginwire.RouteRequest
	calls := 0
	client := protocolV2GuardTestClient(t, func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		calls++
		received = request
		return protocolV2GuardTestResponse(request, http.StatusNoContent), nil
	})
	client.guards[0].Kind = "raw_request"
	request := protocolV2GuardTestRequest()
	request.Authority = protocolV2RawRequestAuthority()
	request.Headers = http.Header{
		"Cookie": {"session=one", "session=two"}, "Authorization": {"Bearer raw"},
		"X-Api-Key": {"guard-api-key"}, "X-Auth-Token": {"guard-auth-token"},
	}
	if err := client.InvokeGuardContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || headers.Get("X-SForum-Guard-Kind") != "raw_request" {
		t.Fatalf("raw guard headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, true)

	request.Authority = protocolV2FilteredCustomRequestAuthority()
	if err := client.InvokeGuardContext(context.Background(), request); !errors.Is(err, ErrProtocolV2GuardInvalid) || calls != 1 {
		t.Fatalf("guard downgrade error=%v calls=%d", err, calls)
	}
	client.guards[0].Kind = "custom"
	request.Authority = protocolV2RawRequestAuthority()
	if err := client.InvokeGuardContext(context.Background(), request); !errors.Is(err, ErrProtocolV2GuardInvalid) || calls != 1 {
		t.Fatalf("guard escalation error=%v calls=%d", err, calls)
	}
	request.Authority = protocolV2FilteredCustomRequestAuthority()
	request.Headers = http.Header{
		"Cookie": {"filtered-session"}, "Authorization": {"Bearer filtered"},
		"X-Api-Key": {"filtered-api-key"}, "X-Auth-Token": {"filtered-auth-token"}, "X-Test": {"guard-visible"},
	}
	received = nil
	if err := client.InvokeGuardContext(context.Background(), request); err != nil || calls != 2 {
		t.Fatalf("filtered guard error=%v calls=%d", err, calls)
	}
	headers, err = protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || headers.Get("X-Test") != "guard-visible" || headers.Get("X-SForum-Guard-Kind") != "custom" {
		t.Fatalf("filtered guard headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, false)
}

func TestProtocolV2RequestAuthorityStripsHostMetadataBeforeRPC(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-header-fence", func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	// 决定 R1（2026-10-16）：包内声明的 kind=raw_request 守卫取代 core.guard.raw_request。
	useProtocolV2DeclaredRawGuard(client)
	request := protocolV2RouteTestRequest()
	request.Authority = protocolV2RawRequestAuthority()
	request.Headers = http.Header{
		"Host":                {"internal.example"},
		"Content-Length":      {"42"},
		"Proxy-Authorization": {"Basic secret"},
		"Proxy-Connection":    {"keep-alive"},
		"X-CSRF-Token":        {"secret"},
		"X-SForum-Actor-ID":   {"42"},
		"Transfer-Encoding":   {"chunked"},
		"Connection":          {"X-Private-Hop"},
		"connection":          {"X-Lower-Hop"},
		"CONNECTION":          {"X-Upper-Hop"},
		"X-Private-Hop":       {"secret"},
		"X-Lower-Hop":         {"lower-secret"},
		"X-Upper-Hop":         {"upper-secret"},
		"X-Keep":              {"one", "two"},
	}
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || !reflect.DeepEqual(headers.Values("X-Keep"), []string{"one", "two"}) {
		t.Fatalf("headers=%#v error=%v", headers, err)
	}
	for _, name := range []string{
		"Host", "Content-Length", "Proxy-Authorization", "Proxy-Connection", "X-Csrf-Token",
		"X-SForum-Actor-ID", "Transfer-Encoding", "Connection", "X-Private-Hop", "X-Lower-Hop", "X-Upper-Hop",
	} {
		if values := headers.Values(name); len(values) != 0 {
			t.Fatalf("blocked header %s survived: %#v", name, values)
		}
	}
}

func TestProtocolV2RawAuthorityIsPreservedForRouteStreams(t *testing.T) {
	opens := make(chan *pluginwire.RouteStreamOpen, 1)
	client := newProtocolV2RouteStreamTestClient(t, func(
		stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame],
	) error {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		opens <- frame.GetOpen()
		return nil
	})
	// 决定 R1（2026-10-16）：raw 流权限来自包内声明的守卫，不再来自 core.guard.raw_request。
	useProtocolV2DeclaredRawGuard(client)
	request := ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2RawRequestAuthority(),
		Headers: http.Header{
			"Cookie": {"session=stream"}, "Authorization": {"Bearer stream"},
			"X-Api-Key": {"stream-api-key"}, "X-Auth-Token": {"stream-auth-token"}, "X-Test": {"stream-visible"},
			"connection": {"X-Stream-Lower-Hop"}, "CONNECTION": {"X-Stream-Upper-Hop"},
			"X-Stream-Lower-Hop": {"lower-secret"}, "X-Stream-Upper-Hop": {"upper-secret"},
		},
	}
	stream, err := client.OpenRouteStreamContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	open := <-opens
	headers, err := protocolV2RouteHTTPHeaders(open.GetHeaders())
	if err != nil || headers.Get("X-Test") != "stream-visible" {
		t.Fatalf("stream headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, true)
	assertProtocolV2BlockedHeaders(t, headers, "Connection", "X-Stream-Lower-Hop", "X-Stream-Upper-Hop")
	stream.Cancel()

	request.Authority = protocolV2FilteredHostRequestAuthority()
	if _, err := client.OpenRouteStreamContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
		t.Fatalf("stream downgrade error=%v", err)
	}
	client.routes[0].Guard = extensionmanifest.GuardCorePublic
	stream, err = client.OpenRouteStreamContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	open = <-opens
	headers, err = protocolV2RouteHTTPHeaders(open.GetHeaders())
	if err != nil || headers.Get("X-Test") != "stream-visible" {
		t.Fatalf("filtered stream headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, false)
	assertProtocolV2BlockedHeaders(t, headers, "Connection", "X-Stream-Lower-Hop", "X-Stream-Upper-Hop")
	stream.Cancel()

	client.routes[0].Mode = extensionmanifest.RouteModeWebSocket
	request.Mode = extensionmanifest.RouteModeWebSocket
	request.Headers.Set("Host", "internal.example")
	request.Headers.Set("Connection", "Upgrade")
	request.Headers.Set("Upgrade", "websocket")
	request.Headers.Set("Proxy-Authorization", "Basic proxy-secret")
	request.Headers.Set("X-CSRF-Token", "csrf-secret")
	request.Headers.Set("X-SForum-Forged", "forged")
	stream, err = client.OpenRouteStreamContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	open = <-opens
	headers, err = protocolV2RouteHTTPHeaders(open.GetHeaders())
	if err != nil || headers.Get("X-Test") != "stream-visible" {
		t.Fatalf("filtered websocket headers=%#v error=%v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, false)
	assertProtocolV2BlockedHeaders(t, headers,
		"Host", "Connection", "Upgrade", "Proxy-Authorization", "X-CSRF-Token", "X-SForum-Forged",
		"X-Stream-Lower-Hop", "X-Stream-Upper-Hop",
	)
}

func assertProtocolV2BlockedHeaders(t *testing.T, headers http.Header, names ...string) {
	t.Helper()
	for _, name := range names {
		if values := headers.Values(name); len(values) != 0 {
			t.Fatalf("blocked header %s survived: %#v", name, values)
		}
	}
}

func assertProtocolV2CredentialHeaders(t *testing.T, headers, source http.Header, raw bool) {
	t.Helper()
	for _, name := range []string{"Cookie", "Authorization", "X-API-Key", "X-Auth-Token"} {
		got := headers.Values(name)
		if raw && !reflect.DeepEqual(got, source.Values(name)) {
			t.Fatalf("raw credential %s=%#v, want %#v", name, got, source.Values(name))
		}
		if !raw && len(got) != 0 {
			t.Fatalf("filtered credential %s survived: %#v", name, got)
		}
	}
}

func protocolV2FilteredHostRequestAuthority() ProtocolV2RequestAuthority {
	return ProtocolV2RequestAuthority{
		Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardHost,
	}
}

func protocolV2FilteredCustomRequestAuthority() ProtocolV2RequestAuthority {
	return ProtocolV2RequestAuthority{
		Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardCustom,
	}
}

func protocolV2RawRequestAuthority() ProtocolV2RequestAuthority {
	return ProtocolV2RequestAuthority{
		Mode: ProtocolV2RequestAuthorityRaw, GuardKind: ProtocolV2RequestGuardRawRequest,
	}
}

// protocolV2DeclaredRawGuardID 是包内声明的 kind=raw_request 守卫。
// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 可在零守卫代码下铸造 raw
// 请求权限，已被判定为漏洞；raw 权限从此只能来自包内显式声明（entry + digest +
// contractVersion）的守卫。覆盖 raw 行为的用例因此必须引用这个声明守卫。
const protocolV2DeclaredRawGuardID = "demo.guard.raw"

// useProtocolV2DeclaredRawGuard 把客户端切换到包内声明的 kind=raw_request 守卫：
// 客户端声明该守卫，并让所有已冻结路由引用它。
func useProtocolV2DeclaredRawGuard(client *protocolV2Client) {
	client.guards = []extensions.ManifestGuard{{
		ID: protocolV2DeclaredRawGuardID, ContractVersion: protocolV2DeclaredRawGuardID + "@1",
		Kind: extensionmanifest.GuardKindRawRequest, Entry: "backend/guard", Digest: "guard-digest",
	}}
	for index := range client.routes {
		client.routes[index].Guard = protocolV2DeclaredRawGuardID
	}
}

// M7 任务 5：CRLF 注入与非法头名必须在投影处被拒绝，绝不能进入 RPC 文档。
func TestProtocolV2RequestAuthorityRejectsCRLFAndInvalidHeaderNames(t *testing.T) {
	calls := 0
	client := newProtocolV2RouteTestClient(t, "runtime-crlf-fence", func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		calls++
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	// 决定 R1（2026-10-16）：这里必须真的拿到 raw 权限，否则拒绝原因会来自路由权威
	// 解析而不是头投影，CRLF/非法头名的覆盖就会失真（假通过）。
	useProtocolV2DeclaredRawGuard(client)
	authority := protocolV2RawRequestAuthority()

	for name, headers := range map[string]http.Header{
		"crlf in value": {"X-Trace": {"ok\r\nX-Injected: yes"}},
		"crlf in name":  {"X-Bad\r\nX-Injected": {"yes"}},
		"space in name": {"X Bad": {"yes"}},
		"nul in value":  {"X-Trace": {"ok\x00evil"}},
		"colon in name": {"X-Bad:": {"yes"}},
	} {
		t.Run(name, func(t *testing.T) {
			request := protocolV2RouteTestRequest()
			request.Authority = authority
			request.Headers = headers
			if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RequestAuthorityInvalid) {
				t.Fatalf("error = %v; want ErrProtocolV2RequestAuthorityInvalid", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid headers reached the plugin %d time(s)", calls)
	}
}

// M7 任务 5：代理/客户端身份头是**客户端或上游代理**提供的输入，宿主既不添加也不
// 背书。此测试固定这条边界：插件看到的是原样透传的声明，不能把它当作已核验身份。
func TestProtocolV2RequestAuthorityDoesNotVouchForClientIdentityHeaders(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-proxy-fence", func(
		_ context.Context,
		request *pluginwire.RouteRequest,
	) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	request := protocolV2RouteTestRequest()
	request.Headers = http.Header{
		"X-Forwarded-For": {"203.0.113.9, 10.0.0.1"},
		"X-Real-Ip":       {"203.0.113.9"},
		"Forwarded":       {"for=203.0.113.9;proto=http"},
		"User-Agent":      {"probe/1.0"},
	}
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"X-Forwarded-For": "203.0.113.9, 10.0.0.1",
		"X-Real-Ip":       "203.0.113.9",
		"Forwarded":       "for=203.0.113.9;proto=http",
		"User-Agent":      "probe/1.0",
	} {
		if got := headers.Get(name); got != want {
			t.Fatalf("header %s = %q; want the client's own claim %q", name, got, want)
		}
	}
	// 宿主不得注入任何"已核验客户端身份"的头：没有 x-sforum-client-ip 之类的派生字段。
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-sforum-") {
			t.Fatalf("host-derived identity header leaked into the request document: %s", name)
		}
	}
}

// M7 任务 5：超大请求文档必须在 RPC 边界失败，而不是被静默截断后交给插件。
// 这里在**进程内**装配与生产相同的消息上限（SDK 服务端 MaxRecvMsgSize /
// 客户端 MaxCallRecvMsgSize），因此断言的是同一组限制，而不是替身行为。
func TestProtocolV2RequestAuthorityBoundsOversizedDocumentsAtTheRPCBoundary(t *testing.T) {
	var received *pluginwire.RouteRequest
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(DefaultProtocolV2MaxMessageBytes))
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2RouteTestServer{
		invoke: func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
			received = request
			return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
		},
	})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///route-oversize",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(DefaultProtocolV2MaxMessageBytes),
			grpc.MaxCallSendMsgSize(DefaultProtocolV2MaxMessageBytes),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
			TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "runtime-oversize",
		},
		instance: "runtime-oversize",
		routes: []extensions.ManifestRoute{{
			ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionAdd,
			Methods: []string{http.MethodGet, http.MethodPost},
			Guard:   extensionmanifest.GuardCorePublic, RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		}},
	})

	request := protocolV2RouteTestRequest()
	request.Headers = http.Header{"X-Oversize": {strings.Repeat("a", DefaultProtocolV2MaxMessageBytes+1)}}
	if _, err := client.InvokeRouteContext(context.Background(), request); err == nil {
		t.Fatalf("oversized request document was accepted")
	}
	if received != nil {
		t.Fatalf("oversized request reached the plugin")
	}
}

// TestProtocolV2RouteRequestAuthorityMatchesFrozenDeclaration 固定「宿主推导的请求权威
// 与运行时冻结声明一致」这条不变量：legacy 代理回落与声明路由派发必须算出同一个值。
func TestProtocolV2RouteRequestAuthorityMatchesFrozenDeclaration(t *testing.T) {
	customGuards := []extensions.ManifestGuard{
		{ID: "demo.guard.custom", Kind: "custom"},
		{ID: "demo.guard.raw", Kind: "raw_request"},
		{ID: "demo.guard.unknown", Kind: "mystery"},
	}
	cases := []struct {
		name    string
		route   extensions.ManifestRoute
		want    ProtocolV2RequestAuthority
		wantErr bool
	}{
		{
			name:  "core login is host-filtered",
			route: extensions.ManifestRoute{Guard: extensionmanifest.GuardCoreLogin},
			want: ProtocolV2RequestAuthority{
				Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardHost,
			},
		},
		{
			name:  "core public is host-filtered",
			route: extensions.ManifestRoute{Guard: extensionmanifest.GuardCorePublic},
			want: ProtocolV2RequestAuthority{
				Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardHost,
			},
		},
		{
			name:    "raw host guard fails closed",
			route:   extensions.ManifestRoute{Guard: extensionmanifest.GuardCoreRaw},
			wantErr: true,
		},
		{
			name:  "custom guard keeps filtered credentials",
			route: extensions.ManifestRoute{Guard: "demo.guard.custom"},
			want: ProtocolV2RequestAuthority{
				Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardCustom,
			},
		},
		{
			name:  "raw_request guard keeps raw credentials",
			route: extensions.ManifestRoute{Guard: "demo.guard.raw"},
			want: ProtocolV2RequestAuthority{
				Mode: ProtocolV2RequestAuthorityRaw, GuardKind: ProtocolV2RequestGuardRawRequest,
			},
		},
		{
			name:    "undeclared guard fails closed",
			route:   extensions.ManifestRoute{Guard: "demo.guard.missing"},
			wantErr: true,
		},
		{
			name:    "unknown guard kind fails closed",
			route:   extensions.ManifestRoute{Guard: "demo.guard.unknown"},
			wantErr: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ProtocolV2RouteRequestAuthority(testCase.route, customGuards)
			if testCase.wantErr {
				if !errors.Is(err, ErrProtocolV2RequestAuthorityInvalid) {
					t.Fatalf("expected ErrProtocolV2RequestAuthorityInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("authority = %#v, want %#v", got, testCase.want)
			}
		})
	}
}
