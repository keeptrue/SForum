# 徽章 M2 · 公开面 + 后台徽章页视觉 QA（390×844 / 1440×900）

- 日期：2026-11-12
- 执行：reviewer（小审），真实 Chrome headless + CDP `Emulation.setDeviceMetricsOverride`（复用 `tests/browser-public-390x844.mjs` / `tests/browser-admin-390x844.mjs` 的手法与判据，**未引入 Playwright**）
- 环境：web `http://127.0.0.1:3000`、api `http://127.0.0.1:8080`；管理端以专用验收账号（本机 `super_admin`）会话注入，cookie 值仅在进程环境变量中传递，未写入任何文件、日志或本文
- 证据目录：`knowledge/reports/qa-2026-11-12-badge-m2/`（14 张截图 + `report.json`）
- 红线遵守：未改生产代码、未 commit、未碰 locale/测试；**发现的问题全部只报不改**

## 结论表（数字为实测）

| 路径 | 视口 | innerWidth | scrollWidth | 卡片 | 有文案卡 | 越界卡 | 越界控件 | console error |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `/badges` | 390×844 | 390 | **390** | 12 | 10 | 0 | 0 | 0 |
| `/badges` | 1440×900 | 1440 | **1440** | 12 | 10 | 0 | 0 | 0 |
| `/u/zhuchunshu?tab=badges` | 390×844 | 390 | **390** | 12 | 10 | 0 | 0 | 0 |
| `/u/zhuchunshu?tab=badges` | 1440×900 | 1440 | **1440** | 12 | 10 | 0 | 0 | 0 |
| `/control-panel/badges?tab=badges` | 390×844 | 390 | **390** | – | – | – | **48** | 0 |
| `/control-panel/badges?tab=groups` | 390×844 | 390 | **390** | – | – | – | **6** | 0 |
| `/control-panel/badges?tab=tiers` | 390×844 | 390 | **390** | – | – | – | 0 | 0 |
| `/control-panel/badges?tab=grants` | 390×844 | 390 | **390** | – | – | – | 0 | 0 |
| `/control-panel/badges`（默认 tab） | 1440×900 | 1440 | **1440** | – | – | – | 0 | 0 |

路由确认（不猜）：资料页徽章 tab 走 `?tab=badges`（`apps/web/app/composables/profile/useProfileTabQuery.ts:5` 明写 `/u/{username}?tab=badges`，宿主 `SFProfileShowPage.vue:416`）；后台前缀 `DEFAULT_ADMIN_ROUTE_PREFIX = '/control-panel'`（`apps/web/app/utils/admin/adminRoutePrefix.ts:1`）+ `pages/admin/badges.vue:25-28`（middleware `admin`），四个 tab 由 `pages/admin/badges.vue:52,63` 定义。

截图（`knowledge/reports/qa-2026-11-12-badge-m2/`）：`public-390x844-badges.png`、`public-1440x900-badges.png`、`public-390x844-u-zhuchunshu-tab-badges.png`、`public-1440x900-u-zhuchunshu-tab-badges.png`、`admin-390x844-{badges,groups,tiers,grants}.png`、`admin-1440x900-default.png`，另有 5 张 `*-frozen.png`（见「测量纪律」）。`report.json` 为机器可读明细。

## 一、公开面

### 1.1 条件文案换行（`[data-badge-slug]` 内的 `.sf-profile-badge-card__description`）

20 个测量点（2 路径 × 2 宽度 × 10 张有文案卡）全部一致：

```
boxHeight=14.8  scrollHeight=15  linesVisible=1.00  contentLines=1.01
clamp=2  white-space=normal  overflow-wrap=anywhere  text-overflow=clip
```

- `-webkit-line-clamp` 实际取到 `2`，`white-space` 是 `normal`（不是 `nowrap`），`overflow-wrap: anywhere`。
- 当前 dev 数据里的文案都是单行（`访问天数 ≥ 7` / `topics published ≥ 10`），`contentLines = 1.01` → **单行文案没有被强行撑成两行**，符合「单行文案不强求两行」。
- **两行裁切的实测证据本轮拿不到**：需要 ≥2 行的文案，dev 库里 10 枚单条件徽章的最长文案是 `days visited ≥ 180`（18 字符），390 宽下仍是一行。裁切行为本轮只有静态依据（`SFBadgeCard.vue:224-236` 的 clamp 声明 + `badgeRequirementText.test.ts:118-140`），**记 pend，不当通过**。
- `text-overflow: clip`、块内无 `nowrap`/`ellipsis` → 单行截断的旧痕迹没有回来。

### 1.2 无横向溢出

- `document.documentElement.scrollWidth` 依次为 **390 / 390 / 1440 / 1440**，与 `innerWidth` 相等。
- 每张卡的 `getBoundingClientRect().right` ≤ 378（390 视口下），**无卡片越过视口**（`cardsOverflowing = []`）。
- 越界可交互控件 0 个（判据与仓库脚本一致：`right > innerWidth + 1 || left < -1`）。
- 按路径滚到底后再测（脚本先 `scrollTo(0, scrollHeight)` 触发惰性挂载，再量），不是只看首屏。

### 1.3 桌面 1440

- `/badges`：`grid-template-columns = 264px ×4` → 1440 档 4 列，符合 `SFProfileBadgeTab.vue:241-245` 的 `@media (min-width:1280px)` 规则。
- 资料页：`193.5px ×4`（扣除公共 sidebar 后的内容区），同样 4 列。
- 卡片高度 82–105px（含单行文案），没有因 clamp 出现异常撑高。

## 二、后台徽章页

### 2.1 横向溢出 —— **没有溢出，但表格被裁切**

- 四个 tab 在 390 下 `scrollWidth` 全部等于 **390**，`documentElement` 无横向滚动。
- 但量与「不裁切」是两件事。实测越界元素（`right > 390`）：

| tab | 越界容器 | 宽度 | 右边界 | 超出 px | 越界可交互控件 |
| --- | --- | --- | --- | --- | --- |
| badges | `table.min-w-full.overflow-clip` | 508.5 | 540.5 | **150.5** | 48 个 `button`（left 376.5 → right 524.5） |
| groups | `table.min-w-full.overflow-clip` | 405.6 | 437.6 | **47.6** | 6 个 `button`（left 369.6 → right 421.6） |
| tiers | — | — | — | 0 | 0 |
| grants | — | — | — | 0 | 0 |

定位证据（`report.json` → `admin.admin-390x844-badges.overNodes`）：`TABLE → THEAD → TR → TH('操作') / TBODY → TR → TD` 整链右边界 540.5，即**表格自身溢出而外层没有滚动容器**。
这说明溢出被某个祖先的 `overflow-x: hidden` 吃掉了，而不是布局真的放得下。

### 2.2 测量纪律：冻结页 vs 用户可见页（两套测量分开做）

用户可见状态由**未做任何 DOM/CSS 改写的页面**测量：`scrollWidth = 390`，无横向滚动条，`overflow-x` 由祖先裁掉。
为回答「表格到底有没有滚动容器」，另做一次**冻结测量**（把 `documentElement` / `body` 的 `overflow-x` 强制成 `hidden` 后复测）：`scrollWidth` 仍是 390 ——
即裁切**不来自 `html/body` 上的 overflow**，来自更内层的容器。`layouts/admin.vue` 的滚动容器是 `overflow-y-auto`（`:722`），
另两处 `overflow-x-hidden` 在侧栏（`:531,:580`），都对不上表格；这需要另一次针对性定位（见 §4 问题 2 的取证建议）。
另：`thead` 是 `sticky top-0`，`table` 上是 `overflow-clip` —— 裁切后 `操作` 列的按钮在 390 视口内根本点不到。

### 2.3 工具栏 / tab 栏

- 后台顶部 tab 实测 6 项：`仪表盘 / 徽章 / 徽章 / 分组 / 等级 / 授予记录`，自身在 390 内未越界（越界的 48/6 个控件全部位于表格内，见 2.1）。
- 四个 tab 各打开一次（`?tab=badges|groups|tiers|grants`），切换后仍无整页横向溢出。

### 2.4 桌面 1440

- `scrollWidth = 1440`、越界控件 0、console error 0，筛选行与工具条无退化迹象（`bodyTextLen=952`）。

### 2.5 grants tab 的 body 偏短 —— 已排除是空页

`?tab=grants` 的 `bodyTextLen` 只有 142（badges 519 / groups 239 / tiers 245）。查了截图与 DOM 归因：
- 该 tab 的内容区**确实只渲染筛选/空态**：数据源是 `POST /admin/badges/:id/grants`（按徽章查，`Badges/controller.go:116`），没有选中徽章时不给行；
- 台账里记录的**不是白屏**：tab 栏 6 项齐全、`clippedControls=0`、`consoleErrors=0`，且 `SFAdminBadgesGrantsTab.vue` 是懒加载组件（`pages/admin/badges.vue:70`）。
- 结论：**页面正常，属于「未选徽章就没有内容」的设计**；本轮把它从 FAIL 降级为「待你确认的产品取舍」，不作为缺陷。

## 三、测量纪律（本报告的自我约束）

1. **SKIP 不冒充通过**：脚本沿用仓库纪律——没有任何断言跑过就打印 `skipped: no assertion ran`；pend 单独列出并注明缺什么。
2. **冻结页不作用户可见证据**：`*-frozen.png` 只用于判断「表格有没有滚动容器」，正文结论一律取自未改写的页面。
3. **作废数据明确标注**：本次运行中 `admin-390x844-badges-frozen` 记录到 `innerWidth=1440 / bodyTextLen=847` 的错位（视口 override 与导航竞态，首帧未落定），该条已作废，以 `admin-390x844-badges`（390/390）为准。
4. **量不到就 pend**：两行 clamp 缺多行样本、内层裁切容器缺精确归属，两条均标 pend。

## 四、我判断「必须改」的清单（附证据，未改代码）

1. **（必须）后台徽章/分组表格在 390 宽把「操作」列裁出视口**。`td/th` 的 `w-[96px]`（`SFAdminBadgeTable.vue:88-91` 的 actions 列）加上 `name` 列 `min-w-[180px]`（`:56-60`）在 390 里放不下，表格实际宽 508.5，超出 **150.5px**；`overflow-clip` 把外部收住，代价是操作按钮不可达（48 个按钮的 right 最大到 524.5）。分组表同病（405.6，超出 47.6px，6 个按钮不可达）。
   取证建议（下一步，不改代码）：打印 `table` 的每条祖先链 `overflow-x` 计算值，找出实际裁切点；再由你决定「包一层横向滚动容器」还是「≤640 改卡片式列表」——这是布局决策，不在我这刀。
2. **（必须确认）clamp 的实测覆盖为零**。当前 dev 数据全部是单行文案，`-webkit-line-clamp: 2` 这条契约至今只被静态断言与单测证明过。要真证据，需要一枚文案 ≥40 字符的徽章（可由运营手填，前端尚无入口——见上一份报告的 D-1）。
3. **（建议）给 `?tab=grants` 一个明确空态**。现在 142 字符的正文里没有「请先选择一枚徽章」的引导，用户会以为页面坏了；这不是缺陷，但补一句文案能把「空白」和「失败」分开。

## 五、pend（未取得证据，不当通过）

1. 两行 clamp 的实际裁切：缺多行文案样本（见 §4-2）。
2. 表格裁切点的精确归属容器：`html/body` 已排除，内层待逐层量 `overflow-x`。
3. 后台 390 的 `?tab=grants` 有数据态（需要先授予记录存在）：本轮只有空态。
4. Playwright/无头外的真机（iOS Safari）未测。

## 附：复现

```bash
# 公开面 + 后台页（会话仅经环境变量注入，不落盘）
SFORUM_QA_DIR=$PWD/knowledge/reports/qa-2026-11-12-badge-m2 \
SFORUM_EVIDENCE_OUTPUT=/tmp/qa-final.json \
SFORUM_SESSION_COOKIE="$(python3 -c "import json;print(json.load(open('/tmp/qa-cred.json'))['session'])")" \
node /tmp/qa-badge-m2.mjs
```
