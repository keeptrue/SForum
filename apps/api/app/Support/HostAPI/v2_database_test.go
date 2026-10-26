package hostapi

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	testDatabaseQueryOperation   = "fixture.database.items.list"
	testDatabaseExecuteOperation = "fixture.database.items.create"
	testDatabaseStatementVersion = "1"
	testDatabaseParameterSchema  = "fixture.database.parameter"
	testDatabaseResultSchema     = "fixture.database.result"
	testDatabaseExtensionID      = "fixture.database"
	testDatabaseExtensionVersion = "1.0.0"
)

func TestProtocolV2DatabaseCatalogIsImmutableAndRejectsUnsafeDefinitions(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	queries, executes := testProtocolV2DatabaseDefinitions()
	executes[0].QueryInvalidationTags = []string{"fixture.database.items"}
	engine, err := newProtocolV2DatabaseEngine(backend, queries, executes)
	if err != nil {
		t.Fatalf("new database engine: %v", err)
	}
	queries[0].SQL = "SELECT secret FROM public.users"
	queries[0].Parameters[0].Field = "mutated"
	executes[0].SQL = "DROP SCHEMA public CASCADE"
	executes[0].QueryInvalidationTags[0] = "fixture.database.changed"
	identity := testProtocolV2DatabaseIdentity()
	query := engine.queries[testProtocolV2DatabaseKey(identity, testDatabaseQueryOperation)]
	execute := engine.executes[testProtocolV2DatabaseKey(identity, testDatabaseExecuteOperation)]
	if query.SQL != "SELECT id, name FROM items WHERE owner_id = $1" || query.Parameters[0].Field != "value" ||
		execute.SQL != "INSERT INTO items (name) VALUES ($1) RETURNING id, name" ||
		!slices.Equal(execute.QueryInvalidationTags, []string{"fixture.database.items"}) {
		t.Fatal("caller mutation changed the immutable database catalog")
	}

	tests := []struct {
		name     string
		queries  []ProtocolV2DatabaseQueryDefinition
		executes []ProtocolV2DatabaseExecuteDefinition
	}{
		{name: "missing exact artifact", queries: []ProtocolV2DatabaseQueryDefinition{{
			OperationID: "fixture.invalid.artifact", StatementVersion: "1", Scope: ProtocolV2DatabaseOwnSchema,
			SQL: "SELECT value FROM items", ResultSchemaID: "fixture.result", ResultSchemaVersion: "1",
			Columns: []ProtocolV2DatabaseColumn{{Name: "value"}}, MaxRows: 1,
		}}},
		{name: "duplicate identity", queries: queries[:1], executes: []ProtocolV2DatabaseExecuteDefinition{{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion, PackageDigest: strings.Repeat("a", 64),
			OperationID: testDatabaseQueryOperation, StatementVersion: testDatabaseStatementVersion,
			SQL: "UPDATE items SET name = $1", Parameters: testDatabaseStringParameter(), MaxAffectedRows: 1,
		}}},
		{name: "multiple SQL statements", queries: []ProtocolV2DatabaseQueryDefinition{{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion, PackageDigest: strings.Repeat("a", 64),
			OperationID: "fixture.invalid.sql", StatementVersion: "1", Scope: ProtocolV2DatabaseOwnSchema,
			SQL: "SELECT 1; DROP TABLE users", ResultSchemaID: "fixture.result", ResultSchemaVersion: "1",
			Columns: []ProtocolV2DatabaseColumn{{Name: "value"}}, MaxRows: 1,
		}}},
		{name: "unbounded query", queries: []ProtocolV2DatabaseQueryDefinition{{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion, PackageDigest: strings.Repeat("a", 64),
			OperationID: "fixture.invalid.rows", StatementVersion: "1", Scope: ProtocolV2DatabaseOwnSchema,
			SQL: "SELECT value FROM items", ResultSchemaID: "fixture.result", ResultSchemaVersion: "1",
			Columns: []ProtocolV2DatabaseColumn{{Name: "value"}},
		}}},
		{name: "unbounded execute", executes: []ProtocolV2DatabaseExecuteDefinition{{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion, PackageDigest: strings.Repeat("a", 64),
			OperationID: "fixture.invalid.execute", StatementVersion: "1", SQL: "DELETE FROM items",
		}}},
		{name: "foreign invalidation tag", executes: func() []ProtocolV2DatabaseExecuteDefinition {
			_, definitions := testProtocolV2DatabaseDefinitions()
			definitions[0].QueryInvalidationTags = []string{"other.plugin.items"}
			return definitions
		}()},
		{name: "noncanonical invalidation tags", executes: func() []ProtocolV2DatabaseExecuteDefinition {
			_, definitions := testProtocolV2DatabaseDefinitions()
			definitions[0].QueryInvalidationTags = []string{"fixture.database.z", "fixture.database.a"}
			return definitions
		}()},
		{name: "bad parameter kind", queries: []ProtocolV2DatabaseQueryDefinition{{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion, PackageDigest: strings.Repeat("a", 64),
			OperationID: "fixture.invalid.parameter", StatementVersion: "1", Scope: ProtocolV2DatabaseOwnSchema,
			SQL: "SELECT value FROM items WHERE value = $1", ResultSchemaID: "fixture.result", ResultSchemaVersion: "1",
			Parameters: []ProtocolV2DatabaseParameter{{SchemaID: "fixture.parameter", SchemaVersion: "1", Field: "value", Kind: "json"}},
			Columns:    []ProtocolV2DatabaseColumn{{Name: "value"}}, MaxRows: 1,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProtocolV2DatabaseEngine(backend, test.queries, test.executes); err == nil {
				t.Fatal("invalid database catalog was accepted")
			}
		})
	}
}

func TestProtocolV2DatabaseCatalogRejectsOtherArtifactsBeforeTransaction(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identities := []*protocolv2.ExtensionIdentity{
		{
			ExtensionId: "fixture.other", ExtensionVersion: testDatabaseExtensionVersion,
			ArtifactDigest: strings.Repeat("a", 64), TrustGrantId: "10", RuntimeEpoch: 1, InstanceId: "other",
		},
		{
			ExtensionId: testDatabaseExtensionID, ExtensionVersion: "2.0.0",
			ArtifactDigest: strings.Repeat("a", 64), TrustGrantId: "11", RuntimeEpoch: 1, InstanceId: "version",
		},
		{
			ExtensionId: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion,
			ArtifactDigest: strings.Repeat("b", 64), TrustGrantId: "12", RuntimeEpoch: 1, InstanceId: "digest",
		},
	}
	for _, identity := range identities {
		request := &hostv2.DatabaseQueryRequest{
			Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
			StatementVersion: testDatabaseStatementVersion,
			Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
		}
		response, err := engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
		if err != nil || response.GetError().GetReason() != "host.database_operation_unsupported" {
			t.Fatalf("artifact %s@%s/%s escaped catalog binding: response=%#v err=%v", identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest(), response, err)
		}
		execute := testProtocolV2DatabaseExecuteRequest(t, identity, "cross-artifact", "value")
		executeResponse, err := engine.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), execute)
		if err != nil || executeResponse.GetError().GetReason() != "host.database_operation_unsupported" {
			t.Fatalf("artifact %s@%s/%s escaped execute catalog binding: response=%#v err=%v", identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest(), executeResponse, err)
		}
	}
	if backend.beginCount() != 0 {
		t.Fatalf("cross-artifact requests opened %d transactions", backend.beginCount())
	}
}

func TestProtocolV2DatabaseQueryUsesAttestedIdentityTypedParametersAndBoundedPages(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.queryRows = []map[string]any{
		{"id": int64(1), "name": "one"},
		{"id": int64(2), "name": "two"},
		{"id": int64(3), "name": "three"},
	}
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identity := testProtocolV2DatabaseIdentity()
	request := &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
		Page:             &protocolv2.PageRequest{Limit: 2},
	}
	response, err := engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
	if err != nil || response.GetError() != nil {
		t.Fatalf("query failed: response=%#v err=%v", response, err)
	}
	if len(response.GetRows()) != 2 || !response.GetPage().GetHasMore() || response.GetPage().GetNextCursor() == "" {
		t.Fatalf("unexpected bounded page: %#v", response)
	}
	if !proto.Equal(response.GetContext().GetExtension(), identity) {
		t.Fatal("response did not bind the broker-attested identity")
	}
	if backend.beginReadOnlyCount() != 1 || backend.tx.resolveScope != ProtocolV2DatabaseOwnSchema || backend.tx.queryLimit != 3 || backend.tx.queryOffset != 0 {
		t.Fatalf("unexpected query transaction: backend=%#v tx=%#v", backend, backend.tx)
	}
	if len(backend.tx.queryArguments) != 1 || backend.tx.queryArguments[0] != int64(42) {
		t.Fatalf("typed parameter was not normalized: %#v", backend.tx.queryArguments)
	}
	if backend.tx.queryStatement != "SELECT id, name FROM items WHERE owner_id = $1" {
		t.Fatalf("runtime did not use Host-owned SQL: %q", backend.tx.queryStatement)
	}
	if backend.tx.commitCount != 1 || backend.tx.rollbackCount != 0 {
		t.Fatalf("query transaction did not commit once: commits=%d rollbacks=%d", backend.tx.commitCount, backend.tx.rollbackCount)
	}

	backend.tx.queryRows = []map[string]any{{"id": int64(3), "name": "three"}}
	next := proto.Clone(request).(*hostv2.DatabaseQueryRequest)
	next.Page = &protocolv2.PageRequest{Limit: 2, Cursor: response.GetPage().GetNextCursor()}
	response, err = engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), next)
	if err != nil || response.GetError() != nil || backend.tx.queryOffset != 2 {
		t.Fatalf("cursor query failed: response=%#v offset=%d err=%v", response, backend.tx.queryOffset, err)
	}

	tampered := proto.Clone(next).(*hostv2.DatabaseQueryRequest)
	tampered.Parameters = []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "43")}
	response, err = engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), tampered)
	if err != nil || response.GetError().GetReason() != "host.database_cursor_invalid" {
		t.Fatalf("cursor accepted a different parameter fingerprint: %#v err=%v", response, err)
	}
}

func TestProtocolV2DatabaseRejectsUnattestedIdentityAndInvalidRequestShapes(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identity := testProtocolV2DatabaseIdentity()
	valid := &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
	}
	tests := []struct {
		name   string
		ctx    context.Context
		change func(*hostv2.DatabaseQueryRequest)
		reason string
	}{
		{name: "missing broker attestation", ctx: context.Background(), reason: "host.database_runtime_stale"},
		{name: "spoofed request identity", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Context.Extension.ArtifactDigest = strings.Repeat("b", 64)
		}, reason: "host.database_identity_mismatch"},
		{name: "unattested actor", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Context.Actor = &protocolv2.Actor{UserId: 42}
		}, reason: "host.database_actor_unattested"},
		{name: "stable core uses HostQuery", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.StableCoreView = true
		}, reason: "host.database_stable_core_view_use_host_query"},
		{name: "unknown operation", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.OperationId = "fixture.database.unknown"
		}, reason: "host.database_operation_unsupported"},
		{name: "wrong parameter schema", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Parameters[0].SchemaId = "fixture.database.wrong"
		}, reason: "host.database_parameter_schema"},
		{name: "extra parameter field", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Parameters[0].Value.Fields["sql"] = structpb.NewStringValue("DROP TABLE users")
		}, reason: "host.database_parameter_shape"},
		{name: "non canonical int64", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Parameters = []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "042")}
		}, reason: "host.database_parameter_value"},
		{name: "page over operation bound", ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), change: func(request *hostv2.DatabaseQueryRequest) {
			request.Page = &protocolv2.PageRequest{Limit: 101}
		}, reason: "host.database_page_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := proto.Clone(valid).(*hostv2.DatabaseQueryRequest)
			if test.change != nil {
				test.change(request)
			}
			response, err := engine.query(test.ctx, request)
			if err != nil || response.GetError().GetReason() != test.reason {
				t.Fatalf("reason=%q want=%q response=%#v err=%v", response.GetError().GetReason(), test.reason, response, err)
			}
		})
	}
	if backend.beginCount() != 0 {
		t.Fatalf("invalid requests opened %d database transactions", backend.beginCount())
	}
}

func TestProtocolV2DatabaseExecuteIsAtomicIdempotentAndConflictSafe(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.executeAffected = 1
	backend.tx.executeRows = []map[string]any{{"id": int64(7), "name": "created"}}
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identity := testProtocolV2DatabaseIdentity()
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	request := testProtocolV2DatabaseExecuteRequest(t, identity, "request-1", "created")

	response, err := engine.execute(ctx, request)
	if err != nil || response.GetError() != nil || response.GetAffectedRows() != 1 || response.GetResult().GetValue().AsMap()["name"] != "created" {
		t.Fatalf("execute failed: response=%#v err=%v", response, err)
	}
	if backend.tx.executeCount != 1 || backend.tx.saveCount != 1 || backend.tx.commitCount != 1 || backend.tx.rollbackCount != 0 || backend.tx.resolveScope != ProtocolV2DatabaseOwnSchema {
		t.Fatalf("unexpected first execution state: %#v", backend.tx)
	}

	restartedIdentity := proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	restartedIdentity.RuntimeEpoch++
	restartedIdentity.InstanceId = "instance-database-restarted"
	replayedRequest := proto.Clone(request).(*hostv2.DatabaseExecuteRequest)
	replayedRequest.Context.Extension = proto.Clone(restartedIdentity).(*protocolv2.ExtensionIdentity)
	replayed, err := engine.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), restartedIdentity), replayedRequest)
	if err != nil || replayed.GetError() != nil || replayed.GetAffectedRows() != 1 || backend.tx.executeCount != 1 || backend.tx.saveCount != 1 || backend.tx.commitCount != 2 {
		t.Fatalf("idempotent replay failed: response=%#v tx=%#v err=%v", replayed, backend.tx, err)
	}

	conflict := testProtocolV2DatabaseExecuteRequest(t, identity, "request-1", "different")
	conflicted, err := engine.execute(ctx, conflict)
	if err != nil || conflicted.GetError().GetReason() != "host.database_idempotency_conflict" || backend.tx.executeCount != 1 || backend.tx.rollbackCount != 1 {
		t.Fatalf("idempotency conflict escaped: response=%#v tx=%#v err=%v", conflicted, backend.tx, err)
	}
}

func TestProtocolV2DatabaseExecuteInvalidationIsTransactionalAndReplaySafe(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.executeAffected = 1
	backend.tx.executeRows = []map[string]any{{"id": int64(7), "name": "created"}}
	queries, executes := testProtocolV2DatabaseDefinitions()
	executes[0].QueryInvalidationTags = []string{"fixture.database.items", "fixture.database.members"}
	engine, err := newProtocolV2DatabaseEngine(backend, queries, executes)
	if err != nil {
		t.Fatal(err)
	}
	identity := testProtocolV2DatabaseIdentity()
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	request := testProtocolV2DatabaseExecuteRequest(t, identity, "invalidate-once", "created")

	response, err := engine.execute(ctx, request)
	if err != nil || response.GetError() != nil || backend.tx.invalidationCount != 1 ||
		backend.tx.invalidationOwner != testDatabaseExtensionID ||
		!slices.Equal(backend.tx.invalidationTags, executes[0].QueryInvalidationTags) {
		t.Fatalf("transactional invalidation failed: response=%#v tx=%#v err=%v", response, backend.tx, err)
	}
	replayed, err := engine.execute(ctx, proto.Clone(request).(*hostv2.DatabaseExecuteRequest))
	if err != nil || replayed.GetError() != nil || backend.tx.invalidationCount != 1 || backend.tx.executeCount != 1 {
		t.Fatalf("replay enqueued a second invalidation: response=%#v tx=%#v err=%v", replayed, backend.tx, err)
	}

	failing := newFakeProtocolV2DatabaseBackend()
	failing.tx.executeAffected = 1
	failing.tx.executeRows = []map[string]any{{"id": int64(8), "name": "created"}}
	failing.tx.invalidationErr = errors.New("queue unavailable")
	failingEngine, err := newProtocolV2DatabaseEngine(failing, queries, executes)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := failingEngine.execute(ctx, testProtocolV2DatabaseExecuteRequest(t, identity, "invalidate-fails", "created"))
	if err != nil || failed.GetError().GetReason() != "host.database_query_invalidation_unavailable" ||
		failing.tx.commitCount != 0 || failing.tx.rollbackCount != 1 {
		t.Fatalf("invalidation failure did not roll back: response=%#v tx=%#v err=%v", failed, failing.tx, err)
	}
}

func TestProtocolV2DatabaseExecuteRollsBackEveryFailureAndHidesStorageErrors(t *testing.T) {
	identity := testProtocolV2DatabaseIdentity()
	request := testProtocolV2DatabaseExecuteRequest(t, identity, "request-failure", "created")
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2DatabaseTx)
		reason    string
	}{
		{name: "stale scope", configure: func(tx *fakeProtocolV2DatabaseTx) { tx.resolveErr = errors.New("secret extension row") }, reason: "host.database_identity_invalid"},
		{name: "receipt unavailable", configure: func(tx *fakeProtocolV2DatabaseTx) { tx.lockErr = errors.New("secret ledger row") }, reason: "host.database_idempotency_unavailable"},
		{name: "statement failed", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.executeErr = errors.New("relation private_table does not exist")
		}, reason: "host.database_execute_failed"},
		{name: "affected rows exceeded", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.executeAffected = 2
			tx.executeRows = []map[string]any{{"id": int64(7), "name": "created"}}
		}, reason: "host.database_affected_rows_exceeded"},
		{name: "multiple returning rows", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.executeAffected = 1
			tx.executeRows = []map[string]any{{"id": int64(7), "name": "a"}, {"id": int64(8), "name": "b"}}
		}, reason: "host.database_result_rows_exceeded"},
		{name: "receipt save failed", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.executeAffected = 1
			tx.executeRows = []map[string]any{{"id": int64(7), "name": "created"}}
			tx.saveErr = errors.New("secret audit failure")
		}, reason: "host.database_receipt_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2DatabaseBackend()
			test.configure(backend.tx)
			engine := newTestProtocolV2DatabaseEngine(t, backend)
			response, err := engine.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
			if err != nil || response.GetError().GetReason() != test.reason {
				t.Fatalf("reason=%q want=%q response=%#v err=%v", response.GetError().GetReason(), test.reason, response, err)
			}
			if strings.Contains(response.GetError().GetMessage(), "secret") || strings.Contains(response.GetError().GetMessage(), "private_table") {
				t.Fatalf("database error leaked across RPC: %#v", response.GetError())
			}
			if backend.tx.commitCount != 0 || backend.tx.rollbackCount != 1 {
				t.Fatalf("failed execute did not roll back once: %#v", backend.tx)
			}
		})
	}
}

func TestProtocolV2DatabaseRollbackFailureReportsUnknownOutcome(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.queryErr = errors.New("query failed")
	backend.tx.rollbackErr = errors.New("connection lost")
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identity := testProtocolV2DatabaseIdentity()
	request := &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
	}
	response, err := engine.query(ContextWithProtocolV2RuntimeIdentity(context.Background(), identity), request)
	if response != nil || err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("rollback failure was reported as an ordinary response: response=%#v err=%v", response, err)
	}
}

func TestProtocolV2DatabaseQueryBoundsResultsAndDeadlines(t *testing.T) {
	identity := testProtocolV2DatabaseIdentity()
	request := &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
	}
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2DatabaseTx)
		reason    string
	}{
		{name: "unexpected column", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.queryRows = []map[string]any{{"id": int64(1), "email": "private@example.test"}}
		}, reason: "host.database_result_shape"},
		{name: "oversized row", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.queryRows = []map[string]any{{"id": int64(1), "name": strings.Repeat("x", protocolV2DatabaseMaximumRowSize)}}
		}, reason: "host.database_row_too_large"},
		{name: "storage error", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.queryErr = errors.New("password authentication failed for secret-role")
		}, reason: "host.database_query_failed"},
		{name: "deadline", configure: func(tx *fakeProtocolV2DatabaseTx) {
			tx.queryWait = true
		}, reason: "host.database_deadline_exceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2DatabaseBackend()
			test.configure(backend.tx)
			engine := newTestProtocolV2DatabaseEngine(t, backend)
			ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
			if test.name == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
				defer cancel()
			}
			response, err := engine.query(ctx, request)
			if err != nil || response.GetError().GetReason() != test.reason {
				t.Fatalf("reason=%q want=%q response=%#v err=%v", response.GetError().GetReason(), test.reason, response, err)
			}
			if strings.Contains(response.GetError().GetMessage(), "secret-role") {
				t.Fatalf("storage error leaked across RPC: %#v", response.GetError())
			}
			if backend.tx.commitCount != 0 || backend.tx.rollbackCount != 1 {
				t.Fatalf("failed query did not roll back: %#v", backend.tx)
			}
		})
	}
}

func TestProtocolV2DatabaseStreamUsesBoundedQueryResult(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	backend.tx.queryRows = []map[string]any{{"id": int64(1), "name": "one"}, {"id": int64(2), "name": "two"}}
	engine := newTestProtocolV2DatabaseEngine(t, backend)
	identity := testProtocolV2DatabaseIdentity()
	request := &hostv2.DatabaseQueryRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseQueryOperation,
		StatementVersion: testDatabaseStatementVersion,
		Parameters:       []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", "42")},
	}
	stream := &fakeProtocolV2DatabaseStream{ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)}
	server := &protocolV2DatabaseServer{engine: engine}
	if err := server.StreamQuery(request, stream); err != nil {
		t.Fatalf("stream query: %v", err)
	}
	if len(stream.rows) != 2 || stream.rows[0].GetSequence() != 1 || stream.rows[1].GetSequence() != 2 || stream.rows[0].GetError() != nil {
		t.Fatalf("unexpected stream rows: %#v", stream.rows)
	}
}

func TestProtocolV2DatabaseUnavailableServerIsFailClosed(t *testing.T) {
	query, err := (&protocolV2DatabaseServer{}).Query(context.Background(), &hostv2.DatabaseQueryRequest{})
	if err != nil || query.GetError().GetReason() != "host.database_backend_unavailable" {
		t.Fatalf("unavailable query response=%#v err=%v", query, err)
	}
	execute, err := (&protocolV2DatabaseServer{}).Execute(context.Background(), &hostv2.DatabaseExecuteRequest{})
	if err != nil || execute.GetError().GetReason() != "host.database_backend_unavailable" {
		t.Fatalf("unavailable execute response=%#v err=%v", execute, err)
	}
}

func TestGatewayBindsAndFreezesProtocolV2DatabaseRuntime(t *testing.T) {
	backend := newFakeProtocolV2DatabaseBackend()
	queries, executes := testProtocolV2DatabaseDefinitions()
	runtime, err := newProtocolV2DatabaseRuntime(backend, queries, executes)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2DatabaseRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2DatabaseRuntime(runtime); err == nil {
		t.Fatal("database runtime rebound before broker registration")
	}
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, ok := server.GetServiceInfo()["sforum.host.v2.DatabaseService"]; !ok {
		t.Fatal("DatabaseService was not registered")
	}
	if err := gateway.BindProtocolV2DatabaseRuntime(runtime); err == nil {
		t.Fatal("database runtime rebound after broker registration")
	}
}

type fakeProtocolV2DatabaseBackend struct {
	mu       sync.Mutex
	beginErr error
	readOnly []bool
	tx       *fakeProtocolV2DatabaseTx
}

func newFakeProtocolV2DatabaseBackend() *fakeProtocolV2DatabaseBackend {
	tx := &fakeProtocolV2DatabaseTx{receipts: make(map[string]protocolV2DatabaseReceipt)}
	return &fakeProtocolV2DatabaseBackend{tx: tx}
}

func (b *fakeProtocolV2DatabaseBackend) Begin(_ context.Context, readOnly bool) (protocolV2DatabaseTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	b.readOnly = append(b.readOnly, readOnly)
	return b.tx, nil
}

func (b *fakeProtocolV2DatabaseBackend) beginCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.readOnly)
}

func (b *fakeProtocolV2DatabaseBackend) beginReadOnlyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, readOnly := range b.readOnly {
		if readOnly {
			count++
		}
	}
	return count
}

type fakeProtocolV2DatabaseTx struct {
	mu sync.Mutex

	resolveScope      string
	resolveErr        error
	queryStatement    string
	queryArguments    []any
	queryLimit        int
	queryOffset       int
	queryRows         []map[string]any
	queryErr          error
	queryWait         bool
	executeStatement  string
	executeArguments  []any
	executeReturns    bool
	executeAffected   uint64
	executeRows       []map[string]any
	executeErr        error
	executeCount      int
	lockErr           error
	saveErr           error
	saveCount         int
	invalidationOwner string
	invalidationTags  []string
	invalidationErr   error
	invalidationCount int
	commitErr         error
	commitCount       int
	rollbackCount     int
	rollbackErr       error
	receipts          map[string]protocolV2DatabaseReceipt
}

func (t *fakeProtocolV2DatabaseTx) ResolveScope(_ context.Context, identity *protocolv2.ExtensionIdentity, scope, operationID, version string) (protocolV2DatabaseScope, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolveScope = scope
	if t.resolveErr != nil {
		return protocolV2DatabaseScope{}, t.resolveErr
	}
	return protocolV2DatabaseScope{
		ExtensionID: identity.GetExtensionId(), ExtensionVersionID: 7,
		ExtensionVersion: identity.GetExtensionVersion(), PackageDigest: identity.GetArtifactDigest(),
		AuthorityType: "trust_grant", TrustGrantID: 9, SchemaName: "sf_ext_fixture",
		RuntimeRoleName: "sf_ext_fixture_runtime", Scope: scope,
		OperationID: operationID, StatementVersion: version,
	}, nil
}

func (t *fakeProtocolV2DatabaseTx) Query(ctx context.Context, _ protocolV2DatabaseScope, statement string, arguments []any, limit, offset int) ([]map[string]any, error) {
	t.mu.Lock()
	t.queryStatement = statement
	t.queryArguments = append([]any(nil), arguments...)
	t.queryLimit = limit
	t.queryOffset = offset
	wait := t.queryWait
	err := t.queryErr
	rows := cloneProtocolV2DatabaseRows(t.queryRows)
	t.mu.Unlock()
	if wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return rows, err
}

func (t *fakeProtocolV2DatabaseTx) Execute(_ context.Context, _ protocolV2DatabaseScope, statement string, arguments []any, returns bool) (uint64, []map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.executeCount++
	t.executeStatement = statement
	t.executeArguments = append([]any(nil), arguments...)
	t.executeReturns = returns
	return t.executeAffected, cloneProtocolV2DatabaseRows(t.executeRows), t.executeErr
}

func (t *fakeProtocolV2DatabaseTx) LockReceipt(_ context.Context, scope protocolV2DatabaseScope, _ string) (*protocolV2DatabaseReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lockErr != nil {
		return nil, t.lockErr
	}
	receipt, exists := t.receipts[scope.IdempotencyKey]
	if !exists {
		return nil, nil
	}
	copy := receipt
	copy.Result = cloneProtocolV2Document(receipt.Result)
	return &copy, nil
}

func (t *fakeProtocolV2DatabaseTx) SaveReceipt(_ context.Context, scope protocolV2DatabaseScope, fingerprint string, receipt protocolV2DatabaseReceipt) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveCount++
	if t.saveErr != nil {
		return t.saveErr
	}
	receipt.Fingerprint = fingerprint
	receipt.Result = cloneProtocolV2Document(receipt.Result)
	t.receipts[scope.IdempotencyKey] = receipt
	return nil
}

func (t *fakeProtocolV2DatabaseTx) EnqueueQueryInvalidation(_ context.Context, owner string, tags []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(tags) == 0 {
		return nil
	}
	t.invalidationCount++
	t.invalidationOwner = owner
	t.invalidationTags = append([]string(nil), tags...)
	return t.invalidationErr
}

func (t *fakeProtocolV2DatabaseTx) Commit(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commitCount++
	return t.commitErr
}

func (t *fakeProtocolV2DatabaseTx) Rollback(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollbackCount++
	return t.rollbackErr
}

func newTestProtocolV2DatabaseEngine(t *testing.T, backend *fakeProtocolV2DatabaseBackend) *protocolV2DatabaseEngine {
	t.Helper()
	queries, executes := testProtocolV2DatabaseDefinitions()
	engine, err := newProtocolV2DatabaseEngine(backend, queries, executes)
	if err != nil {
		t.Fatalf("new database engine: %v", err)
	}
	return engine
}

func testProtocolV2DatabaseDefinitions() ([]ProtocolV2DatabaseQueryDefinition, []ProtocolV2DatabaseExecuteDefinition) {
	columns := []ProtocolV2DatabaseColumn{{Name: "id"}, {Name: "name"}}
	queries := []ProtocolV2DatabaseQueryDefinition{
		{
			ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion,
			PackageDigest: strings.Repeat("a", 64),
			OperationID:   testDatabaseQueryOperation, StatementVersion: testDatabaseStatementVersion,
			Scope: ProtocolV2DatabaseOwnSchema, SQL: "SELECT id, name FROM items WHERE owner_id = $1",
			Parameters: testDatabaseInt64Parameter(), ResultSchemaID: testDatabaseResultSchema,
			ResultSchemaVersion: "1", Columns: columns, MaxRows: 100,
		},
	}
	executes := []ProtocolV2DatabaseExecuteDefinition{{
		ExtensionID: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion,
		PackageDigest: strings.Repeat("a", 64),
		OperationID:   testDatabaseExecuteOperation, StatementVersion: testDatabaseStatementVersion,
		SQL:        "INSERT INTO items (name) VALUES ($1) RETURNING id, name",
		Parameters: testDatabaseStringParameter(), ResultSchemaID: testDatabaseResultSchema,
		ResultSchemaVersion: "1", ReturningColumns: columns, MaxAffectedRows: 1,
	}}
	return queries, executes
}

func testDatabaseInt64Parameter() []ProtocolV2DatabaseParameter {
	return []ProtocolV2DatabaseParameter{{
		SchemaID: testDatabaseParameterSchema, SchemaVersion: "1", Field: "value", Kind: ProtocolV2DatabaseInt64,
	}}
}

func testDatabaseStringParameter() []ProtocolV2DatabaseParameter {
	return []ProtocolV2DatabaseParameter{{
		SchemaID: testDatabaseParameterSchema, SchemaVersion: "1", Field: "value", Kind: ProtocolV2DatabaseString, MaxBytes: 100,
	}}
}

func testProtocolV2DatabaseIdentity() *protocolv2.ExtensionIdentity {
	return &protocolv2.ExtensionIdentity{
		ExtensionId: testDatabaseExtensionID, ExtensionVersion: testDatabaseExtensionVersion,
		ArtifactDigest: strings.Repeat("a", 64), TrustGrantId: "9",
		RuntimeEpoch: 3, InstanceId: "instance-database",
	}
}

func testProtocolV2DatabaseKey(identity *protocolv2.ExtensionIdentity, operationID string) protocolV2DatabaseKey {
	return protocolV2DatabaseKey{
		extensionID: identity.GetExtensionId(), extensionVersion: identity.GetExtensionVersion(),
		packageDigest: identity.GetArtifactDigest(), operationID: operationID, version: testDatabaseStatementVersion,
	}
}

func testProtocolV2DatabaseRequestContext(identity *protocolv2.ExtensionIdentity) *protocolv2.RequestContext {
	return &protocolv2.RequestContext{RequestId: "request-database", Extension: proto.Clone(identity).(*protocolv2.ExtensionIdentity)}
}

func testProtocolV2DatabaseParameter(t *testing.T, schemaID, field string, value any) *protocolv2.TypedDocument {
	t.Helper()
	document, err := structpb.NewStruct(map[string]any{field: value})
	if err != nil {
		t.Fatalf("database parameter: %v", err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: "1", Value: document}
}

func testProtocolV2DatabaseExecuteRequest(t *testing.T, identity *protocolv2.ExtensionIdentity, key, value string) *hostv2.DatabaseExecuteRequest {
	t.Helper()
	return &hostv2.DatabaseExecuteRequest{
		Context: testProtocolV2DatabaseRequestContext(identity), OperationId: testDatabaseExecuteOperation,
		StatementVersion: testDatabaseStatementVersion, IdempotencyKey: key,
		Parameters: []*protocolv2.TypedDocument{testProtocolV2DatabaseParameter(t, testDatabaseParameterSchema, "value", value)},
	}
}

func cloneProtocolV2DatabaseRows(source []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(source))
	for _, row := range source {
		copy := make(map[string]any, len(row))
		for key, value := range row {
			copy[key] = value
		}
		result = append(result, copy)
	}
	return result
}

type fakeProtocolV2DatabaseStream struct {
	ctx  context.Context
	rows []*hostv2.DatabaseRow
}

func (s *fakeProtocolV2DatabaseStream) Send(row *hostv2.DatabaseRow) error {
	s.rows = append(s.rows, proto.Clone(row).(*hostv2.DatabaseRow))
	return nil
}

func (s *fakeProtocolV2DatabaseStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeProtocolV2DatabaseStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeProtocolV2DatabaseStream) SetTrailer(metadata.MD)       {}
func (s *fakeProtocolV2DatabaseStream) Context() context.Context     { return s.ctx }
func (s *fakeProtocolV2DatabaseStream) SendMsg(any) error            { return nil }
func (s *fakeProtocolV2DatabaseStream) RecvMsg(any) error            { return io.EOF }

var _ protocolV2DatabaseBackend = (*fakeProtocolV2DatabaseBackend)(nil)
var _ protocolV2DatabaseTx = (*fakeProtocolV2DatabaseTx)(nil)
