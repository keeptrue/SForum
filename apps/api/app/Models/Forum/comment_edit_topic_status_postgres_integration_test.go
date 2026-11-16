package forum

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 父主题状态守卫（真实 Postgres）：
//
//   - 作者编辑（Origin=self）必须落在 active 主题上：主题关闭/锁定/删除后，
//     作者不得再改写自己的评论；
//   - 版主编辑别人评论走 staff origin，不受此限（关闭主题后仍需能改注、留证据）；
//   - 判定与加锁在**同一事务**内完成：主题状态在读取之后被并发改掉时，
//     `FOR UPDATE OF topics` 会把两条路径排成先后，后到的一方看到新状态。
func TestPostgresUpdateCommentRequiresActiveTopicForAuthorEdits(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "closed_topic_author")
	moderatorID := fixture.insertUser(t, "closed_topic_moderator")
	topic := fixture.insertBareTopic(t, authorID, "closed-topic-comment-edit")
	commentID := fixture.insertRawComment(t, topic.id, authorID, CommentStatusActive)

	edit := func(editorID int64, origin string) error {
		_, err := store.UpdateComment(fixture.ctx, UpdateCommentRecord{
			CommentID: commentID, EditorUserID: editorID, ExpectedRevision: commentCurrentRevision(t, fixture, commentID),
			Origin: origin, AuthorUserID: authorID, Operation: RevisionOperationEdit,
			Content: renderedFixtureContent(t, "edited body"),
		})
		return err
	}

	// 主题还开着：作者编辑照常放行（否则下面的拒绝证明不了任何事）。
	if err := edit(authorID, RevisionOriginSelf); err != nil {
		t.Fatalf("author edit on an active topic: %v", err)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET status = 'locked' WHERE id = $1`, topic.id); err != nil {
		t.Fatal(err)
	}
	if err := edit(authorID, RevisionOriginSelf); !errors.Is(err, ErrTopicClosed) {
		t.Fatalf("author edit on a locked topic = %v, want ErrTopicClosed", err)
	}
	// 版主路径不受主题状态限制：关闭主题后仍要能改注/修正。
	if err := edit(moderatorID, RevisionOriginStaff); err != nil {
		t.Fatalf("staff edit on a locked topic must stay allowed: %v", err)
	}
}

// 并发窗口：作者编辑在读状态之前先锁主题行，所以「读完状态、别人把主题关掉、
// 再写」这条经典窗口不存在——后到的编辑会在锁上等，拿到锁时看到的是新状态。
func TestPostgresUpdateCommentSerializesAgainstConcurrentTopicClose(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "race_topic_author")
	topic := fixture.insertBareTopic(t, authorID, "race-topic-comment-edit")
	commentID := fixture.insertRawComment(t, topic.id, authorID, CommentStatusActive)

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(fixture.ctx) }()
	if _, err := tx.Exec(fixture.ctx, `SELECT id FROM topics WHERE id = $1 FOR UPDATE`, topic.id); err != nil {
		t.Fatal(err)
	}

	revision := commentCurrentRevision(t, fixture, commentID)
	done := make(chan error, 1)
	go func() {
		_, err := store.UpdateComment(context.Background(), UpdateCommentRecord{
			CommentID: commentID, EditorUserID: authorID, ExpectedRevision: revision,
			Origin: RevisionOriginSelf, AuthorUserID: authorID, Operation: RevisionOperationEdit,
			Content: renderedFixtureContent(t, "racing body"),
		})
		done <- err
	}()

	// 编辑必须卡在主题锁上，而不是带着旧状态直接写下去。
	select {
	case err := <-done:
		t.Fatalf("author edit did not wait for the topic lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if _, err := tx.Exec(fixture.ctx, `UPDATE topics SET status = 'locked' WHERE id = $1`, topic.id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrTopicClosed) {
			t.Fatalf("edit that lost the race = %v, want ErrTopicClosed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("author edit never completed after the topic lock was released")
	}
}

// commentCurrentRevision 读评论正文的当前修订号（fixture 直接插行，版本号由默认值决定）。
func commentCurrentRevision(t *testing.T, fixture *revisionLedgerPGFixture, commentID int64) int64 {
	t.Helper()
	var revision int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT posts.current_revision FROM comments JOIN posts ON posts.id = comments.content_id WHERE comments.id = $1
	`, commentID).Scan(&revision); err != nil {
		t.Fatalf("read comment revision: %v", err)
	}
	return revision
}
