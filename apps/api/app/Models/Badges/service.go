package badges

import (
	"context"
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
	// progress 是规则进度的测量能力（bootstrap 注入 `*PostgresStore`）。
	//
	// 它是**可选**的：nil 时进度为空、不报错。Service 本身没有 pool，也不该有——
	// 测量是存储层的批量查询，塞进领域层会让这里出现 SQL 与 N+1。
	progress ProgressMeasurer
}

func NewService(store Store, activity ActivityReader) *Service {
	return &Service{store: store, activity: activity}
}

// SetProgressMeasurer 装配规则进度测量（bootstrap 在 NewService 之后调用）。
//
// `NewService` 的 activity 参数与字段保持不变（外部装配与测试仍按该签名构造），
// 但它**不再**是徽章进度的来源：进度只认规则。未装配测量器时进度为空、不报错。
func (s *Service) SetProgressMeasurer(measurer ProgressMeasurer) {
	if s == nil {
		return
	}
	s.progress = measurer
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
	enabled := make([]Badge, 0, len(badges))
	for _, badge := range badges {
		if !badge.Enabled {
			continue
		}
		enabled = append(enabled, badge)
	}
	// 名字一次批量解析：签名里没有 ctx/store，逐条查库在这里不可能发生。
	names := BadgeRefNames(ctx, RefNameLoaderOf(s.store), enabled)
	items := make([]PublicBadge, 0, len(enabled))
	for _, badge := range enabled {
		items = append(items, badge.Public(names))
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
// 进度只来自规则测量（一次批量 `Prepare`，绝不按徽章逐条查）：legacy 的
// metric/threshold 是只读遗留列，新规则徽章这两列是 NULL，读它们只会得到
// 「键在值空」的进度条。
func (s *Service) ListUserBadges(ctx context.Context, userID int64) ([]BadgeGroupSection, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	userBadges, err := s.store.ListUserBadges(ctx, userID)
	if err != nil {
		return nil, err
	}
	measured := s.measureProgress(ctx, userID, userBadges)
	for i := range userBadges {
		// 一律覆盖：没有测量结果的行必须是零值。留任何 legacy 阈值都会让
		// 「0/7」这种假进度条出现在规则徽章上（本仓的历史坑）。
		userBadges[i].Progress = measured[userBadges[i].Badge.Slug]
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return buildSections(groups, userBadges), nil
}

// measureProgress 把「未获得 + 有 Measure」的徽章交给测量器，返回 slug → 进度。
//
// 已获得的不测：DTO 对 earned 行根本不输出进度键，测了只是白跑。
// 未装配测量器 / 匿名 / 没有可测徽章时返回 nil（读 map 的零值仍然安全）。
func (s *Service) measureProgress(ctx context.Context, userID int64, items []UserBadge) map[string]BadgeProgress {
	if s == nil || s.progress == nil || userID <= 0 || len(items) == 0 {
		return nil
	}
	measures := make(map[string]ProgressMeasure, len(items))
	for i := range items {
		if items[i].Earned || items[i].Badge.Measure == nil {
			continue
		}
		measures[items[i].Badge.Slug] = *items[i].Badge.Measure
	}
	if len(measures) == 0 {
		return nil
	}
	return s.progress.MeasureProgress(ctx, userID, measures)
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

// TopBadgesFor 批量取用户**当前展示**的徽章（一次查询，无 N+1）。
//
// 当前无生产调用方：论坛读模型的徽章由 Forum 的读 SQL 直接投影（topBadgeSelectSQL），
// 不再经本包补齐。保留为 store 能力（后台/插件或未来的批量读）。
func (s *Service) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	if s == nil || s.store == nil {
		return map[int64]BadgeRef{}, nil
	}
	return s.store.TopBadgesFor(ctx, userIDs)
}
