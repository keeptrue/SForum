#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMMAND=""
REQUESTED_VERSION=""
TARGET_VERSION=""
CHANNEL="stable"
CHANNEL_EXPLICIT=false
LANGUAGE=""
PASSTHROUGH=()
LOCK_DIR="$ROOT_DIR/.sforum-bootstrap.lock"
REPOSITORY="${SFORUM_RELEASE_REPOSITORY:-keeptrue/SForum}"
DOWNLOAD_BASE="${SFORUM_RELEASE_DOWNLOAD_BASE_URL:-https://github.com/$REPOSITORY/releases/download}"
ACTIVE_STAGE=""
DEPLOY_LOCK_HELD=false
DEPLOY_LOCK_LIBRARY=""

usage() {
  cat <<'EOF'
Usage: ./sforum-bootstrap.sh install|upgrade [options]

Download and verify the target Release's complete deployment toolkit before
starting an installation or update.

Options:
  --version VERSION    Immutable target tag, or latest (default: latest)
  --channel CHANNEL    stable (default) or prerelease
  --lang zh|en         Installer language (install only)
  --yes, --defaults    Skip supported confirmations
  --bootstrap          Confirm the legacy ingress conversion (upgrade only)
  -h, --help           Show this help

Each run resolves an immutable tag, verifies and refreshes this bootstrap, then
verifies and promotes the same Release's complete toolkit (deploy.sh,
upgrade.sh, Compose files, .env.production.example, deploy/scripts, Caddy
examples). Previous managed tools are backed up under .sforum/tooling-backups/.
.env.production, .deployrc, deploy/runtime/, and data volumes are preserved.

Install refuses to run when .env.production or .deployrc already exists.
Upgrade requires both. To recover a half-finished install, run
./deploy.sh --action deploy --version <TAG> once, then use bootstrap upgrade.

Maintenance actions such as status, logs, backup, restore, restart, and stop
remain local deploy.sh operations and do not require GitHub access. A
maintenance-window authorization is a local upgrade.sh flag; the bootstrap does
not forward --confirm-maintenance.
EOF
}

die() {
  printf 'sforum-bootstrap: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  # 提升动作持有的 .deploy.lock 必须先释放：它保护的是受管文件，留在磁盘上会让下一个
  # 操作白等（或按 pid 判活后自行回收）。DEPLOY_LOCK_HELD 只有真正拿到锁才是 true。
  # 不读库的私有变量 DEPLOY_LOCK_DIR：库改名/换实现时它会消失，而「已经 source 到
  # release_deploy_lock」这个事实仍在 —— 有库就调它，没有就按两边约定的路径删。
  if [ "$DEPLOY_LOCK_HELD" = true ]; then
    if declare -F release_deploy_lock >/dev/null 2>&1; then
      release_deploy_lock
    else
      rm -rf "$ROOT_DIR/.deploy.lock" 2>/dev/null || true
    fi
    DEPLOY_LOCK_HELD=false
  fi
  if [ -n "$ACTIVE_STAGE" ] && [ -d "$ACTIVE_STAGE" ]; then
    rm -rf "$ACTIVE_STAGE"
  fi
  rmdir "$LOCK_DIR" 2>/dev/null || true
}

acquire_lock() {
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    die "another bootstrap operation is already running in $ROOT_DIR"
  fi
}

release_lock() {
  rmdir "$LOCK_DIR" 2>/dev/null || true
}

# OPS-05：提升动作必须与 deploy.sh / upgrade.sh 同域 —— 同一个 .deploy.lock。
#
# 选型 A（source 官方库）而不是在本文件里再写一份：锁的判据（pid 存活、ps -p 兜底、
# lstart 复用检测、认领式回收）已经在 deploy/scripts/deploy-lock.sh 里，复制一份只会
# 让两处随版本漂移。全新安装目录里还没有这个文件 —— 它正是本次要提升的受管文件之一
# —— 于是退到「本次 bundle 里那份」：它与马上要安装的版本完全一致，且已通过 SHA-256 +
# attestation 校验，先 source 再安装不会引入版本偏差。两份都缺 → 明确失败，绝不无锁
# 放行。加锁只覆盖提升动作本身，下载/校验在锁外（只读，不改安装目录）。
lock_text() {
  case "$1" in
    deploy_locked) printf '%s' 'another deployment operation holds .deploy.lock' ;;
    lock_recovered) printf '%s' 'reclaimed a stale .deploy.lock' ;;
    unlock_none) printf '%s' 'no .deploy.lock is present' ;;
    unlock_done) printf '%s' 'removed .deploy.lock by force' ;;
    *) printf '%s' "$1" ;;
  esac
}

load_deploy_lock_library() {
  local bundle_root="$1" candidate
  [ -z "$DEPLOY_LOCK_LIBRARY" ] || return 0
  for candidate in \
    "$ROOT_DIR/deploy/scripts/deploy-lock.sh" \
    "$bundle_root/deploy/scripts/deploy-lock.sh"; do
    [ -f "$candidate" ] || continue
    bash -n "$candidate" || die "deploy-lock.sh has invalid shell syntax: $candidate"
    DEPLOY_LOCK_ROOT="$ROOT_DIR"
    # shellcheck source=deploy/scripts/deploy-lock.sh
    . "$candidate"
    DEPLOY_LOCK_LIBRARY="$candidate"
    return 0
  done
  die "deploy/scripts/deploy-lock.sh is missing from both the install root and the bundle; refusing to promote the toolkit without the deployment lock"
}

# deploy-lock.sh 的 acquire_deploy_lock/release_deploy_lock 会接管 EXIT/HUP/INT/TERM
# （它假定调用方没有别的清理要做）。bootstrap 既要清临时 stage 又要清
# .sforum-bootstrap.lock，所以每次进出都要把本脚本自己的 trap 装回来。
restore_bootstrap_traps() {
  trap cleanup EXIT
  trap 'exit 130' HUP INT TERM
}

acquire_promote_lock() {
  load_deploy_lock_library "$1"
  # 库的 acquire 先接管 EXIT/HUP/INT/TERM、再 mkdir。若我们自己的 handler 落在这两步
  # 之间，cleanup 会看到 DEPLOY_LOCK_HELD=false 而让脚本「没锁继续提升」。先把信号
  # 按死（窗口只有一次 mkdir），拿到锁并立起 flag 之后再装回本脚本的 trap。
  trap '' HUP INT TERM
  acquire_deploy_lock promote
  DEPLOY_LOCK_HELD=true
  restore_bootstrap_traps
}

release_promote_lock() {
  [ "$DEPLOY_LOCK_HELD" = true ] || return 0
  release_deploy_lock
  DEPLOY_LOCK_HELD=false
  restore_bootstrap_traps
}

validate_version() {
  [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "version must look like v3.0.0 or v3.0.0-alpha.13"
}

resolve_latest_version() {
  local api_url response resolved
  if [ "$CHANNEL" = stable ]; then
    api_url="${SFORUM_LATEST_RELEASE_API_URL:-https://api.github.com/repos/$REPOSITORY/releases/latest}"
    response="$(curl -fsSL \
      --connect-timeout 10 \
      --max-time 30 \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      -H 'User-Agent: SForum-bootstrap' \
      "$api_url")" || \
      die "could not query the latest stable Release; check the network or select --channel prerelease"
  else
    api_url="${SFORUM_RELEASES_API_URL:-https://api.github.com/repos/$REPOSITORY/releases?per_page=1}"
    response="$(curl -fsSL \
      --connect-timeout 10 \
      --max-time 30 \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      -H 'User-Agent: SForum-bootstrap' \
      "$api_url")" || die "could not query the latest published Release"
  fi
  resolved="$(printf '%s\n' "$response" | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' | sed -n '1p')"
  [ -n "$resolved" ] || die "GitHub returned no matching published Release"
  validate_version "$resolved"
  printf '%s' "$resolved"
}

download_asset() {
  local asset="$1" destination="$2"
  curl -fsSLo "$destination" \
    --connect-timeout 10 \
    --max-time 120 \
    "$DOWNLOAD_BASE/$TARGET_VERSION/$asset" || \
    die "could not download $asset for $TARGET_VERSION"
}

verify_asset() {
  local directory="$1" asset="$2" checksum_file
  checksum_file="$directory/$asset.sha256"
  (
    cd "$directory"
    awk -v asset="$asset" '$2 == asset { print }' SHA256SUMS > "$asset.sha256"
    [ "$(wc -l < "$asset.sha256" | tr -d '[:space:]')" = 1 ] || \
      die "SHA256SUMS must contain exactly one $asset entry"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c "$asset.sha256"
    else
      shasum -a 256 -c "$asset.sha256"
    fi
  ) >/dev/null || die "checksum verification failed for $asset"

  if [ "${SFORUM_SKIP_ATTESTATION:-false}" != true ] && command -v gh >/dev/null 2>&1; then
    gh attestation verify "$directory/$asset" --repo "$REPOSITORY" >/dev/null || \
      die "provenance verification failed for $asset"
  fi
  rm -f "$checksum_file"
}

make_stage() {
  mktemp -d "${TMPDIR:-/tmp}/sforum-bootstrap.XXXXXX"
}

promote_file() {
  local source="$1" destination="$2" mode="$3" destination_dir temp_file
  destination_dir="$(dirname "$destination")"
  mkdir -p "$destination_dir"
  temp_file="$(mktemp "$destination_dir/.sforum-tool.XXXXXX")"
  cp "$source" "$temp_file"
  chmod "$mode" "$temp_file"
  mv -f "$temp_file" "$destination"
}

refresh_bootstrap() {
  local stage
  [ "${SFORUM_BOOTSTRAP_REFRESHED_VERSION:-}" != "$TARGET_VERSION" ] || return 0

  stage="$(make_stage)"
  ACTIVE_STAGE="$stage"
  download_asset sforum-bootstrap.sh "$stage/sforum-bootstrap.sh"
  download_asset SHA256SUMS "$stage/SHA256SUMS"
  verify_asset "$stage" sforum-bootstrap.sh
  bash -n "$stage/sforum-bootstrap.sh" || die "downloaded bootstrap has invalid shell syntax"

  acquire_lock
  promote_file "$stage/sforum-bootstrap.sh" "$ROOT_DIR/sforum-bootstrap.sh" 0755
  release_lock
  rm -rf "$stage"
  ACTIVE_STAGE=""

  SFORUM_BOOTSTRAP_REFRESHED_VERSION="$TARGET_VERSION" \
    exec "$ROOT_DIR/sforum-bootstrap.sh" "$COMMAND" --version "$TARGET_VERSION" \
      "${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}"
}

# 提升进实例目录的完整清单。heredoc 里只能有文件名：它是 `while read` 的数据源，
# 混进一行的注释会被当成文件名，安装当场 die。
# 每一条都必须真的在 sforum-deploy.tar.gz 里：backup-postgres.sh 与
# restore-postgres.sh 的第一条 source 行就要 postgres-common.sh 与
# postgres-backup-format.sh，归档里有、这里漏了，官方安装路径产出的实例会在第一次
# backup/restore 时崩在那行上（R7-OPS-01，由 scripts/ci/validate-deploy-asset-closure.sh
# 静态闭合）。
managed_files() {
  cat <<'EOF'
sforum-bootstrap.sh
deploy.sh
upgrade.sh
compose.yaml
compose.prod.yaml
compose.release.yaml
compose.zero-downtime.yaml
.env.production.example
VERSION
deploy/scripts/configure-production.sh
deploy/scripts/backup-postgres.sh
deploy/scripts/postgres-common.sh
deploy/scripts/postgres-backup-format.sh
deploy/scripts/restore-postgres.sh
deploy/scripts/wait-for-health.sh
deploy/scripts/deploy-lock.sh
deploy/caddy/Caddyfile
deploy/router/Caddyfile.example
deploy/volumes/README.md
deploy/runtime/.gitignore
deploy/runtime/.gitkeep
EOF
}

executable_file() {
  case "$1" in
    sforum-bootstrap.sh|deploy.sh|upgrade.sh|deploy/scripts/*.sh) return 0 ;;
    *) return 1 ;;
  esac
}

validate_bundle() {
  local bundle_root="$1" relative expected_version
  expected_version="${TARGET_VERSION#v}"
  [ "$(sed -n 's/^version=//p' "$bundle_root/VERSION")" = "$expected_version" ] || \
    die "deployment bundle VERSION does not match $TARGET_VERSION"
  while IFS= read -r relative; do
    [ -f "$bundle_root/$relative" ] || die "deployment bundle is missing $relative"
  done < <(managed_files)
  bash -n "$bundle_root/sforum-bootstrap.sh"
  bash -n "$bundle_root/deploy.sh"
  bash -n "$bundle_root/upgrade.sh"
}

backup_managed_files() {
  local backup_root="$1" relative
  while IFS= read -r relative; do
    [ -f "$ROOT_DIR/$relative" ] || continue
    mkdir -p "$backup_root/$(dirname "$relative")"
    cp -p "$ROOT_DIR/$relative" "$backup_root/$relative"
  done < <(managed_files)
}

restore_managed_files() {
  local backup_root="$1" relative mode
  while IFS= read -r relative; do
    if [ -f "$backup_root/$relative" ]; then
      mode=0644
      executable_file "$relative" && mode=0755
      promote_file "$backup_root/$relative" "$ROOT_DIR/$relative" "$mode"
    else
      rm -f "$ROOT_DIR/$relative"
    fi
  done < <(managed_files)
}

promote_bundle() {
  local bundle_root="$1" relative mode backup_root=""
  # 先拿锁再做任何写入：活性锁在这里就拒绝，所以「拒绝时零文件改动」不需要回滚，
  # 也就没有「先写后回滚」的时间窗。
  acquire_promote_lock "$bundle_root"
  if [ -f "$ROOT_DIR/.env.production" ] || [ -f "$ROOT_DIR/.deployrc" ]; then
    mkdir -p "$ROOT_DIR/.sforum/tooling-backups"
    backup_root="$(mktemp -d "$ROOT_DIR/.sforum/tooling-backups/$(date -u +%Y%m%dT%H%M%SZ)-${TARGET_VERSION#v}.XXXXXX")"
    backup_managed_files "$backup_root"
  fi

  while IFS= read -r relative; do
    mode=0644
    executable_file "$relative" && mode=0755
    if ! promote_file "$bundle_root/$relative" "$ROOT_DIR/$relative" "$mode"; then
      if [ -n "$backup_root" ]; then
        restore_managed_files "$backup_root"
      fi
      die "could not promote $relative; the previous deployment toolkit was restored"
    fi
  done < <(managed_files)

  if [ -n "$backup_root" ]; then
    printf 'Previous deployment tools backed up to %s\n' "$backup_root"
  fi
  release_promote_lock
}

refresh_toolkit() {
  local stage extract_root entry
  stage="$(make_stage)"
  ACTIVE_STAGE="$stage"
  download_asset sforum-deploy.tar.gz "$stage/sforum-deploy.tar.gz"
  download_asset SHA256SUMS "$stage/SHA256SUMS"
  verify_asset "$stage" sforum-deploy.tar.gz

  mkdir -p "$stage/extracted"
  while IFS= read -r entry; do
    case "$entry" in
      sforum-deploy|sforum-deploy/*) ;;
      *) die "deployment bundle contains an unexpected path: $entry" ;;
    esac
    case "/$entry/" in
      */../*) die "deployment bundle contains path traversal: $entry" ;;
    esac
  done < <(tar -tzf "$stage/sforum-deploy.tar.gz")
  tar -xzf "$stage/sforum-deploy.tar.gz" -C "$stage/extracted"
  extract_root="$stage/extracted/sforum-deploy"
  [ -d "$extract_root" ] || die "deployment bundle has an unexpected root directory"
  validate_bundle "$extract_root"

  acquire_lock
  promote_bundle "$extract_root"
  release_lock
  rm -rf "$stage"
  ACTIVE_STAGE=""
}

parse_args() {
  [ "$#" -gt 0 ] || { usage; exit 2; }
  case "$1" in
    install|upgrade) COMMAND="$1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "first argument must be install or upgrade" ;;
  esac

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version)
        [ "$#" -ge 2 ] || die "--version requires a value"
        [ -z "$REQUESTED_VERSION" ] || die "specify the target version only once"
        REQUESTED_VERSION="$2"
        shift 2
        ;;
      --version=*)
        [ -z "$REQUESTED_VERSION" ] || die "specify the target version only once"
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
      --lang)
        [ "$COMMAND" = install ] || die "--lang is only valid with install"
        [ "$#" -ge 2 ] || die "--lang requires a value"
        LANGUAGE="$2"
        PASSTHROUGH+=("$1" "$2")
        shift 2
        ;;
      --lang=*)
        [ "$COMMAND" = install ] || die "--lang is only valid with install"
        LANGUAGE="${1#*=}"
        PASSTHROUGH+=("$1")
        shift
        ;;
      --bootstrap)
        [ "$COMMAND" = upgrade ] || die "--bootstrap is only valid with upgrade"
        PASSTHROUGH+=("$1")
        shift
        ;;
      --yes|--defaults)
        PASSTHROUGH+=("$1")
        shift
        ;;
      -h|--help) usage; exit 0 ;;
      -*) die "unknown option: $1" ;;
      *)
        [ "$COMMAND" = upgrade ] || die "install requires --version for an explicit target"
        [ -z "$REQUESTED_VERSION" ] || die "specify the target version only once"
        REQUESTED_VERSION="$1"
        shift
        ;;
    esac
  done
}

main() {
  trap cleanup EXIT
  trap 'exit 130' HUP INT TERM
  parse_args "$@"
  command -v curl >/dev/null 2>&1 || die "curl is required"
  command -v tar >/dev/null 2>&1 || die "tar is required"
  [ "$CHANNEL" = stable ] || [ "$CHANNEL" = prerelease ] || \
    die "--channel must be stable or prerelease"

  if [ "$COMMAND" = install ]; then
    if [ -f "$ROOT_DIR/.env.production" ] || [ -f "$ROOT_DIR/.deployrc" ]; then
      die "an existing installation was detected; use the upgrade command"
    fi
  else
    [ -f "$ROOT_DIR/.env.production" ] || die ".env.production is missing; run install first"
    [ -f "$ROOT_DIR/.deployrc" ] || die ".deployrc is missing; run install first"
  fi

  REQUESTED_VERSION="${REQUESTED_VERSION:-latest}"
  # 交互模式下询问更新渠道；显式 --channel 或显式版本号时跳过。
  if [ "$CHANNEL_EXPLICIT" != true ] && [ "$REQUESTED_VERSION" = latest ]; then
    local has_defaults=0
    local passthrough_flag
    # bash < 4.4（macOS 自带 3.2）在 set -u 下展开空数组会报 unbound variable。
    # `${arr[@]+"${arr[@]}"}` 的写法在空数组时展开成零个词，非空时仍保持元素边界。
    for passthrough_flag in ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}; do
      case "$passthrough_flag" in --yes|--defaults) has_defaults=1 ;; esac
    done
    if [ "$has_defaults" = 0 ]; then
      local choice
      if [ "$LANGUAGE" = en ]; then
        printf '1) Stable (recommended)\n2) Newest release (includes alpha/beta prereleases)\n'
        read -r -p "Release channel [1]: " choice || die "could not read a channel"
      else
        printf '1) 稳定版（推荐）\n2) 最新发布版（包含 alpha/beta 预发布）\n'
        read -r -p "请选择发布渠道 [1]: " choice || die "无法读取渠道选择"
      fi
      case "${choice:-1}" in
        1|stable) CHANNEL="stable" ;;
        2|prerelease) CHANNEL="prerelease" ;;
        *) die "invalid channel selection: $choice" ;;
      esac
    fi
  fi
  if [ "$REQUESTED_VERSION" = latest ]; then
    TARGET_VERSION="$(resolve_latest_version)"
  else
    TARGET_VERSION="$REQUESTED_VERSION"
    [[ "$TARGET_VERSION" == v* ]] || TARGET_VERSION="v$TARGET_VERSION"
    validate_version "$TARGET_VERSION"
  fi

  printf 'Resolved deployment toolkit: %s\n' "$TARGET_VERSION"
  refresh_bootstrap
  refresh_toolkit

  printf 'Install directory: %s\n' "$ROOT_DIR"

  if [ "$COMMAND" = install ]; then
    exec "$ROOT_DIR/deploy.sh" --action deploy --version "$TARGET_VERSION" \
      "${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}"
  fi
  exec "$ROOT_DIR/upgrade.sh" --version "$TARGET_VERSION" \
    "${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}"
}

main "$@"
