package identity

import (
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/config"
)

// M1/T1B 单元测试：subject HMAC digest。
// 密钥由 ConfigureIdentitySubjectHMAC 注入稳定材料；禁止进程随机。

func TestComputeSubjectDigest_DeterministicAndKeyed(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(config.IdentitySubjectHMACDevDefault); err != nil {
		t.Fatalf("configure: %v", err)
	}

	d1, err := ComputeSubjectDigest("sforum.auth-github.auth", "12345")
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}
	d2, err := ComputeSubjectDigest("sforum.auth-github.auth", "12345")
	if err != nil {
		t.Fatalf("compute digest again: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest not deterministic: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("digest length = %d, want 64", len(d1))
	}
	// 不同 providerId 下同 subject 必须不同。
	d3, err := ComputeSubjectDigest("sforum.auth-other.auth", "12345")
	if err != nil {
		t.Fatalf("compute digest other provider: %v", err)
	}
	if d1 == d3 {
		t.Fatalf("digest must differ across providerId; same subject should not collide")
	}
	// 不同 subject 也应不同。
	d4, err := ComputeSubjectDigest("sforum.auth-github.auth", "99999")
	if err != nil {
		t.Fatalf("compute digest other subject: %v", err)
	}
	if d1 == d4 {
		t.Fatalf("digest must differ across subjects")
	}
}

func TestComputeSubjectDigest_StableAcrossConfigureWithSameSecret(t *testing.T) {
	// 模拟重启：重置后用同一稳定密钥重新注入，digest 必须一致。
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(config.IdentitySubjectHMACDevDefault); err != nil {
		t.Fatalf("configure 1: %v", err)
	}
	d1, err := ComputeSubjectDigest("p", "s")
	if err != nil {
		t.Fatalf("digest 1: %v", err)
	}
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(config.IdentitySubjectHMACDevDefault); err != nil {
		t.Fatalf("configure 2: %v", err)
	}
	d2, err := ComputeSubjectDigest("p", "s")
	if err != nil {
		t.Fatalf("digest 2: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("stable secret must produce same digest across reconfigure: %s vs %s", d1, d2)
	}
}

func TestComputeSubjectDigest_AcceptsHexSecret(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	// 64 hex 字符 = 32 字节，满足最小长度。
	hexSecret := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	if err := ConfigureIdentitySubjectHMAC(hexSecret); err != nil {
		t.Fatalf("configure: %v", err)
	}

	d, err := ComputeSubjectDigest("sforum.auth-github.auth", "12345")
	if err != nil {
		t.Fatalf("compute digest with valid hex secret: %v", err)
	}
	if len(d) != 64 {
		t.Fatalf("digest length = %d, want 64", len(d))
	}
}

func TestComputeSubjectDigest_DevFallsBackToStableDefault(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	// 未 Configure 时懒加载稳定开发默认，禁止进程随机。
	d1, err := ComputeSubjectDigest("p", "s")
	if err != nil {
		t.Fatalf("stable default digest: %v", err)
	}
	ResetIdentitySubjectHMACKeyForTest()
	d2, err := ComputeSubjectDigest("p", "s")
	if err != nil {
		t.Fatalf("stable default digest again: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("dev fallback must be stable, not process-random: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("digest length = %d, want 64", len(d1))
	}
}

func TestConfigureIdentitySubjectHMAC_EmptyUsesStableDevDefault(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(""); err != nil {
		t.Fatalf("configure empty: %v", err)
	}
	key, err := IdentitySubjectHMACKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if string(key) != config.IdentitySubjectHMACDevDefault && string(key) != identitySubjectHMACStableDevDefault {
		// 两者必须是同一字面量。
		if string(key) != identitySubjectHMACStableDevDefault {
			t.Fatalf("empty configure should use stable dev default, got %q", string(key))
		}
	}
	if config.IdentitySubjectHMACDevDefault != identitySubjectHMACStableDevDefault {
		t.Fatalf("config and identity stable defaults must match: %q vs %q",
			config.IdentitySubjectHMACDevDefault, identitySubjectHMACStableDevDefault)
	}
}

func TestValidateIdentitySubjectHMACSecret_ProductionPath(t *testing.T) {
	// 走真实 config 校验（APP_ENV 路径由 config.Load 调用）。
	if err := config.ValidateIdentitySubjectHMACSecret("", true); err == nil {
		t.Fatal("production must reject empty secret")
	}
	if err := config.ValidateIdentitySubjectHMACSecret(config.IdentitySubjectHMACDevDefault, true); err == nil {
		t.Fatal("production must reject dev default")
	}
	if err := config.ValidateIdentitySubjectHMACSecret("tooshort", true); err == nil {
		t.Fatal("production must reject short secret")
	}
	if err := config.ValidateIdentitySubjectHMACSecret("change-me", true); err == nil {
		t.Fatal("production must reject placeholder")
	}
	strong := strings.Repeat("x", 32)
	if err := config.ValidateIdentitySubjectHMACSecret(strong, true); err != nil {
		t.Fatalf("production must accept strong secret: %v", err)
	}
	hexSecret := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	if err := config.ValidateIdentitySubjectHMACSecret(hexSecret, true); err != nil {
		t.Fatalf("production must accept 32-byte hex: %v", err)
	}
}

func TestVerifySubjectDigest_ConstantTimeMatch(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(config.IdentitySubjectHMACDevDefault); err != nil {
		t.Fatalf("configure: %v", err)
	}

	d, err := ComputeSubjectDigest("sforum.auth-github.auth", "42")
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	ok, err := VerifySubjectDigest("sforum.auth-github.auth", "42", d)
	if err != nil || !ok {
		t.Fatalf("verify expected ok, got %v %v", ok, err)
	}
	ok, err = VerifySubjectDigest("sforum.auth-github.auth", "43", d)
	if err != nil || ok {
		t.Fatalf("verify mismatched subject should fail: %v %v", ok, err)
	}
}
