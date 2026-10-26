package extensionmanifest

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSafeArchivePathRejectsCrossPlatformAbsoluteAndTraversalPaths(t *testing.T) {
	unsafePaths := []string{
		"../escape.txt",
		"assets/../../escape.txt",
		"..\\escape.txt",
		"/absolute.txt",
		"\\\\server\\share\\escape.txt",
		"C:\\escape.txt",
		"c:/escape.txt",
		"assets/invalid\x00name.txt",
	}
	for _, unsafePath := range unsafePaths {
		if _, ok := SafeArchivePath(unsafePath); ok {
			t.Fatalf("expected %q to be rejected", unsafePath)
		}
	}

	if got, ok := SafeArchivePath("assets/icons/logo.png"); !ok || got != "assets/icons/logo.png" {
		t.Fatalf("expected ordinary relative path to remain valid, got %q, %v", got, ok)
	}
}

func TestContributionPointDefinitionsContainOnlyHostRenderedDescriptors(t *testing.T) {
	points := ContributionPointDefinitions()
	want := map[string]bool{
		PointForumTopicActions:     true,
		PointForumTopicSidebar:     true,
		PointForumTopicBadges:      true,
		PointForumCommentActions:   true,
		PointForumNavItems:         true,
		PointForumTopicListBadges:  true,
		PointForumComposerToolbar:  true,
		PointForumProfileTabs:      true,
		PointAdminDashboardWidgets: true,
		PointSystemHealthChecks:    true,
		PointForumPageRegions:      true,
	}
	for _, point := range points {
		delete(want, point.ID)
		if point.Kind != ContributionPointKindDescriptor {
			t.Fatalf("executable contribution point survived: %#v", point)
		}
		if point.ID == "admin.test.fixture" {
			t.Fatal("test point leaked into production catalog")
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing production contribution points: %#v", want)
	}
}

func TestF43ContributionPayloadValidation(t *testing.T) {
	base := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.f43",
		Name:            "F43 Demo",
		Description:     "F4.3 contribution payload tests.",
		URL:             "https://example.com/f43",
		Author:          ManifestAuthor{Name: "Demo"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
		Routes: []ManifestRoute{
			{
				ID: "demo.f43.route.action", ContractVersion: "demo.f43.route.action@1", Action: RouteActionAdd,
				Path: "/actions/x", Methods: []string{"POST"}, Guard: GuardCoreLogin, Fallback: "closed",
				Mode: RouteModeHTTP, Handler: "action.handle", RequestSchema: "demo.f43.action.request@1",
				ResponseSchema: "demo.f43.action.response@1",
			},
		},
	}

	okCases := [][]ManifestContribution{
		{{
			Point: PointForumComposerToolbar, ID: "demo.composer", Order: 10,
			Label: map[string]string{"en-US": "Insert"}, Icon: "i-lucide-wand",
			Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/actions/x"}`),
		}},
		{{
			Point: PointForumProfileTabs, ID: "demo.tab", Order: 10,
			Label: map[string]string{"en-US": "Extra"}, Icon: "i-lucide-user",
			Payload: json.RawMessage(`{"type":"hostLink","href":"/tags"}`),
		}},
		{{
			Point: PointForumTopicSidebar, ID: "demo.sidebar", Order: 10,
			Label: map[string]string{"en-US": "Help"}, Icon: "i-lucide-life-buoy",
			Payload: json.RawMessage(`{"type":"hostLink","href":"/help"}`),
		}},
		{{
			Point: PointForumTopicBadges, ID: "demo.badge", Order: 10,
			Label:   map[string]string{"en-US": "Reviewed"},
			Payload: json.RawMessage(`{"tone":"success","href":"/moderation"}`),
		}},
		{{
			Point: PointForumCommentActions, ID: "demo.flag", Order: 10,
			Label: map[string]string{"en-US": "Flag"}, Icon: "i-lucide-flag",
			Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/actions/x","requiresAuth":true}`),
		}},
		{{
			Point: PointForumNavItems, ID: "demo.nav", Order: 10,
			Label: map[string]string{"en-US": "Docs"}, Icon: "i-lucide-book",
			Payload: json.RawMessage(`{"type":"hostLink","href":"/docs"}`),
		}},
		{{
			Point: PointForumTopicListBadges, ID: "demo.list-badge", Order: 10,
			Label:   map[string]string{"en-US": "Hot"},
			Payload: json.RawMessage(`{"tone":"warning"}`),
		}},
		{{
			Point: PointAdminDashboardWidgets, ID: "demo.widget", Order: 10,
			Label: map[string]string{"en-US": "Queue"}, Icon: "i-lucide-gauge",
			Payload: json.RawMessage(`{"type":"adminLink","route":"/jobs","severity":"info"}`),
		}},
		{{
			Point: PointSystemHealthChecks, ID: "demo.health", Order: 10,
			Label:   map[string]string{"en-US": "Demo health"},
			Payload: json.RawMessage(`{"type":"static","component":"extension.demo.f43"}`),
		}},
	}
	for i, contributions := range okCases {
		m := base
		m.Contributions = contributions
		if err := Validate(m); err != nil {
			t.Fatalf("ok case %d should validate: %v", i, err)
		}
	}

	bad := base
	bad.Contributions = []ManifestContribution{{
		Point: PointAdminDashboardWidgets, ID: "evil", Order: 1,
		Label:   map[string]string{"en-US": "Evil"},
		Payload: json.RawMessage(`{"type":"adminLink","route":"https://evil.example/","severity":"info"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("external dashboard route must be rejected")
	}
	bad.Contributions = []ManifestContribution{{
		Point: PointSystemHealthChecks, ID: "evil2", Order: 1,
		Label:   map[string]string{"en-US": "Evil"},
		Payload: json.RawMessage(`{"type":"rpc","component":"x"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("executable/unknown health type must be rejected")
	}
	bad.Contributions = []ManifestContribution{{
		Point: PointForumTopicBadges, ID: "evil-badge", Order: 1,
		Label:   map[string]string{"en-US": "Evil"},
		Payload: json.RawMessage(`{"tone":"rainbow"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("unknown badge tone must be rejected")
	}
	// enabledBySetting 必须指向已声明的 boolean 设置。
	gated := base
	gated.Settings = []ManifestSetting{{
		Key: "show_topic_badge", Label: LocalizedText{Default: "Show badge"}, Type: "boolean", Default: "false",
	}}
	gated.Contributions = []ManifestContribution{{
		Point: PointForumTopicBadges, ID: "gated.badge", Order: 10,
		Label: map[string]string{"en-US": "Policy"}, EnabledBySetting: "show_topic_badge",
		Payload: json.RawMessage(`{"tone":"info","href":"/guidelines"}`),
	}}
	if err := Validate(gated); err != nil {
		t.Fatalf("gated boolean contribution should validate: %v", err)
	}
	gated.Contributions[0].EnabledBySetting = "missing_key"
	if err := Validate(gated); err == nil {
		t.Fatal("enabledBySetting unknown key must be rejected")
	}
	gated.Settings = []ManifestSetting{{
		Key: "mode", Label: LocalizedText{Default: "Mode"}, Type: "text", Default: "reject",
	}}
	gated.Contributions[0].EnabledBySetting = "mode"
	if err := Validate(gated); err == nil {
		t.Fatal("enabledBySetting non-boolean setting must be rejected")
	}
	bad.Contributions = []ManifestContribution{{
		Point: PointForumTopicSidebar, ID: "evil-side", Order: 1,
		Label:   map[string]string{"en-US": "Evil"},
		Payload: json.RawMessage(`{"type":"hostLink","href":"https://evil.example/"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("external sidebar hostLink must be rejected")
	}
	bad.Contributions = []ManifestContribution{{
		Point: PointForumNavItems, ID: "evil-nav", Order: 1,
		Label:   map[string]string{"en-US": "Admin"},
		Payload: json.RawMessage(`{"type":"hostLink","href":"/admin/extensions"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("admin hostLink on public nav must be rejected")
	}
	bad.Contributions = []ManifestContribution{{
		Point: PointForumNavItems, ID: "evil-nav-post", Order: 1,
		Label:   map[string]string{"en-US": "Post"},
		Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/actions/x"}`),
	}}
	if err := Validate(bad); err == nil {
		t.Fatal("non-GET extensionRoute on public nav must be rejected")
	}
}

func TestManifestLangsOptionalAndLocalizedDisplay(t *testing.T) {
	// 无 langs 时保持顶层默认，不要求翻译。
	withoutLangs := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.plugin",
		Name:            "Demo Plugin",
		Description:     "Demo plugin.",
		URL:             "https://example.com/demo",
		Author:          ManifestAuthor{Name: "Demo Studio", URL: "https://example.com"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
	}
	if err := Validate(withoutLangs); err != nil {
		t.Fatalf("manifest without langs should validate: %v", err)
	}
	display := LocalizedDisplay(withoutLangs, "zh-CN")
	if display.Name != "Demo Plugin" || display.Description != "Demo plugin." {
		t.Fatalf("expected top-level defaults without langs, got %#v", display)
	}

	body := []byte(`{
		"manifestVersion":3,
		"id":"demo.plugin",
		"name":"Demo Plugin",
		"description":"Demo plugin.",
		"url":"https://example.com/demo",
		"author":{"name":"Demo Studio","url":"https://example.com"},
		"version":"1.0.0",
		"type":"plugin",
		"sforumVersion":">=0.1.0",
		"langs":{
			"zh":{
				"name":"演示插件",
				"description":"演示插件说明。",
				"author":{"name":"演示工作室"}
			}
		}
	}`)
	var withLangs Manifest
	if err := json.Unmarshal(body, &withLangs); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if err := Validate(withLangs); err != nil {
		t.Fatalf("manifest with langs should validate: %v", err)
	}
	normalized := Normalize(withLangs)
	if _, ok := normalized.Langs["zh"]; !ok {
		t.Fatalf("expected normalized zh locale, got %#v", normalized.Langs)
	}

	// zh-CN 应回退匹配 langs.zh。
	zhDisplay := LocalizedDisplay(withLangs, "zh-CN")
	if zhDisplay.Name != "演示插件" || zhDisplay.Description != "演示插件说明。" || zhDisplay.Author.Name != "演示工作室" {
		t.Fatalf("expected zh overrides, got %#v", zhDisplay)
	}
	// 未覆盖的 author.url 保留顶层默认。
	if zhDisplay.Author.URL != "https://example.com" {
		t.Fatalf("expected author url fallback, got %q", zhDisplay.Author.URL)
	}
	// en 无覆盖时用顶层默认。
	enDisplay := LocalizedDisplay(withLangs, "en-US")
	if enDisplay.Name != "Demo Plugin" || enDisplay.Description != "Demo plugin." {
		t.Fatalf("expected english defaults, got %#v", enDisplay)
	}

	// 无效语言码应拒绝。
	bad := withLangs
	bad.Langs = map[string]ManifestLocale{"   ": {Name: "x"}}
	if err := Validate(bad); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid locale key to fail, got %v", err)
	}
}

func TestAdminManifestV2NormalizeValidateAndResolveManagePath(t *testing.T) {
	body := []byte(`{
		"manifestVersion":3,
		"id":"demo.plugin",
		"name":"Demo Plugin",
		"description":"Demo plugin.",
		"url":"https://example.com/demo",
		"author":{"name":"Demo Studio"},
		"version":"1.0.0",
		"type":"plugin",
		"sforumVersion":">=0.1.0",
		"admin":{
			"entry":"settings",
			"pages":[
				{"path":"settings","label":"Settings","view":"settings"},
				{"path":"/dashboard","label":"Dashboard","view":"about","menu":true,"icon":"i-lucide-layout-dashboard","order":20}
			]
		}
	}`)

	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	normalized := Normalize(manifest)
	if normalized.Admin.Entry != "/settings" {
		t.Fatalf("expected normalized admin entry, got %q", normalized.Admin.Entry)
	}
	pages := EffectiveAdminPages(normalized)
	if len(pages) != 2 {
		t.Fatalf("expected two effective pages, got %#v", pages)
	}
	if pages[0].Path != "/settings" || pages[0].Menu {
		t.Fatalf("settings page should normalize with menu false: %#v", pages[0])
	}
	if pages[1].Path != "/dashboard" || !pages[1].Menu {
		t.Fatalf("dashboard page should be an explicit menu page: %#v", pages[1])
	}
	if AdminManagePath(normalized) != "/settings" {
		t.Fatalf("expected entry to drive manage path, got %q", AdminManagePath(normalized))
	}
	menuPages := MenuAdminPages(normalized)
	if len(menuPages) != 1 || menuPages[0].Path != "/dashboard" {
		t.Fatalf("expected only explicit menu page, got %#v", menuPages)
	}
	if err := Validate(manifest); err != nil {
		t.Fatalf("v2 admin manifest should validate: %v", err)
	}
}

func TestAdminManifestV2RejectsBrokenEntryAndExternalPaths(t *testing.T) {
	base := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.plugin",
		Name:            "Demo Plugin",
		Description:     "Demo plugin.",
		URL:             "https://example.com/demo",
		Author:          ManifestAuthor{Name: "Demo Studio"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
	}

	cases := []struct {
		name     string
		manifest Manifest
	}{
		{
			name: "entry must target declared page or about",
			manifest: func() Manifest {
				next := base
				next.Admin.Entry = "/missing"
				next.Admin.Pages = []ManifestAdminPage{{Path: "/settings", Label: "Settings", View: "settings"}}
				return next
			}(),
		},
		{
			name: "entry cannot be external url",
			manifest: func() Manifest {
				next := base
				next.Admin.Entry = "https://example.com/settings"
				next.Admin.Pages = []ManifestAdminPage{{Path: "/settings", Label: "Settings", View: "settings"}}
				return next
			}(),
		},
		{
			name: "page cannot contain traversal",
			manifest: func() Manifest {
				next := base
				next.Admin.Pages = []ManifestAdminPage{{Path: "/../settings", Label: "Settings", View: "settings"}}
				return next
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected invalid manifest, got %v", err)
			}
		})
	}
}

func TestLegacyAdminPagesRemainCompatible(t *testing.T) {
	manifest := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "legacy.plugin",
		Name:            "Legacy Plugin",
		Description:     "Legacy plugin.",
		URL:             "https://example.com/legacy",
		Author:          ManifestAuthor{Name: "Demo Studio"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
		AdminPages: []ManifestAdminPage{
			{Path: "/settings", Label: "Settings", View: "settings", Menu: true},
		},
	}

	if err := Validate(manifest); err != nil {
		t.Fatalf("legacy adminPages should validate: %v", err)
	}
	normalized := Normalize(manifest)
	if AdminManagePath(normalized) != "/settings" {
		t.Fatalf("expected legacy settings page as manage path, got %q", AdminManagePath(normalized))
	}
	menuPages := MenuAdminPages(normalized)
	if len(menuPages) != 1 || menuPages[0].Path != "/settings" {
		t.Fatalf("expected legacy explicit menu page, got %#v", menuPages)
	}
}

func TestRegionPlacementContributionPayloadValidation(t *testing.T) {
	base := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.regions",
		Name:            "Region Demo",
		Description:     "forum.page.regions payload tests.",
		URL:             "https://example.com/regions",
		Author:          ManifestAuthor{Name: "Demo"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
		Routes: []ManifestRoute{
			{
				ID: "demo.regions.route.ping", ContractVersion: "demo.regions.route.ping@1", Action: RouteActionAdd,
				Path: "/region/ping", Methods: []string{"POST"}, Guard: GuardCoreLogin, Fallback: "closed",
				Mode: RouteModeHTTP, Handler: "region.ping", RequestSchema: "demo.regions.ping.request@1",
				ResponseSchema: "demo.regions.ping.response@1",
			},
		},
	}

	okContributions := [][]ManifestContribution{
		{{
			Point: PointForumPageRegions, ID: "demo.link-card", Order: 10,
			Label: map[string]string{"en-US": "Browse tags"}, Icon: "i-lucide-tags",
			Payload: json.RawMessage(`{"type":"hostLink","pages":["forum.home","forum.tag.index"],"region":"content_after","href":"/tags"}`),
		}},
		{{
			Point: PointForumPageRegions, ID: "demo.action-card", Order: 20,
			Label: map[string]string{"en-US": "Ping"}, Icon: "i-lucide-zap",
			Payload: json.RawMessage(`{"type":"extensionRoute","pages":["forum.topic.show"],"region":"sidebar","method":"POST","path":"/region/ping"}`),
		}},
	}
	for i, contributions := range okContributions {
		m := base
		m.Contributions = contributions
		if err := Validate(m); err != nil {
			t.Fatalf("ok case %d should validate: %v", i, err)
		}
	}

	badPayloads := map[string]string{
		"unknown page":             `{"type":"hostLink","pages":["forum.unknown"],"region":"content_after","href":"/tags"}`,
		"region not on page":       `{"type":"hostLink","pages":["forum.notifications"],"region":"sidebar","href":"/tags"}`,
		"empty pages":              `{"type":"hostLink","pages":[],"region":"content_after","href":"/tags"}`,
		"duplicate pages":          `{"type":"hostLink","pages":["forum.home","forum.home"],"region":"content_after","href":"/tags"}`,
		"external hostLink":        `{"type":"hostLink","pages":["forum.home"],"region":"content_after","href":"https://evil.example/"}`,
		"api hostLink":             `{"type":"hostLink","pages":["forum.home"],"region":"content_after","href":"/api/secrets"}`,
		"hostLink with path":       `{"type":"hostLink","pages":["forum.home"],"region":"content_after","href":"/tags","path":"/x"}`,
		"route with href":          `{"type":"extensionRoute","pages":["forum.home"],"region":"content_after","method":"POST","path":"/region/ping","href":"/tags"}`,
		"route bad method":         `{"type":"extensionRoute","pages":["forum.home"],"region":"content_after","method":"TRACE","path":"/region/ping"}`,
		"widget without component": `{"type":"l2Widget","pages":["forum.home"],"region":"content_after"}`,
		"widget with extras":       `{"type":"l2Widget","pages":["forum.home"],"region":"content_after","componentId":"demo.widget","href":"/x"}`,
		"unknown placement type":   `{"type":"iframe","pages":["forum.home"],"region":"content_after","href":"/tags"}`,
		"widget unknown component": `{"type":"l2Widget","pages":["forum.home"],"region":"content_after","componentId":"demo.widget"}`,
	}
	for name, payload := range badPayloads {
		m := base
		m.Contributions = []ManifestContribution{{
			Point: PointForumPageRegions, ID: "demo.bad", Order: 1,
			Label:   map[string]string{"en-US": "Bad"},
			Payload: json.RawMessage(payload),
		}}
		if err := Validate(m); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("%s must be rejected, got %v", name, err)
		}
	}

	// l2Widget 必须命中本 manifest 内的公开 L2 add 组件（函数级验证，避免整包 L2 校验依赖）。
	withComponent := base
	withComponent.Components = []ManifestComponent{{
		ID: "demo.widget", ContractVersion: "demo.widget@1", Action: ComponentActionAdd,
		L2Component: "assets/widget.mjs", PropsSchema: "schemas/widget-props.json",
	}}
	widgetPayload := json.RawMessage(`{"type":"l2Widget","pages":["forum.topic.show"],"region":"sidebar","componentId":"demo.widget"}`)
	if err := validateRegionPlacementContributionPayload(withComponent, widgetPayload); err != nil {
		t.Fatalf("l2Widget referencing own public add component should validate: %v", err)
	}
	gatedComponent := withComponent
	gatedComponent.Components = []ManifestComponent{{
		ID: "demo.widget", ContractVersion: "demo.widget@1", Action: ComponentActionAdd,
		L2Component: "assets/widget.mjs", PropsSchema: "schemas/widget-props.json", Permission: "demo.perm",
	}}
	if err := validateRegionPlacementContributionPayload(gatedComponent, widgetPayload); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("l2Widget referencing permission-gated component must be rejected, got %v", err)
	}
	ssrOnly := withComponent
	ssrOnly.Components = []ManifestComponent{{
		ID: "demo.widget", ContractVersion: "demo.widget@1", Action: ComponentActionAdd,
		SSRTemplate: "demo.tpl", PropsSchema: "schemas/widget-props.json",
	}}
	if err := validateRegionPlacementContributionPayload(ssrOnly, widgetPayload); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("l2Widget referencing non-L2 component must be rejected, got %v", err)
	}
}

func TestManifestContributionsNormalizeAndValidateTopicAction(t *testing.T) {
	body := []byte(`{
		"manifestVersion":3,
		"id":"demo.plugin",
		"name":"Demo Plugin",
		"description":"Demo plugin.",
		"url":"https://example.com/demo",
		"author":{"name":"Demo Studio"},
		"version":"1.0.0",
		"type":"plugin",
		"sforumVersion":">=0.1.0",
		"contributions":[
			{
				"point":" forum.topic.actions ",
				"id":" Bookmark.Action ",
				"order":200,
				"label":{"zh-CN":"收藏","en-US":"Bookmark"},
				"icon":" i-lucide-bookmark ",
				"payload":{
					"type":"extensionRoute",
					"method":"post",
					"path":"topic-actions/bookmark",
					"confirm":true
				}
			}
		]
	}`)

	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	normalized := Normalize(manifest)
	if len(normalized.Contributions) != 1 {
		t.Fatalf("expected one contribution, got %#v", normalized.Contributions)
	}
	contribution := normalized.Contributions[0]
	if contribution.Point != "forum.topic.actions" || contribution.ID != "bookmark.action" || contribution.Icon != "i-lucide-bookmark" {
		t.Fatalf("contribution was not normalized: %#v", contribution)
	}
	var payload TopicActionContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		t.Fatalf("decode normalized payload: %v", err)
	}
	if payload.Method != "POST" || payload.Path != "/topic-actions/bookmark" || !payload.Confirm {
		t.Fatalf("payload was not normalized: %#v", payload)
	}
	if err := Validate(manifest); err != nil {
		t.Fatalf("valid contribution manifest should validate: %v", err)
	}
}

func TestManifestContributionsRejectUnsafeDeclarations(t *testing.T) {
	base := Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.plugin",
		Name:            "Demo Plugin",
		Description:     "Demo plugin.",
		URL:             "https://example.com/demo",
		Author:          ManifestAuthor{Name: "Demo Studio"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
	}
	valid := ManifestContribution{
		Point: "forum.topic.actions",
		ID:    "demo.bookmark",
		Label: map[string]string{"zh-CN": "收藏", "en-US": "Bookmark"},
		Icon:  "i-lucide-bookmark",
		Payload: json.RawMessage(`{
			"type":"extensionRoute",
			"method":"POST",
			"path":"/topic-actions/bookmark"
		}`),
	}

	cases := []struct {
		name         string
		contribution ManifestContribution
		extra        []ManifestContribution
	}{
		{
			name: "unknown contribution point",
			contribution: func() ManifestContribution {
				next := valid
				next.Point = "forum.unknown"
				return next
			}(),
		},
		{
			name:         "duplicate point and id inside one manifest",
			contribution: valid,
			extra:        []ManifestContribution{valid},
		},
		{
			name: "missing point",
			contribution: func() ManifestContribution {
				next := valid
				next.Point = ""
				return next
			}(),
		},
		{
			name: "missing id",
			contribution: func() ManifestContribution {
				next := valid
				next.ID = ""
				return next
			}(),
		},
		{
			name: "external payload path",
			contribution: func() ManifestContribution {
				next := valid
				next.Payload = json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"https://example.com/action"}`)
				return next
			}(),
		},
		{
			name: "payload path traversal",
			contribution: func() ManifestContribution {
				next := valid
				next.Payload = json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/../action"}`)
				return next
			}(),
		},
		{
			name: "payload path targets core api",
			contribution: func() ManifestContribution {
				next := valid
				next.Payload = json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/api/v1/topics/1"}`)
				return next
			}(),
		},
		{
			name: "unknown method",
			contribution: func() ManifestContribution {
				next := valid
				next.Payload = json.RawMessage(`{"type":"extensionRoute","method":"GET","path":"/topic-actions/bookmark"}`)
				return next
			}(),
		},
		{
			name: "unknown payload type",
			contribution: func() ManifestContribution {
				next := valid
				next.Payload = json.RawMessage(`{"type":"rawHtml","method":"POST","path":"/topic-actions/bookmark"}`)
				return next
			}(),
		},
		{
			name: "unsupported icon prefix",
			contribution: func() ManifestContribution {
				next := valid
				next.Icon = "i-custom-bookmark"
				return next
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := base
			manifest.Contributions = append([]ManifestContribution{tc.contribution}, tc.extra...)
			if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected invalid manifest, got %v", err)
			}
		})
	}
}

// validBaseManifest 构造一个最小合法 manifest，供 C1 Version 校验测试复用。
func validBaseManifest() Manifest {
	return Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.plugin",
		Name:            "Demo",
		Description:     "Demo plugin.",
		URL:             "https://example.com/demo",
		Author:          ManifestAuthor{Name: "Demo"},
		Version:         "1.0.0",
		Type:            "plugin",
		SForumVersion:   ">=0.1.0",
	}
}

// TestManifestVersionRejectsPathTraversal 验证 C1：危险 Version 值（路径穿越）被拒绝。
func TestManifestVersionRejectsPathTraversal(t *testing.T) {
	dangerous := []string{
		"../../../tmp/evil",
		"..",
		"/absolute/path",
		"with/slash",
		"back\\slash",
	}
	for _, v := range dangerous {
		m := validBaseManifest()
		m.Version = v
		if err := Validate(m); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected Version %q to be rejected, got %v", v, err)
		}
	}
}

// TestManifestVersionAcceptsValidSemver 验证 C1：合法版本号仍被接受。
func TestManifestVersionAcceptsValidSemver(t *testing.T) {
	valid := []string{"1.0.0", "2.5.1-beta.1", "1.0.0+build.123", "0.0.1"}
	for _, v := range valid {
		m := validBaseManifest()
		m.Version = v
		if err := Validate(m); err != nil {
			t.Fatalf("expected Version %q to be accepted, got %v", v, err)
		}
	}
}

func TestManifestVersionRejectsNonStrictSemver(t *testing.T) {
	for _, version := range []string{"v1.2.3", "1.0"} {
		manifest := validBaseManifest()
		manifest.Version = version
		if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected non-strict Version %q to be rejected, got %v", version, err)
		}
	}
}

func TestManifestSettingPresentationMetadata(t *testing.T) {
	manifest := validBaseManifest()
	manifest.Settings = []ManifestSetting{{
		Key:              "encryption",
		Label:            LocalizedText{Default: "Encryption"},
		Description:      LocalizedText{Default: "Choose transport security."},
		Type:             "select",
		Placeholder:      LocalizedText{Default: "Select encryption"},
		RecommendedValue: "starttls",
		Group:            LocalizedText{Default: "server"},
		Options: []ManifestSettingOption{
			{Value: "starttls", Label: LocalizedText{Default: "STARTTLS"}, Description: LocalizedText{Default: "Recommended"}},
			{Value: "tls", Label: LocalizedText{Default: "TLS/SSL"}},
		},
	}}

	if err := Validate(manifest); err != nil {
		t.Fatalf("expected presentation metadata to validate: %v", err)
	}
	normalized := Normalize(manifest).Settings[0]
	if normalized.Placeholder.Resolve("") != "Select encryption" || normalized.RecommendedValue != "starttls" || normalized.Group.Resolve("") != "server" {
		t.Fatalf("unexpected normalized metadata: %#v", normalized)
	}
	if len(normalized.Options) != 2 || normalized.Options[0].Value != "starttls" {
		t.Fatalf("expected ordered options, got %#v", normalized.Options)
	}
	if normalized.Width != SettingWidthDefault {
		t.Fatalf("expected default width, got %q", normalized.Width)
	}
}

func TestManifestSettingTextareaAndWidth(t *testing.T) {
	manifest := validBaseManifest()
	manifest.Settings = []ManifestSetting{{
		Key:   "notes",
		Label: LocalizedText{Default: "Notes"},
		Type:  "textarea",
		Width: "full",
	}}
	if err := Validate(manifest); err != nil {
		t.Fatalf("expected textarea+full to validate: %v", err)
	}
	normalized := Normalize(manifest).Settings[0]
	if normalized.Type != "textarea" || normalized.Width != SettingWidthFull {
		t.Fatalf("unexpected normalized textarea setting: %#v", normalized)
	}

	manifest.Settings[0].Width = "wide"
	if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid width to fail, got %v", err)
	}
}

func TestManifestSettingPresentationMetadataRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name        string
		options     []ManifestSettingOption
		recommended string
	}{
		{name: "blank value", options: []ManifestSettingOption{{Value: "", Label: LocalizedText{Default: "Blank"}}}, recommended: ""},
		{name: "blank label", options: []ManifestSettingOption{{Value: "tls", Label: LocalizedText{}}}, recommended: "tls"},
		{name: "duplicate value", options: []ManifestSettingOption{{Value: "tls", Label: LocalizedText{Default: "TLS"}}, {Value: "tls", Label: LocalizedText{Default: "Again"}}}, recommended: "tls"},
		{name: "unknown recommendation", options: []ManifestSettingOption{{Value: "tls", Label: LocalizedText{Default: "TLS"}}}, recommended: "starttls"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validBaseManifest()
			manifest.Settings = []ManifestSetting{{Key: "encryption", Label: LocalizedText{Default: "Encryption"}, Type: "select", Options: tc.options, RecommendedValue: tc.recommended}}
			if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected invalid setting metadata, got %v", err)
			}
		})
	}
}
