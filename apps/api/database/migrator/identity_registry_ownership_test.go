package migrator

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const identityRegistryOwnershipVersion = int64(202607160028)

func TestIdentityRegistryOwnershipMigrationExactCASAndHostApproval(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRegistryOwnershipBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatalf("apply identity registry ownership migration: %v", err)
	}
	assertIdentityRegistryOwnershipSchema(t, ctx, db, true)
	seedIdentityRegistryOwnershipFixtures(t, ctx, db)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'fixture.theme.permission', 'fixture.theme')
	`); err == nil || !strings.Contains(err.Error(), "must be an installed plugin") {
		t.Fatalf("theme owner insert error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'core.spoof.permission', 'core.spoof')
	`); err == nil {
		t.Fatal("third-party core namespace owner was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'core.permission', 'core')
	`); err == nil {
		t.Fatal("bare core extension id captured the Host namespace")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'foreign.permission', 'fixture.identity')
	`); err == nil {
		t.Fatal("foreign namespace owner was accepted")
	}

	for _, value := range []struct{ kind, id string }{
		{kind: "permission", id: "fixture.identity.profile"},
		{kind: "user_field", id: "fixture.identity.field.code"},
		{kind: "provider", id: "fixture.identity.provider.risk"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO extension_identity_registry_owners (
				identity_kind, stable_id, owner_extension_id
			) VALUES ($1, $2, 'fixture.identity')
		`, value.kind, value.id); err != nil {
			t.Fatalf("insert %s owner: %v", value.kind, err)
		}
	}
	assertIdentityRegistryOwnerTypeRace(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_identity_registry_owners SET owner_extension_id = 'other.plugin'
		WHERE identity_kind = 'permission' AND stable_id = 'fixture.identity.profile'
	`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("owner mutation error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'fixture.identity'
	`); err == nil || !strings.Contains(err.Error(), "type is immutable") {
		t.Fatalf("owner type mutation error=%v", err)
	}

	if _, err := db.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
		"permission", "fixture.identity.profile", int64(1), "active",
		int64(102), "1.0.0", strings.Repeat("b", 64),
		"fixture.identity.profile@1", strings.Repeat("c", 64)); err == nil {
		t.Fatal("declaration accepted a mismatched exact artifact")
	}
	if _, err := db.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
		"permission", "fixture.identity.profile", int64(1), "tombstone",
		int64(101), "1.0.0", strings.Repeat("a", 64),
		"fixture.identity.profile@1", strings.Repeat("c", 64)); err == nil {
		t.Fatal("declaration history began with a tombstone")
	}

	insertIdentityRegistryDeclaration(t, ctx, db,
		"permission", "fixture.identity.profile", 1, "active", 101, "1.0.0", "a",
		"fixture.identity.profile@1", "c")
	insertIdentityRegistryDeclaration(t, ctx, db,
		"user_field", "fixture.identity.field.code", 1, "active", 101, "1.0.0", "a",
		"fixture.identity.field.code@1", "d")
	insertIdentityRegistryDeclaration(t, ctx, db,
		"provider", "fixture.identity.provider.risk", 1, "active", 101, "1.0.0", "a",
		"fixture.identity.provider.risk@1", "e")
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_versions WHERE id = 101`); err == nil ||
		!strings.Contains(err.Error(), "cannot be removed before tombstone") {
		t.Fatalf("active identity version delete error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extensions WHERE id = 'fixture.identity'`); err == nil ||
		!strings.Contains(err.Error(), "must be tombstoned before uninstall") {
		t.Fatalf("active identity extension delete error=%v", err)
	}

	var actorID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ('identity-reviewer', 'identity-reviewer',
		        'identity-reviewer@example.test', 'identity-reviewer@example.test',
		        'Identity Reviewer')
		RETURNING id
	`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, identityRoleSuggestionInsertSQL, "super_admin"); err == nil {
		t.Fatal("super_admin role suggestion was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_permission_role_suggestions (
			permission_key, owner_extension_id, extension_version_id,
			extension_version, package_digest, permission_contract_version,
			declaration_digest, role_key, approval_state, revision,
			decided_by_user_id, decision_audit_event_id, decided_at
		) VALUES (
			'fixture.identity.profile', 'fixture.identity', 101,
			'1.0.0', repeat('a', 64), 'fixture.identity.profile@1',
			repeat('c', 64), 'member', 'approved', 2, $1, 7001,
			statement_timestamp()
		)
	`, actorID); err == nil {
		t.Fatal("role suggestion skipped pending state")
	}

	var approvedSuggestionID int64
	if err := db.QueryRowContext(ctx, identityRoleSuggestionInsertSQL+" RETURNING id", "member").Scan(&approvedSuggestionID); err != nil {
		t.Fatal(err)
	}
	var staleSuggestionID int64
	if err := db.QueryRowContext(ctx, identityRoleSuggestionInsertSQL+" RETURNING id", "operator").Scan(&staleSuggestionID); err != nil {
		t.Fatal(err)
	}
	var futureRoleSuggestionID int64
	if err := db.QueryRowContext(ctx, identityRoleSuggestionInsertSQL+" RETURNING id", "future_role").Scan(&futureRoleSuggestionID); err != nil {
		t.Fatalf("store descriptive pending role: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'approved', revision = revision + 1,
		    decided_by_user_id = $2, decision_audit_event_id = 7002
		WHERE id = $1 AND revision = 1
	`, futureRoleSuggestionID, actorID); err == nil || !strings.Contains(err.Error(), "target is unavailable") {
		t.Fatalf("unknown approval target error=%v", err)
	}
	var futureState string
	if err := db.QueryRowContext(ctx, `
		SELECT approval_state FROM extension_permission_role_suggestions WHERE id = $1
	`, futureRoleSuggestionID).Scan(&futureState); err != nil {
		t.Fatal(err)
	}
	if futureState != "pending" {
		t.Fatalf("unknown role state=%q, want descriptive pending", futureState)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'approved', decided_by_user_id = $2,
		    decision_audit_event_id = 7002
		WHERE id = $1
	`, approvedSuggestionID, actorID); err == nil || !strings.Contains(err.Error(), "Host CAS evidence") {
		t.Fatalf("approval without revision CAS error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'approved', revision = revision + 1,
		    decided_by_user_id = $2, decision_audit_event_id = 7003
		WHERE id = $1 AND revision = 1
	`, approvedSuggestionID, actorID); err != nil {
		t.Fatalf("approve exact pending suggestion: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'rejected', revision = revision + 1,
		    decided_by_user_id = $2, decision_audit_event_id = 7004
		WHERE id = $1
	`, approvedSuggestionID, actorID); err == nil {
		t.Fatal("terminal role suggestion decision was mutable")
	}
	assertIdentityRegistrySuggestionConcurrentCAS(t, ctx, db, actorID)

	var implicitGrantCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM role_permissions
		WHERE permission_key = 'fixture.identity.profile'
	`).Scan(&implicitGrantCount); err != nil {
		t.Fatal(err)
	}
	if implicitGrantCount != 0 {
		t.Fatalf("role suggestion created %d implicit grants", implicitGrantCount)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ('fixture.identity.profile', 'extension', 'Host-approved fixture permission')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'fixture.identity.profile' FROM roles WHERE key = 'member'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET status = 'disabled' WHERE id = 'fixture.identity'
	`); err != nil {
		t.Fatal(err)
	}
	insertIdentityRegistryDeclaration(t, ctx, db,
		"permission", "fixture.identity.profile", 2, "tombstone", 101, "1.0.0", "a",
		"fixture.identity.profile@1", "c")
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM role_permissions
		WHERE permission_key = 'fixture.identity.profile'
	`).Scan(&implicitGrantCount); err != nil {
		t.Fatal(err)
	}
	if implicitGrantCount != 1 {
		t.Fatalf("disable removed Host grant; count=%d", implicitGrantCount)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'rejected', revision = revision + 1,
		    decided_by_user_id = $2, decision_audit_event_id = 7005
		WHERE id = $1 AND revision = 1
	`, staleSuggestionID, actorID); err == nil || !strings.Contains(err.Error(), "decision is stale") {
		t.Fatalf("disabled declaration suggestion decision error=%v", err)
	}

	assertIdentityRegistryConcurrentCAS(t, ctx, db)
	insertIdentityRegistryDeclaration(t, ctx, db,
		"user_field", "fixture.identity.field.code", 2, "tombstone", 101, "1.0.0", "a",
		"fixture.identity.field.code@1", "d")
	if _, err := db.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
		"user_field", "fixture.identity.field.code", int64(3), "active",
		int64(101), "1.0.0", strings.Repeat("a", 64),
		"fixture.identity.field.code@1", strings.Repeat("f", 64)); err == nil ||
		!strings.Contains(err.Error(), "cannot drift on reactivation") {
		t.Fatalf("same-artifact declaration drift on reactivation error=%v", err)
	}
	insertIdentityRegistryDeclaration(t, ctx, db,
		"user_field", "fixture.identity.field.code", 3, "active", 101, "1.0.0", "a",
		"fixture.identity.field.code@1", "d")
	insertIdentityRegistryDeclaration(t, ctx, db,
		"user_field", "fixture.identity.field.code", 4, "tombstone", 101, "1.0.0", "a",
		"fixture.identity.field.code@1", "d")
	if _, err := db.ExecContext(ctx, `DELETE FROM extensions WHERE id = 'fixture.identity'`); err != nil {
		t.Fatalf("uninstall after exact identity tombstones: %v", err)
	}
	var retainedFixtureOwners int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_identity_registry_owners
		WHERE owner_extension_id = 'fixture.identity'
	`).Scan(&retainedFixtureOwners); err != nil {
		t.Fatal(err)
	}
	if retainedFixtureOwners != 3 {
		t.Fatalf("retained fixture identity owners=%d", retainedFixtureOwners)
	}
	assertIdentityRegistryNestedOwnership(t, ctx, db)

	if _, err := db.ExecContext(ctx, `
		DELETE FROM extension_identity_registry_declarations
		WHERE identity_kind = 'user_field' AND stable_id = 'fixture.identity.field.code'
	`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("declaration delete error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM extension_permission_role_suggestions WHERE id = $1
	`, approvedSuggestionID); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("suggestion delete error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE extension_identity_registry_declarations`); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("declaration truncate error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE extension_permission_role_suggestions`); err == nil ||
		!strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("suggestion truncate error=%v", err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove extension identity registry ownership history") {
		t.Fatalf("history-protected Down error=%v", err)
	}
	assertIdentityRegistryOwnershipSchema(t, ctx, db, true)
}

func TestIdentityRegistryOwnershipMigrationEmptyDownAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRegistryOwnershipBaseTables(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, false); err != nil {
		t.Fatalf("rollback empty identity registry ownership migration: %v", err)
	}
	assertIdentityRegistryOwnershipSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatalf("reapply identity registry ownership migration: %v", err)
	}
	assertIdentityRegistryOwnershipSchema(t, ctx, db, true)
}

func TestIdentityRegistryOwnershipMigrationDownSeesConcurrentHistory(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedIdentityRegistryOwnershipBaseTables(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ('down.race', 'plugin', 'Down Race', 'enabled')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, true); err != nil {
		t.Fatal(err)
	}

	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	prepared := make([]*sql.Conn, 4)
	for index := range prepared {
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		prepared[index] = connection
		if _, err := connection.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
			t.Fatal(err)
		}
	}
	for _, connection := range prepared {
		_ = connection.Close()
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'down.race.permission', 'down.race')
	`); err != nil {
		t.Fatal(err)
	}

	downResult := make(chan error, 1)
	go func() {
		_, err := provider.ApplyVersion(ctx, identityRegistryOwnershipVersion, false)
		downResult <- err
	}()
	waitForIdentityRegistryDownLock(t, ctx, db)
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-downResult:
		if err == nil || !strings.Contains(err.Error(), "cannot remove extension identity registry ownership history") {
			t.Fatalf("concurrent-history Down error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("identity registry Down did not finish after concurrent writer committed")
	}
	assertIdentityRegistryOwnershipSchema(t, ctx, db, true)
}

const identityRegistryDeclarationInsertSQL = `
	INSERT INTO extension_identity_registry_declarations (
		identity_kind, stable_id, owner_extension_id, revision, registry_state,
		extension_version_id, extension_version, package_digest,
		contract_version, declaration_digest
	) VALUES ($1, $2, 'fixture.identity', $3, $4, $5, $6, $7, $8, $9)
`

const identityRoleSuggestionInsertSQL = `
	INSERT INTO extension_permission_role_suggestions (
		permission_key, owner_extension_id, extension_version_id,
		extension_version, package_digest, permission_contract_version,
		declaration_digest, role_key
	) VALUES (
		'fixture.identity.profile', 'fixture.identity', 101,
		'1.0.0', repeat('a', 64), 'fixture.identity.profile@1',
		repeat('c', 64), $1
	)
`

func insertIdentityRegistryDeclaration(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	kind, stableID string,
	revision int64,
	state string,
	versionID int64,
	version, digestByte, contractVersion, declarationByte string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
		kind, stableID, revision, state, versionID, version,
		strings.Repeat(digestByte, 64), contractVersion, strings.Repeat(declarationByte, 64)); err != nil {
		t.Fatalf("insert %s %s revision %d: %v", kind, stableID, revision, err)
	}
}

func assertIdentityRegistryConcurrentCAS(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	connections, closeConnections := identityRegistryConcurrentConnections(t, ctx, db)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		connection := connections[index]
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := connection.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
				"provider", "fixture.identity.provider.risk", int64(2), "active",
				int64(102), "2.0.0", strings.Repeat("b", 64),
				"fixture.identity.provider.risk@1", strings.Repeat("f", 64))
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	closeConnections()
	close(results)
	var succeeded, conflicted int
	for err := range results {
		if err == nil {
			succeeded++
		} else if strings.Contains(err.Error(), "revision conflict") || strings.Contains(err.Error(), "duplicate key") {
			conflicted++
		} else {
			t.Fatalf("concurrent declaration error=%v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent declarations succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if _, err := db.ExecContext(ctx, identityRegistryDeclarationInsertSQL,
		"provider", "fixture.identity.provider.risk", int64(3), "tombstone",
		int64(101), "1.0.0", strings.Repeat("a", 64),
		"fixture.identity.provider.risk@1", strings.Repeat("e", 64)); err == nil ||
		!strings.Contains(err.Error(), "does not match the active artifact") {
		t.Fatalf("stale provider tombstone error=%v", err)
	}
	insertIdentityRegistryDeclaration(t, ctx, db,
		"provider", "fixture.identity.provider.risk", 3, "tombstone", 102, "2.0.0", "b",
		"fixture.identity.provider.risk@1", "f")
}

func assertIdentityRegistryOwnerTypeRace(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	connections, closeConnections := identityRegistryConcurrentConnections(t, ctx, db)
	defer closeConnections()
	tx, err := connections[0].BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'race.plugin.permission', 'race.plugin')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[1].ExecContext(ctx, `SET lock_timeout = '250ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[1].ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'race.plugin'
	`); err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("concurrent owner/type update did not serialize: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[1].ExecContext(ctx, `SET lock_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[1].ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'race.plugin'
	`); err == nil || !strings.Contains(err.Error(), "type is immutable") {
		t.Fatalf("committed owner did not fence type update: %v", err)
	}
}

func waitForIdentityRegistryDownLock(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE relation = to_regclass(current_schema() || '.extension_identity_registry_owners')
				  AND mode = 'AccessExclusiveLock'
				  AND NOT granted
			)
		`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("identity registry Down never waited for the concurrent owner writer")
}

func assertIdentityRegistrySuggestionConcurrentCAS(t *testing.T, ctx context.Context, db *sql.DB, actorID int64) {
	t.Helper()
	var suggestionID int64
	if err := db.QueryRowContext(ctx, identityRoleSuggestionInsertSQL+" RETURNING id", "moderator").Scan(&suggestionID); err != nil {
		t.Fatal(err)
	}
	connections, closeConnections := identityRegistryConcurrentConnections(t, ctx, db)
	type updateResult struct {
		affected int64
		err      error
	}
	start := make(chan struct{})
	results := make(chan updateResult, 2)
	states := []string{"approved", "rejected"}
	var workers sync.WaitGroup
	for index, state := range states {
		connection := connections[index]
		state := state
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := connection.ExecContext(ctx, `
				UPDATE extension_permission_role_suggestions
				SET approval_state = $2, revision = revision + 1,
				    decided_by_user_id = $3, decision_audit_event_id = 7100 + id
				WHERE id = $1 AND revision = 1
			`, suggestionID, state, actorID)
			if err != nil {
				results <- updateResult{err: err}
				return
			}
			affected, err := result.RowsAffected()
			results <- updateResult{affected: affected, err: err}
		}()
	}
	close(start)
	workers.Wait()
	closeConnections()
	close(results)
	var won, lost int
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent suggestion decision error=%v", result.err)
		}
		switch result.affected {
		case 1:
			won++
		case 0:
			lost++
		default:
			t.Fatalf("concurrent suggestion affected=%d", result.affected)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("concurrent suggestion decisions won=%d lost=%d", won, lost)
	}
	var state string
	var revision int64
	if err := db.QueryRowContext(ctx, `
		SELECT approval_state, revision
		FROM extension_permission_role_suggestions WHERE id = $1
	`, suggestionID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if (state != "approved" && state != "rejected") || revision != 2 {
		t.Fatalf("suggestion state=%q revision=%d", state, revision)
	}
}

func identityRegistryConcurrentConnections(t *testing.T, ctx context.Context, db *sql.DB) ([]*sql.Conn, func()) {
	t.Helper()
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "sforum_lease_") {
		t.Fatalf("unexpected isolated schema %q", schema)
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)
	connections := make([]*sql.Conn, 2)
	for index := range connections {
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection
		if _, err := connection.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
			connection.Close()
			t.Fatal(err)
		}
	}
	return connections, func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
}

func assertIdentityRegistryNestedOwnership(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'ab.c.profile', 'ab')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extensions WHERE id = 'ab'`); err != nil {
		t.Fatalf("uninstall owner extension: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'ab.c.profile', 'ab.c')
	`); err == nil {
		t.Fatal("nested extension id reclaimed durable owner tombstone")
	}
	var retainedOwner string
	if err := db.QueryRowContext(ctx, `
		SELECT owner_extension_id
		FROM extension_identity_registry_owners
		WHERE identity_kind = 'permission' AND stable_id = 'ab.c.profile'
	`).Scan(&retainedOwner); err != nil {
		t.Fatal(err)
	}
	if retainedOwner != "ab" {
		t.Fatalf("retained owner=%q", retainedOwner)
	}
}

func seedIdentityRegistryOwnershipFixtures(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extensions (id, type, name, status) VALUES
			('fixture.identity', 'plugin', 'Identity Fixture', 'enabled'),
			('fixture.theme', 'theme', 'Theme Fixture', 'enabled'),
			('other.plugin', 'plugin', 'Other Plugin', 'enabled'),
			('core', 'plugin', 'Bare Core Spoof', 'enabled'),
			('core.spoof', 'plugin', 'Core Spoof', 'enabled'),
			('race.plugin', 'plugin', 'Owner Type Race', 'enabled'),
			('ab', 'plugin', 'Nested Owner', 'enabled'),
			('ab.c', 'plugin', 'Nested Contender', 'enabled')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES
			(101, 'fixture.identity', '1.0.0', '{}'::jsonb, '/tmp/identity-v1', repeat('a', 64)),
			(102, 'fixture.identity', '2.0.0', '{}'::jsonb, '/tmp/identity-v2', repeat('b', 64)),
			(103, 'fixture.theme', '1.0.0', '{}'::jsonb, '/tmp/theme-v1', repeat('d', 64))
	`); err != nil {
		t.Fatal(err)
	}
}

func seedIdentityRegistryOwnershipBaseTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active'
			  CHECK (status IN ('active', 'disabled', 'banned'))
		);
		CREATE TABLE roles (
			id BIGSERIAL PRIMARY KEY,
			key TEXT NOT NULL UNIQUE
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
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed'
			  CHECK (status IN ('installed', 'enabled', 'disabled'))
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL,
			package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		INSERT INTO roles (key) VALUES ('super_admin'), ('member'), ('operator'), ('moderator');
	`); err != nil {
		t.Fatal(err)
	}
}

func assertIdentityRegistryOwnershipSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"extension_identity_registry_owners",
		"extension_identity_registry_declarations",
		"extension_permission_role_suggestions",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL
		`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("identity registry table %s exists=%t want=%t", table, exists, want)
		}
	}
}
