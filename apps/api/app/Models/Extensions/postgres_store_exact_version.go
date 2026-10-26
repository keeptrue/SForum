package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// RollbackExtensionVersion 原子地把一个精确活动制品切回一个精确历史制品。
// staged 指针和所有 immutable version rows 均保留，供后续恢复或显式处理。
func (s *PostgresStore) RollbackExtensionVersion(ctx context.Context, input RollbackExtensionVersionInput) (Extension, error) {
	if err := validateRollbackExtensionVersionInput(input); err != nil {
		return Extension{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Extension{}, fmt.Errorf("begin extension version rollback: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockExactRollbackVersions(ctx, tx, input); err != nil {
		return Extension{}, err
	}
	if err := rejectActiveThemeArtifactMutation(ctx, tx, input.ExtensionID); err != nil {
		return Extension{}, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE extensions
		SET active_version_id = $5,
		    updated_at = now()
		WHERE id = $1
		  AND active_version_id = $2
		  AND EXISTS (
		    SELECT 1
		    FROM extension_versions
		    WHERE extension_versions.id = $2
		      AND extension_versions.extension_id = $1
		      AND extension_versions.version = $3
		      AND extension_versions.package_digest = $4
		  )
		  AND EXISTS (
		    SELECT 1
		    FROM extension_versions
		    WHERE extension_versions.id = $5
		      AND extension_versions.extension_id = $1
		      AND extension_versions.version = $6
		      AND extension_versions.package_digest = $7
		  )
	`, input.ExtensionID, input.ExpectedActiveVersionID, input.ExpectedActiveVersion,
		input.ExpectedActivePackageDigest, input.TargetVersionID, input.TargetVersion, input.TargetPackageDigest)
	if err != nil {
		return Extension{}, fmt.Errorf("rollback extension version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return Extension{}, ErrExtensionVersionConflict
	}
	rolledBack, err := getExtensionInTransaction(ctx, tx, input.ExtensionID)
	if err != nil {
		return Extension{}, err
	}
	if rolledBack.ActiveVersionID != input.TargetVersionID || rolledBack.Version != input.TargetVersion ||
		rolledBack.PackageDigest != input.TargetPackageDigest {
		return Extension{}, ErrExtensionVersionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return Extension{}, fmt.Errorf("commit extension version rollback: %w", err)
	}
	return rolledBack, nil
}

func lockExactRollbackVersions(ctx context.Context, tx pgx.Tx, input RollbackExtensionVersionInput) error {
	var activeVersionID, stagedVersionID, targetVersionID int64
	var activeExtensionID, activeVersion, activeDigest string
	var targetExtensionID, targetVersion, targetDigest string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(extensions.active_version_id, 0),
		       COALESCE(active_versions.extension_id, ''),
		       COALESCE(active_versions.version, ''),
		       COALESCE(active_versions.package_digest, ''),
		       COALESCE(extensions.staged_version_id, 0),
		       COALESCE(target_versions.id, 0),
		       COALESCE(target_versions.extension_id, ''),
		       COALESCE(target_versions.version, ''),
		       COALESCE(target_versions.package_digest, '')
		FROM extensions
		LEFT JOIN extension_versions AS active_versions
		  ON active_versions.id = extensions.active_version_id
		LEFT JOIN extension_versions AS target_versions
		  ON target_versions.id = $2
		 AND target_versions.extension_id = extensions.id
		WHERE extensions.id = $1
		FOR UPDATE OF extensions
	`, input.ExtensionID, input.TargetVersionID).Scan(
		&activeVersionID, &activeExtensionID, &activeVersion, &activeDigest,
		&stagedVersionID,
		&targetVersionID, &targetExtensionID, &targetVersion, &targetDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExtensionNotFound
	}
	if err != nil {
		return fmt.Errorf("lock exact extension versions for rollback: %w", err)
	}
	if activeVersionID != input.ExpectedActiveVersionID || activeExtensionID != input.ExtensionID ||
		activeVersion != input.ExpectedActiveVersion || activeDigest != input.ExpectedActivePackageDigest {
		return ErrExtensionVersionConflict
	}
	if targetVersionID != input.TargetVersionID || targetExtensionID != input.ExtensionID ||
		targetVersion != input.TargetVersion || targetDigest != input.TargetPackageDigest ||
		targetVersionID == stagedVersionID {
		return ErrExtensionVersionConflict
	}
	return nil
}

// GetExtensionVersion 返回 exact version + digest 对应的不可变快照。
func (s *PostgresStore) GetExtensionVersion(ctx context.Context, input ExactExtensionVersionInput) (ExtensionVersion, error) {
	if err := validateExactExtensionVersionInput(input); err != nil {
		return ExtensionVersion{}, err
	}
	version, err := scanExtensionVersion(s.pool.QueryRow(ctx, extensionVersionSelectSQL()+`
		WHERE extension_versions.extension_id = $1
		  AND extension_versions.version = $2
		  AND extension_versions.package_digest = $3
	`, input.ExtensionID, input.Version, input.PackageDigest))
	if errors.Is(err, pgx.ErrNoRows) {
		return ExtensionVersion{}, ErrExtensionVersionNotFound
	}
	if err != nil {
		return ExtensionVersion{}, fmt.Errorf("get exact extension version: %w", err)
	}
	return version, nil
}

// ListExtensionVersions 返回一个 extension 的全部不可变快照，最新安装优先。
func (s *PostgresStore) ListExtensionVersions(ctx context.Context, extensionID string) ([]ExtensionVersion, error) {
	if extensionID == "" || extensionID != normalizeID(extensionID) {
		return nil, ErrExtensionVersionInvalid
	}
	rows, err := s.pool.Query(ctx, extensionVersionSelectSQL()+`
		WHERE extension_versions.extension_id = $1
		ORDER BY extension_versions.installed_at DESC, extension_versions.id DESC
	`, extensionID)
	if err != nil {
		return nil, fmt.Errorf("list extension versions: %w", err)
	}
	defer rows.Close()

	versions := make([]ExtensionVersion, 0)
	for rows.Next() {
		version, scanErr := scanExtensionVersion(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan extension version: %w", scanErr)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate extension versions: %w", err)
	}
	if len(versions) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM extensions WHERE id = $1)`, extensionID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check extension for version list: %w", err)
		}
		if !exists {
			return nil, ErrExtensionNotFound
		}
	}
	return versions, nil
}

func extensionVersionSelectSQL() string {
	return `
		SELECT extension_versions.id, extension_versions.version,
		       extension_versions.manifest, extension_versions.package_digest,
		       extension_versions.admin_frontend_digest,
		       extension_versions.package_path, extension_versions.installed_at
		FROM extension_versions
	`
}

func scanExtensionVersion(row extensionRow) (ExtensionVersion, error) {
	var version ExtensionVersion
	var manifestJSON []byte
	if err := row.Scan(
		&version.ID, &version.Version, &manifestJSON, &version.PackageDigest,
		&version.AdminFrontendDigest, &version.PackagePath, &version.InstalledAt,
	); err != nil {
		return ExtensionVersion{}, err
	}
	if err := json.Unmarshal(manifestJSON, &version.Manifest); err != nil {
		return ExtensionVersion{}, fmt.Errorf("decode extension version manifest: %w", err)
	}
	version.Manifest = extensionmanifest.Normalize(version.Manifest)
	return version, nil
}

var _ ExactExtensionVersionRepository = (*PostgresStore)(nil)
