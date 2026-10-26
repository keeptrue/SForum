#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-compose-production-test.XXXXXX")"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

"$ROOT_DIR/deploy/scripts/configure-production.sh" \
  --lang en \
  --defaults \
  --version v3.0.0-alpha.9 \
  --root "$ROOT_DIR" \
  --output "$TEMP_DIR/.env.production" >/dev/null

docker compose \
  --env-file "$TEMP_DIR/.env.production" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  --profile tools \
  config --format json > "$TEMP_DIR/compose.json"

cp "$TEMP_DIR/.env.production" "$TEMP_DIR/.env.production.legacy"
printf 'EMBED_WORKER_IN_API=false\n' >> "$TEMP_DIR/.env.production.legacy"
docker compose \
  --env-file "$TEMP_DIR/.env.production.legacy" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  --profile tools \
  config --format json > "$TEMP_DIR/compose.legacy.json"

# 只保留 compose 展开必需的键：四项信任配置必须完全来自 compose.yaml 自身的默认值，
# 否则这段断言会被环境文件里的同名值「蒙对」（向导现在写的值与默认值相同）。
grep -E '^(SFORUM_VERSION|SESSION_HASH_SECRET|IDENTITY_SUBJECT_HMAC_SECRET|APP_OPTION_ENC_KEY|ALTCHA_SECRET)=' \
  "$TEMP_DIR/.env.production" > "$TEMP_DIR/.env.production.defaults"
docker compose \
  --env-file "$TEMP_DIR/.env.production.defaults" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  --profile tools \
  config --format json > "$TEMP_DIR/compose.defaults.json"

# 总开关必须能一键回退：环境文件写 TRUST_PROXY=false 后 API 侧不再采信转发头。
cp "$TEMP_DIR/.env.production.defaults" "$TEMP_DIR/.env.production.off"
printf 'TRUST_PROXY=false\n' >> "$TEMP_DIR/.env.production.off"
docker compose \
  --env-file "$TEMP_DIR/.env.production.off" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  --profile tools \
  config --format json > "$TEMP_DIR/compose.off.json"

ruby -rjson -e '
  env = File.readlines(ARGV.fetch(0), chomp: true).each_with_object({}) do |line, values|
    next if line.empty? || line.start_with?("#")
    key, value = line.split("=", 2)
    values[key] = value
  end
  compose = JSON.parse(File.read(ARGV.fetch(1)))
  services = compose.fetch("services")
  migrate = services.fetch("migrate").fetch("environment")
  required = %w[
    SESSION_HASH_SECRET
    IDENTITY_SUBJECT_HMAC_SECRET
    APP_OPTION_ENC_KEY
    ALTCHA_SECRET
  ]
  required.each do |key|
    actual = migrate.fetch(key)
    expected = env.fetch(key)
    abort "compose-production_test.sh: migrate #{key} was not passed through" unless actual == expected
    abort "compose-production_test.sh: migrate #{key} is empty" if actual.empty?
  end
  abort "compose-production_test.sh: migrate APP_ENV is not production" unless migrate["APP_ENV"] == "production"
  # config.Load 在生产态拒绝相对 EXTENSION_ROOT；migrate 也必须拿到绝对路径，
  # 否则发布冒烟会在 migrate 步骤 panic（2026-10-22 v3.0.13-alpha.1 实测）。
  migrate_extension_root = migrate.fetch("EXTENSION_ROOT", "")
  abort "compose-production_test.sh: migrate EXTENSION_ROOT must be set" if migrate_extension_root.empty?
  unless migrate_extension_root.start_with?("/")
    abort "compose-production_test.sh: migrate EXTENSION_ROOT must be absolute, got #{migrate_extension_root.inspect}"
  end
  api = services.fetch("api")
  abort "compose-production_test.sh: API still exposes Worker ownership configuration" if api.fetch("environment").key?("EMBED_WORKER_IN_API")
  abort "compose-production_test.sh: default topology includes standalone Worker" if services.key?("worker")

  # 真实客户端 IP：api 的对端是 web（Nuxt）容器，信任私网对端才是正确默认值；
  # TRUSTED_PROXIES 留给 CDN / 额外代理，必须存在且为空。
  expected_trust = {
    "TRUST_PROXY" => "true",
    "TRUSTED_PROXIES" => "",
    "TRUST_PROXY_PRIVATE" => "true",
    "TRUST_PROXY_LOOPBACK" => "false",
  }
  expected_trust.each do |key, expected|
    abort "compose-production_test.sh: API does not pass through #{key}" unless api.fetch("environment").key?(key)
    actual = api.fetch("environment").fetch(key)
    unless actual == expected
      abort "compose-production_test.sh: API #{key} should default to #{expected.inspect}, got #{actual.inspect}"
    end
  end
  abort "compose-production_test.sh: API does not pass through SFORUM_SAFE_MODE" unless api.fetch("environment").key?("SFORUM_SAFE_MODE")
  abort "compose-production_test.sh: SFORUM_SAFE_MODE should default to an empty value" unless api.fetch("environment").fetch("SFORUM_SAFE_MODE") == ""
  unless services.fetch("web").fetch("environment").key?("TRUSTED_PROXIES")
    abort "compose-production_test.sh: web does not receive TRUSTED_PROXIES for outbound client IP normalization"
  end

  legacy_services = JSON.parse(File.read(ARGV.fetch(2))).fetch("services")
  abort "compose-production_test.sh: legacy env revived standalone Worker" if legacy_services.key?("worker")
  abort "compose-production_test.sh: legacy env changed API ownership" if legacy_services.fetch("api").fetch("environment").key?("EMBED_WORKER_IN_API")

  # 环境文件省略四项信任配置时，默认值必须来自 compose.yaml 本身。
  defaults_api = JSON.parse(File.read(ARGV.fetch(3))).fetch("services").fetch("api").fetch("environment")
  expected_trust.each do |key, expected|
    actual = defaults_api.fetch(key, "<missing>")
    unless actual == expected
      abort "compose-production_test.sh: compose.yaml default for #{key} should be #{expected.inspect}, got #{actual.inspect}"
    end
  end

  # TRUST_PROXY=false 是唯一需要的回退开关。
  off_api = JSON.parse(File.read(ARGV.fetch(4))).fetch("services").fetch("api").fetch("environment")
  unless off_api.fetch("TRUST_PROXY", "<missing>") == "false"
    abort "compose-production_test.sh: TRUST_PROXY=false from the env file did not reach the API container"
  end
' "$TEMP_DIR/.env.production" "$TEMP_DIR/compose.json" "$TEMP_DIR/compose.legacy.json" \
  "$TEMP_DIR/compose.defaults.json" "$TEMP_DIR/compose.off.json"

printf 'compose-production_test.sh: all checks passed\n'
