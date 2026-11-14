package badgescontroller

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 资料页徽章墙的「规则进度」端到端形状。
//
// 这里用**真实** `badges.Service` + 真实 `Badge.Public()` / `measureForRule`，
// 只把存储与测量替换成内存假实现。这样「规则派生被删掉」会直接变红：
// 没有 `Measure` 就没有测量请求，DTO 也就不会输出 current/threshold。

// progressStubStore 只实现徽章墙/目录需要的读，其余方法由嵌入的 nil Store 兜住。
type progressStubStore struct {
	badges.Store
	items   []badges.UserBadge
	groups  []badges.BadgeGroup
	earned  map[string]struct{}
	catalog []badges.PublicCatalogEntry
	// userBadgeReads 钉住「匿名目录不查库」。
	userBadgeReads int
}

func (s *progressStubStore) ListUserBadges(context.Context, int64) ([]badges.UserBadge, error) {
	s.userBadgeReads++
	return s.items, nil
}

func (s *progressStubStore) ListGroups(context.Context) ([]badges.BadgeGroup, error) {
	return s.groups, nil
}

func (s *progressStubStore) ListPublicCatalog(context.Context, int) ([]badges.PublicCatalogEntry, bool, error) {
	return s.catalog, false, nil
}

func (s *progressStubStore) ListEarnedBadgeSlugs(context.Context, int64) (map[string]struct{}, error) {
	if s.earned == nil {
		return map[string]struct{}{}, nil
	}
	return s.earned, nil
}

// echoMeasurer 按请求里的阈值回填一个确定的当前值。
//
// 它只能拿到 Service 从 `PublicBadge.Measure` 传下来的东西——所以「规则没派生
// 出 Measure」时它根本不会被调用，这正是本文件要钉住的因果。
type echoMeasurer struct {
	calls  int
	userID int64
	last   map[string]badges.ProgressMeasure
}

func (m *echoMeasurer) MeasureProgress(_ context.Context, userID int64, measures map[string]badges.ProgressMeasure) map[string]badges.BadgeProgress {
	m.calls++
	m.userID = userID
	m.last = measures
	out := make(map[string]badges.BadgeProgress, len(measures))
	for slug, measure := range measures {
		out[slug] = badges.BadgeProgress{Current: 2, Threshold: measure.Threshold}
	}
	return out
}

type progressUserLookup struct{ id int64 }

func (l progressUserLookup) PublicProfileUserID(context.Context, string) (int64, error) {
	return l.id, nil
}

// activityCountRule 生成与内置徽章同款的单叶子规则。
func activityCountRule(metric string, op ruleengine.CompareOp, value int64) *ruleengine.Node {
	return &ruleengine.Node{
		Type: conditions.KeyActivityCount,
		Params: map[string]any{
			"metric": metric,
			"window": map[string]any{"kind": ruleengine.WindowKindAll},
		},
		Cmp: &ruleengine.Compare{Op: op, Value: value},
	}
}

// ruleOnlyBadge 是一枚「只有 rule，legacy 两列为 NULL」的自动徽章。
func ruleOnlyBadge(slug string, rule *ruleengine.Node) badges.Badge {
	return badges.Badge{
		ID: 1, Slug: slug, Name: "十帖", Tier: badges.TierBronze,
		Icon: "i-lucide-award", GrantMode: badges.GrantAutomatic, Enabled: true,
		Metric: nil, Threshold: nil, Rule: rule,
	}
}

func newProfileApp(t *testing.T, service *badges.Service) *fiber.App {
	t.Helper()
	controller := (&Controller{service: service, catalog: service, gate: fakeGate{enabled: true}}).
		WithUserLookup(progressUserLookup{id: 7})
	return newCatalogApp(t, controller)
}

func getProfileBadges(t *testing.T, app *fiber.App) map[string]any {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/api/v1/profiles/alice/badges", nil), fiber.TestConfig{Timeout: 5000})
	if err != nil {
		t.Fatalf("GET /api/v1/profiles/alice/badges: %v", err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if response.StatusCode != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%v)", response.StatusCode, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

// profileOnlyItem 取出唯一一枚徽章的 wire 条目（夹具里只有一个未分组分区）。
func profileOnlyItem(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	groups, ok := data["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("want 1 group, got %v", data["groups"])
	}
	group, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("group is not an object: %v", groups[0])
	}
	items, ok := group["badges"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("want 1 badge item, got %v", group["badges"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("badge item is not an object: %v", items[0])
	}
	return item
}

// 规则徽章（metric/threshold 为 NULL）在资料页必须有**值级**进度。
func TestProfileBadgesEmitRuleDerivedProgress(t *testing.T) {
	badge := ruleOnlyBadge("ten-topics", activityCountRule("topic_count", ruleengine.CompareGTE, 10))
	store := &progressStubStore{items: []badges.UserBadge{{Badge: badge.Public(conditions.NewRefNames())}}}
	measurer := &echoMeasurer{}
	service := badges.NewService(store, nil)
	service.SetProgressMeasurer(measurer)

	item := profileOnlyItem(t, getProfileBadges(t, newProfileApp(t, service)))

	if measurer.calls != 1 || measurer.userID != 7 {
		t.Fatalf("want 1 measurement for user 7, got calls=%d userID=%d", measurer.calls, measurer.userID)
	}
	if _, ok := measurer.last["ten-topics"]; !ok {
		t.Fatalf("rule-only badge must be measured, got %v", measurer.last)
	}
	if current, ok := item["current"].(float64); !ok || current != 2 {
		t.Fatalf("current 必须是数字 2（不能只是键存在），得到 %#v", item["current"])
	}
	if threshold, ok := item["threshold"].(float64); !ok || threshold != 10 {
		t.Fatalf("threshold 必须是数字 10，得到 %#v", item["threshold"])
	}
	if earned, _ := item["earned"].(bool); earned {
		t.Fatalf("fixture badge must be unearned: %v", item)
	}
}

// 不可测的规则形状（lte / 布尔 / 多条件组）一律没有进度，也**不触发**测量。
func TestProfileBadgesOmitProgressForUnmeasurableRules(t *testing.T) {
	cases := []struct {
		name string
		rule *ruleengine.Node
	}{
		{"lte", activityCountRule("topic_count", ruleengine.CompareLTE, 10)},
		{"boolean", &ruleengine.Node{Type: conditions.KeyIdentityEmailVerify}},
		{"all-group", &ruleengine.Node{
			Op: ruleengine.OpAll,
			Children: []ruleengine.Node{
				*activityCountRule("topic_count", ruleengine.CompareGTE, 1),
				*activityCountRule("comment_count", ruleengine.CompareGTE, 1),
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badge := ruleOnlyBadge("unmeasurable", tc.rule)
			store := &progressStubStore{items: []badges.UserBadge{{Badge: badge.Public(conditions.NewRefNames())}}}
			measurer := &echoMeasurer{}
			service := badges.NewService(store, nil)
			service.SetProgressMeasurer(measurer)

			item := profileOnlyItem(t, getProfileBadges(t, newProfileApp(t, service)))

			if measurer.calls != 0 {
				t.Fatalf("%s must not be measured, got %d calls (%v)", tc.name, measurer.calls, measurer.last)
			}
			for _, key := range []string{"current", "threshold"} {
				if _, present := item[key]; present {
					t.Fatalf("%s must not carry %q: %v", tc.name, key, item)
				}
			}
		})
	}
}

// 目录页的同一套因果：登录 → 有进度；匿名 → 两个键都没有且不查库。
func TestPublicCatalogRealServiceMergesProgressOnlyForViewer(t *testing.T) {
	badge := ruleOnlyBadge("ten-topics", activityCountRule("topic_count", ruleengine.CompareGTE, 10))
	public := badge.Public(conditions.NewRefNames())
	store := &progressStubStore{
		items: []badges.UserBadge{{Badge: public}},
		catalog: []badges.PublicCatalogEntry{{
			Slug: public.Slug, Name: public.Name, Tier: public.Tier,
			Icon: public.Icon, RequirementText: public.RequirementText,
		}},
	}
	measurer := &echoMeasurer{}
	service := badges.NewService(store, nil)
	service.SetProgressMeasurer(measurer)

	loggedIn := (&Controller{service: service, catalog: service, gate: fakeGate{enabled: true}}).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
		})
	status, _, body := getCatalog(t, newCatalogApp(t, loggedIn))
	if status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	item := catalogItemsBySlug(t, body)["ten-topics"]
	if current, ok := item["current"].(float64); !ok || current != 2 {
		t.Fatalf("logged-in catalog current 必须是数字 2，得到 %#v", item["current"])
	}
	if threshold, ok := item["threshold"].(float64); !ok || threshold != 10 {
		t.Fatalf("logged-in catalog threshold 必须是数字 10，得到 %#v", item["threshold"])
	}

	store.userBadgeReads = 0
	anonymous := &Controller{service: service, catalog: service, gate: fakeGate{enabled: true}}
	status, _, body = getCatalog(t, newCatalogApp(t, anonymous))
	if status != fiber.StatusOK {
		t.Fatalf("anonymous want 200, got %d (%s)", status, body)
	}
	if store.userBadgeReads != 0 {
		t.Fatalf("anonymous catalog must not read user badges, got %d reads", store.userBadgeReads)
	}
	item = catalogItemsBySlug(t, body)["ten-topics"]
	for _, key := range []string{"current", "threshold"} {
		if _, present := item[key]; present {
			t.Fatalf("anonymous catalog must not carry %q: %v", key, item)
		}
	}
}
