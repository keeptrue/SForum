# 2026-11-12 徽章管理页：SelectItem 空串崩溃修复 + 收尾取证

承接 `sessions/2026-11-11-trust-limits-and-badges.md` 之后的三分区徽章页收尾。
本轮只动 `apps/web`，`apps/api` 零改动。

## Changed

- `components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue`：四个筛选 `USelect` 的
  items 首项由 `value: ''` 改为组件内哨兵 `ALL_FILTER = 'all'`；新增
  `filterSelectModel(value)` 与 `onFilterSelect(key, event)` 两个小助手，`w-*` 宽度、
  `aria-label`、顺序、`clearFilters` 全部未动。修掉
  `A <SelectItem /> must have a value prop that is not an empty string` 及连带的
  `SelectContent` update 报错。
- `pages/admin/badges.vue`（`#right` 槽）：窄屏按钮改图标-only —— 两个 `UButton` 的可见
  文字包进 `<span class="hidden sm:inline">`，补 `:aria-label` / `:title`（同一 `t()` 键），
  新增 `recomputeLabel` computed 让重算按钮**两态**（`recompute.action` / `recompute.confirm`）
  都有可读名。图标、toolbar 的 border/px/py/text 类名、左槽统计均未动。
- `SFAdminBadgesBadgesTab.vue` **读侧白名单归一**：`filters` computed 里 `group` 只认
  `/^\d+$/` 或 `'none'`，`tier` / `grantMode` / `enabled` 各认枚举，其余归 `''`。
  修的是读写不对称：手改 `?group=all` 会让 `filterBadges` 把 'all' 当分组 id 比对
  （一条不剩落 noMatch），`?enabled=all` 则「不过滤但 `badgeFiltersActive` 为真」，
  凭空冒出「清除筛选」。
- `SFAdminBadgesBadgesTab.vue` **`applyQuery` 归一**：`route.query` 的值是
  `string | null | string[]`，原来标成 `Record<string, string>` 是假的。新增
  `normalizeQuery`（数组取首项、非字符串与空串丢弃），`router.replace` 只吃归一后的
  对象——`?q=a&q=b` 不会再因为 truthy 带着重复 key 弹回地址栏。
- `pages/admin/badges.vue`：`blockedActive` 上移到 `tabs` 之前。原来声明晚于引用，靠
  computed 惰性求值侥幸不炸，谁加 `immediate: true` 或在 setup 里读 `tabs.value` 就是
  TDZ ReferenceError。
- `SFAdminBadgeTable.vue`：三处 `sortable: SORTABLE.includes('name')` 是自己跟自己比
  （恒 true），改 `sortable: true` 并删掉随之失去用处的 `SORTABLE` 常量。
- `tests/admin/adminBadgesTabs.test.ts`：**回归测试从源码字符串改成行为断言**。照
  grants tab 的 `compileVueSfc` + `mount` harness 挂真实 SFC，stub
  `useRoute`/`useRouter`/`USelect`，8 条新用例：四个下拉的 items 里没有任何空串
  value、空 query 时 model 是哨兵、选「全部」→ `replace` 收到的 query **不含该 key**
  且任何值都不等于 `'all'`、选「未分组」→ 原样写出 `group: 'none'`、每个筛选只清自己
  的 key、读侧 `?group=all`/`?enabled=all` 归一后不筛空也不多出清除按钮、合法值
  （含数字 group id）保留、`enabled=false` 只留停用那枚、数组 query 塌成单值。源码侧只留一条**格式无关** tripwire
  `not.toMatch(/value:\s*(?:''|""|``)/)`，覆盖单/双引号与模板字面量三种写法。

## Decisions

- **哨兵只活在组件里**：`ALL_FILTER` 不写进 URL query，也不进 `filterBadges`。
  query 契约保持「空值 = 不筛选」，`applyQuery` 继续 delete key，URL 里不会出现
  `group=all`。`group=none`（未分组）仍是真实筛选值。
- **`null` 分组不改成 sentinel**：`SFAdminBadgeEditDialog.vue` 的分组下拉用
  `value: null` 表示「—（无分组）」，Reka 只拦空串，`null` 走 `isEqual` 比较；
  浏览器实测「新建徽章 → 分组选『—』→ 保存」落成未分组，回写正常。改 sentinel
  反而是无谓重构。
- `SFAdminBadgesGrantsTab.vue` 的 `:model-value="... ? undefined : ..."` 是
  placeholder 语义（未选择），合法，不动。

## 门禁（我实跑）

- `cd apps/web && bun run typecheck`（`NUXT_BUILD_DIR=.nuxt-typecheck`）→ exit 0，
  vue-tsc 无输出。这是本项目首次跑该命令，**未暴露存量类型错**。
- `cd apps/web && bun test` → **2317 pass / 0 fail**，242 files（基线 2304；
  `adminBadgesTabs.test.ts` 单文件 28 pass / 0 fail）。
- **变异验证（证明测试真的会红，且归属清楚）**：
  - 变异 A：四处 item 改回 `value: ''` → **2 红**：源码 tripwire + items 行为断言。
  - 变异 B：`onFilterSelect` 改成直接写 `next`（哨兵泄漏进 query）→ **2 红**。
  - 变异 C：撤掉读侧归一（`group`/`tier` 直通）→ **1 红**：读侧归一那条。
  - 变异 D（复审后补）：`onFilterSelect` 顺手把所有筛选键写成 `''` → **4 红**，
    其中「只动自己那个键」「『全部』只清自己那个键」两条是改写后新增的。
    **同一次变异下旧版那条是绿的**（复审指出的空转，我复现了绿/红对照）：
    旧用例挂载时 query 是 `{}`，`router.replace` spy 不回写，组件 `filters` 永远读到
    挂载时的空对象，所以「只动该键」「清空后等于 `{}`」两个断言恒真。
    修法选了「挂载带初值」这一条（不是两条都上）：`SEEDED_QUERY` 给满四个键，再 emit
    单键，断言 `query[key]` 变了且 `toMatchObject(others)` 其余三键存活——这才需要
    组件真的**只**动一个键。
  - 复原证据：本轮起改用 `sha1sum -c`（`tabs/` 与两个测试文件是 untracked，
    `git diff --stat` 为空不能当复原证据）。`SFAdminBadgesBadgesTab.vue: OK`。
- `node tests/validate-architecture-boundaries.mjs` → passed，2111 production files，
  213 files above 500-line review threshold。

## 浏览器取证（1633×810 / 390×844）

前置：`curl` 预热 `/control-panel/badges` 到可编译（未登录 302 即说明 SSR 已通），
控制台用 CDP 附加监听（`Runtime.consoleAPICalled` / `Runtime.exceptionThrown` /
`Log.entryAdded`）。

- **四个筛选下拉**：全部正常展开；`全部` 项回落后 URL query 键被删除（不是写
  `all`）；全程 **0 条 Vue warn / 0 exception**（含 15 次页面加载）。
- 分组下拉选项：全部分组 / 未分组 / 活跃 / 内容 / 荣誉；选「未分组」→ `?group=none`。
- 排序三键六态：`?sort=name|tier|grantedCount` + `dir=asc|desc`，表头 aria-label
  同步翻转（「已按…降序排列，点击切换为升序」）。
- 深链：`?tab=groups|grants|badges` 还原对应分区；
  `?tier=gold&enabled=true&sort=name&dir=desc` 还原为 金牌/启用/名称降序（12→3 行）。
- 分组 CRUD：新建 → 3→4 行；删除第一次点击不删（仍 4 行），第二次才 4→3。
- 授予 → 记录可见 → 收回：手动授予 `优质解答`（`grantedCount` 0→1）→ 授予记录
  出现该行（来源 `手动`）→ 收回第一次点击按钮 title 变「再次点击确认收回」且行仍在
  → 第二次点击 1→0 行。测试数据已收回，dev 数据净零。
- 重算：两步确认（`重算授予` → `确认重算` → 执行），toast「已开始重算徽章授予。」
- **窄屏 toolbar（390×844）**：两个按钮 40×32 纯图标，`span.hidden.sm:inline`
  computed `display: none` / 宽 0，无竖排文字；`aria-label` / `title` 均为「刷新」
  「重算授予」，`elementFromPoint` 命中按钮本体，真实点击刷新后抓到
  `GET /api/v1/admin/badges` + `/admin/badges/groups`；toolbar 高度点击前后都是 66px
  不跳。**1633×810** 同一会话复测：文字 label 恢复显示（刷新 74×32、重算授予 102×32），
  toolbar 54px，`aria-label`/`title` 仍在。
- 溢出：1633 与 390 的 `documentElement.scrollWidth - clientWidth` 均为 0；
  390 下四个筛选折成两行（16–176 / 184–328，16–160 / 168–296），下拉浮层
  16→168 落在视口内，5 项全可见。

**取证方法坑（下次照做）**：Reka 的选项监听 `pointerup`，`element.click()` 选不中
——必须 `Input.dispatchMouseEvent` 真实鼠标事件；下拉项若在 listbox 可视区外
（12 个徽章里的最后两项）`elementFromPoint` 会落到 `HTML`，改用键盘
`End` + `Enter`；UButton 的 `@click` 反而可以被 `.click()` 触发。`Emulation.
setDeviceMetricsOverride` 是**按 CDP 会话**生效，断开即失效，量尺寸和截图必须在同一
会话里做完。

## KeepAlive 实例保留（已实测，两个场景都保留）

`:key="activeTab"` + `<KeepAlive>` 下**每个 tab 是不同 key 的实例，被各自缓存**，切回命中
缓存而不是重建。实测（390 与 1633 行为一致）：

| 场景 | 操作 | 结果 |
| --- | --- | --- |
| groups 表单草稿 | 新建分组弹窗输入「草稿测试」→ 切 badges → 切回 groups | 弹窗仍在、输入值仍是「草稿测试」——**保留** |
| grants 选中徽章 | 键盘选中第 4 枚「初次发帖」→ 切 badges → 切回 grants | 仍为「初次发帖」、12 行记录**未重新请求**（CDP Network 抓 `/grants` 为空）——**保留** |

- **页码保留无法实测**：`PER_PAGE = 20`，真实数据单徽章最多 12 条授予记录，分页器不渲染
  （`tbody` 之外没有任何「上一页/下一页」按钮）。页码是同一实例里的 `offset` ref，
  与选中徽章同源，按同一机制保留；等有 >20 条授予的徽章时再补一次实测。
- 附带观察（非缺陷）：弹窗是 teleport 到 body 的，`open` 为真时切 tab 会在 DOM 里留下
  可见弹窗。真实用户点不到 tab（遮罩挡住指针），且 tab 切换走 `router.replace` 不是
  push，浏览器后退也回不到上一个 tab，所以目前没有可达路径；仅当 URL 被外部改写时才可能
  出现残留。要不要在 tab 切换时强制关弹窗是新需求，本轮不改。

## 同日追加：内容区由一张 `UCard` 承载

三个分区的正文原来直接铺在页面灰底上（tab nav 下面是筛选栏 + 裸表格），是全仓 Core 自有
后台页里唯一没有承载容器的。补法与 `pages/admin/permissions.vue` 同款：

- `pages/admin/badges.vue`：`<UCard class="border-slate-200 dark:border-zinc-800 bg-white
  dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">` 包住 `<KeepAlive>`；**卡在
  `KeepAlive` 外**，切 tab 只换卡内组件、卡片本身不重建（DOM 探针计数连续，实测未重建）。
  nav 留在卡外，与 `forum/trust-levels.vue` 同构。
- **不写 `#header`**：分区名已由 `SFAdminFixedTabNav` 承担，加卡头会多出第三个标题层级；
  permissions 同样无卡头。**不写 `:ui` 覆盖**，用默认 body padding（实测 24px / 窄屏 16px）。
- `SFAdminBadgeTable.vue` 的 `max-h` 由 `calc(100vh-20rem)` 调到 `calc(100vh-24rem)`
  （只改数值，`sticky` 未动）。原因按几何算：卡 top=351（1920×1080），卡内 24+24 padding
  + 筛选行约 56 + 表 760（20rem）= 864 > 视口剩余 729 —— 行数顶到上限就必然溢出；
  24rem（696）刚好把卡新增的约 50px 让回来。**未先复现双滚动条**（dev 库当时 0 徽章，
  没有行数能顶到上限），裁决：算式为准。
- **已知残留（接受，不再收紧）**：长表时页面仍有轻微外层滚动。不往 28rem 走——
  1440×900 笔记本会把表格压到 7 行，得不偿失。
- **nav 与卡之间 32px 间隙是既有几何**（父容器 `gap-4` 16 + `SFAdminFixedTabNav` 根节点
  自带 `mb-4` 16），加卡前就在，属几何契约层，本次不动。

实测（CDP `Emulation.setDeviceMetricsOverride`，独立标签页，真实 12 徽章数据）：1920×1080
与 390×844 下卡与 nav / toolbar 的左右边界偏差 **0px**，`scrollWidth-clientWidth = 0`
（无横向溢出），三 tab 内容都在卡内，表头 `sticky` 生效。测试侧只在
`tests/admin/adminBadgesTabs.test.ts` 的 `badges shell` describe 加了一条
`expect(shellSource).toContain('<UCard')` 结构锚点，原有断言未改。

## Next

- 浏览器侧独立复现本轮修复仍缺一环：验收用的是 YG 手动起的 dev server 上已有的
  admin 登录态（`dsh` 浏览器 profile 持久化 cookie），本会话拿不到可编程登录态，
  所以浏览器取证无法在干净 profile 上从零重放。要做成可重复的 E2E，需要一套
  fixture 账号/种子密码的注入方式（或 `storageState` 导出），属新工作。
- 页码保留待有 >20 条授予记录的徽章时补测（当前单徽章最多 12 条，分页器不渲染）。

## Open Questions

- 是否需要给 `USelect` 的 items 加一层共享类型/校验（把「禁止空串 value」变成
  编译期约束），还是继续靠单点源码断言兜住。当前全仓只有这一处踩过，先不加抽象。
