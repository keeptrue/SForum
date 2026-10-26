package pluginv2

import (
	"sort"
	"strings"
	"testing"

	_ "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	_ "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	_ "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestGeneratedServiceCatalog(t *testing.T) {
	want := []string{
		"sforum.host.v2.AdminSurfaceService",
		"sforum.host.v2.AuditService",
		"sforum.host.v2.CacheService",
		"sforum.host.v2.DatabaseService",
		"sforum.host.v2.FileService",
		"sforum.host.v2.HostCommandService",
		"sforum.host.v2.HostQueryService",
		"sforum.host.v2.HttpService",
		"sforum.host.v2.IdentityService",
		"sforum.host.v2.JobService",
		"sforum.host.v2.MediaService",
		"sforum.host.v2.NavigationService",
		"sforum.host.v2.PermissionService",
		"sforum.host.v2.PluginSessionService",
		"sforum.host.v2.ScheduleService",
		"sforum.host.v2.SecretService",
		"sforum.host.v2.ServiceDiscoveryService",
		"sforum.host.v2.TracingService",
		"sforum.plugin.v2.PluginRuntimeService",
	}

	var got []string
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(file.Package()), "sforum.") {
			return true
		}
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			got = append(got, string(services.Get(i).FullName()))
		}
		return true
	})
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("generated service catalog drifted\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

func TestRequiredEnvelopeAndCommandFields(t *testing.T) {
	assertFields(t, "sforum.protocol.v2.RequestContext",
		"request_id", "trace", "actor", "locale", "deadline", "extension", "granted_authority", "idempotency_key", "host_command_delegations", "host_query_delegations", "host_session_delegation")
	assertExactFields(t, "sforum.protocol.v2.HostSessionDelegation", "token")
	assertFields(t, "sforum.protocol.v2.HostCommandDelegation",
		"command_id", "command_version", "idempotency_key", "token")
	assertFields(t, "sforum.protocol.v2.HostQueryDelegation",
		"query_id", "contract_version", "plan_version", "result_schema_id", "result_schema_version", "scope", "token")
	assertFields(t, "sforum.protocol.v2.ExtensionIdentity",
		"extension_id", "extension_version", "artifact_digest", "trust_grant_id", "runtime_epoch", "instance_id")
	assertFields(t, "sforum.protocol.v2.TypedDocument", "schema_id", "schema_version", "value")
	assertFields(t, "sforum.protocol.v2.HandshakeRequest",
		"context", "host_protocols", "host_features", "limits", "host_broker_id", "host_api_version", "runtime_token")
	assertFields(t, "sforum.protocol.v2.LifecycleRequest",
		"context", "action", "plan_version", "step_id", "checkpoint", "input", "dry_run", "forced")
	assertFields(t, "sforum.host.v2.CommandRequest",
		"context", "command_id", "command_version", "idempotency_key", "dry_run", "expected_revision", "input", "actor_delegation", "query_invalidation_tags")
	assertFields(t, "sforum.host.v2.QueryRequest",
		"context", "query_id", "plan_version", "fields", "filters", "sorts", "page", "result_schema_id", "result_schema_version", "contract_version", "relations", "scope", "actor_delegation", "offset")
	assertFields(t, "sforum.host.v2.QueryResponse", "context", "rows", "page", "error", "next_offset")
	assertFields(t, "sforum.host.v2.QueryRow", "context", "sequence", "value", "error", "page", "next_offset", "final")
	assertFields(t, "sforum.host.v2.CommandResult",
		"context", "state", "transaction_id", "audit_event_id", "committed_revision", "output", "error")
	assertFields(t, "sforum.plugin.v2.CommandInvocationRequest",
		"context", "command_id", "contract_version", "handler", "input")
	assertFields(t, "sforum.plugin.v2.CommandInvocationResponse", "context", "result", "error")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimeBinding",
		"query_id", "contract_version", "plan_version", "result_schema", "handler")
	assertExactFields(t, "sforum.plugin.v2.QueryResultFilterRuntimeBinding",
		"filter_id", "filter_contract_version", "query_id", "query_contract_version",
		"query_plan_version", "result_schema", "handler")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimePlan",
		"shape_digest", "fields", "relations", "filters", "sorts", "pagination", "locale", "scope", "fetch_limit")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimePagination", "mode", "offset", "limit")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimeFilter", "field", "value")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimeSort", "field", "descending")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimeRow", "canonical_json")
	assertExactFields(t, "sforum.plugin.v2.QueryRuntimeRows", "rows")
	assertExactFields(t, "sforum.plugin.v2.QueryInvocationRequest", "context", "binding", "plan")
	assertExactFields(t, "sforum.plugin.v2.QueryInvocationResponse", "context", "binding", "shape_digest", "success", "error")
	assertExactFields(t, "sforum.plugin.v2.QueryResultFilterRequest", "context", "binding", "plan", "input")
	assertExactFields(t, "sforum.plugin.v2.QueryResultFilterResponse", "context", "binding", "shape_digest", "success", "error")
	assertOneofFields(t, "sforum.plugin.v2.QueryInvocationResponse", "outcome", "success", "error")
	assertOneofFields(t, "sforum.plugin.v2.QueryResultFilterResponse", "outcome", "success", "error")
	assertFieldKind(t, "sforum.plugin.v2.QueryRuntimeRow", "canonical_json", protoreflect.BytesKind)
	assertFields(t, "sforum.plugin.v2.RouteRequest",
		"context", "route_id", "contract_version", "method", "path", "headers", "path_parameters",
		"query_parameters", "body", "request_authority_mode", "guard_kind", "route_action", "invocation_stage",
		"mutable_request_fields", "mutable_response_fields", "prior_response", "query_parameter_values")
	assertFields(t, "sforum.plugin.v2.RouteResponse",
		"context", "status_code", "headers", "body", "stream_follows", "error", "request_patch", "response_patch")
	assertFields(t, "sforum.plugin.v2.RoutePatchOperation", "kind", "path", "value", "value_json")
	assertFields(t, "sforum.plugin.v2.RouteResponseDocument", "status_code", "headers", "body")
	assertFields(t, "sforum.plugin.v2.RouteQueryParameter", "key", "values")
	assertFields(t, "sforum.plugin.v2.RouteStreamOpen",
		"context", "route_id", "contract_version", "method", "path", "headers", "request_authority_mode", "guard_kind")
	assertEnumValues(t, "sforum.plugin.v2.RouteRequestAuthorityMode",
		"ROUTE_REQUEST_AUTHORITY_MODE_UNSPECIFIED", "ROUTE_REQUEST_AUTHORITY_MODE_FILTERED", "ROUTE_REQUEST_AUTHORITY_MODE_RAW")
	assertEnumValues(t, "sforum.plugin.v2.RouteGuardKind",
		"ROUTE_GUARD_KIND_UNSPECIFIED", "ROUTE_GUARD_KIND_HOST", "ROUTE_GUARD_KIND_CUSTOM", "ROUTE_GUARD_KIND_RAW_REQUEST")
	assertEnumValues(t, "sforum.plugin.v2.RouteInvocationStage",
		"ROUTE_INVOCATION_STAGE_UNSPECIFIED", "ROUTE_INVOCATION_STAGE_HANDLER", "ROUTE_INVOCATION_STAGE_REQUEST", "ROUTE_INVOCATION_STAGE_RESPONSE")
	assertEnumValues(t, "sforum.plugin.v2.RoutePatchOperationKind",
		"ROUTE_PATCH_OPERATION_KIND_UNSPECIFIED", "ROUTE_PATCH_OPERATION_KIND_ADD", "ROUTE_PATCH_OPERATION_KIND_REPLACE", "ROUTE_PATCH_OPERATION_KIND_REMOVE")
	assertFieldNumbers(t, "sforum.plugin.v2.RouteRequest", map[protoreflect.Name]protoreflect.FieldNumber{
		"query_parameters": 8,
		"route_action":     12, "invocation_stage": 13, "mutable_request_fields": 14,
		"mutable_response_fields": 15, "prior_response": 16, "query_parameter_values": 17,
	})
	assertFieldNumbers(t, "sforum.plugin.v2.RouteResponse", map[protoreflect.Name]protoreflect.FieldNumber{
		"request_patch": 7, "response_patch": 8,
	})
	assertFieldNumbers(t, "sforum.plugin.v2.RoutePatchOperation", map[protoreflect.Name]protoreflect.FieldNumber{
		"kind": 1, "path": 2, "value": 3, "value_json": 4,
	})
	assertFieldNumbers(t, "sforum.plugin.v2.RouteResponseDocument", map[protoreflect.Name]protoreflect.FieldNumber{
		"status_code": 1, "headers": 2, "body": 3,
	})
	assertFieldNumbers(t, "sforum.plugin.v2.RouteQueryParameter", map[protoreflect.Name]protoreflect.FieldNumber{
		"key": 1, "values": 2,
	})
}

func TestStreamingModesRemainExplicit(t *testing.T) {
	tests := []struct {
		method          string
		clientStreaming bool
		serverStreaming bool
	}{
		{"sforum.plugin.v2.PluginRuntimeService.RunLifecycle", false, true},
		{"sforum.plugin.v2.PluginRuntimeService.StreamRoute", true, true},
		{"sforum.plugin.v2.PluginRuntimeService.ExecuteJob", false, true},
		{"sforum.plugin.v2.PluginRuntimeService.InvokeQuery", false, false},
		{"sforum.plugin.v2.PluginRuntimeService.FilterQueryResult", false, false},
		{"sforum.plugin.v2.PluginRuntimeService.TransferFile", true, true},
		{"sforum.plugin.v2.PluginRuntimeService.StreamService", true, true},
		{"sforum.host.v2.HostQueryService.Stream", false, true},
		{"sforum.host.v2.DatabaseService.StreamQuery", false, true},
		{"sforum.host.v2.JobService.Watch", false, true},
		{"sforum.host.v2.ServiceDiscoveryService.Stream", true, true},
		{"sforum.host.v2.FileService.Read", false, true},
		{"sforum.host.v2.FileService.Write", true, false},
		{"sforum.host.v2.HttpService.Stream", true, true},
	}
	for _, test := range tests {
		method := methodDescriptor(t, protoreflect.FullName(test.method))
		if method.IsStreamingClient() != test.clientStreaming || method.IsStreamingServer() != test.serverStreaming {
			t.Errorf("%s streaming mode = (%v,%v), want (%v,%v)", test.method,
				method.IsStreamingClient(), method.IsStreamingServer(), test.clientStreaming, test.serverStreaming)
		}
	}
}

func assertFields(t *testing.T, messageName string, names ...protoreflect.Name) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
	if err != nil {
		t.Fatalf("find message %s: %v", messageName, err)
	}
	message, ok := descriptor.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is %T, want message", messageName, descriptor)
	}
	for _, name := range names {
		if message.Fields().ByName(name) == nil {
			t.Errorf("%s missing field %s", messageName, name)
		}
	}
}

func assertExactFields(t *testing.T, messageName string, names ...protoreflect.Name) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
	if err != nil {
		t.Fatalf("find message %s: %v", messageName, err)
	}
	message, ok := descriptor.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is %T, want message", messageName, descriptor)
	}
	if message.Fields().Len() != len(names) {
		t.Fatalf("%s has %d fields, want exactly %d", messageName, message.Fields().Len(), len(names))
	}
	assertFields(t, messageName, names...)
}

func assertOneofFields(t *testing.T, messageName, oneofName string, names ...protoreflect.Name) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
	if err != nil {
		t.Fatalf("find message %s: %v", messageName, err)
	}
	message := descriptor.(protoreflect.MessageDescriptor)
	oneof := message.Oneofs().ByName(protoreflect.Name(oneofName))
	if oneof == nil || oneof.Fields().Len() != len(names) {
		t.Fatalf("%s.%s oneof does not contain the exact expected fields", messageName, oneofName)
	}
	for _, name := range names {
		field := message.Fields().ByName(name)
		if field == nil || field.ContainingOneof() != oneof {
			t.Errorf("%s.%s is not in oneof %s", messageName, name, oneofName)
		}
	}
}

func assertFieldKind(t *testing.T, messageName, fieldName string, want protoreflect.Kind) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
	if err != nil {
		t.Fatalf("find message %s: %v", messageName, err)
	}
	message := descriptor.(protoreflect.MessageDescriptor)
	field := message.Fields().ByName(protoreflect.Name(fieldName))
	if field == nil {
		t.Fatalf("%s missing field %s", messageName, fieldName)
	}
	if field.Kind() != want {
		t.Fatalf("%s.%s kind = %v, want %v", messageName, fieldName, field.Kind(), want)
	}
}

func assertFieldNumbers(t *testing.T, messageName string, numbers map[protoreflect.Name]protoreflect.FieldNumber) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
	if err != nil {
		t.Fatalf("find message %s: %v", messageName, err)
	}
	message, ok := descriptor.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is %T, want message", messageName, descriptor)
	}
	for name, want := range numbers {
		field := message.Fields().ByName(name)
		if field == nil {
			t.Errorf("%s missing field %s", messageName, name)
			continue
		}
		if field.Number() != want {
			t.Errorf("%s.%s field number = %d, want %d", messageName, name, field.Number(), want)
		}
	}
}

func assertEnumValues(t *testing.T, enumName string, names ...protoreflect.Name) {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(enumName))
	if err != nil {
		t.Fatalf("enum %s: %v", enumName, err)
	}
	enum, ok := descriptor.(protoreflect.EnumDescriptor)
	if !ok {
		t.Fatalf("descriptor %s is not an enum", enumName)
	}
	values := enum.Values()
	if values.Len() != len(names) {
		t.Fatalf("enum %s has %d values, want %d", enumName, values.Len(), len(names))
	}
	for index, name := range names {
		if values.Get(index).Name() != name || values.Get(index).Number() != protoreflect.EnumNumber(index) {
			t.Fatalf("enum %s value %d = %s/%d, want %s/%d", enumName, index,
				values.Get(index).Name(), values.Get(index).Number(), name, index)
		}
	}
}

func methodDescriptor(t *testing.T, fullName protoreflect.FullName) protoreflect.MethodDescriptor {
	t.Helper()
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(fullName)
	if err != nil {
		t.Fatalf("find method %s: %v", fullName, err)
	}
	method, ok := descriptor.(protoreflect.MethodDescriptor)
	if !ok {
		t.Fatalf("%s is %T, want method", fullName, descriptor)
	}
	return method
}
