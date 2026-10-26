package hostapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type recordingQueryTraceSink struct {
	mu     sync.Mutex
	traces []QueryTrace
}

func (s *recordingQueryTraceSink) RecordQueryTrace(trace QueryTrace) {
	s.mu.Lock()
	s.traces = append(s.traces, trace)
	s.mu.Unlock()
}

func (s *recordingQueryTraceSink) single(t *testing.T) QueryTrace {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.traces) != 1 {
		t.Fatalf("query traces = %#v", s.traces)
	}
	return s.traces[0]
}

func TestProtocolV2QueryTraceClassifiesEveryTerminalOutcome(t *testing.T) {
	tests := []struct {
		name      string
		resolver  ProtocolV2QueryAuthorityResolver
		executor  protocolV2QueryExecutor
		context   func() (context.Context, context.CancelFunc)
		outcome   QueryTraceOutcome
		errorCode protocolv2.ErrorCode
		rows      int
		hasShape  bool
	}{
		{
			name: "allowed", resolver: allowedQueryAuthority(),
			executor: protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
				return []map[string]any{{"id": "1", "title": "visible"}}, nil
			}),
			outcome: QueryTraceAllowed, rows: 1, hasShape: true,
		},
		{
			name: "denied",
			resolver: protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
				return ProtocolV2QueryAuthority{ExactArtifact: true}, nil
			}),
			executor: forbiddenQueryExecutor(t), outcome: QueryTraceDenied,
			errorCode: protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
		},
		{
			name: "stale",
			resolver: protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
				return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
			}),
			executor: forbiddenQueryExecutor(t), outcome: QueryTraceStale,
			errorCode: protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
		},
		{
			name: "error", resolver: allowedQueryAuthority(),
			executor: protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
				return nil, errors.New("storage failed")
			}),
			outcome: QueryTraceError, errorCode: protocolv2.ErrorCode_ERROR_CODE_INTERNAL, hasShape: true,
		},
		{
			name: "cancel", resolver: allowedQueryAuthority(),
			executor: protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
				return nil, context.Canceled
			}),
			outcome: QueryTraceCancel, errorCode: protocolv2.ErrorCode_ERROR_CODE_CANCELLED, hasShape: true,
		},
		{
			name: "deadline", resolver: allowedQueryAuthority(),
			executor: protocolV2QueryExecutorFunc(func(ctx context.Context, _ protocolV2QueryPlan) ([]map[string]any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}),
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			outcome: QueryTraceDeadline, errorCode: protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, hasShape: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingQueryTraceSink{}
			engine := testProtocolV2QueryEngine(t, test.executor, test.resolver)
			engine.traceSink = sink
			ctx := context.Background()
			cancel := func() {}
			if test.context != nil {
				ctx, cancel = test.context()
			}
			defer cancel()
			ctx = ContextWithProtocolV2RuntimeIdentity(ctx, testProtocolV2QueryIdentity())
			response := engine.execute(ctx, testProtocolV2TopicsQuery(t))
			if test.errorCode == protocolv2.ErrorCode_ERROR_CODE_UNSPECIFIED {
				if response.GetError() != nil {
					t.Fatalf("response error = %#v", response.GetError())
				}
			} else if response.GetError().GetCode() != test.errorCode {
				t.Fatalf("response error = %#v", response.GetError())
			}
			trace := sink.single(t)
			if trace.Outcome != test.outcome || trace.Rows != test.rows || trace.Duration < 0 {
				t.Fatalf("trace = %#v", trace)
			}
			if (trace.ShapeDigest != "") != test.hasShape {
				t.Fatalf("shape digest = %q", trace.ShapeDigest)
			}
			if trace.ExtensionID != "query.plugin" || trace.ExtensionVersion != "1.2.3" ||
				trace.ArtifactDigest != strings.Repeat("a", 64) || trace.QueryID != QueryPublicTopicsList ||
				trace.PlanVersion != QueryStableCorePlanVersion {
				t.Fatalf("trace identity = %#v", trace)
			}
		})
	}
}

func TestProtocolV2QueryTraceUsesBoundedHostSlowThreshold(t *testing.T) {
	sink := &recordingQueryTraceSink{}
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		time.Sleep(15 * time.Millisecond)
		return nil, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, allowedQueryAuthority())
	engine.traceSink = sink
	engine.slowThreshold = 5 * time.Millisecond
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity())
	if response := engine.execute(ctx, testProtocolV2TopicsQuery(t)); response.GetError() != nil {
		t.Fatalf("response = %#v", response)
	}
	trace := sink.single(t)
	if !trace.Slow || trace.Duration < 5*time.Millisecond {
		t.Fatalf("slow trace = %#v", trace)
	}
	if ProtocolV2QueryDefaultSlowThreshold > time.Second {
		t.Fatalf("default slow threshold = %s", ProtocolV2QueryDefaultSlowThreshold)
	}
}

func TestProtocolV2QueryTraceClassifiesAuthorityCancellation(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome QueryTraceOutcome
		code    protocolv2.ErrorCode
	}{
		{name: "cancel", err: context.Canceled, outcome: QueryTraceCancel, code: protocolv2.ErrorCode_ERROR_CODE_CANCELLED},
		{name: "deadline", err: context.DeadlineExceeded, outcome: QueryTraceDeadline, code: protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingQueryTraceSink{}
			resolver := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
				return ProtocolV2QueryAuthority{}, test.err
			})
			engine := testProtocolV2QueryEngine(t, forbiddenQueryExecutor(t), resolver)
			engine.traceSink = sink
			ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity())
			response := engine.execute(ctx, testProtocolV2TopicsQuery(t))
			if response.GetError().GetCode() != test.code || sink.single(t).Outcome != test.outcome {
				t.Fatalf("response=%#v trace=%#v", response, sink.single(t))
			}
		})
	}
}

func TestProtocolV2QueryTraceIsBoundedAndSlogExcludesPayloads(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	engine := testProtocolV2QueryEngine(t,
		protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
			return []map[string]any{{"id": "1", "original_name": "result-secret-marker"}}, nil
		}),
		allowedQueryAuthority(),
	)
	engine.traceSink = NewSlogQueryTraceSink(logger)
	request := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicAttachmentByPublicID,
		PlanVersion: QueryStableCorePlanVersion,
	}
	request.Filters = []*hostv2.QueryFilter{{
		Field: "public_id", Operator: "eq",
		Value: queryParameter(t, QueryTextParameterSchemaID, "parameter-secret-marker"),
	}}
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity())
	if response := engine.execute(ctx, request); response.GetError() != nil {
		t.Fatalf("response = %#v", response)
	}
	logged := output.String()
	for _, required := range []string{"extension_id", "artifact_digest", "query_id", "shape_digest", "duration", "rows", "outcome", "slow"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("structured trace missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"parameter-secret-marker", "result-secret-marker", "SELECT", "password"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("structured trace leaked %q: %s", forbidden, logged)
		}
	}

	bounded := boundedQueryTrace(QueryTrace{
		ExtensionID: strings.Repeat("e", 1000), QueryID: strings.Repeat("q", 1000),
		Rows: 1_000_000, Duration: -time.Second,
	})
	if len(bounded.ExtensionID) != protocolV2QueryTraceIdentityLimit ||
		len(bounded.QueryID) != protocolV2QueryTraceIdentityLimit ||
		bounded.Rows != protocolV2QueryMaximumLimit || bounded.Duration != 0 {
		t.Fatalf("bounded trace = %#v", bounded)
	}
}

func TestProtocolV2QueryTraceShapeExcludesParameterValues(t *testing.T) {
	definition := stableCoreProtocolV2QueryDefinitions()[1]
	request := testProtocolV2TopicsQuery(t)
	request.Filters = []*hostv2.QueryFilter{{
		Field: "category_id", Operator: "eq",
		Value: queryParameter(t, QueryInt64ParameterSchemaID, "1"),
	}}
	first, detail := buildProtocolV2QueryPlan(definition, request)
	if detail != nil {
		t.Fatal(detail)
	}
	request.Filters[0].Value = queryParameter(t, QueryInt64ParameterSchemaID, "2")
	second, detail := buildProtocolV2QueryPlan(definition, request)
	if detail != nil {
		t.Fatal(detail)
	}
	if first.ShapeDigest == second.ShapeDigest {
		t.Fatal("cursor shape must remain parameter-bound")
	}
	if protocolV2QueryTraceShapeDigest(first) != protocolV2QueryTraceShapeDigest(second) {
		t.Fatal("trace shape digest leaked parameter identity")
	}
}

func allowedQueryAuthority() ProtocolV2QueryAuthorityResolver {
	return protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
}

func forbiddenQueryExecutor(t *testing.T) protocolV2QueryExecutor {
	t.Helper()
	return protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("query executor must not run")
		return nil, nil
	})
}
