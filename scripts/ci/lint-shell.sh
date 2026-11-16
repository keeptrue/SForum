#!/usr/bin/env bash
# shellcheck shell=bash
# Shell gate for the deployment toolchain: deploy.sh, upgrade.sh, the root
# sforum-*.sh entrypoints, and every helper under deploy/scripts/.
#
# Exit 0 only when shellcheck reports nothing outside the checked-in baseline.
# A missing shellcheck binary is a hard failure: a lint gate that silently skips
# itself is worse than no gate. A baseline entry that no longer matches anything
# is also a failure: the ratchet must shrink with the debt, not rot.
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

BASELINE="$ROOT_DIR/scripts/ci/shellcheck-baseline.txt"

resolve_shellcheck() {
  local candidate="${SFORUM_SHELLCHECK_BIN:-}"
  if [ -z "$candidate" ]; then
    candidate="$(command -v shellcheck || true)"
  fi
  if [ -z "$candidate" ] || [ ! -x "$candidate" ]; then
    cat >&2 <<'EOF'
lint-shell.sh: shellcheck is required but was not found.

Install shellcheck (https://www.shellcheck.net) or set SFORUM_SHELLCHECK_BIN to
the binary, for example:
    SFORUM_SHELLCHECK_BIN=/tmp/shellcheck-v0.10.0/shellcheck bash scripts/ci/lint-shell.sh

This gate fails closed on purpose: a missing linter must not look like a pass.
EOF
    exit 1
  fi
  printf '%s' "$candidate"
}

SHELLCHECK_BIN="$(resolve_shellcheck)"

# 版本钉死（pin 在 scripts/shellcheck-version.txt）。baseline 对诊断集合是版本敏感
# 的：新版可能新增诊断，runner 镜像一漂移就变成「没人改代码，门突然红」。所以这里
# 先比对版本串，不一致立刻失败，并同时打印 pin 与本地值 —— 让人知道该改哪一边。
#
# pin 缺失或为空同样是失败，不是「跳过检查」：一个能因为文件不见了就自动关掉的
# 版本断言，等于把保险丝装进抽屉。三种失败各自给出修复办法。
PIN_FILE="$ROOT_DIR/scripts/shellcheck-version.txt"
if [ ! -f "$PIN_FILE" ]; then
  cat >&2 <<'EOF'
lint-shell.sh: the shellcheck version pin file is missing.
    expected: scripts/shellcheck-version.txt

Recreate it with a single line, matching the version CI uses:
    version=0.10.0
EOF
  exit 1
fi

PINNED_VERSION="$(sed -n 's/^version=//p' "$PIN_FILE" | tail -n 1)"
if [ -z "$PINNED_VERSION" ]; then
  cat >&2 <<'EOF'
lint-shell.sh: scripts/shellcheck-version.txt has no usable version= line.

It must contain exactly one line of the form:
    version=0.10.0
EOF
  exit 1
fi

ACTUAL_VERSION="$("$SHELLCHECK_BIN" --version 2>/dev/null | sed -n 's/^version:[[:space:]]*//p' | head -n 1)"
if [ "$ACTUAL_VERSION" != "$PINNED_VERSION" ]; then
  cat >&2 <<EOF
lint-shell.sh: shellcheck version mismatch.
    pin (scripts/shellcheck-version.txt): ${PINNED_VERSION}
    local (${SHELLCHECK_BIN}): ${ACTUAL_VERSION:-unknown}

The baseline is version sensitive, so CI and local runs must agree. Install the
pinned version, or bump the pin in the same reviewed change (and re-baseline if
the new version reports different diagnostics).
EOF
  exit 1
fi

FILES=(deploy.sh upgrade.sh scripts/ci/lint-shell.sh)
for entry in sforum-*.sh; do
  [ -f "$entry" ] && FILES+=("$entry")
done
for entry in deploy/scripts/*.sh; do
  [ -f "$entry" ] && FILES+=("$entry")
done

# Baseline entries are "<file> <SCxxxx>" pairs. They exist only for files this
# change is not allowed to modify; anything else must be fixed, not recorded.
baseline_has() {
  [ -f "$BASELINE" ] || return 1
  awk -v file="$1" -v code="$2" '
    /^[[:space:]]*#/ { next }
    NF >= 2 && $1 == file && $2 == code { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$BASELINE"
}

if output="$("$SHELLCHECK_BIN" -x -f gcc "${FILES[@]}" 2>&1)"; then
  shellcheck_status=0
else
  shellcheck_status=$?
fi

failures=()
parsed=0
suppressed=0
while IFS= read -r line; do
  [ -n "$line" ] || continue
  file="${line%%:*}"
  code="$(sed -n 's/.*\[\(SC[0-9][0-9]*\)\]$/\1/p' <<< "$line")"
  [ -z "$code" ] || parsed=$((parsed + 1))
  if [ -n "$code" ] && baseline_has "$file" "$code"; then
    suppressed=$((suppressed + 1))
    continue
  fi
  failures+=("$line")
done <<< "$output"

if [ "${#failures[@]}" -gt 0 ]; then
  printf '%s\n' "${failures[@]}" >&2
  printf 'lint-shell.sh: %d shellcheck diagnostic(s) outside the baseline.\n' "${#failures[@]}" >&2
  exit 1
fi

if [ "$shellcheck_status" -ne 0 ] && [ "$parsed" -eq 0 ]; then
  # The linter failed without a parseable diagnostic (bad option, unreadable
  # file, broken installation). Never translate that into a pass.
  printf 'lint-shell.sh: shellcheck exited %d without a parseable diagnostic.\n' "$shellcheck_status" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi

if [ "$suppressed" -gt 0 ]; then
  printf 'lint-shell.sh: suppressed %d pre-existing diagnostic(s) recorded in scripts/ci/shellcheck-baseline.txt\n' "$suppressed"
fi

# Baseline entries are a ratchet: once the underlying diagnostic is fixed, the
# entry must be deleted in the same change. A stale entry fails the gate.
baseline_stale=0
if [ -f "$BASELINE" ]; then
  while IFS= read -r entry; do
    case "$entry" in
      ''|'#'*) continue ;;
    esac
    # shellcheck disable=SC2086
    set -- $entry
    if [ "$#" -lt 2 ]; then
      printf 'lint-shell.sh: malformed baseline entry: %s\n' "$entry" >&2
      baseline_stale=1
      continue
    fi
    if ! grep -q "^$1:.*\[$2\]$" <<< "$output"; then
      printf 'lint-shell.sh: baseline entry "%s %s" no longer matches any diagnostic; remove it.\n' "$1" "$2" >&2
      baseline_stale=1
    fi
  done < "$BASELINE"
fi
[ "$baseline_stale" -eq 0 ] || exit 1

printf 'lint-shell.sh: %d scripts clean under shellcheck\n' "${#FILES[@]}"
