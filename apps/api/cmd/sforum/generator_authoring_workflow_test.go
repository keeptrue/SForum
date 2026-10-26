package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

// M3 契约测试：生成的公开页/中间件脚手架必须**无需手工编辑**就通过 CLI 校验、
// 包契约测试与运行时激活预检。这些都是长期契约，不是一次性脚本。

func authoringScaffold(t *testing.T, opts makeOptions) (string, extensionmanifest.Manifest) {
	t.Helper()
	opts.NoInteraction = true
	opts.Name = "M3 Scaffold Probe"
	opts.Description = "M3 authoring workflow probe."
	opts.URL = "https://example.com/m3"
	opts.AuthorName = "Acme"
	opts.AuthorURL = "https://example.com"
	opts.AuthorEmail = "dev@example.com"
	target := filepath.Join(t.TempDir(), opts.ID)
	opts.Out = target
	if _, err := GenerateExtensionScaffold(opts); err != nil {
		t.Fatalf("GenerateExtensionScaffold: %v", err)
	}
	manifest, err := extensionmanifest.LoadPackage(target)
	if err != nil {
		t.Fatalf("generated package failed manifest load: %v", err)
	}
	return target, manifest
}

// TestPluginPublicPageScaffoldPassesContractAndRuntimePreflight 覆盖 M3 出口门禁：
// 全新脚手架通过包契约测试（= `extension test`）且精确模板运行时预检通过
// （= 激活路径会跑的同一套校验）。
func TestPluginPublicPageScaffoldPassesContractAndRuntimePreflight(t *testing.T) {
	root, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-plugin", Backend: true,
		PublicPage: "docs", PublicPageLayout: "site", RouteMiddleware: true,
	})
	report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{SkipBackendBinary: true})
	if err != nil {
		t.Fatalf("contract test: %v", err)
	}
	if !report.OK || report.Errors != 0 {
		t.Fatalf("scaffold must pass its own contract test: errors=%d checks=%#v", report.Errors, report.Checks)
	}
	if err := pluginsdk.PreflightExactTemplateRuntime(root, manifest); err != nil {
		t.Fatalf("exact template runtime preflight: %v", err)
	}
}

// TestPluginPublicPageScaffoldDeclaresExactThreeWayIdentity 固定三方身份契约：
// theme.json 的 template 路径 ↔ 清单 templates[] ↔ packageFiles[]，摘要一致，
// 且 contract 与 viewModelSchema 是同一个精确引用。
func TestPluginPublicPageScaffoldDeclaresExactThreeWayIdentity(t *testing.T) {
	root, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-identity", Backend: true, PublicPage: "release-notes",
	})
	theme := readGeneratedThemeJSON(t, root)
	if len(theme.Pages) != 1 {
		t.Fatalf("expected one generated page, got %#v", theme.Pages)
	}
	page := theme.Pages[0]
	if page.Path != "/release-notes" || page.Layout != "site" || page.Access != "public" || page.Action != "add" {
		t.Fatalf("generated page mapping = %#v", page)
	}
	if page.Template != "templates/page-release-notes.html" {
		t.Fatalf("template path = %q", page.Template)
	}
	var declared *extensionmanifest.ManifestTemplate
	for index := range manifest.Templates {
		if manifest.Templates[index].Path == page.Template {
			declared = &manifest.Templates[index]
		}
	}
	if declared == nil {
		t.Fatalf("template %q missing from templates[]", page.Template)
	}
	if declared.ViewModelSchema != page.Contract {
		t.Fatalf("viewModelSchema %q must equal contract %q", declared.ViewModelSchema, page.Contract)
	}
	var file *extensionmanifest.ManifestPackageFile
	for index := range manifest.PackageFiles {
		if manifest.PackageFiles[index].Path == page.Template {
			file = &manifest.PackageFiles[index]
		}
	}
	if file == nil || file.Kind != "template" || file.Digest != declared.Digest {
		t.Fatalf("packageFiles declaration = %#v (template digest %q)", file, declared.Digest)
	}
}

// TestSchemaPageScaffoldPassesSharedPreflight 覆盖 M9 出口门禁：
// `--schema-page` 生成的描述符 + 契约必须直接通过包契约测试与精确运行时预检
// （= 激活路径会跑的同一套校验），无需手工编辑。
func TestSchemaPageScaffoldPassesSharedPreflight(t *testing.T) {
	for _, kind := range []string{"list", "detail", "form", "notice"} {
		t.Run(kind, func(t *testing.T) {
			root, manifest := authoringScaffold(t, makeOptions{
				Kind: "plugin", ID: "acme.m9-schema-" + kind,
				SchemaPage: "orders", SchemaPageKind: kind,
			})
			report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{SkipBackendBinary: true})
			if err != nil {
				t.Fatalf("contract test: %v", err)
			}
			if !report.OK || report.Errors != 0 {
				t.Fatalf("schema scaffold must pass its own contract test: errors=%d checks=%#v", report.Errors, report.Checks)
			}
			if err := pluginsdk.PreflightExactTemplateRuntime(root, manifest); err != nil {
				t.Fatalf("exact runtime preflight: %v", err)
			}
		})
	}
}

// TestSchemaPageScaffoldDeclaresBoundedDescriptorAndContract 固定声明形状：
// theme.json 只给两个 id，packageFiles[] 里两者都是 kind=schema 且带 version，
// contract 是 `<schemaFileId>@1` 精确引用（宿主按精确制品匹配）。
func TestSchemaPageScaffoldDeclaresBoundedDescriptorAndContract(t *testing.T) {
	root, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m9-schema-shape", SchemaPage: "orders",
	})
	theme := readGeneratedThemeJSON(t, root)
	if len(theme.Pages) != 1 {
		t.Fatalf("expected one generated page, got %#v", theme.Pages)
	}
	page := theme.Pages[0]
	if page.View != "schema" {
		t.Fatalf("generated page view = %q; want schema", page.View)
	}
	if page.Template != "" {
		t.Fatalf("view=schema must not declare a template, got %q", page.Template)
	}
	descriptorID := "acme.m9-schema-shape.file.page-orders"
	contractRef := "acme.m9-schema-shape.file.page-orders-contract@1"
	if page.Schema == nil || page.Schema.Descriptor != descriptorID || page.Schema.Contract != contractRef {
		t.Fatalf("generated schema declaration = %#v", page.Schema)
	}
	declared := map[string]string{}
	for _, file := range manifest.PackageFiles {
		if file.Kind == "schema" {
			declared[file.ID] = file.Version
		}
	}
	if declared[descriptorID] != "1" || declared["acme.m9-schema-shape.file.page-orders-contract"] != "1" {
		t.Fatalf("schema packageFiles must be versioned kind=schema: %#v", declared)
	}
	// 描述符文件必须真的在包里，且内容是有界描述符。
	body, err := os.ReadFile(filepath.Join(root, "schemas", "page-orders.json"))
	if err != nil {
		t.Fatalf("descriptor file: %v", err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(body, &descriptor); err != nil {
		t.Fatalf("descriptor JSON: %v", err)
	}
	if descriptor["kind"] != "list" {
		t.Fatalf("descriptor kind = %v; want list", descriptor["kind"])
	}
}

// TestSchemaPageScaffoldRejectsUnsafeFlags 覆盖提前拒绝：非法种类、缺 slug、
// 与 --public-page 共用 slug，都必须在写盘前失败（否则激活期才报错）。
func TestSchemaPageScaffoldRejectsUnsafeFlags(t *testing.T) {
	cases := []struct {
		name string
		opts makeOptions
		want string
	}{
		{
			name: "kind without slug",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", SchemaPageKind: "notice"},
			want: "schema-page-kind requires --schema-page",
		},
		{
			name: "unknown kind",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", SchemaPage: "orders", SchemaPageKind: "dashboard"},
			want: "schema-page-kind must be one of",
		},
		{
			name: "unsafe slug",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", SchemaPage: "../secrets"},
			want: "schema-page must be a lowercase slug",
		},
		{
			name: "slug shared with public-page",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", PublicPage: "docs", SchemaPage: "docs"},
			want: "cannot share the slug",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateWorkflowOptions(normalizeWorkflowOptions(testCase.opts))
			if err == nil {
				t.Fatal("unsafe schema-page flags were accepted")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not mention %q", err, testCase.want)
			}
		})
	}
}

// TestThemePublicPageScaffoldPassesRuntimeThemePreflight 证明主题脚手架（含新增公开页）
// 能过**运行时激活**用的页面预检，而不仅是 CLI 校验：全新安装与替换既有活动主题两种
// 最终状态都必须通过，且新页默认/显式 layout 被如实写入 theme.json。
func TestThemePublicPageScaffoldPassesRuntimeThemePreflight(t *testing.T) {
	for _, test := range []struct {
		name       string
		layout     string
		wantLayout string
		expectBare bool
	}{
		{name: "default site", layout: "", wantLayout: "site"},
		{name: "explicit bare", layout: "bare", wantLayout: "bare", expectBare: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, manifest := authoringScaffold(t, makeOptions{
				Kind: "theme", ID: "acme.m3-theme", PublicPage: "notes", PublicPageLayout: test.layout,
			})
			theme := readGeneratedThemeJSON(t, root)
			if len(theme.Pages) != 2 {
				t.Fatalf("theme scaffold must keep its home page and add one page: %#v", theme.Pages)
			}
			generated := theme.Pages[1]
			if generated.Layout != test.wantLayout || generated.Path != "/notes" || generated.Access != "public" {
				t.Fatalf("generated page = %#v", generated)
			}
			if (generated.Layout == "bare") != test.expectBare {
				t.Fatalf("bare layout not honoured: %#v", generated)
			}
			bridge := pages.NewExtensionBridge(pages.NewRegistry(pages.NewMemoryStore()))
			ext := pages.ThemeExtension{
				ID: manifest.ID, Version: manifest.Version,
				PackagePath: root, PackageDigest: strings.Repeat("a", 64),
			}
			if _, err := bridge.PreflightThemePackage(ext, ""); err != nil {
				t.Fatalf("fresh install preflight: %v", err)
			}
			if _, err := bridge.PreflightThemePackage(ext, extensions.DefaultThemeID); err != nil {
				t.Fatalf("replace-active-theme preflight: %v", err)
			}
		})
	}
}

// TestMakeScaffoldRejectsUnsafeAuthoringFlags 固定「不安全的组合必须给出可理解的拒绝」，
// 而不是生成一个到运行时才失败的包。
func TestMakeScaffoldRejectsUnsafeAuthoringFlags(t *testing.T) {
	for _, test := range []struct {
		name string
		opts makeOptions
		want string
	}{
		{
			name: "layout without page",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", PublicPageLayout: "bare"},
			want: "public-page-layout requires --public-page",
		},
		{
			name: "unknown layout",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", PublicPage: "docs", PublicPageLayout: "chrome"},
			want: "public-page-layout must be site or bare",
		},
		{
			name: "path escape slug",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", PublicPage: "../secrets"},
			want: "public-page must be a lowercase slug",
		},
		{
			name: "middleware without backend",
			opts: makeOptions{Kind: "plugin", ID: "acme.a", RouteMiddleware: true},
			want: "route-middleware requires a plugin scaffold with --backend",
		},
		{
			name: "middleware on theme",
			opts: makeOptions{Kind: "theme", ID: "acme.a", RouteMiddleware: true, Backend: true},
			want: "route-middleware requires a plugin scaffold with --backend",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := test.opts
			opts.Name = "Reject Probe"
			opts.Description = "Reject probe."
			opts.URL = "https://example.com/reject"
			opts.AuthorName = "Acme"
			opts.NoInteraction = true
			opts.Out = filepath.Join(t.TempDir(), opts.ID)
			_, err := GenerateExtensionScaffold(opts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v; want containing %q", err, test.want)
			}
			if _, statErr := os.Stat(opts.Out); statErr == nil {
				t.Fatalf("rejected scaffold must not leave a package behind at %s", opts.Out)
			}
		})
	}
}

// TestAuthoringFlagsComposeWithExistingScaffoldOptions 覆盖 M3 任务 6：
// 新 flag 与 backend / 多文件 manifest / 预构建设置 / Vue 后台页 / provider slot
// 组合时仍然生成一个可加载、声明不重复的包（不引入第二个生成分支）。
func TestAuthoringFlagsComposeWithExistingScaffoldOptions(t *testing.T) {
	root, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-combo", Backend: true, Complex: true,
		PrebuiltSettings: true, VueAdminPage: true, ProviderSlot: "mail.provider",
		PublicPage: "docs", PublicPageLayout: "site", RouteMiddleware: true,
	})
	if err := pluginsdk.PreflightExactTemplateRuntime(root, manifest); err != nil {
		t.Fatalf("exact template runtime preflight: %v", err)
	}
	if len(manifest.Routes) != 2 || len(manifest.Providers) != 1 {
		t.Fatalf("declared routes/providers = %d/%d", len(manifest.Routes), len(manifest.Providers))
	}
	if manifest.SettingsDocument.UI.Mode != extensionmanifest.SettingsUIModeComponent ||
		manifest.SettingsDocument.UI.Component == nil {
		t.Fatalf("prebuilt settings must keep component mode: %#v", manifest.SettingsDocument.UI)
	}
	// Vue 后台页会把 admin entry 指向 /dashboard，并保留一个 settings 页；
	// 这里只固定「组合后仍然是一份合法的 admin 声明」。
	if manifest.Admin.Entry == "" || len(manifest.Admin.Pages) != 2 {
		t.Fatalf("admin declaration = %#v", manifest.Admin)
	}
	componentPages := 0
	for _, page := range manifest.Admin.Pages {
		if page.View == "component" {
			componentPages++
		}
	}
	if componentPages != 1 {
		t.Fatalf("vue admin page missing from the combined scaffold: %#v", manifest.Admin.Pages)
	}
	ids := map[string]bool{}
	for _, file := range manifest.PackageFiles {
		if ids[file.ID] {
			t.Fatalf("duplicate packageFiles id %q", file.ID)
		}
		ids[file.ID] = true
	}
	for _, template := range manifest.Templates {
		if ids[template.ID] {
			t.Fatalf("duplicate template id %q", template.ID)
		}
		ids[template.ID] = true
	}
	var docsDeclared bool
	for _, template := range manifest.Templates {
		if template.Path == "templates/page-docs.html" {
			docsDeclared = true
		}
	}
	if !docsDeclared {
		t.Fatalf("public page template lost in the complex manifest: %#v", manifest.Templates)
	}
}

// TestPublicPageSlugIsNormalizedNotDuplicated 记录 slug 的归一化语义：与既有
// `--id` 一致，大小写与空白被归一化为小写 slug；非法字符仍然拒绝（见上表驱动测试）。
func TestPublicPageSlugIsNormalizedNotDuplicated(t *testing.T) {
	root, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-normalize", Backend: true, PublicPage: "  Docs  ",
	})
	if manifest.ID != "acme.m3-normalize" {
		t.Fatalf("id drifted: %q", manifest.ID)
	}
	if len(manifest.Templates) != 1 || manifest.Templates[0].Path != "templates/page-docs.html" {
		t.Fatalf("slug must normalize to templates/page-docs.html: %#v", manifest.Templates)
	}
	if _, err := os.Stat(filepath.Join(root, "templates", "page-docs.html")); err != nil {
		t.Fatalf("normalized template file missing: %v", err)
	}
	theme := readGeneratedThemeJSON(t, root)
	if len(theme.Pages) != 1 || theme.Pages[0].Path != "/docs" {
		t.Fatalf("normalized page mapping = %#v", theme.Pages)
	}
}

// TestRouteMiddlewareScaffoldNeverRequestsRawAuthority 固定 M3 的安全出口门禁：
// 生成的中间件只继承目标路由授权，只写清单声明的可变字段，不声明 access/permission，
// 也不使用 raw_request。
func TestRouteMiddlewareScaffoldNeverRequestsRawAuthority(t *testing.T) {
	_, manifest := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-middleware", Backend: true, RouteMiddleware: true,
	})
	if len(manifest.Routes) != 2 {
		t.Fatalf("expected before+after routes, got %#v", manifest.Routes)
	}
	seen := map[string]bool{}
	for _, route := range manifest.Routes {
		seen[route.Action] = true
		if route.Guard != extensionmanifest.GuardCoreInherit {
			t.Fatalf("route %s guard = %q; middleware must inherit the target guard", route.ID, route.Guard)
		}
		if route.TargetID != "core.route.forum.topics" || route.Fallback != "closed" {
			t.Fatalf("route %s target/fallback = %q/%q", route.ID, route.TargetID, route.Fallback)
		}
		if route.Access != "" || route.Permission != "" {
			t.Fatalf("route %s must not restate access/permission: %#v", route.ID, route)
		}
		switch route.Action {
		case "before":
			if len(route.MutableResponseFields) != 0 || len(route.MutableRequestFields) == 0 {
				t.Fatalf("before route may only patch declared request fields: %#v", route)
			}
		case "after":
			if len(route.MutableRequestFields) != 0 || len(route.MutableResponseFields) == 0 {
				t.Fatalf("after route may only patch declared response fields: %#v", route)
			}
		}
	}
	if !seen["before"] || !seen["after"] {
		t.Fatalf("missing before/after route: %#v", seen)
	}
}

// TestRouteMiddlewareExampleHandlerIsInertUntilMerged 记录示例处理器的形态：
// 它带 //go:build ignore，因此不会进入作者的构建，也不会伪装成可执行后端。
func TestRouteMiddlewareExampleHandlerIsInertUntilMerged(t *testing.T) {
	root, _ := authoringScaffold(t, makeOptions{
		Kind: "plugin", ID: "acme.m3-example", Backend: true, RouteMiddleware: true,
	})
	raw, err := os.ReadFile(filepath.Join(root, "backend", "routes_example.go"))
	if err != nil {
		t.Fatalf("example handler missing: %v", err)
	}
	body := string(raw)
	if !strings.HasPrefix(body, "//go:build ignore") {
		t.Fatalf("example handler must stay out of the author build:\n%s", body[:120])
	}
	for _, want := range []string{"beforeRouteID", "afterRouteID", "RequestPatch", "ResponsePatch"} {
		if !strings.Contains(body, want) {
			t.Fatalf("example handler missing %q", want)
		}
	}
}

type scaffoldThemeJSON struct {
	Pages []struct {
		ID       string `json:"id"`
		Action   string `json:"action"`
		Path     string `json:"path"`
		Template string `json:"template"`
		Contract string `json:"contract"`
		Access   string `json:"access"`
		Layout   string `json:"layout"`
		View     string `json:"view"`
		Schema   *struct {
			Descriptor string `json:"descriptor"`
			Contract   string `json:"contract"`
		} `json:"schema"`
	} `json:"pages"`
}

func readGeneratedThemeJSON(t *testing.T, root string) scaffoldThemeJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed scaffoldThemeJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}
