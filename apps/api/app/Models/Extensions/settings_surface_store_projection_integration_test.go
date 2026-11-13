package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type recordingProjector struct {
	synced   []string
	disabled []string
	err      error
}

func (p *recordingProjector) SyncManifest(_ context.Context, manifest Manifest) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.synced = append(p.synced, manifest.ID)
	return len(manifest.Contributions), nil
}

func (p *recordingProjector) Disable(_ context.Context, extensionID string) error {
	p.disabled = append(p.disabled, extensionID)
	return nil
}

func (p *recordingProjector) DeprojectStale(context.Context, []string) (int, error) { return 0, nil }

func seedProjectableExtension(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status) VALUES ($1, 'plugin', $1, 'installed')`, id); err != nil {
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
		VALUES ($1, '1.0.0', $2::jsonb, $3, $4)
		RETURNING id`,
		id, string(manifest), "/tmp/"+id, strings.Repeat("a", 64)).Scan(&versionID); err != nil {
		t.Fatalf("seed extension version: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, id, versionID); err != nil {
		t.Fatalf("bind active version: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM extensions WHERE id = $1`, id)
	})
}

func projectableStore(t *testing.T) (*PostgresStore, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewPostgresStore(pool), pool
}

// 投影落在 store 的 enable 原语里：dev 零命令、CLI、SystemTier、legacy 发布
// 都经过它，因此不存在「enabled 但字段没投影」的旁路。
func TestPostgresStoreEnableProjectsSettingsFields(t *testing.T) {
	store, pool := projectableStore(t)
	projector := &recordingProjector{}
	store.WithSettingsSurfaceFields(projector)
	id := fmt.Sprintf("demo.project.%d", time.Now().UnixNano())
	seedProjectableExtension(t, pool, id)

	enabled, err := store.Enable(context.Background(), id, TypePlugin)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.Status != StatusEnabled {
		t.Fatalf("status = %q", enabled.Status)
	}
	if len(projector.synced) != 1 || projector.synced[0] != id {
		t.Fatalf("enable must project the active manifest, got %#v", projector.synced)
	}
}

// 投影失败 → 整笔 enable 失败、status 不落 enabled（不存在「启用成功但表单存不进去」）。
func TestPostgresStoreEnableBlocksOnProjectionFailure(t *testing.T) {
	store, pool := projectableStore(t)
	store.WithSettingsSurfaceFields(&recordingProjector{err: errors.New("boom")})
	id := fmt.Sprintf("demo.project.fail.%d", time.Now().UnixNano())
	seedProjectableExtension(t, pool, id)

	if _, err := store.Enable(context.Background(), id, TypePlugin); !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("enable = %v, want ErrPreflightFailed", err)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM extensions WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == StatusEnabled {
		t.Fatal("failed projection must not leave the extension enabled")
	}
}

// 真语义的投影器：Disable 直接落到 entity-meta，用来断言「停用后定义确实
// enabled=false 且值还在」——这是隐私边界，只验「失败不阻断」不算覆盖。
type entityMetaDisableProjector struct{ meta *entitymeta.Service }

func (p entityMetaDisableProjector) SyncManifest(context.Context, Manifest) (int, error) {
	return 0, nil
}

func (p entityMetaDisableProjector) Disable(ctx context.Context, extensionID string) error {
	return p.meta.SetEnabledByOwnerExtension(ctx, extensionID, false)
}

func (p entityMetaDisableProjector) DeprojectStale(context.Context, []string) (int, error) {
	return 0, nil
}

func TestPostgresStoreDisableDeprojectsSettingsFields(t *testing.T) {
	store, pool := projectableStore(t)
	ctx := context.Background()
	id := fmt.Sprintf("demo.project.disable.%d", time.Now().UnixNano())
	seedProjectableExtension(t, pool, id)

	metaService := entitymeta.NewService(entitymeta.NewPostgresStore(pool))
	store.WithSettingsSurfaceFields(entityMetaDisableProjector{meta: metaService})

	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1) RETURNING id`,
		"surface_disable_"+fmt.Sprint(time.Now().UnixNano()), "surface_disable_"+fmt.Sprint(time.Now().UnixNano())+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	fieldKey := extensionmanifest.SettingsSurfaceFieldKey(id, "theme")
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_meta_values WHERE entity_type = 'user' AND entity_id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_field_definitions WHERE owner_extension_id = $1`, id)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := metaService.EnsureOwnerExtensionField(ctx, entitymeta.CreateFieldInput{
		FieldKey:         fieldKey,
		EntityType:       entitymeta.EntityUser,
		ValueType:        entitymeta.ValueString,
		Visibility:       entitymeta.VisibilityOwner,
		LabelENUS:        "Theme",
		OwnerExtensionID: id,
	}); err != nil {
		t.Fatalf("ensure field: %v", err)
	}
	actor := identity.Actor{ID: userID, Status: identity.UserStatusActive}
	if _, err := metaService.UpsertValues(ctx, actor, entitymeta.EntityUser, userID, []entitymeta.UpsertValueInput{
		{FieldKey: fieldKey, Value: "dark"},
	}); err != nil {
		t.Fatalf("write value: %v", err)
	}
	if _, err := store.Enable(ctx, id, TypePlugin); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if _, err := store.Disable(ctx, id); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// 正面断言：定义被去投影（enabled=false）。
	var enabled bool
	if err := pool.QueryRow(ctx, `
		SELECT enabled FROM entity_field_definitions WHERE field_key = $1`, fieldKey).Scan(&enabled); err != nil {
		t.Fatalf("read definition: %v", err)
	}
	if enabled {
		t.Fatal("disable must deproject the owner's field definitions")
	}
	// 值不能被删：重装回来必须原样可见。
	var values int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_meta_values
		WHERE entity_type = 'user' AND entity_id = $1 AND field_key = $2`, userID, fieldKey).Scan(&values); err != nil {
		t.Fatalf("count values: %v", err)
	}
	if values != 1 {
		t.Fatal("disable must keep stored values")
	}
}

// 生命周期状态发布的投影薄包装：enabled 目标 → 按目标版本 manifest 投影；
// 非 enabled 目标 → 只去投影；投影失败 → preflight 错误（整笔状态发布回滚）。
func TestSyncLifecycleSettingsSurfaceFieldsProjectsAndDeprojects(t *testing.T) {
	store, pool := projectableStore(t)
	ctx := context.Background()
	id := fmt.Sprintf("demo.lifecycle.projection.%d", time.Now().UnixNano())
	seedProjectableExtension(t, pool, id)

	var versionID int64
	if err := pool.QueryRow(ctx, `SELECT active_version_id FROM extensions WHERE id = $1`, id).Scan(&versionID); err != nil {
		t.Fatalf("read active version: %v", err)
	}

	projector := &recordingProjector{}
	store.WithSettingsSurfaceFields(projector)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := store.syncLifecycleSettingsSurfaceFields(ctx, tx, id, lifecycleStateVector{
		Status: StatusEnabled,
		Active: LifecycleStatePublicationArtifact{ExtensionID: id, VersionID: versionID},
	}); err != nil {
		t.Fatalf("enabled projection: %v", err)
	}
	if len(projector.synced) != 1 || projector.synced[0] != id {
		t.Fatalf("lifecycle enable must project the target manifest, got %#v", projector.synced)
	}

	if err := store.syncLifecycleSettingsSurfaceFields(ctx, tx, id, lifecycleStateVector{Status: StatusDisabled}); err != nil {
		t.Fatalf("disabled deprojection: %v", err)
	}
	if len(projector.disabled) != 1 || projector.disabled[0] != id {
		t.Fatalf("lifecycle disable must deproject, got %#v", projector.disabled)
	}

	failing := &recordingProjector{err: errors.New("boom")}
	store.WithSettingsSurfaceFields(failing)
	if err := store.syncLifecycleSettingsSurfaceFields(ctx, tx, id, lifecycleStateVector{
		Status: StatusEnabled,
		Active: LifecycleStatePublicationArtifact{ExtensionID: id, VersionID: versionID},
	}); !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("projection failure = %v, want ErrPreflightFailed", err)
	}
}

var _ = extensionmanifest.TypePlugin
