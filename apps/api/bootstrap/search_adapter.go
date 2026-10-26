package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	forumcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Forum"
	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// forumSearchReader 把 forum.Service 适配为 search.TopicReader。
// 实现 forum.TopicDetail → search.TopicSearchDoc 的转换，隔离两包的数据结构。
type forumSearchReader struct {
	forum *forum.Service
}

func (r forumSearchReader) GetTopicForSearch(ctx context.Context, topicID int64) (search.TopicSearchDoc, error) {
	detail, err := r.forum.GetTopicForSearch(ctx, topicID)
	if err != nil {
		return search.TopicSearchDoc{}, err
	}
	doc := search.TopicSearchDoc{
		ID:             detail.ID,
		Title:          detail.Title,
		Excerpt:        detail.Excerpt,
		PlainText:      detail.Content.PlainText,
		CategoryID:     detail.CategoryID,
		CategorySlug:   detail.CategorySlug,
		CategoryName:   detail.CategoryName,
		AuthorUserID:   detail.AuthorUserID,
		Slug:           detail.Slug,
		Status:         detail.Status,
		IsPinned:       detail.IsPinned,
		CommentCount:   detail.CommentCount,
		ViewCount:      detail.ViewCount,
		CreatedAt:      detail.CreatedAt,
		UpdatedAt:      detail.UpdatedAt,
		LastActivityAt: detail.LastActivityAt,
	}
	if detail.Author != nil {
		doc.AuthorUsername = detail.Author.Username
		doc.AuthorDisplayName = detail.Author.DisplayName
	}
	for _, tag := range detail.Tags {
		doc.TagSlugs = append(doc.TagSlugs, tag.Slug)
	}
	return doc, nil
}

type forumSearchHitStore interface {
	ListPublicTopicSearchHits(context.Context, []int64) (map[int64]forum.TopicSummary, error)
}

// forumLiveSearchSource 用 Forum 摘要读取权威校验搜索引擎命中，剔除幽灵主题。
type forumLiveSearchSource struct {
	store forumSearchHitStore
}

func (s forumLiveSearchSource) ListPublicByIDs(ctx context.Context, ids []int64) (map[int64]search.TopicSearchDoc, error) {
	if s.store == nil {
		return map[int64]search.TopicSearchDoc{}, nil
	}
	hits, err := s.store.ListPublicTopicSearchHits(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]search.TopicSearchDoc, len(hits))
	for id, topic := range hits {
		out[id] = topicSearchDocFromSummary(topic)
	}
	return out, nil
}

// forumLiveSearchBatch 是一次 HTTP 搜索的短生命周期桥接器。它同时服务 live
// 校验和最终序列化，避免同一命中集执行两次 summaries/tags 查询。
type forumLiveSearchBatch struct {
	store forumSearchHitStore
	hits  map[int64]forum.TopicSummary
}

func (b *forumLiveSearchBatch) ListPublicByIDs(ctx context.Context, ids []int64) (map[int64]search.TopicSearchDoc, error) {
	if b == nil || b.store == nil {
		return map[int64]search.TopicSearchDoc{}, nil
	}
	hits, err := b.store.ListPublicTopicSearchHits(ctx, ids)
	if err != nil {
		return nil, err
	}
	b.hits = hits
	out := make(map[int64]search.TopicSearchDoc, len(hits))
	for id, topic := range hits {
		out[id] = topicSearchDocFromSummary(topic)
	}
	return out, nil
}

func (b *forumLiveSearchBatch) orderedSummaries(items []search.TopicSearchDoc) []forum.TopicSummary {
	out := make([]forum.TopicSummary, 0, len(items))
	for _, item := range items {
		if topic, ok := b.hits[item.ID]; ok {
			out = append(out, topic)
		}
	}
	return out
}

func topicSearchDocFromSummary(topic forum.TopicSummary) search.TopicSearchDoc {
	doc := search.TopicSearchDoc{
		ID:             topic.ID,
		Title:          topic.Title,
		Excerpt:        topic.Excerpt,
		CategoryID:     topic.CategoryID,
		CategorySlug:   topic.CategorySlug,
		CategoryName:   topic.CategoryName,
		AuthorUserID:   topic.AuthorUserID,
		Slug:           topic.Slug,
		Status:         topic.Status,
		IsPinned:       topic.IsPinned,
		CommentCount:   topic.CommentCount,
		ViewCount:      topic.ViewCount,
		CreatedAt:      topic.CreatedAt,
		UpdatedAt:      topic.UpdatedAt,
		LastActivityAt: topic.LastActivityAt,
	}
	if topic.Author != nil {
		doc.AuthorUsername = topic.Author.Username
		doc.AuthorDisplayName = topic.Author.DisplayName
	}
	for _, tag := range topic.Tags {
		doc.TagSlugs = append(doc.TagSlugs, tag.Slug)
	}
	return doc
}

// searchServiceAdapter 把 search.Service 适配为 forumcontroller.SearchService。
// 请求私有 live batch 保留引擎排序后的 Forum 摘要，以输出与 GET /topics 同构的行。
type searchServiceAdapter struct {
	inner *search.Service
	store forumSearchHitStore
}

// reindexServiceAdapter 把 search.ReindexManager 适配为 forumcontroller.ReindexService，
// 完成 search.ReindexRun/ReindexStatusOutput → controller 类型的映射与错误转换。
type reindexServiceAdapter struct {
	inner *search.ReindexManager
}

func (a reindexServiceAdapter) Reindex(ctx context.Context, startedByUserID int64) (forumcontroller.ReindexRunOutput, error) {
	if a.inner == nil {
		return forumcontroller.ReindexRunOutput{}, nil
	}
	run, err := a.inner.Reindex(ctx, startedByUserID)
	if err != nil {
		return forumcontroller.ReindexRunOutput{}, mapReindexAdapterError(err)
	}
	return toReindexRunOutput(run), nil
}

func (a reindexServiceAdapter) ReindexStatus(ctx context.Context) (forumcontroller.ReindexStatusOutput, error) {
	if a.inner == nil {
		return forumcontroller.ReindexStatusOutput{}, nil
	}
	status, err := a.inner.ReindexStatus(ctx)
	if err != nil {
		return forumcontroller.ReindexStatusOutput{}, mapReindexAdapterError(err)
	}
	return forumcontroller.ReindexStatusOutput{
		ReindexRunOutput: toReindexRunOutput(status.ReindexRun),
		Processed:        status.Processed,
		Remaining:        status.Remaining,
		Percent:          status.Percent,
	}, nil
}

func (a reindexServiceAdapter) ListReindexRuns(ctx context.Context) ([]forumcontroller.ReindexRunOutput, error) {
	if a.inner == nil {
		return nil, nil
	}
	runs, err := a.inner.ListReindexRuns(ctx)
	if err != nil {
		return nil, mapReindexAdapterError(err)
	}
	out := make([]forumcontroller.ReindexRunOutput, 0, len(runs))
	for _, run := range runs {
		out = append(out, toReindexRunOutput(run))
	}
	return out, nil
}

// toReindexRunOutput 将 search.ReindexRun 转换为 controller 镜像类型。
func toReindexRunOutput(run search.ReindexRun) forumcontroller.ReindexRunOutput {
	return forumcontroller.ReindexRunOutput{
		ID:              run.ID,
		Total:           run.Total,
		Status:          run.Status,
		StartedAt:       run.StartedAt,
		FinishedAt:      run.FinishedAt,
		StartedByUserID: run.StartedByUserID,
		Error:           run.Error,
	}
}

// mapReindexAdapterError 把 search 包的 sentinel error 转换为 controller 包的 sentinel error，
// 使 controller.mapReindexError 能正确识别并映射 HTTP code。
func mapReindexAdapterError(err error) error {
	switch {
	case errors.Is(err, search.ErrReindexAlreadyRunning):
		return forumcontroller.ErrReindexRunning
	case errors.Is(err, search.ErrNoReindexRun):
		return forumcontroller.ErrReindexNoRun
	default:
		return err
	}
}

func (a searchServiceAdapter) Search(ctx context.Context, input forumcontroller.SearchInput) (forumcontroller.SearchOutput, error) {
	if a.inner == nil {
		return forumcontroller.SearchOutput{}, fmt.Errorf("search service unavailable")
	}
	searchInput := search.SearchInput{
		Query:        input.Query,
		CategorySlug: input.CategorySlug,
		TagSlug:      input.TagSlug,
		Page:         input.Page,
		PerPage:      input.PerPage,
	}
	var res search.SearchResult
	var items []forum.TopicSummary
	var err error
	if a.store != nil {
		batch := &forumLiveSearchBatch{store: a.store}
		res, err = a.inner.SearchWithLiveSource(ctx, searchInput, batch)
		items = batch.orderedSummaries(res.Items)
	} else {
		res, err = a.inner.Search(ctx, searchInput)
	}
	if err != nil {
		if errors.Is(err, search.ErrEngineUnavailable) {
			return forumcontroller.SearchOutput{}, forumcontroller.ErrSearchUnavailable
		}
		return forumcontroller.SearchOutput{}, err
	}
	if a.store == nil {
		// 无 store 时回退扁平文档（测试/降级）；生产路径始终注入 store。
		items = make([]forum.TopicSummary, 0, len(res.Items))
		for _, doc := range res.Items {
			items = append(items, forum.TopicSummary{
				ID:           doc.ID,
				CategoryID:   doc.CategoryID,
				CategorySlug: doc.CategorySlug,
				CategoryName: doc.CategoryName,
				AuthorUserID: doc.AuthorUserID,
				Author: &forum.UserSummary{
					ID:          doc.AuthorUserID,
					Username:    doc.AuthorUsername,
					DisplayName: doc.AuthorDisplayName,
				},
				Title:          doc.Title,
				Slug:           doc.Slug,
				Status:         doc.Status,
				IsPinned:       doc.IsPinned,
				CommentCount:   doc.CommentCount,
				ViewCount:      doc.ViewCount,
				Excerpt:        doc.Excerpt,
				CreatedAt:      doc.CreatedAt,
				UpdatedAt:      doc.UpdatedAt,
				LastActivityAt: doc.LastActivityAt,
			})
		}
	}
	return forumcontroller.SearchOutput{
		Items:   items,
		Total:   res.Total,
		Page:    res.Page,
		PerPage: res.PerPage,
	}, nil
}

// searchProviderAdminAdapter 把 SearchProviderRegistry 适配为 forum controller 运营接口。
type searchProviderAdminAdapter struct {
	registry *extensionsruntime.SearchProviderRegistry
}

func (a searchProviderAdminAdapter) List(ctx context.Context) (forumcontroller.SearchProvidersState, error) {
	candidates, err := a.registry.Candidates(ctx)
	if err != nil {
		return forumcontroller.SearchProvidersState{}, err
	}
	items := make([]forumcontroller.SearchProviderItem, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, forumcontroller.SearchProviderItem{
			ExtensionID: c.ExtensionID,
			Label:       c.Label,
			Healthy:     c.Healthy,
			IsDefault:   c.IsDefault,
		})
	}
	selected, _, err := a.registry.Selected(ctx)
	if err != nil {
		return forumcontroller.SearchProvidersState{}, err
	}
	pinned, err := a.registry.IsPinned(ctx)
	if err != nil {
		return forumcontroller.SearchProvidersState{}, err
	}
	// 用候选表补全 selected 的 healthy/isDefault。
	selItem := forumcontroller.SearchProviderItem{
		ExtensionID: selected.ExtensionID,
		Label:       selected.Label,
		Healthy:     true,
		IsDefault:   extensionsruntime.IsSiteSearchProvider(selected.ExtensionID),
	}
	for _, item := range items {
		if item.ExtensionID == selected.ExtensionID {
			selItem = item
			break
		}
	}
	return forumcontroller.SearchProvidersState{
		Items:              items,
		Selected:           selItem,
		Pinned:             pinned,
		DefaultExtensionID: extensionsruntime.DefaultSiteSearchExtensionID,
	}, nil
}

func (a searchProviderAdminAdapter) Select(ctx context.Context, extensionID string) error {
	return a.registry.Select(ctx, extensionID)
}

func (a searchProviderAdminAdapter) RestoreDefault(ctx context.Context) error {
	return a.registry.RestoreDefault(ctx)
}

// registerSearchWorkers 在 worker 进程注册搜索索引/删除 worker。
// 默认站内引擎始终可用；外部引擎经 search.provider 解析。
func registerSearchWorkers(registry *supportjobs.Registry, pool *pgxpool.Pool, searchStore extensionsruntime.SearchProviderStore, extensionRuntime extensionsruntime.SearchRuntime, logger *slog.Logger) *search.Reconciler {
	searchProviders := extensionsruntime.NewSearchProviderRegistry(searchStore)
	siteEngine := search.NewPostgresSiteEngine(pool)
	searchEngine := extensionsruntime.NewResolvingSearchEngine(searchProviders, extensionRuntime, siteEngine)
	// EnsureIndex 失败只告警：migration 未就绪时 worker 仍启动，任务可重试。
	if err := searchEngine.EnsureIndex(context.Background()); err != nil && !errors.Is(err, search.ErrEngineUnavailable) {
		slog.Warn("search: ensure index failed (worker will still start)", "err", err)
	}
	forumService := forum.NewService(forum.ServiceConfig{Store: forum.NewPostgresStore(pool)})
	stateStore := search.NewPostgresIndexStateStore(pool)
	indexer := search.NewIndexer(searchEngine, forumSearchReader{forum: forumService}, nil).
		WithIndexStateStore(stateStore)
	searchjobs.Register(registry, indexer)
	reconciler := search.NewReconciler(searchEngine, stateStore, nil).WithLogger(logger)
	searchjobs.RegisterReconcile(registry, reconciler)
	return reconciler
}
