package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProductionComponentCoreAdmissionIsNoOp(t *testing.T) {
	admission := &ComponentRuntimeAdmissionProduction{}
	lease, err := admission.AcquireComponentRuntime(context.Background(), ComponentRuntimeAdmissionRequest{
		Artifact: HookArtifact{},
	})
	if err != nil || lease == nil {
		t.Fatalf("core admission err=%v lease=%v", err, lease)
	}
	if err := lease.Validate(context.Background()); err != nil {
		t.Fatalf("core lease validate: %v", err)
	}
	lease.Release()
	lease.Release() // idempotent
}

func TestProductionComponentPluginAdmissionRequiresManager(t *testing.T) {
	admission := &ComponentRuntimeAdmissionProduction{}
	_, err := admission.AcquireComponentRuntime(context.Background(), ComponentRuntimeAdmissionRequest{
		Artifact: HookArtifact{
			ExtensionID: "plugin.demo", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-1",
		},
	})
	if !errors.Is(err, ErrComponentCompositionUnauthorized) {
		t.Fatalf("nil manager admission error=%v", err)
	}

	starter := &productionCompositionStarter{instanceID: "runtime-1"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("plugin.demo", "1.0.0", strings.Repeat("a", 64))
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	admission.Manager = manager
	lease, err := admission.AcquireComponentRuntime(context.Background(), ComponentRuntimeAdmissionRequest{
		Artifact: HookArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, RuntimeInstanceID: "runtime-1",
		},
	})
	if err != nil || lease == nil {
		t.Fatalf("manager admission err=%v lease=%v", err, lease)
	}
	if err := lease.Validate(context.Background()); err != nil {
		t.Fatalf("manager lease validate: %v", err)
	}
	lease.Release()
}

func TestProductionComponentCoreRendererPrimaryFragment(t *testing.T) {
	renderer := &ComponentSSRRendererProduction{}
	response, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget,
		Artifact: HookArtifact{},
		Props:    map[string]any{"label": "Home"},
		Contribution: ComponentContribution{
			ID: "core.fallback", Action: extensionmanifest.ComponentActionBefore,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Fragments) != 1 || response.Fragments[0].Text != "Home" ||
		!response.Fragments[0].PrimaryContent || response.Fragments[0].ReviewedHTML != "" {
		t.Fatalf("core renderer fragments = %#v", response.Fragments)
	}

	_, err = renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget,
		Artifact: HookArtifact{
			ExtensionID: "plugin.demo", RuntimeInstanceID: "runtime-1",
		},
		Contribution: ComponentContribution{ID: "plugin.demo.component.before"},
	})
	if !errors.Is(err, ErrComponentCompositionCrash) {
		t.Fatalf("plugin without PluginRenderer error=%v", err)
	}
}

func TestProductionComponentCompositionComposeAndSafeMode(t *testing.T) {
	id := "production.composition.before"
	instanceID := "runtime-production-composition"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "before", extensionmanifest.ComponentActionBefore, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, instanceID); err != nil {
		t.Fatal(err)
	}

	starter := &productionCompositionStarter{instanceID: instanceID}
	manager := NewManager(ManagerConfig{Starter: starter})
	process := managerRuntimeExtension(id, extension.Version, extension.PackageDigest)
	if err := manager.Start(context.Background(), process); err != nil {
		t.Fatal(err)
	}

	pluginCalls := 0
	service, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
		Manager:  manager,
		PluginRenderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			pluginCalls++
			return ComponentRenderResponse{
				Artifact:  call.Artifact,
				Fragments: []ComponentRenderFragment{{ReviewedHTML: "<aside data-test=\"before\">plugin</aside>"}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pluginCalls != 1 {
		t.Fatalf("plugin renderer calls = %d", pluginCalls)
	}
	foundPlugin := false
	for _, segment := range result.Segments {
		if segment.OwnerID == id && strings.Contains(segment.HTML, "plugin") {
			foundPlugin = true
		}
	}
	if !foundPlugin {
		t.Fatalf("expected plugin before segment in %#v", result.Segments)
	}
	html, err := service.ComposePageTarget(context.Background(), "forum.home", map[string]any{"scope": "home"}, ComponentActorAuthority{})
	if err != nil || len(html) == 0 || !strings.Contains(html[0], "plugin") {
		t.Fatalf("ComposePageTarget html=%#v err=%v", html, err)
	}

	if err := registry.RestoreRuntimes([]extensions.Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	safe, err := service.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range safe.Segments {
		if segment.OwnerID == id {
			t.Fatalf("Safe Mode retained third-party segment %#v", segment)
		}
	}
	// first Compose + ComposePageTarget = 2；Safe Mode 不得再调用插件渲染器。
	if pluginCalls != 2 {
		t.Fatalf("Safe Mode must not invoke plugin renderer: calls=%d", pluginCalls)
	}
	pageHTML, err := service.ComposePageTarget(context.Background(), "forum.home", nil, ComponentActorAuthority{})
	if err != nil || len(pageHTML) != 0 {
		t.Fatalf("Safe Mode ComposePageTarget = %#v err=%v", pageHTML, err)
	}
	traces := service.InspectorTraces()
	if len(traces) < 2 {
		t.Fatalf("expected production traces, got %#v", traces)
	}
}

func TestProductionComponentCompositionRejectsNilRegistry(t *testing.T) {
	if _, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{}); !errors.Is(err, ErrComponentCompositionInvalid) {
		t.Fatalf("nil registry error=%v", err)
	}
}

type productionCompositionStarter struct {
	instanceID string
	mu         sync.Mutex
	starts     int
}

func (s *productionCompositionStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	return RouteTarget{InstanceID: s.instanceID}, nil
}

func (s *productionCompositionStarter) Stop(context.Context, extensions.Extension) error { return nil }
