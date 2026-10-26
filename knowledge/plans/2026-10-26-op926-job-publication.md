# 2026-10-26 op 926 job publication：只读分析与方案

**Status**: `draft`（分析完成；实施未批准，先拍板 §6 的边界）
**Owner**: researcher（小研）· **Task**: task-4
**纪律**: 只读。未改 `apps/api` 生产代码、未重启 API、未动 store 仓 `tun2.html`、未移动 tag。
唯一写操作：为跑既有集成测试临时建库 `sforum_research_926`，跑完即 `DROP DATABASE`。

**一句话结论**：`target 为空` 是设计（该版本 manifest 没有 job 声明，空期望集合法）；
`按 operation_id 隔离` 是表结构使然；真正缺的是**读取侧** —— `commit_marker=true` 且未对账的
job publication 没有任何启动期重放入口，而 `reconciliation_state='pending'` 把「待重放」和
「已作废」两种语义混成了一种。op 926 属于后者，当前**没有任何用户可见后果**。

---

## 0. 证据边界

- 数据库：`postgres://sforum:sforum@127.0.0.1:15432/sforum`（容器 `sforum-postgres`），只读 SQL。
- 代码：`apps/api` 当前工作树（有他人未提交改动，与本分析无关，未触碰）。
- 可执行验证：`TestPostgresLifecycleBoundaryJobsCrashRestartCommitAndConcurrentReconcile`
  在隔离库实跑 PASS（§4.5）。
- 本机数据规模：job publication **623** 行 / 父 publication **632** 行 /
  River 中 `kind='extension.plugin_job'` **0** 行。

## 1. 事实基线

### 1.1 op 926 到底发生了什么

| 字段 | 值 |
| --- | --- |
| `operation` / `state` / `terminal_result` | `upgrade` / `failed` / `cancelled` |
| `extension` | `sforum.fetch-url` 1.0.2（target version_id 20827） |
| `current_step_id` | `lifecycle.upgrade.08.host.enabled`（position 8，`hostSideEffectsStarted=true`） |
| `error_code` | `lifecycle.orphaned`（"host reclaimed the orphaned lifecycle operation"） |
| 父 publication | `commit_marker=false`，`committed_attempt=NULL` |
| job publication | `publication_state=target`，`reconciliation_state=pending`，`revision=2` |
| `target_snapshot.jobs` / `reconciliation_plan.entries` | `[]` / `[]` |
| state publication | `transaction_state=source`，`restored_at=2026-10-26 09:32:50` |
| registry publication | `transaction_state=source` |

时间线：

1. `2026-10-25 08:15:50` 创建 op 926（`requested_by_user_id=157`，authority=`trust_grant` 699）。
2. `08:15:51.805` 父 publication 写入，marker 未提交。
3. `08:15:52.377` job publication 写入；`08:15:52.475` 因 rebind 升到 `revision=2`。
4. 发布序列是 `state → jobs → registries → commit`（`lifecycle_composed_boundary_publication.go:59,62,65,71`）。
   现场是 **state 已 target、jobs 已 target、registries 仍 source** → 进程死在
   `jobs.Publish` 之后、`registries.Publish` 之前。
5. `08:16:24` 被孤儿回收关闭（`lifecycle_repository_orphan.go:39-112`），进入 terminal。
6. `2026-10-26 09:32:50` 启动期 interrupted-activation 修复把 state 拉回 source
   （审计 `11057`，`actor_user_id` 为空）。该修复**按契约不碰 jobs**
   （`LifecycleRecovery/activation_state.go:39-53`："never touches runtime instances, registries, jobs, or the drain guard"）。
7. 之后 `sforum.fetch-url` 经 op 1147 升到 1.0.3 并成功；op 926 的 job publication 就此定格。

### 1.2 三个 publication 的分工与启动期待遇

| 账本 | 写者 | 启动期谁读 | 权威事实 |
| --- | --- | --- | --- |
| `extension_lifecycle_state_publications` | state 事务 | `RecoverInterruptedActivations`（仅中断态修复） | `extensions.active_version_id` |
| `extension_lifecycle_registry_publications` | registry 事务 | `RestoreRoutePublications`（无条件恢复） | 路由/身份发布快照 |
| `extension_lifecycle_job_publications` | `PostgresLifecycleBoundaryJobs` | **无人** | `river_job` 自身 |

这张表的不对称就在这里：另外两个成员都有启动期收敛路径，jobs 没有。

## 2. 判定一：「target 为空」是设计，不是写入遗漏

### 2.1 先排除字面误读

`target_extension_id`、`target_extension_version`、`target_package_digest`、
`target_version_id`、`target_runtime_instance_id` 全部 `NOT NULL` 且带非空/格式 CHECK
（`database/migrations/202607140010_extension_lifecycle_job_publications.sql:21-28`）。
op 926 这五列**都有值**。所以「target 为空」只可能指**期望 job 集合为空**：
`target_snapshot.jobs=[]`、`reconciliation_plan.entries=[]`。

### 2.2 空集合是这一版 manifest 的真实期望

- 写入入口：`PrepareLifecycleJobPublication`（`app/Support/Extensions/lifecycle_jobs_postgres.go:63`），
  INSERT 在 `118-145`，`ON CONFLICT ... DO NOTHING` 后由 `149-185` 做 rebind。
- 快照构造：`buildLifecycleJobSnapshot`（`lifecycle_jobs_boundary.go:356`）。
  `Jobs` / `Schedules` 先初始化为**空切片**（`367-371`），只有遍历 `extension.Manifest.Jobs`
  才会追加（`388-409`），`Schedules` 同理（`411` 起）。
- 证据：target 版本 `extension_versions.id=20827` 的 `manifest->'jobs'` 是 **null**（SQL 见附录 A）。
  该扩展在 trust impact 里也是 `"jobs": []`。
- 旁证：无 job/schedule 声明时 `trustGrantId` 被刻意置空（`375-387` 的 else 分支），
  行里的 `target_snapshot` 确实没有 `trustGrantId` 字段。

### 2.3 计划为空同样是真实期望

`reconciliation_plan` 不是「要创建什么」，而是「对**已经存在**的 River 行做何处置」：
`LockPluginJobs` 只枚举 `river_job WHERE kind='extension.plugin_job' AND args->>'extensionId'=$1`
（`app/Support/HostAPI/plugin_job_lifecycle_postgres.go:62-76`），planner 逐行给
execute/drain/migrate/cancel（`plugin_job_lifecycle.go:190-221`）。
本机该 kind 的 River 行是 **0 行** → 计划必然为空。空计划合法，不是丢数据。

### 2.4 反证：如果是写入漏了，会看到什么

- 若漏，带 job 声明的扩展也会得到空快照。但写入是同一段循环，
  `TestPostgresLifecycleBoundaryJobsCrashRestartCommitAndConcurrentReconcile` 用带 job+schedule
  的 manifest（`lifecycle_jobs_postgres_integration_test.go:28`、`attachLifecycleJobManifest`）
  跑通了完整 prepare→publish→commit→reconcile，本轮实跑 PASS（§4.5）。
- 更硬的一条：本库 623 行 job publication **没有任何一行**的
  `target_snapshot.jobs` 或 `reconciliation_plan.entries` 非空。原因是本机唯一声明 job 的
  `sforum.gamification` 在 ledger 里一条 op 都没有（它是 builtin，经 `SyncBuiltins` 同步，当前 disabled，从未走过 lifecycle 发布）。
  **所以「非空路径」目前只有测试证据、没有生产数据证据** —— 这一点必须在实施时补一次真机验证（§8）。

**判定：设计如此。** 空集合是合法的期望值，写入路径忠实记录了它。

## 3. 判定二：「按 operation_id 隔离」是表结构使然

- 表**没有 `extension_id` 列**；`UNIQUE (operation_id, step_id, publication_mode)`
  （migration:56），外键级联到父 publication（`57-59`）。
- 全部读取都是这个三元组：`lifecycle_jobs_postgres.go:489-493`（source 角色）、
  `:516-528`（target 角色）、`loadLifecycleJobPublication`（`:712-728`）。
  没有按扩展、按状态、按时间的列表查询。
- 语义上说得通：这条记录属于**一次 step 尝试**，不属于扩展。
- 代价：想知道「某扩展有没有悬空发布」必须 join `extension_lifecycle_operations`；
  4 条 `target+pending` 只能靠手写 SQL 才看得见（附录 A）。

**值得注意的物证**：迁移建了一个部分索引

```sql
CREATE INDEX extension_lifecycle_job_publications_reconcile_idx
  ON extension_lifecycle_job_publications (reconciliation_state, updated_at, operation_id)
  WHERE publication_state = 'target' AND reconciliation_state <> 'succeeded';
```

形状就是「扫出待对账的 target 行」。但全仓没有任何查询使用它（`rg` 只命中迁移与其测试）。
→ **扫描器被设计过、索引先落地了、查询没写。** 仓库里也没有对应 decision record
（`knowledge/decisions` 无 job publication 相关条目）。

## 4. 判定三：「启动不读 / 不可达」是缺陷（读取侧缺线），当前后果为零

### 4.1 启动装配逐段读什么、跳什么

`restoreAPIExtensionPlatform`（`bootstrap/api_assembly_extension_restore.go:45`）的顺序：

1. `SyncBuiltins` / `syncExternalExtensionSources`（`59-82`）。
2. `ListOpenLifecycleOperations` **只打日志**（`86-100`）。
3. `RecoverInterruptedActivations`（`156-171`）—— 只修 **state**，契约明确不碰 jobs。
4. `startPluginRuntimeCoordinator`（`175-187`）→ genesis 里做孤儿回收
   （`bootstrap/plugin_runtime_coordinator.go:540-557`）→ full-set 收敛
   （`Support/Extensions/plugin_runtime_full_set.go:45`）。
5. `RestoreRoutePublications`（`221`）—— **registries** 的启动恢复。
6. 绑定 asset registry（`234`）。

四类持久事实里，state 有修复、registries 有恢复、runtime 有收敛、**jobs 没有**。
根因不是疏忽某个函数，而是把「River 本身是持久的」当成了「队列转移不需要恢复」——
对**稳态**成立，对**崩溃窗口**不成立。

### 4.2 谁在读 job publication

只有操作内三处（§3），且都要求一个活的 `LifecycleBoundaryRequest`：

- `ReconcileCommittedLifecycleJobs` 要求 `ActorUserID>0 && AuditEventID>0`
  （`lifecycle_jobs_postgres.go:324-326`），只有 composed boundary 在
  `state→jobs→registries→commit` 之后调用（`lifecycle_composed_boundary_publication.go:78-82,129-135`）。
- 操作一旦 terminal，这条路径就消失。

### 4.3 「不可达」的确切含义

一行 `reconciliation_state<>'succeeded'` 在其操作 terminal 之后，**没有任何代码路径能推进它**：

- 启动期不扫（§4.1）；
- 管理端 retry 是「重跑整个 operation」（`app/Models/Extensions/service_lifecycle_recovery.go:57-79`），
  要先过 preflight（`:65`）；op 926 这种已被后续版本超越的操作过不了；
- CLI 的 `extension recovery` 子树只有 `lifecycle-close` 与 `activation-state`（`cmd/sforum/recovery_lifecycle_close.go:55-57`），
  没有 job 子命令；
- 没有定时任务扫这张表：`reconciliation_state` 的非测试引用只有 3 条 UPDATE
  （`lifecycle_jobs_postgres.go:375-388`、`417-431`、`450-463`）与 2 条 SELECT（`489-503`、`712-728`），全部在操作内。

### 4.4 后果矩阵（必须区分两种 `pending`）

| 情形 | 本库行数 | River 是否被改 | 重启后能续跑 | 用户可见症状 |
| --- | --- | --- | --- | --- |
| `marker=false` + `target`（op 926） | 4（op 115/635/686/926） | 否 | 不需要 | 无。纯账本残留 |
| `marker=false` + `source` | 36 | 否 | 不需要 | 无。prepare 残留 |
| **`marker=true` + 未对账** | **0** | admission 已关、队列未对账 | **不能** | 有 job 声明的扩展：旧版本在途 job 继续跑新制品（stale artifact/trust 失败）、目标 job 不迁移；disable/uninstall 留孤儿行反复失败重试 |
| `succeeded` | 583 | 是 | — | — |

要点：**marker 才是权威**。`publication_state=target` 单独出现不会动 River ——
`ReconcileCommittedLifecycleJobs` 在 `338-344` 先查 `LifecyclePublicationCommitted`，
不通过直接返回 `ErrLifecycleBoundaryJobsUncommitted`。所以 op 926 的残留是**惰性的**，
不是运气。

真实风险只在第三行：结构上可能、本库从未发生（`committed_unreconciled=0`）。
它不是「op 926 的问题」，而是同一张表上尚未发生但确实存在的缺口。

### 4.5 可执行证据

```
$ docker exec ... CREATE DATABASE sforum_research_926
$ cd apps/api && SFORUM_TEST_DATABASE_URL='postgres://sforum:sforum@127.0.0.1:15432/sforum_research_926?sslmode=disable' \
    go test ./app/Support/Extensions/ -run TestPostgresLifecycleBoundaryJobsCrashRestartCommitAndConcurrentReconcile -count=1 -v
=== RUN   TestPostgresLifecycleBoundaryJobsCrashRestartCommitAndConcurrentReconcile
--- PASS: TestPostgresLifecycleBoundaryJobsCrashRestartCommitAndConcurrentReconcile (1.31s)
PASS
ok  github.com/keeptrue/sforum/apps/api/app/Support/Extensions  1.476s
$ docker exec ... DROP DATABASE sforum_research_926
```

该用例证明：崩溃窗口内的重放**幂等且并发安全**（16 路并发重放全部成功、`reconciliation_attempt` 仍为 1，
`lifecycle_jobs_postgres_integration_test.go:127-148`），
**但调用方必须显式发起** —— 测试自己手动调 `ReconcileCommittedLifecycleJobs`（`:136`、`:179`），
不是启动钩子。这既证明方案可行，也证明现在缺的正是那根线。

## 5. 方案对比

### A. 启动期独立扫描器

新增一个独立协作者 + 一个**新的启动调用点**，扫 `reconcile_idx` 覆盖的行，逐行重建
`LifecycleBoundaryRequest` 后调用 `ReconcileCommittedLifecycleJobs`。

- 改动面：新文件（扫描 + 请求重建）约 250–350 行 + bootstrap 接线 + 集成测试。
  请求重建需要：精确版本聚合（source/target 各一份，参考
  `service_lifecycle_v2.go:438-483` 的 `rebuildLifecycleReplay`）、
  行内已存的 bindings/position/attempt、`ActorUserID/AuditEventID`。
- 迁移：0（只重放 `marker=true` 的行）；若要给 `marker=false` 的残留终态，需 1 个迁移（见 B）。
- 风险：与 LifecycleRecovery 形成**第二个启动钩子 + 第二套审计口径**；两处都要维护
  「先回收孤儿、再重放」的顺序；advisory lock 必须与操作内一致
  （`lifecycleJobAdvisoryLockKey`，`lifecycle_jobs_postgres.go:350-355`）。
- 验收：重启后 `committed_unreconciled=0`；op 926 行不被重放；双进程同时启动只生效一次。

### B. 并入既有 lifecycle recovery 快照（推荐）

在 `LifecycleRecovery` 阶段新增一个**只做判定**的协作者（纯 SQL，exact evidence），
把「重放」作为接口注入（由 `PostgresLifecycleBoundaryJobs` 实现），
由**同一个启动钩子**驱动，位置在孤儿回收之后、runtime 收敛之前。

- 改动面：`LifecycleRecovery/` 新协作者约 150 行 + 一个 replay 接口 +
  bootstrap 接线 + 扩宽该包的文档契约（现在写着 never touches jobs）+ decision record。
- 迁移：给 `marker=false` 的残留一个终态需要 1 个迁移（新增 `abandoned` 值 + 放宽
  `reconciled_by_user_id>0` 对系统审计的约束，见 §9 Q1）；只重放的话 0 迁移。
- 风险：扩宽 `LifecycleRecovery` 的职责边界（它当前的价值就是「只碰 state、证据极窄」）；
  重放要重算 material，需要 live trust（`lifecycle_jobs_boundary.go:375-383`）与
  migrations 协作者，失败必须按 `ErrLifecycleBoundaryJobsConflict` 跳过而不是报错。
- 验收：同 A，外加 `LifecycleRecovery` 既有用例不回归、`recovery activation-state` 行为不变。

**顺序陷阱（A/B 都有）**：`api_assembly_extension_restore.go:156` 这一时刻孤儿还没被回收
（回收发生在 `175-187` 的 coordinator genesis 里）。所以扫描器必须先显式调一次
`ReclaimOrphanedLifecycleOperations`，或把重放放到 coordinator 收敛之后。
否则「刚崩、lease 还没过期」的操作会被当成「非 terminal」跳过，本轮永远不重放。

### C. 维持现状 + 显式补偿入口

只加只读盘点 CLI（`extension recovery job-publications --status pending`）+ 可选的管理端
「重新对账」动作，不动启动路径。

- 改动面：CLI 一个子命令 + 可选一个 admin 路由/动作（照 `routes.go:71-73` 与
  `lifecycle.go:60-81` 的形态）+ OpenAPI + 前端按钮。
- 迁移：0（除非要终态）。
- 风险：**依赖人发现**。队列已经分叉到下一次有人想起它；op 926 这类残留会永久 pending。
- 验收：CLI 能列出 40 行并区分 committed/void；对 committed 行执行后归零；
  对 void 行拒绝执行并说明原因。

| | 改动面 | 迁移 | 风险 | 能否覆盖「进程死在任何位置」 |
| --- | --- | --- | --- | --- |
| A 独立扫描器 | 中（新协作者+新钩子） | 0–1 | 双启动钩子、双审计口径 | 能 |
| B 并入 recovery | 中（新协作者+接口+契约扩宽） | 0–1 | 扩宽既有包的边界 | 能 |
| C 显式补偿 | 小 | 0–1 | 依赖人、残留永久化 | 不能 |

## 6. 推荐

**推荐 B**，分两步落地；**C 作为 B 的第一步交付，不作为替代**；A 不推荐。

理由：

1. 只有启动期能覆盖「进程死在任何位置」；C 依赖人发现，不构成恢复语义。
2. 重放原语已经存在且已被证明幂等、并发安全（§4.5），B 不需要新机制，只需要接线。
3. A 会造出第二个启动钩子与第二套审计口径，和 `LifecycleRecovery` 的既有纪律重复；
   同一个启动点放两个协作者，评审面更小。
4. C 的只读盘点同时是 B1 的观察面，先做它能把「40 行 pending」当场分类，零风险。

若评审认为「`LifecycleRecovery` 只碰 state」是硬边界，则退化为 A 的形态：
同一启动点、独立协作者、共享 advisory lock 与审计口径。

## 7. 最小落地步骤（后续单独立项可直接照做）

1. **S1 只读盘点**：`sforum extension recovery job-publications`（默认 dry-run，
   输出 `operation_id / extension_id / marker / publication_state / reconciliation_state /
   jobs / plan_entries`）。零迁移、零行为变化。
2. **S2 定终态**：给 `marker=false` 的残留一个终态（建议新增 `abandoned`，
   而不是复用 `failed`）。迁移：`CHECK` 扩值；`reconciled_by_user_id` 若沿用现有 `>0`
   约束，就取 `operation.requested_by_user_id`（本机 157），否则要为该状态放宽约束允许系统审计
   （审计 `actor_user_id` 本身可空，参考 11057）。迁移 Down 的「不得删除历史」保护照抄。
3. **S3 重放协作者**：在 `LifecycleRecovery` 新增 `UnreconciledJobPublications` 扫描，
   只挑 `marker=true AND reconciliation_state<>'succeeded'`；按
   `reconcile_idx` 顺序取批；逐行 `FOR UPDATE` 复核后交注入的 replay 回调。
4. **S4 请求重建**：从 `extension_versions` 取精确 source/target 聚合
   （参考 `service_lifecycle_v2.go:438-483`），bindings/position/attempt 取自 job publication 行本身，
   actor 取 `operation.requested_by_user_id`，审计新写一条系统事件。
5. **S5 接线顺序**：`api_assembly_extension_restore.go:156` 处先
   `ReclaimOrphanedLifecycleOperations`，再跑 S3；或整体放到 coordinator 收敛之后。
   重放失败按 `ErrLifecycleBoundaryJobsConflict` **跳过并留日志**，不阻断启动。
6. **S6 作废残留**：S2 落地后，把 4 条 `marker=false + target` 标成 `abandoned`
   （带系统审计），op 926 从此不再出现在 pending 里。
7. **S7 真机验证**：用 `sforum.gamification`（唯一声明 job 的内置插件）做一次
   enable→kill→重启，确认非空快照路径与重放都在真实数据上走过（§2.4 的空白）。

## 8. 验收标准

- 重启后 `committed_unreconciled = 0`；op 926 行**不被重放**（marker=false 是硬门）。
- 双进程并发启动只生效一次（advisory lock + `revision` CAS）。
- 无 job 声明的扩展零变化：623 行快照逐字节不变。
- `cd apps/api && go test ./app/Support/Extensions/` 绿；新增用例覆盖
  「marker=true + 崩溃」与「marker=false + 残留」两条分支。
- `go build ./...`、`node tests/validate-architecture-boundaries.mjs` 绿。
- 一条 decision record：job publication 的恢复职责归属与 `abandoned` 语义。

## 9. 未决问题

1. 终态用新值 `abandoned` 还是复用 `failed` + error code？新值更诚实，但要动 CHECK 与
   `reconciled_by_user_id>0` 的约束（系统审计没有真人 actor，参考审计 11057 的 `actor_user_id` 为空）。
2. 重放的审计主体：`operation.requested_by_user_id`（本机 157）还是专用系统账号？
   两者都会让 `reconciled_by_user_id` 看起来像「这个人对过账」，需要文案上区分。
3. `LifecycleRecovery` 是否接受职责扩宽？（B 的前提；否则走 A 形态）
4. 36 条 `marker=false + source` 的 prepare 残留要不要一起作废？它们同样永久 pending，
   但语义是「从未开始发布」，风险更低。
5. 启动期重放只做队列对账，不调用 `ResumeLifecycleJobs`；admission 由 full-set 收敛给出
   （`plugin_runtime_full_set.go:45`）。这条是**推断**，实施时必须用一次真机重启确认。

## 10. 相关计划（互指）

`plans/2026-10-26-uninstall-identity-purge.md` 处理同一类问题的另一半：tombstone 的物理副作用
永远不可能完成时如何收尾。两篇**没有冲突推荐**——本篇只加 job publication 的**读取侧**重放，
不碰发布账本；那篇只改 uninstall 的**终态判据**，同样不碰发布账本。

术语约定（不要合并成一个词）：`abandoned`（本篇：副作用因回滚不再适用）≠ `superseded`
（那篇：artifact 被更晚操作取代）≠ `finalized`（副作用已完成）。两者作用的表也不同：
`extension_lifecycle_job_publications` vs `extension_lifecycle_cleanup_records`。

## 附录 A：复现 SQL（只读）

```sql
-- op 926 的 job publication 与父 marker
SELECT j.operation_id, j.publication_state, j.reconciliation_state,
       jsonb_array_length(j.target_snapshot->'jobs')      AS target_jobs,
       jsonb_array_length(j.reconciliation_plan->'entries') AS plan_entries,
       p.commit_marker
FROM extension_lifecycle_job_publications j
JOIN extension_lifecycle_publications p
  ON p.operation_id=j.operation_id AND p.step_id=j.step_id AND p.publication_mode=j.publication_mode
WHERE j.operation_id = 926;

-- 全表分布：623 / 583 succeeded / 36 source+pending / 4 target+pending
SELECT publication_state, reconciliation_state, count(*)
FROM extension_lifecycle_job_publications GROUP BY 1,2 ORDER BY 1,2;

-- 是否出现过「已提交但未对账」
SELECT count(*) FROM extension_lifecycle_job_publications j
JOIN extension_lifecycle_publications p
  ON p.operation_id=j.operation_id AND p.step_id=j.step_id AND p.publication_mode=j.publication_mode
WHERE p.commit_marker AND j.reconciliation_state <> 'succeeded';

-- target 版本是否真的没有 job 声明
SELECT id, extension_id, version, manifest->'jobs' AS jobs
FROM extension_versions WHERE id = 20827;

-- River 里是否有该扩展的 job 行
SELECT count(*) FROM river_job WHERE kind='extension.plugin_job';
```

## 附录 B：证据索引

| 事实 | 位置 |
| --- | --- |
| 表定义 / 非空 CHECK / 唯一键 / 部分索引 | `apps/api/database/migrations/202607140010_extension_lifecycle_job_publications.sql:5-104` |
| 写入 + rebind | `apps/api/app/Support/Extensions/lifecycle_jobs_postgres.go:63-194` |
| publication_state 翻转 | 同上 `:219-278` |
| 对账（marker 门 + 状态机） | 同上 `:315-438` |
| 失败落账 | 同上 `:441-464` |
| 三元组读取 | 同上 `:489-503`、`:516-536`、`:712-728` |
| 快照构造（manifest 驱动） | `apps/api/app/Support/Extensions/lifecycle_jobs_boundary.go:356-410` |
| 计划构造（River 行驱动） | `apps/api/app/Support/Extensions/lifecycle_jobs_boundary.go:307-354` |
| 发布序列与对账调用点 | `apps/api/app/Support/Extensions/lifecycle_composed_boundary_publication.go:13-89,91-137` |
| 启动装配 | `apps/api/bootstrap/api_assembly_extension_restore.go:86-100,156-187,221` |
| 恢复契约（不碰 jobs） | `apps/api/app/Support/Extensions/LifecycleRecovery/activation_state.go:39-53,102-140` |
| 孤儿回收 | `apps/api/app/Models/Extensions/lifecycle_repository_orphan.go:39-112` |
| 管理端 retry 的前置条件 | `apps/api/app/Models/Extensions/service_lifecycle_recovery.go:44-79` |
| CLI 现有两个恢复子命令 | `apps/api/cmd/sforum/recovery_lifecycle_close.go:50-58` |
| 崩溃重放集成测试 | `apps/api/app/Support/Extensions/lifecycle_jobs_postgres_integration_test.go:25-200` |
