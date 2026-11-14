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
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// POST /api/v1/admin/badges/rule-text 的 HTTP 层测试。
//
// 要证明的四件事：有权限给 200 且文案与公开面**同一实现**、没权限 403、
// 非法规则 422（与保存路径同一套码）、预览**不写任何东西**。

// ruleTextStore 只实现预览会用到的能力。
//
// 内嵌 `badges.Store` 接口（值为 nil）是为了满足 AdminService 的构造签名，
// 未覆盖的方法一旦被调用就会 panic —— 这正是「预览只读」最硬的证明：
// 任何写路径都被显式覆盖成 counter，其余能力根本不可达。
type ruleTextStore struct {
	badges.Store
	writes     int
	validated  bool
	validateFn func(ruleengine.Node) error
}

func (s *ruleTextStore) ValidateBadgeRule(_ context.Context, _ int64, rule ruleengine.Node) error {
	s.validated = true
	if s.validateFn != nil {
		return s.validateFn(rule)
	}
	return nil
}

func (s *ruleTextStore) LoadRefNames(context.Context, badges.RefSet) (conditions.RefNames, error) {
	names := conditions.NewRefNames()
	names.AddCategory("general", "综合讨论")
	return names, nil
}

func (s *ruleTextStore) CreateBadge(context.Context, badges.BadgeInput) (badges.Badge, error) {
	s.writes++
	return badges.Badge{}, nil
}

func (s *ruleTextStore) UpdateBadge(context.Context, int64, badges.BadgeInput) (badges.Badge, error) {
	s.writes++
	return badges.Badge{}, nil
}

func (s *ruleTextStore) DeleteBadge(context.Context, int64) error {
	s.writes++
	return nil
}

func actorWithBadgeView() identity.Actor {
	return identity.Actor{ID: 9, Permissions: map[string]bool{badges.PermissionBadgeView: true}}
}

func newRuleTextApp(t *testing.T, store *ruleTextStore, actor identity.Actor) *fiber.App {
	t.Helper()
	controller := &Controller{
		admin: badges.NewAdminService(store),
		gate:  fakeGate{enabled: true},
		loadActor: func(fiber.Ctx) (identity.Actor, error) {
			return actor, nil
		},
	}
	return newCatalogApp(t, controller)
}

// decodeRuleText 从统一响应信封里取出文案。
func decodeRuleText(t *testing.T, body string) requirementTextDTO {
	t.Helper()
	var envelope struct {
		Data requirementTextDTO `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, body)
	}
	return envelope.Data
}

func postRuleText(t *testing.T, app *fiber.App, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(fiber.MethodPost, "/api/v1/admin/badges/rule-text", strings.NewReader(body))
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	response, err := app.Test(request, fiber.TestConfig{Timeout: 5000})
	if err != nil {
		t.Fatalf("POST rule-text: %v", err)
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

const previewRuleBody = `{"rule":{"v":1,"root":{"type":"object.authoring",` +
	`"params":{"targetKind":"category","targetSlug":"general","window":{"kind":"all"}},` +
	`"cmp":{"op":"gte","value":3}}}}`

// 有权限：200，文案与公开面同一条渲染链路（真名 + 同一实现）。
func TestRuleTextPreviewReturnsPublicText(t *testing.T) {
	store := &ruleTextStore{}
	app := newRuleTextApp(t, store, actorWithBadgeView())

	status, body := postRuleText(t, app, previewRuleBody)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	got := decodeRuleText(t, body)
	if !strings.Contains(got.Zh, "综合讨论") || !strings.Contains(got.En, "综合讨论") {
		t.Fatalf("preview must resolve real names, got %+v", got)
	}

	// 与公开面逐字一致：把同一条规则交给公开渲染（同一份名字）必须得到同一句。
	rule, err := ruleengine.Decode([]byte(previewRuleBody[len(`{"rule":`) : len(previewRuleBody)-1]))
	if err != nil {
		t.Fatalf("decode fixture rule: %v", err)
	}
	names := conditions.NewRefNames()
	names.AddCategory("general", "综合讨论")
	want := badges.BuildRuleRequirementText(&rule, names)
	if got.Zh != want.Zh || got.En != want.En {
		t.Fatalf("preview %+v must equal public text %+v", got, want)
	}

	if !store.validated {
		t.Fatal("preview must reuse the save-path validator")
	}
	if store.writes != 0 {
		t.Fatalf("preview must not write anything, writes = %d", store.writes)
	}
}

// 没有 badge.view：403（不是 404，也不是 500）。
func TestRuleTextPreviewRequiresPermission(t *testing.T) {
	store := &ruleTextStore{}
	app := newRuleTextApp(t, store, identity.Actor{ID: 10})

	status, body := postRuleText(t, app, previewRuleBody)
	if status != fiber.StatusForbidden {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if store.writes != 0 {
		t.Fatalf("denied preview must not write, writes = %d", store.writes)
	}
}

// 非法规则：与保存路径同一套 422 错误码。
func TestRuleTextPreviewRejectsInvalidRule(t *testing.T) {
	store := &ruleTextStore{}
	app := newRuleTextApp(t, store, actorWithBadgeView())

	status, body := postRuleText(t, app, `{"rule":{"v":1}}`)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, badges.CodeBadgeRuleInvalid) {
		t.Fatalf("invalid rule must map to %s, got %s", badges.CodeBadgeRuleInvalid, body)
	}

	// 引用失效的错误来自同一条校验链（域层错误 → 422 引用码）。
	store = &ruleTextStore{validateFn: func(ruleengine.Node) error {
		return badges.ErrBadgeRuleReferenceInvalid
	}}
	app = newRuleTextApp(t, store, actorWithBadgeView())
	status, body = postRuleText(t, app, previewRuleBody)
	if status != fiber.StatusUnprocessableEntity || !strings.Contains(body, badges.CodeBadgeRuleReferenceInvalid) {
		t.Fatalf("reference error must map to 422 %s, got %d %s",
			badges.CodeBadgeRuleReferenceInvalid, status, body)
	}
}

// 空规则：手工徽章的固定句，而不是错误态（前端据此区分「还没写规则」）。
func TestRuleTextPreviewNullRuleIsManualSentence(t *testing.T) {
	store := &ruleTextStore{}
	app := newRuleTextApp(t, store, actorWithBadgeView())

	status, body := postRuleText(t, app, `{"rule":null}`)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	got := decodeRuleText(t, body)
	if got.Zh == "" || got.En == "" {
		t.Fatalf("null rule must still yield a fixed sentence, got %+v", got)
	}
}

// 请求体形状错误：422 规则码，不能落到 500。
func TestRuleTextPreviewRejectsMalformedBody(t *testing.T) {
	store := &ruleTextStore{}
	app := newRuleTextApp(t, store, actorWithBadgeView())

	status, body := postRuleText(t, app, `{"rule":`)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, badges.CodeBadgeRuleInvalid) {
		t.Fatalf("malformed body must map to %s, got %s", badges.CodeBadgeRuleInvalid, body)
	}
	// 未识别的错误必须原样透传（这条只是防回归：别把 fiber 错误吞成 nil）。
	var fiberErr *fiber.Error
	if errors.As(mapBadgeError(nil), &fiberErr) {
		t.Fatal("mapBadgeError(nil) must be nil")
	}
}
