package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const queryRegistryProtocolV2MaximumSorts = 2

// QueryRegistryProtocolV2Binding maps one Query Registry core declaration to
// one existing HostAPI v2 definition. The two identities are explicit because
// the frozen catalogs use different namespaces and must never be matched by a
// naming convention.
type QueryRegistryProtocolV2Binding struct {
	QueryID         string
	ContractVersion string
	PlanVersion     string
	ResultSchema    string
	Artifact        queryregistry.Artifact
	HostQueryID     string
	HostPlanVersion string
}

type protocolV2QueryRegistryProvider struct {
	executor   protocolV2QueryExecutor
	definition protocolV2QueryDefinition
	binding    QueryRegistryProtocolV2Binding
}

// NewProtocolV2QueryRegistryProviderResolver reuses the production HostAPI v2
// allowlisted definitions and executor. Authority remains owned by
// QueryRegistry permission/release checks; this adapter therefore accepts only
// sealed Core plans and never acts as a plugin-to-plugin transport.
func NewProtocolV2QueryRegistryProviderResolver(
	runtime ProtocolV2QueryRuntime,
	bindings []QueryRegistryProtocolV2Binding,
) (queryregistry.ExecutableProviderResolver, error) {
	if runtime == nil || runtime.queryEngine() == nil || runtime.queryEngine().executor == nil {
		return nil, errors.New("hostapi: Query Registry requires a configured Protocol V2 query runtime")
	}
	engine := runtime.queryEngine()
	result := make([]queryregistry.ExecutableProviderBinding, 0, len(bindings))
	for _, raw := range bindings {
		binding := raw
		binding.QueryID = strings.ToLower(strings.TrimSpace(binding.QueryID))
		binding.ContractVersion = strings.TrimSpace(binding.ContractVersion)
		binding.PlanVersion = strings.TrimSpace(binding.PlanVersion)
		binding.ResultSchema = strings.TrimSpace(binding.ResultSchema)
		binding.HostQueryID = strings.TrimSpace(binding.HostQueryID)
		binding.HostPlanVersion = strings.TrimSpace(binding.HostPlanVersion)
		canonicalArtifact, artifactErr := queryregistry.NewCoreArtifact(
			binding.Artifact.ExtensionID, binding.Artifact.ExtensionVersion, binding.Artifact.PackageDigest,
		)
		definition, ok := engine.definitions[protocolV2QueryKey{id: binding.HostQueryID, version: binding.HostPlanVersion}]
		if !ok || binding.QueryID == "" || binding.ContractVersion == "" || binding.PlanVersion == "" ||
			binding.ResultSchema == "" || artifactErr != nil || binding.Artifact != canonicalArtifact ||
			binding.ResultSchema != definition.ResultSchemaID+"@"+definition.ResultSchemaVersion {
			return nil, fmt.Errorf("hostapi: invalid Query Registry Protocol V2 binding for %q", binding.QueryID)
		}
		provider := &protocolV2QueryRegistryProvider{
			executor: engine.executor, definition: cloneProtocolV2QueryDefinition(definition), binding: binding,
		}
		result = append(result, queryregistry.ExecutableProviderBinding{
			QueryID: binding.QueryID, ContractVersion: binding.ContractVersion,
			PlanVersion: binding.PlanVersion, ResultSchema: binding.ResultSchema,
			Artifact: binding.Artifact, ProviderDigest: queryRegistryProtocolV2ProviderDigest(binding, definition),
			FailurePolicy: queryregistry.ProviderFailureFailClosed,
			Provider:      provider,
		})
	}
	return queryregistry.NewStaticProviderResolver(result)
}

func queryRegistryProtocolV2ProviderDigest(
	binding QueryRegistryProtocolV2Binding,
	definition protocolV2QueryDefinition,
) string {
	document := struct {
		SchemaVersion   string                    `json:"schemaVersion"`
		QueryID         string                    `json:"queryId"`
		ContractVersion string                    `json:"contractVersion"`
		PlanVersion     string                    `json:"planVersion"`
		ResultSchema    string                    `json:"resultSchema"`
		HostQueryID     string                    `json:"hostQueryId"`
		HostPlanVersion string                    `json:"hostPlanVersion"`
		Definition      protocolV2QueryDefinition `json:"definition"`
	}{
		SchemaVersion: "sforum.query-registry.protocol-v2-provider@1",
		QueryID:       binding.QueryID, ContractVersion: binding.ContractVersion,
		PlanVersion: binding.PlanVersion, ResultSchema: binding.ResultSchema,
		HostQueryID: binding.HostQueryID, HostPlanVersion: binding.HostPlanVersion,
		Definition: cloneProtocolV2QueryDefinition(definition),
	}
	body, _ := json.Marshal(document)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (p *protocolV2QueryRegistryProvider) ExecuteQuery(
	ctx context.Context,
	request queryregistry.ProviderExecutionRequest,
) (queryregistry.ProviderExecutionResult, error) {
	if p == nil || p.executor == nil || !request.Plan.Query.Artifact.Core ||
		request.Plan.Query.ID != p.binding.QueryID ||
		request.Plan.Query.ContractVersion != p.binding.ContractVersion ||
		request.Plan.Query.PlanVersion != p.binding.PlanVersion ||
		request.Plan.Query.ResultSchema != p.binding.ResultSchema ||
		request.Plan.Query.Artifact != p.binding.Artifact ||
		request.FetchLimit < 1 || request.FetchLimit > protocolV2QueryMaximumLimit+1 {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrProviderUnavailable
	}
	if len(request.Plan.Relations) > 0 {
		// The V3 task book leaves Host-owned joins/relations open. Do not translate
		// relation names into SQL or implicit secondary queries here.
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrContractInsufficient
	}
	if p.definition.Single != (request.Plan.Pagination.Mode == queryregistry.PaginationNone) {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrContractInsufficient
	}
	fields, err := queryRegistryProtocolV2Fields(p.definition, request.Plan.Fields)
	if err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	filters, err := queryRegistryProtocolV2Filters(p.definition, request.Plan.Filters)
	if err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	sorts, err := queryRegistryProtocolV2Sorts(p.definition, request.Plan.Sorts)
	if err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	if p.definition.Single && request.FetchLimit != 1 {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrInvalid
	}
	plan := protocolV2QueryPlan{
		Definition: cloneProtocolV2QueryDefinition(p.definition), Fields: fields, Filters: filters, Sorts: sorts,
		Offset: request.Plan.Pagination.Offset, Limit: request.Plan.Pagination.Limit,
		FetchLimit: request.FetchLimit, ShapeDigest: request.Plan.ShapeDigest,
	}
	rows, err := p.executor.ExecuteProtocolV2Query(ctx, plan)
	if err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	result := queryregistry.ProviderExecutionResult{Rows: make([]queryregistry.QueryRow, 0, len(rows))}
	for _, row := range rows {
		item := make(queryregistry.QueryRow, len(row))
		for key, value := range row {
			item[key] = value
		}
		result.Rows = append(result.Rows, item)
	}
	return result, nil
}

func queryRegistryProtocolV2Fields(
	definition protocolV2QueryDefinition,
	requested []string,
) ([]protocolV2QueryField, error) {
	byName := make(map[string]protocolV2QueryField, len(definition.Fields))
	for _, field := range definition.Fields {
		byName[field.Name] = field
	}
	result := make([]protocolV2QueryField, 0, len(requested))
	for _, name := range requested {
		field, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: HostAPI field %q is unavailable", queryregistry.ErrProviderUnavailable, name)
		}
		result = append(result, field)
	}
	return result, nil
}

func queryRegistryProtocolV2Filters(
	definition protocolV2QueryDefinition,
	requested []queryregistry.FilterValue,
) ([]protocolV2QueryFilter, error) {
	byName := make(map[string]protocolV2QueryFilterDefinition, len(definition.Filters))
	for _, filter := range definition.Filters {
		if filter.Operator != "eq" {
			continue
		}
		if _, duplicate := byName[filter.Field]; duplicate {
			return nil, fmt.Errorf("%w: ambiguous HostAPI filter %q", queryregistry.ErrProviderUnavailable, filter.Field)
		}
		byName[filter.Field] = filter
	}
	result := make([]protocolV2QueryFilter, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, item := range requested {
		definition, ok := byName[item.Field]
		if !ok {
			return nil, fmt.Errorf("%w: HostAPI filter %q is unavailable", queryregistry.ErrProviderUnavailable, item.Field)
		}
		value, err := queryRegistryProtocolV2FilterValue(definition, item.Value)
		if err != nil {
			return nil, err
		}
		seen[item.Field] = struct{}{}
		result = append(result, protocolV2QueryFilter{Definition: definition, Value: value})
	}
	for _, required := range definition.RequiredFilters {
		if _, ok := seen[required]; !ok {
			return nil, fmt.Errorf("%w: HostAPI filter %q is required", queryregistry.ErrInvalid, required)
		}
	}
	return result, nil
}

func queryRegistryProtocolV2FilterValue(definition protocolV2QueryFilterDefinition, value string) (any, error) {
	switch definition.Kind {
	case "int64":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
			return nil, fmt.Errorf("%w: filter %q requires a canonical positive int64", queryregistry.ErrInvalid, definition.Field)
		}
		return parsed, nil
	case protocolV2QueryInt64ListFilterKind:
		return parseShortcodeProjectionIDs(value)
	case "text":
		if value == "" || strings.TrimSpace(value) != value || len(value) > 200 {
			return nil, fmt.Errorf("%w: filter %q requires bounded text", queryregistry.ErrInvalid, definition.Field)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("%w: filter %q has no supported HostAPI type", queryregistry.ErrProviderUnavailable, definition.Field)
	}
}

func queryRegistryProtocolV2Sorts(
	definition protocolV2QueryDefinition,
	requested []queryregistry.SortValue,
) ([]protocolV2QuerySort, error) {
	if len(requested) == 0 {
		return append([]protocolV2QuerySort(nil), definition.DefaultSorts...), nil
	}
	// Preserve the established Protocol V2 planner bound for the shared SQL
	// engine. Other Query Registry providers may expose their own reviewed limit.
	if len(requested) > queryRegistryProtocolV2MaximumSorts {
		return nil, fmt.Errorf("%w: HostAPI accepts at most %d sorts",
			queryregistry.ErrInvalid, queryRegistryProtocolV2MaximumSorts)
	}
	byName := make(map[string]protocolV2QuerySortDefinition, len(definition.Sorts))
	for _, item := range definition.Sorts {
		byName[item.Field] = item
	}
	result := make([]protocolV2QuerySort, 0, len(requested)+1)
	seen := make(map[string]struct{}, len(requested))
	for _, item := range requested {
		definition, ok := byName[item.Field]
		if !ok {
			return nil, fmt.Errorf("%w: HostAPI sort %q is unavailable", queryregistry.ErrProviderUnavailable, item.Field)
		}
		seen[item.Field] = struct{}{}
		result = append(result, protocolV2QuerySort{
			Field: definition.Field, Expression: definition.Expression, Descending: item.Descending,
		})
	}
	if id, ok := byName["id"]; ok {
		if _, exists := seen["id"]; !exists {
			result = append(result, protocolV2QuerySort{Field: id.Field, Expression: id.Expression, Descending: true})
		}
	}
	return result, nil
}
