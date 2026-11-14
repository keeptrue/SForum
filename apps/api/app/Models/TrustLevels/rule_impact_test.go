package trustlevels_test

import (
	"context"
	"errors"
	"testing"

	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// fakeEstimator 记录它收到的树，并按预设值返回。
//
// 断的是「喂给估算器的树长什么样」：真正的聚合 SQL 在徽章侧（已有测试），本包负责
// 的是把「数值快筛 + 规则树」正确折成**一棵** all 根，不重不漏。
type fakeEstimator struct {
	got    []ruleengine.Node
	result trustlevels.RuleImpact
	err    error
}

func (f *fakeEstimator) EstimateRuleImpact(_ context.Context, rule ruleengine.Node) (trustlevels.RuleImpact, error) {
	f.got = append(f.got, rule)
	if f.err != nil {
		return trustlevels.RuleImpact{}, f.err
	}
	return f.result, nil
}

// TestEstimateTierImpactComposesRequirementsAndRule 钉住合成口径。
func TestEstimateTierImpactComposesRequirementsAndRule(t *testing.T) {
	estimator := &fakeEstimator{result: trustlevels.RuleImpact{Estimated: int64Ptr(12), Method: "strictest_leaf"}}
	requirements := trustlevels.Requirements{DaysVisited: 30, TopicCount: 5, CommentCount: 0, LikeReceivedCount: 0, LikeGivenCount: 0, BookmarkCount: 2}
	rule, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ruleDoc(verifiedLeaf))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	impact, err := trustlevels.EstimateTierImpact(context.Background(), estimator, requirements, rule.Tiers[0].Rule)
	if err != nil {
		t.Fatalf("估算出错：%v", err)
	}
	if impact.Estimated == nil || *impact.Estimated != 12 || impact.Method != "strictest_leaf" {
		t.Fatalf("结果未透传：%+v", impact)
	}
	if len(estimator.got) != 1 {
		t.Fatalf("应只调用估算器一次（一次聚合查询），得到 %d 次", len(estimator.got))
	}
	root := estimator.got[0]
	if root.Op != ruleengine.OpAll {
		t.Fatalf("合成根应是 all，得到 %q", root.Op)
	}
	// 3 个非零阈值（days_visited / topic_count / bookmark_count）+ 1 条规则 = 4 个子节点；
	// 零值指标（comment / like_received / like_given）不折叶子。
	if len(root.Children) != 4 {
		t.Fatalf("合成根子节点 = %d（期望 4：3 个阈值 + 1 条规则）", len(root.Children))
	}
	for index, child := range root.Children[:3] {
		if child.Type != "activity.count" {
			t.Fatalf("第 %d 个子节点应是 activity.count 折算叶子，得到 %q", index, child.Type)
		}
		if child.Cmp == nil || child.Cmp.Op != ruleengine.CompareGTE {
			t.Fatalf("阈值叶子必须是 gte，得到 %+v", child.Cmp)
		}
	}
}

// TestEstimateTierImpactWithoutRuleUsesRequirementsOnly 钉住「只配数值也要估」。
func TestEstimateTierImpactWithoutRuleUsesRequirementsOnly(t *testing.T) {
	estimator := &fakeEstimator{result: trustlevels.RuleImpact{Estimated: int64Ptr(3), Method: "single_leaf"}}
	impact, err := trustlevels.EstimateTierImpact(context.Background(), estimator,
		trustlevels.Requirements{DaysVisited: 7}, nil)
	if err != nil {
		t.Fatalf("估算出错：%v", err)
	}
	if impact.Estimated == nil || *impact.Estimated != 3 {
		t.Fatalf("结果未透传：%+v", impact)
	}
	if len(estimator.got) != 1 || estimator.got[0].Type != "activity.count" {
		t.Fatalf("只有一个阈值时应直接估该叶子，得到 %+v", estimator.got)
	}
}

// TestEstimateTierImpactNoConditionsIsUnconditional 钉住「无门槛档不编数字」。
func TestEstimateTierImpactNoConditionsIsUnconditional(t *testing.T) {
	estimator := &fakeEstimator{}
	impact, err := trustlevels.EstimateTierImpact(context.Background(), estimator, trustlevels.Requirements{}, nil)
	if err != nil {
		t.Fatalf("估算出错：%v", err)
	}
	if impact.Estimated != nil {
		t.Fatalf("无门槛档不该给数字，得到 %v", *impact.Estimated)
	}
	if impact.Method != "unconditional" {
		t.Fatalf("method = %q（期望 unconditional）", impact.Method)
	}
	if len(estimator.got) != 0 {
		t.Fatalf("无门槛档不该查库，却调用了估算器 %d 次", len(estimator.got))
	}
}

// TestEstimateTierImpactSwallowsUndecodableDraft 钉住「草稿半成品不报 500」。
func TestEstimateTierImpactSwallowsUndecodableDraft(t *testing.T) {
	estimator := &fakeEstimator{}
	impact, err := trustlevels.EstimateTierImpact(context.Background(), estimator, trustlevels.Requirements{}, []byte(`{"v":2,"root":{}}`))
	if err != nil {
		t.Fatalf("半成品草稿不该返错：%v", err)
	}
	if impact.Estimated != nil || impact.Method != "estimate_failed" {
		t.Fatalf("解不开的草稿应给 estimated:null + estimate_failed，得到 %+v", impact)
	}
}

// TestEstimateTierImpactPropagatesEstimatorFailure 钉住环境故障不被吞。
func TestEstimateTierImpactPropagatesEstimatorFailure(t *testing.T) {
	estimator := &fakeEstimator{err: errors.New("db down")}
	if _, err := trustlevels.EstimateTierImpact(context.Background(), estimator,
		trustlevels.Requirements{DaysVisited: 7}, nil); err == nil {
		t.Fatal("估算器失败必须向上传递（由 HTTP 层报 500），不能静默返回 null")
	}
}

// TestEstimateTierImpactRequiresEstimator 钉住装配缺失时吵。
func TestEstimateTierImpactRequiresEstimator(t *testing.T) {
	if _, err := trustlevels.EstimateTierImpact(context.Background(), nil, trustlevels.Requirements{DaysVisited: 1}, nil); err == nil {
		t.Fatal("未注入估算器必须返错")
	}
}

func int64Ptr(value int64) *int64 { return &value }
