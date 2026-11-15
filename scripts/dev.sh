#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

usage() {
  cat <<'USAGE'
Usage: ./scripts/dev.sh [options]

Options:
  --build, --rebuild  Rebuild development images before starting.
  --no-migrate        Skip the one-shot migration command after dependencies start.
  --print-command     Print the resolved Docker Compose command and exit.
  -h, --help          Show this help message.

Default mode starts only development dependency services: PostgreSQL, Redis,
Meilisearch, and Mailpit. Run the frontend and API locally with:

  cd apps/web && bun run dev
  ./scripts/api-dev.sh

In development, the API always owns the background worker. A separate Worker
process is not supported.
USAGE
}

BUILD_ENABLED=0
MIGRATE_ENABLED=1
PRINT_COMMAND=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --build | --rebuild)
      BUILD_ENABLED=1
      ;;
    --no-migrate)
      MIGRATE_ENABLED=0
      ;;
    --watch | --worker | --with-worker)
      echo "Option $1 is no longer supported by ./scripts/dev.sh."
      echo "Start frontend/backend locally with 'bun run dev' and './scripts/api-dev.sh'."
      exit 1
      ;;
    --print-command)
      PRINT_COMMAND=1
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      usage
      exit 1
      ;;
  esac
  shift
done

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required. Please install Docker first."
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose plugin is required. Please install or update Docker."
  exit 1
fi

if [ ! -f .env ]; then
  # .env 会保存 APP_OPTION_ENC_KEY 等本地密钥，创建时就按 0600 落盘，而不是先
  # 按默认 umask 生成一份 0664 再补救。umask 放在子 shell 里：调用方的 umask
  # 不该被这个脚本改掉。
  (umask 077 && cp .env.example .env)
  echo "Created .env from .env.example"
fi

# 权限收敛而不只是创建时收紧：历史工作区里的 .env 可能是 0664（本机就是），
# 而它下面马上会被写入加密密钥。chmod 失败不阻断启动（只读挂载等场景），
# 但必须提示，否则「以为已经收紧」比没收紧更危险。
if [ -f .env ] && ! chmod 600 .env 2>/dev/null; then
  echo "Warning: could not set .env permissions to 0600; check it is not world-readable."
fi

# 首次初始化时补上本地开发用的加密密钥：没有它 Secret Store 走透明模式（明文存储），
# AI 控制台会一直显示「缺少加密密钥」且拒绝保存供应商密钥。
if ! grep -q '^APP_OPTION_ENC_KEY=.\+' .env; then
  dev_enc_key="$(openssl rand -hex 32 2>/dev/null || od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')"
  if [[ "$dev_enc_key" =~ ^[0-9a-f]{64}$ ]]; then
    if grep -q '^APP_OPTION_ENC_KEY=' .env; then
      sed -i "s|^APP_OPTION_ENC_KEY=.*|APP_OPTION_ENC_KEY=${dev_enc_key}|" .env
    else
      printf '\n# 本地开发加密密钥（首次初始化自动生成）\nAPP_OPTION_ENC_KEY=%s\n' "$dev_enc_key" >> .env
    fi
    echo "Generated APP_OPTION_ENC_KEY for local development"
  else
    echo "Warning: could not generate APP_OPTION_ENC_KEY; set it manually with 'openssl rand -hex 32'"
  fi
fi

set -a
. ./.env
set +a

if [ -z "${APP_OPTION_ENC_KEY:-}" ]; then
  echo "Warning: APP_OPTION_ENC_KEY is empty; secrets are stored unencrypted and AI credential saving is blocked."
fi

COMPOSE_FILES=(-f compose.yaml -f compose.dev.yaml)
# Meilisearch 为可选搜索引擎（compose profile search），默认不启动以省内存。
DEPENDENCY_SERVICES=(postgres redis mailpit)
UP_ARGS=("${COMPOSE_FILES[@]}" up --remove-orphans --wait)
if [ "$BUILD_ENABLED" -eq 1 ]; then
  UP_ARGS+=(--build)
fi
UP_ARGS+=("${DEPENDENCY_SERVICES[@]}")

MIGRATE_DATABASE_URL="postgres://${POSTGRES_USER:-sforum}:${POSTGRES_PASSWORD:-sforum}@postgres:5432/${POSTGRES_DB:-sforum}?sslmode=disable"
MIGRATE_PRINT_DATABASE_URL="postgres://${POSTGRES_USER:-sforum}:***@postgres:5432/${POSTGRES_DB:-sforum}?sslmode=disable"
MIGRATE_ARGS=("${COMPOSE_FILES[@]}" run --rm --no-deps -e "DATABASE_URL=${MIGRATE_DATABASE_URL}")
MIGRATE_PRINT_ARGS=("${COMPOSE_FILES[@]}" run --rm --no-deps -e "DATABASE_URL=${MIGRATE_PRINT_DATABASE_URL}")
if [ "$BUILD_ENABLED" -eq 1 ]; then
  MIGRATE_ARGS+=(--build)
  MIGRATE_PRINT_ARGS+=(--build)
fi
MIGRATE_ARGS+=(migrate)
MIGRATE_PRINT_ARGS+=(migrate)

echo "Starting SForum development dependencies..."
if [ "$BUILD_ENABLED" -eq 1 ]; then
  echo "Mode: rebuild migration image when needed"
else
  echo "Mode: fast dependency start, no forced rebuild"
fi
echo "Postgres: 127.0.0.1:${POSTGRES_PORT:-15432}"
echo "Redis: 127.0.0.1:${REDIS_PORT:-16379}"
echo "Mailpit: http://127.0.0.1:${MAILPIT_UI_PORT:-18025}"
echo "Meilisearch: optional (docker compose --profile search up -d meilisearch); external package roots: ${EXTERNAL_EXTENSION_ROOTS:-not configured}"
EXPECTED_NUXT_API_INTERNAL_BASE_URL="http://127.0.0.1:${HTTP_PORT:-8080}/api/v1"
case "${NUXT_API_INTERNAL_BASE_URL:-}" in
  http://127.0.0.1:* | http://localhost:*)
    if [ "${NUXT_API_INTERNAL_BASE_URL}" != "$EXPECTED_NUXT_API_INTERNAL_BASE_URL" ]; then
      echo "Warning: NUXT_API_INTERNAL_BASE_URL=${NUXT_API_INTERNAL_BASE_URL}"
      echo "         but HTTP_PORT=${HTTP_PORT:-8080}; expected ${EXPECTED_NUXT_API_INTERNAL_BASE_URL} for host-run API."
    fi
    ;;
esac
if [ "$MIGRATE_ENABLED" -eq 1 ]; then
  echo "One-shot database migrations run after PostgreSQL is healthy."
else
  echo "One-shot database migrations skipped by --no-migrate."
fi
echo "API startup still follows MIGRATE_ON_STARTUP=${MIGRATE_ON_STARTUP:-true}."
echo "Background jobs always run inside the API process."
echo "Then run: cd apps/web && bun run dev"
echo "Then run: ./scripts/api-dev.sh"
echo "Use './scripts/dev.sh --build' after Dockerfile or dependency changes."

if [ "$PRINT_COMMAND" -eq 1 ]; then
  printf "docker compose"
  printf " %q" "${UP_ARGS[@]}"
  printf "\n"
  if [ "$MIGRATE_ENABLED" -eq 1 ]; then
    printf "docker compose"
    printf " %q" "${MIGRATE_PRINT_ARGS[@]}"
    printf "\n"
  fi
  exit 0
fi

# 旧开发流会留下 Compose 管理的前后端容器；先停掉，避免占用本机端口。
docker compose "${COMPOSE_FILES[@]}" stop web api >/dev/null 2>&1 || true

docker compose "${UP_ARGS[@]}"

if [ "$MIGRATE_ENABLED" -eq 1 ]; then
  docker compose "${MIGRATE_ARGS[@]}"
fi
