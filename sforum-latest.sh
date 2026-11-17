#!/usr/bin/env bash
# sforum-latest.sh — 每次都从 GitHub Release 取回完整部署工具链，再安装或更新。
#
# 与直接执行 ./sforum-bootstrap.sh upgrade 的差别：bootstrap 的自刷新由本地旧
# 脚本发起；本脚本不信任本地脚本，先联网下载目标 Release 的完整部署包
# （sforum-deploy.tar.gz），校验后把 deploy.sh、upgrade.sh、Compose 文件、
# deploy/scripts、Caddy 示例和 VERSION 全部同步到安装目录，再交给同一 Release
# 的 bootstrap 执行 install/upgrade。任何过期或缺失的受管文件都会在这里被替换。
set -euo pipefail
export LC_ALL=C
# 终端配色层：本文件要在 deploy/scripts 还不存在的空目录里执行，所以内联同一份标记块；
# tests/validate-term-ui.sh 与 deploy/scripts/term-ui.sh 逐字节比对。
# >>> term-ui
# 开关优先级：SFORUM_COLOR=always|never > NO_COLOR > TERM=dumb > TTY 自动判定。
# 显式开关优先于环境默认关：always 压过 NO_COLOR 与 TERM=dumb。
ui_init_color() {
  if [ "${SFORUM_COLOR:-auto}" = always ]; then
    UI_COLOR=true
  elif [ "${SFORUM_COLOR:-auto}" = never ] || [ -n "${NO_COLOR:-}" ] || [ "${TERM:-}" = dumb ]; then
    UI_COLOR=false
  elif [ -t 1 ]; then
    UI_COLOR=true
  else
    UI_COLOR=false
  fi
  if [ "$UI_COLOR" = true ]; then
    UI_RESET=$'\033[0m';  UI_BANNER=$'\033[1;36m'; UI_STEP=$'\033[1m'
    UI_OK=$'\033[1;32m';  UI_WARN=$'\033[1;33m';   UI_ERR=$'\033[1;31m'
    UI_KEY=$'\033[36m';   UI_PROMPT=$'\033[1;36m'
  else
    UI_RESET=''; UI_BANNER=''; UI_STEP=''; UI_OK=''
    UI_WARN=''; UI_ERR=''; UI_KEY=''; UI_PROMPT=''
  fi
}
ui_init_color
ui_banner() { printf '%s== %s ==%s\n' "$UI_BANNER" "$1" "$UI_RESET"; }
ui_step()   { printf '%s==> %s%s\n' "$UI_STEP" "$1" "$UI_RESET"; }
ui_ok()     { printf '%s[ok] %s%s\n' "$UI_OK" "$1" "$UI_RESET"; }
ui_warn()   { printf '%s[!] %s%s\n' "$UI_WARN" "$1" "$UI_RESET" >&2; }
ui_err()    { printf '%s[x] %s%s\n' "$UI_ERR" "$1" "$UI_RESET" >&2; }
ui_key()    { printf '%s%s%s\n' "$UI_KEY" "$1" "$UI_RESET"; }
ui_prompt() { printf '%s%s%s' "$UI_PROMPT" "$1" "$UI_RESET"; }
# <<< term-ui

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPOSITORY="${SFORUM_RELEASE_REPOSITORY:-keeptrue/SForum}"
API_ROOT="https://api.github.com/repos/${REPOSITORY}/releases"
DOWNLOAD_ROOT="${SFORUM_RELEASE_DOWNLOAD_BASE_URL:-https://github.com/${REPOSITORY}/releases/download}"
# OPS-02：锁与 deploy.sh / upgrade.sh 同域 —— 同一个 .deploy.lock，同一份元数据格式
# （pid 纯文本 + owner 的 pid=/action=/started_at=/proc_start=）。字段格式与
# deploy/scripts/deploy-lock.sh 逐字对齐，**改动必须两边同改**。
#
# 为什么不 source 那个库：首次运行的目录里根本没有 deploy/scripts/deploy-lock.sh
# （它正是本脚本要下载安装的受管文件之一），所以这里内联同一套判据。
LOCK_DIR="$ROOT_DIR/.deploy.lock"
LOCK_ACTION="latest"
BOOTSTRAP="$ROOT_DIR/sforum-bootstrap.sh"

CHANNEL="stable"
CHANNEL_EXPLICIT=false
ASSUME_YES=false
TARGET_TAG=""
CHECK_ONLY=false
ALLOW_GIT=false
UNLOCK=false
ACTION=""
BUNDLE_SHA=""
ATTESTATION_STATUS="skipped-no-gh"
LOCK_HELD=false
PASSTHROUGH=()
STAGE=""
BUNDLE_ROOT=""
MANAGED_LIST=""
DIFF_FILE=""
BACKUP_ROOT=""
CURRENT_VERSION=""

usage() {
  cat <<'EOF'
用法: ./sforum-latest.sh [选项]

始终下载 GitHub 上最新 Release 的完整部署包，校验后把安装目录里的
deploy.sh、upgrade.sh、compose 文件、deploy/scripts、Caddy 示例与 VERSION
同步到该 Release 的版本，再交给 bootstrap 执行 install/upgrade。当前目录有
.env.production 且 .deployrc 含有效版本时执行 upgrade；有 .env.production 但
.deployrc 缺失或没有版本记录时执行 deploy 重建状态；两者都不存在时执行 install。

选项:
  --check, --dry-run        只下载、校验并报告哪些受管文件需要更新，不写入
  --channel stable|prerelease
                            选择解析渠道（默认 stable；prerelease 取最新发布版）
  --version VERSION         固定到不可变标签（如 v3.0.9），跳过联网解析
  --yes, --defaults         跳过 bootstrap/upgrade 的确认（无人值守）
  --bootstrap               已安装目录首次做蓝绿入口转换（upgrade 专用）
  --lang zh|en              全新安装时的安装器语言（install 专用）
  --allow-git               允许在 Git 检出目录里同步工具链（默认跳过）
  --unlock                  回收陈旧的 .deploy.lock（仍被活性进程持有则拒绝）
  -h, --help                显示本帮助

示例:
  ./sforum-latest.sh --check            先看目标版本和哪些工具文件过期
  ./sforum-latest.sh                    交互选择正式版/预发布版/指定版本
  ./sforum-latest.sh --yes              无人值守更新到最新稳定版
  ./sforum-latest.sh --channel prerelease
  ./sforum-latest.sh --version v3.0.9

环境变量:
  SFORUM_RELEASE_REPOSITORY        覆盖仓库（默认 keeptrue/SForum）
  SFORUM_RELEASE_DOWNLOAD_BASE_URL 覆盖下载基址（镜像场景）
  SFORUM_SKIP_ATTESTATION=true     跳过 gh 构建来源校验（bootstrap 同样识别）
  https_proxy / http_proxy         访问 GitHub 需要时自行设置

维护窗口迁移由安装目录的 upgrade.sh 直接处理，不经过本脚本：
  ./upgrade.sh --version <TAG> --confirm-maintenance
EOF
}

# 机器前缀必须留在行首字节（tests/validate-sforum-latest.sh 按 `^sforum-latest: ` 判定），
# 颜色只包裹前缀之后的正文。
die() {
  printf 'sforum-latest: %s[x] %s%s\n' "$UI_ERR" "$1" "$UI_RESET" >&2
  exit 1
}

cleanup() {
  [ -z "$STAGE" ] || rm -rf "$STAGE"
  release_lock
}

# 进程启动时刻：pid 被复用后必然不同，用它与 owner 里记录的值比对，区分「原来的
# 持有者」和「刚复用同一个 pid 的新进程」。
lock_proc_start() {
  LC_ALL=C ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true
}

lock_field() { sed -n "s/^$1=//p" "$LOCK_DIR/owner" 2>/dev/null | head -n 1 || true; }

# kill -0 对别的用户的进程返回 EPERM：活着的别人家进程也会探测失败。ps -p 兜底把
# 这种情况判回存活，否则一个 root 持有的锁会被普通用户的本次运行当死锁清掉。
lock_pid_alive() {
  [ -n "${1:-}" ] || return 1
  kill -0 "$1" 2>/dev/null || ps -p "$1" >/dev/null 2>&1
}

lock_dir_pid() {
  local dir="$1" pid
  pid="$(sed -n 's/^pid=//p' "$dir/owner" 2>/dev/null | head -n 1 || true)"
  if [ -z "$pid" ] && [ -f "$dir/pid" ]; then
    pid="$(head -n 1 "$dir/pid" | tr -d '[:space:]')"
  fi
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$pid"
}

# 一行持有者摘要，拒绝信息与日志共用同一份格式。
lock_holder_summary() {
  local pid action started_at state=unknown
  pid="$(lock_dir_pid "$LOCK_DIR" || true)"
  action="$(lock_field action)"
  started_at="$(lock_field started_at)"
  if [ -n "$pid" ]; then
    if lock_pid_alive "$pid"; then state=alive; else state=dead; fi
  fi
  printf 'pid=%s action=%s started_at=%s state=%s' \
    "${pid:-?}" "${action:-?}" "${started_at:-?}" "$state"
}

lock_is_stale() {
  local pid recorded current mtime
  LOCK_REASON=""
  if pid="$(lock_dir_pid "$LOCK_DIR")"; then
    if ! lock_pid_alive "$pid"; then LOCK_REASON=pid_gone; return 0; fi
    recorded="$(lock_field proc_start)"
    current="$(lock_proc_start "$pid")"
    if [ -n "$recorded" ] && [ -n "$current" ] && [ "$recorded" != "$current" ]; then
      LOCK_REASON=pid_reused
      return 0
    fi
    LOCK_REASON=held
    return 1
  fi
  # 元数据缺失（只有空目录）：锁龄超过 1 小时才回收，权当没人持有了。
  LOCK_REASON=no_owner
  mtime="$(stat -c %Y "$LOCK_DIR" 2>/dev/null || stat -f %m "$LOCK_DIR" 2>/dev/null || echo 0)"
  [ "$mtime" -gt 0 ] && [ $(($(date +%s) - mtime)) -gt 3600 ]
}

# 认领并销毁陈旧锁：rename 是 shell 里唯一的原子「取走」，只删自己认领到的副本，
# 永远不对 .deploy.lock 直接 rm -rf（那会删掉别人刚建的活锁）。
reclaim_stale_lock() {
  local stale_pid="$1" claim
  claim="$LOCK_DIR.stale.$$"
  rm -rf "$claim" 2>/dev/null || true
  mv "$LOCK_DIR" "$claim" 2>/dev/null || \
    die "同目录已有另一个部署操作正在运行（.deploy.lock 无法认领）：$(lock_holder_summary)"
  # 认领到的可能是别人在判定之后重建的活锁：比对副本里的 pid 与判定时那把是否同一把。
  if [ "$(lock_dir_pid "$claim" || true)" = "$stale_pid" ]; then
    rm -rf "$claim"
    return 0
  fi
  if [ -e "$LOCK_DIR" ]; then
    die "同目录已有另一个部署操作正在运行（.deploy.lock 被重新占用，残留副本 $claim）：$(lock_holder_summary)"
  fi
  mv "$claim" "$LOCK_DIR" 2>/dev/null || \
    die "同目录已有另一个部署操作正在运行（残留副本 $claim）：$(lock_holder_summary)"
  die "同目录已有另一个部署操作正在运行：$(lock_holder_summary)"
}

acquire_lock() {
  local summary stale_pid
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    summary="$(lock_holder_summary)"
    stale_pid="$(lock_dir_pid "$LOCK_DIR" || true)"
    if ! lock_is_stale; then
      case "${LOCK_REASON:-}" in
        no_owner)
          # 空锁目录（mkdir 之后、写元数据之前被 SIGKILL 的典型残留）：pid 无从判断，
          # 不能假装知道有没有人在跑 —— 给出可执行的回收入口，而不是报成「被占用」。
          die "同目录存在 .deploy.lock 但缺少持有者元数据（可能是崩溃残留），无法判断是否有人在运行；如确认无人在部署，用 ./sforum-latest.sh --unlock 回收"
          ;;
        *)
          die "同目录已有另一个部署操作正在运行（.deploy.lock 被占用）：$summary"
          ;;
      esac
    fi
    reclaim_stale_lock "$stale_pid"
    mkdir "$LOCK_DIR" 2>/dev/null || \
      die "同目录已有另一个部署操作正在运行：$(lock_holder_summary)"
    # 机器前缀必须留在行首字节：ui_warn 会把 `[!] ` 顶到最前，
    # 于是 `^sforum-latest: ` 连无色输出都匹配不上。这里与 die() 同形。
    printf 'sforum-latest: %s[!] %s%s\n' "$UI_WARN" \
      "已回收陈旧锁（reason=${LOCK_REASON:-manual}）" "$UI_RESET" >&2
  fi
  printf '%s\n' "$$" > "$LOCK_DIR/pid"
  printf 'pid=%s\naction=%s\nstarted_at=%s\nproc_start=%s\n' "$$" "$LOCK_ACTION" \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$(lock_proc_start "$$")" > "$LOCK_DIR/owner"
  LOCK_HELD=true
}

# 只回收自己持有的锁：锁目录可能已被 --unlock 或回收逻辑换成别人的活锁，盲删会把
# 别人的互斥拆掉。pid 对不上就只放弃持有权，不动目录。
release_lock() {
  [ "$LOCK_HELD" = true ] || return 0
  if [ "$(head -n 1 "$LOCK_DIR/pid" 2>/dev/null | tr -d '[:space:]')" = "$$" ]; then
    rm -rf "$LOCK_DIR" 2>/dev/null || true
  fi
  LOCK_HELD=false
}

# --unlock 的语义只有「回收陈旧锁」：仍被活性进程持有就拒绝，不提供绕过互斥的入口。
force_unlock() {
  local summary
  if [ ! -e "$LOCK_DIR" ]; then
    ui_warn "当前没有部署锁（$LOCK_DIR）。"
    return 0
  fi
  summary="$(lock_holder_summary)"
  # 显式人工动作：只要没有「pid 活着且 proc_start 对得上」的持有者就允许清。空锁目录
  # （mkdir 之后、写 pid 之前被 SIGKILL 的典型残留）属于 no_owner，直接回收，不必等 1 小时。
  lock_is_stale || [ "${LOCK_REASON:-}" = no_owner ] || \
    die "部署锁仍被活性进程持有（$summary）；确认持有者已退出后重试"
  reclaim_stale_lock "$(lock_dir_pid "$LOCK_DIR" || true)"
  ui_warn "已回收部署锁（reason=${LOCK_REASON:-manual}）：$summary"
}

passthrough_has() {
  local needle="$1" item
  for item in ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}; do
    [ "$item" = "$needle" ] && return 0
  done
  return 1
}

normalize_tag() {
  local tag="$1"
  case "$tag" in
    v[0-9]*) ;;
    [0-9]*) tag="v$tag" ;;
  esac
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "版本标签格式不正确: $tag"
  printf '%s' "$tag"
}

version_is_older() {
  local a="${1#v}" b="${2#v}"
  a="${a%%-*}"
  b="${b%%-*}"
  local IFS=.
  local -a ap bp
  read -r -a ap <<< "$a"
  read -r -a bp <<< "$b"
  local i x y
  for i in 0 1 2; do
    x="${ap[$i]:-0}"
    y="${bp[$i]:-0}"
    [ "$x" -lt "$y" ] && return 0
    [ "$x" -gt "$y" ] && return 1
  done
  return 1
}

prompt_channel() {
  local choice tag
  ui_banner '请选择发布渠道'
  ui_key '  1) 正式版 stable（推荐）'
  ui_key '  2) 最新发布版（含 alpha/beta）'
  ui_key '  3) 指定版本'
  ui_prompt '渠道 [1]: '
  read -r choice || choice=1
  case "${choice:-1}" in
    1|stable) CHANNEL=stable ;;
    2|prerelease) CHANNEL=prerelease ;;
    3)
      ui_prompt '版本标签（如 v3.0.12-alpha.6）: '
      read -r tag || die "未读取到版本标签"
      TARGET_TAG="$(normalize_tag "$tag")"
      ;;
    *) die "无效的渠道选择: $choice" ;;
  esac
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --check|--dry-run) CHECK_ONLY=true; shift ;;
      --unlock) UNLOCK=true; shift ;;
      --channel)
        [ "$#" -ge 2 ] || die "--channel 需要一个值"
        CHANNEL="$2"
        CHANNEL_EXPLICIT=true
        shift 2
        ;;
      --channel=*) CHANNEL="${1#*=}"; CHANNEL_EXPLICIT=true; shift ;;
      --version)
        [ "$#" -ge 2 ] || die "--version 需要一个值"
        [ -z "$TARGET_TAG" ] || die "只指定一次目标版本"
        TARGET_TAG="$(normalize_tag "$2")"
        shift 2
        ;;
      --version=*)
        [ -z "$TARGET_TAG" ] || die "只指定一次目标版本"
        TARGET_TAG="$(normalize_tag "${1#*=}")"
        shift
        ;;
      --lang)
        [ "$#" -ge 2 ] || die "--lang 需要一个值"
        PASSTHROUGH+=("$1" "$2")
        shift 2
        ;;
      --lang=*) PASSTHROUGH+=("$1"); shift ;;
      --yes|--defaults) ASSUME_YES=true; PASSTHROUGH+=("$1"); shift ;;
      --bootstrap) PASSTHROUGH+=("$1"); shift ;;
      --allow-git) ALLOW_GIT=true; shift ;;
      --confirm-maintenance|--confirm-maintenance=*)
        die "维护窗口参数由 upgrade.sh 直接处理：./upgrade.sh --version <TAG> --confirm-maintenance"
        ;;
      -h|--help) usage; exit 0 ;;
      -*) die "未知选项: $1" ;;
      *)
        [ -z "$TARGET_TAG" ] || die "只指定一次目标版本"
        TARGET_TAG="$(normalize_tag "$1")"
        shift
        ;;
    esac
  done
  [ "$CHANNEL" = stable ] || [ "$CHANNEL" = prerelease ] || \
    die "--channel 只能是 stable 或 prerelease"
}

detect_action() {
  if [ -f "$ROOT_DIR/.env.production" ]; then
    if [ -f "$ROOT_DIR/.deployrc" ] && grep -Eq '^version=v[0-9]+\.[0-9]+\.[0-9]+' "$ROOT_DIR/.deployrc"; then
      ACTION=upgrade
    else
      ACTION=repair
    fi
  elif [ -f "$ROOT_DIR/.deployrc" ]; then
    ACTION=incomplete
  else
    ACTION=install
  fi
}

resolve_tag_from_redirect() {
  local location tag
  location="$(curl -fsSI --connect-timeout 10 --max-time 30 \
      "https://github.com/${REPOSITORY}/releases/latest/download/sforum-bootstrap.sh" 2>/dev/null \
    | tr -d '\r' \
    | sed -n 's/^[Ll]ocation:[[:space:]]*//p' \
    | sed -n '1p')"
  [ -n "$location" ] || return 1
  tag="$(printf '%s\n' "$location" | sed -n 's|.*/releases/download/\([^/]*\)/.*|\1|p')"
  [ -n "$tag" ] || return 1
  printf '%s' "$tag"
}

resolve_tag() {
  local api_url json tag
  if [ -n "$TARGET_TAG" ]; then
    printf '%s' "$TARGET_TAG"
    return 0
  fi

  if [ "$CHANNEL" = stable ]; then
    # 未显式指定 API 时，先从 releases/latest 的 302 Location 取标签，
    # 这样一次更新完全不消耗 GitHub API 配额。取不到再回退 API。
    if [ -z "${SFORUM_LATEST_RELEASE_API_URL:-}" ]; then
      if tag="$(resolve_tag_from_redirect)"; then
        normalize_tag "$tag"
        return 0
      fi
    fi
    api_url="${SFORUM_LATEST_RELEASE_API_URL:-$API_ROOT/latest}"
  else
    api_url="${SFORUM_RELEASES_API_URL:-$API_ROOT?per_page=1}"
  fi

  json="$(curl -fsSL --connect-timeout 10 --max-time 30 \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    -H 'User-Agent: SForum-latest' \
    "$api_url")" || die "查询 GitHub Release 失败：检查网络或代理，或用 --version 指定标签"

  tag="$(printf '%s\n' "$json" \
    | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | head -n 1 \
    | sed 's/.*"\([^"]*\)"$/\1/' || true)"
  [ -n "$tag" ] || die "GitHub 没有返回可用的 Release 标签"
  normalize_tag "$tag"
}

stage_bundle() {
  local tag="$1" entry sha
  STAGE="$(mktemp -d "$ROOT_DIR/.sforum-latest.XXXXXX")"

  curl -fsSLo "$STAGE/SHA256SUMS" --connect-timeout 10 --max-time 120 \
    "$DOWNLOAD_ROOT/$tag/SHA256SUMS" || \
    die "下载 SHA256SUMS 失败（$tag）"
  curl -fsSLo "$STAGE/sforum-deploy.tar.gz" --connect-timeout 10 --max-time 120 \
    "$DOWNLOAD_ROOT/$tag/sforum-deploy.tar.gz" || \
    die "下载 sforum-deploy.tar.gz 失败（$tag）"

  (
    cd "$STAGE"
    awk '$2 == "sforum-deploy.tar.gz" { print }' SHA256SUMS > sforum-deploy.sha256
    [ "$(wc -l < sforum-deploy.sha256 | tr -d '[:space:]')" = 1 ]
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c sforum-deploy.sha256
    else
      shasum -a 256 -c sforum-deploy.sha256
    fi
  ) >/dev/null || die "sforum-deploy.tar.gz 的 SHA-256 校验失败（$tag）"

  while IFS= read -r entry; do
    case "$entry" in
      sforum-deploy|sforum-deploy/*) ;;
      *) die "部署包包含意外路径: $entry" ;;
    esac
    case "/$entry/" in
      */../*) die "部署包包含路径穿越: $entry" ;;
    esac
  done < <(tar -tzf "$STAGE/sforum-deploy.tar.gz")

  mkdir -p "$STAGE/bundle"
  tar -xzf "$STAGE/sforum-deploy.tar.gz" -C "$STAGE/bundle"
  BUNDLE_ROOT="$STAGE/bundle/sforum-deploy"
  [ -d "$BUNDLE_ROOT" ] || die "部署包根目录不符合预期"
  [ "$(sed -n 's/^version=//p' "$BUNDLE_ROOT/VERSION")" = "${tag#v}" ] || \
    die "部署包 VERSION 与 $tag 不一致"

  bash -n "$BUNDLE_ROOT/sforum-bootstrap.sh" || die "部署包 bootstrap 语法检查失败"
  bash -n "$BUNDLE_ROOT/deploy.sh" || die "部署包 deploy.sh 语法检查失败"
  bash -n "$BUNDLE_ROOT/upgrade.sh" || die "部署包 upgrade.sh 语法检查失败"

  ATTESTATION_STATUS="skipped-no-gh"
  if [ "${SFORUM_SKIP_ATTESTATION:-false}" = true ]; then
    ATTESTATION_STATUS="skipped-env"
  elif command -v gh >/dev/null 2>&1; then
    if gh attestation --help >/dev/null 2>&1; then
      if gh attestation verify "$STAGE/sforum-deploy.tar.gz" --repo "$REPOSITORY" >/dev/null; then
        ATTESTATION_STATUS="verified"
      else
        die "部署包构建来源校验失败（$tag）"
      fi
    else
      # 旧版 gh 没有 attestation 子命令；同步给 bootstrap，避免它内部的同样调用失败。
      ATTESTATION_STATUS="skipped-unsupported-gh"
      export SFORUM_SKIP_ATTESTATION=true
      ui_warn '提示: 本机 gh 不支持 attestation，已跳过构建来源校验（SHA-256 已通过）'
    fi
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    sha="$(sha256sum "$STAGE/sforum-deploy.tar.gz" | awk '{print $1}')"
  else
    sha="$(shasum -a 256 "$STAGE/sforum-deploy.tar.gz" | awk '{print $1}')"
  fi
  BUNDLE_SHA="$sha"
}

extract_managed_files() {
  MANAGED_LIST="$STAGE/managed-files.txt"
  awk '
    /^managed_files\(\) \{/ { in_fn = 1; next }
    in_fn && /cat <<.EOF.$/ { in_list = 1; next }
    in_list && /^EOF$/ { exit }
    in_list { print }
  ' "$BUNDLE_ROOT/sforum-bootstrap.sh" > "$MANAGED_LIST"
  [ -s "$MANAGED_LIST" ] || die "无法从部署包 bootstrap 提取受管文件清单"
}

validate_bundle() {
  local rel
  while IFS= read -r rel; do
    case "$rel" in
      ""|/*|..|../*|*/..|*/../*) die "部署包清单包含非法路径: $rel" ;;
    esac
    [ -f "$BUNDLE_ROOT/$rel" ] || die "部署包缺少受管文件: $rel"
  done < "$MANAGED_LIST"
}

executable_file() {
  case "$1" in
    sforum-bootstrap.sh|deploy.sh|upgrade.sh|deploy/scripts/*.sh) return 0 ;;
    *) return 1 ;;
  esac
}

promote_file() {
  local source="$1" destination="$2" mode="$3" directory temp_file
  directory="$(dirname "$destination")"
  mkdir -p "$directory"
  temp_file="$(mktemp "$directory/.sforum-tool.XXXXXX")"
  cp "$source" "$temp_file"
  chmod "$mode" "$temp_file"
  mv -f "$temp_file" "$destination"
}

toolkit_diff() {
  local rel
  : > "$DIFF_FILE"
  while IFS= read -r rel; do
    if [ ! -f "$ROOT_DIR/$rel" ]; then
      printf 'missing %s\n' "$rel" >> "$DIFF_FILE"
    elif ! cmp -s "$BUNDLE_ROOT/$rel" "$ROOT_DIR/$rel"; then
      printf 'stale   %s\n' "$rel" >> "$DIFF_FILE"
    fi
  done < "$MANAGED_LIST"
}

backup_toolkit() {
  local rel copied=false
  BACKUP_ROOT=""
  while IFS= read -r rel; do
    [ -f "$ROOT_DIR/$rel" ] || continue
    if [ "$copied" = false ]; then
      BACKUP_ROOT="$ROOT_DIR/.sforum/tooling-backups/$(date -u +%Y%m%dT%H%M%SZ)-${TARGET_TAG}"
      mkdir -p "$BACKUP_ROOT"
      copied=true
    fi
    mkdir -p "$BACKUP_ROOT/$(dirname "$rel")"
    cp -p "$ROOT_DIR/$rel" "$BACKUP_ROOT/$rel"
  done < "$MANAGED_LIST"
  if [ "$copied" = true ]; then
    ui_key "旧工具链备份: $BACKUP_ROOT"
  else
    BACKUP_ROOT=""
  fi
}

restore_toolkit() {
  local rel mode
  [ -n "$BACKUP_ROOT" ] || return 0
  while IFS= read -r rel; do
    if [ -f "$BACKUP_ROOT/$rel" ]; then
      mode=0644
      executable_file "$rel" && mode=0755
      promote_file "$BACKUP_ROOT/$rel" "$ROOT_DIR/$rel" "$mode" || true
    else
      rm -f "$ROOT_DIR/$rel"
    fi
  done < "$MANAGED_LIST"
}

promote_toolkit() {
  local rel mode
  while IFS= read -r rel; do
    mode=0644
    executable_file "$rel" && mode=0755
    if ! promote_file "$BUNDLE_ROOT/$rel" "$ROOT_DIR/$rel" "$mode"; then
      restore_toolkit
      die "写入受管文件失败: $rel；旧工具链已尝试恢复"
    fi
  done < "$MANAGED_LIST"
}

print_attestation() {
  case "$ATTESTATION_STATUS" in
    verified) ui_ok '构建来源: gh attestation 校验通过' ;;
    skipped-env) ui_warn '构建来源: 按 SFORUM_SKIP_ATTESTATION 跳过' ;;
    skipped-no-gh) ui_warn '构建来源: 未安装 gh，仅做 SHA-256 校验' ;;
    *) ui_warn '构建来源: 本机 gh 不支持 attestation，仅做 SHA-256 校验' ;;
  esac
}

report() {
  local tag="$1" count
  ui_key "目标 Release: $tag"
  ui_key "部署包 SHA-256: $BUNDLE_SHA"
  print_attestation
  if [ -s "$DIFF_FILE" ]; then
    count="$(wc -l < "$DIFF_FILE" | tr -d '[:space:]')"
    ui_step "受管工具链: $count 个文件需要更新"
    sed -n '1,10p' "$DIFF_FILE" | sed 's/^/  /'
    [ "$count" -le 10 ] || ui_key "  ...（其余 $((count - 10)) 个）"
  else
    ui_ok '受管工具链: 已是最新'
  fi
  case "$ACTION" in
    install) ui_key '安装状态: 未安装，计划动作 install' ;;
    upgrade) ui_key '安装状态: 已安装，计划动作 upgrade' ;;
    repair) ui_key '安装状态: .deployrc 缺少有效版本记录，计划动作 deploy（重建状态）' ;;
    *) ui_key '安装状态: 不完整（只有 .deployrc，没有 .env.production），需要先人工修复' ;;
  esac
}

run_action() {
  case "$ACTION" in
    install|upgrade)
      ui_step "执行: ./sforum-bootstrap.sh $ACTION --version $TARGET_TAG"
      "$BOOTSTRAP" "$ACTION" --version "$TARGET_TAG" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
      ;;
    repair)
      ui_step "执行: ./deploy.sh --action deploy --version $TARGET_TAG"
      "$ROOT_DIR/deploy.sh" --action deploy --version "$TARGET_TAG" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
      ;;
  esac
}

main() {
  trap cleanup EXIT
  trap 'exit 130' HUP INT TERM

  parse_args "$@"
  if [ "$UNLOCK" = true ]; then
    ui_key "安装目录: $ROOT_DIR"
    force_unlock
    exit 0
  fi
  detect_action
  ui_key "安装目录: $ROOT_DIR"

  if [ -z "$TARGET_TAG" ] && [ "$CHANNEL_EXPLICIT" != true ] && \
     [ "$ASSUME_YES" != true ] && [ -t 0 ]; then
    prompt_channel
  fi

  if [ "$ACTION" = upgrade ] && passthrough_has --lang; then
    die "--lang 只用于 install"
  fi
  if [ "$ACTION" != upgrade ] && passthrough_has --bootstrap; then
    die "--bootstrap 只用于 upgrade"
  fi
  if [ "$ACTION" = incomplete ] && [ "$CHECK_ONLY" != true ]; then
    die "安装状态不完整：先执行 ./deploy.sh --action deploy --version <TAG> 修复"
  fi
  if [ -d "$ROOT_DIR/.git" ] && [ "$CHECK_ONLY" != true ] && [ "$ALLOW_GIT" != true ]; then
    die "当前目录是 Git 检出，不是官方安装目录；确认要在这里同步 Release 工具链时加 --allow-git"
  fi

  command -v curl >/dev/null 2>&1 || die "需要 curl"
  command -v tar >/dev/null 2>&1 || die "需要 tar"

  # OPS-02：会写盘的路径在联网与下载之前拿锁 —— 否则每次重试都先付一次完整下载 +
  # attestation，最后才发现目录被别的操作占着。失败路径由 EXIT trap 释放。
  # --check 是只读预检（README/docs 承诺「不写任何文件」），不抢排他锁：既不该因为
  # 安装目录只读而报成「已有另一个部署操作」，也不该在慢网下载期间挡死真正的部署。
  LOCK_ACTION="$ACTION"
  [ "$CHECK_ONLY" = true ] || acquire_lock
  TARGET_TAG="$(resolve_tag)"
  # .deployrc 缺失是正常状态（全新目录 / 首次安装）：没有「当前版本」可比，交给
  # detect_action 的 install 分支（判据用 -f，与 detect_action 全文一致，目录名落在
  # 「视为缺失」）。存在但不可读才是真故障，必须明确报错，不能让 set -e 把读失败变成
  # 静默 EXIT=2（OPS8-01：--check/install/repair 全不可达）。print 后立即 q 的单进程
  # 写法不需要管道，也就没有 SIGPIPE 可兜 —— 真实读错误应当暴露，不该吞成空版本。
  CURRENT_VERSION=""
  if [ -f "$ROOT_DIR/.deployrc" ]; then
    [ -r "$ROOT_DIR/.deployrc" ] || die "无法读取 $ROOT_DIR/.deployrc"
    CURRENT_VERSION="$(sed -n '/^version=/{s/^version=//;p;q}' "$ROOT_DIR/.deployrc")"
  fi
  if [ -n "$CURRENT_VERSION" ] && version_is_older "$TARGET_TAG" "$CURRENT_VERSION"; then
    if [ "$CHECK_ONLY" = true ]; then
      ui_warn "警告: 目标 $TARGET_TAG 低于当前记录的 $CURRENT_VERSION，直接部署会被数据库迁移拒绝。"
    else
      die "目标 $TARGET_TAG 低于当前记录的 $CURRENT_VERSION；改用 --version $CURRENT_VERSION 或更高版本"
    fi
  fi
  stage_bundle "$TARGET_TAG"
  extract_managed_files
  validate_bundle
  DIFF_FILE="$STAGE/toolkit-diff.txt"
  toolkit_diff

  if [ "$CHECK_ONLY" = true ]; then
    report "$TARGET_TAG"
    exit 0
  fi

  ui_key "目标 Release: $TARGET_TAG"
  ui_key "部署包 SHA-256: $BUNDLE_SHA"
  print_attestation

  if [ -s "$DIFF_FILE" ]; then
    ui_step "同步受管工具链: $(wc -l < "$DIFF_FILE" | tr -d '[:space:]') 个文件"
    backup_toolkit
    promote_toolkit
  else
    ui_ok '受管工具链: 已是最新'
  fi

  # OPS-02：跨进程交接。run_action 会把控制权交给 deploy.sh / upgrade.sh，它们自己
  # 会取同一把 .deploy.lock；父进程必须先放锁，否则子进程把父进程的锁判成占用而死。
  # 交接窗口里的竞争是可见失败（子进程报占用），不会出现两个写者同时改受管文件。
  release_lock

  local status
  set +e
  run_action 2>&1 | tee "$STAGE/action.log"
  status=${PIPESTATUS[0]}
  set -e

  if [ "$status" -ne 0 ]; then
    local migrated
    migrated="$(grep -o 'migrated Core=[0-9A-Za-z._-]*' "$STAGE/action.log" 2>/dev/null \
      | head -n1 | cut -d= -f2 || true)"
    if [ -n "$migrated" ]; then
      ui_err "数据库已迁移到 $migrated，目标 $TARGET_TAG 更旧。"
      ui_err "改用：./sforum-latest.sh --version ${migrated#v}"
      ui_err '或：./sforum-latest.sh --channel prerelease'
    fi
  fi

  if [ "$status" -eq 0 ]; then
    toolkit_diff
    if [ -s "$DIFF_FILE" ]; then
      ui_err "执行后工具链仍与 $TARGET_TAG 不一致："
      sed -n '1,20p' "$DIFF_FILE" >&2
      status=1
    fi
  fi

  cleanup
  trap - EXIT HUP INT TERM
  exit "$status"
}

main "$@"
