package http_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identitycontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Identity"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// #235 取 b 的第 8 条端点：POST /auth/password。
//
// 它没有放进 nodata_envelope_test.go 的表，因为它的 recent-auth 门控挂在**具体类型**
// `*identity.PostgresExternalAuthStore` 上（不是接口）：`h.externalAuthStore == nil` 直接 401，
// 非 nil 就要真的查 `user_recent_auth`。无库环境驱动不到 200，所以这里用一次性 PostgreSQL
// 跑真实请求。`./scripts/test-api-isolated.sh` 会提供库；单独 `go test ./app/Http/` 时
// testdb.IsolatedURL 会跳过（与仓里其它 *_postgres_integration_test.go 同约定）。
func TestPostgresSetupPasswordReturnsNoDataEnvelope(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	schema := fmt.Sprintf("nodata_envelope_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	// 只建 NoData 路径真正会查的那张表：recent-auth 记录。
	for _, statement := range []string{
		`CREATE TABLE ` + quoted + `.user_recent_auth (
			user_id BIGINT NOT NULL,
			session_fingerprint TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		)`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install fixture: %v\n%s", err, statement)
		}
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := newHTTPFakeStore()
	linkStore := &nodataLinkStore{}
	sessions := session.NewStore()
	authSessions := authsession.NewManager(sessions, authsession.Config{})
	externalAuthStore := identity.NewPostgresExternalAuthStore(pool)

	identityController := identitycontroller.NewControllerWithAuthSessions(
		identity.NewService(store),
		authSessions,
		humanverify.NewDisabledService(),
	).WithExternalAuthService(
		identity.NewExternalAuthService(identity.ExternalAuthDeps{
			LinkStore:  linkStore,
			RecentAuth: nodataRecentAuth{},
		}),
		nil,
		nil,
		linkStore,
		externalAuthStore,
	)

	app := apphttp.NewApp(testConfig(), slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{identityController, sidProbe{sessions: authSessions}},
	})

	cookie := registerHTTPUser(t, app, "admin", "admin@example.com")

	// 取当前会话的 SID（与 handler 的 currentSessionFingerprint 同一来源），
	// 再按同一算法写入 recent-auth 记录。
	sid := fetchSessionSID(t, app, cookie)
	if sid == "" {
		t.Fatal("expected a session SID after registration")
	}
	fingerprint := identity.SessionFingerprint(sid)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_recent_auth (user_id, session_fingerprint, expires_at) VALUES ($1, $2, now() + interval '5 minutes')`,
		1, fingerprint,
	); err != nil {
		t.Fatalf("seed recent-auth: %v", err)
	}

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"password":"correct horse battery staple"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("setup password request failed: %v", err)
	}
	defer resp.Body.Close()
	assertNoDataEnvelope(t, resp)
}

// sidProbe 只在测试里挂一条读 SID 的路由：handler 的 fingerprint 由 SID 派生，
// 测试必须拿到同一个 SID 才能造出「刚认证过」的记录。
type sidProbe struct{ sessions *authsession.Manager }

func (p sidProbe) RegisterRoutes(api fiber.Router) {
	api.Get("/__test/session-sid", func(c fiber.Ctx) error {
		sid, err := p.sessions.CurrentSID(c)
		if err != nil {
			return err
		}
		return c.SendString(sid)
	})
}

func fetchSessionSID(t *testing.T, app *fiber.App, cookie *nethttp.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/__test/session-sid", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("session sid request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 from sid probe, got %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read sid: %v", err)
	}
	return strings.TrimSpace(string(raw))
}
