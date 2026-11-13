package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

type bootstrapRecordingProjector struct {
	synced []string
	stale  []string
}

func (p *bootstrapRecordingProjector) SyncManifest(_ context.Context, manifest extensionmanifest.Manifest) (int, error) {
	p.synced = append(p.synced, manifest.ID)
	return 0, nil
}

func (p *bootstrapRecordingProjector) Disable(context.Context, string) error { return nil }

func (p *bootstrapRecordingProjector) DeprojectStale(_ context.Context, extensionIDs []string) (int, error) {
	p.stale = append(p.stale, extensionIDs...)
	return len(extensionIDs), nil
}

func seedReconcileExtension(t *testing.T, pool *pgxpool.Pool, id, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name, status) VALUES ($1, 'plugin', $1, $2)`, id, status); err != nil {
		t.Fatalf("seed extension: %v", err)
	}
	manifest, err := json.Marshal(map[string]any{
		"id": id, "version": "1.0.0", "type": "plugin",
		"contributions": []map[string]any{{
			"point": extensionmanifest.PointForumSettingsProfileTabs,
			"id":    id + ".tab",
			"label": map[string]string{"en-US": "Tab"},
			"payload": map[string]any{
				"type": "fields",
				"fields": []map[string]any{{
					"key": "theme", "label": "Theme", "type": "string",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', $2::jsonb, $3, $4) RETURNING id`,
		id, string(manifest), "/tmp/"+id, strings.Repeat("b", 64)).Scan(&versionID); err != nil {
		t.Fatalf("seed extension version: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, id, versionID); err != nil {
		t.Fatalf("bind active version: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM extensions WHERE id = $1`, id)
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_field_definitions WHERE owner_extension_id = $1`, id)
	})
}

// 启动 reconcile 是兜底：库里手工造成「enabled 但没有字段定义」的状态，
// 启动补投影之后定义必须出现（不能只靠 enable 漏斗）。
func TestProjectEnabledSettingsSurfacesHealsMissingDefinitions(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)
	id := fmt.Sprintf("demo.reconcile.%d", time.Now().UnixNano())
	seedReconcileExtension(t, pool, id, extensions.StatusEnabled)

	store := extensions.NewPostgresStore(pool)
	projector := providers.NewSettingsSurfaceFieldSync(entitymeta.NewService(entitymeta.NewPostgresStore(pool)))
	projectEnabledSettingsSurfaces(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), store, projector)

	var definitions int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_field_definitions WHERE owner_extension_id = $1`, id).Scan(&definitions); err != nil {
		t.Fatalf("count definitions: %v", err)
	}
	if definitions == 0 {
		t.Fatal("startup reconcile must create the missing field definitions")
	}
}

// 只对 enabled 的插件补投影：停用插件不该在启动时被「复活」字段。
func TestProjectEnabledSettingsSurfacesSkipsDisabledPlugins(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)
	enabledID := fmt.Sprintf("demo.reconcile.on.%d", time.Now().UnixNano())
	disabledID := fmt.Sprintf("demo.reconcile.off.%d", time.Now().UnixNano())
	seedReconcileExtension(t, pool, enabledID, extensions.StatusEnabled)
	seedReconcileExtension(t, pool, disabledID, extensions.StatusDisabled)

	projector := &bootstrapRecordingProjector{}
	projectEnabledSettingsSurfaces(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)),
		extensions.NewPostgresStore(pool), projector)
	if len(projector.synced) != 1 || projector.synced[0] != enabledID {
		t.Fatalf("startup reconcile must project enabled plugins only, got %#v", projector.synced)
	}
}

// 反方向自愈：插件已停用（或带外停用、Disable 去投影失败）后，字段定义仍
// enabled=true 会让 public 历史值继续匿名可读。启动 reconcile 必须收敛它，
// 且不能碰仍启用的插件。
func TestProjectEnabledSettingsSurfacesDeprojectsFieldsOfDisabledPlugins(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)

	disabledID := fmt.Sprintf("demo.reconcile.stale.%d", time.Now().UnixNano())
	enabledID := fmt.Sprintf("demo.reconcile.live.%d", time.Now().UnixNano())
	seedReconcileExtension(t, pool, disabledID, extensions.StatusDisabled)
	seedReconcileExtension(t, pool, enabledID, extensions.StatusEnabled)

	metaService := entitymeta.NewService(entitymeta.NewPostgresStore(pool))
	projector := providers.NewSettingsSurfaceFieldSync(metaService)
	// 手工造坏状态：停用插件先被投影过一次（enabled=true）。
	if _, err := projector.SyncManifest(ctx, reconcileManifestFor(t, pool, disabledID)); err != nil {
		t.Fatalf("seed projection: %v", err)
	}

	projectEnabledSettingsSurfaces(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)),
		extensions.NewPostgresStore(pool), projector)

	var disabledEnabled int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_field_definitions
		WHERE owner_extension_id = $1 AND enabled`, disabledID).Scan(&disabledEnabled); err != nil {
		t.Fatalf("count disabled-plugin fields: %v", err)
	}
	if disabledEnabled != 0 {
		t.Fatalf("startup reconcile must deproject fields of a disabled plugin, %d still enabled", disabledEnabled)
	}
	var liveEnabled int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_field_definitions
		WHERE owner_extension_id = $1 AND enabled`, enabledID).Scan(&liveEnabled); err != nil {
		t.Fatalf("count enabled-plugin fields: %v", err)
	}
	if liveEnabled == 0 {
		t.Fatal("startup reconcile must leave enabled plugins' fields alone")
	}
}

// reconcileManifestFor 读回 seedReconcileExtension 写入的 manifest。
func reconcileManifestFor(t *testing.T, pool *pgxpool.Pool, id string) extensionmanifest.Manifest {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT extension_versions.manifest FROM extensions
		JOIN extension_versions ON extension_versions.id = extensions.active_version_id
		WHERE extensions.id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest extensionmanifest.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}
