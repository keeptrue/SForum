package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// PublishPluginRuntimeArtifactRepairTx 是「durable 行指向坏制品」恢复路径里**唯一会写库**
// 的一段（CAS active 指针 + 追加 publication revision + audit）。恢复路径必须在故障时
// 能用，所以这段必须有真实 PG 集成测试：只读自检与 dry-run 的测试覆盖不到它。
//
// 两个场景都跑真实事务：
//  1. 正常修复：CAS 成功 → 新 revision 追加 → 成员 digest 换成候选 → audit 写入；
//  2. CAS 冲突：active 指针已被别处改掉 → 返回冲突，且**什么都没写**。
//
// 跑法：`./scripts/test-api-isolated.sh ./app/Models/Extensions/`
// （testdb 只认 SFORUM_TEST_DATABASE_URL，裸 go test 会跳过）。

const (
	repairBadVersionID       int64 = 101
	repairCandidateVersionID int64 = 201
)

// repairFixture 在共享 publication fixture 上加一条「同版本、不同 digest」的候选版本行，
// 并把 active 指针指向坏制品。
func newArtifactRepairFixture(t *testing.T, label string) *pluginRuntimePublicationPGFixture {
	t.Helper()
	fixture := newPluginRuntimePublicationPGFixture(t, label)
	// 修复的 CAS 会顺带写 extensions.updated_at（PublishPluginRuntimeArtifactRepairTx）。
	// 共享 fixture 的表形状里没有这一列，这里按需补上——不去改共享 DDL，
	// 否则会与 legacy fixture 自己的 ALTER 撞成 "column already exists"。
	if _, err := fixture.pool.Exec(fixture.ctx,
		`ALTER TABLE extensions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`); err != nil {
		t.Fatalf("add extensions.updated_at: %v", err)
	}
	// pgx 的预处理语句一次只能一条：分两次执行，别合并成多语句。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO extension_versions (id, extension_id, version, package_digest)
		VALUES ($1, 'fixture.plugin', '1.0.0', repeat('e', 64))
	`, repairCandidateVersionID); err != nil {
		t.Fatalf("seed repair candidate version: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE extensions SET active_version_id = $1 WHERE id = 'fixture.plugin'`,
		repairBadVersionID); err != nil {
		t.Fatalf("seed active pointer: %v", err)
	}
	if _, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 7,
		[]PluginRuntimeMember{fixture.firstMember(), fixture.secondMember()},
	); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	return fixture
}

func (f *pluginRuntimePublicationPGFixture) activeVersionID(t *testing.T, extensionID string) int64 {
	t.Helper()
	var active *int64
	if err := f.pool.QueryRow(f.ctx,
		`SELECT active_version_id FROM extensions WHERE id = $1`, extensionID).Scan(&active); err != nil {
		t.Fatalf("read active version of %s: %v", extensionID, err)
	}
	if active == nil {
		return 0
	}
	return *active
}

func (f *pluginRuntimePublicationPGFixture) countArtifactRepairAudits(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events WHERE action = 'extension.artifact_repair'
	`).Scan(&count); err != nil {
		t.Fatalf("count artifact repair audits: %v", err)
	}
	return count
}

func (f *pluginRuntimePublicationPGFixture) publicationRevisions(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM plugin_runtime_publications`).Scan(&count); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	return count
}

// runArtifactRepair 复刻 CLI 的 executeArtifactRepair：在调用方事务里跑，错误即回滚。
func runArtifactRepair(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	repair PluginRuntimeArtifactRepair,
) (PluginRuntimePublication, error) {
	t.Helper()
	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatalf("begin repair tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	publication, err := PublishPluginRuntimeArtifactRepairTx(fixture.ctx, tx, repair)
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatalf("commit repair tx: %v", err)
	}
	return publication, nil
}

func TestPostgresPluginRuntimeArtifactRepairWritesCASRevisionAndAudit(t *testing.T) {
	fixture := newArtifactRepairFixture(t, "artifact-repair")
	before, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatalf("latest publication: %v", err)
	}
	if got := fixture.activeVersionID(t, "fixture.plugin"); got != repairBadVersionID {
		t.Fatalf("seed active version=%d want %d", got, repairBadVersionID)
	}
	if got := fixture.countArtifactRepairAudits(t); got != 0 {
		t.Fatalf("seed audit count=%d want 0", got)
	}

	publication, err := runArtifactRepair(t, fixture, PluginRuntimeArtifactRepair{
		ExtensionID:        "fixture.plugin",
		CurrentVersionID:   repairBadVersionID,
		CandidateVersionID: repairCandidateVersionID,
		Reason:             "backend/plugin missing after out-of-band deletion",
		ActorUserID:        7,
	})
	if err != nil {
		t.Fatalf("artifact repair: %v", err)
	}

	// 1. 追加了新 revision，且与旧 revision 不是同一条。
	if publication.Revision <= before.Revision {
		t.Fatalf("repair revision=%d must exceed previous %d", publication.Revision, before.Revision)
	}
	if publication.Reason != PluginRuntimePublicationRecovery {
		t.Fatalf("repair publication reason=%q want %q", publication.Reason, PluginRuntimePublicationRecovery)
	}

	// 2. 成员 digest 变成候选值，其余成员原样保留（修复是单成员操作）。
	byID := map[string]PluginRuntimeMember{}
	for _, member := range publication.Members {
		byID[member.ExtensionID] = member
	}
	repaired, ok := byID["fixture.plugin"]
	if !ok {
		t.Fatalf("repaired publication lost fixture.plugin: %#v", publication.Members)
	}
	if repaired.ExtensionVersionID != repairCandidateVersionID ||
		repaired.PackageDigest != strings.Repeat("e", 64) ||
		repaired.ExtensionVersion != "1.0.0" {
		t.Fatalf("repaired member=%#v", repaired)
	}
	if other := byID["second.plugin"]; other.PackageDigest != strings.Repeat("c", 64) {
		t.Fatalf("untouched member changed: %#v", other)
	}

	// 3. durable 成员行与 active 指针都已落库（不只是返回值）。
	var (
		memberDigest string
		memberCount  int
	)
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT package_digest FROM plugin_runtime_publication_members
		WHERE publication_revision = $1 AND extension_id = 'fixture.plugin'
	`, publication.Revision).Scan(&memberDigest); err != nil {
		t.Fatalf("read repaired member row: %v", err)
	}
	if memberDigest != strings.Repeat("e", 64) {
		t.Fatalf("durable member digest=%s", memberDigest)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM plugin_runtime_publication_members WHERE publication_revision = $1
	`, publication.Revision).Scan(&memberCount); err != nil {
		t.Fatalf("count repaired members: %v", err)
	}
	if memberCount != 2 {
		t.Fatalf("repaired publication member count=%d want 2", memberCount)
	}
	if got := fixture.activeVersionID(t, "fixture.plugin"); got != repairCandidateVersionID {
		t.Fatalf("active version=%d want %d", got, repairCandidateVersionID)
	}

	// 4. audit 恰好一条，metadata 记录 from/to 与 revision。
	var metadata []byte
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT metadata FROM audit_events WHERE action = 'extension.artifact_repair'
	`).Scan(&metadata); err != nil {
		t.Fatalf("read artifact repair audit: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("decode audit metadata: %v (%s)", err, string(metadata))
	}
	if decoded["extensionId"] != "fixture.plugin" ||
		decoded["fromDigest"] != strings.Repeat("b", 64) ||
		decoded["toDigest"] != strings.Repeat("e", 64) ||
		int64(decoded["publicationRevision"].(float64)) != publication.Revision {
		t.Fatalf("audit metadata=%s", string(metadata))
	}
	if got := fixture.countArtifactRepairAudits(t); got != 1 {
		t.Fatalf("audit count=%d want 1", got)
	}
}

func TestPostgresPluginRuntimeArtifactRepairRejectsDriftedActivePointerWithoutWriting(t *testing.T) {
	fixture := newArtifactRepairFixture(t, "artifact-repair-cas")
	before, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatalf("latest publication: %v", err)
	}
	// 模拟并发生命周期写入：publication 仍钉 101，但 active 指针已被别处改成候选。
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE extensions SET active_version_id = $1 WHERE id = 'fixture.plugin'`,
		repairCandidateVersionID); err != nil {
		t.Fatalf("drift active pointer: %v", err)
	}
	publicationsBefore := fixture.publicationRevisions(t)
	auditsBefore := fixture.countArtifactRepairAudits(t)

	_, err = runArtifactRepair(t, fixture, PluginRuntimeArtifactRepair{
		ExtensionID:        "fixture.plugin",
		CurrentVersionID:   repairBadVersionID,
		CandidateVersionID: repairCandidateVersionID,
		Reason:             "stale repair attempt",
	})
	if !errors.Is(err, ErrPluginRuntimeArtifactRepairConflict) {
		t.Fatalf("drifted repair error=%v want ErrPluginRuntimeArtifactRepairConflict", err)
	}

	// 冲突必须什么都不写：revision 数、audit 数、active 指针、最新 publication 都不动。
	if got := fixture.publicationRevisions(t); got != publicationsBefore {
		t.Fatalf("conflict wrote a publication: %d -> %d", publicationsBefore, got)
	}
	if got := fixture.countArtifactRepairAudits(t); got != auditsBefore {
		t.Fatalf("conflict wrote an audit row: %d -> %d", auditsBefore, got)
	}
	if got := fixture.activeVersionID(t, "fixture.plugin"); got != repairCandidateVersionID {
		t.Fatalf("conflict moved active pointer to %d", got)
	}
	after, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("latest publication after conflict=%#v err=%v (before revision %d)", after, err, before.Revision)
	}
}

// 事务隔离级别不是 read committed 时必须在拿锁前拒绝：advisory lock 之后的下一条语句
// 看不到新快照，修复就会基于陈旧前提写库。
//
// 注意断言的是**实现当前实际返回的哨兵**：这条守卫返回通用的
// ErrPluginRuntimePublicationConflict，而不是同族的 ErrPluginRuntimeArtifactRepairConflict。
// 两者对 CLI 都是非零退出，行为上无差别；本测试只钉「守卫在、且在拿锁前拒绝」，
// 不改判据（哨兵统一与否不属本次返工范围）。
func TestPostgresPluginRuntimeArtifactRepairRejectsNonReadCommitted(t *testing.T) {
	fixture := newArtifactRepairFixture(t, "artifact-repair-iso")
	tx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		t.Fatalf("begin repeatable read tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = PublishPluginRuntimeArtifactRepairTx(fixture.ctx, tx, PluginRuntimeArtifactRepair{
		ExtensionID:        "fixture.plugin",
		CurrentVersionID:   repairBadVersionID,
		CandidateVersionID: repairCandidateVersionID,
		Reason:             "wrong isolation",
	})
	if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
		t.Fatalf("repeatable read repair error=%v want ErrPluginRuntimePublicationConflict", err)
	}
}
