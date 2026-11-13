// Package settingssurface 是「插件注册的设置 tab / 可扩展字段」的站点治理层：
// 运营可以对每一条声明单独关掉或调序，没有覆盖的一律沿用 manifest 声明。
//
// 为什么在 Models/Extensions 下另开子包，而不是把两个文件平铺进去：
// Models/Extensions 目录的直接文件数已经是架构棘轮（flatDirectoryCaps）里
// 精确到个数的基线，平铺会把基线顶上抬；覆盖行的读/写与语义本来就与该包
// 的扩展生命周期正交，子包是更小的让步。
package settingssurface

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 覆盖键：一条 manifest 声明（tab）或其中的一个字段（item_key）。
type OverrideKey struct {
	ExtensionID    string
	Point          string
	ContributionID string
	// ItemKey 为空表示整个 tab；否则是该 tab 内字段声明的 key。
	ItemKey string
}

// Normalized 归一化键：贡献 id 与扩展 id 大小写不敏感（manifest 侧统一小写）。
func (key OverrideKey) Normalized() OverrideKey {
	return OverrideKey{
		ExtensionID:    extensionmanifest.NormalizeID(key.ExtensionID),
		Point:          strings.TrimSpace(key.Point),
		ContributionID: extensionmanifest.NormalizeID(key.ContributionID),
		// itemKey 是 manifest 裸键：声明侧统一小写，这里对称归一，
		// 传 "Theme" 也应该命中 "theme"，而不是让调用方自己猜。
		ItemKey: strings.ToLower(strings.TrimSpace(key.ItemKey)),
	}
}

func (key OverrideKey) valid() bool {
	return key.ExtensionID != "" && key.Point != "" && key.ContributionID != ""
}

// Override 是一行治理覆盖。SortOrder 为 nil 表示「不覆盖顺序」。
type Override struct {
	Key             OverrideKey
	Enabled         bool
	SortOrder       *int
	UpdatedByUserID *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// OverrideInput 是一次写入。Enabled 无三态：写入即表示运营明确给出可见性；
// 想恢复 manifest 默认就得删行。
type OverrideInput struct {
	Key             OverrideKey
	Enabled         bool
	SortOrder       *int
	UpdatedByUserID *int64
}

// OverrideReader 是 Provider 唯一需要的读能力。
type OverrideReader interface {
	ListOverrides(ctx context.Context) ([]Override, error)
}

// OverrideWriter 是治理保存路径需要的能力。
type OverrideWriter interface {
	UpsertOverride(ctx context.Context, input OverrideInput) (Override, error)
	DeleteOverride(ctx context.Context, key OverrideKey) error
}

// OverrideStore 是完整的持久化契约。
type OverrideStore interface {
	OverrideReader
	OverrideWriter
}

// Section 是过滤、排序、截断之后的一个设置 tab，也是唯一会下发前端的形状。
//
// 刻意没有 Permission 字段：权限过滤只在服务端发生，权限键永不下发。
// 同理没有声明上限/原始 payload：前端只拿到已经裁决过的结果。
type Section struct {
	// Source 让前端区分「插件贡献的 tab」与将来可能的宿主自有 tab。
	Source        string `json:"source"`
	ExtensionID   string `json:"extensionId"`
	ExtensionName string `json:"extensionName"`
	ID            string `json:"id"`
	// EffectiveSortOrder 是裁决后的生效顺序（治理覆盖优先，否则声明顺序）。
	// 名字与治理写端点的 sortOrder 刻意分开：写用请求名，读用生效名。
	EffectiveSortOrder int                             `json:"effectiveSortOrder"`
	Label              map[string]string               `json:"label,omitempty"`
	Icon               string                          `json:"icon,omitempty"`
	Description        extensionmanifest.LocalizedText `json:"description,omitempty"`
	Type               string                          `json:"type"`
	ComponentID        string                          `json:"componentId,omitempty"`
	TabID              string                          `json:"tabId,omitempty"`
	Fields             []Field                         `json:"fields,omitempty"`
}

// Field 是一个可扩展表单字段的展示形状（同样不含 Permission）。
type Field struct {
	// FieldKey 是 entity-meta 组合键（x.<extension>.<key>），不是 manifest 裸键：
	// 前端把它同时当值键与错误键，PUT 的 meta 键必须能直接命中行。
	FieldKey           string                          `json:"fieldKey"`
	EffectiveSortOrder int                             `json:"effectiveSortOrder"`
	Label              extensionmanifest.LocalizedText `json:"label"`
	Description        extensionmanifest.LocalizedText `json:"description,omitempty"`
	Type               string                          `json:"type"`
	Default            string                          `json:"default,omitempty"`
	Placeholder        extensionmanifest.LocalizedText `json:"placeholder,omitempty"`
	Required           bool                            `json:"required,omitempty"`
	Visibility         string                          `json:"visibility"`
	Constraints        json.RawMessage                 `json:"constraints,omitempty"`
	Options            []Option                        `json:"options,omitempty"`
}

// Option 是 select 字段的一个取值。
type Option struct {
	Value string                          `json:"value"`
	Label extensionmanifest.LocalizedText `json:"label"`
}
