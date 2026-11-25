# 2026-10-23 测试门禁并行化与分层

**Status**: accepted
**Scope**: `scripts/test.sh`、`scripts/test-api-isolated.sh`、`scripts/ci/go-gate.sh`、
`apps/web/package.json` 的 `typecheck`

## 背景

本地全量 `./scripts/test.sh` 实测 21 分 31 秒（2026-10-20 记录：release-tooling-gate
1m19s、Go 测试 14m45s、compat+contracts+typecheck+bun test 1m12s、Nuxt build 4m15s）。
Go 段默认 `-p 1` 完全串行；隔离集群被建了两遍（全量一次、compat 农场一次），每遍都
要跑完 209 个迁移；Web 与静态段虽然与 Go 段无依赖，却排在同一条串行主线上。

## 决策

1. **Go 并行 = 多个一次性集群分片，而不是一个 database 上提高 `-p`**。
   `SFORUM_TEST_SHARDS` 默认 `min(nproc, 4)`，每个分片一个独立集群、跑完整迁移，
   分片内 `-p ${SFORUM_TEST_PARALLEL:-1}`（默认 1，与既有 CI 的串行执行完全一致）。
   包按 `go list ./...` 顺序轮转分配到各分片。

   为什么不按任务书原方案直接把 `-p` 提到 8：M0 实测一个 database 上 `-p 8` 会让
   「跑完整迁移链 / 写 public 共享行」的包互相踩，出现四类真实失败：
   - `extension database physical resource conflict`（扩展数据库物理资源/集群级角色）；
   - `extensions: lifecycle operation already in progress: initial plugin runtime publication`
     （一次性 genesis 发布租约）；
   - `could not open relation with OID ...`（迁移链并发 DDL，goose 报 partial migration error）；
   - `core database authority conflicts with the Host ownership boundary`
     （core authority 校验扫到别的包留下的 grant 行）。

   这些不是 schema 隔离能解决的问题：角色、一次性租约、`public` 上的固定对象都是
   database/集群级单例。分片把「并行」从包级挪到集群级，分片之间没有任何共享数据库
   状态，因此既拿到并行度，又保留已被 CI 验证过的串行语义。代价是每个分片都要跑一遍
   209 个迁移（约 40s CPU，并行后是固定开销）。

2. **一次集群跑完 go test 与 compat 农场**：新增 `--with-compat`，分片跑完后在同一脚本
   里新建一个一次性集群跑 `tests/compat`（required cells）。独立 `--compat` 入口保留，
   便于单独调试农场。

3. **`scripts/test.sh` 三线并行**：api（Go 分片 + compat + `go build`）、web
   （typecheck + `bun test` + production build）、static（release-tooling-gate +
   contracts-gate + tzdata）三条线并发，`wait` 聚合退出码；每线日志写
   `tests/.logs/<line>.log`，结束时按序打印摘要，失败线打印日志尾部。三条线互不依赖，
   且静态段的校验脚本都不读 `.nuxt*`，与 web 段的构建产物没有交叉。

4. **`--fast` 是本地显式选择**：跳过 production build、compat 农场、release-tooling
   gate（这三项 CI 已完整覆盖）。不带参数时语义不变，CI 的 `go-gate.sh` 走
   `--with-compat`，门禁不降低。

5. **vue-tsc 增量缓存放在构建目录之外**：`nuxt prepare` 会清空 `NUXT_BUILD_DIR`
   （实测放进去的探针文件被删），所以 `--tsBuildInfoFile` 用
   `${NUXT_BUILD_DIR}.tsbuildinfo`（默认 `apps/web/.nuxt-typecheck.tsbuildinfo`），
   已被 `apps/web/.gitignore` 的 `.nuxt-*` 覆盖。二次运行 45s → 17–20s。

6. **修掉一个 fixture 清理死锁**：
   `theme_publication_postgres_integration_test.go` 里 `defer setup.Release()` 早于
   `defer admin.Close()` 注册，LIFO 下 `admin.Close()` 先执行，而 puddle 的
   `Pool.Close` 会等待已取出的连接归还 → 任何 fixture 失败都会把整包挂死到
   `-test.timeout`（裸跑 `-p 8` 时实测挂了 461 秒）。改成先注册 `admin.Close()`。
   并行成为默认后这是必须修掉的放大器：失败应该失败，而不是挂十分钟。

## 后果

- 本机 12 核：全量门禁从 20+ 分钟降到 5 分钟量级（计时记录见
  `knowledge/sessions/2026-10-23-test-suite-speedup-handoff.md`）。
- `SFORUM_TEST_PARALLEL` 仍然保留，但默认 1；想在一个 database 上再压 `-p` 的人要自己
  承担上面四类竞态。
- 分片固定用 `preferred_port + i` 作为宿主端口，缓存键跨运行稳定；端口被占用时回退
  临时端口，只损失缓存。
- CI 的 Go gate 变成单次调用（`--with-compat`），容器与迁移各跑一遍而不是两遍。
- 包内串行成为新长尾（`database/migrator` 34 个测试文件只有 1 个用 `t.Parallel()`），
  这是后续独立任务，不在本次范围。
- 2026-11-25 M5 更新：模板库降低重包耗时后，默认分片上限提升为
  `min(nproc,6)`；migrator 仍不开启额外并行。见
  `2026-11-25-go-template-db-and-package-parallelism.md`。
