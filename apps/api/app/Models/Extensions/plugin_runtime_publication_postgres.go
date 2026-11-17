package extensions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
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
	canonical, digest, err := canonicalPluginRuntimeMembers(members)
	if err != nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	// 发布期闸门：publication 是追加式不可变表，一旦写进去就只能再追加一条覆盖，
	// 坏制品会让每次启动都在 stage 处 fail-closed（#254 现场）。所以「不写」是这
	// 一族失败唯一代价低的时刻：成员级完整性不过就整次发布拒绝，不写任何行。
	// 这是 apply 期校验（OpenVerifiedRuntimeExecutable / artifactState）的纵深前置，
	// 不是替代——带外删除发生在发布之后，只有 apply 期能看见。
	if err := verifyPluginRuntimePublicationMembers(ctx, tx, canonical); err != nil {
		return PluginRuntimePublication{}, err
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

// verifyPluginRuntimePublicationMembers 在写 publication 之前，对每个成员当前被
// durable 行钉住的制品做一次完整性校验。
//
// 路径来自同一个 tx 里的 extension_versions.package_path：发布入口可能刚在同一
// 事务里写/改过这些行，读自己的写才能保证「校验的制品」与「即将冻结的制品」一致。
//
// 失败语义是 fail-closed 且带坐标：错误直接是 *artifactintegrity.Error，保留
// extensionId / version / digest / 相对路径，不塌成 ErrPluginRuntimePublicationConflict
// 那种无坐标的发布冲突（现场需要凭这一句话直接定位是哪份制品的哪个文件）。
// 成员的 durable 行缺失时路径为空，Verify 会把它报成「package path is empty」——
// 同样是一次带坐标的拒绝，而不是放行。
func verifyPluginRuntimePublicationMembers(
	ctx context.Context,
	tx pgx.Tx,
	members []PluginRuntimeMember,
) error {
	if len(members) == 0 {
		// genesis/空期望集没有制品可校验，必须继续可发布（否则全新库起不来）。
		return nil
	}
	versionIDs := make([]int64, 0, len(members))
	for _, member := range members {
		versionIDs = append(versionIDs, member.ExtensionVersionID)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, COALESCE(package_path, '')
		FROM extension_versions
		WHERE id = ANY($1::bigint[])
	`, versionIDs)
	if err != nil {
		return fmt.Errorf("load plugin runtime publication member artifacts: %w", err)
	}
	defer rows.Close()
	packagePaths := make(map[int64]string, len(versionIDs))
	for rows.Next() {
		var versionID int64
		var packagePath string
		if err := rows.Scan(&versionID, &packagePath); err != nil {
			return fmt.Errorf("scan plugin runtime publication member artifact: %w", err)
		}
		packagePaths[versionID] = packagePath
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read plugin runtime publication member artifacts: %w", err)
	}
	for _, member := range members {
		if err := artifactintegrity.Verify(packagePaths[member.ExtensionVersionID], artifactintegrity.Member{
			ExtensionID: member.ExtensionID,
			Version:     member.ExtensionVersion,
			Digest:      member.PackageDigest,
		}); err != nil {
			return err
		}
	}
	return nil
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
