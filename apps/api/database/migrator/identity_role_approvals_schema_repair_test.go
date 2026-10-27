package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const identityRoleApprovalsSchemaRepairVersion = int64(202607170034)

func requireIdentityRoleApprovalsRepairTestDatabaseURL(t *testing.T) string {
	t.Helper()
	// Never fall back to DATABASE_URL: repair tests must not touch the normal
	// development database that already carries production-like goose history.
	databaseURL := testdb.IsolatedURL(t)
	return databaseURL
}

func TestIdentityRoleApprovalsSchemaRepairFreshCurrentIsNoOp(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
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

	var beforeCatalog, beforeGrants int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_permission_catalog),
		  (SELECT count(*) FROM extension_permission_role_grants)
	`).Scan(&beforeCatalog, &beforeGrants); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("fresh-current repair: %v", err)
	}

	var afterCatalog, afterGrants int
	var hasSuggestionCol, hasDeclarationCol bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_permission_catalog),
		  (SELECT count(*) FROM extension_permission_role_grants),
		  EXISTS (
		    SELECT 1 FROM information_schema.columns
		    WHERE table_schema = current_schema()
		      AND table_name = 'extension_permission_catalog'
		      AND column_name = 'registered_suggestion_id'
		  ),
		  EXISTS (
		    SELECT 1 FROM information_schema.columns
		    WHERE table_schema = current_schema()
		      AND table_name = 'extension_permission_catalog'
		      AND column_name = 'declaration_revision'
		  )
	`).Scan(&afterCatalog, &afterGrants, &hasSuggestionCol, &hasDeclarationCol); err != nil {
		t.Fatal(err)
	}
	if afterCatalog != beforeCatalog || afterGrants != beforeGrants {
		t.Fatalf("fresh-current repair mutated history catalog %d->%d grants %d->%d",
			beforeCatalog, afterCatalog, beforeGrants, afterGrants)
	}
	if hasSuggestionCol || !hasDeclarationCol {
		t.Fatalf("fresh-current shape suggestionCol=%t declarationCol=%t", hasSuggestionCol, hasDeclarationCol)
	}

	// Current store operations: declaration-bound catalog + pending suggestion.
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b")
	seedIdentityRoleApprovalCatalog(t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b", 1)
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b", "member",
	)
	if suggestion.ID <= 0 {
		t.Fatal("pending suggestion insert failed after repair")
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot reverse identity role approval schema repair 202607170034") {
		t.Fatalf("repair Down error=%v", err)
	}
}

func TestIdentityRoleApprovalsSchemaRepairOldEmptyShape(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	seedOldIdentityRoleApprovalDraft029(t, ctx, db)

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("old empty repair: %v", err)
	}
	assertCurrentIdentityRoleApprovalSchema(t, ctx, db)

	// IdentityRegistry Reconcile-shaped inserts: declaration-bound catalog and
	// a pending suggestion without inventing grants.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ('fixture.identity.profile', 'extension', '')
		ON CONFLICT (key) DO NOTHING
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_catalog (
			permission_key, owner_extension_id, declaration_revision,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES (
			'fixture.identity.profile', 'fixture.identity', 1,
			101, '1.0.0', repeat('a', 64),
			'fixture.identity.profile@1', repeat('c', 64)
		)
	`); err != nil {
		t.Fatalf("reconcile catalog insert: %v", err)
	}
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	var grantRows int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1
	`, suggestion.ID).Scan(&grantRows); err != nil {
		t.Fatal(err)
	}
	if grantRows != 0 {
		t.Fatalf("reconcile synthesized grants=%d", grantRows)
	}
}

func TestIdentityRoleApprovalsSchemaRepairBackfillsExactCatalogEvidence(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	seedOldIdentityRoleApprovalDraft029(t, ctx, db)

	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ($1, 'extension', '')
	`, suggestion.PermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_catalog (
			permission_key, owner_extension_id,
			registered_suggestion_id, registered_by_user_id, registered_audit_event_id
		) VALUES ($1, $2, $3, $4, $5)
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID, suggestion.ID, managerID, auditID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("exact evidence repair: %v", err)
	}

	var revision, versionID int64
	var version, packageDigest, contract, declarationDigest string
	var suggestionID, registeredBy, registeredAudit sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT declaration_revision, extension_version_id, extension_version,
		       package_digest, contract_version, declaration_digest,
		       registered_suggestion_id, registered_by_user_id, registered_audit_event_id
		FROM extension_permission_catalog
		WHERE permission_key = $1
	`, suggestion.PermissionKey).Scan(
		&revision, &versionID, &version, &packageDigest, &contract, &declarationDigest,
		&suggestionID, &registeredBy, &registeredAudit,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || versionID != 101 || version != "1.0.0" ||
		packageDigest != strings.Repeat("a", 64) ||
		contract != "fixture.identity.profile@1" ||
		declarationDigest != strings.Repeat("c", 64) {
		t.Fatalf("backfill mismatch rev=%d versionID=%d version=%q digest=%q contract=%q declaration=%q",
			revision, versionID, version, packageDigest, contract, declarationDigest)
	}
	if !suggestionID.Valid || suggestionID.Int64 != suggestion.ID ||
		!registeredBy.Valid || registeredBy.Int64 != managerID ||
		!registeredAudit.Valid || registeredAudit.Int64 != auditID {
		t.Fatalf("legacy evidence not retained suggestion=%v by=%v audit=%v",
			suggestionID, registeredBy, registeredAudit)
	}

	// Legacy approved row without grant remains review-only.
	var grantRows int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1
	`, suggestion.ID).Scan(&grantRows); err != nil {
		t.Fatal(err)
	}
	if grantRows != 0 {
		t.Fatalf("repair synthesized grants for review-only approval: %d", grantRows)
	}

	// Current declaration-bound insert works without a suggestion.
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b")
	seedIdentityRoleApprovalCatalog(t, ctx, db, "fixture.identity.zero", "fixture.identity.zero@1", "b", 1)

	// A development database may already have the repaired shape plus nullable
	// draft evidence columns. Replaying the final source must validate that shape
	// without rewriting its retained history.
	if _, err := db.ExecContext(ctx, `
		DELETE FROM goose_db_version WHERE version_id = $1
	`, identityRoleApprovalsSchemaRepairVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("replay repaired legacy-compatible shape: %v", err)
	}
	assertCurrentIdentityRoleApprovalSchema(t, ctx, db)
}

func TestIdentityRoleApprovalsSchemaRepairAbortsWithoutExactEvidence(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()

	t.Run("missing declaration match", func(t *testing.T) {
		db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
		seedIdentityRoleApprovalBaseTables(t, ctx, db)
		if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
			t.Fatal(err)
		}
		seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
		seedOldIdentityRoleApprovalDraft029(t, ctx, db)

		managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
		// Suggestion artifact does not match any declaration (different package digest).
		suggestion := identityRoleApprovalSuggestion{
			PermissionKey: "fixture.identity.profile", OwnerExtensionID: "fixture.identity",
			ExtensionVersionID: 101, ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("f", 64), PermissionContractVersion: "fixture.identity.profile@1",
			DeclarationDigest: strings.Repeat("c", 64), RoleKey: "member", Revision: 1,
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
			// Insert validation may reject mismatched artifact; seed bypass for repair proof.
			if _, err := db.ExecContext(ctx, `
				DROP TRIGGER IF EXISTS extension_permission_role_suggestion_insert_valid
				  ON extension_permission_role_suggestions
			`); err != nil {
				t.Fatal(err)
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
		}
		auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO permissions (key, module, description)
			VALUES ($1, 'extension', '')
		`, suggestion.PermissionKey); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO extension_permission_catalog (
				permission_key, owner_extension_id,
				registered_suggestion_id, registered_by_user_id, registered_audit_event_id
			) VALUES ($1, $2, $3, $4, $5)
		`, suggestion.PermissionKey, suggestion.OwnerExtensionID, suggestion.ID, managerID, auditID); err != nil {
			t.Fatal(err)
		}

		if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err == nil ||
			!strings.Contains(err.Error(), "without exact suggestion/declaration evidence") {
			t.Fatalf("missing evidence repair error=%v", err)
		}
		assertOldIdentityRoleApprovalDraftStillPresent(t, ctx, db)
	})

	t.Run("partial grants schema", func(t *testing.T) {
		db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
		seedIdentityRoleApprovalBaseTables(t, ctx, db)
		if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
			t.Fatal(err)
		}
		seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
		seedOldIdentityRoleApprovalDraft029(t, ctx, db)

		if _, err := db.ExecContext(ctx, `
			CREATE TABLE extension_permission_role_grants (
				suggestion_id BIGINT PRIMARY KEY
				  REFERENCES extension_permission_role_suggestions(id) ON DELETE RESTRICT
			)
		`); err != nil {
			t.Fatal(err)
		}

		if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err == nil ||
			!strings.Contains(err.Error(), "extension_permission_role_grants schema is incompatible") {
			t.Fatalf("partial grants repair error=%v", err)
		}
		var hasDeclaration bool
		var grantsColumns int
		if err := db.QueryRowContext(ctx, `
			SELECT
			  EXISTS (
			    SELECT 1 FROM information_schema.columns
			    WHERE table_schema = current_schema()
			      AND table_name = 'extension_permission_catalog'
			      AND column_name = 'declaration_revision'
			  ),
			  (
			    SELECT count(*) FROM information_schema.columns
			    WHERE table_schema = current_schema()
			      AND table_name = 'extension_permission_role_grants'
			  )
		`).Scan(&hasDeclaration, &grantsColumns); err != nil {
			t.Fatal(err)
		}
		if hasDeclaration || grantsColumns != 1 {
			t.Fatalf("failed repair left partial state declaration=%t grantsColumns=%d", hasDeclaration, grantsColumns)
		}
	})
}

func TestIdentityRoleApprovalsSchemaRepairUsesSuggestionTimeDeclarationTip(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)

	// Later tombstone/reactivation of the same exact declaration is legitimate
	// history. It must not replace or make ambiguous the tip seen at suggestion
	// creation time.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, owner_extension_id, revision, registry_state,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest, created_at
		) VALUES
			('permission', 'fixture.identity.profile', 'fixture.identity', 2, 'tombstone',
			 101, '1.0.0', repeat('a', 64), 'fixture.identity.profile@1', repeat('c', 64),
			 statement_timestamp() + interval '1 hour'),
			('permission', 'fixture.identity.profile', 'fixture.identity', 3, 'active',
			 101, '1.0.0', repeat('a', 64), 'fixture.identity.profile@1', repeat('c', 64),
			 statement_timestamp() + interval '2 hours')
	`); err != nil {
		t.Fatal(err)
	}
	seedOldIdentityRoleApprovalDraft029(t, ctx, db)
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ($1, 'extension', '')
	`, suggestion.PermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_catalog (
			permission_key, owner_extension_id,
			registered_suggestion_id, registered_by_user_id, registered_audit_event_id
		) VALUES ($1, $2, $3, $4, $5)
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID, suggestion.ID, managerID, auditID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("suggestion-time repair: %v", err)
	}
	var declarationRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT declaration_revision
		FROM extension_permission_catalog
		WHERE permission_key = $1
	`, suggestion.PermissionKey).Scan(&declarationRevision); err != nil {
		t.Fatal(err)
	}
	if declarationRevision != 1 {
		t.Fatalf("catalog bound revision=%d, want suggestion-time revision 1", declarationRevision)
	}
}

func TestIdentityRoleApprovalsSchemaRepairUsesReactivatedTipBeforeSuggestion(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, owner_extension_id, revision, registry_state,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES
			('permission', 'fixture.identity.profile', 'fixture.identity', 2, 'tombstone',
			 101, '1.0.0', repeat('a', 64), 'fixture.identity.profile@1', repeat('c', 64)),
			('permission', 'fixture.identity.profile', 'fixture.identity', 3, 'active',
			 101, '1.0.0', repeat('a', 64), 'fixture.identity.profile@1', repeat('c', 64))
	`); err != nil {
		t.Fatal(err)
	}
	suggestion := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
	seedOldIdentityRoleApprovalDraft029(t, ctx, db)
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	auditID := insertIdentityRoleApprovalAudit(t, ctx, db, managerID, suggestion, "approved", false, false)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ($1, 'extension', '')
	`, suggestion.PermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_catalog (
			permission_key, owner_extension_id,
			registered_suggestion_id, registered_by_user_id, registered_audit_event_id
		) VALUES ($1, $2, $3, $4, $5)
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID, suggestion.ID, managerID, auditID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("reactivated suggestion-time repair: %v", err)
	}
	var declarationRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT declaration_revision
		FROM extension_permission_catalog
		WHERE permission_key = $1
	`, suggestion.PermissionKey).Scan(&declarationRevision); err != nil {
		t.Fatal(err)
	}
	if declarationRevision != 3 {
		t.Fatalf("catalog bound revision=%d, want reactivated suggestion-time revision 3", declarationRevision)
	}
}

func TestIdentityRoleApprovalsSchemaRepairRejectsDriftedGrantEvidenceSchema(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	cases := []struct {
		name string
		sql  string
	}{
		{name: "extra column", sql: `ALTER TABLE extension_permission_role_grants ADD COLUMN foreign_note TEXT`},
		{name: "extra unique authority", sql: `
			ALTER TABLE extension_permission_role_grants
			ADD CONSTRAINT extension_permission_role_grants_foreign_unique
			UNIQUE (owner_extension_id, role_key)
		`},
		{name: "wrong same-name index", sql: `
			DROP INDEX extension_permission_role_grants_mapping_idx;
			CREATE INDEX extension_permission_role_grants_mapping_idx
			ON extension_permission_role_grants (permission_key, role_id)
		`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
			seedIdentityRoleApprovalBaseTables(t, ctx, db)
			if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err == nil ||
				!strings.Contains(err.Error(), "extension_permission_role_grants schema is incompatible") {
				t.Fatalf("drifted grants repair error=%v", err)
			}
			var applied bool
			if err := db.QueryRowContext(ctx, `
				SELECT EXISTS (
				  SELECT 1 FROM goose_db_version
				  WHERE version_id = $1 AND is_applied = TRUE
				)
			`, identityRoleApprovalsSchemaRepairVersion).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied {
				t.Fatal("drifted grants schema marked repair migration applied")
			}
		})
	}
}

func TestIdentityRoleApprovalsSchemaRepairPreservesHistoryAndIsIdempotent(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalDeclaration(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c")
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}
	seedIdentityRoleApprovalCatalog(t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", 1)
	managerID := identityRoleApprovalUserID(t, ctx, db, "manager")
	approved := insertIdentityRoleApprovalSuggestion(
		t, ctx, db, "fixture.identity.profile", "fixture.identity.profile@1", "c", "member",
	)
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
		t.Fatal(err)
	}

	var beforeCatalog, beforeGrants, beforeSuggestions int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_permission_catalog),
		  (SELECT count(*) FROM extension_permission_role_grants),
		  (SELECT count(*) FROM extension_permission_role_suggestions WHERE approval_state = 'approved')
	`).Scan(&beforeCatalog, &beforeGrants, &beforeSuggestions); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("first repair: %v", err)
	}

	// Force a second Up by clearing the goose marker; objects must stay intact.
	if _, err := db.ExecContext(ctx, `
		DELETE FROM goose_db_version WHERE version_id = $1
	`, identityRoleApprovalsSchemaRepairVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true); err != nil {
		t.Fatalf("idempotent second repair: %v", err)
	}

	var afterCatalog, afterGrants, afterSuggestions int
	var grantsExist, catalogExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_permission_catalog),
		  (SELECT count(*) FROM extension_permission_role_grants),
		  (SELECT count(*) FROM extension_permission_role_suggestions WHERE approval_state = 'approved'),
		  to_regclass(current_schema() || '.extension_permission_catalog') IS NOT NULL,
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL
	`).Scan(&afterCatalog, &afterGrants, &afterSuggestions, &catalogExists, &grantsExist); err != nil {
		t.Fatal(err)
	}
	if afterCatalog != beforeCatalog || afterGrants != beforeGrants || afterSuggestions != beforeSuggestions {
		t.Fatalf("history drift catalog %d->%d grants %d->%d approved %d->%d",
			beforeCatalog, afterCatalog, beforeGrants, afterGrants, beforeSuggestions, afterSuggestions)
	}
	if !catalogExists || !grantsExist {
		t.Fatal("idempotent repair dropped authority tables")
	}
	assertCurrentIdentityRoleApprovalSchema(t, ctx, db)
}

func TestIdentityRoleApprovalsSchemaRepairRetriesRollingPublicationFence(t *testing.T) {
	databaseURL := requireIdentityRoleApprovalsRepairTestDatabaseURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRoleApprovalBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRoleApprovalsVersion, true); err != nil {
		t.Fatal(err)
	}

	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "sforum_lease_") {
		t.Fatalf("unexpected isolated schema %q", schema)
	}

	blockerDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer blockerDB.Close()
	blockerConn, err := blockerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blockerConn.Close()
	if _, err := blockerConn.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
		t.Fatal(err)
	}
	blockerTx, err := blockerConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blockerTx.Rollback()
	if _, err := blockerTx.ExecContext(ctx, `
		LOCK TABLE extension_permission_role_suggestions IN ACCESS EXCLUSIVE MODE
	`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, applyErr := provider.ApplyVersion(ctx, identityRoleApprovalsSchemaRepairVersion, true)
		done <- applyErr
	}()

	observed := false
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for !observed {
		select {
		case applyErr := <-done:
			t.Fatalf("repair exited before blocker release: %v", applyErr)
		case <-ticker.C:
			observed = db.Stats().InUse == 1
		case <-deadline.C:
			t.Fatal("repair did not enter the rolling publication retry fence")
		}
	}
	select {
	case applyErr := <-done:
		t.Fatalf("repair exited while publication lock remained held: %v", applyErr)
	case <-time.After(100 * time.Millisecond):
	}

	probeConn, err := blockerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probeConn.Close()
	if _, err := probeConn.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
		t.Fatal(err)
	}
	probeDeadline := time.NewTimer(3 * time.Second)
	defer probeDeadline.Stop()
	var lastProbeErr error
	probeAcquired := false
	for !probeAcquired {
		probeTx, beginErr := probeConn.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		_, lastProbeErr = probeTx.ExecContext(ctx, `
			LOCK TABLE extensions IN ACCESS EXCLUSIVE MODE NOWAIT
		`)
		_ = probeTx.Rollback()
		if lastProbeErr == nil {
			probeAcquired = true
			break
		}
		select {
		case <-ticker.C:
		case <-probeDeadline.C:
			t.Fatalf("repair retained a partial lock set between retries: %v", lastProbeErr)
		}
	}

	if err := blockerTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case applyErr := <-done:
		if applyErr != nil {
			t.Fatalf("repair after blocker release: %v", applyErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not finish after blocker release")
	}
	assertCurrentIdentityRoleApprovalSchema(t, ctx, db)
}

func seedOldIdentityRoleApprovalDraft029(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	// Pre-commit draft catalog: suggestion-bound, no declaration columns, no grants.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extension_permission_catalog (
			identity_kind TEXT NOT NULL DEFAULT 'permission'
				CHECK (identity_kind = 'permission'),
			permission_key TEXT PRIMARY KEY
				REFERENCES permissions(key) ON DELETE RESTRICT,
			owner_extension_id TEXT NOT NULL,
			registered_suggestion_id BIGINT NOT NULL
				REFERENCES extension_permission_role_suggestions(id) ON DELETE RESTRICT
				UNIQUE,
			registered_by_user_id BIGINT NOT NULL CHECK (registered_by_user_id > 0),
			registered_audit_event_id BIGINT NOT NULL
				REFERENCES audit_events(id) ON DELETE RESTRICT,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
			FOREIGN KEY (identity_kind, permission_key, owner_extension_id)
				REFERENCES extension_identity_registry_owners(
					identity_kind, stable_id, owner_extension_id
				) ON DELETE RESTRICT
		);
		CREATE INDEX extension_permission_catalog_owner_idx
			ON extension_permission_catalog (owner_extension_id, permission_key);

		CREATE FUNCTION validate_extension_permission_catalog() RETURNS trigger
		LANGUAGE plpgsql
		AS $fn$
		DECLARE
			suggestion extension_permission_role_suggestions%ROWTYPE;
			event audit_events%ROWTYPE;
		BEGIN
			SELECT * INTO suggestion
			FROM extension_permission_role_suggestions
			WHERE id = NEW.registered_suggestion_id
			FOR KEY SHARE;
			IF suggestion.id IS NULL
				OR suggestion.permission_key IS DISTINCT FROM NEW.permission_key
				OR suggestion.owner_extension_id IS DISTINCT FROM NEW.owner_extension_id
				OR suggestion.approval_state NOT IN ('pending', 'approved') THEN
				RAISE EXCEPTION 'extension permission catalog suggestion is invalid';
			END IF;
			SELECT * INTO event
			FROM audit_events
			WHERE id = NEW.registered_audit_event_id
			FOR KEY SHARE;
			IF event.id IS NULL
				OR event.actor_user_id IS DISTINCT FROM NEW.registered_by_user_id
				OR event.action IS DISTINCT FROM 'identity.role_suggestion.approve' THEN
				RAISE EXCEPTION 'extension permission catalog audit evidence is invalid';
			END IF;
			RETURN NEW;
		END;
		$fn$;

		CREATE TRIGGER extension_permission_catalog_valid
		BEFORE INSERT ON extension_permission_catalog
		FOR EACH ROW EXECUTE FUNCTION validate_extension_permission_catalog();
		CREATE TRIGGER extension_permission_catalog_immutable
		BEFORE UPDATE OR DELETE ON extension_permission_catalog
		FOR EACH ROW EXECUTE FUNCTION reject_extension_identity_registry_history_mutation();
		CREATE TRIGGER extension_permission_catalog_no_truncate
		BEFORE TRUNCATE ON extension_permission_catalog
		FOR EACH STATEMENT EXECUTE FUNCTION reject_extension_identity_registry_history_mutation();

		CREATE FUNCTION extension_identity_actor_can_manage_roles(candidate_user_id BIGINT)
		RETURNS BOOLEAN
		LANGUAGE sql
		STABLE
		AS $fn$
			SELECT EXISTS (
				SELECT 1 FROM users AS actor
				WHERE actor.id = candidate_user_id AND actor.status = 'active'
			);
		$fn$;

		CREATE FUNCTION reject_extension_identity_decision_audit_mutation() RETURNS trigger
		LANGUAGE plpgsql
		AS $fn$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM extension_permission_role_suggestions
				WHERE decision_audit_event_id = OLD.id
			) OR EXISTS (
				SELECT 1 FROM extension_permission_catalog
				WHERE registered_audit_event_id = OLD.id
			) THEN
				RAISE EXCEPTION 'identity role decision audit evidence is immutable';
			END IF;
			RETURN COALESCE(NEW, OLD);
		END;
		$fn$;
		CREATE TRIGGER extension_identity_decision_audit_immutable
		BEFORE UPDATE OR DELETE ON audit_events
		FOR EACH ROW EXECUTE FUNCTION reject_extension_identity_decision_audit_mutation();

		ALTER TABLE extension_permission_role_suggestions
			ADD CONSTRAINT extension_permission_role_suggestions_decision_audit_fk
			FOREIGN KEY (decision_audit_event_id)
			REFERENCES audit_events(id) ON DELETE RESTRICT;

		DROP TRIGGER IF EXISTS extension_permission_role_suggestion_update_valid
			ON extension_permission_role_suggestions;
		CREATE FUNCTION validate_extension_permission_role_suggestion_decision() RETURNS trigger
		LANGUAGE plpgsql
		AS $fn$
		BEGIN
			IF OLD.approval_state <> 'pending'
				OR NEW.approval_state NOT IN ('approved', 'rejected')
				OR NEW.revision <> OLD.revision + 1 THEN
				RAISE EXCEPTION 'permission role suggestion decision requires Host CAS evidence';
			END IF;
			NEW.decided_at := statement_timestamp();
			NEW.updated_at := statement_timestamp();
			RETURN NEW;
		END;
		$fn$;
		CREATE TRIGGER extension_permission_role_suggestion_update_valid
		BEFORE UPDATE ON extension_permission_role_suggestions
		FOR EACH ROW EXECUTE FUNCTION validate_extension_permission_role_suggestion_decision();
	`); err != nil {
		t.Fatal(err)
	}

	// Mark draft 029 applied so Goose accepts the additive repair version next.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied)
		VALUES ($1, TRUE)
	`, identityRoleApprovalsVersion); err != nil {
		t.Fatalf("mark draft 029 applied: %v", err)
	}
}

func assertCurrentIdentityRoleApprovalSchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var hasDeclaration, hasGrants, hasSuggestionCol bool
	var grantTrigger, catalogTrigger int
	if err := db.QueryRowContext(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1 FROM information_schema.columns
		    WHERE table_schema = current_schema()
		      AND table_name = 'extension_permission_catalog'
		      AND column_name = 'declaration_revision'
		      AND is_nullable = 'NO'
		  ),
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL,
		  EXISTS (
		    SELECT 1 FROM information_schema.columns
		    WHERE table_schema = current_schema()
		      AND table_name = 'extension_permission_catalog'
		      AND column_name = 'registered_suggestion_id'
		  ),
		  (
		    SELECT count(*) FROM pg_trigger
		    WHERE tgrelid = format('%I.extension_permission_role_grants', current_schema())::regclass
		      AND NOT tgisinternal
		      AND tgname IN (
		        'extension_permission_role_grant_valid',
		        'extension_permission_role_grant_immutable',
		        'extension_permission_role_grant_no_truncate'
		      )
		  ),
		  (
		    SELECT count(*) FROM pg_trigger
		    WHERE tgrelid = format('%I.extension_permission_catalog', current_schema())::regclass
		      AND NOT tgisinternal
		      AND tgname IN (
		        'extension_permission_catalog_valid',
		        'extension_permission_catalog_immutable',
		        'extension_permission_catalog_no_truncate'
		      )
		  )
	`).Scan(&hasDeclaration, &hasGrants, &hasSuggestionCol, &grantTrigger, &catalogTrigger); err != nil {
		t.Fatal(err)
	}
	if !hasDeclaration || !hasGrants || grantTrigger != 3 || catalogTrigger != 3 {
		t.Fatalf(
			"current schema incomplete declaration=%t grants=%t grantTriggers=%d catalogTriggers=%d",
			hasDeclaration, hasGrants, grantTrigger, catalogTrigger,
		)
	}
	// Draft columns may remain as nullable compatibility evidence after repair.
	_ = hasSuggestionCol
}

func assertOldIdentityRoleApprovalDraftStillPresent(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var hasDeclaration, hasGrants bool
	if err := db.QueryRowContext(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1 FROM information_schema.columns
		    WHERE table_schema = current_schema()
		      AND table_name = 'extension_permission_catalog'
		      AND column_name = 'declaration_revision'
		  ),
		  to_regclass(current_schema() || '.extension_permission_role_grants') IS NOT NULL
	`).Scan(&hasDeclaration, &hasGrants); err != nil {
		t.Fatal(err)
	}
	if hasDeclaration || hasGrants {
		t.Fatalf("failed repair left partial schema declaration=%t grants=%t", hasDeclaration, hasGrants)
	}
}
