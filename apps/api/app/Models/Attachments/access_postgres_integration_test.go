package attachments

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这条测试补上「谓词拆分」与「读守卫 SQL」之间的接缝：匿名可读判定依赖
// ReferenceAccess.ContentType，而 ContentType 由 ListReferenceAccess /
// LoadReadGuardSubject 的 SQL 提供。单元测试用的是假 store，覆盖不到这段 SQL。
func TestExtensionReferenceReadGuardFromPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	extensionID := "it.readguard" + suffix
	publicID := "it-ext-" + suffix + "-readguard"
	var ownerUserID, attachmentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1)
		RETURNING id
	`, "itrg"+suffix, "itrg"+suffix+"@example.test").Scan(&ownerUserID); err != nil {
		t.Fatalf("insert owner (run migrations first): %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name, content_type,
		  extension, size_bytes, sha256, visibility, status
		) VALUES ($1, $2, 'local', $3, 'icon.png', 'image/png', '.png', 1024, $4, 'public', 'active')
		RETURNING id
	`, publicID, ownerUserID, "integration/"+publicID+".png", strings.Repeat("c", 64)).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachments WHERE id = $1`, attachmentID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, ownerUserID)
	})

	// 走真实的认领路径，确保引用行、context 命名空间与计数都由生产代码写入。
	referenceStore := extensionreference.NewStore(pool)
	var revision time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM attachments WHERE id = $1`, attachmentID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := referenceStore.PrepareClaim(ctx, tx, extensionreference.ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: publicID,
		ActorUserID: ownerUserID, ExpectedRevision: revision.UTC(),
		Policy: extensionreference.Policy{MaxBytes: extensionreference.MaxImageBytes, ReferenceLimit: 10},
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := referenceStore.ApplyClaim(ctx, tx, plan); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	attachmentStore := NewPostgresStore(pool)
	references, err := attachmentStore.ListReferenceAccess(ctx, attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0].ContentType != "image/png" {
		t.Fatalf("references = %#v (content_type must be selected)", references)
	}

	subject, err := attachmentStore.LoadReadGuardSubject(ctx, publicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subject.References) != 1 || subject.References[0].ContentType != "image/png" {
		t.Fatalf("read guard subject = %#v", subject)
	}
	// 匿名访客必须能读到扩展公开图标，即使站点把游客读策略设为 login_required。
	if err := AuthorizeReadGuardSubject(identity.Actor{}, subject, true); err != nil {
		t.Fatalf("anonymous extension read denied: %v", err)
	}

	// 但 URL 装饰仍走代理：绝不把对象存储直链泄漏给管理列表与附件 API。
	service := NewService(attachmentStore, nil)
	if !service.shouldProxyAuthorizedURL(ctx, Attachment{ID: attachmentID, PublicID: publicID}) {
		t.Fatal("extension reference must keep the proxied media path")
	}
}
