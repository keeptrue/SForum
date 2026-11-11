package trustlevels

import (
	"regexp"
	"slices"
)

// 本文件是档位**颜色**的唯一来源：取值规则 + 默认色序（与 tier_icons.go 同构）。
//
// tierColors 是**默认色序**，同时是后台推荐下拉的选项集（9 个语义色 token）。
// 它不再是唯一的合法取值：运营还可以写入 `#rrggbb` 形式的自定义色（见
// customColorPattern）。
//
// 色相语义与站点主题（`data-sforum-theme` 的 5 套 accent）**解耦**：档位颜色只表达
// 「这一档的识别色」，不参与任何主题变量推导——换主题不该让档位颜色跟着漂，反过来
// 改档位颜色也不该影响站点的强调色。
//
// 自定义色受格式约束（6 位 hex、小写），不敞开接受任意字符串；是为了把「配置里
// 出现根本不是颜色的值」挡在保存期。颜色值不再被要求静态可枚举：token 名仍由前端
// 映射为 class / CSS 变量，而 hex 自定义色直接落进内联 CSS 变量，明暗两套显示由
// 前端 CSS 从同一色值派生，服务端不需要（也不应该）替主题做明暗适配。
//
// **9 项 = rank 0..8 一一对应**：TL0 占掉 slate，而可配置档位最多 8 个（MaxTiers），
// 所以合法 rank 上界是 TL8；色板必须正好覆盖到 TL8，否则满配站点会撞色
// （8 色时 TL8 会回退到 slate，与 TL0 同色）。amber 专留给顶档，对应设计稿里
// 「amber = 终点」的语义。
//
// 顺序 = 默认色序：第 n 项属于 rank n（TL0 = slate）。
var tierColors = []string{
	"slate",
	"teal",
	"emerald",
	"sky",
	"indigo",
	"violet",
	"fuchsia",
	"rose",
	"amber",
}

// customColorPattern 是自定义色的语法：`#rrggbb`（校验与存储前统一转小写）。
var customColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// TierColors 返回颜色推荐序列（副本）。调用方不得原地修改。
//
// 后台读面把它作为 `colorChoices` 下发；前端不得自己再抄一份——抄了就会在服务端
// 调整白名单时变成「UI 里能选、保存时 422」。
func TierColors() []string {
	return slices.Clone(tierColors)
}

// IsTierColor 报告颜色值是否合法：9 个默认 token 之一，或小写 `#rrggbb`。
func IsTierColor(name string) bool {
	if slices.Contains(tierColors, name) {
		return true
	}
	return customColorPattern.MatchString(name)
}

// DefaultTierColor 返回该 rank 的默认颜色；超出默认色序时返回白名单首项。
//
// 兜底与 DefaultTierIcon 同理：正常 rank 不会越界，但手搓的 Policy 也不该让
// 契约里 required 的 color 变成空串（空串在 UI 上是「没有颜色」，比回退更难排查）。
func DefaultTierColor(rank Level) string {
	if rank >= LevelNewcomer && int(rank) < len(tierColors) {
		return tierColors[rank]
	}
	return tierColors[0]
}
