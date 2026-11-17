# Decision: 预构建设置页在「已授权但未挂载」时先给骨架，不给 Schema 表单

## Status

Accepted（2026-11-18）。收口
`knowledge/sessions/2026-10-15-admin-extension-page-trust-flash.md` 的 Open Question：
「`SFTrustedSettingsComponent` 在信任未决/已决未挂载时该不该继续渲染 Schema fallback 插槽」。

## Context

2026-10-15 那一刀把「信任未读到」从「未授权」里拆了出来，但只拆了一半：
`SFTrustedSettingsComponent` 的骨架条件写成 `!trustResolved`，而 fallback 插槽的条件写成
`trustResolved`。因为设置页的信任结论由 `page-bootstrap` 在 SSR 期间同步带回，
`resolved` 在首帧就已经是 `true`，这两个条件实际上互斥地把「已授权但组件还没挂载」
这一段交给了 fallback 插槽 —— 宿主按 manifest 生成的整张 Schema 表单。

结果是一个比 2026-10-15 更贵的闪屏：绿色「推荐默认值」卡 + 四条 callout + 全部分组字段先
铺满首屏，微前端 `import()` 完成后整块替换，滚动位置与视觉焦点一起跳。管理页那侧
（`SFTrustedAdminPageComponent`）在 2026-10-15 已经改成「已授权未挂载也给骨架」，设置页
漏了同一步。

## Decision

- **D1**：`trustResolved && trusted && !mounted` 属于骨架，不属于 Schema 表单。设置文档
  里已经声明了预构建组件，插件 `import()` 通常只需几百毫秒；先用骨架占住几何，比让
  操作员先读一张马上会消失的表单更便宜。
- **D2**：骨架与 Schema 回退由**同一个纯函数** `resolveTrustedSettingsFirstPaint` 产出，
  不在模板里各写一套条件。两个分支必须互斥，且「信任未读到 → 只骨架」，避免回到
  「表单 → 组件」的二次闪替。
- **D3**：骨架按目标页面的两个语义区（绑定区 / 字段区）给两张带边框卡片
  （2 行 + 6 行），不是一张通用 4 行卡。核心组件不写任何扩展专属文案，形状只是几何。
- **D4**：样式与入口并行取（`Promise.all`），样式仍在 `mount` 之前注入且仍校验
  `content-type`。并行省的是一次 RTT，不是省校验：`text/css` 之外的内容一律 fail-closed。
- **D5**：`hasComponent` 必须同时要求组件描述符与 digest。`performLoad` 在
  `!digest.value` 时直接早退，装上不去；若首帧判定只认描述符，就会永久停在骨架，
  连失败告警都不出现。

## Consequences

- 首帧（含 SSR HTML）在已授权页面上是骨架，`hasSkeleton=true / hasRecommended=false`；
  操作员看不到 Schema 表单，除非真的回退。
- 强退到 Schema 的入口不变：右下角「恢复声明式 Schema UI」、加载失败、读不到状态、
  未授权、缺组件描述符 —— 五种情况仍然渲染 `#fallback` 插槽。
- 页面结构测试（`adminFrontendTrustFlash.test.ts`）现在钉住分支链
  `showSkeleton → failed → forcedFallback → !status → !trusted`，顺序改动会直接红。
- 与 2026-10-15 的结论一致而不是取代它：那次定的是「未读到 ≠ 未授权」，这次补的是
  「已授权 ≠ 可以画表单」。

## Revisit

若以后出现「预构建组件 import 稳定超过 2s」的实际案例，正确的做法是给骨架加进度提示
或改成分段渲染，而不是把 Schema 表单请回首帧 —— 那会让表单里的旧值参与一次假的可编辑
窗口。
