package identitycontroller

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

// P2-2 的管理端点：DELETE /users/{userID}/api-tokens 的 allowed / denied 与
// super_admin 目标保护。
//
// 端点语义：只撤销 PAT，不动浏览器会话；权限口径与「强制下线」一致
// （user.manage + 目标 super_admin 仅超管可操作）。
func TestAdminRevokeUserAPITokensMatrix(t *testing.T) {
	app, store, tokens := newAdminAPITokensTestApp(t)

	// 目标用户先各持一条 PAT，便于断言「撤销真的发生了」。
	seedAPIToken(t, tokens, 2, "target-member")
	seedAPIToken(t, tokens, 3, "target-super")

	tests := []struct {
		name       string
		actorID    int64 // 0 = 匿名
		targetID   string
		wantStatus int
		wantReason string
		wantRevoke int64 // -1 表示不断言
	}{
		{
			name: "user.manage holder revokes a member's tokens", actorID: 1, targetID: "2",
			wantStatus: nethttp.StatusOK, wantRevoke: 1,
		},
		{
			name: "missing user.manage is forbidden", actorID: 2, targetID: "2",
			wantStatus: nethttp.StatusForbidden, wantReason: "permission.denied", wantRevoke: -1,
		},
		{
			// 普通 user.manage 持有者不能掐断超管的机器客户端。
			name: "non super-admin cannot touch a super-admin target", actorID: 1, targetID: "3",
			wantStatus: nethttp.StatusForbidden, wantReason: "api_token.super_admin_locked", wantRevoke: -1,
		},
		{
			name: "super-admin actor may revoke a super-admin target", actorID: 4, targetID: "3",
			wantStatus: nethttp.StatusOK, wantRevoke: 1,
		},
		{
			name: "unknown target is 404", actorID: 1, targetID: "999",
			wantStatus: nethttp.StatusNotFound, wantReason: "user.not_found", wantRevoke: -1,
		},
		{
			name: "anonymous is 401", actorID: 0, targetID: "2",
			wantStatus: nethttp.StatusUnauthorized, wantRevoke: -1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cookie *nethttp.Cookie
			if test.actorID > 0 {
				cookie = startAdminAPITokensSession(t, app, test.actorID)
			}
			req := httptest.NewRequest(nethttp.MethodDelete, "/api/v1/users/"+test.targetID+"/api-tokens", nil)
			if cookie != nil {
				req.AddCookie(cookie)
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, test.wantStatus, body)
			}
			if test.wantReason != "" {
				var envelope struct {
					Data struct {
						Reason string `json:"reason"`
					} `json:"data"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal(body, &envelope)
				reason := envelope.Data.Reason
				if reason == "" {
					reason = envelope.Message
				}
				if reason != test.wantReason {
					t.Fatalf("reason=%q want=%q body=%s", reason, test.wantReason, body)
				}
			}
			if test.wantRevoke >= 0 {
				var envelope struct {
					Data struct {
						Revoked int64 `json:"revoked"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil {
					t.Fatalf("decode body: %v body=%s", err, body)
				}
				if envelope.Data.Revoked != test.wantRevoke {
					t.Fatalf("revoked=%d want=%d body=%s", envelope.Data.Revoked, test.wantRevoke, body)
				}
			}
		})
	}

	// 目标用户 2 的 PAT 已被撤销：记录上必须有原因，且不能再认证。
	records, err := tokens.ListByUser(context.Background(), 2, true)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(records) != 1 || records[0].RevokedAt == nil || records[0].RevokeReason != apitokens.RevokeReasonAdmin {
		t.Fatalf("target token record = %#v", records)
	}
	// 会话不受影响：本端点只清令牌。
	if !store.hasActiveSession(2) {
		t.Fatal("revoking PATs must not end browser sessions")
	}
}

func newAdminAPITokensTestApp(t *testing.T) (*fiber.App, *sessionTestStore, *memoryAPITokenStore) {
	t.Helper()
	store := newSessionTestStore()
	// 四个主体：普通管理员、普通成员、超管目标、超管操作者。
	store.users[1] = identity.CurrentUser{
		ID: 1, Status: identity.UserStatusActive, Username: "admin",
		Permissions: []string{identity.PermissionUserManage},
	}
	store.users[2] = identity.CurrentUser{
		ID: 2, Status: identity.UserStatusActive, Username: "member",
	}
	store.users[3] = identity.CurrentUser{
		ID: 3, Status: identity.UserStatusActive, Username: "super-target",
		RoleKeys: []string{identity.RoleSuperAdmin},
	}
	store.users[4] = identity.CurrentUser{
		ID: 4, Status: identity.UserStatusActive, Username: "super-actor",
		RoleKeys: []string{identity.RoleSuperAdmin}, Permissions: []string{identity.PermissionUserManage},
	}

	service := identity.NewService(store)
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "admin-api-tokens-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	// 控制器只依赖 apitokens.Store 接口；这里复用同包已有的内存实现。
	tokenStore := newMemoryAPITokenStore()
	tokenService := apitokens.NewService(tokenStore, store)
	controller := NewControllerWithAuthSessions(service, manager, nil).WithAPITokens(tokenService)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, adminAPITokensLoginProvider{manager: manager}},
	})
	return app, store, tokenStore
}

// adminAPITokensLoginProvider 只为测试提供「以某个 userID 开会话」的入口。
type adminAPITokensLoginProvider struct {
	manager *authsession.Manager
}

func (p adminAPITokensLoginProvider) RegisterRoutes(api fiber.Router) {
	api.Post("/test-login/:userID", func(c fiber.Ctx) error {
		userID, err := strconv.ParseInt(c.Params("userID"), 10, 64)
		if err != nil || userID <= 0 {
			return fiber.NewError(fiber.StatusBadRequest, "validation.invalid")
		}
		if _, err := p.manager.Start(c, userID); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
}

func startAdminAPITokensSession(t *testing.T, app *fiber.App, userID int64) *nethttp.Cookie {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/test-login/"+strconv.FormatInt(userID, 10), nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK || len(resp.Cookies()) == 0 {
		t.Fatalf("login status=%d cookies=%d", resp.StatusCode, len(resp.Cookies()))
	}
	return resp.Cookies()[0]
}

func seedAPIToken(t *testing.T, store *memoryAPITokenStore, userID int64, name string) {
	t.Helper()
	if _, err := store.Create(context.Background(), userID,
		"pub-"+name, "hash-"+name, name, []string{identity.PermissionTopicCreate}, nil); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

// hasActiveSession 判断该用户是否还有未撤销的会话行（本端点不该动它们）。
func (s *sessionTestStore) hasActiveSession(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.sessions {
		if row.userID == userID && row.revokedAt == nil {
			return true
		}
	}
	return false
}
