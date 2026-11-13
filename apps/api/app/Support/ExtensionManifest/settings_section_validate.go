package extensionmanifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 设置面 tab 的字段键：只允许小写字母开头，后接小写字母/数字/_/-。
// 与 entity-meta 的 fieldKeyPattern（允许 . 与更长）不同：这里是**插件声明的键**，
// 不含宿主拼的 x.<extension>. 前缀，所以更短也更严。
var settingsSectionFieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,40}$`)

// 字段可见性。取值与 entity-meta 对齐（public/owner/admin），但这里不能 import
// Models 层，所以在本包内固定同一组字面量。
const (
	SettingsSectionVisibilityPublic = "public"
	SettingsSectionVisibilityOwner  = "owner"
	SettingsSectionVisibilityAdmin  = "admin"
)

// settingsSectionTabKinds 是 point↔type 矩阵的唯一真值：
// 个人页只能挂 fields/component，站点设置页只能引用自己的 settings tab。
func settingsSectionTabKinds(point string) []string {
	switch point {
	case PointForumSettingsProfileTabs:
		return []string{SettingsSectionTabFields, SettingsSectionTabComponent}
	case PointAdminSettingsTabs:
		return []string{SettingsSectionTabSettings}
	default:
		return nil
	}
}

func settingsSectionTabKindAllowed(point, kind string) bool {
	for _, allowed := range settingsSectionTabKinds(point) {
		if allowed == kind {
			return true
		}
	}
	return false
}

func supportedSettingsSectionFieldType(value string) bool {
	switch value {
	case SettingsSectionFieldString, SettingsSectionFieldText,
		SettingsSectionFieldNumber, SettingsSectionFieldBoolean, SettingsSectionFieldSelect:
		return true
	default:
		return false
	}
}

// NormalizeSettingsSectionVisibility 把空值归一成 public，并小写化。
// Provider 与字段同步共用它，避免展示值与存储值跑偏。
func NormalizeSettingsSectionVisibility(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return SettingsSectionVisibilityPublic
	}
	return value
}

func supportedSettingsSectionVisibility(value string) bool {
	switch NormalizeSettingsSectionVisibility(value) {
	case SettingsSectionVisibilityPublic, SettingsSectionVisibilityOwner, SettingsSectionVisibilityAdmin:
		return true
	default:
		return false
	}
}

// validateSettingsSectionContributionPayload 校验 settingsSection 载荷。
// manifest 用于三处交叉引用：permission 必须在包内声明、componentId 必须是
// 本包的 public L2 组件、tabId 必须指向本包 SettingsDocument 的 tab。
func validateSettingsSectionContributionPayload(manifest Manifest, point string, contribution ManifestContribution, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: settingsSection payload is required", ErrInvalidManifest)
	}
	var payload SettingsSectionContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("%w: settingsSection payload is not a descriptor", ErrInvalidManifest)
	}
	kind := strings.ToLower(strings.TrimSpace(payload.Type))
	if !settingsSectionTabKindAllowed(point, kind) {
		return fmt.Errorf("%w: settingsSection tab shape %q is not allowed on %s", ErrInvalidManifest, payload.Type, point)
	}
	if icon := strings.TrimSpace(payload.Icon); icon != "" && !allowedContributionIcon(icon) {
		return fmt.Errorf("%w: settingsSection icon %q is outside the host icon set", ErrInvalidManifest, icon)
	}
	// 权限过滤只在服务端发生，因此权限键必须由本扩展声明过。
	if permission := strings.TrimSpace(payload.Permission); permission != "" && !manifestHasPermission(manifest, permission) {
		return fmt.Errorf("%w: settingsSection permission %q is not declared by this package", ErrInvalidManifest, permission)
	}
	if count := settingsSectionTabsForPoint(manifest, point); count > SettingsSectionTabsPerPointMax {
		return fmt.Errorf("%w: %s declares %d tabs (cap %d)", ErrInvalidManifest, point, count, SettingsSectionTabsPerPointMax)
	}
	// 字段裸键的唯一性是**整包级**的：entity-meta 键是 x.<ext>.<key>，两个 tab
	// 用同名裸键会落到同一行，表现为「一处保存另一处也变」或「永远 enable 失败」。
	if duplicate := settingsSectionDuplicateFieldKey(manifest); duplicate != "" {
		return fmt.Errorf("%w: settingsSection field key %q is declared more than once in this package", ErrInvalidManifest, duplicate)
	}
	switch kind {
	case SettingsSectionTabFields:
		return validateSettingsSectionFieldTab(manifest, payload)
	case SettingsSectionTabComponent:
		return validateSettingsSectionComponentTab(manifest, payload)
	default:
		return validateSettingsSectionSettingsTab(manifest, payload)
	}
}

func validateSettingsSectionFieldTab(manifest Manifest, payload SettingsSectionContributionPayload) error {
	if len(payload.Fields) == 0 {
		return fmt.Errorf("%w: settingsSection type=fields requires at least one field", ErrInvalidManifest)
	}
	if len(payload.Fields) > SettingsSectionFieldsPerTabMax {
		return fmt.Errorf("%w: settingsSection tab declares %d fields (cap %d)", ErrInvalidManifest, len(payload.Fields), SettingsSectionFieldsPerTabMax)
	}
	if strings.TrimSpace(payload.ComponentID) != "" || strings.TrimSpace(payload.TabID) != "" {
		return fmt.Errorf("%w: settingsSection type=fields must not reference a component or settings tab", ErrInvalidManifest)
	}
	if total := settingsSectionDeclaredFieldCount(manifest); total > SettingsSectionFieldsPerExtensionMax {
		return fmt.Errorf("%w: package declares %d settings fields (cap %d)", ErrInvalidManifest, total, SettingsSectionFieldsPerExtensionMax)
	}
	for _, field := range payload.Fields {
		if err := validateSettingsSectionField(manifest, field); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingsSectionComponentTab(manifest Manifest, payload SettingsSectionContributionPayload) error {
	if strings.TrimSpace(payload.TabID) != "" || len(payload.Fields) != 0 {
		return fmt.Errorf("%w: settingsSection type=component must not declare fields or a settings tab", ErrInvalidManifest)
	}
	componentID := NormalizeID(payload.ComponentID)
	if componentID == "" {
		return fmt.Errorf("%w: settingsSection type=component requires componentId", ErrInvalidManifest)
	}
	// 与 forum.profile.tabs 的 l2Widget 同一判据：public = 组件自身不带 Permission，
	// 且必须是本 manifest 的 add 声明。tab 的 permission 只过滤「谁看到」，
	// 不授予组件执行权（挂载仍由公开 L2 运行时裁决）。
	for _, component := range manifest.Components {
		if NormalizeID(component.ID) != componentID {
			continue
		}
		if component.L2Component == "" || component.Action != ComponentActionAdd || component.Permission != "" {
			return fmt.Errorf("%w: settingsSection component %q is not a public L2 add component", ErrInvalidManifest, componentID)
		}
		return nil
	}
	return fmt.Errorf("%w: settingsSection component %q is not declared by this package", ErrInvalidManifest, componentID)
}

func validateSettingsSectionSettingsTab(manifest Manifest, payload SettingsSectionContributionPayload) error {
	if strings.TrimSpace(payload.ComponentID) != "" || len(payload.Fields) != 0 {
		return fmt.Errorf("%w: settingsSection type=settings must not declare fields or a component", ErrInvalidManifest)
	}
	tabID := NormalizeID(payload.TabID)
	if tabID == "" {
		return fmt.Errorf("%w: settingsSection type=settings requires tabId", ErrInvalidManifest)
	}
	// 站点设置页的 tab 内容来自插件自己的 SettingsDocument，而不是第二套字段声明。
	for _, tab := range manifest.SettingsDocument.UI.Tabs {
		if tab.ID == tabID {
			return nil
		}
	}
	return fmt.Errorf("%w: settingsSection tabId %q is not a tab of this package settings document", ErrInvalidManifest, tabID)
}

func validateSettingsSectionField(manifest Manifest, field SettingsSectionField) error {
	key := strings.TrimSpace(field.Key)
	if !settingsSectionFieldKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: settingsSection field key %q must match %s", ErrInvalidManifest, field.Key, settingsSectionFieldKeyPattern)
	}
	if field.Label.IsEmpty() {
		return fmt.Errorf("%w: settingsSection field %q requires a label", ErrInvalidManifest, key)
	}
	fieldType := strings.ToLower(strings.TrimSpace(field.Type))
	if !supportedSettingsSectionFieldType(fieldType) {
		return fmt.Errorf("%w: settingsSection field %q has unsupported type %q", ErrInvalidManifest, key, field.Type)
	}
	if !supportedSettingsSectionVisibility(field.Visibility) {
		return fmt.Errorf("%w: settingsSection field %q has unsupported visibility %q", ErrInvalidManifest, key, field.Visibility)
	}
	if permission := strings.TrimSpace(field.Permission); permission != "" && !manifestHasPermission(manifest, permission) {
		return fmt.Errorf("%w: settingsSection field %q declares undeclared permission %q", ErrInvalidManifest, key, permission)
	}
	// 约束严格解析（未知键/类型错配/枚举与 options 不一致都在这里拒绝）。
	if _, err := ParseSettingsSectionConstraints(field); err != nil {
		return err
	}
	if fieldType == SettingsSectionFieldSelect {
		if err := validateSettingsSectionOptions(key, field); err != nil {
			return err
		}
	} else if len(field.Options) != 0 {
		return fmt.Errorf("%w: settingsSection field %q declares options but is not type select", ErrInvalidManifest, key)
	}
	if err := validateSettingsSectionDefault(key, fieldType, field.Default, field.Options); err != nil {
		return err
	}
	return nil
}

func validateSettingsSectionOptions(key string, field SettingsSectionField) error {
	if len(field.Options) == 0 {
		return fmt.Errorf("%w: settingsSection field %q is a select without options", ErrInvalidManifest, key)
	}
	values := make(map[string]struct{}, len(field.Options))
	for _, option := range field.Options {
		value := strings.TrimSpace(option.Value)
		if value == "" || option.Label.IsEmpty() {
			return fmt.Errorf("%w: settingsSection field %q has an option without value or label", ErrInvalidManifest, key)
		}
		if _, duplicate := values[value]; duplicate {
			return fmt.Errorf("%w: settingsSection field %q repeats option %q", ErrInvalidManifest, key, value)
		}
		values[value] = struct{}{}
	}
	return nil
}

func validateSettingsSectionDefault(key, fieldType, value string, options []SettingsSectionOption) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch fieldType {
	case SettingsSectionFieldBoolean:
		switch strings.ToLower(value) {
		case "true", "false":
			return nil
		}
		return fmt.Errorf("%w: settingsSection field %q has a non-boolean default", ErrInvalidManifest, key)
	case SettingsSectionFieldNumber:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("%w: settingsSection field %q has a non-numeric default", ErrInvalidManifest, key)
		}
		return nil
	case SettingsSectionFieldSelect:
		for _, option := range options {
			if strings.TrimSpace(option.Value) == value {
				return nil
			}
		}
		return fmt.Errorf("%w: settingsSection field %q default %q is not one of its options", ErrInvalidManifest, key, value)
	default:
		return nil
	}
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return json.Valid(raw) && strings.HasPrefix(trimmed, "{")
}

// settingsSectionTabsForPoint 统计该表面上的插件 tab 数（声明形态不合法的
// 载荷按 0 计：它们会在各自的校验里被拒，这里只负责 cap 计数）。
func settingsSectionTabsForPoint(manifest Manifest, point string) int {
	count := 0
	for _, contribution := range manifest.Contributions {
		if contribution.Point != point {
			continue
		}
		count++
	}
	return count
}

// settingsSectionDuplicateFieldKey 返回整包里第一处重复的裸字段键。
func settingsSectionDuplicateFieldKey(manifest Manifest) string {
	seen := map[string]struct{}{}
	for _, contribution := range manifest.Contributions {
		if contribution.Point != PointForumSettingsProfileTabs && contribution.Point != PointAdminSettingsTabs {
			continue
		}
		var payload SettingsSectionContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			continue
		}
		for _, field := range payload.Fields {
			key := strings.TrimSpace(field.Key)
			if key == "" {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				return key
			}
			seen[key] = struct{}{}
		}
	}
	return ""
}

// settingsSectionDeclaredFieldCount 统计整包的设置字段总数。
func settingsSectionDeclaredFieldCount(manifest Manifest) int {
	total := 0
	for _, contribution := range manifest.Contributions {
		if contribution.Point != PointForumSettingsProfileTabs && contribution.Point != PointAdminSettingsTabs {
			continue
		}
		var payload SettingsSectionContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			continue
		}
		total += len(payload.Fields)
	}
	return total
}

// SettingsSectionFieldConstraints 是插件字段约束的**严格**解析结果。
//
// 与 entity-meta 的历史宽松行为分开：entity_meta 里运营手建的字段沿用「解析
// 不了就当没有约束」，但插件声明是本次新开的入口，不能 fail-open —— 一条写错
// 的 constraints 会变成「看起来有上限、实际什么都不校验」。
type SettingsSectionFieldConstraints struct {
	MaxLength *int     `json:"maxLength"`
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	Enum      []string `json:"enum"`
}

// ParseSettingsSectionConstraints 严格解析字段约束：
//   - 必须是 JSON 对象，未知键直接拒绝（宿主只认 maxLength/min/max/enum）
//   - maxLength 只对 string/text 有意义，必须 ≥0
//   - min/max 只对 number 有意义，且 min ≤ max
//   - enum 只对 select 有意义，必须非空、元素非空字符串，且与 options 集合一致
func ParseSettingsSectionConstraints(field SettingsSectionField) (SettingsSectionFieldConstraints, error) {
	var parsed SettingsSectionFieldConstraints
	fieldType := strings.ToLower(strings.TrimSpace(field.Type))
	if len(field.Constraints) > 0 {
		if !isJSONObject(field.Constraints) {
			return parsed, fmt.Errorf("%w: settingsSection field %q constraints must be a JSON object", ErrInvalidManifest, field.Key)
		}
		decoder := json.NewDecoder(strings.NewReader(string(field.Constraints)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&parsed); err != nil {
			return SettingsSectionFieldConstraints{}, fmt.Errorf("%w: settingsSection field %q constraints are invalid: %v", ErrInvalidManifest, field.Key, err)
		}
	}
	switch fieldType {
	case SettingsSectionFieldString, SettingsSectionFieldText:
		if parsed.MaxLength != nil && *parsed.MaxLength < 0 {
			return parsed, fmt.Errorf("%w: settingsSection field %q maxLength must be >= 0", ErrInvalidManifest, field.Key)
		}
		if parsed.Min != nil || parsed.Max != nil {
			return parsed, fmt.Errorf("%w: settingsSection field %q declares min/max but is not a number", ErrInvalidManifest, field.Key)
		}
		if parsed.Enum != nil {
			return parsed, fmt.Errorf("%w: settingsSection field %q declares enum but is not a select", ErrInvalidManifest, field.Key)
		}
	case SettingsSectionFieldNumber:
		if parsed.MaxLength != nil {
			return parsed, fmt.Errorf("%w: settingsSection field %q declares maxLength but is a number", ErrInvalidManifest, field.Key)
		}
		if parsed.Min != nil && parsed.Max != nil && *parsed.Min > *parsed.Max {
			return parsed, fmt.Errorf("%w: settingsSection field %q min is greater than max", ErrInvalidManifest, field.Key)
		}
		if parsed.Enum != nil {
			return parsed, fmt.Errorf("%w: settingsSection field %q declares enum but is not a select", ErrInvalidManifest, field.Key)
		}
	case SettingsSectionFieldBoolean:
		if parsed.MaxLength != nil || parsed.Min != nil || parsed.Max != nil || parsed.Enum != nil {
			return parsed, fmt.Errorf("%w: settingsSection field %q is a boolean and accepts no constraints", ErrInvalidManifest, field.Key)
		}
	case SettingsSectionFieldSelect:
		// enum 可选：省略时以 options 为唯一真值（同步侧据此写存储层约束）。
		// 但**写了就必须是对的**——空数组与非 options 集合都拒绝。
		if parsed.Enum != nil {
			if len(parsed.Enum) == 0 {
				return parsed, fmt.Errorf("%w: settingsSection select %q declares an empty enum", ErrInvalidManifest, field.Key)
			}
			if err := settingsSectionEnumMatchesOptions(field.Key, parsed.Enum, field.Options); err != nil {
				return parsed, err
			}
		}
	}
	return parsed, nil
}

// settingsSectionEnumMatchesOptions 要求 enum 与 options 是同一个集合：
// 两个真值来源（枚举校验与下拉框）不允许各自维护一份。
func settingsSectionEnumMatchesOptions(key string, enum []string, options []SettingsSectionOption) error {
	declared := make(map[string]struct{}, len(options))
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			return fmt.Errorf("%w: settingsSection select %q has an option without value", ErrInvalidManifest, key)
		}
		declared[value] = struct{}{}
	}
	seen := make(map[string]struct{}, len(enum))
	for _, value := range enum {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("%w: settingsSection select %q declares an empty enum value", ErrInvalidManifest, key)
		}
		if _, ok := declared[trimmed]; !ok {
			return fmt.Errorf("%w: settingsSection select %q enum value %q is not one of its options", ErrInvalidManifest, key, trimmed)
		}
		if _, duplicate := seen[trimmed]; duplicate {
			return fmt.Errorf("%w: settingsSection select %q repeats enum value %q", ErrInvalidManifest, key, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	if len(seen) != len(declared) {
		return fmt.Errorf("%w: settingsSection select %q enum must cover every option", ErrInvalidManifest, key)
	}
	return nil
}
