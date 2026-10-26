package migrator

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

const coreKernelLeaseConnectionLimit = 1

type coreAuthorityQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type coreKernelLeaseCandidate struct {
	ExtensionID          string
	LeaseID              string
	RoleName             string
	RuntimeInstanceID    string
	ExtensionVersion     string
	Status               string
	FailureCode          string
	LeaseExpiresAt       time.Time
	ExtensionOwnerRole   string
	ExtensionSchema      string
	ExtensionRuntimeRole string
	OwnSchema            bool
	DeclarationExact     bool
}

type coreKernelMembershipOptions struct {
	Admin   bool
	Inherit bool
	Set     bool
}

type coreKernelGrantedMembership struct {
	Grantor string
	coreKernelMembershipOptions
}

type coreKernelTemporaryMembership struct {
	RoleName string
	Before   []coreKernelGrantedMembership
	Current  *coreKernelMembershipOptions
}

func lockCorePhysicalAuthority(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtext(current_database()),
			hashtext($1)
		)
	`, coreauthority.PhysicalAuthorityLockName); err != nil {
		return fmt.Errorf("lock Core physical database authority: %w", err)
	}
	return nil
}

func lockCorePhysicalAuthoritySession(ctx context.Context, connection *sql.Conn) error {
	if _, err := connection.ExecContext(ctx, `
		SELECT pg_advisory_lock(
			hashtext(current_database()),
			hashtext($1)
		)
	`, coreauthority.PhysicalAuthorityLockName); err != nil {
		return fmt.Errorf("lock Core physical database authority session: %w", err)
	}
	return nil
}

func unlockCorePhysicalAuthoritySession(ctx context.Context, connection *sql.Conn) error {
	var unlocked bool
	if err := connection.QueryRowContext(ctx, `
		SELECT pg_advisory_unlock(
			hashtext(current_database()),
			hashtext($1)
		)
	`, coreauthority.PhysicalAuthorityLockName).Scan(&unlocked); err != nil {
		return fmt.Errorf("unlock Core physical database authority session: %w", err)
	}
	if !unlocked {
		return fmt.Errorf("%w: Core physical database authority session lock was not held", ErrCoreAuthorityConflict)
	}
	return nil
}

// Core migrations reclaim supported objects from exact kernel leases before
// Goose starts. Unsupported objects remain with a fenced lease for explicit
// operator cleanup, but forged ownership always blocks startup.
func reconcileCoreKernelAuthority(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	targetVersion string,
) (error, error) {
	ready, err := coreKernelAuthorityFactsReady(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("inspect kernel authority ledger availability: %w", err)
	}
	if !ready {
		return nil, nil
	}
	candidates, err := loadCoreKernelLeaseCandidates(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := validateCoreKernelCandidateIdentities(authority, candidates); err != nil {
		return nil, err
	}
	var compatibilityErr error
	if strings.TrimSpace(targetVersion) != "" {
		target, err := semver.StrictNewVersion(strings.TrimSpace(targetVersion))
		if err != nil {
			return nil, fmt.Errorf("invalid target SForum version %q: %w", targetVersion, err)
		}
		declarations, err := loadPhysicalCoreDatabaseCompatibilityBlockers(ctx, tx)
		if err != nil {
			return nil, err
		}
		if err := validatePhysicalCoreCompatibilityDeclarations(declarations); err != nil {
			return nil, err
		}
		compatibilityErr = rejectCoreCompatibilityDeclarations(target, declarations)
	}
	for _, candidate := range candidates {
		if err := validateCoreKernelLeaseCandidate(ctx, tx, authority, candidate); err != nil {
			return nil, fmt.Errorf("validate kernel lease %s: %w", candidate.RoleName, err)
		}
	}
	if err := validateCoreKernelOwnerMembers(ctx, tx, authority, candidates); err != nil {
		return nil, err
	}
	if err := validateCoreKernelOwnershipSources(ctx, tx, authority, candidates); err != nil {
		return nil, err
	}
	temporary, err := acquireCoreKernelLeaseOwnership(ctx, tx, authority, candidates)
	if err != nil {
		return nil, err
	}
	if err := transferCoreObjectOwnership(ctx, tx, authority); err != nil {
		return nil, err
	}
	if err := releaseCoreKernelLeaseOwnership(ctx, tx, authority, temporary); err != nil {
		return nil, err
	}
	return compatibilityErr, nil
}

func validatePhysicalCoreCompatibilityDeclarations(declarations []coreCompatibilityBlocker) error {
	for _, declaration := range declarations {
		if !declaration.DeclarationExact {
			return fmt.Errorf(
				"%w: physical %s authority for extension %s version %s does not match its exact artifact declaration",
				ErrCoreAuthorityConflict, declaration.Authority,
				declaration.ExtensionID, declaration.ExtensionVersion,
			)
		}
	}
	return nil
}

func coreKernelAuthorityFactsReady(ctx context.Context, queryer coreAuthorityQueryer) (bool, error) {
	var ready bool
	err := queryer.QueryRowContext(ctx, `
		SELECT to_regclass('public.extension_database_runtime_leases') IS NOT NULL
		   AND to_regclass('public.extension_database_grant_powers') IS NOT NULL
		   AND to_regclass('public.extension_database_grants') IS NOT NULL
		   AND to_regclass('public.extension_database_resources') IS NOT NULL
	`).Scan(&ready)
	return ready, err
}

func loadCoreKernelLeaseCandidates(
	ctx context.Context,
	queryer coreAuthorityQueryer,
) ([]coreKernelLeaseCandidate, error) {
	rows, err := queryer.QueryContext(ctx, `
			SELECT leases.extension_id, leases.lease_id, leases.role_name,
			       leases.runtime_instance_id, leases.extension_version,
			       leases.status, leases.failure_code,
			       leases.lease_expires_at, resources.owner_role_name,
			       resources.schema_name, resources.runtime_role_name,
		       EXISTS (
		         SELECT 1
		         FROM public.extension_database_grant_powers AS own_schema
		         WHERE own_schema.grant_id = leases.grant_id
		           AND own_schema.power = 'own_schema'
		       ),
			       CASE
			         WHEN jsonb_typeof(declared.powers) = 'array' THEN
			           declared.powers @> persisted.powers
			           AND persisted.powers @> declared.powers
			           AND jsonb_array_length(declared.powers) = jsonb_array_length(persisted.powers)
			           AND (
			             COALESCE(extensions.source = 'builtin' AND extensions.is_system, FALSE)
			             OR COALESCE(trusted.declaration_exact, FALSE)
			           )
			         ELSE FALSE
			       END
		FROM public.extension_database_runtime_leases AS leases
		JOIN public.extension_database_grants AS grants
		  ON grants.id = leases.grant_id
		 AND grants.extension_id = leases.extension_id
		 AND grants.extension_version_id = leases.extension_version_id
		 AND grants.extension_version = leases.extension_version
		 AND grants.package_digest = leases.package_digest
			JOIN public.extension_database_resources AS resources
			  ON resources.extension_id = leases.extension_id
			LEFT JOIN public.extensions AS extensions
			  ON extensions.id = leases.extension_id
		JOIN public.extension_database_grant_powers AS kernel
		  ON kernel.grant_id = leases.grant_id AND kernel.power = 'kernel'
		LEFT JOIN public.extension_versions AS versions
		  ON versions.id = leases.extension_version_id
		 AND versions.extension_id = leases.extension_id
		 AND versions.version = leases.extension_version
		 AND versions.package_digest = leases.package_digest
		CROSS JOIN LATERAL (
		  SELECT CASE versions.manifest #>> '{database,authority}'
		    WHEN 'own_schema' THEN '["own_schema"]'::jsonb
		    WHEN 'core_views' THEN '["own_schema","core_views"]'::jsonb
		    WHEN 'host_commands' THEN '["own_schema","core_views","host_commands"]'::jsonb
		    WHEN 'raw_core' THEN '["own_schema","core_views","host_commands","raw_core"]'::jsonb
		    WHEN 'kernel' THEN '["own_schema","core_views","host_commands","raw_core","kernel"]'::jsonb
		    ELSE COALESCE(versions.manifest #> '{database,grants}', '[]'::jsonb)
		  END AS powers
		) AS declared
			CROSS JOIN LATERAL (
			  SELECT COALESCE(jsonb_agg(powers.power ORDER BY powers.ordinal), '[]'::jsonb) AS powers
			  FROM public.extension_database_grant_powers AS powers
			  WHERE powers.grant_id = leases.grant_id
			) AS persisted
			LEFT JOIN LATERAL (
			  SELECT COALESCE(bool_or(
			    normalized_trust.database_grants @> persisted.powers
			    AND persisted.powers @> normalized_trust.database_grants
			    AND jsonb_array_length(normalized_trust.database_grants) =
			        jsonb_array_length(persisted.powers)
			    AND COALESCE(trust.impact_document #>> '{database,coreCompatibility}', '') =
			        COALESCE(versions.manifest #>> '{database,coreCompatibility}', '')
			  ), FALSE) AS declaration_exact
			  FROM public.extension_trust_grants AS trust
			  CROSS JOIN LATERAL (
			    SELECT CASE trust.impact_document #>> '{database,authority}'
			      WHEN 'own_schema' THEN '["own_schema"]'::jsonb
			      WHEN 'core_views' THEN '["own_schema","core_views"]'::jsonb
			      WHEN 'host_commands' THEN '["own_schema","core_views","host_commands"]'::jsonb
			      WHEN 'raw_core' THEN '["own_schema","core_views","host_commands","raw_core"]'::jsonb
			      WHEN 'kernel' THEN '["own_schema","core_views","host_commands","raw_core","kernel"]'::jsonb
			      ELSE COALESCE(trust.impact_document #> '{database,grants}', '[]'::jsonb)
			    END AS database_grants
			  ) AS normalized_trust
			  WHERE trust.extension_id = leases.extension_id
			    AND trust.extension_version = leases.extension_version
			    AND trust.package_digest = leases.package_digest
			    AND trust.action = 'enable'
			) AS trusted ON TRUE
		WHERE leases.status IN ('active', 'draining')
		   OR (leases.status = 'failed' AND leases.failure_code IN ($1, $2))
		ORDER BY leases.role_name
	`, coreauthority.KernelCleanupPendingRevokeCode, coreauthority.KernelCleanupPendingExpiredCode)
	if err != nil {
		return nil, fmt.Errorf("load exact kernel lease candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]coreKernelLeaseCandidate, 0)
	for rows.Next() {
		var candidate coreKernelLeaseCandidate
		if err := rows.Scan(
			&candidate.ExtensionID,
			&candidate.LeaseID,
			&candidate.RoleName,
			&candidate.RuntimeInstanceID,
			&candidate.ExtensionVersion,
			&candidate.Status,
			&candidate.FailureCode,
			&candidate.LeaseExpiresAt,
			&candidate.ExtensionOwnerRole,
			&candidate.ExtensionSchema,
			&candidate.ExtensionRuntimeRole,
			&candidate.OwnSchema,
			&candidate.DeclarationExact,
		); err != nil {
			return nil, fmt.Errorf("scan exact kernel lease candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read exact kernel lease candidates: %w", err)
	}
	return candidates, nil
}

func validateCoreKernelCandidateIdentities(
	authority coreMigrationAuthority,
	candidates []coreKernelLeaseCandidate,
) error {
	roles := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		identifiers, err := coreauthority.ExtensionDatabaseIdentifiersFor(candidate.ExtensionID)
		if err != nil {
			return fmt.Errorf("%w: kernel lease has an invalid extension identity", ErrCoreAuthorityConflict)
		}
		leaseRole, err := coreauthority.ExtensionDatabaseRuntimeLeaseRoleFor(
			candidate.ExtensionID, candidate.RuntimeInstanceID, candidate.LeaseID,
		)
		if err != nil || candidate.RoleName != leaseRole ||
			candidate.ExtensionOwnerRole != identifiers.OwnerRole ||
			candidate.ExtensionSchema != identifiers.Schema ||
			candidate.ExtensionRuntimeRole != identifiers.RuntimeRole {
			return fmt.Errorf(
				"%w: kernel lease %s does not match its Host-derived database identity",
				ErrCoreAuthorityConflict, candidate.RoleName,
			)
		}
		if !candidate.DeclarationExact {
			return fmt.Errorf(
				"%w: kernel lease %s does not match its exact artifact database declaration",
				ErrCoreAuthorityConflict, candidate.RoleName,
			)
		}
		if candidate.RoleName == authority.SessionRole || candidate.RoleName == authority.OwnerRole {
			return fmt.Errorf("%w: kernel lease reuses a Core authority role", ErrCoreAuthorityConflict)
		}
		if _, duplicate := roles[candidate.RoleName]; duplicate {
			return fmt.Errorf("%w: duplicate kernel lease role %s", ErrCoreAuthorityConflict, candidate.RoleName)
		}
		roles[candidate.RoleName] = struct{}{}
	}
	for _, candidate := range candidates {
		if !candidate.OwnSchema {
			continue
		}
		_, ownerIsLease := roles[candidate.ExtensionOwnerRole]
		if candidate.ExtensionOwnerRole == authority.SessionRole ||
			candidate.ExtensionOwnerRole == authority.OwnerRole || ownerIsLease ||
			coreauthority.IsCoreSchema(candidate.ExtensionSchema) {
			return fmt.Errorf(
				"%w: kernel lease %s has an unsafe extension database resource",
				ErrCoreAuthorityConflict, candidate.RoleName,
			)
		}
	}
	return nil
}

func acquireCoreKernelLeaseOwnership(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	candidates []coreKernelLeaseCandidate,
) ([]coreKernelTemporaryMembership, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	session := pgx.Identifier{authority.SessionRole}.Sanitize()
	temporary := make([]coreKernelTemporaryMembership, 0, len(candidates))
	for _, candidate := range candidates {
		before, err := loadCoreGrantedMemberships(ctx, tx, candidate.RoleName, authority.SessionRole)
		if err != nil {
			return nil, err
		}
		state := coreKernelTemporaryMembership{RoleName: candidate.RoleName, Before: before}
		for _, membership := range before {
			if membership.Grantor == authority.SessionRole {
				options := membership.coreKernelMembershipOptions
				state.Current = &options
				break
			}
		}
		role := pgx.Identifier{candidate.RoleName}.Sanitize()
		if _, err := tx.ExecContext(ctx,
			`GRANT `+role+` TO `+session+` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		); err != nil {
			return nil, fmt.Errorf("acquire kernel lease ownership authority: %w", err)
		}
		temporary = append(temporary, state)
	}
	return temporary, nil
}

func releaseCoreKernelLeaseOwnership(
	ctx context.Context,
	tx *sql.Tx,
	authority coreMigrationAuthority,
	temporary []coreKernelTemporaryMembership,
) error {
	session := pgx.Identifier{authority.SessionRole}.Sanitize()
	for index := len(temporary) - 1; index >= 0; index-- {
		state := temporary[index]
		role := pgx.Identifier{state.RoleName}.Sanitize()
		if _, err := tx.ExecContext(ctx,
			`REVOKE `+role+` FROM `+session+` GRANTED BY CURRENT_USER`,
		); err != nil {
			return fmt.Errorf("release kernel lease ownership authority: %w", err)
		}
		if state.Current != nil {
			if _, err := tx.ExecContext(ctx,
				`GRANT `+role+` TO `+session+
					` WITH ADMIN `+strconv.FormatBool(state.Current.Admin)+
					`, INHERIT `+strconv.FormatBool(state.Current.Inherit)+
					`, SET `+strconv.FormatBool(state.Current.Set),
			); err != nil {
				return fmt.Errorf("restore preexisting kernel lease membership: %w", err)
			}
		}
		after, err := loadCoreGrantedMemberships(ctx, tx, state.RoleName, authority.SessionRole)
		if err != nil {
			return err
		}
		if !slices.Equal(state.Before, after) {
			return fmt.Errorf(
				"%w: kernel lease membership changed during Core ownership transfer",
				ErrCoreAuthorityConflict,
			)
		}
	}
	return nil
}

func loadCoreGrantedMemberships(
	ctx context.Context,
	tx *sql.Tx,
	grantedRole string,
	memberRole string,
) ([]coreKernelGrantedMembership, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT grantors.rolname, memberships.admin_option,
		       memberships.inherit_option, memberships.set_option
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		JOIN pg_roles AS grantors ON grantors.oid = memberships.grantor
		WHERE granted.rolname = $1 AND member.rolname = $2
		ORDER BY grantors.rolname, memberships.admin_option,
		         memberships.inherit_option, memberships.set_option
	`, grantedRole, memberRole)
	if err != nil {
		return nil, fmt.Errorf("inspect grantor-specific kernel lease membership: %w", err)
	}
	defer rows.Close()
	memberships := make([]coreKernelGrantedMembership, 0, 2)
	for rows.Next() {
		var membership coreKernelGrantedMembership
		if err := rows.Scan(
			&membership.Grantor,
			&membership.Admin,
			&membership.Inherit,
			&membership.Set,
		); err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return memberships, nil
}
