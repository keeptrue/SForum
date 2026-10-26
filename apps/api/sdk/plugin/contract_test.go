package plugin

import (
	"os"
	"path/filepath"
	"testing"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestTestManifestUnknownEvent(t *testing.T) {
	manifest := extensionmanifest.Manifest{
		ID:            "acme.bad",
		Name:          "Bad",
		Description:   "Bad plugin for contract test.",
		URL:           "https://example.com",
		Author:        extensionmanifest.ManifestAuthor{Name: "Acme"},
		Version:       "1.0.0",
		Type:          extensionmanifest.TypePlugin,
		SForumVersion: ">=0.1.0",
		Events:        []extensionmanifest.ManifestEvent{{Name: "not.a.real.event"}},
	}
	// 先让 Validate 通过需要完整字段；未知事件在 Validate 阶段就会失败。
	// 这里直接 TestManifest 验证我们的契约层也会标 error。
	report := TestManifest(t.TempDir(), manifest, Options{SkipBackendBinary: true})
	if report.OK {
		t.Fatalf("expected failure for unknown event, checks=%#v", report.Checks)
	}
	found := false
	for _, c := range report.Checks {
		if c.Code == "event.unknown" && c.Level == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected event.unknown error, got %#v", report.Checks)
	}
}

func TestTestManifestScaffoldStubWarn(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/usr/bin/env sh\nprintf 'Build the HashiCorp go-plugin server for acme.demo into this file.\\n'\n"
	if err := os.WriteFile(filepath.Join(backend, "plugin"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := extensionmanifest.Manifest{
		ID:            "acme.demo",
		Name:          "Demo",
		Description:   "Scaffold stub contract.",
		URL:           "https://example.com",
		Author:        extensionmanifest.ManifestAuthor{Name: "Acme"},
		Version:       "0.1.0",
		Type:          extensionmanifest.TypePlugin,
		SForumVersion: ">=0.1.0",
		Backend: extensionmanifest.ManifestBackend{
			Entry: "backend/plugin",
			RPC:   "hashicorp-go-plugin",
		},
	}
	report := TestManifest(root, manifest, Options{})
	// stub 存在 → binary_present ok，并有 scaffold_stub warn；不应 error。
	if !report.OK {
		t.Fatalf("scaffold stub should not fail hard: %#v", report.Checks)
	}
	if report.Warnings == 0 {
		t.Fatalf("expected scaffold stub warning: %#v", report.Checks)
	}
}
