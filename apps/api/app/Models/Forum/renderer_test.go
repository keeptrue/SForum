package forum

import (
	"strings"
	"testing"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

func TestRenderMarkdownContentSanitizesHTML(t *testing.T) {
	rendered, err := RenderContent(ContentInput{
		RawContent:   "你好 **SForum** <script>alert(1)</script><a href=\"javascript:alert(1)\">坏链接</a>",
		SourceFormat: SourceFormatMarkdown,
		EditorType:   EditorTypeMarkdown,
	})
	if err != nil {
		t.Fatalf("RenderContent returned error: %v", err)
	}

	if !strings.Contains(rendered.HTMLContent, "<strong>SForum</strong>") {
		t.Fatalf("expected rendered markdown strong tag, got %q", rendered.HTMLContent)
	}
	if strings.Contains(rendered.HTMLContent, "<script") || strings.Contains(rendered.HTMLContent, "javascript:") {
		t.Fatalf("expected dangerous HTML to be removed, got %q", rendered.HTMLContent)
	}
	if rendered.PlainText == "" || strings.Contains(rendered.PlainText, "alert(1)") {
		t.Fatalf("expected safe plain text excerpt source, got %q", rendered.PlainText)
	}
	if rendered.Excerpt == "" {
		t.Fatal("expected excerpt to be derived from plain text")
	}
	// 摘要应可从 plain 独立再生成，与写路径结果一致。
	if ExcerptFromPlain(rendered.PlainText, RecommendedExcerptRuneLimit) != rendered.Excerpt {
		t.Fatalf("expected ExcerptFromPlain to match rendered excerpt, got %q vs %q",
			ExcerptFromPlain(rendered.PlainText, RecommendedExcerptRuneLimit), rendered.Excerpt)
	}
	if rendered.ContentHash == "" {
		t.Fatal("expected content hash to be generated")
	}
}

func TestExcerptFromPlainTruncatesByRuneLimit(t *testing.T) {
	// 210 个汉字，超过推荐 180。
	runes := make([]rune, 210)
	for i := range runes {
		runes[i] = '测'
	}
	plain := string(runes)
	excerpt := ExcerptFromPlain(plain, 180)
	// 截断后带 "..." 后缀。
	if !strings.HasSuffix(excerpt, "...") {
		t.Fatalf("expected truncated excerpt to end with ellipsis, got %q", excerpt)
	}
	// 去掉省略号后应为 180 rune。
	body := strings.TrimSuffix(excerpt, "...")
	if len([]rune(body)) != 180 {
		t.Fatalf("expected 180 runes before ellipsis, got %d", len([]rune(body)))
	}
	// 短于上限时原样返回。
	if got := ExcerptFromPlain("短摘要", 180); got != "短摘要" {
		t.Fatalf("expected short plain text unchanged, got %q", got)
	}
}

func TestRenderHTMLContentSanitizesRawHTML(t *testing.T) {
	rendered, err := RenderContent(ContentInput{
		RawContent:   `<p onclick="alert(1)">安全内容</p><iframe src="https://example.com"></iframe>`,
		SourceFormat: SourceFormatHTML,
		EditorType:   "richtext",
	})
	if err != nil {
		t.Fatalf("RenderContent returned error: %v", err)
	}

	if !strings.Contains(rendered.HTMLContent, "<p>安全内容</p>") {
		t.Fatalf("expected safe paragraph to remain, got %q", rendered.HTMLContent)
	}
	if strings.Contains(rendered.HTMLContent, "onclick") || strings.Contains(rendered.HTMLContent, "iframe") {
		t.Fatalf("expected unsafe HTML to be removed, got %q", rendered.HTMLContent)
	}
}

func TestRenderContentRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		input ContentInput
	}{
		{name: "empty raw content", input: ContentInput{RawContent: " ", SourceFormat: SourceFormatMarkdown}},
		{name: "unknown source format", input: ContentInput{RawContent: "hello", SourceFormat: "bbcode"}},
		{name: "legacy json source format", input: ContentInput{RawContent: `{"type":"doc"}`, SourceFormat: "json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderContent(tc.input); err == nil {
				t.Fatal("expected RenderContent to reject invalid input")
			}
		})
	}
}

// TestRenderMarkdownRendersGFM 验证 goldmark GFM 扩展的四种产物在 sanitize 后仍然保留：
// 表格、删除线、自动链接、任务列表 checkbox，以及代码块的 language-* class。
func TestRenderMarkdownRendersGFM(t *testing.T) {
	rendered, err := RenderContent(ContentInput{
		RawContent: `| 名称 | 值 |
| --- | --- |
| a | 1 |

~~删除线~~ <https://example.com>

- [ ] 待办
- [x] 完成

` + "```go" + `
func main(){}
` + "```",
		SourceFormat: SourceFormatMarkdown,
		EditorType:   EditorTypeMarkdown,
	})
	if err != nil {
		t.Fatalf("RenderContent returned error: %v", err)
	}
	html := rendered.HTMLContent

	// 表格
	for _, want := range []string{"<table>", "<th>", "<td>"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected GFM table to contain %q, got %q", want, html)
		}
	}
	// 删除线
	if !strings.Contains(html, "<del>") {
		t.Fatalf("expected <del> for strikethrough, got %q", html)
	}
	// 自动链接（裸 URL → <a href>）且站外链接被改写为站内中转页 + 强制新标签页。
	if !strings.Contains(html, `<a href="`+contentlinks.InterstitialPath+`?`) {
		t.Fatalf("expected autolinked href wrapped into interstitial, got %q", html)
	}
	if !strings.Contains(html, `target="_blank"`) || !strings.Contains(html, `rel="nofollow noreferrer noopener"`) {
		t.Fatalf("expected forced new-tab target and rel, got %q", html)
	}
	// 任务列表 checkbox（含 checked 状态）
	if !strings.Contains(html, `<input checked="" disabled="" type="checkbox">`) {
		t.Fatalf("expected checked tasklist checkbox, got %q", html)
	}
	if !strings.Contains(html, `<input disabled="" type="checkbox">`) {
		t.Fatalf("expected unchecked tasklist checkbox, got %q", html)
	}
	// 代码块 language-* class（供前端 highlight.js 识别语言）
	if !strings.Contains(html, `class="language-go"`) {
		t.Fatalf("expected code block language-go class, got %q", html)
	}
	// 渲染版本应为 v2
	if rendered.RenderVersion != "goldmark-bluemonday-v2" {
		t.Fatalf("expected RenderVersion goldmark-bluemonday-v2, got %q", rendered.RenderVersion)
	}
}

// TestRenderSanitizerIsConservative 验证放开 checkbox input 后 sanitizer 仍然收敛：
// 非 checkbox 的 input 被剔除，事件属性被剔除。
func TestRenderSanitizerIsConservative(t *testing.T) {
	rendered, err := RenderContent(ContentInput{
		RawContent:   `<input type="text"><input type="checkbox" onclick="alert(1)">文本`,
		SourceFormat: SourceFormatHTML,
		EditorType:   "html",
	})
	if err != nil {
		t.Fatalf("RenderContent returned error: %v", err)
	}
	html := rendered.HTMLContent
	// type=text 的 input 应被剔除
	if strings.Contains(html, `type="text"`) {
		t.Fatalf("expected type=text input to be stripped, got %q", html)
	}
	// 事件属性应被剔除
	if strings.Contains(html, "onclick") {
		t.Fatalf("expected onclick to be stripped, got %q", html)
	}
	// 仅剩 type=checkbox 的 input（无事件属性）
	if !strings.Contains(html, `<input type="checkbox">`) {
		t.Fatalf("expected a clean checkbox input to remain, got %q", html)
	}
}

func TestRenderEditorDocumentAcceptsNativeJSONAndStripsXSS(t *testing.T) {
	native := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello ","marks":[{"type":"bold"}]},{"type":"text","text":"bad","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]},{"type":"text","text":" ok","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}]}]}`
	rendered, err := RenderContent(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
		EditorType:   EditorTypeTiptap,
	})
	if err != nil {
		t.Fatalf("RenderContent: %v", err)
	}
	if rendered.RenderVersion != RenderVersionEditorDocument {
		t.Fatalf("render version = %q", rendered.RenderVersion)
	}
	if rendered.SourceFormat != SourceFormatEditorDocument {
		t.Fatalf("source format = %q", rendered.SourceFormat)
	}
	if !strings.Contains(rendered.HTMLContent, "<strong>Hello </strong>") {
		t.Fatalf("html = %q", rendered.HTMLContent)
	}
	if strings.Contains(rendered.HTMLContent, "javascript:") {
		t.Fatalf("expected javascript: stripped, got %q", rendered.HTMLContent)
	}
	if !strings.Contains(rendered.PlainText, "Hello") {
		t.Fatalf("plain = %q", rendered.PlainText)
	}
	if rendered.ContentHash == "" {
		t.Fatal("expected content hash")
	}
	// 再渲染同一 raw 应得到相同 hash（规范化存储）。
	again, err := RenderContent(ContentInput{RawContent: rendered.RawContent, SourceFormat: SourceFormatEditorDocument})
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	if again.ContentHash != rendered.ContentHash {
		t.Fatalf("hash drift: %q vs %q", rendered.ContentHash, again.ContentHash)
	}
}

func TestRenderEditorDocumentRejectsEmptyDoc(t *testing.T) {
	for _, raw := range []string{
		`{"type":"doc","content":[]}`,
		`{"type":"doc","content":[{"type":"paragraph"}]}`,
		`{"type":"doc","content":[{"type":"horizontalRule"}]}`,
	} {
		_, err := RenderContent(ContentInput{
			RawContent:   raw,
			SourceFormat: SourceFormatEditorDocument,
		})
		if err != ErrInvalidContent {
			t.Fatalf("expected ErrInvalidContent for %s, got %v", raw, err)
		}
	}
}

func TestRenderEditorDocumentAcceptsImageOnlyDoc(t *testing.T) {
	native := `{"type":"doc","content":[{"type":"image","attrs":{"src":"/media/attachments/0123456789abcdef0123456789abcdef","alt":"sample.png","attachmentId":42,"attachmentPublicId":"0123456789abcdef0123456789abcdef"}}]}`
	rendered, err := RenderContent(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
		EditorType:   EditorTypeTiptap,
	})
	if err != nil {
		t.Fatalf("RenderContent: %v", err)
	}
	if rendered.PlainText != "" || rendered.Excerpt != "" {
		t.Fatalf("image-only derived text = %q / %q", rendered.PlainText, rendered.Excerpt)
	}
	if !strings.Contains(rendered.HTMLContent, `<img src="/media/attachments/0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("html = %q", rendered.HTMLContent)
	}
	if !strings.Contains(rendered.RawContent, `"attachmentId":42`) {
		t.Fatalf("raw = %q", rendered.RawContent)
	}
}

func TestRenderEditorDocumentCoreSchemaFallbacksUnknownPluginNode(t *testing.T) {
	// 无 Editor Registry schema 时，插件节点必须稳定 fallback，不得丢弃整篇。
	native := `{"type":"doc","content":[{"type":"demoVote","attrs":{"question":"A or B?"}},{"type":"paragraph","content":[{"type":"text","text":"after"}]}]}`
	rendered, err := RenderContent(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
	})
	if err != nil {
		t.Fatalf("RenderContent: %v", err)
	}
	if strings.Contains(rendered.RawContent, `"type":"demoVote"`) {
		t.Fatalf("core schema must not persist unregistered node type, raw=%s", rendered.RawContent)
	}
	if !strings.Contains(rendered.PlainText, "after") {
		t.Fatalf("plain = %q", rendered.PlainText)
	}
}

func TestRenderEditorDocumentAdmitsPluginNodeFromMergedSchema(t *testing.T) {
	schema := editordocument.SchemaFromEditorNames([]string{"demoVote"}, nil)
	native := `{"type":"doc","content":[{"type":"demoVote","attrs":{"question":"A or B?"}},{"type":"paragraph","content":[{"type":"text","text":"after"}]}]}`
	rendered, err := RenderContentWithExcerptLimitAndSchema(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
	}, defaultExcerptRuneLimit, schema)
	if err != nil {
		t.Fatalf("RenderContent: %v", err)
	}
	if !strings.Contains(rendered.RawContent, `"type":"demoVote"`) {
		t.Fatalf("expected demoVote preserved, raw=%s", rendered.RawContent)
	}
	if !strings.Contains(rendered.HTMLContent, `data-fallback="demoVote"`) &&
		!strings.Contains(rendered.HTMLContent, "[demoVote]") {
		t.Fatalf("expected plugin node fallback html, got %q", rendered.HTMLContent)
	}
	if !strings.Contains(rendered.PlainText, "after") {
		t.Fatalf("plain = %q", rendered.PlainText)
	}
}
