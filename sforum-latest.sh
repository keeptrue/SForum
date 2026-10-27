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

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPOSITORY="${SFORUM_RELEASE_REPOSITORY:-keeptrue/SForum}"
API_ROOT="https://api.github.com/repos/${REPOSITORY}/releases"
DOWNLOAD_ROOT="${SFORUM_RELEASE_DOWNLOAD_BASE_URL:-https://github.com/${REPOSITORY}/releases/download}"
LOCK_DIR="$ROOT_DIR/.sforum-latest.lock"
BOOTSTRAP="$ROOT_DIR/sforum-bootstrap.sh"

CHANNEL="stable"
CHANNEL_EXPLICIT=false
ASSUME_YES=false
TARGET_TAG=""
CHECK_ONLY=false
ALLOW_GIT=false
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

die() {
  printf 'sforum-latest: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  [ -z "$STAGE" ] || rm -rf "$STAGE"
  if [ "$LOCK_HELD" = true ]; then
    rmdir "$LOCK_DIR" 2>/dev/null || true
    LOCK_HELD=false
  fi
}

acquire_lock() {
  mkdir "$LOCK_DIR" 2>/dev/null || die "同目录已有另一个 sforum-latest 正在运行"
  LOCK_HELD=true
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
  printf '请选择发布渠道：\n  1) 正式版 stable（推荐）\n  2) 最新发布版（含 alpha/beta）\n  3) 指定版本\n'
  read -r -p '渠道 [1]: ' choice || choice=1
  case "${choice:-1}" in
    1|stable) CHANNEL=stable ;;
    2|prerelease) CHANNEL=prerelease ;;
    3)
      read -r -p '版本标签（如 v3.0.12-alpha.6）: ' tag || die "未读取到版本标签"
      TARGET_TAG="$(normalize_tag "$tag")"
      ;;
    *) die "无效的渠道选择: $choice" ;;
  esac
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --check|--dry-run) CHECK_ONLY=true; shift ;;
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
      printf '提示: 本机 gh 不支持 attestation，已跳过构建来源校验（SHA-256 已通过）\n' >&2
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
    printf '旧工具链备份: %s\n' "$BACKUP_ROOT"
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
    verified) printf '构建来源: gh attestation 校验通过\n' ;;
    skipped-env) printf '构建来源: 按 SFORUM_SKIP_ATTESTATION 跳过\n' ;;
    skipped-no-gh) printf '构建来源: 未安装 gh，仅做 SHA-256 校验\n' ;;
    *) printf '构建来源: 本机 gh 不支持 attestation，仅做 SHA-256 校验\n' ;;
  esac
}

report() {
  local tag="$1" count
  printf '目标 Release: %s\n' "$tag"
  printf '部署包 SHA-256: %s\n' "$BUNDLE_SHA"
  print_attestation
  if [ -s "$DIFF_FILE" ]; then
    count="$(wc -l < "$DIFF_FILE" | tr -d '[:space:]')"
    printf '受管工具链: %s 个文件需要更新\n' "$count"
    sed -n '1,10p' "$DIFF_FILE" | sed 's/^/  /'
    [ "$count" -le 10 ] || printf '  ...（其余 %s 个）\n' "$((count - 10))"
  else
    printf '受管工具链: 已是最新\n'
  fi
  case "$ACTION" in
    install) printf '安装状态: 未安装，计划动作 install\n' ;;
    upgrade) printf '安装状态: 已安装，计划动作 upgrade\n' ;;
    repair) printf '安装状态: .deployrc 缺少有效版本记录，计划动作 deploy（重建状态）\n' ;;
    *) printf '安装状态: 不完整（只有 .deployrc，没有 .env.production），需要先人工修复\n' ;;
  esac
}

run_action() {
  case "$ACTION" in
    install|upgrade)
      printf '执行: ./sforum-bootstrap.sh %s --version %s\n' "$ACTION" "$TARGET_TAG"
      "$BOOTSTRAP" "$ACTION" --version "$TARGET_TAG" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
      ;;
    repair)
      printf '执行: ./deploy.sh --action deploy --version %s\n' "$TARGET_TAG"
      "$ROOT_DIR/deploy.sh" --action deploy --version "$TARGET_TAG" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
      ;;
  esac
}

main() {
  trap cleanup EXIT
  trap 'exit 130' HUP INT TERM

  parse_args "$@"
  detect_action
  printf '安装目录: %s\n' "$ROOT_DIR"

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

  TARGET_TAG="$(resolve_tag)"
  CURRENT_VERSION="$(sed -n 's/^version=//p' "$ROOT_DIR/.deployrc" 2>/dev/null | head -n1)"
  if [ -n "$CURRENT_VERSION" ] && version_is_older "$TARGET_TAG" "$CURRENT_VERSION"; then
    if [ "$CHECK_ONLY" = true ]; then
      printf '警告: 目标 %s 低于当前记录的 %s，直接部署会被数据库迁移拒绝。\n' \
        "$TARGET_TAG" "$CURRENT_VERSION" >&2
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

  printf '目标 Release: %s\n' "$TARGET_TAG"
  printf '部署包 SHA-256: %s\n' "$BUNDLE_SHA"
  print_attestation
  acquire_lock

  if [ -s "$DIFF_FILE" ]; then
    printf '同步受管工具链: %s 个文件\n' "$(wc -l < "$DIFF_FILE" | tr -d '[:space:]')"
    backup_toolkit
    promote_toolkit
  else
    printf '受管工具链: 已是最新\n'
  fi

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
      printf '\n数据库已迁移到 %s，目标 %s 更旧。\n' "$migrated" "$TARGET_TAG" >&2
      printf '改用：./sforum-latest.sh --version %s\n' "${migrated#v}" >&2
      printf '或：./sforum-latest.sh --channel prerelease\n' >&2
    fi
  fi

  if [ "$status" -eq 0 ]; then
    toolkit_diff
    if [ -s "$DIFF_FILE" ]; then
      printf '执行后工具链仍与 %s 不一致：\n' "$TARGET_TAG" >&2
      sed -n '1,20p' "$DIFF_FILE" >&2
      status=1
    fi
  fi

  cleanup
  trap - EXIT HUP INT TERM
  exit "$status"
}

main "$@"
