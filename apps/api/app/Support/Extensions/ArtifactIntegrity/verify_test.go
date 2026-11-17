package artifactintegrity_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
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
