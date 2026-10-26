package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func newExtensionPackageCommand() *cobra.Command {
	var output string
	var excludeSource bool
	cmd := &cobra.Command{
		Use:   "package [path]",
		Short: "Build a zip package with digests and SBOM stub for an extension root",
		Long: `Build a zip package with digests and SBOM stub for an extension root.

By default every file under the package root is included (except .git,
node_modules, vendor, and existing *.sforum.zip outputs).

Use --exclude-source for operator-facing release zips: skip common authoring
sources (Go/TS/Vue/Sass, go.mod, source maps, testdata, etc.) while keeping
runtime artifacts such as backend/plugin, prebuilt .mjs/.css, manifests, and
declared package files.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			// Validate package first.
			if _, err := extensionmanifest.LoadPackage(abs); err != nil {
				return fmt.Errorf("package validation failed: %w", err)
			}
			if output == "" {
				base := filepath.Base(abs)
				output = filepath.Join(abs, base+".sforum.zip")
			}
			outAbs, err := filepath.Abs(output)
			if err != nil {
				return err
			}
			result, err := buildExtensionPackage(abs, outAbs, packageBuildOptions{ExcludeSource: excludeSource})
			if err != nil {
				return err
			}
			cmd.Printf("package\t%s\n", result.ZipPath)
			cmd.Printf("digest\t%s\n", result.PackageDigest)
			cmd.Printf("sbom\t%s\n", result.SBOMPath)
			cmd.Printf("files\t%d\n", result.FileCount)
			if result.SkippedCount > 0 {
				cmd.Printf("skipped\t%d\t(source/dev files)\n", result.SkippedCount)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output zip path (default: <root>/<name>.sforum.zip)")
	cmd.Flags().BoolVar(&excludeSource, "exclude-source", false, "Omit common source/dev files from the zip (release packaging)")
	return cmd
}

type packageBuildOptions struct {
	// ExcludeSource 为 true 时跳过常见源码与开发辅助文件，便于分发运行时制品。
	ExcludeSource bool
}

type packageBuildResult struct {
	ZipPath       string
	PackageDigest string
	SBOMPath      string
	FileCount     int
	SkippedCount  int
}

func buildExtensionPackage(root, zipPath string, opts packageBuildOptions) (packageBuildResult, error) {
	var files []string
	skipped := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || strings.HasSuffix(name, ".sforum.zip") {
				if path != root {
					return filepath.SkipDir
				}
			}
			// 发布包不需要测试夹具目录。
			if opts.ExcludeSource && isPackageSourceDir(name) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip existing zip outputs inside root.
		if strings.HasSuffix(path, ".sforum.zip") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if opts.ExcludeSource && isPackageSourceFile(rel) {
			skipped++
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return packageBuildResult{}, err
	}
	sort.Strings(files)

	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return packageBuildResult{}, err
	}
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return packageBuildResult{}, err
	}
	defer zipFile.Close()

	hasher := sha256.New()
	zipWriter := zip.NewWriter(io.MultiWriter(zipFile, hasher))
	for _, rel := range files {
		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil {
			_ = zipWriter.Close()
			return packageBuildResult{}, err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = zipWriter.Close()
			return packageBuildResult{}, err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			_ = zipWriter.Close()
			return packageBuildResult{}, err
		}
		f, err := os.Open(abs)
		if err != nil {
			_ = zipWriter.Close()
			return packageBuildResult{}, err
		}
		_, copyErr := io.Copy(writer, f)
		_ = f.Close()
		if copyErr != nil {
			_ = zipWriter.Close()
			return packageBuildResult{}, copyErr
		}
	}
	if err := zipWriter.Close(); err != nil {
		return packageBuildResult{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))

	// SBOM stub (CycloneDX-like minimal JSON) recorded next to the package for
	// provenance inspection.
	sbomPath := zipPath + ".sbom.json"
	sbom := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  "1.5",
		"version":      1,
		"serialNumber": "urn:sforum:package:" + digest,
		"metadata": map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"component": map[string]any{
				"type":    "application",
				"name":    filepath.Base(root),
				"version": "package",
				"hashes":  []map[string]string{{"alg": "SHA-256", "content": digest}},
			},
		},
		"components": []any{},
	}
	raw, err := json.MarshalIndent(sbom, "", "  ")
	if err != nil {
		return packageBuildResult{}, err
	}
	if err := os.WriteFile(sbomPath, append(raw, '\n'), 0o644); err != nil {
		return packageBuildResult{}, err
	}
	return packageBuildResult{
		ZipPath: zipPath, PackageDigest: digest, SBOMPath: sbomPath,
		FileCount: len(files), SkippedCount: skipped,
	}, nil
}

// isPackageSourceDir 识别应在 --exclude-source 下整目录跳过的开发目录。
// 故意不跳过名为 src/test 的普通目录，避免误伤运行时资源路径。
func isPackageSourceDir(name string) bool {
	switch strings.ToLower(name) {
	case "testdata", "__tests__", ".github", ".vscode", ".idea":
		return true
	default:
		return false
	}
}

// isPackageSourceFile 识别常见作者源码与开发辅助文件。
// 运行时制品（backend 可执行文件、预构建 .mjs/.css、manifest JSON 等）不在此列。
func isPackageSourceFile(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	ext := strings.ToLower(filepath.Ext(base))

	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml",
		"yarn.lock", "bun.lock", "bun.lockb", "tsconfig.json", "jsconfig.json",
		"vite.config.ts", "vite.config.js", "vitest.config.ts", "vitest.config.js",
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs",
		".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json",
		".prettierrc", ".prettierrc.json", ".prettierrc.js",
		".gitignore", ".gitattributes", ".editorconfig",
		"makefile", "dockerfile", "dockerfile.dev",
		"air.toml", ".air.toml":
		return true
	}

	// source map
	if strings.HasSuffix(base, ".map") {
		return true
	}

	switch ext {
	case ".go", ".ts", ".tsx", ".vue", ".jsx", ".scss", ".sass", ".less",
		".c", ".cc", ".cpp", ".h", ".hpp", ".rs", ".java", ".kt", ".swift",
		".py", ".rb", ".php":
		return true
	default:
		return false
	}
}
