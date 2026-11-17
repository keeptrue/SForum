package devhygiene

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

// ArtifactRootLayout 决定一个根目录里包是怎么摆放的。
type ArtifactRootLayout int

const (
	// ArtifactRootDigests 是已安装制品的不可变布局：<root>/<extension-id>/<version>/<digest>/。
	ArtifactRootDigests ArtifactRootLayout = iota
	// ArtifactRootBuiltin 是内置源布局：<root>/plugins/<extension-id>/ 或 <root>/themes/<id>/。
	// 本次事故坏的就是内置插件，所以内置源必须可被搜索，不能只扫外部源根。
	ArtifactRootBuiltin
	// ArtifactRootPackage 的根目录本身就是一个包（EXTERNAL_EXTENSION_ROOTS 的形态）。
	ArtifactRootPackage
)

// ArtifactRoot 是一处可搜索的制品/包根目录。
type ArtifactRoot struct {
	Path   string
	Layout ArtifactRootLayout
}

// ArtifactCandidate 是一份盘上自洽制品的坐标。
type ArtifactCandidate struct {
	ExtensionID string
	Version     string
	Digest      string
	Path        string
	// Manifest 是包根目录里那份 manifest 的字节，供调用方把候选登记成 durable 版本行。
	Manifest []byte
}

// FindSelfConsistentArtifact 在 roots 里寻找 (extensionID, version) 的另一份自洽制品。
//
// 「自洽」的判据只有一处：ArtifactIntegrity.Verify（目录名/树摘要、manifest 身份、
// backend 入口存在且 backend.digest 等于文件 sha256）。本函数只读——stat、读文件、
// 算摘要——绝不创建、删除或重写任何文件；也绝不返回 excludedDigest（当前钉住的
// 坏摘要）那一份，否则自愈会原地打转。
//
// 返回顺序是确定的（同布局内按目录名排序），便于测试与日志复现。
func FindSelfConsistentArtifact(
	extensionID, version, excludedDigest string,
	roots []ArtifactRoot,
) (ArtifactCandidate, bool) {
	for _, root := range roots {
		for _, dir := range artifactCandidateDirs(root, extensionID, version) {
			candidate, ok := inspectArtifactCandidate(extensionID, version, excludedDigest, root, dir)
			if ok {
				return candidate, true
			}
		}
	}
	return ArtifactCandidate{}, false
}

func artifactCandidateDirs(root ArtifactRoot, extensionID, version string) []string {
	rootPath := strings.TrimSpace(root.Path)
	if rootPath == "" {
		return nil
	}
	switch root.Layout {
	case ArtifactRootBuiltin:
		return []string{
			filepath.Join(rootPath, "plugins", extensionID),
			filepath.Join(rootPath, "themes", extensionID),
		}
	case ArtifactRootPackage:
		return []string{rootPath}
	default:
		versionRoot := filepath.Join(rootPath, extensionID, version)
		entries, err := os.ReadDir(versionRoot)
		if err != nil {
			return nil
		}
		dirs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dirs = append(dirs, filepath.Join(versionRoot, entry.Name()))
		}
		sort.Strings(dirs)
		return dirs
	}
}

func inspectArtifactCandidate(
	extensionID, version, excludedDigest string,
	root ArtifactRoot,
	dir string,
) (ArtifactCandidate, bool) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ArtifactCandidate{}, false
	}
	digest, err := extensionpackage.DigestTree(dir)
	if err != nil || digest == "" {
		return ArtifactCandidate{}, false
	}
	if strings.EqualFold(digest, excludedDigest) {
		return ArtifactCandidate{}, false
	}
	// 不可变布局里目录名就是摘要：名字对不上说明它不是这个布局的正式制品
	// （例如构建中的 pending 目录），不选它。
	if root.Layout == ArtifactRootDigests && !strings.EqualFold(filepath.Base(dir), digest) {
		return ArtifactCandidate{}, false
	}
	if err := artifactintegrity.Verify(dir, artifactintegrity.Member{
		ExtensionID: extensionID,
		Version:     version,
		Digest:      digest,
	}); err != nil {
		// 构建窗口（build-builtin-plugins.sh 的 --exclude 'plugins/*/backend/plugin'）
		// 里树是在的、入口不在，这里必须判为不可用，不允许退回「先用后修」。
		return ArtifactCandidate{}, false
	}
	manifest, err := os.ReadFile(filepath.Join(dir, extensionmanifest.ManifestFileName))
	if err != nil {
		return ArtifactCandidate{}, false
	}
	return ArtifactCandidate{
		ExtensionID: extensionID,
		Version:     version,
		Digest:      digest,
		Path:        dir,
		Manifest:    manifest,
	}, true
}
