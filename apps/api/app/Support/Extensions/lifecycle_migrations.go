package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const (
	lifecycleMigrationStatusNotStarted  = "not_started"
	lifecycleMigrationStatusBlocked     = "blocked"
	lifecycleMigrationStatusExecuting   = "executing"
	lifecycleMigrationStatusTargetReady = "target_ready"

	lifecycleMigrationProofHostNoop = "host_noop"
	lifecycleMigrationProofP5       = "p5_engine"
	lifecycleMigrationProofReused   = "reused"
)

var (
	ErrLifecycleMigrationsInvalid       = errors.New("extension lifecycle migration boundary input is invalid")
	ErrLifecycleMigrationsConflict      = errors.New("extension lifecycle migration exact proof conflict")
	ErrLifecycleMigrationProofRequired  = errors.New("extension lifecycle real migration proof is required")
	ErrLifecycleMigrationEngineProofBad = errors.New("extension lifecycle migration engine returned an invalid proof")
)

// LifecycleMigrationEngine is the stable P5 handoff. Reconcile must be
// idempotent by PlanDigest and Inspect must read a durable exact-plan ledger.
// A one-shot success return or the legacy checksum-only ledger is insufficient.
type LifecycleMigrationEngine interface {
	ReconcileLifecycleMigration(context.Context, LifecycleMigrationEnginePlan) error
	InspectLifecycleMigration(context.Context, LifecycleMigrationEnginePlan) (LifecycleMigrationEngineProof, error)
}

type LifecycleMigrationEnginePlan struct {
	OperationID int64
	Operation   extensions.LifecycleMachineOperation
	StepID      string
	Attempt     int
	Mode        LifecycleBoundaryMigrationMode
	PlanDigest  string
	Source      *LifecycleMigrationArtifact
	Target      LifecycleMigrationArtifact
}

type LifecycleMigrationArtifact struct {
	ExtensionID      string
	Version          string
	PackageDigest    string
	VersionID        int64
	MigrationsDigest string
	Migrations       []extensions.ManifestMigration
}

type LifecycleMigrationEngineProof struct {
	ProofID          string
	ProofDigest      string
	PlanDigest       string
	TargetReady      bool
	SourceResumeSafe bool
}

type lifecycleMigrationPlan struct {
	OperationID int64
	Operation   extensions.LifecycleMachineOperation
	StepID      string
	Position    int
	Attempt     int
	Mode        LifecycleBoundaryMigrationMode
	PlanDigest  string
	Source      *LifecycleMigrationArtifact
	Target      LifecycleMigrationArtifact
}

func lifecycleMigrationPlanFor(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryMigrationMode,
	requireCanonicalStep bool,
) (lifecycleMigrationPlan, error) {
	call, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return lifecycleMigrationPlan{}, fmt.Errorf("%w: %v", ErrLifecycleMigrationsInvalid, err)
	}
	position, err := lifecycleMigrationPosition(request.Operation, mode)
	if err != nil {
		return lifecycleMigrationPlan{}, err
	}
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil || position >= len(path) || path[position].Action != "" {
		return lifecycleMigrationPlan{}, ErrLifecycleMigrationsInvalid
	}
	stepID := fmt.Sprintf("lifecycle.%s.%02d.host.%s", request.Operation, position, path[position].State)
	if requireCanonicalStep && (request.Position != position || request.StepID != stepID) {
		return lifecycleMigrationPlan{}, ErrLifecycleMigrationsInvalid
	}

	targetMigrations, targetDigest, err := lifecycleMigrationDeclarations(request.TargetExtension.Manifest.Migrations)
	if err != nil {
		return lifecycleMigrationPlan{}, fmt.Errorf("%w: target declarations: %v", ErrLifecycleMigrationsInvalid, err)
	}
	target := LifecycleMigrationArtifact{
		ExtensionID: call.Target.ExtensionID, Version: call.Target.ExtensionVersion,
		PackageDigest: call.Target.PackageDigest, VersionID: call.Target.VersionID,
		MigrationsDigest: targetDigest, Migrations: targetMigrations,
	}
	plan := lifecycleMigrationPlan{
		OperationID: request.OperationID, Operation: request.Operation, StepID: stepID,
		Position: position, Attempt: request.Attempt, Mode: mode, Target: target,
	}
	if call.Source.Present {
		sourceMigrations, sourceDigest, sourceErr := lifecycleMigrationDeclarations(request.SourceExtension.Manifest.Migrations)
		if sourceErr != nil {
			return lifecycleMigrationPlan{}, fmt.Errorf("%w: source declarations: %v", ErrLifecycleMigrationsInvalid, sourceErr)
		}
		plan.Source = &LifecycleMigrationArtifact{
			ExtensionID: call.Source.ExtensionID, Version: call.Source.ExtensionVersion,
			PackageDigest: call.Source.PackageDigest, VersionID: call.Source.VersionID,
			MigrationsDigest: sourceDigest, Migrations: sourceMigrations,
		}
	}
	plan.PlanDigest, err = lifecycleMigrationPlanDigest(plan)
	if err != nil {
		return lifecycleMigrationPlan{}, err
	}
	return plan, nil
}

func lifecycleMigrationPosition(
	operation extensions.LifecycleMachineOperation,
	mode LifecycleBoundaryMigrationMode,
) (int, error) {
	switch {
	case operation == extensions.LifecycleMachineInstall && mode == LifecycleBoundaryMigrationInstall:
		return 2, nil
	case operation == extensions.LifecycleMachineUpgrade && mode == LifecycleBoundaryMigrationUpgrade:
		return 4, nil
	case operation == extensions.LifecycleMachineRollback && mode == LifecycleBoundaryMigrationRollback:
		return 5, nil
	default:
		return 0, ErrLifecycleMigrationsInvalid
	}
}

func lifecycleMigrationDeclarations(
	items []extensions.ManifestMigration,
) ([]extensions.ManifestMigration, string, error) {
	clone := append([]extensions.ManifestMigration(nil), items...)
	for _, item := range clone {
		if item.ID == "" || item.ID != strings.TrimSpace(item.ID) ||
			item.ContractVersion == "" || item.ContractVersion != strings.TrimSpace(item.ContractVersion) ||
			item.Path == "" || item.Path != strings.TrimSpace(item.Path) ||
			!validLifecycleCleanupDigest(item.Digest) {
			return nil, "", ErrLifecycleMigrationsInvalid
		}
		switch item.Transaction {
		case "required", "forbidden", "auto":
		default:
			return nil, "", ErrLifecycleMigrationsInvalid
		}
	}
	encoded, err := json.Marshal(clone)
	if err != nil {
		return nil, "", fmt.Errorf("encode migration declarations: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return clone, hex.EncodeToString(digest[:]), nil
}

func lifecycleMigrationPlanDigest(plan lifecycleMigrationPlan) (string, error) {
	type digestArtifact struct {
		ExtensionID      string                         `json:"extensionId"`
		Version          string                         `json:"version"`
		PackageDigest    string                         `json:"packageDigest"`
		VersionID        int64                          `json:"versionId"`
		MigrationsDigest string                         `json:"migrationsDigest"`
		Migrations       []extensions.ManifestMigration `json:"migrations"`
	}
	toDigest := func(value LifecycleMigrationArtifact) digestArtifact {
		return digestArtifact{
			ExtensionID: value.ExtensionID, Version: value.Version,
			PackageDigest: value.PackageDigest, VersionID: value.VersionID,
			MigrationsDigest: value.MigrationsDigest,
			Migrations:       append([]extensions.ManifestMigration(nil), value.Migrations...),
		}
	}
	var source *digestArtifact
	if plan.Source != nil {
		value := toDigest(*plan.Source)
		source = &value
	}
	document := struct {
		OperationID int64                                `json:"operationId"`
		Operation   extensions.LifecycleMachineOperation `json:"operation"`
		StepID      string                               `json:"stepId"`
		Position    int                                  `json:"position"`
		Mode        LifecycleBoundaryMigrationMode       `json:"mode"`
		Source      *digestArtifact                      `json:"source,omitempty"`
		Target      digestArtifact                       `json:"target"`
	}{plan.OperationID, plan.Operation, plan.StepID, plan.Position, plan.Mode, source, toDigest(plan.Target)}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode lifecycle migration plan: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (p lifecycleMigrationPlan) enginePlan() LifecycleMigrationEnginePlan {
	plan := LifecycleMigrationEnginePlan{
		OperationID: p.OperationID, Operation: p.Operation, StepID: p.StepID,
		Attempt: p.Attempt, Mode: p.Mode, PlanDigest: p.PlanDigest,
		Target: cloneLifecycleMigrationArtifact(p.Target),
	}
	if p.Source != nil {
		value := cloneLifecycleMigrationArtifact(*p.Source)
		plan.Source = &value
	}
	return plan
}

func cloneLifecycleMigrationArtifact(value LifecycleMigrationArtifact) LifecycleMigrationArtifact {
	value.Migrations = append([]extensions.ManifestMigration(nil), value.Migrations...)
	return value
}

type lifecycleMigrationBlockedError struct {
	reason string
	detail string
}

func (e lifecycleMigrationBlockedError) Error() string {
	if e.detail == "" {
		return ErrLifecycleMigrationProofRequired.Error()
	}
	return ErrLifecycleMigrationProofRequired.Error() + ": " + e.detail
}

func (e lifecycleMigrationBlockedError) Unwrap() error {
	return ErrLifecycleMigrationProofRequired
}

func (e lifecycleMigrationBlockedError) LifecycleCoordinatorFailure() extensions.LifecycleExecutionError {
	reason := e.reason
	if reason == "" {
		reason = "lifecycle.migration_proof_required"
	}
	return extensions.LifecycleExecutionError{
		Code: "lifecycle.migration_blocked", Reason: reason,
		Message: e.Error(), Retryable: true,
	}
}
