package hostapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// TrustLevelQuerySource 是两条信任等级 HostQuery（M4b-3）的读面。
//
// 为什么是「计算」而不是视图查询：见
// knowledge/decisions/2026-11-14-hostquery-computed-definitions.md。
// 一句话——档位目录只存在于 web_options 的 trust.levels 单键 JSON 里，
// 而单用户等级必须现算（账本 user_trust_levels 是快照，不是权威）。
//
// 它返回**未归一化**的行（字段名 → 值）：归一化、投影、分页、schema 校验都由
// 执行器统一做，这一层不许自己造响应。
type TrustLevelQuerySource interface {
	TrustLevelCatalog(ctx context.Context) ([]map[string]any, error)
	UserTrustLevel(ctx context.Context, userID int64) ([]map[string]any, error)
}

// trustLevelQuerySource 是 TrustLevelQuerySource 的生产实现。
//
// 依赖只有三样，全部来自装配层：现算 resolver（M8 唯一实例）、options 读面、
// 以及一个**只读** users 状态列（公开投影要排除非 active 用户）。
type trustLevelQuerySource struct {
	resolver *trustlevels.Resolver
	options  *options.Service
	pool     *pgxpool.Pool
}

// NewTrustLevelQuerySource 构造读面。resolver 必须与 HTTP 装载路径同一个实例，
// 否则同一个人会在两条路径上得到两个答案（D22）。
func NewTrustLevelQuerySource(
	resolver *trustlevels.Resolver,
	optionService *options.Service,
	pool *pgxpool.Pool,
) TrustLevelQuerySource {
	return &trustLevelQuerySource{resolver: resolver, options: optionService, pool: pool}
}

// WithTrustLevelQuerySource 把读面注入查询运行时（bootstrap 装配用）。
func WithTrustLevelQuerySource(source TrustLevelQuerySource) ProtocolV2QueryRuntimeOption {
	return protocolV2QueryRuntimeOptionFunc(func(options *protocolV2QueryRuntimeOptions) {
		options.trustLevels = source
	})
}

// TrustLevelCatalog 返回档位目录：TL1..TLn，来自 policy 解码。
//
// 公开面**结构上**就没有 rule 与 limits：rule 是判定逻辑（会泄露判定意图），
// limits 是限额策略（属于站点运营口径，不由这个读面下发）。这里不重新实现裁剪，
// 而是只挑三个展示字段 + 六个数值门槛。
func (s *trustLevelQuerySource) TrustLevelCatalog(ctx context.Context) ([]map[string]any, error) {
	if s == nil || s.options == nil {
		return nil, errors.New("trust level catalog source is not configured")
	}
	raw, err := s.options.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return nil, fmt.Errorf("read trust level catalog: %w", err)
	}
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("decode trust level catalog: %w", err)
	}
	lastRank := policy.LastRank()
	rows := make([]map[string]any, 0, lastRank)
	for rank := trustlevels.LevelMember; rank <= lastRank; rank++ {
		name := policy.DisplayNameOf(rank)
		requirements := policy.RequirementsOf(rank)
		rows = append(rows, map[string]any{
			"tier_id":             policy.TierID(rank),
			"rank":                int64(rank),
			"name_zh":             name.Zh,
			"name_en":             name.En,
			"icon":                policy.IconOf(rank),
			"color":               policy.ColorOf(rank),
			"days_visited":        requirements.DaysVisited,
			"topic_count":         requirements.TopicCount,
			"comment_count":       requirements.CommentCount,
			"like_received_count": requirements.LikeReceivedCount,
			"like_given_count":    requirements.LikeGivenCount,
			"bookmark_count":      requirements.BookmarkCount,
		})
	}
	return rows, nil
}

// UserTrustLevel 现算一个用户的等级。
//
// 三条口径：
//  1. **现算**：走 `Resolver.Evaluate()`；**不读** `user_trust_levels`。账本只做
//     变更检测，读它当权威就是双真相（见 migration 里的 NOT authoritative 注释）。
//  2. **公开投影**：只服务 active 用户。不存在或非 active → 返回空行（与其它
//     公开 by_id 查询的 denied 形态一致：不是 403，而是「不在公开面里」）。
//  3. **incomplete 要能区分**：判不了时 `level`/`tier_id` 为 null 且 `state`
//     取 "incomplete"，**不能**退化成 0——「等级是 0」与「求不出来」是两件事。
func (s *trustLevelQuerySource) UserTrustLevel(ctx context.Context, userID int64) ([]map[string]any, error) {
	if s == nil || s.resolver == nil || s.pool == nil {
		return nil, errors.New("user trust level source is not configured")
	}
	if userID <= 0 {
		return nil, nil
	}
	var status, kind string
	var createdAt time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT status, kind, created_at FROM users WHERE id = $1`, userID).
		Scan(&status, &kind, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load user trust level input: %w", err)
	}
	if status != "active" {
		return nil, nil
	}
	userKind := identity.UserKindHuman
	if identity.ValidUserKind(identity.UserKind(kind)) {
		userKind = identity.UserKind(kind)
	}
	evaluation, err := s.resolver.Evaluate(ctx, userID, userKind, createdAt)
	if err != nil {
		if errors.Is(err, trustlevels.ErrDisabled) {
			// 产品面关闭：不是错误，是「读不到等级」。公开面返回一条 state=disabled，
			// 让调用方能区分「功能没开」与「等级是 0」。
			return []map[string]any{{
				"user_id": userID,
				"level":   nil,
				"tier_id": nil,
				"state":   "disabled",
			}}, nil
		}
		return nil, err
	}
	row := map[string]any{
		"user_id": userID,
		"level":   nil,
		"tier_id": nil,
		"state":   "resolved",
	}
	if evaluation.Incomplete {
		row["state"] = "incomplete"
		// 结论只是「可能偏低」：公开面不回一个看起来权威的数字。
		return []map[string]any{row}, nil
	}
	row["level"] = int64(evaluation.Level)
	row["tier_id"] = evaluation.TierID
	return []map[string]any{row}, nil
}
