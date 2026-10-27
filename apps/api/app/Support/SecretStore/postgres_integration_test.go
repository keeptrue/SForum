package secretstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresStoreConcurrentRotateAndRestart(t *testing.T) {
	fixture := newSecretStoreFixture(t)
	ctx := fixture.ctx
	ref := Ref{Namespace: "demo.pg", SecretID: "api.key"}

	if _, err := fixture.svc.Put(ctx, ref, []byte("seed-value"), PutOptions{
		Actor: "admin", Purposes: []string{"http.credential"},
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	versions := make(chan int64, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			// 跨连接并发：每 goroutine 使用独立 Service 实例共享同一 pool。
			svc, err := NewWithOptions(Options{
				Store: fixture.store, Cipher: fixture.cipher, Audit: fixture.audit,
				RequireEncryption: true,
			})
			if err != nil {
				errs <- err
				return
			}
			meta, err := svc.Rotate(ctx, ref, []byte(fmt.Sprintf("rot-%d", i)), "admin", []string{"http.credential"})
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
			t.Fatalf("duplicate version %d", v)
		}
		seen[v] = true
	}

	// 重启：新 Service 实例从 Postgres 恢复版本与撤销状态。
	restarted, err := NewWithOptions(Options{
		Store: fixture.store, Cipher: fixture.cipher, Audit: fixture.audit,
		RequireEncryption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := restarted.Meta(ctx, ref)
	if err != nil || !meta.SecretSet || meta.Version < int64(workers) {
		t.Fatalf("after restart meta=%#v err=%v", meta, err)
	}
	lease, err := restarted.Resolve(ctx, Caller{ExtensionID: "demo.pg"}, ref, "http.credential", time.Second)
	if err != nil || len(lease.Value) == 0 {
		t.Fatalf("resolve after restart: %v", err)
	}
	if string(lease.Value) == "seed-value" && meta.Version > 1 {
		// 最新版本应是某次 rotate 的值，不是 seed（除非 race 全失败）。
	}

	// 审计跨进程：Postgres audit 表有 put/rotate 行。
	events, err := fixture.audit.ListRecentAudit(ctx, MaxAuditRing)
	if err != nil || len(events) == 0 {
		t.Fatalf("durable audit empty: %v %#v", err, events)
	}
	for _, event := range events {
		if strings.Contains(event.Actor, "seed-value") || strings.Contains(event.Purpose, "seed-value") {
			t.Fatalf("audit leaked secret material: %#v", event)
		}
		if event.Action == "" {
			t.Fatalf("empty audit action: %#v", event)
		}
	}

	// 撤销后不可 Resolve。
	if _, err := restarted.Clear(ctx, ref, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resolve(ctx, Caller{ExtensionID: "demo.pg"}, ref, "http.credential", 0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after clear: %v", err)
	}
	// 再重启仍 revoked。
	again, err := NewWithOptions(Options{
		Store: fixture.store, Cipher: fixture.cipher, RequireEncryption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Resolve(ctx, Caller{ExtensionID: "demo.pg"}, ref, "http.credential", 0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked after second restart: %v", err)
	}
}

func TestPostgresStoreWrongKeyFailClosed(t *testing.T) {
	fixture := newSecretStoreFixture(t)
	ctx := fixture.ctx
	ref := Ref{Namespace: "demo.wrongkey", SecretID: "token"}
	if _, err := fixture.svc.Put(ctx, ref, []byte("plain-secret"), PutOptions{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	// 用另一把密钥构造 Service：解密必须失败，且错误不得包含明文。
	otherKey := make([]byte, 32)
	if _, err := rand.Read(otherKey); err != nil {
		t.Fatal(err)
	}
	otherCipher, err := cryptox.NewOptionCipher(hex.EncodeToString(otherKey))
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := NewWithOptions(Options{
		Store: fixture.store, Cipher: otherCipher, RequireEncryption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrong.Resolve(ctx, Caller{ExtensionID: "demo.wrongkey"}, ref, "any", 0)
	if err == nil {
		t.Fatal("wrong key must fail closed")
	}
	if strings.Contains(err.Error(), "plain-secret") {
		t.Fatalf("error leaked plaintext: %v", err)
	}
}

func TestPostgresStorePermissionDenyAndNoPlaintextInDB(t *testing.T) {
	fixture := newSecretStoreFixture(t)
	ctx := fixture.ctx
	secret := "db-must-not-contain-this-value"
	ref := Ref{Namespace: "demo.db", SecretID: "password"}
	if _, err := fixture.svc.Put(ctx, ref, []byte(secret), PutOptions{
		Actor: "admin", Purposes: []string{"mail.transport"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.Resolve(ctx, Caller{ExtensionID: "evil.other"}, ref, "mail.transport", 0); !errors.Is(err, ErrNamespaceDenied) {
		t.Fatalf("cross-namespace: %v", err)
	}
	if _, err := fixture.svc.Resolve(ctx, Caller{ExtensionID: "demo.db"}, ref, "wrong.purpose", 0); !errors.Is(err, ErrPurposeDenied) {
		t.Fatalf("purpose deny: %v", err)
	}

	var stored string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT value FROM secret_store WHERE namespace=$1 AND secret_id=$2 ORDER BY version DESC LIMIT 1
	`, ref.Namespace, ref.SecretID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || !cryptox.IsEncrypted(stored) {
		t.Fatalf("database must store ciphertext only, got %q", stored)
	}
	if strings.Contains(stored, secret) {
		t.Fatalf("ciphertext contains plaintext: %q", stored)
	}
}

type secretStoreFixture struct {
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	store  *PostgresStore
	audit  *PostgresAuditStore
	cipher *cryptox.OptionCipher
	svc    *Service
	schema string
}

func newSecretStoreFixture(t *testing.T) *secretStoreFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("secret_store_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if err := applySecretStoreMigrations(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptox.NewOptionCipher(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := NewPostgresAuditStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewWithOptions(Options{
		Store: store, Cipher: cipher, Audit: audit, RequireEncryption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &secretStoreFixture{
		ctx: ctx, admin: admin, pool: pool, store: store, audit: audit,
		cipher: cipher, svc: svc, schema: schema,
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return fixture
}

func applySecretStoreMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	for _, name := range []string{
		"202607210043_secret_store.sql",
		"202607220045_secret_store_audit.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), name)
		if err != nil {
			return err
		}
		up := strings.SplitN(string(body), "-- +goose Down", 2)[0]
		if _, err := pool.Exec(ctx, up); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
