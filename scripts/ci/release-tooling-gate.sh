#!/usr/bin/env bash
# Release/deploy tooling gate: validates the release pipeline itself rather
# than product code. Runs in release-tooling.yml and as part of local test.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

WORKFLOW_PATH_COVERAGE_TARGET=".github/workflows/release-tooling.yml"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# 取出 workflow 某个触发块（pull_request / push）的 paths 条目，去掉 `- ` 前缀。
trigger_paths() {
	awk -v trigger="$1" '
		$0 ~ ("^  " trigger ":[[:space:]]*$") { in_trigger = 1; next }
		in_trigger && /^  [A-Za-z_]+:[[:space:]]*$/ { in_trigger = 0; in_paths = 0 }
		in_trigger && /^    paths:[[:space:]]*$/ { in_paths = 1; next }
		in_paths && /^      - / { sub(/^      - /, ""); print; next }
		in_paths && !/^      / { in_paths = 0 }
	' "$WORKFLOW_PATH_COVERAGE_TARGET"
}

# RV-14 防漂移：仓库根的每个 *.sh 都必须在两个触发块的 paths 里**显式**登记。
# 用显式条目而不是 `*.sh` 通配，是因为通配会让这条断言恒真、也就永远测不出
# 「新脚本忘了登记」——显式清单 + 这条断言才构成闸门。deploy.sh / upgrade.sh /
# sforum-bootstrap.sh / sforum-install.sh 此前就漏在清单外，改它们不触发任何 CI，
# 而 gate 里正跑着 deploy_test.sh、upgrade-health-probe_test.sh 等。
echo "Running release workflow path coverage validation..."
for trigger in pull_request push; do
	entries="$(trigger_paths "$trigger")"
	[ -n "$entries" ] || fail "$WORKFLOW_PATH_COVERAGE_TARGET 的 $trigger 触发块没有 paths 列表"
	for script in *.sh; do
		[ -e "$script" ] || continue
		grep -qxF -- "$script" <<<"$entries" ||
			fail "RV-14: 根脚本 $script 未登记到 $trigger 触发块的 paths，改动它不会触发本工作流"
	done
done

# RV-14 的第二处同形状缺口：deploy/scripts/*_test.sh 曾经是手工列进本 gate 的，
# 目录里 12 个、清单里 9 个，三个（含本轮新增的两个）永远不会在 CI 里跑——
# 测试存在、本地绿、CI 从不执行，删掉被测行为也发现不了。判据只断言「目录里的
# 每个 *_test.sh 都在本脚本里被**执行**」：不比对数量（两边同时改就绕过了），
# 也不认注释里的路径（只认 `./path` 形式的调用行）。
echo "Running deploy test script coverage validation..."
gate_self="${BASH_SOURCE[0]}"
for script in deploy/scripts/*_test.sh; do
	[ -e "$script" ] || continue
	grep -qE "^\./${script}([[:space:]]|$)" "$gate_self" ||
		fail "deploy 测试脚本 $script 没有在本 gate 里被执行，CI 永远不会跑它"
done

# R7-OPS-01：归档、提升、source 三个集合必须一起闭合。发布包里打了、而 bootstrap 的
# managed_files() 从不提升的文件不会让安装报错 —— 它只是安静地留在包里，直到操作员在
# 已经"装好"的实例上第一次跑 backup/restore 才崩在 source 行上。同族缺口的另一半
# （deploy/scripts 的 source 依赖有没有被打进归档）由 release_assets_test.sh 覆盖，
# 这里补的是「有没有被提升到实例目录」这一侧；两支门互为补集，都不许被关掉。
echo "Running deploy asset closure validation (archive -> promoted -> sourced libraries)..."
./scripts/ci/validate-deploy-asset-closure.sh

# RV-18：发布工具链必须三处同源——go.mod 的 go 指令、Dockerfile 的 golang 基础
# 镜像、govulncheck 的扫描工具链。任何一处单独漂移，CI 扫的就不是我们发布的那个
# toolchain：go1.26.10 上 govulncheck 报 1 条 called stdlib 漏洞（GO-2026-6859，
# 修于 1.26.11），而 `stable` 扫出来 0 条——补丁状态在 CI 里结构性不可见。
echo "Running release toolchain parity validation..."
go_directive() {
	awk '$1 == "go" { print $2; exit }' apps/api/go.mod
}
docker_toolchain() {
	awk '/^FROM golang:/ { sub(/^FROM golang:/, ""); sub(/-alpine.*$/, ""); print; exit }' apps/api/Dockerfile
}
scan_toolchain() {
	awk -F': *' '/^[[:space:]]*go-version-input:/ { print $2; exit }' .github/workflows/security.yml | tr -d "'\""
}

go_version="$(go_directive)"
docker_version="$(docker_toolchain)"
scan_version="$(scan_toolchain)"

[ -n "$go_version" ] || fail "apps/api/go.mod 没有 go 指令"
[ -n "$docker_version" ] || fail "apps/api/Dockerfile 没有 golang 基础镜像"
[ -n "$scan_version" ] || fail "security.yml 的 govulncheck 没有 go-version-input"
[ "$scan_version" != "stable" ] || fail "RV-18: govulncheck 用 stable，扫的不是发布工具链；钉到 apps/api/go.mod 的 go 指令"
[ "$docker_version" = "$go_version" ] ||
	fail "RV-18: Dockerfile 基础镜像 golang:$docker_version != go.mod 的 go $go_version"
[ "$scan_version" = "$go_version" ] ||
	fail "RV-18: govulncheck 扫描工具链 $scan_version != go.mod 的 go $go_version"

# 同一族不变量：所有 tracked go.mod 的 go 指令必须一致。插件模块会拿**自己**的
# go 行去钉 GOTOOLCHAIN（app/Support/ExtensionPackage/dev_build.go:127），一旦低于
# 它依赖的 apps/api，零命令插件回路当场断在 "requires go >= x"。
manifest_count=0
while IFS= read -r manifest; do
	declared="$(awk '$1 == "go" { print $2; exit }' "$manifest")"
	manifest_count=$((manifest_count + 1))
	[ "$declared" = "$go_version" ] ||
		fail "go.mod 工具链不一致：$manifest 声明 go $declared，apps/api/go.mod 声明 go $go_version"
done < <(git ls-files | grep 'go\.mod$')
[ "$manifest_count" -gt 0 ] || fail "找不到任何 tracked go.mod，无法校验工具链一致性"
echo "Toolchain parity: go $go_version across $manifest_count go.mod, Dockerfile, and govulncheck."

echo "Running release script validation..."
./scripts/release_test.sh

echo "Running exact main CI release gate validation..."
./scripts/ci/verify-main-ci_test.sh

echo "Running release workflow architecture validation..."
ruby ./scripts/ci/release_workflow_test.rb

echo "Running automatic release notes validation..."
./scripts/ci/generate-release-notes_test.sh

echo "Running release asset packaging validation..."
./scripts/ci/release_assets_test.sh

echo "Running anonymous release image pull validation..."
./scripts/ci/verify-public-images_test.sh

echo "Running PostgreSQL backup and restore safety validation..."
./deploy/scripts/postgres-safety_test.sh

echo "Running release deployment state-machine validation..."
./deploy/scripts/deploy_test.sh

echo "Running deploy lock alignment validation..."
./deploy/scripts/deploy-lock_test.sh

echo "Running production Compose migration configuration validation..."
./deploy/scripts/compose-production_test.sh

echo "Running production configure script validation..."
./deploy/scripts/configure-production_test.sh

echo "Running zero-downtime Compose topology validation..."
./deploy/scripts/zero-downtime-compose_test.sh

echo "Running zero-downtime release selection validation..."
./deploy/scripts/upgrade-version-selection_test.sh

echo "Running verified release bootstrap validation..."
./deploy/scripts/bootstrap_test.sh

echo "Running release bootstrap image validation..."
./deploy/scripts/upgrade-bootstrap-image_test.sh

echo "Running one-line install loader validation..."
./deploy/scripts/install-loader_test.sh

echo "Running zero-downtime state-machine validation..."
./deploy/scripts/zero-downtime-state_test.sh

echo "Running blue/green slot health probe validation..."
./deploy/scripts/upgrade-health-probe_test.sh

echo "Running documentation validation failure-path tests..."
./tests/validate-docs_test.sh

echo "Running publishable Web SDK package validation..."
node ./scripts/ci/publish-web-sdks_test.mjs
node ./scripts/ci/verify-web-sdks.mjs

echo "Running built-in plugin release version validation..."
node tests/validate-builtin-plugin-versions.mjs

echo "Running verified latest-wrapper validation..."
./tests/validate-sforum-latest.sh
