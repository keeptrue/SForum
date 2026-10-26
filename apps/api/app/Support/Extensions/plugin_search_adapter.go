package extensionsruntime

import (
	"context"
	"fmt"
	"strings"

	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// SearchRuntime 是插件搜索引擎 RPC 入口（Manager / ProtocolStarter）。
type SearchRuntime interface {
	SearchEngineProbe(ctx context.Context, extensionID string, request SearchEngineProbeRequest) (SearchEngineProbeResponse, error)
	SearchEngineEnsure(ctx context.Context, extensionID string) (SearchEngineResult, error)
	SearchEngineIndex(ctx context.Context, extensionID string, request SearchEngineIndexRequest) (SearchEngineResult, error)
	SearchEngineDelete(ctx context.Context, extensionID string, request SearchEngineDeleteRequest) (SearchEngineResult, error)
	SearchEngineSearch(ctx context.Context, extensionID string, request SearchEngineSearchRequest) (SearchEngineSearchResponse, error)
}

// PluginSearchEngine 将 search.Engine 翻译为扩展 runtime 的 ProviderCall。
type PluginSearchEngine struct {
	extensionID string
	runtime     SearchRuntime
}

// NewPluginSearchEngine 构造插件搜索引擎适配器。
func NewPluginSearchEngine(extensionID string, runtime SearchRuntime) (*PluginSearchEngine, error) {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" || runtime == nil {
		return nil, search.ErrEngineUnavailable
	}
	return &PluginSearchEngine{extensionID: extensionID, runtime: runtime}, nil
}

var _ search.Engine = (*PluginSearchEngine)(nil)

func (e *PluginSearchEngine) Probe(ctx context.Context) error {
	resp, err := e.runtime.SearchEngineProbe(ctx, e.extensionID, SearchEngineProbeRequest{})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%w: %s", search.ErrEngineUnavailable, firstNonEmpty(resp.Message, resp.Reason))
	}
	return nil
}

func (e *PluginSearchEngine) EnsureIndex(ctx context.Context) error {
	resp, err := e.runtime.SearchEngineEnsure(ctx, e.extensionID)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("search ensure: %s", firstNonEmpty(resp.Message, resp.Reason))
	}
	return nil
}

func (e *PluginSearchEngine) Index(ctx context.Context, doc search.TopicSearchDoc) error {
	resp, err := e.runtime.SearchEngineIndex(ctx, e.extensionID, SearchEngineIndexRequest{Document: doc})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("search index: %s", firstNonEmpty(resp.Message, resp.Reason))
	}
	return nil
}

func (e *PluginSearchEngine) Delete(ctx context.Context, topicID int64) error {
	resp, err := e.runtime.SearchEngineDelete(ctx, e.extensionID, SearchEngineDeleteRequest{TopicID: topicID})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("search delete: %s", firstNonEmpty(resp.Message, resp.Reason))
	}
	return nil
}

func (e *PluginSearchEngine) Search(ctx context.Context, input search.SearchInput) (search.SearchResult, error) {
	resp, err := e.runtime.SearchEngineSearch(ctx, e.extensionID, SearchEngineSearchRequest{
		Query: input.Query, CategorySlug: input.CategorySlug, TagSlug: input.TagSlug,
		Page: input.Page, PerPage: input.PerPage,
	})
	if err != nil {
		return search.SearchResult{}, err
	}
	if !resp.OK {
		if strings.Contains(resp.Reason, "unavailable") || strings.Contains(resp.Reason, "not_configured") {
			return search.SearchResult{}, search.ErrEngineUnavailable
		}
		return search.SearchResult{}, fmt.Errorf("search query: %s", firstNonEmpty(resp.Message, resp.Reason))
	}
	// Host ACL：不信任插件返回的非公开状态。
	items := make([]search.TopicSearchDoc, 0, len(resp.Items))
	for _, doc := range resp.Items {
		if !search.IsPublicSearchStatus(doc.Status) {
			continue
		}
		doc.PlainText = ""
		items = append(items, doc)
	}
	return search.SearchResult{
		Items: items, Total: resp.Total, Page: resp.Page, PerPage: resp.PerPage,
	}, nil
}

// ResolvingSearchEngine 按运营选择解析 search.provider。
// 站内默认（sforum.search-site）短路到进程内 site 引擎；外部插件走 RPC。
type ResolvingSearchEngine struct {
	registry *SearchProviderRegistry
	runtime  SearchRuntime
	site     search.Engine
}

// NewResolvingSearchEngine 构造解析引擎。site 为站内引擎（必填生产路径）；nil 时站内回落 Unavailable。
func NewResolvingSearchEngine(registry *SearchProviderRegistry, runtime SearchRuntime, site search.Engine) *ResolvingSearchEngine {
	if site == nil {
		site = search.UnavailableEngine{}
	}
	return &ResolvingSearchEngine{registry: registry, runtime: runtime, site: site}
}

var _ search.Engine = (*ResolvingSearchEngine)(nil)

// Enabled 站内默认始终可用；外部 pin 时只要 Selected 成功即 true。
func (e *ResolvingSearchEngine) Enabled(ctx context.Context) bool {
	if e == nil {
		return false
	}
	_, ok, err := e.SelectedID(ctx)
	return err == nil && ok
}

// SelectedID 返回当前解析到的扩展 id。
func (e *ResolvingSearchEngine) SelectedID(ctx context.Context) (string, bool, error) {
	if e == nil {
		return "", false, nil
	}
	if e.registry == nil {
		return DefaultSiteSearchExtensionID, true, nil
	}
	sel, ok, err := e.registry.Selected(ctx)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return DefaultSiteSearchExtensionID, true, nil
	}
	return sel.ExtensionID, true, nil
}

func (e *ResolvingSearchEngine) resolve(ctx context.Context) (search.Engine, error) {
	if e == nil {
		return nil, search.ErrEngineUnavailable
	}
	id, ok, err := e.SelectedID(ctx)
	if err != nil {
		return nil, err
	}
	if !ok || IsSiteSearchProvider(id) {
		if e.site == nil {
			return nil, search.ErrEngineUnavailable
		}
		return e.site, nil
	}
	if e.runtime == nil {
		return nil, search.ErrEngineUnavailable
	}
	return NewPluginSearchEngine(id, e.runtime)
}

func (e *ResolvingSearchEngine) Probe(ctx context.Context) error {
	engine, err := e.resolve(ctx)
	if err != nil {
		return err
	}
	return engine.Probe(ctx)
}

func (e *ResolvingSearchEngine) EnsureIndex(ctx context.Context) error {
	engine, err := e.resolve(ctx)
	if err != nil {
		return err
	}
	return engine.EnsureIndex(ctx)
}

func (e *ResolvingSearchEngine) Index(ctx context.Context, doc search.TopicSearchDoc) error {
	engine, err := e.resolve(ctx)
	if err != nil {
		return err
	}
	return engine.Index(ctx, doc)
}

func (e *ResolvingSearchEngine) Delete(ctx context.Context, topicID int64) error {
	engine, err := e.resolve(ctx)
	if err != nil {
		return err
	}
	return engine.Delete(ctx, topicID)
}

func (e *ResolvingSearchEngine) Search(ctx context.Context, input search.SearchInput) (search.SearchResult, error) {
	engine, err := e.resolve(ctx)
	if err != nil {
		return search.SearchResult{}, err
	}
	return engine.Search(ctx, input)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "search engine error"
}
