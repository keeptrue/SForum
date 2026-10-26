package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// PreflightExactTemplateRuntime 对显式 Manifest V3 包复用生产
// Pages.BuildThemeRuntimeSnapshot 路径做激活前精确模板预检。
// 仅在 package-local theme.json 声明了 page template 时运行；
// 从不执行扩展代码。V1/V2、无 page template、L0-only 包直接通过。
func PreflightExactTemplateRuntime(root string, manifest extensionmanifest.Manifest) error {
	if extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 {
		return nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("package root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("package root: %w", err)
	}
	contentRoot := resolveThemeContentRoot(absoluteRoot)
	info, err := os.Lstat(contentRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("package root is not a regular directory")
	}
	pkg, err := pages.LoadThemePackage(contentRoot)
	if err != nil {
		return fmt.Errorf("theme package: %w", err)
	}
	// 作者源码树可能已经装过依赖；预检只关心会进入 artifact 的内容，
	// 因此跳过打包阶段本就会排除的 node_modules/.git/vendor。
	digest, err := extensionpackage.DigestAuthorTree(contentRoot)
	if err != nil {
		return fmt.Errorf("package digest: %w", err)
	}
	if manifest.Type == extensionmanifest.TypeTheme {
		// 上传静态预检会编译全部可安装模板，不能只验证 theme.json 当前选中的页面。
		if err := themecompiler.NewCompiler(themecompiler.Limits{}).PreflightFS(os.DirFS(contentRoot)); err != nil {
			return fmt.Errorf("theme template preflight: %w", err)
		}
	}
	bridge := pages.NewExtensionBridge(pages.NewRegistry(nil))
	extension := pages.ThemeExtension{
		ID: manifest.ID, Version: manifest.Version, PackagePath: contentRoot, PackageDigest: digest,
	}
	var contributions []pages.PageContribution
	if manifest.Type == extensionmanifest.TypePlugin {
		contributions, err = bridge.PreflightPluginPackage(extension)
	} else {
		contributions, err = bridge.PreflightThemePackage(extension, "")
	}
	if err != nil {
		return fmt.Errorf("page package preflight: %w", err)
	}
	// view=schema 公开页在进入运行时快照前先做精确制品 + 契约 + 有界校验：
	// 作者（CLI）与激活路径共用这一个入口，避免出现「CLI 0 error、激活被拒」。
	if err := validateSchemaViewPages(contentRoot, manifest, contributions); err != nil {
		return fmt.Errorf("schema view preflight: %w", err)
	}
	if !themePackageHasPageTemplates(pkg) {
		// L0-only 包仍完成 CSS/包级预检，但无需精确模板运行时快照。
		return nil
	}

	templates := runtimeTemplateDeclarations(manifest.Templates)
	schemas := runtimeDataSchemaDeclarations(manifest.PackageFiles)
	kind := pages.RuntimeTemplateTheme
	if manifest.Type == extensionmanifest.TypePlugin {
		kind = pages.RuntimeTemplatePlugin
	}

	// 与 page_registry_adapter / lifecycle_page_runtime 激活路径同一入口。
	_, err = pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID:      manifest.ID,
			ExtensionVersion: manifest.Version,
			PackageDigest:    digest,
		},
		PackageRoot:              contentRoot,
		Contributions:            contributions,
		Templates:                templates,
		DataSchemas:              schemas,
		PackageKind:              kind,
		RequireDeclaredTemplates: true,
		SiteName:                 "SForum",
	})
	if err == nil {
		return nil
	}
	// 生产激活对「无 provider」同样跳过运行时快照。
	if errors.Is(err, pages.ErrThemeRuntimeMissing) {
		return nil
	}
	return fmt.Errorf("exact template runtime: %w", err)
}

func themePackageHasPageTemplates(pkg pages.ThemePackage) bool {
	for _, page := range pkg.Pages {
		if strings.TrimSpace(page.Template) != "" {
			return true
		}
	}
	return false
}

// resolveThemeContentRoot 对齐 CLI/开发包根与旧 files/ 内容布局。
func resolveThemeContentRoot(root string) string {
	root = filepath.Clean(strings.TrimSpace(root))
	if regularFile(filepath.Join(root, "theme.json")) {
		return root
	}
	files := filepath.Join(root, "files")
	if regularFile(filepath.Join(files, "theme.json")) {
		return files
	}
	if directoryExists(filepath.Join(root, "assets")) || directoryExists(filepath.Join(root, "templates")) {
		return root
	}
	if directoryExists(filepath.Join(files, "assets")) || directoryExists(filepath.Join(files, "templates")) {
		return files
	}
	return root
}

func runtimeTemplateDeclarations(input []extensionmanifest.ManifestTemplate) []pages.RuntimeTemplateDeclaration {
	result := make([]pages.RuntimeTemplateDeclaration, len(input))
	for index, item := range input {
		result[index] = pages.RuntimeTemplateDeclaration{
			ID:               item.ID,
			ContractVersion:  item.ContractVersion,
			Action:           item.Action,
			TargetID:         item.TargetID,
			Path:             item.Path,
			Digest:           item.Digest,
			ViewModelSchema:  item.ViewModelSchema,
			ThemeOverrideKey: item.ThemeOverrideKey,
		}
	}
	return result
}

func runtimeDataSchemaDeclarations(input []extensionmanifest.ManifestPackageFile) []pages.RuntimeDataSchemaDeclaration {
	result := make([]pages.RuntimeDataSchemaDeclaration, 0, len(input))
	for _, item := range input {
		if item.Kind != "schema" {
			continue
		}
		result = append(result, pages.RuntimeDataSchemaDeclaration{
			ID:      item.ID,
			Version: item.Version,
			Path:    item.Path,
			Digest:  item.Digest,
		})
	}
	return result
}
