# 2026-10-26 卸载 tombstone 卡 pending 诊断

范围：`qa.store.proof`(op 1154) 与既有 `sforum.web-search`(op 703)、`sforum.marketplace`(op 908/909)。
只做诊断，未改生产代码；SQL 全部只读（`FOR UPDATE` 只在 `BEGIN ... ROLLBACK` 事务里跑过）。

## 结论（判定 B，附逐行分类）

| op | 扩展 | 卡在哪 | 分类 |
|---|---|---|---|
| 703 | sforum.web-search | host identity exact-artifact 校验 | **A 账本残留**（代码正确，重新安装使 active identity 变了） |
| 908 | sforum.marketplace | database disposition 的 active-grant 校验 | **B 状态机缺陷** |
| 909 | sforum.marketplace | 同上（同一 artifact 的第二次 uninstall，同样失败） | **B** |
| 1154 | qa.store.proof | provider 清理的 actor/audit 守卫；修好后还会撞上 908 同一个 grant 缺陷 | **B**（叠加数据老化） |

判定 **B**：正常路径（install → upgrade → uninstall）也会走不到 finalize，必须修。
反证很硬：库里历史上唯一 2 条 `finalized` 的 tombstone（`p4.e2e.preserve.*`）各自只有 **1** 条 grant 记录；
4 条卡住的各有 **2–3** 条 `active` grant。`validateExactActiveDispositionGrant` 只取任意一行。

## 1. 现场取证

### 1.1 台账（`extension_lifecycle_operations`）

```
 id  | extension_id      | operation | state        | terminal_result | removal_mode | completed_at
-----+-------------------+-----------+--------------+-----------------+--------------+---------------------------
 703 | sforum.web-search | uninstall | uninstalling | succeeded       | preserve     | 2026-10-19 02:30:03.920551+08
 908 | sforum.marketplace| uninstall | uninstalling | succeeded       | preserve     | 2026-10-24 21:03:30.814773+08
 909 | sforum.marketplace| uninstall | uninstalling | succeeded       | preserve     | 2026-10-24 21:04:02.92337+08
1154 | qa.store.proof    | uninstall | uninstalling | succeeded       | preserve     | 2026-10-26 10:52:30.941344+08
```

四条都满足 sweep 谓词（uninstall + succeeded + completed_at 非空）。

### 1.2 tombstone（`extension_lifecycle_cleanup_records`）

```
op   | step_id                                  | pos | cleanup_mode      | status  | retention_marker            | physical_i/p/rr | finalized_* | revision
-----+------------------------------------------+-----+-------------------+---------+-----------------------------+-----------------+-------------+---------
703  | lifecycle.uninstall.06.host.uninstalling | 6   | uninstall_preserve| pending | retained-data-<cleanup_id>  | t/t/t           | NULL        | 1
908  | lifecycle.uninstall.06.host.uninstalling | 6   | uninstall_preserve| pending | retained-data-<cleanup_id>  | t/t/t           | NULL        | 1
909  | lifecycle.uninstall.06.host.uninstalling | 6   | uninstall_preserve| pending | retained-data-<cleanup_id>  | t/t/t           | NULL        | 1
1154 | lifecycle.uninstall.06.host.uninstalling | 6   | uninstall_preserve| pending | retained-data-<cleanup_id>  | t/t/t           | NULL        | 1
```

`finalized_at / finalized_operation_revision / finalized_operation_completed_at / purge_receipt_id / purge_proof / purge_proof_digest`
全部为 NULL。这与迁移里的强耦合 CHECK 一致：`pending` 必须 `physical_* = true`，`finalized` 必须
`physical_* = false` 且回执齐全（`apps/api/database/migrations/202607140008_extension_lifecycle_cleanup_records.sql:114-135`）。
所以这些行不是"半写坏"，而是**从未进入 finalizer 的 UPDATE**。

**step_id 不是历史残留**（线索里"只出现在测试"是假象）：
`lifecycle.uninstall.06.host.uninstalling` 由 `lifecycleCoordinatorStepID()` 生成
（`apps/api/app/Models/Extensions/lifecycle_coordinator_terminal.go:191-202`，`action == ""` 时拼成
`lifecycle.<op>.<pos>.host.<state>`），并由 staging 侧同一公式校验
（`apps/api/app/Support/Extensions/lifecycle_cleanup_postgres.go:207-210`）。它是当前代码的规范值。

### 1.3 目标 identity 现状（`extensions` / `extension_versions`）

```
extensions: sforum.web-search  active_version_id=21983 (1.1.0, digest 9806a5dd…)   status=enabled
            sforum.marketplace active_version_id=4283  (0.1.0, digest d6c41b93…)   status=disabled
            qa.store.proof     active_version_id=21890 (0.2.0, digest 38ce1e20…)   status=disabled
```

- 703 的 tombstone 指向 `retained_version_id=14155`（1.0.0 / `9abf2558…`）→ **与当前 active 不同**。
  时序：10-19 02:30 op 703 uninstall；02:40 op 710 用 `852913b9…` 重新 install，后续升级到 1.1.0。
- 908/909 指向 `4283`，1154 指向 `21890` → **与当前 active 完全一致**，即"台账说卸载成功，但物理清理一步没做"。

### 1.4 根因数据：多条 `active` grant

```
extension_id        | grants | 状态
--------------------+--------+---------------------------
sforum.marketplace  | 3      | 4272(active), 4283(active), 11345(active)
qa.store.proof      | 2      | 21889(active), 21890(active)
p4.e2e.preserve.*   | 1 each | revoked  ← 历史上唯一成功 finalize 的两条
```

唯一索引只保证"同一 artifact 只有一条 active"，不保证"一个扩展只有一条 active"
（`apps/api/database/migrations/202607150022_extension_database_additive_grants.sql:60`）：

```sql
CREATE UNIQUE INDEX extension_database_grants_active_artifact_idx
  ON extension_database_grants (extension_id, extension_version_id, extension_version, package_digest)
  WHERE status = 'active';
```

实测 `QueryRow` 取到的就是**第一行**（在 `BEGIN … ROLLBACK` 里跑同一条 SQL）：

```
marketplace  -> 4272|0.1.0|efdfd483ae0d     (tombstone 要的是 4283|d6c41b933f8c)
qa.store.proof -> 21889|0.1.0|d4f55069e22e   (tombstone 要的是 21890|38ce1e200ad7)
```

## 2. sweep 有没有被调度：有，而且一直在跑

- 周期任务：`extensions.finalize_uninstall_cleanup`，`Interval: 5 * time.Minute`、`RunOnStart: true`
  （`apps/api/app/Support/Jobs/core_schedules.go:101-110`）。
- 只在 embed 装配注册：`apps/api/bootstrap/worker.go:819` → `applyUninstallCleanupSweepRegistration`
  （`apps/api/bootstrap/worker_uninstall_cleanup.go:13-30`）；`deps.UninstallCleanupSweeper` 由
  `apps/api/bootstrap/api_assembly_http.go:161` 注入 `extensions.NewCleanupConvergenceService(...)`。
- 手工入口：`POST /admin/extensions/:id/uninstall-cleanup/finalize`
  （`apps/api/app/Http/Controllers/Extensions/routes.go:52`，super_admin，`FinalizeUninstallCleanupForExtension`）。
- 运行证据：`river_job` 中该 kind 每 5 分钟一条 `completed`（33367/33353/33341…）；
  `/tmp/api-dev.log` 自 19:38 起 52 条 `uninstall cleanup sweep item failed`（13 轮 × 4 条），
  错误每轮完全相同。

**为什么 job 一直 completed 却什么都没推进**：`Work` 丢弃了 `result.Failed`
（`apps/api/app/Jobs/Extensions/finalize_uninstall_cleanup.go:40-47`），而
`RunPendingUninstallCleanupSweep` 对单条失败只记 `slog.Warn` 并 `continue`，整体返回 `nil`
（`apps/api/app/Models/Extensions/lifecycle.go:862-874`）。所以 River 侧没有任何失败信号/重试/告警。

## 3. 逐行卡点（函数 + 行号）

finalize 的**前置校验全部通过**：`loadLifecycleCleanupFinalizationCandidate` +
`validateTerminalSuccess`（`apps/api/app/Support/Extensions/lifecycle_cleanup_finalizer_postgres.go:102-108`、
`272-294`）——operation=uninstall、terminal=succeeded、completed_at 非空、target==operation artifact、
`removal_mode` 映射到 `uninstall_preserve` 全部成立。**不是"台账没成功"**。

### 3.1 op 703 —— `bootstrap/extension_lifecycle_cleanup_postgres.go:125-127`

```go
if !activeVersionID.Valid || activeVersionID.Int64 != request.RetainedVersionID {
    return errProductionLifecycleCleanupConflict
}
```

当前 `active_version_id=21983` ≠ `retained_version_id=14155` → 直接 conflict，purge 与 disposition 都不会跑。
日志：`purge lifecycle cleanup …: bootstrap: production lifecycle cleanup exact artifact conflict`。

这是**刻意的 fail-closed**：扩展已被重新安装成另一个 artifact，旧 tombstone 的物理清理不再适用
（若强行 `commitPurge`，`DELETE FROM extensions WHERE id=$1 AND active_version_id=$2` 也会 0 行 → conflict，
见同文件 `:182-191`）。→ **A 类**。

副作用：`pendingUninstallCleanups` 按扩展投影徽标（`controller.go:330-333`），
所以**当前已安装并启用的 web-search 仍会被标记为「清理挂起」**（代码路径推导，未做浏览器核对）。

### 3.2 op 908 / 909 —— `extension_database_disposition_postgres.go:59` → `:334-357`

purger 执行顺序（`apps/api/bootstrap/extension_lifecycle_cleanup.go:45-103`）：
`validateRequest` → `beginProductionLifecycleCleanupIdentity`（runtime + identity 锁）→
`inspectProductionLifecyclePackage`（digest 匹配）→ `requireRuntimePurged` → **database disposition** → 包删除 → identity 删除。
908/909 已经走到第 5 步，说明 identity/package/runtime 三道 fence 都过了，包和进程都没问题。

卡点：

```go
// extension_database_disposition_postgres.go:334-357
err := tx.QueryRow(ctx, `
    SELECT extension_version_id, extension_version, package_digest
    FROM extension_database_grants
    WHERE extension_id = $1 AND status = 'active'
    FOR UPDATE
`, artifact.ExtensionID).Scan(...)
...
if versionID != artifact.VersionID || version != artifact.Version || digest != artifact.PackageDigest {
    return ErrExtensionDatabaseArtifactConflict
}
```

`QueryRow` 无 `ORDER BY`、无 artifact 条件 → 取首行 4272，与 tombstone 的 4283 不等 →
`ErrExtensionDatabaseArtifactConflict`（文案 `extension database exact artifact conflict`，定义在
`extension_database_registry.go:23`）。
日志：`apply lifecycle database disposition: extension database exact artifact conflict`。

908 与 909 是同一 artifact 相隔 32 秒的两次 uninstall，两次都在同一处失败——**重试不会自愈**。

### 3.3 op 1154 —— `lifecycle.go:540-543`，然后才是 3.2 的同一个坑

```go
if s.providerSlotSelections != nil {
    if actorUserID <= 0 || auditEventID <= 0 || reasonCode == "" {
        return fmt.Errorf("provider slot selection invalidation requires actor and audit evidence")
    }
```

`lifecycleOperationActorUserID(operation)`（`lifecycle_coordinator_drive.go:726-731`）返回 0，因为
`extension_lifecycle_operations.requested_by_user_id IS NULL`。原因是 FK 语义：
`requested_by_user_id REFERENCES users(id) ON DELETE SET NULL` —— 发起卸载的 QA 用户（id 219）已被删除，
连带 `audit_events.actor_user_id` 也变 NULL（同一 FK 规则）。

注意 `authority_snapshot.actorUserId = 219` 仍然完好（JSON 不依赖 users 表），
但 219 已不在 `users` 里；`extension_provider_slot_selection_events.actor_user_id` 也有 FK 到 `users`，
直接回填 219 在有 selection 时会违反外键。

该扩展 `extension_provider_slot_selections` 计数为 0，所以一旦 actor 守卫放行，这一步实际是 no-op；
**下一个拦路的就是 3.2 的 grant 缺陷**（21889 vs 21890），已按同样的 SQL 验证首行是 21889。

## 4. 根因

- **B1（主因）**：`extension_database_grants` 允许一个扩展存在多条 `active` 行，而
  `validateExactActiveDispositionGrant` 按"唯一 active grant"假设写。grant 累积是正常路径产物：
  `upsertExtensionDatabaseGrant` 每次按 artifact upsert（`extension_database_registry_repository.go:151-179`），
  而"撤销旧 active grant"的 `revokeActiveExtensionDatabaseGrants`（同文件 `:131-149`）**从未被调用**（死代码）。
  因此 install → upgrade → uninstall 必然带 2 条 active grant，purge 有极大概率永久 conflict。
- **B2（次因）**：finalize 依赖 `requested_by_user_id`，而该列 `ON DELETE SET NULL`；
  用户删除后没有任何恢复路径——手工端点走的是同一个 `finalizeLifecycleUninstall`，同样失败。
- **A**：被重新安装取代的 tombstone 没有"superseded"终态，只能永久 pending。

## 5. 最小修复（本轮不实施）

### B1（P0，一行 SQL 的改动面）

把 exact-artifact 语义写进查询，而不是"取第一条 active"：

```sql
SELECT extension_version_id, extension_version, package_digest
FROM extension_database_grants
WHERE extension_id = $1 AND status = 'active'
  AND extension_version_id = $2 AND extension_version = $3 AND package_digest = $4
FOR UPDATE
```

- 命中 → 通过；无行 → 通过（该 artifact 没有 active grant，已撤销或从未授予）；同 version 不同 digest → 仍 conflict。
- 为什么安全：真正的"不许在别的版本还活着时 purge"由 `lockExactExtension`
  （`bootstrap/extension_lifecycle_cleanup_postgres.go:125-127`，比对 `extensions.active_version_id`）和
  disposition 的 target==operation 校验（`extension_database_disposition_repository.go:115-120`）负责，
  不依赖 grant 表的行数假设。
- 影响面：仅 `app/Support/Extensions/extension_database_disposition_postgres.go`；单 grant 路径行为不变。
- 测试：在 `extension_database_disposition_integration_test.go` 增加 install 0.1.0 → upgrade 0.2.0 →
  uninstall 场景（两条 active grant），断言 finalize 成功且两条 grant 都变 `revoked`。
- **不建议**本轮把 `revokeActiveExtensionDatabaseGrants` 接进 provisioning：升级期间旧 runtime 可能仍持
  lease/credential，会改变 grant 语义，属于单独决策。

### B2（P1，恢复路径）

actor 解析改为 `requested_by_user_id` → `recovery_actor_user_id` → `authority_snapshot.actorUserId` 回退；
三者皆无时：无 provider selection 按 no-op 放行，有 selection 则需要宿主系统 actor（产品决策，不能塞已删除的 id）。
验收要覆盖"用户被删除后 sweep 仍能收敛"。

### A（P2，收尾）

给被重装取代的 tombstone 一个 `superseded` 终态（或在 finalizer 里识别 `active_version_id` 已变并安全终结），
否则列表徽标永久误报。**不能**用"直接删 tombstone"了事——迁移 CHECK 与审计都要求终态有证据。

### 验收标准

> **取代说明（2026-10-26 终态；下文原文保留，作为当时的判定记录）**：下列第 1 条
> （908/909 finalize 成功）与第 4 条（手工端点返回成功）已被
> `plans/2026-10-26-uninstall-identity-purge.md` 与
> `decisions/2026-10-26-disposition-grant-exact-artifact.md` 的「验收边界（终版）」
> 取代 —— identity purge 与 append-only 发布账本的 `ON DELETE RESTRICT` 冲突，
> 使「物理删除 extensions identity 行」对任何曾 enable/upgrade 过的插件在架构上不可达。
> 本轮（grant 层）的负向断言是：**越过授予层、停在 identity purge 的 FK RESTRICT**，
> 而不是变成 `finalized`。

1. 908/909 finalize 成功：tombstone `status=finalized`、`physical_*=false`、回执/proof 齐全；`extensions` 行删除；
   `storage/extensions/sforum.marketplace/0.1.0/d6c41b93…` 删除；该扩展 grant 全部 `revoked`。
2. 新增双 active-grant 用例通过；既有单 grant 用例不回归。
3. 703 不删当前 1.1.0 包；按 A 方案收尾后不再出现在 pending 集合。
4. `POST /admin/extensions/sforum.marketplace/uninstall-cleanup/finalize` 返回成功（当前返回 503）。

## 6. 复现命令（只读）

```bash
PGPASSWORD=sforum docker exec -e PGPASSWORD=sforum -i sforum-postgres \
  psql -h 127.0.0.1 -U sforum -d sforum -X -A -F '|' -c "
select c.operation_id, c.step_id, c.status, c.physical_identity_present,
       o.terminal_result, o.removal_mode, o.completed_at
from extension_lifecycle_cleanup_records c
join extension_lifecycle_operations o on o.id = c.operation_id
where c.record_kind = 'uninstall_tombstone' and c.status = 'pending';"

PGPASSWORD=sforum docker exec -e PGPASSWORD=sforum -i sforum-postgres \
  psql -h 127.0.0.1 -U sforum -d sforum -X -A -F '|' -c "
select extension_id, extension_version_id, extension_version, left(package_digest,12), status
from extension_database_grants where status = 'active' order by extension_id, id;"

# 证明 QueryRow 取首行（事务内只读，ROLLBACK）
PGPASSWORD=sforum docker exec -e PGPASSWORD=sforum -i sforum-postgres \
  psql -h 127.0.0.1 -U sforum -d sforum -X -A -F '|' -c "
BEGIN; SELECT extension_version_id FROM extension_database_grants
WHERE extension_id='sforum.marketplace' AND status='active' FOR UPDATE; ROLLBACK;"
```

## 7. 本轮边界

- 未修改任何生产代码；未重启 API（重启权在老后）；未碰 web dev server（PID 976992 :3000）。
- SQL 全部只读；唯一带锁的语句在 `BEGIN … ROLLBACK` 内，未产生写入。
- 产出只写本文件。
