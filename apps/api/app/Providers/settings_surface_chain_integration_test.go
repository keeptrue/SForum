package providers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type chainFixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	userID  int64
	actor   identity.Actor
	meta    *entitymeta.Service
	sync    SettingsSurfaceFieldSync
	store   SettingsSurfaceMetaStore
	govern  ExtensionSettingsSurfaceGovernanceProvider
	plugin  string
	overrid *fakeOverrideStore
}

// newChainFixture 铺一条真实链路：插件声明 → entity-meta 字段/值 → 治理覆盖。
// 用真实迁移过的库（testdb 隔离），因为「字段键能不能落到 entity_meta_values」
// 正是这条链路唯一有意义的证据。
func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1) RETURNING id`,
		"settings_surface_"+suffix, "settings_surface_"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_meta_values WHERE entity_type = 'user' AND entity_id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM entity_field_definitions WHERE owner_extension_id = $1`, "demo.plugin")
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})

	metaService := entitymeta.NewService(entitymeta.NewPostgresStore(pool))
	overrides := newFakeOverrideStore()
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab.preferences", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"select","visibility":"owner","order":10,
				 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]},
				{"key":"about","label":{"en-US":"About"},"type":"text","visibility":"public","order":20}
			]}`),
	}}
	surfaces := NewExtensionSettingsSurfaceProvider(source, overrides)
	metaService.WithFieldPolicy(NewSettingsSurfaceFieldPolicy(surfaces))
	// 与声明来源的 ExtensionID 保持一致：投影、治理、读取必须指同一个扩展。
	plugin := "demo.plugin"
	return &chainFixture{
		ctx: ctx, pool: pool, userID: userID,
		actor:   identity.Actor{ID: userID, Status: identity.UserStatusActive},
		meta:    metaService,
		sync:    NewSettingsSurfaceFieldSync(metaService),
		store:   NewSettingsSurfaceMetaStore(surfaces, metaService),
		govern:  governanceTestProvider(source, overrides, nil),
		plugin:  plugin,
		overrid: overrides,
	}
}

func (f *chainFixture) key(t *testing.T, name string) string {
	t.Helper()
	return extensionmanifest.SettingsSurfaceFieldKey(f.plugin, name)
}

// 端到端链路：enable 投影 → GET /profile 下发的组合键 → PUT meta 成功 →
// 治理关闭后写入被拒 → 公开面只显 public。
func TestSettingsSurfaceEndToEndChain(t *testing.T) {
	fixture := newChainFixture(t)
	manifest := extensionmanifest.Manifest{
		ID:   fixture.plugin,
		Type: extensionmanifest.TypePlugin,
		Contributions: []extensionmanifest.ManifestContribution{{
			Point: extensionmanifest.PointForumSettingsProfileTabs,
			ID:    "demo.tab.preferences",
			Order: 10,
			Label: map[string]string{"en-US": "Preferences"},
			Payload: []byte(`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"select","visibility":"owner",
				 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]},
				{"key":"about","label":{"en-US":"About"},"type":"text","visibility":"public"}
			]}`),
		}},
	}

	// 1) enable：字段声明投影进 entity-meta（幂等，重复一次不新增行）。
	if count, err := fixture.sync.SyncManifest(fixture.ctx, manifest); err != nil || count != 2 {
		t.Fatalf("sync manifest = %d, %v", count, err)
	}
	if _, err := fixture.sync.SyncManifest(fixture.ctx, manifest); err != nil {
		t.Fatalf("repeat sync: %v", err)
	}
	var definitions int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM entity_field_definitions WHERE owner_extension_id = $1`,
		fixture.plugin).Scan(&definitions); err != nil {
		t.Fatalf("count definitions: %v", err)
	}
	if definitions != 2 {
		t.Fatalf("repeat enable must not duplicate fields, got %d", definitions)
	}

	// 2) GET /profile 形状：tab + 组合键字段。
	sections, err := NewExtensionSettingsSurfaceProvider(fixture.surfaceSource(), fixture.overrid).
		ProfileSections(fixture.ctx, fixture.actor)
	if err != nil || len(sections) != 1 || len(sections[0].Fields) != 2 {
		t.Fatalf("sections = %#v err=%v", sections, err)
	}
	keys := map[string]bool{}
	for _, field := range sections[0].Fields {
		keys[field.FieldKey] = true
	}
	if !keys[fixture.key(t, "theme")] || !keys[fixture.key(t, "about")] {
		t.Fatalf("field keys must be composite, got %#v", keys)
	}

	// 3) PUT /profile meta：组合键写入成功，值落 entity_meta_values。
	if err := fixture.store.WriteSettingsValues(fixture.ctx, fixture.actor, map[string]any{
		fixture.key(t, "theme"): "dark",
		fixture.key(t, "about"): "hello",
	}); err != nil {
		t.Fatalf("write settings values: %v", err)
	}
	values, err := fixture.store.MySettingsValues(fixture.ctx, fixture.actor)
	if err != nil {
		t.Fatalf("read settings values: %v", err)
	}
	if values[fixture.key(t, "theme")] != "dark" || values[fixture.key(t, "about")] != "hello" {
		t.Fatalf("settings values = %#v", values)
	}

	// 4) 公开面：只有 visibility=public 的字段。
	publicMeta, err := fixture.store.PublicSettingsMeta(fixture.ctx, identity.Actor{}, fixture.userID)
	if err != nil {
		t.Fatalf("public meta: %v", err)
	}
	if len(publicMeta) != 1 || publicMeta[fixture.key(t, "about")].Value != "hello" {
		t.Fatalf("public meta = %#v", publicMeta)
	}
	if _, leaked := publicMeta[fixture.key(t, "theme")]; leaked {
		t.Fatal("owner-visibility field must not leak into the public profile")
	}

	// 5) 治理关闭 theme 字段 → 写入被拒（字段级 422），读取也不再出现。
	admin := extensionAdmin(identity.PermissionExtensionPluginManage)
	disabled := false
	if _, err := fixture.govern.Mutate(fixture.ctx, admin, settingssurface.MutateInput{
		Key: settingssurface.OverrideKey{
			ExtensionID: fixture.plugin, Point: extensionmanifest.PointForumSettingsProfileTabs,
			ContributionID: "demo.tab.preferences", ItemKey: "theme",
		},
		Enabled: &disabled,
	}); err != nil {
		t.Fatalf("governance disable: %v", err)
	}
	err = fixture.store.WriteSettingsValues(fixture.ctx, fixture.actor, map[string]any{
		fixture.key(t, "theme"): "light",
	})
	fieldErr, ok := err.(*profile.SettingsFieldError)
	if !ok {
		t.Fatalf("write to a governed-off field must fail, got %v", err)
	}
	if fieldErr.Fields[fixture.key(t, "theme")][0] != profile.MessageSettingsFieldUnknown {
		t.Fatalf("field error = %#v", fieldErr.Fields)
	}
	// 存储层同样拒绝：绕过 profile 的 /entity-meta 写路径也过不去。
	if _, err := fixture.meta.UpsertValues(fixture.ctx, fixture.actor, entitymeta.EntityUser, fixture.userID,
		[]entitymeta.UpsertValueInput{{FieldKey: fixture.key(t, "theme"), Value: "light"}}); err == nil {
		t.Fatal("entity-meta write path must also honor governance")
	}

	// 6) 治理恢复默认 → 写入重新可用（同一行覆盖被删除）。
	if err := fixture.govern.Reset(fixture.ctx, admin, settingssurface.OverrideKey{
		ExtensionID: fixture.plugin, Point: extensionmanifest.PointForumSettingsProfileTabs,
		ContributionID: "demo.tab.preferences", ItemKey: "theme",
	}); err != nil {
		t.Fatalf("governance reset: %v", err)
	}
	if err := fixture.store.WriteSettingsValues(fixture.ctx, fixture.actor, map[string]any{
		fixture.key(t, "theme"): "light",
	}); err != nil {
		t.Fatalf("write after reset: %v", err)
	}

	// 7) 停用插件：字段 enabled=false，值仍在（重装回来还能看到）。
	if err := fixture.sync.Disable(fixture.ctx, fixture.plugin); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var enabled bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT enabled FROM entity_field_definitions WHERE field_key = $1`,
		fixture.key(t, "theme")).Scan(&enabled); err != nil {
		t.Fatalf("read definition: %v", err)
	}
	if enabled {
		t.Fatal("disable must flip enabled=false")
	}
	var stored int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM entity_meta_values WHERE entity_type = 'user' AND entity_id = $1 AND field_key = $2`,
		fixture.userID, fixture.key(t, "theme")).Scan(&stored); err != nil {
		t.Fatalf("count values: %v", err)
	}
	if stored != 1 {
		t.Fatal("disable must not delete stored values")
	}
}

// surfaceSource 复刻 fixture 的声明集合，供「GET /profile 形状」一步复用。
func (f *chainFixture) surfaceSource() EffectiveContributionSource {
	return fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab.preferences", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"select","visibility":"owner",
				 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]},
				{"key":"about","label":{"en-US":"About"},"type":"text","visibility":"public"}
			]}`),
	}}
}
