# 2026-11-12 信任等级：外观改成 Modal + 全量图标 + 可选自定义色

Status: **accepted**（2026-11-12，Lead 批准并落地）
上游：`2026-11-11-trust-level-tier-color-and-delete.md`（D-C1 档位颜色 / D-C5 CSS 变量）、
`2026-11-11-trust-levels-n-tiers.md`（D-N4 图标白名单）。
设计源：`../reports/2026-11-12-trust-level-appearance-modal-spec.md`（几何、对比度复核、文案）。
本文件用 D-A 前缀，与 D-N / D-C 不撞号。

## 问题

后台「档位管理」的外观（图标 + 颜色）原本是**行内展开面板**，两个选择器都是服务端白名单的
定长 radio 组：

1. 图标只有 16 个 lucide 短名（`tier_icons.go:24-41`），运营想给某一档换个更贴切的图标没有出路。
2. 颜色只有 9 个 token，**零自定义**。而同一后台的「标签 / 分类」早就用 `SFIconPicker` +
   `input[type=color]` + 任意 `#rrggbb`（`pages/admin/forum/tags.vue:359-390`、
   `normalizeTaxonomyIconColor` / `taxonomyIconPattern`）。两套外观编辑语言并存，运营要学两遍。
3. 行内面板把「图标 + 颜色」塞进每一档卡片，8 档页面上每档都展开时整页高度翻倍，
   而面板开合状态还要由 tab 层持有（`SFAdminTrustLevelTiersTab.vue:76` 的 `appearanceFor`）。

## 决定

### D-A1 图标：16 短名（保留为默认序列）∪ `i-(lucide|tabler)-<name>` 全名

`IsTierIcon` = 16 项白名单 ∪ `^i-(lucide|tabler)-[a-z0-9]+(?:-[a-z0-9]+)*$`（`tier_icons.go`）。
存储接受两种形态：历史短名 `leaf` 与全名 `i-tabler-crown`；前端 `normalizeIcon` **不再剥**
`i-lucide-` 前缀（剥了就得到白名单外的短名，保存即 422）。

**为什么可以放开**：真实约束不是「静态可枚举」，而是**图标名必须能被 `@nuxt/icon` 的
serverBundle 解析**——`nuxt.config.ts:185-192` 是 `provider: 'server'` + 全量 lucide/tabler
serverBundle，所以任意集合内名字 SSR 与 CSR 都能渲染；`tierIconClass` 早已双向兼容
`i-` 前缀（`useTrustLevel.ts:212-217`）。集合被限定为 lucide/tabler 两个（已打包的），
不是「任意 Iconify 集合」——那才会在渲染期静默空缺。

### D-A2 颜色：9 token（推荐色板 + 默认色序）∪ 小写 `#rrggbb`

`IsTierColor` = 9 token ∪ `^#[0-9a-f]{6}$`；`config.go` 在校验与落库前统一
`strings.ToLower(strings.TrimSpace())`，所以 `#7C3AED` 与 `  #7C3AED  ` 都落库为 `#7c3aed`。
`tierColors` 9 项与顺序、`DefaultTierColor` 全部不动：**顺序仍是默认色序（index = rank）**，
`colorChoices` 的语义从「合法取值白名单」降级为「推荐色板」。

**这一条取代** `2026-11-11-trust-level-tier-color-and-delete.md` 的「被否：自由 hex 输入」
以及 `tier_colors.go` 文件头「不接受自由 hex」的论断。当时否掉它的理由是「深色模式对比度不可控」，
现在由三件东西回答：D-A3 的自动明暗派生、modal 内的明/暗双底预览、以及 D-A5 的低对比度提示。
**下面这些 D-C1 语义不变**：color 仍是可选属性、空 = 跟随位次自动、解码期不填默认、
有效色由 `Policy.ColorOf` 解析、`colorChoices[n]` 仍属于 rank n。

### D-A3 明暗派生写在 CSS，不写 JS 颜色数学

- 颜色字面量只在 `sforum-trust-colors.css` 出现一次：18 个 `--sf-tl-<token>-light|-dark`。
- 三层映射：`:root, [data-sf-tl-scope="light"]` / `.dark` / `[data-sf-tl-scope="dark"]`。
- 自定义 hex 的**页面级**渲染：`tierAccentStyle` 对 hex 返回
  `{ --tl-accent-light: <hex>, --tl-accent-dark: color-mix(in srgb, <hex> 74%, white) }`，
  `--tl-accent` 由 `[data-sf-tl-accent]` 与 `.dark [data-sf-tl-accent]` 两条规则算出。

**为什么自定义 hex 不能直接内联 `--tl-accent`**：内联声明优先级高于任何 `.dark` 作用域规则，
写死内联值就等于让深色主题永远是同一个色。所以自定义色只内联「两级源值」，
`--tl-accent` 交给样式表算。token 路径不受影响（它内联 `--tl-accent`，本来就靠
`var(--sf-tl-x)` 自带明暗切换），因此这次改动对存量配置是零行为变更。

**为什么不用 `light-dark()`**：它取决于 `color-scheme`，而公开主题把 `color-scheme` 设成
`light dark`（`sforum-theme.css:702`），跟随系统而不是跟随站点的 `.dark` 类 → 站点内切换主题时
档位色会不跟着变。`.dark` 类是本仓既定的单一开关，派生必须跟着它走。

### D-A4 预览卡必须与页面模式解耦

modal 里同时显示「浅色页面 / 深色页面」两张卡。若两张卡都靠 `.dark` 作用域取值，
暗色后台里「浅色卡」拿到的是暗色 hex（白底上 2.3:1），浅色后台里「深色卡」拿到亮色 hex ——
两张卡在对方的模式里都在撒谎。所以预览卡挂 `data-sf-tl-scope="light|dark"`，
卡片自身声明胜过来自 `html.dark` 的继承；底色固定 `#ffffff` / `#1b201f`，不跟主题变量。

### D-A5 非法格式阻断，低对比度只提示

- 格式非法（`#fff` / `red` / 空）→ 「应用」禁用 + 字段级错误，不发意图。
- 深色卡面对比度 < 3:1 → 一行提示（`appearanceDialog.colorLowContrast`），**仍允许应用**。
  设计稿实测：`#111827` 混合后仍只有 2.17:1（近黑色不该做识别色）、`#7c3aed` 2.89→4.65、
  `#166534` 2.31→4.23。阈值反解后中性灰边界约 `#2b3542`。

### D-A6 交互：modal + 即改即生效，落盘仍只有一个入口

外观面板从行内改为 `UModal`（`SFAdminTrustLevelAppearanceDialog`）：桌面 `sm:max-w-2xl`、
`max-height: min(86dvh, 720px)`、body 是唯一滚动区、footer 吸底；390 下全宽。
modal 内改动即时写入页面表单草稿，「完成」只关闭；`恢复默认外观` = `setIcon(null) + setColor(null)`。
**不引入 modal 级 draft 层**：全页保存入口仍只有页脚一个，多一层草稿就多一层同步。

### D-A7 错误码名保持不变

`icon_not_whitelisted` / `color_not_whitelisted` 的**名字**保留，只改文案。它们已在前端 i18n
（`useTrustLevelSaveErrors.ts` 的码表）与契约里作为稳定标识符存在；改名要同时动码表、i18n、
契约与所有断言，收益只是「名字更好听」。语义已由文案承担：「必须是 16 个默认短名之一或
`i-lucide-*` / `i-tabler-*` 全名」「必须是 9 个默认 token 或 `#rrggbb`」。

## 后果

- **6 处**内联 `--tl-accent` 的元素必须同时带 `data-sf-tl-accent`（TierRow / EarnableRow /
  ThresholdsTab / LimitsTab / OverviewTab / SFTrustLevelCard）。漏一处只会让**自定义 hex** 在
  那一处失效（token 不受影响），所以用一条遍历 SFC 的护栏测试兜住。
- `SFTrustLevelCard` 卡面原来那句 `--tl-accent: var(--sf-tl-slate)` 兜底被删：它特异度 0,2,0，
  会压过全局 `[data-sf-tl-accent]`，把 hex 档静默打回 slate。兜底改由全局规则的
  `var(--tl-accent-light, var(--sf-tl-slate))` 承担。
- 新增 3 个 admin 组件、删除 2 个 → v3 目录身份映射要加 3 条、退役 2 条，并重生成
  `core_catalog_gen.go`（`node scripts/v3-catalog/generate.mjs`）。
- **回滚风险（高）**：旧 API 二进制只认 9 token / 16 短名，读到 hex 或全名图标会
  422（`trust.levels` 保存被拒，已存在的库内配置不会自动损坏，但再保存就失败）。
  回滚必须**同时回退该配置值**，不能只回退二进制。
- 存储出现两种图标形态（短名 / 全名）。`tierIconClass` 已双向兼容，`normalizeIcon` 不再归一，
  这是刻意取舍：归一成短名会让 `i-tabler-*` 丢失集合前缀，归一成全名要给 16 个短名编一个集合。

## 被否

- 前端硬编码一份 JS 色表做预览/取色器初值 —— 第二份真相。取色器初值改为运行时读
  `--sf-tl-<token>-light`。
- 让 `SFIconPicker` 的手填输入参与档位图标（它只校验 `i-` 开头，会放过 `i-fa-*` 造成 422）→
  传 `:show-custom-input="false"`，用搜索覆盖全量。
- 自定义色存 `colorLight` + `colorDark` 双值 —— 契约、表单、校验全多一个字段，而绝大多数运营
  不会去调第二个色；自动派生 + 预览已经足够。
- 预览只保留当前模式一张卡 —— 诚实但信息少，运营恰恰要在「另一种模式」里判断这个色会不会糊。
