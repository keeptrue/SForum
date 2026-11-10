package forum

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// bookmarksPGFixture 在 revision ledger fixture 之上补齐收藏切片的 schema。
//
// 与 reactionsPGFixture 同一姿态：复用它的 schema 隔离、insertUser /
// insertBareTopic helper，再叠一次与真实迁移同形的 DDL。base schema 里没有
// content_bookmarks 与 public_bookmark_count，所以这里必须自己建。
type bookmarksPGFixture struct {
	*revisionLedgerPGFixture
}

func newBookmarksPGFixture(t *testing.T) *bookmarksPGFixture {
	t.Helper()
	fixture := &bookmarksPGFixture{revisionLedgerPGFixture: newRevisionLedgerPGFixture(t)}
	if _, err := fixture.pool.Exec(fixture.ctx, bookmarksFixtureSchemaSQL); err != nil {
		t.Fatalf("apply bookmarks fixture DDL: %v", err)
	}
	return fixture
}

// bookmarksFixtureSchemaSQL 是 database/migrations/202611100001_content_bookmarks.sql
// Up 段的最小镜像：表 + 四个 CHECK + 唯一约束 + 三个索引，topics/comments 的
// public_bookmark_count 列与非负约束（NOT VALID + VALIDATE 写法一致）。
//
// 刻意不复制稳定读视图 sforum_core_v1.forum_bookmarks 与通知描述符种子：前者依赖
// sforum_core_v1 schema 与分类可见性谓词，由 database/migrations 的结构测试逐字
// 固定；后者由迁移测试守护。改动迁移时必须同步这里。
const bookmarksFixtureSchemaSQL = `
CREATE TABLE content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'private',
  remind_at TIMESTAMPTZ,
  reminded_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_bookmarks_target_type_check CHECK (target_type IN ('topic', 'comment')),
  CONSTRAINT content_bookmarks_visibility_check CHECK (visibility IN ('private', 'public')),
  CONSTRAINT content_bookmarks_target_positive CHECK (target_id > 0),
  CONSTRAINT content_bookmarks_unique UNIQUE (user_id, target_type, target_id)
);

CREATE INDEX content_bookmarks_public_idx
  ON content_bookmarks (target_type, target_id, created_at DESC, id DESC)
  WHERE visibility = 'public';

CREATE INDEX content_bookmarks_user_idx
  ON content_bookmarks (user_id, created_at DESC, id DESC);

CREATE INDEX content_bookmarks_due_idx
  ON content_bookmarks (remind_at)
  WHERE remind_at IS NOT NULL AND reminded_at IS NULL;

ALTER TABLE topics ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE comments ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0;

ALTER TABLE topics ADD CONSTRAINT topics_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID;
ALTER TABLE comments ADD CONSTRAINT comments_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID;
ALTER TABLE topics VALIDATE CONSTRAINT topics_public_bookmark_count_nonnegative;
ALTER TABLE comments VALIDATE CONSTRAINT comments_public_bookmark_count_nonnegative;

-- 软删可见性谓词依赖这两列；base fixture 没建，store 的可见性与提醒扫描都要用。
ALTER TABLE topics ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE comments ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
`

func (f *bookmarksPGFixture) bookmarkRows(t *testing.T, targetType string, targetID int64) int64 {
	t.Helper()
	var rows int64
	if err := f.pool.QueryRow(f.ctx, `
		SELECT COUNT(*) FROM content_bookmarks WHERE target_type = $1 AND target_id = $2
	`, targetType, targetID).Scan(&rows); err != nil {
		t.Fatalf("count bookmark rows: %v", err)
	}
	return rows
}

func (f *bookmarksPGFixture) publicBookmarkCount(t *testing.T, table string, targetID int64) int64 {
	t.Helper()
	var count int64
	if err := f.pool.QueryRow(f.ctx, `SELECT public_bookmark_count FROM `+table+` WHERE id = $1`, targetID).Scan(&count); err != nil {
		t.Fatalf("read public bookmark count: %v", err)
	}
	return count
}

func (f *bookmarksPGFixture) remindedAtFor(t *testing.T, userID int64, targetType string, targetID int64) *time.Time {
	t.Helper()
	var remindedAt *time.Time
	if err := f.pool.QueryRow(f.ctx, `
		SELECT reminded_at FROM content_bookmarks
		WHERE user_id = $1 AND target_type = $2 AND target_id = $3
	`, userID, targetType, targetID).Scan(&remindedAt); err != nil {
		t.Fatalf("read reminded_at: %v", err)
	}
	return remindedAt
}

// TestPostgresApplyBookmarkCountsOnlyPublicRowsFourWays 是公开计数的四向口径：
//   - private→public  +1
//   - public→private  -1
//   - public 删除     -1
//   - private 删除     不变
//
// 每一向都同时断言关系行数与计数列，避免「计数对了但行没了」这类假绿。
func TestPostgresApplyBookmarkCountsOnlyPublicRowsFourWays(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "bookmark_author")
	memberID := fixture.insertUser(t, "bookmark_member")
	topic := fixture.insertBareTopic(t, authorID, "bookmark-count-topic")

	apply := func(add bool, visibility string) BookmarkState {
		t.Helper()
		state, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
			ActorUserID: memberID,
			TargetType:  BookmarkTargetTopic,
			TargetID:    topic.id,
			TopicID:     topic.id,
			Visibility:  visibility,
			Add:         add,
		})
		if err != nil {
			t.Fatalf("ApplyBookmark(add=%t visibility=%q): %v", add, visibility, err)
		}
		return state
	}

	// 1) 首次私密收藏：行 +1，公开计数不变。
	state := apply(true, BookmarkVisibilityPrivate)
	if state.PublicBookmarkCount != 0 {
		t.Fatalf("private bookmark must not count: %#v", state)
	}
	if rows := fixture.bookmarkRows(t, BookmarkTargetTopic, topic.id); rows != 1 {
		t.Fatalf("rows after private bookmark = %d, want 1", rows)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 0 {
		t.Fatalf("public count after private bookmark = %d, want 0", count)
	}

	// 2) private→public：公开计数 +1。
	state = apply(true, BookmarkVisibilityPublic)
	if state.PublicBookmarkCount != 1 {
		t.Fatalf("private→public must add 1: %#v", state)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 1 {
		t.Fatalf("public count after private→public = %d, want 1", count)
	}

	// 3) public→private：公开计数 -1。
	state = apply(true, BookmarkVisibilityPrivate)
	if state.PublicBookmarkCount != 0 {
		t.Fatalf("public→private must subtract 1: %#v", state)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 0 {
		t.Fatalf("public count after public→private = %d, want 0", count)
	}

	// 4) private 删除：计数不变（仍为 0），行消失。
	state = apply(false, "")
	if state.Bookmarked || state.PublicBookmarkCount != 0 {
		t.Fatalf("private delete must keep count: %#v", state)
	}
	if rows := fixture.bookmarkRows(t, BookmarkTargetTopic, topic.id); rows != 0 {
		t.Fatalf("rows after private delete = %d, want 0", rows)
	}

	// 5) public 删除：计数 -1。
	apply(true, BookmarkVisibilityPublic)
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 1 {
		t.Fatalf("public count before public delete = %d, want 1", count)
	}
	state = apply(false, "")
	if state.Bookmarked || state.PublicBookmarkCount != 0 {
		t.Fatalf("public delete must subtract 1: %#v", state)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 0 {
		t.Fatalf("public count after public delete = %d, want 0", count)
	}
	if rows := fixture.bookmarkRows(t, BookmarkTargetTopic, topic.id); rows != 0 {
		t.Fatalf("rows after public delete = %d, want 0", rows)
	}

	// 6) 重复删除幂等：没有行时删除不报错、不动计数。
	state = apply(false, "")
	if state.PublicBookmarkCount != 0 {
		t.Fatalf("idempotent delete drifted the count: %#v", state)
	}
}

// TestPostgresApplyBookmarkCommentTargetsMaintainOwnCount 固定评论目标走同一套
// 计数口径（private→public +1、public 删除 -1），且不动主题的计数。
func TestPostgresApplyBookmarkCommentTargetsMaintainOwnCount(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "bookmark_comment_author")
	memberID := fixture.insertUser(t, "bookmark_comment_member")
	topic := fixture.insertBareTopic(t, authorID, "bookmark-comment-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "收藏计数评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	apply := func(add bool, visibility string) BookmarkState {
		t.Helper()
		state, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
			ActorUserID: memberID,
			TargetType:  BookmarkTargetComment,
			TargetID:    comment.ID,
			TopicID:     topic.id,
			Visibility:  visibility,
			Add:         add,
		})
		if err != nil {
			t.Fatalf("ApplyBookmark(comment): %v", err)
		}
		return state
	}

	if state := apply(true, BookmarkVisibilityPublic); state.PublicBookmarkCount != 1 {
		t.Fatalf("comment public bookmark count = %d, want 1", state.PublicBookmarkCount)
	}
	if count := fixture.publicBookmarkCount(t, "comments", comment.ID); count != 1 {
		t.Fatalf("comments.public_bookmark_count = %d, want 1", count)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 0 {
		t.Fatalf("comment bookmark must not touch the topic count, got %d", count)
	}
	if state := apply(false, ""); state.PublicBookmarkCount != 0 {
		t.Fatalf("comment public delete count = %d, want 0", state.PublicBookmarkCount)
	}
}

// TestPostgresApplyBookmarkConcurrentPutsCollapseToOneRow 是收藏的唯一性口径：
// 同一用户对同一目标并发 PUT 只能留下一条关系行，公开计数只能 +1。
func TestPostgresApplyBookmarkConcurrentPutsCollapseToOneRow(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "bookmark_concurrent_author")
	memberID := fixture.insertUser(t, "bookmark_concurrent_member")
	topic := fixture.insertBareTopic(t, authorID, "bookmark-concurrent-topic")

	const workers = 10
	states := make([]BookmarkState, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			states[index], errs[index] = store.ApplyBookmark(context.Background(), BookmarkApplyInput{
				ActorUserID: memberID,
				TargetType:  BookmarkTargetTopic,
				TargetID:    topic.id,
				TopicID:     topic.id,
				Visibility:  BookmarkVisibilityPublic,
				Add:         true,
			})
		}(i)
	}
	wg.Wait()

	for i := range states {
		if errs[i] != nil {
			t.Fatalf("concurrent bookmark #%d: %v", i, errs[i])
		}
		if !states[i].Bookmarked {
			t.Fatalf("concurrent bookmark #%d returned bookmarked=false", i)
		}
		if states[i].PublicBookmarkCount != 1 {
			t.Fatalf("concurrent bookmark #%d count=%d, want 1 (above 1 means the counter drifted)", i, states[i].PublicBookmarkCount)
		}
	}
	if rows := fixture.bookmarkRows(t, BookmarkTargetTopic, topic.id); rows != 1 {
		t.Fatalf("rows after concurrent puts = %d, want 1", rows)
	}
	if count := fixture.publicBookmarkCount(t, "topics", topic.id); count != 1 {
		t.Fatalf("public count after concurrent puts = %d, want 1", count)
	}
}

// TestPostgresRunDueBookmarkRemindersClaimsEachRowOnce 固定「只发一次」：
// UPDATE ... WHERE reminded_at IS NULL RETURNING 抢占后，第二轮不再返回同一行；
// 目标不可见时仍然写 reminded_at 但不发通知。
func TestPostgresRunDueBookmarkRemindersClaimsEachRowOnce(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	notifier := &recordingBookmarkReminderNotifier{}
	store := NewPostgresStore(fixture.pool).WithCommentNotifications(notifier)
	authorID := fixture.insertUser(t, "reminder_author")
	memberID := fixture.insertUser(t, "reminder_member")
	topic := fixture.insertBareTopic(t, authorID, "reminder-topic")

	dueAt := time.Now().Add(-time.Minute)
	visibleState, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true, RemindAt: &dueAt,
	})
	if err != nil {
		t.Fatalf("ApplyBookmark: %v", err)
	}
	if visibleState.RemindAt == nil {
		t.Fatal("remindAt must be reported back")
	}

	claimed, notified, err := store.RunDueBookmarkReminders(context.Background(), time.Now().UTC(), 200)
	if err != nil {
		t.Fatalf("RunDueBookmarkReminders: %v", err)
	}
	if claimed != 1 || notified != 1 {
		t.Fatalf("first run claimed=%d notified=%d, want 1/1", claimed, notified)
	}
	if len(notifier.inputs) != 1 || notifier.inputs[0].RecipientUserID != memberID || notifier.inputs[0].TargetID != topic.id {
		t.Fatalf("unexpected reminder inputs %#v", notifier.inputs)
	}
	if remindedAt := fixture.remindedAtFor(t, memberID, BookmarkTargetTopic, topic.id); remindedAt == nil {
		t.Fatal("reminded_at must be set after a successful claim")
	}
	// 名单要能区分「将提醒 / 已提醒」：提醒发出后 remindedAt 必须出现在条目上。
	list, err := store.ListUserBookmarks(context.Background(), memberID, true, 20, 0)
	if err != nil {
		t.Fatalf("ListUserBookmarks after reminder: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].RemindAt == nil || list.Items[0].RemindedAt == nil {
		t.Fatalf("list must surface remindAt+remindedAt after delivery: %#v", list.Items)
	}

	// 第二轮：行已被标记，不再抢占。
	claimed, notified, err = store.RunDueBookmarkReminders(context.Background(), time.Now().UTC(), 200)
	if err != nil {
		t.Fatalf("second RunDueBookmarkReminders: %v", err)
	}
	if claimed != 0 || notified != 0 || len(notifier.inputs) != 1 {
		t.Fatalf("second run claimed=%d notified=%d inputs=%d, want 0/0/1", claimed, notified, len(notifier.inputs))
	}
}

// TestPostgresRunDueBookmarkRemindersSkipsInvisibleTargets 固定不可见目标的处置：
// 仍然标 reminded_at（否则每轮重扫），但不写通知投影。
func TestPostgresRunDueBookmarkRemindersSkipsInvisibleTargets(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	notifier := &recordingBookmarkReminderNotifier{}
	store := NewPostgresStore(fixture.pool).WithCommentNotifications(notifier)
	authorID := fixture.insertUser(t, "reminder_hidden_author")
	memberID := fixture.insertUser(t, "reminder_hidden_member")
	topic := fixture.insertBareTopic(t, authorID, "reminder-hidden-topic")

	dueAt := time.Now().Add(-time.Minute)
	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Visibility: BookmarkVisibilityPublic, Add: true, RemindAt: &dueAt,
	}); err != nil {
		t.Fatalf("ApplyBookmark: %v", err)
	}
	// 主题被删除：视图与提醒都认为目标不可见。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET deleted_at = now() WHERE id = $1`, topic.id); err != nil {
		t.Fatalf("soft delete topic: %v", err)
	}

	claimed, notified, err := store.RunDueBookmarkReminders(context.Background(), time.Now().UTC(), 200)
	if err != nil {
		t.Fatalf("RunDueBookmarkReminders: %v", err)
	}
	if claimed != 1 || notified != 0 {
		t.Fatalf("invisible target claimed=%d notified=%d, want 1/0", claimed, notified)
	}
	if len(notifier.inputs) != 0 {
		t.Fatalf("invisible target must not notify: %#v", notifier.inputs)
	}
	if fixture.remindedAtFor(t, memberID, BookmarkTargetTopic, topic.id) == nil {
		t.Fatal("invisible target must still be marked reminded to avoid rescanning forever")
	}
}

// TestPostgresRunDueBookmarkRemindersRespectsBatchLimit 固定批量上限：limit 决定
// 单轮最多处理多少条，其余留到下一轮。
func TestPostgresRunDueBookmarkRemindersRespectsBatchLimit(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	notifier := &recordingBookmarkReminderNotifier{}
	store := NewPostgresStore(fixture.pool).WithCommentNotifications(notifier)
	authorID := fixture.insertUser(t, "reminder_batch_author")
	memberID := fixture.insertUser(t, "reminder_batch_member")

	dueAt := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		topic := fixture.insertBareTopic(t, authorID, "reminder-batch-topic-"+string(rune('a'+i)))
		if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
			ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
			Visibility: BookmarkVisibilityPublic, Add: true, RemindAt: &dueAt,
		}); err != nil {
			t.Fatalf("ApplyBookmark #%d: %v", i, err)
		}
	}

	claimed, notified, err := store.RunDueBookmarkReminders(context.Background(), time.Now().UTC(), 2)
	if err != nil {
		t.Fatalf("RunDueBookmarkReminders: %v", err)
	}
	if claimed != 2 || notified != 2 {
		t.Fatalf("limited run claimed=%d notified=%d, want 2/2", claimed, notified)
	}
	claimed, notified, err = store.RunDueBookmarkReminders(context.Background(), time.Now().UTC(), 2)
	if err != nil {
		t.Fatalf("second RunDueBookmarkReminders: %v", err)
	}
	if claimed != 1 || notified != 1 {
		t.Fatalf("second limited run claimed=%d notified=%d, want 1/1", claimed, notified)
	}
}

// TestPostgresListUserBookmarksHidesPrivateRowsFromOthers 固定名单可见性：
// 行主看到全部行，他人只看到公开行。
func TestPostgresListUserBookmarksHidesPrivateRowsFromOthers(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "list_author")
	ownerID := fixture.insertUser(t, "list_owner")
	publicTopic := fixture.insertBareTopic(t, authorID, "list-public-topic")
	privateTopic := fixture.insertBareTopic(t, authorID, "list-private-topic")

	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: ownerID, TargetType: BookmarkTargetTopic, TargetID: publicTopic.id, TopicID: publicTopic.id,
		Visibility: BookmarkVisibilityPublic, Add: true,
	}); err != nil {
		t.Fatalf("public bookmark: %v", err)
	}
	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: ownerID, TargetType: BookmarkTargetTopic, TargetID: privateTopic.id, TopicID: privateTopic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true,
	}); err != nil {
		t.Fatalf("private bookmark: %v", err)
	}

	ownerList, err := store.ListUserBookmarks(context.Background(), ownerID, true, 20, 0)
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	if len(ownerList.Items) != 2 || ownerList.Total != 2 {
		t.Fatalf("owner list = %#v", ownerList)
	}
	otherList, err := store.ListUserBookmarks(context.Background(), ownerID, false, 20, 0)
	if err != nil {
		t.Fatalf("other list: %v", err)
	}
	if len(otherList.Items) != 1 || otherList.Total != 1 {
		t.Fatalf("other list must hide private rows: %#v", otherList)
	}
	if otherList.Items[0].Visibility != BookmarkVisibilityPublic {
		t.Fatalf("other list row visibility = %q", otherList.Items[0].Visibility)
	}
	if otherList.Items[0].Topic == nil || otherList.Items[0].Topic.ID != publicTopic.id {
		t.Fatalf("other list must hydrate the public topic summary: %#v", otherList.Items[0].Topic)
	}
}

// recordingBookmarkReminderNotifier 同时满足 NotificationWriter 与
// BookmarkReminderNotifier：收藏提醒是可选投影，不塞进 NotificationWriter。
type recordingBookmarkReminderNotifier struct {
	inputs []BookmarkReminderNotificationInput
	err    error
}

func (n *recordingBookmarkReminderNotifier) NotifyCommentTx(context.Context, pgx.Tx, CommentNotificationInput) error {
	return nil
}

func (n *recordingBookmarkReminderNotifier) NotifyTopicTx(context.Context, pgx.Tx, TopicNotificationInput) error {
	return nil
}

func (n *recordingBookmarkReminderNotifier) NotifyPendingReviewTx(context.Context, pgx.Tx, PendingReviewNotificationInput) error {
	return nil
}

func (n *recordingBookmarkReminderNotifier) NotifyReactionTx(context.Context, pgx.Tx, ReactionNotificationInput) error {
	return nil
}

func (n *recordingBookmarkReminderNotifier) NotifyBookmarkReminderTx(_ context.Context, _ pgx.Tx, input BookmarkReminderNotificationInput) error {
	if n.err != nil {
		return n.err
	}
	n.inputs = append(n.inputs, input)
	return nil
}

// TestPostgresClearBookmarkReminderClearsBothColumnsAndCanBeReset 固定 E14(c)：
// clearReminder 必须把 remind_at 与 reminded_at 一起清掉，且清掉后可以重设同一个
// 时间（重设会把「已提醒」标记复位，否则新提醒会被当成已经发过）。
func TestPostgresClearBookmarkReminderClearsBothColumnsAndCanBeReset(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "clear_reminder_author")
	memberID := fixture.insertUser(t, "clear_reminder_member")
	topic := fixture.insertBareTopic(t, authorID, "clear-reminder-topic")

	remindAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	state, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true, RemindAt: &remindAt,
	})
	if err != nil {
		t.Fatalf("set reminder: %v", err)
	}
	if state.RemindAt == nil || !state.RemindAt.Equal(remindAt) {
		t.Fatalf("reminder not stored: %#v", state)
	}
	// 模拟提醒已发出：直接写 reminded_at（job 的真实路径由提醒用例覆盖）。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE content_bookmarks SET reminded_at = now() WHERE user_id = $1 AND target_type = 'topic' AND target_id = $2
	`, memberID, topic.id); err != nil {
		t.Fatalf("mark reminded: %v", err)
	}

	cleared, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Add: true, ClearReminder: true,
	})
	if err != nil {
		t.Fatalf("clear reminder: %v", err)
	}
	if cleared.RemindAt != nil || cleared.RemindedAt != nil {
		t.Fatalf("clearReminder must wipe both columns: %#v", cleared)
	}
	var (
		dbRemindAt   *time.Time
		dbRemindedAt *time.Time
	)
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT remind_at, reminded_at FROM content_bookmarks
		WHERE user_id = $1 AND target_type = 'topic' AND target_id = $2
	`, memberID, topic.id).Scan(&dbRemindAt, &dbRemindedAt); err != nil {
		t.Fatalf("read cleared row: %v", err)
	}
	if dbRemindAt != nil || dbRemindedAt != nil {
		t.Fatalf("db still holds reminder after clear: remind_at=%v reminded_at=%v", dbRemindAt, dbRemindedAt)
	}

	// 重设同一个时间：remind_at 回到该值，reminded_at 复位为 NULL。
	reset, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Add: true, RemindAt: &remindAt,
	})
	if err != nil {
		t.Fatalf("reset reminder: %v", err)
	}
	if reset.RemindAt == nil || !reset.RemindAt.Equal(remindAt) || reset.RemindedAt != nil {
		t.Fatalf("reset reminder state = %#v", reset)
	}
	// 幂等：同样参数再 PUT 一次仍然是同一个值。
	again, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Add: true, RemindAt: &remindAt,
	})
	if err != nil || again.RemindAt == nil || !again.RemindAt.Equal(remindAt) {
		t.Fatalf("idempotent reset = %#v err=%v", again, err)
	}
}

// TestPostgresBookmarkReminderLimitRejectsExtraPending 固定未到期提醒上限：
// 已有 200 条待发提醒时，新目标再设提醒被拒绝；更新已占名额的那条不受影响。
func TestPostgresBookmarkReminderLimitRejectsExtraPending(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "limit_author")
	memberID := fixture.insertUser(t, "limit_member")
	remindAt := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Microsecond)

	// 造 limit-1 条待发提醒（target 用不存在的 topic id：上限检查只看提醒列），
	// 再用真实主题补到 limit 条——更新这条要能走通计数读回，所以它必须是真主题。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility, remind_at)
		SELECT $1, 'topic', 900000 + g, 'private', $2
		FROM generate_series(1, $3) AS g
	`, memberID, remindAt, BookmarkReminderLimit-1); err != nil {
		t.Fatalf("seed pending reminders: %v", err)
	}
	existingTopic := fixture.insertBareTopic(t, authorID, "limit-existing-topic")
	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: existingTopic.id, TopicID: existingTopic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true, RemindAt: &remindAt,
	}); err != nil {
		t.Fatalf("seed the last pending reminder: %v", err)
	}

	topic := fixture.insertBareTopic(t, authorID, "limit-topic")
	_, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true, RemindAt: &remindAt,
	})
	if !errors.Is(err, ErrBookmarkReminderLimit) {
		t.Fatalf("want ErrBookmarkReminderLimit, got %v", err)
	}
	if rows := fixture.bookmarkRows(t, BookmarkTargetTopic, topic.id); rows != 0 {
		t.Fatalf("rejected reminder must not create a row, got %d", rows)
	}

	// 更新已占名额的那条（排除自身后 pending=limit-1）应当被接受。
	// 截到微秒：timestamptz 只存微秒，纳秒差会让 Equal 失败。
	existing := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Microsecond)
	updated, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: memberID, TargetType: BookmarkTargetTopic, TargetID: existingTopic.id, TopicID: existingTopic.id,
		Add: true, RemindAt: &existing,
	})
	if err != nil {
		t.Fatalf("updating an existing pending reminder must be allowed: %v", err)
	}
	if updated.RemindAt == nil || !updated.RemindAt.Equal(existing) {
		t.Fatalf("updated reminder = %#v, want %v", updated.RemindAt, existing)
	}
}

// TestPostgresListUserBookmarksTotalMatchesVisibleRows 固定 Total/HasMore 口径：
// COUNT 与列表用同一个可见性谓词，目标被隐藏后不得留下假的「加载更多」。
func TestPostgresListUserBookmarksTotalMatchesVisibleRows(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "total_author")
	ownerID := fixture.insertUser(t, "total_owner")
	visibleTopic := fixture.insertBareTopic(t, authorID, "total-visible-topic")
	hiddenTopic := fixture.insertBareTopic(t, authorID, "total-hidden-topic")

	for _, topicID := range []int64{visibleTopic.id, hiddenTopic.id} {
		if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
			ActorUserID: ownerID, TargetType: BookmarkTargetTopic, TargetID: topicID, TopicID: topicID,
			Visibility: BookmarkVisibilityPublic, Add: true,
		}); err != nil {
			t.Fatalf("bookmark topic %d: %v", topicID, err)
		}
	}
	// 主题被删除：行还在，但目标不可见。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET deleted_at = now() WHERE id = $1`, hiddenTopic.id); err != nil {
		t.Fatalf("hide topic: %v", err)
	}

	for _, includePrivate := range []bool{true, false} {
		list, err := store.ListUserBookmarks(context.Background(), ownerID, includePrivate, 1, 0)
		if err != nil {
			t.Fatalf("ListUserBookmarks(includePrivate=%t): %v", includePrivate, err)
		}
		if list.Total != 1 || len(list.Items) != 1 {
			t.Fatalf("includePrivate=%t list = total %d items %d, want 1/1", includePrivate, list.Total, len(list.Items))
		}
		if list.HasMore {
			t.Fatalf("includePrivate=%t HasMore must be false: total=%d items=%d", includePrivate, list.Total, len(list.Items))
		}
		if list.Items[0].TargetID != visibleTopic.id {
			t.Fatalf("unexpected visible row: %#v", list.Items[0])
		}
	}
}

// TestPostgresListUserBookmarksNeverLeaksReminderFieldsToOthers 固定隐私约束：
// remind_at / reminded_at 是行主私有计划数据，owner-only。他人视角（登录或匿名）
// 拉公开名单时两列必须为空——即使行主确实设了提醒；本人视角仍能拿到两列。
//
// SQL 层用 CASE 把两列置 NULL（私有数据根本不进 DTO），Go 侧再清一次兜底；
// 这条用例同时锁住两道闸，任何一道被移除都会变红。
func TestPostgresListUserBookmarksNeverLeaksReminderFieldsToOthers(t *testing.T) {
	fixture := newBookmarksPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "reminder_privacy_author")
	ownerID := fixture.insertUser(t, "reminder_privacy_owner")
	// otherID 只进 Go 侧兜底断言（service 路径的登录他人视角）；store 的
	// 他人视角由匿名（viewer=0）与 includePrivate=false 覆盖。
	_ = fixture.insertUser(t, "reminder_privacy_other")
	topic := fixture.insertBareTopic(t, authorID, "reminder-privacy-topic")

	// 公开收藏 + 已设定提醒：泄露场景的完整前置条件。
	remindAt := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: ownerID, TargetType: BookmarkTargetTopic, TargetID: topic.id, TopicID: topic.id,
		Visibility: BookmarkVisibilityPublic, Add: true, RemindAt: &remindAt,
	}); err != nil {
		t.Fatalf("bookmark with reminder: %v", err)
	}

	// 匿名视角（viewer=0，经 service：includePrivate=false）：两列必须为空。
	// 真正的匿名 HTTP 请求走这条路径（Profile service 先解析 username，再以
	// viewer=0 调 Forum 的 ListUserBookmarks）。
	service := NewBookmarkService(NewService(ServiceConfig{Store: store})).WithStore(store)
	anonymous, err := service.ListUserBookmarks(context.Background(), 0, ownerID, 20, 0)
	if err != nil {
		t.Fatalf("anonymous list: %v", err)
	}
	if len(anonymous.Items) != 1 {
		t.Fatalf("anonymous list = %#v, want 1 public row", anonymous)
	}
	if anonymous.Items[0].RemindAt != nil || anonymous.Items[0].RemindedAt != nil {
		t.Fatalf("anonymous viewer received private schedule data: %#v", anonymous.Items[0])
	}

	// 登录他人视角：同样必须为空。
	// 他人视角：ownerUserID 是名单主人（ownerID），includePrivate=false。
	other, err := store.ListUserBookmarks(context.Background(), ownerID, false, 20, 0)
	if err != nil {
		t.Fatalf("other-member list: %v", err)
	}
	if len(other.Items) != 1 {
		t.Fatalf("other-member list = %#v, want 1 public row", other)
	}
	if other.Items[0].RemindAt != nil || other.Items[0].RemindedAt != nil {
		t.Fatalf("other member received private schedule data: %#v", other.Items[0])
	}

	// 本人视角（回归）：两列仍然可用，不能把功能修坏。
	owner, err := store.ListUserBookmarks(context.Background(), ownerID, true, 20, 0)
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	if len(owner.Items) != 1 || owner.Items[0].RemindAt == nil || !owner.Items[0].RemindAt.Equal(remindAt) || owner.Items[0].RemindedAt != nil {
		t.Fatalf("owner list must keep the reminder: %#v", owner.Items)
	}

	// 私密行（owner 视角）同样携带提醒：可见性与提醒是两个独立维度。
	comment, err := store.CreateComment(context.Background(), CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "隐私提醒评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if _, err := store.ApplyBookmark(context.Background(), BookmarkApplyInput{
		ActorUserID: ownerID, TargetType: BookmarkTargetComment, TargetID: comment.ID, TopicID: topic.id,
		Visibility: BookmarkVisibilityPrivate, Add: true, RemindAt: &remindAt,
	}); err != nil {
		t.Fatalf("private bookmark with reminder: %v", err)
	}
	ownAll, err := store.ListUserBookmarks(context.Background(), ownerID, true, 20, 0)
	if err != nil {
		t.Fatalf("owner full list: %v", err)
	}
	if len(ownAll.Items) != 2 {
		t.Fatalf("owner full list = %d items, want 2", len(ownAll.Items))
	}
	for _, item := range ownAll.Items {
		if item.RemindAt == nil {
			t.Fatalf("owner row is missing its reminder: %#v", item)
		}
	}
}
