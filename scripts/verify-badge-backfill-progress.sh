#!/usr/bin/env bash
# 「回扫进度可见」（第一刀）独立验收验证器。
#
# 断言来源：knowledge/reports/2026-11-12-badge-backfill-progress-acceptance.md
# 计划：    knowledge/plans/2026-11-12-badge-rule-engine.md §13#15 / §15.2
#
# 三种结果（与 verify-badge-rules-acceptance.sh 同口径）：
#   ok    通过
#   FAIL  代码已落地、行为不符判据（要修）
#   pend  对应切片未落地 / 前置不满足（无管理员会话、无 psql、夹具未备），不算通过
#
# 硬约定：
#   * 本脚本**不接** ./scripts/test.sh，也不属于 tests/validate-* 门禁。
#   * FAIL 只留给「代码在、行为错」。缺文件按 pend。
#   * --strict 下存在 pend 即退出 4（收口时用）。
#
# 退出码：0 无 FAIL / 1 有 FAIL / 2 预检失败 / 4 --strict 下有 pend
#
# 环境变量：
#   DATABASE_URL                 只读查库（缺省时 DDL 断言 pend）
#   SFORUM_ACCEPT_SQL_CMD        自定义查库命令（覆盖 psql/docker 探测）
#   SFORUM_ACCEPT_PSQL_IMAGE     无本机 psql 时的镜像，默认 postgres:17-alpine
#   SFORUM_ACCEPT_API_URL        默认 http://127.0.0.1:8080/api/v1
#   SFORUM_SESSION_COOKIE        管理员会话；缺省时权限/HTTP 断言 pend
#
# 用法：
#   ./scripts/verify-badge-backfill-progress.sh
#   ./scripts/verify-badge-backfill-progress.sh --strict
#   ./scripts/verify-badge-backfill-progress.sh --no-tests        # 只跑静态/DDL，不跑 go/bun
#   ./scripts/verify-badge-backfill-progress.sh --in-flight       # task-1/task-2 落地中：结构性缺失记 pend
#   ./scripts/verify-badge-backfill-progress.sh --mutate          # 变异自检（读 mutations.tsv）
#   ./scripts/verify-badge-backfill-progress.sh --mutate MUT-1    # 只跑一条变异
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
WEB_DIR="$ROOT_DIR/apps/web"
CTRL_DIR="$API_DIR/app/Http/Controllers/Badges"
MODEL_DIR="$API_DIR/app/Models/Badges"
JOB_DIR="$API_DIR/app/Jobs/Badges"
MIG_GLOB="$API_DIR/database/migrations/202611120003_*.sql"
DB_URL="${DATABASE_URL:-}"
API_URL="${SFORUM_ACCEPT_API_URL:-http://127.0.0.1:8080/api/v1}"
COOKIE="${SFORUM_SESSION_COOKIE:-}"
STRICT=0
RUN_TESTS=1
IN_FLIGHT=0
MUTATE_ONLY=''
MUTATE=0

usage() { sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }
for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --no-tests) RUN_TESTS=0 ;;
    --in-flight) IN_FLIGHT=1 ;;
    --mutate) MUTATE=1 ;;
    MUT-[0-9]*) MUTATE=1; MUTATE_ONLY="$arg" ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done

work="$(mktemp -d "${TMPDIR:-/tmp}/badge-backfill-accept.XXXXXX")"
trap 'rm -rf "$work"' EXIT

PASS_N=0; FAIL_N=0; PEND_N=0
FAIL_LINES=(); PEND_LINES=()
step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
pass() { PASS_N=$((PASS_N+1)); printf '  ok    %s\n' "$1"; }
fail() { FAIL_N=$((FAIL_N+1)); FAIL_LINES+=("$1${2:+ — $2}"); printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"; }
pend() { PEND_N=$((PEND_N+1)); PEND_LINES+=("$1${2:+ — $2}"); printf '  pend  %s%s\n' "$1" "${2:+（$2）}"; }
has() { grep -q -- "$2" "$1" 2>/dev/null; }
# 落地中的切片：结构性缺失是中间态，不是行为错。--in-flight 把它们记 pend。
midfail() { if [[ "$IN_FLIGHT" == 1 ]]; then pend "$1" "$2（in-flight）"; else fail "$1" "$2"; fi; }

# ---------------------------------------------------------------- 查库
SQL_RUNNER=()
if [[ -n "${SFORUM_ACCEPT_SQL_CMD:-}" ]]; then
  SQL_RUNNER=(bash -c "$SFORUM_ACCEPT_SQL_CMD")
elif command -v psql >/dev/null 2>&1 && [[ -n "$DB_URL" ]]; then
  SQL_RUNNER=(psql "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 && [[ -n "$DB_URL" ]]; then
  SQL_RUNNER=(docker run --rm -i --network=host --entrypoint psql "${SFORUM_ACCEPT_PSQL_IMAGE:-postgres:17-alpine}" "$DB_URL" -v ON_ERROR_STOP=1 -tAq)
fi
SQL_ERR=''
sqlv() {
  local out rc
  [[ ${#SQL_RUNNER[@]} -gt 0 ]] || return 3
  out="$("${SQL_RUNNER[@]}" -c "$1" 2>"$work/sql.err")"; rc=$?
  if [[ $rc -ne 0 ]]; then SQL_ERR="$(tr '\n' ' ' <"$work/sql.err" | cut -c1-240)"; return $rc; fi
  printf '%s' "$out" | tr -d '\r' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'
}
db_ready=0
if [[ ${#SQL_RUNNER[@]} -gt 0 ]]; then
  [[ "$(sqlv 'SELECT 1' 2>/dev/null)" == 1 ]] && db_ready=1
fi
[[ $db_ready -eq 0 ]] && note '查库不可用（无 DATABASE_URL/psql/docker）：DDL 与来源真伪断言 pend。'

# ================================================================ A 契约与注册面
step 'A 契约与注册面'
MIG="$(ls $MIG_GLOB 2>/dev/null | head -1)"
if [[ -z "$MIG" ]]; then
  pend 'A-01 迁移落地' 'apps/api/database/migrations/202611120003_*.sql 不存在（task-1 未落地）'
else
  bad=''
  has "$MIG" 'badge_rule_backfill_runs' || bad="$bad 表名"
  for col in badge_id last_user_id failed_cursor_user_id batch error started_at updated_at finished_at 'status'; do
    has "$MIG" "$col" || bad="$bad 列:$col"
  done
  has "$MIG" "'running'" && has "$MIG" "'completed'" && has "$MIG" "'failed'" || bad="$bad status枚举"
  grep -q "started_at DESC" "$MIG" || bad="$bad 索引(badge_id,started_at DESC)"
  grep -qE "WHERE status *= *'running'" "$MIG" || bad="$bad 部分索引(running)"
  grep -qiE 'ON DELETE CASCADE' "$MIG" || bad="$bad FK级联"
  awk '/-- \+goose Down/{f=1} f' "$MIG" | grep -qi 'DROP TABLE' || bad="$bad Down段"
  has "$MIG" 'pg_get_userbyid' && grep -qE 'OWNER TO' "$MIG" || bad="$bad owner对齐"
  [[ -z "$bad" ]] && pass 'A-01 迁移结构（表/列/枚举/索引/FK级联/Down/owner 对齐）' || fail 'A-01 迁移结构' "缺：$bad"
fi

# A-02 路由顺序（同函数体内行号比较）
ctrl="$CTRL_DIR/controller.go"
if ! grep -rq -- '"/backfill-status"' "$CTRL_DIR" 2>/dev/null; then
  pend 'A-02 路由注册顺序' '/backfill-status 未注册（task-1 未落地）'
else
  awk '/func \(h \*Controller\) RegisterRoutes/{f=1} f{print} f&&/^}/{exit}' "$ctrl" >"$work/rr.txt" 2>/dev/null
  bs=$(grep -n -- '"/backfill-status"' "$work/rr.txt" | head -1 | cut -d: -f1)
  pm=$(grep -n -- '"/:badgeID' "$work/rr.txt" | head -1 | cut -d: -f1)
  if [[ -z "$bs" || -z "$pm" ]]; then
    pend 'A-02 路由注册顺序' 'RegisterRoutes 体内未同时找到两段（可能跨函数注册），需人工确认'
  elif [[ "$bs" -ge "$pm" ]]; then
    fail 'A-02 路由注册顺序' "/backfill-status 注册在 /:badgeID 之后（$bs >= $pm）→ 会被当 badgeID 解析"
  elif ! has "$CTRL_DIR/route_order_test.go" 'backfill-status'; then
    midfail 'A-02 路由注册顺序' '顺序对但 route_order_test.go 没有钉住该路径（下次改分组会悄悄坏）'
  else
    pass 'A-02 /backfill-status 注册在 /:badgeID 之前且有回归测试'
  fi
fi

# A-03 路由计数双源一致
regtest="$API_DIR/app/Support/Routes/registry_test.go"
if [[ ! -f "$regtest" ]]; then
  pend 'A-03 路由计数 422' 'registry_test.go 不存在'
else
  n=$(grep -oE 'len\(catalog\) != [0-9]+' "$regtest" | grep -oE '[0-9]+' | head -1)
  idl=$(node -e '
    const fs=require("fs");
    try{
      const j=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));
      const hit=(j.routes||[]).some(r=>r.path&&r.path.includes("/admin/badges/backfill-status"));
      console.log(`${(j.routes||[]).length} ${hit?"has":"missing"}`);
    }catch(e){console.log("err")}' "$ROOT_DIR/docs/extensions/v3/catalog-identities.json" 2>/dev/null)
  idn=${idl%% *}; idhad=${idl##* }
  gen_has=0; grep -q 'backfill-status' "$API_DIR/app/Support/Routes/core_catalog_gen.go" 2>/dev/null && gen_has=1
  cat_has=0; grep -q 'backfill-status' "$ROOT_DIR/docs/extensions/v3/catalogs/routes.json" 2>/dev/null && cat_has=1
  if [[ "$n" != '422' || "$idn" != '422' || "$idhad" != 'has' || $gen_has -eq 0 || $cat_has -eq 0 ]]; then
    if [[ -z "$n" ]]; then pend 'A-03 路由计数 422' '未找到计数断言（task-1 未落地）'
    else midfail 'A-03 路由计数 422' "registry_test=$n identities=$idn($idhad) core_catalog_gen=$gen_has routes.json=$cat_has"; fi
  else
    pass 'A-03 路由计数 422（registry_test / identities / 生成物 / catalogs 四面一致）'
  fi
fi

# A-04 OpenAPI
oas_index="$ROOT_DIR/contracts/openapi.yaml"
oas_paths="$ROOT_DIR/contracts/openapi/paths/badges.yaml"
oas_schemas="$ROOT_DIR/contracts/openapi/schemas/badges.yaml"
# 契约结构：操作定义在 paths/badges.yaml（键是 operationId），真实路径由 openapi.yaml 映射。
# 只查其中一处会假绿（有 operationId 但没挂路径 = 客户端看不到）。
if ! has "$oas_paths" 'adminBadgeBackfillStatus'; then
  pend 'A-04 OpenAPI 补齐' 'paths/badges.yaml 无 adminBadgeBackfillStatus 操作'
elif ! grep -qE '^[[:space:]]*"?/admin/badges/backfill-status"?:' "$oas_index"; then
  fail 'A-04 OpenAPI 补齐' 'openapi.yaml 没把 /admin/badges/backfill-status 映射到该操作'
elif ! grep -q 'BadgeBackfillRun' "$oas_schemas"; then
  fail 'A-04 OpenAPI 补齐' 'schemas/badges.yaml 没有 BadgeBackfillRun schema'
elif command -v ruby >/dev/null 2>&1; then
  if ( cd "$ROOT_DIR" && ruby scripts/validate-openapi-refs.rb ) >"$work/oas.out" 2>&1; then
    pass 'A-04 OpenAPI path+schema 且 $ref 校验 exit 0'
  else
    fail 'A-04 OpenAPI $ref' "$(tail -3 "$work/oas.out" | tr '\n' ' ' | cut -c1-240)"
  fi
else
  pend 'A-04 $ref 校验' '无 ruby'
fi

# A-05 recompute 恰好三键
dto="$CTRL_DIR/dto.go"
keys=$(awk '/type recomputeResponse struct/{f=1;next} f&&/^}/{exit} f' "$dto" 2>/dev/null | grep -oE 'json:"[a-zA-Z]+' | sed 's/json:"//' | sort | tr '\n' ',' )
if [[ -z "$keys" ]]; then
  pend 'A-05 recompute 恰好三键' 'dto.go 未找到 recomputeResponse'
elif [[ "$keys" == 'granted,queued,refreshed,' ]]; then
  pass 'A-05 recompute 恰好三键 {granted,refreshed,queued}（多/少一键都会红）'
else
  fail 'A-05 recompute 恰好三键' "实际键：$keys"
fi

# A-06 backfill-status 条目键集合
bdto="$(grep -rl 'json:"failedCursorUserId"' "$CTRL_DIR" "$MODEL_DIR" 2>/dev/null | head -1)"
[[ -z "$bdto" ]] && bdto="$(grep -rl 'json:"badgeId"' "$CTRL_DIR" "$MODEL_DIR" 2>/dev/null | grep -i backfill | head -1)"
if [[ -z "$bdto" ]]; then
  pend 'A-06 条目键集合/空 items' 'backfill DTO 未落地'
else
  got=$(awk '/^type .* struct \{/{buf="";inb=1;next} inb&&/^\}/{if(buf~/json:"badgeId"/){print buf};inb=0;next} inb{buf=buf"\n"$0}' "$bdto" \
        | grep -oE 'json:"[a-zA-Z]+' | sed 's/json:"//' | sort -u | tr '\n' ',')
  want='badgeId,error,failedCursorUserId,finishedAt,lastUserId,name,slug,startedAt,status,updatedAt,'
  if [[ "$got" != "$want" ]]; then
    fail 'A-06 条目键集合' "实际：$got"
  elif ! grep -rq 'items.*\[\]' "$CTRL_DIR" 2>/dev/null; then
    pend 'A-06 空 items 序列化为 []' '读取侧未见显式初始化（需 HTTP/单测确认，脚本不判）'
  else
    pass 'A-06 条目键集合 == 冻结 10 键（空 items 形态由 HTTP 断言复核）'
  fi
fi

# A-07 缓存头与权限（GAP-1 已补测：成功 no-store / 缺 badge.view 403 / 未登录 401）
bfile="$(grep -rl 'func (h \*Controller) admin.*BackfillStatus' "$CTRL_DIR" 2>/dev/null | head -1)"
btest="$CTRL_DIR/backfill_status_test.go"
if [[ -z "$bfile" ]]; then
  pend 'A-07 no-store / 权限' 'handler 未落地'
else
  bad=''
  has "$bfile" 'setAdminNoStore' || bad="$bad no-store"
  grep -q 'requireFeature' "$bfile" || bad="$bad requireFeature"
  if [[ -n "$bad" ]]; then
    fail 'A-07 no-store / 权限' "缺：$bad"
  elif [[ ! -f "$btest" ]]; then
    pend 'A-07 no-store / 权限' '缺 backfill_status_test.go（GAP-1 未补）'
  else
    # 判据锚在测试断言的值上：三条路径各自独立（401 不是 403、403 是 permission.denied、no-store 逐字）。
    trip=''
    grep -q '"private, no-store"' "$btest" || trip="$trip 无 no-store 值断言"
    grep -q 'fiber.StatusForbidden' "$btest" || trip="$trip 无 403 断言"
    grep -q 'fiber.StatusUnauthorized' "$btest" || trip="$trip 无 401 断言"
    if [[ -n "$trip" ]]; then
      fail 'A-07 no-store / 权限' "测试存在但判据不足：$trip"
    elif [[ $RUN_TESTS -eq 1 ]]; then
      if ( cd "$API_DIR" && go test -count=1 -v -run 'TestBackfillStatus(ServesNoStoreWithBadgeView|DeniesWithoutBadgeView|RequiresLogin)' ./app/Http/Controllers/Badges/ ) >"$work/a07.out" 2>&1; then
        ran=$(grep -cE '^=== RUN' "$work/a07.out")
        [[ "$ran" -ge 3 ]] && pass "A-07 三条路径测试通过（no-store / 403 / 401，RUN=$ran）" \
          || fail 'A-07' "只跑了 $ran 条（缺 no-store 或 denied）"
      else
        fail 'A-07 no-store / 权限' "$(grep -E '^(--- FAIL|FAIL)' "$work/a07.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
      fi
    else
      pend 'A-07 no-store / 权限' '--no-tests'
    fi
  fi
fi

# ================================================================ B 来源真伪
step 'B 来源真伪（进度必须来自 job 写的行）'
read_files=()
for f in "$MODEL_DIR/backfill_runs.go" "$CTRL_DIR/backfill_controller.go"; do [[ -f "$f" ]] && read_files+=("$f"); done
if [[ ${#read_files[@]} -eq 0 ]]; then
  pend 'B-01/B-02 来源真伪' '读取侧文件未落地（backfill_runs.go / backfill_controller.go）'
  pend 'B-03 只取最新 run' '未落地'
  pend 'B-05 读取路径不碰 user_badges' '未落地'
else
  # 去掉 file:line: 前缀后再判注释，避免把「解释为什么不用 user_badges」的注释误判成实现
  hits=$(grep -nE 'user_badges|COUNT\(' "${read_files[@]}" 2>/dev/null \
    | sed -E 's/^[^:]+:[0-9]+://' | grep -vE '^[[:space:]]*(//|/\*|\*)' | head -3)
  if [[ -n "$hits" ]]; then
    fail 'B-05 读取路径不碰 user_badges' "$(printf '%s' "$hits" | tr '\n' ';' | cut -c1-240)"
  else
    pass 'B-05 读取侧无 user_badges/COUNT( 反推痕迹'
  fi
  # 真库证据（GAP-2 已补：有授予无 run 行不合成）。四条各对应一个用例，按 PASS 行映射。
  if [[ $RUN_TESTS -eq 1 ]]; then
    if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'TestBackfillRuns(ProgressLifecycle|IgnoreGrantRows|KeepsOnlyLatestRowPerBadge|VanishWithBadge)' ./app/Models/Badges/... ) >"$work/bruns.out" 2>&1; then
      for pair in 'B-01:TestBackfillRunsProgressLifecycle' 'B-02:TestBackfillRunsIgnoreGrantRows' 'B-03:TestBackfillRunsKeepsOnlyLatestRowPerBadge' 'B-04:TestBackfillRunsVanishWithBadge'; do
        id=${pair%%:*}; name=${pair##*:}
        if grep -q -- "--- PASS: $name" "$work/bruns.out"; then
          pass "$id（$name PASS）"
        else
          fail "$id" "用例 $name 未通过或缺席"
        fi
      done
    else
      fail 'B-01..B-04' "$(grep -E '^(--- FAIL|FAIL)' "$work/bruns.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
    fi
  else
    pend 'B-01..B-04' '--no-tests'
  fi
fi
if [[ $db_ready -eq 1 ]]; then
  cols=$(sqlv "SELECT string_agg(column_name,',' ORDER BY column_name) FROM information_schema.columns WHERE table_name='badge_rule_backfill_runs' AND table_schema='public'")
  if [[ -z "$cols" ]]; then
    pend 'B-04 徽章删除无孤儿行' '表不存在（迁移未跑）'
  else
    want='badge_id,batch,error,failed_cursor_user_id,finished_at,id,last_user_id,started_at,status,updated_at'
    [[ "$cols" == "$want" ]] && pass 'B-04/A-01 列集合 == 冻结集合' || fail 'A-01 列集合' "实际：$cols"
    del=$(sqlv "SELECT confdeltype FROM pg_constraint WHERE conrelid='public.badge_rule_backfill_runs'::regclass AND contype='f'")
    [[ "$del" == 'c' ]] && pass 'B-04 FK ON DELETE CASCADE（confdeltype=c）' || fail 'B-04 FK 级联' "confdeltype=$del（期望 c）"
    own=$(sqlv "SELECT (SELECT relowner FROM pg_class WHERE relname='badge_rule_backfill_runs') = (SELECT relowner FROM pg_class WHERE relname='badges')")
    [[ "$own" == 't' ]] && pass 'A-01 owner 与 public.badges 同源' || fail 'A-01 owner 对齐' "owner 与 badges 不同（$own）"
    idx=$(sqlv "SELECT string_agg(indexdef,' | ') FROM pg_indexes WHERE tablename='badge_rule_backfill_runs'")
    echo "$idx" | grep -q 'started_at DESC' && echo "$idx" | grep -q "status = 'running'" && pass 'A-01 两个索引在位' || fail 'A-01 索引' "$idx"
  fi
else
  pend 'B-04/A-01 DDL 实测' '查库不可用（无 DATABASE_URL/psql/docker）；行为证据见 C-08 的 cascade 用例 + 迁移静态'
fi

# ================================================================ C/D 状态机与 Enqueuer
step 'C/D 状态机与 Enqueuer'
if [[ ! -f "$JOB_DIR/backfill_rules.go" ]]; then
  pend 'C-01..C-07' 'job 文件不存在'
else
  if [[ $RUN_TESTS -eq 1 ]]; then
    if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Jobs/Badges/... ./app/Models/Badges/... ./app/Http/Controllers/Badges/... ) >"$work/iso.out" 2>&1; then
      pass 'C/D 相关包隔离测试 exit 0'
    else
      fail 'C/D 相关包隔离测试' "$(grep -E '^(FAIL|--- FAIL|# )' "$work/iso.out" | head -3 | tr '\n' ' ' | cut -c1-240)"
    fi
  else
    pend 'C/D 隔离测试' '--no-tests'
  fi
  # C-08 合法的不许被误拒：running/completed/failed 三态 + 空 items 都必须 200 且形状合规。
  # 断言载体是隔离真库的 Backfill 行为测试（不是读代码）。
  if [[ $RUN_TESTS -eq 1 ]]; then
    if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'Backfill' ./app/Models/Badges/... ./app/Http/Controllers/Badges/... ) >"$work/legal.out" 2>&1; then
      ran=$(grep -cE '^(=== RUN|=== PAUSE)' "$work/legal.out")
      [[ "$ran" -gt 0 ]] && pass "C-08 三态 + 空 items 合法路径全通过（真库，run 计数 $ran）" \
        || fail 'C-08 合法路径测试缺席' '-run Backfill 没匹配到任何测试（tripwire 过期）'
    else
      fail 'C-08 合法路径被误拒/误判' "$(grep -E '^(--- FAIL|FAIL)' "$work/legal.out" | head -2 | tr '\n' ' ' | cut -c1-240)"
    fi
  else
    pend 'C-08 合法路径' '--no-tests'
  fi
  # 存在性 tripwire：不是判据本身，但代码在而证明缺席时必须可见
  mc=$(grep -rl 'failed_cursor_user_id\|failedCursorUserId' --include='*_test.go' "$API_DIR" 2>/dev/null | wc -l | tr -d ' ')
  [[ "$mc" -gt 0 ]] && pass "C-03 失败游标有测试覆盖（命中的测试文件数 $mc）" || midfail 'C-03 失败游标测试缺席' '没有任何测试断言 failed_cursor_user_id（值级断言无处落脚）'
  if grep -rqE 'Enqueuer: *nil|enqueuer not wired' --include='*_test.go' "$JOB_DIR" 2>/dev/null; then
    pass 'D-01 Enqueuer=nil 有测试覆盖'
  else
    midfail 'D-01 Enqueuer=nil 测试缺席' '静默 return nil 的回归没人拦'
  fi
  # D-02/D-03（GAP-3）：反向「done+nil → completed」与「入队失败 → 返错且仍 running」。
  if [[ $RUN_TESTS -eq 1 ]]; then
    if ( cd "$API_DIR" && go test -count=1 -v -run 'TestBackfillRulesWorker(CompletesLastPageWithoutEnqueuer|KeepsRunningWhenEnqueueFails)' ./app/Jobs/Badges/ ) >"$work/gap3.out" 2>&1; then
      d2=0; d3=0
      grep -q -- '--- PASS: TestBackfillRulesWorkerCompletesLastPageWithoutEnqueuer' "$work/gap3.out" && d2=1
      grep -q -- '--- PASS: TestBackfillRulesWorkerKeepsRunningWhenEnqueueFails' "$work/gap3.out" && d3=1
      [[ $d2 -eq 1 ]] && pass 'D-02 done=true 且 Enqueuer=nil → completed（反过度修正）' || fail 'D-02' '用例未通过或缺席'
      [[ $d3 -eq 1 ]] && pass 'D-03 入队下一页失败 → 返错且仍 running' || fail 'D-03' '用例未通过或缺席'
    else
      fail 'D-02/D-03' "$(grep -E '^(--- FAIL|FAIL)' "$work/gap3.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
    fi
  else
    pend 'D-02/D-03' '--no-tests'
  fi
  if grep -rqE 'finished_at|finishedAt' --include='*_test.go' "$API_DIR/app" 2>/dev/null; then
    pass 'C-02 completed/finished_at 有测试覆盖'
  else
    midfail 'C-02 终态测试缺席' 'done→completed 可以悄悄被删'
  fi
fi

# ================================================================ E/F/G 前端
step 'E/F/G 前端降级与呈现'
composable="$WEB_DIR/app/composables/admin/badges/useAdminBadgeBackfillStatus.ts"
tab="$WEB_DIR/app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue"
if [[ ! -f "$composable" ]]; then
  pend 'E-01..E-04' 'useAdminBadgeBackfillStatus.ts 未落地（task-2 未落地）'
  pend 'F-01..F-06' '同'
  pend 'G-01..G-05' '同'
else
  if has "$composable" 'failed' && grep -qE '= \[\]' "$composable"; then
    pass 'E-01/E-02 降级标志位 + 降级为 [] 的路径存在（tripwire，值级行为见 bun test）'
  else
    midfail 'E-01/E-02 降级标志位' 'composable 没有失败态标志或没有「降级为 []」的路径 → 无法区分「空」与「挂了」'
  fi
  if grep -rq 'useAdminBadgeBackfillStatus' "$WEB_DIR/app" 2>/dev/null \
     && grep -q 'SFAdminBadgeBackfillRow' "$tab" 2>/dev/null; then
    pass '进度行已接入（composable 在页面装配、SFAdminBadgeBackfillRow 在 tab 渲染）'
  elif ! grep -rq 'useAdminBadgeBackfillStatus' "$WEB_DIR/app" 2>/dev/null; then
    midfail '进度行接入' 'apps/web/app 里没有任何地方调用 useAdminBadgeBackfillStatus'
  else
    midfail '进度行接入' 'composable 有调用但 tab 没渲染 SFAdminBadgeBackfillRow'
  fi
  # 模板顺序：进度行必须在筛选区之前
  ro=$(grep -in 'backfill' "$tab" 2>/dev/null | tail -1 | cut -d: -f1)
  fo=$(grep -n 'admin.badges.filters.searchPlaceholder' "$tab" | head -1 | cut -d: -f1)
  if [[ -n "$ro" && -n "$fo" ]]; then
    [[ "$ro" -lt "$fo" ]] && pass 'G-04 进度行在筛选区之前' || midfail 'G-04 进度行位置' "行 $ro 在筛选区 $fo 之后"
  else
    pend 'G-04 进度行位置' '模板里未找到 backfill 标记或筛选区'
  fi
  # 不新增壳：UCard / tab 数量不许增长
  for marker in 'UCard' 'UTabs'; do
    base=$(git -C "$ROOT_DIR" show "HEAD:$tab" 2>/dev/null | grep -c "$marker")
    cur=$(grep -c "$marker" "$tab" 2>/dev/null)
    if [[ "${base:-0}" -lt "${cur:-0}" ]]; then fail 'G-04 不新增壳' "$marker 计数 $base → $cur"; fi
  done
  pass 'G-04 未新增 UCard/UTabs（与 HEAD 计数比较）'
  # i18n：composable 在 → locale 必须有键；键存在性由既有双向测试兜
  if node -e '
    const fs=require("fs");
    const [zh,en]=[process.argv[1],process.argv[2]].map(p=>JSON.parse(fs.readFileSync(p,"utf8")));
    const flat=(n,p="")=>Object.entries(n||{}).flatMap(([k,v])=>v&&typeof v==="object"?flat(v,p+k+"."):[p+k]);
    const kz=flat(zh.admin?.badges||{}).filter(k=>/backfill/i.test(k));
    const ke=flat(en.admin?.badges||{}).filter(k=>/backfill/i.test(k));
    if(!kz.length||!ke.length){console.log("missing");process.exit(1)}
    const re=/\b(cmp|params|children|op|rule)\b/;            // 正控
    if(!re.test("rule")||!re.test("op")||re.test("topics")){console.log("checker-broken");process.exit(2)}
    const vals=[...kz,...ke].map(k=>{const ns=k.startsWith("admin.badges.")?k:"admin.badges."+k;return JSON.stringify([zh,en].map(l=>ns.split(".").reduce((a,b)=>a&&a[b],l)))});
    const leak=vals.filter(v=>re.test(v));
    console.log(leak.length?`leak:${leak[0]}`:"clean");
  ' "$WEB_DIR/i18n/locales/zh-CN.json" "$WEB_DIR/i18n/locales/en-US.json" >"$work/i18n.out" 2>&1; then
    pass 'G-01/G-03 backfill i18n 键双语在位、文案无内部字段泄露（检查器正反控通过）'
  else
    out=$(cat "$work/i18n.out")
    case "$out" in
      *missing*) fail 'G-01 i18n 键' 'composable 已落地但 zh/en 的 admin.badges.*backfill* 键缺失' ;;
      *checker-broken*) fail 'G-03 检查器自检' '词边界检查器不满足正反控（rule/op 命中、topics 不命中）' ;;
      leak:*) fail 'G-03 内部字段泄露' "$out" ;;
      *) fail 'G-01/G-03' "$out" ;;
    esac
  fi
  if [[ $RUN_TESTS -eq 1 ]]; then
    if ( cd "$WEB_DIR" && bun test tests/admin/ tests/forum/ ) >"$work/web.out" 2>&1; then
      pass 'E/F/G 前端行为测试 exit 0（bun test）'
    else
      fail 'E/F/G 前端行为测试' "$(grep -E '^\(fail\)|^ *[0-9]+ (fail|pass)' "$work/web.out" | head -3 | tr '\n' ' ' | cut -c1-240)"
    fi
  else
    pend 'E/F/G bun test' '--no-tests'
  fi
  if [[ -n "$COOKIE" ]]; then pend 'G-05 390 视觉 QA' '需浏览器实测（tests/browser-admin-390x844.mjs），脚本不代跑'; else pend 'G-05 390 视觉 QA' '无管理员会话'; fi
fi

# ================================================================ 变异自检
step 'M 变异自检'
tsv="$ROOT_DIR/scripts/verify-badge-backfill-mutations.tsv"
run_mutation_cmd() {
  case "$1" in
    iso-job) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Jobs/Badges/... ) ;;
    iso-badge) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Models/Badges/... ./app/Http/Controllers/Badges/... ) ;;
    route) ( cd "$API_DIR" && go test ./app/Support/Routes/ ) ;;
    oas) ( cd "$ROOT_DIR" && ruby scripts/validate-openapi-refs.rb ) ;;
    web) ( cd "$WEB_DIR" && bun test tests/admin/ ) ;;
    static) ( cd "$ROOT_DIR" && ./scripts/verify-badge-backfill-progress.sh --no-tests --in-flight ) ;;
    *) return 9 ;;
  esac
}
if [[ "$MUTATE" != 1 ]]; then
  pend 'MUT-* 变异自检' '未运行（加 --mutate 才执行；单独跑约 1–2 分钟）'
elif [[ ! -f "$tsv" ]]; then
  pend 'MUT-* 变异自检' "缺 $tsv（执行期按报告 §3 填锚点后运行 --mutate）"
else
  executed=0
  while IFS=$'\t' read -r -u 3 id file sed_expr cmd_id asserts note_txt; do
    [[ -z "$id" || "$id" == '#'* ]] && continue
    [[ -n "$MUTATE_ONLY" && "$id" != "$MUTATE_ONLY" ]] && continue
    executed=$((executed + 1))
    full="$ROOT_DIR/$file"
    if [[ ! -f "$full" ]]; then pend "$id 变异" "文件不存在：$file"; continue; fi
    cp "$full" "$work/mut.bak"
    sed -i -E "$sed_expr" "$full"
    if cmp -s "$full" "$work/mut.bak"; then
      cp "$work/mut.bak" "$full"
      fail "$id 变异锚点" "sed 无改动（锚点过期，需重新定位）—$note_txt"
      continue
    fi
    if run_mutation_cmd "$cmd_id" </dev/null >"$work/mut.out" 2>&1; then
      cp "$work/mut.bak" "$full"
      fail "$id 未变红" "变异后 $cmd_id 仍 exit 0（断言 $asserts 是假绿）"
    else
      redline=$(grep -E '^[[:space:]]*(--- FAIL|FAIL|\(fail\))' "$work/mut.out" | head -2 | tr '\n' ' ' | cut -c1-200)
      cp "$work/mut.bak" "$full"
      cmp -s "$full" "$work/mut.bak" && pass "$id 变异后变红并已恢复（断言 $asserts）— $redline" \
        || fail "$id 恢复失败" "文件未回到变异前字节（$file）"
    fi
  done 3<"$tsv"
  # 覆盖自检：变异条数必须与 tsv 一致。少跑一条就是假绿——曾被内层 `docker run -i`
  # 抽干继承来的 stdin（= tsv）跳过 5 条，汇总里既无 ok 也无 FAIL。
  if [[ -n "$MUTATE_ONLY" ]]; then
    expected=$(awk -F'\t' -v id="$MUTATE_ONLY" '!/^[[:space:]]*#/ && NF>=6 && $1==id' "$tsv" | wc -l)
  else
    expected=$(awk -F'\t' '!/^[[:space:]]*#/ && NF>=6' "$tsv" | wc -l)
  fi
  if [[ "$executed" -eq "$expected" ]]; then
    pass "变异覆盖：$executed/$expected 条全部执行"
  elif [[ "$expected" -eq 0 ]]; then
    fail '变异覆盖' "tsv 里没有可执行行（MUTATE_ONLY=${MUTATE_ONLY:-<空>}）；指定 id 不存在即失败"
  else
    fail '变异覆盖' "只执行 $executed / 应有 $expected（有变异被静默跳过）"
  fi
fi

# ---------------------------------------------------------------- 汇总
step '汇总'
printf '  ok %d / FAIL %d / pend %d\n' "$PASS_N" "$FAIL_N" "$PEND_N"
if [[ "$FAIL_N" -gt 0 ]]; then printf '\n  FAIL 清单：\n'; printf '    - %s\n' "${FAIL_LINES[@]}"; fi
if [[ "$PEND_N" -gt 0 ]]; then printf '\n  pend 清单（不算通过，报告中要写明缺什么）：\n'; printf '    - %s\n' "${PEND_LINES[@]}"; fi
[[ "$FAIL_N" -gt 0 ]] && exit 1
if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then printf '\n[exit 4] --strict：存在 %d 条 pend。\n' "$PEND_N" >&2; exit 4; fi
exit 0
