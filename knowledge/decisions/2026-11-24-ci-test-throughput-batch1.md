# 2026-11-24 CI 与测试吞吐第一批

**Status**: accepted
**Scope**: `scripts/test-api-isolated.sh`、`scripts/test.sh`、CI workflow caches、
`contracts-gate.sh`、`release-tooling-gate.sh`、备份/升级 Shell 测试

## Decisions

1. **Go 测试缓存必须显式启用**。隔离测试和完整 `test.sh` 默认使用
   `-count=1` 与临时端口；`--cache` / `SFORUM_TEST_CACHE=1` 才复用固定端口和 Go
   测试缓存。`CI=true` 与 `SFORUM_TEST_NO_CACHE=1` 是硬覆盖。`test.sh --fast`
   作为显式本地日常模式默认启用缓存，但可用 `--no-cache` 关闭。
2. **分片使用实测 LPT 权重，默认仍为 4 片**。首轮无缓存实测更新了
   `database/migrator`、`Models/Extensions`、`Extensions/IntegrationTests` 等长尾；
   默认不升到 6 片，避免与 web/static 并发争抢资源。`SFORUM_TEST_SHARDS=6` 仍可手工压测。
   **2026-11-25 M5 更新**：模板库降低重包耗时后，6 分片实测优于 4 分片，
   默认上限已改为 `min(nproc,6)`，见
   `2026-11-25-go-template-db-and-package-parallelism.md`。
3. **compat 复用最轻分片的已迁移集群**。`--with-compat` 不再串行新建第 5 套
   PG/Redis，也不重复跑迁移；compat 在 LPT 最轻分片的测试完成后同库执行。
4. **门禁并行使用动态 worker pool**。release/contracts 的并发上限默认
   `min(nproc,4)`，任务完成即补位；每个任务独立缓冲日志，失败时完整回放。
   deploy 测试覆盖检查直接比对实际任务登记表，避免 RV-14 退化为 grep 注释。
5. **parity 全仓扫描共享只读输出快照，不共享解析器**。contracts gate 只运行一次
   parity 真实仓库扫描，四个 parity 测试复用 stdout/stderr/status；reachability 与
   parity 仍各自解析，equivalence 仍独立启动两道门。
6. **KDF 测试口只允许 `APP_ENV=test`**。生产缺省固定 200000；测试先跑默认
   200000 + 信封格式闭环，再使用 1000 覆盖其余状态机/篡改用例。
7. **Chrome 无头参数必须隔离后台网络**。Chrome 154 在部分宿主的默认无头配置下会
   卡住 loopback `Page.navigate`；CSP 测试增加禁用后台网络/同步/扩展及回环代理绕过参数，
   不改变任何 CSP 断言。

## Consequences

- 本地无缓存隔离测试从 307.6s 最大分片降到约 215s；两轮最终四分片最大/均值
  为 1.063 / 1.039。
- 最终完整 `test.sh` 为 api 288s / web 244s / static 80s，壁钟 287.5s。
- 稳态 `test.sh --fast` 为 api 29s / web 69s / static 30s，壁钟 69.2s。
- `release-tooling-gate.sh` 降到 45.3s，`contracts-gate.sh` 独立入口 17.1s、
  test.sh 跳过重复 web 单测时 12.5s。
- 完整全量仍受 Nuxt build 与 Go/Web 并发资源竞争限制；M3/M5 仍是下一批主要收益来源。

## Guardrails

- M4（2026-11-25）已把 `SFORUM_CONTRACTS_SKIP_WEB_UNIT` 改绑到
  `changes.web == true`：Web job 执行时 contracts 才跳过重复单测，Web job 被跳过时
  contracts 自己运行这些测试。
- LPT 权重是实测快照，不自动学习；长尾变化时应从分片日志更新。
- 生产备份 KDF 200000、CI 无缓存证据模式、RV-14、parity/reachability 独立性不可弱化。
