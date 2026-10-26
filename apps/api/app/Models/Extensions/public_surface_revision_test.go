package extensions

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type countingPublicSurfaceBumper struct {
	count atomic.Int64
	err   error
}

func (b *countingPublicSurfaceBumper) BumpPublicSurfaceRevision(context.Context) (int64, error) {
	if b.err != nil {
		return 0, b.err
	}
	return b.count.Add(1) + 1, nil
}

func TestManifestAffectsPublicSurface(t *testing.T) {
	if ManifestAffectsPublicSurface(Manifest{}) {
		t.Fatal("empty manifest must not affect public surface")
	}
	if ManifestAffectsPublicSurface(Manifest{Contributions: []ManifestContribution{
		{Point: "forum.topic.actions", ID: "x"},
	}}) {
		t.Fatal("non-public forum points without gate must not affect")
	}
	if !ManifestAffectsPublicSurface(Manifest{Contributions: []ManifestContribution{
		{Point: extensionmanifest.PointForumTopicBadges, ID: "badge", EnabledBySetting: "show_topic_badge"},
	}}) {
		t.Fatal("enabledBySetting must affect public surface")
	}
	if !ManifestAffectsPublicSurface(Manifest{Contributions: []ManifestContribution{
		{Point: extensionmanifest.PointForumTopicSidebar, ID: "sidebar"},
	}}) {
		t.Fatal("forum.topic.sidebar must affect public surface")
	}
	if !ManifestAffectsPublicSurface(Manifest{Contributions: []ManifestContribution{
		{Point: extensionmanifest.PointForumTopicListBadges, ID: "list"},
	}}) {
		t.Fatal("forum.topic.list.badges must affect public surface")
	}
	if !ManifestAffectsPublicSurface(Manifest{Contributions: []ManifestContribution{
		{Point: extensionmanifest.PointForumNavItems, ID: "nav"},
	}}) {
		t.Fatal("forum.nav.items must affect public surface")
	}
}

func TestUpdateSettingsBumpsPublicSurfaceRevisionWhenAffects(t *testing.T) {
	store := &fakeExtensionStore{
		items:    map[string]Extension{},
		settings: map[string]map[string]string{},
	}
	bumper := &countingPublicSurfaceBumper{}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithPublicSurfaceRevisionBumper(bumper),
	)
	actor := extensionManager()

	payload, err := json.Marshal(map[string]string{"tone": "info", "href": "/guidelines"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	plugin := contributionTestPlugin("policy.plugin", StatusEnabled, []ManifestContribution{
		{
			Point:            extensionmanifest.PointForumTopicBadges,
			ID:               "content-policy-active",
			EnabledBySetting: "show_topic_badge",
			Payload:          payload,
		},
	})
	plugin.Manifest.Settings = []ManifestSetting{{
		Key: "show_topic_badge", Label: LocalizedText{Default: "Show badge"}, Type: "boolean", Default: "false",
	}}
	store.items[plugin.ID] = plugin

	if _, err := service.UpdateSettings(context.Background(), actor, plugin.ID, UpdateSettingsInput{
		Values: map[string]string{"show_topic_badge": "true"},
	}, "zh-CN"); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if bumper.count.Load() != 1 {
		t.Fatalf("expected one bump after update, got %d", bumper.count.Load())
	}

	if _, err := service.ResetSettings(context.Background(), actor, plugin.ID, "zh-CN"); err != nil {
		t.Fatalf("ResetSettings: %v", err)
	}
	if bumper.count.Load() != 2 {
		t.Fatalf("expected bump after reset, got %d", bumper.count.Load())
	}
}

func TestUpdateSettingsDoesNotBumpWhenNoPublicSurface(t *testing.T) {
	store := &fakeExtensionStore{
		items:    map[string]Extension{},
		settings: map[string]map[string]string{},
	}
	bumper := &countingPublicSurfaceBumper{}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithPublicSurfaceRevisionBumper(bumper),
	)
	actor := extensionManager()

	plugin := contributionTestPlugin("internal.plugin", StatusEnabled, nil)
	plugin.Manifest.Settings = []ManifestSetting{{
		Key: "token", Label: LocalizedText{Default: "Token"}, Type: "text", Default: "",
	}}
	// 仅 hooks，无公开 forum 贡献、无 enabledBySetting。
	plugin.Manifest.Contributions = nil
	store.items[plugin.ID] = plugin

	if _, err := service.UpdateSettings(context.Background(), actor, plugin.ID, UpdateSettingsInput{
		Values: map[string]string{"token": "abc"},
	}, "zh-CN"); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if bumper.count.Load() != 0 {
		t.Fatalf("expected no bump for non-public settings, got %d", bumper.count.Load())
	}
}
