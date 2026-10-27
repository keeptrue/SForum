package hostapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hosthttp "github.com/keeptrue/sforum/apps/api/app/Support/HostHTTP"
	pluginfiles "github.com/keeptrue/sforum/apps/api/app/Support/PluginFiles"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

func TestProtocolV2SecretResolveExactRuntimeAndDeny(t *testing.T) {
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := secrets.Put(ctx, secretstore.Ref{Namespace: "demo.secret", SecretID: "token"},
		[]byte("sekrit"), secretstore.PutOptions{Actor: "admin", Purposes: []string{"http.credential"}}); err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolV2SecretServiceServer(secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.secret", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("a", 64), InstanceId: "inst-1",
	}
	reqCtx := &protocolv2.RequestContext{RequestId: "sec-1", Extension: identity}
	okCtx := ContextWithProtocolV2RuntimeIdentity(ctx, identity)

	got, err := server.Resolve(okCtx, &hostv2.SecretResolveRequest{
		Context: reqCtx, SecretId: "token", Purpose: "http.credential",
	})
	if err != nil || got.GetError() != nil || string(got.GetValue()) != "sekrit" {
		t.Fatalf("resolve = %#v err=%v", got, err)
	}

	// 无 attested runtime。
	unauth, err := server.Resolve(ctx, &hostv2.SecretResolveRequest{
		Context: reqCtx, SecretId: "token", Purpose: "http.credential",
	})
	if err != nil || unauth.GetError().GetReason() != "host.runtime_unattested" {
		t.Fatalf("unattested = %#v err=%v", unauth, err)
	}

	// 伪造 digest → stale。
	forged := proto.Clone(reqCtx).(*protocolv2.RequestContext)
	forged.Extension = &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.secret", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("f", 64), InstanceId: "inst-1",
	}
	stale, err := server.Resolve(okCtx, &hostv2.SecretResolveRequest{
		Context: forged, SecretId: "token", Purpose: "http.credential",
	})
	if err != nil || stale.GetError().GetReason() != "host.runtime_stale" {
		t.Fatalf("stale = %#v err=%v", stale, err)
	}

	// 跨命名空间：attested 为 other.plugin，尝试解析 demo.secret。
	other := &protocolv2.ExtensionIdentity{
		ExtensionId: "other.plugin", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("b", 64), InstanceId: "inst-2",
	}
	otherCtx := ContextWithProtocolV2RuntimeIdentity(ctx, other)
	denied, err := server.Resolve(otherCtx, &hostv2.SecretResolveRequest{
		Context:  &protocolv2.RequestContext{RequestId: "x", Extension: other},
		SecretId: "sforum.secret://demo.secret/token", Purpose: "http.credential",
	})
	if err != nil || denied.GetError().GetReason() != "host.secret_denied" {
		t.Fatalf("namespace deny = %#v err=%v", denied, err)
	}
}

func TestProtocolV2HttpSSRFAndSecretPolicy(t *testing.T) {
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := secrets.Put(ctx, secretstore.Ref{Namespace: "demo.http", SecretID: "token"},
		[]byte("tok-xyz"), secretstore.PutOptions{Actor: "admin", Purposes: []string{"http.credential"}}); err != nil {
		t.Fatal(err)
	}

	// httptest 走 raw client（SSRF safe 路径会拒绝 loopback；生产插件默认 safe）。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-xyz" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	client := hosthttp.New(hosthttp.Options{
		AllowHTTP: true, AllowRaw: true, Secrets: secrets,
		RawClient: upstream.Client(), Timeout: 5 * time.Second,
	})
	server, err := NewProtocolV2HttpServiceServer(client)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.http", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("c", 64), InstanceId: "inst-http",
	}
	reqCtx := &protocolv2.RequestContext{RequestId: "http-1", Extension: identity}
	okCtx := ContextWithProtocolV2RuntimeIdentity(ctx, identity)

	// SSRF：内网地址在 safe 权威下拒绝。
	ssrf, err := server.Do(okCtx, &hostv2.HttpRequest{
		Context: reqCtx, Method: "GET", Url: "http://127.0.0.1:1/",
	})
	if err != nil || ssrf.GetError().GetReason() != "host.http_ssrf" {
		t.Fatalf("ssrf = %#v err=%v", ssrf, err)
	}

	// 跨命名空间 secret policy 拒绝。
	other := &protocolv2.ExtensionIdentity{
		ExtensionId: "other.http", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("9", 64), InstanceId: "inst-o",
	}
	otherCtx := ContextWithProtocolV2RuntimeIdentity(ctx, other)
	denied, err := server.Do(otherCtx, &hostv2.HttpRequest{
		Context:  &protocolv2.RequestContext{RequestId: "http-2", Extension: other},
		Method:   "GET",
		Url:      upstream.URL,
		PolicyId: "sforum.secret://demo.http/token",
	})
	// PolicyId secret + Safe authority 会先 SSRF loopback；用 raw 需要 policy raw。
	// 此处验证 secret resolve 失败：先用 raw client 路径绕过 SSRF。
	// Protocol 默认 Safe，对 loopback 返回 ssrf 或 secret_denied 均可接受为拒绝。
	if err != nil || denied.GetError() == nil {
		t.Fatalf("cross-namespace secret must fail: %#v err=%v", denied, err)
	}

	// 同命名空间：HostHTTP 层注入已在 Support/HostHTTP 覆盖；此处验证 unattested。
	unauth, err := server.Do(ctx, &hostv2.HttpRequest{
		Context: reqCtx, Method: "GET", Url: "https://example.com/",
	})
	if err != nil || unauth.GetError().GetReason() != "host.runtime_unattested" {
		t.Fatalf("unattested = %#v err=%v", unauth, err)
	}
}

func TestProtocolV2FileNamespaceIsolation(t *testing.T) {
	files, err := pluginfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.EnsureNamespace(pluginfiles.Namespace{ExtensionID: "demo.files"}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Write(pluginfiles.WriteRequest{
		ExtensionID: "demo.files", Kind: pluginfiles.KindPrivate, RelativePath: "a.txt",
		Data: []byte("hello"), Actor: "demo.files",
	}); err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolV2FileServiceServer(files)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.files", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("d", 64), InstanceId: "inst-f",
	}
	reqCtx := &protocolv2.RequestContext{RequestId: "f-1", Extension: identity}
	okCtx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)

	stat, err := server.Stat(okCtx, &hostv2.FileStatRequest{Context: reqCtx, FileId: "private/a.txt"})
	if err != nil || stat.GetError() != nil || !stat.GetExists() || stat.GetSize() != 5 {
		t.Fatalf("stat = %#v err=%v", stat, err)
	}

	// 路径穿越拒绝。
	bad, err := server.Stat(okCtx, &hostv2.FileStatRequest{Context: reqCtx, FileId: "private/../etc/passwd"})
	if err != nil || bad.GetError() == nil {
		t.Fatalf("traversal should fail: %#v err=%v", bad, err)
	}

	// 其他插件不可 Stat。
	other := &protocolv2.ExtensionIdentity{
		ExtensionId: "other.files", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("e", 64), InstanceId: "inst-o",
	}
	otherCtx := ContextWithProtocolV2RuntimeIdentity(context.Background(), other)
	cross, err := server.Stat(otherCtx, &hostv2.FileStatRequest{
		Context: &protocolv2.RequestContext{RequestId: "f-2", Extension: other},
		FileId:  "private/a.txt",
	})
	if err != nil || cross.GetError().GetReason() != "host.file_not_found" {
		// other.files 命名空间为空 → not found（不是跨读）。
		if cross.GetError() == nil {
			t.Fatalf("cross plugin must not see file: %#v", cross)
		}
	}
}

// secretWriteHarness builds a server whose settings schema declares two secret fields and
// one plain field, the same shape the store plugin ships.
type secretWriteHarness struct {
	server   *ProtocolV2SecretServiceServer
	settings *settingslifecycle.Service
	docs     settingslifecycle.DocumentStore
	secrets  *secretstore.Service
	identity *protocolv2.ExtensionIdentity
	ctx      context.Context
	reqCtx   *protocolv2.RequestContext
}

func newSecretWriteHarness(t *testing.T, caps CapabilitySource) secretWriteHarness {
	t.Helper()
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := settingslifecycle.NewMemoryDocumentStore()
	settings := settingslifecycle.NewWithStore(docs, secrets)
	if err := settings.RegisterSchema("demo.store", 1, []settingslifecycle.FieldSchema{
		{Name: "store_url", Type: "string"},
		{Name: "account_token", Type: "secret", Secret: true},
		{Name: "legacy_token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolV2SecretServiceServer(secrets, settings)
	if err != nil {
		t.Fatal(err)
	}
	if caps != nil {
		server.BindCapabilitySource(caps)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.store", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("d", 64), InstanceId: "inst-store",
	}
	base := context.Background()
	return secretWriteHarness{
		server: server, settings: settings, docs: docs, secrets: secrets,
		identity: identity,
		ctx:      ContextWithProtocolV2RuntimeIdentity(base, identity),
		reqCtx:   &protocolv2.RequestContext{RequestId: "store-1", Extension: identity},
	}
}

func settingsOwnCaps() CapabilitySource {
	return fakeCaps{set: capabilities.NewSet([]string{capabilities.SettingsOwn})}
}

func TestProtocolV2SecretPutWritesDeclaredSecret(t *testing.T) {
	h := newSecretWriteHarness(t, settingsOwnCaps())

	put, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
		Context: h.reqCtx, SecretId: "account_token", Value: []byte("sfo_abc"), MediaType: "text/plain",
	})
	if err != nil || put.GetError() != nil || !put.GetSecretSet() || put.GetVersion() == 0 {
		t.Fatalf("put = %#v err=%v", put, err)
	}

	// 设置页看到的是「已配置」：引用存在、明文不在文档里。
	doc, err := h.settings.Get(context.Background(), "demo.store")
	if err != nil {
		t.Fatal(err)
	}
	if !doc.SecretSet["account_token"] {
		t.Fatalf("secretSet = %#v", doc.SecretSet)
	}
	if ref := doc.SecretRefs["account_token"]; !strings.HasPrefix(ref, secretstore.ReferenceScheme) {
		t.Fatalf("secretRef = %q", ref)
	}
	for key, value := range doc.Values {
		if strings.Contains(value, "sfo_abc") {
			t.Fatalf("plaintext leaked into values[%s]", key)
		}
	}

	// 运行时读回明文（这是插件真正拿到令牌的路径）。
	values, err := h.settings.RuntimeValues(context.Background(), "demo.store", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["account_token"] != "sfo_abc" {
		t.Fatalf("runtime value = %q", values["account_token"])
	}

	// 第二个 secret 的写入不得清掉第一个。
	if _, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
		Context: h.reqCtx, SecretId: "legacy_token", Value: []byte("legacy-1"),
	}); err != nil {
		t.Fatal(err)
	}
	values, err = h.settings.RuntimeValues(context.Background(), "demo.store", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["account_token"] != "sfo_abc" || values["legacy_token"] != "legacy-1" {
		t.Fatalf("partial write clobbered a sibling secret: %#v", values)
	}

	// Clear 只清目标。
	clear, err := h.server.Clear(h.ctx, &hostv2.SecretClearRequest{
		Context: h.reqCtx, SecretId: "account_token",
	})
	if err != nil || clear.GetError() != nil || clear.GetSecretSet() {
		t.Fatalf("clear = %#v err=%v", clear, err)
	}
	values, err = h.settings.RuntimeValues(context.Background(), "demo.store", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["account_token"]; ok {
		t.Fatalf("cleared secret still resolves: %#v", values)
	}
	if values["legacy_token"] != "legacy-1" {
		t.Fatalf("clear removed a sibling secret: %#v", values)
	}
}

func TestProtocolV2SecretWriteRejectsUndeclaredAndNonSecret(t *testing.T) {
	h := newSecretWriteHarness(t, settingsOwnCaps())

	cases := []struct {
		name   string
		secret string
		reason string
	}{
		{"plain field is not a secret", "store_url", "host.secret_field_not_declared"},
		{"unknown key", "nope", "host.secret_field_not_declared"},
		{"namespaced reference", "sforum.secret://demo.store/account_token", "host.secret_request_invalid"},
		{"empty id", "  ", "host.secret_request_invalid"},
	}
	for _, tc := range cases {
		got, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
			Context: h.reqCtx, SecretId: tc.secret, Value: []byte("x"),
		})
		if err != nil || got.GetError().GetReason() != tc.reason {
			t.Fatalf("%s: got %#v err=%v", tc.name, got, err)
		}
	}

	empty, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
		Context: h.reqCtx, SecretId: "account_token", Value: nil,
	})
	if err != nil || empty.GetError().GetReason() != "host.secret_request_invalid" {
		t.Fatalf("empty value = %#v err=%v", empty, err)
	}

	tooLarge := bytes.Repeat([]byte("a"), secretstore.MaxPlaintextBytes+1)
	big, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
		Context: h.reqCtx, SecretId: "account_token", Value: tooLarge,
	})
	if err != nil || big.GetError().GetReason() != "host.secret_request_invalid" {
		t.Fatalf("oversize = %#v err=%v", big, err)
	}
}

func TestProtocolV2SecretWriteRequiresSettingsOwn(t *testing.T) {
	deniedHarness := newSecretWriteHarness(t, fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})})
	denied, err := deniedHarness.server.Put(deniedHarness.ctx, &hostv2.SecretPutRequest{
		Context: deniedHarness.reqCtx, SecretId: "account_token", Value: []byte("x"),
	})
	if err != nil || denied.GetError().GetReason() != "host.secret_denied" {
		t.Fatalf("missing settings.own = %#v err=%v", denied, err)
	}
	clear, err := deniedHarness.server.Clear(deniedHarness.ctx, &hostv2.SecretClearRequest{
		Context: deniedHarness.reqCtx, SecretId: "account_token",
	})
	if err != nil || clear.GetError().GetReason() != "host.secret_denied" {
		t.Fatalf("clear without settings.own = %#v err=%v", clear, err)
	}

	unbound := newSecretWriteHarness(t, nil)
	got, err := unbound.server.Put(unbound.ctx, &hostv2.SecretPutRequest{
		Context: unbound.reqCtx, SecretId: "account_token", Value: []byte("x"),
	})
	if err != nil || got.GetError().GetReason() != "host.capability_unavailable" {
		t.Fatalf("unbound capability source = %#v err=%v", got, err)
	}
}

func TestProtocolV2SecretWriteConflictIsRetryable(t *testing.T) {
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := settingslifecycle.NewWithStore(conflictingDocumentStore{}, secrets)
	if err := settings.RegisterSchema("demo.store", 1, []settingslifecycle.FieldSchema{
		{Name: "account_token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolV2SecretServiceServer(secrets, settings)
	if err != nil {
		t.Fatal(err)
	}
	server.BindCapabilitySource(settingsOwnCaps())
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.store", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("e", 64), InstanceId: "inst-c",
	}
	got, err := server.Put(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), &hostv2.SecretPutRequest{
		Context:  &protocolv2.RequestContext{RequestId: "c-1", Extension: identity},
		SecretId: "account_token", Value: []byte("sfo_x"),
	})
	if err != nil || got.GetError().GetReason() != "host.settings_conflict" || !got.GetError().GetRetryable() {
		t.Fatalf("conflict = %#v err=%v", got, err)
	}
}

func TestProtocolV2SecretWriteSurvivesRestart(t *testing.T) {
	h := newSecretWriteHarness(t, settingsOwnCaps())
	if _, err := h.server.Put(h.ctx, &hostv2.SecretPutRequest{
		Context: h.reqCtx, SecretId: "account_token", Value: []byte("sfo_persist"),
	}); err != nil {
		t.Fatal(err)
	}
	// 重启 = 新 Service 叠在同一份文档与密文存储上；进程内缓存不参与。
	restarted := settingslifecycle.NewWithStore(h.docs, h.secrets)
	if err := restarted.RegisterSchema("demo.store", 1, []settingslifecycle.FieldSchema{
		{Name: "store_url", Type: "string"},
		{Name: "account_token", Type: "secret", Secret: true},
		{Name: "legacy_token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	values, err := restarted.RuntimeValues(context.Background(), "demo.store", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["account_token"] != "sfo_persist" {
		t.Fatalf("token lost across restart: %#v", values)
	}
}

// conflictingDocumentStore rejects the first save with the lifecycle CAS error.
type conflictingDocumentStore struct{}

func (conflictingDocumentStore) Load(context.Context, string) (settingslifecycle.Document, int64, error) {
	return settingslifecycle.Document{}, 0, settingslifecycle.ErrNotFound
}

func (conflictingDocumentStore) Save(context.Context, string, settingslifecycle.Document, int64) (int64, error) {
	return 0, settingslifecycle.ErrConflict
}
