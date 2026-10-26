#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-release-assets-test.XXXXXX")"
VERSION="3.2.1-beta.2"
COMMIT="0123456789abcdef0123456789abcdef01234567"
BUILD_TIME="2026-07-30T10:20:30Z"
IMAGE_TAG="sha-$COMMIT"
OUTPUT_DIR="$TEMP_DIR/dist"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'release_assets_test.sh: %s\n' "$1" >&2
  exit 1
}

expect_failure() {
  local expected="$1"
  shift
  local output=""
  if output="$("$@" 2>&1)"; then
    fail "command unexpectedly succeeded: $*"
  fi
  [[ "$output" == *"$expected"* ]] || fail "failure output did not contain '$expected': $output"
}

mkdir -p "$TEMP_DIR/bin" "$OUTPUT_DIR"
cat > "$TEMP_DIR/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  build)
    output=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then
        output="$2"
        break
      fi
      shift
    done
    [[ -n "$output" ]]
    mkdir -p "$(dirname "$output")"
    printf 'GOOS=%s\nGOARCH=%s\n' "$GOOS" "$GOARCH" > "$output"
    chmod +x "$output"
    ;;
  version)
    [[ "${2:-}" == "-m" && -f "${3:-}" ]]
    cat "$3"
    ;;
  *)
    exit 2
    ;;
esac
EOF
cat > "$TEMP_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

command="$1"
shift
case "$command" in
  pull)
    ;;
  create)
    platform=""
    image=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --platform)
          platform="$2"
          shift 2
          ;;
        *)
          image="$1"
          shift
          ;;
      esac
    done
    service="${image##*/}"
    service="${service%%:*}"
    printf 'cid-%s-%s\n' "$service" "${platform#linux/}"
    ;;
  cp)
    source="$1"
    destination="$2"
    container="${source%%:*}"
    source_path="${source#*:}"
    arch="${container##*-}"
    if [[ "$source_path" == "/app/extensions/builtin" ]]; then
      mkdir -p "$destination/builtin/plugins/mock/backend"
      printf 'GOOS=linux\nGOARCH=%s\n' "$arch" > "$destination/builtin/plugins/mock/backend/plugin"
      chmod +x "$destination/builtin/plugins/mock/backend/plugin"
    else
      mkdir -p "$(dirname "$destination")"
      printf 'GOOS=linux\nGOARCH=%s\n' "$arch" > "$destination"
      chmod +x "$destination"
    fi
    ;;
  rm)
    ;;
  *)
    exit 2
    ;;
esac
EOF
chmod +x "$TEMP_DIR/bin/go" "$TEMP_DIR/bin/docker"
export PATH="$TEMP_DIR/bin:$PATH"

for target in \
  "linux amd64" \
  "linux arm64" \
  "darwin amd64" \
  "darwin arm64"; do
  read -r target_os target_arch <<< "$target"
  "$ROOT_DIR/scripts/ci/build-release-assets.sh" \
    "$VERSION" "$COMMIT" "$BUILD_TIME" "$IMAGE_TAG" "$target_os" "$target_arch" "$OUTPUT_DIR" >/dev/null
done

"$ROOT_DIR/scripts/ci/build-deploy-asset.sh" "$VERSION" "$OUTPUT_DIR" >/dev/null

# Regression: GitHub Actions artifacts do not preserve Unix file modes; the
# upload-artifact -> download-artifact cross-job transfer delivers the
# standalone shell assets with 0644, which used to break the finalizer's -x check.
[[ -x "$OUTPUT_DIR/sforum-bootstrap.sh" ]] || fail "built bootstrap is not executable"
[[ -x "$OUTPUT_DIR/upgrade.sh" ]] || fail "built upgrade.sh is not executable"
[[ -x "$OUTPUT_DIR/sforum-install.sh" ]] || fail "built install loader is not executable"
chmod 0644 "$OUTPUT_DIR/sforum-bootstrap.sh" "$OUTPUT_DIR/upgrade.sh" "$OUTPUT_DIR/sforum-install.sh"
[[ ! -x "$OUTPUT_DIR/sforum-bootstrap.sh" ]] || fail "failed to strip the bootstrap execute bit"
[[ ! -x "$OUTPUT_DIR/upgrade.sh" ]] || fail "failed to strip the execute bit"
[[ ! -x "$OUTPUT_DIR/sforum-install.sh" ]] || fail "failed to strip the install loader execute bit"

"$ROOT_DIR/scripts/ci/finalize-release-assets.sh" "$VERSION" "$OUTPUT_DIR" >/dev/null

[[ -x "$OUTPUT_DIR/sforum-bootstrap.sh" ]] || fail "finalizer did not restore the bootstrap execute bit"
[[ -x "$OUTPUT_DIR/upgrade.sh" ]] || fail "finalizer did not restore the upgrade.sh execute bit"
[[ -x "$OUTPUT_DIR/sforum-install.sh" ]] || fail "finalizer did not restore the install loader execute bit"

[[ "$(find "$OUTPUT_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq 11 ]] || fail "unexpected release asset count"
[[ "$(wc -l < "$OUTPUT_DIR/SHA256SUMS" | tr -d ' ')" -eq 10 ]] || fail "unexpected checksum count"
grep -q "sforum-server_${VERSION}_linux_amd64.tar.gz" "$OUTPUT_DIR/SHA256SUMS" || fail "server checksum is missing"
grep -q "sforum-cli_${VERSION}_darwin_arm64.tar.gz" "$OUTPUT_DIR/SHA256SUMS" || fail "macOS CLI checksum is missing"
grep -q "sforum-deploy.tar.gz" "$OUTPUT_DIR/SHA256SUMS" || fail "deploy bundle checksum is missing"
grep -q "^[0-9a-f]\{64\}  sforum-bootstrap.sh$" "$OUTPUT_DIR/SHA256SUMS" || fail "bootstrap checksum is missing"
grep -q "^[0-9a-f]\{64\}  upgrade.sh$" "$OUTPUT_DIR/SHA256SUMS" || fail "updater checksum is missing"
grep -q "^[0-9a-f]\{64\}  sforum-install.sh$" "$OUTPUT_DIR/SHA256SUMS" || fail "install loader checksum is missing"
installer_checksum_lines="$(grep -c 'sforum-install.sh' "$OUTPUT_DIR/SHA256SUMS" || true)"
[[ "$installer_checksum_lines" -eq 1 ]] || fail "SHA256SUMS must contain exactly one sforum-install.sh entry"
upgrade_checksum_lines="$(grep -c 'upgrade.sh' "$OUTPUT_DIR/SHA256SUMS" || true)"
[[ "$upgrade_checksum_lines" -eq 1 ]] || fail "SHA256SUMS must contain exactly one upgrade.sh entry"

deploy_listing="$(tar -tzf "$OUTPUT_DIR/sforum-deploy.tar.gz")"
for entry in \
  "sforum-deploy/sforum-bootstrap.sh" \
  "sforum-deploy/deploy.sh" \
  "sforum-deploy/upgrade.sh" \
  "sforum-deploy/compose.yaml" \
  "sforum-deploy/compose.prod.yaml" \
  "sforum-deploy/compose.release.yaml" \
  "sforum-deploy/compose.zero-downtime.yaml" \
  "sforum-deploy/.env.production.example" \
  "sforum-deploy/VERSION" \
  "sforum-deploy/deploy/scripts/configure-production.sh" \
  "sforum-deploy/deploy/scripts/backup-postgres.sh" \
  "sforum-deploy/deploy/scripts/restore-postgres.sh" \
  "sforum-deploy/deploy/scripts/wait-for-health.sh" \
  "sforum-deploy/deploy/caddy/Caddyfile" \
  "sforum-deploy/deploy/router/Caddyfile.example"; do
  grep -qx "$entry" <<< "$deploy_listing" || fail "deploy archive is missing $entry"
done
grep -qx "sforum-deploy/VERSION" <<< "$deploy_listing" || fail "deploy archive is missing VERSION"
deploy_version="$(tar -xOzf "$OUTPUT_DIR/sforum-deploy.tar.gz" sforum-deploy/VERSION)"
[[ "$deploy_version" == "version=$VERSION" ]] || fail "deploy archive carries the wrong VERSION"

deploy_verbose="$(tar -tvzf "$OUTPUT_DIR/sforum-deploy.tar.gz")"
for entry in \
  "sforum-deploy/sforum-bootstrap.sh" \
  "sforum-deploy/deploy.sh" \
  "sforum-deploy/upgrade.sh" \
  "sforum-deploy/deploy/scripts/configure-production.sh" \
  "sforum-deploy/deploy/scripts/backup-postgres.sh" \
  "sforum-deploy/deploy/scripts/restore-postgres.sh" \
  "sforum-deploy/deploy/scripts/wait-for-health.sh"; do
  mode="$(awk -v name="$entry" '$NF == name { print $1; exit }' <<< "$deploy_verbose")"
  [[ "$mode" == "-rwxr-xr-x" ]] || fail "deploy archive entry does not have mode 0755: $entry ($mode)"
done

expect_failure "Usage:" "$ROOT_DIR/scripts/ci/build-release-assets.sh" \
  invalid "$COMMIT" "$BUILD_TIME" "$IMAGE_TAG" linux amd64 "$OUTPUT_DIR"

expect_failure "Usage:" "$ROOT_DIR/scripts/ci/build-deploy-asset.sh" invalid

printf 'release_assets_test.sh: all checks passed\n'
