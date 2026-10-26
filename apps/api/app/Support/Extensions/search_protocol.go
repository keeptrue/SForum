package extensionsruntime

import (
	"time"

	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// search.provider known-slot 载荷（Protocol V2 ProviderCall）。
// Host 拥有文档与 ACL；插件仅负责引擎侧 index/delete/search/ensure/probe。

const (
	searchProviderSlot          = search.ProviderSlot
	searchLegacyContractVersion = "1"
	searchOpProbe               = "probe"
	searchOpEnsure              = "ensure"
	searchOpIndex               = "index"
	searchOpDelete              = "delete"
	searchOpSearch              = "search"
)

// SearchEngineProbeResponse 探测结果。
type SearchEngineProbeResponse struct {
	OK      bool
	Reason  string
	Message string
}

// SearchEngineResult 无载荷写操作结果。
type SearchEngineResult struct {
	OK      bool
	Reason  string
	Message string
}

// SearchEngineIndexRequest 写入一篇主题文档。
type SearchEngineIndexRequest struct {
	Document search.TopicSearchDoc
}

// SearchEngineDeleteRequest 按主题 id 删除。
type SearchEngineDeleteRequest struct {
	TopicID int64
}

// SearchEngineSearchRequest / SearchEngineSearchResponse 公开检索。
type SearchEngineSearchRequest struct {
	Query        string
	CategorySlug string
	TagSlug      string
	Page         int
	PerPage      int
}

type SearchEngineSearchResponse struct {
	OK      bool
	Reason  string
	Message string
	Items   []search.TopicSearchDoc
	Total   int64
	Page    int
	PerPage int
}

// topicSearchDocToMap / mapToTopicSearchDoc 在 RPC 边界与 TypedDocument 之间转换。
func topicSearchDocToMap(doc search.TopicSearchDoc) map[string]any {
	tagSlugs := make([]any, 0, len(doc.TagSlugs))
	for _, s := range doc.TagSlugs {
		tagSlugs = append(tagSlugs, s)
	}
	return map[string]any{
		"id":                doc.ID,
		"title":             doc.Title,
		"excerpt":           doc.Excerpt,
		"plainText":         doc.PlainText,
		"categoryId":        doc.CategoryID,
		"categorySlug":      doc.CategorySlug,
		"categoryName":      doc.CategoryName,
		"authorUserId":      doc.AuthorUserID,
		"authorUsername":    doc.AuthorUsername,
		"authorDisplayName": doc.AuthorDisplayName,
		"slug":              doc.Slug,
		"status":            doc.Status,
		"isPinned":          doc.IsPinned,
		"commentCount":      doc.CommentCount,
		"viewCount":         doc.ViewCount,
		"tagSlugs":          tagSlugs,
		"createdAt":         doc.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":         doc.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"lastActivityAt":    doc.LastActivityAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapToTopicSearchDoc(values map[string]any) search.TopicSearchDoc {
	doc := search.TopicSearchDoc{
		ID:                int64Value(values, "id"),
		Title:             stringValue(values, "title"),
		Excerpt:           stringValue(values, "excerpt"),
		PlainText:         stringValue(values, "plainText"),
		CategoryID:        int64Value(values, "categoryId"),
		CategorySlug:      stringValue(values, "categorySlug"),
		CategoryName:      stringValue(values, "categoryName"),
		AuthorUserID:      int64Value(values, "authorUserId"),
		AuthorUsername:    stringValue(values, "authorUsername"),
		AuthorDisplayName: stringValue(values, "authorDisplayName"),
		Slug:              stringValue(values, "slug"),
		Status:            stringValue(values, "status"),
		IsPinned:          booleanValue(values, "isPinned"),
		CommentCount:      int64Value(values, "commentCount"),
		ViewCount:         int64Value(values, "viewCount"),
	}
	for _, raw := range anySliceValue(values, "tagSlugs") {
		if s, ok := raw.(string); ok && s != "" {
			doc.TagSlugs = append(doc.TagSlugs, s)
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, stringValue(values, "createdAt")); err == nil {
		doc.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, stringValue(values, "updatedAt")); err == nil {
		doc.UpdatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, stringValue(values, "lastActivityAt")); err == nil {
		doc.LastActivityAt = t
	}
	return doc
}

func anySliceValue(values map[string]any, key string) []any {
	if values == nil {
		return nil
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		return v
	default:
		return nil
	}
}
