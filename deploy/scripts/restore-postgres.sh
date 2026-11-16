#!/usr/bin/env bash
# 生产 PostgreSQL 恢复：先验证内容可信，再切库；切库后先验证 API 就绪，最后才删旧库。
#
# 用法：
#   export SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key   # 加密归档必需
#   SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh path/to/backup
#
# 恢复顺序（RV-16）：
#   1. 解包 / 解密，扫描 SQL 里核心表是否齐全 —— 内容不可信就不碰数据库；
#   2. 在独立库 sforum_restore_<ts> 里灌入并校验（关系数、核心表、清单行数）；
#   3. 按拓扑停槽（蓝绿按槽，不无差别杀连接）→ 改名切库；
#   4. 切库后重新校验 → 恢复附件/扩展卷 → 启动槽位 → 等 /api/v1/ready；
#   5. 只有走到这里才 dropdb 旧库。任何一步失败都自动改名回滚，并打印人工步骤。
#
# 可选环境变量：
#   SFORUM_RESTORE_SKIP_ASSETS=1            只恢复数据库，不动附件/扩展卷
#   SFORUM_DB_READY_ATTEMPTS=40             冷库启动时 pg_isready 重试次数
#   SFORUM_DB_READY_INTERVAL_SECONDS=3      每次重试间隔
#   SFORUM_API_READY_TIMEOUT_SECONDS=180    API 就绪探测超时
#   SFORUM_API_READY_INTERVAL_SECONDS=3     探测间隔
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/scripts/postgres-common.sh
source "$SCRIPT_DIR/postgres-common.sh"
# shellcheck source=deploy/scripts/postgres-backup-format.sh
source "$SCRIPT_DIR/postgres-backup-format.sh"

usage() {
  echo "Usage: deploy/scripts/restore-postgres.sh path/to/backup[.tar.gz.enc|.sql]"
}

if [ "${SFORUM_CONFIRM_RESTORE:-}" != "RESTORE" ]; then
  echo "Set SFORUM_CONFIRM_RESTORE=RESTORE to run a database restore."
  exit 1
fi

if [ "${1:-}" = "" ]; then
  usage
  exit 1
fi

ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

if [ ! -f .env.production ]; then
  echo ".env.production is required."
  exit 1
fi
chmod 600 .env.production

BACKUP_INPUT="$1"
if [ ! -f "$BACKUP_INPUT" ]; then
  echo "Backup file not found: $BACKUP_INPUT"
  exit 1
fi

POSTGRES_DB="$(sforum_env_value .env.production POSTGRES_DB sforum)"
POSTGRES_USER="$(sforum_env_value .env.production POSTGRES_USER sforum)"
POSTGRES_ACTOR="$POSTGRES_USER"

sforum_configure_compose
EXTENSION_CONTAINER_DIR="$(sforum_env_path_value .env.production EXTENSION_ROOT /var/lib/sforum/extensions)"
SFORUM_ATTACHMENT_CONTAINER_DIR="${SFORUM_ATTACHMENT_CONTAINER_DIR:-$SFORUM_ATTACHMENT_CONTAINER_DIR_DEFAULT}"

TARGET_DB="$POSTGRES_DB"
case "$TARGET_DB" in
  ""|postgres|template0|template1)
    echo "Refusing to restore protected PostgreSQL database: ${TARGET_DB:-<empty>}" >&2
    exit 1
    ;;
esac

RESTORE_SUFFIX="$(date -u +%Y%m%d%H%M%S)-$$"
RESTORE_DB="sforum_restore_${RESTORE_SUFFIX}"
PREVIOUS_DB="sforum_before_restore_${RESTORE_SUFFIX}"

# SIGKILL 留下的 $TMPDIR/sforum-restore.* 里同样有 600 的明文 database.sql，而
# retention 只管备份目录，扫不到这里。按「同用户 + 名称匹配 + 超过 1 天」三个条件
# 收，避免踩到并发的另一次恢复（它的工作目录同样匹配这个模式）。
sforum_sweep_stale_restore_dirs() {
  local tmpdir="${TMPDIR:-/tmp}"
  local stale

  while IFS= read -r stale; do
    [ -n "$stale" ] || continue
    rm -rf -- "$stale"
    printf '清理上次异常退出留下的恢复工作目录（含明文转储）：%s\n' "$stale" >&2
  done < <(find "$tmpdir" -mindepth 1 -maxdepth 1 -type d -name 'sforum-restore.*' \
    -user "$(id -u)" -mmin +1440 2>/dev/null | sort)
}
sforum_sweep_stale_restore_dirs

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-restore.XXXXXX")"
MANIFEST="$WORK_DIR/payload/manifest.env"
ASSETS_DIR="$WORK_DIR/payload/assets"
ASSET_SNAPSHOT_DIR="$WORK_DIR/assets-snapshot"

SWAPPED=false
ROLLED_BACK=false
ASSETS_RESTORED=false
SERVICES_STOPPED=false
SERVICES_RESTARTED=false
STOPPED_ADDRS=""
SERVICES_TO_RESTART=()
API_SERVICES=()

print_recovery_hint() {
  local failed_db="$1"
  local start_services="api"

  if [ "${#SERVICES_TO_RESTART[@]}" -gt 0 ]; then
    start_services="${SERVICES_TO_RESTART[*]}"
  fi

  if [ "$ROLLED_BACK" = true ]; then
    cat >&2 <<EOF
已自动回滚：$TARGET_DB 仍是切换前的内容，未通过校验的新库保留为 $failed_db（已禁止连接）。
确认不再需要取证后清理：
  ${SFORUM_COMPOSE[*]} exec -T postgres dropdb --if-exists --force -U $POSTGRES_ACTOR "$failed_db"
备份文件：$BACKUP_INPUT
EOF
    return 0
  fi

  cat >&2 <<EOF
自动回滚未完成，请人工处理（按顺序执行）：
  1) ${SFORUM_COMPOSE[*]} exec -T postgres psql -X -U $POSTGRES_ACTOR -d postgres \\
       -c 'ALTER DATABASE "$TARGET_DB" RENAME TO "$failed_db";' \\
       -c 'ALTER DATABASE "$PREVIOUS_DB" RENAME TO "$TARGET_DB";' \\
       -c 'ALTER DATABASE "$TARGET_DB" WITH ALLOW_CONNECTIONS true;'
  2) ${SFORUM_COMPOSE[*]} start $start_services
  3) 确认新库不再需要取证时：dropdb -U $POSTGRES_ACTOR "$failed_db"
备份文件：$BACKUP_INPUT
EOF
}

restart_stopped_services() {
  [ "$SERVICES_STOPPED" = true ] || return 0
  [ "$SERVICES_RESTARTED" != true ] || return 0
  if ! "${SFORUM_COMPOSE[@]}" start "${SERVICES_TO_RESTART[@]}"; then
    return 1
  fi
  SERVICES_RESTARTED=true
}

rollback_after_swap() {
  local failed_db="sforum_restore_failed_${RESTORE_SUFFIX}"
  local status=0

  printf '切换后校验未通过，回滚到切换前的数据库。\n' >&2
  if "${SFORUM_COMPOSE[@]}" exec -T postgres psql -X --set=ON_ERROR_STOP=1 --single-transaction \
    --set=target_db="$TARGET_DB" \
    --set=previous_db="$PREVIOUS_DB" \
    --set=failed_db="$failed_db" \
    -U "$POSTGRES_ACTOR" \
    -d postgres <<'SQL'
ALTER DATABASE :"target_db" RENAME TO :"failed_db";
ALTER DATABASE :"previous_db" RENAME TO :"target_db";
ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS true;
SQL
  then
    SWAPPED=false
    ROLLED_BACK=true
    printf '已回滚：%s 恢复为切换前内容；未通过校验的新库保留为 %s。\n' "$TARGET_DB" "$failed_db" >&2
    if [ "$ASSETS_RESTORED" = true ]; then
      sforum_restore_asset_snapshots "$ASSET_SNAPSHOT_DIR" || status=1
    fi
  else
    status=1
    printf '自动回滚失败。\n' >&2
  fi
  print_recovery_hint "$failed_db"
  return "$status"
}

cleanup() {
  local status=$?
  trap - EXIT

  if [ "$status" -ne 0 ] && [ "$SWAPPED" = true ]; then
    rollback_after_swap || status=1
  fi
  if [ "$SWAPPED" != true ] && [ -n "$RESTORE_DB" ]; then
    "${SFORUM_COMPOSE[@]}" exec -T postgres dropdb --if-exists --force \
      -U "$POSTGRES_ACTOR" "$RESTORE_DB" >/dev/null 2>&1 || true
  fi
  if ! restart_stopped_services; then
    status=1
  fi
  rm -rf "$WORK_DIR"
  exit "$status"
}
trap cleanup EXIT

# 1. 解密 / 解包 / 内容预检：不碰数据库。
echo "Verifying restore payload: $BACKUP_INPUT"
sforum_prepare_payload "$BACKUP_INPUT" "$WORK_DIR/payload"
RESTORE_SQL="$WORK_DIR/payload/database.sql"
sforum_validate_sql_payload "$RESTORE_SQL"

# 2. 记录本拓扑下正在运行的应用槽位（顺序即停启顺序）。
running_services="$(sforum_running_services)"
# 拓扑与运行态不一致时停手（OPS2-02），两个方向都要防：
#   声明 single、实际在跑槽位 → 「按单拓扑全杀连接」会误杀线上槽；
#   声明 blue-green、实际在跑单拓扑 api/worker → 按槽停启会漏掉它，它的连接
#   留在 terminate 作用域之外，改名照样失败（只是失败得更晚、更难解释）。
# 只防一个方向等于把同一个事故换个入口放进来。
while IFS= read -r running_service; do
  case "$SFORUM_TOPOLOGY:$running_service" in
    single:api-blue|single:api-green|single:worker-blue|single:worker-green)
      sforum_die "检测到蓝绿槽位正在运行（$running_service），但 .deployrc 的 topology 不是 blue-green；先修正拓扑再恢复。"
      ;;
    blue-green:api|blue-green:worker)
      sforum_die "检测到单拓扑服务正在运行（$running_service），但 .deployrc 的 topology 是 blue-green；按槽停启会漏掉它，先修正拓扑再恢复。"
      ;;
  esac
done <<< "$running_services"
app_services=" $(sforum_app_services | tr '\n' ' ') "
while IFS= read -r service; do
  [ -n "$service" ] || continue
  case "$app_services" in
    *" $service "*) SERVICES_TO_RESTART+=("$service") ;;
  esac
done <<< "$running_services"

# bash < 4.4（macOS 自带 3.2）在 set -u 下展开空数组会报 unbound variable：这里
# 没有运行中的应用服务时 SERVICES_TO_RESTART 就是空的，直接展开会在恢复前崩掉。
# 下面的 `${#...} -gt 0` 守卫管的是 241/262 两处「先判空再展开」，与这一行的
# 零词展开写法互不冲突：空数组在这里循环 0 次。
for service in ${SERVICES_TO_RESTART[@]+"${SERVICES_TO_RESTART[@]}"}; do
  case "$service" in
    api) API_SERVICES+=("$service") ;;
    api-*) API_SERVICES+=("$service") ;;
  esac
done

# 连接终止作用域（OPS2-02）：
#   all   单拓扑：只针对目标库的会话（与旧行为一致）
#   slots 蓝绿：只终止「本脚本刚停下的槽」的连接，其它槽绝不碰
#   none  蓝绿且没有槽在跑：一句都不杀，残留连接会让改名失败并触发回滚
TERMINATE_SCOPE="all"
if [ "$SFORUM_TOPOLOGY" = "blue-green" ]; then
  if [ "${#SERVICES_TO_RESTART[@]}" -gt 0 ]; then
    for service in "${SERVICES_TO_RESTART[@]}"; do
      while IFS= read -r address; do
        [ -n "$address" ] || continue
        case ",$STOPPED_ADDRS," in
          *",$address,"*) ;;
          *) STOPPED_ADDRS="${STOPPED_ADDRS:+$STOPPED_ADDRS,}$address" ;;
        esac
      done <<< "$(sforum_service_addresses "$service")"
    done
    if [ -z "$STOPPED_ADDRS" ]; then
      sforum_die "蓝绿拓扑下无法确定槽位容器地址；拒绝无差别终止数据库连接。请先手动停掉运行中的槽位再恢复。"
    fi
    TERMINATE_SCOPE="slots"
    printf 'Slot connections protected for termination scope: %s\n' "$STOPPED_ADDRS"
  else
    TERMINATE_SCOPE="none"
    printf '蓝绿拓扑下没有运行中的槽位：本次不做连接终止，残留连接会让改名失败并触发回滚。\n'
  fi
fi

if [ "${#SERVICES_TO_RESTART[@]}" -gt 0 ]; then
  "${SFORUM_COMPOSE[@]}" stop "${SERVICES_TO_RESTART[@]}"
  SERVICES_STOPPED=true
fi

# 3. 冷库首启（OPS2-03）：慢启动降级成 warning 交给 pg_isready 重试；容器没在跑
# 是真故障，立刻失败，不耗满 attempts × interval。
sforum_start_postgres
sforum_wait_for_postgres "$TARGET_DB"

"${SFORUM_COMPOSE[@]}" exec -T postgres createdb -U "$POSTGRES_ACTOR" \
  --owner "$POSTGRES_ACTOR" --template template0 "$RESTORE_DB"

echo "Restoring PostgreSQL backup into $RESTORE_DB"
"${SFORUM_COMPOSE[@]}" exec -T postgres psql \
  -X \
  --set=ON_ERROR_STOP=1 \
  --single-transaction \
  -U "$POSTGRES_ACTOR" \
  -d "$RESTORE_DB" < "$RESTORE_SQL"

# 4. 切库前的内容校验：关系数 + 核心表 + 清单行数。
sforum_db_validate "$RESTORE_DB" "$MANIFEST"

# 5. 切库。蓝绿拓扑只终止刚停下的槽的连接，单拓扑保持原行为。
#
# 同步义务：这段 SQL 的文本被 postgres-safety_test.sh 的三处 expected 整段 diff
# 比对（C5 的 swap_sql_block、C10 的 slots 段、C11b 的 none 段）。改这里必须同批
# 改那三处，否则变异验证会从「改坏才红」退化成「改了就对不上」。
swap_args=(
  "--set=target_db=$TARGET_DB"
  "--set=restore_db=$RESTORE_DB"
  "--set=previous_db=$PREVIOUS_DB"
)
if [ "$TERMINATE_SCOPE" = "slots" ]; then
  swap_args+=("--set=stopped_addrs=$STOPPED_ADDRS")
fi

{
  cat <<'SQL'
ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS false;
SQL
  if [ "$TERMINATE_SCOPE" != "none" ]; then
    cat <<'SQL'
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = :'target_db' AND pid <> pg_backend_pid()
SQL
    if [ "$TERMINATE_SCOPE" = "slots" ]; then
      cat <<'SQL'
  AND client_addr = ANY (string_to_array(:'stopped_addrs', ',')::inet[])
SQL
    fi
    printf '%s\n' ';'
  fi
  cat <<'SQL'
ALTER DATABASE :"target_db" RENAME TO :"previous_db";
ALTER DATABASE :"restore_db" RENAME TO :"target_db";
SQL
} | "${SFORUM_COMPOSE[@]}" exec -T postgres psql \
  -X \
  --set=ON_ERROR_STOP=1 \
  --single-transaction \
  "${swap_args[@]}" \
  -U "$POSTGRES_ACTOR" \
  -d postgres

SWAPPED=true
RESTORE_DB=""

# 6. 切库后重新校验（改名是元数据操作，但「发布出去的库」必须自己再验一次）。
# quiet：这一份数据刚在步骤 4 校验过，空表 warning 已经打过了，重复第二遍只会
# 让真正的告警变成噪音。
sforum_db_validate "$TARGET_DB" "$MANIFEST" quiet

# 7. 附件卷与扩展制品卷。回滚靠切换前快照。
if [ -f "$ASSETS_DIR/attachments.tar" ] || [ -f "$ASSETS_DIR/extensions.tar" ]; then
  if [ "${SFORUM_RESTORE_SKIP_ASSETS:-0}" = "1" ]; then
    echo "Skipping volume restore (SFORUM_RESTORE_SKIP_ASSETS=1)."
  else
    # 先置位再动手：附件卷解到一半失败时，回滚同样要把已写进去的内容还原。
    ASSETS_RESTORED=true
    sforum_restore_assets "$ASSETS_DIR" "$ASSET_SNAPSHOT_DIR"
    sforum_verify_restored_assets "$ASSETS_DIR" "$WORK_DIR"
  fi
else
  echo "该备份不含附件/扩展制品卷，跳过卷恢复。"
fi

# 8. 启动原槽位并等 API 就绪：这里不通过就不算恢复成功，旧库也还不删。
restart_stopped_services
if [ "${#API_SERVICES[@]}" -gt 0 ]; then
  sforum_wait_for_api_services "${API_SERVICES[@]}"
else
  echo "没有正在运行的 API 槽位，跳过 API 就绪探测。"
fi

# 9. 只有新库通过校验且 API 就绪，才删除切换前的旧库。
# 删不掉只是留了一份取证副本（已禁止连接），不应该把一次成功且已验证的恢复倒回去。
if ! "${SFORUM_COMPOSE[@]}" exec -T postgres dropdb --if-exists --force \
  -U "$POSTGRES_ACTOR" "$PREVIOUS_DB"; then
  printf '警告：旧库未能删除，已禁止连接，可稍后手工清理：%s\n' "$PREVIOUS_DB" >&2
fi
PREVIOUS_DB=""
SWAPPED=false
echo "PostgreSQL restore completed and validated: $TARGET_DB"
