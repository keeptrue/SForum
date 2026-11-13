package extensionmanifest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
)

// 设置面贡献点：让插件把自己的设置（或一组按用户存储的表单字段）挂到宿主
// 已有的设置页上，而不是另建一个插件专属页面。
//
// 两条表面刻意分开：
//   - forum.settings.profile.tabs —— 个人资料设置页的 tab（按用户存值）
//   - admin.settings.tabs        —— 站点设置页的 tab（站点级配置）
//
// 单独成文件是刻意的：manifest.go 是已到顶的 legacy 文件（架构门禁 cap 精确到行），
// 设置面载荷契约在这里独立演进；贡献点定义由 settingsSurfaceContributionPoints()
// 在 manifest.go 里以零行增量组合进目录。
const (
	PointForumSettingsProfileTabs = "forum.settings.profile.tabs"
	PointAdminSettingsTabs        = "admin.settings.tabs"
)

// PayloadTypeSettingsSection 是这两个贡献点共用的宿主拥有 payload 类型。
const PayloadTypeSettingsSection = "settingsSection"

// tab 形态判别值（payload.type）。point↔type 矩阵见
// settings_section_validate.go 的 settingsSectionTabKinds。
const (
	// SettingsSectionTabFields：宿主按 fields[] 渲染表单。个人页按用户存值。
	SettingsSectionTabFields = "fields"
	// SettingsSectionTabComponent：宿主挂载本 manifest 声明的 public L2 组件。
	SettingsSectionTabComponent = "component"
	// SettingsSectionTabSettings：宿主渲染该插件 SettingsDocument 里的一个 tab。
	SettingsSectionTabSettings = "settings"
)

// 字段取值类型。select 落到 entity-meta 的 string + enum 约束。
const (
	SettingsSectionFieldString  = "string"
	SettingsSectionFieldText    = "text"
	SettingsSectionFieldNumber  = "number"
	SettingsSectionFieldBoolean = "boolean"
	SettingsSectionFieldSelect  = "select"
)

// 声明上限。存在的理由是「一次渲染的成本由 manifest 决定」：宿主不能在读到
// 第 500 个字段时才发现要为一页设置渲染 500 个控件。
const (
	// SettingsSectionFieldsPerTabMax 单个 tab 可声明的字段数。
	SettingsSectionFieldsPerTabMax = 24
	// SettingsSectionFieldsPerExtensionMax 单个扩展跨所有设置面 tab 的字段总数。
	SettingsSectionFieldsPerExtensionMax = 60
	// SettingsSectionTabsPerPointMax 单个表面（贡献点）上允许出现的插件 tab 数，
	// Provider 侧同值截断并告警。
	SettingsSectionTabsPerPointMax = 12
)

// SettingsSectionContributionPayload 是 forum.settings.profile.tabs /
// admin.settings.tabs 的载荷。type 是 tab 形态判别值，不是载荷类型名：
// 载荷类型由贡献点目录固定为 settingsSection。
//
// permission 非空时该 tab 只对拥有该权限键的访客出现；宿主在服务端过滤，
// 该字段永不下发前端。字段级 permission 同理。
type SettingsSectionContributionPayload struct {
	Type        string                 `json:"type"`
	Icon        string                 `json:"icon,omitempty"`
	Description LocalizedText          `json:"description,omitempty"`
	Permission  string                 `json:"permission,omitempty"`
	Fields      []SettingsSectionField `json:"fields,omitempty"`
	// ComponentID 仅用于 type=component：必须是本 manifest 声明且 public 的 L2 组件。
	ComponentID string `json:"componentId,omitempty"`
	// TabID 仅用于 type=settings：点名本 manifest SettingsDocument 里的一个 tab。
	TabID string `json:"tabId,omitempty"`
}

// SettingsSectionField 是一个可扩展表单字段的声明。
//
// 声明只描述形状与校验；值不在这里。个人页字段的值按 (user, fieldKey) 落在
// entity-meta，字段键统一前缀 x.<extension>.<key>（见 Providers 的字段同步）。
type SettingsSectionField struct {
	Key         string                  `json:"key"`
	Order       int                     `json:"order,omitempty"`
	Label       LocalizedText           `json:"label"`
	Description LocalizedText           `json:"description,omitempty"`
	Type        string                  `json:"type"`
	Default     string                  `json:"default,omitempty"`
	Placeholder LocalizedText           `json:"placeholder,omitempty"`
	Required    bool                    `json:"required,omitempty"`
	Visibility  string                  `json:"visibility,omitempty"`
	Constraints json.RawMessage         `json:"constraints,omitempty"`
	Options     []SettingsSectionOption `json:"options,omitempty"`
	Permission  string                  `json:"permission,omitempty"`
}

// SettingsSectionOption 是 select 字段的一个取值。value 是存储值，label 是展示文案。
type SettingsSectionOption struct {
	Value string        `json:"value"`
	Label LocalizedText `json:"label"`
}

// settingsSurfaceContributionPoints 由 ContributionPointDefinitions 组合进目录。
// 单独成函数是为了让 manifest.go 保持零行增量（它是架构门禁里精确到行的
// legacy 基线文件）。
func settingsSurfaceContributionPoints() []ContributionPointDefinition {
	return []ContributionPointDefinition{
		{
			ID:    PointForumSettingsProfileTabs,
			Owner: "forum",
			Kind:  ContributionPointKindDescriptor,
			Description: "Personal settings page tabs rendered by the host UI. Allowed tab shapes: " +
				"`fields` (host-rendered form, values stored per user in entity-meta) or `component` " +
				"(a public L2 component declared by this package). Optional permission keys are filtered " +
				"server-side and never reach the client.",
			PayloadType: PayloadTypeSettingsSection,
		},
		{
			ID:    PointAdminSettingsTabs,
			Owner: "admin",
			Kind:  ContributionPointKindDescriptor,
			Description: "Site settings page tabs rendered by the host UI. The only allowed tab shape is " +
				"`settings`: the tab shows one tab of this package SettingsDocument, selected by `tabId`. " +
				"Optional permission keys are filtered server-side and never reach the client.",
			PayloadType: PayloadTypeSettingsSection,
		},
	}
}

// 字段键换算的唯一实现。输入是 manifest 声明的裸 key，输出是 entity-meta 的
// 字段键：x.<extension>.<key>。
//
// 为什么强制组合：前端把「字段键 = 值键 = 错误键」当同一把键，PUT 的 meta 键
// 必须能直接命中 entity-meta 行；插件之间也不能靠裸键互相踩值。
const (
	settingsSurfaceFieldKeyPrefix = "x."
	// entityMetaFieldKeyMaximum 与 entity-meta 的 fieldKeyPattern
	// （^[a-z][a-z0-9._-]{1,63}$）一致：2 + 20 + 1 + 41 = 64。
	entityMetaFieldKeyMaximum              = 64
	settingsSurfaceExtensionSegmentMaximum = 20
)

// SettingsSurfaceFieldKey 组合 manifest 裸键与扩展 id。字段同步、读模型下发、
// 治理写端点都走这一个函数——在别处再拼一次就是漂移的起点。
func SettingsSurfaceFieldKey(extensionID, key string) string {
	return settingsSurfaceFieldKeyPrefix + settingsSurfaceExtensionSegment(extensionID) +
		"." + strings.ToLower(strings.TrimSpace(key))
}

// SettingsSurfaceBareFieldKey 从写入入参取回裸键：入参可以是组合键
// （x.<extension>.<key>）也可以是裸键，治理端点两种都必须接受。
func SettingsSurfaceBareFieldKey(extensionID, itemKey string) string {
	itemKey = strings.TrimSpace(itemKey)
	prefix := settingsSurfaceFieldKeyPrefix + settingsSurfaceExtensionSegment(extensionID) + "."
	if strings.HasPrefix(itemKey, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(itemKey, prefix))
	}
	if _, bare, ok := SettingsSurfaceSplitFieldKey(itemKey); ok {
		return bare
	}
	return itemKey
}

// SettingsSurfaceSplitFieldKey 把组合键拆成扩展段与裸键。
//
// 扩展段里的 . 在组合时已被替换成 -，因此组合键恰好是两个点：
// x.<segment>.<key>。治理端点拿不到扩展 id 时靠这条结构判断「这是组合键还是裸键」。
func SettingsSurfaceSplitFieldKey(itemKey string) (segment, key string, ok bool) {
	itemKey = strings.TrimSpace(itemKey)
	if !strings.HasPrefix(itemKey, settingsSurfaceFieldKeyPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(itemKey, settingsSurfaceFieldKeyPrefix)
	index := strings.Index(rest, ".")
	if index <= 0 || index == len(rest)-1 {
		return "", "", false
	}
	return rest[:index], rest[index+1:], true
}

// settingsSurfaceExtensionSegment 把扩展 id 压成 entity-meta 字段键可用的段。
//
// 段**无条件**带 8 位 FNV-1a 哈希后缀（输入是 NormalizeID 后的原始 id）：
// 可读段只做展示，身份由哈希保证。否则 `sforum.smtp` 与 `sforum-smtp` 这种
// 「只有分隔符不同」的 id 会在压平为 [a-z0-9_-] 后撞成同一行字段。
//
// 结构：<可读前缀 ≤11 且不少于 8><-><8 位十六进制>，总长 ≤20，于是
// x.<ext>.<key> ≤ 2+20+1+41 = 64，正好是 entity-meta fieldKey 的上限。
func settingsSurfaceExtensionSegment(extensionID string) string {
	extensionID = NormalizeID(extensionID)
	digest := fnv.New32a()
	_, _ = digest.Write([]byte(extensionID))
	suffix := fmt.Sprintf("%08x", digest.Sum32())

	readable := make([]rune, 0, len(extensionID))
	for _, r := range extensionID {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			readable = append(readable, r)
		default:
			readable = append(readable, '-')
		}
	}
	prefix := string(readable)
	maxPrefix := settingsSurfaceExtensionSegmentMaximum - 1 - len(suffix)
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	// 可读前缀保底 8 字符：太短的 id（如 `a`）不该让整段看起来像纯哈希。
	for len(prefix) < 8 {
		prefix += "_"
	}
	return prefix + "-" + suffix
}
