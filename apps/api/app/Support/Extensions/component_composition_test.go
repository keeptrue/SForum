package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentCompositionExecutesDeterministicPlanAndNestedWrap(t *testing.T) {
	id := "composition.actions"
	declarations := []extensions.ManifestComponent{
		componentTestContribution(id, "filter-props", extensionmanifest.ComponentActionFilterProps, 80, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "before", extensionmanifest.ComponentActionBefore, 70, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "add", extensionmanifest.ComponentActionAdd, 60, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "wrap-outer", extensionmanifest.ComponentActionWrap, 50, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "wrap-inner", extensionmanifest.ComponentActionWrap, 40, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 30, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "after", extensionmanifest.ComponentActionAfter, 20, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "filter-result", extensionmanifest.ComponentActionFilterResult, 10, componentTestCoreTarget, componentTestCoreContract),
	}
	extension := componentTestExtension(t, id, extensions.TypePlugin, declarations...)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-actions"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := make([]string, 0, len(declarations))
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		mu.Lock()
		calls = append(calls, call.Contribution.ID)
		mu.Unlock()
		response := ComponentRenderResponse{Artifact: call.Artifact}
		switch call.Contribution.Action {
		case extensionmanifest.ComponentActionFilterProps:
			response.Document = map[string]any{"scope": call.Props["scope"].(string) + "-filtered"}
		case extensionmanifest.ComponentActionFilterResult:
			response.Document = map[string]any{"html": call.Result["html"].(string) + "-filtered"}
		case extensionmanifest.ComponentActionReplace:
			response.Document = map[string]any{"html": "replacement"}
			response.Fragments = []ComponentRenderFragment{{ReviewedHTML: "<main>replacement</main>", PrimaryContent: true}}
		case extensionmanifest.ComponentActionWrap:
			response.Document = cloneComponentDocumentMust(call.Result)
			response.Fragments = []ComponentRenderFragment{{ReviewedHTML: "<section class=\"" + call.Contribution.ID + "\"></section>"}}
		case extensionmanifest.ComponentActionAdd:
			response.Document = map[string]any{"html": "added"}
			response.Fragments = []ComponentRenderFragment{{ReviewedHTML: "<aside>added</aside>"}}
		default:
			response.Fragments = []ComponentRenderFragment{{ReviewedHTML: "<div>" + call.Contribution.Action + "</div>"}}
		}
		return response, nil
	})
	binding := componentCompositionTestBinding()
	executor := componentCompositionTestExecutor(t, registry, renderer, func(_ context.Context, _ ComponentTarget) (ComponentTargetBinding, error) {
		child := binding
		child.Fallback = nil
		return child, nil
	}, nil)
	input := map[string]any{"scope": "home"}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: input, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input["scope"] != "home" || result.Props["scope"] != "home-filtered" || result.Result["html"] != "replacement-filtered" {
		t.Fatalf("documents = input:%#v props:%#v result:%#v", input, result.Props, result.Result)
	}
	wantCalls := []string{
		id + ".component.filter-props",
		id + ".component.replace",
		id + ".component.add",
		id + ".component.wrap-inner",
		id + ".component.wrap-outer",
		id + ".component.before",
		id + ".component.after",
		id + ".component.filter-result",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("call order = %#v, want %#v", calls, wantCalls)
	}
	if len(result.Segments) != 3 || result.Segments[0].Action != extensionmanifest.ComponentActionBefore ||
		result.Segments[1].ComponentID != id+".component.wrap-outer" ||
		len(result.Segments[1].Children) != 1 ||
		result.Segments[1].Children[0].ComponentID != id+".component.wrap-inner" ||
		result.Segments[2].Action != extensionmanifest.ComponentActionAfter {
		t.Fatalf("typed nested segments = %#v", result.Segments)
	}
	orders := collectComponentSegmentOrders(result.Segments)
	wantOrders := make([]int, len(orders))
	for index := range wantOrders {
		wantOrders[index] = index
	}
	if !slices.Equal(orders, wantOrders) {
		t.Fatalf("segment orders = %#v, want %#v", orders, wantOrders)
	}
	for _, segment := range flattenComponentSegments(result.Segments) {
		if segment.OwnerID == "" || segment.ComponentID == "" || segment.ContractVersion == "" || segment.Order < 0 {
			t.Fatalf("missing segment attribution: %#v", segment)
		}
	}
	traces := executor.InspectorTraces()
	if len(traces) != 1 || traces[0].ID != result.TraceID || traces[0].Revision != result.Revision ||
		traces[0].Status != "succeeded" || len(traces[0].Steps) != len(wantCalls)+1 {
		t.Fatalf("Inspector trace = %#v", traces)
	}
}

func TestComponentCompositionSelectedProviderResetAndHide(t *testing.T) {
	alpha := componentTestExtension(t, "composition.alpha", extensions.TypePlugin,
		componentTestContribution("composition.alpha", "replace", extensionmanifest.ComponentActionReplace, 20, componentTestCoreTarget, componentTestCoreContract),
	)
	beta := componentTestExtension(t, "composition.beta", extensions.TypePlugin,
		componentTestContribution("composition.beta", "replace", extensionmanifest.ComponentActionReplace, 20, componentTestCoreTarget, componentTestCoreContract),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceAll([]ComponentRuntimeSnapshot{
		{Extension: beta, InstanceID: "runtime-beta"}, {Extension: alpha, InstanceID: "runtime-alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": call.Artifact.ExtensionID},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>" + call.Artifact.ExtensionID + "</main>", PrimaryContent: true}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	binding := componentCompositionTestBinding()
	compose := func() ComponentCompositionResult {
		current, err := executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: binding,
		})
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	if got := compose().Result["html"]; got != alpha.ID {
		t.Fatalf("deterministic tie winner = %v", got)
	}
	plan, _ := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	selection, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: beta.Manifest.Components[0].ID, ExpectedRevision: plan.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := compose().Result["html"]; got != beta.ID {
		t.Fatalf("selected winner = %v", got)
	}
	selected, _ := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if reset, err := registry.ResetReplaceProvider(ResetComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ExpectedRevision: selected.Revision,
	}); err != nil || !reset || selection.ContributionID != beta.Manifest.Components[0].ID {
		t.Fatalf("reset=%t selection=%#v err=%v", reset, selection, err)
	}
	if got := compose().Result["html"]; got != alpha.ID {
		t.Fatalf("reset winner = %v", got)
	}

	hiderID := "composition.hider"
	hider := componentTestExtension(t, hiderID, extensions.TypePlugin, componentTestContribution(
		hiderID, "hide", extensionmanifest.ComponentActionHide, 100, componentTestCoreTarget, componentTestCoreContract,
	))
	if err := registry.ReplaceRuntime(hider, "runtime-hider"); err != nil {
		t.Fatal(err)
	}
	hidden := compose()
	if !hidden.Hidden || len(hidden.Segments) != 1 || hidden.Segments[0].OwnerID != "core" ||
		len(hidden.Segments[0].Fallback) != 1 || hidden.Segments[0].Fallback[0].Reason != "seo_content_retained" {
		t.Fatalf("hidden SEO fallback = %#v", hidden)
	}
}

func TestComponentCompositionFilterRevalidationAndExplicitMutation(t *testing.T) {
	id := "composition.filter"
	propsSchema := `{"type":"object","required":["scope","locked"],"properties":{"scope":{"type":"string"},"locked":{"type":"string"}},"additionalProperties":false}`
	resultSchema := `{"type":"object","required":["html","locked"],"properties":{"html":{"type":"string"},"locked":{"type":"string"}},"additionalProperties":false}`
	extension := componentTestExtensionWithSchemas(t, id, extensions.TypePlugin, propsSchema, resultSchema,
		componentTestContribution(id, "props", extensionmanifest.ComponentActionFilterProps, 20, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(id, "result", extensionmanifest.ComponentActionFilterResult, 10, componentTestCoreTarget, componentTestCoreContract),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-filter"); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		if call.Contribution.Action == extensionmanifest.ComponentActionFilterProps {
			return ComponentRenderResponse{Artifact: call.Artifact, Document: map[string]any{
				"scope": "changed", "locked": "forged",
			}}, nil
		}
		return ComponentRenderResponse{Artifact: call.Artifact, Document: map[string]any{
			"html": "changed", "locked": "forged",
		}}, nil
	})
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps:      componentObjectFieldsValidator("scope", "locked"),
			ValidateResult:     componentObjectFieldsValidator("html", "locked"),
			MutablePropsFields: []string{"scope"}, MutableResultFields: []string{"html"},
			RetainPrimaryContent: true,
		},
		Fallback: func(_ context.Context, _ ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Document:  map[string]any{"html": "core", "locked": "host"},
				Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>core</main>", PrimaryContent: true}},
			}, nil
		},
	}
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home", "locked": "host"}, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Props["scope"] != "home" || result.Props["locked"] != "host" ||
		result.Result["html"] != "core" || result.Result["locked"] != "host" ||
		len(result.Segments[0].Fallback) != 2 {
		t.Fatalf("protected filter fallback = %#v", result)
	}

	failClosed := componentCompositionTestExecutor(t, registry, renderer, nil, func(ComponentContribution) ComponentCallPolicy {
		return ComponentCallPolicy{FailurePolicy: appevents.FailurePolicyFailClosed}
	})
	_, err = failClosed.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home", "locked": "host"}, Binding: binding,
	})
	if !errors.Is(err, ErrComponentCompositionMutation) {
		t.Fatalf("fail-closed mutation = %v", err)
	}

	invalidSchema := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
		func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			if call.Contribution.Action == extensionmanifest.ComponentActionFilterProps {
				return ComponentRenderResponse{Artifact: call.Artifact, Document: map[string]any{
					"scope": 42, "locked": "host",
				}}, nil
			}
			return ComponentRenderResponse{Artifact: call.Artifact, Document: cloneComponentDocumentMust(call.Result)}, nil
		},
	), nil, nil)
	revalidated, err := invalidSchema.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home", "locked": "host"}, Binding: binding,
	})
	if err != nil || revalidated.Props["scope"] != "home" || revalidated.Result["html"] != "core" ||
		len(revalidated.Segments[0].Fallback) != 1 {
		t.Fatalf("schema-invalid filter fallback = %#v err=%v", revalidated, err)
	}

	missingFields := binding
	missingFields.Contract.MutablePropsFields = nil
	if _, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home", "locked": "host"}, Binding: missingFields,
	}); !errors.Is(err, ErrComponentCompositionInvalid) {
		t.Fatalf("implicit mutable fields = %v", err)
	}
}

func componentCompositionTestExecutor(
	t *testing.T,
	registry *ComponentRegistry,
	renderer ComponentSSRRenderer,
	resolver ComponentTargetBindingResolver,
	policy ComponentCallPolicyResolver,
) *ComponentCompositionExecutor {
	t.Helper()
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Renderer: renderer, ResolveTarget: resolver, ResolvePolicy: policy,
		Admission: componentTestRuntimeAdmission(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

type componentTestAdmissionFunc func(
	context.Context,
	ComponentRuntimeAdmissionRequest,
) (ComponentRuntimeAdmissionLease, error)

func (f componentTestAdmissionFunc) AcquireComponentRuntime(
	ctx context.Context,
	request ComponentRuntimeAdmissionRequest,
) (ComponentRuntimeAdmissionLease, error) {
	return f(ctx, request)
}

type componentTestAdmissionLease struct {
	ctx      context.Context
	validate func(context.Context) error
	once     sync.Once
}

func (l *componentTestAdmissionLease) Context() context.Context { return l.ctx }

func (l *componentTestAdmissionLease) Validate(ctx context.Context) error {
	if l.validate != nil {
		return l.validate(ctx)
	}
	return nil
}

func (l *componentTestAdmissionLease) Release() {
	if l != nil {
		l.once.Do(func() {})
	}
}

func componentTestRuntimeAdmission() ComponentRuntimeAdmission {
	return componentTestAdmissionFunc(func(
		ctx context.Context,
		_ ComponentRuntimeAdmissionRequest,
	) (ComponentRuntimeAdmissionLease, error) {
		return &componentTestAdmissionLease{ctx: ctx}, nil
	})
}

func componentCompositionTestBinding() ComponentTargetBinding {
	return ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps:      componentObjectFieldsValidator("scope"),
			ValidateResult:     componentObjectFieldsValidator("html"),
			MutablePropsFields: []string{"scope"}, MutableResultFields: []string{"html"},
			RetainPrimaryContent: true,
		},
		Fallback: func(_ context.Context, _ ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Document:  map[string]any{"html": "core"},
				Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>core</main>", PrimaryContent: true}},
			}, nil
		},
	}
}

func componentObjectFieldsValidator(required ...string) ComponentDocumentValidator {
	return func(_ context.Context, value map[string]any) error {
		if len(value) != len(required) {
			return fmt.Errorf("fields = %#v", value)
		}
		for _, field := range required {
			if _, ok := value[field].(string); !ok {
				return fmt.Errorf("%s must be a string", field)
			}
		}
		return nil
	}
}

func collectComponentSegmentOrders(segments []ComponentRenderSegment) []int {
	result := make([]int, 0)
	for _, segment := range segments {
		result = append(result, segment.Order)
		result = append(result, collectComponentSegmentOrders(segment.Children)...)
	}
	return result
}

func flattenComponentSegments(segments []ComponentRenderSegment) []ComponentRenderSegment {
	result := make([]ComponentRenderSegment, 0)
	for _, segment := range segments {
		result = append(result, segment)
		result = append(result, flattenComponentSegments(segment.Children)...)
	}
	return result
}

func TestComponentCompositionTraceSnapshotsAreDetached(t *testing.T) {
	registry := NewComponentRegistry()
	executor := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
		func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("unexpected")
		},
	), nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	traces := executor.InspectorTraces()
	traces[0].Status = "forged"
	traces[0].Steps[0].Status = "forged"
	again := executor.InspectorTraces()
	if result.TraceID == "" || again[0].Status == "forged" || again[0].Steps[0].Status == "forged" ||
		!reflect.DeepEqual(result.Props, map[string]any{"scope": "home"}) || strings.Contains(again[0].Error, "<main>") {
		t.Fatalf("detached/redacted trace = %#v result=%#v", again, result)
	}
}
