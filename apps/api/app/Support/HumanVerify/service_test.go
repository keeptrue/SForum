package humanverify

import (
	"context"
	"errors"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

func TestDisabledServiceSkipsVerification(t *testing.T) {
	service := NewService(ServiceConfig{Enabled: false, ChallengeTTL: time.Minute}, fakeProvider{}, NewMemoryStore())

	if err := service.Verify(context.Background(), VerifyRequest{Purpose: PurposeRegister}); err != nil {
		t.Fatalf("disabled service should skip verification, got %v", err)
	}
}

func TestServiceSkipsDisabledPurpose(t *testing.T) {
	service := NewService(
		ServiceConfig{
			Enabled:         true,
			ChallengeTTL:    time.Minute,
			EnabledPurposes: map[Purpose]bool{PurposeRegister: false, PurposeLoginRisk: true},
		},
		fakeProvider{},
		NewMemoryStore(),
	)

	challenge, err := service.Challenge(context.Background(), PurposeRegister, Subject{})
	if err != nil {
		t.Fatalf("disabled purpose challenge returned error: %v", err)
	}
	if challenge.Provider != ProviderDisabled {
		t.Fatalf("expected disabled challenge for register purpose, got %#v", challenge)
	}
	if err := service.Verify(context.Background(), VerifyRequest{Purpose: PurposeRegister}); err != nil {
		t.Fatalf("disabled purpose verify should skip token requirement, got %v", err)
	}
	if err := service.Verify(context.Background(), VerifyRequest{Purpose: PurposeLoginRisk}); !errors.Is(err, ErrRequired) {
		t.Fatalf("enabled purpose should still require a token, got %v", err)
	}
}

func TestServiceRequiresTokenWhenEnabled(t *testing.T) {
	service := NewService(ServiceConfig{Enabled: true, ChallengeTTL: time.Minute}, fakeProvider{}, NewMemoryStore())

	err := service.Verify(context.Background(), VerifyRequest{Purpose: PurposeRegister})
	if !errors.Is(err, ErrRequired) {
		t.Fatalf("expected ErrRequired, got %v", err)
	}
}

func TestServiceRejectsInvalidProviderResult(t *testing.T) {
	service := NewService(
		ServiceConfig{Enabled: true, ChallengeTTL: time.Minute},
		fakeProvider{result: VerifyResult{Verified: false, Code: CodeInvalid}},
		NewMemoryStore(),
	)

	err := service.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposeRegister,
		Provider: ProviderAltcha,
		Token:    "bad-token",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestServiceRejectsUnsupportedProvider(t *testing.T) {
	service := NewService(
		ServiceConfig{Enabled: true, ChallengeTTL: time.Minute},
		fakeProvider{result: VerifyResult{Verified: true, Code: CodeOK}},
		NewMemoryStore(),
	)

	err := service.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposeRegister,
		Provider: "other",
		Token:    "valid-token",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestServiceRejectsReplayedToken(t *testing.T) {
	service := NewService(
		ServiceConfig{Enabled: true, ChallengeTTL: time.Minute},
		fakeProvider{result: VerifyResult{Verified: true, Code: CodeOK}},
		NewMemoryStore(),
	)
	req := VerifyRequest{
		Purpose:  PurposeRegister,
		Provider: ProviderAltcha,
		Token:    "token-1",
	}

	if err := service.Verify(context.Background(), req); err != nil {
		t.Fatalf("first verify returned error: %v", err)
	}
	if err := service.Verify(context.Background(), req); !errors.Is(err, ErrReplayed) {
		t.Fatalf("expected replay error, got %v", err)
	}
}

type fakeProvider struct {
	result VerifyResult
}

func (p fakeProvider) Challenge(context.Context, Purpose, Subject) (Challenge, error) {
	return Challenge{
		Provider: ProviderAltcha,
		Purpose:  PurposeRegister,
		Payload:  map[string]any{"challenge": "fake"},
	}, nil
}

func (p fakeProvider) Verify(context.Context, VerifyRequest) (VerifyResult, error) {
	if p.result.Code == "" {
		return VerifyResult{Verified: true, Code: CodeOK}, nil
	}
	return p.result, nil
}

// P3-4：一个解出的 token 全局只能消费一次，不能跨场景各用一次。

func TestServiceRejectsReplayedTokenAcrossPurposes(t *testing.T) {
	service := NewService(
		ServiceConfig{Enabled: true, ChallengeTTL: time.Minute},
		fakeProvider{result: VerifyResult{Verified: true, Code: CodeOK}},
		NewMemoryStore(),
	)
	token := "cross-scenario-token"

	if err := service.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposeRegister,
		Provider: ProviderAltcha,
		Token:    token,
	}); err != nil {
		t.Fatalf("register verify returned error: %v", err)
	}

	err := service.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposePasswordReset,
		Provider: ProviderAltcha,
		Token:    token,
	})
	if !errors.Is(err, ErrReplayed) {
		t.Fatalf("reusing a token in another purpose must be replayed, got %v", err)
	}
}

func TestReplayKeyScopesProviderButNotPurpose(t *testing.T) {
	token := "same-token"
	register := replayKey(VerifyRequest{Purpose: PurposeRegister, Provider: ProviderAltcha, Token: token})
	passwordReset := replayKey(VerifyRequest{Purpose: PurposePasswordReset, Provider: ProviderAltcha, Token: token})
	if register != passwordReset {
		t.Fatalf("replay key must not embed purpose: register=%s password_reset=%s", register, passwordReset)
	}

	// Service.Verify 在 MarkUsed 之前就把非 altcha provider 拒掉，provider 隔离
	// 只能在键层面断言：键必须仍按 provider 分域，避免将来接入第二个 provider 时，
	// 不同 provider 的同名 token 互相把对方标成已消费。
	otherProvider := replayKey(VerifyRequest{Purpose: PurposeRegister, Provider: "other", Token: token})
	if otherProvider == register {
		t.Fatalf("replay key must stay scoped per provider")
	}
}

// P3-4 第二步：purpose 绑进 challenge 签名载荷，token 与签发场景强绑定。

func TestAltchaProviderBindsChallengeToPurpose(t *testing.T) {
	provider := NewAltchaProvider(AltchaConfig{Secret: "test-secret", Cost: 1, ChallengeTTL: time.Minute})
	token := solveAltchaChallenge(t, provider, PurposeRegister)

	result, err := provider.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposeRegister,
		Provider: ProviderAltcha,
		Token:    token,
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if !result.Verified || result.Code != CodeOK {
		t.Fatalf("token must verify for its issuing purpose, got %#v", result)
	}

	result, err = provider.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposePasswordReset,
		Provider: ProviderAltcha,
		Token:    token,
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Verified || result.Code != CodeInvalid {
		t.Fatalf("token issued for register must not verify for password_reset, got %#v", result)
	}
}

func TestAltchaProviderAcceptsLegacyChallengeWithoutPurposeBinding(t *testing.T) {
	provider := NewAltchaProvider(AltchaConfig{Secret: "test-secret", Cost: 1, ChallengeTTL: time.Minute})
	// 模拟本次发布之前签发的 challenge：签名有效，但没有 parameters.data。
	legacy, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:           "PBKDF2/SHA-256",
		Cost:                1,
		HMACSignatureSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("CreateChallenge returned error: %v", err)
	}

	result, err := provider.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposePasswordReset,
		Provider: ProviderAltcha,
		Token:    solveAndEncodeAltchaChallenge(t, legacy),
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("legacy challenge without purpose binding must still verify, got %#v", result)
	}
}

// 本用例守的是「签名覆盖 parameters.data」这条库级性质，不是 provider 的场景比对：
// 把 boundPurpose 的比对整段摘掉，它依然 PASS（已实测）。场景比对由
// TestAltchaProviderBindsChallengeToPurpose 守。分工见
// knowledge/decisions/2026-11-16-altcha-purpose-binding-rollout-window.md §3。
func TestAltchaProviderRejectsStrippedChallengeDataSignature(t *testing.T) {
	provider := NewAltchaProvider(AltchaConfig{Secret: "test-secret", Cost: 1, ChallengeTTL: time.Minute})
	challenge := issueAltchaChallenge(t, provider, PurposeRegister)
	// 剥掉 data 再重新编码：签名失配，所以「兼容分支」无法被伪造出来。
	challenge.Parameters.Data = nil

	result, err := provider.Verify(context.Background(), VerifyRequest{
		Purpose:  PurposePasswordReset,
		Provider: ProviderAltcha,
		Token:    solveAndEncodeAltchaChallenge(t, challenge),
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Verified {
		t.Fatalf("stripping parameters.data must invalidate the signature, got %#v", result)
	}
}

func issueAltchaChallenge(t *testing.T, provider *AltchaProvider, purpose Purpose) altcha.Challenge {
	t.Helper()

	challenge, err := provider.Challenge(context.Background(), purpose, Subject{IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("Challenge returned error: %v", err)
	}
	raw, ok := challenge.Payload.(altcha.Challenge)
	if !ok {
		t.Fatalf("expected altcha challenge payload, got %T", challenge.Payload)
	}
	return raw
}

func solveAltchaChallenge(t *testing.T, provider *AltchaProvider, purpose Purpose) string {
	t.Helper()
	return solveAndEncodeAltchaChallenge(t, issueAltchaChallenge(t, provider, purpose))
}

func solveAndEncodeAltchaChallenge(t *testing.T, challenge altcha.Challenge) string {
	t.Helper()

	solution, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{
		Challenge: challenge,
		DeriveKey: altcha.DeriveKeyPBKDF2(),
	})
	if err != nil {
		t.Fatalf("SolveChallenge returned error: %v", err)
	}
	token, err := encodeAltchaPayload(challenge, *solution)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return token
}
