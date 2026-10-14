package aireply

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

// PostgresReader 从数据库读取生成所需的线程快照。
//
// 它只读公开可见的内容：status 为 active/locked 且分类可见性为 public 的主题，
// 以及 status='active' 的评论。机器人不该因为「是系统调用的」而看到隐藏内容、
// 待审内容或非公开分类——可见性判断必须与访客看到的一致，否则机器人会把不该
// 公开的东西复述出来。
type PostgresReader struct {
	pool *pgxpool.Pool
}

func NewPostgresReader(pool *pgxpool.Pool) *PostgresReader {
	return &PostgresReader{pool: pool}
}

func (r *PostgresReader) ReplyContext(ctx context.Context, topicID int64, parentCommentID int64, triggerCommentID int64) (ReplyContext, error) {
	if r == nil || r.pool == nil {
		return ReplyContext{}, ErrGeneratorUnavailable
	}
	var thread ReplyContext
	err := r.pool.QueryRow(ctx, `
		SELECT topics.title,
		  COALESCE(authors.username, ''),
		  COALESCE(authors.display_name, ''),
		  COALESCE(posts.plain_text, '')
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		JOIN posts ON posts.id = topics.content_id
		LEFT JOIN users authors ON authors.id = topics.author_user_id
		WHERE topics.id = $1
		  AND topics.status IN ('active', 'locked')
		  AND topics.deleted_at IS NULL
		  AND categories.visibility = 'public'
	`, topicID).Scan(&thread.TopicTitle, &thread.TopicAuthor.Username, &thread.TopicAuthor.DisplayName, &thread.TopicBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReplyContext{}, fmt.Errorf("reply context: topic %d is not publicly visible", topicID)
	}
	if err != nil {
		return ReplyContext{}, fmt.Errorf("load topic title: %w", err)
	}

	recent, err := r.recentComments(ctx, topicID)
	if err != nil {
		return ReplyContext{}, err
	}
	thread.RecentComments = recent

	if parentCommentID > 0 {
		author, content, isBot, err := r.comment(ctx, parentCommentID)
		if err != nil {
			return ReplyContext{}, err
		}
		thread.ParentAuthor = author
		thread.ParentContent = content
		thread.ParentIsBot = isBot
	}
	if triggerCommentID > 0 && triggerCommentID != parentCommentID {
		author, content, _, err := r.comment(ctx, triggerCommentID)
		if err != nil {
			return ReplyContext{}, err
		}
		thread.TriggerAuthor = author
		thread.TriggerContent = content
	}
	return thread, nil
}

// recentComments 取最近若干条公开评论并按时间正序返回：提示词需要的是对话
// 顺序，而不是倒序的查询结果。
//
// 每条都取用户的 username 与显示名：昵称用于阅读，@用户名用于调用站内工具。
func (r *PostgresReader) recentComments(ctx context.Context, topicID int64) ([]ThreadComment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(users.username, ''),
		  COALESCE(users.display_name, ''),
		  COALESCE(posts.plain_text, ''), users.kind
		FROM comments
		JOIN posts ON posts.id = comments.content_id
		LEFT JOIN users ON users.id = comments.author_user_id
		WHERE comments.topic_id = $1 AND comments.status = 'active'
		ORDER BY comments.created_at DESC
		LIMIT $2
	`, topicID, RecentCommentWindow)
	if err != nil {
		return nil, fmt.Errorf("load recent comments: %w", err)
	}
	defer rows.Close()
	collected := make([]ThreadComment, 0, RecentCommentWindow)
	for rows.Next() {
		var author AuthorRef
		var content string
		var kind *string
		if err := rows.Scan(&author.Username, &author.DisplayName, &content, &kind); err != nil {
			return nil, err
		}
		collected = append(collected, ThreadComment{
			Author:  author,
			Content: content,
			IsBot:   kind != nil && identity.UserKind(*kind).IsBot(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 查询是倒序的，翻回时间正序。
	for left, right := 0, len(collected)-1; left < right; left, right = left+1, right-1 {
		collected[left], collected[right] = collected[right], collected[left]
	}
	return collected, nil
}

// ReplyImageAttachments 返回一次回复需要考虑图片的定位：主楼与被回复那条
// 评论。只返回定位信息（publicID/content_type），字节由 completer 层在确认
// 多模态开关开启后才读——查询很便宜，读字节很贵。
func (r *PostgresReader) ReplyImageAttachments(ctx context.Context, topicID int64, parentCommentID int64) ([]ImageAttachment, error) {
	if r == nil || r.pool == nil {
		return nil, ErrGeneratorUnavailable
	}
	// 只取公开、active 的图片附件；与可见内容的对齐由 attachments 侧的
	// authorizeAttachmentView 保证，这里先收窄到「机器人在公开主题里能引用的
	// 那类附件」。
	rows, err := r.pool.Query(ctx, `
		SELECT a.public_id, a.content_type
		FROM attachment_references ar
		JOIN attachments a ON a.id = ar.attachment_id
		WHERE (
		    (ar.resource_type = 'topic' AND ar.resource_id = $1)
		    OR (ar.resource_type = 'comment' AND ar.resource_id = $2)
		  )
		  AND a.status = 'active'
		  AND a.visibility = 'public'
		  AND a.deleted_at IS NULL
		  AND a.content_type IN ('image/jpeg', 'image/png', 'image/webp', 'image/gif')
		ORDER BY a.id
		LIMIT $3
	`, topicID, parentCommentID, supportai.MaxImagesPerMessage*2)
	if err != nil {
		return nil, fmt.Errorf("load reply image attachments: %w", err)
	}
	defer rows.Close()
	collected := make([]ImageAttachment, 0, supportai.MaxImagesPerMessage)
	for rows.Next() {
		var attachment ImageAttachment
		if err := rows.Scan(&attachment.PublicID, &attachment.ContentType); err != nil {
			return nil, err
		}
		collected = append(collected, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return collected, nil
}

func (r *PostgresReader) comment(ctx context.Context, commentID int64) (AuthorRef, string, bool, error) {
	var author AuthorRef
	var content string
	var kind *string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(users.username, ''),
		  COALESCE(users.display_name, ''),
		  COALESCE(posts.plain_text, ''), users.kind
		FROM comments
		JOIN posts ON posts.id = comments.content_id
		LEFT JOIN users ON users.id = comments.author_user_id
		WHERE comments.id = $1 AND comments.status = 'active'
	`, commentID).Scan(&author.Username, &author.DisplayName, &content, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorRef{}, "", false, fmt.Errorf("reply context: comment %d is not publicly visible", commentID)
	}
	if err != nil {
		return AuthorRef{}, "", false, fmt.Errorf("load comment: %w", err)
	}
	return author, content, kind != nil && identity.UserKind(*kind).IsBot(), nil
}
