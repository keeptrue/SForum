package forum

import (
	"context"
	"testing"
	"time"
)

func TestMemoryTopicViewCounterDedupAndDrain(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryTopicViewCounter()
	c.DedupTTL = time.Minute

	c.RecordView(ctx, 42, "u:1")
	c.RecordView(ctx, 42, "u:1") // 同访客 30m 内不重复
	c.RecordView(ctx, 42, "u:2")
	if got := c.Delta(42); got != 2 {
		t.Fatalf("delta want 2 got %d", got)
	}

	deltas, err := c.ReadViewDeltas(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if deltas[42] != 2 {
		t.Fatalf("read want 2 got %v", deltas)
	}
	// 读取不消费：落库成功前增量必须还在。
	if c.Delta(42) != 2 {
		t.Fatalf("read must not consume the delta, got %d", c.Delta(42))
	}
	if err := c.AckViewDeltas(ctx, deltas); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if c.Delta(42) != 0 {
		t.Fatalf("delta should be empty after ack")
	}

	// 去重仍在：drain 后同访客仍不计
	c.RecordView(ctx, 42, "u:1")
	if c.Delta(42) != 0 {
		t.Fatalf("still within dedup window")
	}
}

// nil client 必须保持接口字段为 nil：typed-nil 会让方法内的 nil 判定失效并 panic，
// 打破 worker 侧「nil Redis → 空跑」的契约（RV-52 复审项）。
func TestNewRedisTopicViewCounterWithNilClientDoesNotPanic(t *testing.T) {
	counter := NewRedisTopicViewCounter(nil)
	ctx := context.Background()
	counter.RecordView(ctx, 1, "u:1")
	deltas, err := counter.ReadViewDeltas(ctx)
	if err != nil || len(deltas) != 0 {
		t.Fatalf("nil-client read = %v, %v; want empty and no error", deltas, err)
	}
	if err := counter.AckViewDeltas(ctx, map[int64]int64{1: 1}); err != nil {
		t.Fatalf("nil-client ack: %v", err)
	}
}

func TestMemoryTopicViewCounterFailRecordSkips(t *testing.T) {
	c := NewMemoryTopicViewCounter()
	c.FailRecord = true
	c.RecordView(context.Background(), 1, "u:1")
	if c.Delta(1) != 0 {
		t.Fatal("failed recorder must skip")
	}
}

func TestHashVisitorKeyStable(t *testing.T) {
	a := HashVisitorKey("u:9")
	b := HashVisitorKey("u:9")
	if a == "" || a != b {
		t.Fatalf("hash unstable: %q %q", a, b)
	}
	if HashVisitorKey("u:9") == HashVisitorKey("u:10") {
		t.Fatal("different keys should hash differently")
	}
}
