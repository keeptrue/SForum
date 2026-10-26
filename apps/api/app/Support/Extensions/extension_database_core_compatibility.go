package extensionsruntime

import (
	"context"
	"fmt"

	semver "github.com/Masterminds/semver/v3"
	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

func validateExtensionDatabaseCoreCompatibility(
	ctx context.Context,
	queryer extensionDatabaseQuerier,
	declaration extensions.ManifestDatabase,
	powers []string,
) error {
	return validateExtensionDatabaseCoreCompatibilityForHost(
		ctx, queryer, declaration, powers, platformversion.CoreCompatibilityVersion(),
	)
}

func validateExtensionDatabaseCoreCompatibilityForHost(
	ctx context.Context,
	queryer extensionDatabaseQuerier,
	declaration extensions.ManifestDatabase,
	powers []string,
	hostVersion string,
) error {
	if !containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) &&
		!containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		return nil
	}
	var currentVersion, targetVersion, status string
	err := queryer.QueryRow(ctx, `
		SELECT current_version, target_version, status
		FROM public.`+pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize()+`
		WHERE singleton = TRUE
	`).Scan(&currentVersion, &targetVersion, &status)
	if err != nil {
		return fmt.Errorf("%w: load migrated Core version: %v", ErrExtensionDatabaseCoreIncompatible, err)
	}
	if status != "ready" || currentVersion == "" || currentVersion != targetVersion {
		return fmt.Errorf(
			"%w: Core migrations are not ready (status=%s current=%q target=%q)",
			ErrExtensionDatabaseCoreIncompatible, status, currentVersion, targetVersion,
		)
	}
	current, err := semver.StrictNewVersion(currentVersion)
	if err != nil {
		return fmt.Errorf("%w: stored Core version is invalid", ErrExtensionDatabaseCoreIncompatible)
	}
	host, err := semver.StrictNewVersion(hostVersion)
	if err != nil || !host.Equal(current) {
		return fmt.Errorf(
			"%w: host=%q migrated=%q",
			ErrExtensionDatabaseCoreIncompatible, hostVersion, currentVersion,
		)
	}
	constraint, err := semver.NewConstraint(declaration.CoreCompatibility)
	if err != nil || !constraint.Check(current) {
		return fmt.Errorf(
			"%w: migrated=%q constraint=%q",
			ErrExtensionDatabaseCoreIncompatible, currentVersion, declaration.CoreCompatibility,
		)
	}
	return nil
}
