package conditions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// Resolver 是 `ruleengine.Resolver` 的实现：把一个用户 + 一棵树的条件翻译成
// 「每种条件类型一次查询」。
//
// 为什么不是「每次 Resolve 打一条 SQL」：一棵树最多 32 个叶子，逐个查会变成 32
// 次往返；而同一个 type 的叶子共享同一条 SQL 形状（窗口用数组展开），一次就能
// 全部算完。`Prepare` 负责先把树上的叶子按 type 分组并各查一次，之后 `Eval`
// 逐叶子调 `Resolve` 只读 memo。
//
// 单条叶子也可以直接 `Resolve`（未 Prepare 时按单叶加载）：测试和「只有一个条件」
// 的调用方不必构造整棵树。
type Resolver struct {
	pool   *pgxpool.Pool
	userID int64

	memo      map[string]ruleengine.Value
	groupErrs map[string]error
	prepared  bool
	// now 是「今天」的来源。生产不注入（日界 = 进程本地日）；测试用它把日界钉在
	// 一个确定的时刻，否则跨时区断言会随真实时钟漂移。
	now func() time.Time
	// factKeys 是已声明的插件事实 key；未声明的 factKey 求值返 error（无法判定）。
	factKeys map[string]struct{}
}

// WithClock 覆盖「今天」的来源，仅用于测试。
func (r *Resolver) WithClock(now func() time.Time) *Resolver {
	r.now = now
	return r
}

// NewResolver 构造一个针对单个用户的解析器。pool 与 userID 都必须非空/为正。
// NewResolver 构造一个针对单个用户的解析器。
//
// `factKeys` 是**已声明的插件事实 key**（M1 传空）。它决定 `fact.count` 的语义：
// 未声明的 factKey 无法判定 → 求值返 error（引擎转 unknown）。M1 没有扩展声明，
// 所以任何 `fact.count` 叶子都会是 unknown——这是正确的，不是保守：M1 根本没有
// 插件条件可满足。
func NewResolver(pool *pgxpool.Pool, userID int64, factKeys ...string) (*Resolver, error) {
	if pool == nil {
		return nil, fmt.Errorf("conditions: pool 为 nil")
	}
	if userID <= 0 {
		return nil, fmt.Errorf("conditions: userID 必须为正，收到 %d", userID)
	}
	declared := make(map[string]struct{}, len(factKeys))
	for _, key := range factKeys {
		declared[key] = struct{}{}
	}
	return &Resolver{
		pool:      pool,
		userID:    userID,
		memo:      make(map[string]ruleengine.Value, 16),
		groupErrs: make(map[string]error, 4),
		factKeys:  declared,
	}, nil
}

// Prepare 收集树上的全部叶子，按 type 分组，每种类型查一次。
//
// 单个类型查询失败不会中断其它类型：错误按 type 记下，`Resolve` 在遇到该类型的
// 叶子时把它返回给引擎（引擎转成 unknown + 记错误）。这样「一个条件坏了」不会让
// 整次评估彻底失败，也不会让坏条件悄悄成立。
func (r *Resolver) Prepare(ctx context.Context, root ruleengine.Node) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("conditions: resolver 未初始化")
	}
	groups := make(map[string][]ruleengine.Leaf)
	collectLeaves(root, groups)
	types := make([]string, 0, len(groups))
	for typ := range groups {
		types = append(types, typ)
	}
	sort.Strings(types) // 稳定顺序，便于日志与测试
	for _, typ := range types {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := r.load(ctx, typ, groups[typ])
		if err != nil {
			r.groupErrs[typ] = err
			continue
		}
		for key, value := range values {
			r.memo[key] = value
		}
	}
	r.prepared = true
	return nil
}

// Resolve 实现 ruleengine.Resolver。
func (r *Resolver) Resolve(ctx context.Context, leaf ruleengine.Leaf) (ruleengine.Value, error) {
	if r == nil || r.pool == nil {
		return ruleengine.Value{}, fmt.Errorf("conditions: resolver 未初始化")
	}
	key, err := memoKey(leaf)
	if err != nil {
		return ruleengine.Value{}, err
	}
	if value, ok := r.memo[key]; ok {
		return value, nil
	}
	if groupErr, ok := r.groupErrs[leaf.Type]; ok {
		return ruleengine.Value{}, groupErr
	}
	// 未 Prepare 的叶子（单条件调用 / 测试）：按单叶加载一次并记忆。
	values, err := r.load(ctx, leaf.Type, []ruleengine.Leaf{leaf})
	if err != nil {
		return ruleengine.Value{}, err
	}
	for loadedKey, value := range values {
		r.memo[loadedKey] = value
	}
	value, ok := r.memo[key]
	if !ok {
		return ruleengine.Value{}, fmt.Errorf("conditions: 条件 %q 没有产出该参数组合的结果", leaf.Type)
	}
	return value, nil
}

func collectLeaves(node ruleengine.Node, groups map[string][]ruleengine.Leaf) {
	if node.IsGroup() {
		for _, child := range node.Children {
			collectLeaves(child, groups)
		}
		return
	}
	if node.Type == "" {
		return
	}
	groups[node.Type] = append(groups[node.Type], ruleengine.Leaf{Type: node.Type, Params: node.Params})
}

// load 按条件类型分派到具体加载器。返回值的 key 是 `memoKey` 的规范形式。
func (r *Resolver) load(ctx context.Context, typ string, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	switch typ {
	case KeyActivityCount:
		return r.loadActivityCount(ctx, leaves)
	case KeyActivityStreak:
		return r.loadActivityStreak(ctx, leaves)
	case KeyObjectEngagement:
		return r.loadObjectEngagement(ctx, leaves)
	case KeyObjectAuthoring:
		return r.loadObjectAuthoring(ctx, leaves)
	case KeyIdentityAccountAge, KeyIdentityEmailVerify, KeyIdentityOAuthLink, KeyIdentityRole, KeyIdentityAvatar, KeyIdentityProfile:
		return r.loadIdentity(ctx, typ, leaves)
	case KeyContentPinned, KeyContentHidden:
		return r.loadContent(ctx, typ, leaves)
	case KeyBadgeOwned:
		return r.loadBadgeOwned(ctx, leaves)
	case KeyFactCount:
		return r.loadFacts(ctx, leaves)
	default:
		return nil, fmt.Errorf("conditions: 未知条件 %q", typ)
	}
}

// memoKey 把叶子规范成缓存键。
//
// 用 `json.Marshal(map)` 而不是手拼字符串：encoding/json 对 map key 排序，
// 所以同一组参数无论调用方怎么排列都得到同一个键，不会出现「同一条件查两次」。
func memoKey(leaf ruleengine.Leaf) (string, error) {
	encoded, err := json.Marshal(leaf.Params)
	if err != nil {
		return "", fmt.Errorf("conditions: 条件 %q 的参数无法规范化：%w", leaf.Type, err)
	}
	return leaf.Type + "|" + string(encoded), nil
}

// valueKey 是加载器为单条结果生成 key 的辅助：参数由加载器从叶子重建，
// 必须与 `memoKey` 得到同一个字符串，所以统一走这里。
func valueKey(typ string, params map[string]any) (string, error) {
	return memoKey(ruleengine.Leaf{Type: typ, Params: params})
}

// --- 参数取值辅助 ---

// stringParam 读取字符串参数；缺失返回空串（必填性由 ruleengine.Validate 保证）。
func stringParam(leaf ruleengine.Leaf, name string) string {
	value, _ := leaf.Params[name].(string)
	return value
}

// intParam 读取整型参数。JSON 解出来是 float64，程序里构造的是 int / int64。
func intParam(leaf ruleengine.Leaf, name string) (int64, bool) {
	switch typed := leaf.Params[name].(type) {
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// windowDays 解析 `window` 参数：nil 表示全部时间，非 nil 是回溯天数。
//
// `kind=all` 带 days 的输入在 Validate 阶段已经被拒；这里再返回错误是防「未经
// Validate 直接 Eval」的路径——那种输入按 unknown 处理，不能当成 all 静默忽略。
func windowDays(leaf ruleengine.Leaf) (*int64, error) {
	raw, present := leaf.Params["window"]
	if !present || raw == nil {
		// 没有 window 的条件（如 content.pinned）等价于全部时间。
		return nil, nil
	}
	window, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("conditions: %q 的 window 不是对象", leaf.Type)
	}
	kind, _ := window["kind"].(string)
	switch kind {
	case ruleengine.WindowKindAll:
		if _, hasDays := window["days"]; hasDays {
			return nil, fmt.Errorf("conditions: %q 的 window.kind=all 不接受 days", leaf.Type)
		}
		return nil, nil
	case ruleengine.WindowKindRecentDays:
		days, ok := int64Of(window["days"])
		if !ok || days < 1 {
			return nil, fmt.Errorf("conditions: %q 的 window.days 非法", leaf.Type)
		}
		return &days, nil
	default:
		return nil, fmt.Errorf("conditions: %q 的 window.kind 未知：%q", leaf.Type, kind)
	}
}

func int64Of(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// nowTime 返回 resolver 视角的当前时刻（生产 = `time.Now()`，测试可用 `WithClock` 钉住）。
//
// 它是**日界的唯一来源**：`today()` 与所有「日历日差」折算都必须经过它。任何加载器
// 自己再调一次 `time.Now()`，同一次求值里就会出现两个「今天」，跨零点时前后矛盾。
func (r *Resolver) nowTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// today 返回「按 API 进程本地时区」的今天（`YYYY-MM-DD`）。
//
// 条件求值的日界以进程时区为准：`user_visits.visited_on` 是 `time.Now()` 写进去的，
// 任何用数据库 `CURRENT_DATE` 的比较都会在两者时区不一致时跨零点错一天。
func (r *Resolver) today() string {
	// 返回 `YYYY-MM-DD` 字符串而不是 time.Time：后者交给驱动后，Postgres 会按**会话
	// 时区**把 timestamptz 折成 date，等于把刚修掉的日界问题换个地方重现。
	return r.nowTime().Format("2006-01-02")
}

// countValue / boolValue 是两种 ValueKind 的构造器，避免各加载器手写 Kind。
func countValue(count int64) ruleengine.Value {
	return ruleengine.Value{Kind: ruleengine.ValueCount, Count: count}
}

func boolValue(value bool) ruleengine.Value {
	return ruleengine.Value{Kind: ruleengine.ValueBoolean, Bool: value}
}
