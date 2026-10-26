package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestProductionQueryResultCacheSafeModeDoesNotCreateRedisRuntime(t *testing.T) {
	runtime, err := startProductionQueryResultCache(context.Background(), config.Config{
		SafeMode: true, RedisAddr: "127.0.0.1:1",
	}, "")
	if err != nil || runtime == nil || runtime.client != nil || runtime.Cache() != nil || runtime.Invalidator() != nil {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
}

func TestProductionQueryInvalidationSafeModeIsTrueNilAndLazy(t *testing.T) {
	runtime := newProductionQueryInvalidationRuntime(config.Config{
		SafeMode: true, RedisAddr: "127.0.0.1:1",
	}, "", nil)
	if runtime == nil || runtime.Invalidator() != nil {
		t.Fatalf("runtime=%#v invalidator=%#v", runtime, runtime.Invalidator())
	}
	runtime.Close()
	runtime.Close()
}

func TestProductionQueryResultCacheClientDisablesRetriesBoundsIOAndIsIndependent(t *testing.T) {
	cfg := config.Config{
		RedisAddr: "127.0.0.1:6379", RedisPassword: "secret",
		RedisPoolSize: 7, RedisMinIdleConns: 2,
		RedisDialTimeout: 2 * time.Second, RedisReadTimeout: 4 * time.Second,
		RedisWriteTimeout: 5 * time.Second, RedisConnMaxIdleTime: 6 * time.Minute,
		RedisConnMaxLifetime: 7 * time.Minute,
	}
	client := newProductionQueryResultCacheClient(cfg)
	independent := newProductionQueryResultCacheClient(cfg)
	t.Cleanup(func() {
		_ = client.Close()
		_ = independent.Close()
	})
	if client == independent {
		t.Fatal("API and worker constructors shared a Redis client")
	}
	options := client.Options()
	if options.Addr != cfg.RedisAddr || options.Password != cfg.RedisPassword ||
		options.PoolSize != cfg.RedisPoolSize || options.MinIdleConns != cfg.RedisMinIdleConns ||
		options.DialTimeout != cfg.RedisDialTimeout || options.ReadTimeout != cfg.RedisReadTimeout ||
		options.WriteTimeout != cfg.RedisWriteTimeout || options.ConnMaxIdleTime != cfg.RedisConnMaxIdleTime ||
		options.ConnMaxLifetime != cfg.RedisConnMaxLifetime || options.MaxRetries != 0 ||
		!options.ContextTimeoutEnabled {
		t.Fatalf("Query cache Redis options=%#v", options)
	}
}

func TestProductionQueryResultCacheRejectsInvalidInstallationBeforeRedisIO(t *testing.T) {
	runtime, err := startProductionQueryResultCache(context.Background(), config.Config{
		RedisAddr: "127.0.0.1:1", RedisReadTimeout: 4 * time.Second,
		RedisWriteTimeout: 4 * time.Second,
	}, "invalid")
	if err == nil || runtime != nil || !errors.Is(err, queryregistry.ErrExecutionInvalid) {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
}

func TestOptionalProductionQueryResultCacheFallsBackWithoutLeakingError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	secret := "redis://credential@query:value:key"
	runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
		context.Background(), config.Config{}, "installation", logger,
		func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
			return nil, fmt.Errorf("%w: %s", queryregistry.ErrCacheCapability, secret)
		},
	)
	if err != nil || runtime == nil || runtime.Cache() != nil || runtime.client != nil {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if logged := output.String(); bytes.Contains([]byte(logged), []byte(secret)) ||
		!bytes.Contains([]byte(logged), []byte("error_class=capability")) {
		t.Fatalf("unsafe cache startup log=%q", logged)
	}
}

func TestOptionalProductionQueryResultCachePreservesFatalStartupErrors(t *testing.T) {
	runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
		context.Background(), config.Config{}, "installation", nil,
		func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
			return nil, queryregistry.ErrExecutionInvalid
		},
	)
	if runtime != nil || !errors.Is(err, queryregistry.ErrExecutionInvalid) {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}

	t.Run("context canceled before cache startup", func(t *testing.T) {
		cause := errors.New("API bootstrap canceled")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		var starterCalls atomic.Int32
		runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
			ctx, config.Config{}, "installation", nil,
			func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
				starterCalls.Add(1)
				return nil, nil
			},
		)
		if runtime != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || starterCalls.Load() != 0 {
			t.Fatalf("runtime=%#v err=%v starter_calls=%d", runtime, err, starterCalls.Load())
		}
	})

	t.Run("context deadline before cache startup", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		var starterCalls atomic.Int32
		runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
			ctx, config.Config{}, "installation", nil,
			func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
				starterCalls.Add(1)
				return nil, nil
			},
		)
		if runtime != nil || !errors.Is(err, context.DeadlineExceeded) || starterCalls.Load() != 0 {
			t.Fatalf("runtime=%#v err=%v starter_calls=%d", runtime, err, starterCalls.Load())
		}
	})

	t.Run("context cause wins over cache error", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("API shutdown")
		client := newProductionQueryResultCacheClient(config.Config{})
		runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
			ctx, config.Config{}, "installation", nil,
			func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
				cancel(cause)
				return &productionQueryResultCacheRuntime{client: client}, queryregistry.ErrCacheCapability
			},
		)
		if runtime != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("runtime=%#v err=%v", runtime, err)
		}
		if pingErr := client.Ping(context.Background()).Err(); !errors.Is(pingErr, redis.ErrClosed) {
			t.Fatalf("partial runtime client remained open: %v", pingErr)
		}
	})
}

func TestOptionalProductionQueryResultCacheFallsBackForRedisLocalContextErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass string
	}{
		{name: "canceled", err: context.Canceled, wantClass: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, wantClass: "deadline"},
		{
			name: "AOF deadline", wantClass: "capability",
			err: errors.Join(queryregistry.ErrCacheCapability, queryregistry.ErrCacheDurability, context.DeadlineExceeded),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&output, nil))
			client := newProductionQueryResultCacheClient(config.Config{})
			secret := "redis://credential@query:value:key"
			runtime, err := loadOptionalProductionQueryResultCacheWithStarter(
				context.Background(), config.Config{}, "installation", logger,
				func(context.Context, config.Config, string) (*productionQueryResultCacheRuntime, error) {
					return &productionQueryResultCacheRuntime{client: client}, errors.Join(test.err, errors.New(secret))
				},
			)
			if err != nil || runtime == nil || runtime.Cache() != nil || runtime.client != nil {
				t.Fatalf("runtime=%#v err=%v", runtime, err)
			}
			if pingErr := client.Ping(context.Background()).Err(); !errors.Is(pingErr, redis.ErrClosed) {
				t.Fatalf("partial runtime client remained open: %v", pingErr)
			}
			logged := output.String()
			if bytes.Contains([]byte(logged), []byte(secret)) ||
				!bytes.Contains([]byte(logged), []byte("error_class="+test.wantClass)) {
				t.Fatalf("unsafe cache startup log=%q", logged)
			}
		})
	}
}

func TestRecoveringQueryInvalidatorReplacesTerminalInstance(t *testing.T) {
	first := &fakeOwnedProductionQueryInvalidator{err: queryregistry.ErrCacheDurability}
	second := &fakeOwnedProductionQueryInvalidator{rotated: 1}
	var factoryCalls atomic.Int32
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		switch factoryCalls.Add(1) {
		case 1:
			return first, nil
		case 2:
			return second, nil
		default:
			return nil, errors.New("unexpected factory call")
		}
	}, nil)
	invalidator := runtime.Invalidator()
	if _, err := invalidator.InvalidateOwnerTags(context.Background(), "owner.plugin", []string{"owner.plugin.items"}); !errors.Is(err, queryregistry.ErrCacheDurability) {
		t.Fatalf("first invalidation err=%v", err)
	}
	if _, closes := first.snapshot(); closes != 1 {
		t.Fatalf("terminal first close calls=%d", closes)
	}
	if rotated, err := invalidator.InvalidateOwnerTags(context.Background(), "owner.plugin", []string{"owner.plugin.items"}); err != nil || rotated != 1 {
		t.Fatalf("replacement invalidation rotated=%d err=%v", rotated, err)
	}
	runtime.Close()
	runtime.Close()
	if calls, closes := second.snapshot(); calls != 1 || closes != 1 || factoryCalls.Load() != 2 {
		t.Fatalf("replacement calls=%d closes=%d factory=%d", calls, closes, factoryCalls.Load())
	}
}

func TestRecoveringQueryInvalidatorRetriesFactoryActivationFailure(t *testing.T) {
	ready := &fakeOwnedProductionQueryInvalidator{rotated: 1}
	var factoryCalls atomic.Int32
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		if factoryCalls.Add(1) == 1 {
			return nil, queryregistry.ErrCacheCapability
		}
		return ready, nil
	}, nil)
	invalidator := runtime.Invalidator()
	if _, err := invalidator.InvalidateOwnerTags(context.Background(), "owner.plugin", []string{"owner.plugin.items"}); !errors.Is(err, queryregistry.ErrCacheCapability) {
		t.Fatalf("activation failure err=%v", err)
	}
	if rotated, err := invalidator.InvalidateOwnerTags(context.Background(), "owner.plugin", []string{"owner.plugin.items"}); err != nil || rotated != 1 || factoryCalls.Load() != 2 {
		t.Fatalf("retry rotated=%d err=%v factory=%d", rotated, err, factoryCalls.Load())
	}
	runtime.Close()
}

func TestRecoveringQueryInvalidatorClosesPartialFactoryResult(t *testing.T) {
	partial := &fakeOwnedProductionQueryInvalidator{}
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		return partial, queryregistry.ErrCacheCapability
	}, nil)
	if _, err := runtime.Invalidator().InvalidateOwnerTags(
		context.Background(), "owner.plugin", []string{"owner.plugin.items"},
	); !errors.Is(err, queryregistry.ErrCacheCapability) {
		t.Fatalf("partial activation err=%v", err)
	}
	if calls, closes := partial.snapshot(); calls != 0 || closes != 1 {
		t.Fatalf("partial authority calls=%d closes=%d", calls, closes)
	}
	runtime.Close()
}

func TestRecoveringQueryInvalidatorKeepsAuthorityForInvalidInput(t *testing.T) {
	current := &fakeOwnedProductionQueryInvalidator{err: queryregistry.ErrInvalid}
	var factoryCalls atomic.Int32
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		factoryCalls.Add(1)
		return current, nil
	}, nil)
	for range 2 {
		if _, err := runtime.Invalidator().InvalidateOwnerTags(
			context.Background(), "owner.plugin", []string{"owner.plugin.items"},
		); !errors.Is(err, queryregistry.ErrInvalid) {
			t.Fatalf("invalid input err=%v", err)
		}
	}
	if calls, closes := current.snapshot(); calls != 2 || closes != 0 || factoryCalls.Load() != 1 {
		t.Fatalf("calls=%d closes=%d factory=%d", calls, closes, factoryCalls.Load())
	}
	runtime.Close()
	if _, closes := current.snapshot(); closes != 1 {
		t.Fatalf("close calls=%d", closes)
	}
}

func TestRecoveringQueryInvalidatorConcurrentCallsShareOneAuthority(t *testing.T) {
	current := &fakeOwnedProductionQueryInvalidator{rotated: 1}
	var factoryCalls atomic.Int32
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		factoryCalls.Add(1)
		return current, nil
	}, nil)
	const calls = 32
	var wait sync.WaitGroup
	errorsFound := make(chan error, calls)
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			rotated, err := runtime.Invalidator().InvalidateOwnerTags(
				context.Background(), "owner.plugin", []string{"owner.plugin.items"},
			)
			if err != nil || rotated != 1 {
				errorsFound <- fmt.Errorf("rotated=%d err=%w", rotated, err)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if got, _ := current.snapshot(); got != calls || factoryCalls.Load() != 1 {
		t.Fatalf("calls=%d factory=%d", got, factoryCalls.Load())
	}
	runtime.Close()
}

func TestRecoveringQueryInvalidatorCanceledWaiterDoesNotTouchAuthority(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	current := &blockingOwnedProductionQueryInvalidator{
		entered: entered,
		release: release,
	}
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		return current, nil
	}, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Invalidator().InvalidateOwnerTags(
			context.Background(), "owner.plugin", []string{"owner.plugin.items"},
		)
		firstDone <- err
	}()
	<-entered

	canceledCause := errors.New("worker lease canceled")
	ctx, cancel := context.WithCancelCause(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := runtime.Invalidator().InvalidateOwnerTags(
			ctx, "owner.plugin", []string{"owner.plugin.items"},
		)
		secondDone <- err
	}()
	cancel(canceledCause)
	close(release)

	if err := <-firstDone; err != nil {
		t.Fatalf("first invalidation err=%v", err)
	}
	if err := <-secondDone; !errors.Is(err, context.Canceled) || !errors.Is(err, canceledCause) {
		t.Fatalf("canceled waiter err=%v", err)
	}
	if calls, _ := current.snapshot(); calls != 1 {
		t.Fatalf("authority calls=%d, want 1", calls)
	}
	runtime.Close()
}

func TestRecoveringQueryInvalidatorShutdownJoinsTerminalAuthorityClose(t *testing.T) {
	closeEntered := make(chan struct{})
	closeRelease := make(chan struct{})
	current := &blockingOwnedProductionQueryInvalidator{
		err:          queryregistry.ErrCacheDurability,
		closeEntered: closeEntered,
		closeRelease: closeRelease,
	}
	runtime := newProductionQueryInvalidationRuntimeWithFactory(func(context.Context) (ownedProductionQueryInvalidator, error) {
		return current, nil
	}, nil)

	invalidateDone := make(chan error, 1)
	go func() {
		_, err := runtime.Invalidator().InvalidateOwnerTags(
			context.Background(), "owner.plugin", []string{"owner.plugin.items"},
		)
		invalidateDone <- err
	}()
	<-closeEntered

	closeDone := make(chan struct{})
	go func() {
		runtime.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("shutdown returned before terminal authority close completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(closeRelease)
	if err := <-invalidateDone; !errors.Is(err, queryregistry.ErrCacheDurability) {
		t.Fatalf("invalidation err=%v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join terminal authority close")
	}
	if _, closes := current.snapshot(); closes != 1 {
		t.Fatalf("authority closes=%d, want 1", closes)
	}
}

type fakeOwnedProductionQueryInvalidator struct {
	mu      sync.Mutex
	err     error
	rotated uint64
	calls   int
	closes  int
}

type blockingOwnedProductionQueryInvalidator struct {
	mu           sync.Mutex
	err          error
	calls        int
	closes       int
	entered      chan struct{}
	release      chan struct{}
	closeEntered chan struct{}
	closeRelease chan struct{}
}

func (f *blockingOwnedProductionQueryInvalidator) InvalidateOwnerTags(
	context.Context,
	string,
	[]string,
) (uint64, error) {
	f.mu.Lock()
	f.calls++
	entered := f.entered
	f.entered = nil
	release := f.release
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return 0, f.err
}

func (f *blockingOwnedProductionQueryInvalidator) Close(*slog.Logger) {
	f.mu.Lock()
	f.closes++
	entered := f.closeEntered
	f.closeEntered = nil
	release := f.closeRelease
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
}

func (f *blockingOwnedProductionQueryInvalidator) snapshot() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.closes
}

func (f *fakeOwnedProductionQueryInvalidator) InvalidateOwnerTags(
	context.Context,
	string,
	[]string,
) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rotated, f.err
}

func (f *fakeOwnedProductionQueryInvalidator) Close(*slog.Logger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
}

func (f *fakeOwnedProductionQueryInvalidator) snapshot() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.closes
}

// TestQueryResultCacheStageHandoffMatchesAssemblyOwnership 覆盖 wire→finish 拆分后的
// 真实所有权协议：HandOff 前 CloseUnlessHandedOff 必须关闭 Redis client；HandOff 后
// 不得关闭，以便 API.close 继续持有。
func TestQueryResultCacheStageHandoffMatchesAssemblyOwnership(t *testing.T) {
	t.Parallel()

	t.Run("stage_failure_closes_client", func(t *testing.T) {
		t.Parallel()
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
		t.Cleanup(func() { _ = client.Close() })
		runtime := &productionQueryResultCacheRuntime{client: client}
		ownership := newQueryResultCacheStageHandoff(runtime, nil)
		// 模拟 wireAPICoreStack 失败路径：defer CloseUnlessHandedOff 且未 HandOff。
		ownership.CloseUnlessHandedOff()
		if err := client.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
			t.Fatalf("expected closed client after stage failure close, ping=%v", err)
		}
	})

	t.Run("hand_off_keeps_client_for_next_stage", func(t *testing.T) {
		t.Parallel()
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
		t.Cleanup(func() { _ = client.Close() })
		runtime := &productionQueryResultCacheRuntime{client: client}
		// Stage A 成功移交（wireAPICoreStack → finishAPIHTTP）。
		stageA := newQueryResultCacheStageHandoff(runtime, nil)
		handed := stageA.HandOff()
		stageA.CloseUnlessHandedOff() // wire 函数返回时的 defer：不得关闭
		if handed != runtime {
			t.Fatalf("HandOff runtime mismatch: got %#v want %#v", handed, runtime)
		}
		if err := client.Ping(context.Background()).Err(); errors.Is(err, redis.ErrClosed) {
			t.Fatal("stage A CloseUnlessHandedOff closed client after HandOff")
		}
		// Stage B 承接后 defer；成功时再 HandOff 给 API.close。
		stageB := newQueryResultCacheStageHandoff(handed, nil)
		_ = stageB.HandOff()
		stageB.CloseUnlessHandedOff()
		if err := client.Ping(context.Background()).Err(); errors.Is(err, redis.ErrClosed) {
			t.Fatal("stage B CloseUnlessHandedOff closed client after HandOff to API.close")
		}
		// API.close 路径：最终关闭一次。
		handed.Close(nil)
		if err := client.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
			t.Fatalf("expected closed client after API.close, ping=%v", err)
		}
	})

	t.Run("double_close_is_safe", func(t *testing.T) {
		t.Parallel()
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
		runtime := &productionQueryResultCacheRuntime{client: client}
		ownership := newQueryResultCacheStageHandoff(runtime, nil)
		ownership.CloseUnlessHandedOff()
		ownership.CloseUnlessHandedOff()
		runtime.Close(nil)
	})
}
