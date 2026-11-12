package badges

import (
	"context"
	"fmt"
	"sort"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
)

// 本文件是 `RefNameLoader` 的 Postgres 实现：**每一类引用一次查询**。
//
// 它挂在 `PostgresStore` 上而不是装配层（`bootstrap`），是刻意的：
//   - 名字解析需要按 slug 批量读 Core 表；`conditions` 包本来就是同一条先例
//     （直接查 Core 数据面，见 registry.go 文件头与 resolve_activity.go）；
//   - 装配层拿到的 Forum / Identity store 只提供「列出全部」，用它按 slug 过滤
//     是 N+1 的另一种写法。
//
// 为什么直接写 SQL 而不是调 Forum / Identity 的 store：
//   - 那些 store 只提供「列出全部」的读（`ListCategories` / `ListTags` /
//     `ListRoles`），拿它按 slug 过滤是 N+1 的另一种写法；
//   - `conditions` 包本来就按同一口径直接查 Core 数据面（见 registry.go 文件头
//     与 resolve_activity.go），徽章侧沿用这条既有先例，不新造跨域包依赖。
//
// 失败语义：单类查询失败**不**中断其余类（`firstErr` 记录第一条），返回已解析到
// 的名字 + 错误；调用方 `ResolveRefNames` 会 warn 并用已拿到的名字渲染，
// 于是「categories 表挂了」只会让分类名回落泛称，公开页照常出文案。

// providerNameFunc 返回第三方登录方式的真名（id → 双语名）。
//
// 未注入时该类别按泛称渲染：provider 展示名来自 identity 扩展 registry，
// 而当前装配刻意没有把 registry 注入徽章 store（见 bootstrap 注释）。留这个缝
// 是为了让注入成为一次装配改动，而不是再改一遍 `RefNames` / `Phrase`。
type providerNameFunc func() map[string][2]string

// SetProviderNames 注入第三方登录方式真名来源（未注入 = 泛称回落）。
func (s *PostgresStore) SetProviderNames(source providerNameFunc) {
	if s == nil {
		return
	}
	s.providerNames = source
}

// LoadRefNames 批量解析一批引用；返回 partial + 第一条错误。
func (s *PostgresStore) LoadRefNames(ctx context.Context, refs RefSet) (conditions.RefNames, error) {
	names := conditions.NewRefNames()
	if s == nil || refs.Empty() {
		return names, nil
	}
	// 先接「不查库」的两类（登录方式真名来自 registry 快照、事实名来自扩展候选）：
	// 这样单测可以拿一个没有 pool 的 store 验证注入是否生效。
	if s.providerNames != nil {
		byID := s.providerNames()
		for id := range refs.Providers {
			if pair, ok := byID[id]; ok {
				names.AddProvider(id, firstNonEmpty(pair[0], pair[1]))
			}
		}
	}
	if s.pool == nil {
		return names, nil
	}
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("load badge ref names: %w", err)
		}
	}

	if slugs := sortedKeys(refs.Categories); len(slugs) > 0 {
		record(s.queryRefNames(ctx, `SELECT slug, name FROM categories WHERE slug = ANY($1)`, slugs, names.AddCategory))
	}
	if slugs := sortedKeys(refs.Tags); len(slugs) > 0 {
		record(s.queryRefNames(ctx, `SELECT slug, name FROM tags WHERE slug = ANY($1)`, slugs, names.AddTag))
	}
	if keys := sortedKeys(refs.Roles); len(keys) > 0 {
		// roles 没有 name 列，展示名是 alias（「版主」这类运营可改的称呼）。
		record(s.queryRefNames(ctx, `SELECT key, alias FROM roles WHERE key = ANY($1)`, keys, names.AddRole))
	}
	if slugs := sortedKeys(refs.Badges); len(slugs) > 0 {
		record(s.queryRefNames(ctx, `SELECT slug, name FROM badges WHERE slug = ANY($1)`, slugs, names.AddBadge))
	}
	if ids := sortedIDs(refs.Topics); len(ids) > 0 {
		record(s.queryRefNames(ctx, `SELECT id, title FROM topics WHERE id = ANY($1)`, ids,
			func(key string, name string) {
				var topicID int64
				if _, err := fmt.Sscanf(key, "%d", &topicID); err == nil {
					names.AddTopic(topicID, name)
				}
			}))
	}
	// 插件事实名来自扩展清单（候选目录），不进数据库：与条件编辑器同一份快照，
	// 于是「编辑器里的名字」与「公开说明里的名字」不会分叉。
	if s.factCandidates != nil && len(refs.Facts) > 0 {
		for _, candidate := range s.factCandidates() {
			for ref := range refs.Facts {
				if ref.Key != candidate.Key {
					continue
				}
				names.AddFact(candidate.Key, candidate.LabelZh, candidate.LabelEn)
				if ref.Variant == "" {
					continue
				}
				for _, variant := range candidate.Variants {
					if variant.Value == ref.Variant {
						names.AddFactVariant(candidate.Key, ref.Variant, firstNonEmpty(variant.LabelZh, variant.LabelEn))
					}
				}
			}
		}
	}
	return names, firstErr
}

// queryRefNames 跑一条批量查询并把每行交给 apply。
func (s *PostgresStore) queryRefNames(ctx context.Context, query string, keys any, apply func(key, name string)) error {
	rows, err := s.pool.Query(ctx, query, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, name string
		if err := rows.Scan(&key, &name); err != nil {
			return err
		}
		apply(key, name)
	}
	return rows.Err()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// sortedKeys 把去重集变成稳定顺序的切片。
//
// 顺序对 SQL 无意义，但稳定的参数顺序让慢查询日志与测试快照可复现。
func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedIDs(set map[int64]struct{}) []int64 {
	if len(set) == 0 {
		return nil
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
