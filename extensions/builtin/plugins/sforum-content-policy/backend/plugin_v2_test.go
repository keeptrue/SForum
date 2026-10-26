package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestContentPolicyV2TypedHook(t *testing.T) {
	t.Setenv("SFORUM_SETTING_KEYWORDS", "promo")
	t.Setenv("SFORUM_SETTING_MODE", "tag")
	t.Setenv("SFORUM_SETTING_FORCE_TAG", "needs-review")
	server, err := newContentPolicyPluginV2()
	if err != nil {
		t.Fatal(err)
	}
	requestContext := contentPolicyHandshake(t, server)
	payload, err := structpb.NewStruct(map[string]any{
		"title": "promo title", "content": "body", "tagSlugs": []any{"existing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := contentPolicyHookRequest("topic.before_create", requestContext, payload)
	response, err := server.InvokeHook(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetError() != nil || !response.GetAccepted() || response.GetResult().GetValue().AsMap()["reason"] != "content_policy.tagged" {
		t.Fatalf("unexpected typed response: %#v", response)
	}
	if !contentPolicyDocumentMatches(response.GetResult(), contentPolicyHookResultSchema) ||
		!contentPolicyDocumentMatches(response.GetPatch(), contentPolicyHookPatchSchema) {
		t.Fatalf("response schemas do not match manifest contract: %#v", response)
	}
	got := response.GetPatch().GetValue().AsMap()["tagSlugs"]
	want := []any{"existing", "needs-review"}
	if !jsonEqual(got, want) {
		t.Fatalf("patch tagSlugs = %#v, want %#v", got, want)
	}
}

func TestContentPolicyV2BusinessRejectionUsesTypedResult(t *testing.T) {
	t.Setenv("SFORUM_SETTING_KEYWORDS", "blocked")
	t.Setenv("SFORUM_SETTING_MODE", "reject")
	server, err := newContentPolicyPluginV2()
	if err != nil {
		t.Fatal(err)
	}
	requestContext := contentPolicyHandshake(t, server)
	payload, err := structpb.NewStruct(map[string]any{"content": "blocked reply"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.InvokeHook(context.Background(), contentPolicyHookRequest("comment.before_create", requestContext, payload))
	if err != nil {
		t.Fatal(err)
	}
	if response.GetAccepted() || response.GetError() != nil || !contentPolicyDocumentMatches(response.GetResult(), contentPolicyHookResultSchema) {
		t.Fatalf("business rejection must be a typed result: %#v", response)
	}
	result := response.GetResult().GetValue().AsMap()
	if result["reason"] != "content_policy.keyword_blocked" || response.GetPatch() != nil {
		t.Fatalf("unexpected business rejection: %#v", response)
	}
}

func TestContentPolicyV2RejectsMismatchedHookContractsWithTypedErrors(t *testing.T) {
	t.Setenv("SFORUM_SETTING_KEYWORDS", "promo")
	t.Setenv("SFORUM_SETTING_MODE", "tag")
	t.Setenv("SFORUM_SETTING_FORCE_TAG", "needs-review")
	server, err := newContentPolicyPluginV2()
	if err != nil {
		t.Fatal(err)
	}
	requestContext := contentPolicyHandshake(t, server)
	payload, err := structpb.NewStruct(map[string]any{"title": "promo title"})
	if err != nil {
		t.Fatal(err)
	}
	valid := contentPolicyHookRequest("topic.before_create", requestContext, payload)
	tests := []struct {
		name   string
		change func(*pluginwire.HookRequest)
		code   protocolwire.ErrorCode
		reason string
	}{
		{"hook id", func(request *pluginwire.HookRequest) { request.HookId = "sforum.content-policy.event.wrong" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_id_mismatch"},
		{"hook kind", func(request *pluginwire.HookRequest) { request.HookKind = "observe" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_kind_mismatch"},
		{"contract", func(request *pluginwire.HookRequest) {
			request.ContractVersion = "sforum.content-policy.event.topic-before-create@2"
		}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_contract_mismatch"},
		{"schema id", func(request *pluginwire.HookRequest) { request.Payload.SchemaId = "sforum.content-policy.wrong" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_input_schema_mismatch"},
		{"schema version", func(request *pluginwire.HookRequest) { request.Payload.SchemaVersion = "2" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_input_schema_mismatch"},
		{"missing payload", func(request *pluginwire.HookRequest) { request.Payload = nil }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "content_policy.hook_input_schema_mismatch"},
		{"unknown hook", func(request *pluginwire.HookRequest) { request.HookName = "topic.unknown" }, protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, "content_policy.hook_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := proto.Clone(valid).(*pluginwire.HookRequest)
			test.change(request)
			response, err := server.InvokeHook(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.GetAccepted() || response.GetError().GetCode() != test.code || response.GetError().GetReason() != test.reason {
				t.Fatalf("typed failure = %#v", response)
			}
			if response.GetResult() != nil || response.GetPatch() != nil {
				t.Fatalf("failed hook leaked typed output: %#v", response)
			}
		})
	}
}

func TestContentPolicyV2RejectsUnauthorizedPatchWithTypedError(t *testing.T) {
	t.Setenv("SFORUM_SETTING_KEYWORDS", "promo")
	t.Setenv("SFORUM_SETTING_MODE", "tag")
	t.Setenv("SFORUM_SETTING_FORCE_TAG", "needs-review")
	server, err := newContentPolicyPluginV2()
	if err != nil {
		t.Fatal(err)
	}
	requestContext := contentPolicyHandshake(t, server)
	payload, err := structpb.NewStruct(map[string]any{"title": "promo title"})
	if err != nil {
		t.Fatal(err)
	}
	request := contentPolicyHookRequest("topic.before_create", requestContext, payload)
	request.MutableFields = nil
	response, err := server.InvokeHook(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED ||
		response.GetError().GetReason() != "content_policy.hook_patch_forbidden" {
		t.Fatalf("unauthorized patch response = %#v", response)
	}
}

func TestContentPolicyV2HookDeclarationsMatchManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve fixture path")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(file)), "sforum.extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Events []extensionmanifest.ManifestEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Events) != len(contentPolicyHooks) {
		t.Fatalf("manifest events=%d runtime hooks=%d", len(document.Events), len(contentPolicyHooks))
	}
	for _, event := range document.Events {
		declaration, exists := contentPolicyHooks[event.Name]
		if !exists {
			t.Fatalf("manifest event %q has no runtime declaration", event.Name)
		}
		if event.ID != declaration.id || event.Kind != declaration.kind || event.ContractVersion != declaration.contractVersion ||
			event.InputSchema != declaration.inputSchema || event.ResultSchema != declaration.resultSchema {
			t.Fatalf("manifest event drifted: manifest=%#v runtime=%#v", event, declaration)
		}
	}
	resultID, resultVersion, ok := strings.Cut(contentPolicyHookResultSchema, "@")
	if !ok || contentPolicyHookPatchSchema != resultID+".patch@"+resultVersion {
		t.Fatalf("patch schema %q is not derived from result schema %q", contentPolicyHookPatchSchema, contentPolicyHookResultSchema)
	}
}

func TestContentPolicyV2SDKService(t *testing.T) {
	t.Setenv("SFORUM_SETTING_KEYWORDS", "promo")
	t.Setenv("SFORUM_SETTING_MODE", "tag")
	t.Setenv("SFORUM_SETTING_FORCE_TAG", "needs-review")
	server, err := newContentPolicyPluginV2()
	if err != nil {
		t.Fatal(err)
	}
	requestContext := contentPolicyHandshake(t, server)
	payload, err := structpb.NewStruct(map[string]any{
		"eventName": "topic.before_create", "title": "promo title", "tagSlugs": []any{"existing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.InvokeService(context.Background(), &pluginwire.ServiceRequest{
		Context: requestContext, ServiceId: contentPolicyServiceID, ServiceVersion: contentPolicyServiceVersion,
		Operation: "evaluate", Input: contentPolicyDocument(contentPolicyServiceRequestSchema, payload),
	})
	if err != nil || response.GetError() != nil {
		t.Fatalf("invoke SDK service: %#v, %v", response, err)
	}
	result := response.GetOutput().GetValue().AsMap()
	if result["accepted"] != true || result["reason"] != "content_policy.tagged" {
		t.Fatalf("unexpected SDK service result: %#v", result)
	}
	patch, ok := result["patch"].(map[string]any)
	if !ok || !jsonEqual(patch["tagSlugs"], []any{"existing", "needs-review"}) {
		t.Fatalf("unexpected SDK service patch: %#v", result["patch"])
	}
}

func TestContentPolicyBuiltInManifestStartsWithProtocolV2(t *testing.T) {
	packageRoot, manifest, packageDigest := buildContentPolicyV2Package(t)
	extension := extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		Manifest: manifest, PackagePath: packageRoot, PackageDigest: packageDigest,
	}
	gateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: contentPolicyTestSettings{
			"enabled": "true", "keywords": "blocked", "mode": "reject",
			"match_title": "true", "match_content": "true",
		},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("start built-in protocol v2 package: %v", err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	result := starter.InvokeHook(context.Background(), extension, extensionsruntime.HookInput{
		Name: "topic.before_create", Kind: "filter", DeliveryID: 1,
		CorrelationID: "content-policy-v2", Timeout: 2 * time.Second,
		Payload: map[string]any{"title": "a blocked title", "content": "body"},
	})
	if result.OK || result.Reason != "content_policy.keyword_blocked" {
		t.Fatalf("unexpected v2 policy decision: %#v", result)
	}
	telemetry := starter.ProtocolTelemetry(extension.ID)
	if telemetry.ProtocolVersion != 2 || telemetry.Transport != "grpc" || telemetry.Deprecated {
		t.Fatalf("unexpected protocol telemetry: %#v", telemetry)
	}
	service, err := gateway.ProtocolV2ServiceRegistry().Resolve(contentPolicyServiceID, "="+contentPolicyServiceVersion)
	if err != nil || service.Winner.ExtensionID != extension.ID {
		t.Fatalf("content-policy SDK service was not registered: %#v, %v", service, err)
	}
}

type contentPolicyTestSettings map[string]string

func (settings contentPolicyTestSettings) ListSettings(context.Context, string) (map[string]string, error) {
	return settings, nil
}

func contentPolicyHookRequest(name string, requestContext *protocolwire.RequestContext, payload *structpb.Struct) *pluginwire.HookRequest {
	declaration := contentPolicyHooks[name]
	return &pluginwire.HookRequest{
		Context: requestContext, HookId: declaration.id, HookName: declaration.name,
		HookKind: declaration.kind, ContractVersion: declaration.contractVersion,
		Payload: contentPolicyDocument(declaration.inputSchema, payload), MutableFields: []string{"tagSlugs"},
	}
}

func contentPolicyHandshake(t *testing.T, server *contentPolicyPluginV2) *protocolwire.RequestContext {
	t.Helper()
	requestContext := &protocolwire.RequestContext{
		RequestId: "request-1", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		Extension: &protocolwire.ExtensionIdentity{
			ExtensionId: "sforum.content-policy", ExtensionVersion: "1.1.0",
			ArtifactDigest: "artifact", TrustGrantId: "builtin", RuntimeEpoch: 1, InstanceId: "instance-1",
		},
	}
	response, err := server.Handshake(context.Background(), &protocolwire.HandshakeRequest{
		Context:        requestContext,
		HostProtocols:  []*protocolwire.ProtocolRange{{Protocol: "sforum.plugin", Major: 2, MinMinor: 0, MaxMinor: 0}},
		HostApiVersion: pluginv2.HostAPIVersion,
		RuntimeToken:   bytes.Repeat([]byte{0x2a}, 32),
		Limits: &protocolwire.RuntimeLimits{
			MaxReceiveBytes: 4 << 20,
			MaxSendBytes:    4 << 20,
		},
	})
	if err != nil || response.GetError() != nil {
		t.Fatalf("handshake: %#v, %v", response, err)
	}
	return requestContext
}

func buildContentPolicyV2Package(t *testing.T) (string, extensions.Manifest, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve fixture path")
	}
	sourceRoot := filepath.Dir(filepath.Dir(file))
	packageRoot := filepath.Join(t.TempDir(), "sforum-content-policy")
	if err := copyContentPolicyPackage(sourceRoot, packageRoot); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(packageRoot, "backend", "plugin")
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	command.Dir = filepath.Dir(file)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build protocol v2 binary: %v\n%s", err, output)
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	refreshContentPolicyManifestDigest(t, packageRoot, digest)
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact-digest V3 package: %v", err)
	}
	if manifest.ManifestVersion != extensionmanifest.ManifestVersionV3 || manifest.Backend.ProtocolVersion != 2 ||
		manifest.Backend.HostAPIVersion != "sforum.host@2" || manifest.Backend.Digest != digest {
		t.Fatalf("unexpected migrated manifest: %#v", manifest.Backend)
	}
	return packageRoot, manifest, digest
}

func copyContentPolicyPackage(sourceRoot, targetRoot string) error {
	for _, relative := range []string{
		"sforum.extension.json", "manifest/admin.json", "manifest/contributions.json",
		"manifest/settings.json", "manifest/langs/en-US.json", "manifest/langs/zh-CN.json",
	} {
		body, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(targetRoot, "backend"), 0o755)
}

func refreshContentPolicyManifestDigest(t *testing.T, packageRoot, digest string) {
	t.Helper()
	path := filepath.Join(packageRoot, "sforum.extension.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document["backend"].(map[string]any)["digest"] = digest
	for _, raw := range document["packageFiles"].([]any) {
		file := raw.(map[string]any)
		if file["path"] == "backend/plugin" {
			file["digest"] = digest
		}
	}
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	updated = append(updated, '\n')
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonEqual(left, right any) bool {
	leftBody, _ := json.Marshal(left)
	rightBody, _ := json.Marshal(right)
	return bytes.Equal(leftBody, rightBody)
}
