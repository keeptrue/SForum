#!/usr/bin/env bash
# 仓库级全量门禁：Go、Web、静态三条线并发跑，任一线失败整体非零退出。
# 三条线互不依赖（Go 吃数据库/IO，Web 吃 CPU/内存，静态段是纯静态检查），
# 所以并发的收益是直接的；每条线的日志落在 tests/.logs/。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

FAST=0
for arg in "$@"; do
  case "$arg" in
    --fast) FAST=1 ;;
    *)
      echo "unknown argument: $arg (supported: --fast)" >&2
      exit 2
      ;;
  esac
done

LOG_DIR="$ROOT_DIR/tests/.logs"
mkdir -p "$LOG_DIR"

run_api_line() {
  echo "Running Go tests on a disposable cluster..."
  if [[ $FAST == 1 ]]; then
    ./scripts/test-api-isolated.sh
  else
    ./scripts/test-api-isolated.sh --with-compat
  fi
  echo "Building Go commands..."
  (cd apps/api && go build ./...)
}

run_web_line() {
  if [[ ! -d apps/web/node_modules ]]; then
    echo "apps/web/node_modules is required; run bun install --frozen-lockfile first." >&2
    return 1
  fi
  echo "Running Nuxt typecheck..."
  (cd apps/web && bun run typecheck)
  echo "Running full web unit tests..."
  (cd apps/web && bun test)
  if [[ $FAST == 0 ]]; then
    echo "Building production web app..."
    (cd apps/web && bun run build)
  fi
}

run_static_line() {
  if [[ $FAST == 0 ]]; then
    ./scripts/ci/release-tooling-gate.sh
  fi
  ./scripts/ci/contracts-gate.sh
  # Explicit marker: tests/validate-host-api-v2-docs.mjs asserts the repository
  # test gate still runs the Host API v2 documentation validation (executed
  # inside contracts-gate.sh).
  # node tests/validate-host-api-v2-docs.mjs
  node tests/validate-runtime-tzdata.mjs
  # bun audit 棘轮门的逻辑自检：8 个场景（无新增绿 / 新增红 / 基线失效红 /
  # 严重度升级红 / 严重度降级红 / 未播种两态 / 空报告红 / --refresh 播种）
  # 全部用内联夹具跑，不联网。真正的扫描与基线比对在 Security workflow 的
  # web-audit 里。
  node scripts/bun-audit-gate.mjs --self-test
}

LINES=(api web static)
declare -A PID_OF=()
declare -A EXIT_OF=()
declare -A ELAPSED_OF=()

# 子进程是 subshell，kill 它不会传到 go test / nuxt；必须逐层向下发信号，
# 否则 Ctrl-C 会留下容器和构建进程。
kill_tree() {
  local pid="$1" kid
  for kid in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_tree "$kid"
  done
  kill "$pid" 2>/dev/null || true
}

cleanup_lines() {
  local line
  for line in "${LINES[@]}"; do
    if [[ -n "${PID_OF[$line]:-}" ]]; then
      kill_tree "${PID_OF[$line]}"
    fi
  done
}

trap cleanup_lines INT TERM

if [[ $FAST == 1 ]]; then
  echo "Fast mode: skipping the production web build, the compatibility farm, and the release/deploy tooling gate (CI covers those in full)."
fi
echo "Running three gate lines in parallel; per-line logs in tests/.logs/."

# 计时写进 EXIT trap，而不是 `cmd || status=$?`：后者会让 bash 在整个函数体内
# 关闭 errexit，中间步骤失败会被最后一步的成功掩盖（实测漏报过一次 FAIL）。
write_elapsed() {
  echo "$((SECONDS - started))" >"$LOG_DIR/${line}.elapsed"
}

for line in "${LINES[@]}"; do
  # 先删掉上一轮的计时文件：某条线若在写计时前就被 SIGKILL，不能读到旧值。
  rm -f "$LOG_DIR/${line}.elapsed"
  (
    started=$SECONDS
    trap write_elapsed EXIT
    "run_${line}_line"
  ) >"$LOG_DIR/${line}.log" 2>&1 &
  PID_OF[$line]=$!
done

failed=()
for line in "${LINES[@]}"; do
  status=0
  wait "${PID_OF[$line]}" || status=$?
  EXIT_OF[$line]=$status
  ELAPSED_OF[$line]="$(cat "$LOG_DIR/${line}.elapsed" 2>/dev/null || echo '?')"
  if [[ $status != 0 ]]; then
    failed+=("$line")
  fi
done
trap - INT TERM

echo
echo "=========================== gate summary ==========================="
for line in "${LINES[@]}"; do
  if [[ ${EXIT_OF[$line]} == 0 ]]; then
    printf 'PASS  %-6s %4ss  tests/.logs/%s.log\n' "$line" "${ELAPSED_OF[$line]}" "$line"
  else
    printf 'FAIL  %-6s %4ss  exit=%s  tests/.logs/%s.log\n' "$line" "${ELAPSED_OF[$line]}" "${EXIT_OF[$line]}" "$line"
  fi
done

if [[ ${#failed[@]} -gt 0 ]]; then
  for line in "${failed[@]}"; do
    echo
    echo "----- tail of tests/.logs/${line}.log -----"
    tail -n 40 "$LOG_DIR/${line}.log"
  done
  echo
  echo "Gate failed: ${failed[*]}" >&2
  exit 1
fi

echo "All gates passed."
