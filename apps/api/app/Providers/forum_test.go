package providers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestRecommendedForumSettingsIncludePaginationDefaults(t *testing.T) {
	settings := recommendedForumSettings()
	if settings.TopicsPerPage != 20 || settings.CommentsPerPage != 20 {
		t.Fatalf("pagination defaults = %d/%d, want 20/20", settings.TopicsPerPage, settings.CommentsPerPage)
	}
	for _, tc := range []struct {
		value string
		want  int
		ok    bool
	}{{"1", 1, true}, {"100", 100, true}, {"0", 0, false}, {"101", 0, false}} {
		got, ok := normalizeForumPageSize(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("normalizeForumPageSize(%q) = %d/%v, want %d/%v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestExtensionTopicActionProviderBuildsSafeDescriptors(t *testing.T) {
	payload, err := json.Marshal(extensions.TopicActionContributionPayload{
		Type:    "extensionRoute",
		Method:  "POST",
		Path:    "/topic-actions/bookmark",
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		{
			ExtensionID:   "demo.plugin",
			ExtensionName: "Demo Plugin",
			Point:         "forum.topic.actions",
			ID:            "demo.bookmark",
			Order:         100,
			Label:         map[string]string{"zh-CN": "收藏", "en-US": "Bookmark"},
			Icon:          "i-lucide-bookmark",
			Payload:       payload,
		},
		{
			ExtensionID: "demo.plugin",
			Point:       "forum.topic.actions",
			ID:          "demo.bad",
			Payload:     json.RawMessage(`{"type":"extensionRoute","method":"GET","path":"/topic-actions/bad"}`),
		},
		{
			ExtensionID: "other.plugin",
			Point:       "admin.pages",
			ID:          "other.page",
			Payload:     payload,
		},
	}}

	actions, err := NewExtensionTopicActionProvider(source).TopicExtensionActions(context.Background())
	if err != nil {
		t.Fatalf("TopicExtensionActions returned error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one safe action, got %#v", actions)
	}
	action := actions[0]
	if action.ExtensionID != "demo.plugin" || action.ID != "demo.bookmark" || action.URL != "/extensions/demo.plugin/topic-actions/bookmark" {
		t.Fatalf("unexpected action descriptor: %#v", action)
	}
	if action.Method != "POST" || !action.Confirm || action.Label["zh-CN"] != "收藏" || action.Icon != "i-lucide-bookmark" {
		t.Fatalf("unexpected action payload fields: %#v", action)
	}
}

func TestExtensionTopicActionProviderPropagatesSourceErrors(t *testing.T) {
	expected := errors.New("list failed")
	_, err := NewExtensionTopicActionProvider(fakeContributionSource{err: expected}).TopicExtensionActions(context.Background())
	if !errors.Is(err, expected) {
		t.Fatalf("expected source error, got %v", err)
	}
}

func TestExtensionComposerToolbarProvider(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"type": "extensionRoute", "method": "POST", "path": "/composer/insert",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := fakeContributionSource{items: []extensions.EffectiveContribution{{
		ExtensionID: "demo.plugin",
		Point:       "forum.composer.toolbar",
		ID:          "insert",
		Order:       50,
		Label:       map[string]string{"en-US": "Insert"},
		Icon:        "i-lucide-wand",
		Payload:     payload,
	}}}
	actions, err := NewExtensionComposerToolbarProvider(source).ComposerToolbarActions(context.Background())
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions=%#v err=%v", actions, err)
	}
	if actions[0].URL != "/extensions/demo.plugin/composer/insert" || actions[0].Method != "POST" {
		t.Fatalf("unexpected %#v", actions[0])
	}
}

func TestExtensionProfileTabAndDashboardProviders(t *testing.T) {
	tabPayload, _ := json.Marshal(map[string]any{"type": "hostLink", "href": "/tags"})
	widgetPayload, _ := json.Marshal(map[string]any{"type": "adminLink", "route": "/jobs", "severity": "warning"})
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		{ExtensionID: "demo.plugin", Point: "forum.profile.tabs", ID: "tags", Order: 1, Label: map[string]string{"en-US": "Tags"}, Payload: tabPayload},
		{ExtensionID: "demo.plugin", Point: "admin.dashboard.widgets", ID: "jobs", Order: 2, Label: map[string]string{"en-US": "Jobs"}, Icon: "i-lucide-gauge", Payload: widgetPayload},
		{ExtensionID: "demo.plugin", Point: "admin.dashboard.widgets", ID: "evil", Payload: json.RawMessage(`{"type":"adminLink","route":"https://evil/","severity":"info"}`)},
	}}
	tabs, err := NewExtensionProfileTabProvider(source).ProfileTabs(context.Background())
	if err != nil || len(tabs) != 1 || tabs[0].URL != "/tags" || tabs[0].Kind != "hostLink" {
		t.Fatalf("tabs=%#v err=%v", tabs, err)
	}
	widgets, err := NewExtensionDashboardWidgetProvider(source).DashboardWidgets(context.Background())
	if err != nil || len(widgets) != 1 || widgets[0].Route != "/jobs" || widgets[0].Severity != "warning" {
		t.Fatalf("widgets=%#v err=%v", widgets, err)
	}
}

func TestExtensionTopicSurfaceProvider(t *testing.T) {
	sidebarHost, _ := json.Marshal(map[string]any{"type": "hostLink", "href": "/help/policy"})
	sidebarRoute, _ := json.Marshal(map[string]any{"type": "extensionRoute", "method": "GET", "path": "/topic/related"})
	badgePayload, _ := json.Marshal(map[string]any{"tone": "warning", "href": "/moderation"})
	listBadgePayload, _ := json.Marshal(map[string]any{"tone": "info"})
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		{ExtensionID: "demo.plugin", Point: "forum.topic.sidebar", ID: "policy", Order: 20, Label: map[string]string{"zh-CN": "发帖规范"}, Icon: "i-lucide-book-open", Payload: sidebarHost},
		{ExtensionID: "demo.plugin", Point: "forum.topic.sidebar", ID: "related", Order: 10, Label: map[string]string{"en-US": "Related"}, Icon: "i-tabler-link", Payload: sidebarRoute},
		{ExtensionID: "demo.plugin", Point: "forum.topic.sidebar", ID: "evil", Payload: json.RawMessage(`{"type":"hostLink","href":"https://evil/"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.topic.badges", ID: "review", Order: 5, Label: map[string]string{"zh-CN": "待审"}, Payload: badgePayload},
		{ExtensionID: "demo.plugin", Point: "forum.topic.badges", ID: "bad-tone", Payload: json.RawMessage(`{"tone":"rainbow"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.topic.list.badges", ID: "hot", Order: 1, Label: map[string]string{"zh-CN": "热"}, Payload: listBadgePayload},
		{ExtensionID: "demo.plugin", Point: "forum.topic.list.badges", ID: "evil-list", Payload: json.RawMessage(`{"tone":"warning","href":"https://evil/"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.topic.actions", ID: "ignored", Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/x"}`)},
	}}
	provider := NewExtensionTopicSurfaceProvider(source)

	sidebar, err := provider.TopicExtensionSidebar(context.Background())
	if err != nil {
		t.Fatalf("sidebar error: %v", err)
	}
	if len(sidebar) != 2 {
		t.Fatalf("expected 2 safe sidebar items, got %#v", sidebar)
	}
	// EffectiveContributions 已按 order 排序；provider 保持源顺序。
	if sidebar[0].ID != "policy" || sidebar[0].Kind != "hostLink" || sidebar[0].URL != "/help/policy" {
		t.Fatalf("unexpected first sidebar item: %#v", sidebar[0])
	}
	if sidebar[1].ID != "related" || sidebar[1].Kind != "extensionRoute" || sidebar[1].Method != "GET" {
		t.Fatalf("unexpected second sidebar item: %#v", sidebar[1])
	}
	if sidebar[1].URL != "/extensions/demo.plugin/topic/related" {
		t.Fatalf("unexpected sidebar proxy url: %#v", sidebar[1])
	}

	badges, err := provider.TopicExtensionBadges(context.Background())
	if err != nil || len(badges) != 1 {
		t.Fatalf("badges=%#v err=%v", badges, err)
	}
	if badges[0].ID != "review" || badges[0].Tone != "warning" || badges[0].Href != "/moderation" {
		t.Fatalf("unexpected badge: %#v", badges[0])
	}

	listBadges, err := provider.TopicExtensionListBadges(context.Background())
	if err != nil || len(listBadges) != 1 {
		t.Fatalf("list badges=%#v err=%v", listBadges, err)
	}
	if listBadges[0].ID != "hot" || listBadges[0].Tone != "info" {
		t.Fatalf("unexpected list badge: %#v", listBadges[0])
	}
}

func TestExtensionTopicSurfaceProviderPropagatesSourceErrors(t *testing.T) {
	expected := errors.New("list failed")
	provider := NewExtensionTopicSurfaceProvider(fakeContributionSource{err: expected})
	if _, err := provider.TopicExtensionSidebar(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("sidebar want source error, got %v", err)
	}
	if _, err := provider.TopicExtensionBadges(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("badges want source error, got %v", err)
	}
	if _, err := provider.TopicExtensionListBadges(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("list badges want source error, got %v", err)
	}
}

func TestExtensionNavItemProviderBuildsSafeDescriptors(t *testing.T) {
	hostPayload, _ := json.Marshal(map[string]any{"type": "hostLink", "href": "/docs"})
	routePayload, _ := json.Marshal(map[string]any{"type": "extensionRoute", "method": "GET", "path": "/public/page"})
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		{ExtensionID: "demo.plugin", Point: "forum.nav.items", ID: "docs", Order: 20, Label: map[string]string{"zh-CN": "文档"}, Icon: "i-lucide-book", Payload: hostPayload},
		{ExtensionID: "demo.plugin", Point: "forum.nav.items", ID: "page", Order: 10, Label: map[string]string{"en-US": "Page"}, Icon: "i-tabler-link", Payload: routePayload},
		{ExtensionID: "demo.plugin", Point: "forum.nav.items", ID: "admin", Payload: json.RawMessage(`{"type":"hostLink","href":"/admin/extensions"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.nav.items", ID: "post", Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/x"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.nav.items", ID: "external", Payload: json.RawMessage(`{"type":"hostLink","href":"https://evil/"}`)},
		{ExtensionID: "demo.plugin", Point: "forum.topic.actions", ID: "ignored", Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/x"}`)},
	}}
	items, err := NewExtensionNavItemProvider(source).ExtensionNavItems(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if items[0].ID != "docs" || items[0].Kind != "hostLink" || items[0].URL != "/docs" {
		t.Fatalf("unexpected first nav item: %#v", items[0])
	}
	if items[1].ID != "page" || items[1].Kind != "extensionRoute" || items[1].URL != "/extensions/demo.plugin/public/page" {
		t.Fatalf("unexpected second nav item: %#v", items[1])
	}
}

func TestExtensionCommentActionProviderBuildsSafeDescriptors(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"type": "extensionRoute", "method": "POST", "path": "/comment-actions/flag", "confirm": true, "requiresAuth": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		{
			ExtensionID: "demo.plugin",
			Point:       "forum.comment.actions",
			ID:          "demo.flag",
			Label:       map[string]string{"zh-CN": "标记"},
			Icon:        "i-lucide-flag",
			Payload:     payload,
		},
		{
			ExtensionID: "demo.plugin",
			Point:       "forum.comment.actions",
			ID:          "demo.bad-get",
			Payload:     json.RawMessage(`{"type":"extensionRoute","method":"GET","path":"/comment-actions/bad"}`),
		},
		{
			ExtensionID: "demo.plugin",
			Point:       "forum.topic.actions",
			ID:          "ignored",
			Payload:     payload,
		},
	}}
	actions, err := NewExtensionCommentActionProvider(source).CommentExtensionActions(context.Background())
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions=%#v err=%v", actions, err)
	}
	action := actions[0]
	if action.ExtensionID != "demo.plugin" || action.ID != "demo.flag" {
		t.Fatalf("unexpected action: %#v", action)
	}
	if action.URL != "/extensions/demo.plugin/comment-actions/flag" || action.Method != "POST" {
		t.Fatalf("unexpected route fields: %#v", action)
	}
	if !action.Confirm || !action.RequiresAuth || action.Label["zh-CN"] != "标记" {
		t.Fatalf("unexpected payload fields: %#v", action)
	}
}

type fakeContributionSource struct {
	items []extensions.EffectiveContribution
	err   error
}

func (s fakeContributionSource) EffectiveContributions(context.Context) ([]extensions.EffectiveContribution, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}
