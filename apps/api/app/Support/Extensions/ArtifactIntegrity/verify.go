package artifactintegrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// Member 是一份 publication 成员的制品坐标。路径不在这里：它由 durable 行
// （extension_versions.package_path）给出，本包不猜目录布局。
type Member struct {
	ExtensionID string
	Version     string
	Digest      string
}

// manifestProbe 是只取定位信息的最小投影。
//
// 它必须绕过 extensionmanifest.LoadPackage：严格装载会逐条读取 packageFiles
// 并校验其存在与摘要，于是一份「入口缺失」的制品在严格装载处就已经失败，
// 拿不到 entry/digest 这两个坐标，最后只能报一句无法定位的「manifest 不可读」。
type manifestProbe struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Backend struct {
		Entry  string `json:"entry"`
		Digest string `json:"digest"`
	} `json:"backend"`
}

func readManifestProbe(packagePath string) (manifestProbe, error) {
	body, err := os.ReadFile(filepath.Join(packagePath, extensionmanifest.ManifestFileName))
	if err != nil {
		return manifestProbe{}, err
	}
	var probe manifestProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return manifestProbe{}, err
	}
	return probe, nil
}

// Verify 判定一份已安装制品当前是否可装载，并按需返回带坐标的错误。
//
// 判据与运行时装载同构，四条缺一不可：
//  1. 制品目录存在且是目录；
//  2. manifest 可解析且身份（id/version）与 durable 行一致；
//  3. 声明的 backend entry 存在、是普通文件，且 sha256 等于 manifest 的
//     backend.digest；
//  4. 整树摘要等于 durable 行冻结的 <digest>（权限位也参与摘要）。
//
// 顺序是有意的：入口类缺陷（2/3）先于整树摘要（4）判定。缺入口或换掉入口
// 必然同时改变整树摘要，若先报「树摘要不符」，现场就只剩一个不指位置的症状；
// 先报入口，message 直接给出 path 与 want/got。
//
// 第 3 条的摘要比对是这一族里最容易漏的一条：只 stat 入口会把「文件名对、
// 内容被换掉」判成健康，随后收敛期才在子进程启动处炸出无法定位的失败。
func Verify(packagePath string, member Member) error {
	packagePath = strings.TrimSpace(packagePath)
	if packagePath == "" {
		return New(member.ExtensionID, member.Version, member.Digest, ".",
			"package path is empty", ErrArtifactUnusable)
	}
	info, err := os.Stat(packagePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(member.ExtensionID, member.Version, member.Digest, ".",
				"artifact directory is missing", ErrArtifactUnusable, err)
		}
		return New(member.ExtensionID, member.Version, member.Digest, ".",
			"artifact directory is not accessible", ErrArtifactUnusable, err)
	}
	if !info.IsDir() {
		return New(member.ExtensionID, member.Version, member.Digest, ".",
			"artifact path is not a directory", ErrArtifactUnusable)
	}

	probe, err := readManifestProbe(packagePath)
	if err != nil {
		return New(member.ExtensionID, member.Version, member.Digest, extensionmanifest.ManifestFileName,
			"manifest is unreadable", ErrArtifactUnusable, err)
	}
	if probe.ID != member.ExtensionID || probe.Version != member.Version {
		return New(member.ExtensionID, member.Version, member.Digest, extensionmanifest.ManifestFileName,
			fmt.Sprintf("manifest identity mismatch: got %s@%s", probe.ID, probe.Version),
			ErrArtifactUnusable)
	}

	entry := strings.TrimSpace(probe.Backend.Entry)
	if entry != "" {
		if err := verifyEntry(packagePath, member, entry, strings.TrimSpace(probe.Backend.Digest)); err != nil {
			return err
		}
	}

	// 严格装载兜住其余形态：packageFiles 声明的任一文件被增删改都会在这里失败。
	if _, err := extensionmanifest.LoadPackage(packagePath); err != nil {
		return New(member.ExtensionID, member.Version, member.Digest, extensionmanifest.ManifestFileName,
			"manifest is unreadable", ErrArtifactUnusable, err)
	}

	digest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		return New(member.ExtensionID, member.Version, member.Digest, ".",
			"artifact content cannot be digested", ErrArtifactUnusable, err)
	}
	if member.Digest != "" && !strings.EqualFold(digest, member.Digest) {
		return New(member.ExtensionID, member.Version, member.Digest, ".",
			fmt.Sprintf("tree digest mismatch: want %s got %s", member.Digest, digest),
			ErrArtifactUnusable)
	}
	return nil
}

// verifyEntry 校验 backend entry 文件本身：路径合法、存在且是普通文件、摘要
// 等于 manifest 声明值。三条各自对应不同的修复动作，因此各自成一种 Reason。
func verifyEntry(packagePath string, member Member, entry, wantDigest string) error {
	safe, ok := extensionmanifest.SafeArchivePath(entry)
	if !ok || safe != entry {
		return EntryPathRejected(member.ExtensionID, member.Version, member.Digest, entry)
	}
	target := filepath.Join(packagePath, filepath.FromSlash(safe))
	info, err := os.Stat(target)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return EntryUnavailable(member.ExtensionID, member.Version, member.Digest, entry, err)
	}
	if wantDigest == "" {
		return nil
	}
	got, err := fileSHA256(target)
	if err != nil {
		return EntryUnavailable(member.ExtensionID, member.Version, member.Digest, entry, err)
	}
	if !strings.EqualFold(got, wantDigest) {
		return EntryDigestMismatch(member.ExtensionID, member.Version, member.Digest, entry, wantDigest, got)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
