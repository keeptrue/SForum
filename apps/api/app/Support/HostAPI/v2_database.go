package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolV2DatabaseOwnSchema = "own_schema"

	ProtocolV2DatabaseString = "string"
	ProtocolV2DatabaseInt64  = "int64"
	ProtocolV2DatabaseNumber = "number"
	ProtocolV2DatabaseBool   = "bool"

	protocolV2DatabaseDefaultTimeout       = 3 * time.Second
	protocolV2DatabaseMaximumTimeout       = 5 * time.Second
	protocolV2DatabaseRollbackTimeout      = 5 * time.Second
	protocolV2DatabaseDefaultRows          = 20
	protocolV2DatabaseMaximumRows          = 1000
	protocolV2DatabaseMaximumAffectedRows  = 10_000
	protocolV2DatabaseMaximumParameters    = 32
	protocolV2DatabaseMaximumColumns       = 64
	protocolV2DatabaseMaximumParameterSize = 64 << 10
	protocolV2DatabaseMaximumRequestSize   = 256 << 10
	protocolV2DatabaseMaximumRowSize       = 64 << 10
	protocolV2DatabaseMaximumResultSize    = 1 << 20
	protocolV2DatabaseMaximumSQLSize       = 64 << 10
)

var protocolV2DatabaseOperationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)
var protocolV2DatabaseVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,127}$`)
var protocolV2DatabaseDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var protocolV2DatabasePlaceholderPattern = regexp.MustCompile(`\$([1-9][0-9]*)`)

// ProtocolV2DatabaseParameter declares one positional argument. The document
// must contain exactly Field and use the declared schema and scalar kind.
type ProtocolV2DatabaseParameter struct {
	SchemaID      string
	SchemaVersion string
	Field         string
	Kind          string
	Nullable      bool
	MaxBytes      int
}

// ProtocolV2DatabaseColumn is the complete allowlist for a returned row.
type ProtocolV2DatabaseColumn struct {
	Name     string
	Nullable bool
}

// ProtocolV2DatabaseQueryDefinition is Host-owned configuration. SQL is
// frozen when the runtime is constructed and is never accepted over RPC.
type ProtocolV2DatabaseQueryDefinition struct {
	ExtensionID         string
	ExtensionVersion    string
	PackageDigest       string
	OperationID         string
	StatementVersion    string
	Scope               string
	SQL                 string
	Parameters          []ProtocolV2DatabaseParameter
	ResultSchemaID      string
	ResultSchemaVersion string
	Columns             []ProtocolV2DatabaseColumn
	MaxRows             int
	Timeout             time.Duration
}

// ProtocolV2DatabaseExecuteDefinition always runs under the plugin runtime
// role. ReturningColumns may describe at most one RETURNING row.
type ProtocolV2DatabaseExecuteDefinition struct {
	ExtensionID           string
	ExtensionVersion      string
	PackageDigest         string
	OperationID           string
	StatementVersion      string
	SQL                   string
	Parameters            []ProtocolV2DatabaseParameter
	ResultSchemaID        string
	ResultSchemaVersion   string
	ReturningColumns      []ProtocolV2DatabaseColumn
	MaxAffectedRows       uint64
	QueryInvalidationTags []string
	Timeout               time.Duration
}

type protocolV2DatabaseKey struct {
	extensionID      string
	extensionVersion string
	packageDigest    string
	operationID      string
	version          string
}

type protocolV2DatabaseScope struct {
	ExtensionID        string
	ExtensionVersionID int64
	ExtensionVersion   string
	PackageDigest      string
	TrustGrantID       int64
	AuthorityType      string
	SchemaName         string
	RuntimeRoleName    string
	Scope              string
	OperationID        string
	StatementVersion   string
	IdempotencyKey     string
}

type protocolV2DatabaseReceipt struct {
	Fingerprint  string
	AffectedRows uint64
	Result       *protocolv2.TypedDocument
}

type protocolV2DatabaseTx interface {
	ResolveScope(context.Context, *protocolv2.ExtensionIdentity, string, string, string) (protocolV2DatabaseScope, error)
	Query(context.Context, protocolV2DatabaseScope, string, []any, int, int) ([]map[string]any, error)
	Execute(context.Context, protocolV2DatabaseScope, string, []any, bool) (uint64, []map[string]any, error)
	LockReceipt(context.Context, protocolV2DatabaseScope, string) (*protocolV2DatabaseReceipt, error)
	SaveReceipt(context.Context, protocolV2DatabaseScope, string, protocolV2DatabaseReceipt) error
	EnqueueQueryInvalidation(context.Context, string, []string) error
	Commit(context.Context) error
	Rollback(context.Context) error
}

type protocolV2DatabaseBackend interface {
	Begin(context.Context, bool) (protocolV2DatabaseTx, error)
}

type protocolV2DatabaseEngine struct {
	backend       protocolV2DatabaseBackend
	queries       map[protocolV2DatabaseKey]ProtocolV2DatabaseQueryDefinition
	executes      map[protocolV2DatabaseKey]ProtocolV2DatabaseExecuteDefinition
	traceSink     DatabaseTraceSink
	slowThreshold time.Duration
}

// ProtocolV2DatabaseRuntime exposes only the generated service interface; its
// immutable statement catalog cannot be mutated after construction.
type ProtocolV2DatabaseRuntime interface {
	DatabaseService() hostv2.DatabaseServiceServer
	databaseEngine() *protocolV2DatabaseEngine
}

type protocolV2DatabaseRuntime struct {
	engine *protocolV2DatabaseEngine
	server hostv2.DatabaseServiceServer
}

func (r *protocolV2DatabaseRuntime) DatabaseService() hostv2.DatabaseServiceServer {
	if r == nil {
		return nil
	}
	return r.server
}

func (r *protocolV2DatabaseRuntime) databaseEngine() *protocolV2DatabaseEngine {
	if r == nil {
		return nil
	}
	return r.engine
}

func newProtocolV2DatabaseRuntime(
	backend protocolV2DatabaseBackend,
	queries []ProtocolV2DatabaseQueryDefinition,
	executes []ProtocolV2DatabaseExecuteDefinition,
	options ...ProtocolV2DatabaseRuntimeOption,
) (ProtocolV2DatabaseRuntime, error) {
	engine, err := newProtocolV2DatabaseEngine(backend, queries, executes, options...)
	if err != nil {
		return nil, err
	}
	server := &protocolV2DatabaseServer{engine: engine}
	return &protocolV2DatabaseRuntime{engine: engine, server: server}, nil
}

func newProtocolV2DatabaseEngine(
	backend protocolV2DatabaseBackend,
	queries []ProtocolV2DatabaseQueryDefinition,
	executes []ProtocolV2DatabaseExecuteDefinition,
	options ...ProtocolV2DatabaseRuntimeOption,
) (*protocolV2DatabaseEngine, error) {
	if backend == nil {
		return nil, errors.New("hostapi: database backend is required")
	}
	queryCatalog := make(map[protocolV2DatabaseKey]ProtocolV2DatabaseQueryDefinition, len(queries))
	executeCatalog := make(map[protocolV2DatabaseKey]ProtocolV2DatabaseExecuteDefinition, len(executes))
	seen := make(map[protocolV2DatabaseKey]struct{}, len(queries)+len(executes))
	for _, source := range queries {
		definition := cloneProtocolV2DatabaseQueryDefinition(source)
		if err := validateProtocolV2DatabaseQueryDefinition(definition); err != nil {
			return nil, err
		}
		key := protocolV2DatabaseKey{
			extensionID: definition.ExtensionID, extensionVersion: definition.ExtensionVersion,
			packageDigest: definition.PackageDigest, operationID: definition.OperationID, version: definition.StatementVersion,
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("hostapi: duplicate database operation %s@%s", key.operationID, key.version)
		}
		seen[key] = struct{}{}
		queryCatalog[key] = definition
	}
	for _, source := range executes {
		definition := cloneProtocolV2DatabaseExecuteDefinition(source)
		if err := validateProtocolV2DatabaseExecuteDefinition(definition); err != nil {
			return nil, err
		}
		key := protocolV2DatabaseKey{
			extensionID: definition.ExtensionID, extensionVersion: definition.ExtensionVersion,
			packageDigest: definition.PackageDigest, operationID: definition.OperationID, version: definition.StatementVersion,
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("hostapi: duplicate database operation %s@%s", key.operationID, key.version)
		}
		seen[key] = struct{}{}
		executeCatalog[key] = definition
	}
	runtimeOptions := resolveProtocolV2DatabaseRuntimeOptions(options)
	return &protocolV2DatabaseEngine{
		backend: backend, queries: queryCatalog, executes: executeCatalog,
		traceSink: runtimeOptions.traceSink, slowThreshold: ProtocolV2DatabaseDefaultSlowThreshold,
	}, nil
}

type protocolV2DatabaseServer struct {
	hostv2.UnimplementedDatabaseServiceServer
	engine *protocolV2DatabaseEngine
}

func (s *protocolV2DatabaseServer) Query(ctx context.Context, request *hostv2.DatabaseQueryRequest) (*hostv2.DatabaseQueryResponse, error) {
	if s == nil || s.engine == nil {
		return unavailableProtocolV2DatabaseQuery(request), nil
	}
	return s.engine.query(ctx, request)
}

func (s *protocolV2DatabaseServer) Execute(ctx context.Context, request *hostv2.DatabaseExecuteRequest) (*hostv2.DatabaseExecuteResponse, error) {
	if s == nil || s.engine == nil {
		return unavailableProtocolV2DatabaseExecute(request), nil
	}
	return s.engine.execute(ctx, request)
}

func (s *protocolV2DatabaseServer) StreamQuery(request *hostv2.DatabaseQueryRequest, stream grpc.ServerStreamingServer[hostv2.DatabaseRow]) error {
	// V1 deliberately reuses the bounded unary result. It provides a streaming
	// transport shape, not an unbounded PostgreSQL cursor.
	response, err := s.Query(stream.Context(), request)
	if err != nil {
		return err
	}
	if response.GetError() != nil {
		return stream.Send(&hostv2.DatabaseRow{Context: response.GetContext(), Error: response.GetError()})
	}
	for index, row := range response.GetRows() {
		if err := stream.Send(&hostv2.DatabaseRow{Context: response.GetContext(), Sequence: uint64(index + 1), Value: row}); err != nil {
			return err
		}
	}
	return nil
}

func (e *protocolV2DatabaseEngine) query(
	ctx context.Context,
	request *hostv2.DatabaseQueryRequest,
) (result *hostv2.DatabaseQueryResponse, rpcErr error) {
	startedAt := time.Now()
	trace := newProtocolV2DatabaseTrace(ctx, request.GetOperationId(), request.GetStatementVersion(), "query")
	defer func() {
		var detail *protocolv2.ErrorDetail
		if result != nil {
			detail = result.GetError()
			trace.Rows = len(result.GetRows())
		}
		e.recordDatabaseTrace(startedAt, trace, detail, rpcErr)
	}()
	identity, detail := protocolV2DatabaseRequestIdentity(ctx, request.GetContext())
	response := &hostv2.DatabaseQueryResponse{
		Context: protocolV2DatabaseResponseContext(request.GetContext(), identity),
		Page:    &protocolv2.PageInfo{},
	}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if request.GetStableCoreView() {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_stable_core_view_use_host_query", "Stable core reads are available through HostQueryService, not DatabaseService.", false)
		return response, nil
	}
	key := protocolV2DatabaseKey{
		extensionID: identity.GetExtensionId(), extensionVersion: identity.GetExtensionVersion(),
		packageDigest: identity.GetArtifactDigest(), operationID: request.GetOperationId(), version: request.GetStatementVersion(),
	}
	definition, ok := e.queries[key]
	if !ok || key.operationID != strings.TrimSpace(key.operationID) || key.version != strings.TrimSpace(key.version) {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_operation_unsupported", "The database operation id or statement version is not registered.", false)
		return response, nil
	}
	trace.ShapeDigest = protocolV2DatabaseQueryTraceShapeDigest(definition)
	arguments, detail := protocolV2DatabaseParameters(request.GetParameters(), definition.Parameters)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	limit, offset, cursorDigest, detail := protocolV2DatabasePage(request, definition)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, databaseTimeout(definition.Timeout))
	defer cancel()
	tx, err := e.backend.Begin(operationCtx, true)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_transaction_unavailable")
		return response, nil
	}
	committed := false
	defer func() {
		if !committed {
			if err := rollbackProtocolV2DatabaseTx(ctx, tx); err != nil {
				result = nil
				rpcErr = status.Error(codes.Internal, "Host database query rollback failed; transaction outcome is unknown")
			}
		}
	}()
	scope, err := tx.ResolveScope(operationCtx, identity, definition.Scope, definition.OperationID, definition.StatementVersion)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_identity_invalid")
		return response, nil
	}
	rows, err := tx.Query(operationCtx, scope, definition.SQL, arguments, limit+1, offset)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_query_failed")
		return response, nil
	}
	if len(rows) > limit+1 {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_result_rows_exceeded", "The database backend exceeded the registered fetch bound.", false)
		return response, nil
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	documents, detail := protocolV2DatabaseRows(rows, definition.ResultSchemaID, definition.ResultSchemaVersion, definition.Columns)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if err := tx.Commit(operationCtx); err != nil {
		return nil, status.Error(codes.Internal, "Host database query commit failed; transaction outcome is unknown")
	}
	committed = true
	response.Rows = documents
	response.Page.HasMore = hasMore
	if hasMore {
		response.Page.NextCursor = encodeProtocolV2DatabaseCursor(protocolV2DatabaseCursor{
			OperationID: definition.OperationID, StatementVersion: definition.StatementVersion,
			RequestDigest: cursorDigest, Offset: offset + limit,
		})
	}
	return response, nil
}

func (e *protocolV2DatabaseEngine) execute(
	ctx context.Context,
	request *hostv2.DatabaseExecuteRequest,
) (result *hostv2.DatabaseExecuteResponse, rpcErr error) {
	startedAt := time.Now()
	trace := newProtocolV2DatabaseTrace(ctx, request.GetOperationId(), request.GetStatementVersion(), "execute")
	defer func() {
		var detail *protocolv2.ErrorDetail
		if result != nil {
			detail = result.GetError()
			trace.AffectedRows = result.GetAffectedRows()
			if result.GetResult() != nil {
				trace.Rows = 1
			}
		}
		e.recordDatabaseTrace(startedAt, trace, detail, rpcErr)
	}()
	identity, detail := protocolV2DatabaseRequestIdentity(ctx, request.GetContext())
	response := &hostv2.DatabaseExecuteResponse{Context: protocolV2DatabaseResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	key := protocolV2DatabaseKey{
		extensionID: identity.GetExtensionId(), extensionVersion: identity.GetExtensionVersion(),
		packageDigest: identity.GetArtifactDigest(), operationID: request.GetOperationId(), version: request.GetStatementVersion(),
	}
	definition, ok := e.executes[key]
	if !ok || key.operationID != strings.TrimSpace(key.operationID) || key.version != strings.TrimSpace(key.version) {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_operation_unsupported", "The database operation id or statement version is not registered.", false)
		return response, nil
	}
	trace.ShapeDigest = protocolV2DatabaseExecuteTraceShapeDigest(definition)
	idempotencyKey, detail := protocolV2DatabaseIdempotencyKey(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	arguments, detail := protocolV2DatabaseParameters(request.GetParameters(), definition.Parameters)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	fingerprint, err := protocolV2DatabaseExecuteFingerprint(request, identity)
	if err != nil {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_fingerprint_failed", "The database operation could not be prepared.", false)
		return response, nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, databaseTimeout(definition.Timeout))
	defer cancel()
	tx, err := e.backend.Begin(operationCtx, false)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_transaction_unavailable")
		return response, nil
	}
	committed := false
	defer func() {
		if !committed {
			if err := rollbackProtocolV2DatabaseTx(ctx, tx); err != nil {
				result = nil
				rpcErr = status.Error(codes.Internal, "Host database execute rollback failed; transaction outcome is unknown")
			}
		}
	}()
	scope, err := tx.ResolveScope(operationCtx, identity, ProtocolV2DatabaseOwnSchema, definition.OperationID, definition.StatementVersion)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_identity_invalid")
		return response, nil
	}
	scope.IdempotencyKey = idempotencyKey
	receipt, err := tx.LockReceipt(operationCtx, scope, fingerprint)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_idempotency_unavailable")
		return response, nil
	}
	if receipt != nil {
		if receipt.Fingerprint != fingerprint {
			response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.database_idempotency_conflict", "The idempotency key was already used for a different database request.", false)
			return response, nil
		}
		if err := tx.Commit(operationCtx); err != nil {
			return nil, status.Error(codes.Internal, "Host database replay commit failed; transaction outcome is unknown")
		}
		committed = true
		response.AffectedRows = receipt.AffectedRows
		response.Result = cloneProtocolV2Document(receipt.Result)
		return response, nil
	}
	affected, rows, err := tx.Execute(operationCtx, scope, definition.SQL, arguments, len(definition.ReturningColumns) > 0)
	if err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_execute_failed")
		return response, nil
	}
	if affected > definition.MaxAffectedRows {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_affected_rows_exceeded", "The database operation exceeded its registered affected-row limit.", false)
		return response, nil
	}
	if len(rows) > 1 {
		response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_result_rows_exceeded", "The database operation returned more than one result row.", false)
		return response, nil
	}
	var resultDocument *protocolv2.TypedDocument
	if len(definition.ReturningColumns) > 0 {
		if len(rows) != 1 {
			response.Error = databaseError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.database_result_required", "The database operation did not return its registered result row.", false)
			return response, nil
		}
		documents, rowDetail := protocolV2DatabaseRows(rows, definition.ResultSchemaID, definition.ResultSchemaVersion, definition.ReturningColumns)
		if rowDetail != nil {
			response.Error = rowDetail
			return response, nil
		}
		resultDocument = documents[0]
	}
	receipt = &protocolV2DatabaseReceipt{Fingerprint: fingerprint, AffectedRows: affected, Result: cloneProtocolV2Document(resultDocument)}
	if err := tx.SaveReceipt(operationCtx, scope, fingerprint, *receipt); err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_receipt_failed")
		return response, nil
	}
	if err := tx.EnqueueQueryInvalidation(operationCtx, scope.ExtensionID, definition.QueryInvalidationTags); err != nil {
		response.Error = databaseExecutionError(err, operationCtx, "host.database_query_invalidation_unavailable")
		return response, nil
	}
	if err := tx.Commit(operationCtx); err != nil {
		return nil, status.Error(codes.Internal, "Host database execute commit failed; transaction outcome is unknown")
	}
	committed = true
	response.AffectedRows = affected
	response.Result = resultDocument
	return response, nil
}

func protocolV2DatabaseRequestIdentity(ctx context.Context, request *protocolv2.RequestContext) (*protocolv2.ExtensionIdentity, *protocolv2.ErrorDetail) {
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if !validProtocolV2QueryIdentity(identity) {
		return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME, "host.database_runtime_stale", "The broker-attested exact runtime identity is missing or stale.", false)
	}
	if request == nil || !proto.Equal(request.GetExtension(), identity) {
		return identity, databaseError(protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME, "host.database_identity_mismatch", "The request identity does not match the broker-attested exact runtime.", false)
	}
	if request.GetActor() != nil {
		return identity, databaseError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.database_actor_unattested", "Plugin database calls cannot supply an unattested actor.", false)
	}
	return identity, nil
}

func protocolV2DatabaseResponseContext(request *protocolv2.RequestContext, identity *protocolv2.ExtensionIdentity) *protocolv2.ResponseContext {
	bound := &protocolv2.RequestContext{}
	if request != nil {
		bound.RequestId = request.GetRequestId()
		bound.Trace = request.GetTrace()
	}
	if identity != nil {
		bound.Extension = proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	}
	return protocolV2ResponseContext(bound)
}

func protocolV2DatabaseParameters(documents []*protocolv2.TypedDocument, definitions []ProtocolV2DatabaseParameter) ([]any, *protocolv2.ErrorDetail) {
	if len(documents) != len(definitions) || len(documents) > protocolV2DatabaseMaximumParameters {
		return nil, databaseInvalid("host.database_parameter_count", "The database parameters do not match the registered statement.")
	}
	result := make([]any, 0, len(documents))
	totalSize := 0
	for index, definition := range definitions {
		document := documents[index]
		if document == nil || document.GetValue() == nil || document.GetSchemaId() != definition.SchemaID || document.GetSchemaVersion() != definition.SchemaVersion {
			return nil, databaseInvalid("host.database_parameter_schema", "A database parameter does not match its registered schema.")
		}
		size := proto.Size(document)
		if size > protocolV2DatabaseMaximumParameterSize {
			return nil, databaseInvalid("host.database_parameter_too_large", "A database parameter exceeds the allowed size.")
		}
		totalSize += size
		if totalSize > protocolV2DatabaseMaximumRequestSize {
			return nil, databaseInvalid("host.database_request_too_large", "The database parameter set exceeds the allowed size.")
		}
		values := document.GetValue().AsMap()
		value, exists := values[definition.Field]
		if !exists || len(values) != 1 {
			return nil, databaseInvalid("host.database_parameter_shape", "A database parameter has an unexpected field shape.")
		}
		normalized, ok := protocolV2DatabaseParameterValue(value, definition)
		if !ok {
			return nil, databaseInvalid("host.database_parameter_value", "A database parameter has an invalid value.")
		}
		result = append(result, normalized)
	}
	return result, nil
}

func protocolV2DatabaseParameterValue(value any, definition ProtocolV2DatabaseParameter) (any, bool) {
	if value == nil {
		return nil, definition.Nullable
	}
	switch definition.Kind {
	case ProtocolV2DatabaseString:
		text, ok := value.(string)
		limit := definition.MaxBytes
		if limit <= 0 || limit > protocolV2DatabaseMaximumParameterSize {
			limit = protocolV2DatabaseMaximumParameterSize
		}
		return text, ok && len(text) <= limit && !strings.ContainsRune(text, '\x00')
	case ProtocolV2DatabaseInt64:
		text, ok := value.(string)
		parsed, err := strconv.ParseInt(text, 10, 64)
		return parsed, ok && err == nil && strconv.FormatInt(parsed, 10) == text
	case ProtocolV2DatabaseNumber:
		number, ok := value.(float64)
		return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
	case ProtocolV2DatabaseBool:
		boolean, ok := value.(bool)
		return boolean, ok
	default:
		return nil, false
	}
}

func protocolV2DatabaseRows(rows []map[string]any, schemaID, schemaVersion string, columns []ProtocolV2DatabaseColumn) ([]*protocolv2.TypedDocument, *protocolv2.ErrorDetail) {
	result := make([]*protocolv2.TypedDocument, 0, len(rows))
	totalSize := 0
	for _, row := range rows {
		if len(row) != len(columns) {
			return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_result_shape", "The database result did not match its registered columns.", false)
		}
		values := make(map[string]any, len(columns))
		for _, column := range columns {
			value, exists := row[column.Name]
			if !exists || value == nil && !column.Nullable {
				return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_result_shape", "The database result did not match its registered columns.", false)
			}
			normalized, err := normalizeProtocolV2QueryValue(value)
			if err != nil {
				return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_result_type", "The database result contained an unsupported value type.", false)
			}
			values[column.Name] = normalized
		}
		document, err := protocolV2Document(schemaID, schemaVersion, values)
		if err != nil {
			return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_result_encode", "The database result could not be encoded.", false)
		}
		size := proto.Size(document)
		if size > protocolV2DatabaseMaximumRowSize {
			return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE, "host.database_row_too_large", "A database result row exceeds the allowed size.", false)
		}
		totalSize += size
		if totalSize > protocolV2DatabaseMaximumResultSize {
			return nil, databaseError(protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE, "host.database_result_too_large", "The database result exceeds the allowed size.", false)
		}
		result = append(result, document)
	}
	return result, nil
}

type protocolV2DatabaseCursor struct {
	OperationID      string `json:"operationId"`
	StatementVersion string `json:"statementVersion"`
	RequestDigest    string `json:"requestDigest"`
	Offset           int    `json:"offset"`
}

func protocolV2DatabasePage(request *hostv2.DatabaseQueryRequest, definition ProtocolV2DatabaseQueryDefinition) (int, int, string, *protocolv2.ErrorDetail) {
	digest, err := protocolV2DatabaseQueryDigest(request)
	if err != nil {
		return 0, 0, "", databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.database_fingerprint_failed", "The database query could not be prepared.", false)
	}
	limit := int(request.GetPage().GetLimit())
	if limit == 0 {
		limit = min(protocolV2DatabaseDefaultRows, definition.MaxRows)
	}
	if limit <= 0 || limit > definition.MaxRows {
		return 0, 0, "", databaseInvalid("host.database_page_limit", "The database page limit exceeds the registered maximum.")
	}
	offset := 0
	if cursor := request.GetPage().GetCursor(); cursor != "" {
		decoded, decodeErr := decodeProtocolV2DatabaseCursor(cursor)
		if decodeErr != nil || decoded.OperationID != definition.OperationID || decoded.StatementVersion != definition.StatementVersion || decoded.RequestDigest != digest || decoded.Offset < 0 || decoded.Offset > 1_000_000 {
			return 0, 0, "", databaseInvalid("host.database_cursor_invalid", "The database cursor is invalid for this query.")
		}
		offset = decoded.Offset
	}
	return limit, offset, digest, nil
}

func protocolV2DatabaseQueryDigest(request *hostv2.DatabaseQueryRequest) (string, error) {
	bound := &hostv2.DatabaseQueryRequest{
		OperationId: request.GetOperationId(), StatementVersion: request.GetStatementVersion(),
		Parameters: cloneProtocolV2DatabaseDocuments(request.GetParameters()), StableCoreView: request.GetStableCoreView(),
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(bound)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func protocolV2DatabaseExecuteFingerprint(request *hostv2.DatabaseExecuteRequest, identity *protocolv2.ExtensionIdentity) (string, error) {
	// Epoch and instance are broker admission details. Durable replay remains
	// bound to the exact artifact and trust grant across process restarts.
	stableIdentity := &protocolv2.ExtensionIdentity{
		ExtensionId: identity.GetExtensionId(), ExtensionVersion: identity.GetExtensionVersion(),
		ArtifactDigest: identity.GetArtifactDigest(), TrustGrantId: identity.GetTrustGrantId(),
	}
	bound := &hostv2.DatabaseExecuteRequest{
		Context:     &protocolv2.RequestContext{Extension: stableIdentity},
		OperationId: request.GetOperationId(), StatementVersion: request.GetStatementVersion(),
		Parameters: cloneProtocolV2DatabaseDocuments(request.GetParameters()),
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(bound)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func protocolV2DatabaseIdempotencyKey(request *hostv2.DatabaseExecuteRequest) (string, *protocolv2.ErrorDetail) {
	requestKey := request.GetIdempotencyKey()
	contextKey := request.GetContext().GetIdempotencyKey()
	if requestKey != "" && contextKey != "" && requestKey != contextKey {
		return "", databaseInvalid("host.database_idempotency_mismatch", "The request and context idempotency keys do not match.")
	}
	key := requestKey
	if key == "" {
		key = contextKey
	}
	if key == "" {
		return "", databaseInvalid("host.database_idempotency_required", "An idempotency key is required for database writes.")
	}
	if !validProtocolV2CommandIdempotencyKey(key) {
		return "", databaseInvalid("host.database_idempotency_invalid", "The idempotency key must contain 1 to 128 visible ASCII characters.")
	}
	return key, nil
}

func encodeProtocolV2DatabaseCursor(cursor protocolV2DatabaseCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeProtocolV2DatabaseCursor(value string) (protocolV2DatabaseCursor, error) {
	if value == "" || len(value) > 1024 {
		return protocolV2DatabaseCursor{}, errors.New("invalid database cursor")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return protocolV2DatabaseCursor{}, err
	}
	var cursor protocolV2DatabaseCursor
	if err := json.Unmarshal(encoded, &cursor); err != nil {
		return protocolV2DatabaseCursor{}, err
	}
	return cursor, nil
}

func validateProtocolV2DatabaseQueryDefinition(definition ProtocolV2DatabaseQueryDefinition) error {
	if err := validateProtocolV2DatabaseStatement(
		definition.ExtensionID, definition.ExtensionVersion, definition.PackageDigest,
		definition.OperationID, definition.StatementVersion, definition.SQL,
		definition.Parameters, definition.Columns, definition.Timeout,
	); err != nil {
		return err
	}
	if definition.Scope != ProtocolV2DatabaseOwnSchema {
		return errors.New("hostapi: DatabaseService queries are restricted to own_schema; use HostQuery for stable core views")
	}
	normalizedSQL := strings.ToUpper(strings.TrimSpace(definition.SQL))
	if !strings.HasPrefix(normalizedSQL, "SELECT ") && !strings.HasPrefix(normalizedSQL, "WITH ") {
		return errors.New("hostapi: database query must be a read statement")
	}
	if definition.ResultSchemaID == "" || definition.ResultSchemaVersion == "" || len(definition.Columns) == 0 || definition.MaxRows <= 0 || definition.MaxRows > protocolV2DatabaseMaximumRows {
		return errors.New("hostapi: database query result schema, columns, and bounded rows are required")
	}
	return nil
}

func validateProtocolV2DatabaseExecuteDefinition(definition ProtocolV2DatabaseExecuteDefinition) error {
	if err := validateProtocolV2DatabaseStatement(
		definition.ExtensionID, definition.ExtensionVersion, definition.PackageDigest,
		definition.OperationID, definition.StatementVersion, definition.SQL,
		definition.Parameters, definition.ReturningColumns, definition.Timeout,
	); err != nil {
		return err
	}
	normalizedSQL := strings.ToUpper(strings.TrimSpace(definition.SQL))
	if !strings.HasPrefix(normalizedSQL, "INSERT ") && !strings.HasPrefix(normalizedSQL, "UPDATE ") &&
		!strings.HasPrefix(normalizedSQL, "DELETE ") && !strings.HasPrefix(normalizedSQL, "MERGE ") &&
		!strings.HasPrefix(normalizedSQL, "WITH ") {
		return errors.New("hostapi: database execute must be a registered data mutation")
	}
	if definition.MaxAffectedRows == 0 || definition.MaxAffectedRows > protocolV2DatabaseMaximumAffectedRows {
		return errors.New("hostapi: database execute affected-row limit is required")
	}
	if len(definition.ReturningColumns) > 0 && (definition.ResultSchemaID == "" || definition.ResultSchemaVersion == "") || len(definition.ReturningColumns) == 0 && (definition.ResultSchemaID != "" || definition.ResultSchemaVersion != "") {
		return errors.New("hostapi: database execute result schema and returning columns must be declared together")
	}
	canonicalTags, err := queryregistry.CanonicalSemanticCacheTags(definition.ExtensionID, definition.QueryInvalidationTags)
	if len(definition.QueryInvalidationTags) > 0 && (err != nil || !slices.Equal(canonicalTags, definition.QueryInvalidationTags)) {
		return errors.New("hostapi: database execute Query invalidation tags must be canonical and owner-scoped")
	}
	return nil
}

func validateProtocolV2DatabaseStatement(
	extensionID, extensionVersion, packageDigest string,
	operationID, version, sql string,
	parameters []ProtocolV2DatabaseParameter,
	columns []ProtocolV2DatabaseColumn,
	timeout time.Duration,
) error {
	if !protocolV2DatabaseOperationPattern.MatchString(extensionID) || !protocolV2DatabaseVersionPattern.MatchString(extensionVersion) ||
		!protocolV2DatabaseDigestPattern.MatchString(packageDigest) {
		return errors.New("hostapi: exact database catalog artifact identity is required")
	}
	if !protocolV2DatabaseOperationPattern.MatchString(operationID) || version == "" || strings.Trim(version, "0123456789") != "" || version[0] == '0' {
		return errors.New("hostapi: database operation id and positive statement version are required")
	}
	if !strings.HasPrefix(operationID, extensionID+".") {
		return errors.New("hostapi: database operation id must be namespaced to its exact extension")
	}
	if sql != strings.TrimSpace(sql) || sql == "" || len(sql) > protocolV2DatabaseMaximumSQLSize || strings.ContainsAny(sql, ";\x00") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return errors.New("hostapi: database SQL must be one bounded Host-owned statement")
	}
	if len(parameters) > protocolV2DatabaseMaximumParameters || len(columns) > protocolV2DatabaseMaximumColumns || timeout < 0 || timeout > protocolV2DatabaseMaximumTimeout {
		return errors.New("hostapi: database statement limits are invalid")
	}
	seenPlaceholders := make(map[int]struct{}, len(parameters))
	for _, match := range protocolV2DatabasePlaceholderPattern.FindAllStringSubmatch(sql, -1) {
		position, _ := strconv.Atoi(match[1])
		seenPlaceholders[position] = struct{}{}
	}
	if len(seenPlaceholders) != len(parameters) {
		return errors.New("hostapi: database SQL placeholders do not match parameters")
	}
	for position := 1; position <= len(parameters); position++ {
		if _, exists := seenPlaceholders[position]; !exists {
			return errors.New("hostapi: database SQL placeholders must be contiguous")
		}
	}
	for _, parameter := range parameters {
		if parameter.SchemaID == "" || parameter.SchemaVersion == "" || !protocolV2DatabaseOperationPattern.MatchString(parameter.Field) || parameter.MaxBytes < 0 || parameter.MaxBytes > protocolV2DatabaseMaximumParameterSize {
			return errors.New("hostapi: database parameter declaration is invalid")
		}
		switch parameter.Kind {
		case ProtocolV2DatabaseString, ProtocolV2DatabaseInt64, ProtocolV2DatabaseNumber, ProtocolV2DatabaseBool:
		default:
			return errors.New("hostapi: database parameter kind is invalid")
		}
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if !protocolV2DatabaseOperationPattern.MatchString(column.Name) {
			return errors.New("hostapi: database result column is invalid")
		}
		if _, exists := seenColumns[column.Name]; exists {
			return errors.New("hostapi: duplicate database result column")
		}
		seenColumns[column.Name] = struct{}{}
	}
	return nil
}

func cloneProtocolV2DatabaseQueryDefinition(source ProtocolV2DatabaseQueryDefinition) ProtocolV2DatabaseQueryDefinition {
	source.Parameters = append([]ProtocolV2DatabaseParameter(nil), source.Parameters...)
	source.Columns = append([]ProtocolV2DatabaseColumn(nil), source.Columns...)
	return source
}

func cloneProtocolV2DatabaseExecuteDefinition(source ProtocolV2DatabaseExecuteDefinition) ProtocolV2DatabaseExecuteDefinition {
	source.Parameters = append([]ProtocolV2DatabaseParameter(nil), source.Parameters...)
	source.ReturningColumns = append([]ProtocolV2DatabaseColumn(nil), source.ReturningColumns...)
	source.QueryInvalidationTags = append([]string(nil), source.QueryInvalidationTags...)
	return source
}

func cloneProtocolV2DatabaseDocuments(source []*protocolv2.TypedDocument) []*protocolv2.TypedDocument {
	result := make([]*protocolv2.TypedDocument, 0, len(source))
	for _, document := range source {
		if document == nil {
			result = append(result, nil)
		} else {
			result = append(result, proto.Clone(document).(*protocolv2.TypedDocument))
		}
	}
	return result
}

func databaseTimeout(value time.Duration) time.Duration {
	if value <= 0 || value > protocolV2DatabaseMaximumTimeout {
		return protocolV2DatabaseDefaultTimeout
	}
	return value
}

func rollbackProtocolV2DatabaseTx(parent context.Context, tx protocolV2DatabaseTx) error {
	if tx == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), protocolV2DatabaseRollbackTimeout)
	defer cancel()
	return tx.Rollback(ctx)
}

type protocolV2DatabaseError struct {
	detail *protocolv2.ErrorDetail
}

func (e *protocolV2DatabaseError) Error() string {
	if e == nil || e.detail == nil {
		return "Host database operation failed"
	}
	return e.detail.GetMessage()
}

func newProtocolV2DatabaseError(code protocolv2.ErrorCode, reason, message string, retryable bool) error {
	return &protocolV2DatabaseError{detail: databaseError(code, reason, message, retryable)}
}

func databaseExecutionError(err error, ctx context.Context, fallbackReason string) *protocolv2.ErrorDetail {
	var databaseErr *protocolV2DatabaseError
	if errors.As(err, &databaseErr) && databaseErr.detail != nil {
		return proto.Clone(databaseErr.detail).(*protocolv2.ErrorDetail)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return databaseError(protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, "host.database_deadline_exceeded", "The database operation exceeded its deadline.", true)
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return databaseError(protocolv2.ErrorCode_ERROR_CODE_CANCELLED, "host.database_cancelled", "The database operation was cancelled.", true)
	default:
		if strings.Contains(fallbackReason, "unavailable") {
			return databaseError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, fallbackReason, "The Host database operation is unavailable.", true)
		}
		return databaseError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, fallbackReason, "The Host database operation failed.", false)
	}
}

func databaseInvalid(reason, message string) *protocolv2.ErrorDetail {
	return databaseError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, reason, message, false)
}

func databaseError(code protocolv2.ErrorCode, reason, message string, retryable bool) *protocolv2.ErrorDetail {
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Retryable: retryable}
}

func unavailableProtocolV2DatabaseQuery(request *hostv2.DatabaseQueryRequest) *hostv2.DatabaseQueryResponse {
	return &hostv2.DatabaseQueryResponse{
		Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{},
		Error: databaseError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.database_backend_unavailable", "The Host database service is not configured.", true),
	}
}

func unavailableProtocolV2DatabaseExecute(request *hostv2.DatabaseExecuteRequest) *hostv2.DatabaseExecuteResponse {
	return &hostv2.DatabaseExecuteResponse{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   databaseError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.database_backend_unavailable", "The Host database service is not configured.", true),
	}
}
