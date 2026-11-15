#!/usr/bin/env bash
# RV-43 的首次安装 / 首次升级分支：bootstrap_topology 里的 caddy 身份校验。
#
# 为什么单独有这个用例：verify_caddy_image() 只被 bootstrap_topology() 调用，而
# bootstrap_test.sh 只测部署包解包/铺开。审查实测把 verify_caddy_image 的两条拒绝
# 整段删掉，原有 12 个套件仍然全绿——标签重推攻击会静默复活。这里直接把
# bootstrap_topology 跑到 caddy 校验点，用 mock docker 覆盖两条拒绝路径：
#   1) SFORUM_CADDY_IMAGE 不带 @sha256: → 直接拒；
#   2) 拉到的 RepoDigests 不含钉住的 digest → die。
# shellcheck disable=SC1091
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-upgrade-bootstrap-image-test.XXXXXX")"
MOCK_BIN="$TEMP_DIR/bin"
WORK_DIR="$TEMP_DIR/work"
DOCKER_LOG="$TEMP_DIR/docker.log"
FAKE_DIGEST="sha256:$(printf 'a%.0s' {1..64})"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

fail() {
  printf 'upgrade-bootstrap-image_test.sh: %s\n' "$1" >&2
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

mkdir -p "$MOCK_BIN" "$WORK_DIR"
printf '%s\n' 'WEB_PORT=3000' 'API_PORT=18080' > "$WORK_DIR/.env.production"
printf 'lang=en\n' > "$WORK_DIR/.deployrc"

cat > "$MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >> "$MOCK_UPGRADE_LOG"
if [ "${1:-}" = info ]; then
  exit 0
fi
if [ "${1:-}" = buildx ]; then
  printf '"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"\n'
  exit 0
fi
if [ "${1:-}" = pull ]; then
  exit 0
fi
if [ "${1:-}" = image ]; then
  for arg in "$@"; do
    case "$arg" in
      *RepoDigests*)
        case "$*" in
          *caddy*) printf '["caddy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]' ;;
          *) printf '["ghcr.io/keeptrue/sforum-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]' ;;
        esac
        exit 0
        ;;
      *image.version*)
        printf '3.0.0'
        exit 0
        ;;
      *online-safe-check*)
        printf 'v1'
        exit 0
        ;;
    esac
  done
  exit 0
fi
exit 43
EOF
chmod +x "$MOCK_BIN/docker"

# upgrade.sh 只在被直接执行时跑 main；source 进来只取函数。它会把 ROOT_DIR 设成
# 仓库根并 cd 过去，测试随后在自己的 work 目录里调用 bootstrap_topology。
set --
source "$ROOT_DIR/upgrade.sh"
COMPOSE=(docker compose --env-file .env.production)

run_bootstrap() {
  local name="$1" caddy_override="${2:-}"
  : > "$DOCKER_LOG"
  (
    cd "$WORK_DIR" || exit 1
    export PATH="$MOCK_BIN:$PATH"
    export MOCK_UPGRADE_LOG="$DOCKER_LOG"
    export FORCE_BOOTSTRAP=true
    export REGISTRY=ghcr.io/keeptrue
    # 变异后（caddy 校验被删）流程会走到 wait_inside_slot；把宽限压到 1 秒，
    # 让「拒绝缺失」这类回归在 CI 里快速变红，而不是空转两分钟。
    export SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS=1
    if [ -n "$caddy_override" ]; then
      export SFORUM_CADDY_IMAGE="$caddy_override"
    fi
    bootstrap_topology v3.0.0 v3.0.0
  ) > "$TEMP_DIR/$name.out" 2>&1
}

# 1) 覆盖值不带 digest：必须在任何 compose 动作之前拒绝，并说清要写 name@sha256:...
if run_bootstrap no-digest caddy:2.11-alpine; then
  fail "a caddy image override without a digest was accepted"
fi
assert_contains "$TEMP_DIR/no-digest.out" 'must pin a digest'
assert_contains "$DOCKER_LOG" 'buildx imagetools inspect ghcr.io/keeptrue/sforum-api:v3.0.0'
assert_contains "$DOCKER_LOG" 'pull ghcr.io/keeptrue/sforum-migrate:v3.0.0'
assert_contains "$DOCKER_LOG" 'pull caddy:2.11-alpine'
assert_not_contains "$DOCKER_LOG" 'compose --env-file'

# 2) 钉了 digest，但本地 RepoDigests 不是那个 digest：必须 die，且不进 compose。
#    覆盖值写在 .env.production 里，同时验证 shell > 文件 > 默认 的取值顺序。
printf 'SFORUM_CADDY_IMAGE=caddy:2.11-alpine@%s\n' "$FAKE_DIGEST" >> "$WORK_DIR/.env.production"
if run_bootstrap wrong-digest; then
  fail "a caddy image whose RepoDigests do not match the pinned digest was accepted"
fi
assert_contains "$TEMP_DIR/wrong-digest.out" 'edge image content identity mismatch'
assert_contains "$DOCKER_LOG" "image inspect caddy:2.11-alpine@$FAKE_DIGEST"
assert_not_contains "$DOCKER_LOG" 'compose --env-file'

printf 'upgrade-bootstrap-image_test.sh: all checks passed\n'
