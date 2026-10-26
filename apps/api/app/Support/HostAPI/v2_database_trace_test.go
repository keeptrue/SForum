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

type recordingDatabaseTraceSink struct {
	mu     sync.Mutex
	traces []DatabaseTrace
}

func (s *recordingDatabaseTraceSink) RecordDatabaseTrace(trace DatabaseTrace) {
	s.mu.Lock()
	s.traces = append(s.traces, trace)
	s.mu.Unlock()
}

func (s *recordingDatabaseTraceSink) snapshot() []DatabaseTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DatabaseTrace(nil), s.traces...)
}

func (s *recordingDatabaseTraceSink) single(t *testing.T) DatabaseTrace {
	t.Helper()
	traces := s.snapshot()
	if len(traces) != 1 {
		t.Fatalf("database traces = %#v", traces)
	}
	return traces[0]
}

func TestProtocolV2DatabaseQueryTraceCoversAllowedDeniedAndFailure(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2DatabaseTx)
		mutate    func(*hostv2.DatabaseQueryRequest)
		outcome   DatabaseTraceOutcome
		rows      int
		hasShape  bool
	}{
		{
			name: "allowed", outcome: DatabaseTraceAllowed, rows: 1, hasShape: true,
			configure: func(tx *fakeProtocolV2DatabaseTx) {
				tx.queryRows = []map[string]any{{"id": int64(1), "name": "result-secret"}}
			},
		},
		{
			name: "denied", outcome: DatabaseTraceDenied,
			mutate: func(request *hostv2.DatabaseQueryRequest) {
				request.Context.Actor = &protocolv2.Actor{UserId: 42}
			},
		},
		{
			name: "failure", outcome: DatabaseTraceError, hasShape: true,
			configure: func(tx *fakeProtocolV2DatabaseTx) {
				tx.queryErr = errors.New("storage-error-secret")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2DatabaseBackend()
			if test.configure != nil {
				test.configure(backend.tx)
			}
			sink := &recordingDatabaseTraceSink{}
			engine := databaseTraceTestEngine(t, backend, sink)
			identity := testProtocolV2DatabaseIdentity()
			request := databaseTraceQueryRequest(t, identity, "42")
			if test.mutate != nil {
				test.mutate(request)
			}
			response, err := engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
			if err != nil {
				t.Fatal(err)
			}
			if test.outcome == DatabaseTraceAllowed && response.GetError() != nil || test.outcome != DatabaseTraceAllowed && response.GetError() == nil {
				t.Fatalf("response = %#v", response)
			}
			trace := sink.single(t)
			assertDatabaseTraceIdentity(t, trace, identity, testDatabaseQueryOperation, "query")
			if trace.Outcome != test.outcome || trace.Rows != test.rows || trace.AffectedRows != 0 || trace.Duration < 0 {
				t.Fatalf("trace = %#v", trace)
			}
			if (trace.ShapeDigest != "") != test.hasShape {
				t.Fatalf("shape digest = %q", trace.ShapeDigest)
			}
		})
	}
}

func TestProtocolV2DatabaseExecuteTraceCoversAllowedDeniedAndFailure(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2DatabaseTx)
		mutate    func(*hostv2.DatabaseExecuteRequest)
		outcome   DatabaseTraceOutcome
		rows      int
		affected  uint64
		hasShape  bool
	}{
		{
			name: "allowed", outcome: DatabaseTraceAllowed, rows: 1, affected: 1, hasShape: true,
			configure: func(tx *fakeProtocolV2DatabaseTx) {
				tx.executeAffected = 1
				tx.executeRows = []map[string]any{{"id": int64(1), "name": "result-secret"}}
			},
		},
		{
			name: "denied", outcome: DatabaseTraceDenied,
			mutate: func(request *hostv2.DatabaseExecuteRequest) {
				request.Context.Actor = &protocolv2.Actor{UserId: 42}
			},
		},
		{
			name: "failure", outcome: DatabaseTraceError, hasShape: true,
			configure: func(tx *fakeProtocolV2DatabaseTx) {
				tx.executeErr = errors.New("execute-error-secret")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2DatabaseBackend()
			if test.configure != nil {
				test.configure(backend.tx)
			}
			sink := &recordingDatabaseTraceSink{}
			engine := databaseTraceTestEngine(t, backend, sink)
			identity := testProtocolV2DatabaseIdentity()
			request := testProtocolV2DatabaseExecuteRequest(t, identity, "idempotency-secret", "parameter-secret")
			if test.mutate != nil {
				test.mutate(request)
			}
			response, err := engine.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
			if err != nil {
				t.Fatal(err)
			}
			if test.outcome == DatabaseTraceAllowed && response.GetError() != nil || test.outcome != DatabaseTraceAllowed && response.GetError() == nil {
				t.Fatalf("response = %#v", response)
			}
			trace := sink.single(t)
			assertDatabaseTraceIdentity(t, trace, identity, testDatabaseExecuteOperation, "execute")
			if trace.Outcome != test.outcome || trace.Rows != test.rows || trace.AffectedRows != test.affected || trace.Duration < 0 {
				t.Fatalf("trace = %#v", trace)
			}
			if (trace.ShapeDigest != "") != test.hasShape {
				t.Fatalf("shape digest = %q", trace.ShapeDigest)
			}
		})
	}
}

func TestProtocolV2DatabaseTraceClassifiesSlowCancellationAndDeadline(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		outcome DatabaseTraceOutcome
	}{
		{
			name: "cancel", outcome: DatabaseTraceCancel,
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
		},
		{
			name: "deadline", outcome: DatabaseTraceDeadline,
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Millisecond)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2DatabaseBackend()
			backend.tx.queryWait = true
			sink := &recordingDatabaseTraceSink{}
			engine := databaseTraceTestEngine(t, backend, sink)
			engine.slowThreshold = time.Nanosecond
			identity := testProtocolV2DatabaseIdentity()
			ctx, cancel := test.context()
			defer cancel()
			ctx = ContextWithProtocolV2RuntimeIdentity(ctx, identity)
			response, err := engine.query(ctx, databaseTraceQueryRequest(t, identity, "42"))
			if err != nil || response.GetError() == nil {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			trace := sink.single(t)
			if trace.Outcome != test.outcome || !trace.Slow || trace.Duration <= 0 {
				t.Fatalf("trace = %#v", trace)
			}
		})
	}
}

func TestProtocolV2DatabaseTraceIsBoundedAndNeverLogsSensitiveData(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.queryRows = []map[string]any{{"id": int64(1), "name": "result-value-secret"}}
	engine := databaseTraceTestEngine(t, backend, NewSlogDatabaseTraceSink(logger))
	identity := testProtocolV2DatabaseIdentity()
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	queryResponse, err := engine.query(ctx, databaseTraceQueryRequest(t, identity, "42"))
	if err != nil || queryResponse.GetError() != nil || len(queryResponse.GetRows()) != 1 {
		t.Fatalf("query response=%#v err=%v", queryResponse, err)
	}
	backend.tx.executeErr = errors.New("error-text-secret")
	request := testProtocolV2DatabaseExecuteRequest(t, identity, "idempotency-key-secret", "parameter-value-secret")
	response, err := engine.execute(ctx, request)
	if err != nil || response.GetError() == nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	logged := output.String()
	for _, required := range []string{
		"extension_id", "extension_version", "artifact_digest", "operation_id", "statement_version",
		"operation_kind", "shape_digest", "duration", "rows", "affected_rows", "outcome", "slow",
	} {
		if !strings.Contains(logged, required) {
			t.Fatalf("structured trace missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{
		"SELECT ", "INSERT INTO", "parameter-value-secret", "result-value-secret", "idempotency-key-secret",
		"error-text-secret", "runtime_role", "password",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("structured trace leaked %q: %s", forbidden, logged)
		}
	}

	bounded := boundedDatabaseTrace(DatabaseTrace{
		ExtensionID: strings.Repeat("e", 1000), OperationID: strings.Repeat("o", 1000),
		Rows: 1_000_000, AffectedRows: 1_000_000, Duration: -time.Second,
	})
	if len(bounded.ExtensionID) != protocolV2DatabaseTraceIdentityLimit ||
		len(bounded.OperationID) != protocolV2DatabaseTraceIdentityLimit ||
		bounded.Rows != protocolV2DatabaseMaximumRows ||
		bounded.AffectedRows != protocolV2DatabaseMaximumAffectedRows || bounded.Duration != 0 {
		t.Fatalf("bounded trace = %#v", bounded)
	}
}

func TestProtocolV2DatabaseTraceShapeDoesNotDependOnRequestValues(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.queryRows = []map[string]any{{"id": int64(1), "name": "one"}}
	sink := &recordingDatabaseTraceSink{}
	engine := databaseTraceTestEngine(t, backend, sink)
	identity := testProtocolV2DatabaseIdentity()
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	for _, value := range []string{"41", "42"} {
		response, err := engine.query(ctx, databaseTraceQueryRequest(t, identity, value))
		if err != nil || response.GetError() != nil {
			t.Fatalf("response=%#v err=%v", response, err)
		}
	}
	traces := sink.snapshot()
	if len(traces) != 2 || traces[0].ShapeDigest == "" || traces[0].ShapeDigest != traces[1].ShapeDigest {
		t.Fatalf("trace shapes = %#v", traces)
	}
}

func TestProtocolV2DatabaseExecuteTraceShapeBindsQueryInvalidationTags(t *testing.T) {
	_, executes := testProtocolV2DatabaseDefinitions()
	withoutTags := protocolV2DatabaseExecuteTraceShapeDigest(executes[0])
	executes[0].QueryInvalidationTags = []string{"fixture.database.items"}
	withTags := protocolV2DatabaseExecuteTraceShapeDigest(executes[0])
	if withoutTags == "" || withTags == "" || withoutTags == withTags {
		t.Fatalf("execute trace shape did not bind Query invalidation tags: without=%q with=%q", withoutTags, withTags)
	}
}

func databaseTraceTestEngine(t *testing.T, backend *fakeProtocolV2DatabaseBackend, sink DatabaseTraceSink) *protocolV2DatabaseEngine {
	t.Helper()
	queries, executes := testProtocolV2DatabaseDefinitions()
	engine, err := newProtocolV2DatabaseEngine(backend, queries, executes, WithProtocolV2DatabaseTraceSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func databaseTraceQueryRequest(t *testing.T, identity *protocolv2.ExtensionIdentity, value string) *hostv2.DatabaseQueryRequest {
	t.Helper()
	return &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters: []*protocolv2.TypedDocument{
			testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", value),
		},
	}
}

func assertDatabaseTraceIdentity(
	t *testing.T,
	trace DatabaseTrace,
	identity *protocolv2.ExtensionIdentity,
	operationID, kind string,
) {
	t.Helper()
	if trace.ExtensionID != identity.GetExtensionId() ||
		trace.ExtensionVersion != identity.GetExtensionVersion() ||
		trace.ArtifactDigest != identity.GetArtifactDigest() ||
		trace.OperationID != operationID || trace.StatementVersion != testDatabaseStatementVersion ||
		trace.OperationKind != kind {
		t.Fatalf("trace identity = %#v", trace)
	}
}
