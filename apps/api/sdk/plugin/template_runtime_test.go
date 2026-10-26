package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestPreflightExactTemplateRuntimeRejectsMissingV3Declaration(t *testing.T) {
	root := writeV3ThemePackage(t, v3ThemePackageOptions{includeTemplateDeclaration: false})
	manifest, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	preflightErr := PreflightExactTemplateRuntime(root, manifest)
	if preflightErr == nil {
		t.Fatal("expected missing V3 template declaration to fail exact runtime preflight")
	}
	if !strings.Contains(preflightErr.Error(), "no exact template declaration") {
		t.Fatalf("expected declaration error, got %v", preflightErr)
	}

	report := TestManifest(root, manifest, Options{SkipBackendBinary: true})
	if report.OK {
		t.Fatalf("extension test must fail for missing V3 template declaration: %#v", report.Checks)
	}
	found := false
	for _, check := range report.Checks {
		if check.Code == "template.runtime_exact" && check.Level == "error" {
			found = true
			if !strings.Contains(check.Message, "no exact template declaration") {
				t.Fatalf("unexpected message: %s", check.Message)
			}
		}
	}
	if !found {
		t.Fatalf("expected template.runtime_exact error, got %#v", report.Checks)
	}
}

func TestPreflightExactTemplateRuntimeAcceptsExactV3Declaration(t *testing.T) {
	root := writeV3ThemePackage(t, v3ThemePackageOptions{includeTemplateDeclaration: true})
	manifest, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if err := PreflightExactTemplateRuntime(root, manifest); err != nil {
		t.Fatalf("valid exact declaration must pass: %v", err)
	}
	report := TestManifest(root, manifest, Options{SkipBackendBinary: true})
	if !report.OK {
		t.Fatalf("extension test must pass for exact V3 declaration: %#v", report.Checks)
	}
	found := false
	for _, check := range report.Checks {
		if check.Code == "template.runtime_exact" && check.Level == "ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected template.runtime_exact ok, got %#v", report.Checks)
	}
}

func TestPreflightExactTemplateRuntimeRejectsDigestMismatch(t *testing.T) {
	// LoadPackage 要求 templates.digest 与 packageFiles/字节一致；
	// 此处在加载后篡改声明摘要，模拟激活时声明与编译结果不一致。
	root := writeV3ThemePackage(t, v3ThemePackageOptions{includeTemplateDeclaration: true})
	manifest, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if len(manifest.Templates) != 1 {
		t.Fatalf("expected one template declaration, got %d", len(manifest.Templates))
	}
	manifest.Templates[0].Digest = strings.Repeat("a", 64)
	preflightErr := PreflightExactTemplateRuntime(root, manifest)
	if preflightErr == nil {
		t.Fatal("expected digest mismatch to fail exact runtime preflight")
	}
	if !strings.Contains(preflightErr.Error(), "exact digest") {
		t.Fatalf("expected digest error, got %v", preflightErr)
	}
}

func TestPreflightExactTemplateRuntimeRunsProductionPackagePreflight(t *testing.T) {
	root := writeV3ThemePackage(t, v3ThemePackageOptions{
		includeTemplateDeclaration: true,
		cssBody:                    `@import url("https://evil.example/theme.css");`,
	})
	manifest, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	preflightErr := PreflightExactTemplateRuntime(root, manifest)
	if preflightErr == nil || !strings.Contains(preflightErr.Error(), "external @import") {
		t.Fatalf("production CSS preflight was not reused: %v", preflightErr)
	}
}

func TestPreflightExactTemplateRuntimeRejectsLegacyAndSkipsL0(t *testing.T) {
	// Legacy manifests are rejected before template preflight.
	legacyRoot := t.TempDir()
	writePackageFile(t, legacyRoot, "templates/home.html", `<main>home</main><sf-home-page></sf-home-page>`)
	writePackageFile(t, legacyRoot, "theme.json", `{
  "pages": [{"id":"legacy.home","action":"replace","target":"forum.home","template":"templates/home.html","contract":"sforum.page.home@1"}],
  "skin": {"css": []}
}`)
	writePackageFile(t, legacyRoot, "sforum.extension.json", `{
  "id": "legacy.theme",
  "name": "Legacy Theme",
  "description": "Legacy V1 theme without Manifest V3.",
  "url": "https://example.com/legacy",
  "author": {"name": "Legacy"},
  "version": "1.0.0",
  "type": "theme",
  "sforumVersion": ">=0.1.0"
}`)
	if _, err := extensionmanifest.LoadPackage(legacyRoot); err == nil {
		t.Fatal("legacy manifest must be rejected")
	}

	// L0-only V3 theme (no page templates) must pass.
	l0Root := writeV3ThemePackage(t, v3ThemePackageOptions{l0Only: true})
	l0Manifest, err := extensionmanifest.LoadPackage(l0Root)
	if err != nil {
		t.Fatalf("l0 LoadPackage: %v", err)
	}
	if err := PreflightExactTemplateRuntime(l0Root, l0Manifest); err != nil {
		t.Fatalf("L0-only V3 must skip exact preflight: %v", err)
	}
}

type v3ThemePackageOptions struct {
	includeTemplateDeclaration bool
	l0Only                     bool
	cssBody                    string
}

func writeV3ThemePackage(t *testing.T, opts v3ThemePackageOptions) string {
	t.Helper()
	root := t.TempDir()
	cssBody := opts.cssBody
	if cssBody == "" {
		cssBody = `:root { --sf-probe: 1; }`
	}
	cssDigest := sha256Hex(cssBody)
	writePackageFile(t, root, "assets/theme.css", cssBody)

	packageFiles := []map[string]any{
		{"id": "demo.theme.file.css", "kind": "asset", "path": "assets/theme.css", "digest": cssDigest},
	}
	assets := []map[string]any{
		{
			"handle": "demo.theme.asset.theme", "contractVersion": "demo.theme.asset.theme@1",
			"type": "style", "path": "assets/theme.css", "digest": cssDigest,
		},
	}
	var templates []map[string]any

	if opts.l0Only {
		writePackageFile(t, root, "theme.json", `{"pages":[],"skin":{"css":["assets/theme.css"]}}`)
	} else {
		homeBody := `<main>home</main><sf-home-page></sf-home-page>`
		homeDigest := sha256Hex(homeBody)
		writePackageFile(t, root, "templates/home.html", homeBody)
		writePackageFile(t, root, "theme.json", `{
  "pages": [
    {
      "id": "demo.theme.home",
      "action": "replace",
      "target": "forum.home",
      "template": "templates/home.html",
      "contract": "sforum.page.home@1"
    }
  ],
  "skin": {"css": ["assets/theme.css"]}
}`)
		packageFiles = append(packageFiles, map[string]any{
			"id": "demo.theme.file.template", "kind": "template",
			"path": "templates/home.html", "digest": homeDigest,
		})
		if opts.includeTemplateDeclaration {
			templates = []map[string]any{{
				"id": "demo.theme.template.home", "contractVersion": "demo.theme.template.home@1",
				"action": "add", "path": "templates/home.html", "digest": homeDigest,
				"viewModelSchema": "sforum.page.home@1", "themeOverrideKey": "demo.theme.home",
			}}
		}
	}

	manifest := map[string]any{
		"manifestVersion": 3,
		"id":              "demo.theme",
		"name":            "Demo Theme",
		"description":     "V3 theme package for exact template preflight.",
		"url":             "https://example.com/demo-theme",
		"author":          map[string]any{"name": "Demo"},
		"version":         "1.0.0",
		"type":            "theme",
		"sforumVersion":   ">=0.1.0",
		"packageFiles":    packageFiles,
		"assets":          assets,
	}
	if len(templates) > 0 {
		manifest["templates"] = templates
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePackageFile(t, root, "sforum.extension.json", string(raw)+"\n")
	return root
}

func writePackageFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
