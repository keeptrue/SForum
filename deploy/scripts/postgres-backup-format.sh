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
#   加密（v2）：encrypt-then-MAC。AES-256-CBC + PBKDF2 加密内容，再用从同一口令、
#     不同 PBKDF2 目的串派生出的 MAC key 对**密文**做 HMAC-SHA256，MAC 写在归档
#     信封头（明文 key=value 清单）里，恢复侧先验 MAC 再解密。
#     用法：export SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key   # 推荐，0600
#           # 或 export SFORUM_BACKUP_KEY='<口令>'
#   v1 兼容：旧归档（Salted__ 开头的纯密文）仍可恢复，但会打印降级告警。
#   保留：SFORUM_BACKUP_RETENTION_COUNT（默认 14 份）/ _DAYS（默认 30 天），
#         任一设为 default 即回默认值。
#
# 被 backup-postgres.sh / restore-postgres.sh source；依赖 postgres-common.sh。
# shellcheck shell=bash

# 归档格式版本与「加密方案」是两件事：format_version 描述 tar 内布局，
# encryption 描述外层怎么封。v2 只改了后者，所以 format_version 不动。
SFORUM_BACKUP_FORMAT_VERSION=2
# v1：只有加密、没有完整性认证。磁盘写权限者可以翻转 CBC 的 IV 位，确定性地
# 篡改首段明文而解密依旧「成功」。保留读取能力，旧备份必须还能恢复。
SFORUM_BACKUP_ENCRYPTION_SCHEME_V1="openssl-aes-256-cbc-pbkdf2-iter200000"
# v2：encrypt-then-MAC（同一 AES-256-CBC 密文 + 独立派生 MAC key 的 HMAC-SHA256）。
SFORUM_BACKUP_ENCRYPTION_SCHEME="openssl-aes-256-cbc-pbkdf2-iter200000+hmac-sha256"
# 信封头第一行。恢复侧靠它区分「v2 信封」与「v1 纯密文（Salted__）」。
SFORUM_BACKUP_ENVELOPE_MAGIC="SFORUM-BACKUP-ENVELOPE 2"
SFORUM_BACKUP_MAC_KDF_ITER=200000
# MAC key 的 PBKDF2 目的串："sforum-b"（"sforum-backup-mac-v2" 的前 8 字节，
# 十六进制）。它与加密侧每次随机的 salt 必然不同，因此两把 key 由同一口令派生但
# 不可互相推导。
# 只写 8 字节是因为 `openssl enc -S` 的 salt 缓冲区上限就是 8 字节：更长的十六进制
# 会被截断到前 8 字节，同时往 stderr 打一句 "hex string is too long, ignoring
# excess"（被下面的 2>/dev/null 吞掉）。这里用的正是完整目的串
# （73666f72756d2d6261636b75702d6d61632d7632）的截断结果，所以写全 18 字节派生出的
# key 与这里完全相同；用短常量只是让「实际生效的 salt」和注释一致。
SFORUM_BACKUP_MAC_SALT_HEX="73666f72756d2d62"
SFORUM_BACKUP_MAC_KEY_HEX=""
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

# MAC key 派生：把 openssl enc 自己当 PBKDF2 引擎 —— 取 AES-256-CTR 对全零输入的
# 前 32 字节 keystream。这样口令只经 `-pass file:/env:` 进入 openssl，不会出现在
# 进程 argv 里（`openssl kdf` 只收 `-kdfopt hexpass:`，等于把口令交给 ps）。
# PBKDF2 的 salt 用上面的固定目的串，与加密侧每次随机的 salt 不同：
# 同一口令派生出两把不可互相推导的 key，且每次备份的 MAC key 相同（HMAC key
# 复用本身不是问题，跨归档的重放威胁也不在这一层）。
sforum_derive_mac_key() {
  local key_hex

  [ "${#SFORUM_KEY_ARGS[@]}" -gt 0 ] || sforum_die "派生 MAC key 前必须先加载备份密钥。"
  key_hex="$(dd if=/dev/zero bs=32 count=1 2>/dev/null | \
    openssl enc -aes-256-ctr -pbkdf2 -iter "$SFORUM_BACKUP_MAC_KDF_ITER" -md sha256 \
      -S "$SFORUM_BACKUP_MAC_SALT_HEX" "${SFORUM_KEY_ARGS[@]}" 2>/dev/null | \
    od -An -tx1 -v | tr -d ' \n')" || sforum_die "无法从备份口令派生 MAC key。"
  [ "${#key_hex}" -eq 64 ] || sforum_die "派生出的 MAC key 长度非法：${#key_hex} 个十六进制字符"
  SFORUM_BACKUP_MAC_KEY_HEX="$key_hex"
}

# 对文件算 HMAC-SHA256，输出 64 位十六进制。
#
# 已知限制（接受，不修）：`openssl dgst` 没有从文件/环境读 MAC key 的入口，key 只能
# 走 `-macopt hexkey:`，因此派生出的 MAC key 会短暂出现在进程 argv 里。能读到它的
# 本机用户最多伪造 MAC（不能解密、拿不到口令）；口令本身始终只经 -pass file:/env:
# 进入 openssl，从不进 argv。触发条件是「同一宿主上有其它可读 /proc/<pid>/cmdline
# 的用户，且正好赶上备份运行」—— 多用户宿主、共享 CI 机器属于这一类；单租户 VPS
# 与 root 独占的备份机不构成实际暴露。要彻底消除需要换掉 openssl dgst（引入库依赖），
# 与「不改仓库依赖」的约束冲突，故按已知限制记录。
sforum_hmac_sha256_file() {
  local file="$1"
  local key_hex="${2:-$SFORUM_BACKUP_MAC_KEY_HEX}"

  [ -n "$key_hex" ] || sforum_die "计算 HMAC 前必须先派生 MAC key。"
  openssl dgst -sha256 -mac HMAC -macopt "hexkey:$key_hex" -binary "$file" | \
    od -An -tx1 -v | tr -d ' \n'
}

# v2 归档 = 明文信封头 + v1 同款密文。
#   信封头（key=value，空行结束）: magic / scheme / mac-alg / mac-kdf / mac-salt /
#   mac-iter / mac-tag。MAC 必须落在密文之外，否则「先验 MAC 再解密」不可能成立
#   （manifest.env 在密文里面，读它就得先解密）。
#   密文：openssl enc AES-256-CBC + PBKDF2 + salt，与 v1 逐字节同构。
# 先写 $out.tmp 再 mv：加密或算 MAC 中途失败只留临时文件，绝不产生半成品归档。
sforum_encrypt_file() {
  local in="$1"
  local out="$2"
  local body="$out.body.tmp"
  local envelope="$out.tmp"
  local mac_hex

  [ "${#SFORUM_KEY_ARGS[@]}" -gt 0 ] || sforum_die "加密前必须先加载备份密钥。"
  # 先派生 MAC key，再碰任何文件：sforum_derive_mac_key 的失败路径都是 sforum_die
  # → exit 1（所以 `sforum_derive_mac_key || ...` 的兜底分支是死代码），此时还没有
  # 任何密文临时文件，备份目录里不会留下 0600 的 .body.tmp —— retention 按
  # `sforum-backup-*` 前缀扫，本来也扫不到它。派生只依赖口令与常量，与密文无关。
  sforum_derive_mac_key
  rm -f "$body" "$envelope"
  openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt \
    "${SFORUM_KEY_ARGS[@]}" -in "$in" -out "$body" >/dev/null 2>&1 || {
      rm -f "$body"
      sforum_die "openssl 加密失败：$out"
    }
  mac_hex="$(sforum_hmac_sha256_file "$body")" || {
    rm -f "$body"
    sforum_die "计算归档 MAC 失败：$out"
  }
  {
    printf '%s\n' "$SFORUM_BACKUP_ENVELOPE_MAGIC"
    printf 'scheme=%s\n' "$SFORUM_BACKUP_ENCRYPTION_SCHEME"
    printf 'mac-alg=hmac-sha256\n'
    printf 'mac-kdf=pbkdf2-hmac-sha256\n'
    printf 'mac-salt=%s\n' "$SFORUM_BACKUP_MAC_SALT_HEX"
    printf 'mac-iter=%s\n' "$SFORUM_BACKUP_MAC_KDF_ITER"
    printf 'mac-tag=%s\n' "$mac_hex"
    printf '\n'
  } > "$envelope" || {
    rm -f "$body" "$envelope"
    sforum_die "写归档信封失败：$out"
  }
  cat "$body" >> "$envelope" || {
    rm -f "$body" "$envelope"
    sforum_die "写归档信封失败：$out"
  }
  rm -f "$body"
  chmod 600 "$envelope"
  mv -f "$envelope" "$out"
  chmod 600 "$out"
}

# 只解密「裸密文」（Salted__ 开头）：v1 归档与 v2 信封的正文都走这里。
sforum_decrypt_file() {
  local in="$1"
  local out="$2"

  [ "${#SFORUM_KEY_ARGS[@]}" -gt 0 ] || sforum_die "解密前必须先加载备份密钥。"
  openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
    "${SFORUM_KEY_ARGS[@]}" -in "$in" -out "$out" >/dev/null 2>&1 || \
    sforum_die "openssl 解密失败（密钥不对或文件损坏）：$in"
  chmod 600 "$out"
}

# 信封头（含结束空行）的字节数：awk 逐行累加 length+1，遇到空行立刻停，
# 不会读到后面的二进制密文。头部是纯 ASCII，length 即字节数。
sforum_envelope_header_bytes() {
  local envelope="$1"

  LC_ALL=C awk '
    { total += length($0) + 1; if ($0 == "") { found = 1; exit } }
    END { if (!found) exit 1; printf "%d\n", total }
  ' "$envelope"
}

# 验 MAC 再解密。任何一步不成立都 fail closed：不写出明文，更不会走到灌库。
sforum_decrypt_envelope() {
  local envelope="$1"
  local out="$2"
  local header="$out.envelope-header"
  local body="$out.envelope-body"
  local header_bytes scheme recorded_mac actual_mac

  header_bytes="$(sforum_envelope_header_bytes "$envelope")" || \
    sforum_die "加密归档的信封头不完整（缺少结束空行）：$envelope"
  case "$header_bytes" in
    ""|*[!0-9]*) sforum_die "加密归档的信封头长度非法：$envelope" ;;
  esac
  head -c "$header_bytes" "$envelope" > "$header"
  tail -c +$((header_bytes + 1)) "$envelope" > "$body"
  [ -s "$body" ] || sforum_die "加密归档只有信封头、没有密文：$envelope"

  scheme="$(sforum_manifest_value "$header" scheme)"
  [ "$scheme" = "$SFORUM_BACKUP_ENCRYPTION_SCHEME" ] || \
    sforum_die "不支持的备份加密方案：${scheme:-<空>}（本版本只认 $SFORUM_BACKUP_ENCRYPTION_SCHEME）"
  recorded_mac="$(sforum_manifest_value "$header" mac-tag)"
  case "$recorded_mac" in
    "") sforum_die "加密归档缺少 MAC，拒绝解密：$envelope" ;;
    *[!0-9a-f]*) sforum_die "加密归档的 MAC 不是小写十六进制：$envelope" ;;
  esac
  [ "${#recorded_mac}" -eq 64 ] || sforum_die "加密归档的 MAC 长度非法：$envelope"

  sforum_require_backup_key "校验加密备份"
  sforum_derive_mac_key
  actual_mac="$(sforum_hmac_sha256_file "$body")" || sforum_die "计算归档 MAC 失败：$envelope"
  [ "$actual_mac" = "$recorded_mac" ] || \
    sforum_die "备份归档完整性校验失败（HMAC-SHA256 不匹配：文件被篡改或口令不对）：$envelope"

  sforum_decrypt_file "$body" "$out"
  rm -f "$header" "$body"
  chmod 600 "$out"
}

# 恢复输入判型：v2 信封 / v1 纯密文 / 明文归档 / 旧版单文件 SQL。
# 认 magic 而不只看扩展名，避免把 .sql 改名成 .enc 就当成加密包。
sforum_detect_payload() {
  local input="$1"
  local magic=""

  if head -c 24 "$input" 2>/dev/null | grep -qF -- "$SFORUM_BACKUP_ENVELOPE_MAGIC"; then
    printf 'envelope'
    return 0
  fi
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

# 无完整性凭据的恢复源：默认 sforum_die，只在显式开关下放行并打警告。
#
# 开关与 P3-22 共用 SFORUM_RESTORE_ALLOW_UNVERIFIED，不新增环境变量：运维只需要记住
# 一个名字，报错文案也点名它，照着就能自救。放行时仍然打警告——一次不可逆的全库替换
# 必须是一次显式决定，而不是「脚本没说话所以应该没事」。
sforum_require_unverified_override() {
  local reason="$1"
  local input="$2"

  if [ "${SFORUM_RESTORE_ALLOW_UNVERIFIED:-}" = "1" ]; then
    printf '警告：%s。已按 SFORUM_RESTORE_ALLOW_UNVERIFIED=1 跳过完整性校验继续恢复：%s（该载荷无完整性凭据，恢复后无法证明来源，请自行确认文件可信）\n' \
      "$reason" "$input" >&2
    return 0
  fi
  sforum_die "$reason：$input（确属手工解密排查后要恢复时，显式设置 SFORUM_RESTORE_ALLOW_UNVERIFIED=1，并自行确认文件可信）"
}

# 清单里的 encryption= 记录「这份内容当初按哪个方案封的」。信封被剥离后，v2 归档
# 会退化成一个合法的 v1 密文（Salted__ 开头），只看外层分不出 v1/v2；所以解密后
# 必须再核对一次清单：声明 v2 却没有信封 → 拒绝，这是唯一的降级入口。
# v1 清单不阻断恢复，但要明确打印降级告警。
#
# 没有清单（或清单缺 encryption= 行）的**明文**载荷默认拒绝：仓库产出的每一份归档
# 都带清单且都写了 encryption=（sforum_write_manifest），所以「缺凭据」只可能是手搓
# 或篡改 —— 这正是「有备份目录写权限者」最省事的一条绕过（不需要密钥，把文件换成
# 自己打的 tar.gz 就能让任意 SQL 走到灌库）。P3-22 堵的是「改动现有归档」，这条补的是
# 「整份替换成自己的载荷」。
#
# 无清单的**加密**载荷仍然放行，且这不是绕过：encrypted 在解包之前必须过
# sforum_require_backup_key 解密，envelope 还要先验 HMAC 再解密；没有密钥的人伪造不出
# 可解密的载荷，所以「没有清单」不构成一条免校验通道。封死它只会误伤合法的 v1 无清单
# 归档（早期手工 openssl enc 产物），DR 现场多一道没有收益的墙。
sforum_check_payload_integrity() {
  local manifest="$1"
  local kind="$2"
  local declared=""

  if [ ! -f "$manifest" ]; then
    case "$kind" in
      envelope|encrypted) return 0 ;;
      *)
        sforum_require_unverified_override \
          "备份载荷没有清单（manifest.env），来源与完整性都无法校验" "$manifest"
        return 0
        ;;
    esac
  fi
  declared="$(sforum_manifest_value "$manifest" encryption)"
  case "$declared" in
    "")
      case "$kind" in
        archive|sql)
          sforum_require_unverified_override \
            "备份清单缺少 encryption= 记录，载荷是明文且没有完整性凭据" "$manifest"
          ;;
        encrypted)
          printf '警告：该备份使用 v1 加密方案（只有加密、无 HMAC 完整性认证），已按兼容模式继续恢复；下一次备份会自动升级到 v2。\n' >&2
          ;;
      esac
      ;;
    "$SFORUM_BACKUP_ENCRYPTION_SCHEME_V1")
      if [ "$kind" = "encrypted" ]; then
        printf '警告：该备份使用 v1 加密方案（只有加密、无 HMAC 完整性认证），已按兼容模式继续恢复；下一次备份会自动升级到 v2。\n' >&2
      fi
      ;;
    "$SFORUM_BACKUP_ENCRYPTION_SCHEME")
      case "$kind" in
        envelope) ;;
        encrypted)
          sforum_die "备份清单声明使用 $declared，但归档外层没有信封（MAC 已被剥离）。拒绝按 v1 降级恢复：$manifest"
          ;;
        *)
          # 明文载荷 + v2 清单：备份侧永远加密，不存在这种合法产物。它正是
          # 「有备份目录写权限者」最省事的一条绕过 —— 不需要密钥，把文件换成自己打的
          # tar.gz 就能让任意 SQL 走到灌库。因此与 encrypted 分支同口径 fail closed，
          # 只留一个显式开关给「手工解密后排查再恢复」的运维通道。
          sforum_require_unverified_override \
            "备份清单声明使用 $declared，但载荷是明文且没有信封，完整性无法校验" "$manifest"
          ;;
      esac
      ;;
    *)
      sforum_die "备份清单记录了未知的加密方案：$declared"
      ;;
  esac
}

# 把恢复输入统一成 $out_dir 下的 database.sql（可选 manifest.env / assets/*.tar）。
sforum_prepare_payload() {
  local input="$1"
  local out_dir="$2"
  local kind

  mkdir -p "$out_dir"
  kind="$(sforum_detect_payload "$input")"
  case "$kind" in
    envelope)
      # 先验 MAC 再解密，且信封头/正文只落在调用方的 0700 工作目录里。
      sforum_decrypt_envelope "$input" "$out_dir/payload.tar.gz"
      sforum_unpack_archive "$out_dir/payload.tar.gz" "$out_dir"
      rm -f "$out_dir/payload.tar.gz"
      ;;
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
  sforum_check_payload_integrity "$out_dir/manifest.env" "$kind"
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
  # `run` 不接受 `--no-build`（那是 `up` / `build` 的 flag，真 compose 会直接
  # `unknown flag: --no-build`）。`run` 本来也不会隐式构建，所以这里不需要它。
  if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --entrypoint tar \
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
    if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --entrypoint tar \
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
    if ! "${SFORUM_COMPOSE[@]}" run --rm -T --no-deps --entrypoint tar \
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
# 另一个问题域（备份归档本身的完整性由 v2 信封的 HMAC-SHA256 与 tar 自检覆盖）。
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
