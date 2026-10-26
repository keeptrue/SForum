package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentitySessionPolicyEffectGatePrefersWriter(t *testing.T) {
	var gate identitySessionPolicyEffectGate
	if err := gate.lockRead(t.Context()); err != nil {
		t.Fatal(err)
	}
	var initialRelease sync.Once
	releaseInitial := func() { initialRelease.Do(gate.unlockRead) }
	t.Cleanup(releaseInitial)

	writerEntered := make(chan struct{})
	writerRelease := make(chan struct{})
	var writerReleaseOnce sync.Once
	releaseWriter := func() { writerReleaseOnce.Do(func() { close(writerRelease) }) }
	t.Cleanup(releaseWriter)
	writerResult := make(chan error, 1)
	go func() {
		if err := gate.lockWrite(t.Context()); err != nil {
			writerResult <- err
			return
		}
		close(writerEntered)
		<-writerRelease
		gate.unlockWrite()
		writerResult <- nil
	}()
	waitForSessionPolicyGateWriter(t, &gate)

	readerEntered := make(chan struct{})
	readerResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := gate.lockRead(ctx); err != nil {
			readerResult <- err
			return
		}
		close(readerEntered)
		gate.unlockRead()
		readerResult <- nil
	}()
	select {
	case <-readerEntered:
		t.Fatal("late effect crossed a queued Session Policy writer")
	default:
	}

	releaseInitial()
	awaitSessionPolicyGateSignal(t, writerEntered, "writer admission")
	select {
	case <-readerEntered:
		t.Fatal("late effect crossed the active Session Policy writer")
	default:
	}
	releaseWriter()
	if err := awaitSessionPolicyGateResult(t, writerResult, "writer"); err != nil {
		t.Fatal(err)
	}
	awaitSessionPolicyGateSignal(t, readerEntered, "late effect admission")
	if err := awaitSessionPolicyGateResult(t, readerResult, "late effect"); err != nil {
		t.Fatal(err)
	}
}

func TestIdentitySessionPolicyEffectGateWaitIsCancelable(t *testing.T) {
	var gate identitySessionPolicyEffectGate
	if err := gate.lockRead(t.Context()); err != nil {
		t.Fatal(err)
	}
	var readRelease sync.Once
	releaseRead := func() { readRelease.Do(gate.unlockRead) }
	t.Cleanup(releaseRead)

	ctx, cancel := context.WithCancel(t.Context())
	writerResult := make(chan error, 1)
	go func() { writerResult <- gate.lockWrite(ctx) }()
	waitForSessionPolicyGateWriter(t, &gate)
	cancel()
	if err := awaitSessionPolicyGateResult(t, writerResult, "canceled writer"); !errors.Is(err, context.Canceled) {
		t.Fatalf("writer wait error = %v", err)
	}
	releaseRead()
	if !gate.TryRLock() {
		t.Fatal("canceled writer kept later effects blocked")
	}
	gate.RUnlock()
	if !gate.TryLock() {
		t.Fatal("effect gate remained held after canceled writer")
	}
	gate.Unlock()
}

func TestIdentitySessionPolicyEffectContextRejectsMutationReentry(t *testing.T) {
	store, err := NewPostgresIdentitySessionPolicyStore(&pgxpool.Pool{}, identityregistry.New())
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewPostgresIdentitySessionPolicyStore(&pgxpool.Pool{}, identityregistry.New())
	if err != nil {
		t.Fatal(err)
	}
	effectCtx, endEffect := beginSessionPolicyEffectContext(t.Context(), store)
	called := false
	err = store.runIdentitySessionPolicyMutation(
		effectCtx,
		func() error { called = true; return nil },
	)
	if !errors.Is(err, ErrIdentitySessionPolicyInvalid) || called {
		t.Fatalf("reentrant mutation called=%t err=%v", called, err)
	}
	if err := other.runIdentitySessionPolicyMutation(effectCtx, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("other store mutation called=%t err=%v", called, err)
	}

	endEffect()
	called = false
	if err := store.runIdentitySessionPolicyMutation(effectCtx, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("expired effect context mutation called=%t err=%v", called, err)
	}
}

func waitForSessionPolicyGateWriter(t *testing.T, gate *identitySessionPolicyEffectGate) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		gate.mu.Lock()
		waiting := gate.waitingWriters
		gate.mu.Unlock()
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Session Policy writer did not queue")
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitSessionPolicyGateSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitSessionPolicyGateResult(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}
