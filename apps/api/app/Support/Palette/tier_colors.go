// Package palette 是**档位颜色**的唯一来源：取值规则 + 默认色序。
//
// 它被信任等级（TrustLevels）与徽章等级（Badges）共用。放在 Support 下而不是
// 任何一个领域包里，是因为这两个领域互不依赖：Badges 不该 import TrustLevels
// （那会把「徽章」和「信任等级」焊死），TrustLevels 也不该 import Badges。
// 谁先拥有白名单，谁就成了另一方的隐式依赖——所以白名单上提到共享层。
//
// 颜色的语义与站点主题（`data-sforum-theme` 的 5 套 accent）**解耦**：档位颜色只
// 表达「这一档的识别色」，不参与任何主题变量推导——换主题不该让档位颜色跟着漂，
// 反过来改档位颜色也不该影响站点的强调色。
//
// 自定义色受格式约束（6 位 hex、小写），不敞开接受任意字符串；是为了把「配置里
// 出现根本不是颜色的值」挡在保存期。颜色值不被要求静态可枚举：token 名由前端映射
// 为 class / CSS 变量，而 hex 自定义色直接落进内联 CSS 变量，明暗两套显示由前端
// CSS 从同一色值派生，服务端不需要（也不应该）替主题做明暗适配。
package palette

import (
	"regexp"
	"slices"
)

// tierColors 是**默认色序**，同时是后台推荐下拉的选项集（9 个语义色 token）。
//
// **9 项 = 信任等级 rank 0..8 一一对应**：TL0 占掉 slate，而可配置档位最多 8 个
// （TrustLevels.MaxTiers），所以合法 rank 上界是 TL8；色板必须正好覆盖到 TL8，
// 否则满配站点会撞色（8 色时 TL8 会回退到 slate，与 TL0 同色）。amber 专留给顶档，
// 对应设计稿里「amber = 终点」的语义。
//
// 顺序 = 默认色序：第 n 项属于 rank n（TL0 = slate）。徽章等级表复用同一份选项集，
// 但**不**复用「第 n 项属于 rank n」这条绑定：徽章等级自带显式 color 列。
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

// Tokens 返回颜色推荐序列（副本）。调用方不得原地修改。
//
// 后台读面把它作为 `colorChoices` 下发；前端不得自己再抄一份——抄了就会在服务端
// 调整白名单时变成「UI 里能选、保存时 422」。
func Tokens() []string {
	return slices.Clone(tierColors)
}

// IsTierColor 报告颜色值是否合法：9 个默认 token 之一，或小写 `#rrggbb`。
//
// 空串**不**合法：在信任等级里空串表示「跟随位次自动」，那是解码期的语义，
// 不是一种颜色；在徽章等级表里 color 非空由列默认值保证。
func IsTierColor(name string) bool {
	if slices.Contains(tierColors, name) {
		return true
	}
	return customColorPattern.MatchString(name)
}

// DefaultForRank 返回该 rank 的默认颜色；越界时返回白名单首项。
//
// 兜底与 TrustLevels 的 DefaultTierIcon 同理：正常 rank 不会越界，但手搓的 Policy
// 也不该让契约里 required 的 color 变成空串（空串在 UI 上是「没有颜色」，比回退更难排查）。
func DefaultForRank(rank int) string {
	if rank >= 0 && rank < len(tierColors) {
		return tierColors[rank]
	}
	return tierColors[0]
}
