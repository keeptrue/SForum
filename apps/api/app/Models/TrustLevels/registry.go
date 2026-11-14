package trustlevels

import (
	"fmt"
	"slices"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// Earnable 是「rank → 恰好解锁的既有权限键」映射（键是 rank，TL1 起）。
//
// 键用 rank 而不是身份 id：已挣得集合每次装载都要重算，rank 是判定结果，直接用
// 它查表最省一次 id→rank 的折算；retired 档位不进这张表（判定跳过）。
//
// 默认是**空集**（D17）：`SeedMemberPermissions` 已经给了默认成员全部非管理内容键，
// 所以「TL2 解锁 topic.create」对默认安装是零行为变化的假功能。运营在后台按档
// 勾选后才真正生效。
//
// 可挣得范围是**正白名单**（D24）：只有下面 7 个内容创建/管理自己内容的键可以被
// 授予。负面过滤（「不是 *.manage 就行」）挡不住 `post.edit_any` 这类有特权但
// 无审计的键——它会绕过作者编辑窗口且不写审计（revisionOrigin 只比 ID）。
type Earnable map[Level][]string

// earnableWhitelist 是**唯一**可被信任等级授予的权限键集合。
//
// 硬编码字面量而不是从目录里按规则筛：规则会漏（`user.permission_override` 就因为
// 后缀是下划线而不是点漏过一次），字面量漏不了。
var earnableWhitelist = []string{
	"topic.create",
	"post.create",
	"attachment.upload",
	"topic.edit_own",
	"post.edit_own",
	"topic.delete_own",
	"post.delete_own",
}

// EarnableWhitelist 返回可挣得权限键的正白名单（副本）。
func EarnableWhitelist() []string {
	return slices.Clone(earnableWhitelist)
}

// IsWhitelisted 报告某权限键是否在正白名单内。
func IsWhitelisted(permission string) bool {
	return slices.Contains(earnableWhitelist, permission)
}

// Permissions 返回该等级**恰好**解锁的键（不含更低等级），稳定顺序。
func (e Earnable) Permissions(level Level) []string {
	if !level.Valid() {
		return nil
	}
	return slices.Clone(e[level])
}

// Grants 返回该等级（含更低等级）已挣得的权限键，去重且稳定顺序。
func (e Earnable) Grants(level Level) []string {
	if !level.Valid() || level <= LevelNewcomer {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for current := LevelMember; current <= level; current++ {
		for _, key := range e[current] {
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// All 返回**到 lastRank 为止**全部已配置的可挣得键（去重、稳定顺序），供结构断言
// 与后台汇总使用。
//
// 上界必须由调用方传 policy.LastRank()：这里曾经写死 Grants(LevelLeader)，于是 8 档
// 站点上第 5–8 档配的键会从 All() 里静默消失——不报错，只是后台和断言都看不见它们。
func (e Earnable) All(lastRank Level) []string {
	return e.Grants(lastRank)
}

// IsEarnable 报告某权限键是否被配置为可挣得。
func (e Earnable) IsEarnable(permission string) bool {
	for level := LevelMember; level <= MaxLevel; level++ {
		if slices.Contains(e[level], permission) {
			return true
		}
	}
	return false
}

// UnlockLevel 返回某权限键首次被解锁的等级。
func (e Earnable) UnlockLevel(permission string) (Level, bool) {
	for level := LevelMember; level <= MaxLevel; level++ {
		if slices.Contains(e[level], permission) {
			return level, true
		}
	}
	return LevelNewcomer, false
}

// Validate 校验整份 earnable 配置。任何一条不满足都返回错误，调用方（options 归一化
// 与后台写接口）必须拒绝保存，而不是「忽略这一条」——静默丢弃正是 T16 的坑。
//
// 判据是**正白名单**（D24），不是「不是管理类就行」：后者挡不住 post.edit_any 这类
// 有特权、无审计的键。
func (e Earnable) Validate() error {
	for level := LevelMember; level <= MaxLevel; level++ {
		for _, key := range e[level] {
			if key == "" {
				return fmt.Errorf("trustlevels: rank TL%d 的可挣得权限键为空", level)
			}
			if !IsWhitelisted(key) {
				return fmt.Errorf("trustlevels: %q 不在可挣得白名单内", key)
			}
		}
	}
	return nil
}

// EligiblePermissions 返回后台**可勾选**的权限键白名单。
//
// 就是正白名单与真实目录的交集（顺序跟白名单）。这是 D15 真正要防的面：
// 白名单本身不得包含管理类键或父权限键——只校验「已配置的 earnable」不够，
// 白名单漏配时运营勾一下就能打开整组能力。
func EligiblePermissions() []string {
	known := make(map[string]bool, len(identity.SeedPermissions))
	for _, permission := range identity.SeedPermissions {
		known[permission.Key] = true
	}
	out := make([]string, 0, len(earnableWhitelist))
	for _, key := range earnableWhitelist {
		if !known[key] {
			continue
		}
		out = append(out, key)
	}
	return out
}

// Normalize 返回去掉空串与重复项后的副本，保持等级内声明顺序。
func (e Earnable) Normalize() Earnable {
	out := make(Earnable, len(e))
	for level, keys := range e {
		if !level.Valid() || level == LevelNewcomer {
			continue
		}
		seen := make(map[string]bool, len(keys))
		cleaned := make([]string, 0, len(keys))
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			cleaned = append(cleaned, key)
		}
		if len(cleaned) > 0 {
			out[level] = cleaned
		}
	}
	return out
}

// DeclaredPermissionKeys 返回「当前可被信任等级授予」的键全集。
//
// M1 的形态就是正白名单：插件声明（manifest `permissionDefinitions[].trustLevelEarnable`）
// 要等 M4.1/M4.2 才接进来。写成函数而不是直接读 `earnableWhitelist`，是为了让调用方
// 从第一天起就走「已声明集合」这个概念——M4 换实现时不用改调用点。
func DeclaredPermissionKeys() []string { return EarnableWhitelist() }

// ValidateEarnableDeclared 校验一组可挣得键是否都被**已声明**集合覆盖。
//
// 它对应 `earnable_not_declared`（M4.2 的写期拒绝）：解码期**不**做这件事（见
// config.go 里那条码的注释——一次插件停用会让整份配置解码失败，全站等级退回默认
// 策略，那是灾难性失败）。所以判据的形状在这里、调用时机在写路径。
//
// M1 的已声明集合 = 核心七键，因此这条码在当前版本**经由 HTTP 不可达**：解码期的
// `earnable_not_whitelisted` 会先拒绝同一批输入。这不是遗漏，而是 M4.2 的切换点
// ——那一步会把解码期的硬拒改成「保留 + 运行期过滤」，这条码才开始接手。
//
// **M4.2 的接手条件（可执行，别只留在对话里）**：一旦 `config.go` 的
// `validateEarnableInto` 不再因 `!IsWhitelisted(key)` 拒绝，就必须在
// `Http/Controllers/TrustLevels/rule_controller_test.go` 里新增一条 HTTP 断言——
// PUT 一份 `earnable` 含未声明键的配置，期望 422 且
// `details.errors[].reasonCode == "earnable_not_declared"`，并同步把本条注释里
// 「M1 经由 HTTP 不可达」改成「已接通」。**没加那条断言 = 这个码永久不可达**，
// 而码表看上去完全正常（只在读码表的人眼里它像已经实现了）。
func ValidateEarnableDeclared(keys []string, declared []string) []FieldError {
	known := make(map[string]struct{}, len(declared))
	for _, key := range declared {
		known[key] = struct{}{}
	}
	details := make([]FieldError, 0, 1)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := known[key]; ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		details = append(details, FieldError{
			Field:  "earnable",
			Code:   CodeEarnableNotDeclared,
			Params: map[string]any{"permission": key},
			Reason: fmt.Sprintf("%q 没有被任何已启用插件声明，不能被等级授予", key),
		})
	}
	return details
}
