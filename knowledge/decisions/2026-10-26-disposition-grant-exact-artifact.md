# 卸载 disposition 的 exact-artifact 授予校验

- Date: 2026-10-26
- Status: Accepted（授予校验这一层已修并验证；卸载收敛本身仍未完成——本轮只是把
  tombstone 从第一层推到第二层，见「未解决」）
- Related: `sessions/2026-10-26-uninstall-tombstone-diagnosis.md`（第 5 节）、
  迁移 `202607150022_extension_database_additive_grants.sql`、
  `202607160027_plugin_runtime_publications.sql`、
  `decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`

## Context

卸载 tombstone 长期停在 `pending`，sweep 每 5 分钟重试、每轮全败，job 却因
`finalize_uninstall_cleanup.go:40-47` 丢弃 `result.Failed` 而显示 `completed`。
诊断定位到两层：本文件处理的第一层是
`validateExactActiveDispositionGrant`（`app/Support/Extensions/extension_database_disposition_postgres.go`）
的授予校验。

缺陷形态：该查询只按 `extension_id + status='active'` 取一行，隐含假设
「一个扩展只有一条 active grant」。这个假设与 schema 相反：

- `extension_database_grants` 的唯一索引是
  `(extension_id, extension_version_id, extension_version, package_digest) WHERE status='active'`
  （`202607150022:60`），只保证「同一 exact artifact 至多一条 active」；
- 同迁移的注释明写 *Source and target exact artifacts may both remain granted
  while their runtimes overlap*；
- `upsertExtensionDatabaseGrant`（`extension_database_registry_repository.go:151-179`）
  按 artifact upsert，从不撤销别的 artifact 的 active 行。

于是正常 install → upgrade → uninstall 必然留下 2–3 条 active grant，`QueryRow`
取到的首行是旧 artifact（生产现场：marketplace 的 4272 遮住 tombstone 要的 4283），
校验必然 `ErrExtensionDatabaseArtifactConflict`，且重试不会自愈。

## Decision

1. **exact-artifact 条件必须写进查询，不能靠「取首行再比对」。**
   `version_id / extension_version / package_digest` 三个条件与 tombstone 的
   `retained_*` 对齐；无匹配行即通过（该 artifact 没有 active 授予，没有需要
   撤销的东西）。这是本次唯一的代码改动，不涉及 schema、迁移或接口变更。
2. **「同 version 不同 digest」也不再 conflict**，与诊断报告第 5 节的
   bullet 措辞有偏差，理由记录在此：三元组匹配后，「另一 artifact 的 active
   grant」不属于本请求的校验范围；真正的防线是 disposition 之后
   `revokeExtensionDatabaseDispositionLedger` 的 extension 级 UPDATE 加上
   `active == 0` 断言，以及 purge 前 `lockExactExtension` 对
   `extensions.active_version_id` 的比对。保留「取一行再比对」的旧形状只会
   把多行现实重新引回来。
3. **不把 `revokeActiveExtensionDatabaseGrants`（`extension_database_registry_repository.go:131-149`，
   目前从未被调用）接进 provisioning。** 升级期间 source 与 target 的 runtime
   可能同时在线并各自持有 lease/credential，提前撤销旧 grant 会改变运行时语义，
   属于独立决策；grant 的真实回收点是卸载 disposition，那里已经按 extension
   撤销全部 active 行并复核归零。
4. **单 grant 路径行为不变**：既有
   `TestPostgresExtensionDatabaseDispositionAppliesEveryModeAndReplays` /
   `...PersistsTruthfulNoResourceReceipts` 等用例原样通过。

## Consequences

- 验证：新增
  `TestPostgresExtensionDatabaseDispositionToleratesStaleActiveGrant`（真库、两条
  active grant、旧 artifact 物理序在前并断言其遮蔽新 artifact）。修前
  `extension database exact artifact conflict`，修后 PASS 且两条 grant 都被
  disposition 撤销。dev 库与**全新迁移库**（`extension_versions` 序列只有个位数）
  双侧各跑 3 次全绿——旧 artifact 身份取 `version_id=1 / version="0" / digest 全零`
  这一最小三元组，不依赖库里 id 的量级。
- 现场：908/909 不再报 `extension database exact artifact conflict`，推进到
  disposition 之后的下一步；703 仍按 `active_version_id` 比对 fail-closed
  （A 类，需要 `superseded` 终态才能收尾）；1154 仍停在 actor 守卫。
- 不再有「重试也不会变」的假死循环：grant 这一层已不再是卡点。

**验收边界（终版）**：本轮**不**要求 908/909 变成 `finalized`。可验证的负向断言是
「越过授予层、停在 identity 删除的 FK RESTRICT 错误上」；真正收敛需要先解决下面
第 2 条，归 task-8。任何「修好了 / finalized」的表述都与事实不符。

## 未解决（本轮刻意不修，各自需要独立决策）

1. **P1 — actor 回退链**（`lifecycle.go:540-543`）。`requested_by_user_id`
   挂着 `ON DELETE SET NULL`，发起卸载的用户被删除后
   `lifecycleOperationActorUserID` 返回 0，provider selection 失效动作直接
   拒绝；`authority_snapshot.actorUserId` 虽然还留着原 id，但
   `extension_provider_slot_selection_events.actor_user_id` 有到 `users` 的 FK，
   回填已删除的 id 会违反外键。**放行后仍会撞下面第 2 条**，所以 actor 链
   修好不等于 1154 能收敛。
2. **P0 — identity purge 与 append-only 发布账本结构性冲突。**
   908/909 现在停在
   `delete exact lifecycle extension identity: ERROR: update or delete on table
   "extension_versions" violates foreign key constraint
   "plugin_runtime_publication_me_extension_version_id_extensi_fkey" on table
   "plugin_runtime_publication_members" (SQLSTATE 23503)`。
   约束原文：
   `FOREIGN KEY (extension_version_id, extension_id, extension_version, package_digest)
   REFERENCES extension_versions(id, extension_id, version, package_digest)
   ON DELETE RESTRICT`。
   `extension_versions.extension_id` 是 `ON DELETE CASCADE`，所以
   `DELETE FROM extensions` 必然尝试级联删除版本行，然后被发布账本 RESTRICT 挡住；
   `decisions/2026-10-17-version-gc-publication-restrict-cooperation.md` 已判定
   发布历史永不删除、被引用的版本行**永久钉住**，两张表还有 append-only
   触发器兜底。结论：**「物理删除 extensions identity 行」对任何曾 enable/upgrade
   过的插件在架构上不可达**，不是再补一行 SQL 的事。这不是 grant 修复引入的，
   只是原来在更早一步就失败了。

   tombstone 对照（finalized vs pending，版本行/发布成员数）：

   | op | 扩展 | tombstone | versions | pub members |
   | --- | --- | --- | --- | --- |
   | 30 / 34 | `p4.e2e.preserve.*` | finalized | 0 | 0 |
   | 171 | `sforum.web-search` | pending | 57 | 398 |
   | 308 / 309 | `sforum.marketplace` | pending | 7 | 422 |
   | 532 | `qa.store.proof` | pending | 2 | 2 |

   唯一 finalized 的两条对应扩展**没有任何版本行**；所有卡住的都挂着发布历史。
   可行方向只有「卸载不再删 identity 行，改为打标 + 只清理制品 / DB 资源 / 信任」
   这一条，需要新的任务书与决策（已开 task-8 出方案）。
3. **P2/A — 被重装取代的 tombstone 没有终态**（703）。`active_version_id` 已变
   仍按 fail-closed 永久 pending，列表徽标会一直误报「清理挂起」。

## 排查备忘：dev 环境双实例会让 sweep「看起来没跑」

本轮取证时 `apps/api` 下同时存在两个 `air` + 两个 `sforum-api`，连同一个库：
pid 1346530 的日志写 `/tmp/api-dev.log`，pid 1385356 挂在 `/dev/pts/3` 上（日志
不可见）。两个 River client 分食同一队列，于是某一轮周期 job 被「看不见的那个」
消费 —— 日志里完全没有 `uninstall cleanup sweep` 行，但 job 显示 `completed`，
`extension_database_grants` 也确实被改了。**这会被误判成「sweep 没跑」或「sweep
什么都没做」。** 收敛成单实例后 RunOnStart 一轮就给出了干净证据；原日志备份在
`/tmp/api-dev.log.prefix-task7`。下次排查先 `ss -ltnp | grep 8080` 对进程，
再看每个 pid 的 `/proc/<pid>/fd/1`。

