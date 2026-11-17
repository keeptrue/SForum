#!/usr/bin/env bash
# 一键脚本的备份口令自举验证。
#
# 覆盖两件互相独立的事：
#   1) upgrade.sh / deploy.sh 在完全没有口令时自动生成 0600 口令并写回
#      .env.production；已有配置一字不改，重复执行字节不变；
#   2) 恢复动作只解析、不生成（凭空造新口令会把「缺密钥」变成「口令不对」），
#      但写在 .env.production 里的口令必须认得到。
# 独立备份脚本自身的 fail-closed 语义由 postgres-safety_test.sh 的 C3 守着。
# shellcheck disable=SC1091,SC2034
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-backup-key-test.XXXXXX")"
MOCK_BIN="$TEMP_DIR/bin"
MOCK_LOG="$TEMP_DIR/mock.log"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'backup-key_test.sh: %s\n' "$1" >&2
  exit 1
}

case_ok() {
  printf 'ok - %s\n' "$1"
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

assert_mode() {
  local actual
  actual="$(file_mode "$1")"
  [ "$actual" = "$2" ] || fail "$1 mode is $actual, expected $2"
}

assert_key_shape() {
  [ -s "$1" ] || fail "backup key file is missing or empty: $1"
  grep -Eq '^[0-9a-f]{64}$' "$1" || fail "backup key is not 32 bytes of hex: $1"
}

# --- A. upgrade.sh：source 副本并直接调用 ensure_backup_key -----------------
UPGRADE_ROOT="$TEMP_DIR/upgrade-root"

prepare_upgrade_root() {
  rm -rf "$UPGRADE_ROOT"
  mkdir -p "$UPGRADE_ROOT/deploy/scripts"
  cp "$ROOT_DIR/upgrade.sh" "$UPGRADE_ROOT/"
  cp "$ROOT_DIR/deploy/scripts/deploy-lock.sh" "$UPGRADE_ROOT/deploy/scripts/"
  cp "$ROOT_DIR/deploy/scripts/term-ui.sh" "$UPGRADE_ROOT/deploy/scripts/"
  printf '%s\n' \
    'APP_ENV=production' \
    '# SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key' > "$UPGRADE_ROOT/.env.production"
  chmod 600 "$UPGRADE_ROOT/.env.production"
}

# source 时 upgrade.sh 会 cd 到副本目录，正好把副本当安装根用。额外的
# KEY=VALUE 参数会作为环境变量传进去（用来模拟运维已经 export 的情况）。
ensure_key_in_upgrade() {
  (
    cd "$UPGRADE_ROOT"
    # 单引号里的 ${...} 是有意的：它在子 shell 里展开，不是这里。
    # shellcheck disable=SC2016
    env -u SFORUM_BACKUP_KEY -u SFORUM_BACKUP_KEY_FILE "$@" \
      bash -c 'set -euo pipefail; source ./upgrade.sh; ensure_backup_key >/dev/null; printf "%s\n" "${SFORUM_BACKUP_KEY_FILE:-}"'
  )
}

prepare_upgrade_root
key_path="$(ensure_key_in_upgrade)"
[ "$key_path" = "$UPGRADE_ROOT/.sforum/backup.key" ] || \
  fail "generated key landed at $key_path instead of the install root"
assert_key_shape "$key_path"
assert_mode "$key_path" 600
assert_mode "$UPGRADE_ROOT/.sforum" 700
[ "$(grep -c '^SFORUM_BACKUP_KEY_FILE=' "$UPGRADE_ROOT/.env.production")" = 1 ] || \
  fail ".env.production must carry exactly one active SFORUM_BACKUP_KEY_FILE line"
[ "$(grep -c '^# *SFORUM_BACKUP_KEY_FILE=' "$UPGRADE_ROOT/.env.production")" = 0 ] || \
  fail "the commented template line survived the rewrite"
grep -Fqx "SFORUM_BACKUP_KEY_FILE=$key_path" "$UPGRADE_ROOT/.env.production" || \
  fail ".env.production does not record the generated key path"
assert_mode "$UPGRADE_ROOT/.env.production" 600
case_ok "A1 缺口令：生成 0600 口令并写回 .env.production（注释行被替换）"

key_sha="$(sha256sum "$key_path" | cut -d' ' -f1)"
env_sha="$(sha256sum "$UPGRADE_ROOT/.env.production" | cut -d' ' -f1)"
ensure_key_in_upgrade >/dev/null
[ "$key_sha" = "$(sha256sum "$key_path" | cut -d' ' -f1)" ] || fail "second run rewrote the key"
[ "$env_sha" = "$(sha256sum "$UPGRADE_ROOT/.env.production" | cut -d' ' -f1)" ] || \
  fail "second run rewrote .env.production"
case_ok "A2 幂等：第二次调用口令与配置字节不变"

prepare_upgrade_root
env_before="$(sha256sum "$UPGRADE_ROOT/.env.production" | cut -d' ' -f1)"
ensure_key_in_upgrade SFORUM_BACKUP_KEY=inline-passphrase >/dev/null
[ ! -e "$UPGRADE_ROOT/.sforum/backup.key" ] || fail "inline key must not create a key file"
[ "$env_before" = "$(sha256sum "$UPGRADE_ROOT/.env.production" | cut -d' ' -f1)" ] || \
  fail "inline key must not rewrite .env.production"
case_ok "A3 内联口令：一个字节都不落盘"

prepare_upgrade_root
custom_key="$TEMP_DIR/custom/keys/backup.key"
printf 'SFORUM_BACKUP_KEY_FILE=%s\n' "$custom_key" >> "$UPGRADE_ROOT/.env.production"
ensure_key_in_upgrade >/dev/null
assert_key_shape "$custom_key"
assert_mode "$custom_key" 600
assert_mode "$TEMP_DIR/custom/keys" 700
[ "$(grep -c '^SFORUM_BACKUP_KEY_FILE=' "$UPGRADE_ROOT/.env.production")" = 1 ] || \
  fail "configured key path must stay a single line"
grep -Fqx "SFORUM_BACKUP_KEY_FILE=$custom_key" "$UPGRADE_ROOT/.env.production" || \
  fail "configured key path was not preserved"
case_ok "A4 已配置但文件缺失：在原路径生成，不退回默认路径"

prepare_upgrade_root
empty_key="$TEMP_DIR/empty/backup.key"
mkdir -p "$TEMP_DIR/empty"
: > "$empty_key"
chmod 600 "$empty_key"
printf 'SFORUM_BACKUP_KEY_FILE=%s\n' "$empty_key" >> "$UPGRADE_ROOT/.env.production"
ensure_key_in_upgrade >/dev/null
assert_key_shape "$empty_key"
case_ok "A5 空口令文件：原地补内容"

prepare_upgrade_root
printf 'SFORUM_BACKUP_KEY_FILE=deploy/backup.key\n' >> "$UPGRADE_ROOT/.env.production"
ensure_key_in_upgrade >/dev/null
assert_key_shape "$UPGRADE_ROOT/deploy/backup.key"
grep -Fqx "SFORUM_BACKUP_KEY_FILE=$UPGRADE_ROOT/deploy/backup.key" "$UPGRADE_ROOT/.env.production" || \
  fail "relative key path was not resolved against the install root"
case_ok "A6 相对路径：按安装目录解析并持久化绝对路径"

prepare_upgrade_root
# 只 source、不调用：口令文件必须不存在（自举只发生在真正要备份的时刻）。
(
  cd "$UPGRADE_ROOT"
  env -u SFORUM_BACKUP_KEY -u SFORUM_BACKUP_KEY_FILE \
    bash -c 'set -euo pipefail; source ./upgrade.sh; resolve_backup_key'
)
[ ! -e "$UPGRADE_ROOT/.sforum/backup.key" ] || fail "sourcing upgrade.sh created a key"
case_ok "A7 未触发备份：不生成口令"

# A8：backup_database() 的接线 —— 口令必须在调备份脚本之前就位并导出。
prepare_upgrade_root
cat > "$UPGRADE_ROOT/deploy/scripts/backup-postgres.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'upgrade backup key=%s\n' "${SFORUM_BACKUP_KEY_FILE:-unset}" >> "$MOCK_LOG"
EOF
chmod +x "$UPGRADE_ROOT/deploy/scripts/backup-postgres.sh"
: > "$MOCK_LOG"
(
  cd "$UPGRADE_ROOT"
  env -u SFORUM_BACKUP_KEY -u SFORUM_BACKUP_KEY_FILE MOCK_LOG="$MOCK_LOG" \
    bash -c 'set -euo pipefail; source ./upgrade.sh; backup_database >/dev/null'
)
grep -Fq "upgrade backup key=$UPGRADE_ROOT/.sforum/backup.key" "$MOCK_LOG" || \
  fail "upgrade.sh did not hand the generated key to backup-postgres.sh"
case_ok "A8 升级接线：backup_database 先自举再调备份脚本"

# --- B. deploy.sh：整脚本跑 backup / restore -------------------------------
DEPLOY_ROOT="$TEMP_DIR/deploy-root"
mkdir -p "$DEPLOY_ROOT/deploy/scripts" "$MOCK_BIN"
cp "$ROOT_DIR/deploy.sh" "$DEPLOY_ROOT/"
cp "$ROOT_DIR/compose.yaml" "$ROOT_DIR/compose.prod.yaml" "$ROOT_DIR/compose.release.yaml" \
  "$ROOT_DIR/compose.zero-downtime.yaml" "$DEPLOY_ROOT/"
cp "$ROOT_DIR/deploy/scripts/deploy-lock.sh" "$DEPLOY_ROOT/deploy/scripts/"
cp "$ROOT_DIR/deploy/scripts/term-ui.sh" "$DEPLOY_ROOT/deploy/scripts/"

"$ROOT_DIR/deploy/scripts/configure-production.sh" \
  --lang en --defaults --version v3.0.0 \
  --root "$DEPLOY_ROOT" --output "$DEPLOY_ROOT/.env.production" >/dev/null

cat > "$DEPLOY_ROOT/deploy/scripts/backup-postgres.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'backup key=%s\n' "${SFORUM_BACKUP_KEY_FILE:-unset}" >> "$MOCK_LOG"
printf '%s\n' "${MOCK_LOG%/*}/archive.tar.gz.enc"
EOF

cat > "$DEPLOY_ROOT/deploy/scripts/restore-postgres.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'restore key=%s file=%s\n' "${SFORUM_BACKUP_KEY_FILE:-unset}" "${1:-}" >> "$MOCK_LOG"
EOF
chmod +x "$DEPLOY_ROOT/deploy/scripts/backup-postgres.sh" \
  "$DEPLOY_ROOT/deploy/scripts/restore-postgres.sh"

cat > "$MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "info" ]; then
  exit 0
fi
[ "${1:-}" = "compose" ] || exit 90
shift
if [ "${1:-}" = "version" ]; then
  if [ "${2:-}" = "--short" ]; then
    printf '2.24.4\n'
  else
    printf 'Docker Compose version v2.24.4\n'
  fi
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    -f|--env-file|--project-name|--profile)
      [ "$#" -ge 2 ] || exit 91
      shift 2
      ;;
    *) break ;;
  esac
done
command="${1:-}"
case "$command" in
  config|up) exit 0 ;;
  ps) printf 'all services running\n' ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$MOCK_BIN/docker"

run_deploy() {
  (
    cd "$DEPLOY_ROOT"
    env -u SFORUM_BACKUP_KEY -u SFORUM_BACKUP_KEY_FILE \
      PATH="$MOCK_BIN:$PATH" MOCK_LOG="$MOCK_LOG" \
      ./deploy.sh --lang en --version v3.0.0 --action "$1"
  )
}

# B1：没有任何口令时，restore 只解析不生成 —— 明确留给恢复脚本报「缺密钥」。
: > "$MOCK_LOG"
printf '%s\nRESTORE\n' "$TEMP_DIR/absent.tar.gz.enc" | run_deploy restore >/dev/null 2>&1 || \
  fail "restore without a configured key should still reach the restore script"
grep -Fq 'restore key=unset' "$MOCK_LOG" || fail "restore invented a key instead of reporting a missing one"
[ ! -e "$DEPLOY_ROOT/.sforum/backup.key" ] || fail "restore generated a key file"
case_ok "B1 恢复：只解析不生成（缺密钥留给恢复脚本报错）"

# B2：备份动作自举口令，并把路径交给 backup-postgres.sh。
: > "$MOCK_LOG"
run_deploy backup >/dev/null
deploy_key="$DEPLOY_ROOT/.sforum/backup.key"
assert_key_shape "$deploy_key"
assert_mode "$deploy_key" 600
grep -Fq "backup key=$deploy_key" "$MOCK_LOG" || fail "backup script did not receive the generated key path"
[ "$(grep -c '^SFORUM_BACKUP_KEY_FILE=' "$DEPLOY_ROOT/.env.production")" = 1 ] || \
  fail "deploy.sh did not record exactly one SFORUM_BACKUP_KEY_FILE line"
grep -Fqx "SFORUM_BACKUP_KEY_FILE=$deploy_key" "$DEPLOY_ROOT/.env.production" || \
  fail "deploy.sh recorded the wrong key path"
assert_mode "$DEPLOY_ROOT/.env.production" 600
case_ok "B2 备份：deploy.sh 生成口令并交给备份脚本"

deploy_key_sha="$(sha256sum "$deploy_key" | cut -d' ' -f1)"
: > "$MOCK_LOG"
run_deploy backup >/dev/null
[ "$deploy_key_sha" = "$(sha256sum "$deploy_key" | cut -d' ' -f1)" ] || \
  fail "second backup run rewrote the key"
[ "$(grep -c '^SFORUM_BACKUP_KEY_FILE=' "$DEPLOY_ROOT/.env.production")" = 1 ] || \
  fail "second backup run duplicated the config line"
case_ok "B3 备份幂等：重复执行不换口令、不加重复行"

# B4：口令只写在 .env.production 里（没有 export），恢复也必须认得到。
: > "$MOCK_LOG"
printf '%s\nRESTORE\n' "$TEMP_DIR/archive.tar.gz.enc" | run_deploy restore >/dev/null
grep -Fq "restore key=$deploy_key" "$MOCK_LOG" || \
  fail "restore did not pick up SFORUM_BACKUP_KEY_FILE from .env.production"
case_ok "B4 恢复：认得到 .env.production 里的口令路径"

printf 'backup-key_test.sh: all checks passed\n'
