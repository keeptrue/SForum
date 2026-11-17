package extensions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const pluginRuntimePublicationSelect = `
	SELECT revision, member_count, members_digest, reason, actor_user_id, created_at
	FROM plugin_runtime_publications`

func insertPluginRuntimePublication(
	ctx context.Context,
	tx pgx.Tx,
	reason PluginRuntimePublicationReason,
	actorUserID int64,
	members []PluginRuntimeMember,
) (PluginRuntimePublication, error) {
	// TODO(#254): 发布期闸门未接线，写前必须先跑成员级 Verify
	// （ArtifactIntegrity.Verify；见 knowledge/decisions/2026-11-17-artifact-integrity-at-publish-time.md）。
	canonical, digest, err := canonicalPluginRuntimeMembers(members)
	if err != nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	publication := PluginRuntimePublication{
		MemberCount: len(canonical), MembersDigest: digest, Members: canonical,
		Reason: reason, ActorUserID: actorUserID,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO plugin_runtime_publications (
			member_count, members_digest, reason, actor_user_id
		) VALUES ($1, $2, $3, $4)
		RETURNING revision, created_at
	`, publication.MemberCount, publication.MembersDigest, publication.Reason,
		nullablePluginRuntimeActor(actorUserID),
	).Scan(&publication.Revision, &publication.CreatedAt); err != nil {
		return PluginRuntimePublication{}, fmt.Errorf(
			"insert plugin runtime publication: %w",
			mapPluginRuntimePostgresError(err, ErrPluginRuntimePublicationConflict),
		)
	}
	for _, member := range canonical {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_runtime_publication_members (
				publication_revision, extension_id, extension_version_id,
				extension_version, package_digest
			) VALUES ($1, $2, $3, $4, $5)
		`, publication.Revision, member.ExtensionID, member.ExtensionVersionID,
			member.ExtensionVersion, member.PackageDigest); err != nil {
			return PluginRuntimePublication{}, fmt.Errorf(
				"insert plugin runtime publication member: %w",
				mapPluginRuntimePostgresError(err, ErrPluginRuntimePublicationConflict),
			)
		}
	}
	return publication, nil
}

func (s *PostgresStore) LatestPluginRuntimePublication(ctx context.Context) (PluginRuntimePublication, error) {
	if s == nil || s.pool == nil || ctx == nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationNotFound
	}
	return loadPluginRuntimePublication(
		ctx, s.pool, pluginRuntimePublicationSelect+` ORDER BY revision DESC LIMIT 1`,
	)
}

func (s *PostgresStore) PluginRuntimePublicationByRevision(
	ctx context.Context,
	revision int64,
) (PluginRuntimePublication, error) {
	if s == nil || s.pool == nil || ctx == nil || revision <= 0 {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationNotFound
	}
	return loadPluginRuntimePublication(
		ctx, s.pool, pluginRuntimePublicationSelect+` WHERE revision = $1`, revision,
	)
}

type pluginRuntimePublicationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadPluginRuntimePublication(
	ctx context.Context,
	query pluginRuntimePublicationQuerier,
	headerQuery string,
	args ...any,
) (PluginRuntimePublication, error) {
	var publication PluginRuntimePublication
	var actor sql.NullInt64
	if err := query.QueryRow(ctx, headerQuery, args...).Scan(
		&publication.Revision,
		&publication.MemberCount,
		&publication.MembersDigest,
		&publication.Reason,
		&actor,
		&publication.CreatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationNotFound
	} else if err != nil {
		return PluginRuntimePublication{}, err
	}
	publication.ActorUserID = actor.Int64

	rows, err := query.Query(ctx, `
		SELECT extension_id, extension_version_id, extension_version, package_digest
		FROM plugin_runtime_publication_members
		WHERE publication_revision = $1
		ORDER BY extension_id COLLATE "C"
	`, publication.Revision)
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var member PluginRuntimeMember
		if err := rows.Scan(
			&member.ExtensionID,
			&member.ExtensionVersionID,
			&member.ExtensionVersion,
			&member.PackageDigest,
		); err != nil {
			return PluginRuntimePublication{}, err
		}
		publication.Members = append(publication.Members, member)
	}
	if err := rows.Err(); err != nil {
		return PluginRuntimePublication{}, err
	}
	normalized, err := normalizedPluginRuntimePublication(publication)
	if err != nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	return normalized, nil
}

func nullablePluginRuntimeActor(actorUserID int64) any {
	if actorUserID <= 0 {
		return nil
	}
	return actorUserID
}

func mapPluginRuntimePostgresError(err error, fallback error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if strings.Contains(pgErr.Message, "live node lease") {
		return fmt.Errorf("%w: %w", ErrPluginRuntimeNodeLeaseLost, err)
	}
	switch pgErr.Code {
	case "P0001", "23503", "23505", "23514":
		return fmt.Errorf("%w: %w", fallback, err)
	default:
		return err
	}
}

var _ PluginRuntimePublicationRepository = (*PostgresStore)(nil)
