package forum

import "context"

// 收藏写在缓存装饰层上的姿态：转发给内层 BookmarkStore，并在提交后失效
// 主题详情 / 列表 / 评论列表。公开计数列是读模型的一部分，缓存不失效会持续
// 返回旧计数，直到 TTL 到期（详情 30s、评论 20s）。

func (s *CachedStore) ApplyBookmark(ctx context.Context, input BookmarkApplyInput) (BookmarkState, error) {
	store, ok := s.Store.(BookmarkStore)
	if !ok {
		return BookmarkState{}, ErrInvalidBookmarkTarget
	}
	scopes := s.topicListScopes(ctx, input.TopicID)
	state, err := store.ApplyBookmark(ctx, input)
	if err != nil {
		return BookmarkState{}, err
	}
	s.invalidateTopicDetail(ctx, input.TopicID)
	s.invalidateTopicsScoped(ctx, scopes)
	if input.TargetType == BookmarkTargetComment {
		s.invalidateComments(ctx, input.TopicID)
	}
	return state, nil
}

// BookmarkedTargetIDs 纯读、actor 相关装饰的数据源：直接转发内层，不进缓存。
func (s *CachedStore) BookmarkedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error) {
	store, ok := s.Store.(BookmarkStore)
	if !ok {
		return map[int64]struct{}{}, nil
	}
	return store.BookmarkedTargetIDs(ctx, viewerUserID, targetType, targetIDs)
}

// ListUserBookmarks 行主维度名单：直接转发内层，不进缓存（可见性随行主切换）。
func (s *CachedStore) ListUserBookmarks(ctx context.Context, ownerUserID int64, includePrivate bool, limit, offset int) (BookmarkList, error) {
	store, ok := s.Store.(BookmarkStore)
	if !ok {
		return BookmarkList{Items: []BookmarkEntry{}}, nil
	}
	return store.ListUserBookmarks(ctx, ownerUserID, includePrivate, limit, offset)
}
