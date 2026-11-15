#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-compose-production-test.XXXXXX")"

cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

# 与 deploy.sh 的 sanitize_env_contract_overrides 保持一致：这些键在 shell 里导出时
# 会覆盖 --env-file，让断言读到「恰好被环境蒙对」的值。先清干净再测。
unset APP_ENV APP_URL NUXT_PUBLIC_API_BASE_URL NUXT_PUBLIC_ADMIN_ROUTE_PREFIX \
  POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD DATABASE_URL \
  REDIS_ADDR REDIS_PASSWORD SESSION_HASH_SECRET IDENTITY_SUBJECT_HMAC_SECRET \
  APP_OPTION_ENC_KEY ALTCHA_SECRET TRUSTED_PROXIES MIGRATE_ON_STARTUP

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

# Ruby 侧要用 #{key} 插值；单引号是给 shell 的，不是给 Ruby 的。
# shellcheck disable=SC2016
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

  # RV-17：api 段必须自己钉 production。compose.yaml 的 api 默认是 development，
  # 只钉 web/migrate 会让生产 API 以开发态启动，validateProductionSecrets 整段被跳过。
  api_env = api.fetch("environment")
  unless api_env.fetch("APP_ENV", "<missing>") == "production"
    abort "compose-production_test.sh: API APP_ENV must be production, got #{api_env.fetch("APP_ENV", "<missing>").inspect}"
  end
  # 生产闸门会拒绝 compose.yaml 的开发默认密钥，所以 api 段必须像 migrate 一样
  # 拿到真实的四项密钥（compose.prod.yaml 用 `:?` 前置失败，这里校验展开结果）。
  required.each do |key|
    actual = api_env.fetch(key)
    abort "compose-production_test.sh: API #{key} was not passed through" unless actual == env.fetch(key)
    abort "compose-production_test.sh: API #{key} is empty" if actual.empty?
  end
  # config.Load 在生产态拒绝相对 EXTENSION_ROOT；api 与 migrate 同样要拿到绝对路径。
  api_extension_root = api_env.fetch("EXTENSION_ROOT", "")
  if api_extension_root.empty? || !api_extension_root.start_with?("/")
    abort "compose-production_test.sh: API EXTENSION_ROOT must be absolute, got #{api_extension_root.inspect}"
  end

  # RV-41：生产拓扑同样要自带健康探针；否则 compose 的 service_healthy 条件
  # 只能退回 service_started，健康语义就丢了。
  api_health = api.fetch("healthcheck")
  abort "compose-production_test.sh: API lost its healthcheck" unless api_health.fetch("test").to_s.include?("http://127.0.0.1:8080/api/v1/ready")
  web = services.fetch("web")
  web_health = web.fetch("healthcheck")
  abort "compose-production_test.sh: web lost its healthcheck" unless web_health.fetch("test").to_s.include?("http://127.0.0.1:3000/health")
  # compose.yaml 的 web 没有 depends_on：生产拓扑的 web→api 依赖只由
  # compose.prod.yaml 定义，这里必须钉住它等到 API 真正 healthy。
  web_api_condition = web.dig("depends_on", "api", "condition")
  unless web_api_condition == "service_healthy"
    abort "compose-production_test.sh: web must wait for a healthy API, got #{web_api_condition.inspect}"
  end

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
  # web 不再解析 TCP 对端（Nitro 的请求管线拿不到 socket），客户端 IP 由入口代理写入的
  # 单值头决定，因此它不需要、也不应该收到 TRUSTED_PROXIES。
  if services.fetch("web").fetch("environment").key?("TRUSTED_PROXIES")
    abort "compose-production_test.sh: web must not receive TRUSTED_PROXIES; it forwards the ingress client IP instead of resolving peers"
  end

  legacy_services = JSON.parse(File.read(ARGV.fetch(2))).fetch("services")
  abort "compose-production_test.sh: legacy env revived standalone Worker" if legacy_services.key?("worker")
  abort "compose-production_test.sh: legacy env changed API ownership" if legacy_services.fetch("api").fetch("environment").key?("EMBED_WORKER_IN_API")

  # 环境文件省略四项信任配置时，默认值必须来自 compose.yaml 本身。
  defaults_api = JSON.parse(File.read(ARGV.fetch(3))).fetch("services").fetch("api").fetch("environment")
  unless defaults_api.fetch("APP_ENV", "<missing>") == "production"
    abort "compose-production_test.sh: compose.prod.yaml must default API APP_ENV to production, got #{defaults_api.fetch("APP_ENV", "<missing>").inspect}"
  end
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

# OPS2-05：shell 环境变量优先于 --env-file，容器拿到的值可能与 .env.production 不同。
# 先钉住这个前提（证明不加处理时容器真的会拿到 shell 值），再钉住 deploy.sh 的处理。
SHELL_OVERRIDES=(
  "APP_ENV=staging"
  "DATABASE_URL=postgres://sforum:change-me@db.internal:5432/sforum?sslmode=disable"
  "TRUSTED_PROXIES=0.0.0.0/0"
)
env "${SHELL_OVERRIDES[@]}" docker compose \
  --env-file "$TEMP_DIR/.env.production" \
  -f "$ROOT_DIR/compose.yaml" \
  -f "$ROOT_DIR/compose.prod.yaml" \
  -f "$ROOT_DIR/compose.release.yaml" \
  --profile tools \
  config --format json > "$TEMP_DIR/compose.shell.json"

ruby -rjson -e '
  api = JSON.parse(File.read(ARGV.fetch(0))).fetch("services").fetch("api").fetch("environment")
  unless api.fetch("APP_ENV", "<missing>") == "staging"
    abort "compose-production_test.sh: the shell-override premise no longer holds; compose did not prefer the shell APP_ENV (got #{api.fetch("APP_ENV", "<missing>").inspect})"
  end
  unless api.fetch("DATABASE_URL", "<missing>") == ARGV.fetch(1)
    abort "compose-production_test.sh: the shell-override premise no longer holds; compose did not prefer the shell DATABASE_URL"
  end
' "$TEMP_DIR/compose.shell.json" "postgres://sforum:change-me@db.internal:5432/sforum?sslmode=disable"

# deploy.sh 在拼装 compose 命令前清掉这些 shell 覆盖，所以同一组导出下校验仍然
# 只认 .env.production：命令成功，并把被忽略的键显式告警。
DEPLOY_ROOT="$TEMP_DIR/deploy-root"
mkdir -p "$DEPLOY_ROOT/deploy/scripts"
cp "$ROOT_DIR/deploy.sh" "$DEPLOY_ROOT/"
cp "$ROOT_DIR/deploy/scripts/deploy-lock.sh" "$DEPLOY_ROOT/deploy/scripts/"
cp "$TEMP_DIR/.env.production" "$DEPLOY_ROOT/.env.production"

if ! env "${SHELL_OVERRIDES[@]}" bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
  > "$TEMP_DIR/check-env.shell.out" 2>&1; then
  printf 'compose-production_test.sh: deploy.sh must validate .env.production, not shell overrides\n' >&2
  cat "$TEMP_DIR/check-env.shell.out" >&2
  exit 1
fi
for key in APP_ENV DATABASE_URL TRUSTED_PROXIES; do
  grep -Fq -- "$key" "$TEMP_DIR/check-env.shell.out" || {
    printf 'compose-production_test.sh: deploy.sh must report the ignored shell override for %s\n' "$key" >&2
    cat "$TEMP_DIR/check-env.shell.out" >&2
    exit 1
  }
done

# 文件本身写错时必须失败，且提示指向 .env.production（而不是让人去改 shell）。
sed 's/^APP_ENV=.*/APP_ENV=staging/' "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
if bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env > "$TEMP_DIR/check-env.bad.out" 2>&1; then
  printf 'compose-production_test.sh: deploy.sh accepted an APP_ENV that is not production\n' >&2
  exit 1
fi
grep -Fq -- "APP_ENV" "$TEMP_DIR/check-env.bad.out" || {
  printf 'compose-production_test.sh: deploy.sh failure must name APP_ENV\n' >&2
  cat "$TEMP_DIR/check-env.bad.out" >&2
  exit 1
}
grep -Fq -- ".env.production" "$TEMP_DIR/check-env.bad.out" || {
  printf 'compose-production_test.sh: deploy.sh failure must point at .env.production\n' >&2
  cat "$TEMP_DIR/check-env.bad.out" >&2
  exit 1
}

# 占位口令：config.ValidateProductionDatabaseURL 会让容器直接 panic，所以
# check-env 必须在同一批口令上失败，否则又变成「校验值 ≠ 生效值」。
# 后两项是百分号编码形态：Go 的 url.Parse 先解码 userinfo 再比对，shell 侧只比
# 字面值就会漏过去（P3），所以这里把编码写法也钉进用例。
for placeholder in change-me sforum postgres change%2Dme %73forum; do
  sed "s#^DATABASE_URL=.*#DATABASE_URL=postgres://sforum:${placeholder}@postgres:5432/sforum?sslmode=disable#" \
    "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
  if bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
    > "$TEMP_DIR/check-env.password.out" 2>&1; then
    printf 'compose-production_test.sh: deploy.sh accepted the placeholder DATABASE_URL password %s\n' \
      "$placeholder" >&2
    exit 1
  fi
  grep -Fq -- "DATABASE_URL" "$TEMP_DIR/check-env.password.out" || {
    printf 'compose-production_test.sh: deploy.sh failure must name DATABASE_URL for the %s placeholder\n' \
      "$placeholder" >&2
    cat "$TEMP_DIR/check-env.password.out" >&2
    exit 1
  }
  grep -Fq -- ".env.production" "$TEMP_DIR/check-env.password.out" || {
    printf 'compose-production_test.sh: deploy.sh failure must point at .env.production for the %s placeholder\n' \
      "$placeholder" >&2
    cat "$TEMP_DIR/check-env.password.out" >&2
    exit 1
  }
done

# OPS2-08：TRUSTED_PROXIES 声明「谁有权替我写 XFF / X-Real-IP」，全放通等于任何
# 客户端都能伪造来源 IP，生产必须直接拒绝，并指出具体那一项。
for full_range in '0.0.0.0/0' '::/0' '*' '0.0.0.0' '::' '0:0:0:0:0:0:0:0' '10.0.0.0/8,0.0.0.0/0'; do
  sed "s#^TRUSTED_PROXIES=.*#TRUSTED_PROXIES=${full_range}#" \
    "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
  if bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
    > "$TEMP_DIR/check-env.trust.out" 2>&1; then
    printf 'compose-production_test.sh: deploy.sh accepted the full-range TRUSTED_PROXIES %s\n' \
      "$full_range" >&2
    exit 1
  fi
  grep -Fq -- "TRUSTED_PROXIES" "$TEMP_DIR/check-env.trust.out" || {
    printf 'compose-production_test.sh: deploy.sh failure must name TRUSTED_PROXIES for %s\n' \
      "$full_range" >&2
    cat "$TEMP_DIR/check-env.trust.out" >&2
    exit 1
  }
done

# 具体的代理出口段（以及标准拓扑下的空值）必须继续放行。
for allowed_range in '' '10.0.0.0/8,203.0.113.10'; do
  sed "s#^TRUSTED_PROXIES=.*#TRUSTED_PROXIES=${allowed_range}#" \
    "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
  if ! bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
    > "$TEMP_DIR/check-env.trust-ok.out" 2>&1; then
    printf 'compose-production_test.sh: deploy.sh rejected the proxy range %s\n' \
      "${allowed_range:-<empty>}" >&2
    cat "$TEMP_DIR/check-env.trust-ok.out" >&2
    exit 1
  fi
done

# MIGRATE_ON_STARTUP 必须显式声明：compose.yaml 的默认是 true，漏写等于 API 启动时
# 自行迁移（与 deploy.sh 显式的 migrate 服务争用同一套迁移）。
grep -v '^MIGRATE_ON_STARTUP=' "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
if bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
  > "$TEMP_DIR/check-env.migrate.out" 2>&1; then
  printf 'compose-production_test.sh: deploy.sh accepted a missing MIGRATE_ON_STARTUP\n' >&2
  exit 1
fi
grep -Fq -- "MIGRATE_ON_STARTUP" "$TEMP_DIR/check-env.migrate.out" || {
  printf 'compose-production_test.sh: the MIGRATE_ON_STARTUP failure must name the key\n' >&2
  cat "$TEMP_DIR/check-env.migrate.out" >&2
  exit 1
}
sed 's/^MIGRATE_ON_STARTUP=.*/MIGRATE_ON_STARTUP=maybe/' \
  "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
if bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
  > "$TEMP_DIR/check-env.migrate-bad.out" 2>&1; then
  printf 'compose-production_test.sh: deploy.sh accepted a non-boolean MIGRATE_ON_STARTUP\n' >&2
  exit 1
fi
for declared in false true; do
  sed "s#^MIGRATE_ON_STARTUP=.*#MIGRATE_ON_STARTUP=${declared}#" \
    "$TEMP_DIR/.env.production" > "$DEPLOY_ROOT/.env.production"
  if ! bash "$DEPLOY_ROOT/deploy.sh" --lang en --action check-env \
    > "$TEMP_DIR/check-env.migrate-ok.out" 2>&1; then
    printf 'compose-production_test.sh: deploy.sh rejected MIGRATE_ON_STARTUP=%s\n' "$declared" >&2
    cat "$TEMP_DIR/check-env.migrate-ok.out" >&2
    exit 1
  fi
done

# 模板必须给安全默认：迁移由 deploy.sh / upgrade.sh 的 migrate 服务显式执行，
# API 启动时不再自行迁移。向导写入的值与模板必须一致，否则「照着模板手填」会
# 退回 API 自迁移，并与显式 migrate 争用同一套 Goose 锁。
grep -qx 'MIGRATE_ON_STARTUP=false' "$ROOT_DIR/.env.production.example" || {
  printf 'compose-production_test.sh: .env.production.example must default MIGRATE_ON_STARTUP to false\n' >&2
  exit 1
}
grep -q "'MIGRATE_ON_STARTUP=false'" "$ROOT_DIR/deploy/scripts/configure-production.sh" || {
  printf 'compose-production_test.sh: configure-production.sh must write MIGRATE_ON_STARTUP=false\n' >&2
  exit 1
}

printf 'compose-production_test.sh: all checks passed\n'
