package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PublishPluginRuntimeRecoveryTx serializes an out-of-band recovery mutation
// with every other desired-set producer and appends one authoritative recovery
// revision in the caller-owned transaction. mutate must only change Host-owned
// durable state; it returns the complete set of extension ids disabled by that
// one recovery command.
//
// The callback runs only after existing immutable authority is validated and
// while the shared desired-set lock is held. This keeps the CLI independent of
// package files and prevents lock inversion with normal lifecycle writers.
func PublishPluginRuntimeRecoveryTx(
	ctx context.Context,
	tx pgx.Tx,
	mutate func() ([]string, error),
) (PluginRuntimePublication, error) {
	return publishPluginRuntimeRecoveryTx(ctx, tx, mutate, false)
}

// PublishProtectedPluginRuntimeQuarantineTx is the exact-artifact emergency
// path for built-in/system executables. The caller must CAS the protected row
// before returning its id; this function only accepts protected disabled rows.
func PublishProtectedPluginRuntimeQuarantineTx(
	ctx context.Context,
	tx pgx.Tx,
	mutate func() ([]string, error),
) (PluginRuntimePublication, error) {
	return publishPluginRuntimeRecoveryTx(ctx, tx, mutate, true)
}

func publishPluginRuntimeRecoveryTx(
	ctx context.Context,
	tx pgx.Tx,
	mutate func() ([]string, error),
	protected bool,
) (PluginRuntimePublication, error) {
	if ctx == nil || tx == nil || mutate == nil {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return PluginRuntimePublication{}, fmt.Errorf("read plugin runtime recovery isolation: %w", err)
	}
	// Waiting for the advisory lock must be followed by a fresh statement
	// snapshot; snapshot isolation could otherwise retain a stale full-set.
	if strings.TrimSpace(strings.ToLower(isolation)) != "read committed" {
		return PluginRuntimePublication{}, ErrPluginRuntimePublicationConflict
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		return PluginRuntimePublication{}, fmt.Errorf("lock plugin runtime recovery desired set: %w", err)
	}

	latest, err := loadPluginRuntimePublication(
		ctx,
		tx,
		pluginRuntimePublicationSelect+` ORDER BY revision DESC LIMIT 1`,
	)
	missingGenesis := errors.Is(err, ErrPluginRuntimePublicationNotFound)
	if err != nil && !missingGenesis {
		return PluginRuntimePublication{}, fmt.Errorf("load plugin runtime recovery full set: %w", err)
	}
	if !missingGenesis {
		// A structurally valid latest row is insufficient when the immutable
		// history did not begin with the one-time Host genesis projection.
		if err := requireLegacyPluginRuntimeGenesis(ctx, tx); err != nil {
			return PluginRuntimePublication{}, err
		}
	}

	disabledIDs, err := mutate()
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	disabled, err := canonicalRecoveryExtensionIDs(disabledIDs)
	if err != nil {
		return PluginRuntimePublication{}, err
	}
	if err := validateRecoveryDisabledExtensionsTx(ctx, tx, disabledIDs, disabled, protected); err != nil {
		return PluginRuntimePublication{}, err
	}

	baseMembers := latest.Members
	if missingGenesis {
		// Recovery first changes mutable status, then imports only the surviving
		// enabled plugins. A broken target manifest is therefore never decoded.
		baseMembers, err = loadLegacyInitialPluginRuntimeMembers(ctx, tx)
		if err != nil {
			return PluginRuntimePublication{}, err
		}
		if _, err := insertPluginRuntimePublication(
			ctx, tx, PluginRuntimePublicationStartupReconcile, 0, baseMembers,
		); err != nil {
			return PluginRuntimePublication{}, err
		}
	}

	nextMembers := make([]PluginRuntimeMember, 0, len(baseMembers))
	for _, member := range baseMembers {
		if _, remove := disabled[member.ExtensionID]; !remove {
			nextMembers = append(nextMembers, member)
		}
	}
	return insertPluginRuntimePublication(
		ctx, tx, PluginRuntimePublicationRecovery, 0, nextMembers,
	)
}

func validateRecoveryDisabledExtensionsTx(
	ctx context.Context,
	tx pgx.Tx,
	ids []string,
	canonical map[string]struct{},
	protected bool,
) error {
	if len(canonical) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, status, source, is_system
		FROM extensions
		WHERE id = ANY($1::text[])
		ORDER BY id COLLATE "C"
		FOR SHARE
	`, ids)
	if err != nil {
		return fmt.Errorf("validate recovery disabled extensions: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(canonical))
	for rows.Next() {
		var id, status, source string
		var isSystem bool
		if err := rows.Scan(&id, &status, &source, &isSystem); err != nil {
			return fmt.Errorf("scan recovery disabled extension: %w", err)
		}
		isProtected := source == SourceBuiltin || isSystem
		if _, expected := canonical[id]; !expected || status != StatusDisabled ||
			isProtected != protected {
			return ErrPluginRuntimePublicationConflict
		}
		seen[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate recovery disabled extensions: %w", err)
	}
	if len(seen) != len(canonical) {
		return ErrPluginRuntimePublicationConflict
	}
	return nil
}

func canonicalRecoveryExtensionIDs(ids []string) (map[string]struct{}, error) {
	canonical := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || id != strings.TrimSpace(id) {
			return nil, ErrPluginRuntimePublicationConflict
		}
		if _, duplicate := canonical[id]; duplicate {
			return nil, ErrPluginRuntimePublicationConflict
		}
		canonical[id] = struct{}{}
	}
	return canonical, nil
}

// ErrPluginRuntimeRecoveryRecheckMismatch 是 recheck 的 fail-closed 结论：
// 当前 durable 制品证据与最新 full-set 不一致，绝不"尽力修"。
var ErrPluginRuntimeRecoveryRecheckMismatch = errors.New(
	"extensions: plugin runtime recovery recheck found artifacts that do not match durable evidence",
)

// PluginRuntimeRecoveryArtifact 是一条不一致项，供 CLI 原样打印。
// Problem 说明这一项为什么非法，其余三个字段是精确制品坐标。
type PluginRuntimeRecoveryArtifact struct {
	ExtensionID string `json:"extensionId"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	Problem     string `json:"problem"`
}

// RecheckPluginRuntimeRecoveryTx 在调用方事务内重建 desired full set，
// 并在与 durable 证据完全一致时推进一条 reason='recovery' 的不可变 revision。
//
// 语义：
//   - 只认 durable 行（extensions.status + extensions.active_version_id +
//     extension_versions），不接受调用方传入的成员清单；
//   - 与最新 publication 的成员集必须逐字段相等，否则返回
//     ErrPluginRuntimeRecoveryRecheckMismatch 与完整不一致清单，且不写库；
//   - 仍有开放 lifecycle operation 时拒绝（desired set 可能正在切换）；
//   - 一致时插入新 revision（成员不变），让节点 wakeup 后重新应用一次；
//   - 本函数不 Commit / Rollback，审计由调用方在同一事务内写入。
func RecheckPluginRuntimeRecoveryTx(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
) (PluginRuntimePublication, []PluginRuntimeRecoveryArtifact, error) {
	if ctx == nil || tx == nil {
		return PluginRuntimePublication{}, nil, ErrPluginRuntimePublicationConflict
	}
	if actorUserID < 0 {
		return PluginRuntimePublication{}, nil, ErrPluginRuntimePublicationConflict
	}
	latest, err := lockLatestPluginRuntimePublication(ctx, tx)
	if errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		return PluginRuntimePublication{}, nil, fmt.Errorf(
			"%w: no durable plugin runtime publication exists yet",
			ErrPluginRuntimeRecoveryRecheckMismatch,
		)
	}
	if err != nil {
		return PluginRuntimePublication{}, nil, err
	}

	openOperations, err := openPluginRuntimeLifecycleOperations(ctx, tx)
	if err != nil {
		return PluginRuntimePublication{}, nil, err
	}
	if len(openOperations) > 0 {
		artifacts := make([]PluginRuntimeRecoveryArtifact, 0, len(openOperations))
		for _, id := range openOperations {
			artifacts = append(artifacts, PluginRuntimeRecoveryArtifact{
				ExtensionID: id,
				Problem:     "extension has an open lifecycle operation; the desired set is still switching",
			})
		}
		return PluginRuntimePublication{}, artifacts, fmt.Errorf(
			"%w: %w", ErrPluginRuntimeRecoveryRecheckMismatch, ErrLifecycleOperationInProgress,
		)
	}

	desired, artifacts, err := derivePluginRuntimeDesiredMembers(ctx, tx)
	if err != nil {
		return PluginRuntimePublication{}, nil, err
	}
	artifacts = append(artifacts, diffPluginRuntimeMembers(latest.Members, desired)...)
	if len(artifacts) > 0 {
		return PluginRuntimePublication{}, artifacts, ErrPluginRuntimeRecoveryRecheckMismatch
	}

	// 成员不变也必须推进 revision：这是"显式 recheck 才退出 recovery"的触发点，
	// 节点靠 revision 前进才能重新应用同一个 full-set。
	publication, err := insertPluginRuntimePublication(
		ctx, tx, PluginRuntimePublicationRecovery, actorUserID, latest.Members,
	)
	if err != nil {
		return PluginRuntimePublication{}, nil, err
	}
	return publication, nil, nil
}

func openPluginRuntimeLifecycleOperations(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT extension_id
		FROM extension_lifecycle_operations
		WHERE completed_at IS NULL
		ORDER BY extension_id
	`)
	if err != nil {
		return nil, fmt.Errorf("inspect open lifecycle operations: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// derivePluginRuntimeDesiredMembers 只从 durable 行重建成员集。
// 启用的扩展必须有精确 active 制品；停用的扩展不得留在 desired set 里
// （那属于 publication 与 extensions 漂移，由 diff 报告）。
func derivePluginRuntimeDesiredMembers(
	ctx context.Context,
	tx pgx.Tx,
) ([]PluginRuntimeMember, []PluginRuntimeRecoveryArtifact, error) {
	rows, err := tx.Query(ctx, `
		SELECT extensions.id, extensions.status,
		       COALESCE(extensions.active_version_id, 0),
		       COALESCE(extension_versions.version, ''),
		       COALESCE(extension_versions.package_digest, '')
		FROM extensions
		LEFT JOIN extension_versions ON extension_versions.id = extensions.active_version_id
		ORDER BY extensions.id
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("load durable extension artifacts: %w", err)
	}
	defer rows.Close()

	members := []PluginRuntimeMember{}
	artifacts := []PluginRuntimeRecoveryArtifact{}
	for rows.Next() {
		var (
			extensionID string
			status      string
			versionID   int64
			version     string
			digest      string
		)
		if err := rows.Scan(&extensionID, &status, &versionID, &version, &digest); err != nil {
			return nil, nil, err
		}
		if status != StatusEnabled {
			continue
		}
		if versionID <= 0 || version == "" || !validPackageDigest(digest) {
			artifacts = append(artifacts, PluginRuntimeRecoveryArtifact{
				ExtensionID: extensionID,
				Version:     version,
				Digest:      digest,
				Problem:     "enabled extension has no exact active artifact (version id, version and digest are required)",
			})
			continue
		}
		members = append(members, PluginRuntimeMember{
			ExtensionID:        extensionID,
			ExtensionVersionID: versionID,
			ExtensionVersion:   version,
			PackageDigest:      digest,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return members, artifacts, nil
}

// diffPluginRuntimeMembers 双向比对：published 里有而 durable 里没有的成员
// （例如已被停用却仍留在 full-set）和 durable 里新增的都算不一致。
func diffPluginRuntimeMembers(
	published []PluginRuntimeMember,
	desired []PluginRuntimeMember,
) []PluginRuntimeRecoveryArtifact {
	publishedByID := make(map[string]PluginRuntimeMember, len(published))
	for _, member := range published {
		publishedByID[member.ExtensionID] = member
	}
	desiredByID := make(map[string]PluginRuntimeMember, len(desired))
	artifacts := []PluginRuntimeRecoveryArtifact{}
	for _, member := range desired {
		desiredByID[member.ExtensionID] = member
		existing, ok := publishedByID[member.ExtensionID]
		if !ok {
			artifacts = append(artifacts, PluginRuntimeRecoveryArtifact{
				ExtensionID: member.ExtensionID,
				Version:     member.ExtensionVersion,
				Digest:      member.PackageDigest,
				Problem:     "enabled extension is missing from the published full set",
			})
			continue
		}
		if existing.ExtensionVersionID != member.ExtensionVersionID ||
			existing.ExtensionVersion != member.ExtensionVersion ||
			existing.PackageDigest != member.PackageDigest {
			artifacts = append(artifacts, PluginRuntimeRecoveryArtifact{
				ExtensionID: member.ExtensionID,
				Version:     member.ExtensionVersion,
				Digest:      member.PackageDigest,
				Problem: fmt.Sprintf(
					"published artifact %d/%s/%s does not match durable artifact %d/%s/%s",
					existing.ExtensionVersionID, existing.ExtensionVersion, existing.PackageDigest,
					member.ExtensionVersionID, member.ExtensionVersion, member.PackageDigest,
				),
			})
		}
	}
	for _, member := range published {
		if _, ok := desiredByID[member.ExtensionID]; ok {
			continue
		}
		artifacts = append(artifacts, PluginRuntimeRecoveryArtifact{
			ExtensionID: member.ExtensionID,
			Version:     member.ExtensionVersion,
			Digest:      member.PackageDigest,
			Problem:     "published member is not an enabled extension with this exact artifact",
		})
	}
	return artifacts
}
