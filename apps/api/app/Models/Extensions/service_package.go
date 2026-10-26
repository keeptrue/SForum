package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	crypto "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
)

func (s *serviceCore) compensateThemeActivationTrust(
	ctx context.Context,
	actor identity.Actor,
	receipt executableTrustGrantReceipt,
	activationTarget Extension,
	previous *Extension,
	activationErr error,
) error {
	if !receipt.created || s.executableTrust == nil {
		return activationErr
	}
	base := ctx
	if base == nil {
		base = context.Background()
	} else {
		base = context.WithoutCancel(base)
	}
	compensationCtx, cancel := context.WithTimeout(base, themeTrustCompensationTimeout)
	defer cancel()
	// 如果另一个节点已经成功激活同一 exact artifact，本次 grant 已被正式采用，
	// 失败请求不能再把它当作孤儿授权撤销。激活前本来就是同一制品时不适用。
	previousWasTarget := previous != nil && sameThemeExactArtifact(*previous, activationTarget)
	if !previousWasTarget {
		if active, err := s.store.ActiveTheme(compensationCtx); err == nil && sameThemeExactArtifact(active, activationTarget) {
			return activationErr
		}
	}
	if err := s.executableTrust.compensateEnable(compensationCtx, actor, receipt, "theme_activation_failed"); err != nil {
		combined := errors.Join(activationErr, fmt.Errorf("compensate exact executable trust grant: %w", err))
		s.recordEnableFailure(compensationCtx, actor, receipt.impact.ExtensionID, combined)
		return combined
	}
	return activationErr
}

func sameThemeExactArtifact(left, right Extension) bool {
	return left.ID == right.ID && left.Type == TypeTheme && right.Type == TypeTheme &&
		left.Version == right.Version && strings.EqualFold(left.PackageDigest, right.PackageDigest)
}

func themeRuntimePublicationSourceMatches(publication ThemeRuntimePublication, source *Extension) bool {
	if source == nil {
		return publication.SourceThemeID == "" && publication.SourceThemeVersion == "" &&
			publication.SourcePackageDigest == ""
	}
	return publication.SourceThemeID == source.ID && publication.SourceThemeVersion == source.Version &&
		strings.EqualFold(publication.SourcePackageDigest, source.PackageDigest)
}

func themeIDOrEmpty(e *Extension) string {
	if e == nil {
		return ""
	}
	return e.ID
}

// RestoreActiveThemeRegistry API 启动时恢复活动主题 + 已启用插件的页面贡献。
// 无效/缺失主题时安全回退默认主题并写诊断事件。
func (s *ThemeService) RestoreActiveThemeRegistry(ctx context.Context) error {
	if s == nil || (s.pageRegistry == nil && s.assetRegistry == nil) {
		return nil
	}
	s.assetPublicationMu.Lock()
	defer s.assetPublicationMu.Unlock()
	assetBefore := s.captureAssetPublicationSnapshot()
	// 恢复已启用插件页面贡献
	items, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	if s.pageRegistry == nil {
		_, err = s.restoreEnabledAssetPublications(ctx, assetBefore, items, false)
		return err
	}
	for _, item := range items {
		if item.Type != TypePlugin || item.Status != StatusEnabled {
			continue
		}
		if err := s.pageRegistry.RegisterPluginPackage(ctx, item); err != nil {
			// 插件页面失败不阻断启动；清掉该扩展贡献并记事件
			s.pageRegistry.ClearExtension(item.ID)
			_, _ = s.store.CreateEvent(ctx, EventInput{
				ExtensionID: item.ID,
				Action:      EventEnableFailed,
				Message:     "restore plugin page contributions failed: " + err.Error(),
			})
		}
	}

	active, err := s.store.ActiveTheme(ctx)
	if err != nil {
		// 无活动主题 → 尝试默认
		if def, derr := s.EnsureDefaultThemeActive(ctx); derr == nil {
			active = def
		} else {
			return derr
		}
	}
	if active.ID != DefaultThemeID {
		defaultTheme, defaultErr := s.store.Get(ctx, DefaultThemeID)
		if defaultErr != nil {
			return defaultErr
		}
		// 默认主题活动包可能因 Host 契约收紧而无法编译；fallback 只读 staged 制品，不切换活动主题。
		fallbackTheme := healthyBuiltinThemeArtifact(defaultTheme)
		if defaultErr = s.pageRegistry.RegisterDefaultThemeFallback(ctx, fallbackTheme); defaultErr != nil {
			return fmt.Errorf("restore default theme fallback: %w", defaultErr)
		}
	}
	if err := s.pageRegistry.PreflightThemePackage(ctx, active, ""); err != nil {
		// 无效主题 → 优先晋升 staged 内置包，再回退默认
		repaired, repairErr := s.repairActiveThemeAfterRestoreFailure(ctx, active, err)
		if repairErr != nil {
			return repairErr
		}
		active = repaired
	}
	staleThemeIDs := make([]string, 0)
	for _, item := range items {
		if item.Type == TypeTheme && item.ID != active.ID {
			staleThemeIDs = append(staleThemeIDs, item.ID)
		}
	}
	if err := s.pageRegistry.RegisterThemePackageRestoring(ctx, active, staleThemeIDs); err != nil {
		// 包目录被手动删除、digest 漂移等：与 preflight 失败同样尝试回退，避免阻断启动。
		repaired, repairErr := s.repairActiveThemeAfterRestoreFailure(ctx, active, err)
		if repairErr != nil {
			return fmt.Errorf("restore active theme registry: %w", err)
		}
		active = repaired
		staleThemeIDs = staleThemeIDs[:0]
		for _, item := range items {
			if item.Type == TypeTheme && item.ID != active.ID {
				staleThemeIDs = append(staleThemeIDs, item.ID)
			}
		}
		if retryErr := s.pageRegistry.RegisterThemePackageRestoring(ctx, active, staleThemeIDs); retryErr != nil {
			return fmt.Errorf("restore active theme registry: %w", retryErr)
		}
	}
	items, err = s.store.List(ctx)
	if err != nil {
		return err
	}
	_, err = s.restoreEnabledAssetPublications(ctx, assetBefore, items, false)
	return err
}

// repairActiveThemeAfterRestoreFailure 在 preflight/注册失败时尽量自愈。
// 优先晋升 SyncBuiltins 已 stage 的内置制品（Host 契约收紧后常见），
// 否则回退受保护默认主题，避免非默认主题包缺失阻断启动。
func (s *serviceCore) repairActiveThemeAfterRestoreFailure(
	ctx context.Context,
	active Extension,
	cause error,
) (Extension, error) {
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: active.ID,
		Action:      EventEnableFailed,
		Message:     "active theme registry restore failed, attempting repair: " + cause.Error(),
	})
	if s.pageRegistry != nil {
		s.pageRegistry.ClearExtension(active.ID)
	}

	// 1) 当前活动主题若有可预检的 staged 内置包，发布 startup-equivalent exact 激活。
	if repaired, err := s.promoteStagedBuiltinThemeIfHealthy(ctx, active); err == nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: repaired.ID,
			Action:      EventThemeActivated,
			Message:     "promoted staged builtin theme after active package preflight failure",
		})
		return repaired, nil
	}

	if active.ID == DefaultThemeID {
		// 默认主题无可用 staged 时保留原件，让上层继续失败（避免静默空 Registry）。
		return active, nil
	}

	// 2) 非默认主题：切回默认主题；若默认活动包同样损坏再尝试晋升其 staged。
	result, err := s.store.ActivateTheme(ctx, DefaultThemeID)
	if err != nil {
		return Extension{}, err
	}
	if repaired, promoteErr := s.promoteStagedBuiltinThemeIfHealthy(ctx, result.Extension); promoteErr == nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: repaired.ID,
			Action:      EventThemeActivated,
			Message:     "promoted staged default theme after fallback activation preflight failure",
		})
		return repaired, nil
	}
	return result.Extension, nil
}

// promoteStagedBuiltinThemeIfHealthy 在活动包无法加载时，将已 stage 的内置 exact 制品
// 写入 active 并追加 theme_runtime_publications，供 watcher 收敛到可预检包。
func (s *serviceCore) promoteStagedBuiltinThemeIfHealthy(ctx context.Context, active Extension) (Extension, error) {
	if s == nil || s.store == nil || ctx == nil {
		return Extension{}, ErrThemePublicationConflict
	}
	current, err := s.store.Get(ctx, active.ID)
	if err != nil {
		return Extension{}, err
	}
	if current.Type != TypeTheme || current.Source != SourceBuiltin || current.Status != StatusEnabled {
		return Extension{}, ErrInvalidManifest
	}
	staged, ok := current.StagedArtifact()
	if !ok {
		return Extension{}, ErrThemePreviewStale
	}
	if strings.EqualFold(staged.PackageDigest, current.PackageDigest) {
		return Extension{}, ErrThemePreviewStale
	}
	if s.pageRegistry != nil {
		if err := s.pageRegistry.PreflightThemePackage(ctx, staged, ""); err != nil {
			return Extension{}, err
		}
	}
	result, err := s.store.ActivateThemeExact(ctx, current.ID, ThemeActivationInput{
		Version:             staged.Version,
		PackageDigest:       staged.PackageDigest,
		CurrentThemeID:      current.ID,
		CurrentThemeVersion: current.Version,
		CurrentThemeDigest:  current.PackageDigest,
	})
	if err != nil {
		return Extension{}, err
	}
	return result.Extension, nil
}

// healthyBuiltinThemeArtifact 优先返回可编译的 staged 内置制品投影（不改 DB）。
// 用于 fallback 编译路径：不能切换活动主题，但需要避开已失效的 active 包。
func healthyBuiltinThemeArtifact(theme Extension) Extension {
	if theme.Source != SourceBuiltin {
		return theme
	}
	if staged, ok := theme.StagedArtifact(); ok &&
		!strings.EqualFold(staged.PackageDigest, theme.PackageDigest) {
		return staged
	}
	return theme
}

// RestoreSafeModeThemeRegistry 忽略数据库 desired theme 与全部插件贡献，只加载受保护默认主题。
func (s *ThemeService) RestoreSafeModeThemeRegistry(ctx context.Context) error {
	if s == nil || (s.pageRegistry == nil && s.assetRegistry == nil) {
		return nil
	}
	s.themeActivationMu.Lock()
	defer s.themeActivationMu.Unlock()
	return s.restoreSafeModeThemeRegistry(ctx)
}

// FailClosedThemeRuntime permanently closes theme mutation admission for this
// process before installing the protected default. A healthy process restart is
// required to reopen admission after durable watcher ownership is lost.
func (s *ThemeService) FailClosedThemeRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.themeActivationMu.Lock()
	defer s.themeActivationMu.Unlock()
	s.themeRuntimeUnavailable = true
	return s.restoreSafeModeThemeRegistry(ctx)
}

func (s *serviceCore) restoreSafeModeThemeRegistry(ctx context.Context) error {
	if s.pageRegistry == nil && s.assetRegistry == nil {
		return nil
	}
	s.assetPublicationMu.Lock()
	defer s.assetPublicationMu.Unlock()
	assetBefore := s.captureAssetPublicationSnapshot()
	if s.assetRegistry != nil {
		if _, err := s.restoreEnabledAssetPublications(ctx, assetBefore, nil, true); err != nil {
			return err
		}
	}
	if s.pageRegistry == nil {
		return nil
	}
	items, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		s.pageRegistry.ClearExtension(item.ID)
	}
	defaultTheme, err := s.store.Get(ctx, DefaultThemeID)
	if err != nil {
		return err
	}
	if defaultTheme.Type != TypeTheme || defaultTheme.Source != SourceBuiltin || !defaultTheme.IsSystem {
		return ErrInvalidManifest
	}
	// 安全模式/ fail-closed 也必须能加载默认可预检制品。
	// 活动包因 Host 契约失效时晋升 staged，避免 watcher 回退再次踩同一坏包。
	if err := s.pageRegistry.PreflightThemePackage(ctx, defaultTheme, ""); err != nil {
		repaired, promoteErr := s.promoteStagedBuiltinThemeIfHealthy(ctx, defaultTheme)
		if promoteErr != nil {
			return err
		}
		defaultTheme = repaired
		if err := s.pageRegistry.PreflightThemePackage(ctx, defaultTheme, ""); err != nil {
			return err
		}
	}
	if err := s.pageRegistry.RegisterThemePackage(ctx, defaultTheme); err != nil {
		return err
	}
	return nil
}

func (s *serviceCore) verifyExtension(ctx context.Context, extension Extension) error {
	if err := validateInstalledPackage(extension); err != nil {
		if extension.Type == TypeTheme {
			return fmt.Errorf("%w: %v", ErrBuildFailed, err)
		}
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	if extension.Type == TypePlugin && extension.Manifest.Backend.Entry != "" && s.runtime != nil {
		if err := s.runtime.Check(ctx, extension); err != nil {
			return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
		}
	}
	if extension.Type == TypeTheme {
		if !themeRuntimePackagePresent(extension.PackagePath) {
			return fmt.Errorf("%w: theme requires theme.json/assets (L0/L1)", ErrBuildFailed)
		}
	}
	return nil
}

// themeRuntimePackagePresent 检测 L0/L1 运行时主题包。
func themeRuntimePackagePresent(packagePath string) bool {
	root := strings.TrimSpace(packagePath)
	if root == "" {
		return false
	}
	// 与 PackageContentRoot 一致：zip 旁 files/、同级目录或内容寻址目录。
	candidates := []string{root}
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		candidates = []string{
			filepath.Join(filepath.Dir(root), "files"),
			filepath.Dir(root),
		}
	}
	for _, base := range candidates {
		for _, name := range []string{"theme.json", "assets/theme.css", "assets"} {
			if st, err := os.Stat(filepath.Join(base, name)); err == nil {
				if name == "assets" && !st.IsDir() {
					continue
				}
				return true
			}
		}
	}
	return false
}

func validateInstalledPackage(extension Extension) error {
	packagePath := strings.TrimSpace(extension.PackagePath)
	if packagePath == "" {
		return fmt.Errorf("extension package path is empty")
	}
	if strings.TrimSpace(extension.PackageDigest) != "" {
		info, err := os.Lstat(packagePath)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("extension package snapshot %s is not available", packagePath)
		}
		if err := requireInstalledManifest(packagePath); err != nil {
			return err
		}
		digest, err := extensionpackage.DigestTree(packagePath)
		if err != nil {
			return fmt.Errorf("extension package snapshot %s is invalid: %w", packagePath, err)
		}
		if digest != extension.PackageDigest {
			return fmt.Errorf("extension package snapshot %s digest does not match its installed version", packagePath)
		}
		return nil
	}
	if extension.Source == SourceBuiltin {
		info, err := os.Stat(packagePath)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("builtin extension package %s is not available", packagePath)
		}
		return requireInstalledManifest(packagePath)
	}

	info, err := os.Stat(packagePath)
	if err != nil || info.IsDir() {
		return fmt.Errorf("extension archive %s is not available", packagePath)
	}
	return requireInstalledManifest(filepath.Dir(packagePath))
}

func requireInstalledManifest(root string) error {
	manifestPath := filepath.Join(root, ManifestFileName)
	info, err := os.Stat(manifestPath)
	if err != nil || info.IsDir() {
		return fmt.Errorf("extension manifest %s is not available", manifestPath)
	}
	return nil
}

func (s *serviceCore) recordEnableFailure(ctx context.Context, actor identity.Actor, extensionID string, cause error) {
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: extensionID,
		ActorUserID: actor.ID,
		Action:      EventEnableFailed,
		Message:     cause.Error(),
	})
}

func (s *serviceCore) decorateRuntime(ctx context.Context, item Extension) Extension {
	if item.Type == TypePlugin {
		item.CapabilityGrants = extensionmanifest.CapabilityGrants(item.Manifest)
		if s.runtime != nil {
			status := s.runtime.Status(ctx, item)
			item.Runtime = &status
		}
	}
	return item
}

// CapabilitiesFor 返回已启用插件的有效能力集合（Host API CapabilitySource）。
func (s *CatalogService) CapabilitiesFor(ctx context.Context, extensionID string) (capabilities.Set, error) {
	if s.safeMode {
		return nil, ErrExtensionDisabled
	}
	extension, err := s.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return nil, err
	}
	if extension.Type != TypePlugin {
		return nil, ErrExtensionNotFound
	}
	if extension.Status != StatusEnabled {
		return nil, ErrExtensionDisabled
	}
	keys, _ := extensionmanifest.ResolvedCapabilities(extension.Manifest)
	return capabilities.NewSet(keys), nil
}

// DeclaredJobKinds 返回插件 manifest 声明的 job names。
func (s *CatalogService) DeclaredJobKinds(ctx context.Context, extensionID string) ([]string, error) {
	if s.safeMode {
		return []string{}, nil
	}
	extension, err := s.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(extension.Manifest.Jobs))
	for _, job := range extension.Manifest.Jobs {
		if name := strings.TrimSpace(job.Name); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// CapabilityCatalog 返回宿主能力目录（管理端审查文案）。
func (s *CatalogService) CapabilityCatalog(_ context.Context, actor identity.Actor) ([]capabilities.Definition, error) {
	if !canViewExtensions(actor) && !canManagePlugins(actor) {
		return nil, identity.ErrPermissionDenied
	}
	return capabilities.Catalog(), nil
}

func writeManifest(versionDir string, manifest Manifest) error {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(versionDir, ManifestFileName), body, 0o600)
}

// PackageContentRoot 返回扩展包内可读文件的目录根。
// - 内容寻址快照 / builtin：PackagePath 本身就是目录。
// - 旧版上传包：PackagePath 指向 package.zip，解压内容在同级 files/。
// 主题 L0/L1 预检、皮肤资源与模板加载都必须走这里，不能直接拼 package.zip 路径。
func PackageContentRoot(extension Extension) string {
	path := strings.TrimSpace(extension.PackagePath)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	// 有 digest 的快照或内置包：PackagePath 即为内容根目录。
	if strings.TrimSpace(extension.PackageDigest) != "" || extension.Source == SourceBuiltin {
		return path
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	// 旧上传布局：.../1.0.0/package.zip + .../1.0.0/files/*
	files := filepath.Join(filepath.Dir(path), "files")
	if info, err := os.Stat(files); err == nil && info.IsDir() {
		return files
	}
	// 兜底：zip 同级目录（manifest 等同级时）
	return filepath.Dir(path)
}

func installedFilePath(extension Extension, manifestPath string) (string, bool) {
	name, ok := safeArchivePath(manifestPath)
	if !ok {
		return "", false
	}
	root := PackageContentRoot(extension)
	if root == "" {
		return "", false
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	return target, strings.HasPrefix(target, root+string(os.PathSeparator))
}

func InstalledFilePathForRuntime(extension Extension, manifestPath string) (string, bool) {
	return installedFilePath(extension, manifestPath)
}

func validateManifest(manifest Manifest) error {
	return extensionmanifest.Validate(manifest)
}

func ValidateManifest(manifest Manifest) error {
	return extensionmanifest.Validate(manifest)
}

func normalizeManifest(manifest Manifest) Manifest {
	return extensionmanifest.Normalize(manifest)
}

func normalizeID(id string) string {
	return extensionmanifest.NormalizeID(id)
}

func safeArchivePath(name string) (string, bool) {
	return extensionmanifest.SafeArchivePath(name)
}

func normalizeRoutePath(value string) string {
	return extensionmanifest.NormalizeRoutePath(value)
}

func extensionInjectsAdminNavigation(extension Extension) bool {
	if extension.Type == TypePlugin {
		return extension.Status == StatusEnabled
	}
	return extension.Type == TypeTheme && extension.Status == StatusEnabled
}

func normalizedAdminPages(manifest Manifest) []ManifestAdminPage {
	pages := make([]ManifestAdminPage, 0, len(extensionmanifest.EffectiveAdminPages(manifest))+1)
	pages = append(pages, ManifestAdminPage{
		Path:        "/about",
		Label:       manifest.Name,
		Description: manifest.Description,
		Icon:        defaultExtensionIcon(manifest.Type),
		View:        "about",
		Order:       0,
	})
	for _, page := range extensionmanifest.EffectiveAdminPages(manifest) {
		if strings.TrimSpace(page.Path) == "" {
			continue
		}
		pages = append(pages, normalizeAdminPageForDisplay(manifest.Type, page))
	}
	sort.SliceStable(pages, func(left, right int) bool {
		if pages[left].Order == pages[right].Order {
			return pages[left].Path < pages[right].Path
		}
		return pages[left].Order < pages[right].Order
	})
	return pages
}

func normalizedMenuAdminPages(manifest Manifest) []ManifestAdminPage {
	pages := make([]ManifestAdminPage, 0, len(extensionmanifest.MenuAdminPages(manifest)))
	for _, page := range extensionmanifest.MenuAdminPages(manifest) {
		if strings.TrimSpace(page.Path) == "" {
			continue
		}
		pages = append(pages, normalizeAdminPageForDisplay(manifest.Type, page))
	}
	sort.SliceStable(pages, func(left, right int) bool {
		if pages[left].Order == pages[right].Order {
			return pages[left].Path < pages[right].Path
		}
		return pages[left].Order < pages[right].Order
	})
	return pages
}

func normalizeAdminPageForDisplay(extensionType string, page ManifestAdminPage) ManifestAdminPage {
	page.Path = extensionmanifest.NormalizeRoutePath(page.Path)
	page.Label = strings.TrimSpace(page.Label)
	page.Description = strings.TrimSpace(page.Description)
	page.Icon = strings.TrimSpace(page.Icon)
	page.View = strings.TrimSpace(page.View)
	page.Group = strings.TrimSpace(page.Group)
	page.Permission = strings.TrimSpace(page.Permission)
	if page.Icon == "" {
		page.Icon = defaultExtensionIcon(extensionType)
	}
	if page.View == "" {
		page.View = "about"
	}
	return page
}

func defaultExtensionIcon(extensionType string) string {
	if extensionType == TypeTheme {
		return "i-lucide-palette"
	}
	return "i-lucide-plug"
}

// sanitizeSettingValues 将 PUT 解析为完整候选集：提交值 → 已存值 → 默认。
// 省略的 secret 始终保留已存值；未知 key 拒绝且不写库。
func sanitizeSettingValues(manifest Manifest, input, current map[string]string) (map[string]string, error) {
	allowed := map[string]ManifestSetting{}
	for _, setting := range manifest.Settings {
		allowed[setting.Key] = setting
	}
	// 先拒绝未知键，避免部分写入。
	for key := range input {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := allowed[key]; !ok {
			return nil, ErrInvalidManifest
		}
	}
	values := map[string]string{}
	for _, setting := range manifest.Settings {
		key := setting.Key
		if submitted, ok := input[key]; ok {
			normalized := strings.TrimSpace(submitted)
			if setting.Type == "secret" && normalized == "" {
				if cur, has := current[key]; has {
					values[key] = cur
				}
				// 无已存 secret 且提交空：不写入覆盖行。
				continue
			}
			values[key] = normalized
			continue
		}
		// 未提交：保留已存；无已存则不写入（运行时用 default）。
		if cur, has := current[key]; has {
			values[key] = cur
		}
	}
	return values, nil
}

// listDecryptedSettings 读取并解密 secret；错误密文 fail closed（不交给插件/API 明文路径）。
// 历史明文在 cipher 启用时异步迁移写回密文。
func (s *serviceCore) listDecryptedSettings(ctx context.Context, extension Extension) (map[string]string, error) {
	if s.settingsLifecycle != nil && len(extension.Manifest.Settings) > 0 {
		if err := s.host.RegisterSettingsLifecycleFromManifest(extension); err != nil {
			return nil, err
		}
		values, err := s.settingsLifecycle.RuntimeValues(ctx, extension.ID, "settings")
		if err != nil {
			if errors.Is(err, settingslifecycle.ErrNotFound) {
				return map[string]string{}, nil
			}
			return nil, err
		}
		return values, nil
	}
	raw, err := s.store.ListSettings(ctx, extension.ID)
	if err != nil {
		return nil, err
	}
	return s.decryptSettingsMap(ctx, extension, raw)
}

func (s *serviceCore) decryptSettingsMap(ctx context.Context, extension Extension, raw map[string]string) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	secretKeys := secretSettingKeys(extension.Manifest)
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		if !secretKeys[key] {
			out[key] = value
			continue
		}
		plain, migrated, err := s.decryptSecretValue(value)
		if err != nil {
			// 错误密钥/损坏密文：禁止静默清空，也禁止把密文交给插件。
			return nil, fmt.Errorf("%w: setting %s", err, key)
		}
		out[key] = plain
		if migrated {
			enc, encErr := s.encryptSecretValue(plain)
			if encErr != nil {
				return nil, fmt.Errorf("encrypt legacy secret setting %s: %w", key, encErr)
			}
			if _, casErr := s.store.CompareAndSwapSetting(ctx, extension.ID, key, value, enc); casErr != nil {
				return nil, fmt.Errorf("migrate legacy secret setting %s: %w", key, casErr)
			}
		}
	}
	return out, nil
}

func secretSettingKeys(manifest Manifest) map[string]bool {
	out := map[string]bool{}
	for _, setting := range manifest.Settings {
		if setting.Type == "secret" {
			out[setting.Key] = true
		}
	}
	return out
}

func (s *serviceCore) encryptSecretSettings(manifest Manifest, values map[string]string) (map[string]string, error) {
	if values == nil {
		return map[string]string{}, nil
	}
	secretKeys := secretSettingKeys(manifest)
	out := make(map[string]string, len(values))
	for key, value := range values {
		if secretKeys[key] && value != "" {
			enc, err := s.encryptSecretValue(value)
			if err != nil {
				return nil, err
			}
			out[key] = enc
			continue
		}
		out[key] = value
	}
	return out, nil
}

func (s *serviceCore) encryptSecretValue(plaintext string) (string, error) {
	if s == nil || s.cipher == nil {
		return plaintext, nil
	}
	return s.cipher.Encrypt(plaintext)
}

// decryptSecretValue 返回明文；migrated=true 表示存储仍是历史明文且 cipher 已启用，应回写。
func (s *serviceCore) decryptSecretValue(stored string) (plain string, migrated bool, err error) {
	if stored == "" {
		return "", false, nil
	}
	if s == nil || s.cipher == nil || !s.cipher.Enabled() {
		// 透明模式：密文前缀也无法解密，fail closed。
		if crypto.IsEncrypted(stored) {
			return "", false, fmt.Errorf("extensions: encrypted secret requires option encryption key")
		}
		return stored, false, nil
	}
	if !crypto.IsEncrypted(stored) {
		// 历史明文：可读，并标记迁移。
		return stored, true, nil
	}
	plain, err = s.cipher.Decrypt(stored)
	if err != nil {
		return "", false, fmt.Errorf("extensions: secret decrypt failed: %w", err)
	}
	return plain, false, nil
}

func manifestEvents(manifest Manifest) []ManifestEvent {
	return extensionmanifest.DeclaredEvents(manifest)
}

func DeclaredManifestEvents(manifest Manifest) []ManifestEvent {
	return extensionmanifest.DeclaredEvents(manifest)
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
