package badges

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
)

// 本文件是 `RefCandidateStore` 的 Postgres 实现：编辑器下拉的五类候选。
//
// 它和 `ref_names_postgres.go` 是同一条先例的两个方向——那边按 slug 批量解析
// 展示名，这边按 kind 批量列出可选值。为什么也直查 Core 表而不是调 Forum /
// Identity 的 service：
//   - 那些 service 的读口是「列出全部」，形状带分页、权限与视图模型，拿它当
//     下拉候选要么翻页要么改签名，两边都会被绑死；
//   - 徽章侧本来就只读 Core 数据面（见 `ref_names_postgres.go` 文件头与
//     `conditions/registry.go`），沿用既有口径，不新造跨域包依赖。
//
// 失败语义（与 `LoadRefNames` 一致）：**单类查询失败不中断其余类**。失败的那一类
// 标成 `Unavailable`，其余照常返回；第一条错误回给调用方记 warn。调用方绝不把
// 这个错误变成 HTTP 4xx/5xx——候选取不到只该让编辑器退回文本输入。

// RefCandidates 返回五类候选（badge/category/tag/role/provider）。
//
// `error` 是**第一条**单类失败（仅供调用方 warn），返回值里已经带上了其余成功类
// 的结果与失败类的 `Unavailable` 标记，所以调用方应当先使用返回值、再决定要不要
// 记日志。`pool == nil`（单测/装配早期）时五项全 unavailable 且无错误。
func (s *PostgresStore) RefCandidates(ctx context.Context) (conditions.RefCandidateSet, error) {
	if s == nil || s.pool == nil {
		return conditions.UnavailableRefCandidateSet(), nil
	}
	groups := make([]conditions.RefCandidateGroup, 0, len(conditions.RefKinds()))
	var firstErr error
	record := func(kind string, err error) {
		if err == nil {
			return
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("load badge ref candidates (%s): %w", kind, err)
		}
		groups = append(groups, conditions.NewUnavailableRefCandidateGroup(kind))
	}

	// 每类一条 SQL，`LIMIT RefCandidatesLimit+1` 用来判断是否还有更多：多取一行比
	// 再跑一次 COUNT 便宜，也避免「恰好 500 条」与「还有第 501 条」被混为一谈。
	// ORDER BY 与 `NewRefCandidateGroup` 的排序键同源（展示名 → 值），这样被截掉的
	// 确实是排在后半段的那一批。
	limit := conditions.RefCandidatesLimit + 1

	categories, err := s.queryRefCandidates(ctx,
		`SELECT slug, name, visibility FROM categories ORDER BY name, slug, id LIMIT $1`, limit,
		func(rows pgx.Rows) (conditions.RefCandidate, error) {
			var slug, name, visibility string
			if err := rows.Scan(&slug, &name, &visibility); err != nil {
				return conditions.RefCandidate{}, err
			}
			candidate := conditions.RefCandidate{Value: slug, Label: name}
			// 隐藏分类不进公开面，但「当时发在该分类的主题」是历史事实：规则引用
			// 它仍然合法（口径见计划 §5.2.2 #5）。所以只标 hidden，不过滤。
			if visibility != "public" {
				candidate.Flags = []string{conditions.RefFlagHidden}
			}
			return candidate, nil
		})
	if err != nil {
		record(conditions.RefKindCategory, err)
	} else {
		groups = append(groups, conditions.NewRefCandidateGroup(conditions.RefKindCategory, categories))
	}

	tags, err := s.queryRefCandidates(ctx,
		`SELECT slug, name, status FROM tags ORDER BY name, slug, id LIMIT $1`, limit,
		func(rows pgx.Rows) (conditions.RefCandidate, error) {
			var slug, name, status string
			if err := rows.Scan(&slug, &name, &status); err != nil {
				return conditions.RefCandidate{}, err
			}
			candidate := conditions.RefCandidate{Value: slug, Label: name}
			// 下架/待审标签同样只是「当前不可选」，历史事实不变（§5.2.2 #5）。
			if status != "active" {
				candidate.Flags = []string{conditions.RefFlagHidden}
			}
			return candidate, nil
		})
	if err != nil {
		record(conditions.RefKindTag, err)
	} else {
		groups = append(groups, conditions.NewRefCandidateGroup(conditions.RefKindTag, tags))
	}

	roles, err := s.queryRefCandidates(ctx,
		`SELECT key, alias, is_enabled FROM roles ORDER BY alias, key, id LIMIT $1`, limit,
		func(rows pgx.Rows) (conditions.RefCandidate, error) {
			var key, alias string
			var enabled bool
			if err := rows.Scan(&key, &alias, &enabled); err != nil {
				return conditions.RefCandidate{}, err
			}
			// 展示名用 alias 而不是 key：与 `LoadRefNames` 同口径（「版主」这类运营
			// 可改的称呼），但写进规则的值仍然是 `roles.key`（§5.2.2 #6）。
			candidate := conditions.RefCandidate{Value: key, Label: alias}
			if !enabled {
				candidate.Flags = []string{conditions.RefFlagDisabled}
			}
			return candidate, nil
		})
	if err != nil {
		record(conditions.RefKindRole, err)
	} else {
		groups = append(groups, conditions.NewRefCandidateGroup(conditions.RefKindRole, roles))
	}

	badges, err := s.queryRefCandidates(ctx,
		`SELECT slug, name, enabled FROM badges ORDER BY name, slug, id LIMIT $1`, limit,
		func(rows pgx.Rows) (conditions.RefCandidate, error) {
			var slug, name string
			var enabled bool
			if err := rows.Scan(&slug, &name, &enabled); err != nil {
				return conditions.RefCandidate{}, err
			}
			candidate := conditions.RefCandidate{Value: slug, Label: name}
			if !enabled {
				candidate.Flags = []string{conditions.RefFlagDisabled}
			}
			return candidate, nil
		})
	if err != nil {
		record(conditions.RefKindBadge, err)
	} else {
		groups = append(groups, conditions.NewRefCandidateGroup(conditions.RefKindBadge, badges))
	}

	// provider 的候选不进数据库：来源是 identity registry 的已发布声明（与公开
	// 说明里的真名同源，见 `SetProviderNames`）。未注入 = 取不到候选 → unavailable，
	// 编辑器退回文本输入；注入但 registry 里一个 auth provider 都没有 → 空候选，
	// 仍然是「没有可选项」而不是「取不到」。
	if s.providerNames == nil {
		groups = append(groups, conditions.NewUnavailableRefCandidateGroup(conditions.RefKindProvider))
	} else {
		groups = append(groups, conditions.NewRefCandidateGroup(conditions.RefKindProvider,
			providerRefCandidates(s.providerNames())))
	}

	return conditions.NormalizeRefCandidateSet(groups), firstErr
}

// providerRefCandidates 把 id → 双语名映射成候选。
//
// 展示名 zh 优先、回落 en（与 `firstNonEmpty` 同口径）：徽章后台默认中文，
// 跳去英文说「Sign in with X」比显示内部 id 更糟。
func providerRefCandidates(names map[string][2]string) []conditions.RefCandidate {
	out := make([]conditions.RefCandidate, 0, len(names))
	for id, pair := range names {
		out = append(out, conditions.RefCandidate{
			Value: id,
			Label: firstNonEmpty(pair[0], pair[1], id),
		})
	}
	return out
}

// queryRefCandidates 跑一条候选查询并把每行交给 scan。
func (s *PostgresStore) queryRefCandidates(
	ctx context.Context,
	query string,
	limit int,
	scan func(rows pgx.Rows) (conditions.RefCandidate, error),
) ([]conditions.RefCandidate, error) {
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]conditions.RefCandidate, 0, limit)
	for rows.Next() {
		candidate, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
