package providers

import (
	"context"
	"testing"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 公开资料页的真实场景：匿名 viewer。读裁决不能要求「该 viewer 的表单投影非空」，
// 否则 public 字段永远读不到——那正是 profile.meta 整键缺失的根因。
func TestSettingsSurfaceFieldPolicyReadWithAnonymousViewer(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab", 10,
			`{"type":"fields","fields":[
				{"key":"public_field","label":"Public","type":"string","visibility":"public"},
				{"key":"owner_field","label":"Owner","type":"string","visibility":"owner"},
				{"key":"admin_field","label":"Admin","type":"string","visibility":"admin"},
				{"key":"gated_public","label":"Gated","type":"string","visibility":"public","permission":"demo.plugin.view_gated"}
			]}`),
	}}
	overrides := newFakeOverrideStore()
	policy := NewSettingsSurfaceFieldPolicy(NewExtensionSettingsSurfaceProvider(source, overrides))
	anonymous := identity.Actor{}
	key := func(name string) entitymeta.FieldDefinition {
		return entitymeta.FieldDefinition{
			FieldKey:         extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", name),
			OwnerExtensionID: "demo.plugin",
		}
	}

	if !policy.AllowRead(context.Background(), anonymous, key("public_field")) {
		t.Fatal("anonymous viewer must be able to read a public plugin field")
	}
	// visibility 由 entity-meta 的 canReadField 裁决；策略本身不因 owner/admin 而拒绝，
	// 但字段级 permission 必须由策略拒绝。
	if policy.AllowRead(context.Background(), anonymous, key("gated_public")) {
		t.Fatal("permission-gated public field must be filtered for anonymous viewers")
	}
	member := identity.Actor{ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{"demo.plugin.view_gated": true}}
	if !policy.AllowRead(context.Background(), member, key("gated_public")) {
		t.Fatal("viewer holding the field permission must read it")
	}
	// 未声明的键：不可读也不可写。
	if policy.AllowRead(context.Background(), member, key("absent")) {
		t.Fatal("undeclared field must not be readable")
	}
}

// 治理关闭的字段：匿名也不可读（关闭即不可读写）。
func TestSettingsSurfaceFieldPolicyReadRespectsGovernance(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab", 10,
			`{"type":"fields","fields":[{"key":"public_field","label":"Public","type":"string","visibility":"public"}]}`),
	}}
	overrides := newFakeOverrideStore()
	disabled := false
	if _, err := overrides.UpsertOverride(context.Background(), settingssurface.OverrideInput{
		Key: settingssurface.OverrideKey{
			ExtensionID: "demo.plugin", Point: extensionmanifest.PointForumSettingsProfileTabs,
			ContributionID: "demo.tab", ItemKey: "public_field",
		},
		Enabled: disabled,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	policy := NewSettingsSurfaceFieldPolicy(NewExtensionSettingsSurfaceProvider(source, overrides))
	definition := entitymeta.FieldDefinition{
		FieldKey:         extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "public_field"),
		OwnerExtensionID: "demo.plugin",
	}
	if policy.AllowRead(context.Background(), identity.Actor{}, definition) {
		t.Fatal("governed-off field must not be readable, even on the public surface")
	}
}

// 写入裁决保持严格：匿名与会话外的 actor 都不能写（投影为空即拒绝）。
func TestSettingsSurfaceFieldPolicyWriteStaysStrict(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution(extensionmanifest.PointForumSettingsProfileTabs, "demo.tab", 10,
			`{"type":"fields","fields":[{"key":"public_field","label":"Public","type":"string","visibility":"public"}]}`),
	}}
	policy := NewSettingsSurfaceFieldPolicy(NewExtensionSettingsSurfaceProvider(source, newFakeOverrideStore()))
	definition := entitymeta.FieldDefinition{
		FieldKey:         extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "public_field"),
		OwnerExtensionID: "demo.plugin",
	}
	if policy.AllowWrite(context.Background(), identity.Actor{}, definition) {
		t.Fatal("anonymous actor must not write plugin fields")
	}
	if !policy.AllowWrite(context.Background(), identity.Actor{ID: 7, Status: identity.UserStatusActive}, definition) {
		t.Fatal("active member must be able to write a declared public field")
	}
}
