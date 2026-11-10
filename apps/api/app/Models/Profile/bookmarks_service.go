package profile

import (
	"context"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	bookmarkPageDefaultPerPage = 20
	bookmarkPageMaxPerPage     = 50
	bookmarkPageMaxPage        = 200
)

// WithBookmarkLister 注入收藏名单读取面（Forum 实现）。未注入时收藏端点返回
// 空页而不是报错：资料页的可选面缺失不该让整页 500。
func (s *Service) WithBookmarkLister(lister BookmarkLister) *Service {
	if s != nil {
		s.bookmarks = lister
	}
	return s
}

// ListProfileBookmarks 返回某个用户名下的收藏名单。
//
// 口径由 Forum 的 BookmarkLister 执行：本人（viewer.UserID == 目标用户）看到全部，
// 其他人只看到公开收藏。这里只做用户名解析、分页归一与匿名降级。
func (s *Service) ListProfileBookmarks(ctx context.Context, username string, viewer identity.Actor, page, perPage int) (BookmarkView, error) {
	normalized := strings.TrimSpace(username)
	if normalized == "" {
		return BookmarkView{}, ErrProfileNotFound
	}
	if s == nil || s.store == nil {
		return BookmarkView{}, ErrProfileInvalid
	}
	user, err := s.store.GetUserSummaryByUsername(ctx, normalized)
	if err != nil {
		return BookmarkView{}, err
	}
	result, err := s.listBookmarksForUser(ctx, user.UserID, viewer, page, perPage)
	if err != nil {
		return BookmarkView{}, err
	}
	// OwnerView 只给 HTTP 层决定缓存头：本人视角含私密行，绝不能进共享缓存。
	return BookmarkView{Page: result, OwnerView: viewer.ID > 0 && viewer.ID == user.UserID}, nil
}

// ListMyBookmarks 返回当前登录用户自己的收藏名单（含私密行）。
func (s *Service) ListMyBookmarks(ctx context.Context, actor identity.Actor, page, perPage int) (BookmarkPage, error) {
	if s == nil || s.store == nil {
		return BookmarkPage{}, ErrProfileInvalid
	}
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return BookmarkPage{}, identity.ErrPermissionDenied
	}
	return s.listBookmarksForUser(ctx, actor.ID, actor, page, perPage)
}

func (s *Service) listBookmarksForUser(ctx context.Context, ownerUserID int64, viewer identity.Actor, page, perPage int) (BookmarkPage, error) {
	if s.bookmarks == nil {
		return BookmarkPage{Items: []BookmarkItem{}}, nil
	}
	page, perPage = normalizeBookmarkPage(page, perPage)
	offset := (page - 1) * perPage
	result, err := s.bookmarks.ListForViewer(ctx, viewer.ID, ownerUserID, perPage, offset)
	if err != nil {
		return BookmarkPage{}, err
	}
	if result.Items == nil {
		result.Items = []BookmarkItem{}
	}
	return result, nil
}

// normalizeBookmarkPage 与资料活动分页同一套边界（默认 20、上限 50、最多 200 页）。
func normalizeBookmarkPage(page, perPage int) (int, int) {
	if perPage <= 0 {
		perPage = bookmarkPageDefaultPerPage
	}
	if perPage > bookmarkPageMaxPerPage {
		perPage = bookmarkPageMaxPerPage
	}
	if page <= 0 {
		page = 1
	}
	if page > bookmarkPageMaxPage {
		page = bookmarkPageMaxPage
	}
	return page, perPage
}
