#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"

if [ -f "$ENV_FILE" ]; then
  set -a
  . "$ENV_FILE"
  set +a
else
  echo "Missing .env. Run ./scripts/dev.sh once to create it from .env.example."
  exit 1
fi

HTTP_PORT="${HTTP_PORT:-8080}"

if ! command -v air >/dev/null 2>&1; then
  echo "air is required. Install it with: go install github.com/air-verse/air@latest"
  exit 1
fi

# 启动前强制回收本仓库遗留的 sforum-api；docker/其他服务占用则失败，不误杀。
if ! ORPHANS_ONLY=0 "$ROOT_DIR/scripts/free-api-dev-port.sh" "$HTTP_PORT"; then
  echo "API port $HTTP_PORT is already in use by a non-sforum process. Not stopping it."
  exit 1
fi
# 清理热重载遗留的扩展 plugin 孤儿（PPID=1）；不杀当前/其它 live API 的子进程。
"$ROOT_DIR/scripts/cleanup-orphan-extension-plugins.sh" || true

"$ROOT_DIR/scripts/build-builtin-plugins.sh"
# 使用 staging 内置树（含本机编译二进制 + 刷新后的 digest），避免改写 git 跟踪的 manifest。
export BUILTIN_EXTENSION_ROOT="${SFORUM_BUILTIN_DEV_ROOT:-$ROOT_DIR/storage/builtin-dev}"

# 在开发库写入哨兵 public.sforum_dev_guard：apps/api/internal/testdb 见到它就
# 拒绝连接，避免在 source 过 .env 的 shell 里跑 go test 把测试扩展写进开发库。
# 幂等，每次启动都执行；一次性测试集群不会被标记（由集群标记识别）。
if ! (cd "$ROOT_DIR/apps/api" && go run ./internal/testdb/cmd/devguard); then
  echo "Development database guard failed. Is the dev stack up (./scripts/dev.sh)?" >&2
  exit 1
fi

cd "$ROOT_DIR/apps/api"
exec air
