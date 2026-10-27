# P0 审查报告：测试库隔离（apps/api/internal/testdb）

审查人：小审
范围：`apps/api/internal/testdb/**`、138 个 `*_test.go` 的 DSN 改造、`scripts/api-dev.sh:38`、
`AGENTS.md:117`、`tests/test-db-isolation-allowlist.json`。
审查窗口：2026-10-27 08:23–09:05（未提交工作区，main）。

## 结论

**通过**。隔离设计与实现成立，未发现 fail-open 缺陷。初审提出的 2 项必须修（M1 gofmt、M2 死代码）
在收尾包中已修并复核通过；S1（CompatFarm 旁路）、S2（devguard 缺 APP_ENV 门）、S3（缺一条单测）
也都已修。五项全部关闭。

**本报告的"绿"是 2026-10-27 09:01 的快照**。当时树里已有 task-4 的在途文件
（`bootstrap/extension_lifecycle_cleanup_fk_order_test.go`，untracked，08:59）；08:58 一次 `go vet`
曾因该文件引用未定义的 `extensionsruntime.LifecycleBoundaryCleanupUninstallPreserve` 而失败，
09:01 复跑已干净。该中间态与 P0 无关，不应算到 P0 头上。

## 必查项逐条

1. **哨兵表（硬验收项）✅** — 独立探针 `SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL` → `t`；
   `\d` 结构正确（`guard text PRIMARY KEY`、`created_at timestamptz default now()`）；1 行 `development`，
   `created_at = 2026-10-27 08:08:25+08`。`scripts/api-dev.sh:38-41` 在 `exec air` 之前调用，失败即 `exit 1`。
2. **allowlist 6 条 ✅** — 逐条核对，全部不是"回退读取"：
   - `IntegrationTests/database_isolation_test.go:23-45`：`TestMain` 仅在缺 URL 时读 `DATABASE_URL`
     生成拒绝原因并 `exit 2`；拿到 target 后校验一次性集群标记/库名/无 `sforum` 库，再 `os.Setenv`
     把 `DATABASE_URL` 覆盖成测试库。不拿它连开发库。
   - `lifecycle_publication_journal_postgres_integration_test.go:402-407`、
     `provider_slot_selection_postgres_integration_test.go:97-102`：负向用例把 `DATABASE_URL` 设成不可解析的
     `postgres://should-never-be-used/for-integration`，断言 `requireSforumTestDatabaseURL()` 返回 `("",false)`。
   - `cmd/sforum/privacy_erase_user_test.go:41`、`recovery_test.go:46`：`t.Setenv("DATABASE_URL","")` 断言拒绝。
   - `config/config_test.go:38,155,202,751`：`t.Setenv` 写示例串验证配置解析，不连库。
   - 扫描器把 `t.Setenv("DATABASE_URL",…)` 也计入"出现即需登记"，是刻意从严（防止用 Setenv 把开发库
     塞进生产代码路径），因此这 4 条属正常登记而非漏网。
3. **6 处手工改动 ✅**
   - `bootstrap/reference_package_install_chain_test.go:37`：删掉写死 `127.0.0.1:15432/sforum` 兜底。
   - `IntegrationTests/commerce_workflow_reference_plugin_integration_test.go:850-853`：原实现**优先
     `DATABASE_URL`**（最危险的一处）→ 改为 `testdb.IsolatedURL`。
   - `Models/Extensions/settings_lifecycle_postgres_integration_test.go:391-394`：删掉 15432 兜底。
   - `bootstrap/query_registry_worker_ownership_integration_test.go:31`：联合 skip 去掉 `baseURL==""`。
   - `Support/Extensions/extension_database_raw_core_integration_test.go:246,333,435`：主用例/helper 分别
     `IsolatedURL` / `RequireIsolatedURL`，去掉手写空值 skip。
   - `Support/CompatFarm/matrix_test.go:51-53`：删掉 `DATABASE_URL` 与 15432 兜底，改走 `testdb.ValidatedURL`。
4. **子进程 ✅** — 全仓仅 2 处把 `SFORUM_TEST_DATABASE_URL` 传给子进程（`raw_core:285`、
   `migration_multiprocess:56`），父子两侧都用 `RequireIsolatedURL`；`IntegrationTests` 的 `TestMain`
   缺库是 `exit 2` 而非 skip。实证：`SFORUM_EXTENSION_MIGRATION_PROCESS_HELPER=1 …_PLAN=e30`
   （不给 DB 变量）→ `FAIL: SFORUM_TEST_DATABASE_URL is required but not set`。
5. **三层验收 ✅** — 静态 0 违规（6 例外）；运行时 `topics 1247→1247 / posts 6423→6423 / users 127→127`、
   `extensions` 测试前缀行 `6→6`；覆盖率基线已写入（6500 顶层测试 / 136 包 / 0 DB skip / 0 失败包）。

## 问题清单与处理状态

| # | 级别 | 位置 | 问题 | 状态 |
|---|---|---|---|---|
| M1 | 必须修 | `Models/Extensions/settings_lifecycle_postgres_integration_test.go`、`IntegrationTests/commerce_workflow_reference_plugin_integration_test.go` | gofmt 不过（import 组内空行 / 组内乱序） | **已修**：`gofmt -l` 对这两个文件、以及全部 138 个 P0 改动文件均为空 |
| M2 | 必须修 | `settings_lifecycle_postgres_integration_test.go:109-111`、`:292-294` | helper 改 `IsolatedURL` 后 `if databaseURL == "" { t.Skip }` 永假，误导成"还有静默跳过路径" | **已修**：`grep 'if databaseURL == ""'` 无结果 |
| S1 | 建议（真漏洞） | `Support/CompatFarm/matrix_test.go:51-53` | `SFORUM_COMPAT_DATABASE_URL` 非空即用、不过 testdb 校验，换变量名可绕过守卫 | **已修**：新增 `testdb.ValidatedURL`，且 `resolve()` 已委托给它（`internal/testdb/testdb.go:129`），两条入口共用同一套 `probe + Decide`，无第二条旁路 |
| S2 | 建议 | `internal/testdb/cmd/devguard/main.go:18-27` | 哨兵写入无 `APP_ENV` 门，dev 脚本指向非 dev 库时会写入表 | **已修**：空值按 development 处理（与 `config.Load` 默认一致），非 development 直接 `exit 1` |
| S3 | 建议 | `internal/testdb/isolated_postgres_test.go:165-183`（helper `:186-192`） | 缺 `RequireIsolatedURL`"未设置 → Fatal"的单测 | **已修**：新增 `TestRequireIsolatedURLFailsWithoutTestDatabaseURL`——子进程 `-test.run=^TestRequireIsolatedURLMissingHelper$`，把 `SFORUM_TEST_DATABASE_URL` 与 `DATABASE_URL` 都置空，断言子进程非零退出且输出含 `SFORUM_TEST_DATABASE_URL is required`。实测 `go test -count=1 -run TestRequireIsolatedURLFailsWithoutTestDatabaseURL ./internal/testdb` → PASS。放在 `isolated_postgres_test.go` 而非 `testdb_test.go` 是对的：子进程用例需要起真进程，与纯逻辑单测分开更合理。 |

## 关于 `.env` shell 下 3 个失败：判为**既有环境敏感性**，接受

在 HEAD（P0 之前）的独立 worktree 复现出**同样** 3 个失败：

```
cd <HEAD-worktree>/apps/api && set -a && . <repo>/.env && set +a && go test -count=1 ./config
--- FAIL: TestLoadIncludesDefaultWorkerConfig (config_test.go:95)
--- FAIL: TestLoadDefaultsV3TrustChallengesByEnvironment（panic: EXTERNAL_EXTENSION_ROOTS must not scan the development scaffold tree in production）
go test -count=1 ./app/Support/Extensions/IntegrationTests
plugin integration tests require an explicit isolated SFORUM_TEST_DATABASE_URL; use scripts/test-api-isolated.sh   → exit 2
```

- config 两个失败来自 `.env` 的 `APP_ENV` / `EXTERNAL_EXTENSION_ROOTS` 泄漏进 `config.Load()`，与数据库无关；
  `apps/api/config` 本次零改动。
- `IntegrationTests` 的 `TestMain` exit 2 在 HEAD 已存在（源码逐字一致）。
- 结论：非本次改造引入，不退回。

附带观察：`app/Support/Extensions` 的 `TestP3ProtocolCompatibilityMatrixGoPluginTransportGaps` 在**并发跑测试**时
会 flake（一次 51.55s 失败于 `handler-entered` 超时；单独跑两种 TMPDIR 均 0.7s 通过）。不是 P0 引入，但会污染
"全绿"证据，验收时应串行跑。

## 计数复核（独立测量，不采信自证）

| 指标 | 老后自证 | 我的独立测量 | 说明 |
|---|---|---|---|
| 隔离 helper 调用点 | 167 `IsolatedURL` + 5 `RequireIsolatedURL` | 175 + 6（135 个文件，不含 `internal/testdb` 自测） | 口径不同（按调用点计，含同文件多次调用） |
| 执行测试数 | 10942 | 10955（含子测试）/ 6500（仅顶层） | 他的数字是子测试口径；08:25 后树里又多了文件 |
| skip | 19 | 21（含子测试）/ 20（顶层） | 差的 2 个是 `*ProcessHelper` 子进程 helper 自跳 |
| DB 相关 skip | — | **0** | 基线 `dbSkips: {}`：没有 DB 测试因缺库而跳过 |

**流程缺口（如实记录）**："改造前"的数字无法独立复核——基线应在动第一行代码之前钉，而不是事后补。
可复核的是"改造后无 DB 测试丢失"：0 DB skip + 静态 0 违规 + 逐包基线。

## 与 P0 无关但需要记录的归属判定

- **`bootstrap/api_assembly.go` 的 gofmt 脏点是存量**：`git show HEAD:apps/api/bootstrap/api_assembly.go |
  gofmt -l /dev/stdin` 输出 `/dev/stdin`（HEAD 即不格式化），脏点在 import 分组；而本次 30 行改动
  （`closeAPIPoolOnFailure` 看门狗 + `closeCommentRevisionHub` 字段）**没碰 import**。mtime 06:39，同批
  `Forum/comment_revision_hub.go` 06:32，均早于 P0 交付（08:08），属别人开工前的在途改动。
- **`gofmt -l apps/api` = 40 个是仓库存量债**（go1.27 vs go1.26 格式化差异），其中无一是 P0 改动文件；
  建议单开清理任务，不混进功能单。
- **`app/Models/Extensions` 第 93 个文件**：初审时该目录有 `plugin_runtime_recovery_recheck_postgres.go`
  （untracked，08:25，8132B），使架构门 93 > cap 92 变红；收尾包中该文件已删除，门禁回绿
  （`1973 production files scanned`）。它当时是 untracked，删除不留 git 痕迹，无法用 mtime 反推删除时间。

## 复核命令

```bash
# 静态（秒级）
node tests/validate-test-db-isolation.mjs --static-only

# 运行时（source .env 后跑全量 go test，比对开发库只读快照）
TMPDIR=/home/inkedus/.cache/gotmp node tests/validate-test-db-isolation.mjs --runtime-only --allow-suite-failures

# 覆盖率（与基线比对；基线由 --write-baseline 生成）
TMPDIR=/home/inkedus/.cache/gotmp node tests/validate-test-db-isolation.mjs --coverage-only

# 收尾复核用到的旁证
gofmt -l <P0 改动文件...>
node tests/validate-architecture-boundaries.mjs
cd apps/api && go build ./... && go vet ./...
```

## 交付物

- 基线：`tests/test-db-isolation-baseline.json`（6500 顶层测试 / 136 包 / 0 DB skip，2026-10-27T00:46Z）
- 机器可读结果：`tests/.logs/test-db-isolation-report.json`（每次运行覆盖）
- 验收脚本：`tests/validate-test-db-isolation.mjs` + `tests/helpers/test-db-isolation-{scan,db,coverage}.mjs`
- 例外清单：`tests/test-db-isolation-allowlist.json`
