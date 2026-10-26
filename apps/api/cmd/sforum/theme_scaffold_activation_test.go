package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// makeThemeScaffold 生成一个真实的 `make:theme` 脚手包，返回包根与合并后的 manifest。
func makeThemeScaffold(t *testing.T, id string) (string, extensionmanifest.Manifest) {
	t.Helper()
	target := filepath.Join(t.TempDir(), id)
	if _, err := GenerateExtensionScaffold(makeOptions{
		Kind: "theme", ID: id, Name: "Scaffold Probe", Description: "disposable scaffold probe.",
		URL: "https://example.com/scaffold-probe", AuthorName: "Acme",
		AuthorURL: "https://example.com", AuthorEmail: "dev@example.com",
		Out: target, NoInteraction: true,
	}); err != nil {
		t.Fatalf("GenerateExtensionScaffold: %v", err)
	}
	manifest, err := extensionmanifest.LoadPackage(target)
	if err != nil {
		t.Fatalf("scaffold must pass the CLI package validation: %v", err)
	}
	return target, manifest
}

func scaffoldExtension(t *testing.T, root string, manifest extensionmanifest.Manifest) extensions.Extension {
	t.Helper()
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatalf("digest scaffold: %v", err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypeTheme,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: digest, PackagePath: root, Manifest: manifest,
	}
}

// builtinDefaultThemeRoot 返回受保护内置默认主题的源码包根。
func builtinDefaultThemeRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	return filepath.Join(repoRoot, "extensions/builtin/themes/sforum-default")
}

// TestMakeThemeScaffoldPassesCliAndRuntimePreflights 是 `make:theme` 的长期契约：
// 生成的脚手架必须在 CLI 校验（extension validate / extension test 的包加载）
// 与运行时激活前置（页面预检 + 组件注册表转换预检）两侧同时通过。
// 这条测试替代了「先手工下载安装才发现激活被拒」的反馈循环。
func TestMakeThemeScaffoldPassesCliAndRuntimePreflights(t *testing.T) {
	root, manifest := makeThemeScaffold(t, "acme.scaffold-probe")
	extension := scaffoldExtension(t, root, manifest)

	// 1) CLI 侧（extension validate / extension test 共用 LoadPackage）。
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("CLI manifest validation: %v", err)
	}
	// 2) 页面注册表预检：全新安装与替换既有活动主题两种最终状态。
	registry := pages.NewRegistry(pages.NewMemoryStore())
	bridge := pages.NewExtensionBridge(registry)
	theme := pages.ThemeExtension{
		ID: extension.ID, Version: extension.Version,
		PackagePath: root, PackageDigest: extension.PackageDigest,
	}
	if _, err := bridge.PreflightThemePackage(theme, ""); err != nil {
		t.Fatalf("fresh install page preflight: %v", err)
	}
	if _, err := bridge.PreflightThemePackage(theme, extensions.DefaultThemeID); err != nil {
		t.Fatalf("replace-active-theme page preflight: %v", err)
	}
	// 3) 运行时激活的组件注册表预检：脚手架此前正是在这里被拒。
	components := extensionsruntime.NewComponentRegistry()
	if err := components.ValidateThemeTransition(&extension, nil); err != nil {
		t.Fatalf("runtime component registry preflight rejected the scaffold: %v", err)
	}
}

// TestThemeComponentSchemaDeclarationIsEnforcedByCliAndRuntime 固化 CLI 与激活路径
// 的一致性：组件 props/result schema 没有同包精确 packageFiles[] 声明时，
// manifest 校验（CLI validate/test/安装）与组件注册表编译（运行时激活）都必须拒绝。
// 之前只有后者会拒绝，所以作者看到的是「CLI 全绿、激活被拒 + 一句泛化文案」。
func TestThemeComponentSchemaDeclarationIsEnforcedByCliAndRuntime(t *testing.T) {
	root, manifest := makeThemeScaffold(t, "acme.scaffold-parity")
	extension := scaffoldExtension(t, root, manifest)

	componentID := extension.ID + ".component.home"
	schemaRef := extension.ID + ".component.home.props@1"
	manifest.Components = []extensions.ManifestComponent{{
		ID: componentID, ContractVersion: componentID + "@1", Action: extensionmanifest.ComponentActionAdd,
		SSRTemplate: manifest.Templates[0].ID, PropsSchema: schemaRef,
		ThemeOverrideKey: extension.ID + ".home",
	}}
	extension.Manifest = manifest

	cliErr := extensionmanifest.Validate(manifest)
	if cliErr == nil || !strings.Contains(cliErr.Error(), "has no exact packageFiles schema declaration") {
		t.Fatalf("CLI manifest validation must name the missing exact schema declaration, got %v", cliErr)
	}
	runtimeErr := extensionsruntime.NewComponentRegistry().ValidateThemeTransition(&extension, nil)
	if runtimeErr == nil || !strings.Contains(runtimeErr.Error(), "exact package schema declaration is missing") {
		t.Fatalf("runtime component registry must reject the same package, got %v", runtimeErr)
	}
}

// TestActiveThemeDoesNotRequireFullPageCoverage pin 住「运行时主题不需要覆盖全部核心页」
// 这一契约：只有 1 页的脚手架替换 34 页的默认主题时，页面预检必须通过。
// 未覆盖的核心页由 Host 回退渲染，而不是由主题覆盖率兜底。
func TestActiveThemeDoesNotRequireFullPageCoverage(t *testing.T) {
	root, manifest := makeThemeScaffold(t, "acme.scaffold-coverage")
	extension := scaffoldExtension(t, root, manifest)

	registry := pages.NewRegistry(pages.NewMemoryStore())
	bridge := pages.NewExtensionBridge(registry)
	defaultRoot := builtinDefaultThemeRoot(t)
	if _, err := os.Stat(filepath.Join(defaultRoot, "theme.json")); err != nil {
		t.Skipf("built-in default theme source unavailable: %v", err)
	}
	if err := bridge.RegisterThemePackage(context.Background(), pages.ThemeExtension{
		ID: extensions.DefaultThemeID, Version: "1.0.0", PackagePath: defaultRoot, PackageDigest: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("register built-in default theme: %v", err)
	}
	if _, err := bridge.PreflightThemePackage(pages.ThemeExtension{
		ID: extension.ID, Version: extension.Version,
		PackagePath: root, PackageDigest: extension.PackageDigest,
	}, extensions.DefaultThemeID); err != nil {
		t.Fatalf("replacing the full default theme must not require page coverage: %v", err)
	}
}

// TestMakeThemeScaffoldNoLongerDeclaresComponents 记录修复本身：脚手架不再生成
// components[]（其 propsSchema 是宿主核心页契约，主题无法声明同包精确 schema）。
func TestMakeThemeScaffoldNoLongerDeclaresComponents(t *testing.T) {
	_, manifest := makeThemeScaffold(t, "acme.scaffold-nocomponents")
	if len(manifest.Components) != 0 {
		t.Fatalf("theme scaffold must not declare components: %#v", manifest.Components)
	}
	if len(manifest.Templates) != 1 || len(manifest.PackageFiles) != 3 {
		body, _ := json.Marshal(manifest)
		t.Fatalf("unexpected scaffold shape: %s", body)
	}
}
