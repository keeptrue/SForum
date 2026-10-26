package contentsecurity

import (
	"strings"
	"testing"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

// TestHostFinalRichContentAndAttachmentXSSBoundaries is the P10 product-joined
// gate: editor HTML, active attachment MIME policy, and disposition intent stay
// Host-owned even when plugins contribute content surfaces.
func TestHostFinalRichContentAndAttachmentXSSBoundaries(t *testing.T) {
	t.Parallel()

	// 1) Editor storage HTML is always Host-sanitized (bluemonday UGC).
	raw := `<p onclick="alert(1)">hi<script>alert(2)</script>` +
		`<a href="javascript:alert(3)">x</a><img src=x onerror=alert(4)>` +
		`<strong>ok</strong></p>`
	sanitized := editordocument.SanitizeHTML(raw)
	lower := strings.ToLower(sanitized)
	for _, attack := range []string{"<script", "onclick", "onerror", "javascript:"} {
		if strings.Contains(lower, attack) {
			t.Fatalf("editor sanitizer retained %q: %s", attack, sanitized)
		}
	}
	if !strings.Contains(sanitized, "<strong>ok</strong>") {
		t.Fatalf("editor sanitizer dropped safe markup: %s", sanitized)
	}

	// 2) Active attachment MIME types are Host-denied for inline rendering.
	// Plugins cannot reclassify these through content/media declarations.
	active := []string{
		"text/html", "image/svg+xml", "application/javascript", "text/javascript",
		"application/xhtml+xml", "text/xml",
	}
	for _, mime := range active {
		if !options.IsAttachmentActiveContentType(mime) {
			t.Fatalf("expected active content type %q to be Host-blocked", mime)
		}
	}
	safe := []string{"image/png", "image/jpeg", "application/pdf", "text/plain", "audio/mpeg"}
	for _, mime := range safe {
		if options.IsAttachmentActiveContentType(mime) {
			t.Fatalf("safe content type %q must not be treated as active XSS surface", mime)
		}
	}

	// 3) Case and whitespace must not bypass the Host denylist.
	if !options.IsAttachmentActiveContentType(" Text/HTML ") ||
		!options.IsAttachmentActiveContentType("IMAGE/SVG+XML") {
		t.Fatal("active content denylist must normalize MIME case/space")
	}
}

// TestEditorAcceptRejectsScriptURLInStoredTriple proves Accept() product path
// strips javascript: links before HTMLSanitized is stored.
func TestEditorAcceptRejectsScriptURLInStoredTriple(t *testing.T) {
	t.Parallel()
	native := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[
		{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]},
		{"type":"text","text":"ok","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}
	]}]}`)
	accepted, err := editordocument.Accept(editordocument.Input{
		NativeJSON: native,
		Schema:     editordocument.CoreSchema(),
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if strings.Contains(strings.ToLower(accepted.HTMLSanitized), "javascript:") {
		t.Fatalf("stored HTML retained javascript URL: %s", accepted.HTMLSanitized)
	}
	// 站外链接保留但按宿主链接策略改写为 /outbound?u=<base64url>（Support/ContentLinks）。
	if !strings.Contains(accepted.HTMLSanitized, contentlinks.InterstitialHref("https://example.com")) {
		t.Fatalf("safe link stripped or not wrapped: %s", accepted.HTMLSanitized)
	}
}
