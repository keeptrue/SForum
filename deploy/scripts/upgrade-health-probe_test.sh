#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2030,SC2031,SC2034
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
      # 恢复态：/ready 答 5xx，body 里带 recovery 块。探针必须据此立刻停手。
      if [ "$PROBE_RECOVERY" = true ]; then
        printf '%s\n' '{"status":"not_ready","ready":false,"components":[],"recovery":{"code":"plugin_runtime_convergence_failed","component":"extensionRuntime","message":"plugin runtime genesis failed"}}'
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
  PROBE_RECOVERY=false
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

# 6) 恢复态：探针拿到 recovery 块 → 立刻返回 3，不再烧超时预算。
# 这一步必须在当前 shell 里跑（不加子 shell），否则 SLOT_HEALTH_RECOVERY_REASON
# 随子 shell 消失，也就测不到「原因有没有被传出去」。
reset_probe
PROBE_RECOVERY=true
status=0
wait_inside_slot blue > "$TEMP_DIR/recovery.out" 2> "$TEMP_DIR/recovery.err" || status=$?
PROBE_RECOVERY=false
[[ "$status" == "3" ]] || fail "recovery-only was not reported as a distinct terminal state (got $status)"
[[ "$SLOT_HEALTH_RECOVERY_REASON" == "plugin_runtime_convergence_failed" ]] || \
  fail "recovery code was not extracted from the probe body: '$SLOT_HEALTH_RECOVERY_REASON'"
recovery_probes="$(grep -c 'exec -T api-blue' "$LOG_FILE")"
[[ "$recovery_probes" == "1" ]] || \
  fail "recovery-only kept probing after the first answer (saw $recovery_probes probes)"

# 7) 恢复态的失败说明：点名 code 与人工动作，而不是笼统的「健康检查失败」。
status=0
( slot_health_failure_die "fallback text" ) > "$TEMP_DIR/die.out" 2> "$TEMP_DIR/die.err" || status=$?
[[ "$status" == "1" ]] || fail "slot_health_failure_die did not exit non-zero"
assert_contains "$TEMP_DIR/die.err" "恢复态"
assert_contains "$TEMP_DIR/die.err" "sforum extension recovery recheck"
assert_not_contains "$TEMP_DIR/die.err" "fallback text"

# 8) 宿主侧 wait-for-health.sh：三态契约必须与槽位探针同一判据。
health_script="$ROOT_DIR/deploy/scripts/wait-for-health.sh"
mkdir -p "$TEMP_DIR/bin"
cat > "$TEMP_DIR/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w|--max-time) shift 2 ;;
    -sS|-s|-f) shift ;;
    *) shift ;;
  esac
done
printf '%s' "${CURL_BODY:-}" > "$out"
printf '%s' "${CURL_STATUS:-200}"
exit 0
CURL
chmod +x "$TEMP_DIR/bin/curl"

status=0
( export PATH="$TEMP_DIR/bin:$PATH" CURL_STATUS=200 CURL_BODY='{"status":"ready","ready":true}'; \
  "$health_script" http://127.0.0.1:18080/api/v1/ready 5 ) >/dev/null 2>&1 || status=$?
[[ "$status" == "0" ]] || fail "a 200 readiness response was not accepted (got $status)"

status=0
( export PATH="$TEMP_DIR/bin:$PATH" CURL_STATUS=503 \
    CURL_BODY='{"status":"not_ready","ready":false,"components":[],"recovery":{"code":"plugin_runtime_convergence_failed","component":"extensionRuntime","message":"x"}}'; \
  "$health_script" http://127.0.0.1:18080/api/v1/ready 5 ) \
  > "$TEMP_DIR/health-recovery.out" 2> "$TEMP_DIR/health-recovery.err" || status=$?
[[ "$status" == "3" ]] || fail "recovery-only readiness did not exit 3 (got $status)"
assert_contains "$TEMP_DIR/health-recovery.err" "recovery recheck"

# 依赖未就绪也答 503，但没有 recovery 块 —— 那是瞬态，必须继续等到超时。
status=0
( export PATH="$TEMP_DIR/bin:$PATH" CURL_STATUS=503 CURL_BODY='{"status":"not_ready","ready":false,"components":[]}'; \
  "$health_script" http://127.0.0.1:18080/api/v1/ready 1 ) >/dev/null 2>&1 || status=$?
[[ "$status" == "1" ]] || fail "a dependency-not-ready 503 stopped waiting (got $status)"

# 负例：/health 的 "recoveryRequired" 字段不是 recovery 块，不能被当成恢复态。
status=0
( export PATH="$TEMP_DIR/bin:$PATH" CURL_STATUS=503 CURL_BODY='{"status":"ok","recoveryRequired":false}'; \
  "$health_script" http://127.0.0.1:18080/api/v1/ready 1 ) >/dev/null 2>&1 || status=$?
[[ "$status" == "1" ]] || \
  fail "the /health recoveryRequired field was mistaken for a recovery block (got $status)"

# --- 后补键（P3-18）：upgrade.sh 的 backfill_env_keys 与 deploy.sh 等价 ----------
# 存量 .env.production 缺 MEILI_MASTER_KEY（或写成空值），而 compose.prod.yaml 已加
# `:?` 硬闸：升级路径必须在第一个 compose 调用之前补上，且幂等、不覆盖已有值。
file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

BACKFILL_DIR="$TEMP_DIR/env"
mkdir -p "$BACKFILL_DIR"
BACKFILL_ENV="$BACKFILL_DIR/.env.production"
printf 'APP_ENV=production\nMEILI_MASTER_KEY=\nOTHER=1\n' > "$BACKFILL_ENV"
chmod 600 "$BACKFILL_ENV"

( backfill_env_keys "$BACKFILL_ENV" ) >/dev/null 2>&1 || fail "backfill_env_keys failed on an empty key"
[[ "$(grep -c '^MEILI_MASTER_KEY=' "$BACKFILL_ENV" || true)" == "1" ]] || \
  fail "backfill left $(grep -c '^MEILI_MASTER_KEY=' "$BACKFILL_ENV" || true) MEILI_MASTER_KEY line(s)"
backfill_value="$(sed -n 's/^MEILI_MASTER_KEY=//p' "$BACKFILL_ENV")"
[[ "$backfill_value" =~ ^[0-9a-f]{64}$ ]] || fail "backfill did not write a 32-byte hex value"
grep -qx 'OTHER=1' "$BACKFILL_ENV" || fail "backfill dropped an unrelated key"
[[ "$(file_mode "$BACKFILL_ENV")" == "600" ]] || fail "backfill changed the config file mode"

# 幂等：第二次调用不改值、不重复追加。
( backfill_env_keys "$BACKFILL_ENV" ) >/dev/null 2>&1 || fail "second backfill call failed"
[[ "$(sed -n 's/^MEILI_MASTER_KEY=//p' "$BACKFILL_ENV")" == "$backfill_value" ]] || \
  fail "second backfill call overwrote an existing value"
[[ "$(grep -c '^MEILI_MASTER_KEY=' "$BACKFILL_ENV" || true)" == "1" ]] || \
  fail "second backfill call duplicated the key"

# 已有非空值：一个字节都不动（连临时文件都不该产生）。
KEEP_ENV="$BACKFILL_DIR/keep.env"
printf 'MEILI_MASTER_KEY=keep-me\n' > "$KEEP_ENV"
chmod 600 "$KEEP_ENV"
keep_before="$(cksum < "$KEEP_ENV")"
( backfill_env_keys "$KEEP_ENV" ) >/dev/null 2>&1
[[ "$(cksum < "$KEEP_ENV")" == "$keep_before" ]] || fail "backfill rewrote a file that already had a value"

# 配置文件不存在：什么都不做，绝不凭空创建。
( backfill_env_keys "$BACKFILL_DIR/absent.env" ) >/dev/null 2>&1
[ ! -e "$BACKFILL_DIR/absent.env" ] || fail "backfill created a config file that did not exist"

printf 'upgrade-health-probe_test.sh: all checks passed\n'
