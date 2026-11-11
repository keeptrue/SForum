#!/usr/bin/env bash
# Run database tests on owned clusters, never on inherited development URLs.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# go list / go test / go run 都必须在 API 模块内执行；分片选包发生在建集群之前。
cd "$ROOT_DIR/apps/api"
command -v docker >/dev/null
command -v go >/dev/null
docker info >/dev/null

owner="$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
image="${SFORUM_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
# 固定宿主端口让 DATABASE_URL 跨运行逐字节一致。Go 会把测试通过 os.Getenv
# 读到的环境变量值算进结果缓存键，随机端口会让每个数据库包每次运行都重跑
# （实测 45 个包、662 秒）。端口只当缓存键用：集群仍然每次新建并销毁，
# 端口被占用时回退到临时端口，只损失缓存、不损失正确性。
# 分片 i 使用 preferred_port + i：各分片缓存键互不相同，且跨运行稳定。
preferred_port="${SFORUM_TEST_POSTGRES_PORT:-55432}"
cid=""
container=""
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$cid" ]]; then
    local actual
    if ! actual="$(docker inspect --format '{{index .Config.Labels "com.sforum.test-owner"}}' "$cid")"; then
      echo 'Inspecting isolated test container cleanup failed.' >&2
      exit 1
    fi
    if [[ "$actual" != "$owner" ]]; then
      echo 'Test container ownership changed; cleanup stopped.' >&2
      exit 1
    fi
    if ! docker rm -fv "$cid" >/dev/null; then
      echo 'Removing isolated test container failed.' >&2
      exit 1
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 一次性集群，关掉持久化只影响这台用完即删的测试库（实测三项 GUC 均已生效）。
start_cluster() {
  docker run -d --name "$container" --label "com.sforum.test-owner=$owner" \
    -e POSTGRES_DB=sforum_test_suite -e POSTGRES_USER=sforum -e POSTGRES_PASSWORD=sforum \
    -e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 \
    -p "$1" "$image" \
    -c fsync=off -c synchronous_commit=off -c full_page_writes=off
}

# 幂等：同一个进程最多建一个集群，重复调用直接复用。
provision_cluster() {
  if [[ -n "$cid" ]]; then
    return 0
  fi
  container="sforum-test-api-${owner}"
  if ! cid="$(start_cluster "127.0.0.1:${preferred_port}:5432" 2>/dev/null)"; then
    docker rm -f "$container" >/dev/null 2>&1 || true
    echo "端口 ${preferred_port} 已被占用，回退临时端口（本轮测试缓存不会命中）。" >&2
    cid="$(start_cluster "127.0.0.1::5432")"
  fi
  # postgres 官方镜像首次启动会跑 initdb 然后重启一次；pg_isready 在重启前的
  # 临时实例上就可能返回 0，紧接着的 psql 会撞上 shutdown。用真实认证连接做
  # 就绪判定，并要求连续两次成功，避开首次启动的重启窗口。
  local ready=0 stable=0
  for _ in {1..60}; do
    if docker exec -e PGPASSWORD=sforum "$cid" psql -U sforum -d sforum_test_suite -tAc 'SELECT 1' >/dev/null 2>&1; then
      stable=$((stable + 1))
      if [[ "$stable" == 2 ]]; then
        ready=1
        break
      fi
    else
      stable=0
    fi
    sleep 1
  done
  if [[ "$ready" != 1 ]]; then
    docker logs "$cid" >&2
    exit 1
  fi
  docker exec -e PGPASSWORD=sforum "$cid" psql -U sforum -d postgres -v ON_ERROR_STOP=1 \
    -c "COMMENT ON DATABASE postgres IS 'sforum.disposable-test-cluster.v1';" >/dev/null
  local binding port
  binding="$(docker port "$cid" 5432/tcp)"
  if [[ "$binding" != 127.0.0.1:* || "$binding" == *$'\n'* ]]; then
    echo 'Unexpected isolated PostgreSQL port binding.' >&2
    exit 1
  fi
  port="${binding##*:}"
  export DATABASE_URL="postgres://sforum:sforum@127.0.0.1:${port}/sforum_test_suite?sslmode=disable"
  export SFORUM_TEST_DATABASE_URL="$DATABASE_URL"
  export SFORUM_COMPAT_DATABASE_URL="$DATABASE_URL"
  printf 'Disposable test cluster: %s, database: sforum_test_suite, port: %s\n' "$container" "$port"
  go run ./cmd/migrate
}

# 并行度：分片之间物理隔离，分片内部保持 -p 1 的串行语义。
# 为什么不是「一个 database、-p N」：包虽然靠 schema 隔离数据，但跑完整迁移链的包
# 会同时动 database 级共享状态（public schema 上的固定对象、集群级角色、一次性
# genesis 发布租约）。M0 实测 -p 8 下会互相踩出四类失败：
#   extension database physical resource conflict /
#   lifecycle operation already in progress /
#   could not open relation with OID ... /
#   core database authority conflicts with the Host ownership boundary。
# 因此并行改为「多个一次性集群」：分片之间没有任何共享数据库状态，分片内部与既有
# CI 的串行执行完全一致。依据与验证记录见
# knowledge/plans/2026-10-23-test-suite-speedup.md。
if command -v nproc >/dev/null 2>&1; then
  cores="$(nproc)"
else
  cores="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
fi
shards="${SFORUM_TEST_SHARDS:-$((cores > 4 ? 4 : cores))}"
shard_parallel="${SFORUM_TEST_PARALLEL:-1}"

mode="test"
passthrough=()
shard_spec=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --compat)
      mode="compat"
      shift
      ;;
    --with-compat)
      mode="with-compat"
      shift
      ;;
    --shard)
      mode="shard"
      shift
      if [[ $# -eq 0 ]]; then
        echo '--shard 需要 i/n 形式的分片规格。' >&2
        exit 2
      fi
      shard_spec="$1"
      shift
      ;;
    *)
      passthrough+=("$1")
      shift
      ;;
  esac
done

# 参数原样透传给 go test：只把字面量 ./... 换成当前分片的包清单，
# 其余（含 -run/-timeout 的取值）保持相邻顺序，不做 flag/包路径猜测。
if [[ ${#passthrough[@]} -eq 0 ]]; then
  passthrough=(./...)
fi
full_run=0
for arg in "${passthrough[@]}"; do
  if [[ "$arg" == "./..." ]]; then
    full_run=1
    break
  fi
done

# 分片按包名序号轮转分配，确定性且与包数量无关。
select_shard_packages() {
  local index="$1" total="$2" i=0 pkg
  while IFS= read -r pkg; do
    if ((i % total == index)); then
      printf '%s\n' "$pkg"
    fi
    i=$((i + 1))
  done < <(go list ./...)
}

run_shard() {
  local index="$1" total="$2" arg
  local -a selected=() forwarded=()
  mapfile -t selected < <(select_shard_packages "$index" "$total")
  if [[ ${#selected[@]} == 0 ]]; then
    echo "shard ${index}/${total} 没有分配到包。" >&2
    return 1
  fi
  for arg in "${passthrough[@]}"; do
    if [[ "$arg" != "./..." ]]; then
      forwarded+=("$arg")
    fi
  done
  echo "shard ${index}/${total}: ${#selected[@]} 个包，-p ${shard_parallel}"
  provision_cluster
  # -count=1 必须在透传参数**之前**：默认的测试缓存会让「上一版的绿」冒充这一版的绿，
  # 而本脚本的输出常被当作证据（M8 验收、基线、变异对照）。调用方仍可自行覆盖。
  go test -count=1 -p "$shard_parallel" "${forwarded[@]}" "${selected[@]}"
}

run_shards() {
  local total="$1" index status=0 failures=0 arg
  local -a pids=() forwarded=()
  for arg in "${passthrough[@]}"; do
    if [[ "$arg" != "./..." ]]; then
      forwarded+=("$arg")
    fi
  done
  for ((index = 0; index < total; index++)); do
    (
      SFORUM_TEST_POSTGRES_PORT="$((preferred_port + index))" \
        "$ROOT_DIR/scripts/test-api-isolated.sh" --shard "${index}/${total}" "${forwarded[@]}"
    ) &
    pids+=($!)
  done
  for index in "${!pids[@]}"; do
    status=0
    wait "${pids[$index]}" || status=$?
    if [[ $status != 0 ]]; then
      echo "shard ${index}/${total} 失败（exit ${status}）。" >&2
      failures=$((failures + 1))
    fi
  done
  if [[ $failures -gt 0 ]]; then
    return 1
  fi
}

run_compat_farm() {
  local arg
  local -a farm_args=()
  for arg in "${passthrough[@]}"; do
    if [[ "$arg" != "./..." ]]; then
      farm_args+=("$arg")
    fi
  done
  (
    cd "$ROOT_DIR/tests/compat"
    go run . "${farm_args[@]}"
  )
}

case "$mode" in
  compat)
    provision_cluster
    run_compat_farm
    ;;
  shard)
    if [[ ! "$shard_spec" =~ ^[0-9]+/[0-9]+$ ]]; then
      echo '--shard 需要 i/n 形式（例如 0/4）。' >&2
      exit 2
    fi
    shard_index="${shard_spec%%/*}"
    shard_total="${shard_spec##*/}"
    if ((shard_total < 1 || shard_index >= shard_total)); then
      echo '--shard 的 i 必须小于 n，且 n ≥ 1。' >&2
      exit 2
    fi
    run_shard "$shard_index" "$shard_total"
    ;;
  test | with-compat)
    if [[ $full_run == 1 ]]; then
      run_shards "$shards"
    else
      # 显式包路径（含 -run 定位）保持单次调用，不分片。
      # -count=1 同上：证据脚本不允许返回缓存结果。
      provision_cluster
      go test -count=1 -p "$shard_parallel" "${passthrough[@]}"
    fi
    if [[ "$mode" == "with-compat" ]]; then
      echo 'Running V3 P12 compatibility farm (required cells; skip/missing fail)...'
      provision_cluster
      run_compat_farm
    fi
    ;;
esac
