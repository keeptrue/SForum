package mailcontroller

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
)

// 本文件补 RV-54：/admin/mail/* 七条路由的 denied-path（缺 settings.mail.manage
// → 403）+ allowed 对照（有权限 → 非 403），防止「恒 403 也算过」。
//
// 授权在这里由 handler 自己做（Controller.actor），所以 denied 用例完全不需要
// store/providers：门在触库之前就把请求挡下。allowed 用例停在请求体解析（422），
// 只证明门放行了、没有往空依赖里走。
//
// 凭据走 apitokens.WithAuth 注入：宿主 LoadActor 先看 PAT 上下文再看会话，所以
// 不需要为这五条门禁结论搭一套会话存储（会话链路本身由 AuthSession 自己的
// 测试覆盖）。
func TestAdminMailRoutesDeniedWithoutMailPermission(t *testing.T) {
	// 有后台访问权、没有邮件设置权：这正是运营以外的岗位（例如技术管理）的形状。
	app := newMailPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess: true,
	}})

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/mail/providers", ""},
		{http.MethodPut, "/api/v1/admin/mail/provider", `{"extensionId":"acme.mail"}`},
		{http.MethodPost, "/api/v1/admin/mail/provider/reset", ""},
		{http.MethodGet, "/api/v1/admin/mail/deliveries", ""},
		{http.MethodGet, "/api/v1/admin/mail/policy", ""},
		{http.MethodPut, "/api/v1/admin/mail/policy", `{}`},
		{http.MethodPost, "/api/v1/admin/mail/policy/restore", ""},
	}
	for _, route := range routes {
		status, body := mailPermissionCall(t, app, route.method, route.path, route.body)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d（期望 403），body=%s", route.method, route.path, status, body)
		}
	}
}

// TestAdminMailAnonymousIsUnauthorized 把「没有凭据」与「有凭据但没权限」分开：
// 两者都是拒绝，但混成同一个 401/403 会让前端分不清该登录还是该申请权限。
func TestAdminMailAnonymousIsUnauthorized(t *testing.T) {
	app := fiber.New()
	NewController(nil, nil, nil, nil, nil, nil, nil).RegisterRoutes(app.Group("/api/v1"))

	status, body := mailPermissionCall(t, app, http.MethodGet, "/api/v1/admin/mail/providers", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("匿名 GET /admin/mail/providers: status = %d（期望 401），body=%s", status, body)
	}
}

// TestAdminMailRoutesAllowMailManagers 是两个方向的另一半：拿到键就得放行，
// 并且放行之后仍然按正常校验回 422（不是 403、也不是 500）。
//
// 顺带钉住父权限兼容：settings.manage 仍能展开出 settings.mail.manage，
// 否则旧配了父权限的部署会在升级后突然 403。
func TestAdminMailRoutesAllowMailManagers(t *testing.T) {
	cases := []struct {
		name         string
		permissions  map[string]bool
		method       string
		path         string
		body         string
		wantStatus   int
		statusReason string
	}{
		{
			name:         "持有 settings.mail.manage",
			permissions:  map[string]bool{identity.PermissionSettingsMailManage: true},
			method:       http.MethodPut,
			path:         "/api/v1/admin/mail/policy",
			body:         `{"`,
			wantStatus:   http.StatusUnprocessableEntity,
			statusReason: "notification.policy_invalid",
		},
		{
			name:         "只持有父权限 settings.manage",
			permissions:  map[string]bool{identity.PermissionSettingsManage: true},
			method:       http.MethodPut,
			path:         "/api/v1/admin/mail/policy",
			body:         `{"`,
			wantStatus:   http.StatusUnprocessableEntity,
			statusReason: "notification.policy_invalid",
		},
		{
			name:         "持有 settings.mail.manage 选 provider",
			permissions:  map[string]bool{identity.PermissionSettingsMailManage: true},
			method:       http.MethodPut,
			path:         "/api/v1/admin/mail/provider",
			body:         `{"`,
			wantStatus:   http.StatusUnprocessableEntity,
			statusReason: "mail.provider_invalid",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app := newMailPermissionApp(identity.Actor{
				ID: 1, Status: identity.UserStatusActive, Permissions: testCase.permissions,
			})
			status, body := mailPermissionCall(t, app, testCase.method, testCase.path, testCase.body)
			if status != testCase.wantStatus {
				t.Fatalf("%s %s: status = %d（期望 %d），body=%s",
					testCase.method, testCase.path, status, testCase.wantStatus, body)
			}
			if !strings.Contains(body, testCase.statusReason) {
				t.Fatalf("%s %s: 响应里没有 %q：%s", testCase.method, testCase.path, testCase.statusReason, body)
			}
		})
	}
}

func newMailPermissionApp(actor identity.Actor) *fiber.App {
	app := fiber.New()
	// 与生产同构：handler panic 变成 500，而不是把测试进程带走。
	app.Use(recover.New())
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		return c.Next()
	})
	NewController(nil, nil, nil, mailPermissionActors{actor: actor}, nil, nil, nil).
		RegisterRoutes(app.Group("/api/v1"))
	return app
}

func mailPermissionCall(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
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

type mailPermissionActors struct{ actor identity.Actor }

func (store mailPermissionActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return store.actor, nil
}
