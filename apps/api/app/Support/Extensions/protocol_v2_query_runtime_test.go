package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtocolV2QueryRuntimeInvokeAndFilterExactBinding(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.items", ContractVersion: "plugin.query.demo.items@1",
		Entity: "item", PlanVersion: "plugin.query.demo.items.plan@1",
		Fields: []string{"id", "title"}, Pagination: "offset",
		ResultSchema: "plugin.query.demo.items.result@1", PermissionPolicy: "public",
		Handler: "plugin.query.demo.items", IdentityFields: []string{"id"},
		DefaultSort: []extensions.ManifestQuerySort{{Field: "id", Descending: true}},
	}
	filter := extensions.ManifestQueryResultFilter{
		ID: "plugin.query.demo.items.mask", ContractVersion: "plugin.query.demo.items.mask@1",
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, Handler: "plugin.query.demo.items.mask",
		FailurePolicy: "fail_closed", TimeoutMS: 500,
	}
	var invoked *pluginwire.QueryInvocationRequest
	var filtered *pluginwire.QueryResultFilterRequest
	client := newProtocolV2QueryTestClient(t, declaration, filter, func(
		_ context.Context, request *pluginwire.QueryInvocationRequest,
	) (*pluginwire.QueryInvocationResponse, error) {
		invoked = request
		return &pluginwire.QueryInvocationResponse{
			Context: protocolV2QueryTestResponseContext(request.GetContext()),
			Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
			Outcome: &pluginwire.QueryInvocationResponse_Success{
				Success: &pluginwire.QueryRuntimeRows{Rows: []*pluginwire.QueryRuntimeRow{
					{CanonicalJson: []byte(`{"id":"1","title":"exact"}`)},
				}},
			},
		}, nil
	}, func(
		_ context.Context, request *pluginwire.QueryResultFilterRequest,
	) (*pluginwire.QueryResultFilterResponse, error) {
		filtered = request
		return &pluginwire.QueryResultFilterResponse{
			Context: protocolV2QueryTestResponseContext(request.GetContext()),
			Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
			Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
		}, nil
	})
	plan := protocolV2QueryTestPlan(declaration)
	rows, err := client.InvokeQuery(context.Background(), VersionedQueryRequest{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: declaration.Handler, Plan: plan, FetchLimit: 11, Timeout: 500 * time.Millisecond,
	})
	if err != nil || len(rows) != 1 || rows[0]["id"] != "1" || rows[0]["title"] != "exact" {
		t.Fatalf("InvokeQuery rows=%#v err=%v", rows, err)
	}
	if invoked.GetContext().GetActor() != nil || invoked.GetContext().GetGrantedAuthority() != nil ||
		invoked.GetContext().GetIdempotencyKey() != "" || invoked.GetContext().GetTrace() != nil ||
		len(invoked.GetContext().GetHostCommandDelegations()) != 0 ||
		len(invoked.GetContext().GetHostQueryDelegations()) != 0 ||
		invoked.GetBinding().GetHandler() != declaration.Handler ||
		invoked.GetPlan().GetShapeDigest() != plan.ShapeDigest {
		t.Fatalf("InvokeQuery request drifted: %#v", invoked)
	}
	// 整数词素必须无损往返。
	plan.ShapeDigest = strings.Repeat("b", 64)
	filteredRows, err := client.FilterQueryResult(context.Background(), VersionedQueryResultFilterRequest{
		FilterID: filter.ID, FilterContractVersion: filter.ContractVersion,
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: filter.Handler, Plan: plan,
		Rows:    []queryregistry.QueryRow{{"id": json.Number("9007199254740993"), "title": "keep"}},
		Timeout: 500 * time.Millisecond,
	})
	if err != nil || len(filteredRows) != 1 || filteredRows[0]["id"] != json.Number("9007199254740993") {
		t.Fatalf("FilterQueryResult rows=%#v err=%v", filteredRows, err)
	}
	if filtered.GetBinding().GetFilterId() != filter.ID ||
		filtered.GetContext().GetActor() != nil || filtered.GetContext().GetGrantedAuthority() != nil ||
		filtered.GetContext().GetIdempotencyKey() != "" || filtered.GetContext().GetTrace() != nil ||
		len(filtered.GetContext().GetHostCommandDelegations()) != 0 ||
		len(filtered.GetContext().GetHostQueryDelegations()) != 0 {
		t.Fatalf("FilterQueryResult request drifted: %#v", filtered)
	}
	if _, err := client.InvokeQuery(context.Background(), VersionedQueryRequest{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: "plugin.query.demo.drift", Plan: plan, FetchLimit: 1,
	}); err == nil {
		t.Fatal("drifted handler reached transport")
	}
}

func TestProtocolV2QueryRuntimeRejectsResponseIdentityDrift(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.items", ContractVersion: "plugin.query.demo.items@1",
		Entity: "item", PlanVersion: "plugin.query.demo.items.plan@1",
		Fields: []string{"id"}, Pagination: "none", ResultSchema: "plugin.query.demo.items.result@1",
		PermissionPolicy: "public", Handler: "plugin.query.demo.items", IdentityFields: []string{"id"},
		DefaultSort: []extensions.ManifestQuerySort{{Field: "id"}},
	}
	filter := extensions.ManifestQueryResultFilter{
		ID: "plugin.query.demo.items.mask", ContractVersion: "plugin.query.demo.items.mask@1",
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, Handler: "plugin.query.demo.items.mask",
		FailurePolicy: "fail_closed", TimeoutMS: 500,
	}
	plan := protocolV2QueryTestPlan(declaration)

	t.Run("response context", func(t *testing.T) {
		client := newProtocolV2QueryTestClient(t, declaration, filter, func(
			_ context.Context, request *pluginwire.QueryInvocationRequest,
		) (*pluginwire.QueryInvocationResponse, error) {
			return &pluginwire.QueryInvocationResponse{
				Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
				Outcome: &pluginwire.QueryInvocationResponse_Success{Success: &pluginwire.QueryRuntimeRows{
					Rows: []*pluginwire.QueryRuntimeRow{{CanonicalJson: []byte(`{"id":"1"}`)}},
				}},
			}, nil
		}, nil)
		if _, err := client.InvokeQuery(t.Context(), VersionedQueryRequest{
			QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
			PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
			Handler: declaration.Handler, Plan: plan, FetchLimit: 1,
		}); err == nil {
			t.Fatal("query accepted a missing response context")
		}
	})

	t.Run("filter binding", func(t *testing.T) {
		client := newProtocolV2QueryTestClient(t, declaration, filter, nil, func(
			_ context.Context, request *pluginwire.QueryResultFilterRequest,
		) (*pluginwire.QueryResultFilterResponse, error) {
			binding := *request.GetBinding()
			binding.QueryPlanVersion += ".drift"
			return &pluginwire.QueryResultFilterResponse{
				Context: protocolV2QueryTestResponseContext(request.GetContext()),
				Binding: &binding, ShapeDigest: request.GetPlan().GetShapeDigest(),
				Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
			}, nil
		})
		if _, err := client.FilterQueryResult(t.Context(), VersionedQueryResultFilterRequest{
			FilterID: filter.ID, FilterContractVersion: filter.ContractVersion,
			QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
			QueryPlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
			Handler: filter.Handler, Plan: plan, Rows: []queryregistry.QueryRow{{"id": "1"}},
		}); err == nil {
			t.Fatal("query filter accepted a drifted response binding")
		}
	})
}

func TestProtocolV2QueryRuntimeFilterKeepsFetchLimitForShortPage(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.items", ContractVersion: "plugin.query.demo.items@1",
		Entity: "item", PlanVersion: "plugin.query.demo.items.plan@1",
		Fields: []string{"id"}, Pagination: "offset", ResultSchema: "plugin.query.demo.items.result@1",
		PermissionPolicy: "public", Handler: "plugin.query.demo.items", IdentityFields: []string{"id"},
		DefaultSort: []extensions.ManifestQuerySort{{Field: "id"}},
	}
	filter := extensions.ManifestQueryResultFilter{
		ID: "plugin.query.demo.items.mask", ContractVersion: "plugin.query.demo.items.mask@1",
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, Handler: "plugin.query.demo.items.mask",
		FailurePolicy: "fail_closed", TimeoutMS: 500,
	}
	client := newProtocolV2QueryTestClient(t, declaration, filter, nil, func(
		_ context.Context, request *pluginwire.QueryResultFilterRequest,
	) (*pluginwire.QueryResultFilterResponse, error) {
		if request.GetPlan().GetPagination().GetLimit() != 2 || request.GetPlan().GetFetchLimit() != 3 ||
			len(request.GetInput().GetRows()) != 1 {
			t.Fatalf("short-page filter plan drifted: %#v", request.GetPlan())
		}
		return &pluginwire.QueryResultFilterResponse{
			Context: protocolV2QueryTestResponseContext(request.GetContext()),
			Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
			Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
		}, nil
	})
	plan := protocolV2QueryTestPlan(declaration)
	plan.Pagination.Limit = 2
	rows, err := client.FilterQueryResult(t.Context(), VersionedQueryResultFilterRequest{
		FilterID: filter.ID, FilterContractVersion: filter.ContractVersion,
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: filter.Handler, Plan: plan, Rows: []queryregistry.QueryRow{{"id": "tail"}},
	})
	if err != nil || len(rows) != 1 || rows[0]["id"] != "tail" {
		t.Fatalf("short-page filter rows=%#v err=%v", rows, err)
	}
}

func TestProtocolV2QueryRuntimeFilterUsesSingleFetchForNonePagination(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.item", ContractVersion: "plugin.query.demo.item@1",
		Entity: "item", PlanVersion: "plugin.query.demo.item.plan@1",
		Fields: []string{"id"}, Pagination: "none", ResultSchema: "plugin.query.demo.item.result@1",
		PermissionPolicy: "public", Handler: "plugin.query.demo.item", IdentityFields: []string{"id"},
		DefaultSort: []extensions.ManifestQuerySort{{Field: "id"}},
	}
	filter := extensions.ManifestQueryResultFilter{
		ID: "plugin.query.demo.item.mask", ContractVersion: "plugin.query.demo.item.mask@1",
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, Handler: "plugin.query.demo.item.mask",
		FailurePolicy: "fail_closed", TimeoutMS: 500,
	}
	client := newProtocolV2QueryTestClient(t, declaration, filter, nil, func(
		_ context.Context, request *pluginwire.QueryResultFilterRequest,
	) (*pluginwire.QueryResultFilterResponse, error) {
		if request.GetPlan().GetPagination().GetMode() != queryregistry.PaginationNone ||
			request.GetPlan().GetPagination().GetLimit() != 1 || request.GetPlan().GetFetchLimit() != 1 {
			t.Fatalf("none filter plan drifted: %#v", request.GetPlan())
		}
		return &pluginwire.QueryResultFilterResponse{
			Context: protocolV2QueryTestResponseContext(request.GetContext()),
			Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
			Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
		}, nil
	})
	plan := protocolV2QueryTestPlan(declaration)
	plan.Pagination.Limit = 1
	rows, err := client.FilterQueryResult(t.Context(), VersionedQueryResultFilterRequest{
		FilterID: filter.ID, FilterContractVersion: filter.ContractVersion,
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: filter.Handler, Plan: plan, Rows: []queryregistry.QueryRow{{"id": "only"}},
	})
	if err != nil || len(rows) != 1 || rows[0]["id"] != "only" {
		t.Fatalf("none filter rows=%#v err=%v", rows, err)
	}
}

func TestProtocolV2QueryRuntimeHandshakeRequiresExactFeature(t *testing.T) {
	executable := extensions.ManifestQuery{Handler: "plugin.query.demo.items"}
	filter := extensions.ManifestQueryResultFilter{ID: "plugin.query.demo.items.mask"}
	exact := &protocolwire.ProtocolFeature{
		Name: protocolV2QueryRuntimeFeatureName, Version: protocolV2QueryRuntimeFeatureVersion,
	}
	for _, test := range []struct {
		name       string
		queries    []extensions.ManifestQuery
		filters    []extensions.ManifestQueryResultFilter
		selected   []*protocolwire.ProtocolFeature
		protocol   *protocolwire.ProtocolRange
		wantOffer  bool
		wantReject bool
	}{
		{name: "legacy declaration"},
		{name: "executable exact", queries: []extensions.ManifestQuery{executable}, selected: []*protocolwire.ProtocolFeature{exact}, wantOffer: true},
		{name: "executable missing", queries: []extensions.ManifestQuery{executable}, wantOffer: true, wantReject: true},
		{name: "executable wrong version", queries: []extensions.ManifestQuery{executable}, selected: []*protocolwire.ProtocolFeature{{Name: protocolV2QueryRuntimeFeatureName, Version: "2"}}, wantOffer: true, wantReject: true},
		{name: "executable duplicate", queries: []extensions.ManifestQuery{executable}, selected: []*protocolwire.ProtocolFeature{exact, exact}, wantOffer: true, wantReject: true},
		{name: "filter only exact", filters: []extensions.ManifestQueryResultFilter{filter}, selected: []*protocolwire.ProtocolFeature{exact}, wantOffer: true},
		{name: "filter only missing", filters: []extensions.ManifestQueryResultFilter{filter}, wantOffer: true, wantReject: true},
		{name: "unoffered feature", selected: []*protocolwire.ProtocolFeature{{Name: "unknown.feature", Version: "1"}}, wantReject: true},
		{name: "selected minor range", protocol: &protocolwire.ProtocolRange{Protocol: protocolV2Name, Major: 2, MaxMinor: 1}, wantReject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &protocolV2QueryHandshakeTestServer{selected: test.selected, protocol: test.protocol}
			client := newProtocolV2QueryHandshakeTestClient(t, server, protocolV2ClientConfig{
				identity: &protocolwire.ExtensionIdentity{
					ExtensionId: "plugin.query.demo", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
					TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "query-runtime",
				},
				instance: "query-runtime", queries: test.queries, queryResultFilters: test.filters,
			})
			err := client.Handshake(t.Context())
			var mismatch *ProtocolV2Error
			if test.wantReject != errors.As(err, &mismatch) {
				t.Fatalf("Handshake error=%v, wantReject=%v", err, test.wantReject)
			}
			offered := protocolV2SelectedFeature(
				server.offered, protocolV2QueryRuntimeFeatureName, protocolV2QueryRuntimeFeatureVersion,
			)
			if offered != test.wantOffer {
				t.Fatalf("query.runtime offered=%v, want %v: %#v", offered, test.wantOffer, server.offered)
			}
			if test.wantOffer {
				for _, feature := range server.offered {
					if feature.GetName() == protocolV2QueryRuntimeFeatureName && !feature.GetRequired() {
						t.Fatal("query.runtime offer was not marked required")
					}
				}
			}
		})
	}
}

func TestProtocolV2QueryRuntimeMapsDeadlineAndCancellation(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.items", ContractVersion: "plugin.query.demo.items@1",
		Entity: "item", PlanVersion: "plugin.query.demo.items.plan@1",
		Fields: []string{"id"}, Pagination: "none", ResultSchema: "plugin.query.demo.items.result@1",
		PermissionPolicy: "public", Handler: "plugin.query.demo.items",
		IdentityFields: []string{"id"},
		DefaultSort:    []extensions.ManifestQuerySort{{Field: "id"}},
	}
	for _, test := range []struct {
		name string
		code codes.Code
		want error
	}{
		{name: "deadline", code: codes.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "canceled", code: codes.Canceled, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2QueryTestClient(t, declaration, extensions.ManifestQueryResultFilter{},
				func(context.Context, *pluginwire.QueryInvocationRequest) (*pluginwire.QueryInvocationResponse, error) {
					return nil, status.Error(test.code, "provider stopped")
				}, nil)
			_, err := client.InvokeQuery(context.Background(), VersionedQueryRequest{
				QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
				PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
				Handler: declaration.Handler, Plan: protocolV2QueryTestPlan(declaration), FetchLimit: 1,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func protocolV2QueryTestPlan(declaration extensions.ManifestQuery) queryregistry.QueryPlan {
	return queryregistry.QueryPlan{
		ShapeDigest: strings.Repeat("a", 64),
		Fields:      append([]string(nil), declaration.Fields...),
		Locale:      "zh-CN",
		Pagination:  queryregistry.PaginationPlan{Mode: declaration.Pagination, Limit: 10},
		Query: queryregistry.QueryContribution{
			QueryDeclaration: queryregistry.QueryDeclaration{
				ID: declaration.ID, ContractVersion: declaration.ContractVersion,
				PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
				Handler: declaration.Handler, Fields: append([]string(nil), declaration.Fields...),
			},
		},
	}
}

type protocolV2QueryTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	invoke func(context.Context, *pluginwire.QueryInvocationRequest) (*pluginwire.QueryInvocationResponse, error)
	filter func(context.Context, *pluginwire.QueryResultFilterRequest) (*pluginwire.QueryResultFilterResponse, error)
}

type protocolV2QueryHandshakeTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	selected []*protocolwire.ProtocolFeature
	offered  []*protocolwire.ProtocolFeature
	protocol *protocolwire.ProtocolRange
}

func (s *protocolV2QueryHandshakeTestServer) Handshake(
	_ context.Context,
	request *protocolwire.HandshakeRequest,
) (*protocolwire.HandshakeResponse, error) {
	s.offered = request.GetHostFeatures()
	selectedProtocol := s.protocol
	if selectedProtocol == nil {
		selectedProtocol = &protocolwire.ProtocolRange{Protocol: protocolV2Name, Major: 2}
	}
	return &protocolwire.HandshakeResponse{
		SelectedProtocol: selectedProtocol,
		SelectedFeatures: s.selected,
	}, nil
}

func (s *protocolV2QueryTestServer) InvokeQuery(
	ctx context.Context, request *pluginwire.QueryInvocationRequest,
) (*pluginwire.QueryInvocationResponse, error) {
	if s.invoke == nil {
		return nil, status.Error(codes.Unimplemented, "query")
	}
	return s.invoke(ctx, request)
}

func (s *protocolV2QueryTestServer) FilterQueryResult(
	ctx context.Context, request *pluginwire.QueryResultFilterRequest,
) (*pluginwire.QueryResultFilterResponse, error) {
	if s.filter == nil {
		return nil, status.Error(codes.Unimplemented, "filter")
	}
	return s.filter(ctx, request)
}

func newProtocolV2QueryTestClient(
	t *testing.T,
	query extensions.ManifestQuery,
	filter extensions.ManifestQueryResultFilter,
	invoke func(context.Context, *pluginwire.QueryInvocationRequest) (*pluginwire.QueryInvocationResponse, error),
	filterFn func(context.Context, *pluginwire.QueryResultFilterRequest) (*pluginwire.QueryResultFilterResponse, error),
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2QueryTestServer{invoke: invoke, filter: filterFn})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///query-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "plugin.query.demo", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "query-runtime",
	}
	filters := []extensions.ManifestQueryResultFilter{}
	if filter.ID != "" {
		filters = append(filters, filter)
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, instance: identity.InstanceId,
		queries: []extensions.ManifestQuery{query}, queryResultFilters: filters,
	})
}

func newProtocolV2QueryHandshakeTestClient(
	t *testing.T,
	server pluginwire.PluginRuntimeServiceServer,
	config protocolV2ClientConfig,
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///query-handshake-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), config)
}

func protocolV2QueryTestResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	return &protocolwire.ResponseContext{
		RequestId: request.GetRequestId(), Trace: request.GetTrace(),
		ServerTime: timestamppb.Now(), Extension: request.GetExtension(),
	}
}
