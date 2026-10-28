#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2034
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-upgrade-health-test.XXXXXX")"
LOG_FILE="$TEMP_DIR/compose.log"
FAILURES_FILE="$TEMP_DIR/probe-failures"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'upgrade-health-probe_test.sh: %s\n' "$1" >&2
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

source "$ROOT_DIR/upgrade.sh"

DEPLOY_RC="$TEMP_DIR/.deployrc"
printf 'lang=zh\n' > "$DEPLOY_RC"
COMPOSE=(fake_compose)

# 探针行为开关。失败轮数必须落在文件里：probe_slot_endpoint 在命令替换的
# 子 shell 中调用 fake_compose，进程内的变量递减不会回到父 shell。
PROBE_DIAG="wget: can't connect to remote host (127.0.0.1): Connection refused"
EXEC_FAILURE=false

fake_compose() {
  local remaining
  printf 'compose %s\n' "$*" >> "$LOG_FILE"
  case " $* " in
    *' exec -T '*)
      if [ "$EXEC_FAILURE" = true ]; then
        printf 'service "api-blue" is not running\n' >&2
        return 1
      fi
      remaining="$(cat "$FAILURES_FILE" 2>/dev/null || printf '0')"
      if [ "$remaining" -gt 0 ]; then
        printf '%s\n' "$((remaining - 1))" > "$FAILURES_FILE"
        printf '%s\n' "$PROBE_DIAG" >&2
        return 1
      fi
      return 0
      ;;
    *' logs '*) printf 'level=error msg="plugin runtime genesis failed"\n' ;;
    *' ps '*) printf 'api-blue  running\n' ;;
  esac
}

reset_probe() {
  : > "$LOG_FILE"
  printf '0\n' > "$FAILURES_FILE"
  EXEC_FAILURE=false
}

# 1) 快路径：首次探测通过时不写任何输出，也不该把 stderr 弄脏。
reset_probe
if ! ( wait_inside_slot blue ) > "$TEMP_DIR/fast.out" 2> "$TEMP_DIR/fast.err"; then
  fail "a slot that was ready immediately was reported unhealthy"
fi
if [ -s "$TEMP_DIR/fast.err" ]; then
  fail "a slot that was ready immediately wrote to stderr"
fi
if [ -s "$TEMP_DIR/fast.out" ]; then
  fail "a slot that was ready immediately printed progress output"
fi

# 2) 瞬时失败：重试期间的 wget 诊断不得出现在终端，只留一行「已就绪」。
reset_probe
printf '3\n' > "$FAILURES_FILE"
if ! ( wait_inside_slot blue ) > "$TEMP_DIR/retry.out" 2> "$TEMP_DIR/retry.err"; then
  fail "transient probe failures were not retried to success"
fi
if [ -s "$TEMP_DIR/retry.err" ]; then
  fail "transient probe failures leaked to stderr"
fi
assert_contains "$TEMP_DIR/retry.out" "已就绪"
assert_not_contains "$TEMP_DIR/retry.out" "wget:"

# 3) 超时：给出失败探针名、原始诊断，并 dump 容器状态与日志。
reset_probe
printf '999\n' > "$FAILURES_FILE"
if ( export SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS=1; wait_inside_slot blue ) \
  > "$TEMP_DIR/timeout.out" 2> "$TEMP_DIR/timeout.err"; then
  fail "probe timeout was accepted"
fi
assert_contains "$TEMP_DIR/timeout.err" "健康检查超时"
assert_contains "$TEMP_DIR/timeout.err" "Connection refused"
assert_contains "$TEMP_DIR/timeout.err" "api-blue"
assert_contains "$LOG_FILE" 'logs --tail=60 --no-color api-blue'

# 4) 容器根本没在跑：compose 的报错要原样带出来，不能只报「超时」。
reset_probe
EXEC_FAILURE=true
if ( export SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS=1; wait_inside_slot blue ) \
  > "$TEMP_DIR/notrunning.out" 2> "$TEMP_DIR/notrunning.err"; then
  fail "a non-running slot was accepted"
fi
assert_contains "$TEMP_DIR/notrunning.err" "is not running"
assert_contains "$LOG_FILE" 'logs --tail=60 --no-color api-blue'

# 5) 慢启动：等待超过 15 秒必须有心跳，否则操作者看到的是「卡死」。
reset_probe
printf '9\n' > "$FAILURES_FILE"
if ! ( export SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS=40; wait_inside_slot blue ) \
  > "$TEMP_DIR/heartbeat.out" 2> "$TEMP_DIR/heartbeat.err"; then
  fail "a slow but healthy slot was reported unhealthy"
fi
assert_contains "$TEMP_DIR/heartbeat.err" "仍在启动"
assert_contains "$TEMP_DIR/heartbeat.out" "已就绪"
assert_not_contains "$TEMP_DIR/heartbeat.err" "wget: can't connect"

printf 'upgrade-health-probe_test.sh: all checks passed\n'
