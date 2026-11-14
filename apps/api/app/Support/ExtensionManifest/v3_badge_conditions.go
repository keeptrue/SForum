package extensionmanifest

import (
	"fmt"
	"regexp"
	"strings"
)

// ManifestBadgeCondition 声明一个插件事实条件。它不是规则条件类型：规则里
// 永远写 type:"fact.count"，这里声明的是 factKey 的合法取值与 variant 候选。
type ManifestBadgeCondition struct {
	Key             string                          `json:"key"`
	ContractVersion string                          `json:"contractVersion"`
	Label           map[string]string               `json:"label"`
	Unit            map[string]string               `json:"unit,omitempty"`
	Compare         []string                        `json:"compare,omitempty"`
	Variants        []ManifestBadgeConditionVariant `json:"variants,omitempty"`
}

type ManifestBadgeConditionVariant struct {
	Value string            `json:"value"`
	Label map[string]string `json:"label"`
}

// BadgeConditionsMaximum 与 ruleengine.MaxLeaves 对齐：一条规则的叶子数上限
// 就是事实条件数的自然上界，多声明的条件永远进不了任何规则。
const BadgeConditionsMaximum = 32

// BadgeConditionVariantsMaximum 是单个条件的 variant 上限。
const BadgeConditionVariantsMaximum = 16

// badgeConditionVariantValuePattern 与 manifest-v3.schema.json 的
// badgeConditions.items.properties.variants.items.properties.value.pattern 逐字一致。
// 事实表侧 variant 没有 CHECK 约束（迁移里是 TEXT NOT NULL DEFAULT ”），所以这里
// 是唯一收口点：`”` 表示「无 variant」，因此空串必须在清单阶段被拒。
var badgeConditionVariantValuePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validateFactConditions 校验插件事实条件声明（`badgeConditions` 与 `userFactConditions`
// 两个数组共用同一套规则；后者是中性名，前者是兼容别名）。
//
// **改了 Go struct 就必须同批改 schemas/manifest-v3.schema.json。**
// 本仓没有任何 reflect 式的「struct 字段 ↔ schema properties」对齐测试：
//   - 只加 struct 字段不改 schema → ValidateV3JSONSchema 因根 additionalProperties:false
//     直接拒包，但纯 Go 的 Validate(manifest) 路径照样放行，单测可能全绿，
//     只有装载真实 .zip 时才炸；
//   - 只加 schema 不改 struct → 声明被 JSON 解析静默丢弃（encoding/json 忽略未知键），
//     更危险：插件以为声明成功，事实条件却不在目录里。
//
// 两边的连接点只有人工纪律 + 这条注释，别指望测试兜住。
//
// 另一条纪律：key 的 pattern 必须与 badge_facts.fact_key 的 CHECK 逐字一致
// （database/migrations/202611120002_badge_rules.sql）。放宽任何一侧都会得到
// 「清单通过、上报 500」或「上报通过、清单拒绝」的半死状态。注意三方长度并不齐：
// schema 与该 CHECK 都是 2–121 字符，而 Go 侧复用的 manifestIDPattern 只到 81 ——
// 82–121 字符的 key 能过 schema 与 DB CHECK，但会被 manifest 的 versionedID 拒；
// 这是安全侧偏差（保留更严的一侧），不是可修的不一致。
//
// 第三条纪律：**不要给 badgeConditions 加回 valueKind**。插件条件只有 count 语义，
// 「是否发生过」用 compare:["gte"] + 规则 cmp:{gte,1} 表达。加回它等于同时增加
// 一个 struct↔schema 失配点、一条 boolean 求值分支、和一个 not 反转坑（2026-11-12 裁决）。
func (v *v3Validator) validateFactConditions() error {
	groups := []struct {
		field string
		items []ManifestBadgeCondition
	}{
		{"badgeConditions", v.manifest.BadgeConditions},
		{"userFactConditions", v.manifest.UserFactConditions},
	}
	total := 0
	for _, group := range groups {
		total += len(group.items)
	}
	if total == 0 {
		return nil
	}
	// rule #1：主题不能声明事实条件——事实由插件 backend 经 Host 命令写入，
	// 主题没有调用方。
	if v.manifest.Type != TypePlugin {
		return fmt.Errorf("%w: v3 factConditions rule #1", ErrInvalidManifest)
	}
	// rule #2：没有 backend 就没有写入方，声明只会变成永远为 0 的死候选。
	if strings.TrimSpace(v.manifest.Backend.Entry) == "" || v.manifest.Backend.ProtocolVersion != 2 {
		return fmt.Errorf("%w: v3 factConditions rule #2", ErrInvalidManifest)
	}
	// rule #3：**合计**上限。两个数组是同一份事实层的两个名字，各给 32 等于允许 64 条，
	// 而一条规则的叶子上限就是 32（ruleengine.MaxLeaves）——多出来的永远进不了任何规则。
	if total > BadgeConditionsMaximum {
		return fmt.Errorf("%w: v3 factConditions rule #3", ErrInvalidManifest)
	}
	for _, group := range groups {
		field := group.field
		for _, condition := range group.items {
			// versionedID 一次拿到三件事：形态（manifestIDPattern）、`<manifest.id>.`
			// 前缀、以及跨 family 的 id 唯一性（v.ids 全清单共享）。**两个数组共享同一
			// 个 v.ids**，所以跨数组重名也会在这里被拒——「key 全局唯一」不需要第二份判据。
			if err := v.versionedID(condition.Key, condition.ContractVersion, field); err != nil {
				return err
			}
			// 契约版本必须恰好是 `key@N`。versionedID 只校验 pattern，不管等式；
			// 两个名字一旦漂移（`a.b` 配 `a.condition.b@1`），审计与 rule_health 里
			// 就无法判断它们是不是同一个东西。规则编号沿用冻结规范的六条，不新增编号。
			if condition.ContractVersion != condition.Key+"@"+contractVersionOf(condition.ContractVersion) {
				return fmt.Errorf("%w: v3 %s contractVersion drift (key=%s contractVersion=%s)",
					ErrInvalidManifest, field, condition.Key, condition.ContractVersion)
			}
			// rule #4：compare 必须有内容、去重、且只含 CompareOp 全集（没有 ne）。
			// 空 compare 的条件永远无法被任何规则使用，放行只会让作者以为声明成功。
			if len(condition.Compare) == 0 {
				return fmt.Errorf("%w: v3 %s rule #4", ErrInvalidManifest, field)
			}
			seenCompare := make(map[string]struct{}, len(condition.Compare))
			for _, op := range condition.Compare {
				if !validBadgeConditionCompareOp(op) {
					return fmt.Errorf("%w: v3 %s rule #4", ErrInvalidManifest, field)
				}
				if _, duplicate := seenCompare[op]; duplicate {
					return fmt.Errorf("%w: v3 %s rule #4", ErrInvalidManifest, field)
				}
				seenCompare[op] = struct{}{}
			}
			// rule #5：variant 上限、value 非空/唯一/形态。
			if len(condition.Variants) > BadgeConditionVariantsMaximum {
				return fmt.Errorf("%w: v3 %s rule #5", ErrInvalidManifest, field)
			}
			seenVariants := make(map[string]struct{}, len(condition.Variants))
			for _, variant := range condition.Variants {
				if !badgeConditionVariantValuePattern.MatchString(variant.Value) {
					return fmt.Errorf("%w: v3 %s rule #5", ErrInvalidManifest, field)
				}
				if _, duplicate := seenVariants[variant.Value]; duplicate {
					return fmt.Errorf("%w: v3 %s rule #5", ErrInvalidManifest, field)
				}
				seenVariants[variant.Value] = struct{}{}
			}
			// rule #6：label 是服务端下发给编辑器渲染的文案，缺任一语种就会在另一种
			// 界面语言里显示错语言。unit 可选，但给了就守同一条规则。
			if !validBadgeConditionLocalizedText(condition.Label) {
				return fmt.Errorf("%w: v3 %s rule #6", ErrInvalidManifest, field)
			}
			if len(condition.Unit) > 0 && !validBadgeConditionLocalizedText(condition.Unit) {
				return fmt.Errorf("%w: v3 %s rule #6", ErrInvalidManifest, field)
			}
		}
	}
	return nil
}

// validBadgeConditionCompareOp 与 CompareOp 全集（types.go）一致：没有 ne，
// 「未达成」请用 not 包住 gte 表达。
func validBadgeConditionCompareOp(value string) bool {
	switch value {
	case "gte", "gt", "lte", "lt", "eq":
		return true
	default:
		return false
	}
}

// validBadgeConditionLocalizedText 要求双语齐全且值非空。
func validBadgeConditionLocalizedText(values map[string]string) bool {
	for _, locale := range []string{"zh-CN", "en-US"} {
		if strings.TrimSpace(values[locale]) == "" {
			return false
		}
	}
	return true
}
