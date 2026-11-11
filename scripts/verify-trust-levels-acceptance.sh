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
OPENAPI_TL="$ROOT_DIR/contracts/openapi/paths/trust-levels.yaml"
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
cleanup() {
  local status=$?
  trap - EXIT
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
login_any_admin_reader() { # <jar> -> 成功时打印代号
  local jar=$1 code email
  for code in 戊 丁b; do
    email="$(creds_md_email "$code" 2>/dev/null || true)"
    [[ -n "$email" ]] || continue
    if login_into "$jar" "$email" "$QA_PASS"; then
      probe_logged_in=$((probe_logged_in + 1)) printf '%s' "$code"; return 0; fi
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
    fail 'A-3 隔离库测试：本刀相关用例确实执行过' "日志里找不到这些用例的 === RUN：${missing_tests[*]:-（无）}${no_tests_marker:+；日志含 'no tests to run'}。日志必须用 -v 生成：./scripts/test-api-isolated.sh -v > /tmp/iso.log 2>&1"
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
  if [[ "$HTTP_STATUS" == 200 ]]; then
    n_levels="$(printf '%s' "$HTTP_BODY" | jq -r '(.data // .) | (.levels // []) | length' 2>/dev/null)"
    if [[ "${n_levels:-0}" == 5 ]]; then
      pass "C-4 公开等级目录返回 5 档（levels=$n_levels）"
    else
      fail 'C-4 公开等级目录返回 5 档' "HTTP 200 但 levels 数量 = ${n_levels:-0}（D10 是五档）"
    fi
  elif [[ "$HTTP_STATUS" == 429 ]]; then
    pend 'C-4 公开等级目录返回 5 档' '退避重试后仍 429（限流，不是产品行为）'
  elif phase_ready tl4; then
    fail 'C-4 公开等级目录返回 5 档' "HTTP $HTTP_STATUS（$(body_excerpt)）"
  else
    pend 'C-4 公开等级目录返回 5 档' "$(phase_reason tl4)"
  fi
else
  pend 'C-4 公开等级目录返回 5 档' 'API 不可达'
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
    [[ "$code" =~ ^TL[0-4]$ ]] || continue
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
  done < <(jq -r '.[] | select(.code|test("^TL[0-4]$")) | [.code, .email, .expected] | @tsv' "$TL_ACCOUNTS" 2>/dev/null)
  if [[ "$probe_total" -gt 0 && "$probe_logged_in" == 0 ]]; then
    pend 'C-5 矩阵账号等级分布符合预期' "矩阵 $probe_total 个账号一个都登不上：造数未完成（seeder --apply），不是等级不符"
  elif [[ "$probe_total" == 0 ]]; then
    pend 'C-5 矩阵账号等级分布符合预期' '账号清单里没有 TL0–TL4 行'
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
if [[ -d "$TL_DIR" ]]; then
  if grep -rlE 'deleted_at|soft.?delete|软删' "$TL_DIR" --include='*.go' 2>/dev/null | grep -v '_test\.go' | grep -q .; then
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
    pend 'C-12 后台读面带 limits 且四字段齐全' "戊 / 丁b 都登不上（末次 HTTP $HTTP_STATUS：$(body_excerpt)）"
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
if has_glob 'apps/web/app/**/*rust*' && grep -rqE '不影响.*(已获得|已得)|已获得的内容' "$WEB_DIR/app" 2>/dev/null; then
  pass 'D-6 恢复默认的 UI 文案声明不影响已获得内容（D12）'
else
  pend 'D-6 恢复默认的 UI 文案声明不影响已获得内容（D12）' '等前端改文案后重跑：页面已渲染（tl5 已落地），但当前文案不含「不影响已获得的内容」'
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

# H-4 i18n 五档双语（D10）
i18n_hit=0
for loc in zh-CN en-US; do
  f="$WEB_DIR/i18n/locales/$loc.json"
  [[ -f "$f" ]] || continue
  if grep -qE '新成员|New Member|trustLevel|trust_level' "$f" 2>/dev/null; then
    i18n_hit=$((i18n_hit + 1))
  fi
done
if [[ "$i18n_hit" == 2 ]]; then
  pass 'H-4 五档等级名 zh-CN + en-US 双语齐全'
else
  pend 'H-4 五档等级名 zh-CN + en-US 双语齐全' "$(phase_reason tl5)（命中 $i18n_hit/2 个 locale）"
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
  printf '\n  待落地/待人工项（pend 不是通过）：\n'
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
    "$MATRIX_FILE" "${SFORUM_TL_TEST_LOG:-}" "$ASSERT_LOG" "$SUMMARY_DIR/_unimplemented.tsv" \
    >"$SUMMARY_FILE" 2>"$work/summary.err" \
    || { printf '\n  注意 摘要生成失败：%s\n' "$(tail -2 "$work/summary.err" | tr '\n' ' ')"; }
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
