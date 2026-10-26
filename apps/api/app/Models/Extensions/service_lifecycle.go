package extensions

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type LifecycleService struct {
	*Service
	editorPublications RuntimeEditorPublicationBoundary
}

func (s *serviceCore) restoreSettingsAfterRestartFailure(
	ctx context.Context,
	extensionID string,
	previous map[string]string,
	restart RuntimeQuerySettingsRestartTransaction,
	restartErr error,
) error {
	compensationCtx, cancel := settingsCompensationContext(ctx)
	defer cancel()
	if restoreErr := s.store.ReplaceSettings(compensationCtx, extensionID, previous); restoreErr != nil {
		var closeErr error
		if restart != nil {
			closeErr = restart.KeepRuntimeQueriesClosed()
		}
		return errors.Join(
			ErrSettingsRollbackFailed,
			fmt.Errorf("restart extension after settings change: %w", restartErr),
			fmt.Errorf("restore previous extension settings: %w", restoreErr),
			closeErr,
		)
	}
	if restart != nil {
		if restoreErr := restart.RestoreRuntimeQueriesAfterSettingsRollback(compensationCtx); restoreErr != nil {
			return errors.Join(
				ErrSettingsRollbackFailed,
				fmt.Errorf("restart extension after settings change: %w", restartErr),
				fmt.Errorf("restore previous runtime after settings rollback: %w", restoreErr),
				restart.KeepRuntimeQueriesClosed(),
			)
		}
	}
	return fmt.Errorf("restart extension after settings change: %w", restartErr)
}

func (s *serviceCore) resolvePreparedSettingsMutation(
	ctx context.Context,
	extensionID string,
	previous map[string]string,
	desired map[string]string,
	restart RuntimeQuerySettingsRestartTransaction,
	mutationErr error,
) error {
	if restart == nil {
		return mutationErr
	}
	compensationCtx, cancel := settingsCompensationContext(ctx)
	defer cancel()
	current, readErr := s.store.ListSettings(compensationCtx, extensionID)
	if readErr != nil {
		return errors.Join(
			ErrSettingsRollbackFailed,
			ErrSettingsCommitUnknown,
			mutationErr,
			fmt.Errorf("read extension settings after uncertain mutation: %w", readErr),
			restart.KeepRuntimeQueriesClosed(),
		)
	}
	// A transport error after commit is safe to continue only when the durable
	// document exactly matches the encrypted values prepared for this request.
	if maps.Equal(current, desired) {
		return nil
	}
	if !maps.Equal(current, previous) {
		return errors.Join(
			ErrSettingsRollbackFailed,
			ErrSettingsCommitUnknown,
			mutationErr,
			restart.KeepRuntimeQueriesClosed(),
		)
	}
	if restoreErr := restart.RestoreRuntimeQueriesAfterSettingsRollback(compensationCtx); restoreErr != nil {
		return errors.Join(
			ErrSettingsRollbackFailed,
			mutationErr,
			fmt.Errorf("restore runtime after uncommitted settings mutation: %w", restoreErr),
			restart.KeepRuntimeQueriesClosed(),
		)
	}
	return mutationErr
}

func settingsCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, settingsCompensationTimeout)
}

func canManageExtensionSettings(actor identity.Actor, extension Extension) bool {
	// 主题设置：extension.theme.manage（或兼容父权限 extension.manage）。
	if extension.Type == TypeTheme {
		return canManageThemes(actor)
	}
	// 插件设置：extension.plugin.manage；邮件提供商插件也允许 settings.mail.manage。
	// 身份 auth 提供方插件也允许 identity.provider.manage（Login Methods 内嵌设置，
	// 与邮件页委托 settings.mail.manage 对称；executable trust 仍 super_admin-only）。
	if canManagePlugins(actor) {
		return true
	}
	if actor.Can(identity.PermissionSettingsMailManage) {
		for _, provider := range extension.Manifest.Providers {
			if provider.Slot == "mail.provider" {
				return true
			}
		}
	}
	if actor.Can(identity.PermissionIdentityProviderManage) && extensionDeclaresAuthProvider(extension) {
		return true
	}
	return false
}

// extensionDeclaresAuthProvider 判断扩展是否声明了 Identity Registry 的 auth 提供方。
// 仅此类插件可由 identity.provider.manage 读写设置；不得放宽到任意插件。
func extensionDeclaresAuthProvider(extension Extension) bool {
	if extension.Manifest.Identity == nil {
		return false
	}
	for _, provider := range extension.Manifest.Identity.Providers {
		if strings.EqualFold(strings.TrimSpace(provider.Kind), "auth") {
			return true
		}
	}
	return false
}

// PublicActiveThemeSettings 返回当前激活主题的非 secret 设置（含默认值）。
// 供前台主题 layer 读取可运营配置；secret 永不出现在公开响应中。
func (s *LifecycleService) PublicActiveThemeSettings(ctx context.Context) (PublicActiveThemeSettings, error) {
	var theme Extension
	var err error
	if s.safeMode {
		theme, err = s.store.Get(ctx, DefaultThemeID)
	} else {
		theme, err = s.store.ActiveTheme(ctx)
	}
	if err != nil {
		return PublicActiveThemeSettings{Settings: map[string]string{}}, nil
	}
	if theme.Type != TypeTheme {
		return PublicActiveThemeSettings{Settings: map[string]string{}}, nil
	}
	values, err := s.listDecryptedSettings(ctx, theme)
	if err != nil {
		return PublicActiveThemeSettings{}, err
	}
	settings := map[string]string{}
	for _, setting := range theme.Manifest.Settings {
		if setting.Type == "secret" {
			continue
		}
		key := strings.TrimSpace(setting.Key)
		if key == "" {
			continue
		}
		if stored, ok := values[key]; ok {
			settings[key] = stored
			continue
		}
		settings[key] = setting.Default
	}
	return PublicActiveThemeSettings{
		ThemeID:       theme.ID,
		Version:       theme.Version,
		PackageDigest: theme.PackageDigest,
		Settings:      settings,
	}, nil
}

func (s *serviceCore) restartPluginForSettings(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	restart RuntimeQuerySettingsRestartTransaction,
	mutationKey string,
) error {
	if s.safeMode {
		return nil
	}
	if extension.Type != TypePlugin || extension.Status != StatusEnabled || extension.Manifest.Backend.Entry == "" || s.runtime == nil {
		return nil
	}
	if usesLifecycleV2(extension) {
		if err := newSettingsRestartCoordinator(s).restartLifecycleV2(ctx, actor, extension, mutationKey); err != nil {
			return errors.Join(ErrSettingsRestartFailed, err)
		}
		return nil
	}
	if hasRuntimeQueryPublication(extension.Manifest) {
		if restart == nil {
			return ErrRuntimeQuerySettingsRestartUnavailable
		}
		return restart.RestartRuntimeQueriesForSettings(ctx, extension)
	}
	if err := s.runtime.Stop(ctx, extension); err != nil {
		return err
	}
	return s.runtime.Start(ctx, extension)
}

func (s *serviceCore) preparePluginSettingsRestart(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
) (RuntimeQuerySettingsRestartTransaction, error) {
	if s.safeMode || extension.Type != TypePlugin || extension.Status != StatusEnabled ||
		extension.Manifest.Backend.Entry == "" {
		return nil, nil
	}
	if usesLifecycleV2(extension) {
		if err := newSettingsRestartCoordinator(s).preflightLifecycleV2(ctx, actor, extension); err != nil {
			return nil, errors.Join(ErrSettingsRestartUnavailable, err)
		}
		return nil, nil
	}
	if !hasRuntimeQueryPublication(extension.Manifest) {
		return nil, nil
	}
	if s.runtime == nil || extension.Manifest.Backend.ProtocolVersion != 2 {
		return nil, ErrRuntimeQuerySettingsRestartUnavailable
	}
	restarter, ok := s.queryPublications.(RuntimeQuerySettingsRestarter)
	if !ok || restarter == nil {
		return nil, ErrRuntimeQuerySettingsRestartUnavailable
	}
	return restarter.PrepareRuntimeQueriesForSettings(ctx, extension)
}

func (s *LifecycleService) MatchRoute(ctx context.Context, extensionID string, method string, routePath string) (MatchedRoute, error) {
	if s.safeMode {
		return MatchedRoute{}, ErrRouteNotFound
	}
	extension, err := s.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return MatchedRoute{}, err
	}
	if extension.Type != TypePlugin || extension.Status != StatusEnabled {
		return MatchedRoute{}, ErrRouteNotFound
	}
	normalizedPath := normalizeRoutePath(routePath)
	pathExists := false
	for _, route := range extension.Manifest.Routes {
		if normalizeRoutePath(route.Path) != normalizedPath {
			continue
		}
		pathExists = true
		for _, allowed := range route.Methods {
			if strings.EqualFold(allowed, method) {
				return MatchedRoute{Extension: extension, Route: route, Path: normalizedPath}, nil
			}
		}
	}
	if pathExists {
		return MatchedRoute{}, ErrRouteMethodNotAllowed
	}
	return MatchedRoute{}, ErrRouteNotFound
}

func (s *LifecycleService) Enable(ctx context.Context, actor identity.Actor, id string, input EnableInput) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	s.assetPublicationMu.Lock()
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		s.assetPublicationMu.Unlock()
		return Extension{}, err
	}
	if extension.Type == TypeTheme {
		s.assetPublicationMu.Unlock()
		return Extension{}, ErrThemeActivationRequired
	}
	if err := requireEnablePreflight(extension); err != nil {
		s.assetPublicationMu.Unlock()
		return Extension{}, err
	}
	if usesLifecycleV2(extension) {
		s.assetPublicationMu.Unlock()
		return s.enableLifecycleV2(ctx, actor, extension, input)
	}
	defer s.assetPublicationMu.Unlock()
	hasRuntimeQuerySurfaces := hasRuntimeQueryPublication(extension.Manifest)
	hasRuntimeCaches := len(extension.Manifest.Cache) > 0 && s.cachePublications != nil
	hasRuntimeContent := hasRuntimeContentPublication(extension.Manifest)
	hasRuntimeEditor := hasRuntimeEditorPublication(extension.Manifest)
	if hasRuntimeQuerySurfaces && s.queryPublications == nil {
		return Extension{}, ErrRuntimeQueryPublicationUnavailable
	}
	if hasRuntimeContent && s.contentPublications == nil {
		return Extension{}, ErrRuntimeContentPublicationUnavailable
	}
	if hasRuntimeEditor && s.lifecycle.editorPublications == nil {
		return Extension{}, ErrRuntimeEditorPublicationUnavailable
	}
	assetBefore := s.captureAssetPublicationSnapshot()
	if err := s.validateExtensionAssetPublication(ctx, assetBefore, extension); err != nil {
		s.recordEnableFailure(ctx, actor, extension.ID, err)
		return Extension{}, fmt.Errorf("%w: asset registry preflight failed: %v", ErrPreflightFailed, err)
	}
	if s.trustChallengesEnabled {
		if s.executableTrust == nil {
			return Extension{}, ErrTrustChallengeRequired
		}
		if err := s.executableTrust.ConfirmEnable(ctx, actor, extension, input.ConfirmationToken); err != nil {
			return Extension{}, err
		}
	} else {
		// v1 兼容：非内置后端插件启动子进程仅允许 super_admin。
		if err := requireSuperAdminForUntrustedBackend(actor, extension.Source, extension.Manifest); err != nil {
			s.denyUntrustedBackend(ctx, actor, extension.ID, "enable")
			return Extension{}, err
		}
	}

	// 首次启用（非 restart）且存在 Host 能力时，要求运营显式确认（F2.1）。
	if !s.trustChallengesEnabled && extension.Status != StatusEnabled {
		capKeys, _ := extensionmanifest.ResolvedCapabilities(extension.Manifest)
		if capabilities.RequiresConfirmation(capKeys) && !input.ConfirmCapabilities {
			return Extension{}, ErrCapabilityConfirmationRequired
		}
		// V3 challenge 关闭时仍需 exact live grant，protocol v2 子进程握手依赖它。
		// super_admin + confirmCapabilities 后幂等写入兼容授权。
		if s.executableTrust != nil && RequiresExecutableTrust(extension) {
			if err := s.executableTrust.EnsureCompatibilityGrant(ctx, actor, extension); err != nil {
				s.recordEnableFailure(ctx, actor, extension.ID, err)
				return Extension{}, err
			}
		}
	}

	// F4.5：manifest requiresFeatures 必须全部开启。
	if err := s.ensureRequiredFeatures(ctx, extension.Manifest.RequiresFeatures); err != nil {
		return Extension{}, err
	}

	if _, err := s.preflightActivationDependencies(ctx, extension); err != nil {
		s.recordEnableFailure(ctx, actor, extension.ID, err)
		return Extension{}, err
	}

	if err := s.verifyExtension(ctx, extension); err != nil {
		s.recordEnableFailure(ctx, actor, extension.ID, err)
		return Extension{}, err
	}
	assetMutation, err := s.publishExactExtensionAssetPublication(ctx, assetBefore, extension)
	if err != nil {
		s.recordEnableFailure(ctx, actor, extension.ID, err)
		return Extension{}, fmt.Errorf("%w: asset registry publication failed: %v", ErrPreflightFailed, err)
	}
	enabled, err := s.enableLegacyPluginState(ctx, extension, actor.ID)
	if err != nil {
		if rollbackErr := s.rollbackExactAssetMutation(assetMutation); rollbackErr != nil {
			return Extension{}, errors.Join(err, fmt.Errorf("restore asset publication after enable failure: %w", rollbackErr))
		}
		return Extension{}, err
	}
	// 启用成功后从 Manifest 恢复 SettingsLifecycle Schema（供后台保存/迁移）。
	if regErr := s.RegisterSettingsLifecycleFromManifest(enabled); regErr != nil {
		s.recordEnableFailure(ctx, actor, enabled.ID, regErr)
		return Extension{}, regErr
	}
	if enabled.Type == TypePlugin && enabled.Manifest.Backend.Entry != "" && s.runtime != nil {
		var startErr error
		if s.activation != nil {
			startErr = s.activation.Start(ctx, s.runtime, enabled, ActivationTriggerEnable, actor.ID, NewActivationBootID())
		} else {
			startErr = s.runtime.Start(ctx, enabled)
		}
		if startErr != nil {
			if _, disableErr := s.disableLegacyPluginState(ctx, enabled, actor.ID); disableErr != nil {
				startErr = errors.Join(
					startErr,
					fmt.Errorf("publish compensating plugin runtime disable: %w", disableErr),
				)
			}
			if rollbackErr := s.rollbackExactAssetMutation(assetMutation); rollbackErr != nil {
				startErr = errors.Join(startErr, fmt.Errorf("restore asset publication after runtime failure: %w", rollbackErr))
			}
			s.recordEnableFailure(ctx, actor, enabled.ID, startErr)
			return Extension{}, errors.Join(ErrRuntimeFailed, startErr)
		}
	}
	var queryMutation RuntimeQueryPublicationMutation
	if hasRuntimeQuerySurfaces {
		queryMutation, err = s.queryPublications.PublishRuntimeQueries(ctx, enabled)
		if err != nil || queryMutation == nil {
			if err == nil {
				err = ErrRuntimeQueryPublicationUnavailable
			}
			failure := s.compensateLegacyQueryEnable(ctx, enabled, assetMutation, queryMutation, actor.ID, err)
			s.recordEnableFailure(ctx, actor, enabled.ID, failure)
			return Extension{}, errors.Join(ErrRuntimeFailed, fmt.Errorf("publish runtime queries: %w", failure))
		}
	}
	var cacheMutation RuntimeCachePublicationMutation
	if hasRuntimeCaches {
		cacheMutation, err = s.cachePublications.PublishRuntimeCaches(ctx, enabled)
		if err != nil || cacheMutation == nil {
			if err == nil {
				err = ErrRuntimeCachePublicationUnavailable
			}
			failure := s.compensateLegacyCacheEnable(
				ctx, enabled, assetMutation, queryMutation, cacheMutation, actor.ID, err,
			)
			s.recordEnableFailure(ctx, actor, enabled.ID, failure)
			return Extension{}, errors.Join(ErrRuntimeFailed, fmt.Errorf("publish runtime caches: %w", failure))
		}
	}
	var contentMutation RuntimeContentPublicationMutation
	if hasRuntimeContent {
		contentMutation, err = s.contentPublications.PublishRuntimeContent(ctx, enabled)
		if err != nil || contentMutation == nil {
			if err == nil {
				err = ErrRuntimeContentPublicationUnavailable
			}
			if hasRuntimeCaches {
				err = s.compensateLegacyCacheEnable(ctx, enabled, assetMutation, queryMutation, cacheMutation, actor.ID, err)
			} else if hasRuntimeQuerySurfaces {
				err = s.compensateLegacyQueryEnable(ctx, enabled, assetMutation, queryMutation, actor.ID, err)
			} else {
				err = compensateLegacyContentEnable(s.serviceCore, ctx, enabled, assetMutation, contentMutation, actor.ID, err)
			}
			s.recordEnableFailure(ctx, actor, enabled.ID, err)
			return Extension{}, errors.Join(ErrRuntimeFailed, err)
		}
	}
	editorMutation, err := publishLegacyRuntimeEditor(
		s, ctx, enabled, assetMutation, queryMutation, cacheMutation, contentMutation, actor.ID,
	)
	if err != nil {
		s.recordEnableFailure(ctx, actor, enabled.ID, err)
		return Extension{}, errors.Join(ErrRuntimeFailed, fmt.Errorf("publish runtime editor: %w", err))
	}
	// 插件 enable：注册页面贡献（add/replace 候选）；replace 仍需 super_admin 批准。
	if enabled.Type == TypePlugin && s.pageRegistry != nil {
		if err := s.pageRegistry.RegisterPluginPackage(ctx, enabled); err != nil {
			// 页面贡献失败不静默：按声明发布的逆序回滚 enable。
			err = compensateLegacyEditorEnable(
				s.serviceCore, ctx, enabled, assetMutation, queryMutation, cacheMutation,
				contentMutation, editorMutation, actor.ID, err,
			)
			s.pageRegistry.ClearExtension(enabled.ID)
			s.recordEnableFailure(ctx, actor, enabled.ID, err)
			return Extension{}, fmt.Errorf("%w: page contributions: %v", ErrPreflightFailed, err)
		}
	}
	capKeys, _ := extensionmanifest.ResolvedCapabilities(enabled.Manifest)
	auditMetadata := map[string]any{
		"extensionId":  enabled.ID,
		"type":         enabled.Type,
		"capabilities": capKeys,
	}
	auditEventID, _ := s.appendAuditReturningID(ctx, actor, audit.ActionExtensionEnable, auditMetadata)
	if _, err := s.publishLegacyRuntimeIdentity(ctx, enabled, actor.ID, auditEventID); err != nil {
		failure := compensateLegacyEditorEnable(
			s.serviceCore, ctx, enabled, assetMutation, queryMutation, cacheMutation,
			contentMutation, editorMutation, actor.ID, err,
		)
		s.recordEnableFailure(ctx, actor, enabled.ID, failure)
		return Extension{}, errors.Join(ErrRuntimeFailed, fmt.Errorf("publish runtime identity: %w", failure))
	}
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: enabled.ID,
		ActorUserID: actor.ID,
		Action:      EventEnabled,
		Message:     "Extension enabled.",
	})
	if enabled.Type == TypePlugin && s.runtime != nil {
		s.runtime.EmitHook(ctx, appevents.ExtensionEnabled, map[string]any{"extensionId": enabled.ID})
	}
	publishLegacyComponentRuntime(ctx, s.serviceCore, enabled) // 无后端但有公开 L2 组件的包在此收敛
	return s.decorateRuntime(ctx, enabled), nil
}

func (s *LifecycleService) Disable(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	return s.DisableWithInput(ctx, actor, id, LifecycleRequestInput{})
}

// DisableWithInput preserves the old Disable call surface while allowing HTTP
// callers to bind a stable idempotency key for protocol V2 plugins.
func (s *LifecycleService) DisableWithInput(ctx context.Context, actor identity.Actor, id string, input LifecycleRequestInput) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	s.assetPublicationMu.Lock()
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		s.assetPublicationMu.Unlock()
		return Extension{}, err
	}
	if extension.Type == TypeTheme {
		s.assetPublicationMu.Unlock()
		return Extension{}, ErrThemeActivationRequired
	}
	if usesLifecycleV2(extension) {
		s.assetPublicationMu.Unlock()
		return s.disableLifecycleV2(ctx, actor, extension, input)
	}
	defer s.assetPublicationMu.Unlock()
	hasRuntimeQuerySurfaces := hasRuntimeQueryPublication(extension.Manifest)
	hasRuntimeCaches := len(extension.Manifest.Cache) > 0 && s.cachePublications != nil
	hasRuntimeContent := hasRuntimeContentPublication(extension.Manifest)
	hasRuntimeEditor := hasRuntimeEditorPublication(extension.Manifest)
	if hasRuntimeQuerySurfaces && s.queryPublications == nil {
		return Extension{}, ErrRuntimeQueryPublicationUnavailable
	}
	if hasRuntimeContent && s.contentPublications == nil {
		return Extension{}, ErrRuntimeContentPublicationUnavailable
	}
	if hasRuntimeEditor && s.lifecycle.editorPublications == nil {
		return Extension{}, ErrRuntimeEditorPublicationUnavailable
	}
	assetBefore := s.captureAssetPublicationSnapshot()
	assetMutation, err := s.quarantineExactAssetPublication(ctx, assetBefore, extension)
	if err != nil {
		return Extension{}, fmt.Errorf("remove exact asset publication: %w", err)
	}
	var disabled Extension
	var queryMutation RuntimeQueryPublicationMutation
	if hasRuntimeQuerySurfaces {
		var quarantineErr error
		queryMutation, quarantineErr = s.queryPublications.QuarantineRuntimeQueries(ctx, extension)
		if quarantineErr != nil || queryMutation == nil {
			if quarantineErr == nil {
				quarantineErr = ErrRuntimeQueryPublicationUnavailable
			}
			if restoreErr := s.rollbackExactAssetMutation(assetMutation); restoreErr != nil {
				quarantineErr = errors.Join(quarantineErr, fmt.Errorf("restore asset publication: %w", restoreErr))
			}
			return Extension{}, quarantineErr
		}
	}
	var cacheMutation RuntimeCachePublicationMutation
	if hasRuntimeCaches {
		var quarantineErr error
		cacheMutation, quarantineErr = s.cachePublications.QuarantineRuntimeCaches(ctx, extension)
		if quarantineErr != nil || cacheMutation == nil {
			if quarantineErr == nil {
				quarantineErr = ErrRuntimeCachePublicationUnavailable
			}
			return Extension{}, s.compensateLegacyCacheDisable(
				assetMutation, queryMutation, cacheMutation, nil, quarantineErr,
			)
		}
	}
	var contentMutation RuntimeContentPublicationMutation
	if hasRuntimeContent {
		var quarantineErr error
		contentMutation, quarantineErr = s.contentPublications.QuarantineRuntimeContent(ctx, extension)
		if quarantineErr != nil || contentMutation == nil {
			if quarantineErr == nil {
				quarantineErr = ErrRuntimeContentPublicationUnavailable
			}
			if hasRuntimeCaches {
				quarantineErr = s.compensateLegacyCacheDisable(
					assetMutation, queryMutation, cacheMutation, nil, quarantineErr,
				)
			} else if hasRuntimeQuerySurfaces {
				quarantineErr = s.compensateLegacyQueryDisable(assetMutation, queryMutation, nil, quarantineErr)
			} else if restoreErr := s.rollbackExactAssetMutation(assetMutation); restoreErr != nil {
				quarantineErr = errors.Join(quarantineErr, restoreErr)
			}
			return Extension{}, quarantineErr
		}
	}
	editorMutation, err := quarantineLegacyRuntimeEditor(
		s, ctx, extension, assetMutation, queryMutation, cacheMutation, contentMutation,
		hasRuntimeCaches, hasRuntimeQuerySurfaces,
	)
	if err != nil {
		return Extension{}, err
	}
	auditMetadata := map[string]any{
		"extensionId": extension.ID,
		"type":        extension.Type,
	}
	auditEventID, _ := s.appendAuditReturningID(ctx, actor, audit.ActionExtensionDisable, auditMetadata)
	identityMutation, err := s.quarantineLegacyRuntimeIdentity(ctx, extension, actor.ID, auditEventID)
	if err != nil {
		err = rollbackRuntimeEditorMutation(editorMutation, err)
		err = rollbackRuntimeContentMutation(contentMutation, err)
		return Extension{}, s.compensateLegacyIdentityDisable(
			assetMutation, queryMutation, cacheMutation, nil, err,
		)
	}
	if hasRuntimeCaches {
		disabled, err = s.disableLegacyCachePlugin(
			ctx, extension, assetMutation, queryMutation, cacheMutation, identityMutation, actor.ID,
		)
		if err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			return Extension{}, rollbackRuntimeContentMutation(contentMutation, err)
		}
	} else if hasRuntimeQuerySurfaces {
		disabled, err = s.disableLegacyQueryPlugin(ctx, extension, assetMutation, queryMutation, identityMutation, actor.ID)
		if err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			return Extension{}, rollbackRuntimeContentMutation(contentMutation, err)
		}
	} else if identityMutation != nil {
		if err := s.clearPluginProviderSelections(ctx, extension.ID, keepMailProviderSelection); err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			err = rollbackRuntimeContentMutation(contentMutation, err)
			return Extension{}, s.compensateLegacyIdentityDisable(
				assetMutation, nil, nil, identityMutation, err,
			)
		}
		if s.pageRegistry != nil {
			s.pageRegistry.ClearExtension(extension.ID)
		}
		disabled, err = s.disableLegacyPluginState(ctx, extension, actor.ID)
		if err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			err = rollbackRuntimeContentMutation(contentMutation, err)
			return Extension{}, s.compensateLegacyIdentityDisable(
				assetMutation, nil, nil, identityMutation, err,
			)
		}
		if s.runtime != nil {
			_ = s.runtime.Stop(ctx, extension)
			if extension.Status == StatusEnabled {
				s.runtime.EmitHook(ctx, appevents.ExtensionDisabled, map[string]any{
					"extensionId": extension.ID,
					"reason":      "lifecycle_drain",
				})
			}
		}
	} else if contentMutation != nil {
		disabled, err = disableLegacyContentPlugin(s.serviceCore, ctx, extension, assetMutation, contentMutation, actor.ID)
		if err != nil {
			return Extension{}, rollbackRuntimeEditorMutation(editorMutation, err)
		}
	} else {
		// F2.4：未绑定运行时 Query/Cache/Content publication 的 legacy 插件保留原有 drain 顺序。
		if err := s.drainPluginRuntime(ctx, extension); err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			if rollbackErr := s.rollbackExactAssetMutation(assetMutation); rollbackErr != nil {
				return Extension{}, errors.Join(err, fmt.Errorf("restore asset publication after drain failure: %w", rollbackErr))
			}
			return Extension{}, err
		}
		// 立即撤销页面贡献，使 replace 绑定在 Resolve 时回退 core。
		if s.pageRegistry != nil {
			s.pageRegistry.ClearExtension(extension.ID)
		}
		disabled, err = s.disableLegacyPluginState(ctx, extension, actor.ID)
		if err != nil {
			err = rollbackRuntimeEditorMutation(editorMutation, err)
			if restoreErr := s.rollbackExactAssetMutation(assetMutation); restoreErr != nil {
				return Extension{}, errors.Join(err, fmt.Errorf("restore asset publication after disable failure: %w", restoreErr))
			}
			return Extension{}, err
		}
	}
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: disabled.ID,
		ActorUserID: actor.ID,
		Action:      EventDisabled,
		Message:     "Extension disabled.",
	})
	removeLegacyComponentRuntime(ctx, s.serviceCore, disabled.ID) // 与启用对称：移除组件包运行时注册
	erasePluginSessionState(ctx, s.serviceCore, disabled.ID)      // 禁用即擦除该扩展的插件会话状态
	return s.decorateRuntime(ctx, disabled), nil
}

func (s *LifecycleService) VerifyExtension(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	if !canManagePlugins(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return Extension{}, err
	}
	// 预检会触碰后端入口；非内置后端包同样仅 super_admin。
	if err := requireSuperAdminForUntrustedBackend(actor, extension.Source, extension.Manifest); err != nil {
		s.denyUntrustedBackend(ctx, actor, extension.ID, "verify")
		return Extension{}, err
	}
	if err := s.verifyExtension(ctx, extension); err != nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: extension.ID,
			ActorUserID: actor.ID,
			Action:      EventEnableFailed,
			Message:     err.Error(),
		})
		return Extension{}, err
	}
	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: extension.ID,
		ActorUserID: actor.ID,
		Action:      EventVerified,
		Message:     "Extension preflight verified.",
	})
	return s.decorateRuntime(ctx, extension), nil
}

// ActivateTheme 是无预览的兼容入口（无 HTTP 路由，走 startup-repair 语义）。
// 若将来开路由，必须改走 ActivateThemeFromPreview，否则受保护默认主题会变成 Host 批准。
func (s *LifecycleService) ActivateTheme(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	return s.activateTheme(ctx, actor, id, ThemeActivationInput{}, false)
}

func (s *LifecycleService) ActivateThemeFromPreview(
	ctx context.Context,
	actor identity.Actor,
	id string,
	input ThemeActivationInput,
) (Extension, error) {
	return s.activateTheme(ctx, actor, id, input, true)
}

func (s *serviceCore) activateTheme(
	ctx context.Context,
	actor identity.Actor,
	id string,
	input ThemeActivationInput,
	requirePreview bool,
) (Extension, error) {
	// DB commit and process-local Page/ThemeRuntime publication are one ordered
	// critical section on this node. P8 still owns the durable revision watcher
	// required for cross-node convergence.
	s.themeActivationMu.Lock()
	defer s.themeActivationMu.Unlock()
	if s.themeRuntimeUnavailable {
		return Extension{}, ErrThemeRuntimeUnavailable
	}
	s.assetPublicationMu.Lock()
	defer s.assetPublicationMu.Unlock()
	assetBefore := s.captureAssetPublicationSnapshot()
	if !canManageThemes(actor) {
		return Extension{}, identity.ErrPermissionDenied
	}
	if input.ApproveCoreReplacements && !actor.IsSuperAdmin() {
		return Extension{}, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return Extension{}, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return Extension{}, err
	}
	if extension.Type != TypeTheme {
		return Extension{}, ErrThemeActivationRequired
	}
	activationTarget := extension
	if requirePreview {
		if staged, ok := extension.StagedArtifact(); ok {
			activationTarget = staged
		}
	}
	if err := requireEnablePreflight(activationTarget); err != nil {
		return Extension{}, err
	}
	if requirePreview && (strings.TrimSpace(input.Version) == "" || strings.TrimSpace(input.PackageDigest) == "" ||
		strings.TrimSpace(input.Version) != activationTarget.Version ||
		!strings.EqualFold(strings.TrimSpace(input.PackageDigest), activationTarget.PackageDigest)) {
		return Extension{}, ErrThemePreviewStale
	}
	if err := s.verifyExtension(ctx, activationTarget); err != nil {
		s.recordEnableFailure(ctx, actor, extension.ID, err)
		return Extension{}, err
	}

	// 记录旧主题，失败时完整回滚。
	var previous *Extension
	if prev, prevErr := s.store.ActiveTheme(ctx); prevErr == nil {
		p := prev
		previous = &p
	}

	// 1) 完整预检（manifest / theme.json / 模板 / CSS / routes / 贡献）—— 不改 DB、不改 Registry。
	// 按「用新主题替换旧活动主题」的最终状态校验，允许新旧主题相同 add 路径。
	// 主题预检失败使用 ErrBuildFailed，避免前端误显示「后端入口」插件文案。
	prevThemeID := ""
	if previous != nil {
		prevThemeID = previous.ID
	}
	if s.pageRegistry != nil {
		if err := s.pageRegistry.PreflightThemePackage(ctx, activationTarget, prevThemeID); err != nil {
			wrapped := fmt.Errorf("%w: %v", ErrBuildFailed, err)
			s.recordEnableFailure(ctx, actor, extension.ID, wrapped)
			return Extension{}, wrapped
		}
	}
	if s.componentRegistry != nil {
		target := activationTarget
		if err := s.componentRegistry.ValidateThemeTransition(&target, previous); err != nil {
			wrapped := fmt.Errorf("%w: component registry preflight failed: %v", ErrBuildFailed, err)
			s.recordEnableFailure(ctx, actor, extension.ID, wrapped)
			return Extension{}, wrapped
		}
	}
	if err := s.validateThemeAssetTransition(ctx, assetBefore, &activationTarget, previous); err != nil {
		wrapped := fmt.Errorf("%w: asset registry preflight failed: %v", ErrBuildFailed, err)
		s.recordEnableFailure(ctx, actor, extension.ID, wrapped)
		return Extension{}, wrapped
	}

	// 所有静态预检完成后、任何 DB/Registry 切换前消费一次性 exact-artifact
	// challenge。普通 L0/L1 主题不请求可执行权限，因此继续零确认激活。
	var trustReceipt executableTrustGrantReceipt
	if s.trustChallengesEnabled && RequiresExecutableTrust(activationTarget) {
		if s.executableTrust == nil {
			return Extension{}, ErrTrustChallengeRequired
		}
		trustReceipt, err = s.executableTrust.confirmEnable(ctx, actor, activationTarget, input.ConfirmationToken)
		if err != nil {
			return Extension{}, err
		}
	}

	// 2) DB 事务切换活动主题（store.ActivateTheme 内部事务；同主题再激活也幂等写状态）。
	var active Extension
	var activationPublication ThemeRuntimePublication
	if requirePreview {
		input.ActorUserID = actor.ID
		result, activateErr := s.store.ActivateThemeExact(ctx, extension.ID, input)
		err = activateErr
		active = result.Extension
		activationPublication = result.Publication
	} else {
		result, activateErr := s.store.ActivateTheme(ctx, extension.ID)
		err = activateErr
		active = result.Extension
		activationPublication = result.Publication
	}
	if err != nil {
		return Extension{}, s.compensateThemeActivationTrust(ctx, actor, trustReceipt, activationTarget, previous, err)
	}
	committedSource := previous
	var sourceErr error
	if !themeRuntimePublicationSourceMatches(activationPublication, previous) {
		committedSource, sourceErr = s.themePublicationSource(ctx, activationPublication)
	}
	if sourceErr != nil {
		failure := fmt.Errorf("resolve exact theme activation source: %w", sourceErr)
		previous = nil
		return Extension{}, s.compensateCommittedThemeActivation(
			ctx, actor, trustReceipt, activationTarget, active, previous, activationPublication,
			assetBefore, false, failure,
		)
	}
	previous = committedSource

	// 多节点下，另一个失败请求可能在本节点等待 DB CAS 时精确撤销了新 grant。
	// Registry 发布前再次确认 live identity；丢失授权时立即回滚 DB，绝不导入 L2。
	if s.trustChallengesEnabled && RequiresExecutableTrust(active) {
		trusted, trustErr := s.executableTrust.TrustedArtifact(ctx, active)
		if trustErr != nil || !trusted {
			if trustErr == nil {
				trustErr = ErrTrustGrantNotFound
			}
			failure := fmt.Errorf("theme executable trust lost before publication: %w", trustErr)
			rollback, rollbackErr := s.store.CompensateThemeActivation(ctx, activationPublication, previous)
			if rollbackErr != nil {
				failure = errors.Join(failure, fmt.Errorf("theme activation compensation failed: %w", rollbackErr))
			} else {
				failedTarget := active
				if s.componentRegistry != nil {
					if componentErr := s.componentRegistry.PublishThemeTransition(
						previous, &failedTarget, rollback.Publication.Revision,
					); componentErr != nil {
						failure = errors.Join(failure, fmt.Errorf("component registry compensation failed: %w", componentErr))
					}
				}
			}
			return Extension{}, s.compensateThemeActivationTrust(
				ctx, actor, trustReceipt, activationTarget, previous, failure,
			)
		}
	}

	// 3) 先原子替换 Component/Asset Registry，再发布 Page/ThemeRuntime。
	//    Registry 失败时 Page 尚未切换；Page 失败时则按 exact
	//    target 反向恢复 Registry，两条路径都使用 DB CAS 补偿。
	if s.componentRegistry != nil {
		target := active
		if publishErr := s.componentRegistry.PublishThemeTransition(
			&target, previous, activationPublication.Revision,
		); publishErr != nil {
			failure := fmt.Errorf("%w: component registry register failed: %v", ErrBuildFailed, publishErr)
			return Extension{}, s.compensateCommittedThemeActivation(
				ctx, actor, trustReceipt, activationTarget, active, previous, activationPublication,
				assetBefore, false, failure,
			)
		}
	}
	assetAfter, publishErr := s.publishThemeAssetTransition(ctx, assetBefore, &active, previous)
	if publishErr != nil {
		failure := fmt.Errorf("%w: asset registry register failed: %v", ErrBuildFailed, publishErr)
		return Extension{}, s.compensateCommittedThemeActivation(
			ctx, actor, trustReceipt, activationTarget, active, previous, activationPublication,
			assetBefore, false, failure,
		)
	}

	if s.pageRegistry != nil {
		var registerErr error
		if input.ApproveCoreReplacements {
			registerErr = s.pageRegistry.RegisterThemePackageReplacingApproved(ctx, active, prevThemeID, actor.ID)
		} else {
			registerErr = s.pageRegistry.RegisterThemePackageReplacing(ctx, active, prevThemeID)
		}
		if registerErr != nil {
			failure := fmt.Errorf("%w: registry register failed: %v", ErrBuildFailed, registerErr)
			return Extension{}, s.compensateCommittedThemeActivation(
				ctx, actor, trustReceipt, activationTarget, active, previous, activationPublication,
				assetAfter, true, failure,
			)
		}
	}

	_, _ = s.store.CreateEvent(ctx, EventInput{
		ExtensionID: active.ID,
		ActorUserID: actor.ID,
		Action:      EventThemeActivated,
		Message:     "Theme activated via runtime page registry (no site rebuild).",
	})
	s.appendAudit(ctx, actor, audit.ActionExtensionActivate, map[string]any{
		"extensionId":              active.ID,
		"previousId":               themeIDOrEmpty(previous),
		"queued":                   false,
		"runtime":                  true,
		"coreReplacementsApproved": input.ApproveCoreReplacements,
		"publicationRevision":      activationPublication.Revision,
	})
	return active, nil
}

func (s *serviceCore) compensateCommittedThemeActivation(
	ctx context.Context,
	actor identity.Actor,
	trustReceipt executableTrustGrantReceipt,
	activationTarget Extension,
	active Extension,
	previous *Extension,
	publication ThemeRuntimePublication,
	assetAfter assetPublicationSnapshot,
	assetPublished bool,
	failure error,
) error {
	rollback, rollbackErr := s.store.CompensateThemeActivation(ctx, publication, previous)
	var componentRollbackErr error
	if rollbackErr != nil {
		failure = errors.Join(failure, fmt.Errorf("theme activation compensation failed: %w", rollbackErr))
	} else if s.componentRegistry != nil {
		failedTarget := active
		componentRollbackErr = s.componentRegistry.PublishThemeTransition(
			previous, &failedTarget, rollback.Publication.Revision,
		)
		if componentRollbackErr != nil {
			failure = errors.Join(failure, fmt.Errorf("component registry compensation failed: %w", componentRollbackErr))
		}
	}
	var assetRollbackErr error
	if rollbackErr == nil && assetPublished {
		failedTarget := active
		_, assetRollbackErr = s.rollbackThemeAssetTransition(ctx, assetAfter, previous, &failedTarget)
		if assetRollbackErr != nil {
			failure = errors.Join(failure, fmt.Errorf("asset registry compensation failed: %w", assetRollbackErr))
		}
	}
	if rollbackErr != nil || componentRollbackErr != nil || assetRollbackErr != nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: active.ID, ActorUserID: actor.ID, Action: EventEnableFailed,
			Message: failure.Error(),
		})
	}
	return s.compensateThemeActivationTrust(ctx, actor, trustReceipt, activationTarget, previous, failure)
}
