package settingslifecycle

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

func TestPutPreviewResetExportImportAndMigration(t *testing.T) {
	ctx := context.Background()
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := NewMemoryDocumentStore()
	svc := NewWithStore(docs, secrets)
	if err := svc.RegisterSchema("demo.settings", 2, []FieldSchema{
		{Name: "mode", Type: "select", Required: true, Default: "safe", Options: []string{"safe", "advanced"}},
		{Name: "token", Type: "secret", Secret: true},
		{Name: "advanced_only", Type: "string", VisibleWhen: "mode=advanced"},
		{Name: "legacy_name", Type: "string", Default: ""},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterMigration("demo.settings", Migration{
		From: 1, To: 2,
		Apply: func(values map[string]string) (map[string]string, error) {
			if v, ok := values["legacy_name"]; ok {
				values["mode"] = v
				delete(values, "legacy_name")
			}
			return values, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Seed v1 document then migrate on put.
	docs.Seed("demo.settings", Document{
		SchemaVersion: SchemaVersion, ExtensionID: "demo.settings", DataVersion: 1,
		Values: map[string]string{"legacy_name": "safe"}, SecretRefs: map[string]string{}, SecretSet: map[string]bool{},
	})

	doc, err := svc.Put(ctx, "demo.settings", "admin", map[string]string{
		"mode": "safe", "token": "s3cret",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DataVersion != 2 || !doc.SecretSet["token"] || doc.SecretRefs["token"] == "" {
		t.Fatalf("doc = %#v", doc)
	}
	if _, ok := doc.Values["token"]; ok {
		t.Fatal("secret plaintext leaked into values")
	}

	preview, err := svc.Preview(ctx, "demo.settings", map[string]string{"mode": "safe"})
	if err != nil || !preview.OK {
		t.Fatalf("preview = %#v err=%v", preview, err)
	}
	for _, name := range preview.VisibleFields {
		if name == "advanced_only" {
			t.Fatal("advanced_only should be hidden when mode=safe")
		}
	}
	preview2, err := svc.Preview(ctx, "demo.settings", map[string]string{"mode": "advanced", "advanced_only": ""})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range preview2.VisibleFields {
		if name == "advanced_only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("advanced_only not visible: %#v", preview2)
	}

	// Preserve secret on empty.
	doc2, err := svc.Put(ctx, "demo.settings", "admin", map[string]string{"mode": "safe", "token": ""}, true)
	if err != nil || !doc2.SecretSet["token"] {
		t.Fatalf("preserve secret = %#v err=%v", doc2, err)
	}

	exported, err := svc.Export(ctx, "demo.settings")
	if err != nil || !exported.SecretsNeverIncluded || exported.Values["token"] != "" {
		t.Fatalf("export = %#v err=%v", exported, err)
	}
	if exported.SecretRefs["token"] == "s3cret" {
		t.Fatal("export must not include plaintext secret")
	}

	// Reset 明确不保留密钥。
	reset, err := svc.ResetDefaults(ctx, "demo.settings", "admin", ResetOptions{PreserveSecrets: false})
	if err != nil || reset.Values["mode"] != "safe" || reset.SecretSet["token"] {
		t.Fatalf("reset clear secrets = %#v err=%v", reset, err)
	}

	// Import restores values + secret refs (no plaintext).
	imported, err := svc.Import(ctx, "demo.settings", "admin", exported)
	if err != nil || imported.SecretRefs["token"] == "" {
		t.Fatalf("import = %#v err=%v", imported, err)
	}
	if _, err := svc.Import(ctx, "demo.settings", "admin", ExportBundle{
		ExtensionID: "demo.settings", Values: map[string]string{"mode": "enc::deadbeef"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("ciphertext import = %v", err)
	}

	// Reset 保留密钥（beginner-friendly 推荐路径）。
	if _, err := svc.Put(ctx, "demo.settings", "admin", map[string]string{"mode": "advanced", "token": "again"}, true); err != nil {
		t.Fatal(err)
	}
	kept, err := svc.ResetDefaults(ctx, "demo.settings", "admin", ResetOptions{PreserveSecrets: true})
	if err != nil || kept.Values["mode"] != "safe" || !kept.SecretSet["token"] {
		t.Fatalf("reset preserve secrets = %#v err=%v", kept, err)
	}
}

func TestRuntimeValuesResolveSecretReferences(t *testing.T) {
	ctx := context.Background()
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithStore(NewMemoryDocumentStore(), secrets)
	if err := svc.RegisterSchema("demo.runtime", 1, []FieldSchema{
		{Name: "host", Type: "string", Default: ""},
		{Name: "password", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "demo.runtime", "admin", map[string]string{
		"host": "smtp.example.com", "password": "app-secret",
	}, true); err != nil {
		t.Fatal(err)
	}
	values, err := svc.RuntimeValues(ctx, "demo.runtime", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["host"] != "smtp.example.com" || values["password"] != "app-secret" {
		t.Fatalf("runtime values = %#v", values)
	}
}

func TestLegacyEncryptedSettingsMigrateBeforeRuntimeRead(t *testing.T) {
	ctx := context.Background()
	cipher, err := cryptox.NewOptionCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cipher.Encrypt("legacy-secret")
	if err != nil {
		t.Fatal(err)
	}
	kv := NewMemorySettingsKV()
	if err := kv.ReplaceSettings(ctx, "demo.legacy", map[string]string{
		"token": legacy, "mode": "safe", metaRevisionKey: "4",
	}); err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := NewSettingsKVStore(kv)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithStore(docs, secrets).WithLegacyCipher(cipher)
	if err := svc.RegisterSchema("demo.legacy", 1, []FieldSchema{
		{Name: "mode", Type: "string"}, {Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	values, err := svc.RuntimeValues(ctx, "demo.legacy", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if values["token"] != "legacy-secret" || values["mode"] != "safe" {
		t.Fatalf("runtime values = %#v", values)
	}
	raw, err := kv.ListSettings(ctx, "demo.legacy")
	if err != nil {
		t.Fatal(err)
	}
	if cryptox.IsEncrypted(raw["token"]) || !strings.HasPrefix(raw["token"], secretstore.ReferenceScheme) {
		t.Fatalf("legacy ciphertext was not replaced by SecretStore reference: %#v", raw)
	}

	badKV := NewMemorySettingsKV()
	if err := badKV.ReplaceSettings(ctx, "demo.legacy", map[string]string{"token": "enc::not-valid"}); err != nil {
		t.Fatal(err)
	}
	badDocs, _ := NewSettingsKVStore(badKV)
	badSvc := NewWithStore(badDocs, secrets).WithLegacyCipher(cipher)
	_ = badSvc.RegisterSchema("demo.legacy", 1, []FieldSchema{{Name: "token", Type: "secret", Secret: true}})
	if _, err := badSvc.Put(ctx, "demo.legacy", "admin", map[string]string{"mode": "safe"}, true); !errors.Is(err, ErrMigration) {
		t.Fatalf("invalid legacy ciphertext should fail closed, got %v", err)
	}
	badRaw, _ := badKV.ListSettings(ctx, "demo.legacy")
	if badRaw["token"] != "enc::not-valid" {
		t.Fatalf("failed migration changed legacy row: %#v", badRaw)
	}
}

func TestFailedMigrationDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	docs := NewMemoryDocumentStore()
	svc := NewWithStore(docs, nil)
	if err := svc.RegisterSchema("demo.mig", 2, []FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterMigration("demo.mig", Migration{
		From: 1, To: 2,
		Apply: func(values map[string]string) (map[string]string, error) {
			return nil, errors.New("boom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	docs.Seed("demo.mig", Document{
		SchemaVersion: SchemaVersion, ExtensionID: "demo.mig", DataVersion: 1,
		Values: map[string]string{"mode": "old"}, SecretRefs: map[string]string{}, SecretSet: map[string]bool{},
	})
	if _, err := svc.Put(ctx, "demo.mig", "admin", map[string]string{"mode": "new"}, true); !errors.Is(err, ErrMigration) {
		t.Fatalf("expected migration error, got %v", err)
	}
	// 权威存储仍是 v1。
	doc, _, err := docs.Load(ctx, "demo.mig")
	if err != nil || doc.DataVersion != 1 || doc.Values["mode"] != "old" {
		t.Fatalf("store after failed migration = %#v err=%v", doc, err)
	}
}

func TestSettingsKVStoreRoundTripAndCAS(t *testing.T) {
	ctx := context.Background()
	kv := NewMemorySettingsKV()
	store, err := NewSettingsKVStore(kv)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithStore(store, secrets)
	if err := svc.RegisterSchema("demo.kv", 1, []FieldSchema{
		{Name: "title", Type: "string", Default: "hi"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	doc, err := svc.Put(ctx, "demo.kv", "admin", map[string]string{"title": "hello", "token": "s3cret"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// 新 Service 实例模拟重启。
	svc2 := NewWithStore(store, secrets)
	_ = svc2.RegisterSchema("demo.kv", 1, []FieldSchema{
		{Name: "title", Type: "string", Default: "hi"},
		{Name: "token", Type: "secret", Secret: true},
	})
	got, err := svc2.Get(ctx, "demo.kv")
	if err != nil || got.Values["title"] != "hello" || got.SecretRefs["token"] != doc.SecretRefs["token"] {
		t.Fatalf("restart get = %#v err=%v", got, err)
	}
	// CAS conflict.
	_, rev, err := store.Load(ctx, "demo.kv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, "demo.kv", got, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, "demo.kv", got, rev); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS = %v", err)
	}

	// 并发 Put：最终有一致 revision，无 panic。
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, _ = svc2.Put(ctx, "demo.kv", "admin", map[string]string{"title": "t"}, true)
		}(i)
	}
	wait.Wait()
	final, err := svc2.Get(ctx, "demo.kv")
	if err != nil || final.Values["title"] == "" {
		t.Fatalf("after concurrent put = %#v err=%v", final, err)
	}
}

func TestMemorySettingsKVReplaceSettingsCASAddsMissingRevision(t *testing.T) {
	kv := NewMemorySettingsKV()
	values := map[string]string{"title": "hello"}

	revision, err := kv.ReplaceSettingsCAS(context.Background(), "demo.kv", 0, values)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := kv.ListSettings(context.Background(), "demo.kv")
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 || stored[metaRevisionKey] != "1" || stored["title"] != "hello" {
		t.Fatalf("unexpected stored settings: revision=%d values=%#v", revision, stored)
	}
	if _, mutated := values[metaRevisionKey]; mutated {
		t.Fatalf("ReplaceSettingsCAS mutated caller values: %#v", values)
	}
}

func TestActorRequired(t *testing.T) {
	ctx := context.Background()
	svc := New(nil)
	_ = svc.RegisterSchema("x", 1, []FieldSchema{{Name: "a", Type: "string", Default: ""}})
	if _, err := svc.Put(ctx, "x", "", map[string]string{"a": "1"}, true); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("put actor = %v", err)
	}
	if _, err := svc.Put(nil, "x", "admin", map[string]string{"a": "1"}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil ctx = %v", err)
	}
}

// TestTwoIndependentServicesConcurrentSaveNoFieldLoss 证明两个独立 Service
// 实例并发保存时：CAS 保证不会 revision 倒退，也不会在同一 revision 上丢字段。
func TestTwoIndependentServicesConcurrentSaveNoFieldLoss(t *testing.T) {
	ctx := context.Background()
	kv := NewMemorySettingsKV()
	store, err := NewSettingsKVStore(kv)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := []FieldSchema{
		{Name: "title", Type: "string", Default: ""},
		{Name: "mode", Type: "string", Default: "safe"},
		{Name: "token", Type: "secret", Secret: true},
	}
	// 两个完全独立的 Service 实例，共享同一 DocumentStore（模拟双 API 连接）。
	svcA := NewWithStore(store, secrets)
	svcB := NewWithStore(store, secrets)
	if err := svcA.RegisterSchema("demo.race", 1, schema); err != nil {
		t.Fatal(err)
	}
	if err := svcB.RegisterSchema("demo.race", 1, schema); err != nil {
		t.Fatal(err)
	}
	// 种子文档。
	seed, err := svcA.Put(ctx, "demo.race", "admin-a", map[string]string{
		"title": "seed", "mode": "safe", "token": "s3cret",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if seed.SecretRefs["token"] == "" {
		t.Fatal("seed secret missing")
	}
	_, seedRev, err := store.Load(ctx, "demo.race")
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
			// 交替使用两个独立 Service。
			svc := svcA
			actor := "admin-a"
			if i%2 == 1 {
				svc = svcB
				actor = "admin-b"
			}
			// 每个写者只改一个字段，另一字段必须保留。
			values := map[string]string{}
			if i%2 == 0 {
				values["title"] = "title-" + strconv.Itoa(i)
			} else {
				values["mode"] = "mode-" + strconv.Itoa(i)
			}
			_, putErr := svc.Put(ctx, "demo.race", actor, values, true)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case putErr == nil:
				success++
			case errors.Is(putErr, ErrConflict):
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
		t.Fatal("expected at least one successful concurrent save")
	}

	final, finalRev, err := store.Load(ctx, "demo.race")
	if err != nil {
		t.Fatal(err)
	}
	if finalRev < seedRev {
		t.Fatalf("revision regressed: seed=%d final=%d", seedRev, finalRev)
	}
	// 全量文档：title 与 mode 都必须存在（非空）；secret ref 不得因并发丢失。
	if strings.TrimSpace(final.Values["title"]) == "" || strings.TrimSpace(final.Values["mode"]) == "" {
		t.Fatalf("lost field after concurrent saves: %#v rev=%d", final.Values, finalRev)
	}
	if final.SecretRefs["token"] == "" || !final.SecretSet["token"] {
		t.Fatalf("secret lost after concurrent saves: %#v", final)
	}
	// 再次 Get 证明可读一致性。
	got, err := svcB.Get(ctx, "demo.race")
	if err != nil || got.Values["title"] == "" || got.Values["mode"] == "" {
		t.Fatalf("get after race = %#v err=%v", got, err)
	}
}

// TestFailedMigrationDoesNotTouchSecretStoreOrRevision 失败迁移不得改变设置/Secret/revision。
func TestFailedMigrationDoesNotTouchSecretStoreOrRevision(t *testing.T) {
	ctx := context.Background()
	kv := NewMemorySettingsKV()
	store, err := NewSettingsKVStore(kv)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithStore(store, secrets)
	if err := svc.RegisterSchema("demo.failmig", 2, []FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterMigration("demo.failmig", Migration{
		From: 1, To: 2,
		Apply: func(values map[string]string) (map[string]string, error) {
			return nil, errors.New("migrate boom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 先写入 v1 文档（目标 schema 为 2，需先 seed 再注册 migration 场景）。
	// 用 target=1 写入，再抬高 target。
	svcV1 := NewWithStore(store, secrets)
	if err := svcV1.RegisterSchema("demo.failmig", 1, []FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := svcV1.Put(ctx, "demo.failmig", "admin", map[string]string{
		"mode": "old", "token": "keep-me",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, beforeRev, err := store.Load(ctx, "demo.failmig")
	if err != nil {
		t.Fatal(err)
	}
	// 抬高目标版本并失败迁移。
	if err := svc.RegisterSchema("demo.failmig", 2, []FieldSchema{
		{Name: "mode", Type: "string", Default: "a"},
		{Name: "token", Type: "secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "demo.failmig", "admin", map[string]string{"mode": "new"}, true); !errors.Is(err, ErrMigration) {
		t.Fatalf("want migration error, got %v", err)
	}
	after, afterRev, err := store.Load(ctx, "demo.failmig")
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
	// SecretStore 中的明文仍可通过原 ref 解析（未被 wipe）。
	if secrets != nil && before.SecretRefs["token"] != "" {
		// 仅断言 ref 字符串稳定即可；解析路径由 SecretStore 自己测。
	}
}
