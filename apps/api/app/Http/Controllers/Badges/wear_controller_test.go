package badgescontroller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 自由佩戴（GET/PUT/DELETE /api/v1/badges/worn）的 HTTP 层测试。
//
// 领域测试（wear_postgres_integration_test.go）证明「谁有资格戴、失效怎么回落」；
// 这里只证明前端与缓存真正看到的那一层：三条端点共用同一个响应形状、未登录 401、
// 未持有 422 + forum.badge_not_owned、佩戴/取消佩戴后的 mode、以及 private 缓存头。
//
// 用窄假实现而不是真 Service + 真库：这三条 handler 的职责就是「取 actor → 调域 →
// 编码」，把它接到 Postgres 上测的是领域层已经覆盖过的东西，反而让 401/422/缓存头
// 这些传输契约被 SQL 噪音淹没。

const wornActorID int64 = 42

// fakeWearer 只实现佩戴的三条操作，按领域语义改自己的展示态。
//
// 改态这一步是刻意的：handler 在 PUT/DELETE 成功后会**再读一次** DisplayBadge，
// 一个永远返回固定值的假实现会让「响应形状对但状态没变」这类 bug 全绿。
// 写入的引用带完整等级外观：真实领域层返回的 BadgeRef 一定带 tier 指向的外观，
// 外观全空的假实现会让「投影了 tierName/tierColor」与「漏投影」在响应里难以区分。
type fakeWearer struct {
	display    badges.BadgeDisplay
	displayErr error
	setErr     error
	clearErr   error
	hideErr    error

	setUserID     int64
	setSlug       string
	clearUserID   int64
	hideUserID    int64
	displayUserID int64
	setCalls      int
	clearCalls    int
	hideCalls     int
}

func (f *fakeWearer) SetWornBadge(_ context.Context, userID int64, slug string) error {
	f.setCalls++
	f.setUserID, f.setSlug = userID, slug
	if f.setErr != nil {
		return f.setErr
	}
	f.display.Mode = badges.BadgeDisplayModeManual
	f.display.Worn = &badges.BadgeRef{
		Slug:       slug,
		Name:       "佩戴中",
		Tier:       badges.TierSilver,
		Appearance: silverAppearance(),
		Icon:       "i-lucide-medal",
	}
	return nil
}

func (f *fakeWearer) ClearWornBadge(_ context.Context, userID int64) error {
	f.clearCalls++
	f.clearUserID = userID
	if f.clearErr != nil {
		return f.clearErr
	}
	f.display.Mode = badges.BadgeDisplayModeAuto
	f.display.Worn = f.display.Automatic
	f.display.Upgrade = nil
	return nil
}

func (f *fakeWearer) HideWornBadge(_ context.Context, userID int64) error {
	f.hideCalls++
	f.hideUserID = userID
	if f.hideErr != nil {
		return f.hideErr
	}
	f.display.Mode = badges.BadgeDisplayModeHidden
	f.display.Worn = nil
	f.display.Upgrade = nil
	return nil
}

func (f *fakeWearer) DisplayBadge(_ context.Context, userID int64) (badges.BadgeDisplay, error) {
	f.displayUserID = userID
	if f.displayErr != nil {
		return badges.BadgeDisplay{}, f.displayErr
	}
	return f.display, nil
}

// 等级外观夹具：形状锁测试要证明等级外观被投影成 `tierName{zh,en}` + `tierColor`，
// 所以夹具必须带非空外观。外观全空时键名仍然正确、值全是空串，「投影了」与
// 「漏投影」的差别就只剩键名，锁不住真正的回归（例如把 color 写成了 icon）。
func goldAppearance() badges.TierAppearance {
	return badges.TierAppearance{NameZh: "金牌", NameEn: "Gold", Color: "amber", Position: 0}
}

func silverAppearance() badges.TierAppearance {
	return badges.TierAppearance{NameZh: "银牌", NameEn: "Silver", Color: "slate", Position: 1}
}

// autoBadgeDisplay 是「没戴过」的默认态：worn 就是自动推导的最佳徽章，没有 upgrade。
func autoBadgeDisplay() badges.BadgeDisplay {
	best := &badges.BadgeRef{
		Slug:       "gold-badge",
		Name:       "金牌徽章",
		Tier:       badges.TierGold,
		Appearance: goldAppearance(),
		Icon:       "i-lucide-crown",
	}
	return badges.BadgeDisplay{Mode: badges.BadgeDisplayModeAuto, Worn: best, Automatic: best}
}

// manualBadgeDisplay 是「戴了一枚更差的」：存在更优徽章，upgrade 非空。
func manualBadgeDisplay() badges.BadgeDisplay {
	best := &badges.BadgeRef{
		Slug:       "gold-badge",
		Name:       "金牌徽章",
		Tier:       badges.TierGold,
		Appearance: goldAppearance(),
		Icon:       "i-lucide-crown",
	}
	picked := &badges.BadgeRef{
		Slug:       "silver-badge",
		Name:       "银牌徽章",
		Tier:       badges.TierSilver,
		Appearance: silverAppearance(),
		Icon:       "i-lucide-medal",
	}
	return badges.BadgeDisplay{
		Mode:      badges.BadgeDisplayModeManual,
		Worn:      picked,
		Automatic: best,
		Upgrade:   best,
	}
}

// newWornApp 建一个只挂徽章路由的 app，错误响应与生产同形（data.reason 是稳定码）。
func newWornApp(t *testing.T, controller *Controller) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			return c.Status(fiberErr.Code).JSON(fiber.Map{
				"code":    fiberErr.Code,
				"message": fiberErr.Message,
				"data":    fiber.Map{"reason": fiberErr.Message},
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    fiber.StatusInternalServerError,
			"message": err.Error(),
			"data":    fiber.Map{"reason": "internal_error"},
		})
	}})
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)
	return app
}

type wornHTTPResult struct {
	status       int
	cacheControl string
	code         int
	message      string
	data         map[string]any
	// raw 是**未经解码**的响应字节（含信封）。形状锁测试必须在字符串上断言
	// 「JSON 里不出现 tierAppearance / NameZh」：解码成 map 再拼回去会抹掉
	// 「字段名长什么样」这类证据，而那正是要防的回归。
	raw string
}

func (r wornHTTPResult) reason() string {
	reason, _ := r.data["reason"].(string)
	return reason
}

func callWorn(t *testing.T, app *fiber.App, method, body string) wornHTTPResult {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1/badges/worn", strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := app.Test(request, fiber.TestConfig{Timeout: 5000})
	if err != nil {
		t.Fatalf("%s /api/v1/badges/worn: %v", method, err)
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
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload.String()), &envelope); err != nil {
		t.Fatalf("%s response is not JSON: %v (%s)", method, err, payload.String())
	}
	result := wornHTTPResult{
		status:       response.StatusCode,
		cacheControl: response.Header.Get(fiber.HeaderCacheControl),
		code:         envelope.Code,
		message:      envelope.Message,
		raw:          payload.String(),
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &result.data); err != nil {
			t.Fatalf("%s data is not a JSON object: %v (%s)", method, err, envelope.Data)
		}
	}
	return result
}

// newWornController 组装一个已登录（actor=42）的控制器。
func newWornController(wearer badges.WearStore) *Controller {
	return (&Controller{wear: wearer}).WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
		return identity.Actor{ID: wornActorID, Status: identity.UserStatusActive}, nil
	})
}

// TestWornBadgeEndpointsRequireAuth 断言三条端点都先要登录，且 401 早于 body 解析。
//
// 顺序本身是契约：未登录用户带一个坏 body 打 PUT，得到的必须是 401（而不是 400）——
// 否则匿名请求能靠错误码区分「服务端解析到哪一步」，也把「先认证再解码」这条
// 通用约定悄悄反过来了。
func TestWornBadgeEndpointsRequireAuth(t *testing.T) {
	cases := []struct {
		method string
		body   string
	}{
		{fiber.MethodGet, ""},
		{fiber.MethodPut, `{"slug":"silver-badge"}`},
		{fiber.MethodPut, `{"slug":`}, // 坏 body：仍然必须先 401
		{fiber.MethodDelete, ""},
	}
	for _, tc := range cases {
		wearer := &fakeWearer{display: autoBadgeDisplay()}
		controller := (&Controller{wear: wearer}).WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{}, nil
		})
		result := callWorn(t, newWornApp(t, controller), tc.method, tc.body)
		if result.status != fiber.StatusUnauthorized {
			t.Fatalf("%s (body %q): want 401, got %d (%s)", tc.method, tc.body, result.status, result.reason())
		}
		if result.reason() != "auth.required" {
			t.Fatalf("%s: reason = %q, want auth.required", tc.method, result.reason())
		}
		if wearer.setCalls != 0 || wearer.clearCalls != 0 || wearer.displayUserID != 0 {
			t.Fatalf("%s: 未登录请求不得触达领域层（set=%d clear=%d display=%d）",
				tc.method, wearer.setCalls, wearer.clearCalls, wearer.displayUserID)
		}
	}
}

// TestWornBadgePutRejectsUnownedBadge 断言「没持有」是 422 + forum.badge_not_owned。
//
// 不能是 404：404 等于告诉对手方「这枚徽章在站内不存在」，而这一码刻意把
// 「没获得」与「不存在」折成同一个回答。
func TestWornBadgePutRejectsUnownedBadge(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay(), setErr: badges.ErrBadgeNotOwned}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"slug":"silver-badge"}`)

	if result.status != fiber.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (%s)", result.status, result.reason())
	}
	if result.reason() != badges.CodeBadgeNotOwned {
		t.Fatalf("reason = %q, want %q", result.reason(), badges.CodeBadgeNotOwned)
	}
	if badges.CodeBadgeNotOwned != "forum.badge_not_owned" {
		t.Fatalf("wire code changed: %q", badges.CodeBadgeNotOwned)
	}
	if wearer.setUserID != wornActorID || wearer.setSlug != "silver-badge" {
		t.Fatalf("领域层收到 (%d, %q)，want (%d, silver-badge)", wearer.setUserID, wearer.setSlug, wornActorID)
	}
}

// TestWornBadgePutReturnsManualDisplayWithPrivateCache 断言佩戴成功后的 wire 形状与缓存头。
//
// 缓存头是这条端点最容易被顺手改成 public 的地方：响应含「我戴了哪枚」，
// 一旦进共享缓存，A 的佩戴态会被投给 B（本控制器没有 Vary 兜底）。
func TestWornBadgePutReturnsManualDisplayWithPrivateCache(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"slug":"silver-badge"}`)

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	if result.data["mode"] != badges.BadgeDisplayModeManual {
		t.Fatalf("mode = %v, want manual", result.data["mode"])
	}
	worn, ok := result.data["worn"].(map[string]any)
	if !ok || worn["slug"] != "silver-badge" {
		t.Fatalf("worn = %v, want slug silver-badge", result.data["worn"])
	}
	// 引用本身走显式投影：等级外观是 tierName{zh,en} + tierColor，与目录/资料页同一套键。
	assertKeys(t, "worn ref", worn, []string{"slug", "name", "tier", "tierName", "tierColor", "icon"})
	if tierName, ok := worn["tierName"].(map[string]any); !ok || tierName["en"] != "Silver" {
		t.Fatalf("worn.tierName = %v, want {zh:银牌, en:Silver}", worn["tierName"])
	}
	if !strings.Contains(result.cacheControl, "private") {
		t.Fatalf("Cache-Control = %q, want private", result.cacheControl)
	}
	if strings.Contains(result.cacheControl, "public") {
		t.Fatalf("Cache-Control = %q must not be public", result.cacheControl)
	}
	if wearer.setUserID != wornActorID {
		t.Fatalf("set userID = %d, want %d", wearer.setUserID, wornActorID)
	}
}

// TestWornBadgePutResponseUsesFlattenedTierShape 是形状锁：佩戴成功后的等级外观必须是
// 站点统一的 `tier` + `tierName{zh,en}` + `tierColor`，而不是领域结构原样序列化出来的
// `tierAppearance{NameZh,NameEn,Color,Position}`。
//
// 断言打在**响应字节**上（result.raw），不是解码后的 map：
//   - 出现 `tierAppearance` / `NameZh` 说明有人把 handler 改回 `apphttp.OK(c, display)`，
//     前端会拿到一套它不认识的键名（同一份数据两种形状 = 两套解析）；
//   - `tierName` 必须恰好是 {zh,en} 两个键，多一个少一个都说明投影被绕过。
//
// 为什么锁在这三条端点而不是只锁 dto.go：这里的回归方式是「handler 直接序列化领域结构」，
// 它不经过任何 DTO 单测，只有打真实响应的测试能看见。
func TestWornBadgePutResponseUsesFlattenedTierShape(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"slug":"silver-badge"}`)

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	for _, forbidden := range []string{"tierAppearance", "NameZh", "NameEn"} {
		if strings.Contains(result.raw, forbidden) {
			t.Fatalf("响应里出现领域结构键 %q（handler 又直接序列化了领域结构）：%s", forbidden, result.raw)
		}
	}
	worn, ok := result.data["worn"].(map[string]any)
	if !ok {
		t.Fatalf("worn = %v, want object", result.data["worn"])
	}
	assertKeys(t, "worn ref", worn, []string{"slug", "name", "tier", "tierName", "tierColor", "icon"})
	tierName, ok := worn["tierName"].(map[string]any)
	if !ok {
		t.Fatalf("worn.tierName = %v, want {zh,en}", worn["tierName"])
	}
	assertKeys(t, "worn.tierName", tierName, []string{"zh", "en"})
	if tierName["zh"] != "银牌" || tierName["en"] != "Silver" {
		t.Fatalf("worn.tierName = %v, want {zh:银牌, en:Silver}", tierName)
	}
	if worn["tierColor"] != "slate" {
		t.Fatalf("worn.tierColor = %v, want slate", worn["tierColor"])
	}
	if worn["tier"] != string(badges.TierSilver) {
		t.Fatalf("worn.tier = %v, want silver", worn["tier"])
	}
	t.Logf("PUT /api/v1/badges/worn -> %s", result.raw)
}

// TestWornBadgeDeleteReturnsAutomaticDisplay 断言取消佩戴回到 auto，且 upgrade 清空。
func TestWornBadgeDeleteReturnsAutomaticDisplay(t *testing.T) {
	wearer := &fakeWearer{display: manualBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodDelete, "")

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	if result.data["mode"] != badges.BadgeDisplayModeAuto {
		t.Fatalf("mode = %v, want auto", result.data["mode"])
	}
	worn, ok := result.data["worn"].(map[string]any)
	if !ok || worn["slug"] != "gold-badge" {
		t.Fatalf("worn = %v, want 自动推导的 gold-badge", result.data["worn"])
	}
	if tierName, ok := worn["tierName"].(map[string]any); !ok || tierName["zh"] != "金牌" {
		t.Fatalf("worn.tierName = %v, want {zh:金牌, en:Gold}", worn["tierName"])
	}
	if value, present := result.data["upgrade"]; !present || value != nil {
		t.Fatalf("auto 模式下 upgrade 必须是 null，got %v (present=%v)", value, present)
	}
	if wearer.clearUserID != wornActorID {
		t.Fatalf("clear userID = %d, want %d", wearer.clearUserID, wornActorID)
	}
	if !strings.Contains(result.cacheControl, "private") {
		t.Fatalf("Cache-Control = %q, want private", result.cacheControl)
	}
}

// TestWornBadgeGetReturnsFourFieldDisplay 断言 GET 的键集与 upgrade 内容。
//
// 键集用 assertKeys 精确比对：多一个字段前端不会崩，少一个（例如 upgrade）会让
// 「可升级」提示永远不出现，而两边都不会报错。
func TestWornBadgeGetReturnsFourFieldDisplay(t *testing.T) {
	wearer := &fakeWearer{display: manualBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodGet, "")

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	assertKeys(t, "badge display", result.data, []string{"mode", "worn", "automatic", "upgrade"})
	if result.data["mode"] != badges.BadgeDisplayModeManual {
		t.Fatalf("mode = %v, want manual", result.data["mode"])
	}
	upgrade, ok := result.data["upgrade"].(map[string]any)
	if !ok {
		t.Fatalf("upgrade = %v, want object", result.data["upgrade"])
	}
	if upgrade["slug"] != "gold-badge" || upgrade["name"] != "金牌徽章" {
		t.Fatalf("upgrade 必须带 slug+name（前端直接渲染「换成 X」），got %v", upgrade)
	}
	assertKeys(t, "upgrade ref", upgrade, []string{"slug", "name", "tier", "tierName", "tierColor", "icon"})
	if upgrade["tier"] != string(badges.TierGold) || upgrade["tierColor"] != "amber" {
		t.Fatalf("upgrade = %v, want tier gold + tierColor amber", upgrade)
	}
	if wearer.displayUserID != wornActorID {
		t.Fatalf("display userID = %d, want %d", wearer.displayUserID, wornActorID)
	}
}

// TestWornBadgeGetKeepsNullFieldsForEmptyWall 断言没有徽章时三个字段都是 null 而不是被省略。
func TestWornBadgeGetKeepsNullFieldsForEmptyWall(t *testing.T) {
	wearer := &fakeWearer{display: badges.BadgeDisplay{Mode: badges.BadgeDisplayModeAuto}}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodGet, "")

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	assertKeys(t, "empty badge display", result.data, []string{"mode", "worn", "automatic", "upgrade"})
	for _, key := range []string{"worn", "automatic", "upgrade"} {
		value, present := result.data[key]
		if !present {
			t.Fatalf("%s 必须存在（值 null），否则前端要处理「键缺失」这第三种状态", key)
		}
		if value != nil {
			t.Fatalf("%s = %v, want null", key, value)
		}
		// 解码后的 nil 只能证明「值是空」，键是否真的被输出要在原始字节上锁：
		// 这三个键必须字面出现在响应里（`"worn":null`），被 omitempty 吃掉即红。
		if !strings.Contains(result.raw, `"`+key+`":null`) {
			t.Fatalf("%s 必须以 null 字面出现在响应里，got %s", key, result.raw)
		}
	}
	t.Logf("GET /api/v1/badges/worn (empty wall) -> %s", result.raw)
}

// TestWornBadgePutRejectsMalformedBody 断言解析失败是 400 + 专属码，且不触达领域层。
func TestWornBadgePutRejectsMalformedBody(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"slug":`)

	if result.status != fiber.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", result.status, result.reason())
	}
	if result.reason() != badges.CodeBadgeWornBodyInvalid {
		t.Fatalf("reason = %q, want %q", result.reason(), badges.CodeBadgeWornBodyInvalid)
	}
	if wearer.setCalls != 0 {
		t.Fatalf("body 解析失败不得调用领域层（setCalls=%d）", wearer.setCalls)
	}
}

// TestWornBadgeEmptySlugIsNotOwned 断言空 slug 走同一个 422 码（由领域层归一化判定）。
func TestWornBadgeEmptySlugIsNotOwned(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay(), setErr: badges.ErrBadgeNotOwned}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{}`)

	if result.status != fiber.StatusUnprocessableEntity || result.reason() != badges.CodeBadgeNotOwned {
		t.Fatalf("空 slug: want 422 %s, got %d %q", badges.CodeBadgeNotOwned, result.status, result.reason())
	}
	if wearer.setSlug != "" {
		t.Fatalf("空 body 的 slug 应原样下传（由领域层判定），got %q", wearer.setSlug)
	}
}

// TestWornBadgeReadFailureIsServerError 断言读库失败上抛 500，不伪装成空展示态。
//
// 「读失败 → 回 auto 空态」会让资料页显示「你没戴徽章」，而用户其实戴着——
// 静默撒谎比 500 更难排查。
func TestWornBadgeReadFailureIsServerError(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay(), displayErr: errors.New("db down")}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodGet, "")

	if result.status != fiber.StatusInternalServerError {
		t.Fatalf("want 500, got %d (%s)", result.status, result.reason())
	}
}

// TestWornBadgeEndpointsHonorFeatureGate 断言产品开关关闭时三条都 403 badges_disabled。
func TestWornBadgeEndpointsHonorFeatureGate(t *testing.T) {
	for _, method := range []string{fiber.MethodGet, fiber.MethodPut, fiber.MethodDelete} {
		controller := (&Controller{wear: &fakeWearer{display: autoBadgeDisplay()}, gate: fakeGate{enabled: false}}).
			WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
				return identity.Actor{ID: wornActorID, Status: identity.UserStatusActive}, nil
			})
		result := callWorn(t, newWornApp(t, controller), method, `{"slug":"silver-badge"}`)
		if result.status != fiber.StatusForbidden || result.reason() != badges.CodeBadgesDisabled {
			t.Fatalf("%s: want 403 %s, got %d %q", method, badges.CodeBadgesDisabled, result.status, result.reason())
		}
	}
}

// TestWornBadgePutHiddenClearsSlot 断言 `{"hidden":true}` 走隐藏路径：领域层收到
// HideWornBadge（而不是 SetWornBadge/ClearWornBadge），响应是 mode=hidden + worn=null。
//
// worn 必须以 null 字面出现：前端按 mode 分支渲染展示位，「键缺失」与「位子是空的」
// 是两种状态，前者会被当成加载失败。
func TestWornBadgePutHiddenClearsSlot(t *testing.T) {
	wearer := &fakeWearer{display: manualBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"hidden":true}`)

	if result.status != fiber.StatusOK {
		t.Fatalf("want 200, got %d (%s)", result.status, result.reason())
	}
	if result.data["mode"] != badges.BadgeDisplayModeHidden {
		t.Fatalf("mode = %v, want hidden", result.data["mode"])
	}
	for _, key := range []string{"worn", "upgrade"} {
		value, present := result.data[key]
		if !present || value != nil {
			t.Fatalf("%s = %v (present=%v), want null", key, value, present)
		}
		if !strings.Contains(result.raw, `"`+key+`":null`) {
			t.Fatalf("%s 必须以 null 字面出现在响应里，got %s", key, result.raw)
		}
	}
	// automatic 照常下发：前端可以据此显示「你的最佳徽章是 X」。
	if automatic, ok := result.data["automatic"].(map[string]any); !ok || automatic["slug"] != "gold-badge" {
		t.Fatalf("automatic = %v, want gold-badge", result.data["automatic"])
	}
	if wearer.hideCalls != 1 || wearer.hideUserID != wornActorID {
		t.Fatalf("hide = (calls=%d, userID=%d), want (1, %d)", wearer.hideCalls, wearer.hideUserID, wornActorID)
	}
	if wearer.setCalls != 0 || wearer.clearCalls != 0 {
		t.Fatalf("hidden 请求不得走 set/clear 路径（set=%d clear=%d）", wearer.setCalls, wearer.clearCalls)
	}
	if !strings.Contains(result.cacheControl, "private") {
		t.Fatalf("Cache-Control = %q, want private", result.cacheControl)
	}
}

// TestWornBadgePutHiddenAndSlugConflictIs400 断言 hidden 与 slug 同填是 400：
// 两个动作写同一展示位的不同状态，同填意味着调用方自己也不知道要哪个，执行哪个
// 都是错的——静默挑一个会改掉用户没同意的状态。400（而非 422）与「body 解析成功
// 但语义非法」同一码 forum.badge_worn_body_invalid：服务端连用户意图都没读到。
func TestWornBadgePutHiddenAndSlugConflictIs400(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay()}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"slug":"silver-badge","hidden":true}`)

	if result.status != fiber.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", result.status, result.reason())
	}
	if result.reason() != badges.CodeBadgeWornBodyInvalid {
		t.Fatalf("reason = %q, want %q", result.reason(), badges.CodeBadgeWornBodyInvalid)
	}
	if wearer.setCalls != 0 || wearer.clearCalls != 0 || wearer.hideCalls != 0 {
		t.Fatalf("互斥冲突不得触达领域层（set=%d clear=%d hide=%d）", wearer.setCalls, wearer.clearCalls, wearer.hideCalls)
	}
}

// TestWornBadgeHiddenFalseFallsBackToSlug 断言 `{"hidden":false}` 与缺字段等价：
// 走 slug 路径，空 slug 由领域层折成 ErrBadgeNotOwned（不新增第三种「空请求」判定，
// 两处判断迟早分叉）。
func TestWornBadgeHiddenFalseFallsBackToSlug(t *testing.T) {
	wearer := &fakeWearer{display: autoBadgeDisplay(), setErr: badges.ErrBadgeNotOwned}
	result := callWorn(t, newWornApp(t, newWornController(wearer)), fiber.MethodPut, `{"hidden":false}`)

	if result.status != fiber.StatusUnprocessableEntity || result.reason() != badges.CodeBadgeNotOwned {
		t.Fatalf("hidden=false: want 422 %s, got %d %q", badges.CodeBadgeNotOwned, result.status, result.reason())
	}
	if wearer.setCalls != 1 || wearer.hideCalls != 0 {
		t.Fatalf("hidden=false 必须走 slug 路径（set=%d hide=%d）", wearer.setCalls, wearer.hideCalls)
	}
}
