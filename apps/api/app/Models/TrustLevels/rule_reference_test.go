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

// TestValidateEarnableDeclared 钉住 `earnable_not_declared` 的判据形状。
//
// 这条码在 M1 **经由 HTTP 不可达**：解码期的 `earnable_not_whitelisted` 会先拒同一批
// 输入（插件声明面是 M4 的事）。所以它的断言落在领域函数上——等 M4.2 把解码期的硬拒
// 改成「保留 + 运行期过滤」，这个函数就是写路径的拒绝点，判据不变。
func TestValidateEarnableDeclared(t *testing.T) {
	details := trustlevels.ValidateEarnableDeclared(
		[]string{"topic.create", "acme.wiki.pages", "acme.wiki.pages", ""},
		trustlevels.DeclaredPermissionKeys(),
	)
	if len(details) != 1 {
		t.Fatalf("应只报 1 条未声明键（重复与空串不重复报），得到 %d：%+v", len(details), details)
	}
	if details[0].Code != "earnable_not_declared" {
		t.Fatalf("code = %q（期望 earnable_not_declared）", details[0].Code)
	}
	if details[0].Field != "earnable" {
		t.Fatalf("field = %q（期望 earnable）", details[0].Field)
	}
	if details[0].Params["permission"] != "acme.wiki.pages" {
		t.Fatalf("params.permission = %v", details[0].Params["permission"])
	}
	// 已声明集合内的键必须放行（否则「一律拒绝」也会绿）。
	if ok := trustlevels.ValidateEarnableDeclared([]string{"topic.create"}, trustlevels.DeclaredPermissionKeys()); len(ok) != 0 {
		t.Fatalf("核心白名单键不该被判未声明：%+v", ok)
	}
}
