#!/usr/bin/env bash
# 生产 PostgreSQL 备份/恢复的离线安全测试：mock docker，不需要真库、不需要网络。
#
# 覆盖：
#   备份面（OPS2-06）：单一加密归档含库+附件+扩展、缺密钥硬失败、保留策略、旧版 .sql 兼容
#   恢复面（RV-16）：内容预检拒绝、切库后校验、失败自动回滚且不删旧库
#   拓扑（OPS2-02）：蓝绿读 .deployrc、按槽停启、terminate 收窄到已停槽
#   冷库（OPS2-03）：pg_isready 重试与明确超时
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-postgres-safety-test.XXXXXX")"
TEST_ROOT="$TEMP_DIR/repo"
MOCK_BIN="$TEMP_DIR/bin"
MOCK_LOG="$TEMP_DIR/docker.log"
MOCK_DATA="$TEMP_DIR/mock-data"
KEY_FILE="$TEMP_DIR/backup.key"
CHECK_NAMES=()

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'postgres-safety_test.sh: %s\n' "$1" >&2
  exit 1
}

case_ok() {
  CHECK_NAMES+=("$1")
  printf 'ok   %s\n' "$1"
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

reset_log() {
  : > "$MOCK_LOG"
  rm -f "$MOCK_DATA/pg_isready.count"
}

# 从 mock 日志里取出「切库 SQL 的完整连续文本」：只到下一行 exec/compose 为止。
# 语义级断言必须比对整段语句，子串匹配放过 OR true 这类退化。
swap_sql_block() {
  awk '/^exec swap / { inside = 1; next }
       inside && /^(compose|exec|inspect|argv) / { inside = 0 }
       inside { print }' "$1"
}

assert_swap_sql() {
  local expected_file="$1"
  local actual_file="$2"
  local diff_file="$3"
  local label="$4"

  if ! diff -u "$expected_file" "$actual_file" > "$diff_file"; then
    cat "$diff_file" >&2
    fail "$label"
  fi
}

mkdir -p "$TEST_ROOT/deploy/scripts" "$MOCK_BIN" "$MOCK_DATA/cid" "$MOCK_DATA/ip"
cp "$ROOT_DIR/deploy/scripts/backup-postgres.sh" "$TEST_ROOT/deploy/scripts/"
cp "$ROOT_DIR/deploy/scripts/restore-postgres.sh" "$TEST_ROOT/deploy/scripts/"
cp "$ROOT_DIR/deploy/scripts/postgres-common.sh" "$TEST_ROOT/deploy/scripts/"
cp "$ROOT_DIR/deploy/scripts/postgres-backup-format.sh" "$TEST_ROOT/deploy/scripts/"
touch "$TEST_ROOT/compose.yaml" "$TEST_ROOT/compose.prod.yaml"
touch "$TEST_ROOT/compose.release.yaml" "$TEST_ROOT/compose.zero-downtime.yaml"
printf '%s\n' \
  'POSTGRES_DB=sforum_test' \
  'POSTGRES_USER=sforum_test' \
  'EXTENSION_ROOT=/var/lib/sforum/extensions' \
  "UNTRUSTED_VALUE=\$(touch '$TEMP_DIR/env-command-was-executed')" > "$TEST_ROOT/.env.production"
chmod 644 "$TEST_ROOT/.env.production"
printf 'test-passphrase\n' > "$KEY_FILE"
chmod 600 "$KEY_FILE"

# 卷内容（mock 用宿主机目录代表安装点）：备份与恢复都真正读写这里的文件。
mkdir -p "$MOCK_DATA/app/storage/app/attachments/2026"
printf 'png-bytes\n' > "$MOCK_DATA/app/storage/app/attachments/2026/a.png"
mkdir -p "$MOCK_DATA/var/lib/sforum/extensions/demo"
printf '{"name":"demo"}\n' > "$MOCK_DATA/var/lib/sforum/extensions/demo/manifest.json"

cat > "$MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

log() { printf '%s\n' "$*" >> "$MOCK_DOCKER_LOG"; }
rows_for() {
  local name="MOCK_ROWS_$1"
  printf '%s' "${!name:-$2}"
}
# 转储里的真实行数：备份清单的行数就是从这里统计出来的。
dump_rows() {
  case ",${MOCK_EMPTY_TABLES:-}," in
    *",$1,"*) printf '0'; return 0 ;;
  esac
  case "$1" in
    users) printf '12' ;;
    roles) printf '3' ;;
    categories) printf '4' ;;
    topics) printf '6' ;;
    posts) printf '8' ;;
    comments) printf '9' ;;
    attachments) printf '2' ;;
    *) printf '0' ;;
  esac
}

dump_sql() {
  printf '%s\n' '--' '-- PostgreSQL database dump' '--' 'SET statement_timeout = 0;'
  if [ "${MOCK_DUMP_MODE:-full}" = "one_table" ]; then
    printf '%s\n' 'CREATE TABLE public.restored_table (id bigint);'
    return 0
  fi
  local table
  for table in users roles categories topics posts comments attachments; do
    printf 'CREATE TABLE public.%s (id bigint);\n' "$table"
  done
  for table in users roles categories topics posts comments attachments; do
    printf 'COPY public.%s (id) FROM stdin;\n' "$table"
    count="$(dump_rows "$table")"
    row=1
    while [ "$row" -le "$count" ]; do
      printf '%s\n' "$row"
      row=$((row + 1))
    done
    printf '%s\n' '\.'
  done
}

sql_table() {
  local sql="$1"
  local table
  table="$(printf '%s' "$sql" | sed -n 's/.*FROM public\."\([a-z_]*\)".*/\1/p')"
  printf '%s' "$table"
}

psql_exec() {
  local args=("$@")
  local i=0
  local sql=""
  local db=""

  while [ "$i" -lt "${#args[@]}" ]; do
    case "${args[$i]}" in
      -c) i=$((i + 1)); sql="${args[$i]:-}" ;;
      -d) i=$((i + 1)); db="${args[$i]:-}" ;;
    esac
    i=$((i + 1))
  done

  if [ -n "$sql" ]; then
    case "$sql" in
      *to_regclass*)
        log "exec psql missing-tables $db"
        printf '%s\n' "${MOCK_MISSING_TABLES:-}"
        return 0
        ;;
      *pg_class*)
        log "exec psql relation-count $db"
        printf '%s\n' "${MOCK_RELATION_COUNT:-4}"
        return 0
        ;;
      *'count(*) FROM public.'*)
        local table
        table="$(sql_table "$sql")"
        log "exec psql rows $db $table"
        rows_for "$table" "$(dump_rows "$table")"
        printf '\n' 
        return 0
        ;;
      *) exit 92 ;;
    esac
  fi

  case "$db" in
    sforum_restore_*)
      cat > /dev/null
      log "exec restore ${args[*]}"
      if [ "${MOCK_PSQL_FAIL:-false}" = "true" ]; then
        log 'restore failed'
        exit 7
      fi
      ;;
    postgres)
      log "exec swap ${args[*]}"
      if [ "${MOCK_SWAP_FAIL:-false}" = "true" ]; then
        exit 8
      fi
      while IFS= read -r line; do
        log "$line"
      done
      ;;
    *) exit 92 ;;
  esac
}

if [ "${1:-}" != "compose" ]; then
  case "${1:-}" in
    inspect)
      shift
      id=""
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --format) shift 2 ;;
          *) id="$1"; shift ;;
        esac
      done
      log "inspect $id"
      cat "$MOCK_DATA/ip/$id" 2>/dev/null || true
      exit 0
      ;;
    *) exit 90 ;;
  esac
fi

shift
log "argv compose $*"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --env-file|-f|--profile|--project-name) shift 2 ;;
    *) break ;;
  esac
done

command="${1:-}"
shift || true
log "compose ${command}${*:+ $*}"

case "$command" in
  ps)
    if [ "${1:-}" = "-q" ]; then
      service="${!#}"
      cat "$MOCK_DATA/cid/$service" 2>/dev/null || true
      exit 0
    fi
    printf '%s\n' "${MOCK_RUNNING_SERVICES:-api}" | sed '/^[[:space:]]*$/d'
    ;;
  stop|start) ;;
  up)
    # 冷启动时 compose --wait 可能先于 healthcheck 窗口失败，脚本必须继续走重试。
    if [ "${MOCK_UP_FAIL:-false}" = "true" ]; then
      exit 6
    fi
    ;;
  run)
    dir=""
    mode=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --rm|-T|--no-deps) shift ;;
        --entrypoint) shift 2 ;;
        -C) dir="$2"; shift 2 ;;
        -cf) mode="create"; shift; [ "${1:-}" = "-" ] && shift ;;
        -xf) mode="extract"; shift; [ "${1:-}" = "-" ] && shift ;;
        -c) shift 2 ;;
        *) shift ;;
      esac
    done
    host_dir="$MOCK_DATA$dir"
    case "$mode" in
      create)
        [ -d "$host_dir" ] || { printf 'mock: missing volume %s\n' "$host_dir" >&2; exit 95; }
        tar -C "$host_dir" -cf - .
        ;;
      extract)
        mkdir -p "$host_dir"
        tar -C "$host_dir" -xf -
        ;;
      *) exit 95 ;;
    esac
    ;;
  exec)
    [ "${1:-}" = "-T" ] && shift
    service="${1:-}"
    shift || true
    case "$service" in
      postgres)
        inner="${1:-}"
        shift || true
        case "$inner" in
          pg_isready)
            log "exec pg_isready $*"
            count_file="$MOCK_DATA/pg_isready.count"
            count=$(( $(cat "$count_file" 2>/dev/null || printf '0') + 1 ))
            printf '%s' "$count" > "$count_file"
            [ "$count" -gt "${MOCK_PG_READY_FAIL_TIMES:-0}" ] || exit 1
            ;;
          createdb) log "exec createdb $*" ;;
          dropdb) log "exec dropdb $*" ;;
          pg_dump)
            log "exec pg_dump $*"
            dump_sql
            if [ "${MOCK_PG_DUMP_FAIL:-false}" = "true" ]; then
              printf '%s\n' '-- partial output before failure'
              exit 9
            fi
            ;;
          psql) psql_exec "$@" ;;
          *) exit 93 ;;
        esac
        ;;
      api|api-blue|api-green)
        log "exec $service $*"
        [ "${MOCK_API_READY:-true}" = "true" ] || exit 1
        ;;
      *) exit 91 ;;
    esac
    ;;
  *) exit 94 ;;
esac
EOF
chmod +x "$MOCK_BIN/docker"

export PATH="$MOCK_BIN:$PATH"
export MOCK_DOCKER_LOG="$MOCK_LOG"
export MOCK_DATA
export SFORUM_BACKUP_KEY_FILE="$KEY_FILE"

# --- C1 备份：单一加密归档，含库 + 附件 + 扩展制品 ---------------------------
reset_log
SUCCESS_BACKUP_DIR="$TEMP_DIR/backups-success"
backup_output="$(cd "$TEST_ROOT" && SFORUM_BACKUP_DIR="$SUCCESS_BACKUP_DIR" ./deploy/scripts/backup-postgres.sh)"
archive="$(tail -n 1 <<< "$backup_output")"
[[ -s "$archive" ]] || fail "successful backup is missing"
[[ "$archive" == *.tar.gz.enc ]] || fail "backup is not a single encrypted archive: $archive"
[[ "$(file_mode "$archive")" == "600" ]] || fail "backup archive mode is not 0600"
[[ "$(file_mode "$TEST_ROOT/.env.production")" == "600" ]] || fail ".env.production mode is not 0600"
[[ ! -e "$TEMP_DIR/env-command-was-executed" ]] || fail "backup executed shell content from .env.production"
if find "$SUCCESS_BACKUP_DIR" -maxdepth 1 -name '.*' | grep -q .; then
  fail "successful backup left a temporary file"
fi

payload="$TEMP_DIR/payload.tar.gz"
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -pass file:"$KEY_FILE" -in "$archive" -out "$payload"
tar -tzf "$payload" >/dev/null 2>&1 || fail "backup payload is not a readable archive"
archive_listing="$(tar -tzf "$payload")"
for entry in manifest.env database.sql assets/attachments.tar assets/extensions.tar; do
  grep -qx "$entry" <<< "$archive_listing" || fail "backup archive is missing $entry"
done
mkdir -p "$TEMP_DIR/unpack"
tar -C "$TEMP_DIR/unpack" -xzf "$payload" manifest.env database.sql
grep -q '^format_version=2$' "$TEMP_DIR/unpack/manifest.env" || fail "manifest format_version is wrong"
grep -q '^core_tables=users,roles,categories,topics,posts,comments,attachments$' "$TEMP_DIR/unpack/manifest.env" || \
  fail "manifest does not record the core table set"
grep -q '^row_count.users=12$' "$TEMP_DIR/unpack/manifest.env" || fail "manifest is missing row counts"
grep -q '^attachments_entries=1$' "$TEMP_DIR/unpack/manifest.env" || fail "manifest is missing attachment entries"
grep -q '^extension_entries=1$' "$TEMP_DIR/unpack/manifest.env" || fail "manifest is missing extension entries"
grep -q '^CREATE TABLE public\.users ' "$TEMP_DIR/unpack/database.sql" || fail "database dump has no core tables"
grep -q 'exec pg_dump ' "$MOCK_LOG" || fail "backup did not call pg_dump"
grep -q '^compose up -d --wait postgres$' "$MOCK_LOG" || fail "backup did not wait for postgres to be healthy"
case_ok "C1 备份=单一加密归档（库+附件+扩展+清单，0600，无临时残留）"

# --- C2 保留策略：份数 N + 天数，消息走 stderr，末行仍是归档路径 -------------
RETENTION_DIR="$TEMP_DIR/backups-retention"
mkdir -p "$RETENTION_DIR"
for stamp in 20200101T000000Z 20210101T000000Z 20220101T000000Z 20230101T000000Z; do
  printf 'old\n' > "$RETENTION_DIR/sforum-backup-$stamp.tar.gz.enc"
done
touch -d '60 days ago' "$RETENTION_DIR/sforum-backup-20200101T000000Z.tar.gz.enc"
reset_log
retention_output="$(cd "$TEST_ROOT" && \
  SFORUM_BACKUP_DIR="$RETENTION_DIR" \
  SFORUM_BACKUP_RETENTION_COUNT=2 \
  SFORUM_BACKUP_RETENTION_DAYS=30 \
  ./deploy/scripts/backup-postgres.sh 2>"$TEMP_DIR/retention.err")"
retention_archive="$(tail -n 1 <<< "$retention_output")"
[[ -f "$retention_archive" ]] || fail "retention run did not report the new archive on the last stdout line"
remaining="$(find "$RETENTION_DIR" -maxdepth 1 -type f -name 'sforum-backup-*' | wc -l | tr -d ' ')"
[[ "$remaining" == "2" ]] || fail "retention kept $remaining archive(s), expected 2"
grep -q 'older than 30 day' "$TEMP_DIR/retention.err" || fail "retention did not apply the day limit"
grep -q 'keeping newest 2' "$TEMP_DIR/retention.err" || fail "retention did not apply the count limit"
[[ "$(find "$RETENTION_DIR" -maxdepth 1 -type f -name 'sforum-backup-*' | sort -r | head -n 1)" == "$retention_archive" ]] || \
  fail "retention removed the archive it had just created"
case_ok "C2 保留策略：超过天数/份数的归档被删，新建归档保留（末行仍是路径）"

# --- C3 缺密钥：明确失败，且一个字节都不落盘 --------------------------------
MISSING_KEY_DIR="$TEMP_DIR/backups-missing-key"
reset_log
if (cd "$TEST_ROOT" && unset SFORUM_BACKUP_KEY_FILE; \
  SFORUM_BACKUP_DIR="$MISSING_KEY_DIR" ./deploy/scripts/backup-postgres.sh 2>"$TEMP_DIR/nokey.err"); then
  fail "backup without an encryption key unexpectedly succeeded"
fi
grep -q '缺少加密密钥' "$TEMP_DIR/nokey.err" || fail "missing-key failure did not explain the problem"
if find "$MISSING_KEY_DIR" -maxdepth 1 -type f 2>/dev/null | grep -q .; then
  fail "missing-key backup left a plaintext file behind"
fi
case_ok "C3 缺密钥：备份硬失败、不写明文（OPS2-06）"

# --- C4 密钥文件必须是 0600 ------------------------------------------------
LOOSE_KEY="$TEMP_DIR/loose.key"
printf 'test-passphrase\n' > "$LOOSE_KEY"
chmod 644 "$LOOSE_KEY"
reset_log
if (cd "$TEST_ROOT" && SFORUM_BACKUP_KEY_FILE="$LOOSE_KEY" \
  SFORUM_BACKUP_DIR="$TEMP_DIR/backups-loose-key" ./deploy/scripts/backup-postgres.sh 2>"$TEMP_DIR/loose.err"); then
  fail "backup accepted a world-readable key file"
fi
grep -q '必须是 0600' "$TEMP_DIR/loose.err" || fail "loose key file rejection did not explain the mode requirement"
case_ok "C4 密钥文件权限：非 0600 直接拒绝"

# --- C5 成功恢复：解密 + 解包 + 灌库 + 校验 + 切库 + 卷恢复 + 就绪 ----------
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" >/dev/null)
[[ ! -e "$TEMP_DIR/env-command-was-executed" ]] || fail "restore executed shell content from .env.production"
grep -q '^compose stop api$' "$MOCK_LOG" || fail "restore did not stop API"
if grep -q '^compose stop .*worker' "$MOCK_LOG"; then
  fail "restore attempted to stop a legacy standalone Worker"
fi
grep -q 'exec restore .*--set=ON_ERROR_STOP=1.*--single-transaction' "$MOCK_LOG" || fail "restore is missing fail-fast single-transaction flags"
grep -q 'ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS false;' "$MOCK_LOG" || fail "restore did not close target database admission"
grep -q 'ALTER DATABASE :"target_db" RENAME TO :"previous_db";' "$MOCK_LOG" || fail "restore did not retain the old target until swap"
grep -q 'ALTER DATABASE :"restore_db" RENAME TO :"target_db";' "$MOCK_LOG" || fail "restore did not publish the validated database"
grep -q '^compose start api$' "$MOCK_LOG" || fail "restore did not restart the previously running API"
if grep -q '^compose start .*worker' "$MOCK_LOG"; then
  fail "restore attempted to restart a legacy standalone Worker"
fi
grep -q 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG" || fail "successful restore did not drop the previous database"
grep -q 'exec api wget -q -O /dev/null http://127.0.0.1:8080/api/v1/ready' "$MOCK_LOG" || fail "restore did not probe API readiness"
[[ -f "$MOCK_DATA/app/storage/app/attachments/2026/a.png" ]] || fail "restore did not bring attachment files back"
[[ -f "$MOCK_DATA/var/lib/sforum/extensions/demo/manifest.json" ]] || fail "restore did not bring extension artifacts back"
swap_line="$(grep -n 'exec swap ' "$MOCK_LOG" | head -n 1 | cut -d: -f1)"
drop_line="$(grep -n 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG" | head -n 1 | cut -d: -f1)"
[[ -n "$swap_line" && -n "$drop_line" && "$swap_line" -lt "$drop_line" ]] || \
  fail "old database was dropped before the swap/final validation"
cat > "$TEMP_DIR/expected-single-swap.sql" <<'SQL'
ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS false;
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = :'target_db' AND pid <> pg_backend_pid()
;
ALTER DATABASE :"target_db" RENAME TO :"previous_db";
ALTER DATABASE :"restore_db" RENAME TO :"target_db";
SQL
swap_sql_block "$MOCK_LOG" > "$TEMP_DIR/actual-single-swap.sql"
assert_swap_sql "$TEMP_DIR/expected-single-swap.sql" "$TEMP_DIR/actual-single-swap.sql" \
  "$TEMP_DIR/single-swap.diff" "single-topology swap SQL does not match the reviewed statement"
case_ok "C5 成功恢复：内容校验后切库、卷恢复、API 就绪后才删旧库"

# --- C6 备份只含 1 张表：拒绝，且不建库/不切库 ------------------------------
printf '%s\n' '--' '-- PostgreSQL database dump' 'SET statement_timeout = 0;' \
  'CREATE TABLE public.restored_table (id bigint);' > "$TEMP_DIR/one-table.sql"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$TEMP_DIR/one-table.sql" \
  >/dev/null 2>"$TEMP_DIR/one-table.err"); then
  fail "restore accepted a backup that only contains one table"
fi
grep -q '缺少核心表' "$TEMP_DIR/one-table.err" || fail "content rejection did not name the missing core tables"
if grep -q 'exec createdb ' "$MOCK_LOG"; then
  fail "content rejection still created a restore database"
fi
if grep -q 'exec swap ' "$MOCK_LOG"; then
  fail "content rejection still swapped databases"
fi
if grep -q '^compose stop ' "$MOCK_LOG"; then
  fail "content rejection stopped application services needlessly"
fi
case_ok "C6 内容不可信：只含 1 张表 → 拒绝且不建库/不切库（RV-16）"

# --- C6b 行数与备份清单不一致：拒绝，且不切库 ------------------------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_ROWS_users=11 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/row-mismatch.err"); then
  fail "restore accepted a database whose row count contradicts the backup manifest"
fi
grep -q 'has 11 row(s) but the backup recorded 12' "$TEMP_DIR/row-mismatch.err" || \
  fail "row-count mismatch did not name the table and both counts"
if grep -q 'exec swap ' "$MOCK_LOG"; then
  fail "row-count mismatch still swapped databases"
fi
case_ok "C6b 行数与清单不一致：拒绝且不切库（内容可信不仅是「表存在」）"

# --- C6c 核心表为空：只告警、不判失败 --------------------------------------
EMPTY_BACKUP_DIR="$TEMP_DIR/backups-empty"
reset_log
empty_backup="$(cd "$TEST_ROOT" && MOCK_EMPTY_TABLES=users \
  SFORUM_BACKUP_DIR="$EMPTY_BACKUP_DIR" ./deploy/scripts/backup-postgres.sh)"
empty_archive="$(tail -n 1 <<< "$empty_backup")"
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -pass file:"$KEY_FILE" \
  -in "$empty_archive" -out "$TEMP_DIR/empty-payload.tar.gz"
mkdir -p "$TEMP_DIR/empty-unpack"
tar -C "$TEMP_DIR/empty-unpack" -xzf "$TEMP_DIR/empty-payload.tar.gz" manifest.env
grep -q '^row_count.users=0$' "$TEMP_DIR/empty-unpack/manifest.env" || \
  fail "empty-table backup did not record row_count.users=0"
reset_log
if ! (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true MOCK_EMPTY_TABLES=users \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$empty_archive" \
  >/dev/null 2>"$TEMP_DIR/empty-core.err"); then
  fail "restore failed on a structurally complete backup whose core table is empty"
fi
grep -qx '警告：核心表为空（备份可能来自空库、staging 或测试库）：users' "$TEMP_DIR/empty-core.err" || \
  fail "empty core table did not produce the explicit warning"
if grep -q '：users,roles\|：users,' "$TEMP_DIR/empty-core.err"; then
  fail "empty-core-table warning listed tables that do have rows"
fi
grep -q 'exec swap ' "$MOCK_LOG" || fail "empty-core-table restore did not proceed to the swap"
case_ok "C6c 核心表为空：显式 warning，恢复照常完成（不硬失败）"

# --- C7 切库后 API 起不来：自动回滚，旧库不删 ------------------------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=false \
  SFORUM_API_READY_TIMEOUT_SECONDS=1 SFORUM_API_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/rollback.err"); then
  fail "restore reported success although the API never became ready"
fi
grep -q 'exec swap ' "$MOCK_LOG" || fail "rollback case never reached the swap"
grep -q 'ALTER DATABASE :"target_db" RENAME TO :"failed_db";' "$MOCK_LOG" || fail "restore did not rename the failed database aside"
grep -q 'ALTER DATABASE :"previous_db" RENAME TO :"target_db";' "$MOCK_LOG" || fail "restore did not restore the previous database name"
grep -q 'ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS true;' "$MOCK_LOG" || fail "rollback did not reopen the restored database"
if grep -q 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG"; then
  fail "failed restore deleted the previous database"
fi
grep -q '已自动回滚' "$TEMP_DIR/rollback.err" || fail "rollback did not report the automatic rollback"
grep -q 'dropdb --if-exists --force -U sforum_test "sforum_restore_failed_' "$TEMP_DIR/rollback.err" || \
  fail "rollback did not print manual cleanup steps for the failed database"
grep -q '^compose start api$' "$MOCK_LOG" || fail "rollback did not restart the API"
case_ok "C7 切库后 API 起不来：自动改名回滚、旧库保留、打印人工步骤（RV-16）"

# --- C8 冷库首启：pg_isready 第一次失败、第二次成功 --------------------------
reset_log
if ! (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true MOCK_PG_READY_FAIL_TIMES=1 \
  SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" >/dev/null 2>&1); then
  fail "cold-start restore failed even though only the first readiness probe was allowed to fail"
fi
ready_attempts="$(grep -c 'exec pg_isready ' "$MOCK_LOG")"
[[ "$ready_attempts" == "2" ]] || fail "expected 2 pg_isready attempts on a cold database, saw $ready_attempts"
grep -q '^compose up -d --wait postgres$' "$MOCK_LOG" || fail "restore did not use up --wait for postgres"
grep -q 'exec createdb ' "$MOCK_LOG" || fail "restore did not continue after the first readiness probe failed"
case_ok "C8 冷库首启：pg_isready 失败一次后重试成功（OPS2-03）"

# --- C8b compose --wait 非零：不中止，交给 pg_isready 重试判定 ---------------
reset_log
if ! (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true MOCK_UP_FAIL=true \
  MOCK_PG_READY_FAIL_TIMES=1 SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/up-fail.err"); then
  fail "restore aborted when compose up --wait failed although pg_isready then recovered"
fi
grep -q '未在 --wait 窗口内就绪' "$TEMP_DIR/up-fail.err" || fail "restore did not warn about the failed --wait window"
up_attempts="$(grep -c 'exec pg_isready ' "$MOCK_LOG")"
[[ "$up_attempts" == "2" ]] || fail "restore did not fall through to the pg_isready retries (saw $up_attempts attempts)"
grep -q 'exec createdb ' "$MOCK_LOG" || fail "restore never created the restore database after the retries recovered"
case_ok "C8b compose --wait 非零：只警告，pg_isready 重试成功后继续（OPS2-03）"

# --- C9 冷库一直不 ready：重试次数用尽后明确失败 ----------------------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_PG_READY_FAIL_TIMES=99 \
  SFORUM_DB_READY_ATTEMPTS=3 SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/cold.err"); then
  fail "restore succeeded although postgres never became ready"
fi
cold_attempts="$(grep -c 'exec pg_isready ' "$MOCK_LOG")"
[[ "$cold_attempts" == "3" ]] || fail "expected 3 pg_isready attempts before giving up, saw $cold_attempts"
grep -q 'did not become ready' "$TEMP_DIR/cold.err" || fail "cold database timeout did not report an explicit timeout"
if grep -q 'exec createdb ' "$MOCK_LOG"; then
  fail "restore created a database although postgres was never ready"
fi
case_ok "C9 冷库超时：重试用尽后明确失败（OPS2-03）"

# --- C9b compose --wait 非零且始终不 ready：明确失败、不建库 ----------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_UP_FAIL=true MOCK_PG_READY_FAIL_TIMES=99 \
  SFORUM_DB_READY_ATTEMPTS=3 SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/up-fail-exhausted.err"); then
  fail "restore succeeded although compose --wait failed and postgres never became ready"
fi
grep -q '未在 --wait 窗口内就绪' "$TEMP_DIR/up-fail-exhausted.err" || \
  fail "restore did not warn about the failed --wait window before giving up"
exhausted_attempts="$(grep -c 'exec pg_isready ' "$MOCK_LOG")"
[[ "$exhausted_attempts" == "3" ]] || fail "expected 3 pg_isready attempts, saw $exhausted_attempts"
grep -q 'did not become ready' "$TEMP_DIR/up-fail-exhausted.err" || fail "retry exhaustion did not report an explicit timeout"
if grep -q 'exec createdb ' "$MOCK_LOG"; then
  fail "restore created a database although postgres never became ready"
fi
case_ok "C9b compose --wait 非零且重试用尽：明确失败、不建库（OPS2-03）"

# --- C10 蓝绿拓扑：compose 组合 + 按槽停启 + terminate 只针对已停槽 ----------
cat > "$TEST_ROOT/.deployrc" <<'RC'
# 部署状态文件：注释与空行必须被忽略

topology=blue-green
version=v3.0.0
blue_version=v3.0.0
green_version=v3.0.1
RC
printf 'edge-cid\n' > "$MOCK_DATA/cid/edge"
printf '10.77.0.9 ' > "$MOCK_DATA/ip/edge-cid"
printf 'api-blue-cid\n' > "$MOCK_DATA/cid/api-blue"
printf '10.77.0.2 ' > "$MOCK_DATA/ip/api-blue-cid"
printf 'worker-blue-cid\n' > "$MOCK_DATA/cid/worker-blue"
printf '10.77.0.3 ' > "$MOCK_DATA/ip/worker-blue-cid"
printf 'api-green-cid\n' > "$MOCK_DATA/cid/api-green"
printf '10.77.0.4 ' > "$MOCK_DATA/ip/api-green-cid"

rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge\napi-blue\nworker-blue\napi-green' \
  MOCK_API_READY=true SFORUM_CONFIRM_RESTORE=RESTORE \
  ./deploy/scripts/restore-postgres.sh "$archive" >"$TEMP_DIR/blue-green.out" 2>&1) || {
  cat "$TEMP_DIR/blue-green.out" >&2
  fail "blue-green restore failed"
}
grep -q 'compose.zero-downtime.yaml --profile zero-downtime' "$MOCK_LOG" || \
  fail "blue-green restore did not use the zero-downtime compose set"
grep -q 'compose.release.yaml' "$MOCK_LOG" || fail "blue-green restore did not use the release compose file"
grep -q '^compose stop api-blue worker-blue api-green$' "$MOCK_LOG" || fail "blue-green restore did not stop the running slots by slot"
grep -q '^compose start api-blue worker-blue api-green$' "$MOCK_LOG" || fail "blue-green restore did not restart the slots it stopped"
if grep -q '^compose stop api$' "$MOCK_LOG"; then
  fail "blue-green restore fell back to the single-topology service name"
fi
if grep -q 'compose stop edge\|compose start edge' "$MOCK_LOG"; then
  fail "blue-green restore touched the ingress instead of the slots"
fi
if grep -q '10\.77\.0\.9' "$MOCK_LOG"; then
  fail "blue-green restore referenced a non-slot container address (ingress)"
fi
grep -q -- '--set=stopped_addrs=10.77.0.2,10.77.0.3,10.77.0.4' "$MOCK_LOG" || \
  fail "blue-green restore did not scope connection termination to the stopped slots"
cat > "$TEMP_DIR/expected-blue-green-swap.sql" <<'SQL'
ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS false;
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = :'target_db' AND pid <> pg_backend_pid()
  AND client_addr = ANY (string_to_array(:'stopped_addrs', ',')::inet[])
;
ALTER DATABASE :"target_db" RENAME TO :"previous_db";
ALTER DATABASE :"restore_db" RENAME TO :"target_db";
SQL
swap_sql_block "$MOCK_LOG" > "$TEMP_DIR/actual-blue-green-swap.sql"
assert_swap_sql "$TEMP_DIR/expected-blue-green-swap.sql" "$TEMP_DIR/actual-blue-green-swap.sql" \
  "$TEMP_DIR/blue-green-swap.diff" "blue-green swap SQL does not match the reviewed statement"
grep -q 'Slot connections protected for termination scope: 10.77.0.2,10.77.0.3,10.77.0.4' "$TEMP_DIR/blue-green.out" || \
  fail "blue-green restore did not report the termination scope"
grep -q 'exec api-blue wget' "$MOCK_LOG" || fail "blue-green restore did not probe api-blue readiness"
grep -q 'exec api-green wget' "$MOCK_LOG" || fail "blue-green restore did not probe api-green readiness"
[[ -f "$MOCK_DATA/app/storage/app/attachments/2026/a.png" ]] || fail "blue-green restore lost attachments"
case_ok "C10 蓝绿拓扑：按槽停启、terminate 收窄到已停槽、不碰 ingress（OPS2-02）"

# --- C11 蓝绿只跑一个槽：另一个槽完全不被提及 -------------------------------
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge\napi-green' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" >/dev/null)
grep -q '^compose stop api-green$' "$MOCK_LOG" || fail "single-slot blue-green restore did not stop the running slot"
grep -q '^compose start api-green$' "$MOCK_LOG" || fail "single-slot blue-green restore did not restart the running slot"
if grep -q 'api-blue\|worker-blue\|worker-green' "$MOCK_LOG"; then
  fail "single-slot blue-green restore touched the slot that was not running"
fi
grep -q -- '--set=stopped_addrs=10.77.0.4' "$MOCK_LOG" || fail "termination scope included services that were never stopped"
case_ok "C11 蓝绿单槽：未运行的槽既不停也不启、不进 terminate 作用域"

# --- C11b 蓝绿但没有任何槽在跑：一句连接都不杀 ------------------------------
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/no-slot.out" 2>&1) || {
  cat "$TEMP_DIR/no-slot.out" >&2
  fail "blue-green restore failed when no slot was running"
}
if grep -q 'pg_terminate_backend' "$MOCK_LOG"; then
  fail "blue-green restore terminated connections although no slot had been stopped"
fi
grep -q '不做连接终止' "$TEMP_DIR/no-slot.out" || \
  fail "blue-green restore did not explain why no connection was terminated"
if grep -q '^compose stop \|^compose start ' "$MOCK_LOG"; then
  fail "blue-green restore started or stopped services it never found running"
fi
grep -q 'exec swap ' "$MOCK_LOG" || fail "blue-green no-slot restore did not swap the database"
cat > "$TEMP_DIR/expected-no-slot-swap.sql" <<'SQL'
ALTER DATABASE :"target_db" WITH ALLOW_CONNECTIONS false;
ALTER DATABASE :"target_db" RENAME TO :"previous_db";
ALTER DATABASE :"restore_db" RENAME TO :"target_db";
SQL
swap_sql_block "$MOCK_LOG" > "$TEMP_DIR/actual-no-slot-swap.sql"
assert_swap_sql "$TEMP_DIR/expected-no-slot-swap.sql" "$TEMP_DIR/actual-no-slot-swap.sql" \
  "$TEMP_DIR/no-slot-swap.diff" "no-slot blue-green swap SQL must not terminate any connection"
case_ok "C11b 蓝绿无槽在跑：不执行 pg_terminate_backend（残留连接只让改名失败回滚）"

# --- C12 .deployrc 非法值：abort，不猜默认 ----------------------------------
printf '%s\n' 'topology=dual-write' > "$TEST_ROOT/.deployrc"
reset_log
if (cd "$TEST_ROOT" && SFORUM_BACKUP_DIR="$TEMP_DIR/backups-bad-rc" ./deploy/scripts/backup-postgres.sh \
  >/dev/null 2>"$TEMP_DIR/bad-rc.err"); then
  fail "backup accepted an invalid topology in .deployrc"
fi
grep -q 'Invalid topology in .deployrc: dual-write' "$TEMP_DIR/bad-rc.err" || fail "invalid topology error message is unclear"
printf '%s\n' 'topology=blue-green' 'version=v3.0.0' 'blue_version=3.0' 'green_version=v3.0.1' > "$TEST_ROOT/.deployrc"
if (cd "$TEST_ROOT" && SFORUM_BACKUP_DIR="$TEMP_DIR/backups-bad-rc" ./deploy/scripts/backup-postgres.sh \
  >/dev/null 2>"$TEMP_DIR/bad-version.err"); then
  fail "backup accepted an invalid blue_version in .deployrc"
fi
grep -q 'Invalid blue_version in .deployrc' "$TEMP_DIR/bad-version.err" || fail "invalid blue_version error message is unclear"
rm -f "$TEST_ROOT/.deployrc"
case_ok "C12 .deployrc 非法值（topology / blue_version）→ abort"

# --- C13 旧版明文 .sql 备份仍然可恢复 --------------------------------------
{
  printf '%s\n' '--' '-- PostgreSQL database dump'
  for table in users roles categories topics posts comments attachments; do
    printf 'CREATE TABLE public.%s (id bigint);\n' "$table"
  done
} > "$TEMP_DIR/legacy.sql"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$TEMP_DIR/legacy.sql" \
  >"$TEMP_DIR/legacy.out" 2>&1)
grep -q 'exec swap ' "$MOCK_LOG" || fail "legacy .sql restore did not swap the database"
grep -q '该备份不含附件/扩展制品卷' "$TEMP_DIR/legacy.out" || fail "legacy .sql restore did not report the missing asset volumes"
if grep -q '^compose run ' "$MOCK_LOG"; then
  fail "legacy .sql restore tried to restore volume contents it does not have"
fi
case_ok "C13 旧版明文 .sql 备份：仍可恢复，跳过卷恢复"

# --- C14 加密归档缺密钥：拒绝恢复，不建库 ----------------------------------
reset_log
if (cd "$TEST_ROOT" && unset SFORUM_BACKUP_KEY_FILE; MOCK_RUNNING_SERVICES='api' \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/restore-nokey.err"); then
  fail "restore accepted an encrypted archive without a key"
fi
grep -q '缺少加密密钥' "$TEMP_DIR/restore-nokey.err" || fail "restore without a key did not explain the problem"
if grep -q 'exec createdb ' "$MOCK_LOG"; then
  fail "restore without a key still created a database"
fi
case_ok "C14 加密归档缺密钥：恢复硬失败、不建库"

# --- C15 pg_dump 失败：不留部分归档 ----------------------------------------
FAILED_BACKUP_DIR="$TEMP_DIR/backups-failed"
reset_log
if (cd "$TEST_ROOT" && MOCK_PG_DUMP_FAIL=true \
  SFORUM_BACKUP_DIR="$FAILED_BACKUP_DIR" ./deploy/scripts/backup-postgres.sh >/dev/null 2>&1); then
  fail "failed pg_dump unexpectedly produced a successful backup"
fi
if find "$FAILED_BACKUP_DIR" -maxdepth 1 -type f | grep -q .; then
  fail "failed pg_dump left a partial backup"
fi
case_ok "C15 pg_dump 失败：清理临时目录、不留部分归档"

# --- C16 .env.production 的值不会被 shell 求值 ------------------------------
printf '%s\n' \
  "POSTGRES_DB=\$(touch '$TEMP_DIR/database-value-was-executed')" \
  'POSTGRES_USER=sforum_test' > "$TEST_ROOT/.env.production"
reset_log
if (cd "$TEST_ROOT" && SFORUM_BACKUP_DIR="$TEMP_DIR/backups-invalid-env" ./deploy/scripts/backup-postgres.sh \
  >/dev/null 2>&1); then
  fail "backup accepted an unsafe POSTGRES_DB value"
fi
[[ ! -e "$TEMP_DIR/database-value-was-executed" ]] || fail "backup executed an unsafe POSTGRES_DB value"
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' SFORUM_CONFIRM_RESTORE=RESTORE \
  ./deploy/scripts/restore-postgres.sh "$TEMP_DIR/legacy.sql" >/dev/null 2>&1); then
  fail "restore accepted an unsafe POSTGRES_DB value"
fi
[[ ! -e "$TEMP_DIR/database-value-was-executed" ]] || fail "restore executed an unsafe POSTGRES_DB value"
case_ok "C16 .env.production 值只做字面读取，不被 shell 求值"

# 复原一份有效配置：后续用例仍要跑完整流程。
printf '%s\n' \
  'POSTGRES_DB=sforum_test' \
  'POSTGRES_USER=sforum_test' \
  'EXTENSION_ROOT=/var/lib/sforum/extensions' > "$TEST_ROOT/.env.production"
chmod 600 "$TEST_ROOT/.env.production"

# --- C17 灌库失败：临时库被清理、不切库、API 重新拉起 -----------------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_PSQL_FAIL=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/psql-fail.err"); then
  fail "restore unexpectedly succeeded after a psql failure"
fi
grep -q '^restore failed$' "$MOCK_LOG" || fail "psql failure path was not exercised"
if grep -q '^exec swap ' "$MOCK_LOG"; then
  fail "failed restore attempted to publish the temporary database"
fi
grep -q 'exec dropdb .*sforum_restore_' "$MOCK_LOG" || fail "failed restore did not remove its temporary database"
grep -q '^compose start api$' "$MOCK_LOG" || fail "failed restore did not restart the previously running API"
if grep -q '^compose start .*worker' "$MOCK_LOG"; then
  fail "failed restore attempted to restart a legacy standalone Worker"
fi
case_ok "C17 灌库失败：临时库清理、不切库、API 重新拉起"

# --- C18 deploy.sh 侧的既有契约不变 ----------------------------------------
grep -qF "wait-for-health.sh \"http://127.0.0.1:\${api_port}/api/v1/ready\"" "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not wait for API readiness"
grep -qF "wait-for-health.sh \"http://127.0.0.1:\${web_port}/health\"" "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not wait for Web readiness"
grep -q 'chmod 600 .env.production' "$ROOT_DIR/deploy.sh" || fail "deploy does not protect production configuration"
case_ok "C18 deploy.sh 侧既有契约（健康探测、.env 权限）保持"

printf '\npostgres-safety_test.sh: all checks passed (%s cases)\n' "${#CHECK_NAMES[@]}"
printf 'cases:\n'
for name in "${CHECK_NAMES[@]}"; do
  printf '  - %s\n' "$name"
done
