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
		// 候选组的**形状**断言（A2-1）：五类恒五项、固定顺序、字段齐全。
		// 只断 conditions 的测试会让「端点返回 `refCandidates: []`」也绿——那等于把编辑器
		// 的对象下拉整个弄丢，而运营只会看到「分类选不了」。
		if len(payload.RefCandidates) != 5 {
			t.Fatalf("候选组应为 5 项（固定顺序），得到 %d", len(payload.RefCandidates))
		}
		wantKinds := []string{"badge", "category", "tag", "role", "provider"}
		for index, group := range payload.RefCandidates {
			if group.Kind != wantKinds[index] {
				t.Fatalf("候选组[%d] = %q（期望 %q）", index, group.Kind, wantKinds[index])
			}
			if group.Candidates == nil {
				t.Fatalf("%s 的 candidates 必须是 [] 而不是 null", group.Kind)
			}
			if !group.Unavailable {
				// 这个子测试的 source 给的就是 UnavailableRefCandidateSet。
				t.Fatalf("%s 应标记 unavailable（取不到候选 ≠ 候选为空）", group.Kind)
			}
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

// ---- M2：公开面「还差哪几项」的逐条句子 ----

// stubActivityReader / stubPolicySource / stubEvaluator 让公开面测试不碰数据库。
type stubActivityReader struct{ activity trustlevels.Activity }

func (s stubActivityReader) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return s.activity, nil
}

type stubPolicySource struct{ policy trustlevels.Policy }

func (s stubPolicySource) Policy(context.Context) (trustlevels.Policy, error) { return s.policy, nil }

type stubEvaluator struct{ leaves []trustlevels.TierCondition }

func (s stubEvaluator) Prepare(context.Context, []json.RawMessage) {}

func (s stubEvaluator) Match(context.Context, json.RawMessage) trustlevels.RuleOutcome {
	return trustlevels.RuleOutcome{Matched: true}
}

func (s stubEvaluator) LeafConditions(context.Context, json.RawMessage) []trustlevels.TierCondition {
	return s.leaves
}

// policyWithTwoTierRule 造一份「TL1 无规则、TL2 带扁平 all 规则」的策略。
func policyWithTwoTierRule(t *testing.T) trustlevels.Policy {
	t.Helper()
	base := func(rule string) string {
		body := `{"id":"","name":{"zh":"档","en":"Tier"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]`
		if rule == "" {
			return body + `}`
		}
		return body + `,"rule":` + rule + `}`
	}
	policy, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + base("") + `,` + base(ruleDoc(`{"op":"all","children":[`+verifiedLeaf+`]}`)) + `]}`,
	)
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	return policy
}

// TestPublicViewerConditionsAreSentencesWithoutAST 断言公开面句子的三条硬约束（验收 6）。
//
//  1. 只有**本人**、只有**下一档**才有 `conditions`；
//  2. 每条句子双语非空（服务端是唯一文案来源，前端不拼句子）；
//  3. 序列化全文**不含** AST 词汇（`children` / `cmp` / `params` / `type` / `rule`）。
//
// 第 3 条必须是**值级**断言（整段响应字节）：徽章那刀证明过「键在值空」会假绿。
func TestPublicViewerConditionsAreSentencesWithoutAST(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	resolver := trustlevels.NewResolver(
		stubActivityReader{},
		stubPolicySource{policy: policyWithTwoTierRule(t)},
	).WithRuleEvaluators(func(context.Context, int64) (trustlevels.RuleEvaluator, error) {
		return stubEvaluator{leaves: []trustlevels.TierCondition{
			{Satisfied: true, Text: trustlevels.LocalizedName{Zh: "邮箱已验证", En: "email verified"}},
			{Satisfied: false, Text: trustlevels.LocalizedName{Zh: "发布主题 ≥ 3", En: "topics published ≥ 3"}},
		}}, nil
	})
	app := newTestApp(t, NewController(nil, resolver, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policyWithTwoTierRule(t), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }))

	status, body := doRequest(t, app, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("公开读 status = %d，body=%s", status, body)
	}
	var envelope struct {
		Data struct {
			Viewer struct {
				Level      int `json:"level"`
				Conditions []struct {
					Satisfied bool `json:"satisfied"`
					Text      struct {
						Zh string `json:"zh"`
						En string `json:"en"`
					} `json:"text"`
				} `json:"conditions"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	viewer := envelope.Data.Viewer
	if len(viewer.Conditions) != 2 {
		t.Fatalf("本人下一档配了规则时应给出 2 条句子，得到 %d（body=%s）", len(viewer.Conditions), body)
	}
	for index, condition := range viewer.Conditions {
		if condition.Text.Zh == "" || condition.Text.En == "" {
			t.Fatalf("第 %d 条句子缺语言：%+v", index, condition)
		}
	}
	if viewer.Conditions[0].Satisfied != true || viewer.Conditions[1].Satisfied != false {
		t.Fatalf("satisfied 未透传：%+v", viewer.Conditions)
	}
	for _, forbidden := range []string{`"children"`, `"cmp"`, `"params"`, `"type"`, `"rule"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("公开响应出现 AST 词汇 %s：\n%s", forbidden, body)
		}
	}

	// 他人/未解析视角：没有 viewer、也就没有句子。
	anonymous := identity.Actor{}
	appAnon := newTestApp(t, NewController(nil, resolver, nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policyWithTwoTierRule(t), nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return anonymous, nil }))
	status, body = doRequest(t, appAnon, fiber.MethodGet, "/api/v1/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("匿名读 status = %d", status)
	}
	if strings.Contains(body, `"conditions"`) {
		t.Fatalf("匿名响应不该有 conditions：\n%s", body)
	}
}

// ---- M4a：可挣得白名单运行期化 ----

// earnableResolver 造一个带可挣得声明来源的 resolver。
func earnableResolver(policy trustlevels.Policy, declarations []trustlevels.EarnableDeclaration) *trustlevels.Resolver {
	return trustlevels.NewResolver(
		stubActivityReader{},
		stubPolicySource{policy: policy},
	).WithEarnables(func(context.Context) ([]trustlevels.EarnableDeclaration, error) {
		return declarations, nil
	})
}

// TestPutRejectsUndeclaredEarnableKey 是 M1 遗留口的**接通证据**（验收 5）。
//
// M1 时这条码经由 HTTP 不可达（解码期的 `earnable_not_whitelisted` 先拒）。M4a 把解码期
// 硬拒去掉之后，它才真正接管——所以这条断言既是新功能的验收，也是那段注释的替代物。
func TestPutRejectsUndeclaredEarnableKey(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	policy := trustlevels.DefaultPolicy()
	written := 0
	controller := NewController(nil, earnableResolver(policy, nil), nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policy, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { written++; return nil })
	app := newTestApp(t, controller)

	config := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["acme.gone.thing"]}]}`
	status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", `{"config":`+mustJSONString(config)+`}`)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d（期望 422），body=%s", status, response)
	}
	if !strings.Contains(response, `"reasonCode":"earnable_not_declared"`) {
		t.Fatalf("缺少 earnable_not_declared：\n%s", response)
	}
	// **反向断言**：422 明细的 params 不得带 variant / cause。
	//
	// 这条是删除变体通道后的守卫：将来有人把变体加回来，会先撞这条断言，而不是又一次
	// 让前后端各读一个不同的键名。用「解析后无该键」而不是字符串匹配——字符串匹配对
	// `"variant":null` 或转义形态会漏。
	var envelope struct {
		Data struct {
			Details struct {
				Errors []struct {
					ReasonCode string         `json:"reasonCode"`
					Params     map[string]any `json:"params"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		t.Fatalf("422 响应不是合法 JSON：%v", err)
	}
	errors := envelope.Data.Details.Errors
	if len(errors) == 0 {
		t.Fatalf("422 缺少字段级明细：\n%s", response)
	}
	for _, item := range errors {
		for _, forbidden := range []string{"variant", "cause"} {
			if value, present := item.Params[forbidden]; present {
				t.Fatalf("params 里出现了 %q=%v：该成因唯一，变体通道已删除（%s）", forbidden, value, response)
			}
		}
		if item.Params["permission"] != "acme.gone.thing" {
			t.Fatalf("params.permission 丢了（前端靠它定位勾选）：%v", item.Params)
		}
	}
	if written != 0 {
		t.Fatalf("被拒的配置仍写库 %d 次", written)
	}

	// 对照组：核心七键必须能存（反向证据，否则「一律拒绝」也会绿）。
	okConfig := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["topic.create"]}]}`
	if status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", `{"config":`+mustJSONString(okConfig)+`}`); status != fiber.StatusOK {
		t.Fatalf("核心键存不进去：status=%d body=%s", status, response)
	}
}

// TestPutAllowsInactiveEarnableKey 钉住变体分支的另一半：插件停用的键**允许保存**。
//
// 只测 422 那一半会把「先停用插件、稍后修配置」这条合规路径堵死——运营改任何一档都会被
// 一枚他改不了的勾选挡住。
func TestPutAllowsInactiveEarnableKey(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage)
	policy := trustlevels.DefaultPolicy()
	written := 0
	controller := NewController(nil, earnableResolver(policy, []trustlevels.EarnableDeclaration{
		{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
	}), nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policy, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil }).
		WithConfigWriter(func(context.Context, identity.Actor, string) error { written++; return nil })
	app := newTestApp(t, controller)

	config := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["acme.wiki.pages"]}]}`
	if status, response := doRequest(t, app, fiber.MethodPut, "/api/v1/admin/trust-levels", `{"config":`+mustJSONString(config)+`}`); status != fiber.StatusOK {
		t.Fatalf("插件停用的键必须允许保存，status=%d body=%s", status, response)
	}
	if written != 1 {
		t.Fatalf("应写库一次，实际 %d", written)
	}
}

// TestAdminPayloadEarnableStates 钉住后台读面三态（验收 4 的读面部分）。
func TestAdminPayloadEarnableStates(t *testing.T) {
	admin := activeActor(identity.PermissionAdminAccess)
	policy, err := trustlevels.DecodeConfig(`{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["topic.create","acme.shop.orders","acme.wiki.pages","acme.gone.thing"]}]}`)
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	controller := NewController(nil, earnableResolver(policy, []trustlevels.EarnableDeclaration{
		{Key: "acme.shop.orders", ExtensionID: "acme.shop", Enabled: true},
		{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
	}), nil, nil).
		WithFeatureGate(enabledGate).
		WithPolicyReader(func(context.Context) (trustlevels.Policy, error) { return policy, nil }).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) { return admin, nil })
	status, body := doRequest(t, newTestApp(t, controller), fiber.MethodGet, "/api/v1/admin/trust-levels", "")
	if status != fiber.StatusOK {
		t.Fatalf("后台读 status = %d，body=%s", status, body)
	}
	var envelope struct {
		Data struct {
			Eligible       []string `json:"eligiblePermissions"`
			EarnableStates []struct {
				Key   string `json:"key"`
				State string `json:"state"`
			} `json:"earnableStates"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	states := map[string]string{}
	for _, item := range envelope.Data.EarnableStates {
		states[item.Key] = item.State
	}
	want := map[string]string{
		"topic.create":     "valid",
		"acme.shop.orders": "valid",
		"acme.wiki.pages":  "inactive",
		"acme.gone.thing":  "unknown",
	}
	for key, state := range want {
		if states[key] != state {
			t.Fatalf("%q 的状态 = %q（期望 %q）；完整 %v", key, states[key], state, states)
		}
	}
	// eligiblePermissions = 核心 7 + 已声明 valid 的 1 条；inactive/unknown 不进。
	if len(envelope.Data.Eligible) != len(trustlevels.EarnableWhitelist())+1 {
		t.Fatalf("eligiblePermissions = %d 条（期望 %d）：%v", len(envelope.Data.Eligible), len(trustlevels.EarnableWhitelist())+1, envelope.Data.Eligible)
	}
	for _, key := range envelope.Data.Eligible {
		if key == "acme.wiki.pages" || key == "acme.gone.thing" {
			t.Fatalf("不可授予的键 %q 出现在 eligiblePermissions 里", key)
		}
	}
}
