package badges

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
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
	// tiers 是徽章等级校验端口（TierReader）。
	//
	// 单独一个字段而不是直接调 a.store.GetTier：等级校验只需要「这一档存在吗、
	// 启用吗」，而 AdminService 的其余方法需要完整 Store。留出这个缝，单测才能只
	// 实现两条方法就验证「切到停用档必须 422」。
	//
	// 未注入时回退到 store（它本身实现了 TierReader）：这样既有的装配代码
	// NewAdminService(store) 不必改签名，也不会出现「忘了注入 = 校验被跳过」。
	tiers TierReader
	// backfill 是复杂规则的回扫入口；nil 表示装配没接（此时有复杂徽章就报错）。
	backfill BackfillEnqueuer
	// runs 是回扫进度的读口（见 backfill_runs.go）；nil 时状态端点回空 items。
	// 读与写分开：AdminService 只读，写口（BackfillRunWriter）只交给 job 侧。
	runs BackfillRunReader
	// factCandidates 是插件事实候选来源（已启用扩展的 badgeConditions[]）；
	// nil = 没有任何扩展声明，`fact.count` 因此不出现在可发布目录里。
	factCandidates conditions.FactCandidateProvider
	// notifier 是授予通知出口（见 grant_notifications.go）；nil = 不发通知。
	notifier GrantNotifier
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
	items, err := a.store.ListBadges(ctx)
	if err != nil {
		return nil, err
	}
	return a.fillRequirementTexts(ctx, items), nil
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
	// 创建时没有「缺省=不改」：effective rule 就是本次提交的 rule。
	// 这条判断放在这里而不是 validateBadgeInput，因为只有本层知道三态语义；
	// 且必须在落库前给出 422——`badges_rule_check` 抛的是约束错误，会变成 500。
	created, err := a.normalizeNewBadge(ctx, input)
	if err != nil {
		return Badge{}, err
	}
	// 创建/更新的回执也要带文案：前端保存后用服务端结果覆盖预览，
	// 少一次「保存成功但预览还是旧句子」的时序问题。
	created = a.fillRequirementTexts(ctx, []Badge{created})[0]
	return created, nil
}

// normalizeNewBadge 是创建路径的 effective-rule 判定 + 入参归一化。
//
// 与 UpdateBadge 的三态校验一一对应，只是「缺省」在创建时等价于「空」：
//   - automatic 必须有规则（`metric`/`threshold` 已经不再代表判定来源）；
//   - manual 不得带规则（留着规则会让「什么时候该授予」有两种解释）。
//
// 规则形状（未知条件 / 层级过深）由 ruleengine.Decode 的严格模式报出，
// mapBadgeError 会按 CodeOf 拆成 422 的具体错误码。
func (a *AdminService) normalizeNewBadge(ctx context.Context, input BadgeInput) (Badge, error) {
	if input.GrantMode == GrantAutomatic && len(input.RuleRaw) == 0 {
		return Badge{}, fmt.Errorf("%w: automatic badge requires a rule", ErrBadgeRuleInvalid)
	}
	if input.GrantMode == GrantManual && len(input.RuleRaw) > 0 {
		return Badge{}, fmt.Errorf("%w: manual badge must not carry a rule", ErrBadgeRuleModeConflict)
	}
	// 创建时还没有 badge id，跳过 badge.owned 的环检测（新徽章不可能被别人引用），
	// 但形状与引用存在性照样校验。ValidateBadgeRule 的签名要求 badgeID，
	// 所以引用检查走 store 的窄接口（PostgresStore 实现），id 传 0。
	if len(input.RuleRaw) > 0 {
		node, decodeErr := ruleengine.Decode(input.RuleRaw)
		if decodeErr != nil {
			return Badge{}, fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, decodeErr)
		}
		if validator, ok := a.store.(interface {
			ValidateBadgeRule(context.Context, int64, ruleengine.Node) error
		}); ok {
			if err := validator.ValidateBadgeRule(ctx, 0, node); err != nil {
				return Badge{}, err
			}
		}
	}
	normalized, err := validateBadgeInput(ctx, input, a.tierReader(), nil)
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
	// 先读旧值：校验「切档」需要知道原值（保持原值一律允许，即使该档已停用）。
	previous, err := a.store.GetBadge(ctx, badgeID)
	if err != nil {
		return Badge{}, err
	}
	// 规则三态 × grantMode 的组合校验：必须在落库前给出 422，而不是让 CHECK 抛
	// 约束错误（那会变成 500）。三种组合：
	//   {"rule":null}                     + 原本 automatic → 422（自动徽章必须有规则）
	//   {"rule":null,"grantMode":"manual"}                 → 200（转手动时清空是合法且常用的操作）
	//   {"rule":null,"grantMode":"automatic"}              → 422
	effectiveMode := input.GrantMode
	effectiveRule := previous.RuleRaw
	if input.RuleSet {
		effectiveRule = input.RuleRaw
	}
	if effectiveMode == GrantAutomatic && len(effectiveRule) == 0 {
		return Badge{}, fmt.Errorf("%w: automatic badge requires a rule", ErrBadgeRuleInvalid)
	}
	if effectiveMode == GrantManual && len(effectiveRule) > 0 {
		return Badge{}, fmt.Errorf("%w: manual badge must not carry a rule", ErrBadgeRuleModeConflict)
	}

	// 规则三态里「对象」这一态要在这里过一遍保存校验：AST 形状（严格 Decode）、
	// 引用存在性、badge.owned 环检测。缺省与清空都不进这里。
	if input.RuleSet && len(input.RuleRaw) > 0 {
		node, decodeErr := ruleengine.Decode(input.RuleRaw)
		if decodeErr != nil {
			return Badge{}, fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, decodeErr)
		}
		if validator, ok := a.store.(interface {
			ValidateBadgeRule(context.Context, int64, ruleengine.Node) error
		}); ok {
			if err := validator.ValidateBadgeRule(ctx, badgeID, node); err != nil {
				return Badge{}, err
			}
		}
	}
	normalized, err := validateBadgeInput(ctx, input, a.tierReader(), &previous.Tier)
	if err != nil {
		return Badge{}, err
	}
	updated, err := a.store.UpdateBadge(ctx, badgeID, normalized)
	if err != nil {
		return Badge{}, err
	}
	updated = a.fillRequirementTexts(ctx, []Badge{updated})[0]
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
	if err := a.store.RecomputeTopBadge(ctx, input.UserID); err != nil {
		return err
	}
	// 授予行已经落库：通知放在重算之后、且尽力而为（见 notifyGranted）。
	a.notifyGranted(ctx, input.UserID, []GrantedBadge{{ID: badge.ID, Slug: badge.Slug, Name: badge.Name}})
	return nil
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

// recomputeNotifyLimit 是「重算后逐条发授予通知」的上限。
//
// 为什么有上限：重算是一次面向全站的批量补授予，运营调低阈值后一次可能补出
// 成千上万行。逐条写通知（每条一个事务 + 收件箱投影 + 邮件投递行）会把一次
// 点击变成一分钟的写放大，且对用户来说是一夜几十条徽章通知的噪音。
// 超过上限时**整体不发**而不是「发前 200 条」：部分发送会让「谁被通知到」
// 取决于行扫描顺序，运营既解释不了也复现不了。
//
// 与自动授予无关：`badges.evaluate_user` job 每次最多几枚，走的是 job 自己的
// 通知出口（见 bootstrap/badges_wiring.go），不受这个阈值约束。
const recomputeNotifyLimit = 200

// RecomputeResult 是重算的两个可区分结果。
//
// 只返回一个数字时，运营无法判断按钮做了什么：补了授予？还是只修了显示？
type RecomputeResult struct {
	// Granted 是本次按规则新插入的 user_badges 行数。
	Granted int64 `json:"granted"`
	// Refreshed 是 top_badge_id 实际发生变化的用户数。
	Refreshed int64 `json:"refreshed"`
	// Queued 是**被转入异步评估的徽章数**（不是用户数、不是 job 数）。
	//
	// 复杂规则没法在一条 INSERT ... SELECT 里求值，必须由 backfill job 逐人算。
	// 静默跳过等于「重算完成、0 人获得，实际上一半徽章没算」——所以这里必须报数，
	// 运营界面也要说清「Z 枚徽章转入后台评估」。
	Queued int64 `json:"queued"`
}

// BackfillEnqueuer 把「需要异步评估的徽章」交给后台 job。
//
// 只入队不执行：HTTP 请求里跑全量回扫会重演残留 #17（长事务 + 长请求）。
type BackfillEnqueuer interface {
	EnqueueRuleBackfill(ctx context.Context, badgeIDs []int64) error
}

// SetBackfillEnqueuer 注入后台回扫入口（bootstrap 装配；测试可注入 fake）。
func (a *AdminService) SetBackfillEnqueuer(enqueuer BackfillEnqueuer) {
	if a == nil {
		return
	}
	a.backfill = enqueuer
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
	grants, err := a.store.GrantFromRules(ctx)
	if err != nil {
		return RecomputeResult{}, err
	}
	granted, complexBadgeIDs := grants.Granted, grants.ComplexBadgeIDs
	queued := int64(len(complexBadgeIDs))
	if queued > 0 {
		// 没有入队能力时必须报错，不能只把数字填上——那会让运营以为「已经安排好了」，
		// 而实际上一枚复杂规则徽章都没算。
		if a.backfill == nil {
			return RecomputeResult{}, ErrBadgeBackfillUnavailable
		}
		if err := a.backfill.EnqueueRuleBackfill(ctx, complexBadgeIDs); err != nil {
			return RecomputeResult{}, fmt.Errorf("enqueue rule backfill: %w", err)
		}
	}
	refreshed, err := a.store.RecomputeAll(ctx)
	if err != nil {
		return RecomputeResult{}, err
	}
	a.notifyRecomputeGrants(ctx, grants.Rows)
	// 响应形状**严格三键**：授予通知是副作用，不新增计数器（见 modules/badges.md
	// 「queued 与 granted/refreshed 是三个不同单位」——再塞一个数字只会更混乱）。
	return RecomputeResult{Granted: granted, Refreshed: refreshed, Queued: queued}, nil
}

// notifyRecomputeGrants 为重算补出来的授予发通知（≤ recomputeNotifyLimit 时）。
//
// 超过阈值整体不发，只留一条 warn：运营需要知道「这次没通知」，否则会把
// 「用户说没收到通知」当成缺陷去查。
func (a *AdminService) notifyRecomputeGrants(ctx context.Context, rows []RuleGrant) {
	if a == nil || a.notifier == nil || len(rows) == 0 {
		return
	}
	if len(rows) > recomputeNotifyLimit {
		slog.Warn("skip badge grant notifications for recompute over limit",
			"grants", len(rows), "limit", recomputeNotifyLimit)
		return
	}
	// 按用户聚合后再发出：这只减少**通知出口的调用次数**（一次调用带该用户的 N 枚），
	// 不改变投影结果——`badgeGrantedNotifier` 仍逐枚写通知行，去重键是
	// `badge:{slug}`，所以「同一用户拿到 3 枚」最终是 3 条通知行、不是 1 条。
	// 不要照这段注释去优化成「一条通知列多枚」，那会改掉收件箱的既有语义。
	badgesByUser := make(map[int64][]GrantedBadge, len(rows))
	badgeCache := make(map[int64]GrantedBadge)
	for _, row := range rows {
		badge, ok := badgeCache[row.BadgeID]
		if !ok {
			loaded, err := a.store.GetBadge(ctx, row.BadgeID)
			if err != nil {
				// 读不到徽章名只影响这一枚的通知文案，不值得放弃整批通知。
				slog.Warn("load badge for recompute notification failed", "badge_id", row.BadgeID, "error", err)
				continue
			}
			badge = GrantedBadge{ID: loaded.ID, Slug: loaded.Slug, Name: loaded.Name}
			badgeCache[row.BadgeID] = badge
		}
		badgesByUser[row.UserID] = append(badgesByUser[row.UserID], badge)
	}
	for userID, badges := range badgesByUser {
		a.notifyGranted(ctx, userID, badges)
	}
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

// validateBadgeInput 是**字段级**校验的唯一入口（创建与更新共用）。
//
// 它不认识「规则三态」：是否必须有规则由 CreateBadge / UpdateBadge 掌握
// effective rule 之后再判。这里只回答单个字段本身合不合法。
//
// 等级合法性从「枚举自证」变成「查等级表」：tier 必须存在于 badge_tiers，
// 切档时还必须是启用状态；currentTier 非 nil 表示更新，保持原值一律放行。
// 具体规则见 tiers.go 的 resolveBadgeTier。
func validateBadgeInput(ctx context.Context, input BadgeInput, tiers TierReader, currentTier *BadgeTier) (BadgeInput, error) {
	input.Slug = normalizeSlug(input.Slug)
	input.Name = strings.TrimSpace(input.Name)
	if input.Slug == "" || input.Name == "" {
		return BadgeInput{}, fmt.Errorf("%w: slug and name are required", ErrBadgeRuleInvalid)
	}
	if err := resolveBadgeTier(ctx, tiers, input.Tier, currentTier); err != nil {
		return BadgeInput{}, err
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
		// 自 202611120002 起 automatic 的判定来源是 `rule`，`metric`/`threshold` 是
		// legacy 只读列（可为 NULL）。这里只校验「给了就必须合法」，
		// 「必须有规则」由掌握 effective rule 的服务层（CreateBadge / UpdateBadge）判：
		// 那两处才知道「缺省=不改」还是「创建时缺省=没有」。
		if input.Metric != nil && !input.Metric.Valid() {
			return BadgeInput{}, fmt.Errorf("%w: unknown metric %q", ErrBadgeRuleInvalid, *input.Metric)
		}
		if input.Threshold != nil && *input.Threshold <= 0 {
			return BadgeInput{}, fmt.Errorf("%w: threshold must be > 0", ErrBadgeRuleInvalid)
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

// RuleAdminStore 是规则只读面的窄接口（只由 PostgresStore 实现）。
//
// 不往 `Store` 上加方法：那会逼所有测试 fake 实现它们不需要的能力（同 RuleResolver）。
type RuleAdminStore interface {
	RuleHealth(ctx context.Context) ([]BadgeRuleHealth, error)
	EstimateRuleImpact(ctx context.Context, rule ruleengine.Node) (RuleImpact, error)
	GetBadgeRule(ctx context.Context, badgeID int64) (ruleengine.Node, error)
}

// RefCandidateStore 是 objectRef 候选的窄读口（只由 PostgresStore 实现）。
//
// 与 `RuleAdminStore` 同一个理由：不往 `Store` 上加方法，否则每个测试 fake 都得
// 实现一份它们不关心的候选查询。`!ok` 时调用方按「取不到候选」处理，编辑器退回
// 文本输入，而不是渲染空下拉。
type RefCandidateStore interface {
	RefCandidates(ctx context.Context) (conditions.RefCandidateSet, error)
}

// BadgeConditionCatalog 是 `GET /admin/badges/conditions` 的领域结果。
//
// 三个面刻意一起返回：`Items` 是**条件**目录（规则的 type 维），`FactCandidates`
// 是**事实候选**（规则里 `fact.count` 的 factKey/variant 维），`RefCandidates`
// 是**对象引用候选**（`ParamSpec.RefKind` 的取值域：分类/标签/角色/登录方式/徽章）。
// 插件条件的双语 label 属于候选项而不是 Core 条件文案，混进 Items 会让「条件名」
// 被插件覆盖；对象引用候选同理——它是数据（运营增删的对象），不是条件元数据。
type BadgeConditionCatalog struct {
	Items          []conditions.Definition
	FactCandidates []conditions.FactCandidate
	RefCandidates  conditions.RefCandidateSet
}

// BadgeConditions 返回**可发布**的条件目录（展示面）。
//
// 用 `PublishableDefinitions(candidates)` 而不是 `Definitions()`：没有事实候选时
// `fact.count` 是空下拉，暴露出去只会让运营点进死路。校验/求值仍走全集。
//
// 对象引用候选是 best-effort：缺少 `RefCandidateStore`、查询失败都只让对应 kind 变
// `Unavailable`，**绝不**让这个只读端点失败。理由是候选只是编辑器的便利设施——
// 取不到时运营还能手填，而让整个条件目录 500 会把「配规则」这条主流程一起打死。
func (a *AdminService) BadgeConditions(ctx context.Context, actor identity.Actor) (BadgeConditionCatalog, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return BadgeConditionCatalog{}, err
	}
	candidates := []conditions.FactCandidate{}
	if a != nil && a.factCandidates != nil {
		if declared := a.factCandidates(); declared != nil {
			candidates = declared
		}
	}
	refCandidates := conditions.UnavailableRefCandidateSet()
	if a != nil && a.store != nil {
		if store, ok := a.store.(RefCandidateStore); ok {
			// 先接返回值再用错误：store 的契约是「失败类标 unavailable、其余照常
			// 返回」，整体丢弃会把一次局部失败放大成五项全空。
			set, err := store.RefCandidates(ctx)
			if err != nil {
				slog.Default().Warn("badge ref candidates partially unavailable", "error", err)
			}
			if len(set) > 0 {
				refCandidates = set
			}
		}
	}
	return BadgeConditionCatalog{
		Items:          conditions.Core().PublishableDefinitions(candidates),
		FactCandidates: candidates,
		RefCandidates:  refCandidates,
	}, nil
}

// SetFactCandidates 注入事实候选来源（bootstrap 装配；nil = 无候选）。
func (a *AdminService) SetFactCandidates(provider conditions.FactCandidateProvider) {
	if a == nil {
		return
	}
	a.factCandidates = provider
}

// RuleHealth 返回失效引用清单。空清单是正常结果，不是错误。
func (a *AdminService) RuleHealth(ctx context.Context, actor identity.Actor) ([]BadgeRuleHealth, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	store, ok := a.store.(RuleAdminStore)
	if !ok {
		return nil, nil
	}
	return store.RuleHealth(ctx)
}

// EstimateRuleImpact 估算某枚徽章规则的影响面（语义是「≈」）。
//
// **手动徽章不是「读不到规则」，而是「本来就没有规则可估」**：库里 `rule` 为 NULL，
// 若继续往下走 `GetBadgeRule` 就会拿到 `ErrBadgeRuleInvalid` → 422 → 前端渲染成红色
// 「影响面估算获取失败」。而「手动徽章 → 改成自动」正是编辑弹窗里最常见的路径，
// 于是每次正常操作都报一次假故障。这里直接返回「无法估算」的中性结果：
// 契约本来就允许 `estimated: null`（与 `0` 是两件事）。
func (a *AdminService) EstimateRuleImpact(ctx context.Context, actor identity.Actor, badgeID int64) (RuleImpact, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return RuleImpact{}, err
	}
	store, ok := a.store.(RuleAdminStore)
	if !ok {
		return RuleImpact{}, ErrBadgeNotFound
	}
	// 先看授予方式：只有 automatic 才可能有一条规则。
	badge, err := a.store.GetBadge(ctx, badgeID)
	if err != nil {
		return RuleImpact{}, err
	}
	if badge.GrantMode == GrantManual {
		return RuleImpact{
			Estimated:             nil,
			Method:                impactMethodUnsupported,
			Partial:               false,
			UnsupportedConditions: []string{},
		}, nil
	}
	rule, err := store.GetBadgeRule(ctx, badgeID)
	if err != nil {
		return RuleImpact{}, err
	}
	return store.EstimateRuleImpact(ctx, rule)
}
