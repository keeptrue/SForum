package trustlevels

import "slices"

// 本文件是档位**颜色**的唯一来源：白名单 + 默认色序（与 tier_icons.go 同构）。
//
// 色相语义与站点主题（`data-sforum-theme` 的 5 套 accent）**解耦**：档位颜色只表达
// 「这一档的识别色」，不参与任何主题变量推导——换主题不该让档位颜色跟着漂，反过来
// 改档位颜色也不该影响站点的强调色。
//
// 白名单固定 9 色，**不接受自由 hex**：颜色值会进 class / CSS 变量名，前端要靠它
// 静态可枚举才能做树摇与 Tailwind 扫描；敞开接受任意字符串等于把渲染形状交给配置，
// 而且运营填错时只会得到一个「类存在但样式不存在」的透明档位。
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

// TierColors 返回颜色白名单（副本）。调用方不得原地修改。
//
// 后台读面把它作为 `colorChoices` 下发；前端不得自己再抄一份——抄了就会在服务端
// 调整白名单时变成「UI 里能选、保存时 422」。
func TierColors() []string {
	return slices.Clone(tierColors)
}

// IsTierColor 报告颜色名是否在白名单内。
func IsTierColor(name string) bool {
	return slices.Contains(tierColors, name)
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
