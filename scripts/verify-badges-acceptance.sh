#!/usr/bin/env bash
# 徽章 + 用户活动统计：验收验证器（M8 自动化部分）。
#
# 验证对象：plans/2026-11-10-badges-and-user-activity.md 第 0 节十条口径里可自动化的部分
# 与第 6 节边界/失败模式。浏览器 QA、视觉、aria 见同目录 verify-badges-acceptance-manual.md。
#
# 设计前提：这个脚本必须在「代码只完成一半」时也有意义，所以每条断言都绑定阶段：
#   M0 迁移     -> 表 / 视图 / 内置数据是否存在
#   M1 统计层   -> 发帖 / 评论 / 点赞 / 收藏后计数是否 +1、取消是否回退、访问是否去重
#   M2 徽章领域 -> 重复评估不重复授予、top_badge_id 维护、自动徽章不可手动收回
#   M3 HTTP     -> 公开/后台端点、错误码、服务端权限（只靠前端隐藏菜单 = FAIL）
# 阶段由仓库文件存在性自动探测（--phase 可强制覆盖），未落地阶段的断言输出 pend 而不是 FAIL。
#
# 注意 S10-1a/1b/1c/1d 不是「徽章检查」，是**扫描器列数探针**：
#   scanTopicSummaryWithAvatar 被 topicSummarySQL / list_topics / search_live 三处复用，
#   任何一处 SELECT 少一列就是运行时 500（"number of field descriptions must equal number of
#   destinations"）。2026-11-10 的 P0（首页列表全站 500）就是这么来的。
#   **任何给读路径加字段的改动，都必须重跑 S10-1a/1b/1c/1d。不要因为「看起来和徽章无关」删掉。**
#   搜索探针必须用 query= 参数并确认命中 > 0：命中 0 条会 early return，200 是假通过。
#
# 三种结果：
#   ok    通过
#   FAIL  已落地阶段的行为不符（要修）
#   pend  对应阶段未落地 / 环境前置不满足（凭证、分类、注册开关），不算失败
#
# 退出码：
#   0  无 FAIL（允许 pend）
#   1  有 FAIL
#   2  预检失败（缺工具、API 不可达、库不可查）
#   3  开发库守卫：命中 public.sforum_dev_guard，写操作被拒绝，只跑了只读段
#   4  --strict 下存在 pend
#
# 环境变量：
#   DATABASE_URL                     必填。与 API 同一个库；脚本只用它读真值。
#   SFORUM_API_URL                   默认 http://127.0.0.1:8080/api/v1
#   SFORUM_ACCEPT_CSRF_ORIGIN        默认 $APP_URL 或 http://127.0.0.1:3000
#   SFORUM_ACCEPT_USER_A_LOGIN       A：发帖 / 评论 / 收藏的操作者
#   SFORUM_ACCEPT_USER_A_PASSWORD
#   SFORUM_ACCEPT_USER_B_LOGIN       B：给 A 点赞的第二用户
#   SFORUM_ACCEPT_USER_B_PASSWORD
#   SFORUM_ACCEPT_ADMIN_LOGIN        有 badge.manage 的管理员（边界段需要）
#   SFORUM_ACCEPT_ADMIN_PASSWORD
#   SFORUM_ACCEPT_CATEGORY_SLUG      发帖分类，缺省取 /categories 第一个
#   SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1  允许在开发库上造数据（默认拒绝）
#   SFORUM_ACCEPT_PSQL_IMAGE         无本机 psql 时用哪个镜像（默认 postgres:17-alpine）
#   SFORUM_ACCEPT_SQL_CMD            自定义查库命令（读 stdin 的 SQL，只输出值）
#   SFORUM_ACCEPT_KEEP=1             保留造出来的主题/徽章，便于排查
#   SFORUM_ACCEPT_CACHE_WAIT=35      授予后等多少秒再查 API（默认 0：只提示，不判）
#   SFORUM_ACCEPT_CONCURRENCY        并发点赞账号数（默认 10）
#   SFORUM_ACCEPT_CREDS_FILE         QA 账号矩阵文件（默认 storage/qa/creds.txt，按甲/乙/戊行解析）
#   SFORUM_ACCEPT_REDIS_CMD          清访问去重键用的 redis-cli 前缀（默认 docker exec sforum-redis redis-cli）
#   SFORUM_ACCEPT_REDIS_CONTAINER    Redis 容器名（默认 sforum-redis）
#
# 用法：
#   DATABASE_URL=... ./scripts/verify-badges-acceptance.sh            # 自动探测阶段
#   ./scripts/verify-badges-acceptance.sh --read-only                # 只跑只读段
#   ./scripts/verify-badges-acceptance.sh --phase=m3 --strict        # M8 收口门禁

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
WEB_DIR="$ROOT_DIR/apps/web"
MIGRATION_FILE="$API_DIR/database/migrations/202611100002_badges_and_user_activity.sql"
BASELINE_JSON="$ROOT_DIR/tests/architecture-boundaries-baseline.json"

API_URL="${SFORUM_API_URL:-http://127.0.0.1:8080/api/v1}"
API_URL="${API_URL%/}"
DB_URL="${DATABASE_URL:-}"
CSRF_ORIGIN="${SFORUM_ACCEPT_CSRF_ORIGIN:-${APP_URL:-http://127.0.0.1:3000}}"
CSRF_ORIGIN="${CSRF_ORIGIN%/}"

READ_ONLY=0
STRICT=0
KEEP="${SFORUM_ACCEPT_KEEP:-0}"
PHASE_OVERRIDE=""
CACHE_WAIT="${SFORUM_ACCEPT_CACHE_WAIT:-0}"

work="$(mktemp -d)"
cleanup() {
  local status=$?
  trap - EXIT
  [[ -n "$work" && -d "$work" ]] && rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

usage() { sed -n '2,46p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

for arg in "$@"; do
  case "$arg" in
    --read-only) READ_ONLY=1 ;;
    --strict) STRICT=1 ;;
    --keep) KEEP=1 ;;
    --phase=*) PHASE_OVERRIDE="${arg#--phase=}" ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done

PASS_N=0
FAIL_N=0
PEND_N=0
FAIL_LINES=()
PEND_LINES=()
GUARD_REFUSED=0
HTTP_READY=0
ADMIN_READY=0
BADGES_FLAG_STUCK=0
CONN_FAILS=0

step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
warn() { printf '  注意 %s\n' "$*" >&2; }
pass() { PASS_N=$((PASS_N + 1)); printf '  ok    %s\n' "$1"; }
fail() {
  FAIL_N=$((FAIL_N + 1))
  FAIL_LINES+=("$1${2:+ — $2}")
  printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"
}
pend() {
  PEND_N=$((PEND_N + 1))
  PEND_LINES+=("$1${2:+ — $2}")
  printf '  pend  %s%s\n' "$1" "${2:+（$2）}"
}
die() {
  local code="$1"
  shift
  printf '\n[exit %s] %s\n' "$code" "$*" >&2
  exit "$code"
}

# ---------------------------------------------------------------- 阶段探测

phase_ready() {
  local want=$1
  if [[ -n "$PHASE_OVERRIDE" ]]; then
    case "$PHASE_OVERRIDE" in
      full) return 0 ;;
      auto) ;;
      m0) [[ "$want" == m0 ]] && return 0 || return 1 ;;
      m1) [[ "$want" == m0 || "$want" == m1 ]] && return 0 || return 1 ;;
      m2) [[ "$want" == m0 || "$want" == m1 || "$want" == m2 ]] && return 0 || return 1 ;;
      m3) [[ "$want" != m5 ]] && return 0 || return 1 ;;
      m5) return 0 ;;
      *) die 2 "未知 --phase 值：$PHASE_OVERRIDE（auto|m0|m1|m2|m3|m5|full）" ;;
    esac
  fi
  case "$want" in
    m0) [[ -f "$MIGRATION_FILE" ]] ;;
    m1) [[ -f "$API_DIR/app/Models/Badges/activity_store.go" || -f "$API_DIR/app/Models/Badges/activity.go" ]] ;;
    m2) [[ -f "$API_DIR/app/Models/Badges/evaluate.go" && -f "$API_DIR/app/Models/Badges/service.go" ]] ;;
    m3) [[ -f "$API_DIR/app/Http/Controllers/Badges/routes.go" ]] ;;
    m5) [[ -f "$WEB_DIR/app/components/forum/SFBadgeChip.vue" ]] ;;
    *) return 1 ;;
  esac
}

phase_reason() {
  case "$1" in
    m0) printf 'M0 迁移未落地（缺 %s）' "${MIGRATION_FILE#"$ROOT_DIR"/}" ;;
    m1) printf 'M1 统计层未落地（缺 app/Models/Badges/activity_store.go）' ;;
    m2) printf 'M2 徽章领域未落地（缺 app/Models/Badges/evaluate.go 或 service.go）' ;;
    m3) printf 'M3 HTTP 层未落地（缺 app/Http/Controllers/Badges/routes.go）' ;;
    m5) printf 'M5 前端未落地（缺 app/components/forum/SFBadgeChip.vue）' ;;
    *) printf '阶段未落地' ;;
  esac
}

phase_flag() { if phase_ready "$1"; then printf '就绪'; else printf '未落地'; fi; }

# ---------------------------------------------------------------- 预检

step "预检"
command -v curl >/dev/null || die 2 '缺少 curl。'
command -v jq >/dev/null || die 2 '缺少 jq。'
[[ -n "$DB_URL" ]] || die 2 'DATABASE_URL 未设置：脚本需要直连 API 所用的同一个库来读真值。'

# 查库通道：本机 psql -> docker 里的 psql -> 自定义命令。
SQL_RUNNER=()
if [[ -n "${SFORUM_ACCEPT_SQL_CMD:-}" ]]; then
  SQL_RUNNER=(bash -c "$SFORUM_ACCEPT_SQL_CMD")
elif command -v psql >/dev/null; then
  SQL_RUNNER=(psql "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
elif command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  SQL_RUNNER=(docker run --rm -i --network=host --entrypoint psql "${SFORUM_ACCEPT_PSQL_IMAGE:-postgres:17-alpine}" "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
  note "本机无 psql，改用 docker 里的 psql（镜像 ${SFORUM_ACCEPT_PSQL_IMAGE:-postgres:17-alpine}）。"
else
  die 2 '查库需要 psql 或可用的 docker（或设置 SFORUM_ACCEPT_SQL_CMD）。'
fi

SQL_LAST_ERROR=""
sqlv() {
  local out rc
  out="$("${SQL_RUNNER[@]}" -c "$1" 2>"$work/sql.err")"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    SQL_LAST_ERROR="$(tr '\n' ' ' <"$work/sql.err" | cut -c1-400)"
    return $rc
  fi
  printf '%s' "$out" | tr -d '\r' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'
}

if ! probe="$(sqlv 'SELECT 1')" || [[ "$probe" != 1 ]]; then
  die 2 "无法查库（$DB_URL）：${SQL_LAST_ERROR:-无输出}"
fi
ok_db="$(sqlv 'SELECT current_database()')" || die 2 '无法读取 current_database()。'
note "目标库：${ok_db:-?}"

# 开发库哨兵：命中即拒绝一切写操作，除非显式放行。
DEV_DB=0
[[ "$(sqlv "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL")" == t ]] && DEV_DB=1

MUTATIONS=1
if [[ "$READ_ONLY" == 1 ]]; then
  MUTATIONS=0
  note '--read-only：本次不造主题/评论/徽章、不翻开关，写类断言一律 pend。'
fi
if [[ "$DEV_DB" == 1 ]]; then
  if [[ "$READ_ONLY" == 1 ]]; then
    MUTATIONS=0
    note '开发库哨兵命中 + --read-only：只跑只读段。'
  elif [[ "${SFORUM_ACCEPT_ALLOW_DEV_MUTATION:-0}" == 1 ]]; then
    warn '开发库哨兵命中，但 SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1：脚本会在开发库里造主题/评论/徽章。'
  else
    MUTATIONS=0
    GUARD_REFUSED=1
    warn '开发库哨兵命中（public.sforum_dev_guard）：拒绝写操作，只跑只读段。'
    warn '要在开发库上造数据请显式设 SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1；要干净的证据请用一次性集群。'
  fi
fi

# API 可达性：只读段不依赖它，但先探一次便于报告。
# 起跑门用 /api/v1/health（注意不是 /health，那个 404），并要求连续两次 200：
# dev 的 air 会在改代码时重启，单次探测通过也可能在跑到一半时挂掉。
HEALTH_WAIT="${SFORUM_ACCEPT_WAIT_API:-45}"
api_alive() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/health" 2>/dev/null)"
  [[ "$code" == 200 ]] && return 0
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/auth/session" 2>/dev/null)"
  [[ "$code" == 200 || "$code" == 401 || "$code" == 403 ]]
}
api_reachable=0
if [[ "$READ_ONLY" != 1 ]]; then
  consecutive=0
  deadline=$((SECONDS + HEALTH_WAIT))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    if api_alive; then
      consecutive=$((consecutive + 1))
      [[ "$consecutive" -ge 2 ]] && { api_reachable=1; break; }
    else
      consecutive=0
    fi
    sleep 2
  done
  if [[ "$api_reachable" != 1 ]]; then
    note "API 在 ${HEALTH_WAIT}s 内没有连续两次健康（$API_URL/health）：HTTP 段整体 pend。"
    [[ "$STRICT" == 1 ]] && die 2 "API 不稳定，别在重建窗口跑 --strict；先 ./scripts/api-dev.sh 起稳再跑。"
  fi
else
  api_alive && api_reachable=1
fi
if [[ "$api_reachable" == 1 ]]; then
  note "API 可达：$API_URL"
else
  note "API 不可达：$API_URL（HTTP 段会整体 pend；只读段不受影响）"
fi

step "阶段探测"
note "M0 迁移 $(phase_flag m0) · M1 统计 $(phase_flag m1) · M2 徽章 $(phase_flag m2) · M3 HTTP $(phase_flag m3) · M5 前端 $(phase_flag m5)"
# 阶段开关集中在这里算一次：后面任何一段引用都不会再遇到「变量未绑定」。
M1_READY=0; phase_ready m1 && M1_READY=1
M2_READY=0; phase_ready m2 && M2_READY=1
M3_READY=0; phase_ready m3 && M3_READY=1
note '未落地阶段的断言记为 pend；要它们变成 FAIL 用 --phase=full。'

# ---------------------------------------------------------------- 断言原语

# expect_sql <标签> <SQL> <期望值>
expect_sql() {
  local label=$1 sql=$2 want=$3 got
  if ! got="$(sqlv "$sql")"; then
    fail "$label" "查询失败：${SQL_LAST_ERROR:-无输出}"
    return 1
  fi
  if [[ "$got" == "$want" ]]; then
    pass "$label"
    return 0
  fi
  fail "$label" "期望 $want，实际 ${got:-<空>}"
  return 1
}

# report_delta <标签> <操作描述> <被查字段> <期望增量> <实际增量> <阶段就绪 0/1>
# 每条都打印「操作 → 期望 → 实际」三行对照，这是验收证据的正文。
report_delta() {
  local label=$1 op=$2 field=$3 want=$4 got=$5 ready=$6
  printf '  [%s]\n    操作: %s\n    期望: %s 增量 = %s\n    实际: %s 增量 = %s\n' \
    "$label" "$op" "$field" "$want" "$field" "$got"
  if [[ "$got" == "$want" ]]; then
    pass "$label"
    return 0
  fi
  if [[ "$ready" != 1 ]]; then
    pend "$label" "$(phase_reason m1)：计数未写入属预期，表存在即已确认结构"
    return 0
  fi
  fail "$label" "期望增量 $want，实际 $got"
  return 1
}

# report_value <标签> <操作描述> <被查字段> <期望值> <实际值> <阶段就绪 0/1> [pend 原因]
report_value() {
  local label=$1 op=$2 field=$3 want=$4 got=${5:-} ready=${6:-1} reason=${7:-}
  printf '  [%s]\n    操作: %s\n    期望: %s = %s\n    实际: %s = %s\n' \
    "$label" "$op" "$field" "$want" "$field" "${got:-<空>}"
  if [[ "$got" == "$want" ]]; then
    pass "$label"
    return 0
  fi
  if [[ "$ready" != 1 ]]; then
    pend "$label" "${reason:-阶段未落地}"
    return 0
  fi
  fail "$label" "期望 $want，实际 ${got:-<空>}"
  return 1
}

# ---------------------------------------------------------------- HTTP 原语

cookie_value() { awk -v n="$2" '$6 == n { v = $7 } END { if (v != "") print v }' "$1" 2>/dev/null; }

HTTP_STATUS=000
HTTP_BODY=''

# api <METHOD> <PATH> <COOKIE_JAR> [JSON_BODY]
api() {
  local method=$1 path=$2 jar=$3 body=${4:-}
  local -a args=(-sS -o "$work/body.out" -w '%{http_code}' -X "$method"
    -b "$jar" -c "$jar"
    -H "Origin: $CSRF_ORIGIN" -H "Referer: $CSRF_ORIGIN/")
  local token
  token="$(cookie_value "$jar" csrf_)"
  [[ -n "$token" ]] && args+=(-H "X-CSRF-Token: $token")
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' --data-binary "$body")
  fi
  local attempt
  for attempt in 1 2 3 4; do
    # 必须先清空：curl 连接失败时不会截断 -o 文件，残留的上一份响应会冒充本次结果。
    : >"$work/body.out"
    HTTP_STATUS="$(curl "${args[@]}" --max-time 20 "$API_URL$path" 2>"$work/curl.err")" || HTTP_STATUS=000
    HTTP_BODY="$(cat "$work/body.out" 2>/dev/null || true)"
    case "$HTTP_STATUS" in
      429) [[ "$attempt" -lt 4 ]] && sleep "$((attempt * 3))" ;;   # 限流：退避重试
      000) [[ "$attempt" -lt 4 ]] && sleep 2 ;;                    # 连接失败（API 正在重建）：短退避
      *) return 0 ;;
    esac
  done
  # 重试后仍是 000：计入「本轮结论不完整」，别让读者以为全部验过了。
  [[ "$HTTP_STATUS" == 000 ]] && CONN_FAILS=$((CONN_FAILS + 1))
  return 0
}

body_excerpt() { printf '%s' "$HTTP_BODY" | tr '\n' ' ' | cut -c1-220; }

# 首个出现的同名标量：不猜响应 envelope，逐层找 key。
json_first() { printf '%s' "$HTTP_BODY" | jq -r --arg k "$1" '[.. | objects | .[$k]? // empty] | first // empty' 2>/dev/null; }

prime_csrf() { api GET /auth/session "$1" >/dev/null; }

# expect_status <标签> <期望码,逗号分隔> <METHOD> <PATH> <JAR> [BODY] [阶段就绪 0/1]
expect_status() {
  local label=$1 want=$2 ready=${7:-1}
  shift 2
  api "$@"
  if [[ ",$want," == *",$HTTP_STATUS,"* ]]; then
    pass "$label（HTTP $HTTP_STATUS）"
    return 0
  fi
  if [[ "$HTTP_STATUS" == 429 ]]; then
    pend "$label" "期望 HTTP $want，实际 429（被限流，不是产品行为）"
    return 0
  fi
  if [[ "$HTTP_STATUS" == 000 || "$HTTP_STATUS" == 5* ]]; then
    fail "$label" "期望 HTTP $want，实际 $HTTP_STATUS（$(body_excerpt)）"
    return 1
  fi
  if [[ "$ready" != 1 ]]; then
    pend "$label" "期望 HTTP $want，实际 $HTTP_STATUS（$(body_excerpt)）— 阶段未落地"
    return 0
  fi
  fail "$label" "期望 HTTP $want，实际 $HTTP_STATUS（$(body_excerpt)）"
  return 1
}

login_into() { # <jar> <login> <password>
  local jar=$1 login=$2 password=$3
  prime_csrf "$jar"
  api POST /auth/login "$jar" "$(jq -nc --arg l "$login" --arg p "$password" '{login:$l,password:$p}')"
  [[ "$HTTP_STATUS" == 200 ]]
}

uid_of() { # <username> -> user id
  local name=$1
  [[ "$name" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  sqlv "SELECT id FROM users WHERE username = '$name'"
}

act() { # <user_id> <column>
  sqlv "SELECT coalesce((SELECT $2 FROM user_activity WHERE user_id = $1), 0)"
}

topic_body() { # <title> <text>
  local raw
  raw="$(jq -nc --arg t "$2" '{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:$t}]}]}')"
  jq -nc --arg cat "$CATEGORY_SLUG" --arg title "$1" --arg raw "$raw" \
    '{categorySlug:$cat,title:$title,tagSlugs:[],content:{rawContent:$raw,sourceFormat:"editor-document"}}'
}

comment_body() { # <text>
  local raw
  raw="$(jq -nc --arg t "$1" '{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:$t}]}]}')"
  jq -nc --arg raw "$raw" '{parentId:null,content:{rawContent:$raw,sourceFormat:"editor-document"}}'
}

# ---------------------------------------------------------------- S0 结构就绪度

step "S0 结构就绪度（迁移是否已应用到该库）"
if ! phase_ready m0; then
  pend 'M0 迁移文件未落地' "$(phase_reason m0)"
fi

table_exists() { [[ "$(sqlv "SELECT to_regclass('public.$1') IS NOT NULL")" == t ]]; }

if ! table_exists user_activity; then
  pend 'user_activity 表不存在' "迁移 202611100002 未应用到该库（$ok_db）；先跑 apps/api/cmd/migrate 再重跑"
  note '这就是「代码只完成一半」的有效证据：结构未就绪，后面的统计/徽章断言无意义。'
  SCHEMA_READY=0
else
  SCHEMA_READY=1
  for t in user_activity user_visits badge_groups badges user_badges; do
    expect_sql "表 $t 存在" "SELECT to_regclass('public.$t') IS NOT NULL" t
  done
  expect_sql '列 users.top_badge_id 存在' \
    "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='top_badge_id')" t
  for v in forum_badges forum_user_badges forum_user_activity; do
    expect_sql "视图 sforum_core_v1.$v 存在" "SELECT to_regclass('sforum_core_v1.$v') IS NOT NULL" t
  done
  expect_sql '视图全部 security_barrier' \
    "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname='sforum_core_v1' AND c.relname IN ('forum_badges','forum_user_badges','forum_user_activity') AND c.reloptions @> ARRAY['security_barrier=true']" 3
  expect_sql '内置徽章 12 个' 'SELECT count(*) FROM public.badges' 12
  expect_sql '徽章分组 3 个' 'SELECT count(*) FROM public.badge_groups' 3
  expect_sql '分组键 activity/content/honor 齐全' \
    "SELECT count(*) FROM public.badge_groups WHERE key IN ('activity','content','honor')" 3
  expect_sql '自动徽章规则都合法（metric+threshold>0）' \
    "SELECT count(*) FROM public.badges WHERE grant_mode='automatic' AND (metric IS NULL OR threshold IS NULL OR threshold <= 0)" 0
  expect_sql '手动徽章不带规则' \
    "SELECT count(*) FROM public.badges WHERE grant_mode='manual' AND (metric IS NOT NULL OR threshold IS NOT NULL)" 0
  expect_sql 'user_activity 非负 CHECK 约束 >= 8 条' \
    "SELECT count(*) >= 8 FROM pg_constraint WHERE conrelid='public.user_activity'::regclass AND contype='c'" t
  expect_sql 'user_visits 主键含 (user_id, visited_on)' \
    "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.user_visits'::regclass AND contype='p' AND pg_get_constraintdef(oid) LIKE '%(user_id, visited_on)%')" t
  expect_sql 'user_badges 唯一约束含 (user_id, badge_id)' \
    "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.user_badges'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%(user_id, badge_id)%')" t
  expect_sql '通知描述符 badge_granted 一条' \
    "SELECT count(*) FROM notification_type_descriptors WHERE type='badge_granted'" 1
  expect_sql '通知策略 badge_granted 三条' \
    "SELECT count(*) FROM notification_type_policies WHERE type='badge_granted'" 3
  expect_sql '视图不输出阅读类指标' \
    "SELECT count(*) FROM information_schema.columns WHERE table_schema='sforum_core_v1' AND table_name='forum_user_activity' AND column_name IN ('posts_read_count','read_time_seconds')" 0
  expect_sql '视图不输出审计字段' \
    "SELECT count(*) FROM information_schema.columns WHERE table_schema='sforum_core_v1' AND table_name='forum_user_badges' AND column_name IN ('granted_by','grant_reason')" 0
fi

# ---------------------------------------------------------------- 用户与会话

step "用户与会话"
JAR_A="$work/a.jar"
JAR_B="$work/b.jar"
JAR_ADMIN="$work/admin.jar"
JAR_ANON="$work/anon.jar"
: >"$JAR_A"; : >"$JAR_B"; : >"$JAR_ADMIN"; : >"$JAR_ANON"

RUN_TAG="$(date +%s)$$"
UID_A=""; UID_B=""; UID_ADMIN=""
CATEGORY_SLUG="${SFORUM_ACCEPT_CATEGORY_SLUG:-}"

session_username() { json_first username; }

bootstrap_user() { # <jar> <login-env> <password-env> <label> -> 0 成功
  local jar=$1 login_var=$2 pass_var=$3 label=$4
  local login="${!login_var:-}" password="${!pass_var:-}"
  if [[ -n "$login" && -n "$password" ]]; then
    if login_into "$jar" "$login" "$password"; then
      note "$label 登录成功（$login）"
      return 0
    fi
    warn "$label 登录失败（$login）：HTTP $HTTP_STATUS $(body_excerpt)"
    return 1
  fi
  if [[ "$api_reachable" != 1 ]]; then return 1; fi
  # 无凭证时自建一次性账号：注册 -> 登录。
  local name="acc${RUN_TAG}${label}"
  name="${name//[^A-Za-z0-9_]/}"
  name="${name:0:28}"
  prime_csrf "$jar"
  api POST /auth/register "$jar" "$(jq -nc --arg u "$name" --arg e "$name@accept.test" \
    --arg p 'Accept-Passw0rd!' --arg d "$label" '{username:$u,email:$e,password:$p,displayName:$d,locale:"zh-CN"}')"
  if [[ "$HTTP_STATUS" != 200 && "$HTTP_STATUS" != 201 ]]; then
    warn "$label 自建账号失败（HTTP $HTTP_STATUS）：$(body_excerpt)"
    warn "  设 ${login_var} / ${pass_var} 指向已有账号后重跑。"
    return 1
  fi
  if ! login_into "$jar" "$name" 'Accept-Passw0rd!'; then
    warn "$label 注册成功但登录失败（HTTP $HTTP_STATUS）：$(body_excerpt)"
    return 1
  fi
  note "$label 自建账号成功（$name）"
  return 0
}

# 凭证兜底：pm 的 QA 账号矩阵落在 storage/qa/creds.txt（markdown 表格 + 统一密码）。
# 甲 = 被统计者 / 乙 = 点赞者 / 戊 = super_admin；环境变量优先于文件。
CREDS_FILE="${SFORUM_ACCEPT_CREDS_FILE:-$ROOT_DIR/storage/qa/creds.txt}"
creds_email() { # <代号>
  local row
  row="$(grep -E "^\| *$1 " "$CREDS_FILE" 2>/dev/null | head -1)"
  [[ -z "$row" ]] && return 1
  printf '%s' "$row" | awk -F'|' '{gsub(/^[ \t]+|[ \t]+$/, "", $5); print $5}'
}
creds_password() {
  sed -n 's/^统一密码[：:][[:space:]]*//p' "$CREDS_FILE" 2>/dev/null | head -1
}

if [[ -f "$CREDS_FILE" ]]; then
  _pass="$(creds_password)"
  if [[ -z "${SFORUM_ACCEPT_USER_A_LOGIN:-}" ]]; then
    _email="$(creds_email 甲 || true)"
    if [[ -n "$_email" && -n "$_pass" ]]; then
      SFORUM_ACCEPT_USER_A_LOGIN="$_email"
      SFORUM_ACCEPT_USER_A_PASSWORD="$_pass"
      note "A 取自 creds.txt 甲：$_email"
    fi
  fi
  if [[ -z "${SFORUM_ACCEPT_USER_B_LOGIN:-}" ]]; then
    _email="$(creds_email 乙 || true)"
    if [[ -n "$_email" && -n "$_pass" ]]; then
      SFORUM_ACCEPT_USER_B_LOGIN="$_email"
      SFORUM_ACCEPT_USER_B_PASSWORD="$_pass"
      note "B 取自 creds.txt 乙：$_email"
    fi
  fi
  if [[ -z "${SFORUM_ACCEPT_ADMIN_LOGIN:-}" ]]; then
    _email="$(creds_email 戊 || true)"
    if [[ -n "$_email" && -n "$_pass" ]]; then
      SFORUM_ACCEPT_ADMIN_LOGIN="$_email"
      SFORUM_ACCEPT_ADMIN_PASSWORD="$_pass"
      note "管理员取自 creds.txt 戊：$_email"
    fi
  fi
fi

# spawn_account <jar> <tag>：造一个一次性账号并登录，供并发用例使用。
spawn_account() {
  local jar=$1 tag=$2 name
  name="acc$(printf '%s' "$RUN_TAG" | tail -c 7)${tag}"
  name="${name//[^A-Za-z0-9_]/}"
  prime_csrf "$jar"
  api POST /auth/register "$jar" "$(jq -nc --arg u "$name" --arg e "$name@accept.test" \
    --arg p 'Accept-Passw0rd!' --arg d "$tag" '{username:$u,email:$e,password:$p,displayName:$d,locale:"zh-CN"}')"
  if [[ "$HTTP_STATUS" != 200 && "$HTTP_STATUS" != 201 ]]; then
    warn "造账号 $name 失败（HTTP $HTTP_STATUS）：$(body_excerpt)"
    return 1
  fi
  login_into "$jar" "$name" 'Accept-Passw0rd!'
}

# actor_pool <N>：凑 N 个可登录的一次性账号。
# 先复用历史 @accept.test 账号（密码固定），不够再注册——注册端点有限流，别硬刚。
POOL_JARS=()
actor_pool() {
  local need=$1 name jar i=0
  POOL_JARS=()
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    jar="$work/pool$((i + 1)).jar"
    : >"$jar"
    if login_into "$jar" "$name" 'Accept-Passw0rd!'; then
      POOL_JARS+=("$jar")
      i=$((i + 1))
    fi
    [[ "$i" -ge "$need" ]] && break
  done < <(sqlv "SELECT username FROM users WHERE email LIKE '%@accept.test' AND status = 'active' ORDER BY id DESC LIMIT $need")
  while [[ "${#POOL_JARS[@]}" -lt "$need" ]]; do
    jar="$work/poolx$((i + 1)).jar"
    : >"$jar"
    spawn_account "$jar" "P$((i + 1))" || break
    POOL_JARS+=("$jar")
    i=$((i + 1))
    sleep 0.5
  done
}

if [[ "$SCHEMA_READY" == 1 && "$api_reachable" == 1 ]]; then
  if bootstrap_user "$JAR_A" SFORUM_ACCEPT_USER_A_LOGIN SFORUM_ACCEPT_USER_A_PASSWORD A; then
    name="$(prime_csrf "$JAR_A"; session_username)"
    [[ -z "$name" ]] && name="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.user.username // .username // empty)' 2>/dev/null)"
    UID_A="$(uid_of "$name" || true)"
    [[ -n "$UID_A" ]] && note "A = $name (id=$UID_A)" || warn "A 的 username 解析失败，统计断言会 pend"
  fi
  if bootstrap_user "$JAR_B" SFORUM_ACCEPT_USER_B_LOGIN SFORUM_ACCEPT_USER_B_PASSWORD B; then
    name="$(prime_csrf "$JAR_B"; session_username)"
    [[ -z "$name" ]] && name="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.user.username // .username // empty)' 2>/dev/null)"
    UID_B="$(uid_of "$name" || true)"
    [[ -n "$UID_B" ]] && note "B = $name (id=$UID_B)" || warn "B 的 username 解析失败"
  fi
  if [[ -n "${SFORUM_ACCEPT_ADMIN_LOGIN:-}" && -n "${SFORUM_ACCEPT_ADMIN_PASSWORD:-}" ]]; then
    if login_into "$JAR_ADMIN" "$SFORUM_ACCEPT_ADMIN_LOGIN" "$SFORUM_ACCEPT_ADMIN_PASSWORD"; then
      ADMIN_READY=1
      note "管理员登录成功（${SFORUM_ACCEPT_ADMIN_LOGIN}）"
    else
      warn "管理员登录失败：HTTP $HTTP_STATUS $(body_excerpt)"
    fi
  else
    note '未提供 SFORUM_ACCEPT_ADMIN_LOGIN/_PASSWORD：后台端点与边界段会 pend。'
  fi
  [[ -n "$UID_A" && -n "$UID_B" ]] && HTTP_READY=1

  if [[ -z "$CATEGORY_SLUG" ]]; then
    api GET /categories "$JAR_ANON"
    CATEGORY_SLUG="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | if type=="array" then .[0].slug else ((.items // .categories // [])[0].slug) end // empty' 2>/dev/null)"
  fi
  [[ -n "$CATEGORY_SLUG" ]] && note "发帖分类：$CATEGORY_SLUG" || warn '取不到分类 slug：发主题断言会 pend（可设 SFORUM_ACCEPT_CATEGORY_SLUG）。'
else
  note '结构未就绪或 API 不可达：HTTP 段整体 pend。'
fi

# ---------------------------------------------------------------- 动作原语

# action_ok <标签> <期望码,逗号分隔> <METHOD> <PATH> <JAR> [BODY]
# 4xx 视为环境前置不满足（pend），5xx 视为真故障（FAIL）。
action_ok() {
  local label=$1 want=$2
  shift 2
  api "$@"
  [[ ",$want," == *",$HTTP_STATUS,"* ]] && return 0
  if [[ "$HTTP_STATUS" == 000 || "$HTTP_STATUS" == 5* ]]; then
    fail "$label" "动作失败 HTTP $HTTP_STATUS（$(body_excerpt)）"
    return 1
  fi
  pend "$label" "动作未成功 HTTP $HTTP_STATUS（$(body_excerpt)）— 环境/权限前置不满足，计数无从判断"
  return 1
}

# ---------------------------------------------------------------- S1 统计正确性

step "S1 统计正确性（发主题 / 评论 / 点赞 / 收藏）"
[[ "$M1_READY" == 1 ]] || note "$(phase_reason m1)：以下断言记为 pend，但表结构与写入点仍是有效证据。"

TOPIC_ID=""
COMMENT_ID=""
LIKE_BASE_A=0

if [[ "$MUTATIONS" != 1 ]]; then
  pend 'S1 统计正确性整段' '只读模式不造主题/评论，统计增量无从判断'
elif [[ "$HTTP_READY" != 1 ]]; then
  pend 'S1 统计正确性整段' '缺 A/B 会话（API 不可达、注册关闭或缺凭证）'
elif [[ -z "$CATEGORY_SLUG" ]]; then
  pend 'S1 统计正确性整段' '取不到发帖分类（设 SFORUM_ACCEPT_CATEGORY_SLUG）'
else
  before="$(act "$UID_A" topic_count)"
  if action_ok 'S1-1 发主题' 200,201 POST /topics "$JAR_A" "$(topic_body "acceptance $RUN_TAG" 'badges acceptance harness topic')"; then
    TOPIC_ID="$(sqlv "SELECT id FROM topics WHERE author_user_id = $UID_A ORDER BY id DESC LIMIT 1")"
    after="$(act "$UID_A" topic_count)"
    report_delta 'S1-1 发主题 → 作者 topic_count' 'POST /topics (actor=A)' topic_count 1 "$((after - before))" "$M1_READY"
  fi

  if [[ -n "$TOPIC_ID" ]]; then
    before="$(act "$UID_A" comment_count)"
    if action_ok 'S1-2 发评论' 200,201 POST "/topics/$TOPIC_ID/comments" "$JAR_A" "$(comment_body 'acceptance comment')"; then
      COMMENT_ID="$(sqlv "SELECT id FROM comments WHERE topic_id = $TOPIC_ID ORDER BY id DESC LIMIT 1")"
      after="$(act "$UID_A" comment_count)"
      report_delta 'S1-2 发评论 → 作者 comment_count' 'POST /topics/{id}/comments (actor=A)' comment_count 1 "$((after - before))" "$M1_READY"
    fi

    LIKE_BASE_A="$(act "$UID_A" like_received_count)"
    b_before="$(act "$UID_B" like_given_count)"
    if action_ok 'S1-3 B 给 A 的主题点赞' 200,204 POST "/topics/$TOPIC_ID/like" "$JAR_B"; then
      a_after="$(act "$UID_A" like_received_count)"
      b_after="$(act "$UID_B" like_given_count)"
      report_delta 'S1-3a 点赞 → 目标作者 like_received_count' "POST /topics/$TOPIC_ID/like (actor=B)" like_received_count 1 "$((a_after - LIKE_BASE_A))" "$M1_READY"
      report_delta 'S1-3b 点赞 → 操作者 like_given_count' "POST /topics/$TOPIC_ID/like (actor=B)" like_given_count 1 "$((b_after - b_before))" "$M1_READY"
    fi

    b_before="$(act "$UID_B" bookmark_count)"
    if action_ok 'S1-4 B 收藏 A 的主题' 200,204 PUT "/topics/$TOPIC_ID/bookmark" "$JAR_B"; then
      b_after="$(act "$UID_B" bookmark_count)"
      report_delta 'S1-4 收藏 → 操作者 bookmark_count' "PUT /topics/$TOPIC_ID/bookmark (actor=B)" bookmark_count 1 "$((b_after - b_before))" "$M1_READY"
    fi

    # D3 自赞：topics.like_count 照常 +1，但自己的两个 like 计数都不许动。
    lr_before="$(act "$UID_A" like_received_count)"
    lg_before="$(act "$UID_A" like_given_count)"
    tl_before="$(sqlv "SELECT coalesce(like_count, 0) FROM topics WHERE id = $TOPIC_ID")"
    if action_ok 'S1-5 A 给自己的主题点赞（D3 自赞）' 200,204 POST "/topics/$TOPIC_ID/like" "$JAR_A"; then
      tl_after="$(sqlv "SELECT coalesce(like_count, 0) FROM topics WHERE id = $TOPIC_ID")"
      lr_after="$(act "$UID_A" like_received_count)"
      lg_after="$(act "$UID_A" like_given_count)"
      report_delta 'S1-5a 自赞 → topics.like_count 照常 +1' "POST /topics/$TOPIC_ID/like (actor=A 自赞)" like_count 1 "$((tl_after - tl_before))" "$M1_READY"
      report_delta 'S1-5b 自赞 → 自己的 like_received_count 不变' "POST /topics/$TOPIC_ID/like (actor=A 自赞)" like_received_count 0 "$((lr_after - lr_before))" "$M1_READY"
      report_delta 'S1-5c 自赞 → 自己的 like_given_count 不变' "POST /topics/$TOPIC_ID/like (actor=A 自赞)" like_given_count 0 "$((lg_after - lg_before))" "$M1_READY"
      action_ok 'S1-5d A 取消自赞' 200,204 DELETE "/topics/$TOPIC_ID/like" "$JAR_A" >/dev/null
    fi
  fi
fi

# ---------------------------------------------------------------- S4 回退

step "S4 取消点赞回退（且不为负）"
if [[ -z "$TOPIC_ID" ]]; then
  pend 'S4 取消点赞回退' 'S1 未造出主题'
else
  a_before="$(act "$UID_A" like_received_count)"
  if action_ok 'S4-1 B 取消点赞' 200,204 DELETE "/topics/$TOPIC_ID/like" "$JAR_B"; then
    a_after="$(act "$UID_A" like_received_count)"
    report_value 'S4-1 取消点赞 → like_received_count 回到原值' \
      "DELETE /topics/$TOPIC_ID/like (actor=B)" like_received_count "$LIKE_BASE_A" "$a_after" "$M1_READY"
    report_value 'S4-2 取消后不为负' 'SELECT like_received_count >= 0' 'not_negative' true \
      "$([[ "$a_after" -ge 0 ]] && printf true || printf false)" "$M1_READY"
    if [[ "$M1_READY" == 1 && "$a_after" != "$LIKE_BASE_A" ]]; then
      note "回退基线 $LIKE_BASE_A，实际 $a_after；差 $(($a_after - LIKE_BASE_A)) 说明取消路径没对称递减。"
    fi
  fi
fi

# ---------------------------------------------------------------- S3 访问去重

step "S3 访问天数去重（同一天多次请求只算 1 天）"
if [[ -z "$TOPIC_ID" ]]; then
  pend 'S3 访问去重' 'S1 未造出主题'
else
  visits_before="$(sqlv "SELECT count(*) FROM user_visits WHERE user_id = $UID_A")"
  days_before="$(act "$UID_A" days_visited)"
  for _ in 1 2 3 4 5; do api GET "/topics/$TOPIC_ID" "$JAR_A" >/dev/null; done
  today_rows="$(sqlv "SELECT count(*) FROM user_visits WHERE user_id = $UID_A AND visited_on = CURRENT_DATE")"
  visits_after="$(sqlv "SELECT count(*) FROM user_visits WHERE user_id = $UID_A")"
  days_after="$(act "$UID_A" days_visited)"
  printf '  [S3-1] 同一天 5 次请求\n    操作: GET /topics/%s × 5 (actor=A)\n    期望: user_visits 今日行数 = 1\n    实际: %s\n' "$TOPIC_ID" "${today_rows:-<空>}"
  report_value 'S3-1 同日多次请求只产生 1 行 user_visits' 'GET /topics/{id} × 5' 'today_rows' 1 "$today_rows" "$M1_READY"
  report_delta 'S3-2 同日重复访问 → days_visited 只 +1（本次应为 0 或 1）' '5 次详情请求' days_visited \
    "$((visits_after - visits_before))" "$((days_after - days_before))" "$M1_READY"
  report_value 'S3-3 days_visited 与 user_visits 行数一致（反规范化不漂移）' \
    'SELECT days_visited, count(user_visits)' 'days_visited' "$visits_after" "$days_after" "$M1_READY"

  # 清掉今天的 Redis 去重键再请求一次：SETNX 挡住的那条路径被绕过，仍必须靠
  # user_visits 的 (user_id, visited_on) 主键兜住——ON CONFLICT 是第二道防线。
  REDIS_CLI="${SFORUM_ACCEPT_REDIS_CMD:-docker exec ${SFORUM_ACCEPT_REDIS_CONTAINER:-sforum-redis} redis-cli}"
  if $REDIS_CLI ping >/dev/null 2>&1; then
    $REDIS_CLI --scan --pattern "badges:visit:$UID_A:*" 2>/dev/null \
      | xargs -r -n1 $REDIS_CLI DEL >/dev/null 2>&1
    api GET "/topics/$TOPIC_ID" "$JAR_A" >/dev/null
    today_rows2="$(sqlv "SELECT count(*) FROM user_visits WHERE user_id = $UID_A AND visited_on = CURRENT_DATE")"
    days_after2="$(act "$UID_A" days_visited)"
    report_value 'S3-4 清 Redis 去重键后再请求 → 仍只有 1 行 user_visits' \
      "DEL badges:visit:$UID_A:* 后 GET /topics/$TOPIC_ID" 'today_rows' 1 "$today_rows2" "$M1_READY"
    report_value 'S3-5 清键后 days_visited 不重复累加' \
      'SELECT days_visited' 'days_visited' "$days_after" "$days_after2" "$M1_READY"
  else
    pend 'S3-4 清 Redis 去重键后仍只 1 行' "连不上 Redis（$REDIS_CLI），无法验证 ON CONFLICT 兜底"
  fi
fi

# ---------------------------------------------------------------- S5 并发

step "S5 / D10 并发点赞（不同账号 × N，最终数值必须精确等于行数）"
CONCURRENCY="${SFORUM_ACCEPT_CONCURRENCY:-10}"
if [[ "$MUTATIONS" != 1 ]]; then
  pend 'S5 并发点赞' '只读模式不造点赞，并发最终值无从判断'
elif [[ -z "$TOPIC_ID" ]]; then
  pend 'S5 并发点赞' 'S1 未造出主题'
else
  # 清场：A/B 的赞都撤掉，只留并发这一批。
  api DELETE "/topics/$TOPIC_ID/like" "$JAR_B" >/dev/null
  api DELETE "/topics/$TOPIC_ID/like" "$JAR_A" >/dev/null
  react_sql="SELECT count(*) FROM content_reactions WHERE target_type = 'topic' AND target_id = $TOPIC_ID AND reaction = 'like'"
  react_before="$(sqlv "$react_sql")"
  lr_before="$(act "$UID_A" like_received_count)"
  actor_pool "$CONCURRENCY"
  spawned="${#POOL_JARS[@]}"
  spawned_jars=("${POOL_JARS[@]}")
  if [[ "$spawned" -lt 2 ]]; then
    pend 'S5 并发点赞' "只凑到 $spawned 个可登录账号（注册可能被限流或关闭），不足以验证并发"
  else
    ok_likes=0
    for i in "${!spawned_jars[@]}"; do
      (
        api POST "/topics/$TOPIC_ID/like" "${spawned_jars[$i]}" >/dev/null
        printf '%s' "$HTTP_STATUS" >"$work/like$i.status"
      ) &
    done
    wait
    for i in "${!spawned_jars[@]}"; do
      st="$(cat "$work/like$i.status" 2>/dev/null || true)"
      [[ "$st" == 200 || "$st" == 204 ]] && ok_likes=$((ok_likes + 1))
    done
    react_after="$(sqlv "$react_sql")"
    lr_after="$(act "$UID_A" like_received_count)"
    rows_delta=$((react_after - react_before))
    dup_users="$(sqlv "SELECT count(*) FROM (SELECT user_id FROM content_reactions WHERE target_type = 'topic' AND target_id = $TOPIC_ID AND reaction = 'like' GROUP BY user_id HAVING count(*) > 1) d")"
    printf '  [S5/D10]\n    操作: %s 个账号并发 POST /topics/%s/like（成功 %s 次）\n' "$spawned" "$TOPIC_ID" "$ok_likes"
    report_value 'S5-1 并发点赞 → content_reactions 新增行数 = 成功次数' \
      'SELECT count(*) FROM content_reactions' 'rows_delta' "$ok_likes" "$rows_delta" "$M1_READY"
    report_value 'S5-2 并发点赞 → 作者 like_received_count 精确等于新增行数' \
      'SELECT like_received_count' 'like_received_delta' "$rows_delta" "$((lr_after - lr_before))" "$M1_READY"
    report_value 'S5-3 每个用户至多 1 行 reaction（无重复行）' \
      'GROUP BY user_id HAVING count(*) > 1' 'duplicate_users' 0 "${dup_users:-0}" "$M1_READY"
    note "本轮并发造出 $spawned 个一次性账号，dev 库会留痕。"
  fi
fi

# ---------------------------------------------------------------- S2 授予幂等

step "S2 授予幂等（重复评估 10 次，user_badges 恰好 1 行）"
A_TOPICS=""
[[ -n "$UID_A" ]] && A_TOPICS="$(act "$UID_A" topic_count)"
if [[ -z "$UID_A" ]]; then
  pend 'S2 授予幂等' '缺 A 会话'
elif [[ "$M1_READY" != 1 || "${A_TOPICS:-0}" -lt 1 ]]; then
  pend 'S2 授予幂等' "依赖 M1：A 的 topic_count 仍是 ${A_TOPICS:-0}，评估无输入，幂等无从验证"
elif [[ "$ADMIN_READY" != 1 ]]; then
  pend 'S2 授予幂等' '缺管理员会话（评估触发走 POST /admin/badges/recompute）'
else
  badge_id="$(sqlv "SELECT id FROM badges WHERE slug = 'first-topic'")"
  if [[ -z "$badge_id" ]]; then
    pend 'S2 授予幂等' '找不到 first-topic 内置徽章'
  else
    last_recompute=000
    for _ in $(seq 1 10); do
      api POST /admin/badges/recompute "$JAR_ADMIN" '{}' >/dev/null
      last_recompute="$HTTP_STATUS"
    done
    case "$last_recompute" in
      200|202|204) note 'recompute 端点 10 次触发成功。' ;;
      404) note 'recompute 端点未落地（404）：徽章行若已存在，来自写路径触发评估。' ;;
      429) note 'recompute 被限流（429）：10 次触发未全部生效。' ;;
      *) note "recompute 最后一次返回 HTTP $last_recompute。" ;;
    esac
    rows="$(sqlv "SELECT count(*) FROM user_badges WHERE user_id = $UID_A AND badge_id = $badge_id")"
    report_value 'S2-1 10 次评估 → user_badges 恰好 1 行' \
      'POST /admin/badges/recompute × 10' 'rows' 1 "$rows" "$M2_READY"
    report_value 'S2-2 授予后 users.top_badge_id 指向该徽章' \
      'SELECT top_badge_id FROM users WHERE id=A' 'top_badge_id' "$badge_id" \
      "$(sqlv "SELECT coalesce(top_badge_id, -1) FROM users WHERE id = $UID_A")" "$M2_READY"
  fi
fi

# ---------------------------------------------------------------- S6 边界与失败模式

step "S6 边界（停用不展示 / 删除置空 / 自动徽章不可手动收回）"
BOUNDARY_READY=0
[[ "$ADMIN_READY" == 1 && "$M2_READY" == 1 ]] && BOUNDARY_READY=1
if [[ "$MUTATIONS" != 1 ]]; then
  pend 'S6 边界整段' '只读模式不造徽章、不翻开关'
elif [[ -z "$UID_A" || "$ADMIN_READY" != 1 ]]; then
  pend 'S6 边界整段' '缺管理员或 A 会话'
else
  tmp_slug="accept-${RUN_TAG}"
  tmp_badge=""
  # 自建一个 manual/gold 徽章，避免污染内置数据；position=0 + 最新授予 → 应当成为 top badge。
  if action_ok 'S6-0 新建临时徽章' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$tmp_slug" '{slug:$s,name:"Acceptance Temp",description:"verify-badges-acceptance",tier:"gold",icon:"i-lucide-award",grantMode:"manual",position:0}')"; then
    tmp_badge="$(sqlv "SELECT id FROM badges WHERE slug = '$tmp_slug'")"
  else
    note '新建徽章未成功：多半是后台载荷契约尚未冻结（以 contracts/openapi/paths/badges.yaml 为准），后续边界断言 pend。'
  fi

  if [[ -n "$tmp_badge" ]]; then
    if action_ok 'S6-1 手动授予 A' 200,201 POST "/admin/badges/$tmp_badge/grants" "$JAR_ADMIN" \
      "$(jq -nc --argjson u "$UID_A" '{userId:$u,reason:"acceptance"}')"; then
      report_value 'S6-1 授予后 top_badge_id 指向临时徽章' \
        'POST /admin/badges/{id}/grants' 'top_badge_id' "$tmp_badge" \
        "$(sqlv "SELECT coalesce(top_badge_id, -1) FROM users WHERE id = $UID_A")" "$BOUNDARY_READY"

      if action_ok 'S6-2 停用临时徽章' 200,204 PATCH "/admin/badges/$tmp_badge" "$JAR_ADMIN" '{"enabled":false}'; then
        top_after_disable="$(sqlv "SELECT coalesce(top_badge_id, -1) FROM users WHERE id = $UID_A")"
        if [[ "$top_after_disable" != "$tmp_badge" ]]; then
          pass "S6-2c 停用触发重算：top_badge_id 不再指向它（回落到 $top_after_disable）"
        else
          fail 'S6-2c 停用触发重算' "停用后 top_badge_id 仍指向已停用徽章 $tmp_badge"
        fi
        report_value 'S6-2a 停用后稳定视图不再输出该授予行' \
          "SELECT count(*) FROM sforum_core_v1.forum_user_badges WHERE user_id=$UID_A AND badge_id=$tmp_badge" \
          'view_rows' 0 \
          "$(sqlv "SELECT count(*) FROM sforum_core_v1.forum_user_badges WHERE user_id = $UID_A AND badge_id = $tmp_badge")" "$BOUNDARY_READY"
        report_value 'S6-2b 停用后公开徽章端点不再返回它' \
          "GET /profiles/{A}/badges" 'contains_temp_badge' false \
          "$(api GET "/profiles/$(sqlv "SELECT username FROM users WHERE id = $UID_A")/badges" "$JAR_ANON" >/dev/null; \
             if [[ "$HTTP_STATUS" != 200 ]]; then printf 'unavailable(%s)' "$HTTP_STATUS"; \
             elif printf '%s' "$HTTP_BODY" | grep -q "\"$tmp_slug\""; then printf true; else printf false; fi)" \
          "$BOUNDARY_READY" '公开徽章端点未落地'
      fi

      if action_ok 'S6-3 删除临时徽章' 200,204 DELETE "/admin/badges/$tmp_badge" "$JAR_ADMIN"; then
        # 停用那步已经把 top_badge_id 重算到回落值，所以删除时不该再期待 NULL；
        # 唯一能断言的是「不再指向被删的徽章」（没有其它徽章时自然就是 NULL/-1）。
        top_after_delete="$(sqlv "SELECT coalesce(top_badge_id, -1) FROM users WHERE id = $UID_A")"
        if [[ "$top_after_delete" != "$tmp_badge" ]]; then
          pass "S6-3a 删除后 top_badge_id 不再指向它（当前 $top_after_delete）"
        else
          fail 'S6-3a 删除后 top_badge_id 仍指向已删徽章' "top_badge_id=$top_after_delete，badge=$tmp_badge"
        fi
        report_value 'S6-3b 删除徽章 → user_badges 级联清空' \
          'DELETE /admin/badges/{id}' 'grant_rows' 0 \
          "$(sqlv "SELECT count(*) FROM user_badges WHERE badge_id = $tmp_badge")" "$BOUNDARY_READY"
      fi
    fi
  fi

  # 自动徽章手动收回必须 422 forum.badge_manual_only。
  auto_badge="$(sqlv "SELECT id FROM badges WHERE grant_mode = 'automatic' ORDER BY id LIMIT 1")"
  if [[ -n "$auto_badge" && -n "$UID_A" ]]; then
    api DELETE "/admin/badges/$auto_badge/grants/$UID_A" "$JAR_ADMIN"
    if [[ "$HTTP_STATUS" == 422 ]]; then
      if printf '%s' "$HTTP_BODY" | grep -q 'forum.badge_manual_only'; then
        pass 'S6-4 自动徽章手动收回被拒（422 forum.badge_manual_only）'
      else
        fail 'S6-4 自动徽章手动收回被拒' "HTTP 422 但错误码不是 forum.badge_manual_only：$(body_excerpt)"
      fi
    elif [[ "$HTTP_STATUS" == 404 ]]; then
      pend 'S6-4 自动徽章手动收回被拒' '路由未落地（404）'
    elif [[ "$HTTP_STATUS" == 429 ]]; then
      pend 'S6-4 自动徽章手动收回被拒' '被限流（429），不是产品行为'
    else
      report_value 'S6-4 自动徽章手动收回被拒' "DELETE /admin/badges/$auto_badge/grants/$UID_A" 'http_status' 422 "$HTTP_STATUS" "$BOUNDARY_READY"
    fi
  fi

  # features.badges 关闭：两个半句都要验——徽章面关掉，统计面照常。
  if [[ "$ADMIN_READY" != 1 ]]; then
    pend 'S6-5 features.badges 关闭语义' '缺管理员会话（开关走 PUT /admin/features）'
  else
    prof_user="$(sqlv "SELECT username FROM users WHERE id = $UID_A")"
    api PUT /admin/features "$JAR_ADMIN" '{"options":[{"name":"features.badges","value":"false"}]}'
    if [[ "$HTTP_STATUS" != 200 ]]; then
      pend 'S6-5 features.badges 关闭语义' "关闭开关失败 HTTP $HTTP_STATUS（$(body_excerpt)）"
    else
      api GET "/profiles/$prof_user/badges" "$JAR_ANON"
      if [[ "$HTTP_STATUS" == 403 ]] && printf '%s' "$HTTP_BODY" | grep -q 'forum.badges_disabled'; then
        pass 'S6-5a 关闭后公开徽章端点 403 forum.badges_disabled'
      elif [[ "$HTTP_STATUS" == 404 ]]; then
        pend 'S6-5a 关闭后公开徽章端点 403' '路由未落地（404）'
      else
        fail 'S6-5a 关闭后公开徽章端点 403' "期望 403+badges_disabled，实际 HTTP $HTTP_STATUS（$(body_excerpt)）"
      fi
      # 统计面：开关只管徽章产品面，资料页的统计读取路径必须照常。
      api GET "/profiles/$prof_user" "$JAR_ANON"
      if [[ "$HTTP_STATUS" == 200 ]] && printf '%s' "$HTTP_BODY" | grep -q '"topicCount"'; then
        pass 'S6-5b 关闭后资料页统计照常（HTTP 200 + topicCount 在）'
      else
        fail 'S6-5b 关闭后资料页统计照常' "期望 200+topicCount，实际 HTTP $HTTP_STATUS（$(body_excerpt)）"
      fi
      report_value 'S6-5c 关闭后 user_activity 仍在写/在库（统计不属于徽章产品面）' \
        'SELECT count(*) FROM sforum_core_v1.forum_user_activity WHERE user_id=A' 'view_rows' 1 \
        "$(sqlv "SELECT count(*) FROM sforum_core_v1.forum_user_activity WHERE user_id = $UID_A")" "$M1_READY"
      api PUT /admin/features "$JAR_ADMIN" '{"options":[{"name":"features.badges","value":"true"}]}'
      restore_status="$HTTP_STATUS"
      if [[ "$restore_status" != 200 ]]; then
        sleep 3
        api PUT /admin/features "$JAR_ADMIN" '{"options":[{"name":"features.badges","value":"true"}]}'
        restore_status="$HTTP_STATUS"
      fi
      # 恢复成功还要确认端点真的不再 403，否则「开关写成功但读路径没生效」会被后面误判。
      if [[ "$restore_status" == 200 ]]; then
        api GET "/profiles/$prof_user/badges" "$JAR_ANON"
        [[ "$HTTP_STATUS" == 403 ]] && restore_status="stale-403"
      fi
      if [[ "$restore_status" == 200 ]]; then
        note '开关已恢复为开启。'
      else
        BADGES_FLAG_STUCK=1
        fail 'S6-5d 恢复 features.badges' "HTTP $restore_status（$(body_excerpt)）—— 现场需要手工恢复"
        cat >&2 <<MANUAL_FIX

  === 手工恢复：把 features.badges 打开（整段可粘贴执行）===
  J=/tmp/sforum-fix.jar; : >"\$J"
  curl -s -c "\$J" '$API_URL/auth/session' -o /dev/null
  T=\$(awk '\$6=="csrf_"{v=\$7} END{print v}' "\$J")
  curl -s -b "\$J" -c "\$J" -X POST '$API_URL/auth/login' \
    -H 'Content-Type: application/json' -H "X-CSRF-Token: \$T" -H 'Origin: $CSRF_ORIGIN' \
    -d '{"login":"${SFORUM_ACCEPT_ADMIN_LOGIN:-<戊邮箱>}","password":"${SFORUM_ACCEPT_ADMIN_PASSWORD:-<密码>}"}' -o /dev/null
  T=\$(awk '\$6=="csrf_"{v=\$7} END{print v}' "\$J")
  curl -s -b "\$J" -X PUT '$API_URL/admin/features' \
    -H 'Content-Type: application/json' -H "X-CSRF-Token: \$T" -H 'Origin: $CSRF_ORIGIN' \
    -d '{"options":[{"name":"features.badges","value":"true"}]}' -w '\\nHTTP=%{http_code}\\n'
  ================================================================

MANUAL_FIX
      fi
    fi
  fi
fi

# ---------------------------------------------------------------- S6-6 PATCH 部分更新

step "S6-6 PATCH 部分更新语义（只传要改的字段，其余必须原样）"
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S6-6 部分更新语义' 'features.badges 未恢复，后台写端点一律 403'
elif [[ "$MUTATIONS" != 1 ]]; then
  pend 'S6-6 部分更新语义' '只读模式不建临时徽章'
elif [[ "$ADMIN_READY" != 1 || "$M3_READY" != 1 ]]; then
  pend 'S6-6 部分更新语义' '缺管理员会话或 M3 未落地'
else
  slug_m="accept-patch-m-${RUN_TAG}"
  id_m=""
  if action_ok 'S6-6a 建一个 manual 临时徽章' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$slug_m" '{slug:$s,name:"Patch Probe Manual",tier:"bronze",icon:"i-lucide-award",grantMode:"manual"}')"; then
    id_m="$(sqlv "SELECT id FROM badges WHERE slug = '$slug_m'")"
  fi

  if [[ -n "$id_m" ]]; then
    # 1) 只改 enabled：其余字段必须一个都不动（这就是 PATCH 语义）
    if action_ok 'S6-6b PATCH 只传 enabled' 200,204 PATCH "/admin/badges/$id_m" "$JAR_ADMIN" '{"enabled":false}'; then
      row="$(sqlv "SELECT enabled::text || '|' || name || '|' || grant_mode || '|' || tier FROM badges WHERE id = $id_m")"
      report_value 'S6-6b 只改 enabled，name/grantMode/tier 原样' \
        "PATCH /admin/badges/$id_m {\"enabled\":false}" 'enabled|name|grant_mode|tier' \
        "false|Patch Probe Manual|manual|bronze" "$row" "$M3_READY"
    fi
    # 2) 只改 name：enabled 不能被「隐式恢复」成 true
    if action_ok 'S6-6c PATCH 只传 name' 200,204 PATCH "/admin/badges/$id_m" "$JAR_ADMIN" '{"name":"Patch Probe Renamed"}'; then
      row="$(sqlv "SELECT enabled::text || '|' || name FROM badges WHERE id = $id_m")"
      report_value 'S6-6c 只改 name，enabled 保持 false' \
        "PATCH /admin/badges/$id_m {\"name\":...}" 'enabled|name' \
        'false|Patch Probe Renamed' "$row" "$M3_READY"
    fi
    action_ok 'S6-6d 清理 manual 临时徽章' 200,204 DELETE "/admin/badges/$id_m" "$JAR_ADMIN" >/dev/null
  fi

  # 3) 自动徽章改成手动：metric/threshold 必须被清成 NULL，不能留成半自动脏数据
  slug_a="accept-patch-a-${RUN_TAG}"
  id_a=""
  if action_ok 'S6-6e 建一个 automatic 临时徽章' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$slug_a" '{slug:$s,name:"Patch Probe Auto",tier:"silver",icon:"i-lucide-award",grantMode:"automatic",metric:"topic_count",threshold:5}')"; then
    id_a="$(sqlv "SELECT id FROM badges WHERE slug = '$slug_a'")"
  fi
  if [[ -n "$id_a" ]]; then
    if action_ok 'S6-6f PATCH 自动改手动' 200,204 PATCH "/admin/badges/$id_a" "$JAR_ADMIN" '{"grantMode":"manual"}'; then
      row="$(sqlv "SELECT grant_mode || '|' || coalesce(metric, 'NULL') || '|' || coalesce(threshold::text, 'NULL') FROM badges WHERE id = $id_a")"
      report_value 'S6-6f 自动改手动后 metric/threshold 变 NULL' \
        "PATCH /admin/badges/$id_a {\"grantMode\":\"manual\"}" 'grant_mode|metric|threshold' \
        'manual|NULL|NULL' "$row" "$M3_READY"
    fi
    action_ok 'S6-6g 清理 automatic 临时徽章' 200,204 DELETE "/admin/badges/$id_a" "$JAR_ADMIN" >/dev/null
  fi
fi

# ---------------------------------------------------------------- S7 服务端权限

step "S7 服务端权限（无 badge.manage 直接 curl，必须服务端拒绝）"
any_badge="$(sqlv "SELECT id FROM badges ORDER BY id LIMIT 1")"
a_manage_rows=""
a_super_rows=""
if [[ -n "$UID_A" ]]; then
  a_manage_rows="$(sqlv "SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id JOIN role_permissions rp ON rp.role_id = r.id WHERE ur.user_id = $UID_A AND rp.permission_key = 'badge.manage'")"
  a_super_rows="$(sqlv "SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $UID_A AND r.key = 'super_admin'")"
fi
if [[ -z "$UID_A" ]]; then
  pend 'S7 权限整段' '缺 A 会话（需要一个无 badge.manage 的普通账号）'
elif [[ "${a_manage_rows:-0}" != 0 || "${a_super_rows:-0}" != 0 ]]; then
  pend 'S7 权限整段' "A（$UID_A）实际持有 badge.manage 或 super_admin 角色，403 断言不成立——换个纯 member 账号"
elif [[ "$M3_READY" != 1 ]]; then
  pend 'S7 权限整段' "$(phase_reason m3)"
else
  expect_status 'S7-1 普通用户读后台徽章列表被拒' 403 GET /admin/badges "$JAR_A" '' "$M3_READY"
  expect_status 'S7-2 普通用户改徽章被拒' 403 PATCH "/admin/badges/${any_badge:-1}" "$JAR_A" '{"enabled":false}' "$M3_READY"
  expect_status 'S7-3 普通用户触发全量重算被拒' 403 POST /admin/badges/recompute "$JAR_A" '{}' "$M3_READY"
  expect_status 'S7-4 普通用户手动授予被拒' 403 POST "/admin/badges/${any_badge:-1}/grants" "$JAR_A" '{"userId":1,"reason":"x"}' "$M3_READY"
  api GET /admin/badges "$JAR_ANON"
  if [[ "$HTTP_STATUS" == 401 || "$HTTP_STATUS" == 403 ]]; then
    pass "S7-5 匿名访问后台徽章被拒（HTTP $HTTP_STATUS）"
  else
    fail 'S7-5 匿名访问后台徽章被拒' "期望 401/403，实际 $HTTP_STATUS"
  fi
  if [[ "$ADMIN_READY" == 1 ]]; then
    expect_status 'S7-6 有 badge.manage 的管理员可读列表（正向对照）' 200 GET /admin/badges "$JAR_ADMIN" '' 1
  else
    pend 'S7-6 正向对照（管理员可读）' '未提供管理员会话'
  fi
fi

# ---------------------------------------------------------------- S7b 只读账号

step "S7b 只持 badge.view 的账号（丁）：能读不能写，且 reason 不是 badges_disabled"
VIEWER_READY=0
VIEWER_JAR="$work/viewer.jar"
: >"$VIEWER_JAR"
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S7b 只读账号权限' 'features.badges 未恢复（S6-5d 失败），徽章端点一律 403，权限断言不可信'
elif [[ "$M3_READY" != 1 ]]; then
  pend 'S7b 只读账号权限' "$(phase_reason m3)"
else
  # 丁 必须是独立账号：一个账号不能同时承担「零基线（丙）」和「只读管理员（丁）」两个互斥角色。
  # 默认只读凭证，不做自动配夹具——自动挂角色会污染别人正在用的用例账号。
  if [[ -z "${SFORUM_ACCEPT_VIEWER_LOGIN:-}" ]]; then
    _viewer="$(creds_email 丁 || true)"
    _vpass="$(creds_password)"
    if [[ -n "$_viewer" && -n "$_vpass" ]]; then
      SFORUM_ACCEPT_VIEWER_LOGIN="$_viewer"
      SFORUM_ACCEPT_VIEWER_PASSWORD="$_vpass"
      note "丁 取自 creds.txt：$_viewer"
    fi
  fi
  if [[ -n "${SFORUM_ACCEPT_VIEWER_LOGIN:-}" && -n "${SFORUM_ACCEPT_VIEWER_PASSWORD:-}" ]]; then
    if login_into "$VIEWER_JAR" "$SFORUM_ACCEPT_VIEWER_LOGIN" "$SFORUM_ACCEPT_VIEWER_PASSWORD"; then
      VIEWER_READY=1
    else
      pend 'S7b 只读账号权限' "丁 登录失败 HTTP $HTTP_STATUS"
    fi
  elif [[ "${SFORUM_ACCEPT_PROVISION_VIEWER:-0}" == 1 && "$ADMIN_READY" == 1 ]]; then
    # 显式 opt-in 才会配夹具，而且只用一个一次性账号，绝不动丙。
    api POST /roles "$JAR_ADMIN" '{"key":"qa_badge_viewer","alias":"QA Badge Viewer","description":"badge.view only"}'
    api PUT /roles/qa_badge_viewer/permissions "$JAR_ADMIN" '{"permissions":["badge.view"]}'
    prov_jar="$work/prov.jar"
    : >"$prov_jar"
    if spawn_account "$prov_jar" V && [[ -n "$(prime_csrf "$prov_jar"; session_username)" ]]; then
      prov_name="$(prime_csrf "$prov_jar"; session_username)"
      prov_uid="$(uid_of "$prov_name" || true)"
      if [[ -n "$prov_uid" ]]; then
        api PUT "/users/$prov_uid/roles" "$JAR_ADMIN" '{"roleKeys":["member","qa_badge_viewer"]}'
        # 角色变了要重新登录，权限才进 actor。
        if login_into "$VIEWER_JAR" "$prov_name" 'Accept-Passw0rd!'; then
          VIEWER_READY=1
          note "丁 = 临时一次性账号 $prov_name（显式 opt-in 配置，未触碰丙）"
        fi
      fi
    fi
  else
    pend 'S7b 只读账号权限' '没有丁的凭证（设 SFORUM_ACCEPT_VIEWER_LOGIN/_PASSWORD，或 SFORUM_ACCEPT_PROVISION_VIEWER=1 显式配夹具）'
  fi
fi

if [[ "$VIEWER_READY" == 1 ]]; then
  # 丁 的权限纯度：必须持 badge.view、且**不持** badge.manage，否则「能读不能写」验的是别的角色。
  v_name="$(prime_csrf "$VIEWER_JAR"; session_username)"
  v_uid="$(uid_of "$v_name" || true)"
  if [[ -n "$v_uid" ]]; then
    v_roles="$(sqlv "SELECT coalesce(string_agg(r.key, ','), '') FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $v_uid")"
    v_manage="$(sqlv "SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id JOIN role_permissions rp ON rp.role_id = r.id WHERE ur.user_id = $v_uid AND rp.permission_key = 'badge.manage'")"
    v_view="$(sqlv "SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id JOIN role_permissions rp ON rp.role_id = r.id WHERE ur.user_id = $v_uid AND rp.permission_key = 'badge.view'")"
    note "丁 = $v_name（角色：${v_roles:-none}；badge.view 行数 ${v_view:-0}，badge.manage 行数 ${v_manage:-0}）"
    if [[ "${v_manage:-0}" != 0 ]]; then
      VIEWER_READY=0
      pend 'S7b 只读账号权限' "丁 实际持有 badge.manage（角色：$v_roles），只读用例不可信——先把它收成只含 badge.view"
    elif [[ "${v_view:-0}" == 0 ]]; then
      VIEWER_READY=0
      pend 'S7b 只读账号权限' "丁 没有 badge.view（角色：$v_roles），读断言会失败且不说明问题"
    fi
  fi
fi

if [[ "$VIEWER_READY" == 1 ]]; then
  expect_status 'S7b-1 丁 能读后台徽章列表（badge.view 生效）' 200 GET /admin/badges "$VIEWER_JAR" '' 1
  for probe in "PATCH|/admin/badges/${any_badge:-1}|{\"enabled\":false}" \
    'POST|/admin/badges|{"slug":"probe-viewer","name":"x","tier":"bronze","grantMode":"manual"}' \
    'POST|/admin/badges/recompute|{}'; do
    IFS='|' read -r m p b <<<"$probe"
    api "$m" "$p" "$VIEWER_JAR" "$b"
    if [[ "$HTTP_STATUS" == 403 ]] && ! printf '%s' "$HTTP_BODY" | grep -q 'badges_disabled'; then
      pass "S7b-2 $m $p 被拒且 reason 不是 badges_disabled"
    elif [[ "$HTTP_STATUS" == 404 ]]; then
      pend "S7b-2 $m $p" '路由未落地（404）'
    else
      fail "S7b-2 $m $p 被拒" "期望 403+权限拒绝，实际 HTTP $HTTP_STATUS（$(body_excerpt)）"
    fi
  done
fi

# ---------------------------------------------------------------- S7c 公开面字段

step "S7c 公开面不泄露运营字段（DTO 转换最易漏）"
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S7c 公开面字段' 'features.badges 未恢复（S6-5d 失败），公开端点 403，字段泄露无从判断'
elif [[ "$M3_READY" != 1 ]]; then
  pend 'S7c 公开面字段' "$(phase_reason m3)"
elif [[ -z "$UID_A" ]]; then
  pend 'S7c 公开面字段' '缺 A 会话'
else
  pu="$(sqlv "SELECT username FROM users WHERE id = $UID_A")"
  api GET "/profiles/$pu/badges" "$JAR_ANON"
  if [[ "$HTTP_STATUS" == 404 ]]; then
    pend 'S7c 公开面字段' '路由未落地（404）'
  elif [[ "$HTTP_STATUS" != 200 ]]; then
    fail 'S7c 公开面字段' "HTTP $HTTP_STATUS（$(body_excerpt)）"
  else
    leak="$(printf '%s' "$HTTP_BODY" | grep -oE '"(grantedCount|enabled|grantedBy|grantReason)"' | sort -u | tr '\n' ' ')"
    if [[ -z "$leak" ]]; then
      pass 'S7c 公开徽章响应不含 grantedCount/enabled/grantedBy/grantReason'
    else
      fail 'S7c 公开面泄露运营字段' "出现：$leak"
    fi
  fi
fi

# ---------------------------------------------------------------- S8 缓存延迟

step "S8 缓存语义（两个时限不同：资料页 ≤${SFORUM_ACCEPT_PROFILE_WAIT:-10}s，帖子旁 ≤${SFORUM_ACCEPT_TOPIC_WAIT:-35}s）"
PROFILE_WAIT="${SFORUM_ACCEPT_PROFILE_WAIT:-10}"
TOPIC_WAIT="${SFORUM_ACCEPT_TOPIC_WAIT:-35}"

# 资料页不走 Forum 的 topicDetail/comments 缓存，授予后应当很快可见；
# 帖子旁的 chip 走 CachedStore（topicDetail 30s / comments 20s），慢是正常的。
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S8-1 资料页徽章可见时限' 'features.badges 未恢复（S6-5d 失败），资料页徽章端点 403'
elif [[ "$M3_READY" != 1 || -z "$UID_A" ]]; then
  pend 'S8-1 资料页徽章可见时限' "$(phase_reason m3)"
else
  pu="$(sqlv "SELECT username FROM users WHERE id = $UID_A")"
  found=0
  profile_status=000
  deadline=$((SECONDS + PROFILE_WAIT))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    api GET "/profiles/$pu/badges" "$JAR_ANON"
    profile_status="$HTTP_STATUS"
    [[ "$profile_status" == 404 ]] && break
    if [[ "$profile_status" == 200 ]] && printf '%s' "$HTTP_BODY" | grep -q 'first-topic'; then
      found=1
      break
    fi
    sleep 2
  done
  if [[ "$profile_status" == 404 ]]; then
    pend 'S8-1 资料页徽章可见时限' '路由未落地（404）'
  else
    report_value "S8-1 资料页 ${PROFILE_WAIT}s 内出现已授予徽章（不走 Forum 缓存）" \
      "轮询 GET /profiles/$pu/badges" 'found_first_topic' true \
      "$([[ "$found" == 1 ]] && printf true || printf false)" "$M3_READY"
  fi
fi

if [[ -z "$TOPIC_ID" ]]; then
  note '无主题可查，帖子旁缓存段跳过。'
else
  api GET "/topics/$TOPIC_ID" "$JAR_ANON"
  if [[ "$HTTP_STATUS" != 200 ]]; then
    note "详情请求 HTTP $HTTP_STATUS，跳过。"
  elif printf '%s' "$HTTP_BODY" | grep -q '"topBadge"'; then
    note '详情响应已带 topBadge 字段（缓存命中或已刷新）。'
  else
    note '详情响应暂无 topBadge：CachedStore TTL 为 topicDetail 30s / comments 20s，这段窗口内看不到属预期，不判 FAIL。'
  fi
  if [[ "${SFORUM_ACCEPT_CACHE_WAIT:-0}" -gt 0 ]]; then
    note "按 SFORUM_ACCEPT_CACHE_WAIT=${SFORUM_ACCEPT_CACHE_WAIT} 等待后复查（帖子旁窗口，独立于资料页）…"
    sleep "$SFORUM_ACCEPT_CACHE_WAIT"
    api GET "/topics/$TOPIC_ID" "$JAR_ANON"
    if printf '%s' "$HTTP_BODY" | grep -q '"topBadge"'; then
      pass "S8-2 等待 ${SFORUM_ACCEPT_CACHE_WAIT}s 后帖子旁出现 topBadge"
    else
      fail "S8-2 等待 ${SFORUM_ACCEPT_CACHE_WAIT}s 后帖子旁仍无 topBadge" '超过缓存 TTL 仍未装饰，属实现缺陷'
    fi
  fi
fi

# ---------------------------------------------------------------- S10 公开读路径

step "S10 公开读路径（topBadge 装饰、NULL 路径、列表不 500）"
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S10 读路径' 'features.badges 未恢复（S6-5d 失败），读路径断言不可信'
elif [[ "$api_reachable" != 1 ]]; then
  pend 'S10 读路径' 'API 不可达'
else
  # 列表/搜索路径是 scan 列数漂移的第一现场：多一份 SELECT 拷贝就多一处 38 vs 42。
  # 新增任何读路径字段后，S10-1a / 1b / 1c 三条都必须重跑——单测抓不住跨文件漂移。
  api GET '/topics' "$JAR_ANON"
  if [[ "$HTTP_STATUS" == 000 ]]; then
    pend 'S10-1a 主题列表（匿名）200' 'API 连接失败（dev 重建窗口），本条不可判'
  else
    expect_status 'S10-1a 主题列表（匿名）200（scan 列数 = SELECT 列数）' 200 GET '/topics' "$JAR_ANON" '' 1
  fi

  api GET '/topics?limit=5&page=1&category=general' "$JAR_ANON"
  if [[ "$HTTP_STATUS" == 000 ]]; then
    pend 'S10-1b 主题列表（limit+page+category）200' 'API 连接失败，本条不可判'
  else
    expect_status 'S10-1b 主题列表（limit+page+category）200' 200 GET '/topics?limit=5&page=1&category=general' "$JAR_ANON" '' 1
  fi

  # search_live.go 用的是同一份 SELECT 拷贝（已抽成 topicSummaryColumns，但仍是独立查询），
  # 命中 0 条时的 200 会 early return、根本走不到 hydrate，所以「有真实命中」是这条的前提。
  # 参数名是 query，不是 q：传 q 会被忽略并静默返回 0 命中——这正是这个 bug 藏很久的原因之一。
  search_candidates=()
  [[ -n "${SFORUM_ACCEPT_SEARCH_TERM:-}" ]] && search_candidates+=("$SFORUM_ACCEPT_SEARCH_TERM")
  for cand in acceptance 测试 主题 sforum; do
    [[ -n "$cand" ]] && search_candidates+=("$cand")
  done
  search_term=""
  search_hits=0
  for cand in "${search_candidates[@]}"; do
    search_q="$(jq -rn --arg s "$cand" '$s | @uri')"
    api GET "/search?query=$search_q&perPage=5" "$JAR_ANON"
    if [[ "$HTTP_STATUS" == 000 ]]; then
      pend 'S10-1c 搜索 200 且有真实命中' 'API 连接失败，本条不可判'
      search_term=""
      break
    fi
    if [[ "$HTTP_STATUS" != 200 ]]; then
      fail 'S10-1c 搜索 200 且有真实命中' "词「$cand」HTTP $HTTP_STATUS（$(body_excerpt)）— search_live 的 SELECT 拷贝可能同样列数漂移"
      search_term=""
      break
    fi
    search_hits="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.items // []) | length' 2>/dev/null)"
    if [[ "${search_hits:-0}" -gt 0 ]]; then
      search_term="$cand"
      break
    fi
  done
  if [[ -n "$search_term" ]]; then
    pass "S10-1c 搜索 200 且有真实命中（query=$search_term，命中 $search_hits）"
    # 同一份扫描器：搜索结果里的徽章装饰也必须正确，且不能是空对象。
    search_empty_obj="$(printf '%s' "$HTTP_BODY" | jq -r '[(.data // .) | (.items // []) | .[] | select(has("topBadge")) | select((.topBadge|type)=="object") | select((.topBadge.slug // "")=="")] | length' 2>/dev/null)"
    report_value 'S10-1d 搜索结果里没有「空对象徽章」' \
      "GET /search?query=$search_term" 'empty_badge_objects' 0 "${search_empty_obj:-0}" "$M2_READY"
  elif [[ "$search_term" == "" && "$HTTP_STATUS" != 000 ]]; then
    pend 'S10-1c 搜索 200 且有真实命中' '候选词全部 0 命中，无法作为列漂移探针（用 SFORUM_ACCEPT_SEARCH_TERM 指定有结果的词）'
  fi

  badged_topic="$(sqlv "SELECT t.id FROM topics t JOIN users u ON u.id = t.author_user_id WHERE u.top_badge_id IS NOT NULL AND t.status = 'active' AND t.deleted_at IS NULL ORDER BY t.id DESC LIMIT 1")"
  badged_uid="$(sqlv "SELECT author_user_id FROM topics WHERE id = ${badged_topic:-0}")"
  if [[ -z "$badged_topic" ]]; then
    pend 'S10-2 有徽章作者的详情带 topBadge' '库里没有「有徽章作者 + 存活主题」的组合'
  else
    api GET "/topics/$badged_topic" "$JAR_ANON"
    got_slug="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .topBadge.slug // empty' 2>/dev/null)"
    if [[ "$HTTP_STATUS" == 000 ]]; then
      pend 'S10-2 有徽章作者的详情带 topBadge.slug' 'API 连接失败，本条不可判'
    else
      report_value 'S10-2 有徽章作者的详情带 topBadge.slug' \
        "GET /topics/$badged_topic" 'topBadge.slug' non-empty \
        "$([[ -n "$got_slug" ]] && printf non-empty || printf empty)" "$M2_READY"
    fi
  fi

  plain_topic="$(sqlv "SELECT t.id FROM topics t JOIN users u ON u.id = t.author_user_id WHERE u.top_badge_id IS NULL AND t.status = 'active' AND t.deleted_at IS NULL ORDER BY t.id DESC LIMIT 1")"
  if [[ -z "$plain_topic" ]]; then
    pend 'S10-3 无徽章作者的详情不带 topBadge' '库里没有「无徽章作者 + 存活主题」的组合'
  else
    api GET "/topics/$plain_topic" "$JAR_ANON"
    has_key="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | has("topBadge")' 2>/dev/null)"
    if [[ "$HTTP_STATUS" == 000 ]]; then
      pend 'S10-3 无徽章作者的详情不带 topBadge 键' 'API 连接失败，本条不可判'
    else
      report_value 'S10-3 无徽章作者的详情不带 topBadge 键（omitempty）' \
        "GET /topics/$plain_topic" 'has_topBadge_key' false "${has_key:-unknown}" "$M2_READY"
    fi
  fi

  # 评论列表：有徽章作者的评论要有对象，无徽章作者必须「键缺省」而不是空对象。
  comment_topic="${TOPIC_ID:-$badged_topic}"
  if [[ -z "$comment_topic" ]]; then
    pend 'S10-4 评论列表 topBadge 装饰' '没有可用主题'
  else
    api GET "/topics/$comment_topic/comments?limit=30" "$JAR_ANON"
    if [[ "$HTTP_STATUS" == 000 ]]; then
      pend 'S10-4 评论列表可读' 'API 连接失败，本条不可判'
    elif [[ "$HTTP_STATUS" != 200 ]]; then
      fail 'S10-4 评论列表可读' "HTTP $HTTP_STATUS（$(body_excerpt)）"
    else
      empty_obj="$(printf '%s' "$HTTP_BODY" | jq -r '[(.data // .) | if type=="array" then . else (.items // []) end | .[] | select(has("topBadge")) | select((.topBadge | type) == "object") | select((.topBadge.slug // "") == "")] | length' 2>/dev/null)"
      report_value 'S10-4 评论列表里没有「空对象徽章」（必须是键缺省）' \
        "GET /topics/$comment_topic/comments" 'empty_badge_objects' 0 "${empty_obj:-0}" "$M2_READY"
      if [[ -n "${badged_uid:-}" ]]; then
        badged_comment="$(printf '%s' "$HTTP_BODY" | jq -r --argjson u "$badged_uid" '[(.data // .) | if type=="array" then . else (.items // []) end | .[] | select((.author.id // .authorId) == $u) | select(has("topBadge"))] | length' 2>/dev/null)"
        if [[ "${badged_comment:-0}" -gt 0 ]]; then
          pass 'S10-5 有徽章作者的评论带 topBadge'
        else
          pend 'S10-5 有徽章作者的评论带 topBadge' '该主题下暂无该作者的评论'
        fi
      fi
    fi
  fi

  # 零基线用户（丙 457）发一条评论：它的 topBadge 必须键缺省，端到端确认 NULL 路径。
  zero_uid="$(sqlv "SELECT id FROM users WHERE username = 'qa_badge_probe'")"
  if [[ "$MUTATIONS" != 1 ]]; then
    pend 'S10-6 零基线用户评论的 topBadge 键缺省' '只读模式不造评论'
  elif [[ -z "$zero_uid" || -z "$comment_topic" ]]; then
    pend 'S10-6 零基线用户评论的 topBadge 键缺省' '缺丙（qa_badge_probe）或可用主题'
  else
    JAR_C="$work/c.jar"
    : >"$JAR_C"
    zero_email="$(creds_email 丙 || sqlv "SELECT email FROM users WHERE id = $zero_uid")"
    if ! login_into "$JAR_C" "$zero_email" "$(creds_password)"; then
      pend 'S10-6 零基线用户评论的 topBadge 键缺省' "丙 登录失败 HTTP $HTTP_STATUS"
    else
      zero_badge="$(sqlv "SELECT coalesce(top_badge_id::text, '') FROM users WHERE id = $zero_uid")"
      if [[ -n "$zero_badge" ]]; then
        pend 'S10-6 零基线用户评论的 topBadge 键缺省' "丙 现在有徽章（top_badge_id=$zero_badge），不是零基线，先清掉再验"
      else
        raw_doc="$(jq -nc --arg t "S10 null-path probe $RUN_TAG" '{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:$t}]}]}')"
        if action_ok 'S10-6a 零基线用户发评论' 200,201 POST "/topics/$comment_topic/comments" "$JAR_C" \
          "$(jq -nc --arg r "$raw_doc" '{content:{rawContent:$r,sourceFormat:"editor-document"}}')"; then
          new_cid="$(sqlv "SELECT id FROM comments WHERE author_user_id = $zero_uid ORDER BY id DESC LIMIT 1")"
          api GET "/topics/$comment_topic/comments?limit=50" "$JAR_ANON"
          zero_shape="$(printf '%s' "$HTTP_BODY" | jq -r --argjson c "$new_cid" --argjson u "$zero_uid" \
            '[(.data // .) | if type=="array" then . else (.items // []) end | .[] | select(.id == $c) | if has("topBadge") then (if (.topBadge|type)=="object" and ((.topBadge.slug // "")=="" ) then "empty-object" else "object" end) else "absent" end] | first // "missing"' 2>/dev/null)"
          report_value 'S10-6b 零基线评论的 topBadge 键缺省（非空对象）' \
            "GET comments 找评论 $new_cid" 'topBadge_shape' absent "${zero_shape:-missing}" "$M2_READY"
        fi
      fi
    fi
  fi
fi

# ---------------------------------------------------------------- S12 后台语义回归

step "S12 后台语义（重算补授予 / revoked 语义 / 分组部分更新 / 停用不清计数）"
if [[ "$BADGES_FLAG_STUCK" == 1 ]]; then
  pend 'S12 后台语义' 'features.badges 未恢复，后台写端点一律 403'
elif [[ "$MUTATIONS" != 1 ]]; then
  pend 'S12 后台语义' '只读模式不建临时徽章/分组'
elif [[ "$ADMIN_READY" != 1 || "$M3_READY" != 1 ]]; then
  pend 'S12 后台语义' '缺管理员会话或 M3 未落地'
else
  # ---- 1) 重算必须真的补授予（不是只刷 top_badge_id）
  slug_rc="accept-recompute-${RUN_TAG}"
  id_rc=""
  if action_ok 'S12-1a 建 automatic 临时徽章（topic_count>=5）' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$slug_rc" '{slug:$s,name:"Recompute Probe",tier:"bronze",icon:"i-lucide-award",grantMode:"automatic",metric:"topic_count",threshold:5}')"; then
    id_rc="$(sqlv "SELECT id FROM badges WHERE slug = '$slug_rc'")"
  fi
  if [[ -n "$id_rc" ]]; then
    act_before="$(sqlv "SELECT coalesce(sum(topic_count), 0) FROM user_activity")"
    api POST /admin/badges/recompute "$JAR_ADMIN" '{}'
    if [[ "$HTTP_STATUS" != 200 ]]; then
      pend 'S12-1b 重算补授予' "recompute HTTP $HTTP_STATUS（$(body_excerpt)）"
    else
      granted1="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .granted // 0' 2>/dev/null)"
      rows="$(sqlv "SELECT count(*) FROM user_badges WHERE badge_id = $id_rc")"
      recompute_rows="$(sqlv "SELECT count(*) FROM user_badges WHERE badge_id = $id_rc AND granted_by IS NULL AND grant_reason = 'recompute'")"
      report_value 'S12-1b 重算返回 granted >= 1' 'POST /admin/badges/recompute' 'granted' non-zero \
        "$([[ "${granted1:-0}" -gt 0 ]] && printf non-zero || printf zero)" "$M2_READY"
      report_value 'S12-1c 重算真的插了授予行' 'SELECT count(*) FROM user_badges' 'rows' "$granted1" "${rows:-0}" "$M2_READY"
      report_value 'S12-1d 重算授予行带 granted_by=NULL + grant_reason=recompute' \
        'SELECT count(*) ... WHERE granted_by IS NULL AND grant_reason=recompute' 'recompute_rows' "${rows:-0}" "${recompute_rows:-0}" "$M2_READY"
      act_after="$(sqlv "SELECT coalesce(sum(topic_count), 0) FROM user_activity")"
      report_value 'S12-1e 重算不改写历史统计（topic_count 总和不变）' 'SELECT sum(topic_count)' 'sum' "$act_before" "$act_after" "$M1_READY"

      api POST /admin/badges/recompute "$JAR_ADMIN" '{}'
      granted2="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .granted // 0' 2>/dev/null)"
      report_value 'S12-1f 再点一次重算是幂等的（granted = 0）' 'POST /admin/badges/recompute ×2' 'granted' 0 "${granted2:-0}" "$M2_READY"
    fi
    action_ok 'S12-1g 清理 recompute 临时徽章' 200,204 DELETE "/admin/badges/$id_rc" "$JAR_ADMIN" >/dev/null
  fi

  # ---- 2) revoked 语义：没授予过就是 false，不是「请求成功 = true」
  slug_rv="accept-revoke-${RUN_TAG}"
  id_rv=""
  if action_ok 'S12-2a 建 manual 临时徽章' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$slug_rv" '{slug:$s,name:"Revoke Probe",tier:"bronze",icon:"i-lucide-award",grantMode:"manual"}')"; then
    id_rv="$(sqlv "SELECT id FROM badges WHERE slug = '$slug_rv'")"
  fi
  if [[ -n "$id_rv" && -n "$UID_A" ]]; then
    api DELETE "/admin/badges/$id_rv/grants/$UID_A" "$JAR_ADMIN"
    if [[ "$HTTP_STATUS" == 200 ]]; then
      revoked="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .revoked' 2>/dev/null)"
      report_value 'S12-2b 对未授予用户收回 → 200 且 revoked=false' \
        "DELETE /admin/badges/$id_rv/grants/$UID_A" 'revoked' false "${revoked:-missing}" "$M2_READY"
    else
      pend 'S12-2b revoked 语义' "HTTP $HTTP_STATUS（$(body_excerpt)）"
    fi
    action_ok 'S12-2c 清理 revoke 临时徽章' 200,204 DELETE "/admin/badges/$id_rv" "$JAR_ADMIN" >/dev/null
  fi

  # ---- 3) 分组 PATCH 是部分更新：只传 position 不许把 description 清空
  key_grp="accept-group-${RUN_TAG}"
  id_grp=""
  if action_ok 'S12-3a 建临时分组' 200,201 POST /admin/badges/groups "$JAR_ADMIN" \
    "$(jq -nc --arg k "$key_grp" '{key:$k,name:"Acceptance Group",description:"keep me",position:9}')"; then
    id_grp="$(sqlv "SELECT id FROM badge_groups WHERE key = '$key_grp'")"
  fi
  if [[ -n "$id_grp" ]]; then
    if action_ok 'S12-3b 分组只改 position' 200,204 PATCH "/admin/badges/groups/$id_grp" "$JAR_ADMIN" '{"position":5}'; then
      row="$(sqlv "SELECT position::text || '|' || description FROM badge_groups WHERE id = $id_grp")"
      report_value 'S12-3b 只改 position，description 不被清空' \
        "PATCH /admin/badges/groups/$id_grp {\"position\":5}" 'position|description' \
        '5|keep me' "$row" "$M2_READY"
    fi
    action_ok 'S12-3c 清理临时分组' 200,204 DELETE "/admin/badges/groups/$id_grp" "$JAR_ADMIN" >/dev/null
  fi

  # ---- 4) 停用不清 grantedCount（运营视角：停用不等于没人获得过）
  slug_gc="accept-count-${RUN_TAG}"
  id_gc=""
  if action_ok 'S12-4a 建 manual 临时徽章' 200,201 POST /admin/badges "$JAR_ADMIN" \
    "$(jq -nc --arg s "$slug_gc" '{slug:$s,name:"Count Probe",tier:"bronze",icon:"i-lucide-award",grantMode:"manual"}')"; then
    id_gc="$(sqlv "SELECT id FROM badges WHERE slug = '$slug_gc'")"
  fi
  if [[ -n "$id_gc" && -n "$UID_A" ]]; then
    if action_ok 'S12-4b 授予甲' 200,201 POST "/admin/badges/$id_gc/grants" "$JAR_ADMIN" \
      "$(jq -nc --argjson u "$UID_A" '{userId:$u,reason:"count probe"}')"; then
      action_ok 'S12-4c 停用它' 200,204 PATCH "/admin/badges/$id_gc" "$JAR_ADMIN" '{"enabled":false}' >/dev/null
      api GET /admin/badges "$JAR_ADMIN"
      cnt="$(printf '%s' "$HTTP_BODY" | jq -r --arg s "$slug_gc" '(.data // .) | (.items // []) | map(select(.slug == $s)) | .[0].grantedCount // "missing"' 2>/dev/null)"
      report_value 'S12-4d 停用后 grantedCount 保持非零' \
        'GET /admin/badges（停用的那条）' 'grantedCount' non-zero \
        "$([[ "${cnt:-missing}" != missing && "${cnt:-0}" -gt 0 ]] && printf non-zero || printf '%s' "${cnt:-missing}")" "$M2_READY"
    fi
    action_ok 'S12-4e 清理 count 临时徽章' 200,204 DELETE "/admin/badges/$id_gc" "$JAR_ADMIN" >/dev/null
  fi

  # 临时徽章被删后，曾把它当 top 的用户会被 ON DELETE SET NULL 打成 NULL；
  # 收尾再刷一次，把 top_badge_id 修回他们真正的最高级徽章（否则后续浏览器 QA 会看到「没徽章」）。
  api POST /admin/badges/recompute "$JAR_ADMIN" '{}'
  if [[ "$HTTP_STATUS" == 200 ]]; then
    note "收尾重算完成（refreshed=$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .refreshed // 0' 2>/dev/null)），top_badge_id 已修复。"
  else
    note "收尾重算 HTTP $HTTP_STATUS：受影响用户的 top_badge_id 可能仍是 NULL，跑一次 recompute 即可修复。"
  fi
fi

# ---------------------------------------------------------------- S9 棘轮基线

step "S9 棘轮基线（前端抽块 / 后端抽取必须净减并同轮下调）"
check_baseline() { # <仓库相对路径>
  local rel=$1 cap actual
  cap="$(jq -r --arg k "$rel" '.legacyLargeFiles[$k] // empty' "$BASELINE_JSON" 2>/dev/null)"
  if [[ ! -f "$ROOT_DIR/$rel" ]]; then
    pend "基线 $rel" '文件不存在'
    return
  fi
  actual="$(wc -l <"$ROOT_DIR/$rel" | tr -d '[:space:]')"
  if [[ -z "$cap" ]]; then
    pend "基线 $rel" "文件不在 legacyLargeFiles 基线里（$actual 行）"
    return
  fi
  if [[ "$actual" -gt "$cap" ]]; then
    fail "基线 $rel" "文件 $actual 行 > 基线 $cap：棘轮被撑破"
  elif [[ "$actual" -lt "$cap" ]]; then
    # 「变短但没下调基线」由 tests/validate-architecture-boundaries.mjs 判红（两个方向都查），
    # 这里只提示，避免双重报告；M8 的 FAIL 位留给功能问题。
    note "基线 $rel 已净减（$actual < $cap）但基线未同轮下调——由 validator 兜底判红，本脚本不重复计 FAIL。"
  else
    pass "基线 $rel（$actual = $cap）"
  fi
}
check_baseline 'apps/web/app/components/forum/SFTopicShowPage.vue'
check_baseline 'apps/api/app/Models/Forum/postgres_store_ops.go'

# ---------------------------------------------------------------- 收尾

if [[ "$KEEP" != 1 ]]; then
  step "清理（SFORUM_ACCEPT_KEEP=1 可保留）"
  if [[ -n "$TOPIC_ID" && -n "$UID_A" ]]; then
    api DELETE "/topics/$TOPIC_ID" "$JAR_A" >/dev/null
    if [[ "$HTTP_STATUS" == 200 || "$HTTP_STATUS" == 204 ]]; then
      note "已删除造出来的主题 $TOPIC_ID"
    else
      note "主题 $TOPIC_ID 未删除（HTTP $HTTP_STATUS），需要时手动清理"
    fi
  fi
  if [[ -n "$UID_A" ]]; then
    stale="$(sqlv "SELECT coalesce(string_agg(id::text, ' '), '') FROM topics WHERE author_user_id = $UID_A AND title LIKE 'acceptance %' AND deleted_at IS NULL")"
    for id in $stale; do
      api DELETE "/topics/$id" "$JAR_A" >/dev/null
      [[ "$HTTP_STATUS" == 200 || "$HTTP_STATUS" == 204 ]] && note "清掉遗留主题 $id"
    done
  fi
  leftover="$(sqlv "SELECT count(*) FROM badges WHERE slug LIKE 'accept-%'")"
  if [[ "${leftover:-0}" != 0 && "$ADMIN_READY" == 1 ]]; then
    note "还有 $leftover 个 accept-* 临时徽章，需要时手动清理"
  fi
else
  note "SFORUM_ACCEPT_KEEP=1：保留现场。"
fi

step "汇总"
printf '  ok %s · FAIL %s · pend %s\n' "$PASS_N" "$FAIL_N" "$PEND_N"
if [[ "$CONN_FAILS" -gt 0 ]]; then
  printf '  注意 本轮有 %s 次请求在重试后仍连不上 API（重建窗口），相关条目已按 pend 处理——结论不完整，别当全绿。\n' "$CONN_FAILS"
fi
if [[ "$FAIL_N" -gt 0 ]]; then
  printf '\n  失败项：\n'
  for line in "${FAIL_LINES[@]}"; do printf '    - %s\n' "$line"; done
fi
if [[ "$PEND_N" -gt 0 ]]; then
  printf '\n  待落地/待人工项：\n'
  for line in "${PEND_LINES[@]}"; do printf '    - %s\n' "$line"; done
fi
printf '\n  人工清单：scripts/verify-badges-acceptance-manual.md\n'

if [[ "$FAIL_N" -gt 0 ]]; then
  die 1 "有 $FAIL_N 项 FAIL。"
fi
if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then
  die 4 "--strict：还有 $PEND_N 项 pend，M8 收口不算完成。"
fi
if [[ "$GUARD_REFUSED" == 1 ]]; then
  printf '\n[exit 3] 开发库守卫：只跑了只读段。写操作证据请用一次性集群或设 SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1。\n'
  exit 3
fi
printf '\n通过：无 FAIL。\n'
exit 0
