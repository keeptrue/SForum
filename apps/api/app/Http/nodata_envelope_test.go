package http_test

import (
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/jackc/pgx/v5"
	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identitycontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Identity"
	webhookscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Webhooks"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// #235 取 b 的行为面证据：8 条「无内容」端点在实现侧统一走 apphttp.NoData，
// 也就是 **200 + 统一 envelope（data: null）**，不是 204。
//
// 契约（contracts/openapi/paths/identity.yaml、webhooks.yaml）已按实现改成 200 + ApiResponseNull；
// 这里用真实请求逐条打一遍，断言 status=200 且响应体是 {"code":200,"message":"OK","data":null}。
// 表驱动而不是只测 logout：只测一条时，其它 7 条改成 204/空响应体不会有任何测试变红。
//
// setupPassword 单独放在 nodata_envelope_postgres_test.go：它的 recent-auth 门控挂在
// 具体的 *identity.PostgresExternalAuthStore 上（不是接口），无库时无法驱动到 200。

// --- fakes ---

// nodataAPITokenStore 让 PAT 撤销走到成功分支，不做任何真实持久化。
type nodataAPITokenStore struct{ revoked []int64 }

func (s *nodataAPITokenStore) Create(context.Context, int64, string, string, string, []string, *time.Time) (apitokens.Record, error) {
	return apitokens.Record{}, nil
}

func (s *nodataAPITokenStore) ListByUser(context.Context, int64, bool) ([]apitokens.Record, error) {
	return nil, nil
}

func (s *nodataAPITokenStore) GetByPublicID(context.Context, string) (apitokens.Record, error) {
	return apitokens.Record{}, apitokens.ErrTokenNotFound
}

func (s *nodataAPITokenStore) GetByIDForUser(context.Context, int64, int64) (apitokens.Record, error) {
	return apitokens.Record{}, apitokens.ErrTokenNotFound
}

func (s *nodataAPITokenStore) Revoke(_ context.Context, _ int64, id int64) error {
	s.revoked = append(s.revoked, id)
	return nil
}

func (s *nodataAPITokenStore) TouchLastUsed(context.Context, int64) error { return nil }

func (s *nodataAPITokenStore) TokenVersion(context.Context, int64) (int64, error) { return 0, nil }

func (s *nodataAPITokenStore) RevokeAllForUser(context.Context, int64, string) (int, error) {
	return 0, nil
}

// nodataWebhookStore 让 webhook endpoint 删除走到成功分支。
type nodataWebhookStore struct{ deleted []int64 }

func (s *nodataWebhookStore) ListEndpoints(context.Context) ([]webhooks.EndpointRecord, error) {
	return nil, nil
}

func (s *nodataWebhookStore) GetEndpoint(_ context.Context, id int64) (webhooks.EndpointRecord, error) {
	return webhooks.EndpointRecord{Endpoint: webhooks.Endpoint{ID: id}}, nil
}

func (s *nodataWebhookStore) CreateEndpoint(_ context.Context, input webhooks.CreateEndpointInput) (webhooks.EndpointRecord, error) {
	return webhooks.EndpointRecord{}, nil
}

func (s *nodataWebhookStore) UpdateEndpoint(_ context.Context, id int64, _ webhooks.UpdateEndpointInput) (webhooks.EndpointRecord, error) {
	return webhooks.EndpointRecord{Endpoint: webhooks.Endpoint{ID: id}}, nil
}

func (s *nodataWebhookStore) DeleteEndpoint(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *nodataWebhookStore) CreateDeliveryTx(context.Context, pgx.Tx, webhooks.CreateDeliveryInput) (webhooks.Delivery, error) {
	return webhooks.Delivery{}, nil
}

func (s *nodataWebhookStore) GetDelivery(context.Context, int64) (webhooks.Delivery, error) {
	return webhooks.Delivery{}, nil
}

func (s *nodataWebhookStore) UpdateDelivery(context.Context, webhooks.DeliveryUpdate) error {
	return nil
}

func (s *nodataWebhookStore) ListDeliveries(context.Context, int64, int) ([]webhooks.Delivery, error) {
	return nil, nil
}

func (s *nodataWebhookStore) ListEnabledEndpointsForEvent(context.Context, string) ([]webhooks.EndpointRecord, error) {
	return nil, nil
}

// nodataLinkStore 提供一条属于当前 actor 的 active 外部身份绑定，让 unlink 走到成功分支。
type nodataLinkStore struct{ link identity.ExternalIdentityLink }

func (s *nodataLinkStore) Link(context.Context, identity.LinkExternalIdentityInput, identity.ExternalIdentityLinkCommitFence) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, identity.ErrExternalIdentityLinkInvalid
}

func (s *nodataLinkStore) Unlink(_ context.Context, input identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{Link: identity.ExternalIdentityLink{
		ID:       input.LinkID,
		UserID:   input.ActorUserID,
		Status:   identity.ExternalIdentityLinkStatusUnlinked,
		Revision: input.ExpectedRevision + 1,
	}}, nil
}

func (s *nodataLinkStore) Erase(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, identity.ErrExternalIdentityLinkInvalid
}

func (s *nodataLinkStore) Get(_ context.Context, id int64) (identity.ExternalIdentityLink, error) {
	if id != s.link.ID {
		return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
	}
	return s.link, nil
}

func (s *nodataLinkStore) FindActive(context.Context, string, string) (identity.ExternalIdentityLink, error) {
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}

func (s *nodataLinkStore) ListUser(_ context.Context, userID int64) ([]identity.ExternalIdentityLink, error) {
	return []identity.ExternalIdentityLink{s.link}, nil
}

// nodataRecentAuth 把 session-bound recent-auth 固定为「刚认证过」。
type nodataRecentAuth struct{}

func (nodataRecentAuth) IsSessionRecentlyAuthenticated(context.Context, int64, string) (bool, error) {
	return true, nil
}

// --- 表驱动 ---

// nodataHarness 是一套「已登录 super_admin + 三个 fake store」的最小装配。
//
// 每个用例都要独立装一次：logout 会把会话销毁，复用同一个 app 会让后续用例全部 401
// （第一版就是这么红的，别改回共享装配）。
type nodataHarness struct {
	app          *fiber.App
	cookie       *nethttp.Cookie
	tokenStore   *nodataAPITokenStore
	webhookStore *nodataWebhookStore
	linkStore    *nodataLinkStore
}

func newNoDataHarness(t *testing.T) *nodataHarness {
	t.Helper()
	store := newHTTPFakeStore()
	// 自定义角色：deleteRole / replaceRolePermissions 不能拿内置角色做靶子（会被 409 拦下）。
	store.roles["auditor"] = identity.Role{ID: 200, Key: "auditor", Alias: "审计员", IsEnabled: true}

	tokenStore := &nodataAPITokenStore{}
	webhookStore := &nodataWebhookStore{}
	linkStore := &nodataLinkStore{}

	sessions := session.NewStore()
	// webhooks 控制器直接吃 *authsession.Manager（不是 fiber store），与 Identity 共用同一个
	// store 才能读到同一个浏览器会话。
	authSessions := authsession.NewManager(sessions, authsession.Config{})
	identityService := identity.NewService(store)
	identityController := identitycontroller.NewController(identityService, sessions).
		WithAPITokens(apitokens.NewService(tokenStore, store)).
		WithExternalAuthService(
			identity.NewExternalAuthService(identity.ExternalAuthDeps{
				LinkStore:  linkStore,
				RecentAuth: nodataRecentAuth{},
			}),
			nil,
			nil,
			linkStore,
			nil,
		)
	webhooksController := webhookscontroller.NewController(webhooks.NewService(webhookStore, nil, nil), store, authSessions)

	app := apphttp.NewApp(testConfig(), slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{identityController, webhooksController},
	})

	cookie := registerHTTPUser(t, app, "admin", "admin@example.com")
	// registerHTTPUser 注册的是首个用户（初始 super_admin），linkStore 需要归属同一个 actor。
	linkStore.link = identity.ExternalIdentityLink{
		ID:       9,
		UserID:   1,
		Status:   identity.ExternalIdentityLinkStatusActive,
		Revision: 1,
	}
	return &nodataHarness{app: app, cookie: cookie, tokenStore: tokenStore, webhookStore: webhookStore, linkStore: linkStore}
}

// TestNoDataEndpointsReturnEnvelope 覆盖 7 条可在无库环境驱动的「无内容」端点
// （logout / revokeSession / revokeAPIToken / externalIdentityUnlink / deleteRole /
// replaceRolePermissions / deleteEndpoint），第 8 条见 nodata_envelope_postgres_test.go。
// setupPassword 见 nodata_envelope_postgres_test.go（它的 recent-auth 门控是具体类型）。
func TestNoDataEndpointsReturnEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		// assert 可选：确认请求真的走到了业务成功分支，而不是「碰巧 200」。
		assert func(t *testing.T, h *nodataHarness)
	}{
		{
			name:   "logout",
			method: nethttp.MethodPost,
			path:   "/api/v1/auth/logout",
		},
		{
			name:   "revokeSession",
			method: nethttp.MethodDelete,
			path:   "/api/v1/auth/sessions/session-7",
		},
		{
			name:   "revokeAPIToken",
			method: nethttp.MethodDelete,
			path:   "/api/v1/auth/tokens/7",
			assert: func(t *testing.T, h *nodataHarness) {
				t.Helper()
				if len(h.tokenStore.revoked) != 1 || h.tokenStore.revoked[0] != 7 {
					t.Fatalf("expected PAT 7 revoked exactly once, got %#v", h.tokenStore.revoked)
				}
			},
		},
		{
			name:   "externalIdentityUnlink",
			method: nethttp.MethodDelete,
			path:   "/api/v1/auth/external-identities/9",
		},
		{
			name:   "deleteRole",
			method: nethttp.MethodDelete,
			path:   "/api/v1/roles/auditor",
		},
		{
			name:   "replaceRolePermissions",
			method: nethttp.MethodPut,
			path:   "/api/v1/roles/auditor/permissions",
			body:   `{"permissions":[]}`,
		},
		{
			name:   "deleteEndpoint",
			method: nethttp.MethodDelete,
			path:   "/api/v1/admin/webhooks/endpoints/7",
			assert: func(t *testing.T, h *nodataHarness) {
				t.Helper()
				if len(h.webhookStore.deleted) != 1 || h.webhookStore.deleted[0] != 7 {
					t.Fatalf("expected webhook endpoint 7 deleted exactly once, got %#v", h.webhookStore.deleted)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newNoDataHarness(t)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req.AddCookie(h.cookie)
			resp, err := h.app.Test(req)
			if err != nil {
				t.Fatalf("%s %s request failed: %v", tc.method, tc.path, err)
			}
			defer resp.Body.Close()
			assertNoDataEnvelope(t, resp)
			if tc.assert != nil {
				tc.assert(t, h)
			}
		})
	}
}

// assertNoDataEnvelope 断言 #235 的响应形状：200 + {"code":200,"message":"OK","data":null}。
func assertNoDataEnvelope(t *testing.T, resp *nethttp.Response) {
	t.Helper()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200（NoData 统一 envelope），got %d", resp.StatusCode)
	}
	var body apiEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode NoData envelope: %v", err)
	}
	if body.Code != nethttp.StatusOK {
		t.Fatalf("expected envelope code 200, got %d", body.Code)
	}
	if !strings.EqualFold(body.Message, "ok") {
		t.Fatalf("expected envelope message OK/ok, got %q", body.Message)
	}
	if string(body.Data) != "null" {
		t.Fatalf("expected null data, got %s", string(body.Data))
	}
}

// 编译期锚点：这些 fake 必须始终满足对应接口，接口一变这里先炸。
var (
	_ apitokens.Store                    = (*nodataAPITokenStore)(nil)
	_ webhooks.Store                     = (*nodataWebhookStore)(nil)
	_ identity.ExternalIdentityLinkStore = (*nodataLinkStore)(nil)
	_ identity.RecentAuthChecker         = nodataRecentAuth{}
)
