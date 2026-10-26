package extensions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func (s *PostgresStore) EnableLegacyPluginRuntime(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, PluginRuntimePublication, error) {
	return s.transitionLegacyPluginRuntime(ctx, target, actorUserID, true)
}

func (s *PostgresStore) DisableLegacyPluginRuntime(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, PluginRuntimePublication, error) {
	return s.transitionLegacyPluginRuntime(ctx, target, actorUserID, false)
}

// transitionLegacyPluginRuntime is the compatibility bridge for plugins that
// do not own a Lifecycle V2 journal. The immutable desired-set lock is taken
// before any mutable extension read so genesis cannot deadlock or be inferred
// from a partially changed legacy row.
func (s *PostgresStore) transitionLegacyPluginRuntime(
	ctx context.Context,
	target Extension,
	actorUserID int64,
	activate bool,
) (Extension, PluginRuntimePublication, error) {
	if s == nil || s.pool == nil || ctx == nil || actorUserID <= 0 || usesLifecycleV2(target) {
		return Extension{}, PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	if _, _, err := exactPluginRuntimeTransitionArtifact(target); err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, fmt.Errorf(
			"begin legacy plugin runtime transition: %w", err,
		)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		return Extension{}, PluginRuntimePublication{}, fmt.Errorf(
			"lock legacy plugin runtime desired set: %w", err,
		)
	}
	if err := requireLegacyPluginRuntimeGenesis(ctx, tx); err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}

	stored, err := lockLegacyPluginRuntimeArtifact(ctx, tx, target.ID)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}
	if stored.ExtensionID != target.ID || stored.VersionID != target.ActiveVersionID ||
		stored.Version != target.Version || stored.Digest != target.PackageDigest ||
		!reflect.DeepEqual(stored.Manifest, extensionmanifest.Normalize(target.Manifest)) {
		return Extension{}, PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	if stored.Status != StatusInstalled && stored.Status != StatusEnabled && stored.Status != StatusDisabled {
		return Extension{}, PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	// Transition membership is derived from the immutable extension_versions
	// manifest that the exact version id/digest names, never from caller memory.
	target.Manifest = stored.Manifest

	var lifecycleOpen bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM extension_lifecycle_operations
			WHERE extension_id = $1 AND completed_at IS NULL
		)
	`, target.ID).Scan(&lifecycleOpen); err != nil {
		return Extension{}, PluginRuntimePublication{}, fmt.Errorf(
			"inspect legacy plugin lifecycle operations: %w", err,
		)
	}
	if lifecycleOpen {
		return Extension{}, PluginRuntimePublication{}, ErrLifecycleOperationInProgress
	}

	nextStatus := StatusDisabled
	reason := PluginRuntimePublicationDisable
	if activate {
		nextStatus = StatusEnabled
		reason = PluginRuntimePublicationEnable
	}
	var updatedAt time.Time
	command := tx.QueryRow(ctx, `
		UPDATE extensions
		SET status = $2, updated_at = statement_timestamp()
		WHERE id = $1 AND status = $3 AND active_version_id = $4
		RETURNING updated_at
	`, target.ID, nextStatus, stored.Status, target.ActiveVersionID)
	if err := command.Scan(&updatedAt); errors.Is(err, pgx.ErrNoRows) {
		return Extension{}, PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	} else if err != nil {
		return Extension{}, PluginRuntimePublication{}, fmt.Errorf(
			"write legacy plugin state: %w", err,
		)
	}
	if !activate {
		if _, err := tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE extension_id = $1`, target.ID); err != nil {
			return Extension{}, PluginRuntimePublication{}, fmt.Errorf(
				"clear disabled mail provider: %w", err,
			)
		}
	}

	publication, err := PublishPluginRuntimePublicationTransitionTx(
		ctx,
		tx,
		PluginRuntimePublicationTransition{
			Source: &target, Target: target, Activate: activate,
			Reason: reason, ActorUserID: actorUserID,
		},
	)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}
	publication, err = s.commitAuthoritativePluginRuntimePublication(ctx, tx, publication)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}

	result := target
	result.Status = nextStatus
	result.UpdatedAt = updatedAt
	return result, publication, nil
}

// requireLegacyPluginRuntimeGenesis 只校验 genesis 表头不变量：
// 首条 publication 必须是 actor=0 的 startup_reconcile。
// 故意不走 loadPluginRuntimePublication 的 members 归一化——历史 revision 的
// member_count/digest 若与 members 表漂移，不应永久阻断后续 enable/disable；
// 实际 desired-set 以最新 revision 为准。
func requireLegacyPluginRuntimeGenesis(ctx context.Context, tx pgx.Tx) error {
	var reason PluginRuntimePublicationReason
	var actor sql.NullInt64
	err := tx.QueryRow(ctx, `
		SELECT reason, actor_user_id
		FROM plugin_runtime_publications
		ORDER BY revision ASC
		LIMIT 1
	`).Scan(&reason, &actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf(
			"%w: initial plugin runtime publication is required",
			ErrPluginRuntimePublicationConflict,
		)
	}
	if err != nil {
		return fmt.Errorf("load plugin runtime genesis: %w", err)
	}
	if reason != PluginRuntimePublicationStartupReconcile || actor.Valid {
		return fmt.Errorf(
			"%w: invalid plugin runtime genesis",
			ErrPluginRuntimePublicationConflict,
		)
	}
	return nil
}

type legacyPluginRuntimeArtifact struct {
	ExtensionID string
	Status      string
	VersionID   int64
	Version     string
	Digest      string
	Manifest    Manifest
}

func lockLegacyPluginRuntimeArtifact(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
) (legacyPluginRuntimeArtifact, error) {
	var artifact legacyPluginRuntimeArtifact
	var extensionType string
	var manifestJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT extension.status, extension.type, extension.active_version_id,
		       version.extension_id, version.version, version.package_digest,
		       version.manifest
		FROM extensions AS extension
		JOIN extension_versions AS version ON version.id = extension.active_version_id
		WHERE extension.id = $1
		FOR UPDATE OF extension
	`, extensionID).Scan(
		&artifact.Status, &extensionType, &artifact.VersionID,
		&artifact.ExtensionID, &artifact.Version, &artifact.Digest, &manifestJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return legacyPluginRuntimeArtifact{}, ErrExtensionNotFound
	}
	if err != nil {
		return legacyPluginRuntimeArtifact{}, fmt.Errorf("lock legacy plugin artifact: %w", err)
	}
	if extensionType != TypePlugin || artifact.ExtensionID != extensionID {
		return legacyPluginRuntimeArtifact{}, ErrPluginRuntimePublicationConflict
	}
	if err := json.Unmarshal(manifestJSON, &artifact.Manifest); err != nil {
		return legacyPluginRuntimeArtifact{}, fmt.Errorf("decode legacy plugin artifact manifest: %w", err)
	}
	artifact.Manifest = extensionmanifest.Normalize(artifact.Manifest)
	return artifact, nil
}

var _ LegacyPluginRuntimePublicationStore = (*PostgresStore)(nil)
