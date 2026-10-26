package profile

import (
	"context"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

type Store interface {
	// GetProfile 读取用户资料行；不存在时返回空资料（首次访问按需 upsert）。
	GetProfile(ctx context.Context, userID int64) (Profile, error)
	// UpsertProfile 写入或更新用户资料行。
	UpsertProfile(ctx context.Context, input Profile) (Profile, error)
	// SetAvatarAttachment 设置用户头像附件，并维护 attachment_references/reference_count。
	SetAvatarAttachment(ctx context.Context, userID int64, attachmentID *int64, actorUserID int64) (Profile, error)
	// GetAvatarAttachment 读取头像附件展示所需的最小信息。
	GetAvatarAttachment(ctx context.Context, attachmentID int64) (AvatarAttachment, error)
	// GetUserSummaryByUsername 按用户名加载用户基本信息。
	GetUserSummaryByUsername(ctx context.Context, username string) (UserProfileSummary, error)
	// GetUserSummaryByID 按用户 ID 加载用户基本信息。
	GetUserSummaryByID(ctx context.Context, userID int64) (UserProfileSummary, error)
	// SearchPublicUsers 按昵称/用户名检索可公开访问的活跃用户。它不返回邮箱或其他
	// 非公开字段，因此调用方拿不到「能泄露的东西」。
	SearchPublicUsers(ctx context.Context, query string, limit int) ([]UserSearchHit, error)
	// GetProfileStats 统计用户的公开主题数与评论数。
	GetProfileStats(ctx context.Context, userID int64) (ProfileStats, error)
	// ListRecentTopics 列出用户最近的公开主题。
	ListRecentTopics(ctx context.Context, userID int64, limit int) ([]forum.TopicSummary, error)
	// ListRecentActivityTopics 按发布时间列出公开主题活动（首页摘要）。
	ListRecentActivityTopics(ctx context.Context, userID int64, limit int) ([]forum.TopicSummary, error)
	// ListActivityTopics 按发布时间分页列出公开主题活动。
	ListActivityTopics(ctx context.Context, userID int64, limit, offset int) ([]forum.TopicSummary, error)
	// ListRecentComments 按回复时间列出公开回复活动（首页摘要）。
	ListRecentComments(ctx context.Context, userID int64, limit int) ([]ProfileCommentActivity, error)
	// ListActivityComments 按回复时间分页列出公开回复活动。
	ListActivityComments(ctx context.Context, userID int64, limit, offset int) ([]ProfileCommentActivity, error)
}

// NormalizeProfile 是 service 层规范化输入后的资料写入库的中间结构。
type NormalizeProfile struct {
	UserID             int64
	Bio                *string
	Signature          *string
	Location           *string
	WebsiteURL         *string
	AvatarAttachmentID *int64
}
