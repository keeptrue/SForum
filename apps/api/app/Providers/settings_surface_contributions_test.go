package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type fakeOverrideReader struct {
	items []settingssurface.Override
	err   error
}

func (f fakeOverrideReader) ListOverrides(context.Context) ([]settingssurface.Override, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func settingsSectionContribution(point, id string, order int, payload string) extensions.EffectiveContribution {
	return extensions.EffectiveContribution{
		ExtensionID:   "demo.plugin",
		ExtensionName: "Demo Plugin",
		ExtensionType: extensions.TypePlugin,
		Point:         point,
		ID:            id,
		Order:         order,
		Label:         map[string]string{"en-US": "Demo"},
		Icon:          "i-lucide-sliders-horizontal",
		Payload:       json.RawMessage(payload),
	}
}

func settingsSectionOverride(point, contributionID, itemKey string, enabled bool, sortOrder *int) settingssurface.Override {
	return settingssurface.Override{
		Key: settingssurface.OverrideKey{
			ExtensionID:    "demo.plugin",
			Point:          point,
			ContributionID: contributionID,
			ItemKey:        itemKey,
		},
		Enabled:   enabled,
		SortOrder: sortOrder,
	}
}

func intPointer(value int) *int { return &value }

func activeMember(permissions ...string) identity.Actor {
	granted := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		granted[permission] = true
	}
	return identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: granted}
}

func TestExtensionSettingsSurfaceProviderFiltersFieldsAndTabsByPermission(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab.public", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"string"},
				{"key":"secret","label":{"en-US":"Secret"},"type":"string","permission":"demo.plugin.moderate"}
			]}`),
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab.gated", 20,
			`{"type":"fields","permission":"demo.plugin.moderate","fields":[
				{"key":"badge","label":{"en-US":"Badge"},"type":"boolean"}
			]}`),
	}}
	provider := NewExtensionSettingsSurfaceProvider(source, nil)

	anonymous, err := provider.ProfileSections(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("anonymous ProfileSections: %v", err)
	}
	if len(anonymous) != 1 || anonymous[0].ID != "demo.tab.public" {
		t.Fatalf("anonymous viewer must only get the public tab, got %#v", anonymous)
	}
	if len(anonymous[0].Fields) != 1 ||
		anonymous[0].Fields[0].FieldKey != extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "theme") {
		t.Fatalf("field key must be the entity-meta composite key, got %#v", anonymous[0].Fields)
	}

	member := activeMember("other.permission")
	limited, err := provider.ProfileSections(context.Background(), member)
	if err != nil {
		t.Fatalf("member ProfileSections: %v", err)
	}
	if len(limited) != 1 || len(limited[0].Fields) != 1 {
		t.Fatalf("member without the permission must get the same shape, got %#v", limited)
	}

	moderator := activeMember("demo.plugin.moderate")
	full, err := provider.ProfileSections(context.Background(), moderator)
	if err != nil {
		t.Fatalf("moderator ProfileSections: %v", err)
	}
	if len(full) != 2 || full[0].ID != "demo.tab.public" || full[1].ID != "demo.tab.gated" {
		t.Fatalf("moderator must see both tabs in declaration order, got %#v", full)
	}
	if len(full[0].Fields) != 2 {
		t.Fatalf("moderator must see the gated field too, got %#v", full[0].Fields)
	}
}

func TestExtensionSettingsSurfaceProviderAdminTabsRequireAdminAccess(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointAdminSettingsTabs, "demo.tab.general", 10,
			`{"type":"settings","tabId":"general"}`),
		settingsSectionContribution(extensionmanifest.PointAdminSettingsTabs, "demo.tab.risk", 20,
			`{"type":"settings","tabId":"risk","permission":"demo.plugin.moderate"}`),
	}}
	provider := NewExtensionSettingsSurfaceProvider(source, nil)

	if tabs, err := provider.AdminSettingsTabs(context.Background(), activeMember()); err != nil || len(tabs) != 0 {
		t.Fatalf("viewer without admin.access must get no admin tabs, got %#v err=%v", tabs, err)
	}

	admin := activeMember(identity.PermissionAdminAccess)
	tabs, err := provider.AdminSettingsTabs(context.Background(), admin)
	if err != nil {
		t.Fatalf("admin AdminSettingsTabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].TabID != "general" {
		t.Fatalf("admin must only see tabs whose permission they hold, got %#v", tabs)
	}

	privileged := activeMember(identity.PermissionAdminAccess, "demo.plugin.moderate")
	tabs, err = provider.AdminSettingsTabs(context.Background(), privileged)
	if err != nil {
		t.Fatalf("privileged AdminSettingsTabs: %v", err)
	}
	if len(tabs) != 2 || tabs[1].TabID != "risk" {
		t.Fatalf("admin holding the tab permission must see both tabs, got %#v", tabs)
	}
}

func TestExtensionSettingsSurfaceProviderAppliesGovernanceOverrides(t *testing.T) {
	point := extensionmanifest.PointForumSettingsProfileTabs
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(point, "demo.tab.first", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":{"en-US":"Theme"},"type":"string","order":10},
				{"key":"about","label":{"en-US":"About"},"type":"text","order":20},
				{"key":"hidden","label":{"en-US":"Hidden"},"type":"string","order":30}
			]}`),
		settingsSectionContribution(point, "demo.tab.disabled", 20,
			`{"type":"fields","fields":[{"key":"muted","label":{"en-US":"Muted"},"type":"string"}]}`),
	}}
	overrides := fakeOverrideReader{items: []settingssurface.Override{
		// 运营关掉整个 tab。
		settingsSectionOverride(point, "demo.tab.disabled", "", false, nil),
		// 运营把一个字段压到最前，并关掉另一个字段。
		settingsSectionOverride(point, "demo.tab.first", "about", true, intPointer(1)),
		settingsSectionOverride(point, "demo.tab.first", "hidden", false, nil),
	}}
	provider := NewExtensionSettingsSurfaceProvider(source, overrides)

	sections, err := provider.ProfileSections(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileSections: %v", err)
	}
	if len(sections) != 1 || sections[0].ID != "demo.tab.first" {
		t.Fatalf("disabled tab must not be served, got %#v", sections)
	}
	if sections[0].Source != "extension" || sections[0].ExtensionID != "demo.plugin" {
		t.Fatalf("section source/extension must be attributed, got %#v", sections[0])
	}
	fields := sections[0].Fields
	if len(fields) != 2 {
		t.Fatalf("disabled field must be dropped, got %#v", fields)
	}
	if fields[0].FieldKey != extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "about") ||
		fields[1].FieldKey != extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "theme") {
		t.Fatalf("override sort order must win over declared order, got %#v", fields)
	}
}

func TestExtensionSettingsSurfaceProviderHonorsTabSortOverride(t *testing.T) {
	point := extensionmanifest.PointForumSettingsProfileTabs
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(point, "demo.tab.a", 10,
			`{"type":"fields","fields":[{"key":"a","label":"A","type":"string"}]}`),
		settingsSectionContribution(point, "demo.tab.b", 20,
			`{"type":"fields","fields":[{"key":"b","label":"B","type":"string"}]}`),
	}}
	overrides := fakeOverrideReader{items: []settingssurface.Override{
		settingsSectionOverride(point, "demo.tab.b", "", true, intPointer(1)),
	}}
	sections, err := NewExtensionSettingsSurfaceProvider(source, overrides).ProfileSections(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileSections: %v", err)
	}
	if len(sections) != 2 || sections[0].ID != "demo.tab.b" || sections[1].ID != "demo.tab.a" {
		t.Fatalf("override sort order must reorder tabs, got %#v", sections)
	}
	if sections[0].EffectiveSortOrder != 1 {
		t.Fatalf("effective order must be the override value, got %d", sections[0].EffectiveSortOrder)
	}
}

func TestExtensionSettingsSurfaceProviderTruncatesToSurfaceCap(t *testing.T) {
	point := extensionmanifest.PointForumSettingsProfileTabs
	items := make([]extensions.EffectiveContribution, 0, extensionmanifest.SettingsSectionTabsPerPointMax+2)
	for index := 0; index < extensionmanifest.SettingsSectionTabsPerPointMax+2; index++ {
		items = append(items, settingsSectionContribution(point,
			fmt.Sprintf("demo.tab.%02d", index), 10+index,
			fmt.Sprintf(`{"type":"fields","fields":[{"key":"f%d","label":"F","type":"string"}]}`, index)))
	}
	sections, err := NewExtensionSettingsSurfaceProvider(fakeContributionSource{items: items}, nil).
		ProfileSections(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileSections: %v", err)
	}
	if len(sections) != extensionmanifest.SettingsSectionTabsPerPointMax {
		t.Fatalf("surface cap = %d, got %d sections", extensionmanifest.SettingsSectionTabsPerPointMax, len(sections))
	}
	if sections[0].ID != "demo.tab.00" {
		t.Fatalf("truncation must keep the lowest order first, got %#v", sections[0])
	}
}

// 权限键永不下发：序列化结果里不能出现任何 permission 字段或权限键值。
func TestExtensionSettingsSurfaceProviderNeverSerializesPermissions(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab", 10,
			`{"type":"fields","permission":"demo.plugin.moderate","fields":[
				{"key":"theme","label":"Theme","type":"string","permission":"demo.plugin.moderate"}
			]}`),
	}}
	sections, err := NewExtensionSettingsSurfaceProvider(source, nil).
		ProfileSections(context.Background(), activeMember("demo.plugin.moderate"))
	if err != nil {
		t.Fatalf("ProfileSections: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected the permission-gated tab for a privileged viewer, got %#v", sections)
	}
	encoded, err := json.Marshal(sections)
	if err != nil {
		t.Fatalf("marshal sections: %v", err)
	}
	if strings.Contains(string(encoded), "permission") || strings.Contains(string(encoded), "demo.plugin.moderate") {
		t.Fatalf("permission data leaked into the serialized section: %s", encoded)
	}
}

func TestExtensionSettingsSurfaceProviderDropsEmptyFieldTabs(t *testing.T) {
	point := extensionmanifest.PointForumSettingsProfileTabs
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(point, "demo.tab", 10,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`),
	}}
	overrides := fakeOverrideReader{items: []settingssurface.Override{
		settingsSectionOverride(point, "demo.tab", "theme", false, nil),
	}}
	sections, err := NewExtensionSettingsSurfaceProvider(source, overrides).ProfileSections(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileSections: %v", err)
	}
	if len(sections) != 0 {
		t.Fatalf("a fields tab with every field filtered out must not be served, got %#v", sections)
	}
}

func TestExtensionSettingsSurfaceProviderPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := NewExtensionSettingsSurfaceProvider(fakeContributionSource{err: boom}, nil).
		ProfileSections(context.Background(), identity.Actor{}); !errors.Is(err, boom) {
		t.Fatalf("source error must propagate, got %v", err)
	}
	if _, err := NewExtensionSettingsSurfaceProvider(fakeContributionSource{}, fakeOverrideReader{err: boom}).
		ProfileSections(context.Background(), identity.Actor{}); !errors.Is(err, boom) {
		t.Fatalf("override read error must propagate, got %v", err)
	}
	if sections, err := NewExtensionSettingsSurfaceProvider(nil, nil).
		ProfileSections(context.Background(), identity.Actor{}); err != nil || sections != nil {
		t.Fatalf("nil source must yield no sections, got %#v err=%v", sections, err)
	}
}
