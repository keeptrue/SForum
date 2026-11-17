// Package testartifact 造「发布期制品完整性闸门」（issue #254）能接受的
// 真实自洽插件制品，供集成测试把 extension_versions.package_path 指过去。
//
// 为什么单独成包：闸门落地后，至少 app/Models/Extensions、app/Support/Extensions、
// cmd/sforum 三处夹具都必须让 durable 行指向盘上真实制品。判定只有一处
// （ArtifactIntegrity.Verify），构造手法也必须只有一处；否则某一份夹具漂移会
// 变成「这边绿、那边红」的假信号。
//
// 制品来源是仓库内真实的 Manifest V3 包。它的 backend/plugin 是构建产物，被
// .gitignore 忽略（开发机跑过构建才有，CI 永远没有），所以复制时跳过入口文件，
// 统一补一个占位二进制，并把 manifest 里两处 digest 同步成它的 sha256：
// backend.digest（Verify 的 verifyEntry 比对文件）与 packageFiles[backend/plugin]
// （manifest 严格装载会校验它）。两处任何一处不同步，DigestTree/LoadPackage 必红。
package testartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// fixtureRelative 是仓库内真实 Manifest V3 夹具包的相对路径（repo root 起算）。
const fixtureRelative = "extensions/fixtures/plugins/sforum-badge-facts"

// defaultBackendEntry 是绝大多数夹具 manifest 声明的入口路径。
const defaultBackendEntry = "backend/plugin"

// Coords 是一份已落盘制品的坐标。
type Coords struct {
	// Path 是制品目录：<root>/<id>/<version>/<digest> 形状。
	Path string
	// Digest 是制品目录树摘要（= 目录名）。
	Digest string
	// Manifest 是落盘那份 manifest 的字节，入口摘要已同步。
	Manifest []byte
}

// Factory 在一个临时根目录里造制品。
type Factory struct {
	root  string
	seq   int
	cache map[string]Coords
}

// New 创建工厂。根目录随测试清理。
func New(t *testing.T) *Factory {
	t.Helper()
	return NewWithRoot(t, t.TempDir())
}

// NewWithRoot 在调用方给定的根目录里造制品。
//
// 少数用例的 publication 会被同包其它用例继承（不可变 full-set 是全局账本），
// 那时制品生命周期必须长于单个测试，根目录就不能挂 t.TempDir()。
func NewWithRoot(t *testing.T, root string) *Factory {
	t.Helper()
	return &Factory{root: root, cache: make(map[string]Coords, 8)}
}

// Materialize 复制内置夹具包，把身份改写成 (extensionID, version)，返回坐标。
//
// 每次调用都产出不同内容：同一 (id, version) 需要「另一个自洽 digest」时直接
// 再调一次即可。
func (f *Factory) Materialize(t *testing.T, extensionID, version string) Coords {
	t.Helper()
	f.seq++
	base := filepath.Join(f.root, extensionID, version)
	pending := filepath.Join(base, fmt.Sprintf("pending-%d", f.seq))
	if err := copyTree(fixtureSource(t), pending); err != nil {
		t.Fatalf("copy artifact fixture for %s@%s: %v", extensionID, version, err)
	}
	entryDigest, err := writeBackendEntry(filepath.Join(pending, filepath.FromSlash(defaultBackendEntry)))
	if err != nil {
		t.Fatalf("materialize backend entry for %s@%s: %v", extensionID, version, err)
	}
	manifestPath := filepath.Join(pending, extensionmanifest.ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read fixture manifest for %s@%s: %v", extensionID, version, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode fixture manifest for %s@%s: %v", extensionID, version, err)
	}
	sourceID, _ := document["id"].(string)
	document["id"] = extensionID
	document["version"] = version
	// Manifest V3 校验 versionedID 的「归属」：每个声明的 id 必须挂在清单 id 前缀下。
	// 所以改身份不能只改 id 字段，所有以旧 id 开头的字符串都要同步改前缀。
	if sourceID != "" && sourceID != extensionID {
		rewriteIDPrefix(document, sourceID, extensionID)
	}
	// 序号写进自由文本字段：同 (id, version) 的重复 materialize 必须树摘要不同。
	if description, ok := document["description"].(string); ok {
		document["description"] = fmt.Sprintf("%s (fixture artifact %d)", description, f.seq)
	}
	syncBackendDigest(document, defaultBackendEntry, entryDigest)
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode fixture manifest for %s@%s: %v", extensionID, version, err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture manifest for %s@%s: %v", extensionID, version, err)
	}
	path, digest := seal(t, base, pending)
	return Coords{Path: path, Digest: digest, Manifest: body}
}

// MaterializeOnce 幂等地为 (extensionID, version) 造制品：同一工厂内重复调用
// 返回同一坐标。
//
// SaveBuiltin 这类「重复同步必须幂等」的用例依赖它：digest 一漂移就会多插一条
// 版本行，用例要断言的「不追加 revision」就失真了。
func (f *Factory) MaterializeOnce(t *testing.T, extensionID, version string) Coords {
	t.Helper()
	key := extensionID + "@" + version
	if cached, ok := f.cache[key]; ok {
		return cached
	}
	coords := f.Materialize(t, extensionID, version)
	f.cache[key] = coords
	return coords
}

// MaterializeManifest 用调用方给定的 manifest 从零落一份制品。
//
// files 是除 backend 入口外的附加文件（路径用正斜杠、相对包根），按调用方给定
// 的内容原样写入磁盘；入口文件的占位二进制与 manifest 两处 digest 的同步由本
// 函数负责，调用方不用手算。返回的 Coords.Manifest 是同步后的 manifest 字节，
// 调用方应把它写进 durable 行，避免 DB 与磁盘各说一套。
func (f *Factory) MaterializeManifest(
	t *testing.T,
	manifest []byte,
	files map[string][]byte,
) Coords {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(manifest, &document); err != nil {
		t.Fatalf("decode caller manifest: %v", err)
	}
	extensionID, _ := document["id"].(string)
	version, _ := document["version"].(string)
	if extensionID == "" || version == "" {
		t.Fatalf("caller manifest must declare id and version")
	}
	entry := defaultBackendEntry
	if backend, ok := document["backend"].(map[string]any); ok {
		if declared, ok := backend["entry"].(string); ok && declared != "" {
			entry = declared
		}
	}
	f.seq++
	base := filepath.Join(f.root, extensionID, version)
	pending := filepath.Join(base, fmt.Sprintf("pending-%d", f.seq))
	if err := writeArtifactFiles(pending, files); err != nil {
		t.Fatalf("write caller artifact files for %s@%s: %v", extensionID, version, err)
	}
	if _, provided := files[entry]; !provided {
		entryDigest, err := writeBackendEntry(filepath.Join(pending, filepath.FromSlash(entry)))
		if err != nil {
			t.Fatalf("materialize backend entry for %s@%s: %v", extensionID, version, err)
		}
		syncBackendDigest(document, entry, entryDigest)
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode caller manifest for %s@%s: %v", extensionID, version, err)
	}
	if err := os.WriteFile(
		filepath.Join(pending, extensionmanifest.ManifestFileName), append(body, '\n'), 0o644,
	); err != nil {
		t.Fatalf("write caller manifest for %s@%s: %v", extensionID, version, err)
	}
	path, digest := seal(t, base, pending)
	return Coords{Path: path, Digest: digest, Manifest: body}
}

// seal 把 pending 目录按树摘要改名成 <base>/<digest>，返回最终路径与摘要。
func seal(t *testing.T, base, pending string) (string, string) {
	t.Helper()
	digest, err := extensionpackage.DigestTree(pending)
	if err != nil {
		t.Fatalf("digest artifact %s: %v", pending, err)
	}
	final := filepath.Join(base, digest)
	if err := os.Rename(pending, final); err != nil {
		t.Fatalf("name artifact %s by digest: %v", pending, err)
	}
	return final, digest
}

// fixtureSource 从测试工作目录向上定位仓库内的夹具包。
func fixtureSource(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for range 10 {
		candidate := filepath.Join(dir, fixtureRelative)
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("artifact fixture source %q not found above the test working directory", fixtureRelative)
	return ""
}

// copyTree 复制制品源，跳过 backend 入口二进制（24MB，且本地/CI 不一致）。
func copyTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == defaultBackendEntry {
			return nil
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

func writeArtifactFiles(root string, files map[string][]byte) error {
	for path, body := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeBackendEntry(entryPath string) (string, error) {
	body := []byte("#!/bin/sh\nexit 1\n")
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(entryPath, body, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// syncBackendDigest 同步 manifest 里两处入口摘要：backend.digest 与
// packageFiles[entry].digest。只改一处，后面的严格装载或 verifyEntry 必红。
func syncBackendDigest(document map[string]any, entry, entryDigest string) {
	if backend, ok := document["backend"].(map[string]any); ok {
		backend["digest"] = entryDigest
	}
	files, ok := document["packageFiles"].([]any)
	if !ok {
		return
	}
	for _, item := range files {
		file, ok := item.(map[string]any)
		if !ok || file["path"] != entry {
			continue
		}
		file["digest"] = entryDigest
	}
}

// rewriteIDPrefix 把 manifest 里所有以 sourceID 开头的字符串改成 targetID 前缀。
//
// V3 校验要求每个声明的 versionedID（events、permissions、packageFiles、provider
// 契约等）归属于清单 id；只改顶层 id 会让严格装载在第一个声明处失败。
func rewriteIDPrefix(value any, sourceID, targetID string) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok {
				node[key] = rewriteIDString(text, sourceID, targetID)
				continue
			}
			rewriteIDPrefix(child, sourceID, targetID)
		}
	case []any:
		for index, child := range node {
			if text, ok := child.(string); ok {
				node[index] = rewriteIDString(text, sourceID, targetID)
				continue
			}
			rewriteIDPrefix(child, sourceID, targetID)
		}
	}
}

func rewriteIDString(text, sourceID, targetID string) string {
	prefix := sourceID + "."
	if !strings.HasPrefix(text, prefix) {
		return text
	}
	return targetID + "." + strings.TrimPrefix(text, prefix)
}
