package trustlevels

import (
	"context"
	"fmt"
	"slices"
	"sort"
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
			// **不再按白名单硬拒**（D24 修订，M4a）。原因是一次插件停用会让整份
			// `trust.levels` 解码失败 → 全站掉回默认策略：那是灾难性失败，不是保守。
			//
			// 现在的姿态是「解码保留 + 运行期惰性 + 写期拒绝」：
			//   - 运行期：未声明键不授予（`EarnableRegistry` 里 state != valid）；
			//   - 写期：`PUT` 对从未被声明过的键报 422 `earnable_not_declared`。
			// 结构性判据（空键）留在这里，因为它与「谁声明了」无关。
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

// EarnableState 是一个可挣得权限键的当前状态（D22：**不用 orphan**——那个词在既有
// i18n 里是另一种含义，混用会让 unknown 的提示写成「等插件启用」）。
const (
	// EarnableStateValid：核心七键，或某个**已启用**扩展声明为 trustLevelEarnable。
	EarnableStateValid = "valid"
	// EarnableStateInactive：曾被声明，但声明它的扩展已停用（或仅安装未启用）。
	// 配置保留、不授予——「先停用插件、稍后修配置」是合规路径，不能因为停用就拒存。
	EarnableStateInactive = "inactive"
	// EarnableStateUnknown：没有任何扩展声明过，也不在核心白名单里。
	// 语义与既有的 `earnable.orphan.*` 文案对齐（读面保持不动）。
	EarnableStateUnknown = "unknown"
)

// 写期拒绝的**变体**（不进 BACKEND_CODES：对外仍是 `earnable_not_declared` 一个码，
// 前端按变体分支渲染不同修法）。两种成因的修法相反，合并会让运营做错事：
//   - plugin_disabled → 等插件启用（或恢复插件）；
//   - undeclared      → 把这枚勾选删掉。
const (
	EarnableVariantPluginDisabled = "plugin_disabled"
	EarnableVariantUndeclared     = "undeclared"
)

// EarnableDeclaration 是一条「某扩展声明了某权限可被等级授予」的事实。
//
// **包含停用扩展**：区分 `inactive`（曾声明、扩展停用）与 `unknown`（从未声明）
// 全靠它。只传已启用的声明，两者会被抹平成同一个状态。
type EarnableDeclaration struct {
	Key         string
	ExtensionID string
	// Enabled 表示声明它的扩展当前已启用（enabled）。仅安装/停用的声明不算。
	Enabled bool
}

// EarnableSource 返回全部扩展的可授予权限声明（含停用者）。
//
// 由 bootstrap 注入（复用 5s 快照，不新建第二份轮询）。未注入时按「只有核心七键」处理。
type EarnableSource func(ctx context.Context) ([]EarnableDeclaration, error)

// EarnableRegistry 是一次解析后的可挣得目录：core ∪ 已声明，按状态区分。
//
// 它回答两个问题：
//   - 这个键现在**能不能授予**（`Grantable`）——运行期惰性，非 valid 一律不授予；
//   - 后台该怎么显示（`State`）——valid / inactive / unknown。
type EarnableRegistry struct {
	valid    map[string]struct{}
	inactive map[string]struct{}
}

// NewEarnableRegistry 解析声明。
//
// 三条口径：
//   - **核心键永远 valid**：插件对同名键的声明被忽略（核心说了算，不允许插件改写核心
//     语义）——这就是「不得与核心权限键冲突」的落点；
//   - 有任一**已启用**扩展声明 → valid；
//   - 只有停用扩展声明 → inactive（不是 unknown：它曾经是合法的，修法是等启用）。
func NewEarnableRegistry(declarations []EarnableDeclaration) EarnableRegistry {
	registry := EarnableRegistry{
		valid:    make(map[string]struct{}, len(earnableWhitelist)),
		inactive: make(map[string]struct{}, 8),
	}
	for _, key := range earnableWhitelist {
		registry.valid[key] = struct{}{}
	}
	disabledOnly := make(map[string]struct{}, 8)
	for _, declaration := range declarations {
		key := strings.TrimSpace(declaration.Key)
		if key == "" {
			continue
		}
		if IsWhitelisted(key) {
			// 核心键：插件不能通过声明改变它的状态（已是 valid）。
			continue
		}
		if declaration.Enabled {
			registry.valid[key] = struct{}{}
			delete(disabledOnly, key)
			continue
		}
		if _, enabled := registry.valid[key]; enabled {
			continue
		}
		disabledOnly[key] = struct{}{}
	}
	for key := range disabledOnly {
		if _, enabled := registry.valid[key]; enabled {
			continue
		}
		registry.inactive[key] = struct{}{}
	}
	return registry
}

// State 返回该键的当前状态。
func (r EarnableRegistry) State(key string) string {
	if _, ok := r.valid[key]; ok {
		return EarnableStateValid
	}
	if _, ok := r.inactive[key]; ok {
		return EarnableStateInactive
	}
	return EarnableStateUnknown
}

// Grantable 报告该键现在能否被授予（只有 valid 可以）。
func (r EarnableRegistry) Grantable(key string) bool {
	_, ok := r.valid[key]
	return ok
}

// Keys 返回当前可授予的键（核心键在前，随后是已声明插件键，各自保序）。
func (r EarnableRegistry) Keys() []string {
	out := make([]string, 0, len(r.valid))
	for _, key := range earnableWhitelist {
		if _, ok := r.valid[key]; ok {
			out = append(out, key)
		}
	}
	extra := make([]string, 0, len(r.valid))
	for key := range r.valid {
		if IsWhitelisted(key) {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// FilterGranted 从「配置里写了什么」里筛出「现在真的可以授予什么」。
//
// 运行期惰性：inactive / unknown 的键**保留在配置里**（不删库、不改写运营的选择），
// 但不出现在已挣得集合里。下一次装载插件恢复后它们自动重新生效。
func (r EarnableRegistry) FilterGranted(keys []string) []string {
	if len(keys) == 0 {
		return keys
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if r.Grantable(key) {
			out = append(out, key)
		}
	}
	return out
}

// DefaultEarnableRegistry 是「没有插件」的目录（只有核心七键）。
func DefaultEarnableRegistry() EarnableRegistry { return NewEarnableRegistry(nil) }

// DeclaredPermissionKeys 返回「当前可被信任等级授予」的键全集（核心 ∪ 已声明）。
//
// 保留这个函数是给「没有声明来源」的调用方用的：等价于只取核心七键，与 M1 行为一致。
func DeclaredPermissionKeys() []string { return DefaultEarnableRegistry().Keys() }

// ValidateEarnableDeclared 校验一组可挣得键**现在**是否都被声明覆盖。
//
// 只报 `undeclared`：inactive 的键允许保存（先停用插件、稍后修配置是合规路径），
// 只是不授予。这就是「解码保留 + 运行期惰性 + 写期拒绝」里的「写期拒绝」。
func ValidateEarnableDeclared(keys []string, registry EarnableRegistry) []FieldError {
	details := make([]FieldError, 0, 1)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if registry.State(key) != EarnableStateUnknown {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		details = append(details, FieldError{
			Field: "earnable",
			Code:  CodeEarnableNotDeclared,
			Params: map[string]any{
				"permission": key,
				"variant":    EarnableVariantUndeclared,
			},
			Reason: fmt.Sprintf("%q 没有被任何扩展声明为「可被等级授予」；请取消这一项勾选", key),
		})
	}
	return details
}

// InactiveEarnableKeys 返回这组键里处于 inactive 的那些（后台读面标状态用）。
func InactiveEarnableKeys(keys []string, registry EarnableRegistry) []string {
	out := make([]string, 0, 2)
	for _, key := range keys {
		if registry.State(key) == EarnableStateInactive {
			out = append(out, key)
		}
	}
	return out
}
