package badges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// PostgresStore 是 Store 的生产实现。
type PostgresStore struct {
	pool *pgxpool.Pool
	// factKeys 是已声明的插件事实 key 来源；nil 表示没有扩展（空集）。
	factKeys FactKeyProvider
	// factCandidates 是事实**候选**来源（含 variant 与双语 label）。
	//
	// 与 factKeys 分开而不是从它派生：键集合用于「这个 key 还能不能用」，
	// 候选还带 variants，保存期要靠它拦拼错的 variant（spec §8 Errata 2 #1）。
	// 两者在生产装配里同源（见 bootstrap），但测试与迁移期可以只给其中一个。
	factCandidates conditions.FactCandidateProvider
	// declaredProviders 是 identity registry 已声明的 provider（含零绑定）；nil 时只认绑定表。
	declaredProviders DeclaredProviderFunc
	// providerNames 是公开说明用到的登录方式真名（见 ref_names_postgres.go）。
	providerNames providerNameFunc
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// topBadgeOrderSQL 是「最高级徽章」的唯一排序定义。
//
// 排序键来自等级表：badge_tiers.position 升序（0 = 最高档）→ badges.position →
// granted_at 降序。**不再**用 CASE 映射 gold/silver/bronze——等级是运营可增删的
// 数据，写死映射会让新档位永远排最后。
//
// 使用本常量的每个查询都**必须**自带
// `JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`：
//   - JOIN 而不是 LEFT JOIN：停用档位的徽章不该参与竞争（等价于「这一档不存在」）；
//   - 别名固定为 t，本常量引用 t.position。
//
// 资料页、帖子旁 chip、top_badge_id 维护必须共用这一段，否则同一个人在两个地方
// 会显示不同的徽章。
const topBadgeOrderSQL = `
	t.position ASC,
	badges.position ASC,
	user_badges.granted_at DESC`

// badgeTierColumnsSQL 是徽章的等级外观列（双语名/颜色/位次）。
//
// LEFT JOIN + COALESCE 而不是 INNER JOIN：外键保证正常情况一定有行，但读路径
// 不该因为「等级行暂时缺失」（运维手工删数据、回滚中的中间态）把整个后台列表变成 500。
// 空外观在 DTO 层是「没有等级信息」，前端有兜底分支。
const badgeTierColumnsSQL = `
	COALESCE(tier_def.name_zh, '') AS tier_name_zh,
	COALESCE(tier_def.name_en, '') AS tier_name_en,
	COALESCE(tier_def.color, '') AS tier_color,
	COALESCE(tier_def.position, 2147483647) AS tier_position`

const badgeColumnsSQL = `
	badges.id, badges.slug, badges.name, badges.description,
	badges.group_id, badge_groups.name AS group_name, badge_groups.key AS group_key,
	badges.tier, badges.icon, badges.grant_mode, badges.metric, badges.threshold,
	badges.enabled, badges.position, badges.created_at, badges.updated_at,
	badges.rule, badges.requirement_summary_zh, badges.requirement_summary_en,
	COALESCE(grants.granted_count, 0) AS granted_count,` + badgeTierColumnsSQL

const badgeJoinsSQL = `
	FROM badges
	LEFT JOIN badge_groups ON badge_groups.id = badges.group_id
	LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
	LEFT JOIN (
		SELECT badge_id, COUNT(*) AS granted_count FROM user_badges GROUP BY badge_id
	) AS grants ON grants.badge_id = badges.id`

func scanBadge(row pgx.Row) (Badge, error) {
	var badge Badge
	var metric *string
	var ruleRaw []byte
	if err := row.Scan(
		&badge.ID, &badge.Slug, &badge.Name, &badge.Description,
		&badge.GroupID, &badge.GroupName, &badge.GroupKey,
		&badge.Tier, &badge.Icon, &badge.GrantMode, &metric, &badge.Threshold,
		&badge.Enabled, &badge.Position, &badge.CreatedAt, &badge.UpdatedAt,
		&ruleRaw, &badge.RequirementSummaryZh, &badge.RequirementSummaryEn,
		&badge.GrantedCount,
		&badge.Appearance.NameZh, &badge.Appearance.NameEn, &badge.Appearance.Color,
		&badge.Appearance.Position,
	); err != nil {
		return Badge{}, err
	}
	applyScannedBadgeFields(&badge, metric, ruleRaw)
	return badge, nil
}

// applyScannedBadgeFields 把「可空 metric + rule 解码」的扫描后处理收在一处。
//
// 后台列表（scanBadge）与资料页（ListUserBadges）必须逐字同口径：同一条脏规则
// 若在两处得到不同的 Rule，资料页就会莫名少一句「获得条件」，而目录页有——这种
// 分叉没有编译期信号，只能靠共用同一段代码 + 双向测试守住。
func applyScannedBadgeFields(badge *Badge, metric *string, ruleRaw []byte) {
	if metric != nil {
		value := ActivityMetric(*metric)
		badge.Metric = &value
	}
	if len(ruleRaw) > 0 {
		badge.RuleRaw = append(json.RawMessage(nil), ruleRaw...)
		// 解码失败不返回错误：一条脏规则不该让整个列表/评估崩掉。Rule 留 nil，
		// 评估按「不满足」处理，rule_health 会把 RuleRaw 非空 + Rule 为 nil 的行列出来。
		if node, err := ruleengine.Decode(ruleRaw); err == nil {
			badge.Rule = &node
		} else {
			// 解不开的规则记在 RuleBroken 上：公开说明不能把它说成「手动授予」。
			badge.RuleBroken = true
		}
	}
}

// ListBadges 返回全部徽章（含停用），按分组位置、徽章位置排序。
func (s *PostgresStore) ListBadges(ctx context.Context) ([]Badge, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+`
		ORDER BY COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST, badges.position, badges.id`)
	if err != nil {
		return nil, fmt.Errorf("list badges: %w", err)
	}
	defer rows.Close()
	items := make([]Badge, 0, 16)
	for rows.Next() {
		badge, err := scanBadge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan badge: %w", err)
		}
		items = append(items, badge)
	}
	return items, rows.Err()
}

// GetBadge 按 id 读单个徽章。
func (s *PostgresStore) GetBadge(ctx context.Context, badgeID int64) (Badge, error) {
	badge, err := scanBadge(s.pool.QueryRow(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+` WHERE badges.id = $1`, badgeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Badge{}, ErrBadgeNotFound
	}
	if err != nil {
		return Badge{}, fmt.Errorf("get badge: %w", err)
	}
	return badge, nil
}

// GetBadgeBySlug 按 slug 读单个徽章。
func (s *PostgresStore) GetBadgeBySlug(ctx context.Context, slug string) (Badge, error) {
	badge, err := scanBadge(s.pool.QueryRow(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+` WHERE badges.slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return Badge{}, ErrBadgeNotFound
	}
	if err != nil {
		return Badge{}, fmt.Errorf("get badge by slug: %w", err)
	}
	return badge, nil
}

// ListGroups 返回全部分组（含组内徽章数）。
func (s *PostgresStore) ListGroups(ctx context.Context) ([]BadgeGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT badge_groups.id, badge_groups.key, badge_groups.name, badge_groups.description,
		       badge_groups.position, COALESCE(counts.badge_count, 0),
		       badge_groups.created_at, badge_groups.updated_at
		FROM badge_groups
		LEFT JOIN (
			SELECT group_id, COUNT(*) AS badge_count FROM badges GROUP BY group_id
		) AS counts ON counts.group_id = badge_groups.id
		ORDER BY badge_groups.position, badge_groups.id`)
	if err != nil {
		return nil, fmt.Errorf("list badge groups: %w", err)
	}
	defer rows.Close()
	items := make([]BadgeGroup, 0, 8)
	for rows.Next() {
		var group BadgeGroup
		if err := rows.Scan(&group.ID, &group.Key, &group.Name, &group.Description,
			&group.Position, &group.BadgeCount, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan badge group: %w", err)
		}
		items = append(items, group)
	}
	return items, rows.Err()
}

// ListUserBadges 返回「全部启用徽章 × 该用户」的墙：未获得的也要出现（进度用）。
//
// 文案（`RequirementText`）只能经 `Badge.Public()` 生成，与公开目录共用
// `BuildRequirementText` 一份口径。本查询曾经漏选 rule / requirement_summary_*
// 三列，资料页于是恒为 `{"zh":"","en":""}`，而目录页同一枚徽章有文案——
// 前端按「文案整行为空」隐藏卡片，症状是徽章墙少行、且没有报错。
func (s *PostgresStore) ListUserBadges(ctx context.Context, userID int64) ([]UserBadge, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT badges.id, badges.slug, badges.name, badges.description,
		       badge_groups.key AS group_key, badges.tier, badges.icon,
		       badges.grant_mode, badges.metric, badges.threshold, badges.position,
		       badges.rule,
		       user_badges.granted_at,
		       `+badgeTierColumnsSQL+`
		FROM badges
		LEFT JOIN badge_groups ON badge_groups.id = badges.group_id
		LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
		LEFT JOIN user_badges ON user_badges.badge_id = badges.id AND user_badges.user_id = $1
		WHERE badges.enabled
		ORDER BY COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST,
		         badges.position, badges.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user badges: %w", err)
	}
	defer rows.Close()
	items := make([]UserBadge, 0, 16)
	badgeItems := make([]Badge, 0, 16)
	for rows.Next() {
		var item UserBadge
		var badge Badge
		var metric *string
		var ruleRaw []byte
		if err := rows.Scan(
			&badge.ID, &badge.Slug, &badge.Name, &badge.Description,
			&badge.GroupKey, &badge.Tier, &badge.Icon,
			&badge.GrantMode, &metric, &badge.Threshold, &badge.Position,
			&ruleRaw,
			&item.GrantedAt,
			&badge.Appearance.NameZh, &badge.Appearance.NameEn,
			&badge.Appearance.Color, &badge.Appearance.Position,
		); err != nil {
			return nil, fmt.Errorf("scan user badge: %w", err)
		}
		applyScannedBadgeFields(&badge, metric, ruleRaw)
		badgeItems = append(badgeItems, badge)
		item.Earned = item.GrantedAt != nil
		// 进度**不在这里算**：legacy 的 threshold 是只读遗留（规则徽章为 NULL），
		// 而真实进度要按规则批量测量（见 Service.measureProgress / MeasureProgress）。
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 两段式投影：一次批量解析引用名，再逐条渲染（文案只有一份实现）。
	names := BadgeRefNames(ctx, RefNameLoaderOf(s), badgeItems)
	for index := range items {
		items[index].Badge = badgeItems[index].Public(names)
	}
	return items, nil
}

// TopBadgesFor 一次查询取整页用户**当前展示**的徽章，避免列表 N+1。
//
// 可见性与帖子/评论旁 chip 逐字同口径（见 Forum 的 topBadgeJoinSQL）：
//   - 徽章停用后不再展示（`top_badge_id` 可能仍指向它）；
//   - 等级停用同样不展示——自 202611120001 起「停用一档等价于这一档不存在」，
//     停档 + 重算未跑的窗口里不能仍把该档徽章当展示位。
//
// 所以等级是 JOIN 而不是 LEFT JOIN：缺行与停用行在这里是同一件事（不可展示）。
//
// 两个读口（这里的 top_badge_id 投影与 Forum 的 topBadgeJoinSQL）都**不判**
// `users.top_badge_hidden`：hidden 的不可见性完全依赖写入不变量
// `top_badge_hidden = TRUE ⇒ top_badge_id IS NULL`（HideWornBadge 同一句写入，
// 清展示位、清 manual 标记、置 hidden）。半坏行（hidden 为真但仍指向徽章）会让
// 资料页与 chip 同时把它显示出来——那属于数据修复范畴，不在读路径兜。
//
// 消费方是 Profile 资料页展示位（经 bootstrap 适配器转成 forum.BadgeRef）。
func (s *PostgresStore) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	result := make(map[int64]BadgeRef, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT users.id, badges.slug, badges.name, badges.tier, badges.icon,
		       `+badgeTierColumnsSQL+`
		FROM users
		JOIN badges ON badges.id = users.top_badge_id
		JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier AND tier_def.enabled
		WHERE users.id = ANY($1) AND badges.enabled`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("top badges for users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var ref BadgeRef
		if err := rows.Scan(&userID, &ref.Slug, &ref.Name, &ref.Tier, &ref.Icon,
			&ref.Appearance.NameZh, &ref.Appearance.NameEn,
			&ref.Appearance.Color, &ref.Appearance.Position); err != nil {
			return nil, fmt.Errorf("scan top badge: %w", err)
		}
		result[userID] = ref
	}
	return result, rows.Err()
}

// ListEnabledAutomaticBadges 返回启用中的自动徽章（规则评估的输入）。
func (s *PostgresStore) ListEnabledAutomaticBadges(ctx context.Context) ([]Badge, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+`
		WHERE badges.enabled AND badges.grant_mode = 'automatic'
		ORDER BY badges.id`)
	if err != nil {
		return nil, fmt.Errorf("list automatic badges: %w", err)
	}
	defer rows.Close()
	items := make([]Badge, 0, 16)
	for rows.Next() {
		badge, err := scanBadge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan automatic badge: %w", err)
		}
		items = append(items, badge)
	}
	return items, rows.Err()
}

// AffectedUserIDs 返回持有该徽章的用户。
func (s *PostgresStore) AffectedUserIDs(ctx context.Context, badgeID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM user_badges WHERE badge_id = $1`, badgeID)
	if err != nil {
		return nil, fmt.Errorf("affected users: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan affected user: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListGrants 返回授予记录（admin 面），可按用户/徽章过滤。
func (s *PostgresStore) ListGrants(ctx context.Context, input GrantListInput) (GrantList, error) {
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if input.UserID > 0 {
		args = append(args, input.UserID)
		conditions = append(conditions, fmt.Sprintf("user_badges.user_id = $%d", len(args)))
	}
	if input.BadgeID > 0 {
		args = append(args, input.BadgeID)
		conditions = append(conditions, fmt.Sprintf("user_badges.badge_id = $%d", len(args)))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_badges`+where, args...).Scan(&total); err != nil {
		return GrantList{}, fmt.Errorf("count grants: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT user_badges.user_id, users.username, users.display_name,
		       user_badges.badge_id, badges.slug, user_badges.granted_at,
		       user_badges.granted_by, user_badges.grant_reason
		FROM user_badges
		JOIN users ON users.id = user_badges.user_id
		JOIN badges ON badges.id = user_badges.badge_id%s
		ORDER BY user_badges.granted_at DESC, user_badges.id DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return GrantList{}, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()
	items := make([]BadgeGrantRecord, 0, limit)
	for rows.Next() {
		var record BadgeGrantRecord
		if err := rows.Scan(&record.UserID, &record.Username, &record.DisplayName,
			&record.BadgeID, &record.BadgeSlug, &record.GrantedAt,
			&record.GrantedBy, &record.GrantReason); err != nil {
			return GrantList{}, fmt.Errorf("scan grant: %w", err)
		}
		items = append(items, record)
	}
	return GrantList{Items: items, Total: total}, rows.Err()
}

// GrantAutomatic 幂等授予：已持有则不插入，返回 false。
func (s *PostgresStore) GrantAutomatic(ctx context.Context, userID int64, badgeID int64) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		VALUES ($1, $2, NULL, '')
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING
		RETURNING id`, userID, badgeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("grant automatic badge: %w", err)
	}
	return true, nil
}

// GrantManual 手动授予：已持有返回 ErrBadgeAlreadyGranted。
func (s *PostgresStore) GrantManual(ctx context.Context, input GrantInput) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING
		RETURNING id`, input.UserID, input.BadgeID, nullInt64(input.GrantedBy), input.GrantReason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrBadgeAlreadyGranted
	}
	if err != nil {
		return false, fmt.Errorf("grant manual badge: %w", err)
	}
	return true, nil
}

// RevokeManual 收回授予行；不存在返回 false。
func (s *PostgresStore) RevokeManual(ctx context.Context, userID int64, badgeID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM user_badges WHERE user_id = $1 AND badge_id = $2`, userID, badgeID)
	if err != nil {
		return false, fmt.Errorf("revoke badge: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// wornBadgeStillValidSQL 判断「当前展示位那枚徽章是否仍然可以展示」。
//
// 三个条件与 `topBadgeOrderSQL` 的 JOIN 完全一致（enabled + 等级 enabled + 仍持有）：
// 排序查询排除掉的行，这里也必须判为失效，否则会出现「展示位保留了一枚排序里
// 根本不存在的徽章」——chip 不显示，而佩戴状态说在戴。
//
// 只依赖外层查询提供 `users`（外层别名）：片段内自带 `FROM user_badges AS worn`
// 并按 `worn.user_id = users.id` 关联，既不需要外层再提供 user_badges，也不吃 $1。
const wornBadgeStillValidSQL = `EXISTS (
	SELECT 1
	FROM user_badges AS worn
	JOIN badges AS worn_badge ON worn_badge.id = worn.badge_id AND worn_badge.enabled
	JOIN badge_tiers AS t ON t.key = worn_badge.tier AND t.enabled
	WHERE worn.user_id = users.id AND worn.badge_id = users.top_badge_id
)`

// RecomputeTopBadge 重算单个用户的展示徽章（可能落成 NULL）。
//
// **用户佩戴（`top_badge_manual`）优先**：只要那枚徽章仍然有效就不覆盖它，
// 否则「佩戴了，下次评估就变回去」。失效（停用/收回/删除/等级停用）时回落到
// 自动推导的下一枚，并同一条语句把标记清掉——两件事必须一起做，否则要么
// 展示位空着（chip 消失），要么标记留着（下次又拦住重算）。
//
// **hidden 让路**：`top_badge_hidden = TRUE` 时跳过回落写入（top_badge_id 保持
// NULL）——「什么都不戴」是用户的明确选择，回落会把它变成「偷偷替用户戴上」。
//
// 这里写 NULL 而不是 `users.top_badge_id`（保持原值）：`hidden = TRUE` 时展示位
// 的定义就是空，任何「hidden 但 top_badge_id 非空」的行都是半坏状态（chip 会显示
// 一枚徽章，而佩戴面板说「未佩戴」）。写 NULL 让单用户重算顺手把历史脏行收敛到
// 规范形态，而且对规范行是幂等的（本来就是 NULL）。
func (s *PostgresStore) RecomputeTopBadge(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET
			top_badge_manual = (users.top_badge_manual AND NOT users.top_badge_hidden AND `+wornBadgeStillValidSQL+`),
			top_badge_id = CASE
				WHEN users.top_badge_hidden THEN NULL
				WHEN users.top_badge_manual AND `+wornBadgeStillValidSQL+` THEN users.top_badge_id
				ELSE (
					SELECT badges.id
					FROM user_badges
					JOIN badges ON badges.id = user_badges.badge_id
					JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
					WHERE user_badges.user_id = $1 AND badges.enabled
					ORDER BY `+topBadgeOrderSQL+`
					LIMIT 1
				)
			END
		WHERE users.id = $1`, userID)
	if err != nil {
		return fmt.Errorf("recompute top badge: %w", err)
	}
	return nil
}

// grantFromRulesCollectLimit 是批量补授予时**收集**给通知层的行数上限。
//
// 通知在超过 `AdminService.recomputeNotifyLimit`（200）时整体不发，所以收集
// 上限只需「足够判出 200 已被突破」。取 1000 而不是 200 是给阈值调整留余量，
// 同时把十万行授予的内存占用压成常数。
//
// **1000 与 200 的关系是判据余量，不是第二道产品阈值**：超过 1000 条时这里只是
// 停止继续收集（`Granted` 仍是真实行数），不发通知的唯一判据永远是 200。
const grantFromRulesCollectLimit = 1000

// GrantFromRules 按启用规则批量补授予，返回本次新插入的行与需要异步评估的徽章。
//
// 为什么是一条 SQL 而不是逐用户调 evaluator：10 万用户 × 12 规则在 Go 里循环是
// 不可接受的，在 PG 里只是一次 join。这也是「运营调低阈值后点重算就能追溯授予」
// 这条承诺的唯一实现——只修 top_badge_id 的话，按钮在阈值场景下什么也不做。
//
// granted_by = NULL + grant_reason = 'recompute' 让授予记录能区分三种来源：
// 规则自动（NULL/”）、管理员手动（actor/理由）、批量重算（NULL/'recompute'）。
//
// `RETURNING user_id, badge_id` 而不是只看 RowsAffected：重算也要发授予通知，
// 而通知需要知道「谁拿到了哪一枚」。行的收集上限见 grantFromRulesCollectLimit。
func (s *PostgresStore) GrantFromRules(ctx context.Context) (GrantFromRulesResult, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		SELECT activity.user_id, rules.id, NULL, 'recompute'
		FROM user_activity AS activity
		JOIN badges AS rules
		  ON rules.enabled
		 AND rules.grant_mode = 'automatic'
		 AND `+fastPathRulePredicateSQL("rules")+`
		WHERE CASE rules.rule->'root'->'params'->>'metric'
		        WHEN 'topic_count'         THEN activity.topic_count
		        WHEN 'comment_count'       THEN activity.comment_count
		        WHEN 'like_received_count' THEN activity.like_received_count
		        WHEN 'like_given_count'    THEN activity.like_given_count
		        WHEN 'bookmark_count'      THEN activity.bookmark_count
		        WHEN 'days_visited'        THEN activity.days_visited::bigint
		      END >= (rules.rule->'root'->'cmp'->>'value')::bigint
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING
		RETURNING user_id, badge_id`)
	if err != nil {
		return GrantFromRulesResult{}, fmt.Errorf("grant badges from rules: %w", err)
	}
	defer rows.Close()
	result := GrantFromRulesResult{Rows: make([]RuleGrant, 0, 8)}
	for rows.Next() {
		var row RuleGrant
		if err := rows.Scan(&row.UserID, &row.BadgeID); err != nil {
			return GrantFromRulesResult{}, fmt.Errorf("scan rule grant: %w", err)
		}
		result.Granted++
		if len(result.Rows) < grantFromRulesCollectLimit {
			result.Rows = append(result.Rows, row)
		}
	}
	if err := rows.Err(); err != nil {
		return GrantFromRulesResult{}, fmt.Errorf("grant badges from rules: %w", err)
	}
	complexIDs, err := s.complexRuleBadgeIDs(ctx)
	if err != nil {
		return GrantFromRulesResult{}, err
	}
	result.ComplexBadgeIDs = complexIDs
	return result, nil
}

// fastPathRulePredicateSQL 必须与 Go 侧 `fastPathRule()` **完全等价**。
//
// 少一个条件就是语义 bug，而且是批量级的：只认 `type` + `window.kind` 而漏掉
// `cmp.op = 'gte'`，一条 `lte 5` 的规则会被这条 SQL 当成 `>= 5` 授予全站。
// 逐条对应：
//   - `op IS NULL`：组节点不是快路径（`all` 包一个叶子在语义上等价，但
//     `fastPathRule` 只认叶子根，SQL 跟着收窄——漏选只是变慢，误选是语义错）；
//   - `metric IN (...)`：与 `ActivityMetric.Valid()` 的六项白名单一致；
//   - `window.kind='all'` 且 `window.days IS NULL`：`fastPathRule` 对带 days 的 all 也拒绝；
//   - `cmp.op='gte'`、`value` 是数字且 > 0：与 `fastPathRule` 的 `value <= 0` 拒绝一致。
func fastPathRulePredicateSQL(alias string) string {
	return fmt.Sprintf(`
		     %[1]s.rule->>'v' = '1'
		 AND %[1]s.rule->'root'->>'op' IS NULL
		 AND %[1]s.rule->'root'->>'type' = 'activity.count'
		 AND %[1]s.rule->'root'->'params'->>'metric' IN (
		       'topic_count','comment_count','like_received_count',
		       'like_given_count','bookmark_count','days_visited')
		 AND %[1]s.rule->'root'->'params'->'window'->>'kind' = 'all'
		 AND %[1]s.rule->'root'->'params'->'window'->'days' IS NULL
		 AND %[1]s.rule->'root'->'cmp'->>'op' = 'gte'
		 AND jsonb_typeof(%[1]s.rule->'root'->'cmp'->'value') = 'number'
		 AND (%[1]s.rule->'root'->'cmp'->>'value') ~ '^[0-9]+$'
		 AND (%[1]s.rule->'root'->'cmp'->>'value')::bigint > 0`, alias)
}

// complexRuleBadgeIDs 返回「自动徽章里不能走快路径」的那些 id。
//
// `rule IS NULL` 不算：没有规则就没有可评估的东西，入队只会让 job 空转一遍；
// 这类脏数据由 `rule_health` 报出来。
func (s *PostgresStore) complexRuleBadgeIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM badges
		WHERE enabled AND grant_mode = 'automatic' AND rule IS NOT NULL
		  AND NOT (`+fastPathRulePredicateSQL("badges")+`)`)
	if err != nil {
		return nil, fmt.Errorf("list complex rule badges: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan complex rule badge: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RecomputeAll 全量重算展示徽章，返回 `top_badge_id` 实际变更的行数。
//
// 覆盖三类人：有授予行的（可能该升/该降）、`top_badge_id` 非空的（徽章被删/停用
// 后可能该清空）、以及佩戴了徽章的（佩戴值有效时**跳过**，失效时回落）。
// hidden 行同样跳过回落写入（「什么都不戴」是用户的明确选择）。
//
// `refreshed` 的语义没有扩大：仍然只数 `top_badge_id` 真的变了的行。佩戴标记的
// 清理搭同一批行的便车——失效的佩戴值必然让 `top_badge_id` 也变（排序里排除了
// 那枚徽章，回落的下一枚与它不同），所以不会出现「标记清了但没计数」的行。
//
// **hidden 行不进作用域**（`WHERE ... AND NOT users.top_badge_hidden`）：隐藏是
// 「不参与全量重算」而不是「参与但结果不变」。两者在数据上等价，在计数上不等价
// ——`IS DISTINCT FROM` 曾经按 `best.manual_valid=false` 拿 `best.badge_id` 比 NULL，
// 于是 hidden 行每次都命中 UPDATE（值不变），第二次全量重算仍报 refreshed=1。
// 选作用域闸门而不是「把 WHERE 的 CASE 镜像成 SET 的三分支」，理由是后者靠
// 「两份 CASE 恰好相等」推出不命中：将来改 SET 的分支（多一种展示态）而忘了同步
// WHERE，就会原地复现这个 bug，而且没有任何测试之外的现象能提示它。闸门只有一处
// 定义「谁属于重算域」，SET 里的 hidden 分支留作纵深防御（见那里的注释）。
func (s *PostgresStore) RecomputeAll(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users
		SET top_badge_manual = best.manual_valid,
		    top_badge_id = CASE
			    -- 纵深防御：上面的 WHERE 已把 hidden 行挡在作用域外，这一支正常不可达。
			    -- 保留它是因为「放宽 WHERE」是将来最可能的一处改动，而它一旦失守，
			    -- 回落写入会把「什么都不戴」变成「偷偷替用户戴上」，且没有任何报错。
			    WHEN users.top_badge_hidden THEN users.top_badge_id
			    WHEN best.manual_valid THEN users.top_badge_id
			    ELSE best.badge_id
		    END
		FROM (
			SELECT users.id AS user_id,
			       (SELECT badges.id
			        FROM user_badges
			        JOIN badges ON badges.id = user_badges.badge_id
			        JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
			        WHERE user_badges.user_id = users.id AND badges.enabled
			        ORDER BY `+topBadgeOrderSQL+`
			        LIMIT 1) AS badge_id,
			       (users.top_badge_manual AND NOT users.top_badge_hidden AND `+wornBadgeStillValidSQL+`) AS manual_valid
			FROM users
			WHERE users.top_badge_id IS NOT NULL
			   OR users.top_badge_manual
			   OR users.top_badge_hidden
			   OR EXISTS (SELECT 1 FROM user_badges WHERE user_badges.user_id = users.id)
		) AS best
		WHERE users.id = best.user_id
		  AND NOT users.top_badge_hidden
		  AND users.top_badge_id IS DISTINCT FROM
		      CASE WHEN best.manual_valid THEN users.top_badge_id ELSE best.badge_id END`)
	if err != nil {
		return 0, fmt.Errorf("recompute all top badges: %w", err)
	}
	return tag.RowsAffected(), nil
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

var _ Store = (*PostgresStore)(nil)

// pgconn 引用保证导入不被裁剪（nullInt64 之外的写路径都用 pgx）。
var _ pgconn.CommandTag
