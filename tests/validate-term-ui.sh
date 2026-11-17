#!/usr/bin/env bash
# 终端配色层门（一键安装/更新链路）。离线：不联网、不碰 Docker、不起任何服务。
#
# 断言：
#   a) 奇偶：deploy/scripts/term-ui.sh 与三份内联副本的 `# >>> term-ui` … `# <<< term-ui`
#      区间逐字节相同；deploy.sh / upgrade.sh 只 source 那一份，不内联第二套实现。
#   b) 开关矩阵：5 个入口各跑一次 `--nope`（必然 die），验证 SFORUM_COLOR / NO_COLOR /
#      重定向三者的实际优先级。
#   c) TTY 自动判定：有 `script` 时在 pty 里跑一次，必须自己上色。
#   d) 行首锚定：`sforum-latest: ` 仍是首行首字节（tests/validate-sforum-latest.sh 依赖它）。
#   e) 帮助文本：5 个入口的 --help 在 SFORUM_COLOR=always 下不得含 ESC。
#   f) 落盘洁净：deploy_test.sh 仍断言 .deployrc 不含 ESC 字节。
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ENTRIES=(deploy.sh upgrade.sh sforum-install.sh sforum-bootstrap.sh sforum-latest.sh)
INLINE_ENTRIES=(sforum-install.sh sforum-bootstrap.sh sforum-latest.sh)
CANONICAL="deploy/scripts/term-ui.sh"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-term-ui.XXXXXX")"
cleanup() { rm -rf "$TEMP_DIR"; }
trap cleanup EXIT

fail() {
  printf 'validate-term-ui.sh: %s\n' "$1" >&2
  exit 1
}

has_esc() { grep -q $'\033' "$1"; }

extract_block() {
  awk '/^# >>> term-ui$/{p=1} p{print} /^# <<< term-ui$/{p=0}' "$1"
}

# 环境变量全部显式清空再按需注入：调用方 shell 里的 NO_COLOR / SFORUM_COLOR / TERM
# 不能让这条门自己漂移。空数组用 ${arr[@]+...} 展开，macOS 自带 bash 3.2 在 set -u
# 下展开空数组会报 unbound variable。
run_entry() {
  local err_file="$1"
  shift
  local assignments=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    assignments+=("$1")
    shift
  done
  [ "$#" -eq 0 ] || shift
  local entry="$1"
  ENTRY_STATUS=0
  (
    cd "$ROOT_DIR"
    # TERM 显式设成非 dumb：某些环境（含本仓库的 CI 容器）在 TERM 缺失时会回落成
    # dumb，那会让「默认该上色」的用例假红。用例自己的 TERM=dumb 写在后面，仍然生效
    # （env 的重复赋值以最后一个为准）。
    env -u SFORUM_COLOR -u NO_COLOR TERM=xterm \
      ${assignments[@]+"${assignments[@]}"} \
      bash "$entry" --nope
  ) >"$TEMP_DIR/stdout" 2>"$err_file" || ENTRY_STATUS=$?
}

# a) 奇偶 --------------------------------------------------------------------
echo "Running terminal color block parity validation..."
extract_block "$CANONICAL" > "$TEMP_DIR/canonical.block"
[ -s "$TEMP_DIR/canonical.block" ] || \
  fail "$CANONICAL 里没有 # >>> term-ui 标记块"
grep -qxF -- '# >>> term-ui' "$TEMP_DIR/canonical.block" || \
  fail "$CANONICAL 的标记块缺少起始行"
grep -qxF -- '# <<< term-ui' "$TEMP_DIR/canonical.block" || \
  fail "$CANONICAL 的标记块缺少结束行"
grep -qF -- 'ui_init_color' "$TEMP_DIR/canonical.block" || \
  fail "$CANONICAL 的标记块没有 ui_init_color"
for entry in "${INLINE_ENTRIES[@]}"; do
  [ -f "$entry" ] || fail "$entry 不存在"
  count="$(grep -c '^# >>> term-ui$' "$entry" || true)"
  [ "$count" = "1" ] || fail "$entry 的 # >>> term-ui 标记出现 $count 次（必须恰好 1 次）"
  extract_block "$entry" > "$TEMP_DIR/inline.block"
  cmp -s "$TEMP_DIR/canonical.block" "$TEMP_DIR/inline.block" || {
    printf 'validate-term-ui.sh: %s 的内联配色块与 %s 不一致\n' "$entry" "$CANONICAL" >&2
    diff -u "$TEMP_DIR/canonical.block" "$TEMP_DIR/inline.block" >&2 || true
    exit 1
  }
done
# deploy.sh / upgrade.sh 在安装目录里执行，deploy/scripts 一定存在：只许 source，
# 不许内联第二份实现（内联就会绕开这条奇偶断言）。
for entry in deploy.sh upgrade.sh; do
  grep -qF -- "source \"\$ROOT_DIR/deploy/scripts/term-ui.sh\"" "$entry" || \
    fail "$entry 必须 source \"\$ROOT_DIR/deploy/scripts/term-ui.sh\""
  if grep -q '^# >>> term-ui$' "$entry"; then
    fail "$entry 内联了配色块；它只 source 唯一实现"
  fi
done
# 三个助手脚本用 $SCRIPT_DIR 取同目录库（资产闭包门据此判定它们随包分发）。
for entry in deploy/scripts/configure-production.sh deploy/scripts/wait-for-health.sh deploy/scripts/restore-postgres.sh; do
  grep -qF -- 'source "$SCRIPT_DIR/term-ui.sh"' "$entry" || \
    fail "$entry 必须 source \"\$SCRIPT_DIR/term-ui.sh\""
done

# b) 开关矩阵 ----------------------------------------------------------------
echo "Running terminal color switch matrix validation..."
for entry in "${ENTRIES[@]}"; do
  slug="$(printf '%s' "$entry" | tr -c 'a-zA-Z0-9' '-')"

  run_entry "$TEMP_DIR/$slug-always.err" SFORUM_COLOR=always -- "$entry"
  [ "$ENTRY_STATUS" -ne 0 ] || fail "$entry --nope 应当失败（这条矩阵靠 die 取输出）"
  has_esc "$TEMP_DIR/$slug-always.err" || \
    fail "$entry: SFORUM_COLOR=always 时 stderr 必须带 ESC"
  grep -qF -- '[x]' "$TEMP_DIR/$slug-always.err" || \
    fail "$entry: SFORUM_COLOR=always 时错误行必须带 [x] 语义标记"

  run_entry "$TEMP_DIR/$slug-never.err" SFORUM_COLOR=never -- "$entry"
  [ "$ENTRY_STATUS" -ne 0 ] || fail "$entry --nope 应当失败"
  if has_esc "$TEMP_DIR/$slug-never.err"; then
    fail "$entry: SFORUM_COLOR=never 时 stderr 不得带 ESC"
  fi

  run_entry "$TEMP_DIR/$slug-default.err" -- "$entry"
  [ "$ENTRY_STATUS" -ne 0 ] || fail "$entry --nope 应当失败"
  if has_esc "$TEMP_DIR/$slug-default.err"; then
    fail "$entry: 输出被重定向且未显式要求时不得上色（CI 日志必须干净）"
  fi

  # NO_COLOR 是显式退出开关：它压过 SFORUM_COLOR=always（见 term-ui.sh 的判定顺序）。
  run_entry "$TEMP_DIR/$slug-nocolor.err" SFORUM_COLOR=always NO_COLOR=1 -- "$entry"
  [ "$ENTRY_STATUS" -ne 0 ] || fail "$entry --nope 应当失败"
  if has_esc "$TEMP_DIR/$slug-nocolor.err"; then
    fail "$entry: NO_COLOR=1 必须压过 SFORUM_COLOR=always"
  fi

  # TERM=dumb 同样是不上色的终局信号。
  run_entry "$TEMP_DIR/$slug-dumb.err" TERM=dumb SFORUM_COLOR=always -- "$entry"
  [ "$ENTRY_STATUS" -ne 0 ] || fail "$entry --nope 应当失败"
  if has_esc "$TEMP_DIR/$slug-dumb.err"; then
    fail "$entry: TERM=dumb 时必须不上色"
  fi
done

# c) TTY 自动判定 -------------------------------------------------------------
echo "Running terminal color tty auto-detection validation..."
detect_pty() {
  if script -qec true /dev/null >/dev/null 2>&1; then
    PTY_STYLE="util-linux"
    return 0
  fi
  if script -q /dev/null true >/dev/null 2>&1; then
    PTY_STYLE="bsd"
    return 0
  fi
  return 1
}

pty_exec() {
  if [ "$PTY_STYLE" = "util-linux" ]; then
    script -qec "$(printf '%q ' "$@")" /dev/null
  else
    script -q /dev/null "$@"
  fi
}

if detect_pty; then
  # pty 里 stdout 是终端，所以没有 SFORUM_COLOR 也必须自己上色。
  PTY_STATUS=0
  (
    cd "$ROOT_DIR"
    unset SFORUM_COLOR NO_COLOR
    TERM=xterm
    export TERM
    pty_exec bash sforum-latest.sh --nope
  ) >"$TEMP_DIR/pty.out" 2>&1 || PTY_STATUS=$?
  [ "$PTY_STATUS" -ne 0 ] || fail "pty 里的 --nope 应当失败"
  has_esc "$TEMP_DIR/pty.out" || \
    fail "pty 里未显式设置 SFORUM_COLOR 时也必须自动上色（-t 1 判定失效）"
else
  printf 'validate-term-ui.sh: skipping tty auto-detection (no `script` with -e/-c support)\n'
fi

# d) 行首锚定 ----------------------------------------------------------------
echo "Running machine prefix line-anchor validation..."
run_entry "$TEMP_DIR/anchor.err" SFORUM_COLOR=always -- sforum-latest.sh
[ "$ENTRY_STATUS" -ne 0 ] || fail "sforum-latest.sh --nope 应当失败"
first_line="$(head -n 1 "$TEMP_DIR/anchor.err")"
case "$first_line" in
  'sforum-latest: '*) ;;
  *) fail "sforum-latest.sh 的 stderr 首行不再以 'sforum-latest: ' 开头：$first_line" ;;
esac
# 前缀之后才允许出现 ESC：前缀本身必须是无色字面量。
case "$first_line" in
  'sforum-latest: '*$'\033'*) ;;
  *) fail "SFORUM_COLOR=always 时前缀之后的正文没有上色：$first_line" ;;
esac

# e) 帮助文本 ----------------------------------------------------------------
echo "Running help output plain-text validation..."
for entry in "${ENTRIES[@]}"; do
  slug="$(printf '%s' "$entry" | tr -c 'a-zA-Z0-9' '-')"
  HELP_STATUS=0
  (
    cd "$ROOT_DIR"
    env -u NO_COLOR TERM=xterm SFORUM_COLOR=always bash "$entry" --help
  ) >"$TEMP_DIR/$slug-help.out" 2>&1 || HELP_STATUS=$?
  [ "$HELP_STATUS" -eq 0 ] || fail "$entry --help 应当成功（退出码 $HELP_STATUS）"
  if has_esc "$TEMP_DIR/$slug-help.out"; then
    fail "$entry --help 必须保持纯文本，不得含 ESC"
  fi
done

# f) 落盘洁净 ----------------------------------------------------------------
echo "Running on-disk state cleanliness validation..."
grep -qF 'grep -q $'"'"'\033'"'"' "$TEST_ROOT/.deployrc"' deploy/scripts/deploy_test.sh || \
  fail "deploy/scripts/deploy_test.sh 不再断言 .deployrc 不含 ESC 字节"
grep -qF -- '.deployrc contains an ANSI escape byte' deploy/scripts/deploy_test.sh || \
  fail "deploy/scripts/deploy_test.sh 缺少 .deployrc ESC 断言的可读失败信息"

printf 'validate-term-ui.sh: all checks passed\n'
