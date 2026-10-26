package forum

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// ResolveNotificationTarget 复用公开论坛读路径的可见性规则。通知收件人身份
// 不能提升公开目标权限；后台审核入口不是公开通知链接。
func (s *PostgresStore) ResolveNotificationTarget(ctx context.Context, _ int64, targetType string, targetID int64) (bool, string, error) {
	if s == nil || targetID <= 0 {
		return false, "", nil
	}
	switch targetType {
	case "topic":
		if _, err := s.GetTopic(ctx, targetID); err != nil {
			if errors.Is(err, ErrTopicNotFound) {
				return false, "", nil
			}
			return false, "", err
		}
		return true, fmt.Sprintf("/t/%d", targetID), nil
	case "comment":
		comment, err := s.GetCommentSummary(ctx, targetID)
		if err != nil {
			if errors.Is(err, ErrCommentNotFound) {
				return false, "", nil
			}
			return false, "", err
		}
		if comment.Status != CommentStatusActive {
			return false, "", nil
		}
		if _, err := s.GetTopic(ctx, comment.TopicID); err != nil {
			if errors.Is(err, ErrTopicNotFound) {
				return false, "", nil
			}
			return false, "", err
		}
		page := s.notificationTargetCommentPage(ctx, comment)
		// 与前端 forumTopicPath 同形：page>1 时追加 /page/N，page 1 保持旧链接形态。
		if page > 1 {
			return true, fmt.Sprintf("/t/%d/page/%d#comment-%d", comment.TopicID, page, targetID), nil
		}
		return true, fmt.Sprintf("/t/%d#comment-%d", comment.TopicID, targetID), nil
	default:
		return false, "", nil
	}
}

// notificationTargetCommentPage 计算目标评论在 flat 公开分页下所属的页码，
// 与 ResolveCommentPage / Profile.ListActivityComments 同口径：
// page = before/perPage + 1，并钳制到 MaxTopicPage。口径固定为公开可见
// （只数 active，与通知目标"复用公开读路径可见性"的契约一致）；计数或
// 每页大小读取失败时回落 1，宁可降级也不阻断通知链接生成。
func (s *PostgresStore) notificationTargetCommentPage(ctx context.Context, comment CommentSummary) int {
	before, err := s.CountCommentsBefore(ctx, comment.TopicID, comment.CreatedAt, comment.ID, false, 0)
	if err != nil {
		return 1
	}
	perPage := s.commentsPerPageOption(ctx)
	page := int(before/int64(perPage)) + 1
	if page > MaxTopicPage {
		page = MaxTopicPage
	}
	return page
}

// commentsPerPageOption 读取运营配置的评论每页条数；失败或非法时回落 20
// （与 resolveCommentPagination 的默认决策一致，clamp 1-100）。
func (s *PostgresStore) commentsPerPageOption(ctx context.Context) int {
	const fallback = 20
	var raw string
	err := s.pool.QueryRow(ctx, `
		SELECT value FROM web_options WHERE name = $1
	`, options.NameForumCommentsPerPage).Scan(&raw)
	if err != nil {
		return fallback
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(raw))
	if convErr != nil || n < 1 {
		return fallback
	}
	if n > 100 {
		return 100
	}
	return n
}

// ResolveNotificationTargetPreview 只返回当前公开读路径可见的纯文本摘要。
// 评论上下文优先取直接父评论；根评论则取主题正文，便于收件人理解回复关系。
func (s *PostgresStore) ResolveNotificationTargetPreview(ctx context.Context, targetType string, targetID int64) (NotificationTargetPreview, bool, error) {
	if s == nil || targetID <= 0 {
		return NotificationTargetPreview{}, false, nil
	}
	switch targetType {
	case "topic":
		topic, err := s.GetTopic(ctx, targetID)
		if errors.Is(err, ErrTopicNotFound) {
			return NotificationTargetPreview{}, false, nil
		}
		if err != nil {
			return NotificationTargetPreview{}, false, err
		}
		return NotificationTargetPreview{
			TopicID: topic.ID, TopicTitle: topic.Title,
			Content: NotificationTargetPreviewContent{Type: "topic", ID: topic.ID, Excerpt: topic.Content.Excerpt, Author: topic.Author},
		}, true, nil
	case "comment":
		comment, err := getCommentByID(ctx, s.pool, targetID, s.avatarBuilder)
		if errors.Is(err, ErrCommentNotFound) {
			return NotificationTargetPreview{}, false, nil
		}
		if err != nil {
			return NotificationTargetPreview{}, false, err
		}
		if comment.Status != CommentStatusActive {
			return NotificationTargetPreview{}, false, nil
		}
		topic, err := s.GetTopic(ctx, comment.TopicID)
		if errors.Is(err, ErrTopicNotFound) {
			return NotificationTargetPreview{}, false, nil
		}
		if err != nil {
			return NotificationTargetPreview{}, false, err
		}
		preview := NotificationTargetPreview{
			TopicID: topic.ID, TopicTitle: topic.Title,
			Content: NotificationTargetPreviewContent{Type: "comment", ID: comment.ID, Excerpt: comment.Content.Excerpt, Author: comment.Author},
		}
		if comment.ReplyTo != nil {
			preview.Context = &NotificationTargetPreviewContent{Type: "comment", ID: comment.ReplyTo.ID, Excerpt: comment.ReplyTo.Excerpt, Author: comment.ReplyTo.Author}
		} else {
			preview.Context = &NotificationTargetPreviewContent{Type: "topic", ID: topic.ID, Excerpt: topic.Content.Excerpt, Author: topic.Author}
		}
		return preview, true, nil
	default:
		return NotificationTargetPreview{}, false, nil
	}
}
