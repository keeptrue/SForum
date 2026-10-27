package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const externalAuthRegistrationAuditRedactionMigration = "202607270059_external_auth_registration_audit_redaction.sql"

func TestR3_Migration059RedactsOnlyExternalRegistrationAuditHistory(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("external_auth_redaction_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE audit_events (id BIGSERIAL PRIMARY KEY, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb);
		CREATE TABLE extension_versions (id BIGSERIAL PRIMARY KEY, package_digest TEXT NOT NULL);
		INSERT INTO extension_versions (package_digest) VALUES (repeat('e', 64));
		INSERT INTO audit_events (action, metadata) VALUES
		  ('auth.external_register.success', jsonb_build_object(
		    'providerId', 'provider.alpha', 'ownerExtensionId', 'ext.alpha', 'correlationId', 'c1',
		    'ownerPackageDigest', repeat('a', 64), 'subjectDigest', repeat('b', 64),
		    'providerSubject', 'raw-subject', 'state', 'state-secret', 'codeVerifier', 'verifier',
		    'completionToken', 'code', 'token', 'token', 'secret', 'secret', 'clientSecret', 'secret')),
		  ('unrelated.audit', jsonb_build_object('ownerPackageDigest', repeat('c', 64), 'subjectDigest', repeat('d', 64)));
	`); err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(Files(), externalAuthRegistrationAuditRedactionMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration 059 has no Down section")
	}
	if _, err := pool.Exec(ctx, stripSQLComments(parts[0])); err != nil {
		t.Fatalf("apply migration 059: %v", err)
	}

	var redacted, unrelated string
	if err := pool.QueryRow(ctx, `SELECT metadata::text FROM audit_events WHERE action = 'auth.external_register.success'`).Scan(&redacted); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT metadata::text FROM audit_events WHERE action = 'unrelated.audit'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"ownerPackageDigest", "subjectDigest", "providerSubject", "raw-subject",
		"state-secret", "verifier", "completionToken", "token", "secret", "clientSecret",
		repeatString("a", 64), repeatString("b", 64),
	} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted row still contains %q: %s", forbidden, redacted)
		}
	}
	for _, required := range []string{"providerId", "provider.alpha", "ownerExtensionId", "ext.alpha", "correlationId", "c1"} {
		if !strings.Contains(unquoteJSON(redacted), required) {
			t.Fatalf("redacted row lost required audit field %q: %s", required, redacted)
		}
	}
	if !strings.Contains(unrelated, "ownerPackageDigest") || !strings.Contains(unrelated, repeatString("c", 64)) {
		t.Fatalf("migration changed unrelated audit history: %s", unrelated)
	}
	var artifactDigest string
	if err := pool.QueryRow(ctx, `SELECT package_digest FROM extension_versions`).Scan(&artifactDigest); err != nil {
		t.Fatal(err)
	}
	if artifactDigest != repeatString("e", 64) {
		t.Fatalf("migration changed immutable extension artifact history: %s", artifactDigest)
	}
}

func repeatString(value string, count int) string {
	return strings.Repeat(value, count)
}

func unquoteJSON(value string) string {
	return value
}
