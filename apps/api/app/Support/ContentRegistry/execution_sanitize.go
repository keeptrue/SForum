package contentregistry

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/microcosm-cc/bluemonday"
	nethtml "golang.org/x/net/html"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
)

var (
	executionCodeClassPattern = regexp.MustCompile(`^language-[a-z0-9+#.-]+$`)
	executionCheckboxPattern  = regexp.MustCompile(`^checkbox$`)
	executionReviewedHTML     = executionSanitizerPolicy()
)

func normalizeRenderSegments(input RenderSegments, target Contribution, limits ExecutionLimits) (RenderSegments, error) {
	if input.SchemaVersion == "" {
		input.SchemaVersion = RenderSegmentsSchemaVersion
	}
	input.ContentID = strings.ToLower(strings.TrimSpace(input.ContentID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	if input.SchemaVersion != RenderSegmentsSchemaVersion || input.ContentID != target.ID ||
		input.ContractVersion != target.ContractVersion || input.TextEncoding != "" ||
		len(input.Segments) > limits.MaxSegments {
		return RenderSegments{}, ErrContractStale
	}
	if err := preflightRenderSegmentsJSON(input, limits); err != nil {
		return RenderSegments{}, err
	}
	segments := make([]RenderSegment, 0, len(input.Segments))
	var plain strings.Builder
	for _, raw := range input.Segments {
		segment := RenderSegment{Kind: strings.ToLower(strings.TrimSpace(raw.Kind))}
		switch segment.Kind {
		case SegmentHTML:
			if raw.Text != "" {
				return RenderSegments{}, ErrExecutionInvalid
			}
			// 净化后统一收敛链接呈现：插件渲染段里的链接同样强制新标签页 + 站外中转。
			segment.HTML = contentlinks.Apply(executionReviewedHTML.Sanitize(raw.HTML))
			text, textErr := htmlSegmentText(segment.HTML, limits)
			if textErr != nil {
				return RenderSegments{}, textErr
			}
			if err := appendPlainTextBounded(&plain, text, limits.MaxOutputBytes); err != nil {
				return RenderSegments{}, err
			}
		case SegmentText, SegmentUnsupported:
			if raw.HTML != "" || len(raw.Text) > limits.MaxOutputBytes {
				return RenderSegments{}, ErrExecutionInvalid
			}
			segment.Text = raw.Text
			if err := appendPlainTextBounded(&plain, raw.Text, limits.MaxOutputBytes); err != nil {
				return RenderSegments{}, err
			}
		default:
			return RenderSegments{}, ErrExecutionInvalid
		}
		segments = append(segments, segment)
	}
	result := RenderSegments{
		SchemaVersion: RenderSegmentsSchemaVersion, ContentID: target.ID,
		ContractVersion: target.ContractVersion, Segments: segments,
		PlainText: normalizeExecutionPlainText(plain.String()),
	}
	if err := preflightRenderSegmentsJSON(result, limits); err != nil {
		return RenderSegments{}, err
	}
	return result, nil
}

// finalizeRenderSegmentsForSSR is the only text-escaping boundary. Composition
// keeps text raw so repeated sanitizer/filter passes cannot double escape it.
func finalizeRenderSegmentsForSSR(input RenderSegments, target Contribution, limits ExecutionLimits) (RenderSegments, error) {
	result, err := normalizeRenderSegments(input, target, limits)
	if err != nil {
		return RenderSegments{}, err
	}
	escapedTotal := 0
	for index := range result.Segments {
		switch result.Segments[index].Kind {
		case SegmentText, SegmentUnsupported:
			escapedSize, sizeErr := escapedHTMLTextSize(result.Segments[index].Text, limits.MaxOutputBytes)
			if sizeErr != nil || escapedSize > limits.MaxOutputBytes-escapedTotal {
				return RenderSegments{}, ErrExecutionLimit
			}
			escapedTotal += escapedSize
			result.Segments[index].Text = html.EscapeString(result.Segments[index].Text)
		}
	}
	result.TextEncoding = RenderTextEncodingHTMLEscaped
	if err := preflightRenderSegmentsJSON(result, limits); err != nil {
		return RenderSegments{}, err
	}
	return result, nil
}

func validateSSRRenderSegments(input RenderSegments, target Contribution, limits ExecutionLimits) error {
	if input.SchemaVersion != RenderSegmentsSchemaVersion || input.ContentID != target.ID ||
		input.ContractVersion != target.ContractVersion || input.TextEncoding != RenderTextEncodingHTMLEscaped ||
		len(input.Segments) > limits.MaxSegments {
		return ErrContractStale
	}
	if err := preflightRenderSegmentsJSON(input, limits); err != nil {
		return err
	}
	var plain strings.Builder
	for _, segment := range input.Segments {
		switch segment.Kind {
		case SegmentHTML:
			if segment.Text != "" || executionReviewedHTML.Sanitize(segment.HTML) != segment.HTML {
				return ErrExecutionInvalid
			}
			text, textErr := htmlSegmentText(segment.HTML, limits)
			if textErr != nil {
				return textErr
			}
			if err := appendPlainTextBounded(&plain, text, limits.MaxOutputBytes); err != nil {
				return err
			}
		case SegmentText, SegmentUnsupported:
			if segment.HTML != "" || len(segment.Text) > limits.MaxOutputBytes {
				return ErrExecutionInvalid
			}
			decoded := html.UnescapeString(segment.Text)
			if html.EscapeString(decoded) != segment.Text {
				return ErrExecutionInvalid
			}
			if err := appendPlainTextBounded(&plain, decoded, limits.MaxOutputBytes); err != nil {
				return err
			}
		default:
			return ErrExecutionInvalid
		}
	}
	if normalizeExecutionPlainText(plain.String()) != input.PlainText {
		return ErrExecutionInvalid
	}
	return nil
}

func executionSanitizerPolicy() *bluemonday.Policy {
	policy := bluemonday.UGCPolicy()
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	policy.AllowAttrs("class").Matching(executionCodeClassPattern).OnElements("code")
	policy.AllowElements("input")
	policy.AllowAttrs("type").Matching(executionCheckboxPattern).OnElements("input")
	policy.AllowAttrs("checked").OnElements("input")
	policy.AllowAttrs("disabled").OnElements("input")
	// 正文链接统一 target="_blank"（见 Support/ContentLinks）。target 必须是策略内一等属性，
	// 否则任何一次后续净化都会静默抹掉新标签页语义，SSR 固定点校验也会失败。
	policy.AllowAttrs("target").OnElements("a")
	return policy
}

func htmlSegmentText(value string, limits ExecutionLimits) (string, error) {
	node, err := nethtml.Parse(strings.NewReader(value))
	if err != nil {
		return "", ErrExecutionInvalid
	}
	var builder strings.Builder
	type pendingNode struct {
		node  *nethtml.Node
		depth int
	}
	stack := []pendingNode{{node: node, depth: 1}}
	nodes := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes++
		if current.depth > limits.MaxJSONDepth || nodes > limits.MaxJSONNodes {
			return "", ErrExecutionLimit
		}
		if current.node.Type == nethtml.TextNode {
			if err := appendPlainTextBounded(&builder, current.node.Data, limits.MaxOutputBytes); err != nil {
				return "", err
			}
		}
		for child := current.node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, pendingNode{node: child, depth: current.depth + 1})
			if len(stack) > limits.MaxJSONNodes {
				return "", ErrExecutionLimit
			}
		}
	}
	return builder.String(), nil
}

func normalizeExecutionPlainText(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	pendingSpace := false
	for _, current := range value {
		if unicode.IsSpace(current) {
			pendingSpace = result.Len() > 0
			continue
		}
		if pendingSpace {
			result.WriteByte(' ')
			pendingSpace = false
		}
		result.WriteRune(current)
	}
	return result.String()
}

func mergeRenderSegments(target Contribution, limits ExecutionLimits, groups ...RenderSegments) (RenderSegments, error) {
	if err := preflightMergedRenderSegments(target, limits, groups...); err != nil {
		return RenderSegments{}, err
	}
	result := RenderSegments{
		SchemaVersion: RenderSegmentsSchemaVersion,
		ContentID:     target.ID, ContractVersion: target.ContractVersion,
		Segments: []RenderSegment{},
	}
	for _, group := range groups {
		result.Segments = append(result.Segments, group.Segments...)
		if len(result.Segments) > limits.MaxSegments {
			return RenderSegments{}, ErrExecutionLimit
		}
	}
	return normalizeRenderSegments(result, target, limits)
}

func preservedSourceFallback(target Contribution, limits ExecutionLimits) (RenderSegments, error) {
	return normalizeRenderSegments(RenderSegments{
		SchemaVersion: RenderSegmentsSchemaVersion,
		ContentID:     target.ID, ContractVersion: target.ContractVersion,
		Segments: []RenderSegment{{
			Kind: SegmentUnsupported,
			Text: fmt.Sprintf("Content %s is temporarily unavailable; its source is preserved.", target.ID),
		}},
	}, target, limits)
}

func hiddenRender(target Contribution) RenderSegments {
	return RenderSegments{
		SchemaVersion: RenderSegmentsSchemaVersion,
		ContentID:     target.ID, ContractVersion: target.ContractVersion,
		Segments: []RenderSegment{}, PlainText: "",
	}
}
