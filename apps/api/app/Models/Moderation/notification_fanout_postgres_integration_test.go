package moderation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

var errFanoutAfterProjection = errors.New("fanout failed after projection")

type notificationPGFixture struct {
	ctx   context.Context
	admin *pgx.Conn
	pool  *pgxpool.Pool
}

type fakeTxEnqueuer struct{}

func (fakeTxEnqueuer) EnqueueTx(context.Context, pgx.Tx, river.JobArgs, supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{}, nil
}

type moderationNotificationAdapter struct {
	outbox    *notifications.Outbox
	failAfter bool
}

func (a moderationNotificationAdapter) NotifyModerationTx(ctx context.Context, tx pgx.Tx, input moderation.DecisionNotificationInput) error {
	err := a.outbox.NotifyModerationTx(ctx, tx, notifications.ModerationEvent{
		DecisionID: input.DecisionID, TargetType: input.TargetType, TargetID: input.TargetID,
		ReviewerUserID: input.ReviewerUserID, Approved: input.Approved, ReviewNote: input.ReviewNote,
	})
	if err != nil {
		return err
	}
	if a.failAfter {
		return errFanoutAfterProjection
	}
	return nil
}

func TestCoreNotificationRecipientMatrixPostgres(t *testing.T) {
	f := newNotificationPGFixture(t)
	topicAuthor := f.insertUser(t, "matrix_topic_author", "active")
	parentAuthor := f.insertUser(t, "matrix_parent_author", "active")
	actor := f.insertUser(t, "matrix_actor", "active")
	inactive := f.insertUser(t, "matrix_inactive", "disabled")
	topicID := f.insertTopic(t, topicAuthor, "matrix topic", "active")
	parentID := f.insertComment(t, topicID, parentAuthor, 0, "parent", "active")
	store := notifications.NewPostgresStore(f.pool)
	outbox := notifications.NewOutbox(f.pool, store, fakeTxEnqueuer{})

	t.Run("top-level and nested replies choose the owning author", func(t *testing.T) {
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1001, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1002, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
			ParentAuthorUserID: parentAuthor,
		})
		f.assertNotification(t, 1001, notifications.TypeReply, topicAuthor, "comment")
		f.assertNotification(t, 1002, notifications.TypeReply, parentAuthor, "comment")
	})

	t.Run("reply and mention merge into one notification for one recipient", func(t *testing.T) {
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1003, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
			ParentAuthorUserID: parentAuthor,
			MentionedUsernames: []string{"matrix_parent_author", "MATRIX_PARENT_AUTHOR", "matrix_parent_author"},
		})
		f.assertNotification(t, 1003, notifications.TypeReply, parentAuthor, "comment")
		if got := f.notificationCount(t, 1003); got != 1 {
			t.Fatalf("reply plus mention merged into %d rows, want 1", got)
		}
		var payload map[string]any
		if err := f.pool.QueryRow(f.ctx, `SELECT payload FROM notifications WHERE target_id=1003 AND recipient_user_id=$1`, parentAuthor).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["alsoMentioned"] != "true" {
			t.Fatalf("merged reply notification should carry alsoMentioned marker: %#v", payload)
		}
	})

	t.Run("self and inactive recipients are filtered", func(t *testing.T) {
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1004, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: inactive,
			MentionedUsernames: []string{"matrix_actor", "matrix_inactive"},
		})
		if got := f.notificationCount(t, 1004); got != 0 {
			t.Fatalf("self/inactive recipients created %d rows", got)
		}
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1008, TopicID: topicID, ActorUserID: topicAuthor, TopicAuthorUserID: topicAuthor,
		})
		if got := f.notificationCount(t, 1008); got != 0 {
			t.Fatalf("self reply created %d rows", got)
		}
	})

	t.Run("topic mention is active-only, deduplicated, and targeted", func(t *testing.T) {
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := outbox.NotifyTopicTx(f.ctx, tx, notifications.TopicEvent{
			TopicID: 1005, ActorUserID: actor,
			MentionedUsernames: []string{"matrix_topic_author", "MATRIX_TOPIC_AUTHOR", "matrix_actor", "matrix_inactive"},
		}); err != nil {
			_ = tx.Rollback(f.ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		f.assertNotification(t, 1005, notifications.TypeMention, topicAuthor, "topic")
		if got := f.notificationCount(t, 1005); got != 1 {
			t.Fatalf("topic mention matrix created %d rows, want 1", got)
		}
	})

	t.Run("disabled email skips delivery while in-app remains", func(t *testing.T) {
		if _, err := f.pool.Exec(f.ctx, `UPDATE notification_type_policies SET enabled=FALSE, recommended_enabled=FALSE WHERE type='reply' AND channel='email'`); err != nil {
			t.Fatal(err)
		}
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1006, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		f.assertNotification(t, 1006, notifications.TypeReply, topicAuthor, "comment")
		if got := f.deliveryCount(t, "comment:1006:%"); got != 0 {
			t.Fatalf("disabled email created %d deliveries", got)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE notification_type_policies SET enabled=TRUE WHERE type='reply' AND channel='email'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("recipient email preference is bounded by site policy", func(t *testing.T) {
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO notification_preferences (user_id,type,channel,state) VALUES ($1,'reply','email','disabled')`, topicAuthor); err != nil {
			t.Fatal(err)
		}
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1009, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		if got := f.deliveryCount(t, "comment:1009:%"); got != 0 {
			t.Fatalf("recipient-disabled email created %d deliveries", got)
		}

		if _, err := f.pool.Exec(f.ctx, `UPDATE notification_preferences SET state='enabled' WHERE user_id=$1 AND type='reply' AND channel='email'`, topicAuthor); err != nil {
			t.Fatal(err)
		}
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1010, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		if got := f.deliveryCount(t, "comment:1010:%"); got != 1 {
			t.Fatalf("recipient-enabled email created %d deliveries, want 1", got)
		}

		if _, err := f.pool.Exec(f.ctx, `UPDATE notification_type_policies SET enabled=FALSE WHERE type='reply' AND channel='email'`); err != nil {
			t.Fatal(err)
		}
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1011, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		if got := f.deliveryCount(t, "comment:1011:%"); got != 0 {
			t.Fatalf("site-disabled email accepted recipient override and created %d deliveries", got)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE notification_type_policies SET enabled=TRUE WHERE type='reply' AND channel='email'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no provider does not suppress the durable mail projection", func(t *testing.T) {
		f.notifyComment(t, outbox, notifications.CommentEvent{
			CommentID: 1007, TopicID: topicID, ActorUserID: actor, TopicAuthorUserID: topicAuthor,
		})
		if got := f.deliveryCount(t, "comment:1007:%"); got != 1 {
			t.Fatalf("provider-independent mail projection count=%d want=1", got)
		}
		var status string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM mail_deliveries WHERE idempotency_key LIKE 'comment:1007:%'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != notifications.DeliveryQueued {
			t.Fatalf("delivery status=%q want queued", status)
		}
	})

	_ = parentID
}

func TestModerationNotificationFanoutPostgres(t *testing.T) {
	t.Run("topic approval reloads stored source exactly once", func(t *testing.T) {
		f := newNotificationPGFixture(t)
		author := f.insertUser(t, "topicapprovalauthor", "active")
		recipient := f.insertUser(t, "topicapprovalrecipient", "active")
		reviewer := f.insertUser(t, "topicapprovalreviewer", "active")
		topicID := f.insertTopic(t, author, "pending @topicapprovalrecipient", "pending")
		outbox := notifications.NewOutbox(f.pool, notifications.NewPostgresStore(f.pool), fakeTxEnqueuer{})
		store := moderation.NewPostgresStore(f.pool).WithDecisionNotifications(moderationNotificationAdapter{outbox: outbox})

		input := moderation.DecisionInput{Source: moderation.SourcePrePublish, TargetType: moderation.TargetTypeTopic,
			TargetID: topicID, Action: moderation.ActionApprove, ReviewerUserID: reviewer}
		if _, err := store.SubmitDecision(f.ctx, input); err != nil {
			t.Fatalf("approve pending topic: %v", err)
		}
		if _, err := store.SubmitDecision(f.ctx, input); !errors.Is(err, moderation.ErrTaskConflict) {
			t.Fatalf("topic approval retry error=%v want task conflict", err)
		}
		f.assertNotification(t, topicID, notifications.TypeModerationApproved, author, "topic")
		f.assertNotification(t, topicID, notifications.TypeMention, recipient, "topic")
		if got := f.notificationCount(t, topicID); got != 2 {
			t.Fatalf("topic approval/retry created %d notifications, want 2", got)
		}
		if got := f.decisionCount(t, topicID); got != 1 {
			t.Fatalf("topic approval/retry created %d decisions, want 1", got)
		}
	})

	t.Run("approval fans out once and retry is idempotent", func(t *testing.T) {
		f := newNotificationPGFixture(t)
		topicAuthor := f.insertUser(t, "approval_topic_author", "active")
		recipient := f.insertUser(t, "approval_parent", "active")
		author := f.insertUser(t, "approval_author", "active")
		reviewer := f.insertUser(t, "approval_reviewer", "active")
		topicID := f.insertTopic(t, topicAuthor, "approval topic", "active")
		parentID := f.insertComment(t, topicID, recipient, 0, "parent", "active")
		commentID := f.insertComment(t, topicID, author, parentID, "pending @approval_parent", "pending")
		outbox := notifications.NewOutbox(f.pool, notifications.NewPostgresStore(f.pool), fakeTxEnqueuer{})
		store := moderation.NewPostgresStore(f.pool).WithDecisionNotifications(moderationNotificationAdapter{outbox: outbox})

		input := moderation.DecisionInput{Source: moderation.SourcePrePublish, TargetType: moderation.TargetTypeComment,
			TargetID: commentID, Action: moderation.ActionApprove, ReviewerUserID: reviewer}
		if _, err := store.SubmitDecision(f.ctx, input); err != nil {
			t.Fatalf("approve pending comment: %v", err)
		}
		if _, err := store.SubmitDecision(f.ctx, input); !errors.Is(err, moderation.ErrTaskConflict) {
			t.Fatalf("approval retry error=%v want task conflict", err)
		}
		f.assertNotification(t, commentID, notifications.TypeModerationApproved, author, "comment")
		f.assertNotification(t, commentID, notifications.TypeReply, recipient, "comment")
		// 回复对象同时被 @ 时，mention 合并进 reply，不再单独生成通知。
		if got := f.notificationCount(t, commentID); got != 2 {
			t.Fatalf("approval/retry created %d notifications, want 2", got)
		}
		if got := f.decisionCount(t, commentID); got != 1 {
			t.Fatalf("approval/retry created %d decisions, want 1", got)
		}
		f.assertCommentStatus(t, commentID, "active")
	})

	t.Run("rejection creates only the author result", func(t *testing.T) {
		f := newNotificationPGFixture(t)
		topicAuthor := f.insertUser(t, "reject_topic_author", "active")
		recipient := f.insertUser(t, "reject_parent", "active")
		author := f.insertUser(t, "reject_author", "active")
		reviewer := f.insertUser(t, "reject_reviewer", "active")
		topicID := f.insertTopic(t, topicAuthor, "reject topic", "active")
		parentID := f.insertComment(t, topicID, recipient, 0, "parent", "active")
		commentID := f.insertComment(t, topicID, author, parentID, "pending @reject_parent", "pending")
		outbox := notifications.NewOutbox(f.pool, notifications.NewPostgresStore(f.pool), fakeTxEnqueuer{})
		store := moderation.NewPostgresStore(f.pool).WithDecisionNotifications(moderationNotificationAdapter{outbox: outbox})

		if _, err := store.SubmitDecision(f.ctx, moderation.DecisionInput{Source: moderation.SourcePrePublish,
			TargetType: moderation.TargetTypeComment, TargetID: commentID, Action: moderation.ActionReject,
			ReviewerUserID: reviewer, ReviewNote: "not suitable"}); err != nil {
			t.Fatalf("reject pending comment: %v", err)
		}
		f.assertNotification(t, commentID, notifications.TypeModerationRejected, author, "comment")
		if got := f.notificationCount(t, commentID); got != 1 {
			t.Fatalf("rejection created %d notifications, want author result only", got)
		}
		f.assertCommentStatus(t, commentID, "rejected")
	})

	t.Run("failure after projection rolls back decision content and outbox", func(t *testing.T) {
		f := newNotificationPGFixture(t)
		topicAuthor := f.insertUser(t, "rollback_topic_author", "active")
		author := f.insertUser(t, "rollback_author", "active")
		reviewer := f.insertUser(t, "rollback_reviewer", "active")
		topicID := f.insertTopic(t, topicAuthor, "rollback topic", "active")
		commentID := f.insertComment(t, topicID, author, 0, "pending", "pending")
		outbox := notifications.NewOutbox(f.pool, notifications.NewPostgresStore(f.pool), fakeTxEnqueuer{})
		store := moderation.NewPostgresStore(f.pool).WithDecisionNotifications(moderationNotificationAdapter{outbox: outbox, failAfter: true})

		_, err := store.SubmitDecision(f.ctx, moderation.DecisionInput{Source: moderation.SourcePrePublish,
			TargetType: moderation.TargetTypeComment, TargetID: commentID, Action: moderation.ActionApprove,
			ReviewerUserID: reviewer})
		if !errors.Is(err, errFanoutAfterProjection) {
			t.Fatalf("decision error=%v want post-projection failure", err)
		}
		f.assertCommentStatus(t, commentID, "pending")
		if got := f.notificationCount(t, commentID); got != 0 {
			t.Fatalf("rollback left %d notifications", got)
		}
		if got := f.deliveryCount(t, "%"); got != 0 {
			t.Fatalf("rollback left %d deliveries", got)
		}
		if got := f.decisionCount(t, commentID); got != 0 {
			t.Fatalf("rollback left %d decisions", got)
		}
	})
}

func newNotificationPGFixture(t *testing.T) *notificationPGFixture {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is required for PostgreSQL notification integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	schema := "notification_fanout_" + hex.EncodeToString(random)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create fixture schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, notificationFanoutSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
		t.Fatalf("create fixture tables: %v", err)
	}
	f := &notificationPGFixture{ctx: ctx, admin: admin, pool: pool}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	return f
}

func (f *notificationPGFixture) insertUser(t *testing.T, username, status string) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `INSERT INTO users (username, username_lower, email, locale, display_name, status)
		VALUES ($1, lower($1), lower($1) || '@example.test', 'en-US', $1, $2) RETURNING id`, username, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *notificationPGFixture) insertTopic(t *testing.T, authorID int64, body, status string) int64 {
	t.Helper()
	var postID, topicID int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO posts (raw_content) VALUES ($1) RETURNING id`, body).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO topics (category_id, author_user_id, content_id, title, status)
		VALUES (1, $1, $2, 'topic', $3) RETURNING id`, authorID, postID, status).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	return topicID
}

func (f *notificationPGFixture) insertComment(t *testing.T, topicID, authorID, parentID int64, body, status string) int64 {
	t.Helper()
	var postID, commentID int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO posts (raw_content) VALUES ($1) RETURNING id`, body).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	var parent any
	if parentID > 0 {
		parent = parentID
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO comments (topic_id, content_id, author_user_id, parent_comment_id, status)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, topicID, postID, authorID, parent, status).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	return commentID
}

func (f *notificationPGFixture) notifyComment(t *testing.T, outbox *notifications.Outbox, event notifications.CommentEvent) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.NotifyCommentTx(f.ctx, tx, event); err != nil {
		_ = tx.Rollback(f.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *notificationPGFixture) assertNotification(t *testing.T, targetID int64, kind string, recipientID int64, targetType string) {
	t.Helper()
	var gotRecipient int64
	var gotTargetType string
	var payload string
	err := f.pool.QueryRow(f.ctx, `SELECT recipient_user_id, target_type, payload::text FROM notifications
		WHERE target_id=$1 AND type=$2`, targetID, kind).Scan(&gotRecipient, &gotTargetType, &payload)
	if err != nil {
		t.Fatalf("load %s notification for target %d: %v", kind, targetID, err)
	}
	if gotRecipient != recipientID || gotTargetType != targetType {
		t.Fatalf("notification recipient/target=(%d,%s), want=(%d,%s)", gotRecipient, gotTargetType, recipientID, targetType)
	}
	if !strings.Contains(payload, fmt.Sprintf(`"topicId": %d`, f.topicIDForTarget(t, targetID, targetType))) {
		t.Fatalf("notification payload lacks reliable topic target: %s", payload)
	}
	if targetType == "comment" && !strings.Contains(payload, fmt.Sprintf(`"commentId": %d`, targetID)) {
		t.Fatalf("comment notification payload lacks commentId: %s", payload)
	}
}

func (f *notificationPGFixture) topicIDForTarget(t *testing.T, targetID int64, targetType string) int64 {
	t.Helper()
	if targetType == "topic" {
		return targetID
	}
	if targetID >= 1000 {
		// Synthetic create-time event ids carry the real topic id in their payload.
		var topicID int64
		if err := f.pool.QueryRow(f.ctx, `SELECT (payload->>'topicId')::bigint FROM notifications WHERE target_id=$1 LIMIT 1`, targetID).Scan(&topicID); err != nil {
			t.Fatal(err)
		}
		return topicID
	}
	var topicID int64
	if err := f.pool.QueryRow(f.ctx, `SELECT topic_id FROM comments WHERE id=$1`, targetID).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	return topicID
}

func (f *notificationPGFixture) notificationCount(t *testing.T, targetID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, targetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *notificationPGFixture) deliveryCount(t *testing.T, pattern string) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail_deliveries WHERE idempotency_key LIKE $1`, pattern).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *notificationPGFixture) decisionCount(t *testing.T, targetID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM moderation_decisions WHERE target_id=$1`, targetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *notificationPGFixture) assertCommentStatus(t *testing.T, commentID int64, want string) {
	t.Helper()
	var got string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM comments WHERE id=$1`, commentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("comment status=%q want=%q", got, want)
	}
}

const notificationFanoutSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  locale TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active'
);
CREATE TABLE categories (
  id BIGSERIAL PRIMARY KEY,
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
	INSERT INTO categories DEFAULT VALUES;
	CREATE TABLE notification_type_descriptors (
	  type TEXT PRIMARY KEY,
	  category TEXT NOT NULL,
	  active BOOLEAN NOT NULL
	);
	INSERT INTO notification_type_descriptors (type,category,active) VALUES
	  ('reply','conversation',TRUE), ('mention','mention',TRUE),
	  ('moderation_approved','moderation',TRUE), ('moderation_rejected','moderation',TRUE);
	CREATE TABLE notification_type_policies (
	  type TEXT NOT NULL,
	  channel TEXT NOT NULL,
	  enabled BOOLEAN NOT NULL,
	  recommended_enabled BOOLEAN NOT NULL,
	  user_configurable BOOLEAN NOT NULL,
	  required BOOLEAN NOT NULL,
	  PRIMARY KEY (type,channel)
	);
	INSERT INTO notification_type_policies (type,channel,enabled,recommended_enabled,user_configurable,required)
	SELECT type, channel, channel IN ('in_app','email'), channel IN ('in_app','email'), TRUE, FALSE
	FROM notification_type_descriptors
	CROSS JOIN (VALUES ('in_app'),('email'),('web_push')) AS channels(channel);
	CREATE TABLE notification_preferences (
	  user_id BIGINT NOT NULL,
	  type TEXT NOT NULL,
	  channel TEXT NOT NULL,
	  state TEXT NOT NULL,
	  PRIMARY KEY (user_id,type,channel)
	);
	CREATE TABLE posts (
	  id BIGSERIAL PRIMARY KEY,
	  raw_content TEXT NOT NULL,
	  source_format TEXT NOT NULL DEFAULT 'markdown'
	);
CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL REFERENCES categories(id),
  author_user_id BIGINT REFERENCES users(id),
  content_id BIGINT NOT NULL REFERENCES posts(id),
  title TEXT NOT NULL,
  status TEXT NOT NULL,
  moderation_triggers JSONB NOT NULL DEFAULT '[]',
  comment_count BIGINT NOT NULL DEFAULT 0,
  view_count BIGINT NOT NULL DEFAULT 0,
  hot_score BIGINT NOT NULL DEFAULT 0,
  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL REFERENCES topics(id),
  content_id BIGINT NOT NULL REFERENCES posts(id),
  author_user_id BIGINT REFERENCES users(id),
  parent_comment_id BIGINT REFERENCES comments(id),
  reply_count BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  moderation_triggers JSONB NOT NULL DEFAULT '[]',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE notifications (
  id BIGSERIAL PRIMARY KEY,
  recipient_user_id BIGINT NOT NULL REFERENCES users(id),
  type TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'system',
  type_version INTEGER NOT NULL DEFAULT 1,
  payload_version INTEGER NOT NULL DEFAULT 1,
  actor_user_id BIGINT REFERENCES users(id),
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}',
  dedupe_key TEXT NOT NULL UNIQUE,
  read_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE notification_recipient_revisions (
  user_id BIGINT PRIMARY KEY REFERENCES users(id),
  revision BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE mail_deliveries (
  id BIGSERIAL PRIMARY KEY,
  recipient TEXT NOT NULL,
  template_key TEXT NOT NULL,
  template_data JSONB NOT NULL DEFAULT '{}',
  idempotency_key TEXT NOT NULL UNIQUE,
  correlation_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  extension_id TEXT NOT NULL DEFAULT '',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  error_summary TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);
CREATE TABLE moderation_decisions (
  id BIGSERIAL PRIMARY KEY,
  source TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  report_id BIGINT,
  action TEXT NOT NULL,
  reviewer_user_id BIGINT NOT NULL REFERENCES users(id),
  review_note TEXT NOT NULL DEFAULT '',
  trigger_snapshot JSONB NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`
