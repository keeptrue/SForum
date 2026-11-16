#!/usr/bin/env bash
# Run database tests on owned disposable PostgreSQL + Redis containers, never on
# inherited development URLs or a shared Redis.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# go list / go test / go run 都必须在 API 模块内执行；分片选包发生在建集群之前。
cd "$ROOT_DIR/apps/api"
command -v docker >/dev/null
command -v go >/dev/null
docker info >/dev/null

owner="$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
image="${SFORUM_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
# Redis 与 PostgreSQL 同生命周期：有测试用 SFORUM_TEST_REDIS_ADDR 做 gate，
# 只给 PG 会让这些用例在 CI 里恒为 skip（Lua/PTTL 这类只有真 Redis 才红的
# 行为等于没测）。默认镜像跟随 compose.yaml 的生产版本（同一 tag，同大版本、
# alpine 变体），保证测试口径与部署口径一致；换镜像用 SFORUM_TEST_REDIS_IMAGE。
redis_image="${SFORUM_TEST_REDIS_IMAGE:-redis:8.10.2-alpine}"
# 固定宿主端口让 DATABASE_URL 跨运行逐字节一致。Go 会把测试通过 os.Getenv
# 读到的环境变量值算进结果缓存键，随机端口会让每个数据库包每次运行都重跑
# （实测 45 个包、662 秒）。端口只当缓存键用：集群仍然每次新建并销毁，
# 端口被占用时回退到临时端口，只损失缓存、不损失正确性。
# 分片 i 使用 preferred_port + i：各分片缓存键互不相同，且跨运行稳定。
preferred_port="${SFORUM_TEST_POSTGRES_PORT:-55432}"
# Redis 同理：固定端口让 SFORUM_TEST_REDIS_ADDR 跨运行一致，缓存才命中。
redis_preferred_port="${SFORUM_TEST_REDIS_PORT:-56379}"
cid=""
container=""
redis_cid=""
redis_container=""

# Core 兼容版本的测试 pin（门禁确定性，不是产品行为）。
#
# 为什么要 pin：开发构建的 Core 兼容版本由 `git describe --tags --long --dirty`
# 在**运行时**派生（apps/api/version/version.go 的 probeDevelopmentCoreVersion），
# 所以它随 HEAD（提交计数 + 短 sha）和工作区脏状态一起漂移。而测试库是两段式：
# 先 `go run ./cmd/migrate` 把当时的版本写进 public.sforum_core_runtime_state，
# 再由各测试二进制读回来、和自己此刻算出的版本比。只要这两段之间 HEAD 前进或
# 回退（本机多 agent 并发提交）、或者工作区由脏变干净，两边必然不等，
# TestPostgresExtensionDatabaseHighRiskLeaseObservesMigratedCoreFence 就会随机红
# —— 实测过 3.1.0+111.g658b6a503.dirty（迁移时刻）对 3.1.0+117.g9395e73f8.dirty
# （测试运行时刻）。
#
# pin 成什么：最近可达的 v* 标签（`git describe --abbrev=0`），也就是这条代码线
# 声明的发行版；标签不可达时退回本次 `git describe` 的完整读数。两种情况都只作用于
# 测试二进制与测试库的 migrate，生产构建仍由发布流程用 -ldflags 注入 version.Current。
version_pkg="github.com/keeptrue/sforum/apps/api/version"
pinned_core_version="${SFORUM_TEST_PINNED_VERSION:-}"
if [[ -z "$pinned_core_version" ]]; then
  if tag="$(git describe --tags --match 'v[0-9]*' --abbrev=0 2>/dev/null)" && [[ -n "$tag" ]]; then
    pinned_core_version="${tag#v}"
  else
    describe="$(git describe --tags --match 'v[0-9]*' --long --dirty --always 2>/dev/null || true)"
    dirty_suffix=""
    if [[ "$describe" == *-dirty ]]; then
      dirty_suffix=".dirty"
      describe="${describe%-dirty}"
    fi
    if [[ "${describe#v}" =~ ^(.+)-([0-9]+)-g([0-9a-f]+)$ ]]; then
      pinned_core_version="${BASH_REMATCH[1]}+${BASH_REMATCH[2]}.g${BASH_REMATCH[3]}${dirty_suffix}"
    else
      echo "无法从 git 派生 Core 兼容版本（describe=${describe:-<empty>}）：用 SFORUM_TEST_PINNED_VERSION 显式指定，否则迁移记录的版本会与测试二进制漂移。" >&2
      exit 1
    fi
  fi
fi
export SFORUM_TEST_PINNED_VERSION="$pinned_core_version"
# 数组形式保证 -ldflags 与它的值永远成对出现，透传参数不会把它们拆开。
version_ldflags=(-ldflags "-X ${version_pkg}.Current=${pinned_core_version}")

# 只删自己创建的容器：先核对 label 上的 owner，再 rm -fv。
# 并发跑本脚本的另一个进程（或别人手动起的容器）即使撞了名字也不动手。
remove_owned_container() {
  local target="$1" actual
  if ! actual="$(docker inspect --format '{{index .Config.Labels "com.sforum.test-owner"}}' "$target")"; then
    echo 'Inspecting isolated test container cleanup failed.' >&2
    exit 1
  fi
  if [[ "$actual" != "$owner" ]]; then
    echo 'Test container ownership changed; cleanup stopped.' >&2
    exit 1
  fi
  if ! docker rm -fv "$target" >/dev/null; then
    echo 'Removing isolated test container failed.' >&2
    exit 1
  fi
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$redis_cid" ]]; then
    remove_owned_container "$redis_cid"
  fi
  if [[ -n "$cid" ]]; then
    remove_owned_container "$cid"
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
  go run "${version_ldflags[@]}" ./cmd/migrate
}

# probe_redis_port 在**宿主侧**用 RESP 打一次 PING，走的就是测试连接的发布端口。
# 放进子 shell：/dev/tcp 连接失败会让非交互 shell 直接退出，这里只要它返回非零。
probe_redis_port() {
  ( exec 3<>"/dev/tcp/127.0.0.1/$1" || exit 1
    printf '*1\r\n$4\r\nPING\r\n' >&3 || exit 1
    IFS= read -r -t 2 reply <&3 || exit 1
    reply="${reply%$'\r'}"
    [[ "$reply" == "+PONG" ]] ) 2>/dev/null
}

# 一次性 Redis：不开持久化、不设口令（测试直接用 Addr 连，redis.Options 里
# 没有 Password）。每个脚本进程一个实例，分片之间因此物理隔离，不存在共享
# DB 索引导致的键串味。
start_redis() {
  docker run -d --name "$redis_container" --label "com.sforum.test-owner=$owner" \
    -p "$1" "$redis_image" \
    redis-server --save '' --appendonly no
}

# 幂等：同一个进程最多建一个 Redis，重复调用直接复用。
provision_redis() {
  if [[ -n "$redis_cid" ]]; then
    return 0
  fi
  redis_container="sforum-test-api-redis-${owner}"
  if ! redis_cid="$(start_redis "127.0.0.1:${redis_preferred_port}:6379" 2>/dev/null)"; then
    docker rm -f "$redis_container" >/dev/null 2>&1 || true
    # 首选端口起不来有两种原因：端口被占（回退只损失缓存）或镜像不可用
    # （下面这次重试会把 docker 的真实报错带出来，再补一条明确的中文结论）。
    echo "以首选端口 ${redis_preferred_port} 启动一次性 Redis 失败，回退临时端口（端口被占用时本轮测试缓存不会命中）。" >&2
    if ! redis_cid="$(start_redis "127.0.0.1::6379")"; then
      echo "无法启动一次性 Redis（镜像 ${redis_image}），请检查 SFORUM_TEST_REDIS_IMAGE 与 docker。" >&2
      exit 1
    fi
  fi
  local binding port
  binding="$(docker port "$redis_cid" 6379/tcp)"
  if [[ "$binding" != 127.0.0.1:* || "$binding" == *$'\n'* ]]; then
    echo 'Unexpected isolated Redis port binding.' >&2
    exit 1
  fi
  port="${binding##*:}"
  # 与 PG 同思路的就绪判定：真实 PING + 连续两次成功，避开 redis 首次启动窗口。
  # 但 PING 必须走**发布端口**（测试用的就是这条路），不能用 docker exec：
  # exec 只证明容器内进程活着，宿主到容器的端口映射规则比容器启动稍晚生效，
  # 那一小段窗口里 exec 已经 PONG、宿主的第一个命令却连不上。实测踩到过：
  # 首个 EVAL(INCR) 因连接失败被 fail-open 吞掉，用例报「键不存在」，红得像
  # 产品 bug。exec 作为容器级前置检查保留，两者都连续两次成功才算就绪。
  local ready=0 stable=0
  for _ in {1..60}; do
    if [[ "$(docker exec "$redis_cid" redis-cli ping 2>/dev/null)" == "PONG" ]] &&
      probe_redis_port "$port"; then
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
    docker logs "$redis_cid" >&2
    exit 1
  fi
  export SFORUM_TEST_REDIS_ADDR="127.0.0.1:${port}"
  printf 'Disposable test Redis: %s, address: %s\n' "$redis_container" "$SFORUM_TEST_REDIS_ADDR"
}

# 每条执行路径（单包、分片、compat）都只调一次：PG 与 Redis 同生同灭。
provision_backends() {
  provision_cluster
  provision_redis
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
  provision_backends
  # -count=1 必须在透传参数**之前**：默认的测试缓存会让「上一版的绿」冒充这一版的绿，
  # 而本脚本的输出常被当作证据（M8 验收、基线、变异对照）。调用方仍可自行覆盖。
  go test -count=1 -p "$shard_parallel" "${version_ldflags[@]}" "${forwarded[@]}" "${selected[@]}"
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
        SFORUM_TEST_REDIS_PORT="$((redis_preferred_port + index))" \
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
    provision_backends
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
      provision_backends
      go test -count=1 -p "$shard_parallel" "${version_ldflags[@]}" "${passthrough[@]}"
    fi
    if [[ "$mode" == "with-compat" ]]; then
      echo 'Running V3 P12 compatibility farm (required cells; skip/missing fail)...'
      provision_backends
      run_compat_farm
    fi
    ;;
esac
