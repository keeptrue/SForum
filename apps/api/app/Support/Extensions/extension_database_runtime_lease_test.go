package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestMissingExtensionDatabaseRuntimeLeaseRoleOnlyAcceptsUndefinedObject(t *testing.T) {
	if !isMissingExtensionDatabaseRuntimeLeaseRole(&pgconn.PgError{Code: "42704"}) {
		t.Fatal("undefined PostgreSQL role must be an idempotent lease cleanup result")
	}
	if isMissingExtensionDatabaseRuntimeLeaseRole(&pgconn.PgError{Code: "42501"}) {
		t.Fatal("permission failure must not be accepted as an idempotent lease cleanup result")
	}
	if isMissingExtensionDatabaseRuntimeLeaseRole(errors.New("role missing")) {
		t.Fatal("untyped error must not be accepted as an idempotent lease cleanup result")
	}
}

func TestExtensionDatabaseRuntimeLeaseSearchPathFollowsExactPowers(t *testing.T) {
	tests := []struct {
		name   string
		powers []string
		want   string
	}{
		{"own schema", []string{extensionmanifest.DatabaseGrantOwnSchema}, `"plugin_schema", pg_catalog`},
		{"core views", []string{extensionmanifest.DatabaseGrantCoreViews}, `"sforum_core_v1", pg_catalog`},
		{"raw core", []string{extensionmanifest.DatabaseGrantRawCore}, `"public", pg_catalog`},
		{"all", []string{
			extensionmanifest.DatabaseGrantOwnSchema, extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantHostCommands, extensionmanifest.DatabaseGrantRawCore,
		}, `"plugin_schema", "sforum_core_v1", "public", pg_catalog`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := extensionDatabaseRuntimeLeaseSearchPath("plugin_schema", test.powers); got != test.want {
				t.Fatalf("search path = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtensionDatabaseRuntimeLeaseInputFailsClosed(t *testing.T) {
	registry := &PostgresExtensionDatabaseRegistry{}
	artifact := ExtensionDatabaseArtifact{
		ExtensionID: "demo.plugin", Version: "1.0.0", VersionID: 1,
		PackageDigest: strings.Repeat("a", 64),
	}
	valid := ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "runtime-1",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 42, AuditEventID: 43,
		},
	}
	if err := registry.validateRuntimeLeaseIssue(context.Background(), valid); err == nil {
		t.Fatal("registry without a PostgreSQL pool accepted runtime lease issue")
	}
	if _, err := registry.ReapExpiredRuntimeLeases(context.Background(), 1); !errors.Is(err, ErrExtensionDatabaseRegistryInvalid) {
		t.Fatalf("registry without a PostgreSQL pool reaped leases: %v", err)
	}
	for _, authority := range []ExtensionDatabaseLeaseAuthority{
		{},
		{Kind: ExtensionDatabaseLeaseIssuerActor, AuditEventID: 1},
		{Kind: ExtensionDatabaseLeaseIssuerHost, ActorUserID: 1, AuditEventID: 2},
		{Kind: "plugin", ActorUserID: 1, AuditEventID: 2},
	} {
		if validExtensionDatabaseLeaseAuthority(authority) {
			t.Fatalf("invalid lease authority accepted: %#v", authority)
		}
	}
	if !validExtensionDatabaseLeaseAuthority(ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost, AuditEventID: 2,
	}) {
		t.Fatal("Host lease authority was rejected")
	}
	if !validExtensionDatabaseLeaseAuthority(ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost,
	}) {
		t.Fatal("Host-owned audit lease authority was rejected")
	}
	if validExtensionDatabaseLeaseAuthority(ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost, AuditEventID: -1,
	}) {
		t.Fatal("negative Host audit identity was accepted")
	}
}

func TestExtensionDatabaseRuntimeConnectionURLReplacesHostAuthority(t *testing.T) {
	base, err := pgx.ParseConfig("postgres://host_user:host_secret@db.internal:5433/host_db?sslmode=disable&search_path=public&pool_max_conns=9")
	if err != nil {
		t.Fatal(err)
	}
	credential := ExtensionDatabaseRuntimeCredential{
		LeaseID:      strings.Repeat("a", 64),
		Artifact:     ExtensionDatabaseArtifact{ExtensionID: "vendor.plugin"},
		RoleName:     "sforum_ext_l_vendor_plugin_deadbeef",
		DatabaseName: "sforum", Password: strings.Repeat("A", 43),
	}
	connectionURL, err := extensionDatabaseRuntimeConnectionURL(base, credential)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(connectionURL, "host_user") || strings.Contains(connectionURL, "host_secret") ||
		strings.Contains(connectionURL, "search_path") || strings.Contains(connectionURL, "pool_max_conns") {
		t.Fatalf("runtime URL retained Host authority: %s", connectionURL)
	}
	parsed, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.User != credential.RoleName || parsed.Password != credential.Password ||
		parsed.Database != credential.DatabaseName || parsed.Host != "db.internal" || parsed.Port != 5433 {
		t.Fatalf("runtime connection URL mismatch: %#v", parsed.Config)
	}
}
