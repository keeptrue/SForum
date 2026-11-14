package trustlevelscontroller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件钉住 M1 的传输面：两个新端点、公开/后台两套 wire 形状的**值级**隔离、
// 以及 6 个新错误码的 422 精确性。

// ruleDoc 与领域测试里的同名 helper 同形（`{"v":1,"root":…}`）。
func ruleDoc(root string) string { return `{"v":1,"root":` + root + `}` }

const verifiedLeaf = `{"type":"identity.email_verified"}`

const topicCountLeaf = `{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}`

// configWithRuleJSON 造一份「TL1 带规则」的 trust.levels 值。
func configWithRuleJSON(t *testing.T, rule string) string {
	t.Helper()
	tier := `{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]`
	if rule != "" {
		tier += `,"rule":` + rule
	}
	tier += `}`
	return `{"schemaVersion":2,"tiers":[` + tier + `]}`
}

// policyWithRule 返回一份带规则树的策略（公开面隔离测试的输入）。
func policyWithRule(t *testing.T) trustlevels.Policy {
	t.Helper()
	policy, err := trustlevels.DecodeConfig(configWithRuleJSON(t, ruleDoc(topicCountLeaf)))
	if err != nil {
		t.Fatalf("decode policy with rule: %v", err)
	}
	return policy
}

// groupRule 造一棵带 `children` 的树：后台读面的断言必须覆盖组节点，
// 否则「只下发叶子」的退化也会绿。
func groupRule() string {
	return ruleDoc(`{"op":"all","children":[` + topicCountLeaf + `,` + verifiedLeaf + `]}`)
}

// policyWithGroupRule 返回一份带组规则树的策略。
func policyWithGroupRule(t *testing.T) trustlevels.Policy {
	t.Helper()
	policy, err := trustlevels.DecodeConfig(configWithRuleJSON(t, groupRule()))
	if err != nil {
		t.Fatalf("decode policy with group rule: %v", err)
	}
	return policy
}

func enabledGate(context.Context) (bool, error) { return true, nil }

// fakeRefSource 提供固定的候选集合（引用校验的 allowed/denied 两向都用它）。
type fakeRefSource struct{ set conditions.RefCandidateSet }

func (f fakeRefSource) RefCandidates(context.Context) (conditions.RefCandidateSet, error) {
	return f.set, nil
}

// fakeEstimator 返回固定估算结果。
type fakeEstimator struct{ result trustlevels.RuleImpact }

func (f fakeEstimator) EstimateRuleImpact(context.Context, ruleengine.Node) (trustlevels.RuleImpact, error) {
	return f.result, nil
}

// TestPublicPayloadNeverCarriesRule 是**值级**断言（不是键存在性）。
//
// 徽章那刀的教训：只断言「字段不存在」时，一旦有人在共用结构上去掉 omitempty 就会
// 静默通过。这里序列化整份公开响应，逐字节搜 AST 词汇——包括 `children`/`cmp`/
// `params`/`type`，因为 `rule` 漏出去的形态不止一种。
func TestPublicPayloadNeverCarriesRule(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policyWithRule(t), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("公开读 status = %d，body=%s", status, body)
	}
	for _, forbidden := range []string{`"rule"`, `"children"`, `"cmp"`, `"params"`, `"type"`, `"limits"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("公开响应里出现了 %s：\n%s", forbidden, body)
		}
	}
	// 对照组：档位本身在（否则「整个响应空了」也会让上面的断言全绿）。
	if !strings.Contains(body, `"levelSlug":"member"`) {
		t.Fatalf("公开目录缺少 member 档：\n%s", body)
	}
}

// TestAdminPayloadCarriesRule 是公开面隔离的另一半：**必须**下发 rule。
//
// 只测「公开面没有 rule」的测试，把后台的 rule 也弄丢同样会绿——那是把功能删了
// 而不是把泄漏堵了。
func TestAdminPayloadCarriesRule(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policyWithGroupRule(t), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("后台读 status = %d，body=%s", status, body)
	}
	if !strings.Contains(body, `"rule"`) || !strings.Contains(body, `"children"`) {
		t.Fatalf("后台读面必须下发规则树：\n%s", body)
	}
	// 值级复核：rule 是对象而不是被字符串化的 JSON。
	var payload struct {
		Data struct {
			Levels []struct {
				Rule   json.RawMessage `json:"rule"`
				Limits *struct{}       `json:"limits"`
			} `json:"levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("后台响应不是合法 JSON：%v", err)
	}
	found := false
	for _, level := range payload.Data.Levels {
		if len(level.Rule) > 0 {
			found = true
			if !strings.Contains(string(level.Rule), `"root"`) {
				t.Fatalf("rule 不是规则文档：%s", string(level.Rule))
			}
			if level.Limits == nil {
				t.Fatalf("带 limits 的后台档位丢了 limits（拆形状时漏字段）")
			}
		}
	}
	if !found {
		t.Fatal("后台档位里没有任何 rule 键")
	}
}

// TestPutRuleRejectionsReturn422WithExactCode 断言 5 条规则码的 HTTP 精确性。
//
// 传输层的机器码仍是 `trust_levels.invalid_config`（D-N14 不按档位变码），**精确码在
// details.errors[].reasonCode**。所以断言必须一路查到 reasonCode，否则「桶码」也算过。
func TestPutRuleRejectionsReturn422WithExactCode(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { return nil }))

	deepRoot := verifiedLeaf
	for i := 0; i < 5; i++ {
		deepRoot = `{"op":"all","children":[` + deepRoot + `]}`
	}
	cases := []struct {
		name string
		rule string
		code string
	}{
		{"rule_too_complex", ruleDoc(deepRoot), "rule_too_complex"},
		{"rule_unknown_condition", ruleDoc(`{"type":"activity.nope"}`), "rule_unknown_condition"},
		{"rule_shape_invalid", ruleDoc(`{"op":"all","children":[]}`), "rule_shape_invalid"},
		{"rule_condition_not_available", ruleDoc(`{"type":"content.hidden","cmp":{"op":"gte","value":1}}`), "rule_condition_not_available"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := `{"config":` + mustJSONString(configWithRuleJSON(t, testCase.rule)) + `}`
			status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", body)
			if status != fiber.StatusUnprocessableEntity {
				t.Fatalf("status = %d（期望 422），body=%s", status, response)
			}
			if !strings.Contains(response, `"reasonCode":"`+testCase.code+`"`) {
				t.Fatalf("响应里没有精确码 %q：\n%s", testCase.code, response)
			}
			if !strings.Contains(response, `"field":"rule"`) {
				t.Fatalf("明细没有定位到 rule 字段：\n%s", response)
			}
		})
	}
}

// TestPutReferenceInvalidReturns422 断言引用失效走 422 + rule_reference_invalid。
func TestPutReferenceInvalidReturns422(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	source := fakeRefSource{set: conditions.NormalizeRefCandidateSet([]conditions.RefCandidateGroup{
		conditions.NewRefCandidateGroup(conditions.RefKindCategory, []conditions.RefCandidate{{Value: "general", Label: "综合"}}),
	})}
	written := 0
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithRuleEditingDeps(source, nil, nil).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { written++; return nil }))

	authoring := func(slug string) string {
		return ruleDoc(`{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"` + slug + `","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}`)
	}
	body := `{"config":` + mustJSONString(configWithRuleJSON(t, authoring("gone"))) + `}`
	status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", body)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422），body=%s", status, response)
	}
	if !strings.Contains(response, `"reasonCode":"rule_reference_invalid"`) {
		t.Fatalf("缺少 rule_reference_invalid：\n%s", response)
	}
	if written != 0 {
		t.Fatalf("被拒的配置仍写库了 %d 次", written)
	}

	// 对照组：同一形状、引用仍存在 → 200（否则「一律拒绝」也会绿）。
	okBody := `{"config":` + mustJSONString(configWithRuleJSON(t, authoring("general"))) + `}`
	if status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", okBody); status != fiber.StatusOK {
		t.Fatalf("引用仍存在却 status = %d，body=%s", status, response)
	}
	if written != 1 {
		t.Fatalf("合法配置应写库一次，实际 %d 次", written)
	}
}

// TestConditionsEndpoint 断言目录端点：11 条条件 + 五组候选 + 权限。
//
// 11 而不是计划里的 12：`identity.profile` 还没进共享目录（另单）。这里断言的是
// 「子集 = 共享 − 内容质量两条」这条规则，不是那个数字。
func TestConditionsEndpoint(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	controller := NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithRuleEditingDeps(fakeRefSource{set: conditions.UnavailableRefCandidateSet()}, nil, nil)
	app := newTestApp(t, controller)

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels/conditions", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d，body=%s", status, body)
	}
	var envelope struct {
		Data trustLevelConditionCatalog `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	payload := envelope.Data
	shared := conditions.Core().Definitions()
	if len(payload.Conditions) != len(shared)-2 {
		t.Fatalf("条件数 = %d（期望共享 %d − 2）", len(payload.Conditions), len(shared))
	}
	for _, condition := range payload.Conditions {
		if condition.Key == "content.pinned" || condition.Key == "content.hidden" {
			t.Fatalf("子集外条件被下发：%s", condition.Key)
		}
		if condition.LabelZh == "" || condition.ValueKind == "" {
			t.Fatalf("条件 %s 缺少展示元数据", condition.Key)
		}
	}
	if len(payload.RefCandidates) != 5 {
		t.Fatalf("候选组应为 5 项（固定顺序），得到 %d", len(payload.RefCandidates))
	}
	if payload.RefCandidates[0].Kind != "badge" {
		t.Fatalf("候选组首项应为 badge，得到 %q", payload.RefCandidates[0].Kind)
	}

	// 缺 admin.access → 403。
	nobody := activeActor()
	app2 := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return nobody, nil }))
	if status, _ := doRequest(t, app2, fiber.MethodGet, "/api/v1/admin/trust-levels/conditions", ""); status != fiber.StatusForbidden {
		t.Fatalf("缺 admin.access 的目录读 status = %d（期望 403）", status)
	}
}

// TestRuleImpactEndpoint 断言估算端点：成功、权限、未装配三条路径。
func TestRuleImpactEndpoint(t *testing.T) {
	estimated := int64(42)
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	controller := NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithRuleEditingDeps(nil, nil, fakeEstimator{result: trustlevels.RuleImpact{Estimated: &estimated, Method: "strictest_leaf"}})
	app := newTestApp(t, controller)

	body := `{"tiers":[{"tierId":"","requirements":{"days_visited":3},"rule":` + ruleDoc(verifiedLeaf) + `}]}`
	status, response := doRequest(t, app, fiber.MethodPost, "/api/v1/admin/trust-levels/rule-impact", body)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d，body=%s", status, response)
	}
	var envelope struct {
		Data ruleImpactResponse `json:"data"`
	}
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	payload := envelope.Data
	if len(payload.Tiers) != 1 {
		t.Fatalf("应返回 1 档，得到 %d", len(payload.Tiers))
	}
	if payload.Tiers[0].Estimated == nil || *payload.Tiers[0].Estimated != 42 {
		t.Fatalf("estimated 未透传：%+v", payload.Tiers[0])
	}
	if payload.Tiers[0].UnsupportedConditions == nil {
		t.Fatal("unsupportedConditions 必须是空数组而不是 null")
	}

	// 缺 trust.manage（只有 admin.access）→ 403：能读策略不等于能看全站影响面。
	reader := activeActor(identity.PermissionAdminAccess)
	app2 := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return reader, nil }))
	if status, _ := doRequest(t, app2, fiber.MethodPost, "/api/v1/admin/trust-levels/rule-impact", body); status != fiber.StatusForbidden {
		t.Fatalf("缺 trust.manage 的估算 status = %d（期望 403）", status)
	}

	// 未装配估算器 → 500（不能假装算过）。
	app3 := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))
	if status, response := doRequest(t, app3, fiber.MethodPost, "/api/v1/admin/trust-levels/rule-impact", body); status != fiber.StatusInternalServerError {
		t.Fatalf("未装配估算器的 status = %d（期望 500），body=%s", status, response)
	}
}
