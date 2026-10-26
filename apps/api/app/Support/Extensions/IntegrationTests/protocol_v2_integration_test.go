package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2NegotiatesGRPCAndInvokesTypedHook(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, hostState := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	target, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatalf("start protocol v2 plugin: %v", err)
	}
	defer starter.Stop(context.Background(), extension)
	if target.BaseURL != "" {
		t.Fatalf("v2 route target must use gRPC registry, got %#v", target)
	}
	if target.InstanceID == "" {
		t.Fatalf("v2 route target must expose the exact runtime instance: %#v", target)
	}
	if gateway.BaseURL() != "" {
		t.Fatalf("v2 must not start the legacy loopback gateway: %s", gateway.BaseURL())
	}

	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", DeliveryID: 41,
		CorrelationID: "trace-41", Timeout: 2 * time.Second,
		Payload: map[string]any{"title": "before"}, PatchFields: []string{"title"},
	})
	if !result.OK || result.Patch["title"] != "after-v2" {
		t.Fatalf("unexpected v2 hook result: %#v", result)
	}
	jobs, events := hostState.snapshot()
	if len(jobs) != 1 || jobs[0] != "runtime.v2:demo.sync" {
		t.Fatalf("host jobs = %#v", jobs)
	}
	if len(events) != 1 || events[0].Metadata["via"] != "sforum.host/v2" || events[0].Action != "extension.runtime.v2.hook.completed" {
		t.Fatalf("host audit events = %#v", events)
	}
	telemetry := starter.ProtocolTelemetry(extension.ID)
	if telemetry.ProtocolVersion != 2 || telemetry.Transport != "grpc" || telemetry.Deprecated ||
		telemetry.StartCount != 1 || telemetry.CallCount != 1 || telemetry.LastCallAt == nil {
		t.Fatalf("unexpected v2 telemetry: %#v", telemetry)
	}
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.ExtensionID != extension.ID || resolved.Winner.InstanceID != target.InstanceID {
		t.Fatalf("runtime service registration = %#v, %v", resolved, err)
	}
}

func TestProtocolV2InvokesVersionedManifestHookByExactDeclaration(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	extension.Manifest.Hooks = []extensions.ManifestHook{{
		ID: "runtime.v2.hook.transform", ContractVersion: "runtime.v2.hook.transform@1",
		Name: "runtime.v2.content.transform", Kind: "filter", Handler: "runtime.v2.transform",
		InputSchema: "runtime.v2.content@1", ResultSchema: "runtime.v2.content-result@1",
		Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 1000, MutableFields: []string{"title"},
	}}
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		DeclarationID: "runtime.v2.hook.transform", Name: "runtime.v2.content.transform", Kind: "filter",
		ContractVersion: "runtime.v2.hook.transform@1", Timeout: time.Second,
		Payload: map[string]any{"title": "before"}, PatchFields: []string{"title"},
	})
	if !result.OK || result.Patch["title"] != "after-v2" {
		t.Fatalf("versioned protocol hook = %#v", result)
	}
}

func TestProtocolV2InvokesVersionedProviderByExactTypedDeclaration(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	extension.Manifest.Providers = []extensions.ManifestProvider{{
		ID: "runtime.v2.delivery", ContractVersion: "runtime.v2.delivery@1",
		Slot: "runtime.v2.delivery.slot", Label: "Delivery", Handler: "provider.delivery",
		RequestSchema: "runtime.v2.delivery.request@1", ResponseSchema: "runtime.v2.delivery.response@1",
		Fallback: "closed", TimeoutMS: 200,
	}}
	bindProtocolV2ProviderSchemas(t, &extension)
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	validations := []string{}
	result, err := manager.InvokeVersionedProvider(context.Background(), extensionsruntime.VersionedProviderInvocation{
		SlotID: "runtime.v2.delivery", ContractVersion: "runtime.v2.delivery@1", Operation: extensionsruntime.VersionedProviderOperationInvoke,
		InputSchema: "runtime.v2.delivery.request@1", Input: map[string]any{"message": "hello"},
		Revalidate: func(_ context.Context, schema string, _ map[string]any) error {
			validations = append(validations, schema)
			return nil
		},
	})
	if err != nil || result.ProviderID != "runtime.v2.delivery" || result.ExtensionID != extension.ID ||
		result.Output["status"] != "delivered" || result.Output["message"] != "hello" {
		t.Fatalf("versioned provider = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(validations, []string{"runtime.v2.delivery.request@1", "runtime.v2.delivery.response@1"}) {
		t.Fatalf("Host validations = %#v", validations)
	}
	if _, err := starter.InvokeVersionedProvider(context.Background(), extension, extensionsruntime.VersionedProviderRequest{
		DeclarationID: "runtime.v2.undeclared", Slot: "runtime.v2.delivery.slot",
		ContractVersion: "runtime.v2.delivery@1", Operation: extensionsruntime.VersionedProviderOperationInvoke,
		RequestSchema: "runtime.v2.delivery.request@1", ResponseSchema: "runtime.v2.delivery.response@1",
		Input: map[string]any{"message": "forged"},
	}); err == nil {
		t.Fatal("Protocol V2 accepted an undeclared provider identity")
	}
	started := time.Now()
	timedOut, err := manager.InvokeVersionedProvider(context.Background(), extensionsruntime.VersionedProviderInvocation{
		SlotID: "runtime.v2.delivery", ContractVersion: "runtime.v2.delivery@1",
		Operation: extensionsruntime.VersionedProviderOperationInvoke, InputSchema: "runtime.v2.delivery.request@1",
		Input:      map[string]any{"mode": "wait_for_cancel"},
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if !errors.Is(err, context.DeadlineExceeded) || timedOut.Attempts != 1 || time.Since(started) >= time.Second {
		t.Fatalf("Protocol V2 provider timeout = %#v, elapsed=%v, err=%v", timedOut, time.Since(started), err)
	}
}

func bindProtocolV2ProviderSchemas(t *testing.T, extension *extensions.Extension) {
	t.Helper()
	schemas := []struct {
		id   string
		path string
		body string
	}{
		{
			id: "runtime.v2.delivery.request", path: "schemas/delivery-request.json",
			body: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"message":{"type":"string"},"mode":{"enum":["wait_for_cancel"]}},"additionalProperties":false}`,
		},
		{
			id: "runtime.v2.delivery.response", path: "schemas/delivery-response.json",
			body: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["status","message"],"properties":{"status":{"const":"delivered"},"message":{"type":"string"}},"additionalProperties":false}`,
		},
	}
	for _, schema := range schemas {
		fullPath := filepath.Join(extension.PackagePath, filepath.FromSlash(schema.path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte(schema.body)
		if err := os.WriteFile(fullPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
			ID: schema.id, Kind: "schema", Path: schema.path, Digest: hex.EncodeToString(digest[:]), Version: "1",
		})
	}
}

func TestProtocolV2HostBrokerRejectsInvalidCalls(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("start protocol v2 plugin: %v", err)
	}
	defer starter.Stop(context.Background(), extension)

	for _, mode := range []string{"stale_identity", "forged_authority", "expired_deadline", "cancelled", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
				Name: "topic.before_create", Kind: "filter", DeliveryID: 41,
				CorrelationID: "trace-" + mode, Timeout: 2 * time.Second,
				Payload: map[string]any{"title": "before", "mode": mode}, PatchFields: []string{"title"},
			})
			if !result.OK {
				t.Fatalf("expected host rejection to be observed by plugin, got %#v", result)
			}
		})
	}
	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", DeliveryID: 41,
		CorrelationID: "trace-business-reject", Timeout: 2 * time.Second,
		Payload: map[string]any{"title": "before", "mode": "business_reject"}, PatchFields: []string{"title"},
	})
	if result.OK || result.Reason != "content.rejected" || result.Message != "Rejected by policy." {
		t.Fatalf("typed business rejection was not preserved: %#v", result)
	}
}

func TestProtocolV2HookContractFailsClosed(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	undeclared := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "action", Timeout: time.Second,
	})
	if undeclared.OK || undeclared.Reason != "extension.hook_failed" || !strings.Contains(undeclared.Message, "not declared") {
		t.Fatalf("undeclared hook contract = %#v", undeclared)
	}
	invalidResult := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", Timeout: time.Second,
		Payload: map[string]any{"mode": "invalid_result_schema"},
	})
	if invalidResult.OK || invalidResult.Reason != "extension.hook_failed" || !strings.Contains(invalidResult.Message, "hook result") {
		t.Fatalf("invalid hook result contract = %#v", invalidResult)
	}
}

func TestProtocolV2HookPropagatesCallerCancellation(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	readyMarker := filepath.Join(t.TempDir(), "ready")
	cancelledMarker := filepath.Join(filepath.Dir(readyMarker), "cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan extensionsruntime.HookResult, 1)
	go func() {
		resultCh <- starter.InvokeHook(ctx, extension, extensionsruntime.HookInput{
			Name: "topic.before_create", Kind: "filter", Timeout: 5 * time.Second,
			Payload: map[string]any{"mode": "wait_for_cancel", "ready_marker": readyMarker, "cancelled_marker": cancelledMarker},
		})
	}()
	awaitProtocolV2Marker(t, readyMarker, 2*time.Second)
	cancel()
	select {
	case result := <-resultCh:
		if result.OK || result.Reason != "extension.hook_timeout" {
			t.Fatalf("cancelled hook result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled hook did not return promptly")
	}
	awaitProtocolV2Marker(t, cancelledMarker, time.Second)
}

func TestProtocolV2HostBrokerRebindsAfterRestart(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	first, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := starter.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0"); !errors.Is(err, hostapi.ErrServiceNotFound) {
		t.Fatalf("stopped runtime remained discoverable: %v", err)
	}
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("restart protocol v2 plugin: %v", err)
	}
	second, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || second.Winner.InstanceID == first.Winner.InstanceID {
		t.Fatalf("restart did not replace instance: first=%#v second=%#v err=%v", first.Winner, second.Winner, err)
	}
	defer starter.Stop(context.Background(), extension)
	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", DeliveryID: 42,
		CorrelationID: "trace-restart", Timeout: 2 * time.Second,
		Payload: map[string]any{"title": "before"}, PatchFields: []string{"title"},
	})
	if !result.OK || starter.ProtocolTelemetry(extension.ID).StartCount != 2 {
		t.Fatalf("restart result = %#v, telemetry = %#v", result, starter.ProtocolTelemetry(extension.ID))
	}
}

func TestProtocolV2CrashReapsServiceRegistration(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", Timeout: time.Second,
		Payload: map[string]any{"mode": "crash"},
	})
	if result.OK {
		t.Fatalf("crashed runtime returned success: %#v", result)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
		if errors.Is(err, hostapi.ErrServiceNotFound) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("crashed runtime remained discoverable")
}

func TestProtocolV2ConcurrentStartsKeepCurrentServiceInstance(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := starter.Start(context.Background(), extension)
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent start: %v", err)
		}
	}
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID == "" {
		t.Fatalf("current service instance = %#v, %v", resolved.Winner, err)
	}
	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", Timeout: time.Second,
		Payload: map[string]any{"title": "still-running"},
	})
	if !result.OK {
		t.Fatalf("registry points at a stale runtime: %#v", result)
	}
}

func TestProtocolSelectionNeverSilentlyDowngrades(t *testing.T) {
	tests := []struct {
		name            string
		manifestVersion int
		helperMode      string
		hostAPIVersion  string
	}{
		{"v2 manifest with v1 binary", 2, "v1", "sforum.host@2"},
		{"v1 manifest with v2 binary", 1, "v2", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := protocolV2TestExtension(t, test.helperMode)
			extension.Manifest.Backend.ProtocolVersion = test.manifestVersion
			extension.Manifest.Backend.HostAPIVersion = test.hostAPIVersion
			starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
				Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
			})
			if _, err := starter.Start(context.Background(), extension); err == nil {
				t.Fatal("expected exact protocol negotiation failure")
			}
		})
	}
}

func TestProtocolV2RejectsUploadedArtifactWithoutLiveGrant(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{})
	_, err := starter.Start(context.Background(), extension)
	if !errors.Is(err, extensions.ErrTrustGrantNotFound) {
		t.Fatalf("missing live grant error = %v", err)
	}
}

func TestProtocolV2HelperProcess(t *testing.T) {
	switch os.Getenv("SFORUM_PLUGIN_HELPER") {
	case "protocol-v2", "protocol-v2-readiness-fail":
		registry, err := pluginv2sdk.NewServiceRegistry(pluginv2sdk.ServiceDefinition{
			ServiceID: "runtime.v2.service.echo", Version: "1.0.0",
			RequestSchemaID: "runtime.v2.service.echo.request@1", ResponseSchemaID: "runtime.v2.service.echo.response@1",
			Operations: []pluginv2sdk.ServiceOperation{{Name: "echo", Unary: protocolV2EchoService}},
		})
		if err != nil {
			panic(err)
		}
		pluginv2sdk.Serve(&protocolV2Helper{
			Server:        pluginv2sdk.NewServer().WithServiceRegistry(registry),
			readinessFail: os.Getenv("SFORUM_PLUGIN_HELPER") == "protocol-v2-readiness-fail",
		})
		os.Exit(0)
	case "protocol-v2-no-services":
		pluginv2sdk.Serve(&protocolV2Helper{Server: pluginv2sdk.NewServer()})
		os.Exit(0)
	}
}

type protocolV2Helper struct {
	*pluginv2sdk.Server
	readinessFail bool
}

func (s *protocolV2Helper) Health(ctx context.Context, request *protocolwire.HealthRequest) (*protocolwire.HealthResponse, error) {
	response, err := s.Server.Health(ctx, request)
	if err != nil || !response.GetHealthy() {
		return response, err
	}
	expectation := os.Getenv("SFORUM_PLUGIN_EXPECT_DATABASE")
	if expectation == "" {
		return response, nil
	}
	for _, key := range []string{
		"SFORUM_DATABASE_URL", "SFORUM_DATABASE_LEASE_ID", "SFORUM_DATABASE_LEASE_EXPIRES_AT",
		"SFORUM_DATABASE_GRANTS", "SFORUM_DATABASE_SCHEMA", "SFORUM_DATABASE_SEARCH_PATH",
	} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return nil, errors.New("missing exact database lease environment: " + key)
		}
	}
	connectionURL := os.Getenv("SFORUM_DATABASE_URL")
	if expectation == "lease" && (!strings.Contains(connectionURL, "lease_role:lease_secret@") || strings.Contains(connectionURL, "host_secret")) {
		return nil, errors.New("database lease URL contains the wrong authority")
	}
	if expectation != "connect" {
		return response, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := pgx.Connect(connectCtx, connectionURL)
	if err != nil {
		return nil, err
	}
	defer connection.Close(context.Background())
	var currentUser, searchPath string
	if err := connection.QueryRow(connectCtx, `SELECT current_user, current_setting('search_path')`).Scan(&currentUser, &searchPath); err != nil {
		return nil, err
	}
	if currentUser == "" || !strings.Contains(searchPath, os.Getenv("SFORUM_DATABASE_SCHEMA")) {
		return nil, errors.New("database lease role or search_path is not exact")
	}
	return response, nil
}

func (s *protocolV2Helper) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	requestContext := request.GetContext()
	if request.GetDeclarationId() != "runtime.v2.delivery" || request.GetSlotId() != "runtime.v2.delivery.slot" || request.GetContractVersion() != "runtime.v2.delivery@1" ||
		request.GetOperation() != extensionsruntime.VersionedProviderOperationInvoke ||
		request.GetInput().GetSchemaId() != "runtime.v2.delivery.request" || request.GetInput().GetSchemaVersion() != "1" {
		return &pluginwire.ProviderCallResponse{Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "fixture.provider_contract_invalid",
		}}, nil
	}
	if request.GetInput().GetValue().AsMap()["mode"] == "wait_for_cancel" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	input := request.GetInput().GetValue().AsMap()
	output, err := structpb.NewStruct(map[string]any{"status": "delivered", "message": input["message"]})
	if err != nil {
		return nil, err
	}
	return &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{RequestId: requestContext.GetRequestId(), Extension: requestContext.GetExtension()},
		Output:  &protocolwire.TypedDocument{SchemaId: "runtime.v2.delivery.response", SchemaVersion: "1", Value: output},
	}, nil
}

func (s *protocolV2Helper) Readiness(ctx context.Context, request *protocolwire.ReadinessRequest) (*protocolwire.ReadinessResponse, error) {
	if !s.readinessFail {
		return s.Server.Readiness(ctx, request)
	}
	return &protocolwire.ReadinessResponse{
		Context: &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: request.GetContext().GetExtension()},
		Ready:   false,
	}, nil
}

func (s *protocolV2Helper) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	requestContext := request.GetContext()
	identity := requestContext.GetExtension()
	if identity.GetExtensionId() != "runtime.v2" || identity.GetArtifactDigest() == "" ||
		identity.GetTrustGrantId() != "41" || requestContext.GetLocale() != "und" || requestContext.GetDeadline() == nil {
		return &pluginwire.HookResponse{Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "fixture.context_invalid", Message: "typed runtime context is incomplete",
		}}, nil
	}
	versionedHook := request.GetHookId() == "runtime.v2.hook.transform"
	wantID, wantName, wantContract, wantInput, wantResult :=
		"runtime.v2.event.topic-before-create", "topic.before_create", "runtime.v2.event.topic-before-create@1", "runtime.v2.hook-input", "runtime.v2.hook-result"
	if versionedHook {
		wantID, wantName, wantContract, wantInput, wantResult =
			"runtime.v2.hook.transform", "runtime.v2.content.transform", "runtime.v2.hook.transform@1", "runtime.v2.content", "runtime.v2.content-result"
	}
	if request.GetHookId() != wantID || request.GetHookName() != wantName ||
		request.GetHookKind() != "filter" || request.GetContractVersion() != wantContract ||
		request.GetPayload().GetSchemaId() != wantInput || request.GetPayload().GetSchemaVersion() != "1" ||
		!hasProtocolV2Authority(requestContext, capabilities.HostAPI) {
		return &pluginwire.HookResponse{Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Reason: "fixture.authority_invalid", Message: "authority or payload contract is incomplete",
		}}, nil
	}
	mode, _ := request.GetPayload().GetValue().AsMap()["mode"].(string)
	if mode == "crash" {
		os.Exit(23)
	}
	if mode == "wait_for_cancel" {
		values := request.GetPayload().GetValue().AsMap()
		readyMarker, _ := values["ready_marker"].(string)
		cancelledMarker, _ := values["cancelled_marker"].(string)
		if err := os.WriteFile(readyMarker, []byte("ready"), 0o600); err != nil {
			return nil, err
		}
		<-ctx.Done()
		if err := os.WriteFile(cancelledMarker, []byte(ctx.Err().Error()), 0o600); err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	if mode == "invalid_result_schema" {
		return &pluginwire.HookResponse{
			Context:  &protocolwire.ResponseContext{RequestId: requestContext.GetRequestId(), Extension: identity},
			Accepted: true,
			Result:   &protocolwire.TypedDocument{SchemaId: "wrong.result", SchemaVersion: "1"},
		}, nil
	}
	if mode == "business_reject" {
		value, err := structpb.NewStruct(map[string]any{"reason": "content.rejected", "message": "Rejected by policy."})
		if err != nil {
			return nil, err
		}
		return &pluginwire.HookResponse{
			Context: &protocolwire.ResponseContext{RequestId: requestContext.GetRequestId(), Extension: identity},
			Result:  &protocolwire.TypedDocument{SchemaId: "runtime.v2.hook-result", SchemaVersion: "1", Value: value},
		}, nil
	}
	if mode == "" {
		if err := s.invokeHostCallbacks(ctx, request); err != nil {
			return nil, err
		}
	} else if err := s.observeHostRejection(ctx, request, mode); err != nil {
		return nil, err
	}
	patch, err := structpb.NewStruct(map[string]any{"title": "after-v2"})
	if err != nil {
		return nil, err
	}
	result, err := structpb.NewStruct(map[string]any{"reason": "", "message": ""})
	if err != nil {
		return nil, err
	}
	return &pluginwire.HookResponse{
		Context:  &protocolwire.ResponseContext{RequestId: requestContext.GetRequestId(), Extension: identity},
		Accepted: true,
		Result:   &protocolwire.TypedDocument{SchemaId: wantResult, SchemaVersion: "1", Value: result},
		Patch:    &protocolwire.TypedDocument{SchemaId: wantResult + ".patch", SchemaVersion: "1", Value: patch},
	}, nil
}

func awaitProtocolV2Marker(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat protocol v2 marker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("protocol v2 marker %s was not written", filepath.Base(path))
}

func protocolV2EchoService(_ context.Context, _ *pluginv2sdk.ServiceCall) (*protocolwire.TypedDocument, error) {
	return &protocolwire.TypedDocument{SchemaId: "runtime.v2.service.echo.response", SchemaVersion: "1"}, nil
}

func hasProtocolV2Authority(ctx *protocolwire.RequestContext, key string) bool {
	for _, grant := range ctx.GetGrantedAuthority() {
		if grant.GetKey() == key {
			return true
		}
	}
	return false
}

type staticRuntimeTrust struct {
	identity extensions.RuntimeTrustIdentity
}

func (s staticRuntimeTrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return s.identity, nil
}

func protocolV2TestExtension(t *testing.T, helperMode string) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.v2", "1.0.0")
	filesRoot := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=protocol-" + helperMode + " exec " + shellQuote(os.Args[0]) + " -test.run=TestProtocolV2HelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(filesRoot, "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: "runtime.v2", Name: "Runtime V2", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("a", 64), PackagePath: packageRoot,
		CapabilityGrants: []extensions.CapabilityGrant{
			{Key: capabilities.HostAPI, Risk: capabilities.RiskLow},
			{Key: capabilities.SettingsOwn, Risk: capabilities.RiskLow},
			{Key: capabilities.PermissionsCheck, Risk: capabilities.RiskLow},
			{Key: capabilities.UsersRead, Risk: capabilities.RiskMedium},
			{Key: capabilities.JobsEnqueue, Risk: capabilities.RiskMedium},
			{Key: capabilities.AuditAppend, Risk: capabilities.RiskMedium},
		},
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "runtime.v2", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Events: []extensions.ManifestEvent{{
				ID: "runtime.v2.event.topic-before-create", ContractVersion: "runtime.v2.event.topic-before-create@1",
				Name: "topic.before_create", Kind: "filter", Handler: "runtime.v2.filter",
				InputSchema: "runtime.v2.hook-input@1", ResultSchema: "runtime.v2.hook-result@1",
			}},
			Services: []extensions.ManifestService{{
				ID: "runtime.v2.service.echo", ContractVersion: "runtime.v2.service.echo@1", Action: "add",
				Handler: "runtime.v2.service.echo", RequestSchema: "runtime.v2.service.echo.request@1",
				ResponseSchema: "runtime.v2.service.echo.response@1",
			}},
		},
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
