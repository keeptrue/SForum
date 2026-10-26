package aicontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// A4 的轨迹回放端点验收。
//
// 这些用例走真实的 Fiber 路由与真实的权限判定链（PAT context → ActorStore →
// service.Can → 存储层），只有执行记录存储是 fake。理由：权限与 422/200 的语义
// 是这条端点的全部价值，用 fake 控制器或 fake 权限去测等于什么都没测。

// timelineTraces 是执行记录存储的 fake。它真按 (resource_type, resource_id)
// 过滤，因此「按 run 聚合」这条断言不会被 fake 的宽松行为糊弄过去。
type timelineTraces struct {
	records []supportai.ExecutionRecord
	listErr error
	// lastLimit 记录存储层收到的 limit，用于断言上限真的被传下去了。
	lastLimit int
	// listCalls 记录 ListExecutions 被调用了几次：上限用例要证明 500 没有原样透传。
	listCalls int
}

func (f *timelineTraces) RecordExecution(context.Context, supportai.ExecutionRecord) error {
	return nil
}

func (f *timelineTraces) ListExecutions(_ context.Context, limit int) ([]supportai.ExecutionRecord, error) {
	f.listCalls++
	f.lastLimit = limit
	return f.records, nil
}

func (f *timelineTraces) ListRunExecutions(_ context.Context, resourceType, resourceID string, limit int) ([]supportai.ExecutionRecord, error) {
	f.listCalls++
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	if resourceID == "" {
		return []supportai.ExecutionRecord{}, nil
	}
	out := []supportai.ExecutionRecord{}
	for _, record := range f.records {
		if record.ResourceType == resourceType && record.ResourceID == resourceID {
			out = append(out, record)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// timelineActors 按 user id 返回 Actor：id=1 有 ai.manage，id=2 没有。
type timelineActors struct {
	actors map[int64]identity.Actor
}

func (a *timelineActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	actor, ok := a.actors[userID]
	if !ok {
		return identity.Actor{}, fmt.Errorf("no such user: %d", userID)
	}
	return actor, nil
}

func manageActor(id int64) identity.Actor {
	return identity.Actor{
		ID:          id,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAIManage: true},
	}
}

func plainMemberActor(id int64) identity.Actor {
	return identity.Actor{ID: id, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
}

// newTimelineApp 装配真实路由。身份通过 PAT context 注入，避免为测试伪造会话
// 存储——那是生产登录链路的职责，不是这条端点的。
func newTimelineApp(t *testing.T, traces *timelineTraces, actors *timelineActors) *fiber.App {
	t.Helper()
	service := modelsai.NewService(modelsai.Config{Traces: traces})
	controller := NewController(service, actors, nil, nil)
	app := fiber.New()
	api := app.Group("/api/v1")
	// 中间件模拟 PAT 认证：把 user id 放进请求 context，LoadActor 会用它。
	api.Use(func(c fiber.Ctx) error {
		userID := c.Get("X-Test-User")
		if userID == "" {
			return c.Next()
		}
		var id int64
		if _, err := fmt.Sscanf(userID, "%d", &id); err != nil {
			return c.Next()
		}
		ctx := context.WithValue(c.Context(), apitokens.ContextUserID, id)
		c.SetContext(ctx)
		return c.Next()
	})
	controller.RegisterRoutes(api)
	return app
}

// doTimeline 发一次回放请求。header 里的 user 决定身份，空串表示未认证。
func doTimeline(t *testing.T, app *fiber.App, runID string, query string, userHeader string) (int, map[string]any) {
	t.Helper()
	path := "/api/v1/admin/ai/runs/" + runID
	if query != "" {
		path += "?" + query
	}
	request := httptest.NewRequest("GET", path, nil)
	if userHeader != "" {
		request.Header.Set("X-Test-User", userHeader)
	}
	response, err := app.Test(request, fiber.TestConfig{Timeout: 5_000_000_000})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		// 403/422 的响应体形状不同，解码失败不算错误——调用方只看状态码。
		return response.StatusCode, nil
	}
	return response.StatusCode, body
}

func sampleRunRecords() []supportai.ExecutionRecord {
	return []supportai.ExecutionRecord{
		{ID: 1, Purpose: "forum.reply", Status: supportai.ExecutionStatusSucceeded, Model: "glm-5.3-flash",
			ResourceType: supportai.ResourceTypeRun, ResourceID: "run-2steps"},
		{ID: 2, Purpose: "forum.reply", Status: supportai.ExecutionStatusSucceeded, Model: "glm-5.3-flash",
			ResourceType: supportai.ResourceTypeRun, ResourceID: "run-2steps"},
		{ID: 3, Purpose: "forum.reply", Status: supportai.ExecutionStatusSucceeded,
			ResourceType: supportai.ResourceTypeRun, ResourceID: "run-other"},
	}
}

// T-A4-1：按 run 聚合。2 步 run 返回 2 条，且不夹带别的 run。
func TestRunTimelineAggregatesByRun(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{1: manageActor(1)}}
	app := newTimelineApp(t, traces, actors)

	status, body := doTimeline(t, app, "run-2steps", "", "1")
	if status != 200 {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v", body)
	}
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("data = %v", data)
	}
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	for _, raw := range items {
		record := raw.(map[string]any)
		if record["resourceId"] != "run-2steps" {
			t.Fatalf("timeline leaked another run: %v", record)
		}
	}
}

// T-A4-2：空 runId。
//
// 实测行为（Fiber v3 不做路径参数解码）：`/runs/` 不匹配路由，返回 404；
// `%20` 原样作为参数值（非空）抵达控制器。因此「空 id」有三个层次，逐层断言，
// 不做与框架行为不符的假设：
//  1. 路由层：空段 → 404（不是 500，不是「全表查询」）；
//  2. 服务层：空串 → 空列表、无错误（历史记录没有 run 归属是正常状态）；
//  3. 存储层：空串短路，查询根本不执行（由 PostgreSQL 集成用例覆盖）。
func TestRunTimelineWithEmptyRunIDReturnsEmptyList(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{1: manageActor(1)}}
	app := newTimelineApp(t, traces, actors)

	// 1) 路由层：空路径段不匹配 :runId。
	request := httptest.NewRequest("GET", "/api/v1/admin/ai/runs/", nil)
	request.Header.Set("X-Test-User", "1")
	response, err := app.Test(request, fiber.TestConfig{Timeout: 5_000_000_000})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("status = %d, want 404 for an empty path segment", response.StatusCode)
	}
	if traces.listCalls != 0 {
		t.Fatalf("an unmatched route must not reach the store (calls=%d)", traces.listCalls)
	}

	// 2) 服务层：空串 → 空列表、无错误。
	service := modelsai.NewService(modelsai.Config{Traces: traces})
	items, err := service.RunTimeline(context.Background(), manageActor(1), "", supportai.DefaultRunTimelineLimit)
	if err != nil {
		t.Fatalf("empty run id must not error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %v", items)
	}
	// 空 id 传到了存储层，但存储层的短路使它不会变成全表扫描（见 PostgreSQL 用例）。
	if traces.listCalls != 1 {
		t.Fatalf("list calls = %d", traces.listCalls)
	}
}

// T-A4-3：未知 runId 返回 200 + 空列表（不是 404、不是 500）。
func TestRunTimelineForUnknownRunReturnsEmptyList(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{1: manageActor(1)}}
	app := newTimelineApp(t, traces, actors)

	status, body := doTimeline(t, app, "run-missing", "", "1")
	if status != 200 {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	data := body["data"].(map[string]any)
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items must be an array, not null: %v", data["items"])
	}
	if len(items) != 0 {
		t.Fatalf("items = %v", items)
	}
}

// T-A4-4：权限。没有 ai.manage 的用户必须拿到 403，且根本没有触达存储。
func TestRunTimelineRequiresManagePermission(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{
		1: manageActor(1),
		2: plainMemberActor(2),
	}}
	app := newTimelineApp(t, traces, actors)

	status, _ := doTimeline(t, app, "run-2steps", "", "2")
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	if traces.listCalls != 0 {
		t.Fatalf("an unauthorised caller must not reach the store (calls=%d)", traces.listCalls)
	}

	// 未认证（没有身份）必须 401，同样不触达存储。
	status, _ = doTimeline(t, app, "run-2steps", "", "")
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	if traces.listCalls != 0 {
		t.Fatalf("an anonymous caller must not reach the store (calls=%d)", traces.listCalls)
	}
}

// T-A4-5：上限。请求 500 条时实际只取 200 条。
func TestRunTimelineCapsLimitAtMaximum(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{1: manageActor(1)}}
	app := newTimelineApp(t, traces, actors)

	status, _ := doTimeline(t, app, "run-2steps", "limit=500", "1")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if traces.lastLimit != supportai.MaxRunTimelineLimit {
		t.Fatalf("limit = %d, want %d", traces.lastLimit, supportai.MaxRunTimelineLimit)
	}

	// 默认 50。
	traces.lastLimit = 0
	if status, _ := doTimeline(t, app, "run-2steps", "", "1"); status != 200 {
		t.Fatalf("status = %d", status)
	}
	if traces.lastLimit != supportai.DefaultRunTimelineLimit {
		t.Fatalf("default limit = %d, want %d", traces.lastLimit, supportai.DefaultRunTimelineLimit)
	}

	// 0 与负数按下限处理（取默认值），不会被当成「不限」。
	traces.lastLimit = 0
	if status, _ := doTimeline(t, app, "run-2steps", "limit=0", "1"); status != 200 {
		t.Fatalf("status = %d", status)
	}
	if traces.lastLimit != supportai.DefaultRunTimelineLimit {
		t.Fatalf("limit=0 became %d, want %d", traces.lastLimit, supportai.DefaultRunTimelineLimit)
	}
}

// runId 超过 128 字符必须 422，并且不触达存储——它是路径参数，不能变成一次
// 「用一个荒谬长度去查库」的入口。
func TestRunTimelineRejectsOversizedRunID(t *testing.T) {
	traces := &timelineTraces{records: sampleRunRecords()}
	actors := &timelineActors{actors: map[int64]identity.Actor{1: manageActor(1)}}
	app := newTimelineApp(t, traces, actors)

	oversized := ""
	for i := 0; i < MaxRunIDLen+1; i++ {
		oversized += "a"
	}
	status, _ := doTimeline(t, app, oversized, "", "1")
	if status != 422 {
		t.Fatalf("status = %d, want 422", status)
	}
	if traces.listCalls != 0 {
		t.Fatalf("an oversized run id must not reach the store (calls=%d)", traces.listCalls)
	}
}
