package identity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresStoreListPermissionsHidesTombstonedExtensionPermissions(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("permission_catalog_visibility_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE permissions (
			key TEXT PRIMARY KEY,
			module TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			label_locales JSONB NOT NULL DEFAULT '{}'::jsonb,
			description_locales JSONB NOT NULL DEFAULT '{}'::jsonb
		);
		CREATE TABLE extension_permission_catalog (
			permission_key TEXT PRIMARY KEY
		);
		CREATE TABLE extension_identity_registry_declarations (
			identity_kind TEXT NOT NULL,
			stable_id TEXT NOT NULL,
			revision BIGINT NOT NULL,
			registry_state TEXT NOT NULL,
			PRIMARY KEY (identity_kind, stable_id, revision)
		);

		INSERT INTO permissions (
			key, module, label, description, label_locales, description_locales
		) VALUES
			('role.manage', 'identity', 'Manage roles', 'Manage role permissions.', '{}'::jsonb, '{}'::jsonb),
			('fixture.active.manage', 'extension', 'Manage active fixture', 'Manage active fixture.',
			 '{"zh-CN":"管理启用的测试扩展"}'::jsonb,
			 '{"zh-CN":"管理当前启用的测试扩展。"}'::jsonb),
			('fixture.removed.manage', 'extension', '', 'Manage removed fixture.', '{}'::jsonb, '{}'::jsonb);

		INSERT INTO extension_permission_catalog (permission_key) VALUES
			('fixture.active.manage'),
			('fixture.removed.manage');

		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, revision, registry_state
		) VALUES
			('permission', 'fixture.active.manage', 1, 'active'),
			('permission', 'fixture.removed.manage', 1, 'active'),
			('permission', 'fixture.removed.manage', 2, 'tombstone');
	`); err != nil {
		t.Fatal(err)
	}

	permissions, err := NewPostgresStore(pool).ListPermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 2 {
		t.Fatalf("permissions = %#v", permissions)
	}
	if permissions[0].Key != "fixture.active.manage" ||
		permissions[0].LabelLocales["zh-CN"] != "管理启用的测试扩展" ||
		permissions[1].Key != "role.manage" {
		t.Fatalf("permissions = %#v", permissions)
	}
}
