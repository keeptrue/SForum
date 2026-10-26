package pagescontroller

import (
	"encoding/json"
	nethttp "net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// resolvedLayout 是控制器边界的防御性映射：空值走平台默认 site；
// 未知值回退 bare，避免在绕过准入的异常输入上额外添加 chrome。
func TestResolvedLayoutDefaultsToSiteAndNeverGrantsChromeOnUnknownInput(t *testing.T) {
	cases := []struct {
		in   pages.PageLayout
		want pages.PageLayout
	}{
		{"", pages.LayoutSite},
		{"fancy", pages.LayoutBare},
		{"SITE", pages.LayoutSite},
		{pages.LayoutSite, pages.LayoutSite},
		{pages.LayoutBare, pages.LayoutBare},
	}
	for _, tc := range cases {
		if got := resolvedLayout(tc.in); got != tc.want {
			t.Fatalf("resolvedLayout(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// 外壳选择必须与正文渲染结果无关：正文走回退路径时响应仍要给出具体 layout，
// 否则插件页一旦渲染失败就会连带丢掉外壳（变成无导航的裸页）。
// fixture 的 demo 页未声明 layout，因此这里同时验证了平台默认 site。
func TestResolvePathReportsLayoutRegardlessOfBodyRender(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body pagesEnvelope[struct {
		Layout   string `json:"layout"`
		Fallback bool   `json:"fallback"`
		Reason   string `json:"reason"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Layout != string(pages.LayoutSite) {
		t.Fatalf("layout = %q; want platform default %q (fallback=%v reason=%q)",
			body.Data.Layout, pages.LayoutSite, body.Data.Fallback, body.Data.Reason)
	}
}

// 主题未声明外壳时，site 页面必须回落：外壳字段留空，前端走宿主公共外壳。
// 正向路径（主题声明外壳）需要一套带外壳的主题包，属于后续层。
func TestResolvePathLeavesShellEmptyWhenThemeDeclaresNone(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body pagesEnvelope[struct {
		Layout             string `json:"layout"`
		ShellHTML          string `json:"shellHtml"`
		ShellProvider      string `json:"shellProvider"`
		ShellPackageDigest string `json:"shellPackageDigest"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Layout != string(pages.LayoutSite) {
		t.Fatalf("layout = %q; want %q", body.Data.Layout, pages.LayoutSite)
	}
	if body.Data.ShellHTML != "" || body.Data.ShellProvider != "" || body.Data.ShellPackageDigest != "" {
		t.Fatalf("expected no theme shell, got provider=%q digest=%q html=%q",
			body.Data.ShellProvider, body.Data.ShellPackageDigest, body.Data.ShellHTML)
	}
}

// 正向路径：选中主题声明了外壳时，site 页面必须带上该外壳的 HTML 与其精确制品身份。
// 身份是「外壳可归因到某个主题制品」的依据，缺了它前端只能凭空断言来源。
func TestResolvePathReturnsExactThemeShellWhenDeclared(t *testing.T) {
	app, _, themeStore, _ := newPagesTestApp(t)

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../")),
		"extensions/fixtures/themes/plugin-shell-demo")

	themeStore.active = extensions.Extension{
		ID: "demo.shell-theme", Type: extensions.TypeTheme,
		Version: "2.0.0", PackageDigest: "shell-digest", PackagePath: root,
	}

	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body pagesEnvelope[struct {
		Layout             string `json:"layout"`
		ShellHTML          string `json:"shellHtml"`
		ShellProvider      string `json:"shellProvider"`
		ShellVersion       string `json:"shellVersion"`
		ShellPackageDigest string `json:"shellPackageDigest"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Layout != string(pages.LayoutSite) {
		t.Fatalf("layout = %q; want %q", body.Data.Layout, pages.LayoutSite)
	}
	if !strings.Contains(body.Data.ShellHTML, "sf-plugin-page-body") {
		t.Fatalf("shell html must carry the plugin body island, got %q", body.Data.ShellHTML)
	}
	if body.Data.ShellProvider != "demo.shell-theme" || body.Data.ShellVersion != "2.0.0" ||
		body.Data.ShellPackageDigest != "shell-digest" {
		t.Fatalf("shell identity = %q/%q/%q; want demo.shell-theme/2.0.0/shell-digest",
			body.Data.ShellProvider, body.Data.ShellVersion, body.Data.ShellPackageDigest)
	}
	// 组装契约（Route A 第 6 层）：外壳自己拥有 chrome 与正文位置，且每类各恰好一次。
	// 计数放在渲染结果上，而不是「某个字符串出现过」。
	for _, island := range []string{"<sf-navbar", "<sf-footer", "<sf-plugin-page-body"} {
		if count := strings.Count(body.Data.ShellHTML, island); count != 1 {
			t.Fatalf("shell must carry exactly one %s, got %d in %q", island, count, body.Data.ShellHTML)
		}
	}
	// 主题几何类必须在 shellHtml 里存活：它是外壳与主题页面共享几何的凭据。
	if !strings.Contains(body.Data.ShellHTML, "sf-theme-shell--fullwidth-3col") ||
		!strings.Contains(body.Data.ShellHTML, "sf-theme-chrome-body") {
		t.Fatalf("shell geometry classes were stripped: %q", body.Data.ShellHTML)
	}
	// 记录一条实测事实而不是期待：外壳走的是旧 L1 字符串模板路径
	// （LoadTemplate → SanitizeTemplateHTML，bluemonday 主题策略），所以主题自持的
	// data-* 标记不会出现在 shellHtml 中，而 class 保留。内置主题的 33 条
	// 属性选择器都挂在 SFThemeTemplate 包裹层的 data-extension-id 上，因此几何不受
	// 影响；但任何主题若把 CSS 挂在 [data-theme-region]/[data-theme-owned] 上，就会在
	// 插件页与本主题页之间出现差异。详见
	// knowledge/sessions/2026-10-16-route-a-layer6-and-loader-transport-handoff.md。
	if strings.Contains(body.Data.ShellHTML, "data-theme-region") {
		t.Fatalf("shell now preserves theme data markers; re-check the documented caveat: %q", body.Data.ShellHTML)
	}
}

// TestResolvePathLeavesShellEmptyForExplicitBareLayout 固定「bare 不受主题外壳影响」：
// 即使活动主题声明了外壳，显式 bare（以及空值/未知值的防御性映射）的页面也不得拿到
// shellHtml，否则前端会多出一个 chrome 拥有者。
func TestResolvePathLeavesShellEmptyForExplicitBareLayout(t *testing.T) {
	themeStore := &pagesThemeStore{active: extensions.Extension{
		ID: "demo.shell-theme", Type: extensions.TypeTheme,
		Version: "2.0.0", PackageDigest: "shell-digest", PackagePath: shellDemoThemeRoot(t),
	}}
	controller := &Controller{themes: themeStore}
	app := fiber.New()
	app.Get("/probe-shell", func(c fiber.Ctx) error {
		resp := resolveResponse{Layout: c.Query("layout")}
		controller.applyThemeShell(c, &resp, nil, "zh-CN")
		return c.JSON(resp)
	})
	for _, test := range []struct {
		layout string
		want   bool
	}{
		{string(pages.LayoutSite), true},
		{string(pages.LayoutBare), false},
		{"", false},
		{"unknown", false},
	} {
		response := performPages(t, app, nethttp.MethodGet, "/probe-shell?layout="+test.layout, nil, nil)
		var resp resolveResponse
		if err := json.NewDecoder(response.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if test.want {
			if resp.ShellHTML == "" || resp.ShellProvider != "demo.shell-theme" {
				t.Fatalf("layout %q must receive the declared theme shell: %#v", test.layout, resp)
			}
			continue
		}
		if resp.ShellHTML != "" || resp.ShellProvider != "" || resp.ShellPackageDigest != "" {
			t.Fatalf("layout %q must not receive a theme shell: %#v", test.layout, resp)
		}
	}
}

// TestResolvePathFailsOpenToHostShellWhenDeclaredShellDisappears 固定解析期 fail-open：
// 外壳在准入时存在、解析时不可读时，响应必须留空（前端回落宿主外壳），
// 而不是 5xx 或半截外壳。准入期 fail-closed 与解析期 fail-open 是互补的一对。
func TestResolvePathFailsOpenToHostShellWhenDeclaredShellDisappears(t *testing.T) {
	app, _, themeStore, _ := newPagesTestApp(t)
	root := t.TempDir()
	writeControllerFixtureFile(t, root, "theme.json", `{"pages":[],"shell":"templates/plugin-shell.html"}`)
	writeControllerFixtureFile(t, root, "templates/plugin-shell.html",
		`<div class="sf-page"><sf-navbar></sf-navbar><sf-plugin-page-body></sf-plugin-page-body><sf-footer></sf-footer></div>`)
	themeStore.active = extensions.Extension{
		ID: "demo.shell-theme", Type: extensions.TypeTheme,
		Version: "2.0.0", PackageDigest: "shell-digest", PackagePath: root,
	}
	cookie := loginPagesUser(t, app, 2)
	ok := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	var okBody pagesEnvelope[struct {
		Layout    string `json:"layout"`
		ShellHTML string `json:"shellHtml"`
	}]
	if err := json.NewDecoder(ok.Body).Decode(&okBody); err != nil {
		t.Fatal(err)
	}
	if okBody.Data.ShellHTML == "" {
		t.Fatal("precondition: declared shell must resolve before removal")
	}
	// 声明仍然存在（theme.json 仍指向该模板），但模板文件已经不可读。
	if err := os.Remove(filepath.Join(root, "templates/plugin-shell.html")); err != nil {
		t.Fatal(err)
	}
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("fail-open must keep the page served, got status %d", resp.StatusCode)
	}
	var body pagesEnvelope[struct {
		Layout    string `json:"layout"`
		ShellHTML string `json:"shellHtml"`
		Provider  string `json:"provider"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.ShellHTML != "" {
		t.Fatalf("unreadable shell must leave shellHtml empty, got %q", body.Data.ShellHTML)
	}
	if body.Data.Layout != string(pages.LayoutSite) {
		t.Fatalf("layout must stay %q so the Host shell still provides chrome, got %q",
			pages.LayoutSite, body.Data.Layout)
	}
}

func shellDemoThemeRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../")),
		"extensions/fixtures/themes/plugin-shell-demo")
}
