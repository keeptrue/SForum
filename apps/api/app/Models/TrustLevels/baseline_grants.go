package trustlevels

import "slices"

// baselineGrants 是「减法门」（Z3-b″）的键表：默认安装的 member 基线里有这两个键，
// 但产品口径是「TL0 不能开新主题、不能传附件」，所以装载期从 TL0 的有效权限里扣掉。
//
// 为什么是**代码常量**而不是 `DefaultPolicy().Earnable`：
//   - D17 规定默认 Earnable 是空集（「TL2 解锁 topic.create」对默认安装是零行为变化的
//     假功能），而这条门的语义是「从基线里扣」，不是「由等级授予」；
//   - 混进 Earnable 会让 `IsEarnable` / `UnlockLevel` / `All()` 把这两键报成「已配置为
//     可挣得」，与 D33 的 J-01/J-02 断言形状冲突，也会让后台「按档勾选」界面凭空
//     多出两条运营没配过的行。
//
// 与 `earnableWhitelist` 是两张表，**不要合并**：白名单回答「哪些键允许被等级授予」
// （安全边界），这张表回答「哪些键 TL0 必须被扣」（产品口径）。
var baselineGrants = []string{
	"topic.create",
	"attachment.upload",
}

// BaselineGrants 返回减法门的键（副本）。调用方不得原地修改。
func BaselineGrants() []string {
	return slices.Clone(baselineGrants)
}

// BaselineGrantsFor 返回该 rank 装载期**应被扣除**的基线键；rank ≥ 1（任一 active
// 档位）返回 nil。
//
// 判据只看 rank，不看「是不是某个具名档位」：TL1 起就算成员这条产品口径与档位
// 数量无关——加档、删档、停用都不该改变基线门。
//
// 下界放在这里而不是 identity 里：identity 不能 import 本包（D18），
// 让 identity 自己写一个 `level < 1` 就是把 `LevelMember` 的数值抄成第二份。
// 装载器只负责「照单扣除」，不解释「谁是成员」。
func (r *Resolver) BaselineGrantsFor(level int) []string {
	if level >= int(LevelMember) {
		return nil
	}
	return BaselineGrants()
}
