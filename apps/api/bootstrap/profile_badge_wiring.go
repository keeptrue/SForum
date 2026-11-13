package bootstrap

import (
	"context"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// profileTopBadges 把 badges 的「当前展示徽章」批量读口适配成 Profile 需要的最小接口。
//
// 为什么映射放在装配层而不是 Profile 或 Badges 里：
//   - Badges 不知道公开资料页的 wire 形状（forum.BadgeRef 是 Forum 的读模型，
//     徽章包反过来 import Forum 会把「徽章域」绑到论坛读模型上）；
//   - Profile 不许写第二份徽章 SQL：数据源必须是 users.top_badge_id，与帖子/评论
//     旁 chip 同源，而那份 SQL 在 badges.PostgresStore.TopBadgesFor；
//   - 于是两个包之间只留一个窄接口，字段翻译（badges.BadgeRef → forum.BadgeRef）
//     住在唯一同时看得见两边的地方——装配层，和 profileActivityCounters 同一先例。
type profileTopBadges struct {
	store *badges.PostgresStore
}

func (r profileTopBadges) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]forum.BadgeRef, error) {
	result := make(map[int64]forum.BadgeRef, len(userIDs))
	// 空 store 不是错误：与 profileActivityCounters 一样，未装配时返回空集合，
	// 让资料页表现为「没有徽章」而不是整页失败。
	if r.store == nil || len(userIDs) == 0 {
		return result, nil
	}
	refs, err := r.store.TopBadgesFor(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for userID, ref := range refs {
		// 逐字段显式映射：两边的 JSON 键名不同（Appearance → tierName/tierColor），
		// 结构体嵌入或反射复制都会在任一侧加字段时静默丢掉前端要读的键。
		result[userID] = forum.BadgeRef{
			Slug:      ref.Slug,
			Name:      ref.Name,
			Tier:      string(ref.Tier),
			Icon:      ref.Icon,
			TierName:  forum.BadgeTierName{Zh: ref.Appearance.NameZh, En: ref.Appearance.NameEn},
			TierColor: ref.Appearance.Color,
		}
	}
	return result, nil
}

var _ profile.TopBadgeReader = profileTopBadges{}
