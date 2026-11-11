package trustlevels

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// 本文件是档位**展示元数据**的唯一来源：图标取值规则、默认图标序列、命名池
// （D-N3 / D-N4）。档位身份的词汇表在 policy.go 的 builtinTierIdentities 与
// types.go 的常量里——两处分开是因为改图标是运营的事，改 id 是契约的事。
//
// 这些表是服务端权威：后台读面把白名单下发给前端做下拉选择，前端不得自己再抄一份
// （抄了就会在服务端升级时变成「UI 里有、保存时 422」）。

// tierIcons 是**默认图标序列**，同时也是后台推荐下拉的选项集（16 个 lucide
// 图标名）。它不再是唯一的合法取值：运营还可以写入 `i-<collection>-<name>`
// 形式的全名图标（见 hostIconPattern）。
//
// 顺序 = 默认图标序列：第 n 项属于 rank n（TL0 = 芽 / TL1 = 叶 / TL2 = 屋 /
// TL3 = 山 / TL4 = 冠），前五个就是历史上五档在用的一套，所以存量站点的观感不变。
//
// 真实约束不是「静态可枚举」，而是**图标名必须能被前端 @nuxt/icon 的
// serverBundle 解析**：`i-lucide-foo` / `i-tabler-foo` 走 Nuxt Icon 的集合前缀
// 语法，只要集合（lucide / tabler）在依赖里、名字在该集合中存在，前端就能渲染。
// 白名单里的 16 个短名（无前缀）由前端映射到默认集合，同样是这一约束的特例。
// 不敞开接受任意字符串，是为了把「配置里出现根本不存在的图标名」挡在保存期，
// 而不是等到渲染期静默空缺。
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

// hostIconPattern 是 `i-<collection>-<name>` 形式的图标全名语法：集合限定为
// 前端已打包的 lucide / tabler，名字段为小写字母数字与连字符的段式组合。
// 真实约束见文件头：必须能被 @nuxt/icon serverBundle 解析。
var hostIconPattern = regexp.MustCompile(`^i-(lucide|tabler)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// MaxTierNameLength 是单语名称的字符（rune）上限。
const MaxTierNameLength = 32

// tierIDPattern 是新档 id 的语法：内置 id 或 tier_<数字>。
var tierIDPattern = regexp.MustCompile(`^(newcomer|member|resident|veteran|leader|tier_[0-9]+)$`)

// TierIcons 返回图标白名单（副本）。调用方不得原地修改。
func TierIcons() []string {
	return slices.Clone(tierIcons)
}

// IsTierIcon 报告图标名是否合法：16 个默认短名之一，或 i-(lucide|tabler)- 全名。
func IsTierIcon(name string) bool {
	if slices.Contains(tierIcons, name) {
		return true
	}
	return hostIconPattern.MatchString(name)
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
