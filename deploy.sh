#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DEPLOY_RC=".deployrc"
DEFAULT_RELEASE_VERSION="${SFORUM_DEFAULT_VERSION:-latest}"
LANGUAGE=""
ACTION=""
ASSUME_DEFAULTS=false
CONTRACT_MODE="strict"
REQUESTED_RELEASE_VERSION="${SFORUM_VERSION:-}"
RELEASE_VERSION=""
CHANNEL="${SFORUM_DEPLOY_CHANNEL:-stable}"
COMPOSE=()
BLUE_GREEN_COMPOSE=()
RELEASE_IMAGE_SERVICES=()
APP_SERVICES=()

# 环境契约键：deploy.sh 会逐项校验这些值，而容器实际拿到的值必须与校验值完全一致。
# 它们正是 compose.yaml / compose.prod.yaml 里 `${KEY:-...}` 插值的生产必需项。
ENV_CONTRACT_KEYS=(
  APP_ENV
  APP_URL
  NUXT_PUBLIC_API_BASE_URL
  NUXT_PUBLIC_ADMIN_ROUTE_PREFIX
  POSTGRES_DB
  POSTGRES_USER
  POSTGRES_PASSWORD
  DATABASE_URL
  REDIS_ADDR
  REDIS_PASSWORD
  SESSION_HASH_SECRET
  IDENTITY_SUBJECT_HMAC_SECRET
  APP_OPTION_ENC_KEY
  ALTCHA_SECRET
  # 与上面各项同理：compose.yaml 的默认值都在不安全侧，漏写即静默取默认值（OPS2-08）。
  TRUSTED_PROXIES
  MIGRATE_ON_STARTUP
)

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
                        logs, restart, stop, rollback, check-env, unlock
  --yes, --defaults     Accept recommended configuration defaults
  --force-unlock        Reclaim .deploy.lock after a crash (--action unlock)
  -h, --help            Show this help

Actions:
  deploy|install|update Resolve the target release, back up an existing
                        database, run migrations, start the release images, and
                        verify health before recording .deployrc.
  check-env             Validate .env.production against the values the
                        containers will receive, then exit. Needs no Docker.
  backup                Create a PostgreSQL backup under backups/ (0600).
  restore               Restore a backup after typing RESTORE.
  status|logs           Inspect the currently recorded deployment.
  restart|stop          Restart or stop the managed Compose services.
  rollback              Print rollback guidance; it does not change versions.
  unlock                Reclaim a stale .deploy.lock and log who held it.

  status, logs, and stop treat production-configuration problems as warnings:
  reading logs and stopping services must keep working while an operator is
  fixing .env.production. Every other action validates strictly.

State:
  .deployrc             Recorded version, topology, and recovery state (0600).
  .deploy.lock          Prevents concurrent operations; stale locks self-heal.
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
    --force-unlock) ACTION="unlock"; shift ;;
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

# docker compose --env-file 的插值顺序是「shell 环境 > --env-file > compose 文件默认」。
# shell 里 export 的同名变量会静默盖掉 .env.production，容器拿到的值就可能和
# validate_env_contract 校验的值不是同一个（审计 OPS2-05）。所以校验一律走
# effective 取值，并在拼装 compose 命令前清掉这些 shell 覆盖。
env_contract_value() {
  local key="$1"
  local fallback="${2:-}"
  local value=""
  if [ -n "${!key+set}" ]; then
    printf '%s' "${!key}"
    return 0
  fi
  value="$(env_file_value .env.production "$key")"
  printf '%s' "${value:-$fallback}"
}

# 校验值来自哪里：shell 覆盖、.env.production，还是 compose 文件里的默认值。
env_contract_source() {
  local key="$1"
  if [ -n "${!key+set}" ]; then
    printf 'shell'
  elif [ -n "$(env_file_value .env.production "$key")" ]; then
    printf 'file'
  else
    printf 'default'
  fi
}

# 在调用 compose 之前清掉契约键的 shell 覆盖，让「校验的值」就是「容器拿到的值」。
# 被忽略的导出显式告警：静默忽略会让人以为自己 export 的值生效了。
sanitize_env_contract_overrides() {
  local key
  local ignored=()
  for key in "${ENV_CONTRACT_KEYS[@]}"; do
    [ -n "${!key+set}" ] || continue
    ignored+=("$key")
    unset "$key"
  done
  if [ "${#ignored[@]}" -gt 0 ]; then
    printf '%s %s\n' "$(t env_override_ignored)" "${ignored[*]}" >&2
  fi
}

# 校验失败必须指向运维真正能改的地方：.env.production。只有在 shell 覆盖没被清掉
# （sanitize 被绕过）时才点名 shell，避免让人去改错误的文件。
die_env_contract() {
  local key="$1"
  if [ "$(env_contract_source "$key")" = shell ]; then
    contract_fail "$(t invalid_env) ${key} ($(t env_source_shell))"
  else
    contract_fail "$(t invalid_env) ${key} (.env.production)"
  fi
}

# 生产配置闸门分级。strict 直接拒绝；advisory 只把同一条修法提示打到 stderr 并继续。
# stop / logs / status 是事故中的止损与取证动作：新加的配置键（OPS2-08 的
# MIGRATE_ON_STARTUP / TRUSTED_PROXIES）在存量安装里根本不存在，让它们因为「文件缺
# 新键」而停不掉服务、读不到日志，是把闸门一致性排在可用性前面。
contract_fail() {
  if [ "$CONTRACT_MODE" = advisory ]; then
    printf 'warning: %s\n' "$1" >&2
    return 0
  fi
  die "$1"
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
      env_override_ignored) echo "Ignoring these shell environment overrides; production configuration comes from .env.production:" ;;
      env_source_shell) echo "exported in the shell environment and overriding .env.production; unset it or update .env.production" ;;
      env_contract_ok) echo "Production configuration is valid and matches the values the containers receive." ;;
      port_busy) echo "A required loopback port is already used by another process:" ;;
      deploy_locked) echo "Another deployment operation is running; wait for it to finish, or reclaim the lock with --force-unlock. Holder:" ;;
      lock_recovered) echo "Reclaimed a stale deployment lock. Previous holder:" ;;
      unlock_none) echo "No deployment lock is present." ;;
      unlock_done) echo "Removed the deployment lock by force. Previous holder:" ;;
      invalid_trusted_proxies) echo "TRUSTED_PROXIES must not trust every address; delete the full-range entry from .env.production. Under the standard Compose topology it stays empty; only a CDN or an extra proxy warrants concrete egress ranges:" ;;
      missing_migrate_on_startup) echo "MIGRATE_ON_STARTUP must be declared explicitly in .env.production as true or false; compose.yaml would otherwise default it to true and the API would migrate at startup. Add MIGRATE_ON_STARTUP=false and let the deploy.sh migrate service run migrations." ;;
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
      recovery_only) echo "The new API booted into recovery-only mode: only /health and /ready answer, every other path returns 503. This is not a slow start; fix the artifact named in the recovery block, then run 'sforum extension recovery recheck'." ;;
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
      env_override_ignored) echo "已忽略以下 shell 环境变量；生产配置一律以 .env.production 为准：" ;;
      env_source_shell) echo "来自 shell 环境变量并覆盖了 .env.production；请 unset 该变量或更新 .env.production" ;;
      env_contract_ok) echo "生产配置校验通过：容器取值与 .env.production 一致。" ;;
      port_busy) echo "以下本机端口已被其他程序占用：" ;;
      deploy_locked) echo "另一个部署操作正在运行；请等待其结束，或用 --force-unlock 回收残留锁。持有者：" ;;
      lock_recovered) echo "已回收残留部署锁。原持有者：" ;;
      unlock_none) echo "当前没有部署锁。" ;;
      unlock_done) echo "已强制移除部署锁。原持有者：" ;;
      invalid_trusted_proxies) echo "TRUSTED_PROXIES 不能信任所有地址；请从 .env.production 删掉全放通项。标准 Compose 拓扑下应留空，只有 CDN 或额外代理才写具体的出口网段：" ;;
      missing_migrate_on_startup) echo "MIGRATE_ON_STARTUP 必须在 .env.production 中显式声明为 true 或 false；否则 compose.yaml 默认 true，API 会在启动时自行迁移。请写入 MIGRATE_ON_STARTUP=false，由 deploy.sh 的 migrate 服务统一执行迁移。" ;;
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
      recovery_only) echo "新 API 进入了恢复态：只放行 /health 与 /ready，其余路径一律 503。这不是启动慢；先修好恢复块里点名的制品，再执行 sforum extension recovery recheck。" ;;
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

# 百分号解码（只解一层，与 Go 的 url.Parse 对 userinfo 的处理一致）。
# 口令里的 %XX 在 URL 里是合法转义，Go 侧解码后才比对占位口令；shell 侧不解码
# 就会漏掉 `change%2Dme` 这类写法，check-env 通过而容器启动 panic。
percent_decode() {
  local input="$1"
  printf '%b' "${input//%/\\x}"
}

configure_compose() {
  export SFORUM_VERSION="$RELEASE_VERSION"
  # compose 插值里 shell 优先于 --env-file：先清掉契约键的 shell 覆盖，
  # preflight 校验的值才会等于容器实际取值（OPS2-05）。
  sanitize_env_contract_overrides
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
  CONTRACT_MODE="${1:-strict}"
  local public_api_base app_env app_url admin_prefix postgres_db postgres_user postgres_password database_url
  local redis_addr redis_password session_secret identity_secret option_key altcha_secret
  local trusted_proxies token
  # 全部走 env_contract_value：它按 compose 的插值顺序取值，所以校验的就是容器
  # 真正会拿到的值，而不是「文件里写了什么」。
  public_api_base="$(env_contract_value NUXT_PUBLIC_API_BASE_URL /api/v1)"
  [ "$public_api_base" = "/api/v1" ] || contract_fail "$(t invalid_public_api_base)"

  app_env="$(env_contract_value APP_ENV)"
  app_url="$(env_contract_value APP_URL)"
  admin_prefix="$(env_contract_value NUXT_PUBLIC_ADMIN_ROUTE_PREFIX /control-panel)"
  postgres_db="$(env_contract_value POSTGRES_DB)"
  postgres_user="$(env_contract_value POSTGRES_USER)"
  postgres_password="$(env_contract_value POSTGRES_PASSWORD)"
  database_url="$(env_contract_value DATABASE_URL)"
  redis_addr="$(env_contract_value REDIS_ADDR)"
  redis_password="$(env_contract_value REDIS_PASSWORD)"
  session_secret="$(env_contract_value SESSION_HASH_SECRET)"
  identity_secret="$(env_contract_value IDENTITY_SUBJECT_HMAC_SECRET)"
  option_key="$(env_contract_value APP_OPTION_ENC_KEY)"
  altcha_secret="$(env_contract_value ALTCHA_SECRET)"

  [ "$app_env" = "production" ] || die_env_contract APP_ENV
  [[ "$app_url" =~ ^https?://[^[:space:]]+$ ]] || die_env_contract APP_URL
  [[ "$admin_prefix" =~ ^/[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die_env_contract NUXT_PUBLIC_ADMIN_ROUTE_PREFIX
  case "$admin_prefix" in
    *'//'|*/./*|*/../*|/.|/..|*/.|*/..) die_env_contract NUXT_PUBLIC_ADMIN_ROUTE_PREFIX ;;
  esac
  [[ "$postgres_db" =~ ^[A-Za-z0-9_.-]+$ ]] || die_env_contract POSTGRES_DB
  [[ "$postgres_user" =~ ^[A-Za-z0-9_.-]+$ ]] || die_env_contract POSTGRES_USER
  [[ "$postgres_password" =~ ^[A-Za-z0-9._~-]{32,}$ ]] || die_env_contract POSTGRES_PASSWORD
  [[ "$database_url" == postgres://*"@postgres:5432/${postgres_db}"* ]] || die_env_contract DATABASE_URL
  # 与 config.ValidateProductionDatabaseURL 同口径：占位口令的 URL 在容器里会让
  # config.Load 直接 panic，校验阶段放行就等于「check-env 通过、API 起不来」。
  # 口令先做一层百分号解码——Go 的 url.Parse 会解码 userinfo，`change%2Dme`
  # 在两边必须是同一个判定。
  if [[ "$database_url" =~ ^postgres://[^:/@]*:([^@]*)@ ]]; then
    case "$(percent_decode "${BASH_REMATCH[1]}")" in
      ""|change-me|sforum|postgres) die_env_contract DATABASE_URL ;;
    esac
  fi
  [ "$redis_addr" = "redis:6379" ] || die_env_contract REDIS_ADDR
  [[ "$redis_password" =~ ^[A-Za-z0-9._~-]{32,}$ ]] || die_env_contract REDIS_PASSWORD
  [ "${#session_secret}" -ge 32 ] || die_env_contract SESSION_HASH_SECRET
  [ "${#identity_secret}" -ge 32 ] || die_env_contract IDENTITY_SUBJECT_HMAC_SECRET
  [[ "$option_key" =~ ^[0-9A-Fa-f]{64}$ ]] || die_env_contract APP_OPTION_ENC_KEY
  [ "${#altcha_secret}" -ge 32 ] || die_env_contract ALTCHA_SECRET

  # OPS2-08：TRUSTED_PROXIES 声明「谁有权替我写 XFF / X-Real-IP」。全放通等于任何
  # 客户端都能伪造来源 IP，限流键与审计记录按伪造值记账，所以生产直接拒绝。
  trusted_proxies="$(env_contract_value TRUSTED_PROXIES)"
  # 逐项切分而不是用数组：bash 3.2（macOS 自带）在 set -u 下展开空数组会报错。
  while IFS= read -r token; do
    token="${token//[[:space:]]/}"
    case "$token" in
      "") continue ;;
      '*'|'0.0.0.0'|'::'|'0:0:0:0:0:0:0:0'|*/0) contract_fail "$(t invalid_trusted_proxies) TRUSTED_PROXIES=${token}" ;;
    esac
  done <<< "${trusted_proxies//,/$'\n'}"
  # OPS2-08：compose.yaml 的 MIGRATE_ON_STARTUP 默认 true，漏写即「API 启动时自行
  # 迁移」。这里要求显式声明而非静默兜底成 false：兜底会让「文件内容」与「容器取值」
  # 不一致，破坏 env_contract「校验值即容器值」的契约。漏写时 effective 值为空。
  case "$(env_contract_value MIGRATE_ON_STARTUP)" in
    true|false) ;;
    "") contract_fail "$(t missing_migrate_on_startup)" ;;
    *) contract_fail "$(t missing_migrate_on_startup) MIGRATE_ON_STARTUP=$(env_contract_value MIGRATE_ON_STARTUP)" ;;
  esac
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
  local contract_mode="${2:-strict}"
  t preflight
  command -v docker >/dev/null 2>&1 || die "$(t no_docker)"
  docker info >/dev/null 2>&1 || die "$(t no_daemon)"
  docker compose version >/dev/null 2>&1 || die "$(t no_compose)"
  compose_version_supported || die "$(t no_compose)"
  command -v curl >/dev/null 2>&1 || die "$(t no_curl)"
  [ -f .env.production ] || die "$(t env_missing)"
  chmod 600 .env.production
  validate_env_contract "$contract_mode"
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

# 备份密钥来源：shell 环境 > .env.production。两条都要支持 —— backup-postgres.sh
# 只读进程环境，而运维既会 export（文档写法）也会写进 .env.production（模板里给了
# 注释块）；只认前者，模板就成了陷阱。空值等同未设置（脚本按 -n 判定），所以缺 key
# 时行为与直接调用完全一致：硬失败，不写明文。
run_database_backup() {
  SFORUM_BACKUP_KEY_FILE="${SFORUM_BACKUP_KEY_FILE:-$(env_file_value .env.production SFORUM_BACKUP_KEY_FILE)}" \
  SFORUM_BACKUP_KEY="${SFORUM_BACKUP_KEY:-$(env_file_value .env.production SFORUM_BACKUP_KEY)}" \
    ./deploy/scripts/backup-postgres.sh
}

# 返回值与 deploy/scripts/wait-for-health.sh 的三态一致：0 就绪 / 3 恢复态 / 1 超时。
# 3 必须原样上抛，不能折成 1：恢复态是终态（Host 只放行 /health 与 /ready，等下去
# 不会变好，要人工跑 recovery recheck），超时是「再看日志」。把两者折成一个
# health_failed，等于让运维在 120 秒后去猜是哪一种。
wait_for_deployment() {
  local api_port web_port timeout_seconds status=0
  api_port="$(env_file_value .env.production API_PORT 18080)"
  web_port="$(env_file_value .env.production WEB_PORT 3000)"
  timeout_seconds="${SFORUM_DEPLOY_HEALTH_TIMEOUT_SECONDS:-120}"
  ./deploy/scripts/wait-for-health.sh "http://127.0.0.1:${api_port}/api/v1/ready" "$timeout_seconds" || status=$?
  [ "$status" -eq 0 ] || return "$status"
  ./deploy/scripts/wait-for-health.sh "http://127.0.0.1:${web_port}/health" "$timeout_seconds" || status=$?
  [ "$status" -eq 0 ] || return "$status"
  return 0
}

# 健康判定的终态落盘：0 通过；3 = 恢复态（reason=recovery_only）；其余 = 超时
# （reason=health_failed）。两种失败都写 status=recovery_required —— 服务确实没被
# 声明为健康，语义不变 —— 区别只在「运维下一步做什么」：恢复态要跑 recovery
# recheck，超时看日志。
verify_deployment_health() {
  local backup_file="$1"
  local previous_version="$2"
  local status=0

  wait_for_deployment || status=$?
  if [ "$status" -eq 0 ] && ! verify_services_running; then status=1; fi
  if [ "$status" -eq 0 ] && ! verify_services_stable; then status=1; fi
  [ "$status" -ne 0 ] || return 0
  if [ "$status" -eq 3 ]; then
    persist_recovery_state recovery_only "$backup_file" "$previous_version"
    t recovery_only >&2
  else
    persist_recovery_state health_failed "$backup_file" "$previous_version"
    t recovery_required >&2
  fi
  return 1
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
  local service binary image actual expected_version registry
  local backend_services=(api migrate)
  expected_version="${RELEASE_VERSION#v}"
  # 与 compose 插值保持一致：shell 环境优先，其次 .env.production，最后才是默认前缀。
  registry="${SFORUM_REGISTRY:-$(env_file_value .env.production SFORUM_REGISTRY ghcr.io/keeptrue)}"
  registry="${registry%/}"
  for service in "${backend_services[@]}"; do
    binary="sforum-$service"
    image="${registry}/sforum-${service}:${RELEASE_VERSION}"
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

# 部署锁实现见 deploy/scripts/deploy-lock.sh：deploy.sh 与 upgrade.sh 共用同一套
# 判定（pid 存活、ps -p 兜底、lstart 复用检测、认领式回收），避免两处各写一份。
DEPLOY_LOCK_ROOT="$ROOT_DIR"
lock_text() { t "$1"; }
# shellcheck source=deploy/scripts/deploy-lock.sh
source "$ROOT_DIR/deploy/scripts/deploy-lock.sh"

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
  acquire_deploy_lock deploy
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
    backup_output="$(run_database_backup)"
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
  if ! verify_deployment_health "$backup_file" "$previous_version"; then
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
  acquire_deploy_lock backup
  preflight
  "${COMPOSE[@]}" up -d --wait postgres
  run_database_backup
  release_deploy_lock
}

restore_backup() {
  acquire_deploy_lock restore
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

show_status() { preflight false advisory; print_install_location; "${COMPOSE[@]}" ps; }
show_logs() { preflight false advisory; "${COMPOSE[@]}" logs -f --tail=200; }
restart_services() {
  local status=0
  acquire_deploy_lock restart
  preflight true
  "${COMPOSE[@]}" restart
  # restart 不写 .deployrc（它没动过版本），但恢复态必须点名：否则「重启后站点
  # 还是 503」会被当成重启没生效。
  wait_for_deployment || {
    status=$?
    if [ "$status" -eq 3 ]; then
      die "$(t recovery_only)"
    fi
    exit "$status"
  }
  verify_services_running
  "${COMPOSE[@]}" ps
  print_install_location
  print_access_urls
  release_deploy_lock
}
stop_services() { acquire_deploy_lock stop; preflight false advisory; "${COMPOSE[@]}" stop; release_deploy_lock; }

# 只校验生产配置：不启动容器、不需要 Docker daemon，供 CI 与运维本地自检。
# 与 preflight 共用同一套 effective 取值，所以「这里通过」等价于「容器会拿到这些值」。
check_env_contract() {
  [ -f .env.production ] || die "$(t env_missing)"
  chmod 600 .env.production
  sanitize_env_contract_overrides
  validate_env_contract
  printf '%s\n' "$(t env_contract_ok)"
}

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
    check-env) check_env_contract ;;
    unlock) force_unlock_deploy_lock ;;
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
  # check-env 只读 .env.production：不解析版本，也不碰 Docker。
  if [ "$ACTION" != "check-env" ] && [ "$ACTION" != "unlock" ]; then
    case "$ACTION" in
      deploy|install|update) resolve_target_version ;;
      *) resolve_current_version ;;
    esac
  fi
  run_action "$ACTION"
else
  main_menu
fi
