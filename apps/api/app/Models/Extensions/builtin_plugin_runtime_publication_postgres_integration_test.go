package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/zhuchunshu/sforum/apps/api/database/migrations"
)

// SaveBuiltin 需要完整 extensions / extension_versions 列；在隔离 schema 上
// 叠加 plugin runtime publication migration，避免依赖全局脏库状态。
func newBuiltinPluginRuntimeSaveFixture(t *testing.T, label string) *pluginRuntimePublicationPGFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("bps_%d_%s", time.Now().UnixNano(), label)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'installed',
			source TEXT NOT NULL DEFAULT 'uploaded',
			is_system BOOLEAN NOT NULL DEFAULT false,
			is_deletable BOOLEAN NOT NULL DEFAULT true,
			active_version_id BIGINT,
			staged_version_id BIGINT,
			installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extension_versions (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
			package_path TEXT NOT NULL DEFAULT '',
			package_digest TEXT NOT NULL,
			admin_frontend_digest TEXT NOT NULL DEFAULT '',
			installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (extension_id, version, package_digest)
		);
		ALTER TABLE extensions
			ADD CONSTRAINT extensions_active_version_fk
			FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
		ALTER TABLE extensions
			ADD CONSTRAINT extensions_staged_version_fk
			FOREIGN KEY (staged_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
		CREATE TABLE extension_lifecycle_operations (
			id BIGSERIAL PRIMARY KEY,
			completed_at TIMESTAMPTZ
		);
		CREATE TABLE mail_provider_selection (
			slot TEXT PRIMARY KEY,
			extension_id TEXT
		)
	`); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607160027, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply plugin runtime publication migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607270063, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply missing artifact removal migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607300002, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply builtin removal migration: %v", err)
	}
	if err := db.Close(); err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &pluginRuntimePublicationPGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool, store: NewPostgresStore(pool), schema: schema,
	}
	t.Cleanup(fixture.cleanup)
	return fixture
}

func builtinPluginManifest(id, version, backendEntry string) Manifest {
	manifest := Manifest{
		ManifestVersion: 3,
		ID:              id, Name: "Builtin Plugin", Description: "Builtin SaveBuiltin fixture.",
		URL: "https://example.com/builtin-plugin", Author: ManifestAuthor{Name: "SForum"},
		// 内置包与 core 同一发行物：陈旧约束按 source 豁免（默认主题今天也写 ^1.0.0）。
		Version: version, Type: TypePlugin, SForumVersion: "^1.0.0",
	}
	if backendEntry != "" {
		backendDigest := strings.Repeat("e", 64)
		manifest.Backend = ManifestBackend{
			Entry: backendEntry, RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: backendDigest, HostAPIVersion: "sforum.host@2",
		}
		manifest.PackageFiles = []ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: backendEntry, Digest: backendDigest,
		}}
	}
	return manifest
}

func authProviderBuiltinManifest(id, version string) Manifest {
	manifest := builtinPluginManifest(id, version, "backend/plugin")
	manifest.Identity = &ManifestIdentity{
		ContractVersion: "sforum.auth.fixture@1",
		Providers: []ManifestIdentityProvider{{
			ID: id + ".auth", ContractVersion: "sforum.auth.fixture.provider@1",
			Kind: "auth", Handler: id + ".handler",
			Operations: []ManifestIdentityProviderOperation{{
				Name: "login.start", InputSchema: "schemas/start.input.json",
				OutputSchema: "schemas/start.output.json", FailurePolicy: "fail_closed",
			}},
		}},
	}
	return manifest
}

func saveBuiltinPlugin(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	id, version, digest, backendEntry string,
) Extension {
	t.Helper()
	item, err := fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
		Manifest:      builtinPluginManifest(id, version, backendEntry),
		PackagePath:   "/tmp/" + id + "/" + version + "/" + digest[:8],
		PackageDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestSaveBuiltinAuthProviderDefaultsInstalledForExplicitLifecycleEnable(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "auth_default_off")
	const pluginID = "builtin.auth-provider"
	digest := strings.Repeat("a", 64)

	item, err := fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
		Manifest:      authProviderBuiltinManifest(pluginID, "1.0.0"),
		PackagePath:   "/tmp/" + pluginID + "/1.0.0",
		PackageDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusInstalled || item.ActiveVersionID == 0 || item.PackageDigest != digest {
		t.Fatalf("auth provider builtin should stage exact bytes without enabling runtime: %#v", item)
	}
	if _, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("auth provider builtin must not publish plugin runtime on sync: %v", err)
	}
}

func seedBuiltinPluginRuntimePublication(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	reason PluginRuntimePublicationReason,
	members ...PluginRuntimeMember,
) PluginRuntimePublication {
	t.Helper()
	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(fixture.ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		t.Fatal(err)
	}
	publication, err := insertPluginRuntimePublication(fixture.ctx, tx, reason, 0, members)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestPruneMissingPublishedBuiltinPluginRetainsHistoryAndPublishesRemoval(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "prune_published")
	const (
		keptID    = "builtin.kept"
		removedID = "builtin.removed"
	)
	keptDigest := strings.Repeat("a", 64)
	removedDigest := strings.Repeat("b", 64)
	kept := saveBuiltinPlugin(t, fixture, keptID, "1.0.0", keptDigest, "backend/plugin")
	removed := saveBuiltinPlugin(t, fixture, removedID, "1.0.0", removedDigest, "backend/plugin")
	seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
		PluginRuntimeMember{
			ExtensionID: keptID, ExtensionVersionID: kept.ActiveVersionID,
			ExtensionVersion: kept.Version, PackageDigest: keptDigest,
		},
		PluginRuntimeMember{
			ExtensionID: removedID, ExtensionVersionID: removed.ActiveVersionID,
			ExtensionVersion: removed.Version, PackageDigest: removedDigest,
		},
	)

	result, err := fixture.store.PruneMissingBuiltins(fixture.ctx, []string{keptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DisabledPluginIDs) != 1 || result.DisabledPluginIDs[0] != removedID {
		t.Fatalf("disabled plugins=%v", result.DisabledPluginIDs)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Reason != PluginRuntimePublicationStartupReconcile {
		t.Fatalf("publication reason=%s", latest.Reason)
	}
	assertPluginRuntimePublicationMembers(t, latest, PluginRuntimeMember{
		ExtensionID: keptID, ExtensionVersionID: kept.ActiveVersionID,
		ExtensionVersion: kept.Version, PackageDigest: keptDigest,
	})
	assertPluginRuntimePublicationCount(t, fixture, 2)

	if _, err := fixture.store.Get(fixture.ctx, removedID); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("removed builtin remained visible: %v", err)
	}
	var status string
	var versionCount, historicalMemberCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT status FROM extensions WHERE id = $1`, removedID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, removedID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id = $1`, removedID).Scan(&historicalMemberCount); err != nil {
		t.Fatal(err)
	}
	if status != StatusDisabled || versionCount != 1 || historicalMemberCount != 1 {
		t.Fatalf("retained state: status=%s versions=%d historicalMembers=%d", status, versionCount, historicalMemberCount)
	}

	replay, err := fixture.store.PruneMissingBuiltins(fixture.ctx, []string{keptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.DisabledPluginIDs) != 0 {
		t.Fatalf("idempotent prune disabled=%v", replay.DisabledPluginIDs)
	}
	assertPluginRuntimePublicationCount(t, fixture, 2)

	restored := saveBuiltinPlugin(t, fixture, removedID, "1.0.0", removedDigest, "backend/plugin")
	if restored.Status != StatusDisabled {
		t.Fatalf("restored builtin status=%s", restored.Status)
	}
	assertPluginRuntimePublicationCount(t, fixture, 2)
}

func TestSaveBuiltinEnabledPluginStagesDigestBWithoutBypassingLifecycle(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "upgrade_a_b")
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	const pluginID = "builtin.policy"

	v1 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	genesis := seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
		PluginRuntimeMember{
			ExtensionID: pluginID, ExtensionVersionID: v1.ActiveVersionID,
			ExtensionVersion: "1.0.0", PackageDigest: digestA,
		},
	)

	v2 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	if v2.ActiveVersionID != v1.ActiveVersionID || v2.StagedVersion == nil || v2.StagedVersion.ID == v1.ActiveVersionID {
		t.Fatalf("enabled builtin must stage B without advancing active artifact: %#v", v2)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != genesis.Revision {
		t.Fatalf("SyncBuiltins must not bypass lifecycle with a runtime upgrade publication: %+v", latest)
	}
	assertPluginRuntimePublicationMembers(t, latest, PluginRuntimeMember{
		ExtensionID: pluginID, ExtensionVersionID: v1.ActiveVersionID,
		ExtensionVersion: "1.0.0", PackageDigest: digestA,
	})
	assertPluginRuntimePublicationCount(t, fixture, 1)
}

// 旧旁路已把 active 推到 B，而 latest publication 仍停在 A：SaveBuiltin(B)
// 必须以 immutable 成员 A 为 source 追加 actorless upgrade，不能因
// previousActive==B 而短路跳过修复。
func TestSaveBuiltinPluginRuntimeRepairsStalePublicationWhenActiveAlreadyB(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "repair_stale_a")
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	const pluginID = "builtin.stale.active"

	v1 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	stale := seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
		PluginRuntimeMember{
			ExtensionID: pluginID, ExtensionVersionID: v1.ActiveVersionID,
			ExtensionVersion: "1.0.0", PackageDigest: digestA,
		},
	)

	// 模拟旧 SyncBuiltins 旁路：插入 B 并直接改 active，不碰 publication。
	manifestB := builtinPluginManifest(pluginID, "1.0.1", "backend/plugin")
	manifestJSON, err := json.Marshal(manifestB)
	if err != nil {
		t.Fatal(err)
	}
	var versionBID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.1', $2::jsonb, $3, $4)
		RETURNING id
	`, pluginID, string(manifestJSON), "/tmp/"+pluginID+"/side-b", digestB).Scan(&versionBID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions
		SET active_version_id = $1, staged_version_id = NULL, updated_at = now()
		WHERE id = $2
	`, versionBID, pluginID); err != nil {
		t.Fatal(err)
	}
	// 旁路后 publication 仍是 A。
	before, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(before, stale) {
		t.Fatalf("side channel must not touch publication: before=%+v stale=%+v err=%v", before, stale, err)
	}

	// SaveBuiltin 同一 B：应 A→B upgrade，即使 active 已是 B。
	repaired := saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	if repaired.ActiveVersionID != versionBID || repaired.PackageDigest != digestB {
		t.Fatalf("active after repair: %#v want version=%d", repaired, versionBID)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision <= stale.Revision ||
		latest.Reason != PluginRuntimePublicationUpgrade ||
		latest.ActorUserID != 0 {
		t.Fatalf("expected actorless upgrade repair: %+v", latest)
	}
	assertPluginRuntimePublicationMembers(t, latest, PluginRuntimeMember{
		ExtensionID: pluginID, ExtensionVersionID: versionBID,
		ExtensionVersion: "1.0.1", PackageDigest: digestB,
	})
	assertPluginRuntimePublicationCount(t, fixture, 2)

	// 重复 SaveBuiltin(B) 幂等，不再追加。
	again := saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	if again.ActiveVersionID != versionBID {
		t.Fatalf("idempotent active drift: %#v", again)
	}
	replayed, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(replayed, latest) {
		t.Fatalf("idempotent repair failed: want=%+v got=%+v err=%v", latest, replayed, err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 2)
}

func TestSaveBuiltinPluginRuntimeConcurrentRepeatIdempotent(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "concurrent_idempotent")
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	const pluginID = "builtin.concurrent"

	v1 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
		PluginRuntimeMember{
			ExtensionID: pluginID, ExtensionVersionID: v1.ActiveVersionID,
			ExtensionVersion: "1.0.0", PackageDigest: digestA,
		},
	)

	// 已启用制品的 B 只暂存；并发重复同步也不得绕过 lifecycle。
	saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	before, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Reason != PluginRuntimePublicationStartupReconcile {
		t.Fatalf("unexpected publication before concurrent replay: %+v", before)
	}

	workerPool, err := pgxpool.NewWithConfig(fixture.ctx, fixture.pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	stores := []*PostgresStore{fixture.store, NewPostgresStore(workerPool)}
	const producers = 8
	var wait sync.WaitGroup
	errorsFound := make(chan error, producers)
	for index := 0; index < producers; index++ {
		wait.Add(1)
		go func(store *PostgresStore) {
			defer wait.Done()
			_, saveErr := store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
				Manifest:      builtinPluginManifest(pluginID, "1.0.1", "backend/plugin"),
				PackagePath:   "/tmp/" + pluginID + "/1.0.1/" + digestB[:8],
				PackageDigest: digestB,
			})
			if saveErr != nil {
				errorsFound <- saveErr
			}
		}(stores[index%len(stores)])
	}
	wait.Wait()
	close(errorsFound)
	for saveErr := range errorsFound {
		t.Errorf("concurrent SaveBuiltin failed: %v", saveErr)
	}

	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, before) {
		t.Fatalf("concurrent replay changed publication: before=%+v after=%+v err=%v", before, latest, err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)
	active, err := fixture.store.Get(fixture.ctx, pluginID)
	if err != nil || active.ActiveVersionID != v1.ActiveVersionID || active.StagedVersion == nil || active.StagedVersion.PackageDigest != digestB {
		t.Fatalf("staged artifact after concurrent replay: %#v err=%v", active, err)
	}
}

func TestSaveBuiltinPluginRuntimeDoesNotResurrectAbsentMember(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "no_resurrect")
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	const pluginID = "builtin.revoked"

	// 扩展 enabled 且新制品会暂存，但 latest full-set 故意不含该成员
	// （模拟 trust-revocation 摘除成员后 status 仍 enabled）。
	v1 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	empty := seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationRecovery,
	)
	if empty.MemberCount != 0 {
		t.Fatalf("expected empty recovery set: %+v", empty)
	}

	v2 := saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	if v2.ActiveVersionID != v1.ActiveVersionID || v2.StagedVersion == nil || v2.StagedVersion.PackageDigest != digestB {
		t.Fatalf("new artifact should be staged: %#v", v2)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, empty) {
		t.Fatalf("absent member was resurrected: before=%+v after=%+v err=%v", empty, latest, err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)
}

func TestSaveBuiltinPluginRuntimePreservesUnrelatedMembers(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "preserve_third")
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	digestOther := strings.Repeat("c", 64)
	const (
		pluginID = "builtin.primary"
		otherID  = "third.party.plugin"
	)

	primary := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	otherManifest := string(runtimeManifestBody(t, otherID, "9.0.0", TypePlugin, "backend/other"))
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Third', 'enabled', 'uploaded', false, true)
	`, otherID); err != nil {
		t.Fatal(err)
	}
	var otherVersionID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '9.0.0', $2::jsonb, '/tmp/third', $3)
		RETURNING id
	`, otherID, otherManifest, digestOther).Scan(&otherVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions SET active_version_id = $1 WHERE id = $2
	`, otherVersionID, otherID); err != nil {
		t.Fatal(err)
	}

	seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
		PluginRuntimeMember{
			ExtensionID: pluginID, ExtensionVersionID: primary.ActiveVersionID,
			ExtensionVersion: "1.0.0", PackageDigest: digestA,
		},
		PluginRuntimeMember{
			ExtensionID: otherID, ExtensionVersionID: otherVersionID,
			ExtensionVersion: "9.0.0", PackageDigest: digestOther,
		},
	)

	upgraded := saveBuiltinPlugin(t, fixture, pluginID, "1.0.1", digestB, "backend/plugin")
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Reason != PluginRuntimePublicationStartupReconcile {
		t.Fatalf("SyncBuiltins bypassed lifecycle: %+v", latest)
	}
	assertPluginRuntimePublicationMembers(t, latest,
		PluginRuntimeMember{
			ExtensionID: pluginID, ExtensionVersionID: primary.ActiveVersionID,
			ExtensionVersion: "1.0.0", PackageDigest: digestA,
		},
		PluginRuntimeMember{
			ExtensionID: otherID, ExtensionVersionID: otherVersionID,
			ExtensionVersion: "9.0.0", PackageDigest: digestOther,
		},
	)
	assertPluginRuntimePublicationCount(t, fixture, 1)
	if upgraded.ActiveVersionID != primary.ActiveVersionID || upgraded.StagedVersion == nil || upgraded.StagedVersion.PackageDigest != digestB {
		t.Fatalf("primary upgrade was not staged: %#v", upgraded)
	}
}

func TestSaveBuiltinPluginRuntimeNoPublicationLeavesGenesisToLater(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "no_publication")
	digestA := strings.Repeat("a", 64)
	const pluginID = "builtin.genesis"

	item := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digestA, "backend/plugin")
	if item.Status != StatusEnabled || item.PackageDigest != digestA {
		t.Fatalf("unexpected builtin: %#v", item)
	}
	if _, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("SaveBuiltin without publication must not invent a full-set: %v", err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 0)

	// 后续 genesis 应从当前 active 投影完整集合。
	genesis, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationMembers(t, genesis, PluginRuntimeMember{
		ExtensionID: pluginID, ExtensionVersionID: item.ActiveVersionID,
		ExtensionVersion: "1.0.0", PackageDigest: digestA,
	})
	if genesis.Reason != PluginRuntimePublicationStartupReconcile || genesis.ActorUserID != 0 {
		t.Fatalf("unexpected genesis: %+v", genesis)
	}
}

func TestSaveBuiltinPluginRuntimeNewExecutableEnableWithPublication(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "new_enable")
	// 先有空 full-set authority，再插入全新可执行 builtin。
	empty := seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
	)
	digest := strings.Repeat("d", 64)
	const pluginID = "builtin.fresh"
	item := saveBuiltinPlugin(t, fixture, pluginID, "1.0.0", digest, "backend/plugin")
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision <= empty.Revision || latest.Reason != PluginRuntimePublicationEnable ||
		latest.ActorUserID != 0 {
		t.Fatalf("expected actorless enable: %+v", latest)
	}
	assertPluginRuntimePublicationMembers(t, latest, PluginRuntimeMember{
		ExtensionID: pluginID, ExtensionVersionID: item.ActiveVersionID,
		ExtensionVersion: "1.0.0", PackageDigest: digest,
	})
}

func TestSaveBuiltinPluginRuntimeDeclarationOnlyDoesNotAddMember(t *testing.T) {
	fixture := newBuiltinPluginRuntimeSaveFixture(t, "declaration_only")
	empty := seedBuiltinPluginRuntimePublication(
		t, fixture, PluginRuntimePublicationStartupReconcile,
	)
	digest := strings.Repeat("e", 64)
	item := saveBuiltinPlugin(t, fixture, "builtin.decl", "1.0.0", digest, "")
	if item.PackageDigest != digest {
		t.Fatalf("declaration builtin not saved: %#v", item)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, empty) {
		t.Fatalf("declaration-only must not append: before=%+v after=%+v err=%v", empty, latest, err)
	}
}
