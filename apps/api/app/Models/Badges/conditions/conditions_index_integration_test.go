package conditions_test

import (
	"strings"
	"testing"
)

// TestWindowedLikeUsesRosterIndex 是 P-01 的证据：10k 夹具上证明现有
// `content_reactions_roster_idx` 能支撑窗口「获赞」的查询形状，因此**不需要**新增
// `(target_type, target_id, created_at)` 索引——那个定义缺 `reaction` 等值列，
// 对本查询反而更差。
//
// 注意计划形态：`r.user_id <> $1`（自赞排除）用到了索引外的列，所以这里是
// **Index Scan + heap 过滤**，不是小研那份证据里的 Index Only Scan——那是没带
// 自赞排除的探针形状。要回到 Index Only Scan 得把 `user_id` 加进索引，而 P-01 明确
// 不加索引；10k 夹具上实测执行 2ms 量级，不值得为一次徽章判定改生产索引。
//
// 用 `enable_seqscan=off` 是为了证明**索引可用性**（小表上 planner 本来就会选
// seq scan，那不能推出「索引没用」）；默认设置下的实际计划一并打到日志里。
func TestWindowedLikeUsesRosterIndex(t *testing.T) {
	f := newFixture(t)
	authorID := f.user(t, "prolific", 400)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id)
	          SELECT g, (SELECT id FROM categories WHERE slug='general'), $1 FROM generate_series(1,100) AS g`, authorID)
	f.exec(t, `INSERT INTO content_reactions (user_id, target_type, target_id, reaction, created_at)
	          SELECT 1000 + (g % 500), 'topic', 1 + (g % 100), 'like', now() - make_interval(days => g % 400)
	          FROM generate_series(1, 10000) AS g`)

	plan := f.explain(t, false, windowedLikeProbeSQL, authorID)
	if !strings.Contains(plan, "content_reactions_roster_idx") {
		t.Fatalf("默认设置下的计划未用到 roster 索引：\n%s", plan)
	}
	t.Logf("默认设置计划：\n%s", plan)

	forced := f.explain(t, true, windowedLikeProbeSQL, authorID)
	if !strings.Contains(forced, "content_reactions_roster_idx") {
		t.Fatalf("关掉 seqscan 后仍未走 roster 索引：\n%s", forced)
	}
	t.Logf("enable_seqscan=off 计划：\n%s", forced)
}

// windowedLikeProbeSQL 是窗口「获赞」的真实查询形状（与 resolve_activity 一致）。
const windowedLikeProbeSQL = `
SELECT count(*) FROM content_reactions r
  JOIN topics t ON t.id = r.target_id
 WHERE r.target_type = 'topic' AND r.reaction = 'like'
   AND r.user_id <> $1
   AND t.author_user_id = $1
   AND r.created_at >= now() - make_interval(days => 30)
`

func (f *fixture) explain(t *testing.T, disableSeqScan bool, query string, args ...any) string {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if disableSeqScan {
		if _, err := tx.Exec(f.ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(f.ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var builder strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}
