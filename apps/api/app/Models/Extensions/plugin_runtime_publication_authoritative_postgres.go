package extensions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	pluginRuntimeDesiredSetLock    = "sforum.plugin.runtime.desired.v1"
	pluginRuntimePublicationTries  = 4
	pluginRuntimePublicationUnlock = 5 * time.Second
)

// InitialPluginRuntimePublicationEnsurer imports the legacy mutable runtime set
// at most once. Later desired-set changes belong exclusively to lifecycle
// publication transitions.
type InitialPluginRuntimePublicationEnsurer interface {
	EnsureInitialPluginRuntimePublication(context.Context) (PluginRuntimePublication, error)
}

// EnsureInitialPluginRuntimePublication performs the one-time genesis import
// from legacy mutable extension rows. The outer session lock is acquired before
// opening the serializable transaction so a waiter cannot inherit a stale
// transaction snapshot while blocked on the transaction-scoped advisory lock.
// Safe Mode is enforced by callers by not invoking this method.
func (s *PostgresStore) EnsureInitialPluginRuntimePublication(
	ctx context.Context,
) (publication PluginRuntimePublication, returnErr error) {
	if s == nil || s.pool == nil || ctx == nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}

	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return PluginRuntimePublication{}, fmt.Errorf("acquire plugin runtime producer connection: %w", err)
	}
	// Once the lock request is sent, its outcome is unknown until PostgreSQL
	// confirms it. Any request error must therefore take the unlock-or-destroy
	// path rather than returning a possibly locked session to the pool.
	sessionLocked := true
	defer func() {
		publication, returnErr = s.finalizePluginRuntimeProducerResult(
			ctx, connection, sessionLocked, publication, returnErr,
		)
	}()
	if _, err := connection.Exec(ctx, `
		SELECT pg_advisory_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		return PluginRuntimePublication{}, fmt.Errorf("lock plugin runtime desired set: %w", err)
	}

	var lastRetryErr error
	for attempt := 0; attempt < pluginRuntimePublicationTries; attempt++ {
		publication, err := s.ensureInitialPluginRuntimePublicationOnce(ctx, connection.Conn())
		if !retryablePluginRuntimePublicationError(err) {
			return publication, err
		}
		lastRetryErr = err
		if ctx.Err() != nil {
			return PluginRuntimePublication{}, ctx.Err()
		}
	}
	return PluginRuntimePublication{}, errors.Join(
		fmt.Errorf("%w: plugin runtime desired-set transaction retries exhausted", ErrPluginRuntimePublicationConflict),
		lastRetryErr,
	)
}

type pluginRuntimePublicationCommitUnknown struct {
	cause error
}

func (e *pluginRuntimePublicationCommitUnknown) Error() string { return e.cause.Error() }
func (e *pluginRuntimePublicationCommitUnknown) Unwrap() error { return e.cause }

func (s *PostgresStore) finalizePluginRuntimeProducerResult(
	ctx context.Context,
	connection *pgxpool.Conn,
	sessionLocked bool,
	publication PluginRuntimePublication,
	operationErr error,
) (PluginRuntimePublication, error) {
	destroyed, releaseErr := releasePluginRuntimeProducerConnection(ctx, connection, sessionLocked)
	var commitUnknown *pluginRuntimePublicationCommitUnknown
	if errors.As(operationErr, &commitUnknown) {
		if releaseErr != nil && !destroyed {
			return PluginRuntimePublication{}, errors.Join(operationErr, releaseErr)
		}
		// Producer session 必须先回到池或被物理销毁，MaxConns=1 时精确
		// revision 回读才能取得连接并判定不确定 COMMIT。Hijack 后的物理
		// 连接即使 Close 报错也不会回池；pgx v5.10 仍会关闭底层 net.Conn，
		// 因此 session lock fail-closed，可以安全地从替代连接回读。
		recovered, recoveryErr := s.recoverAuthoritativePluginRuntimePublication(
			ctx, publication, commitUnknown.cause,
		)
		if recoveryErr == nil {
			return recovered, nil
		}
		return PluginRuntimePublication{}, errors.Join(recoveryErr, releaseErr)
	}
	if operationErr != nil {
		if releaseErr != nil {
			return PluginRuntimePublication{}, errors.Join(operationErr, releaseErr)
		}
		return PluginRuntimePublication{}, operationErr
	}
	if destroyed {
		// Unlock 的结果不确定时，物理连接已被销毁，不会把 session lock
		// 带回连接池。成功结果仍须从替代连接按精确 revision 回读后才可返回。
		stored, verifyErr := s.readExactAuthoritativePluginRuntimePublication(ctx, publication)
		if verifyErr == nil {
			return stored, nil
		}
		return PluginRuntimePublication{}, errors.Join(
			fmt.Errorf(
				"verify authoritative plugin runtime publication after producer session destroy: %w",
				verifyErr,
			),
			releaseErr,
		)
	}
	if releaseErr != nil {
		return PluginRuntimePublication{}, releaseErr
	}
	return publication, nil
}

func releasePluginRuntimeProducerConnection(
	ctx context.Context,
	connection *pgxpool.Conn,
	sessionLocked bool,
) (bool, error) {
	if !sessionLocked {
		connection.Release()
		return false, nil
	}
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginRuntimePublicationUnlock)
	defer cancel()
	var unlocked bool
	err := connection.QueryRow(unlockCtx, `
		SELECT pg_advisory_unlock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock).Scan(&unlocked)
	if err == nil && unlocked {
		connection.Release()
		return false, nil
	}

	// A session lock survives transaction rollback and normal pool release.
	// Closing the physical connection is the only fail-closed cleanup when the
	// unlock result is unknown or says this session did not own the lock.
	physical := connection.Hijack()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), pluginRuntimePublicationUnlock)
	defer closeCancel()
	closeErr := physical.Close(closeCtx)
	if closeErr == nil {
		return true, nil
	}
	if err != nil {
		return true, errors.Join(fmt.Errorf("unlock plugin runtime desired set: %w", err), closeErr)
	}
	return true, errors.Join(
		fmt.Errorf("%w: plugin runtime desired-set lock was not owned", ErrPluginRuntimePublicationConflict),
		closeErr,
	)
}

func (s *PostgresStore) ensureInitialPluginRuntimePublicationOnce(
	ctx context.Context,
	connection *pgx.Conn,
) (PluginRuntimePublication, error) {
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Keep the lock visible in the transaction contract as well as the outer
	// pre-snapshot gate. Production lifecycle state writers must share this key.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		return PluginRuntimePublication{}, err
	}
	latest, err := loadPluginRuntimePublication(
		ctx,
		tx,
		pluginRuntimePublicationSelect+` ORDER BY revision DESC LIMIT 1`,
	)
	switch {
	case err == nil:
		// 任意历史 revision 都已经建立 immutable authority。这里必须在
		// lifecycle/mutable 查询前短路，重启不得用 legacy rows 重写它。
		return s.commitAuthoritativePluginRuntimePublication(ctx, tx, latest)
	case err != nil && !errors.Is(err, ErrPluginRuntimePublicationNotFound):
		return PluginRuntimePublication{}, fmt.Errorf("load latest plugin runtime publication: %w", err)
	}

	var lifecycleOpen bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM extension_lifecycle_operations
			WHERE completed_at IS NULL
		)
	`).Scan(&lifecycleOpen); err != nil {
		return PluginRuntimePublication{}, fmt.Errorf("inspect open extension lifecycle operations: %w", err)
	}
	if lifecycleOpen {
		return PluginRuntimePublication{}, fmt.Errorf(
			"%w: initial plugin runtime publication waits for lifecycle completion",
			ErrLifecycleOperationInProgress,
		)
	}

	members, err := loadLegacyInitialPluginRuntimeMembers(ctx, tx)
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	publication, err := insertPluginRuntimePublication(
		ctx, tx, PluginRuntimePublicationStartupReconcile, 0, members,
	)
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	return s.commitAuthoritativePluginRuntimePublication(ctx, tx, publication)
}

func (s *PostgresStore) commitAuthoritativePluginRuntimePublication(
	ctx context.Context,
	tx pgx.Tx,
	publication PluginRuntimePublication,
) (PluginRuntimePublication, error) {
	if err := tx.Commit(ctx); err != nil {
		commitErr := fmt.Errorf(
			"commit authoritative plugin runtime publication: %w",
			mapPluginRuntimePostgresError(err, ErrPluginRuntimePublicationConflict),
		)
		if retryablePluginRuntimePublicationError(commitErr) {
			return PluginRuntimePublication{}, commitErr
		}
		return publication, &pluginRuntimePublicationCommitUnknown{cause: commitErr}
	}
	return publication, nil
}

func (s *PostgresStore) recoverAuthoritativePluginRuntimePublication(
	ctx context.Context,
	expected PluginRuntimePublication,
	commitErr error,
) (PluginRuntimePublication, error) {
	stored, err := s.readExactAuthoritativePluginRuntimePublication(ctx, expected)
	if err == nil {
		return stored, nil
	}
	return PluginRuntimePublication{}, errors.Join(
		commitErr,
		fmt.Errorf("verify authoritative plugin runtime publication commit: %w", err),
	)
}

func (s *PostgresStore) readExactAuthoritativePluginRuntimePublication(
	ctx context.Context,
	expected PluginRuntimePublication,
) (PluginRuntimePublication, error) {
	if expected.Revision <= 0 {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginRuntimePublicationUnlock)
	defer cancel()
	stored, err := s.PluginRuntimePublicationByRevision(verifyCtx, expected.Revision)
	if err == nil && samePluginRuntimePublication(stored, expected) {
		return stored, nil
	}
	if err == nil {
		return PluginRuntimePublication{}, fmt.Errorf(
			"%w: committed plugin runtime publication evidence changed",
			ErrPluginRuntimePublicationConflict,
		)
	}
	return PluginRuntimePublication{}, err
}

func loadLegacyInitialPluginRuntimeMembers(
	ctx context.Context,
	tx pgx.Tx,
) ([]PluginRuntimeMember, error) {
	type enabledPlugin struct {
		id, extensionType, status string
		activeVersionID           sql.NullInt64
	}
	rows, err := tx.Query(ctx, `
		SELECT id, type, status, active_version_id
		FROM extensions
		WHERE type = 'plugin' AND status = 'enabled'
		ORDER BY id COLLATE "C"
		FOR SHARE
	`)
	if err != nil {
		return nil, fmt.Errorf("load enabled plugin runtime set: %w", err)
	}

	enabled := make([]enabledPlugin, 0)
	for rows.Next() {
		var plugin enabledPlugin
		if err := rows.Scan(&plugin.id, &plugin.extensionType, &plugin.status, &plugin.activeVersionID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan enabled plugin runtime member: %w", err)
		}
		enabled = append(enabled, plugin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate enabled plugin runtime set: %w", err)
	}
	rows.Close()

	members := make([]PluginRuntimeMember, 0, len(enabled))
	for _, plugin := range enabled {
		if plugin.extensionType != TypePlugin || plugin.status != StatusEnabled || !plugin.activeVersionID.Valid {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has no exact active version",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}

		var versionID int64
		var versionExtensionID, version, packageDigest string
		var manifestBody []byte
		err := tx.QueryRow(ctx, `
			SELECT id, extension_id, version, package_digest, manifest
			FROM extension_versions
			WHERE id = $1
			FOR SHARE
		`, plugin.activeVersionID.Int64).Scan(
			&versionID, &versionExtensionID, &version, &packageDigest, &manifestBody,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has no exact active version",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("lock enabled plugin runtime version: %w", err)
		}
		if versionID != plugin.activeVersionID.Int64 || versionExtensionID != plugin.id || len(manifestBody) == 0 {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has no exact active version",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}

		var manifest Manifest
		if err := json.Unmarshal(manifestBody, &manifest); err != nil {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has a corrupt active manifest",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}
		manifest = extensionmanifest.Normalize(manifest)
		if err := extensionmanifest.Validate(manifest); err != nil ||
			manifest.ID != plugin.id || manifest.Type != TypePlugin ||
			manifest.Version != version {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has an inconsistent active manifest",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}
		if strings.TrimSpace(manifest.Backend.Entry) == "" {
			// Enabled declaration-only plugins participate in the other Registry
			// snapshots but never own a subprocess runtime member.
			continue
		}

		member := PluginRuntimeMember{
			ExtensionID:        plugin.id,
			ExtensionVersionID: versionID,
			ExtensionVersion:   version,
			PackageDigest:      packageDigest,
		}
		if !validPluginRuntimeMember(member) {
			return nil, fmt.Errorf(
				"%w: enabled plugin %q has invalid artifact identity",
				ErrPluginRuntimePublicationConflict,
				plugin.id,
			)
		}
		members = append(members, member)
	}
	return members, nil
}

func retryablePluginRuntimePublicationError(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		(postgresError.Code == "40001" || postgresError.Code == "40P01")
}

var _ InitialPluginRuntimePublicationEnsurer = (*PostgresStore)(nil)
