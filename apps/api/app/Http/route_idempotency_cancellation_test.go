package http

import (
	"context"
	"strings"
	"testing"
	"time"

	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRequiredRouteReplayCompletionOutlivesCallerAcrossCASWindows(t *testing.T) {
	for _, test := range []struct {
		name              string
		cancelBeforeCall  bool
		applyBeforeSignal bool
	}{
		{name: "cancel before CAS", cancelBeforeCall: true},
		{name: "cancel while CAS is pending"},
		{name: "cancel after CAS applied", applyBeforeSignal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &routeCompletionBarrierBackend{
				MemoryBackend: idempotency.NewMemoryBackend(), entered: make(chan struct{}), release: make(chan struct{}),
				applyBeforeSignal: test.applyBeforeSignal,
			}
			cipher, err := idempotency.NewRequiredReplayCipher(strings.Repeat("ab", 32))
			if err != nil {
				t.Fatal(err)
			}
			store := idempotency.NewStore(backend, idempotency.DefaultTTL).WithRequiredReplayCipher(cipher)
			scope := idempotency.RequiredReplayScope{
				ActorScope: "actor:42:cookie", ExtensionID: "cancel.plugin", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("a", 64), RouteID: "cancel.plugin.create",
				ContractVersion: "cancel.plugin.create@1", Method: "POST",
			}
			binding := idempotency.RequiredReplayBinding{
				Fingerprint: strings.Repeat("b", 64), PlanDigest: strings.Repeat("c", 64),
			}
			key := strings.ReplaceAll(test.name, " ", "-")
			storeLease, replay, err := store.BeginRequiredReplayBound(context.Background(), scope, key, binding)
			if err != nil || replay != nil {
				t.Fatalf("lease=%#v replay=%#v error=%v", storeLease, replay, err)
			}
			lease := &requiredRouteIdempotencyLease{store: store, lease: storeLease}
			completion := routes.RouteIdempotencyCompletion{
				Response:              routes.DispatchResponse{Status: 201, Body: []byte(`{"created":true}`)},
				ResponseContractKnown: true,
			}
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancelBeforeCall {
				cancel()
				close(backend.release)
				if err := lease.Complete(ctx, completion); err != nil {
					t.Fatal(err)
				}
			} else {
				completed := make(chan error, 1)
				go func() { completed <- lease.Complete(ctx, completion) }()
				select {
				case <-backend.entered:
				case <-time.After(time.Second):
					t.Fatal("completion CAS did not reach the barrier")
				}
				if test.applyBeforeSignal {
					_, stored, beginErr := store.BeginRequiredReplayBound(context.Background(), scope, key, binding)
					if beginErr != nil || stored == nil || stored.Status != 201 {
						t.Fatalf("applied replay=%#v error=%v", stored, beginErr)
					}
				}
				cancel()
				select {
				case completeErr := <-completed:
					t.Fatalf("caller cancellation ended detached completion before release: %v", completeErr)
				default:
				}
				close(backend.release)
				select {
				case completeErr := <-completed:
					if completeErr != nil {
						t.Fatal(completeErr)
					}
				case <-time.After(time.Second):
					t.Fatal("completion CAS did not finish")
				}
			}

			if backend.ctxErrBefore != nil || backend.ctxErrAfter != nil || !backend.hasDeadline {
				t.Fatalf("CAS context before=%v after=%v deadline=%t", backend.ctxErrBefore, backend.ctxErrAfter, backend.hasDeadline)
			}
			_, stored, err := store.BeginRequiredReplayBound(context.Background(), scope, key, binding)
			if err != nil || stored == nil || stored.Status != 201 || string(stored.Body) != `{"created":true}` {
				t.Fatalf("stored replay=%#v error=%v", stored, err)
			}
		})
	}
}

type routeCompletionBarrierBackend struct {
	*idempotency.MemoryBackend
	entered           chan struct{}
	release           chan struct{}
	applyBeforeSignal bool
	ctxErrBefore      error
	ctxErrAfter       error
	hasDeadline       bool
}

func (b *routeCompletionBarrierBackend) CompareAndSwap(
	ctx context.Context,
	key string,
	expected []byte,
	replacement []byte,
	ttl time.Duration,
) (bool, error) {
	b.ctxErrBefore = ctx.Err()
	_, b.hasDeadline = ctx.Deadline()
	if b.applyBeforeSignal {
		swapped, err := b.MemoryBackend.CompareAndSwap(ctx, key, expected, replacement, ttl)
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		b.ctxErrAfter = ctx.Err()
		return swapped, err
	}
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	b.ctxErrAfter = ctx.Err()
	return b.MemoryBackend.CompareAndSwap(ctx, key, expected, replacement, ttl)
}
