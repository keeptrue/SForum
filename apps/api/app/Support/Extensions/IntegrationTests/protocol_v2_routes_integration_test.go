package extensionsruntime_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const protocolV2RouteHelperEnv = "protocol-v2-route-e2e"

func TestProtocolV2RouteAcrossRealSubprocessAndManagerAdmission(t *testing.T) {
	extension := protocolV2RouteExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Target.BaseURL != "" || snapshot.Identity.InstanceID == "" || snapshot.Admission.ActiveTotal != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	response, err := manager.InvokeRouteInstance(lease.Context, snapshot.Identity, protocolV2RouteE2ERequest())
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated || response.Body["instance"] != snapshot.Identity.InstanceID ||
		response.Body["actor"] != float64(42) || response.Body["title"] != "hello" {
		t.Fatalf("response = %#v", response)
	}
	if !reflect.DeepEqual(response.Body["queryKeyOrder"], []any{"page"}) ||
		!reflect.DeepEqual(response.Body["queryValues"], map[string]any{"page": []any{"2"}}) ||
		!reflect.DeepEqual(response.Body["legacyQuery"], map[string]any{"page": "2"}) {
		t.Fatalf("legacy query response = %#v", response.Body)
	}
	if after, err := manager.InspectRuntimeInstance(snapshot.Identity); err != nil || after.Admission.ActiveTotal != 0 {
		t.Fatalf("admission after call = %#v, %v", after, err)
	}
}

func TestProtocolV2RouteRepeatedQueryAcrossRealSubprocess(t *testing.T) {
	extension := protocolV2RouteExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolV2RouteE2ERequest()
	request.QueryParameters["tag"] = "first"
	request.QueryParameterValues = map[string][]string{
		"zeta":  {"last", "second"},
		"alpha": {""},
		"tag":   {"first", "a+b", "slash/value", ""},
	}
	response, err := manager.InvokeRouteInstance(lease.Context, snapshot.Identity, request)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Body["queryKeyOrder"], []any{"alpha", "page", "tag", "zeta"}) {
		t.Fatalf("query key order = %#v", response.Body["queryKeyOrder"])
	}
	wantValues := map[string]any{
		"alpha": []any{""},
		"page":  []any{"2"},
		"tag":   []any{"first", "a+b", "slash/value", ""},
		"zeta":  []any{"last", "second"},
	}
	if !reflect.DeepEqual(response.Body["queryValues"], wantValues) {
		t.Fatalf("query values = %#v", response.Body["queryValues"])
	}
	wantLegacy := map[string]any{"alpha": "", "page": "2", "tag": "first", "zeta": "last"}
	if !reflect.DeepEqual(response.Body["legacyQuery"], wantLegacy) {
		t.Fatalf("legacy query = %#v", response.Body["legacyQuery"])
	}
}

func TestProtocolV2RouteDeliversHostSignedDelegationAcrossRealBroker(t *testing.T) {
	pool := new(pgxpool.Pool)
	authority, err := hostapi.NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	commandRuntime, err := hostapi.NewPostgresProtocolV2CommandRuntime(hostapi.PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: authority, Jobs: new(supportjobs.Dispatcher),
		Moderation: moderation.NewPostgresStore(pool), AttachmentStatuses: protocolV2RouteAttachmentMutator{},
		AttachmentExtensions: protocolV2RouteAttachmentMutator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway := hostapi.NewGateway(hostapi.New(hostapi.Config{}))
	if err := gateway.BindProtocolV2CommandRuntime(commandRuntime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	extension := protocolV2RouteExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		HostAPI: gateway,
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolV2RouteE2ERequest()
	request.Actor = extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"user.manage": true})
	request.IdempotencyKey = "real-route-request-42"
	response, err := manager.InvokeRouteInstance(lease.Context, snapshot.Identity, request)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if response.Body["delegatedPlan"] != true || response.Body["delegationCount"] != float64(1) {
		t.Fatalf("delegated response = %#v", response)
	}
}

func TestProtocolV2RouteExactRetainedInstanceNeverFallsBackActive(t *testing.T) {
	extension := protocolV2RouteExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
	})
	first, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	second, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = starter.StopInstance(context.Background(), extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: first.InstanceID})
		_ = starter.Stop(context.Background(), extension)
	})
	if first.InstanceID == second.InstanceID {
		t.Fatal("replacement reused runtime identity")
	}
	response, err := starter.InvokeRouteInstance(context.Background(), extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: first.InstanceID,
	}, protocolV2RouteE2ERequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.Body["instance"] != first.InstanceID {
		t.Fatalf("retained call fell back to active %q: %#v", second.InstanceID, response.Body)
	}
	_, err = starter.InvokeRouteInstance(context.Background(), extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: "missing-instance",
	}, protocolV2RouteE2ERequest())
	if !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("missing exact instance error = %v", err)
	}
}

func TestProtocolV2CustomGuardAcrossRealSubprocessAndManagerAdmission(t *testing.T) {
	extension := protocolV2RouteExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, extensionsruntime.RuntimeCallGuard)
	if err != nil {
		t.Fatal(err)
	}
	request := extensionsruntime.ProtocolV2GuardRequest{
		GuardID: "runtime.v2.guard.owner", GuardContractVersion: "runtime.v2.guard.owner@1",
		RouteID: "runtime.v2.route.echo", RouteContractVersion: "runtime.v2.route.echo@1",
		Method: http.MethodPost, Path: "/runtime/41", PathParameters: map[string]string{"id": "41"},
		RequestSchema: "runtime.v2.route.request@1", Body: map[string]any{"title": "guarded"}, BodyPresent: true,
		Authority: extensionsruntime.ProtocolV2RequestAuthority{
			Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
		},
		Actor: extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"topics.write": true}), Timeout: 3 * time.Second,
	}
	err = manager.InvokeGuardInstance(lease.Context, snapshot.Identity, request)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	request.QueryParameters = map[string]string{"deny": "1"}
	snapshot, lease, err = manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, extensionsruntime.RuntimeCallGuard)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.InvokeGuardInstance(lease.Context, snapshot.Identity, request)
	lease.Release()
	if !errors.Is(err, extensionsruntime.ErrProtocolV2GuardDenied) {
		t.Fatalf("denied guard error = %v", err)
	}
	if after, err := manager.InspectRuntimeInstance(snapshot.Identity); err != nil || after.Admission.ActiveTotal != 0 {
		t.Fatalf("guard admission after call = %#v, %v", after, err)
	}
}

func TestProtocolV2RouteHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != protocolV2RouteHelperEnv {
		return
	}
	pluginv2sdk.Serve(&protocolV2RouteE2EServer{Server: pluginv2sdk.NewServer()})
	os.Exit(0)
}

type protocolV2RouteE2EServer struct{ *pluginv2sdk.Server }

func (s *protocolV2RouteE2EServer) InvokeRoute(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	if request.GetRouteId() == pageLoaderDataRouteID {
		// loader 调用必须是 GET + handler 阶段 + 过滤授权（不携带凭证/actor 身份）。
		if request.GetMethod() != http.MethodGet ||
			request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER ||
			request.GetPath() != "/page-data/article" ||
			request.GetContext().GetActor() != nil ||
			request.GetRequestAuthorityMode() != pluginwire.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_FILTERED {
			return &pluginwire.RouteResponse{
				Context: protocolV2RouteResponseContext(ctx),
				Error: &protocolwire.ErrorDetail{
					Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "page_data.fixture_invalid",
				},
			}, nil
		}
		value, err := structpb.NewStruct(map[string]any{
			"title":     "loaded through protocol v2",
			"slug":      request.GetPathParameters()["slug"],
			"locale":    request.GetQueryParameters()["locale"],
			"actorHint": request.GetQueryParameters()["actorHint"],
		})
		if err != nil {
			return nil, err
		}
		return &pluginwire.RouteResponse{
			Context: protocolV2RouteResponseContext(ctx), StatusCode: http.StatusOK,
			Body: &protocolwire.TypedDocument{SchemaId: "runtime.v2.route.response", SchemaVersion: "1", Value: value},
		}, nil
	}
	if request.GetRouteId() == "runtime.v2.guard.owner" {
		status := http.StatusNoContent
		if request.GetQueryParameters()["deny"] == "1" {
			status = http.StatusForbidden
		}
		if request.GetContractVersion() != "runtime.v2.guard.owner@1" || request.GetMethod() != http.MethodPost ||
			request.GetPath() != "/runtime/41" || request.GetPathParameters()["id"] != "41" ||
			request.GetContext().GetActor().GetUserId() != 42 ||
			request.GetBody().GetSchemaId() != "runtime.v2.route.request" {
			status = http.StatusForbidden
		}
		return &pluginwire.RouteResponse{Context: protocolV2RouteResponseContext(ctx), StatusCode: uint32(status)}, nil
	}
	if request.GetRouteId() != "runtime.v2.route.echo" || request.GetContractVersion() != "runtime.v2.route.echo@1" ||
		request.GetRouteAction() != extensionmanifest.RouteActionAdd ||
		request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER ||
		len(request.GetMutableRequestFields()) != 0 || len(request.GetMutableResponseFields()) != 0 || request.GetPriorResponse() != nil ||
		request.GetMethod() != http.MethodPost || request.GetPath() != "/runtime/41" ||
		request.GetPathParameters()["id"] != "41" || request.GetQueryParameters()["page"] != "2" ||
		request.GetBody().GetSchemaId() != "runtime.v2.route.request" || request.GetBody().GetSchemaVersion() != "1" ||
		ctx.GetActor().GetUserId() != 42 {
		return &pluginwire.RouteResponse{
			Context: protocolV2RouteResponseContext(ctx),
			Error:   &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "route.fixture_invalid"},
		}, nil
	}
	input := request.GetBody().GetValue().AsMap()
	delegatedPlan := false
	if len(ctx.GetHostCommandDelegations()) > 0 {
		host, err := s.Host()
		if err != nil {
			return nil, err
		}
		value, err := structpb.NewStruct(map[string]any{"userId": "99", "status": "disabled"})
		if err != nil {
			return nil, err
		}
		command, err := host.DelegatedCommandRequest(ctx,
			hostapi.CommandIdentityUserStatusSetID, hostapi.CommandIdentityUserStatusSetVersion,
			&protocolwire.TypedDocument{
				SchemaId:      hostapi.CommandIdentityUserStatusInputSchemaID,
				SchemaVersion: hostapi.CommandIdentityUserStatusSchemaVersion, Value: value,
			},
		)
		if err != nil {
			return nil, err
		}
		command.ExpectedRevision = "0"
		plan, err := host.Commands.Plan(callCtx, command)
		if err != nil {
			return nil, err
		}
		if plan.GetError() != nil || plan.GetPlanId() == "" {
			return nil, errors.New("Host rejected the real actor delegation")
		}
		delegatedPlan = true
	}
	queryKeyOrder, queryValues, legacyQuery := protocolV2RouteObservedQuery(request)
	value, err := structpb.NewStruct(map[string]any{
		"instance": ctx.GetExtension().GetInstanceId(), "actor": ctx.GetActor().GetUserId(), "title": input["title"],
		"delegatedPlan": delegatedPlan, "delegationCount": len(ctx.GetHostCommandDelegations()),
		"queryKeyOrder": queryKeyOrder, "queryValues": queryValues, "legacyQuery": legacyQuery,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: protocolV2RouteResponseContext(ctx), StatusCode: http.StatusCreated,
		Headers: []*protocolwire.Header{{Name: "X-Route-E2E", Values: []string{"ok"}}},
		Body:    &protocolwire.TypedDocument{SchemaId: "runtime.v2.route.response", SchemaVersion: "1", Value: value},
	}, nil
}

func protocolV2RouteObservedQuery(request *pluginwire.RouteRequest) ([]any, map[string]any, map[string]any) {
	keyOrder := make([]any, 0, len(request.GetQueryParameterValues()))
	values := make(map[string]any, len(request.GetQueryParameterValues()))
	for _, parameter := range request.GetQueryParameterValues() {
		keyOrder = append(keyOrder, parameter.GetKey())
		items := make([]any, len(parameter.GetValues()))
		for index, value := range parameter.GetValues() {
			items[index] = value
		}
		values[parameter.GetKey()] = items
	}
	legacy := make(map[string]any, len(request.GetQueryParameters()))
	for key, value := range request.GetQueryParameters() {
		legacy[key] = value
	}
	return keyOrder, values, legacy
}

type protocolV2RouteAttachmentMutator struct{}

func (protocolV2RouteAttachmentMutator) MutateProtocolV2AttachmentStatus(
	context.Context,
	pgx.Tx,
	int64,
	string,
) (hostapi.ProtocolV2AttachmentStatusResult, error) {
	return hostapi.ProtocolV2AttachmentStatusResult{}, errors.New("route delegation fixture does not execute attachment commands")
}

func (protocolV2RouteAttachmentMutator) ExtensionAttachmentPolicy(context.Context) (extensionreference.Policy, error) {
	return extensionreference.Policy{}, errRouteDelegationAttachmentCommand
}

func (protocolV2RouteAttachmentMutator) PrepareClaim(context.Context, pgx.Tx, extensionreference.ClaimInput) (*extensionreference.ClaimPlan, error) {
	return nil, errRouteDelegationAttachmentCommand
}

func (protocolV2RouteAttachmentMutator) ApplyClaim(context.Context, pgx.Tx, *extensionreference.ClaimPlan) (extensionreference.Claim, error) {
	return extensionreference.Claim{}, errRouteDelegationAttachmentCommand
}

func (protocolV2RouteAttachmentMutator) Count(context.Context, pgx.Tx, extensionreference.ReleaseInput) (int, error) {
	return 0, errRouteDelegationAttachmentCommand
}

func (protocolV2RouteAttachmentMutator) Release(context.Context, pgx.Tx, extensionreference.ReleaseInput) (extensionreference.Release, error) {
	return extensionreference.Release{}, errRouteDelegationAttachmentCommand
}

func (protocolV2RouteAttachmentMutator) List(context.Context, pgx.Tx, extensionreference.ListInput) (extensionreference.List, error) {
	return extensionreference.List{}, errRouteDelegationAttachmentCommand
}

var errRouteDelegationAttachmentCommand = errors.New("route delegation fixture does not execute attachment commands")

func protocolV2RouteResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	return &protocolwire.ResponseContext{
		RequestId: request.GetRequestId(), Trace: proto.Clone(request.GetTrace()).(*protocolwire.TraceContext),
		Extension: proto.Clone(request.GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
	}
}

func protocolV2RouteExtension(t *testing.T) extensions.Extension {
	t.Helper()
	extension := protocolV2TestExtension(t, "v2")
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + protocolV2RouteHelperEnv + " exec " + shellQuote(os.Args[0]) +
		" -test.run=TestProtocolV2RouteHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	extension.Manifest.Events = nil
	extension.Manifest.Services = nil
	extension.Manifest.Database = &extensions.ManifestDatabase{
		ContractVersion: extension.ID + ".database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantHostCommands},
		Retention: extensionmanifest.ManifestRetention{
			OnDisable: "retain", OnUninstall: "retain",
		},
	}
	extension.Manifest.Guards = []extensions.ManifestGuard{{
		ID: "runtime.v2.guard.owner", ContractVersion: "runtime.v2.guard.owner@1", Kind: "custom",
		Entry: "backend/guard", Digest: strings.Repeat("c", 64),
	}}
	extension.Manifest.Routes = []extensions.ManifestRoute{{
		ID: "runtime.v2.route.echo", ContractVersion: "runtime.v2.route.echo@1", Action: "add",
		Path: "/runtime/{id}", Methods: []string{http.MethodPost}, Guard: "runtime.v2.guard.owner", Handler: "route.echo",
		RequestSchema: "runtime.v2.route.request@1", ResponseSchema: "runtime.v2.route.response@1",
	}}
	return extension
}

func protocolV2RouteE2ERequest() extensionsruntime.ProtocolV2RouteRequest {
	return extensionsruntime.ProtocolV2RouteRequest{
		RouteID: "runtime.v2.route.echo", ContractVersion: "runtime.v2.route.echo@1",
		RouteAction: extensionmanifest.RouteActionAdd, InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method: http.MethodPost, Path: "/runtime/41", PathParameters: map[string]string{"id": "41"},
		QueryParameters: map[string]string{"page": "2"}, RequestSchema: "runtime.v2.route.request@1",
		ResponseSchema: "runtime.v2.route.response@1", Body: map[string]any{"title": "hello"}, BodyPresent: true,
		Authority: extensionsruntime.ProtocolV2RequestAuthority{
			Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
		},
		Actor: extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"topics.write": true}), Timeout: 3 * time.Second,
	}
}

var _ pluginwire.PluginRuntimeServiceServer = (*protocolV2RouteE2EServer)(nil)
