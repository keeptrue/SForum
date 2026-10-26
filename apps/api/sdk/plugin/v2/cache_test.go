package pluginv2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	sdkCacheSchemaRef = "demo.cache.value@1"
	sdkCacheNamespace = "demo.cache.records"
)

func TestHostCacheLeaseKeepsExactBindingAndConsumesAtomicSet(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	parent := &protocolwire.RequestContext{
		Locale: "zh-CN", Actor: &protocolwire.Actor{UserId: 42},
		Extension: &protocolwire.ExtensionIdentity{ExtensionId: "forged"},
	}
	lease, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Parent: parent, Namespace: " DEMO.CACHE.RECORDS ", Key: "post:42", TTL: 300 * time.Millisecond,
	})
	if err != nil || !acquired || lease == nil || !time.Now().Before(lease.ExpiresAt()) {
		t.Fatalf("acquire = %#v, %t, %v", lease, acquired, err)
	}
	parent.Locale = "en-US"
	if err := lease.Renew(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	document := sdkCacheDocument(t, map[string]any{"title": "atomic"})
	revision, err := lease.SetAndRelease(context.Background(), CacheSetAndReleaseOptions{
		Value: document, Tags: []string{"demo.cache.tag.records"},
	})
	if err != nil || len(revision) != 64 {
		t.Fatalf("set and release revision=%q err=%v", revision, err)
	}
	if err := lease.Release(context.Background()); !errors.Is(err, ErrHostCacheLeaseConsumed) {
		t.Fatalf("replayed release = %v", err)
	}

	client.mu.Lock()
	acquireRequest := client.lastAcquire
	renewRequest := client.lastRenew
	setRequest := client.lastSet
	client.mu.Unlock()
	for name, requestContext := range map[string]*protocolwire.RequestContext{
		"acquire": acquireRequest.GetContext(), "renew": renewRequest.GetContext(), "set": setRequest.GetContext(),
	} {
		if requestContext.GetLocale() != "zh-CN" || requestContext.GetActor() != nil ||
			!proto.Equal(requestContext.GetExtension(), host.identity) {
			t.Fatalf("%s context drifted: %#v", name, requestContext)
		}
	}
	if acquireRequest.GetNamespace() != sdkCacheNamespace || renewRequest.GetNamespace() != sdkCacheNamespace ||
		setRequest.GetNamespace() != sdkCacheNamespace || acquireRequest.GetKey() != "post:42" ||
		renewRequest.GetLeaseToken() == "" || setRequest.GetLeaseToken() != renewRequest.GetLeaseToken() {
		t.Fatalf("lease binding acquire=%#v renew=%#v set=%#v", acquireRequest, renewRequest, setRequest)
	}
	client.mu.Lock()
	stored := cloneTypedDocument(client.value)
	client.mu.Unlock()
	if TypedDocumentValues(stored)["title"] != "atomic" {
		t.Fatalf("stored value = %#v", stored)
	}
}

func TestHostCacheLeaseReleaseAndContention(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	first, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "release", TTL: time.Second,
	})
	if err != nil || !acquired || first == nil {
		t.Fatalf("first acquire = %#v, %t, %v", first, acquired, err)
	}
	second, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "release", TTL: time.Second,
	})
	if err != nil || acquired || second != nil {
		t.Fatalf("contended acquire = %#v, %t, %v", second, acquired, err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Renew(context.Background(), time.Second); !errors.Is(err, ErrHostCacheLeaseConsumed) {
		t.Fatalf("renew after release = %v", err)
	}
	client.mu.Lock()
	releases := client.releaseCalls
	client.mu.Unlock()
	if releases != 1 {
		t.Fatalf("release calls = %d", releases)
	}
}

func TestHostCacheLeaseRedactsAndClearsOpaqueToken(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	lease, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "redacted", TTL: time.Second,
	})
	if err != nil || !acquired {
		t.Fatalf("acquire = %#v, %t, %v", lease, acquired, err)
	}
	token := client.token
	var logOutput bytes.Buffer
	slog.New(slog.NewTextHandler(&logOutput, nil)).Info("lease", "value", lease)
	for name, rendered := range map[string]string{
		"string": fmt.Sprint(lease), "go string": fmt.Sprintf("%#v", lease), "slog": logOutput.String(),
	} {
		if strings.Contains(rendered, token) || strings.Contains(rendered, "token:") {
			t.Fatalf("%s leaked token: %s", name, rendered)
		}
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease.mu.Lock()
	consumed, retainedToken := lease.consumed, lease.token
	lease.mu.Unlock()
	if !consumed || retainedToken != "" {
		t.Fatalf("consumed=%t retained token=%q", consumed, retainedToken)
	}
}

func TestHostCacheLeaseUnknownOutcomeCleansUpWithoutReplay(t *testing.T) {
	for _, operation := range []string{"release", "set_and_release"} {
		t.Run(operation, func(t *testing.T) {
			client := newSDKMemoryCacheClient()
			host := newSDKCacheHost(client)
			lease, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
				Parent:    &protocolwire.RequestContext{Deadline: timestamppb.New(time.Now().Add(-time.Minute))},
				Namespace: sdkCacheNamespace, Key: operation, TTL: time.Second,
			})
			if err != nil || !acquired {
				t.Fatalf("acquire = %#v, %t, %v", lease, acquired, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if operation == "release" {
				err = lease.Release(ctx)
			} else {
				_, err = lease.SetAndRelease(ctx, CacheSetAndReleaseOptions{
					Value: sdkCacheDocument(t, map[string]any{"cleanup": true}),
				})
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("unknown outcome = %v", err)
			}
			if err := lease.Release(context.Background()); !errors.Is(err, ErrHostCacheLeaseConsumed) {
				t.Fatalf("replay = %v", err)
			}
			lease.mu.Lock()
			retainedToken := lease.token
			lease.mu.Unlock()
			client.mu.Lock()
			locked, releases, sets := client.locked, client.releaseCalls, client.setCalls
			cleanupBounded := client.cleanupBounded
			cleanupDeadline := client.lastRelease.GetContext().GetDeadline()
			client.mu.Unlock()
			if retainedToken != "" || locked || !cleanupBounded || releases == 0 ||
				cleanupDeadline == nil || !cleanupDeadline.IsValid() || !time.Now().Before(cleanupDeadline.AsTime()) ||
				(operation == "release" && releases != 2) || (operation == "set_and_release" && sets != 1) {
				t.Fatalf("token=%q locked=%t cleanup_bounded=%t cleanup_deadline=%v releases=%d sets=%d",
					retainedToken, locked, cleanupBounded, cleanupDeadline, releases, sets)
			}
		})
	}
}

func TestHostCacheOperationWrappers(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	parent := &protocolwire.RequestContext{Locale: "zh-CN", Actor: &protocolwire.Actor{UserId: 42}}
	document := sdkCacheDocument(t, map[string]any{"title": "wrapped"})
	revision, err := host.SetCache(context.Background(), CacheSetOptions{
		Parent: parent, Namespace: " DEMO.CACHE.RECORDS ", Key: "post:42", Value: document,
		Tags: []string{"demo.cache.tag.records"},
	})
	if err != nil || !validCacheOpaqueToken(revision) {
		t.Fatalf("set revision=%q err=%v", revision, err)
	}
	result, err := host.GetCache(context.Background(), CacheGetOptions{
		Parent: parent, Namespace: sdkCacheNamespace, Key: "post:42", ValueSchema: sdkCacheSchemaRef,
	})
	if err != nil || !result.Found || result.Revision != revision ||
		TypedDocumentValues(result.Value)["title"] != "wrapped" {
		t.Fatalf("get=%#v err=%v", result, err)
	}
	counter, err := host.IncrementCache(context.Background(), CacheIncrementOptions{
		Parent: parent, Namespace: sdkCacheNamespace, Key: "views:42",
	})
	if err != nil || counter != 1 {
		t.Fatalf("increment=%d err=%v", counter, err)
	}
	deleted, err := host.DeleteCache(context.Background(), CacheDeleteOptions{
		Parent: parent, Namespace: sdkCacheNamespace, Key: "post:42",
	})
	if err != nil || !deleted {
		t.Fatalf("delete=%t err=%v", deleted, err)
	}
	if _, err := host.SetCache(context.Background(), CacheSetOptions{
		Parent: parent, Namespace: sdkCacheNamespace, Key: "tagged", Value: document,
		Tags: []string{"demo.cache.tag.records"},
	}); err != nil {
		t.Fatal(err)
	}
	invalidated, err := host.InvalidateCacheTags(context.Background(), CacheInvalidateTagsOptions{
		Parent: parent, Namespace: sdkCacheNamespace, Tags: []string{"demo.cache.tag.records"},
	})
	if err != nil || invalidated != 1 {
		t.Fatalf("invalidated=%d err=%v", invalidated, err)
	}
	client.mu.Lock()
	requests := []*protocolwire.RequestContext{
		client.lastDirectSet.GetContext(), client.lastGet.GetContext(), client.lastIncrement.GetContext(),
		client.lastDelete.GetContext(), client.lastInvalidate.GetContext(),
	}
	setTTL, incrementTTL := client.lastDirectSet.GetTtl().AsDuration(), client.lastIncrement.GetTtl().AsDuration()
	setNamespace := client.lastDirectSet.GetNamespace()
	client.mu.Unlock()
	if setTTL != DefaultHostCacheTTL || incrementTTL != DefaultHostCacheTTL || setNamespace != sdkCacheNamespace {
		t.Fatalf("set ttl=%s increment ttl=%s namespace=%q", setTTL, incrementTTL, setNamespace)
	}
	for index, request := range requests {
		if request.GetLocale() != "zh-CN" || request.GetActor() != nil || !proto.Equal(request.GetExtension(), host.identity) {
			t.Fatalf("request %d context drifted: %#v", index, request)
		}
	}
	if _, err := host.InvalidateCacheTags(context.Background(), CacheInvalidateTagsOptions{
		Namespace: sdkCacheNamespace,
	}); !errors.Is(err, ErrHostCacheInvalidArgument) {
		t.Fatalf("empty invalidation = %v", err)
	}
}

func TestHostCacheConditionalWritesMapConflictsAndReleaseLeases(t *testing.T) {
	client := newSDKMemoryCacheClient()
	client.found = true
	client.value = sdkCacheDocument(t, map[string]any{"value": "original"})
	host := newSDKCacheHost(client)

	_, err := host.SetCache(context.Background(), CacheSetOptions{
		Namespace: sdkCacheNamespace, Key: "conditional", Value: sdkCacheDocument(t, map[string]any{"value": "stale"}),
		ExpectedRevision: strings.Repeat("b", 64),
	})
	var cacheErr *HostCacheError
	if !errors.As(err, &cacheErr) || cacheErr.Reason != "host.cache_revision_conflict" {
		t.Fatalf("stale direct set = %#v", err)
	}
	client.mu.Lock()
	valueAfterConflict := cloneTypedDocument(client.value)
	currentRevision := client.revision
	client.mu.Unlock()
	if TypedDocumentValues(valueAfterConflict)["value"] != "original" {
		t.Fatalf("stale direct set changed value: %#v", valueAfterConflict)
	}

	revision, err := host.SetCache(context.Background(), CacheSetOptions{
		Namespace: sdkCacheNamespace, Key: "conditional", Value: sdkCacheDocument(t, map[string]any{"value": "updated"}),
		ExpectedRevision: currentRevision,
	})
	if err != nil || !validCacheOpaqueToken(revision) || revision == currentRevision {
		t.Fatalf("exact direct set revision=%q err=%v", revision, err)
	}

	lease, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "conditional-lock", TTL: time.Second,
	})
	if err != nil || !acquired {
		t.Fatalf("acquire = %#v, %t, %v", lease, acquired, err)
	}
	_, err = lease.SetAndRelease(context.Background(), CacheSetAndReleaseOptions{
		Value: sdkCacheDocument(t, map[string]any{"value": "locked"}), ExpectedRevision: strings.Repeat("c", 64),
	})
	if !errors.As(err, &cacheErr) || cacheErr.Reason != "host.cache_revision_conflict" {
		t.Fatalf("stale atomic set = %#v", err)
	}
	if err := lease.Release(context.Background()); !errors.Is(err, ErrHostCacheLeaseConsumed) {
		t.Fatalf("stale atomic set replay = %v", err)
	}
	client.mu.Lock()
	locked := client.locked
	forwardedRevision := client.lastSet.GetExpectedRevision()
	client.mu.Unlock()
	if locked || forwardedRevision != strings.Repeat("c", 64) {
		t.Fatalf("locked=%t expected_revision=%q", locked, forwardedRevision)
	}
}

func TestHostCacheAtomicCommitCannotOutliveLease(t *testing.T) {
	client := newSDKMemoryCacheClient()
	client.setDelay = 300 * time.Millisecond
	host := newSDKCacheHost(client)
	lease, acquired, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "bounded-commit", TTL: 100 * time.Millisecond,
	})
	if err != nil || !acquired {
		t.Fatalf("acquire = %#v, %t, %v", lease, acquired, err)
	}
	started := time.Now()
	_, err = lease.SetAndRelease(context.Background(), CacheSetAndReleaseOptions{
		Value: sdkCacheDocument(t, map[string]any{"value": "late"}),
	})
	if elapsed := time.Since(started); !errors.Is(err, ErrHostCacheLockExpired) || elapsed > 250*time.Millisecond {
		t.Fatalf("atomic commit err=%v elapsed=%s", err, elapsed)
	}
	client.mu.Lock()
	locked, found := client.locked, client.found
	client.mu.Unlock()
	if locked || found {
		t.Fatalf("expired atomic commit locked=%t found=%t", locked, found)
	}
}

func TestHostRememberCacheHitsAndDoubleChecksBeforeLoader(t *testing.T) {
	t.Run("initial hit", func(t *testing.T) {
		client := newSDKMemoryCacheClient()
		client.found = true
		client.value = sdkCacheDocument(t, map[string]any{"source": "initial"})
		host := newSDKCacheHost(client)
		var loads atomic.Int64
		result, err := host.RememberCache(context.Background(), CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "initial-hit", ValueSchema: sdkCacheSchemaRef,
		}, func(context.Context) (*protocolwire.TypedDocument, error) {
			loads.Add(1)
			return nil, errors.New("loader must not run")
		})
		client.mu.Lock()
		getCalls, acquireCalls := client.getCalls, client.acquireCalls
		client.mu.Unlock()
		if err != nil || result.Loaded || !validCacheOpaqueToken(result.Revision) ||
			TypedDocumentValues(result.Value)["source"] != "initial" ||
			loads.Load() != 0 || getCalls != 1 || acquireCalls != 0 {
			t.Fatalf("result=%#v err=%v loads=%d get=%d acquire=%d",
				result, err, loads.Load(), getCalls, acquireCalls)
		}
	})

	t.Run("post acquire double check", func(t *testing.T) {
		client := newSDKMemoryCacheClient()
		client.publishOnAcquire = sdkCacheDocument(t, map[string]any{"source": "previous-owner"})
		host := newSDKCacheHost(client)
		var loads atomic.Int64
		result, err := host.RememberCache(context.Background(), CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "double-check", ValueSchema: sdkCacheSchemaRef,
		}, func(context.Context) (*protocolwire.TypedDocument, error) {
			loads.Add(1)
			return nil, errors.New("loader must not run")
		})
		client.mu.Lock()
		acquireCalls, releaseCalls, setCalls := client.acquireCalls, client.releaseCalls, client.setCalls
		client.mu.Unlock()
		if err != nil || result.Loaded || !validCacheOpaqueToken(result.Revision) ||
			TypedDocumentValues(result.Value)["source"] != "previous-owner" ||
			loads.Load() != 0 || acquireCalls != 1 || releaseCalls != 1 || setCalls != 0 {
			t.Fatalf("result=%#v err=%v loads=%d acquire=%d release=%d set=%d",
				result, err, loads.Load(), acquireCalls, releaseCalls, setCalls)
		}
	})
}

func TestHostRememberCacheConcurrentSingleFlightUsesOneLoader(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	var loads atomic.Int64
	loader := func(ctx context.Context) (*protocolwire.TypedDocument, error) {
		loads.Add(1)
		timer := time.NewTimer(180 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
		return sdkCacheDocument(t, map[string]any{"value": "shared"}), nil
	}
	options := CacheRememberOptions{
		Namespace: sdkCacheNamespace, Key: "single-flight", ValueSchema: sdkCacheSchemaRef,
		TTL: time.Minute, LockTTL: 120 * time.Millisecond, Wait: 2 * time.Second, Backoff: 2 * time.Millisecond,
	}
	const callers = 32
	start := make(chan struct{})
	results := make(chan CacheRememberResult, callers)
	errorsCh := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := host.RememberCache(context.Background(), options, loader)
			results <- result
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded := 0
	for result := range results {
		if TypedDocumentValues(result.Value)["value"] != "shared" || !validCacheOpaqueToken(result.Revision) {
			t.Fatalf("result = %#v", result)
		}
		if result.Loaded {
			loaded++
		}
	}
	client.mu.Lock()
	acquireCalls, renewCalls := client.acquireCalls, client.renewCalls
	setCalls, releaseCalls, locked := client.setCalls, client.releaseCalls, client.locked
	client.mu.Unlock()
	// A waiter may win the lock immediately after the first atomic commit; its
	// mandatory double-check then releases without running another loader.
	if loads.Load() != 1 || loaded != 1 || acquireCalls <= 1 || renewCalls == 0 || setCalls != 1 ||
		releaseCalls >= callers || locked {
		t.Fatalf("loads=%d loaded=%d acquire=%d renew=%d set=%d release=%d locked=%t",
			loads.Load(), loaded, acquireCalls, renewCalls, setCalls, releaseCalls, locked)
	}
}

func TestHostRememberCacheCancelsExpiredOwnerWhileRenewIsBlocked(t *testing.T) {
	client := newSDKMemoryCacheClient()
	client.renewDelay = 300 * time.Millisecond
	client.renewStarted = make(chan struct{}, 1)
	host := newSDKCacheHost(client)
	var active atomic.Int64
	var overlap atomic.Bool
	firstDone := make(chan error, 1)
	go func() {
		_, err := host.RememberCache(context.Background(), CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "expiring-owner", ValueSchema: sdkCacheSchemaRef,
			LockTTL: 100 * time.Millisecond, Wait: time.Second, Backoff: 2 * time.Millisecond,
		}, func(loaderCtx context.Context) (*protocolwire.TypedDocument, error) {
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			defer active.Add(-1)
			<-loaderCtx.Done()
			return nil, context.Cause(loaderCtx)
		})
		firstDone <- err
	}()
	select {
	case <-client.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("renew did not start")
	}
	time.Sleep(120 * time.Millisecond)
	result, err := host.RememberCache(context.Background(), CacheRememberOptions{
		Namespace: sdkCacheNamespace, Key: "expiring-owner", ValueSchema: sdkCacheSchemaRef,
		LockTTL: time.Second, Wait: time.Second, Backoff: 2 * time.Millisecond,
	}, func(context.Context) (*protocolwire.TypedDocument, error) {
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		defer active.Add(-1)
		return sdkCacheDocument(t, map[string]any{"owner": "replacement"}), nil
	})
	if err != nil || !result.Loaded || TypedDocumentValues(result.Value)["owner"] != "replacement" {
		t.Fatalf("replacement result=%#v err=%v", result, err)
	}
	select {
	case err := <-firstDone:
		if !errors.Is(err, ErrHostCacheLockExpired) {
			t.Fatalf("expired owner = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired owner did not stop")
	}
	if overlap.Load() {
		t.Fatal("loaders overlapped after the first lease expired")
	}
}

func TestHostRememberCacheWaitIsBoundedAndNeverRunsContendedLoader(t *testing.T) {
	client := newSDKMemoryCacheClient()
	client.locked = true
	host := newSDKCacheHost(client)
	var loads atomic.Int64
	started := time.Now()
	_, err := host.RememberCache(context.Background(), CacheRememberOptions{
		Namespace: sdkCacheNamespace, Key: "contended", ValueSchema: sdkCacheSchemaRef,
		LockTTL: time.Second, Wait: 40 * time.Millisecond, Backoff: 2 * time.Millisecond,
	}, func(context.Context) (*protocolwire.TypedDocument, error) {
		loads.Add(1)
		return sdkCacheDocument(t, map[string]any{"unexpected": true}), nil
	})
	elapsed := time.Since(started)
	client.mu.Lock()
	acquireCalls := client.acquireCalls
	client.mu.Unlock()
	if !errors.Is(err, ErrHostCacheLockContended) || loads.Load() != 0 || acquireCalls < 2 ||
		elapsed < 30*time.Millisecond || elapsed > time.Second {
		t.Fatalf("err=%v loads=%d acquire=%d elapsed=%s", err, loads.Load(), acquireCalls, elapsed)
	}
}

func TestHostRememberCacheWaitBoundsContentionRPCsAndPreservesCallerCause(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*sdkMemoryCacheClient)
	}{
		{
			name: "initial get",
			configure: func(client *sdkMemoryCacheClient) {
				client.getDelay = 250 * time.Millisecond
			},
		},
		{
			name: "acquire",
			configure: func(client *sdkMemoryCacheClient) {
				client.locked = true
				client.acquireDelay = 250 * time.Millisecond
			},
		},
		{
			name: "post contention get",
			configure: func(client *sdkMemoryCacheClient) {
				client.locked = true
				client.getDelayAfter = 1
				client.getDelay = 250 * time.Millisecond
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newSDKMemoryCacheClient()
			test.configure(client)
			started := time.Now()
			_, err := newSDKCacheHost(client).RememberCache(context.Background(), CacheRememberOptions{
				Namespace: sdkCacheNamespace, Key: "slow-" + test.name, ValueSchema: sdkCacheSchemaRef,
				LockTTL: time.Second, Wait: 40 * time.Millisecond, Backoff: 2 * time.Millisecond,
			}, func(context.Context) (*protocolwire.TypedDocument, error) {
				return sdkCacheDocument(t, map[string]any{"unexpected": true}), nil
			})
			if elapsed := time.Since(started); !errors.Is(err, ErrHostCacheLockContended) || elapsed > 200*time.Millisecond {
				t.Fatalf("err=%v elapsed=%s", err, elapsed)
			}
		})
	}

	t.Run("earlier caller cause", func(t *testing.T) {
		client := newSDKMemoryCacheClient()
		client.locked = true
		client.acquireDelay = 250 * time.Millisecond
		callerErr := errors.New("caller stopped waiting")
		ctx, cancel := context.WithCancelCause(context.Background())
		t.Cleanup(func() { cancel(nil) })
		time.AfterFunc(30*time.Millisecond, func() { cancel(callerErr) })
		_, err := newSDKCacheHost(client).RememberCache(ctx, CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "caller-cause", ValueSchema: sdkCacheSchemaRef,
			LockTTL: time.Second, Wait: 500 * time.Millisecond,
		}, func(context.Context) (*protocolwire.TypedDocument, error) {
			return sdkCacheDocument(t, map[string]any{"unexpected": true}), nil
		})
		if !errors.Is(err, callerErr) || errors.Is(err, ErrHostCacheLockContended) {
			t.Fatalf("cause = %v", err)
		}
	})
}

func TestHostRememberCacheLoaderFailureAndCancellationReleaseLease(t *testing.T) {
	t.Run("loader error", func(t *testing.T) {
		client := newSDKMemoryCacheClient()
		host := newSDKCacheHost(client)
		loaderErr := errors.New("loader failed")
		_, err := host.RememberCache(context.Background(), CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "loader-error", ValueSchema: sdkCacheSchemaRef, LockTTL: time.Second,
		}, func(context.Context) (*protocolwire.TypedDocument, error) { return nil, loaderErr })
		client.mu.Lock()
		releaseCalls, setCalls, locked := client.releaseCalls, client.setCalls, client.locked
		client.mu.Unlock()
		if !errors.Is(err, loaderErr) || releaseCalls != 1 || setCalls != 0 || locked {
			t.Fatalf("err=%v release=%d set=%d locked=%t", err, releaseCalls, setCalls, locked)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		client := newSDKMemoryCacheClient()
		host := newSDKCacheHost(client)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := host.RememberCache(ctx, CacheRememberOptions{
			Namespace: sdkCacheNamespace, Key: "cancel", ValueSchema: sdkCacheSchemaRef, LockTTL: time.Second,
		}, func(loaderCtx context.Context) (*protocolwire.TypedDocument, error) {
			<-loaderCtx.Done()
			return nil, context.Cause(loaderCtx)
		})
		client.mu.Lock()
		releaseCalls, locked := client.releaseCalls, client.locked
		client.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) || releaseCalls != 1 || locked {
			t.Fatalf("err=%v release=%d locked=%t", err, releaseCalls, locked)
		}
	})
}

func TestHostRememberCacheRenewFailureCancelsLoader(t *testing.T) {
	client := newSDKMemoryCacheClient()
	client.renewError = &protocolwire.ErrorDetail{
		Code:   protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
		Reason: "host.cache_lock_not_owned", Message: "The cache lock is not owned.",
	}
	host := newSDKCacheHost(client)
	_, err := host.RememberCache(context.Background(), CacheRememberOptions{
		Namespace: sdkCacheNamespace, Key: "renew-failure", ValueSchema: sdkCacheSchemaRef,
		LockTTL: 100 * time.Millisecond,
	}, func(loaderCtx context.Context) (*protocolwire.TypedDocument, error) {
		<-loaderCtx.Done()
		return nil, context.Cause(loaderCtx)
	})
	var cacheErr *HostCacheError
	if !errors.As(err, &cacheErr) || cacheErr.Reason != "host.cache_lock_not_owned" ||
		!errors.Is(err, ErrHostCacheRemoteResponse) {
		t.Fatalf("renew error = %#v", err)
	}
}

func TestHostCacheRejectsInvalidArgumentsAndResponseIdentity(t *testing.T) {
	client := newSDKMemoryCacheClient()
	host := newSDKCacheHost(client)
	if _, _, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: "", Key: "value", TTL: time.Second,
	}); !errors.Is(err, ErrHostCacheInvalidArgument) {
		t.Fatalf("invalid acquire = %v", err)
	}
	client.forgeResponseIdentity = true
	if _, _, err := host.AcquireCacheLock(context.Background(), CacheLockOptions{
		Namespace: sdkCacheNamespace, Key: "value", TTL: time.Second,
	}); !errors.Is(err, ErrHostCacheResponseInvalid) {
		t.Fatalf("forged response = %v", err)
	}
	client.mu.Lock()
	locked, releases, cleanupBounded := client.locked, client.releaseCalls, client.cleanupBounded
	client.mu.Unlock()
	if locked || releases != 1 || !cleanupBounded {
		t.Fatalf("forged acquired response cleanup locked=%t releases=%d bounded=%t", locked, releases, cleanupBounded)
	}
}

func TestHostCacheErrorPreservesStableCodeWithoutLeakingMessage(t *testing.T) {
	tests := []struct {
		code protocolwire.ErrorCode
		want error
	}{
		{code: protocolwire.ErrorCode_ERROR_CODE_CANCELLED, want: context.Canceled},
		{code: protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, want: context.DeadlineExceeded},
	}
	for _, test := range tests {
		err := hostCacheErrorFromDetail(&protocolwire.ErrorDetail{
			Code: test.code, Reason: "host.cache_safe_reason", Message: "redis-password-must-not-surface",
		})
		var cacheErr *HostCacheError
		if !errors.Is(err, ErrHostCacheRemoteResponse) || !errors.Is(err, test.want) ||
			!errors.As(err, &cacheErr) || strings.Contains(err.Error(), "password") ||
			strings.Contains(fmt.Sprintf("%#v", cacheErr), "password") {
			t.Fatalf("code=%s err=%v", test.code, err)
		}
	}
}
