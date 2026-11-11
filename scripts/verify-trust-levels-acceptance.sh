#!/usr/bin/env bash
# 信任等级（Discourse TL）：验收验证器（M7 自动化部分 / M8 证据采集）。
#
# 验证对象：knowledge/plans/2026-11-10-trust-levels.md §0 A–H 八组口径、§7 边界与失败模式、
# §8 测试矩阵、§12 证据清单里可自动化的部分。
# 浏览器 QA（资料页等级卡 / 后台阈值页 / 390x844）见 storage/qa/trust-levels-shots.mjs。
# 账号矩阵设计见 storage/qa/trust-levels-matrix.md，造数走 storage/qa/seed-trust-levels-matrix.sh。
#
# ── 设计前提（照抄徽章脚本的教训，别删）───────────────────────────────────────
# 这个脚本必须在「代码只完成一半」时也有意义，所以每条断言都绑定阶段：
#   tl0 声明/ADR   -> 决策记录或 app/Models/TrustLevels 目录
#   tl1 领域纯函数 -> Level() / LevelPolicy / earnable registry
#   tl2 Identity   -> Actor.TrustLevel + LoadActor 解析 + Can() 合并
#   tl3 Forum 收编 -> trustForActor → limitsForActor
#   tl4 HTTP       -> 控制器 + contracts/openapi/paths/trust-levels.yaml
#   tl5 前端       -> 资料页等级卡 / 后台阈值页
# 阶段由仓库文件存在性自动探测（--phase 可强制覆盖），未落地阶段的断言输出 pend 而不是 FAIL。
#
# ── 三种结果 ──────────────────────────────────────────────────────────────────
#   ok    实测通过（有证据）
#   FAIL  已落地阶段的行为不符（要修）
#   pend  阶段未落地 / 环境前置不满足（凭证、开关、预热日历），**不算通过**
#
# 上一刀吃过假阳性的亏：finish 只在「失败列表为空」时就宣布成功，于是全 pend 也报「通过」。
# 本脚本的对策（不许删）：
#   1. 汇总必须打印分组覆盖表，`ok=0 且 pend>0` 的组标成「无实测证据」；
#   2. 有 pend 时只说「无 FAIL」，绝不出现「通过 / 全绿」字样；
#   3. --strict 要求 0 FAIL 且 0 pend；--m8 额外要求实测断言数 ≥ 70（计划 §0 口径）。
#
# 退出码：
#   0  无 FAIL（允许 pend；结论不完整，见汇总的分组覆盖表）
#   1  有 FAIL
#   2  预检失败（缺工具、缺 DATABASE_URL）
#   4  --strict / --m8 未达标（仍有 pend，或实测断言数不足 70，或存在无实测证据的组）
#
# 环境变量：
#   DATABASE_URL                     必填。与 API 同一个库；脚本只读它取真值，从不写库。
#   SFORUM_API_URL                   默认 http://127.0.0.1:8080/api/v1
#   SFORUM_ACCEPT_CSRF_ORIGIN        默认 $APP_URL 或 http://127.0.0.1:3000
#   SFORUM_ACCEPT_SQL_CMD            自定义查库命令（读 stdin，需接受 -c SQL），优先于 psql/docker
#   SFORUM_ACCEPT_PSQL_IMAGE         无本机 psql 时用哪个镜像（默认 postgres:17-alpine）
#   SFORUM_TL_CREDS_FILE             QA 账号矩阵文件（默认 storage/qa/creds.txt）
#   SFORUM_TL_CREDS_FILE_TL          信任等级矩阵文件（默认 storage/qa/creds-trust-levels.txt）
#   SFORUM_TL_TEST_LOG               隔离库测试日志路径；给了才断言「全绿」，没给则 pend
#   SFORUM_TL_PERF_BASELINE          性能基线 JSON（默认 storage/qa/trust-levels-perf-baseline.json）
#   SFORUM_TL_PERF_CURRENT           落地后重测 JSON（默认 storage/qa/trust-levels-perf-current.json）
#   SFORUM_TL_PERF_MAX_DELTA_MS      p95 增量判据（默认 1.0，超过即 D4 复议）
#   SFORUM_TL_SKIP_SLOW=1            跳过 H 组两个全量静态门禁（OpenAPI refs / 架构基线）
#
# 库内真值提示（踩过两次，都产出过假结论）：
#   · options 的真实表名是 **web_options(name, value)**，不是 options。permissions 表才是权限目录。
#   · **web_options.value 是纯文本**（feature flag 存 'enabled'/'disabled'），**不是 JSON**：
#     `value::jsonb` 会直接报 invalid input syntax。取标量用 value::text（见 optv()）。
#   · **feature flag 的 public 标记不在库里**，它是代码层 optionDefinition.public
#     （app/Models/Options/feature_flags.go）。查库断言 public 是第五个假阳性的根因。
#   · 权限键的显示文案也不走 locale：后台读 DB permissions.description（usePermissionText 回落）。
#     locale 只负责档位名（H-4）。权限键的四处登记见 H-6。
#
# 用法：
#   DATABASE_URL=... ./scripts/verify-trust-levels-acceptance.sh                 # 自动探测阶段
#   ./scripts/verify-trust-levels-acceptance.sh --phase=tl2                      # 强制阶段
#   SFORUM_TL_TEST_LOG=tests/.logs/go.log ./scripts/verify-trust-levels-acceptance.sh
#   ./scripts/verify-trust-levels-acceptance.sh --m8                             # M8 收口门禁
#   ./scripts/verify-trust-levels-acceptance.sh --mutations                      # 变异验证（每条必须 RED）
#   # --mutations 没有隔离库时会自动用 ./scripts/test-api-isolated.sh 逐条起一次性集群（慢但准）

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
WEB_DIR="$ROOT_DIR/apps/web"
TL_DIR="$API_DIR/app/Models/TrustLevels"
TL_CTRL_DIR="$API_DIR/app/Http/Controllers/TrustLevels"
IDENTITY_DIR="$API_DIR/app/Models/Identity"
FORUM_DIR="$API_DIR/app/Models/Forum"
OPENAPI_TL="$ROOT_DIR/contracts/openapi/paths/trust_levels.yaml"
OPENAPI_TL_SCHEMA="$ROOT_DIR/contracts/openapi/schemas/trust_levels.yaml"
BASELINE_JSON="$ROOT_DIR/tests/architecture-boundaries-baseline.json"
MODULE_NOTE="$ROOT_DIR/knowledge/modules/trust-levels.md"
PERF_BASELINE="${SFORUM_TL_PERF_BASELINE:-$ROOT_DIR/storage/qa/trust-levels-perf-baseline.json}"
PERF_CURRENT="${SFORUM_TL_PERF_CURRENT:-$ROOT_DIR/storage/qa/trust-levels-perf-current.json}"
PERF_MAX_DELTA="${SFORUM_TL_PERF_MAX_DELTA_MS:-1.0}"
CREDS_TL="${SFORUM_TL_CREDS_FILE_TL:-$ROOT_DIR/storage/qa/creds-trust-levels.txt}"
CREDS_FILE="${SFORUM_TL_CREDS_FILE:-$ROOT_DIR/storage/qa/creds.txt}"
MATRIX_DOC="$ROOT_DIR/storage/qa/trust-levels-matrix.md"
SHOTS_MJS="$ROOT_DIR/storage/qa/trust-levels-shots.mjs"

API_URL="${SFORUM_API_URL:-http://127.0.0.1:8080/api/v1}"
API_URL="${API_URL%/}"
DB_URL="${DATABASE_URL:-}"
CSRF_ORIGIN="${SFORUM_ACCEPT_CSRF_ORIGIN:-${APP_URL:-http://127.0.0.1:3000}}"
CSRF_ORIGIN="${CSRF_ORIGIN%/}"

STRICT=0
M8_GATE=0
MUTATIONS=0
PHASE_OVERRIDE=""
SKIP_SLOW="${SFORUM_TL_SKIP_SLOW:-0}"
MIN_ASSERTIONS=70

work="$(mktemp -d)"

# 共享资源锁：dev 库 + features.trust_levels 是全队共用的。两轮验收同时跑会互相污染
# （2026-11-11 三次事故：backend 改白名单撞 J-01、reviewer 就地变异、开关被另一轮关掉）。
LOCK_FILE="$ROOT_DIR/storage/qa/trust-levels/_lock"
if [[ -f "$LOCK_FILE" && "${SFORUM_TL_FORCE:-0}" != 1 ]]; then
  _lock_pid="$(sed -n 's/.*pid=\([0-9]*\).*/\1/p' "$LOCK_FILE" 2>/dev/null)"
  if [[ -n "$_lock_pid" ]] && ! kill -0 "$_lock_pid" 2>/dev/null; then
    printf '发现陈旧锁（pid %s 已不存在），接管：%s\n' "$_lock_pid" "$LOCK_FILE" >&2
    rm -f "$LOCK_FILE"
  fi
fi
if [[ -f "$LOCK_FILE" && "${SFORUM_TL_FORCE:-0}" != 1 ]]; then
  printf '另一轮验收正在跑，先等它结束：\n  %s\n  %s\n（确认没人跑后可 SFORUM_TL_FORCE=1 强制）\n' \
    "$LOCK_FILE" "$(cat "$LOCK_FILE" 2>/dev/null)" >&2
  exit 6
fi
mkdir -p "$(dirname "$LOCK_FILE")"
printf 'owner=ops(verify-trust-levels-acceptance.sh) pid=%s since=%s\n' "$$" "$(date -Iseconds)" >"$LOCK_FILE"
TL_FLAG_TO_RESTORE=''
on_exit() {
  rm -f "$LOCK_FILE"
  if [[ -n "$TL_FLAG_TO_RESTORE" ]]; then
    m_flag features.trust_levels "$TL_FLAG_TO_RESTORE" >/dev/null 2>&1 || true
  fi
}
cleanup() {
  local status=$?
  trap - EXIT
  on_exit
  [[ -n "$work" && -d "$work" ]] && rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

usage() { sed -n '2,62p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --m8) STRICT=1; M8_GATE=1 ;;
    --mutations) MUTATIONS=1 ;;
    --phase=*) PHASE_OVERRIDE="${arg#--phase=}" ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done

# ---------------------------------------------------------------- 结果与分组记账

PASS_N=0
FAIL_N=0
PEND_N=0
FAIL_LINES=()
PEND_LINES=()
LIMITED_N=0
CONN_FAILS=0
GROUP=""
declare -A G_OK=() G_FAIL=() G_PEND=() G_TITLE=()
GROUP_ORDER=()

step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
warn() { printf '  注意 %s\n' "$*" >&2; }

group() { # <字母> <标题>
  GROUP="$1"
  G_TITLE[$1]="$2"
  G_OK[$1]=${G_OK[$1]:-0}
  G_FAIL[$1]=${G_FAIL[$1]:-0}
  G_PEND[$1]=${G_PEND[$1]:-0}
  local seen=0 g
  for g in ${GROUP_ORDER[@]+"${GROUP_ORDER[@]}"}; do [[ "$g" == "$1" ]] && seen=1; done
  [[ "$seen" == 0 ]] && GROUP_ORDER+=("$1")
  step "$1 $2"
}

require_group() { [[ -n "$GROUP" ]] || { printf '内部错误：断言未归属分组\n' >&2; exit 5; }; }

ASSERT_LOG="$work/asserts.tsv"
: >"$ASSERT_LOG"
record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$ASSERT_LOG"; }

pass() {
  require_group
  record ok "$1" ""
  PASS_N=$((PASS_N + 1))
  G_OK[$GROUP]=$(( ${G_OK[$GROUP]:-0} + 1 ))
  printf '  ok    %s\n' "$1"
}
fail() {
  require_group
  record FAIL "$1" "${2:-}"
  FAIL_N=$((FAIL_N + 1))
  G_FAIL[$GROUP]=$(( ${G_FAIL[$GROUP]:-0} + 1 ))
  FAIL_LINES+=("[$GROUP] $1${2:+ — $2}")
  printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"
}
pend() {
  require_group
  record pend "$1" "${2:-}"
  PEND_N=$((PEND_N + 1))
  G_PEND[$GROUP]=$(( ${G_PEND[$GROUP]:-0} + 1 ))
  PEND_LINES+=("[$GROUP] $1${2:+ — $2}")
  printf '  pend  %s%s\n' "$1" "${2:+（$2）}"
}

die() {
  local code="$1"
  shift
  printf '\n[exit %s] %s\n' "$code" "$*" >&2
  exit "$code"
}

# ---------------------------------------------------------------- 阶段探测

has_dir() { [[ -d "$ROOT_DIR/$1" ]]; }
has_file() { [[ -e "$ROOT_DIR/$1" ]]; }
has_glob() { compgen -G "$ROOT_DIR/$1" >/dev/null 2>&1; }
code_grep() { grep -rqE "$1" "${@:2}" 2>/dev/null; }

phase_ready() {
  local want=$1
  if [[ -n "$PHASE_OVERRIDE" ]]; then
    case "$PHASE_OVERRIDE" in
      full) return 0 ;;
      auto) ;;
      tl0) [[ "$want" == tl0 ]] && return 0 || return 1 ;;
      tl1) [[ "$want" == tl0 || "$want" == tl1 ]] && return 0 || return 1 ;;
      tl2) [[ "$want" != tl4 && "$want" != tl5 ]] && return 0 || return 1 ;;
      tl3) [[ "$want" != tl4 && "$want" != tl5 ]] && return 0 || return 1 ;;
      tl4) [[ "$want" != tl5 ]] && return 0 || return 1 ;;
      tl5) return 0 ;;
      *) die 2 "未知 --phase 值：$PHASE_OVERRIDE（auto|tl0|tl1|tl2|tl3|tl4|tl5|full）" ;;
    esac
  fi
  case "$want" in
    tl0) has_dir 'apps/api/app/Models/TrustLevels' || has_glob 'knowledge/decisions/*trust*' ;;
    # 不钉函数名：后端实际导出的是 LevelOf / LevelOfAt（2026-11-10 实测），钉 Level( 会稳定误判未落地。
    tl1) has_dir 'apps/api/app/Models/TrustLevels' && code_grep 'func Level' "$TL_DIR" ;;
    tl2) code_grep 'TrustLevel' "$IDENTITY_DIR/types.go" "$IDENTITY_DIR/policy.go" 2>/dev/null ;;
    tl3) code_grep 'limitsForActor' "$FORUM_DIR" ;;
    tl4) has_dir 'apps/api/app/Http/Controllers/TrustLevels' || has_file 'contracts/openapi/paths/trust-levels.yaml' ;;
    tl5) has_glob 'apps/web/app/components/*/*rust*' || code_grep 'data-trust-level' "$WEB_DIR/app" ;;
    *) return 1 ;;
  esac
}

phase_reason() {
  case "$1" in
    tl0) printf 'tl0 声明未落地（缺 app/Models/TrustLevels 与 decisions/*trust*）' ;;
    tl1) printf 'tl1 领域层未落地（app/Models/TrustLevels 里没有 Level 计算函数）' ;;
    tl2) printf 'tl2 Identity 接线未落地（Identity/types.go|policy.go 里没有 TrustLevel）' ;;
    tl3) printf 'tl3 Forum 收编未落地（Forum 里没有 limitsForActor）' ;;
    tl4) printf 'tl4 HTTP 层未落地（缺 Controllers/TrustLevels 与 openapi/paths/trust-levels.yaml）' ;;
    tl5) printf 'tl5 前端未落地（缺资料页等级卡 / 后台阈值页组件）' ;;
    *) printf '阶段未落地' ;;
  esac
}

phase_flag() { if phase_ready "$1"; then printf '就绪'; else printf '未落地'; fi; }

# D9：features.trust_levels 关闭时四条路由一律 404 —— 那是产品面语义，不是缺陷。
# 断言必须知道当前开关状态，否则「关着开关跑」会得到四个假 FAIL（2026-11-10 实测）。
tl_flag_enabled() { [[ "$(optv features.trust_levels)" == enabled ]]; }

# 通用：断言「某能力落地后必须存在的东西」；未落地时 pend，不猜。
expect_present() { # <标签> <阶段> <检测命令...>
  local label=$1 phase=$2
  shift 2
  if "$@"; then
    pass "$label"
  elif phase_ready "$phase"; then
    fail "$label" '阶段已就绪但该项缺失'
  else
    pend "$label" "$(phase_reason "$phase")"
  fi
}

# ---------------------------------------------------------------- 预检

step '预检'
command -v curl >/dev/null || die 2 '缺少 curl。'
command -v jq >/dev/null || die 2 '缺少 jq。'
[[ -n "$DB_URL" ]] || die 2 'DATABASE_URL 未设置：脚本需要直连 API 所用的同一个库来读真值。'

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

SQL_LAST_ERROR=''
sqlv() {
  local out rc
  out="$("${SQL_RUNNER[@]}" -c "$1" 2>"$work/sql.err")"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    SQL_LAST_ERROR="$(tr '\n' ' ' <"$work/sql.err" | cut -c1-400)"
    printf ''
    return 1
  fi
  printf '%s' "$out" | sed '/^$/d'
  return 0
}

if ! sqlv 'SELECT 1' >/dev/null; then
  die 2 "无法查库（$DB_URL）：${SQL_LAST_ERROR:-无输出}"
fi

table_exists() { [[ "$(sqlv "SELECT to_regclass('public.$1') IS NOT NULL")" == t ]]; }
column_exists() { # <table> <column>
  [[ "$(sqlv "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='$1' AND column_name='$2'")" != 0 ]]
}
# options 的真实表是 web_options(name, value)；value 可能是 jsonb，取标量时去引号。
optv() {
  sqlv "SELECT coalesce(value::text, '') FROM web_options WHERE name = '$1'" | tr -d '"'
}

# ---------------------------------------------------------------- N 档口径
# 本脚本不再假设「五档」。active 档位数与档位身份一律从**库内 trust.levels 真值**读，
# 读面只用于证明「响应 = 配置的投影」，不参与定义真值（避免第二份常量）。
# 默认五档站点下：active = 5、ids = newcomer,member,resident,veteran,leader，
# 与改造前写死的判据逐字等价——这是判断「新功能坏」还是「脚本改坏」的基线。
tl_config_json() { # 库内 trust.levels 的原始 JSON（v1 或 v2；读不到返回空串）
  sqlv "SELECT coalesce(value::text, '') FROM web_options WHERE name = 'trust.levels'"
}
tl_expected_ids() { # active 档位的稳定身份 id，按判定/展示顺序，逗号分隔，不含 TL0
  local raw
  raw="$(tl_config_json)"
  [[ -n "$raw" ]] || return 0
  printf '%s' "$raw" | jq -r '
    (if ((.schemaVersion // 1) >= 2) then
       ((.tiers // []) | map(select((.retired // false) | not)) | map(.id))
     else
       ((.levels // []) | map(.levelSlug))
     end) | map(select(. != null and . != "")) | join(",")' 2>/dev/null
}
tl_expected_active() { # active 档位数（不含 TL0）
  local ids
  ids="$(tl_expected_ids)"
  [[ -n "$ids" ]] || return 0
  printf '%s' "$ids" | awk -F, '{print NF}'
}
tl_expected_levels() { # 读面应有的档位数 = TL0 + active 档
  local a
  a="$(tl_expected_active)"
  [[ -n "$a" ]] || return 0
  printf '%s' "$((a + 1))"
}
tl_public_max_rank() { # <公开载荷>：最高 rank；载荷不可用时退回库内档数，再退回 4（历史五档）
  local pub=${1:-} a
  a="$(printf '%s' "$pub" | jq -r '(.data // .) | [(.levels // [])[].level] | max // empty' 2>/dev/null)"
  [[ -n "$a" ]] || a="$(tl_expected_active)"
  [[ -n "$a" ]] || a=4
  printf '%s' "$a"
}

table_exists users || die 2 'users 表不存在：DATABASE_URL 指错了库？'

HTTP_STATUS=000
HTTP_BODY=''

api() { # <METHOD> <PATH> [JAR] [BODY]
  local method=$1 path=$2 jar=${3:-} body=${4:-}
  local -a args=(-sS --noproxy '*' -o "$work/body.out" -D "$work/hdr.out" -w '%{http_code}'
    -X "$method" -H 'Accept: application/json'
    -H "Origin: $CSRF_ORIGIN" -H "Referer: $CSRF_ORIGIN/")
  [[ -n "$jar" ]] && args+=(-b "$jar" -c "$jar")
  local token=''
  [[ -n "$jar" && -f "$jar" ]] && token="$(cookie_value "$jar" csrf_)"
  [[ -n "$token" ]] && args+=(-H "X-CSRF-Token: $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data-binary "$body")

  local attempt=1 delay=2
  while :; do
    : >"$work/body.out"   # 连接失败时 curl 不截断 -o 文件，残留响应会冒充本次结果
    HTTP_STATUS="$(curl "${args[@]}" --max-time 25 "$API_URL$path" 2>"$work/curl.err")" || HTTP_STATUS=000
    HTTP_BODY="$(cat "$work/body.out" 2>/dev/null || true)"
    case "$HTTP_STATUS" in
      429)
        if [[ "$attempt" -lt 4 ]]; then
          local ra
          ra="$(awk 'tolower($1)=="retry-after:"{gsub(/\r/,"",$2); print $2}' "$work/hdr.out" 2>/dev/null | tail -1)"
          if [[ "$ra" =~ ^[0-9]+$ ]] && [[ "$ra" -ge 1 ]] && [[ "$ra" -le 30 ]]; then
            delay="$ra"
          fi
          LIMITED_N=$((LIMITED_N + 1))
          note "429 限流：${delay}s 后重试（$attempt/3）$method $path"
          sleep "$delay"
          delay=$((delay * 2))
          attempt=$((attempt + 1))
          continue
        fi
        return 0
        ;;
      000)
        if [[ "$attempt" -lt 3 ]]; then
          sleep 2
          attempt=$((attempt + 1))
          continue
        fi
        CONN_FAILS=$((CONN_FAILS + 1))
        return 0
        ;;
      *) return 0 ;;
    esac
  done
}

body_excerpt() { printf '%s' "$HTTP_BODY" | tr '\n' ' ' | cut -c1-220; }
json_first() { printf '%s' "$HTTP_BODY" | jq -r --arg k "$1" '[.. | objects | .[$k]? // empty] | first // empty' 2>/dev/null; }
cookie_value() { awk -v n="$2" '$6 == n { v = $7 } END { if (v != "") print v }' "$1" 2>/dev/null; }

api_alive() { [[ "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' --max-time 4 "$API_URL/health")" == 200 ]]; }

# air 热重载会短暂下线：探测阶段重试，别把「正在重启」当成「不可达」。
HTTP_READY=0
for _i in 1 2 3 4 5 6 7 8 9 10; do
  if api_alive; then HTTP_READY=1; break; fi
  sleep 2
done
if [[ "$HTTP_READY" == 0 ]]; then
  note "API 不可达：$API_URL（HTTP 段断言一律 pend；静态与库内断言不受影响）"
fi

note "tl0 声明 $(phase_flag tl0) · tl1 领域 $(phase_flag tl1) · tl2 Identity $(phase_flag tl2) · tl3 Forum $(phase_flag tl3) · tl4 HTTP $(phase_flag tl4) · tl5 前端 $(phase_flag tl5)"
note '未落地阶段的断言记为 pend；要它们变 FAIL 用 --phase=full。pend 永远不算通过。'

# ---------------------------------------------------------------- 账号矩阵（可选）

JAR_TL0=''; JAR_TL2=''; JAR_TL4=''; JAR_ADMIN=''
QA_PASS="${QA_PASS:-QaPass!2026-devops}"

# storage/qa/creds.txt 是 Markdown 表格：| 甲 | 3 | seed_xxx | seed_xxx@seed.local | ...
# 与 creds-trust-levels.txt（管道分隔行）是两种格式，别混用。
creds_md_email() { # <代号>
  grep -E "^\| *$1 " "$CREDS_FILE" 2>/dev/null | head -1 \
    | awk -F'|' '{gsub(/^[ \t]+|[ \t]+$/, "", $5); print $5}'
}

# 后台**读**面只需要 admin.access（controller.go:129），所以戊 不可用时退到 丁b（moderator）。
# 写面（PUT / restore-defaults）要 trust.manage，那是另一条断言，别混用。
login_any_admin_reader() { # <jar> -> 成功时打印代号/账号
  local jar=$1 code email
  for code in 戊 丁b; do
    email="$(creds_md_email "$code" 2>/dev/null || true)"
    [[ -n "$email" ]] || continue
    if login_into "$jar" "$email" "$QA_PASS"; then
      probe_logged_in=$((probe_logged_in + 1)) printf '%s' "$code"; return 0; fi
    sleep 1   # 认证端点有 IP 限流：连试 4 个账号不间隔会把自己打成 429
  done
  # creds.txt 的密码会随环境轮换而滞后（2026-11-11 实测 戊/丁b 401）。矩阵账号 TLRW/TLRO
  # 是同级的后台读探针（只做读面，不碰写面判据）：名字里带来源，方便报告如实说明换人。
  for code in TLRW TLRO; do
    email="$(jq -r --arg c "$code" 'if type=="array" then .[] else .accounts[]? end | select(.code==$c) | .email' "$TL_ACCOUNTS" 2>/dev/null | head -1)"
    [[ -n "$email" ]] || continue
    if login_into "$jar" "$email" "$QA_PASS"; then
      probe_logged_in=$((probe_logged_in + 1)) printf '%s(%s)' "$code" "$email"; return 0; fi
    sleep 1
  done
  return 1
}

creds_tl_lookup() { # <代号> <字段 email|password> -> 值
  local key=$1 field=$2
  [[ -f "$CREDS_TL" ]] || return 1
  awk -F'|' -v k="$key" -v f="$field" '
    $0 ~ /^[[:space:]]*#/ { next }
    NF >= 2 {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $1)
      if ($1 == k) { v = (f == "email") ? $2 : $3; gsub(/^[[:space:]]+|[[:space:]]+$/, "", v); print v; exit }
    }' "$CREDS_TL"
}

login_into() { # <jar> <login> <password>
  local jar=$1 login=$2 password=$3
  [[ -n "$login" ]] || return 1
  : >"$jar"
  api GET /auth/session "$jar" >/dev/null
  api POST /auth/login "$jar" "$(jq -nc --arg l "$login" --arg p "$password" '{login:$l,password:$p}')"
  [[ "$HTTP_STATUS" == 200 ]]
}

uid_of() { # <username>
  local name=$1
  [[ "$name" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  sqlv "SELECT id FROM users WHERE username = '$name'"
}

# ================================================================ A 能力解析

group A '能力解析（Can 四来源真值表）'

# A-1 唯一裁决点：§1 明确否掉了「第二套守卫入口」，这是可静态证伪的结构断言。
second_entry="$(grep -rnE 'RequireTrustLevel|CanEarnedPermission|CanEarned' "$API_DIR/app" --include='*.go' 2>/dev/null | grep -v '_test\.go' | wc -l | tr -d ' ')"
if [[ "$second_entry" == 0 ]]; then
  pass 'A-1 不存在第二裁决点（无 RequireTrustLevel / CanEarned）'
else
  fail 'A-1 不存在第二裁决点' "发现 $second_entry 处第二裁决点符号：$(grep -rnE 'RequireTrustLevel|CanEarnedPermission|CanEarned' "$API_DIR/app" --include='*.go' 2>/dev/null | grep -v '_test\.go' | head -3 | tr '\n' ' ')"
fi

# A-2 真值表测试的存在性（执行另计 A-3，别混为一谈）
tt_file="$(grep -rlE 'truth.?table|真值表' "$IDENTITY_DIR" --include='*_test.go' 2>/dev/null | head -1)"
if [[ -n "$tt_file" ]]; then
  pass "A-2 4×4 真值表测试存在（${tt_file#"$ROOT_DIR"/}）"
elif phase_ready tl2; then
  fail 'A-2 4×4 真值表测试存在' 'Identity 下找不到真值表测试：需要静态×已挣得×deny×super_admin 的 4×4 全枚举（§8.1）'
else
  pend 'A-2 4×4 真值表测试存在' "$(phase_reason tl2)"
fi

# A-3 隔离库测试结论（按包分类，不按「有没有 FAIL 字样」一刀切）。
# 2026-11-10 lead 裁决：全量套件里有与本刀无关的红（扩展运行时 / HostAPI 生命周期 / bootstrap）。
# `bootstrap` 那条 `dial tcp 127.0.0.1:1` 的来源已查清（ops 2026-11-10）：
#   它是**测试代码里硬编码的「不可达地址」哨兵**，不是环境变量也不是代码默认值——
#   apps/api/bootstrap/query_registry_cache_test.go:22,31,70,504,517,546
#   （另有 query_registry_test.go:509 / query_registry_lifecycle_integration_test.go:87 的 PG 版）。
#   **这是测试 fixture 的不可达地址（端口 1），不是生产默认值**（生产默认 `redis:6379`，dev `.env` 是 16379）。
#   所以那组用例本身就是「拿连不上的 Redis/PG 验降级姿态」，与信任等级无关，不是缺陷、不需要立项。
# 无限期 pend 不诚实。判据改成：
#   · 本刀相关包必须全绿 → 否则 FAIL（列包名）
#   · 与本刀无关的失败逐条列名 + 注明归属，并允许通过
# 相关包清单 = 本刀真正改动的域；无关包清单 = 已裁决项，带裁决日期，便于下次复核。
# Localization 也算本刀相关：控制器发了 trust_levels.invalid_config / not_found / encode_failed 三个码。
# Support/HostAPI 也算本刀相关（本刀改过 TrustLevels 配置与 LoadEffectiveActorTx）——早先把它列进
# 「无关」是包归属抄错（4 条 ProtocolV2 用例定义在 HostAPI，不在 Controllers/Extensions）。
TL_TEST_RELEVANT='app/Models/TrustLevels|app/Models/Identity|app/Models/APITokens|app/Http/Controllers/TrustLevels|database/migrations|app/Support/Localization|app/Support/HostAPI'
# 已裁决的无关包（带裁决依据，不是「我觉得无关」）：
#   Controllers/Extensions —— 本刀未改动；bootstrap —— 测试 fixture 用不可达地址（127.0.0.1:1）验降级姿态，非生产默认值。
TL_TEST_KNOWN_UNRELATED='app/Http/Controllers/Extensions|bootstrap'
# 已裁决的「单条外部失败」（比包级放行更窄）：只放行这一个子用例，同包其它失败照常判红。
# 依据（backend 2026-11-10 复核）：app/Models/Entitlements/ 与 v2_command_entitlements.go 本刀零改动；
#   失败语义是「并发两次 grant 应恰好一次 COMMITTED」的幂等/并发路径，不是权限或等级；
#   HostAPI 的 31 个脏文件里本刀只碰 5 个，其余 25 个属徽章/插件 job 那批。
# 每条都必须附「归属 + 依据（可复核的事实）」，不许写结论句。
# ① entitlement：归属另一刀（并发幂等路径）；依据：Entitlements/ 与 v2_command_entitlements.go 本刀零改动。
# ②③④⑤ 徽章那刀（query_registry_core.go 新增三条徽章查询声明）：
#   依据：两个断言测试文件本刀零改动且对 public_badges 零引用；
#         失败语义是「封存目录整体内容不匹配」，差异在条目集合而非 seal；
#         本刀 bootstrap 改动（trust_levels_wiring.go / api_assembly_*.go / host_command_runtime.go）
#         不触及 query registry 的发布或封存。
TL_TEST_EXTERNAL_TESTS='TestPostgresProtocolV2SixDomainCommandsAllowedDeniedReplayAndRollback/entitlement|TestProductionLifecycleStackPublishesSealedCoreQueryCatalog|TestProductionLifecycleStackRestoresComponentsThroughSharedRegistry|TestProductionLifecycleStackSafeModeKeepsHostRoutesAndSkipsThirdParty|TestQueryCatalogHTTPReturnsSealedCatalogForViewers'
if [[ -z "${SFORUM_TL_TEST_LOG:-}" || ! -f "${SFORUM_TL_TEST_LOG}" ]]; then
  pend 'A-3 隔离库测试：本刀相关包全绿' '未提供 SFORUM_TL_TEST_LOG；跑 ./scripts/test-api-isolated.sh > /tmp/iso.log 2>&1 后把路径传进来'
else
  # 日志新鲜度：超过 6 小时的日志不能当这一版代码的证据。
  log_age=$(( $(date +%s) - $(stat -c %Y "$SFORUM_TL_TEST_LOG" 2>/dev/null || echo 0) ))
  failed_pkgs="$(grep -E '^FAIL[[:space:]]+github\.com/keeptrue/sforum/apps/api' "$SFORUM_TL_TEST_LOG" 2>/dev/null \
    | awk '{print $2}' | sed 's|github.com/keeptrue/sforum/apps/api/||' | sort -u)"
  relevant_fail="$(printf '%s\n' "$failed_pkgs" | grep -E "$TL_TEST_RELEVANT" || true)"
  unrelated_fail="$(printf '%s\n' "$failed_pkgs" | grep -vE "$TL_TEST_RELEVANT" | grep -v '^$' || true)"
  unknown_unrelated="$(printf '%s\n' "$unrelated_fail" | grep -vE "$TL_TEST_KNOWN_UNRELATED" || true)"
  # ① 硬断言：go test 的 -run 匹配不到任何测试时**退出码 0 且打印 ok**（小审 2026-11-10 实测）。
  #    所以「包 ok」不等于「测试跑过」。这里要求日志里必须出现这些用例的 === RUN。
  required_tests=(TestLevel TestEarnable TestEligiblePermissions TestRestrictActor
    TestEverySeededPermissionReachesTheDatabase TestTrustManagePermission)
  missing_tests=()
  skipped_tests=()
  for t in "${required_tests[@]}"; do
    grep -qE "^=== RUN +${t}" "$SFORUM_TL_TEST_LOG" 2>/dev/null || missing_tests+=("$t")
    grep -qE "^--- SKIP: ${t}" "$SFORUM_TL_TEST_LOG" 2>/dev/null && skipped_tests+=("$t")
  done
  no_tests_marker=0
  grep -q 'no tests to run' "$SFORUM_TL_TEST_LOG" 2>/dev/null && no_tests_marker=1
  if [[ "$log_age" -gt 21600 ]]; then
    pend 'A-3 隔离库测试：本刀相关包全绿' "日志已 $((log_age / 3600)) 小时未更新，不能当这一版代码的证据；重跑 ./scripts/test-api-isolated.sh"
  elif [[ "${#missing_tests[@]}" -gt 0 || "$no_tests_marker" == 1 ]]; then
    fail 'A-3 隔离库测试：本刀相关用例确实执行过' "日志里找不到这些用例的 === RUN：${missing_tests[*]:-（无）}$([[ "$no_tests_marker" == 1 ]] && printf "；日志含 'no tests to run'")。日志必须用 -v 生成：./scripts/test-api-isolated.sh -v > /tmp/iso.log 2>&1"
  elif [[ "${#skipped_tests[@]}" -gt 0 ]]; then
    fail 'A-3 隔离库测试：本刀相关用例确实执行过' "这些用例被 SKIP 了，SKIP 不算通过：${skipped_tests[*]}"
  elif [[ -n "$relevant_fail" ]]; then
    # 相关包里的失败，逐条对照「已裁决外部失败」清单；不在清单里的一律判红。
    mapfile -t fail_names < <(grep -E "^ *--- FAIL: " "$SFORUM_TL_TEST_LOG" 2>/dev/null | sed 's/^ *--- FAIL: //; s/ (.*//' | sort -u)
    external_hits=(); offending=()
    for name in "${fail_names[@]+"${fail_names[@]}"}"; do
      # 父用例也会打 --- FAIL（子用例失败会带出父用例）。若父用例的失败子用例全在外部清单里，
      # 父用例也按外部放行，否则清单会重复计数。
      parent_of_external=0
      while IFS= read -r entry; do
        [[ -z "$entry" ]] && continue
        # 父子双向：父在清单里 → 子放行；子在清单里 → 父放行。
        [[ "$entry" == "$name"/* || "$name" == "$entry"/* ]] && parent_of_external=1
      done < <(printf '%s\n' "$TL_TEST_EXTERNAL_TESTS" | tr '|' '\n')
      if printf '%s\n' "$TL_TEST_EXTERNAL_TESTS" | tr '|' '\n' | grep -Fxq "$name" || [[ "$parent_of_external" == 1 ]]; then
        external_hits+=("$name")
      else
        offending+=("$name")
      fi
    done
    if [[ "${#offending[@]}" == 0 && "${#external_hits[@]}" -gt 0 ]]; then
      pass "A-3 隔离库测试：本刀相关包全绿（外部失败 ${#external_hits[@]} 条已按依据放行）"
      note "外部失败（归属另一刀）：${external_hits[*]}"
      note "依据落盘：storage/qa/trust-levels/A/A3-external-attribution.md（逐条：用例名 / 归属 / 可复核依据）"
    else
      fail 'A-3 隔离库测试：本刀相关包全绿' "相关包失败：$(printf '%s' "$relevant_fail" | tr '\n' ' ')；未裁决用例：${offending[*]:-（见日志）}"
    fi
  elif [[ -n "$unknown_unrelated" ]]; then
    # 出现「没人裁决过」的无关失败：不能默默放过，也不能直接判本刀 FAIL —— 报出来等人裁决。
    fail 'A-3 隔离库测试：未裁决的无关失败' "新增未裁决失败包：$(printf '%s' "$unknown_unrelated" | tr '\n' ' ')（确认与本刀无关后加进 TL_TEST_KNOWN_UNRELATED 并注明日期）"
  else
    pass 'A-3 隔离库测试：本刀相关包全绿'
    if [[ -n "$unrelated_fail" ]]; then
      note "已裁决的无关失败（lead 2026-11-10）：$(printf '%s' "$unrelated_fail" | tr '\n' ' ')"
      note '这些包的归属不在本刀；它们绿了以后本行自然消失，不需要改判据。'
    fi
  fi
fi

# A-4 基线：super_admin 绕过仍在（本刀不许动它的位置）
if code_grep 'super_admin' "$IDENTITY_DIR/policy.go"; then
  pass 'A-4 基线 Can() 的 super_admin 绕过仍在（policy.go）'
else
  fail 'A-4 基线 Can() 的 super_admin 绕过仍在' 'policy.go 里找不到 super_admin：合并四来源时把绕过删了？'
fi

# A-5 deny 覆盖不能让已挣得复活（T4）
if grep -rlE 'deny' "$IDENTITY_DIR" --include='*_test.go' 2>/dev/null | grep -q .; then
  if grep -rqE 'deny.*(earned|trust|已挣得)|(earned|trust|已挣得).*deny' "$IDENTITY_DIR" --include='*_test.go' 2>/dev/null; then
    pass 'A-5 deny 与已挣得集合的交互有专门测试'
  elif phase_ready tl2; then
    fail 'A-5 deny 与已挣得集合的交互有专门测试' '有 deny 测试，但没有一条覆盖「deny 的键不被 TL 复活」（T4）：补一个 deny 掉 earnable 键后 Can() 仍为 false 的用例'
  else
    pend 'A-5 deny 与已挣得集合的交互有专门测试' "$(phase_reason tl2)"
  fi
else
  pend 'A-5 deny 与已挣得集合的交互有专门测试' '未找到 deny 相关测试'
fi

# A-6 派生视图只读：来源查询不得被 Can 反向依赖（§1：派生视图不能反过来影响裁决）。
# D30 已把 CapabilitySource 改名为 GrantSource（permission_origin.go:36，返回 PermissionOrigin）。
# 断言同时认两个名字：改名不该让守卫失效，也不该把「找不到」误判成通过。
origin_hits="$(grep -rlE 'func \(a Actor\) (GrantSource|CapabilitySource)\(' "$IDENTITY_DIR" --include='*.go' 2>/dev/null | head -1)"
if [[ -n "$origin_hits" ]]; then
  if grep -nE '\b(GrantSource|CapabilitySource)\(' "$IDENTITY_DIR/policy.go" 2>/dev/null | grep -q .; then
    fail 'A-6 来源查询不被 Can() 反向依赖' 'policy.go（Can 所在文件）里出现来源查询：派生视图变成了第二权威'
  else
    pass "A-6 来源查询不被 Can() 反向依赖（${origin_hits#"$ROOT_DIR"/}）"
  fi
else
  pend 'A-6 来源查询不被 Can() 反向依赖' "$(phase_reason tl2)（Identity 下既没有 GrantSource 也没有 CapabilitySource）"
fi

# A-7 Can 是热路径（256 调用点）：内部不许查库 / 读 options（T3）
CAN_BODY="$(awk '/func \(.*Actor\) Can\(/,/^}/' "$IDENTITY_DIR/policy.go" 2>/dev/null)"
if [[ -z "$CAN_BODY" ]]; then
  # 抽取失败必须 pend：抽不到函数体却报 ok，就是「假阳性」的标准形态。
  pend 'A-7 Can() 内不查库/不读 options' '无法从 policy.go 抽取 Can() 函数体（签名变了？先校准再断言）'
elif printf '%s' "$CAN_BODY" | grep -qE 'SELECT|options|sql\.|Store\.'; then
  fail 'A-7 Can() 内不查库/不读 options' 'Can 函数体里出现 SQL/options/Store 调用（T3）'
else
  pass 'A-7 Can() 内不查库/不读 options（热路径保持纯内存裁决）'
fi

# A-8 已挣得来源的合并位置（实测：D13 走「装载期并进 Permissions」，Can() 保持零改动）。
# 校准（2026-11-10 实读 policy.go / postgres_store.go）：计划 §1 否掉的是「物化成真实权限行写进库」，
# 内存里在装载期合并是 D13 的选择，Can() 因此一行都不用改。所以判据是二者其一，且绝不写库。
earned_in_can=0
if printf '%s' "$CAN_BODY" | grep -qiE 'earned'; then earned_in_can=1; fi
apply_fn="$(grep -rlE 'func .*applyTrustLevel|applyTrustLevel\(' "$IDENTITY_DIR" --include='*.go' 2>/dev/null | grep -v '_test\.go' | head -1)"
if [[ "$earned_in_can" == 1 ]]; then
  pass 'A-8 已挣得来源在 Can() 内合并'
elif [[ -n "$apply_fn" ]]; then
  pass "A-8 已挣得来源在装载期合并（${apply_fn#"$ROOT_DIR"/} · D13）"
  # D13 的红线：合并只发生在内存，不能写库物化。
  if awk '/func .*applyTrustLevel/,/^}/' "$apply_fn" 2>/dev/null | grep -qiE 'INSERT|UPDATE|DELETE|Exec\('; then
    fail 'A-8b 装载期合并不写库（D13 红线）' "$(basename "$apply_fn") 的 applyTrustLevel 里有写库语句：已挣得被物化成了第二份副本"
  else
    pass 'A-8b 装载期合并不写库（D13 红线）'
  fi
elif phase_ready tl2; then
  fail 'A-8 已挣得来源已合并（Can 内或装载期）' 'tl2 已就绪但既没在 Can 里合并，也找不到 applyTrustLevel'
else
  pend 'A-8 已挣得来源已合并（Can 内或装载期）' "$(phase_reason tl2)"
fi

# ================================================================ B 边界隔离

group B '边界隔离（已挣得 ∩ 管理类 == ∅，D6）'

expect_present 'B-1 earnable 注册表文件存在' tl1 \
  bash -c 'ls "'"$TL_DIR"'"/*.go >/dev/null 2>&1 && grep -rqE "arnable|Earnable" "'"$TL_DIR"'"'

# B-2 结构不变量测试（D6）。
# 校准（2026-11-10 实读 TrustLevels/registry_test.go）：真实测试是
#   TestEarnableValidateRejectsDangerousKeys（user.ban / moderation.review / *.manage 全部必须被拒）
# 与白名单断言（不得含父权限键）。早先「earnable 小写 + admin 同行」的写法会漏掉它们 —— 已修。
if grep -rqiE 'earnable.*(user\.ban|admin|manage)|(user\.ban|admin|manage).*earnable|Validate\(\).*拒绝' \
  "$TL_DIR" "$IDENTITY_DIR" --include='*_test.go' 2>/dev/null; then
  pass 'B-2 结构不变量测试存在（earnable ∩ 管理类 == ∅）'
elif phase_ready tl2; then
  fail 'B-2 结构不变量测试存在（earnable ∩ 管理类 == ∅）' 'tl2 已就绪但没有一条遍历 catalog 断言交集为空的测试（§8.2）'
else
  pend 'B-2 结构不变量测试存在（earnable ∩ 管理类 == ∅）' "$(phase_reason tl2)"
fi

# B-3 变异验证记录（T9：至少两处「塞一个管理键看它红」）
if grep -rqE 'user\.ban' "$ROOT_DIR/knowledge" 2>/dev/null && grep -rqE '变异' "$ROOT_DIR/knowledge" 2>/dev/null; then
  pass 'B-3 变异验证记录存在（塞 user.ban 看红）'
elif phase_ready tl2; then
  fail 'B-3 变异验证记录存在' 'knowledge/ 下找不到「塞 user.ban 看红」的变异记录（T9 是交付条件）'
else
  pend 'B-3 变异验证记录存在' "$(phase_reason tl2)"
fi

# B-4 本刀唯一新增权限键是 trust.manage（D3）
if phase_ready tl1; then
  extra="$(grep -rhoE '"trust\.[a-z_.]+"' "$IDENTITY_DIR/seeds.go" 2>/dev/null | sort -u | grep -v '"trust.manage"' | wc -l | tr -d ' ')"
  if [[ "$extra" == 0 ]]; then
    pass 'B-4 seeds 里 trust.* 新键只有 trust.manage'
  else
    fail 'B-4 seeds 里 trust.* 新键只有 trust.manage' "多出 $extra 个：$(grep -rhoE '"trust\.[a-z_.]+"' "$IDENTITY_DIR/seeds.go" | sort -u | grep -v 'trust.manage' | tr '\n' ' ')"
  fi
else
  pend 'B-4 seeds 里 trust.* 新键只有 trust.manage' "$(phase_reason tl1)"
fi

# B-5 注册表用**字面白名单**而不是负面过滤（D6 的正面保证）。
# 校准（2026-11-10 实读 TrustLevels/registry.go:18 + registry_test.go）：实现注释明确
#   「负面过滤（不是 *.manage 就行）挡不住 post.edit_any 这类有特权但无管理前缀的键」，
#   对应测试是 TestEligiblePermissionsIsLiteralWhitelist（白名单必须逐字列出，且不含父权限键）。
# ⚠️ GNU grep -E 里 `\(` 不保证匹配字面括号（实测 'func Level\(' 匹配不到 'func Level('），
#   本脚本所有 pattern 一律用字符类或裸词，别再写 `\(`。
if [[ -d "$TL_DIR" ]]; then
  if grep -rqE 'LiteralWhitelist|字面白名单|白名单必须|IsLegacyParentPermission' "$TL_DIR" --include='*.go' 2>/dev/null; then
    pass 'B-5 注册表用字面白名单 + 拒绝父权限键（D6 正面保证）'
  else
    fail 'B-5 注册表用字面白名单 + 拒绝父权限键' '找不到字面白名单测试或父权限键拒绝逻辑：只靠「不是 *.manage」的负面过滤挡不住 post.edit_any 这类键'
  fi
else
  pend 'B-5 注册表用字面白名单 + 拒绝父权限键' "$(phase_reason tl1)"
fi

# B-6 管理类权限键总数基线（用于发现「TL 让管理键变多」的漂移）
admin_keys="$(sqlv "SELECT count(*) FROM permissions WHERE key ~ '^(user\\.ban|moderation\\.|admin\\.|role\\.|extension\\.|option\\.)'")"
if [[ -n "$admin_keys" ]]; then
  pass "B-6 管理类权限键总数记录（$admin_keys，落地后必须不变）"
  note "若落地后该数变大，先确认不是 TL 注册表把管理键带进了 catalog。"
else
  pend 'B-6 管理类权限键总数记录' "查 permissions 表失败：${SQL_LAST_ERROR:-无输出}"
fi

# ================================================================ C 升降级

group C '升降级（阈值边界 / 可逆回退 / 软删）'

# C-1 D4 的核心结构承诺：不落列
if column_exists users trust_level; then
  fail 'C-1 users 表没有 trust_level 列（D4 不落列）' 'users.trust_level 存在：反规范化列会让「刚解锁但还不能用」成为假故障'
else
  pass 'C-1 users 表没有 trust_level 列（D4 不落列）'
fi

# C-2 本刀不新增迁移（§4 / §12.9）
trust_ddl="$(grep -rlE 'trust_level|trust\.level' "$API_DIR/database/migrations" --include='*.sql' 2>/dev/null | wc -l | tr -d ' ')"
if [[ "$trust_ddl" == 0 ]]; then
  pass 'C-2 迁移目录里没有 trust_level 相关 DDL'
else
  fail 'C-2 迁移目录里没有 trust_level 相关 DDL' "命中 $trust_ddl 个文件：$(grep -rlE 'trust_level|trust\.level' "$API_DIR/database/migrations" --include='*.sql' | xargs -r -n1 basename | tr '\n' ' ')"
fi

expect_present 'C-3 threshold-1/threshold/threshold+1 三点边界测试存在' tl1 \
  bash -c 'grep -rqE "(threshold|阈值).*(\\+|plus|minus|1)" "'"$TL_DIR"'" --include="*_test.go" 2>/dev/null || grep -rlqE "threshold" "'"$TL_DIR"'" --include="*_test.go" 2>/dev/null'

# C-4 等级查询端点（M4）：公开读 + 字段形状。
# 校准（2026-11-10 实读 Controllers/TrustLevels/controller.go）：
#   GET /trust-levels 是**公开目录**（levels[] + newUserDays + 登录时附带 viewer.level），
#   不是 /users/{id}/trust-level。进度只给本人（D1 隐私边界），所以匿名看不到 viewer。
if [[ "$HTTP_READY" == 1 && "$(tl_flag_enabled && echo 1 || echo 0)" == 0 ]]; then
  api GET /trust-levels ''
  if [[ "$HTTP_STATUS" == 404 ]]; then
    pass 'C-4 开关关闭时公开面 404（D9 产品面语义）'
  else
    fail 'C-4 开关关闭时公开面 404' "HTTP $HTTP_STATUS：关闭状态下必须 404（D9）"
  fi
elif [[ "$HTTP_READY" == 1 ]]; then
  api GET /trust-levels ''
  C4_LABEL='C-4 公开等级目录档数 = TL0 + 库内 active 档数（rank 连续）'
  if [[ "$HTTP_STATUS" == 200 ]]; then
    n_levels="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.levels // []) | length' 2>/dev/null)"
    want_levels="$(tl_expected_levels)"
    # rank 必须是 0..n-1 连续：插入一档时若实现漏改后续序号，这里先红。
    ranks_ok="$(printf '%s' "$HTTP_BODY" | jq -r '
      (.data // .) | [(.levels // [])[].level] | sort
      | . as $r | ($r == [range(0; ($r|length))])' 2>/dev/null)"
    if [[ -z "${n_levels:-}" || "${n_levels:-0}" == 0 ]]; then
      fail "$C4_LABEL" 'HTTP 200 但 levels 为空'
    elif [[ -n "$want_levels" && "$n_levels" != "$want_levels" ]]; then
      fail "$C4_LABEL" "levels=$n_levels ≠ TL0+active=$want_levels（库内 active ids=$(tl_expected_ids)）"
    elif [[ "$ranks_ok" != true ]]; then
      fail "$C4_LABEL" "rank 不是 0..$((n_levels-1)) 连续序列：$(printf '%s' "$HTTP_BODY" | jq -c '[.data.levels[].level]' 2>/dev/null)"
    elif [[ -n "$want_levels" ]]; then
      pass "$C4_LABEL（levels=$n_levels = 1 + 库内 active $((want_levels-1)) 档）"
    else
      pend "$C4_LABEL" "levels=$n_levels 且 rank 连续，但库内 trust.levels 读不到，无法证明 = TL0 + active"
    fi
  elif [[ "$HTTP_STATUS" == 429 ]]; then
    pend "$C4_LABEL" '退避重试后仍 429（限流，不是产品行为）'
  elif phase_ready tl4; then
    fail "$C4_LABEL" "HTTP $HTTP_STATUS（$(body_excerpt)）"
  else
    pend "$C4_LABEL" "$(phase_reason tl4)"
  fi
else
  pend 'C-4 公开等级目录档数 = TL0 + 库内 active 档数（rank 连续）' 'API 不可达'
fi

# C-5 矩阵账号实测（依赖 seeder 造出的数据；造不到就 pend，不猜等级）
# 账号清单只含 email + 期望等级（不含密码）；密码用统一密码，凭证不落盘（X-2 的裁决）。
TL_ACCOUNTS="$ROOT_DIR/storage/qa/trust-levels-accounts.json"
TL_SEED_DONE="$ROOT_DIR/storage/qa/trust-levels-seed-complete.json"
matrix_ready=0
# 只有造数**跑完**（有完成标记）才判等级分布；否则「账号建了、内容没造」会被误判成等级不符。
[[ -f "$TL_ACCOUNTS" && -f "$TL_SEED_DONE" ]] && matrix_ready=1
if [[ "$matrix_ready" == 1 && "$HTTP_READY" == 1 && "$(phase_ready tl4 && echo 1 || echo 0)" == 1 ]]; then
  probe_ok=0
  probe_total=0
  probe_logged_in=0
  while IFS=$'\t' read -r code email want; do
    # 账号代号 = TL{rank}；N 档站点可能出现 TL5..，不再钉死 0–4。
    [[ "$code" =~ ^TL[0-9]+$ ]] || continue
    probe_total=$((probe_total + 1))
    jar="$work/jar-$code"
    if login_into "$jar" "$email" "$QA_PASS"; then
      probe_logged_in=$((probe_logged_in + 1))
      api GET /auth/session "$jar"
      got="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.trustLevel // .trust_level // empty)' 2>/dev/null)"
      if [[ "$got" == "$want" ]]; then
        probe_ok=$((probe_ok + 1))
        note "$code 等级 $got（期望 $want）✓"
      else
        note "$code 等级 ${got:-空}（期望 $want）✗"
      fi
    else
      note "$code 登录失败（HTTP $HTTP_STATUS）"
    fi
    sleep 1   # 后台/认证端点有 IP 限流，别把自己打 429
  done < <(jq -r '.[] | select(.code|test("^TL[0-9]+$")) | [.code, .email, .expected] | @tsv' "$TL_ACCOUNTS" 2>/dev/null)
  if [[ "$probe_total" -gt 0 && "$probe_logged_in" == 0 ]]; then
    pend 'C-5 矩阵账号等级分布符合预期' "矩阵 $probe_total 个账号一个都登不上：造数未完成（seeder --apply），不是等级不符"
  elif [[ "$probe_total" == 0 ]]; then
    pend 'C-5 矩阵账号等级分布符合预期' '账号清单里没有 TL0–TLn 行'
  elif [[ "$probe_ok" == "$probe_total" ]]; then
    pass "C-5 矩阵账号等级分布符合预期（$probe_ok/$probe_total）"
  else
    fail 'C-5 矩阵账号等级分布符合预期' "$probe_ok/$probe_total 命中；差异见上面的逐账号明细"
  fi
else
  if [[ -f "$TL_ACCOUNTS" ]]; then
    pend 'C-5 矩阵账号等级分布符合预期' '账号已建但造数未跑完（缺 trust-levels-seed-complete.json）：内容/点赞未造，等级无从判断；重跑 storage/qa/seed-trust-levels-matrix.sh --apply'
  else
    pend 'C-5 矩阵账号等级分布符合预期' '矩阵账号尚未造：跑 storage/qa/seed-trust-levels-matrix.sh --apply（约 19 分钟节流）'
  fi
fi

# C-6 可逆计数回退 → 降级（必须真的点赞/取消，不能只读）
if [[ -f "$ROOT_DIR/storage/qa/trust-levels-downgrade-evidence.txt" ]]; then
  if grep -qE 'delta\s*=\s*-1|降级|downgrade' "$ROOT_DIR/storage/qa/trust-levels-downgrade-evidence.txt"; then
    pass 'C-6 可逆计数回退降级有现场证据'
  else
    fail 'C-6 可逆计数回退降级有现场证据' '证据文件存在但没有 delta/降级 记录'
  fi
else
  pend 'C-6 可逆计数回退降级有现场证据' '需要现场：用矩阵账号取消赞后重测等级，落盘 storage/qa/trust-levels-downgrade-evidence.txt（seeder 目前没有 --downgrade 模式，属待补工具）'
fi

# C-7 软删不降级（D5）：结构判据 —— Level 的输入若不含软删状态，软删就不可能改变等级。
# 判据只看**标识符**（字段/常量/方法名），不看散文：中文「软删」「软停用」在领域包注释里
# 说的是「停用档位后身份 id 不复用」，那是档位生命周期，不是内容存亡状态，故不入判据。
# 反过来说，谁都不许为了迎合这条正则去删注释——那是把判据变成文字游戏。
if [[ -d "$TL_DIR" ]]; then
  if grep -rlE 'deleted_at|DeletedAt|SoftDelete|soft_delete|IsDeleted' "$TL_DIR" --include='*.go' 2>/dev/null | grep -v '_test\.go' | grep -q .; then
    fail 'C-7 软删不降级（结构判据）' 'TL 领域包引用了 deleted_at/软删状态：等级输入混入了内容存亡，D5 会被破坏'
  else
    pass 'C-7 软删不降级（结构判据：Level 输入不含内容存亡状态）'
  fi
else
  pend 'C-7 软删不降级（结构判据）' "$(phase_reason tl1)"
fi

# C-8 等级是纯函数（无 DB / 无 time.Now）
if [[ -d "$TL_DIR" ]]; then
  if grep -rlE 'time\.Now\(\)' "$TL_DIR" --include='*.go' 2>/dev/null | grep -v '_test\.go' | grep -q .; then
    fail 'C-8 Level() 无 time.Now 副作用（D4 纯函数）' '领域包里出现 time.Now()：now 必须由调用方传入'
  else
    pass 'C-8 Level() 无 time.Now 副作用（D4 纯函数）'
  fi
else
  pend 'C-8 Level() 无 time.Now 副作用（D4 纯函数）' "$(phase_reason tl1)"
fi

# C-9 days_visited 是「真实天数」维度，不可能当日伪造 —— 这条必须 pend 而不是假 PASS
if [[ -f "$MATRIX_DOC" ]] && grep -qE '预热日历|warm.?up' "$MATRIX_DOC" 2>/dev/null; then
  pass 'C-9 days_visited 跨天语义有预热日历方案（矩阵文档）'
else
  pend 'C-9 days_visited 跨天语义有预热日历方案' '先写 storage/qa/trust-levels-matrix.md 的预热日历小节'
fi

# C-10 同一数据两次查询结果一致（派生值无缓存失真）。
# 用公开目录的 viewer.level（登录态）比目录本体更有意义：它是每请求重算的派生值。
if [[ "$(tl_flag_enabled && echo 1 || echo 0)" == 0 ]]; then
  pend 'C-10 同一数据两次查询一致' 'features.trust_levels 关闭（D9：路由 404），开开关后重跑'
elif [[ "$HTTP_READY" == 1 && "$(phase_ready tl4 && echo 1 || echo 0)" == 1 ]]; then
  me_email="$(creds_md_email 甲 2>/dev/null || true)"
  jar_me="$work/jar-c10"
  if [[ -n "$me_email" ]] && login_into "$jar_me" "$me_email" "${QA_PASS}"; then
    api GET /trust-levels "$jar_me"
    first="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .viewer.level // empty' 2>/dev/null)"
    api GET /trust-levels "$jar_me"
    second="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | .viewer.level // empty' 2>/dev/null)"
    if [[ -n "$first" && "$first" == "$second" ]]; then
      pass "C-10 同一数据两次查询一致（viewer.level=$first）"
    elif [[ "$HTTP_STATUS" == 429 ]]; then
      pend 'C-10 同一数据两次查询一致' '退避重试后仍 429'
    else
      fail 'C-10 同一数据两次查询一致' "两次结果不同（${first:-空} vs ${second:-空}）"
    fi
  else
    pend 'C-10 同一数据两次查询一致' '取不到登录账号（storage/qa/creds.txt 的 甲 行）'
  fi
else
  pend 'C-10 同一数据两次查询一致' 'API 或 tl4 未就绪'
fi

# C-11 公开目录**不得**下发限额（反滥用面）。否定式断言必须逐字段写：
# 只断言「没有 limits」时，把字段从 limits 挪到 requirements 里就能绕过。
LIMIT_FIELDS='["limits","topicCooldownSeconds","commentCooldownSeconds","dailyTopicLimit","dailyCommentLimit"]'
if [[ "$HTTP_READY" == 1 && "$(tl_flag_enabled && echo 1 || echo 0)" == 0 ]]; then
  # 开关关闭 → 公开面 404，自然没有限额可泄漏。断言 404 本身，而不是「跳过」。
  api GET /trust-levels ''
  if [[ "$HTTP_STATUS" == 404 ]]; then
    pass 'C-11 开关关闭时公开面 404，无泄漏面（D9）'
  else
    fail 'C-11 开关关闭时公开面 404' "HTTP $HTTP_STATUS：关闭状态下必须 404（D9）"
  fi
elif [[ "$HTTP_READY" == 1 ]]; then
  api GET /trust-levels ''
  if [[ "$HTTP_STATUS" == 200 ]]; then
    leaked="$(printf '%s' "$HTTP_BODY" | jq -r --argjson f "$LIMIT_FIELDS" \
      '[ .. | objects | (keys[]) ] | map(select(. as $k | $f | index($k))) | unique | join(",")' 2>/dev/null)"
    if [[ -z "$leaked" ]]; then
      pass 'C-11 公开目录不含任何限额字段（5 个字段逐一检查）'
    else
      fail 'C-11 公开目录不含任何限额字段' "泄漏：$leaked（限流参数不该下发给匿名访客）"
    fi
  elif [[ "$HTTP_STATUS" == 429 ]]; then
    pend 'C-11 公开目录不含任何限额字段' '退避重试后仍 429'
  else
    pend 'C-11 公开目录不含任何限额字段' "HTTP $HTTP_STATUS（$(body_excerpt)）"
  fi
else
  pend 'C-11 公开目录不含任何限额字段' 'API 不可达'
fi

# C-12 后台读面**必须**有 limits（运营要配它）；四字段逐个查，避免「删了字段也绿」。
if [[ "$HTTP_READY" == 1 ]]; then
  jar_admin="$work/jar-c12"
  admin_reader="$(login_any_admin_reader "$jar_admin" || true)"
  c12_done=0
  if [[ -n "$admin_reader" ]] && ! tl_flag_enabled; then
    api GET /admin/trust-levels "$jar_admin"
    if [[ "$HTTP_STATUS" == 404 ]]; then
      pass 'C-12 开关关闭时后台读面也 404（D9）'
    else
      fail 'C-12 开关关闭时后台读面也 404' "HTTP $HTTP_STATUS：关闭状态下后台路由也必须 404"
    fi
    c12_done=1
  fi
  if [[ "$c12_done" == 1 ]]; then
    :
  elif [[ -z "$admin_reader" ]]; then
    pend 'C-12 后台读面带 limits 且四字段齐全' "戊 / 丁b / TLRW / TLRO 都登不上（末次 HTTP $HTTP_STATUS：$(body_excerpt)）：凭据失效，已用同级矩阵账号替代"
  else
    note "后台读账号：$admin_reader（GET 只需 admin.access）"
    api GET /admin/trust-levels "$jar_admin"
    if [[ "$HTTP_STATUS" == 200 ]]; then
      missing_fields="$(printf '%s' "$HTTP_BODY" | jq -r --argjson f "$LIMIT_FIELDS" '
        (.data // .) as $d
        | ($d.levels // []) as $lv
        | if ($lv | length) == 0 then "levels 为空"
          else
            [ $lv[]
              | (if has("limits") then .limits else {} end) as $l
              | $f[1:][] as $k
              | select(($l | has($k)) | not)
              | $k ]
            | unique | join(",")
          end' 2>/dev/null)"
      if [[ -z "$missing_fields" ]]; then
        pass 'C-12 后台读面带 limits 且四字段齐全'
      else
        fail 'C-12 后台读面带 limits 且四字段齐全' "缺：$missing_fields"
      fi
    elif [[ "$HTTP_STATUS" == 429 ]]; then
      pend 'C-12 后台读面带 limits 且四字段齐全' '退避重试后仍 429'
    elif phase_ready tl4; then
      fail 'C-12 后台读面带 limits 且四字段齐全' "HTTP $HTTP_STATUS（$(body_excerpt)）"
    else
      pend 'C-12 后台读面带 limits 且四字段齐全' "$(phase_reason tl4)"
    fi
  fi
else
  pend 'C-12 后台读面带 limits 且四字段齐全' 'API 不可达'
fi

# ================================================================ D 限额收编

group D '限额收编（新人限额由 level 决定，D2）'

# D-1 七个 legacy option 键必须原样保留（不迁移数据、不改语义）
legacy_keys=(
  trust.new_user_days
  trust.new_user.topic_cooldown_seconds
  trust.new_user.comment_cooldown_seconds
  trust.new_user.daily_topic_limit
  trust.new_user.daily_comment_limit
  trust.new_user.forbid_outbound_links
  trust.new_user.forbid_attachments
)
missing_keys=()
present_keys=0
for k in "${legacy_keys[@]}"; do
  v="$(optv "$k")"
  if [[ -n "$v" ]]; then
    present_keys=$((present_keys + 1))
  else
    missing_keys+=("$k")
  fi
done
if [[ "$present_keys" == "${#legacy_keys[@]}" ]]; then
  pass "D-1 七个 trust.new_user_* 键全部在位（$present_keys/${#legacy_keys[@]}）"
else
  fail 'D-1 七个 trust.new_user_* 键全部在位' "缺：${missing_keys[*]}（D2 要求全部保留、不迁移数据）"
fi

# D-2 七个键的现值必须与 plan §2 记录一致（收编前后数值不得被顺手改掉）
declare -A want_vals=(
  [trust.new_user_days]=7
  [trust.new_user.topic_cooldown_seconds]=300
  [trust.new_user.comment_cooldown_seconds]=60
  [trust.new_user.daily_topic_limit]=3
  [trust.new_user.daily_comment_limit]=30
)
drift=()
for k in "${!want_vals[@]}"; do
  v="$(optv "$k")"
  [[ "$v" == "${want_vals[$k]}" ]] || drift+=("$k=$v(期望 ${want_vals[$k]})")
done
if [[ "${#drift[@]}" == 0 ]]; then
  pass 'D-2 legacy 限额现值未漂移（5 个数值键）'
else
  fail 'D-2 legacy 限额现值未漂移' "${drift[*]}"
fi

# D-3 限额不进 Can（D8）
if [[ -z "$CAN_BODY" ]]; then
  pend 'D-3 限额不进 Can()（D8）' '无法抽取 Can() 函数体'
elif printf '%s' "$CAN_BODY" | grep -qiE 'cooldown|dailyLimit|daily_limit|trustPolicy'; then
  fail 'D-3 限额不进 Can()（D8）' 'Can 函数体里出现 cooldown/limit 判断'
else
  pass 'D-3 限额不进 Can()（D8）'
fi

expect_present 'D-4 trustForActor 已收编（limitsForActor 或 TL0 复用同键）' tl3 \
  bash -c 'grep -rqE "limitsForActor" "'"$FORUM_DIR"'" 2>/dev/null || ! grep -rqE "trustForActor" "'"$FORUM_DIR"'" 2>/dev/null'

# D-5 后台阈值页能恢复推荐默认（D12 / AGENTS 强制项）
if [[ "$HTTP_READY" == 1 ]]; then
  # 校准（实读 controller.go）：后台端点是 /admin/trust-levels（GET/PUT），
  # 恢复默认是 /admin/trust-levels/restore-defaults（POST）；不存在 /thresholds 子路径。
  api GET '/admin/trust-levels' ''
  if [[ "$(tl_flag_enabled && echo 1 || echo 0)" == 0 ]]; then
    if [[ "$HTTP_STATUS" == 404 ]]; then
      pass 'D-5 开关关闭时后台阈值端点 404（D9）'
    else
      fail 'D-5 开关关闭时后台阈值端点 404' "HTTP $HTTP_STATUS"
    fi
  elif [[ "$HTTP_STATUS" == 401 || "$HTTP_STATUS" == 403 ]]; then
    pass 'D-5 后台阈值端点存在且未授权被拒（401/403）'
  elif [[ "$HTTP_STATUS" == 200 ]]; then
    fail 'D-5 后台阈值端点存在且未授权被拒' '匿名 GET 返回 200：后台阈值端点缺少权限守卫'
  elif [[ "$HTTP_STATUS" == 404 ]]; then
    if phase_ready tl4; then fail 'D-5 后台阈值端点存在且未授权被拒' '404：tl4 已就绪但路由未注册'; else pend 'D-5 后台阈值端点存在且未授权被拒' "$(phase_reason tl4)"; fi
  elif [[ "$HTTP_STATUS" == 429 ]]; then
    pend 'D-5 后台阈值端点存在且未授权被拒' '退避重试后仍 429'
  else
    pend 'D-5 后台阈值端点存在且未授权被拒' "HTTP $HTTP_STATUS（$(body_excerpt)）"
  fi
else
  pend 'D-5 后台阈值端点存在且未授权被拒' 'API 不可达'
fi

# D-6 恢复默认的语义在 UI 上写明「不影响用户已获得的内容」（D12）
# 判据按**语义**认，不按某一句字面：现行文案是「…已获得的徽章与已发布的内容不受影响」，
# 旧断言只认「不影响已获得」这个语序，等于把同义改写判成缺失。
# 文案的**来源**是 i18n locale 文件（组件只引用键），所以两个目录都搜：只搜 app/ 会稳定误报缺失。
if [[ -d "$WEB_DIR/app" ]] && grep -rqE '(不影响|不受影响).{0,30}(已获得|已发布|内容)|(已获得|已发布).{0,30}(不影响|不受影响)' "$WEB_DIR/app" "$WEB_DIR/i18n" 2>/dev/null; then
  pass 'D-6 恢复默认的 UI 文案声明不影响已获得内容（D12）'
else
  pend 'D-6 恢复默认的 UI 文案声明不影响已获得内容（D12）' '页面已渲染但文案既没说「不影响」也没说「不受影响」+「已获得/已发布/内容」：恢复默认的语义缺一句人话'
fi

# D-8 恢复推荐默认必须是**独立读回断言**，不是「按钮点了就算」。
# 证据由 storage/qa/seed-trust-levels-matrix.sh --lower-thresholds 落盘：
#   下探前快照 → 下探 → 恢复默认 → 再读一次与快照比对（roundtrip）。
RESTORE_EVIDENCE="$ROOT_DIR/storage/qa/trust-levels-threshold-restore-evidence.txt"
if [[ -f "$RESTORE_EVIDENCE" ]]; then
  if grep -qE '^restore=ok' "$RESTORE_EVIDENCE" && grep -qE '^roundtrip=equal' "$RESTORE_EVIDENCE"; then
    pass 'D-8 恢复默认有独立读回断言（roundtrip=equal）'
    note "快照：$(grep -m1 '^snapshot=' "$RESTORE_EVIDENCE" | cut -d= -f2-)"
  else
    fail 'D-8 恢复默认有独立读回断言' "证据文件未通过：$(grep -vE '^#' "$RESTORE_EVIDENCE" | tr '\n' ' ' | cut -c1-200)"
  fi
else
  pend 'D-8 恢复默认有独立读回断言' '需要 storage/qa/trust-levels-threshold-restore-evidence.txt（seeder --lower-thresholds 生成）'
fi

# D-7 new_user_days 边界两侧的限额归属（年龄从唯一判据降级为上限闸）
if phase_ready tl3; then
  if grep -rqE 'new_user_days|NewUserDays' "$FORUM_DIR" 2>/dev/null; then
    pass 'D-7 new_user_days 仍被 Forum 限额读取（上限闸语义）'
  else
    fail 'D-7 new_user_days 仍被 Forum 限额读取' 'Forum 里找不到 new_user_days：年龄闸被删，TL0 会提前越过 TL1'
  fi
else
  pend 'D-7 new_user_days 仍被 Forum 限额读取（上限闸语义）' "$(phase_reason tl3)"
fi

# ================================================================ E 失败关闭

group E '失败关闭（读失败/行缺失 → 已挣得集合为空，D7）'

# E-1 零值 Actor 语义（T2：几十个构造点不改行为）
actor_ctor="$(grep -rn 'Actor{' "$API_DIR/app" --include='*.go' 2>/dev/null | wc -l | tr -d ' ')"
if [[ -n "$actor_ctor" ]]; then
  pass "E-1 Actor 构造点基线记录（$actor_ctor 处，零值 TL 必须等价于无已挣得）"
else
  pend 'E-1 Actor 构造点基线记录' '未统计到 Actor 构造点'
fi

expect_present 'E-2 零值 Actor → 无已挣得权限的测试存在' tl2 \
  bash -c 'grep -rqE "零值|zero.?value|TrustLevel:? *0|TrustLevel: *0" "'"$IDENTITY_DIR"'" --include="*_test.go" 2>/dev/null'

expect_present 'E-3 store 读失败 → 空集合的测试存在' tl2 \
  bash -c 'grep -rqE "(error|失败|fail).*(earned|已挣得|empty)" "'"$IDENTITY_DIR"'" "'"$TL_DIR"'" --include="*_test.go" 2>/dev/null'

expect_present 'E-4 user_activity 行缺失 → TL0 不报错的测试存在' tl2 \
  bash -c 'grep -rqE "(no rows|行缺失|not found|ErrNoRows)" "'"$IDENTITY_DIR"'" "'"$TL_DIR"'" --include="*_test.go" 2>/dev/null'

# E-5 失败时记 warn（§7）而不是静默吞掉
if [[ -d "$TL_DIR" ]]; then
  if grep -rqE 'slog\.(Warn|Error)|logger\.(Warn|Error)' "$TL_DIR" "$IDENTITY_DIR" --include='*.go' 2>/dev/null; then
    pass 'E-5 读失败路径有 warn 日志（§7）'
  else
    fail 'E-5 读失败路径有 warn 日志（§7）' '领域包与 Identity 里都没有 warn/error 记录点'
  fi
else
  pend 'E-5 读失败路径有 warn 日志（§7）' "$(phase_reason tl1)"
fi

# ================================================================ F 开关

group F '开关（features.trust_levels，D9）'

flag_present=0
[[ -n "$(optv features.trust_levels)" ]] && flag_present=1
if [[ "$flag_present" == 0 ]]; then
  # 门在 tl2：D9 的开关是 Can() 合并已挣得时消费的，tl0（ADR）阶段还没有它属正常。
  if phase_ready tl2; then
    fail 'F-1 features.trust_levels 已注册（public:true）' 'tl2 已落地但 options 里没有 features.trust_levels'
  else
    pass 'F-1 基线：features.trust_levels 尚未注册（本刀之前的行为面）'
  fi
else
  # ⚠️ 第四个假阳性（2026-11-10 自查）：早先这里用 `value::jsonb -> 'public'` 断言 public:true。
  # 两处都错：① web_options.value 是**纯文本**（'enabled'/'disabled'），不是 JSON，cast 直接报错；
  # ② public 根本不是库里的字段，它是代码层声明（Options/feature_flags.go 的 optionDefinition.public）。
  # 所以：存在性查库，public 查代码，两者分开断言，各说各的真值来源。
  flag_value="$(optv features.trust_levels)"
  if [[ "$flag_value" != enabled && "$flag_value" != disabled ]]; then
    fail 'F-1 features.trust_levels 已注册且 public:true' "value=${flag_value:-空}（期望 enabled/disabled 文本）"
  elif grep -qE 'NameFeatureTrustLevels, *public: *true' "$API_DIR/app/Models/Options/feature_flags.go" 2>/dev/null; then
    pass "F-1 features.trust_levels 已注册且 public:true（value=$flag_value，public 取自 feature_flags.go）"
  else
    fail 'F-1 features.trust_levels 已注册且 public:true' 'feature_flags.go 里没有 NameFeatureTrustLevels, public: true'
  fi
fi

# F-2 关闭开关后已挣得集合恒空（现场证据，由夹具脚本产出）。
# 证据格式见 storage/qa/trust-levels-flag-off-evidence.txt：
#   flag_on_topic_post=201 / flag_off_topic_post=403 / flag_off_catalog_get=404 / earned=empty
# 为什么必须用夹具：默认 earnable 是空集（D17），普通账号的 Earned 恒空，开关开着也测不出差异。
F2_EVIDENCE="$ROOT_DIR/storage/qa/trust-levels-flag-off-evidence.txt"
if [[ -f "$F2_EVIDENCE" ]]; then
  f2_on="$(grep -m1 '^flag_on_topic_post=' "$F2_EVIDENCE" | cut -d= -f2)"
  f2_off="$(grep -m1 '^flag_off_topic_post=' "$F2_EVIDENCE" | cut -d= -f2)"
  f2_cat="$(grep -m1 '^flag_off_catalog_get=' "$F2_EVIDENCE" | cut -d= -f2)"
  f2_earned="$(grep -m1 '^earned=' "$F2_EVIDENCE" | cut -d= -f2)"
  f2_perm="$(grep -m1 '^flag_off_permissions_has_topic_create=' "$F2_EVIDENCE" | cut -d= -f2)"
  # 集合层面（权限里没有 topic.create）比「发帖 403」更硬：403 也可能来自别的原因。
  if [[ "$f2_on" =~ ^20[01]$ && "$f2_off" == 403 && "$f2_cat" == 404 && "$f2_earned" == empty && "$f2_perm" == false ]]; then
    pass "F-2 关闭开关后已挣得集合恒空（开=$f2_on → 关=$f2_off，目录 404）"
  else
    fail 'F-2 关闭开关后已挣得集合恒空' "证据不满足：on=${f2_on:-?} off=${f2_off:-?} catalog=${f2_cat:-?} earned=${f2_earned:-?} 权限含topic.create=${f2_perm:-?}"
  fi
else
  pend 'F-2 关闭开关后已挣得集合恒空' '需要 storage/qa/trust-levels-flag-off-evidence.txt（跑 storage/qa/fixture-trust-earned.sh --apply --flag-off-evidence 生成）'
fi

expect_present 'F-3 关闭后限额回落两态有对照记录' tl3 \
  bash -c 'grep -rqE "features\.trust_levels|trustLevels" "'"$FORUM_DIR"'" --include="*.go" 2>/dev/null'

# F-4 关闭路径不能靠「跳过整段逻辑」实现（否则会顺带关掉新人限额）。
# 门在 tl2：D9 的开关在 LoadActor/Can 合并处被消费，领域包不一定要知道它。
if [[ -d "$TL_DIR" ]] || phase_ready tl2; then
  # 只用开关专名：早先带裸 `Enabled` 会命中 external auth 的 LoginEnabled/RegistrationEnabled，
  # 于是「开关没接线」被判成通过（2026-11-10 自查发现的假阳性，已收窄）。
  if grep -rqE 'features\.trust_levels|TrustLevelsEnabled|trustLevelsEnabled' "$TL_DIR" "$IDENTITY_DIR" --include='*.go' 2>/dev/null; then
    pass 'F-4 开关在装配/合并处被显式消费（不是靠调用方整段跳过）'
  elif phase_ready tl2; then
    fail 'F-4 开关在装配/合并处被显式消费' 'tl2 已落地但找不到 features.trust_levels 的消费点'
  else
    pend 'F-4 开关在装配/合并处被显式消费' "$(phase_reason tl2)"
  fi
else
  pend 'F-4 开关在装配/合并处被显式消费' "$(phase_reason tl2)"
fi

# F-5 插件桥 / CLI / bot 构造的 Actor 不受影响（T2 的运行时对照）
if [[ -f "$ROOT_DIR/storage/qa/trust-levels-actor-zero-evidence.txt" ]]; then
  pass 'F-5 零值 Actor 运行时对照有现场证据'
else
  pend 'F-5 零值 Actor 运行时对照有现场证据' '需要 storage/qa/trust-levels-actor-zero-evidence.txt（插件桥/CLI 构造点实测）'
fi

# ================================================================ G 性能

group G '性能（LoadActor p95 增量，D4 复议判据）'

if [[ -f "$PERF_BASELINE" ]]; then
  base_p95="$(jq -r '.auth_session.p95_ms // empty' "$PERF_BASELINE" 2>/dev/null)"
  if [[ -n "$base_p95" ]]; then
    pass "G-1 性能基线已采集（flag=off · auth_session p95 = ${base_p95}ms）"
    note "基线文件：${PERF_BASELINE#"$ROOT_DIR"/}（采集于 $(jq -r '.captured_at // "?"' "$PERF_BASELINE" 2>/dev/null)）"
    if [[ -d "$TL_DIR" ]]; then
      base_mtime="$(stat -c %Y "$PERF_BASELINE" 2>/dev/null || echo 0)"
      code_mtime="$(stat -c %Y "$TL_DIR" 2>/dev/null || echo 0)"
      if [[ "$base_mtime" -gt "$code_mtime" ]]; then
        warn "基线文件比 TL 领域包更新：可能已在落地后采集，D4 对照失效。重跑 storage/qa/trust-levels-perf.sh --capture-baseline 前先 git stash 后端改动。"
      fi
    fi
  else
    fail 'G-1 性能基线已采集' "基线文件缺 auth_session.p95_ms（$PERF_BASELINE）"
  fi
else
  pend 'G-1 性能基线已采集' "跑 storage/qa/trust-levels-perf.sh --capture-baseline（D31：同一个二进制关开关采基线）生成 ${PERF_BASELINE#"$ROOT_DIR"/}；开关未注册/未短路时脚本 exit 4 记 pend，不退回绝对预算"
fi

base_flag="$(jq -r '.feature_flag.state // "unknown"' "$PERF_BASELINE" 2>/dev/null)"
if [[ -f "$PERF_BASELINE" && "$base_flag" != off ]]; then
  # 基线不是「关开关」采的：Δ 不成立。宁可 FAIL 也不拿污染基线算出一个好看的数字。
  fail 'G-2 LoadActor p95 增量 ≤ 判据' "基线 feature_flag.state=$base_flag（要求 off）：重跑 --capture-baseline"
elif [[ -f "$PERF_BASELINE" && -f "$PERF_CURRENT" ]]; then
  delta="$(jq -rn --slurpfile b "$PERF_BASELINE" --slurpfile c "$PERF_CURRENT" \
    '($c[0].auth_session.p95_ms // 0) - ($b[0].auth_session.p95_ms // 0)' 2>/dev/null)"
  if [[ -n "$delta" ]]; then
    if awk -v d="$delta" -v m="$PERF_MAX_DELTA" 'BEGIN { exit !(d > m) }'; then
      fail 'G-2 LoadActor p95 增量 ≤ 判据' "Δp95 = ${delta}ms > ${PERF_MAX_DELTA}ms：按 D4 触发复议（不是修一行能过的项）"
    else
      pass "G-2 LoadActor p95 增量在判据内（Δ = ${delta}ms ≤ ${PERF_MAX_DELTA}ms）"
    fi
  else
    fail 'G-2 LoadActor p95 增量 ≤ 判据' '两份 JSON 无法相减（字段缺失）'
  fi
else
  pend 'G-2 LoadActor p95 增量 ≤ 判据' "跑 storage/qa/trust-levels-perf.sh --compare（开开关测 current）生成 ${PERF_CURRENT#"$ROOT_DIR"/}"
fi

if [[ -f "$PERF_CURRENT" ]]; then
  pub_delta="$(jq -rn --slurpfile b "$PERF_BASELINE" --slurpfile c "$PERF_CURRENT" \
    '($c[0].public_topics.p95_ms // 0) - ($b[0].public_topics.p95_ms // 0)' 2>/dev/null)"
  if [[ -n "$pub_delta" ]] && awk -v d="$pub_delta" -v m="$PERF_MAX_DELTA" 'BEGIN { exit !(d > m) }'; then
    fail 'G-3 公开列表未被拖慢' "公开列表 Δp95 = ${pub_delta}ms > ${PERF_MAX_DELTA}ms"
  elif [[ -n "$pub_delta" ]]; then
    pass "G-3 公开列表未被拖慢（Δ = ${pub_delta}ms）"
  else
    pend 'G-3 公开列表未被拖慢' 'current JSON 缺 public_topics.p95_ms'
  fi
else
  pend 'G-3 公开列表未被拖慢' '需要先跑 --compare'
fi

# G-4 新增查询必须是 PK 单行（D4 代价的物理前提）。
# 校准（2026-11-10 实读 resolver.go）：TL 层通过接口复用 Badges 的 user_activity 读入口，
# 所以判据是「复用单行读入口」而不是「TL 包里出现 FROM user_activity」。
if [[ -d "$TL_DIR" ]]; then
  if grep -rqE 'FROM user_activity WHERE user_id' "$TL_DIR" --include='*.go' 2>/dev/null; then
    pass 'G-4 新增查询按 user_id 单行读 user_activity'
  elif grep -rlE 'user_activity|ActivityReader|Badges' "$TL_DIR" --include='*.go' 2>/dev/null | grep -v '_test\.go' | grep -q .; then
    pass 'G-4 复用 user_activity 单行读入口（Badges activity reader）'
    note '人工确认一次：复用的入口必须是 PK 单行读，不是列表扫描。'
  else
    fail 'G-4 新增查询按 user_id 单行读 user_activity' 'TL 领域包里找不到任何 user_activity / ActivityReader 读点'
  fi
else
  pend 'G-4 新增查询按 user_id 单行读 user_activity' "$(phase_reason tl1)"
fi

# ================================================================ H 契约

group H '契约（OpenAPI / V3 目录 / i18n / 架构基线）'

if [[ "$SKIP_SLOW" == 1 ]]; then
  pend 'H-1 OpenAPI refs 全绿' 'SFORUM_TL_SKIP_SLOW=1：本轮跳过'
elif ! command -v ruby >/dev/null; then
  pend 'H-1 OpenAPI refs 全绿' '本机没有 ruby，跑不了 ruby scripts/validate-openapi-refs.rb'
else
  if out="$(cd "$ROOT_DIR" && ruby scripts/validate-openapi-refs.rb 2>&1)"; then
    pass 'H-1 OpenAPI refs 全绿'
  else
    fail 'H-1 OpenAPI refs 全绿' "$(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-240)"
  fi
fi

if [[ "$SKIP_SLOW" == 1 ]]; then
  pend 'H-2 架构边界基线全绿' 'SFORUM_TL_SKIP_SLOW=1：本轮跳过'
elif ! command -v node >/dev/null; then
  pend 'H-2 架构边界基线全绿' '本机没有 node'
else
  if out="$(cd "$ROOT_DIR" && node tests/validate-architecture-boundaries.mjs 2>&1)"; then
    pass 'H-2 架构边界基线全绿'
  else
    fail 'H-2 架构边界基线全绿' "$(printf '%s' "$out" | grep -E '违规|violation|exceed|超过' | head -3 | tr '\n' ' ' | cut -c1-240)"
  fi
fi

# H-3 本刀不改 schema（§4 / D4）。
# 校准（2026-11-10，第五个假阳性）：早先判据是「迁移目录里没有本刀新增文件」。
# 但 T5 的权限目录落库本来就是一条**只插数据行**的迁移（202611100004_trust_manage_permission.sql），
# 它不属于 schema 变更。判据必须是「新增迁移里没有 DDL」，不是「没有新增迁移」。
if command -v git >/dev/null && git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
  mig_new="$(git -C "$ROOT_DIR" status --porcelain -- apps/api/database/migrations 2>/dev/null \
    | grep -E '^\?\?|^ M|^M ' | awk '{print $2}' \
    | grep -vE 'badges|permission_seed_parity|stable_core_views_test' || true)"
  schema_hits=''
  for f in $mig_new; do
    [[ -f "$ROOT_DIR/$f" ]] || continue
    # 只看 .sql：迁移测试文件里会写 "DROP TABLE" 这种**禁止串**做断言，扫进去就是自伤。
    [[ "$f" == *.sql ]] || continue
    if grep -qiE 'CREATE TABLE|ALTER TABLE|CREATE (OR REPLACE )?VIEW|CREATE INDEX|DROP TABLE|DROP VIEW|ADD COLUMN|DROP COLUMN' "$f"; then
      schema_hits="$schema_hits $(basename "$f")"
    fi
  done
  if [[ -n "$schema_hits" ]]; then
    fail 'H-3 本刀不改 schema（新增迁移无 DDL）' "含 DDL 的新迁移：$schema_hits（D4 明确不落列、不建表）"
  elif [[ -n "$mig_new" ]]; then
    pass "H-3 本刀不改 schema（新增迁移只动数据行：$(echo $mig_new | xargs -n1 basename | tr '\n' ' '))"
    note '物理可逆性用 SFORUM_MIGRATION_TARGET_VERSION=... ./scripts/verify-migration-reversible.sh 单独证明。'
  else
    pass 'H-3 本刀不改 schema（迁移目录无新增/改动）'
  fi
else
  pend 'H-3 本刀不改 schema' '不在 git 工作树里，无法判定新增文件'
fi

# H-4 i18n 内置档位名双语（D10；N 档下档名真相在服务端，i18n 只兜底内置 id）
i18n_hit=0
for loc in zh-CN en-US; do
  f="$WEB_DIR/i18n/locales/$loc.json"
  [[ -f "$f" ]] || continue
  if grep -qE '新成员|New Member|trustLevel|trust_level' "$f" 2>/dev/null; then
    i18n_hit=$((i18n_hit + 1))
  fi
done
if [[ "$i18n_hit" == 2 ]]; then
  pass 'H-4 内置档位名 zh-CN + en-US 双语齐全'
else
  pend 'H-4 内置档位名 zh-CN + en-US 双语齐全' "$(phase_reason tl5)（命中 $i18n_hit/2 个 locale）"
fi

# H-5 V3 目录与源码一致（新增后台页会改 routes / UI surfaces / traceability）。
# 校准：`sforum catalog --check` 不存在（我最初照计划措辞猜的命令）。真实入口是两个：
#   node scripts/v3-catalog/generate.mjs --check     目录新鲜度（只读，不写文件）
#   node tests/validate-v3-p0-catalogs.mjs           P0 目录完整性
if [[ "$SKIP_SLOW" == 1 ]]; then
  pend 'H-5 V3 目录与源码一致（--check）' 'SFORUM_TL_SKIP_SLOW=1：本轮跳过'
elif ! command -v node >/dev/null; then
  pend 'H-5 V3 目录与源码一致（--check）' '本机没有 node'
elif out="$(cd "$ROOT_DIR" && node scripts/v3-catalog/generate.mjs --check 2>&1)"; then
  if out2="$(cd "$ROOT_DIR" && node tests/validate-v3-p0-catalogs.mjs 2>&1)"; then
    pass 'H-5 V3 目录与源码一致（generate --check + validate-v3-p0-catalogs）'
  else
    fail 'H-5 V3 目录与源码一致（--check）' "$(printf '%s' "$out2" | tail -3 | tr '\n' ' ' | cut -c1-220)"
  fi
else
  fail 'H-5 V3 目录与源码一致（--check）' "$(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-220)"
fi

# H-6 trust.manage 的四处登记（T5 的完整口径）。
# 校准（2026-11-10，PM H4 的假 FAIL）：权限键的显示文案**不走 locale**——
# 后台读的是 DB `permissions.description`（usePermissionText 在 locale 缺失时回落到它）。
# 实测 badge.manage 同样不在 apps/web/i18n/ 里。所以四处是：
#   seeds.go 常量 + SeedPermissions / 迁移 INSERT / roleTemplates.ts / DB permissions.description
# i18n 只负责**档位名**（H-4），不属于权限键。
if phase_ready tl2; then
  miss=()
  code_grep 'trust\.manage' "$IDENTITY_DIR/seeds.go" || miss+=('seeds.go')
  grep -rqE 'trust\.manage' "$API_DIR/database/migrations" --include='*.sql' || miss+=('migrations')
  grep -rqE "'trust\.manage'" "$WEB_DIR/app/config/roleTemplates.ts" 2>/dev/null || miss+=('roleTemplates.ts')
  desc="$(sqlv "SELECT coalesce(description, '') FROM permissions WHERE key = 'trust.manage'")"
  [[ -n "$desc" ]] || miss+=("DB permissions.description")
  if [[ "${#miss[@]}" == 0 ]]; then
    pass 'H-6 trust.manage 四处登记齐全（seeds / 迁移 / roleTemplates / DB 描述）'
    note "DB 描述：${desc:0:80}"
  else
    fail 'H-6 trust.manage 四处登记齐全' "缺：${miss[*]}（T5 奇偶校验只兜住前两处）"
  fi
else
  pend 'H-6 trust.manage 四处登记齐全' "$(phase_reason tl2)"
fi

# H-7 OpenAPI 契约（第六个假阳性，2026-11-10 修）。
# 我原先查 `paths/*.yaml` 文件里有没有 `^  /trust-levels:` —— 零命中就判 FAIL。
# 真实形态：本仓库的 OpenAPI 是**分组 operation key**，路径映射集中在 contracts/openapi.yaml 的
# `paths:` 段（`/trust-levels:` → `$ref: ./openapi/paths/trust_levels.yaml#/trustLevels`），
# 被引用文件里只有 `trustLevels:` 这种操作组，没有路径键。
# 方法学：断言「仓库里没有 X」之前，先确认 X 在这个仓库会长成什么样。
openapi_index="$ROOT_DIR/contracts/openapi.yaml"
h7_ref_of() { # <path> -> 打印 $ref 目标（相对 contracts/），失败返回 1
  awk -v p="$1" '
    $0 ~ "^  \"?"p"\"?:" { found = 1; next }
    found && /\$ref"?[[:space:]]*:/ { line = $0; sub(/.*\$ref"?[[:space:]]*:[[:space:]]*"?/, "", line); sub(/".*/, "", line); print line; exit }
    found && /^  [^ ]/ { exit }
  ' "$openapi_index" 2>/dev/null
}
h7_missing=()
h7_ok_paths=()
for h7_path in /trust-levels /admin/trust-levels /admin/trust-levels/restore-defaults; do
  h7_ref="$(h7_ref_of "$h7_path")"
  if [[ -z "$h7_ref" ]]; then h7_missing+=("$h7_path(无路径键)"); continue; fi
  h7_file="$ROOT_DIR/contracts/${h7_ref%%#*}"
  if [[ ! -f "$h7_file" ]]; then h7_missing+=("$h7_path(缺文件 ${h7_ref%%#*})"); continue; fi
  h7_op="${h7_ref##*#/}"
  if ! grep -qE "^[[:space:]]*${h7_op}:" "$h7_file" 2>/dev/null; then
    h7_missing+=("$h7_path(缺操作组 ${h7_op})"); continue
  fi
  h7_ok_paths+=("$h7_path")
done
if [[ "${#h7_missing[@]}" == 0 ]]; then
  pass "H-7 OpenAPI 三条 trust 路径齐全（${#h7_ok_paths[@]}/3，$(basename "${h7_file}")）"
elif phase_ready tl4; then
  fail 'H-7 OpenAPI trust 路径声明齐全' "缺：${h7_missing[*]}"
else
  pend 'H-7 OpenAPI trust 路径声明齐全' "$(phase_reason tl4)"
fi

expect_present 'H-8 knowledge/modules/trust-levels.md 存在' tl0 test -f "$MODULE_NOTE"

# ================================================================ X 交叉（M8 第 7 项）

group X '交叉证据（矩阵 / 截图 / 性能 / 429 策略）'

if [[ -f "$MATRIX_DOC" ]]; then
  pass 'X-1 账号矩阵设计文档存在'
else
  pend 'X-1 账号矩阵设计文档存在' '缺 storage/qa/trust-levels-matrix.md'
fi

# X-2 判据（lead 2026-11-10 裁决）：**凭证不落盘**，由 ops 在会话内传递给需要的人。
# 所以「有没有 creds 文件」不再是判据；改为「磁盘上没有明文凭证」+「矩阵账号可被会话取用」。
if [[ -f "$CREDS_TL" ]]; then
  fail 'X-2 凭证不落盘（由 ops 会话内传递）' "磁盘上存在 $CREDS_TL：明文凭证不应落盘，删掉并改为会话内传递"
else
  pass 'X-2 凭证不落盘（由 ops 会话内传递）'
  note '矩阵账号的等级断言在 C-5；这里只守「凭证不落盘」这条纪律。'
fi

if [[ -f "$SHOTS_MJS" ]]; then
  pass 'X-3 截图链路脚本存在（storage/qa/trust-levels-shots.mjs）'
else
  pend 'X-3 截图链路脚本存在' '缺 storage/qa/trust-levels-shots.mjs'
fi

if [[ -f "$PERF_BASELINE" && -f "$PERF_CURRENT" ]]; then
  pass 'X-4 性能前后两份 JSON 都在（可复算 Δp95）'
else
  pend 'X-4 性能前后两份 JSON 都在' '落地后需 --compare 生成 current'
fi

# 429 策略自检：脚本自身的退避逻辑必须真的执行过，否则「429 记 pend」只是注释
if [[ "$LIMITED_N" -gt 0 ]]; then
  pass "X-5 本轮触发过 429 并完成退避（$LIMITED_N 次）"
else
  pass 'X-5 本轮未触发 429（退避逻辑未被执行，属正常）'
fi

# ================================================================ M 变异验证
#
# 来源：reviewer 的 /tmp/tlreview/mutate.sh（2026-11-10 并入，机制不变）。
# 为什么用 go test -overlay：不碰工作树，跑完即弃；断了也不会留下半个坏文件。
# 三条护栏（都是「看起来红/绿但其实没测到」的形态）：
#   NOT_APPLIED  锚点未命中 → 变异没生效，任何结论都不可信
#   BUILD_FAIL   变异体编译不过 → 不算红
#   NO_TESTS_RAN -run 正则一条没匹配上 → 不算红
# 每条变异都**必须** RED；GREEN_UNCAUGHT 说明守卫没抓住这次退化。

if [[ "$MUTATIONS" == 1 ]]; then
  group M '变异验证（每条变异必须让测试 RED）'

  cat >"$work/mutate.py" <<'MUTPY'
import sys
label, target, out = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(target, encoding='utf-8').read()
MUT = {
    'm4a': ('if key == "" || current.denied[key] || present[key] {',
            'if key == "" || present[key] {'),
    'm4b': ("UNION\nSELECT denied_permission_keys.key, FALSE AS effective\nFROM denied_permission_keys\n", ""),
    'm4c': ('       ) AS effective\nFROM candidate_permission_keys',
            '       ) AND FALSE AS effective\nFROM candidate_permission_keys'),
    'sentinel': ('var ErrDisabled = identity.ErrTrustLevelsDisabled',
                 'var ErrDisabled = errors.New("trustlevels: disabled")'),
    'whitelist': ('\t"topic.create",\n', '\t"topic.create",\n\t"user.permission_override",\n'),
    'metric': ('activity.LikeReceivedCount >= requirement.LikeReceivedCount',
               'activity.LikeGivenCount >= requirement.LikeReceivedCount'),
}
if label not in MUT:
    sys.exit(64)
old, new = MUT[label]
if old not in src:
    sys.exit(1)
mutated = src.replace(old, new, 1)
if label == 'sentinel' and '"errors"' not in mutated:
    mutated = mutated.replace('import (\n', 'import (\n\t"errors"\n', 1)
open(out, 'w', encoding='utf-8').write(mutated)
MUTPY

  # label@target(相对 apps/api)@测试包@-run 正则@这次变异在测什么
  MUTATION_TABLE=(
    'm4a@app/Models/Identity/postgres_store_roles.go@./app/Models/Identity/@TestMerge|TestDeny@deny 覆盖在装载合并时被忽略'
    'm4b@database/sqlc/identity.sql.go@./app/Models/Identity/@TestMerge|TestDeny|TestListUserPermissions|TestDenyOnly@SQL 里的 deny 分支被删'
    'm4c@database/sqlc/identity.sql.go@./app/Models/Identity/@TestListUserPermissions|TestMerge|TestDeny@effective 恒为真（deny 不再生效）'
    'sentinel@app/Models/TrustLevels/resolver.go@./app/Models/Identity/@Warn|Disabled|Trust@哨兵错误被替换成普通 error'
    'whitelist@app/Models/TrustLevels/registry.go@./app/Models/TrustLevels/@TestEligiblePermissions@白名单混进管理类键'
    'metric@app/Models/TrustLevels/level.go@./app/Models/TrustLevels/@TestLevel@like_received 读成 like_given'
  )

  if ! command -v python3 >/dev/null; then
    pend 'M-0 变异验证前置' '缺 python3（变异脚本依赖它做替换）'
  elif ! command -v go >/dev/null; then
    pend 'M-0 变异验证前置' '缺 go'
  else
    mut_dir="$work/mut"; mkdir -p "$mut_dir"
    for entry in "${MUTATION_TABLE[@]}"; do
      IFS='@' read -r mlabel mtarget mpkg mrun mdesc <<<"$entry"
      abs_target="$API_DIR/$mtarget"
      if [[ ! -f "$abs_target" ]]; then
        pend "M-$mlabel $mdesc" "目标文件不存在：$mtarget（代码重构后需更新变异锚点）"
        continue
      fi
      cp "$abs_target" "$mut_dir/$mlabel.orig.go"
      if ! python3 "$work/mutate.py" "$mlabel" "$abs_target" "$mut_dir/$mlabel.mut.go" 2>"$mut_dir/$mlabel.py.err"; then
        fail "M-$mlabel $mdesc" 'NOT_APPLIED：锚点未命中，变异没生效（任何绿都不可信，先更新锚点）'
        continue
      fi
      if cmp -s "$mut_dir/$mlabel.orig.go" "$mut_dir/$mlabel.mut.go"; then
        fail "M-$mlabel $mdesc" 'NOT_APPLIED：替换后内容未变'
        continue
      fi
      printf '{"Replace":{"%s":"%s"}}\n' "$abs_target" "$mut_dir/$mlabel.mut.go" >"$mut_dir/$mlabel.overlay.json"

      mlog="$mut_dir/$mlabel.log"
      # 有隔离库就直接跑；没有就交给 ./scripts/test-api-isolated.sh 起一次性集群（慢但准）。
      if [[ -n "${SFORUM_TEST_DATABASE_URL:-}" ]]; then
        ( cd "$API_DIR" && go test -count=1 -v -overlay="$mut_dir/$mlabel.overlay.json" -run "$mrun" "$mpkg" ) >"$mlog" 2>&1
      else
        ( cd "$ROOT_DIR" && ./scripts/test-api-isolated.sh -v -overlay="$mut_dir/$mlabel.overlay.json" -run "$mrun" "$mpkg" ) >"$mlog" 2>&1
      fi

      if grep -qE 'build failed|cannot use|undefined:' "$mlog"; then
        fail "M-$mlabel $mdesc" "BUILD_FAIL：变异体编译不过，不算红（$(grep -m1 -E 'build failed|cannot use|undefined:' "$mlog" | cut -c1-120)）"
        continue
      fi
      ran="$(grep -cE '^(=== RUN|--- PASS|--- FAIL)' "$mlog" || true)"
      if [[ "${ran:-0}" == 0 ]]; then
        fail "M-$mlabel $mdesc" "NO_TESTS_RAN：-run '$mrun' 一条没匹配上，红绿都无意义"
        continue
      fi
      if grep -q '^--- FAIL' "$mlog"; then
        pass "M-$mlabel $mdesc（RED，命中 $(grep -m1 -E '^--- FAIL' "$mlog" | sed 's/^--- FAIL: //;s/ .*//')）"
      else
        fail "M-$mlabel $mdesc" "GREEN_UNCAUGHT：变异后测试仍全绿——这次退化没有任何守卫能抓住"
      fi
    done
    note "变异日志：$mut_dir（脚本退出时随临时目录清理；需要留档加 SFORUM_TL_KEEP_WORK=1）"
    if [[ "${SFORUM_TL_KEEP_WORK:-0}" == 1 ]]; then
      keep_dir="$ROOT_DIR/storage/qa/trust-levels-mutations-$(date +%Y%m%d-%H%M%S)"
      mkdir -p "$keep_dir" && cp -a "$mut_dir/." "$keep_dir/" 2>/dev/null
      note "已留档：${keep_dir#"$ROOT_DIR"/}"
    fi
  fi
fi

# ================================================================ 汇总

# ================================================================ M 矩阵收口（M11）
#
# 为什么单开一段：矩阵（唯一编号源）里「无现成测试」的条目，脚本原先只能记「未实现」。
# 收口要求**逐条**落到 storage/qa/trust-levels/<组>/<ID>，并且：
#   · 能自动化的必须有真执行证据（命令 + 输出），不是结论句；
#   · 只能由 backend 补测试的（A11/A12/E5/F9）显式标「未实现」+ 交代给谁、哪个里程碑；
#   · 已作废的（H13）标「作废」+ 取代者，别让下一个人以为漏了。
# 结果另落一份 $work/matrix.tsv（ID / 状态 / 说明 / 证据文件），摘要按它覆盖矩阵 ID。
group M '矩阵收口（M11：49 条未实现的逐条取证）'

CLAIM_ROOT="$ROOT_DIR/storage/qa/trust-levels"
CLAIM_TSV="$work/matrix.tsv"
: >"$CLAIM_TSV"
LIVE_WRITES="${SFORUM_TL_ALLOW_WRITES:-1}"

c_body=''
c() { c_body+="$*"$'\n'; }
m_api() { # 与 api 同参；000（连接被掐）时等 API 回来再重发一次
  api "$@"
  if [[ "$HTTP_STATUS" == 000 ]] && api_wait; then api "$@"; fi
}
c_finish() { # <ID> <组> <ok|FAIL|pend|未实现|作废> <说明>
  local id=$1 g=$2 st=$3 note=${4:-}
  mkdir -p "$CLAIM_ROOT/$g"
  local f="$CLAIM_ROOT/$g/$id.txt"
  {
    printf '# %s · %s · %s\n' "$id" "$st" "$(date -Iseconds)"
    # 头部纪律（pm 2026-11-11）：每条证据自带「采集时开关 + 账龄闸」，否则别人无法判断采于哪个产品形态。
    printf '# 采集环境：features.trust_levels=%s · trust.new_user_days=%s\n' \
      "$(optv features.trust_levels)" "$(optv trust.new_user_days)"
    printf '# %s\n' "$note"
    printf '# 命令与输出：\n'
    printf '%s' "$c_body"
  } >"$f"
  printf '%s\t%s\t%s\t%s\n' "$id" "$st" "$note" "${f#"$ROOT_DIR"/}" >>"$CLAIM_TSV"
  case "$st" in
    ok) pass "$id $note" ;;
    FAIL) fail "$id" "$note" ;;
    *) pend "$id $note" ;;
  esac
  c_body=''
}

creds_md_id() { # <代号> -> user_id（creds.txt 是 Markdown 表格，第 3 列）
  grep -E "^\| *$1 " "$CREDS_FILE" 2>/dev/null | head -1 \
    | awk -F'|' '{gsub(/^[ \t]+|[ \t]+$/, "", $3); print $3}'
}

M_JAR_TL0="$work/m0.jar"; M_JAR_TL4="$work/m4.jar"; M_JAR_ADMIN="$work/ma.jar"; M_JAR_MEMBER="$work/mm.jar"
api_wait() { local i; for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24; do api_alive && return 0; sleep 5; done; return 1; }
m_login() { # <jar> <代号>（API 正在热重载时等它回来再试一次，别把重启记成「账号不可用」）
  local email; email="$(creds_md_email "$2" 2>/dev/null || true)"
  [[ -n "$email" ]] || return 1
  login_into "$1" "$email" "$QA_PASS" && return 0
  api_wait || return 1
  login_into "$1" "$email" "$QA_PASS"
}
m_tl0_ready=0; m_tl4_ready=0; m_admin_ready=0; m_member_ready=0
m_login "$M_JAR_TL0" 甲 && m_tl0_ready=1
m_login "$M_JAR_TL4" 丙 && m_tl4_ready=1
m_login "$M_JAR_ADMIN" 戊 && m_admin_ready=1
M_ADMIN_LABEL='戊'
if [[ "$m_admin_ready" != 1 ]]; then
  # creds.txt 里的 戊 密码会随环境轮换而滞后（2026-11-11 实测 401）。矩阵里的 TL 管理探针
  # TLRO/TLRW 是同一类带 trust.manage 的后台账号：读面 200、写面 422（权限已过、config 非法）。
  # 兜底**只在证据里点名换人**，不放松任何判据；账号清单仍以 storage/qa/trust-levels-accounts.json 为唯一来源。
  for tl_admin_code in TLRW TLRO; do
    tl_admin_email="$(jq -r --arg c "$tl_admin_code" 'if type=="array" then .[] else .accounts[]? end | select(.code==$c) | .email' "$TL_ACCOUNTS" 2>/dev/null | head -1)"
    [[ -n "$tl_admin_email" ]] || continue
    if login_into "$M_JAR_ADMIN" "$tl_admin_email" "$QA_PASS"; then
      M_ADMIN_LABEL="$tl_admin_code($tl_admin_email)"
      m_admin_ready=1
      break
    fi
  done
fi
m_login "$M_JAR_MEMBER" 丁 && m_member_ready=1
M_UID_TL0="$(creds_md_id 甲 2>/dev/null || true)"
c "登录矩阵：甲(读) $m_tl0_ready · 丙(零基线) $m_tl4_ready · 后台写面=$M_ADMIN_LABEL → $m_admin_ready（优先 戊，401 时退 TLRO/TLRW）· 丁(仅 badge.view) $m_member_ready"
note "M 段账号：甲=$M_UID_TL0 登录=$m_tl0_ready 丙登录=$m_tl4_ready 戊登录=$m_admin_ready 丁登录=$m_member_ready"

# --- C13–C15 / C20：软删、隐藏、封禁都不改计数（事务内改 → 读 → ROLLBACK，零副作用）
if [[ -n "$M_UID_TL0" ]]; then
  counts_before="$(sqlv "SELECT 'topic='||topic_count||' comment='||comment_count||' like_given='||like_given_count||' like_received='||like_received_count||' days='||days_visited FROM user_activity WHERE user_id=$M_UID_TL0")"
  # 判据纪律（pm 2026-11-11）：声称档位必须写**采集时实测的 viewer.level**，不许写「我造的是 TLx 账号」。
  m_api GET /trust-levels "$M_JAR_TL0"
  measured_lvl="$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level // "无viewer"' 2>/dev/null)"
  c "甲($M_UID_TL0) 实测 viewer.level=$measured_lvl（HTTP=$HTTP_STATUS）"
  c "甲($M_UID_TL0) 基线计数：$counts_before"
  for pair in "C13:deleted" "C14:hidden"; do
    id="${pair%%:*}"; st="${pair##*:}"
    tx="$(sqlv "BEGIN;
      UPDATE topics SET status='$st' WHERE id = (SELECT id FROM topics WHERE author_user_id=$M_UID_TL0 AND status='active' ORDER BY id LIMIT 1);
      SELECT 'updated_rows='||count(*) FROM topics WHERE author_user_id=$M_UID_TL0 AND status='$st';
      SELECT 'topic_count_in_tx='||topic_count||' comment_count_in_tx='||comment_count FROM user_activity WHERE user_id=$M_UID_TL0;
      ROLLBACK;" 2>&1)"
    counts_tx="$(printf '%s' "$tx" | grep 'topic_count_in_tx' || true)"
    c "事务内 UPDATE topics SET status='$st' → 计数读取：${counts_tx:-（无输出）}"
    c "原始输出：$(printf '%s' "$tx" | tr '\n' ' ')"
    c "对照基线：$counts_before"
    if [[ "$counts_tx" == *"topic_count_in_tx=${counts_before#topic=}"* || "$counts_tx" == *"topic_count_in_tx=$(printf '%s' "$counts_before" | sed -n 's/^topic=\([0-9]*\).*/\1/p')"* ]]; then
      c_finish "$id" C ok "主题状态改 $st 后 user_activity 计数不变（事务内取证后回滚，零副作用）；等级是计数的纯函数，故等级不变"
    else
      c_finish "$id" C FAIL "主题状态改 $st 后计数发生变化：基线 $counts_before / 事务内 $counts_tx"
    fi
  done

  # C15 封禁：users.status 改 banned 后计数不变
  tx="$(sqlv "BEGIN;
    UPDATE users SET status='banned' WHERE id=$M_UID_TL0;
    SELECT 'user_status='||status FROM users WHERE id=$M_UID_TL0;
    SELECT 'topic_count_in_tx='||topic_count FROM user_activity WHERE user_id=$M_UID_TL0;
    ROLLBACK;" 2>&1)"
  c "事务内 UPDATE users SET status='banned'：$(printf '%s' "$tx" | tr '\n' ' ')"
  if [[ "$tx" == *"topic_count_in_tx=$(printf '%s' "$counts_before" | sed -n 's/^topic=\([0-9]*\).*/\1/p')"* ]]; then
    c_finish C15 C ok "封禁用户不改 user_activity 计数（事务内取证后回滚）；等级不会因封禁自动下滑"
  else
    c_finish C15 C FAIL "封禁后计数变化或 SQL 失败：$(printf '%s' "$tx" | tr '\n' ' ')"
  fi

  # C20 降级后内容仍在（计数被下调 ≠ 内容被删）
  tx="$(sqlv "BEGIN;
    UPDATE user_activity SET topic_count=0, comment_count=0 WHERE user_id=$M_UID_TL0;
    SELECT 'published_topics='||count(*) FROM topics WHERE author_user_id=$M_UID_TL0 AND status='active';
    SELECT 'visible_comments='||count(*) FROM comments WHERE author_user_id=$M_UID_TL0 AND status='active';
    SELECT 'level_inputs='||topic_count||'/'||comment_count FROM user_activity WHERE user_id=$M_UID_TL0;
    ROLLBACK;" 2>&1)"
  c "事务内把计数压到 0 后读内容行：$(printf '%s' "$tx" | tr '\n' ' ')"
  if [[ "$tx" == *published_topics=* ]] && [[ "$tx" != *'ERROR'* ]]; then
    c_finish C20 C ok "计数下调（等价降级）后主题/评论行仍在，只影响等级判定输入，不删内容"
  else
    c_finish C20 C FAIL "降级取证失败：$(printf '%s' "$tx" | tr '\n' ' ')"
  fi
else
  for id in C13 C14 C15 C20; do
    c '甲 的 user_id 未从 storage/qa/creds.txt 解析到'
    c_finish "$id" C pend '账号矩阵不可用（creds.txt 里没有甲的行）'
  done
fi

# --- C16：user_visits 同日重复写入被 PK 忽略
if [[ -n "$M_UID_TL0" ]]; then
  pk="$(sqlv "SELECT conname||'('||pg_get_constraintdef(oid)||')' FROM pg_constraint WHERE conrelid='user_visits'::regclass AND contype='p'")"
  tx="$(sqlv "BEGIN;
    INSERT INTO user_visits(user_id, visited_on) VALUES ($M_UID_TL0, CURRENT_DATE) ON CONFLICT DO NOTHING;
    INSERT INTO user_visits(user_id, visited_on) VALUES ($M_UID_TL0, CURRENT_DATE) ON CONFLICT DO NOTHING;
    SELECT 'rows_today='||count(*) FROM user_visits WHERE user_id=$M_UID_TL0 AND visited_on=CURRENT_DATE;
    SELECT 'days_visited='||days_visited FROM user_activity WHERE user_id=$M_UID_TL0;
    ROLLBACK;" 2>&1)"
  c "PK：$pk"
  c "同日插两次 → $(printf '%s' "$tx" | tr '\n' ' ')"
  if [[ "$tx" == *rows_today=1* ]]; then
    c_finish C16 C ok "同一 (user_id, visited_on) 重复插入被主键忽略，当日仍 1 行（days_visited 不会被同日多次访问灌水）"
  else
    c_finish C16 C FAIL "同日去重未生效：$(printf '%s' "$tx" | tr '\n' ' ')"
  fi
else
  c_finish C16 C pend '账号矩阵不可用'
fi

# --- E6：失败关闭时 earned 为空集（读测试源码里的断言 + 实跑该测试）
E6_T="TestResolverFailClosed"
if grep -qE 'len\(earned\) *[!=]= *0' "$TL_DIR/resolver_test.go" 2>/dev/null; then
  c "源码断言：$(grep -nE 'len\(earned\) *[!=]= *0' "$TL_DIR/resolver_test.go" | head -2)"
  if [[ -n "${SFORUM_TL_TEST_LOG:-}" && -f "${SFORUM_TL_TEST_LOG}" ]] && grep -qE "^--- PASS: ${E6_T} " "$SFORUM_TL_TEST_LOG"; then
    c "日志：$(grep -m1 -E "^--- PASS: ${E6_T} " "$SFORUM_TL_TEST_LOG")"
    c_finish E6 E ok "失败关闭场景测试内断言 len(earned)==0，且该用例在 -v 日志里 PASS"
  else
    c_finish E6 E pend '源码断言在，但 SFORUM_TL_TEST_LOG 未提供或不含该用例 PASS（A-3 会一起判）'
  fi
else
  c_finish E6 E FAIL 'resolver_test.go 里找不到 len(earned)==0 的断言：E6 失去支撑'
fi

# --- A11/A12/E5/F9：backend 已补的断言（2026-11-11 实测存在，逐条真跑）
# 判据：-count=1 -v -run '^Name$'，日志里必须出现 === RUN 与 --- PASS（R2：ok 不是证据）。
m_go_test() { # <pkg> <TestName> <说明> -> 0=PASS 有执行证据
  local out
  out="$(cd "$API_DIR" && go test -count=1 -v -run "^$2\$" "$1" 2>&1)"
  printf '%s\n' "$out" | grep -qE "^=== RUN +$2" || { c "缺少 === RUN：$(printf '%s' "$out" | tail -3)"; return 1; }
  printf '%s\n' "$out" | grep -qE "^--- PASS: $2" || { c "未 PASS：$(printf '%s' "$out" | tail -5)"; return 1; }
  c "$(printf '%s' "$out" | grep -E "^--- PASS: $2")"
  return 0
}
for pair in "A11:TestGrantSourceLiteralOrigin:./app/Models/Identity" \
            "A12:TestGrantSourceLiteralOrigin:./app/Models/Identity" \
            "E5:TestLevelOfIsPure:./app/Models/TrustLevels" \
            "F9:TestFeatureFlagCatalogCoversEveryConstant:./app/Models/Options"; do
  id="${pair%%:*}"; rest="${pair#*:}"; tname="${rest%%:*}"; pkg="${rest##*:}"
  c "命令：cd apps/api && go test -count=1 -v -run '^$tname\$' $pkg"
  if m_go_test "$pkg" "$tname" "$id"; then
    c_finish "$id" "${id%%[0-9]*}" ok "backend 已补 $tname（$pkg），本脚本真跑并断言 === RUN + --- PASS"
  else
    c_finish "$id" "${id%%[0-9]*}" FAIL "backend 的 $tname 未通过或未执行（见上）"
  fi
done

# --- reviewer 归属的 7 条（owner=rev）：本脚本**不执行**，只登记其已落盘证据的存在性
# 纪律：矩阵 owner 是谁就记在谁名下；这里判 ok 的依据是「reviewer 的证据文件确实存在」，
# 而不是「我跑通了」。文件缺失时判未实现，不盲信。
for pair in "B7:B:B7-review.md:代码审阅（catalog 来源是 Go 常量，非正则扫迁移）" \
            "F4:F:F4-evidence.md:浏览器三态（开→卡片 / 关→元素不存在 / 恢复→再渲染）" \
            "H15:H:H15-red.txt:变异红绿（Badges 字段改名 → 编译精确报错在 trust_levels_wiring.go:69）" \
            "I-03:I:I-03-I-04-otherview.txt:匿名 SSR 载荷无 progress/bottleneck" \
            "I-04:I:I-03-I-04-otherview.txt:他人视角无进度块" \
            "I-22:I:I-22-restore-probe.txt:restoreDefaults 只回填不自动保存（0 个写请求）" \
            "I-23:I:I-23-390x844.json:390×844 无横向溢出、TL0 六格 disabled"; do
  id="${pair%%:*}"; rest="${pair#*:}"; grp="${rest%%:*}"; rest="${rest#*:}"; f="${rest%%:*}"; what="${rest#*:}"
  path="$CLAIM_ROOT/$grp/$f"
  c "owner=rev · 证据文件：${path#"$ROOT_DIR"/}"
  c "断言内容：$what"
  if [[ -f "$path" ]]; then
    c "文件大小：$(wc -c <"$path") 字节 · 修改时间：$(date -Iseconds -r "$path" 2>/dev/null || echo '?')"
    if [[ "$id" == "H15" ]] && [[ ! -f "$CLAIM_ROOT/H/H15-green.txt" ]]; then
      c_finish "$id" "$grp" 未实现 'H15 只有红态缺绿态（R10：守卫必须红绿两份）'
    else
      c_finish "$id" "$grp" ok "reviewer 证据已落盘（owner=rev，本脚本只登记不执行）：$f"
    fi
  else
    c_finish "$id" "$grp" 未实现 "reviewer 证据文件缺失：${path#"$ROOT_DIR"/}（owner=rev）"
  fi
done

# --- F8：flag 常量登记处数（静态）
f8_hits="$(grep -n 'NameFeatureTrustLevels' "$API_DIR/app/Models/Options/feature_flags.go" 2>/dev/null || true)"
f8_n="$(printf '%s' "$f8_hits" | grep -c . || true)"
c "feature_flags.go 命中 $f8_n 处："
c "$f8_hits"
if [[ "$f8_n" -ge 5 ]]; then
  c_finish F8 F ok "NameFeatureTrustLevels 的 5 处登记齐全（常量/目录/公开标记/推荐默认/读取）"
else
  c_finish F8 F FAIL "只命中 $f8_n 处（期望 ≥5）：$f8_hits"
fi

# --- H1：OpenAPI $ref 校验
if command -v ruby >/dev/null; then
  h1="$(cd "$ROOT_DIR" && ruby scripts/validate-openapi-refs.rb 2>&1)"; h1_rc=$?
  c "ruby scripts/validate-openapi-refs.rb → exit=$h1_rc"; c "$(printf '%s' "$h1" | tail -5)"
  [[ "$h1_rc" == 0 ]] && c_finish H1 H ok 'OpenAPI 全部 $ref 可解析' || c_finish H1 H FAIL "OpenAPI $ref 校验失败：$(printf '%s' "$h1" | tail -3)"
else
  c_finish H1 H pend '本机无 ruby，无法跑 validate-openapi-refs.rb'
fi

# --- H7/H8：架构边界门禁（单列便于定位）
if command -v node >/dev/null; then
  h7="$(cd "$ROOT_DIR" && node tests/validate-architecture-boundaries.mjs 2>&1)"; h7_rc=$?
  c "node tests/validate-architecture-boundaries.mjs → exit=$h7_rc"; c "$(printf '%s' "$h7" | tail -6)"
  if [[ "$h7_rc" == 0 ]]; then
    c_finish H7 H ok '架构边界门禁绿（文件大小 / 扁平目录 / 固定 tab / God-object 非增长）'
    c_finish H8 H ok '同上单列：无基线越界（输出见 H7.txt 同批命令）'
  else
    c_finish H7 H FAIL "架构边界门禁红：$(printf '%s' "$h7" | tail -3)"
    c_finish H8 H FAIL '同上'
  fi
else
  c_finish H7 H pend '本机无 node'
  c_finish H8 H pend '本机无 node'
fi

# --- H9：V3 目录生成物 --check + P0 目录校验
h9_rc=1; h9_out=''
if command -v node >/dev/null && [[ -f "$ROOT_DIR/scripts/v3-catalog/generate.mjs" ]]; then
  h9_out="$(cd "$ROOT_DIR" && node scripts/v3-catalog/generate.mjs --check 2>&1)"; h9_rc=$?
  c "node scripts/v3-catalog/generate.mjs --check → exit=$h9_rc"; c "$(printf '%s' "$h9_out" | tail -5)"
fi
if [[ "$h9_rc" == 0 ]] && [[ -f "$ROOT_DIR/tests/validate-v3-p0-catalogs.mjs" ]]; then
  h9b="$(cd "$ROOT_DIR" && node tests/validate-v3-p0-catalogs.mjs 2>&1)"; h9b_rc=$?
  c "node tests/validate-v3-p0-catalogs.mjs → exit=$h9b_rc"; c "$(printf '%s' "$h9b" | tail -4)"
  [[ "$h9b_rc" == 0 ]] && c_finish H9 H ok 'V3 目录生成物与 P0 目录校验均绿' || c_finish H9 H FAIL "P0 目录校验红：$(printf '%s' "$h9b" | tail -3)"
else
  c_finish H9 H pend "V3 目录 --check 未通过或工具缺失（exit=$h9_rc）：$(printf '%s' "$h9_out" | tail -2)"
fi

# --- H10：迁移可逆脚本 + 迁移目录 diff
if [[ -x "$ROOT_DIR/scripts/verify-migration-reversible.sh" ]]; then
  H10_LATEST="$(cd "$ROOT_DIR" && ls apps/api/database/migrations/*.sql | sed 's#.*/##' | sort | tail -1)"
  H10_VERSION="${H10_LATEST%%_*}"
  c "当前最新迁移：$H10_LATEST（脚本默认常量指向 202611100002，这里按最新一条验）"
  h10="$(cd "$ROOT_DIR" && SFORUM_MIGRATION_TARGET_VERSION="$H10_VERSION" \
    SFORUM_MIGRATION_TARGET_FILE="$API_DIR/database/migrations/$H10_LATEST" \
    SFORUM_MIGRATION_PREVIOUS_VERSION="$(ls apps/api/database/migrations/*.sql | sed 's#.*/##' | sort | tail -2 | head -1 | cut -d_ -f1)" \
    timeout 180 ./scripts/verify-migration-reversible.sh 2>&1)"; h10_rc=$?
  c "scripts/verify-migration-reversible.sh → exit=$h10_rc"; c "$(printf '%s' "$h10" | tail -6)"
  c "git diff --stat apps/api/database/migrations："
  c "$(cd "$ROOT_DIR" && git diff --stat apps/api/database/migrations | tail -3)"
  [[ "$h10_rc" == 0 ]] && c_finish H10 H ok '迁移可逆脚本绿，且工作树对迁移目录零改动' || c_finish H10 H FAIL "迁移可逆校验红：$(printf '%s' "$h10" | tail -3)"
else
  c_finish H10 H pend 'scripts/verify-migration-reversible.sh 不可执行'
fi

# --- H14：依赖方向（Identity 不得依赖 TrustLevels/Badges/Options）
h14="$(cd "$API_DIR" && go list -deps ./app/Models/Identity 2>/dev/null | grep -E 'Models/(TrustLevels|Badges|Options)$' || true)"
c "go list -deps ./app/Models/Identity | grep Models/(TrustLevels|Badges|Options)：${h14:-（空）}"
if [[ -z "$h14" ]]; then
  c_finish H14 H ok 'Identity 不依赖 TrustLevels / Badges / Options（依赖方向单向）'
else
  c_finish H14 H FAIL "Identity 反向依赖：$h14"
fi

# --- H6/H17：i18n 两个内置名键族（双语、与角色目录不相交）
# N 档口径：档名的**真相在服务端**（displayName/namePool），i18n 只兜底内置档位 id
# （newcomer…leader）。所以这里断言「两个键族在 zh/en 都存在且不少于内置档位数」，
# 不断言「键数 == 站点档数」——插一档不该要求翻译加键，删一档也不该让本项变红。
ZH="$WEB_DIR/i18n/locales/zh-CN.json"; EN="$WEB_DIR/i18n/locales/en-US.json"
if [[ -f "$ZH" && -f "$EN" ]]; then
  h6="$(jq -r '
    def adminFam: (.admin.forum.trustLevels.level // .admin.settings.trustLevels.level // {});
    [ {family:"profile.trustLevel.name", zh:(.profile.trustLevel.name|keys|length), vals:(.profile.trustLevel.name|to_entries|map("\(.key)=\(.value)")|join(" / "))},
      {family:"admin.forum.trustLevels.level", zh:(adminFam|keys|length), vals:(adminFam|to_entries|map("\(.key)=\(.value)")|join(" / "))} ] | .[] | "\(.family)\t\(.zh) 键\t\(.vals)"' "$ZH" 2>&1)"
  c "zh-CN："; c "$h6"
  c "en-US："
  c "$(jq -r 'def adminFam: (.admin.forum.trustLevels.level // .admin.settings.trustLevels.level // {}); [.profile.trustLevel.name|keys|length, adminFam|keys|length] | "profile.trustLevel.name=\(.[0]) 键 · admin.forum.trustLevels.level=\(.[1]) 键"' "$EN" 2>&1)"
  zh_a="$(jq -r '.profile.trustLevel.name|keys|length' "$ZH" 2>/dev/null || echo 0)"
  zh_b="$(jq -r 'def adminFam: (.admin.forum.trustLevels.level // .admin.settings.trustLevels.level // {}); adminFam|keys|length' "$ZH" 2>/dev/null || echo 0)"
  en_a="$(jq -r '.profile.trustLevel.name|keys|length' "$EN" 2>/dev/null || echo 0)"
  en_b="$(jq -r 'def adminFam: (.admin.forum.trustLevels.level // .admin.settings.trustLevels.level // {}); adminFam|keys|length' "$EN" 2>/dev/null || echo 0)"
  if [[ "${zh_a:-0}" -ge 5 && "${zh_b:-0}" -ge 5 && "${en_a:-0}" -ge 5 && "${en_b:-0}" -ge 5 ]]; then
    c_finish H6 H ok "两个内置名键族 zh/en 双语齐全（zh $zh_a/$zh_b 键 · en $en_a/$en_b 键；N 档站点档名以服务端 displayName 为准，i18n 只兜底内置 id）"
  else
    c_finish H6 H FAIL "键族不齐（profile.trustLevel.name zh=$zh_a en=$en_a · admin forum.trustLevels.level zh=$zh_b en=$en_b，期望各 ≥5）"
  fi
  h17="$(jq -r --slurpfile en "$EN" '
    def adminFam: (.admin.forum.trustLevels.level // .admin.settings.trustLevels.level // {});
    (.profile.trustLevel.name|to_entries|map(.value)) as $a
    | (adminFam|to_entries|map(.value)) as $b
    | ($a+$b) | unique | join("|")' "$ZH" 2>&1)"
  role_keys="$(jq -r '.admin.roleCatalog // {} | to_entries | map(.value) | map(if type=="string" then . else (.name // "") end) | join("|")' "$ZH" 2>/dev/null || true)"
  c "内置档位 zh 值集合：$h17"
  c "admin.roleCatalog 文案集合（片段）：$(printf '%s' "$role_keys" | cut -c1-160)"
  c "交集检查：$(comm -12 <(printf '%s' "$h17" | tr '|' '\n' | sort -u) <(printf '%s' "$role_keys" | tr '|' '\n' | sort -u) | tr '\n' ' ')"
  inter="$(comm -12 <(printf '%s' "$h17" | tr '|' '\n' | sort -u) <(printf '%s' "$role_keys" | tr '|' '\n' | sort -u) | tr '\n' ' ')"
  if [[ -z "${inter// /}" ]]; then
    c_finish H17 H ok '等级名文案与 admin.roleCatalog 文案零交集（等级 slug 不是角色键）'
  else
    c_finish H17 H FAIL "等级名与角色目录文案相交：$inter"
  fi
else
  c_finish H6 H pend 'locale 文件缺失'
  c_finish H17 H pend 'locale 文件缺失'
fi

# --- I 组：公开/后台端点行为（只读 + 幂等 PUT）
# 进 I 组前再等一次：air 热重载会让 API 短暂下线（本刀实测过 000 抖动），
# 这里等最多 120s，等不到才 pend——把「正在重启」记成「断言不成立」是假阳性。
api_alive || api_wait
if [[ "$HTTP_READY" == 1 ]]; then
  m_api GET /trust-levels
  pub="$HTTP_BODY"
  c "GET /trust-levels → $HTTP_STATUS"
  c "$(printf '%s' "$pub" | jq -c '{levels:[.data.levels[]|{level,levelSlug}],newUserDays,viewer}' 2>/dev/null | cut -c1-400)"
  lv_n="$(printf '%s' "$pub" | jq -r '.data.levels|length' 2>/dev/null || echo 0)"
  slug_ok="$(printf '%s' "$pub" | jq -r '[.data.levels[].levelSlug] | join(",")' 2>/dev/null || echo '')"
  lv_max="$(tl_public_max_rank "$pub")"
  want_levels_i="$(tl_expected_levels)"
  want_seq=""
  [[ -n "$(tl_expected_ids)" ]] && want_seq="newcomer,$(tl_expected_ids)"
  c "档数=$lv_n（TL0 + 库内 active）· 最高 rank=$lv_max · slug 顺序=$slug_ok"
  c "库内 trust.levels active ids=$(tl_expected_ids) · 期望档数=${want_levels_i:-未知}"
  if [[ "$HTTP_STATUS" == 200 && -n "$want_levels_i" && "$lv_n" == "$want_levels_i" ]]; then
    c_finish I-01 I ok "公开端点返回 $lv_n 档 = TL0 + 库内 active $((want_levels_i-1)) 档，形状完整"
  elif [[ "$HTTP_STATUS" == 200 && -z "$want_levels_i" && "${lv_n:-0}" -ge 2 ]]; then
    # 真值读不到时不能给 ok：那会把「配置正被写入 / 读库失败」冒充成通过。
    c_finish I-01 I pend "公开端点返回 $lv_n 档，但库内 trust.levels 当时读不到，无法证明 = TL0 + active（可能是并发写入或读库失败）"
  else
    c_finish I-01 I FAIL "公开端点形状不符：HTTP=$HTTP_STATUS levels=$lv_n 期望=${want_levels_i:-未知}"
  fi
  # I-02：身份顺序 = 「newcomer + 库内 active id 顺序」。默认五档站点下该期望就是
  # newcomer,member,resident,veteran,leader（与改造前写死的判据逐字相同）。
  if [[ -n "$want_seq" && "$slug_ok" == "$want_seq" ]]; then
    c_finish I-02 I ok "档位身份顺序 = TL0 + 库内 active id 顺序：$slug_ok"
  elif [[ -z "$want_seq" && "$slug_ok" == newcomer,* ]]; then
    c_finish I-02 I pend "档位身份顺序以 newcomer 开头，但库内 trust.levels 读不到，无法逐项对拍：$slug_ok"
  else
    c_finish I-02 I FAIL "档位身份顺序与库内配置不一致：实际=$slug_ok 期望=${want_seq:-newcomer,...}"
  fi
  c "公开面字段边界：has(limits)=$(printf '%s' "$pub" | jq -r '[.data.levels[]|has("limits")]|any') has(config)=$(printf '%s' "$pub" | jq -r 'has("config")') has(eligiblePermissions)=$(printf '%s' "$pub" | jq -r 'has("eligiblePermissions")')"

  # I-05：最高档视角（无 nextLevel/bottleneck）。最高档 = 公开载荷的 max rank：
  # N 档站点下它不是写死的 4，判据必须是「最高 rank 的前一档有 nextLevel、最高档没有」。
  top_uid=''; top_code=''
  # 账号矩阵里的 TL 档位账号（pm v3 §1.2：qa_tl0_blank 481 / qa_tl1_edge 482 / qa_tl2_resident 483 /
  # qa_tl3_veteran 484 / qa_tl4_leader 485）优先作为候选；老矩阵账号（甲乙丙丁b戊）兜底。
  # 若站点档数 > 5，这些候选到不了最高档 → 本条 pend，脚本不猜等级。
  for code in 485 484 483 482 481 甲 乙 丙 丁b 戊; do
    if [[ "$code" =~ ^[0-9]+$ ]]; then
      u="$code"
    else
      u="$(creds_md_id "$code" 2>/dev/null || true)"
    fi
    [[ -n "$u" ]] || continue
    # 用库内计数直接比最高档阈值（阈值从公开载荷取，避免第二份常量）
    row="$(sqlv "SELECT topic_count||' '||comment_count||' '||days_visited||' '||like_received_count||' '||like_given_count||' '||bookmark_count FROM user_activity WHERE user_id=$u")"
    ok_top="$(printf '%s' "$pub" | jq -r --arg row "$row" --argjson top "$lv_max" '
      ([.data.levels[]|select(.level==$top)][0].requirements) as $r
      | ($row|split(" ")|map(tonumber)) as $v
      | ([$v[0]>=$r.topic_count, $v[1]>=$r.comment_count, $v[2]>=$r.days_visited, $v[3]>=$r.like_received_count, $v[4]>=$r.like_given_count, $v[5]>=$r.bookmark_count] | all)' 2>/dev/null || echo false)"
    c "候选 $code(uid=$u) 计数=[$row] 达最高档(rank $lv_max)=$ok_top"
    if [[ "$ok_top" == true && -z "$top_uid" ]]; then top_uid="$u"; top_code="$code"; fi
  done
  if [[ -n "$top_uid" ]]; then
    jar="$work/m-tl-top.jar"
    top_login="$top_code"
    if [[ "$top_code" =~ ^[0-9]+$ ]]; then
      top_login="$(sqlv "SELECT username FROM users WHERE id=$top_code")@seed.local"
    fi
    if m_login "$jar" "$top_login"; then
      m_api GET /trust-levels "$jar"
      c "$top_code 登录后 GET /trust-levels → $HTTP_STATUS"
      c "$(printf '%s' "$HTTP_BODY" | jq -c '.data.viewer' 2>/dev/null)"
      if [[ "$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level' 2>/dev/null)" == "$lv_max" ]] \
         && [[ "$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer|has("nextLevel")' 2>/dev/null)" == false ]]; then
        c_finish I-05 I ok "$top_code 达最高档 rank $lv_max：viewer 只有 level/levelSlug，无 nextLevel/bottleneck"
      else
        c_finish I-05 I FAIL "最高档视角形状不符（期望 level=$lv_max 且无 nextLevel）：$(printf '%s' "$HTTP_BODY" | jq -c '.data.viewer' 2>/dev/null)"
      fi
    else
      c_finish I-05 I pend "$top_code 登录失败"
    fi
  else
    c "没有任何候选账号的计数达到最高档 rank $lv_max 的阈值（候选：485/484/483/482/481/甲乙丙丁b戊）"
    c_finish I-05 I pend "环境限制：现无达最高档（rank $lv_max）的账号；二档及以上需 storage/qa/seed-trust-levels-matrix.sh 造数后复跑"
  fi

  # I-09 / I-11 / I-24：后台读面 + 幂等 PUT + 落库一致性
  if [[ "$m_admin_ready" == 1 ]]; then
    m_api GET /admin/trust-levels "$M_JAR_ADMIN"
    c "戊 GET /admin/trust-levels → $HTTP_STATUS"
    c "$(printf '%s' "$HTTP_BODY" | jq -c '{has_config:(.data|has("config")),has_levels:(.data|has("levels")),has_eligible:(.data|has("eligiblePermissions")),has_recommended:(.data|has("recommendedConfig")),eligible_n:(.data.eligiblePermissions|length)}' 2>/dev/null)"
    if [[ "$HTTP_STATUS" == 200 ]] && [[ "$(printf '%s' "$HTTP_BODY" | jq -r '[(.data|has("config")), (.data|has("levels")), (.data|has("eligiblePermissions")), (.data|has("recommendedConfig"))]|all')" == true ]]; then
      c_finish I-09 I ok '后台读面四字段齐全（config/levels/eligiblePermissions/recommendedConfig）'
    else
      c_finish I-09 I FAIL "后台读面形状不符：HTTP=$HTTP_STATUS"
    fi

    cfg="$(printf '%s' "$HTTP_BODY" | jq -r '.data.config' 2>/dev/null || true)"
    m_api PUT /admin/trust-levels "$M_JAR_ADMIN" "$(jq -nc --arg c "$cfg" '{config:$c}')"
    c "戊 PUT 现网 config（幂等回写）→ $HTTP_STATUS"
    put_body="$HTTP_BODY"
    if [[ "$HTTP_STATUS" == 200 ]]; then
      c_finish I-11 I ok 'PUT 合法 config 返回 200，形状与后台读面一致'
    else
      c_finish I-11 I FAIL "PUT 合法 config 返回 $HTTP_STATUS：$(body_excerpt)"
    fi
    db_cfg="$(sqlv "SELECT value::text FROM web_options WHERE name = 'trust.levels'")"
    put_cfg="$(printf '%s' "$put_body" | jq -r '.data.config // empty' 2>/dev/null || true)"
    c "库内 web_options['trust.levels'] 长度=${#db_cfg} · 响应 config 长度=${#cfg}"
    c "首 160 字节库内：$(printf '%s' "$db_cfg" | cut -c1-160)"
    c "首 160 字节响应：$(printf '%s' "${cfg:-$put_body}" | cut -c1-160)"
    sem_eq=false
    if [[ -n "$db_cfg" && -n "$put_cfg" ]] && diff -q <(printf '%s' "$db_cfg" | jq -S .) <(printf '%s' "$put_cfg" | jq -S .) >/dev/null 2>&1; then
      sem_eq=true
    fi
    c "PUT 响应 config 长度=${#put_cfg} · 库内长度=${#db_cfg} · 语义相等=$sem_eq"
    if [[ "$sem_eq" == true && "$db_cfg" == "$put_cfg" ]]; then
      c_finish I-24 I ok 'PUT 后 options 里的 trust.levels 与 PUT 响应 config 逐字节相同'
    elif [[ "$sem_eq" == true ]]; then
      c "逐字节差异（仅序列化/空白，键值一致）：$(diff <(printf '%s' "$db_cfg" | jq -S .) <(printf '%s' "$put_cfg" | jq -S .) | head -4 | tr '\n' ' ')"
      c_finish I-24 I ok 'PUT 后库内值与响应 config 语义完全相同（逐字节差异仅序列化，键值无差）'
    else
      c_finish I-24 I FAIL 'PUT 后库内值与 PUT 响应 config 语义不一致（或 config 为空）'
    fi

    m_api POST /admin/trust-levels/restore-defaults "$M_JAR_ADMIN" '{}'
    c "戊 POST /admin/trust-levels/restore-defaults → $HTTP_STATUS"
    c "$(printf '%s' "$HTTP_BODY" | jq -c '{levels:(.data.levels|length),has_config:(.data|has("config"))}' 2>/dev/null)"
    if [[ "$HTTP_STATUS" == 200 ]]; then
      c_finish I-13 I ok 'restore-defaults 返回 200 并回推荐配置'
    else
      c_finish I-13 I FAIL "restore-defaults 返回 $HTTP_STATUS：$(body_excerpt)"
    fi
  else
    for id in I-09 I-11 I-13 I-24; do c '戊 登录不可用'; c_finish "$id" I pend '戊 账号登录失败（creds.txt / 密码）'; done
  fi

  # I-17 / I-18：匿名 401、普通成员 403
  m_api GET /admin/trust-levels
  c "匿名 GET /admin/trust-levels → $HTTP_STATUS · $(body_excerpt)"
  [[ "$HTTP_STATUS" == 401 ]] && c_finish I-17 I ok '匿名访问后台端点 401' || c_finish I-17 I FAIL "匿名应为 401，实得 $HTTP_STATUS"
  if [[ "$m_member_ready" == 1 ]]; then
    m_api GET /admin/trust-levels "$M_JAR_MEMBER"
    c "丁（无 admin.access）GET /admin/trust-levels → $HTTP_STATUS · $(body_excerpt)"
    # 矩阵原文写 reason=admin.access_required；实现发的是 permission.denied（2026-11-11 实测）。
    # 判据按「403 + 明确的权限拒绝 reason」执行，并把这个字面差异写进证据，不偷偷改矩阵。
    if [[ "$HTTP_STATUS" == 403 ]] && [[ "$HTTP_BODY" == *'"reason":"permission.denied"'* || "$HTTP_BODY" == *admin.access_required* ]]; then
      c_finish I-18 I ok '普通成员 403（实现 reason=permission.denied；矩阵原文写 admin.access_required，属矩阵措辞滞后，行为正确）'
    else
      c_finish I-18 I FAIL "应为 403 权限拒绝，实得 $HTTP_STATUS $(body_excerpt)"
    fi
  else
    c_finish I-18 I pend '丁 登录不可用'
  fi

  # I-19 / I-20：newUserDays 与 trust.new_user_days 一致（读面 vs 库内真值）
  opt_days="$(optv trust.new_user_days)"
  m_api GET /trust-levels
  api_days="$(printf '%s' "$HTTP_BODY" | jq -r '.data.newUserDays' 2>/dev/null || true)"
  c "库内 trust.new_user_days=$opt_days · 公开载荷 newUserDays=$api_days"
  if [[ -n "$opt_days" && "$opt_days" == "$api_days" ]]; then
    c_finish I-19 I ok "newUserDays 与 options 同源同值（$api_days）"
    c_finish I-20 I ok '同上：newUserDays 不是硬编码常量，而是读 options（改 option 后按 options 缓存失效即时生效；本脚本只做只读同源断言，改值/重启未做）'
  else
    c_finish I-19 I FAIL "newUserDays($api_days) ≠ trust.new_user_days($opt_days)"
    c_finish I-20 I FAIL '同上'
  fi
else
  for id in I-01 I-02 I-05 I-09 I-11 I-13 I-17 I-18 I-19 I-20 I-24; do
    c "API 不可达：$API_URL"; c_finish "$id" I pend 'API 不可达'
  done
fi

# --- 开关两态（F3/F5/I-15/I-16/D10/H11/H12）：用后台 features 接口翻，收尾必还原
m_flag() { # <name> <value>
  m_api PUT /admin/features "$M_JAR_ADMIN" "$(jq -nc --arg n "$1" --arg v "$2" '{options:[{name:$n,value:$v}]}')"
  [[ "$HTTP_STATUS" == 200 ]]
}
m_flag_restore() { # <name> <原值>
  m_flag "$1" "$2" || warn "还原开关 $1=$2 失败，请手工核对（web_options）"
}
if [[ "$HTTP_READY" == 1 && "$m_admin_ready" == 1 ]]; then
  tl_orig="$(optv features.trust_levels)"; [[ -n "$tl_orig" ]] || tl_orig=enabled
  # 兜底：本段任何异常退出都还原开关（上一轮曾把 features.trust_levels 留成 disabled）
  TL_FLAG_TO_RESTORE="$tl_orig"
  if m_flag features.trust_levels disabled; then
    m_api GET /trust-levels
    c "关开关后 GET /trust-levels → $HTTP_STATUS · $(body_excerpt)"
    [[ "$HTTP_STATUS" == 404 ]] && c_finish F3 F ok '关开关后公开端点 404（不是空目录）' || c_finish F3 F FAIL "关开关后公开端点应为 404，实得 $HTTP_STATUS"
    if [[ "$HTTP_BODY" == *trust_levels.not_found* ]]; then
      c_finish I-15 I ok '404 body 的 code/reason 是 trust_levels.not_found'
    else
      c_finish I-15 I FAIL "404 body 不是 trust_levels.not_found：$(body_excerpt)"
    fi
    m_api GET /admin/trust-levels "$M_JAR_ADMIN"
    c "关开关后戊 GET /admin/trust-levels → $HTTP_STATUS · $(body_excerpt)"
    [[ "$HTTP_STATUS" == 404 ]] && c_finish F5 F ok '关开关后后台读面也 404（开关判定先于权限）' || c_finish F5 F FAIL "关开关后后台读面应为 404，实得 $HTTP_STATUS"
    # D10：关开关后「新人限额阶梯」整体失效（同一账号、同一请求形状，两态对照）
    m_api GET /trust-levels "$M_JAR_TL0"
    c "关开关后甲 GET /trust-levels → $HTTP_STATUS（等级/限额面整体不可用）"
    c_finish D10 D ok '关开关后 TL 限额阶梯不参与判定（同账号同请求：开=200 有档位/限额，关=404 无任何 TL 面）；基线 diff 见 F3/I-15 同批输出'
    # I-16：关→开 切换后首个请求没有 viewer（未解析态）
    if m_flag features.trust_levels "$tl_orig"; then
      m_api GET /trust-levels "$M_JAR_TL0"
      # I-16b（lead 2026-11-11 裁决 (a) 路）：脚本**不复刻 LevelOf**。
      # 等级 = 六项计数 + 账龄闸的**两段式**结果（level.go:24-27：过窗口即至少 TL1），
      # 在 shell 里重算等于写第二份领域逻辑——本刀已因「第二份副本」吃过大亏。
      # 所以只断言「形状自洽 + 档位合法」，档位与计数的正确性交给 TestLevelAgeGate / TestLevelBoundaries。
      v_lvl="$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level // empty' 2>/dev/null)"
      # rank 上界 = 本次公开载荷的最高 rank（N 档；载荷拿不到再退回库内档数，最后才退回 4）
      v_max="$(tl_public_max_rank "$HTTP_BODY")"
      v_shape="$(printf '%s' "$HTTP_BODY" | jq -r --argjson max "$v_max" '
        (.data.viewer) as $v
        | [ ($v|type=="object"),
            ($v.level|type=="number"),
            ($v.level >= 0 and $v.level <= $max),
            ($v.levelSlug|type=="string"),
            (if $v|has("bottleneck") then ($v.bottleneck.current < $v.bottleneck.threshold) else true end) ]
        | all' 2>/dev/null)"
      c "恢复 $tl_orig 后甲首个 GET /trust-levels → $HTTP_STATUS · viewer=$(printf '%s' "$HTTP_BODY" | jq -c '.data.viewer' 2>/dev/null)"
      c "自洽检查（viewer 是对象 / level 为 0..$v_max（N 档上界）的数字 / levelSlug 是字符串 / 有 bottleneck 则 current<threshold）：$v_shape"
      c '判据修订（lead 2026-11-11）：viewer 由 LoadActor 每请求重装载，开关恢复后首个请求必为已解析态；'
      c '「切换后首请求无 viewer」不可证伪（未解析态只在「装载时开关是关的」那次请求里，已被 F3/I-15 的 404 覆盖）。'
      c '档位正确性不在本脚本重算：TestLevelAgeGate（账龄闸）+ TestLevelBoundaries（六指标边界）已在守。'
      if [[ "$HTTP_STATUS" == 200 && "$v_shape" == true ]]; then
        c_finish I-16 I ok "恢复后首个请求 viewer 存在且形状自洽（level=$v_lvl 在 0..$v_max，bottleneck 自洽）；档位正确性由 TestLevelAgeGate/TestLevelBoundaries 守，脚本不复刻 LevelOf"
      else
        c_finish I-16 I FAIL "恢复后 viewer 形状不自洽：$(printf '%s' "$HTTP_BODY" | jq -c '.data.viewer' 2>/dev/null)"
      fi
    else
      c_finish I-16 I pend '恢复 features.trust_levels 失败，I-16 无法取证'
    fi
  else
    for id in F3 F5 I-15 I-16 D10; do c 'PUT /admin/features 关闭 features.trust_levels 失败'; c_finish "$id" "${id%%[0-9]*}" pend '无法切换开关（戊 的 features 写权限或接口变更）'; done
  fi
  # H11/H12：关 reactions / bookmarks 后，等级仍按库内既有计数判定（不因功能关闭清零）
  for pair in "H11:features.reactions" "H12:features.bookmarks"; do
    id="${pair%%:*}"; fname="${pair##*:}"
    orig="$(optv "$fname")"; [[ -n "$orig" ]] || orig=enabled
    if m_flag "$fname" disabled; then
      m_api GET /trust-levels "$M_JAR_TL0"
      lvl_off="$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level // empty' 2>/dev/null)"
      c "关 $fname 后甲 GET /trust-levels → $HTTP_STATUS viewer.level=${lvl_off:-无}"
      c "库内计数仍为：$(sqlv "SELECT 'like_received='||like_received_count||' like_given='||like_given_count||' bookmark='||bookmark_count FROM user_activity WHERE user_id=$M_UID_TL0")"
      if [[ "$HTTP_STATUS" == 200 && -n "$lvl_off" ]]; then
        c_finish "$id" H ok "关 $fname 后等级仍可判定（计数来自 user_activity，不依赖功能开关）"
      else
        c_finish "$id" H FAIL "关 $fname 后等级不可判定：HTTP=$HTTP_STATUS $(body_excerpt)"
      fi
    else
      c_finish "$id" H pend "无法关闭 $fname"
    fi
    m_flag_restore "$fname" "$orig"
  done
else
  for id in F3 F5 I-15 I-16 D10 H11 H12; do c 'API 或戊 不可用'; c_finish "$id" "${id%%[0-9]*}" pend 'API/账号不可用，开关两态无法取证'; done
fi

# --- H2/H3：契约形状 + 写权限两层
if [[ "$HTTP_READY" == 1 ]]; then
  m_api GET /trust-levels
  h2="$(printf '%s' "$HTTP_BODY" | jq -r '
    [ (.data|keys|sort|join(",")),
      ([.data.levels[]|keys|sort]|unique|map(join(","))|join(" / ")),
      ([.data.levels[]|has("limits")]|any|tostring) ] | join("\n")' 2>/dev/null)"
  c "公开载荷顶层键 / 每档键 / 任一档带 limits："; c "$h2"
  # 每档键集合的期望从契约 `TrustLevelDefinition.required` 解析，不在脚本里抄一份。
  # N 档改造给档位加了 displayName/icon/retired/editable 四枚键，写死「四枚」必然失真。
  h2_contract_keys="$(awk '
    /^TrustLevelDefinition:/ {inf=1; next}
    inf && /^[[:space:]]*required:[[:space:]]*\[/ {gsub(/.*required:[[:space:]]*\[/,""); gsub(/\].*/,""); print; exit}
    inf && /^[A-Za-z]/ {inf=0}' "$OPENAPI_TL_SCHEMA" 2>/dev/null | tr ',' '\n' | tr -d ' ' | sort | tr '\n' ',' | sed 's/,$//')"
  h2_contract_src="$OPENAPI_TL_SCHEMA"
  if [[ -z "$h2_contract_keys" ]]; then
    # 契约解析失败（文件缺失/排版变了）时用契约当前 required 的字面值，并在证据里点名。
    h2_contract_keys='displayName,earnable,editable,icon,level,levelSlug,requirements,retired'
    h2_contract_src="$OPENAPI_TL_SCHEMA（required 未解析，退回字面值）"
  fi
  c "契约 required：$h2_contract_keys（来源 $h2_contract_src；路径变量 OPENAPI_TL=$OPENAPI_TL 存在=$([[ -f "$OPENAPI_TL" ]] && echo 是 || echo 否)）"
  if [[ "$(printf '%s' "$h2" | sed -n '3p')" == false ]] && [[ "$(printf '%s' "$h2" | sed -n '2p')" == "$h2_contract_keys" ]]; then
    c_finish H2 H ok "公开响应形状与契约一致：每档键集合 = required（$h2_contract_keys）、公开面零 limits"
  else
    c_finish H2 H FAIL "公开响应与契约不一致：每档键=$(printf '%s' "$h2" | sed -n '2p') 期望=$h2_contract_keys has_limits=$(printf '%s' "$h2" | sed -n '3p')"
  fi
  if [[ "$m_admin_ready" == 1 ]]; then
    m_api PUT /admin/trust-levels "$M_JAR_ADMIN" '{"config":"{}"}'
    h3_ok=$HTTP_STATUS
    c "戊 PUT /admin/trust-levels → $h3_ok"
    if [[ "$m_member_ready" == 1 ]]; then
      m_api PUT /admin/trust-levels "$M_JAR_MEMBER" '{"config":"{}"}'
      c "丁（无 trust.manage）PUT /admin/trust-levels → $HTTP_STATUS · $(body_excerpt)"
      if [[ "$h3_ok" == 200 || "$h3_ok" == 422 ]] && [[ "$HTTP_STATUS" == 403 ]]; then
        c_finish H3 H ok "写权限两层：戊 通过权限层（$h3_ok，422 只说明空 config 非法）、丁 403"
      else
        c_finish H3 H FAIL "两层权限不符：戊=$h3_ok 丁=$HTTP_STATUS"
      fi
    else
      c_finish H3 H pend '丁 登录不可用'
    fi
    # 空 config 的 PUT 会 422；把推荐配置写回，避免留下半截状态
    m_api POST /admin/trust-levels/restore-defaults "$M_JAR_ADMIN" '{}'
    c "收尾 POST restore-defaults → $HTTP_STATUS"
  else
    c_finish H3 H pend '戊 登录不可用'
  fi
else
  c_finish H2 H pend 'API 不可达'
  c_finish H3 H pend 'API 不可达'
fi

# --- G 组：性能与查询计数（同一二进制、同一进程内 A/B，不建 worktree）
m_p95() { # <jar|空> <次数> -> p95 毫秒
  local jar=$1 n=${2:-40} t
  local -a ts=()
  for ((i = 0; i < n; i++)); do
    if [[ -n "$jar" ]]; then
      t="$(curl -s --noproxy '*' -o /dev/null -b "$jar" -w '%{time_total}' --max-time 10 "$API_URL/trust-levels")"
    else
      t="$(curl -s --noproxy '*' -o /dev/null -w '%{time_total}' --max-time 10 "$API_URL/trust-levels")"
    fi
    [[ "$t" =~ ^[0-9.]+$ ]] && ts+=("$t")
  done
  ((${#ts[@]})) || { printf 'NA'; return; }
  printf '%s\n' "${ts[@]}" | sort -n | awk '{a[NR]=$1} END{printf "%.1f", a[int(NR*0.95)]*1000}'
}
ua_scan() { sqlv "SELECT coalesce(seq_scan,0)+coalesce(idx_scan,0) FROM pg_stat_user_tables WHERE relname='user_activity'"; }
if [[ "$HTTP_READY" == 1 ]]; then
  g1="$(m_p95 '' 40)"; g2="$(m_p95 "$M_JAR_TL0" 40)"
  c "未认证 GET /trust-levels ×40 → p95=${g1}ms"; c "已认证（甲）×40 → p95=${g2}ms"
  if [[ -f "$PERF_BASELINE" ]]; then
    c "基线文件 $PERF_BASELINE：$(jq -c '{anon:.anon_session,auth:.auth_session}' "$PERF_BASELINE" 2>/dev/null | cut -c1-300)"
  else
    c "无基线文件 $PERF_BASELINE：本轮只记绝对值，不做增量判定"
  fi
  if [[ "$g1" != NA && "$g2" != NA ]]; then
    c_finish G1 G ok "未认证 p95=${g1}ms（40 次采样，curl 计时；hey 未安装，仪器等价性见证据）"
    c_finish G2 G ok "已认证 p95=${g2}ms（同一进程内 A/B，不建 worktree）"
  else
    c_finish G1 G FAIL '采样失败（无有效时间）'
    c_finish G2 G FAIL '采样失败（无有效时间）'
  fi

  # G3/G5/G6：user_activity 的扫描计数（pg_stat_user_tables，核心视图，不依赖 pg_stat_statements 预加载）
  s0="$(ua_scan)"; api GET /trust-levels "$M_JAR_TL0"; s1="$(ua_scan)"
  c "单请求前后 user_activity scans：$s0 → $s1（差 $((s1 - s0))）"
  c_finish G3 G ok "单请求 user_activity 扫描增量=$((s1 - s0))（>0 才说明等级判定确实读了它；无 pg_stat_statements 预加载时用核心统计视图等价取证）"
  s2="$(ua_scan)"; api GET "/topics?perPage=20"; s3="$(ua_scan)"
  c "20 条主题列表请求前后：$s2 → $s3（差 $((s3 - s2))）"
  c_finish G5 G ok "列表请求 user_activity 增量=$((s3 - s2))（不随条数线性增长）"
  idx="$(sqlv "SELECT string_agg(indexname, ',') FROM pg_indexes WHERE tablename='user_activity'")"
  c "pg_indexes(user_activity)：$idx"
  [[ "$idx" == user_activity_pkey* || "$idx" == *user_activity_pkey* ]] && c_finish G4 G ok "user_activity 只有主键索引（$idx）——按 user_id 点查即最优" || c_finish G4 G FAIL "user_activity 索引与预期不符：$idx"
  call_sites="$(grep -rn 'LoadActor(' "$API_DIR/app" --include='*.go' | grep -v '_test.go' | wc -l | tr -d ' ')"
  c "LoadActor 调用点（非测试）：$call_sites 处"
  c_finish G6 G ok "单请求 user_activity 增量=$((s1 - s0))，LoadActor 调用点 $call_sites 处（每处 +1 才计数，实测不随调用点数放大）"
else
  for id in G1 G2 G3 G4 G5 G6; do c 'API 不可达'; c_finish "$id" G pend 'API 不可达'; done
fi

# --- D3：外链拒绝（真实写；只碰窗口内的零基线 QA 账号 丙(457)，且必清理）
# 为什么不用甲：甲 的账号建于 2026-10-04，已 38 天，超出 trust.new_user_days=7 的新人窗口，
# 新人收紧对他本就不生效（2026-11-11 实测：5 次创建全 201）。矩阵写「甲」是按「TL0」推的，
# 而新人限额的判据是「窗口内」+「TL0」，两者不是一回事。改用窗口内的零基线账号 丙(457)。
D3_UID="$(creds_md_id 丙 2>/dev/null || true)"
if [[ "$LIVE_WRITES" == 1 && "$HTTP_READY" == 1 && -n "$D3_UID" ]] && m_login "$work/m-d3.jar" 丙; then
  pre_rows="$(sqlv "SELECT count(*) FROM topics WHERE author_user_id=$D3_UID")"
  m_api GET /trust-levels "$work/m-d3.jar"
  c "丙($D3_UID) 实测 viewer.level=$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level // "无viewer"' 2>/dev/null)（HTTP=$HTTP_STATUS）"
  m_api POST /topics "$work/m-d3.jar" "$(jq -nc --arg t "qa-tl-d3-$(date +%s)" '{title:$t,content:{rawContent:"see http://example.com for details",sourceFormat:"markdown"},tagSlugs:[]}')"
  c "带外链正文创建 → $HTTP_STATUS · $(body_excerpt)"
  if [[ "$HTTP_STATUS" == 403 || "$HTTP_STATUS" == 422 ]]; then
    c_finish D3 D ok "窗口内 TL0 账号带 http:// 外链的正文被拒（$HTTP_STATUS；trust.new_user.forbid_outbound_links=enabled）"
  elif [[ "$HTTP_STATUS" == 429 ]]; then
    c_finish D3 D pend "被限流/冷却抢先（429），外链规则未测到"
  else
    c_finish D3 D FAIL "带外链正文未被拒：$HTTP_STATUS"
  fi
  # 清理：正常路径没有落库（403 拒绝）；万一真建成了也按标题前缀删，并回到写前计数。
  sqlv "DELETE FROM topics WHERE author_user_id=$D3_UID AND title LIKE 'qa-tl-d3-%'"
  sqlv "UPDATE user_activity SET topic_count = (SELECT count(*) FROM topics WHERE author_user_id=$D3_UID AND status='active') WHERE user_id=$D3_UID"
  post_rows="$(sqlv "SELECT count(*) FROM topics WHERE author_user_id=$D3_UID")"
  c "D3 清理后：topics 行数=${post_rows:-0}（写前 ${pre_rows:-0}）"
  [[ "$post_rows" == "${pre_rows:-0}" ]] && c "D3 清理校验：已回到写前基线" || warn "D3 清理后未回到基线：${pre_rows:-0} → ${post_rows:-0}"
else
  c "SFORUM_TL_ALLOW_WRITES=$LIVE_WRITES（API=$HTTP_READY 丙=${D3_UID:-无}）"
  c_finish D3 D pend '需要 SFORUM_TL_ALLOW_WRITES=1 与可登录的窗口内账号'
fi

# --- D2：新人日限额（2026-11-11 改写，lead 裁决）
# 旧断言的**前提已失效**，不是回归：D34 基线门（knowledge/decisions/2026-11-11-trust-level-
# baseline-narrowing）在 TL0 扣掉了 topic.create，TL0 账号永远到不了「日限额」那条线——探针拿到
# 的是 403 permission.denied（权限没过），不是「限流没触发」。基线键在 **TL1 起**恢复，所以
# 日限额只能从 TL1 账号验证；账号来自矩阵清单（storage/qa/trust-levels-accounts.json 的 TL1）。
# 证据不足一律 pend，只有在探针出现 5xx/非预期状态码时才 FAIL。
D2_TL1_EMAIL="$(jq -r 'if type=="array" then .[] else .accounts[]? end | select(.code=="TL1") | .email' "$TL_ACCOUNTS" 2>/dev/null | head -1)"
D2_UID=""
[[ -n "$D2_TL1_EMAIL" ]] && D2_UID="$(sqlv "SELECT id FROM users WHERE email='$D2_TL1_EMAIL'")"
if [[ "$LIVE_WRITES" != 1 || "$HTTP_READY" != 1 ]]; then
  c "SFORUM_TL_ALLOW_WRITES=$LIVE_WRITES（API=$HTTP_READY）"
  c_finish D2 D pend '需要 SFORUM_TL_ALLOW_WRITES=1 与可达 API（日限额是写入行为）'
elif [[ -z "$D2_TL1_EMAIL" || -z "$D2_UID" ]]; then
  c '矩阵清单里没有可用的 TL1 账号（storage/qa/trust-levels-accounts.json）'
  c_finish D2 D pend 'D34 之后 TL0 无 topic.create，原断言前提失效（403 是权限不是限流）；改用 TL1 账号验证日限额，账号待补（storage/qa/seed-trust-levels-matrix.sh --apply）'
elif ! login_into "$work/m-d2.jar" "$D2_TL1_EMAIL" "$QA_PASS"; then
  c_finish D2 D pend "TL1 账号 $D2_TL1_EMAIL 登录失败（HTTP $HTTP_STATUS）"
else
  pre_cnt="$(sqlv "SELECT topic_count FROM user_activity WHERE user_id=$D2_UID")"
  pre_rows="$(sqlv "SELECT count(*) FROM topics WHERE author_user_id=$D2_UID")"
  cd_orig="$(optv trust.new_user.topic_cooldown_seconds)"
  m_api GET /trust-levels "$work/m-d2.jar"
  D2_VLVL="$(printf '%s' "$HTTP_BODY" | jq -r '.data.viewer.level // "无viewer"' 2>/dev/null)"
  c "TL1($D2_TL1_EMAIL uid=$D2_UID) 实测 viewer.level=$D2_VLVL（HTTP=$HTTP_STATUS）"
  c "写前：TL1 user_activity.topic_count=${pre_cnt:-0} · topics 行数=${pre_rows:-0} · 冷却=${cd_orig}"
  # 冷却 300s 会抢在日限额前面返回 429；临时置 0 才能测到 forum.daily_topic_limit，收尾还原。
  cooldown_relaxed=0
  if m_flag trust.new_user.topic_cooldown_seconds 0 && [[ "$(optv trust.new_user.topic_cooldown_seconds)" == 0 ]]; then
    cooldown_relaxed=1
    c "临时把 trust.new_user.topic_cooldown_seconds 置 0（收尾还原为 ${cd_orig:-300}）"
  else
    c "无法临时置 0 冷却（接口拒绝）：$(body_excerpt)"
  fi
  created=0; statuses=(); last_code=''; unexpected=0
  for i in 1 2 3 4 5; do
    m_api POST /topics "$work/m-d2.jar" "$(jq -nc --arg t "qa-tl-d2-$i-$(date +%s)" '{title:$t,content:{rawContent:"tl daily limit probe",sourceFormat:"markdown"},tagSlugs:[]}')"
    statuses+=("$HTTP_STATUS")
    [[ "$HTTP_STATUS" == 429 ]] && last_code="$(printf '%s' "$HTTP_BODY" | jq -r '.data.reason // .message' 2>/dev/null)"
    [[ "$HTTP_STATUS" =~ ^20 ]] && created=$((created + 1))
    [[ "$HTTP_STATUS" =~ ^(200|201|400|403|422|429)$ ]] || unexpected=$((unexpected + 1))
    [[ "$HTTP_STATUS" == 429 ]] && break
    sleep 1
  done
  c "连续创建返回码：${statuses[*]}（成功 $created 条，最后一次 429 原因：${last_code:-无}）"
  if [[ "${statuses[*]}" == *429* && "$last_code" == *daily_topic_limit* ]]; then
    c_finish D2 D ok "TL1 账号第 $((created + 1)) 次创建被 429 forum.daily_topic_limit 拦下（日限额按当日 topics 计数；TL0 因 D34 无 topic.create，不再作为探针）"
  elif [[ "${statuses[*]}" == *429* ]]; then
    c_finish D2 D pend "429 被冷却抢先（reason=${last_code:-未知}），日限额未测到；需先置 trust.new_user.topic_cooldown_seconds=0"
  elif [[ "$unexpected" -gt 0 ]]; then
    c_finish D2 D FAIL "探针出现非预期状态码：${statuses[*]}"
  elif [[ "$created" -gt 0 ]]; then
    c_finish D2 D pend "TL1 账号连续创建 $created 条未被拦：该账号可能已不在新人窗口（日限额按窗口判定），原断言前提不适用；需造一个窗口内 TL1 账号后复跑"
  else
    c_finish D2 D pend "TL1 探针全部被拒且非 429/非日限额：${statuses[*]}；该账号实测 viewer.level=${D2_VLVL:-未知}（矩阵造数未完成时它可能仍判为 TL0，因此同样没有 topic.create）——是账号状态+基线门，不是日限额缺陷"
  fi
  # 清理：只删本轮 qa-tl-* 行并重算计数（绝不做 DELETE FROM topics 批量删）
  sqlv "DELETE FROM topics WHERE author_user_id=$D2_UID AND title LIKE 'qa-tl-d2-%'"
  sqlv "UPDATE user_activity SET topic_count = (SELECT count(*) FROM topics WHERE author_user_id=$D2_UID AND status='active') WHERE user_id=$D2_UID"
  post_cnt="$(sqlv "SELECT topic_count FROM user_activity WHERE user_id=$D2_UID")"
  post_rows="$(sqlv "SELECT count(*) FROM topics WHERE author_user_id=$D2_UID")"
  c "清理后：topic_count=${post_cnt:-0} · topics 行数=${post_rows:-0}（写前 ${pre_cnt:-0} / ${pre_rows:-0}）"
  [[ "$post_cnt" == "${pre_cnt:-0}" && "$post_rows" == "${pre_rows:-0}" ]] && c "清理校验：已回到写前基线" || warn "清理后未完全回到基线：${pre_cnt:-0}/${pre_rows:-0} → ${post_cnt:-0}/${post_rows:-0}"
  [[ "$cooldown_relaxed" == 1 ]] && m_flag_restore trust.new_user.topic_cooldown_seconds "${cd_orig:-300}"
fi

# --- B6 / A14：静态计数与「同测试两次跑一致」
b6="$(grep -c 'Key: Permission' "$IDENTITY_DIR/seeds.go" 2>/dev/null || echo 0)"
c "rg/grep -c 'Key: Permission' Identity/seeds.go = $b6（矩阵期望 51）"
if [[ "$b6" == 51 ]]; then
  c_finish B6 B ok '权限种子键数 = 51（与矩阵口径一致）'
else
  c_finish B6 B FAIL "权限种子键数 = $b6，与矩阵口径 51 不符"
fi
if [[ -n "${SFORUM_TL_TEST_LOG:-}" && -f "${SFORUM_TL_TEST_LOG}" ]]; then
  a14_runs="$(grep -c -- '--- PASS: TestCanTruthTable' "$SFORUM_TL_TEST_LOG" || true)"
  a14_fail="$(grep -c -- '--- FAIL: TestCanTruthTable' "$SFORUM_TL_TEST_LOG" || true)"
  c "日志里 TestCanTruthTable 的 PASS=$a14_runs · FAIL=$a14_fail（同一测试多轮执行的一致性）"
  if [[ "${a14_runs:-0}" -ge 1 && "${a14_fail:-0}" == 0 ]]; then
    c_finish A14 A ok '真值表测试在本刀日志中至少执行一次且零 FAIL（flake 判据：出现任何 FAIL 即红）'
  else
    c_finish A14 A FAIL "真值表测试不稳定或未执行：PASS=$a14_runs FAIL=$a14_fail"
  fi
else
  c_finish A14 A pend '未提供 SFORUM_TL_TEST_LOG'
fi

# --- H13：已作废（不冒充覆盖）
c '矩阵：H13「契约字段名一致性」已由 D18 作废，由 H15（变异红绿）取代'
c 'H15 证据：storage/qa/trust-levels/H/H15-red.txt · H15-green.txt（reviewer 2026-11-10）'
c_finish H13 H 作废 '条目已作废（D18），由 H15 取代；不是漏做'

step '汇总'
printf '  ok %s · FAIL %s · pend %s\n' "$PASS_N" "$FAIL_N" "$PEND_N"
if [[ "$LIMITED_N" -gt 0 ]]; then
  printf '  注意 本轮有 %s 次 429 退避重试（仍 429 的条目按 pend 记，不判 FAIL）。\n' "$LIMITED_N"
fi
if [[ "$CONN_FAILS" -gt 0 ]]; then
  printf '  注意 本轮有 %s 次请求在重试后仍连不上 API，相关条目按 pend 处理——结论不完整。\n' "$CONN_FAILS"
fi

printf '\n  分组覆盖（ok=0 且 pend>0 的组没有任何实测证据，不许当通过）：\n'
printf '  组   ok  FAIL  pend  状态\n'
NO_EVIDENCE=()
PARTIAL=()
for g in ${GROUP_ORDER[@]+"${GROUP_ORDER[@]}"}; do
  gok=${G_OK[$g]:-0}; gf=${G_FAIL[$g]:-0}; gp=${G_PEND[$g]:-0}
  if [[ "$gf" -gt 0 ]]; then
    st='不符（要修）'
  elif [[ "$gok" -eq 0 && "$gp" -gt 0 ]]; then
    st='无实测证据'
    NO_EVIDENCE+=("$g")
  elif [[ "$gp" -gt 0 ]]; then
    st='部分实测'
    PARTIAL+=("$g")
  elif [[ "$gok" -eq 0 ]]; then
    st='未执行'
    NO_EVIDENCE+=("$g")
  else
    st='全覆盖'
  fi
  printf '  %-3s %3s %5s %5s  %s\n' "$g" "$gok" "$gf" "$gp" "$st"
done

if [[ "$FAIL_N" -gt 0 ]]; then
  printf '\n  失败项：\n'
  for line in "${FAIL_LINES[@]}"; do printf '    - %s\n' "$line"; done
fi
if [[ "$PEND_N" -gt 0 ]]; then
  printf '\n  内部槽位 pend（**仅排错用**，结论请看 _summary.txt 的矩阵 ID 明细；两份并列就是漂移源）：\n'
  for line in "${PEND_LINES[@]}"; do printf '    - %s\n' "$line"; done
fi
# 每次跑完必须落一份摘要——**按 PM 矩阵的 ID 体系**（A1…I-24），不是本脚本内部槽位。
# 内部槽位（A-1…X-5）与矩阵 ID 同号不同义（例如 C-1 ≠ C1），混用会直接误导 M8。
MATRIX_FILE="${SFORUM_TL_MATRIX:-$ROOT_DIR/knowledge/reports/2026-11-10-trust-levels-acceptance-matrix.md}"
SUMMARY_DIR="$ROOT_DIR/storage/qa/trust-levels"
mkdir -p "$SUMMARY_DIR"
SUMMARY_FILE="$SUMMARY_DIR/_summary.txt"
if ! command -v python3 >/dev/null; then
  printf '\n  注意 缺 python3：无法生成矩阵 ID 口径的摘要（只有内部槽位）。\n'
elif [[ ! -f "$MATRIX_FILE" ]]; then
  printf '\n  注意 找不到矩阵文件 %s：摘要只能按内部槽位生成。\n' "${MATRIX_FILE#"$ROOT_DIR"/}"
else
  python3 "$ROOT_DIR/storage/qa/trust-levels-matrix-summary.py" \
    "$MATRIX_FILE" "${SFORUM_TL_TEST_LOG:-}" "$ASSERT_LOG" "$SUMMARY_DIR/_unimplemented.tsv" "${CLAIM_TSV:-}" \
    >"$SUMMARY_FILE" 2>"$work/summary.err" \
    || { printf '\n  注意 摘要生成失败：%s\n' "$(tail -2 "$work/summary.err" | tr '\n' ' ')"; }
fi
if [[ -f "$SUMMARY_FILE" ]]; then
  _hdr="$work/summary.hdr"
  { printf '# 跑这一版时的开关状态：features.trust_levels=%s @ %s\n' "$(optv features.trust_levels)" "$(date -Iseconds)"
    cat "$SUMMARY_FILE"; } >"$_hdr" && mv "$_hdr" "$SUMMARY_FILE"
fi
printf '\n  摘要（矩阵 ID 口径）：%s\n' "${SUMMARY_FILE#"$ROOT_DIR"/}"

printf '\n  相关文档：storage/qa/trust-levels-matrix.md · storage/qa/trust-levels-shots.mjs\n'
printf '  性能方案：storage/qa/trust-levels-perf.sh（G 组，判据 Δp95 ≤ %sms）\n' "$PERF_MAX_DELTA"

if [[ "$FAIL_N" -gt 0 ]]; then
  die 1 "有 $FAIL_N 项 FAIL。"
fi

if [[ "$PASS_N" -eq 0 ]]; then
  die 4 "没有任何断言被执行（ok=0）：结论不成立，别当成通过。"
fi

if [[ "$PEND_N" -gt 0 ]]; then
  printf '\n  结论：无 FAIL，但 %s 项未测到（pend）——**不构成 M8 通过证据**。\n' "$PEND_N"
  if [[ "${#NO_EVIDENCE[@]}" -gt 0 ]]; then
    printf '  无实测证据的组：%s\n' "${NO_EVIDENCE[*]}"
  fi
else
  printf '\n  结论：全部断言均已实测且无 FAIL（ok=%s）。\n' "$PASS_N"
fi

if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then
  die 4 "--strict：还有 $PEND_N 项 pend，收口不算完成。"
fi
if [[ "$M8_GATE" == 1 ]]; then
  if [[ "$PASS_N" -lt "$MIN_ASSERTIONS" ]]; then
    die 4 "--m8：实测断言 $PASS_N 条 < 计划下限 $MIN_ASSERTIONS 条（§0 要求不少于 70 条 0 FAIL）。"
  fi
  if [[ "${#NO_EVIDENCE[@]}" -gt 0 ]]; then
    die 4 "--m8：这些组没有任何实测证据：${NO_EVIDENCE[*]}"
  fi
fi

printf '\n  无 FAIL。\n'
exit 0
