package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 回归：主题运行时 watcher 启动失败时，必须先停掉评论修订 hub 的 LISTEN 协程再
// 关池。LISTEN 连接是借出的池资源，puddle 的 Pool.Close 会等它归还；顺序颠倒时
// 启动失败会变成「进程活着但不监听」，真实错误也永远打不出来。
func TestStartThemeRuntimeWatcherFailureStopsPoolHolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newAPIStartupFailureTestPool(t, ctx)

	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	hub := forum.NewCommentRevisionHub(hubCtx, pool)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = hub.Stop(stopCtx)
	})
	waitForCommentRevisionListen(t, pool)

	// 日志进可读缓冲：看门狗/停止超时日志是「顺序颠倒」的决定性证据，不能丢弃。
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))

	core := &apiCoreStack{
		pool: pool,
		// 与生产 closeCommentRevisionHub 同形：有界停止，超时记录日志而不阻塞。
		closeCommentRevisionHub: func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer stopCancel()
			if stopErr := hub.Stop(stopCtx); stopErr != nil {
				logger.Warn("comment revision hub stop timed out", "error", stopErr)
			}
		},
	}

	injectedCause := errors.New("theme runtime publish failed: injected startup failure")
	originalStart := startAPIThemeRuntimeWatcherForAPI
	startAPIThemeRuntimeWatcherForAPI = func(
		context.Context, *extensions.PostgresStore, *extensions.Service, *slog.Logger, time.Duration,
	) (*apiThemeRuntimeWatcherRuntime, error) {
		return nil, injectedCause
	}
	t.Cleanup(func() { startAPIThemeRuntimeWatcherForAPI = originalStart })

	type startResult struct {
		watcher *apiThemeRuntimeWatcherRuntime
		err     error
	}
	results := make(chan startResult, 1)
	startedAt := time.Now()
	go func() {
		watcher, err := startThemeRuntimeWatcherOrAbort(ctx, config.Config{}, logger, core)
		results <- startResult{watcher: watcher, err: err}
	}()

	var got startResult
	select {
	case got = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("startThemeRuntimeWatcherOrAbort blocked: pool.Close waited for the comment revision LISTEN connection")
	}
	elapsed := time.Since(startedAt)

	if got.watcher != nil {
		t.Fatalf("watcher = %#v, want nil on startup failure", got.watcher)
	}
	if !errors.Is(got.err, injectedCause) {
		t.Fatalf("error = %v, want wrapped injected cause", got.err)
	}
	if !strings.Contains(got.err.Error(), "start theme runtime watcher failed:") {
		t.Fatalf("error = %q, want wrapper prefix", got.err.Error())
	}
	if !strings.Contains(got.err.Error(), injectedCause.Error()) {
		t.Fatalf("error = %q, want real cause text %q", got.err.Error(), injectedCause.Error())
	}

	// 决定性断言：关池必须走干净路径。看门狗/停止超时日志出现，说明 hub 是在
	// pool.Close 之后才被停掉的——顺序颠倒会被 5s 兜底掩盖成「不挂」。
	if logs := logOutput.String(); strings.Contains(logs, "api database pool close timed out") ||
		strings.Contains(logs, "comment revision hub stop timed out") {
		stat := pool.Stat()
		t.Fatalf(
			"startup failure cleanup fell back to a timeout instead of stopping the hub before pool.Close (pool total=%d acquired=%d):\n%s",
			stat.TotalConns(), stat.AcquiredConns(), logs,
		)
	}
	// 第二道：干净关闭是毫秒级；耗时逼近看门狗上限即说明走了兜底路径。
	if elapsed >= apiStartupPoolCloseTimeout {
		t.Fatalf(
			"startup failure cleanup took %s, want < %s: pool.Close ran before the comment revision hub stopped",
			elapsed, apiStartupPoolCloseTimeout,
		)
	}

	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer acquireCancel()
	if conn, acquireErr := pool.Acquire(acquireCtx); acquireErr == nil {
		conn.Release()
		t.Fatal("pool is still open after startup failure")
	}
	// 决定性断言：LISTEN 连接必须已归还，否则 puddle 的 Close 仍在等它。
	stat := pool.Stat()
	if stat.TotalConns() != 0 || stat.AcquiredConns() != 0 {
		t.Fatalf(
			"pool stats after startup failure = total %d / acquired %d, want 0/0: the comment revision LISTEN connection was not stopped before pool.Close",
			stat.TotalConns(), stat.AcquiredConns(),
		)
	}
}

func newAPIStartupFailureTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() {
		// 回归存在时关池仍在等未归还的连接：清理阶段同样要有界，别把 CI 拖住。
		done := make(chan struct{})
		go func() {
			defer close(done)
			pool.Close()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("pool close cleanup timed out")
		}
	})
	return pool
}

// waitForCommentRevisionListen 等到 hub 真正借出 LISTEN 连接，让关池顺序问题可复现。
func waitForCommentRevisionListen(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Stat().AcquiredConns() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("comment revision hub did not acquire a LISTEN connection within 5s")
}
