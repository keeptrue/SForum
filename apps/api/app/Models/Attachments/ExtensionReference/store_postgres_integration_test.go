package extensionreference

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 集成测试跑在真实 PostgreSQL 上：claim/release 的正确性依赖 advisory 锁、
// 唯一索引与「按引用行重算计数」，用假 store 无法覆盖。
func newIntegrationFixture(t *testing.T) (*Store, *pgxpool.Pool, string, int64) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	// 扩展 id 用测试后缀隔离，避免并行测试互相看到对方的引用。
	extensionID := "it.attachext" + suffix
	var ownerUserID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1)
		RETURNING id
	`, "itatt"+suffix, "itatt"+suffix+"@example.test").Scan(&ownerUserID); err != nil {
		t.Fatalf("insert owner (run migrations first): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachment_references WHERE context LIKE $1`, extensionID+":%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM attachments WHERE public_id LIKE $1`, "it-ext-"+suffix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, ownerUserID)
	})
	return NewStore(pool), pool, extensionID, ownerUserID
}

func insertIntegrationAttachment(
	t *testing.T,
	pool *pgxpool.Pool,
	publicID string,
	ownerUserID int64,
	contentType string,
	sizeBytes int64,
	visibility string,
) int64 {
	t.Helper()
	var attachmentID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name, content_type,
		  extension, size_bytes, sha256, image_width, image_height, visibility, status
		) VALUES ($1, $2, 'local', $3, 'icon.png', $4, '.png', $5, $6, 64, 64, $7, 'active')
		RETURNING id
	`, publicID, ownerUserID, "integration/"+publicID+".png", contentType, sizeBytes,
		strings.Repeat("b", 64), visibility).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	return attachmentID
}

func attachmentRevision(t *testing.T, pool *pgxpool.Pool, publicID string) time.Time {
	t.Helper()
	var revision time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at FROM attachments WHERE public_id = $1`, publicID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision.UTC()
}

func TestIntegrationClaimPublishesAndCounts(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	publicID := "it-ext-" + suffix + "-a"
	attachmentID := insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "private")

	claim, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "oauth2-client:sfo_1", PublicID: publicID,
		ActorUserID: ownerUserID, ExpectedRevision: attachmentRevision(t, pool, publicID),
		Policy: Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.ReferenceCount != 1 || claim.URL != URLPath(publicID) {
		t.Fatalf("claim = %#v", claim)
	}
	var visibility string
	var referenceCount int
	if err := pool.QueryRow(ctx,
		`SELECT visibility, reference_count FROM attachments WHERE id = $1`, attachmentID,
	).Scan(&visibility, &referenceCount); err != nil {
		t.Fatal(err)
	}
	// 认领强制公开：图标要对所有访客可读。
	if visibility != "public" || referenceCount != 1 {
		t.Fatalf("visibility=%s referenceCount=%d", visibility, referenceCount)
	}
}

func TestIntegrationClaimReplacesPreviousReference(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	first := "it-ext-" + suffix + "-1"
	second := "it-ext-" + suffix + "-2"
	firstID := insertIntegrationAttachment(t, pool, first, ownerUserID, "image/png", 1024, "public")
	secondID := insertIntegrationAttachment(t, pool, second, ownerUserID, "image/png", 2048, "public")

	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: first, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, first),
		Policy:           Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit},
	}); err != nil {
		t.Fatal(err)
	}
	// 同一 context 换图：唯一索引只允许一行，必须删旧插新。
	claim, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: second, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, second),
		Policy:           Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.AttachmentID != secondID || claim.ReferenceCount != 1 {
		t.Fatalf("claim = %#v", claim)
	}
	var oldCount, newCount int
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, firstID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT reference_count FROM attachments WHERE id = $1`, secondID).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || newCount != 1 {
		t.Fatalf("counts old=%d new=%d", oldCount, newCount)
	}
	var references int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM attachment_references WHERE resource_type = 'extension' AND context = $1`,
		extensionID+":icon").Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 1 {
		t.Fatalf("references = %d", references)
	}
}

func TestIntegrationClaimRejectsForeignReferences(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	publicID := "it-ext-" + suffix + "-seo"
	attachmentID := insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")
	// 正在被 SEO 使用的文件不能被改成扩展公开资产。
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context)
		VALUES ($1, 'seo', 0, 'home.og_image')
	`, attachmentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
	})
	_, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: publicID, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, publicID),
		Policy:           Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationClaimAuthorizationAndLimits(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	publicID := "it-ext-" + suffix + "-auth"
	insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")
	revision := attachmentRevision(t, pool, publicID)
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}

	// 非所有者且无 attachment.manage：拒绝。
	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: publicID,
		ActorUserID: ownerUserID + 1, ExpectedRevision: revision, Policy: policy,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign actor err = %v", err)
	}
	// 持 attachment.manage：允许认领任意附件。
	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: publicID,
		ActorUserID: ownerUserID, ManageAny: true, ExpectedRevision: revision, Policy: policy,
	}); err != nil {
		t.Fatal(err)
	}
	// 修订号不匹配：并发冲突。
	other := "it-ext-" + suffix + "-auth2"
	insertIntegrationAttachment(t, pool, other, ownerUserID, "image/png", 1024, "public")
	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon2", PublicID: other, ActorUserID: ownerUserID,
		ExpectedRevision: revision, Policy: policy,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision conflict err = %v", err)
	}
}

func TestIntegrationClaimRejectsNonImageAndOversize(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}

	svg := "it-ext-" + suffix + "-svg"
	insertIntegrationAttachment(t, pool, svg, ownerUserID, "image/svg+xml", 1024, "public")
	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: svg, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, svg), Policy: policy,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("svg err = %v", err)
	}

	big := "it-ext-" + suffix + "-big"
	insertIntegrationAttachment(t, pool, big, ownerUserID, "image/png", MaxImageBytes+1, "public")
	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: big, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, big), Policy: policy,
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestIntegrationClaimReferenceLimit(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: 1}
	for index, context := range []string{"icon-a", "icon-b"} {
		publicID := fmt.Sprintf("it-ext-%s-limit%d", suffix, index)
		insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")
		_, err := runClaim(t, store, pool, ClaimInput{
			ExtensionID: extensionID, Context: context, PublicID: publicID, ActorUserID: ownerUserID,
			ExpectedRevision: attachmentRevision(t, pool, publicID), Policy: policy,
		})
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && !errors.Is(err, ErrLimitReached) {
			t.Fatalf("limit err = %v", err)
		}
	}
}

func TestIntegrationReleaseScopesAndPrefixEquality(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}
	publicID := "it-ext-" + suffix + "-release"
	attachmentID := insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")

	if _, err := runClaim(t, store, pool, ClaimInput{
		ExtensionID: extensionID, Context: "icon", PublicID: publicID, ActorUserID: ownerUserID,
		ExpectedRevision: attachmentRevision(t, pool, publicID), Policy: policy,
	}); err != nil {
		t.Fatal(err)
	}
	// 另一个扩展 id 是本扩展 id 的前缀 + 通配字符：前缀等值匹配不能误切。
	sibling := extensionID + "_x"
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context)
		VALUES ($1, 'extension', 0, $2)
	`, attachmentID, sibling+":icon"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
	})

	// 跨扩展隔离：本扩展不能释放兄弟扩展的引用。
	released, err := runRelease(t, store, pool, ReleaseInput{ExtensionID: extensionID, Context: "icon"})
	if err != nil {
		t.Fatal(err)
	}
	if released.Released != 1 {
		t.Fatalf("released = %d", released.Released)
	}
	var siblingRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM attachment_references
		WHERE attachment_id = $1 AND LEFT(context, length($2) + 1) = $2 || ':'
	`, attachmentID, sibling).Scan(&siblingRows); err != nil {
		t.Fatal(err)
	}
	if siblingRows != 1 {
		t.Fatalf("sibling references = %d", siblingRows)
	}
	// 幂等：重复 release 不报错。
	if again, err := runRelease(t, store, pool, ReleaseInput{ExtensionID: extensionID, Context: "icon"}); err != nil || again.Released != 0 {
		t.Fatalf("idempotent release = %#v err = %v", again, err)
	}
}

func TestIntegrationReleaseByPublicIDKeepsOtherExtensions(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}
	publicID := "it-ext-" + suffix + "-shared"
	attachmentID := insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")

	// 同一附件允许被同扩展多个 context 共享。
	for _, contextValue := range []string{"icon", "banner"} {
		if _, err := runClaim(t, store, pool, ClaimInput{
			ExtensionID: extensionID, Context: contextValue, PublicID: publicID, ActorUserID: ownerUserID,
			ExpectedRevision: attachmentRevision(t, pool, publicID), Policy: policy,
		}); err != nil {
			t.Fatal(err)
		}
	}
	other := extensionID + "_other"
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_references (attachment_id, resource_type, resource_id, context)
		VALUES ($1, 'extension', 0, $2)
	`, attachmentID, other+":icon"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM attachment_references WHERE attachment_id = $1`, attachmentID)
	})

	released, err := runRelease(t, store, pool, ReleaseInput{ExtensionID: extensionID, PublicID: publicID})
	if err != nil {
		t.Fatal(err)
	}
	if released.Released != 2 {
		t.Fatalf("released = %d", released.Released)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM attachment_references WHERE attachment_id = $1`, attachmentID,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d (other extension must be untouched)", remaining)
	}
}

func TestIntegrationListAndReleaseForExtension(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	for index, contextValue := range []string{"icon", "banner"} {
		publicID := fmt.Sprintf("it-ext-%s-list%d", suffix, index)
		insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public")
		if _, err := runClaim(t, store, pool, ClaimInput{
			ExtensionID: extensionID, Context: contextValue, PublicID: publicID, ActorUserID: ownerUserID,
			ExpectedRevision: attachmentRevision(t, pool, publicID), Policy: policy,
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := runList(t, store, pool, extensionID)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("list = %#v", list)
	}
	for _, item := range list.Items {
		// 存储键是 "<extID>:<context>"，对插件只暴露它自己的那一段。
		if strings.HasPrefix(item.Context, extensionID+":") {
			t.Fatalf("context must be exposed without the extension namespace: %q", item.Context)
		}
		if item.Context != "icon" && item.Context != "banner" {
			t.Fatalf("unexpected context %q", item.Context)
		}
		if item.URL != URLPath(item.PublicID) {
			t.Fatalf("item url = %q", item.URL)
		}
	}
	// 卸载收口：释放该扩展的全部引用。
	released, err := store.ReleaseExtensionAttachmentReferences(context.Background(), extensionID)
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("released = %d", released)
	}
	after, err := runList(t, store, pool, extensionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != 0 {
		t.Fatalf("list after release = %#v", after)
	}
}

// 并发：同 context、不同附件、不同事务。没有扩展级 advisory 锁时，两个事务会在
// 唯一索引上互等，ON CONFLICT DO NOTHING 吞掉插入后仍盲目 +1，产生
// reference_count > 0 却无引用行的幽灵附件。这里断言不变式始终成立。
func TestIntegrationConcurrentClaimKeepsCountInvariant(t *testing.T) {
	store, pool, extensionID, ownerUserID := newIntegrationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	policy := Policy{MaxBytes: MaxImageBytes, ReferenceLimit: DefaultLimit}
	publicIDs := []string{
		"it-ext-" + suffix + "-c1",
		"it-ext-" + suffix + "-c2",
	}
	attachmentIDs := make([]int64, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		attachmentIDs = append(attachmentIDs, insertIntegrationAttachment(t, pool, publicID, ownerUserID, "image/png", 1024, "public"))
	}

	var waitGroup sync.WaitGroup
	errs := make([]error, len(publicIDs))
	for index := range publicIDs {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			claim, err := runClaim(t, store, pool, ClaimInput{
				ExtensionID: extensionID, Context: "icon", PublicID: publicIDs[index],
				ActorUserID:      ownerUserID,
				ExpectedRevision: attachmentRevision(t, pool, publicIDs[index]),
				Policy:           policy,
			})
			if err != nil {
				errs[index] = err
				return
			}
			if claim.ReferenceCount != 1 {
				errs[index] = fmt.Errorf("reference count = %d", claim.ReferenceCount)
			}
		}(index)
	}
	waitGroup.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, ErrRevisionConflict) && !errors.Is(err, ErrConflict) {
			t.Fatalf("concurrent claim error: %v", err)
		}
	}

	// 不变式：每个附件的 reference_count 必须等于它的引用行数。
	rows, err := pool.Query(ctx, `
		SELECT a.id, a.reference_count,
		  (SELECT count(*) FROM attachment_references r WHERE r.attachment_id = a.id)
		FROM attachments a WHERE a.id = ANY($1::bigint[])
	`, attachmentIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           int64
			count, lines int
		)
		if err := rows.Scan(&id, &count, &lines); err != nil {
			t.Fatal(err)
		}
		if count != lines {
			t.Fatalf("attachment %d: reference_count=%d but %d reference rows (ghost count)", id, count, lines)
		}
	}
	var live int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM attachment_references
		WHERE resource_type = 'extension' AND resource_id = 0
		  AND LEFT(context, length($1) + 1) = $1 || ':'
	`, extensionID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live references = %d (unique index allows one row per context)", live)
	}
}

// runClaim / runRelease / runList 各自开一个事务，模拟 Host Command 的事务边界。
func runClaim(t *testing.T, store *Store, pool *pgxpool.Pool, input ClaimInput) (Claim, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	plan, err := store.PrepareClaim(ctx, tx, input)
	if err != nil {
		return Claim{}, err
	}
	claim, err := store.ApplyClaim(ctx, tx, plan)
	if err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func runRelease(t *testing.T, store *Store, pool *pgxpool.Pool, input ReleaseInput) (Release, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	released, err := store.Release(ctx, tx, input)
	if err != nil {
		return Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return released, nil
}

func runList(t *testing.T, store *Store, pool *pgxpool.Pool, extensionID string) (List, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return List{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	list, err := store.List(ctx, tx, ListInput{ExtensionID: extensionID})
	if err != nil {
		return List{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return List{}, err
	}
	return list, nil
}
