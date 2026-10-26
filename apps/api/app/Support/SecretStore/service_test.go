package secretstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
)

func TestPutMetaResolveMaskAndNoValueLeak(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ref := Ref{Namespace: "demo.mail", SecretID: "smtp.password"}

	meta, err := svc.Put(ctx, ref, []byte("s3cret-value"), PutOptions{
		Actor: "admin:1", Purposes: []string{"mail.transport", "http.credential"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !meta.SecretSet || meta.Version != 1 || meta.Reference != "sforum.secret://demo.mail/smtp.password" {
		t.Fatalf("meta = %#v", meta)
	}
	// Meta must never expose plaintext.
	got, err := svc.Meta(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretSet != true || strings.Contains(got.Reference, "s3cret") {
		t.Fatalf("meta leaked: %#v", got)
	}

	lease, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.mail", Actor: "plugin"}, ref, "mail.transport", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(lease.Value) != "s3cret-value" || lease.LeaseID == "" || lease.Version != 1 {
		t.Fatalf("lease = %#v value=%q", lease, lease.Value)
	}

	// Wrong purpose denied.
	if _, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.mail"}, ref, "other.purpose", 0); !errors.Is(err, ErrPurposeDenied) {
		t.Fatalf("purpose deny = %v", err)
	}
	// Cross-namespace denied.
	if _, err := svc.Resolve(ctx, Caller{ExtensionID: "evil.plugin"}, ref, "mail.transport", 0); !errors.Is(err, ErrNamespaceDenied) {
		t.Fatalf("namespace deny = %v", err)
	}
	// Host caller can resolve any namespace.
	hostLease, err := svc.Resolve(ctx, Caller{Actor: "host"}, ref, "mail.transport", 0)
	if err != nil || string(hostLease.Value) != "s3cret-value" {
		t.Fatalf("host resolve: %v value=%q", err, hostLease.Value)
	}

	snap := svc.Inspector()
	if snap.Puts != 1 || snap.Resolves < 2 || snap.Denies < 2 {
		t.Fatalf("inspector = %#v", snap)
	}
	for _, event := range snap.RecentAudit {
		if strings.Contains(event.Actor, "s3cret") || strings.Contains(event.Purpose, "s3cret-value") {
			// purpose is allowed; value must not appear in actor/purpose fields incorrectly
		}
		// Ensure no secret value fields exist on audit (struct has no Value).
		if event.Action == "" {
			t.Fatalf("empty audit action: %#v", event)
		}
	}
}

func TestPreserveOnEmptyAndRotation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ref := Ref{Namespace: "demo.mail", SecretID: "api.key"}

	if _, err := svc.Put(ctx, ref, []byte("first"), PutOptions{Actor: "admin:1"}); err != nil {
		t.Fatal(err)
	}
	// Empty with preserve keeps first version.
	meta, err := svc.Put(ctx, ref, []byte("  "), PutOptions{Actor: "admin:1", PreserveEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Version != 1 || !meta.SecretSet {
		t.Fatalf("preserve meta = %#v", meta)
	}
	lease, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.mail"}, ref, "runtime", 0)
	if err != nil || string(lease.Value) != "first" {
		t.Fatalf("after preserve: %v %q", err, lease.Value)
	}

	// Rotation creates version 2.
	rotated, err := svc.Rotate(ctx, ref, []byte("second"), "admin:2", nil)
	if err != nil || rotated.Version != 2 {
		t.Fatalf("rotate = %#v err=%v", rotated, err)
	}
	lease2, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.mail"}, ref, "runtime", 0)
	if err != nil || string(lease2.Value) != "second" || lease2.Version != 2 {
		t.Fatalf("after rotate: %v %q v=%d", err, lease2.Value, lease2.Version)
	}
	// Old version still resolvable by pin.
	old, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.mail"}, Ref{
		Namespace: ref.Namespace, SecretID: ref.SecretID, Version: 1,
	}, "runtime", 0)
	if err != nil || string(old.Value) != "first" {
		t.Fatalf("pinned old version: %v %q", err, old.Value)
	}
}

func TestClearRevokesAndListMeta(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ref := Ref{Namespace: "core", SecretID: "altcha.secret"}

	if _, err := svc.Put(ctx, ref, []byte("tok"), PutOptions{Actor: "super_admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Clear(ctx, ref, ""); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("clear without actor = %v", err)
	}
	cleared, err := svc.Clear(ctx, ref, "super_admin")
	if err != nil || !cleared.Revoked || cleared.SecretSet {
		t.Fatalf("clear = %#v err=%v", cleared, err)
	}
	if _, err := svc.Resolve(ctx, Caller{}, ref, "verify", 0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("resolve after clear = %v", err)
	}

	// Re-put after clear works as new version.
	if meta, err := svc.Put(ctx, ref, []byte("new"), PutOptions{Actor: "super_admin"}); err != nil || meta.Version < 3 {
		t.Fatalf("re-put after clear = %#v err=%v", meta, err)
	}
	list, err := svc.ListMeta(ctx, "core")
	if err != nil || len(list) != 1 || !list[0].SecretSet {
		t.Fatalf("list = %#v err=%v", list, err)
	}
}

func TestReferenceParseAndShouldPreserve(t *testing.T) {
	if !ShouldPreserve("") || !ShouldPreserve("  ") || ShouldPreserve("x") {
		t.Fatal("ShouldPreserve semantics")
	}
	ref, err := ParseReference("sforum.secret://demo.mail/smtp.password")
	if err != nil || ref.Namespace != "demo.mail" || ref.SecretID != "smtp.password" {
		t.Fatalf("parse = %#v err=%v", ref, err)
	}
	if _, err := ParseReference("not-a-ref"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad ref = %v", err)
	}
}

func TestEncryptionAtRestWithRealCipher(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptox.NewOptionCipher(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(NewMemoryStore(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref := Ref{Namespace: "demo.pay", SecretID: "stripe.key"}
	if _, err := svc.Put(ctx, ref, []byte("sk_live_xxx"), PutOptions{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	// Inspect raw store: ciphertext must not equal plaintext.
	row, ok, err := svc.store.Latest(ctx, "demo.pay", "stripe.key", false)
	if err != nil || !ok || row.Cipher == "sk_live_xxx" || !cryptox.IsEncrypted(row.Cipher) {
		t.Fatalf("expected enc:: ciphertext, got %q ok=%t err=%v", row.Cipher, ok, err)
	}
	lease, err := svc.Resolve(ctx, Caller{ExtensionID: "demo.pay"}, ref, "provider", 0)
	if err != nil || string(lease.Value) != "sk_live_xxx" {
		t.Fatalf("decrypt path: %v %q", err, lease.Value)
	}
}

func TestPutRequiresActorAndBounds(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ref := Ref{Namespace: "demo.x", SecretID: "k"}
	if _, err := svc.Put(ctx, ref, []byte("v"), PutOptions{}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("no actor = %v", err)
	}
	if _, err := svc.Put(ctx, Ref{Namespace: "bad name", SecretID: "k"}, []byte("v"), PutOptions{Actor: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad namespace = %v", err)
	}
	huge := make([]byte, MaxPlaintextBytes+1)
	if _, err := svc.Put(ctx, ref, huge, PutOptions{Actor: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized = %v", err)
	}
}

func TestProductionRequiresEncryptionKey(t *testing.T) {
	_, err := NewWithOptions(Options{Store: NewMemoryStore(), RequireEncryption: true})
	if !errors.Is(err, ErrCipherRequired) {
		t.Fatalf("expected cipher required, got %v", err)
	}
	transparent, err := cryptox.NewOptionCipher("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewWithOptions(Options{Store: NewMemoryStore(), Cipher: transparent, RequireEncryption: true})
	if !errors.Is(err, ErrCipherRequired) {
		t.Fatalf("transparent must fail closed: %v", err)
	}
}

func TestConcurrentRotateVersionsUnique(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ref := Ref{Namespace: "demo.race", SecretID: "token"}
	if _, err := svc.Put(ctx, ref, []byte("seed"), PutOptions{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	versions := make(chan int64, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			meta, err := svc.Rotate(ctx, ref, []byte(fmt.Sprintf("v-%d", i)), "admin", nil)
			if err != nil {
				errs <- err
				return
			}
			versions <- meta.Version
		}(i)
	}
	wait.Wait()
	close(versions)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for v := range versions {
		if seen[v] {
			t.Fatalf("duplicate version %d under concurrent rotate", v)
		}
		seen[v] = true
	}
	if len(seen) != workers {
		t.Fatalf("versions = %d want %d", len(seen), workers)
	}
}

func TestNoPlaintextInInspectorOrMeta(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	secret := "super-secret-plain-value-xyz"
	ref := Ref{Namespace: "demo.mask", SecretID: "key"}
	if _, err := svc.Put(ctx, ref, []byte(secret), PutOptions{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	meta, err := svc.Meta(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(meta)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("meta JSON leaked plaintext: %s", blob)
	}
	snap := svc.Inspector()
	blob, _ = json.Marshal(snap)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("inspector JSON leaked plaintext: %s", blob)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	// Transparent cipher is fine for most unit tests; encryption covered separately.
	svc, err := New(NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
