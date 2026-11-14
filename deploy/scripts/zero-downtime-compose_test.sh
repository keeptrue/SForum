#!/usr/bin/env bash
# shellcheck disable=SC1003,SC2016
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-zero-downtime-compose-test.XXXXXX")"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

"$ROOT_DIR/deploy/scripts/configure-production.sh" \
  --lang en \
  --defaults \
  --version v3.0.0-alpha.10 \
  --root "$ROOT_DIR" \
  --output "$TEMP_DIR/.env.production" >/dev/null

SFORUM_BLUE_VERSION=v3.0.0-alpha.10 \
SFORUM_GREEN_VERSION=v3.0.0-alpha.11 \
docker compose \
  --env-file "$TEMP_DIR/.env.production" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  -f "$ROOT_DIR/compose.zero-downtime.yaml" \
  --profile zero-downtime \
  config --format json > "$TEMP_DIR/compose.json"

ruby -rjson -e '
  services = JSON.parse(File.read(ARGV.fetch(0))).fetch("services")
  slots = %w[blue green]
  edge = services.fetch("edge")
  published = edge.fetch("ports").map { |port| [port["host_ip"], port["published"], port["target"]] }
  expected_ports = [["127.0.0.1", "3000", 3000], ["127.0.0.1", "18080", 18080]]
  abort "edge is not the stable loopback ingress: #{published.inspect}" unless published == expected_ports

  slots.each do |slot|
    api = services.fetch("api-#{slot}")
    web = services.fetch("web-#{slot}")
    abort "api-#{slot} published a host port" if api.key?("ports")
    abort "web-#{slot} published a host port" if web.key?("ports")
    abort "web-#{slot} points at the wrong API" unless web.dig("environment", "NUXT_API_INTERNAL_BASE_URL") == "http://api-#{slot}:8080/api/v1"
    abort "api-#{slot} still exposes Worker ownership configuration" if api.fetch("environment").key?("EMBED_WORKER_IN_API")
    abort "worker-#{slot} must not exist" if services.key?("worker-#{slot}")
    mounts = api.fetch("volumes").map { |volume| [volume["source"], volume["target"]] }
    abort "api-#{slot} lost attachment volume" unless mounts.include?(["attachment_uploads", "/app/storage/app/attachments"])
    abort "api-#{slot} lost extension volume" unless mounts.include?(["extension_packages", "/var/lib/sforum/extensions"])
  end
  abort "blue API tag mismatch" unless services.dig("api-blue", "image").end_with?(":v3.0.0-alpha.10")
  abort "green API tag mismatch" unless services.dig("api-green", "image").end_with?(":v3.0.0-alpha.11")
' "$TEMP_DIR/compose.json"

# RV-02：入口生成的两个 server 块都必须按 {client_ip} 重写 X-Real-IP。限流键、
# 登录锁定 IP 和审计来源地址都读这个头；缺了它上游只能看到 web 容器的地址，
# 客户端自带的同名头也不会被覆盖。这里直接调用 upgrade.sh 的生成函数，不依赖真实部署。
router_config_error() {
  local config="$1" slot="$2" upstream block count
  [ -f "$config" ] || { printf 'rendered router config is missing: %s\n' "$config"; return 1; }
  for upstream in "web-$slot:3000" "api-$slot:8080"; do
    block="$(grep -A1 -F -- "reverse_proxy $upstream {" "$config" || true)"
    case "$block" in
      *"header_up X-Real-IP {client_ip}"*) ;;
      *) printf 'reverse_proxy %s has no X-Real-IP rewrite\n' "$upstream"; return 1 ;;
    esac
  done
  count="$(grep -Fc -- 'header_up X-Real-IP {client_ip}' "$config" || true)"
  [ "$count" -eq 2 ] || { printf 'expected 2 X-Real-IP rewrites, got %s\n' "$count"; return 1; }
  count="$(grep -Fc -- "header X-SForum-Active-Slot $slot" "$config" || true)"
  [ "$count" -eq 2 ] || { printf 'expected the active-slot header in both blocks, got %s\n' "$count"; return 1; }
  return 0
}

RENDER_ROOT="$TEMP_DIR/render"
mkdir -p "$RENDER_ROOT"
for slot in blue green; do
  (
    # upgrade.sh 只在被直接执行时跑 main；source 进来只取生成函数。
    set --
    # shellcheck disable=SC1090,SC1091
    . "$ROOT_DIR/upgrade.sh"
    RUNTIME_DIR="$RENDER_ROOT/runtime"
    write_router_config "$slot" "$RENDER_ROOT/Caddyfile.$slot"
  )
  if ! reason="$(router_config_error "$RENDER_ROOT/Caddyfile.$slot" "$slot")"; then
    printf 'zero-downtime-compose_test.sh: %s\n' "$reason" >&2
    exit 1
  fi
done

# 负向对照：删掉该行后断言必须失败，否则说明上面的检查是空的。
sed '/header_up X-Real-IP/d' "$RENDER_ROOT/Caddyfile.blue" > "$RENDER_ROOT/Caddyfile.stripped"
if router_config_error "$RENDER_ROOT/Caddyfile.stripped" blue >/dev/null; then
  printf 'zero-downtime-compose_test.sh: the X-Real-IP assertion does not detect a missing line\n' >&2
  exit 1
fi

# deploy/router/Caddyfile.example 随发布资产分发，自称稳定入口；手工部署者会照抄，
# 所以它必须与生成的配置同口径，否则照抄就退回旧行为。
if ! reason="$(router_config_error "$ROOT_DIR/deploy/router/Caddyfile.example" blue)"; then
  printf 'zero-downtime-compose_test.sh: deploy/router/Caddyfile.example: %s\n' "$reason" >&2
  exit 1
fi

"$ROOT_DIR/upgrade.sh" --help | grep -Fq -- "explicitly declared backward-compatible Core migrations" || {
  printf 'zero-downtime-compose_test.sh: help must document guarded online migrations\n' >&2
  exit 1
}

grep -Fq 'io.sforum.migrations.online-safe-check="v1"' "$ROOT_DIR/apps/api/Dockerfile" || {
  printf 'zero-downtime-compose_test.sh: migrator capability label is missing\n' >&2
  exit 1
}

grep -Fq 'sforum-migrate --check-online-safe' "$ROOT_DIR/upgrade.sh" || {
  printf 'zero-downtime-compose_test.sh: updater does not use the online migration guard\n' >&2
  exit 1
}

"$ROOT_DIR/upgrade.sh" --help | grep -Fq -- "default: latest" || {
  printf 'zero-downtime-compose_test.sh: help must document the latest default\n' >&2
  exit 1
}

grep -Fq 'http://127.0.0.1:3000/health' "$ROOT_DIR/upgrade.sh" || {
  printf 'zero-downtime-compose_test.sh: candidate Web probe must use /health\n' >&2
  exit 1
}

grep -Fq 'http://127.0.0.1:${web_port}/health' "$ROOT_DIR/upgrade.sh" || {
  printf 'zero-downtime-compose_test.sh: stable Web probe must use /health\n' >&2
  exit 1
}

if grep -Fq 'http://127.0.0.1:3000/ \' "$ROOT_DIR/upgrade.sh" \
  || grep -Fq 'http://127.0.0.1:${web_port}/"' "$ROOT_DIR/upgrade.sh"; then
  printf 'zero-downtime-compose_test.sh: Web probes must not render the cached homepage\n' >&2
  exit 1
fi

printf 'zero-downtime-compose_test.sh: all checks passed\n'
