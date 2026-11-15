package jobscontroller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	jobs "github.com/keeptrue/sforum/apps/api/app/Models/Jobs"
)

// 本文件补 RV-54：/admin/jobs/* 十一条路由的 denied-path + allowed 对照。
//
// 门在 Service 里（jobs.Service 每个方法开头就查 jobs.view / jobs.manage），
// 所以 denied 用例不需要数据库：权限检查发生在 pool/client 之前。
//
// allowed 对照刻意不用需要数据库的方法（overview/list/detail 会打到 nil pool）：
//   - jobs.view → GET /admin/jobs/schedules 走的是「目录 + 启用态」纯逻辑路径
//     （lastRunsByKind 在 pool 为 nil 时降级、scheduleEnabled 在 options 为 nil 时
//     回落到目录默认值），能真回 200；
//   - jobs.manage → 一次合法格式但不存在的 schedule 触发 404（门已过、资源不存在），
//     与 403 可区分。
func TestAdminJobsRoutesDeniedWithoutJobsPermissions(t *testing.T) {
	app := newJobsPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess: true,
	}})

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/jobs/overview"},
		{http.MethodGet, "/api/v1/admin/jobs/schedules"},
		{http.MethodPost, "/api/v1/admin/jobs/schedules/identity.cleanup_sessions/enable"},
		{http.MethodPost, "/api/v1/admin/jobs/schedules/identity.cleanup_sessions/disable"},
		{http.MethodPost, "/api/v1/admin/jobs/schedules/identity.cleanup_sessions/trigger"},
		{http.MethodGet, "/api/v1/admin/jobs"},
		{http.MethodGet, "/api/v1/admin/jobs/42"},
		{http.MethodPost, "/api/v1/admin/jobs/42/retry"},
		{http.MethodPost, "/api/v1/admin/jobs/42/cancel"},
		{http.MethodPost, "/api/v1/admin/jobs/queues/default/pause"},
		{http.MethodPost, "/api/v1/admin/jobs/queues/default/resume"},
	}
	for _, route := range routes {
		status, body := jobsPermissionCall(t, app, route.method, route.path)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d（期望 403），body=%s", route.method, route.path, status, body)
		}
	}
}

// TestAdminJobsViewDoesNotImplyManage 钉住两键的边界：只读运维能看，不能动。
// 只测「没权限 → 403」会让「jobs.view 顺带给了写的权利」这种回归漏过去。
func TestAdminJobsViewDoesNotImplyManage(t *testing.T) {
	viewer := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionJobsView: true,
	}}
	app := newJobsPermissionApp(viewer)

	status, body := jobsPermissionCall(t, app, http.MethodGet, "/api/v1/admin/jobs/schedules")
	if status != http.StatusOK {
		t.Fatalf("GET /admin/jobs/schedules（jobs.view）: status = %d（期望 200），body=%s", status, body)
	}
	// 目录是纯逻辑生成的核心 schedule 列表（不读库），所以这里能真断言内容。
	if !strings.Contains(body, `"identity.cleanup_sessions"`) {
		t.Fatalf("GET /admin/jobs/schedules 响应缺少核心 schedule 目录：%s", body)
	}

	for _, path := range []string{
		"/api/v1/admin/jobs/schedules/identity.cleanup_sessions/enable",
		"/api/v1/admin/jobs/schedules/identity.cleanup_sessions/disable",
		"/api/v1/admin/jobs/42/retry",
		"/api/v1/admin/jobs/42/cancel",
		"/api/v1/admin/jobs/queues/default/pause",
	} {
		status, body := jobsPermissionCall(t, app, http.MethodPost, path)
		if status != http.StatusForbidden {
			t.Fatalf("POST %s（只有 jobs.view）: status = %d（期望 403），body=%s", path, status, body)
		}
	}
}

// TestAdminJobsManagePassesGateThenReportsMissingResource 是 allowed 方向的另一半：
// 拿到 jobs.manage 之后得到的是 404（资源不存在），不是 403、也不是 500。
func TestAdminJobsManagePassesGateThenReportsMissingResource(t *testing.T) {
	manager := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionJobsManage: true,
	}}
	app := newJobsPermissionApp(manager)

	status, body := jobsPermissionCall(t, app, http.MethodPost,
		"/api/v1/admin/jobs/schedules/not-a-real-schedule/enable")
	if status != http.StatusNotFound {
		t.Fatalf("jobs.manage 启用不存在的 schedule: status = %d（期望 404），body=%s", status, body)
	}
	if !strings.Contains(body, "jobs.schedule_not_found") {
		t.Fatalf("期望 jobs.schedule_not_found，body=%s", body)
	}
}

func newJobsPermissionApp(actor identity.Actor) *fiber.App {
	app := fiber.New()
	// 与生产同构：handler panic 变成 500，而不是把测试进程带走。
	app.Use(recover.New())
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		return c.Next()
	})
	// nil pool/client：denied 路径在触库之前就返回，allowed 路径只用纯逻辑方法。
	NewController(jobs.NewService(nil, nil), jobsPermissionActors{actor: actor}, nil).
		RegisterRoutes(app.Group("/api/v1"))
	return app
}

func jobsPermissionCall(t *testing.T, app *fiber.App, method, path string) (int, string) {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: 请求失败: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("%s %s: 读响应失败: %v", method, path, err)
	}
	return response.StatusCode, strings.TrimSpace(string(raw))
}

type jobsPermissionActors struct{ actor identity.Actor }

func (store jobsPermissionActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return store.actor, nil
}
