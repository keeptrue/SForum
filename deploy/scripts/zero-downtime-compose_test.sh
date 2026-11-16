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

# 默认值用例显式剔除 SFORUM_REGISTRY：开发机上导出过它的人会看到一条与提交无关的
# 红，而这条断言要钉的是「不设覆盖值时仓库里的默认仓库是什么」。
SFORUM_BLUE_VERSION=v3.0.0-alpha.10 \
SFORUM_GREEN_VERSION=v3.0.0-alpha.11 \
env -u SFORUM_REGISTRY docker compose \
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

  # RV-42：挂载源必须真实存在于仓库，否则 fresh clone 会被 Docker 建成 root 属主空目录。
  router_mount = edge.fetch("volumes").find { |volume| volume["target"] == "/etc/sforum-router" }
  abort "edge lost the /etc/sforum-router mount" unless router_mount
  abort "edge router mount must be read-only" unless router_mount["read_only"] == true
  abort "edge router mount source is missing: #{router_mount["source"].inspect}" unless File.directory?(router_mount["source"])

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

    # RV-41：槽容器必须自带健康探针，web 也必须等 API 真正 healthy 才启动。
    api_health = api.fetch("healthcheck")
    abort "api-#{slot} lost its healthcheck" unless api_health.fetch("test").to_s.include?("http://127.0.0.1:8080/api/v1/ready")
    abort "api-#{slot} healthcheck start_period is too short: #{api_health["start_period"].inspect}" unless %w[120s 2m0s].include?(api_health["start_period"])
    web_health = web.fetch("healthcheck")
    abort "web-#{slot} lost its healthcheck" unless web_health.fetch("test").to_s.include?("http://127.0.0.1:3000/health")
    abort "web-#{slot} healthcheck start_period is too short: #{web_health["start_period"].inspect}" unless %w[180s 3m0s].include?(web_health["start_period"])
    api_condition = web.dig("depends_on", "api-#{slot}", "condition")
    abort "web-#{slot} must wait for api-#{slot} to be healthy, got #{api_condition.inspect}" unless api_condition == "service_healthy"
  end
  abort "blue API tag mismatch" unless services.dig("api-blue", "image").end_with?(":v3.0.0-alpha.10")
  abort "green API tag mismatch" unless services.dig("api-green", "image").end_with?(":v3.0.0-alpha.11")
' "$TEMP_DIR/compose.json"

# R7-OPS-06：槽位镜像必须和 compose.release.yaml 同口径地读 SFORUM_REGISTRY。
# 硬编码 ghcr.io/keeptrue 时，私有 registry 的部署会出现「upgrade.sh 拉得到目标
# 镜像、edge 背后的槽位却指向别处」——两边各自以为自己拉的是同一个版本。

# 默认值用例要真的走到 `${SFORUM_REGISTRY:-ghcr.io/keeptrue}` 的默认分支，所以
# 既剔除开发机导出的 SFORUM_REGISTRY，也要用一份**没有这一行**的 env 文件：
# configure-production.sh 默认就会写 SFORUM_REGISTRY=ghcr.io/keeptrue，只要它还在，
# 默认分支永远不会被求值，四份镜像的默认断言就成了摆设（变异验证证实过：把
# web-green 的默认仓库改成 ghcr.io/other，带这一行的解析结果完全不变）。
grep -v '^SFORUM_REGISTRY=' "$TEMP_DIR/.env.production" > "$TEMP_DIR/.env.no-registry"

SFORUM_BLUE_VERSION=v3.0.0-alpha.10 \
SFORUM_GREEN_VERSION=v3.0.0-alpha.11 \
env -u SFORUM_REGISTRY docker compose \
  --env-file "$TEMP_DIR/.env.no-registry" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  -f "$ROOT_DIR/compose.zero-downtime.yaml" \
  --profile zero-downtime \
  config --format json > "$TEMP_DIR/compose-default.json"

SFORUM_REGISTRY=registry.internal/sforum \
SFORUM_BLUE_VERSION=v3.0.0-alpha.10 \
SFORUM_GREEN_VERSION=v3.0.0-alpha.11 \
docker compose \
  --env-file "$TEMP_DIR/.env.production" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  -f "$ROOT_DIR/compose.zero-downtime.yaml" \
  --profile zero-downtime \
  config --format json > "$TEMP_DIR/compose-registry.json"

ruby -rjson -e '
  prefix = "registry.internal/sforum"
  expected = {
    "api-blue" => "#{prefix}/sforum-api:v3.0.0-alpha.10",
    "web-blue" => "#{prefix}/sforum-web:v3.0.0-alpha.10",
    "api-green" => "#{prefix}/sforum-api:v3.0.0-alpha.11",
    "web-green" => "#{prefix}/sforum-web:v3.0.0-alpha.11"
  }
  services = JSON.parse(File.read(ARGV.fetch(0))).fetch("services")
  expected.each do |name, image|
    actual = services.fetch(name).fetch("image")
    abort "R7-OPS-06: #{name} ignores SFORUM_REGISTRY: #{actual.inspect} (expected #{image})" unless actual == image
  end

  # 默认值同样是契约，而且四份镜像都要查：不设覆盖值时槽位必须仍然指向
  # ghcr.io/keeptrue，否则改镜像仓库这件事会在默认路径上悄悄发生 —— 只查 api-blue
  # 会放过「另外三份被改到别的仓库」。
  defaults = JSON.parse(File.read(ARGV.fetch(1))).fetch("services")
  {
    "api-blue" => "ghcr.io/keeptrue/sforum-api:v3.0.0-alpha.10",
    "web-blue" => "ghcr.io/keeptrue/sforum-web:v3.0.0-alpha.10",
    "api-green" => "ghcr.io/keeptrue/sforum-api:v3.0.0-alpha.11",
    "web-green" => "ghcr.io/keeptrue/sforum-web:v3.0.0-alpha.11"
  }.each do |name, image|
    actual = defaults.fetch(name).fetch("image")
    abort "R7-OPS-06: default registry drifted for #{name}: #{actual.inspect} (expected #{image})" unless actual == image
  end
' "$TEMP_DIR/compose-registry.json" "$TEMP_DIR/compose-default.json"

# RV-43：edge 镜像必须按 digest 固定，且与 upgrade.sh 的常量逐字一致；否则两处
# 任一漂移都会让「拉取的身份」和「运行的身份」不是同一个东西。
compose_caddy_image="$(ruby -rjson -e 'print JSON.parse(File.read(ARGV.fetch(0))).fetch("services").fetch("edge").fetch("image")' "$TEMP_DIR/compose.json")"
upgrade_caddy_image="$(sed -n 's/^CADDY_IMAGE_DEFAULT="\(.*\)"$/\1/p' "$ROOT_DIR/upgrade.sh")"
case "$compose_caddy_image" in
  *@sha256:*) ;;
  *) printf 'zero-downtime-compose_test.sh: edge image is not digest-pinned: %s\n' "$compose_caddy_image" >&2; exit 1 ;;
esac
[ "$compose_caddy_image" = "$upgrade_caddy_image" ] || {
  printf 'zero-downtime-compose_test.sh: compose pins %s but upgrade.sh pins %s\n' \
    "$compose_caddy_image" "$upgrade_caddy_image" >&2
  exit 1
}

# RV-42：生成的路由配置不入库，但目录本身必须在仓库里。
RUNTIME_SOURCE="$ROOT_DIR/deploy/runtime"
[ -d "$RUNTIME_SOURCE" ] || {
  printf 'zero-downtime-compose_test.sh: deploy/runtime must be tracked so compose does not create it as root\n' >&2
  exit 1
}
[ -f "$RUNTIME_SOURCE/.gitignore" ] || {
  printf 'zero-downtime-compose_test.sh: deploy/runtime/.gitignore is missing\n' >&2
  exit 1
}
for rule in '*' '!.gitkeep' '!.gitignore'; do
  grep -qxF -- "$rule" "$RUNTIME_SOURCE/.gitignore" || {
    printf 'zero-downtime-compose_test.sh: deploy/runtime/.gitignore must contain the rule %s\n' "$rule" >&2
    exit 1
  }
done

# 根 .gitignore 曾经整目录忽略 deploy/runtime/，那样占位文件根本进不了仓库。
# 这里按「已跟踪或对 git 可见」判定，提交前后都成立；没有 .git 的导出包跳过。
if command -v git >/dev/null 2>&1 && git -C "$ROOT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  for placeholder in deploy/runtime/.gitignore deploy/runtime/.gitkeep; do
    if git -C "$ROOT_DIR" ls-files --error-unmatch "$placeholder" >/dev/null 2>&1; then
      continue
    fi
    git -C "$ROOT_DIR" ls-files --others --exclude-standard -- "$placeholder" | grep -qx -- "$placeholder" || {
      printf 'zero-downtime-compose_test.sh: %s is hidden by .gitignore and cannot be committed\n' "$placeholder" >&2
      exit 1
    }
  done
fi

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

# upgrade.sh 只在被直接执行时跑 main；source 进来只取生成函数。它会把 ROOT_DIR
# 重设成仓库根并 cd 过去，所以在函数里 source、出来恢复调用方的路径与工作目录；
# 循环变量叫 slot_name，避免覆盖 upgrade.sh 自己用的 slot。
render_slot_config() {
  local slot_name="$1" destination="$2" saved_root="$ROOT_DIR" saved_pwd="$PWD"
  set --
  # shellcheck disable=SC1090,SC1091
  . "$ROOT_DIR/upgrade.sh"
  ROOT_DIR="$saved_root"
  cd "$saved_pwd" || return 1
  RUNTIME_DIR="$RENDER_ROOT/runtime"
  write_router_config "$slot_name" "$destination"
}

for slot in blue green; do
  if ! render_slot_config "$slot" "$RENDER_ROOT/Caddyfile.$slot"; then
    printf 'zero-downtime-compose_test.sh: could not render the %s router config\n' "$slot" >&2
    exit 1
  fi
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
