package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

const (
	ProtocolV2QueryDefaultSlowThreshold = time.Second
	protocolV2QueryTraceIdentityLimit   = 255
	protocolV2QueryTraceVersionLimit    = 128
	protocolV2QueryTraceDigestLimit     = 128
)

type QueryTraceOutcome string

const (
	QueryTraceAllowed  QueryTraceOutcome = "allowed"
	QueryTraceDenied   QueryTraceOutcome = "denied"
	QueryTraceStale    QueryTraceOutcome = "stale"
	QueryTraceError    QueryTraceOutcome = "error"
	QueryTraceCancel   QueryTraceOutcome = "cancel"
	QueryTraceDeadline QueryTraceOutcome = "deadline"
)

// QueryTrace deliberately excludes request parameters, result documents, SQL,
// credentials, and error text. Sinks receive only bounded Host-owned metadata.
type QueryTrace struct {
	ExtensionID      string
	ExtensionVersion string
	ArtifactDigest   string
	QueryID          string
	PlanVersion      string
	ShapeDigest      string
	Duration         time.Duration
	Rows             int
	Outcome          QueryTraceOutcome
	Slow             bool
}

type QueryTraceSink interface {
	RecordQueryTrace(QueryTrace)
}

type protocolV2QueryRuntimeOptions struct {
	traceSink QueryTraceSink
	// trustLevels 供两条信任等级计算查询（M4b-3）使用；nil 时它们返回 unavailable。
	trustLevels TrustLevelQuerySource
}

type ProtocolV2QueryRuntimeOption interface {
	applyProtocolV2QueryRuntime(*protocolV2QueryRuntimeOptions)
}

type protocolV2QueryRuntimeOptionFunc func(*protocolV2QueryRuntimeOptions)

func (f protocolV2QueryRuntimeOptionFunc) applyProtocolV2QueryRuntime(options *protocolV2QueryRuntimeOptions) {
	f(options)
}

func WithProtocolV2QueryTraceSink(sink QueryTraceSink) ProtocolV2QueryRuntimeOption {
	return protocolV2QueryRuntimeOptionFunc(func(options *protocolV2QueryRuntimeOptions) {
		options.traceSink = sink
	})
}

type slogQueryTraceSink struct {
	logger *slog.Logger
}

func NewSlogQueryTraceSink(logger *slog.Logger) QueryTraceSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &slogQueryTraceSink{logger: logger}
}

func (s *slogQueryTraceSink) RecordQueryTrace(trace QueryTrace) {
	if s == nil || s.logger == nil {
		return
	}
	level := slog.LevelInfo
	if trace.Slow || trace.Outcome != QueryTraceAllowed {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(context.Background(), level, "Host Query trace",
		slog.String("extension_id", trace.ExtensionID),
		slog.String("extension_version", trace.ExtensionVersion),
		slog.String("artifact_digest", trace.ArtifactDigest),
		slog.String("query_id", trace.QueryID),
		slog.String("plan_version", trace.PlanVersion),
		slog.String("shape_digest", trace.ShapeDigest),
		slog.Duration("duration", trace.Duration),
		slog.Int("rows", trace.Rows),
		slog.String("outcome", string(trace.Outcome)),
		slog.Bool("slow", trace.Slow),
	)
}

func boundedQueryTrace(trace QueryTrace) QueryTrace {
	trace.ExtensionID = boundedQueryTraceString(trace.ExtensionID, protocolV2QueryTraceIdentityLimit)
	trace.ExtensionVersion = boundedQueryTraceString(trace.ExtensionVersion, protocolV2QueryTraceVersionLimit)
	trace.ArtifactDigest = boundedQueryTraceString(trace.ArtifactDigest, protocolV2QueryTraceDigestLimit)
	trace.QueryID = boundedQueryTraceString(trace.QueryID, protocolV2QueryTraceIdentityLimit)
	trace.PlanVersion = boundedQueryTraceString(trace.PlanVersion, protocolV2QueryTraceVersionLimit)
	trace.ShapeDigest = boundedQueryTraceString(trace.ShapeDigest, protocolV2QueryTraceDigestLimit)
	if trace.Duration < 0 {
		trace.Duration = 0
	}
	if trace.Rows < 0 {
		trace.Rows = 0
	} else if trace.Rows > protocolV2QueryMaximumLimit {
		trace.Rows = protocolV2QueryMaximumLimit
	}
	return trace
}

func boundedQueryTraceString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func protocolV2QueryTraceShapeDigest(plan protocolV2QueryPlan) string {
	type traceFilter struct {
		Field    string `json:"field"`
		Operator string `json:"operator"`
		SchemaID string `json:"schemaId"`
		Kind     string `json:"kind"`
	}
	type traceSort struct {
		Field      string `json:"field"`
		Descending bool   `json:"descending"`
	}
	document := struct {
		QueryID     string        `json:"queryId"`
		PlanVersion string        `json:"planVersion"`
		Fields      []string      `json:"fields"`
		Filters     []traceFilter `json:"filters"`
		Sorts       []traceSort   `json:"sorts"`
	}{
		QueryID: plan.Definition.ID, PlanVersion: plan.Definition.PlanVersion,
		Fields:  make([]string, 0, len(plan.Fields)),
		Filters: make([]traceFilter, 0, len(plan.Filters)),
		Sorts:   make([]traceSort, 0, len(plan.Sorts)),
	}
	for _, field := range plan.Fields {
		document.Fields = append(document.Fields, field.Name)
	}
	for _, filter := range plan.Filters {
		document.Filters = append(document.Filters, traceFilter{
			Field: filter.Definition.Field, Operator: filter.Definition.Operator,
			SchemaID: filter.Definition.SchemaID, Kind: filter.Definition.Kind,
		})
	}
	for _, sort := range plan.Sorts {
		document.Sorts = append(document.Sorts, traceSort{Field: sort.Field, Descending: sort.Descending})
	}
	encoded, _ := json.Marshal(document)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
