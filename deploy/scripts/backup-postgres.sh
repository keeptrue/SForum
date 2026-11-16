#!/usr/bin/env bash
# 生产 PostgreSQL 备份：数据库 + 附件卷 + 扩展制品卷，单一归档、强制加密、带保留策略。
#
# 用法：
#   export SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key   # 推荐，0600；口令文件
#   ./deploy/scripts/backup-postgres.sh                     # 输出归档绝对路径（最后一行）
#
# 可选环境变量：
#   SFORUM_BACKUP_DIR                 备份目录（默认 ./backups）
#   SFORUM_BACKUP_KEY                 口令（未设 KEY_FILE 时使用；会进进程环境，优先用文件）
#   SFORUM_BACKUP_RETENTION_COUNT     保留份数（默认 14；default 表示回默认值）
#   SFORUM_BACKUP_RETENTION_DAYS      保留天数（默认 30；default 表示回默认值）
#   SFORUM_ATTACHMENT_CONTAINER_DIR   附件卷容器内路径（默认 /app/storage/app/attachments）
#
# 加密是硬要求：没有可用密钥时直接失败，绝不静默产出明文备份（OPS2-06）。
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/scripts/postgres-common.sh
source "$SCRIPT_DIR/postgres-common.sh"
# shellcheck source=deploy/scripts/postgres-backup-format.sh
source "$SCRIPT_DIR/postgres-backup-format.sh"

ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

if [ ! -f .env.production ]; then
  echo ".env.production is required."
  exit 1
fi
chmod 600 .env.production

POSTGRES_DB="$(sforum_env_value .env.production POSTGRES_DB sforum)"
POSTGRES_USER="$(sforum_env_value .env.production POSTGRES_USER sforum)"
POSTGRES_ACTOR="$POSTGRES_USER"

sforum_configure_compose
EXTENSION_CONTAINER_DIR="$(sforum_env_path_value .env.production EXTENSION_ROOT /var/lib/sforum/extensions)"
SFORUM_ATTACHMENT_CONTAINER_DIR="${SFORUM_ATTACHMENT_CONTAINER_DIR:-$SFORUM_ATTACHMENT_CONTAINER_DIR_DEFAULT}"

# 在创建任何文件之前就要拿到密钥：先落盘再报错等于留下了明文残片。
sforum_require_backup_key "创建备份"

BACKUP_DIR="${SFORUM_BACKUP_DIR:-$ROOT_DIR/backups}"
mkdir -p "$BACKUP_DIR"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ARCHIVE="$BACKUP_DIR/sforum-backup-$STAMP.tar.gz.enc"
WORK_DIR="$(mktemp -d "$BACKUP_DIR/.sforum-backup-$STAMP.XXXXXX")"

cleanup() {
  local status=$?
  trap - EXIT
  if [ -n "${WORK_DIR:-}" ] && [ -d "$WORK_DIR" ]; then
    rm -rf "$WORK_DIR"
  fi
  if [ "$status" -ne 0 ] && [ -f "$ARCHIVE" ]; then
    rm -f "$ARCHIVE"
  fi
  exit "$status"
}
trap cleanup EXIT

# 冷机直接跑这个脚本（不是经 deploy.sh）时，postgres 可能还没起。慢启动降级成
# warning 交给 pg_isready 重试；容器没在跑是真故障，立刻失败。
sforum_start_postgres
sforum_wait_for_postgres "$POSTGRES_DB"

echo "Creating PostgreSQL backup: $ARCHIVE"
"${SFORUM_COMPOSE[@]}" exec -T postgres pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  > "$WORK_DIR/database.sql"
if [ ! -s "$WORK_DIR/database.sql" ] || [ ! -r "$WORK_DIR/database.sql" ]; then
  sforum_die "pg_dump 没有产出可读转储：$ARCHIVE"
fi
chmod 600 "$WORK_DIR/database.sql"

# 备份可解析性：转储里必须能认出完整核心表，否则这份归档不值得留在盘上。
sforum_validate_sql_payload "$WORK_DIR/database.sql"

sforum_sql_row_counts "$WORK_DIR/database.sql" "$WORK_DIR/rows.env"

sforum_collect_assets "$WORK_DIR/assets"
sforum_write_manifest "$WORK_DIR/manifest.env" "$POSTGRES_DB" "$WORK_DIR/rows.env"

tar -C "$WORK_DIR" -czf "$WORK_DIR/archive.tar.gz" manifest.env database.sql assets
tar -tzf "$WORK_DIR/archive.tar.gz" >/dev/null 2>&1 || sforum_die "归档自检失败：$ARCHIVE"

sforum_encrypt_file "$WORK_DIR/archive.tar.gz" "$WORK_DIR/archive.tar.gz.enc"
mv "$WORK_DIR/archive.tar.gz.enc" "$ARCHIVE"
chmod 600 "$ARCHIVE"

sforum_apply_retention "$BACKUP_DIR"
printf '%s\n' "$ARCHIVE"
