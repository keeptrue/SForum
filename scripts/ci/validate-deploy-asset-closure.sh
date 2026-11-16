#!/usr/bin/env bash
# 发布资产闭包门（R7-OPS-01）。三个集合必须一起闭合：
#
#   1) build-deploy-asset.sh 的 SOURCE_FILES —— 打进 sforum-deploy.tar.gz 的文件；
#   2) sforum-bootstrap.sh 的 managed_files() —— 官方安装路径真正提升进实例目录的文件；
#   3) deploy/scripts/*.sh 里的 source "$SCRIPT_DIR/<file>" —— 提升后的脚本运行时依赖。
#
# 为什么必须是静态集合比对：归档里有、提升清单里漏掉的文件不会在安装时报错。它安静地
# 躺在 tar 里，直到操作员在一个已经"装好"的实例上第一次跑 backup/restore，才崩在那条
# source 行上 —— 而且崩在一个本来应该能用的安装上。发布前能拦住它的只有这里。
#
# VERSION 不在白名单之外：build-deploy-asset.sh 打包时用 printf 生成它（仓库里没有这个
# 文件），所以它必然只在提升清单一侧出现，属正常差。反过来，managed_files() 丢掉 VERSION
# 会被下面单独断言 —— 那等于安装时没人再校验归档版本号。
#
# 变异验证：从 managed_files() 里删掉任一条目重跑本门，必须 exit 1 并打印差集。
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

BUILD_SCRIPT="scripts/ci/build-deploy-asset.sh"
BOOTSTRAP_SCRIPT="sforum-bootstrap.sh"
DEPLOY_SCRIPTS_DIR="deploy/scripts"
PACKAGED_ONLY_WHITELIST="VERSION"

fail() {
	printf 'validate-deploy-asset-closure: %s\n' "$1" >&2
	exit 1
}

[ -f "$BUILD_SCRIPT" ] || fail "找不到 $BUILD_SCRIPT"
[ -f "$BOOTSTRAP_SCRIPT" ] || fail "找不到 $BOOTSTRAP_SCRIPT"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-deploy-closure.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

# SOURCE_FILES=( ... ) 的条目，去掉引号、缩进、注释与空行。
archive_files() {
	awk '
		/^SOURCE_FILES=\(/ { collecting = 1; next }
		collecting && /^\)/ { collecting = 0; next }
		collecting {
			line = $0
			sub(/#.*$/, "", line)
			gsub(/["[:space:]]/, "", line)
			if (line != "") print line
		}
	' "$BUILD_SCRIPT"
}

# managed_files() 里 `cat <<'EOF' ... EOF` 的全部条目。
promoted_files() {
	awk '
		$0 == "managed_files() {" { in_function = 1; next }
		in_function && /cat <<.EOF.$/ { in_heredoc = 1; next }
		in_function && in_heredoc && $0 == "EOF" { exit }
		in_heredoc { print }
	' "$BOOTSTRAP_SCRIPT"
}

# 随包分发的 deploy/scripts 脚本里 `source "$SCRIPT_DIR/x"` 的 x，还原成 deploy/scripts/x。
# *_test.sh 不随包分发、并且用 $ROOT_DIR 取源，不参与资产闭包。
# 这里只走 grep + 参数展开：把 $SCRIPT_DIR 写成 [$]SCRIPT_DIR，避免单引号里出现 $NAME
# 被 shellcheck 判成「本想展开却写进了单引号」（SC2016）——本文件与 bootstrap_test.sh
# 是同一条断言的两处实现，两边都得过 CI 的 lint-shell 门。
sourced_dependencies() {
	local script line name
	for script in "$DEPLOY_SCRIPTS_DIR"/*.sh; do
		[ -f "$script" ] || continue
		case "$script" in
		*_test.sh) continue ;;
		esac
		while IFS= read -r line; do
			name="${line##*SCRIPT_DIR/}"
			name="${name%%\"*}"
			[ -n "$name" ] || continue
			printf '%s/%s\n' "$DEPLOY_SCRIPTS_DIR" "$name"
		done < <(grep -E '^[[:space:]]*(source|\.)[[:space:]]+"[$]SCRIPT_DIR/' "$script" || true)
	done | sort -u
}

archive_files | sort -u >"$WORK_DIR/archive.txt"
promoted_files | sort -u >"$WORK_DIR/promoted.txt"
sourced_dependencies | sort -u >"$WORK_DIR/sourced.txt"

archive_count="$(wc -l <"$WORK_DIR/archive.txt" | tr -d '[:space:]')"
promoted_count="$(wc -l <"$WORK_DIR/promoted.txt" | tr -d '[:space:]')"
sourced_count="$(wc -l <"$WORK_DIR/sourced.txt" | tr -d '[:space:]')"

# 解析不出条目 = 门恒真。宁可红，也不给一条永远绿的断言。
[ "$archive_count" -gt 0 ] ||
	fail "从 $BUILD_SCRIPT 的 SOURCE_FILES 解析出 0 个条目，解析逻辑已失效"
[ "$promoted_count" -gt 0 ] ||
	fail "从 $BOOTSTRAP_SCRIPT 的 managed_files() 解析出 0 个条目，解析逻辑已失效"
[ "$sourced_count" -gt 0 ] ||
	fail "在 $DEPLOY_SCRIPTS_DIR/*.sh 里解析出 0 个 source \"\$SCRIPT_DIR/...\" 依赖，解析逻辑已失效（若确实不再有同级依赖，请删除这条断言并说明原因）"

comm -23 "$WORK_DIR/archive.txt" "$WORK_DIR/promoted.txt" >"$WORK_DIR/archive-not-promoted.txt"
comm -13 "$WORK_DIR/archive.txt" "$WORK_DIR/promoted.txt" |
	grep -vxF -- "$PACKAGED_ONLY_WHITELIST" >"$WORK_DIR/promoted-not-archived.txt" || true
comm -23 "$WORK_DIR/sourced.txt" "$WORK_DIR/promoted.txt" >"$WORK_DIR/sourced-not-promoted.txt"
comm -23 "$WORK_DIR/sourced.txt" "$WORK_DIR/archive.txt" >"$WORK_DIR/sourced-not-archived.txt"

grep -qxF -- "$PACKAGED_ONLY_WHITELIST" "$WORK_DIR/promoted.txt" ||
	fail "managed_files() 不再提升 $PACKAGED_ONLY_WHITELIST，安装时就没有人校验归档版本号了"

report_diff() {
	local label="$1" file="$2"
	if [ -s "$file" ]; then
		printf '  %s: %s 条差集\n' "$label" "$(wc -l <"$file" | tr -d '[:space:]')"
		sed 's/^/    - /' "$file"
		return 1
	fi
	printf '  %s: 无差集\n' "$label"
	return 0
}

printf 'Deploy asset closure: archive=%s promoted=%s sourced=%s\n' \
	"$archive_count" "$promoted_count" "$sourced_count"

status=0
report_diff "归档 − 提升（打包了但从不提升）" "$WORK_DIR/archive-not-promoted.txt" || status=1
report_diff "提升 − 归档（除 $PACKAGED_ONLY_WHITELIST 外都必须真的在归档里）" "$WORK_DIR/promoted-not-archived.txt" || status=1
report_diff "source − 提升（被 source 的库没被提升）" "$WORK_DIR/sourced-not-promoted.txt" || status=1
report_diff "source − 归档（被 source 的库没被打包）" "$WORK_DIR/sourced-not-archived.txt" || status=1

if [ "$status" -ne 0 ]; then
	fail "发布资产闭包不成立：官方安装路径产出的实例会在第一次 backup/restore 时崩在 source 行上"
fi

printf 'Deploy asset closure verified: %s archived files are all promoted, every promoted file is archived, and all %s sourced libraries ship with them.\n' \
	"$archive_count" "$sourced_count"
