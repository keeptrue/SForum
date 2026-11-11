# 信任等级独立页 · 设计冻结件（三 tab 几何 / 五态 / 文案）

Date: 2026-11-11
Author: designer（小设）
Status: **冻结**。本文件里的 `data-*`、`aria-*`、类名、i18n 键名与文案即契约：
小前照此实现，小审照此 grep 断言。改契约先改本文件。
消费方：frontend（route shell + overview 面板 + 五态 + i18n）、reviewer（§9 / §12 断言）。
上游：`decisions/2026-11-11-trust-levels-standalone-admin-page.md`（D-A / D-B / D-C / D-D，用户已批准）、
`reports/2026-11-10-trust-level-visual-spec.md` §0（后台几何基线）、
`reports/2026-11-11-trust-level-earnable-spec.md`（矩阵几何）、`modules/trust-levels.md`（D12 / D21 / D27）。

本文只写视觉、几何、状态、无障碍与文案，不含组件实现代码，不含后端改动。
颜色只引用既有类串（后台 = `slate-*` / `zinc-*`）与既有 token（`var(--sf-*)`），不复制色值。

> **本文的写法**：§0–§2、§4–§5、§9.3–§9.4 描述**已在工作区落地的实现**（frontend 已按 D-A 搭好骨架），
> 逐条冻结、不许再改形状；标 **Δ** 的条目是本文相对现状要求**改**的地方，集中列在 §13。
>
> **2026-11-11 视觉对齐**：§15 按「论坛设置」页的设计语言追加了分区卡头、推荐配置 alert 与 overview 行式清单，
> 并**取代** §0.4 / §1.1 / §3 的部分结论（§15.0 有取代关系表）。读 §0.4 与 §3 之前先看 §15.0。

---

## 0. 页面身份

### 0.1 路由、菜单、权限（已落地，逐字冻结）

| 项 | 定稿 | 出处 |
| --- | --- | --- |
| 路由 | `/control-panel/forum/trust-levels` | D-A |
| 页面文件 | `apps/web/app/pages/admin/forum/trust-levels.vue` | 已存在 |
| `definePageMeta` | `{ middleware: 'admin', layout: 'admin' }` | 同 `pages/admin/forum/settings.vue:15-18` |
| `defineOptions` | `{ name: 'AdminForumTrustLevels' }` | — |
| 导航 | `{ id: '/forum/trust-levels', labelKey: 'admin.nav.trustLevels', icon: 'i-lucide-gauge', componentName: 'AdminForumTrustLevels', requiredPermissions: ['trust.manage'] }` | `config/adminModules.ts:241-246` |
| 菜单位置 | 论坛管理 children：moderation → categories → tags → settings → **trust-levels** → content | `adminModules.ts:462-467` |
| 页内 tab | `?tab=overview\|thresholds\|earnable`，缺省 **`overview`** | §1.2（Δ） |
| 页门控 | 读与写同权 `trust.manage` | D-B |

**门控是 `trust.manage`，不是 `admin.access`**，理由是实测过的读面行为：`GET /admin/web-options`
按每条 option 的 `managePermission` 过滤（`Options/service.go:717`），没有 `trust.manage` 的账号
**载荷里根本没有 `trust.levels` 键**，而 `readTrustLevelsConfig`（`useTrustLevelsForm.ts:366-368`）
把「键缺失」当成「用推荐默认」。旧形态下这类账号看到的是**推荐默认值冒充当前配置** ——
不是少看了一部分，是看了假的一份。所以本页对无 `trust.manage` 的账号渲染**无权限态**（§2），
而不是渲染一张假表。

### 0.2 权限三档

| 能力 | 权限键 | 依据 |
| --- | --- | --- |
| 进页 / 读配置 / 保存 / 恢复默认 | `trust.manage` | D-B；`permissionCatalog.trust.manage.description` =「调整信任等级阈值、每档限额与已挣得能力映射，并恢复推荐默认。」 |
| 打开「产品开关」页的入口 | `settings.site.manage` | `adminModules.ts:148-153`（`/settings/features` 的 requiredPermissions） |

`settings.site.manage` **只**决定「功能关闭时是否给一个去开开关的按钮」，不决定本页任何读写能力。

### 0.3 旧入口下线（已落地）

1. `pages/admin/settings/index.vue` 的 `trustLevels` 四处登记全部删除（联合类型 / `validTabs` / tab 列表 / 组件映射），无 re-export、无双入口。
2. `/control-panel/settings?tab=trustLevels` 在设置页初始化前 `replace` 到新页 —— 旧链接必须落地，静默回落到 `basic` 等于让「我明明点过信任等级」变成一次无从解释的跳转。
3. i18n 迁移见 §10.3。

### 0.4 组件落位（已落地）

| 文件 | 职责 |
| --- | --- |
| `pages/admin/forum/trust-levels.vue` | route shell：页头、工具栏、tab 与 query 同步、活动面板、**页级唯一吸底 footer**、五态判定 |
| `components/admin/forum/trust-levels/tabs/SFAdminTrustLevelsOverviewTab.vue` | overview 面板（Δ：待新建，纯展示，无表单控件） |
| `components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue` | thresholds 面板（5 行 fieldset） |
| `components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableTab.vue` | earnable 面板根（`data-sf-trust-levels-earnable`）+ feature-off 锁定块 |
| `components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue` | 矩阵本体（被上面那层包着，全仓库唯一实现） |
| `composables/admin/forum/trustLevels/useTrustLevelsAdminPage.ts` | 表单模型、白名单四态、保存/重置/恢复默认、**五态**（Δ：导出 `pageState`） |
| `composables/admin/forum/trustLevels/useTrustLevelsForm.ts` | 读写层纯函数（`readTrustLevelsConfig` / `trustLevelsPayload` / `blockNonIntegerKey` …） |
| `composables/admin/forum/trustLevels/useTrustLevelEarnableMatrix.ts` | 矩阵派生状态纯函数 |

**面板不许渲染页头、工具栏或 tablist**（AGENTS.md：route shell 独占三者）。
**卡片不设 header**：页 `h2` + 工具栏说明已承担标题与说明，tab 标签即分区名；再加一层卡头就是同一句话说三遍。
分区标题由面板自己承担：overview 用 `h3`，earnable 沿用矩阵自带的 `h3`（`sf-trust-earnable__title`），
thresholds 不设标题（tab 标签 + 六个 `UFormField` 标签已足够）。

> ⚠ **本段被 §15.1 取代**（2026-11-11 追加）：用户要求按「论坛设置」页的设计语言重做视觉，分区卡头（`h3` + 说明 + mono 键徽章）恢复为必需。上面的推理在当时成立（页头 + 工具栏已足够），前提是「本页没有视觉基准」；现在基准是 `SFAdminForumTabFrame`，保留原结论反而会与同域页面分叉。文字保留在此作为历史，**现行契约看 §15**。

---

## 1. 页面骨架（route shell）

### 1.1 DOM 树（已落地形状，逐字冻结）

```
div.flex.w-full.min-w-0.flex-col.gap-4
[data-sf-trust-levels-page] [data-sf-page-state="loading|failed|forbidden|feature-off|ready"]

├─ div.mb-4                                              ← 页头（唯一 h2）
│  └─ h2.flex.items-center.gap-2.text-xl.font-bold.text-slate-900.dark:text-zinc-100
│     ├─ UIcon[name=adminPage.icon].size-5.text-[var(--sf-accent)].dark:text-[var(--sf-accent-dark)]
│     └─ 文本 → admin.forum.trustLevels.page.title
│
├─ UDashboardToolbar.mb-6.rounded-lg.border.border-slate-200.bg-white.px-4.py-2.5.text-slate-500.dark:border-zinc-800.dark:bg-zinc-900.dark:text-zinc-400
│  [data-sf-trust-levels-toolbar]
│  ├─ #left  → div.flex.min-w-0.items-center.gap-2.text-sm
│  │           ├─ UIcon[i-lucide-gauge].size-4
│  │           └─ span.truncate → admin.forum.trustLevels.page.description
│  └─ #right → UButton[color=neutral][variant=outline][leading-icon=i-lucide-refresh-cw][:loading=pending]
│               [data-sf-trust-levels-refresh] → admin.common.refresh
│
├─ [state=failed]    UAlert[color=error][variant=soft][icon=i-lucide-triangle-alert]
│                    [data-sf-trust-levels-load-error]
│                    ├─ title       → admin.forum.trustLevels.loadFailed
│                    ├─ description → admin.forum.trustLevels.page.loadFailedDescription（Δ 新增）
│                    └─ UButton[size=xs][color=neutral][variant=outline][type=button]
│                        [leading-icon=i-lucide-refresh-cw][data-sf-trust-levels-retry]
│                        → admin.forum.trustLevels.page.retry（Δ 新增）
│
├─ [state=forbidden] UAlert[color=neutral][variant=soft][icon=i-lucide-lock]
│                    [data-sf-trust-levels-forbidden]（Δ 新增）
│                    ├─ title       → admin.forum.trustLevels.page.forbidden.title
│                    └─ description → admin.forum.trustLevels.page.forbidden.description
│                    此态**不渲染** tablist / 卡片 / footer
│
├─ [state=feature-off] UAlert[color=warning][variant=soft][icon=i-lucide-power-off]
│                    [data-sf-trust-levels-disabled]（已存在，沿用）
│                    ├─ title       → admin.forum.trustLevels.disabled.title
│                    └─ description → admin.forum.trustLevels.disabled.description（§10.3 改指针）
│
├─ div [data-sf-trust-levels-tabs]        ← 钩子挂包裹层，不依赖共享组件透传 attrs
│  └─ SFAdminFixedTabNav[:items][:model-value=activeTab]
│     [:ariaLabel=t('admin.forum.trustLevels.tabs.label')][@update:model-value=setActiveTab]
│
└─ form.flex.flex-col [v-if="showCard"]           ← showCard = ready | feature-off | loading
   └─ UCard.border-slate-200.bg-white.text-slate-900.dark:border-zinc-800.dark:bg-zinc-900.dark:text-zinc-100
      [:ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"]
      ├─ body → div.space-y-5 [data-sf-trust-levels-panel] [:data-tab="activeTab"]
      │         ├─ [state=loading] div.sf-trust-levels-loading [data-sf-trust-levels-loading][aria-busy="true"]
      │         └─ KeepAlive [v-else]
      │            ├─ [overview]   SFAdminTrustLevelsOverviewTab   key="overview"   :form :saved-requirements :trust-levels-enabled @jump
      │            ├─ [thresholds] SFAdminTrustLevelThresholdsTab  key="thresholds" :form :can-manage @threshold @restore
      │            └─ [earnable]   SFAdminTrustLevelEarnableTab    key="earnable"   :form :whitelist :can-manage :trust-levels-enabled @toggle @retry
      └─ #footer [v-if="showFooter"] → §6      ← showFooter = ready | feature-off（**不含 loading**）
```

`KeepAlive` 保留（已落地）：表单状态在 composable 里，切 tab 本就不丢改动；保活只是别让矩阵每次重算列宽。
三个面板互斥渲染（`v-if` / `v-else`），任何时刻**只有一个活动面板**。

> ⚠ **本节被 §15.1 / §15.2 / §15.4 修订**（2026-11-11 追加）：卡片恢复 `#header`（`h3` + 说明 + `UBadge font-mono trust.levels`），
> 工具栏下新增推荐配置 `UAlert`，卡片体从 `div.space-y-5` 改为 `div.grid.max-w-5xl.gap-6`。
> 本节树里的 `UCard` 无 header、无 alert 的形状是历史版本，现行看 §15。

### 1.2 tab 顺序与缺省值（Δ）

| 项 | 现状 | 定稿 |
| --- | --- | --- |
| `tabIds` | `['thresholds', 'earnable']` | `['overview', 'thresholds', 'earnable']` |
| 缺省 tab | `thresholds` | **`overview`** |
| tab 图标 | `thresholds: i-lucide-gauge`、`earnable: i-lucide-key-round` | 加 `overview: i-lucide-list` |

缺省 `overview` 的理由就是「小白友好」这四个字：第一次进页面的人先要知道**这是什么、现在是什么状态、下一步去哪**，
而不是迎面一张 4×6 数字表。三个 tab 的图标依次是「读 → 配数值 → 配能力」，与引导三步同序。

### 1.3 为什么面板不写 `role="tabpanel"`

`SFAdminFixedTabNav` 的 tab 按钮没有 `id`、也没有 `aria-controls`（`shared/SFAdminFixedTabNav.vue:26-40`）。
只给面板加 `role="tabpanel"` 而不补 `id`/`aria-labelledby`，是半套 ARIA —— 读屏会播报一个没有归属的面板。
所以与 `pages/admin/settings/index.vue`、`pages/admin/forum/settings.vue` 保持一致：**tablist 有 `role` + `aria-label`，面板不加 `role`**。
要补全就整条链一起补（按钮 `id` + `aria-controls` + 面板 `aria-labelledby`），那是独立一刀。

---

## 2. 五态（一个状态机，一个 `data-sf-page-state`）

判定优先级（先命中先定态）：`forbidden` → `failed` → `loading` → `feature-off` → `ready`。

| 态 | 触发条件 | 渲染 | 不渲染 |
| --- | --- | --- | --- |
| `forbidden` | `!can('trust.manage')`（D-B：读面同样按此过滤，键缺失即无权限）；请求返回 401/403 同样归此态 | 页头 + 工具栏 + 无权限 `UAlert`（`data-sf-trust-levels-forbidden`） | tablist、卡片、footer、重试按钮 |
| `failed` | 页级数据请求失败（非 401/403） | 页头 + 工具栏 + 错误条（`data-sf-trust-levels-load-error`）+ 重试按钮（`UAlert` 的 `#actions` 槽，`data-sf-trust-levels-retry`）+ tablist。错误条 description **二选一**：`loadErrorReason` 非空 → `page.loadFailedReason`（`{reason}` 原样显示，403/500/网络错误一眼可辨）；为空 → 回落 `page.loadFailedDescription` | 卡片与 footer |
| `loading` | 首次请求在途（SSR 首帧与客户端同形） | 页头 + 工具栏（刷新按钮 `:loading`）+ tablist + **卡片**（`showCard` 含 `loading`；卡片体只剩占位块 `data-sf-trust-levels-loading`，`aria-busy="true"`，虚线框 `min-height:180px`，文案 `page.loading`） | 三个面板；**footer**（`showFooter` 不含 `loading`，见 §6 规则 1） |
| `feature-off` | `features.trust_levels` 解析为 false（`useWebOptions().trustLevelsEnabled`，未下发按 true） | 页头 + 工具栏 + 关闭警告条（`data-sf-trust-levels-disabled`）+ tablist + 卡片；面板按 §3/§4/§5 的 feature-off 分支渲染 | —— |
| `ready` | 数据到齐、有权限、开关为开 | 全部 | —— |

四条硬规则：

1. **`forbidden` 必须先于一切**：没有 `trust.manage` 时，`readTrustLevelsConfig` 会返回推荐默认值，**渲染出来就是假现值**。这一态不许有任何表单像素。
2. **`loading` 不画骨架微光**（视觉规范禁动效）：占位块 = 虚线框 + 一行 `page.loading`，尺寸与矩阵锁定块一致（`min-height:180px`），切态不跳动。
3. **`failed` 的错误条不自动关闭**（AGENTS.md：error 不自动关），且必须给出「重试」这一条出路，不许只有一句报错。
4. **`feature-off` 全页只允许一条警告条**，位置在 tablist 之上；overview 的「功能状态条」在该态下只渲染**紧凑状态行**（徽章 + 一行说明 + 可选入口），**不重复** `disabled.title` / `disabled.description` 这两句长文案。同因同屏两条同名警告，运营会以为其中一条指向别的问题。
5. **`loading` 不渲染 footer**：卡片在（占位块要有个框），保存入口不在。理由不是美观 —— `hasChanges` 在数据到齐前恒为 `false`，此时给一个禁用按钮只会让人以为「保存坏了」。唯一出处是 §6 规则 1；本节表格与 §6 冲突时以 §6 为准，且**不许两处各写一套**。

### 2.1 状态来源（单一出处，别在页面里各算一套）

`useTrustLevelsAdminPage()` 导出 `pageState` 与 `loadErrorReason`，页面只读这两个值：

```
pageState = !canManage            ? 'forbidden'
          : error && !hasLoaded   ? 'failed'
          : !hasLoaded            ? 'loading'
          : !trustLevelsEnabled   ? 'feature-off'
          :                         'ready'
```

`hasLoaded` = 首次 envelope 请求已成功返回（不是「有数据就算」：`useWebOptions` 的默认值会让 `pending` 与
「有数据」同时成立，那会把 `loading` 判成 `ready`）。

### 2.2 页面态 ≠ 白名单态（别混成一套）

| 层 | 状态源 | 取值 | 影响面 |
| --- | --- | --- | --- |
| 页面态 | admin web options（`trust.levels`、`features.trust_levels`）+ `trust.manage` | §2 五态 | 整页 |
| 白名单态 | `GET /admin/trust-levels` 的 `eligiblePermissions`（`useTrustLevelEarnableWhitelist`） | `idle / loading / ready / failed` | 只影响 earnable 面板的矩阵块 |

页面态为 `ready` 时，矩阵仍可能处于 `loading` / `failed` —— 那时**只**由矩阵自己渲染占位与错误条
（既有钩子 `data-sf-earnable-loading` / `data-sf-earnable-error`），页面不升态、不重复报错。

---

## 3. tab `overview`（等级总览，新写 · Δ）

> ⚠ **§3.2 之后的阶梯 DOM 被 §15.3 取代**（2026-11-11 追加）：`ol` 阶梯改行式清单 `ul.divide-y`；指标摘要规则（§3.2）与徽章计算（§3.3）不变。

面板体 = 一个 `<section class="space-y-5">`，不套第二张 `UCard`（第二张卡 = 第二个视觉层级）。

```
section.space-y-5 [data-sf-trust-levels-overview] [aria-labelledby="sf-trust-levels-overview-title"]
│
├─ h3#sf-trust-levels-overview-title.text-sm.font-semibold → admin.forum.trustLevels.overview.title
│
├─ div.flex.flex-wrap.items-start.justify-between.gap-3
│  [data-sf-trust-levels-feature-state="on|off"]
│  ├─ div.flex.min-w-0.items-center.gap-2
│  │  ├─ UBadge[color=neutral|warning][variant=soft]
│  │  │  → overview.feature.enabled | overview.feature.disabled
│  │  └─ span.text-xs.text-muted
│  │     → overview.feature.enabledHint | overview.feature.disabledHint
│  └─ UButton[v-if="off && can('settings.site.manage')"][size=xs][color=neutral][variant=outline]
│     [leading-icon=i-lucide-toggle-left][to="/control-panel/settings/features"]
│     [data-sf-trust-levels-open-features] → overview.feature.openFeatures
│
├─ div.flex.flex-wrap.items-start.gap-2
│  [data-sf-trust-level-state="recommended|custom"]
│  ├─ UBadge[color=primary|neutral][variant=soft]
│  │  → overview.state.recommended | overview.state.custom
│  └─ p.text-xs.text-muted
│     → overview.state.recommendedHint | overview.state.customHint
│
├─ ul.m-0.list-none.space-y-1.p-0.text-xs.text-muted [data-sf-trust-levels-scope]
│  └─ li ×3（前缀 UIcon[i-lucide-info].size-3.5[aria-hidden="true"]）
│     → overview.scope.tl0 / overview.scope.levels / overview.scope.stack
│
├─ div [data-sf-trust-levels-ladder]
│  ├─ h4.text-sm.font-semibold → overview.ladder.title
│  └─ ol.m-0.list-none.p-0.divide-y.divide-slate-200.dark:divide-zinc-800
│     └─ li ×5  [:data-level="0"…"4"] [:data-metrics="<非 0 指标个数>"]
│        ├─ div.flex.flex-wrap.items-center.gap-2
│        │  ├─ span.sf-trust-levels-ladder__icon[:data-level][aria-hidden="true"]
│        │  │  └─ UIcon[name=trustLevelIcon(level)].size-3.5    ← 五档图标阶梯（视觉规范 §2.2 环线/填充）
│        │  ├─ strong.text-sm.font-semibold → admin.forum.trustLevels.level.<slug>（D21 终稿名）
│        │  ├─ span.text-[11px].font-semibold.tabular-nums.text-muted → profile.trustLevel.code {level}
│        │  └─ UBadge[v-if="earnableCount(level) > 0"][color=neutral][variant=soft]
│        │     [data-sf-trust-levels-earnable-count="<n>"] → overview.ladder.earnable {count}
│        └─ p.mt-1.text-xs.text-muted
│           ├─ level === 0 → admin.forum.trustLevels.tl0Hint（迁移键，逐字复用）
│           └─ level ≥ 1    → 非 0 指标摘要（§3.2）；六项全 0 → overview.ladder.noThresholds
│
├─ div [data-sf-trust-levels-guide]
│  ├─ h4.text-sm.font-semibold → overview.guide.title
│  └─ ol.m-0.grid.list-none.gap-2.p-0.sm:grid-cols-3
│     └─ li ×3（序号 1/2/3 用文本 + tabular-nums；不用 emoji、不用装饰符号）
│        → overview.guide.step1 / step2 / step3
│
├─ div.flex.flex-wrap.gap-2 [data-sf-trust-levels-jump]
│  ├─ UButton[type=button][color=neutral][variant=outline][leading-icon=i-lucide-gauge]
│  │  [data-sf-trust-levels-jump-thresholds] → overview.jump.thresholds   （切到 thresholds）
│  └─ UButton[type=button][color=neutral][variant=outline][leading-icon=i-lucide-key-round]
│     [data-sf-trust-levels-jump-earnable] → overview.jump.earnable       （切到 earnable）
│
└─ p.text-xs.text-muted [data-sf-trust-levels-limits-note] → overview.limitsNote
```

### 3.1 三条定性说明（措辞已核，不许改写）

| 键 | 定稿 | 事实依据 |
| --- | --- | --- |
| `overview.scope.tl0` | TL0「新用户」的判定条件由站点设置里的「新人限制」页决定，本页只读展示。 | `admin.settings.newcomers.title` =「新人限制」；`useTrustLevelsForm.ts:22` 注释：TL0 由 `trust.new_user_*` 决定，`config.go` 拒绝它出现在 `trust.levels` 里 |
| `overview.scope.levels` | TL1–TL4 的判定门槛由本页的「门槛设置」决定，保存后即时生效。 | 迁移键 `admin.forum.trustLevels.description` 原文含「保存后即时生效」 |
| `overview.scope.stack` | 信任等级只叠加能力，不剥夺用户组已经授予的权限。 | 矩阵基线披露原文：「已由默认用户组授予的键，勾选不会改变用户实际拿到的权限，能力来源仍记为『用户组』」 |

### 3.2 指标摘要格式（只用 `metric.*` 六项）

- 取 `form.requirements[level]`，按 `TRUST_LEVEL_THRESHOLD_METRICS` 的**声明顺序**（= Go `Metrics` 顺序）过滤 `value > 0`。
- 每项渲染 `overview.ladder.metricItem`，参数 `{metric, value}`：`{metric}` = `admin.forum.trustLevels.metric.<i18nKey>`，`{value}` = 整数。
- 项间用 `overview.ladder.metricsSeparator`（zh「、」/ en「, 」）连接。
- 六项全 0 → 渲染 `overview.ladder.noThresholds`，**不渲染空列表、不渲染占位符**。
- `≥` 的语义依据：阈值是「达到或超过即判定」—— 进度条算法以 `current / threshold` 的比值 1 为达标点（`reports/2026-11-10-trust-level-visual-spec.md` §1.4）。
- `data-metrics` = 该档非 0 指标个数（`"0"`–`"6"`），供断言「TL4 有 6 项、TL1 有 2 项」这类结构检查。
- 「新增解锁 N 项」的 `N` = 已保存 `form.earnable[level]` 的长度；`N === 0` 时**徽章不渲染**（钩子随之不存在），不写「无」。

### 3.3 两个徽章的计算规则（冻结，不许各算各的）

| 徽章 | 数据源 | 判定 |
| --- | --- | --- |
| 功能状态 `data-sf-trust-levels-feature-state` | `trustLevelsEnabled`（`useWebOptions`，既有解析） | `on` / `off`；不另写一套开关解析 |
| 配置状态 `data-sf-trust-level-state` | **已保存**的 `initial.requirements`（不是正在编辑的 `form`） | 与 `recommendedTrustLevelsForm().requirements` 逐档逐项比较：全等 → `recommended`；任一不等 → `custom`。**只看 `requirements`** |

两个理由：

1. 配置状态徽章回答的是「站点**当前**用的是什么」，所以取已保存值。用户正在改阈值时，徽章不该先跳成「自定义」再随保存回弹 —— 未保存这件事由 §6 的 footer 承担，两个通道不许互相替代。
2. 只看 `requirements`：`limits` 本页不可编辑（D-D），`earnable` 是另一维配置。把它们算进「是否推荐默认」，会让一个刚勾了一项能力解锁的站点显示「自定义门槛」，而门槛分毫未动。

> ⚠ **本节被 §15.2 补充**（2026-11-11 追加）：新增的推荐配置 `UAlert` 与这里的配置状态徽章分工——alert 说「推荐默认**是什么**」（静态），徽章说「当前**是不是**推荐默认」（动态比较已保存值）。两者**不许互相复述**，细则见 §15.2。计算规则本身不变。

### 3.4 两个切 tab 按钮

点击 = `setActiveTab('thresholds' | 'earnable')`，与点 tablist 同一条路径（同时写 `?tab=`）。
**不用** `NuxtLink`/`to`：本页 tab 是 query 状态而非路由，链接化会产生一条可被新标签页打开、却没有页面态的快照。
`openFeatures` 那一个是真路由跳转，所以用 `to`。

---

## 4. tab `thresholds`（门槛设置：几何逐项不变）

来源：原 `SFAdminTrustLevelsTab.vue` 的 5 行 fieldset 区块，已迁入
`components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue`。**几何、类名、断点、`data-*` 全部照搬。**

```
div.space-y-5 [data-sf-trust-levels-thresholds]
│
├─ div.flex.flex-wrap.items-start.justify-between.gap-3      ← Δ：从 route shell 移入本面板
│  ├─ UAlert[color=neutral][variant=soft][icon=i-lucide-info]
│  │  [data-sf-trust-levels-restore-hint] → admin.forum.trustLevels.restoreHint
│  └─ UButton[type=button][color=neutral][variant=outline][leading-icon=i-lucide-rotate-ccw]
│     [:disabled="!canManage"][data-sf-trust-levels-restore].self-start
│     → admin.forum.trustLevels.restoreDefaults
│
├─ fieldset.sf-trust-threshold-row ×5
│  [:data-level="0"…"4"] [:data-readonly="true|false"]
│  ├─ legend.sf-trust-threshold-row__legend
│  │  ├─ span.sf-trust-threshold-row__icon[:data-level][aria-hidden="true"] > UIcon.sf-trust-threshold-row__glyph
│  │  ├─ span.sf-trust-threshold-row__name → admin.forum.trustLevels.level.<slug>
│  │  └─ span.sf-trust-threshold-row__code → profile.trustLevel.code {level}
│  ├─ p.mb-2.text-xs.text-muted [v-if="level === 0"] → admin.forum.trustLevels.tl0Hint
│  └─ div.sf-trust-threshold-row__grid
│     └─ UFormField ×6（顺序 = TRUST_LEVEL_THRESHOLD_METRICS）
│        [:name="`trust.level.${level}.${metric.suffix}`"]
│        [:label="admin.forum.trustLevels.metric.<i18nKey>"]
│        ├─ level === 0 → UInput[type=number][placeholder="—"][disabled][:model-value="''"]
│        └─ level ≥ 1   → UInput[type=number][min=0][:max=metric.max][@keydown=blockNonIntegerKey]
│
└─ p.text-xs.text-muted [data-sf-trust-levels-zero-unlimited]  ← Δ：从页面级移入本面板
   → admin.forum.trustLevels.zeroUnlimited
```

不可退让的六条：

1. **TL0 行只读**：六个 `UInput` 全部 `disabled`、`model-value=""`、`placeholder="—"`；绝不渲染成可编辑再靠保存拦截。
2. **列数 6 / 3 / 2**（媒体查询按**视口**）：`<768` → 2 列且**末项跨 2 列**（6 项 → 2+2+2，无空格）；`768–1279` → 3 列（6 → 3+3）；`≥1280` → 6 列（恰好等于 metric 数）。CSS 见 `SFAdminTrustLevelThresholdsTab.vue` 的 `sf-trust-threshold-row__grid` 三段，逐字沿用。
3. **`blockNonIntegerKey`** 挂在每个可编辑输入上：拦 `.`/`e`/`+`/`-`，避免小数进 `requirements`。
4. **`zeroUnlimited` 那句常驻**，位置在最后一行 fieldset 之后（本面板内）。
5. **D12 披露 = 常驻 `UAlert`，紧贴「恢复推荐默认」按钮**（同一 `flex` 行），**禁止**改成 Toast / tooltip / 折叠：`runRestore` 没有确认步骤，这句话必须在按下按钮**之前**读得到。
6. **恢复默认只重置 `requirements`**（`useTrustLevelsAdminPage.restoreThresholds`）：`limits` 与 `earnable` 原样保留。`restoreHint` 迁移后的中文原文已经是「恢复默认只重置五档的阈值。每档限额与能力解锁勾选保持不变；…」——**与代码一致，不要再改**。

### 4.1 feature-off 下的 thresholds

开关关闭时 thresholds **仍可编辑**（`PUT /admin/web-options` 只校 `trust.manage`，不查开关 —— 既有 R1 事实），
警告由 §2 的页面级 `data-sf-trust-levels-disabled` 一条承担，本面板不再重复渲染第二条同名警告。

---

## 5. tab `earnable`（能力解锁：矩阵整块 + 兜底）

```
div.space-y-5 [data-sf-trust-levels-earnable]
│
├─ [feature-off] div.sf-trust-levels-locked [data-sf-trust-levels-earnable-feature-off]（Δ 新增）
│                ├─ UIcon[i-lucide-power-off].size-4[aria-hidden="true"]
│                └─ span → admin.forum.trustLevels.earnable.featureOff
│                （虚线框、min-height:180px，与矩阵锁定块同尺寸；不渲染矩阵）
│
└─ [feature-on]  SFAdminTrustLevelEarnableMatrix[:form][:whitelist][:can-manage][@toggle][@retry]
                 → 既有钩子一个不少：data-sf-earnable-region / -baseline / -count /
                   -readonly / -error / -reason / -retry / -locked / -loading /
                   -gate / -gate-badge / -orphans / -empty
```

矩阵的三条既有语义**逐条继承，不重写**（`SFAdminTrustLevelEarnableMatrix.vue:11-19`）：

1. **累积**：勾「TL2 的 X」= TL2 及以上全部解锁；UI 只写「该档新增」，标题/说明/aria 三处都带这个语义。
2. **失败 ≠ 空**：`status === 'failed'` 时矩阵整块不渲染（错误条 + 锁定占位）；空矩阵等于向后端说谎。
3. **键 → 人话**复用 `admin.permissionCatalog.<key>.label`，不新建映射表。

本面板额外冻结四条：

1. **不渲染「恢复推荐默认」**：推荐默认的 `earnable` 是 `∅`，放在这里等于提供一个「一键清空运营策略」的按钮。恢复默认只属于 thresholds（§4）。
2. **不渲染保存/重置按钮**：全部归 §6 的页级 footer。
3. **`readOnly` 兜底保留**（`data-sf-earnable-readonly`）：`canManage === false` 时勾选框 `disabled`、`state.editable === false` 时不发 `toggle`。注：D-B 之后本页已整体对无 `trust.manage` 的账号渲染 `forbidden`，这条兜底是**第二道闸**（防未来门控放宽时静默变成「能填不能存」），不是死代码。
4. **feature-off 时不开白名单请求**（`shouldLoadWhitelist` 已实现）：`GET /admin/trust-levels` 的 `requireAdmin` 先判开关，关闭时必然 404 —— 发了只会多一条与「功能已关闭」重复的错误条。此时 `earnableWhitelistForSave` 返回 `undefined`，`trustLevelsPayload` **原样带回已存 `earnable`**，不会把白名单缺失当成「清空」。

---

## 6. 整页一次保存（footer 是唯一保存入口）

D-C 的理由是存储形态，不是审美：阈值、分档限额、能力解锁全在**单个 JSON option 键 `trust.levels`** 里，
`trustLevelsPayload` 只产出一条 `{ name: 'trust.levels', value: <整个 JSON> }` —— **一次保存 = 整键覆盖**。
三个 tab 各自一个保存按钮 = 三个写者对同一个键做全量覆盖，任何一份表单状态不是最新，另两个分区就被静默回滚。

```
#footer（UCard sticky footer，类串见 §1.1；整块由 `v-if="showFooter"` 控制）
└─ div.w-full [data-sf-trust-levels-footer]          ← 页面自己的包裹层，钩子挂这里
   └─ SFAdminFormFooter（不带任何 props：两个槽都由本页提供）
      ├─ #left
      │  └─ div.flex.flex-wrap.items-center.gap-2 [v-if="hasChanges"] [data-sf-trust-levels-unsaved]
      │     ├─ UIcon[i-lucide-circle-alert].size-4.text-amber-500
      │     ├─ span.font-medium.text-amber-600.dark:text-amber-400
      │     │  → admin.forum.trustLevels.page.unsaved
      │     └─ span.text-xs.text-slate-500.dark:text-zinc-400
      │        → admin.forum.trustLevels.saveScope
      └─ #actions
         ├─ UButton[type=button][color=neutral][variant=outline][leading-icon=i-lucide-rotate-ccw]
         │  [:disabled="!canManage || saving"][data-sf-trust-levels-reset]
         │  → admin.forum.trustLevels.reset
         └─ UButton[type=submit][leading-icon=i-lucide-save][:loading="saving"]
            [:disabled="!canManage || !hasChanges"][data-sf-trust-levels-save]
            → admin.common.save
```

为什么手搓 `#left` / `#actions` 而不给 `SFAdminFormFooter` 传 props：`data-sf-trust-levels-save`
必须落在**保存按钮本身**（reviewer 要断言的是那个按钮，不是它的容器），而共享组件不该为单个页面
长一个 `data-*` 属性。按钮类串与 `SFAdminFormFooter` 的默认实现逐字一致（`border-slate-200` /
`bg-[var(--sf-accent)]` / `hover:bg-[var(--sf-accent-hover)]`），几何不分叉 —— 先例是
`components/admin/settings/forum/SFAdminForumTabFrame.vue:36-50` 的 `#footer` 手写。

冻结规则：

1. **footer 在 `ready` 与 `feature-off` 两态都渲染**（关闭时保存仍会成功，这是既有行为，界面不假装它被禁）；`loading` / `failed` / `forbidden` 不渲染 footer。
2. **一次保存提交两处改动**：thresholds 的 `requirements` + earnable 的 `earnable`，同一个 `trustLevelsPayload(form, earnableWhitelistForSave(state))`。footer 左侧必须把这件事说清楚 —— 这就是 `saveScope` 存在的唯一理由。
3. **未保存提示只有一个位置** = footer 左侧。overview 不另设徽章；tablist 上不加小圆点（`SFAdminFixedTabNav` 无此能力，第二个指示器会与 footer 争夺同一信息）。
4. **保存按钮在无改动时禁用**（`!hasChanges`）：一次保存是整键覆盖，没有改动时点它只是把同一份 JSON 再写一遍。
5. **重置按钮重置两处**：`requirements` 与 `earnable` 回到 `initial`，`limits` 不动（本页无编辑器）。成功 Toast 用迁移键 `resetChanges`（文案见 §10.3）。
6. **恢复推荐默认不在 footer**：它只重置 `requirements`，放在整页 footer 会让人以为也重置了 `earnable`。
7. **校验槽位**：`SFAdminFormFooter` 无字段级校验通道；本页唯一的输入护栏是 `type=number` + `min=0` + `blockNonIntegerKey`，已足够。**不许为它编一条校验文案**。

---

## 7. 断点与几何

### 7.1 两个必测视口

| 视口 | 侧栏 | 卡片内宽 | thresholds 列数 | earnable 形态 | overview 阶梯 |
| --- | --- | --- | --- | --- | --- |
| **1633×810**（PC 标准） | 256（`default-size=16`） | 1633−256−48−48 = **1281** | **6 列**（视口 ≥1280 命中） | **表头 + 裸勾选框**，列数 = 白名单长度（7） | 单列，`li` 头行一行放得下（图标 + 名 + TL + 徽章） |
| **390×844**（移动） | 覆盖式抽屉，不占列宽 | 390−32−32 = **326** | **2 列**，末项跨 2 列 | **2 列**，格内名，`项数 % 2 === 1` 时末项满宽 | 单列；`li` 头行 `flex-wrap`，徽章换行到第二行 |

> 侧栏 256 / 两个 48 来自 `reports/2026-11-11-trust-level-earnable-spec.md` §2.1 的实测链
> （`layouts/admin.vue` `p-4 sm:p-6`、`UDashboardSidebar :default-size="16"`、`UCard` 默认 `p-4 sm:p-6`）。
> 390 下侧栏「覆盖式、不占列宽」在该文 §2.1 标注为**假设（未实测）**；本页结论**不依赖**它 ——
> 三档断点全部是**视口**媒体查询（`<768` / `768–1279` / `≥1280`），与容器宽度无关。

### 7.2 移动端 390×844 的五条硬要求

1. 工具栏左侧说明 `truncate`，**不换行、不撑破工具栏**；刷新按钮保留文字（zh「刷新」/ en「Refresh」两词，够短），不强制图标化。
2. footer 内 `SFAdminFormFooter` 的「重置 / 保存」保持文字（`admin.form.reset` / `admin.common.save`）；左侧未保存块独占一行时按钮组右对齐，不许把保存按钮挤成图标。
3. overview 阶梯 `li` 头行 `flex-wrap`：`icon + 名称 + TL{n}` 一行，`新增解锁 N 项` 徽章换行，**不截断、不省略**。
4. overview 三步引导：`<640` 单列堆叠（`grid gap-2`），`≥640` 三列（`sm:grid-cols-3`）。
5. 无横向溢出：`min-w-0` 出现在页面列、卡片体、阶梯 `li`、摘要 `p` 四处；`overflow-wrap:anywhere` 只给长键名与指标摘要。

### 7.3 810 高度

overview 内容约 620–700px（标题 + 功能条 + 配置状态 + 说明 3 行 + 阶梯 5×~56 + 引导 + 两个按钮 + 披露），
810 视口下**首屏看到阶梯前 3 档**，滚到底看到引导；footer 吸底常驻。
阶梯**不折叠、不分页**：5 档是固定集合，折叠只会让运营漏看一档（同 `SFAdminTrustLevelsTab.vue:199` 的理由）。

---

## 8. 无障碍

| 位置 | 要求 |
| --- | --- |
| tablist | 由 `SFAdminFixedTabNav` 提供 `role="tablist"` + `:aria-label`；值 = `admin.forum.trustLevels.tabs.label`（zh「信任等级分区」/ en「Trust level sections」）。每个 tab 已有 `aria-selected` |
| 标题层级 | 全页唯一 `h2`（页面标题）；overview 的 `h3`（等级总览）+ `h4`（五档阶梯 / 从这里开始）；earnable 的 `h3` 来自矩阵。**不许跳级、不许第二个 `h2`** |
| 阶梯 | `<ol>` + `<li>`：顺序语义由列表承担，不手写 `aria-posinset`；档位图标 `aria-hidden="true"`；`TL{n}` 是文本，不是图片替代 |
| 功能/配置状态 | 都是**文本徽章**（`UBadge` 内文字），不用纯图标表达状态；状态不靠颜色单独承担（同时有文字） |
| 矩阵 | 复用既有写法，**零新增 aria 键**：`earnable.aria.cell`（`{level} 新增解锁：{permission}`）、`earnable.baseline.gateAria`（`{level}：{permission} 由系统门控…不可取消`）、`aria-describedby="sf-trust-earnable-note"`、`role="img"` 的门控标记（不进 tab 序） |
| 加载/失败 | 占位块 `aria-busy="true"`；失败条是 `color=error` 的 `UAlert`，带可聚焦的「重试」按钮，**不自动关闭** |
| 焦点 | 切 tab 后焦点留在被点的 tab 按钮上（不抢焦到面板）；保存成功后不移动焦点，只出 Toast |
| 图标态按钮 | 本页**没有**仅图标按钮（刷新/重置/保存都带文字），因此不需要 `aria-label` 兜底；若将来有，`aria-label` 与 `title` 必须同时给 |

---

## 9. `data-*` 钩子总表（reviewer 照此 grep）

### 9.1 页面级

| 钩子 | 位置 | 取值 |
| --- | --- | --- |
| `data-sf-trust-levels-page` | 页面内容根 `div.flex.w-full.min-w-0.flex-col.gap-4` | 恒存在 |
| `data-sf-page-state` | 同上 | `loading` \| `failed` \| `forbidden` \| `feature-off` \| `ready` |
| `data-sf-trust-levels-toolbar` | `UDashboardToolbar` 包裹层 | 恒存在 |
| `data-sf-trust-levels-refresh` | 工具栏刷新按钮 | 恒存在 |
| `data-sf-trust-levels-tabs` | tablist 包裹 `div` | 恒存在 |
| `data-sf-trust-levels-panel` | 卡片体 `div.space-y-5` | 恒存在，另带 `data-tab` |
| `data-tab` | 同上 | `overview` \| `thresholds` \| `earnable` |
| `data-sf-trust-levels-loading` | 加载占位块 | 仅 `loading` |
| `data-sf-trust-levels-load-error` | 失败 `UAlert` | 仅 `failed` |
| `data-sf-trust-levels-retry` | 重试按钮 | 仅 `failed` |
| `data-sf-trust-levels-forbidden` | 无权限 `UAlert` | 仅 `forbidden` |
| `data-sf-trust-levels-disabled` | 功能关闭警告条（**已存在，沿用**） | 仅 `feature-off` |
| `data-sf-trust-levels-footer` | footer 外层 `div.w-full`（`SFAdminFormFooter` 的父节点） | `ready` / `feature-off`（= `showFooter`） |
| `data-sf-trust-levels-unsaved` | footer `#left` 槽内的未保存块 | `hasChanges` 时 |
| `data-sf-trust-levels-reset` | footer `#actions` 槽内的重置按钮 | `showFooter` 时恒存在 |
| `data-sf-trust-levels-save` | footer `#actions` 槽内的**保存按钮本身** | `showFooter` 时恒存在 |

### 9.2 overview（全部新增）

| 钩子 | 取值 |
| --- | --- |
| `data-sf-trust-levels-overview` | 面板根 `section` |
| `data-sf-trust-levels-feature-state` | `on` \| `off` |
| `data-sf-trust-levels-open-features` | 开关入口按钮（仅 `off` 且 `can('settings.site.manage')`） |
| `data-sf-trust-level-state` | `recommended` \| `custom` |
| `data-sf-trust-levels-scope` | 三条定性说明 `<ul>` |
| `data-sf-trust-levels-ladder` | 阶梯容器 |
| `data-level` | 阶梯 `li`：`"0"`–`"4"`（十进制字符串、非零填充；五个固定渲染、顺序 0→4） |
| `data-metrics` | 同上：非 0 指标个数 `"0"`–`"6"` |
| `data-sf-trust-levels-earnable-count` | 「新增解锁 N 项」徽章，值为 `N`；`N === 0` 时该徽章不渲染（钩子随之不存在） |
| `data-sf-trust-levels-guide` | 三步引导容器 |
| `data-sf-trust-levels-jump` | 两个切 tab 按钮的容器 |
| `data-sf-trust-levels-jump-thresholds` | 切到 thresholds 的按钮 |
| `data-sf-trust-levels-jump-earnable` | 切到 earnable 的按钮 |
| `data-sf-trust-levels-limits-note` | 限额披露那一行小字 |

> ⚠ **§15.7 追加两个钩子**（2026-11-11）：`data-sf-trust-levels-card-header`（分区卡头）与 `data-sf-trust-levels-recommended`（推荐配置 alert）。本节其余钩子不变。

### 9.3 thresholds

| 钩子 | 取值 |
| --- | --- |
| `data-sf-trust-levels-thresholds` | 面板根（Δ 新增） |
| `data-sf-trust-levels-restore-hint` | D12 常驻 `UAlert`（Δ 新增） |
| `data-sf-trust-levels-restore` | 恢复推荐默认按钮（Δ 新增） |
| `data-sf-trust-levels-zero-unlimited` | `zeroUnlimited` 那行（Δ 新增） |
| `data-level` / `data-readonly` | 5 个 `fieldset`：`"0"`–`"4"` / `"true"`（TL0）`"false"`（TL1–4） |
| `.sf-trust-threshold-row` / `__legend` / `__icon` / `__glyph` / `__name` / `__code` / `__grid` | 类名逐字沿用（§4） |

### 9.4 earnable

| 钩子 | 取值 |
| --- | --- |
| `data-sf-trust-levels-earnable` | 面板根（Δ 新增） |
| `data-sf-trust-levels-earnable-feature-off` | 关闭态锁定块（Δ 新增，仅 `feature-off`） |
| `data-sf-earnable-region` / `-baseline` / `-count` / `-readonly` / `-error` / `-reason` / `-retry` / `-locked` / `-loading` / `-gate` / `-gate-badge` / `-orphans` / `-empty` | 矩阵既有钩子，一个不少 |

---

## 10. i18n 定稿

命名空间 `admin.forum.trustLevels.*`。**内容键由 `admin.settings.trustLevels.*` 整体迁移而来，键名逐字不变**（§10.3，已落地）。

### 10.1 尚缺的新增键（zh-CN）

> ⚠ **§15.5 / §15.6 追加并退役若干键**（2026-11-11）：新增 `recommended.title` / `recommended.description` / `sections.{overview,thresholds,earnable}.description`；退役 `admin.forum.trustLevels.description` 与 `overview.description`（内容并入 `sections.*.description`，卡头是唯一位置）。本节与 §10.2 的其余键不变。

```json
{
  "admin": {
    "forum": {
      "trustLevels": {
        "page": {
          "loading": "正在读取信任等级配置…",
          "loadFailedDescription": "页面数据没有读到，下面的表单不可编辑。请重试；持续失败请检查 API 服务与账号权限。",
          "retry": "重试",
          "loadFailedReason": "失败原因：{reason}",
          "forbidden": {
            "title": "当前账号无法查看信任等级",
            "description": "信任等级需要「信任等级管理」权限才能查看与修改。请联系站点管理员为你的用户组授予该权限。"
          },
          "unsaved": "有未保存的信任等级更改"
        },
        "saveScope": "保存会一并提交「门槛设置」与「能力解锁」的所有改动。",
        "reset": "重置信任等级更改",
        "tabs": { "overview": "等级总览" },
        "overview": {
          "title": "等级总览",
          "scope": {
            "tl0": "TL0「新用户」的判定条件由站点设置里的「新人限制」页决定，本页只读展示。",
            "levels": "TL1–TL4 的判定门槛由本页的「门槛设置」决定，保存后即时生效。",
            "stack": "信任等级只叠加能力，不剥夺用户组已经授予的权限。"
          },
          "feature": {
            "enabled": "功能已开启",
            "enabledHint": "前台信任等级接口可用，等级会按当前门槛判定。",
            "disabled": "功能已关闭",
            "disabledHint": "等级判定与公开接口不会生效（前台返回 404）；本页保存仍会成功。",
            "openFeatures": "打开产品开关"
          },
          "state": {
            "recommended": "当前为推荐默认门槛",
            "recommendedHint": "四档门槛与推荐默认值逐项一致。",
            "custom": "当前使用自定义门槛",
            "customHint": "至少一档的阈值与推荐默认值不同；可在「门槛设置」里逐项核对，或恢复推荐默认。"
          },
          "ladder": {
            "title": "五档阶梯",
            "metricItem": "{metric} ≥ {value}",
            "metricsSeparator": "、",
            "noThresholds": "本档不设门槛：六项阈值均为 0。",
            "earnable": "新增解锁 {count} 项"
          },
          "guide": {
            "title": "从这里开始",
            "step1": "先在「门槛设置」核对四档阈值；不确定就保留推荐默认。",
            "step2": "再在「能力解锁」勾选每档新增解锁的权限键；不勾表示能力仍由用户组静态授予。",
            "step3": "最后在页面底部保存一次，两处改动会同时提交。"
          },
          "jump": { "thresholds": "去设置门槛", "earnable": "去配置能力解锁" },
          "limitsNote": "分档的发帖/评论冷却与每日上限当前不在此页编辑；保存时会原样保留已存的限额配置。"
        },
        "earnable": {
          "featureOff": "功能关闭时读不到可勾选的权限键，能力解锁暂不可编辑；开关打开后会自动加载。"
        }
      }
    }
  }
}
```

### 10.2 尚缺的新增键（en-US）

```json
{
  "admin": {
    "forum": {
      "trustLevels": {
        "page": {
          "loading": "Loading trust level configuration…",
          "loadFailedDescription": "Page data did not load, so the form below is read-only. Retry; if it keeps failing, check the API service and your account permissions.",
          "retry": "Retry",
          "loadFailedReason": "Reason: {reason}",
          "forbidden": {
            "title": "This account cannot view trust levels",
            "description": "Trust levels require the trust-level management permission to view and change. Ask a site administrator to grant it to your user group."
          },
          "unsaved": "Unsaved trust level changes"
        },
        "saveScope": "Saving submits every change from both Thresholds and Earnable abilities.",
        "reset": "Reset trust level changes",
        "tabs": { "overview": "Overview" },
        "overview": {
          "title": "Overview",
          "scope": {
            "tl0": "Level 0 (Newcomer) is decided by the Newcomer limits page under site settings; this page only displays it.",
            "levels": "Thresholds for levels 1–4 are decided by Thresholds on this page and apply as soon as you save.",
            "stack": "Trust levels only add abilities; they never remove permissions already granted by user groups."
          },
          "feature": {
            "enabled": "Enabled",
            "enabledHint": "The public trust level endpoint is live and levels follow the current thresholds.",
            "disabled": "Disabled",
            "disabledHint": "Level checks and the public endpoint stay inactive (404) while the flag is off; saving on this page still succeeds.",
            "openFeatures": "Open feature flags"
          },
          "state": {
            "recommended": "Using recommended thresholds",
            "recommendedHint": "All four level thresholds match the recommended defaults item by item.",
            "custom": "Using custom thresholds",
            "customHint": "At least one threshold differs from the recommended defaults. Review them in Thresholds, or restore the recommended defaults."
          },
          "ladder": {
            "title": "The five levels",
            "metricItem": "{metric} ≥ {value}",
            "metricsSeparator": ", ",
            "noThresholds": "No thresholds on this level: all six values are 0.",
            "earnable": "New unlocks: {count}"
          },
          "guide": {
            "title": "Start here",
            "step1": "Check the four thresholds under Thresholds; keep the recommended defaults if unsure.",
            "step2": "Then pick each level's new unlocks under Earnable abilities. Leaving them unchecked keeps abilities granted by user groups.",
            "step3": "Finally save once at the bottom of the page; both sections are submitted together."
          },
          "jump": { "thresholds": "Go to Thresholds", "earnable": "Go to Earnable abilities" },
          "limitsNote": "Per-level posting and comment cooldowns and daily caps are not edited on this page yet; saving keeps the stored limit values as they are."
        },
        "earnable": {
          "featureOff": "The permission key list cannot be read while the feature is off, so earnable abilities are not editable here; it loads automatically once the flag is on."
        }
      }
    }
  }
}
```

### 10.3 已迁移的 45 条内容键（已落地，键名逐字不变）

前缀 `admin.settings.trustLevels.` → `admin.forum.trustLevels.`，文本原样搬运。逐条清单：

```
title  description  tl0Hint  restoreDefaults  restoreHint  zeroUnlimited  resetChanges
disabled.title  disabled.description
level.newcomer  level.member  level.resident  level.veteran  level.leader
metric.topicCount  metric.commentCount  metric.likeReceived
metric.likeGiven  metric.bookmarkCount  metric.daysVisited
earnable.title  earnable.note  earnable.count  earnable.empty  earnable.emptyGated
earnable.loading  earnable.aria.cell  earnable.aria.cumulative
earnable.readOnly.title  earnable.readOnly.description
earnable.loadFailed.title  earnable.loadFailed.description  earnable.loadFailed.reason
earnable.loadFailed.retry  earnable.loadFailed.locked
earnable.orphan.title  earnable.orphan.description
earnable.baseline.title  earnable.baseline.text
earnable.baseline.titleGated  earnable.baseline.textGated
earnable.baseline.keyBadge  earnable.baseline.gateTitle  earnable.baseline.gateAria
earnable.baseline.keysSeparator
```

**只允许改文本的三条（键名不变）**，理由都是「页面搬了 / 作用域变了，旧话不再成立」：

| 键 | 现在 | 定稿（zh / en） | 理由 |
| --- | --- | --- | --- |
| `tabs.thresholds` | 阈值 / Thresholds | **门槛设置** / Thresholds | 与 `overview.jump.thresholds`（去设置门槛）、`overview.scope.levels`（「门槛设置」）同名同义；「阈值」是术语，「门槛设置」告诉小白这个 tab 能做什么 |
| `disabled.description` | …要启用请先在站点设置中打开信任等级开关。 | …要启用请在「产品开关」里打开信任等级。/ …Turn trust levels on under Feature flags to enable it. | 开关实际在 `/control-panel/settings/features`（`admin.features.title` =「产品开关」，`adminModules.ts:148-153`）；「站点设置」已不再承载该开关 |
| `resetChanges` | 已重置信任等级阈值更改 / Trust level threshold changes were reset | 已重置信任等级更改 / Trust level changes reset. | 现在一并重置 `earnable`（§6 规则 5），旧文案会让人以为只重置了阈值 |

### 10.4 文案纪律（禁写清单）

| 禁止 | 原因 | 合法替代 |
| --- | --- | --- |
| 「已达最高等级」 | 那是资料页进度条在 `L == 4` 的分支文案（视觉规范 §1.4）。本页阶梯是**配置视图**，不是某个用户的进度；照搬即把「配置」说成「用户状态」，且是未证实推断 | 阶梯 TL4 行只写门槛摘要与解锁项，不写任何「已达/已满」 |
| 「系统会自动帮你升级/降级」式承诺 | 判定发生在保存后的重新计算，结果取决于用户行为 | 只保留既有表述「保存后即时生效」 |
| 「勾选即生效」 | 勾选只是草稿，必须保存 | `saveScope` + 引导 step3 |
| 「恢复默认会重置限额」 | 代码只重置 `requirements` | 迁移键 `restoreHint` 原文已正确，照搬 |
| 「TL0 可在本页调整」 | `config.go` 拒绝 TL0 出现在 `trust.levels` | `tl0Hint` + 阶梯 TL0 行只读说明 |
| 「只读查看」作为本页形态 | D-B：无 `trust.manage` 即 `forbidden`，本页没有只读形态 | 无权限态文案指向「申请权限」，不说「你可以看但不能改」 |
| 自造指标名（「活跃度」「贡献值」） | 六项 metric 是契约 | 只用 `admin.forum.trustLevels.metric.*` 六项 |
| 自造档位名（「初级/中级/高级」） | D21 终稿：新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖 | 只用 `admin.forum.trustLevels.level.*` 五档 |
| emoji 当图标或状态标记 | AGENTS.md 前端 UI 约定 | `UIcon`（Tabler / Lucide） |
| 把 `null` 与 `0` 混说 | D27：`null` = 继承站点默认，`0` = 免冷却/不限 | 本页对 `limits` 只说「不在此页编辑，保存时原样保留」 |

---

## 11. 与其他冻结件的关系（别重复实现）

| 既有契约 | 本页怎么用 |
| --- | --- |
| `reports/2026-11-10-trust-level-visual-spec.md` §0 后台几何基线 | 页头（`text-xl` + 图标）、`UDashboardToolbar`、`SFAdminFixedTabNav`、单活动面板，四项逐条复用 |
| 同上 §2.1 五档图标白名单 + §2.2 环线/填充阶梯 | 阶梯与两处 fieldset legend 的图标阶梯，CSS 沿用 `SFAdminTrustLevelThresholdsTab.vue` / 矩阵组件既有规则 |
| `reports/2026-11-11-trust-level-earnable-spec.md` §2 矩阵几何 / §3 基线披露 / §5 aria | earnable 面板整体继承，不重画 |
| `modules/trust-levels.md` D12 | 唯一披露点 = thresholds 的常驻 `UAlert`（§4 第 5 条） |
| 同上 D21 | 五档名只用终稿五词 |
| 同上 D27 / 决策 D-D | 本页不编辑 `limits`，保存时原样带回；文案不把 `null` 说成 `0` |
| 决策 D-A / D-B / D-C | 独立页 + 三 tab、门控 `trust.manage`、整页一次保存 —— 本文只是把这三条落成几何与文案 |

---

## 12. 验收（reviewer 可直接执行）

> ⚠ **§15.8 追加 8 条断言（24–31）**（2026-11-11）：卡头三件套、推荐 alert 的位置与分工、行式清单的 `divide-y`、退役键归零、卡头 `h3` 唯一。本节原 1–23 条继续有效（除第 7 条「卡片没有 `#header` 槽」已被 §15.1 取代）。

**结构**

1. `apps/web/app/pages/admin/forum/trust-levels.vue` 存在，含 `data-sf-trust-levels-page` 与 `data-sf-page-state`。
2. 三个面板文件存在于 `components/admin/forum/trust-levels/tabs/`，且**没有任何一个**渲染 `UDashboardToolbar` 或 `role="tablist"`。
3. 全仓库 `SFAdminTrustLevelEarnableMatrix` 只有一份实现。
4. 页面只有一个 `h2`；`pages/admin/settings/index.vue` 不再出现 `trustLevels`。

**几何**

5. `tabIds` = `['overview','thresholds','earnable']`，缺省 `overview`；三个 tab 标签为 等级总览 / 门槛设置 / 能力解锁。
6. thresholds 面板保留 `sf-trust-threshold-row` 与 `data-readonly`，`@media (min-width:768px)` 与 `(min-width:1280px)` 两档仍在，末项跨列规则仍在。
7. ~~卡片没有 `#header` 槽~~ → **本版取代（§15.1）**：每个分区卡片**必须**有 `#header`（`h3` + `p.mt-1.text-xs.text-muted` + `UBadge font-mono trust.levels`）。仍然有效的一半：`zeroUnlimited` 与 D12 恢复行都在 thresholds 面板内（不在页面级）。

**状态**

8. 五个 `data-sf-page-state` 取值都能在源码里找到。
9. `forbidden` 分支不渲染 tablist；`failed` 分支有 `data-sf-trust-levels-retry`。
10. `feature-off` 下 earnable 面板渲染 `data-sf-trust-levels-earnable-feature-off` 且**不**渲染 `data-sf-earnable-region`。
11. `!can('trust.manage')` 时页面态必为 `forbidden`，且页面上不存在任何阈值输入框。

**钩子**

12. §9 表格里每个钩子在源码中命中；`data-sf-trust-level-state` 有 `recommended` / `custom` 两支。
12b. 页级 footer 的三个钩子都在 `pages/admin/forum/trust-levels.vue` 自己的模板里：`data-sf-trust-levels-footer`（包裹层）、`-reset`（重置按钮）、`-save`（保存按钮本身，不是容器）。`loading` / `failed` / `forbidden` 下这三个钩子**均不存在**。
13. 阶梯 5 个 `li` 的 `data-level` 覆盖 `"0"`–`"4"`，且都带 `data-metrics`。
14. 矩阵既有 13 个 `data-sf-earnable-*` 钩子一个不少。

**文案**

15. `grep -c "admin.settings.trustLevels" apps/web` = 0；`admin.settings.tabs.trustLevels` 已删。
16. `admin.forum.trustLevels.*` 在 zh-CN / en-US **键集合相等**（无单边键）。
17. 全仓库 `grep -rn "已达最高等级" apps/web/app/components/admin` = 0。
18. 档位名只有 D21 五词；指标名只有 `metric.*` 六项。

**视觉（浏览器证据，1633×810 与 390×844 各一遍）**

19. 页头 / 工具栏 / tab 条左边缘对齐，无第二个页头；切 tab 后仅一个活动面板。
20. 1633 下 thresholds 6 列一行、earnable 为表头 + 裸勾选框；390 下两者均 2 列且末项满宽。
21. 390 下无横向滚动（`document.scrollingElement.scrollWidth === clientWidth`）；工具栏说明截断不撑破。
22. footer 吸底常驻；`hasChanges` 时左侧出现未保存块（含 `saveScope` 那句）且保存可点；无改动时保存禁用。
23. overview 配置状态徽章：推荐默认数据下 `recommended`；改一档阈值并保存后 `custom`；**只**改 `earnable` 勾选时保持 `recommended`。

**实测校正（2026-11-11 收尾，三条别读成缺陷）**

1. 页文件 `apps/web/app/pages/admin/forum/trust-levels.vue` 实测 **315 行**（含 `<style scoped>` 里占位块那段）。行数是会动的数字，复算方式是 `wc -l apps/web/app/pages/admin/forum/trust-levels.vue`，本文不承诺它在别的时点仍是 315。
2. **硬加载时浏览器网络面板里没有 `GET /api/v1/admin/web-options`，不是缺陷**：硬加载（直接输入 URL / 刷新）由 **Nitro 服务端**发这条请求，浏览器只收到渲染结果。等价证据是 SSR HTML 的 `#__NUXT_DATA__` 里含 `admin-web-options` 与 `trust.levels`；从别的后台页客户端路由进入时，网络面板可见 1 条。断言取数请用后者或 SSR 载荷，**不要用「面板为空」反推没取数**。
3. `loading` 态**卡片在、footer 不在**（`showCard` 含 `loading`，`showFooter` 不含）。这一条的唯一出处是 §6 规则 1，§2 表格与它同源；若两处不一致，按 §6 改 §2，不许反过来。

---

## 13. Δ 汇总（相对工作区现状要改的，给 frontend 一张单）

> **2026-11-11 收尾复核：本表 11 条均已落地**（含 §14 的 P1/P3 复验）。下表保留为「改了什么」的记录与依据，**不再当作待办清单**；把它读成待办会让人把已实现的东西当成漂移。
>
> **2026-11-11 视觉对齐追加**：本表之后的改动不再列在这里，统一收进 §15（含 §15.0 的取代关系表）。第 6 条「新建 overview 面板」仍在进行中——面板文件已存在，但行式清单与卡头要按 §15.1 / §15.3 改。

| # | 位置 | 改动 | 依据 |
| --- | --- | --- | --- |
| 1 | `pages/admin/forum/trust-levels.vue` | `tabIds` 加 `overview` 并置首、缺省 tab 改 `overview`、图标 `i-lucide-list` | §1.2 |
| 2 | 同上 | 新增 `data-sf-trust-levels-page` / `data-sf-page-state` / `-toolbar` / `-refresh` / `-tabs` / `-panel`+`data-tab` | §9.1 |
| 3 | 同上 | 新增 `forbidden` 与 `loading` 两个分支（`data-sf-trust-levels-forbidden` / `-loading`）；`failed` 分支补描述与 `data-sf-trust-levels-retry` | §2 |
| 4 | 同上 | footer 改：`:disabled="!canManage \|\| !hasChanges"`、`submit-text` → `admin.common.save`、`reset-text` → `admin.forum.trustLevels.reset`、`#left` 换成本页未保存块（`-unsaved`） | §6 |
| 5 | 同上 | D12 恢复行与 `zeroUnlimited` 行移入 thresholds 面板（页面级只留 `<component>`） | §4 / §9.3 |
| 6 | 新建 `tabs/SFAdminTrustLevelsOverviewTab.vue` | 整块新写（§3），含 14 个 overview 钩子 | §3 / §9.2 |
| 7 | `tabs/SFAdminTrustLevelThresholdsTab.vue` | 加 `data-sf-trust-levels-thresholds` / `-restore-hint` / `-restore` / `-zero-unlimited` | §9.3 |
| 8 | `tabs/SFAdminTrustLevelEarnableMatrix.vue` | 包一层 `data-sf-trust-levels-earnable` 面板容器 + `feature-off` 锁定块（`-earnable-feature-off`） | §5 / §9.4 |
| 9 | `useTrustLevelsAdminPage.ts` | 导出 `pageState` 与 `loadErrorReason`（判定式见 §2.1），并暴露 `hasLoaded` | §2.1 |
| 10 | i18n zh-CN / en-US | 补 §10.1 / §10.2 全部新键；改 §10.3 三条文本（`tabs.thresholds` / `disabled.description` / `resetChanges`） | §10 |
| 11 | `apps/web/tests/admin/settings/trustLevels.test.ts` | 该文件仍断言设置页含 `trustLevels` tab（`:65`）与 `locale.admin.settings.tabs.trustLevels`（`:261`），迁移后必红：按新页重写或拆到 `tests/admin/forum/trustLevels*.test.ts`；**不许**靠放宽 `toContain` 绕过 | §0.3；两处断言已实测 |

---

## 14. 实测证据 2026-11-11（reviewer 小审）

环境：dev server 由用户持有（`127.0.0.1:3000`，API `:8080`），登录会话为 `zhuchunshu`（`super_admin` + 信任等级 `member`）。浏览器驱动走 CDP（`storage/qa/trust-levels/cdp.mjs` + `qa.mjs` + `measure.mjs`），视口 1633×810（桌面）/ 390×844@3（移动）。截图与原始测量 JSON 在 `storage/qa/trust-levels/`（gitignored）。

### 14.1 通过项

| # | 断言（§12 编号） | 实测 | 证据 |
| --- | --- | --- | --- |
| 1 | 页头 / toolbar / tab 条左边缘同轴（19） | 桌面 `280 / 280 / 280`，移动 `16 / 16 / 16`；面板左 304 / 32 = 卡片内边距，非错位 | `stage-responsive.json`，`desktop-overview.png`、`mobile-overview.png` |
| 2 | 只有一个页头、一个 tab 条、一个活动面板（19） | 三 tab 均 `panelTab` 与 `?tab=` 一致，切 tab 后只有一个 `[data-sf-trust-levels-panel]` | `stage-responsive.json` |
| 3 | 桌面 thresholds 6 列 × 5 行一屏（20） | 五个 fieldset 全部 `grid-template-columns: 6 列`、每档 1 行（`0..4 → 6c/1r`） | `desktop-thresholds.png`，`grids` 数组 |
| 4 | 移动 thresholds 2 列且末项满宽（20） | 五档全部 `2c/4r`、`lastSpansFullRow: true` | `mobile-thresholds.png` |
| 5 | 移动矩阵两列且格内名可读（20） | 矩阵 `grid-template-columns` = 2 列，格宽 159px；勾选框 25、门控标记 10、计数 `新增项 0/20` | `mobile-earnable.png`，`matrix` 对象 |
| 6 | 390 无页面级横向滚动（21） | `document.documentElement.scrollWidth = 390 = innerWidth`，三 tab 均 false | `stage-responsive.json` → `horizontalOverflow` |
| 7 | footer 吸底且不压最后一行（22） | 滚到底：`footerTop - lastContentBottom = 42px`（桌面）/ `34px`（移动），footer 底边始终在滚动容器内 | `sticky.atBottom`（桌面与移动三 tab 全绿） |
| 8 | footer 按钮在 390 不被裁切（22） | 保存按钮 `right = 358 ≤ 390`、重置 `right = 272`；`clippedControls` 中不含 footer 按钮 | `mobile-*.png`，`footer.saveBox` |
| 9 | `hasChanges` 时出现未保存块、保存可点（22） | 改 TL1 `days_visited 3 → 4`：`[data-sf-trust-levels-unsaved]` 出现、`save` 从 disabled 变可点；保存后自动消失 | `stage-save.json` → `dirty-state` / `after-restore` |
| 10 | overview 五档阶梯 + 推荐默认徽章（23） | `ladder` 5 行 TL0→TL4（`data-metrics` 依次 0/2/5/6/6，TL0 显示 tl0Hint）；`data-sf-trust-level-state="recommended"`，文案「当前为推荐默认门槛」 | `desktop-overview.png`，`ladder` / `stateBadge` |
| 11 | 只改 `earnable` 不改徽章 | 单测侧已钉（`trustLevelsOverview.test.ts`「ignores limits and earnable」）；浏览器侧本轮未单独复测 | `apps/web/tests/admin/forum/trustLevelsOverview.test.ts` |
| 12 | 兼容跳转：`?tab=trustLevels` → 新页 | `GET /control-panel/settings?tab=trustLevels` 最终落 `/control-panel/forum/trust-levels` | `stage-compat.json`、`desktop-legacy-redirect-loading.png` |
| 13 | 站点设置 tab 条已无信任等级 | settings 页 tab 标签：基础设置 / 账号安全 / 注册与用户名 / 新人限制 / 维护模式 / 客户端 / 验证码 / 版本更新（无「信任等级」） | `desktop-settings-tabs.png` |
| 14 | 侧栏「论坛管理」下有入口 | 展开后 `a[href="/control-panel/forum/trust-levels"]` 存在（left 48, top 378），点击即客户端路由进页 | `stage-responsive.json` → `sidebar-entry` |
| 15 | **P2 回归：一次保存 = 一次写入** | 改一个阈值点一次保存：`PUT /api/v1/admin/web-options` **恰好 1 条**、Toast **恰好 1 条**（「站点设置已保存。」）；复原保存同样 1 条 | `stage-save.json` → `first-save` / `restore-save`、`desktop-save-toast.png` |
| 16 | 功能关闭态：一条 warning、无矩阵、零白名单请求 | `pageState="feature-off"`；`[data-sf-trust-levels-disabled]` 恰 1 个；earnable tab 显示 `-earnable-feature-off` 占位、`[role="checkbox"]` = 0；**`/api/v1/admin/trust-levels` 请求数 = 0**（进入后与切 tab 后均为 0） | `stage-switch-verify.json`、`desktop-feature-off-overview.png`、`desktop-feature-off-earnable.png` |
| 17 | 开关已恢复 | `web_options.features.trust_levels = enabled`（psql 实测），公开读面同一值；恢复后新页 `pageState="ready"`、`featureState="on"`、白名单请求恢复 | `desktop-restored-page.png`、下文 §14.4 |

### 14.2 不通过项

**P1｜硬加载（直接访问 / 刷新 / 旧链接跳转）后页面永久停在 `loading`，且不发 `/admin/web-options`**

- 现象：SSR 首帧完整渲染（t≈1.5s 时 `data-sf-page-state="ready"`），水合完成后变为 `"loading"` 并**永久停留**——三个 tab 消失，只剩虚线「加载中」占位块。
- 网络：硬加载期间只有 `GET /api/v1/admin/admin-surfaces`、`/api/v1/web-options`、`/api/v1/auth/session`、`/api/v1/admin/trust-levels`；**没有 `GET /api/v1/admin/web-options`**。
- 最小复现：新标签打开 `http://127.0.0.1:3000/control-panel/forum/trust-levels`（或在该页按 F5）→ 观察 `[data-sf-trust-levels-page]` 的 `data-sf-page-state` 由 `ready` 变 `loading` 且 `[data-sf-trust-levels-loading]` 常驻。客户端路由（侧栏点击）进入则正常 `ready`。
- 影响面：书签、刷新、以及「旧链接 `?tab=trustLevels`」这条兼容路径全部命中（`desktop-legacy-redirect-loading.png` 即该路径截图）。
- 推断根因（需 frontend 确认）：`useTrustLevelsAdminPage` 的 `hasLoaded` 是本地 `ref(false)`，只在 `useAsyncData('admin-web-options')` 的 handler 内置 `true`；硬加载时该 handler 在 SSR 跑过、客户端水合复用 payload 不重跑，于是 `hasLoaded` 恒 `false`，`pageState` 命中 `if (!hasLoaded.value) return 'loading'`。客户端路由进入时 handler 真跑，所以正常。
- 一条可行修法（供参考，不代表唯一解）：`watch(items, value => { if (value) hasLoaded.value = true }, { immediate: true })`，或改用 `useAsyncData` 的 `status`/`pending` 判 loading。

### 14.3 其他发现（P3，不影响验收通过）

1. **390 下 toolbar 的「刷新」按钮被挤出视口**：`[data-sf-trust-levels-toolbar]` 内层 `overflow-x: auto`，左槽宽度 640px（`min-width: auto`，说明文字不收缩），右槽被推到 `left 679 / right 739`（innerWidth 390），需要横向滚动工具栏才能点到；该按钮无 `aria-label`/`title`。证据：`debug-toolbar.mjs` 输出（1633 下为 `left 1500 / right 1574`，正常）。
2. **进入页面时白名单请求发两次**：`onMounted(maybeLoadWhitelist)` 与 `watch(items, maybeLoadWhitelist)` 都会触发，实测 `GET /api/v1/admin/trust-levels` ×2（`stage-save.json`、恢复态 `stage-switch-verify.json`）。功能无碍，只是重复往返。
3. **关闭态警告没有 `role="alert"`/`aria-live`**：`document.querySelectorAll('[role="alert"]')` 在 `feature-off` 页为 0（警告靠 `data-sf-trust-levels-disabled` 钩子才数得到 1 条）。屏幕阅读器不会主动播报；与本站其他页 UAlert 行为一致，属共享组件层面的既有问题，记录备查。
4. **overview 的「功能已关闭」是状态徽章不是第二条 warning**：`feature-off` 页上「warning 只有一条」成立（页面级 alert 1 条）；overview 顶部另有一枚 `data-sf-trust-levels-feature-state="off"` 徽章 + 一行说明 + 「打开产品开关」入口，按 §3 属状态条，不计为重复告警。

### 14.4 状态恢复声明

- `features.trust_levels`：本轮为验证关闭态曾置 `disabled`，**已恢复 `enabled`**（`psql` 直查 `web_options` 与公开读面 `/api/v1/web-options` 双向确认，`stage-switch-verify.json` 复核页 `featureState.state = "on"`）。
- `trust.levels`：P2 回归用「TL1 `days_visited` 3 → 4 → 3」两次保存，落库复核为**推荐默认原值**（member 3/0/5/0/0/0，resident 15/2/20/5/3/0，veteran 45/8/80/30/20/5，leader 100/25/250/100/80/20），`earnable` 全为 `[]`、`limits` 四字段全 `null`；无残留。
- 其他 `features.*` 未改动（中途有一次误点 `features.attachments` 开关，但那次保存前后同值、落库复核仍 `enabled`）。

### 14.5 未验证（转入 handoff 未验证清单）

- **无 `trust.manage` 账号直连新页的 `forbidden` 态**：dev 库只有两类账号——`zhuchunshu`（`super_admin`，持有 `trust.manage`）与 20+ `seed_*`（仅 `member`，随机密码、无法登录，且无 `admin.access`）。**未取得满足条件的账号，未验证**。该项目前只有单测覆盖（`trustLevels.test.ts` 对 `pageState` 判定式的静态断言、`trustLevelsOverview.test.ts` 对 shell `forbidden` 分支的断言），没有浏览器实测。
- 移动端 390 的 `ready` 态是**同会话切换视口**测得的（先桌面进页再切 390），原因正是 §14.2 的硬加载回归会遮住整页——等 P1 修掉后应补一轮 390 硬加载实测。

### 14.6 P1 / P3 修复后复验（2026-11-11 第二轮）

前提：`hasLoaded` 改为从 `useAsyncData` 的 `data` 派生、`pageState` 改为「任何 error 都落 failed」、`maybeLoadWhitelist` 加 `status === 'loading'` 同轮去重、工具栏左右槽加 `min-w-0 flex-1` / `shrink-0`、刷新按钮补 `aria-label`/`title`。测试侧同步改写两条断言（见 §14.7）。

**A. 硬加载三条路径（桌面 1633×810）——通过**

| 路径 | pageState（10 次采样） | `GET /api/v1/admin/trust-levels` | 截图 |
| --- | --- | --- | --- |
| 直接访问 `/control-panel/forum/trust-levels` | `ready` ×10，稳定 | **1** | `desktop-hardload-direct.png` |
| F5（`Page.reload` + ignoreCache） | `ready` ×10，稳定 | **1** | `desktop-hardload-reload.png` |
| 旧链接 `/control-panel/settings?tab=trustLevels` | `ready` ×10，稳定，最终落 `/control-panel/forum/trust-levels` | **1** | `desktop-hardload-legacy-redirect.png` |

- 确定性：三条 tab URL × 5 轮独立标签页硬加载 = **15/15 全部 `ready`、`/admin/trust-levels` 恰 1 次**（`whitelist-request-counts.json`）。P3-b 修复前同场景为 2 次，现为 1 次。
- `GET /api/v1/admin/web-options`：硬加载时该请求由 Nitro **服务端**发出，浏览器网络面板按设计看不到；等价证据是 SSR HTML 的 `#__NUXT_DATA__` 已含 `admin-web-options` 与 `trust.levels`，且 SSR 首帧 `data-sf-page-state="ready"`（`ssr-proof.json`，curl + 真实会话 cookie）。客户端路由（侧栏点击）进入时浏览器网络可见 **`GET /api/v1/admin/web-options` ×1**（`debug-clientnav.mjs` 输出）。

**B. 390×844 硬加载——通过**

- 启动即 `ready`（8/8 采样稳定），`/admin/trust-levels` 恰 1 次。
- 工具栏：`innerScrollWidth 356 == innerClientWidth 356` → **无内层横向滚动**；刷新按钮 `left 283 / right 357 ≤ 390`、`aria-label="刷新"`、`title="刷新"`（P3-a 修复确认）。
- 三 tab：`scrollWidth 390 == innerWidth 390` 无横向溢出；tab 条 1 行；`alerts` 空。
- thresholds：五档全 `2 列 / 4 行 / 末项满宽`；earnable：矩阵 2 列、25 勾选框、计数 `新增项 0/20`；footer 保存按钮 `right 358 ≤ 390`；吸底 `gap 34px`。
- 截图：`mobile-hardload-overview.png` / `mobile-hardload-thresholds.png` / `mobile-hardload-earnable.png`。

**C. 桌面三张重拍（P1 修复后，替换 §14.1 的旧图）——通过**

| tab | pageState | 轴对齐（页头/toolbar/tab） | 关键几何 | 截图 |
| --- | --- | --- | --- | --- |
| overview | `ready` | 280 / 280 / 280 | 五档阶梯 5 行、徽章 `recommended`、吸底 gap 42、裁切 0、无溢出 | `desktop-hardload-tab-overview.png` |
| thresholds | `ready` | 280 / 280 / 280 | 五档全 `6 列 / 1 行`、吸底 gap 42 | `desktop-hardload-tab-thresholds.png` |
| earnable | `ready` | 280 / 280 / 280 | 矩阵 7 列、25 勾选框、10 门控、`新增项 0/20`、吸底 gap 42 | `desktop-hardload-tab-earnable.png` |

**D. 遗留（不影响本次验收）**

- 关闭态警告仍无 `role="alert"`/`aria-live`（§14.3-3 未变；矩阵加载态有 `aria-live="polite"`）。属共享 `UAlert` 层面的既有行为，未在本轮修复范围内。
- 同标签页连续导航时统计到的 `/admin/trust-levels` 计数会被上一文档的在途请求污染（`stage-desktop-reshoot.json` 里出现 0/2/1）。以独立标签页的 15/15 采样为准。

### 14.7 测试断言随新契约改写（不删、不放宽）

1. `trustLevels.test.ts` 五态断言：`if (error.value && !hasLoaded.value) return 'failed'` → `if (error.value) return 'failed'`；新增 `not.toContain('&& !hasLoaded')` 与「failed 分支源码位置早于 loading 分支」（等价于「hasLoaded 为真且 error 非空 ⇒ failed」；编排层依赖 Nuxt 自动导入无法在 bun 挂载，故用分支顺序钉语义）。
2. 同处新增 `hasLoaded` 派生契约：`computed(() => items.value !== null && items.value !== undefined)`，并断言不存在 `const hasLoaded = ref(` 与 `hasLoaded.value = true`（正是 P1 的成因）。
3. `maybeLoadWhitelist` 断言拆三条：开关判据 `/if \(!shouldLoadWhitelist\(trustLevelsEnabled\.value\)\) return/`、去重判据 `/if \(whitelistState\.value\.status === 'loading'\) return/`、`void loadWhitelist()` 计数仍为 2；另加「去重判据必须住在 `maybeLoadWhitelist` 体内」与 `not.toMatch(/status !== 'idle'/)`（否则保存后刷新会被误挡）。

---

## 15. 视觉对齐「论坛设置」页（2026-11-11 追加，**本版取代** §0.4 / §1.1 / §3 的部分结论）

触发：用户明确要求本页按 `/control-panel/forum/settings` 的设计语言重做视觉。
基准不是新发明 —— 是既有那套骨架：`components/admin/settings/forum/SFAdminForumTabFrame.vue`（卡片 header + 吸底 footer）
与 `pages/admin/forum/settings.vue`（页头 + 工具栏 + 推荐配置 alert + 固定 tab 条）。

### 15.0 取代关系（先看这张表，别把 §0.4 当现行）

| 旧结论 | 位置 | 本版 |
| --- | --- | --- |
| 「卡片不设 header」，分区标题由面板自己承担 | §0.4 | **取代**：每个分区一张 `UCard`，header = `h3` + `p.mt-1.text-xs.text-muted` + 右侧 `UBadge font-mono trust.levels`（§15.1） |
| 面板体 `<section class="space-y-5">` | §1.1 / §3 | **修订**：卡片体统一 `div.grid.max-w-5xl.gap-6`（§15.4） |
| overview 用 `ol` 阶梯 | §3 | **取代**：行式清单 `ul.divide-y`（§15.3） |
| 工具栏下无推荐配置 alert | §1.1 | **新增**：`UAlert color=primary variant=soft icon=i-lucide-sparkles`（§15.2） |
| 「推荐默认」只由 overview 徽章承担 | §3.3 | **修订**：alert 说「推荐默认**是什么**」，徽章说「当前**是不是**推荐默认」（§15.2） |
| `description` / `overview.description` 承担分区说明 | §10.1 | **修订**：分区说明统一收进 `sections.<tab>.description`，旧两键退役（§15.5 / §15.6） |

**不变的部分**（§15 一个字都不改它们）：§2 五态、§4 阈值 5 行 fieldset 与 6/3/2 断点、§5 矩阵与 feature-off 兜底、
§6 页级唯一吸底 footer、§7 断点表、§8 无障碍、§9 既有钩子、§10 其余文案、§14 实测证据。
§15 只做三件事：加卡头、加推荐 alert、把阶梯改行式清单。

### 15.1 分区 UCard 的 header（含「为什么不算第二个页头」）

```
UCard（route shell 持有；每个 tab 一张，三个面板互斥）
├─ #header → div.flex.items-center.justify-between.gap-3 [data-sf-trust-levels-card-header]
│  ├─ div.min-w-0
│  │  ├─ h3#sf-trust-levels-panel-title.text-base.font-bold        → 见下表
│  │  └─ p.mt-1.text-xs.text-muted                                 → 见下表
│  └─ UBadge[color=neutral][variant=soft].font-mono                → 字面量 "trust.levels"
├─ body → div.grid.max-w-5xl.gap-6                                  → §15.4
└─ #footer [v-if="showFooter"]                                      → §6（不变）
```

| activeTab | `h3#sf-trust-levels-panel-title` | `p.mt-1.text-xs.text-muted` |
| --- | --- | --- |
| `overview` | `admin.forum.trustLevels.overview.title`（等级总览） | `admin.forum.trustLevels.sections.overview.description` |
| `thresholds` | `admin.forum.trustLevels.title`（信任等级阈值） | `admin.forum.trustLevels.sections.thresholds.description` |
| `earnable` | `admin.forum.trustLevels.earnable.title`（能力解锁（按档新增）） | `admin.forum.trustLevels.sections.earnable.description` |

**它为什么不算「第二个页头」**（这是本版唯一需要论证的地方，写清以免下一轮被当成违规打回）：

1. 页头三件套 —— `h2.text-xl` + `UDashboardToolbar` + `SFAdminFixedTabNav` —— 仍然只在 route shell 出现**各一次**。§0.4 第一条（「面板不许渲染页头、工具栏或 tablist」）**没有放松**：卡头是 `UCard` 的 `#header`，由 route shell 渲染，不是面板渲染的。
2. 层级不同：卡头是 `h3`（分区标题），比页 `h2` 低一级；一个页面一个 `h2` 的规则照旧。
3. 与同域页面逐字同构：`SFAdminForumTabFrame.vue:22-30` 的 header 就是 `div.flex.items-center.justify-between.gap-3` + `h3.text-base.font-bold` + `p.mt-1.text-xs.text-muted` + `UBadge font-mono`。AGENTS.md 的禁令是「tab 组件不得再渲染页头/工具栏/同级 tab 内容」，**不是**「tab 内容不许有标题」——`SFAdminForumTabFrame` 本身就是官方先例，本页只是把同一套骨架用到独立页上。
4. 键前缀徽章是这套语言的固定成分（论坛设置是 `forum.*`，本页是 `trust.levels`）：它告诉运营「这屏写的是哪个 option 键」。字面量进模板、不进 i18n（与 `SFAdminForumTabFrame.vue:28` 同一处理）。
5. **矩阵内的 `h3.sf-trust-earnable__title` 必须删除**：卡头已用 `earnable.title`，同名标题在卡头与卡体各画一次，读屏的「跳转标题」会出现两个同名条目。矩阵根 `section` 的 `aria-labelledby` 改指 `#sf-trust-levels-panel-title`。这是 §5 唯一被本版改到的一处。

### 15.2 工具栏下的推荐配置 alert

位置（与 `pages/admin/forum/settings.vue:120-121` 同槽位）：`UDashboardToolbar` 之后、tablist 之前，与错误条 / 无权限条 / 关闭警告条**同一条竖列**，顺序固定为：

```
错误条(failed) → 无权限条(forbidden) → 关闭警告条(feature-off) → 推荐配置(ready|feature-off) → tablist
```

形状（逐字对齐论坛设置，不新造配色）：

```
UAlert[color=primary][variant=soft][icon=i-lucide-sparkles]
[:title="t('admin.forum.trustLevels.recommended.title')"]
[:description="t('admin.forum.trustLevels.recommended.description')"]
[data-sf-trust-levels-recommended]
```

渲染条件：`ready` 与 `feature-off` 渲染（有卡片的分区上下文）；`forbidden` / `failed` / `loading` 不渲染 —— 数据没到或没权限时先说要紧的那件事，推荐值不是当下的决策依据。

**与 overview 配置状态徽章的分工（两者不许互相复述）**：

| 通道 | 回答的问题 | 数据源 | 随编辑/保存变化 |
| --- | --- | --- | --- |
| 推荐配置 `UAlert` | 推荐默认**是什么** | 静态文案（数值与 `RECOMMENDED` 同源，见 §15.5 注） | 不变 |
| `data-sf-trust-level-state` 徽章 | 当前**是不是**推荐默认 | 已保存 `requirements` 与 `RECOMMENDED` 逐项比较（§3.3） | 保存后变 |

推论（写成禁令，便于 grep）：

- alert 文案**禁止**出现「当前」「已使用」「未修改」「与推荐一致」这类现值判断 —— 那是徽章的话。
- 徽章文案**禁止**复述推荐值（不写「推荐 3 天」）—— 那是 alert 的话。
- 两句同时出现在同一屏时，读者应得到「推荐长这样 / 你现在是这样」两件事，而不是同一句话说两遍。

### 15.3 overview 行式清单（取代 §3 的 `ol` 阶梯）

```
ul.m-0.list-none.divide-y.divide-slate-200.p-0.dark:divide-zinc-800 [data-sf-trust-levels-ladder]
└─ li.py-3.first:pt-0.last:pb-0  [:data-level="0"…"4"] [:data-metrics="<非 0 指标个数>"]
   ├─ div.flex.flex-wrap.items-center.justify-between.gap-x-3.gap-y-2
   │  ├─ div.flex.min-w-0.items-center.gap-2
   │  │  ├─ span.sf-trust-levels-ladder__icon[:data-level][aria-hidden="true"]
   │  │  │  └─ UIcon[name=trustLevelIcon(level)].size-3.5
   │  │  ├─ strong.text-sm.font-semibold → admin.forum.trustLevels.level.<slug>
   │  │  └─ span.text-[11px].font-semibold.tabular-nums.text-muted → profile.trustLevel.code {level}
   │  └─ div.flex.flex-wrap.items-center.gap-2.w-full.sm:w-auto
   │     ├─ span.text-xs.text-muted → 指标摘要（规则见 §3.2，逐条不变）/ 全 0 → overview.ladder.noThresholds
   │     └─ UBadge[v-if="earnableCount(level) > 0"][color=neutral][variant=soft]
   │        [data-sf-trust-levels-earnable-count="<n>"] → overview.ladder.earnable {count}
   └─ p.mt-1.text-xs.text-muted [v-if="level === 0"] → admin.forum.trustLevels.tl0Hint
```

五条规则：

1. **一档一行**，`divide-y` 承担分隔。**不许**再叠 `border-t` / `mt-3.5` / `pt-3.5`：那是 §4 阈值行的分隔语言，同一页出现两套分隔会让密度读错（阈值行是「表单行」，这里是「清单行」）。
2. 每行左侧固定三件：档位图标（24×24 视觉槽，`size-3.5` 图标）+ D21 档位名 + `TL{n}` 编号（tabular-nums）。图标阶梯的环线/填充沿用视觉规范 §2.2，不因行式布局降级。
3. 右侧 = 指标摘要 + 解锁数徽章，`≥640` 与左组同一行两端对齐；`<640`（含 390×844）右侧组整行换行、左对齐（`w-full`），**摘要不截断、徽章跟在摘要后**，两件都不换行到第三行。
4. TL0 行的摘要位置放 `tl0Hint` 那一句（`p.mt-1`），与其它四行的摘要同一行位置，不再单独占一块。
5. 五档固定 5 行，不折叠、不分页（理由同 §7.3）。钩子不变：`data-sf-trust-levels-ladder` / `data-level` / `data-metrics` / `-earnable-count`。

overview 的其余块（功能状态条、配置状态徽章、三条定性说明、三步引导、两个跳转按钮、限额披露）**顺序与钩子全部不变**，只是外框从 `<section class="space-y-5">` 换成卡片体 `div.grid.max-w-5xl.gap-6`。

### 15.4 卡片体统一几何

- 卡片体 = `div.grid.max-w-5xl.gap-6`，与 `SFAdminForumTabFrame.vue:32` 逐字一致：单列 grid 排列分区内所有块，`max-w-5xl`（64rem = 1024px）限制行长，`gap-6` 统一块间距。
- 原 `space-y-5` **删除**：`grid gap-6` 已给间距，两套间距叠用会得到 20 + 24 = 44px 的怪值。
- thresholds 的 5 行 fieldset 与其 6/3/2 列断点**不受影响**：列断点在 fieldset 内部的 grid 上，与卡片体的单列 grid 正交。1633×810 下卡片内宽 1281 > 1024，`max-w-5xl` 生效 → 表单区左对齐、右侧留白（与论坛设置页完全一致）。
- 390×844：`max-w-5xl` 不触发（视口更窄），`gap-6` 保留；页面 `p-4` + 卡片 `p-4` → 内容宽 326（同 §7.1 口径）。

### 15.5 三份新文案定稿（zh-CN / en-US，逐字）

```json
{
  "admin": {
    "forum": {
      "trustLevels": {
        "recommended": {
          "title": "推荐配置",
          "description": "推荐默认是一组按累计行为递增的门槛：TL1 最宽松、TL4 最严格；能力解锁推荐为空（能力仍由用户组授予），分档限额推荐继承站点默认。具体数值与一键恢复都在「门槛设置」。"
        },
        "sections": {
          "overview": { "description": "五档如何判定、当前用的是哪套门槛，以及每档新增解锁了什么。" },
          "thresholds": { "description": "五档阈值按累计行为判定，保存后即时生效。" },
          "earnable": { "description": "勾选某档新增解锁的权限键；未勾选时能力仍由用户组静态授予。" }
        }
      }
    }
  }
}
```

```json
{
  "admin": {
    "forum": {
      "trustLevels": {
        "recommended": {
          "title": "Recommended configuration",
          "description": "The recommended defaults are thresholds that rise with cumulative activity: TL1 is the loosest and TL4 the strictest. Earnable unlocks default to empty (abilities stay with user groups), and per-level limits inherit the site defaults. The exact values and the one-click restore live under Thresholds."
        },
        "sections": {
          "overview": { "description": "How the five levels are decided, which thresholds are in use, and what each level adds." },
          "thresholds": { "description": "Thresholds are evaluated from cumulative activity and apply immediately after saving." },
          "earnable": { "description": "Pick the permission keys each level adds. While nothing is checked, abilities stay granted by user groups." }
        }
      }
    }
  }
}
```

两条与措辞有关的判断，写给后来的人（不同意就改本文，别在实现里悄悄改）：

1. **推荐配置 alert 里不放 24 个数字**。放进去就成了 `RECOMMENDED`（`useTrustLevelsForm.ts:99-104`）的第二个副本，后端一改默认值这句话就开始说谎；而「具体数值」本来就在 thresholds 分区逐格显示，且「恢复推荐默认」按钮也在那儿。所以 alert 只说**结构与倾向**（递增、TL1 最宽松、能力解锁为空、限额继承），数值交给表单。
2. **`sections.thresholds.description` 比迁移来的 `description` 少一句**「阈值为 0 表示该指标不参与该档判定」：同一分区里 `zeroUnlimited` 那行已经逐字说了同一件事，卡头再说一遍是重复。这也是退役 `admin.forum.trustLevels.description` 的原因（§15.6）。

> 协调注：本节五个键的值是设计侧冻结值。若 lead 下发给 frontend 的定稿与此不同，**改本文对齐**（spec 是记录），不要出现「实现改了、spec 没改」的分叉。

### 15.6 i18n 变更清单（本版）

| 动作 | 键 | 说明 |
| --- | --- | --- |
| 新增 | `admin.forum.trustLevels.recommended.title` / `.description` | §15.5 |
| 新增 | `admin.forum.trustLevels.sections.overview.description` | §15.5 |
| 新增 | `admin.forum.trustLevels.sections.thresholds.description` | §15.5 |
| 新增 | `admin.forum.trustLevels.sections.earnable.description` | §15.5 |
| 退役 | `admin.forum.trustLevels.description` | 内容并入 `sections.thresholds.description` 并删去与 `zeroUnlimited` 重复的 0 说明 |
| 退役 | `admin.forum.trustLevels.overview.description` | 内容并入 `sections.overview.description`；分区说明只有一个位置（卡头） |

退役键必须在 zh-CN / en-US 两侧同时删除，且 `grep -rn "trustLevels.overview.description\|trustLevels.description" apps/web` 只剩 `sections.` 形式。

### 15.7 钩子增量

| 钩子 | 位置 | 取值 |
| --- | --- | --- |
| `data-sf-trust-levels-card-header` | 每个分区 `UCard` 的 `#header` 容器 | 恒存在（有卡片的态） |
| `data-sf-trust-levels-recommended` | 推荐配置 `UAlert` | `ready` / `feature-off` |

`h3#sf-trust-levels-panel-title` 的 `id` 是矩阵 `aria-labelledby` 的锚点（§15.1 第 5 条），不是测试钩子，但 id 值冻结。

### 15.8 验收追加（接 §12）

24. 三个分区的 `UCard` 都有 `#header`，且 header 里同时存在 `h3#sf-trust-levels-panel-title`、`p.mt-1.text-xs.text-muted`、`UBadge.font-mono` 且徽章文本为字面量 `trust.levels`。
25. 页面仍只有一个 `h2`；`UDashboardToolbar` 与 `role="tablist"` 各一处（卡头不引入第二套）。
26. 推荐配置 `UAlert` 在 tablist **之前**、关闭警告条**之后**；`forbidden` / `failed` / `loading` 下 `data-sf-trust-levels-recommended` 不存在。
27. 推荐配置 alert 文案不含「当前 / 已使用 / 未修改」；`overview.state.*` 文案不含具体数值。
28. overview 阶梯是 `ul.divide-y` 行式清单，5 个 `li` 均带 `data-level` 与 `data-metrics`，且 `li` 上**没有** `border-t` / `mt-3.5` 类。
29. 卡片体为 `div.grid.max-w-5xl.gap-6`，且全页不存在 `space-y-5` 的卡片体。
30. `grep -rn "trustLevels.description" apps/web` 命中只剩 `sections.*.description`；`overview.description` 归零。
31. 矩阵内 `h3.sf-trust-earnable__title` 已删除，矩阵根 `aria-labelledby="sf-trust-levels-panel-title"`。

### 15.9 本版没做的事（防止被当成遗漏）

- 没有改 §4 阈值行的 6/3/2 列断点、TL0 只读、`blockNonIntegerKey`、D12 常驻 alert。
- 没有改 §6 页级唯一 footer 与「保存按钮无改动时禁用」。
- 没有改 §2 五态判定式、§5 矩阵三条语义、§7 断点表、§8 无障碍。
- 没有给「推荐配置」加一键恢复按钮：恢复默认仍只在 thresholds 分区（它只重置 `requirements`，见 §6 规则 6）。

### 14.8 视觉对齐「论坛设置」· 复验（2026-11-11 第三轮，§15 落地后）

前提：`SFAdminTrustLevelsOverviewTab.vue` 的残留模板已删（362 → 305 行，`</template>` 恰 1 处），`data-sf-trust-levels-ladder` 钩子回到容器 div。五个 SFC `@vue/compiler-sfc` parse 全部 0 errors。截图：`desktop-v15-*.png` / `mobile-v15-*.png`；原始测量：`storage/qa/trust-levels/stage-v15-{desktop,mobile}.json`。

**A. 桌面 1633×810（三 tab 各一次硬加载）**

| 断言（§15.8 编号） | 实测 | 判定 |
| --- | --- | --- |
| 24 卡头三件套 | 三 tab 均 `[data-sf-trust-levels-card-header]` 存在；`h3#sf-trust-levels-panel-title` + `p.mt-1.text-xs.text-muted` + `UBadge.font-mono` 文本 **`trust.levels`**（逐字） | 通过 |
| 24 卡头标题随 tab 换键（§15.1 表） | overview「等级总览」/ thresholds「信任等级阈值」/ earnable「能力解锁（按档新增）」；说明分别为 `sections.*.description` | 通过 |
| 25 页头唯一性 | `h2` = 1、`UDashboardToolbar` = 1、页内 `[role="tablist"]` = 1（后台多标签条 `.sforum-admin-tabs` 属 admin shell，本页未开 tab 时为 0）、**`h3` = 1（h3 去重后）** | 通过 |
| 26 推荐配置条位置 | 三 tab 均 `[data-sf-trust-levels-recommended]` 存在，且 DOM 顺序 关闭警告 → 推荐配置 → tablist（`beforeTabs: true`） | 通过 |
| 29 卡片体几何 | `data-sf-trust-levels-panel` 的 class = `grid max-w-5xl gap-6`；页 + 三面板 `space-y-5` 计数 = **0** | 通过 |
| 31 矩阵标题去重 | `.sf-trust-earnable__title` 计数 = 0；earnable 根 `section[aria-labelledby]` = `sf-trust-levels-panel-title` | 通过 |
| 28 阶梯行式清单 | `ul.divide-y.divide-slate-200` + 5 个 `li`；`li` 上无 `border-t` / `mt-3.5` / `pt-3.5`，`divide-y` 间距 0（分隔线由容器承担）；行距 53 / 45 / 45 / 45（TL0 多一行 `tl0Hint`） | 通过 |
| 布局基线未回归 | 页头/toolbar/tab 左边缘 280 / 280 / 280；无横向溢出；裁切控件 0；吸底 footer `gap = 42px` | 通过 |
| 门槛列数 | 五档仍 `6 列 / 1 行`（§15.4 未影响 §4 断点） | 通过 |

**B. 移动 390×844（三 tab 各一次硬加载）**

| 断言 | 实测 | 判定 |
| --- | --- | --- |
| 卡头换行不挤 | 标题宽 211.6、说明换行；`trust.levels` 徽章落在自己一行右对齐（`right 358 ≤ 390`） | 通过 |
| 阶梯右侧组整行换行 | TL2 左组 `right 144.6 / bottom 973`，右组 `left 32 / top 981 / width 326`（≥ 左组底边、与卡片同宽） | 通过 |
| 摘要不截断 | 摘要 `scrollWidth == clientWidth`，1 行 | 通过 |
| 无横向溢出 | `document.documentElement.scrollWidth = 390 = innerWidth`（三 tab 均） | 通过 |
| 门槛 2 列 | 五档全 `2 列 / 4 行 / 末项满宽` | 通过 |
| 矩阵两列 | earnable 矩阵 2 列、25 勾选框、`新增项 0/20` | 通过 |
| footer 不裁切 | 保存按钮 `right 358`、重置 `right 272`（≤ 390） | 通过 |
| 工具栏刷新可读可达 | `right 357 ≤ 390`、`aria-label="刷新"`、`title="刷新"`、工具栏内层无横向滚动 | 通过 |
| 吸底不压行 | `footerTop - lastContentBottom = 34px` | 通过 |

**C. 上一轮结论抽验（回归）**

| 项 | 实测 | 证据 |
| --- | --- | --- |
| 保存恰 1 条 PUT | 改 TL1 `days_visited 3→4` 点一次保存 → `PUT /api/v1/admin/web-options` **1** 条、Toast **1** 条；复原保存同样 1 条，落库复核回到 3 | `stage-save-spot.json` |
| 硬加载 `ready` | 三 tab 每条 URL 硬加载 `pageState = ready`，`/admin/trust-levels` 各 **1** 次 | `stage-v15-desktop.json` |
| 关闭态零请求 | `features.trust_levels=disabled` 后硬加载：`pageState=feature-off`、关闭警告 **1** 条、推荐配置条仍渲染（§15.2）、勾选框 0、**`/admin/trust-levels` 请求 0 次**；恢复 `enabled` 后 `ready` + 1 次 | `spot-feature-off.json`、`desktop-v15-feature-off.png` |

**D. 本轮新增的测试基建收益**

`apps/web/tests/helpers/vueSfc.ts` 补了 SFC 解析错误守卫（原来只看 `template.errors`，漏 `parse().errors`）。本轮正是靠它把「残留模板段」这种「bun 全绿 + typecheck 绿 + dev 500」的破损变红：补守卫后 16 条 `trustLevelsOverview.test.ts` 用例立即失败并点名 `SFC parse error in …:259`。

**E. 遗留（P3，不影响验收）**

1. **overview 面板内 `h3#sf-trust-levels-overview-title` 与卡头 `h3#sf-trust-levels-panel-title` 同名（都是「等级总览」）**：与 §15.1 第 5 条对矩阵的处理同理（同名标题会让读屏出现两个同名跳转条目），但该条只点名矩阵，overview 这处未被要求删除。实测 `h3` 计数 = 2，两个 id 都在。
2. 关闭态警告仍无 `role="alert"`/`aria-live`（§14.3-3 未变）。
3. 无 `trust.manage` 账号的 `forbidden` 态仍未验证（§14.5，账号不可得）。

**F. h3 去重后复验（2026-11-11，overview 同名标题删除后）**

`SFAdminTrustLevelsOverviewTab.vue` 305 行、`<h3` 计数 0，面板根 `section` 的 `aria-labelledby` 改指 `sf-trust-levels-panel-title`；`sf-trust-levels-overview-title` 与 `sf-trust-earnable-title` 全仓库归零。

| 断言 | 实测 | 证据 |
| --- | --- | --- |
| 桌面 overview 硬加载：`h3` 总数 = **1** | `h3Count = 1`，唯一一条为 `#sf-trust-levels-panel-title`（文本「等级总览」） | `h3-dedupe.json`、`desktop-v15-overview-h3dedupe.png` |
| 旧 id 归零 | `#sf-trust-levels-overview-title` = 0、`.sf-trust-earnable__title` / `#sf-trust-earnable-title` = 0 | `h3-dedupe.json` |
| `aria-labelledby` 锚点 | overview tab：1 个 `[aria-labelledby="sf-trust-levels-panel-title"]` = `section[data-sf-trust-levels-overview]`；earnable tab：1 个 = `section[data-sf-earnable-region]`（`h3-dedupe-earnable.json`）。两个面板互斥渲染（`KeepAlive`），DOM 里不会同时出现两个，各自指向同一锚点 | `h3-dedupe.json` / `h3-dedupe-earnable.json` |
| 其余唯一性未回退 | `h2` = 1、页内 tablist = 1、toolbar = 1、无横向溢出、`pageState = ready` | `h3-dedupe.json` |

---

### 15.9 overview 面板内部密度裁定（2026-11-11，设计侧）

**症状**：`SFAdminTrustLevelsOverviewTab.vue` 面板根 `<section class="grid min-w-0 gap-6">` 有 8 个直系子项，`gap-6` 让 8 段各自为政、恒为 24px；用户在 `/control-panel/forum/trust-levels` 把这种「八段等距」读成「到处都多了一大块空白」。

**尺度（本页唯一一套，5 档）**

| 档 | 值 | Tailwind | 管什么 |
| --- | --- | --- | --- |
| A | 4px | `gap-1` / `space-y-1` / `mt-1` | 行内：图标↔文本、同一列表项之间、行内附注（TL0 hint） |
| B | 8px | `mt-2` / `gap-2` | 标题↔它管辖的内容；并列徽章与说明 |
| C | 12px | `gap-3` | 同一簇内并列块之间（状态行↔状态行、状态簇↔说明列表） |
| D | 16px | `mt-4` | 分区内尾部动作行（引导↔跳转按钮）；hairline 脚注内边距 |
| E | 24px | `gap-6` | 分区↔分区（根容器唯一间距来源，禁止再叠 `space-y-*`） |

**结构：8 个直系子项 → 4 个分区**

1. **状态簇**（功能状态行 + 配置状态行 + 三条定性说明）包进 `<div class="grid min-w-0 gap-3">`（C=12px），不套卡片、不加边框、不加底色——簇只靠间距，分区靠 24px，脚注靠 hairline，三级手段各管一段。
2. **档位阶梯**：`div[data-sf-trust-levels-ladder]` = `h4` + `ul.mt-2` + `p[data-sf-trust-levels-tier-summary].mt-2`。档位统计是阶梯的**尾部图注**，不是独立分区。
3. **流程指引**：`div[data-sf-trust-levels-guide]` = `h4` + `ol.mt-2.grid.gap-3.sm:grid-cols-3.sm:gap-x-4` + `div[data-sf-trust-levels-jump].mt-4`。跳转按钮是「下一步去哪」的出口动作，属本分区，不能上下各 24px 悬空。
4. **限额脚注**：`p[data-sf-trust-levels-limits-note]` 单独占末尾，用 `border-t border-slate-200 pt-4 dark:border-zinc-800` 分隔——用线承担断开，不再用空白（与 `SFAdminForumGeneralTab.vue:39`、`SFAdminForumTagsTab.vue:52`、`SFAdminPersonalizationAnnouncementsTab.vue:226` 同一 `border-t` 语言，只是无标题故 `pt-4` 而非 `pt-5`）。

**h4 与内容：保持 `mt-2`（8px）**。`h4.text-sm.font-semibold` 与内容是「标题-从属」，必须比簇内节奏 C（12px）更紧，否则标题会漂在两块之间。settings 侧 `SFAdminPersonalizationAnnouncementsTab.vue:203-208` 用 `section.space-y-4` 让 h4 组与内容同值（16/16），那是「不分标题层级」的写法；本面板既已引入 5 档尺度，标题就该比内容更紧，8px 是最紧且不粘的值，对应表单 grid 时可放宽到 10px，但不许到 12px。

**不要一起压的两处**：阶梯 `li.py-3`（行高下限由 24px 徽章/20px 图标决定，压到 `py-2` 会让摘要与图标打架；`apps/web/tests/admin/forum/trustLevelsOverview.test.ts:234` 亦断言 `py-3`）；`h4` 字号 `text-sm`。引导 `ol` 的列距反而要从 `gap-2` 放宽到 `sm:gap-x-4`，三列 8px 横距会把三条步骤读成一句话。

**约束**：只动面板内部；页面壳 `div.grid.max-w-5xl.gap-6`（`apps/web/app/pages/admin/forum/trust-levels.vue:485`，`apps/web/tests/admin/forum/trustLevels.test.ts:346` 断言）保持。`space-y-5` 仍禁用（同测试 `:352`）。四个 `data-sf-trust-levels-*` 钩子全部保留，测试按属性查找，不依赖父级嵌套。
