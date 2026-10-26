package pageviewmodels

import (
	"encoding/json"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func mapForumTopics(items []forum.TopicSummary, topicURLMode string) []themecompiler.TopicSummaryView {
	result := make([]themecompiler.TopicSummaryView, 0, len(items))
	for _, item := range items {
		result = append(result, mapForumTopic(item, topicURLMode))
	}
	return result
}

func mapForumTopic(item forum.TopicSummary, topicURLMode string) themecompiler.TopicSummaryView {
	tags := make([]themecompiler.TaxonomyLinkView, 0, len(item.Tags))
	for _, tag := range item.Tags {
		tags = append(tags, themecompiler.TaxonomyLinkView{
			ID: tag.ID, Name: tag.Name, Slug: tag.Slug, URL: "/tags/" + url.PathEscape(tag.Slug),
		})
	}
	return themecompiler.TopicSummaryView{
		ID: item.ID, Title: item.Title, URL: topicPublicPath(item.ID, item.Slug, topicURLMode),
		Excerpt: item.Excerpt, Author: mapForumUser(item.Author, item.AuthorUserID),
		Category: themecompiler.TaxonomyLinkView{
			ID: item.CategoryID, Name: item.CategoryName, Slug: item.CategorySlug,
			URL: "/c/" + url.PathEscape(item.CategorySlug),
		},
		Tags: tags, ReplyCount: item.CommentCount,
		CreatedAt: formatViewTime(item.CreatedAt), UpdatedAt: formatViewTime(item.UpdatedAt),
	}
}

func mapSearchTopics(items []search.TopicSearchDoc, topicURLMode string) []themecompiler.TopicSummaryView {
	result := make([]themecompiler.TopicSummaryView, 0, len(items))
	for _, item := range items {
		tags := make([]themecompiler.TaxonomyLinkView, 0, len(item.TagSlugs))
		for _, slug := range item.TagSlugs {
			tags = append(tags, themecompiler.TaxonomyLinkView{Name: slug, Slug: slug, URL: "/tags/" + url.PathEscape(slug)})
		}
		result = append(result, themecompiler.TopicSummaryView{
			ID: item.ID, Title: item.Title, URL: topicPublicPath(item.ID, item.Slug, topicURLMode),
			Excerpt: item.Excerpt,
			Author: themecompiler.PublicUserView{
				ID: item.AuthorUserID, Username: item.AuthorUsername, DisplayName: item.AuthorDisplayName,
			},
			Category: themecompiler.TaxonomyLinkView{
				ID: item.CategoryID, Name: item.CategoryName, Slug: item.CategorySlug,
				URL: "/c/" + url.PathEscape(item.CategorySlug),
			},
			Tags: tags, ReplyCount: item.CommentCount,
			CreatedAt: formatViewTime(item.CreatedAt), UpdatedAt: formatViewTime(item.UpdatedAt),
		})
	}
	return result
}

func mapSearchResults(items []search.TopicSearchDoc, topicURLMode string) []themecompiler.SearchResultView {
	result := make([]themecompiler.SearchResultView, 0, len(items))
	for _, item := range items {
		result = append(result, themecompiler.SearchResultView{
			Kind: "topic", Title: item.Title, URL: topicPublicPath(item.ID, item.Slug, topicURLMode), Excerpt: item.Excerpt,
		})
	}
	return result
}

func mapForumUser(user *forum.UserSummary, fallbackID int64) themecompiler.PublicUserView {
	if user == nil {
		return themecompiler.PublicUserView{ID: fallbackID}
	}
	return themecompiler.PublicUserView{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.Avatar.URL,
	}
}

func mapCategories(groups []forum.CategoryGroup) []themecompiler.TaxonomyLinkView {
	result := make([]themecompiler.TaxonomyLinkView, 0)
	for _, group := range groups {
		if group.Visibility == "hidden" {
			continue
		}
		for _, category := range group.Categories {
			if category.Visibility == "hidden" {
				continue
			}
			result = append(result, mapCategory(category))
		}
	}
	return result
}

func mapCategory(item forum.Category) themecompiler.TaxonomyLinkView {
	return themecompiler.TaxonomyLinkView{
		ID: item.ID, Name: item.Name, Slug: item.Slug, URL: "/c/" + url.PathEscape(item.Slug),
		Description: item.Description, Count: item.TopicCount,
	}
}

func mapTags(items []forum.Tag) []themecompiler.TaxonomyLinkView {
	result := make([]themecompiler.TaxonomyLinkView, 0, len(items))
	for _, item := range items {
		if item.Status != forum.TagStatusActive {
			continue
		}
		result = append(result, mapTag(item))
	}
	return result
}

func mapTag(item forum.Tag) themecompiler.TaxonomyLinkView {
	return themecompiler.TaxonomyLinkView{
		ID: item.ID, Name: item.Name, Slug: item.Slug, URL: "/tags/" + url.PathEscape(item.Slug),
		Description: item.Description, Count: item.TopicCount,
	}
}

func mapComments(items []forum.Comment) []themecompiler.CommentView {
	result := make([]themecompiler.CommentView, 0, len(items))
	var appendComment func(forum.Comment)
	appendComment = func(item forum.Comment) {
		result = append(result, themecompiler.CommentView{
			ID: item.ID, Author: mapForumUser(item.Author, item.AuthorUserID),
			Body:      themecompiler.NewSafeHTMLFromSanitized(item.Content.HTMLContent),
			CreatedAt: formatViewTime(item.CreatedAt), UpdatedAt: formatViewTime(item.UpdatedAt),
		})
		for _, child := range item.Children {
			appendComment(child)
		}
	}
	for _, item := range items {
		appendComment(item)
	}
	return result
}

func mapPublicProfile(item profile.PublicProfile, topicURLMode string) (themecompiler.PublicUserView, themecompiler.SafeHTML, []themecompiler.TopicSummaryView) {
	user := themecompiler.PublicUserView{
		ID: item.UserID, Username: item.Username, DisplayName: item.DisplayName, AvatarURL: item.Profile.Avatar.URL,
	}
	return user, safePlainHTML(item.Profile.Bio), mapForumTopics(item.RecentTopics, topicURLMode)
}

func mapNotifications(items []notifications.Notification) []themecompiler.NotificationItemView {
	result := make([]themecompiler.NotificationItemView, 0, len(items))
	for _, item := range items {
		var payload struct {
			TopicID int64  `json:"topicId"`
			Title   string `json:"title"`
		}
		_ = json.Unmarshal(item.Payload, &payload)
		title := strings.TrimSpace(payload.Title)
		if title == "" {
			title = item.Type
		}
		targetURL := "/notifications"
		if item.TargetAvailable && strings.HasPrefix(item.TargetPath, "/") {
			targetURL = item.TargetPath
		}
		result = append(result, themecompiler.NotificationItemView{
			ID: item.ID, Kind: item.Type, Title: title, URL: targetURL,
			Read: item.ReadAt != nil, CreatedAt: formatViewTime(item.CreatedAt),
		})
	}
	return result
}

func mapModerationItems(items []moderation.PendingItem) []themecompiler.ModerationQueueItemView {
	result := make([]themecompiler.ModerationQueueItemView, 0, len(items))
	for _, item := range items {
		topicID := item.TopicID
		if item.TargetType == moderation.TargetTypeTopic {
			topicID = item.TargetID
		}
		targetURL := "/moderation"
		if topicID > 0 {
			targetURL = "/t/" + strconv.FormatInt(topicID, 10)
		}
		result = append(result, themecompiler.ModerationQueueItemView{
			ID: item.TargetID, Kind: item.TargetType, Title: item.Title, URL: targetURL,
			Status: "pending", CreatedAt: formatViewTime(item.CreatedAt),
		})
	}
	return result
}

func mapResolvedNavigation(currentPath string, resolved sitechrome.ResolvedNavigation, location string) ([]themecompiler.NavigationItem, bool) {
	for _, resolvedLocation := range resolved.Locations {
		if resolvedLocation.Location != location {
			continue
		}
		items := make([]themecompiler.NavigationItem, 0, len(resolvedLocation.Items))
		for _, item := range resolvedLocation.Items {
			items = append(items, themecompiler.NavigationItem{
				ID: item.SourceKey, Label: item.Label, URL: item.Href,
				Active: navigationItemActive(item.SourceKey, item.Href, currentPath),
			})
		}
		return items, true
	}
	return nil, false
}

func navigationItemActive(sourceKey, href, currentPath string) bool {
	if href != "" && currentPath == href {
		return true
	}
	switch sourceKey {
	case "core.categories":
		return strings.HasPrefix(currentPath, "/categories") || strings.HasPrefix(currentPath, "/c/")
	case "core.tags":
		return strings.HasPrefix(currentPath, "/tags")
	default:
		return false
	}
}

func mapAnnouncements(locale string, items []sitechrome.Announcement) []themecompiler.PageRegion {
	if len(items) == 0 {
		return nil
	}
	region := themecompiler.PageRegion{ID: "forum.region.home.banner"}
	for _, item := range items {
		label, body := item.TitleZhCN, item.BodyZhCN
		if strings.HasPrefix(strings.ToLower(locale), "en") {
			if strings.TrimSpace(item.TitleEnUS) != "" {
				label = item.TitleEnUS
			}
			if strings.TrimSpace(item.BodyEnUS) != "" {
				body = item.BodyEnUS
			}
		}
		region.Items = append(region.Items, themecompiler.PageRegionItem{
			ComponentID: "site.component.announcement", Label: label, Text: body, URL: item.Href,
		})
	}
	return []themecompiler.PageRegion{region}
}

func componentPreviews() []themecompiler.ComponentPreviewView {
	items := componentcatalog.CoreComponentCatalog()
	result := make([]themecompiler.ComponentPreviewView, 0, len(items))
	for _, item := range items {
		result = append(result, themecompiler.ComponentPreviewView{
			ComponentID: item.ID, Label: item.ID, Description: item.ContractVersion,
		})
	}
	return result
}

func topicPublicPath(id int64, slug, mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "id":
		return "/t/" + strconv.FormatInt(id, 10)
	case "slug":
		return "/t/" + url.PathEscape(slug)
	default:
		if strings.TrimSpace(slug) == "" {
			return "/t/" + strconv.FormatInt(id, 10)
		}
		return "/t/" + strconv.FormatInt(id, 10) + "/" + url.PathEscape(slug)
	}
}

func safePlainHTML(value string) themecompiler.SafeHTML {
	return themecompiler.NewSafeHTMLFromSanitized(html.EscapeString(strings.TrimSpace(value)))
}

func formatViewTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func localizedText(locale, zhCN, enUS string) string {
	if strings.HasPrefix(strings.ToLower(locale), "en") {
		return enUS
	}
	return zhCN
}

func localizedLabel(locale string, labels map[string]string, fallback string) string {
	for _, key := range []string{locale, "zh-CN", "en-US"} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return value
		}
	}
	return fallback
}
