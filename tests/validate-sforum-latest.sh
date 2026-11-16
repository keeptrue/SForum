#!/usr/bin/env bash
# The RV-15 checks grep for literal "$ROOT_DIR" text, not a shell expansion.
# shellcheck disable=SC2016
# Guard the optional sforum-latest.sh wrapper: its published SHA-256 must match
# the file, the script must parse, and the documented one-command entry must
# keep pointing at the checksum.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  printf 'validate-sforum-latest.sh: %s\n' "$1" >&2
  exit 1
}

[ -f sforum-latest.sh ] || fail "sforum-latest.sh is missing"
[ -f sforum-latest.sh.sha256 ] || fail "sforum-latest.sh.sha256 is missing"
bash -n sforum-latest.sh || fail "sforum-latest.sh has invalid shell syntax"
grep -q 'sforum-deploy\.tar\.gz' sforum-latest.sh || \
  fail "sforum-latest.sh must sync the complete deploy bundle, not only the bootstrap"
grep -q 'managed_files' sforum-latest.sh || \
  fail "sforum-latest.sh must read the bundle's managed-file list"

# RV-15: the wrapper used to be fetched from the mutable main branch. It is only
# trustworthy as a Release asset shipped together with its checksum, so the
# release build must produce both and the finalizer must require both.
grep -q 'cp "$ROOT_DIR/sforum-latest.sh" "$OUTPUT_DIR/sforum-latest.sh"' \
  scripts/ci/build-deploy-asset.sh || \
  fail "build-deploy-asset.sh must publish sforum-latest.sh as a Release asset"
grep -q 'cp "$ROOT_DIR/sforum-latest.sh.sha256" "$OUTPUT_DIR/sforum-latest.sh.sha256"' \
  scripts/ci/build-deploy-asset.sh || \
  fail "build-deploy-asset.sh must publish sforum-latest.sh.sha256 next to the script"
grep -q '"sforum-latest.sh.sha256"' scripts/ci/finalize-release-assets.sh || \
  fail "finalize-release-assets.sh must require the wrapper checksum in the asset matrix"
grep -q 'sha256sum -c sforum-latest.sh.sha256' scripts/ci/finalize-release-assets.sh || \
  fail "finalize-release-assets.sh must verify the wrapper checksum before publishing"

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c sforum-latest.sh.sha256 >/dev/null || \
    fail "sforum-latest.sh.sha256 is stale; run: sha256sum sforum-latest.sh > sforum-latest.sh.sha256"
else
  shasum -a 256 -c sforum-latest.sh.sha256 >/dev/null || \
    fail "sforum-latest.sh.sha256 is stale; run: shasum -a 256 sforum-latest.sh > sforum-latest.sh.sha256"
fi

for file in docs/zh-CN/deployment/one-click.md docs/en-US/deployment/one-click.md; do
  grep -q 'sforum-latest\.sh\.sha256' "$file" || \
    fail "$file must document the checksum-verified sforum-latest.sh entry"
done

# OPS8-01：.deployrc 在全新目录里根本不存在。直接对缺失文件跑 sed 会在
# set -euo pipefail 下成为致命失败，而失败点早于 ACTION 分派，--check / install /
# repair 三条分支全部不可达（只打印安装目录就 EXIT=2）。守卫必须写在读取之前。
grep -Fq 'if [ -f "$ROOT_DIR/.deployrc" ]; then' sforum-latest.sh || \
  fail ".deployrc 读取必须带存在性守卫，否则全新目录 --check 会静默 EXIT=2"

# OPS8-01 的端到端回归：空安装目录 + 必然失败的 curl 桩，断言「不再静默」而不是
# 钉死某个退出码。显式 --version 让 resolve_tag 早退（:222 一带），PATH 前置的 curl
# 桩保证任何下载尝试立即失败，所以本用例不依赖网络、gh 或真实 curl。
# 判别式：EXIT != 2，且 stderr 非空、末行带 sforum-latest: 前缀。
ops801_empty_dir_check() {
  local work install status=0 stderr_text last_line reason=""
  work="$(mktemp -d "${TMPDIR:-/tmp}/sforum-latest-ops801.XXXXXX")" || \
    fail "OPS8-01: cannot create a temporary directory"
  install="$work/install"
  if ! { mkdir -p "$work/bin" "$install" &&
         printf '#!/bin/sh\nexit 1\n' >"$work/bin/curl" &&
         chmod +x "$work/bin/curl" &&
         cp sforum-latest.sh "$install/sforum-latest.sh"; }; then
    rm -rf "$work"
    fail "OPS8-01: cannot prepare the fixture directory"
  fi

  (cd "$install" && PATH="$work/bin:$PATH" bash ./sforum-latest.sh --version v3.1.0 --check) \
    >"$work/stdout" 2>"$work/stderr" || status=$?

  stderr_text="$(cat "$work/stderr" 2>/dev/null || true)"
  if [ -n "$stderr_text" ]; then
    last_line="$(printf '%s\n' "$stderr_text" | tail -n1)"
  else
    last_line=""
  fi
  [ "$status" -ne 2 ] || reason="仍然 EXIT=2（静默致命失败）"
  if [ -z "$stderr_text" ]; then
    reason="${reason:+$reason; }stderr 为空，错误被吞掉"
  elif ! printf '%s\n' "$last_line" | grep -q '^sforum-latest: '; then
    reason="${reason:+$reason; }末行缺少 sforum-latest: 前缀：$last_line"
  fi
  rm -rf "$work"
  [ -z "$reason" ] || \
    fail "OPS8-01: 全新目录 --check 未给出明确错误（$reason；EXIT=$status）"
}

ops801_empty_dir_check

# OPS-02：sforum-latest 必须与 deploy.sh / upgrade.sh 同域（同一个 .deploy.lock）。
# 静态哨兵：锁目录名、pid 落盘、usage 里的 --unlock，以及旧锁目录名不再出现。
grep -Fq 'LOCK_DIR="$ROOT_DIR/.deploy.lock"' sforum-latest.sh || \
  fail "sforum-latest.sh must use the same .deploy.lock as deploy.sh"
grep -Fq '> "$LOCK_DIR/pid"' sforum-latest.sh || \
  fail "sforum-latest.sh must write the holder pid into .deploy.lock"
grep -Fq -- '--unlock' sforum-latest.sh || \
  fail "sforum-latest.sh usage must offer --unlock (stale locks only)"
if grep -Fq '.sforum-latest.lock' sforum-latest.sh; then
  fail "sforum-latest.sh still keeps its own .sforum-latest.lock; the lock domain is split"
fi

# 两个可跑用例（离线）：活锁必须拒绝且不发起下载；死锁必须回收后继续。
lock_test_dir="$(mktemp -d "${TMPDIR:-/tmp}/sforum-latest-lock.XXXXXX")"
trap 'rm -rf "$lock_test_dir"' EXIT

make_lock_fixture() {
  local case_dir="$1"
  mkdir -p "$case_dir/bin" "$case_dir/install"
  cat > "$case_dir/bin/curl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$case_dir/curl-calls.log"
exit 1
EOF
  chmod +x "$case_dir/bin/curl"
  cp sforum-latest.sh "$case_dir/install/sforum-latest.sh"
  # 装好的目录一定有 .deployrc：让「回收后继续」真的走到下载，而不是卡在别处。
  printf 'version=v3.0.0\n' > "$case_dir/install/.deployrc"
}

run_latest_check() {
  local case_dir="$1" status=0
  (cd "$case_dir/install" && PATH="$case_dir/bin:$PATH" \
    bash ./sforum-latest.sh --version v3.1.0 --check) \
    >"$case_dir/stdout" 2>"$case_dir/stderr" || status=$?
  printf '%s' "$status"
}

live_case="$lock_test_dir/live"
make_lock_fixture "$live_case"
mkdir "$live_case/install/.deploy.lock"
printf '%s\n' "$$" > "$live_case/install/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=%s\nproc_start=%s\n' "$$" \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  "$(LC_ALL=C ps -o lstart= -p "$$" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')" \
  > "$live_case/install/.deploy.lock/owner"
live_status="$(run_latest_check "$live_case")"
[ "$live_status" -ne 0 ] || fail "OPS-02: sforum-latest kept running while .deploy.lock was live"
grep -Eq '部署操作|deploy\.lock' "$live_case/stderr" || \
  fail "OPS-02: a live .deploy.lock was not reported: $(cat "$live_case/stderr")"
[ ! -e "$live_case/curl-calls.log" ] || \
  fail "OPS-02: the lock was taken after the download; curl already ran"
[ "$(cat "$live_case/install/.deploy.lock/pid")" = "$$" ] || \
  fail "OPS-02: a live .deploy.lock owned by another process was removed"

dead_case="$lock_test_dir/dead"
make_lock_fixture "$dead_case"
dead_pid="$(sh -c 'printf "%s" "$$"')"
mkdir "$dead_case/install/.deploy.lock"
printf '%s\n' "$dead_pid" > "$dead_case/install/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=2020-01-01T00:00:00Z\n' "$dead_pid" \
  > "$dead_case/install/.deploy.lock/owner"
dead_status="$(run_latest_check "$dead_case")"
[ "$dead_status" -ne 0 ] || fail "OPS-02: the failing curl stub was expected to stop the run"
[ -e "$dead_case/curl-calls.log" ] || \
  fail "OPS-02: a stale .deploy.lock was not reclaimed; the run never reached the download"
grep -Fq '下载 SHA256SUMS 失败' "$dead_case/stderr" || \
  fail "OPS-02: the run did not continue past the stale lock: $(cat "$dead_case/stderr")"
[ ! -e "$dead_case/install/.deploy.lock" ] || fail "OPS-02: .deploy.lock was not released"
[ -z "$(find "$dead_case/install" -maxdepth 1 -name '.deploy.lock.stale.*' -print -quit)" ] || \
  fail "OPS-02: a reclaimed .deploy.lock.stale.* copy was left behind"

printf 'validate-sforum-latest.sh: all checks passed\n'
