package hostapi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestProtocolV2QueryRegistryBridgeReusesTypedPlanAndExecutor(t *testing.T) {
	var executed protocolV2QueryPlan
	executor := protocolV2QueryExecutorFunc(func(_ context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
		executed = plan
		return []map[string]any{
			{"id": "3", "title": "three"},
			{"id": "2", "title": "two"},
			{"id": "1", "title": "one"},
		}, nil
	})
	runtime, err := newProtocolV2QueryRuntime(executor, allowedQueryAuthority(), stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	registry, artifact, declaration := queryRegistryBridgeFixture(t)
	providers, err := NewProtocolV2QueryRegistryProviderResolver(runtime, []QueryRegistryProtocolV2Binding{{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema, Artifact: artifact,
		HostQueryID: QueryPublicTopicsList, HostPlanVersion: QueryStableCorePlanVersion,
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers,
		Schemas: queryregistry.ResultSchemaValidatorFunc(func(context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: declaration.ID, Fields: []string{"id", "title"},
		Filters:    []queryregistry.FilterValue{{Field: "category_id", Value: "1"}},
		Sorts:      []queryregistry.SortValue{{Field: "last_activity_at", Descending: true}},
		Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	if err != nil || len(result.Rows) != 2 || !result.Page.HasMore || result.Page.NextOffset != 2 ||
		len(result.ProviderDigest) != 64 {
		t.Fatalf("bridge result=%#v err=%v", result, err)
	}
	if executed.Definition.ID != QueryPublicTopicsList || executed.Offset != 0 || executed.Limit != 2 ||
		executed.FetchLimit != 3 || len(executed.Fields) != 2 || len(executed.Filters) != 1 ||
		executed.Filters[0].Value != int64(1) || len(executed.Sorts) != 2 ||
		executed.Sorts[0].Field != "last_activity_at" || executed.Sorts[1].Field != "id" {
		t.Fatalf("bridged Protocol V2 plan=%#v", executed)
	}
}

func TestProtocolV2QueryRegistryBridgeRejectsUnmappedRelationsAndInvalidBindings(t *testing.T) {
	var executorCalls int
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		executorCalls++
		return nil, nil
	})
	runtime, err := newProtocolV2QueryRuntime(executor, allowedQueryAuthority(), stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	registry, artifact, declaration := queryRegistryBridgeFixture(t)
	binding := QueryRegistryProtocolV2Binding{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema, Artifact: artifact,
		HostQueryID: QueryPublicTopicsList, HostPlanVersion: QueryStableCorePlanVersion,
	}
	for name, mutate := range map[string]func(*QueryRegistryProtocolV2Binding){
		"unknown query": func(value *QueryRegistryProtocolV2Binding) { value.HostQueryID = "missing" },
		"schema drift":  func(value *QueryRegistryProtocolV2Binding) { value.ResultSchema = "other.result@1" },
		"noncanonical core artifact": func(value *QueryRegistryProtocolV2Binding) {
			value.Artifact.PackageDigest = "sha256:" + value.Artifact.PackageDigest
		},
		"unsealed plugin": func(value *QueryRegistryProtocolV2Binding) {
			value.Artifact = queryregistry.Artifact{
				ExtensionID: "plugin.bridge", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("f", 64),
				VersionID: 1, RuntimeInstanceID: "plugin-runtime",
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := binding
			mutate(&candidate)
			if _, err := NewProtocolV2QueryRegistryProviderResolver(runtime, []QueryRegistryProtocolV2Binding{candidate}); err == nil {
				t.Fatal("invalid bridge binding accepted")
			}
		})
	}

	providers, err := NewProtocolV2QueryRegistryProviderResolver(runtime, []QueryRegistryProtocolV2Binding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if exportedMethods := reflect.TypeOf(providers).NumMethod(); exportedMethods != 0 {
		t.Fatalf("provider resolver exposes %d raw lookup methods", exportedMethods)
	}
	query, err := registry.Resolve(declaration.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := registry.Plan(t.Context(), queryregistry.PlanRequest{
		QueryID: query.ID, Relations: []string{"owner"}, Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := runtime.queryEngine().definitions[protocolV2QueryKey{id: binding.HostQueryID, version: binding.HostPlanVersion}]
	provider := &protocolV2QueryRegistryProvider{
		executor: runtime.queryEngine().executor, definition: cloneProtocolV2QueryDefinition(definition), binding: binding,
	}
	if _, err := provider.ExecuteQuery(t.Context(), queryregistry.ProviderExecutionRequest{
		Plan: plan, FetchLimit: 3,
	}); !errors.Is(err, queryregistry.ErrContractInsufficient) {
		t.Fatalf("Host-only relation boundary=%v", err)
	}
	plan.Relations = nil
	otherArtifact, err := queryregistry.NewCoreArtifact("core.other", "1.0.0", strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*queryregistry.QueryPlan){
		"query identity drift": func(value *queryregistry.QueryPlan) {
			value.Query.ID = "core.other.items"
		},
		"artifact drift": func(value *queryregistry.QueryPlan) {
			value.Query.Artifact = otherArtifact
		},
		"unknown field": func(value *queryregistry.QueryPlan) {
			value.Fields = []string{"not_allowlisted"}
		},
		"unknown filter": func(value *queryregistry.QueryPlan) {
			value.Filters = []queryregistry.FilterValue{{Field: "not_allowlisted", Value: "1"}}
		},
		"noncanonical filter": func(value *queryregistry.QueryPlan) {
			value.Filters = []queryregistry.FilterValue{{Field: "category_id", Value: "1 OR 1=1"}}
		},
		"unknown sort": func(value *queryregistry.QueryPlan) {
			value.Sorts = []queryregistry.SortValue{{Field: "not_allowlisted"}}
		},
		"too many sorts": func(value *queryregistry.QueryPlan) {
			value.Sorts = []queryregistry.SortValue{
				{Field: "last_activity_at"}, {Field: "created_at"}, {Field: "updated_at"},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := plan
			mutate(&candidate)
			if _, err := provider.ExecuteQuery(t.Context(), queryregistry.ProviderExecutionRequest{
				Plan: candidate, FetchLimit: 3,
			}); !errors.Is(err, queryregistry.ErrProviderUnavailable) && !errors.Is(err, queryregistry.ErrInvalid) {
				t.Fatalf("drifted plan error=%v", err)
			}
		})
	}
	if executorCalls != 0 {
		t.Fatalf("invalid bridge reached SQL executor %d times", executorCalls)
	}
}

func TestQueryRegistryTraceAdapterUsesExistingBoundedHostTraceSink(t *testing.T) {
	sink := &recordingQueryTraceSink{}
	adapter := NewQueryRegistryTraceAdapter(sink)
	adapter.AppendExecutionTrace(queryregistry.ExecutionTrace{
		ExtensionID: "core.bridge", ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("a", 64),
		QueryID: "core.bridge.topics", PlanVersion: "core.bridge.topics.plan@1",
		ShapeDigest: strings.Repeat("b", 64), Rows: 2, Outcome: queryregistry.TraceOutcomeAllowed,
	})
	trace := sink.single(t)
	if trace.ExtensionID != "core.bridge" || trace.QueryID != "core.bridge.topics" ||
		trace.Rows != 2 || trace.Outcome != QueryTraceAllowed || trace.ShapeDigest != strings.Repeat("b", 64) {
		t.Fatalf("adapted trace=%#v", trace)
	}
}

func queryRegistryBridgeFixture(t *testing.T) (*queryregistry.Registry, queryregistry.Artifact, queryregistry.QueryDeclaration) {
	t.Helper()
	artifact, err := queryregistry.NewCoreArtifact("core.bridge", "1.0.0", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	declaration := queryregistry.QueryDeclaration{
		ID: "core.bridge.topics", ContractVersion: "core.bridge.topics@1", Entity: "core.bridge.topic",
		PlanVersion: "core.bridge.topics.plan@1", Fields: []string{"id", "title"}, Relations: []string{"owner"},
		Filters: []string{"category_id"}, Sort: []string{"last_activity_at", "id"},
		Pagination: queryregistry.PaginationOffset, ResultSchema: QueryPublicTopicResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
		PermissionPolicy: queryregistry.PermissionPolicyPublic,
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(
		func(input queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
			return queryregistry.QueryCost{Units: len(input.Fields) + len(input.Filters) + input.Pagination.Limit, Maximum: 1000}, nil
		},
	)))
	if _, err := registry.Publish(queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	return registry, artifact, declaration
}
