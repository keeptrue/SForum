# 2026-10-27 商城更新徽章：真实数据取证（核心侧）

承接 `sessions/2026-10-27-extension-update-badges-review.md`（T5 验收，当时商城 `list_column`
三条 surface 返回 0 条 cells，徽章路径只有静态测试）。本轮把这条路径跑成有真实数据的证据，
并给出「为什么之前是 0」的两种解释与复现方法。

## 结论

- 徽章链路在 dev 上**完整成立**：三条 surface 200 → 行内 `<a>` 徽章 → 深链落到商城页 → 商品弹层打开。
- 「只看有更新」**确实把商城徽章行计入**（A/B：plugins 11→12、themes 1→2、overview 12→14）。
- 真实商城当前**没有**任何「目录版本 > 已装版本」的组合，所以真实数据下就是 0 徽章、0 warning；
  这一点是上游商品状态，不是功能缺陷。
- 取证必须用 fixture：真实目录对 `sforum.oauth2-provider`(1.6.0)、`sforum.fetch-url`(1.0.3)、
  `sforum.web-search`(1.1.0) 只发布同版本商品（逐个 `extensionIds=` 查询确认，`products/<slug>`
  的 `releases` 也只有一版）。

## 关键事实（可复算）

| 项 | 值 |
| --- | --- |
| 三条 surface | `sforum.store.admin.updates.{plugins,themes,overview}@3`，label `商城更新`，priority 10，invokable true |
| 徽章 DOM | `<a title="商城有新版 vX · 来自 sforum.store">` + `span.iconify.i-lucide:arrow-up-right` |
| 徽章位置 | 不在 `button` 内（`a.closest('button') === null`），点击不改变行 `aria-pressed` |
| 深链 | `/control-panel/extensions/sforum.store/pages/catalog?product=<slug>` → `.sfstore__overlay.sfstore-dialog` |
| 计数 A/B | plugins 11→12 / themes 1→2 / overview 12→14（空目录 ↔ 有更新） |

## 取证方法（fixture）

`~/sforum-qa/2026-10-27-update-badges/fixture/catalog-fixture.mjs`：Node HTTPS 服务，
`https://127.0.0.1:8443`，两个前缀 `=/update`（有更新）与 `/update-empty`（空目录），
`GET /update/__fixture` 返回 `{"fixture":true}` 作为数据来源标识。自签 CA（SAN=IP:127.0.0.1）
临时装入系统信任，取证后已移除。

要点（踩过的坑，下次照做）：

1. 插件设置只接受 **https** 基址（`settings.go:110`），所以 fixture 必须带 TLS；插件出站受
   `no_proxy=127.0.0.1` 覆盖，本地直连不会被代理吞掉。
2. `readme` 必须是**对象**（`upstreamReadme`），写成字符串会让插件 decode 失败并对外回 502
   `upstream_error`（`/api/store/product` 直连即可复现）。
3. 设置切换约 1–2s 生效（不必 `restart`；`POST …/restart` 在本插件上会 422 `extension.lifecycle_invalid`）。
4. 目录缓存按完整 URL（10 分钟 fresh），换前缀即换缓存键，A/B 不需要清缓存。

## 发现（未改核心代码，任务约束）

1. **P2 徽章不是首屏就绪**：overview 实测从整页加载到 3 枚徽章齐备约 **8.3s**（plugins 5–8s）。
   疑似 surface 调用早于行资源就绪 → 插件收到空 `resources` 直接返回空 `cells`，资源到达后重算。
   这解释了本轮最初「0 徽章」的误判；等 3–4s 读会读成 0，等 9s 才对。**这是可测的时序缺口**，
   不是数据问题。
2. **P3 同版本不同 digest 的 staged 行会掩盖 A/B**：`sforum.oauth2-provider` 已装 1.6.0、
   staged 也是 1.6.0 但 digest 不同 → 核心判定「重启后生效」，两种模式下都被计数。
   要隔离徽章贡献必须选**无 staged** 的行（本轮用 `sforum.page-component-reference` 与 `acme.switchtest`）。
3. 三条 surface 对「本页没有的资源」不做过滤（插件按传入 `resources` 回答），页面只发本页行，
   所以接口层同时给三条资源会看到三处相同 cells —— 不是重复渲染。

## 商城仓（第二步）

工作区改动已存在且门禁全绿（由 teammate 实跑，只读）：

| 命令 | 结果 |
| --- | --- |
| `cd plugin/plugins/sforum.store/backend && go test ./...` | exit 0 |
| `bun run test`（根 `test` = `--filter service test`，`--parallel=1`） | 826 pass / 0 fail |
| `bun run manifest:check` | exit 0 |

三处文案逐字一致：`surfaces.go:434-446`（`商城更新` / `商城有新版 v`、`Store updates` /
`Update in store v`）、`manifest/admin-surfaces.json` 三条 `label` + `@3`、`surfaces_test.go:301/304/359/543/552`。

两个待办（teammate 发现，未改）：

- `surfaces_test.go:109-122` 的清单比对结构体**不含 `Label`**，三条 label 的漂移没有测试保护。
- 站内商城页自己的卡片徽章仍是 `copy.ts:93` `可更新至 v{version}`（与宿主列表徽章的新文案不同源），
  要不要统一属另一个任务。

## 证据位置

`~/sforum-qa/2026-10-27-update-badges/`（稳定路径，非 /tmp）：

- `dom/page-assertions.md`：逐页断言表、A/B 计数、弹层内容、时序观察
- `dom/surface-invoke-fixture.json`：三条 surface 原始输出
- `shots/*.png`：plugins / themes / overview 三页 + 两次点击弹层（1633×810）
- `fixture/`：fixture 服务与自签证书（可重跑）

## 环境还原

- `sforum.store.catalog_base_url` 已指回 `https://test-sforum-app-store.dalao.me`（channel stable）。
- fixture 进程已停（8443 无监听）、系统信任里的 `sforum-qa-fixture.crt` 已删除并
  `update-ca-certificates --fresh`。
- 真实上游复测：`/api/v1/catalog` 200；三条 surface 200 且 `cells {}`（无更高版本），页面 0 徽章、
  0 warning、「只看有更新」回到 12。

## Next

- 若要真实数据（非 fixture）长期可复现：给商城上传一个比已装更高的版本（例如
  `sforum.oauth2-provider` 1.7.0），或接受 fixture 作为常规手段并在 CI 里固化本仓的 A/B 脚本。
- 时序缺口（发现 1）值得单独立项：把 `useAdminListSurfaces` 的首轮调用改成等 resources 就绪，
  或对空 `resources` 的结果不当作最终答案。

## 追加：行徽章改为 chip（同日，用户反馈「文字不够显眼」）

**症状**：灰字纯文本落在 `已启用` / `运行中` / `重启后生效` 一排 chip 之外，读成元信息，
而它是整行唯一的可点动作；同时 surface 下发的 `tone` / `icon` 在 Core 侧被丢弃。

**改动**（Core 侧，视觉层）：

- `apps/web/app/components/admin/extensions/SFAdminExtensionUpdateBadges.vue`：
  改用 `UBadge`（`variant="soft"`、`:color="badge.tone"`、
  `:icon="badge.icon || 'i-lucide-arrow-up-circle'"`），有 `links` 时尾部加
  `arrow-up-right` 并包在 `<a>` 里；不可点分支仍是 `<UBadge>` + `cursor-default`。
  tone/icon 现在真正生效，Core 不改写插件声明的语义。
- `apps/web/tests/admin/adminExtensionUpdateBadges.test.ts`：把「次级文字链接」
  断言反转为 chip 断言（`<UBadge>`、`variant="soft"`、`:color="badgeColor(badge)"`、
  `trailing-icon`、不得再出现 `text-slate-600`）。
- `docs/extensions/authoring-guide.md`：补一条——`tone` 决定徽章颜色、`icon` 是前置图标，
  尾部箭头只在有 `links` 时出现。
- 决策记录 `decisions/2026-10-27-extension-update-badges-admin-surface.md` 追加修订段。

**验证**：`bun test tests/admin/` 607 pass / 0 fail；`bun run typecheck` exit 0；
`node tests/validate-architecture-boundaries.mjs` 通过（1973 files）。
按用户要求**未跑浏览器验证**。

**未做（仍待决定）**：文案仍是最长的「商城有新版 v{version}」；若缩短成「新版 v{version}」
属商城仓 `surfaces.go` 改动，且 label 是声明的一部分，必须同时把 `contractVersion`
从 `@3` 抬到 `@4` 并同步 manifest 与 `surfaces_test.go`。
