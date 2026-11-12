package badgescontroller

import (
	"testing"

	"github.com/gofiber/fiber/v3"
)

// recordingRouter 只记录注册顺序，不真正建路由。
//
// 内嵌 fiber.Router 是为了满足接口；控制器只用到这六个方法，其余方法不会被调用。
type recordingRouter struct {
	fiber.Router
	gets  []string
	posts []string
}

func (r *recordingRouter) Get(path string, handler any, handlers ...any) fiber.Router {
	r.gets = append(r.gets, path)
	return r
}

func (r *recordingRouter) Post(path string, _ any, _ ...any) fiber.Router {
	r.posts = append(r.posts, path)
	return r
}
func (r *recordingRouter) Put(string, any, ...any) fiber.Router    { return r }
func (r *recordingRouter) Patch(string, any, ...any) fiber.Router  { return r }
func (r *recordingRouter) Delete(string, any, ...any) fiber.Router { return r }
func (r *recordingRouter) Group(string, ...any) fiber.Router       { return r }

// TestRuleRoutesAreRegisteredBeforeParamRoutes 钉住静态段先注册。
//
// `/admin/badges/conditions` 与 `/admin/badges/:badgeID` 同形（同段数、同方法族）。
// 注册顺序错了，Fiber 会把 "conditions" 当 badgeID 解析成 0，症状是 500 或
// 「invalid id」，而不是 404——很难从报错反推是路由顺序问题。顺序对了但没人钉住，
// 下次有人调路由分组就会悄悄坏。
func TestRuleRoutesAreRegisteredBeforeParamRoutes(t *testing.T) {
	recorder := &recordingRouter{}
	(&Controller{}).RegisterRoutes(recorder)

	indexOf := func(path string) int {
		for index, registered := range recorder.gets {
			if registered == path {
				return index
			}
		}
		return -1
	}

	conditions := indexOf("/conditions")
	if conditions < 0 {
		t.Fatal("/conditions 未注册")
	}
	health := indexOf("/rule-health")
	if health < 0 {
		t.Fatal("/rule-health 未注册")
	}
	impact := indexOf("/:badgeID/rule-impact")
	if impact < 0 {
		t.Fatal("/:badgeID/rule-impact 未注册")
	}
	// 两条静态段必须排在所有含参数段的 GET 之前。
	for _, paramPath := range []string{"/:badgeID/rule-impact", "/:badgeID/grants"} {
		if index := indexOf(paramPath); index >= 0 && index < conditions {
			t.Fatalf("%s 注册在 /conditions 之前（index %d < %d）", paramPath, index, conditions)
		}
		if index := indexOf(paramPath); index >= 0 && index < health {
			t.Fatalf("%s 注册在 /rule-health 之前（index %d < %d）", paramPath, index, health)
		}
	}
}

// TestBackfillStatusIsRegisteredBeforeParamRoutes 钉住回扫进度端点的注册位置。
//
// 与 /conditions、/rule-health 同款：静态段一旦排到含参数段后面，`backfill-status`
// 会被当成 badgeID 解析（报 invalid id / 500），而且报错完全指不回路由顺序。
func TestBackfillStatusIsRegisteredBeforeParamRoutes(t *testing.T) {
	recorder := &recordingRouter{}
	(&Controller{}).RegisterRoutes(recorder)

	index := func(path string) int {
		for i, registered := range recorder.gets {
			if registered == path {
				return i
			}
		}
		return -1
	}

	status := index("/backfill-status")
	if status < 0 {
		t.Fatal("/backfill-status 未注册")
	}
	if health := index("/rule-health"); health >= 0 && health > status {
		t.Fatalf("/backfill-status 应紧跟规则健康端点，实际 %d > %d", status, health)
	}
	for _, paramPath := range []string{"/:badgeID/rule-impact", "/:badgeID/grants"} {
		if paramIndex := index(paramPath); paramIndex >= 0 && paramIndex < status {
			t.Fatalf("%s 注册在 /backfill-status 之前（index %d < %d）", paramPath, paramIndex, status)
		}
	}
}

// TestRuleTextPreviewIsRegisteredBeforeParamRoutes 钉住 POST /rule-text 的注册位置。
//
// 与 GET 的 /conditions 同款风险：静态段排到含参数段之后，`rule-text` 会被当成
// badgeID 解析（报 invalid id / 500），而报错完全指不回路由顺序。
func TestRuleTextPreviewIsRegisteredBeforeParamRoutes(t *testing.T) {
	recorder := &recordingRouter{}
	(&Controller{}).RegisterRoutes(recorder)

	index := func(path string) int {
		for i, registered := range recorder.posts {
			if registered == path {
				return i
			}
		}
		return -1
	}

	preview := index("/rule-text")
	if preview < 0 {
		t.Fatal("/rule-text 未以 POST 注册")
	}
	for _, paramPath := range []string{"/:badgeID/grants", "/:badgeID/grants/batch", "/recompute"} {
		if paramIndex := index(paramPath); paramIndex >= 0 && paramIndex < preview {
			t.Fatalf("%s 注册在 /rule-text 之前（index %d < %d）", paramPath, paramIndex, preview)
		}
	}
}
