package pages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func pluginBusinessFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../"))
	return filepath.Join(root, "extensions/fixtures/plugins/sforum-plugin-page-business-e2e")
}

func pluginOverrideThemeFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../"))
	return filepath.Join(root, "extensions/fixtures/themes/sforum-plugin-override-e2e-theme")
}

func fixtureFileDigest(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TestDurableFixturesProveThemeOverridePreservesPluginBusinessContract 使用
// extensions/fixtures 中的 durable 包证明：激活主题覆盖优先、契约漂移 soft-skip、
// 破坏载荷 emergency。
func TestDurableFixturesProveThemeOverridePreservesPluginBusinessContract(t *testing.T) {
	pluginRoot := pluginBusinessFixtureRoot(t)
	themeRoot := pluginOverrideThemeFixtureRoot(t)
	if _, err := os.Stat(filepath.Join(pluginRoot, "templates/article.html")); err != nil {
		t.Fatalf("plugin fixture missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(themeRoot, "templates/plugins/sforum.plugin-page-business-e2e/article.html")); err != nil {
		t.Fatalf("theme fixture missing: %v", err)
	}

	const (
		pluginID      = "sforum.plugin-page-business-e2e"
		themeID       = "sforum.plugin-override-e2e-theme"
		schemaVersion = "sforum.plugin-page-business-e2e.article.data@1"
		overrideKey   = "sforum.plugin-page-business-e2e.article"
		templateID    = "sforum.plugin-page-business-e2e.template.article"
	)

	pluginArtifact := RuntimeArtifact{
		ExtensionID: pluginID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("1", 64),
		RuntimeInstanceID: "runtime-plugin-business-e2e",
	}
	themeArtifact := RuntimeArtifact{
		ExtensionID: themeID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("2", 64),
	}
	contribution := PageContribution{
		ID: pluginID + ".article", Action: ActionAdd, Path: "/e2e-articles/:slug",
		Template: "templates/article.html", Contract: pluginID + ".page.article@1", Access: AccessPublic,
		DataSource: "plugin", DataRoute: "/page-data/article", DataSchema: "schemas/article.json",
		ExtensionID: pluginID, Version: "1.0.0", PackageDigest: pluginArtifact.PackageDigest,
		RuntimeInstanceID: pluginArtifact.RuntimeInstanceID,
	}
	plugin, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: pluginArtifact, PackageRoot: pluginRoot, Contributions: []PageContribution{contribution},
		Templates: []RuntimeTemplateDeclaration{{
			ID: templateID, ContractVersion: templateID + "@1", Action: "add",
			Path: "templates/article.html", Digest: fixtureFileDigest(t, pluginRoot, "templates/article.html"),
			ViewModelSchema: schemaVersion, ThemeOverrideKey: overrideKey,
		}},
		DataSchemas: []RuntimeDataSchemaDeclaration{{
			ID: "sforum.plugin-page-business-e2e.article.data", Version: "1", Path: "schemas/article.json",
			Digest: fixtureFileDigest(t, pluginRoot, "schemas/article.json"),
		}},
		PackageKind: RuntimeTemplatePlugin, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	overridePath := "templates/plugins/sforum.plugin-page-business-e2e/article.html"
	theme, err := BuildThemeRuntimeSnapshot(ThemeRuntimeBuildInput{
		Artifact: themeArtifact, PackageRoot: themeRoot, Contributions: []PageContribution{{
			ID: themeID + ".home", Action: ActionReplace, Target: "forum.home",
			Template: "templates/home.html", Contract: "sforum.page.home@1",
			ExtensionID: themeID, Version: "1.0.0", PackageDigest: themeArtifact.PackageDigest,
		}},
		Templates: []RuntimeTemplateDeclaration{
			{
				ID: themeID + ".template.home", ContractVersion: themeID + ".template.home@1", Action: "add",
				Path: "templates/home.html", Digest: fixtureFileDigest(t, themeRoot, "templates/home.html"),
				ViewModelSchema: "sforum.page.home@1",
			},
			{
				ID: themeID + ".template.article", ContractVersion: themeID + ".template.article@1", Action: "replace",
				TargetID: templateID, Path: overridePath, Digest: fixtureFileDigest(t, themeRoot, overridePath),
				ViewModelSchema: schemaVersion, ThemeOverrideKey: overrideKey,
			},
		},
		PackageKind: RuntimeTemplateTheme, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	registry := NewThemeRuntimeRegistry()
	if _, _, err := registry.Stage(theme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(themeArtifact); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(plugin); err != nil {
		t.Fatal(err)
	}
	renderer, ok := registry.Resolve(pluginArtifact, pluginID+".article", pluginID+".article")
	if !ok {
		t.Fatal("plugin page did not resolve")
	}
	output, err := renderer.RenderPluginData(
		context.Background(), json.RawMessage(`{"title":"Fixture article","state":"published"}`),
		themecompiler.PageSEOView{Title: "Article"}, pluginID+".article",
	)
	if err != nil || output.Source != ThemeRenderSourceActiveOverride || output.Fallback {
		t.Fatalf("override output=%#v err=%v", output, err)
	}
	if html := renderer.LegacyHTML(output); !strings.Contains(html, "theme: Fixture article / published") {
		t.Fatalf("override HTML=%q", html)
	}

	// 破坏契约：emergency，不渲染主题或插件业务字段。
	invalid, err := renderer.RenderPluginData(
		context.Background(), json.RawMessage(`{"title":"Fixture article","state":"published","themeMutation":true}`),
		themecompiler.PageSEOView{Title: "Article"}, pluginID+".article",
	)
	if err != nil || invalid.Source != ThemeRenderSourceEmergency || !invalid.Fallback {
		t.Fatalf("invalid payload output=%#v err=%v", invalid, err)
	}
}
