#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-bootstrap-test.XXXXXX")"
VERSION="v9.8.7-beta.2"
RELEASE_ROOT="$TEMP_DIR/releases/$VERSION"
MOCK_BIN="$TEMP_DIR/bin"

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

make_release() {
  local bundle="$TEMP_DIR/bundle/sforum-deploy" relative
  mkdir -p \
    "$RELEASE_ROOT" \
    "$bundle/deploy/scripts" \
    "$bundle/deploy/caddy" \
    "$bundle/deploy/router" \
    "$bundle/deploy/volumes"
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
    restore-postgres.sh \
    wait-for-health.sh; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "$bundle/deploy/scripts/$relative"
  done
  printf 'target caddy\n' > "$bundle/deploy/caddy/Caddyfile"
  printf 'target router\n' > "$bundle/deploy/router/Caddyfile.example"
  printf 'target volumes\n' > "$bundle/deploy/volumes/README.md"
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

printf 'bootstrap_test.sh: all checks passed\n'
