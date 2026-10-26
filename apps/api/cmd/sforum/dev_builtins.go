package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// M8 / M0 ADR 6.2.5：内置包清单必须从清单树发现，而不是在 shell 里再抄一份。
// 之前 scripts/build-builtin-plugins.sh 硬编码 8 个 id，任何新增/改名都会让
// 脚本、README 与镜像构建彼此漂移，且没有门禁能发现。

// builtinPackage 是一个被发现的包。
type builtinPackage struct {
	ID           string `json:"id"`
	RelativePath string `json:"relativePath"`
	ManifestPath string `json:"manifestPath"`
	Type         string `json:"type"`
	BackendEntry string `json:"backendEntry,omitempty"`
	BackendDir   string `json:"backendDir,omitempty"`
	// HasGoBackend 表示 backend 目录里有可编译的 Go 源码（主题与纯前端包没有）。
	HasGoBackend bool `json:"hasGoBackend"`
}

// discoverBuiltinPackages 递归查找 root 下所有带 sforum.extension.json 的包，
// 按包 id 排序。目录名与包 id 不必一致（历史上有 sforum-shortcodes → sforum.shortcodes）。
func discoverBuiltinPackages(root string) ([]builtinPackage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("builtin discovery root is not a directory: %s", abs)
	}
	packages := make([]builtinPackage, 0, 16)
	seen := map[string]string{}
	err = filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch strings.ToLower(entry.Name()) {
			case "node_modules", "dist", "vendor", ".git", "tmp":
				if path != abs {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != extensionmanifest.ManifestFileName {
			return nil
		}
		pkg, loadErr := loadBuiltinPackage(abs, filepath.Dir(path))
		if loadErr != nil {
			return loadErr
		}
		if previous, duplicate := seen[pkg.ID]; duplicate {
			return fmt.Errorf("duplicate builtin package id %s in %s and %s", pkg.ID, previous, pkg.RelativePath)
		}
		seen[pkg.ID] = pkg.RelativePath
		packages = append(packages, pkg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(packages, func(left, right int) bool { return packages[left].ID < packages[right].ID })
	return packages, nil
}

// builtinManifestShape 是发现阶段需要的清单元数据。这里**故意**不走 LoadPackage：
// 源树里的受保护内置没有 backend 二进制（它只在 staging 里构建），完整包校验会因
// 摘要不匹配而失败——发现是「列举」，不是「校验」。
type builtinManifestShape struct {
	ManifestVersion int    `json:"manifestVersion"`
	ID              string `json:"id"`
	Type            string `json:"type"`
	Backend         struct {
		Entry string `json:"entry"`
	} `json:"backend"`
	Includes map[string]string `json:"includes"`
}

func loadBuiltinPackage(root, dir string) (builtinPackage, error) {
	shape, err := readBuiltinManifestShape(filepath.Join(dir, extensionmanifest.ManifestFileName))
	if err != nil {
		return builtinPackage{}, fmt.Errorf("read builtin manifest %s: %w", dir, err)
	}
	if shape.ManifestVersion != extensionmanifest.ManifestVersionV3 {
		return builtinPackage{}, fmt.Errorf(
			"%s is not an explicit Manifest V3 package (manifestVersion=%d)", dir, shape.ManifestVersion,
		)
	}
	if strings.TrimSpace(shape.ID) == "" || strings.TrimSpace(shape.Type) == "" {
		return builtinPackage{}, fmt.Errorf("%s has no id/type in its manifest", dir)
	}
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return builtinPackage{}, err
	}
	pkg := builtinPackage{
		ID: shape.ID, RelativePath: filepath.ToSlash(relative),
		ManifestPath: filepath.ToSlash(filepath.Join(relative, extensionmanifest.ManifestFileName)),
		Type:         shape.Type,
	}
	entry := strings.TrimSpace(shape.Backend.Entry)
	if entry == "" {
		return pkg, nil
	}
	pkg.BackendEntry = filepath.ToSlash(entry)
	backendDir := filepath.Dir(filepath.Join(dir, filepath.FromSlash(entry)))
	pkg.BackendDir = filepath.ToSlash(filepath.Join(relative, filepath.Dir(filepath.FromSlash(entry))))
	hasGo, err := directoryHasGoSource(backendDir)
	if err != nil {
		return builtinPackage{}, err
	}
	pkg.HasGoBackend = hasGo
	return pkg, nil
}

// readBuiltinManifestShape 读取根清单，并在 backend 只在 includes 分片里声明时跟进该分片。
func readBuiltinManifestShape(path string) (builtinManifestShape, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return builtinManifestShape{}, err
	}
	var shape builtinManifestShape
	if err := json.Unmarshal(body, &shape); err != nil {
		return builtinManifestShape{}, err
	}
	if strings.TrimSpace(shape.Backend.Entry) != "" {
		return shape, nil
	}
	include, ok := shape.Includes["backend"]
	if !ok || strings.TrimSpace(include) == "" {
		return shape, nil
	}
	includePath := filepath.Join(filepath.Dir(path), filepath.FromSlash(include))
	includeBody, err := os.ReadFile(includePath)
	if err != nil {
		return builtinManifestShape{}, err
	}
	var included builtinManifestShape
	if err := json.Unmarshal(includeBody, &included); err != nil {
		return builtinManifestShape{}, err
	}
	shape.Backend = included.Backend
	return shape, nil
}

func directoryHasGoSource(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true, nil
		}
	}
	return false, nil
}

func newExtensionBuiltinsCommand() *cobra.Command {
	var (
		root   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "builtins",
		Short: "Discover built-in packages from the manifest tree",
		Long: `Walk a built-in root and report every Manifest V3 package found there.

This is the single source of truth for the built-in package list: build scripts
and CI consume it instead of hardcoding ids. Text mode prints
"<id>\t<relativePath>\t<backendEntry>" per package; --json adds the Go backend
flag so callers can skip themes and frontend-only packages.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExtensionBuiltins(cmd, root, asJSON)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Built-in root to scan (defaults to extensions/builtin in the repository)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	return cmd
}

func runExtensionBuiltins(cmd *cobra.Command, root string, asJSON bool) error {
	if strings.TrimSpace(root) == "" {
		repoRoot, err := findRepoRoot()
		if err != nil {
			return err
		}
		root = filepath.Join(repoRoot, "extensions", "builtin")
	}
	packages, err := discoverBuiltinPackages(root)
	if err != nil {
		return err
	}
	if asJSON {
		body, err := json.MarshalIndent(packages, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(body))
		return nil
	}
	for _, pkg := range packages {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%t\n", pkg.ID, pkg.RelativePath, pkg.BackendEntry, pkg.HasGoBackend)
	}
	return nil
}
