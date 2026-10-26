package forum

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func (s *Service) applyTopicBeforeCreate(ctx context.Context, actor identity.Actor, input CreateTopicInput) (CreateTopicInput, error) {
	envelope := appevents.NewEnvelope(appevents.TopicBeforeCreate, map[string]any{
		"actorUserId":  actor.ID,
		"categorySlug": input.CategorySlug,
		"tagSlugs":     input.TagSlugs,
		"title":        input.Title,
		"content":      input.Content,
	})
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "topic"
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return CreateTopicInput{}, appevents.Reject(result)
	}
	if len(result.Patch) == 0 {
		return input, nil
	}
	if value, ok := result.Patch["categorySlug"].(string); ok {
		input.CategorySlug = value
	}
	if value, ok := result.Patch["title"].(string); ok {
		input.Title = value
	}
	if value, ok := contentInputFromPatch(result.Patch["content"]); ok {
		input.Content = value
	}
	if value, ok := stringSliceFromPatch(result.Patch["tagSlugs"]); ok {
		input.TagSlugs = value
	}
	return input, nil
}

// applyTopicBeforeUpdate 调用 topic.before_update 同步 filter。
// Payload 仅携带本次请求出现的字段；补丁 allowlist 与 create 相同。
// 插件可补丁未出现在请求中的字段（例如强制加标签），host 随后统一重验。
func (s *Service) applyTopicBeforeUpdate(ctx context.Context, actor identity.Actor, input UpdateTopicInput) (UpdateTopicInput, error) {
	payload := map[string]any{
		"actorUserId": actor.ID,
		"topicId":     input.TopicID,
	}
	if input.CategorySlug != nil {
		payload["categorySlug"] = *input.CategorySlug
	}
	if input.Title != nil {
		payload["title"] = *input.Title
	}
	// TagSlugs 用 nil 表示未改标签；非 nil（含空切片）表示请求显式更新。
	if input.TagSlugs != nil {
		payload["tagSlugs"] = input.TagSlugs
	}
	if input.Content != nil {
		payload["content"] = *input.Content
	}
	envelope := appevents.NewEnvelope(appevents.TopicBeforeUpdate, payload)
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "topic"
	envelope.ResourceID = strconv.FormatInt(input.TopicID, 10)
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return UpdateTopicInput{}, appevents.Reject(result)
	}
	if len(result.Patch) == 0 {
		return input, nil
	}
	if value, ok := result.Patch["categorySlug"].(string); ok {
		input.CategorySlug = &value
	}
	if value, ok := result.Patch["title"].(string); ok {
		input.Title = &value
	}
	if value, ok := contentInputFromPatch(result.Patch["content"]); ok {
		input.Content = &value
	}
	if value, ok := stringSliceFromPatch(result.Patch["tagSlugs"]); ok {
		input.TagSlugs = value
	}
	return input, nil
}

func contentInputFromPatch(value any) (ContentInput, bool) {
	switch typed := value.(type) {
	case ContentInput:
		return typed, true
	case *ContentInput:
		if typed == nil {
			return ContentInput{}, false
		}
		return *typed, true
	case map[string]any:
		body, err := json.Marshal(typed)
		if err != nil {
			return ContentInput{}, false
		}
		var content ContentInput
		if err := json.Unmarshal(body, &content); err != nil {
			return ContentInput{}, false
		}
		return content, true
	default:
		return ContentInput{}, false
	}
}

func stringSliceFromPatch(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...), true
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			items = append(items, text)
		}
		return items, true
	default:
		return nil, false
	}
}

func (s *Service) ListComments(ctx context.Context, input CommentListInput) (CommentList, error) {
	if input.View == "" {
		input.View = "tree"
	}
	// view 枚举校验：非法值直接拒绝，避免 SQL 层走到默认 tree 分支而前端预期不符。
	if input.View != "tree" && input.View != "flat" {
		return CommentList{}, ErrInvalidTopic
	}
	// 可见性兜底：评论列表查询本身不过滤主题状态/分类可见性，
	// 这里复用 GetTopic 的公开可见性规则（status IN active/locked 且分类 public），
	// 隐藏/删除主题或非公开分类统一返回 ErrTopicNotFound，与主题详情页一致，
	// 避免评论接口成为隐藏主题内容的泄漏通道。
	if _, err := s.store.GetTopic(ctx, input.TopicID); err != nil {
		return CommentList{}, err
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return CommentList{}, err
	}
	input.Page, input.PerPage = resolveCommentPagination(input.Page, input.PerPage, settings.CommentsPerPage)
	// M5：非空 after 优先于 page（flat keyset）。
	input.After = strings.TrimSpace(input.After)
	// D2：tree 子孙 cap 由运营选项控制；未配置时 store 仍回落推荐默认 50。
	if input.TreeDescendantsPerRoot <= 0 {
		input.TreeDescendantsPerRoot = settings.TreeDescendantsPerRoot
	}
	input.IncludeDeleted, input.DeletedAuthorUserID = softDeleteQueryScope(settings.SoftDeleteVisibility, input.Viewer)
	list, err := s.store.ListComments(ctx, input)
	if err != nil {
		return CommentList{}, err
	}
	list.Items = applyCommentTreeExcerpts(list.Items, settings.ExcerptRuneLimit)
	list.Items = applyCommentEditMarks(list.Items, settings.ShowCommentEditMark)
	// softDeleteVisibility：列表默认只含 active；若有墓碑行再按策略过滤。
	list.Items = filterSoftDeletedComments(list.Items, settings.SoftDeleteVisibility, input.Viewer)
	return s.decorateCommentExtensionActions(ctx, list), nil
}

// resolveCommentPagination 归一化评论分页参数，与 ListComments 的决策保持一致：
// perPage<=0 时回落到运营配置 commentsPerPage，否则沿用调用方传入值（clamp 1-100）。
// ResolveCommentPage 必须复用本函数，否则反查页码会与实际分页错位。
func resolveCommentPagination(page, perPage, settingsCommentsPerPage int) (int, int) {
	defaultPerPage := 20
	if perPage <= 0 {
		defaultPerPage = settingsCommentsPerPage
	}
	return normalizePageWithDefault(page, perPage, defaultPerPage)
}

// ResolveCommentPage 返回 commentID 在 flat 视图分页下所属的页码与每页大小。
// 用于帖子详情页带 #comment-{id} 锚点进入时，反查应加载哪一页评论。
// 位置计数必须与该 viewer 实际看到的 flat 列表同口径：softDeleteVisibility 允许
// viewer 看到的软删墓碑同样占据分页槽位，只数 active 会导致反查页码整页错位。
// 目标评论不属于该主题、或对该 viewer 不可见时返回 ErrCommentNotFound（不泄漏状态）。
func (s *Service) ResolveCommentPage(ctx context.Context, topicID int64, commentID int64, viewer identity.Actor) (page int, perPage int, err error) {
	// 可见性兜底：与 ListComments 一致，隐藏/删除主题直接 NotFound。
	if _, err := s.store.GetTopic(ctx, topicID); err != nil {
		return 0, 0, err
	}
	summary, err := s.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return 0, 0, err
	}
	// 防止跨主题猜测：commentID 必须属于 topicID。
	if summary.TopicID != topicID {
		return 0, 0, ErrCommentNotFound
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return 0, 0, err
	}
	includeDeleted, deletedAuthorUserID := softDeleteQueryScope(settings.SoftDeleteVisibility, viewer)
	// 目标必须出现在该 viewer 的列表里：active，或按策略对其可见的软删墓碑。
	if summary.Status != CommentStatusActive {
		visibleTombstone := summary.Status == CommentStatusDeleted && includeDeleted &&
			(deletedAuthorUserID == 0 || summary.AuthorUserID == deletedAuthorUserID)
		if !visibleTombstone {
			return 0, 0, ErrCommentNotFound
		}
	}
	// perPage 归一化与 ListComments 同源，保证页码边界一致。
	_, perPage = resolveCommentPagination(1, 0, settings.CommentsPerPage)
	// 排在目标之前、对该 viewer 可见的评论数；flat 排序为 created_at ASC, id ASC。
	// 用发表时间做位置键：与时间流列表同口径，回复不再改变他人楼层。
	before, err := s.store.CountCommentsBefore(ctx, topicID, summary.CreatedAt, summary.ID, includeDeleted, deletedAuthorUserID)
	if err != nil {
		return 0, 0, err
	}
	// 第 before 条评论位于第 before/perPage + 1 页（before 从 0 计）。
	// ListComments 会把页码钳制到 MaxTopicPage，这里同步钳制，避免反查出永远取不到的页。
	page = int(before/int64(perPage)) + 1
	if page > MaxTopicPage {
		page = MaxTopicPage
	}
	return page, perPage, nil
}

func softDeleteQueryScope(visibility string, viewer identity.Actor) (include bool, authorUserID int64) {
	staff := viewer.Can(identity.PermissionPostDeleteAny) || viewer.Can(identity.PermissionModerationReview)
	switch visibility {
	case "staff_only":
		return staff, 0
	case "author_and_staff":
		if staff {
			return true, 0
		}
		if viewer.IsActive() {
			return true, viewer.ID
		}
	}
	return false, 0
}

// filterSoftDeletedComments 按 softDeleteVisibility 保留/剥离软删墓碑。
// 公开列表 SQL 仅 active；本函数覆盖含 deleted 行的扩展查询与单条回复场景。
func filterSoftDeletedComments(items []Comment, visibility string, viewer identity.Actor) []Comment {
	if len(items) == 0 {
		return items
	}
	out := make([]Comment, 0, len(items))
	for _, item := range items {
		if item.Status == CommentStatusDeleted {
			if !canViewSoftDeletedComment(item, visibility, viewer) {
				continue
			}
			// 墓碑：不泄漏正文。
			item.Content = PublicRenderedContent{}
		}
		if len(item.Children) > 0 {
			item.Children = filterSoftDeletedComments(item.Children, visibility, viewer)
		}
		out = append(out, item)
	}
	return out
}

func canViewSoftDeletedComment(comment Comment, visibility string, viewer identity.Actor) bool {
	switch visibility {
	case "hidden", "":
		return false
	case "staff_only":
		return viewer.Can(identity.PermissionPostDeleteAny) || viewer.Can(identity.PermissionModerationReview)
	case "author_and_staff":
		if viewer.Can(identity.PermissionPostDeleteAny) || viewer.Can(identity.PermissionModerationReview) {
			return true
		}
		return viewer.IsActive() && viewer.ID == comment.AuthorUserID
	default:
		return false
	}
}

func (s *Service) decorateCommentExtensionActions(ctx context.Context, list CommentList) CommentList {
	if s.commentActions == nil {
		return list
	}
	actions, err := s.commentActions.CommentExtensionActions(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "forum: resolve comment extension actions failed", "err", err)
		return list
	}
	list.ExtensionActions = actions
	return list
}

func applyCommentTreeExcerpts(items []Comment, limit int) []Comment {
	for i := range items {
		items[i] = applyCommentExcerpt(items[i], limit)
		if len(items[i].Children) > 0 {
			items[i].Children = applyCommentTreeExcerpts(items[i].Children, limit)
		}
	}
	return items
}

func (s *Service) ListCommentReplies(ctx context.Context, commentID int64) ([]Comment, error) {
	return s.ListCommentRepliesForViewer(ctx, commentID, identity.Actor{})
}

// ListCommentRepliesForViewer 与 ListCommentReplies 相同，但按 viewer 应用 softDeleteVisibility。
func (s *Service) ListCommentRepliesForViewer(ctx context.Context, commentID int64, viewer identity.Actor) ([]Comment, error) {
	if commentID <= 0 {
		return nil, ErrCommentNotFound
	}
	// 可见性兜底：回复查询只按 parent_comment_id 过滤，无法自证主题是否公开可见。
	// 先经评论摘要追溯到所属主题，再复用 GetTopic 的公开可见性规则。
	// 评论不存在或主题不可见时统一返回 404，避免通过枚举 commentID 读取隐藏主题的回复。
	summary, err := s.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetTopic(ctx, summary.TopicID); err != nil {
		return nil, err
	}
	limit := RecommendedExcerptRuneLimit
	showEdit := false
	visibility := "author_and_staff"
	if settings, settingsErr := s.resolvedSettings(ctx); settingsErr == nil {
		limit = settings.ExcerptRuneLimit
		showEdit = settings.ShowCommentEditMark
		visibility = settings.SoftDeleteVisibility
	}
	includeDeleted, deletedAuthorUserID := softDeleteQueryScope(visibility, viewer)
	items, err := s.store.ListCommentReplies(ctx, CommentReplyListInput{
		CommentID: commentID, IncludeDeleted: includeDeleted, DeletedAuthorUserID: deletedAuthorUserID,
	})
	if err != nil {
		return nil, err
	}
	items = applyCommentTreeExcerpts(items, limit)
	items = applyCommentEditMarks(items, showEdit)
	return filterSoftDeletedComments(items, visibility, viewer), nil
}

func (s *Service) UpdateComment(ctx context.Context, actor identity.Actor, input UpdateCommentInput) (Comment, error) {
	summary, err := s.store.GetCommentSummary(ctx, input.CommentID)
	if err != nil {
		return Comment{}, err
	}
	if !canEditComment(actor, summary) {
		return Comment{}, identity.ErrPermissionDenied
	}
	if !matchesExpectedRevision(input.ExpectedRevision, summary.CurrentRevision) {
		return Comment{}, ErrRevisionConflict
	}
	if err := validateEditReason(actor.ID, summary.AuthorUserID, input.Reason); err != nil {
		return Comment{}, err
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return Comment{}, err
	}
	// 作者编辑窗口：拥有 post.edit_any 的版主不受限。
	if isAuthorOnlyCommentEdit(actor, summary) && !withinEditWindow(summary.CreatedAt, settings.CommentEditWindowMinutes, time.Now().UTC()) {
		return Comment{}, ErrEditWindowExpired
	}
	input, err = s.applyCommentBeforeUpdate(ctx, actor, summary, input)
	if err != nil {
		return Comment{}, err
	}
	if err := validateCommentContent(input.Content.RawContent, settings); err != nil {
		return Comment{}, err
	}
	if trust := s.trustForActor(ctx, actor); trust.active && trust.forbidLinks && containsOutboundLink(input.Content.RawContent) {
		return Comment{}, ErrOutboundLinkForbidden
	}
	content, err := renderContentWithContext(s, ctx, input.Content, settings.ExcerptRuneLimit, "comment")
	if err != nil {
		return Comment{}, err
	}
	content, err = s.applyContentPostFilter(ctx, content, "comment", strconv.FormatInt(input.CommentID, 10))
	if err != nil {
		return Comment{}, err
	}
	record := UpdateCommentRecord{
		CommentID:                   input.CommentID,
		EditorUserID:                actor.ID,
		ExpectedRevision:            input.ExpectedRevision,
		Reason:                      strings.TrimSpace(input.Reason),
		Origin:                      revisionOrigin(actor.ID, summary.AuthorUserID),
		AuthorUserID:                summary.AuthorUserID,
		Content:                     content,
		LastEditIP:                  strings.TrimSpace(input.IPAddress),
		Operation:                   input.Operation,
		RestoredFromRevisionID:      input.RestoredFromRevisionID,
		RestoredFromRevisionNo:      input.RestoredFromRevisionNo,
		HistoricalAttachmentOwnerID: input.HistoricalAttachmentOwnerID,
	}
	attachmentIDs, submitted, err := normalizeAndValidateContentAttachmentIDs(content, input.Content.AttachmentIDs)
	if err != nil {
		return Comment{}, err
	}
	record.ReplaceAttachments = submitted
	record.AttachmentIDs = attachmentIDs
	// 内容编辑与创建共用发布策略，防止改文绕过预审。
	publication, err := s.publicationDecision(ctx, actor.ID, input.Content.RawContent)
	if err != nil {
		return Comment{}, err
	}
	if publication.Pending {
		record.RequeuePending = true
		record.ModerationTriggers = publication.Triggers
	}
	updated, err := s.store.UpdateComment(ctx, record)
	if err != nil {
		return Comment{}, err
	}
	if !updated.UpdateApplied {
		return applyCommentEditMark(updated, settings.ShowCommentEditMark), nil
	}
	payload := map[string]any{
		"commentId": updated.ID, "topicId": summary.TopicID, "actorUserId": actor.ID,
		"revisionNo": updated.CurrentRevision, "operation": revisionOperation(record.Operation),
		"changedFields": updated.UpdateChangedFields,
	}
	if record.RestoredFromRevisionID > 0 {
		payload["restoredFromRevisionNo"] = record.RestoredFromRevisionNo
	}
	s.events.Emit(ctx, appevents.Envelope{
		Name: appevents.CommentUpdated, Kind: appevents.KindObserve, ActorUserID: actor.ID,
		ResourceType: "comment", ResourceID: strconv.FormatInt(updated.ID, 10), CorrelationID: appevents.NewID(),
		Payload: payload, OccurredAt: time.Now().UTC(),
	})
	// 评论从 active 退回 pending 时主题可见评论数变化，刷新索引。
	if updated.Status == CommentStatusActive {
		s.indexTopic(ctx, summary.TopicID)
	} else if summary.Status == CommentStatusActive {
		s.indexTopic(ctx, summary.TopicID)
	}
	return applyCommentEditMark(updated, settings.ShowCommentEditMark), nil
}

func (s *Service) applyCommentBeforeUpdate(ctx context.Context, actor identity.Actor, summary CommentSummary, input UpdateCommentInput) (UpdateCommentInput, error) {
	envelope := appevents.NewEnvelope(appevents.CommentBeforeUpdate, map[string]any{
		"actorUserId": actor.ID, "commentId": input.CommentID, "topicId": summary.TopicID, "content": input.Content,
	})
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "comment"
	envelope.ResourceID = strconv.FormatInt(input.CommentID, 10)
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return UpdateCommentInput{}, appevents.Reject(result)
	}
	if content, ok := contentInputFromPatch(result.Patch["content"]); ok {
		input.Content = content
	}
	return input, nil
}

func (s *Service) DeleteComment(ctx context.Context, actor identity.Actor, commentID int64) (Comment, error) {
	summary, err := s.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return Comment{}, err
	}
	if !canDeleteComment(actor, summary) {
		return Comment{}, identity.ErrPermissionDenied
	}
	deleted, err := s.store.DeleteComment(ctx, commentID)
	if err != nil {
		return Comment{}, err
	}
	deleted.Content = PublicRenderedContent{}
	deleted.Edited = false
	deleted.EditedAt = nil
	return deleted, nil
}

func (s *Service) resolvedSettings(ctx context.Context) (ForumSettings, error) {
	settings, err := s.settings.ForumSettings(ctx)
	if err != nil {
		return ForumSettings{}, err
	}
	if !isValidForumSettings(settings) {
		return ForumSettings{}, ErrInvalidSettings
	}
	return settings, nil
}

func isValidForumSettings(settings ForumSettings) bool {
	if strings.TrimSpace(settings.DefaultCategorySlug) == "" {
		return false
	}
	switch settings.TagCreationMode {
	case TagCreationModeControlled, TagCreationModeReview, TagCreationModeOpen:
	default:
		return false
	}
	return validForumPageSize(settings.TopicsPerPage) &&
		validForumPageSize(settings.CommentsPerPage) &&
		validForumContentLimits(settings)
}

func (s *Service) enforceTopicCreateLimits(ctx context.Context, authorUserID int64, settings ForumSettings) error {
	return s.enforceTopicCreateLimitsForActor(ctx, identity.Actor{ID: authorUserID}, settings)
}

// enforceTopicCreateLimitsForActor 在全局节奏限制上叠加新人信任阶梯（更严者生效）。
func (s *Service) enforceTopicCreateLimitsForActor(ctx context.Context, actor identity.Actor, settings ForumSettings) error {
	now := time.Now().UTC()
	cooldown := settings.TopicCooldownSeconds
	daily := settings.DailyTopicLimit
	if trust := s.trustForActor(ctx, actor); trust.active {
		if trust.topicCooldown > 0 && (cooldown <= 0 || trust.topicCooldown > cooldown) {
			cooldown = trust.topicCooldown
		}
		if trust.dailyTopic > 0 && (daily <= 0 || trust.dailyTopic < daily) {
			daily = trust.dailyTopic
		}
	}
	if cooldown > 0 {
		lastAt, ok, err := s.store.LatestAuthorTopicCreatedAt(ctx, actor.ID)
		if err != nil {
			return err
		}
		if !cooldownElapsed(lastAt, ok, cooldown, now) {
			return newCooldownError(ErrTopicCooldown, lastAt, cooldown)
		}
	}
	if daily > 0 {
		count, err := s.store.CountAuthorTopicsSince(ctx, actor.ID, dayStartUTC(now))
		if err != nil {
			return err
		}
		if count >= int64(daily) {
			return ErrDailyTopicLimit
		}
	}
	return nil
}

func (s *Service) enforceCommentCreateLimits(ctx context.Context, authorUserID int64, settings ForumSettings) error {
	return s.enforceCommentCreateLimitsForActor(ctx, identity.Actor{ID: authorUserID}, settings)
}

func (s *Service) trustForActor(ctx context.Context, actor identity.Actor) trustLimits {
	// 未注入信任策略或无注册时间时不限制。
	if s.trust == nil || actor.CreatedAt.IsZero() {
		return trustLimits{}
	}
	// 机器人不进入新人信任阶梯：它没有登录凭据、发言由 AI 配额与审核闸门约束，
	// 而「新人禁止外链」会让它无法引用站内内容——那正是回复工具存在的意义。
	// 这条规则来自 AI 辅助平台计划 M3（Bot Identity）的既有决定。
	if actor.Kind.IsBot() {
		return trustLimits{}
	}
	days, err := s.trust.NewUserTrustDays(ctx)
	if err != nil || days <= 0 {
		return trustLimits{}
	}
	if time.Since(actor.CreatedAt) > time.Duration(days)*24*time.Hour {
		return trustLimits{}
	}
	// super_admin / 具备管理权限的用户跳过新人限制。
	if actor.Can(identity.PermissionSettingsManage) || actor.Can(identity.PermissionTopicEditAny) {
		return trustLimits{}
	}
	topicCooldown, _ := s.trust.NewUserTopicCooldownSeconds(ctx)
	commentCooldown, _ := s.trust.NewUserCommentCooldownSeconds(ctx)
	dailyTopic, _ := s.trust.NewUserDailyTopicLimit(ctx)
	dailyComment, _ := s.trust.NewUserDailyCommentLimit(ctx)
	forbidLinks, _ := s.trust.NewUserForbidOutboundLinks(ctx)
	return trustLimits{
		active:          true,
		topicCooldown:   topicCooldown,
		commentCooldown: commentCooldown,
		dailyTopic:      dailyTopic,
		dailyComment:    dailyComment,
		forbidLinks:     forbidLinks,
	}
}

var outboundLinkPattern = regexp.MustCompile(`(?i)https?://`)

func containsOutboundLink(raw string) bool {
	return outboundLinkPattern.MatchString(raw)
}

func isAuthorOnlyTopicEdit(actor identity.Actor, topic TopicSummary) bool {
	return !actor.Can(identity.PermissionTopicEditAny) && topic.AuthorUserID == actor.ID
}

func isAuthorOnlyCommentEdit(actor identity.Actor, comment CommentSummary) bool {
	return !actor.Can(identity.PermissionPostEditAny) && comment.AuthorUserID == actor.ID
}

func canManageForumSettings(actor identity.Actor) bool {
	return actor.Can(identity.PermissionCategoryManage) || actor.Can(identity.PermissionTagManage) || actor.Can(identity.PermissionForumSettingsManage)
}

func normalizeCreateCategoryGroupInput(input CreateCategoryGroupInput) (CreateCategoryGroupInput, error) {
	slug, ok := normalizeAdminSlug(input.Slug)
	if !ok {
		return CreateCategoryGroupInput{}, ErrInvalidTopic
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateCategoryGroupInput{}, ErrInvalidTopic
	}
	visibility, ok := normalizeCategoryVisibility(input.Visibility)
	if !ok {
		return CreateCategoryGroupInput{}, ErrInvalidTopic
	}
	input.Slug = slug
	input.Name = name
	input.Description = strings.TrimSpace(input.Description)
	input.Visibility = visibility
	return input, nil
}

func normalizeUpdateCategoryGroupInput(input UpdateCategoryGroupInput) (UpdateCategoryGroupInput, error) {
	if input.ID <= 0 {
		return UpdateCategoryGroupInput{}, ErrInvalidTopic
	}
	if input.Slug != nil {
		value, ok := normalizeAdminSlug(*input.Slug)
		if !ok {
			return UpdateCategoryGroupInput{}, ErrInvalidTopic
		}
		input.Slug = &value
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" {
			return UpdateCategoryGroupInput{}, ErrInvalidTopic
		}
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
	}
	if input.Visibility != nil {
		value, ok := normalizeCategoryVisibility(*input.Visibility)
		if !ok {
			return UpdateCategoryGroupInput{}, ErrInvalidTopic
		}
		input.Visibility = &value
	}
	return input, nil
}

func normalizeCreateCategoryInput(input CreateCategoryInput) (CreateCategoryInput, error) {
	if input.GroupID <= 0 {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	slug, ok := normalizeAdminSlug(input.Slug)
	if !ok {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	visibility, ok := normalizeCategoryVisibility(input.Visibility)
	if !ok {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	sort, ok := normalizeCategorySort(input.DefaultSort)
	if !ok {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	input.Slug = slug
	input.Name = name
	input.Description = strings.TrimSpace(input.Description)
	icon, ok := normalizeTaxonomyIcon(input.Icon)
	if !ok {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	iconColor, ok := normalizeTaxonomyIconColor(input.IconColor)
	if !ok {
		return CreateCategoryInput{}, ErrInvalidTopic
	}
	input.Icon = icon
	input.IconColor = iconColor
	input.Visibility = visibility
	input.DefaultSort = sort
	return input, nil
}

func normalizeUpdateCategoryInput(input UpdateCategoryInput) (UpdateCategoryInput, error) {
	if input.ID <= 0 {
		return UpdateCategoryInput{}, ErrInvalidTopic
	}
	if input.GroupID != nil && *input.GroupID <= 0 {
		return UpdateCategoryInput{}, ErrInvalidTopic
	}
	if input.Slug != nil {
		value, ok := normalizeAdminSlug(*input.Slug)
		if !ok {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.Slug = &value
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
	}
	if input.Icon != nil {
		value, ok := normalizeTaxonomyIcon(*input.Icon)
		if !ok {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.Icon = &value
	}
	if input.IconColor != nil {
		value, ok := normalizeTaxonomyIconColor(*input.IconColor)
		if !ok {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.IconColor = &value
	}
	if input.Visibility != nil {
		value, ok := normalizeCategoryVisibility(*input.Visibility)
		if !ok {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.Visibility = &value
	}
	if input.DefaultSort != nil {
		value, ok := normalizeCategorySort(*input.DefaultSort)
		if !ok {
			return UpdateCategoryInput{}, ErrInvalidTopic
		}
		input.DefaultSort = &value
	}
	return input, nil
}

func normalizeCreateTagInput(input CreateTagInput) (CreateTagInput, error) {
	slug, ok := normalizeTagSlug(input.Slug)
	if !ok {
		return CreateTagInput{}, ErrInvalidTag
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateTagInput{}, ErrInvalidTag
	}
	status, ok := normalizeTagStatus(input.Status)
	if !ok {
		return CreateTagInput{}, ErrInvalidTag
	}
	input.Slug = slug
	input.Name = name
	input.Description = strings.TrimSpace(input.Description)
	icon, ok := normalizeTaxonomyIcon(input.Icon)
	if !ok {
		return CreateTagInput{}, ErrInvalidTag
	}
	iconColor, ok := normalizeTaxonomyIconColor(input.IconColor)
	if !ok {
		return CreateTagInput{}, ErrInvalidTag
	}
	input.Icon = icon
	input.IconColor = iconColor
	input.Status = status
	return input, nil
}

func normalizeUpdateTagInput(input UpdateTagInput) (UpdateTagInput, error) {
	if input.ID <= 0 {
		return UpdateTagInput{}, ErrInvalidTag
	}
	if input.Slug != nil {
		value, ok := normalizeTagSlug(*input.Slug)
		if !ok {
			return UpdateTagInput{}, ErrInvalidTag
		}
		input.Slug = &value
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" {
			return UpdateTagInput{}, ErrInvalidTag
		}
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
	}
	if input.Icon != nil {
		value, ok := normalizeTaxonomyIcon(*input.Icon)
		if !ok {
			return UpdateTagInput{}, ErrInvalidTag
		}
		input.Icon = &value
	}
	if input.IconColor != nil {
		value, ok := normalizeTaxonomyIconColor(*input.IconColor)
		if !ok {
			return UpdateTagInput{}, ErrInvalidTag
		}
		input.IconColor = &value
	}
	if input.Status != nil {
		value, ok := normalizeTagStatus(*input.Status)
		if !ok {
			return UpdateTagInput{}, ErrInvalidTag
		}
		input.Status = &value
	}
	return input, nil
}

func normalizeUpdateForumSettingsInput(input UpdateForumSettingsInput) (UpdateForumSettingsInput, error) {
	if input.DefaultCategorySlug != nil {
		value, ok := normalizeAdminSlug(*input.DefaultCategorySlug)
		if !ok {
			return UpdateForumSettingsInput{}, ErrInvalidSettings
		}
		input.DefaultCategorySlug = &value
	}
	if input.TagCreationMode != nil {
		value := strings.ToLower(strings.TrimSpace(*input.TagCreationMode))
		switch value {
		case TagCreationModeControlled, TagCreationModeReview, TagCreationModeOpen:
			input.TagCreationMode = &value
		default:
			return UpdateForumSettingsInput{}, ErrInvalidSettings
		}
	}
	if input.TagMinPerTopic != nil && (*input.TagMinPerTopic < HardTagMinPerTopic || *input.TagMinPerTopic > HardTagMaxPerTopic) {
		return UpdateForumSettingsInput{}, ErrInvalidSettings
	}
	if input.TagMaxPerTopic != nil && (*input.TagMaxPerTopic < HardTagMinPerTopic || *input.TagMaxPerTopic > HardTagMaxPerTopic) {
		return UpdateForumSettingsInput{}, ErrInvalidSettings
	}
	if input.TagMinPerTopic != nil && input.TagMaxPerTopic != nil && *input.TagMinPerTopic > *input.TagMaxPerTopic {
		return UpdateForumSettingsInput{}, ErrInvalidSettings
	}
	if input.TopicsPerPage != nil && !validForumPageSize(*input.TopicsPerPage) {
		return UpdateForumSettingsInput{}, ErrInvalidSettings
	}
	if input.CommentsPerPage != nil && !validForumPageSize(*input.CommentsPerPage) {
		return UpdateForumSettingsInput{}, ErrInvalidSettings
	}
	if err := validateOptionalContentLimitFields(input); err != nil {
		return UpdateForumSettingsInput{}, err
	}
	return input, nil
}

func validateOptionalContentLimitFields(input UpdateForumSettingsInput) error {
	checkPair := func(minPtr, maxPtr *int, hardMin, hardMax int, allowZeroMax bool) error {
		if minPtr != nil && (*minPtr < hardMin || *minPtr > hardMax) {
			return ErrInvalidSettings
		}
		if maxPtr != nil {
			lower := hardMin
			if allowZeroMax {
				// max 字段至少为 1（0 在业务上表示不限，但我们的 max 字段始终为正上限）
				lower = 1
			}
			if *maxPtr < lower || *maxPtr > hardMax {
				return ErrInvalidSettings
			}
		}
		if minPtr != nil && maxPtr != nil && *minPtr > *maxPtr {
			return ErrInvalidSettings
		}
		return nil
	}
	if err := checkPair(input.TopicTitleMinRunes, input.TopicTitleMaxRunes, HardTitleMinRunes, HardTitleMaxRunes, false); err != nil {
		return err
	}
	if err := checkPair(input.TopicContentMinRunes, input.TopicContentMaxRunes, HardContentMinRunes, HardContentMaxRunes, true); err != nil {
		return err
	}
	if err := checkPair(input.CommentMinRunes, input.CommentMaxRunes, HardCommentMinRunes, HardCommentMaxRunes, true); err != nil {
		return err
	}
	for _, ptr := range []*int{
		input.TopicEditWindowMinutes,
		input.CommentEditWindowMinutes,
	} {
		if ptr != nil && (*ptr < 0 || *ptr > HardEditWindowMaxMin) {
			return ErrInvalidSettings
		}
	}
	for _, ptr := range []*int{
		input.TopicCooldownSeconds,
		input.CommentCooldownSeconds,
	} {
		if ptr != nil && (*ptr < 0 || *ptr > HardCooldownMaxSec) {
			return ErrInvalidSettings
		}
	}
	for _, ptr := range []*int{
		input.DailyTopicLimit,
		input.DailyCommentLimit,
	} {
		if ptr != nil && (*ptr < 0 || *ptr > HardDailyLimitMax) {
			return ErrInvalidSettings
		}
	}
	if input.CommentMaxNestingDepth != nil && (*input.CommentMaxNestingDepth < HardNestingMin || *input.CommentMaxNestingDepth > HardNestingMax) {
		return ErrInvalidSettings
	}
	if input.TreeDescendantsPerRoot != nil && (*input.TreeDescendantsPerRoot < HardTreeDescendantsPerRootMin || *input.TreeDescendantsPerRoot > HardTreeDescendantsPerRootMax) {
		return ErrInvalidSettings
	}
	if input.ExcerptRuneLimit != nil && (*input.ExcerptRuneLimit < HardExcerptMinRunes || *input.ExcerptRuneLimit > HardExcerptMaxRunes) {
		return ErrInvalidSettings
	}
	return nil
}

func validForumPageSize(value int) bool {
	return value >= 1 && value <= 100
}

func normalizeAdminSlug(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	return value, adminSlugPattern.MatchString(value)
}

func normalizeTagSlug(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	return value, tagSlugPattern.MatchString(value)
}

func normalizeTaxonomyIcon(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", true
	}
	return value, taxonomyIconPattern.MatchString(value)
}

func normalizeTaxonomyIconColor(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", true
	}
	return value, taxonomyIconColorPattern.MatchString(value)
}

func normalizeCategoryVisibility(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "public"
	}
	switch value {
	case "public", "hidden":
		return value, true
	default:
		return "", false
	}
}

func normalizeCategorySort(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "latest"
	}
	switch value {
	case "latest", "hot":
		return value, true
	default:
		return "", false
	}
}

func normalizeTagStatus(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = TagStatusActive
	}
	switch value {
	case TagStatusActive, TagStatusPending, TagStatusDisabled:
		return value, true
	default:
		return "", false
	}
}

func canEditComment(actor identity.Actor, comment CommentSummary) bool {
	if actor.Can(identity.PermissionPostEditAny) {
		return true
	}
	return comment.AuthorUserID == actor.ID && actor.Can(identity.PermissionPostEditOwn)
}

func canDeleteComment(actor identity.Actor, comment CommentSummary) bool {
	if actor.Can(identity.PermissionPostDeleteAny) {
		return true
	}
	return comment.AuthorUserID == actor.ID && actor.Can(identity.PermissionPostDeleteOwn)
}

// canEditTopic: 作者凭 topic.edit_own；版主凭 topic.edit_any。
func canEditTopic(actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicEditAny) {
		return true
	}
	return topic.AuthorUserID == actor.ID && actor.Can(identity.PermissionTopicEditOwn)
}

// canDeleteTopic: 作者凭 topic.delete_own；版主凭 topic.delete_any。
// 站点策略 AllowAuthorDelete=false 时作者删除被禁止（版主仍可删）。
func canDeleteTopic(actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicDeleteAny) {
		return true
	}
	return topic.AuthorUserID == actor.ID && actor.Can(identity.PermissionTopicDeleteOwn)
}

func (s *Service) canDeleteTopicWithPolicy(ctx context.Context, actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicDeleteAny) {
		return true
	}
	if !canDeleteTopic(actor, topic) {
		return false
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return false
	}
	return settings.AllowAuthorDelete
}

// canLockTopic：版主凭 topic.lock；作者在 allowAuthorCloseReplies 开启时可锁/解锁自己的主题。
func (s *Service) canLockTopic(ctx context.Context, actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicLock) {
		return true
	}
	if topic.AuthorUserID != actor.ID || !actor.IsActive() || !actor.Can(identity.PermissionTopicEditOwn) {
		return false
	}
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return false
	}
	return settings.AllowAuthorCloseReplies
}

// enforceDuplicateTitlePolicy：仅 block 服务端拒绝；off 与历史 warn 均不阻断。
func (s *Service) enforceDuplicateTitlePolicy(ctx context.Context, title string, excludeTopicID int64, settings ForumSettings) error {
	if settings.DuplicateTitlePolicy != "block" {
		return nil
	}
	exists, err := s.store.ActiveTopicTitleExists(ctx, title, excludeTopicID)
	if err != nil {
		return err
	}
	if exists {
		return ErrDuplicateTitle
	}
	return nil
}

// AutoLockIdleTopics 将超过 idleDays 无活动的 active 主题锁帖。
// idleDays<=0 时 no-op（站点关闭自动锁）。由周期任务调用。
func (s *Service) AutoLockIdleTopics(ctx context.Context, idleDays int, limit int) (int, error) {
	if idleDays <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if s.store == nil {
		return 0, nil
	}
	return s.store.AutoLockIdleTopics(ctx, idleDays, limit)
}

// canManageTopicVisibility: hide/restore 需要 topic.delete_any。
func canManageTopicVisibility(actor identity.Actor) bool {
	return actor.Can(identity.PermissionTopicDeleteAny)
}

// validateTopicAction 校验动作合法，返回该动作对应的主题状态。
func validateTopicAction(action string) (string, error) {
	switch action {
	case TopicActionHide:
		return TopicStatusHidden, nil
	case TopicActionRestore:
		return TopicStatusActive, nil
	case TopicActionLock:
		return TopicStatusLocked, nil
	case TopicActionUnlock:
		return TopicStatusActive, nil
	case TopicActionPin, TopicActionUnpin:
		// pin/unpin 只改 is_pinned，不改 status，这里返回空表示不更新 status。
		return "", nil
	default:
		return "", ErrInvalidAction
	}
}

// MaxTopicPage 限制主题列表的深翻页，避免 OFFSET 跳过大量行时的性能退化。
// 用户极少翻到 200 页之后；SEO 抓取深度可由 sitemap 单独控制。
// 导出给 Profile 等生成主题深链页码的调用方，保持同一钳制口径。
const MaxTopicPage = 200

func normalizePage(page int, perPage int) (int, int) {
	return normalizePageWithDefault(page, perPage, 20)
}

func normalizePageWithDefault(page int, perPage int, defaultPerPage int) (int, int) {
	if page <= 0 {
		page = 1
	}
	// 深翻页 clamp：超过上限的请求视为末页，消除 OFFSET 千万行扫描风险。
	if page > MaxTopicPage {
		page = MaxTopicPage
	}
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)
var adminSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var tagSlugPattern = regexp.MustCompile(`^[\p{L}\p{N}]+(?:-[\p{L}\p{N}]+)*$`)
var taxonomyIconPattern = regexp.MustCompile(`^i-[a-z0-9]+-[a-z0-9][a-z0-9-]*$`)
var taxonomyIconColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func normalizeTopicTagSlugs(values []string, min int, max int) ([]string, error) {
	if min < HardTagMinPerTopic || max < HardTagMinPerTopic || max > HardTagMaxPerTopic || min > max {
		return nil, ErrInvalidSettings
	}
	slugs := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		slug, ok := normalizeTagSlug(value)
		if slug == "" {
			continue
		}
		if !ok {
			return nil, ErrInvalidTag
		}
		if seen[slug] {
			continue
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	if err := validateTagCount(len(slugs), ForumSettings{TagMinPerTopic: min, TagMaxPerTopic: max}); err != nil {
		return nil, err
	}
	return slugs, nil
}

func slugify(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	slug := nonSlugChars.ReplaceAllString(lower, "-")
	slug = strings.Trim(slug, "-")
	if slug != "" {
		return slug
	}
	hash := strconv.FormatUint(uint64(fnv32(value)), 36)
	return "topic-" + hash
}

// ensureUniqueTopicSlug 在 slug 全局唯一的约束下生成不冲突的 slug。
// 冲突时按 Discourse/Flarum 惯例追加 -2、-3... 后缀。excludeTopicID 用于
// 更新主题时排除自身 slug。新建主题传 0。
func (s *Service) ensureUniqueTopicSlug(ctx context.Context, desired string, excludeTopicID int64) (string, error) {
	base := desired
	candidate := base
	for suffix := 2; ; suffix++ {
		exists, err := s.store.TopicSlugExists(ctx, candidate, excludeTopicID)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = base + "-" + strconv.Itoa(suffix)
	}
}

func fnv32(value string) uint32 {
	var hash uint32 = 2166136261
	for _, b := range []byte(value) {
		hash ^= uint32(b)
		hash *= 16777619
	}
	return hash
}
