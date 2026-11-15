package webhookscontroller

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
	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
)

// 本文件补 RV-54：/admin/webhooks/* 六条路由的 denied-path + allowed 对照。
//
// 六条都属于同一个设置面（settings.manage 或兼容用的 settings.site.manage），
// 其中 /events 的门在 handler 里（避免向普通用户暴露内部事件目录），其余五条在
// Webhooks.Service 的 requireManage 里。denied 用例因此不需要 store：权限先于
// 持久化返回。allowed 用例用「合法主体 + 非法载荷」停在 service 的入参校验
// （422）与纯目录（200），都不触库。
func TestAdminWebhookRoutesDeniedWithoutSettingsPermission(t *testing.T) {
	app := newWebhooksPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess: true,
	}})

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/webhooks/endpoints", ""},
		{http.MethodPost, "/api/v1/admin/webhooks/endpoints", `{}`},
		{http.MethodPatch, "/api/v1/admin/webhooks/endpoints/1", `{}`},
		{http.MethodDelete, "/api/v1/admin/webhooks/endpoints/1", ""},
		{http.MethodGet, "/api/v1/admin/webhooks/deliveries", ""},
		{http.MethodGet, "/api/v1/admin/webhooks/events", ""},
	}
	for _, route := range routes {
		status, body := webhooksPermissionCall(t, app, route.method, route.path, route.body)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d（期望 403），body=%s", route.method, route.path, status, body)
		}
	}
}

// TestAdminWebhookRoutesAllowSettingsManagers 是反方向：拿到键之后要么读到目录
// （200，events 不读库），要么在入参校验处停下（422）。两个都不是 403。
//
// 同时覆盖兼容口径：settings.site.manage 与旧父键 settings.manage 都要放行——
// 端点的注释写的是「两者之一」，测试必须跟着这个语义，而不是只认一个键。
func TestAdminWebhookRoutesAllowSettingsManagers(t *testing.T) {
	cases := []struct {
		name        string
		permission  string
		wantStatus  int
		bodyNeedle  string
		method      string
		path        string
		requestBody string
	}{
		{
			name:       "settings.manage 读事件目录",
			permission: identity.PermissionSettingsManage,
			wantStatus: http.StatusOK, bodyNeedle: `"items"`,
			method: http.MethodGet, path: "/api/v1/admin/webhooks/events",
		},
		{
			name:       "settings.site.manage 读事件目录",
			permission: identity.PermissionSettingsSiteManage,
			wantStatus: http.StatusOK, bodyNeedle: `"items"`,
			method: http.MethodGet, path: "/api/v1/admin/webhooks/events",
		},
		{
			name:       "settings.site.manage 建端点停在入参校验",
			permission: identity.PermissionSettingsSiteManage,
			wantStatus: http.StatusUnprocessableEntity, bodyNeedle: "webhook.invalid",
			method: http.MethodPost, path: "/api/v1/admin/webhooks/endpoints", requestBody: `{}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app := newWebhooksPermissionApp(identity.Actor{
				ID: 1, Status: identity.UserStatusActive,
				Permissions: map[string]bool{testCase.permission: true},
			})
			status, body := webhooksPermissionCall(t, app, testCase.method, testCase.path, testCase.requestBody)
			if status != testCase.wantStatus {
				t.Fatalf("%s %s: status = %d（期望 %d），body=%s",
					testCase.method, testCase.path, status, testCase.wantStatus, body)
			}
			if !strings.Contains(body, testCase.bodyNeedle) {
				t.Fatalf("%s %s: 响应里没有 %q：%s", testCase.method, testCase.path, testCase.bodyNeedle, body)
			}
		})
	}
}

func newWebhooksPermissionApp(actor identity.Actor) *fiber.App {
	app := fiber.New()
	// 与生产同构：handler panic 变成 500，而不是把测试进程带走。
	app.Use(recover.New())
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		return c.Next()
	})
	// nil store：denied 在触库前返回，allowed 用例只用到目录与入参校验。
	NewController(webhooks.NewService(nil, nil, nil), webhooksPermissionActors{actor: actor}, nil).
		RegisterRoutes(app.Group("/api/v1"))
	return app
}

func webhooksPermissionCall(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	response, err := app.Test(request)
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

type webhooksPermissionActors struct{ actor identity.Actor }

func (store webhooksPermissionActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return store.actor, nil
}
