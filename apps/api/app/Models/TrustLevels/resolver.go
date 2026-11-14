package trustlevels

import (
	"context"
	"encoding/json"
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
	// ruleEvaluators 为「一个用户」构造规则求值器。未注入时配了规则的档位一律不成立
	// （fail-closed），而不是忽略规则——那会把「忘了装配」变成「规则静默失效」。
	ruleEvaluators RuleEvaluatorFactory
	// earnables 返回全部扩展的可授予权限声明（含停用者，用于区分 inactive/unknown）。
	// 未注入时按「只有核心七键」处理。
	earnables EarnableSource
}

// RuleEvaluatorFactory 为一个用户构造规则求值器（装配层注入；测试用 fake）。
//
// 工厂而不是实例：求值器内部持「用户 ID + 条件解析器」，天生是一用户一实例。
// 返回 error 表示构造失败（池不可用等），调用方按 fail-closed 处理该档。
type RuleEvaluatorFactory func(ctx context.Context, userID int64) (RuleEvaluator, error)

// NewResolver 构造解析器；now 为 nil 时用 time.Now。
//
// 注册时间由调用方传入（R7）：identity 装载 Actor 时已经读过 users.created_at，
// 这里再查一次就是同一请求读两遍同一行，G 组预算从 4→5 会变成 4→6。
func NewResolver(activity ActivityReader, policy PolicySource) *Resolver {
	return &Resolver{activity: activity, policy: policy, now: time.Now}
}

// WithRuleEvaluators 注入规则求值器工厂（M2）。
//
// 不注入 = 规则档位一律不成立（fail-closed）。这条默认值很重要：`LevelOf`（纯函数、
// 无 ctx）与所有既有测试都走这条路，语义与「没有任何档位配规则」逐字节一致。
func (r *Resolver) WithRuleEvaluators(factory RuleEvaluatorFactory) *Resolver {
	if r != nil {
		r.ruleEvaluators = factory
	}
	return r
}

// WithEarnables 注入可挣得权限声明来源（M4a）。
//
// 不注入 = 只有核心七键（与 M1 行为一致）。注入后，配置里由插件声明为
// `trustLevelEarnable` 的键才会被授予；停用插件的键保留在配置里但不授予。
func (r *Resolver) WithEarnables(source EarnableSource) *Resolver {
	if r != nil {
		r.earnables = source
	}
	return r
}

// EarnableRegistry 返回当前的可挣得目录（后台读面与写期校验共用）。
func (r *Resolver) EarnableRegistry(ctx context.Context) EarnableRegistry {
	if r == nil || r.earnables == nil {
		return DefaultEarnableRegistry()
	}
	declarations, err := r.earnables(ctx)
	if err != nil {
		// 读不到声明 = 不知道插件键现在能不能给。只留核心键（fail-closed）：
		// 宁可少授予，也不要把一个已停用插件的键继续发出去。
		return DefaultEarnableRegistry()
	}
	return NewEarnableRegistry(declarations)
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
	policy, activity, judge, err := r.inputs(ctx, userID, kind)
	if err != nil || !judge {
		return 0, nil, err
	}
	evaluation := r.judge(ctx, userID, activity, createdAt, policy)
	// 运行期惰性（D24 修订）：配置里写了的键不一定现在能授予（插件可停用）。
	// FilterGranted 只筛**已挣得集合**，不动配置本身——插件恢复后自动重新生效。
	granted := r.EarnableRegistry(ctx).FilterGranted(policy.Earnable.Grants(evaluation.Level))
	return int(evaluation.Level), granted, nil
}

// Evaluation 是一次等级判定的完整结论。
//
// 它是给**本域内部消费者**（M4 的 `trust_level.changed` job、进度读面、验收脚本）用的：
// `TrustLevelSource.Grants` 依旧只回 (level, granted, err)，一个字节没改（D18）。
// 扩接口要动 Identity 的两个 loader 与全部 fake，收益为零。
type Evaluation struct {
	Level  Level
	TierID string
	// Incomplete 表示本次判定里有档位的规则**判不了**（查询失败 / 未声明 factKey /
	// 脏规则 / 缺少求值器）。此时那些档位按不成立处理（fail-closed）。
	//
	// 语义边界：`incomplete` 只是「结论可能偏低」，不是「结论无效」。所以它不影响
	// Grants 的返回值，只用于抑制副作用——M4 的 job 在 incomplete 时**不发**事件，
	// 否则一次 DB 抖动会让全站用户收到一条「等级变了」（而其实没变）。
	//
	// **HTTP 装载路径不发事件**，所以这个字段对装载路径没有意义；它只有 job 路径需要。
	Incomplete bool
	// Errors 是规则求值期的逐条问题（叶子级），进日志用。
	Errors []error
}

// Evaluate 现算一个用户的等级（含规则），并带回 incomplete 与逐条问题。
//
// 与 `Grants` 共用同一份判定实现（`levelOfWithRules`），差别只有返回值形状：
//   - 失败姿态一致：policy/activity 读失败 → 返回 error（整次未解析，不减扣、不发事件）；
//   - 规则失败**不**升级成 error：该档不成立 + `Incomplete=true`，其余照常判定。
func (r *Resolver) Evaluate(ctx context.Context, userID int64, kind identity.UserKind, createdAt time.Time) (Evaluation, error) {
	if r == nil || r.activity == nil || r.policy == nil {
		return Evaluation{}, ErrDisabled
	}
	policy, activity, shouldJudge, err := r.inputs(ctx, userID, kind)
	if err != nil {
		return Evaluation{}, err
	}
	if !shouldJudge {
		// 机器人不进等级判定（D25）：TL0、无规则、不算不完整。
		return Evaluation{Level: LevelNewcomer, TierID: NewcomerID}, nil
	}
	return r.judge(ctx, userID, activity, createdAt, policy), nil
}

// inputs 读取判定所需的两份输入，并回答「这个用户要不要参与判定」。
//
// 机器人不进等级判定（D25）：论坛限额层对 bot 有 IsBot() 旁路，如果 bot 也能挣到
// TL4，就会出现「不受限额 + 最高等级」的组合。空串按人类处理（零值语义与
// identity.UserKind 一致）。
func (r *Resolver) inputs(ctx context.Context, userID int64, kind identity.UserKind) (Policy, Activity, bool, error) {
	if kind != "" && kind != identity.UserKindHuman {
		return Policy{}, Activity{}, false, nil
	}
	policy, err := r.policy.Policy(ctx)
	if err != nil {
		return Policy{}, Activity{}, false, err
	}
	activity, err := r.activity.ActivityFor(ctx, userID)
	if err != nil {
		return Policy{}, Activity{}, false, err
	}
	return policy, activity, true, nil
}

// judge 是判定本体：两份输入已就绪，这里只决定「要不要构造求值器」并调唯一遍历。
func (r *Resolver) judge(ctx context.Context, userID int64, activity Activity, createdAt time.Time, policy Policy) Evaluation {
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	var evaluator RuleEvaluator
	// 只有真存在带规则的档位才构造求值器：无规则站点必须**零新增查询**（§6.1），
	// 而构造 conditions.Resolver 本身不查库、但后续 Prepare 会——所以这道闸必须在
	// 构造之前，不能靠「Prepare 时发现没叶子」来兜。
	if r.ruleEvaluators != nil && policy.HasRules() {
		if built, err := r.ruleEvaluators(ctx, userID); err == nil {
			evaluator = built
		}
	}
	level, incomplete, problems := levelOfWithRules(ctx, activity, accountAge(createdAt, now()), policy, evaluator)
	return Evaluation{
		Level:      level,
		TierID:     policy.TierID(level),
		Incomplete: incomplete,
		Errors:     problems,
	}
}

// NextTierConditions 返回「离下一档还差哪几项」的逐条双语句子。
//
// 只公开**扁平 AND**的逐条句子：`any` / `not` / 嵌套一律返回 nil（前端只显示
// satisfied/total 计数）。理由是可读性即正确性——把 `any` 的叶子逐条列出来，读者会
// 以为「满足其中一条就能升级」，而实际要满足整棵表达式。
//
// 只允许对**本人**调用（调用方在控制器里已经限定了 owner 视角）。
func (r *Resolver) NextTierConditions(ctx context.Context, userID int64, policy Policy, level Level) []TierCondition {
	if r == nil || r.ruleEvaluators == nil || !level.Valid() || level >= policy.LastRank() {
		return nil
	}
	next := level + 1
	rule := policy.RuleOf(next)
	if len(rule) == 0 {
		return nil
	}
	evaluator, err := r.ruleEvaluators(ctx, userID)
	if err != nil || evaluator == nil {
		return nil
	}
	// 单棵树的 Prepare：只有这一棵树要句子，不值得走合成根。
	evaluator.Prepare(ctx, []json.RawMessage{rule})
	return evaluator.LeafConditions(ctx, rule)
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
//
// 最高档的判据是 policy.LastRank()（active 档数）而不是内置的 LevelLeader：
// 运营删档/加档之后，「已满级」必须跟着配置走，钉死 4 会让 5 档站点永远显示
// 「还差一点到 TL5」。
func Progress(activity Activity, level Level, policy Policy) (metric string, current, threshold int64, ok bool) {
	if !level.Valid() || level >= policy.LastRank() {
		return "", 0, 0, false
	}
	next := level + 1
	requirement := policy.RequirementsOf(next)
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
