package attachmentscontroller

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 把「空 body 的 handler 缺省语义」与「库里真的没被改」串成一条链：
// 空 body → decodeReconcileRequest 返回 dry-run → service 只统计不写 → reference_count 不变。
func TestEmptyReconcileBodyNeverWrites(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	publicID := "reconcile-empty-body-" + uuid.NewString()
	var attachmentID int64
	// 幽灵附件：stored=1 但没有任何引用行；apply 会把它改成 0，dry-run 必须保持 1。
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name, content_type,
		  extension, size_bytes, sha256, visibility, status, reference_count
		) VALUES ($1, NULL, 'local', $2, 'ghost.bin', 'application/octet-stream', '.bin',
		  16, $3, 'public', 'active', 1)
		RETURNING id
	`, publicID, "reconcile/"+publicID+".bin", strings.Repeat("e", 64)).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment (run migrations first): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachments WHERE id = $1`, attachmentID)
	})

	// 真实 handler 解码路径：完全不发 body、也不带 Content-Type。
	var dryRun bool
	app := fiber.New()
	app.Post("/reconcile", func(c fiber.Ctx) error {
		decoded, err := decodeReconcileRequest(c)
		if err != nil {
			return err
		}
		dryRun = decoded
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest("POST", "/reconcile", nil))
	if err != nil {
		t.Fatalf("empty body request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("empty body status = %d, want 204", response.StatusCode)
	}
	if !dryRun {
		t.Fatal("empty body must default to a dry run")
	}

	service := attachments.NewService(attachments.NewPostgresStore(pool), nil)
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAttachmentManage: true,
	}}
	result, err := service.ReconcileReferenceCounts(ctx, actor, dryRun)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Applied {
		t.Fatalf("empty body reconcile must not apply, got %+v", result)
	}
	var stored int64
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, attachmentID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("dry run changed reference_count: stored = %d, want 1", stored)
	}
}
