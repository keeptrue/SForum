package extensionsruntime

import (
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestLifecycleMigrationPlanFencesAllSupportedModes(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		position  int
		mode      LifecycleBoundaryMigrationMode
	}{
		{extensions.LifecycleMachineInstall, 2, LifecycleBoundaryMigrationInstall},
		{extensions.LifecycleMachineUpgrade, 4, LifecycleBoundaryMigrationUpgrade},
		{extensions.LifecycleMachineRollback, 5, LifecycleBoundaryMigrationRollback},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, test.operation, test.position)
			request.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
				lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "target", strings.Repeat("d", 64)),
			}
			if request.SourceExtension != nil {
				request.SourceExtension.Manifest.Migrations = []extensions.ManifestMigration{
					lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "source", strings.Repeat("e", 64)),
				}
			}
			plan, err := lifecycleMigrationPlanFor(request, test.mode, true)
			if err != nil {
				t.Fatal(err)
			}
			if plan.OperationID != request.OperationID || plan.StepID != request.StepID ||
				plan.Attempt != request.Attempt || plan.PlanDigest == "" ||
				plan.Target.PackageDigest != request.TargetExtension.PackageDigest {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestLifecycleMigrationPlanRejectsModeStepAndArtifactDrift(t *testing.T) {
	base := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 4)
	base.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(base.TargetExtension.ID, "target", strings.Repeat("d", 64)),
	}
	base.SourceExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(base.TargetExtension.ID, "source", strings.Repeat("e", 64)),
	}
	tests := []struct {
		name string
		mode LifecycleBoundaryMigrationMode
		edit func(*LifecycleBoundaryRequest)
	}{
		{"wrong mode", LifecycleBoundaryMigrationRollback, func(*LifecycleBoundaryRequest) {}},
		{"wrong step", LifecycleBoundaryMigrationUpgrade, func(r *LifecycleBoundaryRequest) { r.StepID += ".drift" }},
		{"stale target version", LifecycleBoundaryMigrationUpgrade, func(r *LifecycleBoundaryRequest) {
			r.TargetExtension.ActiveVersionID++
		}},
		{"invalid migration digest", LifecycleBoundaryMigrationUpgrade, func(r *LifecycleBoundaryRequest) {
			r.TargetExtension.Manifest.Migrations[0].Digest = strings.Repeat("F", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneLifecycleBoundaryRequest(base)
			test.edit(&request)
			if _, err := lifecycleMigrationPlanFor(request, test.mode, true); err == nil {
				t.Fatal("expected exact migration plan rejection")
			}
		})
	}
}

func TestLifecycleMigrationBlockedErrorIsRetryable(t *testing.T) {
	err := lifecycleMigrationBlockedError{
		reason: "lifecycle.migration_engine_unavailable", detail: "P5 is not configured",
	}
	if !errors.Is(err, ErrLifecycleMigrationProofRequired) {
		t.Fatalf("blocked error = %v", err)
	}
	failure := err.LifecycleCoordinatorFailure()
	if !failure.Retryable || failure.Code != "lifecycle.migration_blocked" || failure.Reason == "" {
		t.Fatalf("failure = %#v", failure)
	}
}

func lifecycleMigrationTestDeclaration(extensionID, name, digest string) extensions.ManifestMigration {
	return extensions.ManifestMigration{
		ID:              extensionID + ".migration." + name,
		ContractVersion: extensionID + ".migration." + name + "@1",
		Path:            "migrations/" + name + ".sql", Digest: digest, Transaction: "required",
	}
}
