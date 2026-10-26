package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type adminSurfaceRuntimeServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	request *pluginwire.HookRequest
}

func (s *adminSurfaceRuntimeServer) InvokeHook(
	_ context.Context,
	request *pluginwire.HookRequest,
) (*pluginwire.HookResponse, error) {
	s.request = request
	result, err := protocolV2Document("demo.admin.surface.result", "1", map[string]any{"title": "Rendered"})
	if err != nil {
		return nil, err
	}
	return &pluginwire.HookResponse{Accepted: true, Result: result}, nil
}

func TestProtocolV2AdminSurfaceCarriesExactTypedContract(t *testing.T) {
	server := &adminSurfaceRuntimeServer{}
	client := adminSurfaceProtocolClient(t, server)
	client.hostCommands = true
	issuer := &recordingProtocolV2ActorDelegationIssuer{grants: []hostapi.ProtocolV2ActorDelegationGrant{{
		CommandID: "sforum.admin.write", CommandVersion: "1", IdempotencyKey: "admin-request-42", Token: "admin-token",
	}}}
	client.delegations = issuer
	contract := adminSurfaceRuntimeContract()
	output, err := client.invokeAdminSurface(
		context.Background(), contract, map[string]any{"title": "SForum"},
		NewProtocolV2RouteActor(42, true, map[string]bool{"admin.write": true}), "admin-request-42",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := server.request
	if request.GetHookId() != contract.ID || request.GetContractVersion() != contract.ContractVersion ||
		request.GetHookName() != contract.Handler || request.GetHookKind() != "admin_surface" ||
		request.GetPayload().GetSchemaId() != "demo.admin.surface.props" ||
		request.GetPayload().GetSchemaVersion() != "1" || request.GetPayload().GetValue().AsMap()["title"] != "SForum" ||
		request.GetContext().GetActor().GetUserId() != 42 || request.GetContext().GetIdempotencyKey() != "admin-request-42" ||
		len(request.GetContext().GetHostCommandDelegations()) != 1 ||
		request.GetContext().GetHostCommandDelegations()[0].GetToken() != "admin-token" ||
		issuer.calls != 1 || issuer.request.Runtime.GetInstanceId() != contract.InstanceID ||
		!reflect.DeepEqual(issuer.request.PermissionKeys, []string{"admin.write"}) ||
		output["title"] != "Rendered" {
		t.Fatalf("request=%#v output=%#v", request, output)
	}
	stale := contract
	stale.Handler = "admin.changed"
	if _, err := client.invokeAdminSurface(context.Background(), stale, map[string]any{}, nil, ""); !errors.Is(err, ErrAdminSurfaceRuntimeStale) {
		t.Fatalf("stale surface = %v", err)
	}
}

type adminSurfaceStarterStub struct {
	instanceID string
	calls      int
	input      map[string]any
	contract   AdminSurfaceContract
	actor      *ProtocolV2InvocationActor
	key        string
}

type blockingAdminSurfaceStarter struct {
	instances []string
	next      int
	entered   chan RuntimeInstanceIdentity
	release   chan struct{}
}

func (s *blockingAdminSurfaceStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	if s.next >= len(s.instances) {
		return RouteTarget{}, errors.New("admin surface test starter exhausted")
	}
	instanceID := s.instances[s.next]
	s.next++
	return RouteTarget{InstanceID: instanceID}, nil
}

func (*blockingAdminSurfaceStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *blockingAdminSurfaceStarter) InvokeAdminSurface(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	_ AdminSurfaceContract,
	_ map[string]any,
	_ *ProtocolV2InvocationActor,
	_ string,
) (map[string]any, error) {
	s.entered <- identity
	if identity.InstanceID == "runtime-old" {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return map[string]any{"title": "Rendered"}, nil
}

func (s *adminSurfaceStarterStub) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{InstanceID: s.instanceID}, nil
}

func (*adminSurfaceStarterStub) Stop(context.Context, extensions.Extension) error { return nil }

func (s *adminSurfaceStarterStub) InvokeAdminSurface(
	_ context.Context,
	_ RuntimeInstanceIdentity,
	contract AdminSurfaceContract,
	input map[string]any,
	actor *ProtocolV2InvocationActor,
	idempotencyKey string,
) (map[string]any, error) {
	s.calls++
	s.contract = contract
	s.input = input
	s.actor = actor
	s.key = idempotencyKey
	input["title"] = "mutated by plugin"
	return map[string]any{"title": "Rendered"}, nil
}

func TestManagerAdminSurfaceValidatesDocumentsAndExactRuntimeAdmission(t *testing.T) {
	starter := &adminSurfaceStarterStub{instanceID: "runtime-admin"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.admin", []extensions.ManifestAdminSurface{{
		ID: "demo.admin.surface.form", ContractVersion: "demo.admin.surface.form@1",
		Kind: "form", Action: "add", Label: "Form", Handler: "admin.form",
		Schema: "demo.admin.surface.props@1",
	}})
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].Schema, "schemas/admin-surface.json",
		`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}},"additionalProperties":false}`)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	contract, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"title": "SForum"}
	result, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
		ExpectedContract: contract, ContractVersion: extension.Manifest.AdminSurfaces[0].ContractVersion, Input: input,
		Actor: NewProtocolV2RouteActor(42, true, map[string]bool{"admin.write": true}), IdempotencyKey: "admin-request-42",
	})
	if err != nil || result.Output["title"] != "Rendered" || starter.calls != 1 ||
		!reflect.DeepEqual(input, map[string]any{"title": "SForum"}) || starter.contract.InstanceID != starter.instanceID {
		t.Fatalf("result=%#v calls=%d input=%#v contract=%#v err=%v", result, starter.calls, input, starter.contract, err)
	}
	if starter.actor == nil || starter.actor.UserID != 42 || starter.key != "admin-request-42" {
		t.Fatalf("actor=%#v key=%q", starter.actor, starter.key)
	}
	if _, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
		ExpectedContract: contract, ContractVersion: extension.Manifest.AdminSurfaces[0].ContractVersion,
		Input: map[string]any{"title": 42}, Actor: NewProtocolV2RouteActor(42, true, nil), IdempotencyKey: "invalid-props-42",
	}); !errors.Is(err, ErrAdminSurfaceRegistryInvalid) || starter.calls != 1 {
		t.Fatalf("invalid props calls=%d err=%v", starter.calls, err)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: starter.instanceID}
	if _, err := manager.BeginDrain(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
		ExpectedContract: contract, ContractVersion: extension.Manifest.AdminSurfaces[0].ContractVersion,
		Input: map[string]any{"title": "SForum"}, Actor: NewProtocolV2RouteActor(42, true, nil), IdempotencyKey: "draining-42",
	}); !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("draining surface = %v", err)
	}
}

func TestManagerAdminSurfaceRejectsPublicationSwapAfterResolve(t *testing.T) {
	starter := &adminSurfaceStarterStub{instanceID: "runtime-old"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.swap", []extensions.ManifestAdminSurface{{
		ID: "demo.swap.surface.form", ContractVersion: "demo.swap.surface.form@1",
		Kind: "form", Action: "add", Label: "Form", Handler: "admin.form",
		Schema: "demo.swap.surface.props@1",
	}})
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].Schema, "schemas/admin-surface.json",
		`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}},"additionalProperties":false}`)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	expected, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	replacement := extension
	replacement.Version = "1.1.0"
	replacement.Manifest.Version = replacement.Version
	replacement.PackageDigest = strings.Repeat("b", 64)
	starter.instanceID = "runtime-new"
	if err := manager.Start(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
		ExpectedContract: expected, ContractVersion: expected.ContractVersion,
		Input: map[string]any{"title": "SForum"},
	}); !errors.Is(err, ErrAdminSurfaceRuntimeStale) || starter.calls != 0 {
		t.Fatalf("publication swap calls=%d err=%v", starter.calls, err)
	}
}

func TestManagerAdminSurfaceKeepsFrozenValidatorAcrossInflightPublicationSwap(t *testing.T) {
	starter := &blockingAdminSurfaceStarter{
		instances: []string{"runtime-old", "runtime-new"},
		entered:   make(chan RuntimeInstanceIdentity, 2),
		release:   make(chan struct{}),
	}
	defer func() {
		select {
		case <-starter.release:
		default:
			close(starter.release)
		}
	}()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.inflight", []extensions.ManifestAdminSurface{{
		ID: "demo.inflight.surface.action", ContractVersion: "demo.inflight.surface.action@1",
		Kind: "row_action", Action: "add", Label: "Action", Handler: "admin.action",
		Schema: "demo.inflight.surface.props@1",
	}})
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].Schema, "schemas/admin-surface.json",
		`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}},"additionalProperties":false}`)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	expected, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	type invocationResult struct {
		result AdminSurfaceInvocationResult
		err    error
	}
	done := make(chan invocationResult, 1)
	go func() {
		result, invokeErr := manager.InvokeAdminSurface(t.Context(), AdminSurfaceInvocation{
			ExpectedContract: expected, ContractVersion: expected.ContractVersion,
			Input: map[string]any{"title": "SForum"}, Actor: NewProtocolV2RouteActor(42, true, nil), IdempotencyKey: "inflight-42",
		})
		done <- invocationResult{result: result, err: invokeErr}
	}()

	select {
	case identity := <-starter.entered:
		if identity.InstanceID != "runtime-old" {
			t.Fatalf("admitted runtime = %#v", identity)
		}
	case <-time.After(time.Second):
		t.Fatal("old runtime was not invoked")
	}
	replacement := extension
	replacement.Version = "1.1.0"
	replacement.Manifest.Version = replacement.Version
	replacement.PackageDigest = strings.Repeat("b", 64)
	if err := manager.Start(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	close(starter.release)

	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Output["title"] != "Rendered" ||
			outcome.result.Contract.ArtifactDigest != expected.ArtifactDigest || outcome.result.Contract.InstanceID != "runtime-old" {
			t.Fatalf("in-flight result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("old runtime invocation did not finish")
	}
}

func TestManagerAdminSurfaceCatalogHidesDrainedRuntimeWithoutMutatingRegistry(t *testing.T) {
	starter := &adminSurfaceStarterStub{instanceID: "runtime-visible"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.visible", []extensions.ManifestAdminSurface{{
		ID: "demo.visible.surface.notice", ContractVersion: "demo.visible.surface.notice@1",
		Kind: "notice", Action: "add", Label: "Notice", Handler: "admin.notice",
	}})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.AdminSurfaceSnapshot("notice"); len(snapshot.Surfaces) != 1 ||
		snapshot.Surfaces[0].ID != extension.Manifest.AdminSurfaces[0].ID {
		t.Fatalf("active catalog = %#v", snapshot)
	}
	if _, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID); err != nil {
		t.Fatalf("resolve active surface: %v", err)
	}

	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: starter.instanceID}
	if _, err := manager.BeginDrain(identity); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.AdminSurfaceSnapshot(""); len(snapshot.Surfaces) != 0 {
		t.Fatalf("drained catalog = %#v", snapshot)
	}
	if _, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID); !errors.Is(err, ErrAdminSurfaceNotFound) {
		t.Fatalf("resolve drained surface = %v", err)
	}
	if contract, err := manager.HookBus().AdminSurfaces().Resolve(extension.Manifest.AdminSurfaces[0].ID); err != nil ||
		contract.InstanceID != starter.instanceID {
		t.Fatalf("immutable rollback descriptor = %#v, %v", contract, err)
	}
	if _, err := manager.ResumeRuntimeInstance(identity); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.AdminSurfaceSnapshot(""); len(snapshot.Surfaces) != 1 {
		t.Fatalf("resumed catalog = %#v", snapshot)
	}
}

func TestManagerInvokesEveryTaskbookAdminSurfaceKind(t *testing.T) {
	kinds := []string{
		"navigation", "dashboard", "list_column", "list_filter", "row_action", "bulk_action",
		"form", "notice", "editor_panel", "detail_region", "importer", "exporter",
	}
	starter := &adminSurfaceStarterStub{instanceID: "runtime-all-kinds"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.admin.all", nil)
	for _, kind := range kinds {
		extension.Manifest.AdminSurfaces = append(extension.Manifest.AdminSurfaces, extensions.ManifestAdminSurface{
			ID: "demo.admin.all.surface." + kind, ContractVersion: "demo.admin.all.surface." + kind + "@1",
			Kind: kind, Action: "add", Label: kind, Handler: "admin." + kind,
			Schema: "demo.admin.all.surface.props@1",
		})
	}
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, "demo.admin.all.surface.props@1", "schemas/admin-surface.json",
		`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}},"additionalProperties":false}`)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	for _, declaration := range extension.Manifest.AdminSurfaces {
		contract, err := manager.ResolveAdminSurface(declaration.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
			ExpectedContract: contract, ContractVersion: declaration.ContractVersion, Input: map[string]any{"title": declaration.Kind},
			Actor: NewProtocolV2RouteActor(42, true, nil), IdempotencyKey: "all-kinds-" + declaration.Kind,
		})
		if err != nil || result.Contract.Kind != declaration.Kind || result.Output["title"] != "Rendered" {
			t.Fatalf("kind %s result=%#v err=%v", declaration.Kind, result, err)
		}
	}
	if starter.calls != len(kinds) {
		t.Fatalf("calls = %d, want %d", starter.calls, len(kinds))
	}
}

func TestManagerRejectsUntypedAdminSurfaceHandler(t *testing.T) {
	starter := &adminSurfaceStarterStub{instanceID: "runtime-untyped"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.untyped", []extensions.ManifestAdminSurface{{
		ID: "demo.untyped.surface.notice", ContractVersion: "demo.untyped.surface.notice@1",
		Kind: "notice", Action: "add", Label: "Notice", Handler: "admin.notice",
	}})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	contract, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InvokeAdminSurface(context.Background(), AdminSurfaceInvocation{
		ExpectedContract: contract, ContractVersion: extension.Manifest.AdminSurfaces[0].ContractVersion,
	}); !errors.Is(err, ErrAdminSurfaceNotInvokable) || starter.calls != 0 {
		t.Fatalf("untyped surface calls=%d err=%v", starter.calls, err)
	}
}

func TestManagerAdminSurfaceCommandRequiresActorAndIdempotency(t *testing.T) {
	starter := &adminSurfaceStarterStub{instanceID: "runtime-command"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := adminSurfaceExtension("demo.command", []extensions.ManifestAdminSurface{{
		ID: "demo.command.surface.action", ContractVersion: "demo.command.surface.action@1",
		Kind: "row_action", Action: "add", Label: "Action", Handler: "admin.action",
		Schema: "demo.command.surface.action@1", Operation: extensions.AdminSurfaceOperationCommand,
	}})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	contract, err := manager.ResolveAdminSurface(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	base := AdminSurfaceInvocation{ExpectedContract: contract, ContractVersion: contract.ContractVersion, Input: map[string]any{}}
	if _, err := manager.InvokeAdminSurface(context.Background(), base); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("actorless command = %v", err)
	}
	base.Actor = NewProtocolV2RouteActor(42, true, nil)
	if _, err := manager.InvokeAdminSurface(context.Background(), base); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("non-idempotent command = %v", err)
	}
	base.Actor = &ProtocolV2InvocationActor{}
	base.IdempotencyKey = "forged-actor"
	if _, err := manager.InvokeAdminSurface(context.Background(), base); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("forged command actor = %v", err)
	}
	if starter.calls != 0 {
		t.Fatalf("invalid command reached runtime %d times", starter.calls)
	}
}

func adminSurfaceProtocolClient(t *testing.T, server *adminSurfaceRuntimeServer) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///admin-surface-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.admin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-a",
		TrustGrantId: "grant-a", RuntimeEpoch: 1, InstanceId: "runtime-admin",
	}
	declaration := extensions.ManifestAdminSurface{
		ID: "demo.admin.surface.form", ContractVersion: "demo.admin.surface.form@1",
		Kind: "form", Action: "add", PlacementID: "core.component.page.admin", PlacementContractVersion: "sforum.component.page.admin@1",
		Label: "Form", Handler: "admin.form", PropsSchema: "demo.admin.surface.props@1",
		ResultSchema: "demo.admin.surface.result@1", Operation: extensions.AdminSurfaceOperationCommand,
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, adminSurfaces: []extensions.ManifestAdminSurface{declaration},
		token: []byte("01234567890123456789012345678901"), instance: identity.InstanceId,
	})
}

func adminSurfaceRuntimeContract() AdminSurfaceContract {
	return AdminSurfaceContract{
		ID: "demo.admin.surface.form", ContractVersion: "demo.admin.surface.form@1",
		ExtensionID: "demo.admin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-a", InstanceID: "runtime-admin",
		Kind: "form", Action: "add", PlacementID: "core.component.page.admin", PlacementContractVersion: "sforum.component.page.admin@1",
		Label: "Form", Handler: "admin.form", PropsSchema: "demo.admin.surface.props@1",
		ResultSchema: "demo.admin.surface.result@1", Operation: extensions.AdminSurfaceOperationCommand,
	}
}
