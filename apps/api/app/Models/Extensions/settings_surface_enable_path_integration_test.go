package extensions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	providers "github.com/keeptrue/sforum/apps/api/app/Providers"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// dev 零命令的启用路径最终落在 `PostgresStore.Enable`（installed → enabled）或
// 生命周期状态发布，两者共用同一个投影器。这条测试用**真实投影器**（不是记录器）
// 断言：enable 返回时 entity_field_definitions 里已经有该插件的字段——
// 不允许靠重启 reconcile 补齐。
func TestExtensionEnableProjectsRealFieldDefinitions(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)

	id := fmt.Sprintf("demo.dev.auto.%d", time.Now().UnixNano())
	manifest := extensionmanifest.Manifest{
		ID:              id,
		Name:            id,
		Description:     "dev auto enable projection fixture.",
		URL:             "https://example.com/" + id,
		Author:          extensionmanifest.ManifestAuthor{Name: "SForum"},
		Version:         "1.0.0",
		Type:            extensionmanifest.TypePlugin,
		SForumVersion:   ">=0.1.0",
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		Contributions: []extensionmanifest.ManifestContribution{{
			Point: extensionmanifest.PointForumSettingsProfileTabs,
			ID:    id + ".tab",
			Order: 10,
			Label: map[string]string{"en-US": "Preferences"},
			Payload: json.RawMessage(`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"string","visibility":"owner"}
			]}`),
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name, status) VALUES ($1,'plugin',$1,'installed')`, id); err != nil {
		t.Fatalf("seed extension: %v", err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1,'1.0.0',$2::jsonb,$3,$4) RETURNING id`,
		id, string(raw), "/tmp/"+id, strings.Repeat("c", 64)).Scan(&versionID); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, id, versionID); err != nil {
		t.Fatalf("bind version: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_field_definitions WHERE owner_extension_id = $1`, id)
		_, _ = pool.Exec(cleanup, `DELETE FROM extensions WHERE id = $1`, id)
	})

	// 真实投影器：写 entity-meta，而不是只记录调用。
	projector := providers.NewSettingsSurfaceFieldSync(entitymeta.NewService(entitymeta.NewPostgresStore(pool)))
	store := extensions.NewPostgresStore(pool).WithSettingsSurfaceFields(projector)

	if _, err := store.Enable(ctx, id, extensions.TypePlugin); err != nil {
		t.Fatalf("enable: %v", err)
	}

	var definitions int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_field_definitions WHERE owner_extension_id = $1`, id).Scan(&definitions); err != nil {
		t.Fatalf("count definitions: %v", err)
	}
	// 夹具恰好声明 1 个字段：既抓「没投影」，也抓「重复投影」。
	if definitions != 1 {
		t.Fatalf("enable must project exactly the declared settings field, got %d", definitions)
	}
	var enabled int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_field_definitions WHERE owner_extension_id = $1 AND enabled`, id).Scan(&enabled); err != nil {
		t.Fatalf("count enabled definitions: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("projected definition must be enabled for an enabled plugin, got %d", enabled)
	}
}
