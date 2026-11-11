package trustlevels

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件是信任等级的**批量**读入口：给论坛列表/详情/评论的作者补等级徽章。
//
// 与 Resolver 的分工：Resolver 是「本人一次」的装载（每请求一次，喂 Actor 的已挣得
// 权限）；BatchResolver 是「一页作者一批」的读面装饰。两者共用同一个 Policy 与
// LevelOfAt，所以同一个人在任何入口拿到的 rank/颜色都不会分叉。
//
// 依赖方向不变：Identity → TrustLevels。这里引用的都是本包定义的窄接口，实现由
// 装配层提供（Badges 的 user_activity 读入口 + Identity 的 users 读入口）。

// UserAccount 是等级判定需要的**账号级**事实：注册时间与账号类型。
//
// 不直接复用 identity.Actor：Actor 还带着权限集合，批量读面不需要它，也不该为了
// 读一个 created_at 把权限装载整条链路跑一遍。
type UserAccount struct {
	CreatedAt time.Time
	Kind      identity.UserKind
}

// BatchActivityReader 批量读 user_activity。
//
// 返回的 map **只含真实存在的行**：调用方据此区分「没有统计行」与「统计全 0」。
// 单调方法 QueryRow 的 N 次调用不满足本接口——实现必须用一条 SQL 覆盖整批。
type BatchActivityReader interface {
	ActivitiesFor(ctx context.Context, userIDs []int64) (map[int64]Activity, error)
}

// UserAccountReader 批量读 users 的 created_at / kind。缺失的 id 不出现在 map 里。
type UserAccountReader interface {
	UserAccounts(ctx context.Context, userIDs []int64) (map[int64]UserAccount, error)
}

// AuthorLevel 是一个作者的有效等级：rank + 稳定身份 + 有效色。
//
// 只含展示所需的三项：progress / bottleneck / 行为计数一律不出现——like_given_count
// 与 bookmark_count 是刻意不公开的窥探面（D1）。
type AuthorLevel struct {
	Level     Level
	LevelSlug string
	Color     string
}

// BatchResolver 把「一页作者」折算成有效等级。
type BatchResolver struct {
	activity BatchActivityReader
	users    UserAccountReader
	policy   PolicySource
	now      func() time.Time
}

// NewBatchResolver 构造批量解析器；now 为 nil 时用 time.Now。
func NewBatchResolver(activity BatchActivityReader, users UserAccountReader, policy PolicySource) *BatchResolver {
	return &BatchResolver{activity: activity, users: users, policy: policy, now: time.Now}
}

// WithClock 注入时钟（测试用）。返回自身以便链式装配。
func (r *BatchResolver) WithClock(now func() time.Time) *BatchResolver {
	if r != nil && now != nil {
		r.now = now
	}
	return r
}

// AuthorLevels 返回 userIDs 里**能算出等级**的作者映射。
//
// 查询次数固定为两次（users 一次、user_activity 一次），与作者数无关；ids 先去重，
// 所以「一页 20 条同一作者」也只查一次。
//
// 失败姿态（与 Resolver 相反的方向）：本方法只对装饰面负责，错误原样上抛，由调用方
// fail-open 成「不带等级」。产品面关闭时 policy.Policy 返回 ErrDisabled，此时
// **一次数据查询都不会发生**——开关关闭的请求不该为徽章多打两条 SQL。
//
// 缺席语义（逐条）：users 行不存在（作者已删）或 user_activity 行不存在时不产出
// 条目。后者是契约要求：没有统计行 = 这个作者还没有可判定的等级轨迹，宁可不发徽章，
// 也不发一个「看起来像 TL0」的推测值。
func (r *BatchResolver) AuthorLevels(ctx context.Context, userIDs []int64) (map[int64]AuthorLevel, error) {
	if r == nil || r.activity == nil || r.users == nil || r.policy == nil {
		return nil, ErrDisabled
	}
	ids := uniquePositiveIDs(userIDs)
	if len(ids) == 0 {
		return map[int64]AuthorLevel{}, nil
	}
	// D31：产品开关在**取任何数据之前**短路（Policy 实现读 features.trust_levels）。
	policy, err := r.policy.Policy(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := r.users.UserAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	activities, err := r.activity.ActivitiesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	levels := make(map[int64]AuthorLevel, len(ids))
	for _, id := range ids {
		account, ok := accounts[id]
		if !ok {
			continue
		}
		// 机器人不进等级判定（D25，与 Resolver.Grants 同口径）；空串按人类处理。
		if account.Kind != "" && account.Kind != identity.UserKindHuman {
			continue
		}
		activity, ok := activities[id]
		if !ok {
			continue
		}
		// r.now 由构造器/WithClock 保证非 nil（裸字面量不是受支持的构造方式）。
		level := LevelOfAt(activity, account.CreatedAt, r.now(), policy)
		levels[id] = AuthorLevel{
			Level:     level,
			LevelSlug: policy.TierID(level),
			Color:     policy.ColorOf(level),
		}
	}
	return levels, nil
}

// uniquePositiveIDs 去重并剔除非正 id，保持首次出现的顺序（便于测试与日志对齐）。
//
// **多层去重是刻意的**：Forum 装饰层、AuthorTrustLevelReader、Badges/Identity 的批量
// SQL 入口与本方法各有一份近似实现（各包依赖方向不同，无法共享）。改任一处需要同批
// 检查另外三处：`Models/Forum/author_trust_level.go`、`Models/Badges/activity_store_batch.go`、
// `Models/Identity/user_accounts.go`。
func uniquePositiveIDs(userIDs []int64) []int64 {
	seen := make(map[int64]bool, len(userIDs))
	out := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
