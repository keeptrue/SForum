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

JOB_LABELS=()
JOB_COMMANDS=()
DEPLOY_JOB_SCRIPTS=()

register_job() {
	local label="$1" command="$2" deploy_script="${3:-}"
	JOB_LABELS+=("$label")
	JOB_COMMANDS+=("$command")
	if [ -n "$deploy_script" ]; then
		DEPLOY_JOB_SCRIPTS+=("$deploy_script")
	fi
}

# 这些命令是实际执行清单，不是注释或旁路副本。RV-14 的 deploy 覆盖检查直接
# 比对下面的登记项，新增测试却没有接入并发队列时会在启动任何任务前失败。
register_job "release script validation" "./scripts/release_test.sh"
register_job "exact main CI release gate validation" "./scripts/ci/verify-main-ci_test.sh"
register_job "release workflow architecture validation" "ruby ./scripts/ci/release_workflow_test.rb"
register_job "automatic release notes validation" "./scripts/ci/generate-release-notes_test.sh"
register_job "release asset packaging validation" "./scripts/ci/release_assets_test.sh"
register_job "anonymous release image pull validation" "./scripts/ci/verify-public-images_test.sh"
register_job "PostgreSQL backup and restore safety validation" "./deploy/scripts/postgres-safety_test.sh" "deploy/scripts/postgres-safety_test.sh"
register_job "release deployment state-machine validation" "./deploy/scripts/deploy_test.sh" "deploy/scripts/deploy_test.sh"
register_job "one-click backup key bootstrap validation" "./deploy/scripts/backup-key_test.sh" "deploy/scripts/backup-key_test.sh"
register_job "deploy lock alignment validation" "./deploy/scripts/deploy-lock_test.sh" "deploy/scripts/deploy-lock_test.sh"
register_job "production Compose migration configuration validation" "./deploy/scripts/compose-production_test.sh" "deploy/scripts/compose-production_test.sh"
register_job "production configure script validation" "./deploy/scripts/configure-production_test.sh" "deploy/scripts/configure-production_test.sh"
register_job "zero-downtime Compose topology validation" "./deploy/scripts/zero-downtime-compose_test.sh" "deploy/scripts/zero-downtime-compose_test.sh"
register_job "zero-downtime release selection validation" "./deploy/scripts/upgrade-version-selection_test.sh" "deploy/scripts/upgrade-version-selection_test.sh"
register_job "verified release bootstrap validation" "./deploy/scripts/bootstrap_test.sh" "deploy/scripts/bootstrap_test.sh"
register_job "release bootstrap image validation" "./deploy/scripts/upgrade-bootstrap-image_test.sh" "deploy/scripts/upgrade-bootstrap-image_test.sh"
register_job "one-line install loader validation" "./deploy/scripts/install-loader_test.sh" "deploy/scripts/install-loader_test.sh"
register_job "zero-downtime state-machine validation" "./deploy/scripts/zero-downtime-state_test.sh" "deploy/scripts/zero-downtime-state_test.sh"
register_job "blue/green slot health probe validation" "./deploy/scripts/upgrade-health-probe_test.sh" "deploy/scripts/upgrade-health-probe_test.sh"
register_job "documentation validation failure-path tests" "./tests/validate-docs_test.sh"
register_job "publishable Web SDK package validation" "node ./scripts/ci/publish-web-sdks_test.mjs && node ./scripts/ci/verify-web-sdks.mjs"
register_job "built-in plugin release version validation" "node tests/validate-builtin-plugin-versions.mjs"
register_job "terminal color layer validation" "./tests/validate-term-ui.sh"
register_job "verified latest-wrapper validation" "./tests/validate-sforum-latest.sh"

echo "Running deploy test script coverage validation..."
for script in deploy/scripts/*_test.sh; do
	[ -e "$script" ] || continue
	found=0
	for registered in "${DEPLOY_JOB_SCRIPTS[@]}"; do
		if [ "$registered" = "$script" ]; then
			found=1
			break
		fi
	done
	[ "$found" = 1 ] ||
		fail "deploy 测试脚本 $script 没有登记到并行执行列表，CI 永远不会跑它"
done

if command -v nproc >/dev/null 2>&1; then
	cores="$(nproc)"
else
	cores="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
fi
job_limit="${SFORUM_RELEASE_TOOLING_JOBS:-$((cores > 4 ? 4 : cores))}"
[[ "$job_limit" =~ ^[1-9][0-9]*$ ]] ||
	fail "SFORUM_RELEASE_TOOLING_JOBS 必须是正整数：$job_limit"

JOB_TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-release-tooling.XXXXXX")"
ACTIVE_PIDS=()
cleanup_jobs() {
	local status=$?
	trap - EXIT INT TERM
	local pid
	for pid in "${ACTIVE_PIDS[@]:-}"; do
		kill "$pid" 2>/dev/null || true
	done
	rm -rf "$JOB_TMP_DIR"
	exit "$status"
}
trap cleanup_jobs EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

total="${#JOB_LABELS[@]}"
echo "Running $total release/deploy tooling jobs with concurrency $job_limit..."
failed=0
declare -A JOB_PID_INDEX=()
next_index=0
active=0
completed=0
while [ "$completed" -lt "$total" ]; do
	while [ "$active" -lt "$job_limit" ] && [ "$next_index" -lt "$total" ]; do
		printf '[%d/%d] %s\n' "$((next_index + 1))" "$total" "${JOB_LABELS[$next_index]}"
		(
			set -o pipefail
			bash -euo pipefail -c "${JOB_COMMANDS[$next_index]}"
		) >"$JOB_TMP_DIR/$next_index.log" 2>&1 &
		pid=$!
		JOB_PID_INDEX["$pid"]="$next_index"
		ACTIVE_PIDS+=("$pid")
		next_index=$((next_index + 1))
		active=$((active + 1))
	done

	status=0
	wait -n || status=$?
	finished_pid=""
	for pid in "${!JOB_PID_INDEX[@]}"; do
		if ! kill -0 "$pid" 2>/dev/null; then
			finished_pid="$pid"
			break
		fi
	done
	[ -n "$finished_pid" ] || fail "无法识别已完成的 release tooling 任务"
	job_index="${JOB_PID_INDEX[$finished_pid]}"
	unset 'JOB_PID_INDEX[$finished_pid]'
	ACTIVE_PIDS=("${!JOB_PID_INDEX[@]}")
	active=$((active - 1))
	completed=$((completed + 1))
	if [ "$status" -eq 0 ]; then
		printf 'PASS %s\n' "${JOB_LABELS[$job_index]}"
	else
		printf 'FAIL %s (exit %s)\n' "${JOB_LABELS[$job_index]}" "$status" >&2
		cat "$JOB_TMP_DIR/$job_index.log" >&2
		failed=1
	fi
done

[ "$failed" = 0 ] || fail "one or more release/deploy tooling jobs failed"
echo "All release/deploy tooling jobs passed."
