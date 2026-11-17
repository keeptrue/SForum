#!/usr/bin/env bash
# 等待一个健康端点变成 2xx。三态，不是两态：
#   0  就绪
#   3  端点明确报告「恢复态」（5xx 且 body 带 recovery 块）—— 终态，等下去不会
#      变好，必须人工执行 `sforum extension recovery recheck`
#   1  超时 / 参数错误
#
# 为什么能这样判：恢复态下 health.ApplyRecoveryRequirement 把 /ready 的 ready 置
# false（app/Support/Health/recovery.go），于是 /ready 答 503 service.not_ready 并
# 带 recovery{code,component,message}；recoveryOnlyMiddleware 只放行 /health 与
# /ready（app/Http/server.go），所以 /ready 的 body 是唯一可达的恢复态信号。
# /health 仍答 200 —— 它只代表进程活着，不代表能接流量。
#
# 关键区分：依赖没起（PG 未就绪）也答 503，但 body 里没有 recovery 块 —— 那是瞬态，
# 必须继续等。把两者混成同一个「不健康」，恢复态就会白耗满超时，然后被报成
# health_failed，把真正的原因埋掉。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# 终端配色层：唯一实现在同目录的 term-ui.sh，随部署包分发、由 bootstrap 提升。
# shellcheck source=deploy/scripts/term-ui.sh
source "$SCRIPT_DIR/term-ui.sh"

URL="${1:-}"
TIMEOUT_SECONDS="${2:-60}"

usage() {
  echo "Usage: deploy/scripts/wait-for-health.sh URL [timeout_seconds]"
}

if [ "$URL" = "" ]; then
  usage
  exit 1
fi

case "$TIMEOUT_SECONDS" in
  ""|*[!0-9]*)
    usage >&2
    exit 1
    ;;
esac

BODY_FILE="$(mktemp "${TMPDIR:-/tmp}/sforum-health-body.XXXXXX")"
cleanup() { rm -f "$BODY_FILE"; }
trap cleanup EXIT

started_at="$(date +%s)"

while true; do
  status=""
  # 不用 -f：需要读 5xx 的 body 才能区分「恢复态」与「依赖未就绪」。
  if status="$(curl -sS -o "$BODY_FILE" -w '%{http_code}' --max-time 10 "$URL" 2>/dev/null)"; then
    :
  else
    status="000"
  fi

  case "$status" in
    2??)
      ui_ok "Healthy: $URL"
      exit 0
      ;;
    5??)
      # 只认 /ready 的 recovery 块。`"recovery":` 不会命中 /health 的
      # `"recoveryRequired":false` —— 那里的 key 后面跟的是 Required，不是引号加冒号。
      if grep -Eq '"recovery"[[:space:]]*:' "$BODY_FILE" 2>/dev/null; then
        reason="$(sed -n 's/.*"recovery"[[:space:]]*:[^{]*{[^}]*"code"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
          "$BODY_FILE" | head -n 1)"
        ui_err "Recovery-only: $URL answered $status with a recovery block${reason:+ ($reason)}"
        ui_err 'The Host booted into recovery-only mode: only /health and /ready are reachable.'
        ui_err 'Fix the reported artifact, then run: sforum extension recovery recheck'
        exit 3
      fi
      ;;
  esac

  now="$(date +%s)"
  if [ $((now - started_at)) -ge "$TIMEOUT_SECONDS" ]; then
    ui_err "Timed out waiting for health: $URL"
    exit 1
  fi

  sleep 2
done
