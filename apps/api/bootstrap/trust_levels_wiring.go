package bootstrap

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// trustLevelPolicySource 把 options 的两个键组合成一份等级策略。
//
// 单一来源（R6）：阈值/限额来自 trust.levels 单键 JSON，新人窗口来自既有的
// trust.new_user_days。JSON 里没有 newUserDays 字段，所以这里必须显式覆盖。
type trustLevelPolicySource struct {
	options *options.Service
}

func (s trustLevelPolicySource) Policy(ctx context.Context) (trustlevels.Policy, error) {
	if s.options == nil {
		return trustlevels.Policy{}, trustlevels.ErrDisabled
	}
	// D31：产品开关在**调用 resolver 之前**短路。关掉时连等级阈值键都不读，
	// 更不会去读 user_activity —— 性能 A/B 才有干净的 Δp95。
	flags, err := s.options.FeatureFlags(ctx)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	if !flags.TrustLevels {
		return trustlevels.Policy{}, trustlevels.ErrDisabled
	}
	raw, err := s.options.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	trust, err := s.options.TrustPolicy(ctx)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	if trust.NewUserDays >= 0 {
		policy.NewUserDays = trust.NewUserDays
	}
	return policy, nil
}

// trustLevelActivityReader 复用 Badges 的 user_activity 读入口（D18）。
//
// 这里是**编译期检查**：下面每个字段都按名字引用 Badges.ActivityCounters，
// 统计层改字段名时这个文件直接编译失败，而不是静默读错列。
type trustLevelActivityReader struct {
	store *badges.PostgresActivityStore
}

func (r trustLevelActivityReader) ActivityFor(ctx context.Context, userID int64) (trustlevels.Activity, error) {
	if r.store == nil {
		return trustlevels.Activity{}, nil
	}
	counters, err := r.store.GetCounters(ctx, userID)
	if err != nil {
		return trustlevels.Activity{}, err
	}
	return trustlevels.Activity{
		TopicCount:        counters.TopicCount,
		CommentCount:      counters.CommentCount,
		LikeReceivedCount: counters.LikeReceivedCount,
		LikeGivenCount:    counters.LikeGivenCount,
		BookmarkCount:     counters.BookmarkCount,
		DaysVisited:       counters.DaysVisited,
	}, nil
}

// newTrustLevelSource 是**全仓唯一**构造信任等级来源的地方。
//
// 装配纪律（M8 断言项）：有且只有这一处调用 trustlevels.NewResolver，且在 HTTP
// server 启动前完成注入；不是 lazy 首次请求才装。所有消费者（identity store、
// Host Command 后端、用户字段 store）共享同一个实例，否则同一个人会在两条路径
// 上得到两个答案（D22）。
func newTrustLevelSource(
	optionsService *options.Service,
	activity *badges.PostgresActivityStore,
	pool *pgxpool.Pool,
	factKeys func() []string,
) *trustlevels.Resolver {
	return trustlevels.NewResolver(
		trustLevelActivityReader{store: activity},
		trustLevelPolicySource{options: optionsService},
	).WithRuleEvaluators(conditionsRuleEvaluatorFactory(pool, factKeys))
}

// conditionsRuleEvaluatorFactory 把「一个用户 + 条件解析器」装成 M2 的求值器。
//
// 每个用户一个 `conditions.Resolver`（它带 userID），所以这里必须是工厂而不是实例。
// `factKeys` 是**闭包**（D20）：事实目录在 domains 阶段才建好，infra 阶段只能先拿到
// 一个读口。未绑定时返回空集 → `fact.count` 叶子求值返 error → 引擎转 unknown →
// 该档不成立（fail-closed），与「未声明即无法判定」的既有语义一致。
//
// 构造失败（池不可用/用户 ID 非法）返回 error：`Resolver.judge` 会按「没有求值器」
// 处理 —— 配了规则的档位一律不成立，而不是忽略规则放行。
func conditionsRuleEvaluatorFactory(
	pool *pgxpool.Pool,
	factKeys func() []string,
) trustlevels.RuleEvaluatorFactory {
	return func(ctx context.Context, userID int64) (trustlevels.RuleEvaluator, error) {
		conditionResolver, err := conditions.NewResolver(pool, userID, factKeys()...)
		if err != nil {
			return nil, err
		}
		return trustlevels.NewRuleEvaluator(conditionResolver), nil
	}
}

// deferredFactKeys 是「已声明插件事实 key」的延迟绑定读口（D20）。
//
// 与 `deferredBackfillEnqueuer` 同款：infra 阶段 resolver 已经造好，而事实目录要等
// extensions service 就绪（domains 阶段）。未 Bind 时返回 nil 切片 = 没有插件声明，
// 于是 `fact.count` 恒 unknown（fail-closed），不会误判成立。
type deferredFactKeys struct {
	source func() []string
}

// Bind 注入事实 key 来源（domains 阶段调用）。
func (d *deferredFactKeys) Bind(source func() []string) {
	if d != nil {
		d.source = source
	}
}

// Keys 返回当前已声明的事实 key；未绑定返回 nil。
func (d *deferredFactKeys) Keys() []string {
	if d == nil || d.source == nil {
		return nil
	}
	return d.source()
}

// 编译期确认实现满足 identity 侧的窄接口（D18）。
var _ identity.TrustLevelSource = (*trustlevels.Resolver)(nil)

// trustLevelClock 目前只用于测试注入；生产用 Resolver 的默认时钟。
func trustLevelClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

// badgesRuleImpactEstimator 把徽章的聚合估算器适配成信任等级的窄接口（D17）。
//
// 两个域的 `RuleImpact` 形状逐字段相同，这里仍做一次**显式转换**而不是共用类型：
// 共用类型等于让 `Models/TrustLevels` 反向依赖 `Models/Badges` 的内部结构，下一次
// 有人给徽章那份加字段，信任等级的契约就会跟着悄悄变。
//
// 估算 SQL 不复制第二份：`*badges.PostgresStore.EstimateRuleImpact` 已经是「按条件
// 聚合数人数」的唯一实现（AND 取最严、OR 取和、NOT 取补，逐条写清了口径）。
type badgesRuleImpactEstimator struct {
	store *badges.PostgresStore
}

// EstimateRuleImpact 实现 `trustlevels.RuleImpactEstimator`。
func (a badgesRuleImpactEstimator) EstimateRuleImpact(ctx context.Context, rule ruleengine.Node) (trustlevels.RuleImpact, error) {
	if a.store == nil {
		return trustlevels.RuleImpact{}, errImpactEstimatorUnavailable
	}
	impact, err := a.store.EstimateRuleImpact(ctx, rule)
	if err != nil {
		return trustlevels.RuleImpact{}, err
	}
	return trustlevels.RuleImpact{
		Estimated:             impact.Estimated,
		Method:                impact.Method,
		Partial:               impact.Partial,
		UnsupportedConditions: impact.UnsupportedConditions,
	}, nil
}

// errImpactEstimatorUnavailable 是装配缺失时的显式失败（绝不返回「0 人」）。
var errImpactEstimatorUnavailable = errors.New("bootstrap: rule impact estimator has no store")
