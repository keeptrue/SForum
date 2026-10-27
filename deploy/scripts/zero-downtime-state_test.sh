#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2034
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-zero-downtime-state-test.XXXXXX")"
LOG_FILE="$TEMP_DIR/operations.log"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'zero-downtime-state_test.sh: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  grep -Fq -- "$2" "$1" || fail "missing expected text: $2"
}

assert_not_contains() {
  if grep -Fq -- "$2" "$1"; then
    fail "found unexpected text: $2"
  fi
}

assert_before() {
  local file="$1" first="$2" second="$3" first_line second_line
  first_line="$(grep -nF -- "$first" "$file" | head -n 1 | cut -d: -f1)"
  second_line="$(grep -nF -- "$second" "$file" | head -n 1 | cut -d: -f1)"
  [ -n "$first_line" ] && [ -n "$second_line" ] && [ "$first_line" -lt "$second_line" ] || \
    fail "expected '$first' before '$second'"
}

source "$ROOT_DIR/upgrade.sh"

DEPLOY_RC="$TEMP_DIR/.deployrc"
RUNTIME_DIR="$TEMP_DIR/runtime"
COMPOSE=(fake_compose)
SCHEMA_STATE=no_pending
ONLINE_CHECK_CAPABILITY=present
MIGRATION_APPLIED=false
WAIT_RESULT=pass
SWITCH_RESULT=pass

reset_state() {
  : > "$LOG_FILE"
  printf '%s\n' \
    'lang=en' \
    'version=v3.0.0-alpha.10' \
    'mode=release' \
    'topology=blue-green' \
    'active_slot=blue' \
    'blue_version=v3.0.0-alpha.10' \
    'green_version=v3.0.0-alpha.9' > "$DEPLOY_RC"
	SCHEMA_STATE=no_pending
	ONLINE_CHECK_CAPABILITY=present
	MIGRATION_APPLIED=false
	WAIT_RESULT=pass
  SWITCH_RESULT=pass
}

fake_compose() {
	printf 'compose %s\n' "$*" >> "$LOG_FILE"
	case " $* " in
		*' sforum-migrate --check-no-pending '*) [ "$SCHEMA_STATE" = no_pending ] || [ "$MIGRATION_APPLIED" = true ] ;;
		*' sforum-migrate --check-online-safe '*) [ "$SCHEMA_STATE" = safe_pending ] ;;
		*' run --rm -T --pull never migrate '*) MIGRATION_APPLIED=true ;;
		*' ps --status running --services '*) printf '%s\n' api-green ;;
	esac
}

pull_target_images() { printf 'pull %s\n' "$1" >> "$LOG_FILE"; }
verify_target_images() { printf 'verify %s\n' "$1" >> "$LOG_FILE"; }
backup_database() { printf 'backup\n' >> "$LOG_FILE"; }
target_migrator_supports_online_safe_check() { [ "$ONLINE_CHECK_CAPABILITY" = present ]; }

: > "$LOG_FILE"
plan_schema_for_online_update v3.0.0-alpha.11
assert_contains "$LOG_FILE" 'compose run --rm -T --no-deps --pull never migrate sforum-migrate --check-no-pending'
[ "$ONLINE_MIGRATION_REQUIRED" = false ] || fail "up-to-date schema requested a migration"

reset_state
SCHEMA_STATE=safe_pending
ONLINE_CHECK_CAPABILITY=absent
if (plan_schema_for_online_update v3.0.0-alpha.11) > "$TEMP_DIR/no-capability.out" 2>&1; then
	fail "migrator without the capability label was accepted"
fi
assert_not_contains "$LOG_FILE" 'sforum-migrate --check-online-safe'

reset_state
SCHEMA_STATE=safe_pending
plan_schema_for_online_update v3.0.0-alpha.11
[ "$ONLINE_MIGRATION_REQUIRED" = true ] || fail "declared online migration was not planned"
backup_database
apply_planned_online_migrations v3.0.0-alpha.11
assert_contains "$LOG_FILE" 'compose run --rm -T --no-deps --pull never migrate sforum-migrate --check-online-safe'
assert_contains "$LOG_FILE" 'compose run --rm -T --pull never migrate'
assert_before "$LOG_FILE" 'backup' 'compose run --rm -T --pull never migrate'

wait_inside_slot() {
  printf 'wait %s\n' "$1" >> "$LOG_FILE"
  [ "$WAIT_RESULT" = pass ]
}
switch_router() {
  printf 'switch %s %s\n' "$1" "$2" >> "$LOG_FILE"
  [ "$SWITCH_RESULT" = pass ]
}
remove_legacy_worker_containers() {
  printf 'remove legacy workers\n' >> "$LOG_FILE"
}

reset_state
WAIT_RESULT=fail
if (online_update blue v3.0.0-alpha.10 v3.0.0-alpha.11) > "$TEMP_DIR/health.out" 2>&1; then
  fail "candidate health failure was accepted"
fi
assert_not_contains "$LOG_FILE" 'switch green blue'
assert_not_contains "$LOG_FILE" 'remove legacy workers'
assert_contains "$LOG_FILE" 'stop web-green api-green'

reset_state
SWITCH_RESULT=fail
if (online_update blue v3.0.0-alpha.10 v3.0.0-alpha.11) > "$TEMP_DIR/switch.out" 2>&1; then
  fail "router switch failure was accepted"
fi
assert_contains "$LOG_FILE" 'switch green blue'
assert_not_contains "$LOG_FILE" 'remove legacy workers'
assert_contains "$LOG_FILE" 'stop web-green api-green'

reset_state
SCHEMA_STATE=unsafe_pending
if (online_update blue v3.0.0-alpha.10 v3.0.0-alpha.11) > "$TEMP_DIR/schema.out" 2>&1; then
	fail "unsafe pending migrations were accepted"
fi
assert_not_contains "$LOG_FILE" 'up -d --no-build --pull never api-green web-green'
assert_not_contains "$LOG_FILE" 'switch green blue'

reset_state
SCHEMA_STATE=safe_pending
online_update blue v3.0.0-alpha.10 v3.0.0-alpha.11 > "$TEMP_DIR/online-migration.out"
assert_before "$LOG_FILE" 'backup' 'compose run --rm -T --pull never migrate'
assert_before "$LOG_FILE" 'compose run --rm -T --pull never migrate' 'up -d --no-build --pull never api-green web-green'
assert_contains "$DEPLOY_RC" 'active_slot=green'

reset_state
online_update blue v3.0.0-alpha.10 v3.0.0-alpha.11 > "$TEMP_DIR/success.out"
assert_contains "$DEPLOY_RC" 'active_slot=green'
assert_contains "$DEPLOY_RC" 'version=v3.0.0-alpha.11'
assert_contains "$DEPLOY_RC" 'green_version=v3.0.0-alpha.11'
assert_contains "$DEPLOY_RC" "install_dir=$ROOT_DIR"
assert_not_contains "$DEPLOY_RC" 'version=latest'
assert_contains "$TEMP_DIR/success.out" "Install directory: $ROOT_DIR"
assert_contains "$LOG_FILE" 'remove legacy workers'
assert_contains "$LOG_FILE" 'stop web-blue api-blue'

# Exercise the real router rollback path: the failed reload must restore the
# previous on-disk config before attempting a second reload.
source "$ROOT_DIR/upgrade.sh"
RUNTIME_DIR="$TEMP_DIR/runtime"
mkdir -p "$RUNTIME_DIR"
printf 'old-blue-config\n' > "$RUNTIME_DIR/Caddyfile"
RELOAD_COUNT_FILE="$TEMP_DIR/reload-count"
printf '0\n' > "$RELOAD_COUNT_FILE"
fake_router_compose() {
  local count
  case " $* " in
    *' caddy validate '*) return 0 ;;
    *' caddy reload '*)
      count="$(cat "$RELOAD_COUNT_FILE")"
      count=$((count + 1))
      printf '%s\n' "$count" > "$RELOAD_COUNT_FILE"
      [ "$count" -gt 1 ]
      ;;
  esac
}
COMPOSE=(fake_router_compose)
if switch_router green blue; then
  fail "failed Caddy reload was accepted"
fi
assert_contains "$RUNTIME_DIR/Caddyfile" 'old-blue-config'
[ "$(cat "$RELOAD_COUNT_FILE")" = 2 ] || fail "old router config was not reloaded"

printf 'zero-downtime-state_test.sh: all checks passed\n'
