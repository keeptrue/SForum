package identityregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type identityRegistryStoreFixture struct {
	t       *testing.T
	ctx     context.Context
	admin   *pgxpool.Pool
	pool    *pgxpool.Pool
	store   *PostgresStore
	schema  string
	actorID int64
}

func newIdentityRegistryStoreFixture(t *testing.T) *identityRegistryStoreFixture {
	t.Helper()
	// Never fall back to the normal/dev DATABASE_URL: identity fixtures create
	// private schemas and extension rows that must stay off the operator DB.
	databaseURL := testdb.IsolatedURL(t)

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_registry_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	}

	sqlConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	sqlConfig.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*sqlConfig)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := seedIdentityRegistryStoreBaseTables(ctx, db); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if err := applyIdentityRegistryOwnershipMigration(ctx, db); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	db.Close()

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}

	fixture := &identityRegistryStoreFixture{
		t: t, ctx: ctx, admin: admin, pool: pool,
		store: NewPostgresStore(pool), schema: schema,
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'Identity Registry Actor', 'active')
		RETURNING id
	`, "identity_registry_"+schema, "identity_registry_"+schema+"@example.test").Scan(&fixture.actorID); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status) VALUES
			('fixture.identity', 'plugin', 'Identity Fixture', 'enabled')
	`); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES (
			101, 'fixture.identity', '1.0.0', '{}'::jsonb, '/tmp/identity-v1', repeat('a', 64)
		)
	`); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extensions SET active_version_id = 101 WHERE id = 'fixture.identity'
	`); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'identity_reviewer'
	`, fixture.actorID); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		pool.Close()
		removeSchema()
		admin.Close()
	})
	return fixture
}

func seedIdentityRegistryStoreBaseTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
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
		  ('super_admin'), ('member'), ('operator'), ('moderator'), ('identity_reviewer');
		INSERT INTO permissions (key, module, description)
		VALUES
		  ('topic.create', 'forum', 'Create topics'),
		  ('role.manage', 'identity', 'Manage role permissions');
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'topic.create' FROM roles WHERE key = 'member';
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'role.manage' FROM roles WHERE key = 'identity_reviewer';
	`)
	return err
}

func applyIdentityRegistryOwnershipMigration(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	if _, err = provider.ApplyVersion(ctx, identityRegistryOwnershipMigrationVersion, true); err != nil {
		return err
	}
	if _, err = provider.ApplyVersion(ctx, identityRoleApprovalsMigrationVersion, true); err != nil {
		return err
	}
	if _, err = provider.ApplyVersion(ctx, identityRootPublicationsMigrationVersion, true); err != nil {
		return err
	}
	if _, err = provider.ApplyVersion(ctx, identityOrphanTombstoneMigrationVersion, true); err != nil {
		return err
	}
	if _, err = provider.ApplyVersion(ctx, extensionPermissionLocalizationMigrationVersion, true); err != nil {
		return err
	}
	_, err = provider.ApplyVersion(ctx, identityStaleRoleSuggestionRejectionMigrationVersion, true)
	return err
}

func (f *identityRegistryStoreFixture) seedOwner(t *testing.T, kind, stableID string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ($1, $2, 'fixture.identity')
	`, kind, stableID); err != nil {
		t.Fatal(err)
	}
}

func (f *identityRegistryStoreFixture) seedDeclaration(
	t *testing.T,
	kind, stableID string,
	revision int64,
	state, contractVersion, declarationByte string,
) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, owner_extension_id, revision, registry_state,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES (
			$1, $2, 'fixture.identity', $3, $4,
			101, '1.0.0', repeat('a', 64),
			$5, repeat($6, 64)
		)
	`, kind, stableID, revision, state, contractVersion, declarationByte); err != nil {
		t.Fatal(err)
	}
}

func (f *identityRegistryStoreFixture) seedPermissionCatalog(
	t *testing.T,
	permissionKey, contractVersion, declarationByte string,
	revision int64,
) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO permissions (key, module, description)
		VALUES ($1, 'extension', '')
		ON CONFLICT (key) DO NOTHING
	`, permissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `
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

func (f *identityRegistryStoreFixture) seedRoleManager(t *testing.T, status string) int64 {
	t.Helper()
	var actorID int64
	username := fmt.Sprintf("identity_reviewer_%d", time.Now().UnixNano())
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'Identity reviewer', $3)
		RETURNING id
	`, username, username+"@example.test", status).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'identity_reviewer'
	`, actorID); err != nil {
		t.Fatal(err)
	}
	return actorID
}

func (f *identityRegistryStoreFixture) seedSuggestion(t *testing.T, roleKey string) int64 {
	return f.seedSuggestionFor(
		t, "fixture.identity.profile", "fixture.identity.profile@1", "c", roleKey,
	)
}

func (f *identityRegistryStoreFixture) seedSuggestionFor(
	t *testing.T,
	permissionKey, contractVersion, declarationByte, roleKey string,
) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_permission_role_suggestions (
			permission_key, owner_extension_id, extension_version_id,
			extension_version, package_digest, permission_contract_version,
			declaration_digest, role_key
		) VALUES (
			$1, 'fixture.identity', 101,
			'1.0.0', repeat('a', 64), $2,
			repeat($3, 64), $4
		)
		RETURNING id
	`, permissionKey, contractVersion, declarationByte, roleKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *identityRegistryStoreFixture) insertLegacyReviewAudit(t *testing.T, suggestionID int64, roleKey string) int64 {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"suggestionId": suggestionID, "permissionKey": "fixture.identity.profile",
		"ownerExtensionId": "fixture.identity", "extensionVersionId": 101,
		"extensionVersion": "1.0.0", "packageDigest": strings.Repeat("a", 64),
		"permissionContractVersion": "fixture.identity.profile@1",
		"declarationDigest":         strings.Repeat("c", 64),
		"roleKey":                   roleKey, "expectedRevision": 1,
		"approvalState":               "approved",
		"permissionCatalogRegistered": false,
		"rolePermissionAdded":         false,
		"roleGrantApplied":            false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var auditID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'identity.role_suggestion.approve', $2::jsonb)
		RETURNING id
	`, f.actorID, string(metadata)).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	return auditID
}

func (f *identityRegistryStoreFixture) countRolePermissions(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM role_permissions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *identityRegistryStoreFixture) countRolePermissionMapping(
	t *testing.T,
	roleKey string,
	permissionKey string,
) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*)
		FROM role_permissions
		JOIN roles ON roles.id = role_permissions.role_id
		WHERE roles.key = $1 AND role_permissions.permission_key = $2
	`, roleKey, permissionKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *identityRegistryStoreFixture) countAuditEvents(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *identityRegistryStoreFixture) countGrants(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM extension_permission_role_grants`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *identityRegistryStoreFixture) assertAudit(
	t *testing.T,
	auditEventID int64,
	suggestion RoleSuggestion,
	action string,
	wantRolePermissionAdded *bool,
	wantRoleGrantApplied bool,
) {
	t.Helper()
	var gotActor int64
	var gotAction string
	var metadataJSON []byte
	if err := f.pool.QueryRow(f.ctx, `
		SELECT COALESCE(actor_user_id, 0), action, metadata
		FROM audit_events WHERE id = $1
	`, auditEventID).Scan(&gotActor, &gotAction, &metadataJSON); err != nil {
		t.Fatal(err)
	}
	wantActor := suggestion.DecidedByUserID
	if auditEventID == suggestion.AppliedAuditEventID && suggestion.AppliedByUserID > 0 {
		wantActor = suggestion.AppliedByUserID
	}
	if gotActor != wantActor {
		t.Fatalf("audit id=%d actor=%d want %d", auditEventID, gotActor, wantActor)
	}
	if gotAction != action {
		t.Fatalf("audit id=%d action=%q want %q", auditEventID, gotAction, action)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	rolePermissionAdded, typedRolePermissionAdded := metadata["rolePermissionAdded"].(bool)
	if metadata["suggestionId"] != float64(suggestion.ID) ||
		metadata["permissionKey"] != suggestion.PermissionKey ||
		metadata["ownerExtensionId"] != suggestion.OwnerExtensionID ||
		metadata["roleKey"] != suggestion.RoleKey ||
		metadata["approvalState"] != suggestion.ApprovalState ||
		!typedRolePermissionAdded ||
		(wantRolePermissionAdded != nil && rolePermissionAdded != *wantRolePermissionAdded) ||
		metadata["roleGrantApplied"] != wantRoleGrantApplied {
		t.Fatalf("audit metadata=%v for suggestion=%#v", metadata, suggestion)
	}
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE audit_events SET metadata = metadata || '{"tampered":true}'::jsonb WHERE id = $1
	`, auditEventID); err == nil || !strings.Contains(err.Error(), "audit evidence is immutable") {
		t.Fatalf("decision audit update error=%v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM audit_events WHERE id = $1`, auditEventID); err == nil ||
		!strings.Contains(err.Error(), "audit evidence is immutable") {
		t.Fatalf("decision audit delete error=%v", err)
	}
}

func boolExpectation(value bool) *bool {
	return &value
}
