# 2026-10-26 卸载 identity purge 与 append-only 发布账本：矛盾、终态判据与方案

**Status**: `draft`（分析完成；实施未批准，先拍板 §5 的终态判据）
**Owner**: researcher（小研）· **Task**: task-8
**纪律**: 只读。未改生产代码、未改 schema、未重启 API、未动 `tun2.html`、未移动 tag。
SQL 全部只读（唯一带锁的语句在 `BEGIN … ROLLBACK` 内）。

**一句话结论**：`identity purge` 不是产品承诺，是 uninstall 终态的一个实现细节；它撞上的不是一条可以
商量的 FK，而是 2026-10-17 已接受的 append-only 契约。**该让路的是 uninstall 的终态定义，不是账本。**
推荐 **A**：identity 行保留为账本锚点（清 active/staged 指针 + 打标 + 只清制品/DB/信任/运行时），
tombstone 增加 `identity_disposition` 终态语义；**B（归档发布历史）不推荐**，因为它要拆的不是一个 FK，
而是四张表的不可变证据链。

---

## 0. 证据边界

- 数据库：`sforum`（容器 `sforum-postgres`），只读 SQL；现场数据与本轮工作树一致。
- 代码：`apps/api` 当前工作树（含 task-7 未提交的 grant 修复，与本分析不冲突）。
- 输入：`sessions/2026-10-26-uninstall-tombstone-diagnosis.md`、`decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`、`decisions/2026-10-26-disposition-grant-exact-artifact.md`。

## 1. 矛盾的两端

### 1.1 uninstall 想做什么

1. 状态机 7 步，收尾在 `uninstalling`（`app/Models/Extensions/lifecycle_state_machine.go:180-188`）。
2. 第 6 步 `lifecycle.uninstall.06.host.uninstalling` **只登记 tombstone**，不动物理世界
   （`app/Support/Extensions/lifecycle_cleanup_postgres.go:71-94` 的 INSERT；
   迁移头注释 `202607140008_...sql:2-5` 明写 "stages evidence only; it never deletes an extension, version, or package"）。
3. 真正的物理清理在**操作终态之后**：5 分钟 sweep 或管理端手工入口
   （`lifecycle_repository_operations.go:454-487` 的谓词；`bootstrap/worker.go:819` 注册）。
4. purger 顺序：校验 → identity 锁 → 包校验 → runtime 已退 → DB disposition → 删包 → **删 identity**
   （`bootstrap/extension_lifecycle_cleanup.go:45-103`）。
5. 删 identity 的那一行是全仓**唯一**的生产 `DELETE FROM extensions`：
   `bootstrap/extension_lifecycle_cleanup_postgres.go:182-191`。

### 1.2 账本承诺了什么

- `extension_versions.extension_id → extensions(id) ON DELETE CASCADE`：删 identity 会级联删版本行。
- `plugin_runtime_publication_members` 以复合 FK **ON DELETE RESTRICT** 引用版本行
  （`plugin_runtime_publication_me_extension_version_id_extensi_fkey`）。
- 两张表带 `BEFORE UPDATE OR DELETE` 触发器（`reject_plugin_runtime_desired_mutation`）与 `no_truncate`，
  发布历史物理上不可改不可删；Down 迁移在存在历史时拒绝执行。
- `plugin_runtime_applied_members`（19288 行）**同样**以 RESTRICT 引用 members，并且自己也是
  immutable + no_truncate；`plugin_runtime_publication_acks` 有 `no_delete`。
- 2026-10-17 决策据此判定：发布历史永不删除、被引用版本行**永久钉住**、FK RESTRICT 是最后一道 fail-closed 背线。

### 1.3 谁该让（判定）

**修正 uninstall 的终态定义，不推翻 2026-10-17。** 三条理由：

1. **产品承诺 vs 实现细节。** 用户理解的「卸载」是：插件不再运行、包没了、它的 DB 资源被处置、信任被撤销。
   `extensions` 行本身不是用户可见数据，它是账本的根节点（versions → members 都挂在它下面）。
2. **删 identity 不是清理，是销毁证据。** 账本链是 `extensions ← extension_versions ← members ← applied_members`；
   删根节点等于把整条链的锚点拔掉，而这条链是多节点收敛、回滚、审计的共同依据。
3. **删除目标在当前架构下不可达，不是"偶尔失败"。** 任何曾进入发布账本的插件都必然被钉住；
   能删成功的只有「从未发布过」的扩展——那恰好是 fixture 造出来的形状（§2.2）。

所以这不是「要不要放宽一条约束」，而是「uninstall 的完成判据写错了对象」：它把"账本根节点消失"
当成了完成条件，而真正的完成条件是"物理世界与权限世界已清、账本保留"。

## 2. 硬证据（逐条复核）

### 2.1 FK 与级联链

```sql
-- 删 extensions → 级联删 extension_versions → 被 members 的 RESTRICT 挡住
extension_versions_extension_id_fkey
  FOREIGN KEY (extension_id) REFERENCES extensions(id) ON DELETE CASCADE
plugin_runtime_publication_me_extension_version_id_extensi_fkey
  FOREIGN KEY (extension_version_id, extension_id, extension_version, package_digest)
  REFERENCES extension_versions(id, extension_id, version, package_digest) ON DELETE RESTRICT
```

现场报错 `SQLSTATE 23503 ... violates foreign key constraint "plugin_runtime_publication_me_..."` 就是这条链。

### 2.2 「30/34 能 finalized」的真相：这是**负证据**，不是可达性证据

先纠正一处编号歧义：`30/34` 不是「34 条里成功 30 条」，而是 **cleanup record id = 30 与 34** 两条。
本库 tombstone 总共只有 6 条：2 finalized（id 30/34）+ 4 pending（id 171/308/309/532）。

| record id | operation_id | retained_extension_id | status | identity_snapshot.status |
| --- | --- | --- | --- | --- |
| 30 | 181 | `p4.e2e.preserve.1792178784842629779` | finalized | `enabled` |
| 34 | 243 | `p4.e2e.preserve.1792179263590983300` | finalized | `enabled` |

关键事实（全部只读复核）：

- 这两个扩展 id 在 `extensions` 里**已不存在**（0 行）。
- 它们在 `plugin_runtime_publication_members` 里**历史上 0 行**。members 表是 append-only
  （`BEFORE DELETE/UPDATE` 直接 RAISE），所以「0 行」等价于「**从未进入过任何发布修订**」，不是"被清理过"。
- 它们的 identity_snapshot 都写着 `enabled`，但 members 是 0——而正常 `enable` 必然经过 lifecycle 发布事务写 member。
  仓库里能造出这个组合的只有**直改状态**的路径：
  `bootstrap/extension_lifecycle_e2e_integration_test.go:278-299`（`UPDATE extensions SET status='enabled'`
  后直接 `manager.Start`，即 `withAuthIdentity=false` 分支），或 V1 遗留的 `PostgresStore.Enable`
  （`postgres_store.go:486-490`，同样不发布）。两条都不是「正常卸载」路径。

**结论（这就是任务要的解释）**：那两条之所以能 finalized，恰恰是因为它们的版本行**从未被发布账本引用**，
所以级联删除没有任何 RESTRICT 阻挡。它们是「不可达目标只在账本外成立」的证明，反过来说明：
**只要进过账本，identity purge 就不可达**。fixture 的成功不能当作方案 C 可行的证据。

### 2.3 四条卡住的现场

| record id | op | 扩展 | versions | members | 其他 RESTRICT 子表 |
| --- | --- | --- | --- | --- | --- |
| 171 | 703 | `sforum.web-search` | 57 | 398 | 全 0 |
| 308 | 908 | `sforum.marketplace` | 7 | 422 | 全 0 |
| 309 | 909 | `sforum.marketplace` | 7 | 422 | 全 0 |
| 532 | 1154 | `qa.store.proof` | 2 | 2 | 全 0 |

（「其他 RESTRICT 子表」= `attachment_storage_instances`、`extension_builtin_removals`、
`extension_missing_artifact_removals`、`extension_version_gc_removals`、`notification_channel_selections`；
四条现场全为 0，所以对这四条而言 **publication members 是唯一的结构性硬阻塞**。）

全库规模：`plugin_runtime_publications` 768 / `plugin_runtime_publication_members` 7464 /
`plugin_runtime_applied_members` 19288 / `extension_versions` 1235。

### 2.4 除了 FK 还有哪些门（方案 B 必须一起回答）

删 identity 还要过两道 **identity registry 删除守卫**（`202607160028_identity_registry_ownership.sql`）：

- `reject_extension_identity_registry_active_extension_delete`：owner 的声明仍是 `active` → 拒绝
  （"declarations must be tombstoned before uninstall"）。
- `reject_extension_identity_registry_publication_active_extension`：根发布仍是 `active` → 拒绝。

这两道是**可满足的**（卸载流程会把它们 tombstone；marketplace 最新一条就是 `tombstone`），
不属于 append-only 冲突。列出来是为了说明：identity purge 的"前置条件"本来就有五六个，
FK 只是最后一根压死骆驼的线。

## 3. 终态判据（先定义，再选方案）

### 3.1 「卸载完成」的五个条件

1. 运行时实例已退、进程不在（`requireRuntimePurged`）。
2. 制品目录已删（package purge）。
3. DB 资源已按 disposition 处置并撤销该扩展全部 active grant。
4. 信任已撤销、页面/邮件 provider 绑定已清、provider selections 已失效。
5. **identity 行与它的版本行保留**，作为账本锚点与审计证据。

前四条是现有 purger 已经在做的事（`bootstrap/extension_lifecycle_cleanup.go:45-103`）；
第五条是本次要改的判据。

### 3.2 tombstone 需要的终态

- `finalized`：前四条完成。**identity 保留**，所以 `physical_identity_present` 必须允许为 TRUE。
- `superseded`（新增）：该 tombstone 指向的 artifact 已被后续 install/upgrade 取代（703 形态），
  不再适用物理清理。**不执行 purge**，写审计，退出 pending 集合。
- `pending`：保持现状。

### 3.3 `physical_identity_present` 的语义必须显式化

现有 CHECK 把 `finalized` 与 `NOT physical_identity_present` 硬绑（`202607140008_...sql:114-135`），
receipt 也硬要求 `IdentityPurged: true`（`lifecycle_cleanup_finalizer_postgres.go:338`；
`bootstrap/extension_lifecycle_cleanup.go:242,260`）。所以**任何保留 identity 的方案都必须改这两处**，
没有"只改代码不改 schema"的路径。

## 4. 方案

### A. 保留 identity + 打标（推荐）

**终态判据**：§3.1 五条；tombstone `finalized` + `identity_disposition='retained'`。

**改动面**

1. `extensions.status` 增 `'uninstalled'`（迁移：`202607050005_extensions.sql:6` 的 CHECK；
   连带 `extension_lifecycle_state_publications` 的 `source_status/target_status` CHECK
   `202607140009_...sql:15,24` 是否需要同步——按现状 retire 发生在操作终态之后，理论上不会进快照，
   但 CHECK 扩值成本极低，建议一起扩，避免以后踩）。
2. purger 把 `DELETE FROM extensions` 换成 retire：
   `UPDATE extensions SET status='uninstalled', active_version_id=NULL, staged_version_id=NULL WHERE id=$1 AND active_version_id=$2`
   （`bootstrap/extension_lifecycle_cleanup_postgres.go:182-191`）；信任撤销/绑定清理/selection 失效**原样保留**。
   已 retire（active 已为 NULL）时跳过该 UPDATE，按幂等成功处理——908/909 是同一 artifact 的两次卸载。
3. receipt 与 proof 契约：`identityPurged:bool` → `identityDisposition:'purged'|'retained'`，
   schema 从 `sforum.lifecycle.host-purge-proof@1` 升 `@2`
   （`bootstrap/extension_lifecycle_cleanup.go:215-260`；`lifecycle_cleanup_finalizer_postgres.go:330-389`）。
4. tombstone 迁移：`status` 增 `'superseded'`，新增 `identity_disposition` 列，
   重写 finalized 的 physical_* CHECK（§3.2/§3.3）；已存在的 2 条 finalized 回填 `'purged'`。
5. finalizer：`lockExactExtension`（`bootstrap/extension_lifecycle_cleanup_postgres.go:109-127`）区分三态——
   active==retained → 继续；active IS NULL 且 status='uninstalled' → 幂等重放；
   active 指向别的版本 → 判 `superseded`，不 purge、写审计。
6. 重装复活：`SaveInstalled` 的 `created || activeVersionID == 0` 分支
   （`postgres_store.go:205-214`）除设 active 外还要 `status='installed'`，否则重装后行仍是 `uninstalled`。
7. 列表可见性：`extensionSelectSQL` 用 `JOIN extension_versions ON id = active_version_id`
   （`postgres_store.go:877-878`），active 为 NULL 时行自动从 List/Get 消失——**不要依赖这个副作用**，
   显式在查询里排除 `status='uninstalled'`，把它当契约而不是巧合。

**迁移需求**：2–3 个（`extensions.status` CHECK、tombstone 终态/`identity_disposition`；若同步
`extension_lifecycle_state_publications` 的 status CHECK 则第 3 个）+ proof schema 版本化（不是迁移）。
**对 4 条卡住的处理**：
- 908/909：grant 修复（task-7）已让 disposition 通过；按 A 后第一次 finalize 完成 retire，第二次幂等。
- 1154：actor 回退链（§6.2）修好后同 908；provider selection 为 0，实际 no-op。
- 703：判 `superseded`，不 purge、不删 1.1.0 包。

**风险**：
- `extensions.status` 的消费者面大（`Status*` 常量引用全仓（含测试）466 处）；需要一次语义审计，
  重点是 desired-set 查询（`plugin_runtime_publication_authoritative_postgres.go:302-308` 只取 `status='enabled'`，
  `uninstalled` 天然被排除——这条是安全的）与所有 `status != 'disabled'` 形态的判断。
- 重装路径如果漏改，会留下"已安装但显示 uninstalled"的坏行。
- `is_deletable` 仍为 true，管理端若还有"删除"动作需要一起改成"已卸载"。

**验收**：908/909/1154 finalize 成功且 `identity_disposition='retained'`；`extensions` 行仍在、`status='uninstalled'`、
`active_version_id IS NULL`；`plugin_runtime_publication_members` 行数不变（一个字节都不动）；
重装同名插件能回到 `installed` 并正常 enable；703 进入 `superseded` 且 1.1.0 包完好；
既有 2 条 finalized 行 `identity_disposition='purged'` 回填后校验通过。

### B. 归档/快照发布历史，让版本行可回收（不推荐）

**终态判据**：与 A 相同，但 identity 真的被删。

**为什么不做**：

1. 要拆的不是一个 FK，而是**四张表的不可变证据链**：`plugin_runtime_publications`、
   `plugin_runtime_publication_members`、`plugin_runtime_publication_acks`（`no_delete`）、
   `plugin_runtime_applied_members`（immutable + RESTRICT 引用 members）。少拆一张，删除仍被挡。
2. 归档必须证明「没有任何节点会再需要这段历史」；而节点可以离线任意久，
   `plugin_runtime_nodes` 的 applied revision 不能作为"以后不会再落后"的证明。这是收敛语义的根。
3. 即使全部归档，**最新一条修订**仍可能引用该版本（703 就是被重装后的新修订取代），
   可删集合比想象的小得多。
4. 收益只是"行消失"这一观感；代价是回滚、修复、审计三套能力的证据基础。

**改动面 / 迁移 / 验收（即便不推荐，也要把账算完）**：

- 改动面：四张表的不可变触发器与 Down 守卫（publications / members / acks / applied_members）、
  新增归档表与归档事务、节点追赶语义、version GC 的 `publication_history` 判据重写；
  `extension_lifecycle_cleanup_postgres.go:182-191` 的 DELETE 反而**不用改**（删得掉才怪）。
- 迁移：至少 4 个（触发器改造、归档表、GC 判据、历史回填），且需要一次跨版本升级窗口；
  旧版本进程在新 schema 上必须 fail-closed。
- 对 4 条卡住的处理：908/909/1154 在归档覆盖其 members 后**可能**可删；
  **703 仍然不可删**——它的 1.1.0 是当前 active，必然出现在最新一条修订里，
  删 identity 会级联删 1.1.0 并被最新修订挡住。也就是说 B 连 703 都收不了尾。
- 风险：多节点收敛、回滚、审计三套能力同时被削弱；跨版本升级期不可逆。
- 验收：必须证明「节点落后任意多个修订仍能收敛」与「回滚到已归档版本」的语义，
  以及归档后审计链仍可复核；证明不了就不能验收。

**若未来真要做**：前提是独立决策（2026-10-17 决策第 6 条已写明），并且要同时定义归档格式、
节点追赶语义、审计保留期。不属于本任务范围。

### C. 只改终态判据，不给 identity 打标（最小）

**终态判据**：同 A，但 identity 行保持原样（`status` 不变、`active_version_id` 保留），
tombstone 记 `identity_disposition='retained'`。

**改动面**：tombstone 迁移 + receipt/proof + purger 跳过 DELETE（约 A 的 1/3），不新增 status。

**代价（为什么它是"更小的 A"而不是"更好的 A"）**：
- identity 行仍带 `active_version_id` → 在 `List` 里可见，管理端会长期显示一个"已卸载但还在"的禁用插件；
  要消掉这个幽灵行，要么加过滤（那就等于 A 的可见性改动），要么清 active 指针（那就等于 A 的 retire 语义）。
- 重装会走 `activeVersionID != versionID` 分支变成 **staged**（`postgres_store.go:215-224`），
  用户装完发现还要再点一次升级——这是可感知的回归。
- 所以 C 省下的只有"一个 status 值 + 一次语义审计"，却把两个真实缺陷留给产品。

**验收（按 C 实施时）**：908/909/1154 finalize 成功且 `identity_disposition='retained'`；
identity 行仍在且 `active_version_id` 非空（幽灵行可见，需产品确认可接受）；
重装同名插件必须再手动点一次升级才能启用；703 仍需 `superseded` 才能离开 pending。

| | 改动面 | 迁移 | 重装语义 | 幽灵行 | 是否动 append-only |
| --- | --- | --- | --- | --- | --- |
| A 保留+打标 | 中 | 2–3 | 正确（复活） | 无 | 否 |
| B 归档历史 | 很大（4 表） | 多 | 不变 | 无 | **是（推翻 2026-10-17）** |
| C 只改判据 | 小 | 1–2 | **退化为 staged** | **有** | 否 |

## 5. 推荐与最小落地步骤

**推荐 A。** 它不是"最省事"，但它是唯一同时满足三件事的方案：账本不动、重装语义正确、管理端不留幽灵。

1. **S1 定判据**：把 §3.1 五条写进 `decisions/`（一条新 decision：uninstall 终态 = 物理与权限世界已清 +
   identity 作为账本锚点保留），并明确它**不推翻** 2026-10-17。
2. **S2 迁移**：`extensions.status` 扩 `'uninstalled'`；tombstone 增 `'superseded'` + `identity_disposition`
   + 重写 finalized 的 physical_* CHECK；回填 2 条历史 finalized。
3. **S3 purger**：DELETE → UPDATE retire；保留信任撤销/绑定清理/selection 失效；
   `lockExactExtension` 三态化（继续 / 幂等 / superseded）。
4. **S4 receipt**：proof `@2` + `identityDisposition`；finalizer 写 `identity_disposition`。
5. **S5 重装**：`SaveInstalled` 复活分支设 `status='installed'`；补一条"卸载→重装→启用"集成测试。
6. **S6 收尾四条**：grant 修复（已落地）→ 908/909 → 1154（先修 actor 链）→ 703 判 superseded。
7. **S7 验收**：§4.A 验收清单 + `go test ./app/Support/Extensions/ ./app/Models/Extensions/` 绿。

## 6. 顺带回答

### 6.1 703 在推荐方案下怎么收尾

703 的 tombstone 指向 `retained_version_id=14155`（1.0.0），而 `extensions.active_version_id=21983`（1.1.0）。
`lockExactExtension`（`bootstrap/extension_lifecycle_cleanup_postgres.go:109-127`）因此 fail-closed —— **这个判断是对的**：
它保证不会去删一个已经被新版本取代的包。缺的不是"放宽它"，而是**给它一个出口**：

- 判 `superseded`（active 指向另一个版本 + 操作已 terminal succeeded + 存在更晚的 install/upgrade 证据），
  写一条审计，tombstone 离开 pending。
- **不执行任何物理删除**：1.0.0 的包留在磁盘上（它被发布账本钉住，版本 GC 本来也不会释放，
  见 2026-10-17 决策第 3 条"曾被启用的插件版本行被永久钉住"）。这是既有已接受成本，不是本方案新增。
- 副作用修复：`pendingUninstallCleanups` 按扩展投影徽标（`controller.go:330-333`），
  703 进 superseded 后，已重装的 web-search 不再被误标「清理挂起」。

### 6.2 actor 回退链放行后，是否仍撞同一个 grant 坑

**不撞 grant 了**（task-7 已把 exact-artifact 条件写进查询），但**仍会撞 identity purge**：

- 1154 的 actor 守卫在 `lifecycle.go:540-543`，`lifecycleOperationActorUserID`
  （`lifecycle_coordinator_drive.go:726-731`）只读 `recovery_actor_user_id → requested_by_user_id`；
  后者被 `ON DELETE SET NULL` 置空后返回 0。
- 该扩展 provider selection 计数为 0，所以守卫一旦按"无 selection 则 no-op"放行，这一步实际不做事。
- 接着 disposition 走 task-7 的修复 → 通过；包删除 → 通过；**identity 删除 → 仍被 members RESTRICT 挡住**。
- 所以 actor 链修好不等于 1154 收敛；它只是把失败点从「provider selection 守卫」推到「identity 删除」。
  在方案 A 下，最后一步不再删行，1154 才能收敛。

## 7. 与 op 926 那篇的关系（互相指引）

- `plans/2026-10-26-op926-job-publication.md` 推荐的是**给 job publication 增加启动期重放**（读取侧补线），
  它**不改发布账本**、不碰 `plugin_runtime_publication_members`、不碰 tombstone。
- 本篇推荐的是**给 uninstall 定义终态**（写入侧改判据），同样不碰发布账本。
- 两篇的共同模式值得统一命名：都是"账本行已终态、物理副作用永远不可能完成"的收尾问题。
  op926 的 `abandoned`（副作用因回滚而不再适用）与本篇的 `superseded`（artifact 被更晚操作取代）
  是两种不同原因，**不要合并成一个词**；`finalized` 保留给"副作用已完成"。
- 唯一交叉点：op926 的 S6 会给 4 条 `target+pending` 的 job publication 打 `abandoned`，
  那是 `extension_lifecycle_job_publications`，与本篇的 `extension_lifecycle_cleanup_records` 不同表、不同键，
  互不影响。两篇没有冲突推荐。

## 8. 未决问题

1. `extensions.status='uninstalled'` 还是 `retired_at` 时间戳列？前者语义显式但要审计全仓（含测试）466 处常量；
   后者改动小但需要额外过滤条件。建议前者（显式 > 隐式）。
2. 重装是否允许复用同一 `extension_id`？现状 `SaveInstalled` 的 `ON CONFLICT DO NOTHING` 已经允许；
   A 方案要保证"复活"后 `status` 正确，且旧的版本行仍在（被钉住）不影响新版本成为 active。
3. `superseded` 是否要释放旧包目录？本方案选"不释放"（与版本 GC 的既有口径一致），
   若产品要求释放，需要单独决策"被钉住版本是否允许只删盘不删行"。
4. 主题（theme）卸载是否同一套？主题不产生 plugin runtime member（`enforce_plugin_runtime_member_type`
   拒绝非 plugin），所以主题的 identity purge 本来就可达；本方案不应把主题也改成保留，除非另有理由。
5. proof schema `@2` 的兼容窗口：已 finalized 的行不会重新校验，但外部审计工具若解析 `@1` 需要同步。

## 附录 A：复现 SQL（只读）

```sql
-- 全部 tombstone（6 条：2 finalized + 4 pending）
SELECT id, operation_id, retained_extension_id, status, identity_snapshot->>'status' AS identity_status
FROM extension_lifecycle_cleanup_records WHERE record_kind='uninstall_tombstone' ORDER BY id;

-- 每条 tombstone 对应扩展的 versions / members
SELECT c.id, c.retained_extension_id,
       (SELECT count(*) FROM extension_versions v WHERE v.extension_id=c.retained_extension_id) AS versions,
       (SELECT count(*) FROM plugin_runtime_publication_members m WHERE m.extension_id=c.retained_extension_id) AS members
FROM extension_lifecycle_cleanup_records c WHERE c.record_kind='uninstall_tombstone' ORDER BY c.id;

-- 2 条 finalized 的扩展是否还在 / 是否进过账本（append-only ⇒ 0 行 = 从未发布）
SELECT count(*) FROM extensions WHERE id IN
  ('p4.e2e.preserve.1792178784842629779','p4.e2e.preserve.1792179263590983300');
SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_id IN
  ('p4.e2e.preserve.1792178784842629779','p4.e2e.preserve.1792179263590983300');

-- 其他 RESTRICT 子表（四条现场应全 0）
SELECT e.id,
 (SELECT count(*) FROM attachment_storage_instances r WHERE r.extension_id=e.id) AS attachment_instances,
 (SELECT count(*) FROM extension_version_gc_removals r WHERE r.extension_id=e.id) AS gc_removals,
 (SELECT count(*) FROM extension_builtin_removals r WHERE r.extension_id=e.id) AS builtin_removals,
 (SELECT count(*) FROM extension_missing_artifact_removals r WHERE r.extension_id=e.id) AS missing_removals,
 (SELECT count(*) FROM notification_channel_selections r WHERE r.extension_id=e.id) AS notif_selections
FROM extensions e WHERE e.id IN ('sforum.marketplace','qa.store.proof','sforum.web-search');

-- 703 的取代证据
SELECT id, active_version_id FROM extensions WHERE id='sforum.web-search';
```

## 附录 B：证据索引

| 事实 | 位置 |
| --- | --- |
| 唯一的生产 identity 删除 | `apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go:182-191` |
| purger 顺序 | `apps/api/bootstrap/extension_lifecycle_cleanup.go:45-103` |
| receipt 硬编码 IdentityPurged=true | 同上 `:215-260`；`lifecycle_cleanup_finalizer_postgres.go:330-340` |
| finalize UPDATE + physical_* 置 false | `apps/api/app/Support/Extensions/lifecycle_cleanup_finalizer_postgres.go:153-170` |
| tombstone CHECK（finalized ⇒ identity 不在） | `apps/api/database/migrations/202607140008_extension_lifecycle_cleanup_records.sql:114-135` |
| tombstone status CHECK | 同上 `:25-26` |
| extensions.status CHECK | `apps/api/database/migrations/202607050005_extensions.sql:6` |
| identity 锁 + fail-closed | `apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go:109-127` |
| uninstall 状态机 7 步 | `apps/api/app/Models/Extensions/lifecycle_state_machine.go:180-188` |
| tombstone 登记（staging） | `apps/api/app/Support/Extensions/lifecycle_cleanup_postgres.go:71-94` |
| sweep 谓词 | `apps/api/app/Models/Extensions/lifecycle_repository_operations.go:454-487` |
| actor 守卫 / 解析 | `apps/api/app/Models/Extensions/lifecycle.go:540-543`；`lifecycle_coordinator_drive.go:726-731` |
| 直改 enabled 的两条非发布路径 | `bootstrap/extension_lifecycle_e2e_integration_test.go:278-299`；`postgres_store.go:486-490` |
| List 的 INNER JOIN（active 为 NULL 即不可见） | `apps/api/app/Models/Extensions/postgres_store.go:877-878` |
| 重装分支（created / active==0 / staged） | 同上 `:205-224` |
| desired set 只取 enabled | `apps/api/app/Models/Extensions/plugin_runtime_publication_authoritative_postgres.go:302-308` |
| append-only 触发器（members） | 迁移 `202607160027_plugin_runtime_publications.sql`；库内 `plugin_runtime_publication_member_immutable` |
| applied members 的 RESTRICT + immutable | `plugin_runtime_applied_member_publication_revision_extensi_fkey`；`plugin_runtime_applied_member_immutable` |
| identity registry 删除守卫 | `apps/api/database/migrations/202607160028_identity_registry_ownership.sql` |
| 2026-10-17 决策 | `knowledge/decisions/2026-10-17-version-gc-publication-restrict-cooperation.md` |
| task-7 grant 修复 | `knowledge/decisions/2026-10-26-disposition-grant-exact-artifact.md` |
