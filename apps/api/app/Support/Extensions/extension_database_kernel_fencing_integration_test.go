package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func TestPostgresExtensionDatabaseKernelReaperFencesUnsupportedOwnershipWithoutStarvation(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	nonce := time.Now().UnixNano()
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)

	issue := func(extensionID, runtimeID string, actor, audit int64) (ExtensionDatabaseArtifact, ExtensionDatabaseRuntimeCredential) {
		t.Helper()
		artifact := insertExtensionDatabaseRuntimeLeaseFixture(
			t, ctx, pool, extensionID, "1.0.0", runtimeID,
			[]string{extensionmanifest.DatabaseGrantKernel},
		)
		identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
		credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: runtimeID,
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: actor, AuditEventID: audit,
			},
		})
		if err != nil {
			t.Fatalf("issue %s kernel lease: %v", runtimeID, err)
		}
		return artifact, credential
	}

	blockedExtensionID := fmt.Sprintf("p5.kernel.reaper-blocked.%d", nonce)
	blockedArtifact, blocked := issue(blockedExtensionID, "blocked-runtime", 950, 951)
	cleanExtensionID := fmt.Sprintf("p5.kernel.reaper-clean.%d", nonce)
	_, clean := issue(cleanExtensionID, "clean-runtime", 952, 953)

	blockedConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, blocked)
	if err != nil {
		t.Fatal(err)
	}
	defer blockedConnection.Close(context.Background())
	blockedTableName := fmt.Sprintf("p5_kernel_reaper_blocked_%d", nonce)
	blockedCollationName := fmt.Sprintf("p5_kernel_reaper_collation_%d", nonce)
	cleanTableName := fmt.Sprintf("p5_kernel_reaper_clean_%d", nonce)
	blockedTable := pgx.Identifier{extensionDatabaseCoreSchema, blockedTableName}.Sanitize()
	blockedCollation := pgx.Identifier{extensionDatabaseCoreSchema, blockedCollationName}.Sanitize()
	cleanTable := pgx.Identifier{extensionDatabaseCoreSchema, cleanTableName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool,
			`DROP COLLATION IF EXISTS `+blockedCollation+`; `+
				`DROP TABLE IF EXISTS `+blockedTable+`, `+cleanTable+` CASCADE`,
		)
	})
	execExtensionDatabaseKernelDDL(t, ctx, blockedConnection,
		`CREATE TABLE `+blockedTable+` (id BIGINT PRIMARY KEY)`,
		`CREATE COLLATION `+blockedCollation+` (provider = libc, locale = 'C')`,
	)
	cleanConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, clean)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanConnection.Close(context.Background())
	execExtensionDatabaseKernelDDL(t, ctx, cleanConnection,
		`CREATE TABLE `+cleanTable+` (id BIGINT PRIMARY KEY)`,
	)

	expireExtensionDatabaseKernelLeasesForTest(t, ctx, pool,
		struct {
			credential ExtensionDatabaseRuntimeCredential
			offset     string
		}{blocked, "2 minutes"},
		struct {
			credential ExtensionDatabaseRuntimeCredential
			offset     string
		}{clean, "1 minute"},
	)

	reaped, err := registry.ReapExpiredRuntimeLeases(ctx, DefaultExtensionDatabaseRuntimeLeaseReapLimit)
	if reaped < 1 || !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("poisoned global reaper = count:%d err:%v", reaped, err)
	}
	if _, err := blockedConnection.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("expired cleanup-pending kernel session survived fencing")
	}
	if reconnected, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, blocked); err == nil {
		reconnected.Close(context.Background())
		t.Fatal("expired cleanup-pending kernel credential reconnected")
	}
	blockedRef := extensionDatabaseRuntimeCredentialRef(blocked)
	blockedSnapshot, err := registry.InspectRuntimeLease(ctx, blockedRef)
	if err != nil || blockedSnapshot.Status != ExtensionDatabaseLeaseFailed ||
		blockedSnapshot.FailureCode != extensionDatabaseRuntimeLeaseCleanupPendingExpiredCode {
		t.Fatalf("blocked expiry fence = %#v, %v", blockedSnapshot, err)
	}
	if _, err := registry.HeartbeatRuntimeLease(
		ctx, blockedRef, blockedSnapshot.Revision,
	); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("blocked expired lease accepted heartbeat: %v", err)
	}
	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: blockedArtifact, RuntimeInstanceID: "blocked-runtime-replacement",
		Authority: ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost},
	}); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("blocked expired extension issued a replacement lease: %v", err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", extensionDatabaseCoreSchema, blockedTableName, blocked.RoleName,
	)
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "collation", extensionDatabaseCoreSchema, blockedCollationName, blocked.RoleName,
	)

	cleanSnapshot, err := registry.InspectRuntimeLease(ctx, extensionDatabaseRuntimeCredentialRef(clean))
	if err != nil || cleanSnapshot.Status != ExtensionDatabaseLeaseFailed ||
		cleanSnapshot.FailureCode != extensionDatabaseRuntimeLeaseExpiredCode {
		t.Fatalf("clean lease was starved by blocked cleanup = %#v, %v", cleanSnapshot, err)
	}
	var cleanRoleExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, clean.RoleName,
	).Scan(&cleanRoleExists); err != nil {
		t.Fatal(err)
	}
	if cleanRoleExists {
		t.Fatal("clean expired kernel role survived poisoned reaper")
	}

	if count, err := registry.reapExpiredRuntimeLeasesForExtension(
		ctx, blockedExtensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); count != 0 || !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("blocked reaper replay = count:%d err:%v", count, err)
	}
	execExtensionDatabaseTestPhysicalMutation(t, ctx, pool, `DROP COLLATION `+blockedCollation)

	type reapResult struct {
		count int
		err   error
	}
	results := make(chan reapResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			count, reapErr := registry.reapExpiredRuntimeLeasesForExtension(
				ctx, blockedExtensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
			)
			results <- reapResult{count: count, err: reapErr}
		}()
	}
	wait.Wait()
	close(results)
	cleaned := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		cleaned += result.count
	}
	if cleaned != 1 {
		t.Fatalf("concurrent cleanup retry count = %d, want 1", cleaned)
	}
	blockedSnapshot, err = registry.InspectRuntimeLease(ctx, blockedRef)
	if err != nil || blockedSnapshot.Status != ExtensionDatabaseLeaseFailed ||
		blockedSnapshot.FailureCode != extensionDatabaseRuntimeLeaseExpiredCode {
		t.Fatalf("completed blocked expiry cleanup = %#v, %v", blockedSnapshot, err)
	}
	var blockedRoleExists bool
	var expiryAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
		       (SELECT count(*)
		        FROM extension_database_runtime_leases AS leases
		        JOIN audit_events AS events ON events.id = leases.revoke_audit_event_id
		        WHERE leases.lease_id = $2 AND events.action = $3)
	`, blocked.RoleName, blocked.LeaseID, extensionDatabaseRuntimeLeaseExpiredAudit).Scan(
		&blockedRoleExists, &expiryAuditCount,
	); err != nil {
		t.Fatal(err)
	}
	if blockedRoleExists || expiryAuditCount != 1 {
		t.Fatalf("blocked expiry convergence = role:%t audits:%d", blockedRoleExists, expiryAuditCount)
	}
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	coreOwner, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", extensionDatabaseCoreSchema, blockedTableName, coreOwner,
	)
}

func expireExtensionDatabaseKernelLeasesForTest(
	t *testing.T,
	ctx context.Context,
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	},
	leases ...struct {
		credential ExtensionDatabaseRuntimeCredential
		offset     string
	},
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	for _, lease := range leases {
		var expiresAt time.Time
		if err := tx.QueryRow(ctx, `
			UPDATE extension_database_runtime_leases
			SET issued_at = statement_timestamp() - interval '5 minutes',
			    last_heartbeat_at = statement_timestamp() - interval '3 minutes',
			    lease_expires_at = statement_timestamp() - $2::interval
			WHERE lease_id = $1
			RETURNING lease_expires_at
		`, lease.credential.LeaseID, lease.offset).Scan(&expiresAt); err != nil {
			t.Fatal(err)
		}
		role := pgx.Identifier{lease.credential.RoleName}.Sanitize()
		if _, err := tx.Exec(ctx,
			`ALTER ROLE `+role+` VALID UNTIL '`+expiresAt.UTC().Format(time.RFC3339Nano)+`'`,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
