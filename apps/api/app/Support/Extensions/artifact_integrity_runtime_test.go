package extensionsruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

// 缺 backend 入口的制品必须产出类型化错误，而不是裸字符串：issue #253 的现场
// 就是这条路径上的 fmt.Errorf 让恢复码落回 default。
func TestRuntimeSupervisorCheckMissingEntryIsTyped(t *testing.T) {
	root := t.TempDir()
	extension := extensions.Extension{
		ID:            "sforum-shortcodes",
		Version:       "1.3.10",
		Type:          extensions.TypePlugin,
		PackageDigest: "b4f399bdbd8f82304dae57b7615293068c030bf42feed72c54dfd380525d6d74",
		PackagePath:   root,
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "backend/plugin"},
		},
	}

	var supervisor RuntimeSupervisor
	err := supervisor.Check(context.Background(), extension)
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("missing backend entry must be a typed artifact error, got %v", err)
	}
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("typed artifact error must expose coordinates, got %v", err)
	}
	if coordinates.ExtensionID != extension.ID || coordinates.Version != extension.Version ||
		coordinates.Digest != extension.PackageDigest || coordinates.Path != "backend/plugin" {
		t.Fatalf("coordinates=%#v", coordinates)
	}
}

// 入口在位时 Check 仍然放行，且不产生任何类型化错误。
func TestRuntimeSupervisorCheckPassesWhenEntryExists(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "backend", "plugin")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatalf("prepare entry dir: %v", err)
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	extension := extensions.Extension{
		ID:          "sforum-shortcodes",
		Version:     "1.3.10",
		Type:        extensions.TypePlugin,
		PackagePath: root,
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "backend/plugin"},
		},
	}

	var supervisor RuntimeSupervisor
	if err := supervisor.Check(context.Background(), extension); err != nil {
		t.Fatalf("existing entry must pass the check, got %v", err)
	}
}

// 路径解析拒绝仍是 ErrInvalidManifest，但同时被标记为制品不完整。
func TestRuntimeSupervisorCheckRejectedPathKeepsInvalidManifest(t *testing.T) {
	extension := extensions.Extension{
		ID:          "sforum-shortcodes",
		Version:     "1.3.10",
		Type:        extensions.TypePlugin,
		PackagePath: t.TempDir(),
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "../escape/plugin"},
		},
	}

	var supervisor RuntimeSupervisor
	err := supervisor.Check(context.Background(), extension)
	if !errors.Is(err, extensions.ErrInvalidManifest) {
		t.Fatalf("rejected entry path must keep ErrInvalidManifest, got %v", err)
	}
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("rejected entry path must be a typed artifact error, got %v", err)
	}
}
