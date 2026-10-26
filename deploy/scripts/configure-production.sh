#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_PATH=""
LANGUAGE="zh"
DEFAULTS=false
FORCE=false
DEFAULT_VERSION="${SFORUM_VERSION:-}"
PINNED_VERSION=""

usage() {
  cat <<'EOF'
Usage: configure-production.sh [options]

Options:
  --lang zh|en              Prompt language (default: zh)
  --yes, --defaults         Accept all recommended defaults without prompting
  --version VERSION         Pin the release version without prompting
  --default-version VERSION Set the suggested release version
  --force                   Refuse unsafe replacement of an existing file
  --root DIRECTORY          Repository root (primarily for isolated tooling)
  --output PATH             Output path (default: ROOT/.env.production)
  -h, --help                Show this help
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
    --lang=*)
      LANGUAGE="${1#*=}"
      shift
      ;;
    --yes|--defaults)
      DEFAULTS=true
      shift
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      PINNED_VERSION="$2"
      shift 2
      ;;
    --version=*)
      PINNED_VERSION="${1#*=}"
      shift
      ;;
    --default-version)
      [ "$#" -ge 2 ] || die "--default-version requires a value"
      DEFAULT_VERSION="$2"
      shift 2
      ;;
    --default-version=*)
      DEFAULT_VERSION="${1#*=}"
      shift
      ;;
    --force)
      FORCE=true
      shift
      ;;
    --root)
      [ "$#" -ge 2 ] || die "--root requires a value"
      ROOT_DIR="$2"
      shift 2
      ;;
    --root=*)
      ROOT_DIR="${1#*=}"
      shift
      ;;
    --output)
      [ "$#" -ge 2 ] || die "--output requires a value"
      OUTPUT_PATH="$2"
      shift 2
      ;;
    --output=*)
      OUTPUT_PATH="${1#*=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "Unknown option: $1"
      ;;
  esac
done

[ "$LANGUAGE" = "zh" ] || [ "$LANGUAGE" = "en" ] || die "--lang must be zh or en"

if [ -z "$OUTPUT_PATH" ]; then
  OUTPUT_PATH="$ROOT_DIR/.env.production"
elif [[ "$OUTPUT_PATH" != /* ]]; then
  OUTPUT_PATH="$ROOT_DIR/$OUTPUT_PATH"
fi

t() {
  local key="$1"
  if [ "$LANGUAGE" = "en" ]; then
    case "$key" in
      intro) printf '%s' "SForum production setup" ;;
      managed) printf '%s' "This beginner setup uses PostgreSQL and Redis managed by Docker Compose. External database services are an advanced setup and are not configured by this wizard." ;;
      version) printf '%s' "Release version (leave empty for a source build)" ;;
      web_port) printf '%s' "Web port, bound to 127.0.0.1" ;;
      api_port) printf '%s' "API/WebSocket port, bound to 127.0.0.1" ;;
      app_url) printf '%s' "Public site URL" ;;
      admin_prefix) printf '%s' "Admin route prefix (for example /control-panel)" ;;
      exists) printf '%s' "The existing production configuration was preserved. Secret rotation requires a dedicated migration and is not performed by this wizard." ;;
      refuse_force) printf '%s' "Refusing to replace an existing production file because doing so would rotate database and encryption secrets." ;;
      written) printf '%s' "Production configuration created with mode 0600:" ;;
      invalid_version) printf '%s' "Version must be empty or look like v3.0.0-alpha.8." ;;
      invalid_url) printf '%s' "Site URL must be an http:// or https:// URL without spaces, credentials, fragments, or newlines." ;;
      invalid_port) printf '%s' "Ports must be different integers from 1 to 65535." ;;
      invalid_admin_prefix) printf '%s' "Admin route prefix must be a safe URL path such as /control-panel or /staff-admin." ;;
    esac
  else
    case "$key" in
      intro) printf '%s' "SForum 生产环境配置" ;;
      managed) printf '%s' "此入门向导使用 Docker Compose 内置管理的 PostgreSQL 和 Redis，无需另外安装或填写数据库连接。外部数据库属于高级部署，本向导暂不配置。" ;;
      version) printf '%s' "发布版本（留空表示从源码构建）" ;;
      web_port) printf '%s' "Web 端口（只监听 127.0.0.1）" ;;
      api_port) printf '%s' "API/WebSocket 端口（只监听 127.0.0.1）" ;;
      app_url) printf '%s' "站点公开地址" ;;
      admin_prefix) printf '%s' "管理后台路径前缀（例如 /control-panel）" ;;
      exists) printf '%s' "已保留现有生产配置；密钥轮换需要专用迁移流程，本向导不会执行。" ;;
      refuse_force) printf '%s' "拒绝覆盖现有生产配置，否则会轮换数据库和加密密钥。" ;;
      written) printf '%s' "生产配置已创建，文件权限为 0600：" ;;
      invalid_version) printf '%s' "版本必须留空，或使用 v3.0.0-alpha.8 这样的格式。" ;;
      invalid_url) printf '%s' "站点地址必须是 http:// 或 https:// URL，且不能包含空格、账号信息、片段或换行。" ;;
      invalid_port) printf '%s' "两个端口必须不同，且都是 1 到 65535 之间的整数。" ;;
      invalid_admin_prefix) printf '%s' "管理后台路径前缀必须是安全的 URL 路径，例如 /control-panel 或 /staff-admin。" ;;
    esac
  fi
}

contains_newline() {
  case "$1" in
    *$'\n'*|*$'\r'*) return 0 ;;
    *) return 1 ;;
  esac
}

valid_version() {
  [ -z "$1" ] || [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]
}

valid_port() {
  [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

valid_url() {
  local value="$1"
  ! contains_newline "$value" &&
    [[ "$value" =~ ^https?://[^[:space:]@/#]+(:[0-9]+)?(/[^[:space:]#]*)?$ ]]
}

normalize_admin_prefix() {
  local value="$1"
  value="${value:-/control-panel}"
  [[ "$value" == /* ]] || value="/$value"
  value="${value%/}"
  printf '%s' "${value:-/control-panel}"
}

valid_admin_prefix() {
  local value="$1"
  [[ "$value" =~ ^/[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || return 1
  case "$value" in
    *'//'|*/./*|*/../*|/.|/..|*/.|*/..) return 1 ;;
  esac
}

prompt_default() {
  local label="$1"
  local default="$2"
  local answer
  read -r -p "$label [$default]: " answer
  printf '%s' "${answer:-$default}"
}

if [ -e "$OUTPUT_PATH" ]; then
  if [ "$FORCE" = true ]; then
    die "$(t refuse_force)"
  fi
  printf '%s %s\n' "$(t exists)" "$OUTPUT_PATH"
  exit 0
fi

release_version="${PINNED_VERSION:-$DEFAULT_VERSION}"
web_port="3000"
api_port="18080"
app_url="http://127.0.0.1:3000"
admin_prefix="/control-panel"

if [ "$DEFAULTS" != true ]; then
  printf '%s\n%s\n\n' "$(t intro)" "$(t managed)"
  if [ -z "$PINNED_VERSION" ]; then
    release_version="$(prompt_default "$(t version)" "$DEFAULT_VERSION")"
  fi
  web_port="$(prompt_default "$(t web_port)" "$web_port")"
  api_port="$(prompt_default "$(t api_port)" "$api_port")"
  if [ "$web_port" != "3000" ]; then
    app_url="http://127.0.0.1:$web_port"
  fi
  app_url="$(prompt_default "$(t app_url)" "$app_url")"
  admin_prefix="$(prompt_default "$(t admin_prefix)" "$admin_prefix")"
fi

contains_newline "$release_version" && die "$(t invalid_version)"
valid_version "$release_version" || die "$(t invalid_version)"
valid_port "$web_port" || die "$(t invalid_port)"
valid_port "$api_port" || die "$(t invalid_port)"
[ "$web_port" != "$api_port" ] || die "$(t invalid_port)"
valid_url "$app_url" || die "$(t invalid_url)"
admin_prefix="$(normalize_admin_prefix "$admin_prefix")"
valid_admin_prefix "$admin_prefix" || die "$(t invalid_admin_prefix)"

generate_secret() {
  local value
  if command -v openssl >/dev/null 2>&1; then
    value="$(openssl rand -hex 32)"
  else
    value="$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')"
  fi
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || die "Failed to generate a secure random secret."
  printf '%s' "$value"
}

postgres_password="$(generate_secret)"
redis_password="$(generate_secret)"
session_secret="$(generate_secret)"
identity_secret="$(generate_secret)"
option_key="$(generate_secret)"
altcha_secret="$(generate_secret)"
csrf_origin="${app_url%%/*}"
if [[ "$app_url" =~ ^(https?://[^/]+) ]]; then
  csrf_origin="${BASH_REMATCH[1]}"
fi

output_dir="$(dirname "$OUTPUT_PATH")"
[ -d "$output_dir" ] || die "Output directory does not exist: $output_dir"
umask 077
temp_file="$(mktemp "$output_dir/.env.production.XXXXXX")"
cleanup() {
  if [ -n "${temp_file:-}" ] && [ -f "$temp_file" ]; then
    rm -f "$temp_file"
  fi
}
trap cleanup EXIT HUP INT TERM

{
  printf '%s\n' \
    '# Generated by deploy/scripts/configure-production.sh. Keep this file private.' \
    'APP_NAME=SForum' \
    'APP_ENV=production' \
    "APP_URL=$app_url" \
    "APP_LOCALE=$([ "$LANGUAGE" = "en" ] && printf 'en-US' || printf 'zh-CN')" \
    'SUPPORTED_LOCALES=zh-CN,en-US' \
    'LOG_LEVEL=info'
  if [ -n "$release_version" ]; then
    printf 'SFORUM_VERSION=%s\n' "$release_version"
  fi
  printf '%s\n' \
    'SFORUM_REGISTRY=ghcr.io/keeptrue' \
    'HTTP_HOST=0.0.0.0' \
    'HTTP_PORT=8080' \
    'HTTP_BODY_LIMIT=67108864' \
    "WEB_PORT=$web_port" \
    "API_PORT=$api_port" \
    'POSTGRES_DB=sforum' \
    'POSTGRES_USER=sforum' \
    "POSTGRES_PASSWORD=$postgres_password" \
    "DATABASE_URL=postgres://sforum:$postgres_password@postgres:5432/sforum?sslmode=disable" \
    'MIGRATE_ON_STARTUP=false' \
    'DATABASE_MAX_CONNS=10' \
    'WORKER_SHUTDOWN_TIMEOUT=30s' \
    'REDIS_ADDR=redis:6379' \
    "REDIS_PASSWORD=$redis_password" \
    "SESSION_HASH_SECRET=$session_secret" \
    "IDENTITY_SUBJECT_HMAC_SECRET=$identity_secret" \
    "APP_OPTION_ENC_KEY=$option_key" \
    'HUMAN_VERIFICATION_PROVIDER=disabled' \
    "ALTCHA_SECRET=$altcha_secret" \
    'ALTCHA_CHALLENGE_TTL=10m' \
    'ALTCHA_COST=1000' \
    "CSRF_TRUSTED_ORIGINS=$csrf_origin" \
    'EXTENSION_ROOT=/var/lib/sforum/extensions' \
    'BUILTIN_EXTENSION_ROOT=/app/extensions/builtin' \
    'SFORUM_V3_TRUST_CHALLENGES=true' \
    'SFORUM_V3_TRUST_CHALLENGE_TTL=5m' \
    'NUXT_PUBLIC_API_BASE_URL=/api/v1' \
    'NUXT_API_INTERNAL_BASE_URL=http://api:8080/api/v1' \
    "NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=$admin_prefix"
} > "$temp_file"

chmod 600 "$temp_file"
mv -f "$temp_file" "$OUTPUT_PATH"
temp_file=""
trap - EXIT HUP INT TERM

printf '%s %s\n' "$(t written)" "$OUTPUT_PATH"
printf '%s\n' "$(t managed)"
