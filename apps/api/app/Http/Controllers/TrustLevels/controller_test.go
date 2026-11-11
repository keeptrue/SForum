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

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// newTestApp 装配四条路由，并注入四个测试缝（不碰数据库/会话）。
func newTestApp(t *testing.T, controller *Controller) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		// 生产环境的错误信封是 {code, message, data:{reason, details}}；测试里复刻同一
		// 形状，否则 details 的断言只是在测一份测试专用的形状。
		var apiErr *apphttp.APIError
		if errors.As(err, &apiErr) {
			data := fiber.Map{"reason": apiErr.Reason}
			if len(apiErr.Details) > 0 {
				data["details"] = apiErr.Details
			}
			return c.Status(apiErr.Status).JSON(fiber.Map{
				"code": apiErr.Status, "message": apiErr.Reason, "data": data,
			})
		}
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
		{"未知字段（幻影字段 account_age_days）", `{"config":"{\"schemaVersion\":2,\"tiers\":[{\"id\":\"member\",\"name\":{\"zh\":\"甲\",\"en\":\"A\"},\"icon\":\"leaf\",\"requirements\":{\"account_age_days\":3},\"limits\":{},\"earnable\":[]}]}"}`},
		{"schemaVersion 不符", `{"config":"{\"schemaVersion\":9,\"tiers\":[]}"}`},
		{"缺少档位", `{"config":"{\"schemaVersion\":2,\"tiers\":[]}"}`},
		{"body 不是 JSON", `not-json`},
		{"省略 limits（I-12 的静默清空面）", `{"config":"{\"schemaVersion\":2,\"tiers\":[{\"id\":\"member\",\"name\":{\"zh\":\"甲\",\"en\":\"A\"},\"icon\":\"leaf\",\"requirements\":{},\"earnable\":[]}]}"}`},
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

// TestPublicPayloadHasNoLimits 断言公开面不下发限流参数（反滥用面），
// 同时锁定目录形状：TL0 合成首条 + 每条都有 displayName/icon。
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
	if !strings.Contains(body, "displayName") || !strings.Contains(body, "\"icon\"") {
		t.Fatalf("公开响应缺少展示字段（自定义档名/图标要靠它下发）：%s", body)
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

// TestAdminPayloadCarriesTierEditorMetadata 锁定后台读面的编辑器元数据：
// 图标白名单、命名池、档数区间、下一个派生序号、TL0 的只读标记。
//
// 这些字段是「自定义 N 档」的前端唯一数据源：前端不得自己再抄一份图标/名字/档数。
func TestAdminPayloadCarriesTierEditorMetadata(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	_, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	var payload struct {
		Data struct {
			Levels []struct {
				Level       int    `json:"level"`
				LevelSlug   string `json:"levelSlug"`
				DisplayName struct {
					Zh string `json:"zh"`
					En string `json:"en"`
				} `json:"displayName"`
				Icon     string `json:"icon"`
				Editable bool   `json:"editable"`
				Retired  bool   `json:"retired"`
			} `json:"levels"`
			IconChoices []string `json:"iconChoices"`
			NamePool    struct {
				Zh []string `json:"zh"`
				En []string `json:"en"`
			} `json:"namePool"`
			TierLimits struct {
				Min int `json:"min"`
				Max int `json:"max"`
			} `json:"tierLimits"`
			NextTierSeq int `json:"nextTierSeq"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
	}
	data := payload.Data

	// 图标白名单是 16 个，且前五个是历史默认序列（芽/叶/屋/山/冠）。
	if len(data.IconChoices) != 16 {
		t.Fatalf("iconChoices 长度 = %d，期望 16", len(data.IconChoices))
	}
	wantFirstFive := []string{"sprout", "leaf", "home", "mountain", "crown"}
	if !slices.Equal(data.IconChoices[:5], wantFirstFive) {
		t.Fatalf("默认图标序列前五项 = %v，期望 %v", data.IconChoices[:5], wantFirstFive)
	}
	// 命名池双语同序同长。
	if len(data.NamePool.Zh) == 0 || len(data.NamePool.Zh) != len(data.NamePool.En) {
		t.Fatalf("命名池形状不对：%d/%d", len(data.NamePool.Zh), len(data.NamePool.En))
	}
	// 档数区间与下一个派生序号（内置占 1..5，所以第一个新档是 tier_6）。
	if data.TierLimits.Min != 1 || data.TierLimits.Max != 8 {
		t.Fatalf("tierLimits = %+v，期望 1..8", data.TierLimits)
	}
	if data.NextTierSeq != 6 {
		t.Fatalf("nextTierSeq = %d，期望 6", data.NextTierSeq)
	}
	// 目录：TL0 合成首条且不可编辑，其后是 active 档位（默认 4 档）。
	if len(data.Levels) != 5 {
		t.Fatalf("levels 长度 = %d，期望 TL0 + 4 档 = 5", len(data.Levels))
	}
	first := data.Levels[0]
	if first.Level != 0 || first.LevelSlug != trustlevels.NewcomerID || first.Editable || first.Retired {
		t.Fatalf("首条不是只读的 TL0：%+v", first)
	}
	if first.DisplayName.Zh == "" || first.DisplayName.En == "" {
		t.Fatalf("TL0 缺少双语名：%+v", first.DisplayName)
	}
	for index, level := range data.Levels[1:] {
		if level.Level != index+1 {
			t.Fatalf("第 %d 条的 level = %d", index+2, level.Level)
		}
		if !level.Editable || level.Retired {
			t.Fatalf("TL%d 应可编辑且未停用：%+v", level.Level, level)
		}
		if !slices.Contains(data.IconChoices, level.Icon) {
			t.Fatalf("TL%d 的图标 %q 不在白名单内", level.Level, level.Icon)
		}
		if level.DisplayName.Zh == "" || level.DisplayName.En == "" {
			t.Fatalf("TL%d 缺少双语名", level.Level)
		}
	}
}

// sixTierPolicy 造一份 6 档策略（内置 4 档 + 2 个新档），用于验证「判顶跟配置走」。
func sixTierPolicy(t *testing.T) trustlevels.Policy {
	t.Helper()
	raw := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"limits":{},"earnable":[]},
		{"id":"veteran","name":{"zh":"丙","en":"C"},"icon":"mountain","requirements":{},"limits":{},"earnable":[]},
		{"id":"leader","name":{"zh":"丁","en":"D"},"icon":"crown","requirements":{},"limits":{},"earnable":[]},
		{"id":"","name":{"zh":"戊","en":"E"},"icon":"star","requirements":{},"limits":{},"earnable":[]},
		{"id":"","name":{"zh":"己","en":"F"},"icon":"sun","requirements":{"topic_count":50},"limits":{},"earnable":[]}]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return policy
}

// fixedActivityReader 与 fixedPolicySource 是控制器层的两缝实现。
type fixedActivityReader struct{ activity trustlevels.Activity }

func (r fixedActivityReader) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return r.activity, nil
}

type fixedPolicySource struct{ policy trustlevels.Policy }

func (s fixedPolicySource) Policy(context.Context) (trustlevels.Policy, error) {
	return s.policy, nil
}

// TestViewerProgressUsesLastRank 是「判顶写死 TL4」的回归靶子（controller.go:108）：
// 6 档策略下第 5 档用户必须拿到 nextLevel 与 bottleneck，而不是被告知「已达最高等级」。
func TestViewerProgressUsesLastRank(t *testing.T) {
	policy := sixTierPolicy(t)
	if policy.LastRank() != 6 {
		t.Fatalf("前置条件失败：LastRank = %d", policy.LastRank())
	}
	resolver := trustlevels.NewResolver(
		fixedActivityReader{activity: trustlevels.Activity{TopicCount: 10}},
		fixedPolicySource{policy: policy},
	)
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAdminAccess: true},
		TrustLevel:  5, TrustLevelResolved: true,
	}
	app := newTestApp(t, NewController(nil, resolver, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policy, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return actor, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d，body=%s", status, body)
	}
	var payload struct {
		Data struct {
			Viewer *struct {
				Level      int    `json:"level"`
				LevelSlug  string `json:"levelSlug"`
				NextLevel  int    `json:"nextLevel"`
				Bottleneck *struct {
					Metric    string `json:"metric"`
					Current   int64  `json:"current"`
					Threshold int64  `json:"threshold"`
				} `json:"bottleneck"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
	}
	if payload.Data.Viewer == nil {
		t.Fatalf("没有 viewer 段：%s", body)
	}
	viewer := payload.Data.Viewer
	if viewer.LevelSlug != "tier_6" {
		t.Fatalf("第 5 档的 levelSlug = %q，期望 tier_6", viewer.LevelSlug)
	}
	if viewer.NextLevel != 6 {
		t.Fatalf("nextLevel = %d，期望 6（6 档站点上第 5 档不是满级）", viewer.NextLevel)
	}
	if viewer.Bottleneck == nil || viewer.Bottleneck.Metric != "topic_count" {
		t.Fatalf("bottleneck = %+v，期望卡在 topic_count", viewer.Bottleneck)
	}
}

// configErrorDetail 是 422 明细的 wire 形状（与 dto.go 的 trustLevelConfigError 同形）。
type configErrorDetail struct {
	Rank       int            `json:"rank"`
	TierID     string         `json:"tierId"`
	Field      string         `json:"field"`
	ReasonCode string         `json:"reasonCode"`
	Params     map[string]any `json:"params"`
	Reason     string         `json:"reason"`
}

// TestUpdateConfigErrorDetails 是 P2 的回归靶子：422 必须带字段级明细（D-N14）。
//
// 三条正例对应验收里的三项失败回落：非法图标 / 缺 limits / id 重复。只断言 422
// 的测试抓不到这个缺陷——那次「统一一句话」正是这么活下来的。
func TestUpdateConfigErrorDetails(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { return nil }))

	type detail = configErrorDetail
	put := func(t *testing.T, config string) (int, []configErrorDetail, string) {
		t.Helper()
		status, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
			`{"config":`+mustJSONString(config)+`}`)
		var payload struct {
			Message string `json:"message"`
			Data    struct {
				Reason  string `json:"reason"`
				Details struct {
					Errors []detail `json:"errors"`
				} `json:"details"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("响应不是 JSON：%v，body=%s", err, body)
		}
		return status, payload.Data.Details.Errors, payload.Data.Reason
	}

	// 1) 非法图标：明细必须指到那一档的 icon。
	badIcon := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"limits":{},"earnable":[]},
		{"id":"veteran","name":{"zh":"丙","en":"C"},"icon":"rocket","requirements":{},"limits":{},"earnable":[]}]}`
	status, details, reason := put(t, badIcon)
	t.Logf("非法图标的 422 响应：%s", bodyOf(t, app, badIcon))
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422）", status)
	}
	if reason != CodeInvalidConfig {
		t.Fatalf("机器码 = %q，期望稳定的 %q", reason, CodeInvalidConfig)
	}
	if !hasDetail(details, "veteran", "icon", trustlevels.CodeIconNotWhitelisted) {
		t.Fatalf("明细里没有 veteran 的 icon（code=icon_not_whitelisted）：%+v", details)
	}
	if icon, _ := details[0].Params["icon"].(string); icon != "rocket" {
		t.Fatalf("params 里没有可插值的 icon：%+v", details[0].Params)
	}

	// 2) 缺 limits：明细必须指到那一档的 limits（而不是只报「配置无效」）。
	missingLimits := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"earnable":[]}]}`
	status, details, _ = put(t, missingLimits)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422）", status)
	}
	if !hasDetail(details, "resident", "limits", trustlevels.CodeLimitsMissing) {
		t.Fatalf("明细里没有 resident 的 limits（code=limits_missing）：%+v", details)
	}

	// 3) id 重复：明细必须指出冲突的 id。
	duplicate := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]},
		{"id":"member","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"limits":{},"earnable":[]}]}`
	status, details, _ = put(t, duplicate)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422）", status)
	}
	found := false
	for _, item := range details {
		if item.Field == "id" && item.TierID == "member" && item.ReasonCode == trustlevels.CodeIDDuplicate && item.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("明细里没有指出重复的 id：%+v", details)
	}

	// 4) 明细有上界：一份全坏的配置不得刷出几百条。
	many := make([]string, 0, trustlevels.MaxConfigErrorDetails+4)
	for index := 0; index < trustlevels.MaxConfigErrorDetails+4; index++ {
		many = append(many, `{"id":"","name":{"zh":"坏","en":"Bad"},"icon":"rocket","requirements":{},"limits":{},"earnable":[]}`)
	}
	status, details, _ = put(t, `{"schemaVersion":2,"tiers":[`+strings.Join(many, ",")+`]}`)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422）", status)
	}
	if len(details) != trustlevels.MaxConfigErrorDetails {
		t.Fatalf("明细条数 = %d，期望上界 %d", len(details), trustlevels.MaxConfigErrorDetails)
	}
}

// bodyOf 复跑一次同样的请求并返回原始响应体（只用于 -v 日志里留一份真实样本）。
func bodyOf(t *testing.T, app *fiber.App, config string) string {
	t.Helper()
	_, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
		`{"config":`+mustJSONString(config)+`}`)
	return body
}

// hasDetail 断言明细里存在「该档位 + 该字段」的一条。
// hasDetail 断言存在「该档位 + 该字段 + 该稳定代码」的一条明细。
func hasDetail(details []configErrorDetail, tierID, field, code string) bool {
	for _, item := range details {
		if item.TierID == tierID && item.Field == field && item.ReasonCode == code && item.Reason != "" {
			return true
		}
	}
	return false
}

// tierConfigJSON 造一条最小合法 v2 档位（id 传空串 = 新档，序号由服务端派生）。
// 与 Models/TrustLevels 测试里的同名辅助是不同 package 的两份，互不影响。
func tierConfigJSON(id string) string {
	return `{"id":"` + id + `","name":{"zh":"自定义档","en":"Custom"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}`
}

// TestPublicCatalogCarriesEffectiveColors 锁定公开目录的 color 契约：
// 每一条（含合成的 TL0）都带非空有效色，且取值在白名单内。
//
// 「恒非空」是关键：它把「跟随位次」解析成具体值，前端不需要再发明一条空值分支。
func TestPublicCatalogCarriesEffectiveColors(t *testing.T) {
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return identity.Actor{}, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d（期望 200），body=%s", status, body)
	}
	var payload struct {
		Data struct {
			Levels []struct {
				Level int    `json:"level"`
				Color string `json:"color"`
			} `json:"levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
	}
	if len(payload.Data.Levels) != 5 {
		t.Fatalf("levels 长度 = %d，期望 TL0 + 4 档 = 5", len(payload.Data.Levels))
	}
	// 字面量期望，不用 DefaultTierColor 自证：默认策略下 TL0..TL4 就是这套色序，
	// 写死才能抓住「色板被改错位置/长度」这类回归。
	wantColors := []string{"slate", "teal", "emerald", "sky", "indigo"}
	for index, level := range payload.Data.Levels {
		if level.Color == "" {
			t.Fatalf("TL%d 的 color 是空串（契约里是 required 且恒非空）", level.Level)
		}
		if level.Color != wantColors[index] {
			t.Fatalf("TL%d 的 color = %q，期望 %q", level.Level, level.Color, wantColors[index])
		}
	}
}

// TestAdminPayloadCarriesColorsAndTombstoneSeq 锁定后台读面的两个新字段：
// colorChoices 与「计入墓碑的 nextTierSeq」。
func TestAdminPayloadCarriesColorsAndTombstoneSeq(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `,` + tierConfigJSON("resident") + `,` +
			tierConfigJSON("veteran") + `,` + tierConfigJSON("leader") + `],"deletedTierIds":["tier_7"]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policy, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d（期望 200），body=%s", status, body)
	}
	var payload struct {
		Data struct {
			ColorChoices []string `json:"colorChoices"`
			IconChoices  []string `json:"iconChoices"`
			NextTierSeq  int      `json:"nextTierSeq"`
			Levels       []struct {
				Level int    `json:"level"`
				Color string `json:"color"`
			} `json:"levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
	}
	// 字面量期望：白名单 9 项、顺序即 rank（TL0..TL8），别用 TierColors() 自证。
	wantColors := []string{"slate", "teal", "emerald", "sky", "indigo", "violet", "fuchsia", "rose", "amber"}
	if !slices.Equal(payload.Data.ColorChoices, wantColors) {
		t.Fatalf("colorChoices = %v，期望 %v", payload.Data.ColorChoices, wantColors)
	}
	if len(payload.Data.ColorChoices) != 9 {
		t.Fatalf("colorChoices 长度 = %d，期望 9", len(payload.Data.ColorChoices))
	}
	if payload.Data.NextTierSeq != 8 {
		t.Fatalf("nextTierSeq = %d，期望 8（墓碑 tier_7 计入）", payload.Data.NextTierSeq)
	}
	for _, level := range payload.Data.Levels {
		if level.Color == "" {
			t.Fatalf("后台读面 TL%d 的 color 是空串", level.Level)
		}
	}
}

// TestUpdateConfigErrorDetailsColorsAndTombstones 覆盖新增四条码的 422 投影：
// 颜色不在白名单、墓碑不合法/重复/与档位冲突。
func TestUpdateConfigErrorDetailsColorsAndTombstones(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { return nil }))

	put := func(t *testing.T, config string) (int, []configErrorDetail) {
		t.Helper()
		status, body := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
			`{"config":`+mustJSONString(config)+`}`)
		var payload struct {
			Data struct {
				Details struct {
					Errors []configErrorDetail `json:"errors"`
				} `json:"details"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("响应不是 JSON：%v，body=%s", err, body)
		}
		return status, payload.Data.Details.Errors
	}

	badColor := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","color":"neon","requirements":{},"limits":{},"earnable":[]}]}`
	status, details := put(t, badColor)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("非法颜色 status = %d（期望 422）", status)
	}
	if !hasDetail(details, "member", "color", trustlevels.CodeColorNotWhitelisted) {
		t.Fatalf("明细里没有 color_not_whitelisted：%+v", details)
	}

	cases := map[string]struct {
		config string
		code   string
		tierID string
	}{
		"不合法墓碑": {`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `],"deletedTierIds":["tier_x"]}`,
			trustlevels.CodeDeletedTierIDInvalid, "tier_x"},
		"重复墓碑": {`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `],"deletedTierIds":["tier_6","tier_6"]}`,
			trustlevels.CodeDeletedTierIDDuplicate, "tier_6"},
		"与档位冲突": {`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `],"deletedTierIds":["member"]}`,
			trustlevels.CodeDeletedTierIDPresent, "member"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			status, details := put(t, testCase.config)
			if status != fiber.StatusUnprocessableEntity {
				t.Fatalf("status = %d（期望 422）", status)
			}
			if !hasDetail(details, testCase.tierID, "deletedTierIds", testCase.code) {
				t.Fatalf("明细里没有 %q：%+v", testCase.code, details)
			}
		})
	}
}

// TestUpdatePayloadCarriesColorMetadataAndKeepsTombstones 覆盖 PUT 的写路径载荷契约：
// 响应带回 colorChoices / nextTierSeq，写出的 config 保留运营提交的颜色与墓碑。
//
// restore-defaults 的接线回归护栏归 controller_restore_test.go（一个行为一个 owner）：
// 那边专测「领域对、接线错」的形状；这里只管 PUT，不重复断言同一条接线。
func TestUpdatePayloadCarriesColorMetadataAndKeepsTombstones(t *testing.T) {
	previous, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `,` + tierConfigJSON("resident") + `,` +
			tierConfigJSON("veteran") + `,` + tierConfigJSON("leader") + `],"deletedTierIds":["tier_6"]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	written := ""
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return previous, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(_ context.Context, _ identity.Actor, value string) error {
			written = value
			return nil
		}))

	submitted := `{"schemaVersion":2,"tiers":[` +
		`{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","color":"rose","requirements":{},"limits":{},"earnable":[]}` +
		`],"deletedTierIds":["tier_6"]}`
	status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
		`{"config":`+mustJSONString(submitted)+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("PUT status = %d（期望 200），body=%s", status, response)
	}
	var payload struct {
		Data struct {
			ColorChoices []string `json:"colorChoices"`
			NextTierSeq  int      `json:"nextTierSeq"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, response)
	}
	if len(payload.Data.ColorChoices) != 9 {
		t.Fatalf("colorChoices 长度 = %d，期望 9", len(payload.Data.ColorChoices))
	}
	if payload.Data.NextTierSeq < 7 {
		t.Fatalf("nextTierSeq = %d，期望计入墓碑 tier_6 后 ≥ 7", payload.Data.NextTierSeq)
	}

	if written == "" {
		t.Fatal("PUT 没有写回配置")
	}
	var savedConfig map[string]any
	if err := json.Unmarshal([]byte(written), &savedConfig); err != nil {
		t.Fatalf("写回的不是合法 JSON：%v，%s", err, written)
	}
	deleted, ok := savedConfig["deletedTierIds"].([]any)
	if !ok || len(deleted) != 1 || deleted[0] != "tier_6" {
		t.Fatalf("PUT 写回的配置丢了墓碑 tier_6：%s", written)
	}
	tiers, ok := savedConfig["tiers"].([]any)
	if !ok || len(tiers) == 0 {
		t.Fatalf("PUT 写回的配置没有 tiers：%s", written)
	}
	if color, ok := tiers[0].(map[string]any)["color"]; !ok || color != "rose" {
		t.Fatalf("PUT 写回的配置丢了提交的颜色：%s", written)
	}
}
