#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DEPLOY_RC=".deployrc"
DEFAULT_RELEASE_VERSION="${SFORUM_DEFAULT_VERSION:-latest}"
LANGUAGE=""
ACTION=""
ASSUME_DEFAULTS=false
REQUESTED_RELEASE_VERSION="${SFORUM_VERSION:-}"
RELEASE_VERSION=""
CHANNEL="${SFORUM_DEPLOY_CHANNEL:-stable}"
COMPOSE=()
BLUE_GREEN_COMPOSE=()
RELEASE_IMAGE_SERVICES=()
APP_SERVICES=()

usage() {
  cat <<'EOF'
Usage: ./deploy.sh [options]

Without --action, an interactive console offers install/update, backup, restore,
status, logs, restart, and stop.

Options:
  --lang zh|en          Interface language
  --version VERSION     Release tag, or latest stable release (default: latest)
  --channel CHANNEL     Update channel: stable (default) or prerelease
  --action ACTION       deploy, install, update, backup, restore, status,
                        logs, restart, stop, rollback
  --yes, --defaults     Accept recommended configuration defaults
  -h, --help            Show this help

Actions:
  deploy|install|update Resolve the target release, back up an existing
                        database, run migrations, start the release images, and
                        verify health before recording .deployrc.
  backup                Create a PostgreSQL backup under backups/ (0600).
  restore               Restore a backup after typing RESTORE.
  status|logs           Inspect the currently recorded deployment.
  restart|stop          Restart or stop the managed Compose services.
  rollback              Print rollback guidance; it does not change versions.

State:
  .deployrc             Recorded version, topology, and recovery state (0600).
  .deploy.lock          Prevents two deploy operations from running at once.
  .env.production       Required production configuration (0600).

Channels:
  stable                Resolve the newest stable GitHub Release only.
  prerelease            Allow prereleases when resolving "latest".

Prereleases are never selected implicitly: use --channel prerelease or pass an
explicit immutable tag such as v3.0.0-alpha.13. Every choice resolves to a
concrete vX.Y.Z tag before images are pulled; floating "latest" images are
never run.
EOF
}

die() {
  printf '%s\n' "$1" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --lang)
      [ "$#" -ge 2 ] || die "--lang requires a value"
      LANGUAGE="$2"
      shift 2
      ;;
    --lang=*) LANGUAGE="${1#*=}"; shift ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      REQUESTED_RELEASE_VERSION="$2"
      shift 2
      ;;
    --version=*) REQUESTED_RELEASE_VERSION="${1#*=}"; shift ;;
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      CHANNEL="$2"
      shift 2
      ;;
    --channel=*) CHANNEL="${1#*=}"; shift ;;
    --action)
      [ "$#" -ge 2 ] || die "--action requires a value"
      ACTION="$2"
      shift 2
      ;;
    --action=*) ACTION="${1#*=}"; shift ;;
    --yes|--defaults) ASSUME_DEFAULTS=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done

rc_value() {
  local key="$1"
  [ -f "$DEPLOY_RC" ] || return 0
  sed -n "s/^${key}=//p" "$DEPLOY_RC" | tail -n 1
}

env_file_value() {
  local file="$1"
  local key="$2"
  local fallback="${3:-}"
  local value=""
  if [ -f "$file" ]; then
    value="$(sed -n "s/^${key}=//p" "$file" | tail -n 1)"
  fi
  printf '%s' "${value:-$fallback}"
}

load_language() {
  if [ -z "$LANGUAGE" ]; then
    LANGUAGE="$(rc_value lang)"
  fi
  if [ -z "$LANGUAGE" ]; then
    if [ "$ASSUME_DEFAULTS" = true ]; then
      LANGUAGE="zh"
    else
      echo "Choose language / 选择语言:"
      echo "1) English"
      echo "2) 简体中文（推荐）"
      read -r -p "> [2] " choice
      case "$choice" in
        1) LANGUAGE="en" ;;
        *) LANGUAGE="zh" ;;
      esac
    fi
  fi
  [ "$LANGUAGE" = "zh" ] || [ "$LANGUAGE" = "en" ] || die "--lang must be zh or en"
}

t() {
  local key="$1"
  if [ "$LANGUAGE" = "en" ]; then
    case "$key" in
      title) echo "SForum deployment console" ;;
      install) echo "Install or update (recommended)" ;;
      migrate) echo "Run migrations" ;;
      backup) echo "Create PostgreSQL backup" ;;
      restore) echo "Restore PostgreSQL backup" ;;
      status) echo "View service status" ;;
      logs) echo "View logs" ;;
      restart) echo "Restart services" ;;
      stop) echo "Stop services" ;;
      exit) echo "Exit" ;;
      preflight) echo "Checking Docker and production configuration..." ;;
      no_docker) echo "Docker is required. Install Docker Desktop or Docker Engine first." ;;
      no_daemon) echo "Docker is installed but the daemon is not running. Start Docker and retry." ;;
      no_compose) echo "Docker Compose 2.24.4 or newer is required." ;;
      no_curl) echo "curl is required for the final health check." ;;
      env_missing) echo ".env.production is missing. Run the install/update action first." ;;
      invalid_public_api_base) echo "NUXT_PUBLIC_API_BASE_URL must be /api/v1." ;;
      invalid_env) echo "The production configuration is incomplete or unsafe. Move it aside and rerun the installer, or fix the named value." ;;
      port_busy) echo "A required loopback port is already used by another process:" ;;
      deploy_locked) echo "Another deployment operation is running. Wait for it to finish and retry." ;;
      pulling_release) echo "Pulling the required SForum release images before changing the database:" ;;
      verifying_images) echo "Verifying required backend image identities..." ;;
      starting_infra) echo "Starting the managed PostgreSQL and Redis services..." ;;
      fresh_database) echo "Fresh database detected; no backup is needed." ;;
      backup_first) echo "Existing installation detected; creating a PostgreSQL backup..." ;;
      stopping_app) echo "Stopping the previous API and Web containers..." ;;
      migrations_running) echo "Running database migrations with the target release image..." ;;
      starting_app) echo "Starting SForum from release images..." ;;
      verifying) echo "Checking API, Web, and all managed services..." ;;
      recovery_required) echo "Deployment did not complete after the old app stopped. Services were not declared healthy; inspect .deployrc and Docker logs before retrying." ;;
      version) echo "Release version" ;;
      install_dir) echo "Install directory:" ;;
      config_file) echo "Configuration file:" ;;
      backup_dir) echo "Backup directory:" ;;
      web_proxy) echo "Reverse proxy Web target:" ;;
      api_proxy) echo "Reverse proxy API/WebSocket target:" ;;
      access_url) echo "Site access URL:" ;;
      admin_url) echo "Admin URL:" ;;
      success) echo "Deployment completed successfully." ;;
      rollback_later) echo "To roll back, deploy an earlier immutable version only after checking migration compatibility." ;;
      confirm_restore) echo "Type RESTORE to confirm database restore:" ;;
      backup_path) echo "Backup file path:" ;;
      canceled) echo "Canceled." ;;
      *) echo "$key" ;;
    esac
  else
    case "$key" in
      title) echo "SForum 部署控制台" ;;
      install) echo "安装或更新（推荐）" ;;
      migrate) echo "运行数据库迁移" ;;
      backup) echo "创建 PostgreSQL 备份" ;;
      restore) echo "恢复 PostgreSQL 备份" ;;
      status) echo "查看服务状态" ;;
      logs) echo "查看日志" ;;
      restart) echo "重启服务" ;;
      stop) echo "停止服务" ;;
      exit) echo "退出" ;;
      preflight) echo "正在检查 Docker 和生产配置..." ;;
      no_docker) echo "需要先安装 Docker Desktop 或 Docker Engine。" ;;
      no_daemon) echo "Docker 已安装，但服务尚未运行；请启动 Docker 后重试。" ;;
      no_compose) echo "需要 Docker Compose 2.24.4 或更高版本。" ;;
      no_curl) echo "最终健康检查需要 curl。" ;;
      env_missing) echo "缺少 .env.production，请先选择 '安装或更新'。" ;;
      invalid_public_api_base) echo "NUXT_PUBLIC_API_BASE_URL 必须是 /api/v1。" ;;
      invalid_env) echo "生产配置不完整或不安全。请移走旧文件后重新运行安装，或修复提示中的配置项。" ;;
      port_busy) echo "以下本机端口已被其他程序占用：" ;;
      deploy_locked) echo "另一个部署操作正在运行，请等待其结束后重试。" ;;
      pulling_release) echo "先拉取所需的 SForum 发布镜像，成功后才会改动数据库：" ;;
      verifying_images) echo "正在核对所需后端镜像的构建身份..." ;;
      starting_infra) echo "正在启动内置 PostgreSQL 和 Redis..." ;;
      fresh_database) echo "检测到全新数据库，本次无需备份。" ;;
      backup_first) echo "检测到已有安装，正在创建 PostgreSQL 备份..." ;;
      stopping_app) echo "正在停止旧版 API 和 Web 容器..." ;;
      migrations_running) echo "正在使用目标版本镜像运行数据库迁移..." ;;
      starting_app) echo "正在从发布镜像启动 SForum..." ;;
      verifying) echo "正在检查 API、Web 和全部内置服务..." ;;
      recovery_required) echo "旧应用停止后部署未能完成。当前服务未被声明为健康；重试前请检查 .deployrc 和 Docker 日志。" ;;
      version) echo "发布版本" ;;
      install_dir) echo "安装目录：" ;;
      config_file) echo "配置文件：" ;;
      backup_dir) echo "备份目录：" ;;
      web_proxy) echo "反代 Web 目标地址：" ;;
      api_proxy) echo "反代 API/WebSocket 目标地址：" ;;
      access_url) echo "站点访问地址：" ;;
      admin_url) echo "管理后台地址：" ;;
      success) echo "部署成功完成。" ;;
      rollback_later) echo "如需回滚，请先确认数据库迁移兼容，再部署较早的不可变版本。" ;;
      confirm_restore) echo "请输入 RESTORE 确认恢复数据库：" ;;
      backup_path) echo "备份文件路径：" ;;
      canceled) echo "已取消。" ;;
      *) echo "$key" ;;
    esac
  fi
}

# 目标版本解析：仅用于 deploy/install/update。显式 --version 优先；否则按
# --channel 解析 latest。绝不把 .deployrc/.env.production 中的旧版本当作目标。
resolve_target_version() {
  local response resolved
  [ "$CHANNEL" = "stable" ] || [ "$CHANNEL" = "prerelease" ] || \
    die "--channel must be stable or prerelease"
  RELEASE_VERSION="${REQUESTED_RELEASE_VERSION:-$DEFAULT_RELEASE_VERSION}"
  if [ "$RELEASE_VERSION" = "latest" ]; then
    if [ "$CHANNEL" = "stable" ]; then
      response="$(curl -fsSL \
        --connect-timeout 10 \
        --max-time 30 \
        -H 'Accept: application/vnd.github+json' \
        -H 'X-GitHub-Api-Version: 2022-11-28' \
        -H 'User-Agent: SForum-deploy' \
        "${SFORUM_LATEST_RELEASE_API_URL:-https://api.github.com/repos/keeptrue/SForum/releases/latest}")" || \
        die "Could not query the latest stable GitHub Release. Check the network, or use --channel prerelease to allow prereleases."
      resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
      [ -n "$resolved" ] || die "GitHub returned no stable SForum Release. Use --channel prerelease to allow prereleases."
    else
      response="$(curl -fsSL \
        --connect-timeout 10 \
        --max-time 30 \
        -H 'Accept: application/vnd.github+json' \
        -H 'X-GitHub-Api-Version: 2022-11-28' \
        -H 'User-Agent: SForum-deploy' \
        "${SFORUM_RELEASES_API_URL:-https://api.github.com/repos/keeptrue/SForum/releases?per_page=1}")" || \
        die "Could not query the latest GitHub Release. Check the network and retry."
      resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
      [ -n "$resolved" ] || die "GitHub returned no published SForum Release."
    fi
    RELEASE_VERSION="$resolved"
  fi
  validate_release_version
  configure_compose
}

# 当前版本解析：仅用于 status/logs/restart/stop/backup/restore/rollback 等
# 维护动作。默认使用 .deployrc/.env.production 中已部署的版本，不访问 GitHub；
# 显式 --version 优先（同样不访问 GitHub）。
resolve_current_version() {
  local current
  [ "$CHANNEL" = "stable" ] || [ "$CHANNEL" = "prerelease" ] || \
    die "--channel must be stable or prerelease"
  if [ -n "$REQUESTED_RELEASE_VERSION" ]; then
    RELEASE_VERSION="$REQUESTED_RELEASE_VERSION"
  else
    current="$(rc_value version)"
    [ -n "$current" ] || current="$(env_file_value .env.production SFORUM_VERSION)"
    [ -n "$current" ] || die "Cannot determine the currently deployed version; run the install/update action first."
    RELEASE_VERSION="$current"
  fi
  validate_release_version
  configure_compose
}

validate_release_version() {
  if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    die "--version must be latest or look like v3.0.0"
  fi
}

configure_compose() {
  export SFORUM_VERSION="$RELEASE_VERSION"
  COMPOSE=(docker compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml)
  RELEASE_IMAGE_SERVICES=(migrate api web)
  APP_SERVICES=(postgres redis api web)
  configure_blue_green_compose
}

deployment_uses_blue_green() {
  [ "$(rc_value topology)" = blue-green ]
}

configure_blue_green_compose() {
  local blue_version green_version
  BLUE_GREEN_COMPOSE=()
  deployment_uses_blue_green || return 0
  blue_version="$(rc_value blue_version)"
  green_version="$(rc_value green_version)"
  [[ "$blue_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "Invalid blue_version in .deployrc"
  [[ "$green_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "Invalid green_version in .deployrc"
  export SFORUM_BLUE_VERSION="$blue_version"
  export SFORUM_GREEN_VERSION="$green_version"
  BLUE_GREEN_COMPOSE=(
    docker compose --env-file .env.production
    -f compose.yaml -f compose.prod.yaml -f compose.release.yaml
    -f compose.zero-downtime.yaml --profile zero-downtime
  )
}

configure_if_needed() {
  if [ -f .env.production ]; then
    chmod 600 .env.production
    return
  fi
  local wizard=(./deploy/scripts/configure-production.sh --lang "$LANGUAGE" --version "$RELEASE_VERSION")
  if [ "$ASSUME_DEFAULTS" = true ]; then
    wizard+=(--defaults)
  fi
  "${wizard[@]}"
}

compose_version_supported() {
  local raw major minor patch
  raw="$(docker compose version --short 2>/dev/null || true)"
  raw="${raw#v}"
  raw="${raw%%-*}"
  IFS='.' read -r major minor patch <<< "$raw"
  [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ && "$patch" =~ ^[0-9]+$ ]] || return 1
  (( major > 2 || (major == 2 && minor > 24) || (major == 2 && minor == 24 && patch >= 4) ))
}

validate_env_contract() {
  local public_api_base app_env app_url admin_prefix postgres_db postgres_user postgres_password database_url
  local redis_addr redis_password session_secret identity_secret option_key altcha_secret
  public_api_base="$(env_file_value .env.production NUXT_PUBLIC_API_BASE_URL /api/v1)"
  [ "$public_api_base" = "/api/v1" ] || die "$(t invalid_public_api_base)"

  app_env="$(env_file_value .env.production APP_ENV)"
  app_url="$(env_file_value .env.production APP_URL)"
  admin_prefix="$(env_file_value .env.production NUXT_PUBLIC_ADMIN_ROUTE_PREFIX /control-panel)"
  postgres_db="$(env_file_value .env.production POSTGRES_DB)"
  postgres_user="$(env_file_value .env.production POSTGRES_USER)"
  postgres_password="$(env_file_value .env.production POSTGRES_PASSWORD)"
  database_url="$(env_file_value .env.production DATABASE_URL)"
  redis_addr="$(env_file_value .env.production REDIS_ADDR)"
  redis_password="$(env_file_value .env.production REDIS_PASSWORD)"
  session_secret="$(env_file_value .env.production SESSION_HASH_SECRET)"
  identity_secret="$(env_file_value .env.production IDENTITY_SUBJECT_HMAC_SECRET)"
  option_key="$(env_file_value .env.production APP_OPTION_ENC_KEY)"
  altcha_secret="$(env_file_value .env.production ALTCHA_SECRET)"

  [ "$app_env" = "production" ] || die "$(t invalid_env) APP_ENV"
  [[ "$app_url" =~ ^https?://[^[:space:]]+$ ]] || die "$(t invalid_env) APP_URL"
  [[ "$admin_prefix" =~ ^/[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "$(t invalid_env) NUXT_PUBLIC_ADMIN_ROUTE_PREFIX"
  case "$admin_prefix" in
    *'//'|*/./*|*/../*|/.|/..|*/.|*/..) die "$(t invalid_env) NUXT_PUBLIC_ADMIN_ROUTE_PREFIX" ;;
  esac
  [[ "$postgres_db" =~ ^[A-Za-z0-9_.-]+$ ]] || die "$(t invalid_env) POSTGRES_DB"
  [[ "$postgres_user" =~ ^[A-Za-z0-9_.-]+$ ]] || die "$(t invalid_env) POSTGRES_USER"
  [[ "$postgres_password" =~ ^[A-Za-z0-9._~-]{32,}$ ]] || die "$(t invalid_env) POSTGRES_PASSWORD"
  [[ "$database_url" == postgres://*"@postgres:5432/${postgres_db}"* ]] || die "$(t invalid_env) DATABASE_URL"
  [ "$redis_addr" = "redis:6379" ] || die "$(t invalid_env) REDIS_ADDR"
  [[ "$redis_password" =~ ^[A-Za-z0-9._~-]{32,}$ ]] || die "$(t invalid_env) REDIS_PASSWORD"
  [ "${#session_secret}" -ge 32 ] || die "$(t invalid_env) SESSION_HASH_SECRET"
  [ "${#identity_secret}" -ge 32 ] || die "$(t invalid_env) IDENTITY_SUBJECT_HMAC_SECRET"
  [[ "$option_key" =~ ^[0-9A-Fa-f]{64}$ ]] || die "$(t invalid_env) APP_OPTION_ENC_KEY"
  [ "${#altcha_secret}" -ge 32 ] || die "$(t invalid_env) ALTCHA_SECRET"
}

legacy_worker_container_ids() {
  local project service
  project="${COMPOSE_PROJECT_NAME:-$(env_file_value .env.production COMPOSE_PROJECT_NAME sforum)}"
  for service in worker worker-blue worker-green; do
    docker ps -aq \
      --filter "label=com.docker.compose.project=${project}" \
      --filter "label=com.docker.compose.service=${service}"
  done
}

remove_legacy_worker_containers() {
  local id failed=false
  while IFS= read -r id; do
    [ -z "$id" ] || docker rm -f "$id" || failed=true
  done < <(legacy_worker_container_ids | sort -u)
  [ "$failed" = false ]
}

service_is_running() {
  local service="$1"
  "${COMPOSE[@]}" ps --status running --services | grep -Fqx "$service"
}

blue_green_ingress_is_running() {
  [ "${#BLUE_GREEN_COMPOSE[@]}" -gt 0 ] || return 1
  "${BLUE_GREEN_COMPOSE[@]}" ps --status running --services | grep -Fqx edge
}

port_is_listening() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
  elif command -v ss >/dev/null 2>&1; then
    ss -ltn | awk -v suffix=":$port" 'NR > 1 && $4 ~ suffix "$" { found=1 } END { exit !found }'
  else
    return 1
  fi
}

validate_ports_available() {
  local web_port api_port managed_ingress=false
  web_port="$(env_file_value .env.production WEB_PORT 3000)"
  api_port="$(env_file_value .env.production API_PORT 18080)"
  if blue_green_ingress_is_running; then
    managed_ingress=true
  fi
  if ! service_is_running web && [ "$managed_ingress" != true ] && port_is_listening "$web_port"; then
    die "$(t port_busy) 127.0.0.1:${web_port} (Web)"
  fi
  if ! service_is_running api && [ "$managed_ingress" != true ] && port_is_listening "$api_port"; then
    die "$(t port_busy) 127.0.0.1:${api_port} (API)"
  fi
}

preflight() {
  local check_ports="${1:-false}"
  t preflight
  command -v docker >/dev/null 2>&1 || die "$(t no_docker)"
  docker info >/dev/null 2>&1 || die "$(t no_daemon)"
  docker compose version >/dev/null 2>&1 || die "$(t no_compose)"
  compose_version_supported || die "$(t no_compose)"
  command -v curl >/dev/null 2>&1 || die "$(t no_curl)"
  [ -f .env.production ] || die "$(t env_missing)"
  chmod 600 .env.production
  validate_env_contract
  "${COMPOSE[@]}" config --quiet
  if [ "${#BLUE_GREEN_COMPOSE[@]}" -gt 0 ]; then
    "${BLUE_GREEN_COMPOSE[@]}" config --quiet
  fi
  if [ "$check_ports" = true ]; then
    validate_ports_available
  fi
}

database_has_installation() {
  local postgres_user postgres_db result
  postgres_user="$(env_file_value .env.production POSTGRES_USER sforum)"
  postgres_db="$(env_file_value .env.production POSTGRES_DB sforum)"
  if ! result="$("${COMPOSE[@]}" exec -T postgres psql -U "$postgres_user" -d "$postgres_db" -tAc "SELECT to_regclass('public.goose_db_version') IS NOT NULL")"; then
    die "Could not inspect the PostgreSQL migration state."
  fi
  [ "${result//[[:space:]]/}" = "t" ]
}

run_migrations_command() {
  t migrations_running
  "${COMPOSE[@]}" run --rm -T --pull never migrate
}

wait_for_deployment() {
  local api_port web_port timeout_seconds
  api_port="$(env_file_value .env.production API_PORT 18080)"
  web_port="$(env_file_value .env.production WEB_PORT 3000)"
  timeout_seconds="${SFORUM_DEPLOY_HEALTH_TIMEOUT_SECONDS:-120}"
  ./deploy/scripts/wait-for-health.sh "http://127.0.0.1:${api_port}/api/v1/ready" "$timeout_seconds" || return 1
  ./deploy/scripts/wait-for-health.sh "http://127.0.0.1:${web_port}/health" "$timeout_seconds" || return 1
}

verify_services_running() {
  local running service
  local expected=(postgres redis api web)
  running="$("${COMPOSE[@]}" ps --status running --services)"
  if [ -n "$(legacy_worker_container_ids)" ]; then
    echo "Legacy standalone Worker containers are still present." >&2
    return 1
  fi
  for service in "${expected[@]}"; do
    if ! grep -Fqx "$service" <<< "$running"; then
      echo "Service is not running after deployment: $service" >&2
      return 1
    fi
  done
}

verify_services_stable() {
  local stability_seconds="${SFORUM_DEPLOY_STABILITY_SECONDS:-3}"
  if [ "$stability_seconds" -gt 0 ]; then
    sleep "$stability_seconds"
  fi
  verify_services_running
}

verify_release_identities() {
  local service binary image actual expected_version
  local backend_services=(api migrate)
  expected_version="${RELEASE_VERSION#v}"
  t verifying_images
  for service in "${backend_services[@]}"; do
    binary="sforum-$service"
    image="${SFORUM_REGISTRY:-ghcr.io/keeptrue}/sforum-${service}:${RELEASE_VERSION}"
    if ! actual="$(docker run --rm "$image" "$binary" --version)"; then
      echo "Could not inspect the $service image identity." >&2
      return 1
    fi
    if [[ "$actual" != "SForum $expected_version ("* ]]; then
      echo "$service image identity mismatch: $actual" >&2
      return 1
    fi
  done
}

persist_successful_state() {
  local temp_file
  umask 077
  temp_file="$(mktemp "$ROOT_DIR/.deployrc.XXXXXX")"
  printf 'lang=%s\nversion=%s\nmode=release\ninstall_dir=%s\n' \
    "$LANGUAGE" "$RELEASE_VERSION" "$ROOT_DIR" > "$temp_file"
  chmod 600 "$temp_file"
  mv -f "$temp_file" "$DEPLOY_RC"
}

persist_recovery_state() {
  local reason="$1"
  local backup_file="${2:-}"
  local previous_version="$3"
  local temp_file
  umask 077
  temp_file="$(mktemp "$ROOT_DIR/.deployrc.XXXXXX")"
  printf 'lang=%s\nstatus=recovery_required\nattempted_version=%s\nprevious_version=%s\nreason=%s\nbackup=%s\ninstall_dir=%s\n' \
    "$LANGUAGE" "$RELEASE_VERSION" "$previous_version" "$reason" "$backup_file" "$ROOT_DIR" > "$temp_file"
  chmod 600 "$temp_file"
  mv -f "$temp_file" "$DEPLOY_RC"
}

DEPLOY_LOCK_DIR="$ROOT_DIR/.deploy.lock"
acquire_deploy_lock() {
  if ! mkdir "$DEPLOY_LOCK_DIR" 2>/dev/null; then
    die "$(t deploy_locked)"
  fi
  trap 'rmdir "$DEPLOY_LOCK_DIR" 2>/dev/null || true' EXIT HUP INT TERM
}

release_deploy_lock() {
  rmdir "$DEPLOY_LOCK_DIR" 2>/dev/null || true
  trap - EXIT HUP INT TERM
}

# Absolute paths first: an operator should never have to guess where the
# installation, its configuration, and its backups actually live.
print_install_location() {
  local recorded
  printf '%s %s\n' "$(t install_dir)" "$ROOT_DIR"
  printf '%s %s\n' "$(t config_file)" "$ROOT_DIR/.env.production"
  printf '%s %s\n' "$(t backup_dir)" "$ROOT_DIR/backups"
  recorded="$(rc_value install_dir)"
  if [ -n "$recorded" ] && [ "$recorded" != "$ROOT_DIR" ]; then
    if [ "$LANGUAGE" = en ]; then
      printf 'Note: .deployrc records %s but the current directory is %s\n' \
        "$recorded" "$ROOT_DIR"
    else
      printf '注意：.deployrc 记录为 %s，当前目录是 %s\n' "$recorded" "$ROOT_DIR"
    fi
  fi
}

print_access_urls() {
  local app_url admin_prefix admin_url web_port api_port
  app_url="$(env_file_value .env.production APP_URL)"
  app_url="${app_url%/}"
  admin_prefix="$(env_file_value .env.production NUXT_PUBLIC_ADMIN_ROUTE_PREFIX /control-panel)"
  admin_url="${app_url}${admin_prefix}"
  web_port="$(env_file_value .env.production WEB_PORT 3000)"
  api_port="$(env_file_value .env.production API_PORT 18080)"
  printf '%s %s\n' "$(t web_proxy)" "http://127.0.0.1:${web_port}"
  printf '%s %s\n' "$(t api_proxy)" "http://127.0.0.1:${api_port}"
  printf '%s %s\n' "$(t access_url)" "$app_url"
  printf '%s %s\n' "$(t admin_url)" "$admin_url"
}

deploy_update() {
  local backup_output="" backup_file="" previous_version previous_running
  acquire_deploy_lock
  configure_if_needed
  # configure_if_needed may have created the production file after initial
  # version resolution; rebuild the mode-aware Compose command from it.
  configure_compose
  preflight true
  previous_version="$(rc_value version)"
  if [ "${#BLUE_GREEN_COMPOSE[@]}" -gt 0 ]; then
    previous_running="$("${BLUE_GREEN_COMPOSE[@]}" ps --status running --services | \
      grep -E '^(edge|api-(blue|green)|web-(blue|green))$' || true)"
  else
    previous_running="$("${COMPOSE[@]}" ps --status running --services | grep -E '^(api|web)$' || true)"
  fi
  printf '%s %s\n' "$(t version):" "$RELEASE_VERSION"

  echo "$(t pulling_release) $RELEASE_VERSION"
  "${COMPOSE[@]}" pull "${RELEASE_IMAGE_SERVICES[@]}"
  verify_release_identities || die "Release image verification failed before any database change."

  t starting_infra
  "${COMPOSE[@]}" up -d --wait postgres redis

  if database_has_installation; then
    t backup_first
    backup_output="$(./deploy/scripts/backup-postgres.sh)"
    printf '%s\n' "$backup_output"
    backup_file="$(tail -n 1 <<< "$backup_output")"
  else
    t fresh_database
  fi

  t stopping_app
  if [ "${#BLUE_GREEN_COMPOSE[@]}" -gt 0 ]; then
    if ! "${BLUE_GREEN_COMPOSE[@]}" stop \
      web-blue web-green api-blue api-green edge; then
      if [ -n "$previous_running" ]; then
        # shellcheck disable=SC2086
        "${BLUE_GREEN_COMPOSE[@]}" start $previous_running || true
      fi
      die "Could not stop the previous blue/green application services."
    fi
  elif ! "${COMPOSE[@]}" stop api web; then
    if [ -n "$previous_running" ]; then
      # shellcheck disable=SC2086
      "${COMPOSE[@]}" start $previous_running || true
    fi
      die "Could not stop the previous application services."
  fi
  remove_legacy_worker_containers || die "Could not remove legacy standalone Worker containers."
  if ! run_migrations_command; then
    persist_recovery_state migration_failed "$backup_file" "$previous_version"
    t recovery_required >&2
    return 1
  fi

  t starting_app
  if ! "${COMPOSE[@]}" up -d --no-build --pull never "${APP_SERVICES[@]}"; then
    persist_recovery_state startup_failed "$backup_file" "$previous_version"
    t recovery_required >&2
    return 1
  fi

  t verifying
  if ! wait_for_deployment || ! verify_services_running || ! verify_services_stable; then
    persist_recovery_state health_failed "$backup_file" "$previous_version"
    t recovery_required >&2
    "${COMPOSE[@]}" ps >&2 || true
    return 1
  fi
  "${COMPOSE[@]}" ps || true
  persist_successful_state
  release_deploy_lock
  print_install_location
  print_access_urls
  t success
}

create_backup() {
  acquire_deploy_lock
  preflight
  "${COMPOSE[@]}" up -d --wait postgres
  ./deploy/scripts/backup-postgres.sh
  release_deploy_lock
}

restore_backup() {
  acquire_deploy_lock
  preflight
  t backup_path
  read -r backup_file
  t confirm_restore
  read -r confirmation
  if [ "$confirmation" != "RESTORE" ]; then
    t canceled
    release_deploy_lock
    return
  fi
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$backup_file"
  release_deploy_lock
}

show_status() { preflight; print_install_location; "${COMPOSE[@]}" ps; }
show_logs() { preflight; "${COMPOSE[@]}" logs -f --tail=200; }
restart_services() {
  acquire_deploy_lock
  preflight true
  "${COMPOSE[@]}" restart
  wait_for_deployment
  verify_services_running
  "${COMPOSE[@]}" ps
  print_install_location
  print_access_urls
  release_deploy_lock
}
stop_services() { acquire_deploy_lock; preflight; "${COMPOSE[@]}" stop; release_deploy_lock; }

run_action() {
  case "$1" in
    deploy|install|update) deploy_update ;;
    backup) create_backup ;;
    restore) restore_backup ;;
    status) show_status ;;
    logs) show_logs ;;
    restart) restart_services ;;
    stop) stop_services ;;
    rollback) preflight; t rollback_later ;;
    *) die "Unknown action: $1" ;;
  esac
}

main_menu() {
  while true; do
    echo
    echo "== $(t title) =="
    echo "1) $(t install)"
    echo "2) $(t backup)"
    echo "3) $(t restore)"
    echo "4) $(t status)"
    echo "5) $(t logs)"
    echo "6) $(t restart)"
    echo "7) $(t stop)"
    echo "0) $(t exit)"
    read -r -p "> [1] " menu_action
    case "$menu_action" in
      ""|1) resolve_target_version; deploy_update ;;
      2) resolve_current_version; create_backup ;;
      3) resolve_current_version; restore_backup ;;
      4) resolve_current_version; show_status ;;
      5) resolve_current_version; show_logs ;;
      6) resolve_current_version; restart_services ;;
      7) resolve_current_version; stop_services ;;
      0) exit 0 ;;
      *) echo "?" ;;
    esac
  done
}

load_language
if [ -n "$ACTION" ]; then
  case "$ACTION" in
    deploy|install|update) resolve_target_version ;;
    *) resolve_current_version ;;
  esac
  run_action "$ACTION"
else
  main_menu
fi
