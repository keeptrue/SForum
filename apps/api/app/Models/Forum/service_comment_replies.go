package forum

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

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
		// 与点赞路径同口径：隐藏主题下的评论一律按「评论不存在」返回，
		// 避免 comment_not_found / topic_not_found 的差异被用来枚举隐藏内容。
		if errors.Is(err, ErrTopicNotFound) {
			return nil, ErrCommentNotFound
		}
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
