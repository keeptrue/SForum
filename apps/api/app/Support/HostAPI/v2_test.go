package hostapi

import (
	"context"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeUsers struct {
	value map[string]any
	err   error
}

func (f fakeUsers) GetUserSafe(_ context.Context, _ int64, _ int64, declaredFields []string) (map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(declaredFields) == 0 {
		return f.value, nil
	}
	filtered := make(map[string]any, len(declaredFields))
	for _, field := range declaredFields {
		if value, ok := f.value[field]; ok {
			filtered[field] = value
		}
	}
	return filtered, nil
}

func TestRegisterProtocolV2RegistersCompatibilityServices(t *testing.T) {
	server := grpc.NewServer()
	NewGateway(New(Config{})).RegisterProtocolV2(server)
	services := server.GetServiceInfo()
	for _, name := range []string{
		"sforum.host.v2.HostQueryService", "sforum.host.v2.HostCommandService", "sforum.host.v2.DatabaseService",
		"sforum.host.v2.PermissionService",
		"sforum.host.v2.IdentityService", "sforum.host.v2.JobService", "sforum.host.v2.AuditService",
	} {
		if _, ok := services[name]; !ok {
			t.Fatalf("service %s is not registered: %#v", name, services)
		}
	}
}

func TestProtocolV2CompatibilityAdapters(t *testing.T) {
	jobs := &fakeJobs{}
	auditor := &fakeAudit{}
	service := New(Config{
		Capabilities: fakeCaps{
			set: capabilities.NewSet([]string{
				capabilities.SettingsOwn, capabilities.PermissionsCheck, capabilities.UsersRead,
				capabilities.JobsEnqueue, capabilities.AuditAppend,
			}),
			jobs: []string{"demo.sync"},
		},
		Settings:     fakeSettings{values: map[string]string{"host": "smtp.example", "password": "secret"}},
		Permissions:  fakePerms{allowed: true},
		Users:        fakeUsers{value: map[string]any{"id": int64(42), "username": "alice", "email": "alice@example.com"}},
		Jobs:         jobs,
		JobAdmission: &testPluginJobAdmission{},
		Auditor:      auditor,
	})
	core := &protocolV2Core{service: service}
	requestContext := testProtocolV2RequestContext()

	query, err := (&protocolV2QueryServer{core: core}).Execute(context.Background(), &hostv2.QueryRequest{
		Context: requestContext, QueryId: QueryOwnSettingsID, PlanVersion: QueryOwnSettingsVersion,
		Fields: []string{"host"}, ResultSchemaId: QueryOwnSettingsSchemaID, ResultSchemaVersion: QueryOwnSettingsSchemaV1,
	})
	if err != nil || query.GetError() != nil || len(query.GetRows()) != 1 ||
		query.GetRows()[0].GetSchemaId() != QueryOwnSettingsSchemaID {
		t.Fatalf("query = %#v, %v", query, err)
	}
	settings, _ := query.GetRows()[0].GetValue().AsMap()["settings"].(map[string]any)
	if settings["host"] != "smtp.example" || settings["password"] != nil {
		t.Fatalf("filtered settings = %#v", settings)
	}

	permission, err := (&protocolV2PermissionServer{core: core}).Check(context.Background(), &hostv2.PermissionCheckRequest{
		Context: requestContext, UserId: 42, PermissionKey: "topic.create",
	})
	if err != nil || permission.GetError() != nil || !permission.GetAllowed() || permission.GetPolicyId() != PermissionPolicyID {
		t.Fatalf("permission = %#v, %v", permission, err)
	}

	identity, err := (&protocolV2IdentityServer{core: core}).GetUser(context.Background(), &hostv2.IdentityUserRequest{
		Context: requestContext, UserId: 42, DeclaredFields: []string{"id", "username"},
	})
	if err != nil || identity.GetError() != nil {
		t.Fatalf("identity = %#v, %v", identity, err)
	}
	user := identity.GetUser().GetValue().AsMap()
	if user["username"] != "alice" || user["email"] != nil {
		t.Fatalf("declared user fields = %#v", user)
	}

	payload, err := structpb.NewStruct(map[string]any{"page": 2})
	if err != nil {
		t.Fatal(err)
	}
	job, err := (&protocolV2JobServer{core: core}).Enqueue(context.Background(), &hostv2.JobEnqueueRequest{
		Context: requestContext, JobKind: "demo.sync", PayloadVersion: JobPayloadSchemaVersionV1,
		Payload: &protocolv2.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1", Value: payload},
	})
	if err != nil || job.GetError() != nil || jobs.lastExt != "demo.plugin" || jobs.lastKind != "demo.sync" ||
		jobs.contract.JobContract != "demo.plugin.job.sync@1" || jobs.grant != "grant" {
		t.Fatalf("job = %#v, jobs = %#v, %v", job, jobs, err)
	}

	metadata, err := structpb.NewStruct(map[string]any{"source": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := (&protocolV2AuditServer{core: core}).Append(context.Background(), &hostv2.AuditAppendRequest{
		Context: requestContext, Action: "sync.completed", TargetType: "user", TargetId: "7",
		Metadata: &protocolv2.TypedDocument{SchemaId: "demo.audit", SchemaVersion: AuditMetadataSchemaVersion, Value: metadata},
	})
	if err != nil || appended.GetError() != nil || len(auditor.events) != 1 {
		t.Fatalf("audit = %#v, events = %#v, %v", appended, auditor.events, err)
	}
	event := auditor.events[0]
	if event.Action != "extension.demo.plugin.sync.completed" || event.ActorUserID != 42 || event.TargetUserID != 7 ||
		event.Metadata["via"] != VersionV2 || event.Metadata["extensionId"] != "demo.plugin" {
		t.Fatalf("audit event = %#v", event)
	}
}

func TestProtocolV2CompatibilityAdaptersFailClosed(t *testing.T) {
	service := New(Config{Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})}})
	core := &protocolV2Core{service: service}
	requestContext := testProtocolV2RequestContext()

	query, err := (&protocolV2QueryServer{core: core}).Execute(context.Background(), &hostv2.QueryRequest{
		Context: requestContext, QueryId: QueryOwnSettingsID, PlanVersion: QueryOwnSettingsVersion,
	})
	if err != nil || query.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED {
		t.Fatalf("query denial = %#v, %v", query, err)
	}

	jobs := &fakeJobs{}
	service = New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.JobsEnqueue}), jobs: []string{"demo.sync"}},
		Jobs:         jobs,
	})
	core = &protocolV2Core{service: service}
	job, err := (&protocolV2JobServer{core: core}).Enqueue(context.Background(), &hostv2.JobEnqueueRequest{
		Context: requestContext, JobKind: "demo.sync", PayloadVersion: "1", IdempotencyKey: "unsupported",
		Payload: &protocolv2.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1"},
	})
	if err != nil || job.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION || jobs.lastKind != "" {
		t.Fatalf("job denial = %#v, jobs = %#v, %v", job, jobs, err)
	}

	job, err = (&protocolV2JobServer{core: core}).Enqueue(context.Background(), &hostv2.JobEnqueueRequest{
		Context: requestContext, JobKind: "demo.sync", PayloadVersion: "1",
		Payload: &protocolv2.TypedDocument{SchemaId: "undeclared.payload", SchemaVersion: "1"},
	})
	if err != nil || job.GetError().GetReason() != "host.job_payload_contract_mismatch" || jobs.lastKind != "" {
		t.Fatalf("payload contract denial = %#v, jobs = %#v, %v", job, jobs, err)
	}

	staleContext := testProtocolV2RequestContext()
	staleContext.Extension.ArtifactDigest = "stale-artifact"
	job, err = (&protocolV2JobServer{core: core}).Enqueue(context.Background(), &hostv2.JobEnqueueRequest{
		Context: staleContext, JobKind: "demo.sync", PayloadVersion: "1",
		Payload: &protocolv2.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1"},
	})
	if err != nil || job.GetError().GetReason() != "host.job_runtime_stale" || jobs.lastKind != "" {
		t.Fatalf("stale runtime denial = %#v, jobs = %#v, %v", job, jobs, err)
	}
}

func testProtocolV2RequestContext() *protocolv2.RequestContext {
	return &protocolv2.RequestContext{
		RequestId: "request-1", Locale: "zh-CN", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		Actor: &protocolv2.Actor{UserId: 42},
		Extension: &protocolv2.ExtensionIdentity{
			ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
			TrustGrantId: "grant", RuntimeEpoch: 1, InstanceId: "instance",
		},
	}
}
