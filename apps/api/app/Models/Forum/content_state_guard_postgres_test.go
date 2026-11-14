package forum

import (
	"errors"
	"testing"
)

// 删除必须推进内容版本：删除前发出的 expectedRevision 立刻失效，即使内容
// 之后被恢复（status 回到 active）也不能通过 CAS。
func TestContentStateDeleteBumpsTopicRevisionPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "state_topic_author")

	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug: "general", AuthorUserID: authorID, Title: "State machine topic", Slug: "state-machine-topic",
		TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "state body"), Status: TopicStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if topic.CurrentRevision != 1 {
		t.Fatalf("new topic currentRevision=%d, want 1", topic.CurrentRevision)
	}

	deleted, err := store.DeleteTopic(fixture.ctx, topic.ID)
	if err != nil {
		t.Fatalf("DeleteTopic: %v", err)
	}
	if deleted.CurrentRevision != 2 {
		t.Fatalf("deleted topic currentRevision=%d, want 2 (delete must bump)", deleted.CurrentRevision)
	}
	var storedRevision int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT current_revision FROM posts WHERE id = $1
	`, topic.Content.ID).Scan(&storedRevision); err != nil {
		t.Fatalf("load post current_revision: %v", err)
	}
	if storedRevision != 2 {
		t.Fatalf("posts.current_revision=%d after delete, want 2", storedRevision)
	}

	// 模拟版主恢复：状态回到 active 后，删除前的令牌仍然必须失效。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET status = 'active' WHERE id = $1`, topic.ID); err != nil {
		t.Fatalf("restore topic status: %v", err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginSelf, Title: "stale token edit", Slug: "stale-token-edit", TagCreationMode: TagCreationModeControlled,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("pre-delete expectedRevision must conflict, got err=%v", err)
	}

	accepted, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2,
		Origin: RevisionOriginSelf, Title: "post-delete edit", Slug: "post-delete-edit", TagCreationMode: TagCreationModeControlled,
	})
	if err != nil {
		t.Fatalf("UpdateTopic with bumped revision: %v", err)
	}
	if accepted.CurrentRevision != 3 {
		t.Fatalf("accepted revision=%d, want 3", accepted.CurrentRevision)
	}
}

func TestContentStateDeleteBumpsCommentRevisionPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "state_comment_author")
	topic := fixture.insertBareTopic(t, authorID, "state-comment-host")

	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID: topic.id, AuthorUserID: authorID, Content: renderedFixtureContent(t, "comment body"), Status: CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if comment.CurrentRevision != 1 {
		t.Fatalf("new comment currentRevision=%d, want 1", comment.CurrentRevision)
	}

	deleted, err := store.DeleteComment(fixture.ctx, comment.ID)
	if err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if deleted.CurrentRevision != 2 {
		t.Fatalf("deleted comment currentRevision=%d, want 2 (delete must bump)", deleted.CurrentRevision)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE comments SET status = 'active' WHERE id = $1`, comment.ID); err != nil {
		t.Fatalf("restore comment status: %v", err)
	}
	if _, err := store.UpdateComment(fixture.ctx, UpdateCommentRecord{
		CommentID: comment.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginSelf, Content: renderedFixtureContent(t, "stale token body"),
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("pre-delete expectedRevision must conflict, got err=%v", err)
	}

	accepted, err := store.UpdateComment(fixture.ctx, UpdateCommentRecord{
		CommentID: comment.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2,
		Origin: RevisionOriginSelf, Content: renderedFixtureContent(t, "post-delete body"),
	})
	if err != nil {
		t.Fatalf("UpdateComment with bumped revision: %v", err)
	}
	if accepted.CurrentRevision != 3 {
		t.Fatalf("accepted comment revision=%d, want 3", accepted.CurrentRevision)
	}
}

// 编辑触发预审的 UPDATE 必须带状态谓词：hidden 内容不能被洗回 pending。
func TestContentStateRequeueCannotWashHiddenContentToPendingPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "state_requeue_author")

	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug: "general", AuthorUserID: authorID, Title: "Hidden requeue topic", Slug: "hidden-requeue-topic",
		TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "hidden body"), Status: TopicStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID: topic.ID, AuthorUserID: authorID, Content: renderedFixtureContent(t, "hidden comment"), Status: CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET status = 'hidden' WHERE id = $1`, topic.ID); err != nil {
		t.Fatalf("hide topic: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE comments SET status = 'hidden' WHERE id = $1`, comment.ID); err != nil {
		t.Fatalf("hide comment: %v", err)
	}

	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginSelf, Title: "hidden edit", Slug: "hidden-edit", TagCreationMode: TagCreationModeControlled,
		RequeuePending: true, ModerationTriggers: []string{"outbound_link"},
	}); !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("requeue on hidden topic must be rejected, got err=%v", err)
	}
	var status, title string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status, title FROM topics WHERE id = $1
	`, topic.ID).Scan(&status, &title); err != nil {
		t.Fatalf("reload topic: %v", err)
	}
	if status != TopicStatusHidden {
		t.Fatalf("hidden topic status washed to %q", status)
	}
	if title != "Hidden requeue topic" {
		t.Fatalf("rejected requeue must roll back the edit, title=%q", title)
	}

	if _, err := store.UpdateComment(fixture.ctx, UpdateCommentRecord{
		CommentID: comment.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginSelf, Content: renderedFixtureContent(t, "hidden comment edit"),
		RequeuePending: true, ModerationTriggers: []string{"outbound_link"},
	}); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("requeue on hidden comment must be rejected, got err=%v", err)
	}
	var commentStatus string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status FROM comments WHERE id = $1
	`, comment.ID).Scan(&commentStatus); err != nil {
		t.Fatalf("reload comment: %v", err)
	}
	if commentStatus != CommentStatusHidden {
		t.Fatalf("hidden comment status washed to %q", commentStatus)
	}
}
