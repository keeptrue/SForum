package seocontroller

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件补 RV-54：POST /admin/seo/assets 的 denied-path + allowed 对照。
//
// 这条路由的授权不在 handler 里，而在 SEO.AssetService.Replace 的第一行
// （actor.Can(seo.manage)），所以 denied 用例必须带一个**看起来合法**的上传：
// 缺文件会在到达权限检查之前就以 422 结束，那样测的就不是门了。
// 反过来 allowed 对照故意不带文件——证明门放行之后仍然按正常校验回 422，
// 而不是「恒 403」或者「恒 422」。
func TestAdminSEOAssetUploadDeniedWithoutSEOPermission(t *testing.T) {
	for _, actor := range []identity.Actor{
		// 有后台访问权、没有 seo.manage。
		{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionAdminAccess: true,
		}},
		// 有别的设置权、仍然没有 seo.manage：SEO 键不跟着通用设置父权限展开。
		{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionSettingsManage: true,
		}},
	} {
		app := newSEOPermissionApp(actor)
		status, body := seoPermissionUpload(t, app)
		if status != http.StatusForbidden {
			t.Fatalf("actor %d 上传 SEO 素材: status = %d（期望 403），body=%s", actor.ID, status, body)
		}
	}
}

// TestAdminSEOAssetUploadAnonymousIsUnauthorized 把「没登录」和「没权限」分开。
func TestAdminSEOAssetUploadAnonymousIsUnauthorized(t *testing.T) {
	app := fiber.New()
	NewController(nil, nil, nil).RegisterRoutes(app.Group("/api/v1"))
	status, body := seoPermissionUpload(t, app)
	if status != http.StatusUnauthorized {
		t.Fatalf("匿名上传 SEO 素材: status = %d（期望 401），body=%s", status, body)
	}
}

// TestAdminSEOAssetUploadAllowsContentManagers 是 allowed 方向，而且必须**穿过门**。
//
// 这里发的是真实 multipart（seoPermissionUpload，context=seo.og.default）：
// FormFile 会成功，于是请求一定走到 AssetService.Replace —— 先是它的
// `actor.Can(seo.manage)` 正分支，再是它自己的 `assetContextPattern`
// （`^[a-z0-9]+(?:-[a-z0-9]+)*$`，"seo.og.default" 含点 ⇒ 不合法），最后由
// 控制器把 ErrInvalidAsset 映射成 422 `attachment.invalid`。
//
// 不能用「不带文件」凑 422：那条路径在 c.FormFile 处就结束，Replace 一行都没跑，
// 等于把门**加严**也测不出来（审查实证过的假绿形态）。所以验收靠变异：给
// assets.go 的门追加 admin.access 后本用例必须变成 403 / RED。
func TestAdminSEOAssetUploadAllowsContentManagers(t *testing.T) {
	app := newSEOPermissionApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionSEOManage: true,
	}})
	status, body := seoPermissionUpload(t, app)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("seo.manage 上传非法 context: status = %d（期望 422，且必须是 Replace 内部校验给的），body=%s",
			status, body)
	}
	if !strings.Contains(body, "attachment.invalid") {
		t.Fatalf("期望 attachment.invalid（Replace 的 assetContextPattern 校验产物），body=%s", body)
	}
}

func newSEOPermissionApp(actor identity.Actor) *fiber.App {
	app := fiber.New()
	// 与生产同构：handler panic 变成 500，而不是把测试进程带走。
	app.Use(recover.New())
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		return c.Next()
	})
	// nil assets service：denied 在权限检查处返回（Replace 只在 s 上取权限），
	// allowed 用例根本不进 Replace。
	NewController(nil, seoPermissionActors{actor: actor}, nil).RegisterRoutes(app.Group("/api/v1"))
	return app
}

// seoPermissionUpload 发一次带文件的上传，确保请求真的走到 AssetService.Replace。
func seoPermissionUpload(t *testing.T, app *fiber.App) (int, string) {
	t.Helper()
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatalf("构造 multipart: %v", err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n")); err != nil {
		t.Fatalf("写 multipart: %v", err)
	}
	if err := writer.WriteField("context", "seo.og.default"); err != nil {
		t.Fatalf("写 multipart 字段: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/seo/assets", &payload)
	request.Header.Set(fiber.HeaderContentType, writer.FormDataContentType())
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	return response.StatusCode, strings.TrimSpace(string(raw))
}

type seoPermissionActors struct{ actor identity.Actor }

func (store seoPermissionActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return store.actor, nil
}
