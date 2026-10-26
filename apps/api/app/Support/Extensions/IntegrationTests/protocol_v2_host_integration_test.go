package extensionsruntime_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type protocolV2HostState struct {
	mu     sync.Mutex
	jobs   []string
	events []audit.Event
}

func (s *protocolV2HostState) EnqueuePluginJob(_ context.Context, extensionID, kind string, _ map[string]any) error {
	s.mu.Lock()
	s.jobs = append(s.jobs, extensionID+":"+kind)
	s.mu.Unlock()
	return nil
}

func (s *protocolV2HostState) EnqueueVersionedPluginJob(_ context.Context, contract supportjobs.PluginJobContract, _ string, _ map[string]any) error {
	s.mu.Lock()
	s.jobs = append(s.jobs, contract.ExtensionID+":"+contract.JobName)
	s.mu.Unlock()
	return nil
}

func (s *protocolV2HostState) Append(_ context.Context, event audit.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *protocolV2HostState) snapshot() ([]string, []audit.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.jobs...), append([]audit.Event(nil), s.events...)
}

type protocolV2HostCapabilities struct {
	set capabilities.Set
}

func (s protocolV2HostCapabilities) CapabilitiesFor(context.Context, string) (capabilities.Set, error) {
	return s.set, nil
}

func (protocolV2HostCapabilities) DeclaredJobKinds(context.Context, string) ([]string, error) {
	return []string{"demo.sync"}, nil
}

func (protocolV2HostCapabilities) PluginJobContract(_ context.Context, extensionID, jobName string) (supportjobs.PluginJobContract, error) {
	return supportjobs.PluginJobContract{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("a", 64),
		JobName: jobName, JobContract: "runtime.v2.job.demo-sync@1",
		PayloadSchemaID: "demo.sync.payload", PayloadSchemaVersion: "1",
	}, nil
}

type protocolV2HostSettings struct{}

func (protocolV2HostSettings) ListSettings(context.Context, string) (map[string]string, error) {
	return map[string]string{"endpoint": "broker.internal"}, nil
}

type protocolV2HostPermissions struct{}

func (protocolV2HostPermissions) HasPermission(context.Context, int64, string) (bool, error) {
	return true, nil
}

type protocolV2HostUsers struct{}

func (protocolV2HostUsers) GetUserSafe(_ context.Context, _ int64, _ int64, declaredFields []string) (map[string]any, error) {
	user := map[string]any{"id": int64(42), "username": "broker-user", "email": "private@example.com"}
	if len(declaredFields) == 0 {
		return user, nil
	}
	filtered := make(map[string]any, len(declaredFields))
	for _, field := range declaredFields {
		if value, ok := user[field]; ok {
			filtered[field] = value
		}
	}
	return filtered, nil
}

// 本夹具只验证已认证 broker 的 Host API 回调；production 的 exact-runtime
// admission 由 bootstrap adapter + Manager gate 覆盖。
type protocolV2HostJobAdmission struct{}

func (protocolV2HostJobAdmission) AcquirePluginJobEnqueue(
	ctx context.Context,
	_ hostapi.PluginJobEnqueueIdentity,
) (hostapi.PluginJobEnqueueLease, error) {
	return protocolV2HostJobLease{ctx: ctx}, nil
}

type protocolV2HostJobLease struct {
	ctx context.Context
}

func (l protocolV2HostJobLease) Context() context.Context { return l.ctx }
func (protocolV2HostJobLease) Release()                   {}

func newProtocolV2HostGateway() (*hostapi.Gateway, *protocolV2HostState) {
	state := &protocolV2HostState{}
	set := capabilities.NewSet([]string{
		capabilities.HostAPI, capabilities.SettingsOwn, capabilities.PermissionsCheck,
		capabilities.UsersRead, capabilities.JobsEnqueue, capabilities.AuditAppend,
	})
	service := hostapi.New(hostapi.Config{
		Capabilities: protocolV2HostCapabilities{set: set}, Settings: protocolV2HostSettings{},
		Permissions: protocolV2HostPermissions{}, Users: protocolV2HostUsers{}, Jobs: state,
		JobAdmission: protocolV2HostJobAdmission{}, Auditor: state,
	})
	return hostapi.NewGateway(service), state
}

func (s *protocolV2Helper) invokeHostCallbacks(ctx context.Context, request *pluginwire.HookRequest) error {
	host, err := s.Host()
	if err != nil {
		return err
	}
	query := &hostwire.QueryRequest{
		Context: host.RequestContext(request.GetContext()), QueryId: pluginv2sdk.HostQueryOwnSettingsID,
		PlanVersion: pluginv2sdk.HostQueryOwnSettingsVersion, Fields: []string{"endpoint"},
		ResultSchemaId: pluginv2sdk.HostQueryOwnSettingsSchemaID, ResultSchemaVersion: pluginv2sdk.HostQueryOwnSettingsSchemaV1,
	}
	stream, err := host.Queries.Stream(ctx, query)
	if err != nil {
		return fmt.Errorf("stream settings: %w", err)
	}
	row, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive settings: %w", err)
	}
	settings, _ := row.GetValue().GetValue().AsMap()["settings"].(map[string]any)
	if row.GetError() != nil || settings["endpoint"] != "broker.internal" {
		return fmt.Errorf("unexpected settings row: %#v", row)
	}
	if _, err := stream.Recv(); err != io.EOF {
		return fmt.Errorf("settings stream end: %v", err)
	}

	permission, err := host.Permissions.Check(ctx, &hostwire.PermissionCheckRequest{
		Context: host.RequestContext(request.GetContext()), UserId: 42, PermissionKey: "topic.create",
	})
	if err != nil || permission.GetError() != nil || !permission.GetAllowed() {
		return fmt.Errorf("permission callback: response=%#v err=%v", permission, err)
	}
	identity, err := host.Identity.GetUser(ctx, &hostwire.IdentityUserRequest{
		Context: host.RequestContext(request.GetContext()), UserId: 42, DeclaredFields: []string{"username"},
	})
	if err != nil || identity.GetError() != nil || identity.GetUser().GetValue().AsMap()["username"] != "broker-user" ||
		identity.GetUser().GetValue().AsMap()["email"] != nil {
		return fmt.Errorf("identity callback: response=%#v err=%v", identity, err)
	}
	payload, err := structpb.NewStruct(map[string]any{"deliveryId": request.GetDeliveryId()})
	if err != nil {
		return err
	}
	job, err := host.Jobs.Enqueue(ctx, &hostwire.JobEnqueueRequest{
		Context: host.RequestContext(request.GetContext()), JobKind: "demo.sync", PayloadVersion: "1",
		Payload: &protocolwire.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1", Value: payload},
	})
	if err != nil || job.GetError() != nil {
		return fmt.Errorf("job callback: response=%#v err=%v", job, err)
	}
	metadata, err := structpb.NewStruct(map[string]any{"deliveryId": request.GetDeliveryId()})
	if err != nil {
		return err
	}
	appended, err := host.Audit.Append(ctx, &hostwire.AuditAppendRequest{
		Context: host.RequestContext(request.GetContext()), Action: "hook.completed",
		Metadata: &protocolwire.TypedDocument{SchemaId: "runtime.v2.audit", SchemaVersion: "1", Value: metadata},
	})
	if err != nil || appended.GetError() != nil {
		return fmt.Errorf("audit callback: response=%#v err=%v", appended, err)
	}
	return nil
}

func (s *protocolV2Helper) observeHostRejection(ctx context.Context, request *pluginwire.HookRequest, mode string) error {
	host, err := s.Host()
	if err != nil {
		return err
	}
	requestContext := host.RequestContext(request.GetContext())
	callContext := ctx
	expected := codes.OK
	switch mode {
	case "stale_identity":
		requestContext.Extension.RuntimeEpoch++
		expected = codes.FailedPrecondition
	case "forged_authority":
		requestContext.GrantedAuthority[0].Key = "raw.database"
		expected = codes.PermissionDenied
	case "expired_deadline":
		requestContext.Deadline = timestamppb.New(time.Now().Add(-time.Second))
		expected = codes.DeadlineExceeded
	case "cancelled":
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		callContext = cancelled
		expected = codes.Canceled
	case "oversized":
		value, encodeErr := structpb.NewStruct(map[string]any{
			"blob": strings.Repeat("x", extensionsruntime.DefaultProtocolV2MaxMessageBytes),
		})
		if encodeErr != nil {
			return encodeErr
		}
		_, err = host.Jobs.Enqueue(callContext, &hostwire.JobEnqueueRequest{
			Context: requestContext, JobKind: "demo.sync", PayloadVersion: "1",
			Payload: &protocolwire.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1", Value: value},
		})
		expected = codes.ResourceExhausted
	default:
		return fmt.Errorf("unknown host rejection mode %q", mode)
	}
	if mode != "oversized" {
		_, err = host.Permissions.Check(callContext, &hostwire.PermissionCheckRequest{
			Context: requestContext, UserId: 42, PermissionKey: "topic.create",
		})
	}
	if code := status.Code(err); code != expected {
		return fmt.Errorf("host rejection %s returned %s (%v), want %s", mode, code, err, expected)
	}
	return nil
}
