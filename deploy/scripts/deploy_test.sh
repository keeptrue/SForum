#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-deploy-test.XXXXXX")"
TEST_ROOT="$TEMP_DIR/repo"
MOCK_BIN="$TEMP_DIR/bin"
MOCK_LOG="$TEMP_DIR/deploy.log"
MOCK_LEGACY_REMOVED_FILE="$TEMP_DIR/legacy-worker-removed"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'deploy_test.sh: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "$expected" "$file" || fail "missing expected text: $expected"
}

assert_not_contains() {
  local file="$1"
  local unexpected="$2"
  if grep -Fq -- "$unexpected" "$file"; then
    fail "found unexpected text: $unexpected"
  fi
}

line_number() {
  local file="$1"
  local text="$2"
  grep -nF -- "$text" "$file" | head -n 1 | cut -d: -f1
}

assert_before() {
  local file="$1"
  local earlier="$2"
  local later="$3"
  local earlier_line later_line
  earlier_line="$(line_number "$file" "$earlier")"
  later_line="$(line_number "$file" "$later")"
  [ -n "$earlier_line" ] || fail "ordering check is missing: $earlier"
  [ -n "$later_line" ] || fail "ordering check is missing: $later"
  [ "$earlier_line" -lt "$later_line" ] || fail "expected '$earlier' before '$later'"
}

mkdir -p "$TEST_ROOT/deploy/scripts" "$MOCK_BIN"
cp "$ROOT_DIR/deploy.sh" "$TEST_ROOT/"
cp "$ROOT_DIR/compose.yaml" "$ROOT_DIR/compose.prod.yaml" "$ROOT_DIR/compose.release.yaml" \
  "$ROOT_DIR/compose.zero-downtime.yaml" "$TEST_ROOT/"
cp "$ROOT_DIR/deploy/scripts/configure-production.sh" "$TEST_ROOT/deploy/scripts/"

"$ROOT_DIR/deploy/scripts/configure-production.sh" \
  --lang en --defaults --version v3.0.0-alpha.9 \
  --root "$TEST_ROOT" --output "$TEST_ROOT/.env.production" >/dev/null

cat > "$TEST_ROOT/deploy/scripts/backup-postgres.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ ! -e .deployrc ] || [ "${MOCK_ALLOW_EXISTING_RC:-}" = true ] || exit 96
printf 'helper backup\n' >> "$MOCK_DEPLOY_LOG"
EOF

cat > "$TEST_ROOT/deploy/scripts/wait-for-health.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ ! -e .deployrc ] || [ "${MOCK_ALLOW_EXISTING_RC:-}" = true ] || exit 95
printf 'helper health %s\n' "$1" >> "$MOCK_DEPLOY_LOG"
if [ "${MOCK_FAIL_STAGE:-}" = "health" ]; then
  exit 12
fi
EOF

cat > "$MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'docker version=%s %s\n' "${SFORUM_VERSION:-unset}" "$*" >> "$MOCK_DEPLOY_LOG"

if [ "${1:-}" = "info" ]; then
  exit 0
fi
if [ "${1:-}" = "run" ]; then
  [ "${2:-}" = "--rm" ] || exit 89
  [ "${MOCK_FAIL_STAGE:-}" != "identity" ] || exit 13
  printf 'SForum %s (0123456789ab)\n' "${SFORUM_VERSION#v}"
  exit 0
fi
if [ "${1:-}" = "ps" ]; then
  [ "${MOCK_LEGACY_WORKER:-}" != true ] || [ -e "$MOCK_LEGACY_REMOVED_FILE" ] || printf 'legacy-worker-id\n'
  exit 0
fi
if [ "${1:-}" = "rm" ]; then
  : > "$MOCK_LEGACY_REMOVED_FILE"
  exit 0
fi
[ "${1:-}" = "compose" ] || exit 90
shift

if [ "${1:-}" = "version" ]; then
  if [ "${2:-}" = "--short" ]; then
    printf '2.24.4\n'
  else
    printf 'Docker Compose version v2.24.4\n'
  fi
  exit 0
fi

blue_green_compose=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    -f)
      [ "${2:-}" != compose.zero-downtime.yaml ] || blue_green_compose=true
      shift 2
      ;;
    --env-file|--project-name)
      shift 2
      ;;
    --profile) shift 2 ;;
    *)
      break
      ;;
  esac
done

command="${1:-}"
shift || true
case "$command" in
  config)
    exit 0
    ;;
  pull)
    [ "${MOCK_FAIL_STAGE:-}" != "pull" ] || exit 10
    ;;
  up)
    if [ "$blue_green_compose" != true ] && [[ " $* " == *" api "* ]]; then
      : > "$MOCK_DEPLOY_STARTED_FILE"
    fi
    ;;
  stop)
    ;;
  exec)
    [ "${1:-}" = "-T" ] && shift
    [ "${1:-}" = "postgres" ] || exit 91
    shift
    [ "${1:-}" = "psql" ] || exit 92
    printf '%s\n' "${MOCK_DB_STATE:-f}"
    ;;
  run)
    [[ " $* " != *" --version "* ]] || exit 88
    [ "${MOCK_FAIL_STAGE:-}" != "migrate" ] || exit 11
    ;;
  ps)
    if [ "${1:-}" = "--status" ]; then
      [ ! -e .deployrc ] || [ "${MOCK_ALLOW_EXISTING_RC:-}" = true ] || exit 94
      if [ "${MOCK_BLUE_GREEN:-}" = true ] && [ "$blue_green_compose" = true ]; then
        printf '%s\n' postgres redis edge api-blue web-blue
      elif [ "${MOCK_BLUE_GREEN:-}" = true ]; then
        if [ -e "$MOCK_DEPLOY_STARTED_FILE" ]; then
          printf '%s\n' postgres redis api web
        else
          printf '%s\n' postgres redis
        fi
      elif [ "${MOCK_PORT_BUSY:-}" = "true" ]; then
        :
      elif [ "${MOCK_FAIL_STAGE:-}" = "services" ]; then
        printf '%s\n' postgres redis api
      else
        printf '%s\n' postgres redis api web
      fi
    else
      printf 'all services running\n'
    fi
    ;;
  *)
    exit 93
    ;;
esac
EOF

cat > "$MOCK_BIN/lsof" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${MOCK_PORT_BUSY:-}" = "true" ]
EOF

cat > "$MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >> "$MOCK_DEPLOY_LOG"
if [ "${MOCK_NO_RELEASE:-}" = "1" ]; then
  printf 'GitHub not found\n' >&2
  exit 22
fi
case " $* " in
  *"/releases/latest"*)
    printf '%s\n' '{"tag_name":"v3.0.0","prerelease":false,"draft":false}'
    ;;
  *)
    printf '%s\n' '[{"tag_name":"v3.0.0-beta.1","draft":false,"prerelease":true}]'
    ;;
esac
EOF

chmod +x \
  "$TEST_ROOT/deploy.sh" \
  "$TEST_ROOT/deploy/scripts/configure-production.sh" \
  "$TEST_ROOT/deploy/scripts/backup-postgres.sh" \
  "$TEST_ROOT/deploy/scripts/wait-for-health.sh" \
  "$MOCK_BIN/docker" \
  "$MOCK_BIN/curl" \
  "$MOCK_BIN/lsof"

run_deploy() {
  local name="$1"
  local database_state="$2"
  local fail_stage="$3"
  local version="${4-v3.0.0-alpha.9}"
  local channel="${5-}"
  local output_file="$TEMP_DIR/${name}.out"
  local args=(--yes --lang en --action deploy)
  if [ -n "$version" ]; then
    args+=(--version "$version")
  fi
  if [ -n "$channel" ]; then
    args+=(--channel "$channel")
  fi
	: > "$MOCK_LOG"
	rm -f "$TEMP_DIR/deploy-started"
	rm -f "$MOCK_LEGACY_REMOVED_FILE"
	rm -f "$TEST_ROOT/.deployrc"
  if (
    cd "$TEST_ROOT"
    PATH="$MOCK_BIN:$PATH" \
      MOCK_DEPLOY_LOG="$MOCK_LOG" \
		MOCK_DB_STATE="$database_state" \
		MOCK_FAIL_STAGE="$fail_stage" \
		MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started" \
		MOCK_LEGACY_REMOVED_FILE="$MOCK_LEGACY_REMOVED_FILE" \
      SFORUM_DEPLOY_STABILITY_SECONDS=0 \
      ./deploy.sh "${args[@]}"
  ) > "$output_file" 2>&1; then
    return 0
  fi
  return 1
}

for image in migrate api web; do
  assert_contains "$ROOT_DIR/compose.release.yaml" "image: \${SFORUM_REGISTRY:-ghcr.io/keeptrue}/sforum-${image}:\${SFORUM_VERSION:?Set SFORUM_VERSION to an immutable release tag}"
done

if ! run_deploy clean f ""; then
  fail "clean deployment failed"
fi
assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.9 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml pull migrate api web"
for service in api migrate; do
  assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.9 run --rm ghcr.io/keeptrue/sforum-${service}:v3.0.0-alpha.9 sforum-${service} --version"
done
assert_not_contains "$MOCK_LOG" "compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml run --rm -T --no-deps --pull never worker"
assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.9 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml up -d --no-build --pull never postgres redis api web"
assert_not_contains "$MOCK_LOG" "helper backup"
assert_before "$MOCK_LOG" "pull migrate api web" "up -d --wait postgres redis"
assert_before "$MOCK_LOG" "up -d --wait postgres redis" "exec -T postgres psql"
assert_before "$MOCK_LOG" "exec -T postgres psql" "run --rm -T --pull never migrate"
assert_before "$MOCK_LOG" "run --rm -T --pull never migrate" "up -d --no-build --pull never postgres redis api web"
assert_contains "$MOCK_LOG" "helper health http://127.0.0.1:18080/api/v1/ready"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0-alpha.9"
assert_contains "$TEMP_DIR/clean.out" "Deployment completed successfully."
assert_contains "$TEMP_DIR/clean.out" "Reverse proxy Web target: http://127.0.0.1:3000"
assert_contains "$TEMP_DIR/clean.out" "Reverse proxy API/WebSocket target: http://127.0.0.1:18080"
assert_contains "$TEMP_DIR/clean.out" "Site access URL: http://127.0.0.1:3000"
assert_contains "$TEMP_DIR/clean.out" "Admin URL: http://127.0.0.1:3000/control-panel"

# A retained legacy setting cannot disable the API-owned Worker or revive the
# removed standalone service.
cp "$TEST_ROOT/.env.production" "$TEMP_DIR/embedded-default.env"
cp "$TEMP_DIR/embedded-default.env" "$TEST_ROOT/.env.production"
printf 'EMBED_WORKER_IN_API=false\n' >> "$TEST_ROOT/.env.production"
if ! run_deploy legacy-worker-setting f ""; then
  fail "legacy Worker setting deployment failed"
fi
assert_contains "$MOCK_LOG" "pull migrate api web"
assert_contains "$MOCK_LOG" "up -d --no-build --pull never postgres redis api web"
assert_not_contains "$MOCK_LOG" "pull migrate api worker web"
assert_not_contains "$MOCK_LOG" "sforum-worker --version"
cp "$TEMP_DIR/embedded-default.env" "$TEST_ROOT/.env.production"

if ! MOCK_LEGACY_WORKER=true run_deploy legacy-container f ""; then
  fail "legacy Worker container cleanup deployment failed"
fi
assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.9 rm -f legacy-worker-id"
assert_contains "$MOCK_LOG" "label=com.docker.compose.project=sforum"
assert_before "$MOCK_LOG" "stop api web" "rm -f legacy-worker-id"
assert_before "$MOCK_LOG" "rm -f legacy-worker-id" "run --rm -T --pull never migrate"

printf 'COMPOSE_PROJECT_NAME=custom-sforum\n' >> "$TEST_ROOT/.env.production"
if ! MOCK_LEGACY_WORKER=true run_deploy custom-project-legacy-container f ""; then
  fail "custom Compose project legacy Worker cleanup deployment failed"
fi
assert_contains "$MOCK_LOG" "label=com.docker.compose.project=custom-sforum"
cp "$TEMP_DIR/embedded-default.env" "$TEST_ROOT/.env.production"

if ! run_deploy existing t ""; then
  fail "existing deployment failed"
fi
assert_contains "$MOCK_LOG" "helper backup"
assert_before "$MOCK_LOG" "up -d --wait postgres redis" "exec -T postgres psql"
assert_before "$MOCK_LOG" "exec -T postgres psql" "helper backup"
assert_before "$MOCK_LOG" "helper backup" "run --rm -T --pull never migrate"

printf '%s\n' \
  'lang=en' \
  'version=v3.0.0-alpha.8' \
  'mode=release' \
  'topology=blue-green' \
  'active_slot=blue' \
  'blue_version=v3.0.0-alpha.8' \
  'green_version=v3.0.0-alpha.7' > "$TEST_ROOT/.deployrc"
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/deploy-started"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" \
    MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_DB_STATE=t \
    MOCK_PORT_BUSY=true \
    MOCK_BLUE_GREEN=true \
    MOCK_ALLOW_EXISTING_RC=true \
    MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/blue-green.out" 2>&1; then
  cat "$TEMP_DIR/blue-green.out" >&2
  cat "$MOCK_LOG" >&2
  fail "blue/green maintenance deployment failed"
fi
assert_contains "$MOCK_LOG" "-f compose.zero-downtime.yaml --profile zero-downtime stop web-blue web-green api-blue api-green edge"
assert_before "$MOCK_LOG" "helper backup" "stop web-blue web-green api-blue api-green edge"
assert_before "$MOCK_LOG" "stop web-blue web-green api-blue api-green edge" "run --rm -T --pull never migrate"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0-alpha.9"
assert_not_contains "$TEST_ROOT/.deployrc" "topology=blue-green"

for stage in pull identity migrate health services; do
  if run_deploy "failure-${stage}" f "$stage"; then
    fail "$stage failure unexpectedly succeeded"
  fi
  assert_not_contains "$TEMP_DIR/failure-${stage}.out" "Deployment completed successfully."
  case "$stage" in
    pull|identity)
      [ ! -e "$TEST_ROOT/.deployrc" ] || fail "$stage failure wrote deployment state before database work"
      ;;
    *)
      assert_contains "$TEST_ROOT/.deployrc" "status=recovery_required"
      ;;
  esac
done

assert_not_contains "$TEMP_DIR/failure-pull.out" "Starting the managed PostgreSQL and Redis services..."
assert_not_contains "$TEMP_DIR/failure-identity.out" "Starting the managed PostgreSQL and Redis services..."

mkdir "$TEST_ROOT/.deploy.lock"
if run_deploy locked f ""; then
  fail "a concurrent deployment lock was ignored"
fi
assert_not_contains "$MOCK_LOG" "pull migrate api web"
rmdir "$TEST_ROOT/.deploy.lock"

if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" MOCK_PORT_BUSY=true \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/port-busy.out" 2>&1; then
  fail "a foreign port conflict was ignored"
fi
assert_contains "$TEMP_DIR/port-busy.out" "already used by another process"
assert_not_contains "$MOCK_LOG" "pull migrate api web"

cp "$TEST_ROOT/.env.production" "$TEMP_DIR/valid.env"
sed 's/^SESSION_HASH_SECRET=.*/SESSION_HASH_SECRET=change-me/' "$TEMP_DIR/valid.env" > "$TEST_ROOT/.env.production"
: > "$MOCK_LOG"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/invalid-env.out" 2>&1; then
  fail "an unsafe placeholder production secret was accepted"
fi
assert_contains "$TEMP_DIR/invalid-env.out" "SESSION_HASH_SECRET"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
cp "$TEMP_DIR/valid.env" "$TEST_ROOT/.env.production"

rm -f "$TEST_ROOT/.env.production"
if ! run_deploy defaults f "" ""; then
  fail "deployment with a generated default configuration failed"
fi
[ -f "$TEST_ROOT/.env.production" ] || fail "default deployment did not create .env.production"
assert_contains "$MOCK_LOG" "releases/latest"
assert_contains "$MOCK_LOG" "docker version=v3.0.0 compose --env-file .env.production"
assert_contains "$TEST_ROOT/.env.production" "SFORUM_VERSION=v3.0.0"
assert_contains "$TEST_ROOT/.env.production" "DATABASE_URL=postgres://sforum:"
assert_contains "$TEST_ROOT/.env.production" "REDIS_ADDR=redis:6379"
assert_contains "$TEST_ROOT/.env.production" "NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=/control-panel"
if grep -Fq 'change-me' "$TEST_ROOT/.env.production"; then
  fail "default deployment configuration contains placeholder secrets"
fi

# --channel prerelease resolves "latest" through the Release list (incl. prereleases).
rm -f "$TEST_ROOT/.deployrc" "$TEST_ROOT/.env.production"
if ! run_deploy prerelease-channel f "" "" prerelease; then
  fail "prerelease-channel deployment failed"
fi
assert_contains "$MOCK_LOG" "per_page=1"
assert_not_contains "$MOCK_LOG" "/releases/latest"

# An invalid channel is rejected before any network or deployment work.
rm -f "$TEST_ROOT/.deployrc" "$TEST_ROOT/.env.production"
: > "$MOCK_LOG"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
    ./deploy.sh --yes --lang en --channel beta --action deploy
) > "$TEMP_DIR/invalid-channel.out" 2>&1; then
  fail "an invalid deploy channel was accepted"
fi
assert_contains "$TEMP_DIR/invalid-channel.out" "--channel must be stable or prerelease"
assert_not_contains "$MOCK_LOG" "curl "

# No stable Release available: the stable default fails with a channel hint.
rm -f "$TEST_ROOT/.deployrc" "$TEST_ROOT/.env.production"
: > "$MOCK_LOG"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" MOCK_NO_RELEASE=1 \
    ./deploy.sh --yes --lang en --action deploy
) > "$TEMP_DIR/no-release.out" 2>&1; then
  fail "a missing stable Release was not detected"
fi
assert_contains "$TEMP_DIR/no-release.out" "--channel prerelease"
assert_not_contains "$MOCK_LOG" "compose --env-file"

# --- Current vs target version semantics on an existing installation ---

prepare_existing_install() {
  printf '%s\n' \
    'lang=en' \
    'version=v3.0.0-alpha.8' \
    'mode=release' > "$TEST_ROOT/.deployrc"
  cp "$TEMP_DIR/valid.env" "$TEST_ROOT/.env.production"
}

# Existing .deployrc + deploy + stable: the stored version is NOT the target;
# the latest stable Release is resolved and pulled.
prepare_existing_install
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/deploy-started-existing"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" \
    MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_DB_STATE=t \
    MOCK_ALLOW_EXISTING_RC=true \
    MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started-existing" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy
) > "$TEMP_DIR/existing-stable.out" 2>&1; then
  cat "$TEMP_DIR/existing-stable.out" >&2
  fail "existing installation stable-channel deploy failed"
fi
assert_contains "$MOCK_LOG" "releases/latest"
assert_contains "$MOCK_LOG" "docker version=v3.0.0 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0"
assert_not_contains "$TEST_ROOT/.deployrc" "version=v3.0.0-alpha.8"

# Existing .deployrc + deploy + prerelease channel: resolves the newest
# published Release (incl. prereleases) through the list API.
prepare_existing_install
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/deploy-started-existing"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" \
    MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_DB_STATE=t \
    MOCK_ALLOW_EXISTING_RC=true \
    MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started-existing" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --channel prerelease
) > "$TEMP_DIR/existing-prerelease.out" 2>&1; then
  cat "$TEMP_DIR/existing-prerelease.out" >&2
  fail "existing installation prerelease-channel deploy failed"
fi
assert_contains "$MOCK_LOG" "per_page=1"
assert_contains "$MOCK_LOG" "docker version=v3.0.0-beta.1 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0-beta.1"

# Existing .deployrc + explicit --version: no GitHub access at all.
prepare_existing_install
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/deploy-started-existing"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" \
    MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_DB_STATE=t \
    MOCK_ALLOW_EXISTING_RC=true \
    MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started-existing" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/existing-explicit.out" 2>&1; then
  cat "$TEMP_DIR/existing-explicit.out" >&2
  fail "existing installation explicit-version deploy failed"
fi
assert_not_contains "$MOCK_LOG" "curl "
assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.9 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0-alpha.9"

# Existing .deployrc + status: uses the current stored version, no GitHub access.
prepare_existing_install
: > "$MOCK_LOG"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
    ./deploy.sh --lang en --action status
) > "$TEMP_DIR/existing-status.out" 2>&1; then
  cat "$TEMP_DIR/existing-status.out" >&2
  fail "existing installation status action failed"
fi
assert_not_contains "$MOCK_LOG" "curl "
assert_contains "$MOCK_LOG" "docker version=v3.0.0-alpha.8 compose --env-file .env.production"
assert_contains "$TEMP_DIR/existing-status.out" "all services running"

# The interactive process may perform several actions. Resolving status must
# not leave its current version behind as the target of a later deploy choice.
prepare_existing_install
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/deploy-started-interactive"
if ! (
  cd "$TEST_ROOT"
  printf '4\n1\n0\n' | PATH="$MOCK_BIN:$PATH" \
    MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_DB_STATE=t \
    MOCK_ALLOW_EXISTING_RC=true \
    MOCK_DEPLOY_STARTED_FILE="$TEMP_DIR/deploy-started-interactive" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en
) > "$TEMP_DIR/interactive-status-deploy.out" 2>&1; then
  cat "$TEMP_DIR/interactive-status-deploy.out" >&2
  fail "interactive status followed by deploy failed"
fi
assert_contains "$MOCK_LOG" "releases/latest"
assert_contains "$MOCK_LOG" "docker version=v3.0.0 compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc" "version=v3.0.0"

printf 'deploy_test.sh: all checks passed\n'
