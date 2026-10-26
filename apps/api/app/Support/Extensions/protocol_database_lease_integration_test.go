package extensionsruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProtocolStarterUsesRealExactDatabaseLease(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("DATABASE_URL is required for protocol database lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("p5.protocol.database.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "protocol", []string{extensionmanifest.DatabaseGrantOwnSchema},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	seed, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "grant-seed",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 701, AuditEventID: 801,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: seed.RuntimeInstanceID, LeaseID: seed.LeaseID,
	}, ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost}); err != nil {
		t.Fatal(err)
	}

	packageRoot := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_EXPECT_DATABASE=connect SFORUM_PLUGIN_HELPER=protocol-v2-no-services exec " +
		shellQuote(os.Args[0]) + " -test.run=TestProtocolV2DatabaseLeaseHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	extension := runtimeExtension(extensionID)
	extension.Source = extensions.SourceUploaded
	extension.Version = artifact.Version
	extension.PackageDigest = artifact.PackageDigest
	extension.PackagePath = packageRoot
	extension.ActiveVersionID = artifact.VersionID
	extension.Manifest.ID = extensionID
	extension.Manifest.Version = artifact.Version
	extension.Manifest.Database = &extensions.ManifestDatabase{
		ContractVersion: extensionID + ".database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema},
		Schema:          "logical_schema",
		Role:            "logical_role",
	}
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: protocolDatabaseTrust{}, DatabaseLeases: registry,
		DatabaseLeaseHeartbeatInterval: 50 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  5 * time.Second,
	})
	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = starter.Stop(context.Background(), extension)
		}
	})
	var lease ExtensionDatabaseRuntimeLeaseSnapshot
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		lease, err = scanExtensionDatabaseRuntimeLease(pool.QueryRow(ctx, `
			SELECT id, lease_id, grant_id, extension_id, extension_version_id,
			       extension_version, package_digest, runtime_instance_id, role_name,
			       status, issued_by, COALESCE(issued_by_user_id, 0), issue_audit_event_id,
			       issued_at, last_heartbeat_at, lease_expires_at, draining_at,
			       revoked_at, failure_code, lease_revision
			FROM extension_database_runtime_leases
			WHERE extension_id = $1 AND runtime_instance_id = $2
		`, extensionID, target.InstanceID))
		if err == nil && lease.Revision > 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || lease.Revision <= 1 || lease.Status != ExtensionDatabaseLeaseActive ||
		lease.IssuerKind != ExtensionDatabaseLeaseIssuerHost {
		t.Fatalf("real process lease heartbeat = %#v, %v", lease, err)
	}
	if err := starter.Stop(ctx, extension); err != nil {
		t.Fatal(err)
	}
	stopped = true
	ref := ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: target.InstanceID, LeaseID: lease.LeaseID,
	}
	revoked, err := registry.InspectRuntimeLease(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var roleCount, auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_roles WHERE rolname = $1),
		  (SELECT count(*) FROM audit_events
		   WHERE action IN ($2, $3)
		     AND metadata->>'runtimeInstanceId' = $4
		     AND metadata->>'leaseId' = $5)
	`, lease.RoleName, extensionDatabaseRuntimeLeaseIssuedAudit,
		extensionDatabaseRuntimeLeaseRevokedAudit, target.InstanceID, lease.LeaseID).Scan(&roleCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if revoked.Status != ExtensionDatabaseLeaseRevoked || revoked.Revision <= lease.Revision ||
		roleCount != 0 || auditCount != 2 {
		t.Fatalf("real process revoke evidence: lease=%#v role=%d audit=%d", revoked, roleCount, auditCount)
	}
}
