package migrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const identityRoleApprovalsVersion = int64(202607160029)

type identityRoleApprovalSuggestion struct {
	ID                        int64
	PermissionKey             string
	OwnerExtensionID          string
	ExtensionVersionID        int64
	ExtensionVersion          string
	PackageDigest             string
	PermissionContractVersion string
	DeclarationDigest         string
	RoleKey                   string
	Revision                  int64
}

func TestIdentityRoleApprovalsMigrationEnforcesAuthorityAndAuditBinding(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	seedIdentityRoleApprovalCatalog(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", 1)

	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	unauthorizedID := identityRoleApprovalUserID(t, ctx, db, "member")

	unauthorized := insertIdentityRoleApprovalSuggestion(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "future_reject")
	unauthorizedAudit := insertIdentityRoleApprovalAudit(t, ctx, db, unauthorizedID, unauthorized, "rejected", false, false)
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		unauthorized.ID, "rejected", unauthorizedID, unauthorizedAudit); err == nil ||
		!strings.Contains(err.Error(), "actor lacks role.manage") {
		t.Fatalf("unauthorized role suggestion decision error=%v", err)
	}

	rejected := insertIdentityRoleApprovalSuggestion(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "future_reject_2")
	rejectAudit := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, rejected, "rejected", false, false)
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		rejected.ID, "rejected", managerID, rejectAudit); err != nil {
		t.Fatalf("reject exact suggestion: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_events SET action = 'tampered' WHERE id = $1`, rejectAudit); err == nil ||
		!strings.Contains(err.Error(), "audit evidence is immutable") {
		t.Fatalf("referenced audit update error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_events WHERE id = $1`, rejectAudit); err == nil ||
		!strings.Contains(err.Error(), "audit evidence is immutable") {
		t.Fatalf("referenced audit delete error=%v", err)
	}

	approved := insertIdentityRoleApprovalSuggestion(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member")
	approveAudit := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, approved, "approved", true, true)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = $2
	`, approved.PermissionKey, approved.RoleKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_role_grants (
			suggestion_id, permission_key, owner_extension_id, role_key, role_id,
			applied_by_user_id, applied_audit_event_id
		)
		SELECT $1, $2, $3, $4, roles.id, $5, $6
		FROM roles WHERE key = $4
	`, approved.ID, approved.PermissionKey, approved.OwnerExtensionID,
		approved.RoleKey, managerID, approveAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		approved.ID, "approved", managerID, approveAudit); err != nil {
		t.Fatalf("approve exact suggestion: %v", err)
	}

	missingGrant := insertIdentityRoleApprovalSuggestion(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "operator")
	missingGrantAudit := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, missingGrant, "approved", true, true)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = $2
	`, missingGrant.PermissionKey, missingGrant.RoleKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		missingGrant.ID, "approved", managerID, missingGrantAudit); err == nil ||
		!strings.Contains(err.Error(), "grant evidence is missing") {
		t.Fatalf("approval without grant evidence error=%v", err)
	}

	wrongAudit := insertIdentityRoleApprovalSuggestion(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "moderator")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = $2
	`, wrongAudit.PermissionKey, wrongAudit.RoleKey); err != nil {
		t.Fatal(err)
	}
	// Bound grant uses a correct audit; decision attempts a different audit with a
	// mismatched roleKey so the decision trigger rejects the CAS.
	grantAudit := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, wrongAudit, "approved", true, true)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_role_grants (
			suggestion_id, permission_key, owner_extension_id, role_key, role_id,
			applied_by_user_id, applied_audit_event_id
		)
		SELECT $1, $2, $3, $4, roles.id, $5, $6
		FROM roles WHERE key = $4
	`, wrongAudit.ID, wrongAudit.PermissionKey, wrongAudit.OwnerExtensionID,
		wrongAudit.RoleKey, managerID, grantAudit); err != nil {
		t.Fatal(err)
	}
	mismatched := wrongAudit
	mismatched.RoleKey = "other"
	wrongAuditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, mismatched, "approved", true, true)
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		wrongAudit.ID, "approved", managerID, wrongAuditID); err == nil ||
		!(strings.Contains(err.Error(), "audit evidence is invalid") ||
			strings.Contains(err.Error(), "grant evidence is missing")) {
		t.Fatalf("approval with mismatched audit error=%v", err)
	}

	var approvedState string
	var grantCount, catalogCount, grantEvidence int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT approval_state FROM extension_permission_role_suggestions WHERE id = $1),
		  (SELECT count(*) FROM role_permissions
		   JOIN roles ON roles.id = role_permissions.role_id
		   WHERE roles.key = 'member' AND permission_key = $2),
		  (SELECT count(*) FROM extension_permission_catalog WHERE permission_key = $2),
		  (SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1)
	`, approved.ID, approved.PermissionKey).Scan(&approvedState, &grantCount, &catalogCount, &grantEvidence); err != nil {
		t.Fatal(err)
	}
	if approvedState != "approved" || grantCount != 1 || catalogCount != 1 || grantEvidence != 1 {
		t.Fatalf("approved state=%q grants=%d catalog=%d evidence=%d",
			approvedState, grantCount, catalogCount, grantEvidence)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, managerID); err != nil {
		t.Fatalf("erase decision actor while retaining authority evidence: %v", err)
	}
	var nulledActors, retainedDecider, retainedApplier int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM audit_events
		   WHERE id IN ($1, $2) AND actor_user_id IS NULL),
		  (SELECT count(*) FROM extension_permission_role_suggestions
		   WHERE id = $3 AND decided_by_user_id = $4),
		  (SELECT count(*) FROM extension_permission_role_grants
		   WHERE suggestion_id = $3 AND applied_by_user_id = $4)
	`, rejectAudit, approveAudit, approved.ID, managerID).Scan(
		&nulledActors, &retainedDecider, &retainedApplier,
	); err != nil {
		t.Fatal(err)
	}
	if nulledActors != 2 || retainedDecider != 1 || retainedApplier != 1 {
		t.Fatalf(
			"erased actor evidence nulled=%d decider=%d applier=%d",
			nulledActors, retainedDecider, retainedApplier,
		)
	}
	var grantsBeforeDown int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_permission_role_grants
	`).Scan(&grantsBeforeDown); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove identity role approval authority history") {
		t.Fatalf("authority-protected Down error=%v", err)
	}
	var catalogExists, grantsExist bool
	var ownerRows, declarationRows, retainedGrantRows int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  to_regclass(current_schema() || '.extension_permission_catalog') IS NOT NULL,
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL,
		  (SELECT count(*) FROM extension_identity_registry_owners),
		  (SELECT count(*) FROM extension_identity_registry_declarations),
		  (SELECT count(*) FROM extension_permission_role_grants)
	`).Scan(&catalogExists, &grantsExist, &ownerRows, &declarationRows, &retainedGrantRows); err != nil {
		t.Fatal(err)
	}
	if !catalogExists || !grantsExist || ownerRows != 1 || declarationRows != 1 ||
		retainedGrantRows != grantsBeforeDown {
		t.Fatalf(
			"failed Down changed authority catalog=%t grants=%t owners=%d declarations=%d grantRows=%d",
			catalogExists, grantsExist, ownerRows, declarationRows, retainedGrantRows,
		)
	}
}

func TestIdentityRoleApprovalsMigrationAcceptsPreexistingMappingAndRejectsInvalidAuditFlags(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	seedIdentityRoleApprovalCatalog(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", 1)
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")

	preexisting := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = $2
	`, preexisting.PermissionKey, preexisting.RoleKey); err != nil {
		t.Fatal(err)
	}
	preexistingAudit := insertIdentityRoleApprovalAudit(
		t, ctx, db, managerID, preexisting, "approved", false, true,
	)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_permission_role_grants (
			suggestion_id, permission_key, owner_extension_id, role_key, role_id,
			applied_by_user_id, applied_audit_event_id
		)
		SELECT $1, $2, $3, $4, roles.id, $5, $6
		FROM roles WHERE key = $4
	`, preexisting.ID, preexisting.PermissionKey, preexisting.OwnerExtensionID,
		preexisting.RoleKey, managerID, preexistingAudit); err != nil {
		_ = tx.Rollback()
		t.Fatalf("grant with pre-existing Host mapping: %v", err)
	}
	if _, err := tx.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		preexisting.ID, "approved", managerID, preexistingAudit); err != nil {
		_ = tx.Rollback()
		t.Fatalf("approve with pre-existing Host mapping: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var state string
	var mappings, grants int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT approval_state FROM extension_permission_role_suggestions WHERE id = $1),
		  (SELECT count(*) FROM role_permissions
		   JOIN roles ON roles.id = role_permissions.role_id
		   WHERE roles.key = $2 AND permission_key = $3),
		  (SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1)
	`, preexisting.ID, preexisting.RoleKey, preexisting.PermissionKey).Scan(
		&state, &mappings, &grants,
	); err != nil {
		t.Fatal(err)
	}
	if state != "approved" || mappings != 1 || grants != 1 {
		t.Fatalf("pre-existing mapping approval state=%s mappings=%d grants=%d", state, mappings, grants)
	}

	invalidCases := []struct {
		name      string
		roleKey   string
		mutateSQL string
		grantFlag bool
	}{
		{
			name:      "missing rolePermissionAdded",
			roleKey:   "operator",
			mutateSQL: `UPDATE audit_events SET metadata = metadata - 'rolePermissionAdded' WHERE id = $1`,
			grantFlag: true,
		},
		{
			name:      "non-boolean rolePermissionAdded",
			roleKey:   "moderator",
			mutateSQL: `UPDATE audit_events SET metadata = jsonb_set(metadata, '{rolePermissionAdded}', '"false"'::jsonb) WHERE id = $1`,
			grantFlag: true,
		},
		{
			name:      "roleGrantApplied false",
			roleKey:   "identity_manager",
			grantFlag: false,
		},
	}
	for _, testCase := range invalidCases {
		t.Run(testCase.name, func(t *testing.T) {
			suggestion := insertIdentityRoleApprovalSuggestion(
				t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", testCase.roleKey,
			)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO role_permissions (role_id, permission_key)
				SELECT id, $1 FROM roles WHERE key = $2
			`, suggestion.PermissionKey, suggestion.RoleKey); err != nil {
				t.Fatal(err)
			}
			auditID := insertIdentityRoleApprovalAudit(
				t, ctx, db, managerID, suggestion, "approved", false, testCase.grantFlag,
			)
			if testCase.mutateSQL != "" {
				if _, err := db.ExecContext(ctx, testCase.mutateSQL, auditID); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := db.ExecContext(ctx, `
				INSERT INTO extension_permission_role_grants (
					suggestion_id, permission_key, owner_extension_id, role_key, role_id,
					applied_by_user_id, applied_audit_event_id
				)
				SELECT $1, $2, $3, $4, roles.id, $5, $6
				FROM roles WHERE key = $4
			`, suggestion.ID, suggestion.PermissionKey, suggestion.OwnerExtensionID,
				suggestion.RoleKey, managerID, auditID); err == nil ||
				!strings.Contains(err.Error(), "audit evidence is invalid") {
				t.Fatalf("invalid grant audit error=%v", err)
			}

			var pendingState string
			var mappingRows, grantRows int
			if err := db.QueryRowContext(ctx, `
				SELECT
				  (SELECT approval_state FROM extension_permission_role_suggestions WHERE id = $1),
				  (SELECT count(*) FROM role_permissions
				   JOIN roles ON roles.id = role_permissions.role_id
				   WHERE roles.key = $2 AND permission_key = $3),
				  (SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1)
			`, suggestion.ID, suggestion.RoleKey, suggestion.PermissionKey).Scan(
				&pendingState, &mappingRows, &grantRows,
			); err != nil {
				t.Fatal(err)
			}
			if pendingState != "pending" || mappingRows != 1 || grantRows != 0 {
				t.Fatalf("invalid audit changed state=%s mappings=%d grants=%d", pendingState, mappingRows, grantRows)
			}
		})
	}
}

func TestIdentityRoleApprovalsMigrationEmptyDownAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, false); err != nil {
		t.Fatalf("empty identity role approvals Down: %v", err)
	}
	var catalogExists, grantsExist bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  to_regclass(current_schema() || '.extension_permission_catalog') IS NOT NULL,
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL
	`).Scan(&catalogExists, &grantsExist); err != nil {
		t.Fatal(err)
	}
	if catalogExists || grantsExist {
		t.Fatal("authority tables survived empty Down")
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatalf("reapply identity role approvals migration: %v", err)
	}
}

func TestIdentityRoleApprovalsMigrationDownPreservesNonPermissionOwnership(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalOwner(t, ctx, db, "provider", "fixture.identity.auth")
	seedIdentityRoleApprovalDeclarationHistory(
		t, ctx, db, "provider", "fixture.identity.auth", "fixture.identity.auth@1", "d",
	)

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, false); err != nil {
		t.Fatalf("Down with non-permission ownership: %v", err)
	}

	var ownerRows, declarationRows int
	var catalogExists, grantsExist bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_identity_registry_owners
		   WHERE identity_kind = 'provider' AND stable_id = 'fixture.identity.auth'),
		  (SELECT count(*) FROM extension_identity_registry_declarations
		   WHERE identity_kind = 'provider' AND stable_id = 'fixture.identity.auth'),
		  to_regclass(current_schema() || '.extension_permission_catalog') IS NOT NULL,
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL
	`).Scan(&ownerRows, &declarationRows, &catalogExists, &grantsExist); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 1 || declarationRows != 1 || catalogExists || grantsExist {
		t.Fatalf(
			"Down changed 028 ownership owners=%d declarations=%d catalog=%t grants=%t",
			ownerRows, declarationRows, catalogExists, grantsExist,
		)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM extension_identity_registry_owners
		WHERE identity_kind = 'provider' AND stable_id = 'fixture.identity.auth'
	`); err == nil || !strings.Contains(err.Error(), "ownership history is append-only") {
		t.Fatalf("028 ownership guard after Down error=%v", err)
	}
}

func TestIdentityRoleApprovalsMigrationPreservesLegacyApprovedWithoutGrant(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	// Pre-029 review-only approval audit (no grant flags).
	auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)
	if _, err := db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
		suggestion.ID, "approved", managerID, auditID); err != nil {
		t.Fatalf("seed legacy review-only approval: %v", err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatalf("legacy approved upgrade: %v", err)
	}

	var state string
	var revision, grantRows, mappingRows, catalogRows int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT approval_state FROM extension_permission_role_suggestions WHERE id = $1),
		  (SELECT revision FROM extension_permission_role_suggestions WHERE id = $1),
		  (SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1),
		  (SELECT count(*) FROM role_permissions
		   JOIN roles ON roles.id = role_permissions.role_id
		   WHERE roles.key = 'member' AND permission_key = $2),
		  (SELECT count(*) FROM extension_permission_catalog WHERE permission_key = $2)
	`, suggestion.ID, suggestion.PermissionKey).Scan(
		&state, &revision, &grantRows, &mappingRows, &catalogRows,
	); err != nil {
		t.Fatal(err)
	}
	if state != "approved" || revision != 2 || grantRows != 0 || mappingRows != 0 {
		t.Fatalf("legacy approved leaked grant state=%s rev=%d grants=%d mappings=%d",
			state, revision, grantRows, mappingRows)
	}
	if catalogRows != 1 {
		t.Fatalf("legacy upgrade catalog rows=%d", catalogRows)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove identity role approval authority history") {
		t.Fatalf("legacy authority-protected Down error=%v", err)
	}
	var ownerRows, declarationRows int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_identity_registry_owners
		   WHERE identity_kind = 'permission' AND stable_id = $1),
		  (SELECT count(*) FROM extension_identity_registry_declarations
		   WHERE identity_kind = 'permission' AND stable_id = $1)
	`, suggestion.PermissionKey).Scan(&ownerRows, &declarationRows); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 1 || declarationRows != 1 {
		t.Fatalf("failed legacy Down changed 028 ownership owners=%d declarations=%d", ownerRows, declarationRows)
	}

	// Explicit apply with expected revision 2 and a new actor-bound audit.
	applyAudit := insertIdentityRoleApprovalApplyAudit(t, ctx, db, managerID, suggestion, 2)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = $2
	`, suggestion.PermissionKey, suggestion.RoleKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_role_grants (
			suggestion_id, permission_key, owner_extension_id, role_key, role_id,
			applied_by_user_id, applied_audit_event_id
		)
		SELECT $1, $2, $3, $4, roles.id, $5, $6
		FROM roles WHERE key = $4
	`, suggestion.ID, suggestion.PermissionKey, suggestion.OwnerExtensionID,
		suggestion.RoleKey, managerID, applyAudit); err != nil {
		t.Fatalf("explicit legacy apply grant: %v", err)
	}
	var originalDecisionAudit int64
	if err := db.QueryRowContext(ctx, `
		SELECT decision_audit_event_id
		FROM extension_permission_role_suggestions WHERE id = $1
	`, suggestion.ID).Scan(&originalDecisionAudit); err != nil {
		t.Fatal(err)
	}
	if originalDecisionAudit != auditID {
		t.Fatalf("legacy apply rewrote decision audit %d -> %d", auditID, originalDecisionAudit)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1
	`, suggestion.ID).Scan(&grantRows); err != nil {
		t.Fatal(err)
	}
	if grantRows != 1 {
		t.Fatalf("legacy apply grant rows=%d", grantRows)
	}
}

func TestIdentityRoleApprovalsMigrationBackfillsCatalogWithoutSuggestions(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b")

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}

	var permissionCount, catalogCount int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM permissions WHERE key LIKE 'fixture.identity.%'),
		  (SELECT count(*) FROM extension_permission_catalog)
	`).Scan(&permissionCount, &catalogCount); err != nil {
		t.Fatal(err)
	}
	if permissionCount != 2 || catalogCount != 2 {
		t.Fatalf("no-suggestion backfill permissions=%d catalog=%d", permissionCount, catalogCount)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove identity role approval authority history") {
		t.Fatalf("catalog-protected Down error=%v", err)
	}
	var ownerRows, declarationRows int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_identity_registry_owners WHERE identity_kind = 'permission'),
		  (SELECT count(*) FROM extension_identity_registry_declarations WHERE identity_kind = 'permission')
	`).Scan(&ownerRows, &declarationRows); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 2 || declarationRows != 2 {
		t.Fatalf("failed catalog Down changed owners=%d declarations=%d", ownerRows, declarationRows)
	}
}

func TestIdentityRoleApprovalsMigrationRejectsPermissionOwnerWithoutDeclarationHistory(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	const permissionKey = "fixture.identity.orphan"
	seedIdentityRoleApprovalOwner(t, ctx, db, "permission", permissionKey)

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err == nil ||
		!strings.Contains(err.Error(), "without declaration history") {
		t.Fatalf("orphan permission owner migration error=%v", err)
	}
	var ownerRows, declarationRows, permissionRows int
	var catalogExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_identity_registry_owners
		   WHERE identity_kind = 'permission' AND stable_id = $1),
		  (SELECT count(*) FROM extension_identity_registry_declarations
		   WHERE identity_kind = 'permission' AND stable_id = $1),
		  (SELECT count(*) FROM permissions WHERE key = $1),
		  to_regclass(current_schema() || '.extension_permission_catalog') IS NOT NULL
	`, permissionKey).Scan(&ownerRows, &declarationRows, &permissionRows, &catalogExists); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 1 || declarationRows != 0 || permissionRows != 0 || catalogExists {
		t.Fatalf(
			"failed orphan migration changed state owners=%d declarations=%d permissions=%d catalog=%t",
			ownerRows, declarationRows, permissionRows, catalogExists,
		)
	}

	seedIdentityRoleApprovalDeclarationHistory(
		t, ctx, db, "permission", permissionKey, "fixture.identity.orphan@1", "e",
	)
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatalf("reapply after repairing orphan declaration: %v", err)
	}
	var catalogRows int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_permission_catalog WHERE permission_key = $1
	`, permissionKey).Scan(&catalogRows); err != nil {
		t.Fatal(err)
	}
	if catalogRows != 1 {
		t.Fatalf("repaired orphan catalog rows=%d", catalogRows)
	}
}

func TestIdentityRoleApprovalsMigrationFailsClosedOnUntrackedHostPermission(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ('fixture.identity.profile', 'legacy', 'Untracked host permission')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err == nil ||
		!strings.Contains(err.Error(), "untracked Host permission") {
		t.Fatalf("untracked Host permission collision error=%v", err)
	}
}

func TestIdentityRoleApprovalsMigrationConcurrentFenceBlocksOldDecision(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)

	// Hold a non-conflicting open transaction while migration takes ACCESS EXCLUSIVE.
	// The old decision must not commit an inconsistent approved row without grants.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `
		SELECT 1 FROM extension_permission_role_suggestions WHERE id = $1 FOR SHARE
	`, suggestion.ID); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}

	start := make(chan struct{})
	var migrateErr, decideErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, migrateErr = provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true)
	}()
	go func() {
		defer wait.Done()
		<-start
		// Give migration a moment to request ACCESS EXCLUSIVE, then attempt an
		// old-style decision that must wait for the fence and then fail closed
		// under the new trigger (no catalog/grant evidence).
		time.Sleep(150 * time.Millisecond)
		_, decideErr = db.ExecContext(ctx, identityRoleApprovalUpdateSQL,
			suggestion.ID, "approved", managerID, auditID)
	}()
	close(start)
	// Release the share lock so migration can proceed.
	time.Sleep(100 * time.Millisecond)
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	wait.Wait()

	if migrateErr != nil {
		t.Fatalf("fenced migration: %v", migrateErr)
	}
	if decideErr == nil {
		t.Fatal("old decision succeeded across migration fence without grant evidence")
	}
	var state string
	var grants int
	if err := db.QueryRowContext(ctx, `
		SELECT approval_state,
		       (SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1)
		FROM extension_permission_role_suggestions WHERE id = $1
	`, suggestion.ID).Scan(&state, &grants); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || grants != 0 {
		t.Fatalf("post-fence state=%s grants=%d", state, grants)
	}
}

const identityRoleApprovalUpdateSQL = `
	UPDATE extension_permission_role_suggestions
	SET approval_state = $2,
	    revision = revision + 1,
	    decided_by_user_id = $3,
	    decision_audit_event_id = $4
	WHERE id = $1 AND revision = 1 AND approval_state = 'pending'
`

func seedIdentityRoleApprovalBaseTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active'
			  CHECK (status IN ('active', 'disabled', 'banned'))
		);
		CREATE TABLE roles (
			id BIGSERIAL PRIMARY KEY,
			key TEXT NOT NULL UNIQUE,
			is_enabled BOOLEAN NOT NULL DEFAULT TRUE
		);
		CREATE TABLE permissions (
			key TEXT PRIMARY KEY,
			module TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE role_permissions (
			role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
			permission_key TEXT NOT NULL REFERENCES permissions(key) ON DELETE CASCADE,
			PRIMARY KEY (role_id, permission_key)
		);
		CREATE TABLE user_roles (
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
			PRIMARY KEY (user_id, role_id)
		);
		CREATE TABLE user_permission_overrides (
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			permission_key TEXT NOT NULL REFERENCES permissions(key) ON DELETE CASCADE,
			effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
			PRIMARY KEY (user_id, permission_key)
		);
		CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			target_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed'
			  CHECK (status IN ('installed', 'enabled', 'disabled')),
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL,
			package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		INSERT INTO roles (key) VALUES
		  ('super_admin'), ('identity_manager'), ('member'), ('operator'), ('moderator');
		INSERT INTO permissions (key, module, description)
		VALUES ('role.manage', 'identity', 'Manage role permissions');
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'role.manage' FROM roles WHERE key = 'identity_manager';
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES
		  ('manager', 'manager', 'manager@example.test', 'manager@example.test', 'Manager'),
		  ('member', 'member', 'member@example.test', 'member@example.test', 'Member');
		INSERT INTO user_roles (user_id, role_id)
		SELECT users.id, roles.id
		FROM users CROSS JOIN roles
		WHERE users.username = 'manager' AND roles.key = 'identity_manager';
		INSERT INTO extensions (id, type, name, status, active_version_id)
		VALUES ('fixture.identity', 'plugin', 'Identity Fixture', 'enabled', 101);
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES (
			101, 'fixture.identity', '1.0.0', '{}'::jsonb,
			'/tmp/fixture.identity', repeat('a', 64)
		);
	`); err != nil {
		t.Fatal(err)
	}
}

func seedIdentityRoleApprovalDeclaration(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	permissionKey, contractVersion, declarationByte string,
) {
	t.Helper()
	seedIdentityRoleApprovalOwner(t, ctx, db, "permission", permissionKey)
	seedIdentityRoleApprovalDeclarationHistory(
		t, ctx, db, "permission", permissionKey, contractVersion, declarationByte,
	)
}

func seedIdentityRoleApprovalOwner(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	identityKind, stableID string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ($1, $2, 'fixture.identity')
	`, identityKind, stableID); err != nil {
		t.Fatal(err)
	}
}

func seedIdentityRoleApprovalDeclarationHistory(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	identityKind, stableID, contractVersion, declarationByte string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, owner_extension_id, revision, registry_state,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES (
			$1, $2, 'fixture.identity', 1, 'active',
			101, '1.0.0', repeat('a', 64), $3, repeat($4, 64)
		)
	`, identityKind, stableID, contractVersion, declarationByte); err != nil {
		t.Fatal(err)
	}
}

func seedIdentityRoleApprovalCatalog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	permissionKey, contractVersion, declarationByte string,
	revision int64,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ($1, 'extension', '')
		ON CONFLICT (key) DO NOTHING
	`, permissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_catalog (
			permission_key, owner_extension_id, declaration_revision,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES (
			$1, 'fixture.identity', $2,
			101, '1.0.0', repeat('a', 64),
			$3, repeat($4, 64)
		)
		ON CONFLICT (permission_key) DO NOTHING
	`, permissionKey, revision, contractVersion, declarationByte); err != nil {
		t.Fatal(err)
	}
}

func insertIdentityRoleApprovalSuggestion(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	permissionKey, contractVersion, declarationByte, roleKey string,
) identityRoleApprovalSuggestion {
	t.Helper()
	suggestion := identityRoleApprovalSuggestion{
		PermissionKey: permissionKey, OwnerExtensionID: "fixture.identity",
		ExtensionVersionID: 101, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), PermissionContractVersion: contractVersion,
		DeclarationDigest: strings.Repeat(declarationByte, 64), RoleKey: roleKey, Revision: 1,
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_permission_role_suggestions (
			permission_key, owner_extension_id, extension_version_id,
			extension_version, package_digest, permission_contract_version,
			declaration_digest, role_key
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID,
		suggestion.ExtensionVersionID, suggestion.ExtensionVersion,
		suggestion.PackageDigest, suggestion.PermissionContractVersion,
		suggestion.DeclarationDigest, suggestion.RoleKey).Scan(&suggestion.ID); err != nil {
		t.Fatal(err)
	}
	return suggestion
}

func insertIdentityRoleApprovalAudit(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	actorUserID int64,
	suggestion identityRoleApprovalSuggestion,
	state string,
	rolePermissionAdded, roleGrantApplied bool,
) int64 {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"suggestionId": suggestion.ID, "permissionKey": suggestion.PermissionKey,
		"ownerExtensionId":          suggestion.OwnerExtensionID,
		"extensionVersionId":        suggestion.ExtensionVersionID,
		"extensionVersion":          suggestion.ExtensionVersion,
		"packageDigest":             suggestion.PackageDigest,
		"permissionContractVersion": suggestion.PermissionContractVersion,
		"declarationDigest":         suggestion.DeclarationDigest,
		"roleKey":                   suggestion.RoleKey, "expectedRevision": suggestion.Revision,
		"approvalState":               state,
		"permissionCatalogRegistered": false,
		"rolePermissionAdded":         rolePermissionAdded,
		"roleGrantApplied":            roleGrantApplied,
	})
	if err != nil {
		t.Fatal(err)
	}
	action := "identity.role_suggestion." + map[string]string{
		"approved": "approve", "rejected": "reject",
	}[state]
	var auditID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, $2, $3::jsonb)
		RETURNING id
	`, actorUserID, action, string(metadata)).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	return auditID
}

func insertIdentityRoleApprovalApplyAudit(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	actorUserID int64,
	suggestion identityRoleApprovalSuggestion,
	expectedRevision int64,
) int64 {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"suggestionId": suggestion.ID, "permissionKey": suggestion.PermissionKey,
		"ownerExtensionId":          suggestion.OwnerExtensionID,
		"extensionVersionId":        suggestion.ExtensionVersionID,
		"extensionVersion":          suggestion.ExtensionVersion,
		"packageDigest":             suggestion.PackageDigest,
		"permissionContractVersion": suggestion.PermissionContractVersion,
		"declarationDigest":         suggestion.DeclarationDigest,
		"roleKey":                   suggestion.RoleKey, "expectedRevision": expectedRevision,
		"approvalState":               "approved",
		"permissionCatalogRegistered": false,
		"rolePermissionAdded":         true,
		"roleGrantApplied":            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var auditID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'identity.role_suggestion.approve', $2::jsonb)
		RETURNING id
	`, actorUserID, string(metadata)).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	return auditID
}

func identityRoleApprovalUserID(t *testing.T, ctx context.Context, db *sql.DB, username string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
