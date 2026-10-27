package migrator

import (
	"context"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const identitySessionPolicyEvidenceContractVersion = int64(202607190041)

func TestIdentitySessionPolicyEvidenceContractPostgres(t *testing.T) {
	databaseURL := identitySessionPolicyEvidenceTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := db.ExecContext(ctx, `CREATE TABLE users (id BIGSERIAL PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{
		202607190037,
		202607190038,
		202607190039,
		202607190040,
		identitySessionPolicyEvidenceContractVersion,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}

	if _, err := provider.ApplyVersion(ctx, identitySessionPolicyEvidenceContractVersion, false); err != nil {
		t.Fatalf("rollback unused evidence contract: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, identitySessionPolicyEvidenceContractVersion, true); err != nil {
		t.Fatalf("reapply evidence contract: %v", err)
	}
	var immutable, strict, parallelSafe bool
	if err := db.QueryRowContext(ctx, `
		SELECT provolatile = 'i', proisstrict, proparallel = 's'
		FROM pg_proc
		WHERE oid = 'valid_identity_session_policy_evidence(jsonb)'::regprocedure
	`).Scan(&immutable, &strict, &parallelSafe); err != nil {
		t.Fatal(err)
	}
	if !immutable || !strict || !parallelSafe {
		t.Fatalf("evidence validator attributes immutable=%v strict=%v parallelSafe=%v", immutable, strict, parallelSafe)
	}

	pluginEvidence := `{
		"policyId":"fixture.identity.session",
		"providerContractVersion":"fixture.identity.session@1",
		"ownerExtensionId":"fixture.identity",
		"ownerExtensionVersionId":101,
		"ownerExtensionVersion":"1.0.0",
		"ownerPackageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"declarationRevision":7
	}`
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection,
			audit_event_id, selection_revision
		) VALUES ('select', NULL, $1::jsonb, 1001, 1)
	`, pluginEvidence); err != nil {
		t.Fatalf("insert exact plugin evidence: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection,
			audit_event_id, reason_code, selection_revision
		) VALUES ('reset', $1::jsonb, NULL, 1002, 'operator_reset', 2)
	`, pluginEvidence); err != nil {
		t.Fatalf("insert exact reset evidence: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection,
			audit_event_id, selection_revision
		) VALUES (
			'select', $1::jsonb, '{"policyId":"core.session.default"}'::jsonb,
			1003, 3
		)
	`, pluginEvidence); err != nil {
		t.Fatalf("insert exact Core evidence: %v", err)
	}

	invalidEvidence := []string{
		`{}`,
		`{"policyId":"fixture.identity.session"}`,
		`{"policyId":"core.session.default","providerContractVersion":""}`,
		`{"policyId":"core.session.default","sessionId":"secret"}`,
		`{
			"policyId":"fixture.identity.session",
			"providerContractVersion":"fixture.identity.session@1",
			"ownerExtensionId":"fixture.identity",
			"ownerExtensionVersionId":101,
			"ownerExtensionVersion":"1.0.0",
			"ownerPackageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"declarationRevision":7,
			"sessionId":"secret"
		}`,
		`{
			"policyId":"fixture.identity.session",
			"providerContractVersion":"fixture.identity.session@1",
			"ownerExtensionId":"fixture.identity",
			"ownerExtensionVersionId":"101",
			"ownerExtensionVersion":"1.0.0",
			"ownerPackageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"declarationRevision":7
		}`,
		`{
			"policyId":"fixture.identity.session",
			"providerContractVersion":"fixture.identity.session@1",
			"ownerExtensionId":"fixture.identity",
			"ownerExtensionVersionId":101,
			"ownerExtensionVersion":"1.0.0",
			"ownerPackageDigest":"not-a-digest",
			"declarationRevision":7
		}`,
	}
	for _, evidence := range invalidEvidence {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO identity_session_policy_selection_events (
				action, previous_selection, selected_selection,
				audit_event_id, selection_revision
			) VALUES ('select', $1::jsonb, $2::jsonb, 1004, 4)
		`, `{"policyId":"core.session.default"}`, evidence); err == nil ||
			!strings.Contains(err.Error(), "evidence_check") {
			t.Fatalf("invalid evidence %s error=%v", evidence, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection,
			audit_event_id, selection_revision
		) VALUES ('select', '{}'::jsonb, $1::jsonb, 1005, 4)
	`, pluginEvidence); err == nil || !strings.Contains(err.Error(), "previous_evidence_check") {
		t.Fatalf("invalid previous evidence error=%v", err)
	}

	if _, err := provider.ApplyVersion(ctx, identitySessionPolicyEvidenceContractVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove identity session policy evidence contract while evidence exists") {
		t.Fatalf("rollback with retained evidence error=%v", err)
	}
}

func TestIdentitySessionPolicyEvidenceContractRejectsLegacyAmbiguousRows(t *testing.T) {
	databaseURL := identitySessionPolicyEvidenceTestDatabaseURL(t)
	pluginEvidence := `{
		"policyId":"fixture.identity.session",
		"providerContractVersion":"fixture.identity.session@1",
		"ownerExtensionId":"fixture.identity",
		"ownerExtensionVersionId":101,
		"ownerExtensionVersion":"1.0.0",
		"ownerPackageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"declarationRevision":7
	}`
	for _, test := range []struct {
		name       string
		previous   string
		selected   string
		constraint string
		args       []any
	}{
		{
			name: "selected", previous: "NULL", selected: `$1::jsonb`,
			constraint: "identity_session_policy_events_selected_evidence_check",
			args:       []any{`{"policyId":"fixture.session"}`},
		},
		{
			name: "previous", previous: `$1::jsonb`, selected: "$2::jsonb",
			constraint: "identity_session_policy_events_previous_evidence_check",
			args:       []any{`{"policyId":"fixture.session"}`, pluginEvidence},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
			if _, err := db.ExecContext(ctx, `CREATE TABLE users (id BIGSERIAL PRIMARY KEY)`); err != nil {
				t.Fatal(err)
			}
			for _, version := range []int64{202607190037, 202607190038, 202607190039, 202607190040} {
				if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
					t.Fatalf("apply migration %d: %v", version, err)
				}
			}
			query := `INSERT INTO identity_session_policy_selection_events (
				action, previous_selection, selected_selection, audit_event_id, selection_revision
			) VALUES ('select', ` + test.previous + `, ` + test.selected + `, 1001, 1)`
			if _, err := db.ExecContext(ctx, query, test.args...); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ApplyVersion(ctx, identitySessionPolicyEvidenceContractVersion, true); err == nil ||
				!strings.Contains(err.Error(), test.constraint) {
				t.Fatalf("ambiguous retained evidence migration error=%v", err)
			}
		})
	}
}

func identitySessionPolicyEvidenceTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	return databaseURL
}
