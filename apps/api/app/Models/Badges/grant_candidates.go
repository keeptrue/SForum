package badges

import (
	"context"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件是批量授予的「候选用户搜索」：后台在给手动徽章挑人时输入用户名，
// 服务端返回候选 + 是否已持有。
//
// 权限是 badge.view（**不是** user.view）：
// 这里只回答「哪些用户可以被授予这个徽章」，不暴露邮箱、角色、封禁理由等账号信息；
// 要求 user.view 会让「能管徽章但不能看用户目录」的运营角色用不了这个功能，
// 而它本来就是徽章功能的子操作。
//
// 只匹配 username / display_name，**绝不匹配 email**：邮箱是账号标识，用它做模糊
// 搜索等于把「输入 a@b.com 就能确认某人是否在站内」变成徽章后台的附带能力。

// MaxGrantCandidates 是候选列表的硬上限。
const MaxGrantCandidates = 20

// GrantCandidate 是一个可被授予的候选用户。
type GrantCandidate struct {
	UserID      int64
	Username    string
	DisplayName string
	// Status 是账号状态（active / disabled / banned），由前端提示风险，不在服务端过滤：
	// 「给已封禁用户补发一个历史活动徽章」是合法运营动作，服务端不该替他们决定。
	Status string
	// AlreadyGranted 让前端把已持有的人置灰，避免运营重复勾选后收到一堆 skipped。
	AlreadyGranted bool
}

// GrantCandidateInput 是候选搜索入参。
type GrantCandidateInput struct {
	// Query 为空时返回最近的候选（后台打开选择器、还没输入任何字符时的首屏）。
	Query   string
	BadgeID int64
	Limit   int
}

// SearchGrantCandidates 返回候选用户。
func (a *AdminService) SearchGrantCandidates(ctx context.Context, actor identity.Actor, input GrantCandidateInput) ([]GrantCandidate, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	if a == nil || a.store == nil {
		return nil, nil
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 || input.Limit > MaxGrantCandidates {
		input.Limit = MaxGrantCandidates
	}
	return a.store.SearchGrantCandidates(ctx, input)
}

// SearchGrantCandidates 按用户名/显示名模糊匹配用户，并标记是否已持有该徽章。
//
// 排序：用户名完全匹配优先 → 用户名升序。完全不匹配时也返回首批候选（见下），
// 因为「先看看站里有谁」是打开选择器时的第一反应。
func (s *PostgresStore) SearchGrantCandidates(ctx context.Context, input GrantCandidateInput) ([]GrantCandidate, error) {
	limit := input.Limit
	if limit <= 0 || limit > MaxGrantCandidates {
		limit = MaxGrantCandidates
	}
	// ILIKE 的通配符必须转义：运营输入 "a_b" 时 "_" 是单字符通配，会把 "axb" 也搜出来。
	pattern := "%" + escapeLikePattern(input.Query) + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT users.id, users.username, users.display_name, users.status,
		       ($2 > 0 AND EXISTS (
		           SELECT 1 FROM user_badges
		           WHERE user_badges.user_id = users.id AND user_badges.badge_id = $2
		       )) AS already_granted
		FROM users
		WHERE $1 = ''
		   OR users.username ILIKE $3 ESCAPE '\'
		   OR users.display_name ILIKE $3 ESCAPE '\'
		ORDER BY (lower(users.username) = lower($1)) DESC, users.username_lower, users.id
		LIMIT $4`, input.Query, input.BadgeID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search grant candidates: %w", err)
	}
	defer rows.Close()
	items := make([]GrantCandidate, 0, limit)
	for rows.Next() {
		var candidate GrantCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.Username, &candidate.DisplayName,
			&candidate.Status, &candidate.AlreadyGranted); err != nil {
			return nil, fmt.Errorf("scan grant candidate: %w", err)
		}
		items = append(items, candidate)
	}
	return items, rows.Err()
}

// UsernamesByIDs 批量取用户名（失败行回填用）。
func (s *PostgresStore) UsernamesByIDs(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return names, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, username FROM users WHERE id = ANY($1)`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("usernames by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan username: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}

// escapeLikePattern 转义 LIKE/ILIKE 的元字符（\ % _）。
//
// 反斜杠必须第一个替换，否则会把后面刚加上的转义符再转一次。
func escapeLikePattern(raw string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(raw)
}
