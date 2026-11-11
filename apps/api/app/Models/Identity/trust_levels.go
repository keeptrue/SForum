package identity

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrTrustLevelsDisabled 表示信任等级产品面被关闭（features.trust_levels=disabled）。
//
// 它不是故障：装载方必须把它当作「未解析」处理——已挣得集合为空、限额回落到本刀
// 之前的年龄两态。identity 不 import Options / TrustLevels（D18），所以哨兵值放在
// 这里，由 TrustLevels 侧的实现返回。
var ErrTrustLevelsDisabled = errors.New("identity: trust levels disabled")

// TrustLevelSource 把「用户行为统计 + 阈值策略」折算成信任等级与已挣得权限键。
//
// 形态由 D18 固定：identity 只声明窄接口，TrustLevels 实现，bootstrap 注入。
// Identity 因此不 import TrustLevels / Badges / Options（三者都 import identity）。
//
// 未注入时（测试、CLI、部分装配路径）Actor 保持「未解析」：没有已挣得权限，
// 与信任等级功能上线之前逐字节等价。
type TrustLevelSource interface {
	// Grants 返回该用户当前的信任等级与已挣得权限键。
	//
	// kind 与 createdAt 由装载方传入：两者都来自 identity 已经读过的那一行 users，
	// 再让实现自己查一遍就是同一请求读两次（R7）。
	Grants(ctx context.Context, userID int64, kind UserKind, createdAt time.Time) (level int, earned []string, err error)

	// BaselineGrantsFor 返回该等级装载期必须从有效权限里**扣除**的基线键，
	// rank ≥ 1 返回 nil（Z3-b″ 减法门）。
	//
	// 这里的 1 锚的是 **rank**（TL1 起），不是某个具名档位：运营加档、插档、停档
	// 都不改变基线门——「一过新人就是成员」是产品口径，与档数无关。
	//
	// 键表与「谁是成员」的等级下界都留在 TrustLevels 一侧：identity 不能 import
	// 本包（D18），放在这里才不会出现第二份白名单和第二份 `LevelMember` 数值。
	// 装载器只负责照单扣除，并叠加「未解析 / bot / 显式 allow」三条不扣判据。
	BaselineGrantsFor(level int) []string
}

// resolveTrustGrants 是两个装载器（LoadActor 的池路径与 LoadEffectiveActorTx 的
// 事务路径）共用的调用点：未注入 / 产品关闭 / 读失败一律返回 ok=false。
//
// 两个 loader 必须走同一套失败姿态，否则「HTTP 路径按等级放行、插件命令路径不放行」
// 就是两个答案（D22）。
func resolveTrustGrants(ctx context.Context, userID int64, kind UserKind, createdAt time.Time, trust TrustLevelSource) (level int, earned []string, ok bool) {
	if trust == nil {
		return 0, nil, false
	}
	level, earned, err := trust.Grants(ctx, userID, kind, createdAt)
	switch {
	case errors.Is(err, ErrTrustLevelsDisabled):
		// 产品面关闭：静默保持未解析（F 组判据），不写 warn。
		return 0, nil, false
	case err != nil:
		slog.Warn("identity: resolve trust level failed", "userId", userID, "error", err)
		return 0, nil, false
	}
	return level, earned, true
}

// TrustLevelFor 返回指定用户当前的信任等级与是否已解析。
//
// 它给**展示**用（资料页等级卡），不返回任何行为计数：`like_given_count` 与
// `bookmark_count` 是刻意不公开的窥探面，而它们恰好可能是瓶颈 metric。
func (s *PostgresStore) TrustLevelFor(ctx context.Context, userID int64) (int, bool, error) {
	if s == nil || s.pool == nil || userID <= 0 {
		return 0, false, nil
	}
	var createdAt time.Time
	var kindRaw string
	if err := s.pool.QueryRow(ctx, `SELECT created_at, kind FROM users WHERE id = $1`, userID).
		Scan(&createdAt, &kindRaw); err != nil {
		return 0, false, nil
	}
	kind := UserKindHuman
	if ValidUserKind(UserKind(kindRaw)) {
		kind = UserKind(kindRaw)
	}
	level, _, ok := resolveTrustGrants(ctx, userID, kind, createdAt, s.trustLevels)
	if !ok {
		return 0, false, nil
	}
	return level, true, nil
}
