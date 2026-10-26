package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestExtensionValidateBuiltinThemes(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))

	for _, theme := range []string{"sforum-default"} {
		t.Run(theme, func(t *testing.T) {
			root := filepath.Join(repoRoot, "extensions", "builtin", "themes", theme)
			cmd := newRootCommand()
			cmd.SetArgs([]string{"extension", "validate", root})
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("validate built-in theme: %v\n%s", err, out.String())
			}
		})
	}
}

func TestExtensionValidateAndTestRejectMissingV3TemplateDeclaration(t *testing.T) {
	missing := writeCLIThemePackage(t, false)
	valid := writeCLIThemePackage(t, true)

	for _, name := range []string{"validate", "test"} {
		t.Run(name+"/missing", func(t *testing.T) {
			cmd := newRootCommand()
			args := []string{"extension", name, missing}
			if name == "test" {
				args = []string{"extension", name, "--allow-scaffold", missing}
			}
			cmd.SetArgs(args)
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected %s to fail:\n%s", name, out.String())
			}
			combined := err.Error() + "\n" + out.String()
			if !strings.Contains(combined, "no exact template declaration") &&
				!strings.Contains(combined, "exact template runtime") {
				t.Fatalf("expected exact template failure, got err=%v out=\n%s", err, out.String())
			}
		})
		t.Run(name+"/valid", func(t *testing.T) {
			cmd := newRootCommand()
			args := []string{"extension", name, valid}
			if name == "test" {
				args = []string{"extension", name, "--allow-scaffold", valid}
			}
			cmd.SetArgs(args)
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("expected %s to pass: %v\n%s", name, err, out.String())
			}
			if name == "validate" && !strings.Contains(out.String(), "OK") {
				t.Fatalf("expected validate OK:\n%s", out.String())
			}
			if name == "test" && !strings.Contains(out.String(), "PASS") {
				t.Fatalf("expected test PASS:\n%s", out.String())
			}
		})
	}
}

func writeCLIThemePackage(t *testing.T, withTemplateDeclaration bool) string {
	t.Helper()
	root := t.TempDir()
	homeBody := `<main>home</main><sf-home-page></sf-home-page>`
	cssBody := `:root { --sf-cli: 1; }`
	homeDigest := sha256HexString(homeBody)
	cssDigest := sha256HexString(cssBody)
	writeCLIFile(t, root, "templates/home.html", homeBody)
	writeCLIFile(t, root, "assets/theme.css", cssBody)
	writeCLIFile(t, root, "theme.json", `{
  "pages": [
    {
      "id": "cli.theme.home",
      "action": "replace",
      "target": "forum.home",
      "template": "templates/home.html",
      "contract": "sforum.page.home@1"
    }
  ],
  "skin": {"css": ["assets/theme.css"]}
}`)

	manifest := map[string]any{
		"manifestVersion": 3,
		"id":              "cli.theme",
		"name":            "CLI Theme",
		"description":     "CLI validate/test exact template preflight package.",
		"url":             "https://example.com/cli-theme",
		"author":          map[string]any{"name": "CLI"},
		"version":         "1.0.0",
		"type":            "theme",
		"sforumVersion":   ">=0.1.0",
		"packageFiles": []map[string]any{
			{"id": "cli.theme.file.template", "kind": "template", "path": "templates/home.html", "digest": homeDigest},
			{"id": "cli.theme.file.css", "kind": "asset", "path": "assets/theme.css", "digest": cssDigest},
		},
		"assets": []map[string]any{
			{
				"handle": "cli.theme.asset.theme", "contractVersion": "cli.theme.asset.theme@1",
				"type": "style", "path": "assets/theme.css", "digest": cssDigest,
			},
		},
	}
	if withTemplateDeclaration {
		manifest["templates"] = []map[string]any{{
			"id": "cli.theme.template.home", "contractVersion": "cli.theme.template.home@1",
			"action": "add", "path": "templates/home.html", "digest": homeDigest,
			"viewModelSchema": "sforum.page.home@1", "themeOverrideKey": "cli.theme.home",
		}}
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, root, "sforum.extension.json", string(raw)+"\n")
	return root
}

// 脚手架产物必须声明当前宿主能接受的发行线。写死 ^1.0.0 的模板会在 3.x 宿主上被
// 兼容预检直接拒绝（dev 的 coreVersion 现在从 git tag 派生），所以这里生成真实产物
// 再验一遍，而不是只断言模板字符串。
func TestMakePluginScaffoldDeclaresCompatibleReleaseLine(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "simple"},
		{name: "complex", args: []string{"--complex"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "acme.scaffold")
			args := append([]string{
				"make:plugin",
				"--id", "acme.scaffold",
				"--name", "Acme Scaffold",
				"--description", "Scaffold release line check.",
				"--url", "https://example.com/acme",
				"--author-name", "Acme",
				"--out", target,
				"--no-interaction",
			}, tc.args...)

			cmd := newRootCommand()
			cmd.SetArgs(args)
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("make:plugin: %v\n%s", err, out.String())
			}

			body, err := os.ReadFile(filepath.Join(target, extensionmanifest.ManifestFileName))
			if err != nil {
				t.Fatalf("read scaffold manifest: %v", err)
			}
			var manifest extensionmanifest.Manifest
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatalf("decode scaffold manifest: %v", err)
			}
			if manifest.SForumVersion != ">=0.1.0" {
				t.Fatalf("scaffold sforumVersion = %q, want >=0.1.0", manifest.SForumVersion)
			}

			validate := newRootCommand()
			validate.SetArgs([]string{"extension", "validate", target})
			var validateOut strings.Builder
			validate.SetOut(&validateOut)
			validate.SetErr(&validateOut)
			if err := validate.Execute(); err != nil {
				t.Fatalf("scaffold must pass extension validate: %v\n%s", err, validateOut.String())
			}
		})
	}
}

func writeCLIFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256HexString(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
