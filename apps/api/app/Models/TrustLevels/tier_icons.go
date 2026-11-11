package trustlevels

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// 本文件是档位**展示元数据**的唯一来源：图标白名单、默认图标序列、命名池
// （D-N3 / D-N4）。档位身份的词汇表在 policy.go 的 builtinTierIdentities 与
// types.go 的常量里——两处分开是因为改图标是运营的事，改 id 是契约的事。
//
// 这些表是服务端权威：后台读面把白名单下发给前端做下拉选择，前端不得自己再抄一份
// （抄了就会在服务端升级时变成「UI 里有、保存时 422」）。

// tierIcons 是**唯一**被服务端接受的图标白名单（16 个 lucide 图标名）。
//
// 顺序 = 默认图标序列：第 n 项属于 rank n（TL0 = 芽 / TL1 = 叶 / TL2 = 屋 /
// TL3 = 山 / TL4 = 冠），前五个就是历史上五档在用的一套，所以存量站点的观感不变。
//
// 只接受字面量而不是「只要 lucide 里有就行」：图标名会进 HTML class，前端要靠它
// 静态可枚举才能做树摇；敞开接受任意字符串等于把渲染形状交给配置。
var tierIcons = []string{
	"sprout",
	"leaf",
	"home",
	"mountain",
	"crown",
	"star",
	"sun",
	"moon",
	"flame",
	"shield",
	"gem",
	"compass",
	"zap",
	"anchor",
	"medal",
	"trophy",
}

// MaxTierNameLength 是单语名称的字符（rune）上限。
const MaxTierNameLength = 32

// tierIDPattern 是新档 id 的语法：内置 id 或 tier_<数字>。
var tierIDPattern = regexp.MustCompile(`^(newcomer|member|resident|veteran|leader|tier_[0-9]+)$`)

// TierIcons 返回图标白名单（副本）。调用方不得原地修改。
func TierIcons() []string {
	return slices.Clone(tierIcons)
}

// IsTierIcon 报告图标名是否在白名单内。
func IsTierIcon(name string) bool {
	return slices.Contains(tierIcons, name)
}

// DefaultTierIcon 返回该 rank 的默认图标；超出默认序列时返回白名单首项。
//
// 序列只有 16 项而合法 rank 最多 9 个，正常不会越界；兜底是为了让「手搓的
// Policy」也不会返回空串（空串在 UI 上是「没有图标」，比回退更难排查）。
func DefaultTierIcon(rank Level) string {
	if rank >= LevelNewcomer && int(rank) < len(tierIcons) {
		return tierIcons[rank]
	}
	return tierIcons[0]
}

// ValidTierID 报告 id 是否符合 D-N6 的词汇表。
//
// newcomer 是 TL0 的保留 id：它语法合法，但**不能**出现在 tiers 里——TL0 的语义由
// trust.new_user_* 决定，让运营再配一档同名档位会产生两个「新人」。
func ValidTierID(id string) bool {
	return tierIDPattern.MatchString(id)
}

// tierSeq 解析 tier_<seq> 的数字后缀；不是新档 id 时返回 false。
func tierSeq(id string) (int, bool) {
	raw, ok := strings.CutPrefix(id, "tier_")
	if !ok || raw == "" {
		return 0, false
	}
	seq, err := strconv.Atoi(raw)
	if err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

// NewTierID 返回第 seq 个新档的 id。只由解码期派生，不接受客户端指定。
func NewTierID(seq int) string {
	return "tier_" + strconv.Itoa(seq)
}

// NamePool 是命名池：**编辑辅助**，不是存储字段（D-N3）。
//
// 只给运营做一键取名，配置里存的永远是运营最终确认的 name{zh,en}；所以增删命名池
// 不影响任何存量配置，也不需要 schema 版本。
type NamePool struct {
	Zh []string
	En []string
}

// TierNamePool 返回命名池（副本）。Zh 与 En 一一对应，同序。
func TierNamePool() NamePool {
	return NamePool{
		Zh: []string{"新手上路", "正式用户", "常驻用户", "资深用户", "活跃贡献者", "核心贡献者", "社区领袖", "传奇人物"},
		En: []string{"Newcomer", "Member", "Resident", "Veteran", "Contributor", "Core Contributor", "Leader", "Legend"},
	}
}
