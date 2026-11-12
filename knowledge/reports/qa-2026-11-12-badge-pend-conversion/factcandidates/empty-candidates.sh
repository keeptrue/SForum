#!/usr/bin/env bash
# F-01 判据的「空候选」一侧：扩展未启用时
#   `factCandidates` 必须是 `[]`（不是 `null`、不是缺键），
#   且 `items` 里**不得**再出现 `fact.count`（展示面：没有候选的条件不出现，
#   见 `Conditions.PublishableDefinitions(candidates)`），items 条数回到 12。
#
# 顺序铁律：先验空候选，**最后**删账号（反了就 401，判据没被验到）。
set -uo pipefail

API="${API:-http://127.0.0.1:8080/api/v1}"
ORIGIN="${ORIGIN:-http://127.0.0.1:3000}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JAR="$DIR/session-empty.jar"
BODY="$DIR/.body-empty.out"
LOG="$DIR/empty-candidates.log"
EXT="sforum.query-surface-reference"

exec > >(tee "$LOG") 2>&1
FAILS=0
say() { printf '%s\n' "$*"; }
ok()  { printf '  ok   %s\n' "$*"; }
bad() { printf '  FAIL %s\n' "$*"; FAILS=$((FAILS + 1)); }

pgv() { docker exec -e PGPASSWORD=sforum "${PG_CID:-sforum-postgres}" psql -U sforum -d sforum -v ON_ERROR_STOP=1 -tAq -c "$1" 2>&1; }
cookie_value() { [[ -f "$JAR" ]] && awk -v n="$1" '$6 == n { printf "%s", $7 }' "$JAR" 2>/dev/null; return 0; }
HTTP_STATUS=000
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

say '== 0 前置：扩展必须处于「不在管理器列表」的墓碑态'
ext_state="$(pgv "SELECT status||'|active='||coalesce(active_version_id::text,'NULL') FROM extensions WHERE id='$EXT'")"
say "  库内状态: $ext_state"
[[ "$ext_state" == 'disabled|active=NULL' ]] && ok '墓碑态（disabled + active_version_id NULL）' \
  || bad "状态不是墓碑态：$ext_state（若上一轮没跑完，先恢复）"
# factCandidates 有 5s 快照 TTL，disable/uninstall 之后必须等它过期，否则读到旧快照。
say '  等 7s 让候选快照 TTL（5s）过期'
sleep 7

say ''
say '== 1 建一次性超管账号（只为一个 GET）'
TS="$(date +%s)"
QA_USER="qapb$TS"
QA_PASS="${QA_PASS:?set QA_PASS to a throwaway password (not persisted in this repo)}"
: >"$JAR"
api GET /auth/session >/dev/null
api POST /auth/register "$(jq -nc --arg u "$QA_USER" --arg e "$QA_USER@qa.pend.local" \
  --arg p "$QA_PASS" '{username:$u,email:$e,password:$p,displayName:"QA Pend BE",locale:"zh-CN"}')"
[[ "$HTTP_STATUS" == "200" || "$HTTP_STATUS" == "201" ]] || { say "[exit 2] 注册失败 $HTTP_STATUS $(head -c 300 "$BODY")"; exit 2; }
QA_UID="$(pgv "SELECT id FROM users WHERE username='$QA_USER'")"
[[ "$QA_UID" =~ ^[0-9]+$ ]] || { say "[exit 2] 读不到 uid：$QA_UID"; exit 2; }
pgv "INSERT INTO user_roles (user_id, role_id) SELECT $QA_UID, id FROM roles WHERE key='super_admin'" >/dev/null
ok "uid=$QA_UID 已授予 super_admin"

api GET /auth/session >/dev/null
api POST /auth/login "$(jq -nc --arg l "$QA_USER" --arg p "$QA_PASS" '{login:$l,password:$p}')"
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] 登录失败 $HTTP_STATUS $(head -c 300 "$BODY")"; exit 2; }
ok '登录成功'

say ''
say '== 2 GET /admin/badges/conditions（扩展未启用）'
api GET /admin/badges/conditions
[[ "$HTTP_STATUS" == "200" ]] || bad "conditions HTTP $HTTP_STATUS $(head -c 300 "$BODY")"
cp "$BODY" "$DIR/conditions-disabled.json"
say "  响应已存 conditions-disabled.json（$(wc -c <"$DIR/conditions-disabled.json") 字节）"

say ''
say '== 3 空候选值级断言'
node "$DIR/assert-empty-candidates.mjs" "$DIR/conditions-disabled.json"
[[ $? -eq 0 ]] && ok '空候选形态全绿' || bad '空候选形态有红'

say ''
say '== 4 最后删账号'
for stmt in \
  "DELETE FROM user_roles WHERE user_id=$QA_UID" \
  "DELETE FROM user_sessions WHERE user_id=$QA_UID" \
  "DELETE FROM user_credentials WHERE user_id=$QA_UID" \
  "DELETE FROM users WHERE id=$QA_UID"; do
  out="$(pgv "$stmt")"; say "  $stmt -> ${out:-ok}"
done
left="$(pgv "SELECT (SELECT count(*) FROM users WHERE id=$QA_UID)::text||(SELECT count(*) FROM user_roles WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_sessions WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_credentials WHERE user_id=$QA_UID)::text")"
say "  复核 users/user_roles/user_sessions/user_credentials = $left"
[[ "$left" == "0000" ]] && ok '账号残留计数全 0' || bad '账号残留不为 0'

say ''
say "SUMMARY fails=$FAILS"
[[ "$FAILS" -eq 0 ]] || exit 1
say '空候选判据 ok'
