package hostapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type protocolV2QueryAuthorityFunc func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error)

func (f protocolV2QueryAuthorityFunc) ResolveProtocolV2QueryAuthority(ctx context.Context, identity *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
	return f(ctx, identity)
}

type protocolV2QueryExecutorFunc func(context.Context, protocolV2QueryPlan) ([]map[string]any, error)

func (f protocolV2QueryExecutorFunc) ExecuteProtocolV2Query(ctx context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
	return f(ctx, plan)
}

func TestProtocolV2StableQueryUsesServerAuthorityAndBindsCursorShape(t *testing.T) {
	identity := testProtocolV2QueryIdentity()
	request := testProtocolV2TopicsQuery(t)
	request.Context.Extension = &protocolv2.ExtensionIdentity{ExtensionId: "forged.plugin"}
	request.Context.GrantedAuthority = []*protocolv2.AuthorityGrant{{Key: "database.raw_core", Source: "plugin"}}
	request.Fields = []string{"id", "title"}
	request.Page = &protocolv2.PageRequest{Limit: 2}

	var mu sync.Mutex
	plans := make([]protocolV2QueryPlan, 0, 2)
	executor := protocolV2QueryExecutorFunc(func(_ context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
		mu.Lock()
		plans = append(plans, plan)
		call := len(plans)
		mu.Unlock()
		if call == 1 {
			return []map[string]any{{"id": "3", "title": "three"}, {"id": "2", "title": "two"}, {"id": "1", "title": "one"}}, nil
		}
		return []map[string]any{{"id": "1", "title": "one"}}, nil
	})
	authority := protocolV2QueryAuthorityFunc(func(_ context.Context, resolved *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		if !proto.Equal(resolved, identity) {
			t.Fatalf("resolver received request-controlled identity: %#v", resolved)
		}
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, authority)
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)

	first := engine.execute(ctx, request)
	if first.GetError() != nil || len(first.GetRows()) != 2 || !first.GetPage().GetHasMore() || first.GetPage().GetNextCursor() == "" {
		t.Fatalf("first page = %#v", first)
	}
	if got := first.GetRows()[0].GetValue().AsMap(); got["id"] != "3" || got["title"] != "three" {
		t.Fatalf("first row = %#v", got)
	}

	secondRequest := proto.Clone(request).(*hostv2.QueryRequest)
	secondRequest.Page.Cursor = first.GetPage().GetNextCursor()
	second := engine.execute(ctx, secondRequest)
	if second.GetError() != nil || len(second.GetRows()) != 1 || second.GetPage().GetHasMore() {
		t.Fatalf("second page = %#v", second)
	}
	mu.Lock()
	if len(plans) != 2 || plans[0].Limit != 2 || plans[0].FetchLimit != 3 || plans[0].Offset != 0 || plans[1].Offset != 2 {
		t.Fatalf("pagination plans = %#v", plans)
	}
	mu.Unlock()

	wrongShape := proto.Clone(secondRequest).(*hostv2.QueryRequest)
	wrongShape.Fields = []string{"id"}
	invalid := engine.execute(ctx, wrongShape)
	if invalid.GetError().GetReason() != "host.query_cursor_invalid" {
		t.Fatalf("shape-bound cursor accepted: %#v", invalid)
	}
}

func TestProtocolV2StableQueryRejectsMissingStaleAndDeniedServerAuthority(t *testing.T) {
	request := testProtocolV2TopicsQuery(t)
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("unauthorized query executed")
		return nil, nil
	})
	tests := []struct {
		name      string
		ctx       context.Context
		authority ProtocolV2QueryAuthority
		err       error
		reason    string
	}{
		{name: "missing attestation", ctx: context.Background(), err: ErrProtocolV2QueryRuntimeStale, reason: "host.query_runtime_stale"},
		{name: "stale exact artifact", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), authority: ProtocolV2QueryAuthority{}, reason: "host.query_runtime_stale"},
		{name: "core views denied", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), authority: ProtocolV2QueryAuthority{ExactArtifact: true}, reason: "host.query_core_views_denied"},
		{name: "authority backend unavailable", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), err: errors.New("database offline"), reason: "host.query_authority_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := protocolV2QueryAuthorityFunc(func(_ context.Context, identity *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
				if test.name == "missing attestation" && identity != nil {
					t.Fatalf("missing attestation resolved identity: %#v", identity)
				}
				return test.authority, test.err
			})
			response := testProtocolV2QueryEngine(t, executor, resolver).execute(test.ctx, request)
			if response.GetError().GetReason() != test.reason {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestProtocolV2StableQueryValidatesCatalogShape(t *testing.T) {
	identity := testProtocolV2QueryIdentity()
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	resolver := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("invalid query shape executed")
		return nil, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, resolver)
	tests := []struct {
		name   string
		mutate func(*hostv2.QueryRequest)
		reason string
	}{
		{name: "result schema", mutate: func(r *hostv2.QueryRequest) { r.ResultSchemaVersion = "2" }, reason: "host.query_schema_mismatch"},
		{name: "field", mutate: func(r *hostv2.QueryRequest) { r.Fields = []string{"email"} }, reason: "host.query_field_unsupported"},
		{name: "filter", mutate: func(r *hostv2.QueryRequest) {
			r.Filters = []*hostv2.QueryFilter{{Field: "category_id", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "1")}}
		}, reason: "host.query_filter_invalid"},
		{name: "sort", mutate: func(r *hostv2.QueryRequest) { r.Sorts = []*hostv2.QuerySort{{Field: "title"}} }, reason: "host.query_sort_invalid"},
		{name: "limit", mutate: func(r *hostv2.QueryRequest) { r.Page = &protocolv2.PageRequest{Limit: 101} }, reason: "host.query_page_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testProtocolV2TopicsQuery(t)
			test.mutate(request)
			response := engine.execute(ctx, request)
			if response.GetError().GetReason() != test.reason {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestProtocolV2StableQueryBoundsAuthorizationAndExecutionDeadline(t *testing.T) {
	identity := testProtocolV2QueryIdentity()
	resolver := protocolV2QueryAuthorityFunc(func(ctx context.Context, _ *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > protocolV2QueryExecutionTimeout+100*time.Millisecond {
			t.Fatalf("authority deadline is not bounded: %v, %v", deadline, ok)
		}
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	executor := protocolV2QueryExecutorFunc(func(ctx context.Context, _ protocolV2QueryPlan) ([]map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	engine := testProtocolV2QueryEngine(t, executor, resolver)
	parent, cancel := context.WithTimeout(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), 20*time.Millisecond)
	defer cancel()
	response := engine.execute(parent, testProtocolV2TopicsQuery(t))
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED {
		t.Fatalf("deadline response = %#v", response)
	}
}

func TestGatewayBindsProtocolV2QueryRuntimeOnceAndFreezesSnapshot(t *testing.T) {
	resolver := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) { return nil, nil })
	runtime, err := newProtocolV2QueryRuntime(executor, resolver, stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2QueryRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2QueryRuntime(runtime); err == nil {
		t.Fatal("second query runtime bind must fail")
	}
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if gateway.queries == nil || !gateway.protocolV2QueriesFrozen {
		t.Fatalf("query snapshot was not frozen: %#v", gateway)
	}
	if err := gateway.BindProtocolV2QueryRuntime(runtime); err == nil {
		t.Fatal("query runtime replacement after registration must fail")
	}
}

func TestProtocolV2QueryServerReportsStableRuntimeUnavailable(t *testing.T) {
	request := testProtocolV2TopicsQuery(t)
	response, err := (&protocolV2QueryServer{core: &protocolV2Core{}}).Execute(context.Background(), request)
	if err != nil || response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
	request.QueryId = "sforum.unknown"
	response, err = (&protocolV2QueryServer{core: &protocolV2Core{}}).Execute(context.Background(), request)
	if err != nil || response.GetError().GetReason() != "host.query_unsupported" {
		t.Fatalf("unknown response = %#v, err = %v", response, err)
	}
}

func testProtocolV2QueryEngine(t *testing.T, executor protocolV2QueryExecutor, resolver ProtocolV2QueryAuthorityResolver) *protocolV2QueryEngine {
	t.Helper()
	engine, err := newProtocolV2QueryEngine(executor, resolver, stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func testProtocolV2TopicsQuery(t *testing.T) *hostv2.QueryRequest {
	t.Helper()
	return &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicTopicsList,
		PlanVersion:    QueryStableCorePlanVersion,
		ResultSchemaId: QueryPublicTopicResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
	}
}

func testProtocolV2QueryIdentity() *protocolv2.ExtensionIdentity {
	return &protocolv2.ExtensionIdentity{
		ExtensionId: "query.plugin", ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64),
		TrustGrantId: "41", RuntimeEpoch: 7, InstanceId: "query-instance",
	}
}

func queryParameter(t *testing.T, schemaID, value string) *protocolv2.TypedDocument {
	t.Helper()
	document, err := structpb.NewStruct(map[string]any{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: QueryStableCoreParameterSchemaV1, Value: document}
}

func TestParseProtocolV2QueryInt64ListContract(t *testing.T) {
	for _, value := range []string{"[1]", "[1,5,9]"} {
		if _, err := parseProtocolV2QueryInt64List(value); err != nil {
			t.Fatalf("canonical list %q rejected: %v", value, err)
		}
	}
	oversized := make([]string, 0, protocolV2QueryMaxBatchIDs+1)
	for id := 1; id <= protocolV2QueryMaxBatchIDs+1; id++ {
		oversized = append(oversized, strconv.Itoa(id))
	}
	invalid := map[string]string{
		"empty":       "[]",
		"unsorted":    "[9,1]",
		"duplicate":   "[1,1]",
		"zero":        "[0]",
		"negative":    "[-1]",
		"space":       "[1, 5]",
		"trailing":    "[1]x",
		"not-json":    "1,2",
		"object":      `{"1":1}`,
		"non-int64":   `["a"]`,
		"not-canonical": "[01]",
		"over-max":    "[" + strings.Join(oversized, ",") + "]",
	}
	for name, value := range invalid {
		if _, err := parseProtocolV2QueryInt64List(value); err == nil {
			t.Fatalf("invalid %s list %q accepted", name, value)
		}
	}
}

func TestProtocolV2QueryPlanBuildsInt64ListFilterForSafeUsersBatch(t *testing.T) {
	var batch *protocolV2QueryDefinition
	for index := range stableCoreProtocolV2QueryDefinitions() {
		if stableCoreProtocolV2QueryDefinitions()[index].ID == QuerySafeUsersBatch {
			batch = &stableCoreProtocolV2QueryDefinitions()[index]
			break
		}
	}
	if batch == nil {
		t.Fatal("stable catalog is missing the safe_users batch definition")
	}
	rejected := &hostv2.QueryRequest{
		QueryId: QuerySafeUsersBatch, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "user_id", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "[2,1]")}},
	}
	if _, detail := buildProtocolV2QueryPlan(*batch, rejected); detail == nil {
		t.Fatal("unsorted id list must be rejected at plan build")
	}
	accepted := &hostv2.QueryRequest{
		QueryId: QuerySafeUsersBatch, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "user_id", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "[1,2]")}},
	}
	plan, detail := buildProtocolV2QueryPlan(*batch, accepted)
	if detail != nil {
		t.Fatalf("canonical id list rejected: %v", detail)
	}
	if !reflect.DeepEqual(plan.Filters[0].Value, []int64{1, 2}) {
		t.Fatalf("int64 list filter value = %#v", plan.Filters[0].Value)
	}
	if fmt.Sprint(plan.ShapeDigest) == "" {
		t.Fatal("shape digest must stay non-empty for cursor identity")
	}
}
