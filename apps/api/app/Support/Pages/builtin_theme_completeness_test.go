package pages

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// TestBuiltinThemesCoverAllThemeablePages is the P13 reference-theme gate:
// the protected default theme must declare every themeable Page Registry id, ship
// the template file, embed the required host body island, and compile.
func TestBuiltinThemesCoverAllThemeablePages(t *testing.T) {
	repoRoot := themeCompletenessRepoRoot(t)
	for _, themeRel := range []string{
		"extensions/builtin/themes/sforum-default",
	} {
		themeRel := themeRel
		t.Run(themeRel, func(t *testing.T) {
			root := filepath.Join(repoRoot, themeRel)
			pkg, err := LoadThemePackage(root)
			if err != nil {
				t.Fatalf("load theme package: %v", err)
			}
			for location, island := range themeNavigationLocationBindings {
				if pkg.NavigationLocations[location] != island {
					t.Fatalf("navigation location %s=%q want %q", location, pkg.NavigationLocations[location], island)
				}
				if _, allowed := allowedHostIslands[island]; !allowed {
					t.Fatalf("navigation island %s is not template-allowlisted", island)
				}
				if productionThemeIslandBindings()[island].ComponentID == "" {
					t.Fatalf("navigation island %s has no production binding", island)
				}
			}
			byTarget := make(map[string]ThemePageDecl, len(pkg.Pages))
			for _, page := range pkg.Pages {
				if strings.EqualFold(strings.TrimSpace(page.Action), string(ActionReplace)) {
					byTarget[strings.TrimSpace(page.Target)] = page
				}
			}
			islands := productionThemeIslandBindings()
			compiler := themecompiler.NewCompiler(themecompiler.Limits{})
			bindings := themecompiler.Bindings{
				BindingRevision: strings.Repeat("c", 64),
				Islands:         islands,
				SiteName:        "SForum",
				PageViewModels:  map[string]themecompiler.PageTemplateBinding{},
			}
			// 仅编译 theme.json 声明的页模板；plugin override 不走 Page ViewModel 绑定。
			selected := map[string]struct{}{}
			for _, page := range Catalog() {
				if !page.Themeable {
					continue
				}
				decl, ok := byTarget[page.ID]
				if !ok {
					t.Fatalf("missing replace declaration for %s", page.ID)
				}
				if strings.TrimSpace(decl.Template) == "" {
					t.Fatalf("%s has empty template path", page.ID)
				}
				if strings.TrimSpace(decl.Contract) != page.ContractVersion {
					t.Fatalf("%s contract=%q want %q", page.ID, decl.Contract, page.ContractVersion)
				}
				absTemplate := filepath.Join(root, filepath.FromSlash(decl.Template))
				raw, err := os.ReadFile(absTemplate)
				if err != nil {
					t.Fatalf("read %s: %v", decl.Template, err)
				}
				tag := RequiredThemeBodyIslandTag(page.ID)
				if tag == "" {
					t.Fatalf("no body island mapping for themeable page %s", page.ID)
				}
				if !strings.Contains(string(raw), "<"+tag) {
					t.Fatalf("%s template %s missing required island <%s>", page.ID, decl.Template, tag)
				}
				// L1 壳层必须声明主题呈现所有权，避免宿主路由继续承载默认产品布局。
				if !strings.Contains(string(raw), `data-theme-owned="presentation"`) {
					t.Fatalf("%s template %s missing data-theme-owned=presentation", page.ID, decl.Template)
				}
				// 发帖/编辑属于同一 composer 版式家族；只验证岛存在不足以防止
				// 外层主题壳漂移，必须同时声明同一三栏布局契约。
				if page.ID == "forum.topic.create" || page.ID == "forum.topic.edit" {
					if !strings.Contains(string(raw), "sf-theme-shell--fullwidth-3col") {
						t.Fatalf("%s template %s missing fullwidth-3col shell class", page.ID, decl.Template)
					}
					if !strings.Contains(string(raw), `data-layout="fullwidth-3col"`) {
						t.Fatalf("%s template %s missing fullwidth-3col layout marker", page.ID, decl.Template)
					}
				}
				// 非 auth / not_found 公开页应在 L1 挂载导航与页脚岛（auth 用 auth layout；not_found 自带 chrome）。
				if !strings.HasPrefix(page.ID, "auth.") && page.ID != "system.not_found" {
					if !strings.Contains(string(raw), "<sf-navbar") {
						t.Fatalf("%s template %s missing <sf-navbar> chrome island", page.ID, decl.Template)
					}
					if !strings.Contains(string(raw), "<sf-footer") {
						t.Fatalf("%s template %s missing <sf-footer> chrome island", page.ID, decl.Template)
					}
				}
				bindings.PageViewModels[decl.Template] = themecompiler.PageTemplateBinding{
					PageID: page.ID, SchemaVersion: page.ContractVersion,
				}
				selected[decl.Template] = struct{}{}
			}
			// Chrome 复用由各模板内联的 <sf-navbar>/<sf-footer> 岛完成；
			// 不再要求 dead layouts/base 与 chrome-start/end partials。
			digest := strings.Repeat("a", 64)
			snapshot, err := compiler.CompileFS(selectedThemeFS{FS: os.DirFS(root), selected: selected}, digest, bindings)
			if err != nil {
				t.Fatalf("compile theme package: %v", err)
			}
			for _, page := range Catalog() {
				if !page.Themeable {
					continue
				}
				decl := byTarget[page.ID]
				model, err := BuildCorePageViewModel(CorePageViewModelRequest{
					PageID: page.ID, Locale: "zh-CN", Path: page.PathPattern,
					SEO: themecompiler.PageSEOView{Title: page.ID},
				})
				if err != nil {
					t.Fatalf("viewmodel %s: %v", page.ID, err)
				}
				bound, err := themecompiler.CorePageViewModelRegistry().Bind(
					page.ID, page.ContractVersion, digest, model,
				)
				if err != nil {
					t.Fatalf("bind %s: %v", page.ID, err)
				}
				output, err := snapshot.Render(context.Background(), decl.Template, bound)
				if err != nil || len(output.HTMLSegments()) == 0 {
					t.Fatalf("render %s: segments=%d err=%v", page.ID, len(output.HTMLSegments()), err)
				}
			}
		})
	}
}

func themeCompletenessRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// apps/api/app/Support/Pages → repo root is five levels up
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
}
