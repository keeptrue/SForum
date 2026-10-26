package identity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestProductionRoleReplacementAndSuggestionApprovalSerializeExactMapping(t *testing.T) {
	for _, test := range []struct {
		name             string
		replacement      []string
		wantFinalMapping int
	}{
		{
			name:             "replacement includes approved mapping",
			replacement:      []string{"topic.create", "fixture.identity.profile"},
			wantFinalMapping: 1,
		},
		{
			name:             "replacement excludes approved mapping",
			replacement:      []string{"topic.create"},
			wantFinalMapping: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionIdentityApprovalFixture(t)
			ctx, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
			defer cancel()

			start := make(chan struct{})
			var wait sync.WaitGroup
			var approved identityregistry.RoleSuggestion
			var approveErr, replaceErr error
			wait.Add(2)
			go func() {
				defer wait.Done()
				<-start
				approved, approveErr = fixture.registry.DecideRoleSuggestion(ctx, identityregistry.DecideRoleSuggestionInput{
					ID:               fixture.suggestionID,
					ExpectedRevision: 1,
					ApprovalState:    identityregistry.RoleSuggestionApproved,
					ActorUserID:      fixture.actorUserID,
				})
			}()
			go func() {
				defer wait.Done()
				<-start
				replaceErr = fixture.roles.ReplaceRolePermissions(
					ctx, fixture.actorUserID, RoleMember, test.replacement,
				)
			}()
			close(start)
			wait.Wait()
			if ctx.Err() != nil {
				t.Fatalf("production approval/replacement exceeded deadline: %v", ctx.Err())
			}
			if approveErr != nil || replaceErr != nil {
				t.Fatalf("approval error=%v replacement error=%v", approveErr, replaceErr)
			}
			if !approved.Applied || approved.DecisionAuditEventID <= 0 {
				t.Fatalf("approval did not retain grant evidence: %#v", approved)
			}

			// 任一事务都可能先提交；再次应用同一 Host 全量集合，证明公开替换
			// 语义可确定地撤销或保留 mapping，同时不会删除插件审批证据。
			if err := fixture.roles.ReplaceRolePermissions(
				ctx, fixture.actorUserID, RoleMember, test.replacement,
			); err != nil {
				t.Fatal(err)
			}
			if got := fixture.mappingCount(t); got != test.wantFinalMapping {
				t.Fatalf("final mapping=%d want %d", got, test.wantFinalMapping)
			}
			if got := fixture.grantCount(t); got != 1 {
				t.Fatalf("immutable grant rows=%d want 1", got)
			}

			auditsBeforeReplay := fixture.auditCount(t)
			replay, err := fixture.registry.DecideRoleSuggestion(ctx, identityregistry.DecideRoleSuggestionInput{
				ID:               fixture.suggestionID,
				ExpectedRevision: 1,
				ApprovalState:    identityregistry.RoleSuggestionApproved,
				ActorUserID:      fixture.actorUserID,
			})
			if err != nil || !replay.Applied {
				t.Fatalf("approval replay=%#v error=%v", replay, err)
			}
			if fixture.auditCount(t) != auditsBeforeReplay || fixture.grantCount(t) != 1 {
				t.Fatal("approval replay duplicated authority evidence")
			}
			if got := fixture.mappingCount(t); got != test.wantFinalMapping {
				t.Fatalf("approval replay restored explicitly replaced mapping=%d", got)
			}
		})
	}
}

type productionIdentityApprovalFixture struct {
	ctx          context.Context
	pool         *pgxpool.Pool
	roles        *PostgresStore
	registry     *identityregistry.PostgresStore
	schema       string
	actorUserID  int64
	suggestionID int64
}

func newProductionIdentityApprovalFixture(t *testing.T) *productionIdentityApprovalFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_prod_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := seedProductionIdentityApprovalBaseTables(ctx, db); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("seed production identity approval fixture: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607160028, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply identity ownership fixture migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607160029, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply identity approval fixture migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607231001, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply extension permission localization fixture migration: %v", err)
	}
	if err := db.Close(); err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}

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
	fixture := &productionIdentityApprovalFixture{
		ctx: ctx, pool: pool, schema: schema,
		roles: NewPostgresStore(pool), registry: identityregistry.NewPostgresStore(pool),
	}
	if err := fixture.seed(); err != nil {
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

func seedProductionIdentityApprovalBaseTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			locale TEXT NOT NULL DEFAULT 'zh-CN',
			status TEXT NOT NULL DEFAULT 'active'
			  CHECK (status IN ('active', 'disabled', 'banned')),
			is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE roles (
			id BIGSERIAL PRIMARY KEY,
			key TEXT NOT NULL UNIQUE,
			alias TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			is_system BOOLEAN NOT NULL DEFAULT FALSE,
			is_default BOOLEAN NOT NULL DEFAULT FALSE,
			is_deletable BOOLEAN NOT NULL DEFAULT TRUE,
			is_enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE permissions (
			key TEXT PRIMARY KEY,
			module TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE role_permissions (
			role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
			permission_key TEXT NOT NULL REFERENCES permissions(key) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (role_id, permission_key)
		);
		CREATE TABLE user_roles (
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
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
		INSERT INTO roles (
			key, alias, is_system, is_default, is_deletable, is_enabled
		) VALUES
			('super_admin', 'Super admin', TRUE, FALSE, FALSE, TRUE),
			('member', 'Member', TRUE, TRUE, FALSE, TRUE);
		INSERT INTO permissions (key, module, description) VALUES
			('role.manage', 'identity', 'Manage role permissions'),
			('topic.create', 'forum', 'Create topics');
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT roles.id, permissions.key
		FROM roles CROSS JOIN permissions
		WHERE roles.key = 'super_admin';
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'topic.create' FROM roles WHERE key = 'member';
	`)
	return err
}

func (f *productionIdentityApprovalFixture) seed() error {
	username := "identity_prod_" + f.schema
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'Production identity actor', 'active')
		RETURNING id
	`, username, username+"@example.test").Scan(&f.actorUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'super_admin'
	`, f.actorUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ('fixture.identity', 'plugin', 'Identity Fixture', 'enabled');
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES (
			101, 'fixture.identity', '1.0.0', '{}'::jsonb,
			'/tmp/identity-production-v1', repeat('a', 64)
		);
		UPDATE extensions SET active_version_id = 101 WHERE id = 'fixture.identity';
		INSERT INTO extension_identity_registry_owners (
			identity_kind, stable_id, owner_extension_id
		) VALUES ('permission', 'fixture.identity.profile', 'fixture.identity');
		INSERT INTO extension_identity_registry_declarations (
			identity_kind, stable_id, owner_extension_id, revision, registry_state,
			extension_version_id, extension_version, package_digest,
			contract_version, declaration_digest
		) VALUES (
			'permission', 'fixture.identity.profile', 'fixture.identity', 1, 'active',
			101, '1.0.0', repeat('a', 64),
			'fixture.identity.profile@1', repeat('c', 64)
		);
		INSERT INTO permissions (key, module, description)
		VALUES ('fixture.identity.profile', 'extension', 'Identity fixture permission');
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
		return err
	}
	return f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_permission_role_suggestions (
			permission_key, owner_extension_id, extension_version_id,
			extension_version, package_digest, permission_contract_version,
			declaration_digest, role_key
		) VALUES (
			'fixture.identity.profile', 'fixture.identity', 101,
			'1.0.0', repeat('a', 64), 'fixture.identity.profile@1',
			repeat('c', 64), 'member'
		)
		RETURNING id
	`).Scan(&f.suggestionID)
}

func (f *productionIdentityApprovalFixture) mappingCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*)
		FROM role_permissions
		JOIN roles ON roles.id = role_permissions.role_id
		WHERE roles.key = 'member'
		  AND role_permissions.permission_key = 'fixture.identity.profile'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *productionIdentityApprovalFixture) grantCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM extension_permission_role_grants WHERE suggestion_id = $1
	`, f.suggestionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *productionIdentityApprovalFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
