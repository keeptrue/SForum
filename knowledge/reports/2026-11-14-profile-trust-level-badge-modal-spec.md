# 资料页信任等级徽章 + 等级详情弹窗：视觉与交互规格

日期：2026-11-14
状态：定稿（Lead 与产品口径已确认，前端按此实现）
范围：**只**覆盖个人资料页（`forum.profile.show`）。论坛读面（话题列表/详情/回复）不显示等级标识——那是 `knowledge/reports/2026-11-11-trust-level-badge-spec.md` 的领域，且实现已 revert（`46c2f52cb`）。

> **口径变更（本条推翻 2026-11-11 的旧结论）**：`knowledge/modules/trust-levels.md` §「等级标识的展示口径」当时写的是「个人中心也不再加新徽章——那张卡就是全部」。2026-11-14 产品明确要求：资料页头部身份行加一枚等级徽章，等级卡可点开详情弹窗。论坛读面仍然不显示等级，那条约束不变。

## 0. 一句话结论

头部身份行在 `UID n` 之后放一枚 **档位色小牌**（`图标 + TLn`，12% 档位色底 + 34% 环，文字用前景色）；右栏等级卡整卡加一层**透明覆盖按钮**，点它或点徽章打开**同一个**页面级弹窗，内容为当前档 + 下一档门槛 + 本人进度 + 规则条件。

## 1. 为什么不是「色点 + 纯文字」

`2026-11-11` 那份规范为论坛读面选了「6px 色点 + 同色 `TLn`、无底色无描边」，理由是作者行只有 390px、且一行内已经可能有角色徽章，不能再加一个色块。

资料页不成立：

- 头部身份行是**独立一行**（`.sf-profile-intro__identity-meta`），空间充足，不存在与角色徽章抢位的约束；
- 同一屏内已有先例：昵称旁的佩戴徽章 chip 就是「12% 填充 + 34% 环线 + 档位色图标 + 前景色文字」（`SFBadgeChip`）。等级徽章沿用同一族语法，两个彩色元素才读成「同一套标记系统」，而不是两套互不相干的装饰；
- 用户要的是「带颜色」，纯文字 + 色点的颜色面积只有 6px 圆点，在资料页头部这种低密度区域太弱。

结论：**沿用 chip 的配色配方，不沿用它的名字**。徽章只写 `TLn`，不写档位名（档位名已在同一屏的等级卡上，且长名会挤爆 390px 的身份行）。

## 2. 头部徽章几何

与 `.sf-profile-uid` 同高，身份行行高不变（这是硬约束：`.sf-profile-intro__identity-meta` 显式钉了 `line-height: 1.4`）。

| 项 | 值 | 说明 |
|---|---|---|
| 高度 | 20px | `.sf-profile-uid` = `12px × 1.4 + 2px×2` ≈ 20.8px，取整 20px，肉眼无差 |
| 内边距 | `0 6px` | 与 uid pill 的 `2px 6px` 视觉对齐（竖向由 flex 居中） |
| 圆角 | 5px | 与 uid pill 一致 |
| 图标 | 12px，档位色 | `trustLevelIconOf`（存储图标优先，未选按 rank 取内置序列） |
| 文字 | `TL{n}`，11px / 700，tabular-nums | 前景色（`--sf-public-text`），不跟档位色 |
| 间距 | 图标与文字 4px | 沿用 chip |
| 与 UID 间距 | 6px | 由容器 `gap: 4px 6px` 提供，不额外加 margin |

`flex: 0 0 auto` + `white-space: nowrap`：身份行换行时徽章整体换行，不被压缩、不折成两行。

## 3. 颜色与对比度（实测）

底色 = `color-mix(in srgb, var(--tl-accent) 12%, transparent)`；环线 = 同色 34%；图标 = 100% 档位色；文字 = `--sf-public-text`。

算法与 `2026-11-11-trust-tier-color-palette.md` 一致（WCAG 2.1 相对亮度）。浅色按最差卡面 `parchment #fcf8ef` 核验，深色按公开页底 `#1b201f` 核验（实测脚本输出，9 色全量）：

| 档位色 | 浅色：文字 / 图标 | 深色：文字 / 图标 |
|---|---|---|
| slate | 11.99 / 5.96 | 12.44 / 5.23 |
| teal | 12.18 / **4.38** | 11.98 / 6.94 |
| emerald | 11.97 / 5.60 | 11.84 / 7.32 |
| sky | 12.10 / 4.71 | 12.27 / 6.17 |
| indigo | 11.85 / 6.15 | 12.81 / **4.63** |
| violet | 12.06 / 4.51 | 12.64 / 5.01 |
| fuchsia | 11.86 / 4.92 | 12.56 / 5.50 |
| rose | 11.80 / 4.68 | 12.76 / 5.19 |
| amber | 11.97 / 5.57 | 11.80 / 7.62 |

- **文字最差 11.80:1**，远超 4.5:1。徽章的全部语义（档位数字）由文字承载，颜色只做加速识别，灰度/色盲/`forced-colors` 下不丢信息。
- **图标最差 4.38:1**（teal / parchment），高于非文本元素 3:1。
- **环线最差 1.63:1**（teal / parchment）——**这里不承诺 3:1，也不去凑**。徽章不是交互控件、不靠边界表达状态；可访问信息由文字承载。环线只做「这块是一个整体」的视觉分组，与 `SFBadgeChip` 的 34% 环线同值，保持同一族语言。
- 不写死 hex：色相全部经 `tierAccentStyle(tierColorOf(tier, rank))` + `data-sf-tl-accent` 属性。自定义 hex 档位在暗色下由全局规则 `color-mix(… 74%, white)` 提亮，与等级卡走同一条路径。
- 不写 9 个 `--sf-tl-*` token 类：那会绕开 `data-sf-tl-accent`，自定义 hex 档在暗色下会显示未提亮的原色。

## 4. TL0

- 与其它档**同尺寸**，只把环线改成 `dashed`（与等级卡 `[data-level="0"]` 的虚线语言一致），底色降到同值的 12% slate。
- 不降透明度：透明度会把文字与图标一起压到阈值以下，而 TL0 恰恰是最需要被新用户读懂的一档。
- 不隐藏：隐藏会让「没有徽章」同时表示「新成员」和「数据缺失」。

## 5. 等级卡入口：覆盖层按钮

`SFTrustLevelCard` 的 class / `data-*` / `aria-*` 是冻结契约（`apps/web/tests/profile/trustLevelCard.test.ts` 逐条断言），因此**不改根节点、不把整卡换成 `<button>`**（`role="progressbar"` 会被吞进按钮的可访问名，读屏会念成「查看详情 信任等级进度：常驻用户 → 资深用户」）。

做法：根 `<section>` 加 `position: relative`，末尾追加一个透明覆盖按钮：

- `aria-haspopup="dialog"`，`aria-label` = 「查看信任等级详情」；
- `position: absolute; inset: 0; background: transparent; border-radius: inherit; cursor: pointer`；
- `:focus-visible` 用 2px `var(--tl-accent)` outline（**100% 档位色**，4.38:1 起，达标）；
- hover 时边框提亮到 **100% `var(--tl-accent)`**（不是 34%：34% 混色只有 1.63:1，等于没反馈），用 `.sf-trust-level-card:has(.sf-trust-level-card__hit:hover)` 实现——`button` 是最后一个子节点，兄弟选择器够不到前面的元素；
- 覆盖层会阻止卡面文字选中，这是「整卡是入口」的必然代价，接受。

`interactive` 默认 `false`：不传时卡片仍是纯展示，既有测试与既有调用点行为零变化。

## 6. 弹窗结构

体量与留白对齐 `SFBadgeDetailModal`（20px 内边距、右上 X、底部右对齐按钮组），信息项自上而下：

1. **头部**：40px `.sf-tl-mark`（`data-step` 形状轴 + 档位色）+ 档位名（h2，`aria-labelledby`）+ `TLn` 小牌 + 状态（已获得 / 尚未获得）。
2. **门槛区块**（存在下一档时）：标题「升到「{next}」的门槛」；清单 = 下一档 `requirements` 里 `> 0` 的项，按 `TRUST_LEVEL_METRIC_KEYS` 顺序，每行「指标名」+「≥ 阈值」。
3. **进度区块**（仅本人、有瓶颈、未满级）：进度条六件套口径与等级卡完全一致（`aria-valuemax` 是阈值不是 100、`aria-valuenow` clamp、`aria-valuetext` 人话、`aria-label` 不含数字），下面一行 `nextHint` 文案。
4. **规则条件区块**（仅本人、`viewer.conditions` 非空）：逐条 `i-lucide-check` / `i-lucide-x` + 服务端下发的双语句子。这些句子**不参与**数值进度条，两者并列展示，不合成一个分数。
5. **满级**：用「已达最高等级」取代 2、3 两块。
6. **底部**：单个「关闭」。等级不可佩戴、不可购买，没有主操作。

缺哪块不渲染哪块：没有下一档、没有 metrics、没有 conditions 时都不留空标题、不留空段落。

## 7. 他人视角

- 弹窗显示：当前等级 + 下一档门槛（阈值本来就随公开目录下发，人人可见）。
- 弹窗不显示：进度条、`nextHint`、规则条件。后端对他人视角不下发 `bottleneck` / `conditions`（`like_given_count` / `bookmark_count` 是刻意不公开的计数），前端也不许用目录阈值反推「他还差多少」。
- 文案口径：门槛区块标题写「升到「{next}」的门槛」，不写「还差」——对他人页这是描述档位规则，不是描述这个人。

## 8. 降级链

| 情况 | 行为 |
|---|---|
| `features.trust_levels` 关闭 / `trustLevel` 为 null | 徽章、卡片、弹窗全部无入口 |
| 公开目录不可用 | 名字/图标走 `FALLBACK_LADDER`，颜色按 rank 取默认 token（与卡片同一回退） |
| `level` 越界 | 钳到阶梯两端，不显示越界数字 |
| `color` 不在白名单 | `tierColorOf` 按 rank 取默认色，前端不猜色 |
| `requirements` 全为 0 | 门槛区块不渲染（不是「没有门槛」，是「这一档不按数值判定」——不写解释文案） |
| 本人但 `metrics` 为空且未满级 | 不渲染进度块，也不写「已达最高等级」 |

## 9. 交互与可访问性

- 徽章与覆盖层都是原生 `<button>`：Enter / Space 可开，Tab 可达。
- Esc 关闭由 UModal 负责；关闭后焦点回到触发元素（QA 复核）。
- 移动端：右栏内容在 `SFPublicMobileRightDrawer` 里，点卡片前先关抽屉，再开弹窗，避免两层 overlay 叠加。
- 390px 下身份行允许换行（`@handle` / `UID` / 徽章各自整体换行），不压缩徽章、不缩字号。
- 徽章 `:title` 给全名（桌面悬停补充信息），但**任何语义都不只靠 title**：`aria-label` 已含档位名与 `TLn`。

## 10. 验收判据（可机器断言）

- 头部徽章：`data-level`、`data-sf-tl-accent`、`--tl-accent` 内联、`TL{n}` 文本、TL0 的 `border-style: dashed`、`interactive` 时 `button[aria-haspopup="dialog"]`。
- 卡片：不传 `interactive` 时**没有** `.sf-trust-level-card__hit`；传了则 `aria-label` 正确且点击 emit `open`。
- 弹窗：本人有 `[role="progressbar"]` 与条件行；他人两者都没有；满级只有 `profile.trustLevel.maxed`；门槛清单条数 = 下一档 `>0` 的指标数。
- 全量 `bun test`、`bun run typecheck`、`node tests/validate-architecture-boundaries.mjs` 通过。
- 浏览器：桌面 1440×900 与 390×844 各一张截图，含头部徽章、弹窗打开态、他人页无进度块。

## 11. 明确不做

- 完整等级阶梯（TL0→TLn 全景）不进弹窗。
- 他人进度、他人计数。
- 等级权益 / 权限清单展示（`earnable` 有下发，但权限名 i18n 映射是另一件事）。
- 后端与 OpenAPI 改动：`viewer.conditions` / `viewer.nextLevel` / 每档 `requirements` 都已在现有契约里，本次只是前端补齐消费。
