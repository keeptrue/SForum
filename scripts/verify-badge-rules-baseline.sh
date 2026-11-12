#!/usr/bin/env bash
# M1.1-11 / M1.1-12 迁移前基线重放（一次性 PostgreSQL 集群）。
#
# 为什么必须有这个脚本：dev 库已经被迁移过，`top_badge_id` 与 `user_activity` 的
# **迁移前快照**补不回来。要证明「迁移不改排序、不动统计」，只能在一次性集群上
# 回到 202611120001，造边界夹具、拍快照，再迁移、再比对。
#
# 判据（acceptance 矩阵 M1.1-09/10/11/12 + lead 的完成条件）：
#   * 迁移前：should_have_but_missing 非空（否则夹具是空断言，等于没验）
#   * 迁移后：recompute 返回 {granted:0, refreshed:0}
#   * 迁移后：should_have_but_missing = 0 行、has_but_should_not = 0 行
#   * 迁移后：top_badge_id / user_activity 全表与快照逐行一致（差集 0 行）
#   * M1.1-04：迁移前造的非内置自动徽章也被转换
#
# 夹具：每枚内置自动徽章 × {threshold-1, threshold, threshold+1} 各一个用户
# （days_visited 的 threshold=7 时 threshold-1 仍为正，负值一律夹到 0）；
# 另加一个超过阈值的 disabled 用户（授予逻辑不看状态，公开视图才看）。
#
# recompute 的调用路径：脚本内程序直接调 `Badges.PostgresStore` 的
# `GrantFromRules` + `RecomputeAll`——与 `AdminService.Recompute` 内部调用的
# 是同一对方法、同一顺序，只跳过 actor 权限校验（本脚本没有会话）。
#
# 退出码：0 通过；2 预检失败；4 迁移前夹具是空断言；5 迁移前基线失败；
#         6 迁移后 recompute 非零；7 迁移后仍有差异；8 迁移链操作失败。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
PRE_MIGRATION=202611120001
TARGET_MIGRATION=202611120002
IMAGE="${SFORUM_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
PG_USER=sforum
PG_PASSWORD=sforum
PG_DB=sforum_baseline

owner="$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
container="sforum-badge-baseline-${owner}"
cid=""
work_dir=""

step() { printf '\n== %s\n' "$*"; }
ok() { printf '  ok   %s\n' "$*"; }
bad() { printf '  FAIL %s\n' "$*" >&2; }
die() {
  local code="$1"; shift
  printf '\n[exit %s] %s\n' "$code" "$*" >&2
  exit "$code"
}

cleanup() {
  local status=$?
  trap - EXIT
  [[ -n "$work_dir" && -d "$work_dir" ]] && rm -rf "$work_dir"
  if [[ -n "$cid" && "${SFORUM_VERIFY_KEEP_CLUSTER:-0}" != 1 ]]; then
    docker rm -fv "$cid" >/dev/null 2>&1 || true
  elif [[ -n "$cid" ]]; then
    printf '\n保留临时集群 %s\n' "$cid" >&2
  fi
  exit "$status"
}
trap cleanup EXIT

psql_value() {
  docker exec -e PGPASSWORD="$PG_PASSWORD" "$cid" \
    psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}
psql_exec() {
  # -i 必须显式给：docker exec 默认不接 stdin，heredoc 会被丢掉。
  docker exec -i -e PGPASSWORD="$PG_PASSWORD" "$cid" \
    psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -q -f - >/dev/null
}

assert_value() {
  local name="$1" sql="$2" expected="$3" actual
  if ! actual="$(psql_value "$sql" 2>&1)"; then
    bad "$name（查询失败：$(printf '%s' "$actual" | tr '\n' ' ')）"
    return 1
  fi
  actual="$(printf '%s' "$actual" | tr -d '[:space:]')"
  if [[ "$actual" != "$expected" ]]; then
    bad "$name（期望 $expected，实际 ${actual:-<空>}）"
    return 1
  fi
  ok "$name"
}

step "预检"
command -v docker >/dev/null || die 2 '缺少 docker。'
command -v go >/dev/null || die 2 '缺少 go。'
docker info >/dev/null 2>&1 || die 2 'docker daemon 不可用。'

step "启动一次性 PostgreSQL 集群（$IMAGE）"
cid="$(docker run -d --name "$container" \
  -e POSTGRES_DB="$PG_DB" -e POSTGRES_USER="$PG_USER" -e POSTGRES_PASSWORD="$PG_PASSWORD" \
  -p 127.0.0.1::5432 "$IMAGE" \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off)" || die 2 '创建临时集群失败。'
ready=0; stable=0
for _ in {1..60}; do
  if docker exec -e PGPASSWORD="$PG_PASSWORD" "$cid" psql -U "$PG_USER" -d "$PG_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
    stable=$((stable + 1))
    [[ "$stable" == 2 ]] && { ready=1; break; }
  else
    stable=0
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || die 2 '临时集群未能就绪。'
port="$(docker port "$cid" 5432/tcp | sed 's/.*://')"
export DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${port}/${PG_DB}?sslmode=disable"
ok "集群就绪：端口 $port"

step "生成迁移/recompute 助手"
work_dir="$(mktemp -d)"
cat >"$work_dir/main.go" <<'GO'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: helper upto <version> | recompute")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fail("DATABASE_URL is required")
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "upto":
		if len(os.Args) != 3 {
			fail("upto needs a version")
		}
		target, err := strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil {
			fail("parse version: %v", err)
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			fail("open database: %v", err)
		}
		defer db.Close()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files(),
			goose.WithDisableGlobalRegistry(true))
		if err != nil {
			fail("create goose provider: %v", err)
		}
		results, err := provider.UpTo(ctx, target)
		if err != nil {
			fail("up to %d: %v", target, err)
		}
		fmt.Printf("applied %d migration(s), db now at %d\n", len(results), target)
	case "recompute":
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			fail("open pool: %v", err)
		}
		defer pool.Close()
		store := badges.NewPostgresStore(pool)
		granted, _, err := store.GrantFromRules(ctx) // queuedIDs 只用于回执；基线脚本只关心授予数
		if err != nil {
			fail("grant from rules: %v", err)
		}
		refreshed, err := store.RecomputeAll(ctx)
		if err != nil {
			fail("recompute all: %v", err)
		}
		fmt.Printf("granted=%d refreshed=%d\n", granted, refreshed)
	default:
		fail("unknown mode: %s", os.Args[1])
	}
}
GO
helper() { (cd "$API_DIR" && go run "$work_dir/main.go" "$@"); }

step "迁移到迁移前版本 $PRE_MIGRATION"
helper upto "$PRE_MIGRATION" >"$work_dir/up-pre.log" 2>&1 || { tail -20 "$work_dir/up-pre.log" >&2; die 8 '迁移到迁移前版本失败。'; }
ok "已到 $PRE_MIGRATION"

step "造边界夹具（迁移前）"
psql_exec <<'SQL'
-- M1.1-04：非内置的自动徽章也必须被迁移转换（写死 12 个 slug 就 FAIL）。
INSERT INTO badges (slug, name, description, tier, grant_mode, metric, threshold, position)
VALUES ('baseline-custom', 'Baseline Custom', '非内置自动徽章', 'bronze', 'automatic', 'comment_count', 3, 99)
ON CONFLICT (slug) DO NOTHING;

-- 每枚自动徽章 × {threshold-1, threshold, threshold+1} 各一个 active 用户。
DO $$
DECLARE b RECORD; v BIGINT; uid BIGINT; seq INT := 0;
BEGIN
  FOR b IN SELECT id, metric, threshold FROM badges
            WHERE grant_mode = 'automatic' AND enabled AND threshold > 0 ORDER BY id LOOP
    FOREACH v IN ARRAY ARRAY[b.threshold - 1, b.threshold, b.threshold + 1] LOOP
      seq := seq + 1;
      INSERT INTO users (username, username_lower, email, email_lower, display_name)
      VALUES (format('base%03s', seq), format('base%03s', seq),
              format('base%03s@example.test', seq), format('base%03s@example.test', seq),
              format('Base %s', seq))
      RETURNING id INTO uid;
      INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
                                 like_given_count, bookmark_count, days_visited)
      VALUES (uid,
        CASE b.metric WHEN 'topic_count'         THEN GREATEST(v, 0) ELSE 0 END,
        CASE b.metric WHEN 'comment_count'       THEN GREATEST(v, 0) ELSE 0 END,
        CASE b.metric WHEN 'like_received_count' THEN GREATEST(v, 0) ELSE 0 END,
        CASE b.metric WHEN 'like_given_count'    THEN GREATEST(v, 0) ELSE 0 END,
        CASE b.metric WHEN 'bookmark_count'      THEN GREATEST(v, 0) ELSE 0 END,
        CASE b.metric WHEN 'days_visited'        THEN GREATEST(v, 0) ELSE 0 END);
    END LOOP;
  END LOOP;

  -- 一个超过全部阈值的 disabled 用户：授予不看状态，公开视图才看。
  INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
  VALUES ('basedis', 'basedis', 'basedis@example.test', 'basedis@example.test', 'Base Disabled', 'disabled')
  RETURNING id INTO uid;
  INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
                             like_given_count, bookmark_count, days_visited)
  VALUES (uid, 1000, 1000, 1000, 1000, 1000, 1000);
END $$;
SQL
ok "夹具已造：$(psql_value "SELECT count(*) FROM users") 个用户"

step "迁移前基线：旧逻辑的 should_have_but_missing 必须非空"
# 旧语义的**唯一**来源：这一段 legacy 谓词。下面的「缺口计数」与「补授予」都用它，
# 保证两者不可能不一致（不引入第二份手写旧实现）。
legacy_gap_predicate="
 FROM badges b
 JOIN user_activity ua
   ON b.enabled AND b.grant_mode = 'automatic'
  AND b.metric IS NOT NULL AND b.threshold IS NOT NULL AND b.threshold > 0
  AND CASE b.metric
        WHEN 'topic_count' THEN ua.topic_count
        WHEN 'comment_count' THEN ua.comment_count
        WHEN 'like_received_count' THEN ua.like_received_count
        WHEN 'like_given_count' THEN ua.like_given_count
        WHEN 'bookmark_count' THEN ua.bookmark_count
        WHEN 'days_visited' THEN ua.days_visited::bigint
      END >= b.threshold
 WHERE NOT EXISTS (SELECT 1 FROM user_badges ub WHERE ub.user_id = ua.user_id AND ub.badge_id = b.id)"
missing_sql_old="SELECT count(*)${legacy_gap_predicate}"
missing_before="$(psql_value "$missing_sql_old")"
if [[ "${missing_before:-0}" -le 0 ]]; then
  die 4 "迁移前的 should_have_but_missing = ${missing_before:-<空>}，夹具是空断言（等于没验）。"
fi
ok "迁移前缺授予 $missing_before 条（夹具有效）"

step "迁移前跑旧逻辑 recompute（补授予 + 刷 top_badge）"
# 这一步**不调 store.GrantFromRules**：它自 202611120002 起是规则驱动的，读 badges.rule，
# 而该列在迁移前不存在（SQLSTATE 42703）。基线必须由旧语义产生，所以直接复用上面的
# legacy 谓词做 INSERT，并按 topBadgeOrderSQL 的排序刷新 top_badge_id。
pre_granted="$(psql_value "WITH ins AS (
  INSERT INTO user_badges (user_id, badge_id)
  SELECT ua.user_id, b.id${legacy_gap_predicate}
  ON CONFLICT (user_id, badge_id) DO NOTHING
  RETURNING 1
) SELECT count(*) FROM ins")" || die 5 '迁移前补授予失败。'
# 排序定义与 postgres_store.go 的 topBadgeOrderSQL 逐字一致：
# t.position ASC, badges.position ASC, user_badges.granted_at DESC；用 JOIN 不用 LEFT JOIN。
pre_refreshed="$(psql_value "
WITH winners AS (
  SELECT DISTINCT ON (ub.user_id) ub.user_id, ub.badge_id
  FROM user_badges ub
  JOIN badges ON badges.id = ub.badge_id AND badges.enabled
  JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
  ORDER BY ub.user_id, t.position ASC, badges.position ASC, ub.granted_at DESC
), upd AS (
  UPDATE users u SET top_badge_id = w.badge_id
  FROM winners w
  WHERE u.id = w.user_id AND u.top_badge_id IS DISTINCT FROM w.badge_id
  RETURNING 1
) SELECT count(*) FROM upd")" || die 5 '迁移前刷 top_badge_id 失败。'
pre_out="granted=${pre_granted:-?} refreshed=${pre_refreshed:-?}"
ok "迁移前 recompute（legacy SQL）：$pre_out"

# 快照：top_badge_id / user_activity / user_badges 三张全表副本。
psql_exec <<'SQL'
CREATE TABLE baseline_top_badge AS SELECT id AS user_id, top_badge_id FROM users;
CREATE TABLE baseline_counters AS SELECT * FROM user_activity;
CREATE TABLE baseline_grants AS SELECT user_id, badge_id FROM user_badges;
SQL
assert_value '迁移前 should_have_but_missing 归零' "$missing_sql_old" 0 || die 5 '迁移前补授予后仍有缺口，基线不可用。'

step "迁移到 $TARGET_MIGRATION（应用规则转换）"
helper upto "$TARGET_MIGRATION" >"$work_dir/up-target.log" 2>&1 || { tail -20 "$work_dir/up-target.log" >&2; die 8 '迁移到目标版本失败。'; }
ok "已到 $TARGET_MIGRATION"

step "迁移后：recompute 必须零变更（M1.1-09）"
post_out="$(helper recompute)" || die 6 '迁移后 recompute 失败。'
printf '  ·    %s\n' "$post_out"
granted="$(printf '%s' "$post_out" | sed -n 's/.*granted=\([-0-9]*\).*/\1/p')"
refreshed="$(printf '%s' "$post_out" | sed -n 's/.*refreshed=\([-0-9]*\).*/\1/p')"
[[ "$granted" == 0 ]] || die 6 "迁移后 recompute 多授予了徽章（granted=$granted）：$post_out"
ok '迁移后 granted = 0'
[[ "$refreshed" == 0 ]] || die 6 "迁移后 recompute 改了 top_badge_id（refreshed=$refreshed）：$post_out"
ok '迁移后 refreshed = 0'

step "迁移后：规则口径的双向对账（M1.1-10）"
missing_sql_new="
SELECT count(*) FROM badges b
JOIN user_activity ua
  ON b.enabled AND b.grant_mode = 'automatic' AND b.rule IS NOT NULL
 AND CASE b.rule->'root'->'params'->>'metric'
       WHEN 'topic_count' THEN ua.topic_count
       WHEN 'comment_count' THEN ua.comment_count
       WHEN 'like_received_count' THEN ua.like_received_count
       WHEN 'like_given_count' THEN ua.like_given_count
       WHEN 'bookmark_count' THEN ua.bookmark_count
       WHEN 'days_visited' THEN ua.days_visited::bigint
     END >= (b.rule->'root'->'cmp'->>'value')::bigint
WHERE NOT EXISTS (SELECT 1 FROM user_badges ub WHERE ub.user_id = ua.user_id AND ub.badge_id = b.id)"
extra_sql_new="
SELECT count(*) FROM user_badges ub
JOIN badges b ON b.id = ub.badge_id
JOIN user_activity ua ON ua.user_id = ub.user_id
WHERE ub.granted_by IS NULL AND ub.grant_reason IN ('', 'recompute')
  AND b.grant_mode = 'automatic' AND b.rule IS NOT NULL
  AND COALESCE(CASE b.rule->'root'->'params'->>'metric'
       WHEN 'topic_count' THEN ua.topic_count
       WHEN 'comment_count' THEN ua.comment_count
       WHEN 'like_received_count' THEN ua.like_received_count
       WHEN 'like_given_count' THEN ua.like_given_count
       WHEN 'bookmark_count' THEN ua.bookmark_count
       WHEN 'days_visited' THEN ua.days_visited::bigint
     END, -1) < (b.rule->'root'->'cmp'->>'value')::bigint"
assert_value 'should_have_but_missing = 0 行' "$missing_sql_new" 0 || die 7 '迁移后仍少授予。'
assert_value 'has_but_should_not = 0 行' "$extra_sql_new" 0 || die 7 '迁移后多授予。'

step "迁移后：快照比对（M1.1-11 / M1.1-12）"
assert_value 'top_badge_id 差集 0 行' "
SELECT count(*) FROM (
  SELECT b.user_id, b.top_badge_id FROM baseline_top_badge b
  EXCEPT SELECT u.id, u.top_badge_id FROM users u
) diff" 0 || die 7 'top_badge_id 被迁移改动。'
assert_value 'user_activity 全表逐行一致' "
SELECT count(*) FROM (
  SELECT * FROM baseline_counters
  EXCEPT SELECT * FROM user_activity
) diff" 0 || die 7 '迁移改动了 user_activity（统计不得回填）。'
assert_value 'user_badges 授予集合一致' "
SELECT count(*) FROM (
  SELECT user_id, badge_id FROM baseline_grants
  EXCEPT SELECT user_id, badge_id FROM user_badges
) diff" 0 || die 7 '迁移改动了授予集合。'
assert_value 'M1.1-04 非内置自动徽章也有规则' "
SELECT count(*) FROM badges WHERE slug = 'baseline-custom' AND rule IS NOT NULL" 1 || die 7 '非内置自动徽章没被转换（写死 slug 白名单？）。'
assert_value '回滚快照行数 = 自动徽章数' "
SELECT count(*) FROM badge_rules_rollback" "$(psql_value "SELECT count(*) FROM badges WHERE grant_mode = 'automatic'")" || die 7 '回滚快照行数与自动徽章数不符。'

printf '\n全部通过：迁移前后 recompute 零变更、双向对账 0 行、快照逐行一致。\n'
