package attachments

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 集成测试覆盖 reconcile.go 的三层语义：
// 1. dry-run 只读，不改任何 reference_count；
// 2. apply 后全表满足 reference_count == count(attachment_references)；
// 3. service 层权限不足返回 identity.ErrPermissionDenied（HTTP 之外的第二道门）。
func insertRecountAttachment(t *testing.T, pool *pgxpool.Pool, publicID string, storedCount int64) int64 {
	t.Helper()
	var attachmentID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name, content_type,
		  extension, size_bytes, sha256, visibility, status, reference_count
		) VALUES ($1, NULL, 'local', $2, 'reconcile.bin', 'application/octet-stream', '.bin',
		  16, $3, 'public', 'active', $4)
		RETURNING id
	`, publicID, "reconcile/"+publicID+".bin", strings.Repeat("r", 64), storedCount).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment (run migrations first): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachments WHERE id = $1`, attachmentID)
	})
	return attachmentID
}

func insertRecountReference(t *testing.T, pool *pgxpool.Pool, attachmentID int64, resourceType string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context)
		VALUES ($1, $2, 0, 'reconcile-it')
	`, attachmentID, resourceType)
	if err != nil {
		t.Fatalf("insert attachment reference: %v", err)
	}
}

// recountDriftCountSQL 统计全表漂移行数。刻意用外层 count(*) 包住 GROUP BY：
// 直接对 GROUP BY ... HAVING 做 Scan 时，0 条违规会返回 pgx.ErrNoRows，
// 让「查不到行」被误当成「没有违规」，空表时语义反转。
const recountDriftCountSQL = `
SELECT count(*) FROM (
  SELECT a.id
  FROM attachments a
  LEFT JOIN attachment_references r ON r.attachment_id = a.id
  GROUP BY a.id, a.reference_count
  HAVING a.reference_count <> count(r.id)
) drift
`

type recountQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func fullTableRecountInvariant(t *testing.T, q recountQueryer) {
	t.Helper()
	var drift int64
	if err := q.QueryRow(context.Background(), recountDriftCountSQL).Scan(&drift); err != nil {
		t.Fatalf("count reference count drift: %v", err)
	}
	if drift != 0 {
		t.Fatalf("found %d rows violating reference_count == count(attachment_references)", drift)
	}
}

// 空表（0 行）也必须判通过：计数形态返回 0，不会像旧写法那样把 ErrNoRows
// 当成「没有违规」。用事务内清空 + 回滚，不污染同批其它用例的夹具。
func TestFullTableRecountInvariantOnEmptyTable(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM attachments`); err != nil {
		t.Fatalf("clear attachments for empty-table case: %v", err)
	}
	var remaining int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("empty-table case needs zero rows, got %d", remaining)
	}
	fullTableRecountInvariant(t, tx)
}

func TestReconcileReferenceCountsPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// 幽灵附件：stored=1 但无引用行（下调）；反向漂移：stored=0 有一条引用行（上调）。
	ghostID := insertRecountAttachment(t, pool, "reconcile-it-"+uuid.NewString(), 1)
	understatedID := insertRecountAttachment(t, pool, "reconcile-it-"+uuid.NewString(), 0)
	// 干净行：计数与引用一致，应计入 Unchanged 且不被 UPDATE 触碰。
	cleanID := insertRecountAttachment(t, pool, "reconcile-it-"+uuid.NewString(), 2)
	insertRecountReference(t, pool, cleanID, "topic")
	insertRecountReference(t, pool, cleanID, "comment")
	insertRecountReference(t, pool, understatedID, "topic")

	store := NewPostgresStore(pool)

	var ghostStored int64
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, ghostID).Scan(&ghostStored); err != nil {
		t.Fatal(err)
	}
	if ghostStored != 1 {
		t.Fatalf("fixture drift broken: ghost stored=%d", ghostStored)
	}

	// dry-run：只统计，不改数据。
	dry, err := store.ReconcileReferenceCounts(ctx, true)
	if err != nil {
		t.Fatalf("dry-run reconcile: %v", err)
	}
	if dry.Applied {
		t.Fatalf("dry-run must report applied=false, got %+v", dry)
	}
	if dry.Lowered < 1 || dry.Raised < 1 {
		t.Fatalf("expected at least one lowered and one raised row, got %+v", dry)
	}
	if dry.Unchanged < 1 {
		t.Fatalf("expected clean rows to count as unchanged, got %+v", dry)
	}
	if len(dry.Samples) == 0 || len(dry.Samples) > 20 {
		t.Fatalf("expected 1..20 samples, got %v", dry.Samples)
	}
	foundGhost := false
	for _, id := range dry.Samples {
		if id == ghostID {
			foundGhost = true
		}
	}
	if !foundGhost {
		t.Fatalf("expected ghost attachment %d in samples, got %v", ghostID, dry.Samples)
	}
	var stillDrift int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM attachments
		WHERE id IN ($1, $2) AND reference_count <> (SELECT count(*) FROM attachment_references WHERE attachment_id = attachments.id)
	`, ghostID, understatedID).Scan(&stillDrift); err != nil {
		t.Fatal(err)
	}
	if stillDrift != 2 {
		t.Fatalf("dry-run must not change data, %d rows changed", 2-stillDrift)
	}

	// apply：修正漂移行并保持全表不变式。
	applied, err := store.ReconcileReferenceCounts(ctx, false)
	if err != nil {
		t.Fatalf("apply reconcile: %v", err)
	}
	if !applied.Applied {
		t.Fatalf("apply must report applied=true, got %+v", applied)
	}
	if applied.Lowered < 1 || applied.Raised < 1 {
		t.Fatalf("apply should have lowered and raised rows, got %+v", applied)
	}
	fullTableRecountInvariant(t, pool)

	var ghostAfter, understatedAfter int64
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, ghostID).Scan(&ghostAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, understatedID).Scan(&understatedAfter); err != nil {
		t.Fatal(err)
	}
	if ghostAfter != 0 || understatedAfter != 1 {
		t.Fatalf("expected ghost->0 and understated->1, got %d/%d", ghostAfter, understatedAfter)
	}

	// 幂等：再跑一次 apply 应该 0 漂移（隔离库中同批测试可能互相写附件，
	// 因此只要求本次三个夹具不受影响，不要求 lowered+raised==0）。
	again, err := store.ReconcileReferenceCounts(ctx, false)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	for _, id := range []int64{ghostID, understatedID, cleanID} {
		var stored, actual int64
		if err := pool.QueryRow(ctx, `
			SELECT a.reference_count, count(r.id)
			FROM attachments a LEFT JOIN attachment_references r ON r.attachment_id = a.id
			WHERE a.id = $1 GROUP BY a.reference_count
		`, id).Scan(&stored, &actual); err != nil {
			t.Fatal(err)
		}
		if stored != actual {
			t.Fatalf("fixture %d drifted again: stored=%d actual=%d (second run %+v)", id, stored, actual, again)
		}
	}
}

func TestReconcileReferenceCountsPermissionDenied(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	service := NewService(NewPostgresStore(pool), nil)
	// 无权限 actor：Permissions 为空。
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	for _, dryRun := range []bool{true, false} {
		_, err := service.ReconcileReferenceCounts(ctx, actor, dryRun)
		if !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("dryRun=%v: expected permission denied, got %v", dryRun, err)
		}
	}
}

func TestReconcileReferenceCountsUnsupportedStore(t *testing.T) {
	service := NewService(&fakeAttachmentStore{}, nil)
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAttachmentManage: true,
	}}

	_, err := service.ReconcileReferenceCounts(context.Background(), actor, true)
	if !errors.Is(err, ErrReferenceReconcileUnsupported) {
		t.Fatalf("expected unsupported store error, got %v", err)
	}
}

func TestCleanupResultSkippedUsesDeferredCount(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := NewPostgresStore(pool)
	farFuture := time.Now().AddDate(0, 0, 1)
	deferredID := insertRecountAttachment(t, pool, "cleanup-skip-"+uuid.NewString(), 0)
	// created_at 晚于 cutoff ⇒ 在宽限期内，应被计入 Skipped。
	if _, err := pool.Exec(ctx, `UPDATE attachments SET created_at = $1 WHERE id = $2`, farFuture, deferredID); err != nil {
		t.Fatal(err)
	}
	count, err := store.CountDeferredCleanupCandidates(ctx, time.Now())
	if err != nil {
		t.Fatalf("CountDeferredCleanupCandidates: %v", err)
	}
	if count < 1 {
		t.Fatalf("expected at least the deferred fixture to be counted, got %d", count)
	}
	var _ = deferredID
}
