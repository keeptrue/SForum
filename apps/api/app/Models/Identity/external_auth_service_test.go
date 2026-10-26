package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// ExternalAuthService 的纯逻辑测试（无 DB）。
// 重点验证 Host-owned 安全边界：激活门控、operation 匹配、resolvedDigest 兼容性。
// DB 依赖的事务行为（registration/login/link）由集成测试覆盖。

// fakeActivationStoreLite 是 service 测试用的极简激活目录。
type fakeActivationStoreLite struct {
	items map[string]ProviderActivation
}

func (s *fakeActivationStoreLite) Get(_ context.Context, providerID string) (ProviderActivation, error) {
	if a, ok := s.items[providerID]; ok {
		return a, nil
	}
	return ProviderActivation{}, ErrProviderActivationNotFound
}
func (s *fakeActivationStoreLite) List(_ context.Context) ([]ProviderActivation, error) {
	out := make([]ProviderActivation, 0, len(s.items))
	for _, a := range s.items {
		out = append(out, a)
	}
	return out, nil
}
func (s *fakeActivationStoreLite) Upsert(_ context.Context, input ProviderActivationInput) (ProviderActivation, error) {
	return ProviderActivation{}, nil
}
func (s *fakeActivationStoreLite) RecordProbe(_ context.Context, result ProviderActivationProbeResult) error {
	return nil
}
func (s *fakeActivationStoreLite) Delete(_ context.Context, providerID string) error { return nil }
func (s *fakeActivationStoreLite) ResetOperationsToDefaults(_ context.Context, providerID string) (ProviderActivation, error) {
	return ProviderActivation{}, nil
}

// TestExternalAuthService_IsOperationActivatedDefaultOff 默认全 off。
func TestExternalAuthService_IsOperationActivatedDefaultOff(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	svc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: &fakeActivationStoreLite{items: map[string]ProviderActivation{}},
	})
	ctx := context.Background()

	for _, op := range []ExternalAuthOperation{ExternalAuthOperationLogin, ExternalAuthOperationRegistration, ExternalAuthOperationLink} {
		ok, err := svc.IsOperationActivated(ctx, "p1", op)
		if err != nil {
			t.Fatalf("IsOperationActivated %s: %v", op, err)
		}
		if ok {
			t.Fatalf("operation %s must default off", op)
		}
		if err := svc.RequireActivated(ctx, "p1", op); err != ErrExternalAuthOperationNotActivated {
			t.Fatalf("RequireActivated %s should fail, got %v", op, err)
		}
	}
}

// TestExternalAuthService_IsOperationActivatedWhenEnabled 有效激活后允许。
func TestExternalAuthService_IsOperationActivatedWhenEnabled(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	digest := strings.Repeat("e", 64)
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "p1", ContractVersion: "p1@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{
				{Name: AuthOperationLoginStart},
				{Name: AuthOperationLinkStart},
			},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.p1", PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "rt",
		},
	}
	store := &fakeActivationStoreLite{items: map[string]ProviderActivation{
		"p1": {
			ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
			LoginEnabled: true, RegistrationEnabled: false, LinkEnabled: true,
		},
	}}
	svc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	ctx := context.Background()

	ok, err := svc.IsOperationActivated(ctx, "p1", ExternalAuthOperationLogin)
	if err != nil || !ok {
		t.Fatalf("login should be activated: %v %v", ok, err)
	}
	ok, err = svc.IsOperationActivated(ctx, "p1", ExternalAuthOperationRegistration)
	if err != nil || ok {
		t.Fatalf("registration should NOT be activated: %v %v", ok, err)
	}
	ok, err = svc.IsOperationActivated(ctx, "p1", ExternalAuthOperationLink)
	if err != nil || !ok {
		t.Fatalf("link should be activated: %v %v", ok, err)
	}
}

// TestExternalAuthService_IsOperationActivatedUnknownProvider 未知 provider 视为 off（不暴露存在性）。
func TestExternalAuthService_IsOperationActivatedUnknownProvider(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	svc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: &fakeActivationStoreLite{items: map[string]ProviderActivation{}},
	})
	ctx := context.Background()
	ok, err := svc.IsOperationActivated(ctx, "unknown", ExternalAuthOperationLogin)
	if err != nil {
		t.Fatalf("unknown provider should not error: %v", err)
	}
	if ok {
		t.Fatalf("unknown provider should be off")
	}
}

// TestExternalAuthService_CompleteLoginRejectsWrongOperation operation 不匹配拒绝。
func TestExternalAuthService_CompleteLoginRejectsWrongOperation(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	svc := NewExternalAuthService(ExternalAuthDeps{})
	// registration 断言传给 CompleteLogin 应拒绝。
	_, err := svc.CompleteLogin(context.Background(), ExternalAuthAssertion{
		ProviderID:      "p1",
		Operation:       ExternalAuthOperationRegistration,
		ProviderSubject: "123",
	})
	if !errors.Is(err, ErrExternalAuthOperationMismatch) {
		t.Fatalf("expected operation mismatch, got %v", err)
	}
}

// TestExternalAuthAssertion_ResolvedDigestCoreHMAC 验证 Core-HMAC 优先于兼容 digest。
func TestExternalAuthAssertion_ResolvedDigestCoreHMAC(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	a := ExternalAuthAssertion{
		ProviderID: "p1", ProviderSubject: "42", SubjectDigest: "deadbeef",
	}
	d, err := a.resolvedDigest()
	if err != nil {
		t.Fatalf("resolvedDigest: %v", err)
	}
	// 应是 Core-HMAC 计算结果（64 hex），不是兼容 digest "deadbeef"。
	if len(d) != 64 || d == "deadbeef" {
		t.Fatalf("resolvedDigest should be Core-HMAC, got %s", d)
	}
}

// TestExternalAuthAssertion_ResolvedDigestFallsBackToLegacy 无 raw subject 时用兼容 digest。
func TestExternalAuthAssertion_ResolvedDigestFallsBackToLegacy(t *testing.T) {
	a := ExternalAuthAssertion{
		ProviderID: "p1", SubjectDigest: strings.Repeat("a", 64),
	}
	d, err := a.resolvedDigest()
	if err != nil {
		t.Fatalf("resolvedDigest: %v", err)
	}
	if d != a.SubjectDigest {
		t.Fatalf("expected legacy digest %s, got %s", a.SubjectDigest, d)
	}
}

// TestExternalAuthAssertion_ResolvedDigestEmptyFails 全空拒绝。
func TestExternalAuthAssertion_ResolvedDigestEmptyFails(t *testing.T) {
	a := ExternalAuthAssertion{ProviderID: "p1"}
	if _, err := a.resolvedDigest(); err == nil {
		t.Fatalf("expected error on empty assertion")
	}
}

// TestExternalAuthService_ActivationStoreAccessor 验证 ActivationStore() getter。
func TestExternalAuthService_ActivationStoreAccessor(t *testing.T) {
	store := &fakeActivationStoreLite{}
	svc := NewExternalAuthService(ExternalAuthDeps{ActivationStore: store})
	if svc.ActivationStore() != store {
		t.Fatalf("ActivationStore getter mismatch")
	}
	if NewExternalAuthService(ExternalAuthDeps{}).ActivationStore() != nil {
		t.Fatalf("nil ActivationStore should return nil")
	}
}

// 编译期断言：fakeActivationStoreLite 实现 ProviderActivationStore。
var _ ProviderActivationStore = (*fakeActivationStoreLite)(nil)

// 避免 unused import 报错（time 在 probe 字段用到）。
var _ = time.Now
var _ = identityregistry.ProviderKindAuth
