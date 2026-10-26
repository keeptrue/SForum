package pages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestCorePageViewModelFactoryBuildsEveryCatalogContract(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, page := range Catalog() {
		value, err := BuildCorePageViewModel(CorePageViewModelRequest{
			PageID: page.ID, Locale: "zh-CN", Path: "/test",
			SEO: themecompiler.PageSEOView{Title: page.ID},
		})
		if err != nil {
			t.Fatalf("build %s: %v", page.ID, err)
		}
		if _, err := themecompiler.CorePageViewModelRegistry().Bind(page.ID, page.ContractVersion, digest, value); err != nil {
			t.Fatalf("bind %s: %v", page.ID, err)
		}
	}
}

func TestThemeRuntimeSnapshotCompilesOnceAndRendersExactProvider(t *testing.T) {
	root := defaultThemeFixtureRoot(t)
	artifact := RuntimeArtifact{
		ExtensionID: "sforum-default", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("b", 64),
	}
	contribution := PageContribution{
		ID: "sforum.default-theme.home", Action: ActionReplace, Target: "forum.home",
		Template: "templates/home.html", Contract: "sforum.page.home@1",
		ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
	}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution},
		SiteName: "SForum", Locales: []string{"en-US", "zh-CN", "zh-CN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RuntimeKey() == "" || !snapshot.Covers("forum.home", contribution.ID) {
		t.Fatalf("snapshot key=%q coverage=%v", snapshot.RuntimeKey(), snapshot.Covers("forum.home", contribution.ID))
	}
	output, err := snapshot.Render(context.Background(), CorePageViewModelRequest{
		PageID: "forum.home", Locale: "zh-CN", Path: "/",
		SEO: themecompiler.PageSEOView{Title: "Home"},
	}, contribution.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 默认主题 home L1 挂载 navbar + body 岛 + footer；body 岛仍为 forum.component.home_page。
	legacy := snapshot.LegacyHTML(output)
	if len(output.HTMLSegments) == 0 || !strings.Contains(legacy, "<sf-home-page>") {
		t.Fatalf("output=%#v legacy=%q", output, legacy)
	}
	var hasHome, hasNavbar, hasFooter bool
	for _, island := range output.Islands {
		switch island.ComponentID {
		case "forum.component.home_page":
			hasHome = true
		case "navigation.component.navbar":
			hasNavbar = true
		case "navigation.component.footer":
			hasFooter = true
		}
	}
	if !hasHome || !hasNavbar || !hasFooter {
		t.Fatalf("missing chrome/body islands home=%v navbar=%v footer=%v islands=%#v", hasHome, hasNavbar, hasFooter, output.Islands)
	}

	registry := NewThemeRuntimeRegistry()
	if revision := registry.Publish(snapshot); revision != 1 {
		t.Fatalf("revision=%d", revision)
	}
	if _, ok := registry.Resolve(artifact, "forum.home", contribution.ID); !ok {
		t.Fatal("exact snapshot did not resolve")
	}
	stale := artifact
	stale.PackageDigest = strings.Repeat("c", 64)
	if _, ok := registry.Resolve(stale, "forum.home", contribution.ID); ok {
		t.Fatal("stale artifact resolved")
	}
	if _, err := registry.RemoveExact(stale); err != ErrThemeRuntimeConflict {
		t.Fatalf("stale removal error=%v", err)
	}
	if _, err := registry.RemoveExact(artifact); err != nil {
		t.Fatal(err)
	}
}

func TestThemeRuntimeNavigationCapabilitiesFollowExactActiveArtifact(t *testing.T) {
	full := buildThemeRuntimeFixture(t, "theme.full", strings.Repeat("7", 64), "theme.full.home")
	full.navigationLocations = cloneStringMap(themeNavigationLocationBindings)
	limited := buildThemeRuntimeFixture(t, "theme.limited", strings.Repeat("8", 64), "theme.limited.home")
	limited.navigationLocations = map[string]string{"public.topbar.primary": "sf-navbar"}

	registry := NewThemeRuntimeRegistry()
	if !registry.SupportsNavigationLocation("public.sidebar.primary") {
		t.Fatal("Core fallback must support all locations without an active theme")
	}
	if _, _, err := registry.Stage(full); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(full.Artifact()); err != nil {
		t.Fatal(err)
	}
	if !registry.SupportsNavigationLocation("public.sidebar.primary") {
		t.Fatal("full active artifact lost sidebar capability")
	}
	if _, _, err := registry.Stage(limited); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(limited.Artifact()); err != nil {
		t.Fatal(err)
	}
	if registry.SupportsNavigationLocation("public.sidebar.primary") || !registry.SupportsNavigationLocation("public.topbar.primary") {
		t.Fatal("capabilities did not switch with the exact active artifact")
	}
	if _, err := registry.ActivateExact(full.Artifact()); err != nil {
		t.Fatal(err)
	}
	if !registry.SupportsNavigationLocation("public.footer.primary") {
		t.Fatal("reactivating the prior exact artifact did not restore capabilities")
	}
}

func TestThemeRuntimeEmitsExactPublicL2HostIslandWithSSRContent(t *testing.T) {
	root := t.TempDir()
	body := `<main><sf-extension-widget extension-id="demo.public" component-id="demo.public.component.card"><article>Primary SSR content</article></sf-extension-widget><sf-home-page></sf-home-page></main>`
	writeThemeRuntimeTestFile(t, root, "templates/home.html", body)
	artifact := RuntimeArtifact{ExtensionID: "l2.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("9", 64)}
	contribution := PageContribution{
		ID: "l2.theme.home", Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
		Contract: "sforum.page.home@1", ExtensionID: artifact.ExtensionID,
		Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
	}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution}, SiteName: "SForum",
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := snapshot.Render(context.Background(), CorePageViewModelRequest{
		PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "Home"},
	}, contribution.ID)
	if err != nil {
		t.Fatal(err)
	}
	var widget *themecompiler.IslandDescriptor
	for index := range output.Islands {
		if output.Islands[index].ComponentID == "core.component.shared.sfextension_widget" {
			widget = &output.Islands[index]
			break
		}
	}
	props := map[string]string{}
	if widget != nil {
		for _, prop := range widget.Props {
			props[prop.Name] = prop.StringValue
		}
	}
	if widget == nil || len(widget.Props) != 2 || props["extension-id"] != "demo.public" ||
		props["component-id"] != "demo.public.component.card" || len(widget.FallbackHTMLSegments) != 1 ||
		!strings.Contains(widget.FallbackHTMLSegments[0], "Primary SSR content") {
		t.Fatalf("public L2 island is not exact-id bound: %#v", output.Islands)
	}
}

func TestThemeRuntimeRejectsPublicL2IslandWithoutExactComponentIdentity(t *testing.T) {
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/home.html", `<main><sf-extension-widget extension-id="demo.public"></sf-extension-widget><sf-home-page></sf-home-page></main>`)
	artifact := RuntimeArtifact{ExtensionID: "invalid-l2.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("8", 64)}
	_, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{{
			ID: "invalid-l2.theme.home", Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
			Contract: "sforum.page.home@1", ExtensionID: artifact.ExtensionID,
			Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		}},
	})
	if err == nil {
		t.Fatal("L2 Host island without component-id must fail static compilation")
	}
}

func TestThemeRuntimeRejectsPublicL2OnSystemErrorPages(t *testing.T) {
	root := t.TempDir()
	body := `<main><sf-error-details></sf-error-details><sf-extension-widget extension-id="demo.public" component-id="demo.public.component.card">fallback</sf-extension-widget></main>`
	writeThemeRuntimeTestFile(t, root, "templates/forbidden.html", body)
	artifact := RuntimeArtifact{ExtensionID: "l2-system-error.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("7", 64)}
	_, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{{
			ID: "l2-system-error.theme.forbidden", Action: ActionReplace, Target: "system.forbidden",
			Template: "templates/forbidden.html", Contract: "sforum.page.forbidden@1",
			ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		}},
		PackageKind: RuntimeTemplateTheme,
	})
	if !errors.Is(err, ErrThemeRuntimeConflict) {
		t.Fatalf("system error public L2 error=%v", err)
	}
}

func TestThemeRuntimeRejectsPluginOwnedSystemErrorReplacement(t *testing.T) {
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/forbidden.html", `<main><sf-error-details></sf-error-details></main>`)
	artifact := RuntimeArtifact{ExtensionID: "plugin.system-error", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("6", 64)}
	_, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{{
			ID: "plugin.system-error.forbidden", Action: ActionReplace, Target: "system.forbidden",
			Template: "templates/forbidden.html", Contract: "sforum.page.forbidden@1",
			ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		}},
		PackageKind: RuntimeTemplatePlugin,
	})
	if !errors.Is(err, ErrThemeRuntimeConflict) {
		t.Fatalf("plugin-owned system error runtime error=%v", err)
	}
}

func TestThemeRuntimeRequiresSystemErrorActionIsland(t *testing.T) {
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/forbidden.html", `<main><sf-error-details></sf-error-details></main>`)
	artifact := RuntimeArtifact{ExtensionID: "missing-action.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("9", 64)}
	_, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{{
			ID: "missing-action.theme.forbidden", Action: ActionReplace, Target: "system.forbidden",
			Template: "templates/forbidden.html", Contract: "sforum.page.forbidden@1",
			ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		}},
		PackageKind: RuntimeTemplateTheme,
	})
	if !errors.Is(err, themecompiler.ErrRequiredIsland) {
		t.Fatalf("missing system error actions error=%v", err)
	}
}

func TestPluginPreflightRejectsSystemErrorReplacement(t *testing.T) {
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "theme.json", `{"pages":[{"id":"plugin.system-error.not_found","action":"replace","target":"system.not_found","template":"templates/not-found.html","contract":"sforum.page.not_found@1"}]}`)
	writeThemeRuntimeTestFile(t, root, "templates/not-found.html", `<main><sf-error-details></sf-error-details></main>`)
	bridge := NewExtensionBridge(NewRegistry(NewMemoryStore()))
	_, err := bridge.PreflightPluginPackage(ThemeExtension{
		ID: "plugin.system-error", Version: "1.0.0", PackagePath: root, PackageDigest: strings.Repeat("5", 64),
	})
	if !errors.Is(err, ErrNotReplaceable) {
		t.Fatalf("plugin system error replacement error=%v", err)
	}
}

func TestThemeRuntimeSnapshotIgnoresUncoveredLegacyAddTemplates(t *testing.T) {
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/core.html", `<main>core</main><sf-home-page></sf-home-page>`)
	writeThemeRuntimeTestFile(t, root, "templates/plugin.html", `<main>legacy plugin page</main>`)
	writeThemeRuntimeTestFile(t, root, "theme.json", `{"pages":[]}`)
	artifact := RuntimeArtifact{ExtensionID: "mixed.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("d", 64)}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{
			{ID: "mixed.home", Action: ActionReplace, Target: "forum.home", Template: "templates/core.html", Contract: "sforum.page.home@1", ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest},
			{ID: "mixed.add", Action: ActionAdd, Path: "/mixed", Template: "templates/plugin.html", Contract: "mixed.page@1", ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Covers("mixed.add", "mixed.add") {
		t.Fatal("untyped plugin business page entered core Theme runtime")
	}
}

func TestThemeRuntimeRegistryStagesSwitchWithoutDroppingSource(t *testing.T) {
	source := buildThemeRuntimeFixture(t, "source.theme", strings.Repeat("e", 64), "source.home")
	target := buildThemeRuntimeFixture(t, "target.theme", strings.Repeat("f", 64), "target.home")
	registry := NewThemeRuntimeRegistry()
	registry.Publish(source)
	firstSkin, ok := registry.ActiveSkin()
	if !ok || firstSkin.ExtensionID != source.Artifact().ExtensionID || firstSkin.NodeRevision != 1 {
		t.Fatalf("first skin=%#v ok=%v", firstSkin, ok)
	}
	if _, staged, err := registry.Stage(target); err != nil || !staged {
		t.Fatalf("stage target: staged=%v err=%v", staged, err)
	}
	if _, ok := registry.Resolve(source.Artifact(), "forum.home", "source.home"); !ok {
		t.Fatal("source disappeared while target was staged")
	}
	resolvedSource, _ := registry.Resolve(source.Artifact(), "forum.home", "source.home")
	output, err := resolvedSource.Render(context.Background(), CorePageViewModelRequest{
		PageID: "forum.home", Locale: "zh-CN", Path: "/",
	}, "source.home")
	if err != nil || output.NodeRevision != 2 {
		t.Fatalf("staged cache revision output=%#v err=%v", output, err)
	}
	if _, ok := registry.Resolve(target.Artifact(), "forum.home", "target.home"); !ok {
		t.Fatal("staged target is not exact-resolvable")
	}
	if active, _, ok := registry.Active(); !ok || active.Artifact() != source.Artifact() {
		t.Fatalf("staging changed active snapshot: %#v", active)
	}
	if _, err := registry.ActivateExact(target.Artifact()); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RemoveExact(source.Artifact()); err != nil {
		t.Fatal(err)
	}
	if active, _, ok := registry.Active(); !ok || active.Artifact() != target.Artifact() {
		t.Fatalf("target not active: %#v", active)
	}
}

func TestThemeRuntimeFourLevelFallbackIsExactAndZeroIO(t *testing.T) {
	const pluginTemplateID = "plugin.demo.template.plugin.demo.home"
	failing := `<main>{{asset "missing"}}</main><sf-home-page></sf-home-page>`
	defaultTheme, defaultRoot := buildFallbackRuntime(t, "sforum.default-theme", "1", RuntimeTemplateTheme,
		`<main>default output</main><sf-home-page></sf-home-page>`, "", "")
	activeTheme, activeRoot := buildFallbackRuntime(t, "active.theme", "2", RuntimeTemplateTheme,
		failing, pluginTemplateID, failing)
	plugin, pluginRoot := buildFallbackRuntime(t, "plugin.demo", "3", RuntimeTemplatePlugin, failing, "", "")

	registry := NewThemeRuntimeRegistry()
	if _, _, err := registry.Stage(defaultTheme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SetDefaultExact(defaultTheme.Artifact()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(activeTheme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(activeTheme.Artifact()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(plugin); err != nil {
		t.Fatal(err)
	}

	// A published fallback plan owns no path or filesystem handle.
	for _, root := range []string{defaultRoot, activeRoot, pluginRoot} {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}
	renderer, ok := registry.Resolve(plugin.Artifact(), "forum.home", "plugin.demo.home")
	if !ok {
		t.Fatal("plugin render plan did not resolve")
	}
	output, err := renderer.Render(context.Background(), CorePageViewModelRequest{
		PageID: "forum.home", Locale: "zh-CN", Path: "/", SEO: themecompiler.PageSEOView{Title: "Home"},
	}, "plugin.demo.home")
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != ThemeRenderSourceDefaultTheme || !output.Fallback ||
		!strings.Contains(renderer.LegacyHTML(output), "default output") {
		t.Fatalf("unexpected fallback output: %#v", output)
	}
	wantSources := []string{
		ThemeRenderSourceActiveOverride, ThemeRenderSourcePlugin,
		ThemeRenderSourceActiveTheme, ThemeRenderSourceDefaultTheme,
	}
	if len(output.Attempts) != len(wantSources) {
		t.Fatalf("attempts=%#v", output.Attempts)
	}
	for index, source := range wantSources {
		if output.Attempts[index].Source != source {
			t.Fatalf("attempt %d source=%q want=%q", index, output.Attempts[index].Source, source)
		}
		if index < len(wantSources)-1 && output.Attempts[index].FailureCode != "render_failed" {
			t.Fatalf("attempt %d failure=%q", index, output.Attempts[index].FailureCode)
		}
	}
}

func TestThemeRuntimeFallbackUsesOverrideFirstAndEmergencyLast(t *testing.T) {
	const pluginTemplateID = "plugin.demo.template.plugin.demo.home"
	failing := `<main>{{asset "missing"}}</main><sf-home-page></sf-home-page>`
	t.Run("override", func(t *testing.T) {
		active, _ := buildFallbackRuntime(t, "active.theme", "4", RuntimeTemplateTheme,
			`<main>active</main><sf-home-page></sf-home-page>`, pluginTemplateID,
			`<main>override</main><sf-home-page></sf-home-page>`)
		plugin, _ := buildFallbackRuntime(t, "plugin.demo", "5", RuntimeTemplatePlugin,
			`<main>plugin</main><sf-home-page></sf-home-page>`, "", "")
		registry := NewThemeRuntimeRegistry()
		_, _, _ = registry.Stage(active)
		_, _ = registry.ActivateExact(active.Artifact())
		_, _, _ = registry.Stage(plugin)
		renderer, _ := registry.Resolve(plugin.Artifact(), "forum.home", "plugin.demo.home")
		output, err := renderer.Render(context.Background(), CorePageViewModelRequest{
			PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "Home"},
		}, "plugin.demo.home")
		if err != nil || output.Source != ThemeRenderSourceActiveOverride || output.Fallback ||
			!strings.Contains(renderer.LegacyHTML(output), "override") {
			t.Fatalf("output=%#v err=%v", output, err)
		}
	})

	t.Run("emergency", func(t *testing.T) {
		active, _ := buildFallbackRuntime(t, "active.theme", "6", RuntimeTemplateTheme, failing, "", "")
		plugin, _ := buildFallbackRuntime(t, "plugin.demo", "7", RuntimeTemplatePlugin, failing, "", "")
		registry := NewThemeRuntimeRegistry()
		_, _, _ = registry.Stage(active)
		_, _ = registry.ActivateExact(active.Artifact())
		_, _, _ = registry.Stage(plugin)
		renderer, _ := registry.Resolve(plugin.Artifact(), "forum.home", "plugin.demo.home")
		output, err := renderer.Render(context.Background(), CorePageViewModelRequest{
			PageID: "forum.home", Locale: "en-US", Path: "/",
			SEO: themecompiler.PageSEOView{Title: `<unsafe>`},
		}, "plugin.demo.home")
		if err != nil || output.Source != ThemeRenderSourceEmergency || !output.Fallback ||
			!strings.Contains(renderer.LegacyHTML(output), "&lt;unsafe&gt;") {
			t.Fatalf("output=%#v html=%q err=%v", output, renderer.LegacyHTML(output), err)
		}
	})
}

func TestThemeRuntimeV3TemplateDeclarationIsDigestAndContractBound(t *testing.T) {
	root := t.TempDir()
	body := `<main>plugin</main><sf-home-page></sf-home-page>`
	writeThemeRuntimeTestFile(t, root, "templates/home.html", body)
	digest := sha256.Sum256([]byte(body))
	artifact := RuntimeArtifact{
		ExtensionID: "plugin.v3", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("9", 64),
	}
	contribution := PageContribution{
		ID: "plugin.v3.home", Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
		Contract: "sforum.page.home@1", ExtensionID: artifact.ExtensionID,
		Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
	}
	declaration := RuntimeTemplateDeclaration{
		ID: "plugin.v3.template.home", ContractVersion: "plugin.v3.template.home@1", Action: "add",
		Path: "templates/home.html", Digest: hex.EncodeToString(digest[:]), ViewModelSchema: contribution.Contract,
	}
	input := ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution},
		Templates: []RuntimeTemplateDeclaration{declaration}, PackageKind: RuntimeTemplatePlugin,
		RequireDeclaredTemplates: true,
	}
	if _, err := BuildThemeRuntimeSnapshot(input); err != nil {
		t.Fatal(err)
	}
	input.Templates[0].Digest = strings.Repeat("a", 64)
	if _, err := BuildThemeRuntimeSnapshot(input); !errors.Is(err, ErrThemeRuntimeConflict) {
		t.Fatalf("digest mismatch error=%v", err)
	}
	input.Templates[0] = declaration
	input.Templates[0].ViewModelSchema = "sforum.page.topic_show@1"
	if _, err := BuildThemeRuntimeSnapshot(input); !errors.Is(err, ErrThemeRuntimeConflict) {
		t.Fatalf("contract mismatch error=%v", err)
	}
}

func TestThemeRuntimePluginBusinessContractIsPreservedThroughThemeOverride(t *testing.T) {
	plugin, payloadSchemaDigest := buildPluginBusinessRuntime(t, "plugin.demo", "1.0.0", "runtime-1", "plugin.demo.page.article.data@1")
	theme := buildPluginBusinessOverrideRuntime(
		t, "theme.presentation", "plugin.demo.template.article", "plugin.demo.article",
		"plugin.demo.page.article.data@1", `theme: {{.title}} / {{.state}}`,
	)
	registry := NewThemeRuntimeRegistry()
	if _, _, err := registry.Stage(theme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(theme.Artifact()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(plugin); err != nil {
		t.Fatal(err)
	}
	renderer, ok := registry.Resolve(plugin.Artifact(), "plugin.demo.article", "plugin.demo.article")
	if !ok {
		t.Fatal("exact plugin add page did not resolve")
	}
	contract, ok := renderer.PluginDataContract("plugin.demo.article")
	if !ok || contract.ViewModelID != "plugin.demo.template.article" ||
		contract.SchemaVersion != "plugin.demo.page.article.data@1" || contract.SchemaDigest != payloadSchemaDigest {
		t.Fatalf("plugin contract = %#v ok=%t", contract, ok)
	}
	output, err := renderer.RenderPluginData(
		context.Background(), json.RawMessage(`{"title":"Exact article","state":"published"}`),
		themecompiler.PageSEOView{Title: "Article"}, "plugin.demo.article",
	)
	if err != nil || output.Source != ThemeRenderSourceActiveOverride || output.Fallback {
		t.Fatalf("override output=%#v err=%v", output, err)
	}
	if html := renderer.LegacyHTML(output); !strings.Contains(html, "theme: Exact article / published") {
		t.Fatalf("override HTML = %q", html)
	}

	invalid, err := renderer.RenderPluginData(
		context.Background(), json.RawMessage(`{"title":"Exact article","state":"published","themeMutation":true}`),
		themecompiler.PageSEOView{Title: "Article"}, "plugin.demo.article",
	)
	if err != nil || invalid.Source != ThemeRenderSourceEmergency || !invalid.Fallback ||
		len(invalid.Attempts) != 3 || invalid.Attempts[0].FailureCode != "view_model_contract" ||
		invalid.Attempts[1].FailureCode != "view_model_contract" {
		t.Fatalf("invalid payload output=%#v err=%v", invalid, err)
	}
}

func TestThemeRuntimePluginOverrideRequiresExactKeyAndSchema(t *testing.T) {
	plugin, _ := buildPluginBusinessRuntime(t, "plugin.demo", "1.0.0", "runtime-1", "plugin.demo.page.article.data@1")
	for _, test := range []struct {
		name, key, schema string
	}{
		{name: "key drift", key: "plugin.demo.other", schema: "plugin.demo.page.article.data@1"},
		{name: "schema drift", key: "plugin.demo.article", schema: "plugin.demo.page.article.data@2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			theme := buildPluginBusinessOverrideRuntime(
				t, "theme."+strings.ReplaceAll(test.name, " ", "-"), "plugin.demo.template.article", test.key,
				test.schema, `mismatched theme: {{.title}}`,
			)
			registry := NewThemeRuntimeRegistry()
			_, _, _ = registry.Stage(theme)
			_, _ = registry.ActivateExact(theme.Artifact())
			_, _, _ = registry.Stage(plugin)
			renderer, ok := registry.Resolve(plugin.Artifact(), "plugin.demo.article", "plugin.demo.article")
			if !ok {
				t.Fatal("plugin renderer missing")
			}
			output, err := renderer.RenderPluginData(
				context.Background(), json.RawMessage(`{"title":"Plugin semantics","state":"archived"}`),
				themecompiler.PageSEOView{Title: "Article"}, "plugin.demo.article",
			)
			if err != nil || output.Source != ThemeRenderSourcePlugin || output.Fallback ||
				!strings.Contains(renderer.LegacyHTML(output), "plugin: Plugin semantics / archived") {
				t.Fatalf("mismatched override changed output=%#v html=%q err=%v", output, renderer.LegacyHTML(output), err)
			}
		})
	}
}

func buildPluginBusinessRuntime(
	t *testing.T,
	extensionID, version, runtimeID, schemaVersion string,
) (*ThemeRuntimeSnapshot, string) {
	t.Helper()
	root := t.TempDir()
	templateBody := `plugin: {{.title}} / {{.state}}`
	schemaBody := `{"type":"object","required":["title","state"],"additionalProperties":false,"properties":{"title":{"type":"string"},"state":{"type":"string","enum":["published","archived"]}}}`
	writeThemeRuntimeTestFile(t, root, "theme.json", `{"pages":[]}`)
	writeThemeRuntimeTestFile(t, root, "templates/article.html", templateBody)
	writeThemeRuntimeTestFile(t, root, "schemas/article.json", schemaBody)
	templateDigest := sha256.Sum256([]byte(templateBody))
	schemaDigest := sha256.Sum256([]byte(schemaBody))
	artifact := RuntimeArtifact{
		ExtensionID: extensionID, ExtensionVersion: version, PackageDigest: strings.Repeat("c", 64),
		RuntimeInstanceID: runtimeID,
	}
	contribution := PageContribution{
		ID: extensionID + ".article", Action: ActionAdd, Path: "/articles/:slug", Template: "templates/article.html",
		Contract: extensionID + ".page.article@1", Access: AccessPublic,
		DataSource: "plugin", DataRoute: "/page-data/article", DataSchema: "schemas/article.json",
		ExtensionID: extensionID, Version: version, PackageDigest: artifact.PackageDigest, RuntimeInstanceID: runtimeID,
	}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution},
		Templates: []RuntimeTemplateDeclaration{{
			ID: extensionID + ".template.article", ContractVersion: extensionID + ".template.article@1",
			Action: "add", Path: "templates/article.html", Digest: hex.EncodeToString(templateDigest[:]),
			ViewModelSchema: schemaVersion, ThemeOverrideKey: extensionID + ".article",
		}},
		DataSchemas: []RuntimeDataSchemaDeclaration{{
			ID: strings.TrimSuffix(schemaVersion, "@1"), Version: "1", Path: "schemas/article.json",
			Digest: hex.EncodeToString(schemaDigest[:]),
		}},
		PackageKind: RuntimeTemplatePlugin, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, hex.EncodeToString(schemaDigest[:])
}

func buildPluginBusinessOverrideRuntime(
	t *testing.T,
	extensionID, targetID, overrideKey, schemaVersion, overrideBody string,
) *ThemeRuntimeSnapshot {
	t.Helper()
	root := t.TempDir()
	homeBody := `<main>theme home</main><sf-home-page></sf-home-page>`
	overridePath := "templates/plugins/plugin.demo/article.html"
	writeThemeRuntimeTestFile(t, root, "theme.json", `{"pages":[]}`)
	writeThemeRuntimeTestFile(t, root, "templates/home.html", homeBody)
	writeThemeRuntimeTestFile(t, root, overridePath, overrideBody)
	homeDigest := sha256.Sum256([]byte(homeBody))
	overrideDigest := sha256.Sum256([]byte(overrideBody))
	artifact := RuntimeArtifact{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("d", 64),
	}
	contribution := PageContribution{
		ID: extensionID + ".home", Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
		Contract: "sforum.page.home@1", ExtensionID: extensionID, Version: artifact.ExtensionVersion,
		PackageDigest: artifact.PackageDigest,
	}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution},
		Templates: []RuntimeTemplateDeclaration{
			{ID: extensionID + ".template.home", ContractVersion: extensionID + ".template.home@1", Action: "add",
				Path: "templates/home.html", Digest: hex.EncodeToString(homeDigest[:]), ViewModelSchema: "sforum.page.home@1"},
			{ID: extensionID + ".template.article", ContractVersion: extensionID + ".template.article@1", Action: "replace",
				TargetID: targetID, Path: overridePath, Digest: hex.EncodeToString(overrideDigest[:]),
				ViewModelSchema: schemaVersion, ThemeOverrideKey: overrideKey},
		},
		PackageKind: RuntimeTemplateTheme, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func buildFallbackRuntime(
	t *testing.T,
	extensionID, digestByte string,
	kind RuntimeTemplatePackageKind,
	mainBody, overrideTarget, overrideBody string,
) (*ThemeRuntimeSnapshot, string) {
	t.Helper()
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/home.html", mainBody)
	declarations := []RuntimeTemplateDeclaration(nil)
	if overrideTarget != "" {
		name := "templates/plugins/plugin.demo/home.html"
		writeThemeRuntimeTestFile(t, root, name, overrideBody)
		digest := sha256.Sum256([]byte(overrideBody))
		declarations = append(declarations, RuntimeTemplateDeclaration{
			ID: extensionID + ".template.override", ContractVersion: extensionID + ".template.override@1",
			Action: "replace", TargetID: overrideTarget, Path: name, Digest: hex.EncodeToString(digest[:]),
			ViewModelSchema: "sforum.page.home@1",
		})
	}
	artifact := RuntimeArtifact{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat(digestByte, 64),
	}
	contribution := PageContribution{
		ID: extensionID + ".home", Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
		Contract: "sforum.page.home@1", ExtensionID: extensionID, Version: "1.0.0", PackageDigest: artifact.PackageDigest,
	}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []PageContribution{contribution},
		Templates: declarations, PackageKind: kind, SiteName: "SForum",
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, root
}

func buildThemeRuntimeFixture(t *testing.T, extensionID, digest, contributionID string) *ThemeRuntimeSnapshot {
	t.Helper()
	root := t.TempDir()
	writeThemeRuntimeTestFile(t, root, "templates/home.html", `<main>home</main><sf-home-page></sf-home-page>`)
	writeThemeRuntimeTestFile(t, root, "theme.json", `{"pages":[]}`)
	artifact := RuntimeArtifact{ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: digest}
	snapshot, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, SiteName: "SForum",
		Contributions: []PageContribution{{
			ID: contributionID, Action: ActionReplace, Target: "forum.home", Template: "templates/home.html",
			Contract: "sforum.page.home@1", ExtensionID: extensionID, Version: "1.0.0", PackageDigest: digest,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func defaultThemeFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../extensions/builtin/themes/sforum-default"))
}

func writeThemeRuntimeTestFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
