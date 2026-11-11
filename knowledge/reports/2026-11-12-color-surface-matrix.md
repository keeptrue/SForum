# 颜色配置面 × 渲染面矩阵（信任档位 / 分类 / 标签 / 其它可设色实体）

日期：2026-11-11 22:2x（会话时间 2026-11-11T22:3x+08:00，报告名沿用任务约定的 2026-11-12）
状态：只读调查完成。**未修改 apps/** 任何文件，未在后台保存任何配置。**
性质：本文只登记事实与缺口，不含实现。修复由本刀/后续刀承接。

## 0. 结论先行

1. **信任档位色已经全线贯通**（本刀工作区状态）：五个分区 + 资料页卡在运行时都解出各档自己的 `--tl-accent`，实测 TL0 `#475569` / TL1 `#0f766e` / TL2 `#166534` / TL3 `#0369a1` / TL4 `#4338ca`。Lead 交接里「阶梯/阈值/限额/能力解锁仍用站点 accent」的说法，对应的是**本刀改动前的 HEAD**，不是当前工作区。见 `admin-overview.png`、`admin-thresholds.png`、`admin-limits.png`、`admin-earnable.png`。
2. **「TL1 图标看起来是灰的」不是缺陷**：`.sf-tl-mark[data-step="1"]` 本来就把图标字形压成中性色，档位色只体现在边框/底色（step 2/3）与实心块（step 4）。实测 TL1 图标 `color: rgb(103,103,115)`、`--tl-accent: #0f766e`。验收时不要把它当 bug 再报一次。
3. **分类 `iconColor` 的主干读面正常**：左栏导航块、分类索引页、分类选择器、后台列表/弹窗预览都吃到配置色（`/c/announcement` 实测 `rgb(239,68,68)` = DB 里的 `#ef4444`）。
4. **分类 `iconColor` 有两处真实缺口**：分类详情页头部根本不渲染图标（`SFCategoryShowPage.vue` 无 icon 元素），话题列表的分类 chip 走的是「按 slug 哈希的固定 4 色调色板」，与配置色无关（`forumListPresentation.ts`）。
5. **标签 `iconColor` 几乎无处可看**：全站只有一个公开读面（编辑器 `SFTagInput` 的 chip）。标签索引页、标签详情页、话题列表/详情的 tag chip 全部是纯文字。
6. 根因分类：档位色是「单变量 `--tl-accent` + 内联下发」，已经收口；分类/标签色是「每处自己写 `iconColor?.trim() || 'var(--sf-accent)'`」，本刀已抽成 `taxonomyIconColor()`，但**渲染面本身缺失**（不是取值错，是没画）。

## 1. 方法与证据

- 代码面：`rg` 全量扫描 `iconColor` / `tierAccentStyle` / `--tl-accent` / `color-mix` 消费点；逐个确认「色源」是档位色、站点 accent，还是硬编码。
- 运行时（公开面）：`google-chrome --headless=new --screenshot` 直出 SSR 页面，1633×810/900；再开 CDP（`--remote-debugging-port`）用 `getComputedStyle` 读 `--tl-accent`、`color`、`background-color`，避免肉眼判断色差。
- 运行时（后台面）：从已登录的内置浏览器用 `Storage.getCookies` 只读取出 `sforum_session`，**灌进本机独立 headless 实例**（`/tmp/qa-color/prof`）后截图；没有新建标签页、没有动用户的浏览器窗口、没有任何写操作。
- 配置真值：只读查库 `select slug,name,icon_color from categories / tags`（`docker exec -e PGPASSWORD=... sforum-postgres`）。
- 限制（必须知道）：
  - 任务要求「不保存配置」，因此**没有真的把某个档位改成非默认色**去验证「配置色 vs 渲染色」。档位这一栏的「配置值」是位次默认值；`trust.levels` 当前库里确实一个 `color` 键都没有。要闭环这一条，需要一次允许写配置的验收（改一档颜色 → 五个分区 + 资料页卡全绿）。
  - 深色模式只看了代码，没有截图；见 §6 的 P2 项。

## 2. 矩阵 A：信任档位色

色源单一实现：`apps/web/app/utils/profile/trustLevelColors.ts:77` `tierAccentStyle()` → 行根内联 `--tl-accent: var(--sf-tl-<token>)`；token 取值在 `apps/web/app/assets/css/sforum-trust-colors.css`；几何/填充强度由共享类 `.sf-tl-mark[data-step]`（同文件 `:50`）给。

| 渲染面 | 文件:行 | 色源 | 实测 | 结论 |
|---|---|---|---|---|
| 档位管理 行 | `SFAdminTrustLevelTiersTab.vue:93`（`tierAccent`）→ `SFAdminTrustLevelTierRow.vue:106/311-340` | 档位色（内联） | rank chip / icon 块各自着色：`#0f766e`→`#166534`→`#0369a1`→`#4338ca` | 已统一 |
| 等级总览 阶梯 | `SFAdminTrustLevelsOverviewTab.vue:198`（`tierAccentStyle(tierColorOf(row, level))`） | 档位色（内联） | 五档 `#475569/#0f766e/#166534/#0369a1/#4338ca` | 已统一（本刀） |
| 门槛设置 行 | `SFAdminTrustLevelThresholdsTab.vue:59/115` | 档位色（内联） | 同上四色 | 已统一（本刀） |
| 发言限额 行 | `SFAdminTrustLevelLimitsTab.vue:59/109` | 档位色（内联） | 同上四色 | 已统一（本刀） |
| 能力解锁 行/矩阵 | `SFAdminTrustLevelEarnableRow.vue:74` | 档位色（内联） | 同上四色（复选框选中态另走 `--sf-accent`，属交互态，不是档位识别色） | 已统一（本刀） |
| 资料页 信任卡 | `SFTrustLevelCard.vue:55/105`，兜底 `:158` | 档位色（内联） | `/u/qa_tl4_leader` 实测 `data-level=0`、`--tl-accent:#475569`（该号当前就是 TL0）；脚本可确认内联值生效 | 已统一 |
| 档位颜色选择器 | `SFAdminTrustLevelColorPicker.vue:58/83/104`（`tierColorVar`） | 档位色板（每点独立） | 9 色 dot 各自取 `--sf-tl-*` | 已统一 |
| 档位图标选择器 | `SFAdminTrustLevelIconPicker.vue:163-217` | **站点 accent** | 选中环/底色是 accent | 正确（选择器自身的 UI chrome，不是档位识别色） |

> 陷阱（写进验收）：`.sf-tl-mark` 里**不能**再声明 `--tl-accent`，否则遮蔽祖先内联值——`sforum-trust-colors.css:42` 已注释警告，本刀四个分区正是按这条改的。

## 3. 矩阵 B：分类 `iconColor`

配置面：后台「版块分类」表单（`pages/admin/forum/categories.vue:608-622`，`<input type="color">` + hex 文本），存储 `categories.icon_color`（空串 = 跟随站点 accent）。取值收口：`utils/forum/forumTaxonomy.ts` `taxonomyIconColor()`（本刀新增，管理台与公开页共用一份）。

| 渲染面 | 文件:行 | 色源 | 实测/证据 | 结论 |
|---|---|---|---|---|
| 后台列表预览 | `pages/admin/forum/categories.vue:251` | `iconColor` → accent 兜底 | `admin-categories.png`：综合讨论蓝 / 数据库青 / 运维橙 / AI 紫 | 已统一 |
| 新建·编辑弹窗预览 | 同文件 `:378`（含 hex 文本回显） | 同上 | 同上（弹窗内预览与列表同形） | 已统一 |
| 左栏分类导航块 | `components/forum/navigation/SFCategoryNavigationBlock.vue:42` | 同上 | `home.png` / `c-announcement.png`：站点公告红 `#ef4444` | 已统一 |
| 分类索引页 `/categories` | `components/forum/SFCategoryIndexPage.vue:145` | 同上 | 逐项 `rgb`：`/c/frontend` `rgb(14,165,233)`、`/c/database` `rgb(20,184,166)`、`/c/devops` `rgb(249,115,22)`、`/c/announcement` `rgb(239,68,68)` | 已统一 |
| 分类选择器 | `components/forum/SFCategorySelect.vue:53` | 同上 | 代码一致 | 已统一 |
| 分类详情页头部 | `components/forum/SFCategoryShowPage.vue`（全文件无 `icon`/`iconColor`） | **不渲染** | `c-announcement.png`：头部只有「站务管理 / 站点公告」文字 + 标题，无图标块 | **缺口（P1）** |
| 话题列表分类 chip（首页/分类页） | `components/forum/SFHomeTopicRow.vue:40-43` + `utils/forum/forumListPresentation.ts:19-26` | **硬编码 4 色哈希调色板**（`bg-[#eaf2ff] text-[#1467d9]` 等） | `home.png`：综合讨论蓝、站点公告红、后端开发蓝……与 iconColor 无因果关系 | **不一致（P1）** |
| 话题详情页分类块 | `components/forum/SFTopicHeading.vue:48-50` | **纯文字**（无图标） | 代码确认 | 缺口（P2） |
| 编辑器短码/引用 chip | `utils/editor/shortcodes.ts:41`、`SFShortcodeReferenceDialog.vue:276`、`SFShortcodeReferenceNodeView.vue:28-29/116/134/144` | `iconColor`（含 12% `color-mix` 底） | 代码确认 | 已统一 |

契约层事实：`ForumTopicSummary` 只有 `categoryId/categorySlug/categoryName`（`utils/forum/forumTaxonomy.ts:120-123`），**没有** `categoryIcon/categoryIconColor`。所以列表/详情要按配置色渲染分类，先得加 DTO 字段（后端 + OpenAPI + 生成物），不是纯前端改动。

## 4. 矩阵 C：标签 `iconColor`

配置面：后台「标签管理」（`pages/admin/forum/tags.vue:368-381`），存储 `tags.icon_color`。库内样本：`golang #00add8`、`rust #dea584`、`vue #42b883`、`nuxt #00dc82`、`postgresql #336791`……

| 渲染面 | 文件:行 | 色源 | 实测/证据 | 结论 |
|---|---|---|---|---|
| 后台列表预览 | `pages/admin/forum/tags.vue:251` | `iconColor` → accent 兜底 | `admin-tags.png` | 已统一 |
| 弹窗预览 | 同文件 `:378` | 同上 | 同上 | 已统一 |
| `SFTagInput` 录入 chip / 建议项 | `components/forum/SFTagInput.vue:85` | 同上 | 代码确认（本刀已切到 `taxonomyIconColor`） | 已统一 |
| 标签索引页卡片 | `components/forum/SFTagIndexPage.vue:302-311`（`#{{ tag.name }}` 纯文字） | **不渲染图标** | `tags-ai.png` 左栏与卡片：只有 `#` 字形或纯文本 | **缺口（P2）** |
| 标签详情页头部/右栏 | `components/forum/SFTagShowPage.vue`（仅静态 `i-lucide-hash`/`layout-grid`）、`SFTagIndexRightRail.vue` / `SFTagShowRightRail.vue`（热门标签纯文字） | **不渲染配置色** | `tags-ai.png` | 缺口（P2） |
| 话题列表 tag chip | `SFHomeTopicRow.vue:188` + `sf-home` 样式 | 纯文字 | `home.png`：`#Rust #性能优化` | 缺口（P2） |
| 话题详情 tag chip | `SFTopicHeading.vue:107-110`，`sforum-topic.css:158-168` | accent（hover/active 才着色） | 代码确认 | 缺口（P2） |

契约层事实：`ForumTopicTagSummary`（`forumTaxonomy.ts:112-117`）只有 `id/slug/name/status`，没有 `icon/iconColor`。同上，列表/详情要显示标签色需要先加字段。

## 5. 矩阵 D：其它「后台可设颜色/样式」实体

| 实体 | 配置入口 | 渲染面 | 结论 |
|---|---|---|---|
| 站点强调色（5 预设 accent） | `/control-panel/personalization` 外观分区（`SFAdminPersonalizationAppearanceTab.vue:153`，另有用户级 `SFAppearanceSettingsPage.vue:235`） | `--sf-accent` 全站 | **设计如此**，是站点身份色，不属于「实体识别色」。档位色/分类色都刻意与它解耦（`tier_colors.go` 头注释、`taxonomyIconColor` 兜底说明） |
| 徽章 图标 | `SFAdminBadgeEditDialog.vue`（只有图标选择，无颜色字段） | `SFBadgeChip.vue:105-128`（`--sf-badge-chip-hue` 按徽章档位推导） | 无「设了色」入口，**不存在同类问题** |
| 版块分组（category group） | `categories.vue` 分组表单（name/description/visibility/position） | 索引页分组标题 | 分组**没有**颜色/图标字段，不存在同类问题 |
| 公开导航项 | `publicNavigation.ts:30-31` | 顶栏/侧栏 | 只有 `icon`（无颜色）→ 图标统一取站点色，一致 |
| 列表分类 chip 调色板 | 不可配置 | `forumListPresentation.ts:19-26` 4 色 `bg-[#...] text-[#...]` 硬编码 | **第三类隐患**：与主题 token 无关，且没有 `dark:` 变体（暗色下仍是浅底），需在暗色实机复核后决定是否改 token |
| 头像底色板 | 不可配置 | `forumListPresentation.ts:7-16` 8 色硬编码 | 同上（视觉旁支，不属本次色配置范畴，仅登记） |

## 6. 本刀覆盖 / 仍需后续

**本刀已覆盖（有运行时证据）**
1. 信任档位「配置面 → 五个后台分区 + 资料页卡」全链路的档位色绑定，运行时五档四色实测到位（§2）。
2. 分类/标签色的取值收口：`taxonomyIconColor()` / `taxonomyColorInputValue()` 成为唯一实现，后台列表、弹窗、导航块、索引页、选择器、`SFTagInput` 共用（§3/§4 表内「已统一」行）。

**仍需后续（只登记，建议优先级）**

| 优先级 | 问题 | 位置 | 需要的动作 |
|---|---|---|---|
| P1 | 分类详情页头部不渲染图标/色，与索引页/导航块不一致（用户最容易撞见的一处「设了色不渲染」） | `SFCategoryShowPage.vue` 头部 | 加一个 `sf-tl-mark` 风格的图标块，色源走 `taxonomyIconColor(category.iconColor)`；注意别在 span 上再声明 `--tl-accent` 形成遮蔽 |
| P1 | 话题列表分类 chip 用按 slug 哈希的固定 4 色，完全绕过配置色 | `SFHomeTopicRow.vue:40-43`、`forumListPresentation.ts:19-26` | 二选一：(a) 契约加 `categoryIconColor` 字段后按配置色渲染；(b) 明确保留「列表 chip 是稳定调色板」，同时把后台颜色字段的帮助文案改成「用于分类图标，不影响列表 chip」。**不允许两边都不说** |
| P2 | 标签色公开面无可见载体（索引卡、标签页头、话题 tag chip 全是文字） | `SFTagIndexPage.vue:302`、`SFTagShowPage.vue` 头部、`SFTopicHeading.vue:107` | 同 P1 的 (a)/(b) 选择；若选 (b)，后台标签表单需就地说明该色的可见范围（目前只有编辑器 chip） |
| P2 | 话题详情分类块纯文字 | `SFTopicHeading.vue:48-50` | 与 P1 一并处理，避免「列表有 chip、详情没有」 |
| P2 | 列表分类 chip / 头像色板硬编码无 `dark:` 变体，暗色模式可能浅底压字 | `forumListPresentation.ts:7-27` | 暗色实机截图复核后再决定改 token；属视觉旁支 |
| P3 | 档位色「配置值 ≠ 位次默认值」的验收缺口：当前库里所有档位都没存 `color`，无法证明「改过的色也能到五个分区」 | `trust.levels` | 需要一次允许写配置的验收：改 TL2 为 `rose` → 五分区 + 资料页卡同时变 `#be185d`，然后恢复默认 |
| P3 | `.sf-tl-mark[data-step="1"]` 让 TL1 图标本身是中性色，容易被误报成「1 档没色」 | `sforum-trust-colors.css:64` | 写进验收清单/设计说明，不实现改动 |

## 7. 复现命令（只读）

```bash
# 公开面直出
google-chrome --headless=new --no-sandbox --window-size=1633,900 \
  --screenshot=categories.png --virtual-time-budget=8000 http://127.0.0.1:3000/categories

# 后台面（先取已登录会话 cookie，再灌进独立实例）
# 1) Storage.getCookies → 取 sforum_session（只读，不改任何 cookie）
# 2) Network.setCookie 到 127.0.0.1 → Page.navigate → Page.captureScreenshot
# 3) 读色不改色：Runtime.evaluate 取 getComputedStyle(el).getPropertyValue('--tl-accent')

# 配置真值
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -At \
  -c "select slug,icon_color from categories;" -c "select slug,icon_color from tags limit 15;"
```

## 8. 证据文件

目录：`knowledge/reports/assets/2026-11-12-color-surface-matrix/`

| 文件 | 说明 |
|---|---|
| `admin-overview.png` | 等级总览阶梯：五档各自着色（本刀后） |
| `admin-tiers.png` | 档位管理：rank chip + 图标块按档着色 |
| `admin-thresholds.png` | 门槛设置：TL1 中性、TL2 绿、TL3 蓝、TL4 实心靛（`data-step` 阶梯） |
| `admin-limits.png` | 发言限额：与门槛同构 |
| `admin-earnable.png` | 能力解锁矩阵：档位色一致 |
| `admin-categories.png` | 后台分类列表预览：图标色 = 配置色 |
| `admin-tags.png` | 后台标签列表预览：图标色 = 配置色 |
| `categories.png` | 公开分类索引：逐项配置色 |
| `c-announcement.png` | **分类详情页头部无图标**（P1 缺口现场） |
| `tags-ai.png` | **标签页/标签索引纯文字**（P2 缺口现场） |
| `home.png` | 左栏导航有配置色；话题行分类 chip 走哈希调色板 |

