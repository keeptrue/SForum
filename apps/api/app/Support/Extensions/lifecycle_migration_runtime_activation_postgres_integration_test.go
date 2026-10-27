package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestLifecycleRuntimeActivationRequiresDurableMigrationProof(t *testing.T) {
	fixture := newLifecycleMigrationRuntimeActivationFixture(t, false)
	if err := fixture.journal.PrepareLifecyclePublication(
		fixture.ctx, fixture.publicationRequest, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatal(err)
	}

	err := fixture.journal.CommitLifecyclePublication(
		fixture.ctx, fixture.publicationRequest, LifecycleBoundaryActivate,
	)
	if !errors.Is(err, ErrLifecycleMigrationProofRequired) {
		t.Fatalf("publication without migration proof error=%v", err)
	}
	var blocked lifecycleMigrationBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("publication error lost coordinator mapping: %T", err)
	}
	failure := blocked.LifecycleCoordinatorFailure()
	if failure.Code != "lifecycle.migration_blocked" ||
		failure.Reason != "lifecycle.migration_proof_required" || !failure.Retryable {
		t.Fatalf("publication coordinator failure=%#v", failure)
	}
	fixture.assertLatestRuntimeArtifact(t, fixture.source)
	fixture.assertRuntimePublicationCount(t, 1)
	fixture.assertPublicationUncommitted(t)
}

type lifecycleMigrationRuntimeActivationFixture struct {
	ctx                context.Context
	pool               *pgxpool.Pool
	journal            *PostgresLifecycleBoundaryPublicationJournal
	schema             string
	databaseURL        string
	extensionID        string
	source             extensions.Extension
	target             extensions.Extension
	migrationRequest   LifecycleBoundaryRequest
	publicationRequest LifecycleBoundaryRequest
	initialPublication extensions.PluginRuntimePublication
	identifiers        ExtensionDatabaseIdentifiers
	migrationRole      string
}

func newLifecycleMigrationRuntimeActivationFixture(
	t *testing.T,
	failingMigration bool,
) *lifecycleMigrationRuntimeActivationFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("p12_migration_runtime_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	var pool *pgxpool.Pool
	var db *sql.DB
	fixture := &lifecycleMigrationRuntimeActivationFixture{
		ctx: ctx, schema: schema, databaseURL: databaseURL,
	}
	t.Cleanup(func() {
		fixture.cleanup(t, admin, quotedSchema)
		if pool != nil {
			pool.Close()
		}
		if db != nil {
			_ = db.Close()
		}
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	})

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db = stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL
		);
		CREATE TABLE extension_trust_grants (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed',
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL,
			package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		ALTER TABLE extensions
		  ADD CONSTRAINT extensions_active_version_fk
		  FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
	`); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{
		202607140001,
		202607140004,
		202607140007,
		202607140012,
		202607140013,
		202607160027,
		202607160030,
		202607160031,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	poolConfig.MaxConns = 12
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pool = pool
	fixture.journal = NewPostgresLifecycleBoundaryPublicationJournal(pool)

	extensionID := fmt.Sprintf("p12.rollout.%d", time.Now().UnixNano())
	fixture.extensionID = extensionID
	fixture.identifiers, err = ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	migrationRelativePath := "migrations/001.sql"
	migrationPath := filepath.Join(targetRoot, filepath.FromSlash(migrationRelativePath))
	if err := os.MkdirAll(filepath.Dir(migrationPath), 0o700); err != nil {
		t.Fatal(err)
	}
	migrationBody := "CREATE TABLE rollout_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL);\n" +
		"INSERT INTO rollout_probe (id, note) VALUES (1, 'once');\n"
	if failingMigration {
		migrationBody += "INSERT INTO rollout_missing_table (id) VALUES (1);\n"
	}
	if err := os.WriteFile(migrationPath, []byte(migrationBody), 0o600); err != nil {
		t.Fatal(err)
	}
	migrationSum := sha256.Sum256([]byte(migrationBody))
	migrationDigest := hex.EncodeToString(migrationSum[:])
	sourceDigest := sha256.Sum256([]byte(extensionID + ":source"))
	targetDigest := sha256.Sum256([]byte(extensionID + ":target"))

	template := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 4)
	source := *template.SourceExtension
	target := template.TargetExtension
	prepareArtifact := func(extension *extensions.Extension, version string, versionID int64, digest [32]byte, root string) {
		extension.ID = extensionID
		extension.Name = "P12 migration runtime fixture"
		extension.Version = version
		extension.ActiveVersionID = versionID
		extension.PackageDigest = hex.EncodeToString(digest[:])
		extension.PackagePath = root
		extension.Status = extensions.StatusEnabled
		extension.Manifest.ID = extensionID
		extension.Manifest.Name = extension.Name
		extension.Manifest.Version = version
		extension.Manifest.Backend.Digest = extension.PackageDigest
		extension.Manifest.PackageFiles[0].ID = extensionID + ".backend"
		extension.Manifest.PackageFiles[0].Digest = extension.PackageDigest
	}
	prepareArtifact(&source, "1.0.0", 101, sourceDigest, sourceRoot)
	prepareArtifact(&target, "2.0.0", 102, targetDigest, targetRoot)
	migrationID := extensionID + ".migration.initial"
	target.Manifest.Migrations = []extensions.ManifestMigration{{
		ID: migrationID, ContractVersion: migrationID + "@1",
		Path: migrationRelativePath, Digest: migrationDigest, Transaction: "required",
	}}
	target.Manifest.Database = &extensions.ManifestDatabase{
		ContractVersion: extensionID + ".database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema},
		Schema:          "p12_rollout",
		Role:            "p12_rollout",
		Retention: extensionmanifest.ManifestRetention{
			OnDisable: "retain", OnUninstall: "retain",
		},
	}
	sourceDatabase := *target.Manifest.Database
	sourceDatabase.Grants = append([]string(nil), target.Manifest.Database.Grants...)
	source.Manifest.Database = &sourceDatabase
	target.Manifest.PackageFiles = append(target.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: extensionID + ".migration.file", Kind: "migration",
		Path: migrationRelativePath, Digest: migrationDigest,
	})
	if err := extensionmanifest.Validate(source.Manifest); err != nil {
		t.Fatalf("source manifest: %v", err)
	}
	if err := extensionmanifest.Validate(target.Manifest); err != nil {
		t.Fatalf("target manifest: %v", err)
	}
	fixture.source, fixture.target = source, target

	username := strings.ReplaceAll(extensionID, ".", "_")
	actorID, err := insertLifecycleActorFixture(ctx, pool, username, "P12 migration runtime actor")
	if err != nil {
		t.Fatal(err)
	}
	for _, extension := range []extensions.Extension{source, target} {
		manifestJSON, err := json.Marshal(extension.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		if extension.ActiveVersionID == source.ActiveVersionID {
			if _, err := pool.Exec(ctx, `
				INSERT INTO extensions (id, type, name, status)
				VALUES ($1, 'plugin', $2, 'enabled')
			`, extensionID, extension.Name); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO extension_versions (
				id, extension_id, version, manifest, package_path, package_digest
			) VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		`, extension.ActiveVersionID, extensionID, extension.Version,
			manifestJSON, extension.PackagePath, extension.PackageDigest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, extensionID, source.ActiveVersionID); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture.initialPublication, err = extensions.PublishPluginRuntimePublicationTransitionTx(ctx, tx,
		extensions.PluginRuntimePublicationTransition{
			Target: source, Activate: true, Reason: extensions.PluginRuntimePublicationEnable,
			ActorUserID: actorID,
		},
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	migrationRequest := template
	migrationRequest.SourceExtension = &source
	migrationRequest.TargetExtension = target
	migrationRequest.SourceBinding = extensions.LifecycleRuntimeBinding{
		ExtensionID: source.ID, ExtensionVersion: source.Version,
		PackageDigest: source.PackageDigest, VersionID: source.ActiveVersionID,
		RuntimeInstanceID: "source-runtime",
	}
	migrationRequest.TargetBinding = extensions.LifecycleRuntimeBinding{
		ExtensionID: target.ID, ExtensionVersion: target.Version,
		PackageDigest: target.PackageDigest, VersionID: target.ActiveVersionID,
		RuntimeInstanceID: "target-runtime",
	}
	migrationRequest.ActorUserID = actorID
	migrationRequest.AuditEventID = time.Now().UnixNano()
	migrationRequest.Attempt = 1
	migrationRequest.Position = 4
	migrationRequest.StepID = "lifecycle.upgrade.04.host.migrating"
	operationKey := "p12-rollout:" + extensionID
	fingerprint := sha256.Sum256([]byte(operationKey))
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, current_step_id, plan_version,
			idempotency_key, request_fingerprint, authority_type, authority_snapshot,
			requested_by_user_id, audit_event_id
		) VALUES ($1, $2, $3, '{}'::jsonb,
		          'upgrade', 'migrating', $4, 'p12.rollout@1',
		          $5, $6, 'builtin', '{}'::jsonb, $7, $8)
		RETURNING id
	`, extensionID, target.Version, target.PackageDigest, migrationRequest.StepID,
		operationKey, hex.EncodeToString(fingerprint[:]), actorID, migrationRequest.AuditEventID,
	).Scan(&migrationRequest.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, actor_user_id, audit_event_id, started_at
		) VALUES ($1, $2, 'host.gate', 'p12.rollout@1', 1,
		          'running', $3, $4, statement_timestamp())
	`, migrationRequest.OperationID, migrationRequest.StepID,
		actorID, migrationRequest.AuditEventID); err != nil {
		t.Fatal(err)
	}
	fixture.migrationRequest = migrationRequest
	publicationRequest := cloneLifecycleBoundaryRequest(migrationRequest)
	publicationRequest.Position = 8
	publicationRequest.StepID = "lifecycle.upgrade.08.host.enabled"
	fixture.publicationRequest = publicationRequest
	plan, err := lifecycleMigrationPlanFor(migrationRequest, LifecycleBoundaryMigrationUpgrade, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture.migrationRole, err = ExtensionDatabaseMigrationRoleFor(extensionID, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *lifecycleMigrationRuntimeActivationFixture) assertRuntimePublicationCount(t *testing.T, want int) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM plugin_runtime_publications`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("runtime publication count=%d want=%d", count, want)
	}
}

func (f *lifecycleMigrationRuntimeActivationFixture) assertLatestRuntimeArtifact(
	t *testing.T,
	want extensions.Extension,
) {
	t.Helper()
	var extensionID, version, digest string
	var versionID int64
	if err := f.pool.QueryRow(f.ctx, `
		SELECT member.extension_id, member.extension_version_id,
		       member.extension_version, member.package_digest
		FROM plugin_runtime_publication_members AS member
		WHERE member.publication_revision = (
		  SELECT max(revision) FROM plugin_runtime_publications
		)
	`).Scan(&extensionID, &versionID, &version, &digest); err != nil {
		t.Fatal(err)
	}
	if extensionID != want.ID || versionID != want.ActiveVersionID ||
		version != want.Version || digest != want.PackageDigest {
		t.Fatalf("latest runtime artifact=%s/%d/%s/%s want=%s/%d/%s/%s",
			extensionID, versionID, version, digest,
			want.ID, want.ActiveVersionID, want.Version, want.PackageDigest)
	}
}

func (f *lifecycleMigrationRuntimeActivationFixture) cleanup(
	t *testing.T,
	admin *pgxpool.Pool,
	quotedSchema string,
) {
	t.Helper()
	if f.pool == nil || f.extensionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, role := range []string{f.identifiers.RuntimeRole, f.migrationRole} {
		if role != "" {
			_, _ = admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, role)
		}
	}
	if f.identifiers.Schema != "" {
		_, _ = admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{f.identifiers.Schema}.Sanitize()+` CASCADE`)
	}
	for _, role := range []string{f.migrationRole, f.identifiers.RuntimeRole, f.identifiers.OwnerRole} {
		if role == "" {
			continue
		}
		quoted := pgx.Identifier{role}.Sanitize()
		_, _ = admin.Exec(ctx, `DROP OWNED BY `+quoted)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+quoted)
	}
	_ = quotedSchema
}
