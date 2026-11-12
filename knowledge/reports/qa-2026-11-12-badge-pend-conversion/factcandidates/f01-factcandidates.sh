#!/usr/bin/env bash
# F-01 / GAP-C：`GET /admin/badges/conditions` 的 `factCandidates[]` **HTTP 值级**实测。
#
# 前提：库里必须有一个**已启用**且声明了 `badgeConditions` 的扩展
# （`bootstrap/badges_fact_catalog.go` 只采 `StatusEnabled && TypePlugin`）。
# 本脚本走 `sforum.query-surface-reference` 现有 disabled 墓碑的 re-enable 路径，
# 跑完恢复到 disabled 墓碑，并删掉一次性超管账号。
#
# 用法：bash f01-factcandidates.sh
# 退出码：0 全绿；1 有断言红；2 环境/前置失败
set -uo pipefail

API="${API:-http://127.0.0.1:8080/api/v1}"
ORIGIN="${ORIGIN:-http://127.0.0.1:3000}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JAR="$DIR/session.jar"
BODY="$DIR/.body.out"
LOG="$DIR/f01-run.log"
EXT="sforum.query-surface-reference"
ROOT="$(cd "$DIR/../../../.." && pwd)"
ZIP="$DIR/$EXT.zip"

: >"$LOG"
exec > >(tee -a "$LOG") 2>&1

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
ok()   { printf '  ok   %s\n' "$*"; }
bad()  { printf '  FAIL %s\n' "$*"; FAILS=$((FAILS + 1)); }
FAILS=0

# ---------------------------------------------------------------- DB 助手
# 只碰开发库容器，且先验哨兵：这台是 dev（有 public.sforum_dev_guard）。
PG_CID="${PG_CID:-sforum-postgres}"
pgv() {
  docker exec -e PGPASSWORD=sforum "$PG_CID" psql -U sforum -d sforum -v ON_ERROR_STOP=1 -tAq -c "$1" 2>&1
}
guard="$(pgv "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL")"
if [[ "$guard" != "t" ]]; then
  say "[exit 2] 目标库没有 sforum_dev_guard 哨兵，拒绝执行（不是开发库）。got=$guard"
  exit 2
fi

# ---------------------------------------------------------------- HTTP 助手
cookie_value() {
  local name=$1
  [[ -f "$JAR" ]] || return 0
  awk -v n="$name" '$6 == n { printf "%s", $7 }' "$JAR" 2>/dev/null
}

HTTP_STATUS=000
api() { # api <METHOD> <PATH> [JSON_BODY]
  local method=$1 path=$2 body=${3:-}
  local -a args=(-sS -o "$BODY" -w '%{http_code}' -X "$method"
    -b "$JAR" -c "$JAR" -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/")
  local token; token="$(cookie_value csrf_)"
  [[ -n "$token" ]] && args+=(-H "X-CSRF-Token: $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
  for attempt in 1 2 3; do
    : >"$BODY"
    HTTP_STATUS="$(curl "${args[@]}" --max-time 25 "$API$path" 2>/dev/null)" || HTTP_STATUS=000
    [[ "$HTTP_STATUS" == 429 ]] && { sleep "$((attempt * 3))"; continue; }
    [[ "$HTTP_STATUS" == 000 ]] && { sleep 2; continue; }
    break
  done
}
body_head() { head -c 400 "$BODY" 2>/dev/null | tr '\n' ' '; }

prime_csrf() { api GET /auth/session >/dev/null; }
login_into() { # <login> <password>
  prime_csrf
  api POST /auth/login "$(jq -nc --arg l "$1" --arg p "$2" '{login:$l,password:$p}')"
}

# ---------------------------------------------------------------- 0 前置
step "0 前置检查"
probe="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$API/health" 2>/dev/null)"
[[ "$probe" == "200" ]] && ok "API /health 200" || { say "[exit 2] API 不可达（$probe）"; exit 2; }
command -v jq >/dev/null && ok "jq 可用" || { say '[exit 2] 缺 jq'; exit 2; }

BEFORE_EXT="$(pgv "SELECT status||'|'||coalesce(active_version_id::text,'')||'|'||coalesce(staged_version_id::text,'') FROM extensions WHERE id='$EXT'")"
BEFORE_VER="$(pgv "SELECT count(*) FROM extension_versions WHERE extension_id='$EXT'")"
BEFORE_MEM="$(pgv "SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
BEFORE_MEM_IDS="$(pgv "SELECT coalesce(string_agg(publication_revision::text, ',' ORDER BY publication_revision),'') FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
BEFORE_ACTIVE="$(pgv "SELECT coalesce(active_version_id::text,'') FROM extensions WHERE id='$EXT'")"
say "  扩展初始状态: $BEFORE_EXT  versions=$BEFORE_VER  members=$BEFORE_MEM"

# ---------------------------------------------------------------- 1 一次性超管
step "1 建一次性超管账号"
TS="$(date +%s)"
QA_USER="qapb$TS"
QA_PASS="${QA_PASS:?set QA_PASS to a throwaway password (not persisted in this repo)}"
QA_EMAIL="$QA_USER@qa.pend.local"
: >"$JAR"
prime_csrf
api POST /auth/register "$(jq -nc --arg u "$QA_USER" --arg e "$QA_EMAIL" --arg p "$QA_PASS" \
  '{username:$u,email:$e,password:$p,displayName:"QA Pend BE",locale:"zh-CN"}')"
if [[ "$HTTP_STATUS" != "200" && "$HTTP_STATUS" != "201" ]]; then
  say "[exit 2] 注册失败 HTTP $HTTP_STATUS：$(body_head)"; exit 2
fi
QA_UID="$(pgv "SELECT id FROM users WHERE username='$QA_USER'")"
[[ "$QA_UID" =~ ^[0-9]+$ ]] || { say "[exit 2] 读不到新账号 id：$QA_UID"; exit 2; }
ok "注册成功 uid=$QA_UID（$QA_USER）"
granted="$(pgv "INSERT INTO user_roles (user_id, role_id) SELECT $QA_UID, id FROM roles WHERE key='super_admin' RETURNING role_id")"
[[ "$granted" =~ ^[0-9]+$ ]] || { say "[exit 2] 授予 super_admin 失败：$granted"; exit 2; }
ok "授予 super_admin role_id=$granted"
login_into "$QA_USER" "$QA_PASS"
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] 登录失败 HTTP $HTTP_STATUS：$(body_head)"; exit 2; }
ok "登录成功（新会话已带上 super_admin）"
api GET /admin/badges/conditions >/dev/null
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] 管理员权限未生效：conditions HTTP $HTTP_STATUS $(body_head)"; exit 2; }
ok "权限生效：/admin/badges/conditions 200"

# ---------------------------------------------------------------- 2 安装/启用扩展
step "2 安装并启用 $EXT"
# 背景：m34 卸载后留下的是 status=disabled 且 active_version_id IS NULL 的墓碑。
# 而 `extensionSelectSQL()` 是 `JOIN extension_versions ON id = active_version_id`，
# 所以 enable/disable/DELETE 这类带 :id 的入口对它一律 404 extension.not_found——
# 不是权限问题，是墓碑没有「运行中制品」可指向。因此正路是 install-or-upgrade
# （POST /admin/extensions，同一个 service.InstallOrUpgradeArchive）。
FIXTURE_DIR="$ROOT/extensions/fixtures/plugins/sforum-query-surface-reference"
[[ -d "$FIXTURE_DIR" ]] || { say "[exit 2] 夹具目录不存在：$FIXTURE_DIR"; exit 2; }
rm -f "$ZIP"
( cd "$FIXTURE_DIR" && zip -qr "$ZIP" . ) || { say '[exit 2] 打包夹具失败'; exit 2; }
ok "打包 $(basename "$ZIP")（$(wc -c <"$ZIP") 字节，sforum.extension.json 在根）"

prime_csrf
upload_status="$(curl -sS -o "$BODY" -w '%{http_code}' -X POST -b "$JAR" -c "$JAR" \
  -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/" -H "X-CSRF-Token: $(cookie_value csrf_)" \
  -F "file=@$ZIP" "$API/admin/extensions" 2>/dev/null)"
say "  POST /admin/extensions -> $upload_status $(body_head)"
[[ "$upload_status" == "200" || "$upload_status" == "201" ]] || { say '[exit 2] 安装失败'; exit 2; }
cp "$BODY" "$DIR/install-response.json"
ok "安装完成（状态：$(jq -r '.data.status // .status // "?"' "$BODY" 2>/dev/null)）"

enable_with_trust() {
  api POST "/admin/extensions/$EXT/enable" '{"confirmCapabilities":true}'
  if [[ "$HTTP_STATUS" == "200" ]]; then ok "enable 200（无需信任挑战）"; return 0; fi
  say "  enable(no-token) -> $HTTP_STATUS $(body_head)"
  api POST "/admin/extensions/$EXT/trust/challenge"
  if [[ "$HTTP_STATUS" != "200" ]]; then
    bad "trust/challenge HTTP $HTTP_STATUS $(body_head)"; return 1
  fi
  local token; token="$(jq -r '.data.token // .token // empty' "$BODY" 2>/dev/null)"
  if [[ -z "$token" ]]; then bad "challenge 里没有 token：$(body_head)"; return 1; fi
  ok "取得 exact-artifact confirmationToken（${#token} 字符）"
  api POST "/admin/extensions/$EXT/enable" \
    "$(jq -nc --arg t "$token" '{confirmCapabilities:true,confirmationToken:$t}')"
  if [[ "$HTTP_STATUS" == "200" ]]; then ok "enable(token) 200"; return 0; fi
  bad "enable(token) HTTP $HTTP_STATUS $(body_head)"; return 1
}

enable_with_trust || { say '[exit 2] 扩展启用失败，后续无从验起'; exit 2; }

AFTER_STATUS="$(pgv "SELECT status FROM extensions WHERE id='$EXT'")"
[[ "$AFTER_STATUS" == "enabled" ]] && ok "库内 status=enabled" || bad "库内 status=$AFTER_STATUS（期望 enabled）"
# 候选快照有 5s TTL，等它过期再取，避免读到启用前的空快照。
sleep 6
ok "等过 factCandidates 的 5s 快照 TTL"

# ---------------------------------------------------------------- 3 取 /conditions
step "3 GET /admin/badges/conditions（同一会话）"
api GET /admin/badges/conditions
[[ "$HTTP_STATUS" == "200" ]] || { bad "conditions HTTP $HTTP_STATUS $(body_head)"; }
cp "$BODY" "$DIR/conditions-enabled.json"
ok "响应已存 conditions-enabled.json（$(wc -c <"$DIR/conditions-enabled.json") 字节）"

# ---------------------------------------------------------------- 4 值级断言
step "4 factCandidates 值级断言"
node "$DIR/assert-factcandidates.mjs" "$DIR/conditions-enabled.json" \
  "sforum.query-surface-reference.onboarding.tasks" \
  "sforum.query-surface-reference.verified_reports" | tee "$DIR/assert-factcandidates.log"
assert_rc=${PIPESTATUS[0]}
if [[ "$assert_rc" == 0 ]]; then ok 'F-01 值级断言全绿'; else bad "F-01 值级断言红（见 assert-factcandidates.log）"; fi

# ---------------------------------------------------------------- 5 清理
step "5 清理：扩展回到 disabled 墓碑 + 删一次性账号"
api POST "/admin/extensions/$EXT/disable"
say "  disable -> $HTTP_STATUS $(body_head)"
api DELETE "/admin/extensions/$EXT"
say "  DELETE  -> $HTTP_STATUS $(body_head)"
api POST "/admin/extensions/$EXT/uninstall-cleanup/finalize"
say "  finalize -> $HTTP_STATUS $(body_head)"

# 产品路径的 uninstall 不清 `active_version_id` 投影；若测试前它是 NULL，这里还原。
NOW_ACTIVE="$(pgv "SELECT coalesce(active_version_id::text,'') FROM extensions WHERE id='$EXT'")"
if [[ "$BEFORE_ACTIVE" == "" && "$NOW_ACTIVE" != "" ]]; then
  pgv "UPDATE extensions SET active_version_id=NULL WHERE id='$EXT' AND active_version_id=$NOW_ACTIVE" >/dev/null
  say "  还原 active_version_id：$NOW_ACTIVE -> NULL（测试前形态）"
fi
# plugin_runtime_publication_members 是 **append-only**：
#   plugin_runtime_publication_member_immutable  BEFORE DELETE OR UPDATE → RAISE
#   plugin_runtime_publication_member_no_truncate BEFORE TRUNCATE → RAISE
# 每次 enable 追加一条 desired-publication 修订行，这是设计而非残留。禁止
# DISABLE TRIGGER 去把它「擦干净」，所以这里只记录增量，不删。
NOW_MEM_IDS="$(pgv "SELECT coalesce(string_agg(publication_revision::text, ',' ORDER BY publication_revision),'') FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
say "  publication members：基線 [$BEFORE_MEM_IDS] -> 现在 [$NOW_MEM_IDS]（append-only，本轮 enable 各追加 1 行）"

AFTER_EXT="$(pgv "SELECT status||'|'||coalesce(active_version_id::text,'')||'|'||coalesce(staged_version_id::text,'') FROM extensions WHERE id='$EXT'")"
AFTER_VER="$(pgv "SELECT count(*) FROM extension_versions WHERE extension_id='$EXT'")"
AFTER_MEM="$(pgv "SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
say "  扩展收尾状态: $AFTER_EXT  versions=$AFTER_VER  members=$AFTER_MEM"
WANT_EXT="disabled|$BEFORE_ACTIVE|"
[[ "$AFTER_EXT" == "$WANT_EXT" ]] && ok "extensions 回到测试前状态（$AFTER_EXT）" \
  || bad "扩展未回到测试前状态：got=[$AFTER_EXT] want=[$WANT_EXT]"
[[ "$AFTER_VER" == "$BEFORE_VER" ]] && ok "extension_versions 计数回到 $BEFORE_VER" \
  || bad "extension_versions 计数 $BEFORE_VER -> $AFTER_VER"
added_mem=$(( AFTER_MEM - BEFORE_MEM ))
ok "plugin_runtime_publication_members：基线 $BEFORE_MEM -> $AFTER_MEM（+$added_mem，append-only 追加，不可删）"

# 扩展已 disabled（不在管理器列表）→ 候选必须退化成 [] 而不是 null/缺席
step "6 空候选形态：factCandidates 必须是 [] 不是 null"
api GET /admin/badges/conditions
[[ "$HTTP_STATUS" == "200" ]] || bad "conditions HTTP $HTTP_STATUS $(body_head)"
cp "$BODY" "$DIR/conditions-disabled.json"
node "$DIR/assert-empty-candidates.mjs" "$DIR/conditions-disabled.json" | tee "$DIR/assert-empty-candidates.log"
[[ "${PIPESTATUS[0]}" == 0 ]] && ok '空候选形态断言全绿' || bad '空候选形态断言红'


say '  -- 一次性账号清理'
for stmt in \
  "DELETE FROM user_roles WHERE user_id=$QA_UID" \
  "DELETE FROM user_sessions WHERE user_id=$QA_UID" \
  "DELETE FROM user_credentials WHERE user_id=$QA_UID" \
  "DELETE FROM users WHERE id=$QA_UID"; do
  out="$(pgv "$stmt")"
  say "    $stmt -> ${out:-ok}"
done
left_users="$(pgv "SELECT count(*) FROM users WHERE id=$QA_UID")"
left_roles="$(pgv "SELECT count(*) FROM user_roles WHERE user_id=$QA_UID")"
left_sess="$(pgv "SELECT count(*) FROM user_sessions WHERE user_id=$QA_UID")"
left_cred="$(pgv "SELECT count(*) FROM user_credentials WHERE user_id=$QA_UID")"
say "    复核 users=$left_users user_roles=$left_roles user_sessions=$left_sess user_credentials=$left_cred"
[[ "$left_users$left_roles$left_sess$left_cred" == "0000" ]] && ok '账号残留计数全 0' \
  || bad '账号残留计数不为 0'

# 探针数据复核：本轮不建徽章/规则，只确认没顺手留下
probe_badges="$(pgv "SELECT count(*) FROM badges WHERE slug LIKE 'qa-%'")"
probe_runs="$(pgv "SELECT count(*) FROM badge_rule_backfill_runs r LEFT JOIN badges b ON b.id=r.badge_id WHERE b.id IS NULL")"
say "    探针数据：badges(qa-*)=$probe_badges  孤儿 run 行=$probe_runs"
[[ "$probe_badges$probe_runs" == "00" ]] && ok '探针数据 0 残留' || bad '探针数据有残留'

say ''
say "SUMMARY fails=$FAILS"
[[ "$FAILS" -eq 0 ]] || exit 1
say 'F-01 / GAP-C 转 ok'
