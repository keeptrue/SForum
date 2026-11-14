# 信任等级「阈值 tab 折叠面板 + 影响确认弹窗」UI 规范

- 日期：2026-11-14
- 作者：小设（designer）
- 任务：`task-4`（共享任务，revision 2）
- 范围：**只读复核 + 规格产出**。未改动 `apps/web` 与 `apps/api` 下任何文件。
- 上游：`knowledge/plans/2026-11-14-trust-level-conditions.md` §4.4（D16：阈值 tab 内每档折叠面板，不新增 tab）
- 复用基线：`knowledge/reports/2026-11-12-trust-level-appearance-modal-spec.md`（同域已冻结的 modal 几何）
  + `apps/web/app/assets/css/sforum-trust-colors.css`（`--tl-accent` / `data-step` 唯一来源）

本文件只回答两件事：**折叠面板长什么样、落在哪个像素上**，以及**影响弹窗的三种版面怎么互斥**。
文案归 `task-3`（`…-conditions-copy-spec.md`），键名与句子以它为准；本文件只固定**版面位置与几何**。

---

## 0. 决策速览（含被否方案，便于 reviewer 复核取舍）

| # | 决策点 | 候选 | 裁决 | 一句话理由 |
| --- | --- | --- | --- | --- |
| D-1 | 展开开关位置 | A 图例行内右对齐 / B 数值网格下方独立一行 / C 行尾图标按钮 | **A** | 折叠态高度增量 = **0px**（塞进既有 24px 图例行）；B 给每档常驻加 40px；C 把图标按钮混进「外观/停用」命令组，语义被读成动作而非披露 |
| D-2 | 展开后由谁滚动 | A 页面滚（面板无 `overflow`）/ B 面板内滚（`max-height` + `overflow-y:auto`）/ C 桌面内滚、390 页面滚 | **A** | B 与 C 都会造出与页面同轴的第二个 scroller（徽章那刀实测：外层 `scrollWidth == clientWidth` 滚不动、`tabindex` 落错元素）；C 让两档实测判据不同，QA 无法用一条断言覆盖 |
| D-3 | 5 层嵌套的溢出策略 | A 横向滚动 / B 逐层压缩缩进 / C 截断层级 / D 不滚动、靠换行 | **D** | 390 最坏路径（合法上限「4 组 + 1 叶」，实测最内层 `clientWidth` **208**、叶子内容 **182**）≥ 单字段最小基宽 140px，且规则树每个 flex/grid 子项都已 `min-width:0` → 物理上不会横向溢出；A/B/C 都是为了一个不存在的问题引入成本（原写的 170 已作废，见 §1.3 更正块） |
| D-4 | 多档展开 | A 各自独立 / B 单开手风琴 | **B** | 32 叶满配时单棵树就是数千像素；8 档全开是数万像素的无意义长页。切换档不丢草稿（草稿在 composable），且 422 时自动展开出错档 |
| D-5 | 影响弹窗宽度 | A `sm:max-w-md`（徽章影响弹窗现值）/ B `sm:max-w-lg`（本页三个确认弹窗现值） | **B** | 同页一致性优先于跨页一致性：本页 restore / structure / tier 三个弹窗都是 `lg`，影响弹窗是第四个同族确认框 |
| D-6 | `≈` 与数字 | A 符号 + 数字同一行、符号只做视觉降级（当前裁决）/ B 只写「约 128 人」不要符号 / C 符号当数字前缀写进同一个 i18n 占位符 | **A** | B 在中文里「约」和 `≈` 语义重复但视觉层级不同，运营扫视时要的是数字；C 让 `≈` 无法单独降级颜色字号，且 `null` 态容易漏删 |
| D-7 | 目录不可用时能否保存 | A 禁用保存（当前裁决）/ B 只读但允许原样回传 | **A**（判据见 §6.2） | B 会多开一条「配置缺失时静默清空 rule」的路径，与徽章那刀「少写三列撞 CHECK 变 500」同类；闸门只卡在 `willWriteRule` 分支，不误伤纯数值编辑 |

---

## 1. 折叠面板几何

### 1.1 位置（与图标 / 名称 / 阈值输入的相对关系）

放在每档 `<legend>` 的**最右端**，与 24px 图标同一基线：

```
┌ fieldset[data-tier-id] ────────────────────────────────────────────┐
│ [icon 24] 正式用户   TL2                      [ 更多条件（3） ⌄ ]  │ ← legend，高 24
│                                                                    │
│ 访问天数      主题数      评论数      获赞数      收藏数      在线… │
│ [      ]     [     ]     [     ]     [     ]    [     ]    [    ]  │ ← 6 列 @≥1280
│                                                                    │
│ ┌ 面板（仅展开时）────────────────────────────────────────────┐ ▶ │
│ └────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────┘
```

- 开关**只**在 legend 内，不进入命令组（本页的 `SFAdminTrustLevelActionButton` 命令组属于「档位」tab，阈值 tab 没有命令组，不要为此新建一个）。
- 与阈值输入**不同行**：面板在 `.sf-trust-threshold-row__grid` **之后**、`</fieldset>` 之前。
  选在网格之后而不是 legend 之后，是因为 DOM 顺序必须等于视觉顺序（Tab 键从「6 个阈值输入」走到「规则树」），否则键盘用户会在面板与数值之间来回跳。
- 折叠态**占 0 行**：开关高度取 **24px**，恰好等于 legend 现有最高元素（`.sf-trust-threshold-row__icon` 24×24），所以整行高度与今天逐像素相同。

**必须同步的一条修正**：`.sf-trust-threshold-row__name`（`SFAdminTrustLevelThresholdsTab.vue:198`）现在没有收缩规则，长档位名会把右对齐开关顶出 390：

```css
.sf-trust-threshold-row__name {
  flex: 0 1 auto;      /* 可收缩 */
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sf-tl-rule-toggle { flex: 0 0 auto; margin-left: auto; }
```

不修这条，`档位名 24 字 + 开关」` 在 390 会把 `326px` 撑破 —— 这是本次改动**唯一**可能引入横向溢出的地方。

### 1.2 折叠态与展开态的行高

| 状态 | 高度 | 说明 |
| --- | --- | --- |
| 折叠 | 与今天**逐像素相同** | 开关 24px 并入 legend，无 margin 外溢 |
| 展开 | `今天 + 12px(margin-top) + 面板自身高` | 面板无 `max-height`、无内滚，行高按内容撑开 |

面板不参与 `.sf-trust-threshold-row` 的 `border-t` 分隔逻辑（分隔线在 fieldset 上，面板在 fieldset 内部），因此相邻档位的分隔线不受影响。

### 1.3 可用宽度（逐层扣减，可复算）

扣减链（每一层都是现有 CSS 的实测值，不是估算）：

| 层 | 规则来源 | 一次扣减 |
| --- | --- | --- |
| 后台侧栏（≥1024 显示） | `main.css:519` `min/max-width: 256px` | 256 |
| 滚动区 padding | `layouts/admin.vue:722` `p-4 sm:p-6` | 32 / 48 |
| UCard body padding | Nuxt UI `UCard` body 默认 `p-4 sm:p-6`（**未读主题源码**；390 的 32 由徽章 QA 实测 326 反证） | 32 / 48 |
| 面板容器上限 | `trust-levels.vue:479` `max-w-5xl` | 1024（封顶） |
| 折叠面板 | 本规范：border 2 + padding 24 | 26 |
| 每层规则组 | `SFAdminBadgeRuleGroup.vue:254-257` border 2 + padding 24 | 26 / 层 |
| 叶子 | `SFAdminBadgeRuleLeaf.vue:174-177` border 2 + padding 24 | 26 |

**1440（桌面）**

```
1440 − 256(侧栏) − 48(p-6) − 48(card p-6) = 1088  →  max-w-5xl 封顶 = 1024
面板内容 1024 − 26 = 998
组 d1 972 ｜ d2 946 ｜ d3 920 ｜ d4 894 ｜ d5 叶子内容 = 868
```

**390×844**

```
390 − 32(p-4) − 32(card p-4) = 326      ← 与徽章那刀实测的 clientW==326 同值
面板内容 326 − 26 = 300
组 d1 274 ｜ d2 248 ｜ d3 222 ｜ d4 196 ｜ d5 叶子内容 = 170   ← **已作废，见下方更正块**
```

> **更正（2026-11-14，M5 QA 实测；本条取代上面 390 阶梯的最后两格与「170」这个期望值）**
>
> 上面 390 阶梯的**最后两格（d4 196 / d5 叶子内容 170）作废**，错在三处叠加：
>
> 1. **层级口径错**：产品硬上限 `BADGE_RULE_LIMITS.maxDepth = 5`
>    （`apps/web/app/utils/admin/rule.ts:105`）是**总层数**，最深合法结构是「4 组 + 1 叶」；
>    阶梯却按「5 组 + 叶」再减一次。实测把 6 层写进 `PUT /admin/trust-levels` 直接
>    `422 rule_too_complex`——那条路径在产品里不存在。
> 2. **步长假设偏大**：阶梯按 26px/层推，用 d1=274 与实测 d4=208 反推，实际约 **22px/层**
>    （反推值；QA 只逐层记了最内层与叶子）。
> 3. **量错了对象**：170 标的是「叶子**内容**宽」，而 Q-12 把它当成「最内层 `clientWidth`」；
>    实测这两个量分别是 **182** 与 **208**，本来就不是同一个数。
>
> 实测（390×844 同源 iframe、`not → any → all → not → leaf`、`[data-rule-group]` 4 个 depth 1–4）：
> 最内层 `clientWidth=208 / scrollWidth=208`、叶子 `182`、`document.scrollWidth=390 == clientWidth`。
> **Q-12 的判据改为「最深层 `clientWidth ≥ 200` 且 `scrollWidth == clientWidth`、页面无横向溢出」**
> （与深度无关）。计划侧的同一处更正见
> `../plans/2026-11-14-trust-level-conditions.md` §4.4-b「U2 的 170 是怎么推错的」。

1440 那一路的结论**不依赖** card padding 的猜测（可用宽 1136 已大于 `max-w-5xl` 的 1024 封顶，哪怕 card 无内边距也是 1024）；390 那一路的 326 有徽章 QA 的实测反证。两处都不是推演出来的单点依赖。QA 的判据见上方更正块。

结论：

- 桌面：深度 5 的叶子拿到 **868px**，`.sf-rule-field`（`flex: 1 1 140px`）三个控件同排（各约 284px），规则树是「宽而扁平」。
- 390：合法 config 的最深层是「4 组 + 1 叶」，实测最内层 `clientWidth` **208**、叶子内容 **182**，三个控件各占一行；**注意即使深度 1（274px）也已小于 `140+8+140=288`，所以 390 下叶子恒为单列堆叠，与嵌套深度无关** —— 嵌套只贡献缩进视觉（按实测约 22px/层），不改变换行结果。

### 1.4 高度

**明确结论：面板没有 `max-height`，高度 = 内容高度，页面滚动。**

量级（级数估算，不是实测，QA 需记 `panel.offsetHeight`）：

| 场景 | 桌面 | 390 |
| --- | --- | --- |
| 单叶（无参数） | ≈ 70px | ≈ 186px（三行头） |
| 单叶（含 1 个参数） | ≈ 128px | ≈ 244px |
| 32 叶满配 | 约 3k px 量级 | 约 6k px 量级 |

这就是 D-4 选「单开手风琴」的量化依据：8 档全开的页长在数量级上不可用。

### 1.5 溢出策略：为什么不是横向滚动 / 缩进压缩 / 截断层级

**A. 横向滚动（否决）**

1. 会造出第二个滚动容器：徽章那刀实测过同形修法——在 `<UTable>` 外包 `overflow-x-auto` 后，外层 `scrollW == clientW == 326` 根本滚不动，真正的 scroller 在内层，`tabindex` 还落在滚不动的外层上（`knowledge/plans/2026-11-12-badge-rule-engine.md:556`、`knowledge/sessions/2026-11-12-badge-rule-engine-handoff.md:83`）。
2. 触屏上「页面纵向 + 面板横向」是两个轴的手势争用，390 上最容易误触。
3. 触发条件不存在：见 §1.3 更正块，合法最深结构（4 组 + 1 叶）实测最内层 `clientWidth` **208**、
   叶子内容 **182**，都 ≥ 140px 单字段基宽（原写的 170 不可达）。

**B. 逐层压缩缩进（否决）**

5 层总共才 104px；压缩能省的部分 < 100px，却让「同一层级在不同深度长得不一样」，QA 的像素判据失效（无法写死 `padding-left`）。收益远小于可维护性损失。

**C. 截断层级（否决）**

`depth > N` 折成「…」会把可编辑控件藏起来。运营看不到深层叶子，就无法修它；这与本文件开头那句「折叠会让运营漏改一档」是同一类风险，只是换了个位置。

**D. 不滚动，靠换行（采纳）**

规则树三个组件已经把 `min-width: 0` 写在每一个 flex/grid 子项上（`Group:253` / `Leaf:173` / `ParamField:358`），`min-width:0` 会覆盖 flex item 的自动最小尺寸，所以即使内容宽小于 140px 基宽也只是继续收缩、不会撑破。**面板本身必须同样声明 `min-width: 0`**，否则它会以子内容的 min-content 宽度参与父级 grid 的列宽计算。

> 硬约束：`[data-sf-tl-rule-panel]` 及其**所有后代**，计算样式里不得出现 `overflow-x` 或 `overflow-y` 为 `auto` / `scroll`。`overflow: hidden` 允许（现有 `.sf-rule-field__label` 用它做省略号）。

### 1.6 强调色契约（不得引入第二套）

档位颜色现在有三个来源，各管一件事：

| 层 | 载体 | 谁写 | 面板能不能碰 |
| --- | --- | --- | --- |
| 色相 | 祖先 `[data-sf-tl-accent]` 内联的 `--tl-accent`（token 档）或 `--tl-accent-light/-dark` 对（自定义 hex 档） | `tierAccentStyle`（`utils/theme/tierColor.ts:102`） | **只能 `var(--tl-accent)` 消费，不得声明** |
| 形状阶梯 | `.sf-tl-mark` 的 `data-step`（1 环 / 2 环+淡底 / 3 粗环 / 4 实心） | 阈值 tab 图标 `:data-step="min(4, rank)"` | **不适用**，面板不放 `data-step` |
| 面/线强度 | `--tl-accent-strength` / `--tl-accent-tint` | `.sf-tier-row`（65/45/55%）、`[data-retired] .sf-tl-mark`（6/8%） | 可声明（见 §1.7） |

三条禁令：

1. **面板内不得内联 `--tl-accent`。** `sforum-trust-colors.css:93-99` 的 `[data-sf-tl-accent]` / `.dark [data-sf-tl-accent]` 两条规则负责「按当前配色挑明/暗那一支」。内联 `--tl-accent` 的优先级高于这两条选择器，自定义 hex 档在深色后台会**显示未提亮的原色**，与公开页看到的不是同一个色。同理 `--tl-accent: var(--sf-tl-xxx)` 这类内联也禁止，只允许在祖先上存在。
2. **不得用 `--sf-accent` 当档位装饰色。** `--sf-accent` 是站点主色，随主题在 teal / blue / violet / rose / amber 五套间切换（`main.css:31/110/130/150/170`）；amber 主题下它会与 amber 档位色撞色，运营读不出「这一块属于这一档」。
3. **不得新增 hex。** 需要新增颜色时只能从 `--sf-tl-*`（9 token × 明暗 18 个字面量）里取。

**唯一豁免：焦点环。** 规则树组件现有 `outline: 2px solid var(--sf-accent)`（`Group:324`、`Leaf:222`、`ParamField:382`、`Editor:207`）。焦点环是**全站可达性约定**，必须与站点色一致，**不要**改成档位色 —— 每个档位一套焦点色会让「键盘焦点在哪」这个唯一信号随档位变色。

面板自身的可抄规格：

```css
.sf-tl-rule-toggle {                     /* legend 内右对齐开关 */
  display: inline-flex; align-items: center; gap: 4px;
  flex: 0 0 auto; margin-left: auto;
  height: 24px; padding: 0 8px;          /* 高度 = legend 图标高度 → 零位移 */
  border: 1px solid var(--sf-border); border-radius: 8px;
  background: var(--sf-card); color: var(--sf-fg-secondary);
  font-size: 12px; font-weight: 600; line-height: 1; white-space: nowrap;
}
.sf-tl-rule-toggle[aria-expanded="true"] {
  border-color: color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 45%, var(--sf-border));
  background: color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 8%, var(--sf-card));
  color: var(--tl-accent, var(--sf-tl-slate));
}
.sf-tl-rule-toggle:focus-visible { outline: 2px solid var(--sf-accent); outline-offset: 1px; }
.sf-tl-rule-toggle__chevron { width: 14px; height: 14px; }
.sf-tl-rule-toggle[aria-expanded="true"] .sf-tl-rule-toggle__chevron { transform: rotate(180deg); }

.sf-tl-rule-panel {
  --tl-accent-strength: 65%;
  min-width: 0;
  margin-top: 12px; padding: 12px;
  border: 1px solid color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 28%, var(--sf-border));
  border-left: 3px solid color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) var(--tl-accent-strength), var(--sf-border));
  border-radius: 10px;
  background: var(--sf-card);
}
```

- 左侧 3px 色条沿用 `.sf-tier-row` 的现成配方（`TierRow:256`：`color-mix(accent strength, border)`），理由是阈值 tab 里除 24px 图标外**没有任何**档位色，面板这么大一块不挂色条会与它的档脱钩；同时不新增第三种配方。
- 箭头**不做 transition**：本页已有「视觉规范禁动效」的先例（`trust-levels.vue:612` 注释：虚线占位、无骨架微光），箭头瞬时翻转即可。

### 1.7 停用档（retired）

- **可以展开、可以编辑**：停用是软停用，配置保留、判定跳过（D-N8），规则同样保留可改。
- 视觉只降强度、不加新信号：`--tl-accent-strength` 降到 **45%**（深色 **55%**，与 `TierRow:243-252` 同值），面板容器 `border-style: dashed`，但**左侧 3px 色条仍为实线**（3px 虚线读起来像「被禁用」，会误导成不可编辑）。
- **不再加**「本档已停用」的 alert 或 badge：本章已经有图标虚线边框（`ThresholdsTab:194`）和图例里的「已停用」chip（`:127`）两个信号，第三个只会变成噪声。若 `task-3` 为停用档写了提示句，位置只能是面板内首行 12px 灰色文本（`--sf-fg-secondary`），不是 `UAlert`。

### 1.8 展开模型（单开）与错误自动展开

- 组件内 `ref<string | null>` 记录当前展开的 tier id；**不进表单状态、不进 `trust.levels` JSON**（进去会改变配置字节，破坏计划 §3.1 的回滚口径），刷新后全部折叠。
- 打开 B 档自动收起 A 档：草稿在 composable 里，不丢；且不会出现「两棵半开的树 + 一个保存按钮」的归属歧义。
- **422 归位规则**：后端新增的 5 个规则错误码都是 tier 级（带 `rank` + `tierId`），落到 `saveErrorBuckets.byTierId` 后，前端若发现某档的错误 `field` 属于规则域，则**自动展开该档面板并 `scrollIntoView({ block: 'nearest' })` 该 fieldset**。错误文本仍由 `SFAdminTrustLevelFieldErrors` 渲染在行首（见 §1.9），**不新增第二套错误渲染**。

### 1.9 面板内部结构（自顶向下完整顺序）

```
[data-sf-tl-rule-panel]
├─ 规则编辑器（现 SFAdminBadgeRuleEditor，抽成 components/admin/rules/）
│   ├─ 空态：一行 12px 灰字 + [添加条件] 按钮（border 1px dashed，高 44）
│   ├─ 加载态：一行 12px 灰字（目录拉取中）
│   ├─ 失败态：一行红色 12px 文本，永不空白（目录拉不到时规则不可编辑，并触发 §6.2 的保存闸门）
│   └─ 正常态：模式切换 → 句子 → 树 → JSON 预览
└─ 新建档位的估算提示（无已保存规则时一行 12px 灰字 + i-lucide-info）
```

- 面板**不渲染自己的标题**（不出现第二个「条件」h3）：开关本身就是这一块的标签，再给一个标题会让一档里出现两个视觉起点。
- 面板**不包 `UCard`**：本轮约束是「阈值 tab 内加一个折叠面板」，包卡片等于引入第三套卡片体系（页级 `UCard` → 面板 → 规则组三层嵌套边框）。

### 1.10 图标白名单（只用既有图标库，无手写 SVG、无 emoji）

| 位置 | 图标名 | 仓库内是否已有 |
| --- | --- | --- |
| 展开开关箭头 | `i-lucide-chevron-down` | 已有（`layouts/admin.vue`） |
| 空态「添加条件」 | `i-lucide-plus` | 已有（规则组 `addCondition`） |
| 目录加载失败 | `i-lucide-circle-alert` | 已有 |
| 新建档位估算提示 | `i-lucide-info` | 已有（阈值 tab restoreHint） |
| 影响弹窗数字前 | `i-lucide-users` | 已有（图标包内有，校验通过） |

字体：**不引入任何字体**。沿用 `main.css:24` 的 `Inter, "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif`；等宽（如有）走 `var(--sf-font-mono, ui-monospace, monospace)`（`TierRow:321` 已有用法）。无新图片、无新 SVG 素材。

---

## 2. 影响确认弹窗（`SFAdminTrustLevelRuleImpactDialog.vue`）

### 2.1 骨架：整段复用 2026-11-12 已冻结的几何，只换宽度

| 项 | 值 | 来源 |
| --- | --- | --- |
| 宽度 | `sm:max-w-lg`（512px）；<640 全宽 − 24px（左右各 12） | 本页三个确认弹窗同款 |
| 最大高度 | `min(86dvh, 720px)`；<640 `calc(100dvh - 48px)` | 外观弹窗冻结值，逐字复用 |
| 分段 | header（不滚）/ body（**唯一**滚动区）/ footer（吸底） | 同上 |
| body | `overflow-y: auto; overscroll-behavior: contain; padding: 16px 18px 24px` | 同上 |
| footer | `position: sticky; bottom: 0; z-index: 1; background: var(--sf-card); border-top: 1px solid var(--sf-border); padding: 12px 18px` | 同上 |
| <640 内边距 | 左右 14px | 同上 |
| ARIA | 自建内容根 `role="dialog" aria-modal="true" aria-labelledby`（UModal 内容根不带这两个属性，本页三个弹窗都手工补了） | `TierConfirmDialog:75` |

不新增 `✕` 关闭按钮 —— 本页三个弹窗都没有；关闭途径固定为 Esc / 点遮罩 / 「取消」，三者走同一条 `cancel` 路径（`TierConfirmDialog:44` 的既定做法）。加 `✕` 会引入「焦点先落在哪个按钮」的新问题，且与同页不一致。

由「不加 `✕`」直接推出一条：本弹窗**没有图标按钮**，因此按 Q1（§6.1）它没有任何需要 `aria-label` 的元素 —— 本轮新增可访问名键 0 个。若将来确实要加图标关闭，只复用既有 `common.close`（zh-CN 实测=「关闭」），不造新键。

### 2.2 三态互斥表

| 判定 | 版面 | 说明 |
| --- | --- | --- |
| `estimated !== null && !partial` | **态 A** 有值 | 数字 + 依据句 |
| `estimated !== null && partial` | **态 A′** 有值 + 琥珀提示 | 数字 + 依据句 + 「只算了部分条件，是上界」 |
| `estimated === null` | **态 B** 无法估算 | 无数字、**无 `≈`**；逐个列不可估算条件 |
| 无已保存规则（新建档） | **不弹窗** | 只在面板底部给一行提示（§2.6） |

- 互斥是硬约束：`estimated === null` 时**不渲染** `partial` 行。否则屏幕上同时出现「无法估算」和「只算了部分条件」，两句互相否定。
- 「部分」不是独立第三态，而是态 A 的修饰 —— 没有数字时谈「上界」没有意义。

### 2.3 逐态结构图

**态 A（有值，含 0）**

```
┌ modal sm:max-w-lg ────────────────────────────────────┐
│ header  16/18/12                                      │
│   影响范围确认                        16px/700         │
│   这一句说清估的是「已保存的规则」      12px/1.5 次级   │
├ body  16/18/24（唯一滚动区）────────────────────────── │
│                                                       │
│   ≈ 128 人                        ← 唯一数字位，18/700 │
│   依据：三个条件同时满足，取最严的交集   12px 次级      │
│   （态 A′）只算了部分条件，这个数字是上界  12px 琥珀    │
│                                                       │
├ footer  12/18 吸底 ──────────────────────────────────  │
│                                  [ 取消 ]  [ 继续保存 ]│
└───────────────────────────────────────────────────────┘
```

**态 B（`estimated: null`）**

```
├ body ───────────────────────────────────────────────── │
│   无法估算                        16px/700，正文色      │
│   这些条件现在估不出来：           12px 次级            │
│   • 历史置顶数量                   12px，行高 18         │
│   • 已拥有徽章（徽章已删除）                             │
│   • …（默认 5 条，见 §2.4）                             │
│   [ 还有 3 条 ⌄ ]                  ← 展开而非内滚        │
├ footer ─────────────────────────────────────────────── │
│                                  [ 取消 ]  [ 仍然保存 ]│
└───────────────────────────────────────────────────────┘
```

**新建档位（无已保存规则）—— 不弹窗，只在面板底部：**

```
│   ⓘ 保存后才能估算影响范围          12px，--sf-fg-secondary │
```

### 2.4 数字位排版

| 元素 | 规格 |
| --- | --- |
| 容器 | `display: flex; align-items: baseline; gap: 4px; font-variant-numeric: tabular-nums;` |
| `≈` | 字号 **15px**、字重 500、色 `--sf-fg-secondary`；`aria-hidden="true"` |
| 数字 | 字号 **18px**、字重 700、色 `--sf-fg`；`tabular-nums` 保证 8 → 128 时右边界稳定 |
| 单位 | 字号 **12px**、字重 500、色 `--sf-fg-secondary`，与数字同基线 |
| 读数 `0` | 与其它读数**完全同款**（不加灰、不显绿、不加「没有用户受影响」的庆祝话）；`0` 是读数，`null` 才是算不出来 |
| `null` | **整行不出现 `≈`**；标题用 16px/700 正文色，不用红（红色只留给「获取失败」） |

**数字在整屏只出现一次。** 若把 `{n}` 同时写进上方说明句（`task-3` §3.3 的「约 {n} 人受影响」原样）和数字位，屏幕上会出现两个数字，运营会怀疑它们是不是两个口径 —— 这是本文件与 `task-3` 的唯一冲突点，见 §6。

可访问性（按 Q1 收敛，见 §6.1）：`≈` 只做**视觉**降级，保留在文本流里 —— 不 `aria-hidden`、数字位不加 `aria-label`。「这是估算值」的语义由上方说明句承担，数字本身照常被读出。本轮新增可访问名键 0 个。

### 2.5 不可估算条件列表（>5 条怎么收）

- 默认渲染**前 5 条** `<li>`；第 6 条起由一个 `button[aria-expanded]` 揭示，文案形如「还有 N 条」（`task-3` 给键），展开后全量渲染、按钮变「收起」。
- **不用** `max-height` + `overflow: hidden` 裁切，也**不用** `max-height` + `overflow-y: auto`：前者会藏掉条件名（运营拿不到修法线索），后者会在这个弹窗里再造一个滚动条（body 已是唯一滚动区）。
- 列表样式沿用页内既有：`list-style: disc; padding-left: 20px; font-size: 12px; line-height: 18px; color: var(--sf-fg-secondary)`，每项 `break-words`（长条件名不撑宽）。
- 只列 `unsupportedConditions[]`；态 A′ **不重复列**（正文已有「部分」句），列表只在态 B 出现。

### 2.6 按钮：文案、顺序与确认层级

| 位置 | 样式 | 说明 |
| --- | --- | --- |
| 左（DOM 第一） | `color="neutral" variant="outline"` | 「取消」 |
| 右 | `color="warning" variant="solid"` | 「继续保存」/「仍然保存」（`task-3` 定稿） |

- 顺序：`justify-content: flex-end; gap: 8px`，`flex-wrap: wrap`（390 也放得下，但允许换行不裁字）。
- **确认层级 = warning，不是 error**：本页的层级约定是「不可恢复 → `error`（删除档位）」「可恢复但影响面大 → `warning`（停用 / 恢复默认 / 结构变化）」。改严规则会触发降级，但改回来就能恢复，属于第二类。
- **不叠第二层确认**：计划 D8 的「两步确认」= 保存 → 影响弹窗 → 确认，弹窗本身就是第二步；再套一层会让人闭眼点确定（与 `trust-levels.vue:326` 拒绝叠加的理由同源）。
- 焦点：打开后焦点落**取消**（破坏性动作的默认安全位）；Esc = 取消；关闭后焦点回到触发保存的 `[data-sf-trust-levels-save]`。
- 估算加载中：确认按钮 `disabled` + `aria-busy="true"` 落在 body 上，避免「还没数就确认」。

---

## 3. 390×844 专项

### 3.1 滚动归属 —— 只有一个，指名道姓

> **唯一滚动容器：`div.sforum-admin-scroll-area`（`apps/web/app/layouts/admin.vue:722`，`overflow-y: auto`，`p-4 sm:p-6`）。**
> 折叠面板**不是**滚动容器：它没有 `overflow`、没有 `max-height`、没有 `position: sticky`。展开只是把页面撑长，滚动永远发生在 `.sforum-admin-scroll-area` 上。

「单滚动容器」的准确表述（避免 reviewer 把弹窗也判红）：

- 这是**每层表面（surface）各一份**的约束，不是全页加起来只能有一个：页面层 = `.sforum-admin-scroll-area`；模态层 = 弹窗 body。两层不会同时对用户生效（模态打开时背景 inert + `aria-hidden`）。
- 反面教材：徽章那刀在 `<UTable>` 外面再包一层 `overflow-x-auto`，结果外层 `scrollWidth == clientWidth == 326` 滚不动、内层才是真 scroller，`tabindex` 落错元素（`knowledge/plans/2026-11-12-badge-rule-engine.md:556`）。**本次不得出现任何「为了保险」而加的 `overflow-*` 包裹层。**

390 实测判据（可直接粘贴到 QA 脚本）：

```js
// 1) 面板不给 shell 基线**新增**纵向滚动容器（基线可能是 1 也可能是 2，见下方 S1 更正）
const activeScrollers = [...document.querySelectorAll('*')].filter((el) => {
  if (el.closest('[aria-hidden="true"]')) return false;         // 模态打开时的背景层不计
  const s = getComputedStyle(el);
  return /auto|scroll/.test(s.overflowY) && el.scrollHeight > el.clientHeight + 1;
});
// 期望：展开前后 length 不变（增量 0），且其中必有 .sforum-admin-scroll-area

// 2) 面板子树不给基线新增 auto/scroll 轴（基线本身不是 0，见下方 S2 更正）
[...panel.querySelectorAll('*')].filter((el) => {
  const s = getComputedStyle(el);
  return [s.overflowX, s.overflowY].some(v => v === 'auto' || v === 'scroll');
}).length === panelBaselineCount;   // panelBaselineCount 在面板未挂载时同法量一次

// 3) 页面无横向溢出
document.documentElement.scrollWidth <= window.innerWidth + 1;
```

> **S1/S2 更正（2026-11-14，M5 QA 实测；计划 §4.4 的同一张更正表，与本节冲突处以此为准）**
>
> | # | 本规范原写法 | 实测后 |
> | --- | --- | --- |
> | S1 | Q-02「恰好 1 个纵向滚动容器」 | **全站不成立**：`/control-panel/users`（未改动页）同样有 2 个（侧栏导航列 + `.sforum-admin-scroll-area`），390 下侧栏收起才恰好 1 个。判据改为「**面板不给 shell 基线新增 scroller**」。390×844 同源 iframe 内 `overflow-x ∈ {auto,scroll}` 的容器实测 3 个（`.sforum-admin-tabs` / `.sforum-admin-scroll-area` / `.shrink-0`）**全属 shell**，其中真正会滚的（`scrollWidth > clientWidth`）= **0**。 |
> | S2 | Q-03「面板子树内 `overflow≠visible` 元素数为 0」 | **两次更正**。① M3b 实施期：面板子树里唯一 `overflow:auto` 是共享编辑器 JSON 预览的 `<pre>`，它在默认收起的 `<details>` 里（M3a 原样搬来），改它＝改徽章编辑器行为 → 判据暂改为「排除收起 `<details>` 子树后为 0」。② M5 QA：这条**在本仓永远不可能为 0**——排除 `details` 后仍有上面那 3 个 shell 容器；按字面统计 `overflow ≠ visible`（含 `hidden` / `clip`）共 **109 个**，shell 到处 `overflow-hidden` / `clip`（含 `IMG`）。所以 S2 与 S1 同构：「**面板不给 shell 基线新增 scroller / 新增 `overflow≠visible` 元素**」——基线本身不是 0，比的是**面板带来的增量**。证据：`work-team/review/t13-qa/REPORT.md` §2。 |

为什么不让面板内滚（三条，任一都足以否决）：

1. 与页面同轴的第二个 scroller —— 正是徽章那刀的缺陷形状；
2. 页面底部有吸底保存条（`trust-levels.vue:461` 的 `UCard` footer `sticky bottom-0`），面板内滚到底后再滚页面需要「滚动链切换」，390 上最容易误触；
3. 内滚必须写 `max-height`，键盘弹起时 `dvh` 收缩会把叶子控件挤出可视区。外观弹窗用 `dvh` 是成立的，因为它是隔离的模态层；页面层没有这个豁免。

### 3.2 规则树叶子行在 390 的控件堆叠顺序

单列堆叠，顺序 = DOM 顺序（**禁止用 CSS `order` 重排**，否则 Tab 顺序与视觉顺序分叉）：

```
[条件 ▾                      ]   ← 标签 11px，控制器 30px
[比较符 ▾                    ]
[值            ] [单位 11px  ]
[参数控件 1                  ]   ← repeat(auto-fit, minmax(140px,1fr)) 在 208px 内自动退成 1 列
[参数控件 2                  ]
```

| 项 | 规格 |
| --- | --- |
| 字段块 | `display:flex; flex-direction:column; gap:4px`（`Leaf:193-199` 现值） |
| 标签 | 11px / `--sf-fg-tertiary` / 单行省略（`Leaf:201-207` 现值） |
| 控制器 | `padding:4px 8px; border:1px solid var(--sf-border); border-radius:8px; font-size:13px`（现值，不改） |
| 字段间距 | `gap: 8px`（现值） |
| 叶子内边距 | `10px 12px`（现值） |
| 组内动作按钮 | ≤480 只留图标（`Group:357-362` 现值：`.sf-rule-add span { display:none }`），`aria-label` + `title` 必须保留 —— 本规范不改这条 |

单位后缀（`Leaf:257-262`，11px `--sf-fg-tertiary`）在 390 保持同行；`flex: 1 1 auto` 的输入 + `flex: 0 0 auto` 的单位，在最内层 208px（实测）内仍放得下（输入最小 100px 左右）。

### 3.3 键盘可达性与 `aria-*`

Tab 顺序（= DOM 顺序，见 §1.1）：

```
展开开关 → 6 个 [data-sf-threshold-input] → 面板：[简单][高级] → 组 op select →
组动作按钮 → 叶子[条件]→[比较符]→[值]→[参数…] → JSON 预览 <summary>
```

| 元素 | 要求 |
| --- | --- |
| 展开开关 | `<button type="button">`；`aria-expanded="true|false"`；`aria-controls="<面板 id>"`；**不挂 `aria-label`**（可见文本已足够，再挂会念两遍，见 §6.1）；Enter/Space 原生触发；`min-height/width ≥ 24px`（WCAG 2.5.8 AA 最低值，本设计正好 24） |
| 面板 | `<div :id role="region" :aria-labelledby="<开关 id>" :hidden="!expanded">`；内部编辑器用 `v-if="expanded"` —— 外层用 `[hidden]` 保证 `aria-controls` 始终能解析（APG disclosure 模式），内层 `v-if` 保证收起时不挂载重型编辑器 |
| 面板收起 | 只用 `[hidden]`，**不要**用 `max-height:0` + `overflow:hidden` 做动画（会变成隐藏的滚动容器，且本页禁动效） |
| 叶子控件 | 现有 `<label>` 包裹已建立 label/control 关联；`USelectMenu` 必须带 `:aria-label`（`ParamField:311` 已有，**复用现值，不新增键**：它的可见 `<label>` 文本不覆盖内部 combobox） |
| 焦点可见 | 沿用 `outline: 2px solid var(--sf-accent); outline-offset: 1px`（不得改档位色，见 §1.6 豁免） |
| 影响弹窗 | `role="dialog"` + `aria-modal="true"` + `aria-labelledby=<标题 id>` + `aria-describedby=<说明句 id>`（**不指数字行**：数字行没有可访问名，§6.1）；打开后焦点落「取消」；Esc = 取消；**不加 `✕`**（故无图标按钮、无新键）；关闭后焦点回 `[data-sf-trust-levels-save]` |

---

## 4. 视觉 QA 判据清单（reviewer / ops 用，全部可机器断言）

| ID | 判据 | 断言（390×844 与 1440 各跑一次） |
| --- | --- | --- |
| Q-01 | 页面无横向溢出 | `document.documentElement.scrollWidth <= innerWidth + 1` |
| Q-02 | **面板不给 shell 基线新增 scroller**（原「恰好 1 个」作废，见 §3.1 S1 更正） | 见 §3.1 第 1 段；展开前后 `activeScrollers.length` **增量 0**，且集合里含 `.sforum-admin-scroll-area` |
| Q-03 | 面板子树**不给基线新增** scroller 轴（原「无任何 scroller 轴」作废，见 §3.1 S2 更正） | 见 §3.1 第 2 段；面板挂载前后同法计数，`length` **增量 0**（基线含 shell 的 `.sforum-admin-tabs` / `.sforum-admin-scroll-area` / `.shrink-0`） |
| Q-04 | 折叠态零位移 | `legend.getBoundingClientRect().height === 24`；`toggle.height === 24`；`Math.abs(toggle.top - icon.top) <= 0.5` |
| Q-05 | 展开不改相邻行基线 | 记录另一档输入框的 `{top,left,height}`；展开后再测：`left` 与 `height` 变化为 **0**，只有 `top` 整体下移（= 面板高度） |
| Q-06 | 单开 | `document.querySelectorAll('[data-sf-tl-rule-panel]:not([hidden])').length <= 1` |
| Q-07 | 展开开关 ARIA 配对 | `toggle.getAttribute('aria-expanded') === 'true'` 且 `document.getElementById(toggle.getAttribute('aria-controls'))` 非空 |
| Q-08 | 色相继承一致 | `getComputedStyle(panel).getPropertyValue('--tl-accent') === getComputedStyle(panel.closest('fieldset')).getPropertyValue('--tl-accent')` |
| Q-09 | 面板**未**内联 `--tl-accent` | `!(/--tl-accent\s*:/.test(panel.getAttribute('style') || ''))` |
| Q-10 | 深色派生生效 | 为某档设自定义 hex（如 `#7c3aed`）→ `html.classList.add('dark')` → `getComputedStyle(panel).borderLeftColor` 与浅色下的值**不相等**（证明吃到了 `--tl-accent-dark`，而不是原色直出） |
| Q-11 | 无第二套强调色 | 面板子树内不存在 `background`/`border-color` 计算值为 `--sf-accent` 或其 `-soft` 的元素（焦点环 `outline-color` 例外） |
| Q-12 | 最深层不溢出 | 构造 **4 组 + 1 叶（5 层，合法上限）** → 最深层 `.sf-rule-group` 的 `scrollWidth == clientWidth` 且 `clientWidth >= 200`；`document.scrollingElement.scrollWidth == clientWidth`（**期望值 208 已由 M5 QA 实测；原写的「≈170」不可达，见 §1.3 更正块**） |
| Q-13 | 叶子在 390 单列 | 深度 1 的叶子三个字段 `top` 两两不同（各占一行），`left` 相同 |
| Q-14 | 弹窗 `null` 态无 `≈` | `dialog.textContent.includes('≈') === false`；`[data-sf-tl-impact-estimate]` 不存在；`[data-sf-tl-impact-unestimable]` 存在 |
| Q-15 | 弹窗有值态 `≈` 唯一 | `[data-sf-tl-impact-estimate]` 文本匹配 `/≈\s?\d/`；`dialog.textContent.match(/≈/g).length === 1` |
| Q-16 | `0` 与 `null` 不互顶 | 估值为 0 → 走 Q-15 分支；`null` → 走 Q-14 分支；两者渲染节点互斥 |
| Q-17 | >5 条件默认收起 | `[data-sf-tl-impact-unsupported] li` 计数 `=== 5`；`[data-sf-tl-impact-unsupported-more]` 的 `aria-expanded === 'false'`；展开后计数 === 总数 |
| Q-18 | 弹窗单滚动 | 弹窗内 `overflowY: auto` 且真正溢出的元素 `=== 1`（即 body），footer 不在其中 |
| Q-19 | 无 emoji、无手写 SVG | 面板与弹窗子树内 `svg` 均带 `class="iconify"`（或 `data-icon` 前缀为 `lucide`/`tabler`）；文本节点无 emoji 码位 |
| Q-20 | 触达尺寸 | `toggle` 的 `width/height >= 24`；所有可点击元素 `>= 24×24`（WCAG 2.5.8 AA） |
| Q-21 | 保存闸门按 §6.2 生效（存 rule 分支） | 拦截目录请求返 500，且某档有 `rule` → `[data-sf-trust-levels-save].disabled === true`，且面板内可见原因节点存在（不是只有 `title`） |
| Q-22 | 保存闸门不误伤（不存 rule 分支） | 同一 500 下把所有档的 `rule` 清空 → `[data-sf-trust-levels-save].disabled === false`（只改数值不受目录影响） |
| Q-23 | 开关无冗余可访问名 | `toggle.hasAttribute('aria-label') === false`；`impactDialog.querySelector('[aria-label]')` 为空 |

面板的观测点（建议命名，与现有 `data-sf-*` 风格一致，QA 脚本不用再改选择器）：
`data-sf-tl-rule-toggle="<tierId>"`、`data-sf-tl-rule-panel="<tierId>"`、
`data-sf-tl-impact-estimate` / `-unestimable` / `-partial` / `-unsupported` / `-unsupported-more`。

---

## 5. 落地清单（给前端；本文件不改代码）

| 文件 | 动作 | 备注 |
| --- | --- | --- |
| `apps/web/app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue` | legend 加开关、grid 后加面板容器、`.sf-trust-threshold-row__name` 加省略号三连 | 239 行 → 约 330 行，仍在 AGENTS 的 500 行 review 阈值内 |
| `apps/web/app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleSection.vue`（新） | 面板外壳 + 编辑器宿主 + 空/加载/失败三态 + 新建档提示 | 计划 §4.4 已点名，需登记 `catalog-identities.json` 的 `ui` + 生成物 |
| `apps/web/app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleImpactDialog.vue`（新） | §2 全部 | 同上登记 |
| `apps/web/app/components/admin/rules/*`（由 `badges/rule/*` 搬） | props 形状不变，徽章侧只改 import | 计划 §4.4 已定；本规范依赖它的既有几何（§1.3 的 26px/层、140px 基宽、min-width:0） |
| `apps/web/app/composables/admin/forum/trustLevels/useAdminTrustLevelConditions.ts`（新） | 目录三态（loading/failed/ready）+ 导出 `catalogUsable`（§6.2 的四条判据只在这里实现一次）；失败态**必须**给一行可见文案而不是空白 | 保存闸门消费 `catalogUsable`，不在按钮处重复判断 |
| `apps/web/app/composables/admin/forum/trustLevels/useTrustLevelRuleDraft.ts`（新） | 草稿 + 前端镜像校验 + 展开状态 | 展开状态**不进** `trust.levels` |
| `apps/web/app/pages/admin/forum/trust-levels.vue` | 保存按钮的 `:disabled` 追加 `willWriteRule && !catalogUsable`（§6.2） | 只加一个布尔项，不改 footer 结构 |

i18n：键族是 **`admin.forum.trustLevels.*`**（实测 **394** 个叶键）。不是 `admin.settings.trustLevels.*`（实测 `undefined`）——`task-3` 的 §4 硬约束这条写错了，见 §6.5。

---

## 6. 已裁决口径（Lead 2026-11-14 拍定，实施期不再讨论）

### 6.1 Q1 — 不新增 `aria-label` 键

**结论：展开开关只挂 `aria-expanded` + `aria-controls`，不挂 `aria-label`，本规范不新增任何可访问名键。**

理由：开关自己有可见文本（「更多条件（{n}）」/「添加条件」，`task-3` 定稿），再挂 `aria-label` 会让读屏把同一句话念两遍；`aria-expanded` 已经表达开合状态，`aria-controls` 已经建立「它控制哪块」的关系。

需要 `aria-label` 的**只有图标按钮**。本轮影响弹窗**不加** `✕`（§2.1：关闭途径固定为 Esc / 遮罩 / 取消，与本页另三个弹窗同款），所以本轮新增可访问名键 **0 个**。将来若给某处补图标关闭按钮，复用既有 `common.close`（zh-CN 实测=「关闭」），**不造新键、不造第三族**。

连带取消 §2.4 早稿里「数字位容器给 `aria-label`」那条：`≈` 保留在文本流里（不 `aria-hidden`、不加可访问名），「这是估算值」的语义由上方说明句承担，数字本身照常被读出。

### 6.2 Q2 — 目录不可用时禁用保存，判据如下（结论式）

**保存按钮 `disabled` 的完整判据（前端照此实现，不做第二套判断）：**

```
保存不可用  ⇔  canManage === false
            ∨  saving === true
            ∨  willWriteRule ∧ ¬catalogUsable
```

其中：

- `willWriteRule`：本次提交的载荷里**至少有一档带 `rule`**（`form.tiers.some(t => t.rule != null)`，含未保存的草稿）。
  只改数值、所有档 `rule` 均为空时，`willWriteRule === false` —— 此时目录不可用**不影响**保存：这次提交不会写出任何 `rule` 字节，也就不存在「配置缺失时静默清空 rule」的路径。
- `¬catalogUsable`：`GET /admin/trust-levels/conditions` 满足任一即成立 ——
  1. 请求失败（网络错误或非 2xx）；或
  2. 响应 `conditions.items` 为空数组；或
  3. 载荷内**有** `fact.count` 叶子，而响应 `factCandidates` 为空数组；或
  4. 载荷内**有**某 `objectRef` 参数（`refKind = k`），而响应 `refCandidates[k]` 缺失。

  第 3、4 条是「无法校验」而非「本来就没有」：候选缺失时前端判不出该叶子是失效还是有效，原样回传等于把不可校验当成有效。

禁用状态下必须给**可见**原因（不是只挂 `title`：`disabled` 按钮不可聚焦，`title` 读不到）。原因行的位置固定在面板内首行（§1.9 的失败态），文案键由 `task-3` 定；文案要写清「目录加载失败，先修好它才能保存带条件的档位」这类可操作句，不出现「校验失败」这种内部词。

采纳更保守一侧的理由（记录用）：徽章那刀 `CreateBadge` 少写三列、自动徽章撞 CHECK 变 500，同类「宽容路径静默写坏数据」的缺陷最难查。这里的容差只保留在**不写 `rule`** 的那条分支上，边界窄且可断言。

### 6.3 Q3 — 停用档允许配规则

**结论：允许，且必须允许。**

停用是软停用：配置保留、判定跳过（D-N8）。给停用档配规则是「先配好 → 观察 → 再启用」的唯一路径；禁止它等于计划外多生产第 6 个错误码。视觉按 §1.7 降强度处理（`--tl-accent-strength` 45%/55%、面板虚线边框），功能与在位档完全一致。

### 6.4 文案冲突 — 按 §2.4 执行

**结论：数字全屏只出现一次，`≈ N 人` 只在数字位渲染；说明句去掉 `{n}`，只讲「估的是什么」。** `task-3` 按 §2.4 改，本规范不动。

### 6.5 仍需他人动作的事实纠错（不是裁决）

`task-3` 写「真实键族只有 `admin.settings.trustLevels.*`」。实测 `zh-CN.json`：`admin.forum.trustLevels.*`
有 **394** 个叶键，`admin.settings.trustLevels.*` 为 **`undefined`**。本规范全部按
`admin.forum.trustLevels.*` 写；`task-3` 的键表需按实际族名重出，否则前端照抄会全量 miss
（`trustLevels.test.ts` 的双向断言只覆盖错误码键，兜不住这一层）。

> **叶键数更正（2026-11-14 M5 收口复测）：243 → 394。** 本节与 §5、§7 原写的 243 **在任何已提交
> 版本都不成立**：`git show 5a1d4083f:apps/web/i18n/locales/zh-CN.json` 同法计数 = **360**
> （`rule` 76 / `conditions` 35 / `ruleConditions` 0），HEAD（`0b38458bd`）= **394**
> （`ruleConditions` 补到 42，同时 F1 删掉 8 条孤儿键：360 + 42 − 8 = 394）。计数命令：
> `cd apps/web && node -e 'const j=require("./i18n/locales/zh-CN.json");const f=(n,p="")=>n&&typeof n==="object"&&!Array.isArray(n)?Object.keys(n).flatMap(k=>f(n[k],p?p+"."+k:k)):[p];console.log(f(j.admin.forum.trustLevels).length, j.admin.settings?.trustLevels)'`
> → `394 undefined`。`en-US` 同数。

---

## 7. 事实核对（我在 HEAD 上核过的、与计划/其它任务表述不一致处）

| 项 | 计划 / 其它任务的说法 | HEAD 实测 | 证据 |
| --- | --- | --- | --- |
| 后台 i18n 键族 | `task-3`：`admin.settings.trustLevels.*` | `admin.forum.trustLevels.*`（**394** 叶键；`admin.settings.trustLevels.*` = `undefined`） | `apps/web/i18n/locales/{zh-CN,en-US}.json` 键路径枚举（命令见 §6.5 更正块） |
| 阈值 tab 每档行元素 | 计划：`fieldset` 行 | 确实是 `<fieldset>` + `<legend>`，**不是** `SFAdminTrustLevelTierRow.vue`（那是「档位」tab 的行） | `SFAdminTrustLevelThresholdsTab.vue:111-129`；`TierRow` 只在 `SFAdminTrustLevelTiersTab` 用 |
| 折叠面板落点 | 计划 §4.4：「每档行加折叠面板」 | 只能落在阈值 tab；档位 tab 的行已经有 8 个命令按钮，再加披露入口会挤爆 390 | 同上 + `TierRow:112-178` 命令组实测 |
| 档位色驱动方式 | 计划：`data-step` + `--tl-accent` | `--tl-accent` 由**祖先内联**（`tierAccentStyle`），`data-step` 只驱动 `.sf-tl-mark` 图形；自定义 hex 走 `--tl-accent-light/-dark` 由 CSS 在 `.dark` 下挑一支 | `utils/theme/tierColor.ts:102-111`；`sforum-trust-colors.css:93-99` |
| 390 内容宽 | 无明确数字 | **326px**（390 − 16×2 − 16×2），与徽章那刀实测一致 | `layouts/admin.vue:722` + 徽章 QA `clientW==326`（`2026-11-12-badge-rule-engine.md:556`） |
| 规则树每层横向占用 | 无 | **26px/层**（border 2 + padding 24） | `Group:254-257`、`Leaf:174-177` |
| 单字段最小基宽 | 无 | **140px**，且都带 `min-width:0`（可继续收缩不溢出） | `Leaf:193-199`、`ParamField:353-359` |
| 页面滚动容器位置 | — | `.sforum-admin-scroll-area`，`layouts/admin.vue:722`，`overflow-y:auto; p-4 sm:p-6` | 直接读取 |

---

## 8. 一句话交付

折叠面板塞进图例右端（24px 高、零位移）、落在数值网格之后（Tab 顺序 = 视觉顺序）、**不给 shell 基线新增 scroller / 新增 `overflow≠visible` 元素**（页面层唯一的滚动容器仍是 `layouts/admin.vue:722` 的 `.sforum-admin-scroll-area`），5 层嵌套靠 `min-width:0` + 换行消化（390 实测最内层 `clientWidth` 208 ≥ 140px 基宽，不需要横向滚动/缩进压缩/截断）；颜色从祖先 `--tl-accent` 消费、绝不内联（内联会压过 `.dark` 的明暗选择），`--sf-accent` 只保留给焦点环；影响弹窗整段复用外观弹窗的冻结几何（`sm:max-w-lg` + `min(86dvh,720px)` + body 唯一滚动 + footer 吸底），三态互斥、`null` 态全屏不出现 `≈`、`≈` 与数字同基线且数字全屏只出现一次；可访问名：本轮**新增 0 个键**（开关只挂 `aria-expanded` + `aria-controls`，弹窗无图标按钮）；保存闸门只有一条：`willWriteRule ∧ ¬catalogUsable` → 禁用并给可见原因（§6.2）。
