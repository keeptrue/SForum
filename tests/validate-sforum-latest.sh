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

# 防漂移（静态奇偶）：两份实现手写同一份元数据格式，任一侧改了另一侧必须同改。
for peer in sforum-latest.sh deploy/scripts/deploy-lock.sh; do
  grep -Fq "printf 'pid=%s\naction=%s\nstarted_at=%s\nproc_start=%s\n'" "$peer" || \
    fail "$peer no longer writes the shared owner metadata format"
  grep -Fq 'sed -n "s/^$1=//p"' "$peer" || \
    fail "$peer no longer reads owner metadata the same way as the other implementation"
done

# 可跑用例（全部离线）：--check 不持锁、非 --check 活锁拒绝、死锁回收后继续、
# 以及「库写的锁 sforum-latest 认不认」的行为交叉验证。
lock_test_dir="$(mktemp -d "${TMPDIR:-/tmp}/sforum-latest-lock.XXXXXX")"
holder_pid=""
cleanup_lock_tests() {
  if [ -n "$holder_pid" ]; then
    kill -9 "$holder_pid" 2>/dev/null || true
  fi
  rm -rf "$lock_test_dir"
}
trap cleanup_lock_tests EXIT

make_lock_fixture() {
  local case_dir="$1"
  mkdir -p "$case_dir/bin" "$case_dir/install"
  # 桩记录两件事：有没有走下载；走到下载的那一刻 .deploy.lock 是否存在。
  cat > "$case_dir/bin/curl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$case_dir/curl-calls.log"
[ -e "$case_dir/install/.deploy.lock" ] && printf 'yes\n' > "$case_dir/lock-at-download"
exit 1
EOF
  chmod +x "$case_dir/bin/curl"
  cp sforum-latest.sh "$case_dir/install/sforum-latest.sh"
  # 非 --check 会真正进入升级分支：缺 .env.production 时 detect_action 判 incomplete 直接退出。
  printf 'version=v3.0.0\n' > "$case_dir/install/.deployrc"
  printf 'APP_URL=http://127.0.0.1:3000\n' > "$case_dir/install/.env.production"
}

run_latest() {
  local case_dir="$1" status=0
  shift
  (cd "$case_dir/install" && PATH="$case_dir/bin:$PATH" \
    bash ./sforum-latest.sh --version v3.1.0 "$@") \
    >"$case_dir/stdout" 2>"$case_dir/stderr" || status=$?
  printf '%s' "$status"
}

# A) --check 是只读预检：必须走到下载，且下载期间不得持有排他锁。
check_case="$lock_test_dir/check"
make_lock_fixture "$check_case"
run_latest "$check_case" --check >/dev/null
[ -e "$check_case/curl-calls.log" ] || \
  fail "OPS-02: --check never reached the download stage: $(cat "$check_case/stderr")"
[ ! -e "$check_case/lock-at-download" ] || \
  fail "OPS-02: --check held .deploy.lock while downloading; it would block a real deployment"
[ ! -e "$check_case/install/.deploy.lock" ] || fail "OPS-02: --check left .deploy.lock behind"

# B) 非 --check + 活锁：必须在发起下载之前拒绝。
live_case="$lock_test_dir/live"
make_lock_fixture "$live_case"
mkdir "$live_case/install/.deploy.lock"
printf '%s\n' "$$" > "$live_case/install/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=%s\nproc_start=%s\n' "$$" \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  "$(LC_ALL=C ps -o lstart= -p "$$" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')" \
  > "$live_case/install/.deploy.lock/owner"
run_latest "$live_case" >/dev/null
[ ! -e "$live_case/curl-calls.log" ] || \
  fail "OPS-02: a live .deploy.lock did not stop the run before the download"
grep -Fq '.deploy.lock 被占用' "$live_case/stderr" || \
  fail "OPS-02: a live .deploy.lock was not reported: $(cat "$live_case/stderr")"
if grep -Fq '下载 SHA256SUMS 失败' "$live_case/stderr"; then
  fail "OPS-02: the live-lock case actually reached the download instead of being refused"
fi
[ "$(cat "$live_case/install/.deploy.lock/pid")" = "$$" ] || \
  fail "OPS-02: a live .deploy.lock owned by another process was removed"

# C) 非 --check + 死 pid：回收陈旧锁后继续（用同一根桩证明探测器不空转）。
dead_case="$lock_test_dir/dead"
make_lock_fixture "$dead_case"
dead_pid="$(sh -c 'printf "%s" "$$"')"
mkdir "$dead_case/install/.deploy.lock"
printf '%s\n' "$dead_pid" > "$dead_case/install/.deploy.lock/pid"
printf 'pid=%s\naction=upgrade\nstarted_at=2020-01-01T00:00:00Z\n' "$dead_pid" \
  > "$dead_case/install/.deploy.lock/owner"
run_latest "$dead_case" >/dev/null
grep -Fq '已回收陈旧锁' "$dead_case/stderr" || \
  fail "OPS-02: a stale .deploy.lock was not reclaimed: $(cat "$dead_case/stderr")"
grep -Fq '下载 SHA256SUMS 失败' "$dead_case/stderr" || \
  fail "OPS-02: the run did not continue past the stale lock: $(cat "$dead_case/stderr")"
[ -e "$dead_case/lock-at-download" ] || \
  fail "OPS-02: the lock probe never fired; the non-check path must hold .deploy.lock while downloading"
[ ! -e "$dead_case/install/.deploy.lock" ] || fail "OPS-02: .deploy.lock was not released"
[ -z "$(find "$dead_case/install" -maxdepth 1 -name '.deploy.lock.stale.*' -print -quit)" ] || \
  fail "OPS-02: a reclaimed .deploy.lock.stale.* copy was left behind"

# D) 行为交叉：用官方库真持一把锁，sforum-latest 必须认得出它（格式漂移就会漏）。
cross_case="$lock_test_dir/cross"
make_lock_fixture "$cross_case"
(
  die() { printf 'holder: %s\n' "$1" >&2; exit 1; }
  DEPLOY_LOCK_ROOT="$cross_case/install"
  lock_text() { printf '%s' "$1"; }
  # shellcheck source=deploy/scripts/deploy-lock.sh
  . deploy/scripts/deploy-lock.sh
  acquire_deploy_lock upgrade >/dev/null 2>&1
  printf 'ready\n' > "$cross_case/holder-ready"
  # exec 让子 shell 直接变成 sleep：kill $! 才能一并收掉它，否则会漏一个后台 sleep。
  exec sleep 60
) &
holder_pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -e "$cross_case/holder-ready" ] && break
  sleep 0.2
done
[ -e "$cross_case/holder-ready" ] || fail "OPS-02: the deploy-lock.sh holder never acquired the lock"
run_latest "$cross_case" >/dev/null || true
[ ! -e "$cross_case/curl-calls.log" ] || \
  fail "OPS-02: a .deploy.lock written by deploy-lock.sh did not stop sforum-latest"
grep -Fq 'state=alive' "$cross_case/stderr" || \
  fail "OPS-02: sforum-latest could not read the holder metadata written by deploy-lock.sh"
kill -9 "$holder_pid" 2>/dev/null || true
wait "$holder_pid" 2>/dev/null || true
holder_pid=""

printf 'validate-sforum-latest.sh: all checks passed\n'
