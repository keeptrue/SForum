package extensionsjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	deliverysweep "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/deliverysweep"
)

type stubSweeper struct {
	swept     int
	err       error
	threshold time.Duration
	calls     int
}

func (s *stubSweeper) SweepStaleEventDeliveries(_ context.Context, olderThan time.Duration) (int, error) {
	s.calls++
	s.threshold = olderThan
	return s.swept, s.err
}

func TestSweepStaleEventDeliveriesWorkerRequiresStore(t *testing.T) {
	worker := &SweepStaleEventDeliveriesWorker{}
	if err := worker.Work(context.Background(), nil); err == nil {
		t.Fatal("缺少 store 必须报错，而不是静默成功")
	}
}

func TestSweepStaleEventDeliveriesWorkerUsesDefaultThreshold(t *testing.T) {
	store := &stubSweeper{}
	worker := &SweepStaleEventDeliveriesWorker{Store: store}
	if err := worker.Work(context.Background(), nil); err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.threshold != deliverysweep.StaleEventDeliveryThreshold {
		t.Fatalf("threshold = %v, want %v", store.threshold, deliverysweep.StaleEventDeliveryThreshold)
	}
}

// 失败姿态：返回错误交给 River 重试，不 panic；下一轮（新一轮 schedule 触发）
// 不受上一轮失败影响。
func TestSweepStaleEventDeliveriesWorkerFailureDoesNotBlockNextRound(t *testing.T) {
	store := &stubSweeper{err: errors.New("database is unavailable")}
	worker := &SweepStaleEventDeliveriesWorker{Store: store}
	err := worker.Work(context.Background(), nil)
	if err == nil {
		t.Fatal("sweep 失败必须返回错误（River 据此重试）")
	}
	if !errors.Is(err, store.err) {
		t.Fatalf("err = %v, want wrapped %v", err, store.err)
	}

	store.err = nil
	store.swept = 3
	if err := worker.Work(context.Background(), nil); err != nil {
		t.Fatalf("second round: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 2", store.calls)
	}
}
