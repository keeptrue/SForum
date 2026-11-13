package extensions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

type lifecycleStateVector struct {
	Status string
	Active LifecycleStatePublicationArtifact
	Staged *LifecycleStatePublicationArtifact
}

type lifecycleStatePublicationRecord struct {
	ID           int64
	OperationID  int64
	Operation    LifecycleMachineOperation
	Position     int
	StepID       string
	Mode         LifecycleStatePublicationMode
	ExtensionID  string
	Source       lifecycleStateVector
	Target       lifecycleStateVector
	Phase        LifecycleStatePublicationPhase
	FirstAttempt int
	LastAttempt  int
	Revision     int64
}

type lifecycleStatePublicationAuthority struct {
	Operation      LifecycleMachineOperation
	Position       int
	Source         *LifecycleStatePublicationArtifact
	Target         LifecycleStatePublicationArtifact
	LastAttempt    int
	CommitMarker   bool
	OperationOpen  bool
	ExtensionID    string
	ExtensionVer   string
	ExtensionHash  string
	OperationValue string
}

func (s *PostgresStore) PrepareLifecycleStatePublication(
	ctx context.Context,
	input PrepareLifecycleStatePublicationInput,
) (LifecycleStatePublicationRef, error) {
	if s == nil || s.pool == nil || ctx == nil {
		return LifecycleStatePublicationRef{}, ErrLifecycleStatePublicationInvalid
	}
	if err := validatePrepareLifecycleStatePublicationInput(input); err != nil {
		return LifecycleStatePublicationRef{}, err
	}
	ref := LifecycleStatePublicationRef{
		OperationID: input.OperationID, StepID: input.StepID, Mode: input.Mode, Attempt: input.Attempt,
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LifecycleStatePublicationRef{}, fmt.Errorf("begin lifecycle state publication prepare: %w", err)
	}
	defer tx.Rollback(ctx)

	authority, err := lockLifecycleStatePublicationAuthority(ctx, tx, ref)
	if err != nil {
		return LifecycleStatePublicationRef{}, err
	}
	if err := authority.matchesInput(input); err != nil {
		return LifecycleStatePublicationRef{}, err
	}

	record, err := loadLifecycleStatePublication(ctx, tx, ref, true)
	if err == nil {
		if !record.matchesInput(input) || input.Attempt < record.LastAttempt {
			return LifecycleStatePublicationRef{}, ErrLifecycleStatePublicationConflict
		}
		if input.Attempt > record.LastAttempt {
			tag, updateErr := tx.Exec(ctx, `
				UPDATE extension_lifecycle_state_publications
				SET last_attempt = $2,
				    revision = revision + 1,
				    updated_at = statement_timestamp()
				WHERE id = $1 AND revision = $3 AND last_attempt = $4
			`, record.ID, input.Attempt, record.Revision, record.LastAttempt)
			if updateErr != nil {
				return LifecycleStatePublicationRef{}, fmt.Errorf("advance lifecycle state publication attempt: %w", updateErr)
			}
			if tag.RowsAffected() != 1 {
				return LifecycleStatePublicationRef{}, ErrLifecycleStatePublicationConflict
			}
			record.LastAttempt = input.Attempt
			record.Revision++
		}
		current, loadErr := lockLifecycleExtensionState(ctx, tx, record.ExtensionID)
		if loadErr != nil || !record.currentVector().equal(current) ||
			(authority.CommitMarker && record.Phase != LifecycleStatePublicationTarget) {
			return LifecycleStatePublicationRef{}, ErrLifecycleStatePublicationConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return LifecycleStatePublicationRef{}, fmt.Errorf("commit existing lifecycle state publication: %w", err)
		}
		return ref, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return LifecycleStatePublicationRef{}, mapLifecycleStatePublicationLoadError("load lifecycle state publication", err)
	}
	if authority.CommitMarker {
		// marker 已提交却没有状态 intent，不能根据当前行猜测历史 source。
		return LifecycleStatePublicationRef{}, ErrLifecycleStatePublicationConflict
	}

	source, err := lockLifecycleExtensionState(ctx, tx, input.Target.ExtensionID)
	if err != nil {
		return LifecycleStatePublicationRef{}, mapLifecycleStateExtensionError(err)
	}
	if err := lockExactLifecycleStateVersion(ctx, tx, input.Target); err != nil {
		return LifecycleStatePublicationRef{}, err
	}
	target, err := lifecycleTargetState(input, source)
	if err != nil {
		return LifecycleStatePublicationRef{}, err
	}
	if err := insertLifecycleStatePublication(ctx, tx, input, source, target); err != nil {
		return LifecycleStatePublicationRef{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleStatePublicationRef{}, fmt.Errorf("commit lifecycle state publication prepare: %w", err)
	}
	return ref, nil
}

func (s *PostgresStore) InspectLifecycleStatePublication(
	ctx context.Context,
	ref LifecycleStatePublicationRef,
) (LifecycleStatePublicationPhase, error) {
	if s == nil || s.pool == nil || ctx == nil {
		return "", ErrLifecycleStatePublicationInvalid
	}
	if err := validateLifecycleStatePublicationRef(ref); err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin lifecycle state publication inspection: %w", err)
	}
	defer tx.Rollback(ctx)
	authority, record, err := lockLifecycleStateTransaction(ctx, tx, ref)
	if err != nil {
		return "", err
	}
	current, err := lockLifecycleExtensionState(ctx, tx, record.ExtensionID)
	if err != nil || !record.currentVector().equal(current) ||
		(authority.CommitMarker && record.Phase != LifecycleStatePublicationTarget) {
		return "", ErrLifecycleStatePublicationConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit lifecycle state publication inspection: %w", err)
	}
	return record.Phase, nil
}

func (s *PostgresStore) PublishLifecycleState(ctx context.Context, ref LifecycleStatePublicationRef) error {
	return s.moveLifecycleState(ctx, ref, LifecycleStatePublicationTarget)
}

func (s *PostgresStore) RestoreLifecycleState(ctx context.Context, ref LifecycleStatePublicationRef) error {
	return s.moveLifecycleState(ctx, ref, LifecycleStatePublicationSource)
}

func (s *PostgresStore) moveLifecycleState(
	ctx context.Context,
	ref LifecycleStatePublicationRef,
	destination LifecycleStatePublicationPhase,
) error {
	if s == nil || s.pool == nil || ctx == nil ||
		(destination != LifecycleStatePublicationSource && destination != LifecycleStatePublicationTarget) {
		return ErrLifecycleStatePublicationInvalid
	}
	if err := validateLifecycleStatePublicationRef(ref); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle state publication move: %w", err)
	}
	defer tx.Rollback(ctx)
	authority, record, err := lockLifecycleStateTransaction(ctx, tx, ref)
	if err != nil {
		return err
	}
	if destination == LifecycleStatePublicationSource && authority.CommitMarker {
		return ErrLifecycleStatePublicationCommitted
	}
	if authority.CommitMarker && record.Phase != LifecycleStatePublicationTarget {
		return ErrLifecycleStatePublicationConflict
	}

	current, err := lockLifecycleExtensionState(ctx, tx, record.ExtensionID)
	if err != nil || !record.currentVector().equal(current) {
		return ErrLifecycleStatePublicationConflict
	}
	if record.Phase == destination {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit idempotent lifecycle state publication: %w", err)
		}
		return nil
	}
	want := record.Source
	if destination == LifecycleStatePublicationTarget {
		want = record.Target
	}
	if err := lockLifecycleStateVectorVersions(ctx, tx, want); err != nil {
		return err
	}
	if err := writeLifecycleExtensionState(ctx, tx, record.ExtensionID, current, want); err != nil {
		return err
	}
	// 状态真的要变了：设置字段投影与去投影在这里发生（enable/disable/upgrade/
	// rollback/uninstall 全部经过这条状态发布）。与 PostgresStore.Enable/Disable
	// 一起构成两个状态写原语；它们共用同一个投影器实例。
	if err := s.syncLifecycleSettingsSurfaceFields(ctx, tx, record.ExtensionID, want); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_state_publications
		SET transaction_state = $2,
		    revision = revision + 1,
		    updated_at = statement_timestamp(),
		    published_at = CASE WHEN $2 = 'target' THEN COALESCE(published_at, statement_timestamp()) ELSE published_at END,
		    restored_at = CASE WHEN $2 = 'source' THEN statement_timestamp() ELSE restored_at END
		WHERE id = $1 AND revision = $3 AND transaction_state = $4 AND last_attempt = $5
	`, record.ID, destination, record.Revision, record.Phase, ref.Attempt)
	if err != nil {
		return fmt.Errorf("advance lifecycle state publication: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLifecycleStatePublicationConflict
	}
	actual, err := lockLifecycleExtensionState(ctx, tx, record.ExtensionID)
	if err != nil || !want.equal(actual) {
		return ErrLifecycleStatePublicationConflict
	}
	if err := tx.Commit(ctx); err != nil {
		// 调用方必须随后 Inspect，不能根据 commit 返回值猜测事实。
		return fmt.Errorf("commit lifecycle state publication move: %w", err)
	}
	return nil
}

func lockLifecycleStateTransaction(
	ctx context.Context,
	tx pgx.Tx,
	ref LifecycleStatePublicationRef,
) (lifecycleStatePublicationAuthority, lifecycleStatePublicationRecord, error) {
	authority, err := lockLifecycleStatePublicationAuthority(ctx, tx, ref)
	if err != nil {
		return lifecycleStatePublicationAuthority{}, lifecycleStatePublicationRecord{}, err
	}
	record, err := loadLifecycleStatePublication(ctx, tx, ref, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleStatePublicationAuthority{}, lifecycleStatePublicationRecord{}, ErrLifecycleStatePublicationNotPrepared
	}
	if err != nil {
		return lifecycleStatePublicationAuthority{}, lifecycleStatePublicationRecord{}, mapLifecycleStatePublicationLoadError("lock lifecycle state publication", err)
	}
	if record.LastAttempt != ref.Attempt || authority.LastAttempt != ref.Attempt ||
		record.Operation != authority.Operation || record.Position != authority.Position ||
		record.ExtensionID != authority.Target.ExtensionID {
		return lifecycleStatePublicationAuthority{}, lifecycleStatePublicationRecord{}, ErrLifecycleStatePublicationConflict
	}
	if !record.matchesInput(PrepareLifecycleStatePublicationInput{
		OperationID: ref.OperationID, Operation: authority.Operation, Position: authority.Position,
		StepID: ref.StepID, Attempt: ref.Attempt, Mode: ref.Mode,
		Source: authority.Source, Target: authority.Target,
	}) {
		return lifecycleStatePublicationAuthority{}, lifecycleStatePublicationRecord{}, ErrLifecycleStatePublicationConflict
	}
	return authority, record, nil
}

func lockLifecycleStatePublicationAuthority(
	ctx context.Context,
	tx pgx.Tx,
	ref LifecycleStatePublicationRef,
) (lifecycleStatePublicationAuthority, error) {
	var authority lifecycleStatePublicationAuthority
	if err := tx.QueryRow(ctx, `
		SELECT extension_id, extension_version, package_digest, operation,
		       completed_at IS NULL
		FROM extension_lifecycle_operations
		WHERE id = $1
		FOR UPDATE
	`, ref.OperationID).Scan(
		&authority.ExtensionID, &authority.ExtensionVer, &authority.ExtensionHash,
		&authority.OperationValue, &authority.OperationOpen,
	); errors.Is(err, pgx.ErrNoRows) {
		return lifecycleStatePublicationAuthority{}, ErrLifecycleStatePublicationNotPrepared
	} else if err != nil {
		return lifecycleStatePublicationAuthority{}, fmt.Errorf("lock lifecycle state publication operation: %w", err)
	}

	var operation string
	var sourceID, sourceVersion, sourceDigest sql.NullString
	var sourceVersionID sql.NullInt64
	err := tx.QueryRow(ctx, `
		SELECT publication.operation, publication.position,
		       publication.source_extension_id, publication.source_extension_version,
		       publication.source_package_digest, publication.source_version_id,
		       publication.target_extension_id, publication.target_extension_version,
		       publication.target_package_digest, publication.target_version_id,
		       publication.last_attempt, publication.commit_marker
		FROM extension_lifecycle_publications AS publication
		WHERE publication.operation_id = $1
		  AND publication.step_id = $2
		  AND publication.publication_mode = $3
		FOR UPDATE OF publication
	`, ref.OperationID, ref.StepID, ref.Mode).Scan(
		&operation, &authority.Position,
		&sourceID, &sourceVersion, &sourceDigest, &sourceVersionID,
		&authority.Target.ExtensionID, &authority.Target.Version,
		&authority.Target.PackageDigest, &authority.Target.VersionID,
		&authority.LastAttempt, &authority.CommitMarker,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleStatePublicationAuthority{}, ErrLifecycleStatePublicationNotPrepared
	}
	if err != nil {
		return lifecycleStatePublicationAuthority{}, fmt.Errorf("lock lifecycle state publication authority: %w", err)
	}
	authority.Operation = LifecycleMachineOperation(operation)
	if sourceID.Valid || sourceVersion.Valid || sourceDigest.Valid || sourceVersionID.Valid {
		if !sourceID.Valid || !sourceVersion.Valid || !sourceDigest.Valid || !sourceVersionID.Valid {
			return lifecycleStatePublicationAuthority{}, ErrLifecycleStatePublicationConflict
		}
		authority.Source = &LifecycleStatePublicationArtifact{
			ExtensionID: sourceID.String, Version: sourceVersion.String,
			PackageDigest: sourceDigest.String, VersionID: sourceVersionID.Int64,
		}
	}
	if !authority.OperationOpen || authority.OperationValue != operation ||
		authority.ExtensionID != authority.Target.ExtensionID ||
		authority.ExtensionVer != authority.Target.Version ||
		authority.ExtensionHash != authority.Target.PackageDigest {
		return lifecycleStatePublicationAuthority{}, ErrLifecycleStatePublicationConflict
	}
	return authority, nil
}

func (a lifecycleStatePublicationAuthority) matchesInput(input PrepareLifecycleStatePublicationInput) error {
	if a.Operation != input.Operation || a.Position != input.Position || a.LastAttempt != input.Attempt ||
		a.Target != input.Target || !equalLifecycleStateArtifactPointers(a.Source, input.Source) {
		return ErrLifecycleStatePublicationConflict
	}
	return nil
}

func equalLifecycleStateArtifactPointers(left, right *LifecycleStatePublicationArtifact) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func lifecycleTargetState(
	input PrepareLifecycleStatePublicationInput,
	source lifecycleStateVector,
) (lifecycleStateVector, error) {
	target := source
	sourceMatchesTarget := source.Active == input.Target
	sourceMatchesInput := input.Source != nil && source.Active == *input.Source
	switch input.Operation {
	case LifecycleMachineInstall:
		if source.Status != StatusInstalled {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		switch {
		case sourceMatchesTarget && source.Staged == nil:
			// 首个惰性包仍是 active pointer，直接发布状态即可。
		case source.Staged != nil && *source.Staged == input.Target:
			// 首次启用前再次上传只产生 inert candidate。install 事务发布
			// 最新精确候选，旧 active 从未执行且仍保留在 source 快照中。
			target.Active = input.Target
			target.Staged = nil
		default:
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Status = StatusEnabled
	case LifecycleMachineEnable:
		if !sourceMatchesTarget || (source.Status != StatusInstalled && source.Status != StatusDisabled) {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Status = StatusEnabled
	case LifecycleMachineDisable:
		if !sourceMatchesInput || !sourceMatchesTarget || source.Status != StatusEnabled {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Status = StatusDisabled
	case LifecycleMachineUpgrade:
		if !sourceMatchesInput || source.Status != StatusEnabled || source.Staged == nil || *source.Staged != input.Target {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Active = input.Target
		target.Staged = nil
	case LifecycleMachineRollback:
		if !sourceMatchesInput || source.Status != StatusEnabled ||
			(source.Staged != nil && *source.Staged == input.Target) {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Active = input.Target
	case LifecycleMachineUninstall:
		if !sourceMatchesInput || !sourceMatchesTarget ||
			(source.Status != StatusEnabled && source.Status != StatusDisabled) {
			return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
		}
		target.Status = StatusDisabled
	default:
		return lifecycleStateVector{}, ErrLifecycleStatePublicationInvalid
	}
	return target, nil
}

func insertLifecycleStatePublication(
	ctx context.Context,
	tx pgx.Tx,
	input PrepareLifecycleStatePublicationInput,
	source lifecycleStateVector,
	target lifecycleStateVector,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO extension_lifecycle_state_publications (
			operation_id, operation, step_id, position, publication_mode, extension_id,
			source_status, source_active_version_id, source_active_version, source_active_package_digest,
			source_staged_version_id, source_staged_version, source_staged_package_digest,
			target_status, target_active_version_id, target_active_version, target_active_package_digest,
			target_staged_version_id, target_staged_version, target_staged_package_digest,
			transaction_state, first_attempt, last_attempt
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20,
			'source', $21, $21
		)
	`, input.OperationID, input.Operation, input.StepID, input.Position, input.Mode, input.Target.ExtensionID,
		source.Status, source.Active.VersionID, source.Active.Version, source.Active.PackageDigest,
		nullableLifecycleStateArtifactID(source.Staged), nullableLifecycleStateArtifactVersion(source.Staged), nullableLifecycleStateArtifactDigest(source.Staged),
		target.Status, target.Active.VersionID, target.Active.Version, target.Active.PackageDigest,
		nullableLifecycleStateArtifactID(target.Staged), nullableLifecycleStateArtifactVersion(target.Staged), nullableLifecycleStateArtifactDigest(target.Staged),
		input.Attempt)
	if err != nil {
		return fmt.Errorf("insert lifecycle state publication: %w", err)
	}
	return nil
}

func loadLifecycleStatePublication(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	ref LifecycleStatePublicationRef,
	forUpdate bool,
) (lifecycleStatePublicationRecord, error) {
	query := `
		SELECT id, operation, position, extension_id,
		       source_status, source_active_version_id, source_active_version, source_active_package_digest,
		       source_staged_version_id, source_staged_version, source_staged_package_digest,
		       target_status, target_active_version_id, target_active_version, target_active_package_digest,
		       target_staged_version_id, target_staged_version, target_staged_package_digest,
		       transaction_state, first_attempt, last_attempt, revision
		FROM extension_lifecycle_state_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var record lifecycleStatePublicationRecord
	var operation, phase string
	var sourceStagedID, targetStagedID sql.NullInt64
	var sourceStagedVersion, sourceStagedDigest, targetStagedVersion, targetStagedDigest sql.NullString
	err := querier.QueryRow(ctx, query, ref.OperationID, ref.StepID, ref.Mode).Scan(
		&record.ID, &operation, &record.Position, &record.ExtensionID,
		&record.Source.Status, &record.Source.Active.VersionID, &record.Source.Active.Version, &record.Source.Active.PackageDigest,
		&sourceStagedID, &sourceStagedVersion, &sourceStagedDigest,
		&record.Target.Status, &record.Target.Active.VersionID, &record.Target.Active.Version, &record.Target.Active.PackageDigest,
		&targetStagedID, &targetStagedVersion, &targetStagedDigest,
		&phase, &record.FirstAttempt, &record.LastAttempt, &record.Revision,
	)
	if err != nil {
		return lifecycleStatePublicationRecord{}, err
	}
	record.OperationID, record.Operation, record.StepID, record.Mode = ref.OperationID, LifecycleMachineOperation(operation), ref.StepID, ref.Mode
	record.Source.Active.ExtensionID, record.Target.Active.ExtensionID = record.ExtensionID, record.ExtensionID
	record.Phase = LifecycleStatePublicationPhase(phase)
	var stagedErr error
	record.Source.Staged, stagedErr = scanLifecycleStateStaged(record.ExtensionID, sourceStagedID, sourceStagedVersion, sourceStagedDigest)
	if stagedErr != nil {
		return lifecycleStatePublicationRecord{}, stagedErr
	}
	record.Target.Staged, stagedErr = scanLifecycleStateStaged(record.ExtensionID, targetStagedID, targetStagedVersion, targetStagedDigest)
	if stagedErr != nil {
		return lifecycleStatePublicationRecord{}, stagedErr
	}
	if !validLifecycleStateArtifact(record.Source.Active) || !validLifecycleStateArtifact(record.Target.Active) ||
		(record.Phase != LifecycleStatePublicationSource && record.Phase != LifecycleStatePublicationTarget) {
		return lifecycleStatePublicationRecord{}, ErrLifecycleStatePublicationConflict
	}
	return record, nil
}

func (r lifecycleStatePublicationRecord) matchesInput(input PrepareLifecycleStatePublicationInput) bool {
	wantSource := input.Target
	if input.Source != nil {
		wantSource = *input.Source
	}
	wantTarget, err := lifecycleTargetState(input, r.Source)
	sourceMatches := r.Source.Active == wantSource
	if input.Operation == LifecycleMachineInstall && input.Source == nil {
		// install may promote a never-executed staged candidate; lifecycleTargetState
		// validates the complete physical source vector in that case.
		sourceMatches = true
	}
	return err == nil && r.OperationID == input.OperationID && r.Operation == input.Operation && r.Position == input.Position &&
		r.StepID == input.StepID && r.Mode == input.Mode && r.ExtensionID == input.Target.ExtensionID &&
		sourceMatches && r.Target.equal(wantTarget)
}

func (r lifecycleStatePublicationRecord) currentVector() lifecycleStateVector {
	if r.Phase == LifecycleStatePublicationTarget {
		return r.Target
	}
	return r.Source
}

func lockLifecycleExtensionState(ctx context.Context, tx pgx.Tx, extensionID string) (lifecycleStateVector, error) {
	var state lifecycleStateVector
	var stagedID sql.NullInt64
	var stagedVersion, stagedDigest sql.NullString
	err := tx.QueryRow(ctx, `
		SELECT extensions.status,
		       active_versions.id, active_versions.version, active_versions.package_digest,
		       staged_versions.id, staged_versions.version, staged_versions.package_digest
		FROM extensions
		JOIN extension_versions AS active_versions ON active_versions.id = extensions.active_version_id
		LEFT JOIN extension_versions AS staged_versions ON staged_versions.id = extensions.staged_version_id
		WHERE extensions.id = $1
		FOR UPDATE OF extensions
	`, extensionID).Scan(
		&state.Status, &state.Active.VersionID, &state.Active.Version, &state.Active.PackageDigest,
		&stagedID, &stagedVersion, &stagedDigest,
	)
	if err != nil {
		return lifecycleStateVector{}, err
	}
	state.Active.ExtensionID = extensionID
	state.Staged, err = scanLifecycleStateStaged(extensionID, stagedID, stagedVersion, stagedDigest)
	if err != nil || !validLifecycleStateArtifact(state.Active) {
		return lifecycleStateVector{}, ErrLifecycleStatePublicationConflict
	}
	return state, nil
}

func scanLifecycleStateStaged(
	extensionID string,
	id sql.NullInt64,
	version sql.NullString,
	digest sql.NullString,
) (*LifecycleStatePublicationArtifact, error) {
	if !id.Valid && !version.Valid && !digest.Valid {
		return nil, nil
	}
	if !id.Valid || !version.Valid || !digest.Valid {
		return nil, ErrLifecycleStatePublicationConflict
	}
	artifact := &LifecycleStatePublicationArtifact{
		ExtensionID: extensionID, VersionID: id.Int64, Version: version.String, PackageDigest: digest.String,
	}
	if !validLifecycleStateArtifact(*artifact) {
		return nil, ErrLifecycleStatePublicationConflict
	}
	return artifact, nil
}

func lockExactLifecycleStateVersion(ctx context.Context, tx pgx.Tx, artifact LifecycleStatePublicationArtifact) error {
	var extensionID, version, digest string
	err := tx.QueryRow(ctx, `
		SELECT extension_id, version, package_digest
		FROM extension_versions
		WHERE id = $1
		FOR KEY SHARE
	`, artifact.VersionID).Scan(&extensionID, &version, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecycleStatePublicationConflict
	}
	if err != nil {
		return fmt.Errorf("lock exact lifecycle extension version: %w", err)
	}
	if extensionID != artifact.ExtensionID || version != artifact.Version || digest != artifact.PackageDigest {
		return ErrLifecycleStatePublicationConflict
	}
	return nil
}

func lockLifecycleStateVectorVersions(ctx context.Context, tx pgx.Tx, state lifecycleStateVector) error {
	if err := lockExactLifecycleStateVersion(ctx, tx, state.Active); err != nil {
		return err
	}
	if state.Staged != nil {
		return lockExactLifecycleStateVersion(ctx, tx, *state.Staged)
	}
	return nil
}

func writeLifecycleExtensionState(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	current lifecycleStateVector,
	target lifecycleStateVector,
) error {
	var extensionType string
	if err := tx.QueryRow(ctx, `SELECT type FROM extensions WHERE id = $1 FOR UPDATE`, extensionID).Scan(&extensionType); err != nil {
		return mapLifecycleStateExtensionError(err)
	}
	if extensionType == TypeTheme {
		return ErrThemeActivationRequired
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extensions
		SET status = $5,
		    active_version_id = $6,
		    staged_version_id = $7,
		    updated_at = statement_timestamp()
		WHERE id = $1
		  AND status = $2
		  AND active_version_id = $3
		  AND staged_version_id IS NOT DISTINCT FROM $4::bigint
	`, extensionID, current.Status, current.Active.VersionID, nullableLifecycleStateArtifactID(current.Staged),
		target.Status, target.Active.VersionID, nullableLifecycleStateArtifactID(target.Staged))
	if err != nil {
		return fmt.Errorf("publish exact lifecycle extension state: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLifecycleStatePublicationConflict
	}
	return nil
}

func (s lifecycleStateVector) equal(other lifecycleStateVector) bool {
	return s.Status == other.Status && s.Active == other.Active &&
		equalLifecycleStateArtifactPointers(s.Staged, other.Staged)
}

func nullableLifecycleStateArtifactID(artifact *LifecycleStatePublicationArtifact) any {
	if artifact == nil {
		return nil
	}
	return artifact.VersionID
}

func nullableLifecycleStateArtifactVersion(artifact *LifecycleStatePublicationArtifact) any {
	if artifact == nil {
		return nil
	}
	return artifact.Version
}

func nullableLifecycleStateArtifactDigest(artifact *LifecycleStatePublicationArtifact) any {
	if artifact == nil {
		return nil
	}
	return artifact.PackageDigest
}

func mapLifecycleStateExtensionError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExtensionNotFound
	}
	return err
}

func mapLifecycleStatePublicationLoadError(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecycleStatePublicationNotPrepared
	}
	if errors.Is(err, ErrLifecycleStatePublicationConflict) {
		return err
	}
	return fmt.Errorf("%s: %w", action, err)
}

var _ LifecycleStatePublicationRepository = (*PostgresStore)(nil)

// syncLifecycleSettingsSurfaceFields 在状态发布事务内同步插件设置字段。
//
// 规则与 store.Enable/Disable 一致：
//   - 目标状态不是 enabled（停用/卸载）→ 只翻 enabled=false，值保留；失败不阻断
//     （停用必须永远可用，残留字段由启动 reconcile 收敛）
//   - 目标状态是 enabled（启用/升级/回滚）→ 以目标 active version 的 manifest 为准
//     幂等投影；失败返回 preflight 错误，整笔状态发布回滚
func (s *PostgresStore) syncLifecycleSettingsSurfaceFields(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	want lifecycleStateVector,
) error {
	if s.settingsSurfaceFields == nil {
		return nil
	}
	if want.Status != StatusEnabled {
		if err := s.settingsSurfaceFields.Disable(ctx, extensionID); err != nil {
			slog.Warn("settings surface: deproject after lifecycle disable failed",
				"extensionId", extensionID, "error", err)
		}
		return nil
	}
	if want.Active.VersionID == 0 {
		return nil
	}
	var manifestRaw []byte
	if err := tx.QueryRow(ctx, `
		SELECT manifest FROM extension_versions WHERE id = $1`, want.Active.VersionID).Scan(&manifestRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read lifecycle settings surface manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.Type != TypePlugin {
		return nil
	}
	if _, err := s.settingsSurfaceFields.SyncManifest(ctx, manifest); err != nil {
		return fmt.Errorf("%w: settings surface fields: %v", ErrPreflightFailed, err)
	}
	return nil
}

// WithSettingsSurfaceFields 注入设置字段投影器（链式可选）。
// nil = 不投影（既有测试与未接线装配的行为）。
func (s *PostgresStore) WithSettingsSurfaceFields(projector SettingsSurfaceFieldProjector) *PostgresStore {
	if s != nil {
		s.settingsSurfaceFields = projector
	}
	return s
}

// projectSettingsSurfaceFields 在 enable 提交前投影声明；失败返回 preflight 错误，
// 由 defer tx.Rollback 撤销 status 变更。
//
// 投影本身走独立连接（entity-meta 有它自己的事务边界），因此极端情况下可能出现
// 「字段已落库、status 回滚」的残留：它们 enabled=true 但没有插件引用，读模型
// 不可见，下一次 enable 或启动 reconcile 会重新收敛。
func (s *PostgresStore) projectSettingsSurfaceFields(ctx context.Context, id string, manifestRaw []byte) error {
	if s.settingsSurfaceFields == nil || len(manifestRaw) == 0 {
		return nil
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		// 清单读不出来时不能假装投影成功：enabled 会让读模型与存储彻底分叉。
		return fmt.Errorf("%w: settings surface fields: decode manifest: %v", ErrPreflightFailed, err)
	}
	if manifest.Type != "" && manifest.Type != TypePlugin {
		return nil
	}
	if _, err := s.settingsSurfaceFields.SyncManifest(ctx, manifest); err != nil {
		return fmt.Errorf("%w: settings surface fields: %v", ErrPreflightFailed, err)
	}
	return nil
}
