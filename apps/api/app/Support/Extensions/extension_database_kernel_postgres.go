package extensionsruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func acquireExtensionDatabaseKernelOwnerAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roles map[string]struct{},
) error {
	if len(roles) == 0 {
		return nil
	}
	var sessionRole string
	if err := tx.QueryRow(ctx, `SELECT session_user`).Scan(&sessionRole); err != nil {
		return fmt.Errorf("inspect kernel owner management identity: %w", err)
	}
	if !validPostgresIdentifier(sessionRole) {
		return ErrExtensionDatabaseResourceConflict
	}
	session := pgx.Identifier{sessionRole}.Sanitize()
	for _, roleName := range sortedExtensionDatabaseRoleNames(roles) {
		role := pgx.Identifier{roleName}.Sanitize()
		if _, err := tx.Exec(ctx,
			`GRANT `+role+` TO `+session+` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		); err != nil {
			return fmt.Errorf("%w: acquire live kernel owner authority: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	return nil
}

func releaseExtensionDatabaseKernelOwnerAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roles map[string]struct{},
) error {
	if len(roles) == 0 {
		return nil
	}
	var sessionRole string
	if err := tx.QueryRow(ctx, `SELECT session_user`).Scan(&sessionRole); err != nil {
		return fmt.Errorf("inspect kernel owner management identity: %w", err)
	}
	if !validPostgresIdentifier(sessionRole) {
		return ErrExtensionDatabaseResourceConflict
	}
	session := pgx.Identifier{sessionRole}.Sanitize()
	for _, roleName := range sortedExtensionDatabaseRoleNames(roles) {
		role := pgx.Identifier{roleName}.Sanitize()
		if _, err := tx.Exec(ctx, `REVOKE `+role+` FROM `+session+` GRANTED BY CURRENT_USER`); err != nil {
			return fmt.Errorf("%w: release live kernel owner authority: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	return nil
}

func sortedExtensionDatabaseRoleNames(roles map[string]struct{}) []string {
	names := make([]string, 0, len(roles))
	for roleName := range roles {
		names = append(names, roleName)
	}
	sort.Strings(names)
	return names
}

// kernel 继承必须锚定 K1 确定性 owner；Core schema 只允许精确存活租约临时持有对象。
func validateExtensionDatabaseKernelCoreState(
	ctx context.Context,
	tx pgx.Tx,
	extraKernelRoles ...string,
) (extensionDatabaseCoreIdentity, map[string]struct{}, error) {
	identity, err := loadExtensionDatabaseCoreIdentity(ctx, tx)
	if err != nil {
		return extensionDatabaseCoreIdentity{}, nil, err
	}
	liveOwners, pendingOwners, err := loadExtensionDatabaseKernelRoles(ctx, tx)
	if err != nil {
		return extensionDatabaseCoreIdentity{}, nil, err
	}
	allowedOwners := make(map[string]struct{}, len(liveOwners)+len(pendingOwners)+len(extraKernelRoles))
	for roleName := range liveOwners {
		allowedOwners[roleName] = struct{}{}
	}
	for roleName := range pendingOwners {
		allowedOwners[roleName] = struct{}{}
	}
	for _, roleName := range extraKernelRoles {
		if !validPostgresIdentifier(roleName) {
			return extensionDatabaseCoreIdentity{}, nil, ErrExtensionDatabaseRegistryInvalid
		}
		allowedOwners[roleName] = struct{}{}
		liveOwners[roleName] = struct{}{}
	}
	if err := validateExtensionDatabaseCoreOwnerRole(ctx, tx, identity, allowedOwners); err != nil {
		return extensionDatabaseCoreIdentity{}, nil, fmt.Errorf("validate kernel Core owner role: %w", err)
	}
	if err := validateExtensionDatabaseKernelCoreObjectOwners(ctx, tx, identity, allowedOwners); err != nil {
		return extensionDatabaseCoreIdentity{}, nil, fmt.Errorf("validate kernel Core object owners: %w", err)
	}
	for roleName := range liveOwners {
		if err := validateExtensionDatabaseKernelLeaseAuthority(ctx, tx, roleName, identity.ownerRole); err != nil {
			return extensionDatabaseCoreIdentity{}, nil, fmt.Errorf("validate live kernel lease %s: %w", roleName, err)
		}
	}
	for roleName := range pendingOwners {
		if err := validateExtensionDatabaseFencedKernelLeaseAuthority(ctx, tx, roleName, identity.ownerRole); err != nil {
			return extensionDatabaseCoreIdentity{}, nil, fmt.Errorf("validate fenced kernel lease %s: %w", roleName, err)
		}
	}
	return identity, allowedOwners, nil
}

func loadExtensionDatabaseKernelRoles(
	ctx context.Context,
	tx pgx.Tx,
) (map[string]struct{}, map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `
			SELECT leases.role_name, leases.status = 'failed'
			FROM extension_database_runtime_leases AS leases
			WHERE (
			  leases.status IN ('active', 'draining')
			  OR (leases.status = 'failed' AND leases.failure_code IN ($1, $2))
			)
			  AND EXISTS (
				SELECT 1
				FROM extension_database_grant_powers AS powers
				WHERE powers.grant_id = leases.grant_id AND powers.power = 'kernel'
			  )
			ORDER BY leases.role_name
		`, extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode,
		extensionDatabaseRuntimeLeaseCleanupPendingExpiredCode)
	if err != nil {
		return nil, nil, fmt.Errorf("load kernel runtime leases: %w", err)
	}
	defer rows.Close()
	live := make(map[string]struct{})
	pending := make(map[string]struct{})
	for rows.Next() {
		var roleName string
		var cleanupPending bool
		if err := rows.Scan(&roleName, &cleanupPending); err != nil {
			return nil, nil, err
		}
		if !validPostgresIdentifier(roleName) {
			return nil, nil, ErrExtensionDatabaseResourceConflict
		}
		if cleanupPending {
			pending[roleName] = struct{}{}
		} else {
			live[roleName] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return live, pending, nil
}

func validateExtensionDatabaseCoreOwnerRole(
	ctx context.Context,
	tx pgx.Tx,
	identity extensionDatabaseCoreIdentity,
	kernelRoles map[string]struct{},
) error {
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil *string
	if err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil::TEXT
		FROM pg_roles WHERE rolname = $1
	`, identity.ownerRole).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	); err != nil {
		return fmt.Errorf("inspect kernel Core owner role: %w", err)
	}
	if canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != -1 || validUntil != nil {
		return ErrExtensionDatabaseResourceConflict
	}

	var inheritedRoles, settings int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = $1)),
		  (SELECT count(*) FROM pg_db_role_setting WHERE setrole = (SELECT oid FROM pg_roles WHERE rolname = $1))
	`, identity.ownerRole).Scan(&inheritedRoles, &settings); err != nil {
		return fmt.Errorf("inspect kernel Core owner isolation: %w", err)
	}
	if inheritedRoles != 0 || settings != 0 {
		return ErrExtensionDatabaseResourceConflict
	}

	expectedMembers := make(map[string]extensionDatabaseMembershipOptions, len(kernelRoles)+1)
	expectedMembers[identity.sessionRole] = extensionDatabaseMembershipOptions{
		admin: true, inherit: true, set: true,
	}
	for roleName := range kernelRoles {
		expectedMembers[roleName] = extensionDatabaseMembershipOptions{inherit: true}
	}
	rows, err := tx.Query(ctx, `
		SELECT member.rolname, count(*),
		       bool_or(memberships.admin_option),
		       bool_or(memberships.inherit_option), bool_or(memberships.set_option)
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE granted.rolname = $1
		GROUP BY member.rolname
		ORDER BY member.rolname
	`, identity.ownerRole)
	if err != nil {
		return fmt.Errorf("inspect kernel Core owner members: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(expectedMembers))
	for rows.Next() {
		var member string
		var membershipRows int
		var actual extensionDatabaseMembershipOptions
		if err := rows.Scan(
			&member, &membershipRows, &actual.admin, &actual.inherit, &actual.set,
		); err != nil {
			return err
		}
		expected, ok := expectedMembers[member]
		if !ok || actual != expected || (member != identity.sessionRole && membershipRows != 1) {
			return ErrExtensionDatabaseResourceConflict
		}
		if _, duplicate := seen[member]; duplicate {
			return ErrExtensionDatabaseResourceConflict
		}
		seen[member] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(expectedMembers) {
		return ErrExtensionDatabaseResourceConflict
	}

	var isolationConflict bool
	if err := tx.QueryRow(ctx, `
		WITH owner AS (SELECT oid FROM pg_roles WHERE rolname = $1)
		SELECT
		  has_database_privilege($1, current_database(), 'CREATE')
		  OR EXISTS (SELECT 1 FROM pg_database, owner WHERE datdba = owner.oid)
		  OR EXISTS (SELECT 1 FROM pg_tablespace, owner WHERE spcowner = owner.oid)
		  OR EXISTS (SELECT 1 FROM pg_extension, owner WHERE extowner = owner.oid)
		  OR EXISTS (
			SELECT 1 FROM pg_namespace, owner
			WHERE nspowner = owner.oid AND nspname NOT IN ($2, $3)
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			CROSS JOIN owner
			WHERE classes.relowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3, 'pg_toast')
			    OR (namespaces.nspname = $2 AND classes.relname LIKE 'river\_%' ESCAPE '\')
			  )
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			CROSS JOIN owner
			WHERE routines.proowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3)
			    OR (namespaces.nspname = $2 AND routines.proname LIKE 'river\_%' ESCAPE '\')
			  )
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			CROSS JOIN owner
			WHERE types.typowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3, 'pg_toast')
			    OR (namespaces.nspname = $2 AND types.typname ~ '^_?river_')
			  )
		  )
	`, identity.ownerRole, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema).Scan(&isolationConflict); err != nil {
		return fmt.Errorf("inspect kernel Core owner physical isolation: %w", err)
	}
	if isolationConflict {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func validateExtensionDatabaseKernelCoreObjectOwners(
	ctx context.Context,
	tx pgx.Tx,
	identity extensionDatabaseCoreIdentity,
	allowedOwners map[string]struct{},
) error {
	checks := []struct {
		kind  string
		query string
	}{
		{"relation", `
			SELECT namespaces.nspname, classes.relname, owners.rolname
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			JOIN pg_roles AS owners ON owners.oid = classes.relowner
			WHERE namespaces.nspname IN ($1, $2)
			ORDER BY namespaces.nspname, classes.relname
		`},
		{"routine", `
			SELECT namespaces.nspname, routines.proname, owners.rolname
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			JOIN pg_roles AS owners ON owners.oid = routines.proowner
			WHERE namespaces.nspname IN ($1, $2)
			ORDER BY namespaces.nspname, routines.proname, routines.oid
		`},
		{"type", `
			SELECT namespaces.nspname, types.typname, owners.rolname
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			JOIN pg_roles AS owners ON owners.oid = types.typowner
			WHERE namespaces.nspname IN ($1, $2)
			ORDER BY namespaces.nspname, types.typname, types.oid
		`},
	}
	for _, check := range checks {
		rows, err := tx.Query(ctx, check.query, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema)
		if err != nil {
			return fmt.Errorf("inspect kernel Core %s owners: %w", check.kind, err)
		}
		for rows.Next() {
			var schema, name, owner string
			if err := rows.Scan(&schema, &name, &owner); err != nil {
				rows.Close()
				return err
			}
			expectedOwner := identity.ownerRole
			isRiver := schema == extensionDatabaseCoreSchema && isExtensionDatabaseRiverCoreObject(check.kind, name)
			if isRiver {
				expectedOwner = identity.sessionRole
			}
			_, allowedKernelOwner := allowedOwners[owner]
			if owner != expectedOwner && (!allowedKernelOwner || isRiver) {
				rows.Close()
				return ErrExtensionDatabaseResourceConflict
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func isExtensionDatabaseRiverCoreObject(kind string, name string) bool {
	if coreauthority.IsRiverObjectName(name) {
		return true
	}
	// River 类型名已有下划线时，PostgreSQL 生成的数组类型会再增加一个下划线。
	return kind == "type" && strings.HasPrefix(name, "_") &&
		coreauthority.IsRiverObjectName(strings.TrimPrefix(name, "_"))
}

func validateExtensionDatabaseKernelLeaseAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	coreOwnerRole string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(coreOwnerRole) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	); err != nil {
		return fmt.Errorf("inspect exact kernel lease role: %w", err)
	}
	if !canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != extensionDatabaseRuntimeLeaseConnectionLimit || validUntil == nil {
		return ErrExtensionDatabaseResourceConflict
	}
	var ledgerExpiresAt *time.Time
	var liveLeaseRows int
	if err := tx.QueryRow(ctx, `
		SELECT max(leases.lease_expires_at), count(*)
		FROM extension_database_runtime_leases AS leases
		WHERE leases.role_name = $1 AND leases.status IN ('active', 'draining')
		  AND EXISTS (
			SELECT 1
			FROM extension_database_grant_powers AS powers
			WHERE powers.grant_id = leases.grant_id AND powers.power = 'kernel'
		  )
	`, roleName).Scan(&ledgerExpiresAt, &liveLeaseRows); err != nil {
		return fmt.Errorf("inspect exact kernel lease expiry ledger: %w", err)
	}
	if liveLeaseRows > 1 || liveLeaseRows == 1 &&
		(ledgerExpiresAt == nil || !validUntil.UTC().Equal(ledgerExpiresAt.UTC())) {
		return ErrExtensionDatabaseResourceConflict
	}
	return validateExtensionDatabaseKernelLeaseCoreIsolation(ctx, tx, roleName, coreOwnerRole)
}

func validateExtensionDatabaseFencedKernelLeaseAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	coreOwnerRole string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(coreOwnerRole) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	); err != nil {
		return fmt.Errorf("inspect fenced kernel lease role: %w", err)
	}
	if canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != extensionDatabaseRuntimeLeaseConnectionLimit || validUntil == nil ||
		!validUntil.UTC().Equal(time.Unix(0, 0).UTC()) {
		return ErrExtensionDatabaseResourceConflict
	}
	var pendingRows int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM extension_database_runtime_leases AS leases
		WHERE leases.role_name = $1 AND leases.status = 'failed'
		  AND leases.failure_code IN ($2, $3)
		  AND EXISTS (
			SELECT 1
			FROM extension_database_grant_powers AS powers
			WHERE powers.grant_id = leases.grant_id AND powers.power = 'kernel'
		  )
	`, roleName, extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode,
		extensionDatabaseRuntimeLeaseCleanupPendingExpiredCode).Scan(&pendingRows); err != nil {
		return fmt.Errorf("inspect fenced kernel lease ledger: %w", err)
	}
	if pendingRows != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return validateExtensionDatabaseKernelLeaseCoreIsolation(ctx, tx, roleName, coreOwnerRole)
}

func validateExtensionDatabaseKernelLeaseCoreIsolation(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	coreOwnerRole string,
) error {

	var canSetCore, canCreateDatabaseObjects, directCoreACL bool
	if err := tx.QueryRow(ctx, `
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
	`, roleName, coreOwnerRole, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema).Scan(
		&canSetCore, &canCreateDatabaseObjects, &directCoreACL,
	); err != nil {
		return fmt.Errorf("inspect exact kernel lease authority: %w", err)
	}
	if canSetCore || canCreateDatabaseObjects || directCoreACL {
		return fmt.Errorf(
			"%w: kernel lease %s has set_core=%t database_create=%t direct_core_acl=%t",
			ErrExtensionDatabaseResourceConflict, roleName,
			canSetCore, canCreateDatabaseObjects, directCoreACL,
		)
	}
	return nil
}
