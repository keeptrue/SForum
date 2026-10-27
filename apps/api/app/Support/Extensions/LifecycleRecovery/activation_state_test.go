package lifecyclerecovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const activationRecoveryDDL = `
CREATE TABLE extensions (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	active_version_id BIGINT,
	staged_version_id BIGINT,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE extension_lifecycle_operations (
	id BIGINT PRIMARY KEY,
	extension_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	state TEXT NOT NULL,
	terminal_result TEXT,
	completed_at TIMESTAMPTZ,
	attempt_count INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE extension_lifecycle_publications (
	id BIGINT PRIMARY KEY,
	operation_id BIGINT NOT NULL,
	operation TEXT NOT NULL,
	step_id TEXT NOT NULL,
	publication_mode TEXT NOT NULL,
	commit_marker BOOLEAN NOT NULL DEFAULT FALSE,
	last_attempt INTEGER NOT NULL
);
CREATE TABLE extension_lifecycle_state_publications (
	id BIGINT PRIMARY KEY,
	operation_id BIGINT NOT NULL,
	step_id TEXT NOT NULL,
	publication_mode TEXT NOT NULL,
	source_status TEXT NOT NULL,
	source_active_version_id BIGINT,
	source_active_version TEXT,
	source_active_package_digest TEXT,
	target_status TEXT NOT NULL,
	target_active_version_id BIGINT,
	target_active_version TEXT,
	target_active_package_digest TEXT,
	transaction_state TEXT NOT NULL DEFAULT 'source',
	last_attempt INTEGER NOT NULL,
	revision BIGINT NOT NULL DEFAULT 1,
	restored_at TIMESTAMPTZ,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE extension_lifecycle_registry_publications (
	id BIGINT PRIMARY KEY,
	operation_id BIGINT NOT NULL,
	step_id TEXT NOT NULL,
	publication_mode TEXT NOT NULL,
	transaction_state TEXT NOT NULL DEFAULT 'source',
	last_attempt INTEGER NOT NULL
);
CREATE TABLE plugin_runtime_publications (
	revision BIGINT PRIMARY KEY,
	member_count INTEGER NOT NULL DEFAULT 1,
	members_digest TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT 'upgrade',
	actor_user_id BIGINT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE plugin_runtime_publication_members (
	publication_revision BIGINT NOT NULL,
	extension_id TEXT NOT NULL,
	extension_version_id BIGINT NOT NULL,
	extension_version TEXT NOT NULL,
	package_digest TEXT NOT NULL,
	PRIMARY KEY (publication_revision, extension_id)
);
CREATE TABLE audit_events (
	id BIGSERIAL PRIMARY KEY,
	action TEXT NOT NULL,
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);
`

type activationRecoveryFixture struct {
	pool        *pgxpool.Pool
	extensionID string
	operationID int64
	stateID     int64
	sourceID    int64
	sourceVer   string
	sourceDig   string
	targetID    int64
	targetVer   string
	targetDig   string
	stagedID    int64
}

// seedActivationRecoveryFixture writes one exact interrupted activation:
// state published the target, the publication never committed, registries and
// the latest runtime publication stayed on the source artifact.
func seedActivationRecoveryFixture(t *testing.T, pool *pgxpool.Pool) activationRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	fixture := activationRecoveryFixture{
		pool:        pool,
		extensionID: "demo.recovery",
		operationID: 41,
		stateID:     61,
		sourceID:    1,
		sourceVer:   "1.0.0",
		sourceDig:   strings.Repeat("a", 64),
		targetID:    2,
		targetVer:   "2.0.0",
		targetDig:   strings.Repeat("b", 64),
		stagedID:    3,
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed activation recovery fixture: %v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO extensions (id, status, active_version_id, staged_version_id) VALUES ($1, 'enabled', $2, $3)`,
		fixture.extensionID, fixture.targetID, fixture.stagedID)
	exec(`INSERT INTO extension_lifecycle_operations
		(id, extension_id, operation, state, terminal_result, completed_at, attempt_count)
		VALUES ($1, $2, 'upgrade', 'failed', 'cancelled', now(), 1)`, fixture.operationID, fixture.extensionID)
	exec(`INSERT INTO extension_lifecycle_publications
		(id, operation_id, operation, step_id, publication_mode, commit_marker, last_attempt)
		VALUES (51, $1, 'upgrade', 'lifecycle.upgrade.08.host.enabled', 'activate', FALSE, 1)`, fixture.operationID)
	exec(`INSERT INTO extension_lifecycle_state_publications
		(id, operation_id, step_id, publication_mode,
		 source_status, source_active_version_id, source_active_version, source_active_package_digest,
		 target_status, target_active_version_id, target_active_version, target_active_package_digest,
		 transaction_state, last_attempt, revision)
		VALUES ($1, $2, 'lifecycle.upgrade.08.host.enabled', 'activate',
		 'enabled', $3, $4, $5,
		 'enabled', $6, $7, $8,
		 'target', 1, 1)`,
		fixture.stateID, fixture.operationID,
		fixture.sourceID, fixture.sourceVer, fixture.sourceDig,
		fixture.targetID, fixture.targetVer, fixture.targetDig)
	exec(`INSERT INTO extension_lifecycle_registry_publications
		(id, operation_id, step_id, publication_mode, transaction_state, last_attempt)
		VALUES (71, $1, 'lifecycle.upgrade.08.host.enabled', 'activate', 'source', 1)`, fixture.operationID)
	exec(`INSERT INTO plugin_runtime_publications (revision, member_count, members_digest, reason)
		VALUES (1, 1, $1, 'upgrade')`, strings.Repeat("0", 64))
	exec(`INSERT INTO plugin_runtime_publication_members
		(publication_revision, extension_id, extension_version_id, extension_version, package_digest)
		VALUES (1, $1, $2, $3, $4)`, fixture.extensionID, fixture.sourceID, fixture.sourceVer, fixture.sourceDig)
	return fixture
}

func (f activationRecoveryFixture) extensionRow(t *testing.T) (int64, int64, string) {
	t.Helper()
	var active, staged int64
	var status string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(active_version_id, 0), COALESCE(staged_version_id, 0), status
		FROM extensions WHERE id = $1`, f.extensionID).Scan(&active, &staged, &status); err != nil {
		t.Fatalf("read extension row: %v", err)
	}
	return active, staged, status
}

func (f activationRecoveryFixture) stateRow(t *testing.T) (string, bool, int64) {
	t.Helper()
	var phase string
	var restored bool
	var revision int64
	if err := f.pool.QueryRow(context.Background(), `
		SELECT transaction_state, restored_at IS NOT NULL, revision
		FROM extension_lifecycle_state_publications WHERE id = $1`, f.stateID).Scan(&phase, &restored, &revision); err != nil {
		t.Fatalf("read state publication: %v", err)
	}
	return phase, restored, revision
}

func (f activationRecoveryFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_events WHERE action = $1`,
		audit.ActionExtensionLifecycleActivationRecovered).Scan(&count); err != nil {
		t.Fatalf("count recovery audits: %v", err)
	}
	return count
}

func TestRecoverInterruptedActivationsRestoresExactSourceAndIsIdempotent(t *testing.T) {
	pool := newActivationRecoveryPool(t)
	fixture := seedActivationRecoveryFixture(t, pool)
	recovery := NewPostgresInterruptedActivationRecovery(pool)

	recovered, err := recovery.RecoverInterruptedActivations(context.Background(), 0)
	if err != nil {
		t.Fatalf("recover interrupted activation: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("recovered = %#v, want exactly one repair", recovered)
	}
	item := recovered[0]
	if item.OperationID != fixture.operationID || item.ExtensionID != fixture.extensionID ||
		item.StatePublicationID != fixture.stateID || item.FromVersionID != fixture.targetID ||
		item.ToVersionID != fixture.sourceID || item.AuditEventID <= 0 {
		t.Fatalf("recovered item = %#v", item)
	}

	active, staged, status := fixture.extensionRow(t)
	if active != fixture.sourceID || staged != fixture.stagedID || status != "enabled" {
		t.Fatalf("extension row = active %d staged %d status %q", active, staged, status)
	}
	phase, restored, revision := fixture.stateRow(t)
	if phase != "source" || !restored || revision != 2 {
		t.Fatalf("state publication = %s restored=%v revision=%d", phase, restored, revision)
	}
	if count := fixture.auditCount(t); count != 1 {
		t.Fatalf("recovery audits = %d, want 1", count)
	}

	// 幂等：条件已漂移（state 已是 source、active 已回 source），第二轮必须 no-op。
	again, err := recovery.RecoverInterruptedActivations(context.Background(), 0)
	if err != nil || len(again) != 0 {
		t.Fatalf("second recovery = %#v, %v", again, err)
	}
	if count := fixture.auditCount(t); count != 1 {
		t.Fatalf("recovery audits after replay = %d, want 1", count)
	}
	if active, _, _ := fixture.extensionRow(t); active != fixture.sourceID {
		t.Fatalf("replay changed active version to %d", active)
	}
}

func TestRecoverInterruptedActivationsRefusesDriftedEvidence(t *testing.T) {
	tests := []struct {
		name  string
		drift func(t *testing.T, pool *pgxpool.Pool, fixture activationRecoveryFixture)
	}{
		{
			name: "publication marker committed",
			drift: func(t *testing.T, pool *pgxpool.Pool, _ activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE extension_lifecycle_publications SET commit_marker = TRUE`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "registries advanced to target",
			drift: func(t *testing.T, pool *pgxpool.Pool, _ activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE extension_lifecycle_registry_publications SET transaction_state = 'target'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "active pointer moved by another operation",
			drift: func(t *testing.T, pool *pgxpool.Pool, fixture activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE extensions SET active_version_id = 99 WHERE id = $1`, fixture.extensionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "applied runtime member is another artifact",
			drift: func(t *testing.T, pool *pgxpool.Pool, _ activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE plugin_runtime_publication_members SET package_digest = $1`, strings.Repeat("c", 64)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extension status drifted",
			drift: func(t *testing.T, pool *pgxpool.Pool, fixture activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE extensions SET status = 'disabled' WHERE id = $1`, fixture.extensionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "state publication already restored",
			drift: func(t *testing.T, pool *pgxpool.Pool, _ activationRecoveryFixture) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE extension_lifecycle_state_publications SET transaction_state = 'source', restored_at = now()`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := newActivationRecoveryPool(t)
			fixture := seedActivationRecoveryFixture(t, pool)
			test.drift(t, pool, fixture)
			beforeActive, beforeStaged, beforeStatus := fixture.extensionRow(t)
			beforePhase, beforeRestored, beforeRevision := fixture.stateRow(t)

			recovered, err := NewPostgresInterruptedActivationRecovery(pool).
				RecoverInterruptedActivations(context.Background(), 0)
			if err != nil {
				t.Fatalf("recovery on drifted evidence: %v", err)
			}
			if len(recovered) != 0 {
				t.Fatalf("drifted evidence repaired: %#v", recovered)
			}
			active, staged, status := fixture.extensionRow(t)
			if active != beforeActive || staged != beforeStaged || status != beforeStatus {
				t.Fatalf("extension row changed: active %d->%d staged %d->%d status %q->%q",
					beforeActive, active, beforeStaged, staged, beforeStatus, status)
			}
			if count := fixture.auditCount(t); count != 0 {
				t.Fatalf("drifted evidence wrote %d audit rows", count)
			}
			phase, restored, revision := fixture.stateRow(t)
			if phase != beforePhase || restored != beforeRestored || revision != beforeRevision {
				t.Fatalf("state publication changed: %s/%v/%d -> %s/%v/%d",
					beforePhase, beforeRestored, beforeRevision, phase, restored, revision)
			}
		})
	}
}

// newActivationRecoveryPool creates an isolated schema in the test database.
// It only accepts SFORUM_TEST_DATABASE_URL, never DATABASE_URL, so a local dev
// database can never be repaired by a test run.
func newActivationRecoveryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("lar_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop activation recovery schema %s: %v", schema, err)
		}
		admin.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, activationRecoveryDDL); err != nil {
		t.Fatalf("create activation recovery schema: %v", err)
	}
	var currentSchema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatalf("activation recovery schema=%q err=%v", currentSchema, err)
	}
	return pool
}
