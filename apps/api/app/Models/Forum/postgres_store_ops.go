package forum

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

func insertTag(ctx context.Context, tx pgx.Tx, actorUserID int64, slug string, status string) (TopicTagSummary, error) {
	name := strings.ReplaceAll(slug, "-", " ")
	var tag TopicTagSummary
	if err := tx.QueryRow(ctx, `
		INSERT INTO tags (slug, name, status, created_by_user_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, slug, name, status
	`, slug, name, status, nullUserID(actorUserID)).Scan(&tag.ID, &tag.Slug, &tag.Name, &tag.Status); err != nil {
		return TopicTagSummary{}, fmt.Errorf("insert tag: %w", err)
	}
	return tag, nil
}

func attachTopicTags(ctx context.Context, tx pgx.Tx, topicID int64, tags []TopicTagSummary) error {
	for _, tag := range tags {
		if tag.ID <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO topic_tags (topic_id, tag_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, topicID, tag.ID); err != nil {
			return fmt.Errorf("attach topic tag: %w", err)
		}
		if tag.Status != TagStatusActive {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tags
			SET topic_count = topic_count + 1, updated_at = now()
			WHERE id = $1 AND status = 'active'
		`, tag.ID); err != nil {
			return fmt.Errorf("update tag topic count: %w", err)
		}
	}
	return nil
}

func nullUserID(userID int64) any {
	if userID <= 0 {
		return nil
	}
	return userID
}

func (s *PostgresStore) GetTopicForComment(ctx context.Context, topicID int64) (TopicSummary, error) {
	row := s.pool.QueryRow(ctx, topicSummarySQL()+`
		WHERE topics.id = $1
	`, topicID)
	topic, err := scanTopicSummaryWithAvatar(row, s.avatarBuilder)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicSummary{}, ErrTopicNotFound
	}
	if err != nil {
		return TopicSummary{}, fmt.Errorf("get topic for comment: %w", err)
	}
	return topic, nil
}

// GetTopicForAction 加载主题摘要（含 author/status），不做公开可见性过滤，
// 用于更新/删除/生命周期动作的权限判定。
func (s *PostgresStore) GetTopicForAction(ctx context.Context, topicID int64) (TopicSummary, error) {
	row := s.pool.QueryRow(ctx, topicSummarySQL()+`
		WHERE topics.id = $1
	`, topicID)
	topic, err := scanTopicSummaryWithAvatar(row, s.avatarBuilder)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicSummary{}, ErrTopicNotFound
	}
	if err != nil {
		return TopicSummary{}, fmt.Errorf("get topic for action: %w", err)
	}
	return topic, nil
}

func (s *PostgresStore) GetCommentSummary(ctx context.Context, commentID int64) (CommentSummary, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT comments.id, comments.topic_id, comments.author_user_id, comments.parent_comment_id, COALESCE(comments.root_comment_id, comments.id), comments.path_key, comments.depth, comments.status, comments.created_at, posts.current_revision
		FROM comments JOIN posts ON posts.id = comments.content_id
		WHERE comments.id = $1
	`, commentID)
	summary, err := scanCommentSummary(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommentSummary{}, ErrCommentNotFound
	}
	if err != nil {
		return CommentSummary{}, fmt.Errorf("get comment summary: %w", err)
	}
	return summary, nil
}

// LatestAuthorTopicCreatedAt 返回作者最近一次主题创建时间（含非公开状态，用于冷却）。
func (s *PostgresStore) LatestAuthorTopicCreatedAt(ctx context.Context, authorUserID int64) (time.Time, bool, error) {
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT created_at
		FROM topics
		WHERE author_user_id = $1 AND status <> 'deleted'
		ORDER BY created_at DESC
		LIMIT 1
	`, authorUserID).Scan(&createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest author topic: %w", err)
	}
	return createdAt, true, nil
}

// CountAuthorTopicsSince 写路径冷却/日限额（非公开热路径）。M6 审计保留 COUNT。
func (s *PostgresStore) CountAuthorTopicsSince(ctx context.Context, authorUserID int64, since time.Time) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM topics
		WHERE author_user_id = $1 AND status <> 'deleted' AND created_at >= $2
	`, authorUserID, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count author topics: %w", err)
	}
	return count, nil
}

func (s *PostgresStore) LatestAuthorCommentCreatedAt(ctx context.Context, authorUserID int64) (time.Time, bool, error) {
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT created_at
		FROM comments
		WHERE author_user_id = $1 AND status <> 'deleted'
		ORDER BY created_at DESC
		LIMIT 1
	`, authorUserID).Scan(&createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest author comment: %w", err)
	}
	return createdAt, true, nil
}

// CountAuthorCommentsSince 写路径冷却/日限额（非公开热路径）。M6 审计保留 COUNT。
func (s *PostgresStore) CountAuthorCommentsSince(ctx context.Context, authorUserID int64, since time.Time) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM comments
		WHERE author_user_id = $1 AND status <> 'deleted' AND created_at >= $2
	`, authorUserID, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count author comments: %w", err)
	}
	return count, nil
}

// updateCommentLegacy is retained temporarily as a migration reference. M3
// uses updateCommentVersioned so ordinary edits append accepted versions.
func (s *PostgresStore) updateCommentLegacy(ctx context.Context, input UpdateCommentRecord) (Comment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin update comment: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var postID int64
	var topicID int64
	var parentID *int64
	var prevStatus string
	if err := tx.QueryRow(ctx, `
		SELECT content_id, topic_id, parent_comment_id, status
		FROM comments
		WHERE id = $1 AND status IN ('active', 'pending')
		FOR UPDATE
	`, input.CommentID).Scan(&postID, &topicID, &parentID, &prevStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrCommentNotFound
		}
		return Comment{}, fmt.Errorf("lock comment for update: %w", err)
	}
	if err := createPostRevision(ctx, tx, postID, input.EditorUserID); err != nil {
		return Comment{}, err
	}
	if err := updatePost(ctx, tx, postID, input.EditorUserID, input.Content); err != nil {
		return Comment{}, err
	}
	if input.RequeuePending {
		triggerSnapshot, err := json.Marshal(input.ModerationTriggers)
		if err != nil {
			return Comment{}, fmt.Errorf("encode comment moderation triggers: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE comments
			SET status = 'pending', moderation_triggers = $2, updated_at = now()
			WHERE id = $1
		`, input.CommentID, triggerSnapshot); err != nil {
			return Comment{}, fmt.Errorf("requeue comment pending: %w", err)
		}
		// 原 active 评论退审时回滚可见计数。
		if prevStatus == CommentStatusActive {
			if parentID != nil {
				if _, err := tx.Exec(ctx, `
					UPDATE comments
					SET reply_count = GREATEST(reply_count - 1, 0), updated_at = now()
					WHERE id = $1
				`, *parentID); err != nil {
					return Comment{}, fmt.Errorf("decrement parent reply count on requeue: %w", err)
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE topics
				SET comment_count = GREATEST(comment_count - 1, 0),
				    hot_score = `+TopicHotScoreSQL("GREATEST(comment_count - 1, 0)", "like_count")+`,
				    updated_at = now()
				WHERE id = $1
			`, topicID); err != nil {
				return Comment{}, fmt.Errorf("decrement topic comment count on requeue: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE categories
				SET comment_count = GREATEST(comment_count - 1, 0), updated_at = now()
				WHERE id = (SELECT category_id FROM topics WHERE id = $1)
			`, topicID); err != nil {
				return Comment{}, fmt.Errorf("decrement category comment count on requeue: %w", err)
			}
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE comments
		SET updated_at = now()
		WHERE id = $1
	`, input.CommentID); err != nil {
		return Comment{}, fmt.Errorf("touch comment: %w", err)
	}

	// 记录最近一次编辑 IP（创建 ip_address 保持不变）。
	if input.LastEditIP != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE comments SET last_edit_ip = $2 WHERE id = $1
		`, input.CommentID, input.LastEditIP); err != nil {
			return Comment{}, fmt.Errorf("update comment last_edit_ip: %w", err)
		}
	}
	if input.ReplaceAttachments {
		if err := replaceForumAttachmentReferences(ctx, tx, "comment", input.CommentID, input.EditorUserID, input.AttachmentIDs); err != nil {
			return Comment{}, err
		}
	}

	comment, err := getCommentByID(ctx, tx, input.CommentID, s.avatarBuilder)
	if err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit update comment: %w", err)
	}
	return comment, nil
}

func (s *PostgresStore) DeleteComment(ctx context.Context, commentID int64) (Comment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin delete comment: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var topicID int64
	var parentID *int64
	var prevStatus string
	if err := tx.QueryRow(ctx, `
		SELECT topic_id, parent_comment_id, status
		FROM comments
		WHERE id = $1 AND status <> 'deleted'
		FOR UPDATE
	`, commentID).Scan(&topicID, &parentID, &prevStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrCommentNotFound
		}
		return Comment{}, fmt.Errorf("lock comment for delete: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comments
		SET status = 'deleted', deleted_at = COALESCE(deleted_at, now()), updated_at = now()
		WHERE id = $1
	`, commentID); err != nil {
		return Comment{}, fmt.Errorf("soft delete comment: %w", err)
	}
	// 仅 active 评论曾 +1 计数，删除时对称回滚（对齐 moderation workbench）。
	if prevStatus == CommentStatusActive {
		if parentID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE comments
				SET reply_count = GREATEST(reply_count - 1, 0), updated_at = now()
				WHERE id = $1
			`, *parentID); err != nil {
				return Comment{}, fmt.Errorf("decrement parent reply count: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET comment_count = GREATEST(comment_count - 1, 0),
			    hot_score = `+TopicHotScoreSQL("GREATEST(comment_count - 1, 0)", "like_count")+`,
			    updated_at = now()
			WHERE id = $1
		`, topicID); err != nil {
			return Comment{}, fmt.Errorf("decrement topic comment count: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE categories
			SET comment_count = GREATEST(comment_count - 1, 0), updated_at = now()
			WHERE id = (SELECT category_id FROM topics WHERE id = $1)
		`, topicID); err != nil {
			return Comment{}, fmt.Errorf("decrement category comment count: %w", err)
		}
	}
	if err := replaceForumAttachmentReferences(ctx, tx, "comment", commentID, 0, nil); err != nil {
		return Comment{}, err
	}

	comment, err := getCommentByID(ctx, tx, commentID, s.avatarBuilder)
	if err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit delete comment: %w", err)
	}
	return comment, nil
}

func (s *PostgresStore) ListComments(ctx context.Context, input CommentListInput) (CommentList, error) {
	input.Page, input.PerPage = normalizePage(input.Page, input.PerPage)

	if input.View == "flat" {
		return s.listCommentsFlat(ctx, input)
	}
	offset := (input.Page - 1) * input.PerPage
	return s.listCommentsTree(ctx, input, offset)
}

// listCommentsTree 采用"根评论分页 + 有界子孙拉取"两步查询（D2）：
//  1. 对根评论(parent_comment_id IS NULL)按 path_key 分页。Total 为根评论数。
//  2. 每个根最多拉取 TreeDescendantsPerRoot 个子孙（默认 50）；截断时 root.hasMoreChildren=true，
//     更多回复走 ListCommentReplies。
func (s *PostgresStore) listCommentsTree(ctx context.Context, input CommentListInput, offset int) (CommentList, error) {
	capN := normalizeTreeDescendantsPerRoot(input.TreeDescendantsPerRoot)

	total, err := s.commentListTotalRoots(ctx, input)
	if err != nil {
		return CommentList{}, err
	}

	// 第一步：拉当页根评论。
	rootRows, err := s.pool.Query(ctx, commentSelectSQL()+`
		WHERE comments.topic_id = $1 AND comments.parent_comment_id IS NULL
		  AND (comments.status = 'active' OR ($2::boolean AND comments.status = 'deleted'
		    AND ($3::bigint = 0 OR comments.author_user_id = $3)))
		ORDER BY comments.path_key ASC, comments.id ASC
		LIMIT $4 OFFSET $5
	`, input.TopicID, input.IncludeDeleted, input.DeletedAuthorUserID, input.PerPage, offset)
	if err != nil {
		return CommentList{}, fmt.Errorf("list root comments: %w", err)
	}
	roots, err := scanCommentsWithAvatar(rootRows, s.avatarBuilder)
	rootRows.Close()
	if err != nil {
		return CommentList{}, err
	}
	if len(roots) == 0 {
		return CommentList{Items: []Comment{}, Total: total, Page: input.Page, PerPage: input.PerPage, View: "tree", HasMore: false}, nil
	}

	rootIDs := make([]int64, 0, len(roots))
	for _, r := range roots {
		rootIDs = append(rootIDs, r.ID)
	}

	// 各根子孙总数：用于 hasMoreChildren（比拉 N+1 行更清晰，且仍走 root 索引）。
	descendantCounts, err := s.countDescendantsByRoot(ctx, input.TopicID, rootIDs, input.IncludeDeleted, input.DeletedAuthorUserID)
	if err != nil {
		return CommentList{}, err
	}

	// 第二步：每根最多 capN 个子孙（path_key 序），禁止无界加载热帖整棵树。
	descRows, err := s.pool.Query(ctx, commentSelectSQL()+`
		WHERE comments.id IN (
			SELECT id FROM (
				SELECT comments.id,
				       ROW_NUMBER() OVER (
				         PARTITION BY comments.root_comment_id
				         ORDER BY comments.path_key ASC, comments.id ASC
				       ) AS rn
				FROM comments
				WHERE comments.topic_id = $1
				  AND comments.parent_comment_id IS NOT NULL
				  AND comments.root_comment_id = ANY($2::bigint[])
				  AND (comments.status = 'active' OR ($3::boolean AND comments.status = 'deleted'
				    AND ($4::bigint = 0 OR comments.author_user_id = $4)))
			) ranked
			WHERE rn <= $5
		)
		ORDER BY comments.path_key ASC, comments.id ASC
	`, input.TopicID, rootIDs, input.IncludeDeleted, input.DeletedAuthorUserID, capN)
	if err != nil {
		return CommentList{}, fmt.Errorf("list comment descendants: %w", err)
	}
	descendants, err := scanCommentsWithAvatar(descRows, s.avatarBuilder)
	descRows.Close()
	if err != nil {
		return CommentList{}, err
	}

	// 标记被截断的根：子孙总数 > cap。
	for i := range roots {
		if descendantCounts[roots[i].ID] > int64(capN) {
			roots[i].HasMoreChildren = true
		}
	}

	all := make([]Comment, 0, len(roots)+len(descendants))
	all = append(all, roots...)
	all = append(all, descendants...)
	tree := buildCommentTree(all)
	// buildCommentTree 重建 Children，需把 HasMoreChildren 从 roots 写回树根。
	hasMoreByRoot := make(map[int64]bool, len(roots))
	for _, r := range roots {
		if r.HasMoreChildren {
			hasMoreByRoot[r.ID] = true
		}
	}
	for i := range tree {
		if hasMoreByRoot[tree[i].ID] {
			tree[i].HasMoreChildren = true
		}
	}
	// tree 根分页仍用 OFFSET；hasMore 由 total 推导（根数可 COUNT）。
	loadedThrough := int64((input.Page-1)*input.PerPage + len(roots))
	hasMore := loadedThrough < total
	return CommentList{
		Items: tree, Total: total, Page: input.Page, PerPage: input.PerPage,
		View: "tree", HasMore: hasMore,
	}, nil
}

// commentListTotalRoots tree 视图 total = 根评论数（公开路径仅 active）。
// M6：无 root_comment_count 冗余列；COUNT 仅在 ListComments 缓存 miss 时执行（topic 级 gen + 短 TTL）。
// 全表/跨主题 COUNT 已禁止；本查询带 topic_id + parent IS NULL，走局部索引。
func (s *PostgresStore) commentListTotalRoots(ctx context.Context, input CommentListInput) (int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM comments
		WHERE topic_id = $1 AND parent_comment_id IS NULL
		  AND (status = 'active' OR ($2::boolean AND status = 'deleted'
		    AND ($3::bigint = 0 OR author_user_id = $3)))
	`, input.TopicID, input.IncludeDeleted, input.DeletedAuthorUserID).Scan(&total); err != nil {
		return 0, fmt.Errorf("count root comments: %w", err)
	}
	return total, nil
}

func (s *PostgresStore) countDescendantsByRoot(
	ctx context.Context,
	topicID int64,
	rootIDs []int64,
	includeDeleted bool,
	deletedAuthorUserID int64,
) (map[int64]int64, error) {
	out := make(map[int64]int64, len(rootIDs))
	if len(rootIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT root_comment_id, count(*)::bigint
		FROM comments
		WHERE topic_id = $1
		  AND parent_comment_id IS NOT NULL
		  AND root_comment_id = ANY($2::bigint[])
		  AND (status = 'active' OR ($3::boolean AND status = 'deleted'
		    AND ($4::bigint = 0 OR author_user_id = $4)))
		GROUP BY root_comment_id
	`, topicID, rootIDs, includeDeleted, deletedAuthorUserID)
	if err != nil {
		return nil, fmt.Errorf("count descendants by root: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rootID, n int64
		if err := rows.Scan(&rootID, &n); err != nil {
			return nil, fmt.Errorf("scan descendant count: %w", err)
		}
		out[rootID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeTreeDescendantsPerRoot 将配置夹到 [1,100]；0/负值用推荐默认 50。
func normalizeTreeDescendantsPerRoot(n int) int {
	if n <= 0 {
		return RecommendedTreeDescendantsPerRoot
	}
	if n < HardTreeDescendantsPerRootMin {
		return HardTreeDescendantsPerRootMin
	}
	if n > HardTreeDescendantsPerRootMax {
		return HardTreeDescendantsPerRootMax
	}
	return n
}

// scanComments 扫描 rows 到 []Comment，统一处理 rows.Err 与遍历错误。
func scanComments(rows pgx.Rows) ([]Comment, error) {
	return scanCommentsWithAvatar(rows, nil)
}

func scanCommentsWithAvatar(rows pgx.Rows, builder *avatar.ViewBuilder) ([]Comment, error) {
	items := []Comment{}
	for rows.Next() {
		comment, err := scanCommentWithAvatar(rows, builder)
		if err != nil {
			return nil, err
		}
		items = append(items, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comments: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) ListCommentReplies(ctx context.Context, input CommentReplyListInput) ([]Comment, error) {
	rows, err := s.pool.Query(ctx, commentSelectSQL()+`
		WHERE comments.parent_comment_id = $1
		  AND (comments.status = 'active' OR ($2::boolean AND comments.status = 'deleted'
		    AND ($3::bigint = 0 OR comments.author_user_id = $3)))
		ORDER BY comments.path_key ASC, comments.id ASC
	`, input.CommentID, input.IncludeDeleted, input.DeletedAuthorUserID)
	if err != nil {
		return nil, fmt.Errorf("list comment replies: %w", err)
	}
	defer rows.Close()

	items := []Comment{}
	for rows.Next() {
		comment, err := scanCommentWithAvatar(rows, s.avatarBuilder)
		if err != nil {
			return nil, err
		}
		items = append(items, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comment replies: %w", err)
	}
	return items, nil
}

type sqlExecutor interface {
	Exec(context.Context, string, ...any) (pgconnTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type pgconnTag interface {
	RowsAffected() int64
}

// RowScanner 是行扫描抽象，供 store 内部及跨模型复用（如 Profile 复用主题摘要扫描）。
type RowScanner interface {
	Scan(dest ...any) error
}

func scanCategory(row RowScanner) (Category, error) {
	var item Category
	if err := row.Scan(
		&item.ID,
		&item.GroupID,
		&item.GroupSlug,
		&item.GroupName,
		&item.Slug,
		&item.Name,
		&item.Description,
		&item.Icon,
		&item.IconColor,
		&item.Visibility,
		&item.Position,
		&item.DefaultSort,
		&item.TopicCount,
		&item.CommentCount,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return Category{}, fmt.Errorf("scan category: %w", err)
	}
	return item, nil
}

func scanCategoryGroup(row RowScanner) (CategoryGroup, error) {
	var item CategoryGroup
	if err := row.Scan(
		&item.ID,
		&item.Slug,
		&item.Name,
		&item.Description,
		&item.Visibility,
		&item.Position,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return CategoryGroup{}, fmt.Errorf("scan category group: %w", err)
	}
	item.Categories = []Category{}
	return item, nil
}

func scanCategoryGroupRow(row RowScanner) (CategoryGroup, Category, bool, error) {
	var group CategoryGroup
	var categoryID sql.NullInt64
	var categoryGroupID sql.NullInt64
	var categorySlug sql.NullString
	var categoryName sql.NullString
	var categoryDescription sql.NullString
	var categoryIcon sql.NullString
	var categoryIconColor sql.NullString
	var categoryVisibility sql.NullString
	var categoryPosition sql.NullInt64
	var categoryDefaultSort sql.NullString
	var categoryTopicCount sql.NullInt64
	var categoryCommentCount sql.NullInt64
	var categoryCreatedAt sql.NullTime
	var categoryUpdatedAt sql.NullTime

	if err := row.Scan(
		&group.ID,
		&group.Slug,
		&group.Name,
		&group.Description,
		&group.Visibility,
		&group.Position,
		&group.CreatedAt,
		&group.UpdatedAt,
		&categoryID,
		&categoryGroupID,
		&categorySlug,
		&categoryName,
		&categoryDescription,
		&categoryIcon,
		&categoryIconColor,
		&categoryVisibility,
		&categoryPosition,
		&categoryDefaultSort,
		&categoryTopicCount,
		&categoryCommentCount,
		&categoryCreatedAt,
		&categoryUpdatedAt,
	); err != nil {
		return CategoryGroup{}, Category{}, false, fmt.Errorf("scan category group: %w", err)
	}
	if !categoryID.Valid {
		return group, Category{}, false, nil
	}
	category := Category{
		ID:           categoryID.Int64,
		GroupID:      categoryGroupID.Int64,
		GroupSlug:    group.Slug,
		GroupName:    group.Name,
		Slug:         categorySlug.String,
		Name:         categoryName.String,
		Description:  categoryDescription.String,
		Icon:         categoryIcon.String,
		IconColor:    categoryIconColor.String,
		Visibility:   categoryVisibility.String,
		Position:     int(categoryPosition.Int64),
		DefaultSort:  categoryDefaultSort.String,
		TopicCount:   categoryTopicCount.Int64,
		CommentCount: categoryCommentCount.Int64,
		CreatedAt:    categoryCreatedAt.Time,
		UpdatedAt:    categoryUpdatedAt.Time,
	}
	return group, category, true, nil
}

func scanTag(row RowScanner) (Tag, error) {
	var item Tag
	if err := row.Scan(
		&item.ID,
		&item.Slug,
		&item.Name,
		&item.Description,
		&item.Icon,
		&item.IconColor,
		&item.Status,
		&item.TopicCount,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return Tag{}, fmt.Errorf("scan tag: %w", err)
	}
	return item, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

// lastReplyAuthorJoinSQL：取最近一条 active 评论作者；无评论时 COALESCE 回楼主。
// 仅挂在带头像的摘要 SELECT（列表 / GetTopicFor*），不改 Profile 的 ScanTopicSummary 列布局。
func lastReplyAuthorJoinSQL() string {
	return `
		LEFT JOIN LATERAL (
		  SELECT c.author_user_id
		  FROM comments c
		  WHERE c.topic_id = topics.id
		    AND c.status = 'active'
		    AND c.author_user_id IS NOT NULL
		  ORDER BY c.created_at DESC, c.id DESC
		  LIMIT 1
		) last_reply ON true
		LEFT JOIN users last_reply_users ON last_reply_users.id = COALESCE(last_reply.author_user_id, topics.author_user_id)
		LEFT JOIN user_profiles last_reply_profiles ON last_reply_profiles.user_id = last_reply_users.id
		LEFT JOIN attachments last_reply_attachments ON last_reply_attachments.id = last_reply_profiles.avatar_attachment_id
	`
}

func lastReplyAuthorSelectSQL() string {
	return `
		  last_reply_users.id, last_reply_users.username, last_reply_users.display_name, last_reply_users.email,
		  last_reply_profiles.avatar_attachment_id,
		  last_reply_attachments.id, last_reply_attachments.public_id, last_reply_attachments.owner_user_id,
		  last_reply_attachments.content_type, last_reply_attachments.status
	`
}

func topicDetailSQL() string {
	// 详情 SELECT：只取公开正文投影 + 作者头像；excerpt 在 scan 时从 plain 派生。
	// ContentEdited 基于有效 currentRevision > 1；混合迁移期只在当前行上用 revision 索引计数。
	// 不拉 revision 正文，避免详情路径扫历史快照。
	return `
		SELECT topics.id, topics.category_id, categories.slug, categories.name,
		  topics.author_user_id, users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  topics.title, topics.slug, topics.status, topics.is_pinned,
		  topics.comment_count, topics.view_count, topics.like_count,
		  ` + effectivePostCurrentRevisionSQL("posts") + `,
		  ` + contentEditedSQL("posts") + `,
		  ` + contentEditedAtSQL("posts") + `,
		  topics.created_at, topics.updated_at, topics.last_activity_at,
		  posts.id, posts.html_content, posts.plain_text, posts.render_version,
		  ` + topBadgeSelectSQL() + `
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		JOIN posts ON posts.id = topics.content_id
		LEFT JOIN users ON users.id = topics.author_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id` + topBadgeJoinSQL() + `
	`
}

func scanTopicSummary(row RowScanner) (TopicSummary, error) {
	var topic TopicSummary
	var authorID sql.NullInt64
	var username sql.NullString
	var displayName sql.NullString
	var plainPrefix string
	if err := row.Scan(
		&topic.ID,
		&topic.CategoryID,
		&topic.CategorySlug,
		&topic.CategoryName,
		&authorID,
		&username,
		&displayName,
		&topic.Title,
		&topic.Slug,
		&topic.Status,
		&topic.IsPinned,
		&topic.CommentCount,
		&topic.ViewCount,
		&topic.LikeCount,
		&topic.HotScore,
		&plainPrefix,
		&topic.CurrentRevision,
		&topic.ContentEdited,
		&topic.CreatedAt,
		&topic.UpdatedAt,
		&topic.LastActivityAt,
	); err != nil {
		return TopicSummary{}, err
	}
	// 无 settings 上下文时用推荐默认；Service 读路径会按运营配置再截断。
	topic.Excerpt = ExcerptFromPlain(plainPrefix, defaultExcerptRuneLimit)
	if authorID.Valid {
		topic.AuthorUserID = authorID.Int64
		topic.Author = userSummaryWithAvatar(nil, authorID, username, displayName, sql.NullString{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{})
	}
	return topic, nil
}

// ScanTopicSummary 导出主题摘要扫描，供 Profile 等跨模型复用同一 SELECT 列布局。
func ScanTopicSummary(row RowScanner) (TopicSummary, error) {
	return scanTopicSummary(row)
}

func userSummaryWithAvatar(builder *avatar.ViewBuilder, userID sql.NullInt64, username sql.NullString, displayName sql.NullString, email sql.NullString, avatarAttachmentID sql.NullInt64, attachmentID sql.NullInt64, attachmentPublicID sql.NullString, attachmentOwnerID sql.NullInt64, attachmentContentType sql.NullString, attachmentStatus sql.NullString) *UserSummary {
	if !userID.Valid {
		return nil
	}
	if builder == nil {
		builder = avatar.NewViewBuilder(nil)
	}
	user := UserSummary{
		ID:          userID.Int64,
		Username:    username.String,
		DisplayName: displayName.String,
	}
	user.Avatar = builder.AvatarView(context.Background(), avatar.User{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Email:       email.String,
	}, avatarSourceFromSQL(avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus))
	return &user
}

func avatarSourceFromSQL(avatarAttachmentID sql.NullInt64, attachmentID sql.NullInt64, attachmentPublicID sql.NullString, attachmentOwnerID sql.NullInt64, attachmentContentType sql.NullString, attachmentStatus sql.NullString) avatar.Source {
	source := avatar.Source{}
	if avatarAttachmentID.Valid && avatarAttachmentID.Int64 > 0 {
		id := avatarAttachmentID.Int64
		source.AttachmentID = &id
	}
	if attachmentID.Valid && attachmentID.Int64 > 0 {
		source.Attachment = &avatar.Attachment{
			ID:          attachmentID.Int64,
			PublicID:    attachmentPublicID.String,
			OwnerUserID: nullableInt64FromSQL(attachmentOwnerID),
			ContentType: attachmentContentType.String,
			Status:      attachmentStatus.String,
		}
	}
	return source
}

func nullableInt64FromSQL(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func scanCommentSummary(row RowScanner) (CommentSummary, error) {
	var summary CommentSummary
	var authorID sql.NullInt64
	var parentID sql.NullInt64
	if err := row.Scan(&summary.ID, &summary.TopicID, &authorID, &parentID, &summary.RootCommentID, &summary.PathKey, &summary.Depth, &summary.Status, &summary.CreatedAt, &summary.CurrentRevision); err != nil {
		return CommentSummary{}, err
	}
	if authorID.Valid {
		summary.AuthorUserID = authorID.Int64
	}
	if parentID.Valid {
		summary.ParentID = &parentID.Int64
	}
	return summary, nil
}

func buildCommentTree(items []Comment) []Comment {
	childrenByParent := make(map[int64][]Comment)
	roots := []Comment{}
	for _, comment := range items {
		comment.Children = nil
		if comment.ParentID == nil {
			roots = append(roots, comment)
			continue
		}
		childrenByParent[*comment.ParentID] = append(childrenByParent[*comment.ParentID], comment)
	}

	var attachChildren func(Comment) Comment
	attachChildren = func(comment Comment) Comment {
		for _, child := range childrenByParent[comment.ID] {
			comment.Children = append(comment.Children, attachChildren(child))
		}
		return comment
	}

	for index := range roots {
		roots[index] = attachChildren(roots[index])
	}
	return roots
}
