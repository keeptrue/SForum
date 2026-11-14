package badges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// ValidateBadgeRule 是保存路径上的规则校验：AST 形状 + 引用存在性 + 循环依赖。
//
// 三段刻意分开，因为它们的**失败语义不同**：
//   - AST 校验（`ruleengine.Validate`）失败 → `forum.badge_rule_too_complex` /
//     `_unknown_condition`，属于「这条规则本身写错了」；
//   - 引用不存在 → `forum.badge_rule_reference_invalid`，属于「规则写得对，但指向的
//     对象没了」（分类被删、角色改名、徽章 slug 打错）；
//   - 成环 → `forum.badge_rule_cycle`，属于「规则之间互相依赖，永远算不出结果」。
//
// 评估期**不做**这三件事：引用失效在评估期返回确定的 0/false（「没做过」），
// 而成环在评估期不会死循环（`badge.owned` 只查已授予关系，不递归求值）。
func (s *PostgresStore) ValidateBadgeRule(ctx context.Context, badgeID int64, rule ruleengine.Node) error {
	if err := ruleengine.Validate(rule, conditions.Core()); err != nil {
		return fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, err)
	}
	if err := s.checkRuleReferences(ctx, rule); err != nil {
		return err
	}
	return s.checkBadgeRuleCycle(ctx, badgeID, rule)
}

// checkRuleReferences 校验规则里所有 objectRef 的引用存在性。
//
// 每一类引用都必须查**它自己的真值表**：`identity.role` 用 `roles.key`、
// `identity.oauth_link` 用 `identity_external_links.provider_id`、`badge.owned` 用
// `badges.slug`、`object.authoring` 按 targetKind 查 categories / tags、
// `object.engagement` 查 topics。只查一张表会把「类型写错但 slug 恰好存在」放过去。
func (s *PostgresStore) checkRuleReferences(ctx context.Context, rule ruleengine.Node) error {
	for _, leaf := range collectRuleLeaves(rule) {
		var query string
		var arg string
		switch leaf.Type {
		case conditions.KeyObjectAuthoring:
			kind := stringParamOf(leaf, "targetKind")
			arg = stringParamOf(leaf, "targetSlug")
			switch kind {
			case conditions.TargetKindCategory:
				query = `SELECT EXISTS (SELECT 1 FROM categories WHERE slug = $1)`
			case conditions.TargetKindTag:
				query = `SELECT EXISTS (SELECT 1 FROM tags WHERE slug = $1)`
			default:
				return fmt.Errorf("%w: object.authoring 的 targetKind %q 非法", ErrBadgeRuleReferenceInvalid, kind)
			}
		case conditions.KeyIdentityRole:
			query = `SELECT EXISTS (SELECT 1 FROM roles WHERE key = $1)`
			arg = stringParamOf(leaf, "roleKey")
		case conditions.KeyIdentityOAuthLink:
			// 保存期与 `rule_health` 共用**同一个**判据：「绑定表 ∪ identity registry
			// 已声明」。计划 §5.2.1 原文就是「值必须能对上
			// `identity_external_links.provider_id` **或** identity registry 的已声明
			// provider」——只查绑定表会让候选面里唯一的 provider 在零绑定时存不进去
			// （界面能选、保存必 422，候选与保存自相矛盾）；完全不校验则会让拼错的 id
			// 落库、永不命中。失效语义仍由 `rule_health.missing_provider` 承担：
			// 规则是运营资产，存得下、健康页提醒（与 fact.count 未声明 key 同一条纪律）。
			known, err := s.knownProviders(ctx)
			if err != nil {
				return fmt.Errorf("check rule reference: %w", err)
			}
			providerID := stringParamOf(leaf, "providerId")
			if !known[providerID] {
				return fmt.Errorf("%w: %s 的引用 %q 不存在", ErrBadgeRuleReferenceInvalid, leaf.Type, providerID)
			}
			continue
		case conditions.KeyBadgeOwned:
			query = `SELECT EXISTS (SELECT 1 FROM badges WHERE slug = $1)`
			arg = stringParamOf(leaf, "slug")
		case conditions.KeyFactCount:
			// 只校验 variant，不校验 factKey 是否存在：
			//   - factKey 失效（扩展停用/卸载）时必须**放行保存**——规则是运营资产，
			//     目录没了不许让人连改都改不了；失效由 rule_health 展示、求值期 unknown；
			//   - variant 拼错则是另一回事：`factKey` 明明有已声明变体，写一个不存在的
			//     值只会永远不满足，而且没有任何报错。声明了候选就按候选收口。
			factKey := stringParamOf(leaf, "factKey")
			variant := stringParamOf(leaf, "variant")
			if s.factCandidates != nil && !conditions.VariantDeclared(s.factCandidates(), factKey, variant) {
				return fmt.Errorf("%w: 事实 %q 未声明 variant %q", ErrBadgeRuleReferenceInvalid, factKey, variant)
			}
			continue
		case conditions.KeyObjectEngagement:
			topicID, ok := intParamOf(leaf, "topicId")
			if !ok {
				return fmt.Errorf("%w: object.engagement 的 topicId 非法", ErrBadgeRuleReferenceInvalid)
			}
			var exists bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topics WHERE id = $1)`, topicID).Scan(&exists); err != nil {
				return fmt.Errorf("check rule reference: %w", err)
			}
			if !exists {
				return fmt.Errorf("%w: 主题 %d 不存在", ErrBadgeRuleReferenceInvalid, topicID)
			}
			continue
		default:
			continue
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, query, arg).Scan(&exists); err != nil {
			return fmt.Errorf("check rule reference: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: %s 的引用 %q 不存在", ErrBadgeRuleReferenceInvalid, leaf.Type, arg)
		}
	}
	return nil
}

// checkBadgeRuleCycle 检测 `badge.owned` 依赖环。
//
// 三组必须都拒绝：**自引用（A→A）**、2 枚互指（A→B→A）、3 枚成环（A→B→C→A）。
// 同时必须**放行菱形依赖**（A→B、A→C、B→D、C→D）：那不是环，只是共享下游。
//
// 所以判环用「**当前路径集合**」而不是「已访问集合」：用已访问集合会把菱形依赖
// 误判成环，这是图判环的经典区分，也是这段代码最容易图省事写错的地方。
func (s *PostgresStore) checkBadgeRuleCycle(ctx context.Context, badgeID int64, rule ruleengine.Node) error {
	edges := make(map[int64][]int64) // badgeID → 它规则里 badge.owned 指向的徽章
	selfSlugs := badgeOwnedSlugs(rule)
	if len(selfSlugs) == 0 {
		return nil
	}
	roots, err := s.badgeIDsBySlug(ctx, selfSlugs)
	if err != nil {
		return err
	}
	for _, slug := range selfSlugs {
		if _, ok := roots[slug]; !ok {
			return fmt.Errorf("%w: 徽章 %q 不存在", ErrBadgeRuleReferenceInvalid, slug)
		}
	}
	onPath := map[int64]bool{badgeID: true}
	done := map[int64]bool{}
	for _, id := range roots {
		if err := s.walkBadgeOwned(ctx, badgeID, id, edges, onPath, done); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) walkBadgeOwned(ctx context.Context, target int64, current int64, edges map[int64][]int64, onPath map[int64]bool, done map[int64]bool) error {
	if current == target {
		return fmt.Errorf("%w: 徽章 %d 的规则依赖自己（直接或间接）", ErrBadgeRuleCycle, target)
	}
	if done[current] {
		return nil
	}
	if onPath[current] {
		// 回到当前路径上的节点：环（且不是指向 target 的环，target 已在上面判过）。
		return fmt.Errorf("%w: 徽章依赖成环", ErrBadgeRuleCycle)
	}
	onPath[current] = true
	defer delete(onPath, current)

	neighbours, cached := edges[current]
	if !cached {
		loaded, err := s.badgeOwnedTargets(ctx, current)
		if err != nil {
			return err
		}
		neighbours = loaded
		edges[current] = neighbours
	}
	for _, next := range neighbours {
		if err := s.walkBadgeOwned(ctx, target, next, edges, onPath, done); err != nil {
			return err
		}
	}
	done[current] = true
	return nil
}

// badgeOwnedTargets 读取某枚徽章已保存规则里的 badge.owned 依赖。
func (s *PostgresStore) badgeOwnedTargets(ctx context.Context, badgeID int64) ([]int64, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT rule FROM badges WHERE id = $1`, badgeID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || len(raw) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load badge rule: %w", err)
	}
	node, err := ruleengine.Decode(raw)
	if err != nil {
		// 已存规则解不开：当作没有依赖（保存期不因历史脏数据阻塞新保存）。
		return nil, nil
	}
	return s.badgeIDsBySlugList(ctx, badgeOwnedSlugs(node))
}

func (s *PostgresStore) badgeIDsBySlug(ctx context.Context, slugs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(slugs))
	rows, err := s.pool.Query(ctx, `SELECT slug, id FROM badges WHERE slug = ANY($1)`, slugs)
	if err != nil {
		return nil, fmt.Errorf("resolve badge slugs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var id int64
		if err := rows.Scan(&slug, &id); err != nil {
			return nil, fmt.Errorf("scan badge slug: %w", err)
		}
		out[slug] = id
	}
	return out, rows.Err()
}

func (s *PostgresStore) badgeIDsBySlugList(ctx context.Context, slugs []string) ([]int64, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	resolved, err := s.badgeIDsBySlug(ctx, slugs)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(resolved))
	for _, slug := range slugs {
		if id, ok := resolved[slug]; ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// collectRuleLeaves 展平树上的全部叶子。
func collectRuleLeaves(node ruleengine.Node) []ruleengine.Node {
	if node.IsGroup() {
		out := make([]ruleengine.Node, 0, len(node.Children))
		for _, child := range node.Children {
			out = append(out, collectRuleLeaves(child)...)
		}
		return out
	}
	if node.Type == "" {
		return nil
	}
	return []ruleengine.Node{node}
}

// badgeOwnedSlugs 取出规则里所有 `badge.owned` 的 slug（去重）。
func badgeOwnedSlugs(node ruleengine.Node) []string {
	seen := map[string]bool{}
	var out []string
	for _, leaf := range collectRuleLeaves(node) {
		if leaf.Type != conditions.KeyBadgeOwned {
			continue
		}
		slug := stringParamOf(leaf, "slug")
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

func stringParamOf(leaf ruleengine.Node, name string) string {
	value, _ := leaf.Params[name].(string)
	return value
}

func intParamOf(leaf ruleengine.Node, name string) (int64, bool) {
	switch typed := leaf.Params[name].(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// ListActiveUserIDsAfter 按 id 升序取一批活跃用户，供回扫游标推进。
//
// 用 `id > cursor` 而不是 OFFSET：回扫期间的用户增删不会让某一页漏人或重复。
func (s *PostgresStore) ListActiveUserIDsAfter(ctx context.Context, afterUserID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM users
		WHERE status = 'active' AND id > $1
		ORDER BY id
		LIMIT $2`, afterUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list users for backfill: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan backfill user: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetBadgeRule 读取一枚徽章的规则 AST。
//
// 返回 ErrBadgeNotFound 表示徽章不存在；规则为空（手工徽章）返回 ErrBadgeRuleInvalid——
// 调用方（影响估算）不该对一条不存在的规则给出数字。
func (s *PostgresStore) GetBadgeRule(ctx context.Context, badgeID int64) (ruleengine.Node, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT rule FROM badges WHERE id = $1`, badgeID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ruleengine.Node{}, ErrBadgeNotFound
	}
	if err != nil {
		return ruleengine.Node{}, fmt.Errorf("load badge rule: %w", err)
	}
	if len(raw) == 0 {
		return ruleengine.Node{}, ErrBadgeRuleInvalid
	}
	node, err := ruleengine.Decode(raw)
	if err != nil {
		return ruleengine.Node{}, fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, err)
	}
	return node, nil
}

// decodeRuleQuietly 解码库里读出的规则；解不开时返回 nil。
//
// 读路径不能因为一条脏规则整页失败（与 `scanBadge` 同一取舍）：规则解不开的徽章
// 在公开面表现为「没有自动生成的说明」，`rule_health` 会单独报 undecodable_rule。
func decodeRuleQuietly(raw []byte, slug string) *ruleengine.Node {
	if len(raw) == 0 {
		return nil
	}
	node, err := ruleengine.Decode(raw)
	if err != nil {
		// 降级必须可观测：否则「某枚徽章突然没有获得说明」永远查不到原因。
		slog.Default().Warn("badge rule is not decodable; requirement text omitted",
			"badge_slug", slug, "error", err)
		return nil
	}
	return &node
}
