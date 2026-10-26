package contentregistry

import (
	"strings"
	"testing"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
)

// TestRenderSegmentLinkPolicyIsSanitizeStable 固定「链接改写后仍是净化固定点」这一契约：
// validateSSRRenderSegments 会用 executionReviewedHTML.Sanitize(segment.HTML) == segment.HTML
// 校验发布出去的 SSR 段落，任何会让净化器改动产出的写法都会在发布路径上直接失败。
func TestRenderSegmentLinkPolicyIsSanitizeStable(t *testing.T) {
	_, target := executionRegistry(t, false, Declaration{
		ID: "sforum.links.stability.block.embed", ContractVersion: "sforum.links.stability.block.embed@1",
		Kind: KindEmbed, Handler: "embed", Schema: "sforum.links.stability.block.embed.schema@1",
	})
	raw := RenderSegments{
		SchemaVersion:   RenderSegmentsSchemaVersion,
		ContentID:       target.ID,
		ContractVersion: target.ContractVersion,
		Segments: []RenderSegment{
			{Kind: SegmentHTML, HTML: `<figure><a href="https://example.com/v/1">watch</a><a href="/t/1">内部</a></figure>`},
			{Kind: SegmentText, Text: "文本"},
		},
	}

	limits, err := normalizeExecutionLimits(ExecutionLimits{})
	if err != nil {
		t.Fatalf("normalizeExecutionLimits: %v", err)
	}
	normalized, err := normalizeRenderSegments(raw, target, limits)
	if err != nil {
		t.Fatalf("normalizeRenderSegments: %v", err)
	}
	html := normalized.Segments[0].HTML
	if !strings.Contains(html, contentlinks.InterstitialHref("https://example.com/v/1")) {
		t.Fatalf("offsite link was not wrapped: %s", html)
	}
	if !strings.Contains(html, `target="_blank"`) {
		t.Fatalf("missing forced new tab: %s", html)
	}
	if got := executionReviewedHTML.Sanitize(html); got != html {
		t.Fatalf("sanitize is not a fixed point after link rewrite:\nbefore=%s\nafter =%s", html, got)
	}
	// finalize 走的是同一段 HTML，SSR 校验必须仍然通过。
	if _, err := finalizeRenderSegmentsForSSR(normalized, target, limits); err != nil {
		t.Fatalf("finalizeRenderSegmentsForSSR: %v", err)
	}
}
