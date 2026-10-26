#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SFORUM_VERSION="${1:-}"
EXPECTED_VERSION="${2:-}"
EXPECTED_COMMIT="${3:-}"

if [[ -z "$SFORUM_VERSION" || -z "$EXPECTED_VERSION" || -z "$EXPECTED_COMMIT" ]]; then
  echo "Usage: scripts/ci/release-smoke.sh IMAGE_TAG EXPECTED_VERSION EXPECTED_COMMIT" >&2
  exit 2
fi

export SFORUM_VERSION
export SFORUM_REGISTRY="${SFORUM_REGISTRY:-ghcr.io/keeptrue}"
export APP_ENV=production
export APP_URL=http://127.0.0.1:3000
export POSTGRES_PASSWORD=sforum-release-smoke-postgres
export DATABASE_URL='postgres://sforum:sforum-release-smoke-postgres@postgres:5432/sforum?sslmode=disable'
export REDIS_PASSWORD=sforum-release-smoke-redis
export SESSION_HASH_SECRET=sforum-release-smoke-session-secret-32-bytes
export IDENTITY_SUBJECT_HMAC_SECRET=sforum-release-smoke-identity-secret-32-bytes
export APP_OPTION_ENC_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
export ALTCHA_SECRET=sforum-release-smoke-altcha-secret-32-bytes
export CSRF_TRUSTED_ORIGINS=http://127.0.0.1:3000

project="sforum-release-smoke-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
compose=(
  docker compose
  --project-name "$project"
  -f "$ROOT_DIR/compose.yaml"
  -f "$ROOT_DIR/compose.prod.yaml"
  -f "$ROOT_DIR/compose.release.yaml"
)

cleanup() {
  local status=$?
  if (( status != 0 )); then
    "${compose[@]}" ps || true
    "${compose[@]}" logs --no-color --tail=200 || true
  fi
  "${compose[@]}" down --volumes --remove-orphans || true
  exit "$status"
}
trap cleanup EXIT

"${compose[@]}" pull migrate api web

expected_summary="SForum $EXPECTED_VERSION (${EXPECTED_COMMIT:0:12})"
for service in api migrate; do
  binary="sforum-$service"
  image="${SFORUM_REGISTRY}/sforum-${service}:${SFORUM_VERSION}"
  actual_summary="$(docker run --rm "$image" "$binary" --version)"
  if [[ "$actual_summary" != "$expected_summary" ]]; then
    echo "$service build identity mismatch: got '$actual_summary', want '$expected_summary'" >&2
    exit 1
  fi
done

"${compose[@]}" up -d postgres redis
"${compose[@]}" run --rm -T migrate
"${compose[@]}" up -d api web

"$ROOT_DIR/deploy/scripts/wait-for-health.sh" http://127.0.0.1:18080/api/v1/ready 120
"$ROOT_DIR/deploy/scripts/wait-for-health.sh" http://127.0.0.1:3000/health 120

running_services="$("${compose[@]}" ps --status running --services)"
for service in postgres redis api web; do
  if ! grep -qx "$service" <<<"$running_services"; then
    echo "Release smoke service is not running: $service" >&2
    exit 1
  fi
done

deadline=$((SECONDS + 30))
while [ "$SECONDS" -lt "$deadline" ]; do
  if "${compose[@]}" exec -T redis redis-cli -a "$REDIS_PASSWORD" EXISTS sforum:worker:heartbeat 2>/dev/null | grep -qx 1; then
    break
  fi
  sleep 1
done
if ! "${compose[@]}" exec -T redis redis-cli -a "$REDIS_PASSWORD" EXISTS sforum:worker:heartbeat 2>/dev/null | grep -qx 1; then
  echo "Release smoke did not observe the embedded Worker heartbeat." >&2
  exit 1
fi

echo "Published SForum images passed release smoke: $SFORUM_VERSION"
