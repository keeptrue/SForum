package conditions

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// objectEngagementSQL 一次算完本棵树里所有 (action, topicId) 组合。
//
// 三个动作的语义（与成就口径一致，都**不**过滤 status）：
//   - like：该用户对这条主题点过赞；
//   - bookmark：该用户收藏过这条主题，**不看 visibility**。写路径
//     `bumpUserActivityTx` 按 relationDelta 计数、`Recompute` 数全部行，两处都不按
//     可见性过滤；只算 public 会让「收藏家」几乎不可达（visibility 默认 private）。
//   - comment：该用户在这条主题下评论过。**算自己评论自己的主题**（P-11 裁决）：
//     规则层无法表达「主题作者 ≠ 我」，加隐藏例外会让条件「看着满足却不发」。
const objectEngagementSQL = `
SELECT a.action, a.topic_id,
       CASE a.action
         WHEN 'like' THEN EXISTS (
           SELECT 1 FROM content_reactions r
            WHERE r.user_id = $1 AND r.target_type = 'topic'
              AND r.target_id = a.topic_id AND r.reaction = 'like')
         WHEN 'bookmark' THEN EXISTS (
           SELECT 1 FROM content_bookmarks b
            WHERE b.user_id = $1 AND b.target_type = 'topic'
              AND b.target_id = a.topic_id)
         WHEN 'comment' THEN EXISTS (
           SELECT 1 FROM comments c
            WHERE c.author_user_id = $1 AND c.topic_id = a.topic_id)
       END
FROM unnest($2::text[], $3::bigint[]) AS a(action, topic_id)
`

func (r *Resolver) loadObjectEngagement(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	actions := make([]string, 0, len(leaves))
	topics := make([]int64, 0, len(leaves))
	for _, leaf := range leaves {
		topicID, ok := intParam(leaf, "topicId")
		if !ok {
			return nil, fmt.Errorf("conditions: object.engagement 的 topicId 非法")
		}
		actions = append(actions, stringParam(leaf, "action"))
		topics = append(topics, topicID)
	}

	rows, err := r.pool.Query(ctx, objectEngagementSQL, r.userID, actions, topics)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 object.engagement 失败：%w", err)
	}
	defer rows.Close()

	matched := make(map[string]bool, len(leaves))
	for rows.Next() {
		var action string
		var topicID int64
		var hit bool
		if err := rows.Scan(&action, &topicID, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 object.engagement 结果失败：%w", err)
		}
		matched[action+"|"+fmt.Sprint(topicID)] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 object.engagement 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		topicID, _ := intParam(leaf, "topicId")
		hit, ok := matched[stringParam(leaf, "action")+"|"+fmt.Sprint(topicID)]
		if !ok {
			return nil, fmt.Errorf("conditions: object.engagement 缺少 %q/%d 的结果", stringParam(leaf, "action"), topicID)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}

// objectAuthoringSQL 一次算完本棵树里所有 (targetKind, targetSlug, window) 组合。
//
// category 走 `topics.category_id → categories.slug`，tag 走
// `topics → topic_tags → tags.slug`。两者都不按主题状态过滤（成就口径）。
const objectAuthoringSQL = `
SELECT a.kind, a.slug, a.days,
       CASE a.kind
         WHEN 'category' THEN (
           SELECT count(*) FROM topics t
             JOIN categories c ON c.id = t.category_id
            WHERE t.author_user_id = $1 AND c.slug = a.slug
              AND (a.days IS NULL OR t.created_at >= now() - make_interval(days => a.days::int)))
         WHEN 'tag' THEN (
           SELECT count(*) FROM topics t
             JOIN topic_tags tt ON tt.topic_id = t.id
             JOIN tags tg ON tg.id = tt.tag_id
            WHERE t.author_user_id = $1 AND tg.slug = a.slug
              AND (a.days IS NULL OR t.created_at >= now() - make_interval(days => a.days::int)))
       END
FROM unnest($2::text[], $3::text[], $4::bigint[]) AS a(kind, slug, days)
`

func (r *Resolver) loadObjectAuthoring(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	kinds := make([]string, 0, len(leaves))
	slugs := make([]string, 0, len(leaves))
	days := make([]*int64, 0, len(leaves))
	for _, leaf := range leaves {
		window, err := windowDays(leaf)
		if err != nil {
			return nil, err
		}
		kinds = append(kinds, stringParam(leaf, "targetKind"))
		slugs = append(slugs, stringParam(leaf, "targetSlug"))
		days = append(days, window)
	}

	rows, err := r.pool.Query(ctx, objectAuthoringSQL, r.userID, kinds, slugs, days)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 object.authoring 失败：%w", err)
	}
	defer rows.Close()

	type authoringKey struct {
		kind string
		slug string
		days int64
	}
	counts := make(map[authoringKey]int64, len(leaves))
	for rows.Next() {
		var kind, slug string
		var window *int64
		var count *int64
		if err := rows.Scan(&kind, &slug, &window, &count); err != nil {
			return nil, fmt.Errorf("conditions: 读取 object.authoring 结果失败：%w", err)
		}
		key := authoringKey{kind: kind, slug: slug, days: -1}
		if window != nil {
			key.days = *window
		}
		if count != nil {
			counts[key] = *count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 object.authoring 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for index, leaf := range leaves {
		key := authoringKey{kind: kinds[index], slug: slugs[index], days: -1}
		if days[index] != nil {
			key.days = *days[index]
		}
		count, ok := counts[key]
		if !ok {
			return nil, fmt.Errorf("conditions: object.authoring 缺少 %v 的结果（引用无效？）", key)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(count)
	}
	return out, nil
}
