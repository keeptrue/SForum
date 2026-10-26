package extensionmanifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseIdentity() map[string]any {
	return map[string]any{
		"manifestVersion": ManifestVersionV3,
		"id":              "demo.plugin",
		"name":            "Demo Plugin",
		"description":     "Demo description.",
		"url":             "https://example.com/demo",
		"author":          map[string]any{"name": "Demo Author"},
		"version":         "1.0.0",
		"type":            "plugin",
		"sforumVersion":   ">=0.1.0",
	}
}

func writePackage(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body) + "\n"
}

func TestLoadPackageWithoutIncludesParity(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["settings"] = []map[string]any{{
		"key":   "enabled",
		"label": "Enabled",
		"type":  "boolean",
	}}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
	})

	loaded, err := LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if loaded.ID != "demo.plugin" || len(loaded.Settings) != 1 {
		t.Fatalf("unexpected loaded manifest: %#v", loaded)
	}

	// 与直接 Unmarshal + Validate 一致
	raw, _ := os.ReadFile(filepath.Join(root, ManifestFileName))
	var direct Manifest
	if err := json.Unmarshal(raw, &direct); err != nil {
		t.Fatal(err)
	}
	if err := Validate(direct); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPackageLangsDirectory(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["includes"] = map[string]any{"langs": "manifest/langs"}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
		"manifest/langs/zh-CN.json": mustJSON(t, map[string]any{
			"name":        "演示插件",
			"description": "演示描述。",
		}),
		"manifest/langs/en-US.json": mustJSON(t, map[string]any{
			"name": "Demo Plugin EN",
		}),
	})

	loaded, err := LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if _, ok := loaded.Langs["zh-CN"]; !ok {
		t.Fatalf("expected zh-CN, got %#v", loaded.Langs)
	}
	if _, ok := loaded.Langs["en-US"]; !ok {
		t.Fatalf("expected en-US, got %#v", loaded.Langs)
	}
	zh := LocalizedDisplay(loaded, "zh-CN")
	if zh.Name != "演示插件" {
		t.Fatalf("zh display name = %q", zh.Name)
	}
	en := LocalizedDisplay(loaded, "en-US")
	if en.Name != "Demo Plugin EN" {
		t.Fatalf("en display name = %q", en.Name)
	}
}

func TestLoadPackageLangsFileListAndMapFile(t *testing.T) {
	t.Run("file list", func(t *testing.T) {
		root := t.TempDir()
		identity := baseIdentity()
		identity["includes"] = map[string]any{
			"langs": []string{"manifest/langs/zh-CN.json"},
		}
		writePackage(t, root, map[string]string{
			ManifestFileName: mustJSON(t, identity),
			"manifest/langs/zh-CN.json": mustJSON(t, map[string]any{
				"name": "列表中文名",
			}),
		})
		loaded, err := LoadPackage(root)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Langs["zh-CN"].Name != "列表中文名" {
			t.Fatalf("got %#v", loaded.Langs)
		}
	})

	t.Run("single map file", func(t *testing.T) {
		root := t.TempDir()
		identity := baseIdentity()
		identity["includes"] = map[string]any{"langs": "manifest/langs.json"}
		writePackage(t, root, map[string]string{
			ManifestFileName: mustJSON(t, identity),
			"manifest/langs.json": mustJSON(t, map[string]any{
				"zh": map[string]any{"name": "宽语言中文"},
			}),
		})
		loaded, err := LoadPackage(root)
		if err != nil {
			t.Fatal(err)
		}
		// zh-CN 应回退到 zh
		if LocalizedDisplay(loaded, "zh-CN").Name != "宽语言中文" {
			t.Fatalf("fallback failed: %#v", LocalizedDisplay(loaded, "zh-CN"))
		}
	})
}

func TestLoadPackageDualSourceFails(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["langs"] = map[string]any{"zh": map[string]any{"name": "根级"}}
	identity["includes"] = map[string]any{"langs": "manifest/langs"}
	writePackage(t, root, map[string]string{
		ManifestFileName:            mustJSON(t, identity),
		"manifest/langs/zh-CN.json": mustJSON(t, map[string]any{"name": "分文件"}),
	})
	_, err := LoadPackage(root)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "dual source") {
		t.Fatalf("expected dual source message, got %v", err)
	}
}

func TestLoadPackageSettingsContributionsAndAdmin(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["includes"] = map[string]any{
		"settings":      "manifest/settings.json",
		"contributions": "manifest/contributions.json",
		"admin":         "manifest/admin.json",
	}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
		"manifest/settings.json": mustJSON(t, []map[string]any{{
			"key":   "host",
			"label": map[string]string{"zh-CN": "主机", "en-US": "Host"},
			"type":  "text",
		}}),
		"manifest/contributions.json": mustJSON(t, []map[string]any{{
			"point":   PointAdminDashboardWidgets,
			"id":      "custom-link",
			"order":   10,
			"label":   map[string]string{"zh-CN": "自定义链接", "en-US": "Custom link"},
			"payload": map[string]string{"type": "adminLink", "route": "/extensions"},
		}}),
		"manifest/admin.json": mustJSON(t, map[string]any{
			"entry": "/settings",
			"pages": []map[string]any{{
				"path":  "/settings",
				"label": "Settings",
				"view":  "settings",
			}},
		}),
	})

	loaded, err := LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if len(loaded.Settings) != 1 || loaded.Settings[0].Key != "host" {
		t.Fatalf("settings: %#v", loaded.Settings)
	}
	if len(loaded.Contributions) != 1 {
		t.Fatalf("contributions: %#v", loaded.Contributions)
	}
	if loaded.Admin.Entry != "/settings" {
		t.Fatalf("admin: %#v", loaded.Admin)
	}
}

func TestLoadPackageSettingsDirectoryShards(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["includes"] = map[string]any{"settings": "manifest/settings"}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
		"manifest/settings/10-server.json": mustJSON(t, []map[string]any{{
			"key": "host", "label": "Host", "type": "text",
		}}),
		"manifest/settings/20-auth.json": mustJSON(t, []map[string]any{{
			"key": "username", "label": "User", "type": "text",
		}}),
	})
	loaded, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Settings) != 2 {
		t.Fatalf("expected 2 settings, got %d", len(loaded.Settings))
	}
	if loaded.Settings[0].Key != "host" || loaded.Settings[1].Key != "username" {
		t.Fatalf("order wrong: %#v", loaded.Settings)
	}
}

func TestLoadPackageRejectsPathTraversalAndEmptyLangsDir(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		root := t.TempDir()
		identity := baseIdentity()
		identity["includes"] = map[string]any{"settings": "../outside.json"}
		writePackage(t, root, map[string]string{ManifestFileName: mustJSON(t, identity)})
		_, err := LoadPackage(root)
		if !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected invalid, got %v", err)
		}
	})

	t.Run("empty langs dir", func(t *testing.T) {
		root := t.TempDir()
		identity := baseIdentity()
		identity["includes"] = map[string]any{"langs": "manifest/langs"}
		writePackage(t, root, map[string]string{ManifestFileName: mustJSON(t, identity)})
		if err := os.MkdirAll(filepath.Join(root, "manifest/langs"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := LoadPackage(root)
		if !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected invalid, got %v", err)
		}
	})

	t.Run("non-json in langs dir", func(t *testing.T) {
		root := t.TempDir()
		identity := baseIdentity()
		identity["includes"] = map[string]any{"langs": "manifest/langs"}
		writePackage(t, root, map[string]string{
			ManifestFileName:            mustJSON(t, identity),
			"manifest/langs/zh-CN.json": mustJSON(t, map[string]any{"name": "中文"}),
			"manifest/langs/readme.md":  "nope",
		})
		_, err := LoadPackage(root)
		if !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected invalid, got %v", err)
		}
	})
}

func TestLoadPackageDuplicateSettingKeysInShards(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["includes"] = map[string]any{"settings": "manifest/settings"}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
		"manifest/settings/a.json": mustJSON(t, []map[string]any{{
			"key": "host", "label": "A", "type": "text",
		}}),
		"manifest/settings/b.json": mustJSON(t, []map[string]any{{
			"key": "host", "label": "B", "type": "text",
		}}),
	})
	_, err := LoadPackage(root)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestLoadRootBytesWithFileMapFS(t *testing.T) {
	identity := baseIdentity()
	identity["includes"] = map[string]any{
		"langs":    "manifest/langs",
		"settings": "manifest/settings.json",
	}
	rootBody := mustJSON(t, identity)
	files := FileMapFS{
		ManifestFileName: []byte(rootBody),
		"manifest/langs/zh-CN.json": []byte(mustJSON(t, map[string]any{
			"name": "ZIP 中文",
		})),
		"manifest/settings.json": []byte(mustJSON(t, []map[string]any{{
			"key": "port", "label": "Port", "type": "number", "default": "587",
		}})),
	}
	loaded, err := LoadPackageFS(files)
	if err != nil {
		t.Fatalf("LoadPackageFS: %v", err)
	}
	if loaded.Langs["zh-CN"].Name != "ZIP 中文" {
		t.Fatalf("langs: %#v", loaded.Langs)
	}
	if len(loaded.Settings) != 1 || loaded.Settings[0].Key != "port" {
		t.Fatalf("settings: %#v", loaded.Settings)
	}
}

func TestLoadPackageDualSourceSettingsFails(t *testing.T) {
	root := t.TempDir()
	identity := baseIdentity()
	identity["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text"}}
	identity["includes"] = map[string]any{"settings": "manifest/settings.json"}
	writePackage(t, root, map[string]string{
		ManifestFileName: mustJSON(t, identity),
		"manifest/settings.json": mustJSON(t, []map[string]any{{
			"key": "b", "label": "B", "type": "text",
		}}),
	})
	_, err := LoadPackage(root)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid, got %v", err)
	}
}
