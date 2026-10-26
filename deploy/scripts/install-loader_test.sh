#!/usr/bin/env bash
# Behaviour tests for sforum-install.sh, the one-line install loader.
#
# The loader is the only shell entry that is allowed to run straight from a
# pipe, so its job is narrow and must stay verifiable: resolve an immutable
# tag, download and checksum that tag's bootstrap, promote it, and exec it.
# These cases pin that contract, including the failure paths where a tampered
# or incomplete SHA256SUMS must stop before anything is promoted.
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-install-loader-test.XXXXXX")"
STABLE_VERSION="v9.8.7"
PRERELEASE_VERSION="v9.8.7-beta.2"
MOCK_BIN="$TEMP_DIR/bin"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'install-loader_test.sh: %s\n' "$1" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

assert_log() {
  local dir="$1" expected="$2"
  [ -f "$dir/loader.log" ] || fail "$dir: loader.log is missing"
  grep -Fq -- "$expected" "$dir/loader.log" || \
    fail "$dir: loader.log does not contain: $expected"
}

assert_no_staging_left() {
  local dir="$1"
  [ -z "$(find "$dir" -maxdepth 1 -name '.sforum-install.*' -print -quit)" ] || \
    fail "$dir: staging directory was not cleaned up"
}

expect_failure() {
  local label="$1" expected="$2"
  shift 2
  local output status
  set +e
  output="$("$@" 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail "$label: the loader succeeded where it must stop"
  case "$output" in
    *"$expected"*) ;;
    *) fail "$label: expected '$expected' but got: $output" ;;
  esac
}

make_release() {
  local version="$1" dir="$TEMP_DIR/releases/$1"
  mkdir -p "$dir"
  cat > "$dir/sforum-bootstrap.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'bootstrap %s\n' "$*" >> "$SFORUM_INSTALL_TEST_LOG"
EOF
  chmod 0755 "$dir/sforum-bootstrap.sh"
  (
    cd "$dir"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum sforum-bootstrap.sh > SHA256SUMS
    else
      shasum -a 256 sforum-bootstrap.sh > SHA256SUMS
    fi
  )
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
    -o|-fsSLo) destination="$2"; shift 2 ;;
    -H|--connect-timeout|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf 'curl %s\n' "$url" >> "$SFORUM_INSTALL_TEST_LOG"
case "$url" in
  mock://api/stable)
    printf '%s\n' '{"tag_name":"v9.8.7","prerelease":false,"draft":false}'
    ;;
  mock://api/prerelease)
    printf '%s\n' '[{"tag_name":"v9.8.7-beta.2","prerelease":true,"draft":false}]'
    ;;
  mock://releases/*)
    [ -n "$destination" ] || {
      printf 'mock curl: missing -o for %s\n' "$url" >&2
      exit 64
    }
    cp "$SFORUM_INSTALL_TEST_RELEASES/${url#mock://releases/}" "$destination"
    ;;
  *)
    printf 'mock curl: unexpected URL: %s\n' "$url" >&2
    exit 64
    ;;
esac
EOF
  chmod 0755 "$MOCK_BIN/curl"
}

run_loader() {
  local workdir="$1"
  shift
  (
    cd "$workdir"
    PATH="$MOCK_BIN:$PATH" \
      SFORUM_INSTALL_TEST_LOG="$workdir/loader.log" \
      SFORUM_INSTALL_TEST_RELEASES="$TEMP_DIR/releases" \
      SFORUM_RELEASE_DOWNLOAD_BASE_URL=mock://releases \
      SFORUM_LATEST_RELEASE_API_URL=mock://api/stable \
      SFORUM_RELEASES_API_URL=mock://api/prerelease \
      SFORUM_SKIP_ATTESTATION=true \
      bash "$ROOT_DIR/sforum-install.sh" "$@"
  )
}

new_case() {
  local name="$1" dir
  dir="$TEMP_DIR/case-$name"
  mkdir -p "$dir"
  printf '%s' "$dir"
}

make_release "$STABLE_VERSION"
make_release "$PRERELEASE_VERSION"
make_mocks

# Fresh directory: default action is install and the bootstrap lands executable.
CASE="$(new_case fresh)"
run_loader "$CASE" >/dev/null
[ -f "$CASE/sforum/sforum-bootstrap.sh" ] || fail "fresh install did not promote the bootstrap"
[ -x "$CASE/sforum/sforum-bootstrap.sh" ] || fail "promoted bootstrap is not executable"
assert_log "$CASE" "curl mock://releases/$STABLE_VERSION/SHA256SUMS"
assert_log "$CASE" "bootstrap install --version $STABLE_VERSION"
assert_no_staging_left "$CASE/sforum"

# Existing installation: the same one-line command becomes an update and must
# not touch operator state.
CASE="$(new_case upgrade)"
mkdir -p "$CASE/sforum"
printf 'APP_ENV=production\n' > "$CASE/sforum/.env.production"
printf 'version=v9.0.0\n' > "$CASE/sforum/.deployrc"
before="$(sha256_file "$CASE/sforum/.env.production")"
run_loader "$CASE" >/dev/null
assert_log "$CASE" "bootstrap upgrade --version $STABLE_VERSION"
[ "$(sha256_file "$CASE/sforum/.env.production")" = "$before" ] || \
  fail "upgrade modified .env.production"

# Explicit -d installs somewhere else and leaves ./sforum untouched.
CASE="$(new_case custom-dir)"
run_loader "$CASE" -d custom >/dev/null
[ -f "$CASE/custom/sforum-bootstrap.sh" ] || fail "-d did not redirect the install directory"
[ ! -e "$CASE/sforum" ] || fail "-d still created the default directory"

# A tampered bootstrap must stop before promotion.
CASE="$(new_case tampered)"
cp "$TEMP_DIR/releases/$STABLE_VERSION/sforum-bootstrap.sh" "$TEMP_DIR/bootstrap.backup"
printf '\n# tampered\n' >> "$TEMP_DIR/releases/$STABLE_VERSION/sforum-bootstrap.sh"
expect_failure "tampered bootstrap" "checksum verification failed" run_loader "$CASE"
[ ! -f "$CASE/sforum/sforum-bootstrap.sh" ] || fail "tampered bootstrap was promoted"
cp "$TEMP_DIR/bootstrap.backup" "$TEMP_DIR/releases/$STABLE_VERSION/sforum-bootstrap.sh"

# A checksum file without the exact filename entry must stop too.
CASE="$(new_case missing-entry)"
cp "$TEMP_DIR/releases/$STABLE_VERSION/SHA256SUMS" "$TEMP_DIR/SHA256SUMS.backup"
sed 's/sforum-bootstrap\.sh/other-asset.sh/' \
  "$TEMP_DIR/SHA256SUMS.backup" > "$TEMP_DIR/releases/$STABLE_VERSION/SHA256SUMS"
expect_failure "missing checksum entry" "checksum verification failed" run_loader "$CASE"
cp "$TEMP_DIR/SHA256SUMS.backup" "$TEMP_DIR/releases/$STABLE_VERSION/SHA256SUMS"

# A duplicated entry is not "exactly one" either.
CASE="$(new_case duplicate-entry)"
{
  cat "$TEMP_DIR/SHA256SUMS.backup"
  cat "$TEMP_DIR/SHA256SUMS.backup"
} > "$TEMP_DIR/releases/$STABLE_VERSION/SHA256SUMS"
expect_failure "duplicate checksum entry" "checksum verification failed" run_loader "$CASE"
cp "$TEMP_DIR/SHA256SUMS.backup" "$TEMP_DIR/releases/$STABLE_VERSION/SHA256SUMS"

# SFORUM_INSTALL_SHA256 pins the bootstrap beyond SHA256SUMS.
CASE="$(new_case pin-ok)"
pin="$(sha256_file "$TEMP_DIR/releases/$STABLE_VERSION/sforum-bootstrap.sh")"
SFORUM_INSTALL_SHA256="$pin" run_loader "$CASE" >/dev/null
assert_log "$CASE" "bootstrap install --version $STABLE_VERSION"

CASE="$(new_case pin-bad)"
set +e
output="$(SFORUM_INSTALL_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
  run_loader "$CASE" 2>&1)"
status=$?
set -e
[ "$status" -ne 0 ] || fail "pin mismatch: the loader succeeded"
case "$output" in
  *"does not match SFORUM_INSTALL_SHA256"*) ;;
  *) fail "pin mismatch: unexpected output: $output" ;;
esac
[ ! -f "$CASE/sforum/sforum-bootstrap.sh" ] || fail "pin mismatch still promoted the bootstrap"

# Pinned and prerelease targets both work without touching the channel default.
CASE="$(new_case pinned-version)"
run_loader "$CASE" --version "$STABLE_VERSION" >/dev/null
assert_log "$CASE" "bootstrap install --version $STABLE_VERSION"

CASE="$(new_case prerelease)"
run_loader "$CASE" --channel prerelease >/dev/null
assert_log "$CASE" "curl mock://api/prerelease"
assert_log "$CASE" "bootstrap install --version $PRERELEASE_VERSION"

# Half-finished states are refused instead of guessed at.
CASE="$(new_case repair-state)"
mkdir -p "$CASE/sforum"
printf 'APP_ENV=production\n' > "$CASE/sforum/.env.production"
printf 'version=unknown\n' > "$CASE/sforum/.deployrc"
expect_failure "repair state" "run ./sforum-latest.sh" run_loader "$CASE"

CASE="$(new_case incomplete-state)"
mkdir -p "$CASE/sforum"
printf 'version=v9.0.0\n' > "$CASE/sforum/.deployrc"
expect_failure "incomplete state" "repair the directory by hand" run_loader "$CASE"

# A piped run has no terminal; the loader must announce the default fallback
# instead of hanging on a prompt.
if command -v setsid >/dev/null 2>&1; then
  CASE="$(new_case non-interactive)"
  (
    cd "$CASE"
    PATH="$MOCK_BIN:$PATH" \
      SFORUM_INSTALL_TEST_LOG="$CASE/loader.log" \
      SFORUM_INSTALL_TEST_RELEASES="$TEMP_DIR/releases" \
      SFORUM_RELEASE_DOWNLOAD_BASE_URL=mock://releases \
      SFORUM_LATEST_RELEASE_API_URL=mock://api/stable \
      SFORUM_RELEASES_API_URL=mock://api/prerelease \
      SFORUM_SKIP_ATTESTATION=true \
      setsid -w bash "$ROOT_DIR/sforum-install.sh" < /dev/null
  ) >/dev/null 2>&1
  assert_log "$CASE" "bootstrap install --version $STABLE_VERSION --yes"
else
  printf 'install-loader_test.sh: skipping non-interactive case (setsid unavailable)\n'
fi

printf 'install-loader_test.sh: all checks passed\n'
