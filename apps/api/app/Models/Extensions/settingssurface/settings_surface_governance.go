package settingssurface

import (
	"encoding/json"
	"errors"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	// ErrUnknownContribution 表示治理目标不是当前生效的插件声明（拼错、插件被停用、
	// 或 manifest 升级后删掉了这条贡献）。
	ErrUnknownContribution = errors.New("settings_surface: unknown contribution")
	// ErrUnknownItem 表示目标 tab 里没有这个字段。
	ErrUnknownItem = errors.New("settings_surface: unknown item")
)

// MutateInput 是一次治理写入（tab 级或字段级）。
//
// 三态是刻意的：body 里没写 enabled 表示「不改这一列」，写了 false 才是关闭；
// sortOrder 用 SortOrderSet 区分「没传」与「传了 null（清掉顺序覆盖）」。
// 只有两态实现的话，改顺序会顺手把运营关掉的 tab 重新打开。
type MutateInput struct {
	Key          OverrideKey
	Enabled      *bool
	SortOrderSet bool
	SortOrder    *int
}

// OverrideView 是治理写端点的响应：写完这一行之后的生效事实。
//
// 写用 sortOrder、读用 effectiveSortOrder 是刻意的命名分离（前端按此实现）：
// 请求名描述意图，响应名描述生效结果。
type OverrideView struct {
	ExtensionID        string `json:"extensionId"`
	Point              string `json:"point"`
	ContributionID     string `json:"contributionId"`
	ItemKey            string `json:"itemKey,omitempty"`
	Enabled            bool   `json:"enabled"`
	DeclaredSortOrder  int    `json:"declaredSortOrder"`
	EffectiveSortOrder int    `json:"effectiveSortOrder"`
	Overridden         bool   `json:"overridden"`
}

// ContributionItemView 是治理读模型里的一个可扩展字段。
type ContributionItemView struct {
	FieldKey           string `json:"fieldKey"`
	Enabled            bool   `json:"enabled"`
	EffectiveSortOrder int    `json:"effectiveSortOrder"`
	Overridden         bool   `json:"overridden"`
}

// ContributionView 是 /admin/extensions/contributions 的一项。
//
// 它在既有的 EffectiveContribution 之上补治理事实：enabled / overridden /
// effectiveSortOrder / fields[]。Payload 原样保留，既有调用方读到的键不变。
type ContributionView struct {
	ExtensionID   string `json:"extensionId"`
	ExtensionName string `json:"extensionName"`
	ExtensionType string `json:"extensionType"`
	Point         string `json:"point"`
	ID            string `json:"id"`
	Order         int    `json:"order"`
	Enabled       bool   `json:"enabled"`
	Overridden    bool   `json:"overridden"`
	// EffectiveSortOrder 是裁决后的顺序（覆盖优先，否则声明顺序）。
	EffectiveSortOrder int `json:"effectiveSortOrder"`
	// SuppressedByCap 表示该贡献在渲染侧会被单表面上限截断：治理读端点不截断，
	// 只标记，运营才知道「这条我关了也看不到变化」是为什么。
	SuppressedByCap bool                   `json:"suppressedByCap"`
	Label           map[string]string      `json:"label,omitempty"`
	Icon            string                 `json:"icon,omitempty"`
	Payload         json.RawMessage        `json:"payload,omitempty"`
	Fields          []ContributionItemView `json:"fields"`
}

// SurfaceTab 是站点设置页上的一个插件 tab。
//
// ID 采用 ext.<extensionId>.<contributionId> 命名空间：站点设置页的固定 tab 用
// 裸 id（general / login ...），插件 tab 必须一眼可分辨且不会撞名。
type SurfaceTab struct {
	ID             string            `json:"id"`
	Point          string            `json:"point"`
	ContributionID string            `json:"contributionId"`
	Label          map[string]string `json:"label,omitempty"`
	Icon           string            `json:"icon,omitempty"`
	Type           string            `json:"type"`
	ExtensionID    string            `json:"extensionId"`
	TabID          string            `json:"tabId,omitempty"`
	// Settings 是内联解析后的插件设置文档（免二次请求）。只有 type=settings 才有。
	Settings *extensions.ExtensionSettings `json:"settings,omitempty"`
}

// SurfaceTabID 生成站点设置页插件 tab 的稳定 id。
func SurfaceTabID(extensionID, contributionID string) string {
	return "ext." + strings.ToLower(strings.TrimSpace(extensionID)) + "." +
		strings.ToLower(strings.TrimSpace(contributionID))
}
