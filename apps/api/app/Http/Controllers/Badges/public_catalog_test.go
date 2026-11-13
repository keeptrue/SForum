package badgescontroller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 公开徽章目录（GET /api/v1/badges）的 HTTP 层测试。
//
// 领域测试证明「目录含未获得、空组省略、失败上抛」；这里证明 wire 形状、
// 缓存头、开关关闭态与匿名可达性——也就是前端与 CDN 真正看到的那一层。

// fakeCatalogReader 注入目录形状，避开完整 Store 实现。
type fakeCatalogReader struct {
	catalog  badges.PublicCatalog
	earned   map[string]struct{}
	progress map[string]badges.BadgeProgress
	err      error
	earnedID int64
	// progressID / progressReads 钉住「进度只给登录查看者算」：匿名的 progressReads
	// 必须是 0（目录页对匿名是纯公开读，不该为一个不存在的「我」去测量）。
	progressID    int64
	progressReads int
}

func (r *fakeCatalogReader) PublicCatalog(context.Context) (badges.PublicCatalog, error) {
	if r.err != nil {
		return badges.PublicCatalog{}, r.err
	}
	return r.catalog, nil
}

func (r *fakeCatalogReader) EarnedBadgeSlugs(_ context.Context, userID int64) (map[string]struct{}, error) {
	r.earnedID = userID
	if r.earned == nil {
		return map[string]struct{}{}, nil
	}
	return r.earned, nil
}

func (r *fakeCatalogReader) ViewerCatalogProgress(_ context.Context, userID int64) map[string]badges.BadgeProgress {
	r.progressReads++
	r.progressID = userID
	if r.progress == nil {
		return map[string]badges.BadgeProgress{}
	}
	return r.progress
}

func catalogFixture() badges.PublicCatalog {
	return badges.PublicCatalog{
		Groups: []badges.PublicCatalogGroup{
			{
				Key:  "starter",
				Name: "起步",
				Badges: []badges.PublicCatalogEntry{
					{Slug: "first-post", Name: "首帖", Description: "发第一篇主题", Icon: "i-lucide-award", Tier: badges.TierBronze, GroupKey: "starter", GroupName: "起步"},
					{Slug: "ten-posts", Name: "十帖", Description: "", Icon: "i-lucide-award", Tier: badges.TierSilver, GroupKey: "starter", GroupName: "起步"},
				},
			},
		},
	}
}

func newCatalogApp(t *testing.T, controller *Controller) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			return c.Status(fiberErr.Code).JSON(fiber.Map{"code": fiberErr.Code, "message": fiberErr.Message})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"code": 500, "message": err.Error()})
	}})
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)
	return app
}

func getCatalog(t *testing.T, app *fiber.App) (int, string, string) {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/api/v1/badges", nil), fiber.TestConfig{Timeout: 5000})
	if err != nil {
		t.Fatalf("GET /api/v1/badges: %v", err)
	}
	defer response.Body.Close()
	payload := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buf)
		payload.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return response.StatusCode, response.Header.Get(fiber.HeaderCacheControl), payload.String()
}

// B-01：未登录会话得 200（public guard，不挂 badge.view）。
// B-01：未登录会话得 200（public guard，不挂 badge.view）。
//
// 缓存头不在这里断言：那条契约有具名测试 TestPublicBadgeCatalogIsPrivateCacheable
// （B-29）。两个契约各自一个可点名的测试，别把一条测试当两条用。
func TestPublicCatalogServesAnonymous(t *testing.T) {
	reader := &fakeCatalogReader{catalog: catalogFixture()}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("anonymous GET /api/v1/badges: want 200, got %d (%s)", status, body)
	}
	if !strings.Contains(body, `"first-post"`) {
		t.Fatalf("catalog body missing badge entry: %s", body)
	}
}

// B-29：响应含查看者自己的 earned，缓存头必须是 private。
//
// 与资料页徽章墙的 setPublicCacheable（"public, max-age=30"）互斥：那个端点的
// 响应是**被查看者**的 earned，对所有人相同；本端点是**查看者**的。一旦照抄，
// 共享缓存会把 A 的 earned 投给 B。本控制器也没有 Vary 兜底。
func TestPublicBadgeCatalogIsPrivateCacheable(t *testing.T) {
	reader := &fakeCatalogReader{catalog: catalogFixture()}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, cacheControl, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if cacheControl != "private, no-store" {
		t.Fatalf("want Cache-Control: private, got %q", cacheControl)
	}
	if cacheControl == "public, max-age=30" {
		t.Fatal("public cache header on a viewer-scoped response would leak one viewer's earned marks to another")
	}
}

// B-03 的 wire 一半：**匿名**响应的条目键集合精确相等（不是包含），
// 定义级 metric / threshold 与运营字段都不存在。
//
// 登录查看者的进度键由 TestPublicCatalogMergesViewerProgress 单独钉住：
// 那对键是 per-viewer 装饰，不属于匿名目录的契约。
func TestPublicCatalogWireKeysAreWhitelisted(t *testing.T) {
	reader := &fakeCatalogReader{catalog: catalogFixture()}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %s", body)
	}
	assertKeys(t, "response", data, []string{"groups", "truncated"})

	groups, ok := data["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("want 1 group, got %v", data["groups"])
	}
	group, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("group is not an object: %v", groups[0])
	}
	assertKeys(t, "group", group, []string{"key", "name", "badges"})

	items, ok := group["badges"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("want 2 badge entries, got %v", group["badges"])
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("badge entry is not an object: %v", raw)
		}
		// tierName / tierColor 是等级表下发的展示外观：tier 仍是 key（前端 data-tier 用它），
		// 名字与颜色由服务端给，前端不该按 key 硬编码。
		assertKeys(t, "badge entry", item, []string{"slug", "name", "description", "icon", "tier", "tierName", "tierColor", "group", "earned", "requirementText"})
		for _, forbidden := range []string{"threshold", "metric", "enabled", "grantedCount", "createdAt", "updatedAt", "id"} {
			if _, present := item[forbidden]; present {
				t.Fatalf("public catalog entry must not carry %q: %v", forbidden, item)
			}
		}
	}
}

// B-03 的结构一半：目录 DTO 上不得存在**定义级**的 Metric 字段。
//
// 只测 wire 会被 omitempty 骗过：字段存在、JSON 里没键，测试全绿但下一次
// 有人删掉 tag 就泄露。所以字段名与 json tag 都做精确集合断言。
//
// threshold 的语义在 M3 之后分成两种：定义级阈值（运营调参，绝不下发）与
// 查看者进度阈值（未获得 + 可测的条目才有）。前者由 `PublicCatalogEntry`
// **结构上没有该字段**保证（本测试第二段直接核对），后者是本 DTO 的
// current/threshold 一对，只在登录且可测时出现（见 TestPublicCatalogMergesViewerProgress）。
func TestPublicCatalogDTOCarriesNoDefinitionMetricField(t *testing.T) {
	cases := []struct {
		name   string
		typ    reflect.Type
		fields []string
		tags   []string
	}{
		{
			name:   "response",
			typ:    reflect.TypeOf(publicBadgeCatalogResponse{}),
			fields: []string{"Groups", "Truncated"},
			tags:   []string{"groups", "truncated"},
		},
		{
			name:   "group",
			typ:    reflect.TypeOf(publicBadgeCatalogGroup{}),
			fields: []string{"Key", "Name", "Badges"},
			tags:   []string{"key", "name", "badges"},
		},
		{
			name: "entry",
			typ:  reflect.TypeOf(publicBadgeCatalogItem{}),
			// requirementText / current / threshold 与 OpenAPI 的
			// PublicBadgeCatalogEntry 同步：「过滤后重建结构」最容易漏这类新键，
			// 所以这里做白名单式断言。
			fields: []string{"Slug", "Name", "Description", "Icon", "Tier", "TierName", "TierColor", "Group", "Earned", "RequirementText", "Current", "Threshold"},
			tags:   []string{"slug", "name", "description", "icon", "tier", "tierName", "tierColor", "group", "earned", "requirementText", "current", "threshold"},
		},
	}
	for _, tc := range cases {
		gotFields := make([]string, 0, tc.typ.NumField())
		gotTags := make([]string, 0, tc.typ.NumField())
		for index := 0; index < tc.typ.NumField(); index++ {
			field := tc.typ.Field(index)
			gotFields = append(gotFields, field.Name)
			gotTags = append(gotTags, strings.Split(field.Tag.Get("json"), ",")[0])
			if field.Name == "Metric" {
				t.Fatalf("public catalog %s must not declare definition field %q", tc.name, field.Name)
			}
		}
		assertKeys(t, tc.name+" struct fields", namesToSet(gotFields), tc.fields)
		assertKeys(t, tc.name+" json tags", namesToSet(gotTags), tc.tags)
	}

	// 定义级阈值/指标的结构保证：领域目录条目上根本没有这两个字段，
	// 所以「公开目录泄露运营阈值」在类型层面不可能发生——进度阈值走的是
	// 另一个来源（BadgeProgress），不会顺手把定义带出来。
	entryType := reflect.TypeOf(badges.PublicCatalogEntry{})
	for index := 0; index < entryType.NumField(); index++ {
		name := entryType.Field(index).Name
		if name == "Threshold" || name == "Metric" {
			t.Fatalf("PublicCatalogEntry must not carry definition field %q", name)
		}
	}
}

// B-10：空目录必须序列化成 `"groups": []`，不是 null。
func TestPublicCatalogEmptyCatalogSerializesEmptyArray(t *testing.T) {
	reader := &fakeCatalogReader{catalog: badges.PublicCatalog{Groups: []badges.PublicCatalogGroup{}}}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if !strings.Contains(body, `"groups":[]`) {
		t.Fatalf("want groups=[] (empty array), got %s", body)
	}
	if strings.Contains(body, `"groups":null`) {
		t.Fatalf("want groups=[] (empty array), got null: %s", body)
	}
	if !strings.Contains(body, `"truncated":false`) {
		t.Fatalf("want truncated=false, got %s", body)
	}
}

// B-09①：features.badges 关闭 → 403 forum.badges_disabled（与其余十三条同口径）。
func TestPublicCatalogFeatureDisabledReturns403(t *testing.T) {
	reader := &fakeCatalogReader{catalog: catalogFixture()}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: false}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusForbidden {
		t.Fatalf("want 403 forum.badges_disabled, got %d (%s)", status, body)
	}
	if !strings.Contains(body, badges.CodeBadgesDisabled) {
		t.Fatalf("want reason %s, got %s", badges.CodeBadgesDisabled, body)
	}
}

// B-11：读库失败 → 500，不得 200 + 空目录。
func TestPublicCatalogStoreFailureIs500(t *testing.T) {
	reader := &fakeCatalogReader{err: errors.New("db is down")}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusInternalServerError {
		t.Fatalf("store failure must fail closed: want 500, got %d (%s)", status, body)
	}
	if strings.Contains(body, `"groups"`) {
		t.Fatalf("failure response must not carry an empty catalog: %s", body)
	}
}

// B-02 的 wire 一半：earned 由查看者集合合并，未获得为 false。
func TestPublicCatalogMergesViewerEarned(t *testing.T) {
	reader := &fakeCatalogReader{
		catalog: catalogFixture(),
		earned:  map[string]struct{}{"first-post": {}},
	}
	controller := (&Controller{catalog: reader, gate: fakeGate{enabled: true}}).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
		})

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if reader.earnedID != 42 {
		t.Fatalf("earned must be resolved for the viewer: want userID 42, got %d", reader.earnedID)
	}
	if !strings.Contains(body, `"slug":"first-post"`) || !strings.Contains(body, `"earned":true`) {
		t.Fatalf("want first-post earned=true: %s", body)
	}
	// 两条条目里恰好一条 earned=true。
	if got := strings.Count(body, `"earned":true`); got != 1 {
		t.Fatalf("want exactly 1 earned entry, got %d (%s)", got, body)
	}
}

// B-02 的进度一半：登录查看者拿到**值级**进度，已获得的行不给这两个键。
//
// 值级断言的理由与资料页同款：只断言「键存在」会让 `current:0`（空值）和
// 「真的测到 3」看起来一样，而本仓的历史坑就是「键在值空」。
func TestPublicCatalogMergesViewerProgress(t *testing.T) {
	reader := &fakeCatalogReader{
		catalog: catalogFixture(),
		earned:  map[string]struct{}{"first-post": {}},
		// 两条都配了进度：earned 的 first-post 必须被丢弃，未获得的 ten-posts 才输出。
		progress: map[string]badges.BadgeProgress{
			"first-post": {Current: 1, Threshold: 1},
			"ten-posts":  {Current: 3, Threshold: 10},
		},
	}
	controller := (&Controller{catalog: reader, gate: fakeGate{enabled: true}}).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
		})

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if reader.progressID != 42 {
		t.Fatalf("progress must be measured for the viewer: want userID 42, got %d", reader.progressID)
	}
	items := catalogItemsBySlug(t, body)

	ten := items["ten-posts"]
	if current, ok := ten["current"].(float64); !ok || current != 3 {
		t.Fatalf("ten-posts current 必须是数字 3，得到 %#v", ten["current"])
	}
	if threshold, ok := ten["threshold"].(float64); !ok || threshold != 10 {
		t.Fatalf("ten-posts threshold 必须是数字 10，得到 %#v", ten["threshold"])
	}

	earned := items["first-post"]
	for _, key := range []string{"current", "threshold"} {
		if _, present := earned[key]; present {
			t.Fatalf("earned 行不得输出 %q：%v", key, earned)
		}
	}
}

// 匿名：目录对匿名是纯公开读，不查 earned、也不测量进度，两个键都不出现。
func TestPublicCatalogAnonymousSkipsProgress(t *testing.T) {
	reader := &fakeCatalogReader{
		catalog:  catalogFixture(),
		progress: map[string]badges.BadgeProgress{"ten-posts": {Current: 3, Threshold: 10}},
	}
	controller := &Controller{catalog: reader, gate: fakeGate{enabled: true}}

	status, _, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if reader.progressReads != 0 {
		t.Fatalf("anonymous catalog must not measure progress, got %d reads", reader.progressReads)
	}
	if reader.earnedID != 0 {
		t.Fatalf("anonymous catalog must not resolve earned, got userID %d", reader.earnedID)
	}
	for slug, item := range catalogItemsBySlug(t, body) {
		for _, key := range []string{"current", "threshold"} {
			if _, present := item[key]; present {
				t.Fatalf("anonymous item %q must not carry %q: %v", slug, key, item)
			}
		}
	}
}

// 目录响应带查看者进度时，缓存头仍必须是 private + no-store，且不得出现 public。
//
// 这是 B-29 的加强版：进度与 earned 一样是 per-viewer 数据，一旦 public，
// A 的进度会被共享缓存投给 B。
func TestPublicCatalogProgressKeepsPrivateCacheHeader(t *testing.T) {
	reader := &fakeCatalogReader{
		catalog:  catalogFixture(),
		progress: map[string]badges.BadgeProgress{"ten-posts": {Current: 3, Threshold: 10}},
	}
	controller := (&Controller{catalog: reader, gate: fakeGate{enabled: true}}).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
		})

	status, cacheControl, body := getCatalog(t, newCatalogApp(t, controller))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if cacheControl != "private, no-store" {
		t.Fatalf("want Cache-Control: private, no-store, got %q", cacheControl)
	}
	if strings.Contains(cacheControl, "public") {
		t.Fatalf("viewer-scoped progress must never be publicly cacheable, got %q", cacheControl)
	}
}

// catalogItemsBySlug 把目录响应摊平成 slug → 条目，便于值级断言。
func catalogItemsBySlug(t *testing.T, body string) map[string]map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %s", body)
	}
	groups, ok := data["groups"].([]any)
	if !ok {
		t.Fatalf("missing groups array: %s", body)
	}
	items := make(map[string]map[string]any)
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			t.Fatalf("group is not an object: %v", rawGroup)
		}
		badges, ok := group["badges"].([]any)
		if !ok {
			t.Fatalf("missing badges array: %v", group)
		}
		for _, rawItem := range badges {
			item, ok := rawItem.(map[string]any)
			if !ok {
				t.Fatalf("badge entry is not an object: %v", rawItem)
			}
			slug, _ := item["slug"].(string)
			items[slug] = item
		}
	}
	return items
}

func namesToSet(values []string) map[string]any {
	set := make(map[string]any, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func assertKeys(t *testing.T, label string, got map[string]any, want []string) {
	t.Helper()
	actual := make([]string, 0, len(got))
	for key := range got {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(actual, sorted) {
		t.Fatalf("%s key set: want exactly %v, got %v", label, sorted, actual)
	}
}
