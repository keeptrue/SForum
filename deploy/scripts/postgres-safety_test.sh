#!/usr/bin/env bash
# 生产 PostgreSQL 备份/恢复的离线安全测试：mock docker，不需要真库、不需要网络。
#
# 覆盖：
#   备份面（OPS2-06）：单一加密归档含库+附件+扩展、缺密钥硬失败、保留策略、旧版 .sql 兼容
#   加密面（P3-22）：v2 信封的 HMAC 先验后解、密文篡改拒绝、信封剥离拒绝、v1 旧归档兼容
#   恢复面（RV-16）：内容预检拒绝、切库后校验、失败自动回滚且不删旧库
#   恢复后验证（R7-OPS-04）：没有槽在跑也按拓扑拉起 API 并探测，未就绪不删旧库
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
  rm -f "$MOCK_DATA/pg_isready.count" "$MOCK_DATA"/probe_*.count "$MOCK_DATA/date.count"
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

# 用生产代码解密 v2 归档：外层是「信封头 + 密文」，裸 openssl 解不了整份文件。
decrypt_archive() {
  local archive="$1"
  local out="$2"

  (
    set -euo pipefail
    # shellcheck source=deploy/scripts/postgres-common.sh
    source "$ROOT_DIR/deploy/scripts/postgres-common.sh"
    # shellcheck source=deploy/scripts/postgres-backup-format.sh
    source "$ROOT_DIR/deploy/scripts/postgres-backup-format.sh"
    sforum_require_backup_key "测试解密"
    sforum_decrypt_envelope "$archive" "$out"
  )
}

# 信封头字节数也取生产实现，测试不复制切分逻辑。
envelope_header_bytes() {
  local archive="$1"

  (
    set -euo pipefail
    # shellcheck source=deploy/scripts/postgres-common.sh
    source "$ROOT_DIR/deploy/scripts/postgres-common.sh"
    # shellcheck source=deploy/scripts/postgres-backup-format.sh
    source "$ROOT_DIR/deploy/scripts/postgres-backup-format.sh"
    sforum_envelope_header_bytes "$archive"
  )
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
    # 容器状态查询（真 docker: running / exited / restarting / created）。
    # sforum_start_postgres 用它区分「慢启动」与「真故障」；调用形态是
    # `ps --all --format '{{.State}}' <service>`，所以按参数扫描而不是看 $1。
    #
    # 这里必须照抄真 compose 的一条语义：**不带 --all 时只列 running 的容器**。
    # 否则「生产代码忘了 --all」这条变异打不出来 —— 测试会变成自证。
    ps_all=false
    ps_format=false
    for ps_arg in "$@"; do
      case "$ps_arg" in
        --all|-a) ps_all=true ;;
        --format) ps_format=true ;;
      esac
    done
    if [ "$ps_format" = true ]; then
      if [ "$ps_all" = true ] || [ "${MOCK_CONTAINER_STATE:-running}" = "running" ]; then
        printf '%s\n' "${MOCK_CONTAINER_STATE:-running}"
      fi
      exit 0
    fi
    # 用 `-` 而不是 `:-`：`:-` 会把空值当成「未设置」退回 api，于是「一个服务都
    # 没在跑」这个现场根本表达不出来 —— 而 R7-OPS-04 正是这个现场。
    printf '%s\n' "${MOCK_RUNNING_SERVICES-api}" | sed '/^[[:space:]]*$/d'
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
        # 模拟「解包后卷内容被写坏」：证明恢复校验比的是大小，不只是条目名。
        # 只在附件卷上动手（扩展卷里没有这个路径）。
        if [ "${MOCK_TAMPER_AFTER_EXTRACT:-false}" = "true" ] && [ -d "$host_dir/2026" ]; then
          printf 'truncated' > "$host_dir/2026/a.png"
        fi
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
        # 每个服务各自的失败预算：用来证明多槽就绪探测不共享同一个 deadline。
        probe_count_file="$MOCK_DATA/probe_${service}.count"
        probe_limit_var="MOCK_API_FAILURES_${service//-/_}"
        probe_limit="${!probe_limit_var:-0}"
        probe_count=$(( $(cat "$probe_count_file" 2>/dev/null || printf '0') + 1 ))
        printf '%s' "$probe_count" > "$probe_count_file"
        [ "$probe_count" -gt "$probe_limit" ] || exit 1
        [ "${MOCK_API_READY:-true}" = "true" ] || exit 1
        ;;
      *) exit 91 ;;
    esac
    ;;
  *) exit 94 ;;
esac
EOF
chmod +x "$MOCK_BIN/docker"

# 假时钟：只在 MOCK_FAKE_TIME=true 时拦截 `date +%s`，每次调用前进 4 秒。
# 用来把「多槽是否共享同一个就绪预算」变成确定性断言，而不是靠真实等待。
cat > "$MOCK_BIN/date" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${MOCK_FAKE_TIME:-false}" = "true" ] && [ "${1:-}" = "+%s" ]; then
  counter_file="$MOCK_DATA/date.count"
  n=$(( $(cat "$counter_file" 2>/dev/null || printf '0') + 1 ))
  printf '%s' "$n" > "$counter_file"
  printf '%s\n' "$((1000 + n * 4))"
  exit 0
fi
exec /bin/date "$@"
EOF
chmod +x "$MOCK_BIN/date"

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
decrypt_archive "$archive" "$payload"
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
# 加密方案必须是 v2：清单里的这一行是「信封被剥离」检测的唯一依据。
grep -q '^encryption=openssl-aes-256-cbc-pbkdf2-iter200000+hmac-sha256$' "$TEMP_DIR/unpack/manifest.env" || \
  fail "manifest does not record the v2 encryption scheme"
grep -q '^CREATE TABLE public\.users ' "$TEMP_DIR/unpack/database.sql" || fail "database dump has no core tables"
grep -q 'exec pg_dump ' "$MOCK_LOG" || fail "backup did not call pg_dump"
grep -q '^compose up -d --wait postgres$' "$MOCK_LOG" || fail "backup did not wait for postgres to be healthy"
case_ok "C1 备份=单一加密归档（库+附件+扩展+清单，0600，无临时残留）"

# --- C1b 独立跑备份（不经 deploy.sh）：--wait 失败但库随后就绪 → 继续 --------
# deploy.sh 的两条调用路径都会先 wait，所以这条冷机路径以前只在「有人直接跑
# backup-postgres.sh」时才踩到；它不该硬失败，也不该静默。
reset_log
if ! (cd "$TEST_ROOT" && MOCK_UP_FAIL=true MOCK_PG_READY_FAIL_TIMES=1 \
  SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_BACKUP_DIR="$TEMP_DIR/backups-cold" ./deploy/scripts/backup-postgres.sh \
  >"$TEMP_DIR/cold-backup.out" 2>"$TEMP_DIR/cold-backup.err"); then
  cat "$TEMP_DIR/cold-backup.err" >&2
  fail "standalone backup aborted when compose up --wait failed although pg_isready then recovered"
fi
grep -q '未在 --wait 窗口内就绪' "$TEMP_DIR/cold-backup.err" || \
  fail "standalone backup did not warn about the failed --wait window"
[[ -s "$(tail -n 1 "$TEMP_DIR/cold-backup.out")" ]] || fail "standalone cold backup produced no archive"
case_ok "C1b 独立跑备份：--wait 失败但库随后就绪 → 继续（不硬失败）"

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
# 无清单明文载荷默认会被完整性闸门拒掉（C13b/C13c）；这条用例要测的是**内容预检**，
# 所以显式给开关，让执行流走到「缺少核心表」那一步。
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' SFORUM_RESTORE_ALLOW_UNVERIFIED=1 \
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
decrypt_archive "$empty_archive" "$TEMP_DIR/empty-payload.tar.gz"
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
# 同一份数据被校验两次（切库前 / 切库后），空表告警只能打一次。
empty_warnings="$(grep -c '警告：核心表为空' "$TEMP_DIR/empty-core.err" || true)"
[[ "$empty_warnings" == "1" ]] || \
  fail "empty-core-table warning printed $empty_warnings times, expected exactly once"
case_ok "C6c 核心表为空：显式 warning，恢复照常完成（不硬失败）"

# --- C6d v2 归档密文被改：MAC 不匹配 → 拒绝，且一个库动作都没有 ------------
# 这正是 v1 的缺口：CBC 位翻转能确定性地改掉首段明文而解密照样「成功」。
TAMPER_DIR="$TEMP_DIR/tampered"
mkdir -p "$TAMPER_DIR"
tampered_archive="$TAMPER_DIR/sforum-backup-tampered.tar.gz.enc"
cp "$archive" "$tampered_archive"
header_bytes="$(envelope_header_bytes "$tampered_archive")"
original_byte="$(dd if="$tampered_archive" bs=1 skip="$header_bytes" count=1 2>/dev/null | od -An -tu1 | tr -d ' \n')"
[ -n "$original_byte" ] || fail "could not read the first ciphertext byte"
flipped_byte=$(( (original_byte + 1) % 256 ))
printf '%b' "\\$(printf '%03o' "$flipped_byte")" | \
  dd of="$tampered_archive" bs=1 seek="$header_bytes" conv=notrunc 2>/dev/null
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$tampered_archive" \
  >/dev/null 2>"$TEMP_DIR/tampered.err"); then
  fail "restore accepted an archive whose ciphertext was modified"
fi
grep -q '完整性校验失败' "$TEMP_DIR/tampered.err" || \
  fail "ciphertext tampering was not reported as an integrity failure"
if grep -q 'exec restore \|exec swap \|exec psql' "$MOCK_LOG"; then
  fail "tampered archive still reached the database"
fi
case_ok "C6d v2 归档密文被改：HMAC 不匹配 → 拒绝且不碰库（先验 MAC 再解密）"

# --- C6e 信封被剥离：清单声明 v2 却没有信封 → 拒绝按 v1 降级恢复 -----------
STRIPPED_DIR="$TEMP_DIR/stripped"
mkdir -p "$STRIPPED_DIR"
stripped_archive="$STRIPPED_DIR/sforum-backup-stripped.tar.gz.enc"
archive_header_bytes="$(envelope_header_bytes "$archive")"
tail -c +$((archive_header_bytes + 1)) "$archive" > "$stripped_archive"
# 外形上它就是一份合法的 v1 密文（Salted__ 开头），只有清单能拆穿它。
[[ "$(head -c 8 "$stripped_archive")" == "Salted__" ]] || \
  fail "stripped archive does not look like a v1 ciphertext"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$stripped_archive" \
  >/dev/null 2>"$TEMP_DIR/stripped.err"); then
  fail "restore accepted an archive whose authentication envelope was stripped"
fi
grep -q '外层没有信封' "$TEMP_DIR/stripped.err" || \
  fail "stripped envelope was not reported as a downgrade refusal"
if grep -q 'exec restore \|exec swap ' "$MOCK_LOG"; then
  fail "stripped archive still reached the database"
fi
case_ok "C6e 信封被剥离：清单声明 v2 → 拒绝降级恢复"

# --- C6e2 明文载荷 + v2 清单：拒绝（不需要密钥就能绕过 MAC 的那条路） --------
# 有磁盘写权限者可以不解密、直接把归档换成自己打的 tar.gz；只要清单里还写着 v2，
# 这条路径就必须 fail closed，而不是「警告后继续灌库」。
PLAIN_DIR="$TEMP_DIR/plain-v2"
mkdir -p "$PLAIN_DIR"
decrypt_archive "$archive" "$PLAIN_DIR/payload.tar.gz"
tar -C "$PLAIN_DIR" -xzf "$PLAIN_DIR/payload.tar.gz"
rm -f "$PLAIN_DIR/payload.tar.gz"
grep -q '^encryption=openssl-aes-256-cbc-pbkdf2-iter200000+hmac-sha256$' "$PLAIN_DIR/manifest.env" || \
  fail "plaintext fixture does not carry a v2 manifest"
plain_archive="$PLAIN_DIR/sforum-backup-plain.tar.gz.enc"
tar -C "$PLAIN_DIR" -czf "$plain_archive" manifest.env database.sql assets
[[ "$(head -c 2 "$plain_archive" | od -An -tx1 | tr -d ' \n')" == "1f8b" ]] || \
  fail "plaintext fixture is not a gzip archive"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$plain_archive" \
  >/dev/null 2>"$TEMP_DIR/plain-v2.err"); then
  fail "restore accepted a plaintext payload that claims the v2 encryption scheme"
fi
grep -q '完整性无法校验' "$TEMP_DIR/plain-v2.err" || \
  fail "plaintext v2 payload was not rejected with an explicit reason"
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/plain-v2.err" || \
  fail "plaintext v2 rejection did not name the explicit override"
if grep -q 'exec restore \|exec swap \|exec psql' "$MOCK_LOG"; then
  fail "plaintext v2 payload still reached the database"
fi
case_ok "C6e2 明文载荷 + v2 清单：拒绝恢复（不需要密钥的 MAC 绕过）"

# --- C6f v1 旧归档（无 MAC）：仍可恢复，但必须打印降级告警 ----------------
# 用生产代码解出 v2 载荷，把清单改回 v1 方案，再按 v1 方式重新封一份。
V1_DIR="$TEMP_DIR/v1"
mkdir -p "$V1_DIR/payload"
decrypt_archive "$archive" "$V1_DIR/payload.tar.gz"
tar -C "$V1_DIR/payload" -xzf "$V1_DIR/payload.tar.gz"
sed -i.bak 's/^encryption=.*/encryption=openssl-aes-256-cbc-pbkdf2-iter200000/' \
  "$V1_DIR/payload/manifest.env"
rm -f "$V1_DIR/payload/manifest.env.bak"
tar -C "$V1_DIR/payload" -czf "$V1_DIR/payload-v1.tar.gz" manifest.env database.sql assets
openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt -pass file:"$KEY_FILE" \
  -in "$V1_DIR/payload-v1.tar.gz" -out "$V1_DIR/sforum-backup-v1.tar.gz.enc"
reset_log
if ! (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$V1_DIR/sforum-backup-v1.tar.gz.enc" \
  >/dev/null 2>"$TEMP_DIR/v1.err"); then
  cat "$TEMP_DIR/v1.err" >&2
  fail "v1 archive is no longer restorable"
fi
grep -q 'v1 加密方案' "$TEMP_DIR/v1.err" || \
  fail "v1 restore did not print the downgrade warning"
grep -q 'exec swap ' "$MOCK_LOG" || fail "v1 restore did not proceed to the swap"
case_ok "C6f v1 旧归档：跳过验 MAC 但打印降级告警，恢复照常完成"

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

# --- C7b 无运行中 API 槽：按拓扑拉起并探测通过，才允许删旧库（R7-OPS-04）------
# 旧行为在这里打印「没有正在运行的 API 槽位，跳过 API 就绪探测」然后直接 dropdb：
# 一次没有任何进程证明过新库可用的删除。修复后没有槽就按拓扑把 api 拉起来再探。
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
if ! (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/no-slot-api.out" 2>"$TEMP_DIR/no-slot-api.err"); then
  cat "$TEMP_DIR/no-slot-api.err" >&2
  fail "restore failed although the API slot could be brought up while nothing was running"
fi
grep -q '^compose up -d --no-build --pull never api$' "$MOCK_LOG" || \
  fail "restore did not bring the API slot up although no slot was running"
grep -q 'exec api wget -q -O /dev/null http://127.0.0.1:8080/api/v1/ready' "$MOCK_LOG" || \
  fail "restore skipped the API readiness probe although no slot was running"
if grep -q '跳过 API 就绪探测' "$TEMP_DIR/no-slot-api.out"; then
  fail "restore still skips the API readiness probe when no slot is running"
fi
grep -q 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG" || \
  fail "verified restore did not drop the previous database"
case_ok "C7b 无运行中 API 槽：按拓扑拉起、探测通过后才删旧库（R7-OPS-04）"

# --- C7c 无运行中 API 槽且 API 起不来：切换前的库不删、退出非零（R7-OPS-04）---
# 与 C7 的区别：C7 是「本来有槽在跑、重启后不就绪」，这里是「压根没有槽」。两种
# 现场都必须停在同一个结论上：没有活着的 API 打开过新库，就不许删切换前的库。
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='' MOCK_API_READY=false \
  SFORUM_API_READY_TIMEOUT_SECONDS=1 SFORUM_API_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/no-slot-fail.out" 2>&1); then
  fail "restore reported success although no API slot ever opened the restored database"
fi
grep -q '^compose up -d --no-build --pull never api$' "$MOCK_LOG" || \
  fail "restore did not attempt to bring the API slot up before judging readiness"
grep -q 'exec swap ' "$MOCK_LOG" || fail "no-slot failure case never reached the swap"
if grep -q 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG"; then
  fail "restore deleted the previous database without a single ready API"
fi
grep -q '已自动回滚' "$TEMP_DIR/no-slot-fail.out" || \
  fail "no-slot failure did not tell the operator what happened to the databases"
case_ok "C7c 无运行中 API 槽且不可用：切换前的库不删、退出非零（R7-OPS-04）"

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

# --- C9c compose --wait 非零且容器没在跑：真故障，不耗满重试预算 -------------
# 「慢启动」与「起不来」必须分开：restarting / exited 时再等 attempts × interval
# 只是把坏镜像拖成几分钟静默等待，而日志里只有一句超时。
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_UP_FAIL=true \
  MOCK_CONTAINER_STATE=restarting \
  SFORUM_DB_READY_ATTEMPTS=5 SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/up-hard-fail.err"); then
  fail "restore kept retrying although the postgres container never started"
fi
grep -q '无法启动（容器状态：restarting）' "$TEMP_DIR/up-hard-fail.err" || \
  fail "a container that is not running did not produce an explicit startup failure"
if grep -q 'exec pg_isready ' "$MOCK_LOG"; then
  fail "restore burned pg_isready retries although the container was restarting"
fi
case_ok "C9c compose --wait 失败且容器未运行：立刻失败，不耗满重试预算（OPS2-03）"

# --- C9d 容器 exited：必须报 exited，不能退化成「未创建」---------------------
# `compose ps` 不带 --all 只列 running 的容器，exited 会被读成空 → 文案变成
# 「未创建」，把「进程退出了，去看退出码」这条最有用的线索抹掉。
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_UP_FAIL=true \
  MOCK_CONTAINER_STATE=exited \
  SFORUM_DB_READY_ATTEMPTS=3 SFORUM_DB_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/up-exited.err"); then
  fail "restore kept going although the postgres container had exited"
fi
grep -q '无法启动（容器状态：exited）' "$TEMP_DIR/up-exited.err" || \
  fail "an exited container was not reported as exited: $(grep -m1 '无法启动' "$TEMP_DIR/up-exited.err")"
if grep -q 'exec pg_isready ' "$MOCK_LOG"; then
  fail "restore burned pg_isready retries although the container had exited"
fi
case_ok "C9d compose --wait 失败且容器 exited：点名 exited，不退化成「未创建」"

# --- C10 蓝绿拓扑：compose 组合 + 按槽停启 + terminate 只针对已停槽 ----------
# 蓝绿部署状态文件：C10 之后的蓝绿用例共用这一份。active_slot 是恢复脚本唯一
# 认可的「该拉起哪个槽」的来源（与 upgrade.sh 的在线更新同源），传空串表示故意
# 缺这一行。注释与空行必须被忽略，所以这里保留它们。
write_blue_green_deployrc() {
  local active_slot="${1-blue}"
  {
    printf '%s\n' '# 部署状态文件：注释与空行必须被忽略' ''
    printf '%s\n' 'topology=blue-green' 'version=v3.0.0' 'blue_version=v3.0.0' 'green_version=v3.0.1'
    if [ -n "$active_slot" ]; then
      printf 'active_slot=%s\n' "$active_slot"
    fi
  } > "$TEST_ROOT/.deployrc"
}
write_blue_green_deployrc blue
cat > "$TEMP_DIR/expected-deployrc" <<'RC'
# 部署状态文件：注释与空行必须被忽略

topology=blue-green
version=v3.0.0
blue_version=v3.0.0
green_version=v3.0.1
active_slot=blue
RC
diff -u "$TEMP_DIR/expected-deployrc" "$TEST_ROOT/.deployrc" > "$TEMP_DIR/deployrc.diff" || {
  cat "$TEMP_DIR/deployrc.diff" >&2
  fail "the blue-green .deployrc fixture is not what the cases expect"
}
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
# 空槽分支必须只拉 .deployrc 的 active_slot：蓝绿两槽镜像版本不同，把 standby 一起
# 拉起来是「镜像没预拉就失败」与「两个内嵌 worker 同连恢复库」两个事故的入口。
if grep -q 'api-green' "$MOCK_LOG"; then
  fail "blue-green no-slot restore also started the standby slot"
fi
grep -q '^compose up -d --no-build --pull never api-blue$' "$MOCK_LOG" || \
  fail "blue-green no-slot restore did not start the active slot alone"
grep -q '拉起并等待就绪：api-blue$' "$TEMP_DIR/no-slot.out" || \
  fail "blue-green no-slot restore did not report the single slot it started"
if grep -q 'api-green' "$TEMP_DIR/no-slot.out"; then
  fail "blue-green no-slot restore listed the standby slot"
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

# --- C11e 蓝绿无槽且 active 槽拉不起来：不删切换前的库（R7-OPS-04）-----------
# compose up 失败是「没有活着的 API 打开过新库」的另一种形态：与探测超时同一条
# 结论 —— 不判定恢复成功、不动切换前的库。
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge' MOCK_UP_FAIL=true MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/no-slot-upfail.out" 2>&1); then
  fail "restore reported success although the active API slot could not be started"
fi
grep -q '^compose up -d --no-build --pull never api-blue$' "$MOCK_LOG" || \
  fail "no-slot failure case never tried to start the active slot"
grep -q '无法拉起 API 槽位（api-blue）' "$TEMP_DIR/no-slot-upfail.out" || \
  fail "no-slot failure did not name the slot it could not start"
if grep -q 'exec dropdb .*sforum_before_restore_' "$MOCK_LOG"; then
  fail "restore deleted the previous database although the active slot never started"
fi
grep -q '已自动回滚' "$TEMP_DIR/no-slot-upfail.out" || \
  fail "no-slot failure did not print the rollback guidance"
case_ok "C11e 蓝绿无槽且 active 槽拉不起来：不删切换前的库 + 回滚提示（R7-OPS-04）"

# --- C11f 蓝绿无槽且 active_slot 缺失/非法：停在破坏性动作之前（R7-OPS-04）---
# 「拉哪个槽」是部署状态，不是恢复脚本能猜的东西：猜错的代价是把 standby 顶上来。
# 解析放在停容器/建库之前，所以这两种配置错误连一个容器都不该动。
write_blue_green_deployrc ""
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/no-active-slot.out" 2>&1); then
  fail "restore accepted a blue-green deployment without active_slot"
fi
grep -q '缺少 active_slot' "$TEMP_DIR/no-active-slot.out" || \
  fail "missing active_slot did not explain what is missing"
if grep -q '^compose stop \|^compose start \|^compose up -d --no-build \|exec createdb \|exec swap ' "$MOCK_LOG"; then
  fail "missing active_slot was detected after the restore had already touched containers or the database"
fi

write_blue_green_deployrc purple
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES=$'edge' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/bad-active-slot.out" 2>&1); then
  fail "restore accepted an invalid active_slot value"
fi
grep -q 'Invalid active_slot in .deployrc: purple' "$TEMP_DIR/bad-active-slot.out" || \
  fail "invalid active_slot error message is unclear"
if grep -q '^compose up -d --no-build \|exec createdb ' "$MOCK_LOG"; then
  fail "invalid active_slot was detected after the restore had already touched containers or the database"
fi
write_blue_green_deployrc blue
case_ok "C11f 蓝绿无槽且 active_slot 缺失/非法：停在破坏性动作之前（R7-OPS-04）"

# --- C11c 多槽就绪探测：每个槽各自计时，不共享一个 deadline -----------------
# 假时钟每次 date +%s 前进 4 秒，timeout=8 → 每个槽两次探测的预算。
# 共享 deadline 时先探的槽吃掉预算，第二个槽在第一次探测后就被判超时。
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
if (cd "$TEST_ROOT" && MOCK_FAKE_TIME=true \
  MOCK_RUNNING_SERVICES=$'edge\napi-blue\napi-green' \
  MOCK_API_FAILURES_api_blue=1 MOCK_API_FAILURES_api_green=1 \
  SFORUM_API_READY_TIMEOUT_SECONDS=8 SFORUM_API_READY_INTERVAL_SECONDS=0 \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >"$TEMP_DIR/multi-slot.out" 2>&1); then
  :
else
  tail -n 3 "$TEMP_DIR/multi-slot.out" >&2
  fail "restore gave the second slot no readiness budget of its own"
fi
grep -q 'API ready: api-blue' "$TEMP_DIR/multi-slot.out" || fail "api-blue was never reported ready"
grep -q 'API ready: api-green' "$TEMP_DIR/multi-slot.out" || \
  fail "api-green was never reported ready: the second slot inherited a drained deadline"
case_ok "C11c 多槽就绪：api-blue 用掉自己的预算后 api-green 仍有完整预算"

# --- C11d 蓝绿声明但单拓扑服务在跑：同样停手（守卫要对称）-------------------
# 只防「声明 single 实跑槽」时，反向不一致会走完整条恢复链才在改名处失败。
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/reverse-topology.err"); then
  fail "restore accepted blue-green topology while the single-topology api service was running"
fi
grep -q '检测到单拓扑服务正在运行（api）' "$TEMP_DIR/reverse-topology.err" || \
  fail "reverse topology mismatch did not explain the problem"
if grep -q '^compose stop \|exec createdb ' "$MOCK_LOG"; then
  fail "reverse topology mismatch touched services or the database before aborting"
fi
case_ok "C11d 拓扑守卫对称：声明 blue-green 而实跑 api 时同样在动手前停手"

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

# --- C13 旧版明文 .sql 备份：显式开关下仍可恢复，并打警告 -------------------
# 无清单的明文载荷没有任何完整性凭据，默认拒绝（见 C13b/C13c）；这里钉住「保留通道」
# 的另一半：运维显式给出 SFORUM_RESTORE_ALLOW_UNVERIFIED=1 时，恢复必须照常完成，
# 且必须把「你正在恢复一份无凭据载荷」打在 stderr 上。
{
  printf '%s\n' '--' '-- PostgreSQL database dump'
  for table in users roles categories topics posts comments attachments; do
    printf 'CREATE TABLE public.%s (id bigint);\n' "$table"
  done
} > "$TEMP_DIR/legacy.sql"
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_RESTORE_ALLOW_UNVERIFIED=1 SFORUM_CONFIRM_RESTORE=RESTORE \
  ./deploy/scripts/restore-postgres.sh "$TEMP_DIR/legacy.sql" \
  >"$TEMP_DIR/legacy.out" 2>&1)
grep -q 'exec swap ' "$MOCK_LOG" || fail "legacy .sql restore did not swap the database"
grep -q '该备份不含附件/扩展制品卷' "$TEMP_DIR/legacy.out" || fail "legacy .sql restore did not report the missing asset volumes"
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/legacy.out" || \
  fail "legacy .sql restore under the override did not warn with the variable name"
grep -q '无完整性凭据' "$TEMP_DIR/legacy.out" || \
  fail "legacy .sql restore under the override did not warn about missing provenance"
if grep -q '^compose run ' "$MOCK_LOG"; then
  fail "legacy .sql restore tried to restore volume contents it does not have"
fi
case_ok "C13 旧版明文 .sql 备份：SFORUM_RESTORE_ALLOW_UNVERIFIED=1 下仍可恢复并打警告"

# --- C13b 同一份明文 .sql 不带开关：默认拒绝，且一个库动作都没有 --------------
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$TEMP_DIR/legacy.sql" \
  >/dev/null 2>"$TEMP_DIR/legacy-noswitch.err"); then
  fail "restore accepted a manifest-less plaintext .sql without SFORUM_RESTORE_ALLOW_UNVERIFIED=1"
fi
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/legacy-noswitch.err" || \
  fail "manifest-less .sql rejection did not name the explicit override"
grep -q '没有清单' "$TEMP_DIR/legacy-noswitch.err" || \
  fail "manifest-less .sql rejection did not say the manifest is missing"
if grep -q 'exec restore \|exec swap ' "$MOCK_LOG"; then
  fail "manifest-less .sql still reached the database"
fi
case_ok "C13b 无清单明文 .sql 不带开关：拒绝恢复且不碰库"

# --- C13c 手搓无清单 tar.gz：同样默认拒绝（C6e2 只覆盖「清单还在」那一类）----
NOMANIFEST_DIR="$TEMP_DIR/plain-nomanifest"
mkdir -p "$NOMANIFEST_DIR"
cp "$TEMP_DIR/legacy.sql" "$NOMANIFEST_DIR/database.sql"
nomanifest_archive="$TEMP_DIR/sforum-backup-nomanifest.tar.gz"
tar -C "$NOMANIFEST_DIR" -czf "$nomanifest_archive" database.sql
[[ "$(head -c 2 "$nomanifest_archive" | od -An -tx1 | tr -d ' \n')" == "1f8b" ]] || \
  fail "manifest-less fixture is not a gzip archive"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$nomanifest_archive" \
  >/dev/null 2>"$TEMP_DIR/nomanifest.err"); then
  fail "restore accepted a manifest-less plaintext archive without the explicit override"
fi
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/nomanifest.err" || \
  fail "manifest-less archive rejection did not name the explicit override"
if grep -q 'exec restore \|exec swap ' "$MOCK_LOG"; then
  fail "manifest-less archive still reached the database"
fi
# 开关下必须仍能恢复：只拒绝不放行等于把 DR 现场最后一条路封死。
reset_log
(cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_RESTORE_ALLOW_UNVERIFIED=1 SFORUM_CONFIRM_RESTORE=RESTORE \
  ./deploy/scripts/restore-postgres.sh "$nomanifest_archive" \
  >"$TEMP_DIR/nomanifest-allowed.out" 2>&1)
grep -q 'exec swap ' "$MOCK_LOG" || \
  fail "manifest-less archive was not restorable under the explicit override"
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/nomanifest-allowed.out" || \
  fail "manifest-less archive restore under the override did not warn"
case_ok "C13c 手搓无清单 tar.gz：默认拒绝，显式开关下可恢复并打警告"

# --- C13d 清单还在但 encryption= 行被删：同样默认拒绝 -----------------------
# 决策记录「证据 B」的那条绕过：不用换文件，只删一行就让明文 tar.gz 通过。
NOENCRYPT_DIR="$TEMP_DIR/plain-noencryption"
mkdir -p "$NOENCRYPT_DIR"
decrypt_archive "$archive" "$NOENCRYPT_DIR/payload.tar.gz"
tar -C "$NOENCRYPT_DIR" -xzf "$NOENCRYPT_DIR/payload.tar.gz"
rm -f "$NOENCRYPT_DIR/payload.tar.gz"
grep -q '^encryption=' "$NOENCRYPT_DIR/manifest.env" || \
  fail "fixture manifest carries no encryption= line to strip"
sed -i.bak '/^encryption=/d' "$NOENCRYPT_DIR/manifest.env"
rm -f "$NOENCRYPT_DIR/manifest.env.bak"
noencrypt_archive="$TEMP_DIR/sforum-backup-noencryption.tar.gz"
tar -C "$NOENCRYPT_DIR" -czf "$noencrypt_archive" manifest.env database.sql assets
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$noencrypt_archive" \
  >/dev/null 2>"$TEMP_DIR/noencryption.err"); then
  fail "restore accepted a plaintext archive whose manifest lost the encryption= line"
fi
grep -q '缺少 encryption=' "$TEMP_DIR/noencryption.err" || \
  fail "stripped encryption= line was not reported as a missing credential"
grep -q 'SFORUM_RESTORE_ALLOW_UNVERIFIED=1' "$TEMP_DIR/noencryption.err" || \
  fail "stripped encryption= rejection did not name the explicit override"
if grep -q 'exec restore \|exec swap ' "$MOCK_LOG"; then
  fail "stripped encryption= archive still reached the database"
fi
case_ok "C13d 清单缺 encryption= 行：默认拒绝（P3-22 的删行绕过已封）"

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
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' SFORUM_RESTORE_ALLOW_UNVERIFIED=1 \
  SFORUM_CONFIRM_RESTORE=RESTORE \
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

# --- C17b 卷恢复校验：条目齐但大小不符 → 拒绝并回滚 -------------------------
# 只比条目名会放过「写了一半 / 截断」，而那正是卷恢复最真实的失败形态。
rm -rf "$MOCK_DATA/app/storage/app/attachments/2026"
reset_log
if (cd "$TEST_ROOT" && MOCK_RUNNING_SERVICES='api' MOCK_API_READY=true \
  MOCK_TAMPER_AFTER_EXTRACT=true \
  SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$archive" \
  >/dev/null 2>"$TEMP_DIR/tampered.err"); then
  fail "restore accepted a volume whose restored file size no longer matched the archive"
fi
grep -q '大小不符' "$TEMP_DIR/tampered.err" || fail "size mismatch was not reported explicitly"
grep -q '卷内容校验失败' "$TEMP_DIR/tampered.err" || fail "tampered volume did not fail the content check"
grep -q '已回滚' "$TEMP_DIR/tampered.err" || fail "tampered volume did not trigger the rollback path"
case_ok "C17b 卷内容校验：条目齐但大小不符 → 拒绝并回滚（不只看条目名）"

# --- C17c 崩溃残留：retention 清理陈旧的 .sforum-backup-* 工作目录 -----------
# SIGKILL 不走 EXIT trap，备份目录里会留下 700 目录 + 600 明文 database.sql；
# retention 原本只扫归档文件，这些目录会一直躺在盘上。
CRASH_DIR="$TEMP_DIR/backups-crash"
mkdir -p "$CRASH_DIR/.sforum-backup-20200101T000000Z.ABCDEF"
printf 'plaintext dump\n' > "$CRASH_DIR/.sforum-backup-20200101T000000Z.ABCDEF/database.sql"
touch -d '3 days ago' "$CRASH_DIR/.sforum-backup-20200101T000000Z.ABCDEF"
mkdir -p "$CRASH_DIR/.sforum-backup-20261116T000000Z.LIVE01"
printf 'in-flight\n' > "$CRASH_DIR/.sforum-backup-20261116T000000Z.LIVE01/database.sql"
reset_log
(cd "$TEST_ROOT" && SFORUM_BACKUP_DIR="$CRASH_DIR" ./deploy/scripts/backup-postgres.sh \
  >/dev/null 2>"$TEMP_DIR/crash-sweep.err")
[[ ! -e "$CRASH_DIR/.sforum-backup-20200101T000000Z.ABCDEF" ]] || \
  fail "stale crash work directory survived retention"
[[ -d "$CRASH_DIR/.sforum-backup-20261116T000000Z.LIVE01" ]] || \
  fail "retention deleted a work directory that could belong to a running backup"
grep -q 'removed stale work directory' "$TEMP_DIR/crash-sweep.err" || \
  fail "stale work directory removal was not reported"
case_ok "C17c 崩溃残留：陈旧工作目录被 retention 清理，在飞的不动"

# --- C18 deploy.sh 侧的既有契约不变 ----------------------------------------
grep -qF "wait-for-health.sh \"http://127.0.0.1:\${api_port}/api/v1/ready\"" "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not wait for API readiness"
grep -qF "wait-for-health.sh \"http://127.0.0.1:\${web_port}/health\"" "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not wait for Web readiness"
grep -q 'chmod 600 .env.production' "$ROOT_DIR/deploy.sh" || fail "deploy does not protect production configuration"
# 探针三态必须被 deploy.sh 消费：恢复态写 reason=recovery_only，超时写 health_failed。
# 两者都仍是 status=recovery_required，区别只在「运维下一步做什么」。
grep -qF "verify_deployment_health \"\$backup_file\" \"\$previous_version\"" "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not route the probe status into the recovery reason"
grep -qF 'persist_recovery_state recovery_only' "$ROOT_DIR/deploy.sh" || \
  fail "deploy does not record the recovery-only reason"
case_ok "C18 deploy.sh 侧既有契约（健康探测、.env 权限）保持"

printf '\npostgres-safety_test.sh: all checks passed (%s cases)\n' "${#CHECK_NAMES[@]}"
printf 'cases:\n'
for name in "${CHECK_NAMES[@]}"; do
  printf '  - %s\n' "$name"
done
