package providers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ExtensionSettingsReader 读取单个插件解析后的设置文档（surface-tabs 内联用）。
// 由 extensions.Service 实现。
type ExtensionSettingsReader interface {
	Settings(ctx context.Context, actor identity.Actor, extensionID string, locale string) (extensions.ExtensionSettings, error)
}

// ExtensionSettingsSurfaceGovernanceProvider 是设置面治理的唯一策略层：
// 谁能看、谁能改、改的是哪一行、恢复默认是什么意思。
//
// 控制器只做解码/编码；这里负责权限、声明校验、覆盖行的合并不变量：
//   - 只允许改动**当前生效**的声明（插件被停用或升级后删掉 tab，旧覆盖行就成了
//     悬空数据，写入必须先撞上 ErrUnknownContribution，而不是凭空造一行）
//   - PUT 是部分更新：没传的列保持不变；sortOrder=null 才清掉顺序覆盖
//   - DELETE 是「恢复 manifest 默认」，重复删除也返回成功（幂等）
type ExtensionSettingsSurfaceGovernanceProvider struct {
	source    EffectiveContributionSource
	overrides settingssurface.OverrideStore
	surfaces  ExtensionSettingsSurfaceProvider
	settings  ExtensionSettingsReader
}

func NewExtensionSettingsSurfaceGovernanceProvider(
	source EffectiveContributionSource,
	overrides settingssurface.OverrideStore,
	surfaces ExtensionSettingsSurfaceProvider,
	settings ExtensionSettingsReader,
) ExtensionSettingsSurfaceGovernanceProvider {
	return ExtensionSettingsSurfaceGovernanceProvider{
		source:    source,
		overrides: overrides,
		surfaces:  surfaces,
		settings:  settings,
	}
}

// 读治理视图需要 extension.view；父权限 extension.manage 与 super_admin 也放行
// （与 CatalogService 的 canViewExtensions 同一口径）。
func canViewExtensionSurfaces(actor identity.Actor) bool {
	if !actor.IsActive() {
		return false
	}
	return actor.Can(identity.PermissionExtensionView) ||
		actor.Can(identity.PermissionExtensionManage) ||
		actor.IsSuperAdmin()
}

func requireExtensionSurfaceManage(actor identity.Actor) error {
	if !actor.IsActive() || !actor.Can(identity.PermissionExtensionPluginManage) {
		return identity.ErrPermissionDenied
	}
	return nil
}

// Contributions 返回贡献读模型 + 治理事实。
func (p ExtensionSettingsSurfaceGovernanceProvider) Contributions(ctx context.Context, actor identity.Actor) ([]settingssurface.ContributionView, error) {
	if !canViewExtensionSurfaces(actor) {
		return nil, identity.ErrPermissionDenied
	}
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	index, err := p.overrideIndex(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]settingssurface.ContributionView, 0, len(contributions))
	for _, contribution := range contributions {
		tabKey := settingssurface.OverrideKey{
			ExtensionID:    contribution.ExtensionID,
			Point:          contribution.Point,
			ContributionID: contribution.ID,
		}
		normalized := tabKey.Normalized()
		override, overridden := index[normalized]
		view := settingssurface.ContributionView{
			ExtensionID:        contribution.ExtensionID,
			ExtensionName:      contribution.ExtensionName,
			ExtensionType:      contribution.ExtensionType,
			Point:              contribution.Point,
			ID:                 contribution.ID,
			Order:              contribution.Order,
			Enabled:            !overridden || override.Enabled,
			Overridden:         overridden,
			EffectiveSortOrder: settingssurface.EffectiveOrder(tabKey, contribution.Order, index),
			Label:              copyContributionLabel(contribution.Label),
			Icon:               contribution.Icon,
			Payload:            contribution.Payload,
			Fields:             contributionItemViews(contribution, tabKey, index),
		}
		views = append(views, view)
	}
	markSuppressedByCap(views)
	return views, nil
}

// markSuppressedByCap 标出渲染侧会被单表面上限截断的贡献。
//
// 治理读端点的职责是「让运营看到全量声明」，所以这里不截断、只标记；真正的截断
// 在渲染侧（ExtensionSettingsSurfaceProvider.sections，上限
// SettingsSectionTabsPerPointMax）执行。位次按生效顺序计算（覆盖优先），
// 与渲染侧同一把尺子；权限/字段过滤仍可能让实际顺序与此略有出入，因此它是提示位。
func markSuppressedByCap(views []settingssurface.ContributionView) {
	byPoint := map[string][]int{}
	for index, view := range views {
		views[index].SuppressedByCap = false
		byPoint[view.Point] = append(byPoint[view.Point], index)
	}
	for _, indexes := range byPoint {
		sort.SliceStable(indexes, func(left, right int) bool {
			a, b := views[indexes[left]], views[indexes[right]]
			if a.EffectiveSortOrder != b.EffectiveSortOrder {
				return a.EffectiveSortOrder < b.EffectiveSortOrder
			}
			if a.ExtensionID != b.ExtensionID {
				return a.ExtensionID < b.ExtensionID
			}
			return a.ID < b.ID
		})
		for rank, index := range indexes {
			views[index].SuppressedByCap = rank >= extensionmanifest.SettingsSectionTabsPerPointMax
		}
	}
}

// SurfaceTabs 返回站点设置页上的插件 tab，type=settings 的 tab 内联解析后的设置文档。
//
// 守卫口径（与 v3 路由目录声明的 reviewed 策略逐字一致）：先 admin.access，
// 再 extension.view；tab 级 permission 之后在 provider 里过滤。两个权限都要求，
// 是因为这条路由会透出插件的完整设置文档（含非 secret 项），只给「进得了后台」
// 不够。
func (p ExtensionSettingsSurfaceGovernanceProvider) SurfaceTabs(ctx context.Context, actor identity.Actor, locale string) ([]settingssurface.SurfaceTab, error) {
	if !actor.IsActive() || !actor.Can(identity.PermissionAdminAccess) || !actor.Can(identity.PermissionExtensionView) {
		return nil, identity.ErrPermissionDenied
	}
	sections, err := p.surfaces.AdminSettingsTabs(ctx, actor)
	if err != nil {
		return nil, err
	}
	tabs := make([]settingssurface.SurfaceTab, 0, len(sections))
	for _, section := range sections {
		tab := settingssurface.SurfaceTab{
			ID:             settingssurface.SurfaceTabID(section.ExtensionID, section.ID),
			Point:          extensionmanifest.PointAdminSettingsTabs,
			ContributionID: section.ID,
			Label:          section.Label,
			Icon:           section.Icon,
			Type:           section.Type,
			ExtensionID:    section.ExtensionID,
			TabID:          section.TabID,
		}
		if section.Type == extensionmanifest.SettingsSectionTabSettings && p.settings != nil {
			document, err := p.settings.Settings(ctx, actor, section.ExtensionID, locale)
			if err != nil {
				// 单个插件的设置读不出来不该让整页打不开；跳过并留下可查日志。
				slog.Warn("settings surface: inline settings document unavailable",
					"extensionId", section.ExtensionID, "contributionId", section.ID, "error", err)
				continue
			}
			tab.Settings = &document
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

// Mutate 应用一次治理写入。key 必须是当前生效的声明（tab 或字段）。
func (p ExtensionSettingsSurfaceGovernanceProvider) Mutate(ctx context.Context, actor identity.Actor, input settingssurface.MutateInput) (settingssurface.OverrideView, error) {
	if err := requireExtensionSurfaceManage(actor); err != nil {
		return settingssurface.OverrideView{}, err
	}
	if p.overrides == nil {
		return settingssurface.OverrideView{}, settingssurface.ErrInvalid
	}
	key, declaredOrder, err := p.resolveTarget(ctx, input.Key.Normalized())
	if err != nil {
		return settingssurface.OverrideView{}, err
	}
	input.Key = key
	index, err := p.overrideIndex(ctx)
	if err != nil {
		return settingssurface.OverrideView{}, err
	}
	existing, exists := index[key]
	merged := settingssurface.OverrideInput{
		Key: key,
		// 未传 enabled 时保留既有值；无既有行时默认可见（manifest 默认）。
		Enabled:   !exists || existing.Enabled,
		SortOrder: nil,
	}
	if exists {
		merged.SortOrder = existing.SortOrder
	}
	if input.Enabled != nil {
		merged.Enabled = *input.Enabled
	}
	if input.SortOrderSet {
		merged.SortOrder = input.SortOrder
	}
	actorID := actor.ID
	if actorID > 0 {
		merged.UpdatedByUserID = &actorID
	}
	saved, err := p.overrides.UpsertOverride(ctx, merged)
	if err != nil {
		return settingssurface.OverrideView{}, err
	}
	return overrideView(saved, declaredOrder), nil
}

// Reset 删除覆盖行 = 恢复 manifest 默认。重复删除同样是成功（本来就没有覆盖）。
func (p ExtensionSettingsSurfaceGovernanceProvider) Reset(ctx context.Context, actor identity.Actor, key settingssurface.OverrideKey) error {
	if err := requireExtensionSurfaceManage(actor); err != nil {
		return err
	}
	if p.overrides == nil {
		return settingssurface.ErrInvalid
	}
	key, err := p.resolveResetTarget(ctx, key.Normalized())
	if err != nil {
		return err
	}
	if key.ExtensionID == "" {
		// 声明与覆盖行都不存在：本来就没有覆盖，恢复默认已完成。
		return nil
	}
	err = p.overrides.DeleteOverride(ctx, key)
	if err != nil && !errors.Is(err, settingssurface.ErrNotFound) {
		return err
	}
	return nil
}

func (p ExtensionSettingsSurfaceGovernanceProvider) overrideIndex(ctx context.Context) (map[settingssurface.OverrideKey]settingssurface.Override, error) {
	if p.overrides == nil {
		return map[settingssurface.OverrideKey]settingssurface.Override{}, nil
	}
	overrides, err := p.overrides.ListOverrides(ctx)
	if err != nil {
		return nil, err
	}
	return settingssurface.OverrideIndex(overrides), nil
}

// resolveTarget 把治理目标钉到唯一一条生效声明上，并返回它的声明顺序。
//
// extensionId 允许缺省（路径里没有它）：contributionId 在生效声明里唯一时直接解析；
// 同一 contributionId 出现在多个扩展上时必须显式给 extensionId，否则 ErrInvalid——
// 猜一个扩展去写覆盖行比报错危险得多。
func (p ExtensionSettingsSurfaceGovernanceProvider) resolveTarget(ctx context.Context, key settingssurface.OverrideKey) (settingssurface.OverrideKey, int, error) {
	if p.source == nil {
		return key, 0, settingssurface.ErrUnknownContribution
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return key, 0, err
	}
	matches := make([]extensions.EffectiveContribution, 0, 1)
	for _, contribution := range contributions {
		if contribution.Point != key.Point ||
			extensionmanifest.NormalizeID(contribution.ID) != key.ContributionID {
			continue
		}
		if key.ExtensionID != "" && extensionmanifest.NormalizeID(contribution.ExtensionID) != key.ExtensionID {
			continue
		}
		matches = append(matches, contribution)
	}
	if len(matches) == 0 {
		return key, 0, settingssurface.ErrUnknownContribution
	}
	if len(matches) > 1 {
		return key, 0, settingssurface.ErrInvalid
	}
	contribution := matches[0]
	key.ExtensionID = extensionmanifest.NormalizeID(contribution.ExtensionID)
	if key.ItemKey == "" {
		return key, contribution.Order, nil
	}
	for _, item := range settingsSectionDeclaredItems(contribution.Payload) {
		if item.Key == key.ItemKey {
			return key, item.Order, nil
		}
	}
	return key, 0, settingssurface.ErrUnknownItem
}

// resolveResetTarget 让 DELETE 也能清掉悬空覆盖行（声明已被升级删掉）。
// 声明还在时与 resolveTarget 完全一致；声明没了就按覆盖行反查扩展 id。
func (p ExtensionSettingsSurfaceGovernanceProvider) resolveResetTarget(ctx context.Context, key settingssurface.OverrideKey) (settingssurface.OverrideKey, error) {
	resolved, _, err := p.resolveTarget(ctx, key)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, settingssurface.ErrUnknownContribution) {
		return key, err
	}
	if key.ExtensionID != "" || p.overrides == nil {
		return key, nil
	}
	index, err := p.overrideIndex(ctx)
	if err != nil {
		return key, err
	}
	matches := make([]settingssurface.OverrideKey, 0, 1)
	for _, override := range index {
		if override.Key.Point != key.Point || override.Key.ContributionID != key.ContributionID {
			continue
		}
		if key.ItemKey != "" && override.Key.ItemKey != key.ItemKey {
			continue
		}
		matches = append(matches, override.Key)
	}
	switch len(matches) {
	case 0:
		return key, nil
	case 1:
		return matches[0], nil
	default:
		return key, settingssurface.ErrInvalid
	}
}

type declaredSurfaceItem struct {
	Key   string
	Order int
}

// settingsSectionDeclaredItems 取出一条贡献里声明的字段（裸键 + 声明顺序）。
// 非 settingsSection / 非 fields 形态返回空：它们没有可治理的字段。
func settingsSectionDeclaredItems(payload json.RawMessage) []declaredSurfaceItem {
	var descriptor extensionmanifest.SettingsSectionContributionPayload
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(descriptor.Type)) != extensionmanifest.SettingsSectionTabFields {
		return nil
	}
	items := make([]declaredSurfaceItem, 0, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		items = append(items, declaredSurfaceItem{
			Key:   strings.TrimSpace(field.Key),
			Order: field.Order,
		})
	}
	return items
}

func contributionItemViews(
	contribution extensions.EffectiveContribution,
	tabKey settingssurface.OverrideKey,
	index map[settingssurface.OverrideKey]settingssurface.Override,
) []settingssurface.ContributionItemView {
	items := settingsSectionDeclaredItems(contribution.Payload)
	views := make([]settingssurface.ContributionItemView, 0, len(items))
	for _, item := range items {
		itemKey := tabKey
		itemKey.ItemKey = item.Key
		override, overridden := index[itemKey.Normalized()]
		views = append(views, settingssurface.ContributionItemView{
			// 默认下发的字段键就是组合键（前端把它当值键与错误键用），
			// 治理表的 item_key 仍存裸键；换算只走 SettingsSurfaceFieldKey 家族。
			FieldKey:           extensionmanifest.SettingsSurfaceFieldKey(contribution.ExtensionID, item.Key),
			Enabled:            !overridden || override.Enabled,
			EffectiveSortOrder: settingssurface.EffectiveOrder(itemKey, item.Order, index),
			Overridden:         overridden,
		})
	}
	return views
}

func overrideView(override settingssurface.Override, declaredOrder int) settingssurface.OverrideView {
	effective := declaredOrder
	if override.SortOrder != nil {
		effective = *override.SortOrder
	}
	return settingssurface.OverrideView{
		ExtensionID:        override.Key.ExtensionID,
		Point:              override.Key.Point,
		ContributionID:     override.Key.ContributionID,
		ItemKey:            override.Key.ItemKey,
		Enabled:            override.Enabled,
		DeclaredSortOrder:  declaredOrder,
		EffectiveSortOrder: effective,
		Overridden:         true,
	}
}
