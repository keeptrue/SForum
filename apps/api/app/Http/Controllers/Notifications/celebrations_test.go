package notificationscontroller

// 庆祝展示层端点的传输层测试。
//
// 读面的领域语义由 `Models/Notifications/celebrations_postgres_integration_test.go`
// 用真库覆盖；这里只钉三件传输层的事：
//  1. 两个端点都要求登录（401 走 `auth.required`，不是 404/500）；
//  2. 响应头是 `private, no-store`（含「我这个账号收到了什么」，绝不能 public）；
//  3. 路由顺序：`/celebrations` 必须先于 `/:id` 命中，否则它会被当成通知 id。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

// celebrationTestStore 在通用 fake 之上实现 CelebrationStore，用来验传输层形状。
type celebrationTestStore struct {
	notificationTestStore
	cursor          int64
	initialized     bool
	latestID        int64
	rows            []notifications.CelebrationRow
	total           int64
	ackedCursorID   int64
	ackCalls        int
	ackReturnCursor int64
}

func (s *celebrationTestStore) CelebrationCursor(context.Context, int64) (int64, bool, error) {
	return s.cursor, s.initialized, nil
}

func (s *celebrationTestStore) LatestNotificationID(context.Context, int64) (int64, error) {
	return s.latestID, nil
}

func (s *celebrationTestStore) ListCelebrationRows(_ context.Context, _ int64, _ int64, limit int) ([]notifications.CelebrationRow, int64, error) {
	if len(s.rows) > limit {
		return s.rows[:limit], s.total, nil
	}
	return s.rows, s.total, nil
}

func (s *celebrationTestStore) AcknowledgeCelebrationCursor(_ context.Context, _ int64, cursorID int64) (int64, error) {
	s.ackCalls++
	s.ackedCursorID = cursorID
	return s.ackReturnCursor, nil
}

func celebrationTestApp(store notifications.Store, targets notifications.TargetVisibilityResolver) *fiber.App {
	controller := NewController(store, nil, nil, nil).WithTargetVisibility(targets)
	return apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens: notificationBearer{auth: apitokens.Authenticated{
			UserID: 42, TokenID: 7, PublicID: "active",
		}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
}

func celebrationRequest(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestCelebrationsRequireLogin 断言两个端点都要登录。
func TestCelebrationsRequireLogin(t *testing.T) {
	store := &celebrationTestStore{}
	app := celebrationTestApp(store, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications/celebrations"},
		{http.MethodPost, "/api/v1/notifications/celebrations/seen"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without session = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestCelebrationsRouteIsNotSwallowedByDetail 断言 /celebrations 不被 :id 抢走。
//
// 若注册顺序写反，这条会 404（detail 把 "celebrations" 当 id 解析失败）。
func TestCelebrationsRouteIsNotSwallowedByDetail(t *testing.T) {
	store := &celebrationTestStore{latestID: 11}
	app := celebrationTestApp(store, nil)
	resp := celebrationRequest(t, app, http.MethodGet, "/api/v1/notifications/celebrations", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET celebrations = %d, want 200（被 :id 抢走会 404）", resp.StatusCode)
	}
	if cache := resp.Header.Get(fiber.HeaderCacheControl); cache != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", cache)
	}
}

// TestCelebrationFeedWireAndCacheHeaders 断言首访响应形状（initialized/cursorId/total/items）。
func TestCelebrationFeedWireAndCacheHeaders(t *testing.T) {
	store := &celebrationTestStore{initialized: false, latestID: 99}
	app := celebrationTestApp(store, nil)
	resp := celebrationRequest(t, app, http.MethodGet, "/api/v1/notifications/celebrations", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cache := resp.Header.Get(fiber.HeaderCacheControl); cache != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", cache)
	}
	body := readBody(t, resp)
	for _, needle := range []string{`"initialized":false`, `"cursorId":99`, `"total":0`, `"items":[]`} {
		if !strings.Contains(body, needle) {
			t.Fatalf("body 缺少 %s：%s", needle, body)
		}
	}
}

// TestCelebrationAckReturnsStoredCursor 断言 acK 回执是落库后的游标。
func TestCelebrationAckReturnsStoredCursor(t *testing.T) {
	store := &celebrationTestStore{ackReturnCursor: 120}
	app := celebrationTestApp(store, nil)
	resp := celebrationRequest(t, app, http.MethodPost, "/api/v1/notifications/celebrations/seen", `{"cursorId":100}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cache := resp.Header.Get(fiber.HeaderCacheControl); cache != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", cache)
	}
	if store.ackCalls != 1 || store.ackedCursorID != 100 {
		t.Fatalf("ack calls=%d cursor=%d, want 1/100", store.ackCalls, store.ackedCursorID)
	}
	if body := readBody(t, resp); !strings.Contains(body, `"cursorId":120`) {
		t.Fatalf("响应必须是落库游标（120），得到 %s", body)
	}
}

// TestCelebrationAckRejectsMalformedBody 断言坏请求体是 422 而不是 500。
func TestCelebrationAckRejectsMalformedBody(t *testing.T) {
	store := &celebrationTestStore{}
	app := celebrationTestApp(store, nil)
	resp := celebrationRequest(t, app, http.MethodPost, "/api/v1/notifications/celebrations/seen", `{"cursorId":"not-a-number"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if store.ackCalls != 0 {
		t.Fatalf("非法请求体不得触达存储，得到 %d 次", store.ackCalls)
	}
}

// TestCelebrationsUnavailableWhenStoreLacksCapability 断言装配缺失时 503（不是空列表）。
func TestCelebrationsUnavailableWhenStoreLacksCapability(t *testing.T) {
	store := &notificationTestStore{}
	app := celebrationTestApp(store, nil)
	resp := celebrationRequest(t, app, http.MethodGet, "/api/v1/notifications/celebrations", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（装配缺失必须可区分于「没有庆祝」）", resp.StatusCode)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
