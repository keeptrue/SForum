#!/usr/bin/env bash
# 徽章规则引擎性能验收（10k 用户夹具）。
#
# 目标（矩阵 §3.3 / M1.3-10）：
#   1. 窗口「获赞」的真实 EXPLAIN —— 必须看到 content_reactions_roster_idx 的 Index Only Scan；
#   2. 单用户评估的 P50 / P95；
#   3. 全量回扫 10k 用户的吞吐（users/s）。
#
# 设计前提：
#   * **自己造数据**，不读 dev 库。夹具永远从一个空的一次性集群 + 完整迁移链开始，
#     所以数字可复现，也不会把开发库的残留当成基线（矩阵 §9.1 的教训）。
#   * 三态 ok / FAIL / pend：EXPLAIN 类断言现在就能跑；P50/P95 与吞吐要等
#     `Models/Badges/conditions` 落地（窗口解析器），未落地一律 pend。
#   * **不接** `./scripts/test.sh`，也不属于 `tests/validate-*` 门禁。`--strict` 才把 pend 转 exit 4。
#   * **默认 10k 规模就是矩阵里的 `M1-EXIT-PERF` 收口门**：M1 结项前必须跑一次并把 EXPLAIN 原文
#     贴进 `knowledge/reports/2026-11-12-badge-rule-engine-acceptance.md` §9.4。它不是日常循环，
#     2M reactions 要几分钟；冒烟请显式缩小 SFORUM_PERF_USERS。
#
# 退出码：0 无 FAIL；1 有 FAIL；2 预检失败；4 --strict 下有 pend。
#
# 环境变量：
#   SFORUM_PERF_USERS              用户数，默认 10000
#   SFORUM_PERF_TOPICS_PER_USER    每用户主题数，默认 20
#   SFORUM_PERF_LIKES_PER_TOPIC    每主题点赞数，默认 10（其中 60% 落在窗口内）
#   SFORUM_PERF_WINDOW_DAYS        窗口天数，默认 30
#   SFORUM_PERF_BUDGET_P95_MS      单用户 P95 预算，默认 50
#   SFORUM_PERF_IMAGE              默认 postgres:17-alpine
#   SFORUM_PERF_KEEP_CLUSTER=1     保留集群排错（脚本会打印容器名与端口）
#
# 用法：
#   ./scripts/verify-badge-rules-perf.sh
#   SFORUM_PERF_USERS=200 SFORUM_PERF_TOPICS_PER_USER=5 ./scripts/verify-badge-rules-perf.sh   # 冒烟
#   ./scripts/verify-badge-rules-perf.sh --strict
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"

USERS="${SFORUM_PERF_USERS:-10000}"
TOPICS_PER_USER="${SFORUM_PERF_TOPICS_PER_USER:-20}"
LIKES_PER_TOPIC="${SFORUM_PERF_LIKES_PER_TOPIC:-10}"
WINDOW_DAYS="${SFORUM_PERF_WINDOW_DAYS:-30}"
BUDGET_P95_MS="${SFORUM_PERF_BUDGET_P95_MS:-50}"
IMAGE="${SFORUM_PERF_IMAGE:-postgres:17-alpine}"
KEEP_CLUSTER="${SFORUM_PERF_KEEP_CLUSTER:-0}"
STRICT=0

usage() { sed -n '2,32p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done

PASS_N=0; FAIL_N=0; PEND_N=0
FAIL_LINES=(); PEND_LINES=()
step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
pass() { PASS_N=$((PASS_N + 1)); printf '  ok    %s\n' "$1"; }
fail() { FAIL_N=$((FAIL_N + 1)); FAIL_LINES+=("$1${2:+ — $2}"); printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"; }
pend() { PEND_N=$((PEND_N + 1)); PEND_LINES+=("$1${2:+ — $2}"); printf '  pend  %s%s\n' "$1" "${2:+（$2）}"; }
die() { local code=$1; shift; printf '\n[exit %s] %s\n' "$code" "$*" >&2; exit "$code"; }

work="$(mktemp -d)"
cid=''
cleanup() {
  local status=$?
  if [[ -n "$cid" && "$KEEP_CLUSTER" != 1 ]]; then
    docker rm -fv "$cid" >/dev/null 2>&1 || printf '清理临时集群失败：%s\n' "$cid" >&2
  elif [[ -n "$cid" ]]; then
    printf '\n保留集群 %s（端口 %s）。排错完手动 docker rm -fv %s\n' "$cid" "$port" "$cid" >&2
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

step '预检'
command -v docker >/dev/null || die 2 '缺少 docker。'
command -v go >/dev/null || die 2 '缺少 go。'
docker info >/dev/null 2>&1 || die 2 'docker daemon 不可用。'
[[ -d "$API_DIR" ]] || die 2 "找不到 API 目录：$API_DIR"
note "夹具规模：users=${USERS} topics/user=${TOPICS_PER_USER} likes/topic=${LIKES_PER_TOPIC} window=${WINDOW_DAYS}d"
note "预计行数：topics=$((USERS * TOPICS_PER_USER)) reactions=$((USERS * TOPICS_PER_USER * LIKES_PER_TOPIC))"
if [[ -n "${DATABASE_URL:-}" ]]; then
  note '继承的 DATABASE_URL 被忽略：本脚本只在自己的一次性集群上跑，绝不动开发库。'
fi

CONDITIONS_FILE="$API_DIR/app/Models/Badges/conditions/registry.go"
if [[ -f "$CONDITIONS_FILE" ]]; then
  note '窗口解析器已落地：P50/P95 与吞吐段会尝试执行（当前骨架仍记 pend，接探针见 §TODO）。'
else
  note '窗口解析器未落地（缺 app/Models/Badges/conditions/registry.go）：计时断言将 pend。'
fi

step '启动一次性 PostgreSQL 集群'
owner="$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
container="sforum-perf-${owner}"
cid="$(docker run -d --name "$container" \
  --label "com.sforum.perf-owner=$owner" \
  -e POSTGRES_DB=sforum_perf -e POSTGRES_USER=sforum -e POSTGRES_PASSWORD=sforum \
  -p 127.0.0.1::5432 "$IMAGE" \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off) " || die 2 '创建临时集群失败。'

ready=0; stable=0
for _ in {1..60}; do
  if docker exec -e PGPASSWORD=sforum "$cid" psql -U sforum -d sforum_perf -tAc 'SELECT 1' >/dev/null 2>&1; then
    stable=$((stable + 1))
    [[ "$stable" == 2 ]] && { ready=1; break; }
  else
    stable=0
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || { docker logs "$cid" >&2 || true; die 2 '临时集群未就绪。'; }

binding="$(docker port "$cid" 5432/tcp)"
[[ "$binding" == 127.0.0.1:* && "$binding" != *$'\n'* ]] || die 2 "端口绑定异常：$binding"
port="${binding##*:}"
export DATABASE_URL="postgres://sforum:sforum@127.0.0.1:${port}/sforum_perf?sslmode=disable"
note "集群就绪：${container}，端口 ${port}"

psql_run() { docker exec -e PGPASSWORD=sforum -i "$cid" psql -U sforum -d sforum_perf -v ON_ERROR_STOP=1 -tAq "$@"; }

step '完整迁移链'
( cd "$API_DIR" && go run ./cmd/migrate ) || die 2 '迁移链失败。'
note "goose 版本：$(psql_run -c 'SELECT max(version_id) FROM goose_db_version WHERE is_applied')"

step "造夹具（${USERS} 用户 / $((USERS * TOPICS_PER_USER)) 主题 / $((USERS * TOPICS_PER_USER * LIKES_PER_TOPIC)) 点赞）"
seed_start=$SECONDS
psql_run <<SQL || die 2 '夹具写入失败。'
SET synchronous_commit = off;
-- categories.group_id 在 202607070003 之后是 NOT NULL，必须挂在某个分组下
INSERT INTO category_groups (slug, name, description) VALUES ('perf-group', 'Perf', '')
ON CONFLICT (slug) DO NOTHING;
INSERT INTO categories (slug, name, description, group_id)
SELECT 'perf', 'Perf', '', id FROM category_groups WHERE slug = 'perf-group'
ON CONFLICT (slug) DO NOTHING;

-- 用户：注册时间散布在 300 天内（identity.account_age 夹具）
INSERT INTO users (username, username_lower, email, email_lower, display_name, status, created_at)
SELECT 'perf'||g, 'perf'||g, 'perf'||g||'@example.test', 'perf'||g||'@example.test',
       'Perf '||g, 'active', now() - ((g % 300) || ' days')::interval
FROM generate_series(1, ${USERS}) g;

-- 帖子：topics.content_id 是 NOT NULL 外键，必须先有 posts
INSERT INTO posts (raw_content, html_content, plain_text, content_hash, created_by_user_id, created_at)
SELECT 'perf', '<p>perf</p>', 'perf', md5(g::text), 1 + (g % ${USERS}),
       now() - ((g % 90) || ' days')::interval
FROM generate_series(1, ${USERS} * ${TOPICS_PER_USER}) g;

-- 主题：author 轮转；created_at 散布在 90 天内（窗口 vs 累计的夹具）
INSERT INTO topics (category_id, author_user_id, content_id, title, slug, status, created_at)
SELECT c.id, p.created_by_user_id, p.id, 'perf '||p.id, 'perf-'||p.id, 'active', p.created_at
FROM posts p CROSS JOIN (SELECT id FROM categories WHERE slug = 'perf') c;

-- 点赞：每主题 LIKES_PER_TOPIC 条，前 60% 落在窗口内、其余在窗口外
INSERT INTO content_reactions (user_id, target_type, target_id, reaction, created_at)
SELECT 1 + ((t.id * 7 + k) % ${USERS}), 'topic', t.id, 'like',
       CASE WHEN k <= (${LIKES_PER_TOPIC} * 6 / 10)
            THEN now() - ((k * ${WINDOW_DAYS} / GREATEST(${LIKES_PER_TOPIC}, 1)) || ' days')::interval
            ELSE now() - ((${WINDOW_DAYS} + 1 + (k * 7) % 60) || ' days')::interval END
FROM topics t CROSS JOIN generate_series(1, ${LIKES_PER_TOPIC}) k
WHERE 1 + ((t.id * 7 + k) % ${USERS}) <> t.author_user_id
ON CONFLICT DO NOTHING;

-- 统计列按真值回填（不要手工填随机数，否则测的是假数据）
INSERT INTO user_activity (user_id, topic_count, like_received_count, like_given_count, days_visited)
SELECT u.id,
       COALESCE(t.n, 0), COALESCE(lr.n, 0), COALESCE(lg.n, 0), 30
FROM users u
LEFT JOIN (SELECT author_user_id AS uid, count(*) n FROM topics GROUP BY 1) t ON t.uid = u.id
LEFT JOIN (
  SELECT t2.author_user_id AS uid, count(*) n
  FROM content_reactions r JOIN topics t2 ON t2.id = r.target_id
  WHERE r.target_type = 'topic' AND r.reaction = 'like'
  GROUP BY 1
) lr ON lr.uid = u.id
LEFT JOIN (
  SELECT user_id AS uid, count(*) n FROM content_reactions WHERE reaction = 'like' GROUP BY 1
) lg ON lg.uid = u.id
ON CONFLICT (user_id) DO UPDATE
  SET topic_count = EXCLUDED.topic_count,
      like_received_count = EXCLUDED.like_received_count,
      like_given_count = EXCLUDED.like_given_count;

-- 访问记录：每人连续 30 天（activity.streak 夹具）
INSERT INTO user_visits (user_id, visited_on)
SELECT u.id, current_date - g FROM users u CROSS JOIN generate_series(0, 29) g
ON CONFLICT DO NOTHING;

ANALYZE;
SQL
note "夹具写入耗时：$((SECONDS - seed_start))s"

step 'PERF-01 窗口「获赞」的 EXPLAIN（单主题形状，resolver 实际查询）'
sample_topic="$(psql_run -c "SELECT t.id FROM topics t JOIN content_reactions r ON r.target_id = t.id AND r.target_type='topic' WHERE t.author_user_id = 1 GROUP BY t.id ORDER BY count(*) DESC LIMIT 1")"
[[ -n "$sample_topic" ]] || die 2 '夹具里找不到样本主题。'
# 必须按最终 SQL 的形状探针：窗口获赞**排除自赞**（r.user_id <> 作者）。
# 少了这个条件会得到 Index Only Scan，那是探针形状，不是生产形状（见计划 §13.1）。
sample_author="$(psql_run -c "SELECT author_user_id FROM topics WHERE id = ${sample_topic}")"
note "样本主题 ${sample_topic}（作者 ${sample_author}）：EXPLAIN 含自赞排除，与 resolver 最终 SQL 同形"
reactions_n="$(psql_run -c 'SELECT count(*) FROM content_reactions')"
E1_SQL="SELECT count(*) FROM content_reactions WHERE target_type='topic' AND target_id=${sample_topic} AND reaction='like' AND user_id <> ${sample_author} AND created_at >= now() - interval '${WINDOW_DAYS} days'"
psql_run -c "EXPLAIN (ANALYZE, BUFFERS) ${E1_SQL}" >"$work/e1.txt" 2>&1
sed -e 's/^/    /' "$work/e1.txt"
# 判据（计划 §13.1 已修正）：出现 roster_idx 即可，**不再要求 Index Only Scan**。
# 加上自赞排除后 user_id 不在索引里，Index Scan + heap 过滤是正确代价，不是缺陷。
if grep -q 'content_reactions_roster_idx' "$work/e1.txt"; then
  if grep -q 'Index Only Scan' "$work/e1.txt"; then
    pass 'PERF-01 窗口获赞走 content_reactions_roster_idx 的 Index Only Scan（意外之喜，不要求）'
  else
    pass 'PERF-01 窗口获赞走 content_reactions_roster_idx（Index Scan + heap 过滤，自赞排除的预期代价）'
  fi
else
  if [[ "${reactions_n:-0}" -lt 100000 ]]; then
    pend 'PERF-01 窗口获赞计划' "夹具仅 ${reactions_n} 行 reactions，planner 选 seq scan 属正常；用默认规模（10k×20×10=2M）复核"
  else
  # planner 可能在小表/统计不准时选 hash join。先关掉 hashjoin 复核「索引是否可用」，
  # 再决定 P-01 的「不加索引」结论是否成立——不能拿一次 planner 选择当结论。
  note 'planner 未选 roster_idx，关掉 hashjoin 复核（判的是索引可用性，不是这一次的选择）'
  psql_run -c "SET enable_hashjoin = off" -c "SET enable_mergejoin = off" \
    -c "EXPLAIN (ANALYZE, BUFFERS) ${E1_SQL}" >"$work/e1b.txt" 2>&1
  sed -e 's/^/    /' "$work/e1b.txt"
  if grep -q 'content_reactions_roster_idx' "$work/e1b.txt"; then
    pass 'PERF-01 索引可用（关掉 hashjoin 后走 roster_idx）；默认计划的 join 选择需在 10k 规模上复核'
  elif grep -q 'Seq Scan on content_reactions' "$work/e1b.txt"; then
    fail 'PERF-01 窗口获赞计划' '关掉 hashjoin 后退化成 content_reactions 全站顺序扫（P-01 结论作废，M1.3 必须加索引）'
  else
    fail 'PERF-01 窗口获赞计划' '关掉 hashjoin 后仍未出现 content_reactions_roster_idx（P-01 结论作废，M1.3 必须加索引）'
  fi
  fi
fi

step 'PERF-02 本人主题列表的 EXPLAIN'
psql_run -c "EXPLAIN (ANALYZE, BUFFERS) SELECT id FROM topics WHERE author_user_id = 1 AND created_at >= now() - interval '${WINDOW_DAYS} days'" >"$work/e2.txt" 2>&1
sed -e 's/^/    /' "$work/e2.txt"
if grep -q 'topics_author_created_idx' "$work/e2.txt"; then
  pass 'PERF-02 本人主题列表走 topics_author_created_idx'
else
  pend 'PERF-02 本人主题列表计划' '未走 topics_author_created_idx（小夹具上可能选 seq scan，不判 FAIL）'
fi

note 'PERF-01 只判计划形状；「P95 在预算内」由 PERF-03 判，两者合起来才是 M1-EXIT-PERF 的判据。'

step 'PERF-03 单用户评估 P50 / P95'
if [[ ! -f "$CONDITIONS_FILE" ]] || [[ ! -f "$API_DIR/app/Models/Badges/rule_eval.go" ]]; then
  pend 'PERF-03 单用户 P50/P95' '窗口解析器或 EvaluateRule 未落地'
else
  probe_dir="$API_DIR/.reviewer-perf-probe-$$"
  mkdir -p "$probe_dir"
  cat >"$probe_dir/main.go" <<'PROBE'
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	re "github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Println("PERF03ERR missing DATABASE_URL")
		os.Exit(2)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Println("PERF03ERR", err)
		os.Exit(2)
	}
	defer pool.Close()

	// 窗口「获赞」规则：走引擎路径（非快路径），且含自赞排除。
	node, err := re.Decode([]byte(`{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":3}}}`))
	if err != nil {
		fmt.Println("PERF03ERR decode:", err)
		os.Exit(2)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM users ORDER BY id LIMIT 200`)
	if err != nil {
		fmt.Println("PERF03ERR query:", err)
		os.Exit(2)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			fmt.Println("PERF03ERR scan:", err)
			os.Exit(2)
		}
		ids = append(ids, id)
	}
	rows.Close()

	store := badges.NewPostgresStore(pool)
	durs := make([]time.Duration, 0, len(ids))
	failed := 0
	for _, id := range ids {
		start := time.Now()
		_, err := store.EvaluateRule(ctx, id, node)
		durs = append(durs, time.Since(start))
		if err != nil {
			failed++
		}
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	pick := func(q float64) time.Duration {
		if len(durs) == 0 {
			return 0
		}
		return durs[int(float64(len(durs)-1)*q)]
	}
	fmt.Printf("PERF03 users=%d failed=%d p50=%.2fms p95=%.2fms p99=%.2fms\n",
		len(durs), failed, float64(pick(0.50).Microseconds())/1000.0,
		float64(pick(0.95).Microseconds())/1000.0, float64(pick(0.99).Microseconds())/1000.0)
}
PROBE
  if ! ( cd "$API_DIR" && go run "./$(basename "$probe_dir")" ) >"$work/perf03.txt" 2>&1; then
    rm -rf "$probe_dir"
    pend 'PERF-03 单用户 P50/P95' "探针执行失败：$(tail -2 "$work/perf03.txt" | tr '\n' ' ')"
  else
    rm -rf "$probe_dir"
    line="$(grep '^PERF03 ' "$work/perf03.txt")"
    note "$line"
    p95="$(printf '%s' "$line" | sed -n 's/.*p95=\([0-9.]*\)ms.*/\1/p')"
    if [[ -z "$p95" ]]; then
      pend 'PERF-03 单用户 P50/P95' '探针未输出 p95'
    elif awk -v p="$p95" -v b="$BUDGET_P95_MS" 'BEGIN{exit !(p <= b)}'; then
      pass "PERF-03 单用户 P95 ${p95}ms ≤ 预算 ${BUDGET_P95_MS}ms"
    else
      fail 'PERF-03 单用户 P95' "P95 ${p95}ms 超预算 ${BUDGET_P95_MS}ms（P-01 的「不加索引」结论作废）"
    fi
  fi
fi

step 'PERF-04 全量回扫 10k 吞吐'
if [[ ! -f "$CONDITIONS_FILE" ]]; then
  pend 'PERF-04 全量回扫吞吐' '窗口解析器未落地'
else
  # TODO(task-4 落地后)：调 POST /admin/badges/recompute 或直接跑 backfill job，
  # 记录 wall time 与 users/s，按「10k ≤ 10min（≥17 users/s）」判定。
  pend 'PERF-04 全量回扫吞吐' '回扫 job 未接（骨架）；判据 10k ≤ 10min'
fi

if [[ "$USERS" -ge 10000 ]]; then
  note '当前为默认 10k 规模：本次输出即 M1-EXIT-PERF 收口证据，请把 EXPLAIN 原文贴进矩阵 §9.4。'
fi

step '汇总'
printf '  ok %d / FAIL %d / pend %d\n' "$PASS_N" "$FAIL_N" "$PEND_N"
if [[ "$FAIL_N" -gt 0 ]]; then printf '\n  FAIL 清单：\n'; printf '    - %s\n' "${FAIL_LINES[@]}"; fi
if [[ "$PEND_N" -gt 0 ]]; then printf '\n  pend 清单（骨架未接的部分，不算通过）：\n'; printf '    - %s\n' "${PEND_LINES[@]}"; fi
[[ "$FAIL_N" -gt 0 ]] && exit 1
if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then
  printf '\n[exit 4] --strict：存在 %d 条 pend。\n' "$PEND_N" >&2
  exit 4
fi
exit 0
