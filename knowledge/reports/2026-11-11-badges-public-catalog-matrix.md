# `/badges` 公共目录页 — 验收矩阵（B 组）

Owner: pm（阿策）
Status: **草案，待实施**（2026-11-11；口径见 `../plans/2026-11-11-badges-public-catalog.md` §3）
基线：`HEAD = 9e3e1cbe8`（采集 2026-11-11T12:43:51+08:00，复算 `git rev-parse HEAD`；`git status --porcelain` 空）
前序编号源：`2026-11-11-trust-levels-acceptance-matrix-v3.md`（A–M 150 条）、
`2026-11-11-trust-levels-baseline-narrowing-matrix.md`（N 组 16 条 + M-02）。

## 0. 本文件与既有矩阵的关系

| 时点 | 改动（只追加，不重写历史行） |
| --- | --- |
| 13:2x | **B-23 按实测仪器重写**（原文 `resolve?path=/badges` + 只 grep `data-provider`/`data-template` 会被 404 页骗过）：新增硬断言 HTTP 200 / `data-page=forum.badge.index` / `sf-page-outlet` 唯一，新增 **§4-1** 七条（含 before 基线、digest 可复算命令、两条独有红态）。时点为会话内近似值，提交后以 `git log -1 --format=%cI -- knowledge/reports/2026-11-11-badges-public-catalog-matrix.md` 为准。 |
| 13:4x | **第五轮（只改行号与证据点，不重写结构）**：`registry_test.go` `:506/:507` → **`:508/:509`**；`source.go` 的等级闸 → **`:702-708`**（定义 `:697`，函数名实测仍为 `requirePageFeatures`）；`Badges/controller.go` 的 `setPublicCacheable` → **`:713`**、新增的 `setViewerScopedNoStore` 在 **`:723`**。B-29 改指具名测试 `TestPublicBadgeCatalogIsPrivateCacheable`；B-22 加「不得用 `\| tail` 决定退出码」；B-13-red 的 echo 冒充断言要换成真实输出；新增 **§0-4「证据自身的假绿」**。 |
| 14:1x | **第六轮（M10 收口，按 reviewer 复验改三处判据级描述）**：**B-30** 从「静默丢页」改为「**静默丢字段**」（拆行后该行仍在，`contractVersion:""` / `coreComponent:""` / `replaceable:false`，`--check` 仍 exit 0；下游 `validate-public-page-surface-catalog.mjs:39` 的 `page row is incomplete` 能兜住）；**B-23** 的 `data-page` 断言改成「承载 `data-provider` 的元素其 `data-page` = `forum.badge.index`；带该**属性**的元素恰好 2 个、取值全部为该页 ID」（实测原始 HTML 6 处裸子串、2 个属性、payload 里两处 `system.server_error`，旧写法会假红）；**B-21** 写清 digest 复算口径（模板**文件** digest 四处相等 vs **包级** digest 只能与账本行/资源目录比对，`sha256sum` 复算不出包级）。状态：B-23 已 PASS，唯一未闭是 B-26。 |

| 事项 | 处理 |
| --- | --- |
| 编号 | **B-01 起新增**，不与 A–M / N 组重排，也不相加。要表达跨刀残余，写「见 N-xx / M-02」，不写总数 |
| 规则 | 沿用 v3 §1.3 的 R6 / R7 / R10 / R11 / R13 / R14 / R15 / R16，本文件不新造 |
| 前置与命令 | 沿用 v3 §1.1（`API` / `EV` / `Q()` / `login()` / `call()`，jq 一律 `(.data // .)`）与 §1.2 账号矩阵 |
| 证据目录 | `storage/qa/badges-public-catalog/B/`（**约定命名**：`ls` 该目录当前报 not found，首条证据落盘时创建） |
| 计数来源 | `catalog.go` **36** 条 / `public-page-surfaces.json` **36** 条 / `routes.json` **409** 条 / `theme.json` **34** 页 / `templates/` **35** 文件（含 shell）。**与 Lead 转述的 35 / 35 不同，见计划 §0 自纠 1、2** |
| **稳定 page ID** | **`forum.badge.index`（单数）**。命名沿用 `forum.category.index` / `forum.tag.index` 的 `<域>.<实体>.<视图>` 惯例；研究员建议过的 `forum.badges.index`（复数）已被 Lead 驳。此 ID 同时出现在 catalog / ViewModel registry / `theme.json.target` / island 映射四处，**落盘后不得改** |
| 路由与路径 | 端点 `GET /api/v1/badges`，route ID `core.route.badges.public_catalog`（§8-3 已裁）；页面 `/badges`，`CoreComponent` = **`pages/badges/index`**，route shell = `apps/web/app/pages/badges/index.vue` |

> **行号是 `HEAD` 的瞬时值，不是常量。** 本刀内 backend 改 `Badges/controller.go` 就把
> `setPublicCacheable` 从 `:640` 推到了 `:713`。凡引用行号必须带时点与 `HEAD`，复算方式是 `grep -n`。
> 采集时点 2026-11-11T13:4x，`HEAD` 以 `git rev-parse HEAD` 为准（本文件不抄 sha，只抄复算方式）。

### 0-1 每条必须有红态，且红在点名那句上（R10）

本文件每行的「红态形态」列写三样：**变异怎么做 / 红在哪个文件哪一行 / 红的那句原文**。
已有断言的行的号是实测（采集 2026-11-11T12:5x，`HEAD=9e3e1cbe8`）；**新拟断言**的行号在落盘后
按 R15 回填，本文件先写**拟名 + 失败文案**——不许只写「应为 X」。

### 0-2 假绿**四条**（本刀高发，验收时逐条问）

1. `Populate` 的 `case` 返回空 struct：`TestCorePageViewModelSourcePopulatesEveryCatalogContract`
   只断言「不报错」，**会绿**。→ B-17 用「数据非空」钉住。
2. 主题模板存在但 `theme.json` 没声明：模板不参与编译，`data-template` 仍是 `0`。
   → B-20 断言 `theme.json` 的 `pages[]` 条目，B-21 断言渲染值。
3. i18n 只查「缺键」不查「多余键」：死文案留在 locale 里。→ B-24 双向求差。
4. **「只加一种语言会红」本身是假绿**：`Message()` 在目标语言缺键时回退 `DefaultLocale`
   （`messages.go:846-853`），`Message("en-US", code)` 拿到中文、不等于 key，
   `TestAPIErrorCodesHaveLocalizedMessages`（`locales_test.go:126`）**照样绿**。
   → B-12 改用不经回退的 `TestAPIErrorCodesAreLocalizedInEveryLocale`（`locales_test.go:159`）。

### 0-3 工具陷阱（本会话 Lead 与 researcher 各踩一次，不是环境 bug）

`rg -rn "x"` 里的 `-r` 是 **`--replace`**，不是 `-R`。写 `rg -rn "generate" scripts/` 会把匹配串
替换成 `n`，输出里 `generate.mjs` 显示成 `n.mjs`，看起来像「文件不存在」。

- 正确：`rg -n "pattern" path`（递归是 `rg` 默认行为，不需要 `-r`）。
- 自查：`rg --help | grep -A1 '\-r,'` 会看到 `-r, --replace <REPLACEMENT>`。
- 判据：**看到 `n.mjs` / `n.go` 这类「文件名只剩一个字母」的输出，先怀疑 `-r`**，不要去查仓库。

### 0-4 证据自身的假绿（reviewer 提，2026-11-11T13:4x 采纳）

前四类是「被测行为假绿」，这一类是「**证据文件假绿**」——它比前者更贵：断言本身没错，错在用来证明它的那段输出。

| # | 形态 | 怎么识别 | 本刀哪条命中 |
| --- | --- | --- | --- |
| 1 | **管道吞退出码**：`cmd \| tail -3` 的 `EXIT=0` 不代表 `cmd` 成功（`tail` 的退出码才是 0） | 证据里出现 `\|` 且后面跟着 `tail`/`head`/`grep` 时，命令必须写成 `set -o pipefail` 或先落文件再取末几行；判据：**把 `cmd` 故意改坏，`EXIT` 必须变非 0** | **B-22**（`validate-v3-p0-catalogs.mjs \| tail` 曾把失败写成 `EXIT=0`） |
| 2 | **手写 echo 冒充断言**：红态证据里那句 `want X, got Y` 是人手敲的，不是测试输出 | 证据文件里 `want`/`got` 没有对应的 `--- FAIL` / `t.Fatalf` 行号；判据：**同一句话必须能在 `go test` 输出里 grep 到** | **B-13-red**（`want migration count unchanged, got +1` 是 echo） |
| 3 | **`(cached)` 绿**：`go test` 未带 `-count=1` 时的 `ok ... (cached)` 证明的是**上一次**编译，不是这一版 | 输出里有 `(cached)`；判据：所有进证据的 `go test` 一律加 `-count=1` | 本刀 **10 个绿文件**命中 |

> 这三类的共同点：**绿/红都不是被测对象产生的**。验收时对每条证据问一句「这段输出是哪条命令、哪个版本跑出来的」；
> 答不上来的，按未验处理。

## 1. 编号分配

| 段 | 归属 | 内容 |
| --- | --- | --- |
| **B-01..B-06** | backend | 公开读面语义（可见性 / 字段白名单 / 排序 / 上限 / 分组 / 权限口径） |
| **B-07..B-13** | backend | 横切面：路由计数 tripwire、生成物、开关关闭态、空数据、失败路径、错误码本地化、零迁移 |
| **B-14..B-23** | backend（B-20 归 frontend） | Page Registry 七件套 + V3 目录 + 渲染证据 |
| **B-24..B-27** | frontend / ops | i18n 遍历型断言、空态与失败态 UI、视觉 QA、入口可达 |
| **B-28** | frontend | 后台 IA：`/badges` 从 forum 文件夹移到「用户与权限」 |
| **B-29..B-30** | backend | 第二轮新增：缓存头 `private`（D37）、catalog 条目必须单行（生成器逐行正则的假绿） |

## 2. 后端读面语义（B-01..B-06）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-01** | 存在**公开**目录端点 `GET /api/v1/badges`，`Guard.Kind == public`、`Permissions` 为空；它**不是** `permission_any`，也**不**要求 `badge.view` | backend 落 / reviewer 实测 | `Q GET /api/v1/badges` 用**未登录**会话得 200；`rg -n "core.route.badges.public_catalog" apps/api/app/Support/Routes/core_catalog_gen.go` 看 `Kind: CoreGuardKind("public")`；`Q` 用只持 `badge.view` 的账号也得 200（证明没挂后台键） | 把 Guard 写成 `permission_any` + `["badge.view"]` → 未登录得 401，红在 `registry_test.go` 的 guard 校验族（`route %s has no reviewed guard` / 权限矩阵断言）**以及** HTTP 侧 `want 200, got 401`。**不许只红在计数上** | `B/B-01.txt`（未登录 200 + 匿名响应体首 20 行）+ `B/B-01-red.txt` |
| **B-02** | 响应含**全部 `enabled` 徽章**，含未获得的；未获得的 `earned=false`，已获得的 `earned=true`。造数：3 个 enabled（其中 1 个授予给测试账号）+ 1 个 `enabled=false` | backend | 新测试拟名 `TestPublicBadgeCatalogIncludesUngranted`：断言 `len(items)==3`、`items` 里恰 1 条 `earned=true`、`disabled` 那条 `slug` 不出现 | 变异「只返回已获得」（把查询按 `user_badges` 内连接）→ 红在点名句（`want 3 badges (1 earned, 2 unearned), got 1`）。**注意**：这条绿了但 B-05 红，说明只是排序问题；两条同时红才是语义错 | `B/B-02.txt` + `B/B-02-red.txt` |
| **B-03** | **wire 上不存在** `threshold` / `metric` 键（不是 `null`，是**键不存在**）；允许出现的键恰好是 `slug`/`name`/`description`/`icon`/`tier`/`group`/`earned`（+ 顶层 `truncated`） | backend | 断言用 `json.RawMessage` 解析后 `_, ok := obj["threshold"]; if ok { fail }`，并比对键集合**精确相等**（不是「包含」）；HTTP 侧 `Q GET /api/v1/badges \| jq '[..\|objects\|keys[]]\|unique'` | ① 变异「直接复用 admin DTO」→ 红在键集合断言（`unexpected key "threshold"`）；② 变异「用 `omitempty` 但字段存在」→ JSON 里没键、**测试仍绿**，所以必须**同时**断言 Go struct 上不存在该字段（`reflect` 遍历字段名）；②是这条的真正难点，只测 wire 会被 `omitempty` 骗过 | `B/B-03.txt`（键集合）+ `B/B-03-red.txt`（两条变异各一段） |
| **B-04** | 排序稳定：按 `badge_groups.position ASC, badges.position ASC, badges.id ASC`。造数：**两个分组 position 相同**、**组内两条徽章 position 相同** | backend | 新测试拟名 `TestPublicBadgeCatalogOrderIsStable`：连查 3 次断言顺序逐字节相同；再交换两条同 position 行的插入顺序（不同 `id`），断言仍按 `id` 升序 | 变异「去掉 `badges.id` tiebreaker」→ 同 position 两行的顺序变成 DB 返回序，3 次查询中出现不一致 → 红在点名句（`order drifted between calls: [a,b] vs [b,a]`）。**只跑一次查询的测试抓不到这条** | `B/B-04.txt`（3 次查询输出）+ `B/B-04-red.txt` |
| **B-05** | 硬上限 **500**：`enabled` 徽章 501 条时返回 500 条 + 顶层 `truncated: true`；≤500 时 `truncated` 为 `false`（不是缺省/`null`） | backend | 新测试拟名 `TestPublicBadgeCatalogTruncatesAt500`：批量插入 501 条 enabled，断言 `len(items)==500 && truncated==true`；删到 500 条，断言 `truncated==false` | 变异「去掉上限」→ 红在点名句（`want 500 items, got 501`）；变异「上限写成 1000」→ 同上。**反向假绿**：上限写死 500 但忘了 `truncated` → 红在 `want truncated=true, got false` | `B/B-05.txt` + `B/B-05-red.txt` |
| **B-06** | 分组只输出**含至少一个 enabled 徽章**的组（空组省略）；组按 `position` 升序，组内徽章同 B-04 口径 | backend | 新测试拟名 `TestPublicBadgeCatalogOmitsEmptyGroups`：造 1 个有徽章的组 + 1 个全部 `enabled=false` 的组，断言响应 `groups` 只有 1 个且其 `key` 是前者 | 变异「返回全部组」→ 红在点名句（`want 1 group, got 2`）。**口径未裁**：本行按「省略空组」写，若 Lead 裁相反，改本行文本与断言，不改编号（计划 §8-2） | `B/B-06.txt` + `B/B-06-red.txt`；裁决记录写进计划 §8-2 回填 |

## 3. 横切面（B-07..B-13）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-07** | **路由计数 tripwire 409 → 410**：`registry_test.go:508` 的 `if len(catalog) != 410 {`（`!= 409` → `410` 已由 backend 落地），`// 410 = 409 + core.route.badges.public_catalog` 注释已由 backend 补在 **`:506-507`**（链上前一行 `// 409 = 405 + 四条信任等级路由` 在 `:505`） | backend | `go test ./app/Support/Routes -run TestGeneratedCoreRouteCatalog`（族名以文件内实际为准）；`grep -n "!= 410" apps/api/app/Support/Routes/registry_test.go`（命中 `:508`） | 只加路由不改断言 → 红在 `registry_test.go:509` 的 `t.Fatalf("generated core route count = %d", len(catalog))`，输出 `generated core route count = 410`。**这是「加了路由忘了计数」的唯一机械判据**；上一刀（徽章）就漏了它，症状是「`go test ./app/Support/Routes` 红、看起来与徽章毫无关系」（`modules/badges.md` residual 12） | `B/B-07.txt`（`go test` 通过输出 + grep 两行） |
| **B-08** | 生成物**只能重跑生成器**：`node scripts/v3-catalog/generate.mjs` 后 `docs/extensions/v3/catalogs/routes.json` 从 409 → 410；`generate.mjs --check` exit 0；`core_catalog_gen.go` 首行仍是 `// Code generated by ... DO NOT EDIT.` | backend | `node scripts/v3-catalog/generate.mjs --check`；`grep -c '"id"' docs/extensions/v3/catalogs/routes.json`（现 **409**，改后 **410**）；`git diff --stat` 只看生成物与注册点 | 手改 `core_catalog_gen.go` → `generate.mjs --check` 报 `V3 catalog drift check failed`（`tests/validate-v3-p0-catalogs.mjs:87` 的 `assert(generated.status === 0, ... 'V3 catalog drift check failed')`）；或 `validate-public-page-surface-catalog.mjs:88` 的 `generated catalogs are stale:` | `B/B-08.txt`（生成前后计数 + `--check` 输出）+ `B/B-08-red.txt`（手改后 `--check` 红） |
| **B-09** | **开关关闭态**：`features.badges=false` 时 ① `GET /api/v1/badges` 返回 **403 `forum.badges_disabled`**（与既有 13 条徽章端点同口径，`Http/Controllers/Badges/controller.go:108` 的 `requireFeature`）；② 目录页 `/badges` 返回 **404**（页面级 `RequiresFeatures:["features.badges"]` → `source.go:697` 的 `requirePageFeatures` 在 `:702` 起遍历 `RequiresFeatures`、`:708` 返回 `ErrCorePageDataNotFound` → `Pages/controller.go:247-248` 映射 404 `pages.data_not_found`）；③ 统计采集与统计数字**不受影响** | backend + ops | 关开关后 `Q GET /api/v1/badges` 与 `Q GET /badges`（HTML）各一次；开关恢复后再各一次；比对 `user_activity` 行数与资料页统计数字不变 | ① 若端点漏判开关 → 关态 200，红在 `want 403 forum.badges_disabled, got 200`；② 若页面漏声明 `RequiresFeatures` → 关态页面 200 且渲染空目录，红在 `want 404, got 200`。**②比①隐蔽**：页面照常打开、只是没数据，容易被当成「空态正常」 | `B/B-09.txt` + `B/B-09-flagoff.png`（关态页面）+ `B/B-09-red.txt` |
| **B-10** | **空数据**：`enabled` 徽章为 0 条 → 端点 200 + `{"groups": [], "truncated": false}`（**不是** 404、不是 `null`、不是 500）；页面显示空态文案（见 B-25） | backend + ops | 隔离库或 dev 上把全部徽章 `enabled=false`（**记录 before/after 与恢复命令**），`Q GET /api/v1/badges \| jq '(.data // .)'` | 变异「`nil` slice 直接序列化」→ 响应 `"groups": null`，红在点名句（`want groups=[] (empty array), got null`）。这条是 Go 里最常见的 wire 事故，且前端 `.map` 会直接炸 | `B/B-10.txt`（含 before/after SQL）+ `B/B-10-red.txt` |
| **B-11** | **失败路径**：读库失败 → **500** + 本地化错误文案，**不得** 200 + 空数组冒充成功；错误码与 `messages.go` 两份文案对应 | backend | 测试里注入失败 store（拟名 `TestPublicBadgeCatalogFailsClosedOnStoreError`），断言 `error != nil` 且 HTTP 层映射 500；`Q` 侧用断开 DB 或 store stub 观察 | 变异「`if err != nil { return empty, nil }`」→ 红在点名句（`want error on store failure, got nil`）。**这条是 fail-open 的判据**：公开面把失败当空态，运营会看到「站内没有徽章」而不知道库挂了 | `B/B-11.txt` + `B/B-11-red.txt` |
| **B-12** | **错误码本地化（判据换了：直接查目录，不走回退）**：本刀新增的每个错误码在 `apps/api/app/Support/Localization/messages.go` 的 zh（`:61-66` 是徽章六码）与 en（`:418-423`）**两份目录里都存在**；守卫是 `TestAPIErrorCodesAreLocalizedInEveryLocale`（`locales_test.go:159`，注释在 `:151-158` 写明「为什么不能只靠上一条」） | backend | `go test ./app/Support/Localization -run TestAPIErrorCodesAreLocalizedInEveryLocale`；`rg -n "forum\.badge" apps/api/app/Support/Localization/messages.go \| wc -l`（现 12 = 6 码 × 2 份） | 只加 zh 不加 en → 红在点名句（实测文案形如 `missing localized messages: en-US:forum.badges_disabled`）。**旧判据（跑 `TestAPIErrorCodesHaveLocalizedMessages`）抓不到这个**：`Message()` 缺键回退 `DefaultLocale`（`messages.go:846-853`），`en` 拿到中文、≠ key，旧测试照样绿——这是 §0-2 的第 4 条假绿 | `B/B-12.txt`（新守卫输出）+ `B/B-12-red.txt`（只留 zh 的变异输出） |
| **B-13** | **零迁移**：本刀**不新增迁移文件**。断言：`ls apps/api/database/migrations/*.sql \| wc -l` 与基线相等；`git diff --name-only <base>..HEAD -- apps/api/database/migrations` 为空；且「迁移可逆」这条**仍写成断言**（不是「不适用」） | backend | 两条命令各跑一次，数字贴进证据；`scripts/verify-migration-reversible.sh` **不跑**（无迁移可验），但要在证据里写「本刀零迁移，故该脚本不适用，判据是文件集不变」 | 若有人为了「目录查询性能」顺手加索引迁移 → 红在 `migrations` 文件集变化（`want migration count unchanged, got +1`）。**这条红必须来自真实命令输出**：当前 `B-13-red` 里那句是手写 `echo`，不是测试输出，按 §0-4 第 2 类判「证据自身的假绿」，要换成 `ls ... \| wc -l` 的实跑对照。基线：`ls apps/api/database/migrations/*.sql \| wc -l` = **162**（采集 2026-11-11T13:4x）**写成断言的理由**：零迁移是**本刀的设计选择**（读面只查既有表），不是巧合；不写成断言，下一刀加索引时没人会知道这里原本有约束 | `B/B-13.txt`（两条命令输出 + 基线数字） |

## 4. Page Registry 七件套（B-14..B-23）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-14** | **稳定 page ID**：`catalog.go` 新增条目 `{ID:"forum.badge.index", PathPattern:"/badges", Access:AccessPublic, ContractVersion:"sforum.page.badge_index@1", CoreComponent:"pages/badges/index", Replaceable:true, RequiresFeatures:["features.badges"]}`，且**写成整行**（生成器逐行正则，见 B-30）；`/badges` 今天**未被占用**（唯一 `/badges` 是后台 `apps/web/app/pages/admin/badges.vue`，不进公共目录） | backend | `grep -n 'PathPattern: "/badges"' apps/api/app/Support/Pages/catalog.go`；`go test ./app/Support/Pages -run TestValidateCatalog`；路径冲突由 `TestValidateCatalog` 的 `ValidateCatalog()`（`catalog.go:207`）覆盖 | ① 漏 `Replaceable:true` → 主题无法替换，`TestBuiltinThemesCoverAllThemeablePages` 走不到该页；② 漏 `RequiresFeatures` → B-09② 红；③ ID 与 `ContractVersion` 不配对 → `ValidateCatalog` 红（契约 schema 校验）。**ID 一旦落盘不得改**：它同时出现在 catalog / registry / `theme.json.target` / island 映射四处。**`CoreComponent` 写成 `pages/badges`（缺 `/index`）** → 红在 `catalog_coverage_test.go:82` 的 `catalog entry %s references missing page file %s.vue` | `B/B-14.txt` |
| **B-15** | **route shell**：`apps/web/app/pages/badges/index.vue` 存在（`badges/` 目录本次新建），模板里有 `<SFPageOutlet page="forum.badge.index">`（页面 id 必须与目录一致） | frontend | `ls apps/web/app/pages/badges/index.vue`；`go test ./app/Support/Pages -run TestCatalogCoversAllPublicNuxtPages` | ① 只加页面文件不加目录条目 → 红在 `catalog_coverage_test.go:51` 的 `page file %s has no catalog entry (CoreComponent %q); register it in Support/Pages/catalog.go or add an exemption here`；② 页面 id 与目录不一致 → 红在 `:66` 的 `page file %s declares SFPageOutlet page=%q but catalog id is %q`；③ **写成 `pages/badges.vue`（扁平）而目录写 `pages/badges/index`** → 红在 `:82` 的「catalog entry references missing page file」 | `B/B-15.txt` |
| **B-16** | **ThemeCompiler ViewModel registry**：`viewmodel_registry.go` 新增 `coreViewModel("forum.badge.index", "sforum.page.badge_index@1", <Kind>, BadgeIndexPageViewModel{})`；定义表 36 → 37 | backend | `grep -c "coreViewModel(" apps/api/app/Support/ThemeCompiler/viewmodel_registry.go`（现 **37**，含 `:109` 的 `func coreViewModel(` 声明行 ⇒ 36 条定义；改后 38/37）；`go test ./app/Support/ThemeCompiler -run TestCorePageViewModelRegistry`（族名以文件内为准） | 漏登记 → 主题编译找不到该 page 的 ViewModel schema，`/pages/resolve` 回落到 `provider="core"`（B-21 红），而**单测可能全绿**——这是七件套里最容易被漏的一件，判据只能是 B-21 的浏览器证据 | `B/B-16.txt` + `B/B-21.txt` |
| **B-17** | **`Populate` case**：`source.go:108` 的 `switch request.PageID` 有 `case "forum.badge.index":`，且**真的填数据**（不是空 struct） | backend | `go test ./app/Models/PageViewModels -run TestCorePageViewModelSourcePopulatesEveryCatalogContract`；加一条本刀新断言拟名 `TestBadgeIndexPageViewModelCarriesCatalogRows`（断言 `request.Data.BadgeCatalog.Groups` 非空且条数与 store 一致） | ① 漏 case → `Populate` 返回零值，但上面那条「每个契约都 populates」**只断言不报错，会绿**；真正的红在**拟新增**的 `TestBadgeIndexPageViewModelCarriesCatalogRows`（`want 3 catalog badges, got 0`）；② 只加 catalog 不加测试 map → 红在 `source_test.go:238` 的 `if len(requests) != len(pages.Catalog())`，文案 `test catalog drift: requests=%d catalog=%d`（`:239`） | `B/B-17.txt` + `B/B-17-red.txt` |
| **B-18** | **Host island 三处一致**：① `template.go:30` 的 `allowedHostIslands` 含新岛；② `theme_body_island.go:11` 的 `productionThemeIslandBindings()` 有新岛 + `ComponentID`；③ `theme_body_island.go:78` 的 `RequiredThemeBodyIslandTag("forum.badge.index")` 返回同一标签名 | backend | `go test ./app/Support/Pages -run TestBuiltinThemesCoverAllThemeablePages`；`go test ./app/Support/Pages -run TestProductionThemeIslandBindings`（族名以文件内为准） | ① 有绑定无 allowlist → 模板编译被拒；② 有 allowlist 无绑定 → 红在 `builtin_theme_completeness_test.go:77` 的 `no body island mapping for themeable page %s`；③ `RequiredThemeBodyIslandTag` 漏 case → 模板校验时 tag 为空串，红在 `:80` 的 `%s template %s missing required island <%s>` | `B/B-18.txt` |
| **B-19** | **内置主题模板**：`templates/badges.html` 存在，含 `<!-- sforum.page.badge_index@1 -->` 首行注释、`data-theme-owned="presentation"`、`data-page="forum.badge.index"`、navbar / body 岛 / footer 三段 | frontend | `ls extensions/builtin/themes/sforum-default/templates/badges.html`；`go test ./app/Support/Pages -run TestBuiltinThemesCoverAllThemeablePages` | 漏 `data-theme-owned="presentation"` → 红在 `builtin_theme_completeness_test.go:84` 的 `%s template %s missing data-theme-owned=presentation`；漏首行契约注释 → 编译期 schema 校验红 | `B/B-19.txt` |
| **B-20** | **`theme.json` 声明**：`pages[]` 新增 `{id:"sforum.default-theme.badge_index", action:"replace", target:"forum.badge.index", template:"templates/badges.html", contract:"sforum.page.badge_index@1"}`；页数 34 → 35；`templates/` 文件数 35 → 36（含 shell） | frontend | `grep -c '"target"' extensions/builtin/themes/sforum-default/theme.json`（现 **34**）；`ls -1 extensions/builtin/themes/sforum-default/templates/ \| wc -l`（现 **35**） | 模板在、`theme.json` 没声明 → 红在 `:62` 的 `missing replace declaration for %s`；`contract` 写错版本 → 红在 `:68` 的 `%s contract=%q want %q`。**注意**：这两条红都在「模板已存在」的前提下才出现，所以 B-19 与 B-20 必须一起验 | `B/B-20.txt`（两个计数命令 + 测试输出） |
| **B-21** | **Manifest V3 三处一致 + digest 复算**：① **模板文件** digest 四处相等——`theme.json.pages[].template` 路径 ↔ `templates[].digest` ↔ `packageFiles[kind=template].digest` ↔ `sha256sum templates/badges.html`（实测 `2f889809…4b55`）；② **包级** digest 与 `theme_runtime_publications` 账本行、以及页面引用的静态资源目录（`/_sforum/assets/themes/sforum.default-theme/<digest>/`）一致。改完必须 `extension digest --write` → `extension validate` → `extension test` | frontend | ① `sha256sum extensions/builtin/themes/sforum-default/templates/badges.html` 与 manifest 两处 digest 比对；② 激活后比对 `/site/active-theme/skin`、`/pages/resolve`、账本行（`select revision, package_digest, actor_user_id, approval_source, created_at from theme_runtime_publications order by revision desc limit 3;`）、页面资源目录四处 | **口径陷阱**：`c8105b4f…f34a` 是**包级** digest，**`sha256sum` 复算不出来**（manifest 顶层没有 digest 字段）。谁要「用 sha256sum 复算包级 digest」，一定失败——那不是漏了一步，是复算对象选错了。只改模板不改 manifest → `extension validate` 报路径/摘要不一致；只改 manifest 不重算 digest → `extension test` 报漂移 | `B/B-21.txt`（四条 CLI + 四处比对） |
| **B-22** | **V3 目录重生成**：`public-page-surfaces.json` 36 → 37；`validate-public-page-surface-catalog.mjs` 绿（页面集合与 `Pages.coreCatalog` **完全一致，既不多也不少**） | backend | `node tests/validate-public-page-surface-catalog.mjs`；`grep -c '"contractVersion"' docs/extensions/v3/catalogs/public-page-surfaces.json`（现 **36**）；`node tests/validate-v3-p0-catalogs.mjs`；**不得用 `\| tail` 决定退出码**——管道退出码会把失败写成 `EXIT=0`，本刀已发生过一次 | 加 catalog 不重生成 → 红在 `validate-public-page-surface-catalog.mjs:75` 的 `core page ${id} is missing from the public page surface catalog`；反向（目录多一条）红在 `:78` 的 `public page surface catalog lists unknown page ${id}`。**两个方向都要造一次** | `B/B-22.txt` + `B/B-22-red.txt`（两个方向的变异） |
| **B-23** | **渲染证据（主题运行时完成）**：`curl -H 'Accept: text/html'` 取 `/badges` 得 **HTTP 200**；**承载 `data-provider` 的元素（`div.sf-page-outlet`）的 `data-page` 必须是 `forum.badge.index`；带 `data-page` 属性的元素恰好 2 个、取值全部为该页 ID**；`data-provider` 非 `core`、`data-template="1"`、`sf-page-outlet` 恰好 1 个、无可见 fallback；且 `/pages/resolve?id=forum.badge.index` 的 `selectedPackageDigest` 与 `/site/active-theme/skin` 的 `packageDigest` **相等** | ops + reviewer | 见 **§4-1**（七条：仪器用法、改前基线、digest 可复算命令、③ 三条硬断言、⑦ 两条独有红态） | 见 §4-1 三条假绿：① 只 grep `data-provider`/`data-template` → **404 页也会绿**；② 不带 `Accept: text/html` → 拿 JSON 错误体；③ 用 `resolve?path=` → 422 `pages.id_required`。**另有两种「假红」写法要避开**（实测）：写「页面里只有一处 `data-page`」→ 实测 **2 处属性**；对原始 HTML 文本断言「所有 `data-page` 值都等于它」→ 原始串里 6 处 `data-page`，其中 4 处是 Nuxt 内联 payload 的转义 `data-page=\"…\"`、**两处是 `system.server_error`**。两个元素的身份：`div.sf-page-outlet`（带 provider/template/host-chrome）与主题模板根 div（带 `data-theme-owned="presentation"`） | `B/B-23.txt` + `B/B-23-dom.txt` + `B/B-23.png` + `B/B-23-digest.txt`（reviewer 复验原文：`B-23.reviewer.txt`） |

### 4-1 B-23 的取证仪器与三条假绿（ops 在 `/tags` 上验通后带回，2026-11-11T13:0x）

B-23 是全刀唯一一条「主题运行时完成」判据。**它原来的写法（`GET /pages/resolve?path=/badges`
+ grep `data-provider`/`data-template`）会放过一个根本没做出来的页面**。按下面七条重写。

#### ① `/pages/resolve` 只吃 `id`，`path` 参数不存在

路由注册在 `apps/api/app/Http/Controllers/Pages/controller.go:106`
（`api.Get("/pages/resolve", h.resolve)  // ?id=forum.home`）；`:125-134` 读 `id`，
取不到就 `:133` `fiber.NewError(fiber.StatusUnprocessableEntity, "pages.id_required")`。

```bash
# 错（实跑 422 pages.id_required）
curl -sS 'http://127.0.0.1:8080/api/v1/pages/resolve?path=/badges'
# 对
curl -sS 'http://127.0.0.1:8080/api/v1/pages/resolve?id=forum.badge.index'
```

#### ② `/pages/resolve-path` 只处理**插件新增**路径，核心 catalog 页走它必然 404

`controller.go:617` 的 `resolvePath` 只查 `registry.ResolveAddedPathMatch(path)`（`:630`）；
查不到就 404 `pages.not_found`。**核心页（`forum.badge.index`）不注册在那里**，
所以这条路径 404 是**设计行为，不是缺陷**，不得作为失败判据、也不得作为「页面不存在」的证据。

#### ③ 最大的假绿：404 页同样由主题渲染

今天 `/badges` 是 404，而那个 404 页照样带 `data-provider="sforum.default-theme"` 与
`data-template="1"`——只有 `data-page` 是 `system.not_found`
（模板 `extensions/builtin/themes/sforum-default/templates/not-found.html` 首行
`data-page="system.not_found"`）。

⇒ **只 grep `data-provider` / `data-template` 会把「页面根本没做出来」判成通过。**
必须补三条硬断言：

| # | 断言 | 命令 |
| --- | --- | --- |
| a | HTTP **200**（不是 404/307） | `curl -sS -o /dev/null -w '%{http_code}\n' -H 'Accept: text/html' http://127.0.0.1:3000/badges` |
| b | `data-page` **等于 `forum.badge.index`**，且**不得**为 `system.not_found` | `grep -o 'data-page="[^"]*"' page.html` |
| c | `div.sf-page-outlet` 在页面内**唯一**（防一个页面挂两个 outlet） | `grep -c 'sf-page-outlet' page.html` → 必须 `1` |

`sf-page-outlet` 的三个来源要分清：`SFPageOutletRender.vue:39`（主题/Registry 渲染路径）、
`SFNotFoundPage.vue:27`、`SFSystemErrorPage.vue:31`（错误页自己的 outlet）。
计数为 1 说明只走了一条渲染路径；为 2 说明错误页与内容页同时渲染（通常是 fallback 叠加）。

#### ④ curl 必须带 `-H 'Accept: text/html'`

不带时 Nuxt 对 `/badges` 返回 **JSON 错误体（实测 934 B）**，里面没有任何 `data-*` 属性，
grep 全空，会被误读成「主题没渲染」。证据命令里写死这个 header：

```bash
curl -sS -H 'Accept: text/html' 'http://127.0.0.1:3000/badges' -o page.html
```

#### ⑤ 改前基线（before 侧，ops 实测 2026-11-11T13:0x）

| 探测 | 结果 |
| --- | --- |
| `GET /api/v1/pages/resolve?id=forum.badges`（复数，**故意用旧建议名**） | **404 `pages.not_found`** |
| `GET /api/v1/pages/resolve-path?path=/badges` | **404 `pages.not_found`**（②的设计行为） |
| `GET http://127.0.0.1:3000/badges`（带 `Accept: text/html`） | **404** |

> **注意 `localhost` → `127.0.0.1` 有 307 规范化**：先跟随后才是 404。
> 证据里贴 `curl -L` 的最终状态码，别把 307 当成「页面还在」。

#### ⑥ digest 一致性：可复算命令（ops 实测 `equal: True`）

```bash
python3 -c "import json,urllib.request as u;s=json.load(u.urlopen('http://127.0.0.1:8080/api/v1/site/active-theme/skin'))['data'];r=json.load(u.urlopen('http://127.0.0.1:8080/api/v1/pages/resolve?id=forum.badge.index'))['data'];print(s['packageDigest'],r['selectedPackageDigest'],s['packageDigest']==r['selectedPackageDigest'])"
```

输出必须是 `<digest> <同一 digest> True`。**只比一处不算**：skin 与 resolve 是两条独立读取路径，
上一轮已出现过「skin 发新、resolve 发旧」的分叉形态（见计划 §3.5 末段）。

#### ⑦ 两条红态必须造出来（R10，且是 B-23 独有的红）

| 变异 | 期望红在 | 证据 |
| --- | --- | --- |
| 删掉 `theme.json` 的 `pages[]` 条目、**保留** `templates/badges.html` → 渲染回落 Core | `want data-provider != "core", got "core"` | `B/B-23-red-fallback.txt` |
| 把渲染出的 `data-page` 改成 `system.not_found`（模拟 404 页） | `want data-page="forum.badge.index", got "system.not_found"` | `B/B-23-red-notfound.txt` |

**第二条是第 ③ 点那条假绿的机械判据**：没有它，B-23 只是半个守卫——它证明「主题在跑」，
不证明「跑的是这个页面」。

## 5. 前端与视觉（B-24..B-27）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-24** | **i18n 遍历型断言（双向）**：扫描 `apps/web/app` 下新页面与其组件里出现的每个 `t('...')` 字面键，与 `zh-CN.json` / `en-US.json` 求差——**缺键必须为空**；反向再扫 `badges.catalog.*` 命名空间，**多余键必须为空**（死文案也算失败）。两份 locale 的键集必须逐条相等 | frontend | 新测试 `apps/web/tests/badges/publicBadgeCatalog.test.ts`，照 `apps/web/tests/admin/adminBadgesI18n.test.ts` 的 `flatten()` + 双向 diff 模式；`bun test apps/web/tests/badges/publicBadgeCatalog.test.ts` | ① 组件写了 `t('badges.catalog.empty')`、locale 没加 → 红在点名句（`missing keys in zh-CN: badges.catalog.empty`），页面表现为**裸键名**；② locale 加了但组件没用 → 红在（`unused keys in badges.catalog: badges.catalog.retry`）。**上一刀的结构性盲区就是只有单向检查**（`modules/badges.md` residual 11：整块 `badges` 键缺失、84 条断言 0 FAIL） | `B/B-24.txt`（两条 bun test 输出）+ `B/B-24-red.txt` |
| **B-25** | **空态与失败态 UI**：0 徽章 → 显示 `badges.catalog.empty` 文案（不是空白页、不是「加载中」卡死）；接口失败 → 显示 `badges.catalog.loadFailed` + `retry` 按钮，点击后重新请求（可观测第二次请求） | frontend + ops | Playwright：mock 空响应 / mock 500 各一次，断言文案节点存在且 `retry` 可点击；点 `retry` 后断言请求数 +1 | 失败态复用空态文案 → 红在点名句（`want loadFailed copy, got empty copy`）。**这是产品判断**：库挂了却告诉用户「站内没有徽章」等于撒谎（与 B-11 同一条链） | `B/B-25.png` ×2 + `B/B-25.txt` |
| **B-26** | **视觉 QA**：≥1280 与 **390×844** 两档截图；无横向溢出（`document.scrollingElement.scrollWidth <= innerWidth`）；tier 不能**只靠颜色**区分（要有文字或图标，`aria-label` 带 tier 名）；分组标题是标题层级而非纯样式 | designer 定规范 / ops 取证 | `tests/browser-public-390x844.mjs` 同款脚本改路径；两档各一张图 + 溢出断言输出 | 只靠颜色 → 红在 a11y 断言（`tier must not be color-only`）；390 档溢出 → 红在 `scrollWidth 412 > innerWidth 390`。**上一刀 J-10 同款**：窄屏溢出必须重新取证，不能沿用旧图 | `B/B-26.png` ×2 + `B/B-26-dom.txt` |
| **B-27** | **入口可达**（`pend-待裁`，见计划 §8-1）：从站内某处可点进 `/badges`，且入口文案已本地化 | ops | 依 Lead 裁决：① 公共导航 → 导航项 + i18n labelKey + 点击后 URL 断言；② 资料页 tab 内链 → 点击断言；③ 仅 URL 可达 → 本条记 `pend` 并写明裁决依据 | 若做了入口但 `labelKey` 没进两份 locale → 导航显示裸键（红在 B-24 的遍历型断言）；若裁决为「不做入口」而证据里却有导航项 → 反向不一致，判口径未对齐 | `B/B-27.txt`（裁决引用 + 点击证据或 `pend` 说明） |

## 6. 后台信息架构（B-28）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-28** | `/badges` 后台页从 `adminSidebarNavigation` 的 **forum 文件夹**（`adminModules.ts:449` 起的 children，`:455` 那一行）移到 **「用户与权限」文件夹**（`:439` 的 `admin.nav.userPermission`，children 现为 `/users` `/roles` `/permissions`）；**不新建文件夹**，`ADMIN_NAV_SECTION_IDS` 与 admin framework validation 不动；`badge.view` 的 `requiredPermissions`（`:225-230`）不变 | frontend | 浏览器打开后台，展开「用户与权限」看到徽章项、展开「论坛」看不到；`bun test apps/web/tests/admin/` 全绿（含 `adminRouteLocalization`）；`node tests/validate-admin-framework.ts` | ① 只加进新文件夹没从旧的删 → 导航出现两个徽章项（红在导航快照/计数断言）；② 顺手改了 `labelKey` → `adminRouteLocalization` 红（`admin.nav.badges` 在 locale 里存在，但若被改名则缺键）；③ 新建了文件夹 → `ADMIN_NAV_SECTION_IDS` 契约与 validation 红 | `B/B-28.txt` + `B/B-28.png` |

## 6-bis 第二轮新增（B-29..B-30）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **B-29** | `GET /api/v1/badges` 的响应头 `Cache-Control` 含 **`private`** 且**不含** `public`；含 `earned` 的响应不得进共享缓存 | backend | **新增具名测试 `TestPublicBadgeCatalogIsPrivateCacheable`**（Lead 已裁，非拟名）：`app.Test(req)` 后读 `resp.Header.Get("Cache-Control")`，断言 `strings.Contains(v,"private") && !strings.Contains(v,"public")`；HTTP 侧 `curl -sI` 再验一次 | **照抄** `setPublicCacheable`（`controller.go:713`，函数体 `c.Set(fiber.HeaderCacheControl, "public, max-age=30")`）→ 红在点名句（`want Cache-Control containing private and not public, got "public, max-age=30"`）。**这条不是洁癖**：`/badges` 返回查看者自己的 `earned`，`public` 会让共享缓存把 A 的 earned 投给 B；且该控制器**没有 `Vary` 兜底**（`rg -n "Vary" apps/api/app/Http/Controllers/Badges/controller.go` 只命中 `:721` 的注释文字，`c.Set` 上零命中；控制器另新增 `setViewerScopedNoStore`（`:723`）写 `private, no-store`） | `B/B-29.txt`（测试输出 + `curl -sI` 头）+ `B/B-29-red.txt` |
| **B-30** | `catalog.go` 的新条目**必须写成一整行**。生成器用**逐行正则**解析：`scripts/v3-catalog/generate.mjs:463` 的 `for (const line of body.split('\n'))`，随后 `field()` / `flag()` 都只在**当前行**上匹配。判据：生成后该行**字段非空**（`contractVersion` / `coreComponent` 非空串、`replaceable` 为 `true`），且 `grep -c '"contractVersion"' docs/extensions/v3/catalogs/public-page-surfaces.json` = **37** | backend | `node scripts/v3-catalog/generate.mjs` 后跑 `grep -A6 '"id": "forum.badge.index"' docs/extensions/v3/catalogs/public-page-surfaces.json` 看字段；再跑 `--check` 与下游校验器 | 把条目拆成多行（为了「可读性」）→ **该行还在**，但字段被**清空**：`"contractVersion": ""`、`"coreComponent": ""`、`"replaceable": false`；**`--check` 仍然 exit 0**（假绿成立）。⇒ 准确说法是「**静默丢字段**」，不是「静默丢页」。**第二张网**：下游 `tests/validate-public-page-surface-catalog.mjs:39` 的 `page row is incomplete: …` 会抓到并 exit 1 ⇒ 真相是「`--check` 假绿，但下游校验器能兜住」。判据必须写成「该行字段非空」，不能写「grep 无命中」——后者在拆行时**仍会命中 id**，红不出来 | `B/B-30.txt`（字段 + 计数）+ `B/B-30-red.txt`（拆行后的「字段清空 + `--check` 绿 + 下游 exit 1」） |

> B-30 的计数命令用 `grep -c '"contractVersion"'`（现 **36**，本刀后 **37**），不要用行数近似；
> 路径是 `docs/extensions/v3/catalogs/`（复数目录）。

## 7. 横切面覆盖对照（防漏项，验收前逐条对）

| 横切面 | 覆盖条目 |
| --- | --- |
| i18n 键存在性（**遍历型**，双向） | **B-24**（+ B-27 的 labelKey、B-28 的导航文案） |
| 目录计数 tripwire（409 → 410） | **B-07**（+ B-08/B-22 的生成物计数） |
| 错误码本地化 | **B-12** |
| 开关关闭态（`features.badges=false`） | **B-09** |
| 空数据与失败路径 | **B-10** / **B-11** / **B-25** |
| 迁移可逆（本刀零迁移，写成断言） | **B-13** |
| Page Registry 七件套认同同一 page ID | **B-14..B-22**（七件：catalog / route shell / registry / Populate / island / 模板 / 完整性测试） |
| 渲染证据（非 core + template=1） | **B-23** |
| 缓存头 actor 相关性（`private`，D37） | **B-29** |
| 生成器逐行解析（静默丢页的假绿） | **B-30**（+ B-08/B-22 的生成物计数） |

## 8. 本组不覆盖（避免把矩阵绿冒充仓库绿，R16）

- **不覆盖** `./scripts/test.sh --fast` 三线：B 组全绿也不构成可提交依据。
- **不覆盖** D35（不给默认分档限额 / 推荐分档一键应用）与 D36（关注/订阅纵列）：只登记，见计划 §3.1/§3.2。
- **不覆盖** profile 徽章端点的 `threshold`/`current`（进度语义，`dto.go:26`、`:74-78`）：本刀不改。
- **不覆盖** `sforum_core_v1.forum_badges` 视图与 `sforum.core.public_badges.list` HostQuery：插件面，本刀不动。
- **不覆盖** `badge.view` 的「单独持有进不了后台」residual：既有事实，本刀不改语义（见计划 §3.3 第 3 条）。
- **不覆盖** N 组与 M-02（信任等级基线门的残余）：跨刀条目只在需要时引用，不相加。
- **不覆盖** 既有 13 条徽章端点的缓存头（D37 末句）：`setPublicCacheable` 对 profile 端点仍正确。
- **不覆盖** `controller.go:247` 的全局 404 映射（D38）：改它同时影响 `forum.search` 与
  `forum.profile.show` 的关闭态；本刀只按 B-09 分别断言两条路径。
- **不覆盖** 公共导航栏入口（§8-1 裁 ②）：可配置公共导航平台 M7 未收，B-27 走资料页内链。

## 9. 待确认项 → 本轮已回填三条（保留原编号）

1. **B-27 入口归属：已裁 = 资料页徽章 tab 内链**（计划 §8-1）。不做公共导航栏；**不接受「仅 URL 可达」**。
   本行不再记 `pend-待裁`；落地方式（新增 prop vs 调用点渲染）由 frontend 定，验收只钉
   「资料页徽章 tab 里点得到 `/badges`」+ 文案已本地化。
2. **B-06 空组口径：已裁 = 省略空组**（计划 §8-2）。本行断言文本不变。
3. **B-01 路径与 route ID：已裁 = `/api/v1/badges` + `core.route.badges.public_catalog`**（计划 §8-3）。
   B-07 的注释行按此写，不再改。
4. **B-17 的 ViewModel Kind 仍未定**（复用 `ViewModelList` 还是新 kind）：由 backend 决定。
   若新 kind 进 `ThemeCompiler` 的 kind 枚举，需补一条「kind 白名单未放宽」的复核。
5. **B-21 的 CLI 命令原文仍未钉死**（以 `docs/extensions/` 现行手册为准）：取证时把**实跑命令**
   抄进证据文件，不抄手册示例——手册与 CLI 参数曾不同步。
6. **B-29/B-30 是本轮新增编号**：不与 B-01..B-28 重排；若 backend 认为 B-30 应并入 B-14，
   改号不影响内容，只需同步脚本 ID。
