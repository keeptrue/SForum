package trustlevels

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// TrustLevelChangedEvent 是 `trust_level.changed` 的载荷形状。
//
// 字段名就是事件目录里的 PayloadFields（camelCase），改名字要同时改
// `Support/Events/catalog.go` 的 definition，否则订阅方拿到的键会悄悄变。
type TrustLevelChangedEvent struct {
	UserID     int64     `json:"userId"`
	FromLevel  int       `json:"fromLevel"`
	ToLevel    int       `json:"toLevel"`
	FromTierID string    `json:"fromTierId"`
	ToTierID   string    `json:"toTierId"`
	ComputedAt time.Time `json:"computedAt"`
}

// ledgerCASAttempts 是账本 CAS 冲突的最大尝试次数。
//
// 依据：同一用户的并发写只来自「单用户 job」与「批量重算」两条路径，正常最多两次
// 撞车（各自重试一次就收敛）；给到 3 次足以覆盖批量重算与 job 同时开跑，
// 又不至于让一个持续被改的用户卡住 worker。
const ledgerCASAttempts = 3

// RecomputeOutcome 是一次重算的结论（job 的日志/测试都要它）。
type RecomputeOutcome struct {
	Evaluation Evaluation
	// Previous/HadPrevious 是账本里上一次的完整求值；首见用户 HadPrevious=false。
	Previous    LedgerEntry
	HadPrevious bool
	// LedgerWritten 表示本次写入了账本。
	LedgerWritten bool
	// EventEmitted 表示本次发出了 `trust_level.changed`。
	EventEmitted bool
	// Skipped 是「什么都没做」的原因（产品关闭 / incomplete / 同级），进日志用。
	Skipped string
}

// Recomputer 把「现算 → 与账本比较 → 变化才写账本 + 发事件」收敛成一处。
//
// 三条不可动的判据（M4b-2 的骨头）：
//  1. **incomplete → 既不写账本也不发事件**。求值判不了时结论只是「可能偏低」，
//     拿它覆盖账本等于把真实等级擦掉，下一次对比就再也发不出真正的变更事件；
//     发事件则是「一次 DB 抖动 → 全站收到等级变了」的事件洪水。
//  2. **账本不参与授权**。这里从不把账本值当等级对外返回；等级只来自 Evaluate。
//  3. **首见用户只落账本、不发事件**：没有比较基准就没有「变更」。否则功能上线后
//     每个用户第一次重算都会收到一条「0→N 升级」的假事件。
type Recomputer struct {
	resolver  *Resolver
	ledger    LedgerStore
	inputs    UserTrustInputReader
	publisher appevents.Publisher
	logger    *slog.Logger
	now       func() time.Time
}

func NewRecomputer(
	resolver *Resolver,
	ledger LedgerStore,
	inputs UserTrustInputReader,
	publisher appevents.Publisher,
) *Recomputer {
	return &Recomputer{
		resolver:  resolver,
		ledger:    ledger,
		inputs:    inputs,
		publisher: appevents.EnsurePublisher(publisher),
		now:       time.Now,
	}
}

// WithLogger 注入日志（incomplete 与跳过原因要留痕，否则线上只能靠猜）。
func (r *Recomputer) WithLogger(logger *slog.Logger) *Recomputer {
	if r != nil {
		r.logger = logger
	}
	return r
}

// WithClock 注入时钟（测试用）。
func (r *Recomputer) WithClock(now func() time.Time) *Recomputer {
	if r != nil && now != nil {
		r.now = now
	}
	return r
}

// RecomputeUser 现算一个用户的等级并按三条判据决定是否落账本/发事件。
func (r *Recomputer) RecomputeUser(ctx context.Context, userID int64) (RecomputeOutcome, error) {
	if r == nil || r.resolver == nil || r.ledger == nil || r.inputs == nil {
		return RecomputeOutcome{}, errors.New("trustlevels: recomputer is not configured")
	}
	if userID <= 0 {
		return RecomputeOutcome{}, nil
	}
	outcome := RecomputeOutcome{}
	// 求值 → 读账本 → 比较 → 写账本 → 发事件，整段进重试循环（P3-10）。
	//
	// 为什么**重新求值**而不只是重读账本：求值输入（活动统计、策略）确实与账本无关，
	// 但「输入独立」不等于「结果在重试窗口内不变」。冲突意味着另一个重算刚刚写过账本，
	// 它的求值可能发生在更晚的活动统计上；此时沿用本轮的旧 evaluation，就会把**更旧**
	// 的结果写回账本，并发出方向相反的事件（实测反例：赢家写 level=4/leader，
	// 落败方用旧求值写回 level=3/veteran 并发 `4→3` 降级事件——正是 P3-10 要消灭的东西）。
	// 所以每一轮都重读 TrustInput + 重跑 Evaluate；`next.ComputedAt` 用同一轮的时刻，
	// 保证「computed_at 声称的求值时刻」与真实求值一致（stale sweep 按 computed_at 判陈旧，
	// 不一致会让脏写推迟下一次纠正）。
	for attempt := 0; attempt < ledgerCASAttempts; attempt++ {
		input, err := r.inputs.TrustInput(ctx, userID)
		if err != nil {
			return RecomputeOutcome{}, err
		}
		kind := identity.UserKindHuman
		if identity.ValidUserKind(identity.UserKind(input.Kind)) {
			kind = identity.UserKind(input.Kind)
		}
		evaluation, err := r.resolver.Evaluate(ctx, userID, kind, input.CreatedAt)
		if err != nil {
			// 产品面关闭不是故障：静默跳过，不写账本也不发事件（等级不可知时不动账本）。
			if errors.Is(err, ErrDisabled) {
				return RecomputeOutcome{Skipped: "disabled"}, nil
			}
			return RecomputeOutcome{}, err
		}
		outcome.Evaluation = evaluation
		// 判据 1：incomplete 闸。放在读账本之前——连读都不必读，避免「读了就有人顺手写」。
		if evaluation.Incomplete {
			outcome.Skipped = "incomplete"
			if r.logger != nil {
				r.logger.Warn("trust level recompute skipped: evaluation incomplete",
					"user_id", userID, "problems", len(evaluation.Errors))
			}
			return outcome, nil
		}

		previous, found, err := r.ledger.LoadLedger(ctx, userID)
		if err != nil {
			return RecomputeOutcome{}, err
		}
		outcome.Previous = previous
		outcome.HadPrevious = found

		next := LedgerEntry{
			Level:      int(evaluation.Level),
			TierID:     evaluation.TierID,
			ComputedAt: r.now().UTC(),
		}
		if found && previous.Level == next.Level && previous.TierID == next.TierID {
			// 同级同档：既不写账本（computed_at 保持上次完整求值时刻）也不发事件。
			outcome.Skipped = "unchanged"
			return outcome, nil
		}
		expectedRevision := int64(0)
		if found {
			expectedRevision = previous.Revision
		}
		applied, err := r.ledger.SaveLedgerCAS(ctx, userID, expectedRevision, next)
		if err != nil {
			return RecomputeOutcome{}, err
		}
		if !applied {
			// 并发冲突：账本已被别的重算改过。回到循环开头重新求值 + 重读基准，
			// 绝不带旧 previous 或旧 evaluation 发事件。
			continue
		}
		outcome.LedgerWritten = true
		// 判据 3：首见只落账本。没有比较基准就不是「变更」。
		if !found {
			outcome.Skipped = "first-sighting"
			return outcome, nil
		}
		// 判据 2：账本只做变更检测。事件内容来自 previous（账本）与本次求值，不反过来。
		r.publish(ctx, userID, previous, next)
		outcome.EventEmitted = true
		return outcome, nil
	}
	// 重试用尽：账本已由并发方写对（最终一致），本次跳过事件。
	// 宁可少一条事件，也不发一条 FromLevel 已经过期的错序事件——后者会让下游
	// 重复发通知/徽章，而少发的那条下一次重算会因为「同级」自然收敛。
	outcome.Skipped = "ledger-conflict"
	if r.logger != nil {
		r.logger.Warn("trust level recompute gave up after ledger revision conflicts",
			"user_id", userID, "attempts", ledgerCASAttempts)
	}
	return outcome, nil
}

func (r *Recomputer) publish(ctx context.Context, userID int64, previous, next LedgerEntry) {
	event := TrustLevelChangedEvent{
		UserID:     userID,
		FromLevel:  previous.Level,
		ToLevel:    next.Level,
		FromTierID: previous.TierID,
		ToTierID:   next.TierID,
		ComputedAt: next.ComputedAt,
	}
	// observe 通道 fail-open：事件投递失败不改变「账本已更新」这件事，也不重试。
	result := r.publisher.Emit(ctx, appevents.Envelope{
		Name:          appevents.TrustLevelChanged,
		Kind:          appevents.KindObserve,
		ActorUserID:   0,
		ResourceType:  "user",
		ResourceID:    strconv.FormatInt(userID, 10),
		CorrelationID: appevents.NewID(),
		Payload: map[string]any{
			"userId":     event.UserID,
			"fromLevel":  event.FromLevel,
			"toLevel":    event.ToLevel,
			"fromTierId": event.FromTierID,
			"toTierId":   event.ToTierID,
			"computedAt": event.ComputedAt.Format(time.RFC3339Nano),
		},
		OccurredAt: event.ComputedAt,
	})
	if !result.OK && r.logger != nil {
		r.logger.Warn("emit trust level changed failed", "user_id", userID, "reason", result.Reason)
	}
}
