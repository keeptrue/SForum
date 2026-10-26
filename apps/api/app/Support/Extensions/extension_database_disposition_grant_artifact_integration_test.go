package extensionsruntime

import (
	"context"
	"strings"
	"testing"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
)

// TestPostgresExtensionDatabaseDispositionToleratesStaleActiveGrant 固化
// extension_database_grants「一个扩展可以有多条 active」这一事实。
//
// 唯一索引只保证同一 artifact 只有一条 active，不保证一个扩展只有一条
// （database/migrations/202607150022_extension_database_additive_grants.sql:60，
// 注释也写明 source/target 两个 exact artifact 可以同时在线）。而
// validateExactActiveDispositionGrant 过去只按 extension_id + status='active' 取首行，
// 于是正常 install → upgrade → uninstall 留下的旧 grant 会遮住 tombstone 要的新
// grant，purge 永久 conflict（生产库现场：marketplace 的 4272 遮住 4283）。
//
// 修法：把 exact-artifact 条件（version_id / version / digest）写进查询，
// 无匹配行即通过。
func TestPostgresExtensionDatabaseDispositionToleratesStaleActiveGrant(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve,
	)
	artifact := lifecycleCleanupDispositionArtifact(t, h)
	identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	// 先注册回收再写库：stale grant / resources 在 provision 之前就落库，
	// provision 中途失败时也要把 schema、role、grant 一并清掉。
	cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
	// 升级前的旧 artifact 先落库，物理顺序在前 —— 与生产现场同形。
	staleVersionID := insertStaleExtensionDatabaseGrant(t, h, artifact)
	_, _, live := provisionExtensionDatabaseDispositionFixture(t, h, artifact)
	defer live.Close(context.Background())

	assertStaleGrantShadowsArtifact(t, h, artifact, staleVersionID)

	staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
	if err != nil {
		t.Fatal(err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)

	request := ExtensionDatabaseDispositionRequest{
		CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
		CleanupMode: h.mode, Artifact: artifact,
		ExportArtifactID: h.exportArtifactID, ExportDigest: h.exportDigest,
	}
	receipt, err := NewPostgresExtensionDatabaseDisposition(h.pool).
		ApplyLifecycleDataDisposition(h.ctx, request)
	if err != nil {
		t.Fatalf("disposition with a stale active grant: %v", err)
	}
	if receipt.CleanupID != request.CleanupID || !receipt.CredentialRevoked || !receipt.ResourceExisted {
		t.Fatalf("unexpected disposition receipt: %#v", receipt)
	}

	var activeGrants, activeCredentials, staleRevoked int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_grants WHERE extension_id = $1 AND status = 'active'),
		  (SELECT count(*) FROM extension_database_credentials WHERE extension_id = $1 AND status = 'active'),
		  (SELECT count(*) FROM extension_database_grants
		     WHERE extension_id = $1 AND extension_version_id = $2 AND status = 'revoked')
	`, artifact.ExtensionID, staleVersionID).Scan(&activeGrants, &activeCredentials, &staleRevoked); err != nil {
		t.Fatal(err)
	}
	if activeGrants != 0 || activeCredentials != 0 {
		t.Fatalf("active grant/credential rows remain: %d/%d", activeGrants, activeCredentials)
	}
	if staleRevoked != 1 {
		t.Fatalf("stale grant must be revoked by disposition, revoked rows = %d", staleRevoked)
	}
}

// insertStaleExtensionDatabaseGrant 造一条「升级前 artifact」的 active grant，
// 返回它的 extension_version_id。它先于目标 artifact 落库，物理顺序在前。
func insertStaleExtensionDatabaseGrant(
	t *testing.T,
	h *lifecycleCleanupIntegration,
	artifact ExtensionDatabaseArtifact,
) int64 {
	t.Helper()
	identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	if _, err := ensureExtensionDatabaseResources(h.ctx, tx, artifact.ExtensionID, identifiers); err != nil {
		t.Fatal(err)
	}
	stale := artifact
	// 旧 artifact 取「排序上必然在前」的最小身份：version_id=1、version="0"、
	// digest 全零。active-artifact 唯一索引的前缀是 extension_id，规划器按索引序
	// 返回时这三元组一定排在真实 artifact 之前；退化成顺序扫描时它也是先插入的
	// 那一行（目标 grant 随后还会被 UPDATE 推到堆尾）。两种计划下都遮蔽目标，
	// 与生产库里 4272（0.1.0）遮住 4283 同形。
	stale.VersionID = 1
	stale.Version = "0"
	stale.PackageDigest = strings.Repeat("0", 64)
	if _, err := upsertExtensionDatabaseGrant(h.ctx, tx, ExtensionDatabaseGrantRequest{
		Artifact: stale, ActorUserID: h.actorUserID, AuditEventID: h.auditEventID,
	}, extensions.ManifestDatabase{
		ContractVersion: "fixture.database@1", Authority: "own_schema",
		Schema: "fixture", Role: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	return stale.VersionID
}

// assertStaleGrantShadowsArtifact 是 fixture 前置断言：库里必须真有两条 active
// grant，且「按 extension 取首行」拿到的是旧 artifact —— 这就是缺陷形态本身。
// 一旦物理顺序变化导致不再遮蔽，这个断言会先失败，而不是让用例静默通过。
func assertStaleGrantShadowsArtifact(
	t *testing.T,
	h *lifecycleCleanupIntegration,
	artifact ExtensionDatabaseArtifact,
	staleVersionID int64,
) {
	t.Helper()
	var active int
	var shadowVersionID int64
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM extension_database_grants WHERE extension_id = $1 AND status = 'active'
	`, artifact.ExtensionID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("fixture must leave two active grants (install → upgrade → uninstall), got %d", active)
	}
	if err := h.pool.QueryRow(h.ctx, `
		SELECT extension_version_id FROM extension_database_grants
		WHERE extension_id = $1 AND status = 'active' LIMIT 1
	`, artifact.ExtensionID).Scan(&shadowVersionID); err != nil {
		t.Fatal(err)
	}
	if shadowVersionID != staleVersionID {
		t.Fatalf("fixture must shadow the retained artifact: first active grant = %d, stale = %d",
			shadowVersionID, staleVersionID)
	}
}
