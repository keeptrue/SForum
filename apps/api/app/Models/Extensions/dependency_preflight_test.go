package extensions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestResolveInstalledDependencyGraphUsesOnlyEnabledExtensions(t *testing.T) {
	base := dependencyExtension("graph.base", StatusEnabled, "1.0.0", nil)
	consumer := dependencyExtension("graph.consumer", StatusEnabled, "1.0.0", []ManifestDependency{
		{ID: base.ID, Version: "^1.0.0", Kind: "required"},
	})
	disabled := dependencyExtension("graph.disabled", StatusDisabled, "1.0.0", nil)
	store := &permutedDependencyStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{base.ID: base, consumer.ID: consumer, disabled.ID: disabled}),
		listed:             []Extension{consumer, disabled, base},
	}

	graph, err := NewService(store, t.TempDir()).ResolveInstalledDependencyGraph(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{base.ID, consumer.ID}; !reflect.DeepEqual(graph.Order, want) {
		t.Fatalf("order = %#v, want %#v", graph.Order, want)
	}
}

func TestActivationDependencyPreflightResolvesInstalledSet(t *testing.T) {
	tests := []struct {
		name      string
		candidate Extension
		installed []Extension
		wantErr   error
		wantOrder []string
	}{
		{
			name: "required enabled dependency",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.base", Version: "^1.0.0", Kind: "required"},
			}),
			installed: []Extension{dependencyExtension("graph.base", StatusEnabled, "1.2.0", nil)},
			wantOrder: []string{"graph.base", "graph.consumer"},
		},
		{
			name: "required disabled dependency is missing",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.base", Version: "^1.0.0", Kind: "required"},
			}),
			installed: []Extension{dependencyExtension("graph.base", StatusDisabled, "1.2.0", nil)},
			wantErr:   extensionmanifest.ErrDependencyMissing,
		},
		{
			name: "optional dependency can be absent",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.optional", Version: "^2.0.0", Kind: "optional"},
			}),
			wantOrder: []string{"graph.consumer"},
		},
		{
			name: "incompatible optional dependency is ignored",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.optional", Version: "^2.0.0", Kind: "optional"},
			}),
			installed: []Extension{dependencyExtension("graph.optional", StatusEnabled, "1.0.0", nil)},
			wantOrder: []string{"graph.consumer", "graph.optional"},
		},
		{
			name: "required version mismatch",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.base", Version: "^2.0.0", Kind: "required"},
			}),
			installed: []Extension{dependencyExtension("graph.base", StatusEnabled, "1.0.0", nil)},
			wantErr:   extensionmanifest.ErrDependencyVersion,
		},
		{
			name: "transitive cycle",
			candidate: dependencyExtension("graph.candidate", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.middle", Version: "^1.0.0", Kind: "required"},
			}),
			installed: []Extension{
				dependencyExtension("graph.middle", StatusEnabled, "1.0.0", []ManifestDependency{{ID: "graph.candidate", Version: "^1.0.0", Kind: "required"}}),
			},
			wantErr: extensionmanifest.ErrDependencyCycle,
		},
		{
			name: "id conflict",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{ID: "graph.base", Version: "^1.0.0", Kind: "conflict"},
			}),
			installed: []Extension{dependencyExtension("graph.base", StatusEnabled, "1.0.0", nil)},
			wantErr:   extensionmanifest.ErrDependencyConflict,
		},
		{
			name: "capability conflict",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{Capability: "platform.search", Version: "^1.0.0", Kind: "conflict"},
			}),
			installed: []Extension{dependencyExtension("graph.provider", StatusEnabled, "1.0.0", []ManifestDependency{
				{Capability: "platform.search", Version: "1.1.0", Kind: "provides"},
			})},
			wantErr: extensionmanifest.ErrDependencyConflict,
		},
		{
			name: "capability provider",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{Capability: "platform.search", Version: "^2.0.0", Kind: "required"},
			}),
			installed: []Extension{dependencyExtension("graph.provider", StatusEnabled, "1.0.0", []ManifestDependency{
				{Capability: "platform.search", Version: "2.1.0", Kind: "provides"},
			})},
			wantOrder: []string{"graph.provider", "graph.consumer"},
		},
		{
			name: "ambiguous capability provider",
			candidate: dependencyExtension("graph.consumer", StatusInstalled, "1.0.0", []ManifestDependency{
				{Capability: "platform.search", Version: "^2.0.0", Kind: "required"},
			}),
			installed: []Extension{
				dependencyExtension("graph.provider-a", StatusEnabled, "1.0.0", []ManifestDependency{{Capability: "platform.search", Version: "2.1.0", Kind: "provides"}}),
				dependencyExtension("graph.provider-b", StatusEnabled, "1.0.0", []ManifestDependency{{Capability: "platform.search", Version: "2.2.0", Kind: "provides"}}),
			},
			wantErr: extensionmanifest.ErrDependencyAmbiguous,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := map[string]Extension{test.candidate.ID: test.candidate}
			for _, item := range test.installed {
				items[item.ID] = item
			}
			service := NewService(newFakeExtensionStore(items), t.TempDir())
			graph, err := service.preflightActivationDependencies(context.Background(), test.candidate)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || !errors.Is(err, ErrPreflightFailed) {
					t.Fatalf("error = %v, want %v and %v", err, test.wantErr, ErrPreflightFailed)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(graph.Order, test.wantOrder) {
				t.Fatalf("order = %#v, want %#v", graph.Order, test.wantOrder)
			}
		})
	}
}

func TestActivationDependencyPreflightReplacesSameIDManifest(t *testing.T) {
	stale := dependencyExtension("graph.provider", StatusEnabled, "1.0.0", nil)
	candidate := dependencyExtension("graph.provider", StatusInstalled, "2.0.0", nil)
	consumer := dependencyExtension("graph.consumer", StatusEnabled, "1.0.0", []ManifestDependency{
		{ID: candidate.ID, Version: "^2.0.0", Kind: "required"},
	})
	store := newFakeExtensionStore(map[string]Extension{stale.ID: stale, consumer.ID: consumer})

	graph, err := NewService(store, t.TempDir()).preflightActivationDependencies(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{candidate.ID, consumer.ID}; !reflect.DeepEqual(graph.Order, want) {
		t.Fatalf("order = %#v, want %#v", graph.Order, want)
	}
	for _, node := range graph.Nodes {
		if node.ID == candidate.ID && node.Version != candidate.Version {
			t.Fatalf("candidate version = %q, want %q", node.Version, candidate.Version)
		}
	}
}

func TestResolveLifecycleDependencyGraphRemovesDeactivatedCandidate(t *testing.T) {
	provider := dependencyExtension("graph.provider", StatusEnabled, "2.0.0", nil)
	consumer := dependencyExtension("graph.consumer", StatusEnabled, "1.0.0", []ManifestDependency{
		{ID: provider.ID, Version: "^2.0.0", Kind: "required"},
	})
	if _, err := ResolveLifecycleDependencyGraph([]Extension{provider, consumer}, provider, false); !errors.Is(err, extensionmanifest.ErrDependencyMissing) {
		t.Fatalf("deactivation dependency error = %v", err)
	}
}

func TestResolveLifecycleDependencyGraphUsesExactUpgradeCandidate(t *testing.T) {
	active := dependencyExtension("graph.provider", StatusEnabled, "1.0.0", nil)
	target := dependencyExtension("graph.provider", StatusInstalled, "2.0.0", nil)
	consumer := dependencyExtension("graph.consumer", StatusEnabled, "1.0.0", []ManifestDependency{
		{ID: target.ID, Version: "^2.0.0", Kind: "required"},
	})
	graph, err := ResolveLifecycleDependencyGraph([]Extension{active, consumer}, target, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == target.ID && node.Version != target.Version {
			t.Fatalf("resolved provider version = %q, want %q", node.Version, target.Version)
		}
	}
}

func TestActivationDependencyPreflightDiagnosticsAreListOrderStable(t *testing.T) {
	candidate := dependencyExtension("graph.candidate", StatusInstalled, "1.0.0", []ManifestDependency{
		{ID: "graph.missing", Version: "^1.0.0", Kind: "required"},
	})
	alpha := dependencyExtension("graph.alpha", StatusEnabled, "1.0.0", nil)
	zeta := dependencyExtension("graph.zeta", StatusEnabled, "1.0.0", nil)
	orders := [][]Extension{{candidate, zeta, alpha}, {alpha, candidate, zeta}, {zeta, alpha, candidate}}
	var want string
	for iteration := 0; iteration < 30; iteration++ {
		listed := orders[iteration%len(orders)]
		store := &permutedDependencyStore{
			fakeExtensionStore: newFakeExtensionStore(map[string]Extension{candidate.ID: candidate, alpha.ID: alpha, zeta.ID: zeta}),
			listed:             listed,
		}
		_, err := NewService(store, t.TempDir()).preflightActivationDependencies(context.Background(), candidate)
		if err == nil {
			t.Fatal("expected missing dependency")
		}
		if iteration == 0 {
			want = err.Error()
			continue
		}
		if err.Error() != want {
			t.Fatalf("iteration %d error = %q, want %q", iteration, err, want)
		}
	}
}

func TestEnableStopsBeforePackageAndRuntimePreflightWhenDependenciesFail(t *testing.T) {
	candidate := dependencyExtension("graph.candidate", StatusInstalled, "1.0.0", []ManifestDependency{
		{ID: "graph.missing", Version: "^1.0.0", Kind: "required"},
	})
	digest := strings.Repeat("a", 64)
	candidate.Manifest.Backend = ManifestBackend{
		Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
		HostAPIVersion: "sforum.host@2", Digest: digest,
	}
	candidate.Manifest.PackageFiles = []ManifestPackageFile{{
		ID: candidate.ID + ".backend", Kind: "executable", Path: "backend/plugin", Digest: digest,
	}}
	store := newFakeExtensionStore(map[string]Extension{candidate.ID: candidate})
	runtime := &dependencyTrackingRuntime{}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.Enable(context.Background(), extensionManager(), candidate.ID, EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, extensionmanifest.ErrDependencyMissing) || !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("error = %v", err)
	}
	if runtime.checks != 0 || len(runtime.started) != 0 {
		t.Fatalf("runtime touched after dependency failure: checks=%d started=%#v", runtime.checks, runtime.started)
	}
	if store.enabledID != "" {
		t.Fatalf("store Enable called for %q", store.enabledID)
	}
}

func dependencyExtension(id, status, version string, dependencies []ManifestDependency) Extension {
	item := installedExtension(id, TypePlugin, ManifestBackend{})
	item.Status = status
	item.Version = version
	item.Manifest.ManifestVersion = extensionmanifest.ManifestVersionV3
	item.Manifest.Version = version
	item.Manifest.Dependencies = dependencies
	return item
}

type permutedDependencyStore struct {
	*fakeExtensionStore
	listed []Extension
}

func (s *permutedDependencyStore) List(context.Context) ([]Extension, error) {
	return append([]Extension(nil), s.listed...), nil
}

type dependencyTrackingRuntime struct {
	checks  int
	started []string
}

func (r *dependencyTrackingRuntime) Check(context.Context, Extension) error {
	r.checks++
	return nil
}

func (r *dependencyTrackingRuntime) Start(_ context.Context, extension Extension) error {
	r.started = append(r.started, extension.ID)
	return nil
}

func (*dependencyTrackingRuntime) Stop(context.Context, Extension) error { return nil }

func (*dependencyTrackingRuntime) Status(context.Context, Extension) RuntimeStatus {
	return RuntimeStatus{State: RuntimeStopped}
}

func (*dependencyTrackingRuntime) EmitHook(context.Context, string, map[string]any) {}
