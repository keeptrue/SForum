package extensionscontroller

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/config"
)

// proxyGuardPermission 是 permission 类路由声明需要的权限键，授予 user 1 而不授予 user 2。
const proxyGuardPermission = "extension.guard.manage"

// TestProxyExtensionRouteGuardMatrix 是 legacy 代理前缀的守卫矩阵（P1-2）。
//
// 前缀 /api/v1/extensions/:id/* 在生产装配下始终注册，而 MatchRoute 会命中
// custom / raw_request 守卫路由（它们的 Access 本来就是空的）。控制器曾经的
// `access == "" → login` 兜底把这类路由交给任何已登录用户，插件自己的守卫代码
// 从未运行；raw_request 更会把调用者的 cookie / authorization / x-api-key /
// x-auth-token 原样转发给插件进程。
//
// 现在唯一推导点是 extensionmanifest.DeriveRouteAccess：只有 core guard 路由可代理，
// 其余一律 404（与「未声明路径」同形，不泄露路由存在性），且**绝不到达插件 runtime**。
//
// 变异要求：把控制器里 `if !proxyable { return ErrRouteNotFound }` 换回
// `access == "" → login`，custom / raw 两行断言必须红（已登录变成 200 且 runtime 被调用）。
func TestProxyExtensionRouteGuardMatrix(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "proxy-guard-matrix"})
	plugin := extensions.Extension{
		ID: "guard.plugin", Name: "Guard Matrix Plugin", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "guard.plugin", Name: "Guard Matrix Plugin",
			Version: "1.0.0", Type: extensions.TypePlugin,
			Description: "Guard matrix fixture.", URL: "https://example.com/guard-matrix",
			Author:        extensions.ManifestAuthor{Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com"},
			SForumVersion: ">=0.1.0",
			Permissions:   []string{proxyGuardPermission},
			// custom / raw_request 守卫必须显式声明：路由引用它们，而授权由插件
			// 守卫代码拥有——这正是 legacy 前缀无法复现、必须 404 的那一类。
			Guards: []extensions.ManifestGuard{
				{
					ID: "guard.plugin.custom", ContractVersion: "1.0.0", Kind: extensionmanifest.GuardKindCustom,
					Entry: "backend/guards.js", Digest: strings.Repeat("1", 64),
					Permissions: []string{proxyGuardPermission},
				},
				{
					ID: "guard.plugin.raw", ContractVersion: "1.0.0", Kind: extensionmanifest.GuardKindRawRequest,
					Entry: "backend/raw-guard.js", Digest: strings.Repeat("2", 64),
					Permissions: []string{proxyGuardPermission},
				},
			},
			Routes: []extensions.ManifestRoute{
				{
					ID: "guard.route.public", Path: "/public", Methods: []string{http.MethodGet},
					Guard: extensionmanifest.GuardCorePublic, Access: extensions.RouteAccessPublic,
				},
				{
					ID: "guard.route.login", Path: "/login", Methods: []string{http.MethodGet},
					Guard: extensionmanifest.GuardCoreLogin, Access: extensions.RouteAccessLogin,
				},
				{
					ID: "guard.route.permission", Path: "/manage", Methods: []string{http.MethodPost},
					Guard: extensionmanifest.GuardCorePermission, Access: extensions.RouteAccessPermission,
					Permission: proxyGuardPermission,
				},
				{
					ID: "guard.route.custom", Path: "/custom", Methods: []string{http.MethodPost},
					Guard: "guard.plugin.custom",
				},
				{
					ID: "guard.route.raw", Path: "/raw", Methods: []string{http.MethodPost},
					Guard: "guard.plugin.raw",
				},
			},
		},
	}
	store := &controllerFakeStore{items: map[string]extensions.Extension{plugin.ID: plugin}}
	gateway := &controllerRecordingGateway{}
	controller := NewControllerWithGateway(
		extensions.NewService(store, t.TempDir()),
		controllerActors{actors: map[int64]identity.Actor{
			1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{proxyGuardPermission: true}},
			2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		}},
		manager,
		gateway,
	)
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		controller,
		extensionRouteProviderFunc(func(api fiber.Router) {
			api.Post("/test-login/:id", func(c fiber.Ctx) error {
				var id int64 = 1
				if c.Params("id") == "2" {
					id = 2
				}
				_, err := manager.Start(c, id)
				return err
			})
		}),
	}})

	ordinaryCookie := loginExtensionUser(t, app, manager, 2)
	privilegedCookie := loginExtensionUser(t, app, manager, 1)

	probes := []struct {
		name   string
		method string
		path   string
		// 三个主体各自的期望状态码：匿名 / 已登录无权限 / 已登录持权限。
		anonymous, ordinary, privileged int
		// runtimeRuns 是这三个探针允许到达插件 runtime 的总次数。
		runtimeRuns int
	}{
		{
			name: "public", method: http.MethodGet, path: "/api/v1/extensions/guard.plugin/public",
			anonymous: http.StatusOK, ordinary: http.StatusOK, privileged: http.StatusOK, runtimeRuns: 3,
		},
		{
			name: "login", method: http.MethodGet, path: "/api/v1/extensions/guard.plugin/login",
			anonymous: http.StatusUnauthorized, ordinary: http.StatusOK, privileged: http.StatusOK, runtimeRuns: 2,
		},
		{
			name: "permission", method: http.MethodPost, path: "/api/v1/extensions/guard.plugin/manage",
			anonymous: http.StatusUnauthorized, ordinary: http.StatusForbidden, privileged: http.StatusOK, runtimeRuns: 1,
		},
		{
			// custom 守卫的裁决在插件守卫代码里：legacy 前缀无法复现，只能 404。
			name: "custom guard", method: http.MethodPost, path: "/api/v1/extensions/guard.plugin/custom",
			anonymous: http.StatusNotFound, ordinary: http.StatusNotFound, privileged: http.StatusNotFound, runtimeRuns: 0,
		},
		{
			// raw_request 守卫会转发调用者凭据：漏在这里等于把凭据交给插件进程。
			name: "raw_request guard", method: http.MethodPost, path: "/api/v1/extensions/guard.plugin/raw",
			anonymous: http.StatusNotFound, ordinary: http.StatusNotFound, privileged: http.StatusNotFound, runtimeRuns: 0,
		},
	}

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			cases := []struct {
				subject string
				cookie  *http.Cookie
				want    int
			}{
				{subject: "anonymous", cookie: nil, want: probe.anonymous},
				{subject: "ordinary", cookie: ordinaryCookie, want: probe.ordinary},
				{subject: "privileged", cookie: privilegedCookie, want: probe.privileged},
			}
			runtimeBefore := gateway.calls
			for _, test := range cases {
				resp := performExtensionRequest(t, app, probe.method, probe.path, test.cookie)
				body := responseBody(t, resp)
				if resp.StatusCode != test.want {
					t.Fatalf("%s %s %s: status=%d want=%d body=%s",
						probe.name, test.subject, probe.method, resp.StatusCode, test.want, body)
				}
				if test.want == http.StatusNotFound && !strings.Contains(body, extensions.CodeRouteNotFound) {
					// 404 必须与「未声明路径」同形：不能透出「这条路由其实存在」。
					t.Fatalf("%s %s: 404 形状泄露了路由存在性，body=%s", probe.name, test.subject, body)
				}
			}
			if reached := gateway.calls - runtimeBefore; reached != probe.runtimeRuns {
				t.Fatalf("%s: 插件 runtime 被调用 %d 次，期望 %d 次", probe.name, reached, probe.runtimeRuns)
			}
		})
	}

	// 对照组：未声明的路径与 custom / raw 守卫路由必须给出完全相同的 404 形状。
	undeclared := performExtensionRequest(t, app, http.MethodPost, "/api/v1/extensions/guard.plugin/missing", privilegedCookie)
	undeclaredBody := responseBody(t, undeclared)
	if undeclared.StatusCode != http.StatusNotFound || !strings.Contains(undeclaredBody, extensions.CodeRouteNotFound) {
		t.Fatalf("未声明路径 status=%d body=%s", undeclared.StatusCode, undeclaredBody)
	}
}
