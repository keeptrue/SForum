package forum

import "context"

// 点赞写在缓存装饰层上的姿态：转发给内层 ReactionStore，并在提交后失效
// 主题详情 / 列表 / 评论列表。计数列是读模型的一部分，缓存不失效会持续返回
// 旧计数，直到 TTL 到期（详情 30s、评论 20s）。

func (s *CachedStore) ApplyReaction(ctx context.Context, input ApplyReactionRecord) (ReactionState, error) {
	store, ok := s.Store.(ReactionStore)
	if !ok {
		return ReactionState{}, ErrInvalidReactionTarget
	}
	scopes := s.topicListScopes(ctx, input.TopicID)
	state, err := store.ApplyReaction(ctx, input)
	if err != nil {
		return ReactionState{}, err
	}
	s.invalidateTopicDetail(ctx, input.TopicID)
	s.invalidateTopicsScoped(ctx, scopes)
	if input.TargetType == ReactionTargetComment {
		s.invalidateComments(ctx, input.TopicID)
	}
	return state, nil
}

func (s *CachedStore) LikedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error) {
	store, ok := s.Store.(ReactionStore)
	if !ok {
		return map[int64]struct{}{}, nil
	}
	return store.LikedTargetIDs(ctx, viewerUserID, targetType, targetIDs)
}

func (s *CachedStore) ListReactionUsers(ctx context.Context, targetType string, targetID int64, limit, offset int) (ReactionRoster, error) {
	store, ok := s.Store.(ReactionStore)
	if !ok {
		return ReactionRoster{Items: []UserSummary{}}, nil
	}
	return store.ListReactionUsers(ctx, targetType, targetID, limit, offset)
}

// ReactionPreviews 纯读、actor 无关装饰的数据源：直接转发内层，不进缓存。
func (s *CachedStore) ReactionPreviews(ctx context.Context, targetType string, targetIDs []int64, viewerUserID int64, limit int) (map[int64][]UserSummary, error) {
	store, ok := s.Store.(ReactionPreviewStore)
	if !ok {
		return map[int64][]UserSummary{}, nil
	}
	return store.ReactionPreviews(ctx, targetType, targetIDs, viewerUserID, limit)
}
