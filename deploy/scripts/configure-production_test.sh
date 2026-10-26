#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WIZARD="$SCRIPT_DIR/configure-production.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_line() {
  local expected="$1"
  local file="$2"
  grep -Fqx "$expected" "$file" || fail "missing expected line: $expected"
}

env_value() {
  local key="$1"
  local file="$2"
  sed -n "s/^${key}=//p" "$file" | tail -n 1
}

file_mode() {
  local file="$1"
  if stat -f '%Lp' "$file" >/dev/null 2>&1; then
    stat -f '%Lp' "$file"
  else
    stat -c '%a' "$file"
  fi
}

OUTPUT="$TEST_ROOT/default.env"
"$WIZARD" --lang en --defaults --default-version v3.0.0-alpha.8 --root "$TEST_ROOT" --output "$OUTPUT" > "$TEST_ROOT/default.log"

[ -f "$OUTPUT" ] || fail "default mode did not create the output"
[ "$(file_mode "$OUTPUT")" = "600" ] || fail "output mode is not 0600"
assert_line 'APP_ENV=production' "$OUTPUT"
assert_line 'APP_URL=http://127.0.0.1:3000' "$OUTPUT"
assert_line 'WEB_PORT=3000' "$OUTPUT"
assert_line 'API_PORT=18080' "$OUTPUT"
assert_line 'MIGRATE_ON_STARTUP=false' "$OUTPUT"
assert_line 'DATABASE_URL=postgres://sforum:'"$(env_value POSTGRES_PASSWORD "$OUTPUT")"'@postgres:5432/sforum?sslmode=disable' "$OUTPUT"
assert_line 'REDIS_ADDR=redis:6379' "$OUTPUT"
assert_line 'NUXT_API_INTERNAL_BASE_URL=http://api:8080/api/v1' "$OUTPUT"
assert_line 'NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=/control-panel' "$OUTPUT"
assert_line 'SFORUM_VERSION=v3.0.0-alpha.8' "$OUTPUT"
# 真实客户端 IP：标准 Compose 拓扑下 api 的对端是 web 容器，必须信任私网对端；
# TRUSTED_PROXIES 留给 CDN / 额外代理，向导也要把它写出来（空值也算配置）。
assert_line 'TRUST_PROXY=true' "$OUTPUT"
assert_line 'TRUSTED_PROXIES=' "$OUTPUT"
assert_line 'TRUST_PROXY_PRIVATE=true' "$OUTPUT"
assert_line 'TRUST_PROXY_LOOPBACK=false' "$OUTPUT"
grep -Fq 'Client IP: trusting private-network peers' "$TEST_ROOT/default.log" ||
  fail "wizard did not report the effective client IP policy"

if grep -Fq 'change-me' "$OUTPUT"; then
  fail "output contains a placeholder secret"
fi

for key in POSTGRES_PASSWORD REDIS_PASSWORD SESSION_HASH_SECRET IDENTITY_SUBJECT_HMAC_SECRET APP_OPTION_ENC_KEY ALTCHA_SECRET; do
  value="$(env_value "$key" "$OUTPUT")"
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || fail "$key is not a 32-byte lowercase hex value"
done

if grep -Eq '^[A-Z0-9_]*(PASSWORD|SECRET|ENC_KEY|PUBLIC_KEY_HEX)=' "$TEST_ROOT/default.log"; then
  fail "wizard printed a secret"
fi

before_checksum="$(cksum "$OUTPUT")"
printf 'KEEP_THIS=1\n' >> "$OUTPUT"
"$WIZARD" --lang zh --defaults --root "$TEST_ROOT" --output "$TEST_ROOT/force.env" > "$TEST_ROOT/zh.log"
grep -Fq '真实客户端 IP：信任私网对端' "$TEST_ROOT/zh.log" ||
  fail "wizard did not report the client IP policy in Chinese"
"$WIZARD" --lang zh --defaults --root "$TEST_ROOT" --output "$OUTPUT" > "$TEST_ROOT/preserve.log"
assert_line 'KEEP_THIS=1' "$OUTPUT"
[ "$(cksum "$OUTPUT")" != "$before_checksum" ] || fail "preservation test setup did not change the file"

if "$WIZARD" --lang zh --defaults --force --root "$TEST_ROOT" --output "$OUTPUT" >/dev/null 2>&1; then
  fail "--force replaced an existing production configuration"
fi
assert_line 'KEEP_THIS=1' "$OUTPUT"

if "$WIZARD" --defaults --version invalid --root "$TEST_ROOT" --output "$TEST_ROOT/invalid-version.env" >/dev/null 2>&1; then
  fail "invalid version was accepted"
fi

if printf '\n70000\n18080\n/admin\n\n' | "$WIZARD" --lang en --root "$TEST_ROOT" --output "$TEST_ROOT/invalid-port.env" >/dev/null 2>&1; then
  fail "invalid port was accepted"
fi

if printf '\n3001\n18081\n/admin\nnot-a-url\n' | "$WIZARD" --lang en --root "$TEST_ROOT" --output "$TEST_ROOT/invalid-url.env" >/dev/null 2>&1; then
  fail "invalid URL was accepted"
fi

printf '\n3001\n18081\nhttps://forum.example.com\nstaff-admin\n' | "$WIZARD" --lang en --root "$TEST_ROOT" --output "$TEST_ROOT/custom.env" >/dev/null
assert_line 'NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=/staff-admin' "$TEST_ROOT/custom.env"

if printf '\n3001\n18081\nhttps://forum.example.com\n/admin?debug\n' | "$WIZARD" --lang en --root "$TEST_ROOT" --output "$TEST_ROOT/invalid-admin-prefix.env" >/dev/null 2>&1; then
  fail "invalid admin route prefix was accepted"
fi

printf 'configure-production tests passed\n'
