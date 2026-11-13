package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 庆祝读面的持久化实现。
//
// 与 `presentation.go` 的分工：这里只出「行」，可见性判定在 `ListCelebrations`
// 里复用 `ResolveSafeTargets`。刻意不在这条 SQL 里 JOIN 目标表：
// 徽章/主题/评论的可见性规则各有归属域，SQL 里复制一份必然与真实判定漂移。

// CelebrationCursor 读游标行；没有行表示用户还没「初始化」庆祝层。
func (s *PostgresStore) CelebrationCursor(ctx context.Context, userID int64) (int64, bool, error) {
	var cursor int64
	err := s.runner.QueryRow(ctx,
		`SELECT cursor_notification_id FROM user_celebration_cursors WHERE user_id=$1`, userID).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("load celebration cursor: %w", err)
	}
	return cursor, true, nil
}

// LatestNotificationID 是该用户当前最大通知 id（无通知为 0）。
//
// 首访时它被当作「种子游标」返回给前端，前端 ack 之后庆祝层就有了起点，
// 老用户因此不会被历史徽章刷屏。
func (s *PostgresStore) LatestNotificationID(ctx context.Context, userID int64) (int64, error) {
	var latest int64
	if err := s.runner.QueryRow(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM notifications WHERE recipient_user_id=$1`, userID).Scan(&latest); err != nil {
		return 0, fmt.Errorf("load latest notification id: %w", err)
	}
	return latest, nil
}

// ListCelebrationRows 按 id 升序返回庆祝候选，并给出同条件总数（封顶）。
//
// `celebrationWindow` 用 `now() - interval` 而不是 Go 传时间：这里比的是时间戳
// （不是「哪一天」的日历界），时间戳比较允许在 SQL 里取 now()——与「按日取界必须
// Go 传参」那条纪律不冲突（见 knowledge/modules/badges.md 陷阱 2）。
func (s *PostgresStore) ListCelebrationRows(ctx context.Context, userID int64, afterID int64, limit int) ([]CelebrationRow, int64, error) {
	if limit <= 0 {
		limit = DefaultCelebrationLimit
	}
	rows, err := s.runner.Query(ctx, `
		SELECT n.id, n.type, n.target_type, n.target_id, n.payload, d.presentation, n.created_at
		FROM notifications AS n
		JOIN notification_type_descriptors AS d ON d.type = n.type
		WHERE n.recipient_user_id = $1
		  AND d.active
		  AND d.presentation->>'celebration' = 'true'
		  AND n.id > $2
		  AND n.created_at > now() - ($3::bigint * interval '1 millisecond')
		ORDER BY n.id ASC
		LIMIT $4`, userID, afterID, celebrationWindow.Milliseconds(), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list celebration rows: %w", err)
	}
	defer rows.Close()
	items := make([]CelebrationRow, 0, limit)
	for rows.Next() {
		var item CelebrationRow
		var payload, presentation []byte
		if err := rows.Scan(&item.ID, &item.Type, &item.TargetType, &item.TargetID, &payload, &presentation, &item.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan celebration row: %w", err)
		}
		item.Payload = json.RawMessage(payload)
		item.Presentation = json.RawMessage(presentation)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list celebration rows: %w", err)
	}
	var total int64
	if err := s.runner.QueryRow(ctx, `
		SELECT LEAST(COUNT(*), $4::bigint)
		FROM notifications AS n
		JOIN notification_type_descriptors AS d ON d.type = n.type
		WHERE n.recipient_user_id = $1
		  AND d.active
		  AND d.presentation->>'celebration' = 'true'
		  AND n.id > $2
		  AND n.created_at > now() - ($3::bigint * interval '1 millisecond')`,
		userID, afterID, celebrationWindow.Milliseconds(), int64(celebrationTotalCap)).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count celebration rows: %w", err)
	}
	return items, total, nil
}

// AcknowledgeCelebrationCursor 用 GREATEST 语义推进游标。
//
// ON CONFLICT DO UPDATE + GREATEST：两条 ack 乱序到达时游标不会倒退，
// 也就不会把已经看过的通知重新弹一遍。
func (s *PostgresStore) AcknowledgeCelebrationCursor(ctx context.Context, userID int64, cursorID int64) (int64, error) {
	var stored int64
	err := s.runner.QueryRow(ctx, `
		INSERT INTO user_celebration_cursors (user_id, cursor_notification_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		SET cursor_notification_id = GREATEST(user_celebration_cursors.cursor_notification_id, EXCLUDED.cursor_notification_id),
		    updated_at = now()
		RETURNING cursor_notification_id`, userID, cursorID).Scan(&stored)
	if err != nil {
		return 0, fmt.Errorf("acknowledge celebrations: %w", err)
	}
	return stored, nil
}

var _ CelebrationStore = (*PostgresStore)(nil)
