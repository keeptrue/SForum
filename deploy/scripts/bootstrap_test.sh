#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-bootstrap-test.XXXXXX")"
VERSION="v9.8.7-beta.2"
RELEASE_ROOT="$TEMP_DIR/releases/$VERSION"
MOCK_BIN="$TEMP_DIR/bin"
BUNDLE_ROOT=""

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'bootstrap_test.sh: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local file="$1" expected="$2"
  grep -Fq -- "$expected" "$file" || fail "$file does not contain: $expected"
}

assert_not_contains() {
  local file="$1" unexpected="$2"
  if grep -Fq -- "$unexpected" "$file"; then
    fail "$file unexpectedly contains: $unexpected"
  fi
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

# 从某个 bootstrap 副本里取出 managed_files() 的清单。故意与 sforum-bootstrap.sh 的
# 解析路径分开写：断言要比较的是「提升清单」和「磁盘上真正提升出来的集合」。
managed_files_of() {
  awk '
    $0 == "managed_files() {" { in_function = 1; next }
    in_function && /cat <<.EOF.$/ { in_heredoc = 1; next }
    in_function && in_heredoc && $0 == "EOF" { exit }
    in_heredoc { print }
  ' "$1"
}

# 随包分发的 deploy/scripts 脚本里 `source "$SCRIPT_DIR/<file>"` 的目标。
# *_test.sh 不随包分发、且用 $ROOT_DIR 取源，不参与资产闭包。
# 只走 grep + 参数展开（[$]SCRIPT_DIR 而不是单引号里的 $NAME），否则 shellcheck 会判成
# 「本想展开却写进了单引号」（SC2016），而这支测试在 lint-shell 门的检查范围内。
sourced_siblings() {
  local script line name
  for script in "$ROOT_DIR"/deploy/scripts/*.sh; do
    [ -f "$script" ] || continue
    case "$script" in
      *_test.sh) continue ;;
    esac
    while IFS= read -r line; do
      name="${line##*SCRIPT_DIR/}"
      name="${name%%\"*}"
      [ -n "$name" ] || continue
      printf 'deploy/scripts/%s\n' "$name"
    done < <(grep -E '^[[:space:]]*(source|\.)[[:space:]]+"[$]SCRIPT_DIR/' "$script" || true)
  done | sort -u
}

assert_same_set() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    return 0
  fi
  printf 'bootstrap_test.sh: %s\n' "$label" >&2
  printf '  只有期望侧有:\n%s\n' \
    "$(comm -23 <(printf '%s\n' "$expected") <(printf '%s\n' "$actual"))" >&2
  printf '  只有实际侧有:\n%s\n' \
    "$(comm -13 <(printf '%s\n' "$expected") <(printf '%s\n' "$actual"))" >&2
  exit 1
}

make_release() {
  local bundle="$TEMP_DIR/bundle/sforum-deploy" relative
  BUNDLE_ROOT="$bundle"
  mkdir -p \
    "$RELEASE_ROOT" \
    "$bundle/deploy/scripts" \
    "$bundle/deploy/caddy" \
    "$bundle/deploy/router" \
    "$bundle/deploy/volumes" \
    "$bundle/deploy/runtime"
  cp "$ROOT_DIR/sforum-bootstrap.sh" "$RELEASE_ROOT/sforum-bootstrap.sh"
  cp "$ROOT_DIR/sforum-bootstrap.sh" "$bundle/sforum-bootstrap.sh"

  cat > "$bundle/deploy.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'deploy %s\n' "$*" >> "$SFORUM_BOOTSTRAP_TEST_LOG"
printf 'installed\n' > .install-ran
EOF
  cat > "$bundle/upgrade.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ -f .env.production ]
[ -f .deployrc ]
[ "$(cat persistent-data.txt)" = keep-data ]
printf 'upgrade %s\n' "$*" >> "$SFORUM_BOOTSTRAP_TEST_LOG"
printf 'upgraded\n' > .upgrade-ran
EOF
  for relative in \
    compose.yaml \
    compose.prod.yaml \
    compose.release.yaml \
    compose.zero-downtime.yaml \
    .env.production.example; do
    printf 'target=%s\n' "$VERSION" > "$bundle/$relative"
  done
  printf 'version=%s\n' "${VERSION#v}" > "$bundle/VERSION"
  for relative in \
    configure-production.sh \
    backup-postgres.sh \
    postgres-common.sh \
    postgres-backup-format.sh \
    restore-postgres.sh \
    wait-for-health.sh; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "$bundle/deploy/scripts/$relative"
  done
  # 提升动作会 source 这份库来取 .deploy.lock（OPS-05），桩会把它变成空实现，
  # 所以这里放真实文件：发布包发的也是这个文件。
  cp "$ROOT_DIR/deploy/scripts/deploy-lock.sh" "$bundle/deploy/scripts/deploy-lock.sh"
  printf 'target caddy\n' > "$bundle/deploy/caddy/Caddyfile"
  printf 'target router\n' > "$bundle/deploy/router/Caddyfile.example"
  printf 'target volumes\n' > "$bundle/deploy/volumes/README.md"
  printf '*\n!.gitignore\n!.gitkeep\n' > "$bundle/deploy/runtime/.gitignore"
  : > "$bundle/deploy/runtime/.gitkeep"
  chmod 0755 \
    "$RELEASE_ROOT/sforum-bootstrap.sh" \
    "$bundle/sforum-bootstrap.sh" \
    "$bundle/deploy.sh" \
    "$bundle/upgrade.sh" \
    "$bundle"/deploy/scripts/*.sh
  (cd "$TEMP_DIR/bundle" && tar -czf "$RELEASE_ROOT/sforum-deploy.tar.gz" sforum-deploy)
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$RELEASE_ROOT" && sha256sum sforum-bootstrap.sh sforum-deploy.tar.gz > SHA256SUMS)
  else
    (cd "$RELEASE_ROOT" && shasum -a 256 sforum-bootstrap.sh sforum-deploy.tar.gz > SHA256SUMS)
  fi
}

make_mocks() {
  mkdir -p "$MOCK_BIN"
  cat > "$MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o|-fsSLo)
      destination="$2"
      shift 2
      ;;
    -H|--connect-timeout|--max-time)
      shift 2
      ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf 'curl %s\n' "$url" >> "$SFORUM_BOOTSTRAP_TEST_LOG"
case "$url" in
  mock://api/stable)
    printf '%s\n' '{"tag_name":"v9.8.7-beta.2","prerelease":true,"draft":false}'
    ;;
  mock://api/prerelease)
    printf '%s\n' '[{"tag_name":"v9.8.7-beta.2","prerelease":true,"draft":false}]'
    ;;
  mock://releases/*)
    source_path="$SFORUM_BOOTSTRAP_TEST_RELEASES/${url#mock://releases/}"
    [ -n "$destination" ]
    cp "$source_path" "$destination"
    ;;
  *)
    printf 'unexpected URL: %s\n' "$url" >&2
    exit 64
    ;;
esac
EOF
  chmod 0755 "$MOCK_BIN/curl"
}

run_bootstrap() {
  local root="$1"
  shift
  (
    cd "$root"
    PATH="$MOCK_BIN:$PATH" \
      SFORUM_BOOTSTRAP_REFRESHED_VERSION="${SFORUM_BOOTSTRAP_TEST_REFRESHED:+$VERSION}" \
      SFORUM_BOOTSTRAP_TEST_RELEASES="$TEMP_DIR/releases" \
      SFORUM_BOOTSTRAP_TEST_LOG="$root/bootstrap.log" \
      SFORUM_RELEASE_DOWNLOAD_BASE_URL=mock://releases \
      SFORUM_LATEST_RELEASE_API_URL=mock://api/stable \
      SFORUM_RELEASES_API_URL=mock://api/prerelease \
      SFORUM_SKIP_ATTESTATION=true \
      ./sforum-bootstrap.sh "$@"
  )
}

make_release
make_mocks

INSTALL_ROOT="$TEMP_DIR/install"
mkdir -p "$INSTALL_ROOT"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$INSTALL_ROOT/sforum-bootstrap.sh"
printf '# stale bootstrap\n' >> "$INSTALL_ROOT/sforum-bootstrap.sh"
chmod 0755 "$INSTALL_ROOT/sforum-bootstrap.sh"
if ! run_bootstrap "$INSTALL_ROOT" install --channel prerelease --lang en --yes \
  > "$TEMP_DIR/install.out" 2>&1; then
  cat "$TEMP_DIR/install.out" >&2
  fail "fresh install bootstrap failed"
fi
[ -f "$INSTALL_ROOT/.install-ran" ] || fail "fresh install did not hand off to deploy.sh"
EXPECTED_INSTALL_ROOT="$(cd "$INSTALL_ROOT" && pwd -P)"
assert_contains "$TEMP_DIR/install.out" "Install directory: $EXPECTED_INSTALL_ROOT"
assert_contains "$INSTALL_ROOT/bootstrap.log" "deploy --action deploy --version $VERSION --lang en --yes"
assert_contains "$INSTALL_ROOT/compose.yaml" "target=$VERSION"
[ "$(file_mode "$INSTALL_ROOT/sforum-bootstrap.sh")" = 755 ] || fail "bootstrap mode is not 0755"
[ "$(file_mode "$INSTALL_ROOT/deploy.sh")" = 755 ] || fail "deploy.sh mode is not 0755"
assert_not_contains "$INSTALL_ROOT/sforum-bootstrap.sh" "stale bootstrap"

# (a) 提升后的集合必须与 managed_files() 完全一致（R7-OPS-01）：归档里有、清单里
# 漏掉的文件不会让安装报错，它安静地躺在包里，直到操作员在已经装好的实例上第一次跑
# backup/restore 才崩在 source 行上。所以两边都对齐到「发布包里到底有什么」。
installed_managed="$(managed_files_of "$INSTALL_ROOT/sforum-bootstrap.sh" | sort -u)"
bundle_manifest="$(cd "$BUNDLE_ROOT" && find . -type f | sed 's|^\./||' | sort)"
promoted_manifest="$(
  cd "$INSTALL_ROOT" &&
    find . -type f ! -name 'bootstrap.log' ! -name '.install-ran' | sed 's|^\./||' | sort
)"
[ -n "$installed_managed" ] || fail "could not read managed_files() from the promoted bootstrap"
assert_same_set "提升清单 managed_files() 与发布包文件集合不一致" "$bundle_manifest" "$installed_managed"
assert_same_set "磁盘上真正提升出来的文件与发布包文件集合不一致" "$bundle_manifest" "$promoted_manifest"

# (b) deploy/scripts 里每个 source "$SCRIPT_DIR/<file>" 的目标都必须在提升清单内，
# 否则包里有、实例里没有，install 当场 die 或第一次 backup/restore 崩掉。
while IFS= read -r dependency; do
  [ -n "$dependency" ] || continue
  grep -qxF -- "$dependency" <<< "$installed_managed" ||
    fail "$dependency 被 deploy/scripts 的脚本 source，但 managed_files() 不会提升它"
done < <(sourced_siblings)

UPGRADE_ROOT="$TEMP_DIR/upgrade"
mkdir -p "$UPGRADE_ROOT/deploy/runtime"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$UPGRADE_ROOT/sforum-bootstrap.sh"
printf '# stale bootstrap\n' >> "$UPGRADE_ROOT/sforum-bootstrap.sh"
printf 'old compose\n' > "$UPGRADE_ROOT/compose.yaml"
printf 'keep-env\n' > "$UPGRADE_ROOT/.env.production"
printf 'version=v9.8.6\n' > "$UPGRADE_ROOT/.deployrc"
printf 'keep-runtime\n' > "$UPGRADE_ROOT/deploy/runtime/state"
printf 'keep-data\n' > "$UPGRADE_ROOT/persistent-data.txt"
chmod 0755 "$UPGRADE_ROOT/sforum-bootstrap.sh"
run_bootstrap "$UPGRADE_ROOT" upgrade "$VERSION" --yes --bootstrap
[ -f "$UPGRADE_ROOT/.upgrade-ran" ] || fail "existing install did not hand off to upgrade.sh"
assert_contains "$UPGRADE_ROOT/bootstrap.log" "upgrade --version $VERSION --yes --bootstrap"
[ "$(cat "$UPGRADE_ROOT/.env.production")" = keep-env ] || fail ".env.production was changed"
assert_contains "$UPGRADE_ROOT/.deployrc" "version=v9.8.6"
[ "$(cat "$UPGRADE_ROOT/deploy/runtime/state")" = keep-runtime ] || fail "runtime state was changed"
[ "$(cat "$UPGRADE_ROOT/persistent-data.txt")" = keep-data ] || fail "persistent data was changed"
assert_contains "$UPGRADE_ROOT/compose.yaml" "target=$VERSION"
backup_compose="$(find "$UPGRADE_ROOT/.sforum/tooling-backups" -path '*/compose.yaml' -type f | head -n 1)"
[ -n "$backup_compose" ] || fail "existing deployment tools were not backed up"
assert_contains "$backup_compose" "old compose"

FAILED_ROOT="$TEMP_DIR/failed"
mkdir -p "$FAILED_ROOT"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$FAILED_ROOT/sforum-bootstrap.sh"
printf 'keep-old-bootstrap\n' >> "$FAILED_ROOT/sforum-bootstrap.sh"
chmod 0755 "$FAILED_ROOT/sforum-bootstrap.sh"
cp "$RELEASE_ROOT/SHA256SUMS" "$TEMP_DIR/valid-SHA256SUMS"
printf 'tampered\n' > "$RELEASE_ROOT/sforum-bootstrap.sh"
if run_bootstrap "$FAILED_ROOT" install --version "$VERSION" --yes >"$FAILED_ROOT/output" 2>&1; then
  fail "tampered bootstrap unexpectedly passed verification"
fi
assert_contains "$FAILED_ROOT/sforum-bootstrap.sh" "keep-old-bootstrap"
[ ! -e "$FAILED_ROOT/.install-ran" ] || fail "install ran after checksum failure"
mv "$TEMP_DIR/valid-SHA256SUMS" "$RELEASE_ROOT/SHA256SUMS"

if run_bootstrap "$INSTALL_ROOT" install --version "$VERSION" --bootstrap >"$TEMP_DIR/invalid.out" 2>&1; then
  fail "install accepted the upgrade-only --bootstrap option"
fi
assert_contains "$TEMP_DIR/invalid.out" "--bootstrap is only valid with upgrade"

# OPS-05：提升动作进入 .deploy.lock 互斥域。自刷新（只覆盖 sforum-bootstrap.sh）与提升
# 是两件事，用 SFORUM_BOOTSTRAP_REFRESHED_VERSION 跳过前者，让断言只盯提升动作。
LOCKED_ROOT="$TEMP_DIR/locked"
mkdir -p "$LOCKED_ROOT"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$LOCKED_ROOT/sforum-bootstrap.sh"
chmod 0755 "$LOCKED_ROOT/sforum-bootstrap.sh"
printf 'keep-locked-compose\n' > "$LOCKED_ROOT/compose.yaml"
mkdir "$LOCKED_ROOT/.deploy.lock"
printf '%s\n' "$$" > "$LOCKED_ROOT/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=%s\nproc_start=%s\n' "$$" \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  "$(LC_ALL=C ps -o lstart= -p "$$" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')" \
  > "$LOCKED_ROOT/.deploy.lock/owner"
if SFORUM_BOOTSTRAP_TEST_REFRESHED=1 run_bootstrap "$LOCKED_ROOT" install \
  --version "$VERSION" --yes >"$TEMP_DIR/locked.out" 2>&1; then
  fail "promoted the toolkit while .deploy.lock was held by a live process"
fi
assert_contains "$TEMP_DIR/locked.out" "another deployment operation holds .deploy.lock"
[ "$(cat "$LOCKED_ROOT/compose.yaml")" = keep-locked-compose ] || \
  fail "managed files changed even though the deploy lock was live"
[ ! -e "$LOCKED_ROOT/deploy.sh" ] || fail "deploy.sh was written while the deploy lock was live"
[ ! -e "$LOCKED_ROOT/.sforum/tooling-backups" ] || \
  fail "promotion started (backup written) before the deploy lock was acquired"
[ "$(cat "$LOCKED_ROOT/.deploy.lock/pid")" = "$$" ] || fail "the live deploy lock was stolen"
[ ! -e "$LOCKED_ROOT/.sforum-bootstrap.lock" ] || fail "bootstrap lock leaked after a refused promotion"
[ ! -e "$LOCKED_ROOT/.install-ran" ] || fail "install ran while the deploy lock was live"

# 2) 死 pid（进程早已退出）→ 回收陈旧锁后继续完成提升。
STALE_ROOT="$TEMP_DIR/stale"
mkdir -p "$STALE_ROOT"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$STALE_ROOT/sforum-bootstrap.sh"
chmod 0755 "$STALE_ROOT/sforum-bootstrap.sh"
dead_pid="$(sh -c 'printf "%s" "$$"')"
mkdir "$STALE_ROOT/.deploy.lock"
printf '%s\n' "$dead_pid" > "$STALE_ROOT/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=2020-01-01T00:00:00Z\n' "$dead_pid" \
  > "$STALE_ROOT/.deploy.lock/owner"
if ! SFORUM_BOOTSTRAP_TEST_REFRESHED=1 run_bootstrap "$STALE_ROOT" install \
  --version "$VERSION" --yes >"$TEMP_DIR/stale.out" 2>&1; then
  cat "$TEMP_DIR/stale.out" >&2
  fail "stale deploy lock was not reclaimed; the promotion never ran"
fi
assert_contains "$STALE_ROOT/compose.yaml" "target=$VERSION"
[ -f "$STALE_ROOT/.install-ran" ] || fail "install did not run after reclaiming the stale lock"
[ ! -e "$STALE_ROOT/.deploy.lock" ] || fail "the deploy lock was not released after promotion"
[ -z "$(find "$STALE_ROOT" -maxdepth 1 -name '.deploy.lock.stale.*' -print -quit)" ] || \
  fail "a reclaimed lock copy was left behind"

# 3) 提升中途失败：cleanup 必须同时清掉提升锁与 bootstrap 锁（这条分支此前零覆盖）。
#    注意不能用「deploy.sh 预置成目录」造失败 —— mv file existing-dir 会把文件挪进去，
#    仍然返回 0。改为把 deploy 预置成普通文件，让后面 promote_file 的
#    mkdir -p "$ROOT_DIR/deploy/scripts" 必然失败。
FAIL_ROOT="$TEMP_DIR/failpromote"
mkdir -p "$FAIL_ROOT"
cp "$ROOT_DIR/sforum-bootstrap.sh" "$FAIL_ROOT/sforum-bootstrap.sh"
chmod 0755 "$FAIL_ROOT/sforum-bootstrap.sh"
printf 'keep-env\n' > "$FAIL_ROOT/.env.production"
printf 'version=v9.8.6\n' > "$FAIL_ROOT/.deployrc"
printf 'not a directory\n' > "$FAIL_ROOT/deploy"
if SFORUM_BOOTSTRAP_TEST_REFRESHED=1 run_bootstrap "$FAIL_ROOT" upgrade \
  --version "$VERSION" --yes >"$TEMP_DIR/failpromote.out" 2>&1; then
  fail "promotion unexpectedly succeeded with a file where deploy/ should be"
fi
# 钉死失败点：必须是 promote_file 的 die 分支，而不是更早的下载/校验/备份失败。
assert_contains "$TEMP_DIR/failpromote.out" \
  'could not promote deploy/scripts/configure-production.sh'
[ -f "$FAIL_ROOT/deploy" ] || fail "the deploy fixture was replaced by a directory"
[ ! -e "$FAIL_ROOT/.deploy.lock" ] || fail "the failure path left .deploy.lock behind"
[ ! -e "$FAIL_ROOT/.sforum-bootstrap.lock" ] || \
  fail "the failure path left .sforum-bootstrap.lock behind"

printf 'bootstrap_test.sh: all checks passed\n'
