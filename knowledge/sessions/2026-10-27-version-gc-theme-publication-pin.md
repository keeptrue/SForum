# 2026-10-27 Session Handoff — Version GC 钉住主题发布 source（revision 20 事故修复）

Task: 共享任务板 `task-1`（owner: backend）。范围仅
`apps/api/app/Models/Extensions/` 与 `apps/api/database/migrations/`。

## 事故与根因（已用代码 + dev 库证据确认，非假设）

- 现象：dev 库 `theme_runtime_publication_acks` revision 20 在
  2026-10-27 05:48:52 / 06:02:14 两次 `failed`，错误
  `theme runtime publication apply failed / extension version not found`；
  同一 revision 在 03:53:06 仍能 `applied`。
- 根因链（全部有证据）：
  1. `ThemeRuntimeWatcher.reconcileOnce` 每次**进程启动**都用**最新**修订调
     `ApplyThemeRuntimePublication`；`themePublicationSource` 先比对当前 active
     制品，不匹配就 `GetExtensionVersion(source 三元组)`，行不在即
     `ErrExtensionVersionNotFound`（`theme_publication_apply.go:151-187`）。
  2. `versionGCBlockedReason` / `versionGCCandidatesSQL` 只检查
     `plugin_runtime_publication_members`，全文没有 theme 判据
     （修复前 `grep -c -i theme` = 0）。
  3. `theme_runtime_publications.source_*` 是裸 TEXT，无 FK（迁移
     `202607150020`），DB 层不挡删除。
- 时间线证据：`extension_version_gc_removals` id 230 =
  `sforum.default-theme` / `1.0.0` / `555ed50d…` / 2026-10-27 04:26:20 /
  `mode=api` / `requested_by_user_id=1`；`storage/extensions/sforum.default-theme/
  1.0.0/555ed50d…` 目录已不存在；revision 20 的 `source_package_digest` 正是
  `555ed50d…`，target `2f06a9e4…` 仍在（id 19542，active）。
- 附带结论：04:26 那一轮同时回收了 `5e9226a1` / `933fd0fd` / `309cdcdf` /
  `dcbcb68b`，即 revision 15-19 的 source 三元组全部失效。这些历史修订不会再被
  apply（watcher 只 apply 最新），但账本里的引用从此不可解析——这正是本次要
  在判据里补上的不变量。

## Changed

- `apps/api/app/Models/Extensions/postgres_store_version_gc.go`
  - `versionGCCandidatesSQL`：`published` 由"插件账本 EXISTS"改为
    "插件账本 EXISTS **OR** 主题账本 EXISTS"；主题侧按
    `source_theme_id + source_theme_version + source_package_digest` 三元组匹配
    候选版本行（与 `themePublicationSource` 的回查键完全一致）。
  - `deleteReclaimableVersionRow`：锁内复核补一条
    `theme_runtime_publications` 计数（插件侧有 FK RESTRICT 背线，主题侧没有，
    这条就是唯一防线）。
  - 判据函数签名与 `VersionGCBlocked*` 词表**不变**。
- `apps/api/app/Models/Extensions/postgres_store_version_gc_integration_test.go`
  - 新增 `TestVersionGCBlocksThemePublicationSource`（denied 侧：真实
    `ActivateThemeExact` 产生 source 引用 → 候选 blocked 且 dry-run/execute 都
    不碰它，行与制品仍在）。
  - 新增 `TestVersionGCRemovesUnreferencedThemeVersion`（allowed 侧：从未进入
    账本、超出 retain 的主题历史版本仍可回收）。
  - 新增 `TestVersionGCPlanDigestFollowsBlockedDrift`：签发 token 后把候选钉进
    发布账本 → 旧 token `ErrVersionGCConfirmationInvalid`，证明计划摘要随
    blocked 集合变化。
  - 顺带修两处**既有**用例在已 seed 的 dev 库上不可重复执行的问题：过期 token
    改用每次运行唯一的 token（`extension_gc_confirmations.token_hash` 是
    UNIQUE）；actor 用户改 `ON CONFLICT (id) DO NOTHING`（dev 库 id 42 已被
    seed 用户占用）。断言未改。
- `apps/api/app/Models/Extensions/theme_publication_postgres_integration_test.go`
  - 隔离 schema 迁移列表补 `202610170002`（Version GC 台账），供上面两个主题
    用例使用。**不能**在共享库的发布账本里造测试行：账本 append-only，插一行
    就会成为全局最新修订，真实 watcher 会永久 apply 失败。

## Decisions

1. **复用 `publication_history`，不新增 `theme_publication_history`**：该值已是
   对外契约（`contracts/openapi/schemas/extensions.yaml` 的 enum +
   `apps/web/app/utils/admin/adminVersionGc.ts` 的 union），新增值要同步改契约与
   前端；而"被 append-only 发布账本钉住、永久不可回收"的运维含义完全相同，
   具体是哪个账本属于实现细节。
2. **主题发布按全历史钉住**（不是只钉最新修订）：账本是 append-only 证据，任何
   修订引用的精确三元组都应保持可解析，与插件账本一致。代价：主题历史版本一旦
   成为某次激活的 source 就不再回收——刻意的 fail-closed 取舍。若未来更看重
   回收率，改成"只钉最新修订"是局部 SQL 改动，但需要一条独立决策。
3. **不加 migration（FK/触发器都不可行）**：
   - 三元组 FK 技术上可加（`extension_versions` 有
     `UNIQUE (extension_id, version, package_digest)`），但 `ON DELETE RESTRICT`
     会让"曾经是 source 的主题"永远无法卸载（`DELETE FROM extensions` 级联删
     versions 会被挡住），而 `themePublicationSource` 明确允许 source 扩展已被
     卸载（fresh node 场景）。
   - `ON DELETE SET NULL` 不可行：账本有 `BEFORE UPDATE OR DELETE` 的
     append-only 触发器，FK 驱动的 UPDATE 会被 RAISE；且三列 SET NULL 还会破坏
     "source 三列同 NULL/同非 NULL" 的 CHECK。
   - 触发器守卫（`BEFORE DELETE ON extension_versions`）无法区分"GC 回收"与
     "扩展卸载级联"，只能靠会话级 GUC 开后门，脆弱且对后续调用方不可见。
   - 结论：应用层判据放在 `ListVersionGCCandidates` 这条共用路径上才是正确位置
     （与 `open_operation` 挡板同款：那条也没有 FK）。
4. **不加索引**：`theme_runtime_publications` 是低体量 append-only 账本（dev 21
   行，生产一次主题激活一行），候选上限 1000 且 GC 是显式运维动作；若将来账本
   上量，再补 `(source_theme_id, source_theme_version, source_package_digest)`
   的部分索引即可。

## Verification

- `cd apps/api && go build ./...` → 通过。
- 干净库 `sforum_gc_test`（`go run ./cmd/migrate` 建好后）：
  `SFORUM_TEST_DATABASE_URL=… go test ./app/Models/Extensions/ -count=1` → `ok`
  （68s）。
- dev 库（`sforum`）上同一命令**已转绿**（4 个包全 `ok`）。转绿前它会红 3 条：
  `extension_versions` 的历史行已累计 1104 条 > `versionGCMaxCandidates = 1000`，
  全局计划（`VersionGCOptions{}`）按 `extension_id` 排序被 LIMIT 截断，
  `test.version-gc.*` 的 fixture 落在 1000 行之外。用 `git archive HEAD` 的干净
  checkout 复跑 `TestVersionGCCandidatesCriteriaMatrix` 同样失败，可判定为既有
  测试对共享库体量的隐含假设，不是回归。已把 `CandidatesCriteriaMatrix` /
  `ExecuteRemovesDiskThenLedger` / `DiskDeleteFailureKeepsLedger` /
  `AdminServiceRequiresSuperAdmin` 改成 `VersionGCOptions{ExtensionID: fixture.id}`
  作用域计划（断言一字未动）。
- `sforum extension gc --dry-run`（隔离 schema `gc_evidence`，复刻 revision 20
  的 source 三元组 + 一条无引用版本）：

```
version GC plan (dry-run): retain=1 retainDays=0 extensionId=""
  blocked      demo.legacy-theme            1.0.0    reason=within_retention
  reclaimable  demo.legacy-theme            1.0.0    digest=1111… installed=2026-09-01 10:00:00
  blocked      sforum.default-theme         1.0.0    reason=within_retention
  blocked      sforum.default-theme         1.0.0    reason=publication_history
reclaimable: 1
```

  两条 `1.0.0` 都是 rank 2（超出 retain=1）：被 revision 20 source 引用的那条
  是 `publication_history`，无引用的那条仍可回收——同一份输出里就是对照。

## revision 20 处置结论

- **留作历史，不重新 apply、不重建制品。**
- 依据：revision 21（`startup_repair`，2026-10-27 06:06:49，target/source 均为
  `2f06a9e4…`）已是最新修订且被所有 live boot `applied`；watcher 只 apply 最新
  修订，revision 20 不会再被执行，它的 failed ack 只是那两次已死 boot 的证据。
- 重建 `555ed50d…` 也不可能：digest 是 10-23 那次构建的精确字节哈希，当前内置
  主题制品是 `2f06a9e4…` / `7b4c868c…`。
- 账本行**未删改**（append-only 触发器保护，本次全程只读）。

## task-3 补强：变异 B/C 与 dev 库转绿

reviewer 的变异测试挖出两个覆盖缺口，已补：

- **变异 B（主题侧锁内复核零覆盖）**：删掉 `deleteReclaimableVersionRow` 里的
  主题账本重读，整套 `TestVersionGC*` 仍绿——因为公开 sweep 在计划阶段就被
  blocked 判据挡住了，走不到锁内复核。已在
  `TestVersionGCBlocksThemePublicationSource` 末尾直接调
  `deleteReclaimableVersionRow`，断言 `*versionGCTargetDriftError` 且
  `reason == publication_history`、行数仍 1。重跑变异 B：删代码 →
  `in-lock theme recheck error = <nil>` 红；恢复（文件 sha256 与变异前一致）→ 绿。
- **变异 C（三列合取缺负例）**：新增
  `TestVersionGCThemeSourceMatchRequiresExactDigest`——同一 `extension_id` +
  同一 `version`、只有 `package_digest` 不同的历史行（`UNIQUE (extension_id,
  version, package_digest)` 允许并存）必须仍 `reclaimable`，且作用域 sweep 只
  删这一条、不碰被 source 引用的那条。把 SQL 的
  `AND t.source_package_digest = h.package_digest` 删掉后该用例红
  （同 id+version 被误判成 `publication_history`），恢复后绿。

## Next

- 无阻塞项。索引与决策 addendum 已补；后续若要提高主题回收率，唯一出口是
  "只钉最新修订"的独立决策（本 session 的 Decisions 第 2 条）。
