#!/usr/bin/env bash
# 共享部署锁的行为测试：deploy.sh 与 upgrade.sh 都 source deploy/scripts/deploy-lock.sh，
# 这里直接对这份实现断言「怎么判活、怎么判残留、怎么回收」，不经过任一入口脚本。
# 关键回归：
#   - kill -0 对别人家的进程返回 EPERM，必须用 ps -p 兜底判活（否则活锁被当死锁清掉）；
#   - pid 被复用（进程活着但启动时刻变了）必须按残留锁回收，否则会一直拒到 1 小时宽限。
# shellcheck disable=SC1091
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-deploy-lock-test.XXXXXX")"
LOCK_ROOT="$TEMP_DIR/root"
OUT_FILE="$TEMP_DIR/stdout"
ERR_FILE="$TEMP_DIR/stderr"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'deploy-lock_test.sh: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  grep -Fq -- "$2" "$1" || fail "missing expected text: $2"
}

assert_not_contains() {
  if grep -Fq -- "$2" "$1"; then
    fail "found unexpected text: $2"
  fi
}

mkdir -p "$LOCK_ROOT"

# 调用方契约：die() 与 lock_text()。这里用最短文本，方便断言走到了哪条分支。
die() {
  printf '%s\n' "$1" >&2
  exit 1
}
lock_text() {
  case "$1" in
    deploy_locked) printf 'locked:' ;;
    lock_recovered) printf 'recovered:' ;;
    unlock_none) printf 'none' ;;
    unlock_done) printf 'unlocked:' ;;
    *) printf '%s' "$1" ;;
  esac
}

DEPLOY_LOCK_ROOT="$LOCK_ROOT"
# shellcheck source=deploy/scripts/deploy-lock.sh
source "$ROOT_DIR/deploy/scripts/deploy-lock.sh"

reset_lock() {
  rm -rf "$DEPLOY_LOCK_DIR" "$DEPLOY_LOCK_DIR".stale.* "$DEPLOY_LOCK_LOG"
  : > "$OUT_FILE"
  : > "$ERR_FILE"
}

write_lock() {
  local pid="$1" proc_start="$2"
  mkdir -p "$DEPLOY_LOCK_DIR"
  printf '%s\n' "$pid" > "$DEPLOY_LOCK_PID"
  printf 'pid=%s\naction=deploy\nstarted_at=2026-01-01T00:00:00Z\nproc_start=%s\n' \
    "$pid" "$proc_start" > "$DEPLOY_LOCK_OWNER"
}

# 1) 空目录：拿到锁，owner 写全 pid/action/started_at/proc_start；释放后不留残骸。
reset_lock
acquire_deploy_lock backup
[ -f "$DEPLOY_LOCK_OWNER" ] || fail "acquiring the lock did not write owner metadata"
[ "$(cat "$DEPLOY_LOCK_PID")" = "$$" ] || fail "the lock pid file does not record this process"
grep -qx 'action=backup' "$DEPLOY_LOCK_OWNER" || fail "the lock action was not recorded"
grep -qx "pid=$$" "$DEPLOY_LOCK_OWNER" || fail "the owner pid was not recorded"
grep -q '^started_at=' "$DEPLOY_LOCK_OWNER" || fail "the lock start time was not recorded"
grep -q '^proc_start=' "$DEPLOY_LOCK_OWNER" || fail "the process start time was not recorded"
release_deploy_lock
[ ! -e "$DEPLOY_LOCK_DIR" ] || fail "releasing the lock left the directory behind"

# 2) 本机活锁：持有者活着且启动时刻一致 → 拒绝，并点名持有者。
reset_lock
write_lock "$$" "$(lock_proc_start "$$")"
if ( acquire_deploy_lock deploy ) >"$OUT_FILE" 2>"$ERR_FILE"; then
  fail "a live lock was acquired twice"
fi
assert_contains "$ERR_FILE" 'locked:'
assert_contains "$ERR_FILE" 'state=alive'
[ -e "$DEPLOY_LOCK_OWNER" ] || fail "the live lock was removed"

# 3) 跨用户活锁：kill -0 返回 EPERM（不是「进程不存在」），必须靠 ps -p 判活。
reset_lock
if kill -0 1 2>/dev/null; then
  # 以 root 运行：kill 本身就成功，EPERM 分支不存在，但锁仍必须判活。
  lock_pid_alive 1 || fail "pid 1 must be alive"
else
  ps -p 1 >/dev/null 2>&1 || fail "ps -p cannot observe pid 1; the fallback assertion is meaningless"
  lock_pid_alive 1 || fail "a live foreign pid was reported dead when kill -0 returned EPERM"
fi
write_lock 1 "$(lock_proc_start 1)"
if ( acquire_deploy_lock deploy ) >"$OUT_FILE" 2>"$ERR_FILE"; then
  fail "a live lock owned by another user was reclaimed"
fi
assert_contains "$ERR_FILE" 'state=alive'
assert_not_contains "$ERR_FILE" 'recovered:'
[ -e "$DEPLOY_LOCK_OWNER" ] || fail "the foreign live lock was removed"

# 4) pid 已消失：按残留锁回收，日志写明 reason=pid_gone，随后正常拿到锁。
reset_lock
write_lock 999999 'Thu Jan  1 00:00:00 2026'
acquire_deploy_lock deploy >"$OUT_FILE" 2>"$ERR_FILE"
assert_contains "$ERR_FILE" 'recovered:'
assert_contains "$ERR_FILE" 'reason=pid_gone'
assert_contains "$DEPLOY_LOCK_LOG" 'reason=pid_gone'
[ "$(cat "$DEPLOY_LOCK_PID")" = "$$" ] || fail "the reclaimed lock was not re-acquired"
release_deploy_lock

# 5) pid 被复用：进程活着但启动时刻不同 → 同样回收，reason=pid_reused。
reset_lock
write_lock 1 'Thu Jan  1 00:00:00 2026'
acquire_deploy_lock deploy >"$OUT_FILE" 2>"$ERR_FILE"
assert_contains "$ERR_FILE" 'recovered:'
assert_contains "$ERR_FILE" 'reason=pid_reused'
assert_contains "$DEPLOY_LOCK_LOG" 'reason=pid_reused'
release_deploy_lock

# 6) 元数据缺失且锁龄超过 1 小时：回收，reason=no_owner。
reset_lock
mkdir -p "$DEPLOY_LOCK_DIR"
touch -t 202001010000 "$DEPLOY_LOCK_DIR"
acquire_deploy_lock deploy >"$OUT_FILE" 2>"$ERR_FILE"
assert_contains "$ERR_FILE" 'reason=no_owner'
assert_contains "$DEPLOY_LOCK_LOG" 'reason=no_owner'
release_deploy_lock

# 7) 元数据缺失但锁很新：视为活锁拒绝，state=unknown（不猜持有者）。
reset_lock
mkdir -p "$DEPLOY_LOCK_DIR"
if ( acquire_deploy_lock deploy ) >"$OUT_FILE" 2>"$ERR_FILE"; then
  fail "a fresh lock without metadata was reclaimed"
fi
assert_contains "$ERR_FILE" 'locked:'
assert_contains "$ERR_FILE" 'state=unknown'
[ -d "$DEPLOY_LOCK_DIR" ] || fail "the fresh unknown lock was removed"

# 8) 只有 pid 文件、没有 owner：仍要能读出持有者（两个入口共用的旧格式）。
reset_lock
mkdir -p "$DEPLOY_LOCK_DIR"
printf '424242\n' > "$DEPLOY_LOCK_PID"
[ "$(lock_holder_pid)" = 424242 ] || fail "the pid-only lock format is no longer readable"

# 9) 人工兜底：force_unlock 直接清掉并记录清了谁。
reset_lock
write_lock 424242 'Thu Jan  1 00:00:00 2026'
force_unlock_deploy_lock >"$OUT_FILE" 2>"$ERR_FILE"
assert_contains "$ERR_FILE" 'unlocked:'
assert_contains "$ERR_FILE" 'pid=424242'
[ ! -e "$DEPLOY_LOCK_DIR" ] || fail "force unlock left the lock behind"
assert_contains "$DEPLOY_LOCK_LOG" 'pid=424242'

# 10) 空目录时 force_unlock 只说「没有锁」，不制造日志噪音。
reset_lock
force_unlock_deploy_lock >"$OUT_FILE" 2>"$ERR_FILE"
assert_contains "$OUT_FILE" 'none'
[ ! -e "$DEPLOY_LOCK_LOG" ] || fail "force unlock wrote an event when no lock existed"

printf 'deploy-lock_test.sh: all checks passed\n'
