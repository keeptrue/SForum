#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

# 终端配色层：唯一实现是 deploy/scripts/term-ui.sh（随部署包分发、由 bootstrap 提升）。
# 颜色只在打印点生效：say() 的返回值、.deployrc、.env.production、DEPLOY_LOCK_LOG
# 一律保持无 ESC。
# shellcheck source=deploy/scripts/term-ui.sh
source "$ROOT_DIR/deploy/scripts/term-ui.sh"

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
  ui_err "$1"
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

# 存量安装的「后补键」。deploy.sh 里有一份等价实现：两个脚本各自独立分发，
# 共用同一套语义 —— 只在键缺失或为空时补一行，已有非空值一字不动，文件不存在
# 时什么都不做，临时文件 + 原子 mv 保持 0600，连续跑两次不会出现重复键。
# compose.prod.yaml 对 MEILI_MASTER_KEY 加了 `:?` 硬闸，而向导只在配置文件缺失
# 时生成它（configure-production.sh 的 exists 分支直接 exit 0），所以升级路径
# 必须自己补，并且必须在第一个 compose 调用之前完成。
ENV_BACKFILL_KEYS=(MEILI_MASTER_KEY)

generate_env_secret() {
  local value
  if command -v openssl >/dev/null 2>&1; then
    value="$(openssl rand -hex 32)"
  else
    value="$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')"
  fi
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || die "Failed to generate a secure random secret."
  printf '%s' "$value"
}

backfill_env_keys() {
  local file="${1:-.env.production}"
  local key value temp_file drop
  local missing=()

  [ -f "$file" ] || return 0
  for key in "${ENV_BACKFILL_KEYS[@]}"; do
    value="$(sed -n "s/^${key}=//p" "$file" | tail -n 1)"
    [ -n "$value" ] || missing+=("$key")
  done
  [ "${#missing[@]}" -gt 0 ] || return 0

  umask 077
  temp_file="$(mktemp "$(dirname "$file")/.env.production.XXXXXX")"
  drop="$(IFS=,; printf '%s' "${missing[*]}")"
  awk -v drop="$drop" '
    BEGIN { n = split(drop, keys, ","); for (i = 1; i <= n; i++) skip[keys[i]] = 1 }
    { for (k in skip) if (index($0, k "=") == 1) next; print }
  ' "$file" > "$temp_file"
  for key in "${missing[@]}"; do
    printf '%s=%s\n' "$key" "$(generate_env_secret)" >> "$temp_file"
  done
  chmod 600 "$temp_file"
  mv -f "$temp_file" "$file"
  warn "已为 .env.production 补上缺失的生产配置键：${missing[*]}" \
    "Added missing production key(s) to .env.production: ${missing[*]}"
}

# 备份加密口令自举（一键脚本）。升级在切换前必做一次加密备份，而这条路径以前
# 只读进程环境：运维没 export 就停在「创建 PostgreSQL 备份」这一步。这里把
# 「缺失」变成「自动生成 + 持久化」，加密强度不变（32 字节口令、0600 文件、
# 绝不写明文归档）。deploy.sh 里有一份等价实现（两个脚本各自独立分发）。
BACKUP_KEY_DEFAULT_REL=".sforum/backup.key"

# 只解析、不生成：取值顺序与 backup-postgres.sh 的判定一致 —— 内联
# SFORUM_BACKUP_KEY 优先，其次 SFORUM_BACKUP_KEY_FILE；两者都只认 shell 环境
# 或 .env.production 的字面值（不 source、不 eval）。
resolve_backup_key() {
  if [ -z "${SFORUM_BACKUP_KEY_FILE:-}" ] && [ -z "${SFORUM_BACKUP_KEY:-}" ]; then
    SFORUM_BACKUP_KEY_FILE="$(env_value SFORUM_BACKUP_KEY_FILE)"
    SFORUM_BACKUP_KEY="$(env_value SFORUM_BACKUP_KEY)"
  fi
  export SFORUM_BACKUP_KEY_FILE SFORUM_BACKUP_KEY
}

# 把口令路径写回 .env.production：先丢掉 `KEY=` 与 `# KEY=` 两种旧行（模板里是
# 注释形式），再在末尾补一行；临时文件 + 原子 mv 保持 0600，连续跑两次不会出现
# 重复键。文件不存在时什么都不做 —— 缺配置由调用方自己报错。
persist_backup_key_file() {
  local key_file="$1" temp_file

  [ -f .env.production ] || return 0
  if [ "$(env_value SFORUM_BACKUP_KEY_FILE)" = "$key_file" ]; then
    return 0
  fi
  temp_file="$(mktemp "$ROOT_DIR/.env.production.XXXXXX")"
  awk '
    /^SFORUM_BACKUP_KEY_FILE=/ { next }
    /^# *SFORUM_BACKUP_KEY_FILE=/ { next }
    { print }
  ' .env.production > "$temp_file" || {
    rm -f "$temp_file"
    die "Could not rewrite .env.production for SFORUM_BACKUP_KEY_FILE."
  }
  printf 'SFORUM_BACKUP_KEY_FILE=%s\n' "$key_file" >> "$temp_file" || {
    rm -f "$temp_file"
    die "Could not rewrite .env.production for SFORUM_BACKUP_KEY_FILE."
  }
  chmod 600 "$temp_file"
  mv -f "$temp_file" .env.production
}

# 解析 → 缺失则生成。已有文件只收紧权限、绝不改内容；内联口令存在时一个字节
# 都不落盘。相对路径先按安装目录解析成绝对路径，免得 dirname 得到 "." 之后把
# chmod 700 打到安装目录本身。
ensure_backup_key() {
  local key_file dir key_value

  resolve_backup_key
  [ -z "${SFORUM_BACKUP_KEY:-}" ] || return 0
  key_file="${SFORUM_BACKUP_KEY_FILE:-$ROOT_DIR/$BACKUP_KEY_DEFAULT_REL}"
  case "$key_file" in
    /*) ;;
    *) key_file="$ROOT_DIR/${key_file#./}" ;;
  esac
  [ ! -d "$key_file" ] || die "备份加密口令路径是一个目录：$key_file"

  if [ -s "$key_file" ]; then
    chmod 600 "$key_file" 2>/dev/null || true
    export SFORUM_BACKUP_KEY_FILE="$key_file"
    persist_backup_key_file "$key_file"
    return 0
  fi

  dir="$(dirname "$key_file")"
  if [ ! -d "$dir" ]; then
    mkdir -p "$dir" || die "无法创建备份加密口令目录：$dir"
    chmod 700 "$dir" || die "无法收紧备份加密口令目录权限：$dir"
  fi
  key_value="$(generate_env_secret)" || die "Failed to generate a secure random secret."
  ( umask 077; printf '%s\n' "$key_value" > "$key_file" ) || \
    die "无法写入备份加密口令文件：$key_file"
  chmod 600 "$key_file" || die "无法设置备份加密口令文件权限：$key_file"
  export SFORUM_BACKUP_KEY_FILE="$key_file"
  persist_backup_key_file "$key_file"
  warn "未找到备份加密口令，已在 $key_file 生成一个（0600）并写入 .env.production。请把它复制到与备份归档不同的位置：丢掉它等于丢掉全部加密备份。" \
    "No backup encryption key was configured; generated one at $key_file (0600) and recorded it in .env.production. Copy it somewhere other than the disk that holds the archives: losing it makes every encrypted backup unrecoverable."
}

lang() {
  local value
  value="$(rc_value lang)"
  printf '%s' "${value:-zh}"
}

# 纯文本出口：语言选择只在这里。任何被 $(...) 捕获或写进状态文件的调用都必须继续
# 走 say()/lock_text()，它们的返回值不能带 ESC。
_say_text() {
  local zh="$1" en="$2"
  if [ "$(lang)" = "en" ]; then printf '%s' "$en"; else printf '%s' "$zh"; fi
}

say() {
  _say_text "$1" "$2"
  printf '\n'
}

step() { ui_step "$(_say_text "$1" "$2")"; }
key() { ui_key "$(_say_text "$1" "$2")"; }
ok() { ui_ok "$(_say_text "$1" "$2")"; }
warn() { ui_warn "$(_say_text "$1" "$2")"; }
err() { ui_err "$(_say_text "$1" "$2")"; }

# 部署锁与 deploy.sh 共用 deploy/scripts/deploy-lock.sh 的同一套判定：pid 存活用
# ps -p 兜底（跨用户 kill -0 会 EPERM），并用 lstart 识别被复用的 pid。
DEPLOY_LOCK_ROOT="$ROOT_DIR"
lock_text() {
  case "$1" in
    deploy_locked) say "另一个部署或更新操作正在运行。" "Another deployment or update operation is running." ;;
    lock_recovered) say "检测到残留的部署锁（持有进程已退出或锁龄超过 1 小时），已自动清理。" "Detected a stale deployment lock (holder exited or older than 1 hour); removed it automatically." ;;
    unlock_none) say "当前没有部署锁。" "No deployment lock is present." ;;
    unlock_done) say "已强制移除部署锁。原持有者：" "Removed the deployment lock by force. Previous holder:" ;;
    *) printf '%s' "$1" ;;
  esac
}
# shellcheck source=deploy/scripts/deploy-lock.sh
source "$ROOT_DIR/deploy/scripts/deploy-lock.sh"

print_install_location() {
  key "安装目录：$ROOT_DIR" "Install directory: $ROOT_DIR"
  key "配置文件：$ROOT_DIR/.env.production" "Configuration file: $ROOT_DIR/.env.production"
  key "备份目录：$ROOT_DIR/backups" "Backup directory: $ROOT_DIR/backups"
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
      warn \
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

retry_backoff_seconds() {
  local attempt="$1" base="${SFORUM_UPGRADE_RETRY_SLEEP_SECONDS:-2}"
  [[ "$base" =~ ^[0-9]+$ ]] || \
    die "SFORUM_UPGRADE_RETRY_SLEEP_SECONDS must be a non-negative integer: $base"
  printf '%s' "$((attempt * base))"
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
      warn "查询失败，正在重试（第 $((attempt + 1))/3 次）..." \
        "Request failed; retrying (attempt $((attempt + 1))/3)..." >&2
      sleep "$(retry_backoff_seconds "$attempt")"
    fi
  done
  err "${desc_zh}" "${desc_en}" >&2
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
    step "正在查询最新稳定版..." "Resolving the newest stable GitHub Release..." >&2
    local api_url="${SFORUM_LATEST_RELEASE_API_URL:-https://api.github.com/repos/keeptrue/SForum/releases/latest}"
    response="$(fetch_with_retry \
      "无法查询最新稳定版 Release。请检查网络（或配置 https_proxy 代理），也可使用 --channel prerelease。" \
      "Could not query the latest stable GitHub Release. Check the network (or configure an https_proxy), or use --channel prerelease to allow prereleases." \
      "$api_url" "${github_api_headers[@]}")" || \
      die "GitHub stable release lookup failed."
    resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
    [ -n "$resolved" ] || die "GitHub returned no stable SForum Release. Use --channel prerelease to allow prereleases."
  else
    step "正在查询最新发布版本（含预发布）..." "Resolving the newest published GitHub Release (including prereleases)..." >&2
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
  key "当前实际版本：$current_version" "Current resolved version: $current_version"
  key "目标实际版本：$TARGET_VERSION$channel_note" "Target resolved version: $TARGET_VERSION$channel_note"
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

# RV-43：镜像身份必须是内容 digest，不是 org.opencontainers.image.version 标签。
# caddy 在本文件与 compose.zero-downtime.yaml 两处钉死同一个 digest（测试钉住一致）；
# api/migrate/web 在拉取前把可变 tag 解析成 manifest digest，拉取后再核对本地
# RepoDigests 是否仍是那个 digest —— 标签被重推即 die，不会静默跑别的镜像。
CADDY_IMAGE_DEFAULT="caddy:2.11-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0"

caddy_image() {
  # 与 compose 的插值顺序一致：shell 覆盖 > .env.production > 默认 digest。
  local configured="${SFORUM_CADDY_IMAGE:-}"
  if [ -z "$configured" ]; then
    configured="$(env_value SFORUM_CADDY_IMAGE "$CADDY_IMAGE_DEFAULT")"
  fi
  printf '%s' "${configured:-$CADDY_IMAGE_DEFAULT}"
}

PULLED_IMAGE_DIGESTS=""

record_image_digest() {
  PULLED_IMAGE_DIGESTS="${PULLED_IMAGE_DIGESTS}${1} ${2}"$'\n'
}

recorded_image_digest() {
  printf '%s' "$PULLED_IMAGE_DIGESTS" | awk -v service="$1" '$1 == service { print $2; exit }'
}

resolve_image_digest() {
  local image="$1" digest
  digest="$(docker buildx imagetools inspect "$image" \
    --format '{{json .Manifest.Digest}}' 2>/dev/null | tr -d '"[:space:]' || true)"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  printf '%s' "$digest"
}

pull_with_retry() {
  local reference="$1" label="$2" attempt
  for attempt in 1 2 3; do
    if docker pull "$reference"; then
      return 0
    fi
    if [ "$attempt" -lt 3 ]; then
      warn "镜像 $label 拉取失败，正在重试（第 $((attempt + 1))/3 次）..." \
        "Image $label pull failed; retrying (attempt $((attempt + 1))/3)..."
      sleep "$(retry_backoff_seconds "$attempt")"
    fi
  done
  return 1
}

pull_target_images() {
  local version="$1" image reference digest
  step "正在拉取目标版本镜像：$version" "Pulling target release images: $version"
  PULLED_IMAGE_DIGESTS=""
  for image in api migrate web; do
    reference="${REGISTRY}/sforum-${image}:${version}"
    digest="$(resolve_image_digest "$reference")" || \
      die "Could not resolve the manifest digest of $reference. Docker with the buildx plugin (docker buildx imagetools) is required to verify image identity."
    record_image_digest "$image" "$digest"
    pull_with_retry "$reference" "$image" || \
      die "Could not pull $reference after 3 attempts."
  done
}

verify_target_images() {
  local version="$1" expected="${1#v}" image reference digest digests label
  for image in api migrate web; do
    reference="${REGISTRY}/sforum-${image}:${version}"
    digest="$(recorded_image_digest "$image")"
    [ -n "$digest" ] || \
      die "No verified manifest digest was recorded for $reference; pull the target release before verifying it."
    digests="$(docker image inspect "$reference" --format '{{json .RepoDigests}}' 2>/dev/null || true)"
    case "$digests" in
      *"$digest"*) ;;
      *) die "$image image content identity mismatch: $reference does not resolve to $digest" ;;
    esac
    # 版本标签保留为附加自检：它证明镜像自称的发布线，但不再承担身份职责。
    label="$(docker image inspect "$reference" \
      --format '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null || true)"
    [ "$label" = "$expected" ] || die "$image image version is '$label', expected '$expected'"
  done
}

# 基础镜像也必须显式拉。后面的 `up -d --pull never` 不会联网，而 compose 文件里
# 基础镜像 tag 一变（v3.0.23 的 redis:7.4.10-alpine → 8.10.2-alpine），compose 就要
# 重建容器，重建拿不到镜像直接 `No such image`，接着依赖 redis 的 api 槽起不来，
# 报出来的却是一句无关的 `dependency failed to start`。升级路径以前只拉 api /
# migrate / web 三个发布镜像，基础镜像全靠「本机碰巧有」。
pull_infra_images() {
  step "正在拉取基础服务镜像：postgres / redis" "Pulling infrastructure images: postgres / redis"
  "${COMPOSE[@]}" pull postgres redis || \
    die "Could not pull the postgres/redis images; the target slot cannot start without them."
}

verify_caddy_image() {
  local image digest digests
  image="$(caddy_image)"
  case "$image" in
    *@sha256:*) ;;
    *) die "SFORUM_CADDY_IMAGE must pin a digest (name@sha256:...), got '$image'." ;;
  esac
  digest="${image#*@}"
  digests="$(docker image inspect "$image" --format '{{json .RepoDigests}}' 2>/dev/null || true)"
  case "$digests" in
    *"$digest"*) ;;
    *) die "edge image content identity mismatch: $image" ;;
  esac
}

target_migrator_supports_online_safe_check() {
  local version="$1"
  local capability
  capability="$(docker image inspect "${REGISTRY}/sforum-migrate:${version}" \
    --format '{{index .Config.Labels "io.sforum.migrations.online-safe-check"}}' 2>/dev/null || true)"
  [ "$capability" = v1 ]
}

ONLINE_MIGRATION_REQUIRED=false

# 声明形态预检（离线、不碰库）：畸形声明（NO TRANSACTION / 缺 SET LOCAL 超时）是
# 构建缺陷 —— 在它上面谈「在线安全」没有意义，所以直接 abort 并点名文件，不降级成
# 停机升级。「未声明」不在这里判红：离线迁移是合法路径，pending 集合由随后的
# --check-online-safe 决定。
#
# 只有 exit 3 才 abort。exit 2 = 目标镜像还不认识这个 flag（降级到旧版本时会遇到），
# exit 1 = 读不到迁移文件；两者都会让随后的 --check-online-safe 同样失败并走原有
# 分支，所以这里不抢它的判断权，只把输出打出来。
check_online_safe_declaration_form() {
  local version="$1"
  local output="" status=0

  output="$(SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-online-safe-form 2>&1)" || status=$?
  case "$status" in
    0) return 0 ;;
    3)
      printf '%s\n' "$output" >&2
      warn "目标版本的在线迁移声明形态不合格（含 NO TRANSACTION 或缺 SET LOCAL 超时）：这是构建缺陷，既不能在线升级，也不降级成停机升级。" \
        "The target release has malformed online-safe declarations (NO TRANSACTION or missing SET LOCAL timeouts): a build defect; the upgrade is refused rather than downgraded to a maintenance window." >&2
      die "Fix the migration declarations named above, then re-release."
      ;;
    *)
      printf '%s\n' "$output" >&2
      warn "无法完成声明形态预检（退出码 $status），继续由 --check-online-safe 判定。" \
        "The declaration form pre-check did not complete (exit $status); --check-online-safe remains authoritative." >&2
      return 0
      ;;
  esac
}

plan_schema_for_online_update() {
  local version="$1"
  ONLINE_MIGRATION_REQUIRED=false
  step "正在检查目标版本的数据库兼容性..." "Checking the target database compatibility..."
  if SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-no-pending >/dev/null 2>&1; then
    return 0
  fi
  if ! target_migrator_supports_online_safe_check "$version"; then
    return 1
  fi
  check_online_safe_declaration_form "$version"
  if ! SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-online-safe; then
    return 1
  fi
  ONLINE_MIGRATION_REQUIRED=true
}

handle_unsafe_online_migrations() {
  # 目标版本包含无法在线安全执行的迁移：交互确认后原地降级为停机升级；
  # 非交互保持拒绝但给出可复制的授权命令，避免无人值守静默停机。
  if [ "$CONFIRM_MAINTENANCE" = true ]; then
    step "已获得停机升级授权，将在短暂维护窗口内完成迁移。" \
      "Maintenance-window upgrade authorized; migrations will run in a short downtime window."
    return 0
  fi
  if [ "$ASSUME_YES" = true ]; then
    warn \
      "目标版本包含无法在线安全执行的迁移。无人值守模式不会自动停机；请执行：./upgrade.sh --version $TARGET_VERSION --confirm-maintenance" \
      "The target release has migrations that are not safe online. Unattended mode will not trigger downtime automatically; run: ./upgrade.sh --version $TARGET_VERSION --confirm-maintenance"
    die "Maintenance-window confirmation required."
  fi
  local confirmation
  warn \
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
  pull_infra_images
  backup_database

  local old_api old_web new_api new_web
  old_api="$(slot_api "$active_slot")"
  old_web="$(slot_web "$active_slot")"
  new_api="$(slot_api "$inactive_slot")"
  new_web="$(slot_web "$inactive_slot")"

  step "进入维护窗口：停止当前槽并执行完整数据库迁移..." \
    "Entering the maintenance window: stopping the active slot and running all migrations..."
  "${COMPOSE[@]}" stop "$old_web" "$old_api"

  if ! SFORUM_VERSION="$target_version" "${COMPOSE[@]}" run --rm -T --pull never migrate; then
    err "迁移失败，正在恢复旧版本槽..." "Migration failed; restoring the previous slot..."
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Migration failed. The previous slot was restarted; restore the pre-upgrade backup if the schema is now inconsistent."
  fi

  step "迁移完成，正在启动新版本槽..." "Migrations complete; starting the target slot..."
  if ! "${COMPOSE[@]}" up -d --no-build --pull never "$new_api" "$new_web"; then
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Could not start the target slot; the previous slot was restarted."
  fi
  if ! wait_inside_slot "$inactive_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    slot_health_failure_die "Target slot failed its health checks after migration; the previous slot was restarted."
  fi

  if ! switch_router "$inactive_slot" "$active_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    "${COMPOSE[@]}" start "$old_web" "$old_api" || true
    die "Ingress switch failed after maintenance migration; the previous slot was restarted."
  fi

  "${COMPOSE[@]}" stop "$old_web" "$old_api" || true
  remove_legacy_worker_containers || true
  persist_state "$inactive_slot" "$target_version" "$blue_version" "$green_version"
  ok "停机升级完成：$current_version -> $target_version" \
    "Maintenance-window upgrade completed: $current_version -> $target_version"
  print_install_location
}

apply_planned_online_migrations() {
  local version="$1"
  [ "$ONLINE_MIGRATION_REQUIRED" = true ] || return 0
  step \
    "正在保持当前槽在线并执行已声明兼容的数据库迁移..." \
    "Applying declared backward-compatible migrations while the active slot stays online..."
  SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --pull never migrate
  SFORUM_VERSION="$version" "${COMPOSE[@]}" run --rm -T --no-deps --pull never \
    migrate sforum-migrate --check-no-pending >/dev/null || \
    die "Online migrations ran but the target schema is still not exact. Keep the current slot online and inspect the migrator logs."
}

backup_database() {
  step "在线切换前创建 PostgreSQL 备份..." "Creating a PostgreSQL backup before the online switch..."
  ensure_backup_key
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

# 探针：在槽容器内部访问自身端口。失败时把 wget / compose 的诊断留在
# PROBE_OUTPUT，由超时路径一次性打印；循环内不直接输出，避免把「备用槽还在
# 冷启动」写成一片红色错误。赋值放在 if 条件里，免得 set -e 把预期失败当致命错。
PROBE_OUTPUT=""
# 非空代表最近一次 wait_inside_slot 判定为「恢复态」，值是被点名的 recovery code。
SLOT_HEALTH_RECOVERY_REASON=""
# 探针要同时拿到状态码与 body：恢复态的判定信号只在 body 里（见下面
# slot_probe_reports_recovery_only）。原先 `-O /dev/null` + `2>&1 >/dev/null`
# 把 body 丢掉、只留 stderr，于是「恢复态」与「依赖还没起」在探针眼里完全一样。
probe_slot_endpoint() {
  local service="$1" url="$2"
  if PROBE_OUTPUT="$("${COMPOSE[@]}" exec -T "$service" wget -q -O - "$url" 2>&1)"; then
    return 0
  fi
  return 1
}

# 恢复态判定：body 里出现 /ready 的 recovery 块。`"recovery":` 不会命中 /health 的
# `"recoveryRequired":false` —— 那里的 key 后面跟的是 Required，不是引号加冒号。
slot_probe_reports_recovery_only() {
  grep -Eq '"recovery"[[:space:]]*:' <<< "${1:-}"
}

slot_recovery_reason() {
  sed -n 's/.*"recovery"[[:space:]]*:[^{]*{[^}]*"code"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    <<< "${1:-}" | head -n 1
}

# 备用槽健康失败的统一出口：恢复态必须点名原因与人工动作，其余保持原措辞。
# 二者都走 die（调用方已先做完自己的回滚动作），区别只在「运维下一步做什么」。
slot_health_failure_die() {
  local fallback="$1"

  if [ -n "$SLOT_HEALTH_RECOVERY_REASON" ]; then
    err "备用槽进入了恢复态（${SLOT_HEALTH_RECOVERY_REASON}）：Host 只放行 /health 与 /ready，其余路径一律 503。这不是启动慢，等下去不会变好。" \
      "The standby slot booted into recovery-only mode (${SLOT_HEALTH_RECOVERY_REASON}): only /health and /ready answer, every other path returns 503. This is not a slow start and waiting will not fix it." >&2
    die "Fix the artifact named in the recovery block, then run: sforum extension recovery recheck"
  fi
  die "$fallback"
}

dump_slot_diagnostics() {
  # 探针只说「没就绪」，原因在容器日志里；新槽随后会被 stop，所以先留现场。
  local service="$1"
  step "正在收集 ${service} 的容器状态与最近 60 行日志..." \
    "Collecting container state and the last 60 log lines for ${service}..." >&2
  "${COMPOSE[@]}" ps -a "$service" >&2 || true
  "${COMPOSE[@]}" logs --tail=60 --no-color "$service" >&2 || true
}

# 健康超时来自环境变量，两处等待必须给出同一套语义：空串、非数值、0、负数一律
# 回落到默认 120s。不消毒的后果不是「超时短一点」，而是 wait_stable_ingress 里的
# `$((SECONDS + abc))` 在 set -u 下直接致命 —— 而那一行正好跑在 ingress 已经
# `mv` + reload 之后、persist_state 之前（见 switch_router / online_update），
# 崩在那里等于线上已切到新槽，.deployrc 还记着旧槽位与旧版本。
sanitize_positive_int() {
  local raw="${1:-}" fallback="${2:-120}"

  case "$raw" in
    ""|*[!0-9]*) printf '%s\n' "$fallback"; return 0 ;;
  esac
  if [ "$raw" -ge 1 ]; then
    printf '%s\n' "$raw"
  else
    printf '%s\n' "$fallback"
  fi
}

wait_inside_slot() {
  local slot="$1"
  local timeout
  timeout="$(sanitize_positive_int "${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120}")"
  local started=$SECONDS deadline=$((SECONDS + timeout))
  local api web attempts=0 elapsed next_report=15
  local last_failure="" last_service="" last_output=""
  SLOT_HEALTH_RECOVERY_REASON=""
  api="$(slot_api "$slot")"
  web="$(slot_web "$slot")"
  while [ "$SECONDS" -lt "$deadline" ]; do
    attempts=$((attempts + 1))
    elapsed=$((SECONDS - started))
    if ! probe_slot_endpoint "$api" "http://127.0.0.1:8080/api/v1/ready"; then
      last_failure="api ready"; last_service="$api"
    elif ! probe_slot_endpoint "$web" "http://127.0.0.1:3000/health"; then
      last_failure="web health"; last_service="$web"
    elif ! probe_slot_endpoint "$web" "http://127.0.0.1:3000/api/v1/ready"; then
      last_failure="web api ready"; last_service="$web"
    else
      if [ "$attempts" -gt 1 ]; then
        ok "备用槽 $slot 已就绪（等待 ${elapsed}s，共 ${attempts} 次探测）。" \
          "Standby slot $slot is ready after ${elapsed}s (${attempts} probes)."
      fi
      return 0
    fi
    # 探针失败后才看 body：恢复态是终态，继续等只是把整个超时预算烧在必然
    # 失败的探测上，并把真正的原因埋进「健康检查超时」。
    if slot_probe_reports_recovery_only "$PROBE_OUTPUT"; then
      SLOT_HEALTH_RECOVERY_REASON="$(slot_recovery_reason "$PROBE_OUTPUT")"
      return 3
    fi
    last_output="$PROBE_OUTPUT"
    if [ "$elapsed" -ge "$next_report" ]; then
      warn "备用槽 $slot 仍在启动：${last_failure}（$last_service），已等待 ${elapsed}s/${timeout}s..." \
        "Standby slot $slot is still starting: ${last_failure} ($last_service); waited ${elapsed}s/${timeout}s..." >&2
      next_report=$((elapsed + 15))
    fi
    sleep 2
  done
  err "健康检查超时（${timeout} 秒），最后未通过的检查：${last_failure:-无}（${last_service:-未知}）" \
    "Health check timed out after ${timeout}s; the last failing probe was: ${last_failure:-none} (${last_service:-unknown})" >&2
  if [ -n "$last_output" ]; then
    err "最后一次探针输出：${last_output}" "Last probe output: ${last_output}" >&2
  fi
  [ -z "$last_service" ] || dump_slot_diagnostics "$last_service"
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
    # 客户端 IP 必须在本层按 {client_ip} 重新计算：限流键、登录锁定 IP 和审计
    # 来源地址都读 X-Real-IP，缺了这一行上游只能看到 web 容器的地址，客户端
    # 自带的同名头也不会被覆盖（审计 RV-02）。
    printf '\treverse_proxy web-%s:3000 {\n' "$slot"
    printf '\t\theader_up X-Real-IP {client_ip}\n'
    printf '\t}\n'
    printf '}\n\n'
    printf ':18080 {\n'
    printf '\theader X-SForum-Active-Slot %s\n' "$slot"
    printf '\treverse_proxy api-%s:8080 {\n' "$slot"
    printf '\t\theader_up X-Real-IP {client_ip}\n'
    printf '\t}\n'
    printf '}\n'
  } > "$destination"
  chmod 600 "$destination"
}

wait_stable_ingress() {
  local slot="$1" web_port api_port deadline header_file timeout
  web_port="$(env_value WEB_PORT 3000)"
  api_port="$(env_value API_PORT 18080)"
  # 与 wait_inside_slot 用同一个消毒函数：两处对同一个变量不能有两种语义。
  timeout="$(sanitize_positive_int "${SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS:-120}")"
  deadline=$((SECONDS + timeout))
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
  pull_with_retry "$(caddy_image)" edge || die "Could not pull $(caddy_image) after 3 attempts."
  verify_caddy_image
  verify_target_images "$target_version"
  "${COMPOSE[@]}" up -d --wait postgres redis
  if ! plan_schema_for_online_update "$target_version"; then
    # 首次拓扑转换本来就是维护窗口，可直接执行全部迁移。
    warn "目标版本需要停机迁移；本次拓扑转换将一并完成。" \
      "The target release requires offline migrations; this topology conversion will apply them."
    backup_database
    SFORUM_VERSION="$target_version" "${COMPOSE[@]}" run --rm -T --pull never migrate
    "${COMPOSE[@]}" up -d --no-build --pull never api-blue web-blue
  else
    backup_database
    apply_planned_online_migrations "$target_version"
    "${COMPOSE[@]}" up -d --no-build --pull never api-blue web-blue
  fi
  wait_inside_slot blue || slot_health_failure_die "Blue candidate failed its internal API/Web checks."

  write_router_config blue "$RUNTIME_DIR/Caddyfile"
  step "正在执行一次性入口转换..." "Performing the one-time ingress conversion..."
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
  ok "双槽入口已启用。后续兼容版本可保持 HTTP 在线切换。" "Blue/green ingress is enabled. Later compatible releases can switch without HTTP downtime."
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
  pull_infra_images
  apply_planned_online_migrations "$target_version"

  step "正在启动并检查备用槽 ${inactive_slot}..." "Starting and checking standby slot ${inactive_slot}..."
  "${COMPOSE[@]}" up -d --no-build --pull never "$new_api" "$new_web"
  if ! wait_inside_slot "$inactive_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    slot_health_failure_die "Standby slot failed internal health checks; live traffic was not changed."
  fi

  step "正在原子切换 Web/API 流量..." "Atomically switching Web/API traffic..."
  if ! switch_router "$inactive_slot" "$active_slot"; then
    "${COMPOSE[@]}" stop "$new_web" "$new_api" || true
    die "Ingress switch failed and was rolled back to the active slot."
  fi

  remove_legacy_worker_containers || die "Could not remove legacy standalone Worker containers."
  "${COMPOSE[@]}" stop "$old_web" "$old_api"
  persist_state "$inactive_slot" "$target_version" "$blue_version" "$green_version"
  ok "零停机更新完成：$current_version -> $target_version" "Zero-downtime update completed: $current_version -> $target_version"
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
  # 必须在任何 compose 调用（pull / run migrate / up）之前补齐后补键：
  # compose.prod.yaml 的 `:?` 硬闸在插值阶段就失败，不会等到容器启动。
  backfill_env_keys

  local current_version active_slot
  current_version="$(rc_value version)"
  if [ -z "$current_version" ]; then
    command -v docker >/dev/null 2>&1 || die "Docker is required."
    docker info >/dev/null 2>&1 || die "Docker daemon is not running."
    if current_version="$(infer_running_version)"; then
      warn ".deployrc 缺少版本记录，已从运行容器推断当前版本：$current_version" \
        ".deployrc has no version record; inferred the running version from containers: $current_version"
    else
      die "The currently deployed version is unknown; re-run ./deploy.sh to restore state."
    fi
  fi
  if ! [[ "$current_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
      current_version="$(infer_running_version)" || true
    fi
  fi
  validate_version "$current_version"
  select_target_version
  confirm_target_version "$current_version"

  command -v docker >/dev/null 2>&1 || die "Docker is required."
  docker info >/dev/null 2>&1 || die "Docker daemon is not running."
  resolve_registry
  export SFORUM_VERSION="$TARGET_VERSION"
  init_compose
  acquire_deploy_lock upgrade
  active_slot="$(rc_value active_slot)"
  if [ -z "$active_slot" ]; then
    bootstrap_topology "$current_version" "$TARGET_VERSION"
  else
    [ "$active_slot" = blue ] || [ "$active_slot" = green ] || die "Invalid active_slot in .deployrc"
    online_update "$active_slot" "$current_version" "$TARGET_VERSION"
  fi
  release_deploy_lock
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
