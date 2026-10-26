package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

type identityPersistencePGFixture struct {
	t               *testing.T
	ctx             context.Context
	admin           *pgxpool.Pool
	pool            *pgxpool.Pool
	databaseURL     string
	schema          string
	adminUserID     int64
	actorUserID     int64
	targetUserID    int64
	otherUserID     int64
	extensionID     string
	versionID       int64
	publication     identityregistry.Publication
	field           identityregistry.UserFieldContribution
	provider        identityregistry.ProviderContribution
	sessionProvider identityregistry.ProviderContribution
	registry        *identityregistry.Registry
	registryStore   *identityregistry.PostgresStore
	externalLinks   *PostgresExternalIdentityLinkStore
	userFields      *PostgresIdentityUserFieldValueStore
	sessionPolicy   *PostgresIdentitySessionPolicyStore
}

const (
	identityPersistencePermissionKey = "fixture.identity.membership.profile"
	identityPersistenceFieldID       = "fixture.identity.membership.member_code"
	identityPersistenceFieldScope    = "fixture.identity.membership.user-field@1"
	identityPersistencePrivacyScope  = "host.identity.user-field.privacy@1"
)

func newIdentityPersistencePGFixture(t *testing.T) *identityPersistencePGFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_persistence_%d", time.Now().UnixNano())
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
	applyIdentityPersistenceTestMigrations(t, ctx, db, removeSchema)

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &identityPersistencePGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool, schema: schema,
		databaseURL:   databaseURL,
		extensionID:   "fixture.identity.membership",
		registryStore: identityregistry.NewPostgresStore(pool),
		externalLinks: NewPostgresExternalIdentityLinkStore(pool),
	}
	if err := fixture.seed(); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture.userFields, err = NewPostgresIdentityUserFieldValueStore(
		pool, fixture.registry, []byte(strings.Repeat("k", 32)),
	)
	if err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture.sessionPolicy, err = NewPostgresIdentitySessionPolicyStore(pool, fixture.registry)
	if err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture.registryStore = identityregistry.NewPostgresStoreWithDependencies(
		pool,
		identityregistry.PostgresStoreDependencies{
			SessionPolicyInvalidator: fixture.sessionPolicy,
		},
	)
	t.Cleanup(func() {
		pool.Close()
		removeSchema()
		admin.Close()
	})
	return fixture
}

func (f *identityPersistencePGFixture) newPool(name string) *pgxpool.Pool {
	f.t.Helper()
	config, err := pgxpool.ParseConfig(f.databaseURL)
	if err != nil {
		f.t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = f.schema + ",public"
	config.ConnConfig.RuntimeParams["application_name"] = name
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(pool.Close)
	return pool
}

func applyIdentityPersistenceTestMigrations(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	removeSchema func(),
) {
	t.Helper()
	defer db.Close()
	if err := seedIdentityPersistenceTestBaseTables(ctx, db); err != nil {
		removeSchema()
		t.Fatalf("seed isolated identity persistence schema: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		removeSchema()
		t.Fatal(err)
	}
	for _, version := range []int64{
		202607160028,
		202607160029,
		202607160033,
		202607170034,
		202607190037,
		202607190038,
		202607190039,
		202607190040,
		202607190041,
		202607231001,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			removeSchema()
			t.Fatalf("apply identity persistence migration %d: %v", version, err)
		}
	}
}

func seedIdentityPersistenceTestBaseTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			locale TEXT NOT NULL DEFAULT 'zh-CN',
			email_verified_at TIMESTAMPTZ,
			status TEXT NOT NULL DEFAULT 'active'
			  CHECK (status IN ('active', 'disabled', 'banned')),
			current_token_version BIGINT NOT NULL DEFAULT 0,
			kind TEXT NOT NULL DEFAULT 'human'
			  CHECK (kind IN ('human', 'bot'))
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
			source TEXT NOT NULL DEFAULT 'uploaded',
			is_system BOOLEAN NOT NULL DEFAULT FALSE,
			is_deletable BOOLEAN NOT NULL DEFAULT TRUE,
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			manifest JSONB NOT NULL,
			package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		INSERT INTO roles (key) VALUES
		  ('super_admin'), ('member'), ('operator'), ('moderator'), ('identity_reviewer');
		INSERT INTO permissions (key, module, description) VALUES
		  ('topic.create', 'forum', 'Create topics'),
		  ('role.manage', 'identity', 'Manage role permissions');
	`)
	return err
}

func (f *identityPersistencePGFixture) seed() error {
	for _, target := range []struct {
		username string
		name     string
		id       *int64
	}{
		{username: "identity_admin_" + f.schema, name: "Identity Admin", id: &f.adminUserID},
		{username: "identity_actor_" + f.schema, name: "Identity Actor", id: &f.actorUserID},
		{username: "identity_target_" + f.schema, name: "Identity Target", id: &f.targetUserID},
		{username: "identity_other_" + f.schema, name: "Identity Other", id: &f.otherUserID},
	} {
		if err := f.pool.QueryRow(f.ctx, `
			INSERT INTO users (
				username, username_lower, email, email_lower, display_name, locale, status
			) VALUES ($1, $1, $2, $2, $3, 'zh-CN', 'active')
			RETURNING id
		`, target.username, target.username+"@example.test", target.name).Scan(target.id); err != nil {
			return err
		}
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'super_admin'
	`, f.adminUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extensions (
			id, type, name, status, source, is_system, is_deletable
		) VALUES ($1, 'plugin', 'Identity Membership Fixture', 'enabled', 'uploaded', false, true)
	`, f.extensionID); err != nil {
		return err
	}
	digest := strings.Repeat("a", 64)
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.0', '{}'::jsonb, '/tmp/identity-membership-fixture', $2)
		RETURNING id
	`, f.extensionID, digest).Scan(&f.versionID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, f.extensionID, f.versionID); err != nil {
		return err
	}
	publication, err := identityPersistenceProviderPublication(
		f.extensionID, f.versionID, digest, "identity-runtime",
	)
	if err != nil {
		return err
	}
	var auditID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'identity.registry.fixture', '{}'::jsonb)
		RETURNING id
	`, f.actorUserID).Scan(&auditID); err != nil {
		return err
	}
	if _, err := f.registryStore.Reconcile(f.ctx, identityregistry.ReconcilePublicationInput{
		ExtensionID: f.extensionID, AllowedTarget: &publication.Artifact,
		Desired: &publication, ActorUserID: f.actorUserID, AuditEventID: auditID,
	}); err != nil {
		return err
	}
	var implicitGrants int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM role_permissions WHERE permission_key = $1
	`, identityPersistencePermissionKey).Scan(&implicitGrants); err != nil {
		return err
	}
	if implicitGrants != 0 {
		return fmt.Errorf("identity publication created %d implicit role grants", implicitGrants)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'identity_reviewer'
	`, f.actorUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = 'identity_reviewer'
	`, identityPersistencePermissionKey); err != nil {
		return err
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(publication); err != nil {
		return err
	}
	field, err := registry.ResolveUserField(identityPersistenceFieldID)
	if err != nil {
		return err
	}
	provider, err := registry.ResolveProvider(f.extensionID + ".auth")
	if err != nil {
		return err
	}
	sessionProvider, err := registry.ResolveProvider(publication.Identity.SessionPolicy)
	if err != nil {
		return err
	}
	f.publication = publication
	f.field = field
	f.provider = provider
	f.sessionProvider = sessionProvider
	f.registry = registry
	return nil
}

func (f *identityPersistencePGFixture) linkInput(
	userID int64,
	key string,
	subjectDigest string,
) LinkExternalIdentityInput {
	return LinkExternalIdentityInput{
		UserID: userID, Provider: f.provider, ProviderOperation: "link.complete",
		ProviderSubjectDigest: subjectDigest, ActorUserID: userID, IdempotencyKey: key,
	}
}

func (f *identityPersistencePGFixture) retireProvider() error {
	_, err := f.registryStore.Reconcile(f.ctx, identityregistry.ReconcilePublicationInput{
		ExtensionID: f.extensionID, AllowedSource: &f.publication.Artifact,
		ActorUserID: f.actorUserID, AuditEventID: 99,
	})
	return err
}

func (f *identityPersistencePGFixture) insertSessionPolicyPublicationVersion(
	version string,
	digestByte string,
	runtimeInstanceID string,
) identityregistry.Publication {
	f.t.Helper()
	digest := strings.Repeat(digestByte, 64)
	var versionID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, $2, '{}'::jsonb, $3, $4)
		RETURNING id
	`, f.extensionID, version, "/tmp/identity-membership-fixture-"+version, digest).Scan(&versionID); err != nil {
		f.t.Fatal(err)
	}
	publication, err := identityPersistenceProviderPublicationVersion(
		f.extensionID,
		versionID,
		version,
		digest,
		runtimeInstanceID,
	)
	if err != nil {
		f.t.Fatal(err)
	}
	return publication
}

func (f *identityPersistencePGFixture) installSessionPolicyLifecycleFailure(target string) {
	f.t.Helper()
	var statement string
	switch target {
	case "audit":
		statement = `
			CREATE FUNCTION reject_session_policy_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.action = 'identity.session_policy.invalidate' THEN
					RAISE EXCEPTION 'forced session-policy lifecycle audit failure';
				END IF;
				RETURN NEW;
			END;
			$$;
			CREATE TRIGGER reject_session_policy_lifecycle_audit
			BEFORE INSERT ON audit_events
			FOR EACH ROW EXECUTE FUNCTION reject_session_policy_lifecycle_audit();
		`
	case "event":
		statement = `
			CREATE FUNCTION reject_session_policy_lifecycle_event() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.action = 'invalidate' THEN
					RAISE EXCEPTION 'forced session-policy lifecycle event failure';
				END IF;
				RETURN NEW;
			END;
			$$;
			CREATE TRIGGER reject_session_policy_lifecycle_event
			BEFORE INSERT ON identity_session_policy_selection_events
			FOR EACH ROW EXECUTE FUNCTION reject_session_policy_lifecycle_event();
		`
	case "root":
		statement = `
			CREATE FUNCTION reject_session_policy_lifecycle_root() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.registry_state = 'tombstone' THEN
					RAISE EXCEPTION 'forced identity root retirement failure';
				END IF;
				RETURN NEW;
			END;
			$$;
			CREATE TRIGGER reject_session_policy_lifecycle_root
			BEFORE INSERT ON extension_identity_registry_publications
			FOR EACH ROW EXECUTE FUNCTION reject_session_policy_lifecycle_root();
		`
	default:
		f.t.Fatalf("unknown lifecycle failure target %q", target)
	}
	if _, err := f.pool.Exec(f.ctx, statement); err != nil {
		f.t.Fatal(err)
	}
}

func (f *identityPersistencePGFixture) installSessionPolicyCoreBeforeRegistryWriteGuard() {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		CREATE FUNCTION require_core_before_identity_registry_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (
				SELECT 1
				FROM identity_session_policy_selection
				WHERE singleton = TRUE AND owner_extension_id = NEW.owner_extension_id
			) THEN
				RAISE EXCEPTION 'session policy was not reset before identity registry write';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER require_core_before_identity_registry_write
		BEFORE INSERT ON extension_identity_registry_publications
		FOR EACH ROW
		WHEN (NEW.revision > 1)
		EXECUTE FUNCTION require_core_before_identity_registry_write();
		CREATE TRIGGER require_core_before_identity_registry_declaration_write
		BEFORE INSERT ON extension_identity_registry_declarations
		FOR EACH ROW
		WHEN (NEW.identity_kind = 'provider' AND NEW.revision > 1)
		EXECUTE FUNCTION require_core_before_identity_registry_write();
	`); err != nil {
		f.t.Fatal(err)
	}
}

func (f *identityPersistencePGFixture) assertExternalLinkCounts(links, events, audits int) {
	f.t.Helper()
	var gotLinks, gotEvents, gotAudits int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_external_links`).Scan(&gotLinks); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_external_link_events`).Scan(&gotEvents); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events WHERE action LIKE 'identity.external_link.%'
	`).Scan(&gotAudits); err != nil {
		f.t.Fatal(err)
	}
	if gotLinks != links || gotEvents != events || gotAudits != audits {
		f.t.Fatalf(
			"external link counts links=%d/%d events=%d/%d audits=%d/%d",
			gotLinks, links, gotEvents, events, gotAudits, audits,
		)
	}
}

func identityPersistenceProviderPublication(
	extensionID string,
	versionID int64,
	packageDigest string,
	runtimeInstanceID string,
) (identityregistry.Publication, error) {
	return identityPersistenceProviderPublicationVersion(
		extensionID,
		versionID,
		"1.0.0",
		packageDigest,
		runtimeInstanceID,
	)
}

func identityPersistenceProviderPublicationVersion(
	extensionID string,
	versionID int64,
	extensionVersion string,
	packageDigest string,
	runtimeInstanceID string,
) (identityregistry.Publication, error) {
	artifact := identityregistry.Artifact{
		ExtensionID: extensionID, ExtensionVersion: extensionVersion, PackageDigest: packageDigest,
		VersionID: versionID, RuntimeInstanceID: runtimeInstanceID,
	}
	providerID := extensionID + ".auth"
	sessionProviderID := extensionID + ".session"
	publication := identityregistry.Publication{
		Artifact: artifact,
		Permissions: []identityregistry.PermissionDefinition{{
			Key: identityPersistencePermissionKey, ContractVersion: identityPersistencePermissionKey + "@1",
			Label: "Membership profile", Description: "Manage membership profile fields",
			RecommendedRoles: []string{"identity_reviewer"}, AssignmentPolicy: "host",
		}},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: extensionID + "@1",
			SessionPolicy:   sessionProviderID,
			UserFields: []identityregistry.UserField{{
				ID: identityPersistenceFieldID, ContractVersion: identityPersistenceFieldID + "@1",
				Type: "string", Schema: "schemas/member-code.json",
				ReadPermission: identityPersistencePermissionKey, WritePermission: identityPersistencePermissionKey,
			}},
			Providers: []identityregistry.Provider{
				{
					ID: providerID, ContractVersion: providerID + "@1",
					Kind: identityregistry.ProviderKindAuth, Handler: "identity.auth", Priority: 10,
					Operations: []identityregistry.ProviderOperation{
						{
							Name: "registration.complete", InputSchema: "schemas/registration-input.json",
							OutputSchema: "schemas/registration-output.json", TimeoutMS: 500,
							FailurePolicy: identityregistry.ProviderFailureFailClosed,
						},
						{
							Name: "link.complete", InputSchema: "schemas/link-input.json",
							OutputSchema: "schemas/link-output.json", TimeoutMS: 500,
							FailurePolicy: identityregistry.ProviderFailureFailClosed,
						},
					},
				},
				{
					ID: sessionProviderID, ContractVersion: sessionProviderID + "@1",
					Kind: identityregistry.ProviderKindSession, Handler: "identity.session",
					Operations: []identityregistry.ProviderOperation{{
						Name: "session.evaluate", InputSchema: "schemas/session-input.json",
						OutputSchema: "schemas/session-output.json", TimeoutMS: 500,
						FailurePolicy: identityregistry.ProviderFailureFailClosed,
					}},
				},
			},
		},
	}
	fieldSchema := []byte(`{"type":"string","minLength":3,"maxLength":32}`)
	fieldBinding := identityregistry.UserFieldSchemaBinding{
		FieldID: identityPersistenceFieldID, ContractVersion: identityPersistenceFieldID + "@1",
		Artifact: artifact,
		Schema: identityPersistenceSchemaMaterialWithBody(
			"schemas/member-code.json", identityPersistenceFieldID+".value@1", fieldSchema,
		),
	}
	bindings := make([]identityregistry.ProviderOperationSchemaBinding, 0, 3)
	for _, provider := range publication.Identity.Providers {
		for _, operation := range provider.Operations {
			bindings = append(bindings, identityregistry.ProviderOperationSchemaBinding{
				ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
				Operation: operation.Name, Artifact: artifact,
				Input: identityPersistenceSchemaMaterial(
					operation.InputSchema, provider.ID+"."+strings.ReplaceAll(operation.Name, ".", "_")+".input@1",
				),
				Output: identityPersistenceSchemaMaterial(
					operation.OutputSchema, provider.ID+"."+strings.ReplaceAll(operation.Name, ".", "_")+".output@1",
				),
			})
		}
	}
	return identityregistry.BindJSONSchemas(
		publication, []identityregistry.UserFieldSchemaBinding{fieldBinding}, bindings,
	)
}

func identityPersistenceSchemaMaterial(reference string, wireReference string) identityregistry.JSONSchemaMaterial {
	body := []byte(`{"type":"object","additionalProperties":true}`)
	return identityPersistenceSchemaMaterialWithBody(reference, wireReference, body)
}

func identityPersistenceSchemaMaterialWithBody(
	reference string,
	wireReference string,
	body []byte,
) identityregistry.JSONSchemaMaterial {
	digest := sha256.Sum256(body)
	return identityregistry.JSONSchemaMaterial{
		Reference: reference, WireReference: wireReference,
		Digest: hex.EncodeToString(digest[:]), Schema: body,
	}
}
