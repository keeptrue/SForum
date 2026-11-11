package badges

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeStore 是 Store 的内存实现，供领域单测使用。
//
// 它刻意复刻两条真实语义：授予幂等（ON CONFLICT DO NOTHING）与
// top_badge_id 排序（gold > silver > bronze，同级 position 升序、granted_at 降序）。
// 排序复刻是必要的：那是本包最容易写错、也最难在集成测试里定位的一条规则。
type fakeStore struct {
	badges    []Badge
	groups    []BadgeGroup
	nextBadge int64
	nextGroup int64
	// grants: userID -> badgeID -> grantedAt
	grants map[int64]map[int64]time.Time
	top    map[int64]int64
	// grantOrder 记录授予先后，用于 granted_at 降序的同秒排序兜底。
	grantOrder map[int64]map[int64]int
	orderSeq   int
	// activityCounters 供 GrantFromRules 的假实现使用（真实现读 user_activity 表）。
	activityCounters map[int64]ActivityCounters
	lastGrants       []BadgeGrantRecord
	// catalogErr 让公开目录读失败（B-11 的 fail-closed 判据）。
	catalogErr error
	// catalogReads / grantReads 计数用于钉住「匿名不碰 user_badges」。
	catalogReads int
	grantReads   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		nextBadge:        1,
		nextGroup:        1,
		grants:           map[int64]map[int64]time.Time{},
		top:              map[int64]int64{},
		grantOrder:       map[int64]map[int64]int{},
		activityCounters: map[int64]ActivityCounters{},
	}
}

func (f *fakeStore) ListBadges(context.Context) ([]Badge, error) {
	return append([]Badge(nil), f.badges...), nil
}

func (f *fakeStore) ListGroups(context.Context) ([]BadgeGroup, error) {
	return append([]BadgeGroup(nil), f.groups...), nil
}

// ListPublicCatalog 复刻真查询的两条语义：SQL 层过滤 enabled、按
// (组 position, 组 id, 徽章 position, 徽章 id) 排序。
//
// 排序复刻只是为了单测能断言形状；B-04（排序稳定）的**真正判据**是真 Postgres
// 上的集成测试——假实现自己排序，等于拿被测逻辑证明它自己。
func (f *fakeStore) ListPublicCatalog(_ context.Context, limit int) ([]PublicCatalogEntry, bool, error) {
	f.catalogReads++
	if f.catalogErr != nil {
		return nil, false, f.catalogErr
	}
	groups := make(map[int64]BadgeGroup, len(f.groups))
	for _, group := range f.groups {
		groups[group.ID] = group
	}
	rows := make([]PublicCatalogEntry, 0, len(f.badges))
	keys := make([]catalogSortKey, 0, len(f.badges))
	for _, badge := range f.badges {
		if !badge.Enabled {
			continue
		}
		key := catalogSortKey{groupPosition: 2147483647, badgePosition: badge.Position, badgeID: badge.ID}
		var groupKey, groupName string
		if badge.GroupID != nil {
			if group, ok := groups[*badge.GroupID]; ok {
				key.groupPosition = group.Position
				key.groupID = group.ID
				groupKey = group.Key
				groupName = group.Name
			}
		}
		rows = append(rows, PublicCatalogEntry{
			Slug: badge.Slug, Name: badge.Name, Description: badge.Description,
			Icon: badge.Icon, Tier: badge.Tier, GroupKey: groupKey, GroupName: groupName,
		})
		keys = append(keys, key)
	}
	order := make([]int, len(rows))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool {
		return catalogSortKeyLess(keys[order[i]], keys[order[j]])
	})
	items := make([]PublicCatalogEntry, 0, len(rows))
	for _, index := range order {
		items = append(items, rows[index])
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

type catalogSortKey struct {
	groupPosition int
	groupID       int64
	badgePosition int
	badgeID       int64
}

// catalogSortKeyLess 对应 SQL 的
// COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST,
// badges.position, badges.id。
func catalogSortKeyLess(left, right catalogSortKey) bool {
	if left.groupPosition != right.groupPosition {
		return left.groupPosition < right.groupPosition
	}
	if left.groupID != right.groupID {
		if left.groupID == 0 {
			return false
		}
		if right.groupID == 0 {
			return true
		}
		return left.groupID < right.groupID
	}
	if left.badgePosition != right.badgePosition {
		return left.badgePosition < right.badgePosition
	}
	return left.badgeID < right.badgeID
}

// ListEarnedBadgeSlugs 复刻真查询：只看启用徽章，只给该用户。
func (f *fakeStore) ListEarnedBadgeSlugs(_ context.Context, userID int64) (map[string]struct{}, error) {
	f.grantReads++
	earned := make(map[string]struct{})
	grants, ok := f.grants[userID]
	if !ok {
		return earned, nil
	}
	for _, badge := range f.badges {
		if _, granted := grants[badge.ID]; granted && badge.Enabled {
			earned[badge.Slug] = struct{}{}
		}
	}
	return earned, nil
}

func (f *fakeStore) GetBadge(_ context.Context, badgeID int64) (Badge, error) {
	for _, badge := range f.badges {
		if badge.ID == badgeID {
			return badge, nil
		}
	}
	return Badge{}, ErrBadgeNotFound
}

func (f *fakeStore) GetBadgeBySlug(_ context.Context, slug string) (Badge, error) {
	for _, badge := range f.badges {
		if badge.Slug == slug {
			return badge, nil
		}
	}
	return Badge{}, ErrBadgeNotFound
}

func (f *fakeStore) CreateBadge(_ context.Context, input BadgeInput) (Badge, error) {
	badge := Badge{
		ID: f.nextBadge, Slug: input.Slug, Name: input.Name, Description: input.Description,
		GroupID: input.GroupID, Tier: input.Tier, Icon: input.Icon, GrantMode: input.GrantMode,
		Metric: input.Metric, Threshold: input.Threshold, Enabled: input.Enabled, Position: input.Position,
	}
	f.nextBadge++
	f.badges = append(f.badges, badge)
	return badge, nil
}

func (f *fakeStore) UpdateBadge(_ context.Context, badgeID int64, input BadgeInput) (Badge, error) {
	for i := range f.badges {
		if f.badges[i].ID != badgeID {
			continue
		}
		f.badges[i].Slug = input.Slug
		f.badges[i].Name = input.Name
		f.badges[i].Description = input.Description
		f.badges[i].GroupID = input.GroupID
		f.badges[i].Tier = input.Tier
		f.badges[i].Icon = input.Icon
		f.badges[i].GrantMode = input.GrantMode
		f.badges[i].Metric = input.Metric
		f.badges[i].Threshold = input.Threshold
		f.badges[i].Enabled = input.Enabled
		f.badges[i].Position = input.Position
		return f.badges[i], nil
	}
	return Badge{}, ErrBadgeNotFound
}

func (f *fakeStore) DeleteBadge(_ context.Context, badgeID int64) error {
	for i := range f.badges {
		if f.badges[i].ID == badgeID {
			f.badges = append(f.badges[:i], f.badges[i+1:]...)
			return nil
		}
	}
	return ErrBadgeNotFound
}

func (f *fakeStore) CreateGroup(_ context.Context, input BadgeGroupInput) (BadgeGroup, error) {
	group := BadgeGroup{
		ID: f.nextGroup, Key: input.Key, Name: input.Name,
		Description: input.Description, Position: input.Position,
	}
	f.nextGroup++
	f.groups = append(f.groups, group)
	return group, nil
}

func (f *fakeStore) UpdateGroup(_ context.Context, groupID int64, input BadgeGroupInput) (BadgeGroup, error) {
	for i := range f.groups {
		if f.groups[i].ID == groupID {
			f.groups[i].Key = input.Key
			f.groups[i].Name = input.Name
			f.groups[i].Description = input.Description
			f.groups[i].Position = input.Position
			return f.groups[i], nil
		}
	}
	return BadgeGroup{}, ErrBadgeNotFound
}

func (f *fakeStore) DeleteGroup(_ context.Context, groupID int64) error {
	for i := range f.groups {
		if f.groups[i].ID == groupID {
			f.groups = append(f.groups[:i], f.groups[i+1:]...)
			return nil
		}
	}
	return ErrBadgeNotFound
}

func (f *fakeStore) ListUserBadges(_ context.Context, userID int64) ([]UserBadge, error) {
	items := make([]UserBadge, 0, len(f.badges))
	for _, badge := range f.badges {
		if !badge.Enabled {
			continue
		}
		item := UserBadge{Badge: badge.Public()}
		if grantedAt, ok := f.grants[userID][badge.ID]; ok {
			at := grantedAt
			item.GrantedAt = &at
			item.Earned = true
		}
		if badge.Threshold != nil {
			item.Progress.Threshold = *badge.Threshold
		}
		items = append(items, item)
	}
	return items, nil
}

func (f *fakeStore) ListGrants(_ context.Context, input GrantListInput) (GrantList, error) {
	items := make([]BadgeGrantRecord, 0, len(f.lastGrants))
	for _, record := range f.lastGrants {
		if input.UserID > 0 && record.UserID != input.UserID {
			continue
		}
		if input.BadgeID > 0 && record.BadgeID != input.BadgeID {
			continue
		}
		items = append(items, record)
	}
	return GrantList{Items: items, Total: int64(len(items))}, nil
}

func (f *fakeStore) TopBadgesFor(_ context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	result := make(map[int64]BadgeRef, len(userIDs))
	for _, userID := range userIDs {
		badgeID, ok := f.top[userID]
		if !ok {
			continue
		}
		for _, badge := range f.badges {
			if badge.ID == badgeID && badge.Enabled {
				result[userID] = BadgeRef{Slug: badge.Slug, Name: badge.Name, Tier: badge.Tier, Icon: badge.Icon}
			}
		}
	}
	return result, nil
}

func (f *fakeStore) ListEnabledAutomaticBadges(context.Context) ([]Badge, error) {
	items := make([]Badge, 0, len(f.badges))
	for _, badge := range f.badges {
		if badge.Enabled && badge.GrantMode == GrantAutomatic {
			items = append(items, badge)
		}
	}
	return items, nil
}

func (f *fakeStore) AffectedUserIDs(_ context.Context, badgeID int64) ([]int64, error) {
	var ids []int64
	for userID, grants := range f.grants {
		if _, ok := grants[badgeID]; ok {
			ids = append(ids, userID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (f *fakeStore) GrantAutomatic(_ context.Context, userID int64, badgeID int64) (bool, error) {
	return f.insertGrant(userID, badgeID, 0, ""), nil
}

func (f *fakeStore) GrantManual(_ context.Context, input GrantInput) (bool, error) {
	if !f.insertGrant(input.UserID, input.BadgeID, input.GrantedBy, input.GrantReason) {
		return false, ErrBadgeAlreadyGranted
	}
	return true, nil
}

func (f *fakeStore) insertGrant(userID, badgeID, grantedBy int64, reason string) bool {
	if f.grants[userID] == nil {
		f.grants[userID] = map[int64]time.Time{}
		f.grantOrder[userID] = map[int64]int{}
	}
	if _, exists := f.grants[userID][badgeID]; exists {
		return false
	}
	f.orderSeq++
	f.grants[userID][badgeID] = time.Unix(int64(f.orderSeq), 0).UTC()
	f.grantOrder[userID][badgeID] = f.orderSeq
	slug := ""
	for _, badge := range f.badges {
		if badge.ID == badgeID {
			slug = badge.Slug
		}
	}
	var by *int64
	if grantedBy > 0 {
		value := grantedBy
		by = &value
	}
	f.lastGrants = append(f.lastGrants, BadgeGrantRecord{
		UserID: userID, BadgeID: badgeID, BadgeSlug: slug,
		GrantedAt: f.grants[userID][badgeID], GrantedBy: by, GrantReason: reason,
	})
	return true
}

func (f *fakeStore) RevokeManual(_ context.Context, userID int64, badgeID int64) (bool, error) {
	if f.grants[userID] == nil {
		return false, nil
	}
	if _, ok := f.grants[userID][badgeID]; !ok {
		return false, nil
	}
	delete(f.grants[userID], badgeID)
	return true, nil
}

func (f *fakeStore) RecomputeTopBadge(_ context.Context, userID int64) error {
	var best *Badge
	var bestOrder int
	for badgeID, grantedAt := range f.grants[userID] {
		for _, badge := range f.badges {
			if badge.ID != badgeID || !badge.Enabled {
				continue
			}
			order := f.grantOrder[userID][badgeID]
			_ = grantedAt
			if best == nil || betterTopBadge(badge, order, *best, bestOrder) {
				candidate := badge
				best = &candidate
				bestOrder = order
			}
		}
	}
	if best == nil {
		delete(f.top, userID)
		return nil
	}
	f.top[userID] = best.ID
	return nil
}

// betterTopBadge 复刻 SQL 的排序：tier 权重降序 → position 升序 → granted_at 降序。
func betterTopBadge(candidate Badge, candidateOrder int, current Badge, currentOrder int) bool {
	if candidate.Tier.Weight() != current.Tier.Weight() {
		return candidate.Tier.Weight() > current.Tier.Weight()
	}
	if candidate.Position != current.Position {
		return candidate.Position < current.Position
	}
	return candidateOrder > currentOrder
}

// GrantFromRules 复刻批量 SQL 的语义：启用自动徽章 + 活动计数达阈值 → 授予。
func (f *fakeStore) GrantFromRules(ctx context.Context) (int64, error) {
	var granted int64
	rules, _ := f.ListEnabledAutomaticBadges(ctx)
	for userID := range f.activityCounters {
		for _, rule := range rules {
			if ruleSatisfied(rule, f.activityCounters[userID]) {
				if f.insertGrant(userID, rule.ID, 0, "recompute") {
					granted++
				}
			}
		}
	}
	return granted, nil
}

func (f *fakeStore) RecomputeAll(ctx context.Context) (int64, error) {
	var changed int64
	seen := map[int64]struct{}{}
	for userID := range f.grants {
		seen[userID] = struct{}{}
	}
	for userID := range f.top {
		seen[userID] = struct{}{}
	}
	for userID := range seen {
		before, hadBefore := f.top[userID]
		if err := f.RecomputeTopBadge(ctx, userID); err != nil {
			return changed, err
		}
		after, hasAfter := f.top[userID]
		if hadBefore != hasAfter || before != after {
			changed++
		}
	}
	return changed, nil
}

// fakeActivity 是 ActivityReader 的内存实现。
type fakeActivity struct {
	counters map[int64]ActivityCounters
}

func newFakeActivity() *fakeActivity {
	return &fakeActivity{counters: map[int64]ActivityCounters{}}
}

func (f *fakeActivity) GetCounters(_ context.Context, userID int64) (ActivityCounters, error) {
	return f.counters[userID], nil
}

var (
	_ Store          = (*fakeStore)(nil)
	_ ActivityReader = (*fakeActivity)(nil)
	_                = pgx.ErrNoRows
)
