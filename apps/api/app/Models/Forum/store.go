package forum

import (
	"context"
	"fmt"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type Store interface {
	// ListCategories includeHidden=false 只返回公开分类（公开读路径）；
	// 后台管理传 true，隐藏分类仍可见可维护。
	ListCategories(ctx context.Context, includeHidden bool) ([]Category, error)
	// ListCategoryGroups 与 ListCategories 同语义：includeHidden 控制隐藏分组与隐藏分类。
	ListCategoryGroups(ctx context.Context, includeHidden bool) ([]CategoryGroup, error)
	ListTags(ctx context.Context, includePending bool) ([]Tag, error)
	CreateCategoryGroup(ctx context.Context, input CreateCategoryGroupInput) (CategoryGroup, error)
	UpdateCategoryGroup(ctx context.Context, input UpdateCategoryGroupInput) (CategoryGroup, error)
	// DeleteCategoryGroup 删除空分组；仍有分类时返回 ErrCategoryGroupNotEmpty。
	DeleteCategoryGroup(ctx context.Context, groupID int64) (CategoryGroup, error)
	CreateCategory(ctx context.Context, input CreateCategoryInput) (Category, error)
	UpdateCategory(ctx context.Context, input UpdateCategoryInput) (Category, error)
	// DeleteCategory 删除无主题的分类；仍有主题时返回 ErrCategoryNotEmpty。
	DeleteCategory(ctx context.Context, categoryID int64) (Category, error)
	CreateTag(ctx context.Context, input CreateTagInput) (Tag, error)
	UpdateTag(ctx context.Context, input UpdateTagInput) (Tag, error)
	ListTopics(ctx context.Context, input TopicListInput) (TopicList, error)
	// ListAllTopicIDs 返回所有可公开索引的主题 ID（active/locked），
	// 用于搜索索引批量重建。仅 SELECT id，无 JOIN，千万级秒扫。
	ListAllTopicIDs(ctx context.Context) ([]int64, error)
	GetTopic(ctx context.Context, topicID int64) (TopicDetail, error)
	// GetTopicBySlug 按全局唯一 slug 查询公开主题（active/locked），
	// 仅供 "纯 slug" URL 模式使用。依赖 topics_slug_unique_idx 保证唯一。
	GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error)
	// TopicSlugExists 检查 slug 是否已被其它主题占用（排除 excludeTopicID 自身），
	// 用于创建/更新时确保 slug 全局唯一。
	TopicSlugExists(ctx context.Context, slug string, excludeTopicID int64) (bool, error)
	// ActiveTopicTitleExists 是否存在同标题的公开主题（active/locked），用于 duplicateTitlePolicy=block。
	ActiveTopicTitleExists(ctx context.Context, title string, excludeTopicID int64) (bool, error)
	ResolveTopicTags(ctx context.Context, input ResolveTopicTagsInput) ([]TopicTagSummary, error)
	CreateTopic(ctx context.Context, input CreateTopicRecord) (TopicDetail, error)
	UpdateTopic(ctx context.Context, input UpdateTopicRecord) (TopicDetail, error)
	DeleteTopic(ctx context.Context, topicID int64) (TopicDetail, error)
	ApplyTopicAction(ctx context.Context, input TopicLifecycleInput) (TopicLifecycleRecord, error)
	GetTopicForComment(ctx context.Context, topicID int64) (TopicSummary, error)
	// GetTopicForAction 加载主题摘要（含 author/status），不做公开可见性过滤，
	// 用于更新/删除/生命周期动作的权限判定。
	GetTopicForAction(ctx context.Context, topicID int64) (TopicSummary, error)
	// GetTopicEditSource bypasses actor-independent public caches. The editable
	// source service must authorize the actor and resource state before calling it.
	GetTopicEditSource(ctx context.Context, topicID int64) (EditableContentSource, error)
	CreateComment(ctx context.Context, input CreateCommentRecord) (Comment, error)
	GetCommentSummary(ctx context.Context, commentID int64) (CommentSummary, error)
	// GetCommentEditSource has the same uncached, pre-authorized contract as the
	// topic source read.
	GetCommentEditSource(ctx context.Context, commentID int64) (EditableContentSource, error)
	UpdateComment(ctx context.Context, input UpdateCommentRecord) (Comment, error)
	DeleteComment(ctx context.Context, commentID int64) (Comment, error)
	ListComments(ctx context.Context, input CommentListInput) (CommentList, error)
	ListCommentReplies(ctx context.Context, input CommentReplyListInput) ([]Comment, error)
	// CountCommentsBefore 返回同主题内、flat 视图排序（created_at ASC, id ASC）下
	// 排在 (createdAt, id) 之前、对当前 viewer 可见的评论数（active + 按软删可见范围
	// 计入的 deleted 墓碑），用于反查某条评论所在的分页页码。
	// includeDeleted/deletedAuthorUserID 语义与 CommentListInput 相同，
	// 必须与 listCommentsFlat 的 ORDER BY / WHERE 可见范围严格对齐。
	CountCommentsBefore(ctx context.Context, topicID int64, createdAt time.Time, id int64, includeDeleted bool, deletedAuthorUserID int64) (int64, error)
	// 作者发帖/评论节奏统计：冷却与每日上限。无记录时 ok=false。
	LatestAuthorTopicCreatedAt(ctx context.Context, authorUserID int64) (time.Time, bool, error)
	CountAuthorTopicsSince(ctx context.Context, authorUserID int64, since time.Time) (int64, error)
	LatestAuthorCommentCreatedAt(ctx context.Context, authorUserID int64) (time.Time, bool, error)
	CountAuthorCommentsSince(ctx context.Context, authorUserID int64, since time.Time) (int64, error)
	// AutoLockIdleTopics 将 last_activity_at 早于 idleDays 的 active 主题批量锁定。
	AutoLockIdleTopics(ctx context.Context, idleDays int, limit int) (int, error)
	ListTopicRevisions(ctx context.Context, topicID int64, input RevisionListInput) (RevisionList, error)
	// ListTopicContributionTimeline 公开可读：仅 active/locked + public 分类主题；
	// 返回修订 header 级事件，不含 reason/正文。
	ListTopicContributionTimeline(ctx context.Context, topicID int64, input RevisionListInput) (TopicContributionTimeline, error)
	GetTopicRevision(ctx context.Context, topicID int64, revisionNo int64) (ForumRevisionDetail, error)
	ListCommentRevisions(ctx context.Context, commentID int64, input RevisionListInput) (RevisionList, error)
	GetCommentRevision(ctx context.Context, commentID int64, revisionNo int64) (ForumRevisionDetail, error)
	RedactTopicRevision(ctx context.Context, input RevisionRedactionRecord) error
	RedactCommentRevision(ctx context.Context, input RevisionRedactionRecord) error
	ListAdminForumTopics(ctx context.Context, input AdminForumContentListInput) (AdminForumContentList, error)
	GetAdminForumTopic(ctx context.Context, topicID int64) (AdminForumTopicDetail, error)
	ListAdminForumComments(ctx context.Context, input AdminForumContentListInput) (AdminForumContentList, error)
	GetAdminForumComment(ctx context.Context, commentID int64) (AdminForumCommentDetail, error)
}

type SettingsResolver interface {
	ForumSettings(ctx context.Context) (ForumSettings, error)
}

type SettingsManager interface {
	SettingsResolver
	UpdateForumSettings(ctx context.Context, actor identity.Actor, input UpdateForumSettingsInput) (ForumSettings, error)
	ResetForumSettings(ctx context.Context, actor identity.Actor) (ForumSettings, error)
}

type PublicationPolicy interface {
	EvaluatePublication(ctx context.Context, input PublicationInput) (PublicationDecision, error)
}

type AuthorReviewStore interface {
	ListAuthorReviewItems(ctx context.Context, authorUserID int64) (AuthorReviewList, error)
}

type ResolveTopicTagsInput struct {
	ActorUserID  int64
	Slugs        []string
	CreationMode string
}

func CommentPositionForInsert(commentID int64, parent *CommentSummary) CommentPosition {
	segment := formatCommentPathSegment(commentID)
	if parent == nil {
		return CommentPosition{RootCommentID: commentID, PathKey: segment, Depth: 0}
	}
	return CommentPosition{
		RootCommentID: parent.RootCommentID,
		PathKey:       parent.PathKey + "." + segment,
		Depth:         parent.Depth + 1,
	}
}

func formatCommentPathSegment(id int64) string {
	return fmt.Sprintf("%012d", id)
}
