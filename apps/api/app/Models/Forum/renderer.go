package forum

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/net/html"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

const defaultExcerptRuneLimit = RecommendedExcerptRuneLimit

// gfmExtender 与 extension.GFM 等价，但表格对齐输出 align 属性而不是 style：
// sanitizeHTML 使用 bluemonday，会剥掉 style，只有 align 能在净化后保留。
type gfmExtender struct{}

func (gfmExtender) Extend(markdown goldmark.Markdown) {
	extension.Linkify.Extend(markdown)
	extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)).Extend(markdown)
	extension.Strikethrough.Extend(markdown)
	extension.TaskList.Extend(markdown)
}

// plainTextSelectPrefixChars 列表/摘要查询从 plain_text 取的字符上限。
// 大于 HardExcerptMaxRunes，保证按 rune 截断前有足够源文本（含 CJK）。
const plainTextSelectPrefixChars = 2000

func RenderContent(input ContentInput) (RenderedContent, error) {
	return RenderContentWithExcerptLimit(input, defaultExcerptRuneLimit)
}

// RenderContentWithExcerptLimit 渲染正文并派生摘要（摘要不落库，仅写路径/响应使用）。
// editor-document 默认仅 CoreSchema；生产写路径应经 Service.renderContent 注入
// Editor Registry 合并 schema，避免插件 L2 节点在 Accept 时被 fallback 擦除。
func RenderContentWithExcerptLimit(input ContentInput, excerptLimit int) (RenderedContent, error) {
	return RenderContentWithExcerptLimitAndSchema(input, excerptLimit, editordocument.Schema{})
}

// RenderContentWithExcerptLimitAndSchema 与 WithExcerptLimit 相同，但 editor-document
// 使用调用方提供的 Schema（通常来自 EditorRegistry.DocumentSchema）。
// 空 Schema 时 Accept 回退到 CoreSchema。
func RenderContentWithExcerptLimitAndSchema(input ContentInput, excerptLimit int, schema editordocument.Schema) (RenderedContent, error) {
	return RenderContentWithExcerptLimitAndSchemaForResource(input, excerptLimit, schema, "")
}

// RenderContentWithExcerptLimitAndSchemaForResource additionally supplies the
// authoritative Forum resource kind for comment-only shortcode admission.
func RenderContentWithExcerptLimitAndSchemaForResource(input ContentInput, excerptLimit int, schema editordocument.Schema, resourceKind string) (RenderedContent, error) {
	raw := strings.TrimSpace(input.RawContent)
	if raw == "" {
		return RenderedContent{}, ErrInvalidContent
	}

	sourceFormat := strings.TrimSpace(input.SourceFormat)
	if sourceFormat == "" {
		sourceFormat = SourceFormatMarkdown
	}
	editorType := strings.TrimSpace(input.EditorType)
	if editorType == "" {
		editorType = sourceFormat
	}

	// EditorDocument 路径：native Tiptap JSON 经 Host Accept 管线，客户端 HTML 永不信任。
	if sourceFormat == SourceFormatEditorDocument {
		accepted, err := editordocument.Accept(editordocument.Input{
			NativeJSON:   []byte(raw),
			ExcerptLimit: excerptLimit,
			Schema:       schema,
			ResourceKind: resourceKind,
			Locale:       input.Locale,
		})
		if err != nil {
			return RenderedContent{}, ErrInvalidContent
		}
		if !hasMeaningfulEditorContent(accepted) {
			return RenderedContent{}, ErrInvalidContent
		}
		return renderedEditorDocument(accepted, input, editorType, excerptLimit)
	}

	var renderedHTML string
	safeRaw := stripUnsafeHTMLBlocks(raw)
	switch sourceFormat {
	case SourceFormatMarkdown:
		shortcodeDoc, activated, err := editordocument.ParseShortcodeMarkdown(raw, resourceKind)
		if err != nil {
			return RenderedContent{}, err
		}
		if activated {
			nativeJSON, err := json.Marshal(shortcodeDoc)
			if err != nil {
				return RenderedContent{}, err
			}
			accepted, err := editordocument.Accept(editordocument.Input{
				NativeJSON: nativeJSON, Schema: schema, ExcerptLimit: excerptLimit, ResourceKind: resourceKind,
				Locale: input.Locale,
			})
			if err != nil || !hasMeaningfulEditorContent(accepted) {
				return RenderedContent{}, ErrInvalidContent
			}
			return renderedEditorDocument(accepted, input, EditorTypeTiptap, excerptLimit)
		}
		var buffer bytes.Buffer
		// 启用与 extension.GFM 等价的扩展：表格、删除线、自动链接、任务列表。
		// 与前端 Tiptap 编辑器的 gfm:true 保持一致，避免"编辑器预览 ≠ 发布结果"。
		md := goldmark.New(goldmark.WithExtensions(gfmExtender{}))
		if err := md.Convert([]byte(safeRaw), &buffer); err != nil {
			return RenderedContent{}, err
		}
		renderedHTML = sanitizeHTML(buffer.String())
	case SourceFormatHTML:
		renderedHTML = sanitizeHTML(safeRaw)
	default:
		return RenderedContent{}, ErrInvalidContent
	}

	plain := normalizePlainText(htmlToText(renderedHTML))
	if plain == "" {
		return RenderedContent{}, ErrInvalidContent
	}

	hash := sha256.Sum256([]byte(sourceFormat + "\x00" + raw))
	return RenderedContent{
		RawContent:    raw,
		HTMLContent:   renderedHTML,
		PlainText:     plain,
		Excerpt:       ExcerptFromPlain(plain, excerptLimit),
		SourceFormat:  sourceFormat,
		EditorType:    editorType,
		EditorVersion: strings.TrimSpace(input.EditorVersion),
		RenderVersion: RenderVersion,
		ContentHash:   hex.EncodeToString(hash[:]),
	}, nil
}

func renderedEditorDocument(accepted editordocument.Accepted, input ContentInput, editorType string, excerptLimit int) (RenderedContent, error) {
	storedRaw, err := jsonMarshalDocument(accepted)
	if err != nil {
		return RenderedContent{}, err
	}
	return RenderedContent{
		RawContent:    storedRaw,
		HTMLContent:   accepted.HTMLSanitized,
		PlainText:     accepted.PlainText,
		Excerpt:       ExcerptFromPlain(accepted.PlainText, excerptLimit),
		SourceFormat:  SourceFormatEditorDocument,
		EditorType:    firstNonEmpty(editorType, EditorTypeTiptap),
		EditorVersion: strings.TrimSpace(input.EditorVersion),
		RenderVersion: RenderVersionEditorDocument,
		ContentHash:   accepted.ContentHash,
	}, nil
}

// hasMeaningfulEditorContent 保持空结构无效，同时允许已验收的图片节点独立成文。
func hasMeaningfulEditorContent(accepted editordocument.Accepted) bool {
	if strings.TrimSpace(accepted.PlainText) != "" {
		return true
	}

	var containsRenderableNode func([]editordocument.Node) bool
	containsRenderableNode = func(nodes []editordocument.Node) bool {
		for _, node := range nodes {
			if node.Type == "image" || node.Type == editordocument.ShortcodeRefNode ||
				node.Type == editordocument.ShortcodeBlockNode || containsRenderableNode(node.Content) {
				return true
			}
		}
		return false
	}
	return containsRenderableNode(accepted.Native.Content)
}

func jsonMarshalDocument(accepted editordocument.Accepted) (string, error) {
	// 持久化 Accept 规范化后的 Document；encoding/json 对 map 键排序保证稳定。
	body, err := json.Marshal(accepted.Native)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func contentMentionSource(content RenderedContent) string {
	if content.SourceFormat != SourceFormatEditorDocument {
		return content.RawContent
	}
	doc, err := editordocument.Parse(editordocument.Input{NativeJSON: []byte(content.RawContent)})
	if err != nil {
		return content.PlainText
	}
	return editordocument.RenderPublicSideEffectMarkdown(doc)
}

// PublicMentionSource reconstructs only the Host-approved side-effect view.
// Protected descendants are replaced by stable fallback text and therefore
// cannot create mention notifications during moderation replay.
func PublicMentionSource(rawContent, sourceFormat string) string {
	if strings.TrimSpace(sourceFormat) != SourceFormatEditorDocument {
		return rawContent
	}
	doc, err := editordocument.Parse(editordocument.Input{NativeJSON: []byte(rawContent)})
	if err != nil {
		return ""
	}
	return editordocument.RenderPublicSideEffectMarkdown(doc)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ExcerptFromPlain 按运营配置的 rune 上限从纯文本派生列表/引用摘要。
// 不落库；读路径与写路径共用，保证改 excerpt_rune_limit 后旧帖立即生效。
func ExcerptFromPlain(plain string, limit int) string {
	if limit < HardExcerptMinRunes || limit > HardExcerptMaxRunes {
		limit = defaultExcerptRuneLimit
	}
	return makeExcerpt(strings.TrimSpace(plain), limit)
}

// plainTextPrefixSQL 从 posts.plain_text 取前缀供摘要派生，避免列表 SELECT 全量正文。
func plainTextPrefixSQL(column string) string {
	return fmt.Sprintf("left(%s, %d)", column, plainTextSelectPrefixChars)
}

var unsafeHTMLBlockPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`),
	regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`),
	regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>`),
	regexp.MustCompile(`(?is)<object\b[^>]*>.*?</object>`),
	regexp.MustCompile(`(?is)<embed\b[^>]*>.*?</embed>`),
}

func stripUnsafeHTMLBlocks(value string) string {
	for _, pattern := range unsafeHTMLBlockPatterns {
		value = pattern.ReplaceAllString(value, "")
	}
	return value
}

// codeClassPattern 匹配 goldmark 代码块输出的 language-<lang> class，供 highlight.js 精确识别语言。
// 仅允许字母/数字与少量符号，避免 class 成为属性注入入口。
var codeClassPattern = regexp.MustCompile(`^language-[a-z0-9+#.-]+$`)

// checkboxTypePattern 仅允许 type="checkbox"，用于 GFM 任务列表（TaskList 输出只读 checkbox）。
var checkboxTypePattern = regexp.MustCompile(`^checkbox$`)

func sanitizeHTML(value string) string {
	policy := bluemonday.UGCPolicy()
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)

	// 保留代码块 language-* class，供前端 highlight.js 识别语言。
	policy.AllowAttrs("class").Matching(codeClassPattern).OnElements("code")

	// 放开 GFM 任务列表的 checkbox：仅允许 type=checkbox，且只保留 checked/disabled。
	// 事件属性（onclick 等）与非 checkbox input 仍会被 bluemonday 剔除，放开是收敛的。
	policy.AllowElements("input")
	policy.AllowAttrs("type").Matching(checkboxTypePattern).OnElements("input")
	policy.AllowAttrs("checked").OnElements("input")
	policy.AllowAttrs("disabled").OnElements("input")

	// 正文链接统一 target="_blank"（见 Support/ContentLinks）：target 必须在策略内，
	// 否则再次净化会静默抹掉新标签页语义。
	policy.AllowAttrs("target").OnElements("a")

	// 净化后统一收敛链接呈现：正文链接强制新标签页，站外链接改写为站内中转页。
	return contentlinks.Apply(policy.Sanitize(value))
}

func htmlToText(value string) string {
	node, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return value
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			builder.WriteString(n.Data)
			builder.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func normalizePlainText(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	return strings.Join(fields, " ")
}

func makeExcerpt(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "..."
}
