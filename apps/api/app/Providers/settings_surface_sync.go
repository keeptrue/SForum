package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// SettingsSurfaceFieldDirectory 是字段同步需要的 entity-meta 能力。
// 只取两个方法：同步是宿主内部动作，不走需要 actor 的运营 CRUD 路径。
type SettingsSurfaceFieldDirectory interface {
	EnsureOwnerExtensionField(ctx context.Context, input entitymeta.CreateFieldInput) (entitymeta.FieldDefinition, error)
	SetEnabledByOwnerExtension(ctx context.Context, extensionID string, enabled bool) error
	// ListOwnerExtensionDefinitions 供差集清扫找出「该 owner 下已不再声明」的旧字段。
	ListOwnerExtensionDefinitions(ctx context.Context, extensionID string) ([]entitymeta.FieldDefinition, error)
	// SetEnabledByOwnerField 停用单个字段（差集清扫用；插件整体停用仍走 SetEnabledByOwnerExtension）。
	SetEnabledByOwnerField(ctx context.Context, fieldKey string, enabled bool) error
}

// SettingsSurfaceFieldSync 把插件声明的个人设置字段投影进 entity-meta。
//
// 为什么落在 entity-meta 而不是插件自己的设置表：这些值属于**用户**，不是
// 站点配置。落在 entity-meta 就自动获得「按 visibility 过滤、值随用户走、
// 插件卸载不丢」三件事。
type SettingsSurfaceFieldSync struct {
	directory SettingsSurfaceFieldDirectory
	// overrides 用于清理指向已删除声明/字段的悬空治理覆盖行（可为 nil）。
	overrides settingssurface.OverrideStore
}

func NewSettingsSurfaceFieldSync(directory SettingsSurfaceFieldDirectory, overrides ...settingssurface.OverrideStore) SettingsSurfaceFieldSync {
	sync := SettingsSurfaceFieldSync{directory: directory}
	if len(overrides) > 0 {
		sync.overrides = overrides[0]
	}
	return sync
}

// SyncManifest 在插件 enable 时调用：把 forum.settings.profile.tabs 里声明的
// 字段全部 upsert 成 entity-meta 字段（entityType=user）。
//
// 幂等：重复 enable 只会更新声明（label/顺序/约束），不会新增重复行，
// 也不会清空已存的用户值。返回同步的字段数。
//
// 站点设置页（admin.settings.tabs）的字段不在这里同步：站点级配置走插件自己的
// SettingsDocument 与 settings 存储，不混进 per-user 元数据。
func (s SettingsSurfaceFieldSync) SyncManifest(ctx context.Context, manifest extensionmanifest.Manifest) (int, error) {
	if s.directory == nil {
		return 0, nil
	}
	extensionID := extensionmanifest.NormalizeID(manifest.ID)
	if extensionID == "" {
		return 0, fmt.Errorf("%w: settings surface sync requires an extension id", entitymeta.ErrInvalid)
	}
	declared := map[string]struct{}{}
	synced := 0
	for _, contribution := range manifest.Contributions {
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
		for _, field := range payload.Fields {
			input, err := settingsSurfaceFieldInput(field, extensionID)
			if err != nil {
				// 声明本身不合法就不写：半条约束落库比整条不落更危险
				// （看起来有上限，实际什么都不校验）。
				return synced, fmt.Errorf("sync settings field %q of %s: %w", field.Key, extensionID, err)
			}
			if _, err := s.directory.EnsureOwnerExtensionField(ctx, input); err != nil {
				return synced, fmt.Errorf("sync settings field %q of %s: %w", field.Key, extensionID, err)
			}
			declared[input.FieldKey] = struct{}{}
			synced++
		}
	}
	// 差集清扫：本次声明里不存在的旧字段必须停用。
	//
	// 不做的后果不是「多一列没人用」：旧定义仍然 enabled=true，只要它的
	// visibility=public，历史值就会继续在 GET /entity-meta/{user}/{id} 匿名可读，
	// 而且仍可写——插件改名/删字段于是变成一次静默的暴露面扩大。
	if err := s.disableUndeclaredFields(ctx, extensionID, declared); err != nil {
		return synced, err
	}
	// 悬空治理覆盖行：指向已删除声明/字段的覆盖行在这里一并清掉，
	// 否则迁移注释承诺的「下次治理保存时被清掉」永远不会发生。
	s.pruneDanglingOverrides(ctx, manifest, extensionID, declared)
	return synced, nil
}

// disableUndeclaredFields 把该 owner 下未在本次声明出现的定义置 enabled=false。
// 只翻 enabled：已存值不删，回到旧声明时能原样恢复。
func (s SettingsSurfaceFieldSync) disableUndeclaredFields(ctx context.Context, extensionID string, declared map[string]struct{}) error {
	definitions, err := s.directory.ListOwnerExtensionDefinitions(ctx, extensionID)
	if err != nil {
		return fmt.Errorf("list settings surface definitions: %w", err)
	}
	stale := 0
	for _, definition := range definitions {
		if !definition.Enabled {
			continue
		}
		if _, ok := declared[definition.FieldKey]; ok {
			continue
		}
		if err := s.directory.SetEnabledByOwnerField(ctx, definition.FieldKey, false); err != nil {
			return fmt.Errorf("disable undeclared settings field %q: %w", definition.FieldKey, err)
		}
		stale++
	}
	if stale > 0 {
		slog.Info("settings surface: disabled undeclared fields",
			"extensionId", extensionID, "fields", stale)
	}
	return nil
}

// pruneDanglingOverrides 删除该扩展在治理表里指向「已不存在的声明或字段」的行。
// 失败只告警：治理覆盖清不掉不影响功能，但会让运营看到幽灵行。
func (s SettingsSurfaceFieldSync) pruneDanglingOverrides(
	ctx context.Context,
	manifest extensionmanifest.Manifest,
	extensionID string,
	declaredFields map[string]struct{},
) {
	if s.overrides == nil {
		return
	}
	declaredTabs := make(map[settingssurface.OverrideKey]struct{}, len(manifest.Contributions))
	for _, contribution := range manifest.Contributions {
		key := settingssurface.OverrideKey{
			ExtensionID:    extensionID,
			Point:          contribution.Point,
			ContributionID: extensionmanifest.NormalizeID(contribution.ID),
		}
		declaredTabs[key] = struct{}{}
	}
	overrides, err := s.overrides.ListOverrides(ctx)
	if err != nil {
		slog.Warn("settings surface: list overrides for pruning failed", "extensionId", extensionID, "error", err)
		return
	}
	removed := 0
	for _, override := range overrides {
		key := override.Key.Normalized()
		if key.ExtensionID != extensionID {
			continue
		}
		tabKey := key
		tabKey.ItemKey = ""
		if _, ok := declaredTabs[tabKey]; !ok {
			if err := s.overrides.DeleteOverride(ctx, key); err == nil {
				removed++
			}
			continue
		}
		if key.ItemKey == "" {
			continue
		}
		// 字段级覆盖：裸键必须仍在本次声明里（组合键换算回裸键再比）。
		bare := extensionmanifest.SettingsSurfaceBareFieldKey(extensionID, key.ItemKey)
		fieldKey := extensionmanifest.SettingsSurfaceFieldKey(extensionID, bare)
		if _, ok := declaredFields[fieldKey]; !ok {
			if err := s.overrides.DeleteOverride(ctx, key); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		slog.Info("settings surface: pruned dangling governance overrides",
			"extensionId", extensionID, "rows", removed)
	}
}

// DeprojectStale 对「不在 enabled 状态但仍有 enabled=true 定义」的扩展做去投影。
//
// 为什么启动期必须双向：Disable 的去投影失败只告警（停用不能因为字段存储故障
// 而失败），带外恢复命令（sforum extension disable）完全不经过 store。若启动
// reconcile 只补 enabled 插件，这些残留定义会永久 enabled —— public 可见性下
// 历史值继续匿名可读、仍可写。返回实际被自愈的扩展数。
func (s SettingsSurfaceFieldSync) DeprojectStale(ctx context.Context, extensionIDs []string) (int, error) {
	if s.directory == nil {
		return 0, nil
	}
	healed := 0
	for _, extensionID := range extensionIDs {
		extensionID = extensionmanifest.NormalizeID(extensionID)
		if extensionID == "" {
			continue
		}
		definitions, err := s.directory.ListOwnerExtensionDefinitions(ctx, extensionID)
		if err != nil {
			return healed, fmt.Errorf("list stale settings fields of %s: %w", extensionID, err)
		}
		active := 0
		for _, definition := range definitions {
			if definition.Enabled {
				active++
			}
		}
		if active == 0 {
			continue
		}
		if err := s.directory.SetEnabledByOwnerExtension(ctx, extensionID, false); err != nil {
			return healed, fmt.Errorf("deproject stale settings fields of %s: %w", extensionID, err)
		}
		slog.Warn("settings surface: disabled extension still had enabled field definitions; healed at startup",
			"extensionId", extensionID, "fields", active)
		healed++
	}
	return healed, nil
}

// Disable 在插件 disable/uninstall 时调用：把该扩展拥有的字段整体置 enabled=false。
// 绝不删除字段、更不删除已存值——重装回来时运营与用户的既有数据必须原样还在。
func (s SettingsSurfaceFieldSync) Disable(ctx context.Context, extensionID string) error {
	if s.directory == nil {
		return nil
	}
	extensionID = extensionmanifest.NormalizeID(extensionID)
	if extensionID == "" {
		return entitymeta.ErrInvalid
	}
	return s.directory.SetEnabledByOwnerExtension(ctx, extensionID, false)
}

// settingsSurfaceFieldInput 把一条 manifest 声明翻译成 entity-meta 字段定义。
//
// 约束在这里**严格**解析（extensionmanifest.ParseSettingsSectionConstraints）：
// 插件声明是本功能新开的入口，解析失败必须让 sync 失败，而不是落一条没有约束的
// 定义。运营手建的既有字段走 entity-meta 自己的历史宽松路径，不受影响。
func settingsSurfaceFieldInput(field extensionmanifest.SettingsSectionField, extensionID string) (entitymeta.CreateFieldInput, error) {
	constraints, err := settingsSurfaceFieldConstraints(field)
	if err != nil {
		return entitymeta.CreateFieldInput{}, err
	}
	enabled := true
	sortOrder := field.Order
	return entitymeta.CreateFieldInput{
		FieldKey:         extensionmanifest.SettingsSurfaceFieldKey(extensionID, field.Key),
		EntityType:       entitymeta.EntityUser,
		ValueType:        settingsSurfaceValueType(field.Type),
		Visibility:       extensionmanifest.NormalizeSettingsSectionVisibility(field.Visibility),
		LabelZHCN:        field.Label.Resolve("zh-CN"),
		LabelENUS:        field.Label.Resolve("en-US"),
		DescriptionZHCN:  field.Description.Resolve("zh-CN"),
		DescriptionENUS:  field.Description.Resolve("en-US"),
		OwnerExtensionID: extensionID,
		Required:         field.Required,
		Enabled:          &enabled,
		SortOrder:        &sortOrder,
		Constraints:      constraints,
	}, nil
}

// settingsSurfaceValueType 把声明类型映射到 entity-meta 的存储类型。
// select 落到 string：可选项由 constraints.enum 约束（宿主侧的存储层校验），
// 否则 select 就只是前端的下拉框装饰。
func settingsSurfaceValueType(fieldType string) string {
	switch strings.ToLower(strings.TrimSpace(fieldType)) {
	case extensionmanifest.SettingsSectionFieldNumber:
		return entitymeta.ValueNumber
	case extensionmanifest.SettingsSectionFieldText:
		return entitymeta.ValueText
	case extensionmanifest.SettingsSectionFieldBoolean:
		return entitymeta.ValueBoolean
	default:
		return entitymeta.ValueString
	}
}

// settingsSurfaceFieldConstraints 严格解析声明约束，并让 select 的 enum 只有
// 一个真值来源（选项本身）：声明里没写 enum 就从 options 派生；写了就必须与
// options 完全一致（解析器已校验）。
func settingsSurfaceFieldConstraints(field extensionmanifest.SettingsSectionField) (json.RawMessage, error) {
	parsed, err := extensionmanifest.ParseSettingsSectionConstraints(field)
	if err != nil {
		return nil, err
	}
	constraints := map[string]any{}
	if parsed.MaxLength != nil {
		constraints["maxLength"] = *parsed.MaxLength
	}
	if parsed.Min != nil {
		constraints["min"] = *parsed.Min
	}
	if parsed.Max != nil {
		constraints["max"] = *parsed.Max
	}
	if strings.ToLower(strings.TrimSpace(field.Type)) == extensionmanifest.SettingsSectionFieldSelect {
		values := make([]string, 0, len(field.Options))
		for _, option := range field.Options {
			if value := strings.TrimSpace(option.Value); value != "" {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("%w: select field %q has no options", extensionmanifest.ErrInvalidManifest, field.Key)
		}
		constraints["enum"] = values
	}
	if len(constraints) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(constraints)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
