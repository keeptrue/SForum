# 2026-11-12 徽章等级表改造 — Session Handoff

状态：**通过**（初验为「有条件通过」，P1 与 P2 已修复并在 shell 恢复后重跑门禁复验）。
模块笔记：`../modules/badges.md`；独立验收：`../reports/2026-11-12-badges-tiers-review.md`；
入口：`../index.md`。

## Changed

- **迁移 `apps/api/database/migrations/202611120001_badge_tiers.sql`**：新建
  `badge_tiers`（`key` 主键 + 双语名 + `icon` + `color` + `position` + `enabled`），
  种子 gold/silver/bronze（`position` 0/1/2，与旧 `Weight()` 等价，所以升级后逐人的
  最高级徽章不变），删除 `badges_tier_check`、改加
  `badges_tier_fkey ... ON UPDATE CASCADE ON DELETE RESTRICT` + `badges_tier_idx`。
  down 段顺序固定为「非内置档徽章落回 bronze → 拆外键 → 恢复原 CHECK → 删表」。
- **后端领域层**：`Badges/tiers_store.go`（`ListTiers` / `GetTier` / `CreateTier` /
  `UpdateTier` / `DeleteTier` / `ReorderTiers`，含 `badgeCount` 聚合与最后一档保护）、
  `Badges/tiers.go`（`resolveBadgeTier`：切档必须存在且启用，保持原值放行）、
  `Badges/admin_tiers.go`（服务层 + 写后重算）、`Badges/grant_candidates.go`
  （候选搜索 + `MaxGrantCandidates = 20`）、`Badges/admin_service.go` 的
  `BatchGrant` 接线与 `topBadgeAffected` 判定。
- **排序口径重写**：`postgres_store.go` 的 `topBadgeOrderSQL` 从
  `CASE tier gold>silver>bronze` 改成 `badge_tiers.position → badges.position →
  granted_at DESC`，并要求每条使用它的查询自带
  `JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`；新增
  `badgeTierColumnsSQL`（双语名/颜色/位次，`LEFT JOIN` + `COALESCE` 兜住等级行缺失）。
- **配色单源**：9 色 palette 上提到 `apps/api/app/Support/Palette`，
  `TrustLevels/tier_colors.go` 降为薄包装（同名函数转发）；前端
  `apps/web/app/utils/theme/tierColor.ts` 成为唯一实现，token 明暗两套在
  `assets/css/sforum-trust-colors.css` 的 `--sf-tl-*`，自定义 hex 深色走
  `color-mix(in srgb, <hex> 74%, white)`。公开面 chip 统一为 12% 底 / 34% 环线，
  bronze 虚线边框移除。
- **后台四分区**：`pages/admin/badges.vue` 的 tab 从 badges/groups/grants 扩为
  badges/groups/**tiers**/grants；新增 `SFAdminBadgesTiersTab.vue`（等级列表 + 外观
  弹窗：共享 `SFIconPicker`、9 色 + 自定义 hex、明暗双底预览、对比度只提示不阻断）与
  `SFAdminBadgeUserPicker.vue`；`SFAdminBadgeGrantDialog.vue` 改成「搜索 + 多选批量 +
  逐人三态结果 + 只重试失败项」，body 单滚动区、footer 吸底。
- **契约与目录**：`contracts/openapi/paths/badges.yaml` / `schemas/badges.yaml` 补
  tiers 与 grant-candidates；`docs/extensions/v3/catalog-identities.json` 补 8 条路由 +
  6 个组件身份（与 `generate.mjs` 的 public 策略、`reviewedGuardPolicies`、
  registry tripwire 410→418 对得上）。
- **P1 修复（已复验）**：`tiers_store.go` 的 `UpdateTier` 改为事务 +
  `FOR UPDATE` 锁行，`input.Enabled == false` 时先数「除自己以外还有几个启用档」，
  为 0 返回 `ErrBadgeTierInUse`（409 `forum.badge_tier_in_use`），与 `DeleteTier`
  同口径；前端 `SFAdminBadgesTiersTab.vue` 用 `enabledCount` / `isLastEnabled()`
  禁用最后一档的开关并给 `title`；i18n `admin.badges.tiers.hint` 改成同时说明
  「不能选给新徽章」与「不参与最高级徽章竞争」，新增
  `admin.badges.tiers.lastEnabledHint`（中英）；新增集成测试
  `TestUpdateTierRejectsDisablingLastEnabledTier`（拒绝 + 不落库 + 有其它启用档时允许）。
- **P2 修复（写路径 QA 发现，已复验）**：新建等级不传 `position` 时落 0，与金牌并列最高档。
  新增哨兵 `TierPositionAppend = -1`（`tiers.go`，`validateTierInput` 放行），
  `tier_handlers.go` 的 `createInput()` 缺省填哨兵，`tiers_store.go` 的 `CreateTier` 用
  `INSERT ... SELECT CASE WHEN $6 < 0 THEN max(position)+1 ELSE $6 END` 在一条语句里解析
  （并发创建不会拿到同一个 max+1）；显式传 0 仍被尊重。集成测试
  `TestCreateTierAppendsToEnd`（缺省 → 3、显式 0 → 0），`tiers_test.go` 的负 position 用例
  改成 `-2` 并新增「哨兵被接受」。

## Decisions

- **等级是数据，不是枚举**。`badges.tier` 从 CHECK 换成外键，运营可增删档位、改展示
  顺序；写死的 `CASE` 权重会让新档位永远排最后，所以排序改读 `badge_tiers.position`。
- **停用一档 = 该档不存在**（排序查询用 `JOIN ... AND t.enabled`，不是 LEFT JOIN）。
  已授予行保留（历史不抹），但它不再参与最高级徽章竞争。
- **删除必须给 `migrateTo`**：外键 `ON DELETE RESTRICT` + 服务层校验（存在、非自身、
  启用、不是最后一档）。拒绝静默级联——「30 枚徽章悄悄降成铜牌」不该由数据库替运营决定。
- **`key` 不可变**：它同时是 URL 段（`/admin/badges/tiers/:tierKey`）与 `data-tier`
  的取值，允许改名会让前端跟着编码。改名只改 `name_zh` / `name_en`。
- **palette 上提到 `Support/Palette`**：跨领域共享、无领域依赖，`Badges` 不 import
  `TrustLevels`，方向单向；`TrustLevels` 保留同名薄包装让调用点零改动。
- **颜色 token 优先、hex 是逃生口**：token 的明暗取值住在 CSS（换主题跟着走），hex
  没有第二组取值所以深色现算提亮；比例只在 `tierColor.ts` 写一次，JS 与 CSS 两处实现
  必须同步改。
- **授予结果三态不合并**：`skipped` 是幂等命中不是错误，`failed` 要逐条给原因；重试
  只跑失败项并累加计数。
- **候选搜索不返回邮箱、不要求 `user.view`**：只匹配 username / display_name，权限是
  `badge.view`——它只回答「谁可以被授予这枚徽章」，不该顺带变成账号目录搜索。
- **最后一档保护前后端各拦一道**：前端禁用开关是可用性，后端 409 才是边界（前端拦
  不住直接打 API 的调用方）。

## Next

1. **`features.badges` 关闭态**：14 条徽章端点 403、后台三态告警（仍未取证）。
2. **帖子列表 chip / 资料页 chip**：当前库里没有用户持徽章（基线 0），要先授予才有 chip，
   再验等级改色后的 TTL 延迟（帖子 20–30s、资料页 ≤10s）。
3. **`RecomputeAll` 改投 River job**：改序 / 启停 / 删除等级三处目前都在 HTTP 请求内同步
   跑，上万 `user_badges` 时是长事务。
4. **P3 收尾**：390 宽颜色行需横向拖动才能触达「按位置自动」与最后几色（可发现性
   代价真实存在）；图标 picker 的 grid 288px 滚动区嵌在弹窗 body 滚动区内，390 下手势先
   被 grid 吃掉；`useAdminBadges.ts:203` 注释与后端「空查询返回前 20 个用户」不符（UI
   侧不发请求，线上无泄露）；palette parity 测试保留 4 个候选路径、两处分叉会静默取第一
   份，建议收敛成单一断言；迁移 down 的 `UPDATE badges SET tier='bronze'` 没动
   `updated_at`；`Support/Palette` 目录名与包名 `palette` 不一致（Go 允许，与仓库其它
   Support 包惯例不符）。
5. **跨档并发窄窗口**：两个请求各停一档时各锁各自的行，理论上仍可能同时通过「至少保留
   一档」检查。要根治用 advisory lock（`pg_advisory_xact_lock`）。

## Open Questions

- 等级改名 / 改色要不要主动失效 Forum 读模型缓存？现在靠 TTL（帖子 20–30s），运营改完
  颜色会先看到旧色。主动失效要按「哪些主题/评论载荷带这枚徽章」反查，成本与收益待评估。
- 自定义 hex 的对比度现在**只提示不阻断**（`TIER_MIN_DARK_CONTRAST = 3`）。要不要在保存
  时硬拦？硬拦会挡住「我知道它对比度低但这是品牌色」的合法用法。
- `ReorderTiers` 允许只提交部分 keys（未列出的按原序追加在后）。若将来等级列表分页，
  「只排可见档位」会让不可见档位的顺序在两次拖拽间漂移，需要重新确认这个语义。
- 批量授予单次上限 50 人是够用的起点还是要做分批/进度？现在失败只回一次三态结果，
  没有中途进度。
