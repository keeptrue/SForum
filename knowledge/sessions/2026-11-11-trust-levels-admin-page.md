# 2026-11-11 信任等级独立页（论坛管理 → 信任等级）

hot handoff。前置：D33（earnable 编辑器）、D34（基线门）。本刀把后台配置面从
`/control-panel/settings?tab=trustLevels` 提为 `/control-panel/forum/trust-levels`，
页内三 tab `overview`（缺省）/ `thresholds` / `earnable`，**整页一次保存**。
**三 tab 与页级吸底 footer 已全部落地**（2026-11-11 收尾；浏览器证据见 spec §14.6）。

## Changed

- **决策记录（新）**：`../decisions/2026-11-11-trust-levels-standalone-admin-page.md`
  —— D-A 独立页 + 三分区、D-B 门控 `trust.manage`、D-C 整页一次保存、D-D 分档限额本轮不做；
  每条附被否方案，另有「实施期教训 L1–L3」（三个单测与静态门禁都看不见的缺陷）与回退条件。
  它**显式取代**两处旧结论：模块 note 的 D 组 #18、视觉规范 §0 第 23 行。
- **模块 note**：`../modules/trust-levels.md` 多处修订 ——「后台页」行改为新页 + 三 tab + 新文件路径；
  「登记点 9 处」按独立页形态重写（9 处全要，#1–#4 换目标文件），并补 catalog identity
  **按 `source` 匹配**的迁移规则；D 组 #18 加取代标注，视觉规范 §0 那行同步失效。
- **docs 入口（旧描述确实存在，已改）**：`docs/zh-CN/usage/trust-levels.md` 4 处、
  `docs/en-US/usage/trust-levels.md` 4 处（`Settings → Trust levels tab` → `Forum → Trust levels`），
  分区名与 i18n 现值逐字对齐（zh「门槛设置」/「能力解锁」，en `Thresholds` / `Capability unlocks`）。
  `node tests/validate-docs.mjs` → `all checks passed`。
- **实现（frontend 的 task-2，我只读核对 + 跑门禁，未改 apps/）**：
  `pages/admin/forum/trust-levels.vue`（实测 315 行，`TrustLevelsTab = 'overview' | 'thresholds' | 'earnable'`，
  `normalizeTab` 回落 `overview`）+ 四个组件（`SFAdminTrustLevelsOverviewTab` /
  `SFAdminTrustLevelThresholdsTab` / `SFAdminTrustLevelEarnableTab` / `SFAdminTrustLevelEarnableMatrix`）
  + `composables/admin/forum/trustLevels/`（三个 composable）；`adminModules.ts` 的页定义
  `requiredPermissions: ['trust.manage']`、侧栏位在 `/forum/settings` 之后；
  旧 `?tab=trustLevels` 在 `pages/admin/settings/index.vue:34-35` 重定向，四处 tab 登记已删。

## 门禁与证据

| 项 | 结果 | 谁跑的 |
| --- | --- | --- |
| `cd apps/web && bun test tests/admin` | 735 pass / 0 fail | **本文作者重跑**（4.4s） |
| `node scripts/v3-catalog/generate.mjs --check` | `V3 P0 catalogs are current (18 files)` | **本文作者重跑** |
| `node tests/validate-admin-framework.ts` | passed | **本文作者重跑** |
| `node tests/validate-architecture-boundaries.mjs` | passed（2092 files） | **本文作者重跑** |
| 全量 `bun test` | 2112 pass / 0 fail / 236 files | reviewer + Lead（作者未重跑） |
| `bun run typecheck` | exit 0 | reviewer + Lead（作者未重跑） |
| 浏览器 | spec §14.6 / §14.7，截图 `storage/qa/trust-levels/`（gitignored） | reviewer + Lead 复核（作者未重跑） |

浏览器要点：硬加载三条路径（直接访问 / F5 / `?tab=trustLevels`）均 `pageState=ready`、
`/admin/trust-levels` 恰 1 次；3 tab × 5 轮 = 15/15 稳定；390 刷新按钮 `right 357 ≤ 390`
且工具栏无内层滚动；门槛 6 列×5 行（桌面）/ 2 列末项满宽（390）；吸底 footer gap 42 / 34px。

## Decisions

- **D-A/D-B/D-C/D-D** 见决策记录；其中 D-B 的根因链（无 `trust.manage` 的载荷里没有
  `trust.levels`，前端 `useTrustLevelsForm.ts:368` 把推荐默认值当现配置）是本刀真正的驱动因素。
- **三个已修缺陷（别丢，细节与判据见决策记录「实施期教训」）**：
  P1 硬加载永久 loading —— `hasLoaded` 用 `ref(false)` 靠 `useAsyncData` handler 置位，
  水合时 handler 不重跑；已改为从 `items` 派生。**教训：SSR-first 页面禁止用副作用布尔表达
  「已加载」**，且 `pageState` 判定顺序即语义（`forbidden → failed → loading → feature-off → ready`）。
  P2 保存双提交 —— `type="submit"` 与 `@click="save"` 同在 `@submit.prevent` 表单内 → 两次 PUT、
  两条 toast；**happy-dom 不实现隐式提交**，单测抓不到，只有浏览器网络面板能坐实；已去掉 `@click`。
  P3 白名单重复请求（`onMounted` + `watch(items)`）→ 只短路 `status === 'loading'`；
  390 工具栏命令被挤出视口 → 左右槽 `min-w-0 flex-1` / `shrink-0` + 刷新按钮 `aria-label`/`title`。
- **与冻结稿的两处增量（不是漂移）**：新增 i18n 键
  `admin.forum.trustLevels.page.loadFailedReason`（zh「失败原因：{reason}」/ en `Reason: {reason}`，
  failed 态展示后端 reason，与矩阵失败态同口径）；新增测试钩子 `data-sf-trust-levels-reset` 与
  `data-sf-trust-levels-footer`（spec §12 原表只有 `-save`）。
- **一个容易误判的事实**：硬加载时 `GET /api/v1/admin/web-options` 由 Nitro **服务端**发出，
  浏览器网络面板按设计看不到——**不要当成「请求没发」**。等价证据：SSR HTML 的 `#__NUXT_DATA__`
  含 `admin-web-options` 与 `trust.levels`；客户端路由进入时浏览器可见 1 条。

## Next

- **designer**：把上面两处增量回填进
  `../reports/2026-11-11-trust-levels-admin-page-spec.md` 的 §12 钩子表与 i18n 清单
  （该文件是冻结契约，未回填前以决策记录 + 本 handoff 为准）。
- **reviewer**：若后续能取得无 `trust.manage` 的测试账号，补一轮 `forbidden` 态浏览器实测
  （见 Open Questions 1）。

## Open Questions

1. **`forbidden` 态无浏览器证据**：dev 库只有 `zhuchunshu`（`super_admin`，持 `trust.manage`）
   与 20+ `seed_*`（`member`，随机密码、无 `admin.access`），**未取得符合条件的账号，未验证**；
   目前只有单测覆盖。
2. **关闭态警告无 `role="alert"` / `aria-live`**：共享 `UAlert` 的既有行为，本轮未修（矩阵加载态
   有 `aria-live="polite"`），不影响本次验收。
3. 是否需要 `trust.view`：D9 已否（不做第五个权限位）；真出现只读岗位另开决策，别用 `admin.access` 放开。
4. 分档限额编辑器（D-D）排期与三态控件形态未定；`null` 往返测试是它的前置。
