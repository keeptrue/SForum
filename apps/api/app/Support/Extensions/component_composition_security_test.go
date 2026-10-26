package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentCompositionTimeoutCrashUnauthorizedAndFallback(t *testing.T) {
	tests := []struct {
		name       string
		render     ComponentSSRRendererFunc
		wantReason string
	}{
		{
			name: "timeout",
			render: func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
				time.Sleep(50 * time.Millisecond)
				return ComponentRenderResponse{}, errors.New("late response")
			},
			wantReason: "timeout",
		},
		{
			name: "crash",
			render: func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
				panic("renderer crash")
			},
			wantReason: "crash",
		},
		{
			name: "unauthorized artifact",
			render: func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
				artifact := call.Artifact
				artifact.PackageDigest = strings.Repeat("f", 64)
				return ComponentRenderResponse{
					Artifact: artifact, Document: map[string]any{"html": "forged"},
					Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>forged</main>", PrimaryContent: true}},
				}, nil
			},
			wantReason: "unauthorized_artifact",
		},
		{
			name: "renderer error",
			render: func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
				return ComponentRenderResponse{}, errors.New("render failed")
			},
			wantReason: "renderer_failure",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := "composition.failure-" + strings.ReplaceAll(test.name, " ", "-")
			extension := componentTestExtension(t, id, extensions.TypePlugin,
				componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
			)
			registry := NewComponentRegistry()
			if err := registry.ReplaceRuntime(extension, "runtime-failure"); err != nil {
				t.Fatal(err)
			}
			executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
				Registry: registry, Renderer: test.render, DefaultTimeout: 10 * time.Millisecond,
				Admission: componentTestRuntimeAdmission(),
				ResolvePolicy: func(ComponentContribution) ComponentCallPolicy {
					return ComponentCallPolicy{FailurePolicy: appevents.FailurePolicyFailOpen, Timeout: 10 * time.Millisecond}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
				TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
				Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Result["html"] != "core" || len(result.Segments) != 1 ||
				strings.Contains(result.Segments[0].HTML, "forged") ||
				len(result.Segments[0].Fallback) != 1 || result.Segments[0].Fallback[0].Reason != test.wantReason {
				t.Fatalf("safe fallback = %#v", result)
			}
			trace := executor.InspectorTraces()[0]
			if trace.Steps[len(trace.Steps)-1].FallbackReason != test.wantReason {
				t.Fatalf("failure trace = %#v", trace)
			}
		})
	}
}

func TestComponentCompositionRejectsStaleSnapshotBeforeResultRelease(t *testing.T) {
	id := "composition.stale"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-stale"); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		if removed, err := registry.RemoveRuntime(id, "runtime-stale"); err != nil || !removed {
			return ComponentRenderResponse{}, fmt.Errorf("remove during render: removed=%t err=%v", removed, err)
		}
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": "stale"},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>stale</main>", PrimaryContent: true}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if !errors.Is(err, ErrComponentCompositionStale) || len(result.Segments) != 0 {
		t.Fatalf("stale release = result:%#v err:%v", result, err)
	}
	trace := executor.InspectorTraces()[0]
	if trace.Status != "failed" || trace.Error == "" {
		t.Fatalf("stale trace = %#v", trace)
	}
}

func TestComponentCompositionNeverExecutesL2AndRechecksLiveAdmission(t *testing.T) {
	t.Run("L2-only contribution preserves SSR fallback", func(t *testing.T) {
		id := "composition.l2-only"
		extension := componentPublicL2Extension(
			t, id, extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		)
		extension.Manifest.Components[0].SSRTemplate = ""
		registry := NewComponentRegistry()
		if err := registry.ReplaceRuntime(extension, "runtime-l2"); err != nil {
			t.Fatal(err)
		}
		var called atomic.Bool
		executor := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
			func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
				called.Store(true)
				return ComponentRenderResponse{}, errors.New("L2 must not execute on the server")
			},
		), nil, nil)
		result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
		})
		if err != nil || called.Load() || result.Result["html"] != "core" ||
			result.Segments[0].Fallback[0].Reason != "ssr_template_unavailable" {
			t.Fatalf("L2 SSR boundary = called:%t result:%#v err:%v", called.Load(), result, err)
		}
	})

	t.Run("live admission is checked before call and release", func(t *testing.T) {
		id := "composition.admission"
		extension := componentTestExtension(t, id, extensions.TypePlugin,
			componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
		)
		registry := NewComponentRegistry()
		if err := registry.ReplaceRuntime(extension, "runtime-admission"); err != nil {
			t.Fatal(err)
		}
		var admissions atomic.Int32
		executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
			Registry: registry,
			Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
				return ComponentRenderResponse{
					Artifact: call.Artifact, Document: map[string]any{"html": "extension"},
					Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>extension</main>", PrimaryContent: true}},
				}, nil
			}),
			Admission: componentTestAdmissionFunc(func(
				ctx context.Context,
				_ ComponentRuntimeAdmissionRequest,
			) (ComponentRuntimeAdmissionLease, error) {
				return &componentTestAdmissionLease{ctx: ctx, validate: func(context.Context) error {
					if admissions.Add(1) == 2 {
						return errors.New("trust revoked")
					}
					return nil
				}}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
		})
		if err != nil || admissions.Load() != 2 || result.Result["html"] != "core" ||
			result.Segments[0].Fallback[0].Reason != "unauthorized_artifact" {
			t.Fatalf("live admission = calls:%d result:%#v err:%v", admissions.Load(), result, err)
		}
	})
}

func TestComponentCompositionRetainsSEOContentOnInvalidReplacement(t *testing.T) {
	id := "composition.seo"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-seo"); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": "visual-only"},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<div>visual only</div>"}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result["html"] != "core" || !segmentsHavePrimaryContent(result.Segments) ||
		len(result.Segments[0].Fallback) != 1 || result.Segments[0].Fallback[0].Reason != "seo_content_removed" {
		t.Fatalf("SEO fallback = %#v", result)
	}
}

func TestComponentCompositionRetainsSEOContentOnInvalidAddProvider(t *testing.T) {
	id := "composition.seo-provider"
	add := componentTestContribution(
		id, "add", extensionmanifest.ComponentActionAdd, 10,
		componentTestCoreTarget, componentTestCoreContract,
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(
		componentTestExtension(t, id, extensions.TypePlugin, add), "runtime-seo-provider",
	); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": "visual-only"},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<div>visual only</div>"}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: add.ID, TargetContractVersion: add.ContractVersion,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result["html"] != "core" || !segmentsHavePrimaryContent(result.Segments) ||
		len(result.Segments[0].Fallback) != 1 || result.Segments[0].Fallback[0].Reason != "seo_content_removed" {
		t.Fatalf("provider SEO fallback = %#v", result)
	}
}

func TestComponentCompositionThemePresentationBoundary(t *testing.T) {
	filterID := "composition.theme-filter"
	filter := componentTestExtension(t, filterID, extensions.TypeTheme,
		componentTestContribution(filterID, "props", extensionmanifest.ComponentActionFilterProps, 10, componentTestCoreTarget, componentTestCoreContract),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(filter, "runtime-filter"); !errors.Is(err, ErrComponentRegistryInvalid) {
		t.Fatalf("theme business filter = %v", err)
	}

	themeID := "composition.theme-replace"
	theme := componentTestExtension(t, themeID, extensions.TypeTheme,
		componentTestContribution(themeID, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
	)
	if err := registry.ReplaceRuntime(theme, "runtime-theme"); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": "theme changed business result"},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>theme</main>", PrimaryContent: true}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result["html"] != "core" || result.Segments[0].OwnerID != "core" ||
		result.Segments[0].Fallback[0].Reason != "forbidden_mutation" {
		t.Fatalf("theme presentation boundary = %#v", result)
	}
}

func TestComponentCompositionCycleDepthAndOutputBounds(t *testing.T) {
	t.Run("cycle rejected at publication", func(t *testing.T) {
		id := "composition.cycle"
		left := componentTestContribution(id, "left", extensionmanifest.ComponentActionAdd, 10,
			id+".component.right", id+".component.right@1")
		right := componentTestContribution(id, "right", extensionmanifest.ComponentActionAdd, 10,
			id+".component.left", id+".component.left@1")
		extension := componentTestExtension(t, id, extensions.TypePlugin, left, right)
		if err := NewComponentRegistry().ReplaceRuntime(extension, "runtime-cycle"); !errors.Is(err, ErrComponentCompositionCycle) {
			t.Fatalf("cycle publication = %v", err)
		}
	})

	t.Run("publication depth matches runtime bound", func(t *testing.T) {
		id := "composition.publish-depth"
		a := componentTestContribution(
			id, "a", extensionmanifest.ComponentActionAdd, 20,
			componentTestCoreTarget, componentTestCoreContract,
		)
		b := componentTestContribution(
			id, "b", extensionmanifest.ComponentActionAdd, 10, a.ID, a.ContractVersion,
		)
		contributions := map[string]ComponentContribution{
			a.ID: {ID: a.ID, Action: a.Action, TargetID: a.TargetID},
			b.ID: {ID: b.ID, Action: b.Action, TargetID: b.TargetID},
		}
		active := map[string]bool{a.ID: true, b.ID: true}
		if err := validateComponentCompositionGraph(contributions, active, 2); !errors.Is(err, ErrComponentCompositionDepth) {
			t.Fatalf("publication depth bound = %v", err)
		}
	})

	t.Run("runtime depth bound", func(t *testing.T) {
		id := "composition.depth"
		a := componentTestContribution(id, "a", extensionmanifest.ComponentActionAdd, 30, componentTestCoreTarget, componentTestCoreContract)
		b := componentTestContribution(id, "b", extensionmanifest.ComponentActionAdd, 20, a.ID, a.ContractVersion)
		c := componentTestContribution(id, "c", extensionmanifest.ComponentActionAdd, 10, b.ID, b.ContractVersion)
		extension := componentTestExtension(t, id, extensions.TypePlugin, a, b, c)
		registry := NewComponentRegistry()
		if err := registry.ReplaceRuntime(extension, "runtime-depth"); err != nil {
			t.Fatal(err)
		}
		binding := componentCompositionTestBinding()
		childBinding := binding
		childBinding.Fallback = nil
		executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
			Registry:  registry,
			Admission: componentTestRuntimeAdmission(),
			Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
				return ComponentRenderResponse{
					Artifact: call.Artifact, Document: map[string]any{"html": call.Contribution.ID},
					Fragments: []ComponentRenderFragment{{ReviewedHTML: "<div>child</div>"}},
				}, nil
			}),
			ResolveTarget: func(context.Context, ComponentTarget) (ComponentTargetBinding, error) {
				return childBinding, nil
			},
			MaxDepth: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: binding,
		})
		if !errors.Is(err, ErrComponentCompositionDepth) {
			t.Fatalf("depth bound = %v", err)
		}
	})

	t.Run("output size bound falls back", func(t *testing.T) {
		id := "composition.output"
		extension := componentTestExtension(t, id, extensions.TypePlugin,
			componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10, componentTestCoreTarget, componentTestCoreContract),
		)
		registry := NewComponentRegistry()
		if err := registry.ReplaceRuntime(extension, "runtime-output"); err != nil {
			t.Fatal(err)
		}
		executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
			Registry: registry, MaxOutputBytes: 128,
			Admission: componentTestRuntimeAdmission(),
			Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
				return ComponentRenderResponse{
					Artifact: call.Artifact, Document: map[string]any{"html": "oversized"},
					Fragments: []ComponentRenderFragment{{Text: strings.Repeat("x", 256), PrimaryContent: true}},
				}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
		})
		if err != nil || result.Result["html"] != "core" ||
			result.Segments[0].Fallback[0].Reason != "output_limit" {
			t.Fatalf("output fallback = %#v err=%v", result, err)
		}
	})
}
