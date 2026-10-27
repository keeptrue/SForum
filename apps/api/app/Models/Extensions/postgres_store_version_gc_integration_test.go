package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// ---- 纯函数判据矩阵（allowed + denied 双侧）----

func TestVersionGCBlockedReasonMatrix(t *testing.T) {
	now := time.Now()
	old := now.Add(-72 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	t.Run("publication history pins permanently and wins precedence", func(t *testing.T) {
		// 发布引用挡：即使同时存在开放操作且已超窗，发布历史仍优先报告。
		if got := versionGCBlockedReason(true, true, 5, 1, 0, old, now); got != VersionGCBlockedPublicationHistory {
			t.Fatalf("blocked reason = %q, want %q", got, VersionGCBlockedPublicationHistory)
		}
	})
	t.Run("no publication allows next criterion", func(t *testing.T) {
		if got := versionGCBlockedReason(false, false, 5, 1, 0, old, now); got != "" {
			t.Fatalf("blocked reason = %q, want reclaimable", got)
		}
	})
	t.Run("open operation blocks and outranks retention window", func(t *testing.T) {
		if got := versionGCBlockedReason(false, true, 5, 1, 0, old, now); got != VersionGCBlockedOpenOperation {
			t.Fatalf("blocked reason = %q, want %q", got, VersionGCBlockedOpenOperation)
		}
	})
	t.Run("within retention by rank blocks", func(t *testing.T) {
		if got := versionGCBlockedReason(false, false, 1, 1, 0, old, now); got != VersionGCBlockedWithinRetention {
			t.Fatalf("blocked reason = %q, want %q", got, VersionGCBlockedWithinRetention)
		}
	})
	t.Run("beyond retain rank is reclaimable when no time window is set", func(t *testing.T) {
		if got := versionGCBlockedReason(false, false, 2, 1, 0, fresh, now); got != "" {
			t.Fatalf("blocked reason = %q, want reclaimable", got)
		}
	})
	t.Run("retain days keeps fresh versions", func(t *testing.T) {
		if got := versionGCBlockedReason(false, false, 2, 1, 7, fresh, now); got != VersionGCBlockedWithinRetention {
			t.Fatalf("blocked reason = %q, want %q", got, VersionGCBlockedWithinRetention)
		}
	})
	t.Run("retain days releases old versions", func(t *testing.T) {
		if got := versionGCBlockedReason(false, false, 2, 1, 2, old, now); got != "" {
			t.Fatalf("blocked reason = %q, want reclaimable", got)
		}
	})
}

func TestVersionGCOptionsNormalized(t *testing.T) {
	normalized := VersionGCOptions{Retain: 0, RetainDays: -3, ExtensionID: " test.gc "}.normalized()
	if normalized.Retain != versionGCDefaultRetain || normalized.RetainDays != 0 || normalized.ExtensionID != "test.gc" {
		t.Fatalf("normalized options = %#v", normalized)
	}
	if got := (VersionGCOptions{RetainDays: 999999}).normalized().RetainDays; got != versionGCMaxRetainDays {
		t.Fatalf("retain days clamp = %d, want %d", got, versionGCMaxRetainDays)
	}
}

func TestVersionGCPlanDigestBindsOptionsAndCandidates(t *testing.T) {
	opts := VersionGCOptions{Retain: 1}
	candidate := RetiredVersionCandidate{
		ExtensionID: "test.gc", VersionID: 7, Version: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), PackagePath: "/tmp/gc/1.0.0",
	}
	baseline := versionGCPlanDigest(opts, []RetiredVersionCandidate{candidate})
	if again := versionGCPlanDigest(opts, []RetiredVersionCandidate{candidate}); again != baseline {
		t.Fatalf("plan digest is not deterministic")
	}
	// 候选顺序不影响计划摘要（摘要内部重新排序）。
	if reordered := versionGCPlanDigest(opts, []RetiredVersionCandidate{candidate, candidate}); reordered != versionGCPlanDigest(opts, []RetiredVersionCandidate{candidate, candidate}) {
		t.Fatalf("plan digest depends on input order")
	}
	if drifted := versionGCPlanDigest(VersionGCOptions{Retain: 2}, []RetiredVersionCandidate{candidate}); drifted == baseline {
		t.Fatalf("options change must change the plan digest")
	}
	expanded := candidate
	expanded.VersionID = 8
	if grown := versionGCPlanDigest(opts, []RetiredVersionCandidate{candidate, expanded}); grown == baseline {
		t.Fatalf("candidate set change must change the plan digest")
	}
}

// ---- 真库集成测试（隔离库；未配置 SFORUM_TEST_DATABASE_URL 时跳过）----

type versionGCFixture struct {
	id       string
	store    *PostgresStore
	versions []versionGCFixtureVersion // 下标 0 = v1
}

type versionGCFixtureVersion struct {
	Version string
	ID      int64
	Digest  string
	Path    string
}

// newVersionGCFixture 安装 v1（active）→ staging v2 → promote v2，使 v1 成为
// 历史版本；每个版本配一个真实临时目录（含一个文件），让 disk 判据为真、
// bytes_freed > 0。
func newVersionGCFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *versionGCFixture {
	t.Helper()
	store := NewPostgresStore(pool)
	fixture := &versionGCFixture{
		id:    fmt.Sprintf("test.version-gc.%d", time.Now().UnixNano()),
		store: store,
	}
	t.Cleanup(func() {
		// 顺序是硬约束，不是风格：
		//  1) plugin_runtime_publication_members 对 extension_versions 是 RESTRICT（生产里
		//     那是「精确版本不可物理删除」的完整性约束），所以先拆 publication 再删版本；
		//  2) 锚点表 extension_version_gc_removals 对 extensions 也是 RESTRICT；
		//  3) 最后删 extensions，其余子表是 CASCADE。
		// 清理失败必须报出来：吞掉错误会让行留在共享测试库里，后面 bootstrap 的
		// lifecycle preflight 会读到这些「没有有效 manifest 的历史行」并整包失败——
		// 排查成本极高，而根因只是这里少删了两张表。
		retireVersionGCFixture(t, pool, fixture.id)
	})

	base := t.TempDir()
	digests := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	makeVersion := func(index int, version string, wantStaged bool) versionGCFixtureVersion {
		t.Helper()
		dir := filepath.Join(base, fmt.Sprintf("v%d", index+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.bin"), []byte(strings.Repeat("x", 128)), 0o644); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
		installed, err := store.SaveInstalled(ctx, SaveInstalledInput{
			Manifest:      stagedVersionTestManifest(fixture.id, version),
			PackagePath:   dir,
			PackageDigest: digests[index],
		})
		if err != nil {
			t.Fatalf("save fixture version %s: %v", version, err)
		}
		if wantStaged {
			if installed.StagedVersion == nil {
				t.Fatalf("version %s was not staged: %#v", version, installed)
			}
			return versionGCFixtureVersion{Version: version, Digest: digests[index], Path: dir, ID: installed.StagedVersion.ID}
		}
		return versionGCFixtureVersion{Version: version, Digest: digests[index], Path: dir, ID: installed.ActiveVersionID}
	}
	// 已验证序列（对齐 postgres_store_staged_version_integration_test.go）：
	// install v1 → enable → stage v2 → promote（v1 转历史）→ stage v3 →
	// discard（v3 转历史，installed_at 最新 → rank1；v1 为 rank2）。
	// 未启用状态下二次 SaveInstalled 会直接替换 active 而不是 staging。
	first := makeVersion(0, "1.0.0", false)
	if _, err := store.Enable(ctx, fixture.id, TypePlugin); err != nil {
		t.Fatalf("enable fixture extension: %v", err)
	}
	second := makeVersion(1, "2.0.0", true)
	if _, err := store.PromoteStagedVersion(ctx, StagedVersionCASInput{
		ExtensionID:                 fixture.id,
		ExpectedActiveVersionID:     first.ID,
		ExpectedActiveVersion:       first.Version,
		ExpectedActivePackageDigest: first.Digest,
		ExpectedStagedVersionID:     second.ID,
		ExpectedStagedVersion:       second.Version,
		ExpectedPackageDigest:       second.Digest,
	}); err != nil {
		t.Fatalf("promote staged fixture version: %v", err)
	}
	third := makeVersion(2, "3.0.0", true)
	if _, err := store.DiscardStagedVersion(ctx, StagedVersionCASInput{
		ExtensionID:                 fixture.id,
		ExpectedActiveVersionID:     second.ID,
		ExpectedActiveVersion:       second.Version,
		ExpectedActivePackageDigest: second.Digest,
		ExpectedStagedVersionID:     third.ID,
		ExpectedStagedVersion:       third.Version,
		ExpectedPackageDigest:       third.Digest,
	}); err != nil {
		t.Fatalf("discard staged fixture version: %v", err)
	}
	fixture.versions = []versionGCFixtureVersion{first, second, third}
	return fixture
}

func (f *versionGCFixture) version(version string) versionGCFixtureVersion {
	for _, entry := range f.versions {
		if entry.Version == version {
			return entry
		}
	}
	panic(fmt.Sprintf("fixture version %s not found", version))
}

// seedVersionGCOpenOperation 以 (extension_id, version, digest) 为目标种一条
// 开放 lifecycle operation；返回行 id 便于关闭。
func seedVersionGCOpenOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extensionID, version, digest string) int64 {
	t.Helper()
	var operationID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
		  extension_id, extension_version, package_digest,
		  operation, plan_version, idempotency_key, request_fingerprint,
		  authority_type, authority_snapshot
		) VALUES ($1, $2, $3, 'enable', 'version-gc.test@1', $4, $5, 'builtin', '{}'::jsonb)
		RETURNING id
	`, extensionID, version, digest,
		fmt.Sprintf("gc-open:%s", version), strings.Repeat("e", 64),
	).Scan(&operationID)
	if err != nil {
		t.Fatalf("seed open lifecycle operation: %v", err)
	}
	return operationID
}

func closeVersionGCOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET state = 'enabled', terminal_result = 'succeeded',
		    completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE id = $1
	`, operationID); err != nil {
		t.Fatalf("close seeded lifecycle operation: %v", err)
	}
}

// seedVersionGCPublication 用生产 canonical 函数计算 members_digest 后落一条
// 真实发布修订，把指定版本行钉进 append-only 发布历史。
func seedVersionGCPublication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extensionID string, versionID int64, version, digest string) int64 {
	t.Helper()
	_, membersDigest, err := canonicalPluginRuntimeMembers([]PluginRuntimeMember{{
		ExtensionID: extensionID, ExtensionVersionID: versionID,
		ExtensionVersion: version, PackageDigest: digest,
	}})
	if err != nil {
		t.Fatalf("canonicalize fixture publication member: %v", err)
	}
	// 发布行与成员必须在同一事务：full-set 校验触发器是 DEFERRED，语句级
	// autocommit 会在成员落库前触发校验并拒绝。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture publication tx: %v", err)
	}
	var revision int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (1, $1, 'startup_reconcile')
		RETURNING revision
	`, membersDigest).Scan(&revision); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed fixture publication: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_runtime_publication_members (
		  publication_revision, extension_id, extension_version_id,
		  extension_version, package_digest
		) VALUES ($1, $2, $3, $4, $5)
	`, revision, extensionID, versionID, version, digest); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed fixture publication member: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture publication: %v", err)
	}
	return revision
}

func findVersionGCCandidate(t *testing.T, candidates []RetiredVersionCandidate, version string) (RetiredVersionCandidate, bool) {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Version == version {
			return candidate, true
		}
	}
	return RetiredVersionCandidate{}, false
}

func TestVersionGCCandidatesCriteriaMatrix(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	active := fixture.version("2.0.0")
	// 作用域计划：共享库里可能有上千条历史版本行（versionGCMaxCandidates=1000
	// 会按 extension_id 截断），全局清单会让本用例的 fixture 落在 LIMIT 之外。
	candidates, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: fixture.id})
	if err != nil {
		t.Fatalf("list version gc candidates: %v", err)
	}
	mine := make([]RetiredVersionCandidate, 0)
	for _, candidate := range candidates {
		if candidate.ExtensionID == fixture.id {
			mine = append(mine, candidate)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("candidate versions = %d, want 2 (v1, v3): %#v", len(mine), mine)
	}
	// active 挡 / staged 挡：active v2 与（discard 前）staged v3 都不在清单。
	for _, candidate := range mine {
		if candidate.VersionID == active.ID {
			t.Fatalf("active version leaked into candidates: %#v", candidate)
		}
	}
	// v3 是 rank1 历史（installed_at 最新）→ 窗口内挡。
	v3, ok := findVersionGCCandidate(t, mine, "3.0.0")
	if !ok || v3.BlockedReason != VersionGCBlockedWithinRetention || v3.HistoryRank != 1 {
		t.Fatalf("v3 candidate = %#v, want within_retention at rank 1", v3)
	}
	// v1 是 rank2 历史 → 默认 retain=1 下可回收，磁盘存在。
	v1, ok := findVersionGCCandidate(t, mine, "1.0.0")
	if !ok || !v1.Reclaimable || v1.HistoryRank != 2 || v1.PackagePath != fixture.versions[0].Path {
		t.Fatalf("v1 candidate = %#v, want reclaimable at rank 2", v1)
	}

	// 开放 op 挡（且优先于窗口判定）。
	operationID := seedVersionGCOpenOperation(t, ctx, pool, fixture.id, v1.Version, v1.PackageDigest)
	blocked, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: fixture.id})
	if err != nil {
		t.Fatalf("list candidates with open operation: %v", err)
	}
	if v1, ok := findVersionGCCandidate(t, blocked, "1.0.0"); !ok || v1.BlockedReason != VersionGCBlockedOpenOperation {
		t.Fatalf("v1 with open operation = %#v, want %q", v1, VersionGCBlockedOpenOperation)
	}
	closeVersionGCOperation(t, ctx, pool, operationID)

	// 发布引用挡（优先于窗口判定）：发布历史钉住 v1，且这是永久的。
	seedVersionGCPublication(t, ctx, pool, fixture.id, v1.VersionID, v1.Version, v1.PackageDigest)
	pinned, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: fixture.id})
	if err != nil {
		t.Fatalf("list candidates with publication pin: %v", err)
	}
	if v1, ok := findVersionGCCandidate(t, pinned, "1.0.0"); !ok || v1.BlockedReason != VersionGCBlockedPublicationHistory {
		t.Fatalf("pinned v1 = %#v, want %q", v1, VersionGCBlockedPublicationHistory)
	}
}

func TestVersionGCDryRunHasZeroSideEffects(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	var versionsBefore, confirmationsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, fixture.id).Scan(&versionsBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_gc_confirmations WHERE options->>'extensionId' = $1`, fixture.id).Scan(&confirmationsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{}); err != nil {
		t.Fatalf("dry-run list: %v", err)
	}
	var versionsAfter, confirmationsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, fixture.id).Scan(&versionsAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_gc_confirmations WHERE options->>'extensionId' = $1`, fixture.id).Scan(&confirmationsAfter); err != nil {
		t.Fatal(err)
	}
	if versionsAfter != versionsBefore || confirmationsAfter != confirmationsBefore {
		t.Fatalf("dry-run mutated state: versions %d -> %d, confirmations %d -> %d",
			versionsBefore, versionsAfter, confirmationsBefore, confirmationsAfter)
	}
}

// TestVersionGCExecuteRemovesDiskThenLedger 覆盖验收矩阵：
// 删盘成功 → 台账删（锚点 + 审计逐条可查）；重复执行幂等（无候选、零删除）。
func TestVersionGCExecuteRemovesDiskThenLedger(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	options := VersionGCOptions{ExtensionID: fixture.id}
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		t.Fatalf("create gc confirmation: %v", err)
	}
	if confirmation.CandidateCount != 1 {
		t.Fatalf("candidate count = %d, want 1 (v1 beyond retain=1)", confirmation.CandidateCount)
	}
	result, err := fixture.store.ExecuteVersionGCSweep(ctx, options, confirmation.Token)
	if err != nil {
		t.Fatalf("execute gc sweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].Version != "1.0.0" {
		t.Fatalf("removed = %#v, want exactly v1", result.Removed)
	}
	if len(result.Failed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("unexpected failures/skips: %#v / %#v", result.Failed, result.Skipped)
	}
	if result.BytesFreed <= 0 {
		t.Fatalf("bytes freed = %d, want > 0", result.BytesFreed)
	}
	// 磁盘目录已删。
	if _, err := os.Stat(fixture.versions[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package dir still present after gc: %v", err)
	}
	// 台账行已删。
	var versionRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, fixture.versions[0].ID).Scan(&versionRows); err != nil {
		t.Fatal(err)
	}
	if versionRows != 0 {
		t.Fatalf("version row survived gc")
	}
	// 锚点与审计逐条可查。
	var anchors int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_version_gc_removals
		WHERE extension_id = $1 AND extension_version = '1.0.0' AND bytes_freed > 0
	`, fixture.id).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 {
		t.Fatalf("gc removal anchors = %d, want 1", anchors)
	}
	var audits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.version_gc' AND metadata->>'extensionId' = $1
	`, fixture.id).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("gc audit events = %d, want 1", audits)
	}
	// 幂等：重复执行安全（无候选、零删除）。
	second, err := fixture.store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		t.Fatalf("create second confirmation: %v", err)
	}
	secondResult, err := fixture.store.ExecuteVersionGCSweep(ctx, options, second.Token)
	if err != nil {
		t.Fatalf("second gc sweep: %v", err)
	}
	if len(secondResult.Removed) != 0 || secondResult.CandidateCount != 0 {
		t.Fatalf("second sweep = %#v, want empty idempotent result", secondResult)
	}
	// 空计划的 JSON 线上契约：三个结果数组必须是 [] 而不是 null
	// （真实事故：管理端对 null.length 抛 TypeError，把成功执行误报成失败）。
	encoded, err := json.Marshal(secondResult)
	if err != nil {
		t.Fatal(err)
	}
	var decodedSecond map[string]any
	if err := json.Unmarshal(encoded, &decodedSecond); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"removed", "skipped", "failed"} {
		value, ok := decodedSecond[field].([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("empty sweep %s serialized as %v, want an empty JSON array", field, decodedSecond[field])
		}
	}
}

func TestVersionGCExecuteRejectsInvalidTokens(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	// 空 token 在 store 层就是非法输入。
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, " "); !errors.Is(err, ErrVersionGCInvalidInput) {
		t.Fatalf("empty token error = %v, want ErrVersionGCInvalidInput", err)
	}
	// 未知 token。
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, strings.Repeat("0", 64)); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("unknown token error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
	// token 与当前计划摘要绑定：选项漂移即失效。
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, VersionGCOptions{})
	if err != nil {
		t.Fatalf("create confirmation: %v", err)
	}
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{RetainDays: 30}, confirmation.Token); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("drifted plan error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
	// 一次性语义：消费后重放拒绝。
	fresh, err := fixture.store.CreateVersionGCConfirmation(ctx, VersionGCOptions{})
	if err != nil {
		t.Fatalf("create fresh confirmation: %v", err)
	}
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, fresh.Token); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, fresh.Token); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("replayed token error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
	// 过期 token 拒绝。token 必须每次运行唯一：共享测试库里
	// extension_gc_confirmations.token_hash 是 UNIQUE，固定串会让第二次运行
	// 撞唯一约束（既有用例在已 seed 的 dev 库上不可重复执行）。
	expired := fmt.Sprintf("%064x", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_gc_confirmations (token_hash, plan_digest, candidate_count, options, expires_at, created_at)
		VALUES ($1, $2, 0, '{}'::jsonb, statement_timestamp() - interval '1 minute', statement_timestamp() - interval '20 minutes')
	`, versionGCTokenHash(expired), strings.Repeat("0", 64)); err != nil {
		t.Fatalf("seed expired confirmation: %v", err)
	}
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, expired); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("expired token error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
}

// TestVersionGCPlanDigestFollowsBlockedDrift 验证"dry-run 与 execute 同源判据"
// 的确认语义：候选在两次调用之间被新的发布引用钉住（blocked 集合变化）时，
// 计划摘要随之变化，旧 token 直接失效——不能拿一个按旧判据签发的 token 去执行
// 新判据下的回收。
func TestVersionGCPlanDigestFollowsBlockedDrift(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.versions[0]
	options := VersionGCOptions{ExtensionID: fixture.id}
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		t.Fatalf("create gc confirmation: %v", err)
	}
	if confirmation.CandidateCount != 1 {
		t.Fatalf("candidate count = %d, want 1", confirmation.CandidateCount)
	}
	// 签发之后把 v1 钉进发布历史：可回收集合从 1 条变 0 条。
	seedVersionGCPublication(t, ctx, pool, fixture.id, target.ID, target.Version, target.Digest)
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, options, confirmation.Token); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("stale-plan token error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
	// 重新 dry-run 后，同一计划里已经没有可回收候选。
	fresh, err := fixture.store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		t.Fatalf("create fresh confirmation: %v", err)
	}
	if fresh.CandidateCount != 0 || fresh.PlanDigest == confirmation.PlanDigest {
		t.Fatalf("fresh plan = %#v, want 0 candidates and a different digest", fresh)
	}
}

// TestVersionGCTargetDriftRechecksInsideLock 直接验证锁内复核的私有不变量：
// 判据在加锁与删除之间漂移时必须放弃该条且保留台账行（same-package 测试，
// 该不变量无法通过公开契约稳定构造）。
func TestVersionGCTargetDriftRechecksInsideLock(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.versions[0]

	t.Run("publication pin drifts to blocked", func(t *testing.T) {
		seedVersionGCPublication(t, ctx, pool, fixture.id, target.ID, target.Version, target.Digest)
		err := fixture.store.deleteReclaimableVersionRow(ctx, RetiredVersionCandidate{
			ExtensionID: fixture.id, VersionID: target.ID, Version: target.Version,
			PackageDigest: target.Digest, PackagePath: target.Path,
		}, 128, 0, versionGCAuditModeCLI)
		var drift *versionGCTargetDriftError
		if !errors.As(err, &drift) || drift.reason != VersionGCBlockedPublicationHistory {
			t.Fatalf("drift error = %v, want publication history", err)
		}
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, target.ID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("pinned version row was deleted")
		}
	})

	t.Run("missing row drifts to blocked", func(t *testing.T) {
		err := fixture.store.deleteReclaimableVersionRow(ctx, RetiredVersionCandidate{
			ExtensionID: fixture.id, VersionID: -1, Version: "9.9.9",
			PackageDigest: strings.Repeat("9", 64), PackagePath: target.Path,
		}, 0, 0, versionGCAuditModeCLI)
		var drift *versionGCTargetDriftError
		if !errors.As(err, &drift) {
			t.Fatalf("drift error = %v, want versionGCTargetDriftError", err)
		}
	})
}

// TestVersionGCFKRestrictBackstop 验证数据库最后防线：即便应用判据漏网，
// 发布引用的版本行在 SQL 层也删不掉。
func TestVersionGCFKRestrictBackstop(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.versions[0]
	seedVersionGCPublication(t, ctx, pool, fixture.id, target.ID, target.Version, target.Digest)
	if _, err := pool.Exec(ctx, `DELETE FROM extension_versions WHERE id = $1`, target.ID); err == nil {
		t.Fatalf("FK RESTRICT did not block deletion of a published version")
	} else {
		var pgErr interface{ Error() string }
		if !errors.As(err, &pgErr) || !strings.Contains(err.Error(), "plugin_runtime_publication_members") {
			t.Fatalf("unexpected delete error: %v", err)
		}
	}
}

// TestVersionGCStagingRestoredOnTxFailure 验证失败方向：事务失败 → 台账保留
// 且暂存目录恢复原路径（绝不产生盘缺悬挂态）。
func TestVersionGCStagingRestoredOnTxFailure(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.version("2.0.0") // active 行：锁内复核必然拒绝
	outcome := fixture.store.reclaimOneVersion(ctx, RetiredVersionCandidate{
		ExtensionID: fixture.id, VersionID: target.ID, Version: target.Version,
		PackageDigest: target.Digest, PackagePath: target.Path,
	}, VersionGCOptions{Retain: 1}, 0, versionGCAuditModeCLI)
	if !outcome.skipped || outcome.err != nil || outcome.reason != "version became active or staged" {
		t.Fatalf("outcome = %#v, want drift skip", outcome)
	}
	if _, err := os.Stat(target.Path); err != nil {
		t.Fatalf("staging directory was not restored to the original path: %v", err)
	}
	if _, err := os.Stat(target.Path + versionGCStagingSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory leaked: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, target.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("active version row was deleted by gc")
	}
}

// TestVersionGCDiskDeleteFailureKeepsLedger 验证"删盘失败 → 台账保留且错误
// 可读"：把候选目录的父目录设为不可写，改名（删盘第一步）必然失败。
// root 环境无法用 chmod 制造该失败，跳过而不是伪造。
func TestVersionGCDiskDeleteFailureKeepsLedger(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	if os.Geteuid() == 0 {
		t.Skip("cannot simulate rename failure as root")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.versions[0]
	parent := filepath.Dir(target.Path)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("harden fixture parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	options := VersionGCOptions{ExtensionID: fixture.id}
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		t.Fatalf("create confirmation: %v", err)
	}
	result, err := fixture.store.ExecuteVersionGCSweep(ctx, options, confirmation.Token)
	if err != nil {
		t.Fatalf("execute sweep: %v", err)
	}
	if len(result.Failed) != 1 || len(result.Removed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("result = %#v, want exactly one failed item", result)
	}
	if !strings.Contains(result.Failed[0].Error, "stage package directory") {
		t.Fatalf("failure is not readable: %q", result.Failed[0].Error)
	}
	if _, err := os.Stat(target.Path); err != nil {
		t.Fatalf("package directory did not survive the failed removal: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, target.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("ledger row was deleted despite disk failure")
	}
}

// TestVersionGCMissingDiskBetweenPlanAndRunIsNotGcBusiness 验证执行路径上的
// disk_missing 跳过：盘在计划之后消失的候选被跳过并报告，不删除台账。
func TestVersionGCMissingDiskBetweenPlanAndRunIsNotGcBusiness(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	target := fixture.versions[0]
	if err := os.Remove(filepath.Join(target.Path, "package.bin")); err != nil {
		t.Fatalf("empty fixture dir: %v", err)
	}
	if err := os.Remove(target.Path); err != nil {
		t.Fatalf("remove fixture dir: %v", err)
	}
	outcome := fixture.store.reclaimOneVersion(ctx, RetiredVersionCandidate{
		ExtensionID: fixture.id, VersionID: target.ID, Version: target.Version,
		PackageDigest: target.Digest, PackagePath: target.Path,
	}, VersionGCOptions{Retain: 1}, 0, versionGCAuditModeCLI)
	if !outcome.skipped || outcome.reason != VersionGCBlockedDiskMissing {
		t.Fatalf("outcome = %#v, want disk_missing skip", outcome)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, target.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("ledger row was deleted although the disk was already missing")
	}
}

// ---- 阶段 5：VersionGCAdminService（管理端边界）----

func TestVersionGCAdminServiceRequiresSuperAdmin(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := newVersionGCFixture(t, ctx, pool)
	service := NewVersionGCAdminService(fixture.store)
	pluginManager := identity.Actor{
		ID:     9,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}

	// denied：三个入口都要求 super_admin（任务书硬约束 4），plugin.manage 不够。
	if _, err := service.Candidates(ctx, pluginManager, VersionGCOptions{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("candidates denied error = %v, want ErrPermissionDenied", err)
	}
	if _, err := service.CreateConfirmation(ctx, pluginManager, VersionGCOptions{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("confirmation denied error = %v, want ErrPermissionDenied", err)
	}
	if _, err := service.Execute(ctx, pluginManager, VersionGCOptions{}, strings.Repeat("a", 64)); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("execute denied error = %v, want ErrPermissionDenied", err)
	}
	// denied 不得产生任何副作用（按本 fixture 的 options.extensionId 限定计数，
	// 因为同一隔离库里其他 GC 测试也会写确认表）。
	var confirmations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_gc_confirmations WHERE options->>'extensionId' = $1`, fixture.id).Scan(&confirmations); err != nil {
		t.Fatal(err)
	}
	if confirmations != 0 {
		t.Fatalf("denied confirmation minted rows: %d", confirmations)
	}

	// allowed：super_admin 走通读 + 签发。
	if _, err := service.Candidates(ctx, extensionManager(), VersionGCOptions{}); err != nil {
		t.Fatalf("super_admin candidates: %v", err)
	}
	options := VersionGCOptions{ExtensionID: fixture.id}
	confirmation, err := service.CreateConfirmation(ctx, extensionManager(), options)
	if err != nil {
		t.Fatalf("super_admin confirmation: %v", err)
	}
	// 锚点表的 requested_by_user_id 对 users 有 RESTRICT 外键：先种 actor 用户行。
	// 已 seed 的 dev 库里 id 42 可能已是真实用户，冲突时复用该行即可（断言只关心
	// 锚点/审计确实绑定了这个 actor id）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES ($1, 'gc-admin', 'gc-admin', 'gc-admin@example.com', 'gc-admin@example.com', 'GC Admin')
		ON CONFLICT (id) DO NOTHING
	`, extensionManager().ID); err != nil {
		t.Fatalf("seed gc admin user: %v", err)
	}
	// Execute 绑定 actor：审计 mode=api、锚点 requested_by_user_id=42。
	result, err := service.Execute(ctx, extensionManager(), options, confirmation.Token)
	if err != nil {
		t.Fatalf("super_admin execute: %v", err)
	}
	if len(result.Removed) != 1 {
		t.Fatalf("removed = %d, want 1", len(result.Removed))
	}
	var anchors, apiAudits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_version_gc_removals
		WHERE extension_id = $1 AND requested_by_user_id = $2
	`, fixture.id, extensionManager().ID).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.version_gc'
		  AND metadata->>'extensionId' = $1
		  AND metadata->>'mode' = 'api'
	`, fixture.id).Scan(&apiAudits); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || apiAudits != 1 {
		t.Fatalf("actor binding missing: anchors=%d apiAudits=%d, want 1/1", anchors, apiAudits)
	}
	// CLI 与 API 的执行共用同一 token 语义：重放拒绝。
	if _, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{}, confirmation.Token); !errors.Is(err, ErrVersionGCConfirmationInvalid) {
		t.Fatalf("replayed token error = %v, want ErrVersionGCConfirmationInvalid", err)
	}
}

// retireVersionGCFixture 把一个 fixture 扩展从「活跃集合」里摘掉（仅测试使用）。
//
// 为什么不是 DELETE：`plugin_runtime_publication_members` 对 `extension_versions`
// 是 ON DELETE RESTRICT，且 `plugin_runtime_publications` 有 append-only 触发器——
// **已进入 desired full-set 的精确版本在物理上不可删除**，这是生产完整性约束，
// 测试也不该绕过它（踩过：DELETE 语句被触发器拒绝，行仍留在共享测试库里）。
//
// 后果与正确做法：这些行会一直存在，但**不得保持 enabled**。desired full-set 只看
// enabled 的扩展；把状态退休成 disabled、并清掉磁盘已不存在的 staged 指针，后面的
// 包（例如 bootstrap 的 lifecycle e2e）在计算 full-set 时就不会再去读那个已被
// t.TempDir 删掉的包目录。这正是之前 `static preflight: invalid manifest` 的根因。
func retireVersionGCFixture(t *testing.T, pool *pgxpool.Pool, extensionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		UPDATE extensions
		SET status = 'disabled', staged_version_id = NULL, updated_at = now()
		WHERE id = $1
	`, extensionID); err != nil {
		t.Errorf("retire version-gc fixture %s: %v", extensionID, err)
	}
}

// ---- 主题运行时发布账本的钉住判据（隔离 schema）----
//
// 为什么必须用隔离 schema：theme_runtime_publications 是 append-only 账本，
// 往共享测试库里插一条测试发布行会变成全局最新修订，真实 watcher 下次启动
// 就会去 apply 一个只存在于测试里的制品并永久失败。这里复用主题发布 fixture
// 的 schema 隔离，绝不在共享库的发布账本里造行。

type versionGCThemeVersion struct {
	Version string
	ID      int64
	Digest  string
	Path    string
}

// saveVersionGCThemeVersion 写入一个主题版本，并配一个真实磁盘目录：GC 的
// disk 判据和"制品仍在"断言都需要它存在。
func saveVersionGCThemeVersion(t *testing.T, fixture *themePublicationPGFixture, label, version, digest string) versionGCThemeVersion {
	t.Helper()
	extensionID := fixture.prefix + "." + label
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.bin"), []byte(strings.Repeat("x", 128)), 0o644); err != nil {
		t.Fatalf("write theme gc fixture file: %v", err)
	}
	installed, err := fixture.store.SaveInstalled(fixture.ctx, SaveInstalledInput{
		Manifest:    Manifest{ID: extensionID, Name: label, Version: version, Type: TypeTheme},
		PackagePath: dir, PackageDigest: digest,
	})
	if err != nil {
		t.Fatalf("save theme gc fixture version %s: %v", version, err)
	}
	fixture.extensionIDs = append(fixture.extensionIDs, extensionID)
	entry := versionGCThemeVersion{Version: version, Digest: digest, Path: dir, ID: installed.ActiveVersionID}
	if installed.StagedVersion != nil {
		entry.ID = installed.StagedVersion.ID
	}
	if entry.ID <= 0 {
		t.Fatalf("theme gc fixture version %s has no version row: %#v", version, installed)
	}
	return entry
}

// discardVersionGCThemeCandidate 走真实的 staged 候选丢弃路径：只清指针，
// 版本行保留成历史版本（且从未进入发布账本）。
func discardVersionGCThemeCandidate(t *testing.T, fixture *themePublicationPGFixture, extensionID string, active, staged versionGCThemeVersion) {
	t.Helper()
	if _, err := fixture.store.DiscardStagedVersion(fixture.ctx, StagedVersionCASInput{
		ExtensionID:                 extensionID,
		ExpectedActiveVersionID:     active.ID,
		ExpectedActiveVersion:       active.Version,
		ExpectedActivePackageDigest: active.Digest,
		ExpectedStagedVersionID:     staged.ID,
		ExpectedStagedVersion:       staged.Version,
		ExpectedPackageDigest:       staged.Digest,
	}); err != nil {
		t.Fatalf("discard theme gc fixture version %s: %v", staged.Version, err)
	}
}

// pinnedThemeFixture 是"上一版被发布账本钉住"的主题夹具：source 版本曾经
// active，激活到 active 版本时它的三元组被写进 append-only 发布账本。
type pinnedThemeFixture struct {
	ExtensionID string
	Source      versionGCThemeVersion
	Active      versionGCThemeVersion
	Publication ThemeRuntimePublication
}

// pinThemePublicationSource 走真实激活路径构造上述状态，而不是直接往账本插行：
// 只有真实路径能保证夹具形态（source 三元组、reason、审批列）与生产一致。
func pinThemePublicationSource(t *testing.T, fixture *themePublicationPGFixture, label string) pinnedThemeFixture {
	t.Helper()
	ctx := fixture.ctx
	extensionID := fixture.prefix + "." + label
	base := fixture.activeTheme()
	source := saveVersionGCThemeVersion(t, fixture, label, "1.0.0", strings.Repeat("a", 64))
	if _, err := fixture.store.ActivateThemeExact(ctx, extensionID, exactThemeActivationInput(
		base,
		Extension{ID: extensionID, Type: TypeTheme, Version: source.Version, PackageDigest: source.Digest},
		0, false,
	)); err != nil {
		t.Fatalf("activate pinned theme source version: %v", err)
	}
	active := saveVersionGCThemeVersion(t, fixture, label, "2.0.0", strings.Repeat("b", 64))
	activation, err := fixture.store.ActivateThemeExact(ctx, extensionID, exactThemeActivationInput(
		Extension{ID: extensionID, Type: TypeTheme, Version: source.Version, PackageDigest: source.Digest},
		Extension{ID: extensionID, Type: TypeTheme, Version: active.Version, PackageDigest: active.Digest},
		0, false,
	))
	if err != nil {
		t.Fatalf("activate pinned theme active version: %v", err)
	}
	return pinnedThemeFixture{
		ExtensionID: extensionID, Source: source, Active: active, Publication: activation.Publication,
	}
}

// TestVersionGCBlocksThemePublicationSource 钉住 2026-10-27 事故的根因：
// 主题激活会把"上一版"的 source 三元组写进 append-only 发布账本，而
// ThemeRuntimeWatcher 每次进程启动都要回查这条三元组。GC 若回收它，
// apply 就永久 ErrExtensionVersionNotFound。
func TestVersionGCBlocksThemePublicationSource(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "gc-theme-pin")
	ctx := fixture.ctx
	pinnedTheme := pinThemePublicationSource(t, fixture, "pinned")
	pinnedID, v1 := pinnedTheme.ExtensionID, pinnedTheme.Source

	// 前置：v1 确实是这次激活的 source 三元组，否则下面的钉住结论没有意义。
	activation := pinnedTheme.Publication
	if activation.SourceThemeID != pinnedID ||
		activation.SourceThemeVersion != v1.Version ||
		activation.SourcePackageDigest != v1.Digest {
		t.Fatalf("activation source tuple = %#v, want %s/%s/%s",
			activation, pinnedID, v1.Version, v1.Digest)
	}

	candidates, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: pinnedID})
	if err != nil {
		t.Fatalf("list version gc candidates: %v", err)
	}
	pinned, ok := findVersionGCCandidate(t, candidates, v1.Version)
	if !ok || pinned.Reclaimable || pinned.BlockedReason != VersionGCBlockedPublicationHistory {
		t.Fatalf("pinned theme version = %#v, want blocked by %q", pinned, VersionGCBlockedPublicationHistory)
	}

	// dry-run 与 execute 同源判据：计划里不含它，执行也不会碰它。
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, VersionGCOptions{ExtensionID: pinnedID})
	if err != nil {
		t.Fatalf("create gc confirmation: %v", err)
	}
	if confirmation.CandidateCount != 0 {
		t.Fatalf("dry-run plan count = %d, want 0 (the only historical version is pinned)", confirmation.CandidateCount)
	}
	result, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{ExtensionID: pinnedID}, confirmation.Token)
	if err != nil {
		t.Fatalf("execute gc sweep: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Failed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("sweep touched a pinned version: removed=%#v failed=%#v skipped=%#v",
			result.Removed, result.Failed, result.Skipped)
	}
	var rows int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, v1.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("pinned version row was deleted")
	}
	if _, err := os.Stat(v1.Path); err != nil {
		t.Fatalf("pinned package dir was removed: %v", err)
	}

	// P1：主题发布账本没有 FK 背线，锁内复核是它唯一的防线。这里绕过
	// ListVersionGCCandidates 直接走删除路径——只走公开 sweep 时，计划阶段的
	// blocked 判据会把这条路径盖住（reviewer 变异 B 存活的原因）。
	err = fixture.store.deleteReclaimableVersionRow(ctx, RetiredVersionCandidate{
		ExtensionID: pinnedID, VersionID: v1.ID, Version: v1.Version,
		PackageDigest: v1.Digest, PackagePath: v1.Path,
	}, 128, 0, versionGCAuditModeCLI)
	var drift *versionGCTargetDriftError
	if !errors.As(err, &drift) || drift.reason != VersionGCBlockedPublicationHistory {
		t.Fatalf("in-lock theme recheck error = %v, want versionGCTargetDriftError(%q)",
			err, VersionGCBlockedPublicationHistory)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, v1.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("version row was deleted despite the in-lock theme recheck")
	}
}

// TestVersionGCRemovesUnreferencedThemeVersion 是 allowed 侧：没有进入任何
// 发布账本、且超出保留窗口的主题历史版本仍然可回收（判据没有把主题版本
// 一刀切成不可回收）。
func TestVersionGCRemovesUnreferencedThemeVersion(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "gc-theme-free")
	ctx := fixture.ctx
	freeID := fixture.prefix + ".free"

	v1 := saveVersionGCThemeVersion(t, fixture, "free", "1.0.0", strings.Repeat("c", 64))
	v2 := saveVersionGCThemeVersion(t, fixture, "free", "2.0.0", strings.Repeat("d", 64))
	discardVersionGCThemeCandidate(t, fixture, freeID, v1, v2)
	v3 := saveVersionGCThemeVersion(t, fixture, "free", "3.0.0", strings.Repeat("e", 64))
	discardVersionGCThemeCandidate(t, fixture, freeID, v1, v3)

	candidates, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: freeID})
	if err != nil {
		t.Fatalf("list version gc candidates: %v", err)
	}
	// v3 是最近的历史版本（rank 1）→ 默认 retain=1 保留；v2 是 rank 2 → 可回收。
	kept, ok := findVersionGCCandidate(t, candidates, v3.Version)
	if !ok || kept.Reclaimable || kept.BlockedReason != VersionGCBlockedWithinRetention {
		t.Fatalf("newest historical version = %#v, want %q", kept, VersionGCBlockedWithinRetention)
	}
	reclaimable, ok := findVersionGCCandidate(t, candidates, v2.Version)
	if !ok || !reclaimable.Reclaimable {
		t.Fatalf("unreferenced historical version = %#v, want reclaimable", reclaimable)
	}

	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, VersionGCOptions{ExtensionID: freeID})
	if err != nil {
		t.Fatalf("create gc confirmation: %v", err)
	}
	if confirmation.CandidateCount != 1 {
		t.Fatalf("dry-run plan count = %d, want 1", confirmation.CandidateCount)
	}
	result, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{ExtensionID: freeID}, confirmation.Token)
	if err != nil {
		t.Fatalf("execute gc sweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].Version != v2.Version {
		t.Fatalf("removed = %#v, want exactly %s", result.Removed, v2.Version)
	}
	var rows int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, v2.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("unreferenced version row survived gc")
	}
	if _, err := os.Stat(v2.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced package dir still present after gc: %v", err)
	}
}

func findVersionGCCandidateByDigest(t *testing.T, candidates []RetiredVersionCandidate, digest string) (RetiredVersionCandidate, bool) {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.PackageDigest == digest {
			return candidate, true
		}
	}
	return RetiredVersionCandidate{}, false
}

// TestVersionGCThemeSourceMatchRequiresExactDigest 是判据的负例（reviewer 变异 C）：
// source 匹配必须是 id + version + digest 三列合取。同一 extension_id、同一
// version、只有 digest 不同的历史行若被误判成 pinned，"同版本号不同制品"就会
// 永久无法回收——`UNIQUE (extension_id, version, package_digest)` 明确允许它们并存。
func TestVersionGCThemeSourceMatchRequiresExactDigest(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "gc-theme-digest")
	ctx := fixture.ctx
	pinned := pinThemePublicationSource(t, fixture, "digest")

	// 两条同 version、不同 digest 的历史版本：先建的 installed_at 更早，会被
	// 后建的挤到 rank 2，从而超出默认保留窗口（retain=1）成为真正可回收的候选。
	unreferenced := saveVersionGCThemeVersion(t, fixture, "digest", "1.0.0", strings.Repeat("c", 64))
	discardVersionGCThemeCandidate(t, fixture, pinned.ExtensionID, pinned.Active, unreferenced)
	newest := saveVersionGCThemeVersion(t, fixture, "digest", "1.0.0", strings.Repeat("d", 64))
	discardVersionGCThemeCandidate(t, fixture, pinned.ExtensionID, pinned.Active, newest)

	candidates, err := fixture.store.ListVersionGCCandidates(ctx, VersionGCOptions{ExtensionID: pinned.ExtensionID})
	if err != nil {
		t.Fatalf("list version gc candidates: %v", err)
	}
	// 前置：三条历史版本共享同一个 version 字符串，只有 digest 不同。
	if pinned.Source.Version != unreferenced.Version || unreferenced.Version != newest.Version {
		t.Fatalf("fixture versions differ: %q/%q/%q", pinned.Source.Version, unreferenced.Version, newest.Version)
	}
	pinnedCandidate, ok := findVersionGCCandidateByDigest(t, candidates, pinned.Source.Digest)
	if !ok || pinnedCandidate.Reclaimable || pinnedCandidate.BlockedReason != VersionGCBlockedPublicationHistory {
		t.Fatalf("pinned source = %#v, want %q", pinnedCandidate, VersionGCBlockedPublicationHistory)
	}
	negative, ok := findVersionGCCandidateByDigest(t, candidates, unreferenced.Digest)
	if !ok || !negative.Reclaimable {
		t.Fatalf("same id+version with a different digest = %#v, want reclaimable", negative)
	}
	if negative.ExtensionID != pinnedCandidate.ExtensionID || negative.Version != pinnedCandidate.Version {
		t.Fatalf("negative case is not same id+version: %#v vs %#v", negative, pinnedCandidate)
	}

	// 端到端：作用域计划只应包含这条同 version、不同 digest 的历史版本。
	confirmation, err := fixture.store.CreateVersionGCConfirmation(ctx, VersionGCOptions{ExtensionID: pinned.ExtensionID})
	if err != nil {
		t.Fatalf("create gc confirmation: %v", err)
	}
	if confirmation.CandidateCount != 1 {
		t.Fatalf("dry-run plan count = %d, want 1", confirmation.CandidateCount)
	}
	result, err := fixture.store.ExecuteVersionGCSweep(ctx, VersionGCOptions{ExtensionID: pinned.ExtensionID}, confirmation.Token)
	if err != nil {
		t.Fatalf("execute gc sweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].PackageDigest != unreferenced.Digest {
		t.Fatalf("removed = %#v, want exactly digest %s", result.Removed, unreferenced.Digest)
	}
	var rows int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM extension_versions WHERE id = $1`, pinned.Source.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("pinned source row was removed by the scoped sweep")
	}
}
