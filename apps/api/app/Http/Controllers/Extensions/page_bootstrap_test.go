package extensionscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/zhuchunshu/sforum/apps/api/app/Http"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	authsession "github.com/zhuchunshu/sforum/apps/api/app/Support/AuthSession"
	"github.com/zhuchunshu/sforum/apps/api/config"
	platformversion "github.com/zhuchunshu/sforum/apps/api/version"
)

// pageBootstrapFrontendStub 只实现 TrustedFrontendService 的三个方法，
// 便于断言"只在会挂载扩展 UI 的视图下查询前端信任状态"。
type pageBootstrapFrontendStub struct {
	status   extensions.FrontendStatus
	err      error
	requests []string
}

func (s *pageBootstrapFrontendStub) Frontend(_ context.Context, _ identity.Actor, extensionID string) (extensions.FrontendStatus, error) {
	s.requests = append(s.requests, extensionID)
	return s.status, s.err
}

func (s *pageBootstrapFrontendStub) Grant(context.Context, identity.Actor, string, extensions.GrantFrontendInput) (extensions.FrontendStatus, error) {
	return extensions.FrontendStatus{}, nil
}

func (s *pageBootstrapFrontendStub) Revoke(context.Context, identity.Actor, string) (extensions.FrontendStatus, error) {
	return extensions.FrontendStatus{}, nil
}

type pageBootstrapEnvelope struct {
	Data struct {
		extensions.AdminPageBootstrap
		Frontend *extensions.FrontendStatus   `json:"frontend"`
		Host     platformversion.HostIdentity `json:"host"`
	} `json:"data"`
}

func newPageBootstrapApp(t *testing.T, frontend TrustedFrontendService) (*fiber.App, *authsession.Manager, string) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {
			ID:     1,
			Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionManage: true,
			},
		},
	}}
	digest := strings.Repeat("c", 64)
	plugin := extensions.Extension{
		ID: "bootstrap.plugin", Name: "Bootstrap Plugin", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ID: "bootstrap.plugin", Name: "Bootstrap Plugin", Version: "1.0.0", Type: extensions.TypePlugin,
			SForumVersion: ">=0.1.0",
			Admin: extensions.ManifestAdmin{
				Pages: []extensions.ManifestAdminPage{
					{
						Path: "/clients", Label: "Clients", View: "component", Order: 10, Menu: true,
						Component: &extensions.AdminComponent{ID: "clients", APIVersion: 1, Entry: "frontend/admin/dist/clients.mjs"},
					},
					{Path: "/ops/config", Label: "Ops Config", View: "settings", Order: 20},
					{Path: "/about", Label: "About", View: "about", Order: 30},
				},
			},
		},
	}
	plugin.PackagePath = controllerInstalledPackage(t, plugin.Manifest)
	store := &controllerFakeStore{items: map[string]extensions.Extension{plugin.ID: plugin}}
	controller := NewController(extensions.NewService(store, t.TempDir()), users, manager)
	if frontend != nil {
		controller = controller.WithTrustedRuntime(frontend)
	}
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			_, err := manager.Start(c, 1)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, loginProvider}})
	return app, manager, digest
}

func fetchPageBootstrap(t *testing.T, app *fiber.App, cookie *http.Cookie, pagePath string) (pageBootstrapEnvelope, string) {
	t.Helper()
	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path="+pagePath, cookie)
	raw := responseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page-bootstrap %s status=%d body=%s", pagePath, resp.StatusCode, raw)
	}
	var envelope pageBootstrapEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode page-bootstrap %s: %v body=%s", pagePath, err, raw)
	}
	return envelope, raw
}

// component / settings 视图必须随 bootstrap 返回信任结论：管理端是 SSR + 禁缓存，
// 缺这个字段时前端只能把"尚未读到状态"先画成"未授权"，即扩展页首屏闪屏。
func TestControllerPageBootstrapCarriesFrontendStatusForUIRenderingViews(t *testing.T) {
	frontend := &pageBootstrapFrontendStub{status: extensions.FrontendStatus{
		ExtensionID: "bootstrap.plugin",
		Kind:        extensions.AdminFrontendKindPrebuiltComponent,
		TrustState:  extensions.FrontendTrustTrusted,
		Digest:      strings.Repeat("d", 64),
		Component:   &extensions.AdminComponent{ID: "clients", APIVersion: 1, Entry: "frontend/admin/dist/clients.mjs"},
	}}
	app, manager, _ := newPageBootstrapApp(t, frontend)
	cookie := loginExtensionUser(t, app, manager, 1)

	for _, pagePath := range []string{"/clients", "/ops/config"} {
		envelope, _ := fetchPageBootstrap(t, app, cookie, pagePath)
		if envelope.Data.Frontend == nil {
			t.Fatalf("%s must carry frontend status", pagePath)
		}
		if envelope.Data.Frontend.TrustState != extensions.FrontendTrustTrusted ||
			envelope.Data.Frontend.Digest != strings.Repeat("d", 64) {
			t.Fatalf("%s frontend payload=%#v", pagePath, envelope.Data.Frontend)
		}
		// 受信管理页组件必须首帧就能读到宿主版本，而不是再发一次请求。
		if envelope.Data.Host.CoreVersion != platformversion.Identity().CoreVersion || envelope.Data.Host.CoreVersion == "" {
			t.Fatalf("%s host payload=%#v", pagePath, envelope.Data.Host)
		}
	}
	if len(frontend.requests) != 2 {
		t.Fatalf("expected one frontend lookup per UI view, got %v", frontend.requests)
	}
}

// about / 未声明路径不渲染扩展 UI，不应付出信任查询开销，且必须显式编码 null。
func TestControllerPageBootstrapSkipsFrontendStatusForNonUIPages(t *testing.T) {
	frontend := &pageBootstrapFrontendStub{status: extensions.FrontendStatus{
		ExtensionID: "bootstrap.plugin", Kind: extensions.AdminFrontendKindNone, TrustState: extensions.FrontendTrustNone,
	}}
	app, manager, _ := newPageBootstrapApp(t, frontend)
	cookie := loginExtensionUser(t, app, manager, 1)

	for _, pagePath := range []string{"/about", "/missing"} {
		envelope, raw := fetchPageBootstrap(t, app, cookie, pagePath)
		if envelope.Data.Frontend != nil {
			t.Fatalf("%s must not carry frontend status: %#v", pagePath, envelope.Data.Frontend)
		}
		if !strings.Contains(raw, `"frontend":null`) {
			t.Fatalf("%s must encode frontend as explicit null: %s", pagePath, raw)
		}
	}
	if len(frontend.requests) != 0 {
		t.Fatalf("non-UI pages must not query frontend status, got %v", frontend.requests)
	}
}

// 信任状态不可读只降级该字段；bootstrap 本身必须继续成功，前端再回落到自己的 /frontend 请求。
func TestControllerPageBootstrapDegradesWhenFrontendStatusFails(t *testing.T) {
	frontend := &pageBootstrapFrontendStub{err: errors.New("trust store unavailable")}
	app, manager, _ := newPageBootstrapApp(t, frontend)
	cookie := loginExtensionUser(t, app, manager, 1)

	envelope, raw := fetchPageBootstrap(t, app, cookie, "/clients")
	if envelope.Data.Frontend != nil {
		t.Fatalf("failed frontend lookup must degrade to null: %#v", envelope.Data.Frontend)
	}
	if !strings.Contains(raw, `"frontend":null`) {
		t.Fatalf("degraded response must encode frontend as explicit null: %s", raw)
	}
	if envelope.Data.Page == nil || envelope.Data.Page.View != "component" {
		t.Fatalf("page resolution must survive frontend failure: %#v", envelope.Data.Page)
	}
}

// 未装配可信前端运行时（h.frontend == nil）时必须 fail open 为 null，而不是 503。
func TestControllerPageBootstrapWithoutTrustedRuntimeReturnsNullFrontend(t *testing.T) {
	app, manager, _ := newPageBootstrapApp(t, nil)
	cookie := loginExtensionUser(t, app, manager, 1)

	envelope, raw := fetchPageBootstrap(t, app, cookie, "/clients")
	if envelope.Data.Frontend != nil {
		t.Fatalf("missing trusted runtime must degrade to null: %#v", envelope.Data.Frontend)
	}
	if !strings.Contains(raw, `"frontend":null`) {
		t.Fatalf("missing runtime must encode frontend as explicit null: %s", raw)
	}
}
