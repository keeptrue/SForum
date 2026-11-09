# SForum 互动第一刀：点赞（主题 + 评论）实施计划

Status: **landed（可提交，2026-10-29）** — 迁移已在 dev 库应用；后端 5 包一次性
PG 集成与前端 1837 用例全绿；浏览器 QA 通过（flat）；开关关闭证据已采集；最终门禁
9 项 PASS + `./scripts/test.sh --fast` 三线 exit 0
Date: 2026-10-28
Last status pass: 2026-10-29
Parent: `2026-07-12-iteration-a-engagement-loop.md`（Workstream 2）
Decision: `../decisions/2026-10-28-content-reactions.md`
Handoff: `../sessions/2026-10-28-content-reactions.md`

## 执行进度（2026-10-29）

| 里程碑 | 状态 | 说明 |
| --- | --- | --- |
| M0 迁移与契约骨架 | **landed（dev 已应用）** | 迁移文件、稳定视图、迁移结构测试、视图谓词断言均已交付；dev 库已执行到 goose `202610280001` |
| M1 领域写路径 | **landed** | `ReactionService` / `ReactionStore` / 热门公式收敛；单测 + 一次性 PG 集群 store 集成（并发 10→1 行、`like_count == COUNT(*)`、CASCADE、点赞不改 `updated_at`） |
| M2 读模型与 HTTP | **landed** | 6 条路由、装饰、OpenAPI、功能开关；HTTP 403/404 与缓存作用域测试（`private, no-store` + `Vary: Cookie`） |
| M3 通知 | **landed（集成验证）** | 类型 / fanout / 邮件模板 / 前端呈现；PG 集成覆盖自赞不投影、取消再赞只 1 条 |
| M4 前端 | **landed（QA 通过，tree 口径降级）** | 控件、动作行抽取、评论挂载、列表计数、`useTopicReaction` 回写；1837 web 用例 + typecheck 全绿；浏览器 QA 通过（flat）；tree 不可达，记录为产品缺口 |
| M5 扩展数据面 | **landed** | 稳定视图 + `sforum.core.public_reactions.list` + `sforum.core.public_reaction` JSON schema；允许/拒绝测试与 PG 集成已补 |
| M6 目录与基线 | **landed** | 路由 380→386、两个 UI 身份、三处基线下调（`postgres_store_ops.go` 1438、`SFTopicShowPage.vue` 1127、`service_ops.go` 1170）、知识库 |
| M7 验证 | **landed** | 最终门禁 9 项 PASS（后端 5 包 PG 集成、web 1837、typecheck、架构/目录/OpenAPI、开关端到端等）+ `./scripts/test.sh --fast` 三线真 exit 0；浏览器 QA 见 `storage/qa/reactions-qa-report.md` 与 `storage/qa/2026-10-29-visual-responsive-audit.md`；开关证据见 `storage/qa/reactions-flag-evidence.txt` |

---

## 0. 目标与验收口径

**目标**：登录成员可以给主题和评论点赞/取消，计数进入公开读模型与热门排序，作者收到站内通知，运营者可整体关闭；不改主题制品、不新增公共页面。

**完成即验收**（每条都要有可复现证据）：

1. 登录用户点赞 → 计数即时 +1（乐观更新 + 服务端真值），刷新后保持；再点取消回到原值。
2. 连点/双击不重复计数；同一用户对同一目标最多一条 `like`。
3. 评论点赞：**flat 视图浏览器已验证**；tree 由服务端 `ListComments(tree)` 覆盖，默认主题不可达（`SFTopicShowPage.vue:148` 硬编码 flat，`SFCommentStreamControls` 全仓无引用）——记录为产品缺口，本切片不加 UI；嵌套子评论 `likedByMe` 正确。
4. 匿名用户看到计数，点赞按钮点击后跳登录并带回跳地址。
5. 隐藏主题 / 非公开分类 / 已删评论：点赞 404，名单接口返回空或 404，不泄漏存在性。
6. 热门排序：**主题点赞**会改变 `hot_score`（评论点赞不计入）；**审核通过/驳回、删除评论之后点赞贡献不被抹掉**（回归测试锁死）。
7. 作者收到站内「X 赞了你的帖子」；邮件与推送默认关闭；自赞不发通知；取消后再赞不产生第二条通知。
8. `features.reactions` 关闭后：按钮消失、6 条点赞路由（4 个写 + 2 个名单）全部返回 403 `forum.reactions_disabled`。
9. 主题制品无需重建：`/site/active-theme/skin` 摘要与 `data-provider` 前后一致，页面正常渲染新控件。
10. 门禁：`go build ./...`、`go test`（相关包 + 隔离集群）、`bun test`、`typecheck`、架构边界、OpenAPI refs、`v3-catalog --check`、`./scripts/test.sh --fast` 全绿。

---

## 1. 已锁定决策

| 项 | 结论 |
| --- | --- |
| 范围 | 主题 + 评论同一切片；表结构预留 `reaction` 维度 |
| 通知 | 站内开；邮件/推送默认关；用户可在通知偏好打开 |
| 门槛 | 登录即可；封禁/禁言由现有账号状态拦 |
| 热门分 | 计入，权重 `+3`，公式收敛到一处 |
| 名单 | 计数 + 头像堆叠，点开看名单 |
| 列表行 | 首页/分类/标签显示赞数 |
| 开关 | `features.reactions`，后台可整体关闭 |
| 限速 | 写端点复用全局 limiter（默认 30 次/分/IP，`LIMITER_WRITE_MAX`）+ CSRF；名单读无独立限速，记为残留 |

---

## 2. 架构与数据流

```
写：Controller → ReactionService ─┬→ ReactionStore（PostgresStore，同事务）
                                 ├→ NotificationWriter.NotifyReactionTx（同事务）
                                 ├→ 事件 reaction.created / reaction.removed（提交后，fail-open）
                                 └→ CacheInvalidator（提交后，bump 主题/评论代际）

读：PublicReadService（已有 viewer 入口）
      ├→ Service.GetTopic / ListComments（走 CachedStore，计数来自列）
      └→ ReactionReader.Decorate（每请求一次批量查询 likedByMe，不写进缓存）
```

关键约束：**`likedByMe` 永远不进缓存**。`CachedStore` 缓存的是 actor 无关的 `TopicDetail`/`CommentList`，装饰必须发生在缓存之后（`PublicReadService` 内）。

**不碰 `forum.Service`**：它已有 77 个接收者方法、架构门禁对增长与收缩双向报错。新能力落在独立的 `ReactionService` 类型（同包、新文件），沿用 `CitationService`/`AdminUserCreator` 的既有先例。

---

## 3. 数据模型与迁移

新迁移 `apps/api/database/migrations/202610280001_content_reactions.sql`，声明 `-- +sforum OnlineSafe`（新增表 + 带默认值的新列，旧二进制可继续工作）。

**Up**

```sql
CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_reactions_target_type_check CHECK (target_type IN ('topic','comment')),
  CONSTRAINT content_reactions_reaction_check CHECK (reaction IN ('like')),
  CONSTRAINT content_reactions_target_positive CHECK (target_id > 0),
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);
CREATE INDEX content_reactions_roster_idx
  ON content_reactions (target_type, target_id, reaction, created_at DESC, id DESC);
CREATE INDEX content_reactions_user_idx
  ON content_reactions (user_id, created_at DESC);

ALTER TABLE topics   ADD COLUMN like_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE comments ADD COLUMN like_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE topics   ADD CONSTRAINT topics_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE comments ADD CONSTRAINT comments_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE topics   VALIDATE CONSTRAINT topics_like_count_nonnegative;
ALTER TABLE comments VALIDATE CONSTRAINT comments_like_count_nonnegative;
```

- `CHECK ... NOT VALID` + `VALIDATE` 避免对大表做阻塞式全表校验。
- 多态 `target_id` 无外键（与 `entity_meta` 同姿态）：完整性由服务层事务保证；内容删除是软删，行保留，无需级联。
- `user_id` 走 `ON DELETE CASCADE`，用户硬删不留孤儿。

**稳定读视图**（同文件，AGENTS.md 要求的同变更交付）：

```sql
CREATE VIEW sforum_core_v1.forum_reactions
WITH (security_barrier = true, security_invoker = false) AS
SELECT r.id, r.target_type, r.target_id, r.reaction, r.created_at,
       u.id AS user_id, u.username, u.display_name
FROM content_reactions r
JOIN users u ON u.id = r.user_id AND u.status = 'active'
WHERE <主题可见性谓词> OR <评论可见性谓词>
OFFSET 0;
```

谓词必须逐字镜像 `sforum_core_v1.forum_topics` / `forum_comments`（`topics.status IN ('active','locked')`、`categories.visibility='public'`、`comments.status='active'`），不允许另写一套。

**通知类型与默认策略**（同文件，照抄 `202608200001_moderation_pending_notifications.sql`）：

```sql
INSERT INTO notification_type_descriptors (...) VALUES ('reaction',1,1,'engagement',TRUE,FALSE,
  '{"icon":"i-lucide-heart"}'::jsonb, '{"kind":"entity","id":"core.entity.notification_target"}'::jsonb)
ON CONFLICT (type) DO NOTHING;
INSERT INTO notification_type_policies (...) VALUES
  ('reaction','in_app',TRUE,TRUE,TRUE,FALSE),
  ('reaction','email',FALSE,FALSE,TRUE,FALSE),
  ('reaction','web_push',FALSE,FALSE,TRUE,FALSE)
ON CONFLICT (type,channel) DO NOTHING;
UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE;
```

**Down**：删视图 → 删两个约束与两列 → 删索引与表 → 删通知偏好/策略/描述符 → 再 bump `notification_policy_revisions`。

**不做回填**：`like_count` 默认 0 时 `hot_score` 公式与现值一致，无需全表 UPDATE。

---

## 4. 里程碑

### M0 — 迁移与契约骨架

- 上述迁移文件。
- `database/migrations/stable_core_views_test.go`：补 `forum_reactions` 的存在与谓词断言。
- 新迁移的 Up/Down 结构测试（照 `theme_host_approval_test.go` 的声明断言）。

### M1 — 领域写路径

| 文件 | 内容 |
| --- | --- |
| `app/Models/Forum/reactions.go` | `ReactionTargetType`、`ReactionState`、`ReactionRoster`、`ErrReactionsDisabled`、`CodeReactionsDisabled` |
| `app/Models/Forum/reaction_store.go` | 窄接口 `ReactionStore`（**不进 `Store`**，避免 4 个 fake 全部改签名） |
| `app/Models/Forum/reactions_service.go` | `ReactionService`：`LikeTopic`/`UnlikeTopic`/`LikeComment`/`UnlikeComment`/`ListTopicLikes`/`ListCommentLikes` |
| `app/Models/Forum/postgres_store_reactions.go` | `PostgresStore` 实现：`ApplyReaction`、`GetReactionState`、`ListReactionUsers`、`LikedTargetIDs` |
| `app/Models/Forum/hot_score.go` | 唯一公式入口 `TopicHotScoreSQL` |
| `app/Models/Forum/topic_summary_sql.go` | 从 `postgres_store_ops.go` 迁出 `topicSummarySQL` + `topicListOrderBy`（腾出 cap 空间） |

写路径规则：

- 门槛：`actor.ID > 0 && actor.Status == active`；否则 `identity.ErrPermissionDenied`。
- 开关：`FeatureGate.IsFeatureEnabled(ctx, "features.reactions")` 为假 → `ErrReactionsDisabled`。
- 可见性：主题走 `store.GetTopic`（复用公开规则）；评论走 `GetCommentSummary` + 所属主题 `GetTopic`，评论必须 `active`。
- 幂等：`INSERT ... ON CONFLICT DO NOTHING RETURNING id`。插入成功才 `like_count + 1` 并重算 `hot_score`；未插入直接返回当前状态。
- 取消：`DELETE ... RETURNING id`，删到才 `GREATEST(like_count - 1, 0)` 并重算。
- 事务内顺序：reaction 行 → 计数列 → 通知投影。任何一步失败整体回滚。
- 通知：目标作者 ≠ 操作者才写，`DedupeKey = reaction:{targetType}:{targetID}:{actorID}`。
- 事件：提交后 `Emit` `reaction.created` / `reaction.removed`（`KindObserve`，payload 仅 `targetType`/`targetId`/`reaction`/`actorUserId`），失败只记日志。

热门分公式（唯一入口）：

```go
// TopicHotScoreSQL 返回 hot_score 的完整表达式；commentExpr/likeExpr 用于
// 表达「即将写入的新值」，避免六处各写一遍。
func TopicHotScoreSQL(commentExpr, likeExpr string) string
```

必须改用的六处：`postgres_store_ops.go`（评论创建/重排队/删除）、`revision_writes.go`、`Moderation/workbench_store.go`（审核通过/驳回）。点赞写路径新增第七处调用点。

### M2 — 读模型与 HTTP

**类型**（先做抽取，否则越线）：`Forum/types.go` 原 998 行、上限 1000。把评论相关类型移到 `types_comments.go`（同包，零 API 变化），再在 `TopicSummary` 加 `LikeCount`、`TopicDetail` 加 `LikedByMe`；`Comment` 加同名字段。

**查询**：`topics.like_count` 进 `topicSummarySQL`、`list_topics.go`、`search_live.go`、`Profile/postgres_store.go`；`comments.like_count` 进 `commentSelectSQL`（flat/tree/replies 共用）。逐处改 scan。

**装饰**：`public_read_service.go` 加 `WithReactions(reader)`，在 `GetTopicForViewer`、`GetTopicBySlugForViewer`、`ListComments`、`ListCommentRepliesForViewer` 之后调用 `DecorateTopic` / `DecorateComments`（递归 `Children`），最多两次批量查询，匿名直接跳过。

**路由**（`Controllers/Forum/routes.go`）：

| 方法 | 路径 |
| --- | --- |
| POST | `/api/v1/topics/:topicID/like` |
| DELETE | `/api/v1/topics/:topicID/like` |
| POST | `/api/v1/comments/:commentID/like` |
| DELETE | `/api/v1/comments/:commentID/like` |
| GET | `/api/v1/topics/:topicID/likes` |
| GET | `/api/v1/comments/:commentID/likes` |

名单参数：`limit`（默认 20，上限 50）、`offset`（上限 1000）。响应 `{items:[{id,username,displayName,avatar}], total, hasMore}`。

**错误映射**：`mapForumError` 增 `ErrReactionsDisabled` → 403 `forum.reactions_disabled`。

**功能开关**：`Models/Options/feature_flags.go` 增 `NameFeatureReactions`（`public: true`、推荐默认开、`settings.site.manage`），并加入 `service_normalize.go` 的已知键分支与 `FeatureFlagCatalog`。后台 `/control-panel/settings/features` 页面是目录驱动的，只需补 `admin.features.flags.features.reactions` / `flagsHelp` 两条文案。

### M3 — 通知

- `Notifications/types.go`：`TypeReaction = "reaction"`。
- `Notifications/registry.go`：`coreTypeDescriptors()` 增 `{TypeReaction, "engagement", "i-lucide-heart"}`。
- `Notifications/fanout.go`：`NotifyReactionTx(ctx, tx, ReactionEvent)` —— 查目标作者（`users.status='active'`）、跳过自赞、写 `CreateProjectionsTx`（`TargetType` 为 `topic`/`comment`，`Channels: coreDeliveryChannels()`）。`ResolveNotificationTarget` 已支持两种目标，无需改。
- `bootstrap/notification_adapter.go`：接上 forum 侧窄接口 `ReactionNotificationWriter`。
- `Jobs/Notifications/render.go`：新增 `case "forum.reaction"` 的中英邮件文案（数据键与 `forum.reply` 同形）。
- Web：`utils/notifications/notificationsPresentation.ts` 增图标与 `reaction` 过滤项；zh-CN/en-US 文案（`notifications.types.reaction`、`notifications.body.reaction`）。

### M4 — 前端

| 文件 | 改动 |
| --- | --- |
| `components/forum/SFReactionControl.vue`（新） | 赞按钮 + 计数 + 头像堆叠弹层；乐观切换、失败回滚、匿名跳登录、`disabled` 态 |
| `components/forum/SFTopicReactionBar.vue`（新） | 把 `SFTopicShowPage.vue` 现有「分享 / 回复」动作行整段抽出，并挂 `SFReactionControl` |
| `components/forum/SFTopicShowPage.vue` | 动作行替换为 `<SFTopicReactionBar>`（净减行，同步下调架构基线） |
| `components/forum/SFComment.vue` | 动作条内挂 `SFReactionControl` |
| `components/forum/SFHomeTopicRow.vue` | 列表行加赞数（图标 + 数字，沿用现有计数样式） |
| `composables/forum/useForumApi.ts` | `likeTopic`/`unlikeTopic`/`likeComment`/`unlikeComment`/`listTopicLikes`/`listCommentLikes` |
| `utils/forum/forumReaction.ts`（新） | 类型 + 纯函数：乐观切换、真值回填、匿名门槛、名单可见性、登录回跳 |
| `composables/useWebOptions.ts` | 新增 `featureEnabled(name)` 与 `reactionsEnabled`（当前无客户端消费入口） |

交互细节：`aria-pressed` 表达选中；`title`/`aria-label` 提供完整文案；Toast 成功提示；失败保留原值并出错误 Toast（不自动关闭）；390 宽不新增横向溢出。

### M5 — 扩展数据面（AGENTS.md 强制项）

`content_reactions` 是新的核心数据域，必须同变更交付稳定查询：

- `Support/HostAPI/v2_query_runtime.go`：`QueryPublicReactionsList = "sforum.core.public_reactions.list"`、结果 schema `sforum.core.public_reaction`，加入 `isStableProtocolV2QueryID`。
- `Support/HostAPI/v2_query_postgres.go`：基于 `sforum_core_v1.forum_reactions` 的声明式计划（`target_type` + `target_id` 必填，`reaction` 可选，默认 `created_at DESC, id DESC`）。
- `Support/HostAPI/query_registry_core.go`：显式映射（`PaginationOffset`、`PermissionPolicyPublic`）。
- 测试：允许（公开目标返回行）+ 拒绝（隐藏主题返回零行，不报错也不泄漏）。

### M6 — 目录、基线与知识库

- `node scripts/v3-catalog/generate.mjs`：核心路由 380 → 386；两个新组件的 UI 身份 `owners: ["public"]`。
- `tests/architecture-boundaries-baseline.json`：`SFTopicShowPage.vue` 1137 → 1127；`postgres_store_ops.go` 1471 → 1438（棘轮必须同轮下调）。
- `contracts/openapi/paths/forum.yaml` + `schemas/forum.yaml` + `contracts/openapi.yaml` 索引；跑 `ruby scripts/validate-openapi-refs.rb`。
- 知识库：`modules/forum.md`、`modules/notifications.md` 更新；决策 `decisions/2026-10-28-content-reactions.md`；`plans/2026-07-12-iteration-a-engagement-loop.md` 状态更新；session handoff。

### M7 — 验证

- Go：`go build ./...`；`forum` 包单测；`./scripts/test-api-isolated.sh` 跑真实 PostgreSQL 集成（唯一约束并发、计数精确、热门公式六处一致、名单分页、用户删除级联、通知去重）。
- Web：`bun test`、`bun run typecheck`、`node tests/validate-architecture-boundaries.mjs`。
- 浏览器 QA（真实会话，1633×810 + 390×844）：匿名/登录两态、点赞/取消、双击、名单弹层、列表行计数、通知出现、开关关闭后按钮消失。
- 主题无重建证明：记录改动前后 `/site/active-theme/skin` 摘要与 `/pages/resolve?id=forum.topic.show` 的 `data-provider`。

---

## 5. 公开 API 与 Schema 变更

- 新增 6 条路由（上面表），全部在 `contracts/openapi/paths/forum.yaml` 以独立 path item 暴露。
- 新 schema：`ReactionState`、`ReactionRoster`、`ApiResponseReactionState`、`ApiResponseReactionRoster`。
- 既有 schema 增量字段：`TopicSummary.likeCount`、`TopicDetail.likedByMe`、`Comment.likeCount`、`Comment.likedByMe`（全部向后兼容，无必填新增）。
- 新稳定查询：`sforum.core.public_reactions.list` / 结果 schema `sforum.core.public_reaction@1`。
- 新错误码：`forum.reactions_disabled`（403）。

---

## 6. 边界与失败模式

| 场景 | 行为 |
| --- | --- |
| 匿名点赞 | 401；前端先跳登录并带回跳 |
| 隐藏主题 / 非公开分类 | 404 `forum.topic_not_found`（不暴露存在性） |
| 已删评论 / 跨主题评论 id | 404 `forum.comment_not_found` |
| 锁定主题 | 允许点赞（与 Discourse 一致） |
| 自赞 | 允许，不产生通知 |
| 开关关闭 | 6 条路由（4 写 + 2 名单）403；读模型仍返回历史计数，前端隐藏控件 |
| Redis/缓存不可用 | 点赞照常写库；缓存失效失败只记日志，计数真值不受影响 |
| 并发点赞 | 唯一约束 + `ON CONFLICT` 幂等；计数在事务内，不会重复加 |
| 用户硬删 | `ON DELETE CASCADE` 清 reaction 行；`like_count` 可能偏高，记入模块笔记的已知残留与修复 SQL |
| 审核通过/驳回、删评论 | 热门公式统一入口，不再抹掉点赞贡献（回归测试） |

---

## 7. 测试矩阵（最小但完整）

**Go 单测（`package forum`）**：首次点赞 +1、重复点赞不变、取消 -1、取消不存在不变、隐藏主题 404、已删评论 404、开关关闭 `ErrReactionsDisabled`、自赞不通知、名单分页与上限钳制。

**Go 集成（真实 PostgreSQL，一次性集群 5 包）**：
`app/Models/Forum/reactions_postgres_integration_test.go`（并发 10 次点赞只产生 1 行且计数为 1；`like_count` 与 `COUNT(*)` 一致；`hot_score` 三组对照；`ON DELETE CASCADE`；点赞不改 `updated_at`；自赞不投影）、
`app/Models/Notifications/reaction_projection_postgres_integration_test.go`（取消再赞只 1 条通知）、
`app/Support/HostAPI/v2_query_postgres_reactions_integration_test.go`（稳定查询允许/拒绝两侧）。

**Go 契约与 HTTP**：
`database/migrations/content_reactions_test.go` + `database/migrations/stable_core_views_test.go`（视图谓词逐字镜像、`security_barrier`/`security_invoker`、无 PII）；
`Support/HostAPI/query_registry_core_reactions_test.go`（查询注册与权限策略）；
`Http/Controllers/Forum/reactions_http_test.go`（403/404/错误码）、`reaction_cache_scope_test.go`（`private, no-store` + `Vary: Cookie`）。

**Web**：`apps/web/tests/forum/forumReaction.test.ts`（乐观切换、回滚、匿名判定、开关判定、回跳）；`apps/web/tests/forum/forumReactionRoster.test.ts`（打开即请求并渲染名单、关闭不重复请求、comment 目标走 comment 名单，3 条）；两个挂载点存在性契约；i18n 键齐全。全量 `bun test` 1837 pass / 0 fail / 217 文件（1806 → 1837，+31）；`bun run typecheck` 0。

**浏览器 QA（2026-10-29）**：功能 `storage/qa/reactions-qa-report.md`（匿名回跳、登录点赞/刷新保持/取消归零、连点 3 次只 1 POST 且 DB 1 行、flat 与嵌套子评论、名单弹层、列表行计数、通知去重、主题未重建 digest）；视觉/响应式 `storage/qa/2026-10-29-visual-responsive-audit.md`（390 无横向溢出、英文不裁切、aria 合规；四项待修与三项 token 债务见 handoff）。**开关关闭端到端** `storage/qa/reactions-flag-evidence.txt`：6 端点 403 `forum.reactions_disabled`、`aria-pressed` 10→0→10、`like_count` 零漂移、UPDATE 后约 4 秒生效，已还原 enabled。

**门禁**：`./scripts/test.sh --fast` 三线 + `validate-architecture-boundaries.mjs` + `v3-catalog --check`。

---

## 8. 明确不做（第一刀）

- 收藏（第二刀，个人主页 self-only tab）。
- 多表情反应选择器、反应汇总条。
- 点赞排行榜、按赞数筛选/排序。
- 名单读的独立限速（写端点已由全局 limiter 覆盖；要加时复用 Identity 的 Redis limiter 形态）。
- 计数自动对账任务/CLI（先以稳定视图 + 文档化修复 SQL 兜底）。
- 关注、私信、徽章、信任等级。
- 主题制品改动（本切片不需要，也不允许顺手改）。

---

## 9. 假设与风险

1. **`types.go` 与 `postgres_store_ops.go` 必须先抽取再新增**，否则 1000 行硬规则与 1471 行基线都会红；这是本计划里唯一"先重构后加功能"的地方，抽取只搬不改。
2. **热门分收敛是行为变更**：权重从 `comment*5 + view` 变为 `comment*5 + view + like*3`。存量 `like_count=0`，上线瞬间排序不变；之后按点赞演进。若运营不接受权重，只需改 `hot_score.go` 一处常量。
3. **`features.reactions` 需要客户端消费入口**：仓库此前没有 `features.*` 的前端读取封装，本计划补 `featureEnabled(name)`。
4. **通知模板**：`forum.reaction` 必须进 `render.go`，否则用户一旦打开邮件通道会出现无模板投递（默认关闭不等于可以不写）。
5. **多态目标没有外键**：完整性靠服务层事务。若将来出现绕过服务的写入路径，需要补触发器或收回写权限。
6. **架构基线双向报错**：任何"顺手删几行"都会让门禁失败，抽取产生的收缩必须同轮下调基线，不能留余量。
7. **不做主题重激活**：`topic-show.html` 只挂 `<sf-topic-show-page>`，控件在 Core 组件内；若实现中发现需要改模板，必须停下来先确认——那会把本切片从"小"变成"中"。
