#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION_INPUT="${1:-}"
VERSION="${VERSION_INPUT#v}"
OUTPUT_DIR="${2:-}"

usage() {
  echo "Usage: build-deploy-asset.sh VERSION OUTPUT_DIR" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || usage

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"

# The asset must contain every file the operator workflow really needs. This
# list mirrors the runtime dependencies of deploy.sh / upgrade.sh plus the
# production examples they reference. Adding a new dependency here is
# intentional; see finalize-release-assets.sh for the archive-level checks.
SOURCE_FILES=(
  "sforum-bootstrap.sh"
  "deploy.sh"
  "upgrade.sh"
  "compose.yaml"
  "compose.prod.yaml"
  "compose.release.yaml"
  "compose.zero-downtime.yaml"
  ".env.production.example"
  "deploy/scripts/configure-production.sh"
  "deploy/scripts/backup-postgres.sh"
  "deploy/scripts/restore-postgres.sh"
  "deploy/scripts/wait-for-health.sh"
  "deploy/caddy/Caddyfile"
  "deploy/router/Caddyfile.example"
  "deploy/volumes/README.md"
)

for relative in "${SOURCE_FILES[@]}"; do
  [[ -f "$ROOT_DIR/$relative" ]] || {
    echo "Deploy asset source is missing: $relative" >&2
    exit 1
  }
done

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-deploy-asset.XXXXXX")"
cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

ASSET_ROOT="$WORK_DIR/sforum-deploy"
mkdir -p "$ASSET_ROOT/deploy/scripts" "$ASSET_ROOT/deploy/caddy" \
  "$ASSET_ROOT/deploy/router" "$ASSET_ROOT/deploy/volumes"

for relative in "${SOURCE_FILES[@]}"; do
  cp "$ROOT_DIR/$relative" "$ASSET_ROOT/$relative"
done
chmod 0755 "$ASSET_ROOT/sforum-bootstrap.sh" "$ASSET_ROOT/deploy.sh" "$ASSET_ROOT/upgrade.sh"
chmod 0755 "$ASSET_ROOT"/deploy/scripts/*.sh

printf 'version=%s\n' "$VERSION" > "$ASSET_ROOT/VERSION"
{
  printf '# SForum deploy bundle %s\n\n' "$VERSION"
  cat <<'EOF'
This archive contains everything needed to install or update a Compose-based
SForum production installation without cloning the repository:

- deploy.sh — interactive/scripted install, update, backup, restore, status,
  logs, restart, and stop actions;
- upgrade.sh — zero-downtime blue/green updates for existing installations;
- sforum-bootstrap.sh — verified online entry that refreshes the complete
  target Release toolkit before install or update;
- compose.yaml, compose.prod.yaml, compose.release.yaml,
  compose.zero-downtime.yaml — production Compose topology;
- .env.production.example — production environment template;
- deploy/scripts/ — configuration wizard, PostgreSQL backup/restore, and health
  helpers used by the two entrypoints;
- deploy/caddy/Caddyfile — host reverse proxy example;
- deploy/router/Caddyfile.example — managed blue/green ingress example;
- deploy/volumes/README.md — volume layout notes;
- VERSION — the exact release this bundle belongs to.

Quick start (resolves the latest stable release to an immutable tag):

    mkdir -p sforum
    tar -xzf sforum-deploy.tar.gz --strip-components=1 -C sforum
    cd sforum
    ./sforum-bootstrap.sh install

Update an existing installation with ./sforum-bootstrap.sh upgrade. The local
bootstrap refreshes itself and the complete target toolkit before handoff. Full
instructions:
EOF
  printf 'https://github.com/keeptrue/SForum/blob/%s/docs/zh-CN/deployment.md\n' "$VERSION_INPUT"
} > "$ASSET_ROOT/README.md"

ARCHIVE="$OUTPUT_DIR/sforum-deploy.tar.gz"
(
  cd "$WORK_DIR"
  tar -czf "$ARCHIVE" sforum-deploy
)

# Standalone fixed-name compatibility assets. New operator flows use the
# bootstrap; upgrade.sh remains available for installations from older releases.
cp "$ROOT_DIR/sforum-bootstrap.sh" "$OUTPUT_DIR/sforum-bootstrap.sh"
chmod 0755 "$OUTPUT_DIR/sforum-bootstrap.sh"
cp "$ROOT_DIR/upgrade.sh" "$OUTPUT_DIR/upgrade.sh"
chmod 0755 "$OUTPUT_DIR/upgrade.sh"

echo "Created deploy assets:"
echo "  $ARCHIVE"
echo "  $OUTPUT_DIR/sforum-bootstrap.sh"
echo "  $OUTPUT_DIR/upgrade.sh"
