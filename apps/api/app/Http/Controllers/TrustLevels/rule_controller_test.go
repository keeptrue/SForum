package trustlevelscontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
// `err` 非空时模拟读库失败（N3：环境故障必须与可修 422 分开）。
type fakeRefSource struct {
	set conditions.RefCandidateSet
	err error
}

func (f fakeRefSource) RefCandidates(context.Context) (conditions.RefCandidateSet, error) {
	if f.err != nil {
		return nil, f.err
	}
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

// TestConditionsEndpointFiltersByCandidates 断言目录端点**按候选过滤**（F1）。
//
// 判据是双向的，单向会放过两种错：
//   - 只断「少了 1 条」→ 过滤掉了别的东西（比如把 role 条件也砍了）也绿；
//   - 只断「注入后出现」→ 不注入时那条死路照样摆着也绿。
//
// 同时留**反向证据** `RuleConditionAllowed("fact.count") == true`：过滤只许动发布面，
// 动了校验白名单就会让插件停用期间的存量规则读不出来（把「暂时没候选」当成了
// 「条件不存在」）。
func TestConditionsEndpointFiltersByCandidates(t *testing.T) {
	shared := conditions.Core().Definitions()

	t.Run("无候选：不下发 fact.count", func(t *testing.T) {
		admin := activeActor(identity.PermissionAdminAccess)
		controller := NewController(nil, nil, nil, nil).
			WithFeatureGate(enabledGate).
			WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
			WithRuleEditingDeps(fakeRefSource{set: conditions.UnavailableRefCandidateSet()}, nil, nil)
		status, body := doRequest(t, newTestApp(t, controller), fiber.MethodGet, "/api/v1/admin/trust-levels/conditions", "")
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
		// 子集 11 条 − 无事实候选的 fact.count = 10 = 共享目录 − 3。
		if len(payload.Conditions) != len(shared)-3 {
			t.Fatalf("条件数 = %d（期望共享 %d − 3 = %d）", len(payload.Conditions), len(shared), len(shared)-3)
		}
		for _, condition := range payload.Conditions {
			switch condition.Key {
			case "fact.count":
				t.Fatal("没有事实候选时不得下发 fact.count（空下拉死路）")
			case "content.pinned", "content.hidden":
				t.Fatalf("子集外条件被下发：%s", condition.Key)
			}
			if condition.LabelZh == "" || condition.ValueKind == "" {
				t.Fatalf("条件 %s 缺少展示元数据", condition.Key)
			}
		}
		if len(payload.FactCandidates) != 0 {
			t.Fatalf("没有插件声明时 factCandidates 应为空，得到 %d", len(payload.FactCandidates))
		}
		// 反向证据：发布面过滤 ≠ 校验面收紧。
		if !trustlevels.RuleConditionAllowed("fact.count") {
			t.Fatal("RuleConditionAllowed(fact.count) 必须仍为 true：过滤只能作用于发布面")
		}
		if len(trustlevels.RuleConditions()) != len(shared)-2 {
			t.Fatalf("校验用子集应保持 %d 条（共享 − 2），得到 %d", len(shared)-2, len(trustlevels.RuleConditions()))
		}
	})

	t.Run("注入事实候选：fact.count 出现且候选非空", func(t *testing.T) {
		admin := activeActor(identity.PermissionAdminAccess)
		factCandidates := []conditions.FactCandidate{{
			Key: "acme.wiki.pages", LabelZh: "页面数", LabelEn: "Pages", UnitZh: "个", UnitEn: "pages",
			Compare: []string{"gte", "lte"},
		}}
		controller := NewController(nil, nil, nil, nil).
			WithFeatureGate(enabledGate).
			WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
			WithRuleEditingDeps(
				fakeRefSource{set: conditions.UnavailableRefCandidateSet()},
				func() []conditions.FactCandidate { return factCandidates },
				nil,
			)
		status, body := doRequest(t, newTestApp(t, controller), fiber.MethodGet, "/api/v1/admin/trust-levels/conditions", "")
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
		if len(payload.Conditions) != len(shared)-2 {
			t.Fatalf("注入候选后条件数 = %d（期望共享 %d − 2 = %d）", len(payload.Conditions), len(shared), len(shared)-2)
		}
		if len(payload.FactCandidates) == 0 {
			t.Fatal("factCandidates 必须非空（否则编辑器仍拿到空下拉）")
		}
		var factCondition *trustLevelCondition
		for index := range payload.Conditions {
			if payload.Conditions[index].Key == "fact.count" {
				factCondition = &payload.Conditions[index]
			}
		}
		if factCondition == nil {
			t.Fatal("注入事实候选后 fact.count 必须出现")
		}
		// 只是「出现」还不够：`factKey` 参数的 options 必须是已声明的 key，
		// 否则条件在编辑器里仍然是一个空下拉（发布一条选不出来的条件）。
		var options []string
		for _, param := range factCondition.Params {
			if param.Name == "factKey" {
				options = param.Options
			}
		}
		if len(options) != 1 || options[0] != "acme.wiki.pages" {
			t.Fatalf("factKey 参数候选 = %v（期望 [acme.wiki.pages]）", options)
		}
	})
}

// TestConditionsEndpointRequiresAdmin 断言缺 admin.access 时 403。
func TestConditionsEndpointRequiresAdmin(t *testing.T) {
	nobody := activeActor()
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return nobody, nil }))
	if status, _ := doRequest(t, app, fiber.MethodGet, "/api/v1/admin/trust-levels/conditions", ""); status != fiber.StatusForbidden {
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

// TestReferenceCheckWithoutSourceWarns 钉住「跳过校验必须吵」（N2）。
//
// 未注入 `RefCandidateSource` 时写路径不校验引用（fail-open）。这个宽容点本身保留
// （nil 时全拒更糟），但**静默**跳过等于装配漏注入时毫无症状——所以行为不变、日志必须有。
func TestReferenceCheckWithoutSourceWarns(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	written := 0
	// 刻意**不**调用 WithRuleEditingDeps：模拟装配漏注入。
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { written++; return nil }))

	rule := ruleDoc(`{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"general","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}`)
	body := `{"config":` + mustJSONString(configWithRuleJSON(t, rule)) + `}`
	if status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", body); status != fiber.StatusOK {
		t.Fatalf("未注入候选来源时写路径应放行（保留宽容点），status = %d，body=%s", status, response)
	}
	if written != 1 {
		t.Fatalf("写库次数 = %d（期望 1）", written)
	}
	output := logs.String()
	if !strings.Contains(output, "rule reference check skipped") {
		t.Fatalf("跳过引用校验时必须打 warn（否则装配漏注入无任何症状），日志=%q", output)
	}
	if !strings.Contains(output, "WithRuleEditing") {
		t.Fatalf("warn 必须带装配提示（告诉运维该注入什么），日志=%q", output)
	}
	// 对照组：不带规则的配置不该刷这条 warn（否则日志噪声会训练运维忽略它）。
	logs.Reset()
	if status, _ := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels",
		`{"config":`+mustJSONString(configWithRuleJSON(t, ""))+`}`); status != fiber.StatusOK {
		t.Fatalf("无规则配置的写 status = %d（期望 200）", status)
	}
	if strings.Contains(logs.String(), "rule reference check skipped") {
		t.Fatalf("没有规则的配置不该打跳过 warn，日志=%q", logs.String())
	}
}

// TestPutReferenceCheckFailureReturns500 钉住「读候选失败 → 500 且未写库」（N3）。
//
// 读库失败是**环境故障**，与「引用的对象被删」这种可修 422 必须分开：报成 422 会让
// 运营去改一份本来正确的配置。而且它绝不能落库——那条规则根本没被验证过。
func TestPutReferenceCheckFailureReturns500(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	written := 0
	app := newTestApp(t, NewController(nil, nil, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return trustlevels.DefaultPolicy(), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithRuleEditingDeps(fakeRefSource{err: errors.New("candidate query failed")}, nil, nil).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { written++; return nil }))

	rule := ruleDoc(`{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"general","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}`)
	body := `{"config":` + mustJSONString(configWithRuleJSON(t, rule)) + `}`
	status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", body)
	if status != fiber.StatusInternalServerError {
		t.Fatalf("读候选失败时 status = %d（期望 500），body=%s", status, response)
	}
	if !strings.Contains(response, "trust_levels.reference_check_failed") {
		t.Fatalf("500 的原因码不对：\n%s", response)
	}
	if written != 0 {
		t.Fatalf("校验失败仍写库了 %d 次", written)
	}
}
