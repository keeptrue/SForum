package extensions

import (
	"context"
	"testing"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// protectedDefaultThemeApplyFixture 造一个「磁盘制品与 DB 身份一致」的受保护
// 内置默认主题，供 apply 层用例直接调用 ApplyThemeRuntimePublication。
func protectedDefaultThemeApplyFixture(t *testing.T, source string, isSystem, isDeletable bool) Extension {
	t.Helper()
	packageFixture := exactThemeRuntimeExtensionFixture(t, DefaultThemeID, "/default")
	manifest := Manifest{ID: DefaultThemeID, Name: "default", Version: "2.0.0", Type: TypeTheme}
	if err := writeManifest(packageFixture.PackagePath, manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packageFixture.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	target := packageFixture
	target.Version = manifest.Version
	target.Manifest = manifest
	target.PackageDigest = digest
	target.Source = source
	target.IsSystem = isSystem
	target.IsDeletable = isDeletable
	return target
}

// unapprovedProtectedDefaultPublication 复现旧自愈缺陷写下的账本：目标已激活、
// 但 core_replacements_approved=false；来源主题的核心页替换是已批准的。账本本身
// 合法（未批准 + 无 actor），所以只有 apply 层能靠「受保护默认 + 来源已批准」
// 认出这是历史缺陷。
func unapprovedProtectedDefaultPublication(target Extension) ThemeRuntimePublication {
	return ThemeRuntimePublication{
		Revision:      23,
		DesiredState:  ThemeRuntimePublicationActive,
		ThemeID:       target.ID,
		ThemeVersion:  target.Version,
		PackageDigest: target.PackageDigest,
		// 同主题再次发布（现网 rev 23 的形态）：来源与目标是同一件精确制品，
		// 上一版账本里来源的核心页替换是已批准的。
		SourceThemeID:                  target.ID,
		SourceThemeVersion:             target.Version,
		SourcePackageDigest:            target.PackageDigest,
		SourceCoreReplacementsApproved: true,
		SourceApprovalSource:           ThemeApprovalSourceHost,
		CoreReplacementsApproved:       false,
		ApprovalSource:                 ThemeApprovalSourceAdmin,
		Reason:                         ThemeRuntimePublicationActivation,
	}
}

func applyFixtureService(t *testing.T, target Extension, pages *themeActivationApprovalRegistry) *Service {
	t.Helper()
	store := newFakeExtensionStore(map[string]Extension{target.ID: target})
	store.activeThemeID = target.ID
	return NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(pages),
	)
}

// 兜底分支：未批准 + 受保护默认主题 + 来源已批准 → 必须走 HostApproved 注册路径。
// 走普通替换路径会把 page_provider_bindings 删空，整站回落 Core。
func TestThemePublicationApplyUsesHostApprovalForUnapprovedProtectedDefault(t *testing.T) {
	target := protectedDefaultThemeApplyFixture(t, SourceBuiltin, true, false)
	pages := &themeActivationApprovalRegistry{}
	service := applyFixtureService(t, target, pages)

	if err := service.ApplyThemeRuntimePublication(
		context.Background(), unapprovedProtectedDefaultPublication(target),
	); err != nil {
		t.Fatalf("apply unapproved protected default publication: %v", err)
	}
	if pages.hostApproved != 1 || pages.ordinary != 0 || pages.approved != 0 {
		t.Fatalf("apply paths host=%d approved=%d ordinary=%d", pages.hostApproved, pages.approved, pages.ordinary)
	}
}

// 兜底也不是「没有批准就补批准」：人类主动不批准的激活（ActorUserID != 0）
// 必须保持未批准，否则等于给非 super_admin 一条绕过审批拿核心页替换的路径。
func TestThemePublicationApplyFallbackIgnoresHumanUnapprovedActivation(t *testing.T) {
	target := protectedDefaultThemeApplyFixture(t, SourceBuiltin, true, false)
	pages := &themeActivationApprovalRegistry{}
	service := applyFixtureService(t, target, pages)

	publication := unapprovedProtectedDefaultPublication(target)
	publication.ActorUserID = 99
	if err := service.ApplyThemeRuntimePublication(context.Background(), publication); err != nil {
		t.Fatalf("apply human unapproved protected default publication: %v", err)
	}
	if pages.hostApproved != 0 || pages.ordinary != 1 {
		t.Fatalf("human unapproved activation was host-approved: host=%d ordinary=%d", pages.hostApproved, pages.ordinary)
	}
}

// 兜底不是无条件放行：同一个未批准账本，目标不是「受保护默认主题」时仍然走
// 普通替换路径（不许顺手给第三方主题发核心页替换权限）。
func TestThemePublicationApplyFallbackRequiresProtectedDefaultIdentity(t *testing.T) {
	target := protectedDefaultThemeApplyFixture(t, SourceUploaded, false, true)
	pages := &themeActivationApprovalRegistry{}
	service := applyFixtureService(t, target, pages)

	if err := service.ApplyThemeRuntimePublication(
		context.Background(), unapprovedProtectedDefaultPublication(target),
	); err != nil {
		t.Fatalf("apply unapproved non-protected publication: %v", err)
	}
	if pages.hostApproved != 0 || pages.ordinary != 1 {
		t.Fatalf("non-protected target took host approval: host=%d ordinary=%d", pages.hostApproved, pages.ordinary)
	}
}
