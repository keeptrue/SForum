#!/usr/bin/env bash
# Contracts/static CI gate: proto drift, OpenAPI refs, architecture and
# documentation validation. No database required.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-contracts-gate.XXXXXX")"
ACTIVE_PIDS=()
cleanup_jobs() {
  local status=$?
  trap - EXIT INT TERM
  local pid
  for pid in "${ACTIVE_PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  rm -rf "$TMP_DIR"
  exit "$status"
}
trap cleanup_jobs EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Running Protobuf lint and generated SDK drift validation..."
./scripts/proto.sh check

echo "Running Host API v2 SDK documentation drift validation..."
node tests/validate-host-api-v2-docs.mjs

echo "Running OpenAPI reference validation..."
ruby scripts/validate-openapi-refs.rb

echo "Running contract status-code parity validation and capturing its read-only output snapshot..."
PARITY_REPORT_DIR="$TMP_DIR/parity-report"
mkdir -p "$PARITY_REPORT_DIR"
parity_status=0
node tests/validate-contract-status-parity.mjs --print-empty-routes --print-operation-codes \
  >"$PARITY_REPORT_DIR/stdout.log" 2>"$PARITY_REPORT_DIR/stderr.log" || parity_status=$?
printf '%s\n' "$parity_status" >"$PARITY_REPORT_DIR/status"
cat "$PARITY_REPORT_DIR/stdout.log"
if [ "$parity_status" != 0 ]; then
  cat "$PARITY_REPORT_DIR/stderr.log" >&2
  exit "$parity_status"
fi
export SFORUM_PARITY_REPORT="$PARITY_REPORT_DIR"

JOB_LABELS=()
JOB_COMMANDS=()
register_job() {
  JOB_LABELS+=("$1")
  JOB_COMMANDS+=("$2")
}

# 响应助手白名单、调用边、集合相等、反向检查都消费上面同一次真实仓库扫描；
# 它们仍各自启动合成失败树，parity/reachability 两套解析器仍完全独立。
register_job "response-helper registry closure validation" "node tests/validate-contract-status-parity-helpers.test.mjs"
register_job "contract parity call-edge recognition validation" "node tests/validate-contract-parity-call-edges.test.mjs"
register_job "contract parity sample-set equality validation" "node tests/validate-contract-parity-sample-equality.test.mjs"
register_job "contract status-code reverse-parity and mapper-attribution validation" "node tests/validate-contract-status-parity-reverse.test.mjs"
register_job "contract reachability validation" "node tests/validate-contract-reachability.mjs"
register_job "contract reachability negative fixtures" "node tests/validate-contract-reachability.test.mjs"
register_job "contract gate equivalence validation" "node tests/validate-contract-reachability-equivalence.test.mjs"
register_job "staged extension management contract validation" "node tests/validate-staged-extension-contracts.js"
register_job "V3 production trust deployment validation" "node tests/validate-v3-production-trust.mjs"
register_job "production WebSocket proxy validation" "node tests/validate-production-websocket-proxy.mjs"
register_job "architecture boundary validation" "node tests/validate-architecture-boundaries.mjs"
register_job "documentation and version-governance validation" "node tests/validate-docs.mjs"
register_job "identity UI validation" "node tests/validate-identity-ui.js"
register_job "homepage validation" "node tests/validate-homepage.js"
register_job "public SEO validation" "bun tests/validate-public-seo.ts"
register_job "SEO workbench validation" "bun tests/validate-seo-workbench.ts"
register_job "moderation workbench validation" "bun tests/validate-moderation-workbenches.ts"
register_job "theme runtime validation" "node tests/validate-theme-runtime.js"
register_job "trusted admin runtime validation" "node tests/validate-trusted-admin-runtime.js"
register_job "synchronous theme activation validation" "node tests/validate-theme-activation.js"
register_job "development worker script validation" "node tests/validate-dev-worker-script.js"
register_job "Signal Garden theme validation" "node tests/validate-signal-garden-theme.js"
register_job "SF component library validation" "node tests/validate-sf-components.js"
register_job "Runtime Page Registry offline contracts" "node tests/validate-page-registry-runtime.js"
register_job "V3 platform catalog and traceability validation" "node tests/validate-v3-p0-catalogs.mjs"
register_job "public page runtime surface catalog validation" "node tests/validate-public-page-surface-catalog.mjs"

if [ -d apps/web/node_modules ]; then
  if [ "${SFORUM_CONTRACTS_SKIP_WEB_UNIT:-0}" = "1" ]; then
    echo "Skipping duplicated web unit tests because the web gate owns the full suite."
  else
    register_job "web unit tests for trusted editor L2 and registry catalogs" \
      "(cd apps/web && bun test tests/framework/editorL2Load.test.ts tests/framework/editorDomLoadOrder.test.ts tests/admin/adminRegistryCatalogs.test.ts tests/identity/authProvidersPublicUi.test.ts tests/identity/authRouteRendering.test.ts tests/admin/adminLoginMethods.test.ts tests/admin/adminMailNotifications.test.ts tests/identity/accountSecurityM4b.test.ts)"
  fi
  register_job "admin framework validation" "bun tests/validate-admin-framework.ts"
else
  if [ "${CI:-}" = "true" ]; then
    echo "apps/web/node_modules is required in CI; run bun install --frozen-lockfile first." >&2
    exit 1
  fi
  echo "Skipping web contract unit tests because apps/web/node_modules is missing."
fi

if command -v nproc >/dev/null 2>&1; then
  cores="$(nproc)"
else
  cores="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
fi
job_limit="${SFORUM_CONTRACTS_JOBS:-$((cores > 4 ? 4 : cores))}"
[[ "$job_limit" =~ ^[1-9][0-9]*$ ]] ||
  { echo "SFORUM_CONTRACTS_JOBS 必须是正整数：$job_limit" >&2; exit 2; }

total="${#JOB_LABELS[@]}"
echo "Running $total contracts/static jobs with concurrency $job_limit..."
failed=0
declare -A JOB_PID_INDEX=()
next_index=0
active=0
completed=0
while [ "$completed" -lt "$total" ]; do
  while [ "$active" -lt "$job_limit" ] && [ "$next_index" -lt "$total" ]; do
    printf '[%d/%d] %s\n' "$((next_index + 1))" "$total" "${JOB_LABELS[$next_index]}"
    (
      set -o pipefail
      bash -euo pipefail -c "${JOB_COMMANDS[$next_index]}"
    ) >"$TMP_DIR/$next_index.log" 2>&1 &
    pid=$!
    JOB_PID_INDEX["$pid"]="$next_index"
    ACTIVE_PIDS+=("$pid")
    next_index=$((next_index + 1))
    active=$((active + 1))
  done

  status=0
  wait -n || status=$?
  finished_pid=""
  for pid in "${!JOB_PID_INDEX[@]}"; do
    if ! kill -0 "$pid" 2>/dev/null; then
      finished_pid="$pid"
      break
    fi
  done
  [ -n "$finished_pid" ] || { echo "无法识别已完成的 contracts/static 任务" >&2; exit 1; }
  job_index="${JOB_PID_INDEX[$finished_pid]}"
  unset 'JOB_PID_INDEX[$finished_pid]'
  ACTIVE_PIDS=("${!JOB_PID_INDEX[@]}")
  active=$((active - 1))
  completed=$((completed + 1))
  if [ "$status" -eq 0 ]; then
    printf 'PASS %s\n' "${JOB_LABELS[$job_index]}"
  else
    printf 'FAIL %s (exit %s)\n' "${JOB_LABELS[$job_index]}" "$status" >&2
    if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
      printf '::error title=Contracts gate failed::%s\n' "${JOB_LABELS[$job_index]}"
    fi
    cat "$TMP_DIR/$job_index.log" >&2
    failed=1
  fi
done

[ "$failed" = 0 ] || { echo "One or more contracts/static jobs failed." >&2; exit 1; }
echo "All contracts/static jobs passed."
