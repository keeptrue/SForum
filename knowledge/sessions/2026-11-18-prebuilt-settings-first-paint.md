# 2026-11-18 预构建设置页首屏：骨架替代 Schema 表单

## Changed

- 新增 `apps/web/app/components/extensions/settings/firstPaint.ts`：纯函数
  `resolveTrustedSettingsFirstPaint(state)` 一次产出互斥的 `showSkeleton` /
  `showSchemaFallback`，模板与测试都只认这两个布尔值。
  - `showSkeleton = !mounted && !failed && !forcedFallback && (!trustResolved || (hasStatus && trusted && hasComponent))`
  - `showSchemaFallback = trustResolved && !mounted && (failed || forcedFallback || !hasStatus || !trusted || !hasComponent)`
- `SFTrustedSettingsComponent.vue`：
  - 模板第 252 行起的骨架条件与第 311 行的 fallback 条件都改成上面两个值；告警链顺序保持
    「骨架 → 失败 → 强制回退 → 读不到状态 → 未授权」。
  - 骨架从一张 4 行卡换成两张带边框卡片（`SFSkeleton :lines="2"` 贴近绑定区、
    `:lines="6"` 贴近字段区），外层 `aria-busy="true"`，保留 `role="status"` 的
    sr-only「正在加载受信任组件…」；删掉原「loading 提示条」`UAlert` 分支。
  - 样式与入口改 `Promise.all([loadStyle(), import(...)])` 并行；样式仍在 `mount` 之前
    注入，仍校验 `content-type` 是否 `text/css`。
  - 新增 `renderedKey`（digest / component.id / locale / resolvedMode / trustState /
    forcedFallback）：已挂载且 key 未变时 `performLoad` 直接返回，`dispose` 与
    `captureFailure` 清空它。
  - `hasComponent` 传 `Boolean(component.value && digest.value)`，与 `performLoad` 的
    `!component.value || !digest.value` 早退条件对齐 —— 有描述符无 digest 时按「缺组件」
    回退，不会永久停在骨架。
- 新增 `apps/web/tests/admin/prebuiltSettingsFirstPaint.test.ts`：8 种状态各一条，加一条
  7 个布尔 2^7 全组合的互斥/不变式扫描；另有一条钉住 `hasComponent` 与 `performLoad`
  两条条件必须同时收 digest。
- 更新 `apps/web/tests/admin/adminFrontendTrustFlash.test.ts`：分支链改为
  `showSkeleton → failed → forcedFallback → !status → !trusted`；旧断言
  `<div v-if="trustResolved">` 换成 `<div v-if="showSchemaFallback">`，并补
  「已授权但未挂载时不出 Schema 表单」。

## Root Cause

页面的信任结论由 `GET /admin/extensions/{id}/page-bootstrap` 在 SSR 期间带回，
`useAdminFrontendTrust` 用初值同步置 `resolved = true`。旧模板的「未挂载」分支只在
`!trustResolved` 时给骨架，于是 `trusted = true` 的页面立刻走到
`v-if="trustResolved"` → 渲染 fallback 插槽里的整张宿主 Schema 表单（绿色「推荐默认值」
+ 四类 callout + 各分组字段），等微前端 `import()` 完成后再整块替换。

实测（修复前，带登录态 fetch 同一 URL）：`hasSkeleton=false / hasAppIdInput=true /
hasRecommended=true`，即 SSR 首帧就是表单。

## Decisions

见 `knowledge/decisions/2026-11-18-prebuilt-settings-first-paint.md`（D1–D5）。要点：
已授权但未挂载时先给骨架；骨架与 Schema 回退由同一个纯函数产出；骨架按「绑定区 +
字段区」两卡成形；样式与入口并行但样式先校验后注入；`hasComponent` 必须含 digest。

## Verification

- `cd apps/web && bun test tests/admin/prebuiltSettingsFirstPaint.test.ts tests/admin/adminFrontendTrustFlash.test.ts tests/admin/prebuiltSettingsComponent.test.ts`
  → 20 pass / 0 fail。
- `cd apps/web && bun run typecheck` → exit 0。
- `cd apps/web && bun test`（全量，345 文件）→ 3532 pass / 0 fail / 1 skip
  （`app.config.ts 必须在 apps/web/app/ 下`，产物缺失的既有 skip）。
- SSR HTML 探针（登录态同源 fetch，剥掉 `#__NUXT_DATA__` 等 script 段后判定可见标记）：
  `hasSkeleton=true`（2 张卡 / 8 行）、`hasAppIdInput=false`、`hasRecommended=false`、
  `hasAriaBusy=true`、无 `form`/Schema 节点。
- 浏览器硬刷（CDP `Page.captureScreenshot` 帧序列 + DOM 时间线，15 帧 / 2.6s）：
  首帧骨架 → 2.1s 微前端挂载；每一帧 `schema=false`，全程没有 Schema 表单。
- 三种回退复核：强制（点「恢复声明式 Schema UI」→ 表单出现、组件卸载；再点
  「重新检查组件信任」→ 仅骨架 0.4s 后回组件）、未授权（改写这一次 HTML payload 里的
  `trustState` → 警告 + 信任面板 + Schema）、加载失败（拦截样式请求 → 失败告警 +
  Schema + 重试）。
- 1440 与 390×844：无横向溢出（`document.scrollingElement.scrollWidth <= innerWidth`）。

## Next

- 可选：SSR 期间为已信任组件入口发 `<link rel="modulepreload">`（boot 里已有 digest），
  网络面板确认命中；未信任时不能发（该 digest 的资产端点会 404）。
- 可选：骨架目前固定两卡，若以后有设置页声明了完全不同的分区数，可让 manifest 提供
  骨架形状提示 —— 现在不做，避免为单一插件引入契约。

## Residuals

独立审查通过后的三条非阻塞项，均不改变本次结论。

- **F1（测试接线缺口）**：`resolveTrustedSettingsFirstPaint` 自身被 8 种状态 +
  2^7 全组合钉死，但它的 7 个入参是由组件的 computed 接线的，这层接线没有测试。
  漏传一个（例如 `trustResolved`）时现有测试仍全绿，运行时却会永远停在骨架。
  建议后续在 `prebuiltSettingsFirstPaint.test.ts` 补一条源码断言，把组件的参数列表
  钉住（本轮的 `hasComponent` 已用同一手法钉过）。
- **F2（既有问题，HEAD 同样）**：`restoreSchemaFallback` 在 `trusted` 时会调
  `mutate('revoke')`，而 V3 exact-artifact 下
  `DELETE /api/v1/admin/extensions/{id}/frontend/trust` 返回 404，于是点「恢复声明式
  Schema UI」会弹一条红色 404 toast。回退行为本身仍然生效（强制回退是先写字面量再做
  撤销），**不是本次引入**，也不在本刀范围。
- **F3（平台约束，可接受）**：sessionStorage 里已有强制回退标记时，硬刷首帧仍会先出
  一帧骨架再切 Schema —— SSR 读不到 sessionStorage，服务端无法知道这个客户端选择。
  属于预期表现。
