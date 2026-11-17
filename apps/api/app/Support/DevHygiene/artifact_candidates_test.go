package devhygiene_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
	"github.com/keeptrue/sforum/apps/api/internal/testartifact"
)

// 候选发现是 dev 自愈唯一的读盘面：它必须只认「自洽」的制品，并且在
// 构建窗口（backend/plugin 还没生成）时什么都不选。

const (
	candidateExtensionID = "fixture.plugin"
	candidateVersion     = "1.0.0"
)

// materializeArtifact 造一份自洽制品，返回目录与摘要。
func materializeArtifact(t *testing.T) testartifact.Coords {
	t.Helper()
	return testartifact.New(t).Materialize(t, candidateExtensionID, candidateVersion)
}

// relocateArtifact 把制品目录搬到另一个路径（内容与摘要不变），用于模拟
// 内置源 / 外部源那两种布局。
func relocateArtifact(t *testing.T, source, target string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create relocate parent: %v", err)
	}
	if err := os.Rename(source, target); err != nil {
		t.Fatalf("relocate artifact: %v", err)
	}
	return target
}

func TestFindSelfConsistentArtifactInInstalledDigestLayout(t *testing.T) {
	coords := materializeArtifact(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(coords.Path)))

	candidate, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: root, Layout: devhygiene.ArtifactRootDigests,
		}},
	)
	if !ok {
		t.Fatal("intact artifact in installed layout must be discovered")
	}
	if candidate.Digest != coords.Digest || candidate.Path != coords.Path {
		t.Fatalf("candidate=%#v coords=%#v", candidate, coords)
	}
	if len(candidate.Manifest) == 0 {
		t.Fatal("candidate must carry manifest bytes for durable registration")
	}
}

// 目录名必须等于树摘要：名字对不上说明它不是这个布局的正式制品（例如构建中的
// pending 目录），不能拿它当候选。
func TestFindSelfConsistentArtifactSkipsMislabelledDigestDirectory(t *testing.T) {
	coords := materializeArtifact(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(coords.Path)))
	renamed := relocateArtifact(t, coords.Path,
		filepath.Join(filepath.Dir(coords.Path), "pending-1"))

	if _, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: root, Layout: devhygiene.ArtifactRootDigests,
		}},
	); ok {
		t.Fatalf("mislabelled directory %s must not be a candidate", renamed)
	}
}

// 当前钉住的那份摘要永远不是候选：否则自愈会原地打转。
func TestFindSelfConsistentArtifactSkipsExcludedDigest(t *testing.T) {
	coords := materializeArtifact(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(coords.Path)))

	if _, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, coords.Digest, []devhygiene.ArtifactRoot{{
			Path: root, Layout: devhygiene.ArtifactRootDigests,
		}},
	); ok {
		t.Fatal("pinned digest must be excluded from candidates")
	}
}

// 构建窗口：build-builtin-plugins.sh 用 --exclude 'plugins/*/backend/plugin' 同步
// 源码树，入口二进制在这一刻不存在。树摘要会变、manifest 的 backend.digest 也对
// 不上，候选必须判为不可用。
func TestFindSelfConsistentArtifactRejectsIncompleteBuildWindow(t *testing.T) {
	coords := materializeArtifact(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(coords.Path)))
	if err := os.Remove(filepath.Join(coords.Path, "backend", "plugin")); err != nil {
		t.Fatalf("remove backend entry: %v", err)
	}

	if _, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: root, Layout: devhygiene.ArtifactRootDigests,
		}},
	); ok {
		t.Fatal("artifact missing backend/plugin must not be a candidate")
	}
}

// 内置源布局：storage/builtin-dev 下是 plugins/<id> / themes/<id>，本次坏的就是
// 内置插件，所以这条路径必须可被搜索。
func TestFindSelfConsistentArtifactInBuiltinSourceLayout(t *testing.T) {
	coords := materializeArtifact(t)
	builtinRoot := t.TempDir()
	target := relocateArtifact(t, coords.Path, filepath.Join(builtinRoot, "plugins", candidateExtensionID))

	candidate, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: builtinRoot, Layout: devhygiene.ArtifactRootBuiltin,
		}},
	)
	if !ok {
		t.Fatal("self-consistent builtin source package must be discovered")
	}
	if candidate.Path != target || candidate.Digest != coords.Digest {
		t.Fatalf("candidate=%#v want path=%s digest=%s", candidate, target, coords.Digest)
	}
}

// 外部源布局：EXTERNAL_EXTENSION_ROOTS 的每一项本身就是包目录。
func TestFindSelfConsistentArtifactInExternalPackageRoot(t *testing.T) {
	coords := materializeArtifact(t)
	externalRoot := filepath.Join(t.TempDir(), "acme.demo")
	relocateArtifact(t, coords.Path, externalRoot)

	candidate, ok := devhygiene.FindSelfConsistentArtifact(
		candidateExtensionID, candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: externalRoot, Layout: devhygiene.ArtifactRootPackage,
		}},
	)
	if !ok {
		t.Fatal("self-consistent external package root must be discovered")
	}
	if candidate.Path != externalRoot {
		t.Fatalf("candidate path=%s want %s", candidate.Path, externalRoot)
	}
}

// 身份不符（manifest id/version 与 durable 行不同）不能当候选：Verify 的身份判据
// 必须在这里生效，否则会把别的扩展制品登记成这个成员的版本行。
func TestFindSelfConsistentArtifactRejectsForeignIdentity(t *testing.T) {
	coords := materializeArtifact(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(coords.Path)))

	if _, ok := devhygiene.FindSelfConsistentArtifact(
		"other.plugin", candidateVersion, "", []devhygiene.ArtifactRoot{{
			Path: root, Layout: devhygiene.ArtifactRootDigests,
		}},
	); ok {
		t.Fatal("artifact belonging to another extension must not be a candidate")
	}
}
