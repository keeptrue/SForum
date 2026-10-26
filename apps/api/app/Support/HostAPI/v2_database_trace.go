package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	ProtocolV2DatabaseDefaultSlowThreshold = ProtocolV2QueryDefaultSlowThreshold
	protocolV2DatabaseTraceIdentityLimit   = 255
	protocolV2DatabaseTraceVersionLimit    = 128
	protocolV2DatabaseTraceDigestLimit     = 128
)

type DatabaseTraceOutcome = QueryTraceOutcome

const (
	DatabaseTraceAllowed  = QueryTraceAllowed
	DatabaseTraceDenied   = QueryTraceDenied
	DatabaseTraceStale    = QueryTraceStale
	DatabaseTraceError    = QueryTraceError
	DatabaseTraceCancel   = QueryTraceCancel
	DatabaseTraceDeadline = QueryTraceDeadline
)

// DatabaseTrace 只包含有界的宿主元数据。SQL、参数、结果、凭据、幂等键和错误文本
// 均不得进入 trace sink。
type DatabaseTrace struct {
	ExtensionID      string
	ExtensionVersion string
	ArtifactDigest   string
	OperationID      string
	StatementVersion string
	OperationKind    string
	ShapeDigest      string
	Duration         time.Duration
	Rows             int
	AffectedRows     uint64
	Outcome          DatabaseTraceOutcome
	Slow             bool
}

type DatabaseTraceSink interface {
	RecordDatabaseTrace(DatabaseTrace)
}

type protocolV2DatabaseRuntimeOptions struct {
	traceSink          DatabaseTraceSink
	queryInvalidations *supportjobs.Dispatcher
}

type ProtocolV2DatabaseRuntimeOption interface {
	applyProtocolV2DatabaseRuntime(*protocolV2DatabaseRuntimeOptions)
}

type protocolV2DatabaseRuntimeOptionFunc func(*protocolV2DatabaseRuntimeOptions)

func (f protocolV2DatabaseRuntimeOptionFunc) applyProtocolV2DatabaseRuntime(options *protocolV2DatabaseRuntimeOptions) {
	f(options)
}

func WithProtocolV2DatabaseTraceSink(sink DatabaseTraceSink) ProtocolV2DatabaseRuntimeOption {
	return protocolV2DatabaseRuntimeOptionFunc(func(options *protocolV2DatabaseRuntimeOptions) {
		options.traceSink = sink
	})
}

func WithProtocolV2DatabaseQueryInvalidationJobs(dispatcher *supportjobs.Dispatcher) ProtocolV2DatabaseRuntimeOption {
	return protocolV2DatabaseRuntimeOptionFunc(func(options *protocolV2DatabaseRuntimeOptions) {
		options.queryInvalidations = dispatcher
	})
}

func resolveProtocolV2DatabaseRuntimeOptions(options []ProtocolV2DatabaseRuntimeOption) protocolV2DatabaseRuntimeOptions {
	resolved := protocolV2DatabaseRuntimeOptions{}
	for _, option := range options {
		if option != nil {
			option.applyProtocolV2DatabaseRuntime(&resolved)
		}
	}
	return resolved
}

type slogDatabaseTraceSink struct {
	logger *slog.Logger
}

func NewSlogDatabaseTraceSink(logger *slog.Logger) DatabaseTraceSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &slogDatabaseTraceSink{logger: logger}
}

func (s *slogDatabaseTraceSink) RecordDatabaseTrace(trace DatabaseTrace) {
	if s == nil || s.logger == nil {
		return
	}
	level := slog.LevelInfo
	if trace.Slow || trace.Outcome != DatabaseTraceAllowed {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(context.Background(), level, "DatabaseService trace",
		slog.String("extension_id", trace.ExtensionID),
		slog.String("extension_version", trace.ExtensionVersion),
		slog.String("artifact_digest", trace.ArtifactDigest),
		slog.String("operation_id", trace.OperationID),
		slog.String("statement_version", trace.StatementVersion),
		slog.String("operation_kind", trace.OperationKind),
		slog.String("shape_digest", trace.ShapeDigest),
		slog.Duration("duration", trace.Duration),
		slog.Int("rows", trace.Rows),
		slog.Uint64("affected_rows", trace.AffectedRows),
		slog.String("outcome", string(trace.Outcome)),
		slog.Bool("slow", trace.Slow),
	)
}

func boundedDatabaseTrace(trace DatabaseTrace) DatabaseTrace {
	trace.ExtensionID = boundedQueryTraceString(trace.ExtensionID, protocolV2DatabaseTraceIdentityLimit)
	trace.ExtensionVersion = boundedQueryTraceString(trace.ExtensionVersion, protocolV2DatabaseTraceVersionLimit)
	trace.ArtifactDigest = boundedQueryTraceString(trace.ArtifactDigest, protocolV2DatabaseTraceDigestLimit)
	trace.OperationID = boundedQueryTraceString(trace.OperationID, protocolV2DatabaseTraceIdentityLimit)
	trace.StatementVersion = boundedQueryTraceString(trace.StatementVersion, protocolV2DatabaseTraceVersionLimit)
	trace.OperationKind = boundedQueryTraceString(trace.OperationKind, 16)
	trace.ShapeDigest = boundedQueryTraceString(trace.ShapeDigest, protocolV2DatabaseTraceDigestLimit)
	if trace.Duration < 0 {
		trace.Duration = 0
	}
	if trace.Rows < 0 {
		trace.Rows = 0
	} else if trace.Rows > protocolV2DatabaseMaximumRows {
		trace.Rows = protocolV2DatabaseMaximumRows
	}
	if trace.AffectedRows > protocolV2DatabaseMaximumAffectedRows {
		trace.AffectedRows = protocolV2DatabaseMaximumAffectedRows
	}
	return trace
}

func newProtocolV2DatabaseTrace(ctx context.Context, operationID, statementVersion, kind string) DatabaseTrace {
	trace := DatabaseTrace{
		OperationID: strings.TrimSpace(operationID), StatementVersion: strings.TrimSpace(statementVersion),
		OperationKind: kind, Outcome: DatabaseTraceError,
	}
	if identity := ProtocolV2RuntimeIdentityFromContext(ctx); identity != nil {
		trace.ExtensionID = identity.GetExtensionId()
		trace.ExtensionVersion = identity.GetExtensionVersion()
		trace.ArtifactDigest = identity.GetArtifactDigest()
	}
	return trace
}

func (e *protocolV2DatabaseEngine) recordDatabaseTrace(startedAt time.Time, trace DatabaseTrace, detail *protocolv2.ErrorDetail, rpcErr error) {
	if e == nil || e.traceSink == nil {
		return
	}
	trace.Duration = time.Since(startedAt)
	trace.Outcome = protocolV2DatabaseTraceOutcome(detail, rpcErr)
	threshold := e.slowThreshold
	if threshold <= 0 || threshold > ProtocolV2DatabaseDefaultSlowThreshold {
		threshold = ProtocolV2DatabaseDefaultSlowThreshold
	}
	trace.Slow = trace.Duration >= threshold
	e.traceSink.RecordDatabaseTrace(boundedDatabaseTrace(trace))
}

func protocolV2DatabaseTraceOutcome(detail *protocolv2.ErrorDetail, rpcErr error) DatabaseTraceOutcome {
	if rpcErr != nil {
		switch {
		case errors.Is(rpcErr, context.DeadlineExceeded):
			return DatabaseTraceDeadline
		case errors.Is(rpcErr, context.Canceled):
			return DatabaseTraceCancel
		default:
			return DatabaseTraceError
		}
	}
	if detail == nil {
		return DatabaseTraceAllowed
	}
	switch detail.GetCode() {
	case protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		return DatabaseTraceDeadline
	case protocolv2.ErrorCode_ERROR_CODE_CANCELLED:
		return DatabaseTraceCancel
	case protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME:
		return DatabaseTraceStale
	case protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
		protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
		protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND,
		protocolv2.ErrorCode_ERROR_CODE_CONFLICT:
		return DatabaseTraceDenied
	case protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION:
		if strings.Contains(detail.GetReason(), "identity") || strings.Contains(detail.GetReason(), "runtime_stale") {
			return DatabaseTraceStale
		}
		return DatabaseTraceDenied
	default:
		return DatabaseTraceError
	}
}

func protocolV2DatabaseQueryTraceShapeDigest(definition ProtocolV2DatabaseQueryDefinition) string {
	return protocolV2DatabaseTraceShapeDigest(
		"query", definition.OperationID, definition.StatementVersion, definition.SQL,
		definition.Parameters, definition.ResultSchemaID, definition.ResultSchemaVersion,
		definition.Columns, definition.MaxRows, 0, nil, definition.Timeout,
	)
}

func protocolV2DatabaseExecuteTraceShapeDigest(definition ProtocolV2DatabaseExecuteDefinition) string {
	return protocolV2DatabaseTraceShapeDigest(
		"execute", definition.OperationID, definition.StatementVersion, definition.SQL,
		definition.Parameters, definition.ResultSchemaID, definition.ResultSchemaVersion,
		definition.ReturningColumns, 0, definition.MaxAffectedRows, definition.QueryInvalidationTags, definition.Timeout,
	)
}

func protocolV2DatabaseTraceShapeDigest(
	kind, operationID, statementVersion, sql string,
	parameters []ProtocolV2DatabaseParameter,
	resultSchemaID, resultSchemaVersion string,
	columns []ProtocolV2DatabaseColumn,
	maxRows int,
	maxAffectedRows uint64,
	queryInvalidationTags []string,
	timeout time.Duration,
) string {
	statementDigest := sha256.Sum256([]byte(sql))
	document := struct {
		Kind                  string
		OperationID           string
		StatementVersion      string
		StatementDigest       string
		Parameters            []ProtocolV2DatabaseParameter
		ResultSchemaID        string
		ResultSchemaVersion   string
		Columns               []ProtocolV2DatabaseColumn
		MaxRows               int
		MaxAffectedRows       uint64
		QueryInvalidationTags []string
		TimeoutMS             int64
	}{
		Kind: kind, OperationID: operationID, StatementVersion: statementVersion,
		StatementDigest: hex.EncodeToString(statementDigest[:]), Parameters: parameters,
		ResultSchemaID: resultSchemaID, ResultSchemaVersion: resultSchemaVersion,
		Columns: columns, MaxRows: maxRows, MaxAffectedRows: maxAffectedRows,
		QueryInvalidationTags: queryInvalidationTags,
		TimeoutMS:             timeout.Milliseconds(),
	}
	encoded, _ := json.Marshal(document)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
