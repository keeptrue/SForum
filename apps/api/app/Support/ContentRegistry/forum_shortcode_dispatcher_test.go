package contentregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	businesscache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

type protectedShortcodeTestCapture struct {
	mu       sync.Mutex
	requests []RendererProviderRequest
	calls    atomic.Int64
	render   func(context.Context, RendererProviderRequest) (RenderSegments, error)
}

func (c *protectedShortcodeTestCapture) RenderContent(ctx context.Context, request RendererProviderRequest) (RenderSegments, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	return c.render(ctx, request)
}

func (c *protectedShortcodeTestCapture) snapshot() []RendererProviderRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]RendererProviderRequest(nil), c.requests...)
}

type forumShortcodeDispatcherFixture struct {
	registry    *Registry
	publication Publication
	dispatcher  *ForumShortcodeDispatcher
	admission   *executionTestAdmission
}

func newForumShortcodeDispatcherFixture(
	t *testing.T,
	limits ForumShortcodeDispatcherLimits,
	renderer RendererProvider,
) *forumShortcodeDispatcherFixture {
	t.Helper()
	item := publication(shortcodeExtensionID, false, 'e')
	for _, spec := range shortcodeRuntimeSpecs {
		item.Content = append(item.Content, Declaration{
			ID: spec.id, ContractVersion: spec.contractVersion, Kind: KindShortcode,
			Handler: spec.id, Schema: spec.schema,
		})
	}
	registry := New()
	if _, err := registry.Publish(item); err != nil {
		t.Fatal(err)
	}
	admission := &executionTestAdmission{}
	dispatcher, err := NewForumShortcodeDispatcher(ForumShortcodeDispatcherConfig{
		Registry: registry, Admission: admission,
		Providers: ContentProviderResolverFunc(func(contribution Contribution) (ProviderSet, error) {
			if contribution.Artifact != item.Artifact {
				return ProviderSet{}, ErrArtifactConflict
			}
			return ProviderSet{Renderer: renderer}, nil
		}),
		Cache: businesscache.NewMemoryCache(), Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	return &forumShortcodeDispatcherFixture{
		registry: registry, publication: item, dispatcher: dispatcher, admission: admission,
	}
}

func forumShortcodeTestRenderer(calls *atomic.Int64) RendererProvider {
	return RendererProviderFunc(func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
		calls.Add(1)
		_, target, _, err := decodeForumShortcodeRenderNode(ForumShortcodeRenderNode{
			Placeholder: "test", ID: request.Target.ID, ContractVersion: request.Target.ContractVersion,
			Value: request.Document.Value, Depth: 1,
		})
		if err != nil {
			return RenderSegments{}, err
		}
		label := request.Target.ID
		if target.valid() {
			label = target.String()
		}
		return executionRender(request.Target, `<p>`+label+`</p>`), nil
	})
}

func forumShortcodeNode(t *testing.T, placeholder, id string, targetID int64, depth int) ForumShortcodeRenderNode {
	t.Helper()
	spec := shortcodeRuntimeSpecs[id]
	arguments := map[string]any{}
	if spec.argument != "" {
		arguments[spec.argument] = targetID
	}
	value, err := json.Marshal(map[string]any{
		"type": "sforumShortcodeRef",
		"attrs": map[string]any{
			"id": id, "contractVersion": spec.contractVersion, "arguments": arguments,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ForumShortcodeRenderNode{
		Placeholder: placeholder, ID: id, ContractVersion: spec.contractVersion,
		Value: value, Depth: depth,
	}
}

func renderForumShortcodeTestDocument(
	t *testing.T,
	dispatcher *ForumShortcodeDispatcher,
	resource ShortcodeResourceKey,
	stack []ShortcodeResourceKey,
	nodes ...ForumShortcodeRenderNode,
) ForumShortcodeRenderResult {
	t.Helper()
	var template strings.Builder
	for _, node := range nodes {
		template.WriteString(node.Placeholder)
	}
	results, err := dispatcher.RenderDocuments(t.Context(), []ForumShortcodeRenderDocument{{
		Resource: resource, ReferenceStack: stack, TemplateHTML: template.String(), Nodes: nodes,
	}}, "en-US")
	if err != nil || len(results) != 1 {
		t.Fatalf("render documents: results=%#v err=%v", results, err)
	}
	return results[0]
}

// activateProtectedShortcodeForTest swaps a focused renderer into an exact
// active protected binding so failure paths can be exercised deterministically.
func activateProtectedShortcodeForTest(
	t *testing.T,
	fixture *forumShortcodeDispatcherFixture,
	id string,
	renderer RendererProvider,
	trace ContentTraceSink,
) {
	t.Helper()
	contribution, err := fixture.registry.Resolve(id)
	if err != nil {
		t.Fatalf("missing protected test contribution %s: %v", id, err)
	}
	binding := ExecutionBinding{
		TargetID: contribution.ID, TargetContractVersion: contribution.ContractVersion,
		DeclarationID: contribution.ID, ContractVersion: contribution.ContractVersion,
		Artifact: contribution.Artifact, Action: ActionAdd, Fallback: FallbackClosed,
		Providers: ProviderSet{Renderer: renderer},
	}
	options := []ExecutorOption{}
	if trace != nil {
		options = append(options, WithContentTraceSink(trace))
	}
	executor, err := NewExecutor(
		fixture.registry, []ExecutionBinding{binding}, fixture.admission,
		forumShortcodeSchemaValidator{}, ExecutionLimits{
			MaxBatchSize: fixture.dispatcher.limits.MaxBatchSize, MaxConcurrentCalls: 1,
			MaxOutputBytes: fixture.dispatcher.limits.MaxOutputBytes,
			CallTimeout:    fixture.dispatcher.limits.CallTimeout,
		}, options...,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.dispatcher.mu.Lock()
	if fixture.dispatcher.executor != nil {
		fixture.dispatcher.executor.Close()
	}
	fixture.dispatcher.executor = executor
	fixture.dispatcher.active = map[string]Contribution{id: contribution}
	fixture.dispatcher.revision = fixture.registry.Revision()
	fixture.dispatcher.digest = fixture.registry.Snapshot().Digest
	fixture.dispatcher.mu.Unlock()
}

func protectedShortcodeRequest(t *testing.T, id, marker string) ForumProtectedShortcodeRenderRequest {
	t.Helper()
	fragment, err := json.Marshal(map[string]any{
		"type": "doc", "content": []any{map[string]any{
			"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": marker}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ForumProtectedShortcodeRenderRequest{
		ID: id, ContractVersion: id + "@1", AcceptedFragment: fragment,
	}
}

func TestForumProtectedShortcodeProductionBindingIsPrivateAndMinimal(t *testing.T) {
	const marker = "M8_CONTENT_REGISTRY_SECRET_MARKER"
	trace := NewContentTraceRing(32)
	capture := &protectedShortcodeTestCapture{render: func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
		if !request.SuppressHostDelegations || request.Scope != "protected" || request.ResourceID != "protected:authorized" {
			return RenderSegments{}, ErrExecutionDenied
		}
		return executionRender(request.Target, `<p>TEST_ONLY_ALLOWED</p>`), nil
	}}
	fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, capture)
	fixture.dispatcher.trace = trace
	result, err := fixture.dispatcher.RenderProtectedFragments(t.Context(), []ForumProtectedShortcodeRenderRequest{protectedShortcodeRequest(t, ShortcodeLoginID, marker)}, "en-US")
	if err != nil || len(result) != 1 || !result[0].Rendered || result[0].HTML != `<p>TEST_ONLY_ALLOWED</p>` {
		t.Fatalf("protected test binding result=%#v err=%v", result, err)
	}
	requests := capture.snapshot()
	if len(requests) != 1 || !strings.Contains(string(requests[0].Document.Value), marker) {
		t.Fatalf("protected renderer did not receive exact accepted fragment: %#v", requests)
	}
	serializedRequest, _ := json.Marshal(requests[0])
	for _, forbidden := range []string{"session", "email", "ipAddress", "permissions", "moderation", "actorUserId"} {
		if strings.Contains(strings.ToLower(string(serializedRequest)), strings.ToLower(forbidden)) {
			t.Fatalf("protected renderer request exposed %q: %s", forbidden, serializedRequest)
		}
	}
	serializedTrace, _ := json.Marshal(trace.ContentTraces(0))
	if strings.Contains(string(serializedTrace), marker) || strings.Contains(string(serializedTrace), "TEST_ONLY_ALLOWED") {
		t.Fatalf("protected trace leaked source or output: %s", serializedTrace)
	}
}

func TestForumProtectedShortcodeFailuresRemainClosed(t *testing.T) {
	const marker = "M8_PROTECTED_FAILURE_SECRET_MARKER"
	tests := []struct {
		name   string
		limits ForumShortcodeDispatcherLimits
		render func(context.Context, RendererProviderRequest) (RenderSegments, error)
	}{
		{name: "crash", render: func(context.Context, RendererProviderRequest) (RenderSegments, error) { panic(marker) }},
		{name: "timeout", limits: ForumShortcodeDispatcherLimits{CallTimeout: 5 * time.Millisecond, TotalTimeout: 20 * time.Millisecond}, render: func(ctx context.Context, _ RendererProviderRequest) (RenderSegments, error) {
			<-ctx.Done()
			return RenderSegments{}, ctx.Err()
		}},
		{name: "invalid output", render: func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
			result := executionRender(request.Target, `<p>`+marker+`</p>`)
			result.ContentID = "wrong.content"
			return result, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, id := range []string{ShortcodeLoginID, ShortcodeReplyID, ShortcodeOnlyAuthorID} {
				t.Run(id, func(t *testing.T) {
					fixture := newForumShortcodeDispatcherFixture(t, test.limits, forumShortcodeTestRenderer(&atomic.Int64{}))
					capture := &protectedShortcodeTestCapture{render: test.render}
					activateProtectedShortcodeForTest(t, fixture, id, capture, NewContentTraceRing(16))
					request := protectedShortcodeRequest(t, id, marker)
					result, err := fixture.dispatcher.RenderProtectedFragments(t.Context(), []ForumProtectedShortcodeRenderRequest{request}, "en-US")
					serialized, _ := json.Marshal(result)
					if err != nil || len(result) != 1 || result[0].Rendered || strings.Contains(string(serialized), marker) {
						t.Fatalf("failure did not close: result=%s err=%v", serialized, err)
					}
				})
			}
		})
	}

	for _, state := range []string{"disabled", "safe mode"} {
		t.Run(state, func(t *testing.T) {
			for _, id := range []string{ShortcodeLoginID, ShortcodeReplyID, ShortcodeOnlyAuthorID} {
				t.Run(id, func(t *testing.T) {
					fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, forumShortcodeTestRenderer(&atomic.Int64{}))
					capture := &protectedShortcodeTestCapture{render: func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
						return executionRender(request.Target, `<p>`+marker+`</p>`), nil
					}}
					activateProtectedShortcodeForTest(t, fixture, id, capture, nil)
					if state == "disabled" {
						if _, removed, err := fixture.registry.Remove(fixture.publication.Artifact); err != nil || !removed {
							t.Fatalf("remove publication: removed=%v err=%v", removed, err)
						}
					} else if _, err := fixture.registry.ReplaceAllIfRevision(fixture.registry.Revision(), nil, true); err != nil {
						t.Fatal(err)
					}
					request := protectedShortcodeRequest(t, id, marker)
					result, err := fixture.dispatcher.RenderProtectedFragments(t.Context(), []ForumProtectedShortcodeRenderRequest{request}, "en-US")
					if err != nil || len(result) != 1 || result[0].Rendered || capture.calls.Load() != 0 {
						t.Fatalf("%s did not close: result=%#v calls=%d err=%v", state, result, capture.calls.Load(), err)
					}
				})
			}
		})
	}
}

func TestForumShortcodeDispatcherResourceCyclesFailClosedWithoutPluginCall(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, forumShortcodeTestRenderer(&calls))
	tests := []struct {
		name     string
		resource ShortcodeResourceKey
		stack    []ShortcodeResourceKey
		node     ForumShortcodeRenderNode
		label    string
	}{
		{
			name: "self topic", resource: ShortcodeResourceKey{Type: "topic", ID: 7},
			node: forumShortcodeNode(t, "{{a}}", ShortcodeTopicID, 7, 1), label: "Topic reference unavailable",
		},
		{
			name: "topic A to B to A", resource: ShortcodeResourceKey{Type: "topic", ID: 8},
			stack: []ShortcodeResourceKey{{Type: "topic", ID: 7}},
			node:  forumShortcodeNode(t, "{{b}}", ShortcodeTopicID, 7, 2), label: "Topic reference unavailable",
		},
		{
			name: "comment self reference", resource: ShortcodeResourceKey{Type: "comment", ID: 9},
			node: forumShortcodeNode(t, "{{c}}", ShortcodeCommentID, 9, 1), label: "Comment reference unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := renderForumShortcodeTestDocument(t, fixture.dispatcher, test.resource, test.stack, test.node)
			if !strings.Contains(result.HTML, test.label) || strings.Contains(result.HTML, fmt.Sprint(test.node.Value)) {
				t.Fatalf("cycle fallback = %q", result.HTML)
			}
		})
	}
	if calls.Load() != 0 || len(fixture.admission.snapshot()) != 0 {
		t.Fatalf("cycles reached plugin: calls=%d admissions=%d", calls.Load(), len(fixture.admission.snapshot()))
	}
}

func TestForumShortcodeDispatcherDeduplicatesRepeatedResourceIDs(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, forumShortcodeTestRenderer(&calls))
	first := forumShortcodeNode(t, "{{one}}", ShortcodeTopicID, 42, 1)
	second := forumShortcodeNode(t, "{{two}}", ShortcodeTopicID, 42, 1)
	// The same canonical value with different insignificant whitespace must
	// still resolve by resource identity, not by source bytes.
	second.Value = json.RawMessage(strings.ReplaceAll(string(second.Value), ":", ": "))
	result := renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil, first, second)
	if calls.Load() != 1 || strings.Count(result.HTML, "topic:42") != 2 {
		t.Fatalf("repeated ID was not deduplicated: calls=%d admissions=%d html=%q", calls.Load(), len(fixture.admission.snapshot()), result.HTML)
	}
	if strings.Count(result.HTML, `class="sf-shortcode sf-shortcode--reference sf-shortcode--topic"`) != 2 {
		t.Fatalf("topic shortcode presentation hooks missing: html=%q", result.HTML)
	}
}

func TestForumShortcodeDispatcherEnforcesDepthReferenceAndOutputBudgets(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{
		MaxDepth: 2, MaxTotalReferences: 1,
	}, forumShortcodeTestRenderer(&calls))
	result := renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
		forumShortcodeNode(t, "{{one}}", ShortcodeTopicID, 2, 1),
		forumShortcodeNode(t, "{{two}}", ShortcodeTopicID, 3, 3),
	)
	if calls.Load() != 1 || !strings.Contains(result.HTML, "topic:2") ||
		!strings.Contains(result.HTML, "Shortcode unavailable") {
		t.Fatalf("budget result calls=%d html=%q", calls.Load(), result.HTML)
	}

	large := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{MaxOutputBytes: 4096},
		RendererProviderFunc(func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
			return executionRender(request.Target, `<p>`+strings.Repeat("x", 8192)+`</p>`), nil
		}))
	largeResult := renderForumShortcodeTestDocument(t, large.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
		forumShortcodeNode(t, "{{large}}", ShortcodeTopicID, 2, 1))
	if !strings.Contains(largeResult.HTML, "Topic reference unavailable") || strings.Contains(largeResult.HTML, strings.Repeat("x", 32)) {
		t.Fatalf("output budget did not fail closed: %q", largeResult.HTML)
	}
}

func TestForumShortcodeDispatcherCrashTimeoutDisableAndSafeModeFallback(t *testing.T) {
	t.Run("crash", func(t *testing.T) {
		fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{},
			RendererProviderFunc(func(context.Context, RendererProviderRequest) (RenderSegments, error) { panic("secret panic") }))
		result := renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
			forumShortcodeNode(t, "{{node}}", ShortcodeTopicID, 2, 1))
		if !strings.Contains(result.HTML, "Topic reference unavailable") || strings.Contains(result.HTML, "secret panic") {
			t.Fatalf("crash fallback = %q", result.HTML)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{
			CallTimeout: 5 * time.Millisecond, TotalTimeout: 20 * time.Millisecond,
		}, RendererProviderFunc(func(ctx context.Context, _ RendererProviderRequest) (RenderSegments, error) {
			<-ctx.Done()
			return RenderSegments{}, ctx.Err()
		}))
		result := renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
			forumShortcodeNode(t, "{{node}}", ShortcodeCommentID, 2, 1))
		if !strings.Contains(result.HTML, "Comment reference unavailable") {
			t.Fatalf("timeout fallback = %q", result.HTML)
		}
	})

	t.Run("disable during render", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{},
			RendererProviderFunc(func(_ context.Context, request RendererProviderRequest) (RenderSegments, error) {
				close(started)
				<-release
				return executionRender(request.Target, `<p>must not publish</p>`), nil
			}))
		resultCh := make(chan ForumShortcodeRenderResult, 1)
		go func() {
			resultCh <- renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
				forumShortcodeNode(t, "{{node}}", ShortcodeTopicID, 2, 1))
		}()
		<-started
		if _, removed, err := fixture.registry.Remove(fixture.publication.Artifact); err != nil || !removed {
			t.Fatalf("disable publication: removed=%v err=%v", removed, err)
		}
		close(release)
		result := <-resultCh
		if !strings.Contains(result.HTML, "Topic reference unavailable") || strings.Contains(result.HTML, "must not publish") {
			t.Fatalf("disable fallback = %q", result.HTML)
		}
	})

	t.Run("safe mode", func(t *testing.T) {
		var calls atomic.Int64
		fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, forumShortcodeTestRenderer(&calls))
		if _, err := fixture.registry.ReplaceAllIfRevision(fixture.registry.Revision(), nil, true); err != nil {
			t.Fatal(err)
		}
		result := renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
			forumShortcodeNode(t, "{{node}}", ShortcodeTopicID, 2, 1))
		if !strings.Contains(result.HTML, "Topic reference unavailable") || calls.Load() != 0 {
			t.Fatalf("safe mode calls=%d html=%q", calls.Load(), result.HTML)
		}
	})
}

func TestForumShortcodeDispatcherReferenceCacheInvalidation(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	fixture := newForumShortcodeDispatcherFixture(t, ForumShortcodeDispatcherLimits{}, forumShortcodeTestRenderer(&calls))
	render := func() ForumShortcodeRenderResult {
		return renderForumShortcodeTestDocument(t, fixture.dispatcher, ShortcodeResourceKey{Type: "topic", ID: 1}, nil,
			forumShortcodeNode(t, "{{node}}", ShortcodeCommentID, 91, 1))
	}
	first, second := render(), render()
	if calls.Load() != 1 || first.HTML != second.HTML ||
		!strings.Contains(strings.Join(first.CacheTags, ","), "forum.shortcode.comment.91") {
		t.Fatalf("cache miss calls=%d first=%#v second=%#v", calls.Load(), first, second)
	}
	fixture.dispatcher.InvalidateReferenceResource(t.Context(), "comment", 91)
	render()
	if calls.Load() != 2 {
		t.Fatalf("comment invalidation calls=%d", calls.Load())
	}
	fixture.dispatcher.InvalidateTopicVisibility(t.Context())
	render()
	if calls.Load() != 3 {
		t.Fatalf("owning topic visibility invalidation calls=%d", calls.Load())
	}
}

func TestNormalizeForumShortcodeDispatcherLimitsRejectsInvalidBudgets(t *testing.T) {
	t.Parallel()
	for _, limits := range []ForumShortcodeDispatcherLimits{
		{MaxDepth: 5}, {MaxTotalReferences: 1025}, {MaxBatchSize: 33},
		{MaxOutputBytes: 512}, {CallTimeout: time.Second, TotalTimeout: time.Millisecond},
	} {
		if _, err := normalizeForumShortcodeDispatcherLimits(limits); !errors.Is(err, ErrExecutionInvalid) {
			t.Fatalf("invalid limits accepted: %#v err=%v", limits, err)
		}
	}
}
