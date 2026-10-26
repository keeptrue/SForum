package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2ProviderBrokerPluginBConsumesPluginA(t *testing.T) {
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	broker, err := extensionsruntime.NewProtocolV2ProviderBroker(manager, extensionsruntime.BoundedProviderDocumentRevalidator)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2ProviderBroker(broker); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })

	marker := filepath.Join(t.TempDir(), "provider-a-calls")
	provider := protocolV2ProviderExtension(t, "provider.a", "provider-a", marker)
	consumer := protocolV2ProviderExtension(t, "consumer.b", "provider-b", "")
	consumer.Manifest.Providers = nil
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "optional"}}
	consumer.Manifest.Hooks = []extensions.ManifestHook{{
		ID: "consumer.b.invoke", ContractVersion: "consumer.b.invoke@1", Name: "consumer.b.provider.invoke",
		Kind: "filter", Handler: "consumer.invoke", InputSchema: "consumer.b.invoke.input@1",
		ResultSchema: "consumer.b.invoke.result@1", Execution: "sync", FailurePolicy: "fail_closed",
		TimeoutMS: 1000, MutableFields: []string{"title"},
	}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}

	if got := invokeProviderConsumer(t, manager, "valid"); got != "hello@1.0.0" {
		t.Fatalf("B -> Host -> A result = %q", got)
	}
	if count := providerMarkerCount(t, marker); count != 1 {
		t.Fatalf("provider calls after valid input = %d", count)
	}
	if got := invokeProviderConsumer(t, manager, "invalid_input"); got != "host.provider_request_invalid:0" {
		t.Fatalf("invalid request reason = %q", got)
	}
	if count := providerMarkerCount(t, marker); count != 1 {
		t.Fatalf("invalid input reached Plugin A: calls=%d", count)
	}
	if got := invokeProviderConsumer(t, manager, "invalid_output"); got != "host.provider_response_invalid:1" {
		t.Fatalf("invalid output reason = %q", got)
	}
	if count := providerMarkerCount(t, marker); count != 2 {
		t.Fatalf("invalid output did not execute exactly one A call: calls=%d", count)
	}
	started := time.Now()
	if got := invokeProviderConsumer(t, manager, "timeout"); got != "host.provider_timeout:1" || time.Since(started) >= 2*time.Second {
		t.Fatalf("provider timeout = %q after %v", got, time.Since(started))
	}

	if err := manager.Stop(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if got := invokeProviderConsumer(t, manager, "valid"); got != "host.provider_not_found:0" {
		t.Fatalf("disabled provider result = %q", got)
	}

	upgraded := provider
	upgraded.Version, upgraded.Manifest.Version = "1.1.0", "1.1.0"
	upgraded.PackageDigest = strings.Repeat("b", 64)
	if err := manager.Start(context.Background(), upgraded); err != nil {
		t.Fatal(err)
	}
	if got := invokeProviderConsumer(t, manager, "valid"); got != "hello@1.1.0" {
		t.Fatalf("upgraded provider result = %q", got)
	}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if got := invokeProviderConsumer(t, manager, "valid"); got != "hello@1.0.0" {
		t.Fatalf("rolled back provider result = %q", got)
	}
}

func TestProtocolV2ProviderBrokerHelperProcess(t *testing.T) {
	switch os.Getenv("SFORUM_PLUGIN_HELPER") {
	case "provider-a":
		pluginv2sdk.Serve(&protocolV2ProviderAServer{Server: pluginv2sdk.NewServer(), marker: os.Getenv("SFORUM_PROVIDER_MARKER")})
		os.Exit(0)
	case "provider-b":
		pluginv2sdk.Serve(&protocolV2ProviderBServer{Server: pluginv2sdk.NewServer()})
		os.Exit(0)
	}
}

type protocolV2ProviderAServer struct {
	*pluginv2sdk.Server
	marker string
}

func (s *protocolV2ProviderAServer) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	if request.GetDeclarationId() != "provider.a.delivery" || request.GetSlotId() != "provider.a.delivery.slot" ||
		request.GetContractVersion() != "provider.a.delivery@1" || request.GetOperation() != extensionsruntime.VersionedProviderOperationInvoke {
		return &pluginwire.ProviderCallResponse{Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "provider.fixture_contract_invalid",
		}}, nil
	}
	file, err := os.OpenFile(s.marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString("call\n"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	input := request.GetInput().GetValue().AsMap()
	message := input["message"]
	if message == "wait" {
		<-ctx.Done()
		return nil, ctx.Err()
	} else if message == "invalid-output" {
		message = float64(42)
	} else {
		message = fmt.Sprintf("%v@%s", message, request.GetContext().GetExtension().GetExtensionVersion())
	}
	output, err := structpb.NewStruct(map[string]any{"status": "delivered", "message": message})
	if err != nil {
		return nil, err
	}
	return &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: request.GetContext().GetExtension()},
		Output:  &protocolwire.TypedDocument{SchemaId: "provider.a.delivery.response", SchemaVersion: "1", Value: output},
	}, nil
}

type protocolV2ProviderBServer struct{ *pluginv2sdk.Server }

func (s *protocolV2ProviderBServer) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	host, err := s.Host()
	if err != nil {
		return nil, err
	}
	mode, _ := request.GetPayload().GetValue().AsMap()["mode"].(string)
	message := any("hello")
	if mode == "invalid_input" {
		message = float64(42)
	} else if mode == "invalid_output" {
		message = "invalid-output"
	} else if mode == "timeout" {
		message = "wait"
	}
	input, err := structpb.NewStruct(map[string]any{"message": message})
	if err != nil {
		return nil, err
	}
	provider, err := host.Services.InvokeProvider(ctx, &hostv2.ProviderInvokeRequest{
		Context: host.RequestContext(request.GetContext()), SlotId: "provider.a.delivery",
		ContractVersion: "provider.a.delivery@1", Operation: extensionsruntime.VersionedProviderOperationInvoke,
		Input: &protocolwire.TypedDocument{SchemaId: "provider.a.delivery.request", SchemaVersion: "1", Value: input},
	})
	if err != nil {
		return nil, err
	}
	title := ""
	if provider.GetError() != nil {
		title = fmt.Sprintf("%s:%d", provider.GetError().GetReason(), provider.GetAttempts())
	} else {
		title, _ = provider.GetOutput().GetValue().AsMap()["message"].(string)
	}
	patch, err := structpb.NewStruct(map[string]any{"title": title})
	if err != nil {
		return nil, err
	}
	result, err := structpb.NewStruct(map[string]any{"reason": "", "message": ""})
	if err != nil {
		return nil, err
	}
	return &pluginwire.HookResponse{
		Context:  &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: request.GetContext().GetExtension()},
		Accepted: true,
		Result:   &protocolwire.TypedDocument{SchemaId: "consumer.b.invoke.result", SchemaVersion: "1", Value: result},
		Patch:    &protocolwire.TypedDocument{SchemaId: "consumer.b.invoke.result.patch", SchemaVersion: "1", Value: patch},
	}, nil
}

func protocolV2ProviderExtension(t *testing.T, id, mode, marker string) extensions.Extension {
	t.Helper()
	extension := protocolV2TestExtension(t, "v2")
	extension.ID, extension.Name = id, id
	extension.Manifest.ID = id
	extension.Manifest.Events = nil
	extension.Manifest.Services = nil
	extension.PackageDigest = strings.Repeat("a", 64)
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + mode
	if marker != "" {
		launcher += " SFORUM_PROVIDER_MARKER=" + shellQuote(marker)
	}
	launcher += " exec " + shellQuote(os.Args[0]) + " -test.run=TestProtocolV2ProviderBrokerHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if id == "provider.a" {
		extension.Manifest.Providers = []extensions.ManifestProvider{{
			ID: "provider.a.delivery", ContractVersion: "provider.a.delivery@1", Slot: "provider.a.delivery.slot",
			Label: "Delivery", Handler: "provider.delivery", RequestSchema: "provider.a.delivery.request@1",
			ResponseSchema: "provider.a.delivery.response@1", Fallback: "closed", TimeoutMS: 500,
		}}
		bindProviderBrokerSchemas(t, &extension)
	}
	return extension
}

func bindProviderBrokerSchemas(t *testing.T, extension *extensions.Extension) {
	t.Helper()
	writeProviderSchema(t, extension, "provider.a.delivery.request", "schemas/provider-request.json",
		`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}},"additionalProperties":false}`)
	writeProviderSchema(t, extension, "provider.a.delivery.response", "schemas/provider-response.json",
		`{"type":"object","required":["status","message"],"properties":{"status":{"const":"delivered"},"message":{"type":"string"}},"additionalProperties":false}`)
}

func writeProviderSchema(t *testing.T, extension *extensions.Extension, id, path, schema string) {
	t.Helper()
	fullPath := filepath.Join(extension.PackagePath, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(schema)
	if err := os.WriteFile(fullPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: id, Kind: "schema", Path: path, Digest: hex.EncodeToString(digest[:]), Version: "1",
	})
}

func invokeProviderConsumer(t *testing.T, manager *extensionsruntime.Manager, mode string) string {
	t.Helper()
	result := manager.InvokeVersionedHook(context.Background(), extensionsruntime.VersionedHookInvocation{
		HookID: "consumer.b.invoke", ContractVersion: "consumer.b.invoke@1", Payload: map[string]any{"mode": mode, "title": ""},
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if !result.OK {
		t.Fatalf("consumer hook %s = %#v", mode, result)
	}
	title, _ := result.Payload["title"].(string)
	return title
}

func providerMarkerCount(t *testing.T, marker string) int {
	t.Helper()
	body, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(body), "call\n")
}

var _ pluginwire.PluginRuntimeServiceServer = (*protocolV2ProviderAServer)(nil)
var _ pluginwire.PluginRuntimeServiceServer = (*protocolV2ProviderBServer)(nil)
