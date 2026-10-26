package extensionsruntime

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

// providerSlotSelectionMigrationVersion 与 durable provider-slot selection 表迁移一致。
const providerSlotSelectionMigrationVersion = int64(202607150023)

func TestPostgresProviderSlotSelectionExactArtifactCASAuditAndInvalidation(t *testing.T) {
	fixture := newProviderSlotSelectionIntegrationFixture(t)
	ctx, pool := fixture.ctx, fixture.pool

	registry, owner, provider := providerSelectionFixture(t, "next")
	unique := fmt.Sprintf("provider-slot-%d", time.Now().UnixNano())
	owner.ID, owner.Manifest.ID = unique+".owner", unique+".owner"
	provider.ID, provider.Manifest.ID = unique+".candidate", unique+".candidate"
	owner.Manifest.Providers[0].ID = owner.ID + ".delivery"
	owner.Manifest.Providers[0].ContractVersion = owner.Manifest.Providers[0].ID + "@1"
	owner.Manifest.Providers[0].Slot = owner.ID + ".slot"
	provider.Manifest.Providers[0].ID = provider.ID + ".delivery"
	provider.Manifest.Providers[0].TargetID = owner.Manifest.Providers[0].ID
	provider.Manifest.Providers[0].ContractVersion = owner.Manifest.Providers[0].ContractVersion
	provider.Manifest.Providers[0].Slot = owner.Manifest.Providers[0].Slot
	provider.Manifest.Dependencies[0].ID = owner.ID
	registry = NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}

	actorID, err := insertLifecycleActorFixture(ctx, pool, unique, unique)
	if err != nil {
		t.Fatal(err)
	}
	ownerVersionID := insertProviderSlotSelectionExtension(t, ctx, pool, owner, unique+"-owner")
	providerVersionID := insertProviderSlotSelectionExtension(t, ctx, pool, provider, unique+"-candidate")

	store := NewPostgresProviderSlotSelectionStore(pool)
	api := NewProviderSlotSelectionAPI(registry, store)
	selection, err := api.Select(ctx, owner.Manifest.Providers[0].ID, provider.Manifest.Providers[0].ID, 0, actorID, 71)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ContractVersionID != ownerVersionID || selection.ProviderVersionID != providerVersionID || selection.Revision != 1 {
		t.Fatalf("selection = %#v", selection)
	}
	if _, err := api.Select(ctx, selection.ContractID, selection.CandidateID, 0, actorID, 72); !errors.Is(err, ErrProviderSlotSelectionRevisionConflict) {
		t.Fatalf("stale selection CAS = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET status='disabled' WHERE id=$1`, provider.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Selected(ctx, selection.ContractID); !errors.Is(err, ErrProviderSlotSelectionStale) {
		t.Fatalf("disabled provider selection = %v", err)
	}
	desired, err := store.Desired(ctx, selection.ContractID)
	if err != nil || desired.Revision != 1 {
		t.Fatalf("desired stale selection = %#v, %v", desired, err)
	}
	count, err := api.InvalidateExtension(ctx, InvalidateProviderSlotRequest{
		ExtensionID: provider.ID, ActorUserID: actorID, AuditEventID: 73, ReasonCode: "extension_disabled",
	})
	if err != nil || count != 1 {
		t.Fatalf("invalidate count=%d err=%v", count, err)
	}
	events, err := api.Events(ctx, selection.ContractID, 10)
	if err != nil || len(events) != 2 || events[0].Action != "invalidate" || events[0].PreviousSelection == nil {
		t.Fatalf("events = %#v, %v", events, err)
	}

	// 私有 schema 隔离：cleanup 关闭 pool 之后仍可通过 admin 可靠 DROP。
	if fixture.pool == nil {
		t.Fatal("fixture pool missing before cleanup registration completes")
	}
}

func TestProviderSlotSelectionIntegrationRequiresExplicitTestDatabaseURL(t *testing.T) {
	t.Setenv("SFORUM_TEST_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgres://should-never-be-used/for-integration")
	if url, ok := requireSforumTestDatabaseURL(); ok || url != "" {
		t.Fatalf("setup must refuse without SFORUM_TEST_DATABASE_URL; got url=%q ok=%v", url, ok)
	}
}

type providerSlotSelectionIntegration struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	schema string
}

func newProviderSlotSelectionIntegrationFixture(t *testing.T) *providerSlotSelectionIntegration {
	t.Helper()
	databaseURL, ok := requireSforumTestDatabaseURL()
	if !ok {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("provider_slot_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	var pool *pgxpool.Pool
	var db *sql.DB
	// Cleanup 顺序：先关业务连接，再 DROP 私有 schema，最后关 admin。
	// 不在 DROP 前关闭 admin；不静默吞掉 DROP 错误。
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
			pool = nil
		}
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close provider slot migration db: %v", err)
			}
			db = nil
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop provider slot private schema %s: %v", schema, err)
		}
		admin.Close()
	})

	sqlConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	sqlConfig.RuntimeParams["search_path"] = schema + ",public"
	db = stdlib.OpenDB(*sqlConfig)
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
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed'
			  CHECK (status IN ('installed', 'enabled', 'disabled')),
			source TEXT NOT NULL DEFAULT 'uploaded',
			is_system BOOLEAN NOT NULL DEFAULT FALSE,
			is_deletable BOOLEAN NOT NULL DEFAULT TRUE,
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGSERIAL PRIMARY KEY,
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
	if _, err := provider.ApplyVersion(ctx, providerSlotSelectionMigrationVersion, true); err != nil {
		t.Fatalf("apply provider slot selection migration: %v", err)
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
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}

	// 确认 fixture 落在私有 schema，而不是 public（避免污染宿主启动路径）。
	var selectionNamespace string
	if err := pool.QueryRow(ctx, `
		SELECT n.nspname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'extension_provider_slot_selections'
		  AND n.nspname = current_schema()
	`).Scan(&selectionNamespace); err != nil {
		t.Fatalf("provider slot selection table is not in private schema: %v", err)
	}
	if selectionNamespace != schema {
		t.Fatalf("provider slot selection schema = %q, want %q", selectionNamespace, schema)
	}

	return &providerSlotSelectionIntegration{ctx: ctx, pool: pool, schema: schema}
}

func insertProviderSlotSelectionExtension(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extension extensions.Extension,
	path string,
) int64 {
	t.Helper()
	// enabled 扩展必须写入 exact manifest，避免空 manifest 泄漏到宿主 genesis。
	manifest, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) == 0 || string(manifest) == "null" || string(manifest) == "{}" {
		t.Fatal("enabled provider-slot fixture requires a non-empty exact manifest")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id,type,name,status,source,is_system,is_deletable)
		VALUES ($1,'plugin',$1,'enabled','uploaded',false,true)`, extension.ID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO extension_versions
		(extension_id,version,manifest,package_path,package_digest) VALUES ($1,$2,$3::jsonb,$4,$5) RETURNING id`,
		extension.ID, extension.Version, manifest, "/tmp/"+path, extension.PackageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id=$2 WHERE id=$1`, extension.ID, versionID); err != nil {
		t.Fatal(err)
	}
	return versionID
}

// requireSforumTestDatabaseURL 只认 SFORUM_TEST_DATABASE_URL，绝不回落到 DATABASE_URL。
func requireSforumTestDatabaseURL() (string, bool) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		return "", false
	}
	return databaseURL, true
}
