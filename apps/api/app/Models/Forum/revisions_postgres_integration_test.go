package forum

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestRevisionLedgerCreateTopicWritesRevisionOnePostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "topic_author")
	content := renderedFixtureContent(t, "初始主题正文")

	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug:    "general",
		AuthorUserID:    authorID,
		Title:           "Revision One Topic",
		Slug:            "revision-one-topic",
		TagCreationMode: TagCreationModeControlled,
		Content:         content,
		Status:          TopicStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if topic.CurrentRevision != 1 {
		t.Fatalf("topic currentRevision=%d, want 1", topic.CurrentRevision)
	}
	if topic.ContentEdited {
		t.Fatal("new topic revision 1 must not be marked edited")
	}

	var revisionNo int64
	var operation, origin string
	var snapshotComplete bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT post_revisions.revision_no, post_revisions.operation, post_revisions.origin,
		  post_revisions.snapshot_complete
		FROM post_revisions
		WHERE post_id = $1
	`, topic.Content.ID).Scan(&revisionNo, &operation, &origin, &snapshotComplete); err != nil {
		t.Fatalf("load topic revision: %v", err)
	}
	if revisionNo != 1 || operation != RevisionOperationCreate || origin != RevisionOriginSelf || !snapshotComplete {
		t.Fatalf("revision row = no:%d op:%s origin:%s complete:%v", revisionNo, operation, origin, snapshotComplete)
	}

	var title, categorySlug string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT title, category_slug
		FROM topic_revision_snapshots
		JOIN post_revisions ON post_revisions.id = topic_revision_snapshots.post_revision_id
		WHERE post_revisions.post_id = $1
	`, topic.Content.ID).Scan(&title, &categorySlug); err != nil {
		t.Fatalf("load topic revision snapshot: %v", err)
	}
	if title != "Revision One Topic" || categorySlug != "general" {
		t.Fatalf("topic snapshot title=%q category=%q", title, categorySlug)
	}
}

func TestRevisionLedgerCreateCommentWritesRevisionOnePostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "comment_author")
	topic := fixture.insertBareTopic(t, authorID, "comment-host")

	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "评论正文"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if comment.CurrentRevision != 1 {
		t.Fatalf("comment currentRevision=%d, want 1", comment.CurrentRevision)
	}
	if comment.ContentEdited {
		t.Fatal("new comment revision 1 must not be marked edited")
	}

	var revisionNo int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT revision_no FROM post_revisions WHERE post_id = $1
	`, comment.Content.ID).Scan(&revisionNo); err != nil {
		t.Fatalf("load comment revision: %v", err)
	}
	if revisionNo != 1 {
		t.Fatalf("comment revision_no=%d, want 1", revisionNo)
	}
}

func TestRevisionLedgerVersionedTopicEditCASNoopAndAcceptedSnapshotPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "versioned_topic_author")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug: "general", AuthorUserID: authorID, Title: "Original title", Slug: "original-title",
		TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "original body"), Status: TopicStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}

	updated, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginSelf, Title: "Accepted title", Slug: "accepted-title", TagCreationMode: TagCreationModeControlled,
	})
	if err != nil {
		t.Fatalf("UpdateTopic: %v", err)
	}
	if !updated.UpdateApplied || updated.CurrentRevision != 2 {
		t.Fatalf("updated topic = %#v", updated)
	}
	var revisionNo int64
	var title, raw string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT post_revisions.revision_no, topic_revision_snapshots.title, post_revisions.raw_content
		FROM post_revisions JOIN topic_revision_snapshots ON topic_revision_snapshots.post_revision_id = post_revisions.id
		WHERE post_revisions.post_id = $1 AND post_revisions.revision_no = 2
	`, topic.Content.ID).Scan(&revisionNo, &title, &raw); err != nil {
		t.Fatalf("load accepted revision: %v", err)
	}
	if revisionNo != 2 || title != "Accepted title" || raw != "original body" {
		t.Fatalf("accepted revision = no:%d title:%q raw:%q", revisionNo, title, raw)
	}

	noOp, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2,
		Origin: RevisionOriginSelf, Title: "Accepted title", Slug: "accepted-title", TagCreationMode: TagCreationModeControlled,
	})
	if err != nil {
		t.Fatalf("no-op UpdateTopic: %v", err)
	}
	if noOp.UpdateApplied || noOp.CurrentRevision != 2 {
		t.Fatalf("no-op result = %#v", noOp)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale UpdateTopic error = %v, want ErrRevisionConflict", err)
	}
}

func TestRevisionLedgerVersionedCommentEditSavesFinalAcceptedSnapshotPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "versioned_comment_author")
	topic := fixture.insertBareTopic(t, authorID, "versioned-comment-host")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{TopicID: topic.id, AuthorUserID: authorID, Content: renderedFixtureContent(t, "original comment"), Status: CommentStatusActive})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	summary, err := store.GetCommentSummary(fixture.ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetCommentSummary: %v", err)
	}
	if summary.ID != comment.ID || summary.CurrentRevision != 1 {
		t.Fatalf("comment summary = %#v", summary)
	}
	accepted := renderedFixtureContent(t, "accepted comment")
	updated, err := store.UpdateComment(fixture.ctx, UpdateCommentRecord{CommentID: comment.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, Content: accepted})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if !updated.UpdateApplied || updated.CurrentRevision != 2 {
		t.Fatalf("updated comment = %#v", updated)
	}
	var raw string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT raw_content FROM post_revisions WHERE post_id = $1 AND revision_no = 2`, comment.Content.ID).Scan(&raw); err != nil {
		t.Fatalf("load accepted comment revision: %v", err)
	}
	if raw != "accepted comment" {
		t.Fatalf("accepted comment raw=%q", raw)
	}
}

func TestRevisionLedgerRestoreAppendsImmutableVersionPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "restore_author")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "original title", Slug: "original-title", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "original body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, Title: "edited title", Slug: "edited-title", TagCreationMode: TagCreationModeControlled}); err != nil {
		t.Fatalf("UpdateTopic: %v", err)
	}
	var sourceID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM post_revisions WHERE post_id = $1 AND revision_no = 1`, topic.Content.ID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2, Reason: "restore reviewed version", Origin: RevisionOriginSelf, Operation: RevisionOperationRestore, RestoredFromRevisionID: sourceID, RestoredFromRevisionNo: 1, HistoricalAttachmentOwnerID: authorID, Title: "original title", Slug: "original-title", CategorySlug: "general", TagSlugs: []string{}, TagCreationMode: TagCreationModeControlled, HasContent: true, Content: renderedFixtureContent(t, "original body"), ReplaceAttachments: true})
	if err != nil {
		t.Fatalf("restore UpdateTopic: %v", err)
	}
	if !updated.UpdateApplied || updated.CurrentRevision != 3 || updated.Title != "original title" {
		t.Fatalf("restore result %#v", updated)
	}
	var op string
	var restoredFrom int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT operation, restored_from_revision_id FROM post_revisions WHERE post_id = $1 AND revision_no = 3`, topic.Content.ID).Scan(&op, &restoredFrom); err != nil {
		t.Fatal(err)
	}
	if op != RevisionOperationRestore || restoredFrom != sourceID {
		t.Fatalf("restore ledger op=%q from=%d", op, restoredFrom)
	}
	var originalRaw string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT raw_content FROM post_revisions WHERE id = $1`, sourceID).Scan(&originalRaw); err != nil || originalRaw != "original body" {
		t.Fatalf("restore must not rewrite source: raw=%q err=%v", originalRaw, err)
	}
}

func TestRevisionLedgerRestoreUnavailableAttachmentFailsAtomicallyPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "attachment_restore_author")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "attachment restore", Slug: "attachment-restore", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	var attachmentID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `INSERT INTO attachments (owner_user_id, status, visibility) VALUES ($1, 'active', 'public') RETURNING id`, authorID).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	content := renderedFixtureContent(t, "version with attachment")
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, TagCreationMode: TagCreationModeControlled, HasContent: true, Content: content, ReplaceAttachments: true, AttachmentIDs: []int64{attachmentID}}); err != nil {
		t.Fatal(err)
	}
	var sourceID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM post_revisions WHERE post_id = $1 AND revision_no = 2`, topic.Content.ID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE attachments SET status = 'deleted' WHERE id = $1`, attachmentID); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2, Reason: "restore attachment", Origin: RevisionOriginSelf, Operation: RevisionOperationRestore, RestoredFromRevisionID: sourceID, HistoricalAttachmentOwnerID: authorID, TagCreationMode: TagCreationModeControlled, HasContent: true, Content: content, ReplaceAttachments: true, AttachmentIDs: []int64{attachmentID}})
	if !errors.Is(err, ErrRevisionAttachmentUnavailable) {
		t.Fatalf("restore attachment error=%v", err)
	}
	var currentRevision, revisions int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT current_revision FROM posts WHERE id = $1`, topic.Content.ID).Scan(&currentRevision); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT COUNT(*) FROM post_revisions WHERE post_id = $1`, topic.Content.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if currentRevision != 2 || revisions != 2 {
		t.Fatalf("failed restore changed ledger current=%d rows=%d", currentRevision, revisions)
	}
}

func TestRevisionLedgerRestoreUnavailableCategoryAndTagFailAtomicallyPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "taxonomy_restore_author")
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO categories (slug, name, visibility) VALUES ('archive', 'Archive', 'public'); INSERT INTO tags (slug, name, status) VALUES ('history', 'History', 'active')`); err != nil {
		t.Fatal(err)
	}
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "taxonomy restore", Slug: "taxonomy-restore", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, CategorySlug: "archive", TagSlugs: []string{"history"}, TagCreationMode: TagCreationModeControlled}); err != nil {
		t.Fatal(err)
	}
	var sourceID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM post_revisions WHERE post_id = $1 AND revision_no = 2`, topic.Content.ID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE categories SET visibility = 'hidden' WHERE slug = 'archive'`); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2, Reason: "restore category", Origin: RevisionOriginSelf, Operation: RevisionOperationRestore, RestoredFromRevisionID: sourceID, HistoricalAttachmentOwnerID: authorID, CategorySlug: "archive", TagSlugs: []string{"history"}, TagCreationMode: TagCreationModeControlled, HasContent: true, Content: renderedFixtureContent(t, "body")})
	if !errors.Is(err, ErrRevisionCategoryUnavailable) {
		t.Fatalf("restore unavailable category error=%v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE categories SET visibility = 'public' WHERE slug = 'archive'; UPDATE tags SET status = 'disabled' WHERE slug = 'history'`); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 2, Reason: "restore tag", Origin: RevisionOriginSelf, Operation: RevisionOperationRestore, RestoredFromRevisionID: sourceID, HistoricalAttachmentOwnerID: authorID, CategorySlug: "archive", TagSlugs: []string{"history"}, TagCreationMode: TagCreationModeControlled, HasContent: true, Content: renderedFixtureContent(t, "body")})
	if !errors.Is(err, ErrRevisionTagUnavailable) {
		t.Fatalf("restore unavailable tag error=%v", err)
	}
	var currentRevision, revisions int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT current_revision FROM posts WHERE id = $1`, topic.Content.ID).Scan(&currentRevision); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT COUNT(*) FROM post_revisions WHERE post_id = $1`, topic.Content.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if currentRevision != 2 || revisions != 2 {
		t.Fatalf("taxonomy restore changed ledger current=%d rows=%d", currentRevision, revisions)
	}
}

func TestRevisionLedgerSuperAdminRedactsOldRevisionAtomicallyPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `CREATE TABLE audit_events (id BIGSERIAL PRIMARY KEY, actor_user_id BIGINT NULL, target_user_id BIGINT NULL, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(fixture.pool).WithAuditor(audit.NewPostgresWriter(fixture.pool))
	authorID := fixture.insertUser(t, "redaction_author")
	adminID := fixture.insertUser(t, "redaction_admin")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "redaction target", Slug: "redaction-target", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "private body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, Title: "changed", Slug: "changed", TagCreationMode: TagCreationModeControlled}); err != nil {
		t.Fatal(err)
	}
	if err := store.RedactTopicRevision(fixture.ctx, RevisionRedactionRecord{TargetID: topic.ID, RevisionNo: 1, ExpectedRevision: 2, ActorUserID: adminID, Reason: "privacy request"}); err != nil {
		t.Fatalf("redact old revision: %v", err)
	}
	var raw, title, action string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT raw_content FROM post_revisions WHERE post_id = $1 AND revision_no = 1`, topic.Content.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT title FROM topic_revision_snapshots JOIN post_revisions ON post_revisions.id = topic_revision_snapshots.post_revision_id WHERE post_revisions.post_id = $1 AND post_revisions.revision_no = 1`, topic.Content.ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT action FROM audit_events`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if raw != "" || title != "" || action != audit.ActionForumTopicRevisionRedact {
		t.Fatalf("redaction payload/audit raw=%q title=%q action=%q", raw, title, action)
	}
	if _, err := store.GetTopicRevision(fixture.ctx, topic.ID, 1); !errors.Is(err, ErrRevisionRedacted) {
		t.Fatalf("redacted revision readable: %v", err)
	}
	if err := store.RedactTopicRevision(fixture.ctx, RevisionRedactionRecord{TargetID: topic.ID, RevisionNo: 2, ExpectedRevision: 2, ActorUserID: adminID, Reason: "bad"}); !errors.Is(err, ErrRevisionRedactionForbidden) {
		t.Fatalf("current redaction error=%v", err)
	}
}

func TestRevisionLedgerAuditCleanupDoesNotBreakHistoryPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `CREATE TABLE audit_events (id BIGSERIAL PRIMARY KEY, actor_user_id BIGINT NULL, target_user_id BIGINT NULL, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(fixture.pool).WithAuditor(audit.NewPostgresWriter(fixture.pool))
	authorID := fixture.insertUser(t, "audit_cleanup_author")
	staffID := fixture.insertUser(t, "audit_cleanup_staff")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug: "general", AuthorUserID: authorID, Title: "audit cleanup target", Slug: "audit-cleanup-target",
		TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "original body"), Status: TopicStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: staffID, AuthorUserID: authorID, ExpectedRevision: 1,
		Origin: RevisionOriginStaff, Reason: "moderation correction", Title: "corrected title", Slug: "corrected-title",
		TagCreationMode: TagCreationModeControlled,
	}); err != nil {
		t.Fatalf("UpdateTopic: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE audit_events SET created_at = now() - interval '91 days'`); err != nil {
		t.Fatal(err)
	}
	result, err := audit.NewPostgresWriter(fixture.pool).CleanupOlderThan(fixture.ctx, audit.RecommendedRetentionDays)
	if err != nil {
		t.Fatalf("CleanupOlderThan: %v", err)
	}
	if result.Deleted != 1 {
		t.Fatalf("cleanup result=%#v, want one deleted audit event", result)
	}
	var revisions int64
	var currentRaw string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT COUNT(*), max(raw_content) FILTER (WHERE revision_no = 2)
		FROM post_revisions
		WHERE post_id = $1
	`, topic.Content.ID).Scan(&revisions, &currentRaw); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 || currentRaw != "original body" {
		t.Fatalf("audit cleanup altered revision ledger rows=%d currentRaw=%q", revisions, currentRaw)
	}
}

func TestRevisionLedgerHardDeleteCascadesRevisionPayloadsPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "hard_delete_revision_author")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "hard delete", Slug: "hard-delete", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1, Origin: RevisionOriginSelf, Title: "hard delete revised", Slug: "hard-delete-revised", TagCreationMode: TagCreationModeControlled}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM topics WHERE id = $1`, topic.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM posts WHERE id = $1`, topic.Content.ID); err != nil {
		t.Fatal(err)
	}
	var revisions, snapshots int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT COUNT(*) FROM post_revisions WHERE post_id = $1`, topic.Content.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT COUNT(*) FROM topic_revision_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || snapshots != 0 {
		t.Fatalf("hard delete retained revisions=%d snapshots=%d", revisions, snapshots)
	}
}

func TestRevisionLedgerStaffEditAppendsAuditInContentTransactionPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `CREATE TABLE audit_events (id BIGSERIAL PRIMARY KEY, actor_user_id BIGINT NULL, target_user_id BIGINT NULL, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create audit_events: %v", err)
	}
	store := NewPostgresStore(fixture.pool).WithAuditor(audit.NewPostgresWriter(fixture.pool))
	authorID := fixture.insertUser(t, "staff_audit_author")
	staffID := fixture.insertUser(t, "staff_audit_editor")
	topic, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{CategorySlug: "general", AuthorUserID: authorID, Title: "Audit target", Slug: "audit-target", TagCreationMode: TagCreationModeControlled, Content: renderedFixtureContent(t, "audit body"), Status: TopicStatusActive})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{TopicID: topic.ID, EditorUserID: staffID, AuthorUserID: authorID, ExpectedRevision: 1, Reason: "correct title", Origin: RevisionOriginStaff, Title: "Corrected title", Slug: "corrected-title", TagCreationMode: TagCreationModeControlled}); err != nil {
		t.Fatalf("staff UpdateTopic: %v", err)
	}
	var action, rawMetadata string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT action, metadata::text FROM audit_events`).Scan(&action, &rawMetadata); err != nil {
		t.Fatalf("load edit audit: %v", err)
	}
	if action != audit.ActionForumTopicEditAny || !strings.Contains(rawMetadata, `"revisionNo": 2`) || strings.Contains(rawMetadata, "correct title") {
		t.Fatalf("unexpected audit action/metadata: %q %s", action, rawMetadata)
	}
}

func TestRevisionLedgerBackfillIsBatchedResumableAndIdempotentPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "backfill_author")
	editorID := fixture.insertUser(t, "backfill_editor")
	first := fixture.insertLegacyTopicWithRevisions(t, authorID, editorID, "legacy-one", []string{"旧正文 A", "旧正文 B"}, "当前正文 C")
	second := fixture.insertLegacyTopicWithRevisions(t, authorID, editorID, "legacy-two", nil, "未编辑当前正文")

	result, err := store.BackfillContentRevisions(fixture.ctx, RevisionBackfillOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if result.Claimed != 1 || result.Completed != 1 || result.Pending != 1 {
		t.Fatalf("first result=%+v, want claimed/completed 1 pending 1", result)
	}
	assertLegacyTopicBackfilled(t, fixture.pool, fixture.ctx, first.postID, 3)

	result, err = store.BackfillContentRevisions(fixture.ctx, RevisionBackfillOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if result.Claimed != 1 || result.Completed != 1 || result.Pending != 0 {
		t.Fatalf("second result=%+v, want claimed/completed 1 pending 0", result)
	}
	assertLegacyTopicBackfilled(t, fixture.pool, fixture.ctx, second.postID, 1)

	result, err = store.BackfillContentRevisions(fixture.ctx, RevisionBackfillOptions{BatchSize: 10})
	if err != nil {
		t.Fatalf("third backfill: %v", err)
	}
	if result.Claimed != 0 || result.Completed != 0 || result.Pending != 0 {
		t.Fatalf("idempotent result=%+v, want all zero", result)
	}

	var revisionCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT COUNT(*) FROM post_revisions WHERE post_id IN ($1, $2)
	`, first.postID, second.postID).Scan(&revisionCount); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisionCount != 4 {
		t.Fatalf("revision count after rerun=%d, want 4", revisionCount)
	}
}

func TestRevisionReadModelsListDetailLegacyAndRedactedPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "revision_reader_author")
	editorID := fixture.insertUser(t, "revision_reader_editor")
	topic := fixture.insertLegacyTopicWithRevisions(t, authorID, editorID, "read-legacy", []string{"旧正文"}, "当前正文")
	if _, err := store.BackfillContentRevisions(fixture.ctx, RevisionBackfillOptions{BatchSize: 10}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	list, err := store.ListTopicRevisions(fixture.ctx, topic.id, RevisionListInput{PerPage: 200})
	if err != nil {
		t.Fatalf("ListTopicRevisions: %v", err)
	}
	if list.PerPage != revisionMaxPerPage || len(list.Items) != 2 {
		t.Fatalf("unexpected topic revision list %#v", list)
	}
	if list.Items[0].RevisionNo != 2 || !list.Items[0].Current {
		t.Fatalf("newest revision should be current, got %#v", list.Items[0])
	}
	if list.Items[1].SnapshotComplete || !slices.Equal(list.Items[1].RestorableFields, []string{"content"}) {
		t.Fatalf("legacy summary should be content-only incomplete, got %#v", list.Items[1])
	}

	detail, err := store.GetTopicRevision(fixture.ctx, topic.id, 1)
	if err != nil {
		t.Fatalf("GetTopicRevision legacy detail: %v", err)
	}
	if detail.RawContent != "旧正文" || detail.TopicMetadata != nil || !slices.Equal(detail.RestorableFields, []string{"content"}) {
		t.Fatalf("unexpected legacy detail %#v", detail)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE post_revisions
		SET redacted_at = now(), redacted_by_user_id = $2, redaction_reason = 'privacy cleanup'
		WHERE post_id = $1 AND revision_no = 1
	`, topic.postID, editorID); err != nil {
		t.Fatalf("redact fixture revision: %v", err)
	}
	list, err = store.ListTopicRevisions(fixture.ctx, topic.id, RevisionListInput{})
	if err != nil {
		t.Fatalf("ListTopicRevisions redacted: %v", err)
	}
	if !list.Items[1].Redacted || len(list.Items[1].RestorableFields) != 0 {
		t.Fatalf("redacted list header should be payload-free tombstone, got %#v", list.Items[1])
	}
	if _, err := store.GetTopicRevision(fixture.ctx, topic.id, 1); !errors.Is(err, ErrRevisionRedacted) {
		t.Fatalf("redacted detail should fail closed, got %v", err)
	}
	if _, err := store.GetTopicRevision(fixture.ctx, topic.id, 99); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("missing detail should return ErrRevisionNotFound, got %v", err)
	}
}

func TestRevisionReadModelsCommentAndAdminContentPostgres(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "admin_content_author")
	topic := fixture.insertBareTopic(t, authorID, "admin-content-host")

	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "后台评论正文"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	revisions, err := store.ListCommentRevisions(fixture.ctx, comment.ID, RevisionListInput{})
	if err != nil {
		t.Fatalf("ListCommentRevisions: %v", err)
	}
	if len(revisions.Items) != 1 || revisions.Items[0].RevisionNo != 1 || !revisions.Items[0].Current {
		t.Fatalf("unexpected comment revisions %#v", revisions)
	}
	detail, err := store.GetCommentRevision(fixture.ctx, comment.ID, 1)
	if err != nil {
		t.Fatalf("GetCommentRevision: %v", err)
	}
	if detail.RawContent != "后台评论正文" || detail.Attachments.Total != 0 {
		t.Fatalf("unexpected comment revision detail %#v", detail)
	}

	topicRows, err := store.ListAdminForumTopics(fixture.ctx, AdminForumContentListInput{TitlePrefix: "Host admin"})
	if err != nil {
		t.Fatalf("ListAdminForumTopics: %v", err)
	}
	if len(topicRows.Items) != 1 || topicRows.Items[0].TargetType != "topic" || topicRows.Items[0].CurrentRevision < 1 {
		t.Fatalf("unexpected admin topic rows %#v", topicRows)
	}
	topicDetail, err := store.GetAdminForumTopic(fixture.ctx, topic.id)
	if err != nil {
		t.Fatalf("GetAdminForumTopic: %v", err)
	}
	if topicDetail.Content.RawContent == "" || topicDetail.Slug != "admin-content-host" {
		t.Fatalf("unexpected admin topic detail %#v", topicDetail)
	}

	commentRows, err := store.ListAdminForumComments(fixture.ctx, AdminForumContentListInput{TopicID: topic.id})
	if err != nil {
		t.Fatalf("ListAdminForumComments: %v", err)
	}
	if len(commentRows.Items) != 1 || commentRows.Items[0].TargetType != "comment" || commentRows.Items[0].TopicID != topic.id {
		t.Fatalf("unexpected admin comment rows %#v", commentRows)
	}
	commentDetail, err := store.GetAdminForumComment(fixture.ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetAdminForumComment: %v", err)
	}
	if commentDetail.Content.RawContent != "后台评论正文" || commentDetail.TopicTitle == "" {
		t.Fatalf("unexpected admin comment detail %#v", commentDetail)
	}
}

func TestRevisionBackfillSourceUsesSkipLocked(t *testing.T) {
	body, err := os.ReadFile("revisions.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "FOR UPDATE SKIP LOCKED") {
		t.Fatal("backfill claim query must use FOR UPDATE SKIP LOCKED")
	}
}

type revisionLedgerPGFixture struct {
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	schema string
}

type bareTopicFixture struct {
	id     int64
	postID int64
}

func newRevisionLedgerPGFixture(t *testing.T) *revisionLedgerPGFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("forum_revision_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if err := runRevisionLedgerFixtureMigrations(ctx, db); err != nil {
		db.Close()
		cleanup()
		t.Fatalf("run fixture migrations: %v", err)
	}
	if err := db.Close(); err != nil {
		cleanup()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	fixture := &revisionLedgerPGFixture{ctx: ctx, admin: admin, pool: pool, schema: schema}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return fixture
}

func runRevisionLedgerFixtureMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, revisionLedgerFixtureBaseSchemaSQL); err != nil {
		return err
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	_, err = provider.ApplyVersion(ctx, 202607220052, true)
	return err
}

const revisionLedgerFixtureBaseSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  email_lower TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE attachments (
  id BIGSERIAL PRIMARY KEY,
  public_id TEXT NOT NULL DEFAULT '',
  owner_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  content_type TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  visibility TEXT NOT NULL DEFAULT 'public',
  reference_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_profiles (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  avatar_attachment_id BIGINT REFERENCES attachments(id) ON DELETE SET NULL
);

CREATE TABLE categories (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'public',
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE posts (
  id BIGSERIAL PRIMARY KEY,
  raw_content TEXT NOT NULL,
  html_content TEXT NOT NULL,
  plain_text TEXT NOT NULL,
  source_format TEXT NOT NULL DEFAULT 'markdown',
  editor_type TEXT NOT NULL DEFAULT 'markdown',
  editor_version TEXT NOT NULL DEFAULT '',
  render_version TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
  author_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  content_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE RESTRICT,
  title TEXT NOT NULL,
  slug TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  comment_count BIGINT NOT NULL DEFAULT 0,
  view_count BIGINT NOT NULL DEFAULT 0,
  hot_score BIGINT NOT NULL DEFAULT 0,
  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  moderation_triggers JSONB NOT NULL DEFAULT '[]'::jsonb,
  ip_address TEXT NOT NULL DEFAULT '',
  last_edit_ip TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  content_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE RESTRICT,
  author_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  parent_comment_id BIGINT REFERENCES comments(id) ON DELETE SET NULL,
  root_comment_id BIGINT REFERENCES comments(id) ON DELETE SET NULL,
  path_key TEXT NOT NULL DEFAULT '',
  depth INTEGER NOT NULL DEFAULT 0,
  reply_count BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  moderation_triggers JSONB NOT NULL DEFAULT '[]'::jsonb,
  ip_address TEXT NOT NULL DEFAULT '',
  last_edit_ip TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE post_revisions (
  id BIGSERIAL PRIMARY KEY,
  post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  edited_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  raw_content TEXT NOT NULL,
  source_format TEXT NOT NULL,
  editor_type TEXT NOT NULL,
  editor_version TEXT NOT NULL DEFAULT '',
  render_version TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tags (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  topic_count BIGINT NOT NULL DEFAULT 0,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE topic_tags (
  topic_id BIGINT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (topic_id, tag_id)
);

CREATE TABLE attachment_references (
  attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  resource_type TEXT NOT NULL,
  resource_id BIGINT NOT NULL,
  context TEXT NOT NULL,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

INSERT INTO categories (slug, name, visibility)
VALUES ('general', '综合讨论', 'public');
`

func (f *revisionLedgerPGFixture) insertUser(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	email := username + "@example.test"
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, lower($1), $2, lower($2), $1)
		RETURNING id
	`, username, email).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func (f *revisionLedgerPGFixture) insertBareTopic(t *testing.T, authorID int64, slug string) bareTopicFixture {
	t.Helper()
	content := renderedFixtureContent(t, "host topic body")
	var postID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO posts (
		  raw_content, html_content, plain_text, source_format, editor_type,
		  editor_version, render_version, content_hash, created_by_user_id, updated_by_user_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING id
	`, content.RawContent, content.HTMLContent, content.PlainText, content.SourceFormat,
		content.EditorType, content.EditorVersion, content.RenderVersion, content.ContentHash,
		authorID).Scan(&postID); err != nil {
		t.Fatalf("insert bare post: %v", err)
	}
	categoryID := f.generalCategoryID(t)
	var topicID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug, status)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING id
	`, categoryID, authorID, postID, "Host "+slug, slug).Scan(&topicID); err != nil {
		t.Fatalf("insert bare topic: %v", err)
	}
	return bareTopicFixture{id: topicID, postID: postID}
}

func (f *revisionLedgerPGFixture) insertLegacyTopicWithRevisions(t *testing.T, authorID, editorID int64, slug string, legacyBodies []string, currentBody string) bareTopicFixture {
	t.Helper()
	topic := f.insertBareTopic(t, authorID, slug)
	current := renderedFixtureContent(t, currentBody)
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE posts
		SET raw_content = $2,
		    html_content = $3,
		    plain_text = $4,
		    source_format = $5,
		    editor_type = $6,
		    editor_version = $7,
		    render_version = $8,
		    content_hash = $9,
		    updated_by_user_id = $10,
		    updated_at = now()
		WHERE id = $1
	`, topic.postID, current.RawContent, current.HTMLContent, current.PlainText, current.SourceFormat,
		current.EditorType, current.EditorVersion, current.RenderVersion, current.ContentHash, editorID); err != nil {
		t.Fatalf("update legacy current post: %v", err)
	}
	baseTime := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	for i, body := range legacyBodies {
		content := renderedFixtureContent(t, body)
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO post_revisions (
			  post_id, superseded_by_user_id, raw_content, source_format, editor_type,
			  editor_version, render_version, content_hash, created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, topic.postID, editorID, content.RawContent, content.SourceFormat, content.EditorType,
			content.EditorVersion, content.RenderVersion, content.ContentHash,
			baseTime.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("insert legacy revision: %v", err)
		}
	}
	return topic
}

func (f *revisionLedgerPGFixture) generalCategoryID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		SELECT id FROM categories WHERE slug = 'general'
	`).Scan(&id); err != nil {
		t.Fatalf("load general category: %v", err)
	}
	return id
}

func renderedFixtureContent(t *testing.T, raw string) RenderedContent {
	t.Helper()
	content, err := RenderContent(ContentInput{
		RawContent:    raw,
		SourceFormat:  SourceFormatMarkdown,
		EditorType:    EditorTypeMarkdown,
		EditorVersion: "test",
	})
	if err != nil {
		t.Fatalf("render fixture content: %v", err)
	}
	return content
}

func assertLegacyTopicBackfilled(t *testing.T, pool *pgxpool.Pool, ctx context.Context, postID, wantCurrent int64) {
	t.Helper()
	var currentRevision int64
	if err := pool.QueryRow(ctx, `
		SELECT current_revision FROM posts WHERE id = $1
	`, postID).Scan(&currentRevision); err != nil {
		t.Fatalf("load post current revision: %v", err)
	}
	if currentRevision != wantCurrent {
		t.Fatalf("post current_revision=%d, want %d", currentRevision, wantCurrent)
	}
	rows, err := pool.Query(ctx, `
		SELECT revision_no, operation, origin, snapshot_complete
		FROM post_revisions
		WHERE post_id = $1
		ORDER BY revision_no ASC
	`, postID)
	if err != nil {
		t.Fatalf("load revision order: %v", err)
	}
	defer rows.Close()
	seen := []int64{}
	for rows.Next() {
		var revisionNo int64
		var operation, origin string
		var complete bool
		if err := rows.Scan(&revisionNo, &operation, &origin, &complete); err != nil {
			t.Fatalf("scan revision order: %v", err)
		}
		seen = append(seen, revisionNo)
		if operation != RevisionOperationMigration || origin != RevisionOriginMigration {
			t.Fatalf("revision %d operation/origin=%s/%s", revisionNo, operation, origin)
		}
		if revisionNo == wantCurrent && !complete {
			t.Fatalf("current revision %d must be complete", revisionNo)
		}
		if revisionNo < wantCurrent && complete {
			t.Fatalf("legacy revision %d must remain incomplete", revisionNo)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate revision order: %v", err)
	}
	if len(seen) != int(wantCurrent) {
		t.Fatalf("revision rows=%v, want 1..%d", seen, wantCurrent)
	}
	for i, revisionNo := range seen {
		if revisionNo != int64(i+1) {
			t.Fatalf("revision order=%v, want contiguous", seen)
		}
	}
}
