package contentregistry

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

// 前后端净化白名单契约（H3 纵深防御的接缝守卫）。
//
// 背景：后端 bluemonday（executionReviewedHTML）放行一批标签，前端 DOMPurify
// （apps/web/app/utils/sfSanitize.ts 的 SF_PURIFY_CONFIG.ALLOWED_TAGS）再净化
// 一次。两者一旦不对齐，前端会静默剥掉后端刚渲染出的标签——标签没了但文本
// 还在，CSS 后代选择器全部落空，表面上只呈现为「样式没生效」，极难定位。
// 已发生过一次：shortcode 卡片的 <article>/<time> 被剥，卡片 CSS 全失效。
//
// 这里不枚举「后端允许什么」，而是断言「后端实际渲染出的标签 ⊆ 前端白名单」，
// 这样后端新增任何一个渲染标签而前端没跟上，测试立刻红。
var htmlTagPattern = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)`)

// collectRenderedTags 提取一段 HTML 里真实出现的标签名。
func collectRenderedTags(html string) []string {
	seen := make(map[string]bool)
	for _, match := range htmlTagPattern.FindAllStringSubmatch(html, -1) {
		seen[strings.ToLower(match[1])] = true
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func frontendSanitizeRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// apps/api/app/Support/ContentRegistry → 仓库根为五层之上
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
}

// frontendAllowedTags 从前端 sfSanitize.ts 解析出 ALLOWED_TAGS 白名单。
// 直接读源码而非复制常量：复制的常量会在前端改了之后继续绿，失去守卫意义。
func frontendAllowedTags(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(frontendSanitizeRepoRoot(t), "apps", "web", "app", "utils", "sfSanitize.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取前端净化配置失败（契约测试依赖它）: %v", err)
	}
	block := regexp.MustCompile(`(?s)ALLOWED_TAGS:\s*\[(.*?)\]`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatalf("%s 中未找到 ALLOWED_TAGS 白名单", path)
	}
	allowed := make(map[string]bool)
	names := regexp.MustCompile(`'([a-zA-Z][a-zA-Z0-9]*)'`).FindAllStringSubmatch(block[1], -1)
	if len(names) == 0 {
		t.Fatalf("%s 的 ALLOWED_TAGS 为空，契约测试无法守卫", path)
	}
	for _, name := range names {
		allowed[strings.ToLower(name[1])] = true
	}
	return allowed
}

// assertTagsCovered 断言 layer 渲染出的标签全部在前端白名单内。
func assertTagsCovered(t *testing.T, layer, html string, extra []string, allowed map[string]bool) {
	t.Helper()
	tags := collectRenderedTags(html)
	tags = append(tags, extra...)
	var missing []string
	seen := make(map[string]bool)
	for _, tag := range tags {
		if allowed[tag] || seen[tag] {
			continue
		}
		seen[tag] = true
		missing = append(missing, tag)
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	t.Errorf(
		"%s 渲染出的标签 %v 不在前端 SF_PURIFY_CONFIG.ALLOWED_TAGS 内；"+
			"前端 DOMPurify 会把它们剥掉（标签没了、文本还在，CSS 后代选择器静默失效）。"+
			"修复方式：把它们加入 apps/web/app/utils/sfSanitize.ts 的 ALLOWED_TAGS，"+
			"或调整后端渲染改用已在白名单内的标签。", layer, missing)
}

// TestEditorDocumentRenderTagsAreFrontendAllowed 覆盖 EditorDocument 渲染管线：
// 遍历 CoreSchema 的每个节点与每种 mark，断言渲染结果不越出前端白名单。
func TestEditorDocumentRenderTagsAreFrontendAllowed(t *testing.T) {
	allowed := frontendAllowedTags(t)
	schema := editordocument.CoreSchema()

	allMarks := []editordocument.Mark{
		{Type: "bold"}, {Type: "italic"}, {Type: "strike"}, {Type: "code"}, {Type: "underline"},
		{Type: "link", Attrs: map[string]any{"href": "/x", "target": "_blank", "rel": "noopener"}},
		{Type: "demoHighlight", Attrs: map[string]any{"color": "#fff"}},
	}
	textNode := func(text string) []editordocument.Node {
		return []editordocument.Node{{Type: "text", Text: text, Marks: allMarks}}
	}

	// 覆盖 CoreSchema 声明的全部节点类型（doc/text 由外层结构承载，不单独渲染）
	document := editordocument.Document{Type: "doc", Content: []editordocument.Node{
		{Type: "paragraph", Content: textNode("段落")},
		{Type: "heading", Attrs: map[string]any{"level": float64(2)}, Content: textNode("标题2")},
		{Type: "heading", Attrs: map[string]any{"level": float64(6)}, Content: textNode("标题6")},
		{Type: "blockquote", Content: []editordocument.Node{{Type: "paragraph", Content: textNode("引用")}}},
		{Type: "codeBlock", Attrs: map[string]any{"language": "go"}, Content: textNode("代码")},
		{Type: "bulletList", Content: []editordocument.Node{
			{Type: "listItem", Content: []editordocument.Node{{Type: "paragraph", Content: textNode("无序项")}}},
		}},
		{Type: "orderedList", Attrs: map[string]any{"start": float64(3)}, Content: []editordocument.Node{
			{Type: "listItem", Content: []editordocument.Node{{Type: "paragraph", Content: textNode("有序项")}}},
		}},
		{Type: "hardBreak"},
		{Type: "horizontalRule"},
		{Type: "image", Attrs: map[string]any{"src": "/x.png", "alt": "图", "title": "标题", "width": float64(10), "height": float64(10), "displaySize": "standard"}},
		{Type: "sforumEmoji", Attrs: map[string]any{"name": "smile", "label": "微笑", "native": "x"}},
		{Type: editordocument.ShortcodeRefNode, Attrs: map[string]any{
			"id": "sforum-shortcodes.comment", "contractVersion": "sforum-shortcodes.comment@1",
			"arguments": map[string]any{"commentId": float64(1)},
		}},
		{Type: editordocument.ShortcodeBlockNode, Attrs: map[string]any{
			"id": "sforum-shortcodes.login", "contractVersion": "sforum-shortcodes.login@1",
			"arguments": map[string]any{},
		}, Content: []editordocument.Node{{Type: "paragraph", Content: textNode("受保护")}}},
	}}

	// RenderHTMLForLocale：Accept 写入路径（plan=nil，shortcode 走 fallback）
	assertTagsCovered(t, "EditorDocument(写入路径)",
		editordocument.RenderHTMLForLocale(document, schema, "zh-CN"), nil, allowed)
	// RenderHTMLWithAllShortcodeSlots：公开读取路径（输出占位符 + 保留受保护片段）
	template, _, _ := editordocument.RenderHTMLWithAllShortcodeSlots(document, schema, "zh-CN")
	assertTagsCovered(t, "EditorDocument(读取路径模板)", template, nil, allowed)
	// 英文 locale 只换文案不换标签，一并纳入避免 locale 分支漏 guard
	assertTagsCovered(t, "EditorDocument(en-US)",
		editordocument.RenderHTMLForLocale(document, schema, "en-US"), nil, allowed)
}

// TestShortcodeWrapperTagsAreFrontendAllowed 覆盖 Core 包装器与降级 HTML：
// 这两处是 Core 在 sanitize 之后拼上的，前端同样会二次净化。
func TestShortcodeWrapperTagsAreFrontendAllowed(t *testing.T) {
	allowed := frontendAllowedTags(t)

	// 每种 shortcode 包装类都过一遍，确保新增 kind 不会悄悄引入新标签
	for _, id := range []string{
		ShortcodeUserID, ShortcodeTopicID, ShortcodeCommentID, ShortcodeCategoryID, ShortcodeFriendLinksID,
		ShortcodeLoginID, ShortcodeReplyID, ShortcodeOnlyAuthorID,
	} {
		wrapped := WrapForumShortcodeHTML(id, `<article><p>标签</p><h3><a href="/x">标题</a></h3><p>摘要</p><time>2026-01-01 00:00:00</time></article>`, false)
		assertTagsCovered(t, "WrapForumShortcodeHTML("+id+")", wrapped, nil, allowed)

		fallbackWrapped := WrapForumShortcodeHTML(id, `<span class="sf-editor-fallback" data-fallback="x">不可用</span>`, true)
		assertTagsCovered(t, "WrapForumShortcodeHTML("+id+",fallback)", fallbackWrapped, nil, allowed)
	}
}

// TestShortcodePluginRenderTagsAreFrontendAllowed 覆盖 shortcode 插件本体。
// 插件是独立 Go module 的 package main，无法 import，因此退化为扫描其渲染
// 源码里的标签字面量——比运行时探测弱，但足以拦住「插件新输出一个标签」。
func TestShortcodePluginRenderTagsAreFrontendAllowed(t *testing.T) {
	allowed := frontendAllowedTags(t)
	root := frontendSanitizeRepoRoot(t)
	dir := filepath.Join(root, "extensions", "builtin", "plugins", "sforum-shortcodes", "backend")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 shortcode 插件目录失败: %v", err)
	}
	pattern := regexp.MustCompile("`<(/?[a-zA-Z][a-zA-Z0-9]*)")
	tags := make([]string, 0)
	seen := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go")) {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			continue
		}
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			tag := strings.ToLower(strings.TrimPrefix(match[1], "/"))
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		t.Fatal("未在 shortcode 插件源码里扫到任何渲染标签，契约断言失去意义，请检查扫描正则")
	}
	assertTagsCovered(t, "sforum-shortcodes 插件渲染", "", tags, allowed)
}
