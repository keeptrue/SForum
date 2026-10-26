package forum

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type Service struct {
	store             Store
	settings          SettingsResolver
	events            appevents.Publisher
	topicActions      TopicExtensionActionProvider
	commentActions    CommentExtensionActionProvider
	topicSurfaces     TopicExtensionSurfaceProvider
	composerToolbar   ComposerToolbarProvider
	publicationPolicy PublicationPolicy
	// indexer 触发搜索引擎索引调度；nil 表示不索引（搜索为派生数据，可重建）。
	indexer TopicSearchIndexer
	// trust 可选新人信任阶梯；nil 时不叠加新人限制。
	trust TrustPolicyResolver
	// contentFilter 可选：Host Render 之后的 ContentRegistry 后置缝合；nil 为恒等。
	contentFilter ContentPostFilter
	// editorSchema 可选：editor-document Accept 合并 Editor Registry 节点/标记。
	editorSchema EditorDocumentSchemaProvider
	// views 可选：公开详情浏览计数（Redis INCR + 去重）；nil 时不计。
	views TopicViewRecorder
	// topicEventLinks resolves live operator URL settings for topic observe payloads.
	topicEventLinks TopicEventLinkResolver
}

// ServiceConfig 集中声明 Forum 服务依赖，避免随能力增加派生新的构造器排列组合。
// 可选依赖为 nil 时保持对应能力关闭；Settings 和 Publisher 会使用安全默认值。
type ServiceConfig struct {
	Store             Store
	Settings          SettingsResolver
	Publisher         appevents.Publisher
	Indexer           TopicSearchIndexer
	TopicActions      TopicExtensionActionProvider
	CommentActions    CommentExtensionActionProvider
	TopicSurfaces     TopicExtensionSurfaceProvider
	ComposerToolbar   ComposerToolbarProvider
	PublicationPolicy PublicationPolicy
	TrustPolicy       TrustPolicyResolver
	ContentPostFilter ContentPostFilter
	EditorSchema      EditorDocumentSchemaProvider
	ViewRecorder      TopicViewRecorder
	TopicEventLinks   TopicEventLinkResolver
}

// WithComposerToolbar 注入 composer 工具栏贡献解析（F4.3）。
func (s *Service) WithComposerToolbar(provider ComposerToolbarProvider) *Service {
	if s != nil {
		s.composerToolbar = provider
	}
	return s
}

// WithTopicExtensionSurfaces 注入主题详情 sidebar/badges 贡献解析（E2.1）。
func (s *Service) WithTopicExtensionSurfaces(provider TopicExtensionSurfaceProvider) *Service {
	if s != nil {
		s.topicSurfaces = provider
	}
	return s
}

// WithCommentExtensionActions 注入评论行扩展动作解析（E2.2）。
func (s *Service) WithCommentExtensionActions(provider CommentExtensionActionProvider) *Service {
	if s != nil {
		s.commentActions = provider
	}
	return s
}

// ListComposerToolbarActions 返回已启用插件的 composer 工具栏动作。
func (s *Service) ListComposerToolbarActions(ctx context.Context) ([]ComposerToolbarAction, error) {
	if s == nil || s.composerToolbar == nil {
		return nil, nil
	}
	return s.composerToolbar.ComposerToolbarActions(ctx)
}

// WithTrustPolicy 注入新人信任策略（options 适配）。链式可选。
func (s *Service) WithTrustPolicy(trust TrustPolicyResolver) *Service {
	s.trust = trust
	return s
}

// WithViewRecorder 注入公开详情浏览计数器（D3 / Iteration A WS1）。
// nil 或 Redis 故障时详情仍 200，仅跳过计数。
func (s *Service) WithViewRecorder(recorder TopicViewRecorder) *Service {
	if s != nil {
		s.views = recorder
	}
	return s
}

// RecordTopicView 在公开详情 GET 成功后调用：30m 去重 + Redis 增量。
// 不写入 PG；刷盘由 forum.flush_view_counts。失败静默。
func (s *Service) RecordTopicView(ctx context.Context, topicID int64, visitorKey string) {
	if s == nil || s.views == nil || topicID <= 0 {
		return
	}
	s.views.RecordView(ctx, topicID, visitorKey)
}

func NewService(config ServiceConfig) *Service {
	if config.Settings == nil {
		config.Settings = staticSettingsResolver{}
	}
	return &Service{
		store:             config.Store,
		settings:          config.Settings,
		events:            appevents.EnsurePublisher(config.Publisher),
		indexer:           config.Indexer,
		topicActions:      config.TopicActions,
		commentActions:    config.CommentActions,
		topicSurfaces:     config.TopicSurfaces,
		composerToolbar:   config.ComposerToolbar,
		publicationPolicy: config.PublicationPolicy,
		trust:             config.TrustPolicy,
		contentFilter:     config.ContentPostFilter,
		editorSchema:      config.EditorSchema,
		views:             config.ViewRecorder,
		topicEventLinks:   config.TopicEventLinks,
	}
}

func (s *Service) publicationDecision(ctx context.Context, actorUserID int64, rawContent string) (PublicationDecision, error) {
	if s.publicationPolicy == nil {
		return PublicationDecision{}, nil
	}
	return s.publicationPolicy.EvaluatePublication(ctx, PublicationInput{ActorUserID: actorUserID, RawContent: rawContent})
}

func (s *Service) ListAuthorReviewItems(ctx context.Context, actor identity.Actor) (AuthorReviewList, error) {
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return AuthorReviewList{}, identity.ErrPermissionDenied
	}
	store, ok := s.store.(AuthorReviewStore)
	if !ok {
		return AuthorReviewList{}, ErrInvalidAction
	}
	list, err := store.ListAuthorReviewItems(ctx, actor.ID)
	if err != nil {
		return AuthorReviewList{}, err
	}
	limit := RecommendedExcerptRuneLimit
	if settings, settingsErr := s.resolvedSettings(ctx); settingsErr == nil {
		limit = settings.ExcerptRuneLimit
	}
	for i := range list.Items {
		list.Items[i].Excerpt = ExcerptFromPlain(list.Items[i].Excerpt, limit)
	}
	return list, nil
}

// indexTopic 在主题写流程成功后触发搜索引擎索引调度。
// 失败只记日志不中断主流程：搜索是可从 PG 重建的派生数据。
func (s *Service) indexTopic(ctx context.Context, topicID int64) {
	if s.indexer == nil || topicID <= 0 {
		return
	}
	if err := s.indexer.EnqueueIndex(ctx, topicID); err != nil {
		slog.ErrorContext(ctx, "forum: enqueue topic index failed", "topicId", topicID, "err", err)
	}
}

// deleteTopicIndex 在主题删除/隐藏后触发搜索引擎删除调度。
func (s *Service) deleteTopicIndex(ctx context.Context, topicID int64) {
	if s.indexer == nil || topicID <= 0 {
		return
	}
	if err := s.indexer.EnqueueDelete(ctx, topicID); err != nil {
		slog.ErrorContext(ctx, "forum: enqueue topic index delete failed", "topicId", topicID, "err", err)
	}
}

type staticSettingsResolver struct{}

func normalizeContentAttachmentIDs(input *[]int64) ([]int64, bool, error) {
	if input == nil {
		return nil, false, nil
	}
	if len(*input) > 100 {
		return nil, true, ErrInvalidContent
	}
	seen := make(map[int64]struct{}, len(*input))
	ids := make([]int64, 0, len(*input))
	for _, id := range *input {
		if id <= 0 {
			return nil, true, ErrInvalidContent
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true, nil
}

func (staticSettingsResolver) ForumSettings(context.Context) (ForumSettings, error) {
	return defaultForumSettings(), nil
}

func (s *Service) ListCategories(ctx context.Context, includeHidden bool) ([]Category, error) {
	return s.store.ListCategories(ctx, includeHidden)
}

func (s *Service) ListCategoryGroups(ctx context.Context, includeHidden bool) ([]CategoryGroup, error) {
	return s.store.ListCategoryGroups(ctx, includeHidden)
}

func (s *Service) ListTags(ctx context.Context, includePending bool) ([]Tag, error) {
	return s.store.ListTags(ctx, includePending)
}

func (s *Service) CreateCategoryGroup(ctx context.Context, actor identity.Actor, input CreateCategoryGroupInput) (CategoryGroup, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return CategoryGroup{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeCreateCategoryGroupInput(input)
	if err != nil {
		return CategoryGroup{}, err
	}
	created, err := s.store.CreateCategoryGroup(ctx, normalized)
	if err != nil {
		return CategoryGroup{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryCreated, map[string]any{
		"categoryGroupId": created.ID,
		"categorySlug":    created.Slug,
	}))
	return created, nil
}

func (s *Service) UpdateCategoryGroup(ctx context.Context, actor identity.Actor, input UpdateCategoryGroupInput) (CategoryGroup, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return CategoryGroup{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeUpdateCategoryGroupInput(input)
	if err != nil {
		return CategoryGroup{}, err
	}
	updated, err := s.store.UpdateCategoryGroup(ctx, normalized)
	if err != nil {
		return CategoryGroup{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryUpdated, map[string]any{
		"categoryGroupId": updated.ID,
		"categorySlug":    updated.Slug,
	}))
	return updated, nil
}

func (s *Service) CreateCategory(ctx context.Context, actor identity.Actor, input CreateCategoryInput) (Category, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return Category{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeCreateCategoryInput(input)
	if err != nil {
		return Category{}, err
	}
	created, err := s.store.CreateCategory(ctx, normalized)
	if err != nil {
		return Category{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryCreated, map[string]any{
		"categoryId":   created.ID,
		"categorySlug": created.Slug,
		"groupId":      created.GroupID,
	}))
	return created, nil
}

func (s *Service) UpdateCategory(ctx context.Context, actor identity.Actor, input UpdateCategoryInput) (Category, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return Category{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeUpdateCategoryInput(input)
	if err != nil {
		return Category{}, err
	}
	updated, err := s.store.UpdateCategory(ctx, normalized)
	if err != nil {
		return Category{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryUpdated, map[string]any{
		"categoryId":   updated.ID,
		"categorySlug": updated.Slug,
		"groupId":      updated.GroupID,
	}))
	return updated, nil
}

func (s *Service) CreateTag(ctx context.Context, actor identity.Actor, input CreateTagInput) (Tag, error) {
	if !actor.Can(identity.PermissionTagManage) {
		return Tag{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeCreateTagInput(input)
	if err != nil {
		return Tag{}, err
	}
	normalized.ActorUserID = actor.ID
	created, err := s.store.CreateTag(ctx, normalized)
	if err != nil {
		return Tag{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.TagCreated, map[string]any{
		"tagId":   created.ID,
		"tagSlug": created.Slug,
		"status":  created.Status,
	}))
	return created, nil
}

func (s *Service) UpdateTag(ctx context.Context, actor identity.Actor, input UpdateTagInput) (Tag, error) {
	if !actor.Can(identity.PermissionTagManage) {
		return Tag{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeUpdateTagInput(input)
	if err != nil {
		return Tag{}, err
	}
	normalized.ActorUserID = actor.ID
	updated, err := s.store.UpdateTag(ctx, normalized)
	if err != nil {
		return Tag{}, err
	}
	s.events.Emit(ctx, appevents.NewEnvelope(appevents.TagUpdated, map[string]any{
		"tagId":   updated.ID,
		"tagSlug": updated.Slug,
		"status":  updated.Status,
	}))
	return updated, nil
}

func (s *Service) ForumSettings(ctx context.Context, actor identity.Actor) (ForumSettings, error) {
	if !canManageForumSettings(actor) {
		return ForumSettings{}, identity.ErrPermissionDenied
	}
	return s.resolvedSettings(ctx)
}

// PublicForumSettings 返回站点论坛策略（含游客阅读等），无需管理权限。
// 供公开路由守卫读取；不暴露密钥，仅业务策略。
func (s *Service) PublicForumSettings(ctx context.Context) (ForumSettings, error) {
	return s.resolvedSettings(ctx)
}

func (s *Service) UpdateForumSettings(ctx context.Context, actor identity.Actor, input UpdateForumSettingsInput) (ForumSettings, error) {
	if input.DefaultCategorySlug != nil && !actor.Can(identity.PermissionCategoryManage) {
		return ForumSettings{}, identity.ErrPermissionDenied
	}
	if (input.TagCreationMode != nil || input.TagPublicPages != nil || input.TagMinPerTopic != nil || input.TagMaxPerTopic != nil) && !actor.Can(identity.PermissionTagManage) {
		return ForumSettings{}, identity.ErrPermissionDenied
	}
	if forumSettingsManageFieldsPresent(input) && !actor.Can(identity.PermissionForumSettingsManage) {
		return ForumSettings{}, identity.ErrPermissionDenied
	}
	manager, ok := s.settings.(SettingsManager)
	if !ok {
		return ForumSettings{}, ErrInvalidSettings
	}
	normalized, err := normalizeUpdateForumSettingsInput(input)
	if err != nil {
		return ForumSettings{}, err
	}
	return manager.UpdateForumSettings(ctx, actor, normalized)
}

// forumSettingsManageFieldsPresent 分页/字数/节奏等需 settings.manage。
func forumSettingsManageFieldsPresent(input UpdateForumSettingsInput) bool {
	return input.TopicsPerPage != nil ||
		input.CommentsPerPage != nil ||
		input.TopicTitleMinRunes != nil ||
		input.TopicTitleMaxRunes != nil ||
		input.TopicContentMinRunes != nil ||
		input.TopicContentMaxRunes != nil ||
		input.TopicEditWindowMinutes != nil ||
		input.TopicCooldownSeconds != nil ||
		input.DailyTopicLimit != nil ||
		input.CommentMinRunes != nil ||
		input.CommentMaxRunes != nil ||
		input.CommentMaxNestingDepth != nil ||
		input.TreeDescendantsPerRoot != nil ||
		input.CommentEditWindowMinutes != nil ||
		input.CommentCooldownSeconds != nil ||
		input.DailyCommentLimit != nil ||
		input.ExcerptRuneLimit != nil
}

func (s *Service) ResetForumSettings(ctx context.Context, actor identity.Actor) (ForumSettings, error) {
	if !canManageForumSettings(actor) {
		return ForumSettings{}, identity.ErrPermissionDenied
	}
	manager, ok := s.settings.(SettingsManager)
	if !ok {
		return ForumSettings{}, ErrInvalidSettings
	}
	return manager.ResetForumSettings(ctx, actor)
}

func (s *Service) ListTopics(ctx context.Context, input TopicListInput) (TopicList, error) {
	// 关键词检索已迁移到专用搜索端点（GET /api/v1/search）。
	// 旧 ILIKE 全表扫描在千万级数据下不可接受；这里明确拒绝并引导调用方。
	if strings.TrimSpace(input.Query) != "" {
		return TopicList{}, ErrUseSearchEndpoint
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return TopicList{}, err
	}
	defaultPerPage := settings.TopicsPerPage
	if defaultPerPage <= 0 {
		defaultPerPage = 20
	}
	input.Page, input.PerPage = normalizePageWithDefault(input.Page, input.PerPage, defaultPerPage)
	// sort 解析：显式合法值 > 站点默认 > 推荐默认 active；空/非法值不隐式落到 latest。
	input.Sort = resolveTopicListSort(input.Sort, settings.ListDefaultSort)
	// M5：非空 after 优先于 page；非法游标在 store 校验。
	input.After = strings.TrimSpace(input.After)
	list, err := s.store.ListTopics(ctx, input)
	if err != nil {
		return TopicList{}, err
	}
	// store 用推荐默认截断；此处按运营 excerpt_rune_limit 再派生，改配置即生效。
	for i := range list.Items {
		list.Items[i] = applyTopicSummaryExcerpt(list.Items[i], settings.ExcerptRuneLimit)
		list.Items[i].Edited = settings.ShowTopicEditMark && list.Items[i].ContentEdited
	}
	return s.decorateTopicListExtensionBadges(ctx, list), nil
}

// decorateTopicListExtensionBadges 挂载 forum.topic.list.badges；失败只记日志。
func (s *Service) decorateTopicListExtensionBadges(ctx context.Context, list TopicList) TopicList {
	if s.topicSurfaces == nil {
		return list
	}
	badges, err := s.topicSurfaces.TopicExtensionListBadges(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "forum: resolve topic list extension badges failed", "err", err)
		return list
	}
	list.ExtensionListBadges = badges
	return list
}

func (s *Service) GetTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	if topicID <= 0 {
		return TopicDetail{}, ErrTopicNotFound
	}
	topic, err := s.store.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic = s.applyTopicDetailExcerpt(ctx, topic)
	return s.decorateTopicExtensionActions(ctx, topic), nil
}

// GetTopicBySlug 按 slug 查询主题。仅 "纯 slug" URL 模式使用，
// 依赖 topics_slug_idx（UNIQUE）保证全局唯一。空 slug 直接返回未找到。
func (s *Service) GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error) {
	if strings.TrimSpace(slug) == "" {
		return TopicDetail{}, ErrTopicNotFound
	}
	topic, err := s.store.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	topic = s.applyTopicDetailExcerpt(ctx, topic)
	return s.decorateTopicExtensionActions(ctx, topic), nil
}

// applyTopicDetailExcerpt 按运营配置从 plain_text 派生详情摘要字段，并填充编辑标记。
func (s *Service) applyTopicDetailExcerpt(ctx context.Context, topic TopicDetail) TopicDetail {
	limit := RecommendedExcerptRuneLimit
	showEdit := false
	if settings, err := s.resolvedSettings(ctx); err == nil {
		limit = settings.ExcerptRuneLimit
		showEdit = settings.ShowTopicEditMark
	}
	topic = applyTopicDetailExcerpt(topic, limit)
	return applyTopicEditMark(topic, showEdit)
}

func applyTopicDetailExcerpt(topic TopicDetail, limit int) TopicDetail {
	plain := topic.Content.PlainText
	if plain == "" {
		// 列表摘要可能只有 Excerpt 占位；详情应有 PlainText。
		plain = topic.Excerpt
	}
	excerpt := ExcerptFromPlain(plain, limit)
	topic.Excerpt = excerpt
	topic.Content.Excerpt = excerpt
	return topic
}

func applyTopicSummaryExcerpt(topic TopicSummary, limit int) TopicSummary {
	// store 已写入默认截断的 Excerpt；若有更长 plain 不可用时直接再截断 Excerpt。
	topic.Excerpt = ExcerptFromPlain(topic.Excerpt, limit)
	return topic
}

func applyCommentExcerpt(comment Comment, limit int) Comment {
	comment.Content.Excerpt = ExcerptFromPlain(comment.Content.PlainText, limit)
	if comment.ReplyTo != nil {
		// 父评论摘要同样按运营长度截断（store 已用默认上限）。
		comment.ReplyTo.Excerpt = ExcerptFromPlain(comment.ReplyTo.Excerpt, limit)
	}
	return comment
}

func (s *Service) decorateTopicExtensionActions(ctx context.Context, topic TopicDetail) TopicDetail {
	if s.topicActions != nil {
		actions, err := s.topicActions.TopicExtensionActions(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "forum: resolve topic extension actions failed", "err", err)
		} else {
			topic.ExtensionActions = actions
		}
	}
	// E2.1：sidebar / badges 与 actions 同属详情装饰；失败只记日志，不阻断主题读取。
	if s.topicSurfaces != nil {
		sidebar, err := s.topicSurfaces.TopicExtensionSidebar(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "forum: resolve topic extension sidebar failed", "err", err)
		} else {
			topic.ExtensionSidebar = sidebar
		}
		badges, err := s.topicSurfaces.TopicExtensionBadges(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "forum: resolve topic extension badges failed", "err", err)
		} else {
			topic.ExtensionBadges = badges
		}
	}
	return topic
}

// GetTopicForSearch 返回用于搜索索引的主题快照。
// 复用公开可见性过滤的 GetTopic：若主题已 hidden/deleted，返回 ErrTopicNotFound，
// 调用方（search.Indexer）据此清理索引项，实现优雅降级。
func (s *Service) GetTopicForSearch(ctx context.Context, topicID int64) (TopicDetail, error) {
	return s.GetTopic(ctx, topicID)
}

func (s *Service) CreateTopic(ctx context.Context, actor identity.Actor, input CreateTopicInput) (TopicDetail, error) {
	if !actor.Can(identity.PermissionTopicCreate) {
		return TopicDetail{}, identity.ErrPermissionDenied
	}
	input, err := s.applyTopicBeforeCreate(ctx, actor, input)
	if err != nil {
		return TopicDetail{}, err
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return TopicDetail{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return TopicDetail{}, ErrInvalidTopic
	}
	if err := validateTopicTitle(title, settings); err != nil {
		return TopicDetail{}, err
	}
	if err := s.enforceDuplicateTitlePolicy(ctx, title, 0, settings); err != nil {
		return TopicDetail{}, err
	}
	if err := validateTopicContent(input.Content.RawContent, settings); err != nil {
		return TopicDetail{}, err
	}
	if err := s.enforceTopicCreateLimitsForActor(ctx, actor, settings); err != nil {
		return TopicDetail{}, err
	}
	// 新人禁止外链：内容含 http(s):// 时拒绝。
	if trust := s.trustForActor(ctx, actor); trust.active && trust.forbidLinks && containsOutboundLink(input.Content.RawContent) {
		return TopicDetail{}, ErrOutboundLinkForbidden
	}
	categorySlug := strings.TrimSpace(input.CategorySlug)
	if categorySlug == "" {
		categorySlug = settings.DefaultCategorySlug
	}
	tagSlugs, err := normalizeTopicTagSlugs(input.TagSlugs, settings.TagMinPerTopic, settings.TagMaxPerTopic)
	if err != nil {
		return TopicDetail{}, err
	}
	content, err := renderContentWithContext(s, ctx, input.Content, settings.ExcerptRuneLimit, "topic")
	if err != nil {
		return TopicDetail{}, err
	}
	content, err = s.applyContentPostFilter(ctx, content, "topic", "new")
	if err != nil {
		return TopicDetail{}, err
	}
	var mentionNames []string
	if settings.MentionsEnabled {
		mentionNames = MentionedUsernames(contentMentionSource(content))
		if settings.MentionsMaxPerPost > 0 && len(mentionNames) > settings.MentionsMaxPerPost {
			return TopicDetail{}, ErrMentionsLimit
		}
	}
	attachmentIDs, _, err := normalizeAndValidateContentAttachmentIDs(content, input.Content.AttachmentIDs)
	if err != nil {
		return TopicDetail{}, err
	}
	publication, err := s.publicationDecision(ctx, actor.ID, input.Content.RawContent)
	if err != nil {
		return TopicDetail{}, err
	}
	status := TopicStatusActive
	if publication.Pending {
		status = TopicStatusPending
	}
	// slug 全局唯一化：冲突时追加 -2/-3 后缀。
	topicSlug, err := s.ensureUniqueTopicSlug(ctx, slugify(title), 0)
	if err != nil {
		return TopicDetail{}, err
	}
	created, err := s.store.CreateTopic(ctx, CreateTopicRecord{
		CategorySlug:       categorySlug,
		AuthorUserID:       actor.ID,
		Title:              title,
		Slug:               topicSlug,
		TagSlugs:           tagSlugs,
		TagCreationMode:    settings.TagCreationMode,
		Content:            content,
		Status:             status,
		ModerationTriggers: publication.Triggers,
		MentionedUsernames: mentionNames,
		IPAddress:          strings.TrimSpace(input.IPAddress),
		AttachmentIDs:      attachmentIDs,
	})
	if err != nil {
		return TopicDetail{}, err
	}
	createdPayload := buildTopicEventPayload(ctx, s.topicEventLinks, created.TopicSummary)
	s.events.Emit(ctx, appevents.Envelope{
		Name:          appevents.TopicCreated,
		Kind:          appevents.KindObserve,
		ActorUserID:   actor.ID,
		ResourceType:  "topic",
		ResourceID:    strconv.FormatInt(created.ID, 10),
		CorrelationID: appevents.NewID(),
		Payload:       createdPayload,
		OccurredAt:    time.Now().UTC(),
	})
	if created.Status == TopicStatusActive {
		s.indexTopic(ctx, created.ID)
	}
	return created, nil
}

func (s *Service) UpdateTopic(ctx context.Context, actor identity.Actor, input UpdateTopicInput) (TopicDetail, error) {
	if input.TopicID <= 0 {
		return TopicDetail{}, ErrTopicNotFound
	}
	topic, err := s.store.GetTopicForAction(ctx, input.TopicID)
	if err != nil {
		return TopicDetail{}, err
	}
	if !canEditTopic(actor, topic) {
		return TopicDetail{}, identity.ErrPermissionDenied
	}
	if !matchesExpectedRevision(input.ExpectedRevision, topic.CurrentRevision) {
		return TopicDetail{}, ErrRevisionConflict
	}
	if err := validateEditReason(actor.ID, topic.AuthorUserID, input.Reason); err != nil {
		return TopicDetail{}, err
	}

	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return TopicDetail{}, err
	}

	// 作者编辑窗口：版主/任意编辑权限不受窗口限制。窗口过期时不调用 filter。
	if isAuthorOnlyTopicEdit(actor, topic) && !withinEditWindow(topic.CreatedAt, settings.TopicEditWindowMinutes, time.Now().UTC()) {
		return TopicDetail{}, ErrEditWindowExpired
	}

	// E1.2：权限与编辑窗口通过后、字段校验与落库前同步 filter。
	input, err = s.applyTopicBeforeUpdate(ctx, actor, input)
	if err != nil {
		return TopicDetail{}, err
	}

	record := UpdateTopicRecord{
		TopicID:                     input.TopicID,
		EditorUserID:                actor.ID,
		ExpectedRevision:            input.ExpectedRevision,
		Reason:                      strings.TrimSpace(input.Reason),
		Origin:                      revisionOrigin(actor.ID, topic.AuthorUserID),
		AuthorUserID:                topic.AuthorUserID,
		TagCreationMode:             settings.TagCreationMode,
		LastEditIP:                  strings.TrimSpace(input.IPAddress),
		Operation:                   input.Operation,
		RestoredFromRevisionID:      input.RestoredFromRevisionID,
		RestoredFromRevisionNo:      input.RestoredFromRevisionNo,
		HistoricalAttachmentOwnerID: input.HistoricalAttachmentOwnerID,
	}

	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			return TopicDetail{}, ErrInvalidTopic
		}
		if err := validateTopicTitle(title, settings); err != nil {
			return TopicDetail{}, err
		}
		// 改标题同样受 duplicateTitlePolicy=block 约束（排除自身）。
		if err := s.enforceDuplicateTitlePolicy(ctx, title, input.TopicID, settings); err != nil {
			return TopicDetail{}, err
		}
		record.Title = title
		// 改标题时重新生成 slug 并保证全局唯一（排除自身）。
		uniqueSlug, err := s.ensureUniqueTopicSlug(ctx, slugify(title), input.TopicID)
		if err != nil {
			return TopicDetail{}, err
		}
		record.Slug = uniqueSlug
	}

	if input.CategorySlug != nil {
		categorySlug, ok := normalizeAdminSlug(*input.CategorySlug)
		if !ok {
			return TopicDetail{}, ErrInvalidTopic
		}
		record.CategorySlug = categorySlug
	}

	if input.TagSlugs != nil {
		tagSlugs, err := normalizeTopicTagSlugs(input.TagSlugs, settings.TagMinPerTopic, settings.TagMaxPerTopic)
		if err != nil {
			return TopicDetail{}, err
		}
		record.TagSlugs = tagSlugs
	}

	if input.Content != nil {
		if err := validateTopicContent(input.Content.RawContent, settings); err != nil {
			return TopicDetail{}, err
		}
		// 与创建路径一致：作者编辑外链等仍受新人信任阶梯限制。
		if trust := s.trustForActor(ctx, actor); trust.active && trust.forbidLinks && containsOutboundLink(input.Content.RawContent) {
			return TopicDetail{}, ErrOutboundLinkForbidden
		}
		content, err := renderContentWithContext(s, ctx, *input.Content, settings.ExcerptRuneLimit, "topic")
		if err != nil {
			return TopicDetail{}, err
		}
		content, err = s.applyContentPostFilter(ctx, content, "topic", strconv.FormatInt(input.TopicID, 10))
		if err != nil {
			return TopicDetail{}, err
		}
		record.HasContent = true
		record.Content = content
		attachmentIDs, submitted, err := normalizeAndValidateContentAttachmentIDs(content, input.Content.AttachmentIDs)
		if err != nil {
			return TopicDetail{}, err
		}
		record.ReplaceAttachments = submitted
		record.AttachmentIDs = attachmentIDs
		// 内容变更时重跑发布策略，避免编辑绕过创建时的预审门。
		publication, err := s.publicationDecision(ctx, actor.ID, input.Content.RawContent)
		if err != nil {
			return TopicDetail{}, err
		}
		if publication.Pending {
			record.RequeuePending = true
			record.ModerationTriggers = publication.Triggers
		}
	}

	updated, err := s.store.UpdateTopic(ctx, record)
	if err != nil {
		return TopicDetail{}, err
	}

	if !updated.UpdateApplied {
		return applyTopicEditMark(updated, settings.ShowTopicEditMark), nil
	}
	payload := buildTopicEventPayload(ctx, s.topicEventLinks, resolveTopicEventSnapshot(ctx, s.store, updated.TopicSummary))
	payload["actorUserId"] = actor.ID
	payload["revisionNo"] = updated.CurrentRevision
	payload["operation"] = revisionOperation(record.Operation)
	payload["changedFields"] = updated.UpdateChangedFields
	if record.RestoredFromRevisionID > 0 {
		payload["restoredFromRevisionNo"] = input.RestoredFromRevisionNo
	}
	s.emitTopicEvent(ctx, appevents.TopicUpdated, actor.ID, updated.ID, payload)
	// 仅 active 主题进入公开索引；pending 应移除以免审核前可见。
	if updated.Status == TopicStatusActive {
		s.indexTopic(ctx, updated.ID)
	} else {
		s.deleteTopicIndex(ctx, updated.ID)
	}
	return applyTopicEditMark(updated, settings.ShowTopicEditMark), nil
}

func matchesExpectedRevision(expected, current int64) bool {
	if expected <= 0 {
		return false
	}
	// current_revision=0 只会出现在 M1 回填未完成的旧行；公开读模型将它
	// 表示为 version 1，写事务仍会在锁内以真实值二次确认。
	if current <= 0 {
		current = 1
	}
	return expected == current
}

func validateEditReason(actorID, authorID int64, reason string) error {
	trimmed := strings.TrimSpace(reason)
	if actorID != authorID && trimmed == "" {
		return ErrRevisionReasonRequired
	}
	if utf8.RuneCountInString(trimmed) > 500 {
		return ErrRevisionReasonRequired
	}
	return nil
}

func revisionOrigin(actorID, authorID int64) string {
	if actorID == authorID {
		return RevisionOriginSelf
	}
	return RevisionOriginStaff
}

func (s *Service) DeleteTopic(ctx context.Context, actor identity.Actor, topicID int64) (TopicDetail, error) {
	if topicID <= 0 {
		return TopicDetail{}, ErrTopicNotFound
	}
	topic, err := s.store.GetTopicForAction(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	if !s.canDeleteTopicWithPolicy(ctx, actor, topic) {
		return TopicDetail{}, identity.ErrPermissionDenied
	}
	deleted, err := s.store.DeleteTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic = resolveTopicEventSnapshot(ctx, s.store, topic)
	topic.Status = TopicStatusDeleted
	deletedPayload := buildTopicEventPayload(ctx, s.topicEventLinks, topic)
	deletedPayload["actorUserId"] = actor.ID
	s.emitTopicEvent(ctx, appevents.TopicDeleted, actor.ID, topicID, deletedPayload)
	// 软删后从搜索索引移除，避免命中已删除主题。
	s.deleteTopicIndex(ctx, topicID)
	deleted.Content = PublicRenderedContent{}
	deleted.Excerpt = ""
	deleted.Edited = false
	deleted.EditedAt = nil
	return deleted, nil
}

func (s *Service) ApplyTopicAction(ctx context.Context, actor identity.Actor, input TopicLifecycleInput) (TopicLifecycleRecord, error) {
	if input.TopicID <= 0 {
		return TopicLifecycleRecord{}, ErrTopicNotFound
	}
	if _, err := validateTopicAction(input.Action); err != nil {
		return TopicLifecycleRecord{}, err
	}
	topic, err := s.store.GetTopicForAction(ctx, input.TopicID)
	if err != nil {
		return TopicLifecycleRecord{}, err
	}

	// 按动作类型判定权限。
	switch input.Action {
	case TopicActionHide, TopicActionRestore:
		if !canManageTopicVisibility(actor) {
			return TopicLifecycleRecord{}, identity.ErrPermissionDenied
		}
	case TopicActionLock, TopicActionUnlock:
		// 版主：topic.lock；作者：站点 allowAuthorCloseReplies + 主题归属。
		if !s.canLockTopic(ctx, actor, topic) {
			return TopicLifecycleRecord{}, identity.ErrPermissionDenied
		}
	case TopicActionPin, TopicActionUnpin:
		if !actor.Can(identity.PermissionTopicPin) {
			return TopicLifecycleRecord{}, identity.ErrPermissionDenied
		}
	}

	// restore 可作用于 hidden 或 deleted 主题；其它动作仅作用于可见主题。
	if input.Action != TopicActionRestore && (topic.Status == TopicStatusHidden || topic.Status == TopicStatusDeleted) {
		return TopicLifecycleRecord{}, ErrTopicNotFound
	}

	result, err := s.store.ApplyTopicAction(ctx, input)
	if err != nil {
		return TopicLifecycleRecord{}, err
	}

	topic = resolveTopicEventSnapshot(ctx, s.store, topic)
	topic.Status = result.Status
	topic.IsPinned = result.IsPinned
	actionEvent := buildTopicEventPayload(ctx, s.topicEventLinks, topic)
	actionEvent["actorUserId"] = actor.ID
	switch input.Action {
	case TopicActionHide:
		s.emitTopicEvent(ctx, appevents.TopicHidden, actor.ID, input.TopicID, actionEvent)
	case TopicActionRestore:
		s.emitTopicEvent(ctx, appevents.TopicRestored, actor.ID, input.TopicID, actionEvent)
	case TopicActionLock:
		s.emitTopicEvent(ctx, appevents.TopicLocked, actor.ID, input.TopicID, actionEvent)
	case TopicActionUnlock:
		s.emitTopicEvent(ctx, appevents.TopicUnlocked, actor.ID, input.TopicID, actionEvent)
	case TopicActionPin:
		s.emitTopicEvent(ctx, appevents.TopicPinned, actor.ID, input.TopicID, actionEvent)
	case TopicActionUnpin:
		s.emitTopicEvent(ctx, appevents.TopicUnpinned, actor.ID, input.TopicID, actionEvent)
	}
	// 隐藏从搜索索引移除；恢复/锁定/置顶等需重建文档（status 用于过滤与排序）。
	switch input.Action {
	case TopicActionHide:
		s.deleteTopicIndex(ctx, input.TopicID)
	case TopicActionRestore, TopicActionLock, TopicActionUnlock, TopicActionPin, TopicActionUnpin:
		s.indexTopic(ctx, input.TopicID)
	}
	return result, nil
}

// emitTopicEvent 发送主题相关 observe 事件的统一辅助，确保 ResourceType 等元信息一致。
func (s *Service) emitTopicEvent(ctx context.Context, name string, actorID, topicID int64, extra map[string]any) {
	payload := map[string]any{}
	for key, value := range extra {
		payload[key] = value
	}
	s.events.Emit(ctx, appevents.Envelope{
		Name:          name,
		Kind:          appevents.KindObserve,
		ActorUserID:   actorID,
		ResourceType:  "topic",
		ResourceID:    strconv.FormatInt(topicID, 10),
		CorrelationID: appevents.NewID(),
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	})
}

func (s *Service) CreateComment(ctx context.Context, actor identity.Actor, input CreateCommentInput) (Comment, error) {
	if !actor.Can(identity.PermissionPostCreate) {
		return Comment{}, identity.ErrPermissionDenied
	}
	if input.TopicID <= 0 {
		return Comment{}, ErrTopicNotFound
	}
	topic, err := s.store.GetTopicForComment(ctx, input.TopicID)
	if err != nil {
		return Comment{}, err
	}
	// 锁定/非 active 主题不可回复：须在 filter 之前拒绝，避免插件白跑。
	if topic.Status != TopicStatusActive {
		return Comment{}, ErrTopicClosed
	}

	// E1.1：鉴权与主题状态通过后、校验与落库前同步 filter（可拒绝或补丁 content）。
	input, err = s.applyCommentBeforeCreate(ctx, actor, input)
	if err != nil {
		return Comment{}, err
	}

	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return Comment{}, err
	}
	// 补丁后的正文走同一套长度/外链/提及校验。
	if err := validateCommentContent(input.Content.RawContent, settings); err != nil {
		return Comment{}, err
	}
	if err := s.enforceCommentCreateLimitsForActor(ctx, actor, settings); err != nil {
		return Comment{}, err
	}
	if trust := s.trustForActor(ctx, actor); trust.active && trust.forbidLinks && containsOutboundLink(input.Content.RawContent) {
		return Comment{}, ErrOutboundLinkForbidden
	}
	var parent *CommentSummary
	if input.ParentID != nil {
		summary, err := s.store.GetCommentSummary(ctx, *input.ParentID)
		if err != nil {
			return Comment{}, err
		}
		if summary.TopicID != input.TopicID || summary.Status != CommentStatusActive {
			return Comment{}, ErrInvalidTopic
		}
		if err := validateCommentNesting(summary.Depth, settings); err != nil {
			return Comment{}, err
		}
		parent = &summary
	} else if err := validateCommentNesting(-1, settings); err != nil {
		return Comment{}, err
	}

	content, err := renderContentWithContext(s, ctx, input.Content, settings.ExcerptRuneLimit, "comment")
	if err != nil {
		return Comment{}, err
	}
	content, err = s.applyContentPostFilter(ctx, content, "comment", "new")
	if err != nil {
		return Comment{}, err
	}
	// 提及只读取 Host 公开安全投影；保护块子内容仍参加审核与外链校验，
	// 但不会触发通知或成为内容存在性的旁路。
	var mentionNames []string
	if settings.MentionsEnabled {
		mentionNames = MentionedUsernames(contentMentionSource(content))
		if settings.MentionsMaxPerPost > 0 && len(mentionNames) > settings.MentionsMaxPerPost {
			return Comment{}, ErrMentionsLimit
		}
	}
	attachmentIDs, _, err := normalizeAndValidateContentAttachmentIDs(content, input.Content.AttachmentIDs)
	if err != nil {
		return Comment{}, err
	}
	publication, err := s.publicationDecision(ctx, actor.ID, input.Content.RawContent)
	if err != nil {
		return Comment{}, err
	}
	status := CommentStatusActive
	if publication.Pending {
		status = CommentStatusPending
	}
	created, err := s.store.CreateComment(ctx, CreateCommentRecord{
		TopicID:            input.TopicID,
		AuthorUserID:       actor.ID,
		TopicAuthorUserID:  topic.AuthorUserID,
		ParentID:           input.ParentID,
		Parent:             parent,
		Content:            content,
		Status:             status,
		ModerationTriggers: publication.Triggers,
		MentionedUsernames: mentionNames,
		IPAddress:          strings.TrimSpace(input.IPAddress),
		AttachmentIDs:      attachmentIDs,
	})
	if err != nil {
		return Comment{}, err
	}
	payload := map[string]any{
		"commentId":    created.ID,
		"topicId":      created.TopicID,
		"authorUserId": actor.ID,
	}
	if created.ParentID != nil {
		payload["parentId"] = *created.ParentID
	}
	s.events.Emit(ctx, appevents.Envelope{
		Name:          appevents.CommentCreated,
		Kind:          appevents.KindObserve,
		ActorUserID:   actor.ID,
		ResourceType:  "comment",
		ResourceID:    strconv.FormatInt(created.ID, 10),
		CorrelationID: appevents.NewID(),
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	})
	// 新评论更新了主题 last_activity_at，需重新索引以刷新搜索排序。
	if created.Status == CommentStatusActive {
		s.indexTopic(ctx, created.TopicID)
	}
	return created, nil
}
