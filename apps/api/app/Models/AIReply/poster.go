package aireply

import (
	"context"
	"errors"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

var (
	// ErrNotABot 表示试图让一个非机器人账号走生成回复的路径。
	ErrNotABot = errors.New("aireply: only bot accounts may post generated replies")
)

// CommentCreator 是以指定身份创建评论的最小面，由 forum.Service 实现。
type CommentCreator interface {
	CreateComment(ctx context.Context, actor identity.Actor, input forum.CreateCommentInput) (forum.Comment, error)
}

// BotCommentPoster 以机器人身份发布回复。
//
// 它刻意走与普通用户完全相同的写路径：审核决策、提及解析、通知扇出、修订记录
// 一个都不跳过。绕过其中任何一步，机器人都成了发布后门——用户只要在 @ 的内容里
// 塞进广告，就能借机器人把审核拦下的东西发出去。
type BotCommentPoster struct {
	users identity.ActorStore
	forum CommentCreator
}

func NewBotCommentPoster(users identity.ActorStore, creator CommentCreator) *BotCommentPoster {
	return &BotCommentPoster{users: users, forum: creator}
}

// PostBotComment 以机器人身份发布回复。
//
// input.Citations 由生成器在**发布成功之后**另行落库，不由本类型写入。原因有两条：
//   - 证据必须后于评论存在，否则一次失败的发布会在 ai_citations 里留下指向一条
//     从未出现的评论的依据（外键会先拦下它，但那说明写入时机已经错了）；
//   - run_id 是生成侧的事实（A3 的 run 关联 id 只出现在 CompletionOutput 里），
//     本类型拿不到它，写出来的依据会缺失归因。
//
// 因此本类型对 Citations 的处理是「不处理」：它只保证评论本身按普通用户路径
// 创建成功并返回评论 id，证据由调用方用这个 id 去写。
func (p *BotCommentPoster) PostBotComment(ctx context.Context, botUserID int64, input BotCommentInput) (int64, error) {
	if p == nil || p.users == nil || p.forum == nil {
		return 0, ErrGeneratorUnavailable
	}
	if botUserID <= 0 || input.TopicID <= 0 {
		return 0, ErrGeneratorUnavailable
	}
	actor, err := p.users.LoadActor(ctx, botUserID)
	if err != nil {
		return 0, err
	}
	// 防御：只有机器人账号能走这条路径。人类账号的发言必须来自它自己的会话，
	// 否则这里会变成一条以他人名义发帖的通道。
	if !actor.Kind.IsBot() {
		return 0, ErrNotABot
	}
	var parentID *int64
	if input.ParentID > 0 {
		parent := input.ParentID
		parentID = &parent
	}
	comment, err := p.forum.CreateComment(ctx, actor, forum.CreateCommentInput{
		TopicID:  input.TopicID,
		ParentID: parentID,
		Content: forum.ContentInput{
			RawContent:   input.Content,
			SourceFormat: "markdown",
			EditorType:   "markdown",
		},
	})
	if err != nil {
		return 0, err
	}
	return comment.ID, nil
}
