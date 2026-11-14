package extensionscontroller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	"github.com/keeptrue/sforum/apps/api/config"
)

type queryCatalogEnvelope struct {
	Data queryCatalogResponse `json:"data"`
}

func newQueryCatalogTestApp(t *testing.T, configured bool) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	if configured {
		catalog, err := hostapi.NewQueryRegistryCoreCatalog()
		if err != nil {
			t.Fatal(err)
		}
		controller.WithQueryCatalog(catalog)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/route-inspector-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
	return app, manager
}

// sealedQueryCatalogIDs 是密封目录的**字面期望集合**（L-06）。
//
// 为什么写死而不是用 `12+len(ShortcodeProjectionQueryIDs())`：纯计数断言在徽章那三条
// 加进来时只会说「19 != 22」，不会告诉任何人多了哪三条，于是它红了整整一天没人管。
// 显式集合让失败信息直接说出是哪条多/少了。
//
// 构成：15 条 stable（含 1 条 consent-gated 的 core.query.identity.self_email）
// + 7 条 shortcode 投影 = 23。四条徽章查询（public_badges.list / public_user_badges.list /
// public_user_activity.list / public_user_badge_facts.list）来自基线提交 f63ad2cf9
// 与规则引擎那一刀，属于密封目录的一部分。
// **加查询必须同批改这里**，否则本测试红。
//
// 同一份清单在 `bootstrap/sealed_query_catalog_test.go` 里还有一份（`sealedCoreQueryCatalogIDs`）：
// bootstrap 断言装配产物、本文件断言 HTTP 响应，两个包各自必须能独立红。
// 改一处必须同批改另一处。
var sealedQueryCatalogIDs = []string{
	// stable（16）
	"core.query.identity.self_email",
	"core.query.public_attachment.by_public_id",
	"core.query.public_badges.list",
	"core.query.public_bookmarks.list",
	"core.query.public_categories.list",
	"core.query.public_comments.list",
	"core.query.public_reactions.list",
	"core.query.public_topic.by_id",
	"core.query.public_topics.list",
	"core.query.public_user_activity.list",
	"core.query.public_user_badge_facts.list",
	"core.query.public_user_badges.list",
	"core.query.public_trust_level_catalog.list",
	"core.query.public_user_trust_level.get",
	"core.query.safe_user.by_id",
	"core.query.safe_user.by_username",
	"core.query.safe_users.batch",
	"core.query.site_stats.current",
	// shortcode 投影（7）
	"core.query.shortcode.author_decisions.batch",
	"core.query.shortcode.friend_links.list",
	"core.query.shortcode.public_categories.batch",
	"core.query.shortcode.public_comments.batch",
	"core.query.shortcode.public_topics.batch",
	"core.query.shortcode.public_users.batch",
	"core.query.shortcode.reply_eligibility.batch",
}

// sealedQueryCatalogBadgeIDs 单独点名：徽章那三条被吞掉过一次，专门留一条读得懂的断言。
var sealedQueryCatalogBadgeIDs = []string{
	"core.query.public_badges.list",
	"core.query.public_user_badges.list",
	"core.query.public_user_activity.list",
}

func TestQueryCatalogHTTPReturnsSealedCatalogForViewers(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, true)
	for name, userID := range map[string]int64{"viewer": 1, "legacy_manager": 2} {
		t.Run(name, func(t *testing.T) {
			cookie := loginRouteInspectorUser(t, app, userID)
			response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/query-catalog", cookie)
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			var envelope queryCatalogEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			data := envelope.Data

			gotIDs := make(map[string]struct{}, len(data.Queries))
			gotSorted := make([]string, 0, len(data.Queries))
			for _, entry := range data.Queries {
				gotIDs[entry.QueryID] = struct{}{}
				gotSorted = append(gotSorted, entry.QueryID)
			}

			// 1. **先点名徽章**：这条测试存在的理由就是「徽章查询被吞过一次」。
			//    顺序不能挪到总数断言之后——删掉一条徽章查询时总数会先红（22→21），
			//    点名那句永远轮不到执行，等于对唯一要防的回归不可达。
			for _, badgeQueryID := range sealedQueryCatalogBadgeIDs {
				if _, ok := gotIDs[badgeQueryID]; !ok {
					t.Fatalf("徽章查询 %q 不在密封目录里（基线 f63ad2cf9 落地的稳定定义）", badgeQueryID)
				}
			}

			// 2. 总数 == 字面期望集合长度，且与自身条目数自洽（三个数字必须同时相等）。
			if data.ExtensionID != hostapi.QueryRegistryCoreExtensionID ||
				data.Total != len(sealedQueryCatalogIDs) ||
				data.Total != len(data.Queries) {
				t.Fatalf("catalog total = %d（len(Queries) = %d），期望 %d：%#v",
					data.Total, len(data.Queries), len(sealedQueryCatalogIDs), data)
			}

			// 3. 集合双向相等：多一条或少一条都红，且列出完整差异。
			wantSorted := slices.Clone(sealedQueryCatalogIDs)
			slices.Sort(wantSorted)
			slices.Sort(gotSorted)
			if !slices.Equal(gotSorted, wantSorted) {
				t.Fatalf("密封目录 ID 集合与期望不一致\n实际：%v\n期望：%v", gotSorted, wantSorted)
			}

			if data.CostMaximum != 500 || data.ArtifactDigest == "" {
				t.Fatalf("catalog budget/digest = %#v", data)
			}
			var stats *queryCatalogEntryView
			for index := range data.Queries {
				if data.Queries[index].HostQueryID == hostapi.QuerySiteStats {
					stats = &data.Queries[index]
				}
				if data.Queries[index].ResultSchemaDigest == "" || len(data.Queries[index].Fields) == 0 {
					t.Fatalf("entry incomplete: %#v", data.Queries[index])
				}
			}
			if stats == nil || stats.Pagination != "none" || len(stats.Fields) != 4 ||
				stats.QueryID != "core.query.site_stats.current" || stats.HostPlanVersion != hostapi.QueryStableCorePlanVersion {
				t.Fatalf("site stats entry = %#v", stats)
			}
		})
	}
}

func TestQueryCatalogHTTPRequiresAuthenticationAndExtensionView(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, true)
	endpoint := "/api/v1/admin/extensions/query-catalog"
	unauthenticated := performExtensionRequest(t, app, http.MethodGet, endpoint, nil)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	denied := performExtensionRequest(t, app, http.MethodGet, endpoint, loginRouteInspectorUser(t, app, 3))
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("no-view status=%d body=%s", denied.StatusCode, responseBody(t, denied))
	}
	denied.Body.Close()
}

func TestQueryCatalogHTTPFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, false)
	viewer := loginRouteInspectorUser(t, app, 1)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/query-catalog", viewer)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var payload struct {
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Reason != queryCatalogUnavailableReason {
		t.Fatalf("reason = %q", payload.Data.Reason)
	}
}
