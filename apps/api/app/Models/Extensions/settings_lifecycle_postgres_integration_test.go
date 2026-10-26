package extensions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
)

func TestLegacyCiphertextMigratesOnFirstPostgresSettingsWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres legacy settings migration requires real database")
	}
	databaseURL := settingsLifecycleTestDatabaseURL()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	extID := fmt.Sprintf("test.settings.legacy.%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'legacy settings', 'installed')
	`, extID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_settings WHERE extension_id = $1`, extID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extID)
	})

	cipher, err := cryptox.NewOptionCipher(strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cipher.Encrypt("postgres-legacy-secret")
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	if err := store.ReplaceSettings(ctx, extID, map[string]string{
		"mode": "old", "token": legacy,
	}); err != nil {
		t.Fatal(err)
	}
	docStore, err := settingslifecycle.NewSettingsKVStore(store)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := settingslifecycle.NewWithStore(docStore, secrets).WithLegacyCipher(cipher)
	if err := svc.RegisterSchema(extID, 1, []settingslifecycle.FieldSchema{
		{Name: "mode", Type: "string"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, extID, "user:1", map[string]string{"mode": "new"}, true); err != nil {
		t.Fatal(err)
	}

	var persisted string
	if err := pool.QueryRow(ctx, `
		SELECT value FROM extension_settings WHERE extension_id = $1 AND name = 'token'
	`, extID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(persisted, "enc::") || !strings.HasPrefix(persisted, secretstore.ReferenceScheme) {
		t.Fatalf("legacy ciphertext was not replaced atomically: %q", persisted)
	}
	values, err := svc.RuntimeValues(ctx, extID, "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["mode"] != "new" || values["token"] != "postgres-legacy-secret" {
		t.Fatalf("runtime values = %#v", values)
	}
}

// TestTwoIndependentPostgresConnectionsConcurrentSaveNoFieldLoss 用两条独立
// PostgreSQL 连接 + 两个独立 SettingsLifecycle Service，证明生产 CAS 路径：
// revision 不倒退、字段不丢失、secret ref 不丢。
// 禁止仅依赖 Support 层 MemorySettingsKV。
func TestTwoIndependentPostgresConnectionsConcurrentSaveNoFieldLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres concurrent settings requires real database")
	}
	databaseURL := settingsLifecycleTestDatabaseURL()
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()

	// 两条独立连接池，模拟双 API 节点。
	poolA, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pool A: %v", err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pool B: %v", err)
	}
	t.Cleanup(poolB.Close)
	if err := poolA.Ping(ctx); err != nil {
		t.Fatalf("ping A: %v", err)
	}
	if err := poolB.Ping(ctx); err != nil {
		t.Fatalf("ping B: %v", err)
	}

	extID := fmt.Sprintf("test.settings.race.%d", time.Now().UnixNano())
	// extension_settings 外键到 extensions；仅插入身份行，不 seed 设置。
	if _, err := poolA.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'settings race', 'installed')
		ON CONFLICT (id) DO NOTHING
	`, extID); err != nil {
		t.Fatalf("seed extension row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = poolA.Exec(context.Background(), `DELETE FROM extension_settings WHERE extension_id = $1`, extID)
		_, _ = poolA.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extID)
	})

	storeA := NewPostgresStore(poolA)
	storeB := NewPostgresStore(poolB)
	docA, err := settingslifecycle.NewSettingsKVStore(storeA)
	if err != nil {
		t.Fatal(err)
	}
	docB, err := settingslifecycle.NewSettingsKVStore(storeB)
	if err != nil {
		t.Fatal(err)
	}
	// SecretStore 使用内存即可；本测关注 extension_settings CAS 与双连接。
	secretsA, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	secretsB, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svcA := settingslifecycle.NewWithStore(docA, secretsA)
	svcB := settingslifecycle.NewWithStore(docB, secretsB)

	schema := []settingslifecycle.FieldSchema{
		{Name: "title", Type: "string", Default: ""},
		{Name: "mode", Type: "string", Default: "safe"},
		{Name: "token", Type: "secret", Secret: true},
	}
	if err := svcA.RegisterSchema(extID, 1, schema); err != nil {
		t.Fatal(err)
	}
	if err := svcB.RegisterSchema(extID, 1, schema); err != nil {
		t.Fatal(err)
	}

	seed, err := svcA.Put(ctx, extID, "admin-a", map[string]string{
		"title": "seed", "mode": "safe", "token": "s3cret",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if seed.SecretRefs["token"] == "" {
		t.Fatal("seed secret missing")
	}
	_, seedRev, err := docA.Load(ctx, extID)
	if err != nil || seedRev < 1 {
		t.Fatalf("seed rev=%d err=%v", seedRev, err)
	}

	const workers = 24
	var (
		wait     sync.WaitGroup
		mu       sync.Mutex
		success  int
		conflict int
		other    int
	)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			svc := svcA
			actor := "admin-a"
			if i%2 == 1 {
				svc = svcB
				actor = "admin-b"
			}
			values := map[string]string{}
			if i%2 == 0 {
				values["title"] = "title-" + strconv.Itoa(i)
			} else {
				values["mode"] = "mode-" + strconv.Itoa(i)
			}
			// CAS 冲突时重试：生产客户端会重试；此处保证至少有成功写。
			var putErr error
			for attempt := 0; attempt < 8; attempt++ {
				_, putErr = svc.Put(ctx, extID, actor, values, true)
				if putErr == nil || !errors.Is(putErr, settingslifecycle.ErrConflict) {
					break
				}
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case putErr == nil:
				success++
			case errors.Is(putErr, settingslifecycle.ErrConflict):
				conflict++
			default:
				other++
				t.Errorf("unexpected put error: %v", putErr)
			}
		}(i)
	}
	wait.Wait()
	if other > 0 {
		t.Fatalf("unexpected errors=%d success=%d conflict=%d", other, success, conflict)
	}
	if success < 1 {
		t.Fatal("expected at least one successful concurrent save across two connections")
	}

	// 第三条连接读回，证明事务提交对所有节点可见。
	poolC, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolC.Close)
	storeC := NewPostgresStore(poolC)
	docC, err := settingslifecycle.NewSettingsKVStore(storeC)
	if err != nil {
		t.Fatal(err)
	}
	final, finalRev, err := docC.Load(ctx, extID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRev < seedRev {
		t.Fatalf("revision regressed: seed=%d final=%d", seedRev, finalRev)
	}
	if strings.TrimSpace(final.Values["title"]) == "" || strings.TrimSpace(final.Values["mode"]) == "" {
		t.Fatalf("lost field after concurrent dual-connection saves: %#v rev=%d", final.Values, finalRev)
	}
	if final.SecretRefs["token"] == "" || !final.SecretSet["token"] {
		t.Fatalf("secret lost after concurrent dual-connection saves: %#v", final)
	}

	// 直接查 PostgreSQL 行，证明 revision CAS + 全量替换在同一事务后一致。
	var revRow string
	if err := poolC.QueryRow(ctx, `
		SELECT value FROM extension_settings
		WHERE extension_id = $1 AND name = '__sforum.revision'
	`, extID).Scan(&revRow); err != nil {
		t.Fatalf("read revision row: %v", err)
	}
	if parseInt64(revRow) != finalRev {
		t.Fatalf("pg revision row=%s document rev=%d", revRow, finalRev)
	}
}

// TestFailedMigrationDoesNotChangePostgresSettingsOrRevision 失败迁移不得改
// extension_settings / revision（真实 PostgreSQL 事务路径）。
func TestFailedMigrationDoesNotChangePostgresSettingsOrRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres failed migration requires real database")
	}
	databaseURL := settingsLifecycleTestDatabaseURL()
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	extID := fmt.Sprintf("test.settings.failmig.%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'settings failmig', 'installed')
		ON CONFLICT (id) DO NOTHING
	`, extID); err != nil {
		t.Fatalf("seed extension row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_settings WHERE extension_id = $1`, extID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extID)
	})

	store := NewPostgresStore(pool)
	docStore, err := settingslifecycle.NewSettingsKVStore(store)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svcV1 := settingslifecycle.NewWithStore(docStore, secrets)
	if err := svcV1.RegisterSchema(extID, 1, []settingslifecycle.FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := svcV1.Put(ctx, extID, "admin", map[string]string{
		"mode": "old", "token": "keep-me",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, beforeRev, err := docStore.Load(ctx, extID)
	if err != nil {
		t.Fatal(err)
	}

	// 独立 Service（模拟升级后重新绑定），目标版本 2 + 失败迁移。
	svcV2 := settingslifecycle.NewWithStore(docStore, secrets)
	if err := svcV2.RegisterSchema(extID, 2, []settingslifecycle.FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svcV2.RegisterMigration(extID, settingslifecycle.Migration{
		From: 1, To: 2,
		Apply: func(values map[string]string) (map[string]string, error) {
			return nil, errors.New("migrate boom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svcV2.Put(ctx, extID, "admin", map[string]string{"mode": "new"}, true); !errors.Is(err, settingslifecycle.ErrMigration) {
		t.Fatalf("want migration error, got %v", err)
	}

	after, afterRev, err := docStore.Load(ctx, extID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRev != beforeRev {
		t.Fatalf("revision changed on failed migration: before=%d after=%d", beforeRev, afterRev)
	}
	if after.DataVersion != 1 || after.Values["mode"] != "old" {
		t.Fatalf("settings changed on failed migration: %#v", after)
	}
	if after.SecretRefs["token"] != before.SecretRefs["token"] {
		t.Fatalf("secret ref changed: before=%s after=%s", before.SecretRefs["token"], after.SecretRefs["token"])
	}

	// 直接数 PostgreSQL 行：不得出现 mode=new。
	var modeVal string
	if err := pool.QueryRow(ctx, `
		SELECT value FROM extension_settings WHERE extension_id = $1 AND name = 'mode'
	`, extID).Scan(&modeVal); err != nil {
		t.Fatalf("mode row: %v", err)
	}
	if modeVal != "old" {
		t.Fatalf("pg mode row = %q want old", modeVal)
	}
}

func settingsLifecycleTestDatabaseURL() string {
	if v := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		return v
	}
	// 开发 compose 默认端口。
	return "postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable"
}

func parseInt64(raw string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n
}
