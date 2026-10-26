package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 脚手架 digest 占位符：__BACKEND_DIGEST__ / __OPENAPI_DIGEST__ 等。
var manifestDigestPlaceholder = regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`)

func newExtensionDigestCommand() *cobra.Command {
	var write bool
	cmd := &cobra.Command{
		Use:   "digest [path]",
		Short: "Inspect or refresh exact Manifest V3 package-file digests",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runExtensionDigest(cmd, root, write)
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "Write refreshed digests to the root manifest and validate the package")
	return cmd
}

func runExtensionDigest(cmd *cobra.Command, root string, write bool) error {
	abs, err := resolveExtensionPackageRoot(root)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(abs, extensionmanifest.ManifestFileName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		// 正式打包路径：仅有 .tmpl 时先 materialize 占位 digest，再由 --write 刷新真实摘要。
		// 测试与作者不得再手算 SHA 替换 token。
		if !os.IsNotExist(err) {
			return err
		}
		tmplPath := filepath.Join(abs, extensionmanifest.ManifestFileName+".tmpl")
		tmplBody, tmplErr := os.ReadFile(tmplPath)
		if tmplErr != nil {
			return fmt.Errorf("read manifest: %w (also no %s: %v)", err, filepath.Base(tmplPath), tmplErr)
		}
		body = []byte(materializeManifestTemplate(string(tmplBody)))
		if writeErr := os.WriteFile(manifestPath, body, 0o600); writeErr != nil {
			return writeErr
		}
		cmd.Printf("materialized %s from template\n", manifestPath)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		return err
	}
	version, _ := manifest["manifestVersion"].(float64)
	if int(version) != extensionmanifest.ManifestVersionV3 {
		return fmt.Errorf("%s is not an explicit Manifest V3 package", manifestPath)
	}
	if write {
		// 资产文件一旦修改，清单里的 SRI 必然过期，包会被判 invalid；此前作者必须
		// 像 prepare-package.sh 那样手写脚本重算。--write 路径顺带修复它。
		if err := refreshAssetIntegrity(cmd, abs, manifest); err != nil {
			return err
		}
	}
	// 没有 packageFiles 是合法形态（例如只声明 routes/events 契约、不携带任何包内文件的包）。
	// 此时没有可刷新的摘要：仍然执行 --write 的校验，但不再以一句难以理解的错误中断作者循环。
	// 修正（2026-10-16）：此前直接报 “Manifest V3 packageFiles must be inline for digest refresh”，
	// 而 `extension validate` / `test` 对同一包是绿的——又一个「CLI 与作者预期不一致」。
	rawFiles, present := manifest["packageFiles"]
	files, ok := rawFiles.([]any)
	if !present || rawFiles == nil {
		if !write {
			cmd.Printf("no package files declared; nothing to refresh\n")
			return nil
		}
		if _, err := extensionmanifest.LoadPackage(abs); err != nil {
			return fmt.Errorf("package is invalid: %w", err)
		}
		cmd.Printf("no package files declared; %s validated\n", manifestPath)
		return nil
	}
	if !ok {
		return fmt.Errorf("Manifest V3 packageFiles must be an inline array for digest refresh")
	}
	digests := make(map[string]string, len(files))
	for _, raw := range files {
		file, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid packageFiles entry")
		}
		relative, _ := file["path"].(string)
		digest, err := digestPackageRelativeFile(abs, relative)
		if err != nil {
			return err
		}
		digests[relative] = digest
		file["digest"] = digest
	}
	syncInlineDeclarationDigests(manifest, digests)
	paths := make([]string, 0, len(digests))
	for relative := range digests {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		cmd.Printf("%s\t%s\n", relative, digests[relative])
	}
	if !write {
		return nil
	}
	if err := writeJSON(manifestPath, manifest); err != nil {
		return err
	}
	if _, err := extensionmanifest.LoadPackage(abs); err != nil {
		if restoreErr := os.WriteFile(manifestPath, body, 0o644); restoreErr != nil {
			return fmt.Errorf("refreshed manifest is invalid: %v; restore failed: %w", err, restoreErr)
		}
		return fmt.Errorf("refreshed manifest is invalid: %w", err)
	}
	cmd.Printf("updated %s\n", manifestPath)
	return nil
}

// materializeManifestTemplate 将脚手架占位符（如 __BACKEND_DIGEST__）替换为 64 个 0，
// 仅用于生成可被 digest --write 刷新的合法 JSON；真实摘要必须由 digest 命令写入。
func materializeManifestTemplate(body string) string {
	return manifestDigestPlaceholder.ReplaceAllString(body, strings.Repeat("0", 64))
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

// refreshAssetIntegrity 重算清单里已声明 integrity 的资产 SRI。
// Host 校验要求 integrity == "sha256-" + base64(sha256(文件字节))；只修复已声明
// 的值：从缺省（空）开始的清单不被悄悄改写，既有包基线不受影响。
func refreshAssetIntegrity(cmd *cobra.Command, root string, manifest map[string]any) error {
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
			cmd.Printf("integrity refreshed: %s\n", relative)
		}
	}
	return nil
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
