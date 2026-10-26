package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentRegistrySafeModeIsStickyRevisionedAndDetached(t *testing.T) {
	id := "component.safe-mode"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-safe-mode"); err != nil {
		t.Fatal(err)
	}
	before := registry.Snapshot()
	if err := registry.RestoreRuntimes([]extensions.Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if !snapshot.SafeMode || snapshot.Revision != before.Revision+1 ||
		len(snapshot.Contributions) != 0 || len(snapshot.Selections) != 0 || len(snapshot.Targets) == 0 {
		t.Fatalf("Safe Mode snapshot = %#v", snapshot)
	}
	cacheRevision := snapshot.Revision
	if err := registry.RestoreRuntimes(nil, true); err != nil || registry.Snapshot().Revision != cacheRevision {
		t.Fatalf("idempotent Safe Mode changed cache revision: revision=%d err=%v", registry.Snapshot().Revision, err)
	}

	// Inspector callers cannot mutate the recovery snapshot or Core catalog.
	snapshot.Targets[0].ID = "mutated"
	snapshot.Targets[0].Owners = append(snapshot.Targets[0].Owners, "mutated")
	fresh := registry.Snapshot()
	if fresh.Targets[0].ID == "mutated" || len(fresh.Targets[0].Owners) != 1 || !fresh.SafeMode {
		t.Fatalf("Safe Mode snapshot shared state = %#v", fresh.Targets[0])
	}

	for operation, err := range map[string]error{
		"replace runtime": registry.ReplaceRuntime(extension, "runtime-republish"),
		"replace all": registry.ReplaceAll([]ComponentRuntimeSnapshot{{
			Extension: extension, InstanceID: "runtime-republish",
		}}),
		"startup restore": registry.RestoreRuntimes([]extensions.Extension{extension}, false),
	} {
		if !errors.Is(err, ErrComponentRegistrySafeMode) {
			t.Fatalf("%s after Safe Mode error=%v", operation, err)
		}
	}
	if _, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: extension.Manifest.Components[0].ID, ExpectedRevision: fresh.Revision,
	}); !errors.Is(err, ErrComponentRegistrySafeMode) {
		t.Fatalf("selection after Safe Mode error=%v", err)
	}
	if _, err := registry.ResetReplaceProvider(ResetComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ExpectedRevision: fresh.Revision,
	}); !errors.Is(err, ErrComponentRegistrySafeMode) {
		t.Fatalf("reset after Safe Mode error=%v", err)
	}

	theme := componentTestExtension(t, "component.safe-theme", extensions.TypeTheme,
		componentTestContribution(
			"component.safe-theme", "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	if err := registry.ValidateThemeTransition(&theme, nil); !errors.Is(err, ErrComponentRegistrySafeMode) {
		t.Fatalf("theme preflight after Safe Mode error=%v", err)
	}
	if err := registry.PublishThemeTransition(&theme, nil, 1); !errors.Is(err, ErrComponentRegistrySafeMode) {
		t.Fatalf("theme publication after Safe Mode error=%v", err)
	}
	if _, ok := registry.RuntimeSnapshot(extension.ID); ok || registry.AdmitPublicComponent(extension, extension.Manifest.Components[0]) {
		t.Fatal("Safe Mode retained an extension runtime admission")
	}

	plan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || plan.Revision != cacheRevision || !plan.Target.Core || len(plan.Contributions) != 0 {
		t.Fatalf("Safe Mode Core plan = %#v err=%v", plan, err)
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: componentTestRuntimeAdmission(),
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("Safe Mode must not invoke extension code")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ExpectedRevision: cacheRevision, Props: map[string]any{"scope": "home"},
		Binding: componentCompositionTestBinding(),
	})
	if err != nil || result.Revision != cacheRevision || result.Result["html"] != "core" {
		t.Fatalf("Safe Mode Core composition = %#v err=%v", result, err)
	}
}

func TestComponentRegistrySafeModeWinsConcurrentPublicationRace(t *testing.T) {
	id := "component.safe-race"
	base := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(base, "runtime-initial"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 9)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			for version := 1; version <= 40; version++ {
				next := base
				next.Version = fmt.Sprintf("1.%d.%d", worker, version)
				next.Manifest.Version = next.Version
				next.PackageDigest = fmt.Sprintf("%064x", worker*1000+version+1)
				err := registry.ReplaceRuntime(next, fmt.Sprintf("runtime-%d-%d", worker, version))
				if err != nil && !errors.Is(err, ErrComponentRegistrySafeMode) {
					errorsCh <- err
					return
				}
			}
		}(worker)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		if err := registry.RestoreRuntimes(nil, true); err != nil {
			errorsCh <- err
		}
	}()
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if !snapshot.SafeMode || len(snapshot.Contributions) != 0 || len(snapshot.Selections) != 0 {
		t.Fatalf("publication survived Safe Mode race = %#v", snapshot)
	}
	for attempt := 0; attempt < 100; attempt++ {
		if err := registry.ReplaceRuntime(base, fmt.Sprintf("runtime-late-%d", attempt)); !errors.Is(err, ErrComponentRegistrySafeMode) {
			t.Fatalf("late publication %d error=%v", attempt, err)
		}
	}
}
