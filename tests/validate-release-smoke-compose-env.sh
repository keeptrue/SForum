#!/usr/bin/env bash
# shellcheck disable=SC2016
# 解析 compose 文件清单时，正则里匹配的是 smoke 脚本里的**字面量** `$ROOT_DIR`，
# 本脚本不展开它，因此整文件关掉 "Expressions don't expand in single quotes"。
# 静态守卫：compose 里的 `${VAR:?}` 硬闸必须全部由 release-smoke.sh 提供的环境满足。
#
# 为什么需要这条门（2026-11-18 v3.1.1 发布）：compose.prod.yaml 给 MEILI_MASTER_KEY 加了
# `:?` 硬闸（4b4352d48），release-smoke.sh 的 export 清单没跟上。docker compose 在任何
# 子命令之前都先给整份文件做插值，于是 release workflow 的 smoke job 在 `compose pull`
# 的第一步就炸，而 main CI 全绿 —— 因为这条断言当时只存在于 release workflow 里。
# 「只在 release workflow 里跑的校验等于没有校验」。
#
# 判据：从 release-smoke.sh 自己喂给 docker compose 的 `-f` 清单取 compose 文件（不写死
# 文件名），把每个 `${VAR:?...}` / `${VAR?...}` 的变量名与脚本里的 `export VAR` 集合比对；
# 缺一个就红，并打印变量名与它的 compose 文件:行。只解析文本，不起容器、不读 .env、不联网。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

SMOKE_SCRIPT="scripts/ci/release-smoke.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[ -f "$SMOKE_SCRIPT" ] || fail "找不到 $SMOKE_SCRIPT"

# smoke 脚本实际使用的 compose 文件集合。取自 `-f "$ROOT_DIR/<file>"` 行，脚本换组合时
# 这条门自动跟上，不留下会过期的硬编码文件名清单。带引号与不带引号两种写法都认。
compose_files=()
while IFS= read -r candidate; do
  [ -n "$candidate" ] || continue
  [ -f "$candidate" ] || fail "$SMOKE_SCRIPT 引用的 compose 文件不存在：$candidate"
  compose_files+=("$candidate")
done < <(grep -oE '\-f[[:space:]]+"?\$ROOT_DIR/[^"[:space:]]+' "$SMOKE_SCRIPT" |
  sed -E 's/^-f[[:space:]]+"?\$ROOT_DIR\///')
[ "${#compose_files[@]}" -gt 0 ] ||
  fail "在 $SMOKE_SCRIPT 里解析不到任何 -f \"\$ROOT_DIR/<compose 文件>\"，守卫自身失效"

# 上面只认 `-f [\"]$ROOT_DIR/<file>` 一种写法。若脚本里出现本解析器不认识的 -f
# （绝对路径、变量间接、`--file "$ROOT_DIR/..."`），清单会被**静默**少读一个文件：
# 门变绿，而 docker compose 照样会为那个文件插值并炸。所以断言 -f 的出现次数与解析
# 到的文件数相等 —— 不一致就红，并要求把新写法并进解析器，绝不放宽判据。
f_flag_count="$(grep -oE '(^|[[:space:]])-f[[:space:]]' "$SMOKE_SCRIPT" | wc -l | tr -d ' ')"
if (( f_flag_count != ${#compose_files[@]} )); then
  fail "$SMOKE_SCRIPT 里有 $f_flag_count 处 -f，但只解析出 ${#compose_files[@]} 个 compose 文件。
      compose 文件清单必须全部来自 -f \"\$ROOT_DIR/<file>\"：其余写法请并入本脚本的解析器
      （而不是跳过），否则那个文件的 \${VAR:?} 硬闸不会被本门检查。"
fi

# 脚本导出的变量名。`export VAR=value` 与 `export VAR` 两种写法都算：后者用于导出脚本
# 前面已经赋值的变量（例如 SFORUM_VERSION）。
exported_vars="$(grep -oE '^[[:space:]]*export[[:space:]]+[A-Za-z_][A-Za-z0-9_]*' "$SMOKE_SCRIPT" |
  awk '{print $NF}' | sort -u)"
[ -n "$exported_vars" ] || fail "在 $SMOKE_SCRIPT 里解析不到任何 export，守卫自身失效"

has_export() {
  grep -qxF -- "$1" <<< "$exported_vars"
}

missing=0
checked=0
for compose_file in "${compose_files[@]}"; do
  while IFS= read -r hit; do
    line_number="${hit%%:*}"
    token="${hit#*:}"
    variable="$(sed -E 's/^\$\{([A-Za-z_][A-Za-z0-9_]*).*/\1/' <<< "$token")"
    checked=$((checked + 1))
    if ! has_export "$variable"; then
      echo "MISSING $variable required by $compose_file:$line_number" >&2
      missing=$((missing + 1))
    fi
  done < <(grep -noE '\$\{[A-Za-z_][A-Za-z0-9_]*(:\?|\?)' "$compose_file" || true)
done

if (( missing > 0 )); then
  echo "FAIL: $missing 处 compose 硬闸变量没有被 $SMOKE_SCRIPT 提供（共检查 $checked 处）" >&2
  echo "      compose 硬闸与 $SMOKE_SCRIPT 的 export 清单必须同行：补 export，或去掉硬闸。" >&2
  exit 1
fi

echo "OK: $checked 处 compose 硬闸变量全部由 $SMOKE_SCRIPT 的 export 满足（${compose_files[*]}）"
