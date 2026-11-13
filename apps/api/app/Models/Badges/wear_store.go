package badges

import (
	"context"
	"errors"
	"fmt"
)

// 自由佩戴（M3）：用户可以从自己已获得的徽章里挑一枚常驻展示。
//
// 三条硬口径（改之前先读 knowledge/decisions/2026-11-13-badge-worn-display.md）：
//
//  1. **`top_badge_id` 的语义是「当前展示的徽章」**，`top_badge_manual` 表示它是不是
//     用户选的。两者必须同源写入，否则「佩戴了但重算又改回去」。
//  2. **排序只有一份实现**：`topBadgeOrderSQL`。佩戴/回落/可升级提示全部用它，
//     另写一份比较必然在运营调档位后与 chip 显示不一致。
//  3. **失效即回落，不留空洞**：徽章停用/收回/删除或等级停用后，展示位回落到自动
//     推导的下一枚，而不是变成 NULL（用户会看到 chip 消失，却不知道为什么）。
//  4. **hidden 同源写入**：「不佩戴」是第三种展示态（`top_badge_hidden = TRUE`），
//     隐藏 = `top_badge_id=NULL, top_badge_manual=FALSE, top_badge_hidden=TRUE` 一次
//     写齐（HideWornBadge）；重算对 hidden 行让路——top_badge_id 已是 NULL，回落
//     写入会让「什么都不戴」变成「偷偷替用户戴上」，而且没有任何报错。

// ErrBadgeNotOwned 表示用户并不持有那枚徽章（或它已停用/等级停用）。
//
// 与 ErrBadgeNotFound 分开：后者是「站内没有这个 slug」（运营视角的数据问题），
// 这里是「你有这个 slug 但没资格戴」，前端提示完全不同（「先获得它」vs「徽章不存在」）。
var ErrBadgeNotOwned = errors.New("badges: badge not owned")

// 展示模式取值（wire 上逐字使用）。
const (
	BadgeDisplayModeAuto   = "auto"
	BadgeDisplayModeManual = "manual"
	BadgeDisplayModeHidden = "hidden"
)

// WearStore 是「自由佩戴」的领域能力（*PostgresStore 实现）。
//
// 刻意**不**并进 Store 组合接口：佩戴只服务 HTTP 面的三条端点，并进去会逼
// 每个假实现（含插件侧的窄 fake）写三个它用不到的方法，而它们对「读目录/授予」
// 的正确性毫无帮助。Service 在调用时探测这个能力（见下面的 wearStore），
// 探测不到就上抛装配错误，而不是伪装成「你没持有这枚徽章」。
type WearStore interface {
	SetWornBadge(ctx context.Context, userID int64, slug string) error
	ClearWornBadge(ctx context.Context, userID int64) error
	HideWornBadge(ctx context.Context, userID int64) error
	DisplayBadge(ctx context.Context, userID int64) (BadgeDisplay, error)
}

// errWearStoreUnavailable 表示装配里没有佩戴能力（store 不是 *PostgresStore）。
//
// 这是装配错误，不是用户输入错误：上抛 500，不要折成 ErrBadgeNotOwned——
// 那会让「忘了接 store」表现为「你没获得过这枚徽章」，运维永远查不出来。
var errWearStoreUnavailable = errors.New("badges: wear store unavailable")

// BadgeDisplay 是 GET/PUT/DELETE /badges/worn 的响应体。
//
// `Worn` 与 `Automatic` 允许为 nil（没有任何可展示徽章）；`Upgrade` 只在
// 「用户选择比自动推导差」时非 nil——它不是错误，是提示。
//
// 三者都用 `BadgeRef` 而不是只给 slug：前端要立刻渲染「你现在可以换成 X」，
// 只有 slug 会迫使它再查一次徽章目录（多一次往返 + 一次可能的空目录）。
type BadgeDisplay struct {
	Mode      string    `json:"mode"`
	Worn      *BadgeRef `json:"worn"`
	Automatic *BadgeRef `json:"automatic"`
	Upgrade   *BadgeRef `json:"upgrade"`
}

// SetWornBadge 让用户佩戴一枚自己已获得的徽章。
//
// **一条 SQL 完成校验 + 写入**：校验（badge enabled + 等级 enabled + 该用户持有）
// 与写入分两条语句时，两个并发请求之间可能夹着一次收回，结果是把已经不该展示的
// 徽章写进展示位。CTE 让「查到的」与「写的」是同一个快照。
//
// 不满足任何一条即 ErrBadgeNotOwned（**不区分**是没持有还是已停用）：
// 对手方（普通用户）不需要知道「站内是否存在这枚徽章」，区分只会变成一个探测面。
//
// **佩戴 = 三列同源写入**：`top_badge_id = X, top_badge_manual = TRUE,
// top_badge_hidden = FALSE`，一条语句、一个快照。清 hidden 不能漏：`DisplayBadge`
// 先判 hidden 就 return，只写前两列会得到「200 + mode=hidden」的假成功——用户看到
// 「已佩戴」而展示位仍是「未佩戴徽章」，且没有任何报错。拆成两条语句同样不行：
// 两次写之间可能夹进一次隐藏或一次重算，结果取决于调度顺序而不是用户的选择。
func (s *PostgresStore) SetWornBadge(ctx context.Context, userID int64, slug string) error {
	if s == nil || userID <= 0 {
		return ErrBadgeNotOwned
	}
	normalized := normalizeSlug(slug)
	if normalized == "" {
		return ErrBadgeNotOwned
	}
	tag, err := s.pool.Exec(ctx, `
		WITH target AS (
			SELECT badges.id
			FROM badges
			JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
			JOIN user_badges ON user_badges.badge_id = badges.id AND user_badges.user_id = $1
			WHERE badges.slug = $2 AND badges.enabled
		)
		UPDATE users
		SET top_badge_id = (SELECT id FROM target),
		    top_badge_manual = TRUE,
		    top_badge_hidden = FALSE
		WHERE users.id = $1 AND EXISTS (SELECT 1 FROM target)`, userID, normalized)
	if err != nil {
		return fmt.Errorf("set worn badge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBadgeNotOwned
	}
	return nil
}

// ClearWornBadge 取消佩戴：清标记后立刻按排序重算，展示位不会留空洞。
func (s *PostgresStore) ClearWornBadge(ctx context.Context, userID int64) error {
	if s == nil || userID <= 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET top_badge_manual = FALSE, top_badge_hidden = FALSE WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("clear worn badge: %w", err)
	}
	// 先清标记再重算：顺序反了的话 RecomputeTopBadge 会看到 manual=TRUE 而拒绝覆盖，
	// 于是「取消佩戴」变成一个只改了标记、展示位没动的假操作。
	return s.RecomputeTopBadge(ctx, userID)
}

// HideWornBadge 让用户选择「什么都不戴」：展示位清空并标记 hidden。
//
// 与 SetWornBadge 的 CTE 同一条硬口径（第四条）：三列同源写入，不能只写
// top_badge_id = NULL——那会让重算立刻回落写入自动推导的最佳徽章，「什么都不戴」
// 变成一个点完按钮就失效的假操作。这里不需要校验用户是否持有徽章：隐藏对
// 「一面空徽章墙」同样是有意义的选择。
func (s *PostgresStore) HideWornBadge(ctx context.Context, userID int64) error {
	if s == nil || userID <= 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE users
		SET top_badge_id = NULL,
		    top_badge_manual = FALSE,
		    top_badge_hidden = TRUE
		WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("hide worn badge: %w", err)
	}
	return nil
}

// DisplayBadge 返回佩戴状态：当前展示 / 自动推导 / 是否可升级。
//
// 一次查询取回「该用户持有的全部可展示徽章 + 用 topBadgeOrderSQL 算出的名次」，
// 于是三件事出自同一个排序定义：
//   - Automatic = 名次 1（与 chip、`top_badge_id` 的自动推导完全一致：徽章停用、
//     等级停用、授予行被收回的行都不参与名次）；
//   - Worn = `users.top_badge_id` 指向的那一行（按 id 精确对，不按名次猜）；
//   - Upgrade = 手动模式下 `/u/...` 展示位不是名次 1（说明存在更好的徽章）。
//
// 在 Go 里另写一次「谁更好」的比较是这一节最容易被重构掉的东西：运营调档位后
// chip 与提示会分叉，而且两条路径都不会报错——所以名次一律由 SQL 的 ROW_NUMBER
// 按 `topBadgeOrderSQL` 给出。
//
// 脏数据（`top_badge_manual = TRUE` 但 `top_badge_id` 指向已停用/已删徽章）
// 在这里**按自动模式呈现**（mode=auto、worn=automatic）：chip 那边同样不显示那枚
// 无效徽章，报 manual 会让前端显示「佩戴中」而 chip 是空的。真正清标记发生在
// 下一次评估/重算（见 RecomputeTopBadge 的失效回落）。
func (s *PostgresStore) DisplayBadge(ctx context.Context, userID int64) (BadgeDisplay, error) {
	result := BadgeDisplay{Mode: BadgeDisplayModeAuto}
	if s == nil || userID <= 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT users.top_badge_id, users.top_badge_manual, users.top_badge_hidden,
		       -- 判别列：徽章**与等级**都必须有效。badges.id 只回答「徽章还在吗」
		       -- （停用/删除时变 NULL）；等级停用时是 t.key 变 NULL。只判别 badges.id
		       -- 会把等级停用的徽章当成有效候选——而 chip、top_badge_id 的自动推导
		       -- （RecomputeTopBadge）与 SetWornBadge 都按 INNER JOIN badge_tiers 的
		       -- 口径排除了它，于是面板会指向一枚「chip 不显示、佩戴 422、通知目标
		       -- 判不可见」的徽章。
		       -- 为什么不用 INNER JOIN 过滤：那会让「一枚可展示徽章都没有」的用户
		       -- 查不到任何行，top_badge_id / top_badge_manual 也跟着读不出来；保留
		       -- LEFT JOIN + 判别列，让「没有徽章」始终是一行全 NULL 而不是空结果集。
		       CASE WHEN t.key IS NULL THEN NULL ELSE badges.id END,
		       -- LEFT JOIN 对「没有徽章 / 徽章已停用 / 等级已停用」的行给出全 NULL；
		       -- 这里必须 COALESCE，否则 pgx 会拒绝把 NULL 扫进 string（cannot scan
		       -- NULL into *string），整个佩戴面板变成 500。判别「这行算不算候选」靠
		       -- 上一列的判别列是否为 NULL，不靠这几个字符串。
		       COALESCE(badges.slug, ''), COALESCE(badges.name, ''),
		       COALESCE(badges.tier, ''), COALESCE(badges.icon, ''),
		       `+badgeTierColumnsSQL+`,
		       -- 名次与判别列同口径：失效行对外返回 NULL 名次（它们按 t.position
		       -- NULLS LAST 排在最后，不挤占有效行的 1..k）。只看 badges.id 会让停用
		       -- 档位的徽章占住名次 1，automatic 与 upgrade 的比较随之错位。
		       CASE WHEN t.key IS NULL THEN NULL
		            ELSE ROW_NUMBER() OVER (ORDER BY `+topBadgeOrderSQL+`) END AS rank
		FROM users
		LEFT JOIN user_badges ON user_badges.user_id = users.id
		LEFT JOIN badges ON badges.id = user_badges.badge_id AND badges.enabled
		LEFT JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
		LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
		WHERE users.id = $1
		ORDER BY rank NULLS LAST`, userID)
	if err != nil {
		return BadgeDisplay{}, fmt.Errorf("load badge display: %w", err)
	}
	defer rows.Close()
	var topBadgeID *int64
	var manual bool
	var hidden bool
	var wornChoice *BadgeRef
	var wornChoiceRank int64
	var automatic *BadgeRef
	var automaticRank int64
	for rows.Next() {
		var rowTopBadgeID *int64
		var rowManual bool
		var rowHidden bool
		var badgeID *int64
		var ref BadgeRef
		var appearance TierAppearance
		var rank *int64
		if err := rows.Scan(&rowTopBadgeID, &rowManual, &rowHidden, &badgeID, &ref.Slug, &ref.Name, &ref.Tier, &ref.Icon,
			&appearance.NameZh, &appearance.NameEn, &appearance.Color, &appearance.Position, &rank); err != nil {
			return BadgeDisplay{}, fmt.Errorf("scan badge display: %w", err)
		}
		topBadgeID, manual, hidden = rowTopBadgeID, rowManual, rowHidden
		// 该用户没有任何可展示徽章：LEFT JOIN 给出一行全 NULL。
		if badgeID == nil || rank == nil {
			continue
		}
		ref.Appearance = appearance
		candidate := ref
		if automatic == nil {
			automatic = &candidate
			automaticRank = *rank
		}
		if topBadgeID != nil && *topBadgeID == *badgeID {
			wornChoice = &candidate
			wornChoiceRank = *rank
		}
	}
	if err := rows.Err(); err != nil {
		return BadgeDisplay{}, fmt.Errorf("load badge display: %w", err)
	}
	result.Automatic = automatic
	// hidden 模式：用户主动选择不佩戴。worn=nil（展示位确实是空的）、automatic
	// 照常推导（前端可以显示「你的最佳徽章是 X」）、upgrade=nil（没有佩戴就没有
	// 「升级」可言）。hidden=true 时 top_badge_id 必为 NULL（HideWornBadge 同源写入），
	// wornChoice 天然匹配不到任何行。
	if hidden {
		result.Mode = BadgeDisplayModeHidden
		return result, nil
	}
	if manual && wornChoice != nil {
		result.Mode = BadgeDisplayModeManual
		result.Worn = wornChoice
		// 只在存在更好的徽章时提示；equal（名次相同）不可能，因为名次唯一。
		if automatic != nil && wornChoiceRank > automaticRank {
			result.Upgrade = automatic
		}
		return result, nil
	}
	// 自动模式（含「manual 标记指向无效徽章」的回落）：展示位就是自动推导结果。
	result.Worn = automatic
	return result, nil
}

// wearStore 返回底层 store 的佩戴能力（没有则 nil）。
//
// 探测而不是构造时绑定：Service 的构造函数签名被装配与既有测试大量使用，
// 为一个可选能力加参数会让每个调用点都要传 nil。
func (s *Service) wearStore() WearStore {
	if s == nil || s.store == nil {
		return nil
	}
	wear, _ := s.store.(WearStore)
	return wear
}

// SetWornBadge 让当前用户佩戴一枚自己已获得的徽章。
//
// HTTP 层只依赖 Service（与其它徽章端点一致），不必知道 store 的具体类型；
// 「必须持有、必须启用」的判定全在 SetWornBadge 的那条 CTE 里，这里不做二次校验——
// 两处校验迟早分叉，而分叉后前端看到的是「接口说成功、展示位没变」。
func (s *Service) SetWornBadge(ctx context.Context, userID int64, slug string) error {
	wear := s.wearStore()
	if wear == nil {
		return errWearStoreUnavailable
	}
	return wear.SetWornBadge(ctx, userID, slug)
}

// ClearWornBadge 取消佩戴，展示位回落到自动推导的最佳徽章。
func (s *Service) ClearWornBadge(ctx context.Context, userID int64) error {
	wear := s.wearStore()
	if wear == nil {
		return errWearStoreUnavailable
	}
	return wear.ClearWornBadge(ctx, userID)
}

// HideWornBadge 让当前用户选择「什么都不戴」（hidden 展示态）。
func (s *Service) HideWornBadge(ctx context.Context, userID int64) error {
	wear := s.wearStore()
	if wear == nil {
		return errWearStoreUnavailable
	}
	return wear.HideWornBadge(ctx, userID)
}

// DisplayBadge 返回佩戴状态（当前展示 / 自动推导 / 是否可升级）。
func (s *Service) DisplayBadge(ctx context.Context, userID int64) (BadgeDisplay, error) {
	wear := s.wearStore()
	if wear == nil {
		return BadgeDisplay{}, errWearStoreUnavailable
	}
	return wear.DisplayBadge(ctx, userID)
}
