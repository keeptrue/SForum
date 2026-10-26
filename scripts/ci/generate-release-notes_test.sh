#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-release-notes-test.XXXXXX")"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'generate-release-notes_test.sh: %s\n' "$1" >&2
  exit 1
}

git -C "$TEMP_DIR" init -q
git -C "$TEMP_DIR" config user.name SForum
git -C "$TEMP_DIR" config user.email release@sforum.test
printf 'base\n' > "$TEMP_DIR/history.txt"
git -C "$TEMP_DIR" add history.txt
git -C "$TEMP_DIR" commit -qm 'chore: previous release'
git -C "$TEMP_DIR" tag -a v3.0.0-alpha.10 -m 'SForum 3.0.0-alpha.10'
printf 'metrics\n' >> "$TEMP_DIR/history.txt"
git -C "$TEMP_DIR" commit -qam 'fix: restore production metrics'
printf 'upgrade\n' >> "$TEMP_DIR/history.txt"
git -C "$TEMP_DIR" commit -qam 'feat: add zero-downtime updates'
git -C "$TEMP_DIR" tag -a v3.0.0-alpha.11 -m $'SForum 3.0.0-alpha.11\n\n- Production fixes'

(
  cd "$TEMP_DIR"
  GITHUB_REPOSITORY=keeptrue/SForum \
    "$ROOT_DIR/scripts/ci/generate-release-notes.sh" v3.0.0-alpha.11 "$TEMP_DIR/notes.md"
)

grep -Fq -- '- Production fixes' "$TEMP_DIR/notes.md" || fail "annotated tag notes are missing"
grep -Fq '> Maintainable, plugin-first open-source forum framework.' "$TEMP_DIR/notes.md" || fail "product summary is missing"
grep -Fq 'docker pull ghcr.io/keeptrue/sforum-api:v3.0.0-alpha.11' "$TEMP_DIR/notes.md" || fail "Docker pull instructions are missing"
grep -Fq 'curl -fsSL https://github.com/keeptrue/SForum/releases/download/v3.0.0-alpha.11/sforum-install.sh | bash' "$TEMP_DIR/notes.md" || fail "one-line install entry is missing"
grep -Fq 'curl -fsSL https://github.com/keeptrue/SForum/releases/download/v3.0.0-alpha.11/sforum-install.sh | bash -s -- upgrade' "$TEMP_DIR/notes.md" || fail "one-line update entry is missing"
if grep -Fq 'sforum-worker' "$TEMP_DIR/notes.md"; then
  fail "release notes still advertise the retired standalone Worker image"
fi
grep -Fq './sforum-bootstrap.sh install --yes --version v3.0.0-alpha.11' "$TEMP_DIR/notes.md" || fail "Compose installation instructions are missing"
grep -Fq './sforum-bootstrap.sh upgrade --version v3.0.0-alpha.11' "$TEMP_DIR/notes.md" || fail "update instructions are missing"
grep -Fq 'docs/zh-CN/deployment.md' "$TEMP_DIR/notes.md" || fail "deployment documentation is missing"
grep -Fq 'v3.0.0-alpha.10...v3.0.0-alpha.11' "$TEMP_DIR/notes.md" || fail "full changelog comparison is missing"

# Download/verify/extract order: the archive must be saved before it can be
# verified, and extraction must come after the checksum verification. A
# regression that pipes the archive straight into tar must fail here.
if grep -Fq '| tar -xz' "$TEMP_DIR/notes.md"; then
  fail "deploy bundle is piped into tar instead of being saved for verification"
fi
if grep -Fq -- '--ignore-missing' "$TEMP_DIR/notes.md"; then
  fail "release notes rely on --ignore-missing instead of an exact checksum entry"
fi
grep -Fq 'curl -fsSLo sforum-deploy.tar.gz' "$TEMP_DIR/notes.md" || fail "deploy bundle is not saved to disk first"
grep -Fq 'curl -fsSLo SHA256SUMS' "$TEMP_DIR/notes.md" || fail "SHA256SUMS is not downloaded"
grep -Fq 'awk '\''$2 == "sforum-deploy.tar.gz" { print }'\'' SHA256SUMS > sforum-deploy.sha256' "$TEMP_DIR/notes.md" || fail "exact checksum filename-field filter is missing"
grep -Fq 'test "$(wc -l < sforum-deploy.sha256 | tr -d '\''[:space:]'\'')" = 1' "$TEMP_DIR/notes.md" || fail "checksum entry uniqueness check is missing"
grep -Fq '  set -eu' "$TEMP_DIR/notes.md" || fail "deploy bundle commands are not fail-closed"
grep -Fq 'sha256sum -c sforum-deploy.sha256' "$TEMP_DIR/notes.md" || fail "exact checksum verification is missing"
grep -Fq 'if command -v gh >/dev/null 2>&1; then' "$TEMP_DIR/notes.md" || fail "optional provenance check is not guarded by command availability"
grep -Fq 'gh attestation verify sforum-deploy.tar.gz' "$TEMP_DIR/notes.md" || fail "provenance verification is missing"
grep -Fq 'awk '\''$2 == "sforum-bootstrap.sh" { print }'\'' SHA256SUMS > sforum-bootstrap.sha256' "$TEMP_DIR/notes.md" || fail "bootstrap checksum selection is missing"
grep -Fq 'install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh' "$TEMP_DIR/notes.md" || fail "verified bootstrap is not promoted"
if grep -Eq '^\./upgrade\.sh' "$TEMP_DIR/notes.md"; then
  fail "release notes recommend a potentially stale local updater"
fi
notes_download_line="$(grep -nF 'curl -fsSLo sforum-deploy.tar.gz' "$TEMP_DIR/notes.md" | head -n 1 | cut -d: -f1)"
notes_extract_line="$(grep -nF 'tar -xzf sforum-deploy.tar.gz' "$TEMP_DIR/notes.md" | head -n 1 | cut -d: -f1)"
[ -n "$notes_download_line" ] && [ -n "$notes_extract_line" ] || fail "cannot locate the download/extract lines in release notes"
[ "$notes_download_line" -lt "$notes_extract_line" ] || fail "release notes extract the bundle before downloading it"

git -C "$TEMP_DIR" tag -a v3.0.0-alpha.12 -m 'SForum 3.0.0-alpha.12'
(
  cd "$TEMP_DIR"
  GITHUB_REPOSITORY=keeptrue/SForum \
    "$ROOT_DIR/scripts/ci/generate-release-notes.sh" v3.0.0-alpha.12 "$TEMP_DIR/automatic.md"
)
grep -Fq -- '- add zero-downtime updates' "$TEMP_DIR/automatic.md" || fail "automatic commit summary is missing"
grep -Fq -- '- restore production metrics' "$TEMP_DIR/automatic.md" || fail "automatic fix summary is missing"
if grep -Eq '^- (feat|fix):' "$TEMP_DIR/automatic.md"; then
  fail "conventional commit prefix was not removed"
fi

printf 'generate-release-notes_test.sh: all checks passed\n'
