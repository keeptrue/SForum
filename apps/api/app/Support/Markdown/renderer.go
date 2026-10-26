package markdown

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
)

// RenderSafe renders operator-authored Markdown and sanitizes the result before
// it crosses an HTTP presentation boundary.
func RenderSafe(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	var buffer bytes.Buffer
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	if err := parser.Convert([]byte(value), &buffer); err != nil {
		return "", err
	}

	policy := bluemonday.NewPolicy()
	policy.AllowElements("a", "p", "br", "strong", "b", "em", "i", "ul", "ol", "li")
	policy.AllowAttrs("href").Matching(regexp.MustCompile(`^(https?://|/|#|mailto:)`)).OnElements("a")
	policy.AllowAttrs("title", "rel", "target").OnElements("a")
	policy.AllowStandardURLs()
	policy.RequireParseableURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	// 运营 Markdown 同样收敛：链接强制新标签页，站外链接走站内中转页。
	return contentlinks.Apply(policy.Sanitize(buffer.String())), nil
}
