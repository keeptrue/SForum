package identitycontroller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	p7RoleMappingVersionID      = int64(101)
	p7IdentityMigration         = int64(202607160028)
	p7RoleApprovalMigration     = int64(202607160029)
	p7IdentityRootMigration     = int64(202607160033)
	p7RoleRepairMigration       = int64(202607170034)
	p7PermissionLocaleMigration = int64(202607231001)
)

func TestP7HostOwnedRoleMappingJoined(t *testing.T) {
	extension := p7RoleMappingExtension(t)
	fixture := newP7RoleMappingPostgresFixture(t, extension)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: p7RoleMappingStarter{instanceID: "p7-role-runtime-1"},
	})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	repository := &p7RoleMappingPhaseRepository{ref: extensionsruntime.LifecycleRegistryPublicationRef{
		OperationID: 701, StepID: "lifecycle.enable.05.host.enabled",
		Mode: extensionsruntime.LifecycleBoundaryActivate, Attempt: 1,
	}, phase: extensionsruntime.LifecycleRegistryPublicationSource, pool: fixture.pool}
	store := identityregistry.NewPostgresStore(fixture.pool)
	registry := identityregistry.New()
	boundary := newP7RoleMappingBoundary(t, manager, registry, store, repository)
	request := extensionsruntime.LifecycleBoundaryRequest{
		OperationID: 701, Operation: extensions.LifecycleMachineEnable, Position: 5,
		StepID: "lifecycle.enable.05.host.enabled", Attempt: 1,
		TargetExtension: extension,
		TargetBinding: extensions.LifecycleRuntimeBinding{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		ActorUserID: fixture.adminUserID, AuditEventID: fixture.lifecycleAuditID,
	}
	transaction, err := boundary.PrepareLifecycleRegistryPublication(
		ctx, request, extensionsruntime.LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertAuthorityCounts(t, 0, 0, 0)
	if _, err := manager.BeginDrain(runtime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(ctx, runtime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err := transaction.Inspect(ctx); err != nil || state != extensionsruntime.LifecycleBoundaryTransactionTarget {
		t.Fatalf("lifecycle publication state=%q err=%v", state, err)
	}
	pending := fixture.pendingSuggestion(t)
	if pending.PermissionKey != p7RoleMappingPermission(extension.ID) || pending.RoleKey != identity.RoleOperator ||
		pending.ApprovalState != identityregistry.RoleSuggestionPending || pending.Revision != 1 || pending.Applied {
		t.Fatalf("pending role suggestion=%#v", pending)
	}
	fixture.assertAuthorityCounts(t, 1, 0, 0)
	fixture.assertPublicationEvidence(t)

	// Restart from durable Identity Registry state before any Host role decision.
	restartedStore := identityregistry.NewPostgresStore(fixture.pool)
	restartedRegistry := identityregistry.New()
	restarted := newP7RoleMappingBoundary(
		t,
		extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
			Starter: p7RoleMappingStarter{instanceID: "p7-role-runtime-2"},
		}),
		restartedRegistry,
		restartedStore,
		nil,
	)
	if err := restarted.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	restored, found := restartedRegistry.SnapshotPublication(extension.ID)
	if !found || restored.Artifact.ExtensionID != extension.ID ||
		len(restored.Permissions) != 1 || len(restored.Permissions[0].RecommendedRoles) != 1 ||
		restored.Permissions[0].RecommendedRoles[0] != identity.RoleOperator {
		t.Fatalf("restored identity publication=%#v found=%t", restored, found)
	}
	fixture.assertAuthorityCounts(t, 1, 0, 0)

	app := newRoleSuggestionControllerTestApp(t, restartedStore, map[int64]identity.Actor{
		fixture.adminUserID: {
			ID: fixture.adminUserID, Status: identity.UserStatusActive,
			RoleKeys:    []string{identity.RoleSuperAdmin},
			Permissions: map[string]bool{identity.PermissionRoleManage: true},
		},
		fixture.operatorUserID: {
			ID: fixture.operatorUserID, Status: identity.UserStatusActive,
			RoleKeys: []string{identity.RoleOperator}, Permissions: map[string]bool{},
		},
	})
	operatorCookie := loginRoleSuggestionControllerUser(t, app, fixture.operatorUserID)
	response := roleSuggestionControllerRequest(
		t, app, http.MethodPost,
		fmt.Sprintf("/api/v1/roles/suggestions/%d/decision", pending.ID), operatorCookie,
		[]byte(`{"expectedRevision":1,"approvalState":"approved"}`),
	)
	assertRoleSuggestionError(t, response, http.StatusForbidden, "permission.denied")
	fixture.assertAuthorityCounts(t, 1, 0, 0)

	adminCookie := loginRoleSuggestionControllerUser(t, app, fixture.adminUserID)
	for _, cookie := range []*http.Cookie{nil, adminCookie} {
		response = roleSuggestionControllerPATRequest(
			t, app, http.MethodPost,
			fmt.Sprintf("/api/v1/roles/suggestions/%d/decision", pending.ID), cookie,
			[]byte(`{"expectedRevision":1,"approvalState":"approved"}`),
		)
		assertRoleSuggestionError(t, response, http.StatusForbidden, "identity.role_suggestion.cookie_required")
	}
	fixture.assertAuthorityCounts(t, 1, 0, 0)

	response = roleSuggestionControllerRequest(
		t, app, http.MethodGet,
		"/api/v1/roles/suggestions?approvalState=pending&roleKey=operator&permissionKey="+
			p7RoleMappingPermission(extension.ID)+"&ownerExtensionId="+extension.ID,
		adminCookie, nil,
	)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("role suggestion list status=%d", response.StatusCode)
	}
	var page roleSuggestionTestEnvelope[identityregistry.RoleSuggestionPage]
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(page.Data.Items) != 1 || page.Data.Items[0].ID != pending.ID || page.Data.Items[0].Applied {
		t.Fatalf("HTTP pending role suggestions=%#v", page.Data)
	}

	response = roleSuggestionControllerRequest(
		t, app, http.MethodPost,
		fmt.Sprintf("/api/v1/roles/suggestions/%d/decision", pending.ID), adminCookie,
		[]byte(`{"expectedRevision":1,"approvalState":"approved"}`),
	)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("role suggestion decision status=%d", response.StatusCode)
	}
	var decision roleSuggestionTestEnvelope[identityregistry.RoleSuggestion]
	if err := json.NewDecoder(response.Body).Decode(&decision); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if decision.Data.ID != pending.ID || decision.Data.Revision != 2 ||
		decision.Data.ApprovalState != identityregistry.RoleSuggestionApproved || !decision.Data.Applied ||
		decision.Data.DecidedByUserID != fixture.adminUserID || decision.Data.AppliedByUserID != fixture.adminUserID ||
		decision.Data.DecisionAuditEventID <= 0 ||
		decision.Data.AppliedAuditEventID != decision.Data.DecisionAuditEventID {
		t.Fatalf("approved role suggestion=%#v", decision.Data)
	}
	fixture.assertAuthorityCounts(t, 1, 1, 1)
	fixture.assertApprovalEvidence(t, decision.Data)

	auditsBeforeReplay := fixture.auditCount(t)
	response = roleSuggestionControllerRequest(
		t, app, http.MethodPost,
		fmt.Sprintf("/api/v1/roles/suggestions/%d/decision", pending.ID), adminCookie,
		[]byte(`{"expectedRevision":1,"approvalState":"approved"}`),
	)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("role suggestion replay status=%d", response.StatusCode)
	}
	response.Body.Close()
	fixture.assertAuthorityCounts(t, 1, 1, 1)
	if got := fixture.auditCount(t); got != auditsBeforeReplay {
		t.Fatalf("approval replay duplicated audit: before=%d after=%d", auditsBeforeReplay, got)
	}
}

func p7RoleMappingExtension(t *testing.T) extensions.Extension {
	t.Helper()
	id := "fixture.identity.role-mapping"
	packagePath := t.TempDir()
	backendBody := []byte("p7-role-mapping-runtime")
	backendDigest := sha256.Sum256(backendBody)
	backendDigestText := hex.EncodeToString(backendDigest[:])
	backendPath := filepath.Join(packagePath, "bin", "plugin")
	if err := os.MkdirAll(filepath.Dir(backendPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backendPath, backendBody, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := extensions.Manifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              id, Name: "P7 Role Mapping Fixture", Description: "Host-owned role mapping joined fixture.",
		URL: "https://example.com/p7-role-mapping", Author: extensions.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
		Backend: extensions.ManifestBackend{
			Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: backendDigestText, HostAPIVersion: "sforum.host@2",
		},
		Lifecycle: &extensions.ManifestLifecycle{ContractVersion: id + ".lifecycle@1"},
		PackageFiles: []extensions.ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: "bin/plugin", Digest: backendDigestText,
		}},
		PermissionDefinitions: []extensions.ManifestPermissionDefinition{{
			Key: p7RoleMappingPermission(id), ContractVersion: p7RoleMappingPermission(id) + "@1",
			Label:            extensions.LocalizedText{Default: "Manage role mapping fixture"},
			Description:      extensions.LocalizedText{Default: "Manage the joined P7 fixture."},
			RecommendedRoles: []string{identity.RoleOperator}, AssignmentPolicy: "host",
		}},
	}
	manifest = extensionmanifest.Normalize(manifest)
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, extensionmanifest.ManifestFileName), manifestBody, 0o600); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: id, Name: manifest.Name, Type: extensions.TypePlugin, Version: manifest.Version,
		Status: extensions.StatusEnabled, ActiveVersionID: p7RoleMappingVersionID,
		PackagePath: packagePath, PackageDigest: packageDigest, Manifest: manifest,
	}
}

func p7RoleMappingPermission(extensionID string) string { return extensionID + ".manage" }

func newP7RoleMappingBoundary(
	t *testing.T,
	manager *extensionsruntime.Manager,
	registry *identityregistry.Registry,
	store identityregistry.PublicationStore,
	repository extensionsruntime.LifecycleRegistryPublicationRepository,
) *extensionsruntime.PostgresLifecycleBoundaryRegistries {
	t.Helper()
	routeSchemas, err := extensionopenapi.NewRouteSchemaContractPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	return extensionsruntime.NewPostgresLifecycleBoundaryRegistries(
		extensionsruntime.LifecycleRegistryBoundaryConfig{
			Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil),
			Routes: routes.NewRegistry(), RouteSchemas: routeSchemas, Services: hostapi.NewServiceRegistry(),
			Identity: registry, IdentityStore: store,
		},
	)
}

type p7RoleMappingStarter struct{ instanceID string }

func (s p7RoleMappingStarter) Start(ctx context.Context, _ extensions.Extension) (extensionsruntime.RouteTarget, error) {
	if err := ctx.Err(); err != nil {
		return extensionsruntime.RouteTarget{}, err
	}
	return extensionsruntime.RouteTarget{BaseURL: "http://127.0.0.1:1", InstanceID: s.instanceID}, nil
}

func (p7RoleMappingStarter) Stop(context.Context, extensions.Extension) error { return nil }

type p7RoleMappingPhaseRepository struct {
	mu    sync.Mutex
	ref   extensionsruntime.LifecycleRegistryPublicationRef
	phase extensionsruntime.LifecycleRegistryPublicationPhase
	input extensionsruntime.PrepareLifecycleRegistryPublicationInput
	pool  *pgxpool.Pool
}

func (r *p7RoleMappingPhaseRepository) PrepareLifecycleRegistryPublication(
	ctx context.Context,
	input extensionsruntime.PrepareLifecycleRegistryPublicationInput,
) (extensionsruntime.LifecycleRegistryPublicationRef, error) {
	if err := ctx.Err(); err != nil {
		return extensionsruntime.LifecycleRegistryPublicationRef{}, err
	}
	if input.SourceDigest == "" || input.TargetDigest == "" {
		return extensionsruntime.LifecycleRegistryPublicationRef{}, fmt.Errorf("missing lifecycle registry digest")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.input = input
	return r.ref, nil
}

func (r *p7RoleMappingPhaseRepository) InspectLifecycleRegistryPublication(
	ctx context.Context,
	ref extensionsruntime.LifecycleRegistryPublicationRef,
) (extensionsruntime.LifecycleRegistryPublicationPhase, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref != r.ref {
		return "", fmt.Errorf("unexpected lifecycle registry ref")
	}
	return r.phase, nil
}

func (r *p7RoleMappingPhaseRepository) MoveLifecycleRegistryPublication(
	ctx context.Context,
	ref extensionsruntime.LifecycleRegistryPublicationRef,
	phase extensionsruntime.LifecycleRegistryPublicationPhase,
	apply func() error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	valid := ref == r.ref
	r.mu.Unlock()
	if !valid {
		return fmt.Errorf("unexpected lifecycle registry ref")
	}
	if err := apply(); err != nil {
		return err
	}
	r.mu.Lock()
	r.phase = phase
	r.mu.Unlock()
	return nil
}

func (r *p7RoleMappingPhaseRepository) MoveLifecycleRegistryPublicationTx(
	ctx context.Context,
	ref extensionsruntime.LifecycleRegistryPublicationRef,
	phase extensionsruntime.LifecycleRegistryPublicationPhase,
	apply func(pgx.Tx) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	valid := ref == r.ref && r.pool != nil
	r.mu.Unlock()
	if !valid {
		return fmt.Errorf("unexpected lifecycle registry ref")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := apply(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	r.phase = phase
	r.mu.Unlock()
	return nil
}

type p7RoleMappingPostgresFixture struct {
	ctx              context.Context
	pool             *pgxpool.Pool
	adminUserID      int64
	operatorUserID   int64
	lifecycleAuditID int64
	extension        extensions.Extension
}

func newP7RoleMappingPostgresFixture(t *testing.T, extension extensions.Extension) *p7RoleMappingPostgresFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("p7_role_mapping_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") }

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
	if err := seedP7RoleMappingBaseTables(ctx, db); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if err := applyP7RoleMappingMigrations(ctx, db); err != nil {
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
	fixture := &p7RoleMappingPostgresFixture{ctx: ctx, pool: pool, extension: extension}
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

func seedP7RoleMappingBaseTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY, username TEXT NOT NULL, username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL, email_lower TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'banned'))
		);
		CREATE TABLE roles (
			id BIGSERIAL PRIMARY KEY, key TEXT NOT NULL UNIQUE,
			is_enabled BOOLEAN NOT NULL DEFAULT TRUE
		);
		CREATE TABLE permissions (
			key TEXT PRIMARY KEY, module TEXT NOT NULL, description TEXT NOT NULL DEFAULT ''
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
			id BIGSERIAL PRIMARY KEY, actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			target_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY, type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'installed'
				CHECK (status IN ('installed', 'enabled', 'disabled')), active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY, extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL, manifest JSONB NOT NULL, package_path TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		INSERT INTO roles (key) VALUES ('super_admin'), ('member'), ('operator');
		INSERT INTO permissions (key, module, description) VALUES
			('role.manage', 'identity', 'Manage role permissions'),
			('topic.create', 'forum', 'Create topics');
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'role.manage' FROM roles WHERE key = 'super_admin';
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'topic.create' FROM roles WHERE key = 'operator';
	`)
	return err
}

func applyP7RoleMappingMigrations(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	for _, version := range []int64{
		p7IdentityMigration, p7RoleApprovalMigration, p7IdentityRootMigration, p7RoleRepairMigration,
		p7PermissionLocaleMigration,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			return err
		}
	}
	return nil
}

func (f *p7RoleMappingPostgresFixture) seed() error {
	manifestBody, err := json.Marshal(f.extension.Manifest)
	if err != nil {
		return err
	}
	adminName := "p7_role_admin_" + fmt.Sprint(time.Now().UnixNano())
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'P7 role admin', 'active') RETURNING id
	`, adminName, adminName+"@example.test").Scan(&f.adminUserID); err != nil {
		return err
	}
	operatorName := "p7_role_operator_" + fmt.Sprint(time.Now().UnixNano())
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'P7 operator', 'active') RETURNING id
	`, operatorName, operatorName+"@example.test").Scan(&f.operatorUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'super_admin'
	`, f.adminUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'operator'
	`, f.operatorUserID); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', $2, 'enabled')
	`, f.extension.ID, f.extension.Name); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, $2, $3, $4::jsonb, $5, $6)
	`, f.extension.ActiveVersionID, f.extension.ID, f.extension.Version,
		string(manifestBody), f.extension.PackagePath, f.extension.PackageDigest); err != nil {
		return err
	}
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, f.extension.ID, f.extension.ActiveVersionID); err != nil {
		return err
	}
	return f.pool.QueryRow(f.ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'extension.lifecycle.enable', jsonb_build_object('extensionId', $2::text))
		RETURNING id
	`, f.adminUserID, f.extension.ID).Scan(&f.lifecycleAuditID)
}

func (f *p7RoleMappingPostgresFixture) pendingSuggestion(t *testing.T) identityregistry.RoleSuggestion {
	t.Helper()
	items, err := identityregistry.NewPostgresStore(f.pool).ListRoleSuggestions(
		f.ctx,
		identityregistry.RoleSuggestionFilter{
			ApprovalState:    identityregistry.RoleSuggestionPending,
			OwnerExtensionID: f.extension.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("pending role suggestions=%#v", items)
	}
	return items[0]
}

func (f *p7RoleMappingPostgresFixture) assertAuthorityCounts(t *testing.T, suggestions, mappings, grants int) {
	t.Helper()
	queries := []struct {
		name string
		sql  string
		args []any
		want int
	}{
		{"suggestions", `SELECT count(*) FROM extension_permission_role_suggestions WHERE owner_extension_id = $1`, []any{f.extension.ID}, suggestions},
		{"mappings", `SELECT count(*) FROM role_permissions JOIN roles ON roles.id = role_permissions.role_id WHERE roles.key = 'operator' AND permission_key = $1`, []any{p7RoleMappingPermission(f.extension.ID)}, mappings},
		{"grants", `SELECT count(*) FROM extension_permission_role_grants WHERE owner_extension_id = $1`, []any{f.extension.ID}, grants},
		{"operator baseline", `SELECT count(*) FROM role_permissions JOIN roles ON roles.id = role_permissions.role_id WHERE roles.key = 'operator' AND permission_key = 'topic.create'`, nil, 1},
	}
	for _, query := range queries {
		var got int
		if err := f.pool.QueryRow(f.ctx, query.sql, query.args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != query.want {
			t.Fatalf("%s=%d want=%d", query.name, got, query.want)
		}
	}
}

func (f *p7RoleMappingPostgresFixture) assertPublicationEvidence(t *testing.T) {
	t.Helper()
	for name, query := range map[string]string{
		"owner":       `SELECT count(*) FROM extension_identity_registry_owners WHERE owner_extension_id = $1`,
		"declaration": `SELECT count(*) FROM extension_identity_registry_declarations WHERE owner_extension_id = $1 AND registry_state = 'active'`,
		"catalog":     `SELECT count(*) FROM extension_permission_catalog WHERE owner_extension_id = $1`,
		"root":        `SELECT count(*) FROM extension_identity_registry_publications WHERE owner_extension_id = $1 AND registry_state = 'active'`,
	} {
		var count int
		if err := f.pool.QueryRow(f.ctx, query, f.extension.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s publication rows=%d", name, count)
		}
	}
}

func (f *p7RoleMappingPostgresFixture) assertApprovalEvidence(
	t *testing.T,
	decision identityregistry.RoleSuggestion,
) {
	t.Helper()
	var permissionKey, ownerExtensionID, roleKey string
	var actorUserID, auditEventID int64
	if err := f.pool.QueryRow(f.ctx, `
		SELECT permission_key, owner_extension_id, role_key,
		       applied_by_user_id, applied_audit_event_id
		FROM extension_permission_role_grants WHERE suggestion_id = $1
	`, decision.ID).Scan(&permissionKey, &ownerExtensionID, &roleKey, &actorUserID, &auditEventID); err != nil {
		t.Fatal(err)
	}
	if permissionKey != decision.PermissionKey || ownerExtensionID != f.extension.ID ||
		roleKey != identity.RoleOperator || actorUserID != f.adminUserID ||
		auditEventID != decision.DecisionAuditEventID {
		t.Fatalf("role grant evidence=%q/%q/%q/%d/%d", permissionKey, ownerExtensionID, roleKey, actorUserID, auditEventID)
	}
	var auditMatches int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events
		WHERE id = $1 AND actor_user_id = $2
		  AND action = 'identity.role_suggestion.approve'
		  AND metadata @> jsonb_build_object(
			'suggestionId', $3::bigint, 'permissionKey', $4::text,
			'ownerExtensionId', $5::text, 'roleKey', 'operator',
			'expectedRevision', 1, 'approvalState', 'approved',
			'rolePermissionAdded', true, 'roleGrantApplied', true
		  )
	`, decision.DecisionAuditEventID, f.adminUserID, decision.ID,
		decision.PermissionKey, f.extension.ID).Scan(&auditMatches); err != nil {
		t.Fatal(err)
	}
	if auditMatches != 1 {
		t.Fatalf("decision audit evidence=%d", auditMatches)
	}
}

func (f *p7RoleMappingPostgresFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

var (
	_ extensionsruntime.Starter                                = p7RoleMappingStarter{}
	_ extensionsruntime.LifecycleRegistryPublicationRepository = (*p7RoleMappingPhaseRepository)(nil)
)
