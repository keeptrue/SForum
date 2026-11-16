package http_test

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"testing"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// W1 端点级证据：必登面（RequireActor）在「拿不到主体」时按原因回答。
//
// 覆盖判据（第 14 轮 A–E 三分法）：
//   - C 主体不可用（identity.ErrUserNotFound / disabled / banned）→ 401 auth.required
//   - D 依赖故障（会话存储、身份存储、超时）                    → 503 service.not_ready + Retry-After: 1
//   - E 未识别                                                  → 503（fail-closed，不放宽成匿名）
//   - A 无凭据                                                  → 401（必登面没有匿名视图）
//
// 每个端点都用 service=nil 的控制器：只要 handler 继续往下走就会崩，
// 所以「401/503 而不是 500」本身就证明 actor 门在业务之前答完。
func TestRequireActorEndpointsAnswerByCause(t *testing.T) {
	endpoints := []struct {
		name   string
		path   string
		module string
	}{
		{name: "admin overview", path: "/api/v1/admin/overview", module: "adminoverview"},
		{name: "attachment upload policy", path: "/api/v1/attachments/upload-policy", module: "attachments"},
		{name: "account navigation", path: "/api/v1/site/account-navigation", module: "sitechrome"},
		{name: "my profile", path: "/api/v1/profile", module: "profile"},
		{name: "my bookmarks", path: "/api/v1/profile/bookmarks", module: "profile"},
	}

	t.Run("D dependency failure answers 503", func(t *testing.T) {
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				app := readPathApp(t, endpoint.module,
					readPathSessionStore(errors.New("redis: connection refused")),
					staticReadPathActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}})
				response := readPathRequest(t, app, endpoint.path, "sforum_session=stale-session-cookie")
				defer response.Body.Close()
				assertEnvelope(t, response, nethttp.StatusServiceUnavailable, "service.not_ready")
				if got := response.Header.Get(fiber.HeaderRetryAfter); got != "1" {
					t.Fatalf("expected Retry-After: 1, got %q", got)
				}
			})
		}
	})

	t.Run("D identity store failure answers 503", func(t *testing.T) {
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				store := readPathSessionStore(nil)
				app := readPathApp(t, endpoint.module, store,
					errorReadPathActorStore{err: identity.ErrStoreUnavailable})
				response := readPathRequest(t, app, endpoint.path, readPathLoginCookie(t, store))
				defer response.Body.Close()
				assertEnvelope(t, response, nethttp.StatusServiceUnavailable, "service.not_ready")
			})
		}
	})

	t.Run("C missing subject answers 401", func(t *testing.T) {
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				store := readPathSessionStore(nil)
				app := readPathApp(t, endpoint.module, store,
					errorReadPathActorStore{err: identity.ErrUserNotFound})
				response := readPathRequest(t, app, endpoint.path, readPathLoginCookie(t, store))
				defer response.Body.Close()
				assertEnvelope(t, response, nethttp.StatusUnauthorized, "auth.required")
			})
		}
	})

	// 账号状态（disabled/banned）刻意**不**在门上判：必登面没有匿名回退，降级不是放宽，
	// 失效主体由域层按既有契约答 403 permission.denied（见决策记录 §4）。这里钉住
	// 边界：门放行、由域层裁决，而不是把 403 换成 401。
	t.Run("unusable subject status is decided by the domain, not the gate", func(t *testing.T) {
		for _, status := range []identity.UserStatus{identity.UserStatusDisabled, identity.UserStatusBanned} {
			t.Run(string(status), func(t *testing.T) {
				store := readPathSessionStore(nil)
				app := readPathApp(t, "adminoverview", store,
					staticReadPathActorStore{actor: identity.Actor{ID: 7, Status: status}})
				response := readPathRequest(t, app, "/api/v1/admin/overview", readPathLoginCookie(t, store))
				defer response.Body.Close()
				if response.StatusCode != nethttp.StatusForbidden {
					body, _ := io.ReadAll(response.Body)
					t.Fatalf("status = %d, want 403 from the domain layer (%s)", response.StatusCode, body)
				}
			})
		}
	})

	// E 必须 fail-closed：不确定的错误既不能当匿名放行，也不能说成「你的凭据无效」。
	t.Run("E unrecognized failure answers 503", func(t *testing.T) {
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				store := readPathSessionStore(nil)
				app := readPathApp(t, endpoint.module, store,
					errorReadPathActorStore{err: errors.New("unexpected actor store failure")})
				response := readPathRequest(t, app, endpoint.path, readPathLoginCookie(t, store))
				defer response.Body.Close()
				assertEnvelope(t, response, nethttp.StatusServiceUnavailable, "service.not_ready")
			})
		}
	})

	// A：必登面没有匿名视图——不带凭据必须 401，而且不能落到 service 上。
	t.Run("A anonymous answers 401", func(t *testing.T) {
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				app := readPathApp(t, endpoint.module, readPathSessionStore(nil), staticReadPathActorStore{})
				response := readPathRequest(t, app, endpoint.path, "")
				defer response.Body.Close()
				assertEnvelope(t, response, nethttp.StatusUnauthorized, "auth.required")
			})
		}
	})
}

func assertEnvelope(t *testing.T, response *nethttp.Response, status int, reason string) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d (%s)", response.StatusCode, status, body)
	}
	var envelope apiEnvelope[apiErrorData]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope %s: %v", body, err)
	}
	if envelope.Data.Reason != reason {
		t.Fatalf("reason = %q, want %q (%s)", envelope.Data.Reason, reason, body)
	}
}
