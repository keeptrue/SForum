package providers

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// settingsSurfaceSourceExtension 是 Section.Source 的插件取值。
const settingsSurfaceSourceExtension = "extension"

// ExtensionSettingsSurfaceProvider 解析插件注册的设置面：
//
//   - ProfileSections —— forum.settings.profile.tabs（个人设置页）
//   - AdminSettingsTabs —— admin.settings.tabs（站点设置页）
//
// 两个方法共用一条裁决管线，顺序是刻意的：
//
//	插件启用态 + enabledBySetting 闸门（EffectiveContributions 已做）
//	→ 治理覆盖（tab/字段的 enabled、sort_order）
//	→ 权限过滤（服务端；权限键永不下发）
//	→ 排序（覆盖顺序优先，否则声明顺序）
//	→ 超过单表面上限时截断并 slog 告警
//
// 治理覆盖在权限之前：运营关掉的东西不因为某个访客恰好有权限而复活。
type ExtensionSettingsSurfaceProvider struct {
	source    EffectiveContributionSource
	overrides settingssurface.OverrideReader
}

func NewExtensionSettingsSurfaceProvider(source EffectiveContributionSource, overrides settingssurface.OverrideReader) ExtensionSettingsSurfaceProvider {
	return ExtensionSettingsSurfaceProvider{source: source, overrides: overrides}
}

// ProfileSections 返回该访客能看到的个人设置页 tab。
func (p ExtensionSettingsSurfaceProvider) ProfileSections(ctx context.Context, viewer identity.Actor) ([]settingssurface.Section, error) {
	return p.sections(ctx, extensionmanifest.PointForumSettingsProfileTabs, viewer)
}

// AdminSettingsTabs 返回站点设置页的插件 tab。
// 进不了管理后台的访客一律拿不到任何 tab：这里的空 permission 不等于公开，
// 站点设置页本身是 admin.access 之后的界面。
func (p ExtensionSettingsSurfaceProvider) AdminSettingsTabs(ctx context.Context, adminViewer identity.Actor) ([]settingssurface.Section, error) {
	if !adminViewer.IsActive() || !adminViewer.Can(identity.PermissionAdminAccess) {
		return nil, nil
	}
	return p.sections(ctx, extensionmanifest.PointAdminSettingsTabs, adminViewer)
}

// DeclaredFieldPolicy 是一条字段声明的**与 viewer 无关**的事实。
// 读裁决用它，渲染投影不用它（投影还要按 viewer 过滤）。
type DeclaredFieldPolicy struct {
	Permission    string
	TabPermission string
}

// DeclaredFields 返回全部设置字段声明（按组合键索引），不受 viewer 影响，
// 但**受站点治理影响**：被治理关闭的字段不出现（关闭即不可读写）。
func (p ExtensionSettingsSurfaceProvider) DeclaredFields(ctx context.Context) (map[string]DeclaredFieldPolicy, error) {
	if p.source == nil {
		return map[string]DeclaredFieldPolicy{}, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	index := map[settingssurface.OverrideKey]settingssurface.Override{}
	if p.overrides != nil {
		overrides, err := p.overrides.ListOverrides(ctx)
		if err != nil {
			return nil, err
		}
		index = settingssurface.OverrideIndex(overrides)
	}
	declared := map[string]DeclaredFieldPolicy{}
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointForumSettingsProfileTabs {
			continue
		}
		var payload extensionmanifest.SettingsSectionContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(payload.Type)) != extensionmanifest.SettingsSectionTabFields {
			continue
		}
		tabKey := settingssurface.OverrideKey{
			ExtensionID:    contribution.ExtensionID,
			Point:          contribution.Point,
			ContributionID: contribution.ID,
		}
		if item, ok := index[tabKey.Normalized()]; ok && !item.Enabled {
			continue
		}
		for _, field := range payload.Fields {
			fieldKey := tabKey
			fieldKey.ItemKey = strings.TrimSpace(field.Key)
			if item, ok := index[fieldKey.Normalized()]; ok && !item.Enabled {
				continue
			}
			declared[extensionmanifest.SettingsSurfaceFieldKey(contribution.ExtensionID, field.Key)] = DeclaredFieldPolicy{
				Permission:    strings.TrimSpace(field.Permission),
				TabPermission: strings.TrimSpace(payload.Permission),
			}
		}
	}
	return declared, nil
}

func (p ExtensionSettingsSurfaceProvider) sections(ctx context.Context, point string, viewer identity.Actor) ([]settingssurface.Section, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	index := map[settingssurface.OverrideKey]settingssurface.Override{}
	if p.overrides != nil {
		overrides, err := p.overrides.ListOverrides(ctx)
		if err != nil {
			return nil, err
		}
		index = settingssurface.OverrideIndex(overrides)
	}
	sections := make([]settingssurface.Section, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != point {
			continue
		}
		section, ok := settingsSectionFromContribution(contribution, index, viewer)
		if !ok {
			continue
		}
		sections = append(sections, section)
	}
	sort.SliceStable(sections, func(left, right int) bool {
		if sections[left].EffectiveSortOrder != sections[right].EffectiveSortOrder {
			return sections[left].EffectiveSortOrder < sections[right].EffectiveSortOrder
		}
		if sections[left].ExtensionID != sections[right].ExtensionID {
			return sections[left].ExtensionID < sections[right].ExtensionID
		}
		return sections[left].ID < sections[right].ID
	})
	if limit := extensionmanifest.SettingsSectionTabsPerPointMax; len(sections) > limit {
		dropped := len(sections) - limit
		sections = sections[:limit]
		slog.Warn("settings surface: plugin tabs truncated",
			"point", point, "limit", limit, "dropped", dropped)
	}
	return sections, nil
}

// settingsSectionFromContribution 把一条声明裁决成一个可下发的 tab。
// 返回 false 表示该 tab 对这个访客不存在（治理关闭、权限不足或声明不可用）；
// 这里一律 fail closed——读不懂的声明宁可不显示，也不下发半成品。
func settingsSectionFromContribution(
	contribution extensions.EffectiveContribution,
	index map[settingssurface.OverrideKey]settingssurface.Override,
	viewer identity.Actor,
) (settingssurface.Section, bool) {
	var payload extensionmanifest.SettingsSectionContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return settingssurface.Section{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(payload.Type))
	if kind == "" {
		return settingssurface.Section{}, false
	}
	if permission := strings.TrimSpace(payload.Permission); permission != "" && !viewer.Can(permission) {
		return settingssurface.Section{}, false
	}
	tabKey := settingssurface.OverrideKey{
		ExtensionID:    contribution.ExtensionID,
		Point:          contribution.Point,
		ContributionID: contribution.ID,
	}
	if item, ok := index[tabKey.Normalized()]; ok && !item.Enabled {
		return settingssurface.Section{}, false
	}
	icon := strings.TrimSpace(payload.Icon)
	if icon == "" {
		icon = contribution.Icon
	}
	section := settingssurface.Section{
		Source:             settingsSurfaceSourceExtension,
		ExtensionID:        contribution.ExtensionID,
		ExtensionName:      contribution.ExtensionName,
		ID:                 contribution.ID,
		EffectiveSortOrder: settingssurface.EffectiveOrder(tabKey, contribution.Order, index),
		Label:              copyContributionLabel(contribution.Label),
		Icon:               icon,
		Description:        payload.Description,
		Type:               kind,
	}
	switch kind {
	case extensionmanifest.SettingsSectionTabComponent:
		componentID := extensionmanifest.NormalizeID(payload.ComponentID)
		if componentID == "" {
			return settingssurface.Section{}, false
		}
		section.ComponentID = componentID
	case extensionmanifest.SettingsSectionTabSettings:
		tabID := extensionmanifest.NormalizeID(payload.TabID)
		if tabID == "" {
			return settingssurface.Section{}, false
		}
		section.TabID = tabID
	case extensionmanifest.SettingsSectionTabFields:
		fields := settingsSectionFields(contribution.ExtensionID, payload.Fields, tabKey, index, viewer)
		// 全被治理或权限过滤掉的表单 tab 没有可渲染内容，不当成空壳下发。
		if len(fields) == 0 {
			return settingssurface.Section{}, false
		}
		section.Fields = fields
	default:
		return settingssurface.Section{}, false
	}
	return section, true
}

func settingsSectionFields(
	extensionID string,
	declared []extensionmanifest.SettingsSectionField,
	tabKey settingssurface.OverrideKey,
	index map[settingssurface.OverrideKey]settingssurface.Override,
	viewer identity.Actor,
) []settingssurface.Field {
	fields := make([]settingssurface.Field, 0, len(declared))
	for _, field := range declared {
		// visibility=admin 的字段是「谁能在服务端读」，不是「用户自己填」；
		// 它不该出现在个人设置表单里，也不该进可写键集合。
		if extensionmanifest.NormalizeSettingsSectionVisibility(field.Visibility) ==
			extensionmanifest.SettingsSectionVisibilityAdmin {
			continue
		}
		if permission := strings.TrimSpace(field.Permission); permission != "" && !viewer.Can(permission) {
			continue
		}
		// 治理覆盖行按 manifest 裸键存（item_key 列），下发给前端的是组合键。
		fieldKey := tabKey
		fieldKey.ItemKey = strings.TrimSpace(field.Key)
		if item, ok := index[fieldKey.Normalized()]; ok && !item.Enabled {
			continue
		}
		fields = append(fields, settingssurface.Field{
			FieldKey:           extensionmanifest.SettingsSurfaceFieldKey(extensionID, field.Key),
			EffectiveSortOrder: settingssurface.EffectiveOrder(fieldKey, field.Order, index),
			Label:              field.Label,
			Description:        field.Description,
			Type:               strings.ToLower(strings.TrimSpace(field.Type)),
			Default:            strings.TrimSpace(field.Default),
			Placeholder:        field.Placeholder,
			Required:           field.Required,
			Visibility:         extensionmanifest.NormalizeSettingsSectionVisibility(field.Visibility),
			Constraints:        field.Constraints,
			Options:            settingsSectionOptions(field.Options),
		})
	}
	sort.SliceStable(fields, func(left, right int) bool {
		if fields[left].EffectiveSortOrder != fields[right].EffectiveSortOrder {
			return fields[left].EffectiveSortOrder < fields[right].EffectiveSortOrder
		}
		return fields[left].FieldKey < fields[right].FieldKey
	})
	return fields
}

func settingsSectionOptions(declared []extensionmanifest.SettingsSectionOption) []settingssurface.Option {
	if len(declared) == 0 {
		return nil
	}
	options := make([]settingssurface.Option, 0, len(declared))
	for _, option := range declared {
		options = append(options, settingssurface.Option{
			Value: strings.TrimSpace(option.Value),
			Label: option.Label,
		})
	}
	return options
}
