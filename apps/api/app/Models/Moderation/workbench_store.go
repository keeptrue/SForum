package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

func (s *PostgresStore) QueueCounts(ctx context.Context) (QueueCounts, error) {
	var counts QueueCounts
	// pending/open 对齐对应列表；historyTotal 对齐全量历史列表；processedToday 仅作今日 KPI。
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM topics WHERE status = 'pending') +
		    (SELECT count(*) FROM comments WHERE status = 'pending'),
		  (SELECT count(*) FROM moderation_reports WHERE status IN ('open', 'reviewing')),
		  (SELECT count(*) FROM moderation_decisions WHERE created_at >= date_trunc('day', now())),
		  (SELECT count(*) FROM moderation_decisions)
	`).Scan(&counts.PendingContent, &counts.OpenReports, &counts.ProcessedToday, &counts.HistoryTotal)
	if err != nil {
		return QueueCounts{}, fmt.Errorf("load moderation queue counts: %w", err)
	}
	return counts, nil
}

func (s *PostgresStore) ListPending(ctx context.Context, input WorkbenchListInput) (PendingList, error) {
	// posts.excerpt 已删除：取 plain_text 前缀，扫描后按论坛推荐摘要长度截断。
	const pendingCTE = `
		WITH pending AS (
		  SELECT 'topic'::text AS target_type, topics.id AS target_id, topics.id AS topic_id,
		    topics.title, left(posts.plain_text, 2000) AS plain_prefix, topics.author_user_id AS author_id,
		    COALESCE(NULLIF(users.display_name, ''), users.username, '') AS author_name,
		    categories.name AS category, topics.moderation_triggers AS triggers, topics.created_at,
		    COALESCE(topics.ip_address, '') AS ip_address, COALESCE(topics.last_edit_ip, '') AS last_edit_ip
		  FROM topics
		  JOIN posts ON posts.id = topics.content_id
		  JOIN categories ON categories.id = topics.category_id
		  LEFT JOIN users ON users.id = topics.author_user_id
		  WHERE topics.status = 'pending'
		  UNION ALL
		  SELECT 'comment'::text, comments.id, comments.topic_id, topics.title,
		    left(posts.plain_text, 2000), comments.author_user_id,
		    COALESCE(NULLIF(users.display_name, ''), users.username, ''),
		    categories.name, comments.moderation_triggers, comments.created_at,
		    COALESCE(comments.ip_address, ''), COALESCE(comments.last_edit_ip, '')
		  FROM comments
		  JOIN posts ON posts.id = comments.content_id
		  JOIN topics ON topics.id = comments.topic_id
		  JOIN categories ON categories.id = topics.category_id
		  LEFT JOIN users ON users.id = comments.author_user_id
		  WHERE comments.status = 'pending'
		)
	`
	var total int64
	if err := s.pool.QueryRow(ctx, pendingCTE+`
		SELECT count(*) FROM pending WHERE $1 = '' OR target_type = $1
	`, input.TargetType).Scan(&total); err != nil {
		return PendingList{}, fmt.Errorf("count pending moderation items: %w", err)
	}
	rows, err := s.pool.Query(ctx, pendingCTE+`
		SELECT target_type, target_id, topic_id, title, plain_prefix, author_id, author_name,
		  category, triggers, created_at, ip_address, last_edit_ip
		FROM pending
		WHERE $1 = '' OR target_type = $1
		ORDER BY created_at DESC, target_id DESC
		LIMIT $2 OFFSET $3
	`, input.TargetType, input.PerPage, (input.Page-1)*input.PerPage)
	if err != nil {
		return PendingList{}, fmt.Errorf("list pending moderation items: %w", err)
	}
	defer rows.Close()
	items := make([]PendingItem, 0)
	for rows.Next() {
		var item PendingItem
		var triggers []byte
		var plainPrefix string
		if err := rows.Scan(&item.TargetType, &item.TargetID, &item.TopicID, &item.Title,
			&plainPrefix, &item.AuthorID, &item.AuthorName, &item.Category, &triggers, &item.CreatedAt,
			&item.IPAddress, &item.LastEditIP); err != nil {
			return PendingList{}, fmt.Errorf("scan pending moderation item: %w", err)
		}
		item.Excerpt = forum.ExcerptFromPlain(plainPrefix, forum.RecommendedExcerptRuneLimit)
		_ = json.Unmarshal(triggers, &item.Triggers)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return PendingList{}, fmt.Errorf("iterate pending moderation items: %w", err)
	}
	return PendingList{Items: items, Total: total, Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *PostgresStore) ListReportItems(ctx context.Context, input WorkbenchListInput) (ReportItemList, error) {
	where := "WHERE reports.status IN ('open', 'reviewing') AND ($1 = '' OR reports.target_type = $1)"
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_reports reports `+where, input.TargetType).Scan(&total); err != nil {
		return ReportItemList{}, fmt.Errorf("count moderation report items: %w", err)
	}
	rows, err := s.pool.Query(ctx, reportItemSelectSQL()+where+`
		ORDER BY reports.created_at DESC, reports.id DESC LIMIT $2 OFFSET $3
	`, input.TargetType, input.PerPage, (input.Page-1)*input.PerPage)
	if err != nil {
		return ReportItemList{}, fmt.Errorf("list moderation report items: %w", err)
	}
	defer rows.Close()
	items := make([]ReportItem, 0)
	for rows.Next() {
		item, err := scanReportItem(rows)
		if err != nil {
			return ReportItemList{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ReportItemList{}, fmt.Errorf("iterate moderation report items: %w", err)
	}
	return ReportItemList{Items: items, Total: total, Page: input.Page, PerPage: input.PerPage}, nil
}

func reportItemSelectSQL() string {
	return `
		SELECT reports.id, reports.reporter_user_id,
		  COALESCE(NULLIF(reporter.display_name, ''), reporter.username, ''),
		  reports.target_type, reports.target_id, reports.reason_code, reports.body, reports.status,
		  reports.reviewer_user_id,
		  COALESCE(NULLIF(reviewer.display_name, ''), reviewer.username, ''),
		  reports.review_note, reports.created_at, reports.updated_at, reports.resolved_at,
		  COALESCE(topic.title, parent_topic.title, ''), COALESCE(left(post.plain_text, 2000), ''),
		  COALESCE(topic.author_user_id, comment.author_user_id, 0),
		  COALESCE(NULLIF(target_user.display_name, ''), target_user.username, ''),
		  COALESCE(category.name, ''), COALESCE(topic.status, comment.status, 'deleted'),
		  COALESCE(comment.topic_id, topic.id, 0),
		  COALESCE(topic.ip_address, comment.ip_address, ''),
		  COALESCE(topic.last_edit_ip, comment.last_edit_ip, '')
		FROM moderation_reports reports
		LEFT JOIN users reporter ON reporter.id = reports.reporter_user_id
		LEFT JOIN users reviewer ON reviewer.id = reports.reviewer_user_id
		LEFT JOIN topics topic ON reports.target_type = 'topic' AND topic.id = reports.target_id
		LEFT JOIN comments comment ON reports.target_type = 'comment' AND comment.id = reports.target_id
		LEFT JOIN topics parent_topic ON parent_topic.id = comment.topic_id
		LEFT JOIN posts post ON post.id = COALESCE(topic.content_id, comment.content_id)
		LEFT JOIN users target_user ON target_user.id = COALESCE(topic.author_user_id, comment.author_user_id)
		LEFT JOIN categories category ON category.id = COALESCE(topic.category_id, parent_topic.category_id)
	`
}

func scanReportItem(row reportScanner) (ReportItem, error) {
	var item ReportItem
	var reporterID, reviewerID sql.NullInt64
	var reporterName, reviewerName sql.NullString
	var resolvedAt sql.NullTime
	var plainPrefix string
	if err := row.Scan(
		&item.ID, &reporterID, &reporterName, &item.TargetType, &item.TargetID,
		&item.ReasonCode, &item.Body, &item.Status, &reviewerID, &reviewerName,
		&item.ReviewNote, &item.CreatedAt, &item.UpdatedAt, &resolvedAt,
		&item.Title, &plainPrefix, &item.TargetAuthorID, &item.TargetAuthorName,
		&item.Category, &item.TargetStatus, &item.TargetTopicID,
		&item.IPAddress, &item.LastEditIP,
	); err != nil {
		return ReportItem{}, fmt.Errorf("scan moderation report item: %w", err)
	}
	item.Excerpt = forum.ExcerptFromPlain(plainPrefix, forum.RecommendedExcerptRuneLimit)
	if reporterID.Valid {
		item.ReporterUserID = reporterID.Int64
		item.ReporterName = reporterName.String
	}
	if reviewerID.Valid {
		value := reviewerID.Int64
		item.ReviewerUserID = &value
		item.ReviewerName = reviewerName.String
	}
	if resolvedAt.Valid {
		value := resolvedAt.Time
		item.ResolvedAt = &value
	}
	return item, nil
}

func (s *PostgresStore) ListDecisions(ctx context.Context, input DecisionListInput) (DecisionList, error) {
	where := `WHERE ($1 = '' OR decisions.action = $1)
	  AND ($2 = '' OR decisions.target_type = $2)
	  AND ($3 = 0 OR decisions.reviewer_user_id = $3)`
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_decisions decisions `+where,
		input.Action, input.TargetType, input.ReviewerID).Scan(&total); err != nil {
		return DecisionList{}, fmt.Errorf("count moderation decisions: %w", err)
	}
	rows, err := s.pool.Query(ctx, decisionSelectSQL()+where+`
		ORDER BY decisions.created_at DESC, decisions.id DESC LIMIT $4 OFFSET $5
	`, input.Action, input.TargetType, input.ReviewerID, input.PerPage, (input.Page-1)*input.PerPage)
	if err != nil {
		return DecisionList{}, fmt.Errorf("list moderation decisions: %w", err)
	}
	defer rows.Close()
	items := make([]Decision, 0)
	for rows.Next() {
		item, err := scanDecision(rows)
		if err != nil {
			return DecisionList{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return DecisionList{}, fmt.Errorf("iterate moderation decisions: %w", err)
	}
	return DecisionList{Items: items, Total: total, Page: input.Page, PerPage: input.PerPage}, nil
}

func decisionSelectSQL() string {
	return `
		SELECT decisions.id, decisions.source, decisions.target_type, decisions.target_id,
		  decisions.report_id, decisions.action, decisions.reviewer_user_id,
		  COALESCE(NULLIF(users.display_name, ''), users.username, ''),
		  decisions.review_note, decisions.trigger_snapshot, decisions.created_at
		FROM moderation_decisions decisions
		LEFT JOIN users ON users.id = decisions.reviewer_user_id
	`
}

func scanDecision(row reportScanner) (Decision, error) {
	var item Decision
	var reportID sql.NullInt64
	var triggers []byte
	if err := row.Scan(&item.ID, &item.Source, &item.TargetType, &item.TargetID, &reportID,
		&item.Action, &item.ReviewerUserID, &item.ReviewerName, &item.ReviewNote,
		&triggers, &item.CreatedAt); err != nil {
		return Decision{}, fmt.Errorf("scan moderation decision: %w", err)
	}
	if reportID.Valid {
		value := reportID.Int64
		item.ReportID = &value
	}
	_ = json.Unmarshal(triggers, &item.Triggers)
	return item, nil
}

func (s *PostgresStore) GetReviewContext(ctx context.Context, input ReviewContextInput) (ReviewContext, error) {
	var row pgx.Row
	if input.TargetType == TargetTypeTopic {
		row = s.pool.QueryRow(ctx, `
			SELECT topics.id, topics.title, posts.html_content, topics.author_user_id,
			  COALESCE(NULLIF(users.display_name, ''), users.username, ''), categories.name,
			  topics.status, topics.moderation_triggers, ''::text, topics.created_at,
			  COALESCE(topics.ip_address, ''), COALESCE(topics.last_edit_ip, '')
			FROM topics
			JOIN posts ON posts.id = topics.content_id
			JOIN categories ON categories.id = topics.category_id
			LEFT JOIN users ON users.id = topics.author_user_id
			WHERE topics.id = $1
		`, input.TargetID)
	} else if input.TargetType == TargetTypeComment {
		row = s.pool.QueryRow(ctx, `
			SELECT comments.topic_id, topics.title, posts.html_content, comments.author_user_id,
			  COALESCE(NULLIF(users.display_name, ''), users.username, ''), categories.name,
			  comments.status, comments.moderation_triggers, topics.title, comments.created_at,
			  COALESCE(comments.ip_address, ''), COALESCE(comments.last_edit_ip, '')
			FROM comments
			JOIN posts ON posts.id = comments.content_id
			JOIN topics ON topics.id = comments.topic_id
			JOIN categories ON categories.id = topics.category_id
			LEFT JOIN users ON users.id = comments.author_user_id
			WHERE comments.id = $1
		`, input.TargetID)
	} else {
		return ReviewContext{}, ErrDecisionInvalid
	}
	contextItem := ReviewContext{Source: input.Source, TargetType: input.TargetType, TargetID: input.TargetID, ReportID: input.ReportID}
	var triggers []byte
	if err := row.Scan(&contextItem.TopicID, &contextItem.Title, &contextItem.HTML, &contextItem.AuthorID,
		&contextItem.AuthorName, &contextItem.Category, &contextItem.Status, &triggers,
		&contextItem.ParentTopic, &contextItem.CreatedAt, &contextItem.IPAddress, &contextItem.LastEditIP); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReviewContext{}, ErrTaskNotFound
		}
		return ReviewContext{}, fmt.Errorf("load moderation review context: %w", err)
	}
	_ = json.Unmarshal(triggers, &contextItem.Triggers)
	return contextItem, nil
}

func (s *PostgresStore) SubmitDecision(ctx context.Context, input DecisionInput) (Decision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("begin moderation decision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	decision, err := s.SubmitDecisionTx(ctx, tx, input)
	if err != nil {
		return Decision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Decision{}, fmt.Errorf("commit moderation decision: %w", err)
	}
	return decision, nil
}

// SubmitDecisionTx composes the complete moderation decision write with a
// caller-owned transaction. Authorization remains the caller's responsibility.
func (s *PostgresStore) SubmitDecisionTx(ctx context.Context, tx pgx.Tx, input DecisionInput) (Decision, error) {
	if tx == nil {
		return Decision{}, fmt.Errorf("moderation decision transaction is required")
	}
	if err := validateDecision(&input); err != nil {
		return Decision{}, err
	}

	triggers, err := applyDecision(ctx, tx, input)
	if err != nil {
		return Decision{}, err
	}
	triggerJSON, err := json.Marshal(triggers)
	if err != nil {
		return Decision{}, fmt.Errorf("encode moderation decision triggers: %w", err)
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO moderation_decisions (
		  source, target_type, target_id, report_id, action, reviewer_user_id,
		  review_note, trigger_snapshot
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, source, target_type, target_id, report_id, action,
		  reviewer_user_id, ''::text, review_note, trigger_snapshot, created_at
	`, input.Source, input.TargetType, input.TargetID, nullablePositive(input.ReportID), input.Action,
		input.ReviewerUserID, input.ReviewNote, triggerJSON)
	decision, err := scanDecision(row)
	if err != nil {
		return Decision{}, err
	}
	if input.Source == SourcePrePublish && (input.Action == ActionApprove || input.Action == ActionReject) && s.notifications != nil {
		if err := s.notifications.NotifyModerationTx(ctx, tx, DecisionNotificationInput{DecisionID: decision.ID, TargetType: input.TargetType, TargetID: input.TargetID, ReviewerUserID: input.ReviewerUserID, Approved: input.Action == ActionApprove, ReviewNote: input.ReviewNote}); err != nil {
			return Decision{}, fmt.Errorf("create moderation notification: %w", err)
		}
	}
	return decision, nil
}

func applyDecision(ctx context.Context, tx pgx.Tx, input DecisionInput) ([]string, error) {
	if input.Source == SourceReport {
		return nil, applyReportDecision(ctx, tx, input)
	}
	if input.TargetType == TargetTypeTopic {
		return applyPendingTopicDecision(ctx, tx, input)
	}
	return applyPendingCommentDecision(ctx, tx, input)
}

func applyPendingTopicDecision(ctx context.Context, tx pgx.Tx, input DecisionInput) ([]string, error) {
	var categoryID int64
	var triggers []byte
	if err := tx.QueryRow(ctx, `
		SELECT category_id, moderation_triggers FROM topics
		WHERE id = $1 AND status = 'pending' FOR UPDATE
	`, input.TargetID).Scan(&categoryID, &triggers); err != nil {
		return nil, taskStateError(err)
	}
	status := "rejected"
	if input.Action == ActionApprove {
		status = "active"
		if _, err := tx.Exec(ctx, `UPDATE categories SET topic_count = topic_count + 1, updated_at = now() WHERE id = $1`, categoryID); err != nil {
			return nil, fmt.Errorf("increment approved topic count: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE topics SET status = $2, updated_at = now() WHERE id = $1`, input.TargetID, status); err != nil {
		return nil, fmt.Errorf("update pending topic: %w", err)
	}
	var decoded []string
	_ = json.Unmarshal(triggers, &decoded)
	return decoded, nil
}

func applyPendingCommentDecision(ctx context.Context, tx pgx.Tx, input DecisionInput) ([]string, error) {
	var topicID, categoryID int64
	var parentID sql.NullInt64
	var triggers []byte
	if err := tx.QueryRow(ctx, `
		SELECT comments.topic_id, topics.category_id, comments.parent_comment_id, comments.moderation_triggers
		FROM comments JOIN topics ON topics.id = comments.topic_id
		WHERE comments.id = $1 AND comments.status = 'pending' FOR UPDATE OF comments
	`, input.TargetID).Scan(&topicID, &categoryID, &parentID, &triggers); err != nil {
		return nil, taskStateError(err)
	}
	status := "rejected"
	if input.Action == ActionApprove {
		status = "active"
		if parentID.Valid {
			if _, err := tx.Exec(ctx, `UPDATE comments SET reply_count = reply_count + 1, updated_at = now() WHERE id = $1`, parentID.Int64); err != nil {
				return nil, fmt.Errorf("increment approved reply count: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET comment_count = comment_count + 1,
			    hot_score = (comment_count + 1) * 5 + view_count,
			    last_activity_at = now(),
			    updated_at = now()
			WHERE id = $1
		`, topicID); err != nil {
			return nil, fmt.Errorf("increment approved comment count: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE categories SET comment_count = comment_count + 1, updated_at = now() WHERE id = $1`, categoryID); err != nil {
			return nil, fmt.Errorf("increment approved category comment count: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE comments SET status = $2, updated_at = now() WHERE id = $1`, input.TargetID, status); err != nil {
		return nil, fmt.Errorf("update pending comment: %w", err)
	}
	var decoded []string
	_ = json.Unmarshal(triggers, &decoded)
	return decoded, nil
}

func applyReportDecision(ctx context.Context, tx pgx.Tx, input DecisionInput) error {
	var targetType string
	var targetID int64
	if err := tx.QueryRow(ctx, `
		SELECT target_type, target_id FROM moderation_reports
		WHERE id = $1 AND status IN ('open', 'reviewing') FOR UPDATE
	`, input.ReportID).Scan(&targetType, &targetID); err != nil {
		return taskStateError(err)
	}
	if targetType != input.TargetType || targetID != input.TargetID {
		return ErrDecisionInvalid
	}
	if input.Action == ActionHideAndClose || input.Action == ActionDeleteAndClose {
		status := "hidden"
		if input.Action == ActionDeleteAndClose {
			status = "deleted"
		}
		if targetType == TargetTypeTopic {
			if err := applyReportedTopicState(ctx, tx, targetID, status); err != nil {
				return err
			}
		} else if err := applyReportedCommentState(ctx, tx, targetID, status); err != nil {
			return err
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE moderation_reports SET status = 'resolved', reviewer_user_id = $2,
		  review_note = $3, resolved_at = now(), updated_at = now()
		WHERE id = $1 AND status IN ('open', 'reviewing')
	`, input.ReportID, input.ReviewerUserID, input.ReviewNote)
	if err != nil {
		return fmt.Errorf("close moderation report: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrTaskConflict
	}
	return nil
}

func applyReportedTopicState(ctx context.Context, tx pgx.Tx, topicID int64, nextStatus string) error {
	var categoryID int64
	var currentStatus string
	if err := tx.QueryRow(ctx, `
		SELECT category_id, status FROM topics WHERE id = $1 FOR UPDATE
	`, topicID).Scan(&categoryID, &currentStatus); err != nil {
		return taskStateError(err)
	}
	if currentStatus == "active" || currentStatus == "locked" {
		if _, err := tx.Exec(ctx, `
			UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now()
			WHERE id = $1
		`, categoryID); err != nil {
			return fmt.Errorf("decrement reported topic count: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE topics SET status = $2, updated_at = now(),
		  deleted_at = CASE WHEN $2 = 'deleted' THEN COALESCE(deleted_at, now()) ELSE deleted_at END
		WHERE id = $1
	`, topicID, nextStatus); err != nil {
		return fmt.Errorf("apply reported topic action: %w", err)
	}
	return nil
}

func applyReportedCommentState(ctx context.Context, tx pgx.Tx, commentID int64, nextStatus string) error {
	var topicID, categoryID int64
	var parentID sql.NullInt64
	var currentStatus string
	if err := tx.QueryRow(ctx, `
		SELECT comments.topic_id, topics.category_id, comments.parent_comment_id, comments.status
		FROM comments JOIN topics ON topics.id = comments.topic_id
		WHERE comments.id = $1 FOR UPDATE OF comments
	`, commentID).Scan(&topicID, &categoryID, &parentID, &currentStatus); err != nil {
		return taskStateError(err)
	}
	if currentStatus == "active" {
		if parentID.Valid {
			if _, err := tx.Exec(ctx, `
				UPDATE comments SET reply_count = GREATEST(reply_count - 1, 0), updated_at = now()
				WHERE id = $1
			`, parentID.Int64); err != nil {
				return fmt.Errorf("decrement reported reply count: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET comment_count = GREATEST(comment_count - 1, 0),
			    hot_score = GREATEST(comment_count - 1, 0) * 5 + view_count,
			    updated_at = now()
			WHERE id = $1
		`, topicID); err != nil {
			return fmt.Errorf("decrement reported topic comment count: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE categories SET comment_count = GREATEST(comment_count - 1, 0), updated_at = now()
			WHERE id = $1
		`, categoryID); err != nil {
			return fmt.Errorf("decrement reported category comment count: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comments SET status = $2, updated_at = now(),
		  deleted_at = CASE WHEN $2 = 'deleted' THEN COALESCE(deleted_at, now()) ELSE deleted_at END
		WHERE id = $1
	`, commentID, nextStatus); err != nil {
		return fmt.Errorf("apply reported comment action: %w", err)
	}
	return nil
}

func taskStateError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTaskConflict
	}
	return err
}

func nullablePositive(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
