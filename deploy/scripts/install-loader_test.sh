#!/usr/bin/env bash
# Behaviour tests for sforum-install.sh, the one-line install loader.
#
# The loader is the only shell entry that is allowed to run straight from a
# pipe, so its job is narrow and must stay verifiable: pick the install
# directory, resolve an immutable tag, download and checksum that tag's
# bootstrap, promote it, and exec it. These cases pin that contract, including
# the failure paths where a tampered or incomplete SHA256SUMS must stop before
# anything is promoted, and the interactive directory prompt that must never
# read the piped script itself.
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
      SFORUM_NONINTERACTIVE=1 \
      LANG=C LC_MESSAGES=C LANGUAGE= \
      bash "$ROOT_DIR/sforum-install.sh" "$@"
  )
}

# Interactive cases need a pseudo-terminal, because the loader refuses to read
# the piped script from stdin. Both script(1) dialects are supported; when
# neither works the interactive block is skipped instead of failing.
PTY_STYLE=""
detect_pty() {
  if script -qec true /dev/null >/dev/null 2>&1; then
    PTY_STYLE="util-linux"
    return 0
  fi
  if script -q /dev/null true >/dev/null 2>&1; then
    PTY_STYLE="bsd"
    return 0
  fi
  return 1
}

pty_exec() {
  if [ "$PTY_STYLE" = "util-linux" ]; then
    script -qec "$(printf '%q ' "$@")" /dev/null
  else
    script -q /dev/null "$@"
  fi
}

run_loader_pty() {
  local workdir="$1" input="$2" runner
  shift 2
  runner="$TEMP_DIR/pty-runner-$RANDOM.sh"
  cat > "$runner" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$workdir"
exec env \
  PATH="$MOCK_BIN:\$PATH" \
  SFORUM_INSTALL_TEST_LOG="$workdir/loader.log" \
  SFORUM_INSTALL_TEST_RELEASES="$TEMP_DIR/releases" \
  SFORUM_RELEASE_DOWNLOAD_BASE_URL=mock://releases \
  SFORUM_LATEST_RELEASE_API_URL=mock://api/stable \
  SFORUM_RELEASES_API_URL=mock://api/prerelease \
  SFORUM_SKIP_ATTESTATION=true \
  LANG=C LC_MESSAGES=C LANGUAGE= \
  bash "$ROOT_DIR/sforum-install.sh" "\$@"
EOF
  chmod 0755 "$runner"
  printf '%b' "$input" | pty_exec "$runner" "$@"
}

# The documented entry is a pipe: fd 0 is the script itself, so the prompt can
# only come from the controlling terminal. This reproduces that shape exactly.
run_loader_piped_pty() {
  local workdir="$1" input="$2" runner
  shift 2
  runner="$TEMP_DIR/pty-piped-runner-$RANDOM.sh"
  cat > "$runner" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$workdir"
cat "$ROOT_DIR/sforum-install.sh" | env \
  PATH="$MOCK_BIN:\$PATH" \
  SFORUM_INSTALL_TEST_LOG="$workdir/loader.log" \
  SFORUM_INSTALL_TEST_RELEASES="$TEMP_DIR/releases" \
  SFORUM_RELEASE_DOWNLOAD_BASE_URL=mock://releases \
  SFORUM_LATEST_RELEASE_API_URL=mock://api/stable \
  SFORUM_RELEASES_API_URL=mock://api/prerelease \
  SFORUM_SKIP_ATTESTATION=true \
  LANG=C LC_MESSAGES=C LANGUAGE= \
  bash -s -- "\$@"
EOF
  chmod 0755 "$runner"
  printf '%b' "$input" | pty_exec "$runner" "$@"
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

# Every mode reports the absolute install path before touching the network, so
# an operator always knows where the files will land.
CASE="$(new_case echo-absolute)"
run_loader "$CASE" > "$CASE/loader.out" 2>&1 || fail "absolute-path run failed"
expected="$(cd "$CASE/sforum" && pwd -P)"
grep -Fq -- "Install directory: $expected" "$CASE/loader.out" || \
  fail "the loader did not report the absolute install directory"

if detect_pty; then
  # Enter keeps the default ./sforum, and the prompt itself shows the absolute
  # path rather than a bare relative name.
  CASE="$(new_case prompt-default)"
  run_loader_pty "$CASE" '\n' > "$CASE/pty.out" 2>&1 || fail "interactive default run failed"
  [ -f "$CASE/sforum/sforum-bootstrap.sh" ] || \
    fail "interactive default did not install into ./sforum"
  grep -Fq -- "Install directory [ " "$CASE/pty.out" || \
    fail "the install directory prompt was not shown"
  assert_log "$CASE" "bootstrap install --version $STABLE_VERSION"

  # The documented entry is a pipe, so the prompt has to come from the
  # controlling terminal rather than from the script's own stdin.
  CASE="$(new_case prompt-piped)"
  run_loader_piped_pty "$CASE" '\n' > "$CASE/pty.out" 2>&1 || fail "piped loader run failed"
  grep -Fq -- "Install directory [ " "$CASE/pty.out" || \
    fail "the piped loader did not ask for the install directory"
  [ -f "$CASE/sforum/sforum-bootstrap.sh" ] || \
    fail "the piped loader did not install into ./sforum"
  assert_log "$CASE" "bootstrap install --version $STABLE_VERSION"

  # A relative answer resolves against the caller's working directory and is
  # echoed as an absolute path.
  CASE="$(new_case prompt-relative)"
  run_loader_pty "$CASE" 'myforum\n' > "$CASE/pty.out" 2>&1 || fail "relative answer failed"
  [ -f "$CASE/myforum/sforum-bootstrap.sh" ] || \
    fail "a relative answer did not create that directory"
  [ ! -e "$CASE/sforum" ] || fail "a relative answer still created the default directory"
  expected="$(cd "$CASE/myforum" && pwd -P)"
  grep -Fq -- "Install directory: $expected" "$CASE/pty.out" || \
    fail "a relative answer was not reported as an absolute path"

  # An absolute answer is used as given.
  CASE="$(new_case prompt-absolute)"
  run_loader_pty "$CASE" "$CASE/custom\n" > "$CASE/pty.out" 2>&1 || fail "absolute answer failed"
  [ -f "$CASE/custom/sforum-bootstrap.sh" ] || \
    fail "an absolute answer did not create that directory"

  # The filesystem root is refused and the question repeats.
  CASE="$(new_case prompt-root)"
  run_loader_pty "$CASE" '/\nrooted\n' > "$CASE/pty.out" 2>&1 || fail "root rejection run failed"
  grep -Fq -- "Refusing to install into the filesystem root" "$CASE/pty.out" || \
    fail "the loader did not refuse the filesystem root"
  [ -f "$CASE/rooted/sforum-bootstrap.sh" ] || \
    fail "the loader did not continue after a refused answer"

  # A non-empty directory needs an explicit confirmation; declining returns to
  # the directory question instead of aborting the run.
  CASE="$(new_case prompt-nonempty)"
  mkdir -p "$CASE/sforum"
  printf 'keep\n' > "$CASE/sforum/keep.txt"
  run_loader_pty "$CASE" '\nn\nelsewhere\n' > "$CASE/pty.out" 2>&1 || \
    fail "non-empty cancel run failed"
  grep -Fq -- "The directory is not empty" "$CASE/pty.out" || \
    fail "the loader did not warn about a non-empty directory"
  [ ! -f "$CASE/sforum/sforum-bootstrap.sh" ] || \
    fail "a declined non-empty directory was still used"
  [ -f "$CASE/elsewhere/sforum-bootstrap.sh" ] || \
    fail "the loader did not accept the replacement directory"

  CASE="$(new_case prompt-nonempty-yes)"
  mkdir -p "$CASE/sforum"
  printf 'keep\n' > "$CASE/sforum/keep.txt"
  run_loader_pty "$CASE" '\ny\n' > "$CASE/pty.out" 2>&1 || fail "non-empty confirm run failed"
  [ -f "$CASE/sforum/sforum-bootstrap.sh" ] || \
    fail "a confirmed non-empty directory was not used"
  [ -f "$CASE/sforum/keep.txt" ] || fail "the confirmation path removed an existing file"

  # Running inside an existing installation defaults to that directory instead
  # of nesting a second install under ./sforum.
  CASE="$(new_case prompt-in-place)"
  printf 'APP_ENV=production\n' > "$CASE/.env.production"
  printf 'version=v9.0.0\n' > "$CASE/.deployrc"
  run_loader_pty "$CASE" '\n' > "$CASE/pty.out" 2>&1 || fail "in-place run failed"
  grep -Fq -- "Existing installation detected: v9.0.0" "$CASE/pty.out" || \
    fail "the loader did not report the detected installation"
  assert_log "$CASE" "bootstrap upgrade --version $STABLE_VERSION"
  [ ! -e "$CASE/sforum" ] || fail "the loader nested a second install directory"

  # SFORUM_INSTALL_DIR is as explicit as --dir and never prompts.
  CASE="$(new_case prompt-env-dir)"
  SFORUM_INSTALL_DIR=fromenv run_loader_pty "$CASE" '\n' > "$CASE/pty.out" 2>&1 || \
    fail "SFORUM_INSTALL_DIR run failed"
  if grep -Fq -- "Install directory [ " "$CASE/pty.out"; then
    fail "SFORUM_INSTALL_DIR still asked for the install directory"
  fi
  [ -f "$CASE/fromenv/sforum-bootstrap.sh" ] || fail "SFORUM_INSTALL_DIR was ignored"

  # --yes and SFORUM_NONINTERACTIVE never ask, even with a terminal attached.
  CASE="$(new_case prompt-yes)"
  run_loader_pty "$CASE" '\n' --yes > "$CASE/pty.out" 2>&1 || fail "--yes run failed"
  if grep -Fq -- "Install directory [ " "$CASE/pty.out"; then
    fail "--yes still asked for the install directory"
  fi
  [ -f "$CASE/sforum/sforum-bootstrap.sh" ] || fail "--yes did not use the default directory"

  CASE="$(new_case prompt-noninteractive)"
  SFORUM_NONINTERACTIVE=1 run_loader_pty "$CASE" '\n' > "$CASE/pty.out" 2>&1 || \
    fail "non-interactive terminal run failed"
  if grep -Fq -- "Install directory [ " "$CASE/pty.out"; then
    fail "SFORUM_NONINTERACTIVE still asked for the install directory"
  fi
  assert_log "$CASE" "bootstrap install --version $STABLE_VERSION --yes"
else
  printf 'install-loader_test.sh: skipping interactive cases (no usable script(1))\n'
fi

printf 'install-loader_test.sh: all checks passed\n'
