package badges

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// ResolveNotificationBadgeTarget 把徽章通知的目标解析成收件人可点击的路径。
//
// 目标固定是**查看者自己的资料页徽章 tab，并带上这枚徽章的 slug**：徽章是公开
// 荣誉，但点进去要能立刻看到「我拿到了什么」，公开目录页做不到这件事（目录页只
// 标 earned）。`badge` 查询参数让资料页直接展开这一枚的详情，而不是让用户在自己
// 的徽章墙里再找一次——「收到通知 → 找到那枚徽章」这一步没有产品价值。
//
// 三重可见性校验（fail-closed，任一不满足都返回 (false,"",nil) 而不是报错）：
//  1. 徽章仍启用（停用后公开面任何地方都不展示它，链接进去是空页）；
//  2. 该用户仍持有这条授予行（收回后通知不该还能跳到「我获得了」）；
//  3. 徽章所在等级仍启用（`topBadgeOrderSQL` 的语义把停用档当「不存在」，
//     资料页那枚徽章同样不渲染）。
//
// 不报错是刻意的：这是**读路径装饰**，一次目标解析失败只该让这一个链接消失，
// 不该把整个收件箱变成 500（与 Forum 的 `ResolveNotificationTarget` 同一姿态）。
func (s *PostgresStore) ResolveNotificationBadgeTarget(ctx context.Context, userID int64, badgeID int64) (bool, string, error) {
	if s == nil || userID <= 0 || badgeID <= 0 {
		return false, "", nil
	}
	var username, slug string
	err := s.pool.QueryRow(ctx, `
		SELECT users.username, badges.slug
		FROM users
		JOIN user_badges ON user_badges.user_id = users.id AND user_badges.badge_id = $2
		JOIN badges ON badges.id = user_badges.badge_id AND badges.enabled
		JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
		WHERE users.id = $1 AND users.status = 'active'`, userID, badgeID).Scan(&username, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("resolve badge notification target: %w", err)
	}
	// 用户名按路径段转义：站点的用户名规则目前是 [a-z0-9_-]，但这里不依赖它——
	// 一条脏数据不该让整条通知的链接指向别处。查询串交给 url.Values 编码：
	// slug 是运营可改的文本，手拼会让 `&`/`#`/空格把链接切坏。
	query := url.Values{"tab": {"badges"}, "badge": {slug}}
	return true, "/u/" + url.PathEscape(username) + "?" + query.Encode(), nil
}
