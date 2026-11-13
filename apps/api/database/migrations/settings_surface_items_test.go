package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const settingsSurfaceItemsMigration = "202611130001_settings_surface_items.sql"

// TestSettingsSurfaceItemsMigrationDeclaresSparseOverlay 固定设置面治理表的
// 结构契约：只有覆盖项（无行=manifest 默认，删行=恢复默认），tab 与字段共用
// 一张表（item_key 空串 = 整个 tab），唯一键四元组，Down 对称拆除。
func TestSettingsSurfaceItemsMigrationDeclaresSparseOverlay(t *testing.T) {
	body, err := fs.ReadFile(Files(), settingsSurfaceItemsMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("settings surface items migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")

	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		"CREATE TABLE IF NOT EXISTS settings_surface_items",
		"id BIGSERIAL PRIMARY KEY",
		"extension_id TEXT NOT NULL CHECK (extension_id <> '')",
		"point TEXT NOT NULL CHECK (point <> '')",
		"contribution_id TEXT NOT NULL CHECK (contribution_id <> '')",
		"item_key TEXT NOT NULL DEFAULT '' CHECK (item_key = '' OR item_key ~ '^[a-z][a-z0-9_-]{1,40}$')",
		"enabled BOOLEAN NOT NULL DEFAULT TRUE",
		"sort_order INTEGER",
		"updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"CONSTRAINT settings_surface_items_unique UNIQUE (extension_id, point, contribution_id, item_key)",
		"CREATE INDEX IF NOT EXISTS settings_surface_items_point_idx ON settings_surface_items (point, extension_id, contribution_id, item_key)",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("settings surface items Up is missing %q", clause)
		}
	}

	// 治理覆盖不是产品数据：不为它建稳定读视图（注释里提到这件事是刻意的）。
	if strings.Contains(up, "CREATE VIEW") || strings.Contains(up, "CREATE OR REPLACE VIEW") {
		t.Fatal("settings surface items must not expose a stable core view")
	}

	for _, clause := range []string{
		"DROP INDEX IF EXISTS settings_surface_items_point_idx",
		"DROP TABLE IF EXISTS settings_surface_items",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("settings surface items Down is missing %q", clause)
		}
	}
}
