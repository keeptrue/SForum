#!/usr/bin/env bash
# B：backfill 最强底 —— `--mutate` + DATABASE_URL（一次性集群）+ 管理员会话。
# 顺序：建会话 → 跑脚本 → 删会话/账号 → 销毁集群。
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API="${API:-http://127.0.0.1:8080/api/v1}"
ORIGIN="${ORIGIN:-http://127.0.0.1:3000}"
CLUSTER="${CLUSTER:?need CLUSTER}"
JAR="$DIR/session-b.jar"; BODY="$DIR/.body-b.out"
LOG="$DIR/run-backfill-mutate.log"
HTTP_STATUS=000; FAILS=0
: >"$LOG"; exec > >(tee -a "$LOG") 2>&1
say(){ printf '%s\n' "$*"; }
ok(){ printf '  ok   %s\n' "$*"; }
bad(){ printf '  FAIL %s\n' "$*"; FAILS=$((FAILS+1)); }
pgv(){ docker exec -e PGPASSWORD=sforum "${PG_CID:-sforum-postgres}" psql -U sforum -d sforum -v ON_ERROR_STOP=1 -tAq -c "$1" 2>&1; }
cookie_value(){ [[ -f "$JAR" ]] && awk -v n="$1" '$6 == n { printf "%s", $7 }' "$JAR" 2>/dev/null; return 0; }
cookie_header(){ awk 'NF >= 7 && ($1 ~ /^#HttpOnly_/ || $1 !~ /^#/) { n2=$6; v=$7; if (v=="") next; printf "%s%s=%s", (n++ ? "; " : ""), n2, v }' "$JAR" 2>/dev/null; }
api(){ local m=$1 p=$2 b=${3:-}; local -a a=(-sS -o "$BODY" -w '%{http_code}' -X "$m" -b "$JAR" -c "$JAR" -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/"); local t; t="$(cookie_value csrf_)"; [[ -n "$t" ]] && a+=(-H "X-CSRF-Token: $t"); [[ -n "$b" ]] && a+=(-H 'Content-Type: application/json' --data-binary "$b"); : >"$BODY"; HTTP_STATUS="$(curl "${a[@]}" --max-time 25 "$API$p" 2>/dev/null)" || HTTP_STATUS=000; }

say "== 0 前置 $(date '+%F %T %z')"
port="$(docker port "$CLUSTER" 5432/tcp | head -1 | sed 's/.*://')"
DBURL="postgres://sforum:sforum@127.0.0.1:${port}/sforum_migration_verify?sslmode=disable"
say "  集群 $CLUSTER -> $DBURL"
tbl="$(docker exec -e PGPASSWORD=sforum "$CLUSTER" psql -U sforum -d sforum_migration_verify -tAq -c "SELECT to_regclass('public.badge_rule_backfill_runs')" 2>&1)"
say "  集群内表: $tbl"
[[ "$tbl" == "badge_rule_backfill_runs" ]] && ok '集群已到最新迁移链' || bad "集群状态异常：$tbl"
guard="$(docker exec -e PGPASSWORD=sforum "$CLUSTER" psql -U sforum -d sforum_migration_verify -tAq -c "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL" 2>&1)"
[[ "$guard" == "f" ]] && ok '无 dev 哨兵（不是开发库）' || bad "带 dev 哨兵，拒绝"

say ''
say '== 1 一次性超管会话'
TS="$(date +%s)"; QA_USER="qapb$TS"; QA_PASS="${QA_PASS:?set QA_PASS to a throwaway password (not persisted in this repo)}"
: >"$JAR"
api GET /auth/session >/dev/null
api POST /auth/register "$(jq -nc --arg u "$QA_USER" --arg e "$QA_USER@qa.pend.local" --arg p "$QA_PASS" '{username:$u,email:$e,password:$p,displayName:"QA Pend BE",locale:"zh-CN"}')"
[[ "$HTTP_STATUS" == "200" || "$HTTP_STATUS" == "201" ]] || { say "[exit 2] 注册失败 $HTTP_STATUS"; exit 2; }
QA_UID="$(pgv "SELECT id FROM users WHERE username='$QA_USER'")"
pgv "INSERT INTO user_roles (user_id, role_id) SELECT $QA_UID, id FROM roles WHERE key='super_admin'" >/dev/null
api GET /auth/session >/dev/null
api POST /auth/login "$(jq -nc --arg l "$QA_USER" --arg p "$QA_PASS" '{login:$l,password:$p}')"
[[ "$HTTP_STATUS" == "200" ]] || { say "[exit 2] 登录失败 $HTTP_STATUS"; exit 2; }
COOKIE="$(cookie_header)"
ok "uid=$QA_UID 已登录，cookie 串 ${#COOKIE} 字符（含 sforum_session）"
case "$COOKIE" in *sforum_session=*) ok 'cookie 含 sforum_session';; *) bad 'cookie 缺 sforum_session';; esac

say ''
say '== 2 backfill --mutate（DB + 会话）'
bflog="$DIR/backfill-db-session-mutate.log"
( cd "$ROOT" && DATABASE_URL="$DBURL" SFORUM_SESSION_COOKIE="$COOKIE" ./scripts/verify-badge-backfill-progress.sh --mutate ) >"$bflog" 2>&1
rc=$?
printf '\nEXIT=%s\n' "$rc" >>"$bflog"
say "  exit=$rc  日志 $(wc -l <"$bflog") 行 / $(wc -c <"$bflog") 字节"
sed -n '/== 汇总/,$p' "$bflog" | head -12
[[ $rc -eq 0 ]] && ok 'backfill --mutate exit 0' || bad "backfill --mutate exit $rc"
say '  -- A-01/B-04 原文'
grep -nE 'A-01|B-04' "$bflog" | head -8

say ''
say '== 3 收尾'
for s in "DELETE FROM user_roles WHERE user_id=$QA_UID" "DELETE FROM user_sessions WHERE user_id=$QA_UID" "DELETE FROM user_credentials WHERE user_id=$QA_UID" "DELETE FROM users WHERE id=$QA_UID"; do
  out="$(pgv "$s")"; say "  $s -> ${out:-ok}"
done
left="$(pgv "SELECT (SELECT count(*) FROM users WHERE id=$QA_UID)::text||(SELECT count(*) FROM user_roles WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_sessions WHERE user_id=$QA_UID)::text||(SELECT count(*) FROM user_credentials WHERE user_id=$QA_UID)::text")"
say "  账号残留 = $left"; [[ "$left" == "0000" ]] && ok '账号残留全 0' || bad '账号残留不为 0'
probe="$(pgv "SELECT (SELECT count(*) FROM badges WHERE slug LIKE 'qa-%')::text||(SELECT count(*) FROM badge_rule_backfill_runs)::text")"
say "  探针 qa-* badges / runs = $probe"; [[ "$probe" == "00" ]] && ok '探针全 0' || bad "探针残留 $probe"
docker rm -fv "$CLUSTER" >/dev/null 2>&1 && ok "集群已销毁 $CLUSTER" || bad '销毁失败'
n="$(docker ps -a --format '{{.Names}}' | grep -c 'sforum-migration-verify' || true)"
say "  剩余 sforum-migration-verify-* = $n"; [[ "$n" == "0" ]] && ok '无遗留集群' || bad "遗留 $n 台"

say ''
say "SUMMARY fails=$FAILS"
[[ "$FAILS" -eq 0 ]] || exit 1
