package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// LifecycleMigrationPreflight exposes the exact migration plan without
// acquiring an execution lock, provisioning PostgreSQL resources, or writing
// migration state. Implementations must not execute package migration SQL.
type LifecycleMigrationPreflight interface {
	PreflightLifecycleMigration(context.Context, LifecycleMigrationEnginePlan) (LifecycleMigrationPreflightResult, error)
}

type LifecycleMigrationPreflightResult struct {
	OperationID         int64
	Operation           extensions.LifecycleMachineOperation
	Mode                LifecycleBoundaryMigrationMode
	StepID              string
	Attempt             int
	PlanDigest          string
	DryRunDigest        string
	SchemaName          string
	OwnerRoleName       string
	Source              *LifecycleMigrationPreflightArtifact
	Target              LifecycleMigrationPreflightArtifact
	Steps               []LifecycleMigrationPreflightStep
	Warnings            []string
	HasNonTransactional bool
	Backup              LifecycleMigrationBackupRequirement
}

type LifecycleMigrationPreflightArtifact struct {
	ExtensionID             string
	Version                 string
	VersionID               int64
	PackageDigest           string
	MigrationsDigest        string
	DatabaseContractVersion string
	DatabaseGrants          []string
	BackupRequired          bool
	BackupStrategy          string
}

type LifecycleMigrationPreflightStep struct {
	Position          int
	Direction         string
	MigrationID       string
	ContractVersion   string
	PackagePath       string
	Checksum          string
	TransactionPolicy string
	ExecutionMode     string
	NoTransaction     bool
	WarningCode       string
	Artifact          LifecycleMigrationPreflightArtifact
	StatementDigests  []string
}

type LifecycleMigrationBackupRequirement struct {
	Required   bool
	Strategies []string
}

// PreflightLifecycleMigration performs the same exact-artifact discovery and
// Goose parsing used by execution. Goose is connected only to the in-memory
// statement recorder, so package SQL is inspected but never sent to PostgreSQL.
func (e *PostgresLifecycleMigrationEngine) PreflightLifecycleMigration(
	ctx context.Context,
	plan LifecycleMigrationEnginePlan,
) (LifecycleMigrationPreflightResult, error) {
	if e == nil || e.pool == nil || ctx == nil {
		return LifecycleMigrationPreflightResult{}, ErrExtensionDatabaseMigrationInvalid
	}
	if err := ctx.Err(); err != nil {
		return LifecycleMigrationPreflightResult{}, err
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return LifecycleMigrationPreflightResult{}, err
	}
	defer tx.Rollback(ctx)
	dryRun, err := discoverExtensionDatabaseMigrationPlan(ctx, tx, plan)
	if err != nil {
		return LifecycleMigrationPreflightResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleMigrationPreflightResult{}, err
	}
	return lifecycleMigrationPreflightResult(dryRun), nil
}

func lifecycleMigrationPreflightResult(
	dryRun extensionDatabaseMigrationDryRun,
) LifecycleMigrationPreflightResult {
	result := LifecycleMigrationPreflightResult{
		OperationID:         dryRun.EnginePlan.OperationID,
		Operation:           dryRun.EnginePlan.Operation,
		Mode:                dryRun.EnginePlan.Mode,
		StepID:              dryRun.EnginePlan.StepID,
		Attempt:             dryRun.EnginePlan.Attempt,
		PlanDigest:          dryRun.EnginePlan.PlanDigest,
		DryRunDigest:        dryRun.Digest,
		SchemaName:          dryRun.Identifiers.Schema,
		OwnerRoleName:       dryRun.Identifiers.OwnerRole,
		Target:              lifecycleMigrationPreflightArtifact(dryRun.Target),
		HasNonTransactional: dryRun.HasNonTransactional,
	}
	if dryRun.Source != nil {
		source := lifecycleMigrationPreflightArtifact(*dryRun.Source)
		result.Source = &source
	}
	result.Backup = lifecycleMigrationBackupRequirement(result.Source, result.Target)
	warnings := make(map[string]struct{})
	result.Steps = make([]LifecycleMigrationPreflightStep, 0, len(dryRun.Steps))
	for _, step := range dryRun.Steps {
		executionMode := "transactional"
		if !step.Transactional {
			executionMode = "non_transactional"
		}
		if step.WarningCode != "" {
			if _, seen := warnings[step.WarningCode]; !seen {
				warnings[step.WarningCode] = struct{}{}
				result.Warnings = append(result.Warnings, step.WarningCode)
			}
		}
		result.Steps = append(result.Steps, LifecycleMigrationPreflightStep{
			Position: step.Position, Direction: step.Direction,
			MigrationID: step.Declaration.ID, ContractVersion: step.Declaration.ContractVersion,
			PackagePath: step.Declaration.Path, Checksum: step.Declaration.Digest,
			TransactionPolicy: step.Declaration.Transaction, ExecutionMode: executionMode,
			NoTransaction: !step.Transactional, WarningCode: step.WarningCode,
			Artifact:         lifecycleMigrationPreflightArtifact(step.Artifact),
			StatementDigests: extensionDatabaseMigrationStatementDigests(step.Statements),
		})
	}
	return result
}

func lifecycleMigrationPreflightArtifact(
	artifact extensionDatabaseExactMigrationArtifact,
) LifecycleMigrationPreflightArtifact {
	return LifecycleMigrationPreflightArtifact{
		ExtensionID: artifact.ExtensionID, Version: artifact.Version,
		VersionID: artifact.VersionID, PackageDigest: artifact.PackageDigest,
		MigrationsDigest:        artifact.MigrationsDigest,
		DatabaseContractVersion: artifact.Database.ContractVersion,
		DatabaseGrants:          extensionmanifest.DatabaseGrants(&artifact.Database),
		BackupRequired:          artifact.Database.Backup.Required,
		BackupStrategy:          artifact.Database.Backup.Strategy,
	}
}

func lifecycleMigrationBackupRequirement(
	source *LifecycleMigrationPreflightArtifact,
	target LifecycleMigrationPreflightArtifact,
) LifecycleMigrationBackupRequirement {
	result := LifecycleMigrationBackupRequirement{}
	seen := make(map[string]struct{})
	appendArtifact := func(artifact LifecycleMigrationPreflightArtifact) {
		if !artifact.BackupRequired {
			return
		}
		result.Required = true
		strategy := strings.TrimSpace(artifact.BackupStrategy)
		if strategy == "" {
			return
		}
		if _, exists := seen[strategy]; exists {
			return
		}
		seen[strategy] = struct{}{}
		result.Strategies = append(result.Strategies, strategy)
	}
	if source != nil {
		appendArtifact(*source)
	}
	appendArtifact(target)
	return result
}

func extensionDatabaseMigrationStatementDigests(statements []string) []string {
	digests := make([]string, 0, len(statements))
	for _, statement := range statements {
		digest := sha256.Sum256([]byte(statement))
		digests = append(digests, hex.EncodeToString(digest[:]))
	}
	return digests
}

var _ LifecycleMigrationPreflight = (*PostgresLifecycleMigrationEngine)(nil)
