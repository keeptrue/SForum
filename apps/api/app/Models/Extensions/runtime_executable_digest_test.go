package extensions_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// backendArtifactFixture 写一个「已安装」的最小制品：<pkg>/backend/plugin。
// declaredDigest 空串表示 manifest 不声明摘要。
func backendArtifactFixture(t *testing.T, content string, declaredDigest string) extensions.Extension {
	t.Helper()
	packageRoot := t.TempDir()
	backendDir := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, "plugin"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if declaredDigest == "content" {
		declaredDigest = digestOf(content)
	}
	return extensions.Extension{
		ID:            "demo.plugin",
		Type:          extensions.TypePlugin,
		Status:        extensions.StatusEnabled,
		Source:        extensions.SourceUploaded,
		Version:       "1.0.0",
		PackageDigest: digestOf("package-" + content),
		PackagePath:   packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              "demo.plugin",
			Version:         "1.0.0",
			Backend: extensions.ManifestBackend{
				Entry:           "backend/plugin",
				RPC:             "hashicorp-go-plugin",
				ProtocolVersion: 2,
				HostAPIVersion:  "sforum.host@2",
				Digest:          declaredDigest,
			},
		},
	}
}

func digestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func backendEntryPath(extension extensions.Extension) string {
	return filepath.Join(extension.PackagePath, "backend", "plugin")
}

// allowed：内容未被改动时，exec 前复核必须放行，并返回按内容算出的摘要。
func TestVerifyRuntimeExecutableDigestAcceptsIntactArtifact(t *testing.T) {
	extension := backendArtifactFixture(t, "#!/bin/sh\nexit 0\n", "content")
	digest, err := extensions.VerifyRuntimeExecutableDigest(extension)
	if err != nil {
		t.Fatalf("intact artifact was rejected: %v", err)
	}
	if digest != digestOf("#!/bin/sh\nexit 0\n") {
		t.Fatalf("unexpected digest %q", digest)
	}
}

// allowed：内容不变、只是 mtime 变化（重新构建、cp -p 失败等）不该被误判为篡改。
func TestVerifyRuntimeExecutableDigestAllowsMetadataOnlyChange(t *testing.T) {
	extension := backendArtifactFixture(t, "#!/bin/sh\nexit 0\n", "content")
	if _, err := extensions.VerifyRuntimeExecutableDigest(extension); err != nil {
		t.Fatalf("warm-up verification failed: %v", err)
	}
	touched := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(backendEntryPath(extension), touched, touched); err != nil {
		t.Fatal(err)
	}
	if _, err := extensions.VerifyRuntimeExecutableDigest(extension); err != nil {
		t.Fatalf("metadata-only change was treated as tampering: %v", err)
	}
}

// denied：EX2-04b 的原始攻击——在 TTL 内改字节，同时把大小与 mtime 伪造回旧值，
// 让 size+mtime 指纹命中旧缓存。exec 前复核必须拒绝，而不是复用缓存结论。
func TestVerifyRuntimeExecutableDigestRejectsForgedFingerprint(t *testing.T) {
	const original = "#!/bin/sh\nexit 0\n"
	const tampered = "#!/bin/sh\nexit 7\n"
	if len(original) != len(tampered) {
		t.Fatalf("fixture must keep the byte length identical to forge the size field")
	}
	// 刻意不声明 manifest 摘要：这样「缓存摘要 vs 内容摘要」是唯一的检出手段，
	// 也就是本次修复本身；声明摘要那条锚点由下面的用例单独覆盖。
	extension := backendArtifactFixture(t, original, "")
	entry := backendEntryPath(extension)

	before, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	// 预热：把「content → digest」写进元数据指纹缓存。
	if _, err := extensions.VerifyRuntimeExecutableDigest(extension); err != nil {
		t.Fatalf("warm-up verification failed: %v", err)
	}
	if err := os.WriteFile(entry, []byte(tampered), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(entry, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("forgery setup failed: size/mtime still differ (%d/%d, %s/%s)",
			after.Size(), before.Size(), after.ModTime(), before.ModTime())
	}

	if _, err := extensions.VerifyRuntimeExecutableDigest(extension); !errors.Is(err, extensions.ErrFrontendPackageChanged) {
		t.Fatalf("forged fingerprint was not rejected: %v", err)
	}
	// 拒绝不能只拒绝「这一次」：被污染的缓存条目必须被丢弃，否则 TTL 内其它读路径
	// 还会拿到旧摘要。用导出接口回读，确认缓存已经重算到篡改后的内容。
	cached, err := extensionpackage.VerifiedFileDigest(entry)
	if err != nil {
		t.Fatalf("read back cached digest: %v", err)
	}
	if cached == digestOf(original) {
		t.Fatal("poisoned verification cache entry survived the rejection")
	}
}

// denied：manifest 声明的摘要与磁盘内容不一致（制品不是被授权的那一份）。
func TestVerifyRuntimeExecutableDigestRejectsDeclaredDigestMismatch(t *testing.T) {
	extension := backendArtifactFixture(t, "#!/bin/sh\nexit 0\n", digestOf("some-other-binary"))
	if _, err := extensions.VerifyRuntimeExecutableDigest(extension); !errors.Is(err, extensions.ErrFrontendPackageChanged) {
		t.Fatalf("declared digest mismatch was not rejected: %v", err)
	}
}
