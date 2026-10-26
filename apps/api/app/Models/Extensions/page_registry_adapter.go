package extensions

import (
	"context"
	"errors"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// PageRuntimeInstanceSource 解析扩展当前活动 runtime instance id。
//
// 为什么 enable 期必须写它：loader 的精确制品检查要求页面贡献携带 runtime instance id，
// 而此前只有启动恢复路径（`restoreExactPluginPagePublications`）会写——于是「刚启用、
// 还没重启 API」时 `resolved.RuntimeInstanceID` 为空，插件页数据恒为
// `pages: exact plugin runtime unavailable`，必须重启一次才生效。把解析注入适配器，
// 让启用路径与启动恢复写同一份契约。
type PageRuntimeInstanceSource interface {
	// ActiveRuntimeInstanceID 只在活动实例与传入的版本/摘要**精确匹配**时返回 id。
	ActiveRuntimeInstanceID(extensionID, version, packageDigest string) (string, bool)
}

// PageRegistryAdapter 将 pages.ExtensionBridge 接到 Service.PageRegistry。
type PageRegistryAdapter struct {
	Bridge           *pages.ExtensionBridge
	ThemeRuntime     *pages.ThemeRuntimeRegistry
	RuntimeInstances PageRuntimeInstanceSource
	SiteName         string
	Locales          []string
}

// WithRuntimeInstances 注入活动 runtime instance 解析（enable 期绑定页面贡献用）。
func (a *PageRegistryAdapter) WithRuntimeInstances(source PageRuntimeInstanceSource) *PageRegistryAdapter {
	if a != nil {
		a.RuntimeInstances = source
	}
	return a
}

// bindPluginRuntimeInstance 把活动 runtime instance id 写进插件页面贡献。
// 解析不到（运行时未就绪/制品不匹配）时保持为空：注册表会在请求期按 admission 拒绝，
// 不伪造身份。
func (a *PageRegistryAdapter) bindPluginRuntimeInstance(extension Extension, contributions []pages.PageContribution) {
	if a == nil || a.RuntimeInstances == nil || len(contributions) == 0 {
		return
	}
	instanceID, ok := a.RuntimeInstances.ActiveRuntimeInstanceID(extension.ID, extension.Version, extension.PackageDigest)
	if !ok {
		return
	}
	for index := range contributions {
		contributions[index].RuntimeInstanceID = instanceID
	}
}

func (a *PageRegistryAdapter) WithThemeRuntime(runtime *pages.ThemeRuntimeRegistry, siteName string, locales []string) *PageRegistryAdapter {
	if a != nil {
		a.ThemeRuntime = runtime
		a.SiteName = siteName
		a.Locales = append([]string(nil), locales...)
	}
	return a
}

func NewPageRegistryAdapter(registry *pages.Registry) *PageRegistryAdapter {
	return &PageRegistryAdapter{Bridge: pages.NewExtensionBridge(registry)}
}

func (a *PageRegistryAdapter) PreflightThemePackage(ctx context.Context, extension Extension, previousActiveThemeID string) error {
	if a == nil || a.Bridge == nil {
		return nil
	}
	contributions, err := a.Bridge.PreflightThemePackage(toThemeExt(extension), previousActiveThemeID)
	if err != nil || a.ThemeRuntime == nil {
		return err
	}
	_, err = a.buildThemeRuntime(extension, contributions)
	return err
}

func (a *PageRegistryAdapter) RegisterThemePackage(ctx context.Context, extension Extension) error {
	return a.registerThemePackage(ctx, extension, nil)
}

func (a *PageRegistryAdapter) RegisterThemePackageRestoring(ctx context.Context, extension Extension, staleThemeIDs []string) error {
	return a.registerThemePackage(ctx, extension, staleThemeIDs)
}

func (a *PageRegistryAdapter) registerThemePackage(ctx context.Context, extension Extension, oldThemeIDs []string) error {
	if a == nil || a.Bridge == nil {
		return nil
	}
	previous := ""
	if len(oldThemeIDs) != 0 {
		previous = oldThemeIDs[0]
	}
	contributions, err := a.Bridge.PreflightThemePackage(toThemeExt(extension), previous)
	if err != nil {
		return err
	}
	snapshot, err := a.buildThemeRuntime(extension, contributions)
	if err != nil {
		return err
	}
	staged, err := a.stageThemeRuntime(snapshot)
	if err != nil {
		return err
	}
	previousRuntime := a.activeThemeArtifact()
	publication := a.Bridge.Registry.CaptureThemePublication(append([]string{extension.ID}, oldThemeIDs...)...)
	var publishErr error
	if oldThemeIDs == nil {
		publishErr = a.Bridge.Registry.RegisterThemeContributions(extension.ID, contributions)
	} else {
		publishErr = a.Bridge.Registry.RestoreThemeContributions(ctx, extension.ID, contributions, oldThemeIDs)
	}
	if publishErr != nil {
		return errors.Join(publishErr, a.rollbackStagedThemeRuntime(snapshot, staged))
	}
	if err := a.activateThemeRuntime(snapshot); err != nil {
		return a.compensateThemePublication(ctx, err, publication, previousRuntime, snapshot, staged, append([]string{extension.ID}, oldThemeIDs...)...)
	}
	if extension.ID == DefaultThemeID && snapshot != nil {
		if _, err := a.ThemeRuntime.SetDefaultExact(snapshot.Artifact()); err != nil {
			return a.compensateThemePublication(ctx, err, publication, previousRuntime, snapshot, staged, append([]string{extension.ID}, oldThemeIDs...)...)
		}
	}
	return nil
}

// RegisterDefaultThemeFallback compiles and retains the protected default
// theme without publishing its Page Registry contributions as the active UI.
func (a *PageRegistryAdapter) RegisterDefaultThemeFallback(ctx context.Context, extension Extension) error {
	if a == nil || a.Bridge == nil || a.ThemeRuntime == nil {
		return nil
	}
	contributions, err := a.Bridge.PreflightThemePackage(toThemeExt(extension), "")
	if err != nil {
		return err
	}
	snapshot, err := a.buildThemeRuntime(extension, contributions)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return pages.ErrThemeRuntimeMissing
	}
	if _, staged, err := a.ThemeRuntime.Stage(snapshot); err != nil {
		return err
	} else if _, err = a.ThemeRuntime.SetDefaultExact(snapshot.Artifact()); err != nil {
		if staged {
			_, removeErr := a.ThemeRuntime.RemoveExact(snapshot.Artifact())
			return errors.Join(err, removeErr)
		}
		return err
	}
	return nil
}

func (a *PageRegistryAdapter) RegisterThemePackageReplacing(ctx context.Context, extension Extension, previousActiveThemeID string) error {
	return a.RegisterThemePackageReplacingApproved(ctx, extension, previousActiveThemeID, 0)
}

func (a *PageRegistryAdapter) RegisterThemePackageReplacingApproved(
	ctx context.Context,
	extension Extension,
	previousActiveThemeID string,
	approvedBy int64,
) error {
	if approvedBy > 0 {
		return a.registerThemePackageReplacing(ctx, extension, previousActiveThemeID,
			func(contributions []pages.PageContribution) error {
				return a.Bridge.Registry.ActivateThemeContributions(
					ctx, extension.ID, contributions, previousActiveThemeID, approvedBy,
				)
			})
	}
	return a.registerThemePackageReplacing(ctx, extension, previousActiveThemeID,
		func(contributions []pages.PageContribution) error {
			return a.Bridge.Registry.SwitchThemeContributions(
				ctx, extension.ID, contributions, previousActiveThemeID,
			)
		})
}

// RegisterThemePackageReplacingHostApproved 是受保护内置默认主题的 Host 批准
// 注册路径。适配器层再次确认目标身份：上传/第三方主题无法经由此路径拿到
// 核心页替换权限。
func (a *PageRegistryAdapter) RegisterThemePackageReplacingHostApproved(
	ctx context.Context,
	extension Extension,
	previousActiveThemeID string,
) error {
	if a == nil || a.Bridge == nil {
		return nil
	}
	if !isProtectedBuiltinDefaultTheme(extension) {
		return ErrInvalidManifest
	}
	return a.registerThemePackageReplacing(ctx, extension, previousActiveThemeID,
		func(contributions []pages.PageContribution) error {
			return a.Bridge.Registry.ActivateThemeContributionsHostApproved(
				ctx, extension.ID, contributions, previousActiveThemeID,
			)
		})
}

func (a *PageRegistryAdapter) registerThemePackageReplacing(
	ctx context.Context,
	extension Extension,
	previousActiveThemeID string,
	publish func([]pages.PageContribution) error,
) error {
	if a == nil || a.Bridge == nil {
		return nil
	}
	contributions, err := a.Bridge.PreflightThemePackage(toThemeExt(extension), previousActiveThemeID)
	if err != nil {
		return err
	}
	snapshot, err := a.buildThemeRuntime(extension, contributions)
	if err != nil {
		return err
	}
	var previous pages.RuntimeArtifact
	if active, _, ok := a.ThemeRuntime.Active(); ok {
		previous = active.Artifact()
	}
	staged, err := a.stageThemeRuntime(snapshot)
	if err != nil {
		return err
	}
	publication := a.Bridge.Registry.CaptureThemePublication(extension.ID, previousActiveThemeID)
	if publishErr := publish(contributions); publishErr != nil {
		return errors.Join(publishErr, a.rollbackStagedThemeRuntime(snapshot, staged))
	}
	if err := a.activateThemeRuntime(snapshot); err != nil {
		return a.compensateThemePublication(ctx, err, publication, previous, snapshot, staged, extension.ID, previousActiveThemeID)
	}
	if extension.ID == DefaultThemeID && snapshot != nil {
		if _, err := a.ThemeRuntime.SetDefaultExact(snapshot.Artifact()); err != nil {
			return a.compensateThemePublication(ctx, err, publication, previous, snapshot, staged, extension.ID, previousActiveThemeID)
		}
	}
	if previous.ExtensionID != "" && previous.ExtensionID != DefaultThemeID && (snapshot == nil || previous != snapshot.Artifact()) {
		_, _ = a.ThemeRuntime.RemoveExact(previous)
	}
	return nil
}

func (a *PageRegistryAdapter) RegisterPluginPackage(ctx context.Context, extension Extension) error {
	if a == nil || a.Bridge == nil {
		return nil
	}
	contributions, err := a.Bridge.PreflightPluginPackage(toThemeExt(extension))
	if err != nil {
		return err
	}
	if len(contributions) == 0 {
		// A plugin without theme.json pages owns no Page Registry artifact.
		// Clear any previous contribution/runtime instead of publishing a
		// legacy placeholder that cannot satisfy Lifecycle V2 exact fences.
		a.ClearExtension(extension.ID)
		return nil
	}
	// 与启动恢复路径一致：先把活动 runtime instance 绑到贡献上，再编译快照/注册。
	a.bindPluginRuntimeInstance(extension, contributions)
	if a.ThemeRuntime == nil {
		return a.Bridge.Registry.RegisterContributions(extension.ID, contributions)
	}
	snapshot, err := a.buildTemplateRuntime(extension, contributions, pages.RuntimeTemplatePlugin)
	if err != nil {
		return err
	}
	staged, err := a.stageThemeRuntime(snapshot)
	if err != nil {
		return err
	}
	if err := a.Bridge.Registry.RegisterContributions(extension.ID, contributions); err != nil {
		a.rollbackStagedThemeRuntime(snapshot, staged)
		return err
	}
	return nil
}

func (a *PageRegistryAdapter) ClearExtension(extensionID string) {
	if a == nil || a.Bridge == nil {
		return
	}
	a.Bridge.ClearExtension(extensionID)
	if a.ThemeRuntime != nil {
		a.ThemeRuntime.ClearExtension(extensionID)
	}
}

func (a *PageRegistryAdapter) buildThemeRuntime(extension Extension, contributions []pages.PageContribution) (*pages.ThemeRuntimeSnapshot, error) {
	return a.buildTemplateRuntime(extension, contributions, pages.RuntimeTemplateTheme)
}

func (a *PageRegistryAdapter) buildTemplateRuntime(
	extension Extension,
	contributions []pages.PageContribution,
	kind pages.RuntimeTemplatePackageKind,
) (*pages.ThemeRuntimeSnapshot, error) {
	if a == nil || a.ThemeRuntime == nil {
		return nil, nil
	}
	runtimeInstanceID := ""
	for _, contribution := range contributions {
		if contribution.RuntimeInstanceID == "" {
			continue
		}
		if runtimeInstanceID != "" && runtimeInstanceID != contribution.RuntimeInstanceID {
			return nil, pages.ErrThemeRuntimeConflict
		}
		runtimeInstanceID = contribution.RuntimeInstanceID
	}
	snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtimeInstanceID,
		},
		PackageRoot: PackageContentRoot(extension), Contributions: contributions,
		Templates:   runtimeTemplateDeclarations(extension.Manifest.Templates),
		DataSchemas: runtimeDataSchemaDeclarations(extension.Manifest.PackageFiles), PackageKind: kind,
		RequireDeclaredTemplates: extensionmanifest.EffectiveManifestVersion(extension.Manifest) >= extensionmanifest.ManifestVersionV3,
		SiteName:                 a.SiteName, Locales: a.Locales,
	})
	if errors.Is(err, pages.ErrThemeRuntimeMissing) {
		return nil, nil
	}
	return snapshot, err
}

func runtimeDataSchemaDeclarations(input []ManifestPackageFile) []pages.RuntimeDataSchemaDeclaration {
	result := make([]pages.RuntimeDataSchemaDeclaration, 0, len(input))
	for _, item := range input {
		if item.Kind != "schema" {
			continue
		}
		result = append(result, pages.RuntimeDataSchemaDeclaration{
			ID: item.ID, Version: item.Version, Path: item.Path, Digest: item.Digest,
		})
	}
	return result
}

func runtimeTemplateDeclarations(input []ManifestTemplate) []pages.RuntimeTemplateDeclaration {
	result := make([]pages.RuntimeTemplateDeclaration, len(input))
	for index, item := range input {
		result[index] = pages.RuntimeTemplateDeclaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Action: item.Action,
			TargetID: item.TargetID, Path: item.Path, Digest: item.Digest,
			ViewModelSchema: item.ViewModelSchema, ThemeOverrideKey: item.ThemeOverrideKey,
		}
	}
	return result
}

func (a *PageRegistryAdapter) stageThemeRuntime(snapshot *pages.ThemeRuntimeSnapshot) (bool, error) {
	if snapshot == nil || a.ThemeRuntime == nil {
		return false, nil
	}
	_, staged, err := a.ThemeRuntime.Stage(snapshot)
	return staged, err
}

func (a *PageRegistryAdapter) activateThemeRuntime(snapshot *pages.ThemeRuntimeSnapshot) error {
	if snapshot == nil || a.ThemeRuntime == nil {
		return nil
	}
	_, err := a.ThemeRuntime.ActivateExact(snapshot.Artifact())
	return err
}

func (a *PageRegistryAdapter) activeThemeArtifact() pages.RuntimeArtifact {
	if a == nil || a.ThemeRuntime == nil {
		return pages.RuntimeArtifact{}
	}
	if active, _, ok := a.ThemeRuntime.Active(); ok {
		return active.Artifact()
	}
	return pages.RuntimeArtifact{}
}

func (a *PageRegistryAdapter) compensateThemePublication(
	ctx context.Context,
	cause error,
	publication pages.ThemePublicationSnapshot,
	previous pages.RuntimeArtifact,
	target *pages.ThemeRuntimeSnapshot,
	staged bool,
	owners ...string,
) error {
	publicationErr := a.Bridge.Registry.RestoreThemePublication(ctx, publication, owners...)
	var runtimeErr error
	if previous.ExtensionID != "" && a.ThemeRuntime != nil {
		_, runtimeErr = a.ThemeRuntime.ActivateExact(previous)
	}
	removeErr := a.rollbackStagedThemeRuntime(target, staged)
	return errors.Join(cause, publicationErr, runtimeErr, removeErr)
}

func (a *PageRegistryAdapter) rollbackStagedThemeRuntime(snapshot *pages.ThemeRuntimeSnapshot, staged bool) error {
	if !staged || snapshot == nil || a.ThemeRuntime == nil {
		return nil
	}
	_, err := a.ThemeRuntime.RemoveExact(snapshot.Artifact())
	return err
}

func toThemeExt(extension Extension) pages.ThemeExtension {
	return pages.ThemeExtension{
		ID:      extension.ID,
		Version: extension.Version,
		// 旧上传包 PackagePath 可能是 package.zip；L0/L1 必须读 files/ 内容根。
		PackagePath:   PackageContentRoot(extension),
		PackageDigest: extension.PackageDigest,
		Source:        string(extension.Source),
	}
}
