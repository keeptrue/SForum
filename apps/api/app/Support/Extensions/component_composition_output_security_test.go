package extensionsruntime

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type expandingComponentJSON string

var expandingComponentJSONCalls atomic.Int32

func (expandingComponentJSON) MarshalJSON() ([]byte, error) {
	expandingComponentJSONCalls.Add(1)
	return []byte(`"` + strings.Repeat("x", 4096) + `"`), nil
}

type addressableComponentJSON string

var addressableComponentJSONCalls atomic.Int32

func (*addressableComponentJSON) MarshalJSON() ([]byte, error) {
	addressableComponentJSONCalls.Add(1)
	return []byte(`"` + strings.Repeat("x", 4096) + `"`), nil
}

type expandingComponentText string

var expandingComponentTextCalls atomic.Int32

func (expandingComponentText) MarshalText() ([]byte, error) {
	expandingComponentTextCalls.Add(1)
	return []byte(strings.Repeat("x", 4096)), nil
}

var (
	_ json.Marshaler         = expandingComponentJSON("")
	_ json.Marshaler         = (*addressableComponentJSON)(nil)
	_ encoding.TextMarshaler = expandingComponentText("")
)

func TestComponentDocumentBudgetRejectsCustomMarshalersBeforeExecution(t *testing.T) {
	expandingComponentJSONCalls.Store(0)
	if _, err := cloneComponentDocument(map[string]any{
		"value": expandingComponentJSON("small"),
	}, 1024); !errors.Is(err, ErrComponentCompositionInvalid) {
		t.Fatalf("JSON marshaler document error=%v", err)
	}
	if calls := expandingComponentJSONCalls.Load(); calls != 0 {
		t.Fatalf("JSON marshaler ran before Host rejection: calls=%d", calls)
	}

	addressableComponentJSONCalls.Store(0)
	values := []addressableComponentJSON{"small"}
	if _, err := cloneComponentDocument(map[string]any{"values": values}, 1024); !errors.Is(err, ErrComponentCompositionInvalid) {
		t.Fatalf("addressable JSON marshaler document error=%v", err)
	}
	if calls := addressableComponentJSONCalls.Load(); calls != 0 {
		t.Fatalf("addressable JSON marshaler ran before Host rejection: calls=%d", calls)
	}

	expandingComponentTextCalls.Store(0)
	if _, err := cloneComponentDocument(map[string]any{
		"value": expandingComponentText("small"),
	}, 1024); !errors.Is(err, ErrComponentCompositionInvalid) {
		t.Fatalf("text marshaler document error=%v", err)
	}
	if calls := expandingComponentTextCalls.Load(); calls != 0 {
		t.Fatalf("text marshaler ran before Host rejection: calls=%d", calls)
	}
}

func TestComponentDocumentBudgetRejectsBytesDepthNodesAndCyclesBeforeMarshal(t *testing.T) {
	if _, err := cloneComponentDocument(map[string]any{
		"value": strings.Repeat("<", 128),
	}, 256); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("escaped string budget error=%v", err)
	}

	deep := map[string]any{"value": "leaf"}
	for range maximumComponentDocumentDepth + 2 {
		deep = map[string]any{"next": deep}
	}
	if _, err := cloneComponentDocument(deep, DefaultComponentCompositionMaxBytes); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("deep document error=%v", err)
	}

	cyclic := map[string]any{"value": "root"}
	cyclic["self"] = cyclic
	if _, err := cloneComponentDocument(cyclic, DefaultComponentCompositionMaxBytes); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("cyclic document error=%v", err)
	}

	nodes := make([]any, maximumComponentDocumentNodes+1)
	if _, err := cloneComponentDocument(map[string]any{
		"nodes": nodes,
	}, DefaultComponentCompositionMaxBytes); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("node count error=%v", err)
	}
}

func TestComponentFragmentNormalizationRejectsExpansionBeforeRelease(t *testing.T) {
	if _, err := normalizeComponentRenderFragment(ComponentRenderFragment{
		Text: strings.Repeat("&", 32),
	}, 64); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("escaped text expansion error=%v", err)
	}

	reviewed := `<a href="https://example.com">external</a>`
	if _, err := normalizeComponentRenderFragment(ComponentRenderFragment{
		ReviewedHTML: reviewed,
	}, len(reviewed)); !errors.Is(err, ErrComponentCompositionOutput) {
		t.Fatalf("sanitizer expansion error=%v", err)
	}
}

func TestComponentCompositionBudgetsInputBeforeTargetValidation(t *testing.T) {
	registry := NewComponentRegistry()
	var validatorCalls atomic.Int32
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps: func(context.Context, map[string]any) error {
				validatorCalls.Add(1)
				return nil
			},
			ValidateResult: func(context.Context, map[string]any) error { return nil },
		},
		Fallback: func(context.Context, ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("oversized input must not reach fallback")
		},
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: componentTestRuntimeAdmission(), MaxOutputBytes: 128,
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("oversized input must not reach renderer")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": strings.Repeat("x", 256)}, Binding: binding,
	})
	if !errors.Is(err, ErrComponentCompositionOutput) || validatorCalls.Load() != 0 {
		t.Fatalf("oversized input error=%v validatorCalls=%d", err, validatorCalls.Load())
	}
}

func TestComponentCompositionBudgetsFallbackResultBeforeTargetValidation(t *testing.T) {
	registry := NewComponentRegistry()
	var resultValidatorCalls atomic.Int32
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps: func(context.Context, map[string]any) error { return nil },
			ValidateResult: func(context.Context, map[string]any) error {
				resultValidatorCalls.Add(1)
				return nil
			},
		},
		Fallback: func(context.Context, ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Document:  map[string]any{"html": strings.Repeat("x", 256)},
				Fragments: []ComponentRenderFragment{{Text: "core", PrimaryContent: true}},
			}, nil
		},
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: componentTestRuntimeAdmission(), MaxOutputBytes: 128,
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("empty graph must not reach renderer")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: binding,
	})
	if !errors.Is(err, ErrComponentCompositionOutput) || resultValidatorCalls.Load() != 0 {
		t.Fatalf("oversized fallback error=%v resultValidatorCalls=%d", err, resultValidatorCalls.Load())
	}
}

func TestComponentCompositionRejectsCustomRendererAndFilterDocumentsWithoutExecution(t *testing.T) {
	for _, action := range []string{
		extensionmanifest.ComponentActionFilterProps,
		extensionmanifest.ComponentActionReplace,
		extensionmanifest.ComponentActionFilterResult,
	} {
		t.Run(action, func(t *testing.T) {
			id := "composition.document-budget-" + action
			extension := componentTestExtension(t, id, extensions.TypePlugin,
				componentTestContribution(id, "candidate", action, 10, componentTestCoreTarget, componentTestCoreContract),
			)
			registry := NewComponentRegistry()
			if err := registry.ReplaceRuntime(extension, "runtime-document-budget"); err != nil {
				t.Fatal(err)
			}
			expandingComponentJSONCalls.Store(0)
			executor := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
				func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
					key := "html"
					if action == extensionmanifest.ComponentActionFilterProps {
						key = "scope"
					}
					return ComponentRenderResponse{
						Artifact:  call.Artifact,
						Document:  map[string]any{key: expandingComponentJSON("small")},
						Fragments: []ComponentRenderFragment{{Text: "candidate", PrimaryContent: true}},
					}, nil
				},
			), nil, nil)
			result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
				TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
				Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
			})
			if err != nil || result.Result["html"] != "core" || result.Props["scope"] != "home" {
				t.Fatalf("custom document fallback result=%#v err=%v", result, err)
			}
			if calls := expandingComponentJSONCalls.Load(); calls != 0 {
				t.Fatalf("custom renderer marshaler executed: calls=%d", calls)
			}
		})
	}
}

func TestComponentCompositionReviewedHTMLSanitizesXSSCorpus(t *testing.T) {
	corpus := []string{
		`<img src="x" onerror="alert(1)"><script>alert(2)</script><p>safe</p>`,
		`<a href="javascript:alert(1)" onclick="alert(2)">link</a><p>safe</p>`,
		`<svg><a xlink:href="javascript:alert(1)">bad</a></svg><p>safe</p>`,
		`<iframe srcdoc="<script>alert(1)</script>"></iframe><p>safe</p>`,
		`<a href="&#x6a;avascript:alert(1)">encoded</a><p>safe</p>`,
		`<math><mtext><img src=x onerror=alert(1)></mtext></math><p>safe</p>`,
	}
	for index, payload := range corpus {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			result := composeComponentHTMLFixture(t, ComponentRenderFragment{
				ReviewedHTML: payload, PrimaryContent: true,
			})
			if len(result.Segments) != 1 || result.Segments[0].OwnerID == "core" ||
				result.Segments[0].Encoding != ComponentRenderEncodingSanitizedHTML {
				t.Fatalf("sanitized segment = %#v", result.Segments)
			}
			lower := strings.ToLower(result.Segments[0].HTML)
			for _, forbidden := range []string{
				"<script", "onerror", "onclick", "javascript:", "<svg", "<iframe", "<math",
			} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("sanitizer retained %q in %q", forbidden, result.Segments[0].HTML)
				}
			}
			if !strings.Contains(lower, "<p>safe</p>") {
				t.Fatalf("sanitizer removed safe content: %q", result.Segments[0].HTML)
			}
		})
	}
}

func TestComponentCompositionTextFragmentUsesContextualEscaping(t *testing.T) {
	result := composeComponentHTMLFixture(t, ComponentRenderFragment{
		Text: `<img src=x onerror="alert(1)">&"`, PrimaryContent: true,
	})
	if len(result.Segments) != 1 || result.Segments[0].Encoding != ComponentRenderEncodingEscapedText ||
		strings.Contains(result.Segments[0].HTML, "<img") ||
		!strings.Contains(result.Segments[0].HTML, "&lt;img") ||
		!strings.Contains(result.Segments[0].HTML, "&amp;") {
		t.Fatalf("escaped text segment = %#v", result.Segments)
	}
}

func TestComponentCompositionRejectsAmbiguousFragmentEncoding(t *testing.T) {
	id := "composition.ambiguous-fragment"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-ambiguous"); err != nil {
		t.Fatal(err)
	}
	executor := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
		func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Artifact: call.Artifact, Document: map[string]any{"html": "ambiguous"},
				Fragments: []ComponentRenderFragment{{
					Text: "ordinary", ReviewedHTML: "<strong>reviewed</strong>", PrimaryContent: true,
				}},
			}, nil
		},
	), nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil || result.Result["html"] != "core" ||
		result.Segments[0].Fallback[0].Reason != "output_limit" {
		t.Fatalf("ambiguous fragment fallback: result=%#v err=%v", result, err)
	}
}

func TestComponentCompositionJSONClonePreservesLargeInteger(t *testing.T) {
	const exact int64 = 9007199254740993
	registry := NewComponentRegistry()
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps:  componentExactIntegerValidator,
			ValidateResult: componentExactIntegerValidator,
		},
		Fallback: func(_ context.Context, call ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Document:  map[string]any{"value": call.Props["value"]},
				Fragments: []ComponentRenderFragment{{Text: "exact integer", PrimaryContent: true}},
			}, nil
		},
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: componentTestRuntimeAdmission(),
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("empty Core graph must not invoke extension renderer")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"value": exact}, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Result["value"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("detached large integer=%T(%v)", result.Result["value"], result.Result["value"])
	}
	raw, err := json.Marshal(result.Result)
	if err != nil || !strings.Contains(string(raw), `9007199254740993`) {
		t.Fatalf("serialized large integer=%s err=%v", raw, err)
	}
}

func componentExactIntegerValidator(_ context.Context, document map[string]any) error {
	value, ok := document["value"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		return fmt.Errorf("integer=%T(%v)", document["value"], document["value"])
	}
	return nil
}

func TestComponentCompositionPreflightsWrapAmplificationBeforeClone(t *testing.T) {
	id := "composition.wrap-budget"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "wrap", extensionmanifest.ComponentActionWrap, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-wrap-budget"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	binding := componentCompositionTestBinding()
	binding.Fallback = func(context.Context, ComponentFallbackCall) (ComponentRenderResponse, error) {
		fragments := make([]ComponentRenderFragment, 8)
		for index := range fragments {
			fragments[index] = ComponentRenderFragment{
				ReviewedHTML: fmt.Sprintf("<div>core-%d</div>", index), PrimaryContent: index == 0,
			}
		}
		return ComponentRenderResponse{Document: map[string]any{"html": "core"}, Fragments: fragments}, nil
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: componentTestRuntimeAdmission(), MaxSegments: 10,
		ResolvePolicy: func(ComponentContribution) ComponentCallPolicy {
			return ComponentCallPolicy{FailurePolicy: appevents.FailurePolicyFailClosed}
		},
		Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			calls.Add(1)
			return ComponentRenderResponse{
				Artifact: call.Artifact, Document: cloneComponentDocumentMust(call.Result),
				Fragments: []ComponentRenderFragment{
					{ReviewedHTML: "<section>one</section>"},
					{ReviewedHTML: "<section>two</section>"},
				},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: binding,
	})
	if !errors.Is(err, ErrComponentCompositionOutput) || len(result.Segments) != 0 || calls.Load() != 1 {
		t.Fatalf("wrap amplification: result=%#v calls=%d err=%v", result, calls.Load(), err)
	}
}

func composeComponentHTMLFixture(t *testing.T, fragment ComponentRenderFragment) ComponentCompositionResult {
	t.Helper()
	id := "composition.html-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-html"); err != nil {
		t.Fatal(err)
	}
	executor := componentCompositionTestExecutor(t, registry, ComponentSSRRendererFunc(
		func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Artifact: call.Artifact, Document: map[string]any{"html": "extension"},
				Fragments: []ComponentRenderFragment{fragment},
			}, nil
		},
	), nil, nil)
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
