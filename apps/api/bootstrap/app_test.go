package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestAPIAddressUsesConfiguredHostAndPort(t *testing.T) {
	cfg := config.Config{HTTPHost: "127.0.0.1", HTTPPort: "8081"}

	if got := apiAddress(cfg); got != "127.0.0.1:8081" {
		t.Fatalf("expected configured address, got %q", got)
	}
}

func TestAPICloseRunsCleanupOnce(t *testing.T) {
	calls := 0
	api := &API{
		close: func() {
			calls++
		},
	}

	api.Close()
	api.Close()

	if calls != 1 {
		t.Fatalf("expected cleanup once, got %d", calls)
	}
}

func TestAPIFailuresExposeOnlyConfiguredCoordinatorChannel(t *testing.T) {
	if failures := (*API)(nil).Failures(); failures != nil {
		t.Fatalf("nil API failures = %#v", failures)
	}
	if failures := (&API{}).Failures(); failures != nil {
		t.Fatalf("API without coordinator failures = %#v", failures)
	}

	configured := make(chan error, 1)
	api := &API{failures: configured}
	if api.Failures() != configured {
		t.Fatal("API did not expose its exact coordinator failure channel")
	}
}

func TestExtensionRuntimeFactoryCanBeReplacedForBootstrapTests(t *testing.T) {
	original := newExtensionRuntimeManager
	defer func() { newExtensionRuntimeManager = original }()

	called := false
	newExtensionRuntimeManager = func(extensions.Store, extensionsruntime.HostAPIRegistrar, extensionsruntime.PluginSettings, extensionsruntime.RuntimeTrustSource, extensionsruntime.RuntimeDatabaseLeaseRegistry) extensionRuntime {
		called = true
		return fakeBootstrapExtensionRuntime{}
	}

	runtime := newExtensionRuntimeManager(nil, nil, nil, nil, nil)
	runtime.Reconcile(context.Background(), []extensions.Extension{{
		ID:     "demo.plugin",
		Type:   extensions.TypePlugin,
		Status: extensions.StatusEnabled,
	}})
	runtime.Close(context.Background())

	if !called {
		t.Fatal("expected runtime factory replacement to be called")
	}
}

func TestPageRouteTargetAdapterRequiresExactRuntimeAndReleasesMismatches(t *testing.T) {
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "plugin.page", InstanceID: "runtime-1"}
	snapshot := extensionsruntime.RuntimeInstanceSnapshot{
		Identity: identity, ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64),
		Target: extensionsruntime.RouteTarget{BaseURL: "http://127.0.0.1:44001", InstanceID: identity.InstanceID},
	}
	artifact := pages.RuntimeArtifact{
		ExtensionID: identity.ExtensionID, ExtensionVersion: snapshot.ExtensionVersion,
		PackageDigest: snapshot.ArtifactDigest, RuntimeInstanceID: identity.InstanceID,
	}
	for _, test := range []struct {
		name   string
		mutate func(*pages.RuntimeArtifact)
		want   bool
	}{
		{name: "exact", mutate: func(*pages.RuntimeArtifact) {}, want: true},
		{name: "version", mutate: func(item *pages.RuntimeArtifact) { item.ExtensionVersion = "9.9.9" }},
		{name: "digest", mutate: func(item *pages.RuntimeArtifact) { item.PackageDigest = strings.Repeat("b", 64) }},
		{name: "runtime", mutate: func(item *pages.RuntimeArtifact) { item.RuntimeInstanceID = "runtime-2" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &pageTargetRuntimeStub{snapshot: snapshot, gate: gate}
			candidate := artifact
			test.mutate(&candidate)
			target, ok := (pageRouteTargetAdapter{runtime: runtime}).AcquireRouteTarget(context.Background(), candidate)
			if ok != test.want || runtime.extensionID != identity.ExtensionID || runtime.class != extensionsruntime.RuntimeCallPage {
				t.Fatalf("target=%#v ok=%t runtime=%#v", target, ok, runtime)
			}
			if ok {
				if target.BaseURL != snapshot.Target.BaseURL || target.Release == nil {
					t.Fatalf("exact target=%#v", target)
				}
				target.Release()
			}
			if active := gate.Snapshot().ActiveTotal; active != 0 {
				t.Fatalf("admission leases after resolution=%d", active)
			}
		})
	}
}

// TestPageRouteTargetAdapterDeniesRuntimeWithoutLoopbackTarget pin 住 Loader 目标
// 解析的传输前提：exact 制品匹配但插件没有 loopback HTTP BaseURL 时必须拒绝，
// 且不得泄漏 admission lease。Protocol V2 运行时正是这种情况，所以这条分支决定
// 了 dataSource=plugin 的页面贡献在生产中的实际行为。
func TestPageRouteTargetAdapterDeniesRuntimeWithoutLoopbackTarget(t *testing.T) {
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "plugin.page", InstanceID: "runtime-v2"}
	snapshot := extensionsruntime.RuntimeInstanceSnapshot{
		Identity: identity, ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("a", 64),
		// Protocol V2 明确不回报 loopback 地址。
		Target: extensionsruntime.RouteTarget{BaseURL: "", InstanceID: identity.InstanceID},
	}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &pageTargetRuntimeStub{snapshot: snapshot, gate: gate}
	target, ok := NewPageRouteTargetSource(runtime).AcquireRouteTarget(context.Background(), pages.RuntimeArtifact{
		ExtensionID: identity.ExtensionID, ExtensionVersion: snapshot.ExtensionVersion,
		PackageDigest: snapshot.ArtifactDigest, RuntimeInstanceID: identity.InstanceID,
	})
	if ok || target.BaseURL != "" || target.Release != nil {
		t.Fatalf("target=%#v ok=%t", target, ok)
	}
	if active := gate.Snapshot().ActiveTotal; active != 0 {
		t.Fatalf("denied resolution leaked %d admission leases", active)
	}
}

func TestAPIExtensionRuntimeUsesCipherServiceSettings(t *testing.T) {
	original := newExtensionRuntimeManager
	defer func() { newExtensionRuntimeManager = original }()

	cipher, _ := crypto.NewOptionCipher(strings.Repeat("a", 64))
	enc, _ := cipher.Encrypt("api-secret")
	item := runtimeSettingsExtension("api.plugin")
	store := &bootstrapExtensionSettingsStore{
		item:     item,
		settings: map[string]string{"token": enc},
	}
	service := extensions.NewService(store, t.TempDir())
	extensions.WithCipher(cipher)(service)

	var got map[string]string
	newExtensionRuntimeManager = func(_ extensions.Store, _ extensionsruntime.HostAPIRegistrar, settings extensionsruntime.PluginSettings, _ extensionsruntime.RuntimeTrustSource, _ extensionsruntime.RuntimeDatabaseLeaseRegistry) extensionRuntime {
		var err error
		got, err = settings.ListSettings(context.Background(), item.ID)
		if err != nil {
			t.Fatalf("load API runtime settings: %v", err)
		}
		return fakeBootstrapExtensionRuntime{}
	}
	_ = bindAPIExtensionRuntime(store, nil, service, nil, nil)
	if got["token"] != "api-secret" {
		t.Fatalf("API runtime received %#v", got)
	}
}

func TestNewHumanVerifyServiceRespectsDisabledProvider(t *testing.T) {
	service, err := newHumanVerifyService(config.Config{
		HumanVerificationProvider: "disabled",
		AltchaChallengeTTL:        time.Minute,
		AltchaCost:                1,
	}, humanverify.NewMemoryStore())
	if err != nil {
		t.Fatalf("newHumanVerifyService returned error: %v", err)
	}
	if service.Enabled() {
		t.Fatal("expected disabled human verifier")
	}
}

type fakeBootstrapExtensionRuntime struct{}

type pageTargetRuntimeStub struct {
	fakeBootstrapExtensionRuntime
	snapshot    extensionsruntime.RuntimeInstanceSnapshot
	gate        *extensionsruntime.RuntimeAdmissionGate
	extensionID string
	class       extensionsruntime.RuntimeCallClass
}

func (s *pageTargetRuntimeStub) AcquireActiveRuntimeCall(
	ctx context.Context,
	extensionID string,
	class extensionsruntime.RuntimeCallClass,
) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
	s.extensionID = extensionID
	s.class = class
	lease, err := s.gate.Acquire(ctx, class)
	return s.snapshot, lease, err
}

func (fakeBootstrapExtensionRuntime) ProtocolV2ProviderBroker() (hostapi.ProtocolV2ProviderBroker, error) {
	return workerProviderBrokerStub{}, nil
}

func (fakeBootstrapExtensionRuntime) Check(context.Context, extensions.Extension) error { return nil }

func (fakeBootstrapExtensionRuntime) Start(context.Context, extensions.Extension) error { return nil }

func (fakeBootstrapExtensionRuntime) Stop(context.Context, extensions.Extension) error { return nil }

func (fakeBootstrapExtensionRuntime) Status(context.Context, extensions.Extension) extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: extensions.RuntimeStopped}
}

func (fakeBootstrapExtensionRuntime) EmitHook(context.Context, string, map[string]any) {}

func (fakeBootstrapExtensionRuntime) Emit(context.Context, appevents.Envelope) appevents.Result {
	return appevents.Result{OK: true}
}

func (fakeBootstrapExtensionRuntime) RouteTarget(string) (extensionsruntime.RouteTarget, bool) {
	return extensionsruntime.RouteTarget{}, false
}

func (fakeBootstrapExtensionRuntime) AcquireActiveRuntimeCall(context.Context, string, extensionsruntime.RuntimeCallClass) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
	return extensionsruntime.RuntimeInstanceSnapshot{}, nil, extensions.ErrRuntimeUnavailable
}

func (fakeBootstrapExtensionRuntime) AcquireRuntimeCall(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error) {
	return nil, extensions.ErrRuntimeUnavailable
}

func (fakeBootstrapExtensionRuntime) AdminSurfaceSnapshot(string) extensionsruntime.AdminSurfaceRegistrySnapshot {
	return extensionsruntime.AdminSurfaceRegistrySnapshot{}
}

func (fakeBootstrapExtensionRuntime) ResolveAdminSurface(string) (extensionsruntime.AdminSurfaceContract, error) {
	return extensionsruntime.AdminSurfaceContract{}, extensionsruntime.ErrAdminSurfaceNotFound
}

func (fakeBootstrapExtensionRuntime) InvokeAdminSurface(context.Context, extensionsruntime.AdminSurfaceInvocation) (extensionsruntime.AdminSurfaceInvocationResult, error) {
	return extensionsruntime.AdminSurfaceInvocationResult{}, extensionsruntime.ErrAdminSurfaceNotFound
}

func (fakeBootstrapExtensionRuntime) Reconcile(context.Context, []extensions.Extension) {}

func (fakeBootstrapExtensionRuntime) Close(context.Context) {}

func (fakeBootstrapExtensionRuntime) SendMail(context.Context, string, extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error) {
	return extensionsruntime.MailProviderResponse{}, nil
}

func (fakeBootstrapExtensionRuntime) StoragePutBegin(context.Context, string, extensionsruntime.StoragePutBeginRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StoragePutChunk(context.Context, string, extensionsruntime.StoragePutChunkRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageOpen(context.Context, string, extensionsruntime.StorageOpenRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageGetChunk(context.Context, string, extensionsruntime.StorageGetChunkRequest) (extensionsruntime.StorageGetChunkResponse, error) {
	return extensionsruntime.StorageGetChunkResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageClose(context.Context, string, extensionsruntime.StorageCloseRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageDelete(context.Context, string, extensionsruntime.StorageObjectRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageStat(context.Context, string, extensionsruntime.StorageStatRequest) (extensionsruntime.StorageStatResponse, error) {
	return extensionsruntime.StorageStatResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageExists(context.Context, string, extensionsruntime.StorageExistsRequest) (extensionsruntime.StorageExistsResponse, error) {
	return extensionsruntime.StorageExistsResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StoragePublicURL(context.Context, string, extensionsruntime.StoragePublicURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageSignedURL(context.Context, string, extensionsruntime.StorageSignedURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) StorageProbe(context.Context, string, extensionsruntime.StorageProbeRequest) (extensionsruntime.StorageProbeResponse, error) {
	return extensionsruntime.StorageProbeResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) SearchEngineProbe(context.Context, string, extensionsruntime.SearchEngineProbeRequest) (extensionsruntime.SearchEngineProbeResponse, error) {
	return extensionsruntime.SearchEngineProbeResponse{}, nil
}
func (fakeBootstrapExtensionRuntime) SearchEngineEnsure(context.Context, string) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (fakeBootstrapExtensionRuntime) SearchEngineIndex(context.Context, string, extensionsruntime.SearchEngineIndexRequest) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (fakeBootstrapExtensionRuntime) SearchEngineDelete(context.Context, string, extensionsruntime.SearchEngineDeleteRequest) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (fakeBootstrapExtensionRuntime) SearchEngineSearch(context.Context, string, extensionsruntime.SearchEngineSearchRequest) (extensionsruntime.SearchEngineSearchResponse, error) {
	return extensionsruntime.SearchEngineSearchResponse{}, nil
}

func TestNewHumanVerifyServiceEnablesAltchaProvider(t *testing.T) {
	service, err := newHumanVerifyService(config.Config{
		HumanVerificationProvider: "altcha",
		AltchaSecret:              "test-secret",
		AltchaChallengeTTL:        time.Minute,
		AltchaCost:                1,
	}, humanverify.NewMemoryStore())
	if err != nil {
		t.Fatalf("newHumanVerifyService returned error: %v", err)
	}
	if !service.Enabled() {
		t.Fatal("expected enabled human verifier")
	}
}
