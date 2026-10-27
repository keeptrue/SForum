#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

REQUESTED_VERSION=""
TARGET_VERSION=""
ASSUME_YES=false
FORCE_BOOTSTRAP=false
CONFIRM_MAINTENANCE=false
CHANNEL="stable"
CHANNEL_EXPLICIT=false
VERSION_EXPLICIT=false
DEPLOY_RC=".deployrc"
RUNTIME_DIR="$ROOT_DIR/deploy/runtime"
LOCK_DIR="$ROOT_DIR/.deploy.lock"
COMPOSE=()
# 镜像前缀必须与 compose 插值出的前缀一致：pull/verify 用这个变量，
# compose run migrate 读的是 .env.production 的同名变量。
REGISTRY=""
CANONICAL_REGISTRY="ghcr.io/keeptrue"
LEGACY_REGISTRY="ghcr.io/zhuchunshu"

usage() {
  cat <<'EOF'
Usage: ./upgrade.sh [VERSION] [options]

Zero-downtime HTTP update for an existing SForum Compose installation.

Normally invoked by ./sforum-bootstrap.sh upgrade after the bootstrap refreshes
the target Release toolkit. Running this file directly requires both
.env.production and .deployrc in the current directory.

Options:
  VERSION             Target release number/tag, or latest (default: latest)
  --version VERSION   Same as the positional VERSION argument
  --channel CHANNEL   Update channel: stable (default) or prerelease
  --bootstrap         Convert the legacy direct-port topology once
  --confirm-maintenance
                      Authorize a short maintenance-window upgrade when the
                      target release contains migrations that cannot run while
                      the old release serves traffic. Run this flag on the
                      local upgrade.sh; the bootstrap does not forward it.
  --yes               Skip version and one-time topology confirmations
  -h, --help          Show this help

State:
  .deployrc           Required. Records the active slot and both slot versions.
  .deploy.lock        Prevents concurrent deploy/update operations.
  deploy/runtime/     Managed blue/green Caddy configuration.

Channels:
  stable             Resolve the newest stable GitHub Release only.
  prerelease         Allow prereleases when resolving "latest".

Prereleases are never selected implicitly: use --channel prerelease or pass an
explicit immutable tag such as v3.0.0-alpha.11. Every choice resolves to a
concrete vX.Y.Z tag before images are pulled; floating "latest" images are
never run.

The updater can apply explicitly declared backward-compatible Core migrations
while the old slot stays online. Undeclared Core or any River migrations still
require ./deploy.sh and a maintenance window.

Examples: latest, v3.0.0-alpha.11, or 3.0.0-alpha.11.
EOF
}

die() {
  printf '%s\n' "$1" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      [ -z "$REQUESTED_VERSION" ] || die "Specify the target version only once."
      REQUESTED_VERSION="$2"
      shift 2
      ;;
    --version=*)
      [ -z "$REQUESTED_VERSION" ] || die "Specify the target version only once."
      REQUESTED_VERSION="${1#*=}"
      shift
      ;;
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      CHANNEL="$2"
      CHANNEL_EXPLICIT=true
      shift 2
      ;;
    --channel=*) CHANNEL="${1#*=}"; CHANNEL_EXPLICIT=true; shift ;;
    --bootstrap) FORCE_BOOTSTRAP=true; shift ;;
    --confirm-maintenance) CONFIRM_MAINTENANCE=true; shift ;;
    --yes|--defaults) ASSUME_YES=true; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) die "Unknown option: $1" ;;
    *)
      [ -z "$REQUESTED_VERSION" ] || die "Specify the target version only once."
      REQUESTED_VERSION="$1"
      VERSION_EXPLICIT=true
      shift
      ;;
  esac
done

case "${REQUESTED_VERSION:-}" in
  "") ;;
  *) VERSION_EXPLICIT=true ;;
esac

REQUESTED_VERSION="${REQUESTED_VERSION:-${SFORUM_VERSION:-}}"
[ "$CHANNEL" = "stable" ] || [ "$CHANNEL" = "prerelease" ] || \
  die "--channel must be stable or prerelease"

rc_value() {
  local key="$1"
  [ -f "$DEPLOY_RC" ] || return 0
  sed -n "s/^${key}=//p" "$DEPLOY_RC" | tail -n 1
}

env_value() {
  local key="$1" fallback="${2:-}" value=""
  if [ -f .env.production ]; then
    value="$(sed -n "s/^${key}=//p" .env.production | tail -n 1)"
  fi
  printf '%s' "${value:-$fallback}"
}

lang() {
  local value
  value="$(rc_value lang)"
  printf '%s' "${value:-zh}"
}

say() {
  local zh="$1" en="$2"
  if [ "$(lang)" = "en" ]; then printf '%s\n' "$en"; else printf '%s\n' "$zh"; fi
}

print_install_location() {
  say "安装目录：$ROOT_DIR" "Install directory: $ROOT_DIR"
  say "配置文件：$ROOT_DIR/.env.production" "Configuration file: $ROOT_DIR/.env.production"
  say "备份目录：$ROOT_DIR/backups" "Backup directory: $ROOT_DIR/backups"
}

rewrite_env_registry() {
  # 只改写 SFORUM_REGISTRY 这一行。用临时文件 + mv 保持 0600 权限，
  # 避免 sed -i 在 GNU/BSD 上的参数差异。
  local prefix="$1" temp_file
  temp_file="$(mktemp "$ROOT_DIR/.env.production.XXXXXX")"
  awk -v prefix="$prefix" '
    /^SFORUM_REGISTRY=/ { print "SFORUM_REGISTRY=" prefix; next }
    { print }
  ' .env.production > "$temp_file"
  chmod 600 "$temp_file"
  mv -f "$temp_file" .env.production
}

resolve_registry() {
  # GitHub 账号改名后，ghcr.io/zhuchunshu 已不再提供新标签。仍固定旧前缀的
  # 安装会在迁移阶段暴露成 `No such image`：pull 用的是新前缀，而
  # `compose run --pull never migrate` 读 .env.production 里的旧前缀。
  # 这里把前缀一次性迁到新命名空间，并让本次运行的 pull/verify 共用它。
  local configured backup stamp
  configured="$(env_value SFORUM_REGISTRY "")"
  configured="${configured%/}"
  case "$configured" in
    ""|"$CANONICAL_REGISTRY")
      REGISTRY="$CANONICAL_REGISTRY"
      ;;
    "$LEGACY_REGISTRY")
      stamp="$(date -u +%Y%m%dT%H%M%SZ)"
      backup="$ROOT_DIR/.env.production.bak.$stamp"
      cp -p .env.production "$backup"
      rewrite_env_registry "$CANONICAL_REGISTRY"
      say \
        "检测到改名前的镜像命名空间 $LEGACY_REGISTRY，已更新为 $CANONICAL_REGISTRY（备份：$backup）。" \
        "Found the pre-rename image namespace $LEGACY_REGISTRY; updated it to $CANONICAL_REGISTRY (backup: $backup)."
      REGISTRY="$CANONICAL_REGISTRY"
      ;;
    *)
      REGISTRY="$configured"
      ;;
  esac
  export SFORUM_REGISTRY="$REGISTRY"
}

validate_version() {
  [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "Version must look like v3.0.0 or v3.0.0-alpha.11"
}

fetch_with_retry() {
  # 用法: fetch_with_retry <描述zh> <描述en> <url> [curl 参数...]
  # 失败自动重试（共 3 次，指数退避），网络偶发抖动不再直接终止更新。
  local desc_zh="$1" desc_en="$2" url="$3" attempt response
  shift 3
  for attempt in 1 2 3; do
    if response="$(curl -fsSL --connect-timeout 10 --max-time 30 "$@" "$url")"; then
      printf '%s' "$response"
      return 0
    fi
    if [ "$attempt" -lt 3 ]; then
      say "查询失败，正在重试（第 $((attempt + 1))/3 次）..." \
        "Request failed; retrying (attempt $((attempt + 1))/3)..." >&2
      sleep $((attempt * 2))
    fi
  done
  say "${desc_zh}" "${desc_en}" >&2
  return 1
}

github_api_headers=(
  -H 'Accept: application/vnd.github+json'
  -H 'X-GitHub-Api-Version: 2022-11-28'
  -H 'User-Agent: SForum-upgrade'
)

resolve_latest_version() {
  local response resolved
  if [ "$CHANNEL" = "stable" ]; then
    say "正在查询最新稳定版..." "Resolving the newest stable GitHub Release..." >&2
    local api_url="${SFORUM_LATEST_RELEASE_API_URL:-https://api.github.com/repos/keeptrue/SForum/releases/latest}"
    response="$(fetch_with_retry \
      "无法查询最新稳定版 Release。请检查网络（或配置 https_proxy 代理），也可使用 --channel prerelease。" \
      "Could not query the latest stable GitHub Release. Check the network (or configure an https_proxy), or use --channel prerelease to allow prereleases." \
      "$api_url" "${github_api_headers[@]}")" || \
      die "GitHub stable release lookup failed."
    resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
    [ -n "$resolved" ] || die "GitHub returned no stable SForum Release. Use --channel prerelease to allow prereleases."
  else
    say "正在查询最新发布版本（含预发布）..." "Resolving the newest published GitHub Release (including prereleases)..." >&2
    # Public unauthenticated listings exclude drafts. Asking for one result keeps
    # the parser dependency-free while still including prereleases.
    local api_url="${SFORUM_RELEASES_API_URL:-https://api.github.com/repos/keeptrue/SForum/releases?per_page=1}"
    response="$(fetch_with_retry \
      "无法查询最新 Release。请检查网络（或配置 https_proxy 代理）后重试。" \
      "Could not query the latest GitHub Release. Check the network (or configure an https_proxy) and retry." \
      "$api_url" "${github_api_headers[@]}")" || \
      die "GitHub release lookup failed."
    resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
    [ -n "$resolved" ] || die "GitHub returned no published SForum Release."
  fi
  validate_version "$resolved"
  printf '%s' "$resolved"
}

select_channel_interactively() {
  # 交互模式下询问更新渠道；显式 --channel 或显式版本号时跳过。选择只对本次运行生效。
  [ "$CHANNEL_EXPLICIT" = true ] && return 0
  [ "$VERSION_EXPLICIT" = true ] && return 0
  [ "$ASSUME_YES" = true ] && return 0
  local choice
  if [ "$(lang)" = en ]; then
    printf '1) Stable (recommended)\n2) Newest release (includes alpha/beta prereleases)\n'
    read -r -p "Update channel [1]: " choice || die "Could not read a channel. Use --channel or --yes for unattended updates."
  else
    printf '1) 稳定版（推荐）\n2) 最新发布版（包含 alpha/beta 预发布）\n'
    read -r -p "请选择更新渠道 [1]: " choice || die "无法读取渠道选择。可使用 --channel 或 --yes 进行无人值守更新。"
  fi
  case "${choice:-1}" in
    ""|1|stable) CHANNEL="stable" ;;
    2|prerelease) CHANNEL="prerelease" ;;
    *) die "Invalid channel selection: $choice" ;;
  esac
}

select_target_version() {
  local normalized
  select_channel_interactively
  if [ -z "$REQUESTED_VERSION" ]; then
    if [ "$ASSUME_YES" != true ]; then
      if [ "$(lang)" = en ]; then
        read -r -p "Release to install [latest]: " REQUESTED_VERSION || \
          die "Could not read a release. Use --version or --yes for unattended updates."
      else
        read -r -p "请输入要更新的版本 [latest]: " REQUESTED_VERSION || \
          die "Could not read a release. Use --version or --yes for unattended updates."
      fi
      case "$REQUESTED_VERSION" in
        "") ;;
        latest) ;;
        *) VERSION_EXPLICIT=true ;;
      esac
    fi
  fi
  REQUESTED_VERSION="${REQUESTED_VERSION:-latest}"
  if [ "$REQUESTED_VERSION" != latest ]; then VERSION_EXPLICIT=true; fi
  if [ "$REQUESTED_VERSION" = latest ]; then
    TARGET_VERSION="$(resolve_latest_version)"
  else
    normalized="$REQUESTED_VERSION"
    if [[ "$normalized" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
      normalized="v$normalized"
    fi
    validate_version "$normalized"
    TARGET_VERSION="$normalized"
  fi
}

confirm_target_version() {
  local current_version="$1" confirmation channel_note=""
  [ "$CHANNEL" = prerelease ] && channel_note=" (prerelease)"
  say "当前实际版本：$current_version" "Current resolved version: $current_version"
  say "目标实际版本：$TARGET_VERSION$channel_note" "Target resolved version: $TARGET_VERSION$channel_note"
  [ "$ASSUME_YES" = true ] && return 0
  if [ "$(lang)" = en ]; then
    read -r -p "Update to $TARGET_VERSION? [Y/n]: " confirmation || \
      die "Could not read confirmation; use --yes to continue unattended."
  else
    read -r -p "确认更新到 $TARGET_VERSION？[Y/n]: " confirmation || \
      die "Could not read confirmation; use --yes to continue unattended."
  fi
  case "$confirmation" in
    ""|y|Y|yes|YES|Yes) ;;
    *) die "Canceled." ;;
  esac
}

acquire_lock() {
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    # 锁自愈：持有进程已退出或锁龄超过 1 小时时清理残留锁，而不是永久拒绝。
    local lock_pid lock_age=false
    if [ -f "$LOCK_DIR/pid" ]; then
      lock_pid="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
      if [ -n "$lock_pid" ] && ! kill -0 "$lock_pid" 2>/dev/null; then
        lock_age=true
      fi
    fi
    if [ "$lock_age" = true ]; then
      : # 持有进程已死
    else
      local lock_mtime now mtime
      lock_mtime="$(stat -c %Y "$LOCK_DIR" 2>/dev/null || stat -f %m "$LOCK_DIR" 2>/dev/null || echo 0)"
      now="$(date +%s)"
      if [ "$lock_mtime" -gt 0 ] && [ $((now - lock_mtime)) -gt 3600 ]; then
        lock_age=true
      fi
    fi
    if [ "$lock_age" = true ]; then
      say "检测到残留的部署锁（持有进程已退出或锁龄超过 1 小时），已自动清理。" \
        "Detected a stale deployment lock (holder exited or older than 1 hour); removed it automatically."
      rm -rf "$LOCK_DIR"
      mkdir "$LOCK_DIR" 2>/dev/null || die "Another deployment or update operation is running."
    else
      die "Another deployment or update operation is running."
    fi
  fi
  printf '%s\n' "$$" > "$LOCK_DIR/pid"
  trap 'release_lock' EXIT HUP INT TERM
}

release_lock() {
  rm -rf "$LOCK_DIR" 2>/dev/null || true
  trap - EXIT HUP INT TERM
}

configure_versions() {
  local active_slot="$1" active_version="$2" inactive_slot="$3" inactive_version="$4"
  if [ "$active_slot" = blue ]; then
    export SFORUM_BLUE_VERSION="$active_version"
    export SFORUM_GREEN_VERSION="$inactive_version"
  else
    export SFORUM_GREEN_VERSION="$active_version"
    export SFORUM_BLUE_VERSION="$inactive_version"
  fi
}

init_compose() {
  COMPOSE=(
    docker compose --env-file .env.production
    -f compose.yaml -f compose.prod.yaml -f compose.release.yaml
    -f compose.zero-downtime.yaml --profile zero-downtime
  )
}

pull_target_images() {
  local version="$1" image attempt ok
  say "正在拉取目标版本镜像：$version" "Pulling target release images: $version"
  for image in api migrate web; do
    ok=false
    for attempt in 1 2 3; do
      if docker pull "${REGISTRY}/sforum-${image}:${version}"; then
        ok=true
        break
      fi
      if [ "$attempt" -lt 3 ]; then
        say "镜像 $image 拉取失败，正在重试（第 $((attempt + 1))/3 次）..." \
          "Image $image pull failed; retrying (attempt $((attempt + 1))/3)..."
        sleep $((attempt * 2))
      fi
    done
    [ "$ok" = true ] || die "Could not pull ${REGISTRY}/sforum-${image}:${version} after 3 attempts."
  done
}

verify_target_images() {
  local version="$1" expected="${1#v}" image actual
  for image in api migrate web; do
    actual="$(docker image inspect "${REGISTRY}/sforum-${image}:${version}" \
      --format '{{index .Config.Labels "org.opencontainers.image.version"}}')"
    [ "$actual" = "$expected" ] || die "$image image version is '$actual', expected '$expected'"
  done
}

target_migrator_supports_online_safe_check() {
  local version="$1"
  local capability
  capability="$(docker image inspect "${REGISTRY}/sforum-migrate:${version}" \
    --format '{{index .Config.Labels "io.sforum.migrations.online-safe-check"}}' 2>/dev/null || true)"
  [ "$capability" = v1 ]
}

ONLINE_MIGRATION_REQUIRED=false
MAINTENANCE_MIGRATION_REQUIRED=false

plan_schema_for_online_update() {
  local version="$1"
  ONLINE_MIGRATION_REQUIRED=false
  say "正在检查目标版本的数据库兼容性..." "Checking the target database compatibility..."
  if SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-no-pending >/dev/null 2>&1; then
    return 0
  fi
  if ! target_migrator_supports_online_safe_check "$version"; then
    MAINTENANCE_MIGRATION_REQUIRED=true
    return 1
  fi
  if ! SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-online-safe; then
    MAINTENANCE_MIGRATION_REQUIRED=true
    return 1
  fi
  ONLINE_MIGRATION_REQUIRED=true
}

handle_unsafe_online_migrations() {
  # 目标版本包含无法在线安全执行的迁移：交互确认后原地降级为停机升级；
  # 非交互保持拒绝但给出可复制的授权命令，避免无人值守静默停机。
  if [ "$CONFIRM_MAINTENANCE" = true ]; then
    say "已获得停机升级授权，将在短暂维护窗口内完成迁移。" \
      "Maintenance-window upgrade authorized; migrations will run in a short downtime window."
    return 0
  fi
  if [ "$ASSUME_YES" = true ]; then
    say \
      "目标版本包含无法在线安全执行的迁移。无人值守模式不会自动停机；请执行：./upgrade.sh --version $TARGET_VERSION --confirm-maintenance" \
      "The target release has migrations that are not safe online. Unattended mode will not trigger downtime automatically; run: ./upgrade.sh --version $TARGET_VERSION --confirm-maintenance"
    die "Maintenance-window confirmation required."
  fi
  local confirmation
  say \
    "目标版本包含无法在旧版本继续服务时安全执行的数据库迁移，需要短暂停机完成升级。" \
    "The target release has migrations that cannot run while the old release serves traffic; a short maintenance window is required."
  if [ "$(lang)" = en ]; then
    read -r -p "Continue with a maintenance-window upgrade? [y/N]: " confirmation || die "Canceled."
  else
    read -r -p "是否继续进行停机升级？[y/N]: " confirmation || die "Canceled."
  fi
  case "$confirmation" in
    y|Y|yes|YES|Yes) return 0 ;;
    *) die "Canceled. Run ./deploy.sh or retry with --confirm-maintenance." ;;
  esac
}

maintenance_update() {
  local active_slot="$1" current_version="$2" target_version="$3"
  local inactive_slot blue_version green_version
  if [ "$active_slot" = blue ]; then inactive_slot=green; else inactive_slot=blue; fi
  configure_versions "$active_slot" "$current_version" "$inactive_slot" "$target_version"
  blue_version="$SFORUM_BLUE_VERSION"
  green_version="$SFORUM_GREEN_VERSION"

  pull_target_images "$target_version"
  verify_target_images "$target_version"
  backup_database

  local old_api old_web new_api new_web
  old_api="$(slot_api "$active_slot")"
  old_web="$(slot_web "$active_slot")"
  new_api="$(slot_api "$inactive_slot")"
  new_web="$(slot_web "$inactive_slot")"

  say "进入维护窗口：停止当前槽并执行完整数据库迁移..." \
    "Entering the maintenance window: stopping the active slot and running all migrations..."
  "${COMPOSE[@]}" stop "$old_web" "$old_api"

  if ! SFORUM_VERSION="$target_version" "${COMPOSE[@]}" run --rm -T --pull never migrate; then
    say "迁移失败，正在恢复旧版本槽..." "Migration failed; restoring the previous slot..."
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Migration failed. The previous slot was restarted; restore the pre-upgrade backup if the schema is now inconsistent."
  fi

  say "迁移完成，正在启动新版本槽..." "Migrations complete; starting the target slot..."
  if ! "${COMPOSE[@]}" up -d --no-build --pull never "$new_api" "$new_web"; then
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Could not start the target slot; the previous slot was restarted."
  fi
  if ! wait_inside_slot "$inactive_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Target slot failed its health checks after migration; the previous slot was restarted."
  fi

  if ! switch_router "$inactive_slot" "$active_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Ingress switch failed after maintenance migration; the previous slot was restarted."
  fi

  "${COMPOSE[@]}" stop "$old_web" "$old_api" || true
  remove_legacy_worker_containers || true
  persist_state "$inactive_slot" "$target_version" "$blue_version" "$green_version"
  say "停机升级完成：$current_version -> $target_version" \
    "Maintenance-window upgrade completed: $current_version -> $target_version"
  print_install_location
}

apply_planned_online_migrations() {
  local version="$1"
  [ "$ONLINE_MIGRATION_REQUIRED" = true ] || return 0
  say \
    "正在保持当前槽在线并执行已声明兼容的数据库迁移..." \
    "Applying declared backward-compatible migrations while the active slot stays online..."
  SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --pull never migrate
  SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-no-pending >/dev/null || \
    die "Online migrations ran but the target schema is still not exact. Keep the current slot online and inspect the migrator logs."
}

backup_database() {
  say "在线切换前创建 PostgreSQL 备份..." "Creating a PostgreSQL backup before the online switch..."
  ./deploy/scripts/backup-postgres.sh
}

slot_api() { printf 'api-%s' "$1"; }
slot_web() { printf 'web-%s' "$1"; }
legacy_worker_container_ids() {
  local project service
  project="${COMPOSE_PROJECT_NAME:-$(env_value COMPOSE_PROJECT_NAME sforum)}"
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

wait_inside_slot() {
  local slot="$1" deadline=$((SECONDS + ${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120}))
  local api web last_failure=""
  api="$(slot_api "$slot")"
  web="$(slot_web "$slot")"
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! "${COMPOSE[@]}" exec -T "$api" wget -q -O /dev/null http://127.0.0.1:8080/api/v1/ready; then
      last_failure="api ready ($api)"
    elif ! "${COMPOSE[@]}" exec -T "$web" wget -q -O /dev/null http://127.0.0.1:3000/health; then
      last_failure="web health ($web)"
    elif ! "${COMPOSE[@]}" exec -T "$web" wget -q -O /dev/null http://127.0.0.1:3000/api/v1/ready; then
      last_failure="web api ready ($web)"
    else
      return 0
    fi
    sleep 2
  done
  say "健康检查超时（${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120} 秒），最后未通过的检查：$last_failure" \
    "Health check timed out after ${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120}s; the last failing probe was: $last_failure" >&2
  return 1
}

write_router_config() {
  local slot="$1" destination="$2"
  umask 077
  mkdir -p "$RUNTIME_DIR"
  {
    printf ':3000 {\n'
    printf '\theader X-SForum-Active-Slot %s\n' "$slot"
    # 公开只读资产：QQ 内置浏览器（X5 云加速）、CDN 换源后仍要能加载。
    # 应用侧已发同名头，这里是 ingress 冗余；defer 必须保留，否则上游
    # same-origin 会再追加一份，响应里出现两个 CORP 值。
    printf '\t@cors_assets path /_nuxt/* /_sforum/*\n'
    printf '\theader @cors_assets {\n'
    printf '\t\tdefer\n'
    printf '\t\tAccess-Control-Allow-Origin "*"\n'
    printf '\t\tTiming-Allow-Origin "*"\n'
    printf '\t\tCross-Origin-Resource-Policy "cross-origin"\n'
    printf '\t}\n'
    printf '\treverse_proxy web-%s:3000\n' "$slot"
    printf '}\n\n'
    printf ':18080 {\n'
    printf '\theader X-SForum-Active-Slot %s\n' "$slot"
    printf '\treverse_proxy api-%s:8080\n' "$slot"
    printf '}\n'
  } > "$destination"
  chmod 600 "$destination"
}

wait_stable_ingress() {
  local slot="$1" web_port api_port deadline header_file
  web_port="$(env_value WEB_PORT 3000)"
  api_port="$(env_value API_PORT 18080)"
  deadline=$((SECONDS + ${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120}))
  header_file="$(mktemp "${TMPDIR:-/tmp}/sforum-upgrade-headers.XXXXXX")"
  while [ "$SECONDS" -lt "$deadline" ]; do
    if curl -fsS -D "$header_file" -o /dev/null "http://127.0.0.1:${web_port}/health" \
      && grep -Eiq "^X-SForum-Active-Slot:[[:space:]]*${slot}[[:space:]]*$" "$header_file" \
      && curl -fsS -D "$header_file" -o /dev/null "http://127.0.0.1:${api_port}/api/v1/ready" \
      && grep -Eiq "^X-SForum-Active-Slot:[[:space:]]*${slot}[[:space:]]*$" "$header_file"; then
      rm -f "$header_file"
      return 0
    fi
    sleep 1
  done
  rm -f "$header_file"
  return 1
}

switch_router() {
  local target_slot="$1" previous_slot="$2"
  local next="$RUNTIME_DIR/Caddyfile.next" current="$RUNTIME_DIR/Caddyfile" previous="$RUNTIME_DIR/Caddyfile.previous"
  write_router_config "$target_slot" "$next"
  "${COMPOSE[@]}" exec -T edge caddy validate --config /etc/sforum-router/Caddyfile.next --adapter caddyfile >/dev/null
  cp "$current" "$previous"
  mv -f "$next" "$current"
  if ! "${COMPOSE[@]}" exec -T edge caddy reload --config /etc/sforum-router/Caddyfile --adapter caddyfile >/dev/null; then
    mv -f "$previous" "$current"
    "${COMPOSE[@]}" exec -T edge caddy reload --config /etc/sforum-router/Caddyfile --adapter caddyfile >/dev/null || true
    return 1
  fi
  if wait_stable_ingress "$target_slot"; then
    rm -f "$previous"
    return 0
  fi
  mv -f "$previous" "$current"
  "${COMPOSE[@]}" exec -T edge caddy reload --config /etc/sforum-router/Caddyfile --adapter caddyfile >/dev/null || true
  wait_stable_ingress "$previous_slot" || true
  return 1
}

persist_state() {
  local active_slot="$1" active_version="$2" blue_version="$3" green_version="$4" temp_file
  validate_version "$active_version"
  validate_version "$blue_version"
  validate_version "$green_version"
  temp_file="$(mktemp "$ROOT_DIR/.deployrc.XXXXXX")"
  umask 077
  printf 'lang=%s\nversion=%s\nmode=release\ntopology=blue-green\nactive_slot=%s\nblue_version=%s\ngreen_version=%s\ninstall_dir=%s\n' \
    "$(lang)" "$active_version" "$active_slot" "$blue_version" "$green_version" "$ROOT_DIR" > "$temp_file"
  chmod 600 "$temp_file"
  mv -f "$temp_file" "$DEPLOY_RC"
}

bootstrap_topology() {
  local current_version="$1" target_version="$2" slot=blue
  local legacy_services=(api web)
  if [ "$FORCE_BOOTSTRAP" != true ] && [ "$ASSUME_YES" != true ]; then
    say \
      "这是旧版直连端口拓扑。首次转换路由器会有一次短暂维护窗口；后续兼容更新可保持 HTTP 在线。输入 BOOTSTRAP 继续：" \
      "This is the legacy direct-port topology. The one-time router conversion has a short maintenance window; later compatible updates keep HTTP online. Type BOOTSTRAP to continue:"
    local confirmation
    read -r confirmation
    [ "$confirmation" = BOOTSTRAP ] || die "Canceled."
  fi

  configure_versions blue "$target_version" green "$target_version"
  pull_target_images "$target_version"
  docker pull caddy:2.10.2-alpine
  verify_target_images "$target_version"
  "${COMPOSE[@]}" up -d --wait postgres redis
  if ! plan_schema_for_online_update "$target_version"; then
    # 首次拓扑转换本来就是维护窗口，可直接执行全部迁移。
    say "目标版本需要停机迁移；本次拓扑转换将一并完成。" \
      "The target release requires offline migrations; this topology conversion will apply them."
    backup_database
    SFORUM_VERSION="$target_version" "${COMPOSE[@]}" run --rm -T --pull never migrate
    "${COMPOSE[@]}" up -d --no-build --pull never api-blue web-blue
  else
    backup_database
    apply_planned_online_migrations "$target_version"
    "${COMPOSE[@]}" up -d --no-build --pull never api-blue web-blue
  fi
  wait_inside_slot blue || die "Blue candidate failed its internal API/Web checks."

  write_router_config blue "$RUNTIME_DIR/Caddyfile"
  say "正在执行一次性入口转换..." "Performing the one-time ingress conversion..."
  "${COMPOSE[@]}" stop "${legacy_services[@]}"
  remove_legacy_worker_containers || die "Could not remove legacy standalone Worker containers."
  if ! "${COMPOSE[@]}" up -d --no-build --pull never edge; then
    "${COMPOSE[@]}" start "${legacy_services[@]}" || true
    die "Could not start the stable router; legacy services were restarted."
  fi
  if ! wait_stable_ingress blue; then
    "${COMPOSE[@]}" stop edge || true
    "${COMPOSE[@]}" start "${legacy_services[@]}" || true
    die "Router health check failed; legacy services were restarted."
  fi
  persist_state blue "$target_version" "$target_version" "$target_version"
  say "双槽入口已启用。后续兼容版本可保持 HTTP 在线切换。" "Blue/green ingress is enabled. Later compatible releases can switch without HTTP downtime."
}

online_update() {
  local active_slot="$1" current_version="$2" target_version="$3"
  local inactive_slot old_api old_web new_api new_web blue_version green_version
  if [ "$active_slot" = blue ]; then inactive_slot=green; else inactive_slot=blue; fi
  configure_versions "$active_slot" "$current_version" "$inactive_slot" "$target_version"
  blue_version="$SFORUM_BLUE_VERSION"
  green_version="$SFORUM_GREEN_VERSION"
  old_api="$(slot_api "$active_slot")"
  old_web="$(slot_web "$active_slot")"
  new_api="$(slot_api "$inactive_slot")"
  new_web="$(slot_web "$inactive_slot")"

  pull_target_images "$target_version"
  verify_target_images "$target_version"
  if ! plan_schema_for_online_update "$target_version"; then
    handle_unsafe_online_migrations
    maintenance_update "$active_slot" "$current_version" "$target_version"
    return 0
  fi
  backup_database
  apply_planned_online_migrations "$target_version"

  say "正在启动并检查备用槽 ${inactive_slot}..." "Starting and checking standby slot ${inactive_slot}..."
  "${COMPOSE[@]}" up -d --no-build --pull never "$new_api" "$new_web"
  if ! wait_inside_slot "$inactive_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    die "Standby slot failed internal health checks; live traffic was not changed."
  fi

  say "正在原子切换 Web/API 流量..." "Atomically switching Web/API traffic..."
  if ! switch_router "$inactive_slot" "$active_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    die "Ingress switch failed and was rolled back to the active slot."
  fi

  remove_legacy_worker_containers || die "Could not remove legacy standalone Worker containers."
  "${COMPOSE[@]}" stop "$old_web" "$old_api"
  persist_state "$inactive_slot" "$target_version" "$blue_version" "$green_version"
  say "零停机更新完成：$current_version -> $target_version" "Zero-downtime update completed: $current_version -> $target_version"
  print_install_location
}

infer_running_version() {
  # .deployrc 的版本缺失或非法时，从运行中的槽容器镜像 label 反查自愈。
  local slot label version
  for slot in blue green; do
    label="$(docker inspect "api-$slot" \
      --format '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null || true)"
    if [ -n "$label" ]; then
      version="v$label"
      if [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
        printf '%s' "$version"
        return 0
      fi
    fi
  done
  return 1
}

main() {
  [ -f .env.production ] || die ".env.production is missing; run ./deploy.sh first."
  [ -f "$DEPLOY_RC" ] || die ".deployrc is missing; run ./deploy.sh first."
  command -v curl >/dev/null 2>&1 || die "curl is required."

  local current_version active_slot
  current_version="$(rc_value version)"
  if [ -z "$current_version" ]; then
    command -v docker >/dev/null 2>&1 || die "Docker is required."
    docker info >/dev/null 2>&1 || die "Docker daemon is not running."
    if current_version="$(infer_running_version)"; then
      say ".deployrc 缺少版本记录，已从运行容器推断当前版本：$current_version" \
        ".deployrc has no version record; inferred the running version from containers: $current_version"
    else
      die "The currently deployed version is unknown; re-run ./deploy.sh to restore state."
    fi
  fi
  if ! [[ "$current_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 && \
      current_version="$(infer_running_version)" || true
  fi
  validate_version "$current_version"
  select_target_version
  confirm_target_version "$current_version"

  command -v docker >/dev/null 2>&1 || die "Docker is required."
  docker info >/dev/null 2>&1 || die "Docker daemon is not running."
  resolve_registry
  export SFORUM_VERSION="$TARGET_VERSION"
  init_compose
  acquire_lock
  active_slot="$(rc_value active_slot)"
  if [ -z "$active_slot" ]; then
    bootstrap_topology "$current_version" "$TARGET_VERSION"
  else
    [ "$active_slot" = blue ] || [ "$active_slot" = green ] || die "Invalid active_slot in .deployrc"
    online_update "$active_slot" "$current_version" "$TARGET_VERSION"
  fi
  release_lock
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
