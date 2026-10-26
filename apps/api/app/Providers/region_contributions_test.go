package providers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func regionContribution(id string, order int, payload string) extensions.EffectiveContribution {
	return extensions.EffectiveContribution{
		ExtensionID: "demo.plugin",
		Point:       "forum.page.regions",
		ID:          id,
		Order:       order,
		Label:       map[string]string{"en-US": "Demo"},
		Icon:        "i-lucide-box",
		Payload:     json.RawMessage(payload),
	}
}

func TestExtensionPageRegionProviderBuildsSafeDescriptors(t *testing.T) {
	source := fakeContributionSource{items: []extensions.EffectiveContribution{
		regionContribution("demo.link", 10, `{"type":"hostLink","pages":["forum.home","forum.tag.index"],"region":"content_after","href":"/tags"}`),
		regionContribution("demo.action", 20, `{"type":"extensionRoute","pages":["forum.home"],"region":"content_after","method":"POST","path":"/region/ping"}`),
		regionContribution("demo.widget", 30, `{"type":"l2Widget","pages":["forum.home"],"region":"sidebar","componentId":"demo.widget.component"}`),
		// 非本页贡献:不应出现在 forum.home。
		regionContribution("demo.other-page", 5, `{"type":"hostLink","pages":["forum.topic.show"],"region":"content_before","href":"/guidelines"}`),
		// 非法内容:目录外 region、不安全 href、跨协议路径,全部丢弃。
		regionContribution("demo.bad-region", 1, `{"type":"hostLink","pages":["forum.home"],"region":"header","href":"/tags"}`),
		regionContribution("demo.bad-href", 1, `{"type":"hostLink","pages":["forum.home"],"region":"content_after","href":"https://evil.example/"}`),
		regionContribution("demo.bad-path", 1, `{"type":"extensionRoute","pages":["forum.home"],"region":"content_after","method":"POST","path":"/api/secrets"}`),
	}}
	provider := NewExtensionPageRegionProvider(source)

	regions, err := provider.PageRegions(context.Background(), "forum.home")
	if err != nil {
		t.Fatalf("PageRegions: %v", err)
	}
	if len(regions) != 2 {
		t.Fatalf("regions = %d, want 2 (content_after + sidebar)", len(regions))
	}
	// 区域按目录顺序:content_before(空,省略)→ content_after → sidebar。
	contentAfter := regions[0]
	if contentAfter.ID != "content_after" || contentAfter.Kind != "content" {
		t.Fatalf("first region = %+v, want content_after/content", contentAfter)
	}
	if len(contentAfter.Items) != 2 {
		t.Fatalf("content_after items = %d, want 2", len(contentAfter.Items))
	}
	if contentAfter.Items[0].Kind != "link" || contentAfter.Items[0].Href != "/tags" {
		t.Fatalf("link item = %+v", contentAfter.Items[0])
	}
	if contentAfter.Items[1].Kind != "action" || contentAfter.Items[1].Method != "POST" || contentAfter.Items[1].Path != "/region/ping" {
		t.Fatalf("action item = %+v", contentAfter.Items[1])
	}
	sidebar := regions[1]
	if sidebar.ID != "sidebar" || len(sidebar.Items) != 1 {
		t.Fatalf("sidebar = %+v", sidebar)
	}
	widget := sidebar.Items[0]
	if widget.Kind != "widget" || widget.Widget == nil ||
		widget.Widget.ExtensionID != "demo.plugin" || widget.Widget.ComponentID != "demo.widget.component" {
		t.Fatalf("widget item = %+v", widget)
	}

	// 其他页只看到自己的贡献。
	topicRegions, err := provider.PageRegions(context.Background(), "forum.topic.show")
	if err != nil {
		t.Fatalf("PageRegions(topic): %v", err)
	}
	if len(topicRegions) != 1 || topicRegions[0].ID != "content_before" || len(topicRegions[0].Items) != 1 {
		t.Fatalf("topic regions = %+v", topicRegions)
	}
}

func TestExtensionPageRegionProviderUnknownPageIsNil(t *testing.T) {
	provider := NewExtensionPageRegionProvider(fakeContributionSource{})
	regions, err := provider.PageRegions(context.Background(), "admin.dashboard")
	if err != nil || regions != nil {
		t.Fatalf("unknown page = %+v/%v, want nil/nil", regions, err)
	}
}

func TestExtensionPageRegionProviderPropagatesSourceError(t *testing.T) {
	expected := errors.New("boom")
	_, err := NewExtensionPageRegionProvider(fakeContributionSource{err: expected}).PageRegions(context.Background(), "forum.home")
	if !errors.Is(err, expected) {
		t.Fatalf("err = %v, want %v", err, expected)
	}
}
