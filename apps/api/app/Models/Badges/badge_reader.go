package badges

// 本文件是本包唯一的 Forum 依赖，唯一职责是实现 forum.BadgeReader。
//
// 这条单向边是安全的（Forum 不 import 本包），但它容易被后来者误读成
// 「Badges 可以随便用 Forum 类型」——那才是真正会制造循环依赖的入口。
// 因此：任何新逻辑都不要加在这个文件里；需要 Forum 类型的新能力先讨论依赖方向。
import (
	"context"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// Reader 实现 forum.BadgeReader：给主题/评论的作者补上最高级徽章。
//
// 这是本包唯一 import Forum 的文件，也是依赖方向上唯一被允许的方向：
// Forum 定义接口、不 import 本包；本包为了满足接口签名引用 Forum 的类型。
// 转换函数放在这里（而不是 Forum 侧），因为只有这里同时知道两边的形状。
type Reader struct {
	service *Service
}

func NewReader(service *Service) *Reader {
	return &Reader{service: service}
}

// DecorateTopics 批量补齐主题作者与最近回复作者的最高级徽章。
func (r *Reader) DecorateTopics(ctx context.Context, topics []forum.TopicSummary) error {
	if r == nil || r.service == nil || len(topics) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(topics)*2)
	for i := range topics {
		if topics[i].Author != nil && topics[i].Author.ID > 0 {
			userIDs = append(userIDs, topics[i].Author.ID)
		}
		if topics[i].LastReplyAuthor != nil && topics[i].LastReplyAuthor.ID > 0 {
			userIDs = append(userIDs, topics[i].LastReplyAuthor.ID)
		}
	}
	refs, err := r.service.TopBadgesFor(ctx, userIDs)
	if err != nil {
		return err
	}
	for i := range topics {
		if topics[i].Author != nil {
			topics[i].Author.TopBadge = toForumBadgeRef(refs[topics[i].Author.ID])
		}
		if topics[i].LastReplyAuthor != nil {
			topics[i].LastReplyAuthor.TopBadge = toForumBadgeRef(refs[topics[i].LastReplyAuthor.ID])
		}
	}
	return nil
}

// DecorateComments 批量补齐评论作者的最高级徽章。
func (r *Reader) DecorateComments(ctx context.Context, comments []forum.Comment) error {
	if r == nil || r.service == nil || len(comments) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(comments))
	for i := range comments {
		if comments[i].Author != nil && comments[i].Author.ID > 0 {
			userIDs = append(userIDs, comments[i].Author.ID)
		}
	}
	refs, err := r.service.TopBadgesFor(ctx, userIDs)
	if err != nil {
		return err
	}
	for i := range comments {
		if comments[i].Author != nil {
			comments[i].Author.TopBadge = toForumBadgeRef(refs[comments[i].Author.ID])
		}
	}
	return nil
}

// toForumBadgeRef 把本包类型转成 Forum 的读模型；没有徽章时返回 nil（不是空结构），
// 否则前端会渲染出一个空 chip。
func toForumBadgeRef(ref BadgeRef) *forum.BadgeRef {
	if ref.Slug == "" {
		return nil
	}
	return &forum.BadgeRef{
		Slug: ref.Slug,
		Name: ref.Name,
		Tier: string(ref.Tier),
		Icon: ref.Icon,
		TierName: forum.BadgeTierName{
			Zh: ref.Appearance.NameZh,
			En: ref.Appearance.NameEn,
		},
		TierColor: ref.Appearance.Color,
	}
}

var _ forum.BadgeReader = (*Reader)(nil)
