package forum

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

var mentionPattern = regexp.MustCompile(`(?:^|[^\pL\pN_])@([\pL\pN_-]{1,` + strconv.Itoa(mentionUsernameMaxLength) + `})`)

// mentionUsernameMaxLength 与 Options.usernameMaxLengthMax 对齐：identity.username.max_length
// 的可配置上限是 64，超过就不可能解析成用户名（旧值 50 会漏掉长用户名的提及）。
// token 字符集同时包含 '-'，与 identity.UsernamePolicy 允许的字符集一致。
const mentionUsernameMaxLength = 64

// MentionedUsernames 从已经通过 Host 过滤的 Markdown 源中提取提及。代码块和
// 行内代码被忽略，供创建与审批后的通知重放共用。
func MentionedUsernames(markdown string) []string {
	source := []byte(markdown)
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	var visible strings.Builder
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindCodeSpan:
			visible.WriteByte(' ')
			return ast.WalkSkipChildren, nil
		case ast.KindParagraph, ast.KindHeading:
			visible.WriteByte(' ')
		case ast.KindText:
			visible.Write(node.(*ast.Text).Segment.Value(source))
		}
		return ast.WalkContinue, nil
	})

	seen := map[string]struct{}{}
	items := []string{}
	for _, match := range mentionPattern.FindAllStringSubmatch(visible.String(), -1) {
		key := strings.ToLower(match[1])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, match[1])
	}
	return items
}

// MentionedUsernamesFromSource 按已存储正文的格式选择提及解析源。
//
// editor-document 存的是 native Tiptap JSON，直接按 Markdown 解析会把代码块里的文本
// 也算成可见段落：@ 出现在代码块中会被误判为提及。先经 Accept 还原 Markdown，
// 复用与展示一致的代码块边界；还原失败时退回原文解析，保持解析永不失败。
func MentionedUsernamesFromSource(rawContent, sourceFormat string) []string {
	if sourceFormat != SourceFormatEditorDocument {
		return MentionedUsernames(rawContent)
	}
	accepted, err := editordocument.Accept(editordocument.Input{NativeJSON: []byte(rawContent)})
	if err != nil {
		return MentionedUsernames(rawContent)
	}
	return MentionedUsernames(accepted.Markdown)
}
