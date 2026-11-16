package idempotency

import (
	"context"
	"errors"
	"testing"
)

// leaseFixture 让「租约过期 → 新请求接管」变成确定性的：直接把 pending 的
// expiresAt 拨到过去，等价于进程在 Begin 之后崩溃、90s 短租约自然到期。
type leaseFixture struct {
	store   *Store
	backend *ttlRecordingBackend
}

func newLeaseFixture() leaseFixture {
	backend := &ttlRecordingBackend{MemoryBackend: NewMemoryBackend()}
	return leaseFixture{store: NewStore(backend, DefaultTTL), backend: backend}
}

func (f leaseFixture) expireLeases() { f.backend.expireAll() }

// TestCompleteWithStaleLeaseDoesNotOverwriteNewLease 是 P2-4 的核心断言：
// Complete 必须只允许 lease 持有者收敛。旧请求在租约过期后不能覆盖新请求。
//
// 变异要求：把 Complete 改回无条件 Set，本用例必须红（旧请求会覆盖新 lease）。
func TestCompleteWithStaleLeaseDoesNotOverwriteNewLease(t *testing.T) {
	fixture := newLeaseFixture()
	ctx := context.Background()
	key := "actor:POST:/topics:stale-complete"

	first, started, conflict, err := fixture.store.Begin(ctx, key)
	if err != nil || !started || conflict {
		t.Fatalf("first begin: started=%v conflict=%v err=%v", started, conflict, err)
	}
	if first.Lease == "" {
		t.Fatal("Begin 必须返回 lease，否则调用方无法证明自己是持有者")
	}

	// 租约过期，第二个请求抢到同一个 key。
	fixture.expireLeases()
	second, started, conflict, err := fixture.store.Begin(ctx, key)
	if err != nil || !started || conflict {
		t.Fatalf("second begin after expiry: started=%v conflict=%v err=%v", started, conflict, err)
	}
	if second.Lease == first.Lease {
		t.Fatal("两次 Begin 必须拿到不同的 lease 令牌，否则 CAS 无法区分新旧请求")
	}

	// 旧请求迟到的 Complete 必须失败，且不能覆盖新请求的 pending。
	if err := fixture.store.Complete(ctx, key, first.Lease, 201, []byte(`{"owner":"old"}`)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale complete err=%v want ErrLeaseLost", err)
	}
	pending, found, err := fixture.store.Get(ctx, key)
	if err != nil || !found || pending.State != statePending || pending.Lease != second.Lease {
		t.Fatalf("新请求的 pending 被旧请求破坏：found=%v rec=%#v err=%v", found, pending, err)
	}

	// 持有者照常收敛。
	if err := fixture.store.Complete(ctx, key, second.Lease, 202, []byte(`{"owner":"new"}`)); err != nil {
		t.Fatalf("current complete: %v", err)
	}
	completed, found, err := fixture.store.Get(ctx, key)
	if err != nil || !found || completed.State != stateCompleted || completed.Status != 202 {
		t.Fatalf("completed record = %#v found=%v err=%v", completed, found, err)
	}
}

// TestAbortWithStaleLeaseKeepsNewPending 固定 Abort 侧的同一语义：
// 旧请求的 Abort 不能把新请求的 pending 删掉（删掉 = 两个请求同时持权）。
func TestAbortWithStaleLeaseKeepsNewPending(t *testing.T) {
	fixture := newLeaseFixture()
	ctx := context.Background()
	key := "actor:POST:/topics:stale-abort"

	first, started, _, err := fixture.store.Begin(ctx, key)
	if err != nil || !started {
		t.Fatalf("first begin: started=%v err=%v", started, err)
	}
	fixture.expireLeases()
	second, started, _, err := fixture.store.Begin(ctx, key)
	if err != nil || !started {
		t.Fatalf("second begin: started=%v err=%v", started, err)
	}

	if err := fixture.store.Abort(ctx, key, first.Lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale abort err=%v want ErrLeaseLost", err)
	}
	// 新请求仍然持有执行权：第三个请求必须 409。
	if _, _, conflict, err := fixture.store.Begin(ctx, key); err != nil || !conflict {
		t.Fatalf("new pending was released by a stale abort: conflict=%v err=%v", conflict, err)
	}
	if err := fixture.store.Abort(ctx, key, second.Lease); err != nil {
		t.Fatalf("current abort: %v", err)
	}
	if _, started, conflict, err := fixture.store.Begin(ctx, key); err != nil || !started || conflict {
		t.Fatalf("begin after current abort: started=%v conflict=%v err=%v", started, conflict, err)
	}
}

// TestSettleWithoutLeaseIsRejected 防止调用方漏传 lease：空 lease 不能退化成
// 「无条件写/删」，否则 P2-4 的收敛保证等于没有。
func TestSettleWithoutLeaseIsRejected(t *testing.T) {
	fixture := newLeaseFixture()
	ctx := context.Background()
	key := "actor:POST:/topics:no-lease"

	rec, started, _, err := fixture.store.Begin(ctx, key)
	if err != nil || !started {
		t.Fatalf("begin: started=%v err=%v", started, err)
	}
	if err := fixture.store.Complete(ctx, key, "", 201, []byte(`{"ok":true}`)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("empty lease complete err=%v want ErrLeaseLost", err)
	}
	if err := fixture.store.Abort(ctx, key, "  "); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("blank lease abort err=%v want ErrLeaseLost", err)
	}
	pending, found, err := fixture.store.Get(ctx, key)
	if err != nil || !found || pending.State != statePending || pending.Lease != rec.Lease {
		t.Fatalf("空 lease 的收敛动作改动了 pending：found=%v rec=%#v err=%v", found, pending, err)
	}
}
