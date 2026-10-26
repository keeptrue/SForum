package migrator

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func loadPhysicalCoreDatabaseCompatibilityBlockers(
	ctx context.Context,
	queryer coreAuthorityQueryer,
) ([]coreCompatibilityBlocker, error) {
	ready, err := coreKernelAuthorityFactsReady(ctx, queryer)
	if err != nil {
		return nil, fmt.Errorf("inspect physical kernel compatibility ledger: %w", err)
	}
	if !ready {
		return nil, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT DISTINCT leases.extension_id, leases.extension_version,
		       CASE WHEN EXISTS (
		         SELECT 1
		         FROM public.extension_database_grant_powers AS kernel
		         WHERE kernel.grant_id = leases.grant_id AND kernel.power = 'kernel'
		       ) THEN 'kernel' ELSE 'raw_core' END,
		       COALESCE(versions.manifest #>> '{database,coreCompatibility}', ''),
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
		JOIN public.extension_database_grant_powers AS high_risk
		  ON high_risk.grant_id = leases.grant_id
		 AND high_risk.power IN ('raw_core', 'kernel')
		JOIN pg_roles AS physical_roles ON physical_roles.rolname = leases.role_name
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
		   OR (
		     leases.status = 'failed'
		     AND leases.failure_code IN ($1, $2)
		     AND EXISTS (
		       SELECT 1
		       FROM public.extension_database_grant_powers AS pending_kernel
		       WHERE pending_kernel.grant_id = leases.grant_id
		         AND pending_kernel.power = 'kernel'
		     )
		   )
		ORDER BY leases.extension_id, leases.extension_version
	`, coreauthority.KernelCleanupPendingRevokeCode, coreauthority.KernelCleanupPendingExpiredCode)
	if err != nil {
		return nil, fmt.Errorf("load physical kernel compatibility declarations: %w", err)
	}
	defer rows.Close()
	blockers := make([]coreCompatibilityBlocker, 0)
	for rows.Next() {
		var blocker coreCompatibilityBlocker
		if err := rows.Scan(
			&blocker.ExtensionID,
			&blocker.ExtensionVersion,
			&blocker.Authority,
			&blocker.Constraint,
			&blocker.DeclarationExact,
		); err != nil {
			return nil, fmt.Errorf("scan physical kernel compatibility declaration: %w", err)
		}
		blockers = append(blockers, blocker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read physical kernel compatibility declarations: %w", err)
	}
	return blockers, nil
}
