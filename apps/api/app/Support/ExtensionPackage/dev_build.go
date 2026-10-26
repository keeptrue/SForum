package extensionpackage

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

// DevBuild 是「dev 自动激活」构建阶段的公共实现（host dev watcher 与
// `sforum extension build/dev` CLI 共用）：
//   - BuildBackend：backend/ 有 Go 源码时执行 go build -trimpath -buildvcs=false -o backend/plugin ./；
//   - WriteDigest：等价于 `sforum extension digest --write`（含 assets[].integrity
//     的 SRI 重算与 inline 声明摘要同步）。
//
// 放在 Support 层是因为 host 进程不能 import cmd/sforum 的 main 包。
// 环境门（仅 development 使用）由调用方（bootstrap dev 装配）负责。

// BuildBackend 构建包的后端二进制。backend 目录不存在或没有 Go 文件时是无害 no-op。
func BuildBackend(packageRoot string) error {
	backendDir := filepath.Join(packageRoot, "backend")
	entries, err := os.ReadDir(backendDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read backend dir: %w", err)
	}
	hasGo := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			hasGo = true
			break
		}
	}
	if !hasGo {
		return nil
	}
	binary := filepath.Join(backendDir, "plugin")
	// 先构建到 .tmp 产物:dev watcher 忽略 .tmp 事件,且产物与现役二进制字节
	// 一致时不替换 backend/plugin——否则每次重建都会自触发 watch 事件,
	// 形成「构建 → 写产物 → 事件 → 重建」的稳态循环。
	staging := binary + ".tmp"
	plan, err := newBackendBuildPlan(backendDir)
	if err != nil {
		return err
	}
	defer plan.Cleanup()
	cmd := exec.Command("go", backendBuildArgs(staging)...)
	cmd.Dir = backendDir
	cmd.Env = plan.Env
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(staging)
		return fmt.Errorf("go build backend: %v: %s", err, strings.TrimSpace(string(output)))
	}
	identical, err := filesHaveEqualBytes(staging, binary)
	if err != nil {
		_ = os.Remove(staging)
		return err
	}
	if identical {
		_ = os.Remove(staging)
		return nil
	}
	return os.Rename(staging, binary)
}

// backendBuildArgs 是 dev 后端构建的完整 argv。
//
// 它必须与发行构建脚本（sforum-app-store `plugin/scripts/build.sh`）逐参数一致：
// 同一棵插件树有两个写者（host dev watcher 与发行脚本），任何一处口径差异都会让
// 两边算出不同的二进制摘要，进而让 manifest 永远在两个 digest 之间抖动。
// `-ldflags="-s -w"` 是历史上唯一缺的一项；`-trimpath`/`-buildvcs=false` 保证可复现。
func backendBuildArgs(staging string) []string {
	return []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", staging, "./"}
}

// backendBuildPlan 是一次 dev 后端构建的完整环境。
//
// 两个构建者（host dev watcher 与本包的 BuildBackend、发行脚本
// sforum-app-store 的 plugin/scripts/build.sh）必须产出逐字节相同的二进制，
// 否则 manifest 的 backend.digest 会在两个值之间抖动。有两个变量必须消掉：
//
//  1. Go 工具链。`go` 指令只是下限，本机 Go 更新时会用本机版本，而 CI 用
//     setup-go 按同一条指令装的精确版本。⇒ GOTOOLCHAIN 钉到 go.mod 的 go 行。
//  2. replace 的文件系统路径。Go 会把它逐字写进 buildinfo（`go version -m` 可见
//     `=> /path/to/module`），`-trimpath` 不处理它；而本地兄弟布局与 CI 的嵌套
//     布局深度必然不同。⇒ 用临时 go.work 承载同一组模块，workspace 解析不经过
//     replace，路径因此不进二进制。
type backendBuildPlan struct {
	// Env 是构建进程的完整环境。
	Env []string
	// Work 是临时 go.work 路径；空表示该模块不需要 workspace。
	Work string

	cleanup func()
}

// Cleanup 删除为本次构建创建的临时 workspace；重复调用安全。
func (p *backendBuildPlan) Cleanup() {
	if p != nil && p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// newBackendBuildPlan 从目标模块的 go.mod 派生构建环境。
//
// 没有 `go` 行就不钉工具链；没有文件系统 replace 就不建 workspace（显式
// GOWORK=off，避免被上层目录的 go.work 意外影响）。
func newBackendBuildPlan(backendDir string) (*backendBuildPlan, error) {
	env := append(os.Environ(), "CGO_ENABLED=0")
	plan := &backendBuildPlan{Env: env}

	goVersion, replacePaths := readBackendGoMod(filepath.Join(backendDir, "go.mod"))
	if goVersion != "" {
		plan.Env = append(plan.Env, "GOTOOLCHAIN=go"+goVersion)
	}
	if len(replacePaths) == 0 {
		plan.Env = append(plan.Env, "GOWORK=off")
		return plan, nil
	}

	absoluteRoot, err := filepath.Abs(backendDir)
	if err != nil {
		return nil, fmt.Errorf("resolve module root: %w", err)
	}
	var uses []string
	uses = append(uses, absoluteRoot)
	for _, relative := range replacePaths {
		resolved := relative
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(absoluteRoot, relative)
		}
		uses = append(uses, filepath.Clean(resolved))
	}

	dir, err := os.MkdirTemp("", "sforum-plugin-work-")
	if err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	workPath := filepath.Join(dir, "go.work")
	var body strings.Builder
	if goVersion != "" {
		body.WriteString("go " + goVersion + "\n\n")
	}
	body.WriteString("use (\n")
	for _, use := range uses {
		body.WriteString("\t" + use + "\n")
	}
	body.WriteString(")\n")
	if err := os.WriteFile(workPath, []byte(body.String()), 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write workspace file: %w", err)
	}
	plan.Work = workPath
	plan.Env = append(plan.Env, "GOWORK="+workPath)
	plan.cleanup = func() { _ = os.RemoveAll(dir) }
	return plan, nil
}

// readBackendGoMod 从 go.mod 里取出 `go` 行与所有文件系统 replace 目标。
//
// 只认路径形式（`./x`、`../x`、`/x`）；模块版本形式的 replace 不影响构建路径，
// 交给默认解析即可。解析失败一律返回空，调用方据此退回默认行为。
func readBackendGoMod(path string) (goVersion string, replacePaths []string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	inReplaceBlock := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if inReplaceBlock {
			if trimmed == ")" {
				inReplaceBlock = false
				continue
			}
			if target, ok := replaceTargetPath(trimmed); ok {
				replacePaths = append(replacePaths, target)
			}
			continue
		}
		if trimmed == "replace (" {
			inReplaceBlock = true
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 2 && fields[0] == "go" && goVersion == "" {
			// 只有 x.y.z 才钉工具链：`go 1.26` 这种两段式在 GOTOOLCHAIN 里是
			// 非法/歧义取值，会让 go 去下载一个不存在的补丁版本。
			if strings.Count(fields[1], ".") == 2 {
				goVersion = fields[1]
			}
			continue
		}
		if len(fields) > 1 && fields[0] == "replace" {
			if target, ok := replaceTargetPath(strings.TrimPrefix(trimmed, "replace ")); ok {
				replacePaths = append(replacePaths, target)
			}
		}
	}
	return goVersion, replacePaths
}

// replaceTargetPath 从 `old [version] => target` 里取出文件系统目标。
func replaceTargetPath(spec string) (string, bool) {
	arrow := strings.Index(spec, "=>")
	if arrow < 0 {
		return "", false
	}
	target := strings.TrimSpace(spec[arrow+2:])
	if target == "" || strings.HasPrefix(target, ".") == false && strings.HasPrefix(target, "/") == false {
		return "", false
	}
	return target, true
}

// filesHaveEqualBytes 判断两个文件内容是否逐字节一致;任一不存在视为不一致。
func filesHaveEqualBytes(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if infoA.Size() != infoB.Size() {
		return false, nil
	}
	sumA, err := fileSHA256(a)
	if err != nil {
		return false, err
	}
	sumB, err := fileSHA256(b)
	if err != nil {
		return false, err
	}
	return sumA == sumB, nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest.Sum(nil))
	return sum, nil
}

// WriteDigest 刷新包清单的 exact 摘要（等价 `extension digest --write`），
// 并对已声明 integrity 的资产重算 SRI。返回清单刷新后的根摘要。
func WriteDigest(packageRoot string) (string, error) {
	manifestPath := filepath.Join(packageRoot, extensionmanifest.ManifestFileName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", err
	}
	version, _ := manifest["manifestVersion"].(float64)
	if int(version) != extensionmanifest.ManifestVersionV3 {
		return "", fmt.Errorf("%s is not an explicit Manifest V3 package", manifestPath)
	}
	if err := refreshAssetIntegrity(packageRoot, manifest); err != nil {
		return "", err
	}
	rawFiles, present := manifest["packageFiles"]
	files, _ := rawFiles.([]any)
	if !present || rawFiles == nil {
		// 无 packageFiles 的合法形态：没有可刷新的摘要，仍校验一遍包。
		if _, err := extensionmanifest.LoadPackage(packageRoot); err != nil {
			return "", fmt.Errorf("package is invalid: %w", err)
		}
		return extensionpackageRootDigest(packageRoot)
	}
	digests := make(map[string]string, len(files))
	for _, raw := range files {
		file, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid packageFiles entry")
		}
		relative, _ := file["path"].(string)
		digest, err := digestPackageRelativeFile(packageRoot, relative)
		if err != nil {
			return "", err
		}
		digests[relative] = digest
		file["digest"] = digest
	}
	syncInlineDeclarationDigests(manifest, digests)
	refreshed, err := marshalManifestJSON(manifest)
	if err != nil {
		return "", err
	}
	// 内容不变时不重写:manifest 在 dev watch 树内,无意义重写会让 watcher
	// 自触发重建循环(构建 → 写 manifest → 事件 → 再构建)。
	wrote := false
	if !bytes.Equal(body, refreshed) {
		if err := os.WriteFile(manifestPath, refreshed, 0o644); err != nil {
			return "", err
		}
		wrote = true
	}
	if _, err := extensionmanifest.LoadPackage(packageRoot); err != nil {
		if wrote {
			_ = os.WriteFile(manifestPath, body, 0o644)
		}
		return "", fmt.Errorf("refreshed manifest is invalid: %w", err)
	}
	return extensionpackageRootDigest(packageRoot)
}

func extensionpackageRootDigest(packageRoot string) (string, error) {
	tree, err := VerifiedTreeDigest(packageRoot)
	if err != nil {
		return "", err
	}
	return tree, nil
}

// refreshAssetIntegrity 重算清单里已声明 integrity 的资产 SRI（与 CLI 行为一致）。
func refreshAssetIntegrity(root string, manifest map[string]any) error {
	assets, _ := manifest["assets"].([]any)
	for _, raw := range assets {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		current, _ := item["integrity"].(string)
		if current == "" {
			continue
		}
		relative, _ := item["path"].(string)
		if relative == "" {
			continue
		}
		digest, err := digestPackageRelativeFile(root, relative)
		if err != nil {
			return fmt.Errorf("recompute integrity for %s: %w", relative, err)
		}
		rawDigest, err := hex.DecodeString(digest)
		if err != nil {
			return fmt.Errorf("recompute integrity for %s: %w", relative, err)
		}
		integrity := "sha256-" + base64.StdEncoding.EncodeToString(rawDigest)
		if item["integrity"] != integrity {
			item["integrity"] = integrity
		}
	}
	return nil
}

func digestPackageRelativeFile(root string, relative string) (string, error) {
	safe, ok := extensionmanifest.SafeArchivePath(relative)
	if !ok || safe != relative || safe == extensionmanifest.ManifestFileName {
		return "", fmt.Errorf("unsafe package file path %q", relative)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("package root is a symlink: %s", root)
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("package file path contains symlink: %s", relative)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func syncInlineDeclarationDigests(manifest map[string]any, digests map[string]string) {
	if backend, ok := manifest["backend"].(map[string]any); ok {
		if entry, _ := backend["entry"].(string); digests[entry] != "" {
			backend["digest"] = digests[entry]
		}
	}
	for family, pathKey := range map[string]string{
		"migrations": "path", "guards": "entry", "templates": "path", "assets": "path", "openapi": "path",
	} {
		items, _ := manifest[family].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			path, _ := item[pathKey].(string)
			if digests[path] != "" {
				item["digest"] = digests[path]
			}
		}
	}
	items, _ := manifest["editor"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		path, _ := item["l2Module"].(string)
		if digests[path] != "" {
			item["l2Digest"] = digests[path]
		}
	}
}

// marshalManifestJSON 把清单写回磁盘，口径必须与发行脚本的
// `JSON.stringify(manifest, null, 2) + "\n"` 逐字节一致：
//   - 2 空格缩进、结尾换行；
//   - 不做 HTML 转义。encoding/json 默认把 `<` `>` `&` 写成 `\u003c` `\u003e`
//     `\u0026`，而 JSON.stringify 原样输出。同一份 manifest 被两个写者轮流落盘时，
//     这个差异会表现为 `"sforumVersion": "\u003e=1.0.0"` 与 `">=1.0.0"` 的永久抖动。
func marshalManifestJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// BuildAndDigestDevPackage 是 dev watcher 的单包构建入口：
// backend 构建（无后端源码时 no-op）→ digest 写回（含 SRI 重算）→ 返回根摘要。
func BuildAndDigestDevPackage(packageRoot string) (string, error) {
	// 构建会改写包内文件：先丢弃该根的记忆化校验结果，避免指纹窗口内的陈旧
	// 复用；随后 WriteDigest 重新计算并写入新的记忆。
	InvalidateVerification(packageRoot)
	if err := BuildFrontend(packageRoot); err != nil {
		return "", err
	}
	if err := BuildBackend(packageRoot); err != nil {
		return "", err
	}
	return WriteDigest(packageRoot)
}

// BuildFrontend 在 frontend/admin 声明了构建脚本时执行 bun run build
// （跳过依赖安装：dev 依赖由作者首次安装；无 package.json 时 no-op）。
// 与 CLI buildExtensionAdminFrontend 同语义，但面向 host 内 dev watcher：
// 失败返回错误，由调用方保留上一个健康制品。
func BuildFrontend(packageRoot string) error {
	frontendRoot := filepath.Join(packageRoot, "frontend", "admin")
	packageJSON := filepath.Join(frontendRoot, "package.json")
	if info, err := os.Stat(packageJSON); err != nil || info.IsDir() {
		return nil
	}
	cmd := exec.Command("bun", "run", "build")
	cmd.Dir = frontendRoot
	cmd.Env = append(os.Environ(), "CI=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bun run build (frontend/admin): %v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
