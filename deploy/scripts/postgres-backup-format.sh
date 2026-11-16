#!/usr/bin/env bash
# 备份归档格式：一个归档同时带数据库、附件卷与扩展制品卷。
#
#   归档内容（tar.gz 内）：
#     manifest.env            清单：格式版本、核心表集合、行数、条目数、加密方案
#     database.sql            pg_dump 纯文本
#     assets/attachments.tar  附件卷（容器内路径）
#     assets/extensions.tar   扩展制品卷（EXTENSION_ROOT）
#
#   落地文件：$SFORUM_BACKUP_DIR/sforum-backup-<UTC>.tar.gz.enc
#   加密：openssl AES-256-CBC + PBKDF2（不改仓库依赖，不落明文）。
#     用法：export SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key   # 推荐，0600
#           # 或 export SFORUM_BACKUP_KEY='<口令>'
#   保留：SFORUM_BACKUP_RETENTION_COUNT（默认 14 份）/ _DAYS（默认 30 天），
#         任一设为 default 即回默认值。
#
# 被 backup-postgres.sh / restore-postgres.sh source；依赖 postgres-common.sh。
# shellcheck shell=bash

SFORUM_BACKUP_FORMAT_VERSION=2
SFORUM_BACKUP_ENCRYPTION_SCHEME="openssl-aes-256-cbc-pbkdf2-iter200000"
SFORUM_BACKUP_RETENTION_COUNT_DEFAULT=14
SFORUM_BACKUP_RETENTION_DAYS_DEFAULT=30
# 附件卷在容器里的固定挂载点（compose.yaml:109 attachment_uploads:/app/storage/app/attachments）。
# 备份/恢复脚本按需覆盖，所以这里的默认值在文件内没有引用点。
# shellcheck disable=SC2034
SFORUM_ATTACHMENT_CONTAINER_DIR_DEFAULT="/app/storage/app/attachments"

SFORUM_KEY_ARGS=()
SFORUM_ATTACHMENT_CONTAINER_DIR=""
EXTENSION_CONTAINER_DIR=""
SFORUM_ATTACHMENT_ENTRIES=0
SFORUM_EXTENSION_ENTRIES=0

# 缺密钥必须明确失败：静默出明文备份等于把全库内容留在磁盘上。
sforum_require_backup_key() {
  local purpose="${1:-备份}"
  local key_file="${SFORUM_BACKUP_KEY_FILE:-}"

  SFORUM_KEY_ARGS=()

  if [ -n "$key_file" ]; then
    [ -f "$key_file" ] || sforum_die "${purpose}缺少加密密钥文件：$key_file"
    [ -s "$key_file" ] || sforum_die "加密密钥文件为空：$key_file"
    if [ "$(sforum_file_mode "$key_file")" != "600" ]; then
      sforum_die "加密密钥文件必须是 0600：$key_file"
    fi
    SFORUM_KEY_ARGS=(-pass "file:$key_file")
    return 0
  fi

  if [ -n "${SFORUM_BACKUP_KEY:-}" ]; then
    SFORUM_KEY_ARGS=(-pass "env:SFORUM_BACKUP_KEY")
    return 0
  fi

  sforum_die "${purpose}缺少加密密钥：请设置 SFORUM_BACKUP_KEY_FILE（推荐，0600 文件）或 SFORUM_BACKUP_KEY；不会写出明文备份。"
}

sforum_encrypt_file() {
  local in="$1"
  local out="$2"

  [ "${#SFORUM_KEY_ARGS[@]}" -gt 0 ] || sforum_die "加密前必须先加载备份密钥。"
  openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt \
    "${SFORUM_KEY_ARGS[@]}" -in "$in" -out "$out" >/dev/null 2>&1 || \
    sforum_die "openssl 加密失败：$out"
  chmod 600 "$out"
}

sforum_decrypt_file() {
  local in="$1"
  local out="$2"

  [ "${#SFORUM_KEY_ARGS[@]}" -gt 0 ] || sforum_die "解密前必须先加载备份密钥。"
  openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
    "${SFORUM_KEY_ARGS[@]}" -in "$in" -out "$out" >/dev/null 2>&1 || \
    sforum_die "openssl 解密失败（密钥不对或文件损坏）：$in"
  chmod 600 "$out"
}

# 恢复输入判型：加密归档 / 明文归档 / 旧版单文件 SQL。
# 认 magic 而不只看扩展名，避免把 .sql 改名成 .enc 就当成加密包。
sforum_detect_payload() {
  local input="$1"
  local magic=""

  magic="$(head -c 8 "$input" | od -An -tx1 | tr -d ' \n')"
  case "$magic" in
    53616c7465645f5f*) printf 'encrypted' ;;  # "Salted__"
    1f8b*) printf 'archive' ;;
    *)
      if head -c 64 "$input" | grep -aq '^--'; then
        printf 'sql'
      else
        printf 'unknown'
      fi
      ;;
  esac
}

sforum_unpack_archive() {
  local archive="$1"
  local dest="$2"

  tar -tzf "$archive" >/dev/null 2>&1 || sforum_die "备份归档不是可读的 gzip tar：$archive"
  if tar -tzf "$archive" | grep -Eq '^/|(^|/)\.\.(/|$)'; then
    sforum_die "备份归档包含越界路径，拒绝解包：$archive"
  fi
  mkdir -p "$dest"
  tar -C "$dest" -xzf "$archive"
}

# 把恢复输入统一成 $out_dir 下的 database.sql（可选 manifest.env / assets/*.tar）。
sforum_prepare_payload() {
  local input="$1"
  local out_dir="$2"
  local kind

  mkdir -p "$out_dir"
  kind="$(sforum_detect_payload "$input")"
  case "$kind" in
    encrypted)
      sforum_require_backup_key "恢复加密备份"
      sforum_decrypt_file "$input" "$out_dir/payload.tar.gz"
      sforum_unpack_archive "$out_dir/payload.tar.gz" "$out_dir"
      rm -f "$out_dir/payload.tar.gz"
      ;;
    archive)
      sforum_unpack_archive "$input" "$out_dir"
      ;;
    sql)
      cp "$input" "$out_dir/database.sql"
      ;;
    *)
      sforum_die "无法识别的恢复输入（既不是加密归档、明文归档，也不是 SQL 转储）：$input"
      ;;
  esac

  [ -f "$out_dir/database.sql" ] || sforum_die "恢复内容缺少 database.sql：$input"
  [ -s "$out_dir/database.sql" ] || sforum_die "恢复内容里的 database.sql 为空：$input"
  chmod 600 "$out_dir/database.sql"
}

# 行数从转储本身统计：备份不停应用，直接查线上库会和转储内容漂移，
# 让「恢复后内容校验」永远对不上。统计 COPY 块的数据行才是最忠实的口径。
sforum_sql_row_counts() {
  local sql="$1"
  local out="$2"
  local tables_csv

  tables_csv="$(IFS=,; printf '%s' "${SFORUM_CORE_TABLES[*]}")"
  awk -v want="$tables_csv" '
    BEGIN {
      n = split(want, names, ",")
      for (i = 1; i <= n; i++) core[names[i]] = 1
    }
    /^COPY public\./ {
      name = $0
      sub(/^COPY public\./, "", name)
      sub(/[ (].*$/, "", name)
      gsub(/"/, "", name)
      if (name in core) {
        current = name
        rows[name] = 0
        counting = 1
      } else {
        counting = 0
        current = ""
      }
      next
    }
    counting && /^\\\.$/ {
      counting = 0
      current = ""
      next
    }
    counting && current != "" {
      rows[current]++
    }
    END {
      for (name in rows) printf "row_count.%s=%s\n", name, rows[name]
    }
  ' "$sql" | sort > "$out"
}

sforum_sql_missing_core_tables() {
  local sql="$1"
  local tables_csv
  tables_csv="$(IFS=,; printf '%s' "${SFORUM_CORE_TABLES[*]}")"

  awk -v want="$tables_csv" '
    BEGIN {
      n = split(want, names, ",")
      for (i = 1; i <= n; i++) pending[names[i]] = 1
    }
    /^CREATE TABLE public\./ {
      name = $0
      sub(/^CREATE TABLE public\./, "", name)
      sub(/[ (].*$/, "", name)
      gsub(/"/, "", name)
      delete pending[name]
      next
    }
    /^COPY public\./ {
      name = $0
      sub(/^COPY public\./, "", name)
      sub(/[ (].*$/, "", name)
      gsub(/"/, "", name)
      delete pending[name]
    }
    END {
      for (key in pending) print key
    }
  ' "$sql"
}

# 备份可解析性预检：切库之前先证明「这份 SQL 里确实有完整核心表」，
# 而不是等灌完库再看关系数 > 0。
sforum_validate_sql_payload() {
  local sql="$1"
  local missing=""

  missing="$(sforum_sql_missing_core_tables "$sql" | sort | tr '\n' ' ')"
  if [ -n "${missing// /}" ]; then
    sforum_die "备份内容不完整，缺少核心表：${missing% }"
  fi
}

sforum_count_tar_entries() {
  local archive="$1"
  local count
  count="$(tar -tf "$archive" 2>/dev/null | grep -c -v '/$' || true)"
  printf '%s' "${count:-0}"
}

# 从卷里读内容：用同一镜像起一次性容器，不要求业务容器在跑，
# 也不引入镜像之外的新工具。
sforum_archive_volume() {
  local container_dir="$1"
  local out="$2"

  [ -n "$container_dir" ] || sforum_die "归档卷时容器内目录为空。"
  if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --no-build --entrypoint tar \
    api -C "$container_dir" -cf - . > "$out"; then
    sforum_die "读取容器目录失败：$container_dir"
  fi
  [ -s "$out" ] || sforum_die "容器目录产生了空归档：$container_dir"
  tar -tf "$out" >/dev/null 2>&1 || sforum_die "卷归档不可读：$out"
}

# 附件卷 + 扩展制品卷一起进归档，并记下条目数供人工核对。
sforum_collect_assets() {
  local assets_dir="$1"

  mkdir -p "$assets_dir"
  sforum_archive_volume "$SFORUM_ATTACHMENT_CONTAINER_DIR" "$assets_dir/attachments.tar"
  sforum_archive_volume "$EXTENSION_CONTAINER_DIR" "$assets_dir/extensions.tar"
  SFORUM_ATTACHMENT_ENTRIES="$(sforum_count_tar_entries "$assets_dir/attachments.tar")"
  SFORUM_EXTENSION_ENTRIES="$(sforum_count_tar_entries "$assets_dir/extensions.tar")"
}

sforum_write_manifest() {
  local out="$1"
  local postgres_db="$2"
  local rows_file="$3"
  local tables_csv

  tables_csv="$(IFS=,; printf '%s' "${SFORUM_CORE_TABLES[*]}")"
  {
    printf 'format_version=%s\n' "$SFORUM_BACKUP_FORMAT_VERSION"
    printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'postgres_db=%s\n' "$postgres_db"
    printf 'core_tables=%s\n' "$tables_csv"
    printf 'encryption=%s\n' "$SFORUM_BACKUP_ENCRYPTION_SCHEME"
    printf 'attachments_entries=%s\n' "$SFORUM_ATTACHMENT_ENTRIES"
    printf 'extension_entries=%s\n' "$SFORUM_EXTENSION_ENTRIES"
    if [ -f "$rows_file" ]; then
      cat "$rows_file"
    fi
  } > "$out"
  chmod 600 "$out"
}

# 恢复卷内容：覆盖同名文件。归档里不存在的旧文件不会被删除（避免误删运营数据），
# 快照留在 $SFORUM_ASSET_SNAPSHOT_DIR 供回滚。
sforum_restore_assets() {
  local archive_dir="$1"
  local snapshot_dir="$2"
  local name dir archive

  mkdir -p "$snapshot_dir"

  for name in attachments extensions; do
    archive="$archive_dir/$name.tar"
    [ -f "$archive" ] || continue
    case "$name" in
      attachments) dir="$SFORUM_ATTACHMENT_CONTAINER_DIR" ;;
      *) dir="$EXTENSION_CONTAINER_DIR" ;;
    esac
    sforum_archive_volume "$dir" "$snapshot_dir/$name.before.tar"
    if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --no-build --entrypoint tar \
      api -C "$dir" -xf - < "$archive"; then
      sforum_die "解包卷内容失败：$dir"
    fi
    printf 'Restored volume: %s\n' "$dir"
  done
}

# 回滚：把快照覆盖回去。恢复期间新增、而快照里没有的文件不会自动消失，会打印出来。
sforum_restore_asset_snapshots() {
  local snapshot_dir="$1"
  local name dir snapshot

  for name in attachments extensions; do
    snapshot="$snapshot_dir/$name.before.tar"
    [ -f "$snapshot" ] || continue
    case "$name" in
      attachments) dir="$SFORUM_ATTACHMENT_CONTAINER_DIR" ;;
      *) dir="$EXTENSION_CONTAINER_DIR" ;;
    esac
    if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --no-build --entrypoint tar \
      api -C "$dir" -xf - < "$snapshot"; then
      printf '警告：卷快照回滚失败，需人工恢复：%s（快照 %s）\n' "$dir" "$snapshot" >&2
      return 1
    fi
    printf '已回滚卷内容：%s\n' "$dir" >&2
  done
}

# 把 tar 详细列表归一化成 "<name>\t<size>"，供逐条比对。
#
# 为什么比大小而不是比哈希：卷恢复的真实失败形态是「条目缺失」与「写一半 /
# 真实截断」，这两类都会改变大小；全量哈希要把整个附件卷再读一遍（可能是几十 GB），
# 代价与该风险的量级不成比例。同名同大小的内容替换不会被这一层发现 —— 那属于
# 另一个问题域（备份归档本身的完整性由 openssl 解密与 tar 自检覆盖）。
sforum_tar_sizes() {
  local archive="$1"
  local out="$2"

  tar -tvf "$archive" 2>/dev/null | awk '
    NF >= 6 {
      size = $3
      name = $6
      for (i = 7; i <= NF; i++) name = name " " $i
      sub(/^\.\//, "", name)
      if (size ~ /^[0-9]+$/) printf "%s\t%s\n", name, size
    }
  ' | sort > "$out"
}

# 逐条比对「归档里有什么」与「卷里恢复出什么」：条目名必须齐，大小必须等。
sforum_compare_tar_sizes() {
  local want="$1"
  local got="$2"

  awk -F'\t' '
    FNR == NR { size[$1] = $2; order[++n] = $1; next }
    { actual[$1] = $2 }
    END {
      missing = 0
      truncated = 0
      for (i = 1; i <= n; i++) {
        name = order[i]
        if (!(name in actual)) {
          if (missing < 5) printf "缺少条目：%s\n", name > "/dev/stderr"
          missing++
          continue
        }
        if (actual[name] != size[name]) {
          if (truncated < 5) {
            printf "大小不符：%s（归档 %s 字节，卷里 %s 字节）\n", name, size[name], actual[name] > "/dev/stderr"
          }
          truncated++
        }
      }
      if (missing || truncated) {
        printf "卷内容校验失败：%d 个条目缺失、%d 个条目大小不符\n", missing, truncated > "/dev/stderr"
        exit 1
      }
    }
  ' "$want" "$got"
}

# 解包后逐条比对：归档里的每个条目都必须出现在卷里，且大小一致。
sforum_verify_restored_assets() {
  local archive_dir="$1"
  local work_dir="$2"
  local name dir archive

  for name in attachments extensions; do
    archive="$archive_dir/$name.tar"
    [ -f "$archive" ] || continue
    case "$name" in
      attachments) dir="$SFORUM_ATTACHMENT_CONTAINER_DIR" ;;
      *) dir="$EXTENSION_CONTAINER_DIR" ;;
    esac
    sforum_archive_volume "$dir" "$work_dir/$name.after.tar"
    sforum_tar_sizes "$archive" "$work_dir/$name.want.list"
    sforum_tar_sizes "$work_dir/$name.after.tar" "$work_dir/$name.got.list"
    # 列表解析不出任何条目时不能当成「校验通过」：那是解析失败，不是空归档。
    [ -s "$work_dir/$name.want.list" ] || sforum_die "无法解析归档条目列表：$archive"
    sforum_compare_tar_sizes "$work_dir/$name.want.list" "$work_dir/$name.got.list" || \
      sforum_die "卷内容校验失败：$dir"
    printf 'Volume content validated: %s\n' "$dir"
  done
}

sforum_retention_value() {
  local name="$1"
  local default_value="$2"
  local raw="${!name:-}"

  case "$raw" in
    ""|default) printf '%s\n' "$default_value" ;;
    *[!0-9]*) sforum_die "Invalid $name: $raw (positive integer or 'default')" ;;
    *)
      [ "$raw" -ge 1 ] || sforum_die "Invalid $name: $raw (must be >= 1)"
      printf '%s\n' "$raw"
      ;;
  esac
}

# SIGKILL（不是 SIGTERM）不会走 EXIT trap，$SFORUM_BACKUP_DIR 里会留下
# .sforum-backup-* 临时目录：700 目录 + 600 的明文 database.sql。retention 只扫
# 归档文件，这些目录原本会一直躺在盘上。按小时判定「陈旧」而不是立刻删，是为了
# 不踩到另一个正在跑的备份（它的临时目录同样匹配这个模式）。
sforum_sweep_stale_workdirs() {
  local dir="$1"
  local hours="${SFORUM_BACKUP_STALE_WORKDIR_HOURS:-24}"
  local stale

  sforum_require_uint SFORUM_BACKUP_STALE_WORKDIR_HOURS "$hours" 1
  while IFS= read -r stale; do
    [ -n "$stale" ] || continue
    rm -rf -- "$stale"
    printf 'Retention: removed stale work directory %s (crash leftover older than %sh)\n' \
      "${stale##*/}" "$hours" >&2
  done < <(find "$dir" -mindepth 1 -maxdepth 1 -type d -name '.sforum-backup-*' \
    -mmin +$((hours * 60)) 2>/dev/null | sort)
}

# 保留策略：先按天数删超龄归档，再按份数只留最新 N 份。
# 输出走 stderr：备份脚本的 stdout 末行必须是归档路径（deploy.sh 用 tail -n 1 取值）。
sforum_apply_retention() {
  local dir="$1"
  local count days kept=0 file

  sforum_sweep_stale_workdirs "$dir"

  count="$(sforum_retention_value SFORUM_BACKUP_RETENTION_COUNT "$SFORUM_BACKUP_RETENTION_COUNT_DEFAULT")"
  days="$(sforum_retention_value SFORUM_BACKUP_RETENTION_DAYS "$SFORUM_BACKUP_RETENTION_DAYS_DEFAULT")"

  while IFS= read -r file; do
    [ -n "$file" ] || continue
    rm -f -- "$file"
    printf 'Retention: removed %s (older than %s day(s))\n' "${file##*/}" "$days" >&2
  done < <(find "$dir" -maxdepth 1 -type f -name 'sforum-backup-*' -mtime +"$days" 2>/dev/null | sort)

  while IFS= read -r file; do
    [ -n "$file" ] || continue
    kept=$((kept + 1))
    if [ "$kept" -le "$count" ]; then
      continue
    fi
    rm -f -- "$file"
    printf 'Retention: removed %s (keeping newest %s)\n' "${file##*/}" "$count" >&2
  done < <(find "$dir" -maxdepth 1 -type f -name 'sforum-backup-*' 2>/dev/null | sort -r)
}
