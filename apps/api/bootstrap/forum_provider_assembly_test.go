package bootstrap

import (
	"context"
	"errors"
	"testing"

	providers "github.com/keeptrue/sforum/apps/api/app/Providers"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// assemblyTestConfigSource 模拟 options.Service 的运行时读取。
type assemblyTestConfigSource struct {
	cfg humanverify.RuntimeConfig
	err error
}

func (s assemblyTestConfigSource) HumanVerificationConfig(context.Context) (humanverify.RuntimeConfig, error) {
	return s.cfg, s.err
}

// productionForumProviderForTest 用**生产同一条构造链**（同一个构造函数）造一个
// 依赖全为 nil 的 ForumProvider：装配用例要证明的是「收口点是否接线」，
// 不是「某个测试专用构造函数是否接线」，所以不用简化的 NewForumProvider。
func productionForumProviderForTest() *providers.ForumProvider {
	return providers.NewForumProviderWithPublicContributions(
		nil, // store
		nil, // options
		nil, // users
		nil, // sessions
		nil, // publisher
		nil, // indexer
		nil, // search
		nil, // reindexer
		nil, // topic actions
		nil, // comment actions
		nil, // topic surfaces
		nil, // composer toolbar
		nil, // publication policy
	)
}

// postRiskRuntimeVerifier 返回**生产注入类型** *humanverify.RuntimeService，
// 而不是测试 fake —— 这样装配用例证明的是 bootstrap 实际注入的那个类型能拦下请求。
func postRiskRuntimeVerifier(t *testing.T, postRiskEnabled bool) *humanverify.RuntimeService {
	t.Helper()
	return humanverify.NewRuntimeService(
		assemblyTestConfigSource{cfg: humanverify.RuntimeConfig{
			Provider:     humanverify.ProviderAltcha,
			AltchaSecret: "assembly-test-secret",
			PurposeEnabled: map[humanverify.Purpose]bool{
				humanverify.PurposePostRisk: postRiskEnabled,
			},
		}},
		humanverify.NewMemoryStore(),
		humanverify.RuntimeConfig{},
	)
}

// TestFinalizeAPIForumProviderWiresPostRiskHumanVerification 是 P3-3 的装配级证据：
// 删掉 finalizeAPIForumProvider 里的 .WithHumanVerification(...) 会让本用例变红
// （探针为 false / 直接返回错误），而不是像之前那样「生产接线删了测试仍全绿」。
func TestFinalizeAPIForumProviderWiresPostRiskHumanVerification(t *testing.T) {
	verifier := postRiskRuntimeVerifier(t, true)
	base := productionForumProviderForTest()
	if base.HasHumanVerification() {
		t.Fatal("probe must be false before wiring: 说明探针本身失效了")
	}

	wired, err := finalizeAPIForumProvider(base, verifier)
	if err != nil {
		t.Fatalf("finalizeAPIForumProvider: %v", err)
	}
	if wired == nil || !wired.HasHumanVerification() {
		t.Fatal("production assembly dropped the post_risk human verification verifier")
	}

	// 注入的确实是会拦请求的那个实例：同一 verifier 在开关打开时缺 token 必须拒绝。
	if err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	}); !errors.Is(err, humanverify.ErrRequired) {
		t.Fatalf("injected verifier does not enforce post_risk: err=%v", err)
	}
}

// 收口点不允许「没人验证的发帖写入路径」：nil verifier 必须让装配失败。
func TestFinalizeAPIForumProviderRejectsMissingVerifier(t *testing.T) {
	_, err := finalizeAPIForumProvider(productionForumProviderForTest(), nil)
	if err == nil {
		t.Fatal("nil verifier must fail closed at assembly")
	}
}

func TestFinalizeAPIForumProviderRejectsMissingProvider(t *testing.T) {
	_, err := finalizeAPIForumProvider(nil, postRiskRuntimeVerifier(t, true))
	if err == nil {
		t.Fatal("nil provider must be rejected")
	}
}

// 开关关闭时同一装配仍然「接了但放行」：装配存在性与开关状态是两件事，
// 避免把探针误用成「开关是否打开」。
func TestFinalizeAPIForumProviderWiresEvenWhenScenarioDisabled(t *testing.T) {
	wired, err := finalizeAPIForumProvider(productionForumProviderForTest(), postRiskRuntimeVerifier(t, false))
	if err != nil {
		t.Fatalf("finalizeAPIForumProvider: %v", err)
	}
	if !wired.HasHumanVerification() {
		t.Fatal("wiring must be observable even when the scenario switch is off")
	}
}
