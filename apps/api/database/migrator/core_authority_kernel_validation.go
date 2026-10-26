package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func validateCoreKernelLeaseCandidate(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidate coreKernelLeaseCandidate,
) error {
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles WHERE rolname = $1
	`, candidate.RoleName).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: exact kernel lease role is missing", ErrCoreAuthorityConflict)
	}
	if err != nil {
		return fmt.Errorf("inspect exact kernel lease role: %w", err)
	}
	pending := candidate.Status == "failed"
	unsafeAttributes := !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != coreKernelLeaseConnectionLimit || !validUntil.Valid
	if pending {
		unsafeAttributes = unsafeAttributes || canLogin ||
			!validUntil.Time.UTC().Equal(time.Unix(0, 0).UTC())
	} else {
		unsafeAttributes = unsafeAttributes || !canLogin ||
			!validUntil.Time.UTC().Equal(candidate.LeaseExpiresAt.UTC())
	}
	if unsafeAttributes {
		return fmt.Errorf("%w: exact kernel lease role has unsafe attributes", ErrCoreAuthorityConflict)
	}
	if err := validateCoreKernelExtensionResource(ctx, tx, authority, candidate); err != nil {
		return err
	}
	if err := validateCoreKernelLeaseMemberships(ctx, tx, authority, candidate); err != nil {
		return err
	}
	if err := validateCoreKernelLeaseMembers(ctx, tx, authority, candidate); err != nil {
		return err
	}
	return validateCoreKernelLeaseIsolation(ctx, tx, authority, candidate)
}

func validateCoreKernelExtensionResource(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidate coreKernelLeaseCandidate,
) error {
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles
		WHERE rolname = $1
	`, candidate.ExtensionOwnerRole).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: exact extension database owner role is missing", ErrCoreAuthorityConflict)
	}
	if err != nil {
		return fmt.Errorf("inspect exact extension database owner role: %w", err)
	}
	if canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != -1 || validUntil.Valid {
		return fmt.Errorf("%w: exact extension database owner role has unsafe attributes", ErrCoreAuthorityConflict)
	}

	var schemaOwner string
	if err := tx.QueryRowContext(ctx, `
		SELECT owners.rolname
		FROM pg_namespace AS schemas
		JOIN pg_roles AS owners ON owners.oid = schemas.nspowner
		WHERE schemas.nspname = $1
	`, candidate.ExtensionSchema).Scan(&schemaOwner); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: exact extension database schema is missing", ErrCoreAuthorityConflict)
	} else if err != nil {
		return fmt.Errorf("inspect exact extension database schema owner: %w", err)
	} else if schemaOwner != candidate.ExtensionOwnerRole {
		return fmt.Errorf("%w: exact extension database schema owner drift", ErrCoreAuthorityConflict)
	}

	var inheritsRole bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM pg_auth_members AS memberships
		  JOIN pg_roles AS member ON member.oid = memberships.member
		  WHERE member.rolname = $1
		)
	`, candidate.ExtensionOwnerRole).Scan(&inheritsRole); err != nil {
		return fmt.Errorf("inspect exact extension database owner memberships: %w", err)
	}
	canSetCore, canCreateDatabaseObjects, directCoreACL, err := inspectCoreRoleIsolation(
		ctx, tx, candidate.ExtensionOwnerRole, authority.OwnerRole,
	)
	if err != nil {
		return err
	}
	if inheritsRole || canSetCore || canCreateDatabaseObjects || directCoreACL {
		return fmt.Errorf(
			"%w: extension owner inherits_role=%t set_core=%t database_create=%t direct_core_acl=%t",
			ErrCoreAuthorityConflict, inheritsRole, canSetCore, canCreateDatabaseObjects, directCoreACL,
		)
	}
	return nil
}

func validateCoreKernelLeaseMemberships(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidate coreKernelLeaseCandidate,
) error {
	expected := map[string]coreKernelMembershipOptions{
		authority.OwnerRole: {Inherit: true},
	}
	if candidate.OwnSchema {
		expected[candidate.ExtensionOwnerRole] = coreKernelMembershipOptions{Inherit: true, Set: true}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT granted.rolname, count(*),
		       bool_or(memberships.admin_option),
		       bool_or(memberships.inherit_option),
		       bool_or(memberships.set_option)
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE member.rolname = $1
		GROUP BY granted.rolname
		ORDER BY granted.rolname
	`, candidate.RoleName)
	if err != nil {
		return fmt.Errorf("inspect kernel lease inherited roles: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var granted string
		var count int
		var actual coreKernelMembershipOptions
		if err := rows.Scan(&granted, &count, &actual.Admin, &actual.Inherit, &actual.Set); err != nil {
			return err
		}
		want, ok := expected[granted]
		if !ok || count != 1 || actual != want {
			return fmt.Errorf("%w: kernel lease inherited-role membership drift", ErrCoreAuthorityConflict)
		}
		seen[granted] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: kernel lease inherited-role membership is incomplete", ErrCoreAuthorityConflict)
	}
	return nil
}

func validateCoreKernelLeaseMembers(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidate coreKernelLeaseCandidate,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT member.rolname
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE granted.rolname = $1
		ORDER BY member.rolname
	`, candidate.RoleName)
	if err != nil {
		return fmt.Errorf("inspect kernel lease members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return err
		}
		if member != authority.SessionRole {
			return fmt.Errorf(
				"%w: role %s inherits exact kernel lease %s",
				ErrCoreAuthorityConflict, member, candidate.RoleName,
			)
		}
	}
	return rows.Err()
}

func validateCoreKernelLeaseIsolation(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidate coreKernelLeaseCandidate,
) error {
	canSetCore, canCreateDatabaseObjects, directCoreACL, err := inspectCoreRoleIsolation(
		ctx, tx, candidate.RoleName, authority.OwnerRole,
	)
	if err != nil {
		return err
	}
	if canSetCore || canCreateDatabaseObjects || directCoreACL {
		return fmt.Errorf(
			"%w: kernel lease has set_core=%t database_create=%t direct_core_acl=%t",
			ErrCoreAuthorityConflict, canSetCore, canCreateDatabaseObjects, directCoreACL,
		)
	}
	return nil
}

func inspectCoreRoleIsolation(
	ctx context.Context,
	tx *sql.Tx,
	roleName string,
	coreOwnerRole string,
) (bool, bool, bool, error) {
	var canSetCore, canCreateDatabaseObjects, directCoreACL bool
	if err := tx.QueryRowContext(ctx, `
			SELECT pg_has_role($1, $2, 'SET'),
		       has_database_privilege($1, current_database(), 'CREATE'),
		       EXISTS (
		         SELECT 1
		         FROM pg_namespace AS namespaces
		         CROSS JOIN LATERAL aclexplode(namespaces.nspacl) AS privileges
		         WHERE namespaces.nspname IN ($3, $4)
		           AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		           AND privileges.grantor <> privileges.grantee
		       )
		       OR EXISTS (
		         SELECT 1
		         FROM pg_class AS classes
		         JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
		         CROSS JOIN LATERAL aclexplode(classes.relacl) AS privileges
		         WHERE namespaces.nspname IN ($3, $4)
		           AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		           AND privileges.grantor <> privileges.grantee
		       )
		       OR EXISTS (
		         SELECT 1
		         FROM pg_attribute AS attributes
		         JOIN pg_class AS classes ON classes.oid = attributes.attrelid
		         JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
		         CROSS JOIN LATERAL aclexplode(attributes.attacl) AS privileges
		         WHERE namespaces.nspname IN ($3, $4)
		           AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		           AND privileges.grantor <> privileges.grantee
		       )
		       OR EXISTS (
		         SELECT 1
		         FROM pg_proc AS routines
		         JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		         CROSS JOIN LATERAL aclexplode(routines.proacl) AS privileges
		         WHERE namespaces.nspname IN ($3, $4)
		           AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		           AND privileges.grantor <> privileges.grantee
		       )
		       OR EXISTS (
		         SELECT 1
		         FROM pg_type AS types
		         JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
		         CROSS JOIN LATERAL aclexplode(types.typacl) AS privileges
		         WHERE namespaces.nspname IN ($3, $4)
		           AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		           AND privileges.grantor <> privileges.grantee
		       )
		`, roleName, coreOwnerRole,
		coreauthority.PublicSchema, coreauthority.StableCoreViewSchema).Scan(
		&canSetCore, &canCreateDatabaseObjects, &directCoreACL,
	); err != nil {
		return false, false, false, fmt.Errorf("inspect database role Core isolation: %w", err)
	}
	return canSetCore, canCreateDatabaseObjects, directCoreACL, nil
}

func validateCoreKernelOwnerMembers(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidates []coreKernelLeaseCandidate,
) error {
	expected := make(map[string]coreKernelMembershipOptions, len(candidates)+1)
	expected[authority.SessionRole] = coreKernelMembershipOptions{Admin: true, Inherit: true, Set: true}
	for _, candidate := range candidates {
		expected[candidate.RoleName] = coreKernelMembershipOptions{Inherit: true}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT member.rolname, count(*),
		       bool_or(memberships.admin_option),
		       bool_or(memberships.inherit_option),
		       bool_or(memberships.set_option)
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE granted.rolname = $1
		GROUP BY member.rolname
		ORDER BY member.rolname
	`, authority.OwnerRole)
	if err != nil {
		return fmt.Errorf("inspect Core owner kernel members: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var member string
		var count int
		var actual coreKernelMembershipOptions
		if err := rows.Scan(&member, &count, &actual.Admin, &actual.Inherit, &actual.Set); err != nil {
			return err
		}
		want, ok := expected[member]
		if !ok || actual != want || member != authority.SessionRole && count != 1 {
			return fmt.Errorf("%w: Core owner member %s is not exact", ErrCoreAuthorityConflict, member)
		}
		seen[member] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: Core owner member set is incomplete", ErrCoreAuthorityConflict)
	}
	return nil
}

func validateCoreKernelOwnershipSources(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidates []coreKernelLeaseCandidate,
) error {
	if err := validateCoreKernelRiverOwnership(ctx, tx, authority); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		authority.SessionRole: {},
		authority.OwnerRole:   {},
	}
	for _, candidate := range candidates {
		allowed[candidate.RoleName] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT dependencies.classid::regclass::TEXT,
		       pg_describe_object(
		         dependencies.classid, dependencies.objid, dependencies.objsubid
		       ),
		       identified.object_names[1], identified.object_names[2],
		       owners.rolname
		FROM pg_shdepend AS dependencies
		JOIN pg_roles AS owners ON owners.oid = dependencies.refobjid
		CROSS JOIN LATERAL pg_identify_object_as_address(
		  dependencies.classid, dependencies.objid, dependencies.objsubid
		) AS identified
		WHERE dependencies.refclassid = 'pg_authid'::regclass
		  AND dependencies.deptype = 'o'
		  AND dependencies.dbid = (
		    SELECT oid FROM pg_database WHERE datname = current_database()
		  )
		  AND identified.object_names[1] IN ($1, $2)
		ORDER BY dependencies.classid, dependencies.objid, dependencies.objsubid
	`, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema)
	if err != nil {
		return fmt.Errorf("census Core ownership sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var catalog, object, schema, owner string
		var name sql.NullString
		if err := rows.Scan(&catalog, &object, &schema, &name, &owner); err != nil {
			return err
		}
		if schema == coreauthority.PublicSchema && name.Valid &&
			isCoreKernelRiverObject(catalog, name.String) && owner != authority.SessionRole {
			return fmt.Errorf(
				"%w: River object %s is owned by unexpected role %s",
				ErrCoreAuthorityConflict, object, owner,
			)
		}
		if _, ok := allowed[owner]; ok {
			continue
		}
		if catalog == "pg_namespace" && owner == "pg_database_owner" && object == "schema public" {
			continue
		}
		return fmt.Errorf(
			"%w: Core object %s is owned by unexpected role %s",
			ErrCoreAuthorityConflict, object, owner,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read Core ownership census: %w", err)
	}
	return nil
}

func validateCoreKernelRiverOwnership(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
) error {
	checks := []struct {
		kind  string
		query string
	}{
		{"relation", `
			SELECT classes.relname, owners.rolname
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			JOIN pg_roles AS owners ON owners.oid = classes.relowner
			WHERE namespaces.nspname = $1
			  AND classes.relname ~ '^_?river_'
			  AND owners.rolname <> $2
			ORDER BY classes.relname
			LIMIT 1
		`},
		{"routine", `
			SELECT routines.proname, owners.rolname
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			JOIN pg_roles AS owners ON owners.oid = routines.proowner
			WHERE namespaces.nspname = $1
			  AND routines.proname ~ '^_?river_'
			  AND owners.rolname <> $2
			ORDER BY routines.proname, routines.oid
			LIMIT 1
		`},
		{"type", `
			SELECT types.typname, owners.rolname
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			JOIN pg_roles AS owners ON owners.oid = types.typowner
			WHERE namespaces.nspname = $1
			  AND types.typname ~ '^_{0,2}river_'
			  AND owners.rolname <> $2
			ORDER BY types.typname, types.oid
			LIMIT 1
		`},
	}
	for _, check := range checks {
		var object, owner string
		err := tx.QueryRowContext(
			ctx, check.query, coreauthority.PublicSchema, authority.SessionRole,
		).Scan(&object, &owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect River %s ownership: %w", check.kind, err)
		}
		return fmt.Errorf(
			"%w: River %s %s is owned by unexpected role %s",
			ErrCoreAuthorityConflict, check.kind, object, owner,
		)
	}
	return nil
}

func isCoreKernelRiverObject(catalog string, name string) bool {
	switch catalog {
	case "pg_class", "pg_proc":
		return coreauthority.IsRiverObjectName(name)
	case "pg_type":
		return coreauthority.IsRiverObjectName(name) ||
			strings.HasPrefix(name, "_") && coreauthority.IsRiverObjectName(strings.TrimPrefix(name, "_"))
	default:
		return false
	}
}
