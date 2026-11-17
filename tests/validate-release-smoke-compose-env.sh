#!/usr/bin/env bash
# shellcheck disable=SC2016
# 解析 compose 文件清单时，正则里匹配的是 smoke 脚本里的**字面量** `$ROOT_DIR`，
# 本脚本不展开它，因此整文件关掉 "Expressions don't expand in single quotes"。
# 静态守卫：compose 里的 `${VAR:?}` 硬闸必须全部由 release-smoke.sh 提供的非空环境满足。
#
# 为什么需要这条门（2026-11-18 v3.1.1 发布）：compose.prod.yaml 给 MEILI_MASTER_KEY 加了
# `:?` 硬闸（4b4352d48），release-smoke.sh 的 export 清单没跟上。docker compose 在任何
# 子命令之前都先给整份文件做插值，于是 release workflow 的 smoke job 在 `compose pull`
# 的第一步就炸，而 main CI 全绿 —— 因为这条断言当时只存在于 release workflow 里。
# 「只在 release workflow 里跑的校验等于没有校验」。
#
# 判据（三件事，缺一条这门就会静默变绿）：
#   1. compose 文件清单取自 release-smoke.sh 自己喂给 docker compose 的 `-f` 清单，不写死
#      文件名；解析面只认规范的 `-f "$ROOT_DIR/<file>"`（空白可有可无、引号可有可无），
#      而**计数面**认 `-f` 与 `--file` 两种写法、不限定后面跟什么。两者按行逐处比对，
#      不等即红并打印行号：否则把 `-f` 写成 `--file` 或 `-f"$ROOT_DIR/x"` 会让解析数与
#      计数一起缩水、断言恒相等，那份文件的硬闸就再也没人检查。
#   2. 只看「有没有 export」不够：`${VAR:?}` 对空串同样拒绝，所以 `export VAR=` 必须红。
#   3. compose 里的注释不参与匹配：注释掉的旧硬闸不该把门弄红（误报会让人关掉门）。
# 只解析文本，不起容器、不读 .env、不联网。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

SMOKE_SCRIPT="scripts/ci/release-smoke.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[ -f "$SMOKE_SCRIPT" ] || fail "找不到 $SMOKE_SCRIPT"

# 去掉 `#` 及其后内容，但**保留行号**（只改行内内容，不删行），这样后面 `grep -n`
# 报的仍然是原文件行号；剥完变空的行自然不会产生匹配。
strip_comments() {
  sed -E 's/#.*$//' -- "$1"
}

# 计数面：任何 `-f` / `--file` 都进分母（不限定后面跟什么）。
FLAG_RE='(^|[[:space:]])(-f|--file)[[:space:]]*'
# 解析面：只认规范写法 —— `-f` 与路径之间必须有空白（`-f"$ROOT_DIR/x"` 是 pflag 式的
# 贴写，本门不猜，直接判为未识别），路径必须是 `$ROOT_DIR/` 开头。
PATH_RE='(^|[[:space:]])-f[[:space:]]+"?\$ROOT_DIR/[^"[:space:]]+'

mapfile -t flag_hits < <(grep -noE "$FLAG_RE" <(strip_comments "$SMOKE_SCRIPT") || true)
mapfile -t path_hits < <(grep -noE "$PATH_RE" <(strip_comments "$SMOKE_SCRIPT") || true)

declare -A flag_per_line=()
declare -A path_per_line=()
for hit in "${flag_hits[@]:-}"; do
  [ -n "$hit" ] || continue
  line_number="${hit%%:*}"
  flag_per_line["$line_number"]=$(( ${flag_per_line["$line_number"]:-0} + 1 ))
done
for hit in "${path_hits[@]:-}"; do
  [ -n "$hit" ] || continue
  line_number="${hit%%:*}"
  path_per_line["$line_number"]=$(( ${path_per_line["$line_number"]:-0} + 1 ))
done

total_flags=0
for line_number in "${!flag_per_line[@]}"; do
  total_flags=$(( total_flags + flag_per_line["$line_number"] ))
done
[ "$total_flags" -gt 0 ] ||
  fail "在 $SMOKE_SCRIPT 里找不到任何 -f/--file，守卫自身失效（compose 清单来源变了？）"

unrecognized=0
while IFS= read -r line_number; do
  [ -n "$line_number" ] || continue
  flags="${flag_per_line["$line_number"]:-0}"
  parsed="${path_per_line["$line_number"]:-0}"
  if (( flags != parsed )); then
    echo "UNRECOGNIZED $SMOKE_SCRIPT:$line_number 有 $flags 处 -f/--file，只认出 $parsed 处 \"\$ROOT_DIR/<compose 文件>\"" >&2
    unrecognized=$(( unrecognized + 1 ))
  fi
done < <(printf '%s\n' "${!flag_per_line[@]}" | sort -n)
if (( unrecognized > 0 )); then
  fail "$SMOKE_SCRIPT 里有 $unrecognized 行 -f/--file 写法本门认不出来。
      compose 文件清单必须全部写成 -f \"\$ROOT_DIR/<file>\"：其它写法（--file、-f\"\$ROOT_DIR/x\"）
      会让那份文件不进检查。请改成规范写法 —— 不要放宽本门的识别面。"
fi

# compose 文件集合：按脚本里出现的顺序去重，每个都必须真的存在。
compose_files=()
for hit in "${path_hits[@]:-}"; do
  [ -n "$hit" ] || continue
  candidate="${hit#*\$ROOT_DIR/}"
  [ -n "$candidate" ] || continue
  if [ "${#compose_files[@]}" -gt 0 ] &&
    printf '%s\n' "${compose_files[@]}" | grep -qxF -- "$candidate"; then
    continue
  fi
  [ -f "$candidate" ] || fail "$SMOKE_SCRIPT 引用的 compose 文件不存在：$candidate"
  compose_files+=("$candidate")
done
[ "${#compose_files[@]}" -gt 0 ] || fail "守卫自身失效：认出了 -f，却没解出任何 compose 文件"

# 脚本导出的变量：`export VAR=value` 记值（要判空），`export VAR` 视为非空 —— 后者是
# 导出脚本前面已经赋值的变量（例如 SFORUM_VERSION="${1:-}"，而且脚本自己会先校验它非空）；
# 静态解析看不到那次赋值，把裸 export 判空会造成误报。
declare -A export_value=()
declare -A export_bare=()
while IFS= read -r line; do
  [[ "$line" =~ ^[[:space:]]*export[[:space:]]+([A-Za-z_][A-Za-z0-9_]*)(=(.*))?$ ]] || continue
  name="${BASH_REMATCH[1]}"
  if [ -n "${BASH_REMATCH[2]:-}" ]; then
    export_value["$name"]="${BASH_REMATCH[3]:-}"
  else
    export_bare["$name"]=1
  fi
done < <(strip_comments "$SMOKE_SCRIPT")

[ "${#export_value[@]}" -gt 0 ] || fail "在 $SMOKE_SCRIPT 里解析不到任何 export，守卫自身失效"

declared() {
  [ -n "${export_value[$1]+x}" ] || [ -n "${export_bare[$1]+x}" ]
}

# `${VAR:?}` 拒绝的是空串：`""` 与 `''` 去掉引号后就是空串，和什么都没写等价。
is_empty_value() {
  local value="$1"
  case "$value" in
    \"*\") value="${value#\"}"; value="${value%\"}" ;;
    \'*\') value="${value#\'}"; value="${value%\'}" ;;
  esac
  [ -z "$value" ]
}

missing=0
empty=0
checked=0
for compose_file in "${compose_files[@]}"; do
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    line_number="${hit%%:*}"
    token="${hit#*:}"
    variable="$(sed -E 's/^\$\{([A-Za-z_][A-Za-z0-9_]*).*/\1/' <<< "$token")"
    checked=$(( checked + 1 ))
    if ! declared "$variable"; then
      echo "MISSING $variable required by $compose_file:$line_number" >&2
      missing=$(( missing + 1 ))
    elif [ -n "${export_value[$variable]+x}" ] && is_empty_value "${export_value[$variable]}"; then
      echo "EMPTY $variable required by $compose_file:$line_number（export 给了空串，\${VAR:?} 同样拒绝）" >&2
      empty=$(( empty + 1 ))
    fi
  done < <(grep -noE '\$\{[A-Za-z_][A-Za-z0-9_]*(:\?|\?)' <(strip_comments "$compose_file") || true)
done

if (( missing > 0 || empty > 0 )); then
  echo "FAIL: $(( missing + empty )) 处 compose 硬闸变量没有被 $SMOKE_SCRIPT 提供可用值（共检查 $checked 处）" >&2
  echo "      compose 硬闸与 $SMOKE_SCRIPT 的 export 清单必须同行：补 export/补值，或去掉硬闸。" >&2
  exit 1
fi

echo "OK: $checked 处 compose 硬闸变量全部由 $SMOKE_SCRIPT 的 export 满足（${compose_files[*]}）"
