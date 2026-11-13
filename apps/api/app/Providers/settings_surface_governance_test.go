package providers

import (
	"context"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type fakeOverrideStore struct {
	rows map[settingssurface.OverrideKey]settingssurface.Override
}

func newFakeOverrideStore() *fakeOverrideStore {
	return &fakeOverrideStore{rows: map[settingssurface.OverrideKey]settingssurface.Override{}}
}

func (s *fakeOverrideStore) ListOverrides(context.Context) ([]settingssurface.Override, error) {
	out := make([]settingssurface.Override, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row)
	}
	return out, nil
}

func (s *fakeOverrideStore) UpsertOverride(_ context.Context, input settingssurface.OverrideInput) (settingssurface.Override, error) {
	key := input.Key.Normalized()
	if key.ExtensionID == "" || key.Point == "" || key.ContributionID == "" {
		return settingssurface.Override{}, settingssurface.ErrInvalid
	}
	row := settingssurface.Override{
		Key: key, Enabled: input.Enabled, SortOrder: input.SortOrder, UpdatedByUserID: input.UpdatedByUserID,
	}
	if existing, ok := s.rows[key]; ok {
		row.CreatedAt = existing.CreatedAt
	}
	s.rows[key] = row
	return row, nil
}

func (s *fakeOverrideStore) DeleteOverride(_ context.Context, key settingssurface.OverrideKey) error {
	key = key.Normalized()
	if _, ok := s.rows[key]; !ok {
		return settingssurface.ErrNotFound
	}
	delete(s.rows, key)
	return nil
}

type fakeExtensionSettingsReader struct {
	documents map[string]extensions.ExtensionSettings
	err       error
}

func (r fakeExtensionSettingsReader) Settings(_ context.Context, _ identity.Actor, extensionID string, _ string) (extensions.ExtensionSettings, error) {
	if r.err != nil {
		return extensions.ExtensionSettings{}, r.err
	}
	return r.documents[extensionID], nil
}

func governanceTestSource() fakeContributionSource {
	return fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab.first", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"string","order":10},
				{"key":"about","label":{"en-US":"About"},"type":"text","order":20}
			]}`),
		settingsSectionContribution(extensionmanifest.PointAdminSettingsTabs, "demo.tab.site", 5,
			`{"type":"settings","tabId":"general"}`),
	}}
}

func governanceTestProvider(source EffectiveContributionSource, overrides settingssurface.OverrideStore, settings ExtensionSettingsReader) ExtensionSettingsSurfaceGovernanceProvider {
	return NewExtensionSettingsSurfaceGovernanceProvider(
		source, overrides,
		NewExtensionSettingsSurfaceProvider(source, overrides),
		settings,
	)
}

func extensionAdmin(permissions ...string) identity.Actor {
	granted := map[string]bool{}
	for _, permission := range permissions {
		granted[permission] = true
	}
	return identity.Actor{ID: 4, Status: identity.UserStatusActive, Permissions: granted}
}

func TestSettingsSurfaceGovernanceContributionsExposeGovernanceFacts(t *testing.T) {
	overrides := newFakeOverrideStore()
	source := governanceTestSource()
	disabled := false
	if _, err := overrides.UpsertOverride(context.Background(), settingssurface.OverrideInput{
		Key: settingssurface.OverrideKey{
			ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
			ContributionID: "demo.tab.first",
		},
		Enabled: disabled, SortOrder: intPointer(3),
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	provider := governanceTestProvider(source, overrides, nil)

	if _, err := provider.Contributions(context.Background(), activeMember()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer without extension.view must be denied, got %v", err)
	}
	items, err := provider.Contributions(context.Background(), extensionAdmin(identity.PermissionExtensionView))
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected both declared contributions, got %#v", items)
	}
	first := items[0]
	if first.Enabled || !first.Overridden || first.EffectiveSortOrder != 3 || first.Order != 10 {
		t.Fatalf("tab governance facts = %#v", first)
	}
	if len(first.Fields) != 2 {
		t.Fatalf("declared fields must be listed, got %#v", first.Fields)
	}
	if first.Fields[0].FieldKey != extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "theme") {
		t.Fatalf("item fieldKey must be the composite key, got %#v", first.Fields[0])
	}
	if !first.Fields[0].Enabled || first.Fields[0].Overridden {
		t.Fatalf("ungoverned field must default to enabled, got %#v", first.Fields[0])
	}
}

func TestSettingsSurfaceGovernanceMutateRequiresManagePermission(t *testing.T) {
	provider := governanceTestProvider(governanceTestSource(), newFakeOverrideStore(), nil)
	key := settingssurface.OverrideKey{
		ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
		ContributionID: "demo.tab.first",
	}
	if _, err := provider.Mutate(context.Background(), extensionAdmin(identity.PermissionExtensionView),
		settingssurface.MutateInput{Key: key, Enabled: boolPtr(false)}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("view-only admin must not mutate, got %v", err)
	}
	disabled := false
	view, err := provider.Mutate(context.Background(), extensionAdmin(identity.PermissionExtensionPluginManage),
		settingssurface.MutateInput{Key: key, Enabled: &disabled})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if view.Enabled || !view.Overridden || view.EffectiveSortOrder != 10 || view.DeclaredSortOrder != 10 {
		t.Fatalf("mutate view = %#v", view)
	}
}

func TestSettingsSurfaceGovernanceMutatePreservesUntouchedColumns(t *testing.T) {
	overrides := newFakeOverrideStore()
	provider := governanceTestProvider(governanceTestSource(), overrides, nil)
	admin := extensionAdmin(identity.PermissionExtensionPluginManage)
	key := settingssurface.OverrideKey{
		ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
		ContributionID: "demo.tab.first",
	}
	disabled := false
	if _, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: key, Enabled: &disabled, SortOrderSet: true, SortOrder: intPointer(7)}); err != nil {
		t.Fatalf("initial mutate: %v", err)
	}
	// 只改顺序：enabled=false 必须保留，否则「调个顺序」会把 tab 偷偷打开。
	view, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: key, SortOrderSet: true, SortOrder: intPointer(2)})
	if err != nil {
		t.Fatalf("second mutate: %v", err)
	}
	if view.Enabled {
		t.Fatalf("sort-only mutate must keep enabled=false, got %#v", view)
	}
	if view.EffectiveSortOrder != 2 {
		t.Fatalf("sort override = %d", view.EffectiveSortOrder)
	}
	// sortOrder=null 清掉顺序覆盖，回到声明顺序。
	view, err = provider.Mutate(context.Background(), admin, settingssurface.MutateInput{Key: key, SortOrderSet: true})
	if err != nil {
		t.Fatalf("clear sort: %v", err)
	}
	if view.EffectiveSortOrder != 10 || view.Enabled {
		t.Fatalf("cleared sort must fall back to declared order and keep enabled, got %#v", view)
	}
}

func TestSettingsSurfaceGovernanceRejectsUnknownTargets(t *testing.T) {
	provider := governanceTestProvider(governanceTestSource(), newFakeOverrideStore(), nil)
	admin := extensionAdmin(identity.PermissionExtensionPluginManage)

	unknownTab := settingssurface.OverrideKey{
		ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
		ContributionID: "demo.tab.absent",
	}
	if _, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: unknownTab, Enabled: boolPtr(false)}); !errors.Is(err, settingssurface.ErrUnknownContribution) {
		t.Fatalf("unknown tab = %v", err)
	}
	unknownItem := unknownTab
	unknownItem.ContributionID = "demo.tab.first"
	unknownItem.ItemKey = "absent"
	if _, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: unknownItem, Enabled: boolPtr(false)}); !errors.Is(err, settingssurface.ErrUnknownItem) {
		t.Fatalf("unknown item = %v", err)
	}
	// itemKey 是裸键（组合键在控制器层已拆开），声明过就能改。
	knownItem := unknownItem
	knownItem.ItemKey = "theme"
	if _, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: knownItem, Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("known item must be mutable: %v", err)
	}
}

func TestSettingsSurfaceGovernanceResetRestoresManifestDefault(t *testing.T) {
	overrides := newFakeOverrideStore()
	provider := governanceTestProvider(governanceTestSource(), overrides, nil)
	admin := extensionAdmin(identity.PermissionExtensionPluginManage)
	key := settingssurface.OverrideKey{
		ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
		ContributionID: "demo.tab.first",
	}
	if _, err := provider.Mutate(context.Background(), admin,
		settingssurface.MutateInput{Key: key, Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := provider.Reset(context.Background(), admin, key); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(overrides.rows) != 0 {
		t.Fatalf("reset must delete the override row, got %#v", overrides.rows)
	}
	// 重复删除同样成功：本来就没有覆盖。
	if err := provider.Reset(context.Background(), admin, key); err != nil {
		t.Fatalf("repeat reset must be idempotent: %v", err)
	}
	if err := provider.Reset(context.Background(), extensionAdmin(identity.PermissionExtensionView), key); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("reset without manage permission = %v", err)
	}
}

func TestSettingsSurfaceGovernanceSurfaceTabsInlineSettingsAndFilter(t *testing.T) {
	source := governanceTestSource()
	settings := fakeExtensionSettingsReader{documents: map[string]extensions.ExtensionSettings{
		"demo.plugin": {ExtensionID: "demo.plugin", Renderer: extensions.ExtensionSettingsRenderer{Mode: "schema"}},
	}}
	provider := governanceTestProvider(source, newFakeOverrideStore(), settings)

	// 只有 extension.view、没有 admin.access：拒绝（不是空列表）。
	if _, err := provider.SurfaceTabs(context.Background(), activeMember(identity.PermissionExtensionView), "zh-CN"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer without admin.access = %v, want ErrPermissionDenied", err)
	}
	// 只有 admin.access、没有 extension.view：同样拒绝。
	if _, err := provider.SurfaceTabs(context.Background(), activeMember(identity.PermissionAdminAccess), "zh-CN"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer without extension.view = %v, want ErrPermissionDenied", err)
	}
	admin := extensionAdmin(identity.PermissionAdminAccess, identity.PermissionExtensionView)
	tabs, err := provider.SurfaceTabs(context.Background(), admin, "zh-CN")
	if err != nil {
		t.Fatalf("SurfaceTabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "ext.demo.plugin.demo.tab.site" || tabs[0].Type != "settings" {
		t.Fatalf("surface tabs = %#v", tabs)
	}
	if tabs[0].Settings == nil || tabs[0].Settings.ExtensionID != "demo.plugin" {
		t.Fatalf("type=settings must inline the resolved settings document, got %#v", tabs[0].Settings)
	}
}

func TestSettingsSurfaceGovernanceMarksCapSuppressedContributions(t *testing.T) {
	items := make([]extensions.EffectiveContribution, 0, extensionmanifest.SettingsSectionTabsPerPointMax+2)
	for index := 0; index < extensionmanifest.SettingsSectionTabsPerPointMax+2; index++ {
		items = append(items, settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs,
			"demo.tab."+string(rune('a'+index)), index,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`))
	}
	provider := governanceTestProvider(fakeContributionSource{items: items}, newFakeOverrideStore(), nil)
	views, err := provider.Contributions(context.Background(), extensionAdmin(identity.PermissionExtensionView))
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	if len(views) != len(items) {
		t.Fatalf("governance read model must not truncate, got %d of %d", len(views), len(items))
	}
	suppressed := 0
	for _, view := range views {
		if view.SuppressedByCap {
			suppressed++
		}
	}
	if suppressed != 2 {
		t.Fatalf("expected 2 cap-suppressed contributions, got %d", suppressed)
	}
}

// itemKey 与组合键都归一成小写裸键：传 "Theme" 必须命中 "theme"，
// 而不是让调用方自己猜大小写。
func TestSettingsSurfaceGovernanceNormalizesItemKeyCase(t *testing.T) {
	overrides := newFakeOverrideStore()
	provider := governanceTestProvider(governanceTestSource(), overrides, nil)
	admin := extensionAdmin(identity.PermissionExtensionPluginManage)
	for _, itemKey := range []string{"THEME", "Theme", "theme"} {
		key := settingssurface.OverrideKey{
			ExtensionID:    "demo.plugin",
			Point:          extensionmanifest.PointForumSettingsProfileTabs,
			ContributionID: "demo.tab.first",
			ItemKey:        itemKey,
		}
		disabled := false
		if _, err := provider.Mutate(context.Background(), admin,
			settingssurface.MutateInput{Key: key, Enabled: &disabled}); err != nil {
			t.Fatalf("mutate with itemKey %q: %v", itemKey, err)
		}
		if len(overrides.rows) != 1 {
			t.Fatalf("itemKey %q must land on the same row, got %#v", itemKey, overrides.rows)
		}
		for stored := range overrides.rows {
			if stored.ItemKey != "theme" {
				t.Fatalf("itemKey must be stored lowercased, got %q", stored.ItemKey)
			}
		}
	}
}
