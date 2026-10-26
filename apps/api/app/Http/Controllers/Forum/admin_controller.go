package forumcontroller

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type categoryGroupRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	Position    int    `json:"position"`
}

type updateCategoryGroupRequest struct {
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Visibility  *string `json:"visibility"`
	Position    *int    `json:"position"`
}

type categoryRequest struct {
	GroupID     int64  `json:"groupId"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	IconColor   string `json:"iconColor"`
	Visibility  string `json:"visibility"`
	Position    int    `json:"position"`
	DefaultSort string `json:"defaultSort"`
}

type updateCategoryRequest struct {
	GroupID     *int64  `json:"groupId"`
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	IconColor   *string `json:"iconColor"`
	Visibility  *string `json:"visibility"`
	Position    *int    `json:"position"`
	DefaultSort *string `json:"defaultSort"`
}

type tagRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	IconColor   string `json:"iconColor"`
	Status      string `json:"status"`
}

type updateTagRequest struct {
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	IconColor   *string `json:"iconColor"`
	Status      *string `json:"status"`
}

type updateForumSettingsRequest struct {
	DefaultCategorySlug *string `json:"defaultCategorySlug"`
	TagCreationMode     *string `json:"tagCreationMode"`
	TagPublicPages      *bool   `json:"tagPublicPages"`
	TagMinPerTopic      *int    `json:"tagMinPerTopic"`
	TagMaxPerTopic      *int    `json:"tagMaxPerTopic"`
	TopicsPerPage       *int    `json:"topicsPerPage"`
	CommentsPerPage     *int    `json:"commentsPerPage"`

	TopicTitleMinRunes       *int `json:"topicTitleMinRunes"`
	TopicTitleMaxRunes       *int `json:"topicTitleMaxRunes"`
	TopicContentMinRunes     *int `json:"topicContentMinRunes"`
	TopicContentMaxRunes     *int `json:"topicContentMaxRunes"`
	TopicEditWindowMinutes   *int `json:"topicEditWindowMinutes"`
	TopicCooldownSeconds     *int `json:"topicCooldownSeconds"`
	DailyTopicLimit          *int `json:"dailyTopicLimit"`
	CommentMinRunes          *int `json:"commentMinRunes"`
	CommentMaxRunes          *int `json:"commentMaxRunes"`
	CommentMaxNestingDepth   *int `json:"commentMaxNestingDepth"`
	TreeDescendantsPerRoot   *int `json:"treeDescendantsPerRoot"`
	CommentEditWindowMinutes *int `json:"commentEditWindowMinutes"`
	CommentCooldownSeconds   *int `json:"commentCooldownSeconds"`
	DailyCommentLimit        *int `json:"dailyCommentLimit"`
	ExcerptRuneLimit         *int `json:"excerptRuneLimit"`

	GuestRead               *string `json:"guestRead"`
	ListDefaultSort         *string `json:"listDefaultSort"`
	ListHotWindowDays       *int    `json:"listHotWindowDays"`
	AllowAuthorCloseReplies *bool   `json:"allowAuthorCloseReplies"`
	AllowAuthorDelete       *bool   `json:"allowAuthorDelete"`
	AutoLockIdleDays        *int    `json:"autoLockIdleDays"`
	ShowTopicEditMark       *bool   `json:"showTopicEditMark"`
	DuplicateTitlePolicy    *string `json:"duplicateTitlePolicy"`
	ShowCommentEditMark     *bool   `json:"showCommentEditMark"`
	SoftDeleteVisibility    *string `json:"softDeleteVisibility"`
	MentionsEnabled         *bool   `json:"mentionsEnabled"`
	MentionsMaxPerPost      *int    `json:"mentionsMaxPerPost"`

	ExternalInterstitial *bool   `json:"externalInterstitial"`
	TrustedDomains       *string `json:"trustedDomains"`
}

func (h *Controller) adminCategoryGroups(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if _, err := h.service.ForumSettings(c.Context(), actor); err != nil {
		return mapForumError(err)
	}
	// 后台必须看到隐藏分组与隐藏分类，否则隐藏项既不可见也不可维护。
	items, err := h.service.ListCategoryGroups(c.Context(), true)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateCategoryGroup(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req categoryGroupRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	item, err := h.service.CreateCategoryGroup(c.Context(), actor, forum.CreateCategoryGroupInput{
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
		Position:    req.Position,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateCategoryGroup(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateCategoryGroupRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	item, err := h.service.UpdateCategoryGroup(c.Context(), actor, forum.UpdateCategoryGroupInput{
		ID:          int64(paramInt(c, "groupID")),
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
		Position:    req.Position,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminDeleteCategoryGroup(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.taxonomy.DeleteCategoryGroup(c.Context(), actor, int64(paramInt(c, "groupID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminCategories(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if _, err := h.service.ForumSettings(c.Context(), actor); err != nil {
		return mapForumError(err)
	}
	// 后台必须看到隐藏分组与隐藏分类，否则隐藏项既不可见也不可维护。
	items, err := h.service.ListCategories(c.Context(), true)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateCategory(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req categoryRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	item, err := h.service.CreateCategory(c.Context(), actor, forum.CreateCategoryInput{
		GroupID:     req.GroupID,
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		IconColor:   req.IconColor,
		Visibility:  req.Visibility,
		Position:    req.Position,
		DefaultSort: req.DefaultSort,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateCategory(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateCategoryRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	item, err := h.service.UpdateCategory(c.Context(), actor, forum.UpdateCategoryInput{
		ID:          int64(paramInt(c, "categoryID")),
		GroupID:     req.GroupID,
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		IconColor:   req.IconColor,
		Visibility:  req.Visibility,
		Position:    req.Position,
		DefaultSort: req.DefaultSort,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminDeleteCategory(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.taxonomy.DeleteCategory(c.Context(), actor, int64(paramInt(c, "categoryID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminTags(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if _, err := h.service.ForumSettings(c.Context(), actor); err != nil {
		return mapForumError(err)
	}
	items, err := h.service.ListTags(c.Context(), true)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateTag(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req tagRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTag)
	}
	item, err := h.service.CreateTag(c.Context(), actor, forum.CreateTagInput{
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		IconColor:   req.IconColor,
		Status:      req.Status,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateTag(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateTagRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTag)
	}
	item, err := h.service.UpdateTag(c.Context(), actor, forum.UpdateTagInput{
		ID:          int64(paramInt(c, "tagID")),
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		IconColor:   req.IconColor,
		Status:      req.Status,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.ForumSettings(c.Context(), actor)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) adminUpdateSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateForumSettingsRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidSettings)
	}
	settings, err := h.service.UpdateForumSettings(c.Context(), actor, forum.UpdateForumSettingsInput{
		DefaultCategorySlug:      req.DefaultCategorySlug,
		TagCreationMode:          req.TagCreationMode,
		TagPublicPages:           req.TagPublicPages,
		TagMinPerTopic:           req.TagMinPerTopic,
		TagMaxPerTopic:           req.TagMaxPerTopic,
		TopicsPerPage:            req.TopicsPerPage,
		CommentsPerPage:          req.CommentsPerPage,
		TopicTitleMinRunes:       req.TopicTitleMinRunes,
		TopicTitleMaxRunes:       req.TopicTitleMaxRunes,
		TopicContentMinRunes:     req.TopicContentMinRunes,
		TopicContentMaxRunes:     req.TopicContentMaxRunes,
		TopicEditWindowMinutes:   req.TopicEditWindowMinutes,
		TopicCooldownSeconds:     req.TopicCooldownSeconds,
		DailyTopicLimit:          req.DailyTopicLimit,
		CommentMinRunes:          req.CommentMinRunes,
		CommentMaxRunes:          req.CommentMaxRunes,
		CommentMaxNestingDepth:   req.CommentMaxNestingDepth,
		TreeDescendantsPerRoot:   req.TreeDescendantsPerRoot,
		CommentEditWindowMinutes: req.CommentEditWindowMinutes,
		CommentCooldownSeconds:   req.CommentCooldownSeconds,
		DailyCommentLimit:        req.DailyCommentLimit,
		ExcerptRuneLimit:         req.ExcerptRuneLimit,
		GuestRead:                req.GuestRead,
		ListDefaultSort:          req.ListDefaultSort,
		ListHotWindowDays:        req.ListHotWindowDays,
		AllowAuthorCloseReplies:  req.AllowAuthorCloseReplies,
		AllowAuthorDelete:        req.AllowAuthorDelete,
		AutoLockIdleDays:         req.AutoLockIdleDays,
		ShowTopicEditMark:        req.ShowTopicEditMark,
		DuplicateTitlePolicy:     req.DuplicateTitlePolicy,
		ShowCommentEditMark:      req.ShowCommentEditMark,
		SoftDeleteVisibility:     req.SoftDeleteVisibility,
		MentionsEnabled:          req.MentionsEnabled,
		MentionsMaxPerPost:       req.MentionsMaxPerPost,
		ExternalInterstitial:     req.ExternalInterstitial,
		TrustedDomains:           req.TrustedDomains,
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) adminResetSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.ResetForumSettings(c.Context(), actor)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) adminContentTopics(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	input, err := adminContentListInput(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListAdminForumTopics(c.Context(), actor, input)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, list)
}

func (h *Controller) adminContentTopic(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	detail, err := h.service.GetAdminForumTopic(c.Context(), actor, int64(paramInt(c, "topicID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, detail)
}

func (h *Controller) adminContentComments(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	input, err := adminContentListInput(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListAdminForumComments(c.Context(), actor, input)
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, list)
}

func (h *Controller) adminContentComment(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	detail, err := h.service.GetAdminForumComment(c.Context(), actor, int64(paramInt(c, "commentID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, detail)
}

func adminContentListInput(c fiber.Ctx) (forum.AdminForumContentListInput, error) {
	updatedFrom, err := queryOptionalTime(c, "updatedFrom")
	if err != nil {
		return forum.AdminForumContentListInput{}, fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	updatedTo, err := queryOptionalTime(c, "updatedTo")
	if err != nil {
		return forum.AdminForumContentListInput{}, fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	}
	return forum.AdminForumContentListInput{
		After:          c.Query("after"),
		PerPage:        queryInt(c, "perPage"),
		Status:         c.Query("status"),
		AuthorUserID:   int64(queryInt(c, "authorUserID")),
		AuthorUsername: c.Query("authorUsername"),
		UpdatedFrom:    updatedFrom,
		UpdatedTo:      updatedTo,
		TopicID:        int64(queryInt(c, "topicID")),
		TitlePrefix:    c.Query("titlePrefix"),
		CategorySlug:   c.Query("categorySlug"),
	}, nil
}

func queryOptionalTime(c fiber.Ctx, key string) (time.Time, error) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

// adminListSearchProviders 列出已启用的 search.provider 与当前解析结果。需 search.manage。
func (h *Controller) adminListSearchProviders(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.searchProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	state, err := h.searchProviders.List(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, state)
}

// adminSelectSearchProvider 显式 pin 一个已启用的 search.provider。需 search.manage。
func (h *Controller) adminSelectSearchProvider(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.searchProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	var req struct {
		ExtensionID string `json:"extensionId"`
	}
	if err := c.Bind().Body(&req); err != nil || req.ExtensionID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "search.provider_invalid")
	}
	if err := h.searchProviders.Select(c.Context(), req.ExtensionID); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "search.provider_unavailable")
	}
	return apphttp.OK(c, map[string]bool{"selected": true})
}

// adminResetSearchProvider 清除 pin，解析回落站内搜索。需 search.manage。
func (h *Controller) adminResetSearchProvider(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.searchProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	if err := h.searchProviders.RestoreDefault(c.Context()); err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"pinned": false, "defaultExtensionId": "sforum.search-site"})
}

// adminReindexSearch 触发一次搜索索引全量重建。需 search.manage 权限。
func (h *Controller) adminReindexSearch(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.reindexer == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	run, err := h.reindexer.Reindex(c.Context(), actor.ID)
	if err != nil {
		return mapReindexError(err)
	}
	return apphttp.OK(c, run)
}

// adminReindexStatus 返回当前重建的实时进度。需 search.manage 权限。
func (h *Controller) adminReindexStatus(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.reindexer == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	status, err := h.reindexer.ReindexStatus(c.Context())
	if err != nil {
		return mapReindexError(err)
	}
	return apphttp.OK(c, status)
}

// adminReindexRuns 返回重建历史。需 search.manage 权限。
func (h *Controller) adminReindexRuns(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSearchManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.reindexer == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	}
	runs, err := h.reindexer.ListReindexRuns(c.Context())
	if err != nil {
		return mapReindexError(err)
	}
	return apphttp.OK(c, runs)
}
