# 信任等级视觉规范（M5 前端施工版 · 冻结 DOM 契约）

Date: 2026-11-10
Author: designer（小设）
Scope: `plans/2026-11-10-trust-levels.md` §5 能力映射表 + §6 M5 前端节
Status: **冻结**。本文件中的 class 名、`data-*` 属性、`aria-*` 取值规则即契约：
小前照此实现，小审照此 grep 断言。改契约先改本文件。

本文件只描述视觉、几何与可访问性契约，不含业务逻辑、不含组件实现代码。
颜色一律引用既有 token 名（`var(--token)`），本文不复制色值 —— 避免与
`apps/web/app/assets/css/sforum-components.css`、`sforum-theme.css` 漂移。
凡出现像素/字号数值，都是可直接写进 CSS 的定稿值。

---

## 0. 既有几何基线（施工前先认这六条，全部实测）

| 基线 | 实测来源 | 对本刀的含义 |
| --- | --- | --- |
| 资料页右栏宽度 | `sforum-home__layout--with-right` = `230px + 1fr + 270px`；≤390 视口右栏收进 `SFProfileRightRail` 的 `drawer` | 等级卡落在右栏，**可用内容宽 270px**（drawer 态为满宽），所有尺寸按 270px 设计 |
| 右栏分区结构 | `SFProfileRightRail.vue:48-67`：`section.sf-profile-side-section` + `header.sf-profile-side-section__head > h3 + span` | 等级卡是**第五个 side-section**，不新造分区壳 |
| 身份区位置 | 同上，`sf-profile-side-identity` 是第一个 section | 等级卡插在身份区**之后**、`profile.publicDetails` **之前** |
| 后台设置页几何 | `pages/admin/settings/index.vue`：`text-xl` 标题 + `UDashboardToolbar` + `SFAdminFixedTabNav` + `KeepAlive` 单活动面板 | 阈值页是**既有 tab 数组里的一项**，不是新页面 |
| 固定 tab 组件契约 | `components/admin/settings/shared/SFAdminFixedTabNav.vue`：`items[{id,label,icon,disabled?}]` + `modelValue` + `ariaLabel`，内部 `role="tablist"` | 只追加一项，不传第二套 tab 系统 |
| 同域表单先例 | `SFAdminSiteNewcomersTab.vue`：`UCard`（header 标题 + `UBadge font-mono` 键前缀）→ `UAlert` + 恢复默认按钮同行 → `UFormField` 网格 → `SFAdminFormFooter`（sticky） | 阈值 tab **逐项复用**这个骨架，不自创表单几何 |
| 日间预设 | `sforum-theme.css:157-260`，12 个 `[data-sforum-light-background]`：`pure_white` / `porcelain` / `paper` / `parchment` / `mist_gray` / `cool_frost` / `cloud_blue` / `mint_mist` / `sage` / `sakura` / `lilac_mist` / `morning_apricot` | 只换 `--sf-light-*` 背景/边框族，**不换 `--sf-accent`**；等级阶梯必须不依赖背景色 |
| 后台配色约定 | 后台一律 `slate-*` / `zinc-*` Tailwind 类串（见 `newcomers` / `settings/index.vue`） | 后台阈值页**不用** `--sf-*` 公开页 token；公开页等级卡**不用** slate 类串 |

> 一条硬纪律：公开页用 `var(--sf-*)`，后台页用 slate/zinc 类串。混用会在 12 个日间预设下露馅。

---

## 1. 资料页等级卡 `SFTrustLevelCard`

### 1.1 落位（先定位置，再谈样式）

- 组件：`apps/web/app/components/profile/SFTrustLevelCard.vue`，在 `SFProfileRightRail.vue` 内渲染。
- 位置：**身份区（`sf-profile-side-identity`）之后、`profile.publicDetails` 之前**。
- 理由：等级是身份元数据，不是 tab 内容。放右栏意味着在 `topics` / `replies` / `about` / `badges`
  每个 tab 下都可见；放徽章 tab 会让「等级」被误读成「徽章的一种」，而 D10/§5 明确两者是不同产品面
  （徽章只展示，等级给能力）。
- **不新增资料页 tab**。`SFProfileShowPage` 的 tab 条（`bookmarks → badges → about`）本刀零改动。
- 数据依赖：公开等级查询（M4）。取数在 `useTrustLevel()`，卡片只渲染。
- `features.trust_levels` 关闭时：**整卡不渲染**（`v-if` 卸载），不留占位、不显示「已关闭」。
  理由同徽章：给一个永远关闭的面板写占位文案，等于把运营开关变成用户可见的故障。

### 1.2 结构（DOM 树，逐字照此）

```
section.sf-profile-side-section.sf-trust-level-card   [data-level][data-earned]
├─ header.sf-profile-side-section__head
│  ├─ h3                                  → profile.trustLevel.title（「信任等级」）
│  └─ span.sf-trust-level-card__code      → "TL2"（tabular-nums）
├─ div.sf-trust-level-card__main
│  ├─ span.sf-trust-level-card__icon      [aria-hidden="true"] → UIcon 档位图标
│  └─ div.sf-trust-level-card__text
│     ├─ strong.sf-trust-level-card__name → 档位名「常驻用户」
│     └─ p.sf-trust-level-card__hint      → 副文案（一行，截断）
├─ div.sf-trust-level-card__progress      （TL4 时整块不渲染）
│  ├─ div.sf-trust-level-card__track      [role="progressbar"][aria-*]
│  │  └─ span.sf-trust-level-card__fill   [style.width]
│  └─ span.sf-trust-level-card__progress-text → profile.trustLevel.progress.text（"48 / 60"）
└─ ol.sf-trust-level-card__rungs          [aria-hidden="true"]
   └─ li.sf-trust-level-card__rung ×5     [data-level][data-earned]
```

**标题层级**：`h3` 是右栏分区标题（沿用 `.sf-profile-side-section__head h3` 既有样式，不改），
`strong.sf-trust-level-card__name` 是**值**不是分区标题，**禁止**再起 `h4`。
一个右栏 section 内出现两级 heading 会让屏幕阅读器的「跳转标题」导航把「常驻用户」当成同级分区。

### 1.3 冻结的 DOM 契约（M8 grep 验收，属性名与取值逐字一致）

| 元素 | 必需属性 | 取值规则 |
| --- | --- | --- |
| 卡片根 `section.sf-trust-level-card` | `data-level` | 当前等级 `"0"`–`"4"`，**十进制字符串**，非零填充 |
| 卡片根 `section.sf-trust-level-card` | `data-earned` | 等级 ≥1 → `"true"`；等级 `0` → **显式写 `"false"`**（降级态，不是错误态） |
| 档位刻度 `li.sf-trust-level-card__rung` ×5 | `data-level` | 该格代表的档位 `"0"`–`"4"`，**五个节点固定渲染，顺序 0→4** |
| 档位刻度 `li.sf-trust-level-card__rung` ×5 | `data-earned` | `rung.level <= card.data-level` → `"true"`，否则 **显式 `"false"`** |
| 进度条 `div.sf-trust-level-card__track` | `role` | 恒为 `"progressbar"` |
| 同上 | `aria-valuemin` | 恒为 `"0"` |
| 同上 | `aria-valuemax` | 见 §1.4，**是阈值不是 100** |
| 同上 | `aria-valuenow` | 见 §1.4，**必须 clamp 到 `[0, valuemax]`** |
| 同上 | `aria-valuetext` | 见 §1.4 模板原文，进 i18n |
| 同上 | `aria-label` | `profile.trustLevel.progress.ariaLabel`，含「当前档 → 下一档」，不含数字 |

- `data-earned="false"` 必须**显式写出**，不许用省略属性表达：M8 会 grep `data-earned="false"` 确认
  降级态存在（TL0 用户命中卡片根 + 5 个刻度；TL2 用户命中 3 个刻度）。省略即判失败。
- `data-level` 取数字字符串而不是档位名：档位名随 i18n 变，数字是把卡片、刻度、后台行、options 键
  （`trust.level.2.*`）对齐的稳定键。
- 这些属性只作测试与样式钩子，**不参与 `aria-label`**；无障碍文案仍由 §5 模板生成，两者不许互相替代。

### 1.4 进度条取值规则（唯一算法，小审按此写断言）

设当前等级 `L`，下一档 `N = L + 1`。

1. **`L == 4`（最高档）**：`div.sf-trust-level-card__progress` **整块不渲染**；渲染
   `<p class="sf-trust-level-card__maxed">`（`profile.trustLevel.maxed` =「已达最高等级」，
   11px、`var(--sf-fg-tertiary)`、**`margin:10px 0 0`**）。`aria-valuemax` 等属性随之不存在 —— M8 断言
   「TL4 页面上 `role="progressbar"` 计数为 0」，而不是断言某个值。
   **margin 不能是 0**（原稿疏漏，已改）：`__maxed` 紧跟 `__name` 时零间距会让读屏与视觉都把「领袖已达最高等级」
   读成同一段文本；10px 与 `__progress` 的 `margin-top` 同值，替换进度块时卡片高度不变、不跳动。
2. **瓶颈 metric 选取**：从「`N` 档判定用到的 metric」中，排除 `threshold <= 0`（阈值为 0 = 该 metric
   不参与本档判定）后，取 `current / threshold` **最小**者；若并列取 metric 声明顺序靠前者。
2b. **边缘态：`L < 4` 且 `N` 档所有 `threshold <= 0`（即 `valuemax` 无解）**：**不渲染进度块**
   （`div.sf-trust-level-card__progress` 不存在），**同时不渲染 `__maxed`、不渲染 `__hint`** ——
   卡内只剩 `h3` + `__code` + `__name` + 5 段刻度。
   **不新增文案、不猜原因**（不许写「已达最高等级」，那是错的；也不许写「继续活跃即可升级」，那是编的）。
   这个状态实际只在「新人期上限闸把等级压在 TL1」时出现（活动量够但账龄不够）。等 M4 暴露「被上限闸压住」
   的信号后，再用 `nextHint` 写「新人期结束后可继续升级」；在那之前这句**不存在**。
3. `aria-valuemax` = 瓶颈 metric 的 `threshold`（整数，字符串化）。
4. `aria-valuenow` = `min(current, threshold)`，且 `max(0, …)`。**必须 clamp**：
   `valuenow > valuemax` 是 a11y 违规，会同时被 axe 与浏览器读屏判为错误状态。
5. `aria-valuetext` = `profile.trustLevel.progress.valueText`，参数 `{metric, current, threshold}`：
   - zh-CN：`"获赞 48 / 60"`
   - en-US：`"Likes received 48 / 60"`
   其中 `metric` 是**指标名的人话**（§8 的 `profile.trustLevel.metric.*`），不是 options 键名。
6. `aria-label` = `profile.trustLevel.progress.ariaLabel`，参数 `{current, next}`：
   - zh-CN：`"信任等级进度：常驻用户 → 资深用户"`；en-US：`"Trust level progress: Resident → Veteran"`
   - 不含数字：数字在 `aria-valuetext` 里，重复播报会变成「信任等级进度：常驻用户到资深用户，获赞48/60」两遍数字。
7. 填充宽度 = `min(100, round(current / threshold * 100))` %，与 `valuenow` **独立计算**
   （`valuenow` 走 clamp，宽度走百分比，两者不互相派生）。
8. 副文案 `p.sf-trust-level-card__hint` 与进度条**必须用同一个瓶颈 metric**，不得各说一项：
   - 未达下一档：`profile.trustLevel.nextHint` =「再获 {count} 个{metric}可升到「{next}」」
     （`count = threshold - current`）
   - 已达最高档：不渲染 `hint`，由 `maxed` 取代
   - 副文案**恒为一行**（`white-space:nowrap` + `text-overflow:ellipsis`），270px 下不换行撑高卡片。

### 1.5 尺寸与字级（右栏 270px 设计基准）

| 项 | 值 | 说明 |
| --- | --- | --- |
| 卡片外边距 | `margin:0` | 间距全部由 `.sf-profile-side-section` 既有 `+` 规则提供，不自带 |
| `__main` 布局 | `display:flex; align-items:center; gap:10px` | 图标左、文本右 |
| `__icon` | `28×28`，`border-radius:9px`，图标 `16×16` | 比徽章卡的 32/18 小一档：右栏比主列窄，32 会挤压档位名 |
| `__text` | `min-width:0; display:flex; flex-direction:column; gap:2px` | `min-width:0` 是省略号生效条件 |
| `__name` 档位名 | `14px / 700`，`var(--sf-fg)`，单行 ellipsis | 卡内最高字重，比 hint 高两级 |
| `__hint` 副文案 | `11px / 400`，`line-height:1.4`，`var(--sf-fg-secondary)`，单行 ellipsis | 不进 tertiary：它是行动指引，不是元数据 |
| `__code`（TL 编号） | `11px / 600`，`var(--sf-fg-tertiary)`，`tabular-nums` | 与 `h3` 同行，`margin-left:auto` |
| `__progress` | `display:flex; align-items:center; gap:8px; margin-top:10px` | 与徽章卡进度条同一布局 |
| `__track` | `flex:1 1 auto; height:6px; border-radius:999px; background:var(--sf-muted); overflow:hidden` | 6px（徽章卡 4px）：右栏只有一条进度，可以稍厚 |
| `__fill` | `height:100%; border-radius:999px; background:var(--sf-accent)` | 进度永远 accent，**不按档位换色** |
| `__progress-text` | `11px`，`var(--sf-fg-tertiary)`，`tabular-nums`，`flex:0 0 auto` | 与徽章卡同档 |
| `__rungs` | `display:grid; grid-template-columns:repeat(5, minmax(0,1fr)); gap:3px; margin:8px 0 0; padding:0; list-style:none` | 5 格等宽 |
| `__rung` | `height:3px; border-radius:2px` | 3px 是「刻度」不是「进度」：比进度条细，避免两条同权重横线打架 |
| `__rung[data-earned="true"]` | `background:var(--sf-accent)` | 已走过的档位 |
| `__rung[data-earned="false"]` | `background:color-mix(in srgb, var(--sf-fg) 16%, transparent)` | 见 §2.4：不用 `--sf-muted`，在 `paper`/`parchment` 下 muted 与卡面几乎同色 |
| 悬停 | 无任何变化 | 卡片不可点，不做 hover |

**刻度只回答「走过几档」，不再标记当前档。** 当前档由 `__code`（TL2）与 `__name`（常驻用户）表达；
若刻度再给当前格加一圈描边，同一信息就有两套视觉语法，且 `data-earned` 的布尔语义会被第三种状态污染。

### 1.6 四种状态

| 状态 | 表现 |
| --- | --- |
| 正常（TL1–TL4） | 见 §1.5；`data-earned="true"` |
| **TL0 新用户** | `data-earned="false"`；`__icon` 环线 `1px dashed var(--sf-border)`、无填充、图标 `var(--sf-fg-tertiary)`；`__name` 用 `var(--sf-fg-secondary)`；5 个刻度全 `false`；进度条**照常渲染**（TL0 有下一档，最需要进度） |
| TL4 最高档 | 无进度块，`__maxed` 一行（`margin:10px 0 0`）；5 个刻度全 `true` |
| **边缘态：`L<4` 且下一档全阈值 0** | 无进度块、**无 `__maxed`、无 `__hint`**；刻度与档位名照常。不猜原因、不编文案（§1.4-2b） |
| 加载 / 失败 | 加载：整卡不渲染（右栏其余 section 照常，不插骨架 —— 右栏插骨架会让整栏在首屏抖一次）。失败：不渲染，不报错文案（等级是增强信息，失败不该在资料页留下错误条） |
| `features.trust_levels` 关闭 | 整卡不渲染 |

---

## 2. 五档视觉区分（不靠 emoji，不新增调色板）

### 2.1 档位 → 图标（lucide，全部已存在于 `@iconify-json/lucide`）

| TL | 档位名（zh / en） | 图标 | 备注 |
| --- | --- | --- | --- |
| 0 | 新用户 / Newcomer | `i-lucide-sprout` | 与 `admin.settings.tabs.newcomers` 同图标，语义一致（同域，非重复造词） |
| 1 | 正式用户 / Member | `i-lucide-leaf` | 抽芽之后是叶，视觉上是同一生长序列的第二格；**zh 显示名与角色 key `member` 脱钩**（D21） |
| 2 | 常驻用户 / Resident | `i-lucide-house` | 「住下来了」；不用 `home`（与导航图标撞） |
| 3 | 资深用户 / Veteran | `i-lucide-mountain-snow` | 高度 + 时间感；不用 `medal`/`award` |
| 4 | 领袖 / Leader | `i-lucide-crown` | **`crown` 在这里是合法保留词**：徽章规范 §4.4 明令徽章禁用 `crown`/`medal`/`award`，就是为了把它留给等级 |

五个图标构成一条**生长/攀升**的语义序列（芽 → 叶 → 屋 → 山 → 冠），而不是五个互不相干的符号。
禁止用 `star` / `sparkles` / `flame`（这三个在站内已被用于其他语义）。

### 2.2 档位 → 形状与色（两轴单调阶梯）

单一色相无法区分五档（站内只有 accent + 中性 + warning 三族可用），所以阶梯建在**两条单调轴**上：
**填充强度** 与 **环线权重**。两轴同向递增，任何一档都同时被两轴区分，不靠单点色差。

| TL | 环线 | 填充 | 图标色 |
| --- | --- | --- | --- |
| 0 | `1px dashed var(--sf-border)` | `transparent` | `var(--sf-fg-tertiary)` |
| 1 | `1px solid var(--sf-border)` | `transparent` | `var(--sf-fg-secondary)` |
| 2 | `1px solid color-mix(in srgb, var(--sf-accent) 34%, transparent)` | `var(--sf-accent-soft)` | `var(--sf-accent)` |
| 3 | `2px solid color-mix(in srgb, var(--sf-accent) 55%, transparent)` | `var(--sf-accent-soft)` | `var(--sf-accent)` |
| 4 | `1px solid var(--sf-accent)` | `var(--sf-accent)`（实心） | `var(--sf-card)`（反色） |

> **step 1 一行已被 D-C6 取代（2026-11-11，表体保留为历史结论）**：TL1 不再是「`--sf-border` 环 +
> `var(--sf-fg-secondary)` 字形」的中性态，而是**档位色 34% 环（`color-mix(--tl-accent 34%, --sf-border)`）
> + 透明底 + 档位色字形**。原因：色相改按档位派生后，step 1 的中性规则会把 rank 1 的档位色整个吃掉
> （给 TL1 配任何色都无效，其余档正常）。阶梯强度仍由「环 → 淡底 → 粗环 → 实心」表达，色相从第一档起就在。

- **TL3 与 TL2 的区分靠环线 2px**（唯一 2px 档）。这条是刻意的：`accent-soft` 在 12 个日间预设下
  与 `accent` 的明度差会被背景色吃掉一部分，2px 环线是纯几何差异，任何预设、任何缩放下都成立。
- **TL4 是唯一的实心档**。整站没有第二个「实心 accent 方块」，所以最高档一眼可辨。
- **档位名与副文案永远是 `var(--sf-fg)` / `var(--sf-fg-secondary)`**，色相只上图标容器，
  永不承担文字可读性（沿用徽章规范 §1.3 第 1 条）。
- 未达成态**不用 `opacity`** 降级：TL0 的降级靠「虚线环 + 无填充 + tertiary 图标」三处实现。

> **2026-11-11 标注（D-C6，本节的色相来源已被取代，形状梯度仍生效）**：上表 `var(--sf-accent)` /
> `var(--sf-accent-soft)` 一列**不再作为档位色来源**——现在色相按档位派生
> （`tierColorOf` → 行根内联 `--tl-accent` → 共享 `.sf-tl-mark`），淡底改为
> `color-mix(in srgb, var(--tl-accent) 12%, var(--sf-card))`。**环线权重（1px / 2px / 实心）与
> TL0 虚线仍是现行契约**，「档位名与副文案永不上色」「未达成态不用 opacity」同样不变。
> 原表保留为 2026-11-10 的历史结论，不追改；裁决见
> `../decisions/2026-11-11-trust-level-color-render-unification.md`。

### 2.3 暗色

| 项 | 亮色 | 暗色（`.dark`） |
| --- | --- | --- |
| accent 族 | `var(--sf-accent)` | `var(--sf-accent-dark)` |
| accent-soft | `var(--sf-accent-soft)` | `var(--sf-accent-soft)`（token 自身在暗色下已是低 alpha 暖底，不二次处理） |
| TL4 反色图标 | `var(--sf-card)` | `var(--sf-card)`（暗色卡面 = 深色，`accent-dark` 是亮色，反色仍成立） |

- `--sf-accent-dark` 是**按 accent 主题分别定义**的（`main.css:35/114/134/154/174`：teal / blue / violet /
  rose / amber 各一套），所以暗色下必须走 token，禁止写死亮色 accent。
- 暗色下 `1px solid` 的 `color-mix(accent 34%)` 会偏暗，**保持 34% 不动**：暗底上细环靠 accent 自身
  的亮度即可见，提高到 60% 会让 TL2 与 TL3 的环线权重差消失。

### 2.4 12 个日间预设下的表现（逐条结论，不是「应该没问题」）

12 个预设只覆盖 `--sf-light-background/surface/muted/border/border-strong/glass/row-hover`，
**不覆盖 `--sf-accent`**。因此五档阶梯的色相关系在 12 个预设下**完全一致**，需要单独处理的是底色对比：

| 预设 | 底色倾向 | 对本卡的影响 | 结论 |
| --- | --- | --- | --- |
| `pure_white` / `porcelain` / `mist_gray` | 近白中性 | 无 | 直接成立 |
| `paper` / `parchment` / `morning_apricot` | 暖米 / 暖杏 | `--sf-muted` 与 `--sf-card` 明度差被压到约 1.1:1 | **刻度未达成段改用 `color-mix(fg 16%)`**（§1.5），不依赖 muted |
| `sakura` / `lilac_mist` | 粉 / 紫调 | `accent-soft` 叠在粉紫底上偏「粉」 | 填充只做背景，环线与图标仍承载信息；TL2 的 1px 环 + TL3 的 2px 环不受影响 |
| `cloud_blue` / `cool_frost` / `mint_mist` / `sage` | 冷调 | `accent-soft` 在冷底上略降饱和 | 同上，不构成问题 |

**硬结论**：卡面背景固定 `var(--sf-card)`（12 个预设里都是近白 surface，`parchment` 也是 `#fcf8ef` 级），
**不透明叠在页面背景上**；禁止在等级卡上用 `--sf-surface`（`cloud_blue`/`mint_mist` 等预设下 surface
与 background 只差 2~3 个明度级，卡片边界会消失）。

---

## 3. 后台阈值页（`SFAdminTrustLevelsTab`）

### 3.1 落位：既有 tab 数组追加一项，**不是新页面**（已核实，非假设）

**核实结论（2026-11-10，实读源码）**：

| 事实 | 证据 |
| --- | --- |
| `newcomers` 这个 id **确实存在** | `pages/admin/settings/index.vue:24`（`SettingsTab` 联合类型）、`:31`（`validTabs`）、`:38`（`tabs` 数组项）、`:48`（`tabComponents`） |
| 其 tab 组件路径 | `apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`（同目录另有 7 个 tab 文件，命名一致） |
| 其 tab 图标 | `i-lucide-sprout`（`:38`）—— 与本规范 TL0 档位图标同源，语义一致 |
| i18n 键已存在 | `admin.settings.tabs.newcomers` =「新人限制」/「Newcomers」；正文键族 `admin.settings.newcomers.*`（15 个） |
| `SFAdminFixedTabNav` **不需要任何注册** | `components/admin/settings/shared/SFAdminFixedTabNav.vue` 是纯 props 驱动（`items` / `modelValue` / `ariaLabel`），内部无白名单、无 catalog 查询；`tests/admin/settings/fixedTabNav.test.ts` 用**任意** `items` 断言行为，不枚举真实 tab |
| **新增组件必须登记 V3 reviewed identity**（原稿此处结论有误，已按小前实测改正） | `scripts/v3-catalog/generate.mjs:793` — 缺 identity 直接 `throw new Error(\`new or moved UI surface needs a reviewed stable identity mapping: ${item.source}\`)`；本条实测落地在 `docs/extensions/v3/catalog-identities.json` 的 `core.component.admin.sfadmin_trust_levels_tab` 与 `core.component.public.s_f_trust_level_card` |

**登记点共 8 处**（漏项的后果分三档，从「静默」到「挡住提交」）：

| # | 登记点 | 漏了会怎样 |
| --- | --- | --- |
| 1 | `pages/admin/settings/index.vue` — `SettingsTab` 联合类型加 `'trustLevels'` | 类型报错 |
| 2 | 同文件 — `validTabs` 加 `'trustLevels'` | **静默**：`?tab=trustLevels` 被 `normalizeTab` 回落 `basic` |
| 3 | 同文件 — `tabs` 数组插入 `{ id: 'trustLevels', label: t('admin.settings.tabs.trustLevels'), icon: 'i-lucide-gauge' }` | tab 不出现 |
| 4 | 同文件 — `tabComponents` 加 `trustLevels: SFAdminTrustLevelsTab` | **静默**：渲染空面板，不报错 |
| 5 | `i18n/locales/zh-CN.json` + `en-US.json` — `admin.settings.tabs.trustLevels` | 界面显示裸键名 |
| 6 | `docs/extensions/v3/catalog-identities.json` — `core.component.admin.sfadmin_trust_levels_tab` / `sforum.component.admin.sfadmin_trust_levels_tab@1` | **生成器抛错**：`new or moved UI surface needs a reviewed stable identity mapping: …`（`scripts/v3-catalog/generate.mjs:793`） |
| 7 | 同上 — `core.component.public.s_f_trust_level_card` / `sforum.component.public.s_f_trust_level_card@1` | 同上 |
| 8 | V3 目录再生成（`adminSurfaceCatalog.gen.ts` 等 18 份对齐产物，`adminSurfaceCatalog.test.ts` 计数随之 +1） | 目录 `--check` 与 `validate-v3-p0-catalogs` 红 |

- **catalog identity 漏了是「抛错」不是「静默失败」**：这比漏 `validTabs` 温和（不会带病上线），
  但同样会挡住提交，且错误信息里只有 `item.source`，不告诉你怎么命名 —— 照上表两条 id/contractVersion 抄。
- **「`SFAdminFixedTabNav` 不需要注册」这个结论仍然成立**，本表不推翻它：它没有 catalog identity、
  不参与 V3 生成，新增 tab 只是往它的 `items` 数组里加一项。原稿错的只是「settings 子 tab 不进 V3 目录」那半句 ——
  组件级 identity 与 tab 级注册是两件事，前者必须登记，后者不需要。

**建议补一条 contract 测试**：照 `tests/admin/settings/clientVersionPolicy.test.ts:41-47` 的既有写法，
断言 shell 含该 tab 项、且两份 locale 的 `admin.settings.tabs.trustLevels` 都 truthy。这是既有先例，不是新制度。

- 组件：`apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue`（AGENTS.md 强制：
  固定 Core tab 的实质内容必须是 `tabs/` 下的独立文件）。
- 位置：紧接 `newcomers` 之后（`basic → accountSecurity → registration → newcomers → trustLevels → maintenance → client → verification → updates`）。
  理由：`trust.new_user_*` 与 `trust.level.*` 同域（plan §4），相邻才不会让运营在两处之间迷路。
- URL：`/control-panel/settings?tab=trustLevels`，走既有 `normalizeTab` + query 同步，**不新增路由**。
- 权限：读页复用 `admin.access`（plan D9），tab 不做额外权限门；写操作走 `trust.manage` 由 API 裁决。
- **禁止**：独立页 `/control-panel/trust-levels`（会需要第二套页头/工具栏，且改 V3 目录计数，收益为零）、
  自建 `role="tablist"`（第二套 tab 系统）、在页内再做二级 tab（手风琴也不行，见 3.3 的密度结论）。

### 3.2 页头与外壳（逐字对齐既有契约，不自创）

tab 组件内**不渲染页面标题、不渲染 `UDashboardToolbar`** —— 它们属于 route shell
（`pages/admin/settings/index.vue:96-125`），tab 组件再画一次就是 AGENTS.md 禁止的「第二个页头」。

卡片外壳（逐字复用 `SFAdminSiteNewcomersTab.vue` 的类串）：

| 元素 | 规格 |
| --- | --- |
| `UCard` | `class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"` |
| `UCard` footer | `:ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"` |
| header 标题 | `<h2 class="text-base font-bold">` + `admin.settings.trustLevels.title`（「信任等级阈值」） |
| header 描述 | `<p class="mt-1 text-xs text-muted">` + `admin.settings.trustLevels.description`（一句：五档阈值，改动即时生效） |
| header 右侧 | `UBadge color="neutral" variant="soft" class="font-mono">trust.level.*</UBadge>`（对齐 newcomers 的 `trust.new_user.*`） |
| 卡片内边距 | 交给 `UCard` 默认；不自加 `p-*` |

### 3.3 每档一行阈值编辑（密度定稿）

每档一行，**5 行一屏可见，不做折叠/手风琴**：

```
fieldset.sf-trust-threshold-row   [data-level="0..4"]
├─ legend.sf-trust-threshold-row__legend
│  ├─ span.sf-trust-threshold-row__icon   [aria-hidden] → 档位图标（§2.1）
│  ├─ span.sf-trust-threshold-row__name   → 档位名（14px/600）
│  └─ span.sf-trust-threshold-row__code   → "TL0"（11px/600, tabular-nums, slate-500）
└─ div.sf-trust-threshold-row__grid
   └─ UFormField ×7   label = 指标名，name = 指标 suffix（写入单个 trust.levels JSON）
      └─ UInput type="number" min="0" class="w-full"
```

| 项 | 值 | 说明 |
| --- | --- | --- |
| 行容器 | `<fieldset>` + `<legend>` | 语义分组；屏幕阅读器进每个输入会播报所属档位，25 个输入不迷路 |
| `legend` 布局 | `display:flex; align-items:center; gap:8px; margin-bottom:10px` | 图标 + 档位名 + TL 编号一行 |
| `__icon` | `24×24`，`border-radius:8px`，图标 `14×14`，环线/填充按 §2.2 档位表 | 比资料页卡小一档：后台行密度优先 |
| 行间分隔 | `border-top:1px solid` `border-slate-200 dark:border-zinc-800`，`padding-top:14px; margin-top:14px` | 与后台卡内既有分隔同色 |
| 网格（≥1280） | `grid-template-columns: repeat(6, minmax(0, 1fr)); gap:10px` | **6 个指标一行**（≈150px/格），单行一档、5 档一屏 |
| 网格（768–1279） | `grid-template-columns: repeat(3, minmax(0, 1fr))` | 3+3，两整行、零空格 |
| 网格（≤767，含 390） | `grid-template-columns: repeat(2, minmax(0, 1fr))` | 2+2+2，三整行、零空格；**不启用 `span 2`** |

**断点按 6 项重算（原 5 项/7 项的结论都作废）**：6 是偶数，落行是 `6` / `3+3` / `2+2+2`，**每档都零空格**。
**因此「末项 `span 2`」这条在 6 项下不成立、已删除**：2 列 + 6 项本来就能整除，硬让末项跨 2 会在倒数第二行
留出半格空洞（`(1,2) (3,4) (5,·) (6 跨2)`）—— 那是 5 项（奇数）时的补丁，套到偶数项上反而制造缺口。
规则改为：**指标数为奇数时才启用 `:last-child { grid-column: span 2 }`；偶数时不加**。
单格宽下限判据保留：1024 视口内容区约 736px → `(736-36)/4` 的旧算式作废，3 列时 `(736-24)/3 ≈ 237px`；
768 视口约 496px → `(496-24)/3 ≈ 157px`；390 视口 drawer 满宽 2 列仍 > 96px 输入可读下限。

| 单行高度 | label 16 + input 36 + 行间距 ≈ 76px；5 行 ≈ 380px + 卡片头/尾 | 一屏可容纳，**这是不做折叠的量化依据** |
| 零值语义 | 阈值为 `0` = 该指标不参与本档判定，副提示 `text-xs text-muted`（「0 表示不限制」，同 newcomers 的 `zeroUnlimited`） | 与后端语义一致，避免运营把 0 理解成「必须 0 个」 |

**能力解锁集合不在本页编辑。** 本刀 M4 只冻结阈值读写；能力映射由 PM 报告（§5 能力映射表）定稿，
后台若要展示解锁项，只能是 `UBadge` 只读列表且需要独立 API 形状 —— 未定稿前**不画**。
（这条写进 §6 反面清单。）

### 3.4 恢复默认按钮（D12 强制项，位置与文案冻结）

位置与 newcomers **完全同构**：卡片 body 第一行，`flex flex-wrap items-start justify-between gap-3`：

| 元素 | 规格 |
| --- | --- |
| 左：`UAlert` | `color="neutral" variant="soft" icon="i-lucide-info" class="flex-1"`，`:title="t('admin.settings.trustLevels.restoreHint')"` |
| 右：`UButton` | `type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw"`，`class="self-start"`，点击走 `section.runRestore` |
| 按钮文案 | zh-CN「恢复推荐默认」/ en-US "Restore recommended defaults"（`admin.settings.trustLevels.restoreDefaults`） |
| Alert 文案（D12 原文要求） | zh-CN「恢复默认只重置五档阈值，不会影响用户已获得的等级」；en-US "Restoring defaults only resets the five level thresholds; it never changes levels users already earned." |
| 确认机制（已核实，非假设） | `useSettingsSection().runRestore`（`composables/settings/useSettingsSection.ts:60-68`）**没有确认框**：它直接 `options.apply()` 然后弹一条 success Toast（`color:'success'`、`icon:'i-lucide-rotate-ccw'`、`duration:5000`、`title` 由调用方传入）。**不自造确认弹窗**，与 newcomers 逐字同机制 |
| Toast 标题 | `admin.settings.trustLevels.restoreDefaults`（=「恢复推荐默认」），与按钮同文案；不另造「已恢复」句 |

**因为没有确认步骤，上面那条 Alert 不是装饰，是唯一的口径披露点**（D12 的硬要求）：它必须在按钮**同一行或正上方**、必须常驻（不可折叠、不可 5 秒后自动消失），且**禁止**把这句话改写成 Toast —— Toast 会消失，而「恢复默认不会改用户已获得的等级」是运营按下按钮**之前**必须读到的信息。
| 窄屏 | `flex-wrap` 生效：Alert 整行，按钮换行后 `self-start` 左对齐；按钮**不缩成纯图标**（此处是破坏性操作，图标-only 不可接受） |

**保存 / 重置**：`UCard #footer` 内 `SFAdminFormFooter`，`submit-text = t('admin.settings.save')`，
`:show-unsaved-alert="hasChanges"`，`@reset` 走「撤销未保存改动」并 Toast（`color:'neutral'`、
`icon:'i-lucide-rotate-ccw'`、`duration:5000`）—— 与 newcomers 逐字一致。
保存成功 Toast：`color:'success'`（跟随主题 token），5 秒自动消失；失败 Toast 不自动消失。

### 3.5 指标与输入上限（与 §1.4 同一套 metric）

| # | metric（`Requirements` JSON 字段） | 后台 label（zh） | 公开页 label（zh） | `min` / `max` |
| --- | --- | --- | --- | --- |
| 1 | `days_visited` | 访问天数 | 访问天数 | 0 / 3650 |
| 2 | `topic_count` | 主题数 | 主题 | 0 / 10000 |
| 3 | `comment_count` | 回复数 | 回复 | 0 / 100000 |
| 4 | `like_received_count` | 获赞数 | 获赞 | 0 / 100000 |
| 5 | `like_given_count` | 给赞数 | 给赞 | 0 / 100000 |
| 6 | `bookmark_count` | 收藏数 | 收藏 | 0 / 100000 |

- **恰好 6 个**，顺序即 `app/Models/TrustLevels/types.go:88-95` 的 `Metrics` 顺序（后台表单与契约的展示顺序跟它一致，
  不自排序）。原稿的 `account_age_days` **不存在**：逐档账龄（C7）已作废，账龄只保留 `trust.new_user_days`
  一个上限闸（`Policy.NewUserDays`，0 = 不设闸）。
- **存储形状（实测 `app/Models/TrustLevels/config.go:23-36`）**：单选项键 `trust.levels`，值是严格解码的 JSON：

  ```json
  { "schemaVersion": 1,
    "levels": [
      { "levelSlug": "resident",
        "requirements": { "days_visited": 0, "topic_count": 0, "comment_count": 0,
                          "like_received_count": 0, "like_given_count": 0, "bookmark_count": 0 },
        "limits": { "topicCooldownSeconds": null, "commentCooldownSeconds": null,
                    "dailyTopicLimit": null, "dailyCommentLimit": null },
        "earnable": [] }
    ] }
  ```

  - `schemaVersion`（不是 `version`）必须等于 `ConfigSchemaVersion`（当前 1）；
  - `levels` 是**数组**不是对象，且必须齐 5 档、无重复、无未知等级；
  - `limits` 四字段**可空**：`null` = 继承站点默认，`0` = 免冷却/免限额（D27）——**两者语义不同，UI 不许把 null 显示成 0**；
  - 解码是 `DisallowUnknownFields` + 严格校验：未知字段、缺档、负值、尾随内容一律拒绝。
    所以**任何表单漏字段都会在保存时被后端直接拒绝**，不会静默丢数据。
- **本页只编辑 `requirements` 六项**。`limits` 与 `earnable` 同在这份 JSON 里，但不在本页编辑范围；
  实现**必须整份 JSON 回写**（读原值 → 只改 6 个 requirement → 原样写回 `limits`/`earnable`），
  禁止用「只构造 requirements 的新对象」整体覆盖 —— 那会把 `limits` 清成 `null`、`earnable` 清成空数组，
  等于顺手改掉运营没碰过的配置。
- 上限只是输入护栏，不是业务上限；越界时 `boundedInteger` 回落到推荐默认（与 newcomers 同函数口径）。
- 若后端最终暴露的 metric 数量再变：**网格按列数档位生效**，M8 断言的是「窄屏 2 列且无空半格」，
  不是「恰好 6 项」。

---

## 4. 窄屏 390×844（两处，逐条验收）

### 4.1 资料页等级卡

- 390 视口下右栏收进 `SFProfileRightRail` 的 drawer（既有行为，本刀不改）：卡宽变为 drawer 内容宽。
- 卡片所有文本仍单行截断（`__name` / `__hint`），**drawer 内不允许因为变宽就换行**：换行会让
  drawer 高度随档位名长度跳动。
- 刻度 5 格在 drawer 宽度下 `repeat(5, 1fr)` 仍成立（每格 ≥ 40px，3px 高），不折行。
- 无横向溢出：卡内唯一可能超宽的是 `__hint`，已 `nowrap + ellipsis + min-width:0`。

### 4.2 后台阈值 tab

- `390×844` 实测要求（M8 浏览器断言）：
  1. `document.documentElement.scrollWidth <= clientWidth`（无横向滚动条）；
  2. `.sf-trust-threshold-row__grid` 计算后 `grid-template-columns` 为 **2 列**；
  3. 每档 `fieldset` 内 6 格排成 3 整行（2 列 × 3 行），**无半格空隙**；`span 2` 在 6 项下不启用；
  4. 恢复默认按钮可见且可点（未被 `overflow:hidden` 裁掉），Alert 与按钮换行后不重叠；
  5. `UInput` 在 2 列网格内 `width:100%`，不出现被压到 < 96px 的输入框（96px 是 4 位数的可读下限）。
- 页头/tab 条：`SFAdminFixedTabNav` 自带 `flex-wrap`，390 下 tab 换行不横向滚动 —— 本刀只是多一项，
  **不新增任何 tab 条样式**。

---

## 5. 无障碍（四条判定标准，沿用 D8 写法）

### 5.1 判定标准（每条都必须能写成断言，缺一条打回）

1. **每个可感知元素有完整 `aria-label`，且不用 `title` 代替。**
   等级卡根、进度条、每个后台输入（`UFormField` 已提供）、每个图标按钮必须有可读名称；
   `aria-label` 文案走 §8 的 i18n 键，模板原文进翻译文件，不拼裸字符串。
2. **状态不只靠颜色：文字 + 形状 + 语义三重冗余。**
   文字：档位名「常驻用户」+ TL 编号「TL2」；形状：虚线/实线/2px 环/实心（§2.2）；语义：`aria-label`
   含档位名。**只有颜色**的等级表达（如仅靠 5 个色块）判失败。刻度条是 `aria-hidden` 的纯装饰冗余，
   信息由文字与 `aria-label` 承载。
3. **进度条语义完整且自洽。**
   `role="progressbar"` + `aria-valuemin="0"` + `aria-valuemax=阈值` + `aria-valuenow`（clamp 后）
   + `aria-valuetext`（人话）+ `aria-label`（档位迁移），六项齐全；`aria-valuemax` **不是 100**；
   `valuenow <= valuemax` 恒成立。TL4 无进度块时，断言「页面上 progressbar 计数为 0」。
4. **结构不跳级、可点区域单一、焦点可见。**
   heading 顺序 `h3`（右栏分区）之下不再起 heading；卡片不可点，因此不加 `tabindex`、不加 `role="link"`；
   后台每个输入可获得焦点，焦点环 `outline: 2px solid var(--sf-accent); outline-offset: 2px`；
   图标一律 `aria-hidden="true"`，图标永远不单独承担语义。

### 5.2 `aria-label` 原文模板（直接进 i18n）

```
等级卡根节点（可省略，h3 + 名称已可读；若加，用这一句）
zh-CN: "信任等级 {name}，{earned|notEarned}"
en-US: "Trust level {name}, {earned|notEarned}"

进度条
zh-CN: "信任等级进度：{current} → {next}"
en-US: "Trust level progress: {current} → {next}"
aria-valuetext
zh-CN: "{metric} {current} / {threshold}"
en-US: "{metric} {current} / {threshold}"

后台档位分组
legend 已提供可读名称；每个输入的可读名称 = "{档位名} {指标名}"
例：「常驻用户 获赞数」
```

---

## 6. 不做什么（出现即打回）

1. **不做营销式 hero**：资料页不加通栏等级横幅、不加渐变底、不加「你已超越 87% 的用户」这类文案。
2. **不加第二套 tab**：等级不是资料页 tab；后台阈值页只往 `SFAdminFixedTabNav` 的 `items` 追加一项，
   禁止自建 `role="tablist"`、禁止二级 tab、禁止独立 `/control-panel/trust-levels` 路由。
3. **不用 emoji 当状态标记**：🌱🏠👑 一律禁止，包括 `aria-label`、`title`、Toast 文案、占位文案。
4. **不手写 inline SVG**：档位图标只走 `UIcon` + §2.1 白名单的 lucide 名。
5. **不新增调色板**：不许加 `--sf-level-1`…`--sf-level-5`、不许加金/银/铜/钻石色。五档靠两条单调轴
   （填充 + 环线权重），不靠五个色相。
6. **不写十六进制 / `rgb(` 字面量**：颜色一律 `var(--token)`；需要透明度时用 `color-mix(in srgb, var(--token) N%, transparent)`。
7. **不用 `opacity` 表达未达成态**（TL0）：降级靠虚线环 + 无填充 + tertiary 图标。
8. **不按档位换进度条颜色**：进度永远 `var(--sf-accent)`；五档换色会让一屏出现五种进度条。
9. **不在后台画能力解锁勾选矩阵**：能力映射未定稿（M4 只冻结阈值），画了就是第二套真相。
10. **不做折叠/手风琴阈值行**：5 行一屏可见（≈380px），折叠只会让运营漏改一档。
11. **不给后台 tab 加页头/工具栏**：route shell 已有 `text-xl` 标题 + `UDashboardToolbar`，tab 内再加一层即违规。
12. **不在 390 靠横向滚动解决**：任何新面板出现横向滚动条即失败。
13. **不加动效**：不加位移、缩放、渐变扫光、进度条补间动画、骨架微光。等级变化是低频事件，
    动画只会让「刚升级」的提示变成噪音。
14. **不把等级做成徽章**：不进 `badges` tab、不生成 `data-badge-slug`、不复用 `SFBadgeChip`。

---

## 7. 变量清单（本文只允许引用这些）

| 用途 | token |
| --- | --- |
| 主色 / TL2–TL4 | `--sf-accent`、`--sf-accent-dark`、`--sf-accent-soft`、`--sf-accent-focus` |
| 中性 / TL0–TL1 | `--sf-fg`、`--sf-fg-secondary`、`--sf-fg-tertiary`、`--sf-border`、`--sf-muted` |
| 卡面 | `--sf-card`（**禁止**用 `--sf-surface` 作等级卡底色，见 §2.4） |
| 焦点 | `--sf-accent`（焦点环）+ `--sf-accent-focus`（如需外发光层，本刀不需要） |

未在此表内的 token 视为未批准使用；需要新 token 时先改本文件，再写代码。
后台页额外允许 Tailwind 的 `slate-*` / `zinc-*`（与 `settings/index.vue`、`newcomers` tab 同一约定）。

---

## 8. i18n 键清单（施工照抄；zh-CN 与 en-US **两份都要**，H 组会断言齐全）

**文件路径（已核实）**：`apps/web/i18n/locales/zh-CN.json`、`apps/web/i18n/locales/en-US.json`。
**不是** `app/i18n/`。两份必须同键同参，缺一份即显示裸键名。

**命名风格（跟随现有，不另起一套）**：沿用 `badges.tier.*` / `badges.card.*` / `admin.settings.newcomers.*`
的写法 —— 域下用小驼峰、嵌套对象分组、插值用单花括号 `{name}`。
**档位用语义 slug，不用数字键**：`badges.tier.gold|silver|bronze` 已是先例；`name.0` 这种数字键会让
翻译器无法排序、也无法在缺键时给出可读报错。前端用 `levelKey(0..4)` 映射到 slug。

| level | slug |
| --- | --- |
| 0 | `newcomer` |
| 1 | `member` |
| 2 | `resident` |
| 3 | `veteran` |
| 4 | `leader` |

### 8.0 D21 约束（已拍板，键表按此冻结；**本组命名是最后一次变更，之后冻结**）

1. **五档 zh-CN 显示名 = 新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖**
   （en-US 不变：`Newcomer` / `Member` / `Resident` / `Veteran` / `Leader`；**slug 不变**，
   `name.newcomer|member|resident|veteran|leader` 五个键名一个不动）。
   **终稿键数 39**：原 36 键名全部不变，新增 5 键（`profile.trustLevel.progress.text`、
   `profile.trustLevel.metric.likeGiven` / `.bookmarkCount`、`admin.settings.trustLevels.metric.likeGiven` / `.bookmarkCount`），
   并因 `account_age_days` 不存在而删除 2 键（`metric.accountAge` ×2）。
   原因：上一版「正式成员」对用户组词「普通会员」仍带近义感（成员 / 会员）。改后整组是**带「用户」的单调阶梯**，
   与用户组词族（普通会员 / 版主 / 站点运营 / 超级管理员 / 技术管理）**零字面重叠**，
   机器断言与「一眼不可混」同时成立，不必退到人工兜底。
   被否的方案：「老用户」——「老」在等级阶梯里没有位置感，且对新人暗示贬值。
   **只改显示名，不改 slug**：slug 是 API/数据契约，显示名是展示词，两者解耦正是这一条要证明的事。
2. **API 的 JSON 字段统一叫 `levelSlug`，禁止裸 `slug`**（plan D21）。前端 i18n 键的后缀仍取 slug 值
   （`...name.member`），但**任何 DTO / 组件 prop / 事件载荷都不许出现裸 `slug` 字段** ——
   `level.slug` 与 `roleKey` 长得太像，同一个词在两处指两件事就是下一次事故的种子。
3. **i18n 审查断言（两层，机器层在本命名组下已能全覆盖）**：
   等级键族（`profile.trustLevel.*` + `admin.settings.trustLevels.*`）的 zh-CN 值，与用户组键族
   （**实际命名空间是 `admin.roleCatalog.*`，不是 `roles.*`**；本项目的「角色」在产品文案里叫
   **用户组**）的 zh-CN 值：
   - **机器可断言（硬）**：两侧 zh 值**集合交集为空**。新档位组已满足，脚本可跑、CI 可守。
   - **人工兜底（降级为「新增档位名时才走」）**：同词根近似（成员/会员、管理/运营）机器抓不到；
     只有**新增或改名**一个档位名时才人工过一遍，不是每次审查都做。
   若将来某个档位名与用户组词撞车，**改档位名，不改用户组词** —— 用户组是既有产品词，等级是新增展示词。
4. **本条来源**：设计侧实测纠正了 D21 原文里不存在的键族 `roles.*`（真实为 `admin.roleCatalog.*`，
   且产品文案把「角色」叫「用户组」）。计划 D21 已同步注明「设计侧实测纠正」。

### 8.1 资料页等级卡（`profile.trustLevel.*`）

| key | zh-CN | en-US |
| --- | --- | --- |
| `profile.trustLevel.title` | 信任等级 | Trust level |
| `profile.trustLevel.name.newcomer` | 新用户 | Newcomer |
| `profile.trustLevel.name.member` | 正式用户 | Member |
| `profile.trustLevel.name.resident` | 常驻用户 | Resident |
| `profile.trustLevel.name.veteran` | 资深用户 | Veteran |
| `profile.trustLevel.name.leader` | 领袖 | Leader |
| `profile.trustLevel.code` | `TL{level}` | `TL{level}` |
| `profile.trustLevel.nextHint` | 再获 {count} 个{metric}可升到「{next}」 | Earn {count} more {metric} to reach {next} |
| `profile.trustLevel.maxed` | 已达最高等级 | Highest level reached |
| `profile.trustLevel.earned` | 已获得 | earned |
| `profile.trustLevel.notEarned` | 尚未获得 | not earned |
| `profile.trustLevel.ariaLabel` | 信任等级 {name}，{state} | Trust level {name}, {state} |
| `profile.trustLevel.progress.ariaLabel` | 信任等级进度：{current} → {next} | Trust level progress: {current} → {next} |
| `profile.trustLevel.progress.text` | {current} / {threshold} | {current} / {threshold} |
| `profile.trustLevel.progress.valueText` | {metric} {current} / {threshold} | {metric} {current} / {threshold} |
| `profile.trustLevel.metric.daysVisited` | 访问天数 | days visited |
| `profile.trustLevel.metric.topicCount` | 主题 | topics |
| `profile.trustLevel.metric.commentCount` | 回复 | replies |
| `profile.trustLevel.metric.likeReceived` | 获赞 | likes received |
| `profile.trustLevel.metric.likeGiven` | 给赞 | likes given |
| `profile.trustLevel.metric.bookmarkCount` | 收藏 | bookmarks |

- `nextHint` 的 `{metric}` 参数传 `t('profile.trustLevel.metric.*')` 的结果（人话），**不是** options 键名。
  中文量词天然可省（「再获 12 个获赞」读得通），英文用 `{count} more {metric}` 避免 a/an 判断。
- `ariaLabel` 的 `{state}` 参数传 `earned` / `notEarned` 两条；`earned` 的英文值小写，因为它嵌在句中。
- **`progress.text` 服务 §1.2 的 DOM 要求**：进度条右侧要显示纯数字 `"48 / 60"`（`__progress-text`），
  规范禁止裸字符串，所以必须有这个键。先例是 `badges.card.progress` = `'{current} / {threshold}'`，逐字同形。
- **`progress.text` 与 `progress.valueText` 是两个键，不是重复**：`text` 是**可见文字**（只有数字），
  `valueText` 是**屏幕阅读器播报**（带指标名的人话句）。两者内容故意不同；合并会让读屏只念数字、
  或让视觉上出现一长句。
- `progress.valueText` 两份文案**完全相同**（纯数据格式），仍要各自建键：跨语言格式以后可能分叉
  （如英文加千分位），共用一个键会锁死这个自由度。
- 失败/加载**没有键**：等级卡失败静默不渲染（§1.6），不留 `loadFailed`，避免出现「预留了却永远不显示」的死键
  —— 这与 plan D9「不做 `trust.view` 死权限」是同一条纪律。

### 8.2 后台阈值页（`admin.settings.trustLevels.*`）

| key | zh-CN | en-US |
| --- | --- | --- |
| `admin.settings.tabs.trustLevels` | 信任等级 | Trust levels |
| `admin.settings.trustLevels.title` | 信任等级阈值 | Trust level thresholds |
| `admin.settings.trustLevels.description` | 五档阈值按累计行为判定，保存后即时生效；阈值为 0 表示该指标不参与该档判定。 | Thresholds are evaluated from cumulative activity and apply immediately after saving. A zero threshold means that metric does not participate for that level. |
| `admin.settings.trustLevels.restoreDefaults` | 恢复推荐默认 | Restore recommended defaults |
| `admin.settings.trustLevels.restoreHint` | 恢复默认只重置五档阈值，不会影响用户已获得的等级。 | Restoring defaults only resets the five level thresholds; it never changes levels users already earned. |
| `admin.settings.trustLevels.zeroUnlimited` | 0 表示该指标不参与本档判定。 | 0 means this metric does not participate for this level. |
| `admin.settings.trustLevels.resetChanges` | 已重置信任等级阈值更改 | Trust level threshold changes were reset |
| `admin.settings.trustLevels.level.newcomer` | 新用户 | Newcomer |
| `admin.settings.trustLevels.level.member` | 正式用户 | Member |
| `admin.settings.trustLevels.level.resident` | 常驻用户 | Resident |
| `admin.settings.trustLevels.level.veteran` | 资深用户 | Veteran |
| `admin.settings.trustLevels.level.leader` | 领袖 | Leader |
| `admin.settings.trustLevels.metric.daysVisited` | 访问天数 | Days visited |
| `admin.settings.trustLevels.metric.topicCount` | 主题数 | Topics |
| `admin.settings.trustLevels.metric.commentCount` | 回复数 | Replies |
| `admin.settings.trustLevels.metric.likeReceived` | 获赞数 | Likes received |
| `admin.settings.trustLevels.metric.likeGiven` | 给赞数 | Likes given |
| `admin.settings.trustLevels.metric.bookmarkCount` | 收藏数 | Bookmarks |

**复用既有键，不新建**（`newcomers` tab 已是这个口径）：

| 用途 | 复用键 |
| --- | --- |
| 保存成功 Toast | `admin.settings.saved` |
| 保存失败 Toast | `admin.settings.saveFailed` |
| 保存按钮 | `admin.settings.save` |
| 恢复默认成功 Toast 标题 | 直接传 `admin.settings.trustLevels.restoreDefaults`（`runRestore` 的 `title` 参数），不另造「已恢复」句 |

**不需要的键（明确不建）**：

- 后台档位名与公开页档位名**各建一份**（`profile.trustLevel.name.*` 与 `admin.settings.trustLevels.level.*`）：
  公开页是「用户看到的身份词」，后台是「运营看到的配置标签」，未来任一侧改词不该牵动另一侧。
- 输入框的可读名称**不需要拼接键**：`<fieldset><legend>` 提供档位名、`UFormField` 的 `label` 提供指标名，
  屏幕阅读器自动组合成「常驻用户 获赞数」。**禁止**再手写一个 `aria-label` 拼接键覆盖它（会覆盖掉 legend 语义）。
- 恢复默认**没有确认弹窗文案**：`runRestore` 无确认步骤（§3.4 已核实），确认句不存在，不要建 `confirm*` 键。

---

## 9. 施工检查表（前端自查，小审按此验收）

- [ ] 等级卡挂在右栏身份区之后、`publicDetails` 之前；资料页 tab 条零改动。
- [ ] 卡片根有 `data-level` 与 `data-earned`；TL0 时 `data-earned="false"` 显式写出。
- [ ] 5 个 `li.sf-trust-level-card__rung` 固定渲染，各自带 `data-level` + `data-earned`。
- [ ] 进度条六件套齐全：`role` / `valuemin` / `valuemax`（阈值）/ `valuenow`（clamp）/ `valuetext` / `aria-label`。
- [ ] TL4 页面 `role="progressbar"` 计数为 0，渲染 `__maxed`。
- [ ] 副文案与进度条使用同一个瓶颈 metric，`count = threshold - current`。
- [ ] 五档图标与 §2.1 逐字一致（sprout / leaf / house / mountain-snow / crown），无 emoji、无 inline SVG。
- [ ] 档位视觉只改「环线 + 填充 + 图标色」，名称/副文案永远 `--sf-fg` / `--sf-fg-secondary`。
- [ ] 暗色下 accent 走 `--sf-accent-dark`；TL4 实心图标用 `--sf-card` 反色。
- [ ] 刻度未达成段用 `color-mix(in srgb, var(--sf-fg) 16%, transparent)`，不用 `--sf-muted`。
- [ ] 后台阈值 tab 的 4 处代码登记齐全：`SettingsTab` 联合类型 / `validTabs` / `tabs` 数组（`newcomers` 之后）/ `tabComponents`；URL `?tab=trustLevels` 可直达不回落 `basic`。
- [ ] `SFAdminFixedTabNav` **未做**任何额外注册（它是纯 props 组件），也没有改 catalog。
- [ ] tab 组件内无页面标题、无 `UDashboardToolbar`、无自建 tablist。
- [ ] 每档一行 `<fieldset>` + `<legend>`，**7 个** `UFormField`（含给赞数、收藏数），值写入单个 `trust.levels` JSON（不是一指标一键）。
- [ ] 新增两个组件的 V3 reviewed identity 已登记（`core.component.admin.sfadmin_trust_levels_tab`、`core.component.public.s_f_trust_level_card`），V3 目录再生成后 `--check` 绿。
- [ ] 恢复默认：Alert + 按钮同行（`flex-wrap`），文案含「不会影响用户已获得的等级」，走 `runRestore`。
- [ ] 390×844：无横向滚动、网格 2 列 × 3 整行（6 项，无 `span 2`）、输入框 ≥96px、按钮未被裁。
- [ ] 后台只用 slate/zinc 类串，公开页只用 `var(--sf-*)`；无十六进制、无 `rgb(`。
- [ ] i18n 按 §8 清单建键：`apps/web/i18n/locales/zh-CN.json` + `en-US.json` 两份同键同参；档位用语义 slug（newcomer/member/resident/veteran/leader），不用数字键；无裸字符串。
- [ ] 未新建死键（无 `loadFailed`、无 `confirm*`、无输入框 aria-label 拼接键）。
- [ ] 五档 zh 值为 新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖（en 不变），slug 与键名全不变；DTO 字段叫 `levelSlug`，无裸 `slug`；
      等级键族 zh 值与 `roles.*` zh 值交集为空（D21 断言）。
- [ ] 全仓新增 CSS 未引入 `--sf-level-*` 之类新调色板。
