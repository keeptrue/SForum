package trustlevelscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// newTestApp 装配四条路由，并注入四个测试缝（不碰数据库/会话）。
func newTestApp(t *testing.T, controller *Controller) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			return c.Status(fiberErr.Code).JSON(fiber.Map{"message": fiberErr.Message})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": err.Error()})
	}})
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)
	return app
}

func doRequest(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := app.Test(request, fiber.TestConfig{Timeout: 5000})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
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
	return response.StatusCode, payload.String()
}

func activeActor(permissions ...string) identity.Actor {
	grants := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		grants[permission] = true
	}
	return identity.Actor{
		ID: 7, Status: identity.UserStatusActive, Permissions: grants,
		TrustLevel: 1, TrustLevelResolved: true,
	}
}

// TestFeatureDisabledReturns404OnEveryRoute 断言四条路由**各一次** 404。
//
// 只测一条是这类守卫最常见的漏洞：三条里漏一条没人发现。
// 关闭态必须是 404（本刀之前这些端点不存在），不是 403、不是空对象。
func TestFeatureDisabledReturns404OnEveryRoute(t *testing.T) {
	controller := NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return false, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage), nil
		})
	app := newTestApp(t, controller)

	routes := []struct {
		method string
		path   string
	}{
		{fiber.MethodGet, "/api/v1/trust-levels"},
		{fiber.MethodGet, "/api/v1/admin/trust-levels"},
		{fiber.MethodPut, "/api/v1/admin/trust-levels"},
		{fiber.MethodPost, "/api/v1/admin/trust-levels/restore-defaults"},
	}
	for _, route := range routes {
		status, body := doRequest(t, app, route.method, route.path, `{"config":"{}"}`)
		if status != fiber.StatusNotFound {
			t.Fatalf("%s %s: status = %d（期望 404），body=%s", route.method, route.path, status, body)
		}
	}
}

// TestAdminRoutesRequirePermissions 断言缺权限时 403，且与关闭态的 404 可区分。
func TestAdminRoutesRequirePermissions(t *testing.T) {
	enabled := func(context.Context) (bool, error) { return true, nil }
	policy := func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }
	written := 0

	// 有 admin.access 但没有 trust.manage：读 200、写 403、恢复默认 403。
	reader := activeActor(identity.PermissionAdminAccess)
	controller := NewController(nil, nil, nil, nil).
		WithFeatureGate(enabled).
		WithPolicyReader(policy).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return reader, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error {
			written++
			return nil
		})
	app := newTestApp(t, controller)

	if status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", ""); status != fiber.StatusOK {
		t.Fatalf("后台读 status = %d（期望 200），body=%s", status, body)
	}
	if status, _ := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", `{"config":"{}"}`); status != fiber.StatusForbidden {
		t.Fatalf("缺 trust.manage 的写 status = %d（期望 403）", status)
	}
	if status, _ := doRequest(t, app, fiber.MethodPost, "/api/v1/admin/trust-levels/restore-defaults", ""); status != fiber.StatusForbidden {
		t.Fatalf("缺 trust.manage 的恢复默认 status = %d（期望 403）", status)
	}

	// 完全没有后台访问权限：读也 403。
	nobody := activeActor()
	app2 := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabled).WithPolicyReader(policy).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return nobody, nil }))
	if status, _ := doRequest(t, app2, fiber.MethodGet, "/api/v1/admin/trust-levels", ""); status != fiber.StatusForbidden {
		t.Fatalf("缺 admin.access 的读 status = %d（期望 403）", status)
	}

	// 写路径一次都不该被调用（403 必须在 save 之前拦住）。
	if written != 0 {
		t.Fatalf("被拒绝的写请求仍调用了 configWriter %d 次", written)
	}
}

// TestUpdateRejectsInvalidConfig 断言非法 config → 422，且带可读原因。
func TestUpdateRejectsInvalidConfig(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { return nil }))

	validConfig := mustEncode(t, trustlevels.DefaultPolicy())
	cases := []struct {
		name string
		body string
	}{
		{"未知字段（幻影字段 account_age_days）", `{"config":"{\"schemaVersion\":1,\"levels\":[{\"levelSlug\":\"member\",\"requirements\":{\"account_age_days\":3},\"limits\":{},\"earnable\":[]}]}"}`},
		{"schemaVersion 不符", `{"config":"{\"schemaVersion\":9,\"levels\":[]}"}`},
		{"缺少等级", `{"config":"{\"schemaVersion\":1,\"levels\":[]}"}`},
		{"body 不是 JSON", `not-json`},
		{"省略 limits（I-12 的静默清空面）", `{"config":"{\"schemaVersion\":1,\"levels\":[{\"levelSlug\":\"member\",\"requirements\":{},\"earnable\":[]},{\"levelSlug\":\"resident\",\"requirements\":{},\"limits\":{},\"earnable\":[]},{\"levelSlug\":\"veteran\",\"requirements\":{},\"limits\":{},\"earnable\":[]},{\"levelSlug\":\"leader\",\"requirements\":{},\"limits\":{},\"earnable\":[]}]}"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", testCase.body)
			if status != fiber.StatusUnprocessableEntity {
				t.Fatalf("status = %d（期望 422），body=%s", status, body)
			}
		})
	}

	// 对照组（只测拒绝的测试，把 handler 整个删掉也会绿）：合法 config → 200。
	status, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
		`{"config":`+mustJSONString(validConfig)+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("合法 config status = %d（期望 200），body=%s", status, body)
	}
}

// TestPublicPayloadHasNoLimits 断言公开面不下发限流参数（反滥用面）。
func TestPublicPayloadHasNoLimits(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("公开读 status = %d，body=%s", status, body)
	}
	if strings.Contains(body, "topicCooldownSeconds") || strings.Contains(body, "dailyTopicLimit") {
		t.Fatalf("公开响应泄漏了限流参数：%s", body)
	}
	if !strings.Contains(body, "levelSlug") || !strings.Contains(body, "requirements") {
		t.Fatalf("公开响应缺少等级定义：%s", body)
	}

	// 对照组：后台读面**必须**有限额（运营要配它）。
	status, adminBody := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("后台读 status = %d", status)
	}
	if !strings.Contains(adminBody, "topicCooldownSeconds") {
		t.Fatalf("后台读面缺少限额字段：%s", adminBody)
	}
}

func mustEncode(t *testing.T, policy trustlevels.Policy) string {
	t.Helper()
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	return encoded
}

func mustJSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestPayloadArraysAreNeverNull 锁定 JSON 形状：数组字段必须是 `[]`，不能是 `null`。
//
// 默认 earnable 是空集（D17），所以默认安装下每一档都会走这条路径；
// `null` 满足 required 但不满足「它是数组」，消费者 `for...of` 直接炸。
func TestPayloadArraysAreNeverNull(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	for _, path := range []string{"/api/v1/trust-levels", "/api/v1/admin/trust-levels"} {
		status, body := doRequest(t, app, fiber.MethodGet, path, "")
		if status != fiber.StatusOK {
			t.Fatalf("%s status = %d", path, status)
		}
		if strings.Contains(body, "null") && strings.Contains(body, `"earnable":null`) {
			t.Fatalf("%s 的 earnable 是 null：%s", path, body)
		}
		if !strings.Contains(body, `"earnable":[]`) {
			t.Fatalf("%s 的 earnable 不是空数组：%s", path, body)
		}
	}

	// 后台读面的 eligiblePermissions 同样不得为 null（白名单非空，这里只验形状）。
	_, adminBody := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	if strings.Contains(adminBody, `"eligiblePermissions":null`) {
		t.Fatalf("eligiblePermissions 是 null：%s", adminBody)
	}
	if strings.Contains(adminBody, `"recommendedConfig":null`) {
		t.Fatalf("recommendedConfig 是 null：%s", adminBody)
	}
}

// TestAdminEligiblePermissionsLiteral 是 M9 的白名单形状测试（J-01/J-02）。
//
// 两层断言刻意分开，因为它们是两件不同的事：
//
//  1. **字面集合相等**：七个键是字面量，不用 trustlevels.EarnableWhitelist() 反推。
//     用实现给自己出题等于没断言——白名单混进 user.ban 时两边会一起变绿。
//     注意别拿 SeedMemberPermissions 的顺序当期望：seeds.go 与白名单**顺序不同**
//     （seeds 是 topic.create → topic.edit_own → … → attachment.upload），
//     那是两个独立的字面量清单，断言它们同序必红。
//  2. **顺序等于白名单顺序**：白名单顺序是 UI 展示序的唯一权威（前端按数组顺序渲染
//     勾选矩阵），控制器若排序或走 map 迭代就会在这里红。
func TestAdminEligiblePermissionsLiteral(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	// 字面量，按字典序写（比较前会排序，写序不影响断言，但便于人读）。
	wantSet := []string{
		"attachment.upload",
		"post.create",
		"post.delete_own",
		"post.edit_own",
		"topic.create",
		"topic.delete_own",
		"topic.edit_own",
	}

	read := func() []string {
		t.Helper()
		status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
		if status != fiber.StatusOK {
			t.Fatalf("后台读 status = %d，body=%s", status, body)
		}
		var payload struct {
			Data struct {
				Eligible []string `json:"eligiblePermissions"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
		}
		return payload.Data.Eligible
	}

	got := read()
	if len(got) != 7 {
		t.Fatalf("eligiblePermissions 长度 = %d，期望 7：%v", len(got), got)
	}

	// 断言 1：集合相等（不看顺序）。
	gotSorted := slices.Clone(got)
	slices.Sort(gotSorted)
	if !slices.Equal(gotSorted, wantSet) {
		t.Fatalf("eligiblePermissions 集合 = %v，期望 %v", gotSorted, wantSet)
	}

	// 断言 2：顺序等于白名单顺序（UI 展示序的权威），且两次读之间稳定。
	wantOrder := trustlevels.EarnableWhitelist()
	if !slices.Equal(got, wantOrder) {
		t.Fatalf("eligiblePermissions 顺序 = %v，期望与白名单同序 %v", got, wantOrder)
	}
	second := read()
	if !slices.Equal(second, got) {
		t.Fatalf("两次读的顺序不一致：%v vs %v", got, second)
	}
}

// TestUpdateAdminStoreFailureIsServerError 锁定 M12-a 在**这条路径上**的行为变化：
// `PUT /admin/trust-levels` 的写库失败现在回 500 `options.store_failure`。
//
// 这是本刀唯一有意改变的 HTTP 状态码：之前它回 422 `validation.invalid`，
// 等于告诉运营「你的配置写错了」——那是在说谎，运营会去改一份本来就正确的配置，
// 而真正的问题是库。
func TestUpdateAdminStoreFailureIsServerError(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error {
			return fmt.Errorf("%w: %w", options.ErrStoreFailure, errors.New("connection refused"))
		}))

	validConfig := mustEncode(t, trustlevels.DefaultPolicy())
	status, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
		`{"config":`+mustJSONString(validConfig)+`}`)
	if status != fiber.StatusInternalServerError {
		t.Fatalf("写库失败 status = %d（期望 500），body=%s", status, body)
	}
	if !strings.Contains(body, "options.store_failure") {
		t.Fatalf("reason 不是 options.store_failure：%s", body)
	}
	if strings.Contains(body, "validation.invalid") {
		t.Fatalf("写库失败仍被报成输入错误（422 语义）：%s", body)
	}
}

// TestAdminBaselinePermissionsLiteral 钉住 Z3-b″ 的读面字段 baselinePermissions。
//
// 三层断言：
//  1. **字面集合相等**：两个键写死在测试里，不用 BaselineGrants() 反推自己
//     （拿实现给自己出题等于没断言）。
//  2. **顺序等于 BaselineGrants()**：顺序的权威在那个常量里，控制器若排序或走
//     map 迭代会在这里红。
//  3. **永远下发且不是 null**：省略会让人误判（earnable 的三态教训）。
func TestAdminBaselinePermissionsLiteral(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	wantSet := []string{"attachment.upload", "topic.create"}

	read := func() []string {
		t.Helper()
		status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
		if status != fiber.StatusOK {
			t.Fatalf("后台读 status = %d，body=%s", status, body)
		}
		if strings.Contains(body, `"baselinePermissions":null`) {
			t.Fatalf("baselinePermissions 是 null（应永远下发数组）：%s", body)
		}
		var payload struct {
			Data struct {
				Baseline []string `json:"baselinePermissions"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
		}
		return payload.Data.Baseline
	}

	got := read()
	gotSorted := slices.Clone(got)
	slices.Sort(gotSorted)
	if !slices.Equal(gotSorted, wantSet) {
		t.Fatalf("baselinePermissions 集合 = %v，期望 %v", gotSorted, wantSet)
	}
	if wantOrder := trustlevels.BaselineGrants(); !slices.Equal(got, wantOrder) {
		t.Fatalf("baselinePermissions 顺序 = %v，期望与 BaselineGrants() 同序 %v", got, wantOrder)
	}
	if second := read(); !slices.Equal(second, got) {
		t.Fatalf("两次读的顺序不一致：%v vs %v", got, second)
	}
}
