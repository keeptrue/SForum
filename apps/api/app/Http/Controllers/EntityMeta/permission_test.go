package entitymetacontroller

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
	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件补 RV-54：/admin/entity-meta/* 四条路由的 denied-path + allowed 对照，
// 外加写入面 `/entity-meta/:entityType/:entityID` 的「失效主体」路径。
//
// 四条后台路由的门在 entitymeta.Service.requireManage（entity_meta.manage，或由
// 父权限 settings.manage 展开），且都在读 store 之前执行，所以 denied 用例不需要
// 数据库；allowed 用例用「非法 entityType」停在 422，证明门放行且后续校验照常。
func TestAdminEntityMetaRoutesDeniedWithoutManagePermission(t *testing.T) {
	app := newEntityMetaPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess: true,
	}})

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/entity-meta/definitions", ""},
		{http.MethodPost, "/api/v1/admin/entity-meta/definitions", `{}`},
		{http.MethodPatch, "/api/v1/admin/entity-meta/definitions/acme.wiki.pages", `{}`},
		{http.MethodDelete, "/api/v1/admin/entity-meta/definitions/acme.wiki.pages", ""},
	}
	for _, route := range routes {
		status, body := entityMetaPermissionCall(t, app, route.method, route.path, route.body)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d（期望 403），body=%s", route.method, route.path, status, body)
		}
	}
}

// TestAdminEntityMetaRoutesAllowManageAndParentPermission 是反方向，并钉住父权限兼容：
// 只配 settings.manage 的旧部署仍然能管字段定义。
func TestAdminEntityMetaRoutesAllowManageAndParentPermission(t *testing.T) {
	for _, permission := range []string{
		identity.PermissionEntityMetaManage,
		identity.PermissionSettingsManage,
	} {
		app := newEntityMetaPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive,
			Permissions: map[string]bool{permission: true}})
		status, body := entityMetaPermissionCall(t, app, http.MethodGet,
			"/api/v1/admin/entity-meta/definitions?entityType=not-a-real-type", "")
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d（期望 422），body=%s", permission, status, body)
		}
		if !strings.Contains(body, "entity_meta.invalid") {
			t.Fatalf("%s: 响应里没有 entity_meta.invalid：%s", permission, body)
		}
	}
}

// TestEntityMetaValueWriteDeniedForInactiveActor 覆盖非后台的写入面：
// `/entity-meta/:entityType/:entityID` 的授权是「登录 + 对该实体有写权」，
// 主体失效时必须在读 store 之前就 403（UpsertValues 的 IsActive 检查先于
// EntityExists），否则一次失效会话就能走到持久化层。
func TestEntityMetaValueWriteDeniedForInactiveActor(t *testing.T) {
	app := newEntityMetaPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusDisabled, Permissions: map[string]bool{
		identity.PermissionEntityMetaManage: true,
	}})
	status, body := entityMetaPermissionCall(t, app, http.MethodPut,
		"/api/v1/entity-meta/"+entitymeta.EntityUser+"/1",
		`{"values":[{"fieldKey":"acme.wiki.pages","value":"x"}]}`)
	if status != http.StatusForbidden {
		t.Fatalf("失效主体写实体元数据: status = %d（期望 403），body=%s", status, body)
	}
}

func newEntityMetaPermissionApp(actor identity.Actor) *fiber.App {
	app := fiber.New()
	// 与生产同构：handler panic 变成 500，而不是把测试进程带走。
	app.Use(recover.New())
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		return c.Next()
	})
	// nil service：denied 在 requireManage 处返回，allowed 用例停在 validEntityType。
	NewController(nil, entityMetaPermissionActors{actor: actor}, nil).RegisterRoutes(app.Group("/api/v1"))
	return app
}

func entityMetaPermissionCall(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
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

type entityMetaPermissionActors struct{ actor identity.Actor }

func (store entityMetaPermissionActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return store.actor, nil
}
