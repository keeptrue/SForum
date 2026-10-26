package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) ListUsers(ctx context.Context, input UserListInput) (AdminUserList, error) {
	const countSQL = `
		SELECT count(*)
		FROM users
		WHERE ($1 = '' OR username_lower LIKE '%' || lower($1) || '%' ESCAPE '\' OR email_lower LIKE '%' || lower($1) || '%' ESCAPE '\' OR lower(display_name) LIKE '%' || lower($1) || '%' ESCAPE '\')
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR EXISTS (
		    SELECT 1
		    FROM user_roles
		    JOIN roles ON roles.id = user_roles.role_id
		    WHERE user_roles.user_id = users.id AND roles.key = $3
		  ))
	`
	const listSQLPrefix = `
		SELECT id, username, email, email_verified_at IS NOT NULL, display_name, locale, status, is_initial_super_admin, created_at, updated_at
		FROM users
		WHERE ($1 = '' OR username_lower LIKE '%' || lower($1) || '%' ESCAPE '\' OR email_lower LIKE '%' || lower($1) || '%' ESCAPE '\' OR lower(display_name) LIKE '%' || lower($1) || '%' ESCAPE '\')
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR EXISTS (
		    SELECT 1
		    FROM user_roles
		    JOIN roles ON roles.id = user_roles.role_id
		    WHERE user_roles.user_id = users.id AND roles.key = $3
		  ))
		ORDER BY `
	const listSQLSuffix = `
		LIMIT $4 OFFSET $5
	`

	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, input.Query, input.Status, input.RoleKey).Scan(&total); err != nil {
		return AdminUserList{}, fmt.Errorf("count admin users: %w", err)
	}

	offset := (input.Page - 1) * input.PerPage
	listSQL := listSQLPrefix + adminUserOrderBy(input.SortBy, input.SortOrder) + listSQLSuffix
	rows, err := s.pool.Query(ctx, listSQL, input.Query, input.Status, input.RoleKey, input.PerPage, offset)
	if err != nil {
		return AdminUserList{}, fmt.Errorf("list admin users: %w", err)
	}
	defer rows.Close()

	items := []AdminUserSummary{}
	for rows.Next() {
		var user AdminUserSummary
		if err := rows.Scan(
			&user.ID,
			&user.Username,
			&user.Email,
			&user.EmailVerified,
			&user.DisplayName,
			&user.Locale,
			&user.Status,
			&user.IsInitialSuperAdmin,
			&user.CreatedAt,
			&user.UpdatedAt,
		); err != nil {
			return AdminUserList{}, fmt.Errorf("scan admin user: %w", err)
		}
		roleKeys, err := s.listAssignedUserRoleKeys(ctx, user.ID)
		if err != nil {
			return AdminUserList{}, err
		}
		user.RoleKeys = roleKeys
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return AdminUserList{}, fmt.Errorf("iterate admin users: %w", err)
	}

	return AdminUserList{Items: items, Total: total, Page: input.Page, PerPage: input.PerPage}, nil
}

func adminUserOrderBy(sortBy, sortOrder string) string {
	sortBy, sortOrder = normalizeUserListSorting(sortBy, sortOrder)
	direction := "DESC"
	if sortOrder == UserListSortOrderAsc {
		direction = "ASC"
	}

	column := "created_at"
	switch sortBy {
	case UserListSortUpdatedAt:
		column = "updated_at"
	case UserListSortUsername:
		column = "username_lower"
	case UserListSortDisplayName:
		column = "lower(display_name)"
	case UserListSortEmail:
		column = "email_lower"
	case UserListSortStatus:
		column = "status"
	}
	return column + " " + direction + ", id " + direction
}

func (s *PostgresStore) GetAdminUser(ctx context.Context, userID int64) (AdminUserDetail, error) {
	var detail AdminUserDetail
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, email, email_verified_at IS NOT NULL, display_name, locale, status, is_initial_super_admin, created_at, updated_at, kind
		FROM users
		WHERE id = $1
	`, userID).Scan(
		&detail.ID,
		&detail.Username,
		&detail.Email,
		&detail.EmailVerified,
		&detail.DisplayName,
		&detail.Locale,
		&detail.Status,
		&detail.IsInitialSuperAdmin,
		&detail.CreatedAt,
		&detail.UpdatedAt,
		&detail.Kind,
	)
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("get admin user: %w", err)
	}

	if err := s.loadAdminUserAccess(ctx, &detail); err != nil {
		return AdminUserDetail{}, err
	}
	if err := s.loadAdminUserProfile(ctx, &detail); err != nil {
		return AdminUserDetail{}, err
	}
	if err := s.loadAdminUserPreview(ctx, &detail); err != nil {
		return AdminUserDetail{}, err
	}
	history, err := loadNicknameHistory(ctx, s.pool, detail.ID, AdminNicknameHistoryLimit)
	if err != nil {
		return AdminUserDetail{}, err
	}
	detail.NicknameHistory = history
	return detail, nil
}

// loadAdminUserPreview 填充管理端预览：完整 IP/UA 会话、身份审计、内容计数、改密时间。
const adminUserPreviewSessionLimit = 30
const adminUserPreviewAuthEventLimit = 30

func (s *PostgresStore) loadAdminUserPreview(ctx context.Context, detail *AdminUserDetail) error {
	if detail == nil {
		return nil
	}
	detail.Sessions = []AdminSessionInspect{}
	detail.RecentAuthEvents = []AdminAuthEvent{}
	detail.Activity = AdminUserActivity{}

	// 改密时间（无凭证行时保持 nil，如仅 OAuth 账号的未来形态）。
	var passwordChangedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT password_changed_at FROM user_credentials WHERE user_id = $1
	`, detail.ID).Scan(&passwordChangedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load admin user password_changed_at: %w", err)
	}
	if err == nil {
		ts := passwordChangedAt
		detail.PasswordChangedAt = &ts
	}

	// 会话计数。
	if err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE revoked_at IS NULL),
			count(*)
		FROM user_sessions
		WHERE user_id = $1
	`, detail.ID).Scan(&detail.Activity.ActiveSessionCount, &detail.Activity.TotalSessionCount); err != nil {
		return fmt.Errorf("count admin user sessions: %w", err)
	}

	// 最近会话（含完整 IP / 原始 UA）。
	sessionRows, err := s.pool.Query(ctx, `
		SELECT sid, device_name, browser, os, ip_prefix, ip_address, user_agent_raw,
		       created_at, last_seen_at, revoked_at, revoke_reason
		FROM user_sessions
		WHERE user_id = $1
		ORDER BY COALESCE(last_seen_at, created_at) DESC, created_at DESC
		LIMIT $2
	`, detail.ID, adminUserPreviewSessionLimit)
	if err != nil {
		return fmt.Errorf("list admin user sessions: %w", err)
	}
	defer sessionRows.Close()

	for sessionRows.Next() {
		var rec AdminSessionInspect
		if err := sessionRows.Scan(
			&rec.ID, &rec.DeviceName, &rec.Browser, &rec.OS,
			&rec.IPPrefix, &rec.IPAddress, &rec.UserAgent,
			&rec.CreatedAt, &rec.LastSeenAt, &rec.RevokedAt, &rec.RevokeReason,
		); err != nil {
			return fmt.Errorf("scan admin user session: %w", err)
		}
		rec.IsActive = rec.RevokedAt == nil
		detail.Sessions = append(detail.Sessions, rec)
		if rec.IsActive {
			if detail.Activity.LastSeenAt == nil || rec.LastSeenAt.After(*detail.Activity.LastSeenAt) {
				ts := rec.LastSeenAt
				detail.Activity.LastSeenAt = &ts
			}
		}
	}
	if err := sessionRows.Err(); err != nil {
		return fmt.Errorf("iterate admin user sessions: %w", err)
	}

	// 主题/评论计数（论坛表存在时；纯 identity 测试库可能无表，失败则记 0）。
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM topics WHERE author_user_id = $1 AND deleted_at IS NULL
	`, detail.ID).Scan(&detail.Activity.TopicCount)
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM comments WHERE author_user_id = $1 AND deleted_at IS NULL
	`, detail.ID).Scan(&detail.Activity.CommentCount)

	// 最近登录/注册审计（metadata 中的 ipAddress / userAgent）。
	authRows, err := s.pool.Query(ctx, `
		SELECT id, action, metadata, created_at
		FROM audit_events
		WHERE target_user_id = $1
		  AND action IN ($2, $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, detail.ID, AuditActionLogin, AuditActionRegister, adminUserPreviewAuthEventLimit)
	if err != nil {
		return fmt.Errorf("list admin user auth events: %w", err)
	}
	defer authRows.Close()

	for authRows.Next() {
		var (
			event    AdminAuthEvent
			metadata []byte
		)
		if err := authRows.Scan(&event.ID, &event.Action, &metadata, &event.CreatedAt); err != nil {
			return fmt.Errorf("scan admin user auth event: %w", err)
		}
		event.IPAddress, event.UserAgent, event.SessionHash = parseAuthAuditMetadata(metadata)
		detail.RecentAuthEvents = append(detail.RecentAuthEvents, event)
	}
	if err := authRows.Err(); err != nil {
		return fmt.Errorf("iterate admin user auth events: %w", err)
	}

	if len(detail.RecentAuthEvents) > 0 {
		first := detail.RecentAuthEvents[0]
		// 最近一条登录优先；若列表以注册开头也可用。
		for _, event := range detail.RecentAuthEvents {
			if event.Action == AuditActionLogin {
				first = event
				break
			}
		}
		ts := first.CreatedAt
		detail.Activity.LastLoginAt = &ts
		detail.Activity.LastLoginIP = first.IPAddress
		detail.Activity.LastLoginUserAgent = first.UserAgent
	} else if len(detail.Sessions) > 0 {
		// 无审计时退回最近会话的 IP/UA。
		latest := detail.Sessions[0]
		ts := latest.CreatedAt
		detail.Activity.LastLoginAt = &ts
		detail.Activity.LastLoginIP = latest.IPAddress
		if detail.Activity.LastLoginIP == "" {
			detail.Activity.LastLoginIP = latest.IPPrefix
		}
		detail.Activity.LastLoginUserAgent = latest.UserAgent
	}

	return nil
}

func parseAuthAuditMetadata(raw []byte) (ipAddress, userAgent, sessionHash string) {
	if len(raw) == 0 {
		return "", "", ""
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", "", ""
	}
	ipAddress = stringFromAny(meta["ipAddress"])
	userAgent = stringFromAny(meta["userAgent"])
	sessionHash = stringFromAny(meta["sessionHash"])
	return ipAddress, userAgent, sessionHash
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

// loadAdminUserProfile 读取 user_profiles；无行时返回空资料（注册后可能尚未 upsert）。
func (s *PostgresStore) loadAdminUserProfile(ctx context.Context, detail *AdminUserDetail) error {
	err := s.pool.QueryRow(ctx, `
		SELECT bio, signature, location, website_url
		FROM user_profiles
		WHERE user_id = $1
	`, detail.ID).Scan(
		&detail.Profile.Bio,
		&detail.Profile.Signature,
		&detail.Profile.Location,
		&detail.Profile.WebsiteURL,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail.Profile = AdminUserProfile{}
			return nil
		}
		return fmt.Errorf("load admin user profile: %w", err)
	}
	return nil
}

// UpdateAdminUser 在事务中更新 users 账户字段与 user_profiles 资料字段。
func (s *PostgresStore) UpdateAdminUser(ctx context.Context, actorUserID int64, targetUserID int64, input AdminUpdateUserInput) (AdminUserDetail, error) {
	if s.authorityMutationGate == nil {
		return s.updateAdminUser(ctx, actorUserID, targetUserID, input)
	}
	var result AdminUserDetail
	err := s.runIdentityAuthorityMutation(ctx, func() error {
		var updateErr error
		result, updateErr = s.updateAdminUser(ctx, actorUserID, targetUserID, input)
		return updateErr
	})
	return result, err
}

func (s *PostgresStore) runIdentityAuthorityMutation(ctx context.Context, mutation func() error) error {
	if s == nil || mutation == nil {
		return ErrInvalidUserUpdate
	}
	if s.authorityMutationGate == nil {
		return mutation()
	}
	return s.authorityMutationGate.RunSessionPolicyMutation(ctx, mutation)
}

func (s *PostgresStore) updateAdminUser(ctx context.Context, actorUserID int64, targetUserID int64, input AdminUpdateUserInput) (AdminUserDetail, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("begin admin user update tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 读取当前行作为合并基准，并做唯一性冲突检查。
	var current AdminUserDetail
	err = tx.QueryRow(ctx, `
		SELECT id, username, email, email_verified_at IS NOT NULL, display_name, locale, status, is_initial_super_admin, created_at, updated_at, kind
		FROM users
		WHERE id = $1
		FOR UPDATE
	`, targetUserID).Scan(
		&current.ID,
		&current.Username,
		&current.Email,
		&current.EmailVerified,
		&current.DisplayName,
		&current.Locale,
		&current.Status,
		&current.IsInitialSuperAdmin,
		&current.CreatedAt,
		&current.UpdatedAt,
		&current.Kind,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminUserDetail{}, ErrUserNotFound
		}
		return AdminUserDetail{}, fmt.Errorf("lock admin user: %w", err)
	}

	next := current
	if input.Username != nil {
		next.Username = *input.Username
	}
	if input.Email != nil {
		next.Email = *input.Email
	}
	if input.DisplayName != nil {
		next.DisplayName = *input.DisplayName
	}
	if input.Locale != nil {
		next.Locale = *input.Locale
	}
	if input.Status != nil {
		next.Status = *input.Status
	}
	if input.Kind != nil {
		next.Kind = *input.Kind
	}
	statusChanged := next.Status != current.Status
	emailChanged := next.Email != current.Email
	kindChanged := next.Kind != current.Kind

	// 用户名/邮箱变更时检查唯一性（排除自身）。
	if next.Username != current.Username || next.Email != current.Email {
		var takenUsername, takenEmail bool
		if err := tx.QueryRow(ctx, `
			SELECT
			  EXISTS(SELECT 1 FROM users WHERE username_lower = lower($1) AND id <> $3),
			  EXISTS(SELECT 1 FROM users WHERE email_lower = lower($2) AND id <> $3)
		`, next.Username, next.Email, targetUserID).Scan(&takenUsername, &takenEmail); err != nil {
			return AdminUserDetail{}, fmt.Errorf("check user uniqueness: %w", err)
		}
		if takenUsername || takenEmail {
			// 落成字段消息：服务层预检与这里查的是同一件事，只是这里能给出并发竞态下
			// 究竟哪个字段真的被占用。
			return AdminUserDetail{}, NewAdminUserConflictError(adminUserConflictFields(takenUsername, takenEmail))
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE users
		SET username = $2,
		    username_lower = lower($2),
		    email = $3,
		    email_lower = lower($3),
		    display_name = $4,
		    locale = $5,
		    status = $6,
		    current_token_version = current_token_version + CASE WHEN $7 THEN 1 ELSE 0 END,
		    email_verified_at = CASE WHEN $8 THEN NULL ELSE email_verified_at END,
		    kind = $9,
		    updated_at = now()
		WHERE id = $1
	`, targetUserID, next.Username, next.Email, next.DisplayName, next.Locale, string(next.Status), statusChanged, emailChanged, string(next.Kind))
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("update admin user account: %w", err)
	}
	// 管理员改名也进历史：运营侧同样需要「谁在什么时候改成了什么」。
	// 这里不写 nickname_changed_at —— 版务修正不该占用用户的自助改名冷却。
	if next.DisplayName != current.DisplayName {
		if err := insertNicknameChange(ctx, tx, targetUserID, current.DisplayName, next.DisplayName, NicknameSourceAdmin, actorUserID); err != nil {
			return AdminUserDetail{}, err
		}
	}
	if statusChanged && next.Status != UserStatusActive {
		if _, err := tx.Exec(ctx, `
			UPDATE user_sessions
			SET revoked_at = transaction_timestamp(), revoke_reason = 'admin_status_changed'
			WHERE user_id = $1 AND revoked_at IS NULL
		`, targetUserID); err != nil {
			return AdminUserDetail{}, fmt.Errorf("revoke sessions after admin status update: %w", err)
		}
	}
	// 变成机器人时撤销其全部会话：可登录的机器人等于给所有人开了一个冒充 AI
	// 的入口。恢复为人类后可以重新登录。
	if kindChanged && next.Kind.IsBot() {
		if _, err := tx.Exec(ctx, `
			UPDATE user_sessions
			SET revoked_at = transaction_timestamp(), revoke_reason = 'user_became_bot'
			WHERE user_id = $1 AND revoked_at IS NULL
		`, targetUserID); err != nil {
			return AdminUserDetail{}, fmt.Errorf("revoke sessions after user became a bot: %w", err)
		}
	}

	// 资料：先读再合并，无行时 upsert 空行再写。
	profile := AdminUserProfile{}
	err = tx.QueryRow(ctx, `
		SELECT bio, signature, location, website_url
		FROM user_profiles
		WHERE user_id = $1
		FOR UPDATE
	`, targetUserID).Scan(&profile.Bio, &profile.Signature, &profile.Location, &profile.WebsiteURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AdminUserDetail{}, fmt.Errorf("lock admin user profile: %w", err)
	}
	if input.Bio != nil {
		profile.Bio = *input.Bio
	}
	if input.Signature != nil {
		profile.Signature = *input.Signature
	}
	if input.Location != nil {
		profile.Location = *input.Location
	}
	if input.WebsiteURL != nil {
		profile.WebsiteURL = *input.WebsiteURL
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO user_profiles (user_id, bio, signature, location, website_url)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE
		SET bio = EXCLUDED.bio,
		    signature = EXCLUDED.signature,
		    location = EXCLUDED.location,
		    website_url = EXCLUDED.website_url,
		    updated_at = now()
	`, targetUserID, profile.Bio, profile.Signature, profile.Location, profile.WebsiteURL)
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("upsert admin user profile: %w", err)
	}

	metadata, _ := json.Marshal(map[string]any{
		"username":    next.Username,
		"email":       next.Email,
		"displayName": next.DisplayName,
		"locale":      next.Locale,
		"status":      string(next.Status),
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_user_id, target_user_id, action, metadata)
		VALUES ($1, $2, 'user.admin_update', $3::jsonb)
	`, actorUserID, targetUserID, string(metadata)); err != nil {
		return AdminUserDetail{}, fmt.Errorf("audit admin user update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AdminUserDetail{}, fmt.Errorf("commit admin user update: %w", err)
	}
	return s.GetAdminUser(ctx, targetUserID)
}
