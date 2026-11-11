package badges

import (
	"context"
	"fmt"
	"sort"
)

// ActivityReader 读用户活动统计（M1 的 ActivityStore 满足它）。
// Service 只需要读，不接受写能力。
type ActivityReader interface {
	GetCounters(ctx context.Context, userID int64) (ActivityCounters, error)
}

// Service 是公开面（资料页徽章墙 + 帖子旁最高级徽章）。
type Service struct {
	store    Store
	activity ActivityReader
}

func NewService(store Store, activity ActivityReader) *Service {
	return &Service{store: store, activity: activity}
}

// ListBadges 返回启用中的徽章（公开面）。
func (s *Service) ListBadges(ctx context.Context) ([]PublicBadge, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	badges, err := s.store.ListBadges(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]PublicBadge, 0, len(badges))
	for _, badge := range badges {
		if !badge.Enabled {
			continue
		}
		items = append(items, badge.Public())
	}
	return items, nil
}

// ListGroups 返回分组（公开面只要外壳字段，BadgeCount 由 admin 面使用）。
func (s *Service) ListGroups(ctx context.Context) ([]BadgeGroup, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.ListGroups(ctx)
}

// ListUserBadges 返回资料页徽章墙：按分组分区，每格带「是否获得 + 进度」。
//
// 进度取自 user_activity 的一次读取，绝不按徽章逐条查（那就是 N+1）。
func (s *Service) ListUserBadges(ctx context.Context, userID int64) ([]BadgeGroupSection, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	userBadges, err := s.store.ListUserBadges(ctx, userID)
	if err != nil {
		return nil, err
	}
	var counters ActivityCounters
	if s.activity != nil && userID > 0 {
		counters, err = s.activity.GetCounters(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("load activity counters: %w", err)
		}
	}
	for i := range userBadges {
		userBadges[i].Progress = progressFor(userBadges[i].Badge, counters)
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return buildSections(groups, userBadges), nil
}

// progressFor 计算一格进度。手动徽章没有阈值，进度恒为 (0,0)。
func progressFor(badge PublicBadge, counters ActivityCounters) BadgeProgress {
	if badge.Threshold == nil || *badge.Threshold <= 0 || badge.Metric == nil {
		return BadgeProgress{}
	}
	return BadgeProgress{
		Current:   counters.Value(*badge.Metric),
		Threshold: *badge.Threshold,
	}
}

// buildSections 按分组位置把徽章装进分区。
//
// 分组顺序与 admin 面一致（position 升序）；未挂分组的徽章统一进末尾的
// 「未分组」分区，保证它们仍然可见——悄悄丢掉比排错位置更糟。
func buildSections(groups []BadgeGroup, items []UserBadge) []BadgeGroupSection {
	// 排序在领域层再做一次：分组顺序是契约（前端 tab 顺序），不能只依赖
	// 某条 SQL 的 ORDER BY——换存储或加过滤条件时它很容易被漏掉。
	ordered := append([]BadgeGroup(nil), groups...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Position != ordered[j].Position {
			return ordered[i].Position < ordered[j].Position
		}
		return ordered[i].ID < ordered[j].ID
	})
	groups = ordered

	sections := make([]BadgeGroupSection, 0, len(groups)+1)
	index := make(map[string]int, len(groups))
	for _, group := range groups {
		index[group.Key] = len(sections)
		sections = append(sections, BadgeGroupSection{
			Key:         group.Key,
			Name:        group.Name,
			Description: group.Description,
			Position:    group.Position,
			Badges:      make([]UserBadge, 0, 4),
		})
	}
	ungrouped := -1
	for _, item := range items {
		key := ""
		if item.Badge.GroupKey != nil {
			key = *item.Badge.GroupKey
		}
		position, ok := index[key]
		if !ok {
			if ungrouped < 0 {
				ungrouped = len(sections)
				sections = append(sections, BadgeGroupSection{
					Key:    "",
					Name:   "未分组",
					Badges: make([]UserBadge, 0, 4),
				})
			}
			position = ungrouped
		}
		sections[position].Badges = append(sections[position].Badges, item)
	}
	// 空分组不返回：资料页出现空壳 tab 只会让人以为加载失败。
	kept := sections[:0]
	for _, section := range sections {
		if len(section.Badges) > 0 {
			kept = append(kept, section)
		}
	}
	return kept
}

// TopBadgesFor 批量取整页用户的最高级徽章（一次查询，无 N+1）。
func (s *Service) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	if s == nil || s.store == nil {
		return map[int64]BadgeRef{}, nil
	}
	return s.store.TopBadgesFor(ctx, userIDs)
}
