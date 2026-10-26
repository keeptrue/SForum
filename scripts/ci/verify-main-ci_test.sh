#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-verify-main-ci-test.XXXXXX")"
COMMIT="0123456789abcdef0123456789abcdef01234567"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'verify-main-ci_test.sh: %s\n' "$1" >&2
  exit 1
}

expect_failure() {
  local expected="$1"
  shift
  local output
  if output="$("$@" 2>&1)"; then
    fail "command unexpectedly succeeded: $*"
  fi
  [[ "$output" == *"$expected"* ]] || fail "failure output did not contain '$expected': $output"
}

mkdir -p "$TEMP_DIR/bin"
cat > "$TEMP_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$MOCK_GH_ARGS"
case "$MOCK_GH_MODE" in
  success)
    printf '101\037completed\037success\037https://example.invalid/runs/101\037%s\n' "$MOCK_RELEASE_COMMIT"
    ;;
  failure)
    printf '102\037completed\037failure\037https://example.invalid/runs/102\037%s\n' "$MOCK_RELEASE_COMMIT"
    ;;
  wrong-sha)
    printf '103\037completed\037success\037https://example.invalid/runs/103\037ffffffffffffffffffffffffffffffffffffffff\n'
    ;;
  wait-success)
    count=0
    [[ -f "$MOCK_GH_COUNT" ]] && read -r count < "$MOCK_GH_COUNT"
    count=$((count + 1))
    printf '%s\n' "$count" > "$MOCK_GH_COUNT"
    if ((count == 1)); then
      printf '104\037in_progress\037\037https://example.invalid/runs/104\037%s\n' "$MOCK_RELEASE_COMMIT"
    else
      printf '104\037completed\037success\037https://example.invalid/runs/104\037%s\n' "$MOCK_RELEASE_COMMIT"
    fi
    ;;
  missing)
    ;;
  *)
    exit 2
    ;;
esac
EOF
chmod +x "$TEMP_DIR/bin/gh"

export PATH="$TEMP_DIR/bin:$PATH"
export GITHUB_REPOSITORY="keeptrue/SForum"
export MOCK_GH_ARGS="$TEMP_DIR/gh-args"
export MOCK_GH_COUNT="$TEMP_DIR/gh-count"
export MOCK_RELEASE_COMMIT="$COMMIT"
export SFORUM_CI_WAIT_SECONDS=0
export SFORUM_CI_MAX_MISSING_ATTEMPTS=2
export SFORUM_CI_MAX_ATTEMPTS=3

export MOCK_GH_MODE=success
SUCCESS_OUTPUT="$("$ROOT_DIR/scripts/ci/verify-main-ci.sh" "$COMMIT")"
[[ "$SUCCESS_OUTPUT" == *"Verified successful main CI run 101"* ]] || fail "successful CI was not accepted"
ARGS="$(cat "$MOCK_GH_ARGS")"
[[ "$ARGS" == *"--workflow ci.yml"* ]] || fail "workflow filter is missing"
[[ "$ARGS" == *"--branch main"* ]] || fail "main branch filter is missing"
[[ "$ARGS" == *"--event push"* ]] || fail "push event filter is missing"
[[ "$ARGS" == *"--commit $COMMIT"* ]] || fail "exact commit filter is missing"

export MOCK_GH_MODE=wait-success
WAIT_OUTPUT="$("$ROOT_DIR/scripts/ci/verify-main-ci.sh" "$COMMIT")"
[[ "$WAIT_OUTPUT" == *"is in_progress; waiting"* ]] || fail "in-progress CI was not waited for"
[[ "$WAIT_OUTPUT" == *"Verified successful main CI run 104"* ]] || fail "completed CI was not accepted after waiting"

export MOCK_GH_MODE=failure
expect_failure "completed with failure" "$ROOT_DIR/scripts/ci/verify-main-ci.sh" "$COMMIT"

export MOCK_GH_MODE=wrong-sha
expect_failure "unexpected commit" "$ROOT_DIR/scripts/ci/verify-main-ci.sh" "$COMMIT"

export MOCK_GH_MODE=missing
expect_failure "No main CI push run appeared" "$ROOT_DIR/scripts/ci/verify-main-ci.sh" "$COMMIT"

expect_failure "full lowercase Git SHA" "$ROOT_DIR/scripts/ci/verify-main-ci.sh" short-sha

printf 'verify-main-ci_test.sh: all checks passed\n'
