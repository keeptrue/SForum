package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestPostgresExtensionDatabaseRuntimeLeasesOverlapAndRevokeExactly(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for runtime lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("p5.runtime.lease.%d", time.Now().UnixNano())
	coreProbeName := createExtensionDatabaseRawCoreProbe(t, ctx, pool)
	coreProbe := pgx.Identifier{extensionDatabaseCoreSchema, coreProbeName}.Sanitize()
	source := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "source", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantRawCore,
		},
	)
	target := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "2.0.0", "target", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantRawCore,
		},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	sourceCredential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: source, RuntimeInstanceID: "source-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 101, AuditEventID: 201,
		},
	})
	if err != nil {
		t.Fatalf("issue source runtime lease: %v", err)
	}
	targetCredential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: target, RuntimeInstanceID: "target-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 102, AuditEventID: 202,
		},
	})
	if err != nil {
		t.Fatalf("issue target runtime lease: %v", err)
	}
	if sourceCredential.RoleName == targetCredential.RoleName ||
		sourceCredential.Password == targetCredential.Password || sourceCredential.GrantID == targetCredential.GrantID {
		t.Fatalf("source and target leases are not exact: source=%#v target=%#v", sourceCredential, targetCredential)
	}

	sourceConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, sourceCredential)
	if err != nil {
		t.Fatalf("connect source runtime lease: %v", err)
	}
	defer sourceConnection.Close(context.Background())
	targetConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, targetCredential)
	if err != nil {
		t.Fatalf("connect target runtime lease: %v", err)
	}
	defer targetConnection.Close(context.Background())
	if _, err := sourceConnection.Exec(ctx, `CREATE TABLE runtime_lease_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatalf("source creates own-schema data: %v", err)
	}
	if _, err := targetConnection.Exec(ctx, `INSERT INTO runtime_lease_probe (id, note) VALUES (1, 'overlap')`); err != nil {
		t.Fatalf("target writes during source overlap: %v", err)
	}
	var coreProbeID int64
	if err := sourceConnection.QueryRow(ctx, `INSERT INTO `+coreProbe+` (note) VALUES ('source') RETURNING id`).Scan(&coreProbeID); err != nil {
		t.Fatalf("source raw Core write: %v", err)
	}
	if _, err := targetConnection.Exec(ctx, `UPDATE `+coreProbe+` SET note = 'target' WHERE id = $1`, coreProbeID); err != nil {
		t.Fatalf("target raw Core write during overlap: %v", err)
	}
	var note string
	if err := sourceConnection.QueryRow(ctx, `SELECT note FROM runtime_lease_probe WHERE id = 1`).Scan(&note); err != nil || note != "overlap" {
		t.Fatalf("source reads target write: note=%q err=%v", note, err)
	}

	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: source, RuntimeInstanceID: "source-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerHost, AuditEventID: 203,
		},
	}); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("duplicate live runtime error = %v", err)
	}

	sourceRef := ExtensionDatabaseRuntimeLeaseRef{
		Artifact: source, RuntimeInstanceID: "source-runtime", LeaseID: sourceCredential.LeaseID,
	}
	heartbeat, err := registry.HeartbeatRuntimeLease(ctx, sourceRef, 1)
	if err != nil {
		t.Fatalf("heartbeat source runtime lease: %v", err)
	}
	if heartbeat.Revision != 2 || heartbeat.Status != ExtensionDatabaseLeaseActive ||
		!heartbeat.ExpiresAt.After(sourceCredential.ExpiresAt) {
		t.Fatalf("heartbeat did not extend exact source lease: %#v", heartbeat)
	}
	if _, err := registry.HeartbeatRuntimeLease(ctx, sourceRef, 1); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("stale heartbeat revision was accepted: %v", err)
	}
	draining, err := registry.BeginRuntimeLeaseDrain(ctx, sourceRef, heartbeat.Revision)
	if err != nil {
		t.Fatalf("begin exact source lease drain: %v", err)
	}
	if draining.Status != ExtensionDatabaseLeaseDraining || draining.Revision != 3 || draining.DrainingAt == nil {
		t.Fatalf("source lease did not enter draining state: %#v", draining)
	}
	if _, err := sourceConnection.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("drain revoked source before runtime calls completed: %v", err)
	}
	if _, err := registry.HeartbeatRuntimeLease(ctx, sourceRef, draining.Revision); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("draining lease accepted a heartbeat: %v", err)
	}
	revokedSource, err := registry.RevokeRuntimeLease(ctx, sourceRef, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 103, AuditEventID: 204,
	})
	if err != nil || revokedSource.Status != ExtensionDatabaseLeaseRevoked {
		t.Fatalf("revoke source runtime lease: snapshot=%#v err=%v", revokedSource, err)
	}
	if _, err := sourceConnection.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("source session survived exact lease revoke")
	}
	if err := targetConnection.QueryRow(ctx, `SELECT note FROM runtime_lease_probe WHERE id = 1`).Scan(&note); err != nil || note != "overlap" {
		t.Fatalf("target or source-owned data was lost: note=%q err=%v", note, err)
	}
	if err := targetConnection.QueryRow(ctx, `SELECT note FROM `+coreProbe+` WHERE id = $1`, coreProbeID).Scan(&note); err != nil || note != "target" {
		t.Fatalf("target raw Core authority was lost with source revoke: note=%q err=%v", note, err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, sourceRef, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost, AuditEventID: 205,
	}); err != nil {
		t.Fatalf("exact revoke replay did not converge: %v", err)
	}

	hostCredential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: target, RuntimeInstanceID: "target-runtime-restarted",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerHost,
		},
	})
	if err != nil {
		t.Fatalf("Host could not issue from an existing exact grant: %v", err)
	}
	hostConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, hostCredential)
	if err != nil {
		t.Fatal(err)
	}
	if err := hostConnection.QueryRow(ctx, `SELECT note FROM runtime_lease_probe WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("restarted target cannot use retained schema: %v", err)
	}
	_ = hostConnection.Close(ctx)
	hostLease, err := registry.InspectRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseRef{
		Artifact: target, RuntimeInstanceID: hostCredential.RuntimeInstanceID, LeaseID: hostCredential.LeaseID,
	})
	if err != nil || hostLease.IssueAuditEventID <= 0 {
		t.Fatalf("Host-owned issue audit = %#v, %v", hostLease, err)
	}

	for _, item := range []struct {
		credential ExtensionDatabaseRuntimeCredential
		authority  ExtensionDatabaseLeaseAuthority
	}{
		{hostCredential, ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost}},
		{targetCredential, ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 104, AuditEventID: 208}},
	} {
		if _, err := registry.RevokeRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseRef{
			Artifact: target, RuntimeInstanceID: item.credential.RuntimeInstanceID, LeaseID: item.credential.LeaseID,
		}, item.authority); err != nil {
			t.Fatalf("revoke target runtime %s: %v", item.credential.RuntimeInstanceID, err)
		}
	}

	var activeGrants, liveLeases, powerRows, plaintextRows, sourceRoleExists, targetRoleExists, hostAuditRows int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_grants WHERE extension_id = $1 AND status = 'active'),
		  (SELECT count(*) FROM extension_database_runtime_leases WHERE extension_id = $1 AND status IN ('active', 'draining')),
		  (SELECT count(*) FROM extension_database_grant_powers AS powers
		   JOIN extension_database_grants AS grants ON grants.id = powers.grant_id
		   WHERE grants.extension_id = $1),
		  (SELECT count(*) FROM extension_database_runtime_leases
		   WHERE extension_id = $1 AND credential_fingerprint IN ($2, $3)),
		  (SELECT count(*) FROM pg_roles WHERE rolname = $4),
		  (SELECT count(*) FROM pg_roles WHERE rolname = $5),
		  (SELECT count(*) FROM audit_events
		   WHERE id IN (
		     SELECT issue_audit_event_id FROM extension_database_runtime_leases WHERE lease_id = $6
		     UNION ALL
		     SELECT revoke_audit_event_id FROM extension_database_runtime_leases WHERE lease_id = $6
		   ) AND action IN ($7, $8))
	`, extensionID, sourceCredential.Password, targetCredential.Password,
		sourceCredential.RoleName, targetCredential.RoleName, hostCredential.LeaseID,
		extensionDatabaseRuntimeLeaseIssuedAudit, extensionDatabaseRuntimeLeaseRevokedAudit).Scan(
		&activeGrants, &liveLeases, &powerRows, &plaintextRows, &sourceRoleExists, &targetRoleExists, &hostAuditRows,
	); err != nil {
		t.Fatal(err)
	}
	if activeGrants != 2 || liveLeases != 0 || powerRows != 4 || plaintextRows != 0 ||
		sourceRoleExists != 0 || targetRoleExists != 0 || hostAuditRows != 2 {
		t.Fatalf(
			"grant/lease evidence mismatch: grants=%d live=%d powers=%d plaintext=%d sourceRole=%d targetRole=%d hostAudit=%d",
			activeGrants, liveLeases, powerRows, plaintextRows, sourceRoleExists, targetRoleExists, hostAuditRows,
		)
	}
}

func TestPostgresExtensionDatabaseRuntimeLeasePreservesAdditivePowers(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for runtime lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)

	tests := []struct {
		name   string
		powers []string
		probe  func(*pgx.Conn) error
	}{
		{
			name: "core views without own schema",
			powers: []string{
				extensionmanifest.DatabaseGrantCoreViews,
			},
			probe: func(connection *pgx.Conn) error {
				var count int
				return connection.QueryRow(ctx, `SELECT count(*) FROM safe_users`).Scan(&count)
			},
		},
		{
			name: "own schema and raw core",
			powers: []string{
				extensionmanifest.DatabaseGrantOwnSchema,
				extensionmanifest.DatabaseGrantRawCore,
			},
			probe: func(connection *pgx.Conn) error {
				if _, err := connection.Exec(ctx, `CREATE TABLE additive_power_probe (id BIGINT PRIMARY KEY)`); err != nil {
					return err
				}
				var count int
				return connection.QueryRow(ctx, `SELECT count(*) FROM public.extensions`).Scan(&count)
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extensionID := fmt.Sprintf("p5.runtime.power.%d.%d", time.Now().UnixNano(), index)
			artifact := insertExtensionDatabaseRuntimeLeaseFixture(
				t, ctx, pool, extensionID, "1.0.0", test.name, test.powers,
			)
			identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
			credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
				Artifact: artifact, RuntimeInstanceID: "runtime",
				Authority: ExtensionDatabaseLeaseAuthority{
					Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 301, AuditEventID: 401,
				},
			})
			if err != nil {
				t.Fatalf("issue additive runtime lease: %v", err)
			}
			connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
			if err != nil {
				t.Fatalf("connect additive runtime lease: %v", err)
			}
			defer connection.Close(context.Background())
			if err := test.probe(connection); err != nil {
				t.Fatalf("additive power probe failed: %v", err)
			}
		})
	}
}

func TestPostgresExtensionDatabaseRuntimeLeaseReaperConvergesAndAudits(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for runtime lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("p5.runtime.reaper.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "expired", []string{extensionmanifest.DatabaseGrantOwnSchema},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "orphan-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 501, AuditEventID: 601,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	if _, err := connection.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("runtime lease was not live before expiry: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_database_runtime_leases
		SET issued_at = statement_timestamp() - interval '4 minutes',
		    last_heartbeat_at = statement_timestamp() - interval '2 minutes',
		    lease_expires_at = statement_timestamp() - interval '1 minute'
		WHERE lease_id = $1
	`, credential.LeaseID); err != nil {
		t.Fatal(err)
	}

	type reapResult struct {
		count int
		err   error
	}
	results := make(chan reapResult, 2)
	for range 2 {
		go func() {
			count, err := registry.reapExpiredRuntimeLeasesForExtension(
				ctx, extensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
			)
			results <- reapResult{count: count, err: err}
		}()
	}
	reaped := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		reaped += result.count
	}
	if reaped != 1 {
		t.Fatalf("concurrent reaper count = %d, want 1", reaped)
	}
	if _, err := connection.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("expired runtime session survived the reaper")
	}
	ref := ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID,
	}
	snapshot, err := registry.InspectRuntimeLease(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != ExtensionDatabaseLeaseFailed || snapshot.FailureCode != extensionDatabaseRuntimeLeaseExpiredCode ||
		snapshot.RevokedAt == nil || snapshot.Revision != 3 {
		t.Fatalf("expired lease evidence = %#v", snapshot)
	}
	var roleCount, auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_roles WHERE rolname = $1),
		  (SELECT count(*)
		   FROM extension_database_runtime_leases AS leases
		   JOIN audit_events AS events ON events.id = leases.revoke_audit_event_id
		   WHERE leases.lease_id = $2
		     AND events.action = $3
		     AND events.metadata->>'runtimeInstanceId' = $4)
	`, credential.RoleName, credential.LeaseID, extensionDatabaseRuntimeLeaseExpiredAudit,
		credential.RuntimeInstanceID).Scan(&roleCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if roleCount != 0 || auditCount != 1 {
		t.Fatalf("reaper evidence mismatch: role=%d audit=%d", roleCount, auditCount)
	}
	if count, err := registry.reapExpiredRuntimeLeasesForExtension(
		ctx, extensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); err != nil || count != 0 {
		t.Fatalf("exact extension reaper replay = %d, %v", count, err)
	}
	if _, err := registry.ReapExpiredRuntimeLeases(ctx, DefaultExtensionDatabaseRuntimeLeaseReapLimit); err != nil {
		t.Fatalf("global reaper failed: %v", err)
	}
}

// 真实运行时可能在 Host 重启前已经由旧实例或人工恢复删除 login role。此时
// cleanup-pending 行必须收敛为终态，而不是让下一次 API bootstrap 永久失败。
func TestPostgresExtensionDatabaseRuntimeLeaseReaperConvergesWhenRoleWasAlreadyRemoved(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for runtime lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("p5.runtime.reaper.missing-role.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "already-removed", []string{extensionmanifest.DatabaseGrantOwnSchema},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "already-removed-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 701, AuditEventID: 801,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_database_runtime_leases
		SET issued_at = statement_timestamp() - interval '4 minutes',
		    last_heartbeat_at = statement_timestamp() - interval '2 minutes',
		    lease_expires_at = statement_timestamp() - interval '1 minute'
		WHERE lease_id = $1
	`, credential.LeaseID); err != nil {
		t.Fatal(err)
	}

	// 先走生产 reaper 的第一阶段，留下已逻辑失效但尚待物理回收的租约。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		t.Fatal(err)
	}
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reapExpiredExtensionDatabaseRuntimeLeasesLocked(
		ctx, tx, extensionID, identifiers, databaseName, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// 模拟旧进程/恢复工具在物理回收之间删除同一 lease role。
	quotedRole := pgx.Identifier{credential.RoleName}.Sanitize()
	quotedOwner := pgx.Identifier{identifiers.OwnerRole}.Sanitize()
	for _, statement := range []string{
		`REASSIGN OWNED BY ` + quotedRole + ` TO ` + quotedOwner,
		`DROP OWNED BY ` + quotedRole,
		`DROP ROLE ` + quotedRole,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("remove runtime lease role before reaper retry: %v", err)
		}
	}

	reaped, err := registry.reapExpiredRuntimeLeasesForExtension(
		ctx, extensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	)
	if err != nil || reaped != 1 {
		t.Fatalf("reaper retry after role removal = count:%d err:%v", reaped, err)
	}
	ref := ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID,
	}
	snapshot, err := registry.InspectRuntimeLease(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != ExtensionDatabaseLeaseFailed || snapshot.FailureCode != extensionDatabaseRuntimeLeaseExpiredCode {
		t.Fatalf("already-removed role lease did not converge: %#v", snapshot)
	}
}

func insertExtensionDatabaseRuntimeLeaseFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID string,
	version string,
	digestSource string,
	powers []string,
) ExtensionDatabaseArtifact {
	t.Helper()
	digest := sha256.Sum256([]byte(extensionID + ":" + digestSource))
	packageDigest := hex.EncodeToString(digest[:])
	manifest := extensions.Manifest{
		ID: extensionID, Name: "P5 runtime lease fixture", Version: version, Type: extensions.TypePlugin,
		Database: &extensions.ManifestDatabase{
			ContractVersion: extensionID + ".database@1",
			Grants:          append([]string(nil), powers...),
			Schema:          "logical_schema", Role: "logical_role",
		},
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) ||
		containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		// High-risk powers are only issuable when the declaration covers the
		// migrated Core release, which follows this checkout's git tag. Derive
		// it from the Host authority instead of freezing a release line here.
		manifest.Database.CoreCompatibility, _ = hostCoreCompatibility(t)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'P5 runtime lease fixture', 'installed')
		ON CONFLICT (id) DO NOTHING
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, $2, $3::jsonb, $4, $5)
		RETURNING id
	`, extensionID, version, manifestJSON, t.TempDir(), packageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return ExtensionDatabaseArtifact{
		ExtensionID: extensionID, Version: version, VersionID: versionID, PackageDigest: packageDigest,
	}
}

func connectExtensionDatabaseRuntimeCredential(
	ctx context.Context,
	pool *pgxpool.Pool,
	credential ExtensionDatabaseRuntimeCredential,
) (*pgx.Conn, error) {
	config := pool.Config().ConnConfig.Copy()
	config.User = credential.RoleName
	config.Password = credential.Password
	config.Database = credential.DatabaseName
	delete(config.RuntimeParams, "search_path")
	delete(config.RuntimeParams, "role")
	return pgx.ConnectConfig(ctx, config)
}

func cleanupExtensionDatabaseRuntimeLeaseFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	extensionID string,
	identifiers ExtensionDatabaseIdentifiers,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin runtime lease fixture cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Errorf("lock runtime lease fixture cleanup: %v", err)
		return
	}
	rows, _ := tx.Query(ctx, `
		SELECT leases.role_name
		FROM extension_database_runtime_leases AS leases
		JOIN pg_roles AS roles ON roles.rolname = leases.role_name
		WHERE leases.extension_id = $1
	`, extensionID)
	var leaseRoles []string
	if rows != nil {
		for rows.Next() {
			var role string
			if rows.Scan(&role) == nil {
				leaseRoles = append(leaseRoles, role)
			}
		}
		rows.Close()
	}
	for _, role := range leaseRoles {
		if _, err := tx.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, role); err != nil {
			t.Errorf("terminate runtime lease fixture role %s: %v", role, err)
			return
		}
		quotedRole := pgx.Identifier{role}.Sanitize()
		quotedOwner := pgx.Identifier{identifiers.OwnerRole}.Sanitize()
		for _, query := range []string{
			`REASSIGN OWNED BY ` + quotedRole + ` TO ` + quotedOwner,
			`DROP OWNED BY ` + quotedRole,
			`DROP ROLE ` + quotedRole,
		} {
			if _, err := tx.Exec(ctx, query); err != nil {
				t.Errorf("cleanup runtime lease fixture role %s: %v", role, err)
				return
			}
		}
	}
	for _, query := range []struct {
		statement string
		arguments []any
	}{
		{`DELETE FROM extension_database_runtime_leases WHERE extension_id = $1`, []any{extensionID}},
		{`DELETE FROM extension_database_grant_powers
		  WHERE grant_id IN (SELECT id FROM extension_database_grants WHERE extension_id = $1)`, []any{extensionID}},
		{`DELETE FROM extension_database_credentials WHERE extension_id = $1`, []any{extensionID}},
		{`DELETE FROM extension_database_grants WHERE extension_id = $1`, []any{extensionID}},
		{`DELETE FROM extension_database_resources WHERE extension_id = $1`, []any{extensionID}},
		{`DELETE FROM extensions WHERE id = $1`, []any{extensionID}},
		{`DROP SCHEMA IF EXISTS ` + pgx.Identifier{identifiers.Schema}.Sanitize() + ` CASCADE`, nil},
	} {
		if _, err := tx.Exec(ctx, query.statement, query.arguments...); err != nil {
			t.Errorf("cleanup runtime lease fixture data: %v", err)
			return
		}
	}
	for _, role := range []string{identifiers.RuntimeRole, identifiers.OwnerRole} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Errorf("inspect runtime lease fixture role %s: %v", role, err)
			return
		}
		if !exists {
			continue
		}
		quoted := pgx.Identifier{role}.Sanitize()
		for _, query := range []string{`DROP OWNED BY ` + quoted, `DROP ROLE ` + quoted} {
			if _, err := tx.Exec(ctx, query); err != nil {
				t.Errorf("cleanup runtime lease fixture role %s: %v", role, err)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit runtime lease fixture cleanup: %v", err)
	}
}
