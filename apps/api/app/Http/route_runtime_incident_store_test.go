package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestRouteRuntimeIncidentKeyAndPayloadFreeValidation(t *testing.T) {
	seen := make(map[string]struct{})
	for range 100 {
		key, err := NewRouteRuntimeIncidentKey()
		if err != nil || !routeRuntimeIncidentHex.MatchString(key) {
			t.Fatalf("key=%q err=%v", key, err)
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate incident key %q", key)
		}
		seen[key] = struct{}{}
	}

	evidence := routeRuntimeIncidentTestEvidence(t, 42)
	if !validRouteRuntimeIncidentEvidence(evidence) {
		t.Fatalf("valid evidence rejected: %#v", evidence)
	}
	metadata, err := routeRuntimeIncidentAuditMetadata(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"requestBody", "responseBody", "rawError", "headers", "query", "payload"} {
		if strings.Contains(strings.ToLower(metadata), strings.ToLower(forbidden)) {
			t.Fatalf("audit metadata contains %q: %s", forbidden, metadata)
		}
	}

	invalid := evidence
	invalid.RuntimeExecutionObserved = false
	if validRouteRuntimeIncidentEvidence(invalid) {
		t.Fatal("unobserved runtime evidence accepted")
	}
	invalid = evidence
	invalid.Mode = extensionmanifest.RouteModeHTTP
	if validRouteRuntimeIncidentEvidence(invalid) {
		t.Fatal("handler-stage HTTP incident accepted")
	}
	for _, status := range []int{99, 600, 700} {
		invalid = evidence
		invalid.ResponseStatus = status
		if validRouteRuntimeIncidentEvidence(invalid) {
			t.Fatalf("invalid response status %d accepted", status)
		}
	}
	invalid = evidence
	invalid.StepIndex = int(^uint32(0))
	if validRouteRuntimeIncidentEvidence(invalid) {
		t.Fatal("out-of-range step index accepted")
	}
	invalid = evidence
	invalid.ContractVersion = "INVALID@1"
	if validRouteRuntimeIncidentEvidence(invalid) {
		t.Fatal("invalid contract version accepted")
	}
	invalid = evidence
	invalid.PathSignature = strings.Repeat("x", 1025)
	if validRouteRuntimeIncidentEvidence(invalid) {
		t.Fatal("oversized path signature accepted")
	}
	response := evidence
	response.InvocationStage = routes.InvocationStageResponse
	response.Phase = routes.RoutePhaseAfter
	response.Action = extensionmanifest.RouteActionAfter
	response.Mode = extensionmanifest.RouteModeHTTP
	response.FailureCode = routes.RouteFailureResponseSchemaRejected
	response.CauseClass = "response_schema"
	response.CommitState = routes.RouteCommitFinal
	if !validRouteRuntimeIncidentEvidence(response) {
		t.Fatalf("valid response incident rejected: %#v", response)
	}
}

func TestPostgresRouteRuntimeIncidentAtomicReplayAndResolution(t *testing.T) {
	fixture := newRouteRuntimeIncidentPGFixture(t)
	evidence := routeRuntimeIncidentTestEvidence(t, fixture.actorID)
	evidence.Artifact = fixture.artifact

	record, created, err := fixture.store.CreatePending(fixture.ctx, evidence)
	if err != nil || !created || record.ID <= 0 || record.ExtensionVersionID != fixture.versionID ||
		record.AuditEventID <= 0 || record.LocalResult != RouteRuntimeIncidentPending || record.ResolvedAt != nil {
		t.Fatalf("record=%#v created=%t err=%v", record, created, err)
	}
	replayed, created, err := fixture.store.CreatePending(fixture.ctx, evidence)
	if err != nil || created || replayed.ID != record.ID || replayed.AuditEventID != record.AuditEventID {
		t.Fatalf("replayed=%#v created=%t err=%v", replayed, created, err)
	}
	changed := evidence
	changed.RouteID += ".changed"
	if _, _, err := fixture.store.CreatePending(fixture.ctx, changed); !errors.Is(err, ErrRouteRuntimeIncidentConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	fixture.assertIncidentAuditCount(evidence.IncidentKey, 1, 1)

	resolved, err := fixture.store.Resolve(fixture.ctx, evidence.IncidentKey, RouteRuntimeIncidentQuarantined)
	if err != nil || resolved.LocalResult != RouteRuntimeIncidentQuarantined || resolved.ResolvedAt == nil {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	idempotent, err := fixture.store.Resolve(fixture.ctx, evidence.IncidentKey, RouteRuntimeIncidentQuarantined)
	if err != nil || idempotent.ID != resolved.ID {
		t.Fatalf("idempotent=%#v err=%v", idempotent, err)
	}
	if _, err := fixture.store.Resolve(fixture.ctx, evidence.IncidentKey, RouteRuntimeIncidentFailed); !errors.Is(err, ErrRouteRuntimeIncidentConflict) {
		t.Fatalf("conflicting resolution error=%v", err)
	}
	if _, err := fixture.store.Resolve(fixture.ctx, strings.Repeat("f", 64), RouteRuntimeIncidentFailed); !errors.Is(err, ErrRouteRuntimeIncidentNotFound) {
		t.Fatalf("missing resolution error=%v", err)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_route_runtime_incidents SET route_id = route_id || '.forged'
		WHERE incident_key = $1
	`, evidence.IncidentKey); err == nil {
		t.Fatal("immutable incident identity was updated")
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		DELETE FROM extension_route_runtime_incidents WHERE incident_key = $1
	`, evidence.IncidentKey); err == nil {
		t.Fatal("append-only incident was deleted")
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `TRUNCATE extension_route_runtime_incidents`); err == nil {
		t.Fatal("append-only incident table was truncated")
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM audit_events WHERE id = $1`, record.AuditEventID); err != nil {
		t.Fatalf("generic audit retention could not remove correlation row: %v", err)
	}
	fixture.assertIncidentAuditCount(evidence.IncidentKey, 1, 0)
}

func TestPostgresRouteRuntimeIncidentConcurrentCreateAndResolve(t *testing.T) {
	fixture := newRouteRuntimeIncidentPGFixture(t)
	evidence := routeRuntimeIncidentTestEvidence(t, fixture.actorID)
	evidence.Artifact = fixture.artifact

	const workers = 16
	start := make(chan struct{})
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, created, err := fixture.store.CreatePending(fixture.ctx, evidence)
			if err != nil {
				errs <- err
				return
			}
			results <- created
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	createdCount := 0
	for created := range results {
		if created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d", createdCount)
	}
	fixture.assertIncidentAuditCount(evidence.IncidentKey, 1, 1)

	resolutionStart := make(chan struct{})
	resolveErrors := make(chan error, 2)
	for _, result := range []RouteRuntimeIncidentLocalResult{RouteRuntimeIncidentQuarantined, RouteRuntimeIncidentFailed} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-resolutionStart
			_, err := fixture.store.Resolve(fixture.ctx, evidence.IncidentKey, result)
			resolveErrors <- err
		}()
	}
	close(resolutionStart)
	wait.Wait()
	close(resolveErrors)
	successes, conflicts := 0, 0
	for err := range resolveErrors {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRouteRuntimeIncidentConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("resolution successes=%d conflicts=%d", successes, conflicts)
	}
}

type routeRuntimeIncidentPGFixture struct {
	t         *testing.T
	ctx       context.Context
	admin     *pgxpool.Pool
	pool      *pgxpool.Pool
	store     *PostgresRouteRuntimeIncidentStore
	database  string
	actorID   int64
	versionID int64
	artifact  routes.PluginArtifact
}

func newRouteRuntimeIncidentPGFixture(t *testing.T) *routeRuntimeIncidentPGFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.ConnConfig.Database = "postgres"
	delete(adminConfig.ConnConfig.RuntimeParams, "role")
	delete(adminConfig.ConnConfig.RuntimeParams, "search_path")
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("sforum_route_incident_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	fixture := &routeRuntimeIncidentPGFixture{
		t: t, ctx: ctx, admin: admin, database: database,
	}
	t.Cleanup(fixture.cleanup)

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.Database = database
	delete(config.RuntimeParams, "role")
	delete(config.RuntimeParams, "search_path")
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	applyRouteRuntimeIncidentTestMigrations(t, ctx, db)

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = database
	delete(poolConfig.ConnConfig.RuntimeParams, "role")
	delete(poolConfig.ConnConfig.RuntimeParams, "search_path")
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pool = pool
	fixture.store = NewPostgresRouteRuntimeIncidentStore(pool)
	fixture.artifact = routes.PluginArtifact{
		ExtensionID: "route.incident." + database, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-" + database,
	}
	username := "route_incident_" + database
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username,username_lower,email,email_lower,display_name,status)
		VALUES ($1,$1,$2,$2,'Route Incident Actor','active') RETURNING id
	`, username, username+"@example.test").Scan(&fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id,type,name,status,source,is_system,is_deletable)
		VALUES ($1,'plugin',$1,'enabled','uploaded',false,true)
	`, fixture.artifact.ExtensionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id,version,manifest,package_path,package_digest)
		VALUES ($1,$2,'{}'::jsonb,$3,$4) RETURNING id
	`, fixture.artifact.ExtensionID, fixture.artifact.ExtensionVersion,
		"/tmp/"+database, fixture.artifact.PackageDigest).Scan(&fixture.versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extensions SET active_version_id=$2,status='disabled' WHERE id=$1
	`, fixture.artifact.ExtensionID, fixture.versionID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *routeRuntimeIncidentPGFixture) cleanup() {
	if f.pool != nil {
		f.pool.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := f.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{f.database}.Sanitize()+" WITH (FORCE)"); err != nil {
		f.t.Errorf("drop route incident test database: %v", err)
	}
	f.admin.Close()
}

func applyRouteRuntimeIncidentTestMigrations(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	defer db.Close()
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 202607180036); err != nil {
		t.Fatalf("migrate isolated route incident database: %v", err)
	}
}

func routeRuntimeIncidentTestEvidence(t *testing.T, actorID int64) RouteRuntimeIncidentEvidence {
	t.Helper()
	key, err := NewRouteRuntimeIncidentKey()
	if err != nil {
		t.Fatal(err)
	}
	return RouteRuntimeIncidentEvidence{
		IncidentKey: key, RouteRevision: 7, StepIndex: 0,
		Phase: routes.RoutePhaseHandler, InvocationStage: routes.InvocationStageHandler,
		Action: extensionmanifest.RouteActionAdd, Mode: extensionmanifest.RouteModeStream,
		RouteID: "route.incident.stream", ContractVersion: "route.incident.stream@1",
		Method: "GET", PathSignature: "/s:incident",
		FailureCode: routes.RouteFailureTransportFailed, CauseClass: "runtime_transport",
		RuntimeExecutionObserved: true, ActorID: actorID, ResponseStatus: 200,
		CommitState: routes.RouteCommitResponseStarted,
		Artifact: routes.PluginArtifact{
			ExtensionID: "route.incident.plugin", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-1",
		},
	}
}

func (f *routeRuntimeIncidentPGFixture) assertIncidentAuditCount(key string, incidents, audits int) {
	f.t.Helper()
	var incidentCount, auditCount int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM extension_route_runtime_incidents WHERE incident_key=$1
	`, key).Scan(&incidentCount); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events
		WHERE action='routes.runtime_incident' AND metadata->>'incidentKey'=$1
	`, key).Scan(&auditCount); err != nil {
		f.t.Fatal(err)
	}
	if incidentCount != incidents || auditCount != audits {
		f.t.Fatalf("incident count=%d audit count=%d", incidentCount, auditCount)
	}
}
