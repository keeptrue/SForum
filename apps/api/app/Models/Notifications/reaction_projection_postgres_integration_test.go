package notifications

// 点赞通知投影的真实 PostgreSQL 集成测试。
//
// 连接与建表来源与 channels_postgres_integration_test.go /
// pending_review_fanout_postgres_integration_test.go 一致：连接串只来自
// internal/testdb，表结构由 scripts/test-api-isolated.sh 的迁移链建立，
// 这里不重抄 DDL。相关迁移：
//   - 202607110016_notifications_mail_deliveries.sql（notifications 的 dedupe_key UNIQUE、mail_deliveries）
//   - 202607280064_notification_platform_v2.sql（类型策略、notification_channel_deliveries、recipient revision）
//   - 202610280001_content_reactions.sql（reaction 类型：in_app 开，email/web_push 默认关）

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// reactionProjectionPool 打开隔离集群连接池；多事务场景必须走真实提交，
// 因此不能复用单事务 fixture notificationChannelPostgresTx。
func reactionProjectionPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

// requireReactionNotificationType 在缺少 content_reactions 迁移的老测试库上跳过，
// 避免把「类型没播种」误报成投影缺陷。
func requireReactionNotificationType(t *testing.T, ctx context.Context, runner interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) {
	t.Helper()
	var active bool
	err := runner.QueryRow(ctx, `SELECT active FROM notification_type_descriptors WHERE type=$1`, TypeReaction).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Skip("reaction notification type is not seeded; run the full migration chain")
	}
	if err != nil {
		t.Fatalf("load reaction notification type: %v", err)
	}
	if !active {
		t.Fatalf("reaction notification type is inactive")
	}
}

func TestNotifyReactionTxDedupesCancelledAndRelikedNotification(t *testing.T) {
	ctx, pool := reactionProjectionPool(t)
	requireReactionNotificationType(t, ctx, pool)

	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authorID := insertNotificationChannelUser(t, ctx, seed, "reaction_dedupe_author")
	actorID := insertNotificationChannelUser(t, ctx, seed, "reaction_dedupe_actor")
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	targetID := time.Now().UnixNano()
	event := ReactionEvent{TargetType: "topic", TargetID: targetID, TopicID: targetID, ActorUserID: actorID, RecipientUserID: authorID}
	dedupe := fmt.Sprintf("reaction:%s:%d:%d", event.TargetType, event.TargetID, event.ActorUserID)
	channelKey := dedupe + ":web_push"

	// reaction 的 web_push 默认关（202610280001_content_reactions.sql）。这里显式
	// 打开该渠道，让渠道投影也进入去重断言，结束后还原成迁移里的默认值。
	// 收件人没有个人偏好（inherit）时，deliveryEnabled 返回的是 recommended_enabled，
	// 所以站点级开关必须同时写这两列。
	if _, err := pool.Exec(ctx, `
		UPDATE notification_type_policies SET enabled=TRUE, recommended_enabled=TRUE
		WHERE type=$1 AND channel='web_push'`, TypeReaction); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `
			UPDATE notification_type_policies SET enabled=FALSE, recommended_enabled=FALSE
			WHERE type=$1 AND channel='web_push'`, TypeReaction)
		_, _ = pool.Exec(bg, `DELETE FROM mail_deliveries WHERE idempotency_key=$1`, dedupe)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id=ANY($1)`, []int64{authorID, actorID})
	})

	// 三次独立事务模拟真实的提交边界：赞 → 取消 → 再赞。取消点赞不写通知，
	// 所以通知侧看到的就是三次 NotifyReactionTx，靠 dedupe_key 收敛成一行。
	for attempt := 1; attempt <= 3; attempt++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{}).NotifyReactionTx(ctx, tx, event); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("reaction notification attempt %d: %v", attempt, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit reaction notification attempt %d: %v", attempt, err)
		}
	}

	var notificationRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE dedupe_key=$1`, dedupe).Scan(&notificationRows); err != nil {
		t.Fatal(err)
	}
	if notificationRows != 1 {
		t.Fatalf("cancelled+reliked reaction produced %d notifications rows for %s, want 1", notificationRows, dedupe)
	}
	var notificationID, recipientID int64
	var kind string
	if err := pool.QueryRow(ctx, `SELECT id, recipient_user_id, type FROM notifications WHERE dedupe_key=$1`, dedupe).
		Scan(&notificationID, &recipientID, &kind); err != nil {
		t.Fatal(err)
	}
	if recipientID != authorID || kind != TypeReaction {
		t.Fatalf("reaction notification recipient/type=(%d,%s) want=(%d,%s)", recipientID, kind, authorID, TypeReaction)
	}

	// 邮件默认关：三次投影都不产生 mail_deliveries 行。
	var mailRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM mail_deliveries WHERE idempotency_key=$1`, dedupe).Scan(&mailRows); err != nil {
		t.Fatal(err)
	}
	if mailRows != 0 {
		t.Fatalf("default-off email channel created %d mail deliveries", mailRows)
	}

	// 渠道投影按 idempotency_key 去重（notification_channel_deliveries），
	// 且必须挂在唯一那条站内通知上，而不是各自新建通知。
	var channelRows, linkedRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_channel_deliveries WHERE idempotency_key=$1`, channelKey).Scan(&channelRows); err != nil {
		t.Fatal(err)
	}
	if channelRows != 1 {
		t.Fatalf("cancelled+reliked reaction produced %d web_push projection rows, want 1", channelRows)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_channel_deliveries WHERE idempotency_key=$1 AND notification_id=$2`, channelKey, notificationID).Scan(&linkedRows); err != nil {
		t.Fatal(err)
	}
	if linkedRows != 1 {
		t.Fatalf("web_push projection is not attached to notification %d", notificationID)
	}

	// 重复投影不得重复推进未读 revision：只有首次插入才 +1。
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM notification_recipient_revisions WHERE user_id=$1`, authorID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("duplicate reaction projections bumped recipient revision to %d, want 1", revision)
	}
}

func TestNotifyReactionTxSkipsSelfReaction(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	requireReactionNotificationType(t, ctx, tx)
	authorID := insertNotificationChannelUser(t, ctx, tx, "reaction_self_author")
	actorID := insertNotificationChannelUser(t, ctx, tx, "reaction_self_actor")
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})

	base := time.Now().UnixNano()
	cases := []struct {
		name             string
		recipient, actor int64
	}{
		{"self reaction", authorID, authorID},
		{"zero recipient", 0, actorID},
		{"negative recipient", -7, actorID},
	}
	keys := make([]string, 0, len(cases))
	for index, testCase := range cases {
		targetID := base + int64(index)
		event := ReactionEvent{
			TargetType: "topic", TargetID: targetID, TopicID: targetID,
			ActorUserID: testCase.actor, RecipientUserID: testCase.recipient,
		}
		if err := outbox.NotifyReactionTx(ctx, tx, event); err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		dedupe := fmt.Sprintf("reaction:%s:%d:%d", event.TargetType, event.TargetID, event.ActorUserID)
		keys = append(keys, dedupe)
		var rows int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE dedupe_key=$1`, dedupe).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s wrote %d notifications rows, want 0", testCase.name, rows)
		}
	}

	// 兜底：两个参与者都没有任何收件箱或邮件投影。
	var inbox, mail int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id=ANY($1)`, []int64{authorID, actorID}).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM mail_deliveries WHERE idempotency_key=ANY($1)`, keys).Scan(&mail); err != nil {
		t.Fatal(err)
	}
	if inbox != 0 || mail != 0 {
		t.Fatalf("self/zero-recipient reactions left inbox=%d mail=%d rows", inbox, mail)
	}
}

func TestNotifyReactionTxNotifiesTargetAuthorOnly(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	requireReactionNotificationType(t, ctx, tx)
	authorID := insertNotificationChannelUser(t, ctx, tx, "reaction_target_author")
	actorID := insertNotificationChannelUser(t, ctx, tx, "reaction_target_actor")
	bystanderID := insertNotificationChannelUser(t, ctx, tx, "reaction_target_bystander")
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})

	targetID := time.Now().UnixNano()
	event := ReactionEvent{TargetType: "topic", TargetID: targetID, TopicID: targetID, ActorUserID: actorID, RecipientUserID: authorID}
	if err := outbox.NotifyReactionTx(ctx, tx, event); err != nil {
		t.Fatalf("notify reaction: %v", err)
	}
	dedupe := fmt.Sprintf("reaction:%s:%d:%d", event.TargetType, event.TargetID, event.ActorUserID)

	var recipientID, gotTargetID int64
	var kind, targetType, category string
	var actor sql.NullInt64
	var payload json.RawMessage
	if err := tx.QueryRow(ctx, `
		SELECT recipient_user_id, type, category, actor_user_id, target_type, target_id, payload
		FROM notifications WHERE dedupe_key=$1`, dedupe).
		Scan(&recipientID, &kind, &category, &actor, &targetType, &gotTargetID, &payload); err != nil {
		t.Fatalf("load reaction notification: %v", err)
	}
	// category 必须与类型目录/迁移声明的 engagement 一致，否则按 category
	// 过滤的收件箱会把点赞通知归到 plugin_unknown。
	if category != "engagement" {
		t.Fatalf("reaction notification category=%q want engagement", category)
	}
	if recipientID != authorID {
		t.Fatalf("reaction notification recipient=%d want target author %d", recipientID, authorID)
	}
	if kind != TypeReaction {
		t.Fatalf("reaction notification type=%q want %q", kind, TypeReaction)
	}
	if !actor.Valid || actor.Int64 != actorID {
		t.Fatalf("reaction notification actor=%v want %d", actor, actorID)
	}
	if targetType != "topic" || gotTargetID != targetID {
		t.Fatalf("reaction notification target=(%s,%d) want=(\"topic\",%d)", targetType, gotTargetID, targetID)
	}
	var envelope struct {
		TopicID    int64  `json:"topicId"`
		TargetType string `json:"targetType"`
		TargetID   int64  `json:"targetId"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode reaction payload %s: %v", payload, err)
	}
	if envelope.TopicID != targetID || envelope.TargetType != "topic" || envelope.TargetID != targetID {
		t.Fatalf("reaction payload envelope=%+v want topicId/targetId=%d targetType=topic (payload=%s)", envelope, targetID, payload)
	}

	// 只有目标作者收到投影；无关用户没有行。
	var rows, bystanderRows, mailRows int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE dedupe_key=$1`, dedupe).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id=$1`, bystanderID).Scan(&bystanderRows); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM mail_deliveries WHERE idempotency_key=$1`, dedupe).Scan(&mailRows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || bystanderRows != 0 || mailRows != 0 {
		t.Fatalf("reaction projection rows=%d bystander=%d mail=%d, want 1/0/0", rows, bystanderRows, mailRows)
	}
}
