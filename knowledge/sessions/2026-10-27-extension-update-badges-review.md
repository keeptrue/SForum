# 2026-10-27 扩展更新徽章 T5 验收（Core）

本文件是「扩展管理三页显示商城更高版本徽章」的最终验收记录，供 T6 引用。
商城仓同名文件是同一批工作的商城侧记录：`sforum-app-store/knowledge/sessions/2026-10-27-extension-update-badges-review.md`。

最终运行态：`sforum.store` enabled / active version `22851` / `0.1.1` / package_digest `6bf70405…` / staged 空；
三条 `list_column` surface contractVersion 均为 `@2`，placement `@1`，priority 10，invokable true。

## Changed

### Core（本仓）

- `apps/web/app/utils/admin/adminSurfaces.ts`（+73/−1）
  新增 `AdminListBadge` 与 `resolveAdminListBadges()`：把 `list_column` 在某个资源上的
  单元格折叠成徽章，`links` 经 `safeAdminPageId()` 只接受 `/` 开头且非 `//`、不含反斜杠与
  控制字符的后台相对路径；非法值降级为不可点击徽章。用 `Object.hasOwn` 取 `links`，避免
  `constructor`/`toString` 这类原型键。
- `apps/web/app/composables/admin/useAdminListSurfaces.ts`（+12）
  暴露 `badgesFor()`、`failures`、`hasListColumns`。`hasListColumns` 依据**已发现的 surface**
  而不是已解析的列，保证查询失败时「检查更新」按钮仍在，warning 里的重试才有入口。
- `apps/web/app/components/admin/extensions/SFAdminExtensionUpdateBadges.vue`（新增，55 行）
  有 `pageId` 时渲染 `<a>`，否则渲染 `<UBadge>`；`aria-label` 拼上扩展名。
- `apps/web/app/components/admin/extensions/SFAdminExtensionIdentity.vue`（新增，120 行）
  可选中行的身份簇：更新徽章放在选择 `<button>` **之后**，避免 `<a>` 嵌 `<button>`。
- `apps/web/app/pages/admin/extensions/{plugins,themes,index}.vue`
  三页各自 `useAdminListSurfaces({ pageId, resources, context })`，行内渲染徽章；
  查询失败时渲染 `UAlert`（标题 `admin.extensions.updateCheck.failed`）+ 重试。
- `apps/web/i18n/locales/{zh-CN,en-US}.json`（各 +5）：`updateCheck.refresh|failed|retry` 等。
- `apps/api/app/Support/ComponentCatalog/core_catalog_gen.go`（+9）：登记两个新组件。
- `docs/extensions/{authoring-guide.md, v3/catalog-identities.json, v3/catalogs/*}`：契约文档与
  生成产物同步。
- 测试：`apps/web/tests/admin/adminExtensionUpdateBadges.test.ts`（新增）、
  `adminListSurfaces.test.ts`（+28）、`adminSurfaces.test.ts`（+161）。
- 决策：`knowledge/decisions/2026-10-27-extension-update-badges-admin-surface.md`。

## 验证命令与结果

| 命令 | 结果 |
| --- | --- |
| `node tests/validate-architecture-boundaries.mjs` | 通过：1963 个生产文件，206 个仍在 500 行阈值之上 |
| `node scripts/v3-catalog/generate.mjs --check` | 通过：`V3 P0 catalogs are current (18 files)` |
| `cd apps/web && bun run typecheck` | exit 0（`.nuxt-typecheck` 已生成） |
| `cd apps/web && bun test` | **1767 pass / 0 fail**，10569 expect()，208 文件，8.65s |
| `cd apps/api && go build ./...` | exit 0 |

## 浏览器 QA 证据

三类证据严格分开：**真实商城数据**、**fixture 注入**、**浏览器层响应注入**。
fixture 注入不是真实插件数据；浏览器层注入既不是 fixture 也不是真实数据。

环境：共享浏览器窗口 1657×922（视口 1633×810）；390×844 由隔离无头 Chrome
（`Emulation.setDeviceMetricsOverride`，dpr 3）跑，避免占用共享窗口。
截图目录：`/tmp/sforum-qa/shots`（易失，见文末说明）与持久副本 `~/下载/sforum-qa/`。

### A. 真实商城数据

- 三页 @1633×810：`a[href*="pages/catalog?product="]` = 0，warning = 0，
  `button[aria-label="检查更新"]` 存在且可见，`documentElement.scrollWidth` = 1633（无横向溢出）。
  截图 `A1-plugins-real-empty-1633x810.png`、`A2-themes-real-empty-1633x810.png`、
  `A3-overview-real-empty-1633x810.png`。
- 真实数据徽章旁证（04:34，真实基址，经商城仓 owner 用 `audit_events` 核对确为真实基址窗口）：
  直接 invoke 三条 surface 返回 `cells {sforum.oauth2-provider: "可更新至 v1.7.0"}`，
  总览页徽章真实挂上，`href=/control-panel/extensions/sforum.store/pages/catalog?product=oauth2-provider`，
  点击后 URL 变为 `?product=oauth2-provider` 且 `.sfstore__overlay` 打开，弹层文本含
  「已安装 v1.6.0 / 可更新至 v1.7.0」。
  截图 `03-overview-real-badge-1633x810.png`、`04-overview-badge-click-catalog-overlay-1633x810.png`（1853×961 实窗）。
  同一时刻远端 `latestVersion=1.7.0`；04:41 起远端改为 `1.6.0`（== 已装），因此后续真实路径回到空结果。
- **05:06 之后远端商城对该批量查询返回 502**（`curl` 复现：`HTTP 502 in 1.78s`，单 extensionId 同样 502），
  因此最终状态下真实路径表现为 warning 而非空结果。这是上游状态，不是本功能回归。

### B. fixture 注入（本轮由 reviewer 重建）

`/tmp/sforum-qa` 在 05:0x 被外部清空（fixture 与截图一并丢失），因此本轮重建了等价 fixture：
自签 CA（SAN 含 `IP:127.0.0.1`）装入系统信任、Node HTTPS 服务监听 `127.0.0.1:8443`、
路径前缀 `/update` 用于隔离插件缓存键；插件设置通过
`PUT /api/v1/admin/extensions/sforum.store/settings` 切换并 `POST …/restart` 立即生效。

- 三页徽章（1633×810）：plugins → `可更新至 v1.7.0` → `?product=oauth2-provider`；
  themes → `可更新至 v1.1.0` → `?product=theme-quiet`；overview → 两条都在。
  三条均 `inButton:false`、`nested:0`、`visible:true`。
  截图 `B1-overview-fixture-update-1633x810.png`、`B3-plugins-fixture-update-1633x810.png`、
  `B4-themes-fixture-update-1633x810.png`。
- 徽章归属正确：主题页两行显示名同为「SForum 默认主题」，徽章挂在 `acme.switchtest · v1.0.0`
  那一行，`sforum.default-theme` 行 `hasBadge:false`（builtin 被插件按设计跳过）。
- 点击插件页徽章：`href=/control-panel/extensions/sforum.store/pages/catalog?product=oauth2-provider`，
  落地页 `.sfstore__overlay` 存在（`class="sfstore__overlay sfstore-dialog"`），弹层含
  「已安装 v1.6.0 / 可更新至 v1.7.0」。截图 `B2-overview-badge-click-catalog-overlay-1633x810.png`。
- 总览页徽章点击不改变选中态：给 22 个行按钮挂 click 监听后以 `ctrlKey` 合成点击徽章，
  `rowButtonClicked=0`，`aria-pressed` `false → false`，URL 不变（仍是 `/control-panel/extensions`）；
  默认选中行 `Built-in Shortcodes` 保持 `aria-pressed=true`。
- fixture 503：三页 `UAlert` 标题「扩展更新检查未完成。」+ 描述
  「可更新: 管理端界面扩展运行时暂时不可用，请稍后重试。」+「重试」按钮，
  `button[aria-label="检查更新"]` 保留，徽章 0。
  截图 `D1-plugins-fixture-503-warning-1633x810.png`、`D2-…`、`D3-…`。
- 未配置 `catalog_base_url`：三条 surface 均 200 且 `cells {}`、`links {}`（19/13/17 ms，无上游调用）；
  页面无徽章、无 warning、「检查更新」保留。
- 真实传输失败 `https://127.0.0.1:9`：三条 surface 503，页面 warning + 重试 + 按钮保留。
  截图 `E1-overview-502-transport-warning-1633x810.png`。
- 插件停用：`/api/v1/admin/admin-surfaces` 返回 0 条；三页均 0 徽章、0 warning，
  且「检查更新」按钮**消失**（`hasListColumns=false`）。截图 `F1-plugins-store-disabled-1633x810.png`。
  再启用后回到 enabled / active 22851 / `0.1.1` / staged 空，三条 surface 全部回来。

### C. 浏览器层响应注入（非法 links / 无 link）

在页面里包一层 `fetch` 改写 surface 响应的 `cells`/`links`，再点「刷新」重放：

- `links = {"sforum.oauth2-provider": "//evil.example.test/x"}`：
  `labelNodes=[{tag:"SPAN", inAnchor:false, title:"可更新"}]`，`productAnchors=[]`，`evilAnchors=0`。
  截图 `C1-plugins-illegal-link-badge-span-1633x810.png`。
- `links = {}`（cells 有值）：同上，`labelNodes` 为 `SPAN`、`inAnchor:false`，`productAnchors=[]`。

结论：非法/缺失深链降级为不可点击徽章，DOM 里没有任何 `<a href>`。

### D. 只读临时用户

- 只带 `extension.view`：访问 `/control-panel/extensions/plugins` 被重定向到 `/`
  （admin 中间件要求 `admin.access`，见 `apps/web/app/middleware/admin.ts`）。
  截图 `I1-deny-extension-view-only-redirect.png`。**任务书里「只带 extension.view 即可打开三页」的
  前提不成立**，正确的最小只读组合是 `admin.access` + `extension.view`。
- `admin.access` + `extension.view`：三页均可打开（plugins / themes / overview），
  徽章 1 / 1 / 2 条，`href` 带 `/control-panel` 前缀，0 warning，「检查更新」存在。
  截图 `G-plugins-readonly-1633x810.png`、`G-themes-readonly-1633x810.png`、
  `G-overview-readonly-1633x810.png`。
- 只读性由后端保证：以该用户 cookie 调 `POST /admin/extensions/sforum.store/disable` 与
  `POST …/upgrade` 均返回 `403 {"reason":"permission.denied"}`，插件仍 enabled。
- 但 UI 仍渲染可点击的管理动作：plugins 页 64 个管理按钮中 53 个 `disabled=false`
  （含 `全部升级（10）`、`升级`、`禁用`、`重启`、`卸载`），overview 页 61 个中 53 个同样可点。
- 清理：三只临时账号 user id 427 / 428（只读用例）与 429（仅 `extension.view` 的拒绝用例）均执行
  「撤销全部会话 → 清空角色 → `status=banned`」，临时角色 `qa-extension-viewer`、
  `qa-extension-view-only` 已删除（`GET /roles` 中无 `qa-*` 残留）。
  产品没有删除用户的路由（`apps/api/app/Http/Controllers/Identity/routes.go` 只有
  PATCH/PUT，无 DELETE `/users/:id`），因此是「封禁 + 清权 + 踢会话」收尾，未做 DB 硬删以避免外键残留。
  回读：user 428 / 429 均为 `status=banned`、`roleKeys=[]`、`permissions=[]`、活跃会话 0。

### E. 390×844

隔离无头 Chrome、`390×844@dpr3`、真实 390 视口截图（1170×2532）：

| 页面 | scrollWidth | overflowX | clipped 控件 | 徽章 | 徽章尺寸/可见 | 检查更新 | warning |
| --- | --- | --- | --- | --- | --- | --- | --- |
| plugins | 390 | false | 0 | 1 | 119×24，在视口内 | 有 | 0 |
| themes | 390 | false | 0 | 1 | 118×24，在视口内 | 有 | 0 |
| overview | 390 | false | 0 | 2 | 119×24 / 118×24，均在视口内 | 有 | 0 |

截图 `H2-390x844-plugins-fixture-update.png`、`H2-390x844-themes-fixture-update.png`、
`H2-390x844-overview-fixture-update.png`。

## Findings

### P1

无。最终 build 上未发现阻断性缺陷；三条 surface 的契约版本、props/result schema digest
（props `39aec253…`、result `e740a0a4…`）与直接 invoke 200 均可复核。

### P2

1. **只读管理员看到可点的管理动作 —— pre-existing，非本功能引入（out of scope，由 Core 后续排期）**。
   `can('extension.plugin.manage')` 只用于内置自动更新开关与加载失败提示（HEAD 行 114/129/133/411），
   行内的 `禁用/启用/重启/卸载/升级/全部升级/上传 ZIP` 从未按权限门控，本次实现没有改动这一层。
   git 证据：`git show HEAD:apps/web/app/pages/admin/extensions/plugins.vue` 的六条 `:disabled` 绑定
   （`upgradeAllBusy || !canUpgradePlugin(item)`、`item.status !== 'enabled'`、
   `!isExtensionArtifactAvailable(item)` ×2、`!canRestartPlugin(item)`、
   `item.status === 'enabled' && !isLifecycleV2Plugin(item)`）与工作区逐字相同，仅行号平移
   （HEAD 585/601/621/630/652/665 → 工作区 620/636/656/665/687/700）；唯一差异是 artifact 缺失说明段落
   随身份簇抽进 `SFAdminExtensionIdentity.vue` 的 `artifactMissing`，行为不变。
   观测值：只读用户（`admin.access` + `extension.view`）在 plugins 页 64 个管理按钮中 53 个
   `disabled=false`、overview 页 61 个中 53 个；点击后后端 403 `permission.denied`。
   后端权威没有问题，但 UI 邀请了一次注定失败的操作。
2. **失败信息不可区分**。503（插件运行时不可用）、504（surface 5s 超时）、502（上游传输失败）
   三种情况页面文本完全一致：`可更新: 管理端界面扩展运行时暂时不可用，请稍后重试。`
   运维无法据此判断该去重启插件还是去看商城。
3. **重启后出站连接僵死会让同一批缓存键连续超时**。实测：同一 themes 键连续 3 次 504
   （5014 / 5123 / 5018 ms），`POST …/restart` 后三条 surface 立刻 200（1791 / 614 / 594 ms）。
   即「重试」按钮不保证恢复，显式 restart 才可靠。比任务书里「首次上游调用 5s 超时」的措辞更强。

### P3

1. **目录请求失败被静默**。`useAdminListSurfaces` 里 `surfaceFailures` 过滤掉 `id === 'catalog'`，
   而「检查更新」按钮又依赖 `hasListColumns`（surface 非空）。因此 `/admin/admin-surfaces` 本身失败时，
   页面既无 warning 也无按钮，与「该插件没有声明 list_column」在 UI 上不可区分。
2. **同名主题的徽章读屏不可区分**。两个主题显示名都是「SForum 默认主题」，
   徽章 `aria-label` 为 `可更新: 可更新至 v1.1.0 · SForum 默认主题`，读屏拿不到扩展 id；
   只有 DOM 里的 `acme.switchtest · v1.0.0` 行能区分。
3. **未配置商城地址时完全没有提示**（已批准的取舍，本次复核一致：无徽章、无 warning、按钮保留）。

## 未验证项

- **旧商城 400（`oldstore400`）与 `slow`（6s）模式**：本轮 fixture 是 reviewer 重建的最小版本，
  只实现 update 模式；这两种失败语义只有 `surfaces_test.go` 单测覆盖，未在浏览器层复验。
- **401/403/404（不可重试）与 429（可重试）分支**：同上，仅单测覆盖。
- **插件升级路径**（staged → 激活）：为避免改变 active 版本未执行。
- **商城详情页自身的 390 布局**：不属于本功能范围。
- **公开首页 1633 宽横向溢出**（`scrollWidth 1657 > 1633`）：与本功能无关，未纳入范围，仅记录。

## 残余风险

- `plugin/plugins/sforum.store/frontend/admin/src/catalog.ts` 1051 行（HEAD 1021），后续应抽详情选择/平台匹配。
- dev 环境出站代理（`127.0.0.1:7897`）连接复用会在插件重启后僵死，导致 5s 超时且可能连续多次；
  只有显式 restart 可靠（见 P2-3）。生产环境是否复现未验证。
- 商城未配置时无提示（已批准取舍）。
- 旧商城 400 静默空结果 vs 401/403/404 warning：两者在徽章位置都无徽章，只能靠页面级提示区分。
- `admin-surfaces.md` 里 `core.admin.page.users` 的 columns/rowActions/bulkActions/editorPanels
  `yes→no` 是 identity 批次 marker 漂移，out of scope。
- 本轮新增观察：真实商城自 05:06 起对插件的批量目录查询返回 502（单 id 也 502），
  未定位是上游部署还是限流；恢复前「真实空结果」不可复现。

## 环境还原（最终状态）

- `sforum.store.catalog_base_url = https://test-sforum-app-store.dalao.me`、channel `stable`（DB 直读）。
- `sforum.store`：status=`enabled` / active_version_id=`22851` / version `0.1.1` /
  package_digest `6bf70405…` / staged 空（`staged_version_id IS NULL`）。
- fixture 已停止（`127.0.0.1:8443` 无监听）；临时 CA 已从系统信任移除（`/etc/ssl/certs` 无 sforum 链接，
  bundle 回到 120 张，公网 TLS 复测正常）。
- 临时账号 427 / 428 / 429 已「撤销全部会话 + 清空角色 + `status=banned`」，临时角色
  `qa-extension-viewer`、`qa-extension-view-only` 已删除。未做 DB 硬删：产品没有 DELETE 用户路由，
  且硬删会把 `audit_events.actor_user_id` 置 NULL，破坏本次 QA 的审计归属。
- 真实商城自 05:06 起为**整站 502**（`/`、`/api/v1/health`、catalog 三条均 `502 in ~1.4–1.6s`，
  返回边缘 `error code: 502` 页面）；05:15 Lead 复测仍 502，batch 与单 extensionId 都 502。
  属上游状态，非本功能回归。

## 证据位置说明

`/tmp` 在本轮被外部清空过两次（第一次丢失上一轮 fixture 与截图，第二次丢失本轮中途产物），
因此截图同时保存在持久目录 `~/下载/sforum-qa/`；`/tmp/sforum-qa/shots` 为易失副本。
fixture 与截图均不在仓库内，仓库零污染。
