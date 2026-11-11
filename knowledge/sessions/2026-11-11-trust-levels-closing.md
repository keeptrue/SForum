# 2026-11-11 Session Handoff — 信任等级收尾（M9–M13）

Status: **已提交 `57a3de5ad`**，`./scripts/test.sh --fast` **三线 exit 0**（api 267s / web 42s / static 14s，
日志 `tests/.logs/{api,web,static}.log`）。计划 `../plans/2026-11-11-trust-levels-closing.md`；
裁决 `../decisions/2026-11-11-trust-level-earnable.md`（D33）；
**本刀最重要的产出**是 P0 决策 `../decisions/2026-11-11-host-command-authority-gate-p0.md`；
矩阵增量 `../reports/2026-11-11-trust-levels-acceptance-matrix-v3.md`（唯一编号源）。
**验收数字以 reviewer 合并表为准，本文件不抄计数**（计数会变，结论不会）。

## Changed

- **M9 契约**：`eligiblePermissions` 的「顺序 = UI 展示序、七键固定」写进
  `contracts/openapi/schemas/trust_levels.yaml`；形状断言用**字面集合**，不用实现反推。
  注意白名单顺序 ≠ `SeedMemberPermissions` 顺序（**集合相等、顺序不同**），UI 只认白名单序。
- **M10 编辑器**：`apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue`
  （TL1–TL4 × 白名单；TL0 整行 disabled）。白名单唯一来源 = 后台读面 `eligiblePermissions`，
  请求失败 → **不可编辑 + 错误条，绝不回落空矩阵**；`earnable: []` 原样带回（`?? []` 不能改 `?? null`）。
- **M11 收口**：证据按矩阵 ID 落 `storage/qa/trust-levels/{A…K}/`，脚本
  `scripts/verify-trust-levels-acceptance.sh` 按 ID 命名。新增 `Models/Identity/permission_origin_test.go`
  与 `Models/TrustLevels/level_purity_test.go`（纯度：fake store 调用计数 == 0）。
- **M12 两条 dev 库的账**：`apps/api/app/Http/options_error.go` 成为 `MapOptionsError` 唯一真源
  （Options / TrustLevels 共用）+ AST tripwire。**有意变更**：写库失败 422 → **500
  `options.store_failure`**；422 只剩 `options.invalid_value` / `options.invalid_value_set`。
- **M13 扫上一刀**：徽章后台 i18n 整块缺失，74 键文案表见
  `../reports/2026-11-11-badges-admin-i18n-copy.md`（67 字面 + 7 动态），已落 locale。
- **M13 P0（本刀最重）**：`host_commands` 授权闸门被 Go 方法提升静默丢弃——见
  `../decisions/2026-11-11-host-command-authority-gate-p0.md`。修法三层：wrapper 重声明返回 wrapper 的
  `WithTrustLevelSource` + 装配点显式两步 + 两条 tripwire 与一条带对照组的语义守卫。

## Decisions

**D33 = A 路。** 默认 `earnable = ∅`，而默认 `member` 组已持有正白名单全部 7 键（集合相等、顺序不同），
所以「TL2 解锁 `topic.create`」在默认安装是**零行为变化**——等级只影响限额与展示；机制建好却没人能配置
就是死代码（D17）。裁决：把编辑器做完，**默认空集保持**，运营按需开启。
**B 路（Z3）被否**：收窄 `member` 基线、7 键改由 TL1 授予，前置是重写「开关关闭」的语义
（现 D9 = 关闭 → 已挣得集合恒空；收窄后一关开关全站会员不能发帖），那是 F 组口径重写 + 权限行迁移，
不该和编辑器混进同一次提交。

## 本刀换来的五条硬纪律

1. **默认安装下 earnable 是空操作，这不是 bug 是算术。** 7 键全在 `member` 基线里，
   `mergeEarnedPermissions` 的 `present[key]` 让它们**进不了** `Earned`
   （`Identity/postgres_store_roles.go:341-357`），于是 `Earned` 恒空、`GrantSource` 恒 `static`。
   判据只能是**配置生效**，不是行为变化。想让 TL 真有牙只能走 B 路。
2. **同一份逻辑两个副本会分开腐烂。** `mapOptionsError` 曾有两份：修了 Options 那份，
   **真正踩到症状的 TrustLevels 那份没修**，两份行为还不一致。这是本仓第三次同形复发
   （前两次：fixture base schema、错误映射）。看到「两处实现同一判断」，默认假设它们已分叉。
3. **没有人见过它红的守卫等于没有守卫。** 四条实例：不可证伪的纯度断言、只断言自己一致的形状测试、
   `go test -run '^Name$'` 匹配不到也退 0（必须看日志有没有 `=== RUN`）、扫描器跳过自己的根。
   规则：**守卫交付必须附「它红过的样子」**，否则不算交付。
4. **共享资源需要串行化。** 本刀三次并发污染：共享树里就地变异撞了别人的验收、开关被另一人翻掉
   导致 11 条假 FAIL、就地 TS 变异差点废掉证据。规则：变异走 `-overlay`/临时副本；验收前抢锁；
   **开关状态写进证据头部**。
5. **矩阵绿 ≠ 仓库绿。** 矩阵 124 条全绿的同时整仓门禁一直红着，提交前跑 `./scripts/test.sh --fast`
   才发现 P0（两个 worktree 二分定性为基线引入）。矩阵只证明「声明过的条目有证据」，不证明
   「没声明的地方没问题」。**下一刀验收必须顺带跑整仓门禁，矩阵 `0 FAIL` 不得单独作为可提交依据。**

## Next

1. **`/badges` 公共目录页**：要 Page Registry 全套（catalog / route shell / ViewModel / Host island /
   主题模板 / 完整性测试），独立一刀。
2. **`SFComment.vue` 537 行**：拆分归属未定，本刀只登记。
3. **`sqlc generate` 不可安全运行**：会把 `database/sqlc/models.go` 从 84 行推到约 2481 行并重命名 param。
4. **`LoadEffectiveActorTx` 不展开父→子继承**：插件命令路径今天就会误拒 `extension.manage` 持有者。
5. **`trust.new_user.forbid_attachments` 死配置**：接线还是删键，未决。
6. **`mapOptionsError` 的 422 → 500 是有意变更，不是回归**——别「顺手修回去」。

下一会话：先 `./scripts/test.sh --fast` 三线 + 四条静态门禁；动 earnable 前重读 D33 与纪律 1。

## Open Questions

- B 路（Z3）什么时候开：它决定「信任等级」是不是真有牙，但前置是 F 组口径重写 + 迁移。
- earnable 要真生效，必须**同时**把该键从 `member` 角色里移除（否则被 static 吸收）。
  编辑器要不要就地提示，甚至提供「从角色移除」的入口？
- 阈值全 0 = 人人达标（文档已写）。阈值表单要不要加一条内联警告？现在只有文档，UI 不提示。
- `/badges` 是独立后台页却挂在 `adminSidebarNavigation` 的 **forum 文件夹**下。徽章的权限模块确实是
  `forum`（`Identity/seeds.go:102`），但信息架构上它既不是论坛内容也不是论坛设置，更像一级菜单或
  system 组。**「forum 文件夹里有徽章」不是配置错误，是既有配置**；是否调整归产品另开一刀。
- `GrantSource` 目前**无任何生产消费者**：J-14 证明了默认安装下这 7 键返回 `static`，但没有 UI 或
  接口读它。若将来前端要做「为什么我不能发帖」的解释，那时再暴露 `origin`——届时它才有消费者。
