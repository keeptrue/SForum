package badges

import (
	"context"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 权限键（与 Identity/seeds.go、迁移 202611100003 逐字一致）。
const (
	PermissionBadgeView   = identity.PermissionBadgeView
	PermissionBadgeManage = identity.PermissionBadgeManage
)

// requireBadgePermission 是 admin 面唯一的授权入口。
//
// 为什么放在领域层而不是 handler：AGENTS.md 要求「domain services own
// authorization-aware business workflows」。放在 handler 里，任何新增的调用方
// （CLI、job、插件适配器）都会绕过它；放在这里，越权在领域边界就被挡住。
//
// super_admin 走角色旁路：它的权限集合不保证逐条展开，用角色判断更稳。
func requireBadgePermission(actor identity.Actor, permission string) error {
	if actor.ID <= 0 {
		return identity.ErrPermissionDenied
	}
	if actor.Permissions[permission] {
		return nil
	}
	for _, role := range actor.RoleKeys {
		if role == identity.RoleSuperAdmin {
			return nil
		}
	}
	return identity.ErrPermissionDenied
}

// AdminService 是运营面：目录 CRUD、规则校验、手动授予/收回、授予记录、全量重算。
//
// 它返回 Badge（含 enabled / grantedCount / 审计字段），与公开面的 PublicBadge
// 刻意分离——两条面共用一个结构再靠 omitempty 遮字段，迟早漏字段。
type AdminService struct {
	store Store
}

func NewAdminService(store Store) *AdminService {
	return &AdminService{store: store}
}

// ListBadges 返回全部徽章（含停用）。
func (a *AdminService) ListBadges(ctx context.Context, actor identity.Actor) ([]Badge, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	if a == nil || a.store == nil {
		return nil, nil
	}
	return a.store.ListBadges(ctx)
}

// GetBadge 读单个徽章（后台编辑弹窗要用旧值做合并）。
func (a *AdminService) GetBadge(ctx context.Context, actor identity.Actor, badgeID int64) (Badge, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return Badge{}, err
	}
	if a == nil || a.store == nil {
		return Badge{}, ErrBadgesDisabled
	}
	return a.store.GetBadge(ctx, badgeID)
}

// ListGroups 返回全部分组。
func (a *AdminService) ListGroups(ctx context.Context, actor identity.Actor) ([]BadgeGroup, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	if a == nil || a.store == nil {
		return nil, nil
	}
	return a.store.ListGroups(ctx)
}

// CreateBadge 校验后创建徽章。
func (a *AdminService) CreateBadge(ctx context.Context, actor identity.Actor, input BadgeInput) (Badge, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return Badge{}, err
	}
	if a == nil || a.store == nil {
		return Badge{}, ErrBadgesDisabled
	}
	normalized, err := validateBadgeInput(input)
	if err != nil {
		return Badge{}, err
	}
	return a.store.CreateBadge(ctx, normalized)
}

// UpdateBadge 校验后更新徽章；停用或规则变更时重算受影响用户的最高级徽章。
//
// 为什么停用也要重算：已授予行保留（历史不抹），但 top_badge_id 指向一个停用徽章
// 会让帖子旁继续展示它。ON DELETE SET NULL 只覆盖删除，覆盖不到停用。
func (a *AdminService) UpdateBadge(ctx context.Context, actor identity.Actor, badgeID int64, input BadgeInput) (Badge, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return Badge{}, err
	}
	if a == nil || a.store == nil {
		return Badge{}, ErrBadgesDisabled
	}
	normalized, err := validateBadgeInput(input)
	if err != nil {
		return Badge{}, err
	}
	previous, err := a.store.GetBadge(ctx, badgeID)
	if err != nil {
		return Badge{}, err
	}
	updated, err := a.store.UpdateBadge(ctx, badgeID, normalized)
	if err != nil {
		return Badge{}, err
	}
	if topBadgeAffected(previous, updated) {
		if err := a.recomputeAffected(ctx, badgeID); err != nil {
			return Badge{}, err
		}
	}
	return updated, nil
}

// DeleteBadge 删除徽章；user_badges 级联删除，users.top_badge_id 由外键置空。
func (a *AdminService) DeleteBadge(ctx context.Context, actor identity.Actor, badgeID int64) error {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrBadgesDisabled
	}
	return a.store.DeleteBadge(ctx, badgeID)
}

// CreateGroup 创建分组。
func (a *AdminService) CreateGroup(ctx context.Context, actor identity.Actor, input BadgeGroupInput) (BadgeGroup, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return BadgeGroup{}, err
	}
	if a == nil || a.store == nil {
		return BadgeGroup{}, ErrBadgesDisabled
	}
	normalized, err := validateGroupInput(input)
	if err != nil {
		return BadgeGroup{}, err
	}
	return a.store.CreateGroup(ctx, normalized)
}

// UpdateGroup 更新分组。
func (a *AdminService) UpdateGroup(ctx context.Context, actor identity.Actor, groupID int64, input BadgeGroupInput) (BadgeGroup, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return BadgeGroup{}, err
	}
	if a == nil || a.store == nil {
		return BadgeGroup{}, ErrBadgesDisabled
	}
	normalized, err := validateGroupInput(input)
	if err != nil {
		return BadgeGroup{}, err
	}
	return a.store.UpdateGroup(ctx, groupID, normalized)
}

// DeleteGroup 删除分组；组内徽章的 group_id 由外键 SET NULL 落成未分组。
func (a *AdminService) DeleteGroup(ctx context.Context, actor identity.Actor, groupID int64) error {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrBadgesDisabled
	}
	return a.store.DeleteGroup(ctx, groupID)
}

// Grant 手动授予。
//
// 自动徽章拒绝手动授予：规则会立刻重新评估，手动授予没有意义且会造成
// 「已授予但规则不满足」的假状态。
func (a *AdminService) Grant(ctx context.Context, actor identity.Actor, input GrantInput) error {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrBadgesDisabled
	}
	badge, err := a.store.GetBadge(ctx, input.BadgeID)
	if err != nil {
		return err
	}
	if badge.GrantMode != GrantManual {
		return ErrBadgeManualOnly
	}
	if _, err := a.store.GrantManual(ctx, input); err != nil {
		return err
	}
	return a.store.RecomputeTopBadge(ctx, input.UserID)
}

// Revoke 手动收回。
//
// 自动徽章拒绝收回：规则下一次评估就会重新授予，收回只是短暂假象。
// 想撤销自动徽章的效果，正确做法是停用它（停用后不再评估，已授予行保留）。
func (a *AdminService) Revoke(ctx context.Context, actor identity.Actor, userID int64, badgeID int64) (bool, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return false, err
	}
	if a == nil || a.store == nil {
		return false, ErrBadgesDisabled
	}
	badge, err := a.store.GetBadge(ctx, badgeID)
	if err != nil {
		return false, err
	}
	if badge.GrantMode != GrantManual {
		return false, ErrBadgeManualOnly
	}
	removed, err := a.store.RevokeManual(ctx, userID, badgeID)
	if err != nil {
		return false, err
	}
	if err := a.store.RecomputeTopBadge(ctx, userID); err != nil {
		return false, err
	}
	// 透传「是否真的删到行」：响应体不能报假（收回一个不存在的授予时必须是 false）。
	return removed, nil
}

// ListGrants 返回授予记录。
func (a *AdminService) ListGrants(ctx context.Context, actor identity.Actor, input GrantListInput) (GrantList, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return GrantList{}, err
	}
	if a == nil || a.store == nil {
		return GrantList{}, nil
	}
	return a.store.ListGrants(ctx, input)
}

// RecomputeResult 是重算的两个可区分结果。
//
// 只返回一个数字时，运营无法判断按钮做了什么：补了授予？还是只修了显示？
type RecomputeResult struct {
	// Granted 是本次按规则新插入的 user_badges 行数。
	Granted int64 `json:"granted"`
	// Refreshed 是 top_badge_id 实际发生变化的用户数。
	Refreshed int64 `json:"refreshed"`
}

// Recompute 全量重算：先按规则补授予，再刷新 top_badge_id。
//
// 两件事的顺序有意义：先补授予，后面的 top_badge 刷新才能看到新徽章，
// 否则运营调低阈值后点一次按钮，徽章补上了但帖子旁还是旧徽章。
//
// 它修的是「关系行/规则与展示不一致」的漂移，**不碰统计计数**：
// topic_count / comment_count 记的是事件累计历史，重算不得抹平（计划 D4）。
func (a *AdminService) Recompute(ctx context.Context, actor identity.Actor) (RecomputeResult, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return RecomputeResult{}, err
	}
	if a == nil || a.store == nil {
		return RecomputeResult{}, ErrBadgesDisabled
	}
	granted, err := a.store.GrantFromRules(ctx)
	if err != nil {
		return RecomputeResult{}, err
	}
	refreshed, err := a.store.RecomputeAll(ctx)
	if err != nil {
		return RecomputeResult{}, err
	}
	return RecomputeResult{Granted: granted, Refreshed: refreshed}, nil
}

// recomputeAffected 重算持有该徽章的全部用户的 top_badge_id。
func (a *AdminService) recomputeAffected(ctx context.Context, badgeID int64) error {
	userIDs, err := a.store.AffectedUserIDs(ctx, badgeID)
	if err != nil {
		return err
	}
	for _, userID := range userIDs {
		if err := a.store.RecomputeTopBadge(ctx, userID); err != nil {
			return fmt.Errorf("recompute top badge for user %d: %w", userID, err)
		}
	}
	return nil
}

// topBadgeAffected 报告这次更新是否可能改变「谁是他最高级徽章」。
//
// 只看三个排序/过滤相关的字段：enabled（停用后不再参与）、tier 与 position（排序）。
// 改名换图标不影响谁被选中，不必重算。
func topBadgeAffected(previous, updated Badge) bool {
	return previous.Enabled != updated.Enabled ||
		previous.Tier != updated.Tier ||
		previous.Position != updated.Position
}

// validateBadgeInput 是规则校验的唯一入口（创建与更新共用）。
func validateBadgeInput(input BadgeInput) (BadgeInput, error) {
	input.Slug = normalizeSlug(input.Slug)
	input.Name = strings.TrimSpace(input.Name)
	if input.Slug == "" || input.Name == "" {
		return BadgeInput{}, fmt.Errorf("%w: slug and name are required", ErrBadgeRuleInvalid)
	}
	if !input.Tier.Valid() {
		return BadgeInput{}, fmt.Errorf("%w: unknown tier %q", ErrBadgeRuleInvalid, input.Tier)
	}
	if !input.GrantMode.Valid() {
		return BadgeInput{}, fmt.Errorf("%w: unknown grant mode %q", ErrBadgeRuleInvalid, input.GrantMode)
	}
	if input.Icon == "" {
		input.Icon = "i-lucide-award"
	}
	if !strings.HasPrefix(input.Icon, "i-lucide-") && !strings.HasPrefix(input.Icon, "i-tabler-") {
		return BadgeInput{}, fmt.Errorf("%w: icon must use i-lucide- or i-tabler- prefix", ErrBadgeRuleInvalid)
	}
	switch input.GrantMode {
	case GrantAutomatic:
		if input.Metric == nil || !input.Metric.Valid() {
			return BadgeInput{}, fmt.Errorf("%w: automatic badge requires a known metric", ErrBadgeRuleInvalid)
		}
		if input.Threshold == nil || *input.Threshold <= 0 {
			return BadgeInput{}, fmt.Errorf("%w: automatic badge requires threshold > 0", ErrBadgeRuleInvalid)
		}
	case GrantManual:
		// 手动徽章必须两者皆空：留着 metric 会让「什么时候该授予」有两种解释。
		if input.Metric != nil || input.Threshold != nil {
			return BadgeInput{}, fmt.Errorf("%w: manual badge must not carry metric or threshold", ErrBadgeRuleInvalid)
		}
	}
	return input, nil
}

// validateGroupInput 校验分组入参。
func validateGroupInput(input BadgeGroupInput) (BadgeGroupInput, error) {
	input.Key = normalizeSlug(input.Key)
	input.Name = strings.TrimSpace(input.Name)
	if input.Key == "" || input.Name == "" {
		return BadgeGroupInput{}, fmt.Errorf("%w: group key and name are required", ErrBadgeRuleInvalid)
	}
	return input, nil
}
