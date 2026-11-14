# 2026-11-14 话题页原生 confirm → 站内 Modal

## Changed

公共话题详情页 `apps/web/app/components/forum/SFTopicShowPage.vue` 的 **4 处浏览器原生
`window.confirm` 全部换成站内 Nuxt UI Modal**：评论删除、话题删除、话题扩展动作、评论扩展动作。
纯前端改动 —— 后端、OpenAPI、管理员页、编辑器/导航编辑器的原生 confirm 一律未动。

- `app/components/forum/SFConfirmDialog.vue`（新，通用确认弹窗）：`UModal`，title/description 走
  props、按钮放 `#footer`；props `request: ConfirmDialogRequest`，emits `settle: [boolean]`。
  底部顺序固定「取消（`color="neutral"` `variant="ghost"`）在前、确认（`color="error"`
  `variant="solid"`）在后」；组件**不给任何元素设置自动聚焦**，破坏性按钮因此不会成为默认焦点。
  `@update:open` 收到 false 时 emit `settle(false)`，所以 ESC / 点遮罩 / 右上角关闭统一按取消结算。
- `app/composables/forum/useConfirmDialog.ts`（新，Promise 语义状态机）：`confirm(options):
  Promise<boolean>`、`resolveConfirm(value)`、`request`（reactive 的
  `{ open, title, description, confirmLabel, cancelLabel }`）。同一时刻只允许一个待结算请求，
  连续两次 `confirm` 时**前一个按 false 结算**，不让旧 Promise 悬空；结算后 `request.open` 归 false。
  纯逻辑、不依赖 i18n，文案由调用方给。
- `app/components/forum/SFTopicShowPage.vue`：两个新文件**显式 import**（嵌套目录组件不能靠
  自动导入改名），模板只挂一次 `<SFConfirmDialog :request="confirmRequest" @settle="resolveConfirm" />`，
  4 处 `window.confirm` 改为 `await confirm({...})`。删除流程其余逻辑（`deletingCommentId` 守卫、
  软删、刷新列表、Toast、失败报错）原样保留。
- `app/utils/forum/forumCommentTree.ts`（新）：承接纯函数 `mergeCommentReplies`（全仓库仅该页面用）。
- `app/utils/forum/forumTopicPresentation.ts`：承接三个扩展动作 helper
  （`topicExtensionActionKey` / `commentExtensionActionKey` / `topicExtensionActionLabel`）；
  标签签名改为显式传 locale（`topicExtensionActionLabel(action, locale.value)`），工具层不依赖运行时。
- `i18n/locales/{zh-CN,en-US}.json`：新增 `topicDetail.confirmDialog.commentDeleteTitle` /
  `topicDeleteTitle` / `extensionActionTitle` 三键，两侧成对；正文与按钮复用既有键
  （`topicDetail.confirmCommentDelete` / `confirmDelete` / `confirmExtensionAction` / `cancel` /
  `delete`），未新增无消费者的死键。
- `tests/architecture-boundaries-baseline.json`：`legacyLargeFiles` 中
  `apps/web/app/components/forum/SFTopicShowPage.vue` 由 1127 下调为 **1124**（与实际行数严格相等）。
- 测试：新增 `apps/web/tests/forum/topicConfirmDialog.test.ts`（13 用例，覆盖 composable 的
  确认/取消/连续请求/结算后 open 归 false、组件的标题正文渲染与 settle 载荷、页面契约
  「源码不再出现 `window.confirm(` 且仍引用 `topicDetail.confirmCommentDelete`」）。
- 模块笔记：`modules/forum.md` 新增「Topic Confirm Dialog（公共话题页确认弹窗）— 2026-11-14」。

## Decisions

- **用站内 Modal 取代原生 `window.confirm`**：原生对话框脱离站点主题与 i18n，且无法保证
  破坏性按钮的焦点顺序；站内弹窗可固定「取消在前、删除在后」并把关闭路径统一成取消结算。
- **确认语义做成 Promise，而不是把 open 状态散在页面里**：`await confirm({...})` 让「确认后才
  发请求」写进控制流，连续请求时前一个按 false 结算，杜绝悬空 Promise。
- **ESC / 点遮罩 / 关闭按钮统一按取消结算**：三者在用户意图上等价于「不确认」，因此不各自
  分叉成不同结果；组件不设任何自动聚焦，初始焦点留在关闭按钮，破坏性按钮不做默认焦点。
- **先挪 helper、再下调基线**：`SFTopicShowPage.vue` 是 baseline 棘轮对象，行数只降不升。
  本次为让出净空，把 `mergeCommentReplies` 与三个扩展动作 helper 搬进
  `app/utils/forum/` 下的两个工具文件，页面由 1127 行降到 1124 行，基线同步下调 —— 变小的
  文件必须回收容量，不留旧值。
- **纯前端收口**：不碰后端、OpenAPI、管理员页与编辑器/导航编辑器的 confirm；颜色全部走
  Nuxt UI 的 color/variant token，不写死 hex，不用 emoji 图标。

## Next

- 门禁四项全绿：`cd apps/web && bun test` **3070 pass / 0 fail**（全量，改了公共页 island
  不能只跑切片）、`bun run typecheck` exit 0、`node tests/validate-architecture-boundaries.mjs`
  passed、`bun run build` exit 0。
- 浏览器实测（127.0.0.1:3000 话题页，未改动任何数据）：站内 Modal 出现、浏览器原生对话框
  未触发（`document.querySelector('dialog[open]')` 为 null）、取消/ESC/X/点遮罩四条关闭路径
  均生效且数据未变（评论数恒为 8）、初始焦点落在关闭按钮。截图
  `/home/inkedus/Downloads/topic-confirm-modal.png`。
- 实现与审查均由独立 agent 复核通过（task-1 实现、task-2 审查 PASS，报告落盘
  `work-team/review/task-1-rmwg69jri-14/report.md`）。
- 未提交。提交时注意 `work-team/` 与 `.dsh/` 都不在 `.gitignore` 里，**不要 `git add -A`**。
- 可选的下一步（未做）：把同一套 `SFConfirmDialog` 复用到其余仍用原生 confirm 的页面
  （编辑器离开确认、导航编辑器、贡献治理、工具栏动作）——本轮按范围约束未动。

## Open Questions

- Nuxt UI 自身文案（如弹窗关闭按钮的 aria-label `Close`）未随站点 locale 中文化，是全站既有
  情况，本次未处理。
- 新的两个工具文件（`forumCommentTree.ts` / `forumTopicPresentation.ts`）暂无直接用例，目前由
  页面契约测试间接覆盖。
- 测试里有一条断言「组件源码不含字面量 `autofocus`」，偏脆：将来若在注释里提到该词会误报
  （本轮已因组件注释措辞触发过一次误报）。
- 弹窗打开期间，评论行的「删除」按钮仍显示（未置灰），用户可重复点开确认流；当前靠
  `deletingCommentId` 守卫兜底，未做交互层屏蔽。
