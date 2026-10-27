package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresExtensionDatabaseRegistryCredentialLifecycleAndIsolation(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.registry.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	packageDigest := hex.EncodeToString(digestBytes[:])
	artifact := insertExtensionDatabaseRegistryFixture(t, ctx, pool, extensionID, packageDigest)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	first, err := registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 101, AuditEventID: 201,
	})
	if err != nil {
		t.Fatalf("provision own-schema credential: %v", err)
	}
	if first.CredentialRevision != 1 || first.Password == "" ||
		first.SchemaName != identifiers.Schema || first.OwnerRoleName != identifiers.OwnerRole ||
		first.RoleName != identifiers.RuntimeRole || first.SearchPath != identifiers.Schema+",pg_catalog" {
		t.Fatalf("unexpected first credential: %#v", first)
	}

	firstConnection, err := connectExtensionDatabaseCredential(ctx, pool, first)
	if err != nil {
		t.Fatalf("connect with provisioned credential: %v", err)
	}
	var searchPath, currentUser string
	if err := firstConnection.QueryRow(ctx, `SHOW search_path`).Scan(&searchPath); err != nil {
		t.Fatal(err)
	}
	if err := firstConnection.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(searchPath, " ", "") != identifiers.Schema+",pg_catalog" || currentUser != identifiers.RuntimeRole {
		t.Fatalf("credential session is not scoped: search_path=%q current_user=%q", searchPath, currentUser)
	}
	if _, err := firstConnection.Exec(ctx, `CREATE TABLE credential_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatalf("create own-schema table: %v", err)
	}
	if _, err := firstConnection.Exec(ctx, `INSERT INTO credential_probe (id, note) VALUES (1, 'retained')`); err != nil {
		t.Fatalf("write own-schema table: %v", err)
	}
	if _, err := firstConnection.Exec(ctx, `SELECT id FROM public.extensions LIMIT 1`); !isInsufficientPrivilege(err) {
		t.Fatalf("own-schema role read core table, err=%v", err)
	}
	if err := firstConnection.Close(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := registry.RotateOwnSchemaCredential(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 102, AuditEventID: 202,
	})
	if err != nil {
		t.Fatalf("rotate own-schema credential: %v", err)
	}
	if second.CredentialRevision != 2 || second.Password == first.Password || second.RoleName != first.RoleName {
		t.Fatalf("unexpected rotated credential: first=%#v second=%#v", first, second)
	}
	if stale, staleErr := connectExtensionDatabaseCredential(ctx, pool, first); staleErr == nil {
		_ = stale.Close(ctx)
		t.Fatal("old credential remained valid after rotation")
	}
	secondConnection, err := connectExtensionDatabaseCredential(ctx, pool, second)
	if err != nil {
		t.Fatalf("connect with rotated credential: %v", err)
	}
	var retained string
	if err := secondConnection.QueryRow(ctx, `SELECT note FROM credential_probe WHERE id = 1`).Scan(&retained); err != nil {
		t.Fatalf("read retained own-schema data: %v", err)
	}
	if retained != "retained" {
		t.Fatalf("unexpected retained value %q", retained)
	}

	if err := registry.RevokeOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 103, AuditEventID: 203,
	}); err != nil {
		t.Fatalf("revoke own-schema credential: %v", err)
	}
	if _, err := secondConnection.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("open credential session survived explicit revoke")
	}
	_ = secondConnection.Close(context.Background())
	if revoked, revokedErr := connectExtensionDatabaseCredential(ctx, pool, second); revokedErr == nil {
		_ = revoked.Close(ctx)
		t.Fatal("revoked credential opened a new connection")
	}

	snapshot, err := registry.InspectOwnSchemaGrant(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "revoked" || snapshot.ActiveCredentialRevision != 2 ||
		snapshot.SchemaName != identifiers.Schema {
		t.Fatalf("unexpected revoked grant snapshot: %#v", snapshot)
	}
	var retainedAfterRevoke string
	query := `SELECT note FROM ` + pgx.Identifier{identifiers.Schema, "credential_probe"}.Sanitize() + ` WHERE id = 1`
	if err := pool.QueryRow(ctx, query).Scan(&retainedAfterRevoke); err != nil {
		t.Fatalf("schema/data were not retained on revoke: %v", err)
	}
	if retainedAfterRevoke != "retained" {
		t.Fatalf("unexpected retained value after revoke %q", retainedAfterRevoke)
	}

	var activeCredentials, plaintextMatches int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'active'),
		       count(*) FILTER (WHERE credential_fingerprint IN ($2, $3))
		FROM extension_database_credentials WHERE extension_id = $1
	`, extensionID, first.Password, second.Password).Scan(&activeCredentials, &plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if activeCredentials != 0 || plaintextMatches != 0 {
		t.Fatalf("credential ledger retained active/plaintext secrets: active=%d plaintext=%d", activeCredentials, plaintextMatches)
	}
}

func TestPostgresExtensionDatabaseRegistryRejectsPreexistingRoleMembership(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.membership.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(
		t, ctx, pool, extensionID, hex.EncodeToString(digestBytes[:]),
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	highDigest := sha256.Sum256([]byte(extensionID + ":high"))
	highRole := "sforum_ext_test_high_" + hex.EncodeToString(highDigest[:10])
	if _, err := pool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{highRole}.Sanitize()+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()+` NOLOGIN`); err != nil {
		_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{highRole}.Sanitize())
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `GRANT SELECT ON public.extensions TO `+pgx.Identifier{highRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `GRANT `+pgx.Identifier{highRole}.Sanitize()+` TO `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `REVOKE `+pgx.Identifier{highRole}.Sanitize()+` FROM `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
		cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)
		_, _ = pool.Exec(context.Background(), `DROP OWNED BY `+pgx.Identifier{highRole}.Sanitize())
		_, _ = pool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{highRole}.Sanitize())
	}()

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	_, err = registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 301, AuditEventID: 401,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("preexisting inherited core authority was not rejected: %v", err)
	}
	var remainsMember, canLogin bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_has_role($1, $2, 'MEMBER'), rolcanlogin
		FROM pg_roles WHERE rolname = $1
	`, identifiers.RuntimeRole, highRole).Scan(&remainsMember, &canLogin); err != nil {
		t.Fatal(err)
	}
	if !remainsMember || canLogin {
		t.Fatalf("failed provision mutated the preexisting role: member=%v login=%v", remainsMember, canLogin)
	}
	var resourceRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_database_resources WHERE extension_id = $1
	`, extensionID).Scan(&resourceRows); err != nil {
		t.Fatal(err)
	}
	if resourceRows != 0 {
		t.Fatalf("failed role validation persisted %d resource rows", resourceRows)
	}
}

func TestPostgresExtensionDatabaseRegistryRejectsPreexistingRoleOwnership(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.ownership.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(
		t, ctx, pool, extensionID, hex.EncodeToString(digestBytes[:]),
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	tableDigest := sha256.Sum256([]byte(extensionID + ":owned-table"))
	tableName := "p5_owned_" + hex.EncodeToString(tableDigest[:8])
	if _, err := pool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE `+pgx.Identifier{"public", tableName}.Sanitize()+` (id BIGINT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE `+pgx.Identifier{"public", tableName}.Sanitize()+` OWNER TO `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+pgx.Identifier{"public", tableName}.Sanitize())
		cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)
	}()

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	_, err = registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 302, AuditEventID: 402,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("preexisting ownership outside the plugin schema was not rejected: %v", err)
	}
	var owner string
	if err := pool.QueryRow(ctx, `
		SELECT roles.rolname
		FROM pg_class
		JOIN pg_roles AS roles ON roles.oid = pg_class.relowner
		WHERE pg_class.oid = to_regclass($1)
	`, "public."+tableName).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != identifiers.RuntimeRole {
		t.Fatalf("failed provision took ownership of the preexisting object: %q", owner)
	}
}

func TestPostgresExtensionDatabaseRegistryRejectsPreexistingRoleSettings(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.settings.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(
		t, ctx, pool, extensionID, hex.EncodeToString(digestBytes[:]),
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRole := pgx.Identifier{identifiers.RuntimeRole}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE ROLE `+runtimeRole+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER ROLE `+runtimeRole+` SET search_path TO public, pg_catalog`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `ALTER ROLE `+runtimeRole+` RESET ALL`)
		cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)
	}()

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	_, err = registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 303, AuditEventID: 403,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("preexisting global role setting was not rejected: %v", err)
	}
	var setting string
	if err := pool.QueryRow(ctx, `
		SELECT unnest(rolconfig) FROM pg_roles WHERE rolname = $1
	`, identifiers.RuntimeRole).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(setting, " ", "") != "search_path=public,pg_catalog" {
		t.Fatalf("failed provision mutated the preexisting role setting: %q", setting)
	}
}

func insertExtensionDatabaseRegistryFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID string,
	packageDigest string,
) ExtensionDatabaseArtifact {
	t.Helper()
	manifest := extensions.Manifest{
		ID: extensionID, Name: "P5 registry fixture", Version: "1.0.0", Type: extensions.TypePlugin,
		Database: &extensions.ManifestDatabase{
			ContractVersion: extensionID + ".database@1", Authority: "own_schema",
			Schema: "logical_schema", Role: "logical_role",
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'P5 registry fixture', 'installed')
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.0', $2::jsonb, $3, $4)
		RETURNING id
	`, extensionID, manifestJSON, t.TempDir(), packageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return ExtensionDatabaseArtifact{
		ExtensionID: extensionID, Version: "1.0.0", VersionID: versionID, PackageDigest: packageDigest,
	}
}

func connectExtensionDatabaseCredential(
	ctx context.Context,
	pool *pgxpool.Pool,
	credential ExtensionDatabaseCredential,
) (*pgx.Conn, error) {
	config := pool.Config().ConnConfig.Copy()
	config.User = credential.RoleName
	config.Password = credential.Password
	config.Database = credential.DatabaseName
	delete(config.RuntimeParams, "search_path")
	delete(config.RuntimeParams, "role")
	return pgx.ConnectConfig(ctx, config)
}

func isPostgresErrorCode(err error, code string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == code
}

func isInsufficientPrivilege(err error) bool {
	return isPostgresErrorCode(err, "42501")
}

func cleanupExtensionDatabaseRegistryFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	artifact ExtensionDatabaseArtifact,
	identifiers ExtensionDatabaseIdentifiers,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, identifiers.RuntimeRole)
	rows, _ := pool.Query(ctx, `SELECT role_name FROM extension_database_runtime_leases WHERE extension_id = $1`, artifact.ExtensionID)
	if rows != nil {
		for rows.Next() {
			var roleName string
			if rows.Scan(&roleName) == nil && validPostgresIdentifier(roleName) {
				_, _ = pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, roleName)
				_, _ = pool.Exec(ctx, `DROP OWNED BY `+pgx.Identifier{roleName}.Sanitize())
				_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{roleName}.Sanitize())
			}
		}
		rows.Close()
	}
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_runtime_leases WHERE extension_id = $1`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_credentials WHERE extension_id = $1`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_grant_powers WHERE grant_id IN (SELECT id FROM extension_database_grants WHERE extension_id = $1)`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_grants WHERE extension_id = $1`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_resources WHERE extension_id = $1`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, artifact.ExtensionID)
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{identifiers.Schema}.Sanitize()+` CASCADE`)
	_, _ = pool.Exec(ctx, `DROP OWNED BY `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
	_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{identifiers.RuntimeRole}.Sanitize())
	_, _ = pool.Exec(ctx, `DROP OWNED BY `+pgx.Identifier{identifiers.OwnerRole}.Sanitize())
	_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{identifiers.OwnerRole}.Sanitize())
}
