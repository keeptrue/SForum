# 2026-11-11 Session Handoff — 信任等级收尾（M9–M13）

Status: **in progress**。计划 `../plans/2026-11-11-trust-levels-closing.md`；
裁决 `../decisions/2026-11-11-trust-level-earnable.md`（D33）；
矩阵增量 `../reports/2026-11-11-trust-levels-acceptance-matrix-v3.md`（J/K/L 组，唯一编号源）。
**验收数字以 reviewer 的合并表为准，本文件不抄计数**（计数会变，结论不会）。

## Changed

- **M9 契约**：`eligiblePermissions` 的「顺序 = UI 展示序、七键固定」写进
  `contracts/openapi/schemas/trust_levels.yaml`；形状断言用**字面集合**，不用实现反推
  （`apps/api/app/Http/Controllers/TrustLevels/controller_test.go`）。
  注意白名单顺序 ≠ `SeedMemberPermissions` 顺序（**集合相等、顺序不同**），UI 只认白名单序。
- **M10 编辑器**：`apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue`
  （TL1–TL4 × 白名单；TL0 整行 disabled）。白名单唯一来源 = 后台读面 `eligiblePermissions`
  （`apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts`）：请求失败 →
  **不可编辑 + 错误条，绝不回落空矩阵**；`earnable: []` 原样带回（`?? []` 不能改 `?? null`）。
  8 处登记点已齐（组件 / 两份 locale / `docs/extensions/v3/catalog-identities.json` / 生成物）。
- **M11 未实现收口**：证据按矩阵 ID 落 `storage/qa/trust-levels/{A…K}/`，脚本
  `scripts/verify-trust-levels-acceptance.sh` 按 ID 命名（矩阵有而脚本没有的进矩阵 §8）。
  新增 `apps/api/app/Models/Identity/permission_origin_test.go`（`GrantSource` 与 `Can()` 走不同路径）、
  `apps/api/app/Models/TrustLevels/level_purity_test.go`（`Level()` 纯度：fake store 调用计数 == 0）。
- **M12 两条 dev 库的账**：`apps/api/app/Http/options_error.go` 成为 `MapOptionsError` 的**唯一**真源
  （Options / TrustLevels 共用），AST tripwire `options_error_tripwire_test.go` 守「只有一份」。
  **有意变更**：写库失败从 422 改成 **500 `options.store_failure`**，422 只剩两个细分码
  `options.invalid_value`（归一化失败）/ `options.invalid_value_set`（不在枚举内）。
- **M13 扫上一刀**：徽章后台 i18n 整块缺失（两份 locale 各 0 个 `admin.badges.*` 定义）——
  74 键文案表在 `../reports/2026-11-11-badges-admin-i18n-copy.md`（67 字面 + 7 动态），已交 frontend。

## Decisions

**D33 = A 路。** 默认 `earnable = ∅`，而默认 `member` 组已持有正白名单全部 7 键（集合相等、顺序不同），
所以「TL2 解锁 `topic.create`」在默认安装是**零行为变化**——等级只影响限额与展示；机制建好却没人能配置，
就是死代码（D17）。裁决：把 earnable 编辑器做完，**默认空集保持**，运营按需开启，零行为变更。
**B 路（Z3）被否**：收窄 `member` 基线、7 键改由 TL1 授予——它的前置是重写「开关关闭」的语义
（现 D9 = 关闭 → 已挣得集合恒空；收窄后一关开关全站会员不能发帖），那是 F 组口径重写 + `member`
权限行迁移，不该和编辑器混进同一次提交（混在一起，出问题分不清是编辑器写坏配置还是基线收窄错了）。

## 本刀换来的四条硬纪律

1. **默认安装下 earnable 是空操作，这不是 bug 是算术。** 7 键全在 `member` 基线里，
   `mergeEarnedPermissions` 的 `present[key]` 会让这些键**进不了** `Earned`
   （`Identity/postgres_store_roles.go:341-357`），于是 `Earned` 恒空、`GrantSource` 恒 `static`。
   所以「勾了就能发帖」的文案和断言都是假的；判据只能是**配置生效**（`earnable` 里确有该键），
   不是行为变化。想让 TL 真有牙只能走 B 路（Z3，本刀明确不做）。
2. **同一份逻辑两个副本会分开腐烂。** `mapOptionsError` 曾有两份：修了 Options 那份，
   **真正踩到症状的 TrustLevels 那份没修**，两份行为还不一致。现在收成 `apphttp.MapOptionsError`
   单一真源 + AST tripwire。这是本仓第三次同形复发（前两次：fixture base schema、错误映射）——
   看到「两处都实现了同一判断」，默认假设它们已经分叉。
3. **没有人见过它红的守卫等于没有守卫。** 本刀四条实例：不可证伪的纯度断言（fake store 调用计数
   没有断言）、只断言自己一致的形状测试（用 `EarnableWhitelist()` 反推白名单）、
   `go test -run '^Name$'` 匹配不到也退 0（输出 `ok … [no tests to run]`，必须看日志里有没有
   `=== RUN`）、扫描器跳过自己的根目录（「零命中」其实是没看自己）。
   规则：**守卫交付必须附「它红过的样子」**，否则不算交付。
4. **共享资源需要串行化。** 本刀三次并发污染：在共享树里就地变异撞了别人的验收、
   开关被另一人翻掉导致 11 条假 FAIL、就地 TS 变异差点废掉证据。
   规则：变异走 `-overlay` 或临时副本；验收跑前抢锁；**开关状态写进证据头部**（否则下一个人无法复现）。

## Next

未闭合，按优先级：

1. **`/badges` 公共目录页**：要 Page Registry 全套（catalog / route shell / ViewModel /
   Host island / 主题模板 / 完整性测试），是独立一刀，本刀只登记。
2. **`SFComment.vue` 537 行**：拆分归属未定，本刀登记不修。
3. **`sqlc generate` 不可安全运行**：会把 `database/sqlc/models.go` 从 84 行推到约 2481 行并重命名 param，另开一刀。
4. **`LoadEffectiveActorTx` 不展开父→子继承**（`Identity/actor_postgres_tx.go`）：后果是插件命令路径
   今天就会误拒 `extension.manage` 持有者。看到「插件命令 403 而 HTTP 放行」先查这条，别当成 TL 的 bug。
5. **`trust.new_user.forbid_attachments` 死配置**：值存了、后台显示了、没有消费者。接线还是删键，未决。
6. **`mapOptionsError` 的 422 → 500 是有意变更，不是回归**——别「顺手修回去」。

下一会话：先 `./scripts/test.sh --fast` 三线 + 四条静态门禁；动 earnable 前重读 D33 与纪律 1。

## Open Questions

- B 路（Z3）什么时候开：它决定「信任等级」是不是真有牙，但前置是 F 组口径重写 + 迁移。
- earnable 要真生效，必须**同时**把该键从 `member` 角色里移除（否则被 static 吸收）。
  编辑器要不要就地提示这件事，甚至提供「从角色移除」的入口？
- 阈值全 0 = 人人达标（文档已写）。阈值表单要不要加一条内联警告？现在只有文档，UI 不提示。
