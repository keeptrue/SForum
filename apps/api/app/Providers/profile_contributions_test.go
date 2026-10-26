package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func profileTabContribution(id string, order int, payload string) extensions.EffectiveContribution {
	return extensions.EffectiveContribution{
		ExtensionID: "demo.plugin",
		Point:       "forum.profile.tabs",
		ID:          id,
		Order:       order,
		Label:       map[string]string{"en-US": "Demo"},
		Icon:        "i-lucide-user",
		Payload:     json.RawMessage(payload),
	}
}

func TestExtensionProfileTabProviderFiltersByViewerPermission(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		profileTabContribution("public", 10, `{"type":"hostLink","href":"/tags"}`),
		profileTabContribution("gated", 20, `{"type":"hostLink","href":"/moderation","permission":"demo.plugin.moderate"}`),
		profileTabContribution("gated-widget", 30, `{"type":"l2Widget","componentId":"demo.plugin.stats","permission":"demo.plugin.moderate"}`),
	}}
	provider := NewExtensionProfileTabProvider(source)

	anonymous, err := provider.ProfileTabs(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("anonymous ProfileTabs: %v", err)
	}
	if len(anonymous) != 1 || anonymous[0].ID != "public" {
		t.Fatalf("anonymous viewer must only get the public tab, got %#v", anonymous)
	}

	// 活跃但没有该权限键的成员：依旧只看到公开 tab。
	member := identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{"other.permission": true}}
	tabs, err := provider.ProfileTabs(context.Background(), member)
	if err != nil {
		t.Fatalf("member ProfileTabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "public" {
		t.Fatalf("member without permission must only get the public tab, got %#v", tabs)
	}

	// 拥有权限键的活跃访客：受限 tab 出现，且保持源顺序。
	moderator := identity.Actor{ID: 6, Status: identity.UserStatusActive, Permissions: map[string]bool{"demo.plugin.moderate": true}}
	tabs, err = provider.ProfileTabs(context.Background(), moderator)
	if err != nil {
		t.Fatalf("moderator ProfileTabs: %v", err)
	}
	if len(tabs) != 3 || tabs[0].ID != "public" || tabs[1].ID != "gated" || tabs[2].ID != "gated-widget" {
		t.Fatalf("moderator tabs = %#v, want public/gated/gated-widget", tabs)
	}

	// 停用账号即使带权限键也拿不到受限 tab（Actor.Can 先看活跃状态）。
	inactive := identity.Actor{ID: 7, Status: identity.UserStatusBanned, Permissions: map[string]bool{"demo.plugin.moderate": true}}
	tabs, err = provider.ProfileTabs(context.Background(), inactive)
	if err != nil {
		t.Fatalf("inactive ProfileTabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "public" {
		t.Fatalf("inactive viewer must only get the public tab, got %#v", tabs)
	}
}

func TestExtensionProfileTabProviderBuildsWidgetTabWithoutURL(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		profileTabContribution("stats", 10, `{"type":"l2Widget","componentId":" Demo.Plugin.Stats "}`),
		// 缺 componentId / 类型未知的 widget 载荷一律丢弃，不产出半成品 tab。
		profileTabContribution("broken", 20, `{"type":"l2Widget"}`),
		profileTabContribution("unknown", 30, `{"type":"iframe","href":"https://evil.example/"}`),
	}}
	tabs, err := NewExtensionProfileTabProvider(source).ProfileTabs(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileTabs: %v", err)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %#v, want exactly the valid widget tab", tabs)
	}
	widget := tabs[0]
	if widget.Kind != "widget" || widget.ComponentID != "demo.plugin.stats" {
		t.Fatalf("widget tab = %#v, want kind=widget componentId=demo.plugin.stats", widget)
	}
	// widget 不走扩展路由代理：URL 与 Method 都必须为空，前端只按 ComponentID 挂载。
	if widget.URL != "" || widget.Method != "" {
		t.Fatalf("widget tab must not carry URL/method, got %#v", widget)
	}
}

func TestExtensionProfileTabProviderCapsWidgetTabs(t *testing.T) {
	items := make([]extensions.EffectiveContribution, 0, 11)
	for index := 0; index < 10; index++ {
		items = append(items, profileTabContribution(
			fmt.Sprintf("widget-%02d", index), index,
			`{"type":"l2Widget","componentId":"demo.plugin.stats"}`,
		))
	}
	items = append(items, profileTabContribution("link", 99, `{"type":"hostLink","href":"/tags"}`))

	tabs, err := NewExtensionProfileTabProvider(fakeContributionSource{items: items}).ProfileTabs(context.Background(), identity.Actor{})
	if err != nil {
		t.Fatalf("ProfileTabs: %v", err)
	}
	if len(tabs) != maxProfileWidgetTabs+1 {
		t.Fatalf("tabs = %d, want %d widgets + 1 link", len(tabs), maxProfileWidgetTabs)
	}
	for index := 0; index < maxProfileWidgetTabs; index++ {
		if tabs[index].Kind != "widget" {
			t.Fatalf("tab %d = %#v, want widget", index, tabs[index])
		}
	}
	// 截断只作用于 widget：链接 tab 仍保留在末尾。
	if last := tabs[len(tabs)-1]; last.Kind != "hostLink" || last.URL != "/tags" {
		t.Fatalf("link tab must survive widget truncation, got %#v", last)
	}
}

func TestExtensionProfileTabProviderPropagatesSourceErrors(t *testing.T) {
	expected := errors.New("list failed")
	_, err := NewExtensionProfileTabProvider(fakeContributionSource{err: expected}).ProfileTabs(context.Background(), identity.Actor{})
	if !errors.Is(err, expected) {
		t.Fatalf("expected source error, got %v", err)
	}
}
