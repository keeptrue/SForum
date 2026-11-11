# 2026-11-11 Session Handoff — `/badges` 公共目录页（互动系列收口）

Status: **PASS-with-residual（reviewer 独立判定，2026-11-11T14:12 回填激活后复验）**。
**未提交，本会话工作树**（采集 2026-11-11T14:1x，`HEAD = 9e3e1cbe8`，复算 `git rev-parse HEAD`）。
验收：`reports/2026-11-11-badges-public-catalog-matrix.md`（B-01..B-30）；
独立判定原文：`storage/qa/badges-public-catalog/VERDICT.md`（299 行，本地产物、重跑即得）。
**计数不抄进本文件**（会变）；要数字先重跑，命令见 VERDICT §5。

## Changed

- **公开读面**：`GET /api/v1/badges`（route `core.route.badges.public_catalog`，`Guard.Kind=public`）。
  返回全部 `enabled` 徽章（含未获得，`earned` 标记），**不返回** `threshold`/`metric`；
  按 `badge_groups.position, badges.position, badges.id` 排序、不分页、硬上限 500 + `truncated`。
  缓存头 `private, no-store`（D37）。路由计数 tripwire **409 → 410**。
- **Page Registry 七件套**：catalog 条目（`catalog.go:137`，单行，`pages/badges/index`）、
  route shell `apps/web/app/pages/badges/index.vue`、ThemeCompiler ViewModel registry、
  `PageViewModels.Source.Populate` + `mapping.go`、Host 岛 `sf-badge-index-page`
  （ComponentID `forum.component.badge_index`）、内置主题 `templates/badges.html` + `theme.json`、
  完整性测试四件。
- **主题激活**：包级 digest `2f06a9e4…` → **`c8105b4f…f34a`**，`theme_runtime_publications`
  revision **22** / `actor_user_id=1` / `approval_source='admin'` / `2026-11-11 14:07:54.875571+08`。
  只改源码 + 重启 API **不会**生效（`stagedEnabledArtifact` 只写 staged）。
- **入口**：资料页徽章 tab 的「查看全部徽章」内链（`SFProfileBadgeTab.vue` 双 variant）。
- **后台 IA**：`/badges` 从 admin 的 forum 文件夹移到「用户与权限」。
- **后台侧栏裸键已修（14:09:53）**：`admin.nav.badges` 在两份 locale 补齐
  （`zh-CN` `"徽章"` / `en-US` `"Badges"`），新增遍历型守卫
  `apps/web/tests/admin/adminNavLabelKeys.test.ts`（扫 `adminModules.ts` 里 **56 个字面 labelKey
  （55 唯一）**，页级 + 文件夹级 + 扩展页全覆盖，另有一条「禁止运行时拼装 labelKey」的防绕过断言）。
  红态实测：删 `zh-CN.admin.nav.badges` → `adminNavLabelKeys.test.ts:66`
  `missing labelKey in zh-CN: admin.nav.badges`。`bun test tests/admin/adminNavLabelKeys.test.ts`
  → 3 pass / 0 fail。
- **知识库**：`modules/badges.md` 的 Cache Semantics 改写（第六条幻影）、residual 9 结案、
  新增 residual 15（`SFProfileBadgeTab.vue` 514 行）；本文件与
  `decisions/2026-11-11-badges-public-catalog-and-series-closeout.md`（D35–D38）。

## Decisions

**D35（YG）**：不给默认分档限额，D27 保持；「推荐分档」改后台一键应用（显式点击才落库）。只登记。
**D36（YG）**：下一纵列 = 关注/订阅（follow + watch），复用 `content_bookmarks` 提醒管线。只登记。
**D37（Lead）**：`/badges` 缓存头必须 `private`、禁 `public`——它返回**查看者自己**的 earned，
`public` 会把 A 的 earned 投给 B；既有 13 条（返回被查看者的）不动。
**D38（Lead）**：关闭态页面 404 / API 403 不统一，**不动** `Pages/controller.go:247` 的全局映射。
四条原文、理由与被否方案见 `decisions/2026-11-11-badges-public-catalog-and-series-closeout.md`。

## 本刀换来的纪律（新的，六条）

1. **404 页也由主题渲染，且激活前的 Core fallback 是静默的。**
   404 页带 `data-provider="sforum.default-theme"` + `data-template="1"`，只有 `data-page="system.not_found"`。
   更狠的是激活前实测：`<title>` 正确、真实数据渲染出来、`data-page` 也对、outlet=1、
   **屏幕上一点异常都没有**——只有 `data-provider` 是 `core`、`data-template` 是 `0`。
   ⇒ 只 grep provider/template 或只看截图都会误判。判据只能是
   「承载 `data-provider` 的元素其 `data-page` = 目标页 ID」+ provider/template 两值。
2. **「只加一种语言会红」是假绿。** `Message()` 缺键回退 `DefaultLocale`
   （`Localization/messages.go:846-853`），`Message("en-US", code)` 拿到中文、≠ key，
   旧守卫照样绿。判据必须是**直接查目录**的 `TestAPIErrorCodesAreLocalizedInEveryLocale`。
3. **管道吞退出码。** `cmd | tail -3` 的 `EXIT=0` 是 `tail` 的；B-22 就是这么把失败写成 0 的
   （真因还叠了一层：`validate-v3-p0-catalogs.mjs` 用相对路径起子进程，cwd 错就 `MODULE_NOT_FOUND`）。
   判据：`out=$(cmd); rc=$?`，且故意改坏一次确认 `rc != 0`。
4. **`labelKey` 是数据字段，不是 `t()` 调用。** i18n 遍历只扫 `t()` 字面键，admin 测试只查**文件夹级**
   `labelKey` ⇒ `admin.nav.badges` 在 HEAD 就缺、2079 条全绿照不出来（本刀现场）。
   机械判据是**跨注册表的遍历守卫**：`apps/web/tests/admin/adminNavLabelKeys.test.ts` 扫
   `adminModules.ts` 里 **56 个字面 labelKey（55 唯一）**、逐个查两份 locale，外加一条
   「禁止运行时拼装 labelKey」的防绕过断言；红态是删一个键 →
   `:66` `missing labelKey in zh-CN: admin.nav.badges`。
   **只写「补了守卫」不算判据**——要能说出扫了多少个键、红在哪一行。
5. **`go test` 不带 `-count=1` 的 `(cached)` 不能证明「这一版」**（本刀 10 个绿文件命中）。
   进证据的 `go test` 一律 `-count=1`。
6. **行号是 `HEAD` 的瞬时值。** 本刀内 backend 改 `Badges/controller.go` 就把 `setPublicCacheable`
   从 `:640` 推到 `:713`、公开 profile 路由从 `:65` 推到 `:87`。引用行号必须带时点 +
   `grep -n` 复算；矩阵里已有一批行号是旧值。

## Next

1. **B-26 视觉 QA 未取证**（ops 在采，reviewer 待独立复核：两档截图 + `scrollWidth <= innerWidth`
   + tier 非纯颜色 + 单一 chrome）。到货前不要对运营宣布「视觉已验」。
2. **矩阵三处描述已按 reviewer 复验改正**（B-30 静默丢**字段**、B-23 的 `data-page` 断言措辞、
   B-21 的 digest 复算口径）；**B-07 的「唯一机械判据」仍过宽**——它只覆盖「加路由忘改计数」方向，
   删路由是被生成器的 stable-identity 守卫（`generate.mjs:824`）接住的。下一轮收窄。
3. **`validate-v3-p0-catalogs.mjs` 依赖 cwd，只有从仓库根跑才绿。**
   `tests/validate-v3-p0-catalogs.mjs:8` 用 `const root = process.cwd()`，靠 `scripts/test.sh:8-9`
   （`ROOT_DIR=` + `cd "$ROOT_DIR"`）才成立；从 `apps/api` 跑就会 `MODULE_NOT_FOUND`，
   而本刀已经实际踩红过一次（又被 `| tail` 吃成 `EXIT=0`，见纪律 3）。
   同目录 `validate-public-page-surface-catalog.mjs:14` 用
   `resolve(dirname(fileURLToPath(import.meta.url)), '..')` 自定位，从任何 cwd 都对。
   **一行修法**：把 `process.cwd()` 换成同样的 `import.meta.url` 自定位。
4. **D35 的「推荐分档一键应用」UI** 与 **D36 的关注/订阅纵列**都未开工，按决策记录起计划。
5. `SFProfileBadgeTab.vue` 514 行的 residual：第三个消费者出现时抽 `SFBadgeCard.vue`。

## Open Questions

- **互动系列是否正式关闭**：点赞 / 收藏 / 徽章+统计 / 信任等级 / `/badges` 目录五刀都已 landed
  （本刀待 B-26）。父计划 `plans/2026-07-12-iteration-a-engagement-loop.md` 是 completed；
  是否把五刀归档进 `archive/2026-11/`，由 Lead 收口时决定。
- **`badge.view` 的死权限 residual** 仍在（可达性前提是 `admin.access`），本刀未动语义。
- **公开目录是否需要 HostQuery 面**：D38 同批裁为「不加」（本刀不新增数据域）；
  若将来插件要替换目录页，按 `2026-10-18-extension-data-surface-policy.md` 单独开。
