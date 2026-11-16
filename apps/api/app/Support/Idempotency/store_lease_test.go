package idempotency

import (
	"context"
	"testing"
	"time"
)

// pending 是 in-flight 租约，不是「已完成响应」的保留窗口（RV-52）。
// 用同一个 24h TTL 会出现：进程在 Begin 与 Complete/Abort 之间死掉，
// 同一个 Idempotency-Key 被 409 锁死一整天。两条 TTL 必须分开。
func TestPendingLeaseUsesShortTTLWhileCompletedKeepsReplayWindow(t *testing.T) {
	backend := &ttlRecordingBackend{MemoryBackend: NewMemoryBackend()}
	store := NewStore(backend, DefaultTTL)
	ctx := context.Background()

	rec, started, conflict, err := store.Begin(ctx, "actor:POST:/topics:abc")
	if err != nil || !started || conflict {
		t.Fatalf("begin: started=%v conflict=%v err=%v", started, conflict, err)
	}
	if len(backend.pendingTTLs) != 1 {
		t.Fatalf("pending lease writes = %d, want 1", len(backend.pendingTTLs))
	}
	if got := backend.pendingTTLs[0]; got != DefaultPendingTTL {
		t.Fatalf("pending lease TTL = %s, want %s (a crashed Begin must not lock the key for the full replay window)", got, DefaultPendingTTL)
	}
	if DefaultPendingTTL >= DefaultTTL {
		t.Fatalf("pending lease %s must be shorter than the completed replay window %s", DefaultPendingTTL, DefaultTTL)
	}

	if err := store.Complete(ctx, "actor:POST:/topics:abc", rec.Lease, 201, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(backend.completedTTLs) != 1 {
		t.Fatalf("completed writes = %d, want 1", len(backend.completedTTLs))
	}
	if got := backend.completedTTLs[0]; got != DefaultTTL {
		t.Fatalf("completed record TTL = %s, want %s", got, DefaultTTL)
	}
}

// 租约到期后同一个 key 必须能重新开始，而不是继续 409。
func TestExpiredPendingLeaseAllowsRetry(t *testing.T) {
	backend := &ttlRecordingBackend{MemoryBackend: NewMemoryBackend()}
	store := NewStore(backend, DefaultTTL)
	ctx := context.Background()
	key := "actor:POST:/topics:abc"

	if _, started, _, err := store.Begin(ctx, key); err != nil || !started {
		t.Fatalf("first begin: started=%v err=%v", started, err)
	}
	if _, _, conflict, err := store.Begin(ctx, key); err != nil || !conflict {
		t.Fatalf("second begin within the lease: conflict=%v err=%v", conflict, err)
	}
	// 直接把租约过期：等价于进程在 Begin 之后崩溃、短租约自然到期。
	backend.expireAll()
	if _, started, conflict, err := store.Begin(ctx, key); err != nil || !started || conflict {
		t.Fatalf("begin after lease expiry: started=%v conflict=%v err=%v", started, conflict, err)
	}
}

type ttlRecordingBackend struct {
	*MemoryBackend
	pendingTTLs   []time.Duration
	completedTTLs []time.Duration
}

func (b *ttlRecordingBackend) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	b.pendingTTLs = append(b.pendingTTLs, ttl)
	return b.MemoryBackend.SetNX(ctx, key, value, ttl)
}

func (b *ttlRecordingBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	b.completedTTLs = append(b.completedTTLs, ttl)
	return b.MemoryBackend.Set(ctx, key, value, ttl)
}

// Complete 走 CompareAndSwap 收敛（P2-4），完成记录的 TTL 从这里来；
// replacement 为 nil 是 Abort 的删除，不算一次完成写入。
func (b *ttlRecordingBackend) CompareAndSwap(
	ctx context.Context, key string, expected, replacement []byte, ttl time.Duration,
) (bool, error) {
	if replacement != nil {
		b.completedTTLs = append(b.completedTTLs, ttl)
	}
	return b.MemoryBackend.CompareAndSwap(ctx, key, expected, replacement, ttl)
}

func (b *ttlRecordingBackend) expireAll() {
	b.MemoryBackend.mu.Lock()
	defer b.MemoryBackend.mu.Unlock()
	for key, entry := range b.MemoryBackend.items {
		entry.expiresAt = time.Now().Add(-time.Second)
		b.MemoryBackend.items[key] = entry
	}
}
