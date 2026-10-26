package hostapi_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestPostgresProtocolV2DatabaseRuntimeExactTransactionsAndRevocation(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for the destructive DatabaseService integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: databaseURL}); err != nil {
		t.Fatalf("prepare database schema: %v", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("p5.database-service.%d", time.Now().UnixNano())
	version := "1.0.0"
	digest := strings.Repeat("d", 64)
	artifact, trustGrantID := insertDatabaseServiceArtifact(t, ctx, pool, extensionID, version, digest)
	identifiers, err := extensionsruntime.ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	// Register cleanup before provisioning. Every later assertion may fail after
	// cluster roles or durable authority rows have already been created.
	t.Cleanup(func() {
		cleanupDatabaseServiceArtifact(t, pool, artifact, identifiers)
	})
	registry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	if _, err := registry.ProvisionOwnSchema(ctx, extensionsruntime.ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 701, AuditEventID: 801,
	}); err != nil {
		t.Fatalf("provision exact own-schema grant: %v", err)
	}
	installDatabaseServiceTable(t, ctx, pool, identifiers)

	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: version, ArtifactDigest: digest,
		TrustGrantId: strconv.FormatInt(trustGrantID, 10), RuntimeEpoch: 1,
		InstanceId: "database-service-runtime-1",
	}
	queryID := extensionID + ".items.list"
	executeID := extensionID + ".items.create"
	parameterSchema := extensionID + ".parameter"
	resultSchema := extensionID + ".result"
	columns := []hostapi.ProtocolV2DatabaseColumn{{Name: "id"}, {Name: "name"}}
	queryDefinitions := []hostapi.ProtocolV2DatabaseQueryDefinition{{
		ExtensionID: extensionID, ExtensionVersion: version, PackageDigest: digest,
		OperationID: queryID, StatementVersion: "1", Scope: hostapi.ProtocolV2DatabaseOwnSchema,
		SQL: "SELECT id, name FROM items ORDER BY id", ResultSchemaID: resultSchema,
		ResultSchemaVersion: "1", Columns: columns, MaxRows: 10,
	}}
	executeDefinitions := []hostapi.ProtocolV2DatabaseExecuteDefinition{{
		ExtensionID: extensionID, ExtensionVersion: version, PackageDigest: digest,
		OperationID: executeID, StatementVersion: "1",
		SQL: "INSERT INTO items (name) VALUES ($1) RETURNING id, name",
		Parameters: []hostapi.ProtocolV2DatabaseParameter{{
			SchemaID: parameterSchema, SchemaVersion: "1", Field: "value",
			Kind: hostapi.ProtocolV2DatabaseString, MaxBytes: 100,
		}},
		ResultSchemaID: resultSchema, ResultSchemaVersion: "1",
		ReturningColumns: columns, MaxAffectedRows: 1,
		QueryInvalidationTags: []string{extensionID + ".items"},
	}}
	jobClient, err := supportjobs.NewInsertOnlyClient(pool, supportjobs.Config{})
	if err != nil {
		t.Fatal(err)
	}
	jobDispatcher := supportjobs.NewDispatcher(jobClient)
	runtime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(
		pool,
		queryDefinitions,
		executeDefinitions,
		hostapi.WithProtocolV2DatabaseQueryInvalidationJobs(jobDispatcher),
	)
	if err != nil {
		t.Fatal(err)
	}
	service := runtime.DatabaseService()

	execute := &hostv2.DatabaseExecuteRequest{
		Context:     databaseServiceContext(t, identity, "database-e2e-1"),
		OperationId: executeID, StatementVersion: "1", IdempotencyKey: "database-e2e-1",
		Parameters: []*protocolv2.TypedDocument{databaseServiceParameter(t, parameterSchema, "created")},
	}
	auditsBeforeExecute := countDatabaseServiceAudits(t, ctx, pool, extensionID)
	executeResponse, err := service.Execute(hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, identity), execute)
	if err != nil || executeResponse.GetError() != nil || executeResponse.GetAffectedRows() != 1 ||
		executeResponse.GetResult().GetValue().AsMap()["name"] != "created" {
		t.Fatalf("execute exact operation: response=%v protocol_error=%v err=%v", executeResponse, executeResponse.GetError(), err)
	}
	if jobs := countDatabaseInvalidationJobs(t, ctx, pool, extensionID); jobs != 1 {
		t.Fatalf("committed invalidation jobs = %d, want 1", jobs)
	}
	if receipts := countDatabaseServiceReceipts(t, ctx, pool, extensionID, execute.GetIdempotencyKey()); receipts != 1 {
		t.Fatalf("committed receipts = %d, want 1", receipts)
	}
	auditsAfterFirst := countDatabaseServiceAudits(t, ctx, pool, extensionID)
	if auditsAfterFirst != auditsBeforeExecute+1 {
		t.Fatalf("committed audit count changed from %d to %d", auditsBeforeExecute, auditsAfterFirst)
	}

	restarted := proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	restarted.RuntimeEpoch = 2
	restarted.InstanceId = "database-service-runtime-2"
	replay := proto.Clone(execute).(*hostv2.DatabaseExecuteRequest)
	replay.Context.Extension = proto.Clone(restarted).(*protocolv2.ExtensionIdentity)
	replayed, err := service.Execute(hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted), replay)
	if err != nil || replayed.GetError() != nil || replayed.GetAffectedRows() != 1 {
		t.Fatalf("replay after runtime restart: response=%#v err=%v", replayed, err)
	}
	if jobs := countDatabaseInvalidationJobs(t, ctx, pool, extensionID); jobs != 1 {
		t.Fatalf("replay invalidation jobs = %d, want 1", jobs)
	}
	if receipts := countDatabaseServiceReceipts(t, ctx, pool, extensionID, execute.GetIdempotencyKey()); receipts != 1 {
		t.Fatalf("replay receipts = %d, want 1", receipts)
	}
	if audits := countDatabaseServiceAudits(t, ctx, pool, extensionID); audits != auditsAfterFirst {
		t.Fatalf("replay changed audit count from %d to %d", auditsAfterFirst, audits)
	}
	var itemCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{identifiers.Schema, "items"}.Sanitize()).Scan(&itemCount); err != nil || itemCount != 1 {
		t.Fatalf("idempotent replay wrote %d rows: %v", itemCount, err)
	}

	failureRequest := proto.Clone(execute).(*hostv2.DatabaseExecuteRequest)
	failureRequest.Context = databaseServiceContext(t, restarted, "database-e2e-invalidation-failure")
	failureRequest.IdempotencyKey = "database-e2e-invalidation-failure"
	failureRequest.Parameters = []*protocolv2.TypedDocument{databaseServiceParameter(t, parameterSchema, "rolled-back")}
	failingClient := &databaseInvalidationFailingClient{delegate: jobClient}
	failingRuntime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(
		pool, queryDefinitions, executeDefinitions,
		hostapi.WithProtocolV2DatabaseQueryInvalidationJobs(supportjobs.NewDispatcher(failingClient)),
	)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := failingRuntime.DatabaseService().Execute(
		hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted), failureRequest,
	)
	if err != nil || failed.GetError().GetReason() != "host.database_query_invalidation_unavailable" {
		t.Fatalf("invalidation failure response=%#v err=%v", failed, err)
	}
	insertCalls, insertTxCalls, successfulInsertTxCalls, insertManyCalls := failingClient.snapshot()
	if insertCalls != 0 || insertTxCalls != 1 || successfulInsertTxCalls != 1 || insertManyCalls != 0 {
		t.Fatalf("invalidation inserts nonTx=%d tx=%d successfulTx=%d many=%d",
			insertCalls, insertTxCalls, successfulInsertTxCalls, insertManyCalls)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{identifiers.Schema, "items"}.Sanitize()).Scan(&itemCount); err != nil || itemCount != 1 {
		t.Fatalf("failed invalidation leaked business row count=%d err=%v", itemCount, err)
	}
	if receipts := countDatabaseServiceReceipts(t, ctx, pool, extensionID, failureRequest.GetIdempotencyKey()); receipts != 0 {
		t.Fatalf("failed invalidation leaked %d receipts", receipts)
	}
	if audits := countDatabaseServiceAudits(t, ctx, pool, extensionID); audits != auditsAfterFirst {
		t.Fatalf("failed invalidation changed audit count from %d to %d", auditsAfterFirst, audits)
	}
	if jobs := countDatabaseInvalidationJobs(t, ctx, pool, extensionID); jobs != 1 {
		t.Fatalf("failed invalidation changed job count to %d", jobs)
	}

	retried, err := service.Execute(hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted), failureRequest)
	if err != nil || retried.GetError() != nil || retried.GetAffectedRows() != 1 {
		t.Fatalf("retry after invalidation rollback: response=%#v err=%v", retried, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{identifiers.Schema, "items"}.Sanitize()).Scan(&itemCount); err != nil || itemCount != 2 {
		t.Fatalf("retry business row count=%d err=%v", itemCount, err)
	}
	if receipts := countDatabaseServiceReceipts(t, ctx, pool, extensionID, failureRequest.GetIdempotencyKey()); receipts != 1 {
		t.Fatalf("retry receipts = %d, want 1", receipts)
	}
	if audits := countDatabaseServiceAudits(t, ctx, pool, extensionID); audits != auditsAfterFirst+1 {
		t.Fatalf("retry audit count = %d, want %d", audits, auditsAfterFirst+1)
	}
	if jobs := countDatabaseInvalidationJobs(t, ctx, pool, extensionID); jobs != 2 {
		t.Fatalf("retry invalidation jobs = %d, want 2", jobs)
	}

	query := &hostv2.DatabaseQueryRequest{
		Context:     databaseServiceContext(t, restarted, "database-e2e-query"),
		OperationId: queryID, StatementVersion: "1", Page: &protocolv2.PageRequest{Limit: 10},
	}
	queryResponse, err := service.Query(hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted), query)
	if err != nil || queryResponse.GetError() != nil || len(queryResponse.GetRows()) != 2 ||
		queryResponse.GetRows()[0].GetValue().AsMap()["name"] != "created" {
		t.Fatalf("query exact operation: response=%#v err=%v", queryResponse, err)
	}

	coreRuntime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(pool, []hostapi.ProtocolV2DatabaseQueryDefinition{{
		ExtensionID: extensionID, ExtensionVersion: version, PackageDigest: digest,
		OperationID: extensionID + ".core.probe", StatementVersion: "1", Scope: hostapi.ProtocolV2DatabaseOwnSchema,
		SQL: "SELECT id FROM public.users", ResultSchemaID: resultSchema, ResultSchemaVersion: "1",
		Columns: []hostapi.ProtocolV2DatabaseColumn{{Name: "id"}}, MaxRows: 1,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := coreRuntime.DatabaseService().Query(
		hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted),
		&hostv2.DatabaseQueryRequest{
			Context:     databaseServiceContext(t, restarted, "database-core-denied"),
			OperationId: extensionID + ".core.probe", StatementVersion: "1",
		},
	)
	if err != nil || denied.GetError().GetReason() != "host.database_query_failed" ||
		strings.Contains(strings.ToLower(denied.GetError().GetMessage()), "permission") || strings.Contains(denied.GetError().GetMessage(), "users") {
		t.Fatalf("core isolation/error redaction: response=%#v err=%v", denied, err)
	}

	if err := registry.RevokeOwnSchema(ctx, extensionsruntime.ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 702, AuditEventID: 802,
	}); err != nil {
		t.Fatalf("revoke exact own-schema grant: %v", err)
	}
	revoked, err := service.Query(hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, restarted), query)
	if err != nil || revoked.GetError().GetReason() != "host.database_runtime_stale" {
		t.Fatalf("revoked database grant remained callable: response=%#v err=%v", revoked, err)
	}
}

type databaseInvalidationFailingClient struct {
	mu                      sync.Mutex
	delegate                supportjobs.RiverClient
	insertCalls             int
	insertTxCalls           int
	successfulInsertTxCalls int
	insertManyCalls         int
}

func (c *databaseInvalidationFailingClient) Insert(
	context.Context, river.JobArgs, *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	c.mu.Lock()
	c.insertCalls++
	c.mu.Unlock()
	return nil, fmt.Errorf("injected invalidation insert failure")
}

func (c *databaseInvalidationFailingClient) InsertTx(
	ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	c.mu.Lock()
	c.insertTxCalls++
	c.mu.Unlock()
	if c.delegate == nil {
		return nil, fmt.Errorf("missing invalidation insert delegate")
	}
	result, err := c.delegate.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.successfulInsertTxCalls++
	c.mu.Unlock()
	return result, fmt.Errorf("injected invalidation insert failure after River insert")
}

func (c *databaseInvalidationFailingClient) InsertMany(
	context.Context, []river.InsertManyParams,
) ([]*rivertype.JobInsertResult, error) {
	c.mu.Lock()
	c.insertManyCalls++
	c.mu.Unlock()
	return nil, fmt.Errorf("injected invalidation insert failure")
}

func (c *databaseInvalidationFailingClient) snapshot() (int, int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.insertCalls, c.insertTxCalls, c.successfulInsertTxCalls, c.insertManyCalls
}

func countDatabaseInvalidationJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extensionID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM river_job
		WHERE kind = $1 AND args ->> 'owner_extension_id' = $2
	`, queryregistryjobs.InvalidateResultCacheKind, extensionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countDatabaseServiceReceipts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID string,
	idempotencyKey string,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM extension_host_command_receipts
		WHERE extension_id = $1 AND idempotency_key = $2
	`, extensionID, idempotencyKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countDatabaseServiceAudits(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extensionID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM audit_events
		WHERE metadata #>> '{extensionId}' = $1
	`, extensionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func insertDatabaseServiceArtifact(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID, version, digest string,
) (extensionsruntime.ExtensionDatabaseArtifact, int64) {
	t.Helper()
	manifest := fmt.Sprintf(`{"id":%q,"version":%q,"database":{"contractVersion":%q,"authority":"own_schema","schema":"logical_schema","role":"logical_role","backup":{"required":false},"retention":{"onDisable":"retain","onUninstall":"retain"}}}`, extensionID, version, extensionID+".database@1")
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system)
		VALUES ($1, 'plugin', 'DatabaseService integration fixture', 'installed', 'uploaded', false)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, $2, $3::jsonb, '/tmp/inert-database-service-fixture', $4)
		RETURNING id
	`, extensionID, version, manifest, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET status = 'enabled', active_version_id = $2 WHERE id = $1`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action,
			artifact_digests, impact_document, impact_digest
		) VALUES (
			$1, $2, $3, 'enable', '{}'::jsonb, $4::jsonb, repeat('e', 64)
		) RETURNING id
	`, extensionID, version, digest, manifest).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	return extensionsruntime.ExtensionDatabaseArtifact{
		ExtensionID: extensionID, Version: version, VersionID: versionID, PackageDigest: digest,
	}, grantID
}

func installDatabaseServiceTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, identifiers extensionsruntime.ExtensionDatabaseIdentifiers) {
	t.Helper()
	table := pgx.Identifier{identifiers.Schema, "items"}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE TABLE `+table+` (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` OWNER TO `+pgx.Identifier{identifiers.OwnerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	sequence := pgx.Identifier{identifiers.Schema, "items_id_seq"}.Sanitize()
	if _, err := pool.Exec(ctx, `ALTER SEQUENCE `+sequence+` OWNER TO `+pgx.Identifier{identifiers.OwnerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
}

func cleanupDatabaseServiceArtifact(t *testing.T, pool *pgxpool.Pool, artifact extensionsruntime.ExtensionDatabaseArtifact, identifiers extensionsruntime.ExtensionDatabaseIdentifiers) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exec := func(label, query string, arguments ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, arguments...); err != nil {
			t.Errorf("cleanup DatabaseService fixture %s: %v", label, err)
		}
	}
	exec("Query invalidations", `DELETE FROM river_job WHERE kind = $1 AND args ->> 'owner_extension_id' = $2`,
		queryregistryjobs.InvalidateResultCacheKind, artifact.ExtensionID)
	exec("command actor delegations", `DELETE FROM extension_host_command_actor_delegation_consumptions WHERE extension_id = $1`, artifact.ExtensionID)
	exec("command receipts", `DELETE FROM extension_host_command_receipts WHERE extension_id = $1`, artifact.ExtensionID)
	exec("runtime leases", `DELETE FROM extension_database_runtime_leases WHERE extension_id = $1`, artifact.ExtensionID)
	exec("credentials", `DELETE FROM extension_database_credentials WHERE extension_id = $1`, artifact.ExtensionID)
	exec("grant powers", `
		DELETE FROM extension_database_grant_powers
		WHERE grant_id IN (SELECT id FROM extension_database_grants WHERE extension_id = $1)
	`, artifact.ExtensionID)
	exec("grants", `DELETE FROM extension_database_grants WHERE extension_id = $1`, artifact.ExtensionID)
	exec("schema", `DROP SCHEMA IF EXISTS `+pgx.Identifier{identifiers.Schema}.Sanitize()+` CASCADE`)
	exec("role membership", `REVOKE `+pgx.Identifier{identifiers.OwnerRole}.Sanitize()+` FROM `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
	exec("runtime role ownership", `DROP OWNED BY `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
	exec("runtime role", `DROP ROLE IF EXISTS `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
	exec("owner role ownership", `DROP OWNED BY `+pgx.Identifier{identifiers.OwnerRole}.Sanitize())
	exec("owner role", `DROP ROLE IF EXISTS `+pgx.Identifier{identifiers.OwnerRole}.Sanitize())
	exec("resources", `DELETE FROM extension_database_resources WHERE extension_id = $1`, artifact.ExtensionID)
	exec("trust grants", `DELETE FROM extension_trust_grants WHERE extension_id = $1`, artifact.ExtensionID)
	exec("active version", `UPDATE extensions SET active_version_id = NULL WHERE id = $1`, artifact.ExtensionID)
	exec("extension", `DELETE FROM extensions WHERE id = $1`, artifact.ExtensionID)
	exec("audit", `DELETE FROM audit_events WHERE metadata #>> '{extensionId}' = $1`, artifact.ExtensionID)
}

func databaseServiceContext(t *testing.T, identity *protocolv2.ExtensionIdentity, requestID string) *protocolv2.RequestContext {
	t.Helper()
	return &protocolv2.RequestContext{
		RequestId: requestID, Extension: proto.Clone(identity).(*protocolv2.ExtensionIdentity),
	}
}

func databaseServiceParameter(t *testing.T, schemaID, value string) *protocolv2.TypedDocument {
	t.Helper()
	document, err := structpb.NewStruct(map[string]any{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: "1", Value: document}
}
