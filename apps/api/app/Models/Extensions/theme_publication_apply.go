package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

var ErrThemeRuntimeApplyFailed = errors.New("extensions: theme runtime publication apply failed")

// ApplyThemeRuntimePublication converges process-local Page/ThemeRuntime and
// Component Registry state. The desired database state and publication row
// were committed by the activation transaction before this method runs.
func (s *ThemeService) ApplyThemeRuntimePublication(ctx context.Context, publication ThemeRuntimePublication) error {
	if s == nil || ctx == nil || s.pageRegistry == nil || !validThemeRuntimePublication(publication) {
		return ErrThemeRuntimeApplyFailed
	}
	s.themeActivationMu.Lock()
	defer s.themeActivationMu.Unlock()
	if s.themeRuntimeUnavailable {
		return errors.Join(ErrThemeRuntimeApplyFailed, ErrThemeRuntimeUnavailable)
	}
	s.assetPublicationMu.Lock()
	defer s.assetPublicationMu.Unlock()
	assetBefore := s.captureAssetPublicationSnapshot()

	items, err := s.store.List(ctx)
	if err != nil {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	source, err := s.themePublicationSource(ctx, publication)
	if err != nil {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	if publication.DesiredState == ThemeRuntimePublicationNone {
		if s.componentRegistry != nil {
			if err := s.componentRegistry.ValidateThemeTransition(nil, source); err != nil {
				return errors.Join(ErrThemeRuntimeApplyFailed, err)
			}
			if err := s.componentRegistry.PublishThemeTransition(nil, source, publication.Revision); err != nil {
				return errors.Join(ErrThemeRuntimeApplyFailed, err)
			}
		}
		if err := s.validateThemeAssetTransition(ctx, assetBefore, nil, source); err != nil {
			return errors.Join(ErrThemeRuntimeApplyFailed, err)
		}
		if _, err := s.publishThemeAssetTransition(ctx, assetBefore, nil, source); err != nil {
			if s.componentRegistry != nil {
				if rollbackErr := s.componentRegistry.RollbackThemeTransition(
					source, nil, publication.Revision,
				); rollbackErr != nil {
					err = errors.Join(err, fmt.Errorf("restore source component publication: %w", rollbackErr))
				}
			}
			return errors.Join(ErrThemeRuntimeApplyFailed, err)
		}
		for _, item := range items {
			if item.Type == TypeTheme {
				s.pageRegistry.ClearExtension(item.ID)
			}
		}
		return nil
	}

	target, err := s.store.Get(ctx, publication.ThemeID)
	if err != nil || target.Type != TypeTheme || target.Status != StatusEnabled ||
		target.Version != publication.ThemeVersion || target.PackageDigest != publication.PackageDigest {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	if err := s.verifyExtension(ctx, target); err != nil {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	if err := s.pageRegistry.PreflightThemePackage(ctx, target, publication.SourceThemeID); err != nil {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	if s.componentRegistry != nil {
		if err := s.componentRegistry.ValidateThemeTransition(&target, source); err != nil {
			return errors.Join(ErrThemeRuntimeApplyFailed, err)
		}
	}
	if err := s.validateThemeAssetTransition(ctx, assetBefore, &target, source); err != nil {
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	if target.ID != DefaultThemeID {
		defaultTheme, defaultErr := s.store.Get(ctx, DefaultThemeID)
		if defaultErr != nil {
			return errors.Join(ErrThemeRuntimeApplyFailed, defaultErr)
		}
		if defaultErr = s.pageRegistry.RegisterDefaultThemeFallback(ctx, defaultTheme); defaultErr != nil {
			return errors.Join(ErrThemeRuntimeApplyFailed, defaultErr)
		}
	}
	componentPublished := false
	if s.componentRegistry != nil {
		if err := s.componentRegistry.PublishThemeTransition(&target, source, publication.Revision); err != nil {
			return errors.Join(ErrThemeRuntimeApplyFailed, err)
		}
		componentPublished = true
	}
	assetAfter, publishErr := s.publishThemeAssetTransition(ctx, assetBefore, &target, source)
	if publishErr != nil {
		if componentPublished {
			if rollbackErr := s.componentRegistry.RollbackThemeTransition(
				source, &target, publication.Revision,
			); rollbackErr != nil {
				publishErr = errors.Join(publishErr, fmt.Errorf("restore source component publication: %w", rollbackErr))
			}
		}
		return errors.Join(ErrThemeRuntimeApplyFailed, publishErr)
	}
	switch {
	case publication.CoreReplacementsApproved && publication.ApprovalSource == ThemeApprovalSourceHost:
		// Host 批准只可能来自受保护内置默认主题（store 层已强制），
		// 绑定写 host 来源而不是伪造审批用户。
		err = s.pageRegistry.RegisterThemePackageReplacingHostApproved(
			ctx, target, publication.SourceThemeID,
		)
	case publication.CoreReplacementsApproved:
		err = s.pageRegistry.RegisterThemePackageReplacingApproved(
			ctx, target, publication.SourceThemeID, publication.ActorUserID,
		)
	case isProtectedBuiltinDefaultTheme(target) && publication.SourceCoreReplacementsApproved &&
		publication.ActorUserID == 0:
		// 兜底：未批准、没有人类 actor，但目标是受保护内置默认主题、且来源主题的
		// 核心页替换是已批准的——这只可能是旧自愈缺陷（staged 精确晋升没带 Host
		// 批准，如现网 rev 23）留下的历史账本。按普通替换落地会删空
		// page_provider_bindings，导致整站回落 Core；这里按 Host 批准落地，并留
		// 告警，启动自愈（defaultThemeApprovalRepair.EnsureDefaultThemeApproved）
		// 随后追加一版 approved 账本把它转正。
		//
		// ActorUserID != 0 表示这是人类主动「不批准」的激活：那里必须尊重未批准，
		// 否则等于给非 super_admin 一条绕过审批拿到核心页替换的路径。
		slog.Warn(
			"theme runtime publication is missing the protected default approval; publishing as host-approved",
			"event", "theme.publication.host_approval_fallback",
			"revision", publication.Revision,
			"themeId", publication.ThemeID,
			"sourceThemeId", publication.SourceThemeID,
		)
		err = s.pageRegistry.RegisterThemePackageReplacingHostApproved(
			ctx, target, publication.SourceThemeID,
		)
	default:
		err = s.pageRegistry.RegisterThemePackageReplacing(ctx, target, publication.SourceThemeID)
	}
	if err != nil {
		if componentPublished {
			if rollbackErr := s.componentRegistry.RollbackThemeTransition(
				source, &target, publication.Revision,
			); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("restore source component publication: %w", rollbackErr))
			}
		}
		if _, rollbackErr := s.rollbackThemeAssetTransition(
			ctx, assetAfter, source, &target,
		); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("restore source asset publication: %w", rollbackErr))
		}
		return errors.Join(ErrThemeRuntimeApplyFailed, err)
	}
	for _, item := range items {
		if item.Type == TypeTheme && item.ID != target.ID && item.ID != publication.SourceThemeID {
			s.pageRegistry.ClearExtension(item.ID)
		}
	}
	return nil
}

func (s *serviceCore) themePublicationSource(
	ctx context.Context,
	publication ThemeRuntimePublication,
) (*Extension, error) {
	if publication.SourceThemeID == "" {
		return nil, nil
	}
	expected := Extension{
		ID: publication.SourceThemeID, Type: TypeTheme,
		Version: publication.SourceThemeVersion, PackageDigest: publication.SourcePackageDigest,
	}
	base, err := s.store.Get(ctx, publication.SourceThemeID)
	if errors.Is(err, ErrExtensionNotFound) {
		// The inactive source package may already be uninstalled on a fresh
		// node. Its durable exact tuple remains sufficient to fence a stale
		// process-local publication; no source package code is loaded here.
		return &expected, nil
	}
	if err != nil {
		return nil, err
	}
	if sameThemeExactArtifact(base, expected) {
		value := base
		return &value, nil
	}
	versions, ok := s.store.(ExactExtensionVersionRepository)
	if !ok {
		return nil, ErrThemePublicationConflict
	}
	version, err := versions.GetExtensionVersion(ctx, ExactExtensionVersionInput{
		ExtensionID:   publication.SourceThemeID,
		Version:       publication.SourceThemeVersion,
		PackageDigest: publication.SourcePackageDigest,
	})
	if err != nil {
		return nil, err
	}
	exact := extensionFromExactVersion(base, version)
	if !sameThemeExactArtifact(exact, expected) {
		return nil, ErrThemePublicationConflict
	}
	return &exact, nil
}

// EnsureDefaultThemeActive 是首次安装、启动修复与恢复路径共用的默认主题激活入口。
// 受保护内置默认主题由 Host 批准核心页替换；其它主题仍走人工审批。
func (s *ThemeService) EnsureDefaultThemeActive(ctx context.Context) (Extension, error) {
	active, err := s.store.ActiveTheme(ctx)
	if err == nil && active.ID == DefaultThemeID && active.Source == SourceBuiltin {
		return active, nil
	}
	defaultTheme, getErr := s.store.Get(ctx, DefaultThemeID)
	if getErr != nil {
		if err != nil {
			return Extension{}, err
		}
		return Extension{}, getErr
	}
	if defaultTheme.Type != TypeTheme || defaultTheme.Source != SourceBuiltin {
		return Extension{}, ErrInvalidManifest
	}
	result, err := s.store.ActivateTheme(ctx, DefaultThemeID)
	if err != nil {
		return Extension{}, err
	}
	if result.Publication.CoreReplacementsApproved &&
		result.Publication.ApprovalSource == ThemeApprovalSourceHost {
		// 首次安装/启动修复没有人类 actor：Host 批准自己的受保护默认主题，
		// 用一条事件让运维在扩展日志里能看到这次自动批准。
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: DefaultThemeID,
			Action:      EventThemeActivated,
			Message:     "host-approved protected built-in default theme (first install or startup repair)",
		})
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
	input := ThemeActivationInput{
		Version:             staged.Version,
		PackageDigest:       staged.PackageDigest,
		CurrentThemeID:      current.ID,
		CurrentThemeVersion: current.Version,
		CurrentThemeDigest:  current.PackageDigest,
	}
	if isProtectedBuiltinDefaultTheme(current) {
		// 自愈不得降权：受保护内置默认主题的精确晋升必须继续带 Host 批准。
		// 漏掉这一步就会写下「已激活但未批准」的账本，apply 时走普通替换路径
		// 把 page_provider_bindings 清空，整站回落 Core（现网回归 rev 23）。
		input.HostApproveProtectedDefault = true
		input.Reason = ThemeRuntimePublicationStartupRepair
	}
	result, err := s.store.ActivateThemeExact(ctx, current.ID, input)
	if err != nil {
		return Extension{}, err
	}
	return result.Extension, nil
}

// defaultThemeApprovalRepair 只负责一件事：把「受保护内置默认主题的核心页替换
// 批准」从旧自愈缺陷留下的未批准账本修回来。
//
// 为什么不是 Service / ThemeService 的方法：这两个 receiver 的方法数已经被
// tests/architecture-boundaries-baseline.json 的 ratchet 钉在 69 / 5，新增方法
// 等于给 God object 加职责。启动自愈只有一个调用点（CatalogService.SyncBuiltins），
// 单独成协作者比再扩一层 facade 更清楚。
type defaultThemeApprovalRepair struct {
	core *serviceCore
}

// EnsureDefaultThemeApproved 是默认主题「审批账本」的启动自愈入口。
//
// 现场：API 曾从仓库根启动（cwd 错）→ 活动主题 restore 失败 → 自愈按 staged
// 精确晋升默认主题时没带 Host 批准 → 账本写下「已激活但未批准」→ apply 走普通
// 替换路径清空 page_provider_bindings → 主题仍 enabled 所以 EnsureDefaultThemeActive
// 早退，整站永久回落 Core。
//
// 判据只看 durable 账本：最新一版发布指向**当前精确制品**且
// core_replacements_approved=true 才算健康——健康时零写入。否则让 Host 重新激活
// 默认主题（该原语自带 host 批准与 startup_repair 原因），由 watcher 把核心页
// 绑定重新发布回来。
//
// Safe Mode 下不动作：安全模式只加载受保护默认主题，不允许改写发布账本。
func (r defaultThemeApprovalRepair) EnsureDefaultThemeApproved(ctx context.Context) error {
	core := r.core
	if core == nil || core.store == nil || ctx == nil {
		return nil
	}
	if core.safeMode {
		return nil
	}
	active, err := core.store.ActiveTheme(ctx)
	if err != nil || active.ID != DefaultThemeID || active.Source != SourceBuiltin {
		return nil
	}
	// 不给 60 方法的 Store 接口加方法：账本读取只用发布仓储的类型断言。
	// 存储实现没有这本账（测试替身/旧装配）时视为无可判定，不猜也不写。
	repository, ok := core.store.(ThemeRuntimePublicationRepository)
	if !ok {
		return nil
	}
	previousRevision := int64(0)
	latest, latestErr := repository.LatestThemeRuntimePublication(ctx)
	switch {
	case errors.Is(latestErr, ErrThemePublicationNotFound):
		// 账本为空：没有可继承的批准，交给下面的重新激活补一版。
	case latestErr != nil:
		return latestErr
	default:
		previousRevision = latest.Revision
		if defaultThemeApprovalHealthy(latest, active) {
			return nil
		}
	}
	result, err := core.store.ActivateTheme(ctx, DefaultThemeID)
	if err != nil {
		return err
	}
	if result.Publication.CoreReplacementsApproved &&
		result.Publication.ApprovalSource == ThemeApprovalSourceHost {
		_, _ = core.store.CreateEvent(ctx, EventInput{
			ExtensionID: DefaultThemeID,
			Action:      EventThemeActivated,
			Message: fmt.Sprintf(
				"re-approved core replacements after lost approval (previous revision %d, new revision %d)",
				previousRevision, result.Publication.Revision,
			),
		})
	}
	return nil
}

// defaultThemeApprovalHealthy 判定最新账本是否已经为当前精确制品记下核心页替换
// 批准。只比 exact tuple（id/version/digest）+ 批准位，不看进程内已发布状态：
// watcher 状态是易失的，账本才是权威。
func defaultThemeApprovalHealthy(latest ThemeRuntimePublication, active Extension) bool {
	return latest.DesiredState == ThemeRuntimePublicationActive &&
		latest.ThemeID == active.ID &&
		latest.ThemeVersion == active.Version &&
		strings.EqualFold(latest.PackageDigest, active.PackageDigest) &&
		latest.CoreReplacementsApproved
}
