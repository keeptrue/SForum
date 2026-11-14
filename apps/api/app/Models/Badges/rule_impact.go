package badges

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RuleImpact 是规则影响面的**估算**。
//
// 语义就是「≈」：AND 取最严叶子、OR 取和，两者对「同时满足」的真实人数都只是
// **上界**（叶子之间通常相关，人数不会相加）。`Method` 存在的唯一目的，是让前端
// 能如实说「≈ 120 人（按最严条件估算）」——写成精确数字，运营会拿它跟实际授予数
// 对账，然后来问为什么不一样。
type RuleImpact struct {
	// Estimated 是估算人数；nil 表示**无法估算**（不是 0 人）。
	//
	// 用 0 表达「算不出来」会让运营读成「这规则没人能达标」，然后去改一条本来正确的
	// 规则。这与 factKey 上那条原则是同一条：「查不到数据」与「无法判定」必须用不同
	// 的值表达。
	Estimated *int64 `json:"estimated"`
	Method    string `json:"method"`
	// Partial 表示估算只用了部分叶子（AND 里有不可估的条件）。
	Partial bool `json:"partial"`
	// UnsupportedConditions 列出参与规则但无法估算的条件 key。
	UnsupportedConditions []string `json:"unsupportedConditions"`
}

// 估算方法标识。前端按它选文案，所以是契约。
const (
	impactMethodSingleLeaf    = "single_leaf"
	impactMethodStrictestLeaf = "strictest_leaf"
	impactMethodSumOfLeaves   = "sum_of_leaves"
	impactMethodNotLeaf       = "complement_of_leaf"
	impactMethodUnsupported   = "unsupported_condition"
	// impactMethodStrictestLeafPartial：AND 里部分叶子不可估，但 supported 的最严项
	// 仍是合法上界（true_count ≤ min(supported)）。
	impactMethodStrictestLeafPartial = "strictest_leaf_partial"
)

// EstimateRuleImpact 估算一条规则会覆盖多少人。
//
// 合成规则（都是**上界**，不是精确值）：
//   - AND → 取最小（最严叶子）；部分叶子不可估时仍给上界 + `strictest_leaf_partial`；
//   - OR  → 求和；**任一叶子不可估就返回 null**（「≤ 全部活跃用户」对决策零信息量，
//     不该占一个看起来确定的数字位）；
//   - NOT → 总人数 − 叶子人数；叶子不可估则 null。
func (s *PostgresStore) EstimateRuleImpact(ctx context.Context, rule ruleengine.Node) (RuleImpact, error) {
	total, err := s.countUsers(ctx)
	if err != nil {
		return RuleImpact{}, err
	}
	estimate, method, partial, unsupported, err := s.estimateNode(ctx, rule, total)
	if err != nil {
		return RuleImpact{}, err
	}
	if estimate != nil {
		value := *estimate
		if value < 0 {
			value = 0
		}
		if value > total {
			value = total
		}
		estimate = &value
	}
	return RuleImpact{Estimated: estimate, Method: method, Partial: partial, UnsupportedConditions: unsupported}, nil
}

func (s *PostgresStore) estimateNode(ctx context.Context, node ruleengine.Node, total int64) (*int64, string, bool, []string, error) {
	if node.IsLeaf() {
		count, ok, err := s.countLeafMatches(ctx, node, total)
		if err != nil {
			return nil, "", false, nil, err
		}
		if !ok {
			return nil, impactMethodUnsupported, false, []string{node.Type}, nil
		}
		return &count, impactMethodSingleLeaf, false, nil, nil
	}
	switch node.Op {
	case ruleengine.OpAll:
		var strictest *int64
		var unsupported []string
		for _, child := range node.Children {
			count, _, _, childUnsupported, err := s.estimateNode(ctx, child, total)
			if err != nil {
				return nil, "", false, nil, err
			}
			unsupported = append(unsupported, childUnsupported...)
			if count == nil {
				continue
			}
			if strictest == nil || *count < *strictest {
				strictest = count
			}
		}
		if strictest == nil {
			return nil, impactMethodUnsupported, false, unsupported, nil
		}
		if len(unsupported) > 0 {
			return strictest, impactMethodStrictestLeafPartial, true, unsupported, nil
		}
		return strictest, impactMethodStrictestLeaf, false, nil, nil
	case ruleengine.OpAny:
		sum := int64(0)
		var unsupported []string
		for _, child := range node.Children {
			count, _, _, childUnsupported, err := s.estimateNode(ctx, child, total)
			if err != nil {
				return nil, "", false, nil, err
			}
			unsupported = append(unsupported, childUnsupported...)
			if count == nil {
				// OR 里有一个不可估 → 整体不可估（不拿总人数凑上界）。
				return nil, impactMethodUnsupported, false, unsupported, nil
			}
			sum += *count
		}
		return &sum, impactMethodSumOfLeaves, false, nil, nil
	case ruleengine.OpNot:
		if len(node.Children) != 1 {
			return nil, impactMethodUnsupported, false, nil, nil
		}
		count, _, _, unsupported, err := s.estimateNode(ctx, node.Children[0], total)
		if err != nil {
			return nil, "", false, nil, err
		}
		if count == nil {
			return nil, impactMethodUnsupported, false, unsupported, nil
		}
		complement := total - *count
		return &complement, impactMethodNotLeaf, false, nil, nil
	default:
		return nil, impactMethodUnsupported, false, nil, nil
	}
}

// countLeafMatches 数「满足该叶子的人数」。第二返回值 false 表示这类条件算不出来。
//
// 每个条件都要写成**聚合**形式（`count(DISTINCT ...)`），不要逐用户求值：估算发生
// 在运营点开弹窗的那一刻，逐用户跑一遍引擎会把一个只读接口变成全表扫描。
func (s *PostgresStore) countLeafMatches(ctx context.Context, leaf ruleengine.Node, total int64) (int64, bool, error) {
	metric := stringParamOf(leaf, "metric")
	threshold := int64(0)
	if leaf.Cmp != nil {
		threshold = leaf.Cmp.Value
	}
	switch leaf.Type {
	case conditions.KeyActivityCount:
		window, err := windowDaysOf(leaf)
		if err != nil {
			return 0, false, nil
		}
		if window == nil {
			// 累计：直接数 user_activity 的反规范化列（与快路径同一真值位置）。
			column, ok := activityColumn(metric)
			if !ok || leaf.Cmp == nil || leaf.Cmp.Op != ruleengine.CompareGTE {
				return 0, false, nil
			}
			query := fmt.Sprintf(`SELECT count(*) FROM user_activity WHERE %s >= $1`, column)
			count, err := s.countQuery(ctx, query, threshold)
			return count, true, err
		}
		// 窗口：数「窗口内有 >= N 次」的用户。这里只支持 gte，其它比较退化为不可估。
		if leaf.Cmp == nil || leaf.Cmp.Op != ruleengine.CompareGTE {
			return 0, false, nil
		}
		query, ok := activityWindowCountSQL(metric)
		if !ok {
			return 0, false, nil
		}
		count, err := s.countQuery(ctx, query, threshold, *window)
		return count, true, err
	case conditions.KeyContentPinned:
		count, err := s.countQuery(ctx, `SELECT count(DISTINCT author_user_id) FROM topics WHERE is_pinned AND author_user_id IS NOT NULL`, nil)
		return count, true, err
	case conditions.KeyContentHidden:
		count, err := s.countQuery(ctx, `SELECT count(DISTINCT author_user_id) FROM topics WHERE status = 'hidden' AND author_user_id IS NOT NULL`, nil)
		return count, true, err
	case conditions.KeyIdentityEmailVerify:
		count, err := s.countQuery(ctx, `SELECT count(*) FROM users WHERE email_verified_at IS NOT NULL AND status = 'active'`, nil)
		return count, true, err
	case conditions.KeyIdentityAvatar:
		count, err := s.countQuery(ctx, `SELECT count(DISTINCT profiles.user_id) FROM user_profiles AS profiles JOIN users ON users.id = profiles.user_id WHERE profiles.avatar_attachment_id IS NOT NULL AND users.status = 'active'`, nil)
		return count, true, err
	case conditions.KeyIdentityRole:
		key := stringParamOf(leaf, "roleKey")
		count, err := s.countQuery(ctx, `SELECT count(DISTINCT user_roles.user_id) FROM user_roles JOIN roles ON roles.id = user_roles.role_id WHERE roles.key = $1 AND roles.is_enabled`, key)
		return count, true, err
	case conditions.KeyBadgeOwned:
		slug := stringParamOf(leaf, "slug")
		count, err := s.countQuery(ctx, `SELECT count(DISTINCT user_badges.user_id) FROM user_badges JOIN badges ON badges.id = user_badges.badge_id WHERE badges.slug = $1`, slug)
		return count, true, err
	case conditions.KeyIdentityAccountAge:
		count, err := s.countQuery(ctx, `SELECT count(*) FROM users WHERE (CURRENT_DATE - created_at::date) >= $1 AND status = 'active'`, threshold)
		return count, true, err
	case conditions.KeyObjectAuthoring, conditions.KeyObjectEngagement, conditions.KeyIdentityOAuthLink, conditions.KeyActivityStreak, conditions.KeyFactCount:
		// 这些形状的人数需要「按窗口/按对象」的额外语义，M1 不给估算：
		// 返回「估不了」比给一个偏差很大的数字安全（运营会拿它对账）。
		return 0, false, nil
	default:
		return 0, false, nil
	}
}

func activityColumn(metric string) (string, bool) {
	switch metric {
	case conditions.MetricTopicCount, conditions.MetricCommentCount,
		conditions.MetricLikeReceivedCount, conditions.MetricLikeGivenCount,
		conditions.MetricBookmarkCount, conditions.MetricDaysVisited:
		return metric, true
	default:
		return "", false
	}
}

// activityWindowCountSQL 返回「窗口内次数 >= $1 的用户数」。
//
// 复用条件解析器的真值表口径（软删仍计入、获赞排除自赞），只是把「一个用户」
// 换成「按作者分组数」。
func activityWindowCountSQL(metric string) (string, bool) {
	base := `SELECT count(*) FROM (
	  SELECT actor.user_id, count(*) AS n FROM (%s) AS actor GROUP BY actor.user_id
	) AS per_user WHERE per_user.n >= $1`
	var source string
	switch metric {
	case conditions.MetricTopicCount:
		source = `SELECT author_user_id AS user_id FROM topics WHERE created_at >= now() - make_interval(days => $2::int) AND author_user_id IS NOT NULL`
	case conditions.MetricCommentCount:
		source = `SELECT author_user_id AS user_id FROM comments WHERE created_at >= now() - make_interval(days => $2::int) AND author_user_id IS NOT NULL`
	case conditions.MetricLikeGivenCount:
		source = `SELECT user_id FROM content_reactions WHERE reaction = 'like' AND created_at >= now() - make_interval(days => $2::int)`
	case conditions.MetricBookmarkCount:
		source = `SELECT user_id FROM content_bookmarks WHERE created_at >= now() - make_interval(days => $2::int)`
	case conditions.MetricLikeReceivedCount:
		// 自赞不计入（与窗口计数同口径）。
		source = `SELECT topics.author_user_id AS user_id FROM content_reactions
		            JOIN topics ON topics.id = content_reactions.target_id
		           WHERE content_reactions.target_type = 'topic' AND content_reactions.reaction = 'like'
		             AND content_reactions.user_id <> topics.author_user_id
		             AND content_reactions.created_at >= now() - make_interval(days => $2::int)
		             AND topics.author_user_id IS NOT NULL`
	default:
		return "", false
	}
	return fmt.Sprintf(base, source), true
}

func (s *PostgresStore) countUsers(ctx context.Context) (int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE status = 'active'`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count active users: %w", err)
	}
	return total, nil
}

func (s *PostgresStore) countQuery(ctx context.Context, query string, args ...any) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("estimate rule impact: %w", err)
	}
	return count, nil
}

// windowDaysOf 解析叶子的 window 参数：nil = 全部时间。
func windowDaysOf(leaf ruleengine.Node) (*int64, error) {
	raw, present := leaf.Params["window"]
	if !present || raw == nil {
		return nil, nil
	}
	window, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("badges: window 不是对象")
	}
	kind, _ := window["kind"].(string)
	if kind != ruleengine.WindowKindRecentDays {
		return nil, nil
	}
	days, ok := window["days"].(float64)
	if !ok {
		if value, okInt := window["days"].(int64); okInt {
			return &value, nil
		}
		return nil, fmt.Errorf("badges: window.days 非法")
	}
	value := int64(days)
	return &value, nil
}
