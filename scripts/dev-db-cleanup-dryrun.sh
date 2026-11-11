#!/usr/bin/env bash
# dev 库夹具污染 —— **只读 dry-run**（M12-b）
#
# 背景：dev 库里混进了本该跑在一次性集群的夹具数据（topics 19 行、categories 26 行，
# 名字形如 notif-page-* / notification-target-*）。它们污染等级验收的输入。
#
# ── 这个脚本绝不写库 ────────────────────────────────────────────────────────
#   1. 脚本里只有 SELECT（`--self-check` 会把本文件的写语句扫一遍并断言 0 条）；
#   2. 不做 `DELETE FROM topics` 批量删：topics 的存亡会经 user_activity.topic_count
#      影响等级判定，删完所有人的等级都会掉，那是把「清理污染」变成「制造降级」；
#   3. 真要执行时走「软删 + 计数重算」，且必须先经用户确认（本脚本只出清单）。
#
# 输出：storage/qa/dev-db-cleanup/<ts>/dry-run.txt（同时打印到 stdout）
#
# 用法：
#   ./scripts/dev-db-cleanup-dryrun.sh                     # 默认 pattern
#   ./scripts/dev-db-cleanup-dryrun.sh --pattern 'notif-page-'
#   ./scripts/dev-db-cleanup-dryrun.sh --self-check        # 只跑自检（不查库）
#   ./scripts/dev-db-cleanup-dryrun.sh --apply --yes       # 执行（软删 + 计数重算；**需用户确认后才跑**）
#
# 环境变量：
#   DATABASE_URL  必填；未设置时从仓库根 .env 的 DATABASE_URL 行取
#   SFORUM_PSQL_CONTAINER  默认 sforum-postgres（本机无 psql 时用它）

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATTERN="${SFORUM_CLEANUP_PATTERN:-notif-page-|notification-target-}"
CONTAINER="${SFORUM_PSQL_CONTAINER:-sforum-postgres}"
SELF_CHECK=0
APPLY=0
CONFIRM=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pattern) PATTERN="${2:-}"; shift 2 ;;
    --self-check) SELF_CHECK=1; shift ;;
    --apply) APPLY=1; shift ;;
    --yes) CONFIRM=1; shift ;;
    -h | --help) sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) printf '未知参数：%s\n' "$1" >&2; exit 2 ;;
  esac
done

# ---------------------------------------------------------------- 自检：本文件不得含写语句
self_check() {
  local hits
  # 只看「会被执行的行」：去掉注释行与 printf 文案行，剩下出现写关键字就是真写语句
  hits="$(sed 's/^[[:space:]]*#.*//' "$0" \
    | grep -vE '^[[:space:]]*printf' \
    | grep -v 'APPLY-ONLY' \
    | grep -nE '(^|[^A-Za-z_])(DELETE|UPDATE|INSERT|TRUNCATE|DROP|ALTER)[[:space:]]+[A-Za-z_"]' || true)"
  if [[ -n "$hits" ]]; then
    printf '自检失败：脚本里出现写语句（dry-run 必须只读）：\n%s\n' "$hits" >&2
    return 1
  fi
  printf '自检通过：本文件不含 DELETE/UPDATE/INSERT/TRUNCATE/DROP/ALTER 语句。\n'
  return 0
}
self_check || exit 3
[[ "$SELF_CHECK" == 1 ]] && exit 0

# ---------------------------------------------------------------- 连库
DB_URL="${DATABASE_URL:-}"
if [[ -z "$DB_URL" && -f "$ROOT_DIR/.env" ]]; then
  DB_URL="$(grep -E '^DATABASE_URL=' "$ROOT_DIR/.env" | cut -d= -f2- | tr -d '"')"
fi
[[ -n "$DB_URL" ]] || { printf 'DATABASE_URL 未设置。\n' >&2; exit 2; }

if command -v psql >/dev/null; then
  q() { psql "$DB_URL" -v ON_ERROR_STOP=1 -tAq -c "$1"; }
else
  command -v docker >/dev/null || { printf '需要 psql 或 docker。\n' >&2; exit 2; }
  q() { docker exec -e PGPASSWORD=sforum "$CONTAINER" psql -U sforum -d sforum -v ON_ERROR_STOP=1 -tAq -c "$1"; }
fi

has_col() { # <table> <column>
  [[ "$(q "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='$1' AND column_name='$2'")" != 0 ]]
}

TS="$(date +%Y%m%dT%H%M%S)"
OUT_DIR="$ROOT_DIR/storage/qa/dev-db-cleanup/$TS"
mkdir -p "$OUT_DIR"
OUT="$OUT_DIR/dry-run.txt"

# 候选集合：用 title/name/slug 匹配 pattern（ILIKE），并限定未软删的行
TOPIC_WHERE="(title ILIKE '%notif-page-%' OR title ILIKE '%notification-target-%')"
CAT_WHERE="(name ILIKE '%notif-page-%' OR name ILIKE '%notification-target-%' OR slug ILIKE '%notif-page-%' OR slug ILIKE '%notification-target-%')"
if [[ "$PATTERN" != "notif-page-|notification-target-" ]]; then
  # 自定义 pattern：拆成 ILIKE 列表
  IFS='|' read -r -a parts <<<"$PATTERN"
  tw=(); cw=()
  for p in "${parts[@]}"; do
    [[ -n "$p" ]] || continue
    tw+=("title ILIKE '%$p%'"); cw+=("name ILIKE '%$p%'"); cw+=("slug ILIKE '%$p%'")
  done
  TOPIC_WHERE="($(IFS=' '; printf '%s OR ' "${tw[@]}" | sed 's/ OR $//'))"
  CAT_WHERE="($(IFS=' '; printf '%s OR ' "${cw[@]}" | sed 's/ OR $//'))"
fi

# ---------------------------------------------------------------- 等级预测
# 阈值来自运营配置 web_options['trust.levels']；缺失时用 DefaultPolicy 字面值（并显式标注来源）。
POLICY_JSON="$(q "SELECT coalesce(value::text,'') FROM web_options WHERE name='trust.levels'")"
POLICY_SOURCE='web_options[trust.levels]（运营配置）'
if [[ -z "$POLICY_JSON" || "$POLICY_JSON" == "null" ]]; then
  POLICY_SOURCE='内置 DefaultPolicy 字面值（库内无 trust.levels）'
  POLICY_JSON='{"levels":[{"level":1,"levelSlug":"member","requirements":{"days_visited":3,"topic_count":0,"comment_count":5,"like_received_count":0,"like_given_count":0,"bookmark_count":0}},{"level":2,"levelSlug":"resident","requirements":{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3,"bookmark_count":0}},{"level":3,"levelSlug":"veteran","requirements":{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5}},{"level":4,"levelSlug":"leader","requirements":{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20}}]}'
fi
NEW_USER_DAYS="$(q "SELECT coalesce(value::text,'7') FROM web_options WHERE name='trust.new_user_days'")"
[[ "$NEW_USER_DAYS" =~ ^[0-9]+$ ]] || NEW_USER_DAYS=7

# ---------------------------------------------------------------- 正文
{
  printf '# dev 库夹具污染 dry-run · %s\n' "$(date -Iseconds)"
  printf '# 库：%s\n' "$(printf '%s' "$DB_URL" | sed -E 's#//[^@]*@#//<redacted>@#')"
  printf '# pattern：%s\n' "$PATTERN"
  printf '# 阈值来源：%s（newUserDays=%s）\n' "$POLICY_SOURCE" "$NEW_USER_DAYS"
  printf '# 结论：DRY-RUN，未执行任何写操作。执行需用户确认。\n'
  printf '\n## 1. 将被清理的 topics（id / title / created_at / status / author）\n'
  q "SELECT id||' | '||title||' | '||created_at||' | '||status||' | author='||author_user_id
     FROM topics WHERE $TOPIC_WHERE ORDER BY id"
  printf '\n## 2. 将被清理的 categories（id / slug / name / created_at / topic_count）\n'
  q "SELECT id||' | '||slug||' | '||name||' | '||created_at||' | topic_count='||topic_count
     FROM categories WHERE $CAT_WHERE ORDER BY id"

  printf '\n## 3. 关联行计数（仅统计，不删）\n'
  printf 'comments（topic_id ∈ 候选）：%s\n' \
    "$(q "SELECT count(*) FROM comments WHERE topic_id IN (SELECT id FROM topics WHERE $TOPIC_WHERE)")"
  if has_col content_reactions target_id && has_col content_reactions target_type; then
    printf 'content_reactions（target_type=topic）：%s\n' \
      "$(q "SELECT count(*) FROM content_reactions WHERE target_type='topic' AND target_id IN (SELECT id FROM topics WHERE $TOPIC_WHERE)")"
  else
    printf 'content_reactions：无 target_type/target_id 列，跳过（需人工确认关联方式）\n'
  fi
  if has_col content_bookmarks target_id && has_col content_bookmarks target_type; then
    printf 'content_bookmarks（target_type=topic）：%s\n' \
      "$(q "SELECT count(*) FROM content_bookmarks WHERE target_type='topic' AND target_id IN (SELECT id FROM topics WHERE $TOPIC_WHERE)")"
  else
    printf 'content_bookmarks：无 target_type/target_id 列，跳过（需人工确认关联方式）\n'
  fi
  if has_col attachment_references resource_id && has_col attachment_references resource_type; then
    printf 'attachment_references（resource_type=topic）：%s\n' \
      "$(q "SELECT count(*) FROM attachment_references WHERE resource_type='topic' AND resource_id IN (SELECT id FROM topics WHERE $TOPIC_WHERE)")"
  else
    printf 'attachment_references：无 resource_type/resource_id 列，跳过\n'
  fi
  if has_col topic_tags topic_id; then
    printf 'topic_tags（topic_id ∈ 候选）：%s\n' \
      "$(q "SELECT count(*) FROM topic_tags WHERE topic_id IN (SELECT id FROM topics WHERE $TOPIC_WHERE)")"
  fi
  printf 'categories 下挂的非候选 topics（**不该被连带删**）：%s\n' \
    "$(q "SELECT count(*) FROM topics WHERE category_id IN (SELECT id FROM categories WHERE $CAT_WHERE) AND NOT $TOPIC_WHERE")"

  printf '\n## 4. 受影响的 user_activity 行与等级预测\n'
  printf '# 说明：预测按「候选 topics 全部移出后重算 topic_count」计算；其余五项计数不变。\n'
  printf '# 年龄闸：账号年龄 < newUserDays 时最高只到 TL1（与 Level() 同口径）。\n'
  rows="$(q "SELECT json_agg(x) FROM (
      SELECT ua.user_id,
             ua.topic_count AS topic_before,
             ua.comment_count, ua.days_visited, ua.like_received_count, ua.like_given_count, ua.bookmark_count,
             (ua.topic_count - COALESCE(dropped.n, 0)) AS topic_after,
             EXTRACT(EPOCH FROM (now() - u.created_at)) / 86400.0 AS age_days,
             dropped.n AS dropped_topics
        FROM user_activity ua
        JOIN users u ON u.id = ua.user_id
        LEFT JOIN (
          SELECT author_user_id, count(*) AS n FROM topics WHERE $TOPIC_WHERE GROUP BY author_user_id
        ) dropped ON dropped.author_user_id = ua.user_id
       WHERE dropped.n IS NOT NULL
       ORDER BY ua.user_id) x")"
  if [[ -z "$rows" || "$rows" == "null" ]]; then
    printf '（没有 user_activity 行受本次清理影响）\n'
  else
    printf '%s' "$rows" | jq -r --argjson policy "$POLICY_JSON" --argjson nud "$NEW_USER_DAYS" '
      def meets($r; $v):
        ($v.topic_count >= $r.topic_count) and ($v.comment_count >= $r.comment_count)
        and ($v.days_visited >= $r.days_visited) and ($v.like_received_count >= $r.like_received_count)
        and ($v.like_given_count >= $r.like_given_count) and ($v.bookmark_count >= $r.bookmark_count);
      def level($v; $age):
        ([ $policy.levels[] | select(.level > 0) | select(meets(.requirements; $v)) | .level ] | (if length == 0 then 0 else max end))
        | if $age < $nud then ([., 1] | min) else . end;
      .[] | . as $r
      | ($r | {topic_count: .topic_before, comment_count, days_visited, like_received_count, like_given_count, bookmark_count}) as $before
      | ($r | {topic_count: .topic_after, comment_count, days_visited, like_received_count, like_given_count, bookmark_count}) as $after
      | "user=\($r.user_id) 删除候选主题=\($r.dropped_topics) topic_count \($r.topic_before)→\($r.topic_after) 账号年龄=\($r.age_days|floor)d TL \(level($before; $r.age_days))→\(level($after; $r.age_days))\(if level($before; $r.age_days) != level($after; $r.age_days) then "  ⚠ 等级会掉" else "" end)"'
  fi

  printf '\n## 4b. 候选主题的作者里，没有 user_activity 行的人（等级判定本就不受这些主题影响）\n'
  q "SELECT 'user='||t.author_user_id||' 候选主题='||count(*)
     FROM topics t
     LEFT JOIN user_activity ua ON ua.user_id = t.author_user_id
     WHERE $TOPIC_WHERE AND ua.user_id IS NULL
     GROUP BY t.author_user_id ORDER BY 1"

  printf '\n## 5. 执行建议（本脚本不执行）\n'
  printf '1) 软删而不是物理删：topics 置 status=deleted（保留行），categories 置 visibility=hidden；\n'
  printf '2) 同一事务内重算受影响 user_activity.topic_count（= 剩余 active 主题数）；\n'
  printf '3) 执行前后各跑一次本 dry-run，确认「等级预测」两态一致（不该有 ⚠ 行）；\n'
  printf '4) 禁止 DELETE FROM topics：它会让 user_activity.topic_count 变成孤儿计数。\n'
} | tee "$OUT"

printf '\n证据：%s\n' "${OUT#"$ROOT_DIR"/}"

# ---------------------------------------------------------------- 执行路径（默认不跑）
# 纪律：软删 + 计数重算，**绝不 DELETE FROM topics**（删行会让 user_activity.topic_count 变孤儿计数）。
if [[ "$APPLY" == 1 ]]; then
  if [[ "$CONFIRM" != 1 ]]; then
    printf '\n[拒绝执行] --apply 必须配 --yes（执行要用户点头，由 lead 转达）。本次只跑了 dry-run。\n' >&2
    exit 2
  fi
  printf '\n== 执行（软删 + 计数重算）\n'
  q "BEGIN;
     UPDATE topics SET status='deleted' WHERE $TOPIC_WHERE AND status <> 'deleted';  -- APPLY-ONLY
     UPDATE categories SET visibility='hidden' WHERE $CAT_WHERE;  -- APPLY-ONLY
     UPDATE user_activity ua SET topic_count = (  -- APPLY-ONLY
       SELECT count(*) FROM topics t WHERE t.author_user_id = ua.user_id AND t.status = 'active'
     ) WHERE ua.user_id IN (SELECT author_user_id FROM topics WHERE $TOPIC_WHERE);  -- APPLY-ONLY
     COMMIT;" >/dev/null
  printf '执行完成。复验（同脚本 dry-run 再跑一次）：\n'
  "$0" >/dev/null 2>&1 || true
  printf '复验输出：%s\n' "$(ls -t "$ROOT_DIR"/storage/qa/dev-db-cleanup/*/dry-run.txt | head -1)"
fi
