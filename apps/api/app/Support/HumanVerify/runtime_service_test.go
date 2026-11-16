package humanverify_test

import (
	"context"
	"errors"
	"testing"

	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// stubConfigSource 模拟 options.Service：每次读都返回当前运营配置。
type stubConfigSource struct {
	cfg humanverify.RuntimeConfig
	err error
}

func (s stubConfigSource) HumanVerificationConfig(context.Context) (humanverify.RuntimeConfig, error) {
	return s.cfg, s.err
}

func postRiskRuntimeService(t *testing.T, source humanverify.ConfigSource, fallback humanverify.RuntimeConfig) *humanverify.RuntimeService {
	t.Helper()
	return humanverify.NewRuntimeService(source, humanverify.NewMemoryStore(), fallback)
}

func altchaRuntimeConfig(postRiskEnabled bool) humanverify.RuntimeConfig {
	return humanverify.RuntimeConfig{
		Provider:        humanverify.ProviderAltcha,
		AltchaSecret:    "test-altcha-secret",
		PurposeEnabled:  map[humanverify.Purpose]bool{humanverify.PurposePostRisk: postRiskEnabled},
		RateLimitWindow: 0,
	}
}

// 生产注入类型的行为基线：开关打开时缺 token 必须 ErrRequired。
func TestRuntimeServiceRequiresTokenWhenPostRiskEnabled(t *testing.T) {
	verifier := postRiskRuntimeService(t, stubConfigSource{cfg: altchaRuntimeConfig(true)}, humanverify.RuntimeConfig{})

	err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	})
	if !errors.Is(err, humanverify.ErrRequired) {
		t.Fatalf("empty token with post_risk enabled: err=%v want ErrRequired", err)
	}
}

// 开关关闭时必须零校验：这是「不误伤」的一半，另一半在 Forum 控制器用例里。
func TestRuntimeServiceSkipsWhenPostRiskDisabled(t *testing.T) {
	verifier := postRiskRuntimeService(t, stubConfigSource{cfg: altchaRuntimeConfig(false)}, humanverify.RuntimeConfig{})

	if err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	}); err != nil {
		t.Fatalf("post_risk disabled must skip verification, err=%v", err)
	}
}

// 其余场景不受 post_risk 开关影响（避免「按场景开关」退化成全局开关）。
func TestRuntimeServicePostRiskSwitchDoesNotGateOtherPurposes(t *testing.T) {
	cfg := altchaRuntimeConfig(true)
	cfg.PurposeEnabled[humanverify.PurposeRegister] = false
	verifier := postRiskRuntimeService(t, stubConfigSource{cfg: cfg}, humanverify.RuntimeConfig{})

	if err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposeRegister,
	}); err != nil {
		t.Fatalf("register must stay disabled, err=%v", err)
	}
	if err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	}); !errors.Is(err, humanverify.ErrRequired) {
		t.Fatalf("post_risk must stay enforced, err=%v", err)
	}
}

// typed-nil 装进 Verifier 接口时不得 panic：装配层没有反射判空，靠这里兜住。
func TestRuntimeServiceTypedNilIsDisabledNotPanic(t *testing.T) {
	var verifier *humanverify.RuntimeService
	var asInterface humanverify.Verifier = verifier

	if err := asInterface.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
		Token:    "any-token",
	}); err != nil {
		t.Fatalf("typed-nil verifier must behave as disabled, err=%v", err)
	}
	challenge, err := asInterface.Challenge(context.Background(), humanverify.PurposePostRisk, humanverify.Subject{})
	if err != nil {
		t.Fatalf("typed-nil challenge: %v", err)
	}
	if challenge.Provider != humanverify.ProviderDisabled {
		t.Fatalf("typed-nil challenge provider=%q want %q", challenge.Provider, humanverify.ProviderDisabled)
	}
}

// 降级语义（fail-closed 方向）：env 声明了 altcha 时，Options 读失败仍强制校验。
// fallback 的 PurposeEnabled 为 nil → Service 视为「所有场景启用」。
func TestRuntimeServiceOptionsFailureKeepsAltchaFallbackEnforced(t *testing.T) {
	fallback := humanverify.RuntimeConfig{Provider: humanverify.ProviderAltcha, AltchaSecret: "env-secret"}
	verifier := postRiskRuntimeService(t, stubConfigSource{err: errors.New("options unavailable")}, fallback)

	err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	})
	if !errors.Is(err, humanverify.ErrRequired) {
		t.Fatalf("altcha fallback must stay enforced, err=%v want ErrRequired", err)
	}
}

// 降级语义（fail-open 方向，已记录前提）：env 没声明 provider 时 Options 读失败 → 零校验。
// 该分支的存在条件与安全性论证写在 NewRuntimeService 的注释里；这里把它钉成行为契约，
// 将来若有人把它改成 fail-closed，会先红在这里。
func TestRuntimeServiceOptionsFailureWithDisabledFallbackSkips(t *testing.T) {
	verifier := postRiskRuntimeService(t, stubConfigSource{err: errors.New("options unavailable")}, humanverify.RuntimeConfig{})

	if err := verifier.Verify(context.Background(), humanverify.VerifyRequest{
		Provider: humanverify.ProviderAltcha,
		Purpose:  humanverify.PurposePostRisk,
	}); err != nil {
		t.Fatalf("disabled fallback must skip verification, err=%v", err)
	}
}
