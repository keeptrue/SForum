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

# 认领副本（.deploy.lock.stale.<pid>）不许留在仓库根：正常回收要删干净，
# 只有「还原目标被占」这一种情况才允许保留一份给运维手动清。
claim_leftovers() {
  find "$TEST_ROOT" -maxdepth 1 -name '.deploy.lock.stale.*' -print 2>/dev/null || true
}

assert_no_claim_leftovers() {
  local leftovers
  leftovers="$(claim_leftovers)"
  [ -z "$leftovers" ] || fail "claim copy left behind: $leftovers"
}

mkdir -p "$TEST_ROOT/deploy/scripts" "$MOCK_BIN"
cp "$ROOT_DIR/deploy.sh" "$TEST_ROOT/"
cp "$ROOT_DIR/compose.yaml" "$ROOT_DIR/compose.prod.yaml" "$ROOT_DIR/compose.release.yaml" \
  "$ROOT_DIR/compose.zero-downtime.yaml" "$TEST_ROOT/"
cp "$ROOT_DIR/deploy/scripts/configure-production.sh" "$TEST_ROOT/deploy/scripts/"
cp "$ROOT_DIR/deploy/scripts/deploy-lock.sh" "$TEST_ROOT/deploy/scripts/"

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
# 恢复态：wait-for-health.sh 的三态里 exit 3 = Host 只放行 /health 与 /ready。
if [ "${MOCK_FAIL_STAGE:-}" = "recovery" ]; then
  exit 3
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
  logs)
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

# 「判定后 pid 换人」场景专用：deploy.sh 会两次查询那个死 pid（先算持有者摘要，再判定
# 残留），第二次才模拟另一个进程抢先回收并重建了锁（写进 MOCK_PID_SWAP_LOCK 的
# pid/owner）；其余调用原样交给真 ps —— 靠自己的目录名把 mock 目录从 PATH 里摘掉，
# 避免自调用。
cat > "$MOCK_BIN/ps" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ -n "${MOCK_PID_SWAP_FROM:-}" ] && [[ " $* " == *" -p ${MOCK_PID_SWAP_FROM} "* ]]; then
  if [ -e "${MOCK_PID_SWAP_MARKER:?}" ]; then
    printf '%s\n' "$$" > "$MOCK_PID_SWAP_LOCK/pid"
    printf 'pid=%s\naction=deploy\nstarted_at=2026-01-01T00:00:00Z\nproc_start=mock\n' "$$" \
      > "$MOCK_PID_SWAP_LOCK/owner"
  else
    : > "$MOCK_PID_SWAP_MARKER"
  fi
  exit 1
fi
self_dir="$(cd "$(dirname "$0")" && pwd)"
PATH="${PATH//$self_dir:/}" exec ps "$@"
EOF

# 「认领副本被读到的瞬间，第三方抢建了 .deploy.lock」场景专用：只对带认领副本路径的
# 调用生效（deploy.sh 只在比对副本 pid 时读那个路径），其余调用原样交给真 sed。
cat > "$MOCK_BIN/sed" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ -n "${MOCK_CLAIM_OCCUPY:-}" ]; then
  for arg in "$@"; do
    case "$arg" in
      *".deploy.lock.stale."*) mkdir -p "$MOCK_CLAIM_OCCUPY" ;;
    esac
  done
fi
self_dir="$(cd "$(dirname "$0")" && pwd)"
PATH="${PATH//$self_dir:/}" exec sed "$@"
EOF

chmod +x \
  "$TEST_ROOT/deploy.sh" \
  "$TEST_ROOT/deploy/scripts/configure-production.sh" \
  "$TEST_ROOT/deploy/scripts/backup-postgres.sh" \
  "$TEST_ROOT/deploy/scripts/wait-for-health.sh" \
  "$MOCK_BIN/docker" \
  "$MOCK_BIN/curl" \
  "$MOCK_BIN/lsof" \
  "$MOCK_BIN/ps" \
  "$MOCK_BIN/sed"

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
EXPECTED_ROOT="$(cd "$TEST_ROOT" && pwd -P)"
assert_contains "$TEMP_DIR/clean.out" "Install directory: $EXPECTED_ROOT"
assert_contains "$TEMP_DIR/clean.out" "Configuration file: $EXPECTED_ROOT/.env.production"
assert_contains "$TEMP_DIR/clean.out" "Backup directory: $EXPECTED_ROOT/backups"
assert_contains "$TEST_ROOT/.deployrc" "install_dir=$EXPECTED_ROOT"

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

# 探针三态必须落成两个不同的 reason：两者都仍是 status=recovery_required，但
# 恢复态（exit 3）要人去跑 recovery recheck，超时要看日志。折成一个 reason
# 等于让运维在 120 秒后猜是哪一种。
if run_deploy "failure-recovery" f recovery; then
  fail "a recovery-only deployment unexpectedly succeeded"
fi
assert_contains "$TEST_ROOT/.deployrc" "status=recovery_required"
assert_contains "$TEST_ROOT/.deployrc" "reason=recovery_only"
assert_contains "$TEMP_DIR/failure-recovery.out" "recovery recheck"

if run_deploy "failure-timeout" f health; then
  fail "a health timeout deployment unexpectedly succeeded"
fi
assert_contains "$TEST_ROOT/.deployrc" "reason=health_failed"
assert_not_contains "$TEST_ROOT/.deployrc" "reason=recovery_only"

# 无元数据的空锁目录（旧版本 mkdir-only 或崩溃窗口）在 1 小时宽限内必须拒绝：
# 这是「持有者可能还活着就不许抢」的那一侧保护。
mkdir "$TEST_ROOT/.deploy.lock"
if run_deploy locked f ""; then
  fail "a concurrent deployment lock was ignored"
fi
assert_contains "$TEMP_DIR/locked.out" "state=unknown"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
rmdir "$TEST_ROOT/.deploy.lock"

# 同一分支的另一侧：锁龄超过 1 小时的空锁目录必须回收，不能永久拒绝。
mkdir "$TEST_ROOT/.deploy.lock"
touch -t "$(date -d '2 hours ago' +%Y%m%d%H%M 2>/dev/null || date -v-2H +%Y%m%d%H%M)" \
  "$TEST_ROOT/.deploy.lock"
rm -f "$TEST_ROOT/.deployrc.lock.log"
if ! run_deploy orphan-lock-old f ""; then
  cat "$TEMP_DIR/orphan-lock-old.out" >&2
  fail "a lock without owner metadata older than the grace period was not reclaimed"
fi
assert_contains "$TEMP_DIR/orphan-lock-old.out" "state=unknown"
assert_contains "$MOCK_LOG" "pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc.lock.log" "reason=no_owner"
[ ! -e "$TEST_ROOT/.deploy.lock" ] || fail "the aged orphan lock was left behind"
assert_no_claim_leftovers

# 认领到的是别人刚重建的活锁（判定后 pid 换人）：认领副本必须原样还回 .deploy.lock，
# 部署 abort、docker 调用 0 次、不留 .deploy.lock.stale.* 残骸。
rm -rf "$TEST_ROOT/.deploy.lock"
mkdir "$TEST_ROOT/.deploy.lock"
printf '999998\n' > "$TEST_ROOT/.deploy.lock/pid"
printf 'pid=999998\naction=deploy\nstarted_at=2026-01-01T00:00:00Z\nproc_start=Thu Jan  1 00:00:00 2026\n' \
  > "$TEST_ROOT/.deploy.lock/owner"
: > "$MOCK_LOG"
rm -f "$TEMP_DIR/ps-swap.marker"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_PID_SWAP_FROM=999998 MOCK_PID_SWAP_LOCK="$TEST_ROOT/.deploy.lock" \
    MOCK_PID_SWAP_MARKER="$TEMP_DIR/ps-swap.marker" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/pid-swap.out" 2>&1; then
  fail "a lock whose pid changed after the stale check was reclaimed anyway"
fi
assert_not_contains "$TEMP_DIR/pid-swap.out" "Reclaimed a stale deployment lock"
assert_contains "$TEMP_DIR/pid-swap.out" "--force-unlock"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
assert_contains "$TEST_ROOT/.deploy.lock/owner" "proc_start=mock"
assert_no_claim_leftovers
rm -rf "$TEST_ROOT/.deploy.lock"

# 认领错了要还原时，.deploy.lock 在认领期间又被别人建出来：不许 mv 嵌套、不许覆盖，
# 保留认领副本并 die（提示副本路径），部署 abort。
rm -rf "$TEST_ROOT/.deploy.lock" "$TEMP_DIR/ps-swap.marker"
mkdir "$TEST_ROOT/.deploy.lock"
printf '999997\n' > "$TEST_ROOT/.deploy.lock/pid"
printf 'pid=999997\naction=deploy\nstarted_at=2026-01-01T00:00:00Z\nproc_start=Thu Jan  1 00:00:00 2026\n' \
  > "$TEST_ROOT/.deploy.lock/owner"
: > "$MOCK_LOG"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
    MOCK_PID_SWAP_FROM=999997 MOCK_PID_SWAP_LOCK="$TEST_ROOT/.deploy.lock" \
    MOCK_PID_SWAP_MARKER="$TEMP_DIR/ps-swap.marker" \
    MOCK_CLAIM_OCCUPY="$TEST_ROOT/.deploy.lock" \
    SFORUM_DEPLOY_STABILITY_SECONDS=0 \
    ./deploy.sh --yes --lang en --action deploy --version v3.0.0-alpha.9
) > "$TEMP_DIR/claim-occupied.out" 2>&1; then
  fail "an unrecoverable claim must abort the deployment"
fi
assert_contains "$TEMP_DIR/claim-occupied.out" "recovered copy left at"
assert_contains "$TEMP_DIR/claim-occupied.out" "--force-unlock"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
[ -z "$(find "$TEST_ROOT/.deploy.lock" -mindepth 1 -name '.deploy.lock.stale.*' -print 2>/dev/null)" ] || \
  fail "the claim copy was nested into .deploy.lock"
[ "$(claim_leftovers | wc -l)" -eq 1 ] || fail "the claim copy must be kept for manual cleanup"
rm -rf "$TEST_ROOT"/.deploy.lock.stale.* "$TEST_ROOT/.deploy.lock"
assert_no_claim_leftovers

# OPS2-04：锁记录持有者 pid / 操作名 / 启动时间。pid 已消失 = 残留锁，自动回收并
# 打印一行 warn，而不是把「锁存在」当成永久拒绝。
rm -f "$TEST_ROOT/.deployrc.lock.log"
mkdir "$TEST_ROOT/.deploy.lock"
printf '999999\n' > "$TEST_ROOT/.deploy.lock/pid"
printf 'pid=999999\naction=backup\nstarted_at=2026-01-01T00:00:00Z\nproc_start=Thu Jan  1 00:00:00 2026\n' \
  > "$TEST_ROOT/.deploy.lock/owner"
if ! run_deploy stale-lock f ""; then
  cat "$TEMP_DIR/stale-lock.out" >&2
  fail "a lock whose holder is gone was not reclaimed"
fi
assert_contains "$TEMP_DIR/stale-lock.out" "Reclaimed a stale deployment lock"
assert_contains "$TEMP_DIR/stale-lock.out" "pid=999999"
assert_contains "$TEMP_DIR/stale-lock.out" "action=backup"
assert_contains "$MOCK_LOG" "pull migrate api web"
assert_contains "$TEST_ROOT/.deployrc.lock.log" "reason=pid_gone"
[ ! -e "$TEST_ROOT/.deploy.lock" ] || fail "the reclaimed lock was left behind"
assert_no_claim_leftovers

# pid 还在但启动时刻不同 = pid 已被别的进程复用，同样按残留锁回收（并说明原因）。
rm -f "$TEST_ROOT/.deployrc.lock.log"
mkdir "$TEST_ROOT/.deploy.lock"
printf '%s\n' "$$" > "$TEST_ROOT/.deploy.lock/pid"
printf 'pid=%s\naction=restore\nstarted_at=2026-01-01T00:00:00Z\nproc_start=Thu Jan  1 00:00:00 2026\n' \
  "$$" > "$TEST_ROOT/.deploy.lock/owner"
if ! run_deploy reused-pid-lock f ""; then
  cat "$TEMP_DIR/reused-pid-lock.out" >&2
  fail "a lock whose pid was reused was not reclaimed"
fi
assert_contains "$TEMP_DIR/reused-pid-lock.out" "reason=pid_reused"
assert_contains "$MOCK_LOG" "pull migrate api web"

# 活着的持有者必须挡住：拒绝、点名持有者与 --force-unlock，且不产生任何 docker 调用。
rm -f "$TEST_ROOT/.deployrc.lock.log"
mkdir "$TEST_ROOT/.deploy.lock"
printf '%s\n' "$$" > "$TEST_ROOT/.deploy.lock/pid"
printf 'pid=%s\naction=restart\nstarted_at=2026-01-01T00:00:00Z\nproc_start=%s\n' \
  "$$" "$(LC_ALL=C ps -o lstart= -p "$$" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')" \
  > "$TEST_ROOT/.deploy.lock/owner"
if run_deploy live-lock f ""; then
  fail "a lock held by a live process was reclaimed"
fi
assert_contains "$TEMP_DIR/live-lock.out" "--force-unlock"
assert_contains "$TEMP_DIR/live-lock.out" "state=alive"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
[ -e "$TEST_ROOT/.deploy.lock/owner" ] || fail "the live lock was removed"

# --force-unlock 是人工兜底：清掉锁、留下「清了谁」的日志。
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" ./deploy.sh --lang en --force-unlock
) > "$TEMP_DIR/force-unlock.out" 2>&1; then
  cat "$TEMP_DIR/force-unlock.out" >&2
  fail "--force-unlock failed to remove a live lock"
fi
[ ! -e "$TEST_ROOT/.deploy.lock" ] || fail "--force-unlock left the lock behind"
assert_contains "$TEST_ROOT/.deployrc.lock.log" "Removed the deployment lock by force"
assert_contains "$TEST_ROOT/.deployrc.lock.log" "pid=$$"

# 没有锁时 --force-unlock 是幂等的，不报错。
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" ./deploy.sh --lang en --action unlock
) > "$TEMP_DIR/unlock-noop.out" 2>&1; then
  fail "--action unlock must be a no-op when no lock exists"
fi
assert_contains "$TEMP_DIR/unlock-noop.out" "No deployment lock is present."

# OPS2-08 的副作用修正：stop / logs / status 是事故中的止损与取证动作，而新键在存量
# 安装里根本不存在。它们只把同一条修法提示降级成 warning，不 die；deploy 与显式
# check-env 仍然 strict（退出码非 0），--force-unlock 不进闸门。
cp "$TEST_ROOT/.env.production" "$TEMP_DIR/graded.env"
grep -v '^MIGRATE_ON_STARTUP=' "$TEMP_DIR/graded.env" > "$TEST_ROOT/.env.production"
for advisory_action in stop logs status; do
  : > "$MOCK_LOG"
  if ! (
    cd "$TEST_ROOT"
    PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" \
      ./deploy.sh --lang en --action "$advisory_action"
  ) > "$TEMP_DIR/gate-${advisory_action}.out" 2>&1; then
    cat "$TEMP_DIR/gate-${advisory_action}.out" >&2
    fail "$advisory_action must keep working when MIGRATE_ON_STARTUP is missing"
  fi
  assert_contains "$TEMP_DIR/gate-${advisory_action}.out" "warning:"
  assert_contains "$TEMP_DIR/gate-${advisory_action}.out" "MIGRATE_ON_STARTUP"
  if [ "$advisory_action" = logs ]; then
    assert_contains "$MOCK_LOG" "logs -f --tail=200"
  fi
done

if run_deploy gate-deploy f ""; then
  fail "deploy must stay strict when MIGRATE_ON_STARTUP is missing"
fi
assert_contains "$TEMP_DIR/gate-deploy.out" "MIGRATE_ON_STARTUP"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
if (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" ./deploy.sh --lang en --action check-env
) > "$TEMP_DIR/gate-check-env.out" 2>&1; then
  fail "check-env must stay strict when MIGRATE_ON_STARTUP is missing"
fi
assert_contains "$TEMP_DIR/gate-check-env.out" "MIGRATE_ON_STARTUP"

# 全放通 TRUSTED_PROXIES 走同一分级：stop 放行，deploy 仍然拒绝。
sed 's#^TRUSTED_PROXIES=.*#TRUSTED_PROXIES=0.0.0.0/0#' "$TEMP_DIR/graded.env" \
  > "$TEST_ROOT/.env.production"
: > "$MOCK_LOG"
if ! (
  cd "$TEST_ROOT"
  PATH="$MOCK_BIN:$PATH" MOCK_DEPLOY_LOG="$MOCK_LOG" ./deploy.sh --lang en --action stop
) > "$TEMP_DIR/gate-stop-trust.out" 2>&1; then
  cat "$TEMP_DIR/gate-stop-trust.out" >&2
  fail "stop must keep working with a full-range TRUSTED_PROXIES"
fi
assert_contains "$TEMP_DIR/gate-stop-trust.out" "warning:"
assert_contains "$TEMP_DIR/gate-stop-trust.out" "TRUSTED_PROXIES"
if run_deploy gate-deploy-trust f ""; then
  fail "deploy must reject a full-range TRUSTED_PROXIES"
fi
assert_contains "$TEMP_DIR/gate-deploy-trust.out" "TRUSTED_PROXIES"
assert_not_contains "$MOCK_LOG" "pull migrate api web"
cp "$TEMP_DIR/graded.env" "$TEST_ROOT/.env.production"

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
assert_contains "$TEMP_DIR/existing-status.out" "Install directory: $EXPECTED_ROOT"

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
