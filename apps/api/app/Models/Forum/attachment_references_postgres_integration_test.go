package forum

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

// 这一组用例存在的理由：论坛多附件写入长期只有「假 pgx.Tx」覆盖，唯一约束永远
// 不会触发，于是「一帖只能一张图」的回归在 2026-07 之后一路绿灯。这里跑真实迁移链
// （而不是手写裸表 + 单版本 ApplyVersion），让 schema 级不变量真正参与测试。
func TestPostgresTopicKeepsMultipleAttachmentReferences(t *testing.T) {
	fixture := newAttachmentReferencePGFixture(t)
	store := NewPostgresStore(fixture.pool)

	userID := fixture.seedUser(t, "multi-attachment-author")
	categorySlug := fixture.seedCategory(t, "general")
	first := fixture.seedAttachment(t, userID)
	second := fixture.seedAttachment(t, userID)

	created, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug:  categorySlug,
		AuthorUserID:  userID,
		Title:         "两张图的帖子",
		Slug:          "topic-with-two-images",
		Status:        TopicStatusActive,
		Content:       fixture.renderedContent(`{"type":"doc","content":[]}`),
		AttachmentIDs: []int64{first, second},
	})
	if err != nil {
		t.Fatalf("create topic with two attachments: %v", err)
	}

	if got := fixture.referenceCount(t, "topic", created.ID); got != 2 {
		t.Fatalf("topic references=%d, want 2", got)
	}
	for _, id := range []int64{first, second} {
		if got := fixture.attachmentReferenceCount(t, id); got != 1 {
			t.Fatalf("attachment %d reference_count=%d, want 1", id, got)
		}
	}

	source, err := store.GetTopicEditSource(fixture.ctx, created.ID)
	if err != nil {
		t.Fatalf("read edit source: %v", err)
	}
	if len(source.AttachmentIDs) != 2 || source.AttachmentIDs[0] != first || source.AttachmentIDs[1] != second {
		t.Fatalf("edit-source attachment ids=%v, want [%d %d]", source.AttachmentIDs, first, second)
	}
}

func TestPostgresTopicUpdateAndCommentReplaceMultipleAttachments(t *testing.T) {
	fixture := newAttachmentReferencePGFixture(t)
	store := NewPostgresStore(fixture.pool)

	userID := fixture.seedUser(t, "attachment-editor")
	categorySlug := fixture.seedCategory(t, "general")
	first := fixture.seedAttachment(t, userID)
	second := fixture.seedAttachment(t, userID)
	third := fixture.seedAttachment(t, userID)

	created, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug:  categorySlug,
		AuthorUserID:  userID,
		Title:         "先两张后一张",
		Slug:          "topic-replacing-attachments",
		Status:        TopicStatusActive,
		Content:       fixture.renderedContent(`{"type":"doc","content":[]}`),
		AttachmentIDs: []int64{first, second},
	})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}

	// 编辑成一张图：引用应当只剩一条，被移除的附件计数归零。
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID:            created.ID,
		EditorUserID:       userID,
		AuthorUserID:       userID,
		ExpectedRevision:   1,
		Operation:          RevisionOperationEdit,
		Origin:             RevisionOriginSelf,
		Title:              "先两张后一张",
		CategorySlug:       categorySlug,
		ReplaceAttachments: true,
		AttachmentIDs:      []int64{first},
	}); err != nil {
		t.Fatalf("update topic to one attachment: %v", err)
	}
	if got := fixture.referenceCount(t, "topic", created.ID); got != 1 {
		t.Fatalf("topic references after edit=%d, want 1", got)
	}
	if got := fixture.attachmentReferenceCount(t, first); got != 1 {
		t.Fatalf("kept attachment reference_count=%d, want 1", got)
	}
	if got := fixture.attachmentReferenceCount(t, second); got != 0 {
		t.Fatalf("removed attachment reference_count=%d, want 0", got)
	}

	// 评论同样支持多图（与主题共用同一条写入路径）。
	if _, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:           created.ID,
		AuthorUserID:      userID,
		TopicAuthorUserID: userID,
		Status:            CommentStatusActive,
		Content:           fixture.renderedContent("评论里的两张图"),
		AttachmentIDs:     []int64{second, third},
	}); err != nil {
		t.Fatalf("create comment with two attachments: %v", err)
	}
	if got := fixture.referenceCount(t, "comment", 1); got != 2 {
		t.Fatalf("comment references=%d, want 2", got)
	}
	if got := fixture.attachmentReferenceCount(t, third); got != 1 {
		t.Fatalf("comment attachment reference_count=%d, want 1", got)
	}
}

func TestPostgresAttachmentUnavailableIsTyped(t *testing.T) {
	fixture := newAttachmentReferencePGFixture(t)
	store := NewPostgresStore(fixture.pool)

	owner := fixture.seedUser(t, "attachment-owner")
	other := fixture.seedUser(t, "attachment-other")
	categorySlug := fixture.seedCategory(t, "general")
	foreign := fixture.seedAttachment(t, other)
	missing := int64(99999999)

	_, err := store.CreateTopic(fixture.ctx, CreateTopicRecord{
		CategorySlug:  categorySlug,
		AuthorUserID:  owner,
		Title:         "引用了别人的图",
		Slug:          "topic-with-foreign-attachment",
		Status:        TopicStatusActive,
		Content:       fixture.renderedContent("正文"),
		AttachmentIDs: []int64{foreign, missing},
	})
	if !errors.Is(err, ErrAttachmentUnavailable) {
		t.Fatalf("expected ErrAttachmentUnavailable, got %v", err)
	}
	var unavailable *AttachmentUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected typed error, got %T", err)
	}
	if len(unavailable.AttachmentIDs) != 2 {
		t.Fatalf("unavailable ids=%v", unavailable.AttachmentIDs)
	}
}

// 唯一性边界本身也要断言：槽位型资源必须仍然是「一槽一行」。
func TestPostgresAttachmentReferenceUniquenessScope(t *testing.T) {
	fixture := newAttachmentReferencePGFixture(t)

	var slotIndex, legacyIndex int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  count(*) FILTER (WHERE indexname = 'attachment_references_slot_context_unique'),
		  count(*) FILTER (WHERE indexname = 'attachment_references_resource_context_unique')
		FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'attachment_references'
	`).Scan(&slotIndex, &legacyIndex); err != nil {
		t.Fatalf("inspect attachment_references indexes: %v", err)
	}
	if slotIndex != 1 {
		t.Fatalf("slot-scoped unique index missing (found=%d)", slotIndex)
	}
	if legacyIndex != 0 {
		t.Fatalf("global unique index still present; topics can only hold one attachment")
	}

	userID := fixture.seedUser(t, "slot-owner")
	first := fixture.seedAttachment(t, userID)
	second := fixture.seedAttachment(t, userID)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context, created_by_user_id)
		VALUES ($1, 'site', 0, 'logo', $3), ($2, 'site', 0, 'logo', $3)
	`, first, second, userID); err == nil {
		t.Fatal("slot resources must reject a second attachment in the same context")
	}
}

type attachmentReferencePGFixture struct {
	ctx   context.Context
	admin *pgxpool.Pool
	pool  *pgxpool.Pool
}

func newAttachmentReferencePGFixture(t *testing.T) *attachmentReferencePGFixture {
	t.Helper()

	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}

	ctx := context.Background()
	// 迁移链包含数据库级对象（sforum_core_v1 等稳定视图 schema），schema 隔离
	// 覆盖不了它们，还会把套件库的稳定视图重绑进测试 schema。因此每个夹具
	// 独占一个一次性数据库：链在全新 public 上从零执行，结束后整体 DROP。
	adminURL, err := swapDatabaseInURL(databaseURL, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("forum_att_ref_%d", time.Now().UnixNano())
	quotedDB := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quotedDB); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	dropDB := func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quotedDB+" WITH (FORCE)")
	}
	testURL, err := swapDatabaseInURL(databaseURL, dbName)
	if err != nil {
		dropDB()
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgx.ParseConfig(testURL)
	if err != nil {
		dropDB()
		admin.Close()
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if err := runAttachmentReferenceMigrations(ctx, db); err != nil {
		db.Close()
		dropDB()
		admin.Close()
		t.Fatalf("run migrations: %v", err)
	}
	if err := db.Close(); err != nil {
		dropDB()
		admin.Close()
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		dropDB()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &attachmentReferencePGFixture{ctx: ctx, admin: admin, pool: pool}
	t.Cleanup(func() {
		pool.Close()
		dropDB()
		admin.Close()
	})
	return fixture
}

// swapDatabaseInURL 把连接串的数据库名替换为指定库，用于管理连接（创建/删除夹具库）。
func swapDatabaseInURL(rawURL, database string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}

// runAttachmentReferenceMigrations 跑完整迁移链：唯一约束是 schema 级事实，
// 只应用单条版本或手写裸表都会把约束漏掉。
func runAttachmentReferenceMigrations(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

func (f *attachmentReferencePGFixture) seedUser(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $2, $3, $4, $1)
		RETURNING id
	`, username, username, username+"@example.test", username+"@example.test").Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	return id
}

func (f *attachmentReferencePGFixture) seedCategory(t *testing.T, slug string) string {
	t.Helper()
	// 迁移链自带种子分类（如 general）；已存在时直接复用，避免撞 categories_slug_key。
	var existing string
	err := f.pool.QueryRow(f.ctx, `SELECT slug FROM categories WHERE slug = $1`, slug).Scan(&existing)
	if err == nil {
		return slug
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("check seed category: %v", err)
	}
	var groupID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO category_groups (slug, name, position) VALUES ($1, $1, 0) RETURNING id
	`, slug+"-group").Scan(&groupID); err != nil {
		t.Fatalf("seed category group: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO categories (slug, name, group_id, visibility) VALUES ($1, $1, $2, 'public')
	`, slug, groupID); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return slug
}

func (f *attachmentReferencePGFixture) seedAttachment(t *testing.T, ownerID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO attachments (public_id, owner_user_id, provider, object_key, original_name, content_type, size_bytes, sha256, status, visibility)
		VALUES ($1, $2, 'local', $3, 'image.png', 'image/png', 128, $4, 'active', 'public')
		RETURNING id
	`, fmt.Sprintf("%032x", time.Now().UnixNano()+id), ownerID, fmt.Sprintf("k/%d", time.Now().UnixNano()), fmt.Sprintf("%064x", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	return id
}

func (f *attachmentReferencePGFixture) renderedContent(raw string) RenderedContent {
	return RenderedContent{
		RawContent:    raw,
		HTMLContent:   "<p>" + raw + "</p>",
		PlainText:     raw,
		SourceFormat:  SourceFormatMarkdown,
		EditorType:    "markdown",
		EditorVersion: "v1",
		RenderVersion: "v1",
		ContentHash:   fmt.Sprintf("%064x", len(raw)),
	}
}

func (f *attachmentReferencePGFixture) referenceCount(t *testing.T, resourceType string, resourceID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM attachment_references
		WHERE resource_type = $1 AND resource_id = $2 AND context = $3
	`, resourceType, resourceID, forumAttachmentContext).Scan(&count); err != nil {
		t.Fatalf("count references: %v", err)
	}
	return count
}

func (f *attachmentReferencePGFixture) attachmentReferenceCount(t *testing.T, attachmentID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT reference_count FROM attachments WHERE id = $1
	`, attachmentID).Scan(&count); err != nil {
		t.Fatalf("read attachment reference_count: %v", err)
	}
	return count
}
