# 2026-11-10 Session Handoff — 收藏（Bookmarks）

## Changed

收藏从零到可用，范围比 Iteration A 原草案更大（运营者要求：主题 + 评论、每条
可公开/私密、只统计公开计数、到期提醒、公开 profile tab）。

- 迁移 `apps/api/database/migrations/202611100001_content_bookmarks.sql`：
  `content_bookmarks`（`UNIQUE(user_id, target_type, target_id)`、
  `visibility private|public` 默认 private、`remind_at`/`reminded_at`）、三个索引
  （公开名单 / 本人 keyset / 到期扫描部分索引）、`topics.public_bookmark_count` +
  `comments.public_bookmark_count`（非负 CHECK 走 NOT VALID + VALIDATE）、稳定视图
  `sforum_core_v1.forum_bookmarks`（谓词逐字镜像 `forum_reactions`，只出公开行，
  不出提醒列）、通知类型 `bookmark_reminder` 与 policy revision bump。
- Forum 域：`bookmarks.go` / `bookmark_store.go` / `bookmarks_service.go` /
  `postgres_store_bookmarks.go` / `cached_store_bookmarks.go` /
  `bookmarks_reminders.go`。`BookmarkService` 是独立聚焦类型，未给 `Service`
  增加接收者方法。
- 路由 6 条：`PUT|DELETE /topics/{id}/bookmark`、`PUT|DELETE /comments/{id}/bookmark`、
  `GET /profile/bookmarks`、`GET /profiles/{username}/bookmarks`。`PUT` 是全量状态
  写入（`visibility` / `remindAt` / `clearReminder`），`DELETE` 不读 body。
- 读模型：`bookmarkedByMe` 在 `PublicReadService` 缓存之后装饰、永不进缓存、整页
  一次批量查询。
- Profile：最小接口 `BookmarkLister` + `Providers/forum_bookmarks.go` 适配器；
  Forum 不认识 Profile，Profile 不写收藏 SQL。
- 提醒：`forum.bookmark_reminders`（60s，maintenance 队列，`UPDATE ... WHERE
  reminded_at IS NULL RETURNING` 抢占），通知类型 `bookmark_reminder` 站内 + 邮件
  默认开，模板 `forum.bookmark_reminder`。
- 事件 `bookmark.created` / `bookmark.removed`；HostQuery
  `core.query.public_bookmarks.list`；开关 `features.bookmarks`。
- 前端：`SFBookmarkControl`（实心/空心 + 可见性菜单 + 提醒面板）、
  `useBookmark`、`forumBookmark`（纯函数 + 边界校验）、`SFProfileBookmarkTab`、
  `useProfileBookmarks`；资料页新增第 4 个内置 tab「收藏」；`SFProfileFeedTab` 与
  `useProfileActivityFeeds` 是从 722 行的 `SFProfileShowPage` 里抽出来的（现 500 行）。

## Decisions

`decisions/2026-11-10-content-bookmarks.md`。要点：公开/私密按条、默认私密；只统计
公开计数；提醒三态（`clearReminder` 显式清除，`remindAt: null` 不能表达清除）；
提醒窗口 -60s ~ +730d、单用户未到期上限 200（软上限，不做串行化）；提醒列
owner-only 三层闸（SQL CASE + store 兜底 + 适配器）；不进 `hot_score`；不新增 RBAC
权限键；收藏放 `Forum` 内聚焦文件而不是新包（避免与 Forum 的装饰需求形成 import 环）。

## Evidence

- 三轮独立审查：首轮 8 项返工（含「提醒清不掉」功能缺陷）、二轮 1 项阻断（他人视角
  泄露行主提醒时间）、终审 10 项全 PASS 判可交付。
- 实机：迁移 up/down 双向可逆；`forum.bookmark_reminders` 每 60s completed；
  `features.bookmarks` 关闭时 6 条路由全 403、开启全 200
  （`storage/qa/bookmarks-flag-evidence.txt`）；提醒链路站内 + Mailpit 实收
  （`storage/qa/bookmarks-reminder-evidence.txt`）；计数对账 0 差异
  （`storage/qa/bookmarks-reconcile-evidence.txt`）。
- 浏览器 QA：桌面 + 390x844，控件与资料页 tab 均 `overflow=false`、`clipped=0`、
  `consoleErrors=0`，提醒面板在视口内。
- 全量门禁 `./scripts/test.sh --fast` 三线 PASS（api / web / static）；
  `bun test` 1944 pass / 0 fail；`go build`、`validate-architecture-boundaries`、
  `validate-openapi-refs`、`validate-v3-p0-catalogs`（392 routes / 356 UI surfaces）
  全绿。

## Gate lessons（下次别再踩）

只跑目标包会漏掉四类集成点，全部是本次全量门禁才暴露的：

1. **V3 路由目录 guard 分类**：新写路由落进 `service-policy` 兜底分类时
   `scripts/v3-catalog/generate.mjs` 直接抛错，必须先补 `reviewedGuardPolicies`
   条目（收藏用独立的 `active actor bookmark policy; visibility rechecked by service`
   → `kind: login`，不要复用点赞那条字符串）。分类规则的位置很关键：必须排在
   `/^\/api\/v1\/comments\// && method !== 'GET'` 那条 permission 兜底之前，否则
   评论收藏会被误吞成 `post edit/delete` 权限。
2. **V3 身份账本与生成物**：新增路由要进 `docs/extensions/v3/catalog-identities.json`，
   新增 Core 组件要进 UI 身份（本次 3 条：`sfbookmark_control`、
   `sfprofile_bookmark_tab`、`sfprofile_feed_tab`，owners 显式 `['public']`），
   然后重新生成 18 个 catalog 文件与两份 `core_catalog_gen.go`。账本是 append-only：
   旧 ID 只能 retired，不能复用。
3. **文档生成物**：新增事件（`bookmark.created`/`removed`）与周期任务
   （`forum.bookmark_reminders`）会让 `docs/extensions/catalogs/{events,hooks,schedules}.md`
   过期，`cmd/sforum` 的 `TestExtensionDocsGenerateCheck` 会红——跑
   `cd apps/api && go run ./cmd/sforum extension docs generate`。
4. **手写目录计数**：`Support/Routes/registry_test.go` 的路由计数（386→392）与
   `Models/Jobs/service_test.go` 的核心调度计数（8→9）是手写账本，生成器不会替你改；
   后者还断言具体 schedule 常量列表，不能只改数字。
5. **错误码本地化**：新增具名错误码必须同时在 `Support/Localization/messages.go`
   补 zh-CN + en-US，否则 `TestAPIErrorCodesHaveLocalizedMessages` 会列出全部缺失码。

结论：新增一条核心写路由的真实成本 = 迁移 + 域 + 控制器 + 契约 + 稳定视图/HostQuery
+ 事件 + 开关 + **V3 身份与 guard 分类 + 生成物 + 文档生成 + 目录计数 + 错误码本地化**。
最后两项经常被当成「顺手」而漏掉。


## Next

- 收藏计数 `publicBookmarkCount` 已进 composable 但界面暂不展示；要展示需定位置。
- `BookmarkCommentRef` 只有 id/topicId/authorUserId/status/createdAt，没有标题与
  摘要，评论行按 `#id` 呈现并走 `/t/{topicId}#comment-{id}`；若补 excerpt/topicSlug，
  只需改 `SFProfileBookmarkTab` 的 `entryTitle`/`entryLink`。
- 未做真实登录态的收藏写入 E2E（前端侧）；运维侧已用真实会话跑通 6 条路由。
- dev 库留有测试收藏行 id=36..39（user 2/3），清理按 id 删。

## Open Questions

- 关注（follows）是否接着做：它是私信门控的自然前置（Discourse 用信任等级门控
  私信，SForum 没有 TL，互关是替代方案）。
- 徽章/等级：运营者已决定「恢复徽章 + 等级」，但等级归入徽章（不建独立体系、不碰
  RBAC）。落地前必须新写决策记录 supersede `2026-10-12-remove-gamification-badges-growth.md`，
  且原 7 个 UI 身份是永久 tombstone，重建必须用新 ID + 新 contractVersion。
