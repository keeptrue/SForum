# 2026-10-28 Content Reactions (likes) Handoff

## Changed

- 新迁移 `202610280001_content_reactions.sql`（OnlineSafe）：`content_reactions`
  表、`topics.like_count` / `comments.like_count`（`CHECK NOT VALID` +
  `VALIDATE`）、稳定视图 `sforum_core_v1.forum_reactions`、通知类型 `reaction`
  的描述符与策略（in_app 开，email/web_push 关）。**已在 dev 库应用**
  （goose `202610280001`，`content_reactions` 与视图均存在）。
- 领域层：`Forum/ReactionService`（聚焦协作者，不增长 `Service` 的 77 个接收者
  方法）、`ReactionStore` 窄接口、`PostgresStore` 实现、`CachedStore` 转发 +
  提交后失效。
- 热门分：`TopicHotScoreSQL` 成为唯一公式入口，七处写点全部改用它；Go 侧镜像
  `ComputeHotScore` 已删除（无调用点）。只有主题点赞进入 `hot_score`。
- 读模型：`TopicSummary.likeCount`、`TopicDetail.likedByMe`、`Comment.likeCount`
  / `likedByMe`；`PublicReadService` 在缓存之后按请求装饰（递归子评论，最多两次
  批量查询）；登录详情响应带 `private, no-store` + `Vary: Cookie`。
- HTTP：4 个切换端点 + 2 个名单端点；`forum.reactions_disabled` → 403，
  `forum.reaction_target_invalid` → 404；写端点复用全局写限流（默认 30 次/分/IP）
  与 CSRF。
- 通知：`TypeReaction`（category `engagement`）、`Outbox.NotifyReactionTx`、
  `forum.reaction` 邮件模板、bootstrap 适配器；自赞不投影，去重键
  `reaction:{type}:{id}:{actor}`。
- 功能开关：`features.reactions`（public，默认开），后台开关页由目录驱动，无需
  新页面；关闭时 6 条路由 403，前端 `v-if` 隐藏控件。
- 前端：`SFReactionControl`（按钮 + 计数 + 头像堆叠名单，乐观切换/失败回滚/匿名
  跳登录）、`SFTopicReactionBar`（从 `SFTopicShowPage` 抽出的动作行 + 点赞）、
  评论行挂载、首页/分类/标签列表行赞数、`useWebOptions().reactionsEnabled`；
  新增 `useTopicReaction.ts` 把详情页的 `@change` 回写计数。
- 评论点赞控件位置调整：从 `SFComment.vue` 头部 `.sf-comment__floor-actions` 移到
  footer 动作行 `.sf-comment__actions` 首位；动作行 `v-if` 改为
  `actions.length || (comment && reactionsEnabled)`。`sforum-comment.css` 对齐
  字号（`:411-420`、`:657-660`），`.72` 透明度下沉给文字按钮、点赞计数保持不透明
  （`:793-805`）。契约测试 328 pass。
- 契约：OpenAPI 4 个 path item + 4 个 schema；HostAPI 新增
  `sforum.core.public_reaction` JSON schema；路由目录 380 → 386；两个新组件进入
  UI 身份账本（owners: public）；架构基线三处下调（`postgres_store_ops.go`
  1438、`SFTopicShowPage.vue` 1127、`service_ops.go` 1170）。
- 类型腾位：`Forum/types.go` 的评论类型抽到 `types_comments.go`；
  `topicSummarySQL` 与 `topicListOrderBy` 抽到 `topic_summary_sql.go`；
  评论回复读取从 `service_ops.go` 抽到 `service_comment_replies.go`（52 行）。

## Fixed

实现与审查中修掉的真实缺陷：

- HostAPI 缺 `sforum.core.public_reaction` JSON schema，catalog 构建直接失败。
- 评论 404 存在性预言机：`reactions_service.go` 与 `service_comment_replies.go`
  统一归一为 `ErrCommentNotFound`，跨主题/已删评论不再泄漏差异。
- 新增错误码 `forum.reaction_target_invalid`（404），替代笼统的 500/404 混淆。
- 登录详情补 `private, no-store` + `Vary: Cookie`，避免 per-viewer
  `likedByMe` 被共享缓存。
- 点赞不再写 `updated_at`（点赞不是内容编辑）。
- 通知 category 从 `plugin_unknown` 修正为 `engagement`。
- 主题计数改用 `TargetID` 同源，避免评论 id 与主题 id 混用。
- 删除死面 `GetReactionState` 与无调用点的 `ComputeHotScore` 镜像。
- 前端 7+1 处：开关关闭只 disabled 不隐藏 → `v-if`；错误 Toast 自动关闭 →
  `duration: 0`；补成功 Toast；i18n `topicDetail.reaction.*` → `forum.reaction.*`；
  回跳/回滚走纯函数；列表行双 `ml-auto`；`SFTopicShowPage` 未接 `@change` →
  新 `useTopicReaction.ts` 回写。
- QA 发现并修复：名单弹层永不加载——`SFReactionControl.vue` 的 `UPopover` 绑
  `@open`，而 Reka 只发 `update:open`；改为
  `@update:open="onRosterOpenChange"`，新增
  `apps/web/tests/forum/forumReactionRoster.test.ts` 3 条（打开即请求并渲染名字、
  关闭不重复请求、comment 目标走 comment 名单）。

## Verification

- 后端一次性 PG 集群 5 包全绿：
  `database/migrations/content_reactions_test.go`、
  `database/migrations/stable_core_views_test.go`（`forum_reactions` 谓词逐字镜像 +
  `security_barrier`/`security_invoker` + 无 PII）、
  `Support/HostAPI/query_registry_core_reactions_test.go` +
  `Support/HostAPI/v2_query_postgres_reactions_integration_test.go`、
  `Models/Forum/reactions_postgres_integration_test.go`（并发 10→1 行、
  `like_count == COUNT(*)`、`hot_score` 三组、CASCADE、点赞不改 `updated_at`、
  自赞不投影）、`Models/Notifications/reaction_projection_postgres_integration_test.go`
  （取消再赞只 1 条）、`Http/Controllers/Forum/reactions_http_test.go` +
  `reaction_cache_scope_test.go`。
- `bun test` 1837 pass / 0 fail；`bun run typecheck` 0 错。
- 本次复跑：`go build ./...`、`node tests/validate-architecture-boundaries.mjs`
  （1992 文件 / 206 个超 500 行）、`ruby scripts/validate-openapi-refs.rb`
  （2968 refs OK）、`node scripts/v3-catalog/generate.mjs --check`（current）、
  `go run ./cmd/migrate --check-no-pending`（schema-up-to-date）全绿。
- 浏览器功能 QA（`storage/qa/reactions-qa-report.md`，2026-10-29，1633×810 真实会话）：
  - 匿名点赞 → `/login?redirect=/t/1/topic-ngkxas`，登录后回跳成功。
  - 登录 `POST`/`DELETE` 200；硬刷新后 `aria-pressed=true`、计数 1；取消后计数 0、
    DB `like_count=0` 且 reaction 行 0。
  - 1 秒内连点 3 次只发 1 个 POST，DB 1 行。
  - flat 评论点赞 200；嵌套子评论独立控件、计数 0 / `likedByMe=false`。
  - 名单弹层修复后显示头像与 `/u/seed_44888364289b` 链接。
  - 首页列表行 heart +「1」（`aria-label="1 人点赞"`）；0 赞行无 heart 节点。
  - 通知 id 212（recipient 75、actor 3、category `engagement`、dedupe
    `reaction:topic:37:3`）；取消再赞仍 1 条（同 id、`created_at` 不变）。
  - 3001 `/t/1` 的 `data-provider="sforum.default-theme"`、`data-template="1"`、
    fallback 0 次；skin digest
    `2f06a9e48565a3d826fb7cf467de6919b2c5617457a053f19fa7c57cf0782442`、
    nodeRevision 6，与基线一致。
- 视觉/响应式审计（`storage/qa/2026-10-29-visual-responsive-audit.md`）：
  390 无横向溢出、英文文案不裁切、aria 语义合规。
- 开关关闭端到端（`storage/qa/reactions-flag-evidence.txt`，2026-10-29）：
  6 端点 403 `forum.reactions_disabled`（含 2 个名单读），`aria-pressed` 10→0→10，
  `like_count` 零漂移（topic1 / topic4 / comment9 均 0），UPDATE 后约 4 秒生效，
  已还原 enabled。
- 最终门禁（task-11，小审二次复跑）：9 项 PASS；`./scripts/test.sh --fast` 三线
  真 exit 0；快照 01:33；结论「可提交」。
- 方法纠正：早先「3001 未水合」是误判——`_vei` 是 Symbol 键，需用
  `Object.getOwnPropertySymbols` 检查；3001 与 3000 行为一致，浏览器交互可用。
- 告警排除：匿名跳转 `/` 未复现——隔离上下文逐场景验证均正确带 redirect；仅无
  redirect 直接访问 `/login` 才回首页，属设计，不是缺陷。
- 环境注记：3000 用户 dev server SSR worker OOM 未重启；功能 QA 主体在 3000
  （OOM 前采集）与 3001 隔离 QA server 完成（`NUXT_BUILD_DIR=.nuxt-qa`，
  `--dotenv ../../.env`）。

## Residuals (open)

1. **产品缺口：默认主题只有 flat 评论视图。** `SFTopicShowPage.vue:148` 硬编码
   `ref<'flat'>('flat')`、不读 query；`SFCommentStreamControls.vue` 支持 tree/flat
   但全仓无引用。tree 由服务端 `ListComments(tree)` 覆盖；本切片不加视图切换 UI。
2. **设计审计待修四项**（小前正在修）：实心心形（A2）、点赞热区补到 38–40px
   （A1）、名单弹层 `max-width` + 长用户名截断（C2）、列表计数对比度
   `--sf-public-text-muted` → secondary（D1）。
3. **不修三项，归全局 token 债务**：评论动作 2.59:1（B1）、默认焦点环未 token 化
   （E1）、暗色点赞激活色 3.51:1（E2）。
4. 通知浏览器 UI 未单独断言（API + DB 证据充分）；3000 用户 dev server SSR OOM
   未重启。邮件真实投递未验证（模板已加）。
5. 取舍清单见 `../decisions/2026-10-28-content-reactions.md` 的 Known tradeoffs：
   未验证邮箱可点赞、视图无 `topic_id`、名单读无限速、TOCTOU、硬删用户后
   `like_count` 偏高（修复 SQL 在 `modules/forum.md`）、`roster.Total` 只算
   active 用户、`forum_topics` 与 `public_categories` 的 category group 可见性
   口径差异（open question）。

## Next

1. 提交卫生：`scripts/brand/spec.json` 里的 `sforum-related-discussions` 条目与本
   切片无关，属他人未提交改动；提交时单独拆开，不要混进点赞切片。
2. 第二刀：收藏（个人主页 self-only tab）。
