#!/usr/bin/env bash
# 任务 ④ 一键驱动：扩展启用 → m3（--mutate）→ backfill（基线 + 可选变异）→ 收尾。
# 顺序铁律：会话先建、扩展先启、m3 再跑、backfill 最后、清理永远在末尾且必达。
# 产物全部落本目录。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API="${API:-http://127.0.0.1:8080/api/v1}"
ORIGIN="${ORIGIN:-http://127.0.0.1:3000}"
EXT="sforum.query-surface-reference"
CLUSTER="${CLUSTER:-}"
JAR="$DIR/session-run.jar"
BODY="$DIR/.body-run.out"
ZIP="$DIR/$EXT.zip"
LOG="$DIR/run-pend-conversion.log"
HTTP_STATUS=000
FAILS=0

: >"$LOG"
exec > >(tee -a "$LOG") 2>&1

say()  { printf '%s\n' "$*"; }
ok()   { printf '  ok   %s\n' "$*"; }
bad()  { printf '  FAIL %s\n' "$*"; FAILS=$((FAILS + 1)); }
step() { printf '\n======== %s\n' "$*"; }

pgv() { docker exec -e PGPASSWORD=sforum "${PG_CID:-sforum-postgres}" psql -U sforum -d sforum -v ON_ERROR_STOP=1 -tAq -c "$1" 2>&1; }
cookie_value() { [[ -f "$JAR" ]] && awk -v n="$1" '$6 == n { printf "%s", $7 }' "$JAR" 2>/dev/null; return 0; }
# 把 cookie jar 摊成 `name=value; name=value`，脚本用 `curl -b "<这串>"`。
cookie_header() {
  [[ -f "$JAR" ]] || return 0
  # HttpOnly cookie 在 Netscape 格式里以 `#HttpOnly_` 前缀出现，不能按 `#` 注释过滤掉，
  # 否则 sforum_session 会被漏掉，只剩 csrf_ → 服务端 401。
  awk 'NF >= 7 && ($1 ~ /^#HttpOnly_/ || $1 !~ /^#/) {
         name=$6; val=$7;
         sub(/^#HttpOnly_/, "", $1);
         if (val == "") next;
         printf "%s%s=%s", (n++ ? "; " : ""), name, val
       }' "$JAR" 2>/dev/null
}
api() {
  local method=$1 path=$2 body=${3:-}
  local -a args=(-sS -o "$BODY" -w '%{http_code}' -X "$method" -b "$JAR" -c "$JAR" \
    -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/")
  local token; token="$(cookie_value csrf_)"
  [[ -n "$token" ]] && args+=(-H "X-CSRF-Token: $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
  : >"$BODY"
  HTTP_STATUS="$(curl "${args[@]}" --max-time 25 "$API$path" 2>/dev/null)" || HTTP_STATUS=000
}
body_head() { head -c 300 "$BODY" 2>/dev/null | tr '\n' ' '; }

# ================================================================ 0 前置
step "0 前置"
say "  时间 $(date '+%F %T %z')"
health="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$API/health" 2>/dev/null)"
[[ "$health" == "200" ]] && ok "API /health 200" || { say "[exit 2] API 不可达 $health"; exit 2; }
say "  API 进程: pid $(pgrep -f 'tmp/sforum-api' | head -1) lstart $(ps -o lstart= -p "$(pgrep -f 'tmp/sforum-api' | head -1)" 2>/dev/null)"
BEFORE_ACTIVE="$(pgv "SELECT coalesce(active_version_id::text,'') FROM extensions WHERE id='$EXT'")"
BEFORE_MEM="$(pgv "SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
BEFORE_MEM_IDS="$(pgv "SELECT coalesce(string_agg(publication_revision::text, ',' ORDER BY publication_revision),'') FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
say "  基线：active_version_id=[$BEFORE_ACTIVE] members=$BEFORE_MEM [$BEFORE_MEM_IDS]"
if [[ -n "$CLUSTER" ]]; then
  cport="$(docker port "$CLUSTER" 5432/tcp 2>/dev/null | head -1)"
  say "  一次性集群 $CLUSTER -> $cport"
fi

# ================================================================ 1 会话
step "1 一次性超管会话"
TS="$(date +%s)"; QA_USER="qapb$TS"; QA_PASS="${QA_PASS:?set QA_PASS to a throwaway password (not persisted in this repo)}"
: >"$JAR"
api GET /auth/session >/dev/null
api POST /auth/register "$(jq -nc --arg u "$QA_USER" --arg e "$QA_USER@qa.pend.local" --arg p "$QA_PASS" \
  '{username:$u,email:$e,password:$p,displayName:"QA Pend BE",locale:"zh-CN"}')"
[[ "$HTTP_STATUS" == "200" || "$HTTP_STATUS" == "201" ]] || { say "[exit 2] 注册失败 $HTTP_STATUS $(body_head)"; exit 2; }
QA_UID="$(pgv "SELECT id FROM users WHERE username='$QA_USER'")"
pgv "INSERT INTO user_roles (user_id, role_id) SELECT $QA_UID, id FROM roles WHERE key='super_admin'" >/dev/null
ok "uid=$QA_UID 授 super_admin"
api GET /auth/session >/dev/null
api POST /auth/login "$(jq -nc --arg l "$QA_USER" --arg p "$QA_PASS" '{login:$l,password:$p}')"
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] 登录失败 $HTTP_STATUS $(body_head)"; exit 2; }
COOKIE="$(cookie_header)"
say "  cookie 串长度 ${#COOKIE}：$(printf '%s' "$COOKIE" | sed 's/=.\{0,6\}[^;]*/=***/g' | cut -c1-160)"
api GET /admin/badges/conditions >/dev/null
[[ "$HTTP_STATUS" == "200" ]] && ok "会话有效（conditions 200）" || bad "conditions $HTTP_STATUS"

# ================================================================ 2 扩展启用
step "2 安装并启用 $EXT（追加 1 行 publication member 是预期）"
rm -f "$ZIP"; ( cd "$ROOT/extensions/fixtures/plugins/sforum-query-surface-reference" && zip -qr "$ZIP" . ) || { say '[exit 2] 打包失败'; exit 2; }
api GET /auth/session >/dev/null
up="$(curl -sS -o "$BODY" -w '%{http_code}' -X POST -b "$JAR" -c "$JAR" \
  -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/" -H "X-CSRF-Token: $(cookie_value csrf_)" \
  -F "file=@$ZIP" "$API/admin/extensions" 2>/dev/null)"
say "  POST /admin/extensions -> $up"
[[ "$up" == "200" || "$up" == "201" ]] || { say '[exit 2] 安装失败'; exit 2; }
api POST "/admin/extensions/$EXT/enable" '{"confirmCapabilities":true}'
say "  enable -> $HTTP_STATUS"
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] enable 失败 $(body_head)"; exit 2; }
st="$(pgv "SELECT status FROM extensions WHERE id='$EXT'")"
[[ "$st" == "enabled" ]] && ok "status=enabled" || bad "status=$st"
AFTER_MEM="$(pgv "SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
NEW_ROW="$(pgv "SELECT publication_revision||'|'||extension_version_id||'|'||extension_version||'|'||package_digest FROM plugin_runtime_publication_members WHERE extension_id='$EXT' AND publication_revision NOT IN (${BEFORE_MEM_IDS:-0}) ORDER BY publication_revision DESC LIMIT 1")"
say "  members: $BEFORE_MEM -> $AFTER_MEM；新行: $NEW_ROW"
say '  等 7s 让 factCandidates 快照 TTL 过期'
sleep 7

# ================================================================ 3 m3
step "3 m3 验收（--mutate，带会话）"
m3log="$DIR/m3-mutate.log"
( cd "$ROOT" && SFORUM_SESSION_COOKIE="$COOKIE" ./scripts/verify-badge-m3-manifest.sh --mutate ) >"$m3log" 2>&1
m3rc=$?
say "  exit=$m3rc"
grep -E '^(ok|FAIL|pend|[[:space:]]*(ok|FAIL|pend))' "$m3log" | tail -8
say '  -- 汇总行原文'
grep -nE 'ok [0-9]+ */ *FAIL|汇总|F-01' "$m3log" | tail -10
[[ $m3rc -eq 0 ]] && ok "m3 exit 0" || bad "m3 exit $m3rc"

# ================================================================ 4 backfill
if [[ "${SKIP_BACKFILL:-0}" == "1" ]]; then
  say ''
  say '  （SKIP_BACKFILL=1：跳过 backfill，沿用 backfill-db-session.log 的原文）'
else
step "4 backfill 验收（带 DATABASE_URL + 会话）"
bflog="$DIR/backfill-db-session.log"
( cd "$ROOT" && DATABASE_URL="${DATABASE_URL_CLUSTER:-}" SFORUM_SESSION_COOKIE="$COOKIE" \
    ./scripts/verify-badge-backfill-progress.sh ) >"$bflog" 2>&1
bfrc=$?
say "  exit=$bfrc"
grep -nE 'A-01|B-04|G-05|ok [0-9]+ */ *FAIL' "$bflog" | tail -12
[[ $bfrc -eq 0 ]] && ok "backfill exit 0" || bad "backfill exit $bfrc"

if [[ "${RUN_BACKFILL_MUTATE:-0}" == "1" ]]; then
  step "4b backfill 变异口径（--mutate，带 DATABASE_URL + 会话）"
  bfmlog="$DIR/backfill-db-session-mutate.log"
  ( cd "$ROOT" && DATABASE_URL="${DATABASE_URL_CLUSTER:-}" SFORUM_SESSION_COOKIE="$COOKIE" \
      ./scripts/verify-badge-backfill-progress.sh --mutate ) >"$bfmlog" 2>&1
  bfmrc=$?
  say "  exit=$bfmrc"
  grep -nE 'A-01|B-04|G-05|ok [0-9]+ */ *FAIL' "$bfmlog" | tail -12
  [[ $bfmrc -eq 0 ]] && ok "backfill --mutate exit 0" || bad "backfill --mutate exit $bfmrc"
fi

fi

# ================================================================ 5 收尾
step "5 收尾：扩展还原 → 删号 → 删集群"
api POST "/admin/extensions/$EXT/disable"; say "  disable -> $HTTP_STATUS"
api DELETE "/admin/extensions/$EXT";      say "  DELETE  -> $HTTP_STATUS"
api POST "/admin/extensions/$EXT/uninstall-cleanup/finalize"; say "  finalize -> $HTTP_STATUS"

NOW_ACTIVE="$(pgv "SELECT coalesce(active_version_id::text,'') FROM extensions WHERE id='$EXT'")"
if [[ "$BEFORE_ACTIVE" == "" && "$NOW_ACTIVE" != "" ]]; then
  pgv "UPDATE extensions SET active_version_id=NULL WHERE id='$EXT' AND active_version_id=$NOW_ACTIVE" >/dev/null
  say "  还原 active_version_id：$NOW_ACTIVE -> NULL"
fi
FINAL_ACTIVE="$(pgv "SELECT coalesce(active_version_id::text,'NULL') FROM extensions WHERE id='$EXT'")"
FINAL_STATUS="$(pgv "SELECT status FROM extensions WHERE id='$EXT'")"
FINAL_MEM="$(pgv "SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
FINAL_MEM_IDS="$(pgv "SELECT coalesce(string_agg(publication_revision::text, ',' ORDER BY publication_revision),'') FROM plugin_runtime_publication_members WHERE extension_id='$EXT'")"
say "  扩展收尾：status=$FINAL_STATUS active=$FINAL_ACTIVE members=$FINAL_MEM [$FINAL_MEM_IDS]（append-only，不可清）"
[[ "$FINAL_STATUS" == "disabled" && "$FINAL_ACTIVE" == "NULL" ]] && ok '扩展回到测试前形态' || bad "扩展未还原：$FINAL_STATUS/$FINAL_ACTIVE"

for stmt in \
  "DELETE FROM user_roles WHERE user_id=$QA_UID" \
  "DELETE FROM user_sessions WHERE user_id=$QA_UID" \
  "DELETE FROM user_credentials WHERE user_id=$QA_UID" \
  "DELETE FROM users WHERE id=$QA_UID"; do
  out="$(pgv "$stmt")"; say "  $stmt -> ${out:-ok}"
done
left="$(pgv "SELECT (SELECT count(*) FROM users WHERE id=$QA_UID)::text||(SELECT count(*) FROM user_roles WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_sessions WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_credentials WHERE user_id=$QA_UID)::text")"
say "  账号残留 users/roles/sessions/credentials = $left"
[[ "$left" == "0000" ]] && ok '账号残留全 0' || bad '账号残留不为 0'
probe="$(pgv "SELECT (SELECT count(*) FROM badges WHERE slug LIKE 'qa-%')::text||(SELECT count(*) FROM badge_rule_backfill_runs)::text")"
say "  探针：qa-* badges / badge_rule_backfill_runs = $probe"
[[ "$probe" == "00" ]] && ok '探针数据全 0' || bad "探针数据残留 $probe"

if [[ -n "$CLUSTER" ]]; then
  docker rm -fv "$CLUSTER" >/dev/null 2>&1 && ok "已销毁集群 $CLUSTER" || bad "销毁集群失败"
  left_c="$(docker ps -a --format '{{.Names}}' | grep -c 'sforum-migration-verify' || true)"
  say "  剩余 sforum-migration-verify-* 容器数: $left_c"
  [[ "$left_c" == "0" ]] && ok '无遗留迁移集群' || bad "还有 $left_c 台遗留"
fi

say ''
say '  -- git 工作树（apps/api|apps/web）'
( cd "$ROOT" && git status --short | grep -E 'apps/(api|web)' | head -20 ) || say '    （空）'

say ''
say "SUMMARY fails=$FAILS"
[[ "$FAILS" -eq 0 ]] || exit 1
say '任务 ④ 完成'
