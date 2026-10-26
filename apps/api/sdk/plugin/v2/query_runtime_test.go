package pluginv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestQueryRuntimeOptionalHandlersRemainUnimplemented(t *testing.T) {
	server := NewServer()
	if _, err := server.InvokeQuery(context.Background(), validQueryRuntimeRequest()); status.Code(err) != codes.Unimplemented {
		t.Fatalf("InvokeQuery without handler code = %v, want Unimplemented", status.Code(err))
	}
	if _, err := server.FilterQueryResult(context.Background(), validQueryResultFilterRequest()); status.Code(err) != codes.Unimplemented {
		t.Fatalf("FilterQueryResult without handler code = %v, want Unimplemented", status.Code(err))
	}

	server.WithQueryRuntimeHandlers(QueryRuntimeHandlers{InvokeQuery: func(
		context.Context, *QueryRuntimeCall,
	) ([]json.RawMessage, error) {
		return nil, nil
	}})
	if _, err := server.FilterQueryResult(context.Background(), validQueryResultFilterRequest()); status.Code(err) != codes.Unimplemented {
		t.Fatalf("omitted FilterQueryResult code = %v, want Unimplemented", status.Code(err))
	}
}

func TestQueryRuntimeRequiresExactNegotiatedFeatureBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name    string
		feature *protocolwire.ProtocolFeature
	}{
		{name: "missing"},
		{name: "wrong version", feature: &protocolwire.ProtocolFeature{Name: QueryRuntimeFeatureName, Version: "2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := NewServer().WithFeatures(QueryRuntimeProtocolFeature()).WithQueryRuntimeHandlers(QueryRuntimeHandlers{
				InvokeQuery: func(context.Context, *QueryRuntimeCall) ([]json.RawMessage, error) {
					calls.Add(1)
					return nil, nil
				},
				FilterQueryResult: func(context.Context, *QueryResultFilterRuntimeCall) ([]json.RawMessage, error) {
					calls.Add(1)
					return nil, nil
				},
			})
			handshake := validHandshakeRequest()
			if test.feature != nil {
				handshake.HostFeatures = append(handshake.HostFeatures, test.feature)
			}
			response, err := server.Handshake(context.Background(), handshake)
			if err != nil || response.GetError() != nil ||
				hasProtocolFeature(response.GetSelectedFeatures(), QueryRuntimeFeatureName, QueryRuntimeFeatureVersion) {
				t.Fatalf("handshake = %#v, %v", response, err)
			}
			if _, err := server.InvokeQuery(context.Background(), validQueryRuntimeRequest()); status.Code(err) != codes.Unimplemented {
				t.Fatalf("InvokeQuery code = %v, want Unimplemented", status.Code(err))
			}
			if _, err := server.FilterQueryResult(context.Background(), validQueryResultFilterRequest()); status.Code(err) != codes.Unimplemented {
				t.Fatalf("FilterQueryResult code = %v, want Unimplemented", status.Code(err))
			}
			if calls.Load() != 0 {
				t.Fatalf("unnegotiated feature reached handlers %d times", calls.Load())
			}
		})
	}
}

func TestQueryRuntimeDispatchEchoesExactIdentityAndPreservesIntegerLexeme(t *testing.T) {
	wantQueryRow := []byte(`{"a":{"b":2},"z":9007199254740993}`)
	wantFilteredRow := []byte(`{"a":{"b":2},"filtered":true,"z":9007199254740993}`)
	server := NewServer().WithFeatures(QueryRuntimeProtocolFeature()).WithQueryRuntimeHandlers(QueryRuntimeHandlers{
		InvokeQuery: func(_ context.Context, call *QueryRuntimeCall) ([]json.RawMessage, error) {
			if call.Context.GetActor() != nil || len(call.Context.GetGrantedAuthority()) != 0 ||
				call.Binding.GetHandler() != "demo.v2.query.items" || call.Plan.GetFetchLimit() != 1 {
				t.Fatalf("unsafe or drifted query call: %#v", call)
			}
			return []json.RawMessage{json.RawMessage(`{"z":9007199254740993,"a":{"b":2}}`)}, nil
		},
		FilterQueryResult: func(_ context.Context, call *QueryResultFilterRuntimeCall) ([]json.RawMessage, error) {
			if call.Binding.GetHandler() != "demo.v2.query.items.decorate" || len(call.Rows) != 1 ||
				!bytes.Equal(call.Rows[0], wantQueryRow) {
				t.Fatalf("unexpected result-filter call: %#v", call)
			}
			return []json.RawMessage{json.RawMessage(`{"z":9007199254740993,"filtered":true,"a":{"b":2}}`)}, nil
		},
	})
	handshake := validHandshakeRequest()
	handshake.HostFeatures = append(handshake.HostFeatures, QueryRuntimeProtocolFeature())
	handshakeResponse, err := server.Handshake(context.Background(), handshake)
	if err != nil || handshakeResponse.GetError() != nil ||
		!hasProtocolFeature(handshakeResponse.GetSelectedFeatures(), QueryRuntimeFeatureName, QueryRuntimeFeatureVersion) {
		t.Fatalf("query runtime handshake = %#v, %v", handshakeResponse, err)
	}

	request := validQueryRuntimeRequest()
	response, err := server.InvokeQuery(context.Background(), request)
	if err != nil || response.GetError() != nil || response.GetSuccess() == nil ||
		!proto.Equal(response.GetBinding(), request.GetBinding()) ||
		response.GetShapeDigest() != request.GetPlan().GetShapeDigest() ||
		len(response.GetSuccess().GetRows()) != 1 ||
		!bytes.Equal(response.GetSuccess().GetRows()[0].GetCanonicalJson(), wantQueryRow) {
		t.Fatalf("query response = %#v, %v", response, err)
	}
	decoded, err := DecodeQueryRuntimeRow(response.GetSuccess().GetRows()[0].GetCanonicalJson())
	if err != nil {
		t.Fatal(err)
	}
	number, ok := decoded["z"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("integer lexeme = %#v", decoded["z"])
	}

	filterRequest := validQueryResultFilterRequest()
	filterRequest.Input = response.GetSuccess()
	filterResponse, err := server.FilterQueryResult(context.Background(), filterRequest)
	if err != nil || filterResponse.GetError() != nil || filterResponse.GetSuccess() == nil ||
		!proto.Equal(filterResponse.GetBinding(), filterRequest.GetBinding()) ||
		filterResponse.GetShapeDigest() != filterRequest.GetPlan().GetShapeDigest() ||
		len(filterResponse.GetSuccess().GetRows()) != 1 ||
		!bytes.Equal(filterResponse.GetSuccess().GetRows()[0].GetCanonicalJson(), wantFilteredRow) {
		t.Fatalf("filter response = %#v, %v", filterResponse, err)
	}
}

func TestQueryRuntimeAcceptsExactPackagePathSchemaReference(t *testing.T) {
	server := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{
		InvokeQuery: func(context.Context, *QueryRuntimeCall) ([]json.RawMessage, error) {
			return []json.RawMessage{json.RawMessage(`{"id":1}`)}, nil
		},
		FilterQueryResult: func(_ context.Context, call *QueryResultFilterRuntimeCall) ([]json.RawMessage, error) {
			return call.Rows, nil
		},
	})
	query := validQueryRuntimeRequest()
	query.Binding.ResultSchema = "schemas/items.json"
	queryResponse, err := server.InvokeQuery(context.Background(), query)
	if err != nil || queryResponse.GetError() != nil || queryResponse.GetSuccess() == nil {
		t.Fatalf("path query response = %#v, %v", queryResponse, err)
	}
	filter := validQueryResultFilterRequest()
	filter.Binding.ResultSchema = "schemas/items.json"
	filterResponse, err := server.FilterQueryResult(context.Background(), filter)
	if err != nil || filterResponse.GetError() != nil || filterResponse.GetSuccess() == nil {
		t.Fatalf("path filter response = %#v, %v", filterResponse, err)
	}
}

func TestQueryRuntimeRejectsAuthorityProjectionBeforeHandler(t *testing.T) {
	var calls atomic.Int32
	server := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{InvokeQuery: func(
		context.Context, *QueryRuntimeCall,
	) ([]json.RawMessage, error) {
		calls.Add(1)
		return nil, nil
	}})
	tests := []struct {
		name   string
		change func(*protocolwire.RequestContext)
	}{
		{"actor", func(value *protocolwire.RequestContext) { value.Actor = &protocolwire.Actor{UserId: 7} }},
		{"authority", func(value *protocolwire.RequestContext) {
			value.GrantedAuthority = []*protocolwire.AuthorityGrant{{Key: "raw_database"}}
		}},
		{"idempotency", func(value *protocolwire.RequestContext) { value.IdempotencyKey = "secret-key" }},
		{"command delegation", func(value *protocolwire.RequestContext) {
			value.HostCommandDelegations = []*protocolwire.HostCommandDelegation{{Token: "secret-token"}}
		}},
		{"query delegation", func(value *protocolwire.RequestContext) {
			value.HostQueryDelegations = []*protocolwire.HostQueryDelegation{{Token: "secret-token"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validQueryRuntimeRequest()
			test.change(request.Context)
			response, err := server.InvokeQuery(context.Background(), request)
			if err != nil || response.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED ||
				response.GetError().GetReason() != "query_runtime.context_authority_forbidden" {
				t.Fatalf("response = %#v, %v", response, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe projection reached handler %d times", calls.Load())
	}
}

func TestQueryRuntimeRejectsPlanAndRowBounds(t *testing.T) {
	var calls atomic.Int32
	server := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{InvokeQuery: func(
		context.Context, *QueryRuntimeCall,
	) ([]json.RawMessage, error) {
		calls.Add(1)
		return []json.RawMessage{json.RawMessage(`{"id":1}`)}, nil
	}})
	tests := []struct {
		name   string
		change func(*pluginwire.QueryInvocationRequest)
		code   protocolwire.ErrorCode
	}{
		{"relations remain host only", func(value *pluginwire.QueryInvocationRequest) {
			value.Plan.Relations = []string{"author"}
		}, protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION},
		{"too many fields", func(value *pluginwire.QueryInvocationRequest) {
			value.Plan.Fields = make([]string, maximumQueryRuntimeFields+1)
			for index := range value.Plan.Fields {
				value.Plan.Fields[index] = fmt.Sprintf("field_%d", index)
			}
		}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
		{"fetch mismatch", func(value *pluginwire.QueryInvocationRequest) {
			value.Plan.FetchLimit = 2
		}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
		{"duplicate filter", func(value *pluginwire.QueryInvocationRequest) {
			value.Plan.Filters = []*pluginwire.QueryRuntimeFilter{{Field: "state", Value: "open"}, {Field: "state", Value: "closed"}}
		}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validQueryRuntimeRequest()
			test.change(request)
			response, err := server.InvokeQuery(context.Background(), request)
			if err != nil || response.GetError().GetCode() != test.code || response.GetSuccess() != nil {
				t.Fatalf("response = %#v, %v", response, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid plans reached handler %d times", calls.Load())
	}

	overflow := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{InvokeQuery: func(
		context.Context, *QueryRuntimeCall,
	) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`)}, nil
	}})
	response, err := overflow.InvokeQuery(context.Background(), validQueryRuntimeRequest())
	if err != nil || response.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE ||
		response.GetSuccess() != nil {
		t.Fatalf("overflow response = %#v, %v", response, err)
	}
}

func TestQueryResultFilterRejectsNonCanonicalInputBeforeHandler(t *testing.T) {
	var calls atomic.Int32
	server := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{FilterQueryResult: func(
		context.Context, *QueryResultFilterRuntimeCall,
	) ([]json.RawMessage, error) {
		calls.Add(1)
		return nil, nil
	}})
	for _, row := range [][]byte{
		[]byte(` {"id":1}`),
		[]byte(`{"z":1,"a":2}`),
		[]byte(`{"id":1,"id":2}`),
	} {
		request := validQueryResultFilterRequest()
		request.Input.Rows[0].CanonicalJson = row
		response, err := server.FilterQueryResult(context.Background(), request)
		if err != nil || response.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT ||
			response.GetError().GetReason() != "query_filter.input_invalid" || response.GetSuccess() != nil {
			t.Fatalf("input %q response = %#v, %v", row, response, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("non-canonical rows reached filter handler %d times", calls.Load())
	}
}

func TestQueryResultFilterRejectsCardinalityChange(t *testing.T) {
	server := handshakenQueryRuntimeServer(t, QueryRuntimeHandlers{FilterQueryResult: func(
		context.Context, *QueryResultFilterRuntimeCall,
	) ([]json.RawMessage, error) {
		return nil, nil
	}})
	response, err := server.FilterQueryResult(context.Background(), validQueryResultFilterRequest())
	if err != nil || response.GetError().GetReason() != "query_filter.cardinality_mismatch" ||
		response.GetSuccess() != nil {
		t.Fatalf("cardinality response = %#v, %v", response, err)
	}
}

func TestCanonicalQueryRuntimeRowRejectsAmbiguousOrUnboundedJSON(t *testing.T) {
	canonical, err := CanonicalQueryRuntimeRow([]byte(` { "z": 9007199254740993, "a": {"y":2,"x":1} } `))
	if err != nil || string(canonical) != `{"a":{"x":1,"y":2},"z":9007199254740993}` {
		t.Fatalf("canonical = %s, %v", canonical, err)
	}
	invalid := [][]byte{
		[]byte(`[]`),
		[]byte(`{"a":1} trailing`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"a":{"b":1,"b":2}}`),
		[]byte(`{"n":NaN}`),
		[]byte(`{"n":Infinity}`),
		[]byte(`{"n":01}`),
		{0xff, 0xfe},
	}
	deep := strings.Repeat(`{"a":`, maximumQueryRuntimeJSONDepth+2) + `0` +
		strings.Repeat(`}`, maximumQueryRuntimeJSONDepth+2)
	invalid = append(invalid, []byte(deep), make([]byte, maximumQueryRuntimeResultBytes+1))
	for index, row := range invalid {
		if _, err := CanonicalQueryRuntimeRow(row); err == nil {
			t.Errorf("invalid row %d was accepted", index)
		}
	}
}

func handshakenQueryRuntimeServer(t *testing.T, handlers QueryRuntimeHandlers) *Server {
	t.Helper()
	server := NewServer().WithFeatures(QueryRuntimeProtocolFeature()).WithQueryRuntimeHandlers(handlers)
	request := validHandshakeRequest()
	request.HostFeatures = append(request.HostFeatures, QueryRuntimeProtocolFeature())
	response, err := server.Handshake(context.Background(), request)
	if err != nil || response.GetError() != nil {
		t.Fatalf("handshake = %#v, %v", response, err)
	}
	return server
}

func validQueryRuntimeRequest() *pluginwire.QueryInvocationRequest {
	contextValue := proto.Clone(validHandshakeRequest().GetContext()).(*protocolwire.RequestContext)
	contextValue.Locale = "zh-CN"
	contextValue.Deadline = timestamppb.New(time.Now().Add(time.Minute))
	contextValue.Trace = &protocolwire.TraceContext{
		TraceId: "0123456789abcdef0123456789abcdef", SpanId: "0123456789abcdef",
		Traceparent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
	}
	return &pluginwire.QueryInvocationRequest{
		Context: contextValue,
		Binding: &pluginwire.QueryRuntimeBinding{
			QueryId: "demo.v2.items", ContractVersion: "demo.v2.items@1",
			PlanVersion: "demo.v2.items.plan@1", ResultSchema: "demo.v2.items.result@1",
			Handler: "demo.v2.query.items",
		},
		Plan: &pluginwire.QueryRuntimePlan{
			ShapeDigest: strings.Repeat("a", 64), Fields: []string{"id"}, Locale: "zh-CN",
			Pagination: &pluginwire.QueryRuntimePagination{Mode: "none", Limit: 1}, FetchLimit: 1,
		},
	}
}

func validQueryResultFilterRequest() *pluginwire.QueryResultFilterRequest {
	query := validQueryRuntimeRequest()
	return &pluginwire.QueryResultFilterRequest{
		Context: query.GetContext(), Plan: query.GetPlan(),
		Binding: &pluginwire.QueryResultFilterRuntimeBinding{
			FilterId: "demo.v2.items.decorate", FilterContractVersion: "demo.v2.items.decorate@1",
			QueryId: "demo.v2.items", QueryContractVersion: "demo.v2.items@1",
			QueryPlanVersion: "demo.v2.items.plan@1", ResultSchema: "demo.v2.items.result@1",
			Handler: "demo.v2.query.items.decorate",
		},
		Input: &pluginwire.QueryRuntimeRows{Rows: []*pluginwire.QueryRuntimeRow{{CanonicalJson: []byte(`{"id":1}`)}}},
	}
}

func hasProtocolFeature(values []*protocolwire.ProtocolFeature, name, version string) bool {
	for _, value := range values {
		if value.GetName() == name && value.GetVersion() == version {
			return true
		}
	}
	return false
}
