# 2026-11-11 Session Handoff — 档位色单源渲染 + 后台 IA 归位（收口刀）

## Changed

- **后台五分区图标块收敛到共享 `.sf-tl-mark`**（`apps/web/app/assets/css/sforum-trust-colors.css`）：
  总览阶梯 / 档位管理行 / 门槛设置 / 发言限额 / 能力解锁 + 资料页 `SFTrustLevelCard`。
  色相一律来自**行根内联** `--tl-accent`（`tierColorOf` → `tierAccentStyle`），
  分区里只留尺寸与 `--sf-tl-mark-radius`。删掉了六处 `--tl-accent: var(--sf-accent)` /
  `--sf-accent-dark` 子层声明（遮蔽病根）与 `--sf-accent-soft` 淡底。
- **形状轴搬家**：资料页卡的 `data-step` 从卡根移到 `.sf-tl-mark` 上；卡根保留 `data-level`
  （`data-level="0"` 虚线两处都保留）。
- **分类/标签图标色单一来源**：`utils/forum/forumTaxonomy.ts` 新增 `taxonomyIconColor()` /
  `taxonomyColorInputValue()`，替换 `pages/admin/forum/{categories,tags}.vue`、
  `components/forum/{SFCategoryIndexPage,SFCategorySelect,SFTagInput}.vue`、
  `navigation/SFCategoryNavigationBlock.vue` 里的 4 份内联副本。
- **后台 IA**：`config/adminModules.ts` 把 `/forum/trust-levels` 从「论坛管理」移到
  「用户/权限」（徽章之后）；页面 id / 路由 / 组件身份 / API 全不变（D-C7）。
- **TL1 色相回归修复（用户复核后补刀）**：共享块 `[data-step="1"]` 原来照抄旧「强度阶梯」的
  `color: var(--sf-fg-secondary)`，把 rank 1 的档位色整个吃掉 —— 表现为「同一档在档位管理是 teal、
  其余五面中性灰」。改为 `color-mix(--tl-accent 34%, --sf-border)` 环 + 透明底 + 档位色字形；
  浏览器复测 TL1 = `rgb(15, 118, 110)`、TL2 淡底、TL3 2px 环、TL4 实心，四档行根 `--tl-accent`
  与图标色一一对应。

## Decisions

- **D-C6 色相唯一来源 = `tierColorOf` → 行根内联 `--tl-accent`**；档位图形禁用站点 accent、
  禁用 `--sf-accent-soft`；淡底 `color-mix(--tl-accent var(--tl-accent-tint), --sf-card)`；
  形状梯度（1px / 2px / 实心 / TL0 虚线）保留。并写明病根：**自定义属性在更深一层声明会遮蔽祖先内联值**。
- **D-C7 后台 IA 只搬菜单**：理由 = B-28（主体是用户不是内容）+ 扩展 admin surface `targetId` 稳定性。
  被否：改路由到 `/trust-levels` 需退役 identity + 重生成 V3 目录 + 迁移扩展 target。
- 两条决策全文见 `decisions/2026-11-11-trust-level-color-render-unification.md`。

## 验收（独立验收，非实现者自述）

- 命令门：`bun test tests/admin tests/profile tests/forum` 1358 pass / 0 fail（136 文件 6.44s）；
  `bun test` 全量 2269 pass / 0 fail（240 文件 12.50s）；`node tests/validate-admin-framework.ts` passed；
  `node tests/validate-architecture-boundaries.mjs` passed（2108 文件扫描）；`bun run typecheck` exit 0。
- 改色端到端（**researcher 报告 P3 缺口已由本刀闭环**）：`qa_tl_admin_rw`（`admin.access` +
  `trust.manage`）会话 `PUT /api/v1/admin/trust-levels` 把 TL1/TL2 设 `rose`（同时把 TL2 门槛临时降到
  TL1 同值，为造出 level-2 命中）→ CDP/Chromium 实测**六面同变玫红**：总览阶梯 TL2
  `color: rgb(190,24,93)` / 淡底 `color(srgb .969 .891 .924)`、档位管理左色条、门槛设置、发言限额、
  能力解锁、资料页卡根内联 `--tl-accent: var(--sf-tl-rose)` + 达成段 `background: rgb(190,24,93)`；
  暗色 `rgb(244,114,182)`。390×844 无横向溢出（390/390）。
- 回滚已验证：`PUT` 原始 config → `GET /admin/trust-levels` 与原始值**逐字段相等**、
  `web_options.value` 里 `"color"` 键计数 **0**、`resident.days_visited` 回到 **15**、公开读面回到
  `slate/teal/emerald/sky/indigo`，浏览器复拍六面回默认色序。证据与回滚命令在
  `storage/qa/trust-level-color-unify/REPORT.md`。

## Next

后续项**未实现**，只登记（来自 `reports/2026-11-12-color-surface-matrix.md`，已按刀切好）：

1. **分类详情页头部不渲染图标**（`components/forum/SFCategoryShowPage.vue` 全文件无 `icon`/`iconColor`）：
   配置了颜色也看不到；要么加图标块，要么在后台分类表单里说明该色「在这里不显示」。
2. **话题列表分类 chip 是 slug 哈希硬编码 4 色**（`utils/forum/forumListPresentation.ts`），与配置色无关；
   要改成配置色必须先给 `ForumTopicSummary` 加 `categoryIcon/categoryIconColor` 字段
   （后端 DTO + OpenAPI + 生成物），不是纯前端改动。
3. **标签色公开面无可见载体**（标签索引卡、标签详情页头/右栏、话题列表与详情 tag chip 全是文字）：
   同上，需要 `ForumTopicTagSummary` 加字段，或明确「标签色只在编辑器 chip 生效」并写进后台说明。
4. **后台页 SSR 首屏渲染推荐默认色，配色要等客户端取数后 hydrate**（验收时 900ms 取样会看到
   emerald 默认色，等长一点即 rose）：不是本刀引入的，但会让截图验收误判；若要改，
   是「后台页数据取数从 client-only 改 SSR」的独立刀。

## Open Questions

- 侧栏负向断言（「论坛管理」下没有它）目前只有静态门（`validate-admin-framework.ts` 精确数组断言），
  浏览器只能证明「唯一入口 + 父文件夹是用户/权限」—— QA 账号 `qa_tl_admin_rw` 权限过窄
  （只有 `admin.access` + `trust.manage`），侧栏根本不渲染「论坛管理」文件夹。要浏览器级负向证据，
  得用一个能看见全部文件夹的账号。
- 暗色 teal / violet / amber 的「环线 + 淡底可辨」只有 token 级证据（取值 + `reports/2026-11-11-trust-tier-color-palette.md` §9
  的对比度表），没有人眼目检结论。
