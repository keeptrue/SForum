package badges

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 失效种类。前端按 kind 决定提示文案与修复入口，所以枚举值即契约。
const (
	IssueMissingTopic     = "missing_topic"
	IssueMissingCategory  = "missing_category"
	IssueMissingTag       = "missing_tag"
	IssueMissingRole      = "missing_role"
	IssueMissingProvider  = "missing_provider"
	IssueMissingBadgeSlug = "missing_badge_slug"
	IssueMissingFactKey   = "missing_fact_key"
	// IssueUndecodableRule：JSONB 解不开（修法 = 重新编辑这条规则）。
	IssueUndecodableRule = "undecodable_rule"
	// IssueInvalidRuleShape：能解码但 `ruleengine.Validate` 不过（如空 `all`）。
	// 与 undecodable 分开是因为**修法不同**：这里要补上缺的那个条件。
	// `path` 取首个错误的 Path、`ref` 取首个错误的错误码（前端有
	// `admin.badges.rule.errors.<code>` 可直接渲染）、`label` 留空。
	IssueInvalidRuleShape = "invalid_rule_shape"
)

// RuleIssue 是规则里的一处失效引用。`Path` 与 `ruleengine.Error.Path` 同一套坐标
// （`root.children[1].params.topicId`），前端可以直接用它定位到编辑器里的节点。
type RuleIssue struct {
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

// BadgeRuleHealth 是一枚徽章的健康结论。Issues 为空表示健康。
type BadgeRuleHealth struct {
	BadgeID int64       `json:"badgeId"`
	Slug    string      `json:"slug"`
	Name    string      `json:"name"`
	Issues  []RuleIssue `json:"issues"`
}

// DeclaredProviderFunc 返回 identity registry 里**已声明**的 provider id。
//
// 与 `identity_external_links` 的区别：那个表只记「有人绑定过」。插件刚装、还没人
// 绑定时，只查表会把所有引用它的规则误报成失效——所以判定要「表 ∪ 已声明」。
type DeclaredProviderFunc func(ctx context.Context) ([]string, error)

// SetDeclaredProviders 注入已声明 provider 来源（bootstrap 装配；未注入时只认绑定表）。
func (s *PostgresStore) SetDeclaredProviders(source DeclaredProviderFunc) {
	if s == nil {
		return
	}
	s.declaredProviders = source
}

// RuleHealth 扫描启用中的自动徽章，列出**引用或条件失效**的项。
//
// 刻意**不列「零授予」**：一枚规则正确、只是暂时没人达标的徽章是健康的，把它混进来
// 会让健康页永远有几十条噪音，真正的失效反而看不见。空 `items` 是正常结果。
func (s *PostgresStore) RuleHealth(ctx context.Context) ([]BadgeRuleHealth, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, rule FROM badges
		WHERE enabled AND grant_mode = 'automatic'
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list automatic badges for health: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		id   int64
		slug string
		name string
		raw  []byte
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.slug, &item.name, &item.raw); err != nil {
			return nil, fmt.Errorf("scan badge for health: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	providers, err := s.knownProviders(ctx)
	if err != nil {
		return nil, err
	}
	report := make([]BadgeRuleHealth, 0, len(candidates))
	for _, item := range candidates {
		issues := make([]RuleIssue, 0, 2)
		if len(item.raw) == 0 {
			continue // 自动徽章没有 rule 属脏数据，但健康页只报「引用/条件失效」。
		}
		node, err := ruleengine.Decode(item.raw)
		if err != nil {
			issues = append(issues, RuleIssue{
				Kind: IssueUndecodableRule, Path: "", Ref: truncate(string(item.raw), 200), Label: item.slug,
			})
			report = append(report, BadgeRuleHealth{BadgeID: item.id, Slug: item.slug, Name: item.name, Issues: issues})
			continue
		}
		// 「能解码」不等于「能用」：Decode 管形状、Validate 管语义，健康页要报的是
		// 「这条规则实际会发生什么」，所以必须再跑一次 Validate。
		if validateErr := ruleengine.Validate(node, conditions.Core()); validateErr != nil {
			code, path := ruleengine.CodeOf(validateErr), ""
			var ruleErr *ruleengine.Error
			if errors.As(validateErr, &ruleErr) {
				path = ruleErr.Path
			}
			issues = append(issues, RuleIssue{Kind: IssueInvalidRuleShape, Path: path, Ref: code, Label: ""})
		}
		if err := s.collectRuleIssues(ctx, node, "root", providers, &issues); err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			sortRuleIssues(issues)
			report = append(report, BadgeRuleHealth{BadgeID: item.id, Slug: item.slug, Name: item.name, Issues: issues})
		}
	}
	return report, nil
}

// collectRuleIssues 递归收集引用失效项，path 与 ruleengine 的坐标一致。
func (s *PostgresStore) collectRuleIssues(ctx context.Context, node ruleengine.Node, path string, providers map[string]bool, issues *[]RuleIssue) error {
	if node.IsGroup() {
		for index, child := range node.Children {
			if err := s.collectRuleIssues(ctx, child, fmt.Sprintf("%s.children[%d]", path, index), providers, issues); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Type == "" {
		return nil
	}
	paramPath := func(name string) string { return path + ".params." + name }

	switch node.Type {
	case conditions.KeyObjectAuthoring:
		kind := stringParamOf(node, "targetKind")
		slug := stringParamOf(node, "targetSlug")
		switch kind {
		case conditions.TargetKindCategory:
			ok, err := s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM categories WHERE slug = $1)`, slug)
			if err != nil {
				return err
			}
			if !ok {
				*issues = append(*issues, RuleIssue{Kind: IssueMissingCategory, Path: paramPath("targetSlug"), Ref: slug, Label: "分类 " + slug})
			}
		case conditions.TargetKindTag:
			ok, err := s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE slug = $1)`, slug)
			if err != nil {
				return err
			}
			if !ok {
				*issues = append(*issues, RuleIssue{Kind: IssueMissingTag, Path: paramPath("targetSlug"), Ref: slug, Label: "标签 " + slug})
			}
		}
	case conditions.KeyObjectEngagement:
		topicID, ok := intParamOf(node, "topicId")
		if !ok {
			return nil
		}
		exists, err := s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM topics WHERE id = $1)`, topicID)
		if err != nil {
			return err
		}
		if !exists {
			*issues = append(*issues, RuleIssue{
				Kind: IssueMissingTopic, Path: paramPath("topicId"), Ref: fmt.Sprint(topicID), Label: fmt.Sprintf("主题 #%d", topicID),
			})
		}
	case conditions.KeyIdentityRole:
		key := stringParamOf(node, "roleKey")
		exists, err := s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE key = $1)`, key)
		if err != nil {
			return err
		}
		if !exists {
			*issues = append(*issues, RuleIssue{Kind: IssueMissingRole, Path: paramPath("roleKey"), Ref: key, Label: "角色 " + key})
		}
	case conditions.KeyIdentityOAuthLink:
		provider := stringParamOf(node, "providerId")
		if !providers[provider] {
			*issues = append(*issues, RuleIssue{Kind: IssueMissingProvider, Path: paramPath("providerId"), Ref: provider, Label: "登录方式 " + provider})
		}
	case conditions.KeyBadgeOwned:
		slug := stringParamOf(node, "slug")
		exists, err := s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM badges WHERE slug = $1)`, slug)
		if err != nil {
			return err
		}
		if !exists {
			*issues = append(*issues, RuleIssue{Kind: IssueMissingBadgeSlug, Path: paramPath("slug"), Ref: slug, Label: "徽章 " + slug})
		}
	case conditions.KeyFactCount:
		key := stringParamOf(node, "factKey")
		declared := map[string]bool{}
		if s.factKeys != nil {
			for _, item := range s.factKeys() {
				declared[item] = true
			}
		}
		if !declared[key] {
			*issues = append(*issues, RuleIssue{Kind: IssueMissingFactKey, Path: paramPath("factKey"), Ref: key, Label: "插件事实 " + key})
		}
	}
	return nil
}

// knownProviders = 绑定表里出现过的 ∪ identity registry 已声明的。
func (s *PostgresStore) knownProviders(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT provider_id FROM identity_external_links`)
	if err != nil {
		return nil, fmt.Errorf("list linked providers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.declaredProviders != nil {
		declared, err := s.declaredProviders(ctx)
		if err != nil {
			return nil, fmt.Errorf("list declared providers: %w", err)
		}
		for _, id := range declared {
			out[id] = true
		}
	}
	return out, nil
}

func (s *PostgresStore) rowExists(ctx context.Context, query string, arg any) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, query, arg).Scan(&exists); err != nil {
		return false, fmt.Errorf("check rule reference: %w", err)
	}
	return exists, nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// sortRuleIssues 让同一枚徽章的失效项顺序稳定（按 path），便于前端 diff 与测试断言。
func sortRuleIssues(issues []RuleIssue) {
	sort.Slice(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
}
