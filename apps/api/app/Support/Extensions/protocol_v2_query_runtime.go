package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Host-side query.runtime@1 行 JSON 预算与 SDK 保持一致；不得 import sdk/plugin/v2
// （该包依赖 Extensions，会形成 import cycle）。
const (
	protocolV2QueryRuntimeResultBytes = 8 << 20
	protocolV2QueryRuntimeJSONDepth   = 64
	protocolV2QueryRuntimeJSONNodes   = 1 << 20
)

// VersionedQueryRequest is the Host-normalized InvokeQuery projection.
type VersionedQueryRequest struct {
	QueryID         string
	ContractVersion string
	PlanVersion     string
	ResultSchema    string
	Handler         string
	Plan            queryregistry.QueryPlan
	FetchLimit      int
	Timeout         time.Duration
}

// VersionedQueryResultFilterRequest is the Host-normalized FilterQueryResult
// projection. Identity and permission material stay on the Host side.
type VersionedQueryResultFilterRequest struct {
	FilterID              string
	FilterContractVersion string
	QueryID               string
	QueryContractVersion  string
	QueryPlanVersion      string
	ResultSchema          string
	Handler               string
	Plan                  queryregistry.QueryPlan
	Rows                  []queryregistry.QueryRow
	Timeout               time.Duration
}

func (c *protocolV2Client) InvokeQuery(
	parent context.Context,
	input VersionedQueryRequest,
) ([]queryregistry.QueryRow, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return nil, extensions.ErrRuntimeUnavailable
	}
	query, err := c.exactManifestQuery(input.QueryID, input.ContractVersion, input.PlanVersion, input.ResultSchema, input.Handler)
	if err != nil {
		return nil, err
	}
	timeout := input.Timeout
	if timeout <= 0 || timeout > DefaultProtocolV2RequestTimeout {
		timeout = DefaultProtocolV2RequestTimeout
	}
	plan, err := protocolV2QueryRuntimePlan(input.Plan, input.FetchLimit)
	if err != nil {
		return nil, err
	}
	ctx, cancel := protocolV2Deadline(parent, timeout)
	defer cancel()
	// 查询 runtime 禁止 actor/authority，并使用 reduced context（无自由 form TraceId）。
	requestContext := c.queryRuntimeRequestContext(ctx, input.Plan.Locale)
	binding := &pluginv2.QueryRuntimeBinding{
		QueryId: query.ID, ContractVersion: query.ContractVersion,
		PlanVersion: query.PlanVersion, ResultSchema: query.ResultSchema, Handler: query.Handler,
	}
	response, err := c.client.InvokeQuery(ctx, &pluginv2.QueryInvocationRequest{
		Context: requestContext,
		Binding: binding, Plan: plan,
	})
	if err != nil {
		return nil, mapProtocolV2QueryCallError(ctx, err)
	}
	if err := validateProtocolV2QueryResponseContext(response.GetContext(), requestContext); err != nil {
		return nil, fmt.Errorf("protocol v2 query %q response context: %w", input.QueryID, err)
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return nil, err
	}
	if response.GetSuccess() == nil {
		return nil, fmt.Errorf("protocol v2 query %q returned no rows outcome", input.QueryID)
	}
	if response.GetShapeDigest() != input.Plan.ShapeDigest || !proto.Equal(response.GetBinding(), binding) {
		return nil, fmt.Errorf("protocol v2 query %q binding or shape drifted", input.QueryID)
	}
	return decodeProtocolV2QueryRows(response.GetSuccess(), input.FetchLimit)
}

func (c *protocolV2Client) FilterQueryResult(
	parent context.Context,
	input VersionedQueryResultFilterRequest,
) ([]queryregistry.QueryRow, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return nil, extensions.ErrRuntimeUnavailable
	}
	filter, err := c.exactManifestQueryResultFilter(input)
	if err != nil {
		return nil, err
	}
	timeout := input.Timeout
	if timeout <= 0 || timeout > DefaultProtocolV2RequestTimeout {
		timeout = DefaultProtocolV2RequestTimeout
	}
	filterFetchLimit := input.Plan.Pagination.Limit
	if input.Plan.Pagination.Mode != queryregistry.PaginationNone {
		filterFetchLimit++
	}
	plan, err := protocolV2QueryRuntimePlan(input.Plan, filterFetchLimit)
	if err != nil {
		return nil, err
	}
	// Filter 可能收到 provider 的短尾页；分页查询仍保留 Host 原始的
	// limit+1 合同，而不是本次实际行数。none 查询固定为 1。
	encoded, err := encodeProtocolV2QueryRows(input.Rows)
	if err != nil {
		return nil, err
	}
	ctx, cancel := protocolV2Deadline(parent, timeout)
	defer cancel()
	requestContext := c.queryRuntimeRequestContext(ctx, input.Plan.Locale)
	binding := &pluginv2.QueryResultFilterRuntimeBinding{
		FilterId: filter.ID, FilterContractVersion: filter.ContractVersion,
		QueryId: filter.QueryID, QueryContractVersion: filter.QueryContractVersion,
		QueryPlanVersion: filter.QueryPlanVersion, ResultSchema: input.ResultSchema,
		Handler: filter.Handler,
	}
	response, err := c.client.FilterQueryResult(ctx, &pluginv2.QueryResultFilterRequest{
		Context: requestContext,
		Binding: binding, Plan: plan, Input: encoded,
	})
	if err != nil {
		return nil, mapProtocolV2QueryCallError(ctx, err)
	}
	if err := validateProtocolV2QueryResponseContext(response.GetContext(), requestContext); err != nil {
		return nil, fmt.Errorf("protocol v2 query filter %q response context: %w", input.FilterID, err)
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return nil, err
	}
	if response.GetSuccess() == nil {
		return nil, fmt.Errorf("protocol v2 query filter %q returned no rows outcome", input.FilterID)
	}
	if response.GetShapeDigest() != input.Plan.ShapeDigest || !proto.Equal(response.GetBinding(), binding) {
		return nil, fmt.Errorf("protocol v2 query filter %q binding or shape drifted", input.FilterID)
	}
	return decodeProtocolV2QueryRows(response.GetSuccess(), len(input.Rows))
}

func (c *protocolV2Client) exactManifestQuery(
	queryID, contractVersion, planVersion, resultSchema, handler string,
) (extensions.ManifestQuery, error) {
	for _, query := range c.queries {
		if query.ID != queryID || query.ContractVersion != contractVersion {
			continue
		}
		if query.PlanVersion != planVersion || query.ResultSchema != resultSchema ||
			strings.TrimSpace(query.Handler) != strings.TrimSpace(handler) || query.Handler == "" {
			return extensions.ManifestQuery{}, fmt.Errorf("protocol v2 query %q contract is not frozen", queryID)
		}
		return query, nil
	}
	return extensions.ManifestQuery{}, fmt.Errorf("protocol v2 query %q is not declared", queryID)
}

func (c *protocolV2Client) exactManifestQueryResultFilter(
	input VersionedQueryResultFilterRequest,
) (extensions.ManifestQueryResultFilter, error) {
	for _, filter := range c.queryResultFilters {
		if filter.ID != input.FilterID || filter.ContractVersion != input.FilterContractVersion {
			continue
		}
		if filter.QueryID != input.QueryID || filter.QueryContractVersion != input.QueryContractVersion ||
			filter.QueryPlanVersion != input.QueryPlanVersion ||
			strings.TrimSpace(filter.Handler) != strings.TrimSpace(input.Handler) {
			return extensions.ManifestQueryResultFilter{}, fmt.Errorf(
				"protocol v2 query result filter %q contract is not frozen", input.FilterID)
		}
		return filter, nil
	}
	return extensions.ManifestQueryResultFilter{}, fmt.Errorf(
		"protocol v2 query result filter %q is not declared", input.FilterID)
}

func protocolV2QueryRuntimePlan(plan queryregistry.QueryPlan, fetchLimit int) (*pluginv2.QueryRuntimePlan, error) {
	if fetchLimit < 1 || fetchLimit > 101 {
		return nil, queryregistry.ErrExecutionInvalid
	}
	result := &pluginv2.QueryRuntimePlan{
		ShapeDigest: plan.ShapeDigest,
		Fields:      append([]string(nil), plan.Fields...),
		Relations:   append([]string(nil), plan.Relations...),
		Locale:      plan.Locale,
		Scope:       plan.Scope,
		FetchLimit:  uint32(fetchLimit),
		Pagination: &pluginv2.QueryRuntimePagination{
			Mode: plan.Pagination.Mode, Offset: uint64(plan.Pagination.Offset),
			Limit: uint32(plan.Pagination.Limit),
		},
	}
	if len(plan.Filters) > 0 {
		result.Filters = make([]*pluginv2.QueryRuntimeFilter, 0, len(plan.Filters))
		for _, item := range plan.Filters {
			result.Filters = append(result.Filters, &pluginv2.QueryRuntimeFilter{Field: item.Field, Value: item.Value})
		}
	}
	if len(plan.Sorts) > 0 {
		result.Sorts = make([]*pluginv2.QueryRuntimeSort, 0, len(plan.Sorts))
		for _, item := range plan.Sorts {
			result.Sorts = append(result.Sorts, &pluginv2.QueryRuntimeSort{Field: item.Field, Descending: item.Descending})
		}
	}
	return result, nil
}

func encodeProtocolV2QueryRows(rows []queryregistry.QueryRow) (*pluginv2.QueryRuntimeRows, error) {
	result := &pluginv2.QueryRuntimeRows{Rows: make([]*pluginv2.QueryRuntimeRow, 0, len(rows))}
	for _, row := range rows {
		body, err := json.Marshal(map[string]any(row))
		if err != nil {
			return nil, err
		}
		canonical, err := canonicalProtocolV2QueryRuntimeRow(body)
		if err != nil {
			return nil, err
		}
		result.Rows = append(result.Rows, &pluginv2.QueryRuntimeRow{CanonicalJson: canonical})
	}
	return result, nil
}

func decodeProtocolV2QueryRows(rows *pluginv2.QueryRuntimeRows, maximum int) ([]queryregistry.QueryRow, error) {
	if rows == nil {
		return nil, queryregistry.ErrProviderFailed
	}
	if len(rows.GetRows()) > maximum {
		return nil, queryregistry.ErrResultTooLarge
	}
	result := make([]queryregistry.QueryRow, 0, len(rows.GetRows()))
	for _, row := range rows.GetRows() {
		decoded, err := decodeProtocolV2QueryRuntimeRow(row.GetCanonicalJson())
		if err != nil {
			return nil, errors.Join(queryregistry.ErrResultInvalid, err)
		}
		result = append(result, queryregistry.QueryRow(decoded))
	}
	return result, nil
}

// canonicalProtocolV2QueryRuntimeRow 与 SDK CanonicalQueryRuntimeRow 同语义：
// 对象、去重 key、紧凑 JSON、保留 json.Number 词素。
func canonicalProtocolV2QueryRuntimeRow(raw []byte) (json.RawMessage, error) {
	value, _, err := decodeProtocolV2QueryRuntimeJSONObject(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(canonical) > protocolV2QueryRuntimeResultBytes {
		return nil, queryregistry.ErrResultTooLarge
	}
	return json.RawMessage(canonical), nil
}

func decodeProtocolV2QueryRuntimeRow(raw []byte) (map[string]any, error) {
	canonical, err := canonicalProtocolV2QueryRuntimeRow(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeProtocolV2QueryRuntimeJSONObject(raw []byte) (map[string]any, int, error) {
	if len(raw) == 0 || len(raw) > protocolV2QueryRuntimeResultBytes {
		return nil, 0, queryregistry.ErrResultTooLarge
	}
	if !utf8.Valid(raw) {
		return nil, 0, queryregistry.ErrResultInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	value, err := readProtocolV2QueryRuntimeJSONValue(decoder, 0, &nodes)
	if err != nil {
		return nil, 0, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, 0, fmt.Errorf("%w: row must be an object", queryregistry.ErrResultInvalid)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, 0, fmt.Errorf("%w: trailing JSON data", queryregistry.ErrResultInvalid)
	}
	return object, nodes, nil
}

func readProtocolV2QueryRuntimeJSONValue(decoder *json.Decoder, depth int, nodes *int) (any, error) {
	if depth > protocolV2QueryRuntimeJSONDepth || *nodes >= protocolV2QueryRuntimeJSONNodes {
		return nil, queryregistry.ErrResultTooLarge
	}
	*nodes = *nodes + 1
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", queryregistry.ErrResultInvalid, err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("%w: %v", queryregistry.ErrResultInvalid, err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, queryregistry.ErrResultInvalid
				}
				if _, duplicate := object[key]; duplicate {
					return nil, fmt.Errorf("%w: duplicate object key %q", queryregistry.ErrResultInvalid, key)
				}
				child, err := readProtocolV2QueryRuntimeJSONValue(decoder, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("%w: %v", queryregistry.ErrResultInvalid, err)
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := readProtocolV2QueryRuntimeJSONValue(decoder, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("%w: %v", queryregistry.ErrResultInvalid, err)
			}
			return array, nil
		default:
			return nil, queryregistry.ErrResultInvalid
		}
	case bool, string, json.Number, nil:
		return value, nil
	default:
		return nil, queryregistry.ErrResultInvalid
	}
}

// queryRuntimeRequestContext builds the reduced Host→plugin projection required
// by query.runtime@1: no actor/authority/idempotency/delegations, no free-form
// TraceId, and locale exactly matching the Host plan.
func (c *protocolV2Client) queryRuntimeRequestContext(ctx context.Context, locale string) *protocolv2.RequestContext {
	request := c.requestContext(ctx, "query.runtime")
	request.GrantedAuthority = nil
	request.Actor = nil
	request.IdempotencyKey = ""
	request.HostCommandDelegations = nil
	request.HostQueryDelegations = nil
	// SDK 要求 TraceId 要么为空要么 32 位 hex；通用 requestContext 的 correlation 不满足。
	request.Trace = nil
	request.Locale = locale
	return request
}

func validateProtocolV2QueryResponseContext(
	response *protocolv2.ResponseContext,
	request *protocolv2.RequestContext,
) error {
	if response == nil || request == nil || response.GetRequestId() != request.GetRequestId() ||
		!proto.Equal(response.GetTrace(), request.GetTrace()) ||
		!proto.Equal(response.GetExtension(), request.GetExtension()) {
		return errors.New("response context does not match the exact runtime request")
	}
	if response.GetServerTime() == nil || !response.GetServerTime().IsValid() {
		return errors.New("response server time is invalid")
	}
	return nil
}

func mapProtocolV2QueryCallError(ctx context.Context, err error) error {
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	case codes.Canceled:
		return context.Canceled
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
