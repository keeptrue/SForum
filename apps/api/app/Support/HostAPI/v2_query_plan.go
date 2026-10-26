package hostapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type protocolV2QueryField struct {
	Name       string
	Expression string
}

type protocolV2QueryFilterDefinition struct {
	Field      string
	Operator   string
	Expression string
	SchemaID   string
	Kind       string
}

type protocolV2QuerySortDefinition struct {
	Field      string
	Expression string
}

type protocolV2QueryDefinition struct {
	ID                  string
	PlanVersion         string
	ResultSchemaID      string
	ResultSchemaVersion string
	From                string
	Fields              []protocolV2QueryField
	Filters             []protocolV2QueryFilterDefinition
	RequiredFilters     []string
	Sorts               []protocolV2QuerySortDefinition
	DefaultSorts        []protocolV2QuerySort
	Single              bool
	// ActorScoped marks sealed Host projections whose SQL may use the actor
	// bound to an opaque Query Registry delegation. The actor never enters the
	// plugin request, filter values, result rows, trace, or cache identity.
	ActorScoped bool
	// ConsentGated marks a sensitive definition that may only execute behind a
	// Host-issued single-use actor delegation plus an exact-artifact capability
	// grant. The actorless stable entry always refuses such a definition; the
	// delegated Query Registry projection is the only executable outlet.
	ConsentGated bool
}

type protocolV2QueryFilter struct {
	Definition protocolV2QueryFilterDefinition
	Value      any
}

type protocolV2QuerySort struct {
	Field      string
	Expression string
	Descending bool
}

type protocolV2QueryPlan struct {
	Definition  protocolV2QueryDefinition
	Fields      []protocolV2QueryField
	Filters     []protocolV2QueryFilter
	Sorts       []protocolV2QuerySort
	Offset      int
	Limit       int
	FetchLimit  int
	ShapeDigest string
}

type protocolV2QueryKey struct {
	id      string
	version string
}

// protocolV2QueryInt64ListFilterKind filters on a canonical JSON array of
// positive int64 ids (strictly ascending, unique, 1..MaxBatchIDs). The
// executor binds it with `= ANY($n)`; the shortcode projections share the kind.
const protocolV2QueryInt64ListFilterKind = "int64_list"

func buildProtocolV2QueryPlan(
	definition protocolV2QueryDefinition,
	request *hostv2.QueryRequest,
) (protocolV2QueryPlan, *protocolv2.ErrorDetail) {
	if request.GetOffset() != 0 {
		return protocolV2QueryPlan{}, queryInvalid("host.query_page_invalid", "Stable Host Queries use their signed cursor and do not accept a raw offset.")
	}
	if hasProtocolV2QueryRegistryContractFields(request) {
		return protocolV2QueryPlan{}, queryInvalid("host.query_shape_unsupported", "Stable Host Queries do not accept Query Registry contract fields.")
	}
	if (request.GetResultSchemaId() != "" && request.GetResultSchemaId() != definition.ResultSchemaID) ||
		(request.GetResultSchemaVersion() != "" && request.GetResultSchemaVersion() != definition.ResultSchemaVersion) {
		return protocolV2QueryPlan{}, queryInvalid("host.query_schema_mismatch", "The requested result schema is not supported.")
	}
	fields, err := selectProtocolV2QueryFields(definition, request.GetFields())
	if err != nil {
		return protocolV2QueryPlan{}, queryInvalid("host.query_field_unsupported", err.Error())
	}
	filters, err := selectProtocolV2QueryFilters(definition, request.GetFilters())
	if err != nil {
		return protocolV2QueryPlan{}, queryInvalid("host.query_filter_invalid", err.Error())
	}
	sorts, err := selectProtocolV2QuerySorts(definition, request.GetSorts())
	if err != nil {
		return protocolV2QueryPlan{}, queryInvalid("host.query_sort_invalid", err.Error())
	}
	limit := int(request.GetPage().GetLimit())
	if definition.Single {
		if limit > 1 || request.GetPage().GetCursor() != "" || len(request.GetSorts()) > 0 {
			return protocolV2QueryPlan{}, queryInvalid("host.query_page_invalid", "Single-row queries do not accept cursors, sorts, or a limit above one.")
		}
		limit = 1
	} else if limit == 0 {
		limit = protocolV2QueryDefaultLimit
	} else if limit > protocolV2QueryMaximumLimit {
		return protocolV2QueryPlan{}, queryInvalid("host.query_page_invalid", "The query limit exceeds the maximum of 100 rows.")
	}
	shapeDigest := protocolV2QueryShapeDigest(definition, fields, filters, sorts)
	offset := 0
	if cursor := request.GetPage().GetCursor(); cursor != "" {
		decoded, decodeErr := decodeProtocolV2QueryCursor(cursor)
		if decodeErr != nil || decoded.QueryID != definition.ID || decoded.PlanVersion != definition.PlanVersion ||
			decoded.ShapeDigest != shapeDigest || decoded.Offset < 0 || decoded.Offset > protocolV2QueryMaximumOffset {
			return protocolV2QueryPlan{}, queryInvalid("host.query_cursor_invalid", "The query cursor is invalid or belongs to a different query shape.")
		}
		offset = decoded.Offset
	}
	return protocolV2QueryPlan{
		Definition: definition, Fields: fields, Filters: filters, Sorts: sorts,
		Offset: offset, Limit: limit, FetchLimit: limit + 1, ShapeDigest: shapeDigest,
	}, nil
}

func selectProtocolV2QueryFields(definition protocolV2QueryDefinition, requested []string) ([]protocolV2QueryField, error) {
	byName := make(map[string]protocolV2QueryField, len(definition.Fields))
	for _, field := range definition.Fields {
		byName[field.Name] = field
	}
	if len(requested) == 0 {
		return append([]protocolV2QueryField(nil), definition.Fields...), nil
	}
	result := make([]protocolV2QueryField, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		field, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("field %q is not allowlisted", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("field %q is duplicated", name)
		}
		seen[name] = struct{}{}
		result = append(result, field)
	}
	return result, nil
}

func selectProtocolV2QueryFilters(definition protocolV2QueryDefinition, requested []*hostv2.QueryFilter) ([]protocolV2QueryFilter, error) {
	byKey := make(map[string]protocolV2QueryFilterDefinition, len(definition.Filters))
	for _, filter := range definition.Filters {
		byKey[filter.Field+"\x00"+filter.Operator] = filter
	}
	result := make([]protocolV2QueryFilter, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, item := range requested {
		if item == nil {
			return nil, errors.New("nil filter is not allowed")
		}
		key := item.GetField() + "\x00" + item.GetOperator()
		filterDefinition, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("filter %q %q is not allowlisted", item.GetField(), item.GetOperator())
		}
		if _, exists := seen[item.GetField()]; exists {
			return nil, fmt.Errorf("filter field %q is duplicated", item.GetField())
		}
		value, err := protocolV2QueryParameter(item.GetValue(), filterDefinition)
		if err != nil {
			return nil, err
		}
		seen[item.GetField()] = struct{}{}
		result = append(result, protocolV2QueryFilter{Definition: filterDefinition, Value: value})
	}
	for _, required := range definition.RequiredFilters {
		if _, exists := seen[required]; !exists {
			return nil, fmt.Errorf("filter field %q is required", required)
		}
	}
	return result, nil
}

func protocolV2QueryParameter(document *protocolv2.TypedDocument, definition protocolV2QueryFilterDefinition) (any, error) {
	if document == nil || document.GetValue() == nil || document.GetSchemaId() != definition.SchemaID ||
		document.GetSchemaVersion() != QueryStableCoreParameterSchemaV1 {
		return nil, fmt.Errorf("filter field %q has the wrong parameter schema", definition.Field)
	}
	values := document.GetValue().AsMap()
	value, exists := values["value"]
	if !exists || len(values) != 1 {
		return nil, fmt.Errorf("filter field %q has no value", definition.Field)
	}
	switch definition.Kind {
	case "int64":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("filter field %q must use a decimal string", definition.Field)
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != text {
			return nil, fmt.Errorf("filter field %q is not a positive canonical int64", definition.Field)
		}
		return parsed, nil
	case "text":
		text, ok := value.(string)
		if !ok || text == "" || text != strings.TrimSpace(text) || len(text) > 200 {
			return nil, fmt.Errorf("filter field %q is not valid text", definition.Field)
		}
		return text, nil
	case protocolV2QueryInt64ListFilterKind:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("filter field %q must use a canonical JSON int64 list", definition.Field)
		}
		ids, listErr := parseProtocolV2QueryInt64List(text)
		if listErr != nil {
			return nil, fmt.Errorf("filter field %q is not a valid id list", definition.Field)
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("filter field %q has an unsupported kind", definition.Field)
	}
}

// parseProtocolV2QueryInt64List accepts only the canonical form: strictly
// ascending, unique, positive int64 ids as compact JSON, at most
// protocolV2QueryMaxBatchIDs entries. Canonical input keeps the plan shape
// digest deterministic for cursor and cache identity.
func parseProtocolV2QueryInt64List(value string) ([]int64, error) {
	if value == "" || len(value) > 4096 {
		return nil, errors.New("id list is missing or too large")
	}
	var ids []int64
	decoder := json.NewDecoder(strings.NewReader(value))
	if err := decoder.Decode(&ids); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("id list has trailing content")
	}
	if len(ids) == 0 || len(ids) > protocolV2QueryMaxBatchIDs {
		return nil, fmt.Errorf("id list must contain 1..%d ids", protocolV2QueryMaxBatchIDs)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("id list entries must be positive")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("id list entries must be unique")
		}
		seen[id] = struct{}{}
	}
	canonical := "["
	for index, id := range ids {
		if index > 0 && id <= ids[index-1] {
			return nil, errors.New("id list must be strictly ascending")
		}
		if index > 0 {
			canonical += ","
		}
		canonical += strconv.FormatInt(id, 10)
	}
	if canonical+"]" != value {
		return nil, errors.New("id list is not canonical")
	}
	return ids, nil
}

func selectProtocolV2QuerySorts(definition protocolV2QueryDefinition, requested []*hostv2.QuerySort) ([]protocolV2QuerySort, error) {
	if len(requested) == 0 {
		return append([]protocolV2QuerySort(nil), definition.DefaultSorts...), nil
	}
	if len(requested) > 2 {
		return nil, errors.New("at most two sort fields are allowed")
	}
	byName := make(map[string]protocolV2QuerySortDefinition, len(definition.Sorts))
	for _, sort := range definition.Sorts {
		byName[sort.Field] = sort
	}
	result := make([]protocolV2QuerySort, 0, len(requested)+1)
	seen := make(map[string]struct{}, len(requested))
	for _, item := range requested {
		if item == nil {
			return nil, errors.New("nil sort is not allowed")
		}
		sortDefinition, ok := byName[item.GetField()]
		if !ok {
			return nil, fmt.Errorf("sort field %q is not allowlisted", item.GetField())
		}
		if _, exists := seen[item.GetField()]; exists {
			return nil, fmt.Errorf("sort field %q is duplicated", item.GetField())
		}
		seen[item.GetField()] = struct{}{}
		result = append(result, protocolV2QuerySort{Field: sortDefinition.Field, Expression: sortDefinition.Expression, Descending: item.GetDescending()})
	}
	if id, ok := byName["id"]; ok {
		if _, exists := seen["id"]; !exists {
			result = append(result, protocolV2QuerySort{Field: id.Field, Expression: id.Expression, Descending: true})
		}
	}
	return result, nil
}

type protocolV2QueryCursor struct {
	QueryID     string `json:"queryId"`
	PlanVersion string `json:"planVersion"`
	ShapeDigest string `json:"shapeDigest"`
	Offset      int    `json:"offset"`
}

func encodeProtocolV2QueryCursor(cursor protocolV2QueryCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeProtocolV2QueryCursor(value string) (protocolV2QueryCursor, error) {
	if value == "" || len(value) > 1024 {
		return protocolV2QueryCursor{}, errors.New("invalid query cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return protocolV2QueryCursor{}, err
	}
	var cursor protocolV2QueryCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return protocolV2QueryCursor{}, err
	}
	return cursor, nil
}

func protocolV2QueryShapeDigest(definition protocolV2QueryDefinition, fields []protocolV2QueryField, filters []protocolV2QueryFilter, sorts []protocolV2QuerySort) string {
	document := struct {
		ID      string                  `json:"id"`
		Version string                  `json:"version"`
		Fields  []protocolV2QueryField  `json:"fields"`
		Filters []protocolV2QueryFilter `json:"filters"`
		Sorts   []protocolV2QuerySort   `json:"sorts"`
	}{definition.ID, definition.PlanVersion, fields, filters, sorts}
	encoded, _ := json.Marshal(document)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateProtocolV2QueryDefinition(definition protocolV2QueryDefinition) error {
	if definition.ID == "" || definition.PlanVersion == "" || definition.ResultSchemaID == "" ||
		definition.ResultSchemaVersion == "" || definition.From == "" || len(definition.Fields) == 0 {
		return errors.New("hostapi: query identity, schema, source, and fields are required")
	}
	seen := make(map[string]struct{})
	for _, field := range definition.Fields {
		if field.Name == "" || field.Expression == "" {
			return errors.New("hostapi: query field name and expression are required")
		}
		if _, exists := seen[field.Name]; exists {
			return fmt.Errorf("hostapi: duplicate query field %s", field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	return nil
}

func cloneProtocolV2QueryDefinition(source protocolV2QueryDefinition) protocolV2QueryDefinition {
	source.Fields = append([]protocolV2QueryField(nil), source.Fields...)
	source.Filters = append([]protocolV2QueryFilterDefinition(nil), source.Filters...)
	source.RequiredFilters = append([]string(nil), source.RequiredFilters...)
	source.Sorts = append([]protocolV2QuerySortDefinition(nil), source.Sorts...)
	source.DefaultSorts = append([]protocolV2QuerySort(nil), source.DefaultSorts...)
	return source
}

func queryInvalid(reason, message string) *protocolv2.ErrorDetail {
	return queryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, reason, message, false)
}

func queryError(code protocolv2.ErrorCode, reason, message string, retryable bool) *protocolv2.ErrorDetail {
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Retryable: retryable}
}
