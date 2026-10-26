package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	extensionDatabaseMigrationMaximumBytes            = 16 << 20
	extensionDatabaseMigrationFailureInvalidPlan      = "migration.invalid_plan"
	extensionDatabaseMigrationFailureArtifactConflict = "migration.artifact_conflict"
	extensionDatabaseMigrationFailureAuthority        = "migration.authority_unsupported"
	extensionDatabaseMigrationFailurePackagePath      = "migration.package_path_invalid"
	extensionDatabaseMigrationFailureChecksumDrift    = "migration.checksum_drift"
)

var (
	ErrExtensionDatabaseMigrationInvalid          = errors.New("extension database migration plan is invalid")
	ErrExtensionDatabaseMigrationArtifactConflict = errors.New("extension database migration exact artifact conflict")
	ErrExtensionDatabaseMigrationPackage          = errors.New("extension database migration package is invalid")
	ErrExtensionDatabaseMigrationChecksumDrift    = errors.New("extension database migration checksum drift")
)

type extensionDatabaseMigrationFailure struct {
	code string
	err  error
}

func (e extensionDatabaseMigrationFailure) Error() string { return e.err.Error() }
func (e extensionDatabaseMigrationFailure) Unwrap() error { return e.err }

func newExtensionDatabaseMigrationFailure(code string, err error) error {
	return extensionDatabaseMigrationFailure{code: code, err: err}
}

func extensionDatabaseMigrationFailureCode(err error) string {
	var failure extensionDatabaseMigrationFailure
	if errors.As(err, &failure) && failure.code != "" {
		return failure.code
	}
	return "migration.execution_failed"
}

type extensionDatabaseExactMigrationArtifact struct {
	LifecycleMigrationArtifact
	PackagePath string
	Manifest    extensions.Manifest
	Database    extensions.ManifestDatabase
}

type extensionDatabaseMigrationStepPlan struct {
	Position      int
	Direction     string
	Declaration   extensions.ManifestMigration
	Artifact      extensionDatabaseExactMigrationArtifact
	Statements    []string
	Transactional bool
	WarningCode   string
}

type extensionDatabaseMigrationDryRun struct {
	EnginePlan          LifecycleMigrationEnginePlan
	Identifiers         ExtensionDatabaseIdentifiers
	Source              *extensionDatabaseExactMigrationArtifact
	Target              extensionDatabaseExactMigrationArtifact
	Steps               []extensionDatabaseMigrationStepPlan
	Digest              string
	HasNonTransactional bool
	WarningCode         string
}

func discoverExtensionDatabaseMigrationPlan(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	plan LifecycleMigrationEnginePlan,
) (extensionDatabaseMigrationDryRun, error) {
	if querier == nil || ctx == nil {
		return extensionDatabaseMigrationDryRun{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, ErrExtensionDatabaseMigrationInvalid,
		)
	}
	if err := validateExtensionDatabaseMigrationEnginePlan(plan); err != nil {
		return extensionDatabaseMigrationDryRun{}, err
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(plan.Target.ExtensionID)
	if err != nil {
		return extensionDatabaseMigrationDryRun{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, ErrExtensionDatabaseMigrationInvalid,
		)
	}
	target, err := loadExactExtensionDatabaseMigrationArtifact(ctx, querier, plan.Target)
	if err != nil {
		return extensionDatabaseMigrationDryRun{}, err
	}
	var source *extensionDatabaseExactMigrationArtifact
	if plan.Source != nil {
		value, sourceErr := loadExactExtensionDatabaseMigrationArtifact(ctx, querier, *plan.Source)
		if sourceErr != nil {
			return extensionDatabaseMigrationDryRun{}, sourceErr
		}
		source = &value
	}
	transitions, err := buildExtensionDatabaseMigrationTransitions(source, target)
	if err != nil {
		return extensionDatabaseMigrationDryRun{}, err
	}
	for index := range transitions {
		step := &transitions[index]
		body, readErr := readExactExtensionDatabaseMigration(step.Artifact, step.Declaration)
		if readErr != nil {
			return extensionDatabaseMigrationDryRun{}, readErr
		}
		parsed, parseErr := parseExtensionDatabaseMigration(
			ctx, body, step.Direction, step.Declaration.Transaction,
		)
		if parseErr != nil {
			code := extensionDatabaseMigrationFailureParse
			if errors.Is(parseErr, ErrExtensionDatabaseMigrationPolicy) {
				code = extensionDatabaseMigrationFailurePolicy
			}
			return extensionDatabaseMigrationDryRun{}, newExtensionDatabaseMigrationFailure(code, parseErr)
		}
		step.Statements = parsed.Statements
		step.Transactional = parsed.Transactional
		step.WarningCode = parsed.WarningCode
	}
	digest, err := extensionDatabaseMigrationDryRunDigest(plan, identifiers, transitions)
	if err != nil {
		return extensionDatabaseMigrationDryRun{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, err,
		)
	}
	dryRun := extensionDatabaseMigrationDryRun{
		EnginePlan: plan, Identifiers: identifiers, Source: source, Target: target,
		Steps: transitions, Digest: digest,
	}
	for _, step := range transitions {
		if !step.Transactional {
			dryRun.HasNonTransactional = true
			dryRun.WarningCode = extensionDatabaseMigrationWarningNonTransactional
		}
	}
	return dryRun, nil
}

func validateExtensionDatabaseMigrationEnginePlan(plan LifecycleMigrationEnginePlan) error {
	invalid := func() error {
		return newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, ErrExtensionDatabaseMigrationInvalid,
		)
	}
	if plan.OperationID <= 0 || plan.StepID == "" || plan.StepID != strings.TrimSpace(plan.StepID) ||
		plan.Attempt <= 0 || !validLifecycleCleanupDigest(plan.PlanDigest) {
		return invalid()
	}
	if string(plan.Operation) != string(plan.Mode) ||
		(plan.Operation != extensions.LifecycleMachineInstall &&
			plan.Operation != extensions.LifecycleMachineUpgrade &&
			plan.Operation != extensions.LifecycleMachineRollback) {
		return invalid()
	}
	if err := validateExtensionDatabaseLifecycleArtifact(plan.Target); err != nil {
		return err
	}
	if plan.Operation == extensions.LifecycleMachineInstall {
		if plan.Source != nil {
			return invalid()
		}
		return nil
	}
	if plan.Source == nil || plan.Source.ExtensionID != plan.Target.ExtensionID {
		return invalid()
	}
	return validateExtensionDatabaseLifecycleArtifact(*plan.Source)
}

func validateExtensionDatabaseLifecycleArtifact(artifact LifecycleMigrationArtifact) error {
	if !extensionDatabaseIDPattern.MatchString(artifact.ExtensionID) ||
		artifact.Version == "" || artifact.Version != strings.TrimSpace(artifact.Version) ||
		artifact.VersionID <= 0 || !validLifecycleCleanupDigest(artifact.PackageDigest) ||
		!validLifecycleCleanupDigest(artifact.MigrationsDigest) {
		return newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, ErrExtensionDatabaseMigrationInvalid,
		)
	}
	declarations, digest, err := lifecycleMigrationDeclarations(artifact.Migrations)
	if err != nil || !reflect.DeepEqual(declarations, artifact.Migrations) || digest != artifact.MigrationsDigest {
		return newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureInvalidPlan, ErrExtensionDatabaseMigrationInvalid,
		)
	}
	return nil
}

func loadExactExtensionDatabaseMigrationArtifact(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	artifact LifecycleMigrationArtifact,
) (extensionDatabaseExactMigrationArtifact, error) {
	var extensionID, version, packageDigest, packagePath string
	var manifestJSON []byte
	err := querier.QueryRow(ctx, `
		SELECT extension_id, version, package_digest, package_path, manifest
		FROM extension_versions WHERE id = $1
	`, artifact.VersionID).Scan(&extensionID, &version, &packageDigest, &packagePath, &manifestJSON)
	if err != nil {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureArtifactConflict,
			fmt.Errorf("%w: load version %d: %v", ErrExtensionDatabaseMigrationArtifactConflict, artifact.VersionID, err),
		)
	}
	if extensionID != artifact.ExtensionID || version != artifact.Version ||
		packageDigest != artifact.PackageDigest || packagePath == "" || !filepath.IsAbs(packagePath) {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureArtifactConflict, ErrExtensionDatabaseMigrationArtifactConflict,
		)
	}
	var manifest extensions.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureArtifactConflict,
			fmt.Errorf("%w: decode manifest: %v", ErrExtensionDatabaseMigrationArtifactConflict, err),
		)
	}
	manifest = extensionmanifest.Normalize(manifest)
	if manifest.ID != artifact.ExtensionID || manifest.Version != artifact.Version ||
		!reflect.DeepEqual(manifest.Migrations, artifact.Migrations) || manifest.Database == nil {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureArtifactConflict, ErrExtensionDatabaseMigrationArtifactConflict,
		)
	}
	if !extensionmanifest.HasDatabaseGrant(manifest.Database, extensionmanifest.DatabaseGrantOwnSchema) {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureAuthority, ErrExtensionDatabaseAuthority,
		)
	}
	if !extensionDatabaseContractPattern.MatchString(manifest.Database.ContractVersion) {
		return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureArtifactConflict, ErrExtensionDatabaseMigrationArtifactConflict,
		)
	}
	for _, migration := range manifest.Migrations {
		if !manifestDeclaresExactMigrationFile(manifest, migration) {
			return extensionDatabaseExactMigrationArtifact{}, newExtensionDatabaseMigrationFailure(
				extensionDatabaseMigrationFailureArtifactConflict, ErrExtensionDatabaseMigrationArtifactConflict,
			)
		}
	}
	return extensionDatabaseExactMigrationArtifact{
		LifecycleMigrationArtifact: artifact, PackagePath: packagePath,
		Manifest: manifest, Database: *manifest.Database,
	}, nil
}

func manifestDeclaresExactMigrationFile(
	manifest extensions.Manifest,
	migration extensions.ManifestMigration,
) bool {
	for _, file := range manifest.PackageFiles {
		if file.Kind == "migration" && file.Path == migration.Path && file.Digest == migration.Digest {
			return true
		}
	}
	return false
}

func buildExtensionDatabaseMigrationTransitions(
	source *extensionDatabaseExactMigrationArtifact,
	target extensionDatabaseExactMigrationArtifact,
) ([]extensionDatabaseMigrationStepPlan, error) {
	sourceByID := make(map[string]extensions.ManifestMigration)
	if source != nil {
		for _, migration := range source.Migrations {
			sourceByID[migration.ID] = migration
		}
	}
	targetByID := make(map[string]extensions.ManifestMigration, len(target.Migrations))
	for _, migration := range target.Migrations {
		targetByID[migration.ID] = migration
		if previous, ok := sourceByID[migration.ID]; ok && !reflect.DeepEqual(previous, migration) {
			return nil, newExtensionDatabaseMigrationFailure(
				extensionDatabaseMigrationFailureChecksumDrift,
				fmt.Errorf("%w: migration %s changed", ErrExtensionDatabaseMigrationChecksumDrift, migration.ID),
			)
		}
	}
	steps := make([]extensionDatabaseMigrationStepPlan, 0)
	if source != nil {
		for index := len(source.Migrations) - 1; index >= 0; index-- {
			migration := source.Migrations[index]
			if _, retained := targetByID[migration.ID]; retained {
				continue
			}
			steps = append(steps, extensionDatabaseMigrationStepPlan{
				Direction: "down", Declaration: migration, Artifact: *source,
			})
		}
	}
	for _, migration := range target.Migrations {
		steps = append(steps, extensionDatabaseMigrationStepPlan{
			Direction: "up", Declaration: migration, Artifact: target,
		})
	}
	for index := range steps {
		steps[index].Position = index + 1
	}
	return steps, nil
}

func readExactExtensionDatabaseMigration(
	artifact extensionDatabaseExactMigrationArtifact,
	declaration extensions.ManifestMigration,
) ([]byte, error) {
	safe, ok := extensionmanifest.SafeArchivePath(declaration.Path)
	if !ok || safe != declaration.Path || !strings.HasSuffix(safe, ".sql") ||
		!validLifecycleCleanupDigest(declaration.Digest) {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	root, err := os.OpenRoot(artifact.PackagePath)
	if err != nil {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath,
			fmt.Errorf("%w: open package root: %v", ErrExtensionDatabaseMigrationPackage, err),
		)
	}
	defer root.Close()
	before, err := root.Lstat(safe)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&fs.ModeSymlink != 0 {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	handle, err := root.Open(safe)
	if err != nil {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	body, err := io.ReadAll(io.LimitReader(handle, extensionDatabaseMigrationMaximumBytes+1))
	if err != nil || len(body) == 0 || len(body) > extensionDatabaseMigrationMaximumBytes {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	after, err := handle.Stat()
	if err != nil || !os.SameFile(opened, after) || int64(len(body)) != after.Size() {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailurePackagePath, ErrExtensionDatabaseMigrationPackage,
		)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != declaration.Digest {
		return nil, newExtensionDatabaseMigrationFailure(
			extensionDatabaseMigrationFailureChecksumDrift, ErrExtensionDatabaseMigrationChecksumDrift,
		)
	}
	return body, nil
}

func extensionDatabaseMigrationDryRunDigest(
	plan LifecycleMigrationEnginePlan,
	identifiers ExtensionDatabaseIdentifiers,
	steps []extensionDatabaseMigrationStepPlan,
) (string, error) {
	type digestStep struct {
		Position      int      `json:"position"`
		Direction     string   `json:"direction"`
		MigrationID   string   `json:"migrationId"`
		Contract      string   `json:"contract"`
		Path          string   `json:"path"`
		Checksum      string   `json:"checksum"`
		Policy        string   `json:"policy"`
		Transactional bool     `json:"transactional"`
		Statements    []string `json:"statements"`
	}
	document := struct {
		PlanDigest string       `json:"planDigest"`
		Schema     string       `json:"schema"`
		OwnerRole  string       `json:"ownerRole"`
		Steps      []digestStep `json:"steps"`
	}{PlanDigest: plan.PlanDigest, Schema: identifiers.Schema, OwnerRole: identifiers.OwnerRole}
	for _, step := range steps {
		statementDigests := make([]string, 0, len(step.Statements))
		for _, statement := range step.Statements {
			digest := sha256.Sum256([]byte(statement))
			statementDigests = append(statementDigests, hex.EncodeToString(digest[:]))
		}
		document.Steps = append(document.Steps, digestStep{
			Position: step.Position, Direction: step.Direction,
			MigrationID: step.Declaration.ID, Contract: step.Declaration.ContractVersion,
			Path: step.Declaration.Path, Checksum: step.Declaration.Digest,
			Policy: step.Declaration.Transaction, Transactional: step.Transactional,
			Statements: statementDigests,
		})
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode extension database dry-run: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
