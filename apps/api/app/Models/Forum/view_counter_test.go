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

	deltas, err := c.DrainDeltas(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if deltas[42] != 2 {
		t.Fatalf("drained want 2 got %v", deltas)
	}
	if c.Delta(42) != 0 {
		t.Fatalf("delta should be empty after drain")
	}

	// 去重仍在：drain 后同访客仍不计
	c.RecordView(ctx, 42, "u:1")
	if c.Delta(42) != 0 {
		t.Fatalf("still within dedup window")
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
