# 2026-11-25 Go 测试模板库、包内并行与 CompatFarm 预编译复用

**Status**: accepted
**Scope**: `apps/api/internal/testdb/`、`scripts/test-api-isolated.sh`、
`app/Models/Extensions/`、`app/Models/Identity/`、`app/Support/HostAPI/`、
`app/Support/CompatFarm/`、`tests/compat/`

## Decisions

1. **每个隔离集群只跑一次完整迁移，然后冻结为模板库**。
   `test-api-isolated.sh` 在 `go run ./cmd/migrate` 后创建
   `sforum_test_template` 并标记 `IS_TEMPLATE=true`。模板库与分片容器同生同灭，
   不跨运行持久化。
2. **包级模板负责 fixture 专属 DDL，用例克隆最终模板**。
   `testdb.NewTemplate` 通过 `sync.Once` 准备一次包级模板；准备回调必须关闭所有连接。
   模板建立私有 schema，并设置 `search_path=<fixture>,public`；用例从模板克隆独立数据库。
   克隆清理使用 `DROP DATABASE ... WITH (FORCE)`。
3. **模板缺失必须 fail closed**。
   `MigratedDatabaseURL` 和 `DatabaseTemplate.Clone` 只从
   `SFORUM_TEST_DATABASE_URL` 指向的一次性集群派生；模板不存在时直接失败并提示使用
   隔离脚本。禁止回退到 `DATABASE_URL` 或开发库。既有 dev guard、集群 marker、
   测试连接 URL 校验和开发库拒绝逻辑保持原样。
4. **只在隔离边界成立的重包开启 `t.Parallel()`**。
   Extensions、Identity、HostAPI 的 Postgres fixture 各自拥有数据库；
   Identity 的并发计数测试改为 `atomic.Int64` 后通过 `-race -count=5`。
   `database/migrator` 不开启额外并行：其测试必须验证真实迁移链，
   Goose 解析会并发分配大量 4 MiB 缓冲，race + count=5 在默认 10 分钟上限内无法稳定完成。
5. **CompatFarm 复用预编译内置包时必须同时满足源码干净和制品完整**。
   `RunOptions.PrebuiltPluginRoot` / `SFORUM_COMPAT_PREBUILT_ROOT` 指向 staging root；
   `apps/api` 与 `extensions/builtin` 必须无未提交改动，manifest、
   `VerifiedTreeDigest` 与 executable digest 全部通过后才跳过现场 `go build`。
   显式 `BuiltinPluginPath` 优先于预编译 root；篡改制品失败，源码脏时回退现场构建。
   仓库不自动信任 `storage/builtin-dev`，避免干净提交复用旧 staging。
6. **LPT 权重和分片上限按本批实测更新**。
   默认分片上限从 `min(nproc,4)` 改为 `min(nproc,6)`；权重使用无缓存日志更新。
   6 分片无缓存 API 为 172s，4 分片为 200s。

## Consequences

- `Extensions` 71.1s → 26.1s，`Identity` 55.9s → 13.3s，
  `HostAPI` 30.0s → 10.2s。
- 无缓存全量门禁：api 172s / web 195s / static 83s，壁钟 195s；
  完整门禁仍由 Web production build 主导。
- `test.sh --fast` 稳态 43s（api 20 / web 43 / static 19）。
- 每个分片仍承担一次完整迁移的固定成本；继续增加分片会同时增加容器与迁移开销。
- migrator 的 77.6s 仍是 Go 侧单包长尾，需要后续缓存/复用 Goose 解析结果才能安全提速。

## Guardrails

- `CI=true` 与 `SFORUM_TEST_NO_CACHE=1` 继续强制 `-count=1`。
- 固定端口被占用时只能回退临时端口，不得抢占其他测试容器。
- PG/Redis 连续两次真实认证 / PONG 判据不得删除。
- 模板库初始化失败是粘性失败；不得退回共享数据库。
- 生产备份 KDF 默认仍为 200000。
- 预编译插件复用不得绕过 manifest、tree digest、executable digest 或源码清洁检查。
