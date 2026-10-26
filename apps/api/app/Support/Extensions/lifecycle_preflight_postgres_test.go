package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestLifecycleStaticPreflightCoversSixOperationsWithoutStartingCode(t *testing.T) {
	source := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.demo", "1.0.0", strings.Repeat("a", 64), "preflight.lifecycle@1", 101,
	))
	target := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.demo", "2.0.0", strings.Repeat("b", 64), "preflight.lifecycle@2", 102,
	))
	source.Status = extensions.StatusEnabled
	target.Status = extensions.StatusInstalled
	facts := &lifecycleStaticCompatibilityFacts{}
	trust := &lifecycleStaticTrustFacts{trusted: true}
	preflight := NewProductionLifecycleBoundaryPreflight(ProductionLifecycleBoundaryPreflightConfig{
		Inventory:     lifecycleStaticInventory{items: []extensions.Extension{source}},
		Compatibility: facts,
		Trust:         trust,
	})

	tests := []LifecycleStaticPreflightRequest{
		{Operation: extensions.LifecycleMachineInstall, TargetExtension: target},
		{Operation: extensions.LifecycleMachineEnable, TargetExtension: target},
		{Operation: extensions.LifecycleMachineDisable, SourceExtension: &source, TargetExtension: source},
		{Operation: extensions.LifecycleMachineUpgrade, SourceExtension: &source, TargetExtension: target},
		{Operation: extensions.LifecycleMachineRollback, SourceExtension: &target, TargetExtension: source},
		{Operation: extensions.LifecycleMachineUninstall, SourceExtension: &source, TargetExtension: source},
	}
	for _, request := range tests {
		if err := preflight.CheckLifecycleStaticPreflight(t.Context(), request); err != nil {
			t.Fatalf("%s preflight: %v", request.Operation, err)
		}
	}
	if facts.checks != len(tests) || facts.starts != 0 {
		t.Fatalf("static compatibility calls: checks=%d starts=%d", facts.checks, facts.starts)
	}
	if trust.calls != 3 {
		t.Fatalf("live trust calls = %d, want activation operations only", trust.calls)
	}
}

func TestLifecycleStaticPreflightUsesExactCandidateAndBlocksDeactivationDependants(t *testing.T) {
	source := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.provider", "1.0.0", strings.Repeat("a", 64), "preflight.lifecycle@1", 111,
	))
	target := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.provider", "2.0.0", strings.Repeat("b", 64), "preflight.lifecycle@2", 112,
	))
	consumer := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.consumer", "1.0.0", strings.Repeat("c", 64), "preflight.consumer.lifecycle@1", 113,
	))
	source.Status, target.Status, consumer.Status = extensions.StatusEnabled, extensions.StatusInstalled, extensions.StatusEnabled
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: target.ID, Version: "^2.0.0", Kind: "required",
	}}
	preflight := NewProductionLifecycleBoundaryPreflight(ProductionLifecycleBoundaryPreflightConfig{
		Inventory:     lifecycleStaticInventory{items: []extensions.Extension{source, consumer}},
		Compatibility: &lifecycleStaticCompatibilityFacts{},
		Trust:         &lifecycleStaticTrustFacts{trusted: true},
	})
	if err := preflight.CheckLifecycleStaticPreflight(t.Context(), LifecycleStaticPreflightRequest{
		Operation: extensions.LifecycleMachineUpgrade, SourceExtension: &source, TargetExtension: target,
	}); err != nil {
		t.Fatalf("candidate replacement preflight: %v", err)
	}
	if err := preflight.CheckLifecycleStaticPreflight(t.Context(), LifecycleStaticPreflightRequest{
		Operation: extensions.LifecycleMachineDisable, SourceExtension: &source, TargetExtension: source,
	}); err == nil || !errors.Is(err, extensions.ErrPreflightFailed) {
		t.Fatalf("deactivation dependency error = %v", err)
	}
}

func TestLifecycleRollbackUsesFrozenAuthorityWithoutConsultingLiveTrust(t *testing.T) {
	source := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.rollback", "2.0.0", strings.Repeat("b", 64), "preflight.rollback.lifecycle@2", 142,
	))
	target := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.rollback", "1.0.0", strings.Repeat("a", 64), "preflight.rollback.lifecycle@1", 141,
	))
	source.Status, target.Status = extensions.StatusEnabled, extensions.StatusEnabled
	trust := &lifecycleStaticTrustFacts{trusted: false}
	preflight := NewProductionLifecycleBoundaryPreflight(ProductionLifecycleBoundaryPreflightConfig{
		Inventory:     lifecycleStaticInventory{items: []extensions.Extension{source}},
		Compatibility: &lifecycleStaticCompatibilityFacts{},
		Trust:         trust,
	})
	if err := preflight.CheckLifecycleStaticPreflight(t.Context(), LifecycleStaticPreflightRequest{
		Operation: extensions.LifecycleMachineRollback, SourceExtension: &source, TargetExtension: target,
	}); err != nil {
		t.Fatalf("rollback rejected frozen authority because live trust is absent: %v", err)
	}
	if trust.calls != 0 {
		t.Fatalf("rollback consulted live trust %d time(s)", trust.calls)
	}
}

func TestLifecycleStaticPreflightFailsBeforeAnyProcessStart(t *testing.T) {
	target := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.blocked", "1.0.0", strings.Repeat("d", 64), "preflight.blocked.lifecycle@1", 121,
	))
	facts := &lifecycleStaticCompatibilityFacts{err: errors.New("unsupported Host API")}
	preflight := NewProductionLifecycleBoundaryPreflight(ProductionLifecycleBoundaryPreflightConfig{
		Inventory: lifecycleStaticInventory{}, Compatibility: facts,
		Trust: &lifecycleStaticTrustFacts{trusted: true},
	})
	err := preflight.CheckLifecycleStaticPreflight(t.Context(), LifecycleStaticPreflightRequest{
		Operation: extensions.LifecycleMachineInstall, TargetExtension: target,
	})
	if err == nil || !errors.Is(err, extensions.ErrPreflightFailed) {
		t.Fatalf("preflight error = %v", err)
	}
	if facts.starts != 0 {
		t.Fatalf("static preflight started code %d time(s)", facts.starts)
	}
}

func TestLifecycleStaticPreflightBlocksDeclaredMigrationsBeforeProcessStart(t *testing.T) {
	target := lifecycleStaticValidArtifact(exactCoordinatorTestExtension(
		"preflight.migration", "1.0.0", strings.Repeat("a", 64), "preflight.migration.lifecycle@1", 131,
	))
	migration := lifecycleMigrationTestDeclaration(target.ID, "install", strings.Repeat("e", 64))
	target.Manifest.Migrations = []extensions.ManifestMigration{migration}
	target.Manifest.PackageFiles = append(target.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: target.ID + ".file.migration", Kind: "migration", Path: migration.Path, Digest: migration.Digest,
	})
	facts := &lifecycleStaticCompatibilityFacts{}
	preflight := NewProductionLifecycleBoundaryPreflight(ProductionLifecycleBoundaryPreflightConfig{
		Inventory: lifecycleStaticInventory{}, Compatibility: facts,
		Trust:      &lifecycleStaticTrustFacts{trusted: true},
		Migrations: lifecycleStaticMigrationFacts{prepare: false},
	})
	err := preflight.CheckLifecycleStaticPreflight(t.Context(), LifecycleStaticPreflightRequest{
		Operation: extensions.LifecycleMachineInstall, TargetExtension: target,
	})
	if !errors.Is(err, ErrLifecyclePreflightMigrations) {
		t.Fatalf("declared migration preflight error = %v", err)
	}
	if facts.starts != 0 {
		t.Fatalf("declared migration preflight started code %d time(s)", facts.starts)
	}
}

type lifecycleStaticInventory struct {
	items []extensions.Extension
	err   error
}

func (i lifecycleStaticInventory) List(context.Context) ([]extensions.Extension, error) {
	return append([]extensions.Extension(nil), i.items...), i.err
}

type lifecycleStaticCompatibilityFacts struct {
	checks int
	starts int
	err    error
}

func (f *lifecycleStaticCompatibilityFacts) Check(context.Context, extensions.Extension) error {
	f.checks++
	return f.err
}

// Start deliberately exists to prove the preflight dependency is narrowed to
// RuntimePreflight and never invokes a process-capable method.
func (f *lifecycleStaticCompatibilityFacts) Start(context.Context, extensions.Extension) error {
	f.starts++
	return errors.New("static preflight must not start code")
}

type lifecycleStaticTrustFacts struct {
	trusted bool
	calls   int
}

type lifecycleStaticMigrationFacts struct {
	prepare bool
	ready   bool
}

func (f lifecycleStaticMigrationFacts) LifecycleArtifactMigrationReady(context.Context, extensions.Extension) (bool, error) {
	return f.ready, nil
}

func (f lifecycleStaticMigrationFacts) CanPrepareLifecycleMigrations(context.Context, LifecycleStaticPreflightRequest) (bool, error) {
	return f.prepare, nil
}

func lifecycleStaticValidArtifact(item extensions.Extension) extensions.Extension {
	backendDigest := strings.Repeat("f", 64)
	item.Name = "Lifecycle Static Fixture"
	item.Manifest.Name = item.Name
	item.Manifest.Description = "Static lifecycle preflight fixture."
	item.Manifest.URL = "https://example.com/" + item.ID
	item.Manifest.Author = extensions.ManifestAuthor{Name: "SForum Tests"}
	item.Manifest.SForumVersion = ">=0.1.0"
	item.Manifest.Backend.Entry = "backend/plugin"
	item.Manifest.Backend.RPC = "hashicorp-go-plugin"
	item.Manifest.Backend.Digest = backendDigest
	item.Manifest.PackageFiles = []extensions.ManifestPackageFile{{
		ID: item.ID + ".file.backend", Kind: "executable",
		Path: item.Manifest.Backend.Entry, Digest: backendDigest,
	}}
	return item
}

func (f *lifecycleStaticTrustFacts) TrustedArtifact(context.Context, extensions.Extension) (bool, error) {
	f.calls++
	return f.trusted, nil
}
