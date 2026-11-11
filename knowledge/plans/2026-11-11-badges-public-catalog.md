# 2026-11-11 `/badges` 公共目录页（互动第四刀·收官缺口）

Status: **accepted 待实施**（口径由 YG / Lead 在 2026-11-11 会话裁决；本文件只拆任务，不含实现）。
基线：`HEAD = 9e3e1cbe8`（采集 2026-11-11T12:43:51+08:00，复算 `git rev-parse HEAD`；`git status --porcelain` 空）。
前序：`sessions/2026-11-11-trust-level-baseline-gate.md`（四条硬纪律 + Next 五项）、
`decisions/2026-11-11-trust-level-baseline-narrowing.md`（D34）、
`plans/2026-11-11-trust-levels-baseline-narrowing.md`（§1 / §4-bis / §8）、
`modules/badges.md`（Known Residuals 11–14 = i18n / 计数 tripwire / 错误码 / 统计条文案）、
`plans/2026-11-10-badges-and-user-activity.md`（§12 T1–T12 陷阱清单）。
验收矩阵：`../reports/2026-11-11-badges-public-catalog-matrix.md`（**B-01 起**，唯一编号源）。

## 0. 修订记录（先读这节）

本仓计划被自己推翻过 32 条决策，直接读正文会拿到过期口径。本节是唯一的时间线。

| 时点 | 改动 |
| --- | --- |
| — | 本节时点是**会话内近似值**（本文件尚未提交）；提交后以 `git log -1 --format=%cI -- knowledge/plans/2026-11-11-badges-public-catalog.md` 为准（R15）。 |
| 12:5x | 首版。登记 D35 / D36 两条裁决（**只登记不实现**）、公开面四条口径、IA 归属（`/badges` 从 admin forum 文件夹移到「用户与权限」）。 |
| 12:5x | **自纠 1（与 Lead 转述不一致）**：Lead 说「`catalog.go:131` 起 `coreCatalog` 共 **35** 条」。实测 **36** 条：`var coreCatalog = []PageDefinition{` 在 `catalog.go:127`，`grep -c 'ID: "' apps/api/app/Support/Pages/catalog.go` = **36**，与 `docs/extensions/v3/catalogs/public-page-surfaces.json` 的 `grep -c '"contractVersion"'` = **36** 一致。本刀新增后是 **37**，不是 36。 |
| 12:5x | **自纠 2（与 Lead 转述不一致）**：Lead 说「内置主题 `templates/` 现有 **35** 个模板文件」。文件数确为 35（`ls -1 .../templates/ \| wc -l` = 35），但其中 `plugin-page-shell.html` 是 **shell**，页面模板是 **34** 个，与 `theme.json` 的 `grep -c '"target"'` = **34** 对齐。本刀新增页面模板后：文件 36 / 页面 35。**验收时两个数字都要给，别只给一个**——只给 35 会让人以为 35 个页面都已主题化。 |
| 12:5x | 登记 §8 待确认 6 条（公开入口归属、空组是否出现、`/api/v1/badges` 路径命名、HostQuery 是否需要新面、`truncated` 的契约位置、V3 UI 面计数是否随页面增加）。 |
| 13:10 | **第二轮修订（Lead 追加裁决）**：① §3 新增 **D37**（`/badges` 响应缓存头必须 `private`、禁止 `public`；依据 `controller.go:639-642` 的 `setPublicCacheable` 写 `public, max-age=30`，而 `/badges` 返回**查看者自己**的 earned）与 **D38**（`RequiresFeatures` 关闭时页面 404 / API 403 不统一，本刀**不动** `controller.go:247` 的全局映射）；② §8 六条待确认**全部回填为已裁**（保留编号）；③ route shell 路径 `pages/badges.vue` → **`pages/badges/index.vue`**，`CoreComponent` 同步为 **`pages/badges/index`**（为将来的 `/badges/:slug` 详情页留 index + [id] 形状）；④ 矩阵新增 B-29（缓存头）、B-30（catalog 条目必须单行——生成器逐行正则）与「0-3 工具陷阱」。 |
| 13:10 | **自纠 3**：Lead 的 §8-6 原话写 `node tests/v3-catalog/generate.mjs --check`，实测该路径**不存在**（`ls tests/v3-catalog` 报 `No such file or directory`），生成器在 **`scripts/v3-catalog/generate.mjs`**。本文件按实测路径写，`--check` 语义不变。 |
| 13:10 | **自纠 4**：Lead 的 §8-1 原话说「`variant='profile'` 分支加内链」，实测 `SFProfileBadgeTab.vue:19-25` 的 props 只有 `groups` / `loading` / `failed`，**全仓无 `variant`**（`grep -rn "variant" apps/web/app/components/forum/SFBadgeChip.vue apps/web/app/utils/forum/forumBadge.ts` 零命中）。裁决的**位置**（资料页徽章 tab）照写；**落地方式**改为「新增一个显式 prop，或在唯一调用点 `SFProfileShowPage.vue:417` 旁渲染链接」，由 frontend 定；验收只钉「tab 内可点到 `/badges`」。 |
| 13:2x | **第三轮（B-23 + M4 完成判据）**：矩阵 B-23 按 ops 的实测仪器重写（`resolve?id=` 而非 `?path=`；补 HTTP 200 / `data-page=forum.badge.index` / `sf-page-outlet` 唯一三条硬断言），并新增矩阵 **§4-1** 七条（含 before 基线、digest 可复算命令、两条独有红态）；本节新增 **§3.5 末段**（源码 `7b4c868c` vs 运行 `2f06a9` 的既有分叉）与 **§4-1**（M4 五步激活判据，含 `ErrThemePreviewStale` 与 `approval_source` 口径）；§5.3 补运行制品 digest 行。时点为会话内近似值。 |
| 13:2x | **第四轮（证伪 + 补登）**：① 矩阵 **B-12 判据被证伪并重写**——`Message()` 缺键回退 `DefaultLocale`（`messages.go:846-853`），「只加 zh 不加 en」旧守卫照样绿；改判据为 `TestAPIErrorCodesAreLocalizedInEveryLocale`（`locales_test.go:159`），§0-2 假绿**三 → 四**。② 新增 **§3.7**（既有事实：徽章没有缓存层，`api_assembly_domains.go:554` 是裸 store；`CachedStore` 只在 `app/Models/Forum`，`modules/badges.md` 那句指 `BadgeRef` 随 Forum 载荷进缓存）。③ 新增 **§4-2**（实现期两条事实：`./app/Support/Pages` 的绿要 M2+M4 两方齐；`SFProfileBadgeTab.vue` 509 行的 residual）。④ §5.3 补登 `generate.mjs` 的 `routePolicy` 分支与 `catalog-identities.json` 的预登记。 |
| 13:4x | **第五轮（只改行号与证据点）**：`registry_test.go` `:506/:507` → **`:508/:509`**（`!= 409` → `410` 已落地，`410` 注释在 `:506-507`）；`source.go` 等级闸 → **`:114` 调用 / `:697` 定义 / `:702-708` 分支**；`Badges/controller.go` 的 `setPublicCacheable` → **`:713`**、调用点 `:148`、新增 `setViewerScopedNoStore` 在 **`:723`**（写 `private, no-store`）；§3.6 的 `source.go:113` → `:114`；§5.1 的 `controller.go:65` → `:87`。矩阵侧另加 B-29 具名测试、B-22 的 `\| tail` 陷阱、B-13-red 的 echo 问题与 **§0-4「证据自身的假绿」三类**。 |

> **本文件的行号是 `HEAD` 的瞬时值。** 本刀内 backend 改 `Badges/controller.go` 就把
> `setPublicCacheable` 从 `:639-642` 推到了 `:713`，把公开路由从 `:65` 推到 `:87`。
> 凡引用行号必须带时点与 `HEAD`，复算方式是 `grep -n`；不要把它当常量传下去。

## 1. 目标与完成即验收

**目标**：普通用户能在一个公开页面上看到站内**有哪些徽章、怎么拿、自己拿到了哪些**。
今天徽章只有三个落点：`/admin/badges`（要 `admin.access`）、资料页第 5 个 tab（只显示**已获得**）、
帖子旁 chip（只显示最高级 1 个）。这是互动四刀里**唯一剩下的用户可见缺口**。

完成 = 下列七条**同时**可复现（AGENTS.md「Page Registry 全套」硬要求，缺一项不算完成）：

| # | 完成判据 | 复现证据 |
| --- | --- | --- |
| 1 | catalog 条目：稳定 page ID + `PathPattern:"/badges"` + `AccessPublic` + `Replaceable:true` + `RequiresFeatures:["features.badges"]` | `grep -n '"/badges"' apps/api/app/Support/Pages/catalog.go` |
| 2 | route shell：`apps/web/app/pages/badges.vue`（`SFPageOutlet` + `useSForumSeo`） | `ls apps/web/app/pages/badges.vue`；`go test ./app/Support/Pages` 的 `TestCatalogCoversAllPublicNuxtPages` 绿 |
| 3 | ThemeCompiler ViewModel registry 条目（36 → 37） | `grep -c "coreViewModel(" apps/api/app/Support/ThemeCompiler/viewmodel_registry.go`（现 37 含 1 行函数声明 ⇒ 36 条定义；新增后 38/37） |
| 4 | `PageViewModels.Source.Populate` 有对应 case，且 `TestCorePageViewModelSourcePopulatesEveryCatalogContract` 的 `len(requests) != len(pages.Catalog())`（`source_test.go:238`）不 drift | `go test ./app/Models/PageViewModels` |
| 5 | Host island：`allowedHostIslands`（`template.go:30`）+ `productionThemeIslandBindings()`（`theme_body_island.go:11`）+ `RequiredThemeBodyIslandTag`（`theme_body_island.go:78`）三处都认识新岛 | `go test ./app/Support/Pages` 的 `TestBuiltinThemesCoverAllThemeablePages` 绿 |
| 6 | 内置主题模板 `templates/badges.html` + `theme.json` 的 `pages[]` 一行（34 → 35） | `ls extensions/builtin/themes/sforum-default/templates/badges.html`；`grep -c '"target"' .../theme.json` = 35 |
| 7 | catalog 完整性测试全绿（`catalog_coverage_test.go:15`、`builtin_theme_completeness_test.go:17`、`source_test.go:179`、`tests/validate-public-page-surface-catalog.mjs`） | 四份命令输出，见矩阵 B-16/B-22/B-23 |

**渲染证据另算**（不算进上面七条，但没有它就不算主题运行时完成）：浏览器实测必须同时看到
`data-provider` **非 `core`** 与 `data-template="1"`；出现 `provider="core"`、`data-template="0"`、
或页面上可见 fallback 提示，**一律判未完成**（AGENTS.md 原文口径）。

## 2. 为什么是 `/badges`

### 2.1 缺口是「可见性」，不是「功能」

徽章域已经 landed（`modules/badges.md`：五表 + 13 路由 + 三视图 + 通知）。缺的是**公开读面**：
今天唯一公开徽章端点是 `GET /api/v1/profiles/:username/badges`
（`core.route.badges.public_profile_badges`，`Guard.Kind = public`，`core_catalog_gen.go:343`），
它按用户返回**已获得**徽章。**「站内有哪些徽章」这件事今天没有任何 HTTP 面**——
`sforum_core_v1.forum_badges` 视图与 HostQuery `sforum.core.public_badges.list` 只服务插件，
不是 HTTP 公开面（`modules/badges.md` Extension Surface）。

### 2.2 三个候选被否

| 候选 | 做法 | 为什么否 |
| --- | --- | --- |
| **A. 扩资料页第 5 个 tab**（加「未获得」区） | 不动 Page Registry，只改 `SFProfileBadgeTab.vue` | 资料页是**某人**的页面，语义是「他有什么」；把「站内有什么」塞进去会让每个访客的资料页都变成目录页，且**未登录用户进不了资料页 tab 的语境**（tab 需要 profile 上下文）。产品问题不是「看得少」，是「没有地方看」 |
| **B. 只做 API，不做页面** | 只加 `GET /api/v1/badges` | 解决不了原始缺口：没有页面就没有入口，普通用户不会去读 OpenAPI。且本刀的产品价值一半在「Page Registry 全套第一次被公开页走通」的验收面 |
| **C. 后台加只读「徽章预览」页** | 挂在 `/admin/badges` 下加 tab | 后台可达性前提是 `admin.access`（`Identity/seeds.go:14`），普通用户永远看不到；这是给运营的自检工具，不是产品面 |

## 3. 已裁口径（**不要重新发明**）

### 3.1 D35（YG 2026-11-11 裁决，本刀只登记不实现）

**不给默认分档限额**，D27 保持：TL1–TL4 默认仍继承站点默认（冷却 / 日限额都是 0）。
「推荐分档」做成后台**一键应用**（预填推荐值，运营显式点击才落库）。

理由（写下来是为了让下一个人不必重推）：默认收紧 TL1 会让**存量站点全体会员**突然有冷却，
破坏面比 D34 大一个数量级。D34 只影响 TL0（新用户 + 未达 TL1 的存量用户），D35 若按「默认收紧」
会影响所有已过新人窗口的会员。

**本刀不实现 D35 的 UI**（不在 `/badges` 范围）。矩阵不覆盖 D35。

### 3.2 D36（YG 同日裁决，本刀只登记不实现）

下一产品纵列 = **关注 / 订阅（follow + watch）**，复用 `content_bookmarks` 的提醒管线
（`forum.bookmark_reminders` 周期任务 + `bookmark_reminder` 通知的形状）。
**本刀不做**；`plans/README.md` 的下一条 active 由 Lead 收口时登记。

### 3.3 公开面四条口径

1. **未获得的徽章可见**：目录页返回**全部 `enabled` 徽章**，含未获得；已获得的用 `earned` 标记。
   目录页的产品目的就是「有哪些、怎么拿」——只显示已获得等于把目录页做成第二个资料页 tab。
2. **不暴露 `threshold` / `metric` 数值**：wire 上只给 `name` / `description` / `icon` / `tier` / `group`。
   理由：阈值是运营调参，**公开即不可撤回**；想公开的运营写进 `description`。
   **不为此新增 option**（避免 D20 那类死配置）。
   `forum_badges` 视图已对插件暴露 `threshold`，那是**插件面**，不是 HTTP 公开面——两者不冲突。
3. **`badge.view` 不是公开读所需**：公开目录走 public guard，与
   `core.route.badges.public_profile_badges`（`Guard.Kind=public`）同口径。
   `badge.view` 保持**后台读键**，今天被 `apps/web/app/config/adminModules.ts:225-230`
   （`id:'/badges'` 的 `requiredPermissions:['badge.view']`）与 admin 路由 guard 使用，**不是死权限**；
   但「单独持有 `badge.view` 进不了后台」这条 residual 仍成立（可达性前提是 `admin.access`）。
4. **分页与排序稳定口径**：按 `badge_groups.position ASC, badges.position ASC, badges.id ASC`
   分组返回（`id` 作 tiebreaker，防 position 相同导致漂移）；**不分页**，服务端常量硬上限 **500** 条，
   超限截断并带 `truncated: true`。
   理由：徽章是运营配置量级（默认安装十几个），分页是为不存在的规模付复杂度；
   但**无上限的公开列表不可接受**。

### 3.4 信息架构归属（Lead 已裁）

徽章权限模块确实是 `forum`（`apps/api/app/Models/Identity/seeds.go:102`），但 `/badges` 后台页
现在挂在 `adminSidebarNavigation` 的 **forum 文件夹**下（`apps/web/app/config/adminModules.ts:455`，
实测行号，采集 2026-11-11T12:43:51+08:00）——它既不是论坛内容，也不是论坛设置。

**裁决**：移到「用户与权限」文件夹（`adminModules.ts:439` 的 `admin.nav.userPermission`，现 children
为 `/users` `/roles` `/permissions`），与它们同轴——徽章主体是用户（授予关系、`user_activity`、
`top_badge_id`）。**不新建文件夹**：新增文件夹要同时改 i18n `labelKey`、`ADMIN_NAV_SECTION_IDS` 契约
与 admin framework validation 三处，收益不抵成本。

> 与 `modules/badges.md`「`badge.view` 是死权限」的关系：那条讲的是**权限键单独存在无法构成后台可达性**，
> 与本节的**导航分组**是两件事。移动文件夹不改变任何权限语义。

### 3.5 D37（Lead 2026-11-11 裁决）：`/badges` 的缓存头必须 `private`，禁止 `public`

`/badges` 的响应里含**查看者自己的** `earned`，与既有公开徽章端点不是同一类对象：

| 端点 | 返回谁的数据 | 缓存头 | 为什么 |
| --- | --- | --- | --- |
| `GET /api/v1/profiles/:username/badges`（既有） | **被查看者**的 earned（actor 无关） | `public, max-age=30`（`controller.go:713` `setPublicCacheable`，调用点 `:148`） | 对同一 URL，任何访客看到的内容相同 ⇒ 可被共享缓存 |
| `GET /api/v1/badges`（本刀新增） | **查看者自己**的 earned（actor 相关） | **`private`**（且不得含 `public`） | 同一 URL 对不同用户内容不同；`public` 会让共享缓存把 A 的 earned 投给 B |

实测（采集 2026-11-11T13:4x，行号是 `HEAD` 瞬时值）：`rg -n "Vary" apps/api/app/Http/Controllers/Badges/controller.go`
只命中 `:721` 的**注释文字**，`c.Set` 上零命中；`rg -n "setPublicCacheable"` 命中 `:148`（调用）与 `:713`（定义）。
**控制器没有 `Vary` 兜底**，所以既有的「按 URL 共享缓存」不能靠「加 Vary」补救，必须**换缓存级别**。

backend 已按 D37 新增 `setViewerScopedNoStore`（`:723`，写 `private, no-store`），并在
`:712` 的注释里写明「`/badges` 必须用它，不能复用 `setPublicCacheable`」。
选 `no-store` 而不是只写 `private`：目录是低频读，宁可每次回源，也不赌缓存键正确。

**不改既有 13 条路由的头**：`setPublicCacheable` 对 profile 端点仍然正确（它返回被查看者的数据）。
若把它们一起改成 `private`，是拿掉一项既有正确行为，不是修 bug。

**既有事实（不是本刀引入）：主题源码与运行制品已经分叉，而且不报错。**
`extensions.updated_at = 2026-11-11 11:25:01` 证明今天启动重算出的源码 digest 仍是 **`7b4c868c`**，
而 `/site/active-theme/skin` 与 `/pages/resolve` 仍在发 **`2f06a9`**（staged `20769` 从未被激活）。
机制：`apps/api/app/Models/Extensions/postgres_store.go:319` 的 `stagedEnabledArtifact` 分支
写死只写 `staged_version_id`，注释原文「已启用的内置制品不得由同步旁路切换 active_version」。

⇒ **只改 `extensions/builtin/themes/**` 源码 + 重启 API，会「看起来改了没生效」。**
M4 的完成判据因此必须是「新 digest 在 skin 与 resolve **两处**同时生效」（见 §4 的 M4 行、§5.3）。

### 3.6 D38（Lead 2026-11-11 裁决）：关闭态下页面 404 / API 403 不统一，本刀不动

`features.badges=false` 时两条路径的语义不同，**都成立**：

| 面 | 结果 | 机制 | 改它的代价 |
| --- | --- | --- | --- |
| 页面 `/badges` | **404** | `catalog` 条目 `RequiresFeatures` → `source.go:114` 调用 `requirePageFeatures`（定义 `:697`），`:708` 返回 `ErrCorePageDataNotFound` → `controller.go:247` 映射 404 `pages.data_not_found` | `controller.go:247` 是**全局映射**，改它同时影响 `forum.search`（`features.search`）与 `forum.profile.show`（`features.public_profiles`） |
| API `/api/v1/badges` | **403 `forum.badges_disabled`** | 徽章域既有口径（`controller.go:82` `requireFeature`），与既有 13 条端点一致 | 改成 404 会与同域 13 条端点分叉 |

**裁决：本刀不动 `controller.go:247`。** 两层语义不同——页面是「这个页面不存在」，端点是「这个产品没开」。
矩阵 B-09 按两条路径分别断言，不合并成一条。

### 3.7 既有事实（不是本刀引入）：徽章**没有**缓存层

`bootstrap/api_assembly_domains.go:554` 是裸 `badges.NewPostgresStore(pool)`，**没有任何装饰器**；
`CachedStore` 全仓只在 `app/Models/Forum`（`cached_store.go:28` 定义、`:75` 构造函数 `NewCachedStore`）。

⇒ `knowledge/modules/badges.md` 的「徽章可以进 `CachedStore`」**指的不是 Badges store 包装**，
而是 Forum 载荷里的 `BadgeRef`（`app/Models/Badges/types.go:158-163`，注释原文「actor 无关……因此可以随
主题/评论进缓存」）随主题/评论进缓存。

对本刀的含义：`GET /api/v1/badges` 是「**缓存层级不存在**」，不是「绕过了缓存」。
`earned` 的防线（D37 的 `private`）因此**不依赖装配方式**——就算将来给 Badges 加 `CachedStore`，
`earned` 也必须每请求装饰、永不进缓存（与 `likedByMe` / `bookmarkedByMe` 同型）。

**标记**：M10 收口时必须改 `modules/badges.md` 那一句，否则下一个人会以为 Badges 有 `CachedStore` 包装。
这是本仓**第六条「幻影」候选**（前五条：`account_age_days`、`roles.*`、`regular`、`ThresholdOptionKey`、
`SeedMemberPermissions`）。

## 4. 里程碑与依赖顺序

**后端是结构性瓶颈**：没有公开读面，前端只能对着空数组渲染，验收无法落 `data-provider` 证据。
所以 M1 必须先行，其余可并行。

| 里程碑 | 内容 | 归属 | 依赖 | 产出 |
| --- | --- | --- | --- | --- |
| **M0** | 本计划 + B 组矩阵 | pm | — | 两个 knowledge 文件（本文件 + matrix） |
| **M1** | 公开目录读面：领域查询（enabled + 分组排序 + 500 上限）+ HTTP 端点（**`Cache-Control: private`，D37**）+ 契约 + 路由登记 + 计数 tripwire 409→410 | **backend** | M0 | 矩阵 B-01..B-13 + **B-29** 证据 |
| **M2** | Page Registry 后端五件套：catalog 条目（**`CoreComponent:"pages/badges/index"`，且必须写成整行，D-单行**）/ ViewModel registry / `Populate` case / island allowlist + production binding / `RequiredThemeBodyIslandTag` | **backend** | M1（`Populate` 要调 M1 的读面） | B-14..B-19 + **B-30** |
| **M3** | 契约与目录生成：`contracts/openapi/paths/badges.yaml` 新 operation、`validate-openapi-refs.rb` 绿、`node scripts/v3-catalog/generate.mjs` 重生成（`routes.json` / `public-page-surfaces.json`） | **backend**（契约文件归 backend，`knowledge` 归 pm） | M1/M2 | B-08/B-22 |
| **M4** | 内置主题模板 + `theme.json` 一行 + Manifest V3 三处一致（template 声明 / `packageFiles` / 双 digest），**且完成判据是「新 digest 在 skin 与 resolve 两处同时生效」**：① `./scripts/build-builtin-plugins.sh`；② 重启 API 让 `SyncBuiltins` 暂存新 digest（`staged_version_id` 变、`active_version_id` **不动**，见 §3.5 末段）；③ 后台 `/control-panel/extensions/themes` 手动激活（**没有 CLI**）；④ 验收 `/site/active-theme/skin` 与 `/pages/resolve?id=forum.badge.index` **同时**变新 digest；⑤ **`approval_source='host'` 不是手动选项**（只由全新安装 genesis 与 startup_repair 写），手动激活记 `approval_source='admin'` + 真实 actor | **frontend** | M2 | B-20/B-21 + **B-23** |
| **M5** | 前端 route shell（`apps/web/app/pages/badges/index.vue`）+ 页面组件（分组、tier 标记、earned 标记、空态、失败态、i18n）+ **资料页徽章 tab 的「查看全部徽章」内链（§8-1 裁决）** | **frontend** | M1（可先用 fixture 开工） | B-24..B-26 + **B-27** |
| **M6** | 视觉规范：tier 的表达（不能只靠颜色）、分组标题层级、空态与失败态文案 | **designer** | M0 | `storage/qa/badges-public-catalog/ui-spec.md` |
| **M7** | 后台 IA 移动：`/badges` 从 forum 文件夹移到「用户与权限」 | **frontend**（一行改动 + i18n 不动） | 无（可并行） | B-28 |
| **M8** | 账号矩阵 + 浏览器取证（≥1280 / 390×844）+ 开关关闭态 / 空数据 / 失败路径取证 | **ops** + **reviewer** | M4/M5 | B-09/B-10/B-11/B-26/B-27 证据 |
| **M9** | 门禁三线 + 独立复核 | **reviewer** | 全部 | `./scripts/test.sh --fast` 三线 exit 0 + `storage/qa/badges-public-catalog/VERDICT.md` |
| **M10** | 知识库收口：`modules/badges.md` residual 9 划掉 + **改掉「徽章可以进 `CachedStore`」那一句**（§3.7：Badges store 无装饰器，进缓存的是 `BadgeRef`）；`index.md` 由 Lead 统一改 | **pm**（只写 `modules/badges.md`，`index.md` 归 Lead） | M9 | 知识库 diff |

**顺序上的两条硬约束**：

1. **M2 不能先于 M1 的读面形状定稿**：`Populate` 的 `case` 要有真实数据来源，否则会写出一个
   返回空 struct 的假 case，`TestCorePageViewModelSourcePopulatesEveryCatalogContract` 照样绿
   （它只断言「不报错」）——那是**假绿**。矩阵 B-17 用「数据非空」的红态钉住这一点。
2. **M4 的模板不能先于 M2 的岛标签定稿**：岛标签名是 `RequiredThemeBodyIslandTag` 的返回值，
   模板里写死一个自造标签会被 `allowedHostIslands` 白名单拒（`template.go:30`）。

### 4-1 M4 的激活路径（**完成判据，不是描述**）

ops 查实：内置主题的激活**只有一条路径，没有 CLI**。五步缺一步都不算 M4 完成。

| # | 步骤 | 可观测判据 |
| --- | --- | --- |
| 1 | `./scripts/build-builtin-plugins.sh`（脚本存在，3713 bytes） | 产物 digest 变化；构建输出无 error |
| 2 | 重启 API，让 `SyncBuiltins` 暂存新 digest | `staged_version_id` **变**、`active_version_id` **不动**（前后两值都贴进证据）。机制：`postgres_store.go:319` 的 `stagedEnabledArtifact` 分支只写 staged |
| 3 | 后台 `/control-panel/extensions/themes` → SForum Default Theme → 激活 → 确认弹窗 | 弹窗里显示的 version/digest 与 staged 一致 |
| 3′ | API 等价（无 UI 时）：先 `GET /api/v1/admin/pages/activate-preview/sforum.default-theme`（`Pages/controller.go:116`），再 `POST /api/v1/admin/extensions/sforum.default-theme/activate`（`Extensions/routes.go:69`） | body **必须用 preview 的值回填**（`version` / `packageDigest` / `currentTheme*`）；与当前 staged 不等会直接 `ErrThemePreviewStale`（`service_lifecycle.go:782-786`） |
| 4 | 默认主题替换 Core 页需 super_admin 显式 `approveCoreReplacements=true`（请求体字段，见 `Extensions/controller_test.go:974` 的 payload 形状） | 不传或传 false → 激活被拒；传 true 才落库 |
| 5 | 验收：`/site/active-theme/skin` 与 `/pages/resolve?id=forum.badge.index` **同时**变新 digest | 用矩阵 §4-1 ⑥ 的 python 一行命令，输出必须 `<digest> <同一 digest> True` |

**`approval_source='host'` 不是手动选项。** 它只由两条路径写入：全新安装 genesis
（`theme_publication_genesis_postgres.go:72` 的 `ThemeApprovalSourceHost`）与「活动制品预检失败时的
`startup_repair`」。当前 active `2f06a9` 健康，不会自动触发。手动激活记的是
`approval_source='admin'` + 真实 actor——**看到 `host` 就该怀疑不是人点的**。

> 为什么把这条写成完成判据：`§3.5` 末段的既有分叉（源码 `7b4c868c` vs 运行 `2f06a9`）
> 意味着「改了源码 + 重启 API」在验收上看不出任何变化。不写死这五步，下一个人会以为改完了。

### 4-2 实现期两条事实（登记，不当缺陷修）

#### 4-2.1 `./app/Support/Pages` 的绿需要 **M2 + M4 两方齐**

backend 用可复现探针（加 catalog 条目 → 跑测试 → 立即还原）实测：**只加 catalog 条目**会让这个包红在三处：

| 红点 | 断言原文 | 缺的是 |
| --- | --- | --- |
| `builtin_theme_completeness_test.go:62` | `missing replace declaration for %s` | M4：`theme.json` 的 `pages[]` 条目 |
| `theme_runtime_test.go:26` | `build %s: %v`（`for _, page := range Catalog()` 循环里） | M2：ViewModel registry 里没有该 page 的 schema |
| `theme_runtime_hot_path_test.go:51` | `build %s ViewModel: %v` | 同上 |

⇒ **这包在 M2 与 M4 都落之前必定红，不是回归。**
不写清这一点，下一个人会把「加完目录条目后 `./app/Support/Pages` 红」当成自己弄坏的，去回退一个正确的改动
（上一刀的纪律 1：**「红在别处」与「我没弄红它」是两个断言**）。

判据：探针还原后 `git status --porcelain` 必须回到基线；红的三条断言名要逐条抄进证据，
不是只写「Pages 包红了」。

#### 4-2.2 `SFProfileBadgeTab.vue` 379 → **509 行**（跨过 500 复核线）

现在一个组件承载**双 variant 单 DOM**（`profile` / `catalog`）。AGENTS.md 把 500 行当「复核内聚性」的提示线、
1000 行当硬警告，所以这不违规，但要登记：

**residual：第三个消费者出现时必须把卡片抽成 `apps/web/app/components/forum/badges/SFBadgeCard.vue`。**
现在不抽，是为了不制造第二份（本仓已抓到五类「同一判断两份实现」）——**不是省事**。
抽取条件写死为「第三个消费者」，避免「两个调用点就急着抽象」和「永远不抽」两种偏差。

## 5. 涉及文件清单

R14：每个路径先 `ls`；通配符不是路径。下面「存在性」列的命令均在 2026-11-11 12:5x 实跑过。

### 5.1 后端（改）

| 路径 | 存在性 / 现值 | 本刀改动 |
| --- | --- | --- |
| `apps/api/app/Models/Badges/postgres_store_catalog.go` | 存在（`ls apps/api/app/Models/Badges/` 命中） | 新增公开目录查询（enabled + 分组排序 + 500 上限） |
| `apps/api/app/Models/Badges/service.go` | 存在 | 公开目录服务方法（**不加**在 `Options.Service` 上，见 T9） |
| `apps/api/app/Http/Controllers/Badges/controller.go` | 存在；`:87` 是既有公开路由 `api.Get("/profiles/:username/badges", ...)` | 新增公开目录路由（同文件同 group 内） |
| `apps/api/app/Http/Controllers/Badges/dto.go` | 存在；`:19-38` 是 profile DTO，`:85-131` 是 admin DTO | 新增**第三个** DTO（不复用上面两个：profile DTO 带 `threshold`/`current`，admin DTO 带 `metric`/`grantMode`） |
| `apps/api/app/Support/Pages/catalog.go` | 存在；`:127` `var coreCatalog`，`grep -c 'ID: "'` = **36** | 新增 1 条（`/badges`，`AccessPublic`，`CoreComponent:"pages/badges/index"`，`RequiresFeatures:["features.badges"]`）→ **37**；**必须写成整行**（生成器逐行正则，见矩阵 B-30） |
| `apps/api/app/Support/ThemeCompiler/viewmodel_registry.go` | 存在；`:43-80` 定义表，`:82` 是 `site.guidelines` 行 | 新增 1 条 `coreViewModel(...)`（36 → 37 条定义） |
| `apps/api/app/Models/PageViewModels/source.go` | 存在；`:108` `Populate`，`:694` `requirePageFeatures` | 新增 `case`（并把数据映射写进 `mapping.go`） |
| `apps/api/app/Models/PageViewModels/mapping.go` | 存在 | 新 DTO → ViewModel 的映射函数 |
| `apps/api/app/Support/Pages/theme_body_island.go` | 存在；`:11` `productionThemeIslandBindings()`，`:78` `RequiredThemeBodyIslandTag` | 新岛 + 新 case |
| `apps/api/app/Support/Pages/template.go` | 存在；`:30` `allowedHostIslands` | 新岛加入白名单 |
| `apps/api/app/Support/Routes/registry_test.go` | 存在；`:506` `if len(catalog) != 409 {` | 409 → 410，并补 `// 410 = 409 + core.route.badges.public_catalog` 注释 |
| `apps/api/app/Support/Routes/core_catalog_gen.go` | 存在；首行 `// Code generated by scripts/v3-catalog/generate.mjs. DO NOT EDIT.` | **不手改**，跑生成器 |
| `contracts/openapi/paths/badges.yaml` | 存在（16652 bytes） | 新 path item + operation |
| `contracts/openapi/schemas/badges.yaml` | 存在 | 新 schema（catalog 响应） |

### 5.2 前端（改 / 增）

| 路径 | 存在性 / 现值 | 本刀改动 |
| --- | --- | --- |
| `apps/web/app/pages/badges/index.vue` | **不存在**（`ls apps/web/app/pages/` 无 `badges/` 目录；`apps/web/app/pages/admin/badges.vue` 是后台页，另一个文件） | 新增 route shell（参照 `pages/guidelines.vue`：`useSForumSeo` + `SFPageOutlet`）。**路径是 `badges/index.vue` 而不是 `badges.vue`**：`/badges/:slug` 详情页是下一纵列候选，AGENTS.md 要求 index + `[id]` 并存时用 `pages/<domain>/index.vue` |
| `apps/web/app/components/forum/SFBadgeCatalogPage.vue` | **不存在** | 新增页面组件（`components/forum/` 已存在） |
| `apps/web/app/components/profile/SFProfileBadgeTab.vue` | 存在；379 行；`:19-25` props = `groups`/`loading`/`failed`（**无 `variant`**） | 加「查看全部徽章」内链（§8-1 裁决）：新增显式 prop 或在调用点旁渲染，由 frontend 定 |
| `apps/web/app/components/profile/SFProfileShowPage.vue` | 存在；`:417` 是 `SFProfileBadgeTab` 的**唯一**调用点 | 若走调用点方案，在此渲染内链；不新增第二个调用点 |
| `apps/web/app/config/adminModules.ts` | 存在；647 行；`:225` `id:'/badges'`，`:439` `admin.nav.userPermission`，`:449` `admin.nav.forum`，`:455` `pageId:'/badges'` | 把 `:455` 那一行移入 `:439` 的 children（`/users` `/roles` `/permissions` 之后） |
| `apps/web/i18n/locales/zh-CN.json` | 存在；7378 行；`:6573` 已有 `"badges": {` 块 | 新增 `badges.catalog.*` 子命名空间 |
| `apps/web/i18n/locales/en-US.json` | 存在；7338 行；`:6533` 已有 `"badges": {` 块 | 同上，键集逐条对齐 |
| `apps/web/tests/badges/publicBadgeCatalog.test.ts` | **不存在** | 新增：i18n 遍历型断言 + 渲染结构断言 |
| `extensions/builtin/themes/sforum-default/templates/badges.html` | **不存在**（目录现 35 个文件） | 新增页面模板（照 `tag-index.html` 的三段壳：navbar / body 岛 / footer） |
| `extensions/builtin/themes/sforum-default/theme.json` | 存在；`grep -c '"target"'` = **34** | 新增 1 条 `pages[]`（→ 35） |

### 5.3 生成物与运行制品（不手改，只重跑）

| 路径 | 现值 | 生成方式 |
| --- | --- | --- |
| `docs/extensions/v3/catalogs/routes.json` | `grep -c '"id"'` = **409** | `node scripts/v3-catalog/generate.mjs` |
| `docs/extensions/v3/catalogs/public-page-surfaces.json` | `grep -c '"contractVersion"'` = **36** | 同上 |
| `apps/api/app/Support/Routes/core_catalog_gen.go` | 首行标注 DO NOT EDIT | 同上 |
| `extensions/builtin/themes/sforum-default/**` 的**运行制品 digest** | 运行中 active = **`2f06a9`**；今天启动重算出的源码 digest = **`7b4c868c`**（`extensions.updated_at = 2026-11-11 11:25:01`）；staged `20769` 从未激活 | `./scripts/build-builtin-plugins.sh` → 重启 API（**只 stage，不动 active**）→ 后台 `/control-panel/extensions/themes` 手动激活（**没有 CLI**）。五步完成判据见 §4-1；只改源码 + 重启会「看起来改了没生效」 |
| `scripts/v3-catalog/generate.mjs` | `routePolicy`（`:117`）新增 `if (path === '/api/v1/badges') return ['public', ...]` 分支（`:137-140`，注释 `:137-139` + `if` 在 `:140`），`reviewedGuardPolicies`（`:258`）新增对应条目 | **生成链硬前置**：缺任一处，生成器在 `:348` 直接抛 `route %s %s has no reviewed guard classification: %s / %s`。不是绕过门禁 |
| `docs/extensions/v3/catalog-identities.json` | 新增 route identity `core.route.badges.public_catalog`（`:2435-2438`，path `/api/v1/badges`）+ 两条 UI identity 预登记：`core.component.page.forum.badge.index`（`:4037`）、`core.component.shared.sf_badge_catalog_page`（`:2477`） | 由 `generate.mjs` 的 identities 通道写入；**预登记不是放宽校验**——retired tombstone 的 append-only 规则仍生效（`validate-v3-p0-catalogs.mjs:167`） |

### 5.4 知识库（本刀只读 + 收口写一处）

| 路径 | 归属 |
| --- | --- |
| `knowledge/modules/badges.md`（281 行；residual 9 就是「`/badges` 未做」） | pm 收口时划掉 residual 9 |
| `knowledge/index.md`（2871 行） | **Lead 统一改**，pm 不动 |
| `knowledge/plans/2026-11-11-badges-public-catalog.md`、`knowledge/reports/2026-11-11-badges-public-catalog-matrix.md` | pm（本刀产出） |

## 6. 判据（沿用 R6/R7/R10/R11/R13/R14/R15/R16，不新发明）

- **R6**：断言必须能在**干净副本**上复现；工作树绿不算，`git worktree add /tmp/x <HEAD>` 也要绿。
- **R7**：判据写在断言里，不写在「预期行为」段落里；每条断言自带失败文案。
- **R10**：每条守卫附「它红过的样子」，且**红在点名那句上**（行号 + 点名文字）。只红在计数上不算。
- **R11**：证据文件要能被下一个人**重跑得到**，不落一次性人工观察。
- **R13**：证据文件写前置校验（开关状态 / 账号 / 现场已清理）+ 采集时间 + 实现文件 sha256。
- **R14**：文档里每个路径先 `ls`，每个计数先 `wc -l` / `rg | wc -l`；通配符不是路径。
- **R15**：会移动的事实（行号 / `HEAD` / 工作树 / 开关 / 账号档位 / 计数）必须带时点 + 复算方式。
- **R16**：矩阵 `0 FAIL` **不得**单独作为可提交依据；必须顺带跑 `./scripts/test.sh --fast` 三线。
- **证据自身的假绿**（矩阵 §0-4 三类）：管道吞退出码 / 手写 echo 冒充断言 / `go test (cached)`。
  这三类**不是被测对象产生的**，验收前对每条证据问「这段输出是哪条命令、哪个版本跑出来的」。

补充两条本刀专用（不是新规则，是 R10/R16 的落地形态）：

- **「全绿」必须写清是哪几包**：至少 `./app/Support/Pages`、`./app/Models/PageViewModels`、
  `./app/Support/ThemeCompiler`、`./app/Support/Routes`、`./app/Http/Controllers/Badges`、
  `./app/Models/Badges` 六包。上一刀「八包全绿」漏了三个包，里面藏着 P0。
- **假绿四条**（本刀高发）：① `Populate` 的 case 返回空 struct（测试只断言不报错）；
  ② 主题模板存在但 `theme.json` 没声明（模板不参与编译，`data-template` 仍是 0）；
  ③ i18n 只查「缺键」不查「多余键」（死文案留在 locale 里）；
  ④ **「只加一种语言会红」也是假绿**：`Message()` 缺键回退 `DefaultLocale`（`messages.go:846-853`），
  `Message("en-US", code)` 拿到中文、≠ key，`TestAPIErrorCodesHaveLocalizedMessages` 照样绿
  ——判据必须换成不经回退的 `TestAPIErrorCodesAreLocalizedInEveryLocale`（`locales_test.go:159`，矩阵 B-12）。

## 7. 本刀不做

- **不做 D35 的「推荐分档一键应用」UI**（只登记决策，见 §3.1）。
- **不做 D36 的关注 / 订阅纵列**（只登记，见 §3.2）。
- **不改 profile 徽章端点的 DTO**：`dto.go:26` 的 `Threshold` 与 `:74-78` 的 `current/threshold`
  是**进度**语义（「你 12/50」），不是「阈值公开」。§3.3 第 2 条约束的是**新目录端点**。
  改它会把资料页 tab 的进度条打断，且不在本刀产品目标内。
- **不改既有 13 条徽章端点的缓存头**（D37）：`setPublicCacheable` 对 profile 端点仍正确
  （它返回被查看者的数据）；本刀只给新端点写 `private`。
- **不动 `controller.go:247` 的全局 404 映射**（D38）：改它同时影响 `forum.search` 与
  `forum.profile.show` 的关闭态行为。
- **不做公共导航栏入口**（§8-1 裁 ②）：可配置公共导航平台（`2026-07-27-configurable-public-navigation-platform.md`）
  M7 未收，本刀不提前接线；入口走资料页徽章 tab 内链。
- **不新增 `forum_badges` 视图列**：视图已对插件暴露 `threshold`（插件面），本刀不动视图。
- **不新增 option**（§3.3 第 2 条已裁：不为「要不要公开阈值」加配置）。
- **不做徽章详情页**（`/badges/:slug`）：目录页一行卡片足够，详情页是无数据的空壳。
- **不动 `badge.view` 权限语义**，不把公开读改成 `permission_any`。
- **不跑 `sqlc generate`**（residual：`models.go` 84 → 约 2481 行并重命名 param）。
- **不改 `knowledge/index.md`**（Lead 收口统一改）。

## 8. 待确认项 → **本轮已全部回填为裁决**（保留原编号）

六条在 2026-11-11T13:10 由 Lead 裁决。**保留编号**，改写为「裁决 + 依据」，供下一位直接执行；
新增的未决项不占用本节的旧编号。

1. **§8-1 公开入口：裁 ② 资料页徽章 tab 内链。**
   位置：资料页徽章 tab（`SFProfileBadgeTab.vue`，唯一调用点 `SFProfileShowPage.vue:417`）
   加一个「查看全部徽章」链接指向 `/badges`。
   **不做公共导航栏**：可配置公共导航平台（`decisions/2026-07-27-configurable-public-navigation-platform.md`）
   M7 未收，本刀不提前接线（要动 SiteChrome / 导航配置 / i18n labelKey 三处）。
   **不接受「仅 URL 可达」**——那是把缺口留在原地。
   **落地方式需 frontend 定**：`SFProfileBadgeTab.vue:19-25` 的 props 只有 `groups`/`loading`/`failed`，
   全仓无 `variant`（见 §0 自纠 4），所以「`variant='profile'` 分支」这个说法在当前代码里没有对应物；
   要么新增显式 prop（如 `showCatalogLink`），要么在调用点旁渲染链接。**验收只钉结果**：
   资料页徽章 tab 里可点到 `/badges`，且链接文案已本地化（矩阵 B-27）。
2. **§8-2 空组：确认省略。** 全部徽章都 `enabled=false` 的分组不出现在响应里（不返回
   `{group, badges: []}`）。理由：目录页不为空区块付版面；「这个组曾经有徽章」不是产品信息。
   矩阵 B-06 按此写，无需改断言。
3. **§8-3 端点：确认 `GET /api/v1/badges` + route ID `core.route.badges.public_catalog`。**
   与 `/api/v1/admin/badges` 同词根（公开面去掉 admin 前缀），与 `public_profile_badges` 同族命名。
   矩阵 B-07 的注释行按此写。
4. **§8-4 HostQuery：确认不加。** 本刀**不新增数据域**，只新增读面；`sforum_core_v1.forum_badges`
   视图与 `sforum.core.public_badges.list` 原样保留（插件面）。若将来目录面需要被插件替换，
   再按 `2026-10-18-extension-data-surface-policy.md` 的四个受批面单独开一刀。
5. **§8-5 `truncated`：确认顶层。** 响应顶层 `truncated: true/false`，不进分组级
   （截断是全局行为，分组级会让前端误以为只截了一组）。
6. **§8-6 V3 UI 面计数：保留「实跑取证，不预设答案」。**
   M3 的完成证据必须包含两条实跑输出：`node scripts/v3-catalog/generate.mjs --check` 与
   `node tests/validate-v3-p0-catalogs.mjs`。**命令路径以实测为准**：Lead 原话写
   `node tests/v3-catalog/generate.mjs --check`，该路径不存在（见 §0 自纠 3），生成器在 `scripts/`。
   新增页面组件是否需要一条 `core.component.*` UI identity，由实跑结果决定，本计划不猜。

### 8-bis 仍未决（不占 §8 旧编号）

1. **`Populate` 用的 ViewModel Kind**（复用 `ViewModelList` 还是新 kind）：由 backend 定。
   若新 kind 进 `ThemeCompiler` 的 kind 枚举，需要一条「kind 白名单未放宽」的复核（矩阵 §9-4）。
2. **主题 `extension` CLI 的命令原文**：以 `docs/extensions/` 现行手册为准，取证时抄**实跑命令**，
   不抄手册示例（手册与 CLI 参数曾不同步，矩阵 §9-5）。
