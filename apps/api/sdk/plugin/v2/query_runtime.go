package pluginv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const (
	QueryRuntimeFeatureName     = "query.runtime"
	QueryRuntimeFeatureVersion  = "1"
	QueryRuntimeFeatureContract = QueryRuntimeFeatureName + "@" + QueryRuntimeFeatureVersion

	maximumQueryRuntimeFields       = 128
	maximumQueryRuntimeRelations    = 32
	maximumQueryRuntimeFilters      = 64
	maximumQueryRuntimeSorts        = 16
	maximumQueryRuntimeFilterBytes  = 512
	maximumQueryRuntimeNameBytes    = 256
	maximumQueryRuntimeSchemaBytes  = 256
	maximumQueryRuntimeScopeBytes   = 128
	maximumQueryRuntimeOffset       = 1_000_000
	maximumQueryRuntimePageLimit    = 100
	maximumQueryRuntimeFetchLimit   = maximumQueryRuntimePageLimit + 1
	maximumQueryRuntimeResultBytes  = 8 << 20
	maximumQueryRuntimeJSONDepth    = 64
	maximumQueryRuntimeJSONNodes    = 1 << 20
	maximumQueryRuntimeTraceBytes   = 512
	maximumQueryRuntimeRequestBytes = 256
)

var (
	queryRuntimeIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
	queryRuntimeDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	queryRuntimeLocalePattern = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8}){0,3}$`)
	queryRuntimeTracePattern  = regexp.MustCompile(`^[0-9a-f]+$`)

	errInvalidQueryRuntimeJSON  = errors.New("pluginv2: invalid query runtime JSON row")
	errQueryRuntimeRowsTooLarge = errors.New("pluginv2: query runtime rows exceed bounds")
)

// QueryRuntimeProtocolFeature returns the independently negotiable
// query.runtime@1 feature declaration.
func QueryRuntimeProtocolFeature() *protocolwire.ProtocolFeature {
	return &protocolwire.ProtocolFeature{Name: QueryRuntimeFeatureName, Version: QueryRuntimeFeatureVersion}
}

// QueryRuntimeHandlers are optional author-side handlers. The Server freezes
// this pair at its first successful handshake.
type QueryRuntimeHandlers struct {
	InvokeQuery       QueryRuntimeHandler
	FilterQueryResult QueryResultFilterRuntimeHandler
}

type QueryRuntimeHandler func(context.Context, *QueryRuntimeCall) ([]json.RawMessage, error)

type QueryResultFilterRuntimeHandler func(context.Context, *QueryResultFilterRuntimeCall) ([]json.RawMessage, error)

// QueryRuntimeCall contains only the Host-normalized query projection.
type QueryRuntimeCall struct {
	Context *protocolwire.RequestContext
	Binding *pluginwire.QueryRuntimeBinding
	Plan    *pluginwire.QueryRuntimePlan
}

// QueryResultFilterRuntimeCall carries canonical input rows for one exact
// result-filter binding.
type QueryResultFilterRuntimeCall struct {
	Context *protocolwire.RequestContext
	Binding *pluginwire.QueryResultFilterRuntimeBinding
	Plan    *pluginwire.QueryRuntimePlan
	Rows    []json.RawMessage
}

func (s *Server) InvokeQuery(
	ctx context.Context,
	request *pluginwire.QueryInvocationRequest,
) (*pluginwire.QueryInvocationResponse, error) {
	s.mu.RLock()
	handler := s.queryHandlers.InvokeQuery
	negotiated := hasExactQueryRuntimeFeature(s.selectedFeatures)
	s.mu.RUnlock()
	if handler == nil || !negotiated {
		return s.UnimplementedPluginRuntimeServiceServer.InvokeQuery(ctx, request)
	}
	response := queryInvocationResponse(request, s.nowTime())
	if detail := validateQueryInvocationRequest(request); detail != nil {
		response.Outcome = &pluginwire.QueryInvocationResponse_Error{Error: detail}
		return response, nil
	}
	if detail := s.validateRuntimeContext(request.GetContext()); detail != nil {
		response.Outcome = &pluginwire.QueryInvocationResponse_Error{Error: detail}
		return response, nil
	}

	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	rows, err := handler(handlerCtx, &QueryRuntimeCall{
		Context: cloneRequestContext(request.GetContext()),
		Binding: cloneQueryRuntimeBinding(request.GetBinding()),
		Plan:    cloneQueryRuntimePlan(request.GetPlan()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Outcome = &pluginwire.QueryInvocationResponse_Error{Error: familyErrorDetail(
			err, "query.handler_failed", "Plugin query handler failed.",
		)}
		return response, nil
	}
	success, detail := encodeQueryRuntimeRows(rows, request.GetPlan().GetFetchLimit(), "query")
	if detail != nil {
		response.Outcome = &pluginwire.QueryInvocationResponse_Error{Error: detail}
		return response, nil
	}
	response.Outcome = &pluginwire.QueryInvocationResponse_Success{Success: success}
	return response, nil
}

func (s *Server) FilterQueryResult(
	ctx context.Context,
	request *pluginwire.QueryResultFilterRequest,
) (*pluginwire.QueryResultFilterResponse, error) {
	s.mu.RLock()
	handler := s.queryHandlers.FilterQueryResult
	negotiated := hasExactQueryRuntimeFeature(s.selectedFeatures)
	s.mu.RUnlock()
	if handler == nil || !negotiated {
		return s.UnimplementedPluginRuntimeServiceServer.FilterQueryResult(ctx, request)
	}
	response := queryResultFilterResponse(request, s.nowTime())
	if detail := validateQueryResultFilterRequest(request); detail != nil {
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: detail}
		return response, nil
	}
	if detail := s.validateRuntimeContext(request.GetContext()); detail != nil {
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: detail}
		return response, nil
	}
	input, inputRows, detail := normalizeQueryRuntimeRows(
		request.GetInput(), request.GetPlan().GetFetchLimit(), "query_filter.input_invalid",
	)
	if detail != nil {
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: detail}
		return response, nil
	}

	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	rows, err := handler(handlerCtx, &QueryResultFilterRuntimeCall{
		Context: cloneRequestContext(request.GetContext()),
		Binding: cloneQueryResultFilterRuntimeBinding(request.GetBinding()),
		Plan:    cloneQueryRuntimePlan(request.GetPlan()),
		Rows:    inputRows,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: familyErrorDetail(
			err, "query_filter.handler_failed", "Plugin query result filter failed.",
		)}
		return response, nil
	}
	if len(rows) != len(input.GetRows()) {
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: queryRuntimeError(
			protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"query_filter.cardinality_mismatch",
			"Plugin query result filters must preserve row cardinality.",
		)}
		return response, nil
	}
	success, detail := encodeQueryRuntimeRows(rows, request.GetPlan().GetFetchLimit(), "query_filter")
	if detail != nil {
		response.Outcome = &pluginwire.QueryResultFilterResponse_Error{Error: detail}
		return response, nil
	}
	response.Outcome = &pluginwire.QueryResultFilterResponse_Success{Success: success}
	return response, nil
}

func queryInvocationResponse(request *pluginwire.QueryInvocationRequest, now time.Time) *pluginwire.QueryInvocationResponse {
	response := &pluginwire.QueryInvocationResponse{}
	if request == nil {
		return response
	}
	response.Context = responseContext(request.GetContext(), now)
	response.Binding = cloneQueryRuntimeBinding(request.GetBinding())
	response.ShapeDigest = request.GetPlan().GetShapeDigest()
	return response
}

func queryResultFilterResponse(request *pluginwire.QueryResultFilterRequest, now time.Time) *pluginwire.QueryResultFilterResponse {
	response := &pluginwire.QueryResultFilterResponse{}
	if request == nil {
		return response
	}
	response.Context = responseContext(request.GetContext(), now)
	response.Binding = cloneQueryResultFilterRuntimeBinding(request.GetBinding())
	response.ShapeDigest = request.GetPlan().GetShapeDigest()
	return response
}

func validateQueryInvocationRequest(request *pluginwire.QueryInvocationRequest) *protocolwire.ErrorDetail {
	if request == nil {
		return queryRuntimeInvalid("query.request_required", "A query invocation request is required.")
	}
	if detail := validateQueryRuntimeContextProjection(request.GetContext(), request.GetPlan()); detail != nil {
		return detail
	}
	if !validQueryRuntimeBinding(request.GetBinding(), request.GetContext().GetExtension().GetExtensionId()) {
		return queryRuntimeInvalid("query.binding_invalid", "The exact query binding is invalid.")
	}
	return validateQueryRuntimePlan(request.GetPlan())
}

func validateQueryResultFilterRequest(request *pluginwire.QueryResultFilterRequest) *protocolwire.ErrorDetail {
	if request == nil {
		return queryRuntimeInvalid("query_filter.request_required", "A query result-filter request is required.")
	}
	if detail := validateQueryRuntimeContextProjection(request.GetContext(), request.GetPlan()); detail != nil {
		return detail
	}
	if !validQueryResultFilterRuntimeBinding(request.GetBinding(), request.GetContext().GetExtension().GetExtensionId()) {
		return queryRuntimeInvalid("query_filter.binding_invalid", "The exact query result-filter binding is invalid.")
	}
	if request.GetInput() == nil {
		return queryRuntimeInvalid("query_filter.input_required", "Query result-filter input rows are required.")
	}
	return validateQueryRuntimePlan(request.GetPlan())
}

func validateQueryRuntimeContextProjection(
	request *protocolwire.RequestContext,
	plan *pluginwire.QueryRuntimePlan,
) *protocolwire.ErrorDetail {
	if detail := validateFamilyRequestContext(request, "query_runtime"); detail != nil {
		return detail
	}
	if request.GetActor() != nil || len(request.GetGrantedAuthority()) != 0 || request.GetIdempotencyKey() != "" ||
		len(request.GetHostCommandDelegations()) != 0 || len(request.GetHostQueryDelegations()) != 0 ||
		request.GetHostSessionDelegation() != nil {
		return queryRuntimeError(protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"query_runtime.context_authority_forbidden",
			"Actor, authority, idempotency, and delegation material are forbidden on query runtime calls.")
	}
	if !validOpaqueQueryRuntime(request.GetRequestId(), maximumQueryRuntimeRequestBytes) ||
		!validQueryRuntimeLocale(request.GetLocale()) || !validQueryRuntimeTrace(request.GetTrace()) {
		return queryRuntimeInvalid("query_runtime.context_invalid", "The reduced query runtime context is invalid.")
	}
	if plan == nil || request.GetLocale() != plan.GetLocale() {
		return queryRuntimeInvalid("query_runtime.locale_mismatch", "Context and plan locale must match exactly.")
	}
	return nil
}

func validQueryRuntimeBinding(binding *pluginwire.QueryRuntimeBinding, extensionID string) bool {
	if binding == nil || !queryRuntimeIDPattern.MatchString(binding.GetQueryId()) ||
		!strings.HasPrefix(binding.GetQueryId(), extensionID+".") ||
		!validExactContract(binding.GetContractVersion(), binding.GetQueryId()) ||
		!validContractVersion(binding.GetPlanVersion()) || !validQueryRuntimeSchemaReference(binding.GetResultSchema()) ||
		!validQueryRuntimeHandler(binding.GetHandler(), extensionID) {
		return false
	}
	return true
}

func validQueryResultFilterRuntimeBinding(
	binding *pluginwire.QueryResultFilterRuntimeBinding,
	extensionID string,
) bool {
	if binding == nil || !queryRuntimeIDPattern.MatchString(binding.GetFilterId()) ||
		!strings.HasPrefix(binding.GetFilterId(), extensionID+".") ||
		!validExactContract(binding.GetFilterContractVersion(), binding.GetFilterId()) ||
		!queryRuntimeIDPattern.MatchString(binding.GetQueryId()) ||
		!validExactContract(binding.GetQueryContractVersion(), binding.GetQueryId()) ||
		!validContractVersion(binding.GetQueryPlanVersion()) || !validQueryRuntimeSchemaReference(binding.GetResultSchema()) ||
		!validQueryRuntimeHandler(binding.GetHandler(), extensionID) {
		return false
	}
	return true
}

func validExactContract(contract, id string) bool {
	return validContractVersion(contract) && strings.HasPrefix(contract, id+"@")
}

func validQueryRuntimeHandler(handler, extensionID string) bool {
	return len(handler) <= maximumQueryRuntimeNameBytes && handler == strings.TrimSpace(handler) &&
		validManifestHandler(handler) && strings.HasPrefix(handler, extensionID+".")
}

func hasExactQueryRuntimeFeature(features []*protocolwire.ProtocolFeature) bool {
	for _, feature := range features {
		if feature.GetName() == QueryRuntimeFeatureName && feature.GetVersion() == QueryRuntimeFeatureVersion {
			return true
		}
	}
	return false
}

func validQueryRuntimeSchemaReference(value string) bool {
	if value == "" || len(value) > maximumQueryRuntimeSchemaBytes || value != strings.TrimSpace(value) {
		return false
	}
	if validContractVersion(value) {
		return true
	}
	if !validOptionalOpaqueQueryRuntime(value, maximumQueryRuntimeSchemaBytes) || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || !strings.HasSuffix(value, ".json") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validateQueryRuntimePlan(plan *pluginwire.QueryRuntimePlan) *protocolwire.ErrorDetail {
	if plan == nil || !queryRuntimeDigestPattern.MatchString(plan.GetShapeDigest()) ||
		len(plan.GetFields()) == 0 || len(plan.GetFields()) > maximumQueryRuntimeFields ||
		len(plan.GetRelations()) > maximumQueryRuntimeRelations || len(plan.GetFilters()) > maximumQueryRuntimeFilters ||
		len(plan.GetSorts()) > maximumQueryRuntimeSorts || !validQueryRuntimeLocale(plan.GetLocale()) ||
		(plan.GetScope() != "" && (len(plan.GetScope()) > maximumQueryRuntimeScopeBytes ||
			!queryRuntimeIDPattern.MatchString(plan.GetScope()))) {
		return queryRuntimeInvalid("query_runtime.plan_invalid", "The Host-normalized query plan is invalid.")
	}
	// Relations remain Host-only in query.runtime@1; the field is reserved so a
	// future plan version can add explicit semantics without changing the envelope.
	if len(plan.GetRelations()) != 0 {
		return queryRuntimeError(protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"query_runtime.relations_host_only", "Third-party query execution cannot resolve relations.")
	}
	if !validQueryRuntimeNames(plan.GetFields(), maximumQueryRuntimeFields) ||
		!validQueryRuntimeNames(plan.GetRelations(), maximumQueryRuntimeRelations) ||
		!validQueryRuntimeFilters(plan.GetFilters()) || !validQueryRuntimeSorts(plan.GetSorts()) ||
		!validQueryRuntimePagination(plan.GetPagination(), plan.GetFetchLimit()) {
		return queryRuntimeInvalid("query_runtime.plan_invalid", "The Host-normalized query plan is invalid.")
	}
	return nil
}

func validQueryRuntimeNames(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validOpaqueQueryRuntime(value, maximumQueryRuntimeNameBytes) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validQueryRuntimeFilters(values []*pluginwire.QueryRuntimeFilter) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == nil || !validOpaqueQueryRuntime(value.GetField(), maximumQueryRuntimeNameBytes) ||
			!validOpaqueQueryRuntime(value.GetValue(), maximumQueryRuntimeFilterBytes) {
			return false
		}
		if _, duplicate := seen[value.GetField()]; duplicate {
			return false
		}
		seen[value.GetField()] = struct{}{}
	}
	return true
}

func validQueryRuntimeSorts(values []*pluginwire.QueryRuntimeSort) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == nil || !validOpaqueQueryRuntime(value.GetField(), maximumQueryRuntimeNameBytes) {
			return false
		}
		if _, duplicate := seen[value.GetField()]; duplicate {
			return false
		}
		seen[value.GetField()] = struct{}{}
	}
	return true
}

func validQueryRuntimePagination(value *pluginwire.QueryRuntimePagination, fetchLimit uint32) bool {
	if value == nil || value.GetOffset() > maximumQueryRuntimeOffset || fetchLimit < 1 ||
		fetchLimit > maximumQueryRuntimeFetchLimit {
		return false
	}
	switch value.GetMode() {
	case "none":
		return value.GetOffset() == 0 && value.GetLimit() == 1 && fetchLimit == 1
	case "offset", "cursor":
		return value.GetLimit() >= 1 && value.GetLimit() <= maximumQueryRuntimePageLimit &&
			fetchLimit == value.GetLimit()+1
	default:
		return false
	}
}

func validQueryRuntimeLocale(value string) bool {
	return value == "" || len(value) <= 32 && value == strings.TrimSpace(value) && queryRuntimeLocalePattern.MatchString(value)
}

func validQueryRuntimeTrace(value *protocolwire.TraceContext) bool {
	if value == nil {
		return true
	}
	if value.GetTraceId() != "" && (len(value.GetTraceId()) != 32 || !queryRuntimeTracePattern.MatchString(value.GetTraceId())) {
		return false
	}
	if value.GetSpanId() != "" && (len(value.GetSpanId()) != 16 || !queryRuntimeTracePattern.MatchString(value.GetSpanId())) {
		return false
	}
	return validOptionalOpaqueQueryRuntime(value.GetTraceparent(), maximumQueryRuntimeTraceBytes) &&
		validOptionalOpaqueQueryRuntime(value.GetTracestate(), maximumQueryRuntimeTraceBytes)
}

func validOpaqueQueryRuntime(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && validOptionalOpaqueQueryRuntime(value, maximum)
}

func validOptionalOpaqueQueryRuntime(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

// CanonicalQueryRuntimeRow validates one JSON object, rejects duplicate keys
// at every depth, and emits deterministic compact bytes while preserving every
// json.Number lexeme (including integers beyond IEEE-754 precision).
func CanonicalQueryRuntimeRow(raw []byte) (json.RawMessage, error) {
	canonical, _, err := canonicalQueryRuntimeRow(raw)
	return canonical, err
}

func canonicalQueryRuntimeRow(raw []byte) (json.RawMessage, int, error) {
	value, nodes, err := decodeQueryRuntimeJSONObject(raw)
	if err != nil {
		return nil, 0, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, 0, errInvalidQueryRuntimeJSON
	}
	if len(canonical) > maximumQueryRuntimeResultBytes {
		return nil, 0, errQueryRuntimeRowsTooLarge
	}
	return json.RawMessage(canonical), nodes, nil
}

// DecodeQueryRuntimeRow returns a detached object with numbers represented as
// json.Number, never float64.
func DecodeQueryRuntimeRow(raw []byte) (map[string]any, error) {
	canonical, err := CanonicalQueryRuntimeRow(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, errInvalidQueryRuntimeJSON
	}
	return value, nil
}

func decodeQueryRuntimeJSONObject(raw []byte) (map[string]any, int, error) {
	if len(raw) == 0 || len(raw) > maximumQueryRuntimeResultBytes {
		return nil, 0, errQueryRuntimeRowsTooLarge
	}
	if !utf8.Valid(raw) {
		return nil, 0, errInvalidQueryRuntimeJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	value, err := readQueryRuntimeJSONValue(decoder, 0, &nodes)
	if err != nil {
		return nil, 0, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, 0, fmt.Errorf("%w: row must be an object", errInvalidQueryRuntimeJSON)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, 0, fmt.Errorf("%w: trailing JSON data", errInvalidQueryRuntimeJSON)
	}
	return object, nodes, nil
}

func readQueryRuntimeJSONValue(decoder *json.Decoder, depth int, nodes *int) (any, error) {
	if depth > maximumQueryRuntimeJSONDepth || *nodes >= maximumQueryRuntimeJSONNodes {
		return nil, errQueryRuntimeRowsTooLarge
	}
	*nodes = *nodes + 1
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidQueryRuntimeJSON, err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("%w: %v", errInvalidQueryRuntimeJSON, err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errInvalidQueryRuntimeJSON
				}
				if _, duplicate := object[key]; duplicate {
					return nil, fmt.Errorf("%w: duplicate object key %q", errInvalidQueryRuntimeJSON, key)
				}
				child, err := readQueryRuntimeJSONValue(decoder, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
				return nil, errInvalidQueryRuntimeJSON
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := readQueryRuntimeJSONValue(decoder, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
				return nil, errInvalidQueryRuntimeJSON
			}
			return array, nil
		default:
			return nil, errInvalidQueryRuntimeJSON
		}
	case json.Number, string, bool, nil:
		return value, nil
	default:
		return nil, errInvalidQueryRuntimeJSON
	}
}

func normalizeQueryRuntimeRows(
	input *pluginwire.QueryRuntimeRows,
	fetchLimit uint32,
	reason string,
) (*pluginwire.QueryRuntimeRows, []json.RawMessage, *protocolwire.ErrorDetail) {
	if input == nil || len(input.GetRows()) > int(fetchLimit) {
		return nil, nil, queryRuntimeInvalid(reason, "Query runtime input rows exceed the Host fetch limit.")
	}
	raw := make([]json.RawMessage, 0, len(input.GetRows()))
	for _, row := range input.GetRows() {
		if row == nil {
			return nil, nil, queryRuntimeInvalid(reason, "Query runtime input contains an invalid row.")
		}
		raw = append(raw, json.RawMessage(row.GetCanonicalJson()))
	}
	normalized, detail := encodeQueryRuntimeRows(raw, fetchLimit, "query_filter.input")
	if detail != nil {
		return nil, nil, queryRuntimeInvalid(reason, "Query runtime input contains invalid canonical JSON.")
	}
	rows := make([]json.RawMessage, 0, len(normalized.GetRows()))
	for index, row := range normalized.GetRows() {
		if !bytes.Equal(row.GetCanonicalJson(), input.GetRows()[index].GetCanonicalJson()) {
			return nil, nil, queryRuntimeInvalid(reason, "Query runtime input rows must already be canonical JSON.")
		}
		rows = append(rows, append(json.RawMessage(nil), row.GetCanonicalJson()...))
	}
	return normalized, rows, nil
}

func encodeQueryRuntimeRows(
	rows []json.RawMessage,
	fetchLimit uint32,
	family string,
) (*pluginwire.QueryRuntimeRows, *protocolwire.ErrorDetail) {
	if fetchLimit < 1 || fetchLimit > maximumQueryRuntimeFetchLimit || len(rows) > int(fetchLimit) {
		return nil, queryRuntimeError(protocolwire.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE,
			family+".rows_exceeded", "Plugin query rows exceed the Host fetch limit.")
	}
	result := &pluginwire.QueryRuntimeRows{Rows: make([]*pluginwire.QueryRuntimeRow, 0, len(rows))}
	total := 2
	totalNodes := 0
	for index, row := range rows {
		canonical, nodes, err := canonicalQueryRuntimeRow(row)
		if err != nil {
			code := protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
			if errors.Is(err, errQueryRuntimeRowsTooLarge) {
				code = protocolwire.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE
			}
			return nil, queryRuntimeError(code, family+".row_invalid", "Plugin query rows must be bounded canonical JSON objects.")
		}
		if index > 0 {
			total++
		}
		if nodes > maximumQueryRuntimeJSONNodes-totalNodes || len(canonical) > maximumQueryRuntimeResultBytes-total {
			return nil, queryRuntimeError(protocolwire.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE,
				family+".result_too_large", "Plugin query rows exceed the Host result-byte limit.")
		}
		total += len(canonical)
		totalNodes += nodes
		result.Rows = append(result.Rows, &pluginwire.QueryRuntimeRow{CanonicalJson: canonical})
	}
	return result, nil
}

func cloneQueryRuntimeBinding(value *pluginwire.QueryRuntimeBinding) *pluginwire.QueryRuntimeBinding {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*pluginwire.QueryRuntimeBinding)
}

func cloneQueryResultFilterRuntimeBinding(
	value *pluginwire.QueryResultFilterRuntimeBinding,
) *pluginwire.QueryResultFilterRuntimeBinding {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*pluginwire.QueryResultFilterRuntimeBinding)
}

func cloneQueryRuntimePlan(value *pluginwire.QueryRuntimePlan) *pluginwire.QueryRuntimePlan {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*pluginwire.QueryRuntimePlan)
}

func queryRuntimeInvalid(reason, message string) *protocolwire.ErrorDetail {
	return queryRuntimeError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, reason, message)
}

func queryRuntimeError(code protocolwire.ErrorCode, reason, message string) *protocolwire.ErrorDetail {
	return &protocolwire.ErrorDetail{Code: code, Reason: reason, Message: message}
}
