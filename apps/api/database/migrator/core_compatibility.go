package migrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

var ErrCoreUpgradeIncompatible = errors.New("core upgrade is incompatible with an enabled raw database extension")

type coreCompatibilityBlocker struct {
	ExtensionID      string
	ExtensionVersion string
	Authority        string
	Constraint       string
	DeclarationExact bool
}

// checkCoreUpgradeCompatibility rejects a Core target that an enabled raw
// database extension does not declare compatible.
//
// declared reports whether targetVersion is a real declaration. When it is
// false the value is the untagged-development fallback sentinel, which carries
// no intent: comparing it against extension declarations would reject every
// extension declaring >=3.x on a machine that merely has no reachable v* tag.
// "Unknown" is not "incompatible", so the check returns before running any SQL.
func checkCoreUpgradeCompatibility(
	ctx context.Context,
	db coreAuthorityQueryer,
	targetVersion string,
	declared bool,
) error {
	if !declared {
		return nil
	}
	target, err := semver.StrictNewVersion(strings.TrimSpace(targetVersion))
	if err != nil {
		return fmt.Errorf("invalid target SForum version %q: %w", targetVersion, err)
	}
	ready, err := coreCompatibilityFactsReady(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect core compatibility schema: %w", err)
	}
	if !ready {
		// Databases predating executable V3 trust cannot contain a live raw-core
		// grant, so there is no compatibility declaration to enforce yet.
		return nil
	}

	rows, err := db.QueryContext(ctx, `
		WITH enabled_database_extensions AS (
		  SELECT extensions.id, extensions.source, extensions.is_system,
		         extension_versions.version, extension_versions.package_digest,
		         extension_versions.manifest,
		         COALESCE(extension_versions.manifest #>> '{database,authority}', 'additive') AS authority,
		         CASE extension_versions.manifest #>> '{database,authority}'
		           WHEN 'own_schema' THEN '["own_schema"]'::jsonb
		           WHEN 'core_views' THEN '["own_schema","core_views"]'::jsonb
		           WHEN 'host_commands' THEN '["own_schema","core_views","host_commands"]'::jsonb
		           WHEN 'raw_core' THEN '["own_schema","core_views","host_commands","raw_core"]'::jsonb
		           WHEN 'kernel' THEN '["own_schema","core_views","host_commands","raw_core","kernel"]'::jsonb
		           ELSE COALESCE(extension_versions.manifest #> '{database,grants}', '[]'::jsonb)
		         END AS database_grants
			FROM public.extensions
			JOIN public.extension_versions ON extension_versions.id = extensions.active_version_id
		WHERE extensions.type = 'plugin'
		  AND extensions.status = 'enabled'
		)
			SELECT enabled.id, enabled.version, enabled.authority,
			       COALESCE(enabled.manifest #>> '{database,coreCompatibility}', ''),
			       CASE
			         WHEN enabled.source = 'builtin' AND enabled.is_system THEN TRUE
			         ELSE COALESCE(trusted.declaration_exact, FALSE)
			       END
			FROM enabled_database_extensions AS enabled
			LEFT JOIN LATERAL (
			  SELECT
			    COALESCE(bool_or(
			      normalized_trust.database_grants @> enabled.database_grants
			      AND enabled.database_grants @> normalized_trust.database_grants
			      AND jsonb_array_length(normalized_trust.database_grants) =
			          jsonb_array_length(enabled.database_grants)
			    ), FALSE) AS powers_exact,
			    COALESCE(bool_or(
			      normalized_trust.database_grants @> enabled.database_grants
			      AND enabled.database_grants @> normalized_trust.database_grants
			      AND jsonb_array_length(normalized_trust.database_grants) =
			          jsonb_array_length(enabled.database_grants)
			      AND COALESCE(trust.impact_document #>> '{database,coreCompatibility}', '') =
			          COALESCE(enabled.manifest #>> '{database,coreCompatibility}', '')
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
			  WHERE trust.extension_id = enabled.id
			    AND trust.extension_version = enabled.version
			    AND trust.package_digest = enabled.package_digest
			    AND trust.action = 'enable'
			    AND trust.revoked_at IS NULL
			) AS trusted ON TRUE
			WHERE enabled.database_grants ?| ARRAY['raw_core', 'kernel']
			  AND (
			    (enabled.source = 'builtin' AND enabled.is_system)
			    OR (
			      enabled.source = 'uploaded' AND NOT enabled.is_system
			      AND COALESCE(trusted.powers_exact, FALSE)
			    )
			  )
		ORDER BY enabled.id
	`)
	if err != nil {
		return fmt.Errorf("load raw database compatibility declarations: %w", err)
	}
	declarations := make([]coreCompatibilityBlocker, 0)
	for rows.Next() {
		var declaration coreCompatibilityBlocker
		if err := rows.Scan(
			&declaration.ExtensionID,
			&declaration.ExtensionVersion,
			&declaration.Authority,
			&declaration.Constraint,
			&declaration.DeclarationExact,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan raw database compatibility declaration: %w", err)
		}
		declarations = append(declarations, declaration)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read raw database compatibility declarations: %w", err)
	}
	rows.Close()
	return rejectCoreCompatibilityDeclarations(target, declarations)
}

func rejectCoreCompatibilityDeclarations(
	target *semver.Version,
	declarations []coreCompatibilityBlocker,
) error {
	blockers := make([]coreCompatibilityBlocker, 0)
	seen := make(map[coreCompatibilityBlocker]struct{}, len(declarations))
	for _, declaration := range declarations {
		if _, duplicate := seen[declaration]; duplicate {
			continue
		}
		seen[declaration] = struct{}{}
		constraint, constraintErr := semver.NewConstraint(declaration.Constraint)
		if !declaration.DeclarationExact || strings.TrimSpace(declaration.Constraint) == "" ||
			constraintErr != nil || !constraint.Check(target) {
			blockers = append(blockers, declaration)
		}
	}
	if len(blockers) == 0 {
		return nil
	}
	first := blockers[0]
	return fmt.Errorf(
		"%w: target=%s extension=%s version=%s authority=%s constraint=%q blockers=%d",
		ErrCoreUpgradeIncompatible, target.String(), first.ExtensionID, first.ExtensionVersion,
		first.Authority, first.Constraint, len(blockers),
	)
}

func coreCompatibilityFactsReady(ctx context.Context, db coreAuthorityQueryer) (bool, error) {
	if db == nil {
		return false, errors.New("database is required")
	}
	var ready bool
	err := db.QueryRowContext(ctx, `
		SELECT to_regclass('public.extensions') IS NOT NULL
		   AND to_regclass('public.extension_versions') IS NOT NULL
		   AND to_regclass('public.extension_trust_grants') IS NOT NULL
		   AND EXISTS (
		     SELECT 1 FROM information_schema.columns
		     WHERE table_schema = 'public' AND table_name = 'extensions'
		       AND column_name = 'active_version_id'
		   )
	`).Scan(&ready)
	return ready, err
}
