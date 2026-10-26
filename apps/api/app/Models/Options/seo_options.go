package options

import (
	"regexp"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const seoContentPrefix = "seo.content_type."

var seoTemplateVariablePattern = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9]*)\}`)

type SEOOptions struct {
	InheritSiteName        bool
	SiteName               string
	EffectiveSiteName      string
	HomeTitle              string
	HomeDescription        string
	HomeKeywords           string
	HomeOGTitle            string
	HomeOGDescription      string
	HomeOGImageURL         string
	PageTitleTemplate      string
	PageDefaultDescription string
	PageTitleSeparator     string
}

type seoContentTypeDefaults struct {
	TitleTemplate     string
	DescriptionSource string
	IndexMode         string
	IncludeInSitemap  bool
	SchemaType        string
	Variables         []string
}

var seoContentDefaults = map[string]seoContentTypeDefaults{
	"category": {"{categoryName} | {seoSiteName}", "category_description,site_default", "index", true, "CollectionPage", []string{"categoryName", "seoSiteName"}},
	"tag":      {"{tagName} | {seoSiteName}", "tag_description,site_default", "index", true, "CollectionPage", []string{"tagName", "seoSiteName"}},
	"topic":    {"{topicTitle} | {seoSiteName}", "topic_summary,topic_excerpt,site_default", "index", true, "DiscussionForumPosting", []string{"topicTitle", "categoryName", "authorName", "seoSiteName"}},
	"profile":  {"{authorName} | {seoSiteName}", "profile_bio,site_default", "noindex", false, "ProfilePage", []string{"authorName", "seoSiteName"}},
	"static":   {"{pageTitle} | {seoSiteName}", "page_description,site_default", "index", true, "WebPage", []string{"pageTitle", "seoSiteName"}},
}

func init() {
	optionDefinitions = append(optionDefinitions, seoOptionDefinitions()...)
}

func seoOptionDefinitions() []optionDefinition {
	names := []string{
		NameSEOSiteInheritSiteName, NameSEOSiteName,
		NameSEOHomeTitle, NameSEOHomeDescription, NameSEOHomeKeywords,
		NameSEOHomeOGTitle, NameSEOHomeOGDescription, NameSEOHomeOGImageURL,
		NameSEOPageTitleTemplate, NameSEOPageDefaultDescription, NameSEOPageTitleSeparator,
	}
	for contentType := range seoContentDefaults {
		for _, suffix := range []string{"title_template", "description_source", "default_image_url", "index_mode", "include_in_sitemap", "schema_type"} {
			names = append(names, seoContentOptionName(contentType, suffix))
		}
	}
	definitions := make([]optionDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, optionDefinition{name: name, public: true, managePermission: identity.PermissionSEOManage})
	}
	return definitions
}

func seoRecommendedDefaults() map[string]string {
	values := map[string]string{
		NameSEOSitemapIncludeForumContent: enabledOptionValue(true),
		NameSEOSiteInheritSiteName:        enabledOptionValue(true),
		NameSEOSiteName:                   "",
		NameSEOHomeTitle:                  "",
		NameSEOHomeDescription:            "",
		NameSEOHomeKeywords:               "",
		NameSEOHomeOGTitle:                "",
		NameSEOHomeOGDescription:          "",
		NameSEOHomeOGImageURL:             "",
		NameSEOPageTitleTemplate:          "{pageTitle} | {seoSiteName}",
		NameSEOPageDefaultDescription:     "",
		NameSEOPageTitleSeparator:         "|",
	}
	for contentType, defaults := range seoContentDefaults {
		values[seoContentOptionName(contentType, "title_template")] = defaults.TitleTemplate
		values[seoContentOptionName(contentType, "description_source")] = defaults.DescriptionSource
		values[seoContentOptionName(contentType, "default_image_url")] = ""
		values[seoContentOptionName(contentType, "index_mode")] = defaults.IndexMode
		values[seoContentOptionName(contentType, "include_in_sitemap")] = enabledOptionValue(defaults.IncludeInSitemap)
		values[seoContentOptionName(contentType, "schema_type")] = defaults.SchemaType
	}
	return values
}

func seoOptionsFromValues(values map[string]string) SEOOptions {
	siteName := strings.TrimSpace(values[NameSiteName])
	if siteName == "" {
		siteName = "SForum"
	}
	inherit := values[NameSEOSiteInheritSiteName] == "" || isEnabledOption(values[NameSEOSiteInheritSiteName])
	seoSiteName := strings.TrimSpace(values[NameSEOSiteName])
	if inherit || seoSiteName == "" {
		seoSiteName = siteName
	}
	homeTitle := strings.TrimSpace(values[NameSEOHomeTitle])
	if homeTitle == "" {
		homeTitle = seoSiteName
	}
	homeDescription := strings.TrimSpace(values[NameSEOHomeDescription])
	if homeDescription == "" {
		homeDescription = seoSiteName
	}
	template := strings.TrimSpace(values[NameSEOPageTitleTemplate])
	if template == "" {
		template = "{pageTitle} | {seoSiteName}"
	}
	separator := strings.TrimSpace(values[NameSEOPageTitleSeparator])
	if separator == "" {
		separator = "|"
	}
	return SEOOptions{
		InheritSiteName: inherit, SiteName: values[NameSEOSiteName], EffectiveSiteName: seoSiteName,
		HomeTitle: homeTitle, HomeDescription: homeDescription, HomeKeywords: values[NameSEOHomeKeywords],
		HomeOGTitle: values[NameSEOHomeOGTitle], HomeOGDescription: values[NameSEOHomeOGDescription], HomeOGImageURL: values[NameSEOHomeOGImageURL],
		PageTitleTemplate: template, PageDefaultDescription: values[NameSEOPageDefaultDescription], PageTitleSeparator: separator,
	}
}

func normalizeSEOOption(name, value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch name {
	case NameSEOSiteInheritSiteName:
		return normalizeEnabledOption(value)
	case NameSEOSiteName, NameSEOHomeTitle, NameSEOHomeOGTitle:
		return normalizeBoundedText(value, seoTitleTemplateMaxRunes)
	case NameSEOHomeDescription, NameSEOHomeOGDescription, NameSEOPageDefaultDescription:
		return normalizeBoundedText(value, seoDescriptionMaxRunes)
	case NameSEOHomeKeywords:
		return normalizeBoundedText(value, seoKeywordsMaxRunes)
	case NameSEOHomeOGImageURL:
		return normalizeOptionalURL(value)
	case NameSEOPageTitleTemplate:
		return normalizeSEOTemplate(value, []string{"pageTitle", "seoSiteName"})
	case NameSEOPageTitleSeparator:
		return normalizeChoice(value, []string{"|", "-", "·"})
	}
	contentType, suffix, ok := parseSEOContentOption(name)
	if !ok {
		return "", false
	}
	defaults := seoContentDefaults[contentType]
	switch suffix {
	case "title_template":
		return normalizeSEOTemplate(value, defaults.Variables)
	case "description_source":
		return normalizeSEODescriptionSources(value, strings.Split(defaults.DescriptionSource, ","))
	case "default_image_url":
		return normalizeOptionalURL(value)
	case "index_mode":
		return normalizeChoice(strings.ToLower(value), []string{"index", "noindex"})
	case "include_in_sitemap":
		return normalizeEnabledOption(value)
	case "schema_type":
		// Schema.org 类型名为 PascalCase；normalizeChoice 会先 ToLower 再精确匹配，
		// 合法默认值（如 CollectionPage）会被误拒。这里用大小写不敏感比较并回写规范名。
		return normalizeStringChoice(value, []string{"CollectionPage", "DiscussionForumPosting", "ProfilePage", "WebPage"})
	default:
		return "", false
	}
}

func normalizeSEOTemplate(value string, allowed []string) (string, bool) {
	value, ok := normalizeBoundedText(value, seoTitleTemplateMaxRunes)
	if !ok || value == "" {
		return value, ok
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, variable := range allowed {
		allowedSet[variable] = true
	}
	for _, match := range seoTemplateVariablePattern.FindAllStringSubmatch(value, -1) {
		if !allowedSet[match[1]] {
			return "", false
		}
	}
	return value, true
}

func normalizeSEODescriptionSources(value string, allowed []string) (string, bool) {
	allowedSet := make(map[string]bool, len(allowed))
	for _, source := range allowed {
		allowedSet[source] = true
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		source := strings.TrimSpace(part)
		if !allowedSet[source] || seen[source] {
			return "", false
		}
		seen[source] = true
		result = append(result, source)
	}
	return strings.Join(result, ","), len(result) > 0
}

func seoContentOptionName(contentType, suffix string) string {
	return seoContentPrefix + contentType + "." + suffix
}

func parseSEOContentOption(name string) (string, string, bool) {
	rest := strings.TrimPrefix(name, seoContentPrefix)
	if rest == name {
		return "", "", false
	}
	parts := strings.SplitN(rest, ".", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	_, ok := seoContentDefaults[parts[0]]
	return parts[0], parts[1], ok
}
