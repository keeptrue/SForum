package extensionsruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
)

func TestProtocolStarterQueryCallsUseExactInstanceInsteadOfActivePointer(t *testing.T) {
	declaration := extensions.ManifestQuery{
		ID: "plugin.query.demo.items", ContractVersion: "plugin.query.demo.items@1",
		Entity: "item", PlanVersion: "plugin.query.demo.items.plan@1",
		Fields: []string{"id"}, Pagination: queryregistry.PaginationNone,
		ResultSchema: "plugin.query.demo.items.result@1", PermissionPolicy: queryregistry.PermissionPolicyPublic,
		Handler: "plugin.query.demo.items", IdentityFields: []string{"id"},
		DefaultSort: []extensions.ManifestQuerySort{{Field: "id"}},
	}
	filter := extensions.ManifestQueryResultFilter{
		ID: "plugin.query.demo.items.mask", ContractVersion: "plugin.query.demo.items.mask@1",
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, Handler: "plugin.query.demo.items.mask",
		FailurePolicy: queryregistry.ResultFilterFailClosed, TimeoutMS: 500,
	}
	var exactCalls, activeCalls atomic.Int32
	exactClient := newProtocolV2QueryTestClient(t, declaration, filter,
		func(_ context.Context, request *pluginwire.QueryInvocationRequest) (*pluginwire.QueryInvocationResponse, error) {
			exactCalls.Add(1)
			return protocolV2ExactQueryResponse(request, "exact"), nil
		},
		func(_ context.Context, request *pluginwire.QueryResultFilterRequest) (*pluginwire.QueryResultFilterResponse, error) {
			exactCalls.Add(1)
			return &pluginwire.QueryResultFilterResponse{
				Context: protocolV2QueryTestResponseContext(request.GetContext()),
				Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
				Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
			}, nil
		},
	)
	activeClient := newProtocolV2QueryTestClient(t, declaration, filter,
		func(_ context.Context, request *pluginwire.QueryInvocationRequest) (*pluginwire.QueryInvocationResponse, error) {
			activeCalls.Add(1)
			return protocolV2ExactQueryResponse(request, "active"), nil
		},
		func(_ context.Context, request *pluginwire.QueryResultFilterRequest) (*pluginwire.QueryResultFilterResponse, error) {
			activeCalls.Add(1)
			return &pluginwire.QueryResultFilterResponse{
				Context: protocolV2QueryTestResponseContext(request.GetContext()),
				Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
				Outcome: &pluginwire.QueryResultFilterResponse_Success{Success: request.GetInput()},
			}, nil
		},
	)
	exactIdentity := RuntimeInstanceIdentity{ExtensionID: "plugin.query.demo", InstanceID: "runtime-exact"}
	activeIdentity := RuntimeInstanceIdentity{ExtensionID: exactIdentity.ExtensionID, InstanceID: "runtime-active"}
	exactClient.identity.InstanceId = exactIdentity.InstanceID
	activeClient.identity.InstanceId = activeIdentity.InstanceID
	starter := &ProtocolStarter{
		protocols:              map[string]PluginProtocol{exactIdentity.ExtensionID: activeClient},
		activeRuntimeInstances: map[string]string{exactIdentity.ExtensionID: activeIdentity.InstanceID},
		runtimeInstances: map[string]map[string]*protocolRuntimeInstance{
			exactIdentity.ExtensionID: {
				exactIdentity.InstanceID: {
					identity: exactIdentity, extensionVersion: "1.0.0", artifactDigest: "digest-v1",
					protocolVersion: 2, protocol: exactClient,
				},
				activeIdentity.InstanceID: {
					identity: activeIdentity, extensionVersion: "1.0.0", artifactDigest: "digest-v1",
					protocolVersion: 2, protocol: activeClient,
				},
			},
		},
	}
	extension := extensions.Extension{ID: exactIdentity.ExtensionID, Version: "1.0.0", PackageDigest: "digest-v1"}
	plan := protocolV2QueryTestPlan(declaration)
	rows, err := starter.InvokeQueryInstance(t.Context(), exactIdentity, extension, VersionedQueryRequest{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: declaration.Handler, Plan: plan, FetchLimit: 1,
	})
	if err != nil || len(rows) != 1 || rows[0]["id"] != "exact" {
		t.Fatalf("exact query rows=%#v err=%v", rows, err)
	}
	filtered, err := starter.FilterQueryResultInstance(t.Context(), exactIdentity, extension, VersionedQueryResultFilterRequest{
		FilterID: filter.ID, FilterContractVersion: filter.ContractVersion,
		QueryID: declaration.ID, QueryContractVersion: declaration.ContractVersion,
		QueryPlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: filter.Handler, Plan: plan, Rows: rows,
	})
	if err != nil || len(filtered) != 1 || filtered[0]["id"] != "exact" ||
		exactCalls.Load() != 2 || activeCalls.Load() != 0 {
		t.Fatalf("exact filter rows=%#v err=%v exact=%d active=%d",
			filtered, err, exactCalls.Load(), activeCalls.Load())
	}
	missing := RuntimeInstanceIdentity{ExtensionID: exactIdentity.ExtensionID, InstanceID: "runtime-missing"}
	if _, err := starter.InvokeQueryInstance(t.Context(), missing, extension, VersionedQueryRequest{}); !errors.Is(err, queryregistry.ErrArtifactUnavailable) || activeCalls.Load() != 0 {
		t.Fatalf("missing exact runtime error=%v active calls=%d", err, activeCalls.Load())
	}
}

func protocolV2ExactQueryResponse(
	request *pluginwire.QueryInvocationRequest,
	id string,
) *pluginwire.QueryInvocationResponse {
	return &pluginwire.QueryInvocationResponse{
		Context: protocolV2QueryTestResponseContext(request.GetContext()),
		Binding: request.GetBinding(), ShapeDigest: request.GetPlan().GetShapeDigest(),
		Outcome: &pluginwire.QueryInvocationResponse_Success{
			Success: &pluginwire.QueryRuntimeRows{Rows: []*pluginwire.QueryRuntimeRow{
				{CanonicalJson: []byte(`{"id":"` + id + `"}`)},
			}},
		},
	}
}
