> **已作废（2026-11-14）**：本文关于后台 i18n 键族的说法错误。真实族是 `admin.forum.trustLevels.*`（394 叶键），`admin.settings.trustLevels.*` 在两份 locale 里均为 `undefined`。见 `../modules/trust-levels.md` §命名与 i18n 三条硬约定。

# 能力解锁（earnable）勾选矩阵 — 视觉与措辞规范（M10 施工版）

Status: **ready**（2026-11-11 09:18–09:30 +08:00 读码后定稿）
上游：`plans/2026-11-11-trust-levels-closing.md`（D33 裁决 / §2 已核实事实 / §5 T21–T25）
消费方：frontend 小前（组件与 i18n）、reviewer 小审（J-05/06/07/10 断言）、writer 笔笔（文案定稿）
本文只覆盖 **`SFAdminTrustLevelEarnableMatrix.vue` 这一个组件**的视觉、措辞与无障碍；不改后端、不改 route shell。

---

## 0. 读前必读：三件作废口径

1. **`reports/2026-11-10-trust-level-visual-spec.md` §6 第 9 条「不在后台画能力解锁勾选矩阵」已作废**
   （D33 走 A 路，编辑器就是要做）。那条禁令的理由是「能力映射未定稿」，现在定稿了。
   除这一条外，那份规范的 §2.1 图标白名单、§5 无障碍写法、§7 变量纪律**继续有效**。
2. **父计划 §3/§5 的「TL2 解锁 `topic.create` 是默认行为」已作废**：默认 `earnable = ∅`，
   勾选前对站点零行为变化。文案不得暗示「等级已经解锁了这些能力」。
3. **阈值网格的「6 偶不跨」规则不能照抄到本矩阵**：本矩阵是 7 项，跨列规则由**余数**决定，
   不是由奇偶决定。推导见 §2.3，两套规则的差别写在那里。

另外本文顺手核出一处**文案与代码漂移**（不属于本刀范围但会误导运营，建议 frontend/writer 同轮修）：
`SFAdminTrustLevelsTab.vue:72-85` 的 `restoreRecommended()` 只重置 `requirements`，**保留 `limits`**，
而 `restoreHint` 现文案写的是「恢复默认只重置五档的阈值与限额」。加了矩阵之后这句必须重写（见 §8.3）。

---

## 1. 落位与外壳（不新增页头 / 工具栏 / 卡片）

| 项 | 决定 | 理由 |
| --- | --- | --- |
| 宿主 | `SFAdminTrustLevelsTab.vue` 的**同一个 `UCard` body** 内，`zeroUnlimited` 那句 `<p>`（当前第 180 行）之后、旧注释块（182–188 行）处插入 `<SFAdminTrustLevelEarnableMatrix />` | 阈值与能力解锁是同一份 `trust.levels` JSON 的两个字段，拆两张卡会让运营以为要分两次保存 |
| 分区壳 | `<section>` + 上边框分隔：`border-t border-slate-200 dark:border-zinc-800 pt-5` | 与阈值行 `mt-3.5 border-t pt-3.5` 同一套分隔语言；body 已有 `space-y-5`，不再叠 margin |
| 页头 | **禁止**。`pages/admin/settings/index.vue:100-119` 已有 `text-xl` 标题 + `UDashboardToolbar` | AGENTS.md「Core-owned admin settings」几何契约 |
| 卡片 | **禁止**再加 `UCard`；本组件只是 body 里的一个 `<section>` | 同上；第二张卡 = 第二个视觉层级 |
| 标题层级 | `h3.text-sm.font-semibold`（卡头是 `h2`） | 不跳级；`h3` 同时是 `aria-labelledby` 目标 |
| 保存 | 走宿主 `<form>` 的既有 `SFAdminFormFooter`（sticky），组件内**不放**保存/重置按钮 | 一次保存一个 payload（`trustLevelsPayload`） |
| 恢复默认 | **不接管**。`restoreRecommended()` 保持「不动 `earnable`」的现有行为，只改文案（§8.3） | 推荐默认的 `earnable` 是 `∅`，让「恢复默认」清空运营勾选 = 一次不可见的策略变更 |

---

## 2. 矩阵几何

### 2.1 可用宽度（先算清，再定列）

后台内容区实测链（全部读码得出，非截图）：

- 页面容器 `layouts/admin.vue:722` → `p-4 sm:p-6`（≥640 视口左右各 24px）。
- 侧栏 `UDashboardSidebar :default-size="16" :min-size="14" :max-size="22"`（rem）→ 默认 **256px**，最小 224px。
- 卡片 body 为 `UCard` 默认 `p-4 sm:p-6` → ≥640 视口左右各 24px。

因此 **卡片内宽 `W = 视口 − 侧栏 − 48 − 48`**：

| 视口 | 侧栏 | W | 7 列时单格（gap 8） |
| --- | --- | --- | --- |
| 390 | 覆盖式抽屉，不占列宽（**假设**，见 §11 未实测项） | 390−32−32 = **326** | 不适用（2 列，159） |
| 768 | 224（min） | 448 | 不适用（3 列，144） |
| 1024 | 256 | 672 | 不适用（3 列，219） |
| 1280 | 256 | **928** | **125** |
| 1366 | 256 | 1014 | 138 |
| 1440 | 256 | 1088 | 149 |
| 1920 | 256 | 1568 | 217 |

### 2.2 三种形态（列数 / 行高 / 控件）

**≥1280：7 列「表头 + 裸勾选框」形态**（`data-mode` 由 CSS 媒体查询控制，见 §2.4）

| 元素 | 规格 |
| --- | --- |
| 列模板 | `grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 8px` |
| 表头行 | 每列两行文字：展示名 `12px/16px 600`，键名 `11px/14px font-mono slate-500`；`padding-bottom:8px`；`align-items:end`（各列底部对齐，键名行落在同一基线）；下边框 `1px` |
| 表头高度 | 30 + 8 + 1 = **39px**（展示名允许换到 2 行 → +16px，各列仍底部对齐） |
| 行首（档位） | `legend`：图标 `24×24`（`border-radius:8px`，图标 `14×14`，环线/填充按视觉规范 §2.2 五档阶梯）+ 档位名 `14px/600` + `TL{n}` `11px/600 tabular-nums slate-500`，`gap:8px`，`margin-bottom:8px` |
| 单元格 | 裸 `UCheckbox`，**无文字**；`min-height:32px`，内容水平居中（`justify-center`），`padding:6px 8px` |
| 勾选框 | **16×16**（`size="md"` → `base: size-4`），`ring 1px inset`、`border-radius:4px`，勾号 `16×16`；命中区 = 整个 32×125 单元格（≥24×24 WCAG 2.2 下限） |
| 行高 | 24（legend）+ 8 + 32 = **64**；加行分隔（1 + pt 12 + mt 12）= **89px** |
| 整块高度 | 表头 39 + 5×89 = **484px**；加标题/说明/空态 ≈ 560px（卡片内，可滚，不折叠） |
| 列对齐 | 表头与单元格**共用同一 grid 模板与同一 gap**，因此天然对齐；行首不占左列（legend 在单元格行上方整行铺开），不需要左侧标签列 |

**768–1279：3 列「标签在格内」形态**

| 元素 | 规格 |
| --- | --- |
| 列模板 | `repeat(3, minmax(0,1fr)); gap: 8px` |
| 表头行 | **`display:none`**（展示名移到格内，见下） |
| 单元格 | `UCheckbox` + 展示名，勾选框 `16×16` + `gap:8px` + 文本 `12px/16px`，`padding:6px 8px`，`min-height:32px`，允许 2 行 |
| 单格宽 | 768→144px / 1024→219px；「编辑自己的主题」= 16+8+84 = **108px ≤ 144**，单行 |
| 7 项落行 | 3+3+1 → **末项 `grid-column: span 3`**（满宽 448px） |

**≤767：2 列形态**

| 元素 | 规格 |
| --- | --- |
| 列模板 | `repeat(2, minmax(0,1fr)); gap: 8px` |
| 单格宽 | 390→**159px**（108px 内容，余 51px 富余） |
| 7 项落行 | 2+2+2+1 → **末项 `span 2`**（满宽 326px） |
| 320 下限 | W=256，单格 124px；`Upload attachments` 在 12px 下 ≈108+24 = 132 > 124 → 允许 2 行，`min-height` 兜住 |
| 横向溢出 | 硬性 0：容器 `overflow-x` 不得出现滚动条，`scrollWidth <= clientWidth` |

### 2.3 跨列规则：为什么和阈值网格相反（本文最关键的一条）

**判据是余数，不是奇偶。**

```
cols ∈ {2,3,7}；items = 7
7 mod 7 = 0 → 不跨
7 mod 3 = 1 → 末项 span 3（否则尾行只剩 1 个孤项 + 2 个空洞）
7 mod 2 = 1 → 末项 span 2（否则尾行只剩 1 个孤项 + 1 个空洞）
```

阈值网格是 6 项：`6 mod 6 = 6 mod 3 = 6 mod 2 = 0`，**任何列数都整除，所以任何列数都不跨**——
那份规范里的「不启用 `span 2`」是「余数为 0」的结果，不是「偶数」的规则。

**所以「7 是奇数 → 允许末项 span 2」只是余数为 1 时的自然结果**，别把它记成「奇数就跨」：
若哪天白名单变成 9 项，`9 mod 3 = 0` → 3 列时**不该跨**；`9 mod 2 = 1` → 2 列时该跨。
一句话：`remainder === 1` 才启用 `:last-child { grid-column: span <cols> }`，其余情况一律 `auto`。

**为什么不给 768–1279 用 4 列**（看起来更省高度）：768 视口单格只有 `(448−24)/4 = 106px`，
小于「勾选框 + 8 + 7 个汉字 @12px = 108px」→ 每格都换行，7 列里 4 列变两行，行高不齐。3 列是这一档的唯一解。

### 2.4 两种形态是**一个 DOM**，靠 CSS 切换

禁止用 JS `matchMedia` 渲染两套模板：两套 DOM = 两份 `aria-label` 契约 = 两套测试，
且 SSR 首帧与客户端不一致。做法：

- 表头 `<div class="sf-trust-earnable__columns" aria-hidden="true">` 始终渲染，`@media (max-width:1279px){ display:none }`。
- 格内展示名 `<span class="sf-trust-earnable__cell-name">` 始终渲染，`@media (min-width:1280px){ display:none }`。
- 两种形态下**可读名称都来自 `aria-label`**（§7），不依赖可见文字，所以切换不影响无障碍。

---

## 3. 累积语义的三处措辞（T21，硬要求）

`Earnable.Grants(level)` 从 `LevelMember` 循环到 `level` 取并集（`registry.go:55-73`，09:19 实测）：
**勾「TL2 的 `topic.create`」= TL2/TL3/TL4 都解锁**。UI 若写成「该档拥有」，运营会以为只给 TL2。

### 3.1 三处措辞模板（zh-CN / en-US）

| 位置 | zh-CN | en-US |
| --- | --- | --- |
| 矩阵标题（`h3`） | 能力解锁（按档新增） | Capability unlocks (added per level) |
| 常驻行内说明（`p`，标题下、矩阵上） | 勾选某档的权限键，只表示**该档新增**解锁；该档及以上各档同时获得，不需要在更高档重复勾选。 | A checked key is added at that level only. Every level above it inherits it, so higher levels do not need the same key checked again. |
| `aria-label`（每个勾选框） | {档位名} 新增解锁：{键展示名} | Adds for {level name}: {key label} |
| `aria-describedby` 指向的隐藏句 | 该档及以上各档同时解锁 | Unlocks at this level and above |

- 行内说明**常驻**，不进 Toast、不折叠、不悬浮提示（运营要在按下勾选**之前**读到它）。
- 三处必须同时存在：只写 aria-label 满足不了视觉读者，只写说明满足不了屏幕阅读器。

> **注意：常驻说明有两层，不止这一条。** §3.3 的基线说明（「默认安装下这是空操作」）排在它**之前**，
> 两句都常驻、都不折叠；显示顺序与理由见 §3.3.1。本节只定义「机制」那一层。

### 3.2 禁用措辞（出现即打回）

`该档拥有` / `该档的权限` / `TL2 的权限` / `此等级授予` / `本档权限` /
`permissions for this level` / `this level has` / `grants at this level` / `own permissions`。

判据（可写成断言）：矩阵 `innerText` 不得命中 `/该档拥有|该档的权限|此等级授予/`；
`innerText` 必须命中 `及以上`（J-06）。

### 3.3 默认安装下这是空操作：必须披露（researcher 审计 §2.2/§2.3，本刀最高优先级诚实项）

**事实（2026-11-11 09:27 逐条复验，命令见 §11）**：

1. `SeedMemberPermissions`（`seeds.go:157-165`）与 `earnableWhitelist`（`registry.go:26-34`）
   **是同一组 7 个键**，且三层迁移（`202607040001` / `202607120001` / `202607050004`）都把
   它们授予 `member` —— `is_default = TRUE` 的开放注册用户默认用户组。
2. 所以默认安装下勾任意档任意键，**用户实际拿到的权限不变**，运营看不到任何变化。
3. 更硬的一层：`mergeEarnedPermissions`（`postgres_store_roles.go:343-365`）对
   `present[key]` 为真的键直接 `continue`（第 353 行），因此这 7 个键**永远进不了 `Actor.Earned`**。
4. 于是 `GrantSource()`（`permission_origin.go:36-54`）对这 7 个键**只会返回 `static`**，
   永不返回 `earned`。

**只讲累积（§3.1）而不讲这条，等于告诉运营「勾了 TL2 就能让 TL2 用户发帖」——那是假话**：
TL1 用户本来就能发。

#### 3.3.1 与 §3.1 的关系：两条分开、都常驻、顺序固定

| 顺序 | 文本 | 回答的问题 | 视觉形态 |
| --- | --- | --- | --- |
| ① 先读 | 基线说明（本节） | 「这个功能对本站有没有用」 | 左边框 `2px` + `13px/20px`，`i-lucide-info` |
| ② 后读 | 机制说明（§3.1） | 「勾下去会发生什么」 | `text-xs` 纯文本 |

**不合并**的理由：两句是不同心智模型 —— ① 是**前提**（本站当前角色配置可能让这次配置空转），
② 是**机制**（后端 `Grants()` 的累积不变式）。合并会变成 90+ 字的复合句，而 ② 是运营接到
「TL3 怎么也拿到这个权限」投诉时要**反复回看**的那句，被 ① 稀释后不好找。分开还能各自独立断言
（J-06 查 ②；新增断言查 ①，见 §3.3.5）。

#### 3.3.2 措辞模板

zh-CN（`earnable.baseline.title` / `earnable.baseline.text`）：

> **默认安装下这是空操作**
> 默认安装下，这些权限键已经由默认用户组授予，所有人都有：此时勾选任何档位，用户实际拿到的权限都不变，能力来源仍记为「用户组」而不是「等级」。勾选只对「用户组里没有这个键」的用户生效。要让等级解锁真正起作用，先在「权限管理」里把该键从用户组移除，再回来勾选。

en-US：

> **No-op on a default install**
> On a default install these keys are already granted by the default group, so every user has them: checking any level changes nothing they can actually do, and the grant source stays "group" rather than "level". Checks only affect users whose group does not include that key. To make level unlocks real, remove the key from the group under Permission Management first, then come back and check it.

**不写具体数字**（不写「这 7 个键」）：白名单是 API 下发的，写死数字就是第二份副本（T22）。

**不渲染角色别名** `admin.roleCatalog.member.alias`：zh 是「普通会员」，而 TL1 档位名
`admin.settings.trustLevels.level.member` 是「正式用户」，同屏并列会让运营以为是两个用户组。
统一说「默认用户组」，具体是哪个组由运营在「权限管理」页自己认。

**不加跳转链接**：跨页链接会把本组件绑死到 `/control-panel/permissions` 的路由与标题文案上，
而这段说明是**不变量**，不值得为它引入路由耦合。

**视觉规格**（新增两条 scoped 规则，不进变量表、不用 `--sf-*`，后台 slate/zinc 约定）：

| 元素 | 规格 |
| --- | --- |
| 容器 | `border-left: 2px solid` `slate-300 dark:zinc-700`；`padding-left: 12px`；`margin-top: 8px` |
| 标题行 | `13px/20px 600`，`slate-900 dark:zinc-100`，`display:flex; align-items:center; gap:6px; margin-bottom:2px`，前置 `i-lucide-info` `14×14`（`aria-hidden`） |
| 正文 | `13px/20px`，`slate-600 dark:zinc-300`（比机制说明的 `text-muted` 重一档，但**不是**告警色） |

**为什么不用 `UAlert color="warning"`**：颜色通道要留给真正的异常 —— `error` 给 §5 白名单失败，
`warning` 给 §5.3 白名单外残留键。这条是**前提说明**不是异常；给它 warning 会让告警贬值，
真正出错时运营已经对黄色脱敏。左边框 + 提一档字重足够表达「先读这段」，不占用严重性通道。

#### 3.3.3 常驻，不可折叠（判断与理由）

**常驻。** 三条理由：

1. 它是「这个功能对本站有没有用」的**前提**，与 §3.1 同级；藏进「更多」等于把前提藏起来，
   而运营的默认动作是直接开始勾。
2. 折叠控件的默认态没有正确答案：默认折叠 = 藏起来（失效），默认展开 = 折叠没意义（多余控件）。
   **没有正确默认值的控件就不该是控件。**
3. 成本是一次性阅读（约 3 秒），收益是避免一次「以为配好了」的静默无效配置。

#### 3.3.4 无条件常驻，不是条件提示（回答「摘掉之后还显示吗」）

**无条件常驻；而且摘掉之后它依然为真，不需要隐藏。**

- **判据不可得**：本组件唯一数据源是 `eligiblePermissions` —— 只有**键**，没有「谁被授予了」。
  要判断「`member` 还有没有这个键」，得再读 `/admin/permissions` 的角色矩阵并在前端 join
  「角色 × 键」，那是**第二个授权真相源**（与 T22 同一条纪律），且与运营在另一个标签页改角色
  存在竞态（读到 10 秒前的状态）。用会过期的事实决定「要不要说真话」，比一直说真话更糟。
- **措辞本身已含条件**：「默认安装下…」「勾选只对用户组里没有这个键的用户生效」——这两句在
  摘掉之后**依然为真**（默认安装仍是那样，机制仍是这样），所以不需要切换形态。
- **代价**：已摘掉的运营会读到一句对本站「部分不适用」的说明。可接受：它不会让人做错事，
  只会让人多读一句；反过来隐藏会让新人做错事。

#### 3.3.5 断言

| 断言 | 判据 |
| --- | --- |
| 基线说明常驻 | 矩阵 `innerText` 命中「默认用户组」与「只对」；en 命中 `default group` 与 `only affect` |
| 未写成告警 | 基线说明容器**不是** `UAlert`（无 `role="alert"`），且不与 §5 错误条同色 |
| 数字未写死 | 文案内不出现「7 个键」/ `seven keys`（白名单增长后不能变谎） |
| 状态无关 | `status ∈ {loading, ready, failed}` 三种下都渲染（它不依赖白名单数据） |

---

## 4. 空态（默认态，不是错误态）

全部未勾选是**出厂状态**（推荐默认 `earnable = ∅`），必须让人一眼看出「这是正常的」：

| 项 | 规格 |
| --- | --- |
| 触发 | `status === 'ready' && checkedCount === 0` |
| 位置 | 矩阵下方，`margin-top:12px`（在矩阵与「白名单外残留」警告之间） |
| 形态 | **纯文本** `text-xs text-slate-500 dark:text-zinc-400` + `i-lucide-info` `14×14`（`aria-hidden`），**不是** `UAlert`、不用 error/warning 色、不占错误条位置 |
| 文案 zh | 当前所有档位都没有新增解锁：能力仍由用户组静态授予决定，这是推荐默认状态，不是错误。勾选任意一项即开始按等级解锁。 |
| 文案 en | No level adds any unlock yet. Capabilities still come from static group grants; this is the recommended default, not an error. Check any key to start unlocking by level. |
| 矩阵本身 | 5 行 × 7 格**照常渲染**（全空）。禁止「空态就不画矩阵」——那会让运营以为功能没上线 |
| 计数徽标 | 仍显示「新增项 0/28」（默认安装的示例值），与空态文案一致，不隐藏；分母按下方公式算，不写死 |

**计数定义（确认题的回答）**：

- 分母 = `keys.length × TRUST_LEVEL_EDITABLE_LEVELS.length` = 7 × 4 = **28**。它是**可提交的格子总数**，
  不是「可选权限数」——本矩阵里两者恰好相等，因为 TL0 已被排除。
- **TL0 不算**：`config.go` 拒绝 TL0 出现在 `trust.levels` 里，`Grants()` 对 `level <= LevelNewcomer`
  直接返回 `nil`，TL0 的 7 个格子 `disabled` 且永不进 payload。把 TL0 算进去会得到 35，那是错的。
- 分母**必须是算出来的**，不许写死 `28`：白名单增长到 8 键时，写死的数字立刻变谎（与 §3.3.2
  「不写具体数字」同一条纪律）。分子 `checked` = 四档 `earnable` 数组长度之和（去重后按白名单净化过的值）。

---

## 5. 白名单请求失败态（T22：**不许**回落成空矩阵）

空矩阵在运营眼里 = 「后端说没有可勾的键」= 谎。四态必须互不冒充
（数据层已由 frontend 落地：`useTrustLevelEarnableWhitelist()` 的 `status: idle|loading|ready|failed`，
`failed` 时 `keys` 同样是 `[]` —— 所以**禁止**用 `keys.length === 0` 判断空态，只看 `status`）。

### 5.1 失败态规格

| 项 | 规格 |
| --- | --- |
| 触发 | `status === 'failed'`（含请求失败与 `eligiblePermissions` 形状非法 `invalid_response`） |
| 矩阵 | **整块不渲染**（不是渲染成灰的 7 列）→ 换成锁定占位块 |
| 锁定占位 | `div`，`min-height:180px`，`border:1px dashed slate-200 / zinc-800`，`border-radius:8px`，内容居中：`i-lucide-lock` `16×16`（`aria-hidden`）+ 文案「矩阵不可用」`12px slate-500`。`min-height` 是为了不产生布局跳动 |
| 错误条位置 | **紧跟 `h3` 之后**（占原矩阵位置的上方），`UAlert color="error" variant="soft" icon="i-lucide-triangle-alert"`，`class="mt-3"` |
| 错误条标题 | 可勾选的权限键读取失败 |
| 错误条正文 | 矩阵已锁定，避免在没有白名单时误改配置。现在保存不会改动能力解锁配置，阈值仍会正常保存。 |
| 失败原因 | 正文下一行 `text-xs font-mono`：失败原因：{reason}（`reason` = 后端 `reason` 或 `request_failed` / `invalid_response`；**原样显示，不翻译、不美化**，否则 ops 无法定位） |
| 重试 | 错误条内右下 `UButton size="xs" color="neutral" variant="outline" leading-icon="i-lucide-refresh-cw"`，文案「重试」，点击调 `load()` |
| 常驻性 | 错误 Alert **不自动消失**（AGENTS.md：error 不自动关闭），只有 `load()` 成功才消失 |
| 隐藏项 | **机制说明**（§3.1 的 `note`）与计数徽标在失败态**不渲染**（没有可解释的对象，留着是噪音） |
| 保留项 | **基线说明**（§3.3）在 `loading` / `failed` 下**照常渲染**：它不依赖白名单数据，且「本站这些键默认人人都有」这句与矩阵能否加载无关。失败态下它排在错误条**之后**、锁定占位块**之前** |
| 保存按钮 | **保持可用**：`trustLevelsPayload(form)` 不带 `whitelist` 参数时原样带回已存 `earnable`，所以这次保存不会静默清空配置。错误条正文已把这一点说清 |

### 5.2 加载态

`status === 'loading'`：同位置渲染锁定占位的同尺寸占位块（`min-height:180px`），内容
「正在读取可勾选的权限键…」+ `aria-busy="true"`；**不加**骨架微光/动画（视觉规范 §6-13 禁动效）。
SSR 首帧与客户端首帧渲染同一占位块，避免 hydration 差异。

### 5.3 白名单外残留键（frontend 已暴露 `unknownEarnableKeys`，此处定它的位置）

已存配置里可能有白名单外的键（白名单收窄、旧版本写入）。这些键保存时会被清掉（`sanitizeEarnableKeys`），
**必须提前披露**，不能静默丢：

| 项 | 规格 |
| --- | --- |
| 触发 | `status === 'ready' && orphans.length > 0` |
| 位置 | 矩阵下方、空态文案**之后** |
| 形态 | `UAlert color="warning" variant="soft" icon="i-lucide-triangle-alert"`（warning 不自动消失也算可接受：它要求一次确认动作） |
| 标题 zh | 有 {count} 个已存键不在白名单内 |
| 正文 zh | 这些键会被后端拒绝，保存时会从对应档位移除：{keys}（`{keys}` 形如 `TL2 · user.ban`，按档位升序、档内按存储顺序） |
| en | "{count} stored keys are outside the whitelist" / "The backend rejects these keys; saving removes them from their levels: {keys}" |
| 交互 | 只披露，不提供「保留」按钮（保留是不可能的：后端会 422） |

---

## 6. 七个键 → 人话（**不新建映射**，复用既有权限文案）

**核实结论（09:22 实测）**：这 7 个键的 zh/en 展示名与说明**已经存在**于
`admin.permissionCatalog.<键>.label|description`（点号路径即 i18n 嵌套路径），
`usePermissionText().permissionLabel({ key, module })` 已经能解析它们
（`apps/web/app/composables/identity/usePermissionText.ts:11-23`）。
**所以本刀不许新增 `earnable.permission.*` 一类第二份映射**（T22 的同一条纪律）。

| 键 | zh-CN 展示名（现文案） | en-US 展示名（现文案） | 说明（zh，现文案） |
| --- | --- | --- | --- |
| `topic.create` | 发布主题 | Create topics | 创建新的论坛主题。 |
| `post.create` | 发表回复 | Create replies | 在主题下创建回复。 |
| `attachment.upload` | 上传附件 | Upload attachments | 允许登录用户上传附件。 |
| `topic.edit_own` | 编辑自己的主题 | Edit own topics | 编辑自己发布的主题。 |
| `post.edit_own` | 编辑自己的回复 | Edit own replies | 编辑自己发布的回复。 |
| `topic.delete_own` | 删除自己的主题 | Delete own topics | 删除自己发布的主题。 |
| `post.delete_own` | 删除自己的回复 | Delete own replies | 删除自己发布的回复。 |

落地规则：

- 矩阵的列名 / 格内名一律 `permissionLabel({ key, module: '' })`，**不写死中文**、不写死键名。
- 缺键时 `permissionLabel` 回落成**裸键名**（`topic.create`）——可接受降级，但必须有一条测试
  断言这 7 个键在两份 locale 里都 `te()` 命中（否则英文站会看到中文混排或裸键）。
- 展示名与键名**同时显示**（桌面表头两行：名称 + `font-mono` 键名）。理由：运营要在
  `/control-panel/permissions` 权限矩阵里对上同一个键，只给中文名对不上。
- 格内（<1280）**只显示名称**，不显示键名（宽度不够，键名挤成两行更糟）。

### 6.1 复用哪一族（确认题的回答）

**唯一复用族：`admin.permissionCatalog.<module>.<...>.<leaf>.label|description`** ——
`<键>` 按**点号**逐段展开成嵌套对象（`topic.create` → `.topic.create`，
`attachment.upload` → `.attachment.upload`，`topic.edit_own` → `.topic.edit_own`）。

**`admin.roles.permissions` 不是键族**，别拿它当映射：它是权限编辑区的**区块标题**，
zh 值就是两个字「权限」，en 是 "Permissions"（`jq -r '.admin.roles.permissions'` 可验）。
拿它按 key 取值只会得到 undefined。

覆盖情况（09:28 实测）：**7 个键全部命中，两份 locale 齐全，无缺口**。逐键验证命令
（frontend 自己跑一遍，任何人重跑都能复现；`MISSING` 出现即说明键族被改过名）：

```bash
cd apps/web
for loc in zh-CN en-US; do
  for k in topic.create post.create attachment.upload topic.edit_own post.edit_own topic.delete_own post.delete_own; do
    printf '%s %-20s %s\n' "$loc" "$k" \
      "$(jq -r --arg k "$k" '.admin.permissionCatalog | getpath($k|split(".")) | .label // "MISSING"' i18n/locales/$loc.json)"
  done
done
```

期望输出 14 行，全部非 `MISSING`（当前值即上面那张表的「现文案」列）。

**用 `rg` 验存在性不可靠**：i18n 是嵌套 JSON，`rg '"topic.create"'` 在 locale 文件里零命中
（键被拆成 `"topic": { "create": ... }`）。所以验证命令只给 `jq getpath` 这一条，
不提供 `rg` 版本——给了就是一条「看起来能跑、实际必空」的假命令。

**若未来某键缺失**：`permissionLabel` 会回落成裸键名（可接受降级），但**不许**为此新建
`earnable.permission.*` 映射——正确做法是把缺的 `admin.permissionCatalog.*` 补齐（那是权限目录的
职责，也是 `/control-panel/permissions` 同一份文案），并让测试红。

---

## 7. 无障碍

### 7.1 `aria-label` 组合规则（唯一算法）

```
cellLabel(level, key) =
  zh: `${levelLabel(level)} 新增解锁：${permissionLabel(key)}`
  en: `Adds for ${levelLabel(level)}: ${permissionLabel(key)}`
```

- `levelLabel(level)` = `t('admin.settings.trustLevels.level.' + trustLevelSlug(level))`
  （「常驻用户」/「Resident」），**与阈值行同一个键**，不另起一套档位名。
- 三元组顺序固定：**档位 → 语义（新增解锁）→ 键名**。顺序反了会让屏幕阅读器在 35 个勾选框（28 个可操作 + 7 个 TL0 禁用）上
  连续播报同一个键名开头，无法按档位跳读。
- `aria-describedby` 统一指向行内说明的 `id`（`sf-trust-earnable-note`），补上「及以上」的累积语义；
  **不要**把累积语义塞进 `aria-label`（会变成 40+ 字的单句）。
- 每个勾选框 `UCheckbox`（渲染为 `role="checkbox"` 的 button，`type=button`），**禁止**用裸
  `<input type="checkbox">`：`UCheckbox` 的 root 是 `<label>`，整格可点，命中区天然达标。

### 7.2 TL0 行

| 项 | 规格 |
| --- | --- |
| 控件 | 7 个 `disabled` 勾选框照常渲染（几何与其它行一致，**不隐藏**） |
| 语义 | `fieldset[data-level="0"][data-readonly="true"][aria-disabled="true"]`；每个勾选框同时 `disabled` + `aria-disabled="true"`（J-05 断言这两个属性） |
| 说明 | legend 下 `p.__tl0` `text-xs slate-500`，**复用既有键** `admin.settings.trustLevels.tl0Hint`（「TL0 由「新人限制」页决定，此处只读。」）——不新写第二句 |
| aria-label | 仍按 §7.1 生成（内容与其它行同形），保证 35 个勾选框语义同构（TL0 也按同一模板生成，不特殊化） |

### 7.3 焦点与结构

- 焦点环：`focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--sf-accent)]`。
- 每档一个 `<fieldset>` + `<legend>`（沿用阈值行的语义分组），屏幕阅读器进每个勾选框都播报所属档位。
- 图标一律 `aria-hidden="true"`；表头行 `aria-hidden="true"`（信息由 `aria-label` 承担，避免重复播报）。
- 计数徽标 `aria-live="polite"`（勾选后读屏能听到「新增项 3/28」），空态文案不加 `aria-live`（静态初值）。
- 不新增 heading 层级：`h2`（卡头）→ `h3`（本节），矩阵内部不再起 heading。

---

## 8. i18n 键清单（zh-CN 与 en-US 两份都要，路径 `apps/web/i18n/locales/{zh-CN,en-US}.json`）

### 8.1 新增族 `admin.settings.trustLevels.earnable.*`

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `title` | 能力解锁（按档新增） | Capability unlocks (added per level) |
| `note` | 勾选某档的权限键，只表示**该档新增**解锁；该档及以上各档同时获得，不需要在更高档重复勾选。 | A checked key is added at that level only. Every level above it inherits it, so higher levels do not need the same key checked again. |
| `count` | 新增项 {checked}/{total} | Added items {checked}/{total} |
| `empty` | 当前所有档位都没有新增解锁：能力仍由用户组静态授予决定，这是推荐默认状态，不是错误。勾选任意一项即开始按等级解锁。 | No level adds any unlock yet. Capabilities still come from static group grants; this is the recommended default, not an error. Check any key to start unlocking by level. |
| `loading` | 正在读取可勾选的权限键… | Loading the eligible permission keys… |
| `aria.cell` | {level} 新增解锁：{permission} | Adds for {level}: {permission} |
| `aria.cumulative` | 该档及以上各档同时解锁 | Unlocks at this level and above |
| `loadFailed.title` | 可勾选的权限键读取失败 | Could not load the eligible permission keys |
| `loadFailed.description` | 矩阵已锁定，避免在没有白名单时误改配置。现在保存不会改动能力解锁配置，阈值仍会正常保存。 | The matrix is locked so the configuration cannot be edited without the whitelist. Saving now leaves capability unlocks untouched; thresholds still save normally. |
| `loadFailed.reason` | 失败原因：{reason} | Reason: {reason} |
| `loadFailed.retry` | 重试 | Retry |
| `loadFailed.locked` | 矩阵不可用 | Matrix unavailable |
| `orphan.title` | 有 {count} 个已存键不在白名单内 | {count} stored keys are outside the whitelist |
| `orphan.description` | 这些键会被后端拒绝，保存时会从对应档位移除：{keys} | The backend rejects these keys; saving removes them from their levels: {keys} |
| `baseline.title` | 默认安装下这是空操作 | No-op on a default install |
| `baseline.text` | 默认安装下，这些权限键已经由默认用户组授予，所有人都有：此时勾选任何档位，用户实际拿到的权限都不变，能力来源仍记为「用户组」而不是「等级」。勾选只对「用户组里没有这个键」的用户生效。要让等级解锁真正起作用，先在「权限管理」里把该键从用户组移除，再回来勾选。 | On a default install these keys are already granted by the default group, so every user has them: checking any level changes nothing they can actually do, and the grant source stays "group" rather than "level". Checks only affect users whose group does not include that key. To make level unlocks real, remove the key from the group under Permission Management first, then come back and check it. |

### 8.2 复用（不新建）

`admin.settings.trustLevels.level.*`（五档名）、`admin.settings.trustLevels.tl0Hint`、
`admin.permissionCatalog.<键>.label`、`profile.trustLevel.code`（`TL{level}`）、`admin.settings.save`。

### 8.3 必须改写既有键 `admin.settings.trustLevels.restoreHint`

现文案与实际行为不符（§0 末条）。改成：

- zh：恢复默认只重置五档的阈值。每档限额与能力解锁勾选**保持不变**；用户的等级会按新阈值重新判定，已获得的徽章与已发布的内容不受影响。
- en：Restoring defaults resets only the five levels' thresholds. Per-level limits and capability unlock checks are left unchanged. Users' levels are re-evaluated against the new thresholds; earned badges and published content are unaffected.

> 这是**文案对齐代码**，不是改行为。若 reviewer 更愿意改代码去真重置 limits，那是另一刀（会改变运营已配的限额），本刀不做。

---

## 9. 验收断言（reviewer 可直接抄）

| 断言 | 层级 | 判据 |
| --- | --- | --- |
| 7 列 × 5 行，TL0 行 7 个 `disabled` + `aria-disabled="true"`，`fieldset[data-level="0"][data-readonly="true"]` | 组件测试 | J-05 |
| 页面文本含「及以上」且不含「该档拥有」 | 组件测试 | J-06 |
| `status==='failed'` 时：无 `.sf-trust-earnable__columns`、无 `.sf-trust-earnable__cells`、存在 error Alert、存在锁定占位、重试可再调 `load()` | 组件测试（mock 500） | J-07 |
| 每个勾选框 `aria-label` == `{档位名} 新增解锁：{键名}`，`aria-describedby` 指向 note | 组件测试 | 本文 §7.1 |
| 7 个键在两份 locale 的 `admin.permissionCatalog.*.label` 均 `te()` 命中 | 组件测试 | 本文 §6 |
| 桌面 1280：表头 7 列与单元格 7 列同宽（`getBoundingClientRect().width` 逐列相等，误差 ≤0.5px） | 浏览器 | §2.2 |
| 390×844：2 列、末项满宽、`scrollWidth <= clientWidth`、无横向滚动条 | 浏览器 | J-10 |
| 768：3 列、末项满宽、展示名单行不换行 | 浏览器 | §2.3 |
| 勾 TL2 `topic.create` → 保存 → 重载 → TL2 仍勾、TL3/TL4 未被自动勾上 | 浏览器 | J-03 + T21（累积由后端负责，前端不得复制到高档） |
| 基线说明常驻且不依赖白名单：`status ∈ {loading, ready, failed}` 三种下 `innerText` 均含「默认用户组」「只对」；容器不是 `UAlert`（无 `role="alert"`） | 组件测试 | §3.3.5 |
| 基线说明与机制说明**顺序**：基线段落的 DOM 位置在机制说明之前 | 组件测试 | §3.3.1 |
| 计数分母是算出来的：`7 × 4 = 28`，且 `keys` 变长时分母跟着变；TL0 的 7 格不计入 | 组件测试 | §4 |

---

## 10. 不做什么（出现即打回）

1. 不加页头、不加 `UDashboardToolbar`、不加第二个 `UCard`、不自建 tab。
2. 不用 emoji 作图标；图标只走 `UIcon` + lucide（本文用到的全部已核实存在：`info` / `triangle-alert` / `refresh-cw` / `lock` / `sprout` / `leaf` / `house` / `mountain-snow` / `crown`）。
3. 不硬编码 7 个权限键、不新建键→人话映射表（§6）。
4. 不把「该档拥有」写进任何一处文案（§3.2）。
5. 失败态不回落成空矩阵、不把「加载中」画成「全空」。
6. 不用 JS `matchMedia` 切两套 DOM（§2.4）。
7. 不加动效/骨架微光；不加 hover 高亮整列（会把矩阵变成仪表盘）。
8. 不在 390 用横向滚动解决 7 列。
9. 不给 TL0 行加「全选」开关（TL0 不可配置，多一个可点控件就是多一个谎）。
10. 不把累积语义做成 Toast/悬浮提示（必须常驻可读）。
11. 不把 §3.3 的基线说明做成 `warning`/`error` Alert：严重性通道留给真正的异常（§5 / §5.3）。
12. 不折叠 §3.3 的基线说明，也不给它「展开/收起」（没有正确默认值的控件不该存在）。
13. 不为了「要不要显示基线说明」去读 `/admin/permissions` 做角色 × 键的 join（第二个授权真相源 + 竞态）。
14. 不在基线说明里写「7 个键」这类数字，也不渲染 `admin.roleCatalog.member.alias`（与 TL1 档位名撞脸）。

---

## 11. 证据（命令 + 时间，2026-11-11 +08:00）

| 结论 | 命令 | 时间 |
| --- | --- | --- |
| 白名单 7 键字面量与顺序 | `sed -n '1,80p' apps/api/app/Models/TrustLevels/registry.go` | 09:19 |
| `Grants()` 累积（`LevelMember` 起循环到 `level` 取并集） | 同上 55–73 行 | 09:19 |
| `eligiblePermissions` = `EligiblePermissions()`（白名单顺序，过滤 `SeedPermissions` 未知键） | `sed -n '122,134p' registry.go`；`sed -n '135,155p' apps/api/app/Http/Controllers/TrustLevels/controller.go`；`sed -n '60,80p' dto.go` | 09:24 |
| 7 键 zh/en 展示名已存在于 `admin.permissionCatalog.*` | `python3` 逐键解析两份 locale（见下） | 09:22 |
| `permissionLabel` 的解析规则与回落 | `cat apps/web/app/composables/identity/usePermissionText.ts` | 09:22 |
| 后台几何：`p-4 sm:p-6`、侧栏 `default-size=16/min=14` | `sed -n '495,535p;720,726p' apps/web/app/layouts/admin.vue` | 09:21 |
| 阈值行既有几何（legend 24/8/14/11、网格 2/3/6、`gap:12px`） | `read SFAdminTrustLevelsTab.vue`（全文 310 行） | 09:20 |
| 阈值行与 `restoreRecommended()` 只重置 `requirements` | 同上 72–85 行 | 09:20 |
| `UCheckbox size=md` = `base size-4`（16px）/ `container h-5` / `label text-sm` | `sed -n '1636,1700p' node_modules/@nuxt/ui/dist/shared/ui.BfHSFiIO.mjs` | 09:23 |
| lucide 图标名存在 | 逐个查 `node_modules/@iconify-json/lucide/icons.json` | 09:26 |
| frontend 数据层四态已落地（`status/keys/error`、`unknownEarnableKeys`、`toggleEarnableKey`） | `git diff apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts`（工作树，未提交） | 09:25 |
| 基线 | `git rev-parse --short HEAD` → `55ee9c366`；`date -Iseconds` → `2026-11-11T09:24:09+08:00` | 09:24 |
| **`SeedMemberPermissions` = 白名单同一组 7 键** | `sed -n '150,170p' apps/api/app/Models/Identity/seeds.go` | 09:26 |
| **`mergeEarnedPermissions` 对 `present[key]` 直接 `continue`**（已授予的键永不进 `Earned`） | `rg -n "func mergeEarnedPermissions" -A 25 apps/api/app/Models/Identity/*.go` → `postgres_store_roles.go:343-365`，跳过分支在第 353 行 | 09:27 |
| **`GrantSource` 的 earned 出口只在 `a.Earned[permission]` 为真时命中** | `rg -n "func .*GrantSource" -A 20 apps/api/app/Models/Identity/*.go` → `permission_origin.go:36-54` | 09:27 |
| **`member` 是 `is_default = TRUE` 的默认注册用户组** | `sed -n '75,78p' apps/api/database/migrations/202607040001_identity_rbac.sql` | 09:27 |
| **三层迁移都授予了这 7 个键** | `rg -n "topic.edit_own|topic.delete_own|attachment.upload" apps/api/database/migrations/*.sql` → `202607040001`（4 键）+ `202607120001`（edit_own/delete_own）+ `202607050004`（attachment.upload） | 09:27 |
| **`admin.roles.permissions` 是区块标题不是键族** | `jq -r '.admin.roles.permissions, .admin.roleCatalog.member.alias' apps/web/i18n/locales/zh-CN.json` → `权限` / `普通会员` | 09:28 |
| **7 键在 `admin.permissionCatalog` 全覆盖（14 行非 MISSING）** | 见 §6.1 的 `jq getpath` 循环，`wc -l` → 14；`rg -c '"topic.create"' i18n/locales/zh-CN.json` → exit 1（嵌套键，rg 查不到） | 09:28 |

7 键双语解析命令（可复现）：

```bash
python3 - <<'EOF'
import json
keys=["topic.create","post.create","attachment.upload","topic.edit_own","post.edit_own","topic.delete_own","post.delete_own"]
def resolve(d,path):
    cur=d
    for seg in path.split('.'):
        if isinstance(cur,dict) and seg in cur: cur=cur[seg]
        else: return None
    return cur
for loc in ['zh-CN','en-US']:
    d=json.load(open(f'apps/web/i18n/locales/{loc}.json'))
    for k in keys: print(loc,k,resolve(d,f'admin.permissionCatalog.{k}.label'))
EOF
```

**未实测项（诚实交代）**：§2 的所有像素数是**读码推算**（`W = 视口 − 侧栏 − 48 − 48`），
**不是浏览器截图实测**。本文没有开浏览器：需要真实渲染才能确认的三件事——(a) 1280 视口下
`Upload attachments` 在 12px 是否真的落在 108px 内；(b) `UCheckbox` 的 root `<label>` 在整格
`padding:6px 8px` 下命中区是否达到 32px 高——由 reviewer 在 M13 的 desktop + `390×844` 浏览器
实测里一并确认（矩阵 §7 的 J-10 已含该断言）。(c) 390 视口下 `UDashboardSidebar` 是否真的覆盖在内容之上
而不占列宽（`layouts/admin.vue:521-527` 只声明了 `collapsible resizable`，移动端行为未读源码证实）；
若它实际占宽，390 的可用宽要减掉侧栏，2 列仍成立（单格会更窄，`min-height` 与 2 行文本已兜住）。若 (a) 不成立，降级方案是表头名允许 2 行（本文已允许，
表头用 `align-items:end` 保底），**不要**因此把列数改成 6。

---

## 12. 改动记录（本文自身的版本）

| 时间 | 改动 | 触发 |
| --- | --- | --- |
| 09:18–09:30 | 初稿：§1 落位、§2 几何（含 §2.3 余数规则）、§3.1–3.2 措辞、§4 空态、§5 失败/加载/残留三态、§6 键→人话、§7 无障碍、§8 i18n、§9 断言、§10 反面清单、§11 证据 | lead 派工（M10） |
| 09:26–09:28 | **新增 §3.3「默认安装下这是空操作」**（5 个分节：与 §3.1 的顺序、措辞模板、常驻判断、无条件常驻的判断与代价、断言）；§3.1 末尾加「常驻说明有两层」交叉引用；§4 补计数分母定义（28 = 7×4，TL0 不计入，禁止写死）；§5.1 拆「隐藏项 / 保留项」（基线说明在失败态照常渲染）；**新增 §6.1** 指明唯一复用族 `admin.permissionCatalog.<点号键>.label`、给 `jq getpath` 复现命令、说明 `admin.roles.permissions` 不是键族、并声明不给 `rg` 版本（`rg '"topic.create"'` 实测 exit 1）；§7.1/§7.2 的「35 个控件」改口径为「35 个勾选框（28 可操作 + 7 个 TL0 禁用）」；§8.1 增 `baseline.title` / `baseline.text`；§9 增三条断言；§11 增 8 行证据 | lead 反馈：researcher 审计 §2.2/§2.3（`Earned` 恒空、`GrantSource` 恒 `static`）+ 两处确认题 |
