# 徽章视觉规范（M5 前端施工版）

Date: 2026-11-10
Author: designer（小设）
Scope: `2026-11-10-badges-and-user-activity.md` 的 M5 前端节
Status: 施工规范，可直接照此写代码

本文件只描述**视觉与几何**，不含业务逻辑。所有颜色一律引用现有 token 名，本文不复制任何色值，
避免与 `apps/web/app/assets/css/sforum-components.css`、`sforum-theme.css` 的定义漂移。
凡本文出现像素值，都是可直接写进 CSS 的数值；凡出现 token 名，都必须写成 `var(--token)`。

---

## 0. 既有几何基线（施工前先认这三条）

| 基线 | 实测来源 | 结论 |
| --- | --- | --- |
| 评论作者行 | `app/assets/css/sforum-comment.css` `.sf-comment__header`：`display:flex; flex-wrap:wrap; align-items:baseline; gap:0.45rem` | chip 必须能塞进 baseline 行且不加高行盒 |
| 评论昵称字号 | 同上 `.sf-comment__author`：`font-size:0.86rem; font-weight:800` | 桌面行盒约 17.9px，窄屏（≤680）14px → 约 18.2px |
| 昵称同行插槽 | `app/components/forum/SFUserIdentityName.vue` 的 `#name-suffix` 插槽，源码注释明确「渲染在昵称同一行……不会落到 handle 行」 | chip 唯一正确挂载点，禁止另起 flex 容器 |
| 资料页主列宽度 | `sforum-home__layout--with-right` = `230px + 1fr + 270px`，`--sf-public-edge-inset:24px` | 1633 视口下主列约 1060px；390 视口下主列 358px（`padding:20px 16px`） |
| 资料页 tab 条 | `.sf-profile-tabs`：`min-height:51px`、`border-bottom:1px solid var(--sf-public-border-strong)`、`overflow-x:auto`、滚动条隐藏 | 新 tab 只追加一项，不改容器几何 |
| 后台页头 | `pages/admin/users.vue:103-122`、`pages/admin/settings/index.vue:97-116` | 标题 `text-xl font-bold` + `size-5` accent 图标 + 描述；工具栏用 `UDashboardToolbar` |

---

## 1. tier 视觉映射（bronze / silver / gold）

### 1.1 映射表（唯一允许的写法）

| tier | 主色（图标 / 环线 / 文字标签图标） | 底色（soft fill） | 文字色 | 线型 |
| --- | --- | --- | --- | --- |
| `gold` | 亮色：`var(--sf-accent)`；暗色：`var(--sf-accent-dark)` | `var(--sf-accent-soft)` | `var(--sf-fg)` | `1px solid` + soft 填充 |
| `silver` | `var(--sf-fg-secondary)` | `var(--sf-muted)` | `var(--sf-fg)` | `1px solid`，无填充 |
| `bronze` | `var(--sf-warning)` | `var(--sf-warning-soft)` | `var(--sf-fg)` | `1px dashed`，无填充 |

暗色下的 accent 必须换成 `--sf-accent-dark`，这与 `pages/admin/users.vue:106` 既有写法一致
（`text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]`），是全站已有约定，不是为徽章新造的规则。

### 1.2 为什么不是「金属色」

本项目不新增调色板，也没有可用的金/银/铜色板。因此 tier 表达的是**强调度阶梯**，不是金属拟物：

- gold 继承站点 accent → 换主题时金档自动跟随主色，不会出现刺眼的假金色；
- silver 用中性前景色 → 三档里唯一的「无色」档，天然居中；
- bronze 用 warning 的暖色 → 是现有 token 里唯一偏土的色相，语义上最接近铜。

**这条不是妥协，是约束下的正确解**：任何硬编码金属色都会在 20 套主题（见 `sforum-theme.css`
的 `--sf-light-*` 主题组）和暗色模式下崩掉。

### 1.3 暗色模式「不糊」的五条硬规则

1. **tier 色只上图标、环线、填充，永不做正文文字色。** 卡片里「金牌 / 银牌 / 铜牌」和徽章名一律
   `var(--sf-fg)`，未获得态 `var(--sf-fg-secondary)`。色相永远不承担可读性。
2. **gold 在暗色必须走 `--sf-accent-dark`。** 暗色下 `--sf-accent` 与 `--sf-card` 对比度不足 3:1，
   画在深色卡上会糊成一团。
3. **bronze 的对比度只够图形，不够小字。** `--sf-warning` 对暗色卡面约 3.5:1：满足图形对象 ≥3:1，
   但 11px 文字达不到 4.5:1。所以 bronze 的 tier 名用 `--sf-fg-secondary` 呈现，图标才上 `--sf-warning`。
4. **同屏不叠两个 tier 色块。** 相邻两个 tier 元素之间至少有 8px 间距或 1px `var(--sf-border)` 分隔，
   禁止两个 soft 底色块直接贴边（暗色下两块 16%~20% alpha 的暖色会连成一片）。
5. **未获得态不靠降低透明度。** 用「去色 + 去填充」降级（见 3.4），不用 `opacity`——
   opacity 会连带压掉文字对比度，且与 hover 态无法区分。

### 1.4 对比度自检口径

数值取自 token 定义处的实色计算（不在本文复制色值）：

| 组合 | 对比度 | 用途判定 |
| --- | --- | --- |
| `--sf-accent-dark` 图标 / 暗色卡面 | 约 9:1 | 图标、环线、focus 都够 |
| `--sf-fg-secondary` 图标 / 明暗卡面 | ≥7:1 | silver 图标与所有 tier 名 |
| `--sf-warning` 图标 / 暗色卡面 | 约 3.5:1 | 仅图形，禁止做 11px 文字 |
| `--sf-warning` 图标 / 亮色卡面 | 约 4.6:1 | 图标与短标签均可用 |
| `--sf-fg` 正文 / 两种卡面 | ≥12:1 | tier 名与徽章名的唯一用色 |

---

## 2. `SFBadgeChip`（帖子 / 评论作者旁）

### 2.1 尺寸与形状

| 项 | 值 | 说明 |
| --- | --- | --- |
| 高度 | `18px`（`line-height:18px`） | 必须 ≤ 昵称行盒（17.9~18.2px），否则作者行会跳高 |
| 圆角 | `999px`（胶囊） | 与「楼主」方标（`border-radius:4px`）形成形状区分，见 2.4 |
| 内边距 | `0 6px 0 5px` | 图标侧收紧 1px，视觉居中 |
| 图标 | `12px`（`size-3`），不缩放 | 复用徽章自身图标（admin 选定），不是 tier 图标 |
| 图标与文字间距 | `4px` | 与 `.sf-user-identity-name__name-line` 的 `gap:5px` 叠加后仍不挤 |
| 字号 / 字重 | `11px / 700` | 与 `.sf-comment__meta`（11px/600）同档，不抢昵称 |
| 最大宽度 | 桌面 `120px`，≤480 视口 `96px` | 超出截断，不换行 |
| 截断 | `overflow:hidden; text-overflow:ellipsis; white-space:nowrap`，作用在名称 `span` 上 | chip 本体 `max-width` 兜底 |
| 外边距 | `margin:0` | 间距全部由父级 `gap` 提供，chip 不许自带 margin |

### 2.2 挂载位置与不跳动

- 唯一挂载点：`SFUserIdentityName` 的 `#name-suffix` 插槽，与昵称同基线。
- 无徽章时：`v-if="comment.topBadge"` **整块不渲染**，不留占位元素。
- 作者行**禁止**设 `min-height`、`height`、`padding-block`，也禁止给 chip 负 `margin-top` 补偿。
  18px ≤ 行盒即可零位移。
- 昵称本身已限宽（`.sf-user-identity-name__name` 有 `min-width:0`），chip 追加后由 `name-line` 的
  `max-width:100%` 接管溢出：先截 chip 文字，再截昵称。

```css
.sf-badge-chip {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  height: 18px;
  max-width: 120px;
  padding: 0 6px 0 5px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  line-height: 18px;
  text-decoration: none;
  /* tier 变体只改这三个自定义属性，不复制规则 */
  color: var(--sf-fg);
  background: var(--sf-badge-chip-bg, transparent);
  border: 1px solid var(--sf-badge-chip-ring);
}

.sf-badge-chip__icon {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  color: var(--sf-badge-chip-hue);
}

.sf-badge-chip__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-badge-chip[data-tier="gold"] {
  --sf-badge-chip-hue: var(--sf-accent);
  --sf-badge-chip-bg: var(--sf-accent-soft);
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-accent) 34%, transparent);
}

.sf-badge-chip[data-tier="silver"] {
  --sf-badge-chip-hue: var(--sf-fg-secondary);
  --sf-badge-chip-bg: transparent;
  --sf-badge-chip-ring: var(--sf-public-border-strong);
}

.sf-badge-chip[data-tier="bronze"] {
  --sf-badge-chip-hue: var(--sf-warning);
  --sf-badge-chip-bg: transparent;
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-warning) 45%, transparent);
  border-style: dashed;
}

.dark .sf-badge-chip[data-tier="gold"] {
  --sf-badge-chip-hue: var(--sf-accent-dark);
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-accent-dark) 38%, transparent);
}

.sf-badge-chip:hover,
.sf-badge-chip:focus-visible {
  border-color: var(--sf-badge-chip-hue);
}

.sf-badge-chip:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

@media (max-width: 480px) {
  .sf-badge-chip { max-width: 96px; }
}
```

`color-mix(in srgb, ...)` 是本项目既有用法（`sforum-content-semantics.css`、`sforum-theme.css`
暗色 `--sf-public-row-hover` 都在用），不是新引入的机制。

### 2.3 交互与跳转

- 点击 → `/u/{username}?tab=badges`。
- `title` 与 `aria-label` 用同一句完整文案（见 §5.1），`title` 只是鼠标补充。
- chip 是链接时用 `NuxtLink`；不可跳转的场景（如通知头像旁）退化为 `<span>`，样式不变。
- 不新增动效：hover 只改 `border-color`。

### 2.4 与「楼主」徽标共存

作者行里可能出现两个身份标记：`.sf-comment__op-badge`（4px 方角、纯文字）与 `SFBadgeChip`
（胶囊、带图标）。顺序固定为 **昵称 → 楼主 → tier chip**：chip 永远在 `#name-suffix` 内且排在
`isOpAuthor` 之后，两者间距由 `name-line` 的 `gap:5px` 提供，不加额外 margin。

### 2.5 主题页作者块

`SFTopicAuthorBlock.vue`（从 `SFTopicShowPage.vue` 抽取）使用**同一个** `SFBadgeChip`，
只传 props，不复制样式、不加局部覆盖。抽取时净减行数并按 M7 下调 `SFTopicShowPage.vue` 基线。

---

## 3. 资料页徽章 tab（`SFProfileBadgeTab`）

### 3.1 tab 位置与几何

顺序（`bookmarks` 开启时）：`topics` → `replies` → `bookmarks` → `badges` → `about` → 扩展 widget tabs。
`bookmarks` 关闭时：`topics` → `replies` → `badges` → `about` → 扩展 widget tabs。

**为什么 badges 必须在 `about` 之前**（lead 定稿口径）：`SFProfileShowPage` 的既有约定是
「内置 tab 全在『关于』之前，扩展 widget tab 一律在『关于』之后」。badges 是内置 tab，
一旦放到 about 后面就会和扩展 tab 混成一段，那条约定当场失效。所以 badges 插在 bookmarks 之后、
about 之前；**「扩展 tab 一律排在关于之后」这条约定本身保持不变**。

- `badges` tab 只加 label，**不加 `count`**：tab 内条目是「已获 + 未获」，count 会被误读成徽章总数。
- 容器几何完全沿用 `.sf-profile-tabs` / `.sf-profile-tab`，不新增 CSS。
- 数据依赖：tab 激活时取数、切走保留（与 `useProfileActivityFeeds` 同一口径）。
- **施工依赖**：当前 `SFProfileShowPage` 的 `activeTab` 是本地 ref，未与 URL 同步；chip 要能直达
  `?tab=badges`，需在初始化时读一次 `route.query.tab`（非法值回落 `topics`），不要求全量 query 同步。

### 3.2 分组标题层级

| 项 | 值 |
| --- | --- |
| 元素 | `<h2>`（与 `.sf-profile-about h2` 同级，页面不新增 h1） |
| 字号 / 字重 | `15px / 700`，`line-height:1.3` |
| 颜色 | `var(--sf-fg)` |
| 上间距 | 组间 `28px`，首组 `4px` |
| 右侧统计 | 同行的「已获 3 / 8」，`12px`，`var(--sf-fg-tertiary)`，`margin-left:auto` |
| 组间分隔 | 只靠间距，不加横线（页面已有 tab 下边框，再加线会碎） |

分组顺序按后端返回的 `position`，组内按 tier 降序（gold → silver → bronze），同级按 `position`。

### 3.3 组内网格（两档明确结论）

| 视口 / 主列宽 | 列数 | `grid-template-columns` | 单卡宽度 |
| --- | --- | --- | --- |
| 1633（主列 ≈1060px） | **4 列** | `repeat(4, minmax(0, 1fr))` | ≈246px |
| 1024–1279 | 3 列 | `repeat(3, minmax(0, 1fr))` | ≈290px |
| 768–1023 | 2 列 | `repeat(2, minmax(0, 1fr))` | ≈350px |
| 390（主列 358px） | **1 列** | `minmax(0, 1fr)` | 358px |

`gap: 12px`（行、列同值）。**390 档不硬塞两列**：358px 分两列只剩 173px，名称必然截断、
进度条只剩 150px 不到，可读性差于单列。单列时卡片改为横排（图标左、文本右），信息密度不降。

```css
.sf-badge-grid {
  display: grid;
  gap: 12px;
  grid-template-columns: minmax(0, 1fr);
  margin: 10px 0 0;
}

@media (min-width: 768px)  { .sf-badge-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (min-width: 1024px) { .sf-badge-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (min-width: 1280px) { .sf-badge-grid { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
```

### 3.4 单个徽章卡的信息密度

结构（横排，两种断点共用同一 DOM，只改方向）：

```
[ icon 32 ] 徽章名（13px/700，单行截断）
            [tier 名 11px] · 获得于 2026-11-03（11px，tertiary）
            [—— 进度条 4px ——]  3 / 10
```

| 项 | 值 |
| --- | --- |
| 卡片圆角 | `12px`（对齐 `.sf-card`） |
| 内边距 | `12px` |
| 边框 | `1px solid var(--sf-border)`；已获得且 gold 时 `border-color` 用 tier 环色 |
| 背景 | `var(--sf-card)`；gold 已获得时 `var(--sf-accent-soft)`（暗色由 token 自动降为低 alpha 暖底） |
| 图标容器 | `32×32`，`border-radius:10px`，图标 `18px`；容器底色 = tier soft，环线 = tier hue |
| 最小高度 | 有进度条 `84px`，无进度条 `64px` |
| 名称 | `13px / 700`，`var(--sf-fg)`，单行 `ellipsis` |
| tier 标签 | `11px / 600`，`var(--sf-fg-secondary)`，文本为「金牌 / 银牌 / 铜牌」 |
| 获得日期 | `11px / 400`，`var(--sf-fg-tertiary)`，格式 `YYYY-MM-DD`（`tabular-nums`） |
| 进度条 | 高 `4px`，`border-radius:999px`；轨道 `var(--sf-muted)`，填充 `var(--sf-accent)` |
| 进度文字 | `11px`，`var(--sf-fg-tertiary)`，`current / threshold`，右对齐 |
| 悬停 | 只把 `border-color` 提到 `var(--sf-public-border-strong)`；不位移、不加阴影 |

**进度条颜色统一用 accent，不按 tier 变色。** 进度是系统信息，不是身份标记；三档各自换色会让
一屏出现三种进度条，且暗色下 bronze 填充对轨道对比不足。

### 3.5 未获得状态的视觉降级

| 项 | 已获得 | 未获得 |
| --- | --- | --- |
| 图标容器底 | tier soft | `transparent` |
| 图标容器环 | tier hue | `var(--sf-border)` |
| 图标颜色 | tier hue | `var(--sf-fg-tertiary)` |
| 名称颜色 | `var(--sf-fg)` | `var(--sf-fg-secondary)` |
| 右上角标记 | 无 | 「未获得」`11px`，`var(--sf-fg-tertiary)` |
| 进度条 | 不显示（已达成） | 显示 |
| 整体透明度 | 100% | 100%（**禁止用 opacity 降级**） |

只有「自动规则徽章」才有未获得态；手动授予类徽章在资料页**不出现**未获得卡（未授予即不返回）。

### 3.6 空态、加载态、开关关闭

- 空态：`<p>` 一句「还没有获得徽章」，`13px`，`var(--sf-fg-secondary)`，上下 `32px` 留白；不画插画。
- 加载态：3 张骨架卡（`height:64px`，`background:var(--sf-muted)`，`border-radius:12px`），
  不使用 spinner 占据整块。
- `features.badges` 关闭：tab 与 chip 整体消失，**统计数字照常显示**；不做「已关闭」占位文案。

### 3.7 活动统计条（同页，不属于徽章产品面）

**5 项定稿**：主题数 / 回复数 / 分类数 / 获赞数 / 访问天数。

- 前三项是**已上线的公开数字**（`SFProfileFeedTab.vue:56-69` 的 `topicCount` / `commentCount` /
  `categoryCount`），本刀只加不减——删掉任何一个都是回退既有产品面，不在「加徽章」的授权范围内。
- 给赞数与收藏数**不公开展示**：它们是行为计数，公开价值低，还多一层窥探面；这两个数只在徽章进度里用。

**两种取数口径，删内容后的行为不同，这不是 bug：**

| 指标 | 取数来源 | 用户删掉自己的主题 / 评论后 |
| --- | --- | --- |
| 主题数 | Profile 实时 `COUNT` | 数字立刻变小 |
| 回复数 | Profile 实时 `COUNT` | 数字立刻变小 |
| 分类数 | Profile 实时 `COUNT` | 数字立刻变小（分类里没有内容时不再计入） |
| 获赞数 | `user_activity` 快照 | **不变**（快照只做增量，不回溯历史） |
| 访问天数 | `user_activity` 快照 | 不变 |

前三项是「现在有什么」，后两项是「累计发生过什么」。**禁止**为了「看起来一致」把前三项也改成快照，
也**禁止**在 UI 上加「数据可能延迟」之类的免责文案——两种语义本来就不同，加提示反而暗示系统有错。

布局：复用 `.sf-profile-summary` 的单元格与边框（`min-height:66px`、`padding:11px 13px`、
`border-top/left` + 每格 `border-right/bottom`），**只改列数与末项跨列**，新增一个修饰类：

```css
/* 5 项版本：现有 .sf-profile-summary 固定 3 列，5 项会落单两格、边框出现半格缺口 */
.sf-profile-summary--quint {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

/* 末项跨两列，2 列布局下不会留下一个空格子 */
.sf-profile-summary--quint > :last-child {
  grid-column: span 2;
}

@media (min-width: 768px) {
  .sf-profile-summary--quint {
    grid-template-columns: repeat(5, minmax(0, 1fr));
  }

  .sf-profile-summary--quint > :last-child {
    grid-column: auto;
  }
}
```

**落位要求（不是风格建议，是生效条件）**：这段必须追加在 `sforum-profile.css` **文件末尾**。
该文件在 `@media (max-width: 680px)` 里已有一条 `.sf-profile-summary { grid-template-columns:
repeat(3, minmax(0, 1fr)); }`（约 674 行），与修饰类同权重，同权重靠源码顺序决胜负——
写在那条之前会被 680 断点覆盖回 3 列。

| 视口 | 列数 | 结果 | 单格宽度 |
| --- | --- | --- | --- |
| 1633 | 5 列 | 一行 5 格 | 约 212px |
| 768–1279 | 5 列 | 一行 5 格 | 约 144px（主列最窄时） |
| 390 | 2 列 | 三行：2 + 2 + 1（末项跨满） | 约 179px |

- 数字 `18px/800`、`tabular-nums`，标签 `11px`、`var(--sf-public-text-muted)`——全部沿用既有
  `.sf-profile-summary__item strong/span`，**不缩字号**。
- `min-height:66px` 保持不动。390 档宁可变成三行（约 198px 高），也不靠压字号或压行高塞进两行。
- 「访问天数」11px 约 44px，最窄 144px 单格下不换行。
- 2 列 + 末项 `span 2` 的组合没有空单元格，容器 `border-top/left` 与每格 `border-right/bottom`
  仍然闭合，不出现半格缺口。

---

## 4. 后台徽章列表页（`pages/admin/badges.vue`）

### 4.1 页头与工具栏（照既有契约，逐项对齐）

| 元素 | 规格 | 参照 |
| --- | --- | --- |
| 页头容器 | `mb-4 flex flex-wrap items-start justify-between gap-3` | `users.vue:103` |
| 标题 | `text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100` | `users.vue:105` |
| 标题图标 | `UIcon size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]`，用 `adminPage.icon` | `users.vue:106` |
| 描述 | `mt-1 text-sm text-slate-500 dark:text-zinc-400` | `users.vue:109` |
| 主操作 | `UButton color="primary" icon="i-lucide-plus" class="self-start"`（`badge.manage` 才渲染） | `users.vue:113` |
| 工具栏 | `UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400"` | `settings/index.vue:104` |
| 工具栏左 | `i-lucide-award` + 一句 intro，`text-sm`，`truncate` | `settings/index.vue:107` |
| 工具栏右 | 刷新（`color="neutral" variant="outline"`）+ 新建 | 同上 |
| 表格 | `UTable sticky class="max-h-[calc(100vh-20rem)]"` | `SFAdminUserTable.vue:88` |

**禁止**：营销式大标题、页头再套一张卡、第二套 tab 系统、与 `settings/index.vue` 不同的工具栏类串。

### 4.2 列宽与内容

| 列 | 宽度建议 | 内容 |
| --- | --- | --- |
| 图标 | `48px` | `32×32` 圆角 8 容器 + 18px 图标，tier soft 底 |
| 名称 | `min-w-[180px]` | 徽章名 `font-semibold` + 第二行 `slug`（`code text-xs`） |
| 分组 | `100px` | 纯文本 |
| tier | `88px` | 复用 `SFBadgeChip` 的 tier 视觉，**admin 变体**：无链接、无跳转、`height:18px` |
| 授予方式 | `96px` | 「自动」/「手动」，用 `UBadge color="neutral" variant="soft"` |
| 规则 | `min-w-[160px]` | `metric ≥ threshold`，`tabular-nums`；手动徽章显示 `—` |
| 已授予 | `96px` | 右对齐，`tabular-nums` |
| 启用 | `72px` | `USwitch`，`aria-label` 带徽章名 |
| 操作 | `96px` | 编辑 / 授予 / 停用（图标按钮，带 `aria-label` + `title`） |

tier 列不允许只用颜色：必须同时有 tier 文字（「金牌」）。表格里的 tier 视觉是 chip 的**简化变体**：
保留 hue + 文字，去掉背景填充，避免一列彩色块。

### 4.3 窄屏折叠策略（390 不横向滚动）

- 用 `UTable` 的列 `meta.class.td/th`（Nuxt UI 4 已支持，`Table.vue` 通过 `cell.column.columnDef.meta?.class?.td` 解析）
  逐列收起，而不是让表格横向滚动：

| 列 | ≤640 | 641–1023 |
| --- | --- | --- |
| 图标 | 保留 | 保留 |
| 名称 | 保留 | 保留 |
| 分组 | `hidden` | 保留 |
| tier | 保留 | 保留 |
| 授予方式 | `hidden` | 保留 |
| 规则 | `hidden` | `hidden` |
| 已授予 | `hidden` | 保留 |
| 启用 | 保留 | 保留 |
| 操作 | 保留（仅图标） | 保留 |

- 收起的信息不丢：行点击打开编辑弹窗，弹窗内完整呈现规则与授予方式。
- 操作按钮窄屏只留图标，但必须保留 `aria-label` 与 `title`（AGENTS.md 的工具栏约定）。
- 分页条沿用 `users.vue` 的 `mt-4 flex flex-col gap-3 border-t ... sm:flex-row`。

### 4.4 编辑弹窗几何

- `UModal`，桌面 `max-w-xl`，390 下全宽。
- 字段顺序：`slug` → 名称 → 描述 → 分组 → tier → 图标 → 授予方式 → `metric` + `threshold`。
- tier 选择：三个并排 radio-card（不是下拉），每张显示 tier 图标 + 名称 + 环色，选中态 `border-color` 用 tier hue；
  390 下改为纵向堆叠。
- 图标选择：从下面的**准入白名单**挑，网格 8 列 / 390 下 6 列，每格 `40×40`，选中态环色用 tier hue。

**准入规则（明文约束，不是一次性观察）：**

1. **不许用编码「等级」的图标**：`crown`、`medal`、`award`、`rank` 系一律禁止。等级只由 chip 的
   环线 / 填充 / 线型表达，图标只讲徽章自身语义。同一个徽章上出现两种等级编码，用户不知道信哪个。
2. **12px 可辨识**：`<path>` 子路径 ≤3 优先；超过 3 的必须是闭合高对比剪影（如 `hourglass` 4、
   `trophy` 6）。内部细节渲染到 12px 后低于 2px 的图标直接淘汰。
3. **不许与站内既有语义冲突**：`flag`（全站=举报，5 处）、`sparkles`（全站=推荐/AI 装饰，20 处）、
   `heart`（全站=点赞控件）不得作为徽章图标。
4. **同组不重复、跨组不歧义**：12 个内置徽章零重复；运营新增徽章必须先在本表里查重。

**12 个内置徽章定稿（slug → icon）：**

| 组 | slug | icon | 备注 |
| --- | --- | --- | --- |
| 活跃 | `regular-visitor` | `i-lucide-calendar` | 原 `calendar-check` 内部勾在 12px 糊成点 |
| 活跃 | `resident` | `i-lucide-house` | 全组最贴切，保留 |
| 活跃 | `veteran` | `i-lucide-hourglass` | 原 `crown` 编码等级，见下 |
| 内容 | `first-topic` | `i-lucide-sprout` | 原 `pen-line` 是笔+下划线两根细线，12px 连成一条 |
| 内容 | `topic-author` | `i-lucide-square-pen` | 原 `pen-tool` 是贝塞尔工具，414 字符 + 语义跑偏 |
| 内容 | `discussant` | `i-lucide-message-circle` | 原 `messages-square` 与 `square-pen` 方框族撞形 |
| 内容 | `collector` | `i-lucide-bookmark` | 与站内「收藏」动作同形，保留 |
| 内容 | `quality-answer` | `i-lucide-circle-check-big` | 圆+粗勾，12px 干净，保留 |
| 荣誉 | `rising-star` | `i-lucide-star` | 单子路径闭合五角星，保留 |
| 荣誉 | `well-received` | `i-lucide-thumbs-up` | 原 `sparkles` 与推荐/AI 语言冲突，见下 |
| 荣誉 | `widely-acclaimed` | `i-lucide-trophy` | 全组唯一「最高荣誉」剪影，保留 |
| 荣誉 | `community-contribution` | `i-lucide-users` | 原 `heart-handshake` 450 字符连续轮廓，12px 成色块 |

**两处争议项已定稿，理由留档：**

- `veteran` → `hourglass`，不用 `clock` / `tree-pine`：元老的核心语义是**时间沉淀**，沙漏最贴。
  「倒计时」的联想不构成否决理由——那是任务系统的心智模型，不在成就徽章语境里。`clock` 会被读成
  「最近活跃 / 在线」；`tree-pine` 的三个三角堆叠在 12px 边缘不如沙漏两个高对比三角稳。
- `well-received` → `thumbs-up`，不用 `heart`：**避免交互歧义优先于语义贴合**。本站点赞控件就是
  `heart`，徽章再用 `heart` 会让用户伸手去点；徽章是「成就」不是「动作」，这个区分比「更贴产品语言」
  重要。`thumbs-up` 是「他人认可」的通用符号，不绑定任何具体交互。

**准入规则第 1 条的由来**：换掉 `crown` 之后，12 个图标里没有任何一个编码「等级」——等级只由 chip
的环线 / 填充 / 线型表达，图标只讲徽章自身语义。这不是一次性观察，而是上表四条准入规则里第 1 条的
依据：以后运营加新徽章，`crown` / `medal` / `award` / `rank` 系图标明文不许用。
- 保存成功弹 Toast（5s 自动消失）；错误 Toast 不自动消失，字段级错误留在字段旁。

---

## 5. 无障碍

### 5.1 `aria-label` 完整文案模板（原文，直接进 i18n）

`SFBadgeChip`：

```
zh-CN: "{tier}徽章：{badgeName}"
       例：「金牌徽章：众望所归」
en-US: "{tier} badge: {badgeName}"
       e.g. "Gold badge: Voice of the Community"
```

- 拼接键：`badges.chip.ariaLabel` = `"{tier}徽章：{name}"`；`badges.tier.gold|silver|bronze` = `金牌|银牌|铜牌`。
- 链接型 chip 的 `aria-label` 末尾追加动作：「，查看资料页徽章」（`badges.chip.ariaLabelLink`），
  避免与纯展示 chip 同文案不同行为。
- `title` 用同一句，不加句号。

徽章卡（`SFProfileBadgeTab` 内每张卡）：

```
已获得 zh-CN: "{tier}徽章：{badgeName}，获得于 {date}"
未获得 zh-CN: "{tier}徽章：{badgeName}，未获得，进度 {current}/{threshold}"
en-US:        "{tier} badge: {badgeName}, earned on {date}"
              "{tier} badge: {badgeName}, not earned, progress {current}/{threshold}"
```

后台 tier 列、启停开关：

```
开关: "启用徽章：{badgeName}"
行操作: "编辑徽章：{badgeName}" / "授予徽章：{badgeName}"
```

#### 5.1.1 冻结的 DOM 契约（M8 用 grep 验收，属性名与取值逐字一致）

| 元素 | 必需属性 | 取值 |
| --- | --- | --- |
| `SFBadgeChip` 根节点 | `data-badge-slug` | 徽章 slug 原文，如 `well-received` |
| `SFBadgeChip` 根节点 | `data-tier` | `gold` / `silver` / `bronze`（§2.2 的 CSS 选择器依赖它） |
| 资料页徽章卡根节点 | `data-badge-slug` | 同上 |
| 资料页徽章卡根节点 | `data-earned` | 已获得 `"true"`，未获得 **`"false"`** |
| 资料页徽章卡根节点 | `data-tier` | 同上 |

- `data-earned` 未获得时必须**显式写 `"false"`**，不许用省略属性来表达：M8 会 grep
  `data-earned="false"` 确认降级态存在，省略即判失败。
- `data-badge-slug` 取 slug 而不是名称：名称随 i18n 变，slug 是把帖子 chip 与资料页卡片对齐的稳定键。
- 这些属性只作测试与样式钩子，**不参与 `aria-label`**；无障碍文案仍由上面的模板生成，两者不许互相替代。

### 5.2 tier 不能只靠颜色

三处冗余，缺一不可：

1. **文字**：卡片里有「金牌 / 银牌 / 铜牌」；后台 tier 列同样有文字。
2. **线型**：gold `solid + soft 填充`、silver `solid 无填充`、bronze `dashed 无填充`。
3. **语义**：`aria-label` 与 `title` 都含 tier 名。

chip 因为宽度受限只承载 1 项视觉冗余（线型），所以 chip 必须带完整 `aria-label`，
且**禁止**把 chip 做成纯色圆点。

### 5.3 进度条语义

优先用 `UProgress`（`size="xs"`、`color="primary"`、`:model-value="percent"`），组件自带
`role="progressbar"`。若手写轨道，必须补全：

```html
<div role="progressbar" aria-valuemin="0" aria-valuemax="10" aria-valuenow="3"
     aria-valuetext="已发布 3 个主题，共需 10 个" aria-label="主题徽章进度"></div>
```

- `aria-valuemax` 用 `threshold`，不是 100；`aria-valuetext` 用人话，不用百分比。
- 进度条的 `aria-label` 是徽章名 + 「进度」，不重复卡片已有文案的完整句。

### 5.4 焦点与键盘

- 焦点环：`outline: 2px solid var(--sf-accent); outline-offset: 2px`（chip / 卡片链接 / 表格图标按钮统一）。
- 卡片可点区域是整个卡片（`NuxtLink` 包整卡），`<a>` 内不得嵌套 `<a>` 或 `<button>`。
- 后台表格行操作用图标按钮时，`aria-label` 必填；不用 `title` 代替 `aria-label`。

---

## 6. 反面清单（出现即打回）

1. **emoji 当图标**：奖牌、奖杯、星星一类的表情符号一律禁止，包括 `aria-label`、`title` 与占位文案。
2. **手写 inline SVG**：徽章图标只走 `UIcon` + `i-lucide-*` 白名单。
3. **新引入图标包**：只允许 `@iconify-json/lucide` 与 `@iconify-json/tabler`（已在依赖内），
   且徽章图标白名单只用 lucide。
4. **十六进制裸值 / rgb 字面量**：颜色一律 `var(--token)`；需要透明度时用
   `color-mix(in srgb, var(--token) N%, transparent)`，不写把 alpha 通道手写进函数的那种字面量。
5. **新增调色板**：不许在 `sforum-components.css` 加 `--sf-gold` / `--sf-silver` / `--sf-bronze`。
6. **荧光色 / 高饱和自发光**：不许用高亮青绿、纯黄、亮紫做 tier；不许加 `box-shadow` 光晕。
7. **opacity 降级**：未获得态禁止 `opacity:.5`。
8. **作者行跳高**：禁止给 chip 加 `margin-top:-2px` 之类的补偿，禁止给作者行设固定 `height`。
9. **同一页面两套几何**：后台徽章页不许自造页头/工具栏，资料页徽章 tab 不许自造 tab 条。
10. **为 tier 换进度条颜色**：进度条永远 accent。
11. **390 靠横向滚动解决**：任何新页面出现 `overflow-x` 滚动条即失败。
12. **动效堆砌**：不加位移、缩放、渐变扫光、骨架微光。

---

## 7. 变量清单（本文只允许引用这些）

| 用途 | token |
| --- | --- |
| 主色 / 金档亮色 | `--sf-accent`、`--sf-accent-dark`、`--sf-accent-soft`、`--sf-accent-focus` |
| 铜档 | `--sf-warning`、`--sf-warning-soft` |
| 银档 / 次要文字 | `--sf-fg-secondary`、`--sf-fg-tertiary`、`--sf-muted` |
| 正文 | `--sf-fg`、`--sf-public-text`、`--sf-public-text-secondary`、`--sf-public-text-muted` |
| 卡面 / 边框 | `--sf-card`、`--sf-surface`、`--sf-border`、`--sf-border-light`、`--sf-public-border`、`--sf-public-border-strong` |
| 半径 | `--sf-public-radius`（4px，用于「楼主」方标，chip 不用） |
| 断点常量 | `--sf-public-edge-inset`、`--sf-public-sidebar-width`、`--sf-public-right-rail-width` |

未在此表内的 token 视为未批准使用；需要新 token 时先改本文件，再写代码。

---

## 8. i18n 键（避免与既有 `topicDetail.badge.*` 冲突）

已存在 `topicDetail.badge.pinned/locked/featured`（话题状态），因此新命名空间一律用**复数 `badges`**：

```
badges.tier.gold / silver / bronze
badges.chip.ariaLabel / ariaLabelLink
badges.card.ariaLabel / ariaLabelUngranted
badges.card.earnedOn / notEarned / progress
badges.group.count            "{earned} / {total}"
badges.empty
profile.tabs.badges
admin.badges.*（title / intro / columns.* / actions.* / form.* / grant.* / toast.*）
```

---

## 9. 施工检查表（前端工程师自查）

- [ ] 全仓新增 CSS 无十六进制、无 `rgb(`/`rgba(` 字面量，只有 `var(--token)` 与 `color-mix`。
- [ ] `sforum-components.css` 未新增任何 `--sf-*` 颜色变量。
- [ ] chip 高度 18px，挂在 `#name-suffix`，有/无徽章时作者行高度差为 0（1633 与 390 各验一次）。
- [ ] chip 最大宽 120px / ≤480 时 96px，长名称出省略号，不换行、不撑破作者行。
- [ ] 资料页 1633 → 4 列、390 → 1 列；无横向溢出。
- [ ] 未获得态无 `opacity`，tier 名用 `--sf-fg-secondary`。
- [ ] 进度条有 `role="progressbar"` 或 `UProgress`，`aria-valuemax = threshold`。
- [ ] 后台页头/工具栏类串与 `users.vue`、`settings/index.vue` 逐字一致。
- [ ] 每个 chip、每张卡、每个图标按钮都有完整 `aria-label`。
- [ ] chip 与徽章卡都带 `data-badge-slug` + `data-tier`；未获得卡显式写 `data-earned="false"`（M8 grep）。
- [ ] 统计条是 5 项（主题 / 回复 / 分类 / 获赞 / 访问天数），用 `.sf-profile-summary--quint`；
      该规则追加在 `sforum-profile.css` 末尾，且 390 档为三行、末项跨满两列。
- [ ] 内置徽章图标与 §4.4 定稿表逐字一致，且未使用 `crown` / `medal` / `award` 等编码等级的图标。
- [ ] 暗色模式下 gold 用 `--sf-accent-dark`，无低对比文字。
