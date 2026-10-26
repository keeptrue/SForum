#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-public-images-test.XXXXXX")"
MOCK_LOG="$TEMP_DIR/docker.log"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'verify-public-images_test.sh: %s\n' "$1" >&2
  exit 1
}

mkdir -p "$TEMP_DIR/bin"
cat > "$TEMP_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "${1:-}" == "pull" ]] || exit 90
[[ -n "${DOCKER_CONFIG:-}" && -d "$DOCKER_CONFIG" ]] || exit 91
[[ ! -e "$DOCKER_CONFIG/config.json" ]] || exit 92
[[ -z "${DOCKER_AUTH_CONFIG:-}" ]] || exit 93

reference="${2:-}"
printf '%s\n' "$reference" >> "$MOCK_DOCKER_LOG"
if [[ -n "${MOCK_FAIL_IMAGE:-}" && "$reference" == *"/$MOCK_FAIL_IMAGE:"* ]]; then
  exit 7
fi
EOF
chmod +x "$TEMP_DIR/bin/docker"

export PATH="$TEMP_DIR/bin:$PATH"
export MOCK_DOCKER_LOG="$MOCK_LOG"
export DOCKER_AUTH_CONFIG='{"auths":{"ghcr.io":{"auth":"must-not-be-used"}}}'

"$ROOT_DIR/scripts/ci/verify-public-images.sh" ghcr.io/keeptrue v3.0.0-beta.1 >/dev/null
expected="$(printf '%s\n' \
  'ghcr.io/keeptrue/sforum-api:v3.0.0-beta.1' \
  'ghcr.io/keeptrue/sforum-migrate:v3.0.0-beta.1' \
  'ghcr.io/keeptrue/sforum-web:v3.0.0-beta.1')"
[[ "$(cat "$MOCK_LOG")" == "$expected" ]] || fail "unexpected anonymous pull set"

: > "$MOCK_LOG"
if MOCK_FAIL_IMAGE=sforum-migrate "$ROOT_DIR/scripts/ci/verify-public-images.sh" ghcr.io/keeptrue v3.0.0 >/dev/null 2>&1; then
  fail "one failed image pull did not fail the gate"
fi
[[ "$(wc -l < "$MOCK_LOG" | tr -d ' ')" -eq 2 ]] || fail "gate continued after a failed pull"

if "$ROOT_DIR/scripts/ci/verify-public-images.sh" invalid.example/owner v3.0.0 >/dev/null 2>&1; then
  fail "non-GHCR registry was accepted"
fi
if "$ROOT_DIR/scripts/ci/verify-public-images.sh" ghcr.io/keeptrue latest >/dev/null 2>&1; then
  fail "mutable release tag was accepted"
fi

WORKFLOW_FILE="$ROOT_DIR/.github/workflows/release.yml"
ruby -ryaml - "$WORKFLOW_FILE" "$ROOT_DIR/scripts/ci/verify-public-images.sh" <<'RUBY'
workflow = YAML.load_file(ARGV.fetch(0))
jobs = workflow.fetch('jobs')
gate = jobs.fetch('verify-public-images')
release = jobs.fetch('github-release')

unless File.executable?(ARGV.fetch(1))
  abort 'anonymous pull script is missing or not executable'
end

permissions = gate.fetch('permissions')
unless permissions == { 'contents' => 'read' }
  abort 'anonymous pull job must have only contents: read permission'
end

steps = gate.fetch('steps')
uses = steps.map { |step| step['uses'] }.compact
commands = steps.map { |step| step['run'] }.compact
if uses.any? { |action| action.include?('docker/login-action') } ||
   commands.any? { |command| command.match?(/\bdocker\s+login\b/) }
  abort 'anonymous pull job must not log in to GHCR'
end
unless commands.any? { |command| command.include?('./scripts/ci/verify-public-images.sh') }
  abort 'anonymous pull job does not execute verify-public-images.sh'
end
unless Array(gate.fetch('needs')).include?('promote')
  abort 'anonymous pull job must run after image promotion'
end
unless Array(release.fetch('needs')).include?('verify-public-images')
  abort 'GitHub Release creation must depend on anonymous image pulls'
end
RUBY

printf 'verify-public-images_test.sh: all checks passed\n'
