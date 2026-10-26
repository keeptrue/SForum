package extensionsruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProtocolStarterBindsExactDatabaseLeaseLifecycle(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust:                          protocolDatabaseTrust{},
		DatabaseLeases:                 registry,
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	target, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if target.InstanceID == "" || registry.issue.RuntimeInstanceID != target.InstanceID ||
		registry.issue.Artifact.VersionID != extension.ActiveVersionID {
		t.Fatalf("runtime lease was not exact: target=%#v issue=%#v", target, registry.issue)
	}
	waitProtocolDatabaseLease(t, func() bool { return registry.heartbeatCount() > 0 })
	if err := starter.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.snapshot()
	if snapshot.Status != ExtensionDatabaseLeaseRevoked || registry.drainCount != 1 || registry.revokeCount != 1 {
		t.Fatalf("normal stop lease evidence: snapshot=%#v drain=%d revoke=%d", snapshot, registry.drainCount, registry.revokeCount)
	}
}

func TestProtocolStarterRevokesDatabaseLeaseAfterStartFailure(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: protocolDatabaseTrust{}, DatabaseLeases: registry,
		DatabaseLeaseOperationTimeout: time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, false)
	entry, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
	if !ok {
		t.Fatal("resolve failing helper entry")
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.Start(context.Background(), extension); err == nil {
		t.Fatal("failing plugin process unexpectedly started")
	}
	if snapshot := registry.snapshot(); snapshot.Status != ExtensionDatabaseLeaseRevoked || registry.revokeCount != 1 {
		t.Fatalf("start failure retained database lease: %#v revoke=%d", snapshot, registry.revokeCount)
	}
}

func TestProtocolStarterRevokesDatabaseLeaseAfterProcessCrash(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: protocolDatabaseTrust{}, DatabaseLeases: registry,
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	target, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	instance := starter.protocolInstance(RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID})
	if instance == nil {
		t.Fatal("started runtime instance is missing")
	}
	instance.client.Kill()
	waitProtocolDatabaseLease(t, func() bool {
		return registry.snapshot().Status == ExtensionDatabaseLeaseRevoked
	})
	if _, err := starter.InspectInstance(instance.identity); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("crashed runtime remained retained: %v", err)
	}
}

func TestProtocolStarterRevokesDatabaseLeaseAfterDiscard(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: protocolDatabaseTrust{}, DatabaseLeases: registry,
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	target, err := starter.startProtocolInstanceLocked(context.Background(), extension, false)
	if err != nil {
		t.Fatal(err)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
	instance := starter.protocolInstance(identity)
	if instance == nil {
		t.Fatal("staged test runtime is missing")
	}
	// The transport is irrelevant to Discard's exact-process cleanup; mark this
	// unpublished fixture as V2 so it exercises the public candidate boundary.
	instance.protocolVersion = 2
	if err := starter.DiscardInstance(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.snapshot(); snapshot.Status != ExtensionDatabaseLeaseRevoked || registry.revokeCount != 1 {
		t.Fatalf("discard retained database lease: %#v revoke=%d", snapshot, registry.revokeCount)
	}
}

func TestProtocolStarterKillsRuntimeBeforeDatabaseLeaseExpires(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	registry.heartbeatErr = ErrExtensionDatabaseRuntimeLeaseConflict
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: protocolDatabaseTrust{}, DatabaseLeases: registry,
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	target, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	waitProtocolDatabaseLease(t, func() bool {
		return registry.snapshot().Status == ExtensionDatabaseLeaseRevoked
	})
	if _, err := starter.InspectInstance(RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: target.InstanceID,
	}); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("runtime survived terminal database heartbeat: %v", err)
	}
}

func protocolDatabaseLeaseExtension(t *testing.T, expectDatabaseEnv bool) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.database", "1.0.0")
	filesRoot := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\n"
	if expectDatabaseEnv {
		launcher += "SFORUM_PLUGIN_EXPECT_DATABASE=lease "
	}
	launcher += "SFORUM_PLUGIN_HELPER=protocol-v2-no-services exec " + shellQuote(os.Args[0]) + " -test.run=TestProtocolV2DatabaseLeaseHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(filesRoot, "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	extension := runtimeExtension("runtime.database")
	extension.Source = extensions.SourceUploaded
	extension.Version = "1.0.0"
	extension.PackageDigest = strings.Repeat("a", 64)
	extension.PackagePath = packageRoot
	extension.ActiveVersionID = 71
	extension.Manifest.Database = &extensions.ManifestDatabase{
		ContractVersion: "runtime.database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema},
	}
	return extension
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type protocolDatabaseTrust struct{}

func (protocolDatabaseTrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: "database-grant", ImpactDigest: "database-impact"}, nil
}

type protocolDatabaseLeaseRegistry struct {
	mu sync.Mutex

	issue        ExtensionDatabaseRuntimeLeaseIssue
	lease        ExtensionDatabaseRuntimeLeaseSnapshot
	heartbeats   int
	drainCount   int
	revokeCount  int
	heartbeatErr error
}

func newProtocolDatabaseLeaseRegistry() *protocolDatabaseLeaseRegistry {
	return &protocolDatabaseLeaseRegistry{}
}

func (r *protocolDatabaseLeaseRegistry) IssueRuntimeLease(
	_ context.Context,
	request ExtensionDatabaseRuntimeLeaseIssue,
) (ExtensionDatabaseRuntimeCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease.Status == ExtensionDatabaseLeaseActive || !r.accepts(request.Authority) {
		return ExtensionDatabaseRuntimeCredential{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	now := time.Now().UTC()
	r.issue = request
	r.lease = ExtensionDatabaseRuntimeLeaseSnapshot{
		ID: 1, LeaseID: strings.Repeat("b", 64), GrantID: 2,
		Artifact: request.Artifact, RuntimeInstanceID: request.RuntimeInstanceID,
		RoleName: "sforum_ext_l_runtime_database_deadbeef", Status: ExtensionDatabaseLeaseActive,
		IssuerKind: ExtensionDatabaseLeaseIssuerHost, IssueAuditEventID: 3,
		IssuedAt: now, LastHeartbeatAt: now, ExpiresAt: now.Add(2 * time.Minute), Revision: 1,
	}
	return ExtensionDatabaseRuntimeCredential{
		LeaseID: r.lease.LeaseID, GrantID: r.lease.GrantID, Artifact: request.Artifact,
		RuntimeInstanceID: request.RuntimeInstanceID,
		Powers:            []string{extensionmanifest.DatabaseGrantOwnSchema},
		SchemaName:        "sforum_ext_s_runtime_database_deadbeef",
		OwnerRoleName:     "sforum_ext_o_runtime_database_deadbeef",
		RoleName:          r.lease.RoleName,
		DatabaseName:      "sforum",
		SearchPath:        "sforum_ext_s_runtime_database_deadbeef, pg_catalog",
		ConnectionURL:     "postgres://lease_role:lease_secret@127.0.0.1:5432/sforum?sslmode=disable",
		Password:          strings.Repeat("A", 43),
		ExpiresAt:         r.lease.ExpiresAt,
		Revision:          r.lease.Revision,
	}, nil
}

func (r *protocolDatabaseLeaseRegistry) HeartbeatRuntimeLease(
	_ context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	expectedRevision int64,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.heartbeatErr != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, r.heartbeatErr
	}
	if !r.matches(ref) || r.lease.Status != ExtensionDatabaseLeaseActive || r.lease.Revision != expectedRevision {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	r.heartbeats++
	r.lease.Revision++
	r.lease.LastHeartbeatAt = time.Now().UTC()
	r.lease.ExpiresAt = r.lease.LastHeartbeatAt.Add(2 * time.Minute)
	return r.lease, nil
}

func (r *protocolDatabaseLeaseRegistry) BeginRuntimeLeaseDrain(
	_ context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	expectedRevision int64,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(ref) || r.lease.Status != ExtensionDatabaseLeaseActive || r.lease.Revision != expectedRevision {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	r.drainCount++
	r.lease.Status = ExtensionDatabaseLeaseDraining
	r.lease.Revision++
	now := time.Now().UTC()
	r.lease.DrainingAt = &now
	return r.lease, nil
}

func (r *protocolDatabaseLeaseRegistry) RevokeRuntimeLease(
	_ context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	authority ExtensionDatabaseLeaseAuthority,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(ref) || r.lease.Status != ExtensionDatabaseLeaseDraining ||
		authority.Kind != ExtensionDatabaseLeaseIssuerHost || authority.AuditEventID != 0 {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	r.revokeCount++
	r.lease.Status = ExtensionDatabaseLeaseRevoked
	r.lease.Revision++
	now := time.Now().UTC()
	r.lease.RevokedAt = &now
	return r.lease, nil
}

func (r *protocolDatabaseLeaseRegistry) InspectRuntimeLease(
	_ context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(ref) {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseNotFound
	}
	return r.lease, nil
}

// accepts 复刻真实注册表的权威校验：host 权威不带 actor，actor 权威必须同时
// 带 actor 与审计事件 id。
func (r *protocolDatabaseLeaseRegistry) accepts(authority ExtensionDatabaseLeaseAuthority) bool {
	switch authority.Kind {
	case ExtensionDatabaseLeaseIssuerHost:
		return authority.ActorUserID == 0 && authority.AuditEventID == 0
	case ExtensionDatabaseLeaseIssuerActor:
		return authority.ActorUserID > 0 && authority.AuditEventID > 0
	default:
		return false
	}
}

func (r *protocolDatabaseLeaseRegistry) matches(ref ExtensionDatabaseRuntimeLeaseRef) bool {
	return r.lease.LeaseID == ref.LeaseID && r.lease.Artifact == ref.Artifact &&
		r.lease.RuntimeInstanceID == ref.RuntimeInstanceID
}

func (r *protocolDatabaseLeaseRegistry) heartbeatCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heartbeats
}

func (r *protocolDatabaseLeaseRegistry) snapshot() ExtensionDatabaseRuntimeLeaseSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lease
}

func waitProtocolDatabaseLease(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("database lease lifecycle did not converge")
}

var _ RuntimeDatabaseLeaseRegistry = (*protocolDatabaseLeaseRegistry)(nil)

func TestProtocolDatabaseLeaseAuthorityFollowsTheStartContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want ExtensionDatabaseLeaseAuthority
	}{
		{
			name: "host start",
			ctx:  context.Background(),
			want: ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost},
		},
		{
			name: "operator bound start",
			ctx: extensions.WithDatabaseGrantAuthority(context.Background(), extensions.DatabaseGrantAuthority{
				ActorUserID: 31, AuditEventID: 77,
			}),
			want: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 31, AuditEventID: 77,
			},
		},
		{
			name: "half authority stays host",
			ctx:  extensions.WithDatabaseGrantAuthority(context.Background(), extensions.DatabaseGrantAuthority{ActorUserID: 31}),
			want: ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := protocolDatabaseLeaseAuthority(item.ctx); got != item.want {
				t.Fatalf("authority=%#v want %#v", got, item.want)
			}
		})
	}
}

func TestProtocolStarterIssuesOperatorBoundLeaseWhenContextCarriesAuthority(t *testing.T) {
	registry := newProtocolDatabaseLeaseRegistry()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust:                          protocolDatabaseTrust{},
		DatabaseLeases:                 registry,
		DatabaseLeaseHeartbeatInterval: time.Hour,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	ctx := extensions.WithDatabaseGrantAuthority(context.Background(), extensions.DatabaseGrantAuthority{
		ActorUserID: 17, AuditEventID: 23,
	})
	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatal(err)
	}
	if registry.issue.Authority.Kind != ExtensionDatabaseLeaseIssuerActor ||
		registry.issue.Authority.ActorUserID != 17 || registry.issue.Authority.AuditEventID != 23 {
		t.Fatalf("issued lease authority=%#v", registry.issue.Authority)
	}
	if err := starter.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if registry.revokeCount != 1 {
		t.Fatalf("lease must be drained and revoked exactly once: %d", registry.revokeCount)
	}
	if target.InstanceID == "" {
		t.Fatal("operator-bound start must still produce an exact instance")
	}
}
