package forum

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 用结构体字面量构造 hub：Subscribe 的容量判定与唤醒扇出不需要真实连接，
// 只有 LISTEN 循环才需要 pool（listen() 不在这些用例里运行）。
func newTestCommentRevisionHub() *CommentRevisionHub {
	return &CommentRevisionHub{
		pool:     &pgxpool.Pool{},
		byTopic:  make(map[int64]map[uint64]chan struct{}),
		byClient: make(map[string]int),
	}
}

func TestCommentRevisionHubRejectsInvalidTopic(t *testing.T) {
	hub := newTestCommentRevisionHub()
	if _, _, err := hub.Subscribe(0, "1.2.3.4"); !errors.Is(err, ErrCommentRevisionWakeUnavailable) {
		t.Fatalf("zero topic error = %v, want wake unavailable", err)
	}
	var nilHub *CommentRevisionHub
	if _, _, err := nilHub.Subscribe(10, "1.2.3.4"); !errors.Is(err, ErrCommentRevisionWakeUnavailable) {
		t.Fatalf("nil hub error = %v, want wake unavailable", err)
	}
}

func TestCommentRevisionHubCapsPerClientAndReleases(t *testing.T) {
	hub := newTestCommentRevisionHub()
	releases := make([]func(), 0, maxCommentStreamConnectionsPerClient)
	for i := 0; i < maxCommentStreamConnectionsPerClient; i++ {
		_, release, err := hub.Subscribe(int64(10+i), "1.2.3.4")
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	if _, _, err := hub.Subscribe(99, "1.2.3.4"); !errors.Is(err, ErrCommentRevisionConnectionLimit) {
		t.Fatalf("over per-client cap error = %v, want limit", err)
	}
	// 释放一个连接后同一客户端可再次订阅；重复 release 不得重复扣减。
	releases[0]()
	releases[0]()
	if _, release, err := hub.Subscribe(99, "1.2.3.4"); err != nil {
		t.Fatalf("resubscribe after release: %v", err)
	} else {
		release()
	}
	if hub.total != maxCommentStreamConnectionsPerClient-1 {
		t.Fatalf("tracked connections = %d, want %d", hub.total, maxCommentStreamConnectionsPerClient-1)
	}
}

func TestCommentRevisionHubCapsPerTopic(t *testing.T) {
	hub := newTestCommentRevisionHub()
	// 每个订阅用独立客户端键，隔离单主题上限与单客户端上限。
	for i := 0; i < maxCommentStreamConnectionsPerTopic; i++ {
		if _, release, err := hub.Subscribe(42, fmt.Sprintf("client-%d", i)); err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		} else {
			_ = release
		}
	}
	if _, _, err := hub.Subscribe(42, "another"); !errors.Is(err, ErrCommentRevisionConnectionLimit) {
		t.Fatalf("over per-topic cap error = %v, want limit", err)
	}
}

func TestCommentRevisionHubPublishesOnlyToTopicSubscribersAndCoalesces(t *testing.T) {
	hub := newTestCommentRevisionHub()
	wakeA, releaseA, err := hub.Subscribe(42, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	wakeB, releaseB, err := hub.Subscribe(43, "b")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()

	hub.publish(42)
	hub.publish(42)
	select {
	case <-wakeA:
	default:
		t.Fatal("topic 42 subscriber was not woken")
	}
	select {
	case <-wakeA:
		t.Fatal("duplicate wake was not coalesced")
	default:
	}
	select {
	case <-wakeB:
		t.Fatal("topic 43 subscriber must not receive topic 42 wake")
	default:
	}
}

// Stop 必须尊重调用方给出的截止时间：LISTEN 循环没退出时不能无限等下去，
// 否则启动失败路径会在关池前再次挂住。
func TestCommentRevisionHubStopHonorsDeadline(t *testing.T) {
	listenCtx, cancelListen := context.WithCancel(context.Background())
	hub := &CommentRevisionHub{ctx: listenCtx, cancel: cancelListen, done: make(chan struct{})}
	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	if err := hub.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop error = %v, want context.Canceled", err)
	}
	if listenCtx.Err() == nil {
		t.Fatal("stop did not cancel the LISTEN context")
	}
	// 循环退出后 Close 幂等：重复调用都立即返回。
	close(hub.done)
	hub.Close()
	hub.Close()
	var nilHub *CommentRevisionHub
	if err := nilHub.Stop(context.Background()); err != nil {
		t.Fatalf("nil hub stop error = %v, want nil", err)
	}
}

// 正常路径下 Stop 应等到 LISTEN 循环真正退出，而不是取消后立刻返回。
func TestCommentRevisionHubStopWaitsForListenLoopExit(t *testing.T) {
	listenCtx, cancelListen := context.WithCancel(context.Background())
	hub := &CommentRevisionHub{ctx: listenCtx, cancel: cancelListen, done: make(chan struct{})}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()
	if err := hub.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error = %v, want context.DeadlineExceeded", err)
	}
	close(hub.done)
	if err := hub.Stop(context.Background()); err != nil {
		t.Fatalf("stop after loop exit = %v, want nil", err)
	}
}

func TestCommentLiveServiceWithoutRevisionCapability(t *testing.T) {
	service := NewCommentLiveService(nil)
	if _, err := service.RevisionState(context.Background(), 10); !errors.Is(err, ErrCommentRevisionUnavailable) {
		t.Fatalf("error = %v, want revision unavailable", err)
	}
	if _, _, err := service.Subscribe(10, "1.2.3.4"); !errors.Is(err, ErrCommentRevisionWakeUnavailable) {
		t.Fatalf("subscribe error = %v, want wake unavailable", err)
	}
}
