package badges

import "context"

// Store 按能力拆成四个窄接口，组合成完整存储。
//
// 拆分的理由不是美观：Service 只需要读面，Evaluator 只需要授予面，
// AdminService 需要目录写面。让每个调用方只依赖它真正用到的那一块，
// 才能在测试里用最小假实现替换，也避免再长出一个 God interface。
type Store interface {
	CatalogReader
	CatalogWriter
	GrantReader
	GrantWriter
}

// CatalogReader 读徽章目录与分组。
type CatalogReader interface {
	ListBadges(ctx context.Context) ([]Badge, error)
	ListGroups(ctx context.Context) ([]BadgeGroup, error)
	GetBadge(ctx context.Context, badgeID int64) (Badge, error)
	GetBadgeBySlug(ctx context.Context, slug string) (Badge, error)
}

// CatalogWriter 维护徽章目录与分组（admin 面）。
type CatalogWriter interface {
	CreateBadge(ctx context.Context, input BadgeInput) (Badge, error)
	UpdateBadge(ctx context.Context, badgeID int64, input BadgeInput) (Badge, error)
	DeleteBadge(ctx context.Context, badgeID int64) error
	CreateGroup(ctx context.Context, input BadgeGroupInput) (BadgeGroup, error)
	UpdateGroup(ctx context.Context, groupID int64, input BadgeGroupInput) (BadgeGroup, error)
	DeleteGroup(ctx context.Context, groupID int64) error
}

// GrantReader 读授予关系与最高级徽章。
type GrantReader interface {
	ListUserBadges(ctx context.Context, userID int64) ([]UserBadge, error)
	ListGrants(ctx context.Context, input GrantListInput) (GrantList, error)
	// TopBadgesFor 一次查询覆盖整页用户，禁止 N+1。
	TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error)
	ListEnabledAutomaticBadges(ctx context.Context) ([]Badge, error)
	// AffectedUserIDs 返回持有该徽章的用户（停用/规则变更后要重算 top_badge_id）。
	AffectedUserIDs(ctx context.Context, badgeID int64) ([]int64, error)
}

// GrantWriter 写授予关系并维护 users.top_badge_id。
type GrantWriter interface {
	// GrantAutomatic 幂等授予；返回 true 表示这次真的插入了新行。
	GrantAutomatic(ctx context.Context, userID int64, badgeID int64) (bool, error)
	GrantManual(ctx context.Context, input GrantInput) (bool, error)
	RevokeManual(ctx context.Context, userID int64, badgeID int64) (bool, error)
	RecomputeTopBadge(ctx context.Context, userID int64) error
	RecomputeAll(ctx context.Context) (int64, error)
	// GrantFromRules 按启用规则批量补授予（阈值下调后的追溯），返回新插入行数。
	GrantFromRules(ctx context.Context) (int64, error)
}

// BadgeInput 是创建/更新徽章的入参。
//
// 指针字段区分「未提供」与「显式置空」：manual 徽章的 Metric / Threshold 必须为 nil。
type BadgeInput struct {
	Slug        string
	Name        string
	Description string
	GroupID     *int64
	Tier        BadgeTier
	Icon        string
	GrantMode   GrantMode
	Metric      *ActivityMetric
	Threshold   *int64
	Enabled     bool
	Position    int
}

// BadgeGroupInput 是创建/更新分组的入参。
type BadgeGroupInput struct {
	Key         string
	Name        string
	Description string
	Position    int
}

// GrantInput 是手动授予的入参。
type GrantInput struct {
	UserID      int64
	BadgeID     int64
	GrantedBy   int64
	GrantReason string
}

// GrantListInput 是授予记录列表的查询条件；零值字段表示不过滤。
type GrantListInput struct {
	UserID  int64
	BadgeID int64
	Limit   int
	Offset  int
}

// GrantList 是授予记录分页结果。
type GrantList struct {
	Items []BadgeGrantRecord
	Total int64
}
