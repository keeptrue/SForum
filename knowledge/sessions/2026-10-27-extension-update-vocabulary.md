# 2026-10-27 Session Handoff — 扩展管理「更新」三态词汇

## Changed

- 三页（`/extensions`、`/extensions/plugins`、`/extensions/themes`）不再渲染原始
  `stagedVersion` 徽章，改为每行派生一个更新状态：`可升级 v{staged}`（semver 更高）/
  `重启后生效`（同版本插件内容更新）/ `激活后生效`（同版本主题内容更新）/ 无。
  派生在 `app/utils/admin/extensions/updateState.ts`（semver 2.0.0 优先级含 prerelease，
  非法输入 fail-closed），动作与徽章映射在 `updatePresentation.ts`。
- 新增组件 `SFAdminExtensionPrimaryAction.vue`（上下文主按钮：立即升级 / 升级并启用 /
  重启 / 重启并启用 / 激活 / 重新激活 / 恢复默认 / 启用 / 管理）与
  `SFAdminExtensionUpdateState.vue`；新增 `useExtensionRowMenu.ts`（行尾 `⋯` 完整动作清单）。
- 行结构收敛为两个控件；徽章重排为 状态 → 运行态/制品缺失 → 核心更新 → 商城链接 →
  类型/辅助。商城 `list_column` 徽章降为次级文字链接（`↗`，`{label} · 来自 {sourceExtensionId}`），
  渲染在行选择按钮之外；核心更新徽章是纯文本，可在按钮内 —— 不存在 `<a>` 嵌 `<button>`。
- `AdminListBadge` 新增 `sourceExtensionId`；工具栏新增 `只看有更新`（纯视图开关，不持久化）
  与 `{m} 个内置更新待重启`；`全部升级（N）` 只收 `kind=version` 且 `canUpgradePlugin` 的插件。
- i18n：`admin.extensions.update.*` 34 键 + `trust.{blockingErrorApply,titleApply,bodyApply,confirmApply}`；
  删除 `stagedVersion` / `stagedVersionBadge` / `stagedVersionHint` / `upgradeUnavailable` /
  `admin.extensions.upgrade` / `admin.extensions.detailTitle` / `plugins.autoUpdate.incompleteHint`。
  启用弹层新增 `apply` 用途（legacy 分支用 `update.restartTitle/Body/Confirm`）。
- catalog 生成物（`core_catalog_gen.go`、`catalog-identities.json`、`ui-surfaces.{json,md}`）
  登记两个新组件；架构 baseline `useAdminExtensionsManager.ts` 921→920；中英文 usage 文档同步。

## Decisions

- **`⋯` 永远渲染，至少含「管理」**（Lead 2026-10-27 覆盖原规格「只剩重复项就不渲染触发器」）：
  主按钮会随更新状态换动作（当前主题带内容更新时变「重新激活」），抑制触发器会让该行
  彻底失去管理入口。见 `../decisions/2026-10-27-extension-update-vocabulary.md` 决策 3 与「已拍板」5。
- 「内置更新待重启」只数 `source=builtin` 的**插件**内容更新；内置主题走激活，不计入。
- 制品不可用（`artifactState=missing`）既不出更新徽章，也不计入筛选与批量。
- 筛掉当前选中行时清空选中、回到详情空态；不自动改选下一行，关闭筛选也不恢复选中。

## Verification

- 门禁（T4 终态树）：`bun test` **1777 pass / 0 fail / 10712 expect / 209 files**；
  `bun run typecheck` exit 0；`node tests/validate-architecture-boundaries.mjs` passed
  （1968 文件）；`node tests/validate-staged-extension-contracts.js` passed；
  `node scripts/v3-catalog/generate.mjs --check` current（18 文件）；
  `cd apps/api && go build ./...` exit 0（`apps/api` 仅生成物 2 行）。
- 审查：T2 里发现 P2×2（内置计数含主题、主题行丢管理入口）+ P3×4（死 i18n×3、死导出、
  文档口径、legacy 重启弹层文案），frontend 全部修复后逐条复读复核通过；无 P0/P1/P2 遗留。
- 浏览器 QA（lead，2026-10-27 06:20 前后，共享浏览器已登录 super_admin，1633×810 + 390×844）：
  - 总览：**22 行**（API 列表排除 7 条 missing-artifact tombstone + 1 条 builtin removal，
    30→22；`sforum.marketplace` 仍在列表里显示「制品缺失」），`⋯` 22/22；
    徽章 可升级 ×1（Web Push v1.0.7）/ 重启后生效 ×7 / 激活后生效 ×1；
    工具栏 `已安装 22 个扩展` / `6 个内置更新待重启` / `全部升级（1）` / `清理缺失制品（1）`；
    筛选 关 22 → 开 **9**，开关 aria「只看有更新 · 9 个扩展有更新」；全页无「待升级」；
    详情侧栏 `sforum.smtp`：当前版本 v1.1.7 / 新版本（已下载，尚未生效）v1.1.7 /
    内容已更新 / 包摘要 `60c5f2b3`。
  - 插件：**20 行**、`⋯` 20/20、筛选 **8**、商城徽章 0 条。
  - 主题：**2 行**、`⋯` 2/2（P2-2 修复实测：当前主题行打开后仅「管理」，入口恢复）、
    筛选 **1**、本页无「内置更新待重启」。
  - a11y：`button[aria-pressed] a` = 0；核心更新徽章为纯文本 span，title 正确
    （「已下载 v1.0.7，当前运行 v1.0.6；升级后生效。」）；`⋯` 指针点击与键盘 Enter 均可打开
    `role=menu`，项为 管理/禁用/重启/生命周期记录（builtin 无卸载），Esc 可关。
  - 390×844：三页 `documentElement.scrollWidth = 390`，无溢出元素，主按钮文案保留、无裁切。
- **未覆盖**：本轮商城 `list_column` 返回 0 条，`+商城` 的筛选命中与商城链接渲染没有真实数据证据。

## Next

- 商城徽章路径待真实数据验证（筛选命中 = 核心 + 商城；链接在行选择按钮外；title 为
  `{label} · 来自 {sourceExtensionId}`）。
- `⋯` 触发器有 `aria-haspopup=menu` 但缺 `aria-expanded`（Nuxt UI 未回填），记为残余 a11y 缺口。
- QA 截图未落盘到仓库；若发布需要浏览器证据，需按稳定路径重拍。
- 决策文档里的数据表（30 行 / 12 staged）是设计时快照，dev watcher 已 promote 4 个上传插件，
  当前为 22 行可见 / 11 行 staged；引用时以库为准。

## Open Questions

- 「全部升级」是否要覆盖同版本内容更新（当前按决策不做）。
- staged 版本更低时是否要在行内留一个可见痕迹（当前静默，只能走生命周期记录/回滚）。
