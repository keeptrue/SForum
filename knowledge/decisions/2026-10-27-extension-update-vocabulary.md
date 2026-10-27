# 决策：扩展管理「更新」的三态词汇与行结构

Status: accepted（2026-10-27 设计定稿，T2 实现前以此为准）
Date: 2026-10-27
Related: `2026-10-27-extension-update-badges-admin-surface.md`（商城徽章经 Admin
Surface `list_column` 渲染）、`2026-10-24-app-store-out-of-tree.md`（Core 不承载商城
目录）、`2026-10-26-disposition-grant-exact-artifact.md`（「已下载未生效」= 精确制品
语义）。上游计划：本批 T1 任务书 §6 文案清单。

## 背景

扩展管理三页（`/extensions`、`/extensions/plugins`、`/extensions/themes`）同时存在
两个几乎同名的更新信号：

- **Core 的「待升级 vX」**：本地已暂存（staged）新制品，尚未生效。
- **商城的「可更新至 vX」**：远端目录里有更高版本，Core 不解析、不比较。

两者都是行内短徽章、都带「vX」版本号、动词都是"升级"，在同一个身份簇里读作同一件事。
更糟的是 Core 这一侧并不区分「版本变高」与「版本没变但包内容变了」——只要存在 staged 就
渲染徽章。

### 实测（2026-10-27，dev 库 `extensions` × `extension_versions`）

| 项 | 数量 |
| --- | --- |
| 已安装扩展 | 30 |
| 有 staged 制品 | 12 |
| staged 版本**更高** | 2（`sforum.page-loader-reference` 1.0.0→1.0.1、`sforum.web-push` 1.0.6→1.0.7） |
| staged 版本**相同**、摘要不同 | 10（9 插件 + 1 主题） |
| staged 版本**更低** | 0 |

9 个「同版本」插件里 6 个 `source=builtin`、3 个 `source=uploaded`。

即：**12 行里有 10 行的徽章写着「待升级 vX」，而 X 恰好就是当前版本**（例如
`sforum.smtp` 当前 v1.1.7、徽章也是 v1.1.7）——新手读到的是"升级到当前版本"。英文侧
更直接：`admin.extensions.stagedVersionBadge` 是 `Upgrade available v{version}`，与
商城贡献的 `可更新至 v1.7.0` 字面同义，两个来源在语言层面无法区分。

根因不是文案不够准，而是**一个词承担了两组区分**：

- (a) 版本变高 vs 版本没变、内容变了；
- (b) 本地已下载（事实，可立即应用）vs 远端目录更高（建议，需要去下载）。

## 决策 1：一个「有更新」，按动作拆三态

Core 只承认一个概念：**这一行有一个已下载、尚未生效的制品**。它按"要做什么才能让它
生效"呈现为三种，而不是按"版本号变没变"分成两种来源：

| 术语 | 含义 | 归属 |
| --- | --- | --- |
| **可升级 v{version}** | 已下载制品的版本**高于**当前运行版本；应用后版本号会变 | Core |
| **重启后生效** | 版本号不变、包内容变了（插件）；重启运行时后才生效 | Core |
| **激活后生效** | 版本号不变、包内容变了（主题）；激活后才生效 | Core |
| **商城有新版** | 远端目录有更高版本；**只是建议**，需要去商城下载，Core 不解析其版本号 | 扩展（商城） |
| **只看有更新** | 列表视图筛选，不是设置 | Core |

判定顺序固定，前一步不成立才看下一步：

1. **制品不可用**（`isExtensionArtifactAvailable` 为假）→ 不出更新徽章，只出「制品缺失」。
   理由：此时任何应用动作都会失败，给徽章等于给死路。
2. **无 staged** → 无更新。
3. **版本比较**（semver，含 prerelease；非法版本不猜）：
   - `staged > 当前` → 可升级 v{staged.version}
   - `staged < 当前` → 无更新（静默；回滚残留，不催管理员做任何事）
   - `staged == 当前` → 看第 4 步
4. **摘要比较**（`packageDigest`）：
   - 不同 → 插件=重启后生效 / 主题=激活后生效
   - 相同 → 无更新（防御分支：同版本同摘要不应出现 staged）

### 状态机表

| # | 条件（自上而下求值） | 徽章 | 主按钮 | 计入「全部升级」 | 计入「{m} 个内置更新待重启」 | 计入「只看有更新」 |
| --- | --- | --- | --- | --- | --- | --- |
| A | 制品不可用 | 无（显示「制品缺失」） | 管理（禁用） | 否 | 否 | 否 |
| B | 无 staged | 无 | 管理 | 否 | 否 | 否 |
| C | `staged > 当前`（插件） | 可升级 v{staged} | 立即升级（停用行：升级并启用） | 是 | 否 | 是 |
| D | `staged > 当前`（主题） | 可升级 v{staged} | 激活 / 重新激活 | 否（主题不批量） | 否 | 是 |
| E | `staged == 当前` 且摘要不同（插件） | 重启后生效 | 重启（停用行：重启并启用） | 否 | 是（仅 `source=builtin`） | 是 |
| F | `staged == 当前` 且摘要不同（主题） | 激活后生效 | 激活 / 重新激活 | 否 | 否 | 是 |
| G | `staged < 当前` | 无 | 管理 | 否 | 否 | 否 |
| H | `staged == 当前` 且摘要相同 | 无 | 管理 | 否 | 否 | 否 |
| I | 任一侧 semver 非法 | 无（不猜） | 管理 | 否 | 否 | 否 |

A/H/I 是防御分支（B–G 之外必须闭合），不是新产品语义。G 的静默是有意的：staged 更旧
通常是回滚后的残留，管理员不需要为它做任何事；要排查时看详情侧栏与生命周期记录。

三态徽章与主按钮的对应关系是**动作级**的，不是装饰级的：

| 徽章 | 图标 | 主按钮 | 按钮图标 |
| --- | --- | --- | --- |
| 可升级 v{staged} | `i-lucide-package-plus` | 立即升级（主题：激活） | `package-plus`（主题：`play`） |
| 重启后生效 | `i-lucide-rotate-cw` | 重启 | `rotate-cw` |
| 激活后生效 | `i-lucide-play` | 激活 / 重新激活 | `play` / `refresh-cw` |

即"会发生什么"（徽章）与"点哪里"（按钮）在图标上成对，用户不必先读懂文案再找按钮。

## 决策 2：行内徽章顺序与层级

徽章不是平铺的清单，分五层，顺序固定：

| 层 | 内容 | 回答的问题 | 样式 |
| --- | --- | --- | --- |
| L0 状态 | 已启用 / 已停用 / 已安装 / 当前主题 | 它现在是什么 | `UBadge` subtle + `statusColor` |
| L1 运行态（仅插件页） | 运行中 / 启动中 / 降级 / 失败 / 已停止 | 进程健康吗 | `UBadge` subtle + `runtimeColor` |
| L2 更新簇 | 核心更新徽章 **或** 制品缺失（互斥） | 有什么待办 | warning subtle（可升级）/ warning outline（重启后·激活后）/ error subtle（缺失） |
| L3 扩展链接 | 商城徽章 | 外面还有什么 | 文字级链接（见下） |
| L4 类型 / 来源 | 插件 / 主题；主题的「内置」 | 这是哪一类 | outline / subtle |
| L5 辅助 | 待清理、协议弃用、设置、熔断、内存、内置维护、自动更新已关闭 | 细节 | subtle |

- L1 保留在 L2 之前：健康问题优先于"有待办"（已拍板）。这条不改变任务书给的顺序——
  那条约束的是**更新簇内部与类型徽章**的相对位置。
- L2 只允许一个徽章：有核心更新就不出制品缺失，反之亦然（A 已抑制更新徽章）。
- L4 按页取舍：总览页渲染「插件/主题」类型徽章；插件页不渲染（页面即类型）；主题页用
  「内置」来源徽章占这一层。
- 核心更新徽章是**纯文本**，永远不做成可点击徽章；一旦可点，它就变成按钮里的链接。

### 行结构（桌面）

```text
┌ 行（grid：minmax(0,1fr) auto）─────────────────────────────────────────────┐
│ [图标] 扩展名称 [状态] [更新簇] [商城 ↗] [类型]              [主按钮] [⋯] │
│        描述（两行截断）                                                    │
│        id · v1.3.5 · 12 项能力声明                                        │
│        作者：X ↗                                                          │
└───────────────────────────────────────────────────────────────────────────┘
```

总览页的行是可选中行，DOM 顺序必须是（`SFAdminExtensionIdentity` 现有结构）：

```text
<div class="flex flex-wrap">
  <button aria-pressed>   图标 · 名称 · 状态 · 更新簇      ← 行选择热区
  <a>                    商城徽章 ↗                        ← 必须是按钮的兄弟
  <UBadge>               类型
  <UBadge>               制品缺失 / 待清理 …
</div>
```

类型徽章落在按钮外，是「扩展链接在类型之前」这条顺序的直接后果；它是纯文本，不影响
行选择的可理解性。

### 商城徽章（L3）的降级

从"和核心徽章平级的 warning 徽章"降为"和「作者：X ↗」同档的文字链接"：

- 排版：`inline-flex items-center gap-1 text-xs font-medium`，正文色 `slate-600` /
  `zinc-300`；hover 时 `text-[var(--sf-accent)]` + `underline underline-offset-2`。
- 图标：文案之后 `i-lucide-arrow-up-right`（`size-3`，`aria-hidden`）。
- 有深链 → `<a href>`；无深链 → 同样的排版但不可点、无下划线、`cursor-default`。
- 来源说明：`title` / `aria-label` = 「{label} · 来自 {sourceExtensionId}」。来源取
  surface 的扩展 id 原值，不查展示名——Core 不认识商城。
- 文案由扩展拥有（如「可更新至 v1.7.0」），Core 只负责"它是次级链接"这件事。

允许与核心更新徽章同时出现（本地 1.0.1 待应用 + 远端 1.0.2）。此时**不保证**商城版本
高于核心版本：Core 不解析商城版本号，两者是独立事实，不做比较、不做合并。

## 决策 3：每行两个控件

行尾只留一个上下文主按钮 + 一个 `⋯` 菜单，取代现在最多 7 个平铺按钮（插件页/总览页）。

### 主按钮映射

| 行状态 | 主按钮 | 颜色 / 变体 |
| --- | --- | --- |
| 可升级（插件，可执行） | 立即升级 | warning subtle |
| 可升级（**停用**插件） | 升级并启用 | warning subtle |
| 可升级（主题） | 激活 / 重新激活（按 `themeActionState`） | primary / neutral subtle |
| 重启后生效（已启用插件） | 重启 | neutral subtle |
| 重启后生效（**停用**插件） | 重启并启用 | neutral subtle |
| 激活后生效（主题未启用） | 激活 | primary |
| 激活后生效（当前主题） | 重新激活 | neutral subtle |
| 无更新（插件已启用 / 当前主题） | 管理 | neutral ghost |
| 无更新（插件未启用 / 主题未启用） | 启用 / 激活 | primary |
| 制品缺失 | 管理（禁用） | neutral ghost |

- **「重启并启用」「升级并启用」是本批新增的两个按钮文案**：`LifecycleService.Restart`
  以 `Enable` 收尾（`apps/api/app/Models/Extensions/service_restart.go:109-118`），对停用
  插件点「重启」会把它**启用**。停用插件的版本升级也走这条路——`extensionUpgradeLifecycleAction`
  对非 `enabled` 的插件本来就返回 `restart`（`apps/web/app/utils/admin/adminExtensions.ts:800-807`）。
  停用行把这件事写在按钮上，而不是藏进 hover title（已拍板）。
- 主按钮不可执行时（`canUpgradePlugin` / `canRestartPlugin` 为假）保持可见但禁用，
  `title` 说明原因；不换成"点下去必然失败"的可用按钮。
- 主题的按钮动词沿用现有 `themeActionState`（激活 / 重新激活 / 恢复默认主题），不新造
  「应用更新」：徽章说"会发生什么"，按钮说"现在做什么"。

### `⋯` 菜单

顺序固定，分隔线只在后面还有项时渲染：

| # | 菜单项 | 图标 | 适用 | 可见 / 禁用 |
| --- | --- | --- | --- | --- |
| 1 | 管理 | `settings` | 全部 | 制品缺失时禁用（现有 `artifact.actionUnavailable`） |
| 2 | 存储实例… | `database` | 插件 | 仅声明了多实例 `attachment.storage.provider` 的插件；未启用时禁用 |
| 3 | 启用 / 禁用 | `play` / `pause` | 插件 | 二选一；制品缺失时禁用 |
| 4 | 重启 | `refresh-cw` | 插件 | 不可重启时禁用（现有 `restartUnavailable`） |
| 5 | 生命周期记录 | `history` | 插件 | 始终 |
| — | ───────── | — | — | 仅当第 6 项可见 |
| 6 | 卸载 | `trash-2`，error | 全部 | 沿用各页现有可见性判定 |

- 菜单是"完整清单"，主按钮是"当前最可能的动作"的快捷方式。**允许重复**（重启既是主
  按钮又是菜单项），但菜单项位置不随状态漂移（唯一例外是启用/停用二选一）。
- 主题行的菜单收敛为：管理 / ─── / 卸载。若卸载不可见，菜单只剩「管理」——此时**仍然渲染
  `⋯` 触发器**：菜单永远渲染，且至少含「管理」，不因主按钮换了动作而丢掉管理入口。
  （2026-10-27 Lead 覆盖原规格「只有重复项时不渲染触发器」；理由见「已拍板」第 5 条。）
- 卸载可见性三页判定不同（`canRequestExtensionUninstall` vs `isDeletable && source !==
  'builtin' && !isSystem`），本批不改：那是权限与生命周期语义，不是视觉问题。

## 决策 4：工具栏三件套

| 页面 | 左区（状态与视图） | 右区（动作） |
| --- | --- | --- |
| 总览 | 已安装 {n} 个扩展 · {m} 个内置更新待重启 · 只看有更新 | 全部升级（N）· 清理缺失制品（K）· 版本 GC · 检查更新 · 刷新 · 上传 |
| 插件 | {n} 个插件 · {m} 个内置更新待重启 · 只看有更新 | 内置自动更新 · 全部升级（N）· 检查更新 · 刷新 · 检查器 |
| 主题 | {n} 个主题 · 只看有更新 | 检查更新 · 刷新 |

分区原则：**左区回答"我现在看到的是什么"，右区回答"我能做什么"**。筛选是视图状态，
不是动作，因此进左区；这同时避免它紧邻「全部升级」这种批量数据操作被误读。

- **全部升级（N）**：N = 同时满足 `kind = version` 且可执行（`canUpgradePlugin`）的插件数。
  只计插件——主题激活会切换公开站点外观，必须逐个确认。N = 0 时不渲染。
- **{m} 个内置更新待重启**：m = `source = builtin` 且 `kind = content` 的行数。纯文本，
  不是按钮：重启是逐行决定（见「不做」），做成按钮会暗示"一键重启全部"。m = 0 时不渲染。
  口径已拍板为内置（dev = 6），保留「内置」二字；若将来改成统计全部内容更新，文案必须
  同步去掉「内置」。
- **只看有更新**：`USwitch`，默认关，不持久化（每次进页面重置）。命中条件 = 核心更新
  状态非空 **或** 有商城徽章。始终渲染（即使当前 0 个更新），避免工具栏随数据跳动；
  0 个更新时打开筛选会得到空态，空态自己解释原因。
- **检查更新**：保留现有位置与显隐条件（`hasListColumns`）。
- 插件页已有「内置插件自动更新」开关，与「只看有更新」外形相似但语义不同：前者写服务器
  设置，后者只改视图。两者分处左右两区，并在 `title` 里各自说明。

## 决策 5：筛选、空态与选中

- 筛掉的如果是当前选中行：**清空选中**，详情侧栏回到空态。不自动改选下一行（自动改选
  会让"我刚才在看什么"变成猜谜）。
- 关闭筛选不恢复上次选中。
- 列表空态（筛选命中 0，但库里有扩展）：
  - 正常：标题「所有扩展都是最新版本」，说明「关闭「只看有更新」可查看全部 {count} 个
    扩展。」，动作「显示全部」（就地关闭筛选）。
  - 有更新检查失败（`surfaceFailures` 非空）：标题降级为「当前筛选下没有更新」，说明
    「部分更新检查未完成，结果可能不完整。」。**不宣称"最新版本"**——检查没跑完就说
    "最新"是假话。
- 列表空态（库里没有扩展）：沿用现有 `emptyTitle` / `emptyDescription`，不新增文案。
- 详情侧栏空态（未选中，或选中被筛掉）：紧凑块，图标 + 「未选择扩展」+「在左侧列表
  选择一行查看详情。」。不用 `SFEmptyState`（那是 `p-10` 的整块空态，塞进 420px 侧栏
  会喧宾夺主）。

## 决策 6：详情侧栏

`dl` 里把原来的「版本」与「待激活版本」两行，换成并排的两行：

| 行 | 标签 | 值 | 出现条件 |
| --- | --- | --- | --- |
| 1 | 当前版本 | `v{version}` | 始终 |
| 2 | 新版本（已下载，尚未生效） | `v{staged.version}`；版本相同时追加小字「内容已更新」 | `kind ≠ none` |
| 2′ | （值下方一行，muted） | `包摘要 {digest 前 8 位}`，`title` 为完整摘要 | `kind = content` |

- 版本相同时，"新版本 v1.3.5" 与"当前版本 v1.3.5" 并列会像 bug，所以必须带「内容已更新」
  这 4 个字；摘要行是"同版本凭什么算更新"的唯一证据。
- `kind = none`（含 staged 更旧）时只显示当前版本，不显示第二行。
- 「已下载」对开发态（本地构建产物）不精确，对外部操作者成立；若将来 dev 场景需要，
  再改「已就绪」，本批不改。

## 决策 7：390×844

| 元素 | ≥ sm | 390 |
| --- | --- | --- |
| 计数文字 | 显示 | 隐藏，仅图标（现状） |
| 内置更新提示 | 显示 | 隐藏（信息由行内徽章承担） |
| 只看有更新 | 图标 + 文字（文字 ≥ xl）+ 开关 | 图标 + 开关，文字隐藏，`aria-label` 必填 |
| 主按钮 | 图标 + 文字 | **默认保留文字**：390 可用宽约 358px，两个控件最宽约 170px，放得下 |
| 主按钮（兜底） | — | 若与 `⋯` 换行超过一行，优先折叠文案为纯图标，且 `aria-label` + `title` 必须同时存在 |
| 徽章簇 | `flex-wrap` | `flex-wrap`；**禁止**改成 `flex-col`；单个徽章 `whitespace-nowrap` 不截断，截断只由名称承担 |
| 行布局 | `[minmax(0,1fr)_auto]` | 单列，控件换到第二行、左对齐 |
| 详情侧栏 | `xl` 起右侧 420px | 列表下方全宽 |

验收口径：`documentElement.scrollWidth = 390`，无横向溢出，无被裁切的控件。

## 决策 8：可访问性

1. **`<a>` 不嵌 `<button>`**：总览页的商城链接与类型徽章都在行选择按钮之外；核心更新
   徽章是纯文本，放在按钮内合法。任何徽章都不得变成可点击控件。
2. **`⋯` 菜单键盘可达**：触发器是可聚焦按钮，`aria-label` 与 `title` 同为「更多操作：{name}」，
   由 `UDropdownMenu` 提供 `aria-haspopup` / `aria-expanded`，方向键移动、Esc 关闭并
   归还焦点；不手写 `keydown`。禁用项保留 `title` 说明原因。
3. **开关语义**：`USwitch` 自带 `role=switch` 与 `aria-checked`；`aria-label` 必须含
   状态与数量——「只看有更新 · {count} 个扩展有更新」（0 时为「… · 没有更新」），
   这样读屏用户不必回到工具栏读那行提示文字。
4. **焦点可见**：链接与按钮统一 `focus-visible:ring-2 ring-[var(--sf-accent)]
   ring-offset-2`（现有 token），不为新控件另造焦点样式。
5. **两行版本用 `dl/dt/dd`**（现状已是 `dl`），读屏能念出「当前版本 1.3.5」「新版本
   （已下载，尚未生效）1.3.5」。
6. **颜色不是唯一信号**：三态各有文字，不只靠 warning 色区分。

## 决策 9：文案清单

zh-CN / en-US 一一对应；建议键名前缀 `admin.extensions.update.*`（T2 可调整）。

| 用途 | zh-CN | en-US |
| --- | --- | --- |
| 可升级徽章 | 可升级 v{version} | Upgrade available v{version} |
| 重启后生效徽章 | 重启后生效 | Restart to apply |
| 激活后生效徽章 | 激活后生效 | Activate to apply |
| 筛选开关 | 只看有更新 | Only with updates |
| 筛选开关 aria | 只看有更新 · {count} 个扩展有更新 | Only with updates · {count} extensions have updates |
| 工具栏提示 | {count} 个内置更新待重启 | {count} built-in updates waiting for restart |
| 主按钮（版本） | 立即升级 | Upgrade now |
| 主按钮（停用行·内容更新） | 重启并启用 | Restart and enable |
| 主按钮（停用行·版本升级） | 升级并启用 | Upgrade and enable |
| 来源说明 | 来自 {extension} | From {extension} |
| 侧栏第一行 | 当前版本 | Current version |
| 侧栏第二行 | 新版本（已下载，尚未生效） | New version (downloaded, not active) |
| 同版本补充 | 内容已更新 | Content updated |
| 筛选空态标题 | 所有扩展都是最新版本 | Everything is up to date |
| 筛选空态说明 | 关闭「只看有更新」可查看全部 {count} 个扩展。 | Turn off “Only with updates” to see all {count} extensions. |
| 检查失败空态标题 | 当前筛选下没有更新 | No updates in this view |
| 空态动作 | 显示全部 | Show all |
| 菜单触发器 | 更多操作：{name} | More actions for {name} |
| 侧栏空态 | 未选择扩展 / 在左侧列表选择一行查看详情。 | No extension selected / Select a row in the list to see details. |

徽章 `title`（hover 解释，纯补充，不承载唯一信息）：

| 徽章 | title |
| --- | --- |
| 可升级 | 已下载 v{staged}，当前运行 v{current}；升级后生效。 |
| 重启后生效（已启用） | 版本仍为 v{version}，包内容已更新（摘要 {digest8}）；重启插件后生效。 |
| 重启后生效（停用） | 插件当前停用；重启会应用已下载的制品并启用它。 |
| 激活后生效 | 版本仍为 v{version}，主题包内容已更新（摘要 {digest8}）；激活后生效。 |

### 禁用词（仅约束 Core 拥有的文案）

`待升级`、`待激活版本`、`升级到当前版本`、`可更新至`、`更新可用`、`有新版本可用`。

「可更新至 vX」属于**扩展**文案（商城自己写），不算违规；Core 不得复制这套词，也不得
解析它。

## 不做

- 不做第四种状态（如「更新已就绪」「待应用」）：三态已经覆盖"版本变了/内容变了/不用管"。
- 不做批量重启：`Restart` 对 legacy 插件会先停用再启用（`service_restart.go:90-118`），
  批量执行等于一次可控性很差的批量停机。
- 不做批量主题激活：会切换公开站点外观。
- 不持久化筛选状态，不做服务端排序/筛选。
- 不让 Core 解析或比较商城版本号，不新增目录、表、API。
- 不新增 Provider Slot 或商城专用接口。
- 不重构运行态/协议/内存等既有辅助徽章（L5 降噪另开一批）。
- 不改三页的卸载可见性判定。

## 后果

- dev 库立即从"10 行升级到当前版本"变成 2 行可升级 + 9 行重启后生效 + 1 行激活后生效；
  工具栏「全部升级（2）」。
- 「{m} 个内置更新待重启」在 dev 显示 **6**，而筛选命中 **12**。差的 6 行是：3 个非
  内置同版本包（上传/开发源）、2 个版本升级、1 个主题内容更新。这不是 bug，是"内置
  自动更新通道"与"所有更新"两个口径；数字与词面绑定。
- 行内控件从最多 7 个降到 2 个，管理/卸载多一次点击；高频动作由主按钮承担。
- 商城徽章从行内徽章降为文字链接，视觉权重下降，点击率会降——这是有意的：它是
  次要路径，Core 不对它的版本号负责。
- 删除 `stagedVersion` / `stagedVersionBadge` / `stagedVersionHint` / `upgradeUnavailable`
  是破坏性 i18n 变更，需同步 `tests/validate-staged-extension-contracts.js`（T2 已列）。
- 纯前端派生，无 API/OpenAPI/DB 变更；旧宿主不受影响。

## 已拍板（Lead, 2026-10-27）

1. **停用行的主按钮要写明副作用**：内容更新 →「重启并启用」，版本升级 →「升级并启用」。
   依据：两条路径都以 `Enable` 收尾（`service_restart.go:109-118`；停用插件的升级在
   `extensionUpgradeLifecycleAction` 里本就返回 `restart`，`adminExtensions.ts:800-807`）。
2. **提示计数口径 = 内置**（dev = 6），保留「内置」二字。
3. **运行态徽章留在核心更新之前**（健康优先），不下沉到 L5。
4. **存储实例配置固定在 `⋯` 第 2 位**（管理之后），不与卸载相邻。
5. **`⋯` 永远渲染，至少含「管理」**（Lead, 2026-10-27，覆盖决策 3 原规则）：主题行即使只剩
   与主按钮重复的「管理」也保留触发器。理由：主按钮随更新状态切换动作（当前主题带内容更新
   时变成「重新激活」、内置默认主题未激活时是「恢复默认主题」），抑制触发器会让这些行失去
   唯一的管理入口。

## 重开条件

- 需要"全部更新"跨页聚合、按更新状态排序，或把更新状态暴露到通知/公开页时。
- 引入更新频道（stable/beta）或"自动应用更新"时。
- 商城徽章需要参与排序、筛选或计数（当前 Core 不解析其版本号，做不到）。
- 主题需要批量应用时。

## 证据

- 页面与组件：`apps/web/app/pages/admin/extensions/index.vue`（工具栏 300-385、行 476-643、
  徽章 slot 494-524、控件 527-642、详情侧栏 647-705、staged 行 667-679）、
  `plugins.vue`（工具栏 267-437、行 482-708、徽章 496-576、控件 613-707）、
  `themes.vue`（工具栏 116-140、行 176-281、徽章 190-218、控件 224-280）、
  `SFAdminExtensionIdentity.vue`（按钮/链接分离 44-81）、
  `SFAdminExtensionUpdateBadges.vue`（32-54）。
- 派生谓词：`apps/web/app/utils/admin/adminExtensions.ts`（`AdminExtensionVersion` 379、
  `isExtensionArtifactAvailable` 699、`themeActionState` 747-755、`canRestartPlugin`
  791-794、`canUpgradePlugin` 796-798、`extensionUpgradeCandidates` 809-811）、
  `builtinAutoUpdate.ts`（`shouldShowBuiltinAutoUpdateIncomplete` 43-51）。
- 后端语义：「重启以 Enable 收尾」`apps/api/app/Models/Extensions/service_restart.go:90-118`；
  主题激活会晋升 staged `theme_publication_postgres.go:55-112`。
- 现有文案：`apps/web/i18n/locales/zh-CN.json:1709-1711`（`stagedVersion*`）、
  `en-US.json:1669-1671`（`Upgrade available v{version}`）。
- 数据：dev 库 `extensions` × `extension_versions` 直查（2026-10-27，designer 复核）：
  30 行 / 12 行有 staged / 2 行版本更高 / 10 行同版本不同摘要 / 0 行更低；
  9 个同版本插件中 6 个 `builtin`、3 个 `uploaded`。
- 相关决策：`2026-10-27-extension-update-badges-admin-surface.md`、
  `2026-10-24-app-store-out-of-tree.md`。
