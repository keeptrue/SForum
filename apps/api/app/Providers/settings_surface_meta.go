package providers

import (
	"context"
	"errors"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// SettingsMetaDirectory 是插件字段值读写需要的 entity-meta 能力。
// 只取两个方法：插件字段就是用户元数据，读写都走与运营同一套校验
// （visibility、constraints、enum、required），不在 Provider 里复制一遍。
type SettingsMetaDirectory interface {
	ListValues(ctx context.Context, actor identity.Actor, entityType string, entityID int64) ([]entitymeta.MetaValue, error)
	UpsertValues(ctx context.Context, actor identity.Actor, entityType string, entityID int64, inputs []entitymeta.UpsertValueInput) ([]entitymeta.MetaValue, error)
}

// SettingsSurfaceMetaStore 是 Profile 与 entity-meta 之间的适配器。
//
// 三个入口共用同一个过滤函数 settingsMetaEntries：
//   - 自助面（settingsValues）：public + owner，且只保留当前 tab 声明过的键
//   - 公开面（profile.meta）：只 public，viewer 权限由 entity-meta 的读取策略裁决
//   - 写入：先按同一份字段目录校验键，再逐字段交给 entity-meta
//
// 「哪些键合法」的唯一来源是 ExtensionSettingsSurfaceProvider 的字段投影
// （已含权限、站点治理、manifest 校验），这里绝不重新解析 manifest。
type SettingsSurfaceMetaStore struct {
	surfaces ExtensionSettingsSurfaceProvider
	meta     SettingsMetaDirectory
}

func NewSettingsSurfaceMetaStore(surfaces ExtensionSettingsSurfaceProvider, meta SettingsMetaDirectory) SettingsSurfaceMetaStore {
	return SettingsSurfaceMetaStore{surfaces: surfaces, meta: meta}
}

// MySettingsValues 返回当前用户可编辑字段的值（键是组合 fieldKey）。
func (s SettingsSurfaceMetaStore) MySettingsValues(ctx context.Context, actor identity.Actor) (map[string]any, error) {
	if !actor.IsActive() {
		return nil, identity.ErrPermissionDenied
	}
	sections, err := s.surfaces.ProfileSections(ctx, actor)
	if err != nil {
		return nil, err
	}
	allowed := settingsSectionFieldKeys(sections)
	if len(allowed) == 0 {
		return nil, nil
	}
	entries, err := s.settingsMetaEntries(ctx, actor, actor.ID, allowed, selfSettingsVisibility)
	if err != nil {
		return nil, err
	}
	values := make(map[string]any, len(entries))
	for key, entry := range entries {
		values[key] = entry.Value
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

// PublicSettingsMeta 返回某用户对 viewer 可见的公开字段值（键是组合 fieldKey）。
func (s SettingsSurfaceMetaStore) PublicSettingsMeta(ctx context.Context, viewer identity.Actor, userID int64) (map[string]profile.SettingsMetaValue, error) {
	if userID <= 0 {
		return nil, entitymeta.ErrInvalid
	}
	// 公开面不限定「必须是插件字段」：运营手建且声明为 public 的用户字段同样属于
	// 公开资料页。allowedKeys=nil 表示不按 tab 过滤，visibility 由下面这条策略裁决。
	return s.settingsMetaEntries(ctx, viewer, userID, nil, publicSettingsVisibility)
}

// WriteSettingsValues 校验并写入插件字段。
//
// 逐字段写入是刻意的：entity-meta 的错误不带字段名，一次批量写只能得到「有一项
// 不合法」。逐字段写能把每个错误精确贴到 fieldKey 上；代价是同一请求内可能部分
// 成功——前端保留 dirty 并重发全部字段即可收敛，而「静默丢弃用户的修改」不可收敛。
func (s SettingsSurfaceMetaStore) WriteSettingsValues(ctx context.Context, actor identity.Actor, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	if !actor.IsActive() {
		return identity.ErrPermissionDenied
	}
	sections, err := s.surfaces.ProfileSections(ctx, actor)
	if err != nil {
		return err
	}
	allowed := settingsSectionFieldKeys(sections)
	fieldErrors := make(map[string][]string, len(values))
	for key, value := range values {
		if _, ok := allowed[key]; !ok {
			// 未声明 / 已被站点停用 / 无写权限：一律拒绝，绝不静默丢弃。
			fieldErrors[key] = []string{profile.MessageSettingsFieldUnknown}
			continue
		}
		_, err := s.meta.UpsertValues(ctx, actor, entitymeta.EntityUser, actor.ID, []entitymeta.UpsertValueInput{
			{FieldKey: key, Value: value},
		})
		if err == nil {
			continue
		}
		message, ok := settingsMetaMessageKey(err, value)
		if !ok {
			return err
		}
		fieldErrors[key] = []string{message}
	}
	if len(fieldErrors) > 0 {
		return &profile.SettingsFieldError{Fields: fieldErrors}
	}
	return nil
}

// settingsMetaEntries 是公开面与自助面唯一的过滤实现。
//
// allowedKeys 为 nil 表示不按字段目录过滤；allowVisibility 决定这一面允许哪些
// visibility。两层过滤叠加在 entity-meta 自身的读取策略之上：owner/admin 字段
// 无论哪条入口漏判，都不可能出现在公开响应里。
func (s SettingsSurfaceMetaStore) settingsMetaEntries(
	ctx context.Context,
	viewer identity.Actor,
	userID int64,
	allowedKeys map[string]struct{},
	allowVisibility func(visibility string) bool,
) (map[string]profile.SettingsMetaValue, error) {
	values, err := s.meta.ListValues(ctx, viewer, entitymeta.EntityUser, userID)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]profile.SettingsMetaValue, len(values))
	for _, value := range values {
		if !allowVisibility(value.Visibility) {
			continue
		}
		if allowedKeys != nil {
			if _, ok := allowedKeys[value.FieldKey]; !ok {
				continue
			}
		}
		entries[value.FieldKey] = profile.SettingsMetaValue{Label: value.Label, Value: value.Value}
	}
	return entries, nil
}

// selfSettingsVisibility：自己的设置面能看到 public 与 owner。
// admin 可见性字段是「运营读它」，不是「用户编辑它」，因此在这里就挡掉。
func selfSettingsVisibility(visibility string) bool {
	return visibility == entitymeta.VisibilityPublic || visibility == entitymeta.VisibilityOwner
}

// publicSettingsVisibility：公开资料页只放 public。
func publicSettingsVisibility(visibility string) bool {
	return visibility == entitymeta.VisibilityPublic
}

func settingsSectionFieldKeys(sections []settingssurface.Section) map[string]struct{} {
	keys := make(map[string]struct{}, 16)
	for _, section := range sections {
		for _, field := range section.Fields {
			keys[field.FieldKey] = struct{}{}
		}
	}
	return keys
}

// settingsMetaMessageKey 把 entity-meta 的拒绝翻译成字段级消息键。
// 返回 false 表示不是「字段值问题」而是基础设施错误，必须原样上抛。
func settingsMetaMessageKey(err error, value any) (string, bool) {
	switch {
	case errors.Is(err, entitymeta.ErrFieldDisabled), errors.Is(err, identity.ErrPermissionDenied):
		return profile.MessageSettingsFieldForbidden, true
	case errors.Is(err, entitymeta.ErrInvalid):
		// 清空必填字段也走 ErrInvalid，但用户该看到的提示不同。
		if value == nil {
			return profile.MessageSettingsFieldRequired, true
		}
		return profile.MessageSettingsFieldInvalid, true
	case errors.Is(err, entitymeta.ErrNotFound):
		// 键合法但定义不存在 = 投影没落库（能看不能存），与「未声明/停用/无权」分开。
		return profile.MessageSettingsFieldUndefined, true
	case errors.Is(err, entitymeta.ErrEntityNotFound):
		return profile.MessageSettingsFieldUnknown, true
	default:
		return "", false
	}
}
