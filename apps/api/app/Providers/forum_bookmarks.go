package providers

import (
	"context"

	forumcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Forum"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// 收藏的装配件放在本文件：forum.go 已经贴着行数闸门，而「点赞 + 收藏」这两个
// 用户交互协作者的装配天然成对，合并成一个入口后 forum.go 的调用行数不变。
func attachForumEngagement(controller *forumcontroller.Controller, service *forum.Service, optionsService *options.Service) {
	attachForumReactions(controller, service, optionsService)
	attachForumBookmarks(controller, service, optionsService)
}

// attachForumBookmarks 装配收藏协作者。
//
// 与 attachForumReactions 同一姿态：optionsService 为 nil 时不注入 FeatureGate
// （未装配时视为开启），避免把 nil *Service 塞进非空接口后 panic。
func attachForumBookmarks(controller *forumcontroller.Controller, service *forum.Service, optionsService *options.Service) *forum.BookmarkService {
	if controller == nil || service == nil {
		return nil
	}
	bookmarks := forum.NewBookmarkService(service)
	if optionsService != nil {
		bookmarks.WithFeatureGate(optionsService)
	}
	controller.WithBookmarks(bookmarks)
	return bookmarks
}

// BookmarkService 暴露收藏协作者，供 bootstrap 接到 Profile 的最小接口上。
// 从 controller 取回同一个实例：另建一个会丢掉 FeatureGate 装配。
func (p *ForumProvider) BookmarkService() *forum.BookmarkService {
	if p == nil {
		return nil
	}
	return p.controller.BookmarkService()
}

// ForumBookmarkLister 把 Forum 的收藏名单适配成 Profile 的最小接口。
//
// 依赖方向刻意单向：Forum 不认识 Profile（forum.BookmarkService 只实现自己的
// ListUserBookmarks），Profile 只认 BookmarkLister；把两者接起来的是 bootstrap
// 的装配层，也就是这里。SQL 仍然只存在于 Forum 的 store 里。
type ForumBookmarkLister struct {
	service *forum.BookmarkService
}

func NewForumBookmarkLister(service *forum.BookmarkService) ForumBookmarkLister {
	return ForumBookmarkLister{service: service}
}

var _ profile.BookmarkLister = ForumBookmarkLister{}

func (l ForumBookmarkLister) ListForViewer(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (profile.BookmarkPage, error) {
	if l.service == nil {
		return profile.BookmarkPage{Items: []profile.BookmarkItem{}}, nil
	}
	list, err := l.service.ListUserBookmarks(ctx, viewerID, ownerUserID, limit, offset)
	if err != nil {
		return profile.BookmarkPage{}, err
	}
	page := profile.BookmarkPage{
		Items:   make([]profile.BookmarkItem, 0, len(list.Items)),
		Total:   list.Total,
		HasMore: list.HasMore,
	}
	for _, entry := range list.Items {
		item := profile.BookmarkItem{
			TargetType: entry.TargetType,
			TargetID:   entry.TargetID,
			Visibility: entry.Visibility,
			CreatedAt:  entry.CreatedAt,
			Topic:      entry.Topic,
			Comment:    entry.Comment,
			RemindAt:   entry.RemindAt,
			RemindedAt: entry.RemindedAt,
		}
		// 第二道闸（防御纵深）：提醒时间是行主私有计划数据，owner-only。
		// Forum 的 store 已在 SQL 与 entry 两层清过；这里再清一次，防止未来
		// 改动漏掉上游闸门时 Profile 契约把私有调度数据吐给匿名访客。
		if viewerID != ownerUserID {
			item.RemindAt = nil
			item.RemindedAt = nil
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}
