#!/usr/bin/env bash
# 徽章规则引擎（badge rule engine）M1 独立验收验证器。
#
# 断言来源：knowledge/reports/2026-11-12-badge-rule-engine-acceptance.md
# 计划：    knowledge/plans/2026-11-12-badge-rule-engine.md
#
# 三种结果（与 scripts/verify-badges-acceptance.sh 同口径）：
#   ok    通过
#   FAIL  代码已落地、行为不符判据（要修）
#   pend  对应切片未落地 / 前置不满足（夹具缺、API 不可达、需人工步骤），不算通过
#
# 硬约定：
#   * 本脚本**不接**进 ./scripts/test.sh，也不属于 tests/validate-* 门禁。
#     未落地的断言在这里是 pend，接进门禁就会把整条门禁变红。
#   * FAIL 只留给「代码在、行为错」。缺文件、缺夹具、缺会话一律 pend 并写明缺什么。
#   * **包无法编译**记 pend（并行开发期的瞬时态）；收口时这类 pend 必须按 FAIL 处理，用 --strict。
#
# 退出码：
#   0  无 FAIL（允许 pend）
#   1  有 FAIL
#   2  预检失败（缺 go / 参数非法）
#   4  --strict 下存在 pend
#
# 环境变量：
#   DATABASE_URL                  可选。缺省时所有查库断言 pend（纯函数断言仍会跑）。
#   SFORUM_ACCEPT_API_URL         默认 http://127.0.0.1:8080/api/v1（公开面断言用）。
#   SFORUM_ACCEPT_PSQL_IMAGE      无本机 psql 时的镜像，默认 postgres:17-alpine。
#   SFORUM_ACCEPT_SQL_CMD         自定义查库命令（覆盖 psql/docker 探测）。
#
# 用法：
#   ./scripts/verify-badge-rules-acceptance.sh
#   ./scripts/verify-badge-rules-acceptance.sh --strict
#   DATABASE_URL='postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable' \
#     ./scripts/verify-badge-rules-acceptance.sh
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
WEB_DIR="$ROOT_DIR/apps/web"
DB_URL="${DATABASE_URL:-}"
API_URL="${SFORUM_ACCEPT_API_URL:-http://127.0.0.1:8080/api/v1}"
STRICT=0

usage() { sed -n '2,32p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done

PASS_N=0
FAIL_N=0
PEND_N=0
FAIL_LINES=()
PEND_LINES=()

step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# ---------------------------------------------------------------- 切片探测
#
# 判定只看「文件是否存在」。这是刻意的：脚本的职责是给出 pend，而不是猜实现者
# 打算用什么文件名。新增文件时同步这里即可。

MIGRATION_FILE="$API_DIR/database/migrations/202611120002_badge_rules.sql"
RULEENGINE_FILE="$API_DIR/app/Support/RuleEngine/eval.go"
CONDITIONS_FILE="$API_DIR/app/Models/Badges/conditions/registry.go"
RULE_STORE_FILE="$API_DIR/app/Models/Badges/rule_store.go"
ROUTES_TEST="$API_DIR/app/Support/Routes/registry_test.go"
REVERSIBLE_SCRIPT="$ROOT_DIR/scripts/verify-migration-reversible.sh"

slice_m11=0; [[ -f "$MIGRATION_FILE" ]] && slice_m11=1
slice_m12=0; [[ -f "$RULEENGINE_FILE" ]] && slice_m12=1
slice_m13=0; [[ -f "$CONDITIONS_FILE" ]] && slice_m13=1
slice_m14=0; [[ -f "$RULE_STORE_FILE" ]] && slice_m14=1

step "预检"
command -v go >/dev/null || die 2 '缺少 go。'
note "仓库：$ROOT_DIR"
note "切片落地：M1.1=${slice_m11} M1.2=${slice_m12} M1.3=${slice_m13} M1.4=${slice_m14}"

# 查库通道：本机 psql -> docker 里的 psql -> 自定义命令。与 verify-badges-acceptance.sh 同款。
SQL_RUNNER=()
if [[ -z "$DB_URL" ]]; then
  note 'DATABASE_URL 未设置：所有查库断言将 pend（纯函数断言照常执行）。'
elif [[ -n "${SFORUM_ACCEPT_SQL_CMD:-}" ]]; then
  SQL_RUNNER=(bash -c "$SFORUM_ACCEPT_SQL_CMD")
elif command -v psql >/dev/null; then
  SQL_RUNNER=(psql "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
elif command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  SQL_RUNNER=(docker run --rm -i --network=host --entrypoint psql "${SFORUM_ACCEPT_PSQL_IMAGE:-postgres:17-alpine}" "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
  note "本机无 psql，改用 docker 里的 psql（镜像 ${SFORUM_ACCEPT_PSQL_IMAGE:-postgres:17-alpine}）。"
else
  note '查库需要 psql 或可用的 docker（或 SFORUM_ACCEPT_SQL_CMD）：所有查库断言 pend。'
  DB_URL=''
fi

SQL_LAST_ERROR=''
sqlv() {
  local out rc
  [[ ${#SQL_RUNNER[@]} -gt 0 ]] || return 3
  out="$("${SQL_RUNNER[@]}" -c "$1" 2>"$work/sql.err")"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    SQL_LAST_ERROR="$(tr '\n' ' ' <"$work/sql.err" | cut -c1-300)"
    return $rc
  fi
  printf '%s' "$out" | tr -d '\r' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'
}

db_ready=0
if [[ -n "$DB_URL" ]]; then
  if [[ "$(sqlv 'SELECT 1' 2>/dev/null)" == 1 ]]; then
    db_ready=1
    note "目标库：$(sqlv 'SELECT current_database()')"
    [[ "$(sqlv "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL")" == t ]] \
      && note '开发库哨兵命中：本脚本只读，不造数据，可以继续。'
  else
    note "无法查库（${SQL_LAST_ERROR:-无输出}）：所有查库断言 pend。"
  fi
fi

# ---------------------------------------------------------------- go test 断言通道
#
# 一次编译跑一组测试，再按测试名逐条映射结果：
#   --- PASS: Name  -> ok
#   --- FAIL: Name  -> FAIL
#   名字不出现      -> pend（测试被改名或未落地，不算失败）
run_pkg_tests() {
  local label=$1 pkg=$2 pattern=$3 out="$work/$4"
  ( cd "$API_DIR" && go test -count=1 -v -run "$pattern" "$pkg" ) >"$out" 2>&1
  local rc=$?
  if [[ $rc -ne 0 ]] && ! grep -qE '^--- (PASS|FAIL): ' "$out"; then
    # 包无法编译时记 pend 而不是 FAIL：开发期多个切片并行，编译错误常常是「正在编辑」
    # 的瞬时态；收口时若有这条 pend，人工必须按 FAIL 处理（见 --strict）。
    printf '%s' "$out" | tail -5 >&2
    pend "$label" "包无法编译（可能正在编辑）：$(grep -m1 -E 'cannot|undefined|declared|build failed' "$out" | cut -c1-120)"
    return 1
  fi
  return 0
}

# expect_test <断言ID> <说明> <输出文件> <测试名>
expect_test() {
  local id=$1 desc=$2 out=$3 name=$4
  if grep -qE "^--- PASS: ${name}([[:space:]]|$)" "$out"; then
    pass "$id $desc（${name}）"
  elif grep -qE "^--- FAIL: ${name}([[:space:]]|$)" "$out"; then
    fail "$id $desc" "测试 ${name} 失败"
  else
    pend "$id $desc" "测试 ${name} 不在输出里（改名或未落地）"
  fi
}

# ================================================================ M1.2 纯 AST
step 'M1.2 Support/RuleEngine（纯函数，无 DB）'
if [[ "$slice_m12" != 1 ]]; then
  for id in 01 02 03 04 05 06 07; do
    pend "M1.2-$id" 'RuleEngine 未落地（缺 app/Support/RuleEngine/eval.go）'
  done
else
  run_pkg_tests 'M1.2 编译' ./app/Support/RuleEngine/ \
    '^(TestDecodeRejectsUnknownFields|TestEvalTruthTable|TestValidate(Depth|Leaf|Node)Boundary|TestValidate.*DaysRangeIndependentOfSchema|TestValidateConditionAndCompare|TestEvalNotOfUnknownStaysUnknown)$' m12.out

  expect_test 'M1.2-01' '未知字段解码报错' "$work/m12.out" 'TestDecodeRejectsUnknownFields'
  expect_test 'M1.2-02' 'all/any/not 真值表与嵌套' "$work/m12.out" 'TestEvalTruthTable'
  if grep -qE '^--- PASS: TestValidate(Depth|Leaf|Node)Boundary' "$work/m12.out" \
     && grep -qE '^--- PASS: TestValidate.*DaysRangeIndependentOfSchema' "$work/m12.out"; then
    pass 'M1.2-03 深度/叶子/节点/天数上限'
  elif grep -qE '^--- FAIL: TestValidate' "$work/m12.out"; then
    fail 'M1.2-03 上限断言' 'TestValidate*Boundary 失败'
  else
    pend 'M1.2-03 上限断言' '边界测试未落地'
  fi
  if grep -qE '^    --- PASS: TestValidateConditionAndCompare/未知条件' "$work/m12.out"; then
    pass 'M1.2-04 未知条件在校验期拒绝'
  elif grep -qE '^    --- FAIL: TestValidateConditionAndCompare/未知条件' "$work/m12.out"; then
    fail 'M1.2-04 未知条件' '子测试失败'
  else
    pend 'M1.2-04 未知条件' '子测试名不在输出里'
  fi
  if grep -qE '^    --- PASS: TestValidateConditionAndCompare/(布尔条件带_cmp|不允许的比较运算)' "$work/m12.out"; then
    pass 'M1.2-05 cmp 白名单（布尔禁止 / 计数按目录）'
  elif grep -qE '^    --- FAIL: TestValidateConditionAndCompare/' "$work/m12.out"; then
    fail 'M1.2-05 cmp 白名单' '子测试失败'
  else
    pend 'M1.2-05 cmp 白名单' '子测试名不在输出里'
  fi
  expect_test 'M1.2-06' 'not(unknown) 仍为 unknown（fail-closed 不被反转）' "$work/m12.out" 'TestEvalNotOfUnknownStaysUnknown'

  deps="$( cd "$API_DIR" && go list -deps ./app/Support/RuleEngine 2>/dev/null | grep -E 'Models/(Badges|Forum|Identity)' || true )"
  if [[ -z "$deps" ]]; then
    pass 'M1.2-07 RuleEngine 不依赖任何 Models 领域包'
  else
    fail 'M1.2-07 纯函数包' "依赖了领域包：$(printf '%s' "$deps" | tr '\n' ' ')"
  fi
fi

# ================================================================ M1.1 迁移
step 'M1.1 迁移 / 视图 / 回归'
M11_ITEMS=(
  '01|迁移已应用且是最新一条'
  '02|badges 三列存在 + badges_rule_check 改为 rule 形态'
  '03|内置规则转换逐枚等价'
  '05|badge_rules_rollback 在 Up 段写入'
  '06|forum_badges 视图输出 rule 与双列 summary'
  '07|forum_user_badge_facts 白名单'
  '10|逐人逐规则真值等价（该有却没有 = 0）'
  '11|授予行与迁移前快照逐行一致'
  '12|迁移未改动 user_activity'
)
pend_m11_all() {
  local item
  for item in "${M11_ITEMS[@]}"; do
    pend "M1.1-${item%%|*}" "${item#*|}：$1"
  done
}
if [[ "$slice_m11" != 1 ]]; then
  pend_m11_all '迁移未落地（缺 database/migrations/202611120002_badge_rules.sql）'
elif [[ "$db_ready" != 1 ]]; then
  pend_m11_all 'DATABASE_URL 未设置或不可用'
else
  version="$(sqlv "SELECT max(version_id) FROM goose_db_version WHERE is_applied")"
  if [[ "$version" == 202611120002 ]]; then
    pass "M1.1-01 迁移已应用（$version）"
  else
    fail 'M1.1-01 迁移版本' "最新已应用 = ${version:-?}，期望 202611120002"
  fi

  cols="$(sqlv "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='badges' AND column_name IN ('rule','requirement_summary_zh','requirement_summary_en')")"
  cdef="$(sqlv "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='badges_rule_check'")"
  if [[ "$cols" == 3 ]] && printf '%s' "$cdef" | grep -q 'rule IS NOT NULL'; then
    pass 'M1.1-02 三列存在且 badges_rule_check 改为 rule 形态'
  else
    fail 'M1.1-02 badges 列/约束' "三列命中 ${cols:-0}/3；约束=${cdef:-<缺失>}"
  fi

  auto_n="$(sqlv "SELECT count(*) FROM badges WHERE grant_mode='automatic'")"
  # 形状无关：root 可以直接是叶子（迁移实际写法），也可以是 all 包一个叶子（计划 §5.1 示例）。
  # 两种都被引擎接受，断言只认「生效的那个叶子」是否与 legacy metric/threshold 等价。
  mismatch="$(sqlv "WITH eff AS (
      SELECT b.id, b.metric, b.threshold,
             CASE WHEN b.rule->'root'->>'type' IS NOT NULL THEN b.rule->'root'
                  ELSE b.rule->'root'->'children'->0 END AS leaf,
             CASE WHEN b.rule->'root'->>'op' = 'all'
                  THEN jsonb_array_length(COALESCE(b.rule->'root'->'children','[]'::jsonb))
                  ELSE 1 END AS child_n
      FROM badges b WHERE b.grant_mode='automatic')
    SELECT count(*) FROM eff WHERE leaf IS NULL OR child_n <> 1
       OR leaf->>'type' IS DISTINCT FROM 'activity.count'
       OR leaf->'params'->>'metric' IS DISTINCT FROM metric
       OR leaf->'params'->'window'->>'kind' IS DISTINCT FROM 'all'
       OR leaf->'cmp'->>'op' IS DISTINCT FROM 'gte'
       OR (leaf->'cmp'->>'value')::bigint IS DISTINCT FROM threshold")"
  if [[ -z "$auto_n" || "$auto_n" == 0 ]]; then
    pend 'M1.1-03 内置规则转换逐枚等价' '库里没有 automatic 徽章（夹具空）'
  elif [[ "$mismatch" == 0 ]]; then
    pass "M1.1-03 规则转换逐枚等价（${auto_n} 枚 automatic）"
  else
    fail 'M1.1-03 规则转换' "${mismatch}/${auto_n} 枚与 legacy metric/threshold 不等价"
  fi

  if [[ "$(sqlv "SELECT to_regclass('public.badge_rules_rollback') IS NOT NULL")" == t ]]; then
    rollback_n="$(sqlv 'SELECT count(*) FROM badge_rules_rollback')"
    if [[ "$rollback_n" == "$auto_n" ]]; then
      pass "M1.1-05 回滚表已写入（${rollback_n} 行）"
    else
      fail 'M1.1-05 回滚表行数' "${rollback_n:-?} 行，期望等于 automatic 徽章数 ${auto_n:-?}"
    fi
  else
    fail 'M1.1-05 回滚表' 'badge_rules_rollback 不存在（计划 §4.3 要求 Up 段写入）'
  fi

  view_cols="$(sqlv "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='sforum_core_v1' AND table_name='forum_badges'")"
  if printf '%s' "$view_cols" | grep -q 'rule' && printf '%s' "$view_cols" | grep -q 'requirement_summary_zh'; then
    pass 'M1.1-06 forum_badges 视图输出 rule 与双列 summary'
  else
    fail 'M1.1-06 forum_badges 视图' "列集=${view_cols:-<视图不存在>}"
  fi

  # M1.1-13：rule 只进视图，不进 HostQuery Fields（残留 R-1，见矩阵 §7）
  hostq_rule="$(grep -c 'stable\.rule' "$API_DIR/app/Support/HostAPI/v2_query_postgres.go" 2>/dev/null || true)"
  if printf '%s' "$view_cols" | tr ',' '\n' | grep -qx 'rule' && [[ "${hostq_rule:-0}" == 0 ]]; then
    pass 'M1.1-13 rule 只进视图（HostQuery public_badges.list 的 Fields 无 rule）'
  else
    fail 'M1.1-13 rule 传播边界' "视图含 rule=$(printf '%s' "$view_cols" | tr ',' '\n' | grep -cx rule)，HostQuery 出现 stable.rule=${hostq_rule:-?} 次（期望 0）"
  fi

  if [[ "$(sqlv "SELECT to_regclass('sforum_core_v1.forum_user_badge_facts') IS NOT NULL")" == t ]]; then
    fact_cols="$(sqlv "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='sforum_core_v1' AND table_name='forum_user_badge_facts'")"
    leaked=''
    for bad in delta dedupe_key source_extension_id; do
      printf '%s' "$fact_cols" | tr ',' '\n' | grep -qx "$bad" && leaked="$leaked $bad"
    done
    if [[ -z "$leaked" ]]; then
      pass 'M1.1-07 forum_user_badge_facts 白名单（无 delta/dedupe/source）'
    else
      fail 'M1.1-07 新视图泄露内部列' "多出：$leaked"
    fi
  else
    pend 'M1.1-07 新视图' 'sforum_core_v1.forum_user_badge_facts 不存在'
  fi

  # M1.1-10：逐人逐规则真值等价（第一方向必须 0 行；第二方向受「改严不收回」豁免，见矩阵 §2.3）
  qualifying="$(sqlv "SELECT count(*) FROM users u JOIN badges b ON b.grant_mode='automatic' AND b.enabled AND b.metric IS NOT NULL AND b.threshold IS NOT NULL AND b.threshold > 0 LEFT JOIN user_activity ua ON ua.user_id=u.id WHERE COALESCE(CASE b.metric WHEN 'topic_count' THEN ua.topic_count WHEN 'comment_count' THEN ua.comment_count WHEN 'like_received_count' THEN ua.like_received_count WHEN 'like_given_count' THEN ua.like_given_count WHEN 'bookmark_count' THEN ua.bookmark_count WHEN 'days_visited' THEN ua.days_visited::bigint END,0) >= b.threshold")"
  missing="$(sqlv "SELECT count(*) FROM users u JOIN badges b ON b.grant_mode='automatic' AND b.enabled AND b.metric IS NOT NULL AND b.threshold IS NOT NULL AND b.threshold > 0 LEFT JOIN user_activity ua ON ua.user_id=u.id WHERE COALESCE(CASE b.metric WHEN 'topic_count' THEN ua.topic_count WHEN 'comment_count' THEN ua.comment_count WHEN 'like_received_count' THEN ua.like_received_count WHEN 'like_given_count' THEN ua.like_given_count WHEN 'bookmark_count' THEN ua.bookmark_count WHEN 'days_visited' THEN ua.days_visited::bigint END,0) >= b.threshold AND NOT EXISTS (SELECT 1 FROM user_badges ub WHERE ub.user_id=u.id AND ub.badge_id=b.id)")"
  if [[ -z "$qualifying" || "$qualifying" == 0 ]]; then
    pend 'M1.1-10 逐人逐规则真值等价' '夹具里没有任何达标用户（断言会空转，需按矩阵 §2.3 造边界夹具）'
  elif [[ "$missing" == 0 ]]; then
    pass "M1.1-10 该有却没有 = 0（达标组合 ${qualifying}）"
  else
    fail 'M1.1-10 真值等价' "${missing}/${qualifying} 个达标组合缺授予"
  fi

  # 快照类断言需要矩阵 §2.1 的基线表；没有就 pend，不假装通过。
  if [[ "$(sqlv "SELECT to_regclass('badge_regress.ub_before') IS NOT NULL")" == t ]]; then
    diff_n="$(sqlv "SELECT count(*) FROM (SELECT user_id, slug, granted_at, granted_by, grant_reason FROM badge_regress.ub_before EXCEPT SELECT ub.user_id, b.slug, ub.granted_at, ub.granted_by, ub.grant_reason FROM user_badges ub JOIN badges b ON b.id=ub.badge_id) x")"
    if [[ "$diff_n" == 0 ]]; then
      pass 'M1.1-11 授予行与迁移前快照逐行一致'
    else
      fail 'M1.1-11 授予行漂移' "${diff_n} 行差异（先按矩阵 §2.4 定位到 slug）"
    fi
  else
    pend 'M1.1-11 授予行快照比对' '缺 badge_regress.ub_before（矩阵 §2.1 需在迁移前建基线）'
  fi

  if [[ "$(sqlv "SELECT to_regclass('badge_regress.counter_before') IS NOT NULL")" == t ]]; then
    counter_diff="$(sqlv "SELECT count(*) FROM (SELECT user_id, topic_count, comment_count, like_received_count, like_given_count, bookmark_count, days_visited FROM badge_regress.counter_before EXCEPT SELECT user_id, topic_count, comment_count, like_received_count, like_given_count, bookmark_count, days_visited FROM user_activity) d")"
    if [[ "$counter_diff" == 0 ]]; then
      pass 'M1.1-12 迁移未改动 user_activity'
    else
      fail 'M1.1-12 user_activity 被改动' "${counter_diff} 行差异（迁移只应加列）"
    fi
  else
    pend 'M1.1-12 user_activity 快照比对' '缺 badge_regress.counter_before'
  fi

  pend 'M1.1-04 非内置 automatic 徽章也被转换' '需迁移前造一枚自定义 automatic 徽章（一次性集群），本脚本不造数据'
  if grep -rq 'public_user_badge_facts' "$API_DIR/app/Support/HostAPI" 2>/dev/null; then
    if ( cd "$API_DIR" && go test -count=1 ./app/Support/HostAPI/ ./bootstrap/ ./app/Http/Controllers/Extensions/ ) >"$work/m1108.out" 2>&1; then
      pass 'M1.1-08 新 HostQuery 五面一致（HostAPI/bootstrap/Extensions 测试全绿）'
    elif grep -qE 'build failed|cannot use|undefined:' "$work/m1108.out"; then
      pend 'M1.1-08 HostQuery 五面一致' "包无法编译（可能正在编辑）：$(grep -m1 -E 'cannot use|undefined:' "$work/m1108.out" | cut -c1-120)"
    else
      fail 'M1.1-08 HostQuery 注册面不一致' "见 $(tail -3 "$work/m1108.out" | tr '\n' ' ')"
    fi
  else
    pend 'M1.1-08 HostQuery 五面一致' 'public_user_badge_facts 尚未在 HostAPI 注册'
  fi
  pend 'M1.1-09 迁移后重算零变更' '需管理员会话调 POST /admin/badges/recompute，读 {granted,refreshed} 均为 0'
fi

# ================================================================ M1.3 解析器
step 'M1.3 条件解析器'
if [[ "$slice_m13" != 1 ]]; then
  for pair in \
    '01|每个条件 key 的 allowed/denied' '02|窗口 vs 累计' \
    '03|软删口径：窗口计数不过滤 status' \
    '04|content.hidden 只统计主题 status=hidden（隐藏评论不计）' \
    '05|连续天数以今天/昨天收尾' '06|oauth_link 只认 status=active' \
    '07|object.engagement 三 action（comment 含自评论）' \
    '08|失效引用恒 false + 健康清单' '09|每 type 一次查询（memo）' \
    '10|窗口获赞 P95（10k 夹具，见 verify-badge-rules-perf.sh）' \
    '11|自赞排除：累计与窗口获赞都必须为 0' \
    '12|content.hidden 文案不得出现违规/处罚/封禁' \
    '13|content.pinned = 当前仍被置顶的数量' \
    '14|streak 日期由 Go 传入（API TZ 与 PG UTC 不同仍正确）' \
    '15|窗口日界不得依赖 PG 时区（::date 取 now()）' \
    '16|fact.count 必须在 Core() 里（13 条）' \
    '17|fact.count 不在 PublishableDefinitions(nil)' \
    '18|fact.count 的 factKey Options 内容' \
    '19|bookmark 三面口径一致（累计/窗口/object）'; do
    pend "M1.3-${pair%%|*}" "${pair#*|}：条件解析器未落地（缺 app/Models/Badges/conditions/registry.go）"
  done
elif [[ -z "$DB_URL" ]]; then
  for pair in \
    '01|每个条件 key 的 allowed/denied' '02|窗口 vs 累计' \
    '03|软删口径：窗口计数不过滤 status' \
    '04|content.hidden 只统计主题 status=hidden（隐藏评论不计）' \
    '05|连续天数以今天/昨天收尾' '06|oauth_link 只认 status=active' \
    '07|object.engagement 三 action（comment 含自评论）' \
    '08|失效引用恒 false + 健康清单' '09|每 type 一次查询（memo）' \
    '10|窗口获赞 P95（10k 夹具）' '11|自赞排除' \
    '12|content.hidden 文案口径' '13|content.pinned 当前语义' \
    '14|streak 日期由 Go 传入' '15|窗口日界不依赖 PG 时区' \
    '16|fact.count 在 Core() 里' '17|fact.count 不在发布面' \
    '18|factKey Options 内容' '19|bookmark 三面一致'; do
    pend "M1.3-${pair%%|*}" "${pair#*|}：PG 集成测试需要 DATABASE_URL"
  done
else
  # 条件解析器是 PG 集成测试：走一次性集群，不碰 DATABASE_URL 指向的库。
  ./scripts/test-api-isolated.sh -v -run '^(TestRegistryCatalogIsCompleteAndValidatable|TestActivityCountCumulativeAndWindow|TestActivityCountWindowedLikeReceived|TestActivityCountExcludesSelfLikes|TestActivityStreakEndsTodayOrYesterday|TestDaysVisitedWindowUsesProcessLocalDate|TestActivityCountWindowCountsSoftDeleted|TestPrepareServesFromMemo|TestObjectEngagement|TestObjectAuthoring|TestIdentityConditions|TestContentAndBadgeConditions|TestFactCountCumulativeWindowAndClamp)$' ./app/Models/Badges/conditions/... >"$work/m13.out" 2>&1
  if ! grep -qE '^(ok|FAIL|--- (PASS|FAIL): )' "$work/m13.out"; then
    tail -15 "$work/m13.out" >&2
    for id in 01 02 04 05 06 07 08 09 11 13; do
      fail "M1.3-$id" 'conditions 包集成测试无法执行（见上）'
    done
  else
    expect_test 'M1.3-01' '条件目录完整且可校验' "$work/m13.out" 'TestRegistryCatalogIsCompleteAndValidatable'
    expect_test 'M1.3-02' '窗口 vs 累计' "$work/m13.out" 'TestActivityCountCumulativeAndWindow'
    expect_test 'M1.3-04' 'content.hidden 只统计主题' "$work/m13.out" 'TestContentAndBadgeConditions'
    expect_test 'M1.3-05' '连续天数以今天/昨天收尾' "$work/m13.out" 'TestActivityStreakEndsTodayOrYesterday'
    expect_test 'M1.3-06' 'oauth_link 只认 active' "$work/m13.out" 'TestIdentityConditions'
    expect_test 'M1.3-07' 'object.engagement 三 action' "$work/m13.out" 'TestObjectEngagement'
    expect_test 'M1.3-08' '失效引用恒 false' "$work/m13.out" 'TestObjectAuthoring'
    expect_test 'M1.3-09' '每 type 一次查询（memo）' "$work/m13.out" 'TestPrepareServesFromMemo'
    expect_test 'M1.3-11' '自赞排除' "$work/m13.out" 'TestActivityCountExcludesSelfLikes'
    expect_test 'M1.3-13' 'content.pinned 当前语义' "$work/m13.out" 'TestContentAndBadgeConditions'
    expect_test 'M1.3-16' 'fact.count 在 Core()（13 条）' "$work/m13.out" 'TestRegistryCatalogIsCompleteAndValidatable'
    expect_test 'M1.3-17' 'fact.count 不在发布面（空候选）' "$work/m13.out" 'TestRegistryCatalogIsCompleteAndValidatable'
    expect_test 'M1.3-18' 'factKey Options 恰含传入的 factKey（且不污染单例）' "$work/m13.out" 'TestRegistryCatalogIsCompleteAndValidatable'
    expect_test 'M1.3-03' '软删内容仍计入窗口计数' "$work/m13.out" 'TestActivityCountWindowCountsSoftDeleted'
    expect_test 'M1.3-15' '窗口日界用进程本地日' "$work/m13.out" 'TestDaysVisitedWindowUsesProcessLocalDate'
  fi

  # M1.3-12 文案口径：静态断言，不依赖 DB。
  if grep -qE '违规|处罚|封禁' "$API_DIR/app/Models/Badges/conditions/registry.go" 2>/dev/null; then
    fail 'M1.3-12 content.hidden 文案' '条件说明出现「违规/处罚/封禁」，库里没有处罚记录，属误导'
  else
    pass 'M1.3-12 content.hidden 文案中性（无违规/处罚/封禁）'
  fi

  # M1.3-14 streak 日期来源：不得用 CURRENT_DATE；streak/account_age 必须收 $2::date。
  cd_hits="$(grep -rnE 'CURRENT_DATE|current_date' "$API_DIR/app/Models/Badges/conditions/" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -vE ':[0-9]+:[[:space:]]*//' | wc -l)"
  if [[ "${cd_hits:-0}" == 0 ]]; then
    pass 'M1.3-14 streak/account_age 的今天由 Go 传入（$2::date），无 CURRENT_DATE'
  else
    fail 'M1.3-14 streak 日期来源' "conditions 包仍有 ${cd_hits} 处 CURRENT_DATE（非注释）"
  fi

  # M1.3-19 bookmark 三面一致：窗口分支若按 visibility 过滤，就与累计/object 分叉。
  if grep -q "b.visibility = 'public'" "$API_DIR/app/Models/Badges/conditions/resolve_activity.go" 2>/dev/null; then
    fail 'M1.3-19 bookmark 三面一致' "窗口 SQL 过滤 b.visibility='public'，而 user_activity.bookmark_count 与 object.engagement 都不过滤（private 收藏被计入）→ 同一指标累计与窗口分叉"
  else
    pass 'M1.3-19 bookmark 三面一致（窗口不再按 visibility 过滤）'
  fi

  # M1.3-15 窗口日界：`(now() - interval)::date` 与 visited_on 的写入日界不同源。
  pgdate_hits="$(grep -rn 'now() - make_interval' "$API_DIR/app/Models/Badges/conditions/resolve_activity.go" | grep -c '::date' || true)"
  if [[ "${pgdate_hits:-0}" == 0 ]]; then
    pass 'M1.3-15 窗口日界不依赖 PG 时区'
  else
    fail 'M1.3-15 窗口日界' "days_visited 窗口下界用 (now()-interval)::date（PG 时区），与 user_visits 的 API 本地日界不同源：${pgdate_hits} 处"
  fi

  pend 'M1.3-10 窗口获赞 P95' '需 ./scripts/verify-badge-rules-perf.sh（默认 10k 即 M1-EXIT-PERF）'
fi

# ================================================================ M1.4 存储/HTTP
step 'M1.4 规则存储 / 求值 / 回扫 / HTTP / 契约'
M14_ITEMS=(
  '01|快路径：SQL 选中集合 ⊆ Go fastPathRule'
  '02|纯 rule 徽章（metric NULL）能被授予'
  '03|重复评估幂等（并发只授予一次）'
  '04|三来源判别式（回扫不得写 recompute）'
  '05|rule 三态：缺省=不改（只改 name 后 rule 字节级不变）'
  '06|rule 三态：null=清空（单独 null 必须 422）'
  '07|rule 三态：对象=替换'
  '08|422 错误码映射（五种非法规则）'
  '09a|环引用：2 枚互指 → 422'
  '09b|环引用：3 枚互指 → 422'
  '09c|环引用：自引用 → 422（DFS 最常漏）'
  '09d|环检测只在保存期（评估不死循环）'
  '09e|反向断言：菱形依赖必须保存成功'
  '10|影响估算 {estimated, method}'
  '11|健康清单'
  '12a|回扫游标可续' '12b|回扫失败可观测' '12c|ByArgs 去重键'
  '14|requirementText 回落矩阵'
  '15|OpenAPI 契约一致'
  '16|rule_health 认「已声明但零绑定」的 provider'
  '17|rule_health 不把「零授予」当失效'
  '18|recompute 的 queued = 徽章数'
  '19|快路径必须校验 cmp.op=gte（lte 规则 → granted=0, queued=1）'
  '20|rule_health 区分「引用失效」与「条件失效」'
  '21|HTTP recompute 的 queued 与 store 层一致'
  '22|契约级：OpenAPI required 列表 vs 真实响应键集合'
)
pend_m14_all() {
  local item
  for item in "${M14_ITEMS[@]}"; do
    pend "M1.4-${item%%|*}" "${item#*|}：$1"
  done
}
if [[ "$slice_m14" != 1 ]]; then
  pend_m14_all '规则存储未落地（缺 app/Models/Badges/rule_store.go）'
elif [[ -z "$DB_URL" ]]; then
  pend_m14_all 'PG 集成测试需要 DATABASE_URL'
else
  ./scripts/test-api-isolated.sh -v -run '^(TestFastPathSQLSubsetOfGoFastPath|TestGrantFromRulesCoversEveryMetricBranch|TestConcurrentEvaluationGrantsExactlyOnce|TestUpdateBadgeRuleTriState|TestBadgeRuleCycleDetection|TestEstimateRuleImpactIsUpperBound|TestRuleHealthReportsOnlyBrokenReferences|TestRecordFactIdempotencyAndCounter|TestPublicBadgeRequirementText|TestRecomputeGrantsMissingBadgesFromRules|TestEvaluatePureRuleBadgeWithoutLegacyMetric|TestGrantSourcesAreDistinguishable|TestUpdateBadgeRuleNullCombinations)$' ./app/Models/Badges/... >"$work/m14.out" 2>&1
  ./scripts/test-api-isolated.sh -v -run '^(TestPublicCatalogWireKeysAreWhitelisted|TestPublicCatalogDTOCarriesNoThresholdOrMetricField|TestRuleRoutesAreRegisteredBeforeParamRoutes|TestMapBadgeErrorRuleCodes|TestRecomputeResponseCarriesAllCounters)$' ./app/Http/Controllers/Badges/... >"$work/m14c.out" 2>&1

  expect_test 'M1.4-01' '快路径 SQL 选中集合 ⊆ fastPathRule' "$work/m14.out" 'TestFastPathSQLSubsetOfGoFastPath'
  # P-04：纯 rule 徽章（metric IS NULL）没有任何测试夹具覆盖授予路径。
  expect_test 'M1.4-02' '纯 rule 徽章（metric/threshold 均 NULL）能授予' "$work/m14.out" 'TestEvaluatePureRuleBadgeWithoutLegacyMetric'
  expect_test 'M1.4-03' '并发评估只授予一次' "$work/m14.out" 'TestConcurrentEvaluationGrantsExactlyOnce'
  expect_test 'M1.4-04' '三来源判别式（自动/重算/手动）' "$work/m14.out" 'TestGrantSourcesAreDistinguishable'
  expect_test 'M1.4-05' 'rule 三态：缺省不改' "$work/m14.out" 'TestUpdateBadgeRuleTriState'
  expect_test 'M1.4-06' 'rule 三态：null 清空（三组合）' "$work/m14.out" 'TestUpdateBadgeRuleNullCombinations'
  expect_test 'M1.4-07' 'rule 三态：对象替换' "$work/m14.out" 'TestUpdateBadgeRuleTriState'
  expect_test 'M1.4-08' '五个错误码 → 422 + 精确码' "$work/m14c.out" 'TestMapBadgeErrorRuleCodes'
  expect_test 'M1.4-09a' '环引用 2 枚互指' "$work/m14.out" 'TestBadgeRuleCycleDetection'
  expect_test 'M1.4-09b' '环引用 3 枚互指' "$work/m14.out" 'TestBadgeRuleCycleDetection'
  expect_test 'M1.4-09c' '环引用 自引用' "$work/m14.out" 'TestBadgeRuleCycleDetection'
  expect_test 'M1.4-09d' '环检测只在保存期' "$work/m14.out" 'TestBadgeRuleCycleDetection'
  expect_test 'M1.4-09e' '菱形依赖必须保存成功' "$work/m14.out" 'TestBadgeRuleCycleDetection'
  expect_test 'M1.4-10' '影响估算是上界' "$work/m14.out" 'TestEstimateRuleImpactIsUpperBound'
  expect_test 'M1.4-11' '健康清单' "$work/m14.out" 'TestRuleHealthReportsOnlyBrokenReferences'
  # M1.4-12：结构断言（HTTP 只调 EnqueueRuleBackfill，评估在 worker 里），不测时间。
  ./scripts/test-api-isolated.sh -v -run '^(TestBackfillRulesWorkerChainsPages|TestBackfillRulesWorkerSurfacesFailures|TestBackfillRulesArgsDedupeKey)$' ./app/Jobs/Badges/... >"$work/m1412.out" 2>&1
  expect_test 'M1.4-12a' '回扫游标可续' "$work/m1412.out" 'TestBackfillRulesWorkerChainsPages'
  expect_test 'M1.4-12b' '失败可观测' "$work/m1412.out" 'TestBackfillRulesWorkerSurfacesFailures'
  expect_test 'M1.4-12c' 'ByArgs 去重键' "$work/m1412.out" 'TestBackfillRulesArgsDedupeKey'
  expect_test 'M1.4-14' 'requirementText 回落（领域层）' "$work/m14.out" 'TestPublicBadgeRequirementText'
  # wire 层：OpenAPI 的 PublicBadgeCatalogEntry 把 requirementText 列为 required，
  # 领域层有值不等于响应里有这个键（这是「过滤后重建结构」的典型漏法）。
  if command -v jq >/dev/null && [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/badges" 2>/dev/null)" == 200 ]]; then
    cat_entries="$(curl -s --max-time 5 "$API_URL/badges" | jq '[.data.groups[].badges[]] | length' 2>/dev/null)"
    cat_missing="$(curl -s --max-time 5 "$API_URL/badges" | jq '[.data.groups[].badges[] | select((.requirementText | type) != "object")] | length' 2>/dev/null)"
    if [[ -z "$cat_entries" || "$cat_entries" == 0 ]]; then
      pend 'M1.4-14 requirementText 下发' '公开目录为空，无法判定'
    elif [[ "${cat_missing:-0}" != 0 ]]; then
      fail 'M1.4-14 requirementText 下发' "${cat_missing}/${cat_entries} 条缺 requirementText（OpenAPI 标为 required，领域层有值但 DTO 没输出）"
    else
      pass "M1.4-14 requirementText 键已下发到公开目录（${cat_entries} 条）"
      # 键在 ≠ 值在：DTO 字段没有 omitempty，漏赋值会输出 {"zh":"","en":""}，
      # 键存在性检查（M1.4-22）抓不到。这里按 DB 里的规则形状验值。
      single_slugs="$(sqlv "SELECT COALESCE(string_agg(slug, ','), '') FROM badges WHERE enabled AND rule IS NOT NULL AND ((rule->'root'->>'type') IS NOT NULL OR ((rule->'root'->>'op') = 'all' AND jsonb_array_length(COALESCE(rule->'root'->'children','[]'::jsonb)) = 1))" 2>/dev/null)"
      empty_vals="$(curl -s --max-time 5 "$API_URL/badges" | jq -r --arg slugs "$single_slugs" '
        ($slugs | split(",") | map(select(length > 0))) as $want
        | [ .data.groups[].badges[]
            | select(.slug as $s | $want | index($s))
            | select(((.requirementText.zh // "") | length) == 0 or ((.requirementText.en // "") | length) == 0)
          ] | length' 2>/dev/null)"
      structural="$(curl -s --max-time 5 "$API_URL/badges" | jq -r '[ .data.groups[].badges[] | select(((.requirementText.zh // "") + (.requirementText.en // "")) | test("\\{|cmp|children|params")) ] | length' 2>/dev/null)"
      if [[ -z "$single_slugs" ]]; then
        pend 'M1.4-14 requirementText 值' '库里没有单条件规则徽章'
      elif [[ "${empty_vals:-1}" == 0 ]]; then
        pass "M1.4-14 单条件徽章的 requirementText 两语言非空（$(printf '%s' "$single_slugs" | tr ',' '
' | grep -c .) 条）"
      else
        fail 'M1.4-14 requirementText 值' "${empty_vals} 条单条件徽章的双语文本有空串（键在、值没填）"
      fi
      if [[ "${structural:-1}" == 0 ]]; then
        pass 'M1.4-14 requirementText 未退化成结构文本'
      else
        fail 'M1.4-14 requirementText 结构泄漏' "${structural} 条文案里出现 { / cmp / children / params"
      fi
    fi
  else
    pend 'M1.4-14 requirementText 下发' 'API 不可达或缺 jq'
  fi
  # 资料页墙同源：只验目录会漏掉「目录有、资料页没有」。
  #
  # 判据必须是**值级**：DTO 的 requirementText 没有 omitempty，漏赋值照样输出
  # {"zh":"","en":""}。旧判据 `(.requirementText | type) != "object"` 里 object 永远是
  # object，条件恒假 → 恒 pass（假绿）。现在按**规则形状**取样、双向验值：
  #   单条件（单 activity.count 叶子 + gte + window.all，即 fastPathRuleForText 的判据）
  #     → 两种语言都必须非空；
  #   多条件（root >= 2 children，且运营没手填文本）
  #     → 两种语言都必须为空（服务端刻意不把 AST 拼成句子）。
  # 样本从库里按形状挑，不写死 slug 名单：规则改了断言不跟着烂。
  # 某形状样本为 0 时记 pend 而不是 pass——没有样本的「满足」是空真。
  if command -v jq >/dev/null; then
    prof_user="$(sqlv "SELECT username FROM users ORDER BY id LIMIT 1" 2>/dev/null)"
    if [[ -z "$prof_user" ]]; then
      pend 'M1.4-14 requirementText 资料页' '库里没有用户，无法取样'
    elif [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/profiles/${prof_user}/badges" 2>/dev/null)" != 200 ]]; then
      pend 'M1.4-14 requirementText 资料页' "API 不可达（$API_URL/profiles/${prof_user}/badges）"
    else
      prof_json="$work/prof_badges.json"
      curl -s --max-time 5 "$API_URL/profiles/${prof_user}/badges" >"$prof_json" 2>/dev/null
      prof_total="$(jq '[.data.groups[].badges[]] | length' "$prof_json" 2>/dev/null)"
      # 单条件形状 = 与 app/Models/Badges/requirement_text.go 的 fastPathRuleForText 同一判据。
      prof_single_slugs="$(sqlv "SELECT COALESCE(string_agg(slug, ','), '') FROM badges WHERE enabled AND rule IS NOT NULL AND (rule->'root'->>'type') = 'activity.count' AND (rule->'root'->'cmp'->>'op') = 'gte' AND COALESCE((rule->'root'->'cmp'->>'value')::numeric, 0) > 0 AND (rule->'root'->'params'->'window'->>'kind') = 'all' AND NOT (COALESCE(rule->'root'->'params'->'window', '{}'::jsonb) ? 'days') AND (rule->'root'->'params'->>'metric') IN ('topic_count','comment_count','like_received_count','like_given_count','bookmark_count','days_visited')" 2>/dev/null)"
      # 多条件形状 = root 有 >= 2 个 children；排除运营手填文本（否则空值断言变成假 FAIL）。
      prof_multi_slugs="$(sqlv "SELECT COALESCE(string_agg(slug, ','), '') FROM badges WHERE enabled AND rule IS NOT NULL AND jsonb_array_length(COALESCE(rule->'root'->'children', '[]'::jsonb)) >= 2 AND btrim(COALESCE(requirement_summary_zh, '')) = '' AND btrim(COALESCE(requirement_summary_en, '')) = ''" 2>/dev/null)"
      prof_single_n=0
      [[ -n "$prof_single_slugs" ]] && prof_single_n="$(printf '%s' "$prof_single_slugs" | tr ',' '\n' | grep -c .)"
      prof_multi_n=0
      [[ -n "$prof_multi_slugs" ]] && prof_multi_n="$(printf '%s' "$prof_multi_slugs" | tr ',' '\n' | grep -c .)"
      if [[ -z "$prof_total" || "$prof_total" == 0 ]]; then
        pend 'M1.4-14 requirementText 资料页' '该用户徽章墙为空'
      elif [[ "$prof_single_n" == 0 && "$prof_multi_n" == 0 ]]; then
        pend 'M1.4-14 requirementText 资料页' '库里没有可判定形状的规则徽章（单/多条件样本都为 0）'
      else
        # 值级判据：按形状取交集，逐条看两种语言的**长度**（字符串内容，不看键类型）。
        prof_single_bad="$(jq -r --arg slugs "$prof_single_slugs" '
          ($slugs | split(",") | map(select(length > 0))) as $want
          | [ .data.groups[].badges[]
              | select(.slug as $s | $want | index($s))
              | select(((.requirementText.zh // "") | length) == 0 or ((.requirementText.en // "") | length) == 0)
              | .slug ] | join(",")' "$prof_json" 2>/dev/null)"
        prof_single_seen="$(jq -r --arg slugs "$prof_single_slugs" '
          ($slugs | split(",") | map(select(length > 0))) as $want
          | [ .data.groups[].badges[] | select(.slug as $s | $want | index($s)) ] | length' "$prof_json" 2>/dev/null)"
        prof_multi_bad="$(jq -r --arg slugs "$prof_multi_slugs" '
          ($slugs | split(",") | map(select(length > 0))) as $want
          | [ .data.groups[].badges[]
              | select(.slug as $s | $want | index($s))
              | select((((.requirementText.zh // "") | length) > 0) or (((.requirementText.en // "") | length) > 0))
              | .slug ] | join(",")' "$prof_json" 2>/dev/null)"
        prof_multi_seen="$(jq -r --arg slugs "$prof_multi_slugs" '
          ($slugs | split(",") | map(select(length > 0))) as $want
          | [ .data.groups[].badges[] | select(.slug as $s | $want | index($s)) ] | length' "$prof_json" 2>/dev/null)"
        # 文案里出现规则内部词汇 = AST 泄漏。词边界匹配：`topics published` 不能被 `op` 误判。
        prof_struct="$(jq -r '[ .data.groups[].badges[].requirementText | (.zh, .en)
          | select(test("\\b(cmp|params|children|op|rule|type)\\b")) ] | length' "$prof_json" 2>/dev/null)"
        [[ "$prof_single_seen" =~ ^[0-9]+$ ]] || prof_single_seen=0
        [[ "$prof_multi_seen" =~ ^[0-9]+$ ]] || prof_multi_seen=0
        if [[ "$prof_single_n" == 0 ]]; then
          pend 'M1.4-14 资料页单条件值' '库里没有单条件规则徽章（空真不算通过）'
        elif [[ "$prof_single_seen" -lt "$prof_single_n" ]]; then
          fail 'M1.4-14 资料页单条件值' "库里 ${prof_single_n} 枚单条件徽章，资料页只回 ${prof_single_seen} 枚（两墙不同源）"
        elif [[ -z "$prof_single_bad" ]]; then
          pass "M1.4-14 资料页单条件徽章两种语言都有值（${prof_user}，${prof_single_seen} 枚）"
        else
          fail 'M1.4-14 资料页 requirementText 值' "单条件徽章有整语言空串：${prof_single_bad}（键在、值没填）"
        fi
        if [[ "$prof_multi_n" == 0 ]]; then
          pend 'M1.4-14 资料页多条件值为空' '库里没有多条件规则徽章；该分支无法取证（空真不算通过）'
        elif [[ "$prof_multi_seen" -lt "$prof_multi_n" ]]; then
          fail 'M1.4-14 资料页多条件值' "库里 ${prof_multi_n} 枚多条件徽章，资料页只回 ${prof_multi_seen} 枚"
        elif [[ -z "$prof_multi_bad" ]]; then
          pass "M1.4-14 资料页多条件徽章两种语言都空（${prof_multi_seen} 枚，未把 AST 拼成句子）"
        else
          fail 'M1.4-14 资料页多条件值为空' "多条件徽章不应有服务端文案，却有值：${prof_multi_bad}"
        fi
        if [[ "${prof_struct:-1}" == 0 ]]; then
          pass 'M1.4-14 资料页文案无规则内部词汇（词边界 cmp/params/children/op/rule/type）'
        else
          fail 'M1.4-14 资料页 AST 泄漏' "${prof_struct:-?} 段文案出现规则内部词汇"
        fi
      fi
    fi
  fi
  # 静态护栏：metricLabels 缺一个指标，单条件徽章就会「没有文案」；上面按形状取样时会
  # 用同一张指标白名单把它排除，覆盖就悄悄丢了。这里把 registry 与标签表钉在一起。
  metric_consts="$(grep -oE 'Metric[A-Za-z]+[[:space:]]+=' "$API_DIR/app/Models/Badges/conditions/registry.go" 2>/dev/null | grep -oE 'Metric[A-Za-z]+' | sort -u)"
  label_consts="$(grep -oE 'conditions\.Metric[A-Za-z]+' "$API_DIR/app/Models/Badges/requirement_text.go" 2>/dev/null | sed 's/conditions\.//' | sort -u)"
  if [[ -z "$metric_consts" || -z "$label_consts" ]]; then
    pend 'M1.4-14 指标文案标签齐全' '缺 registry.go 或 requirement_text.go'
  else
    missing_labels="$(comm -23 <(printf '%s\n' "$metric_consts") <(printf '%s\n' "$label_consts") | grep -v '^$' | tr '\n' ' ')"
    if [[ -z "${missing_labels// /}" ]]; then
      pass "M1.4-14 指标文案标签齐全（$(printf '%s\n' "$label_consts" | grep -c .)/$(printf '%s\n' "$metric_consts" | grep -c .) 个指标）"
    else
      fail 'M1.4-14 指标文案标签' "requirement_text.go 的 metricLabels 缺：${missing_labels}"
    fi
  fi
  if ( cd "$ROOT_DIR" && ruby scripts/validate-openapi-refs.rb ) >"$work/m1415.out" 2>&1; then
    pass 'M1.4-15 OpenAPI $ref 校验通过'
  else
    fail 'M1.4-15 OpenAPI' "validate-openapi-refs.rb 失败：$(tail -2 "$work/m1415.out" | tr '\n' ' ')"
  fi
  expect_test 'M1.4-16' 'provider 零绑定仍有效' "$work/m14.out" 'TestRuleHealthReportsOnlyBrokenReferences'
  expect_test 'M1.4-17' '零授予不算失效' "$work/m14.out" 'TestRuleHealthReportsOnlyBrokenReferences'
  expect_test 'M1.4-18' 'queued = 徽章数' "$work/m14.out" 'TestGrantFromRulesCoversEveryMetricBranch'
  expect_test 'M1.4-19' 'lte 不进快路径' "$work/m14.out" 'TestFastPathSQLSubsetOfGoFastPath'
  expect_test 'M1.4-20' '两种失效都列' "$work/m14.out" 'TestRuleHealthReportsOnlyBrokenReferences'
  # HTTP 半边：store 层通过不等于 HTTP 层对——控制器可能把 queued 丢掉。
  # 映射是内联在 fiber handler 里的（没有独立可调用的 DTO 函数），所以这里做
  # 「响应键集合 vs OpenAPI required」的契约级比对；完整 HTTP 往返仍需管理员会话。
  expect_test 'M1.4-21' 'recompute 响应三个计数器都在（映射层单测）' "$work/m14c.out" 'TestRecomputeResponseCarriesAllCounters'
  # 契约级：DTO 的 json tag 集合 vs OpenAPI required。映射已抽成 recomputeResponse，
  # 所以这里读结构体而不是 handler 里的字面量（字面量只能 grep 守，抓不到拼错/漏加）。
  if command -v ruby >/dev/null; then
    verdict="$( cd "$ROOT_DIR" && ruby -ryaml -e '
      src = File.read("apps/api/app/Http/Controllers/Badges/dto.go")
      body = src[/type recomputeResponse struct \{.*?\n\}/m] || ""
      keys = body.scan(/json:"([a-z_]+)"/).flatten.uniq
      schema = YAML.load_file("contracts/openapi/schemas/badges.yaml", aliases: true)
      req = schema.dig("BadgeRecomputeResult", "required") || []
      if body.empty? || req.empty?
        puts "UNPARSEABLE"
      else
        missing = req - keys
        puts(missing.empty? ? "OK" : "MISSING:" + missing.join(","))
      end
    ' 2>&1 )"
    case "$verdict" in
      OK) pass 'M1.4-21 recompute DTO 的 json tag 覆盖 OpenAPI required' ;;
      MISSING:*) fail 'M1.4-21 recompute DTO 契约' "DTO 缺键：${verdict#MISSING:}" ;;
      *) pend 'M1.4-21 recompute DTO 契约' "契约解析失败：$(printf '%s' "$verdict" | tail -1 | cut -c1-100)" ;;
    esac
  else
    pend 'M1.4-21 recompute DTO 契约' '缺 ruby'
  fi
  pend 'M1.4-21 完整 HTTP 往返' '未执行（无管理员会话）；store 层语义由 M1.4-18/19 覆盖，映射层由上一条覆盖'
  # 路由顺序（X-02b 的机器证据）
  expect_test 'X-02b' '规则路由先于 /:badgeID 注册' "$work/m14c.out" 'TestRuleRoutesAreRegisteredBeforeParamRoutes'
fi

# M1.4-13 公开面不泄露 AST：只要有 API 就能查，不依赖 M1.4 文件。
if ! command -v curl >/dev/null || ! command -v jq >/dev/null; then
  pend 'M1.4-13 公开面不泄露 AST' '缺 curl 或 jq'
elif [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/badges" 2>/dev/null)" != 200 ]]; then
  pend 'M1.4-13 公开面不泄露 AST' "API 不可达（$API_URL/badges）"
else
  keys="$(curl -s --max-time 5 "$API_URL/badges" | jq -r '.. | objects | keys[]' 2>/dev/null | sort -u)"
  leaked=''
  for bad in rule cmp params children op; do
    printf '%s\n' "$keys" | grep -qx "$bad" && leaked="$leaked $bad"
  done
  if [[ -z "$leaked" ]]; then
    pass 'M1.4-13 公开面键集合不含 AST 字段'
  else
    fail 'M1.4-13 公开面泄露 AST' "出现字段：$leaked"
  fi
fi

# M1.4-22 契约级：OpenAPI required 列表 vs 真实响应键集合。
# 只验「字段存在」不够——required 写了但 DTO 漏了，存在性检查照样绿。
if ! command -v ruby >/dev/null || ! command -v jq >/dev/null; then
  pend 'M1.4-22 契约级 required 比对' '缺 ruby 或 jq'
elif [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API_URL/badges" 2>/dev/null)" != 200 ]]; then
  pend 'M1.4-22 契约级 required 比对' "API 不可达（$API_URL/badges）"
else
  verdict="$(curl -s --max-time 5 "$API_URL/badges" | ( cd "$ROOT_DIR" && ruby -ryaml -rjson -e '
    schema = begin
      YAML.load_file("contracts/openapi/schemas/badges.yaml", aliases: true)
    rescue ArgumentError
      YAML.load_file("contracts/openapi/schemas/badges.yaml")
    end
    req = schema.dig("PublicBadgeCatalogEntry", "required") || []
    resp = JSON.parse(STDIN.read)
    entries = (resp.dig("data", "groups") || []).flat_map { |g| g["badges"] || [] }
    if entries.empty?
      puts "EMPTY"
    else
      missing = entries.flat_map { |e| req.reject { |k| e.key?(k) }.map { |k| "#{e["slug"]}:#{k}" } }
      puts(missing.empty? ? "OK" : "MISSING:" + missing.join(","))
    end
  ' ) 2>&1)"
  case "$verdict" in
    OK) pass 'M1.4-22 公开目录响应满足 OpenAPI required 列表' ;;
    EMPTY) pend 'M1.4-22 契约级 required 比对' '公开目录为空，无法判定' ;;
    MISSING:*) fail 'M1.4-22 契约级 required 比对' "缺键：${verdict#MISSING:}" ;;
    *) pend 'M1.4-22 契约级 required 比对' "契约解析失败：$(printf '%s' "$verdict" | tail -1 | cut -c1-120)" ;;
  esac
fi

# ================================================================ X 横切面
step 'X 横切面四条'

# X-01 i18n 双向断言（M1 阶段约定不加 admin.badges.* 键）
if [[ ! -d "$WEB_DIR/node_modules" ]] || ! command -v bun >/dev/null; then
  pend 'X-01 i18n 键双向一致' '缺 apps/web/node_modules 或 bun'
elif [[ -f "$WEB_DIR/tests/admin/adminBadgesI18n.test.ts" ]]; then
  if ( cd "$WEB_DIR" && bun test tests/admin/adminBadgesI18n.test.ts ) >"$work/x01.out" 2>&1; then
    pass 'X-01 i18n 键双向一致（missing/extra 均为空）'
  else
    fail 'X-01 i18n 键不一致' "见 bun test 输出（$(tail -3 "$work/x01.out" | tr '\n' ' ')）"
  fi
else
  pend 'X-01 i18n 键双向一致' 'adminBadgesI18n.test.ts 不存在'
fi

# X-02 路由计数 tripwire：期望值随 M1.4 路由落地而变
if [[ ! -f "$ROUTES_TEST" ]]; then
  pend 'X-02 路由计数 tripwire' 'registry_test.go 不存在'
else
  asserted="$(grep -oE 'len\(catalog\) != [0-9]+' "$ROUTES_TEST" | grep -oE '[0-9]+' | head -1)"
  expected=418
  [[ "$slice_m14" == 1 ]] && expected=421
  if [[ "$asserted" == "$expected" ]]; then
    if ( cd "$API_DIR" && go test -count=1 ./app/Support/Routes/ ) >"$work/x02.out" 2>&1; then
      pass "X-02 路由计数 = ${expected}"
    elif grep -qE 'build failed|cannot use|undefined:' "$work/x02.out"; then
      pend 'X-02 路由计数测试' "Routes 包无法编译（可能正在编辑）"
    else
      fail 'X-02 路由计数测试' 'go test ./app/Support/Routes/ 失败（见输出）'
    fi
  else
    fail 'X-02 路由计数 tripwire' "断言值 ${asserted:-?}，期望 ${expected}（418 + 规则引擎路由）"
  fi
fi

# X-02b 静态段先于 /:badgeID（源码级：conditions / rule-health 必须在 :badgeID 之前注册）
CTRL="$API_DIR/app/Http/Controllers/Badges/controller.go"
if [[ ! -f "$CTRL" ]]; then
  pend 'X-02b 静态段注册顺序' 'controller.go 不存在'
elif ! grep -q 'conditions\|rule-health' "$CTRL"; then
  pend 'X-02b 静态段注册顺序' '规则引擎路由未注册'
else
  first_static="$(grep -nE '"/conditions"|"/rule-health"' "$CTRL" | head -1 | cut -d: -f1)"
  first_param="$(grep -nE 'Get\("/:[a-zA-Z]+"|Patch\("/:[a-zA-Z]+"|Delete\("/:[a-zA-Z]+"' "$CTRL" | head -1 | cut -d: -f1)"
  if [[ -n "$first_static" && -n "$first_param" && "$first_static" -lt "$first_param" ]]; then
    pass 'X-02b 静态段先于 /:badgeID 注册'
  else
    fail 'X-02b 静态段注册顺序' "静态段行 ${first_static:-?} 未先于参数段行 ${first_param:-?}"
  fi
fi

# X-03 新错误码 zh/en（先确认常量已加，再跑本地化测试）
CODES_FILE="$API_DIR/app/Models/Badges/errors.go"
new_codes='forum.badge_rule_too_complex forum.badge_rule_unknown_condition forum.badge_rule_cycle forum.badge_rule_reference_invalid forum.badge_fact_write_denied'
missing_codes=''
for code in $new_codes; do
  grep -rq "\"$code\"" "$API_DIR/app" 2>/dev/null || missing_codes="$missing_codes $code"
done
if [[ -n "$missing_codes" ]]; then
  pend 'X-03 新错误码 zh/en 文案' "错误码常量未落地：$missing_codes"
elif ( cd "$API_DIR" && go test -count=1 -run 'TestAPIErrorCodes' ./app/Support/Localization/ ) >"$work/x03.out" 2>&1; then
  pass 'X-03 新错误码两份 locale 均有文案'
else
  fail 'X-03 错误码本地化' "见 go test 输出（$(tail -3 "$work/x03.out" | tr '\n' ' ')）"
fi

# X-04 迁移可逆：脚本分支存在性 + 目标版本
if [[ "$slice_m11" != 1 ]]; then
  pend 'X-04 迁移 up/down' '迁移未落地'
elif grep -q '202611120002' "$REVERSIBLE_SCRIPT"; then
  pass 'X-04 可逆脚本已含 202611120002 分支（执行：SFORUM_MIGRATION_TARGET_VERSION=202611120002 ./scripts/verify-migration-reversible.sh）'
else
  # 交付缺口（task-1）而非行为错：脚本分支没写时 CMD-REV 会落进 else 断言旧对象消失，
  # 结果是 exit 6 假红。这里记 pend + 提示，避免开发期把脚本自己变成红门禁。
  printf '  注意 %s\n' 'verify-migration-reversible.sh 缺 202611120002 分支：现在跑 CMD-REV 会假红 exit 6'
  pend 'X-04 迁移 up/down' '可逆脚本缺 202611120002 分支（task-1 交付缺口）'
fi

# ================================================================ P 计划未覆盖
step 'P 计划未覆盖但必须验'

# P-01 窗口获赞的索引前提：只提示，不做判定（需 EXPLAIN 证据）
if [[ "$slice_m13" == 1 ]]; then
  pend 'P-01 窗口获赞索引前提' '需在 10k 夹具上取 EXPLAIN (ANALYZE, BUFFERS)；结论写入 M1.3-10 附件'
else
  pend 'P-01 窗口获赞索引前提' '解析器未落地'
fi

# M1-EXIT-PERF：M1 收口门，只在结项前跑一次（不进日常循环）
if [[ -f "$ROOT_DIR/scripts/verify-badge-rules-perf.sh" ]]; then
  pend 'M1-EXIT-PERF 10k 规模 EXPLAIN' '需结项前跑一次 ./scripts/verify-badge-rules-perf.sh（默认 10k），把 PERF-01/02 的 EXPLAIN 原文贴进矩阵 §9.4'
else
  pend 'M1-EXIT-PERF 10k 规模 EXPLAIN' '缺 scripts/verify-badge-rules-perf.sh'
fi

# P-12 畸形树必须 fail-closed（Lead 2026-11-12 裁决选项 ①，task-5）
#
# 不依赖实现方自己的断言：这里编译一个一次性探针，直接对 RuleEngine 调 Eval，
# 读 matched / incomplete / errors 三个字段。探针目录用点开头（go 工具链忽略），
# 跑完立刻删除。环境跑不起来时记 pend，不判 FAIL。
if [[ "$slice_m12" != 1 ]]; then
  pend 'P-12 畸形树 fail-closed（空组 / not 元数 / 错形组）' 'RuleEngine 未落地'
elif [[ "${SFORUM_ACCEPT_NO_PROBE:-0}" == 1 ]]; then
  pend 'P-12 畸形树 fail-closed' 'SFORUM_ACCEPT_NO_PROBE=1，跳过探针'
else
  probe_dir="$API_DIR/.reviewer-p12-probe-$$"
  mkdir -p "$probe_dir"
  cat >"$probe_dir/main.go" <<'PROBE'
package main

import (
	"context"
	"fmt"

	re "github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

type nopResolver struct{}

func (nopResolver) Resolve(context.Context, re.Leaf) (re.Value, error) {
	return re.Value{Kind: re.ValueBoolean, Bool: true}, nil
}

func report(name string, node re.Node) {
	res, err := re.Eval(context.Background(), node, nopResolver{})
	if err != nil {
		fmt.Printf("%s top_error=%v\n", name, err)
		return
	}
	codes := ""
	for _, e := range res.Errors {
		if c := re.CodeOf(e); c != "" {
			codes += c + ","
		}
	}
	fmt.Printf("%s matched=%v incomplete=%v errors=%d codes=%s\n", name, res.Matched, res.Incomplete, len(res.Errors), codes)
}

func main() {
	leaf := re.Node{Type: "identity.email_verified"}
	report("empty_all", re.Node{Op: re.OpAll})
	report("empty_any", re.Node{Op: re.OpAny})
	report("not_zero", re.Node{Op: re.OpNot})
	report("not_two", re.Node{Op: re.OpNot, Children: []re.Node{leaf, leaf}})
	report("group_with_type", re.Node{Op: re.OpAll, Type: "identity.email_verified", Children: []re.Node{leaf}})
	report("leaf_with_children", re.Node{Type: "identity.email_verified", Children: []re.Node{leaf}})
	report("normal_all_true", re.Node{Op: re.OpAll, Children: []re.Node{leaf}})
}
PROBE
  if ! ( cd "$API_DIR" && go run "./$(basename "$probe_dir")" ) >"$work/p12.out" 2>&1; then
    rm -rf "$probe_dir"
    pend 'P-12 畸形树 fail-closed' "探针编译/执行失败（$(tail -2 "$work/p12.out" | tr '\n' ' ')）"
  else
    rm -rf "$probe_dir"
    bad=''
    for name in empty_all empty_any not_zero not_two group_with_type leaf_with_children; do
      line="$(grep "^${name} " "$work/p12.out" || true)"
      if ! printf '%s' "$line" | grep -q 'matched=false incomplete=true' \
         || ! printf '%s' "$line" | grep -q 'codes=.*shape_invalid'; then
        bad="$bad ${name}"
      fi
    done
    if [[ -n "$bad" ]]; then
      fail 'P-12 畸形树 fail-closed' "以下畸形树未被判为 unknown+shape_invalid：$bad（$(grep -c . "$work/p12.out") 行探针输出）"
    elif ! grep -q '^normal_all_true matched=true incomplete=false errors=0' "$work/p12.out"; then
      fail 'P-12 兜底误伤正常路径' 'normal_all_true 不再成立（兜底把正常 all 也变成 unknown）'
    else
      # 行为已正确，再要求 task-5 的三条回归测试在位，防止下次被改回去。
      missing_t=''
      for t in TestEvalEmptyGroupStaysUnknown TestEvalNotArityStaysUnknown TestEvalMalformedGroupStaysUnknown; do
        grep -rq "func ${t}(" "$API_DIR/app/Support/RuleEngine/" 2>/dev/null || missing_t="$missing_t $t"
      done
      if [[ -n "$missing_t" ]]; then
        pend 'P-12 畸形树 fail-closed' "行为已正确但缺回归测试：$missing_t（task-5）"
      else
        pass 'P-12 畸形树 fail-closed（空组/not 元数/错形组 → unknown+shape_invalid，正常路径未误伤）'
      fi
    fi
  fi
fi

# ---------------------------------------------------------------- 汇总
step '汇总'
printf '  ok %d / FAIL %d / pend %d\n' "$PASS_N" "$FAIL_N" "$PEND_N"
if [[ "$FAIL_N" -gt 0 ]]; then
  printf '\n  FAIL 清单：\n'
  printf '    - %s\n' "${FAIL_LINES[@]}"
fi
if [[ "$PEND_N" -gt 0 ]]; then
  printf '\n  pend 清单（不算通过，报告中要写明缺什么）：\n'
  printf '    - %s\n' "${PEND_LINES[@]}"
fi

if [[ "$FAIL_N" -gt 0 ]]; then
  exit 1
fi
if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then
  printf '\n[exit 4] --strict：存在 %d 条 pend。\n' "$PEND_N" >&2
  exit 4
fi
exit 0
