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
# 不按引号剥字符串是有意的：`echo "usage: -f <file>"` 这类文档字符串会被计数面算成未识别
# 而红（fail-closed），但若为了放过它去剥引号内内容，真正的 `-f "$ROOT_DIR/..."` 代码行
# 也会被一起剥掉，门就彻底瞎了。代价是遇到这种字符串要改措辞，失败消息里会提示。
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
  echo "      若这是文档/usage 字符串里的 -f（例如 echo \"usage: -f <file>\"），请改写措辞，例如 -f ＜file＞；" >&2
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

# 脚本导出的变量：`export VAR=value` 记值（要判空）；裸 `export VAR` 不直接算非空。
# 它只有在**脚本真的给 VAR 赋过值**时才算非空 —— 例如 SFORUM_VERSION="${1:-}"（脚本还会
# 先校验它非空）。裸导出一个从没赋值过的变量，运行时就是空串，`${VAR:?}` 照样拒绝：
# 把它一律当非空，等于给「把 export 值删掉」留一条静默变绿的路，那正是本门存在的理由。
# 赋值行只认 `[export ]VAR=...` 这一种写法；认不出就红（fail-closed），需要时再扩判据。
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

[ $(( ${#export_value[@]} + ${#export_bare[@]} )) -gt 0 ] ||
  fail "在 $SMOKE_SCRIPT 里既没有任何 export VAR=value，也没有任何裸 export VAR，守卫自身失效"

assigned_names="$(strip_comments "$SMOKE_SCRIPT" |
  sed -nE 's/^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=.*/\2/p' | sort -u)"
has_assignment() {
  grep -qxF -- "$1" <<< "$assigned_names"
}

declared() {
  [ -n "${export_value[$1]+x}" ] || [ -n "${export_bare[$1]+x}" ]
}

# `${VAR:?}` 拒绝的是空串，而 shell 语义下「等号后只有空白」就是空串：`export X= ` 与
# `export X=` 完全等价。所以先右裁尾部空白，再去成对引号，最后判空；只裁尾部，
# 值内部的空格必须留着（`export X="a b"`、`export X=a b` 都是非空）。
is_empty_value() {
  local value="$1"
  value="${value%"${value##*[![:space:]]}"}"
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
    elif [ -n "${export_bare[$variable]+x}" ] && ! has_assignment "$variable"; then
      echo "EMPTY $variable required by $compose_file:$line_number（只有裸 export $variable，脚本前面从未给它赋值 → 运行时是空串）" >&2
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
