package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestPostgresRecoveryRepositoryPublishesImmutableFullSetsWithoutPackageReads(t *testing.T) {
	fixture := newRecoveryPostgresFixture(t)
	ctx := fixture.ctx
	builtinID := "recovery.builtin"
	firstID := "recovery.first"
	secondID := "recovery.second"
	for _, item := range []struct {
		id, source string
		system     bool
	}{
		{builtinID, "builtin", true},
		{firstID, "uploaded", false},
		{secondID, "uploaded", false},
	} {
		if err := insertRecoveryFixture(ctx, fixture.pool, item.id, item.source, item.system); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO mail_provider_selection (slot, extension_id)
		VALUES ('mail.provider', $1)
	`, firstID); err != nil {
		t.Fatal(err)
	}

	repository := &postgresRecoveryRepository{pool: fixture.pool}
	if _, err := repository.Disable(ctx, builtinID); !errors.Is(err, errRecoveryProtected) {
		t.Fatalf("disable protected extension: %v", err)
	}
	if _, err := repository.QuarantineProtected(
		ctx, firstID, "1.0.0", strings.Repeat("a", 64),
	); !errors.Is(err, errRecoveryNotProtected) {
		t.Fatalf("quarantine ordinary extension: %v", err)
	}
	if _, err := repository.QuarantineProtected(
		ctx, builtinID, "1.0.0", strings.Repeat("b", 64),
	); !errors.Is(err, errRecoveryArtifactMismatch) {
		t.Fatalf("quarantine wrong built-in artifact: %v", err)
	}
	disabled, err := repository.Disable(ctx, firstID)
	if err != nil || disabled.Status != "disabled" {
		t.Fatalf("disable first=%#v err=%v", disabled, err)
	}
	firstRecovery := latestRecoveryPublication(t, fixture.pool)
	if firstRecovery.Reason != extensions.PluginRuntimePublicationRecovery ||
		firstRecovery.ActorUserID != 0 {
		t.Fatalf("first recovery=%+v", firstRecovery)
	}
	assertRecoveryPublicationMembers(t, firstRecovery, builtinID, secondID)

	var selectedMail int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM mail_provider_selection WHERE extension_id = $1
	`, firstID).Scan(&selectedMail); err != nil || selectedMail != 0 {
		t.Fatalf("mail provider selection count=%d err=%v", selectedMail, err)
	}
	items, err := repository.DisableAllThirdParty(ctx)
	if err != nil || !recoveryItemsContain(items, firstID) || !recoveryItemsContain(items, secondID) {
		t.Fatalf("disable all items=%#v err=%v", items, err)
	}
	secondRecovery := latestRecoveryPublication(t, fixture.pool)
	if secondRecovery.Revision <= firstRecovery.Revision ||
		secondRecovery.Reason != extensions.PluginRuntimePublicationRecovery ||
		secondRecovery.ActorUserID != 0 {
		t.Fatalf("first=%+v second=%+v", firstRecovery, secondRecovery)
	}
	assertRecoveryPublicationMembers(t, secondRecovery, builtinID)

	var builtinStatus string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM extensions WHERE id = $1
	`, builtinID).Scan(&builtinStatus); err != nil || builtinStatus != "enabled" {
		t.Fatalf("builtin status=%q err=%v", builtinStatus, err)
	}
	var audits int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.cli_recovery'
		  AND (metadata->'extensionIds') ?| $1::text[]
	`, []string{firstID, secondID}).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("recovery audits=%d err=%v", audits, err)
	}
	var publicationCount int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM plugin_runtime_publications
	`).Scan(&publicationCount); err != nil || publicationCount != 3 {
		t.Fatalf("publication count=%d err=%v", publicationCount, err)
	}
}

func TestPostgresRecoveryRepositoryQuarantinesExactProtectedArtifact(t *testing.T) {
	fixture := newRecoveryPostgresFixture(t)
	const builtinID = "recovery.builtin.exact"
	if err := insertRecoveryFixture(fixture.ctx, fixture.pool, builtinID, "builtin", true); err != nil {
		t.Fatal(err)
	}

	repository := &postgresRecoveryRepository{pool: fixture.pool}
	quarantined, err := repository.QuarantineProtected(
		fixture.ctx, builtinID, "1.0.0", strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined.Status != "disabled" || quarantined.ID != builtinID {
		t.Fatalf("quarantined=%#v", quarantined)
	}
	publication := latestRecoveryPublication(t, fixture.pool)
	if publication.Reason != extensions.PluginRuntimePublicationRecovery ||
		publication.MemberCount != 0 || len(publication.Members) != 0 {
		t.Fatalf("publication=%+v", publication)
	}
	var mode string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT metadata->>'mode' FROM audit_events
		WHERE action = 'extension.cli_recovery'
		ORDER BY id DESC LIMIT 1
	`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "quarantine_protected_exact" {
		t.Fatalf("audit mode=%q", mode)
	}
}

func TestPostgresRecoveryRepositoryCommitFailureRollsBackStateAndPublication(t *testing.T) {
	fixture := newRecoveryPostgresFixture(t)
	const extensionID = "recovery.rollback"
	if err := insertRecoveryFixture(
		fixture.ctx, fixture.pool, extensionID, "uploaded", false,
	); err != nil {
		t.Fatal(err)
	}
	// The recovery audit is written before the immutable full-set. A deferred
	// FK proves that a commit-time failure rolls both of them and mutable status
	// back together instead of exposing a partial recovery decision.
	if _, err := fixture.pool.Exec(fixture.ctx, `
		CREATE TABLE allowed_audit_actions (action TEXT PRIMARY KEY);
		ALTER TABLE audit_events ADD CONSTRAINT audit_events_action_fk
			FOREIGN KEY (action) REFERENCES allowed_audit_actions(action)
			DEFERRABLE INITIALLY DEFERRED;
	`); err != nil {
		t.Fatal(err)
	}

	repository := &postgresRecoveryRepository{pool: fixture.pool}
	if _, err := repository.Disable(fixture.ctx, extensionID); err == nil {
		t.Fatal("commit-time failure was accepted")
	}
	var status string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status FROM extensions WHERE id = $1
	`, extensionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "enabled" {
		t.Fatalf("rolled-back status=%q", status)
	}
	for name, query := range map[string]string{
		"audit":       `SELECT count(*) FROM audit_events`,
		"publication": `SELECT count(*) FROM plugin_runtime_publications`,
	} {
		var count int
		if err := fixture.pool.QueryRow(fixture.ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count=%d", name, count)
		}
	}
}

func recoveryItemsContain(items []recoveryExtension, extensionID string) bool {
	for _, item := range items {
		if item.ID == extensionID {
			return true
		}
	}
	return false
}

func insertRecoveryFixture(ctx context.Context, pool *pgxpool.Pool, id, source string, system bool) error {
	backendDigest := strings.Repeat("e", 64)
	manifest := extensions.Manifest{
		ManifestVersion: 3,
		ID:              id, Name: id, Description: "CLI recovery PostgreSQL fixture.",
		URL: "https://example.com/recovery-fixture", Author: extensions.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
		Backend: extensions.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: backendDigest, HostAPIVersion: "sforum.host@2",
		},
		PackageFiles: []extensions.ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: backendDigest,
		}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var versionID int64
	if _, err := tx.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', $1, 'enabled', $2, $3, NOT $3)
	`, id, source, system); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', $2::jsonb, '/missing/malformed/package', $3)
		RETURNING id
	`, id, string(manifestJSON), strings.Repeat("a", 64)).Scan(&versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, id, versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func latestRecoveryPublication(t *testing.T, pool *pgxpool.Pool) extensions.PluginRuntimePublication {
	t.Helper()
	publication, err := extensions.NewPostgresStore(pool).LatestPluginRuntimePublication(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func assertRecoveryPublicationMembers(
	t *testing.T,
	publication extensions.PluginRuntimePublication,
	extensionIDs ...string,
) {
	t.Helper()
	if len(publication.Members) != len(extensionIDs) {
		t.Fatalf("members=%+v want ids=%v", publication.Members, extensionIDs)
	}
	want := make(map[string]struct{}, len(extensionIDs))
	for _, extensionID := range extensionIDs {
		want[extensionID] = struct{}{}
	}
	for _, member := range publication.Members {
		if _, ok := want[member.ExtensionID]; !ok {
			t.Fatalf("unexpected member=%+v want ids=%v", member, extensionIDs)
		}
	}
}

type recoveryPostgresFixture struct {
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	schema string
}

func newRecoveryPostgresFixture(t *testing.T) *recoveryPostgresFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("recovery_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cleanupSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	createRecoveryBaseSchema(t, db, ctx, cleanupSchema, admin)

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607160027, true); err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatalf("apply plugin runtime publication migration: %v", err)
	}
	if err := db.Close(); err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &recoveryPostgresFixture{ctx: ctx, admin: admin, pool: pool, schema: schema}
	t.Cleanup(fixture.cleanup)
	return fixture
}

func createRecoveryBaseSchema(
	t *testing.T,
	db *sql.DB,
	ctx context.Context,
	cleanupSchema func(),
	admin *pgxpool.Pool,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed',
			source TEXT NOT NULL,
			is_system BOOLEAN NOT NULL DEFAULT false,
			is_deletable BOOLEAN NOT NULL DEFAULT true,
			active_version_id BIGINT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
		);
		CREATE TABLE extension_versions (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL,
			package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		ALTER TABLE extensions ADD CONSTRAINT extensions_active_version_fk
			FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
		CREATE TABLE mail_provider_selection (
			slot TEXT PRIMARY KEY,
			extension_id TEXT NOT NULL
		);
		CREATE TABLE extension_events (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL,
			action TEXT NOT NULL,
			message TEXT NOT NULL
		);
		CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb
		);
	`); err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
}

func (f *recoveryPostgresFixture) cleanup() {
	f.pool.Close()
	_, _ = f.admin.Exec(
		context.Background(),
		"DROP SCHEMA IF EXISTS "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE",
	)
	f.admin.Close()
}
