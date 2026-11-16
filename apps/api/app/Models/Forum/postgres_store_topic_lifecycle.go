package forum

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) ApplyTopicAction(ctx context.Context, input TopicLifecycleInput) (TopicLifecycleRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TopicLifecycleRecord{}, fmt.Errorf("begin topic action: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	result, err := s.ApplyTopicActionTx(ctx, tx, input)
	if err != nil {
		return TopicLifecycleRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TopicLifecycleRecord{}, fmt.Errorf("commit topic action: %w", err)
	}
	return result, nil
}

// ApplyTopicActionTx lets Host-owned transactional commands compose the
// existing topic lifecycle write with their receipt and audit evidence.
// The caller owns commit/rollback and must enforce actor authorization first.
func (s *PostgresStore) ApplyTopicActionTx(ctx context.Context, tx pgx.Tx, input TopicLifecycleInput) (TopicLifecycleRecord, error) {
	if tx == nil {
		return TopicLifecycleRecord{}, fmt.Errorf("topic action transaction is required")
	}

	// 锁定主题，确认存在；category_id 供公开计数同步使用。
	var status string
	var previousCategoryID int64
	if err := tx.QueryRow(ctx, `
		SELECT status, category_id FROM topics WHERE id = $1 FOR UPDATE
	`, input.TopicID).Scan(&status, &previousCategoryID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TopicLifecycleRecord{}, ErrTopicNotFound
		}
		return TopicLifecycleRecord{}, fmt.Errorf("lock topic for action: %w", err)
	}

	var setStatus string
	var hasStatusUpdate bool
	var setPinned *bool
	switch input.Action {
	case TopicActionHide:
		setStatus = TopicStatusHidden
		hasStatusUpdate = true
	case TopicActionRestore:
		setStatus = TopicStatusActive
		hasStatusUpdate = true
	case TopicActionLock:
		setStatus = TopicStatusLocked
		hasStatusUpdate = true
	case TopicActionUnlock:
		setStatus = TopicStatusActive
		hasStatusUpdate = true
	case TopicActionPin:
		pinned := true
		setPinned = &pinned
	case TopicActionUnpin:
		pinned := false
		setPinned = &pinned
	default:
		return TopicLifecycleRecord{}, ErrInvalidAction
	}

	// restore 时重置 deleted_at/locked_at，并恢复为 active；其它动作不触碰 deleted_at。
	if input.Action == TopicActionRestore {
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET status = $2, deleted_at = NULL, locked_at = NULL,
			    is_pinned = COALESCE($3::boolean, is_pinned), updated_at = now(), last_activity_at = now()
			WHERE id = $1
		`, input.TopicID, setStatus, nullableBool(setPinned)); err != nil {
			return TopicLifecycleRecord{}, fmt.Errorf("restore topic: %w", err)
		}
	} else if hasStatusUpdate {
		// 隐藏/锁定/解锁：按动作维护 locked_at 时间戳。
		var lockedExpr string
		switch input.Action {
		case TopicActionHide:
			lockedExpr = "locked_at"
		case TopicActionLock:
			lockedExpr = "now()"
		case TopicActionUnlock:
			lockedExpr = "NULL"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET status = $2, locked_at = `+lockedExpr+`,
			    is_pinned = COALESCE($3::boolean, is_pinned), updated_at = now(), last_activity_at = now()
			WHERE id = $1
		`, input.TopicID, setStatus, nullableBool(setPinned)); err != nil {
			return TopicLifecycleRecord{}, fmt.Errorf("update topic status: %w", err)
		}
	} else if setPinned != nil {
		// pin/unpin：维护 pinned_at，并更新 last_activity。
		var pinnedAtExpr string
		if *setPinned {
			pinnedAtExpr = "now()"
		} else {
			pinnedAtExpr = "NULL"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET is_pinned = $2, pinned_at = `+pinnedAtExpr+`, updated_at = now()
			WHERE id = $1
		`, input.TopicID, *setPinned); err != nil {
			return TopicLifecycleRecord{}, fmt.Errorf("update topic pin: %w", err)
		}
	}

	// 公开可见性变化必须同步 categories.topic_count：hide 让主题离开公开集（-1），
	// restore 让它回到公开集（+1）。口径与 list_topics 的过滤共用 TopicCountsPublic
	// （active+locked）；lock/unlock 两端都是公开状态，delta 为 0，自然不动计数。
	// 之前这里完全不碰计数，于是「版主 hide」与「举报处置 hide」两条路径对同一个
	// 动作给出两种结果（P2-5 补刀）。
	if hasStatusUpdate {
		switch {
		case TopicCountsPublic(status) && !TopicCountsPublic(setStatus):
			if _, err := tx.Exec(ctx, `
				UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now() WHERE id = $1
			`, previousCategoryID); err != nil {
				return TopicLifecycleRecord{}, fmt.Errorf("decrement category count on hide: %w", err)
			}
		case !TopicCountsPublic(status) && TopicCountsPublic(setStatus):
			if _, err := tx.Exec(ctx, `
				UPDATE categories SET topic_count = topic_count + 1, updated_at = now() WHERE id = $1
			`, previousCategoryID); err != nil {
				return TopicLifecycleRecord{}, fmt.Errorf("increment category count on restore: %w", err)
			}
		}
	}

	var result TopicLifecycleRecord
	if err := tx.QueryRow(ctx, `
		SELECT id, status, is_pinned FROM topics WHERE id = $1
	`, input.TopicID).Scan(&result.TopicID, &result.Status, &result.IsPinned); err != nil {
		return TopicLifecycleRecord{}, fmt.Errorf("read topic after action: %w", err)
	}

	return result, nil
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func resolveTopicTags(ctx context.Context, tx pgx.Tx, input ResolveTopicTagsInput) ([]TopicTagSummary, error) {
	mode := strings.TrimSpace(input.CreationMode)
	switch mode {
	case TagCreationModeControlled, TagCreationModeReview, TagCreationModeOpen:
	default:
		return nil, ErrInvalidSettings
	}
	// ResolveTopicTags 允许最多 HardTagMaxPerTopic 个 slug，不强制 min（min 在 service 层校验）。
	slugs, err := normalizeTopicTagSlugs(input.Slugs, 0, HardTagMaxPerTopic)
	if err != nil {
		return nil, err
	}
	items := make([]TopicTagSummary, 0, len(slugs))
	for _, slug := range slugs {
		tag, found, err := loadTagForUpdate(ctx, tx, slug)
		if err != nil {
			return nil, err
		}
		if found {
			if tag.Status == TagStatusDisabled || (tag.Status == TagStatusPending && mode == TagCreationModeControlled) {
				return nil, ErrInvalidTag
			}
			items = append(items, tag)
			continue
		}

		switch mode {
		case TagCreationModeControlled:
			return nil, ErrInvalidTag
		case TagCreationModeReview:
			tag, err = insertTag(ctx, tx, input.ActorUserID, slug, TagStatusPending)
		case TagCreationModeOpen:
			tag, err = insertTag(ctx, tx, input.ActorUserID, slug, TagStatusActive)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, tag)
	}
	return items, nil
}

func loadTagForUpdate(ctx context.Context, tx pgx.Tx, slug string) (TopicTagSummary, bool, error) {
	var tag TopicTagSummary
	err := tx.QueryRow(ctx, `
		SELECT id, slug, name, status
		FROM tags
		WHERE slug = $1
		FOR UPDATE
	`, slug).Scan(&tag.ID, &tag.Slug, &tag.Name, &tag.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicTagSummary{}, false, nil
	}
	if err != nil {
		return TopicTagSummary{}, false, fmt.Errorf("load tag: %w", err)
	}
	return tag, true, nil
}
