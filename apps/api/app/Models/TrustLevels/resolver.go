package trustlevels

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// ErrDisabled 表示信任等级产品面被关闭（features.trust_levels=disabled）。
//
// 它不是故障：装载方必须把它当作「未解析」处理（已挣得集合为空、限额回落到本刀
// 之前的年龄两态），与 D9 的 F 组断言一一对应。
//
// 必须是**同一个**错误值（别名，不是同文案的第二个 errors.New）：identity 侧用
// errors.Is 识别它，两个独立值会让关闭态掉进「err != nil」分支，变成每请求一条
// warn，而 D9 要求关闭态静默。
var ErrDisabled = identity.ErrTrustLevelsDisabled

// ActivityReader 读用户行为统计。
//
// 生产实现由装配层提供，直接复用 Badges 的 user_activity 读入口
// （badges.PostgresActivityStore.GetCounters），不在这里写第二份 SQL。
// 行缺失必须返回零值 Activity 而不是错误（从未产生统计事件 = 全 0）。
type ActivityReader interface {
	ActivityFor(ctx context.Context, userID int64) (Activity, error)
}

// PolicySource 提供当前阈值策略。实现由装配层提供（读 options 的 trust.levels
// 单键并严格解码）。产品面关闭时返回 ErrDisabled。
type PolicySource interface {
	Policy(ctx context.Context) (Policy, error)
}

// Resolver 把「活动统计 + 阈值策略」折算成等级与已挣得权限。
//
// 它是装载期的一次性计算：每个已认证请求一次（identity.LoadActor 内），
// 不在 Can() 里。签名刻意与 identity.TrustLevelSource 同形（level int），
// 这样装配层直接注入即可，identity 不需要 import 本包（D18）。
type Resolver struct {
	activity ActivityReader
	policy   PolicySource
	now      func() time.Time
}

// NewResolver 构造解析器；now 为 nil 时用 time.Now。
//
// 注册时间由调用方传入（R7）：identity 装载 Actor 时已经读过 users.created_at，
// 这里再查一次就是同一请求读两遍同一行，G 组预算从 4→5 会变成 4→6。
func NewResolver(activity ActivityReader, policy PolicySource) *Resolver {
	return &Resolver{activity: activity, policy: policy, now: time.Now}
}

// WithClock 注入时钟（测试用）。返回自身以便链式装配。
func (r *Resolver) WithClock(now func() time.Time) *Resolver {
	if r != nil && now != nil {
		r.now = now
	}
	return r
}

// Grants 返回该用户当前的信任等级与已挣得权限键。
//
// 失败姿态（D7）：任何一步失败都返回错误，装载方据此把 Actor 标成「未解析」——
// 已挣得集合为空、限额回落到本刀之前的语义。绝不在失败时按高等级放行。
// 产品面关闭返回 ErrDisabled（装载方静默保持未解析，不写 warn）。
func (r *Resolver) Grants(ctx context.Context, userID int64, kind identity.UserKind, createdAt time.Time) (int, []string, error) {
	if r == nil || r.activity == nil || r.policy == nil {
		return 0, nil, ErrDisabled
	}
	// 机器人不进等级判定（D25）：论坛限额层对 bot 有 IsBot() 旁路，如果 bot 也能
	// 挣到 TL4，就会出现「不受限额 + 最高等级」的组合。
	// 空串按人类处理（零值语义与 identity.UserKind 一致）。
	if kind != "" && kind != identity.UserKindHuman {
		return 0, nil, nil
	}
	policy, err := r.policy.Policy(ctx)
	if err != nil {
		return 0, nil, err
	}
	activity, err := r.activity.ActivityFor(ctx, userID)
	if err != nil {
		return 0, nil, err
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	level := LevelOfAt(activity, createdAt, now(), policy)
	return int(level), policy.Earnable.Grants(level), nil
}

// Limits 返回该等级的限额配置；nil 表示沿用站点默认。
func (p Policy) LimitsFor(level Level) LevelLimits {
	if !level.Valid() {
		return LevelLimits{}
	}
	return p.Limits[level]
}

// Activity 返回该用户的行为统计。
//
// 只供「本人还差多少」计算使用：`like_given_count` / `bookmark_count` 是刻意不公开
// 的窥探面，任何对**他人**的响应都不得包含它们（D1 隐私边界）。
func (r *Resolver) Activity(ctx context.Context, userID int64) (Activity, error) {
	if r == nil || r.activity == nil {
		return Activity{}, ErrDisabled
	}
	return r.activity.ActivityFor(ctx, userID)
}

// Progress 返回「离下一档还差什么」：取第一项未达标的指标作为瓶颈。
//
// 返回 false 表示已经最高档或指标全达标。只允许对**本人**调用。
func Progress(activity Activity, level Level, policy Policy) (metric string, current, threshold int64, ok bool) {
	if !level.Valid() || level >= LevelLeader {
		return "", 0, 0, false
	}
	next := level + 1
	requirement := policy.Levels[next]
	for _, candidate := range Metrics {
		want, known := requirement.Value(candidate)
		if !known || want <= 0 {
			continue
		}
		have, _ := activity.Value(candidate)
		if have >= want {
			continue
		}
		return candidate, have, want, true
	}
	return "", 0, 0, false
}
