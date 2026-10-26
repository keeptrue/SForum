package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type extensionDatabaseDispositionPhysicalState struct {
	SchemaPresent         bool
	OwnerRolePresent      bool
	RuntimeRolePresent    bool
	MigrationRolesPresent []string
}

func applyExtensionDatabasePhysicalDisposition(
	ctx context.Context,
	tx pgx.Tx,
	request ExtensionDatabaseDispositionRequest,
	identifiers ExtensionDatabaseIdentifiers,
	resourceExisted bool,
	fence extensionDatabaseDispositionFence,
) (extensionDatabaseDispositionProofResource, error) {
	resource, exists, err := loadExtensionDatabaseDispositionResource(
		ctx, tx, request.Artifact.ExtensionID, true,
	)
	if err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	if exists != resourceExisted || (exists && !resource.matches(identifiers)) {
		return extensionDatabaseDispositionProofResource{}, ErrExtensionDatabaseDispositionConflict
	}
	migrationRoles, err := loadExtensionDatabaseDispositionMigrationRoles(
		ctx, tx, request.Artifact.ExtensionID,
	)
	if err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	proof := extensionDatabaseDispositionProofResource{
		Existed: resourceExisted, SchemaName: identifiers.Schema,
		OwnerRoleName: identifiers.OwnerRole, RuntimeRoleName: identifiers.RuntimeRole,
		MigrationRoles:              migrationRoles,
		MigrationRolesPresentBefore: make([]string, 0),
		MigrationRolesPresentAfter:  make([]string, 0),
	}
	if !resourceExisted {
		if len(migrationRoles) != 0 {
			return extensionDatabaseDispositionProofResource{}, ErrExtensionDatabaseDispositionConflict
		}
		if err := validateAbsentExtensionDatabaseResources(ctx, tx, identifiers); err != nil {
			return extensionDatabaseDispositionProofResource{}, err
		}
		return proof, nil
	}

	if err := validateExactActiveDispositionGrant(ctx, tx, request.Artifact); err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	before, err := inspectExtensionDatabaseDispositionPhysicalState(
		ctx, tx, identifiers, migrationRoles, databaseName, true,
	)
	if err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	if !before.SchemaPresent || !before.OwnerRolePresent {
		return extensionDatabaseDispositionProofResource{}, ErrExtensionDatabaseResourceConflict
	}
	proof.SchemaPresentBefore = before.SchemaPresent
	proof.OwnerRolePresentBefore = before.OwnerRolePresent
	proof.RuntimeRolePresentBefore = before.RuntimeRolePresent
	proof.MigrationRolesPresentBefore = before.MigrationRolesPresent

	if err := revokeExtensionDatabaseDispositionLedger(ctx, tx, request.Artifact.ExtensionID, fence); err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	children := append([]string{identifiers.RuntimeRole}, migrationRoles...)
	for _, roleName := range children {
		present := roleName == identifiers.RuntimeRole && before.RuntimeRolePresent
		if roleName != identifiers.RuntimeRole {
			present = containsSortedString(before.MigrationRolesPresent, roleName)
		}
		if !present {
			continue
		}
		if err := disableExtensionDatabaseRuntimeRole(ctx, tx, roleName, databaseName); err != nil {
			return extensionDatabaseDispositionProofResource{}, err
		}
		if err := reassignAndDropExtensionDatabaseRole(
			ctx, tx, roleName, identifiers.OwnerRole, databaseName,
		); err != nil {
			return extensionDatabaseDispositionProofResource{}, err
		}
	}

	schemaRetained := request.CleanupMode == LifecycleBoundaryCleanupPreserve
	if !schemaRetained {
		if _, err := tx.Exec(
			ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{identifiers.Schema}.Sanitize()+` CASCADE`,
		); err != nil {
			return extensionDatabaseDispositionProofResource{}, fmt.Errorf("drop extension database schema: %w", err)
		}
		if err := dropExtensionDatabaseOwnerRole(ctx, tx, identifiers.OwnerRole); err != nil {
			return extensionDatabaseDispositionProofResource{}, err
		}
	}
	if err := markExtensionDatabaseResourceDisposed(
		ctx, tx, resource, schemaRetained,
	); err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}

	after, err := inspectExtensionDatabaseDispositionPhysicalState(
		ctx, tx, identifiers, migrationRoles, databaseName, false,
	)
	if err != nil {
		return extensionDatabaseDispositionProofResource{}, err
	}
	proof.SchemaPresentAfter = after.SchemaPresent
	proof.OwnerRolePresentAfter = after.OwnerRolePresent
	proof.RuntimeRolePresentAfter = after.RuntimeRolePresent
	proof.MigrationRolesPresentAfter = after.MigrationRolesPresent
	if after.RuntimeRolePresent || len(after.MigrationRolesPresent) != 0 ||
		after.SchemaPresent != schemaRetained || after.OwnerRolePresent != schemaRetained {
		return extensionDatabaseDispositionProofResource{}, ErrExtensionDatabaseResourceConflict
	}
	return proof, nil
}

func loadExtensionDatabaseDispositionMigrationRoles(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT plan_digest
		FROM extension_database_migration_plans
		WHERE extension_id = $1
		ORDER BY plan_digest
	`, extensionID)
	if err != nil {
		return nil, fmt.Errorf("load extension database disposition migration roles: %w", err)
	}
	defer rows.Close()
	roles := make([]string, 0)
	for rows.Next() {
		var planDigest string
		if err := rows.Scan(&planDigest); err != nil {
			return nil, err
		}
		roleName, err := ExtensionDatabaseMigrationRoleFor(extensionID, planDigest)
		if err != nil {
			return nil, ErrExtensionDatabaseResourceConflict
		}
		roles = append(roles, roleName)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(roles)
	return roles, nil
}

func validateAbsentExtensionDatabaseResources(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	identifiers ExtensionDatabaseIdentifiers,
) error {
	var physicalResources int
	err := querier.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_namespace WHERE nspname = $1)
		  + (SELECT count(*) FROM pg_roles WHERE rolname IN ($2, $3))
	`, identifiers.Schema, identifiers.OwnerRole, identifiers.RuntimeRole).Scan(&physicalResources)
	if err != nil {
		return fmt.Errorf("inspect absent extension database resources: %w", err)
	}
	if physicalResources != 0 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func inspectExtensionDatabaseDispositionPhysicalState(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
	migrationRoles []string,
	databaseName string,
	validate bool,
) (extensionDatabaseDispositionPhysicalState, error) {
	state := extensionDatabaseDispositionPhysicalState{MigrationRolesPresent: make([]string, 0)}
	var owner string
	err := tx.QueryRow(ctx, `
		SELECT roles.rolname
		FROM pg_namespace
		JOIN pg_roles AS roles ON roles.oid = pg_namespace.nspowner
		WHERE pg_namespace.nspname = $1
	`, identifiers.Schema).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		state.SchemaPresent = false
	} else if err != nil {
		return state, fmt.Errorf("inspect extension database disposition schema: %w", err)
	} else if owner != identifiers.OwnerRole {
		return state, ErrExtensionDatabaseResourceConflict
	} else {
		state.SchemaPresent = true
	}

	state.OwnerRolePresent, err = inspectExtensionDatabaseDispositionRole(
		ctx, tx, identifiers.OwnerRole, identifiers.Schema, "", databaseName, false, false, validate,
	)
	if err != nil {
		return state, err
	}
	state.RuntimeRolePresent, err = inspectExtensionDatabaseDispositionRole(
		ctx, tx, identifiers.RuntimeRole, identifiers.Schema, identifiers.OwnerRole,
		databaseName, true, true, validate,
	)
	if err != nil {
		return state, err
	}
	for _, roleName := range migrationRoles {
		present, inspectErr := inspectExtensionDatabaseDispositionRole(
			ctx, tx, roleName, identifiers.Schema, identifiers.OwnerRole,
			databaseName, true, true, validate,
		)
		if inspectErr != nil {
			return state, inspectErr
		}
		if present {
			state.MigrationRolesPresent = append(state.MigrationRolesPresent, roleName)
		}
	}
	if validate {
		allowedOwnerMembers := make(map[string]struct{}, len(migrationRoles)+1)
		allowedOwnerMembers[identifiers.RuntimeRole] = struct{}{}
		for _, roleName := range migrationRoles {
			allowedOwnerMembers[roleName] = struct{}{}
		}
		if err := validateExtensionDatabaseDispositionRoleMembers(
			ctx, tx, identifiers.OwnerRole, allowedOwnerMembers,
		); err != nil {
			return state, err
		}
		for _, roleName := range append([]string{identifiers.RuntimeRole}, migrationRoles...) {
			if err := validateExtensionDatabaseDispositionRoleMembers(
				ctx, tx, roleName, nil,
			); err != nil {
				return state, err
			}
		}
	}
	return state, nil
}

func inspectExtensionDatabaseDispositionRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	schemaName string,
	allowedMembership string,
	databaseName string,
	allowLogin bool,
	allowExactSearchPath bool,
	validate bool,
) (bool, error) {
	var canLogin, superuser, createDatabase, createRole, replication, bypassRLS bool
	err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(&canLogin, &superuser, &createDatabase, &createRole, &replication, &bypassRLS)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect extension database disposition role: %w", err)
	}
	if !validate {
		return true, nil
	}
	if superuser || createDatabase || createRole || replication || bypassRLS || (!allowLogin && canLogin) {
		return false, ErrExtensionDatabaseResourceConflict
	}
	if err := validateExtensionDatabaseRoleIsolation(
		ctx, tx, roleName, schemaName, allowedMembership, databaseName, allowExactSearchPath,
	); err != nil {
		return false, err
	}
	return true, nil
}

func validateExtensionDatabaseDispositionRoleMembers(
	ctx context.Context,
	tx pgx.Tx,
	grantedRole string,
	allowed map[string]struct{},
) error {
	rows, err := tx.Query(ctx, `
		SELECT member.rolname
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS member ON member.oid = memberships.member
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		WHERE granted.rolname = $1
		ORDER BY member.rolname
	`, grantedRole)
	if err != nil {
		return fmt.Errorf("inspect extension database disposition role members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return err
		}
		if _, ok := allowed[member]; !ok {
			return fmt.Errorf(
				"%w: role %s is inherited by unexpected role %s",
				ErrExtensionDatabaseResourceConflict, grantedRole, member,
			)
		}
	}
	return rows.Err()
}

// validateExactActiveDispositionGrant 校验「本次要抹除的 exact artifact」是否还挂着
// active grant。查询必须带 artifact 条件（version_id / version / digest）：
// extension_database_grants 允许一个扩展同时存在多条 active（唯一索引只约束同一
// artifact，见 202607150022_extension_database_additive_grants.sql:60），正常
// install → upgrade → uninstall 必然留下旧 artifact 的 active grant；只按
// extension_id 取首行会拿到旧行，从而把 purge 永久判成 conflict。
// 无匹配行即通过：该 artifact 没有 active grant，没有需要撤销的授予。
func validateExactActiveDispositionGrant(
	ctx context.Context,
	tx pgx.Tx,
	artifact ExtensionDatabaseArtifact,
) error {
	var versionID int64
	var version, digest string
	err := tx.QueryRow(ctx, `
		SELECT extension_version_id, extension_version, package_digest
		FROM extension_database_grants
		WHERE extension_id = $1 AND status = 'active'
		  AND extension_version_id = $2 AND extension_version = $3 AND package_digest = $4
		FOR UPDATE
	`, artifact.ExtensionID, artifact.VersionID, artifact.Version, artifact.PackageDigest).
		Scan(&versionID, &version, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock active extension database disposition grant: %w", err)
	}
	if versionID != artifact.VersionID || version != artifact.Version || digest != artifact.PackageDigest {
		return ErrExtensionDatabaseArtifactConflict
	}
	return nil
}

func revokeExtensionDatabaseDispositionLedger(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	fence extensionDatabaseDispositionFence,
) error {
	actor, audit := dispositionRevocationActor(fence)
	if _, err := tx.Exec(ctx, `
		UPDATE extension_database_credentials
		SET status = 'revoked', revoked_by_user_id = $2, revoke_audit_event_id = $3,
		    revoked_at = statement_timestamp()
		WHERE extension_id = $1 AND status = 'active'
	`, extensionID, actor, audit); err != nil {
		return fmt.Errorf("revoke extension database disposition credentials: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE extension_database_grants
		SET status = 'revoked', revoked_by_user_id = $2, revoke_audit_event_id = $3,
		    revoked_at = statement_timestamp(), grant_revision = grant_revision + 1,
		    updated_at = statement_timestamp()
		WHERE extension_id = $1 AND status = 'active'
	`, extensionID, actor, audit); err != nil {
		return fmt.Errorf("revoke extension database disposition grants: %w", err)
	}
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_credentials WHERE extension_id = $1 AND status = 'active')
		  + (SELECT count(*) FROM extension_database_grants WHERE extension_id = $1 AND status = 'active')
	`, extensionID).Scan(&active); err != nil {
		return fmt.Errorf("verify extension database disposition credential revoke: %w", err)
	}
	if active != 0 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func reassignAndDropExtensionDatabaseRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	ownerRoleName string,
	databaseName string,
) error {
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	role := pgx.Identifier{roleName}.Sanitize()
	owner := pgx.Identifier{ownerRoleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	for _, query := range []string{
		`REASSIGN OWNED BY ` + role + ` TO ` + owner,
		`REVOKE ` + owner + ` FROM ` + role,
		`DROP OWNED BY ` + role,
		`ALTER ROLE ` + role + ` IN DATABASE ` + database + ` RESET ALL`,
		`DROP ROLE ` + role,
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("drop extension database disposable role %s: %w", roleName, err)
		}
	}
	return nil
}

func dropExtensionDatabaseOwnerRole(ctx context.Context, tx pgx.Tx, roleName string) error {
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	role := pgx.Identifier{roleName}.Sanitize()
	for _, query := range []string{`DROP OWNED BY ` + role, `DROP ROLE ` + role} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("drop extension database owner role %s: %w", roleName, err)
		}
	}
	return nil
}

func markExtensionDatabaseResourceDisposed(
	ctx context.Context,
	tx pgx.Tx,
	resource extensionDatabaseResourceRecord,
	schemaRetained bool,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_resources
		SET status = 'revoked', failure_code = '', schema_retained = $2,
		    revoked_at = COALESCE(revoked_at, statement_timestamp()),
		    resource_revision = resource_revision + 1,
		    updated_at = statement_timestamp()
		WHERE extension_id = $1 AND resource_revision = $3
	`, resource.ExtensionID, schemaRetained, resource.Revision)
	if err != nil {
		return fmt.Errorf("mark extension database resource disposed: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func containsSortedString(values []string, want string) bool {
	index := sort.SearchStrings(values, want)
	return index < len(values) && values[index] == want
}
