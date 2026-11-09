package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件是 seed 的收尾阶段：主题写完后一次性回填展示字段（浏览数、置顶），
// 再按真实行数刷新分类/标签冗余计数。
//
// 之所以放在收尾而不是逐条写入：Service 路径每次事务都会维护 hot_score 与计数，
// 逐条设置浏览数会多出上千次 UPDATE；这里按 id 数组一次完成。

// seedTopicPresentation 是要回填到主题行的展示字段。
type seedTopicPresentation struct {
	TopicID int64
	Views   int64
	Pinned  bool
}

// seedPostPass 抽象收尾写入，方便在上层命令中按 pool 是否存在决定是否启用。
type seedPostPass interface {
	ApplyTopicPresentation(ctx context.Context, topics []seedTopicPresentation) error
	RefreshCounters(ctx context.Context) error
}

// seedPostgresPostPass 是 seedPostPass 的 PostgreSQL 实现。
type seedPostgresPostPass struct {
	pool *pgxpool.Pool
}

// newSeedPostPass 在 pool 为空时返回 nil，调用方按 nil 跳过收尾阶段。
func newSeedPostPass(pool *pgxpool.Pool) seedPostPass {
	if pool == nil {
		return nil
	}
	return seedPostgresPostPass{pool: pool}
}

// ApplyTopicPresentation 按 id 批量写入 view_count / is_pinned，并同步 hot_score。
// hot_score 公式的唯一权威是 forum.TopicHotScoreSQL：comment_count*5 + view_count + like_count*3。
func (p seedPostgresPostPass) ApplyTopicPresentation(ctx context.Context, topics []seedTopicPresentation) error {
	if len(topics) == 0 {
		return nil
	}
	ids := make([]int64, len(topics))
	views := make([]int64, len(topics))
	pinned := make([]bool, len(topics))
	for i, item := range topics {
		ids[i] = item.TopicID
		views[i] = item.Views
		pinned[i] = item.Pinned
	}
	if _, err := p.pool.Exec(ctx, `
		UPDATE topics AS t
		SET view_count = u.views,
		    hot_score = (t.comment_count * 5) + u.views + (t.like_count * 3),
		    is_pinned = u.pinned,
		    updated_at = now()
		FROM unnest($1::bigint[], $2::bigint[], $3::boolean[]) AS u(id, views, pinned)
		WHERE t.id = u.id
	`, ids, views, pinned); err != nil {
		return fmt.Errorf("apply topic presentation: %w", err)
	}
	return nil
}

// RefreshCounters 用真实 COUNT 回填分类冗余计数（与 perf bulk 路径共用同一 SQL）。
func (p seedPostgresPostPass) RefreshCounters(ctx context.Context) error {
	if err := refreshCategoryCounters(ctx, p.pool); err != nil {
		return fmt.Errorf("refresh category counters: %w", err)
	}
	return nil
}
