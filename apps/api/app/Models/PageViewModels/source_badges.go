package pageviewmodels

import (
	"context"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// CoreBadgeViewReader 是公开徽章目录的窄读口（*badges.Service 满足它）。
//
// 用窄接口而不是直接吃 *badges.Service，理由与本文件的 CoreForumViewReader 相同：
// Models/Badges 已经 import Models/Forum（badge_reader.go 实现 Forum 声明的接口），
// 把具体服务塞进来会让依赖图更容易长成环。
//
// 两个方法刻意分开：PublicCatalog 的签名里没有 actor（目录本体 actor 无关），
// EarnedBadgeSlugs 是唯一吃 userID 的读，结果只在本次装配里合并、不写回目录结构。
type CoreBadgeViewReader interface {
	PublicCatalog(ctx context.Context) (badges.PublicCatalog, error)
	EarnedBadgeSlugs(ctx context.Context, userID int64) (map[string]struct{}, error)
}

// populateBadges 装配 /badges 公共目录页的载荷。
//
// 特性门不在这里判：catalog 条目带 RequiresFeatures:["features.badges"]，
// requirePageFeatures（source.go 的 Populate 最前面）已经短路并返回
// ErrCorePageDataNotFound → HTTP 层 404。这与徽章 API 的 403 是两层语义，
// 刻意不统一（改 404 映射会影响所有带 RequiresFeatures 的页）。
func (s *CorePageViewModelSource) populateBadges(ctx context.Context, request *pages.CorePageViewModelRequest, actor identity.Actor) error {
	if s.deps.Badges == nil {
		return ErrCorePageDataUnavailable
	}
	catalog, err := s.deps.Badges.PublicCatalog(ctx)
	if err != nil {
		return err
	}
	var earned map[string]struct{}
	if actor.ID > 0 {
		earned, err = s.deps.Badges.EarnedBadgeSlugs(ctx, actor.ID)
		if err != nil {
			return err
		}
	}
	title := localizedText(request.Locale, "徽章", "Badges")
	request.SEO.Title = title
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), title, request.Path)
	request.Data.BadgeIndex = &themecompiler.BadgeIndexPageViewModel{
		Groups:    mapBadgeCatalogGroups(catalog.Groups, earned),
		Truncated: catalog.Truncated,
	}
	return nil
}

// mapBadgeCatalogGroups 把领域目录映射成主题可渲染的分组。
//
// earned 只做查表：领域目录结构里没有 earned 字段，所以「actor 相关数据混进
// 可缓存结构」在这里无从发生。
func mapBadgeCatalogGroups(groups []badges.PublicCatalogGroup, earned map[string]struct{}) []themecompiler.BadgeCatalogGroupView {
	out := make([]themecompiler.BadgeCatalogGroupView, 0, len(groups))
	for _, group := range groups {
		items := make([]themecompiler.BadgeCatalogItemView, 0, len(group.Badges))
		for _, entry := range group.Badges {
			_, granted := earned[entry.Slug]
			items = append(items, themecompiler.BadgeCatalogItemView{
				Slug:        entry.Slug,
				Name:        entry.Name,
				Description: entry.Description,
				Icon:        entry.Icon,
				Tier:        string(entry.Tier),
				Group:       entry.GroupKey,
				Earned:      granted,
			})
		}
		out = append(out, themecompiler.BadgeCatalogGroupView{
			Key:    group.Key,
			Name:   group.Name,
			Badges: items,
		})
	}
	return out
}
