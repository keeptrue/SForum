package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestGeneratePluginScaffoldNonInteractive(t *testing.T) {
	target := filepath.Join(t.TempDir(), "plugin")
	created, err := GenerateExtensionScaffold(makeOptions{
		Kind:          "plugin",
		ID:            "acme.demo",
		Name:          "Acme Demo",
		Description:   "Demo plugin.",
		URL:           "https://example.com/acme-demo",
		AuthorName:    "Acme",
		AuthorURL:     "https://example.com",
		AuthorEmail:   "dev@example.com",
		Out:           target,
		NoInteraction: true,
		Backend:       true,
	})
	if err != nil {
		t.Fatalf("GenerateExtensionScaffold returned error: %v", err)
	}
	if created != target {
		t.Fatalf("expected target %q, got %q", target, created)
	}
	manifest := readGeneratedManifest(t, target)
	if manifest.Type != extensionmanifest.TypePlugin || manifest.Backend.Entry != "backend/plugin" {
		t.Fatalf("unexpected plugin manifest: %#v", manifest)
	}
	if extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 || manifest.Backend.Digest == "" || len(manifest.PackageFiles) != 1 {
		t.Fatalf("plugin scaffold must be exact-digest Manifest V3: %#v", manifest)
	}
	if len(manifest.PermissionDefinitions) != 1 || manifest.PermissionDefinitions[0].AssignmentPolicy != "host" {
		t.Fatalf("plugin permissions must remain Host-assigned: %#v", manifest.PermissionDefinitions)
	}
	if manifest.Admin.Entry != "/settings" || len(manifest.Admin.Pages) != 1 || manifest.Admin.Pages[0].Menu {
		t.Fatalf("expected v2 plugin admin settings page without sidebar menu: %#v", manifest.Admin)
	}
	if len(manifest.AdminPages) != 0 {
		t.Fatalf("new scaffolds should not use legacy adminPages: %#v", manifest.AdminPages)
	}
	if len(manifest.Contributions) != 0 {
		t.Fatalf("new plugin scaffolds should not enable demo contributions by default: %#v", manifest.Contributions)
	}
	if manifest.SettingsDocument.SchemaVersion != 1 || manifest.SettingsDocument.UI.Mode != extensionmanifest.SettingsUIModeSchema || manifest.SettingsDocument.UI.Layout != extensionmanifest.SettingsLayoutTabs {
		t.Fatalf("new scaffolds must default to versioned Schema UI: %#v", manifest.SettingsDocument)
	}
	readme, err := os.ReadFile(filepath.Join(target, "README.md"))
	if err != nil {
		t.Fatalf("read generated README: %v", err)
	}
	if !strings.Contains(string(readme), "forum.topic.actions") || !strings.Contains(string(readme), "\"contributions\"") {
		t.Fatalf("expected README to document contribution example, got:\n%s", readme)
	}
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("generated plugin manifest should validate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "backend", "plugin")); err != nil {
		t.Fatalf("expected backend stub: %v", err)
	}
}

func TestGenerateComplexPluginScaffold(t *testing.T) {
	target := filepath.Join(t.TempDir(), "complex-plugin")
	created, err := GenerateExtensionScaffold(makeOptions{
		Kind:          "plugin",
		ID:            "acme.complex",
		Name:          "Acme Complex",
		Description:   "Complex multi-file plugin.",
		URL:           "https://example.com/acme-complex",
		AuthorName:    "Acme",
		Out:           target,
		NoInteraction: true,
		Complex:       true,
	})
	if err != nil {
		t.Fatalf("GenerateExtensionScaffold complex: %v", err)
	}
	if created != target {
		t.Fatalf("target mismatch: %s", created)
	}
	// 必须通过 LoadPackage（解析 includes + settings 分片）。
	manifest, err := extensionmanifest.LoadPackage(target)
	if err != nil {
		t.Fatalf("LoadPackage generated complex package: %v", err)
	}
	if manifest.ID != "acme.complex" {
		t.Fatalf("id = %s", manifest.ID)
	}
	if extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 {
		t.Fatalf("complex scaffold contract = %s", extensionmanifest.ManifestContract(manifest))
	}
	if len(manifest.Settings) != 2 {
		t.Fatalf("expected 2 settings from shards, got %d", len(manifest.Settings))
	}
	if _, ok := manifest.Langs["zh-CN"]; !ok {
		t.Fatalf("expected zh-CN lang file, got %#v", manifest.Langs)
	}
	if manifest.Admin.Entry != "/settings" {
		t.Fatalf("admin: %#v", manifest.Admin)
	}
	if !manifest.SettingsDocument.Explicit || manifest.SettingsDocument.UI.Layout != extensionmanifest.SettingsLayoutTabs {
		t.Fatalf("complex scaffold must use one versioned Settings Document: %#v", manifest.SettingsDocument)
	}
	rootBody, err := os.ReadFile(filepath.Join(target, extensionmanifest.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rootBody), `"includes"`) {
		t.Fatalf("root should declare includes: %s", rootBody)
	}
	// includes 值会包含 "settings" 路径；禁止的是根级 settings 数组字段。
	if strings.Contains(string(rootBody), `"settings": [`) {
		t.Fatalf("root should not inline settings array when complex: %s", rootBody)
	}
}

func TestGeneratePrebuiltProviderSettingsScaffold(t *testing.T) {
	target := filepath.Join(t.TempDir(), "prebuilt-plugin")
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind: "plugin", ID: "acme.prebuilt", Name: "Acme Prebuilt", Description: "Prebuilt settings.",
		URL: "https://example.com/prebuilt", AuthorName: "Acme", Out: target, NoInteraction: true,
		Backend: true, PrebuiltSettings: true, ProviderSlot: "mail.provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readGeneratedManifest(t, target)
	component := manifest.SettingsDocument.UI.Component
	if component == nil || manifest.SettingsDocument.UI.Mode != extensionmanifest.SettingsUIModeComponent || component.Entry != "frontend/admin/dist/settings.mjs" {
		t.Fatalf("missing prebuilt component contract: %#v", manifest.SettingsDocument)
	}
	if len(manifest.SettingsDocument.Actions) != 1 || manifest.SettingsDocument.Actions[0].Kind != extensionmanifest.SettingsActionProviderProbe || len(manifest.Providers) != 1 {
		t.Fatalf("missing provider probe scaffold: actions=%#v providers=%#v", manifest.SettingsDocument.Actions, manifest.Providers)
	}
	if manifest.Providers[0].ID == "" || manifest.Providers[0].ContractVersion == "" || manifest.Providers[0].Handler == "" || len(manifest.PackageFiles) != 3 {
		t.Fatalf("prebuilt V3 provider declarations are incomplete: provider=%#v files=%#v", manifest.Providers[0], manifest.PackageFiles)
	}
	for _, relative := range []string{"frontend/admin/dist/settings.mjs", "frontend/admin/dist/settings.css"} {
		if _, err := os.Stat(filepath.Join(target, relative)); err != nil {
			t.Fatalf("missing prebuilt file %s: %v", relative, err)
		}
	}
	if _, err := extensionmanifest.LoadPackage(target); err != nil {
		t.Fatalf("generated prebuilt package must validate: %v", err)
	}
}

func TestGenerateVueAdminPageScaffold(t *testing.T) {
	target := filepath.Join(t.TempDir(), "vue-admin-plugin")
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind: "plugin", ID: "acme.dashboard", Name: "Acme Dashboard", Description: "Vue admin page.",
		URL: "https://example.com/dashboard", AuthorName: "Acme", Out: target, NoInteraction: true,
		VueAdminPage: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readGeneratedManifest(t, target)
	if manifest.Admin.Entry != "/dashboard" || len(manifest.Admin.Pages) != 2 {
		t.Fatalf("unexpected Vue admin declaration: %#v", manifest.Admin)
	}
	page := manifest.Admin.Pages[0]
	if page.View != "component" || !page.Menu || page.Permission != "acme.dashboard.manage" || page.Component == nil {
		t.Fatalf("dashboard page is incomplete: %#v", page)
	}
	if page.Component.Entry != "frontend/admin/dist/dashboard.mjs" || page.Component.CSS != "frontend/admin/dist/dashboard.css" {
		t.Fatalf("dashboard artifacts are incomplete: %#v", page.Component)
	}
	if len(manifest.PackageFiles) != 2 {
		t.Fatalf("expected exact dashboard artifacts, got %#v", manifest.PackageFiles)
	}
	for _, relative := range []string{
		"frontend/admin/package.json",
		"frontend/admin/tsconfig.json",
		"frontend/admin/vite.config.ts",
		"frontend/admin/src/admin.ts",
		"frontend/admin/src/AdminDashboard.vue",
		"frontend/admin/dist/dashboard.mjs",
		"frontend/admin/dist/dashboard.css",
	} {
		if _, err := os.Stat(filepath.Join(target, relative)); err != nil {
			t.Fatalf("missing Vue authoring file %s: %v", relative, err)
		}
	}
	viteConfig, err := os.ReadFile(filepath.Join(target, "frontend", "admin", "vite.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(viteConfig), "emptyOutDir: false") || !strings.Contains(string(viteConfig), "dashboard.mjs") {
		t.Fatalf("Vite output must preserve sibling admin components: %s", viteConfig)
	}
	adminEntry, err := os.ReadFile(filepath.Join(target, "frontend", "admin", "src", "admin.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// 图标解析器由 SDK 的 definePluginPage 注入；作者手写 createApp 会让图标静默消失。
	if !strings.Contains(string(adminEntry), "definePluginPage") || strings.Contains(string(adminEntry), "createApp(") {
		t.Fatalf("Vue admin entry must mount through the SDK adapter: %s", adminEntry)
	}
	dashboard, err := os.ReadFile(filepath.Join(target, "frontend", "admin", "src", "AdminDashboard.vue"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dashboard), "i-lucide-") {
		t.Fatalf("reference page must demonstrate host icons: %s", dashboard)
	}
	adminPackage, err := os.ReadFile(filepath.Join(target, "frontend", "admin", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 期望版本从真实 SDK 的 package.json 读，不写死：写死的话每次 SDK 升版都会
	// 在别处炸一次（v3.0.17 的 release 就是被同一个错配挡下的）。
	for _, dependency := range []string{"@sforum/admin-sdk", "@sforum/plugin-ui"} {
		want := fmt.Sprintf("%q: %q", dependency, "^"+sdkPackageVersion(t, dependency))
		if !strings.Contains(string(adminPackage), want) {
			t.Fatalf("author workspace must depend on %s: %s", want, adminPackage)
		}
	}
	readme, err := os.ReadFile(filepath.Join(target, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "extension build --allow-scaffold") || !strings.Contains(string(readme), "--skip-install") {
		t.Fatalf("missing Vue build loop: %s", readme)
	}
}

func TestGenerateComplexVueAdminAndPrebuiltSettingsScaffold(t *testing.T) {
	target := filepath.Join(t.TempDir(), "complex-vue-admin-plugin")
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind: "plugin", ID: "acme.combined", Name: "Acme Combined", Description: "Combined admin surfaces.",
		URL: "https://example.com/combined", AuthorName: "Acme", Out: target, NoInteraction: true,
		Complex: true, PrebuiltSettings: true, VueAdminPage: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readGeneratedManifest(t, target)
	if manifest.Admin.Entry != "/dashboard" || len(manifest.Admin.Pages) != 2 {
		t.Fatalf("complex admin include lost Vue page: %#v", manifest.Admin)
	}
	if manifest.SettingsDocument.UI.Component == nil || len(manifest.PackageFiles) != 4 {
		t.Fatalf("combined scaffold lost exact artifacts: settings=%#v files=%#v", manifest.SettingsDocument, manifest.PackageFiles)
	}
}

func TestGenerateVueAdminPageRejectsTheme(t *testing.T) {
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind: "theme", ID: "acme.invalid", Name: "Invalid", Description: "Invalid Vue page.",
		URL: "https://example.com/invalid", AuthorName: "Acme", Out: filepath.Join(t.TempDir(), "theme"),
		NoInteraction: true, VueAdminPage: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires a plugin") {
		t.Fatalf("expected plugin-only validation, got %v", err)
	}
}

func TestGenerateThemeScaffoldNonInteractive(t *testing.T) {
	target := filepath.Join(t.TempDir(), "theme")
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind:          "theme",
		ID:            "acme.theme",
		Name:          "Acme Theme",
		Description:   "Demo theme.",
		URL:           "https://example.com/acme-theme",
		AuthorName:    "Acme",
		AuthorURL:     "https://example.com",
		Out:           target,
		NoInteraction: true,
	})
	if err != nil {
		t.Fatalf("GenerateExtensionScaffold returned error: %v", err)
	}
	manifest := readGeneratedManifest(t, target)
	if manifest.Type != extensionmanifest.TypeTheme {
		t.Fatalf("unexpected theme manifest: %#v", manifest)
	}
	// 主题脚手架不得声明 components[]：组件 propsSchema 是宿主核心页契约，
	// 主题无法提供同包精确 schema 声明，声明组件会让包必然激活失败。详见
	// theme_scaffold_activation_test.go。
	if extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 || len(manifest.Templates) != 1 || len(manifest.Components) != 0 || len(manifest.Assets) != 2 || len(manifest.PackageFiles) != 3 {
		t.Fatalf("theme scaffold must declare V3 presentation contracts: %#v", manifest)
	}
	if manifest.Admin.Entry != "/settings" || len(manifest.Admin.Pages) != 1 || len(manifest.Settings) == 0 {
		t.Fatalf("expected theme v2 admin page and settings declarations: %#v", manifest)
	}
	if len(manifest.AdminPages) != 0 {
		t.Fatalf("new theme scaffolds should not use legacy adminPages: %#v", manifest.AdminPages)
	}
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("generated theme manifest should validate: %v", err)
	}
	for _, rel := range []string{"theme.json", "assets/theme.css", "templates/home.html"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Fatalf("expected runtime theme file %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "layer", "nuxt.config.ts")); err == nil {
		t.Fatal("runtime theme scaffold must not create Nuxt layer")
	}
}

func readGeneratedManifest(t *testing.T, dir string) extensionmanifest.Manifest {
	t.Helper()
	// 统一走 LoadPackage，兼容单文件与 complex includes。
	manifest, err := extensionmanifest.LoadPackage(dir)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	return manifest
}

func TestExtensionValidateCommand(t *testing.T) {
	// 使用临时构建的 SMTP 包，避免 gitignored backend/plugin 摘要漂移。
	smtpRoot := prepareBuiltinPluginPackage(t, "sforum-smtp")

	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "validate", smtpRoot})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("extension validate: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "sforum.smtp") {
		t.Fatalf("expected id in output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "zh-CN") {
		t.Fatalf("expected langs in output:\n%s", out.String())
	}
	// 内置 SMTP 已迁到 Manifest V3；validate 输出应报告 sforum.manifest@3。
	if !strings.Contains(out.String(), "contract: sforum.manifest@3") {
		t.Fatalf("expected manifest contract in output:\n%s", out.String())
	}
}

// 没有 packageFiles 的包（只声明 routes/events 契约、不携带包内文件）也必须能跑作者循环。
// 修正（2026-10-16）：`digest --write` 此前对它直接报
// “Manifest V3 packageFiles must be inline for digest refresh”，而同一个包在
// `extension validate` / `extension test` 下是绿的——作者按文档跑 digest 就撞墙。
func TestExtensionDigestCommandAcceptsPackageWithoutPackageFiles(t *testing.T) {
	target := filepath.Join(t.TempDir(), "contract-only")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "manifestVersion": 3,
  "id": "acme.contract-only",
  "name": "Acme Contract Only",
  "description": "Package with contract declarations and no package files.",
  "url": "https://example.com/contract-only",
  "author": {"name": "Acme"},
  "version": "1.0.0",
  "type": "plugin",
  "sforumVersion": ">=0.1.0",
  "events": [{
    "id": "acme.contract-only.event.topic-created",
    "contractVersion": "acme.contract-only.event.topic-created@1",
    "name": "topic.created", "kind": "observe",
    "handler": "acme.contract-only.event.topic-created",
    "inputSchema": "acme.contract-only.event.topic-created.input@1",
    "resultSchema": "acme.contract-only.event.topic-created.result@1"
  }]
}
`
	if err := os.WriteFile(filepath.Join(target, extensionmanifest.ManifestFileName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extensionmanifest.LoadPackage(target); err != nil {
		t.Fatalf("fixture manifest must be valid: %v", err)
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "digest", "--write", target})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("digest refresh on a package without package files: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "no package files declared") {
		t.Fatalf("expected an explicit no-op message, got: %s", out.String())
	}
}

func TestExtensionDigestCommandRefreshesGeneratedArtifact(t *testing.T) {
	target := filepath.Join(t.TempDir(), "digest-plugin")
	_, err := GenerateExtensionScaffold(makeOptions{
		Kind: "plugin", ID: "acme.digest", Name: "Acme Digest", Description: "Digest refresh.",
		URL: "https://example.com/digest", AuthorName: "Acme", Out: target, NoInteraction: true, Backend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := readGeneratedManifest(t, target).Backend.Digest
	if err := os.WriteFile(filepath.Join(target, "backend", "plugin"), []byte("replacement binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := extensionmanifest.LoadPackage(target); err == nil {
		t.Fatal("changed bytes must make the old manifest stale")
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "digest", "--write", target})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("digest refresh: %v\n%s", err, out.String())
	}
	after := readGeneratedManifest(t, target).Backend.Digest
	if after == "" || after == before || !strings.Contains(out.String(), "updated ") {
		t.Fatalf("digest not refreshed: before=%s after=%s output=%s", before, after, out.String())
	}
}

// sdkPackageVersion 读 apps/web/packages/<name> 的真实版本，作为脚手架的期望值。
func sdkPackageVersion(t *testing.T, name string) string {
	t.Helper()
	manifest := filepath.Join("..", "..", "..", "web", "packages", strings.TrimPrefix(name, "@sforum/"), "package.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read %s manifest: %v", name, err)
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode %s manifest: %v", name, err)
	}
	if parsed.Version == "" {
		t.Fatalf("%s manifest has no version", name)
	}
	return parsed.Version
}
