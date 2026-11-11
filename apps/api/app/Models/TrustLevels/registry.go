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
