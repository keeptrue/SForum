package forum

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ApplyBookmark 在一个事务里完成「关系行 + 公开计数列」。
//
// 幂等语义：已有同可见性行的重复 PUT 不重复计数；可见性切换的 ±1 与行更新
// 同事务，计数漂移只可能来自 bug，不来自并发。计数只统计 visibility='public'：
// private→public +1、public→private -1、public 删除 -1、private 删除不变。
func (s *PostgresStore) ApplyBookmark(ctx context.Context, input BookmarkApplyInput) (BookmarkState, error) {
	if s == nil || s.pool == nil {
		return BookmarkState{}, ErrInvalidBookmarkTarget
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BookmarkState{}, fmt.Errorf("begin bookmark: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	applied, err := applyBookmarkRow(ctx, tx, input)
	if err != nil {
		return BookmarkState{}, err
	}
	count, err := applyBookmarkCount(ctx, tx, input, applied)
	if err != nil {
		return BookmarkState{}, err
	}
	// 计划 D4：收藏/取消收藏是关系行增删，bookmark_count 跟着回退；
	// 只改可见性（私密 ↔ 公开）不改变关系行，relationDelta=0 自然 no-op。
	if err := bumpUserActivityTx(ctx, tx, input.ActorUserID, activityMetricBookmarkCount, int64(applied.relationDelta)); err != nil {
		return BookmarkState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BookmarkState{}, fmt.Errorf("commit bookmark: %w", err)
	}
	// D2：收藏/取消后触发评估（收藏家徽章依赖 bookmark_count）。
	if applied.relationDelta != 0 {
		s.enqueueBadgeEvaluation(ctx, input.ActorUserID)
	}
	return BookmarkState{
		TargetType:          input.TargetType,
		TargetID:            input.TargetID,
		Visibility:          applied.visibility,
		Bookmarked:          input.Add,
		PublicBookmarkCount: count,
		RemindAt:            applied.remindAt,
		RemindedAt:          applied.remindedAt,
	}, nil
}

// bookmarkApplyOutcome 是一次行写入对计数的影响与最终行状态。
type bookmarkApplyOutcome struct {
	// delta 是 public_bookmark_count 的增量（+1/-1/0）。
	delta int
	// relationDelta 是「收藏关系行」的增量（+1/-1/0），与可见性无关：
	// 新建私密收藏 delta=0 但 relationDelta=1，改成公开 delta=+1 而 relationDelta=0。
	// user_activity.bookmark_count 记的是「收藏过多少条」，必须用 relationDelta。
	relationDelta int
	visibility    string
	remindAt      *time.Time
	remindedAt    *time.Time
}

// applyBookmarkRow 写/删收藏行。
//
// 提醒字段是三态：ClearReminder 清空、RemindAt 设置（并清掉 reminded_at）、
// 两者都没有则保持原值。旧实现只在 RemindAt != nil 时更新，导致「清除提醒」
// 永远写不掉旧值——响应回填旧时间，前端弹回原值。
func applyBookmarkRow(ctx context.Context, tx pgx.Tx, input BookmarkApplyInput) (bookmarkApplyOutcome, error) {
	visibility := input.Visibility
	if visibility != "" && visibility != BookmarkVisibilityPrivate && visibility != BookmarkVisibilityPublic {
		return bookmarkApplyOutcome{}, ErrInvalidBookmarkTarget
	}
	if input.Add {
		if input.RemindAt != nil {
			if err := ensureBookmarkReminderCapacity(ctx, tx, input); err != nil {
				return bookmarkApplyOutcome{}, err
			}
		}
		// 新行：ON CONFLICT DO NOTHING 区分「新建」与「已存在」，并发 PUT 只有一条赢。
		var id int64
		if visibility == "" {
			visibility = BookmarkVisibilityPrivate
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility, remind_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT ON CONSTRAINT content_bookmarks_unique DO NOTHING
			RETURNING id
		`, input.ActorUserID, input.TargetType, input.TargetID, visibility, input.RemindAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return updateExistingBookmarkRow(ctx, tx, input)
		}
		if err != nil {
			return bookmarkApplyOutcome{}, fmt.Errorf("insert bookmark: %w", err)
		}
		delta := 0
		if visibility == BookmarkVisibilityPublic {
			delta = 1
		}
		return bookmarkApplyOutcome{delta: delta, relationDelta: 1, visibility: visibility, remindAt: input.RemindAt}, nil
	}
	var (
		deletedVisibility string
		remindAt          *time.Time
		remindedAt        *time.Time
	)
	err := tx.QueryRow(ctx, `
		DELETE FROM content_bookmarks
		WHERE user_id = $1 AND target_type = $2 AND target_id = $3
		RETURNING visibility, remind_at, reminded_at
	`, input.ActorUserID, input.TargetType, input.TargetID).Scan(&deletedVisibility, &remindAt, &remindedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return bookmarkApplyOutcome{visibility: BookmarkVisibilityPrivate}, nil
	}
	if err != nil {
		return bookmarkApplyOutcome{}, fmt.Errorf("delete bookmark: %w", err)
	}
	delta := 0
	if deletedVisibility == BookmarkVisibilityPublic {
		delta = -1
	}
	return bookmarkApplyOutcome{delta: delta, relationDelta: -1, visibility: deletedVisibility}, nil
}

// ensureBookmarkReminderCapacity 在同一事务里数该用户未到期提醒。
//
// 不做串行化：两个并发请求可能同时看到 limit-1 并各自插入，最终略超上限。
// 这是刻意的取舍——提醒是低频个人动作，为它引入 per-user 咨询锁会让收藏写
// 路径互相等待，而提醒 job 是批量扫描，多几条只是下一轮多处理几条。
// 排除当前目标行：更新已有提醒不该被自己占的名额挡下。
func ensureBookmarkReminderCapacity(ctx context.Context, tx pgx.Tx, input BookmarkApplyInput) error {
	var pending int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM content_bookmarks
		WHERE user_id = $1
		  AND remind_at IS NOT NULL
		  AND reminded_at IS NULL
		  AND remind_at > now()
		  AND NOT (target_type = $2 AND target_id = $3)
	`, input.ActorUserID, input.TargetType, input.TargetID).Scan(&pending); err != nil {
		return fmt.Errorf("count pending bookmark reminders: %w", err)
	}
	if pending >= BookmarkReminderLimit {
		return ErrBookmarkReminderLimit
	}
	return nil
}

// updateExistingBookmarkRow 处理「行已存在」的 PUT：可见性切换与提醒三态在一条
// UPDATE 里完成，返回最终行状态与计数增量（无变化时增量为 0，不重复计数）。
func updateExistingBookmarkRow(ctx context.Context, tx pgx.Tx, input BookmarkApplyInput) (bookmarkApplyOutcome, error) {
	var current string
	if err := tx.QueryRow(ctx, `
		SELECT visibility
		FROM content_bookmarks
		WHERE user_id = $1 AND target_type = $2 AND target_id = $3
		FOR UPDATE
	`, input.ActorUserID, input.TargetType, input.TargetID).Scan(&current); err != nil {
		return bookmarkApplyOutcome{}, fmt.Errorf("lock bookmark: %w", err)
	}
	nextVisibility := current
	if input.Visibility != "" {
		nextVisibility = input.Visibility
	}
	delta := 0
	switch {
	case current == BookmarkVisibilityPrivate && nextVisibility == BookmarkVisibilityPublic:
		delta = 1
	case current == BookmarkVisibilityPublic && nextVisibility == BookmarkVisibilityPrivate:
		delta = -1
	}
	var (
		remindAt   *time.Time
		remindedAt *time.Time
	)
	// 提醒三态：清空 / 设置（并清掉已提醒标记）/ 保持不变。
	if err := tx.QueryRow(ctx, `
		UPDATE content_bookmarks
		SET visibility = $4,
		    remind_at = CASE WHEN $5 THEN NULL ELSE COALESCE($6::timestamptz, remind_at) END,
		    reminded_at = CASE WHEN $5 THEN NULL WHEN $6::timestamptz IS NOT NULL THEN NULL ELSE reminded_at END,
		    updated_at = now()
		WHERE user_id = $1 AND target_type = $2 AND target_id = $3
		RETURNING remind_at, reminded_at
	`, input.ActorUserID, input.TargetType, input.TargetID, nextVisibility, input.ClearReminder, input.RemindAt).Scan(&remindAt, &remindedAt); err != nil {
		return bookmarkApplyOutcome{}, fmt.Errorf("update bookmark: %w", err)
	}
	return bookmarkApplyOutcome{delta: delta, visibility: nextVisibility, remindAt: remindAt, remindedAt: remindedAt}, nil
}

// applyBookmarkCount 在同一事务里维护公开收藏计数；delta=0 时读回现值。
func applyBookmarkCount(ctx context.Context, tx pgx.Tx, input BookmarkApplyInput, outcome bookmarkApplyOutcome) (int64, error) {
	switch input.TargetType {
	case BookmarkTargetTopic, BookmarkTargetComment:
	default:
		return 0, ErrInvalidBookmarkTarget
	}
	table := "topics"
	if input.TargetType == BookmarkTargetComment {
		table = "comments"
	}
	if outcome.delta == 0 {
		var count int64
		if err := tx.QueryRow(ctx, `SELECT public_bookmark_count FROM `+table+` WHERE id = $1`, input.TargetID).Scan(&count); err != nil {
			return 0, fmt.Errorf("read bookmark count: %w", err)
		}
		return count, nil
	}
	var count int64
	if err := tx.QueryRow(ctx, `
		UPDATE `+table+`
		SET public_bookmark_count = GREATEST(public_bookmark_count + $2, 0)
		WHERE id = $1
		RETURNING public_bookmark_count
	`, input.TargetID, outcome.delta).Scan(&count); err != nil {
		return 0, fmt.Errorf("update bookmark count: %w", err)
	}
	return count, nil
}

// BookmarkedTargetIDs 一次查询回答一页里「我收藏过哪些」，避免 N+1。
func (s *PostgresStore) BookmarkedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error) {
	marked := make(map[int64]struct{}, len(targetIDs))
	if s == nil || s.pool == nil || viewerUserID <= 0 || len(targetIDs) == 0 {
		return marked, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT target_id
		FROM content_bookmarks
		WHERE user_id = $1 AND target_type = $2 AND target_id = ANY($3)
	`, viewerUserID, targetType, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("list bookmarked targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		marked[id] = struct{}{}
	}
	return marked, rows.Err()
}

// bookmarkTargetVisibleExpr 与 sforum_core_v1.forum_bookmarks 的可见性谓词同构：
// 公开名单与到期提醒都只认仍然可见的目标。谓词只写一次，两处引用同一常量，
// 避免「名单可见但提醒发给已删内容」这类漂移。
const bookmarkTargetVisibleExpr = `(
    (
      bookmarks.target_type = 'topic'
      AND EXISTS (
        SELECT 1 FROM topics
        JOIN categories ON categories.id = topics.category_id
        WHERE topics.id = bookmarks.target_id
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
    OR (
      bookmarks.target_type = 'comment'
      AND EXISTS (
        SELECT 1 FROM comments
        JOIN topics ON topics.id = comments.topic_id
        JOIN categories ON categories.id = topics.category_id
        WHERE comments.id = bookmarks.target_id
          AND comments.status = 'active'
          AND comments.deleted_at IS NULL
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
  )`

const bookmarkTargetVisibleSQL = "\n  AND " + bookmarkTargetVisibleExpr

// ListUserBookmarks 返回行主的收藏名单。includePrivate=false（他人视角）只出
// 公开行且要求目标仍可见；行主本人（true）看全部，目标摘要缺失（已删/隐藏）
// 时仅回元数据，不虚构摘要。
func (s *PostgresStore) ListUserBookmarks(ctx context.Context, ownerUserID int64, includePrivate bool, limit, offset int) (BookmarkList, error) {
	list := BookmarkList{Items: []BookmarkEntry{}}
	if s == nil || s.pool == nil || ownerUserID <= 0 {
		return list, nil
	}
	visibilityFilter := " AND bookmarks.visibility = 'public'"
	if includePrivate {
		visibilityFilter = ""
	}

	// COUNT 必须拼同一个可见性谓词：只按 visibility 数会让「目标已删/隐藏」的
	// 行计入 Total，于是 HasMore 变成假的「加载更多」，而后续页是空的。
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM content_bookmarks AS bookmarks
		WHERE bookmarks.user_id = $1`+visibilityFilter+bookmarkTargetVisibleSQL, ownerUserID).Scan(&list.Total); err != nil {
		return list, fmt.Errorf("count user bookmarks: %w", err)
	}
	if list.Total == 0 || offset >= BookmarkListMaxOffset {
		return list, nil
	}

	type bookmarkRow struct {
		targetType string
		targetID   int64
		visibility string
		createdAt  time.Time
		remindAt   *time.Time
		remindedAt *time.Time
	}
	reminderColumns, reminderArgs := bookmarkListReminderColumns(includePrivate)
	args := append([]any{ownerUserID, limit, offset}, reminderArgs...)
	rows, err := s.pool.Query(ctx, `
		SELECT bookmarks.target_type, bookmarks.target_id, bookmarks.visibility, bookmarks.created_at,
		  `+reminderColumns+`
		FROM content_bookmarks AS bookmarks
		WHERE bookmarks.user_id = $1`+visibilityFilter+bookmarkTargetVisibleSQL+`
		ORDER BY bookmarks.created_at DESC, bookmarks.id DESC
		LIMIT $2 OFFSET $3
	`, args...)
	if err != nil {
		return list, fmt.Errorf("list user bookmarks: %w", err)
	}
	defer rows.Close()
	var entries []BookmarkEntry
	for rows.Next() {
		var row bookmarkRow
		if err := rows.Scan(&row.targetType, &row.targetID, &row.visibility, &row.createdAt, &row.remindAt, &row.remindedAt); err != nil {
			return list, err
		}
		entry := BookmarkEntry{
			TargetType: row.targetType,
			TargetID:   row.targetID,
			Visibility: row.visibility,
			CreatedAt:  row.createdAt,
			RemindAt:   row.remindAt,
			RemindedAt: row.remindedAt,
		}
		if !includePrivate {
			// 第二道闸（防御纵深）：SQL 层已把两列置 NULL，这里再清一次，
			// 防止未来有人改 SELECT 时漏掉 CASE 而把私有计划数据漏出去。
			entry.RemindAt = nil
			entry.RemindedAt = nil
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return list, err
	}

	if err := s.hydrateBookmarkSummaries(ctx, entries); err != nil {
		return list, err
	}
	list.Items = entries
	list.HasMore = int64(offset+len(entries)) < list.Total
	return list, nil
}

// bookmarkListReminderColumns 构造收藏名单的提醒列表达式与追加参数。
//
// 提醒是行主私有计划数据（owner-only）：他人视角（includePrivate=false）用
// CASE WHEN $4 把两列置 NULL，让私有调度数据根本不进 DTO；行主视角直接读列。
// 布尔参数固定 $4（在 LIMIT/OFFSET 的 $2/$3 之后追加），参数顺序因此稳定。
//
// 抽成纯函数是为了让「SQL 层那道闸」可以被静态断言锁定：端到端用例里 Go 侧
// 兜底会遮蔽它，删掉 CASE 也仍然全绿。
func bookmarkListReminderColumns(includePrivate bool) (string, []any) {
	if includePrivate {
		return "bookmarks.remind_at, bookmarks.reminded_at", nil
	}
	return "CASE WHEN $4 THEN bookmarks.remind_at END, CASE WHEN $4 THEN bookmarks.reminded_at END", []any{false}
}

// hydrateBookmarkSummaries 按目标类型分两批补齐主题/评论摘要。
// 摘要缺失（软删、隐藏）不报错：entry 保留元数据，调用方据 omitempty 呈现。
func (s *PostgresStore) hydrateBookmarkSummaries(ctx context.Context, entries []BookmarkEntry) error {
	if len(entries) == 0 {
		return nil
	}
	topicIDs := make([]int64, 0, len(entries))
	commentIDs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		switch entry.TargetType {
		case BookmarkTargetTopic:
			topicIDs = append(topicIDs, entry.TargetID)
		case BookmarkTargetComment:
			commentIDs = append(commentIDs, entry.TargetID)
		}
	}
	if len(topicIDs) > 0 {
		// 直接复用 topicSummarySQL 的 JOIN 形状再补 WHERE：包一层子查询会因为
		// 派生表里出现多个同名 id 列（topics/users/attachments）而让 summaries.id
		// 变成歧义引用。
		rows, err := s.pool.Query(ctx, topicSummarySQL()+` WHERE topics.id = ANY($1)`, topicIDs)
		if err != nil {
			return fmt.Errorf("hydrate bookmark topics: %w", err)
		}
		topics := map[int64]TopicSummary{}
		for rows.Next() {
			summary, err := scanTopicSummaryWithAvatar(rows, s.avatarBuilder)
			if err != nil {
				rows.Close()
				return err
			}
			topics[summary.ID] = summary
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for i := range entries {
			if entries[i].TargetType == BookmarkTargetTopic {
				if summary, ok := topics[entries[i].TargetID]; ok {
					entries[i].Topic = &summary
				}
			}
		}
	}
	if len(commentIDs) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT comments.id, comments.topic_id, comments.author_user_id, comments.parent_comment_id,
			  COALESCE(comments.root_comment_id, comments.id), comments.path_key, comments.depth,
			  comments.status, comments.created_at, posts.current_revision
			FROM comments JOIN posts ON posts.id = comments.content_id
			WHERE comments.id = ANY($1)
		`, commentIDs)
		if err != nil {
			return fmt.Errorf("hydrate bookmark comments: %w", err)
		}
		comments := map[int64]CommentSummary{}
		for rows.Next() {
			summary, err := scanCommentSummary(rows)
			if err != nil {
				rows.Close()
				return err
			}
			comments[summary.ID] = summary
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for i := range entries {
			if entries[i].TargetType != BookmarkTargetComment {
				continue
			}
			summary, ok := comments[entries[i].TargetID]
			if !ok {
				continue
			}
			entries[i].Comment = &BookmarkCommentRef{
				ID:              summary.ID,
				TopicID:         summary.TopicID,
				AuthorUserID:    summary.AuthorUserID,
				Depth:           summary.Depth,
				Status:          summary.Status,
				CurrentRevision: summary.CurrentRevision,
				CreatedAt:       summary.CreatedAt,
			}
		}
	}
	return nil
}
