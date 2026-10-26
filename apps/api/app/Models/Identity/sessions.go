package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// CreateSession 登记一条新会话/设备到 user_sessions。
// sid 由 server 在 Begin 时用 32 字节 crypto/rand 生成，是稳定的 opaque 标识（非 cookie 凭证），
// 与 cookie session id 解耦：cookie id 每 24h 轮换但 sid 不变，设备列表不会因此分裂。
// 该方法签名直接满足 authsession.SessionStore 接口（结构化匹配）。
//
// 用 ON CONFLICT (sid) DO NOTHING 而非 DO UPDATE：sid 碰撞属于异常情况（32 字节随机
// 实际不会碰撞），若真的碰撞说明 sid 生成出了问题，此时覆盖旧行会导致 user_id 不匹配
// （新用户的 sid 指向旧用户的行），后续 IsSessionRevoked 查不到而误判已下线。碰撞时
// 返回错误让调用方感知异常，而非静默污染数据。
func (s *PostgresStore) CreateSession(ctx context.Context, input authsession.SessionRecordInput) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_sessions (user_id, sid, session_hash, device_name, browser, os, user_agent_raw, ip_prefix, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (sid) DO NOTHING
	`, input.UserID, input.SID, input.SessionHash, input.DeviceName, input.Browser, input.OS, input.UserAgentRaw, input.IPPrefix, input.IPAddress)
	if err != nil {
		return fmt.Errorf("create user session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// sid 已存在（碰撞或重复调用）——返回错误而非静默覆盖。
		return fmt.Errorf("create user session: sid already exists (collision or duplicate)")
	}
	return nil
}

// IsSessionRevoked 判断给定 sid 是否已被下线（revoked_at 非空）。
// CurrentUserID 在热路径调用，用于实现「下次请求失效」：被下线的设备在下一次请求即视为未登录。
// 只查询属于该用户的行，sid 不匹配时视为已撤销（保守拒绝）。
func (s *PostgresStore) IsSessionRevoked(ctx context.Context, userID int64, sid string) (bool, error) {
	var revokedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT revoked_at
		FROM user_sessions
		WHERE user_id = $1 AND sid = $2
	`, userID, sid).Scan(&revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 行不存在：sid 不属于该用户，保守视为已撤销（拒绝鉴权）。
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("check session revoked: %w", err)
	}
	return revokedAt != nil, nil
}

// ListUserSessions 列出用户会话。includeHistory=false 仅活跃（revoked_at IS NULL），
// true 则含历史。currentSID 用于标记 isCurrent，不参与过滤。
func (s *PostgresStore) ListUserSessions(ctx context.Context, userID int64, currentSID string, includeHistory bool, page int, perPage int) (SessionListResult, error) {
	page, perPage = normalizeSessionPage(page, perPage)
	historyClause := "AND revoked_at IS NULL"
	if includeHistory {
		historyClause = ""
	}

	var total int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM user_sessions WHERE user_id = $1 `+historyClause,
		userID,
	).Scan(&total); err != nil {
		return SessionListResult{}, fmt.Errorf("count user sessions: %w", err)
	}

	offset := (page - 1) * perPage
	orderClause := "last_seen_at DESC"
	if includeHistory {
		orderClause = "created_at DESC"
	}

	rows, err := s.pool.Query(ctx, `
		SELECT sid, device_name, browser, os, ip_prefix, created_at, last_seen_at, revoked_at, revoke_reason
		FROM user_sessions
		WHERE user_id = $1 `+historyClause+`
		ORDER BY `+orderClause+`
		LIMIT $2 OFFSET $3
	`, userID, perPage, offset)
	if err != nil {
		return SessionListResult{}, fmt.Errorf("list user sessions: %w", err)
	}
	defer rows.Close()

	items := []SessionRecord{}
	for rows.Next() {
		var rec SessionRecord
		if err := rows.Scan(&rec.ID, &rec.DeviceName, &rec.Browser, &rec.OS, &rec.IPPrefix, &rec.CreatedAt, &rec.LastSeenAt, &rec.RevokedAt, &rec.RevokeReason); err != nil {
			return SessionListResult{}, fmt.Errorf("scan user session: %w", err)
		}
		rec.IsCurrent = rec.ID == currentSID
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return SessionListResult{}, fmt.Errorf("iterate user sessions: %w", err)
	}
	return SessionListResult{Items: items, Total: total, Page: page, PerPage: perPage}, nil
}

// RevokeSession 下线单个会话（标记 revoked_at），仅作用于属于该用户且当前活跃的行。
// 不匹配（sid 不存在或不属于该用户）返回 ErrSessionNotFound，以便上层区分 404 与成功。
func (s *PostgresStore) RevokeSession(ctx context.Context, userID int64, sid string, reason string) error {
	// RETURNING sid：撤销事实落库后要据此擦除该会话的插件状态（best-effort）。
	rows, err := s.pool.Query(ctx, `
		UPDATE user_sessions
		SET revoked_at = now(), revoke_reason = $3
		WHERE user_id = $1 AND sid = $2 AND revoked_at IS NULL
		RETURNING sid
	`, userID, sid, reason)
	if err != nil {
		return fmt.Errorf("revoke user session: %w", err)
	}
	revoked, err := collectRevokedSIDs(rows)
	if err != nil {
		return fmt.Errorf("collect revoked sessions: %w", err)
	}
	if len(revoked) == 0 {
		// 可能已下线或根本不存在；进一步区分以给出准确错误。
		var exists bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM user_sessions WHERE user_id = $1 AND sid = $2)
		`, userID, sid).Scan(&exists); err != nil {
			return fmt.Errorf("check user session existence: %w", err)
		}
		if !exists {
			return ErrSessionNotFound
		}
		// 行存在但已下线：视为幂等成功（已是期望状态），但仍要确保插件状态不残留。
		s.dropPluginSessionState(ctx, []string{sid})
		return nil
	}
	s.dropPluginSessionState(ctx, revoked)
	return nil
}

// RevokeOtherSessions 下线除当前会话外的所有活跃会话，返回被下线的条数。
func (s *PostgresStore) RevokeOtherSessions(ctx context.Context, userID int64, currentSID string, reason string) (int, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE user_sessions
		SET revoked_at = now(), revoke_reason = $3
		WHERE user_id = $1 AND sid <> $2 AND revoked_at IS NULL
		RETURNING sid
	`, userID, currentSID, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke other user sessions: %w", err)
	}
	revoked, err := collectRevokedSIDs(rows)
	if err != nil {
		return 0, fmt.Errorf("collect revoked sessions: %w", err)
	}
	s.dropPluginSessionState(ctx, revoked)
	return len(revoked), nil
}

// RevokeUserSessions 下线某用户的全部活跃会话（管理员强制下线），返回下线条数。
// 与 RevokeOtherSessions 不同：不排除任何会话，用于安全事件后管理员清空目标用户所有设备。
func (s *PostgresStore) RevokeUserSessions(ctx context.Context, userID int64, reason string) (int, error) {
	var revoked int
	err := s.runIdentityAuthorityMutation(ctx, func() error {
		var revokeErr error
		revoked, revokeErr = s.revokeUserSessions(ctx, userID, reason)
		return revokeErr
	})
	return revoked, err
}

func (s *PostgresStore) revokeUserSessions(ctx context.Context, userID int64, reason string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin revoke user sessions: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var revision int64
	if err := tx.QueryRow(ctx, `
		UPDATE users
		SET current_token_version = current_token_version + 1, updated_at = transaction_timestamp()
		WHERE id = $1
		RETURNING current_token_version
	`, userID).Scan(&revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("advance user session authority: %w", err)
	}
	rows, err := tx.Query(ctx, `
		UPDATE user_sessions
		SET revoked_at = transaction_timestamp(), revoke_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL
		RETURNING sid
	`, userID, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	revoked, err := collectRevokedSIDs(rows)
	if err != nil {
		return 0, fmt.Errorf("collect revoked sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit revoke user sessions: %w", err)
	}
	// 只在提交成功后擦除：回滚过的事务不该产生副作用。
	s.dropPluginSessionState(ctx, revoked)
	return len(revoked), nil
}

// DeleteOldRevokedSessions 清理已下线超过保留期的历史会话行（periodic job 调用）。
// 返回删除条数。仅删除 revoked_at 非空且早于 keepDays 的行；活跃会话永不被删。
func (s *PostgresStore) DeleteOldRevokedSessions(ctx context.Context, keepDays int) (int, error) {
	if keepDays < 1 {
		keepDays = 30
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_sessions
		WHERE revoked_at IS NOT NULL
		  AND revoked_at < now() - make_interval(days => $1)
	`, keepDays)
	if err != nil {
		return 0, fmt.Errorf("delete old revoked sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// EnforceMaxSessions 登录时强制最大活跃设备数：当前会话（currentSID）一定保留，
// 其余活跃会话按 last_seen_at 降序保留最新的 maxDevices-1 个，超出部分标记为 max_exceeded。
// 返回被踢出的条数。maxDevices <= 0 时不限制。
//
// 为什么排除 currentSID：登录流程在 Save（CreateSession）后调用本方法，此时当前会话
// 已入库且 last_seen_at=now()。若不排除，当多个会话 last_seen_at 相等（同一时刻并发登录）
// 时 ORDER BY ... LIMIT 的排序不稳定，可能把刚登录的当前设备排在踢出区，导致用户登录成功
// 后下一次请求立即失效。显式排除 currentSID 保证刚登录的设备永不被踢。
func (s *PostgresStore) EnforceMaxSessions(ctx context.Context, userID int64, currentSID string, maxDevices int) (int, error) {
	if maxDevices <= 0 {
		return 0, nil
	}
	if currentSID == "" {
		return 0, nil
	}
	// 单条 SQL：保留当前会话 + 其余活跃会话中最新的 (maxDevices-1) 个，把其余活跃行标记下线。
	rows, err := s.pool.Query(ctx, `
		UPDATE user_sessions
		SET revoked_at = now(), revoke_reason = $4
		WHERE user_id = $1
		  AND revoked_at IS NULL
		  AND sid <> $2
		  AND sid NOT IN (
		    SELECT sid FROM user_sessions
		    WHERE user_id = $1 AND revoked_at IS NULL AND sid <> $2
		    ORDER BY last_seen_at DESC
		    LIMIT $3
		  )
		RETURNING sid
	`, userID, currentSID, maxDevices-1, RevokeReasonMaxExceeded)
	if err != nil {
		return 0, fmt.Errorf("enforce max sessions: %w", err)
	}
	evicted, err := collectRevokedSIDs(rows)
	if err != nil {
		return 0, fmt.Errorf("collect evicted sessions: %w", err)
	}
	// 设备数驱逐同样是「会话结束」，被踢出设备的插件状态不得残留。
	s.dropPluginSessionState(ctx, evicted)
	return len(evicted), nil
}

// TouchSessionLastSeen 刷新会话最后活跃时间，用于展示「最后活跃」。
// 调用频率由 AuthSession refresh 节流，避免每个请求都写。
func (s *PostgresStore) TouchSessionLastSeen(ctx context.Context, userID int64, sid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_sessions SET last_seen_at = now()
		WHERE user_id = $1 AND sid = $2 AND revoked_at IS NULL
	`, userID, sid)
	if err != nil {
		return fmt.Errorf("touch user session last seen: %w", err)
	}
	return nil
}

// HasSessionFingerprint 判断该用户是否有活跃会话匹配给定指纹。
// 指纹按 user_agent_raw 匹配（登录时存入的截断 UA）；同一 UA 字符串视为已知设备。
// 用于风险登录：未命中表示新设备，可触发 login_risk 人机验证。
func (s *PostgresStore) HasSessionFingerprint(ctx context.Context, userID int64, fingerprint string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_sessions
			WHERE user_id = $1 AND user_agent_raw = $2 AND revoked_at IS NULL
		)
	`, userID, fingerprint).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check session fingerprint: %w", err)
	}
	return exists, nil
}

// ClearUserClientIPs 清空该用户名下的真实客户端 IP（隐私：删号/封禁/应监管请求）。
// 不删会话行与内容行，只把 IP 字段置空；审计流水 metadata 中的历史 IP 保留供安全调查。
func (s *PostgresStore) ClearUserClientIPs(ctx context.Context, userID int64) (ClearUserClientIPsResult, error) {
	var result ClearUserClientIPsResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ClearUserClientIPsResult{}, fmt.Errorf("begin clear user client ips: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE user_sessions
		SET ip_address = '', ip_prefix = ''
		WHERE user_id = $1 AND (ip_address <> '' OR ip_prefix <> '')
	`, userID)
	if err != nil {
		return ClearUserClientIPsResult{}, fmt.Errorf("clear session ips: %w", err)
	}
	result.SessionsCleared = int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		UPDATE topics
		SET ip_address = '', last_edit_ip = ''
		WHERE author_user_id = $1 AND (ip_address <> '' OR last_edit_ip <> '')
	`, userID)
	if err != nil {
		return ClearUserClientIPsResult{}, fmt.Errorf("clear topic ips: %w", err)
	}
	result.TopicsCleared = int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		UPDATE comments
		SET ip_address = '', last_edit_ip = ''
		WHERE author_user_id = $1 AND (ip_address <> '' OR last_edit_ip <> '')
	`, userID)
	if err != nil {
		return ClearUserClientIPsResult{}, fmt.Errorf("clear comment ips: %w", err)
	}
	result.CommentsCleared = int(tag.RowsAffected())

	if err := tx.Commit(ctx); err != nil {
		return ClearUserClientIPsResult{}, fmt.Errorf("commit clear user client ips: %w", err)
	}
	return result, nil
}

// normalizeSessionPage 规范化设备列表的分页参数。
// 与 normalizePage 一致但最大 perPage 更小（设备列表不需要大量）。
func normalizeSessionPage(page int, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > maxAdminListPage {
		page = maxAdminListPage
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 50 {
		perPage = 50
	}
	return page, perPage
}
