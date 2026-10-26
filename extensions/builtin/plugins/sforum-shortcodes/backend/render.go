package main

import (
	"html"
	"net/url"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

// htmlSegments converts complete inline-safe card HTML into typed render
// segments. The Host sanitizer remains the final authority; every dynamic
// value below is html.EscapeString-escaped before being passed. The plugin
// deliberately emits no class/style attributes so the Host sanitizer output is
// stable and the card never claims a presentation contract it cannot own.
func htmlSegments(values ...string) []pluginv2.ContentRenderSegment {
	segments := make([]pluginv2.ContentRenderSegment, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		segments = append(segments, pluginv2.ContentRenderSegment{
			Kind: pluginv2.ContentSegmentHTML, HTML: value,
		})
	}
	return segments
}

// safePathEscape escapes one single untrusted path segment so reconstructed
// Core URLs cannot smuggle additional slashes, query, fragment, or backslash.
// The static Core path prefix is provided by the caller and never contains a
// projection value.
func safePathEscape(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return ""
	}
	return url.PathEscape(value)
}

// profilePath builds the fixed Core public profile path with the Host-projected
// username as one fully escaped segment.
func profilePath(username string) string {
	segment := safePathEscape(username)
	if segment == "" {
		return ""
	}
	return "/u/" + segment
}

// categoryPath builds the fixed Core public category path with the
// Host-projected slug as one fully escaped segment.
func categoryPath(slug string) string {
	segment := safePathEscape(slug)
	if segment == "" {
		return ""
	}
	return "/c/" + segment
}

func topicPath(id int64, slug string) string {
	segment := safePathEscape(slug)
	if id <= 0 || segment == "" {
		return ""
	}
	return "/t/" + strconv.FormatInt(id, 10) + "/" + segment
}

func commentPath(comment publicComment) string {
	base := topicPath(comment.TopicID, comment.TopicSlug)
	if base == "" || comment.ID <= 0 || !comment.OwningTopicPublic {
		return ""
	}
	return base + "#comment-" + strconv.FormatInt(comment.ID, 10)
}

// safeLinkURL allows only http(s) absolute links for operator-owned friend
// links; everything else renders as plain bounded text.
func safeLinkURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String()
	default:
		return ""
	}
}

// boundedText caps display text to 512 Unicode runes so a single malicious or
// enormous projection field cannot blow up the render output.
func boundedText(value string) string {
	runes := []rune(value)
	limit := 512
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// renderUserCard builds the public user summary card. The profile link
// (/u/:username) is a static Core public path reconstructed only from the
// Host projection; untrusted node arguments never form a URL.
func renderUserCard(user publicUser, label renderCopy) string {
	path := profilePath(user.Username)
	displayName := strings.TrimSpace(user.DisplayName)
	username := strings.TrimSpace(user.Username)
	if path == "" || username == "" {
		return ""
	}
	if displayName == "" {
		displayName = username
	}
	var builder strings.Builder
	builder.WriteString(`<div><a href="` + path + `">` + html.EscapeString(boundedText(displayName)) + `</a>`)
	builder.WriteString(` <span>@` + html.EscapeString(boundedText(username)) + `</span></div>`)
	builder.WriteString(`<p>` + html.EscapeString(label.userCard) + `</p>`)
	return builder.String()
}

func renderCategoryCard(category publicCategory, label renderCopy) string {
	path := categoryPath(category.Slug)
	name := strings.TrimSpace(category.Name)
	if path == "" || name == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(`<div><a href="` + path + `">` + html.EscapeString(boundedText(name)) + `</a>`)
	if description := strings.TrimSpace(category.Description); description != "" {
		builder.WriteString(` <span>` + html.EscapeString(boundedText(description)) + `</span>`)
	}
	builder.WriteString(`</div>`)
	builder.WriteString(`<p>` + html.EscapeString(label.categoryCard) + `</p>`)
	return builder.String()
}

func renderTopicCard(topic publicTopic, label renderCopy) string {
	path := topicPath(topic.ID, topic.Slug)
	title := strings.TrimSpace(topic.Title)
	if path == "" || title == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(`<article><p>` + html.EscapeString(label.topicCard) + `</p>`)
	builder.WriteString(`<h3><a href="` + path + `">` + html.EscapeString(boundedText(title)) + `</a></h3>`)
	if category := strings.TrimSpace(topic.CategoryName); category != "" {
		builder.WriteString(`<p>` + html.EscapeString(boundedText(category)) + `</p>`)
	}
	if excerpt := strings.TrimSpace(topic.Excerpt); excerpt != "" {
		builder.WriteString(`<p>` + html.EscapeString(boundedText(excerpt)) + `</p>`)
	}
	builder.WriteString(`</article>`)
	return builder.String()
}

func renderCommentCard(comment publicComment, label renderCopy) string {
	path := commentPath(comment)
	if path == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(`<article><p>` + html.EscapeString(label.commentCard) + `</p>`)
	builder.WriteString(`<h3><a href="` + path + `">` + html.EscapeString(boundedText(strings.TrimSpace(comment.TopicTitle))) + `</a></h3>`)
	if excerpt := strings.TrimSpace(comment.Excerpt); excerpt != "" {
		builder.WriteString(`<p>` + html.EscapeString(boundedText(excerpt)) + `</p>`)
	}
	if createdAt := strings.TrimSpace(comment.CreatedAt); createdAt != "" {
		builder.WriteString(`<time>` + html.EscapeString(boundedText(createdAt)) + `</time>`)
	}
	builder.WriteString(`</article>`)
	return builder.String()
}

func renderUnavailable(copyValue string) string {
	return `<p>` + html.EscapeString(copyValue) + `</p>`
}

// renderFriendLinksBlock emits one bounded list of enabled operator links.
// Every URL passes safeLinkURL and every text value is escaped; an empty
// result set produces no public output at all.
func renderFriendLinksBlock(links []publicFriendLink, label renderCopy) string {
	items := make([]string, 0, len(links))
	for _, link := range links {
		nameValue := strings.TrimSpace(link.Name)
		if link.ID <= 0 || nameValue == "" {
			continue
		}
		var item strings.Builder
		item.WriteString(`<li>`)
		name := html.EscapeString(boundedText(nameValue))
		if safe := safeLinkURL(link.URL); safe != "" {
			item.WriteString(`<a href="` + html.EscapeString(safe) + `">` + name + `</a>`)
		} else {
			item.WriteString(name)
		}
		if description := strings.TrimSpace(link.Description); description != "" {
			item.WriteString(` <span>` + html.EscapeString(boundedText(description)) + `</span>`)
		}
		item.WriteString(`</li>`)
		items = append(items, item.String())
	}
	if len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(`<div>`)
	title := strings.TrimSpace(label.friendLinksTitle)
	if title != "" {
		builder.WriteString(`<p>` + html.EscapeString(title) + `</p>`)
	}
	builder.WriteString(`<ul>`)
	for _, item := range items {
		builder.WriteString(item)
	}
	builder.WriteString(`</ul>`)
	builder.WriteString(`</div>`)
	return builder.String()
}
