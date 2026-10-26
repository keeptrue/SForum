package main

import (
	"encoding/json"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	protectedMaxNodes = 512
	protectedMaxDepth = 32
)

type protectedCallValue struct {
	Decision string          `json:"decision"`
	Fragment json.RawMessage `json:"fragment"`
}

type protectedDocument struct {
	Type    string          `json:"type"`
	Content []protectedNode `json:"content"`
}

type protectedNode struct {
	Type    string                     `json:"type"`
	Attrs   map[string]json.RawMessage `json:"attrs,omitempty"`
	Content []protectedNode            `json:"content,omitempty"`
	Text    string                     `json:"text,omitempty"`
	Marks   []protectedMark            `json:"marks,omitempty"`
}

type protectedMark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs,omitempty"`
}

func (p *shortcodePlugin) renderProtected(call *pluginv2.ContentCall) (pluginv2.ContentResult, error) {
	if call == nil || call.Scope != "protected" || call.Document.SchemaVersion != pluginv2.ContentEditorDocumentSchema {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(call.Document.Value)))
	decoder.DisallowUnknownFields()
	var value protectedCallValue
	if decoder.Decode(&value) != nil || value.Decision != "allowed" || len(value.Fragment) == 0 {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	documentDecoder := json.NewDecoder(strings.NewReader(string(value.Fragment)))
	documentDecoder.DisallowUnknownFields()
	var document protectedDocument
	if documentDecoder.Decode(&document) != nil || document.Type != "doc" || len(document.Content) == 0 {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	if documentDecoder.Decode(&trailing) != io.EOF {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	var builder strings.Builder
	nodes := 0
	for _, node := range document.Content {
		if !renderProtectedNode(&builder, node, 1, &nodes, call.Locale) {
			return pluginv2.ContentResult{}, errShortcodeCallInput
		}
	}
	if strings.TrimSpace(builder.String()) == "" {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	return renderResult(call, builder.String()), nil
}

func renderProtectedNode(builder *strings.Builder, node protectedNode, depth int, count *int, locale string) bool {
	*count++
	if depth > protectedMaxDepth || *count > protectedMaxNodes {
		return false
	}
	switch node.Type {
	case "paragraph", "blockquote", "bulletList", "listItem":
		tags := map[string][2]string{
			"paragraph": {"<p>", "</p>"}, "blockquote": {"<blockquote>", "</blockquote>"},
			"bulletList": {"<ul>", "</ul>"}, "listItem": {"<li>", "</li>"},
		}[node.Type]
		builder.WriteString(tags[0])
		if !renderProtectedChildren(builder, node.Content, depth, count, locale) {
			return false
		}
		builder.WriteString(tags[1])
	case "orderedList":
		start := protectedPositiveInt(node.Attrs["start"], 1, 1_000_000)
		if start == 1 {
			builder.WriteString("<ol>")
		} else {
			builder.WriteString(`<ol start="` + strconv.Itoa(start) + `">`)
		}
		if !renderProtectedChildren(builder, node.Content, depth, count, locale) {
			return false
		}
		builder.WriteString("</ol>")
	case "heading":
		level := protectedPositiveInt(node.Attrs["level"], 2, 6)
		tag := "h" + strconv.Itoa(level)
		builder.WriteString("<" + tag + ">")
		if !renderProtectedChildren(builder, node.Content, depth, count, locale) {
			return false
		}
		builder.WriteString("</" + tag + ">")
	case "text":
		if len(node.Content) != 0 || node.Text == "" {
			return false
		}
		renderProtectedText(builder, node.Text, node.Marks)
	case "hardBreak":
		builder.WriteString("<br>")
	case "horizontalRule":
		builder.WriteString("<hr>")
	case "codeBlock":
		builder.WriteString("<pre><code>")
		for _, child := range node.Content {
			if child.Type != "text" || len(child.Content) != 0 {
				return false
			}
			builder.WriteString(html.EscapeString(child.Text))
		}
		builder.WriteString("</code></pre>")
	case "sforumEmoji":
		value := protectedString(node.Attrs["native"])
		if value == "" {
			value = ":" + protectedString(node.Attrs["name"]) + ":"
		}
		builder.WriteString(html.EscapeString(value))
	case "image":
		src := protectedSafeURL(protectedString(node.Attrs["src"]))
		if src == "" {
			builder.WriteString(`<span>[image]</span>`)
			return true
		}
		builder.WriteString(`<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(protectedString(node.Attrs["alt"])) + `" loading="lazy">`)
	case "sforumShortcodeBlock":
		if !renderProtectedChildren(builder, node.Content, depth, count, locale) {
			return false
		}
	case "sforumShortcodeRef":
		builder.WriteString(`<span class="sf-editor-fallback" data-fallback="shortcode.reference.unavailable">` + html.EscapeString(protectedReferenceFallback(locale)) + `</span>`)
	default:
		builder.WriteString(`<span class="sf-editor-fallback" data-fallback="shortcode.reference.unavailable">` + html.EscapeString(protectedReferenceFallback(locale)) + `</span>`)
	}
	return true
}

func renderProtectedChildren(builder *strings.Builder, children []protectedNode, depth int, count *int, locale string) bool {
	for _, child := range children {
		if !renderProtectedNode(builder, child, depth+1, count, locale) {
			return false
		}
	}
	return true
}

func renderProtectedText(builder *strings.Builder, value string, marks []protectedMark) {
	prefixes := make([]string, 0, len(marks))
	suffixes := make([]string, 0, len(marks))
	for _, mark := range marks {
		switch mark.Type {
		case "bold":
			prefixes, suffixes = append(prefixes, "<strong>"), append([]string{"</strong>"}, suffixes...)
		case "italic":
			prefixes, suffixes = append(prefixes, "<em>"), append([]string{"</em>"}, suffixes...)
		case "strike":
			prefixes, suffixes = append(prefixes, "<s>"), append([]string{"</s>"}, suffixes...)
		case "code":
			prefixes, suffixes = append(prefixes, "<code>"), append([]string{"</code>"}, suffixes...)
		case "underline":
			prefixes, suffixes = append(prefixes, "<u>"), append([]string{"</u>"}, suffixes...)
		case "link":
			href := protectedSafeURL(protectedString(mark.Attrs["href"]))
			if href != "" {
				prefixes = append(prefixes, `<a href="`+html.EscapeString(href)+`" rel="nofollow noopener noreferrer">`)
				suffixes = append([]string{"</a>"}, suffixes...)
			}
		}
	}
	builder.WriteString(strings.Join(prefixes, ""))
	builder.WriteString(html.EscapeString(value))
	builder.WriteString(strings.Join(suffixes, ""))
}

func protectedString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || len([]rune(value)) > 4096 {
		return ""
	}
	return value
}

func protectedPositiveInt(raw json.RawMessage, fallback, maximum int) int {
	var value int
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 1 || value > maximum {
		return fallback
	}
	return value
}

func protectedSafeURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return ""
	}
	if parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if !parsed.IsAbs() && !strings.HasPrefix(value, "/") {
		return ""
	}
	return parsed.String()
}

func protectedReferenceFallback(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en") {
		return "Reference unavailable"
	}
	return "引用暂不可用"
}
