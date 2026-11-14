# M5 门禁基线快照（HEAD 5a1d4083f）

- 采集时间：2026-11-14 12:44–13:05（Asia/Shanghai）
- 采集人：ops（老运），task-10
- 结论一句话：**HEAD 不是绿的。四条门禁 2 绿 2 红（`arch`、`openapi` 绿；`v3catalog`、`fast` 红）**，红点全部可复现于 `5a1d4083f` 的干净检出，与工作树脏不脏无关（唯一例外见 §3 末尾的“非红点”）。
- 采集之后主干前进到 `5cac2038e`（4 个 trust-levels 提交）；13:11 在 `5cac2038e` 上复验了三条廉价门禁，**红点原样存在**：`v3catalog` 仍报 `routes.json`/`routes.md` 过期，`validate-docs` 仍报 `docs/en-US/extensions/trust-levels.md` 缺失，`extension docs generate --check` 仍报 `manifest-v3.md` 漂移。
- 每一处红的**当前修复状态**和复核结论见 §6 末尾（R1/R3 由 `1ac0c0a9b` 修、R4 由 `830dd2c5b` 修、R5 由 `03c9a794b` 修）。

## 1. 四条门禁结论（干净检出 @ 5a1d4083f）

| 门禁 | 命令 | exit | 耗时 | 结论 |
| --- | --- | --- | --- | --- |
| arch | `node tests/validate-architecture-boundaries.mjs` | 0 | 1022 ms | 绿 |
| openapi | `ruby scripts/validate-openapi-refs.rb` | 0 | 696 ms | 绿 |
| v3catalog | `node scripts/v3-catalog/generate.mjs --check` | 1 | 445 ms | **红**：`docs/extensions/v3/catalogs/routes.{json,md}` 过期 |
| fast | `./scripts/test.sh --fast` | 1 | 491758 ms（8m11.8s） | **红**：三条线 `api`/`web`/`static` 全红（3 个独立红点，见 §3） |

同一份数据在**主工作树**（脏）上的一次完整跑：

| 门禁 | exit | 耗时 | 结论 |
| --- | --- | --- | --- |
| arch | 0 | 396 ms | 绿 |
| openapi | 0 | 462 ms | 绿 |
| v3catalog | 1 | 427 ms | 红（同一红点） |
| fast | 1 | 455833 ms（7m35.8s） | 红：api + static 红，web 绿（2996/2996） |

关键差异只有一处：主工作树里 `apps/web` 全量单测是绿的（2996 pass / 0 fail），干净检出上是 2995 pass / 1 fail —— 因为同事正在改的 `useAdminTrustLevelConditions` 系列文件恰好把这条断言改绿了；**HEAD 本身是红的**。

## 2. 复现方式

四条门禁已收敛到 `scripts/m5-gates.sh`（串行、任一条红则整体非零退出、逐条打印耗时、日志落 `tests/.logs/m5-gates-<UTC戳>/`）：

```bash
./scripts/m5-gates.sh                 # 全跑
./scripts/m5-gates.sh --only v3catalog
./scripts/m5-gates.sh --fail-fast
./scripts/m5-gates.sh --list
```

脚本选择说明：`scripts/` 下没有等价物。`scripts/test.sh` 是三条并发线，只经 `scripts/ci/contracts-gate.sh` 间接覆盖 openapi + arch，**不含** `v3-catalog --check`，也不带逐条计时；`scripts/ci/contracts-gate.sh` 是 CID 静态门禁的一段，同样不含 v3 catalog。因此新建 `scripts/m5-gates.sh`，不改造现有脚本（避免改变 CI 语义）。

本次两条基线日志（都已落在主工作树 `tests/.logs/` 下，worktree 已删除）：

- 主工作树（脏）：`tests/.logs/m5-gates-20261114T044742Z/`（`summary.tsv` + 4 份 log）
- 干净检出（`git worktree add --detach` 到 `/tmp/sforum-head-5a1d4083f`，跑完拷回）：`tests/.logs/m5-gates-20261114T045706Z/`（log 里的绝对路径仍带 `/tmp/sforum-head-5a1d4083f` 前缀，属正常留档）

## 3. 红点清单与最小失败单元

### R1 — `v3catalog`：V3 路由目录行号漂移（committed）

- 失败单元：`docs/extensions/v3/catalogs/routes.json`、`docs/extensions/v3/catalogs/routes.md` 与生成器输出不一致。
- 差异只有 `source` 行号，全部集中在 `apps/api/app/Http/Controllers/TrustLevels/controller.go`：6 条 trust-levels 路由各 **+1 行**（89→90、90→91、91→92、92→93、93→94、86→87）。路由集合本身没变。
- 引入点：`e0c67b7bf feat(extensions): 事实条件通用别名 + 权限可被等级授予 + 可挣得白名单运行期化` 在 `TrustLevels/controller.go` 的注册块上方插入了一行，但没重生成目录。
- 修法（不在本次范围）：`node scripts/v3-catalog/generate.mjs`。

### R2 — `fast` / static 线：`docs/en-US/extensions/trust-levels.md` 缺失

- 失败单元：`tests/validate-docs.mjs:203`「docs/en-US is missing the parallel file: extensions/trust-levels.md」。
- 事实：`docs/zh-CN/extensions/trust-levels.md` 存在，`docs/en-US/extensions/` 没有对应文件（同目录其余 5 份都是成对的）。
- 引入点：`62d9fa459 refactor(trust-levels): 删掉不可达的 earnable 成因变体通道`（只落了 zh-CN）。
- 修法：补英文平行文档，或按仓库规则成对删除。

### R3 — `fast` / api 线：extension 目录文档过期（两处断言同一根因）

- 失败单元 1：`apps/api/sdk/plugin` → `TestCommittedCatalogDocsInSync`（`docsgen_test.go:141`）：`drift in manifest-v3.md`。
- 失败单元 2：`apps/api/cmd/sforum` → `TestExtensionDocsGenerateCheck`（`docs_test.go:26`）：`drift in manifest-v3.md`。
- 根因（已用 `generate` + `git diff` 定位，随后已还原）：`docs/extensions/catalogs/manifest-v3.md` 的字段表缺 `userFactConditions` 一行，而 schema 已有该字段（`apps/api/app/Support/ExtensionManifest/manifest.go:140`、`schemas/manifest-v3.schema.json:175`）。差异 1 行新增。
- 修法：`cd apps/api && go run ./cmd/sforum extension docs generate`。

### R4 — `fast` / web 线：earnable 白名单 reason 解析（1 条断言）

- 失败单元：`apps/web/tests/admin/forum/trustLevels.test.ts:936`
  - 期望：`resolveTrustLevelWhitelist({ok:false, error:{code:403,message:'forbidden',data:{reason:'forbidden'}}})` → `status:'failed', error:'forbidden'`
  - 实得：`error:'request_failed'`（`error.data.reason` 没被读到）
- 复现环境与命令（**版本必须一起记，否则下一个人复现不出来**）：
  - bun **1.4.2**（`744846f84`，日志横幅原文 `bun test v1.4.2 (744846f84)`；本机 `~/.bun/bin/bun` 时间戳 9 月 5 日，当天没有升级过 bun）
  - 判据命令是**全量** `bun test`，不是子目录：
    ```
    git worktree add --detach /tmp/sforum-head-5a1d4083f 5a1d4083f
    ln -s "$PWD/apps/web/node_modules" /tmp/sforum-head-5a1d4083f/apps/web/node_modules
    cd /tmp/sforum-head-5a1d4083f/apps/web && bunx nuxi prepare   # 缺这步会有 93 条找不到 ~/* 的假失败
    bun test                                                     # 2995 pass / 1 fail，连跑 2 次都是这 1 条
    bun test ./tests/admin/forum/trustLevels.test.ts              # 67/67 全绿（单文件不出事）
    ```
- 机制与最终归因（2026-11-14 下午复核，**不是 bun 版本差异**）：泄漏源是同目录的
  `apps/web/tests/admin/forum/trustLevelsSaveConfirmMount.test.ts`，它在 `830dd2c5b`
  （`test(web): 修跨文件模块 mock 泄漏（trustLevels whitelist 用例全量跑红）`，13:28 提交）里被修掉。
  在 **bun 1.4.2** 上把该文件回退到 `5a1d4083f` 版本，两种作用域都能复现同一条失败：
  - 全量 `bun test`：`3006 pass / 1 fail`，失败项与基线完全一致
  - `bun test tests/admin/forum`：`345 pass / 1 fail`，同一断言
  同一棵树（当前 HEAD）不回退该文件时：全量 `3007 pass / 0 fail`。
  结论：**触发条件是「哪个文件注入了进程级 mock」，不是 bun 版本；修掉泄漏文件即消失**。
  拿 `bun test tests/admin/forum` 加自造 `zz-/aaa-` 前缀文件跑绿，不能证明「1.4.2 不跨文件生效」——
  自造文件没有注入那个 mock，而回退真泄漏文件在 1.4.2 上照样红。
- 仍然开放的版本问题：CI 钉 `bun 1.3.14`（`.github/workflows/ci.yml` 两处），本地是 `1.4.2`，
  **两边没跑过同一份用例**。这条差异本身要盯（见 §4），但它不是 R4 的原因。
- 修法：`830dd2c5b` 已修；若再有同类，优先在泄漏文件里 `mock.restore()`，不要靠调整测试作用域绕过。

### R5 — `fast` / api 线：bootstrap 卸载测试 `users_pkey` 冲突（已定位，非偶发）

- 失败单元：`apps/api/bootstrap/extension_lifecycle_cleanup_fk_order_test.go:362` → `newLifecyclePurgeFixture` 建用户时 `duplicate key value violates unique constraint "users_pkey" (SQLSTATE 23505)`，测试名 `TestLifecycleCleanupPurgeRetainsIdentityWhenPublished`。
- 已定位并修复（task-24，commit `03c9a794b`）：`users.id` 是 BIGSERIAL，同分片多包共用一个 `sforum_test_suite` 库，其中 18 处夹具用显式 id 插 `users`（`id=1/2/3/7/42…`）却不推进序列；下一次默认 id 插入就会撞 `users_pkey`（对照插入的 detail 实测 `Key (id)=(2) already exists.`）。
- 复现强度：原测试单独跑 `-count=20` → 0 红（所以它自己刷不出来）；确定性复现用例（先 `nextval` 消费 n，再显式插 `id=n+1`）修复前 `-count=20` → **20/20 红**，修复后 → **0/20 红**；`--shard 1/4` 里 `ok bootstrap`。
- 判定：**测试夹具问题，不是生产缺陷**（生产注册走默认 id；唯一带显式 id 的是 `internal/trustgate` 的测试 Fixture）。修法是建用户前把序列对齐到 `max(id)`（`repairUsersIDSequence`），没有用 `ON CONFLICT` 兜底，并用对照断言钉住「重复显式 id 仍然必须报 23505」。

### 非红点（脏树幻觉，干净检出不复现）

- `TestPostgresExtensionDatabaseHighRiskLeaseObservesMigratedCoreFence`：断言 `sforum_core_runtime_state.current_version` 等于 `CoreCompatibilityVersion()`。主工作树跑到一半时同事的提交进入工作树，`git describe --dirty` 变成 `3.0.23+68.g5a1d4083f.dirty`，而测试库迁移时记录的还是干净版本 → 报版本不一致。干净检出上该测试通过。**结论：这是并发改工作树导致的环境假红，不是 HEAD 缺陷**；但它说明“边改边跑全量门禁”这个组合会自己制造红。

## 4. 环境事实

- HEAD：`5a1d4083f`（`5a1d4083f7443857e23176b4166d5a80e2863d33`），`git describe --tags --dirty` = `v3.0.23-68-g5a1d4083f`
- Go：`go1.27.1 linux/amd64`；`apps/api/go.mod` 声明 `go 1.26.10`
- Node：`v26.10.0`；Ruby：`3.3.8`
- **Bun 存在本地/CI 版本差**：本地 `1.4.2`（`744846f84`），CI 钉 `1.3.14`（`.github/workflows/ci.yml:80`、`:112`，`release-tooling.yml:52`）。
  本报告所有 web 结论都是 **1.4.2** 跑出来的（日志横幅 `bun test v1.4.2 (744846f84)`），
  所以「本地绿 / CI 红」不能先假设是代码问题，也不能反过来假设是版本问题 —— 先看版本号。
  `scripts/m5-gates.sh` 现在把 `bun --version` + CI 钉住值一起打进头部、`env.txt` 和每份 gate 日志第一行，并在两者不一致时 warn。
  **这条按「M5 收口的已知限制」登记，不单独开单**：截至目前两边从未跑过同一份用例，谁都不能保证 web 结论可互换；
  收口时以 CI（1.3.14）的结论为准，本地绿只作参考，反过来也一样。
- PostgreSQL：容器 `sforum-postgres` → `127.0.0.1:15432`，`pg_isready` = accepting connections
- Redis：容器 `sforum-redis` → `127.0.0.1:16379`，`PING` 返回 `NOAUTH Authentication required`（在监听、需口令；部分 Go 测试的 `127.0.0.1:6379` 直连会打印 `connection refused` 警告但不影响判定）
- Go 测试集群：`scripts/test-api-isolated.sh` 每次新建 4 个一次性集群，宿主端口 `127.0.0.1:55432–55435`，用完即删
- dev server（全程未干预）：`:3000` Nuxt dev（pid 150256）、`:8080` sforum-api（pid 162232）；`/tmp/web-dev.log` 最后一行 `[nitro] ✔ Nuxt Nitro server built in 9872ms`
- 机器：12 核 / 14 GB，单次 `fast` 门禁实际耗时 7m36s–8m12s
- 干净检出用的 worktree 已删除；重建：`git worktree add --detach /tmp/sforum-head-5a1d4083f 5a1d4083f && ln -s "$PWD/apps/web/node_modules" /tmp/sforum-head-5a1d4083f/apps/web/node_modules`，并在跑 web 线前先 `bunx nuxi prepare`（否则 `~/*` 别名解析不到，会出现 93 条假失败——本次已实测到该现象）

### 4.1 采基线/做变异时必须遵守的三条环境纪律（本轮踩过）

1. **边改边跑全量门禁会自己制造红。** 主工作树那次 `TestPostgresExtensionDatabaseHighRiskLeaseObservesMigratedCoreFence`
   假红就是同事的提交在门禁跑到一半进树、`git describe --dirty` 变 `…dirty` 造成的。
   采基线前先 `git status`；要判定「HEAD 绿不绿」就用干净 detached worktree（`git worktree add --detach <sha>`），
   别在有人写文件的树上跑完整门禁。
2. **`apps/web/i18n/locales/*.json` 不能用非原子写。** 手工/脚本写这两个文件会触发 Nuxt watcher，
   本轮把 `:3000` dev server 打崩过两次（都在变异测试期间）。变异协议：先 `cp` 备份 →
   写临时文件再 `mv`（同一文件系统内 `rename` 原子替换）→ 跑完 `cp` 恢复 → `sha256sum` 核对与原文件一致。
3. **dev 库的 `trust.levels` 是多 agent 共享写资源。** reviewer 做浏览器 QA 期间，他的配置在验证成功之后
   被别的任务整份回滚过。谁写谁负责：写入前 `dump`（`Options` 当前值）、验证后读回比对，收工前再核一次。

## 5. 复跑脚本失败传染的证明

把 `ruby` 换成一个恒 `exit 1` 的 shim，只跑 openapi 一条：

```
$ PATH=<shim>:$PATH ./scripts/m5-gates.sh --only openapi
==> [openapi] ruby scripts/validate-openapi-refs.rb
<== [openapi] FAIL  exit=1  14ms  log: tests/.logs/m5-gates-20261114T044745Z/openapi.log
=========================== m5 gate summary ===========================
FAIL  openapi          14ms  exit=1  ruby scripts/validate-openapi-refs.rb
M5 gates failed: openapi
$ echo $?
1
```

证据留档：`tests/.logs/m5-gates-20261114T044745Z/{openapi.log,summary.tsv}`。

## 6. 给收口的建议顺序（不含实施）

1. ~~R3 / R1~~ 已修：`1ac0c0a9b`（`chore(catalogs): 重新生成 v3 路由行号 + manifest-v3 补 userFactConditions`），一次消掉 3 个断言（`cmd/sforum` 与 `sdk/plugin`）。
2. R2 补 `docs/en-US/extensions/trust-levels.md`，消掉 static 线（pm 在办）。
3. ~~R5~~ 已修：`03c9a794b`（`users_id_seq` 对齐 + 确定性回归用例）。R4 由 `830dd2c5b` 修掉；两者都不是 flake，是确定性触发条件，别再用「偶发」记它们。
4. 收口那条命令统一用 `./scripts/m5-gates.sh`（现在会在头部/`env.txt`/每份日志里记 `bun`/`go`/`node`/`ruby` 版本与 CI 的 bun 钉住值）；不要在同事正在改文件的树上把红当成 HEAD 红。
