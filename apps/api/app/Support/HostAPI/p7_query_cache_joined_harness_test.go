package hostapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const (
	p7QueryCacheJoinedRedisAddressEnv       = "SFORUM_QUERY_CACHE_TEST_REDIS_ADDR"
	p7QueryCacheJoinedRedisPasswordEnv      = "SFORUM_QUERY_CACHE_TEST_REDIS_PASSWORD"
	p7QueryCacheJoinedExpectedRedisRunIDEnv = "SFORUM_QUERY_CACHE_JOINED_EXPECTED_REDIS_RUN_ID"
	p7QueryCacheJoinedOwnershipTokenEnv     = "SFORUM_QUERY_CACHE_JOINED_OWNERSHIP_TOKEN"
)

type p7QueryCacheJoinedDatabaseService interface {
	Query(context.Context, *hostv2.DatabaseQueryRequest) (*hostv2.DatabaseQueryResponse, error)
	Execute(context.Context, *hostv2.DatabaseExecuteRequest) (*hostv2.DatabaseExecuteResponse, error)
}

type p7QueryCacheJoinedFixture struct {
	ctx          context.Context
	baseURL      string
	databaseURL  string
	databaseName string
	seed         string
	pool         *pgxpool.Pool

	extensionID     string
	version         string
	digest          string
	artifact        extensionsruntime.ExtensionDatabaseArtifact
	identifiers     extensionsruntime.ExtensionDatabaseIdentifiers
	trustGrantID    int64
	identity        *protocolv2.ExtensionIdentity
	queryID         string
	executeID       string
	parameterSchema string
	resultSchema    string
	service         p7QueryCacheJoinedDatabaseService

	cleanupAuthorized bool
	grantProvisioned  bool
	retain            bool
}

type p7QueryCacheJoinedExecution struct {
	runtime       *queryregistry.ExecutionRuntime
	providerCalls atomic.Int32
}

func newP7QueryCacheJoinedSeedFixture(
	t *testing.T,
	ctx context.Context,
	baseURL string,
	seed string,
) *p7QueryCacheJoinedFixture {
	t.Helper()
	databaseName := p7QueryCacheJoinedDatabaseName(seed)
	databaseURL := p7QueryCacheJoinedDatabaseURL(t, baseURL, databaseName)
	p7QueryCacheJoinedCreateDatabase(t, ctx, baseURL, databaseName)
	fixture := newP7QueryCacheJoinedFixture(ctx, baseURL, databaseURL, databaseName, seed)
	fixture.setIdentifiers(t)
	fixture.cleanupAuthorized = true
	t.Cleanup(func() { fixture.finish(t) })
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open isolated P7 Query database: %v", err)
	}
	fixture.pool = pool
	fixture.createOwnershipEvidence(t, p7QueryCacheJoinedOwnershipToken(t))

	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: databaseURL}); err != nil {
		t.Fatalf("migrate isolated P7 Query database: %v", err)
	}

	artifact, trustGrantID := insertDatabaseServiceArtifact(
		t, ctx, pool, fixture.extensionID, fixture.version, fixture.digest,
	)
	fixture.artifact = artifact
	fixture.trustGrantID = trustGrantID
	registry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	if _, err := registry.ProvisionOwnSchema(ctx, extensionsruntime.ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 1701, AuditEventID: 1801,
	}); err != nil {
		t.Fatalf("provision joined Query own-schema grant: %v", err)
	}
	fixture.grantProvisioned = true
	installDatabaseServiceTable(t, ctx, pool, fixture.identifiers)
	if _, err := pool.Exec(ctx, `INSERT INTO `+pgx.Identifier{fixture.identifiers.Schema, "items"}.Sanitize()+` (name) VALUES ('before')`); err != nil {
		t.Fatalf("seed joined Query provider row: %v", err)
	}
	fixture.bindRuntime(t)
	return fixture
}

func openP7QueryCacheJoinedVerifyFixture(
	t *testing.T,
	ctx context.Context,
	baseURL string,
	seed string,
) *p7QueryCacheJoinedFixture {
	t.Helper()
	databaseName := p7QueryCacheJoinedDatabaseName(seed)
	if !p7QueryCacheJoinedDatabaseExists(t, ctx, baseURL, databaseName) {
		t.Fatalf("isolated P7 Query database %q is missing; run seed phase first", databaseName)
	}
	fixture := newP7QueryCacheJoinedFixture(
		ctx, baseURL, p7QueryCacheJoinedDatabaseURL(t, baseURL, databaseName), databaseName, seed,
	)
	fixture.setIdentifiers(t)
	pool, err := pgxpool.New(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatalf("open retained P7 Query database: %v", err)
	}
	fixture.pool = pool
	fixture.requireOwnershipEvidence(t, p7QueryCacheJoinedOwnershipToken(t))
	fixture.cleanupAuthorized = true
	t.Cleanup(func() { fixture.finish(t) })
	if err := pool.QueryRow(ctx, `
		SELECT ev.id, tg.id
		FROM extensions e
		JOIN extension_versions ev ON ev.id = e.active_version_id
		JOIN extension_trust_grants tg
		  ON tg.extension_id = e.id
		 AND tg.extension_version = ev.version
		 AND tg.package_digest = ev.package_digest
		 AND tg.action = 'enable'
		 AND tg.revoked_at IS NULL
		WHERE e.id = $1
	`, fixture.extensionID).Scan(&fixture.artifact.VersionID, &fixture.trustGrantID); err != nil {
		t.Fatalf("restore retained P7 Query artifact: %v", err)
	}
	fixture.grantProvisioned = true
	fixture.bindRuntime(t)
	return fixture
}

func newP7QueryCacheJoinedFixture(
	ctx context.Context,
	baseURL string,
	databaseURL string,
	databaseName string,
	seed string,
) *p7QueryCacheJoinedFixture {
	digest := sha256.Sum256([]byte("artifact\x00" + seed))
	suffix := p7QueryCacheJoinedSuffix(seed)
	extensionID := "p7.query-joined." + suffix
	fixture := &p7QueryCacheJoinedFixture{
		ctx: ctx, baseURL: baseURL, databaseURL: databaseURL, databaseName: databaseName, seed: seed,
		extensionID: extensionID, version: "1.0.0", digest: hex.EncodeToString(digest[:]),
		queryID: extensionID + ".items.latest", executeID: extensionID + ".items.create",
		parameterSchema: extensionID + ".parameter", resultSchema: extensionID + ".database-result",
	}
	fixture.artifact = extensionsruntime.ExtensionDatabaseArtifact{
		ExtensionID:   fixture.extensionID,
		Version:       fixture.version,
		PackageDigest: fixture.digest,
	}
	return fixture
}

func (fixture *p7QueryCacheJoinedFixture) setIdentifiers(t *testing.T) {
	t.Helper()
	identifiers, err := extensionsruntime.ExtensionDatabaseIdentifiersFor(fixture.extensionID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.identifiers = identifiers
}

func (fixture *p7QueryCacheJoinedFixture) bindRuntime(t *testing.T) {
	t.Helper()
	fixture.identity = &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.extensionID, ExtensionVersion: fixture.version,
		ArtifactDigest: fixture.digest, TrustGrantId: strconv.FormatInt(fixture.trustGrantID, 10),
		RuntimeEpoch: 1, InstanceId: "p7-query-joined-runtime",
	}
	columns := []hostapi.ProtocolV2DatabaseColumn{{Name: "name"}}
	queryDefinitions := []hostapi.ProtocolV2DatabaseQueryDefinition{{
		ExtensionID: fixture.extensionID, ExtensionVersion: fixture.version, PackageDigest: fixture.digest,
		OperationID: fixture.queryID, StatementVersion: "1", Scope: hostapi.ProtocolV2DatabaseOwnSchema,
		SQL: "SELECT name FROM items ORDER BY id DESC LIMIT 1", ResultSchemaID: fixture.resultSchema,
		ResultSchemaVersion: "1", Columns: columns, MaxRows: 1,
	}}
	executeDefinitions := []hostapi.ProtocolV2DatabaseExecuteDefinition{{
		ExtensionID: fixture.extensionID, ExtensionVersion: fixture.version, PackageDigest: fixture.digest,
		OperationID: fixture.executeID, StatementVersion: "1",
		SQL: "INSERT INTO items (name) VALUES ($1) RETURNING name",
		Parameters: []hostapi.ProtocolV2DatabaseParameter{{
			SchemaID: fixture.parameterSchema, SchemaVersion: "1", Field: "value",
			Kind: hostapi.ProtocolV2DatabaseString, MaxBytes: 100,
		}},
		ResultSchemaID: fixture.resultSchema, ResultSchemaVersion: "1",
		ReturningColumns: columns, MaxAffectedRows: 1,
		QueryInvalidationTags: []string{fixture.extensionID + ".items"},
	}}
	jobClient, err := supportjobs.NewInsertOnlyClient(fixture.pool, supportjobs.Config{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(
		fixture.pool, queryDefinitions, executeDefinitions,
		hostapi.WithProtocolV2DatabaseQueryInvalidationJobs(supportjobs.NewDispatcher(jobClient)),
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = runtime.DatabaseService()
}

func (fixture *p7QueryCacheJoinedFixture) buildExecution(
	t *testing.T,
	cache queryregistry.QueryResultCache,
) *p7QueryCacheJoinedExecution {
	t.Helper()
	execution := &p7QueryCacheJoinedExecution{}
	var requestSequence atomic.Uint64
	provider := queryregistry.ExecutableProviderFunc(func(
		ctx context.Context,
		_ queryregistry.ProviderExecutionRequest,
	) (queryregistry.ProviderExecutionResult, error) {
		execution.providerCalls.Add(1)
		requestID := fmt.Sprintf("p7-query-provider-%d", requestSequence.Add(1))
		response, err := fixture.service.Query(
			hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, fixture.identity),
			&hostv2.DatabaseQueryRequest{
				Context: &protocolv2.RequestContext{
					RequestId: requestID, Extension: proto.Clone(fixture.identity).(*protocolv2.ExtensionIdentity),
				},
				OperationId: fixture.queryID, StatementVersion: "1",
				Page: &protocolv2.PageRequest{Limit: 1},
			},
		)
		if err != nil {
			return queryregistry.ProviderExecutionResult{}, err
		}
		if response.GetError() != nil {
			return queryregistry.ProviderExecutionResult{}, fmt.Errorf("DatabaseService query failed: %s", response.GetError().GetReason())
		}
		if len(response.GetRows()) != 1 {
			return queryregistry.ProviderExecutionResult{}, fmt.Errorf("DatabaseService rows=%d, want 1", len(response.GetRows()))
		}
		name, ok := response.GetRows()[0].GetValue().AsMap()["name"].(string)
		if !ok || name == "" {
			return queryregistry.ProviderExecutionResult{}, fmt.Errorf("DatabaseService returned invalid name")
		}
		return queryregistry.ProviderExecutionResult{Rows: []queryregistry.QueryRow{{"name": name}}}, nil
	})

	artifact := queryregistry.Artifact{
		ExtensionID: fixture.extensionID, ExtensionVersion: fixture.version,
		PackageDigest: fixture.digest, VersionID: fixture.artifact.VersionID,
		RuntimeInstanceID: fixture.identity.GetInstanceId(),
	}
	declaration := queryregistry.QueryDeclaration{
		ID: fixture.queryID, ContractVersion: fixture.queryID + "@1",
		Entity: fixture.extensionID + ".item", PlanVersion: fixture.queryID + ".plan@1",
		Fields: []string{"name"}, Sort: []string{"name"}, Pagination: queryregistry.PaginationNone,
		ResultSchema:     fixture.queryID + ".result@1",
		PermissionPolicy: queryregistry.PermissionPolicyPublic,
		CacheTags:        []string{fixture.extensionID + ".items"}, Handler: fixture.queryID,
		IdentityFields: []string{"name"}, DefaultSort: []queryregistry.SortValue{{Field: "name"}},
	}
	publication := queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}
	schema := []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}},"additionalProperties":false}`)
	schemaDigest := sha256.Sum256(schema)
	bound, err := queryregistry.BindResultSchemas(publication, []queryregistry.JSONResultSchemaBinding{{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Artifact: artifact, SchemaDigest: hex.EncodeToString(schemaDigest[:]), Schema: schema,
	}})
	if err != nil {
		t.Fatalf("bind joined Query result schema: %v", err)
	}
	bound, err = queryregistry.BindExecutableRuntime(bound, []queryregistry.ExecutableProviderMaterial{{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Handler: declaration.Handler, Provider: provider,
	}}, nil)
	if err != nil {
		t.Fatalf("bind joined Query provider: %v", err)
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(func(
		queryregistry.QueryCostInput,
	) (queryregistry.QueryCost, error) {
		return queryregistry.QueryCost{Units: 10, Maximum: 1_000}, nil
	}))).WithPluginAdmission(func(candidate queryregistry.Artifact) bool { return candidate == artifact })
	if _, err := registry.Publish(bound); err != nil {
		t.Fatalf("publish joined Query: %v", err)
	}
	binding := bound.Queries[0]
	resolver, err := queryregistry.NewStaticProviderResolver([]queryregistry.ExecutableProviderBinding{{
		QueryID: binding.ID, ContractVersion: binding.ContractVersion, PlanVersion: binding.PlanVersion,
		ResultSchema: binding.ResultSchema, Artifact: artifact, ProviderDigest: binding.ProviderDigest,
		FailurePolicy: queryregistry.ProviderFailureFailClosed, Provider: provider,
	}})
	if err != nil {
		t.Fatalf("create joined Query provider resolver: %v", err)
	}
	execution.runtime, err = queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: resolver, Schemas: registry, Cache: cache,
		Admission: queryregistry.ContextualExecutionAdmissionFunc(func(
			ctx context.Context,
			candidate queryregistry.Artifact,
		) (queryregistry.ExecutionAdmissionLease, error) {
			if candidate != artifact {
				return queryregistry.ExecutionAdmissionLease{}, queryregistry.ErrArtifactUnavailable
			}
			return queryregistry.ExecutionAdmissionLease{Context: ctx, Release: func() {}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create joined Query execution runtime: %v", err)
	}
	return execution
}

func (fixture *p7QueryCacheJoinedFixture) executeMutation(t *testing.T) int64 {
	t.Helper()
	auditsBefore := countDatabaseServiceAudits(t, fixture.ctx, fixture.pool, fixture.extensionID)
	request := &hostv2.DatabaseExecuteRequest{
		Context:     databaseServiceContext(t, fixture.identity, "p7-query-joined-mutation"),
		OperationId: fixture.executeID, StatementVersion: "1", IdempotencyKey: "p7-query-joined-mutation",
		Parameters: []*protocolv2.TypedDocument{databaseServiceParameter(t, fixture.parameterSchema, "after")},
	}
	response, err := fixture.service.Execute(
		hostapi.ContextWithProtocolV2RuntimeIdentity(fixture.ctx, fixture.identity), request,
	)
	if err != nil || response.GetError() != nil || response.GetAffectedRows() != 1 ||
		response.GetResult().GetValue().AsMap()["name"] != "after" {
		t.Fatalf("joined Query mutation response=%#v err=%v", response, err)
	}
	if jobs := countDatabaseInvalidationJobs(t, fixture.ctx, fixture.pool, fixture.extensionID); jobs != 1 {
		t.Fatalf("joined Query invalidation jobs=%d, want 1", jobs)
	}
	if receipts := countDatabaseServiceReceipts(t, fixture.ctx, fixture.pool, fixture.extensionID, request.GetIdempotencyKey()); receipts != 1 {
		t.Fatalf("joined Query receipts=%d, want 1", receipts)
	}
	if audits := countDatabaseServiceAudits(t, fixture.ctx, fixture.pool, fixture.extensionID); audits != auditsBefore+1 {
		t.Fatalf("joined Query audits=%d, want %d", audits, auditsBefore+1)
	}
	var rows int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM `+pgx.Identifier{fixture.identifiers.Schema, "items"}.Sanitize()).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("joined Query business rows=%d err=%v", rows, err)
	}
	var jobID int64
	var state string
	var argsJSON []byte
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT id, state::text, args
		FROM river_job
		WHERE kind = $1 AND args ->> 'owner_extension_id' = $2
	`, queryregistryjobs.InvalidateResultCacheKind, fixture.extensionID).Scan(&jobID, &state, &argsJSON); err != nil {
		t.Fatalf("load joined Query River row: %v", err)
	}
	var args queryregistryjobs.InvalidateResultCacheArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		t.Fatalf("decode joined Query River row: %v", err)
	}
	if state != "available" || args.SchemaVersion != queryregistryjobs.InvalidateResultCacheSchemaVersion ||
		args.OwnerExtensionID != fixture.extensionID ||
		len(args.Tags) != 1 || args.Tags[0] != fixture.extensionID+".items" {
		t.Fatalf("joined Query River state=%q args=%#v", state, args)
	}
	return jobID
}

func (fixture *p7QueryCacheJoinedFixture) assertDurableEvidence(t *testing.T) {
	t.Helper()
	var rows, receipts, jobs int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM `+pgx.Identifier{fixture.identifiers.Schema, "items"}.Sanitize()).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_host_command_receipts
		WHERE extension_id = $1 AND idempotency_key = 'p7-query-joined-mutation'
	`, fixture.extensionID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM river_job
		WHERE kind = $1 AND args ->> 'owner_extension_id' = $2 AND state = 'completed'
	`, queryregistryjobs.InvalidateResultCacheKind, fixture.extensionID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || receipts != 1 || jobs != 1 {
		t.Fatalf("durable joined evidence rows=%d receipts=%d completed_jobs=%d", rows, receipts, jobs)
	}
}

func (fixture *p7QueryCacheJoinedFixture) retainForRestart() { fixture.retain = true }

func (fixture *p7QueryCacheJoinedFixture) finish(t *testing.T) {
	t.Helper()
	if fixture == nil {
		return
	}
	if fixture.retain {
		if fixture.pool != nil {
			fixture.pool.Close()
		}
		return
	}
	if !fixture.cleanupAuthorized {
		if fixture.pool != nil {
			fixture.pool.Close()
		}
		return
	}
	if fixture.pool != nil {
		if fixture.grantProvisioned {
			if err := p7QueryCacheJoinedRevokeGrant(fixture); err != nil {
				t.Errorf("revoke joined Query own-schema grant: %v", err)
			}
		}
		fixture.pool.Close()
		fixture.pool = nil
	}
	if err := p7QueryCacheJoinedDropDatabase(fixture.baseURL, fixture.databaseName); err != nil {
		t.Errorf("cleanup joined Query database: %v", err)
		return
	}
	if err := p7QueryCacheJoinedCleanupRoles(fixture.baseURL, fixture.databaseName, fixture.identifiers); err != nil {
		t.Errorf("cleanup joined Query roles: %v", err)
	}
}
