package badgescontroller

import (
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 本文件只做「领域结构 → 扁平 JSON」的映射，不写任何业务逻辑：
// 不排序、不算进度、不判可见性。那些都在领域层，改这里不该改变行为。

// profileBadgeItem 是资料页徽章墙上的一格（扁平形状，前端已按此实现）。
//
// 指针字段表达「没有这个键」而不是「值是 0」：
//   - current / threshold 只在未获得的自动徽章上出现（进度条要用）；
//     已获得条目不输出这两个键。current=0 是有效值，用 omitempty 会把它吃掉。
//   - grantedAt 只在 earned=true 时出现，未获得条目不返回 null。
type profileBadgeItem struct {
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	Tier      string     `json:"tier"`
	Icon      string     `json:"icon"`
	Position  int        `json:"position"`
	Earned    bool       `json:"earned"`
	Current   *int64     `json:"current,omitempty"`
	Threshold *int64     `json:"threshold,omitempty"`
	GrantedAt *time.Time `json:"grantedAt,omitempty"`
}

type profileBadgeGroup struct {
	Key      string             `json:"key"`
	Name     string             `json:"name"`
	Position int                `json:"position"`
	Badges   []profileBadgeItem `json:"badges"`
}

type profileBadgesResponse struct {
	Groups []profileBadgeGroup `json:"groups"`
}

// toProfileBadges 把领域分区映射成前端契约形状。
func toProfileBadges(sections []badges.BadgeGroupSection) profileBadgesResponse {
	groups := make([]profileBadgeGroup, 0, len(sections))
	for _, section := range sections {
		items := make([]profileBadgeItem, 0, len(section.Badges))
		for _, entry := range section.Badges {
			items = append(items, toProfileBadgeItem(entry))
		}
		groups = append(groups, profileBadgeGroup{
			Key:      section.Key,
			Name:     section.Name,
			Position: section.Position,
			Badges:   items,
		})
	}
	return profileBadgesResponse{Groups: groups}
}

func toProfileBadgeItem(entry badges.UserBadge) profileBadgeItem {
	item := profileBadgeItem{
		Slug:     entry.Badge.Slug,
		Name:     entry.Badge.Name,
		Tier:     string(entry.Badge.Tier),
		Icon:     entry.Badge.Icon,
		Position: entry.Badge.Position,
		Earned:   entry.Earned,
	}
	if entry.Earned {
		// 已获得：只给 grantedAt，不给进度条数据。
		item.GrantedAt = entry.GrantedAt
		return item
	}
	// 未获得：只有自动徽章有阈值，手动徽章两者都不输出。
	if entry.Badge.GrantMode == badges.GrantAutomatic && entry.Progress.Threshold > 0 {
		current := entry.Progress.Current
		threshold := entry.Progress.Threshold
		item.Current = &current
		item.Threshold = &threshold
	}
	return item
}

// adminBadgeResponse 是后台列表的一行：公开面不得出现这些字段。
type adminBadgeResponse struct {
	ID           int64     `json:"id"`
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	GroupID      *int64    `json:"groupId"`
	GroupName    *string   `json:"groupName"`
	Tier         string    `json:"tier"`
	Icon         string    `json:"icon"`
	GrantMode    string    `json:"grantMode"`
	Metric       *string   `json:"metric"`
	Threshold    *int64    `json:"threshold"`
	Enabled      bool      `json:"enabled"`
	Position     int       `json:"position"`
	GrantedCount int64     `json:"grantedCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func toAdminBadge(badge badges.Badge) adminBadgeResponse {
	var metric *string
	if badge.Metric != nil {
		value := string(*badge.Metric)
		metric = &value
	}
	return adminBadgeResponse{
		ID: badge.ID, Slug: badge.Slug, Name: badge.Name, Description: badge.Description,
		GroupID: badge.GroupID, GroupName: badge.GroupName,
		Tier: string(badge.Tier), Icon: badge.Icon, GrantMode: string(badge.GrantMode),
		Metric: metric, Threshold: badge.Threshold, Enabled: badge.Enabled,
		Position: badge.Position, GrantedCount: badge.GrantedCount,
		CreatedAt: badge.CreatedAt, UpdatedAt: badge.UpdatedAt,
	}
}

func toAdminBadges(items []badges.Badge) []adminBadgeResponse {
	out := make([]adminBadgeResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toAdminBadge(item))
	}
	return out
}

type adminBadgeGroupResponse struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	BadgeCount  int64     `json:"badgeCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func toAdminBadgeGroup(group badges.BadgeGroup) adminBadgeGroupResponse {
	return adminBadgeGroupResponse{
		ID: group.ID, Key: group.Key, Name: group.Name, Description: group.Description,
		Position: group.Position, BadgeCount: group.BadgeCount,
		CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
	}
}

func toAdminBadgeGroups(items []badges.BadgeGroup) []adminBadgeGroupResponse {
	out := make([]adminBadgeGroupResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toAdminBadgeGroup(item))
	}
	return out
}
