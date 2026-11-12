package badgescontroller

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// GAP-1 / A-07：`GET /admin/badges/backfill-status` 的响应头与权限路径。
//
// 三条路径各自独立断言：未登录是 401（不是 403）、缺 badge.view 是 403 permission.denied
// （不是 badges_disabled）、成功必须 `Cache-Control: private, no-store`（运维态数据
// 不可被任何中间层缓存）。把三条写成一条「反正不是 200」就等于没测。

type fakeBackfillRunsReader struct {
	items []badges.BackfillRunStatus
	err   error
}

func (r fakeBackfillRunsReader) ListLatestBackfillRuns(context.Context) ([]badges.BackfillRunStatus, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.items, nil
}

func backfillStatusController(actor identity.Actor) *Controller {
	admin := badges.NewAdminService(nil)
	admin.SetBackfillRuns(fakeBackfillRunsReader{})
	controller := &Controller{admin: admin, gate: fakeGate{enabled: true}}
	return controller.WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return actor, nil })
}

func callBackfillStatus(t *testing.T, controller *Controller) (*httptest.ResponseRecorder, int, string) {
	t.Helper()
	app := newCatalogApp(t, controller)
	response, err := app.Test(
		httptest.NewRequest(fiber.MethodGet, "/api/v1/admin/badges/backfill-status", nil),
		fiber.TestConfig{Timeout: 5000},
	)
	if err != nil {
		t.Fatalf("GET backfill-status: %v", err)
	}
	defer response.Body.Close()
	payload := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buf)
		payload.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return nil, response.StatusCode, payload.String() + "\x00" + response.Header.Get(fiber.HeaderCacheControl)
}

// TestBackfillStatusServesNoStoreWithBadgeView 断言成功路径的缓存头与 items 形状。
func TestBackfillStatusServesNoStoreWithBadgeView(t *testing.T) {
	controller := backfillStatusController(identity.Actor{ID: 3, Permissions: map[string]bool{badges.PermissionBadgeView: true}})
	_, status, combined := callBackfillStatus(t, controller)
	body, cacheControl, _ := strings.Cut(combined, "\x00")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d，want 200（body=%s）", status, body)
	}
	if cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control = %q，want \"private, no-store\"", cacheControl)
	}
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("无 run 时必须回空数组而不是 null，body=%s", body)
	}
}

// TestBackfillStatusDeniesWithoutBadgeView 断言缺 badge.view 是 403 permission.denied。
func TestBackfillStatusDeniesWithoutBadgeView(t *testing.T) {
	controller := backfillStatusController(identity.Actor{ID: 3, Permissions: map[string]bool{badges.PermissionBadgeManage: true}})
	_, status, combined := callBackfillStatus(t, controller)
	body, _, _ := strings.Cut(combined, "\x00")
	if status != fiber.StatusForbidden {
		t.Fatalf("status = %d，want 403（body=%s）", status, body)
	}
	// 403 的语义必须能与「产品开关关闭」区分：后者才是 badges_disabled。
	if strings.Contains(body, badges.CodeBadgesDisabled) {
		t.Fatalf("权限拒绝不得伪装成功能关闭：%s", body)
	}
	if !strings.Contains(body, "permission") {
		t.Fatalf("403 应带权限拒绝信号，body=%s", body)
	}
}

// TestBackfillStatusRequiresLogin 断言未登录是 401 auth.required（不是 403）。
func TestBackfillStatusRequiresLogin(t *testing.T) {
	controller := backfillStatusController(identity.Actor{})
	_, status, combined := callBackfillStatus(t, controller)
	body, _, _ := strings.Cut(combined, "\x00")
	if status != fiber.StatusUnauthorized {
		t.Fatalf("status = %d，want 401（body=%s）", status, body)
	}
	if !strings.Contains(body, "auth.required") {
		t.Fatalf("401 必须带 auth.required，body=%s", body)
	}
}
