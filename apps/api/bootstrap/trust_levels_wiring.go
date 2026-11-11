package bootstrap

import (
	"context"
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
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
func newTrustLevelSource(optionsService *options.Service, activity *badges.PostgresActivityStore) *trustlevels.Resolver {
	return trustlevels.NewResolver(
		trustLevelActivityReader{store: activity},
		trustLevelPolicySource{options: optionsService},
	)
}

// 编译期确认实现满足 identity 侧的窄接口（D18）。
var _ identity.TrustLevelSource = (*trustlevels.Resolver)(nil)

// trustLevelClock 目前只用于测试注入；生产用 Resolver 的默认时钟。
func trustLevelClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}
