# 决策：扩展更新徽章复用 Admin Surface `list_column` + `links`

Status: accepted（2026-10-27 落地）
Date: 2026-10-27
Related: 商城仓 `sforum-app-store` 的 `2026-10-27-update-badges-via-admin-surface.md`
（surface 声明与更新判定）、`2026-10-24-app-store-core-install-primitive.md`
（Core 不承载商城目录的同一条边界）、`2026-10-18-extension-data-surface-policy.md`
（数据面只经四种受制裁面）

## 背景

扩展管理三页（`/extensions`、`/extensions/plugins`、`/extensions/themes`）过去
没有任何「有新版本」提示：Core 不判断版本，商城插件也拿不到宿主列表行的渲染
入口。要让管理员在列表上直接看到「可更新」并点进详情，必须同时回答两个问题：
**谁判断更新**（商城），以及**判断结果怎么进宿主页面**（Core 的渲染面）。

三条现成路径都不合适：

- **新增 Provider Slot**：slot 是给可替换实现用的（storage / mail / AI），不是
  给「在列表里加一列」用的；为它开一个 slot 等于把 UI 扩展点伪装成 provider。
- **Core 加商城专用接口、目录或表**：违反
  `2026-10-24-app-store-core-install-primitive.md` 的 out-of-tree 边界，且 Core
  不该知道「商城」这个词。
- **让插件替换整页**：`list_column` 已经有注册表、placement、权限与审计；为了
  一列徽章去替换整页是过度授权。

## 决策

1. **复用 Admin Surface 的 `list_column`**：商城插件声明一个 `list_column`
   surface，挂在扩展三页的 placement 上。宿主按页面加载调用一次（批量带上
   页面资源行：`id` + attributes），把 `cells[resourceId]` 渲染成行内徽章。
2. **结果新增可选 `links`**：`resourceId → 宿主后台路径`。宿主在归一化时逐条过
   `safeAdminPageId`（`apps/web/app/utils/admin/adminSurfaces.ts`）：必须以 `/`
   开头、不以 `//` 开头、不含反斜杠、不含 `U+0000`–`U+001F`；不合规条目**单条
   丢弃**，不整份失败。
3. **没有 `links` 就不是链接**：徽章仍渲染，但只是内容，不做假目标
   （`SFAdminExtensionUpdateBadges.vue` 在无 `pageId` 时渲染成 `<UBadge>`）。
4. **判断留在商城**：是否可更新、目标版本、文案与 tone 都由插件算；Core 只提供
   渲染位与链接安全边界，不新增目录、表、API，也不认识商城。
5. **行身份抽组件**：三页共用的行身份抽成 `SFAdminExtensionIdentity`（图标、
   名字、状态、徽章、`badges`/`details`/`footer` slot）；overview 的选中按钮与
   链接徽章分离——`<a>` 不能嵌在 `<button>` 里，否则既非法也会吞掉键盘操作。
   `index.vue` 同时把 Version GC 状态机抽到 `useAdminExtensionVersionGc.ts`，
   页面从 990 行降到 900 行，过 1000 行架构闸门。

## 不做

- 不新增 Provider Slot、商城专用 Core 接口、Core 侧更新检查任务或缓存表。
- 不在 Core 放「商城地址」配置：商城 URL 仍由插件自己的设置页持有。
- 不做服务端渲染期预取：surface 调用仍是页面加载后的一次 bridge 调用。
- 不在 Core 校验「可更新」是否属实：徽章语义由插件拥有。

## 后果

- **additive、无契约版本提升**：`output` 在
  `contracts/openapi/schemas/extension-admin-surfaces.yaml` 里是
  `additionalProperties: true`，`links` 只是新增可选键；surface 的
  `contractVersion`、placement 契约和清单 schema 都不用动。
- **旧 Core 惰性**：未升级的宿主忽略 `links`，徽章只是不可点，不会因此报错；
  插件不需要按 Core 版本分支。
- **链接安全**：扩展不能用徽章把管理员带去站外——`//`、绝对 URL、反斜杠、控制
  字符都在宿主边界被丢弃，且丢弃粒度是单条，不会连带整份 surface 失败。
- **audit 成本**：每次页面加载对每个 `list_column` surface 调用一次；失败只降级
  为「扩展更新检查未完成」警告 + 重试按钮，不影响列表本身渲染。
- **`links` 不参与行可见性**：`visibleResourceIds` 仍由 surface 决定，链接只是
  给可见行加目标。

## 重开条件

- 需要跨页聚合或按更新状态排序时，`list_column` 不够，要重新设计查询面。
- 需要把更新状态暴露给非管理端（公开页面、通知、AI 工具）时，走数据面申请
  流程，不复用这个渲染面。

## 证据

- 契约与归一化：`apps/web/app/utils/admin/adminSurfaces.ts`（`AdminListBadge`、
  `normalizeResourceLinks`、`resolveAdminListBadges`）、
  `apps/web/app/composables/admin/useAdminListSurfaces.ts`（`badgesFor`、
  `hasListColumns`、`failures`）。
- 页面：`apps/web/app/pages/admin/extensions/index.vue`、`plugins.vue`、
  `themes.vue`；组件：
  `apps/web/app/components/admin/extensions/SFAdminExtensionIdentity.vue`、
  `SFAdminExtensionUpdateBadges.vue`。
- 单测：`apps/web/tests/admin/adminSurfaces.test.ts`（链接白名单与拒绝项、空
  `links` 不构成可渲染结果、徽章只认 `add` + invokable 列）、
  `apps/web/tests/admin/adminExtensionUpdateBadges.test.ts`（三页接线、
  `<a>` 不在 `<button>` 内、`refreshAll` 先列表后 surface）。
- 门禁与回归（Core 批）：`node scripts/v3-catalog/generate.mjs --check` →
  `V3 P0 catalogs are current (18 files)`；`bun test` 1766 pass；typecheck 绿；
  架构闸门 1963 files 通过。
- 身份行：本批为两个新组件补 2 条 reviewed UI 身份行以解锁
  `scripts/ci/contracts-gate.sh:70`。同批另有 6+1 条 identity 批次遗留的既有
  缺行一并补齐，属门禁修复，不是本功能的一部分。
- 生成目录说明：`docs/extensions/v3/catalogs/admin-surfaces.md` 里
  `core.admin.page.users` 的 columns/rowActions/bulkActions/editorPanels 由
  yes 变 no，是 identity 批次把 surface 用法搬进子组件后的既有 marker 漂移
  （生成器只扫 `apps/web/app/pages/admin/*.vue`）；本批按生成器结果重写，不代表
  users 页的 surface 清单仍然完整，语义修正在 identity 批次跟进。
- 浏览器验证与残余风险见
  `../sessions/2026-10-27-extension-update-badges-review.md`（T5）。
