package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestLifecycleBoundaryCallFenceBindsCanonicalState(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	fence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		t.Fatal(err)
	}
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if fence.State != path[request.Position].State || fence.StepID != request.StepID || fence.Attempt != request.Attempt {
		t.Fatalf("fence = %#v", fence)
	}
}

func TestLifecycleBoundaryPostgresFenceRejectsStaleStepAndAttempt(t *testing.T) {
	ctx, pool, request := newLifecycleBoundaryFenceIntegration(t)
	fence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		t.Fatal(err)
	}
	validate := func() error {
		t.Helper()
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback(ctx)
		return validateLifecycleBoundaryPostgresFence(ctx, tx, fence, true)
	}
	if err := validate(); err != nil {
		t.Fatalf("valid fence: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET current_step_id = 'lifecycle.enable.99.host.failed', revision = revision + 1
		WHERE id = $1
	`, request.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := validate(); !errors.Is(err, ErrLifecycleBoundaryFenceConflict) {
		t.Fatalf("advanced step error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET current_step_id = $2, revision = revision + 1
		WHERE id = $1
	`, request.OperationID, request.StepID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'succeeded', completed_at = statement_timestamp(),
		    updated_at = statement_timestamp()
		WHERE operation_id = $1 AND step_id = $2 AND attempt = $3
	`, request.OperationID, request.StepID, request.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, actor_user_id, audit_event_id, started_at
		) VALUES ($1, $2, 'host.gate', 'fence.integration@1', $3,
		          'running', $4, $5, statement_timestamp())
	`, request.OperationID, request.StepID, request.Attempt+1,
		request.ActorUserID, request.AuditEventID); err != nil {
		t.Fatal(err)
	}
	if err := validate(); !errors.Is(err, ErrLifecycleBoundaryFenceConflict) {
		t.Fatalf("stale attempt error = %v", err)
	}

	fence.Attempt++
	if err := validate(); err != nil {
		t.Fatalf("latest attempt fence: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET state = 'recovery', revision = revision + 1
		WHERE id = $1
	`, request.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := validate(); !errors.Is(err, ErrLifecycleBoundaryFenceConflict) {
		t.Fatalf("state drift error = %v", err)
	}
}

func newLifecycleBoundaryFenceIntegration(
	t *testing.T,
) (context.Context, *pgxpool.Pool, LifecycleBoundaryRequest) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	unique := fmt.Sprintf("lifecycle-fence-%d", time.Now().UnixNano())
	request.TargetExtension.ID = unique
	request.TargetExtension.Manifest.ID = unique
	request.TargetBinding.ExtensionID = unique
	actorUserID, err := insertLifecycleActorFixture(ctx, pool, unique, "Lifecycle Fence")
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	request.ActorUserID = actorUserID
	request.AuditEventID = time.Now().UnixNano()
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, audit_event_id,
			current_step_id
		) VALUES ($1, $2, $3, '{}'::jsonb,
		          $4, $5, 'fence.integration@1', $6, $7,
		          'builtin', '{}'::jsonb, $8, $9, $10)
		RETURNING id
	`, request.TargetExtension.ID, request.TargetExtension.Version,
		request.TargetExtension.PackageDigest, request.Operation, path[request.Position].State,
		unique, strings.Repeat("c", 64), request.ActorUserID, request.AuditEventID,
		request.StepID).Scan(&request.OperationID); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, actor_user_id, audit_event_id, started_at
		) VALUES ($1, $2, 'host.gate', 'fence.integration@1', $3,
		          'running', $4, $5, statement_timestamp())
	`, request.OperationID, request.StepID, request.Attempt,
		request.ActorUserID, request.AuditEventID); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, request.OperationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, request.ActorUserID)
		pool.Close()
	})
	return ctx, pool, request
}
