package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// retainedPurger 是只回答"身份保留"的 purger：用它把 finalizer 的验收路径
// （receipt → finalized → identityRetained）与物理 purge 解耦，物理层由
// TestLifecycleCleanupPurge* 覆盖。它像真 purger 一样写一条 extension.uninstall
// 证据行，这样"重复 finalize 会不会重复写证据"可以被实证。
type retainedPurger struct {
	pool  *pgxpool.Pool
	calls int
}

func (p *retainedPurger) PurgeLifecycleHostCleanup(
	ctx context.Context,
	request extensionsruntime.LifecycleCleanupPurgeRequest,
) (extensionsruntime.LifecycleCleanupPurgeReceipt, error) {
	p.calls++
	metadata, err := json.Marshal(map[string]any{
		"mode": "lifecycle_purge", "cleanupId": request.CleanupID,
		"operationId": request.OperationID, "extensionId": request.RetainedExtensionID,
		"identityRetained": true, "cleared": nil,
	})
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	if _, err := p.pool.Exec(ctx, `
		INSERT INTO audit_events (action, metadata) VALUES ('extension.uninstall', $1::jsonb)
	`, string(metadata)); err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	proof, err := json.Marshal(map[string]any{
		"schema":    "sforum.lifecycle.host-purge-proof@1",
		"cleanupId": request.CleanupID, "operationId": request.OperationID,
		"extensionId":    request.RetainedExtensionID,
		"identityPurged": false, "identityRetained": true,
		"packagePurged": true, "runtimeRecoveryPurged": true,
		"dataDisposition": "preserved",
	})
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	return extensionsruntime.LifecycleCleanupPurgeReceipt{
		CleanupID: request.CleanupID, OperationID: request.OperationID,
		CleanupMode: request.CleanupMode, RetainedPackageDigest: request.RetainedPackageDigest,
		ReceiptID:      "host-purge-retained-fixture",
		IdentityPurged: false, PackagePurged: true, RuntimeRecoveryPurged: true,
		DataDisposition: "preserved", Proof: proof,
	}, nil
}

// 端到端：身份保留的 purge 必须让 finalizer 收敛到 finalized（而不是卡在
// physical_purge），PhysicalPurgeCompleted 如实为 false；重复调用必须早返回、
// 不重复写证据。
func TestFinalizerConvergesRetainedIdentityPurge(t *testing.T) {
	fixture := newLifecyclePurgeFixture(t, true)
	purger := &retainedPurger{pool: fixture.pool}
	finalizer := extensionsruntime.NewPostgresLifecycleBoundaryCleanupFinalizer(fixture.pool, purger)

	result, err := finalizer.FinalizeLifecycleHostCleanup(fixture.ctx, fixture.operation)
	if err != nil {
		t.Fatalf("finalizer must accept a retained identity receipt: %v", err)
	}
	if result.Status != "finalized" {
		t.Fatalf("cleanup status = %q, want finalized", result.Status)
	}
	if result.PhysicalPurgeCompleted {
		t.Fatal("retained identity must report PhysicalPurgeCompleted=false, not lie")
	}

	var status string
	var finalizedAt *string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status, finalized_at::text FROM extension_lifecycle_cleanup_records WHERE cleanup_id = $1
	`, fixture.cleanupRow).Scan(&status, &finalizedAt); err != nil {
		t.Fatal(err)
	}
	if status != "finalized" || finalizedAt == nil {
		t.Fatalf("durable cleanup record = %q finalizedAt=%v, want finalized with timestamp", status, finalizedAt)
	}

	// 第二次 finalize 必须被 Status=="finalized" 早返回短路：不再调用 purger，
	// 也就不再写证据。这是"审计不刷屏"的实证。
	replayed, err := finalizer.FinalizeLifecycleHostCleanup(fixture.ctx, fixture.operation)
	if err != nil {
		t.Fatalf("replay finalize must succeed: %v", err)
	}
	if replayed.Status != "finalized" {
		t.Fatalf("replay status = %q, want finalized", replayed.Status)
	}
	if purger.calls != 1 {
		t.Fatalf("purger invoked %d time(s) across two finalize calls, want 1", purger.calls)
	}
	var audits int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.uninstall'
		  AND (metadata->>'operationId')::bigint = $1
	`, fixture.operation).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit rows after two finalize calls = %d, want exactly 1", audits)
	}
}

// 反向：没有 retained 语义的 receipt 仍然必须被拒——不能变成"谁都放行"。
func TestFinalizerRejectsUnjustifiedIdentityRetention(t *testing.T) {
	request := productionLifecycleCleanupTestRequest(t)
	database := productionLifecycleDatabaseReceipt(t, request, true)
	honest, err := productionLifecycleCleanupReceipt(request, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := extensionsruntime.ValidateLifecycleCleanupPurgeReceipt(request, honest); err != nil {
		t.Fatalf("retained proof must be accepted: %v", err)
	}

	liar := honest
	liar.Proof = []byte(`{"identityPurged":false,"packagePurged":true,"runtimeRecoveryPurged":true,"dataDisposition":"preserved"}`)
	if _, _, err := extensionsruntime.ValidateLifecycleCleanupPurgeReceipt(request, liar); err == nil {
		t.Fatal("identity retention without an explicit retained flag must be rejected")
	}
}
