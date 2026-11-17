package artifactintegrity_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

// 夹具源是仓库内的真实 Manifest V3 包（含 backend/plugin 与匹配的
// backend.digest），不依赖 storage/ 下的开发制品。
const verifyFixtureRelative = "extensions/fixtures/plugins/sforum-badge-facts"

// fixtureSource 从测试工作目录向上定位仓库根，避免把包深写死。
func fixtureSource(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for range 10 {
		candidate := filepath.Join(dir, verifyFixtureRelative)
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("fixture source %q not found above the test working directory", verifyFixtureRelative)
	return ""
}

// copyFixture 把夹具复制成 <root>/<id>/<version>/<digest> 形状，返回制品目录、
// 成员坐标与当前树摘要。
func copyFixture(t *testing.T) (string, artifactintegrity.Member) {
	t.Helper()
	source := fixtureSource(t)
	root := t.TempDir()
	target := filepath.Join(root, "sforum.badge-facts", "1.0.0", "placeholder")
	if err := copyTree(source, target); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := materializeBackendEntry(target); err != nil {
		t.Fatalf("materialize backend entry: %v", err)
	}
	digest, err := extensionpackage.DigestTree(target)
	if err != nil {
		t.Fatalf("digest fixture: %v", err)
	}
	final := filepath.Join(root, "sforum.badge-facts", "1.0.0", digest)
	if err := os.Rename(target, final); err != nil {
		t.Fatalf("name fixture by digest: %v", err)
	}
	return final, artifactintegrity.Member{
		ExtensionID: "sforum.badge-facts",
		Version:     "1.0.0",
		Digest:      digest,
	}
}

// materializeBackendEntry 保证被拷进来的包里一定有 backend/plugin。
//
// 为什么需要：fixture 源里的 `backend/plugin` 是**构建产物**，被
// `.gitignore` 的 `extensions/fixtures/plugins/*/backend/plugin` 忽略 ——
// 开发机上跑过构建就有，CI 上永远没有。测试直接依赖它会让「本地绿、CI 红」。
//
// 本地有真实二进制时原样保留（Verify 校验的是 digest 一致性，真文件更有代表性）；
// 没有时补一个占位二进制，并把 manifest 里 **两处** digest 同步成它的 sha256：
// `backend.digest`（verifyEntry 用它比对文件）与 `packageFiles[backend/plugin]`
// （LoadPackage 的严格装载会校验它）。两处不同步，后续的 DigestTree/LoadPackage 必红。
func materializeBackendEntry(packagePath string) error {
	entryPath := filepath.Join(packagePath, "backend", "plugin")
	if info, err := os.Stat(entryPath); err == nil && !info.IsDir() {
		return nil
	}
	body := []byte("#!/bin/sh\nexit 1\n")
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(entryPath, body, 0o755); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	entryDigest := hex.EncodeToString(sum[:])

	manifestPath := filepath.Join(packagePath, extensionmanifest.ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if backend, ok := doc["backend"].(map[string]any); ok {
		backend["digest"] = entryDigest
	}
	if files, ok := doc["packageFiles"].([]any); ok {
		for _, item := range files {
			file, ok := item.(map[string]any)
			if !ok || file["path"] != "backend/plugin" {
				continue
			}
			file["digest"] = entryDigest
		}
	}
	updated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(updated, '\n'), 0o644)
}

func copyTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		defer handle.Close()
		body, err := io.ReadAll(handle)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode().Perm())
	})
}

func TestVerifyAcceptsIntactArtifact(t *testing.T) {
	packagePath, member := copyFixture(t)
	if err := artifactintegrity.Verify(packagePath, member); err != nil {
		t.Fatalf("intact artifact must verify, got %v", err)
	}
}

// 形态一：目录在、入口不在（现场的 28/29 文件）。必须报入口缺失，而不是
// 被整树摘要变化先吃掉成一个不指位置的症状。
func TestVerifyRejectsMissingBackendEntry(t *testing.T) {
	packagePath, member := copyFixture(t)
	if err := os.Remove(filepath.Join(packagePath, "backend", "plugin")); err != nil {
		t.Fatalf("remove entry: %v", err)
	}

	err := artifactintegrity.Verify(packagePath, member)
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("missing entry must be an artifact error, got %v", err)
	}
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("missing entry must carry coordinates, got %v", err)
	}
	if coordinates.Path != "backend/plugin" || coordinates.Reason != "missing" {
		t.Fatalf("coordinates=%#v", coordinates)
	}
	if coordinates.ExtensionID != member.ExtensionID || coordinates.Digest != member.Digest {
		t.Fatalf("coordinates=%#v", coordinates)
	}
}

// 形态二：入口名对、内容被换掉。只 stat 的判据会放行，必须由摘要比对拦下。
func TestVerifyRejectsBackendEntryDigestMismatch(t *testing.T) {
	packagePath, member := copyFixture(t)
	entry := filepath.Join(packagePath, "backend", "plugin")
	if err := os.WriteFile(entry, []byte("substituted binary"), 0o755); err != nil {
		t.Fatalf("substitute entry: %v", err)
	}

	err := artifactintegrity.Verify(packagePath, member)
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("digest mismatch must carry coordinates, got %v", err)
	}
	if coordinates.Path != "backend/plugin" {
		t.Fatalf("coordinates=%#v", coordinates)
	}
	if !strings.HasPrefix(coordinates.Reason, "digest mismatch: want ") ||
		!strings.Contains(coordinates.Reason, " got ") {
		t.Fatalf("reason must record both digests, got %q", coordinates.Reason)
	}
}

// 目录整个不在：这是最弱的一种形态，不能拿它冒充上面两种。
func TestVerifyRejectsMissingDirectory(t *testing.T) {
	_, member := copyFixture(t)
	err := artifactintegrity.Verify(filepath.Join(t.TempDir(), "absent"), member)
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("missing directory must carry coordinates, got %v", err)
	}
	if coordinates.Reason != "artifact directory is missing" {
		t.Fatalf("reason=%q", coordinates.Reason)
	}
}

// 目录在、入口在、摘要对，但 durable 行冻结的树摘要与盘上不一致。
func TestVerifyRejectsTreeDigestMismatch(t *testing.T) {
	packagePath, member := copyFixture(t)
	member.Digest = strings.Repeat("a", 64)

	err := artifactintegrity.Verify(packagePath, member)
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("tree mismatch must carry coordinates, got %v", err)
	}
	if !strings.HasPrefix(coordinates.Reason, "tree digest mismatch: want ") {
		t.Fatalf("reason=%q", coordinates.Reason)
	}
}
