#!/usr/bin/env bash
# M5 四条门禁一站式串行复跑：给出「HEAD 到底绿不绿」的单次可复现答案。
#
# 四条门禁（顺序：便宜的在前，长跑的在最后；互相不依赖）：
#   arch     node tests/validate-architecture-boundaries.mjs
#   openapi  ruby scripts/validate-openapi-refs.rb
#   v3catalog node scripts/v3-catalog/generate.mjs --check
#   fast     ./scripts/test.sh --fast
#
# 行为：串行执行，逐条打印耗时；汇总表落 summary.tsv；任一条失败整体非零退出。
# 默认「跑完四条再判」（基线快照需要全部结论），--fail-fast 则第一条红色即停。
#
# 用法：
#   ./scripts/m5-gates.sh                     # 全跑
#   ./scripts/m5-gates.sh --fail-fast         # 红即停
#   ./scripts/m5-gates.sh --only openapi      # 只跑指定门禁（可重复/逗号分隔）
#   ./scripts/m5-gates.sh --list              # 列出门禁 id 与命令
#
# 日志：tests/.logs/m5-gates-<UTC 时间戳>/{arch,openapi,v3catalog,fast}.log + summary.tsv
# 不修改任何受版本控制的文件（v3-catalog 只用 --check，不写产物）。
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GATE_ORDER=(arch openapi v3catalog fast)
declare -A GATE_CMD=(
  [arch]="node tests/validate-architecture-boundaries.mjs"
  [openapi]="ruby scripts/validate-openapi-refs.rb"
  [v3catalog]="node scripts/v3-catalog/generate.mjs --check"
  [fast]="./scripts/test.sh --fast"
)

FAIL_FAST=0
LIST_ONLY=0
SELECTED=""

usage() {
  sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
}

add_selection() {
  local id
  local IFS=','
  for id in $1; do
    [[ -z "$id" ]] && continue
    if [[ -z "${GATE_CMD[$id]:-}" ]]; then
      echo "unknown gate: $id (known: ${GATE_ORDER[*]})" >&2
      exit 2
    fi
    SELECTED+=" $id"
  done
}

while (($# > 0)); do
  case "$1" in
    --fail-fast) FAIL_FAST=1 ;;
    --list) LIST_ONLY=1 ;;
    --only)
      if (($# < 2)); then
        echo "--only requires a gate id" >&2
        exit 2
      fi
      add_selection "$2"
      shift
      ;;
    --only=*) add_selection "${1#--only=}" ;;
    -h | --help) usage; exit 0 ;;
    *)
      echo "unknown argument: $1 (supported: --fail-fast, --only <id>, --list, --help)" >&2
      exit 2
      ;;
  esac
  shift
done

if ((LIST_ONLY == 1)); then
  for id in "${GATE_ORDER[@]}"; do
    printf '%-10s %s\n' "$id" "${GATE_CMD[$id]}"
  done
  exit 0
fi

RUN_GATES=("${GATE_ORDER[@]}")
if [[ -n "$SELECTED" ]]; then
  RUN_GATES=()
  for id in "${GATE_ORDER[@]}"; do
    [[ " $SELECTED " == *" $id "* ]] && RUN_GATES+=("$id")
  done
fi

TS="$(date -u +%Y%m%dT%H%M%SZ)"
LOG_DIR="$ROOT_DIR/tests/.logs/m5-gates-$TS"
mkdir -p "$LOG_DIR"
SUMMARY="$LOG_DIR/summary.tsv"
printf 'gate\texit\telapsed_ms\tcommand\n' >"$SUMMARY"

now_ms() { date +%s%3N; }

# 本地工具链必须写进日志：web 门的结论对 bun 版本敏感，历史上出现过
# 「本地 1.4.2 绿 / CI 1.3.14 红」这种只能靠版本号解释的差异。
BUN_VERSION="$(bun --version 2>/dev/null || echo '?')"
GIT_HEAD="$(git rev-parse HEAD 2>/dev/null || echo '?')"
# CI 的 bun 版本就是仓库声明的钉住值（.github/workflows/ci.yml 的 bun-version）。
CI_BUN_VERSION="$(awk '/setup-bun/{found=1} found && /bun-version:/{gsub(/[",]/, "", $2); print $2; exit}' \
  "$ROOT_DIR/.github/workflows/ci.yml" 2>/dev/null || true)"
[[ -z "$CI_BUN_VERSION" ]] && CI_BUN_VERSION='?'

{
  echo "captured_at=$(date -Iseconds)"
  echo "head=$GIT_HEAD ($(git rev-parse --short HEAD 2>/dev/null || echo '?'))"
  echo "go=$(go version 2>/dev/null || echo '?')"
  echo "node=$(node --version 2>/dev/null || echo '?')"
  echo "ruby=$(ruby --version 2>/dev/null | head -1 || echo '?')"
  echo "bun=$BUN_VERSION revision=$(bun --revision 2>/dev/null || echo '?')"
  echo "bun_ci_pin=$CI_BUN_VERSION"
  echo "uname=$(uname -srm)"
} >"$LOG_DIR/env.txt"

echo "HEAD      $(git rev-parse --short HEAD 2>/dev/null || echo '?')  ($GIT_HEAD)"
echo "go        $(go version 2>/dev/null | awk '{print $3}' || echo '?')"
echo "node      $(node --version 2>/dev/null || echo '?')"
echo "ruby      $(ruby --version 2>/dev/null | awk '{print $2}' || echo '?')"
echo "bun       $BUN_VERSION  (CI pin: $CI_BUN_VERSION)"
if [[ "$BUN_VERSION" != '?' && "$CI_BUN_VERSION" != '?' && "$BUN_VERSION" != "$CI_BUN_VERSION" ]]; then
  echo "warn      local bun $BUN_VERSION != CI pin $CI_BUN_VERSION; the fast gate's web line may differ from CI for version-sensitive behaviour." >&2
fi
echo "log dir   ${LOG_DIR#"$ROOT_DIR"/}  (env: env.txt)"
[[ -n "${DATABASE_URL:-}" ]] && echo "note      DATABASE_URL is set in this shell; Go tests still provision an isolated cluster via scripts/test-api-isolated.sh."
echo

declare -A EXIT_OF=()
FAILED=()

for id in "${RUN_GATES[@]}"; do
  cmd="${GATE_CMD[$id]}"
  log="$LOG_DIR/$id.log"
  echo "==> [$id] $cmd"
  # 每份日志自带环境头，脱离控制台也能回答「这是哪个 bun/go 跑出来的」。
  printf '# env gate=%s head=%s bun=%s(ci %s) node=%s go=%s ruby=%s\n' \
    "$id" "$(git rev-parse --short HEAD 2>/dev/null || echo '?')" \
    "$BUN_VERSION" "$CI_BUN_VERSION" \
    "$(node --version 2>/dev/null || echo '?')" \
    "$(go version 2>/dev/null | awk '{print $3}' || echo '?')" \
    "$(ruby --version 2>/dev/null | awk '{print $2}' || echo '?')" >"$log"
  started="$(now_ms)"
  # pipefail 让 `cmd | tee` 的退出码取自 cmd，而不是 tee；-a 保留上面的环境头。
  eval "$cmd" 2>&1 | tee -a "$log"
  status=${PIPESTATUS[0]}
  elapsed=$(( $(now_ms) - started ))
  EXIT_OF[$id]=$status
  printf '%s\t%s\t%s\t%s\n' "$id" "$status" "$elapsed" "$cmd" >>"$SUMMARY"
  if ((status == 0)); then
    printf '<== [%s] PASS  %sms\n\n' "$id" "$elapsed"
  else
    printf '<== [%s] FAIL  exit=%s  %sms  log: %s\n\n' "$id" "$status" "$elapsed" "${log#"$ROOT_DIR"/}"
    FAILED+=("$id")
    if ((FAIL_FAST == 1)); then
      echo "--fail-fast: stopping after [$id]" >&2
      break
    fi
  fi
done

echo "=========================== m5 gate summary ==========================="
for id in "${RUN_GATES[@]}"; do
  status="${EXIT_OF[$id]:-skipped}"
  if [[ "$status" == 0 ]]; then
    printf 'PASS  %-10s %8sms  %s\n' "$id" "$(awk -F'\t' -v g="$id" '$1==g{print $3}' "$SUMMARY")" "${GATE_CMD[$id]}"
  else
    printf 'FAIL  %-10s %8sms  exit=%s  %s\n' "$id" "$(awk -F'\t' -v g="$id" '$1==g{print $3}' "$SUMMARY")" "$status" "${GATE_CMD[$id]}"
  fi
done
echo "summary: ${SUMMARY#"$ROOT_DIR"/}"

if ((${#FAILED[@]} > 0)); then
  echo
  for id in "${FAILED[@]}"; do
    echo "----- tail of ${LOG_DIR#"$ROOT_DIR"/}/$id.log -----"
    tail -n 40 "$LOG_DIR/$id.log"
    echo
  done
  echo "M5 gates failed: ${FAILED[*]}" >&2
  exit 1
fi

echo "All M5 gates passed."
