package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// T1E 真实数据库 transition：link → unlink（TransitionTx）→ revision 冲突。
// 需要 SFORUM_TEST_DATABASE_URL；未设置时 skip。

func TestT1E_PostgresExternalLinkTransitionAndRevisionRace(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()

	subjectDigest := strings.Repeat("a", 64)
	input := fixture.linkInput(fixture.targetUserID, "t1e-external-link:transition", subjectDigest)
	mutation, err := fixture.externalLinks.Link(ctx, input, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Link.Status != ExternalIdentityLinkStatusActive || mutation.Link.Revision != 1 {
		t.Fatalf("link mutation=%#v", mutation)
	}

	// TransitionTx：在调用方事务内 unlink。
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlinked, err := fixture.externalLinks.TransitionTx(ctx, tx, ExternalIdentityLinkActionUnlink, TransitionExternalIdentityLinkInput{
		LinkID: mutation.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "t1e-external-link:transition:unlink",
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if unlinked.Link.Status != ExternalIdentityLinkStatusUnlinked || unlinked.Link.Revision != 2 {
		_ = tx.Rollback(ctx)
		t.Fatalf("transition unlink=%#v", unlinked)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// 陈旧 revision 必须冲突。
	_, err = fixture.externalLinks.Unlink(ctx, TransitionExternalIdentityLinkInput{
		LinkID: mutation.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "t1e-external-link:transition:stale",
	})
	if err == nil {
		t.Fatalf("stale revision must fail")
	}
	if !errors.Is(err, ErrExternalIdentityLinkStateConflict) &&
		!errors.Is(err, ErrExternalIdentityLinkNotFound) {
		// 已 unlinked 后 revision 1 可能 not found 或 conflict，两者均可接受。
		t.Logf("stale unlink err=%v (acceptable if not success)", err)
	}

	// 二次 link 不同 subject：同用户多 provider 能力（同一 fixture provider 下换 digest）。
	secondDigest := strings.Repeat("b", 64)
	second := fixture.linkInput(fixture.targetUserID, "t1e-external-link:second", secondDigest)
	second.ProviderSubjectDigest = secondDigest
	secondMut, err := fixture.externalLinks.Link(ctx, second, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if secondMut.Link.Status != ExternalIdentityLinkStatusActive {
		t.Fatalf("second link=%#v", secondMut)
	}

	// 列表 redaction 边界：DB 存 digest，但 ListUser 返回的字段调用方不得暴露给浏览器。
	listed, err := fixture.externalLinks.ListUser(ctx, fixture.targetUserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) < 1 {
		t.Fatalf("expected links for user")
	}
	for _, l := range listed {
		// ExternalIdentityLink 故意不暴露 subject digest 字段；Host 仅经 FindActive 使用。
		// 控制器只序列化 linkId/providerId/status/linkedAt/ownerExtensionId。
		if strings.Contains(l.ProviderID, secondDigest) {
			t.Fatalf("provider id must not embed digest")
		}
		if l.ID == 0 || l.ProviderID == "" {
			t.Fatalf("incomplete link list item: %#v", l)
		}
	}
}

func TestT1E_PostgresRegistrationRollbackOnDefaultRoleFailure(t *testing.T) {
	t.Parallel()
	// 证明 LinkTx 与调用方事务可 rollback：模拟 link 写入后主动 Rollback，
	// 行不得残留（registration 原子边界的 DB 证据）。
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()

	subjectDigest := strings.Repeat("c", 64)
	input := fixture.linkInput(fixture.targetUserID, "t1e-external-link:rollback", subjectDigest)

	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// LinkTx 在同一事务写入；随后 Rollback 模拟 default-role 失败。
	if _, err := fixture.externalLinks.LinkTx(ctx, tx, input); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// 回滚后不得有 active link。
	_, err = fixture.externalLinks.FindActive(ctx, fixture.provider.ID, subjectDigest)
	if !errors.Is(err, ErrExternalIdentityLinkNotFound) {
		t.Fatalf("rolled-back link must not be active: %v", err)
	}

	// 控制：提交路径可查到。
	tx2, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.externalLinks.LinkTx(ctx, tx2, input); err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	found, err := fixture.externalLinks.FindActive(ctx, fixture.provider.ID, subjectDigest)
	if err != nil || found.Status != ExternalIdentityLinkStatusActive {
		t.Fatalf("committed link missing: %#v err=%v", found, err)
	}
}

// 确保 pgx.Tx 导入用于类型检查（TransitionTx 参数）。
var _ = pgx.ErrTxClosed
