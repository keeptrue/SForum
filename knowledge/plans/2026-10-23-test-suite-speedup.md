# 2026-10-23 全量测试提速任务书

**Status**: active（本地验收 1–4、6 全绿；5 待 PR）
**Owner**: 2026-10-23 session
**目标**：`./scripts/test.sh` 本地全量从 20+ 分钟降到 6–8 分钟；新增 `--fast` 分层约 3–4 分钟；不删测试、不降低任何门禁。

**2026-11-25 更新**：M5 模板库降低重包耗时后，`SFORUM_TEST_SHARDS` 默认上限已从
`min(nproc,4)` 提升到 `min(nproc,6)`；见
`2026-11-24-ci-cd-and-test-speedup.md` 与
`decisions/2026-11-25-go-template-db-and-package-parallelism.md`。

## 背景与诊断（已完成勘察，证据如下）

基线：开发机 12 核，本地全量 `./scripts/test.sh` 20 余分钟。耗时结构：

1. **Go API 测试串行（最大头，约 10–15 分钟）**
   `scripts/test-api-isolated.sh` 末段 `go test -p "${SFORUM_TEST_PARALLEL:-1}"` 默认完全串行。
   `apps/api` 有 1254 个 `_test.go`、132 个含测试的包；脚本内注释实测「45 个数据库包串行 662 秒」。
   同一注释已声明：包之间共享同一个 database、靠 schema 隔离，并行是支持的，只是默认值保守。

2. **隔离集群建了两遍（约 1–2 分钟浪费）**
   `scripts/test.sh` 先跑 `test-api-isolated.sh`（全量 go test），再跑 `test-api-isolated.sh --compat`（兼容农场）。
   每次调用都：新建一次性 PG 集群 → 等就绪（连续两次真实认证连接）→ `go run ./cmd/migrate` 把 209 个迁移完整跑一遍。
   compat 农场（`apps/api/app/Support/CompatFarm/run.go`）还要现场 `go build` 两个 builtin 插件（sforum-smtp、sforum-search-site）。

3. **Web 段三段串行（约 4–7 分钟）**
   `scripts/ci/web-gate.sh`：typecheck（`nuxt prepare` 生成独立 `.nuxt-typecheck` + vue-tsc 全量）→ `bun test`（188 个文件）→ production `bun run build`（`.nuxt-build`，6GB 堆）。三段与 Go 段无任何依赖却被排在同一条串行主线上。

4. **静态 gates 串行占位（约 1–2 分钟）**
   `scripts/ci/release-tooling-gate.sh` + `scripts/ci/contracts-gate.sh` 均为纯静态检查，不依赖数据库，也在主线串行。

## 工作项

### M0 并行稳定性验证（前置，只跑不改）

- `SFORUM_TEST_PARALLEL=8 ./scripts/test-api-isolated.sh` 连跑两轮。
- 零 flaky → 直接进 M1；个别包冲突 → 记录冲突包名，M1 用排除名单处理，不退回全局串行。

### M1 Go 测试默认并行化

- 文件：`scripts/test-api-isolated.sh`
- 默认值改为按核数取值、上限 8：`SFORUM_TEST_PARALLEL:-$(( $(nproc) > 8 ? 8 : $(nproc) ))`。
- 同步更新脚本内注释（删掉「默认仍然串行」的过时说明，写明并行依据：schema 隔离 + M0 验证结论）。
- CI `scripts/ci/go-gate.sh` 走同一脚本，自动受益，无需另改。

### M2 合并两次隔离运行

- 文件：`scripts/test-api-isolated.sh`、`scripts/test.sh`、`scripts/ci/go-gate.sh`
- 新增 `--with-compat` 模式：同一个一次性集群内先 `go test ./...`，再 `cd tests/compat && go run .`；集群只建一次、209 个迁移只跑一次。
- `test.sh` 与 `go-gate.sh` 改为单次调用；保留独立 `--compat` 入口以便单独调试农场。

### M3 test.sh 顶层三线并行

- 文件：`scripts/test.sh`
- 拆成三条后台线并发，`&` + `wait` 聚合退出码；每条线日志写 `tests/.logs/{api,web,static}.log`，全线结束后按序打印摘要，失败线打印日志尾部。
- API 线：M2 合并后的 isolated 测试（含 compat）+ `go build ./...`。
- Web 线：typecheck → `bun test` → production build。
- 静态线：release-tooling-gate + contracts-gate + `validate-runtime-tzdata.mjs`。
- 资源错峰天然成立：Go 测试吃数据库/IO，web build 吃 CPU/内存。
- `tests/.logs/` 加入 `.gitignore`。

### M4 `--fast` 分层

- 文件：`scripts/test.sh`、`AGENTS.md`（Commands 段补一行）
- `./scripts/test.sh --fast`：跳过 production build、compat farm、release-tooling-gate（三项 CI 已完整覆盖，本地日常属重复验证），只跑 Go 测试 + typecheck + `bun test`。
- 不带参数保持现状语义，全量门槛不降低；CI 不受影响。

### M5 typecheck 增量缓存

- 文件：`apps/web/package.json` typecheck 脚本
- vue-tsc 加 `--incremental --tsBuildInfoFile .nuxt-typecheck/.tsbuildinfo`，二次运行从全量降为增量。
- 验证 `.nuxt-typecheck` 已在 gitignore（应该已在，确认即可）。

### M6（后续跟进，不在本次范围）

`database/migrator` 34 个测试文件仅 1 个用 `t.Parallel()`；`-p` 提升后包内串行成为新长尾。另开任务推广包内并行，本任务不动。

## 明确不做

- 不删任何测试、不降低 CI 门禁；`--fast` 仅是本地显式选择。
- 不动收尾 `go build ./...`（编译缓存命中，成本近零）。
- 不加 `-count=1`（保留 Go 测试缓存收益；固定端口 55432 的缓存键设计继续生效）。

## 验收

1. `SFORUM_TEST_PARALLEL=8` 连跑两轮零 flaky（M0 记录结果）。
2. 改完 `./scripts/test.sh` 连跑 3 次计时取中位数，对比 20+ 分钟基线；目标 ≤ 8 分钟。
3. `./scripts/test.sh --fast` 跑通，目标 ≤ 4 分钟。
4. 三线并行下任一线失败时退出码非零、失败日志可见；全部成功时输出 "All gates passed."。
5. PR 上 ci.yml 三个 gate job（go / web / contracts）全绿。
6. 完成后更新本文件 Status 并在 `knowledge/plans/README.md` 同步。

## 执行记录（2026-10-23）

### M0 并行稳定性：原方案不可行，改用集群级分片

任务书原命令 `SFORUM_TEST_PARALLEL=8 ./scripts/test-api-isolated.sh` 在本机**证明不了
任何事**：Go 测试缓存全量命中（首轮 34/34 个包输出 `(cached)`，真实执行 0 个）。因此
M0 用 `-count=1` 强制真跑（仅用于验证，没有写进门禁）。

裸 `-p 8` 跑 `./...`（8 分 04 秒）暴露 3 处失败 + 1 处挂死：

| 包 | 现象 | 根因 |
| --- | --- | --- |
| `app/Models/Extensions` | 461s 挂死 | fixture 迁移失败后 `defer setup.Release()` 晚于 `defer admin.Close()`，puddle `Pool.Close` 等连接归还 → 整包挂到 `-test.timeout` |
| `app/Support/Extensions/IntegrationTests` | `extension database physical resource conflict` | 扩展数据库物理资源（集群级角色） |
| `bootstrap` | `lifecycle operation already in progress: initial plugin runtime publication` | 一次性 genesis 发布租约 |
| `app/Support/HostAPI`（后续复跑） | `core database authority conflicts with the Host ownership boundary` | core authority 校验扫到别的包留下的 grant 行 |

三个包单独 `-p 3` 一起跑过 91 秒全绿，但换到隔离阶段复现了
`could not open relation with OID ...`（goose partial migration error）——说明「排除名单 +
一个 database」只能降低概率，不能保证零 flaky。

**最终方案**：并行从「包级 `-p`」移到「集群级分片」。`SFORUM_TEST_SHARDS` 默认
`min(nproc, 4)`，每片一个一次性集群、跑完整迁移、片内 `-p 1`（与既有 CI 串行语义一致）。
分片之间没有任何共享数据库状态。

### 计时（本机 12 核，开发环境同时在跑）

| 轮次 | 缓存 | 总墙钟 | api | web | static |
| --- | --- | --- | --- | --- | --- |
| 1 | 冷（`go clean -testcache`） | 7:26.87 | 447s | 387s | 105s |
| 2 | 冷 | 7:24.17 | 444s | 383s | 106s |
| 3 | 热 | 6:14.35 | 41s | 374s | 89s |
| `--fast` | 热 | 0:40.71 | 26s | 40s | 21s |

三次连跑中位数 **7:24**，对比 2026-10-20 基线 21:31。Go 段从 14m45s 降到 4–5 分钟（冷）
/ 41 秒（热）；门禁的长尾已经变成 Nuxt production build（约 300s）。

### 验收对照

1. 两轮冷缓存真跑零 flaky：**通过**（上表第 1、2 轮，154 个包全部真实执行，0 失败）。
2. 三次计时中位数 ≤ 8 分钟：**通过**（7:24）。
3. `./scripts/test.sh --fast` ≤ 4 分钟：**通过**（0:40.71；web 段 1508 个用例真实跑过，
   见 `tests/.logs/web.log` 的 `Ran 1508 tests across 184 files`）。
4. 失败路径：**通过**。用 `PATH=/usr/bin:/bin` 跑真脚本，三线全 FAIL、退出码 1、
   摘要与三条日志尾部都打印；全绿时输出 `All gates passed.`。
5. PR 上三个 gate job 全绿：**待 PR**。本地已跑通三条 gate 脚本的并集
   （`go-gate.sh` = api 线、`web-gate.sh` = web 线、`contracts-gate.sh` = static 线）。
6. 计划 Status 与 `knowledge/plans/README.md` 同步：**完成**。

### 偏离登记

| 编号 | 任务书要求 | 实际做法 | 原因 |
| --- | --- | --- | --- |
| D1 | M0 直接跑 `SFORUM_TEST_PARALLEL=8` 两轮 | 加 `-count=1` 强制真跑 | 全量缓存命中，不加就是空跑，证明不了 flaky |
| D2 | M1 `-p` 默认 min(nproc, 8) | 改为 `SFORUM_TEST_SHARDS` 默认 min(nproc, 4)，片内 `-p` 默认 1 | `-p>1` 在一个 database 上实测四类跨包竞态（上表） |
| D3 | M2 集群只建一次 | 分片各建一个；compat 农场另建一个 | 共享 database 就是竞态来源，隔离必须做到集群级 |
| D4 | M5 `.nuxt-typecheck/.tsbuildinfo` | `${NUXT_BUILD_DIR}.tsbuildinfo`（同级文件） | `nuxt prepare` 会清空该目录，放里面每次都被删（实测探针文件消失） |
| D5 | 未提及 | 修 `theme_publication_postgres_integration_test.go` 的 defer 顺序 | 并行成为默认后，任何 fixture 失败都会挂死 10 分钟 |

### 残留

- 分片负载不均：第 3 片只跑 27s、第 1 片 366s（按包名轮转，未按耗时做 LPT 均衡）。
  均衡后可再省约 1–2 分钟，留作后续。
- `database/migrator` 等包内部仍串行（M6 范围）。
- Nuxt production build 是当前长尾（约 300s），受内存压力影响波动大。
