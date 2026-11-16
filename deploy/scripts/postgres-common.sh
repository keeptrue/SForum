#!/usr/bin/env bash
# 生产 PostgreSQL 备份 / 恢复共享库（由 backup-postgres.sh / restore-postgres.sh source）。
#
# 三条硬约束，改这个文件前先读：
#   1. .env.production 只做字面取值，绝不 source / eval —— 值里出现 $(...) 也不会被执行。
#   2. .deployrc 只认 key=value；拓扑 / 版本号非法时 fail-closed，不猜默认值。
#   3. compose 文件集合与服务名由拓扑决定：蓝绿拓扑不得退回单拓扑假设
#      （deploy.sh:431 deployment_uses_blue_green / :435 configure_blue_green_compose）。
#
# 这里只放「配置读取 + 拓扑 + compose + 数据库只读校验 + 就绪等待」，不放备份归档格式。
# shellcheck shell=bash

# 核心表集合：恢复出来的库必须能被应用真正使用，而不是「有任意关系就算成功」。
# users/roles 是身份与权限前提；categories/topics/posts/comments 是论坛内容；
# attachments 是附件元数据（文件体在附件卷里单独备份）。少任何一张都说明备份不完整。
SFORUM_CORE_TABLES=(users roles categories topics posts comments attachments)

SFORUM_DB_READY_ATTEMPTS_DEFAULT=40
SFORUM_DB_READY_INTERVAL_SECONDS_DEFAULT=3
SFORUM_API_READY_TIMEOUT_SECONDS_DEFAULT=180
SFORUM_API_READY_INTERVAL_SECONDS_DEFAULT=3

SFORUM_DEPLOY_RC=".deployrc"
SFORUM_TOPOLOGY="single"
SFORUM_COMPOSE=()
POSTGRES_ACTOR=""

sforum_die() {
  printf '%s\n' "$*" >&2
  exit 1
}

sforum_file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

# 只做字面取值：不 source、不 eval。quote 成对时去引号，字符集不合法直接失败。
sforum_env_value() {
  local file="$1"
  local key="$2"
  local default_value="${3:-}"
  local line value=""

  if [ -f "$file" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line%$'\r'}"
      case "$line" in
        "$key="*) value="${line#*=}" ;;
      esac
    done < "$file"
  fi

  if [ -z "$value" ]; then
    value="$default_value"
  elif [ "${value:0:1}" = '"' ] || [ "${value:0:1}" = "'" ]; then
    if [ "${value: -1}" != "${value:0:1}" ]; then
      sforum_die "Invalid quoted value for $key in $file."
    fi
    value="${value:1:${#value}-2}"
  fi

  if [ -z "$value" ]; then
    value="$default_value"
  fi

  case "$value" in
    *[!A-Za-z0-9_.-]*)
      sforum_die "Invalid value for $key in $file."
      ;;
  esac
  printf '%s\n' "$value"
}

# 容器内绝对路径（EXTENSION_ROOT 这类值只能写容器内路径，见 compose.yaml:101）。
sforum_env_path_value() {
  local file="$1"
  local key="$2"
  local default_value="${3:-}"
  local line value=""

  if [ -f "$file" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line%$'\r'}"
      case "$line" in
        "$key="*) value="${line#*=}" ;;
      esac
    done < "$file"
  fi

  if [ -z "$value" ]; then
    value="$default_value"
  elif [ "${value:0:1}" = '"' ] || [ "${value:0:1}" = "'" ]; then
    if [ "${value: -1}" != "${value:0:1}" ]; then
      sforum_die "Invalid quoted value for $key in $file."
    fi
    value="${value:1:${#value}-2}"
  fi

  case "$value" in
    /*) ;;
    *)
      sforum_die "Invalid absolute path for $key in $file: ${value:-<empty>}"
      ;;
  esac
  case "$value" in
    *[!A-Za-z0-9_./-]*)
      sforum_die "Invalid path characters for $key in $file."
      ;;
  esac
  printf '%s\n' "$value"
}

# 照 deploy.sh:131 rc_value 的语义取值（同名键取最后一次出现），但多一步字符集校验：
# 拓扑 / 版本号写错要立刻 abort，不能带着坏值继续跑。
sforum_rc_value() {
  local key="$1"
  local line value=""

  [ -f "$SFORUM_DEPLOY_RC" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      ""|"#"*) continue ;;
    esac
    case "$line" in
      "$key="*) value="${line#*=}" ;;
    esac
  done < "$SFORUM_DEPLOY_RC"

  case "$value" in
    *[!A-Za-z0-9_.-]*)
      sforum_die "Invalid value for $key in $SFORUM_DEPLOY_RC: $value"
      ;;
  esac
  printf '%s' "$value"
}

sforum_topology() {
  local topology
  topology="$(sforum_rc_value topology)"
  case "$topology" in
    ""|single) printf 'single' ;;
    blue-green) printf 'blue-green' ;;
    *)
      sforum_die "Invalid topology in $SFORUM_DEPLOY_RC: $topology (expected single or blue-green)"
      ;;
  esac
}

sforum_validate_release_version() {
  local key="$1"
  local value="$2"

  [ -n "$value" ] || sforum_die "$key is required in $SFORUM_DEPLOY_RC for this topology."
  [[ "$value" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    sforum_die "Invalid $key in $SFORUM_DEPLOY_RC: $value"
}

sforum_require_compose_file() {
  [ -f "$1" ] || sforum_die "Missing compose file: $1"
}

# 组装 compose 命令并导出路径模板需要的版本变量。与 deploy.sh 的
# COMPOSE / BLUE_GREEN_COMPOSE 保持同一份文件集合顺序，避免两套真值。
sforum_configure_compose() {
  local rc_version blue green file

  SFORUM_TOPOLOGY="$(sforum_topology)"

  for file in compose.yaml compose.prod.yaml; do
    sforum_require_compose_file "$file"
  done
  SFORUM_COMPOSE=(docker compose --env-file .env.production -f compose.yaml -f compose.prod.yaml)

  rc_version="$(sforum_rc_value version)"
  if [ -n "$rc_version" ]; then
    export SFORUM_VERSION="$rc_version"
  fi

  [ "$SFORUM_TOPOLOGY" = "blue-green" ] || return 0

  # 蓝绿拓扑：compose.release.yaml 的 ${SFORUM_VERSION:?...} 与
  # compose.zero-downtime.yaml 的槽位镜像都必须在命令行上成型。
  for file in compose.release.yaml compose.zero-downtime.yaml; do
    sforum_require_compose_file "$file"
  done
  sforum_validate_release_version version "$rc_version"
  blue="$(sforum_rc_value blue_version)"
  green="$(sforum_rc_value green_version)"
  sforum_validate_release_version blue_version "$blue"
  sforum_validate_release_version green_version "$green"
  export SFORUM_BLUE_VERSION="$blue"
  export SFORUM_GREEN_VERSION="$green"
  SFORUM_COMPOSE+=(-f compose.release.yaml -f compose.zero-downtime.yaml --profile zero-downtime)
}

# 备份清单（manifest.env）是纯 key=value，不用 source：归档来自外部，只允许被解析。
sforum_manifest_value() {
  local file="$1"
  local key="$2"

  [ -f "$file" ] || return 0
  awk -v key="$key" 'index($0, key "=") == 1 { value = substr($0, length(key) + 2) } END { printf "%s", value }' "$file"
}

sforum_running_services() {
  "${SFORUM_COMPOSE[@]}" ps --status running --services
}

# 会持有数据库连接的应用服务。单拓扑只有 api（worker 已内嵌在 api 进程里，
# deploy.sh:550 把独立 worker 容器当遗留物清理，restore 也不负责复活它）；
# 蓝绿拓扑按槽给出 api-*/worker-*，停启都要保留每槽原有运行状态。
sforum_app_services() {
  case "$SFORUM_TOPOLOGY" in
    blue-green) printf '%s\n' api-blue worker-blue api-green worker-green ;;
    *) printf '%s\n' api ;;
  esac
}

# 取容器 IP，用于把 pg_terminate_backend 收窄到「本脚本刚停掉的槽」。
sforum_service_addresses() {
  local service="$1"
  local id output

  output="$("${SFORUM_COMPOSE[@]}" ps -q "$service" 2>/dev/null || true)"
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$id" 2>/dev/null || true
  done <<< "$output" | tr ' ' '\n' | grep -E '^[0-9]{1,3}(\.[0-9]{1,3}){3}$' || true
}

sforum_require_uint() {
  local name="$1"
  local value="$2"
  local minimum="$3"

  case "$value" in
    ""|*[!0-9]*) sforum_die "Invalid $name: ${value:-<empty>} (expected an integer >= $minimum)" ;;
  esac
  [ "$value" -ge "$minimum" ] || \
    sforum_die "Invalid $name: $value (expected an integer >= $minimum)"
}

# 容器状态（running / exited / restarting / created）；没有容器时输出空。
# 必须带 --all：不带时 compose 只列 running 的容器，exited 会被读成「未创建」，
# 于是「容器退出了」与「容器压根没建出来」这两种完全不同的故障被压成同一句话。
# 只用 compose 自己的 ps，不引 docker inspect：ps 的字段语义与 compose 版本绑定，
# 比 inspect 的 JSON 形状更稳定。
sforum_postgres_container_state() {
  "${SFORUM_COMPOSE[@]}" ps --all --format '{{.State}}' postgres 2>/dev/null | head -n 1 | tr -d '[:space:]'
}

# OPS2-03：冷库首次启动时 pg_isready 会先失败几次，必须重试到明确超时，
# 而不是一次失败就整条恢复链断在「库还没起来」。
#
# 但「慢启动」与「真故障」必须分开：compose 的 --wait 窗口由 healthcheck 决定
# （interval 10s × retries 5，没有 start_period），冷启动慢过它属于正常；而容器
# 根本没在跑（exited / restarting / 压根没创建）时，再耗满 attempts × interval
# 只是把「坏镜像 / 坏配置」拖成几分钟的静默等待。所以 --wait 失败后先看状态：
# 只有 running 才降级成 warning 交给 pg_isready 重试，其余立刻失败并点名原因。
sforum_start_postgres() {
  local state

  if "${SFORUM_COMPOSE[@]}" up -d --wait postgres; then
    return 0
  fi

  state="$(sforum_postgres_container_state)"
  case "$state" in
    running)
      printf '警告：postgres 未在 --wait 窗口内就绪，交给 pg_isready 重试判定。\n' >&2
      return 0
      ;;
    *)
      sforum_die "postgres 无法启动（容器状态：${state:-未创建}），不进入就绪重试。先看 \`docker compose logs postgres\`。"
      ;;
  esac
}

sforum_wait_for_postgres() {
  local db="$1"
  local attempts="${SFORUM_DB_READY_ATTEMPTS:-$SFORUM_DB_READY_ATTEMPTS_DEFAULT}"
  local interval="${SFORUM_DB_READY_INTERVAL_SECONDS:-$SFORUM_DB_READY_INTERVAL_SECONDS_DEFAULT}"
  local attempt=1

  sforum_require_uint SFORUM_DB_READY_ATTEMPTS "$attempts" 1
  sforum_require_uint SFORUM_DB_READY_INTERVAL_SECONDS "$interval" 0

  while [ "$attempt" -le "$attempts" ]; do
    if "${SFORUM_COMPOSE[@]}" exec -T postgres pg_isready -U "$POSTGRES_ACTOR" -d "$db" >/dev/null 2>&1; then
      printf 'PostgreSQL ready for %s after %s attempt(s).\n' "$db" "$attempt"
      return 0
    fi
    attempt=$((attempt + 1))
    [ "$attempt" -le "$attempts" ] || break
    sleep "$interval"
  done

  sforum_die "PostgreSQL did not become ready for $db after $attempts attempt(s) with ${interval}s interval."
}

sforum_psql() {
  "${SFORUM_COMPOSE[@]}" exec -T postgres psql -X --set=ON_ERROR_STOP=1 -U "$POSTGRES_ACTOR" "$@"
}

sforum_db_relation_count() {
  local db="$1"
  sforum_psql -XAt -d "$db" -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast';"
}

sforum_assert_identifier() {
  local name="$1"
  [[ "$name" =~ ^[a-z_][a-z0-9_]*$ ]] || sforum_die "Refusing unsafe table identifier: $name"
}

# 输出缺失的核心表（每行一张）；空输出代表全部存在。
sforum_db_missing_tables() {
  local db="$1"
  shift
  local table values=""

  [ "$#" -gt 0 ] || return 0
  for table in "$@"; do
    sforum_assert_identifier "$table"
    values+="('$table'),"
  done
  values="${values%,}"
  sforum_psql -XAt -d "$db" -c "SELECT t.name FROM (VALUES $values) AS t(name) WHERE to_regclass('public.' || t.name) IS NULL;"
}

sforum_db_table_rows() {
  local db="$1"
  local table="$2"
  sforum_assert_identifier "$table"
  sforum_psql -XAt -d "$db" -c "SELECT count(*) FROM public.\"$table\";"
}

# 内容可信性校验（RV-16 的核心）：关系数只是入场券，核心表齐全、行数与备份清单
# 一致才算通过。清单里没写行数时只校验表存在（兼容旧备份）。
#
# 第三个参数 quiet 只关掉「空核心表」那一条 warning，不关任何校验：一次恢复会
# 校验两次（切库前校验恢复库、切库后再校验发布库），而两次看的是同一份数据，
# 空表提示重复两遍只会稀释它。
sforum_db_validate() {
  local db="$1"
  local manifest="${2:-}"
  local quiet="${3:-}"
  local declared=""
  local tables=()
  local relation_count missing table expected rows
  local empty_tables=""

  relation_count="$(sforum_db_relation_count "$db")"
  case "$relation_count" in
    ""|*[!0-9]*)
      sforum_die "Restored database validation returned an invalid relation count: $relation_count"
      ;;
  esac
  [ "$relation_count" -gt 0 ] || sforum_die "Restored database validation found no application tables."

  tables=("${SFORUM_CORE_TABLES[@]}")
  if [ -n "$manifest" ] && [ -f "$manifest" ]; then
    declared="$(sforum_manifest_value "$manifest" core_tables)"
    if [ -n "$declared" ]; then
      IFS=',' read -r -a tables <<< "$declared"
    fi
  fi

  while IFS= read -r missing; do
    [ -n "$missing" ] || continue
    sforum_die "Restored database is missing core table: $missing"
  done < <(sforum_db_missing_tables "$db" "${tables[@]}")

  for table in "${tables[@]}"; do
    rows="$(sforum_db_table_rows "$db" "$table")"
    case "$rows" in
      ""|*[!0-9]*) sforum_die "Row count for $table returned garbage: $rows" ;;
    esac
    if [ "$rows" -eq 0 ]; then
      empty_tables="${empty_tables:+$empty_tables,}$table"
    fi
    expected="$(sforum_manifest_value "$manifest" "row_count.$table")"
    [ -n "$expected" ] || continue
    [ "$rows" = "$expected" ] || \
      sforum_die "Restored table $table has $rows row(s) but the backup recorded $expected."
  done

  # 空核心表不是错误（staging / 测试库理应为空），但必须是「看得见的状态」：
  # 恢复一份全空数据却一句提示都没有，才是真正的运维事故。
  if [ -n "$empty_tables" ] && [ "$quiet" != "quiet" ]; then
    printf '警告：核心表为空（备份可能来自空库、staging 或测试库）：%s\n' "$empty_tables" >&2
  fi
  printf 'Restored database content validated: %s relation(s), core tables complete.\n' "$relation_count"
}

# 容器内健康探测：/api/v1/ready 与 deploy.sh 使用同一路径。用 exec 而不是宿主端口，
# 因为蓝绿拓扑的端口由 edge 持有，槽位本身不发布端口。
#
# 每个槽位各自计时：蓝绿拓扑会传进来两个槽（api-blue / api-green），共用同一个
# deadline 时先探的槽会把预算吃干，后一个槽拿到 0 秒 —— 表现为「第二个槽永远
# 探不过」，而不是「超时」。
sforum_wait_for_api_services() {
  local timeout="${SFORUM_API_READY_TIMEOUT_SECONDS:-$SFORUM_API_READY_TIMEOUT_SECONDS_DEFAULT}"
  local interval="${SFORUM_API_READY_INTERVAL_SECONDS:-$SFORUM_API_READY_INTERVAL_SECONDS_DEFAULT}"
  local deadline service

  sforum_require_uint SFORUM_API_READY_TIMEOUT_SECONDS "$timeout" 1
  sforum_require_uint SFORUM_API_READY_INTERVAL_SECONDS "$interval" 0

  [ "$#" -gt 0 ] || return 0
  for service in "$@"; do
    deadline=$(( $(date +%s) + timeout ))
    while ! "${SFORUM_COMPOSE[@]}" exec -T "$service" \
      wget -q -O /dev/null http://127.0.0.1:8080/api/v1/ready >/dev/null 2>&1; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        sforum_die "API readiness probe did not pass within ${timeout}s for service $service."
      fi
      sleep "$interval"
    done
    printf 'API ready: %s\n' "$service"
  done
}
