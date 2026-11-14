package trustlevels_test

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// fakeRefSource 是 `RefCandidateSource` 的测试替身。
//
// 用替身而不是真库：这条路径要断的是「引用不存在 → 失效」与「引用仍存在 → 放行」
// 两个方向，而真库还需要先造分类/角色/插件夹具——那测的是徽章 store 的查询，不是
// 本包的判定（D17 的窄接口正是为了让这条边界可测）。
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

// refSet 造一份「只有 category=general、role=moderator」的候选集合。
func refSet() conditions.RefCandidateSet {
	return conditions.NormalizeRefCandidateSet([]conditions.RefCandidateGroup{
		conditions.NewRefCandidateGroup(conditions.RefKindCategory, []conditions.RefCandidate{{Value: "general", Label: "综合"}}),
		conditions.NewRefCandidateGroup(conditions.RefKindRole, []conditions.RefCandidate{{Value: "moderator", Label: "版主"}}),
	})
}

// authoringRule 造一条引用分类的规则。
func authoringRule(slug string) string {
	return ruleDoc(`{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"` + slug + `","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}`)
}

// factRule 造一条引用插件事实的规则。
func factRule(factKey string) string {
	return ruleDoc(`{"type":"fact.count","params":{"factKey":"` + factKey + `","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}`)
}

// TestRuleReferenceAllowed 是对照组：引用仍存在时必须放行。
//
// 只测拒绝的测试，把整个校验删掉也会绿；这条对照断言保证「删掉校验」或「一律拒绝」
// 两种退化都会红。
func TestRuleReferenceAllowed(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", authoringRule("general"))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	details, err := trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{set: refSet()}, nil)
	if err != nil {
		t.Fatalf("校验出错：%v", err)
	}
	if len(details) != 0 {
		t.Fatalf("引用仍存在却报失效：%+v", details)
	}
}

// TestRuleReferenceDenied 断言引用被删后的精确码与定位。
func TestRuleReferenceDenied(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", authoringRule("gone"))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	details, err := trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{set: refSet()}, nil)
	if err != nil {
		t.Fatalf("校验出错：%v", err)
	}
	if len(details) != 1 {
		t.Fatalf("应有 1 条失效引用，得到 %d：%+v", len(details), details)
	}
	detail := details[0]
	if detail.Code != "rule_reference_invalid" {
		t.Fatalf("reasonCode = %q（期望 rule_reference_invalid）", detail.Code)
	}
	if detail.Field != "rule" {
		t.Fatalf("field = %q（期望 rule）", detail.Field)
	}
	if detail.Params["ref"] != "gone" {
		t.Fatalf("params.ref = %v（期望 gone）", detail.Params["ref"])
	}
	if path, _ := detail.Params["path"].(string); path != "root.params.targetSlug" {
		t.Fatalf("params.path = %q（期望 root.params.targetSlug）", path)
	}
	if detail.Rank != 1 || detail.TierID != "member" {
		t.Fatalf("定位丢失：rank=%d tierId=%q", detail.Rank, detail.TierID)
	}
}

// TestRuleReferenceFactKeyDeclaration 钉住「未声明的事实无法判定」这条边界。
func TestRuleReferenceFactKeyDeclaration(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", factRule("acme.wiki.pages"))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	details, err := trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{set: refSet()}, nil)
	if err != nil {
		t.Fatalf("校验出错：%v", err)
	}
	if len(details) != 1 || details[0].Code != "rule_reference_invalid" {
		t.Fatalf("未声明的事实应报 rule_reference_invalid，得到 %+v", details)
	}
	if reason := details[0].Reason; reason == "" {
		t.Fatal("缺少人读原因")
	}
	// 声明之后必须放行。
	details, err = trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{set: refSet()}, []string{"acme.wiki.pages"})
	if err != nil {
		t.Fatalf("校验出错：%v", err)
	}
	if len(details) != 0 {
		t.Fatalf("事实已声明却报失效：%+v", details)
	}
}

// TestRuleReferenceSkipsRetiredTier 钉住「停用档不参与判定，也就不该挡住保存」。
//
// 软停用的合规路径是「先停用、稍后再修引用」；把停用档的失效引用做成 422 会把这条路
// 堵死，运营只能删掉整档配置才能保存一次无关的改动。
func TestRuleReferenceSkipsRetiredTier(t *testing.T) {
	retired := `{"id":"tier_7","name":{"zh":"停用档","en":"Retired"},"icon":"leaf","requirements":{},"limits":{},"earnable":[],"retired":true,"rule":` + authoringRule("gone") + `}`
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ""), retired))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	details, err := trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{set: refSet()}, nil)
	if err != nil {
		t.Fatalf("校验出错：%v", err)
	}
	if len(details) != 0 {
		t.Fatalf("停用档位的失效引用不该被报：%+v", details)
	}
}

// TestRuleReferenceSourceFailuresAreLoud 钉住两种失败姿态：
// 候选读不出来 → 返错（不是「没有失效引用」）；未注入候选来源 → 也返错。
func TestRuleReferenceSourceFailuresAreLoud(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", authoringRule("general"))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := trustlevels.RuleReferenceIssues(context.Background(), policy, fakeRefSource{err: errors.New("db down")}, nil); err == nil {
		t.Fatal("候选查询失败必须返错：否则「引用已删」会被静默放行")
	}
	if _, err := trustlevels.RuleReferenceIssues(context.Background(), policy, nil, nil); err == nil {
		t.Fatal("未注入候选来源必须返错")
	}
	// 不含任何引用参数的规则不需要查库，也不该因为 source 为 nil 就报错。
	plain, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ruleDoc(verifiedLeaf))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if details, err := trustlevels.RuleReferenceIssues(context.Background(), plain, nil, nil); err != nil || len(details) != 0 {
		t.Fatalf("无引用参数的规则不该查库：details=%+v err=%v", details, err)
	}
}

// TestValidateEarnableDeclared 钉住 `earnable_not_declared` 的判据形状（M4a 接通后）。
//
// 现在这条码**经由 HTTP 可达**（解码期不再硬拒未声明键），所以断言从「领域函数能报」
// 升级成「只报 undeclared、不报 inactive」——后者是本刀最容易写反的一条：插件停用时
// 报 422 会让「先停用插件、稍后修配置」这条合规路径直接走不通。
func TestValidateEarnableDeclared(t *testing.T) {
	registry := trustlevels.NewEarnableRegistry([]trustlevels.EarnableDeclaration{
		{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
		{Key: "acme.shop.orders", ExtensionID: "acme.shop", Enabled: true},
	})
	details := trustlevels.ValidateEarnableDeclared(
		[]string{"topic.create", "acme.shop.orders", "acme.gone.thing", "acme.gone.thing"},
		registry,
	)
	if len(details) != 1 {
		t.Fatalf("只该报 1 条（未声明的那枚；重复与已声明/核心键都不报），得到 %d：%+v", len(details), details)
	}
	if details[0].Code != "earnable_not_declared" {
		t.Fatalf("code = %q（期望 earnable_not_declared）", details[0].Code)
	}
	if details[0].Field != "earnable" {
		t.Fatalf("field = %q（期望 earnable）", details[0].Field)
	}
	if details[0].Params["permission"] != "acme.gone.thing" {
		t.Fatalf("params.permission = %v", details[0].Params["permission"])
	}
	// **反向断言**：params 里不得出现 variant / cause。
	//
	// 这一支只有一种成因（从未声明），变体字段是死枚举；曾经后端写 `variant`、前端读
	// `cause`，两层漂移叠在一起。删掉之后靠这条断言守住：将来真需要区分成因时，
	// 先加「谁会让它取第二个值」的测试，再动这里——而不是悄悄塞回一个永远同值的键。
	for _, forbidden := range []string{"variant", "cause"} {
		if value, present := details[0].Params[forbidden]; present {
			t.Fatalf("params 里出现了 %q=%v：该成因唯一，变体通道已删除", forbidden, value)
		}
	}
	if details[0].Params["permission"] != "acme.gone.thing" {
		t.Fatalf("params.permission 必须保留（前端靠它定位勾选）：%v", details[0].Params)
	}
	// inactive 的键**允许保存**（不产生明细）；它只在运行期不授予。
	if got := trustlevels.ValidateEarnableDeclared([]string{"acme.wiki.pages"}, registry); len(got) != 0 {
		t.Fatalf("插件停用的键不该被写期拒绝（否则停用即锁死编辑）：%+v", got)
	}
	// 核心键永远 valid。
	for _, key := range trustlevels.EarnableWhitelist() {
		if state := registry.State(key); state != trustlevels.EarnableStateValid {
			t.Fatalf("核心键 %q 状态 = %q（期望 valid）", key, state)
		}
	}
}
