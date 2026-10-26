#!/usr/bin/env bash
# Guard the optional sforum-latest.sh wrapper: its published SHA-256 must match
# the file, the script must parse, and the documented one-command entry must
# keep pointing at the checksum.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  printf 'validate-sforum-latest.sh: %s\n' "$1" >&2
  exit 1
}

[ -f sforum-latest.sh ] || fail "sforum-latest.sh is missing"
[ -f sforum-latest.sh.sha256 ] || fail "sforum-latest.sh.sha256 is missing"
bash -n sforum-latest.sh || fail "sforum-latest.sh has invalid shell syntax"
grep -q 'sforum-deploy\.tar\.gz' sforum-latest.sh || \
  fail "sforum-latest.sh must sync the complete deploy bundle, not only the bootstrap"
grep -q 'managed_files' sforum-latest.sh || \
  fail "sforum-latest.sh must read the bundle's managed-file list"

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c sforum-latest.sh.sha256 >/dev/null || \
    fail "sforum-latest.sh.sha256 is stale; run: sha256sum sforum-latest.sh > sforum-latest.sh.sha256"
else
  shasum -a 256 -c sforum-latest.sh.sha256 >/dev/null || \
    fail "sforum-latest.sh.sha256 is stale; run: shasum -a 256 sforum-latest.sh > sforum-latest.sh.sha256"
fi

for file in docs/zh-CN/deployment/one-click.md docs/en-US/deployment/one-click.md; do
  grep -q 'sforum-latest\.sh\.sha256' "$file" || \
    fail "$file must document the checksum-verified sforum-latest.sh entry"
done

printf 'validate-sforum-latest.sh: all checks passed\n'
