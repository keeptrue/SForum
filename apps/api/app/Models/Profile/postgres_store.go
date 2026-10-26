package profile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// GetProfile 读取用户资料；若行不存在则返回空资料（首次访问时按需 upsert）。
func (s *PostgresStore) GetProfile(ctx context.Context, userID int64) (Profile, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT user_id, bio, signature, location, website_url, avatar_attachment_id, created_at, updated_at
		FROM user_profiles
		WHERE user_id = $1
	`, userID)
	profile, err := scanProfile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{UserID: userID}, nil
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return profile, nil
}

func (s *PostgresStore) UpsertProfile(ctx context.Context, input Profile) (Profile, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO user_profiles (user_id, bio, signature, location, website_url, avatar_attachment_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE
		  SET bio = EXCLUDED.bio,
		      signature = EXCLUDED.signature,
		      location = EXCLUDED.location,
		      website_url = EXCLUDED.website_url,
		      avatar_attachment_id = EXCLUDED.avatar_attachment_id,
		      updated_at = now()
		RETURNING user_id, bio, signature, location, website_url, avatar_attachment_id, created_at, updated_at
	`, input.UserID, input.Bio, input.Signature, input.Location, input.WebsiteURL, nullableInt64(input.AvatarAttachmentID))
	profile, err := scanProfile(row)
	if err != nil {
		return Profile{}, fmt.Errorf("upsert profile: %w", err)
	}
	return profile, nil
}

func (s *PostgresStore) SetAvatarAttachment(ctx context.Context, userID int64, attachmentID *int64, actorUserID int64) (Profile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("begin set avatar attachment: %w", err)
	}
	defer tx.Rollback(ctx)

	if attachmentID != nil {
		if err := validateAvatarAttachment(ctx, tx, userID, *attachmentID); err != nil {
			return Profile{}, err
		}
	}

	oldRows, err := tx.Query(ctx, `
		SELECT attachment_id
		FROM attachment_references
		WHERE resource_type = 'user' AND resource_id = $1 AND context = 'avatar'
	`, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("list old avatar references: %w", err)
	}
	oldIDs := []int64{}
	for oldRows.Next() {
		var id int64
		if err := oldRows.Scan(&id); err != nil {
			oldRows.Close()
			return Profile{}, fmt.Errorf("scan old avatar reference: %w", err)
		}
		oldIDs = append(oldIDs, id)
	}
	if err := oldRows.Err(); err != nil {
		oldRows.Close()
		return Profile{}, fmt.Errorf("iterate old avatar references: %w", err)
	}
	oldRows.Close()

	if _, err := tx.Exec(ctx, `
		DELETE FROM attachment_references
		WHERE resource_type = 'user' AND resource_id = $1 AND context = 'avatar'
	`, userID); err != nil {
		return Profile{}, fmt.Errorf("delete old avatar references: %w", err)
	}
	if len(oldIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE attachments
			SET reference_count = GREATEST(reference_count - 1, 0), updated_at = now()
			WHERE id = ANY($1)
		`, oldIDs); err != nil {
			return Profile{}, fmt.Errorf("decrement old avatar references: %w", err)
		}
	}

	if attachmentID != nil {
		tag, err := tx.Exec(ctx, `
			INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context, created_by_user_id)
			VALUES ($1, 'user', $2, 'avatar', $3)
			ON CONFLICT DO NOTHING
		`, *attachmentID, userID, nullableActorID(actorUserID))
		if err != nil {
			return Profile{}, fmt.Errorf("insert avatar reference: %w", err)
		}
		if tag.RowsAffected() > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE attachments
				SET reference_count = reference_count + 1, updated_at = now()
				WHERE id = $1
			`, *attachmentID); err != nil {
				return Profile{}, fmt.Errorf("increment avatar reference: %w", err)
			}
		}
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO user_profiles (user_id, avatar_attachment_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		  SET avatar_attachment_id = EXCLUDED.avatar_attachment_id,
		      updated_at = now()
		RETURNING user_id, bio, signature, location, website_url, avatar_attachment_id, created_at, updated_at
	`, userID, nullableInt64(attachmentID))
	profile, err := scanProfile(row)
	if err != nil {
		return Profile{}, fmt.Errorf("upsert avatar profile: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, fmt.Errorf("commit set avatar attachment: %w", err)
	}
	return profile, nil
}

func (s *PostgresStore) GetAvatarAttachment(ctx context.Context, attachmentID int64) (AvatarAttachment, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, public_id, owner_user_id, content_type, status
		FROM attachments
		WHERE id = $1
	`, attachmentID)
	attachment, err := scanAvatarAttachment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AvatarAttachment{}, ErrProfileInvalid
	}
	if err != nil {
		return AvatarAttachment{}, fmt.Errorf("get avatar attachment: %w", err)
	}
	return attachment, nil
}

func (s *PostgresStore) GetUserSummaryByUsername(ctx context.Context, username string) (UserProfileSummary, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, email, display_name, created_at
		FROM users
		WHERE username_lower = $1 AND status = 'active'
	`, strings.ToLower(strings.TrimSpace(username)))
	summary, err := scanUserSummary(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserProfileSummary{}, ErrProfileNotFound
	}
	if err != nil {
		return UserProfileSummary{}, fmt.Errorf("get user summary by username: %w", err)
	}
	return summary, nil
}

func (s *PostgresStore) GetUserSummaryByID(ctx context.Context, userID int64) (UserProfileSummary, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, email, display_name, created_at
		FROM users
		WHERE id = $1
	`, userID)
	summary, err := scanUserSummary(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserProfileSummary{}, ErrProfileNotFound
	}
	if err != nil {
		return UserProfileSummary{}, fmt.Errorf("get user summary by id: %w", err)
	}
	return summary, nil
}

// SearchPublicUsers 按昵称或用户名检索公开可达的活跃用户。
//
// 过滤口径与论坛 composer 的提及候选一致（活跃用户、用户名或昵称匹配），但结果
// 只投影 id / username / 显示名：邮箱与非公开状态既不参与匹配也不进入结果。
// 排序把完全匹配排在最前，其余按昵称字典序，因此同名候选的顺序是稳定的。
func (s *PostgresStore) SearchPublicUsers(ctx context.Context, query string, limit int) ([]UserSearchHit, error) {
	normalized := strings.TrimSpace(query)
	if normalized == "" || limit <= 0 {
		return nil, nil
	}
	lowered := strings.ToLower(normalized)
	pattern := "%" + escapeUserSearchLike(lowered) + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, COALESCE(NULLIF(BTRIM(display_name), ''), username)
		FROM users
		WHERE status = 'active'
		  AND (username_lower ILIKE $1 ESCAPE E'\\' OR LOWER(display_name) ILIKE $1 ESCAPE E'\\')
		ORDER BY (username_lower = $2) DESC,
		         (LOWER(COALESCE(NULLIF(BTRIM(display_name), ''), username)) = $2) DESC,
		         LOWER(COALESCE(NULLIF(BTRIM(display_name), ''), username)) ASC,
		         id ASC
		LIMIT $3
	`, pattern, lowered, limit)
	if err != nil {
		return nil, fmt.Errorf("search public users: %w", err)
	}
	defer rows.Close()
	items := make([]UserSearchHit, 0, limit)
	for rows.Next() {
		var item UserSearchHit
		if err := rows.Scan(&item.UserID, &item.Username, &item.DisplayName); err != nil {
			return nil, fmt.Errorf("scan public user hit: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public user hits: %w", err)
	}
	return items, nil
}

// escapeUserSearchLike 转义 LIKE 元字符：用户输入的 % 与 _ 是字面量，不是通配符，
// 否则一次 `%` 查询就变回「列出全站用户」。
func escapeUserSearchLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (s *PostgresStore) GetProfileStats(ctx context.Context, userID int64) (ProfileStats, error) {
	var stats ProfileStats
	// 公开主题数（active/locked + public 分类）。
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		WHERE topics.author_user_id = $1
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
	`, userID).Scan(&stats.TopicCount); err != nil {
		return ProfileStats{}, fmt.Errorf("count user topics: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM comments
		JOIN topics ON topics.id = comments.topic_id
		JOIN categories ON categories.id = topics.category_id
		WHERE comments.author_user_id = $1
		  AND comments.status = 'active'
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
	`, userID).Scan(&stats.CommentCount); err != nil {
		return ProfileStats{}, fmt.Errorf("count user comments: %w", err)
	}
	return stats, nil
}

func (s *PostgresStore) ListRecentTopics(ctx context.Context, userID int64, limit int) ([]forum.TopicSummary, error) {
	return s.listRecentTopics(ctx, userID, limit, 0, "activity")
}

func (s *PostgresStore) ListRecentActivityTopics(ctx context.Context, userID int64, limit int) ([]forum.TopicSummary, error) {
	return s.listRecentTopics(ctx, userID, limit, 0, "created")
}

func (s *PostgresStore) ListActivityTopics(ctx context.Context, userID int64, limit, offset int) ([]forum.TopicSummary, error) {
	return s.listRecentTopics(ctx, userID, limit, offset, "created")
}

func (s *PostgresStore) listRecentTopics(ctx context.Context, userID int64, limit, offset int, order string) ([]forum.TopicSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > profileActivityPageMaxPerPage {
		limit = profileActivityPageMaxPerPage
	}
	if offset < 0 {
		offset = 0
	}
	orderBy := "topics.last_activity_at DESC, topics.id DESC"
	if order == "created" {
		orderBy = "topics.created_at DESC, topics.id DESC"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT topics.id, topics.category_id, categories.slug, categories.name,
		  topics.author_user_id, users.username, users.display_name,
		  topics.title, topics.slug, topics.status, topics.is_pinned,
		  topics.comment_count, topics.view_count, topics.hot_score, left(posts.plain_text, 2000),
		  `+forumCurrentRevisionSQL("posts")+`,
		  (`+forumCurrentRevisionSQL("posts")+`) > 1,
		  topics.created_at, topics.updated_at, topics.last_activity_at
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		JOIN posts ON posts.id = topics.content_id
		LEFT JOIN users ON users.id = topics.author_user_id
		WHERE topics.author_user_id = $1
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
		ORDER BY `+orderBy+`
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list recent topics: %w", err)
	}
	defer rows.Close()

	items := []forum.TopicSummary{}
	for rows.Next() {
		item, err := forum.ScanTopicSummary(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent topics: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) ListRecentComments(ctx context.Context, userID int64, limit int) ([]ProfileCommentActivity, error) {
	return s.ListActivityComments(ctx, userID, limit, 0)
}

func (s *PostgresStore) ListActivityComments(ctx context.Context, userID int64, limit, offset int) ([]ProfileCommentActivity, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > profileActivityPageMaxPerPage {
		limit = profileActivityPageMaxPerPage
	}
	if offset < 0 {
		offset = 0
	}
	// perPage 与 ResolveCommentPage / ListComments 同源，保证个人主页深链页码不漂移。
	commentsPerPage := s.forumCommentsPerPage(ctx)
	rows, err := s.pool.Query(ctx, `
		SELECT comments.id, left(comment_posts.plain_text, 2000), comments.created_at,
		  topics.id, topics.slug, topics.title, topics.status, topics.comment_count,
		  topics.created_at, topics.updated_at, topics.last_activity_at,
		  categories.slug, categories.name,
		  (
		    SELECT count(*) FROM comments siblings
		    WHERE siblings.topic_id = comments.topic_id
		      AND siblings.status = 'active'
		      AND ROW(siblings.created_at, siblings.id) < ROW(comments.created_at, comments.id)
		  ) AS active_before
		FROM comments
		JOIN posts comment_posts ON comment_posts.id = comments.content_id
		JOIN topics ON topics.id = comments.topic_id
		JOIN categories ON categories.id = topics.category_id
		WHERE comments.author_user_id = $1
		  AND comments.status = 'active'
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
		ORDER BY comments.created_at DESC, comments.id DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list recent comments: %w", err)
	}
	defer rows.Close()

	items := []ProfileCommentActivity{}
	for rows.Next() {
		var item ProfileCommentActivity
		var plainPrefix string
		var activeBefore int64
		if err := rows.Scan(
			&item.CommentID,
			&plainPrefix,
			&item.CreatedAt,
			&item.Topic.ID,
			&item.Topic.Slug,
			&item.Topic.Title,
			&item.Topic.Status,
			&item.Topic.CommentCount,
			&item.Topic.CreatedAt,
			&item.Topic.UpdatedAt,
			&item.Topic.LastActivityAt,
			&item.Topic.CategorySlug,
			&item.Topic.CategoryName,
			&activeBefore,
		); err != nil {
			return nil, err
		}
		// 与 Forum.ResolveCommentPage 一致：page = before/perPage + 1，
		// 并钳制到 MaxTopicPage——评论列表接口会把更大的页码钳回上限，
		// 不钳制的话深链会指向一个永远取不到目标评论的页。
		item.CommentPage = int(activeBefore)/commentsPerPage + 1
		if item.CommentPage > forum.MaxTopicPage {
			item.CommentPage = forum.MaxTopicPage
		}
		item.Excerpt = forum.ExcerptFromPlain(plainPrefix, forum.RecommendedExcerptRuneLimit)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent comments: %w", err)
	}
	return items, nil
}

// forumCommentsPerPage 读取运营配置的评论每页条数；失败或非法时回落 20（与 forum 默认一致）。
func (s *PostgresStore) forumCommentsPerPage(ctx context.Context) int {
	const fallback = 20
	var raw string
	err := s.pool.QueryRow(ctx, `
		SELECT value FROM web_options WHERE name = $1
	`, options.NameForumCommentsPerPage).Scan(&raw)
	if err != nil {
		return fallback
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(raw))
	if convErr != nil || n < 1 {
		return fallback
	}
	if n > 100 {
		return 100
	}
	return n
}

func forumCurrentRevisionSQL(postAlias string) string {
	return `CASE
		  WHEN ` + postAlias + `.current_revision > 0 THEN ` + postAlias + `.current_revision + (
		    SELECT COUNT(*) FROM post_revisions pr_effective
		    WHERE pr_effective.post_id = ` + postAlias + `.id AND pr_effective.revision_no IS NULL
		  )
		  ELSE 1 + (SELECT COUNT(*) FROM post_revisions pr_effective WHERE pr_effective.post_id = ` + postAlias + `.id)
		END`
}

type profileScanner = forum.RowScanner

func scanProfile(row profileScanner) (Profile, error) {
	var profile Profile
	var avatarID sql.NullInt64
	if err := row.Scan(
		&profile.UserID,
		&profile.Bio,
		&profile.Signature,
		&profile.Location,
		&profile.WebsiteURL,
		&avatarID,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	); err != nil {
		return Profile{}, err
	}
	if avatarID.Valid {
		id := avatarID.Int64
		profile.AvatarAttachmentID = &id
	}
	return profile, nil
}

func scanUserSummary(row profileScanner) (UserProfileSummary, error) {
	var summary UserProfileSummary
	if err := row.Scan(&summary.UserID, &summary.Username, &summary.Email, &summary.DisplayName, &summary.JoinedAt); err != nil {
		return UserProfileSummary{}, err
	}
	return summary, nil
}

func scanAvatarAttachment(row profileScanner) (AvatarAttachment, error) {
	var attachment AvatarAttachment
	var ownerID sql.NullInt64
	if err := row.Scan(&attachment.ID, &attachment.PublicID, &ownerID, &attachment.ContentType, &attachment.Status); err != nil {
		return AvatarAttachment{}, err
	}
	if ownerID.Valid {
		attachment.OwnerUserID = ownerID.Int64
	}
	return attachment, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableActorID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func validateAvatarAttachment(ctx context.Context, tx pgx.Tx, userID int64, attachmentID int64) error {
	attachment, err := scanAvatarAttachment(tx.QueryRow(ctx, `
		SELECT id, public_id, owner_user_id, content_type, status
		FROM attachments
		WHERE id = $1
	`, attachmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProfileInvalid
	}
	if err != nil {
		return fmt.Errorf("validate avatar attachment: %w", err)
	}
	if attachment.OwnerUserID != userID || attachment.Status != "active" || !strings.HasPrefix(strings.ToLower(attachment.ContentType), "image/") {
		return ErrProfileInvalid
	}
	return nil
}
