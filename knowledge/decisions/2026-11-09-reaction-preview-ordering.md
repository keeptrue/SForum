# 点赞者预览：不进缓存 + 两处排序不同

- 日期：2026-11-09
- 状态：accepted
- 相关：`knowledge/modules/forum.md`（点赞者头像组预览）、
  `knowledge/decisions/2026-10-28-content-reactions.md`

## 背景

点赞计数旁的头像组（`likedBy`）需要在**首屏 SSR** 就有内容，且不能为每条
主题/评论多打一次请求。同时「谁赞了」弹层需要完整名单与分页。两者数据同源
（`content_reactions`），但读模型、顺序、缓存姿态不同。

## 决策

### 1. 预览不进任何共享缓存

`likedBy` 与既有的 `likedByMe` 一样，**只在缓存之后装饰**：`CachedStore` 只缓存
actor 无关的主题/评论主体，装饰在 `PublicReadService` 拿到值拷贝之后就地完成；
`CachedStore.ReactionPreviews` 是纯转发（不读、不写缓存）。

理由：预览首个元素是「当前访客自己」，是 per-viewer 数据。一旦进入共享缓存，
访客之间会串味，且无法靠 TTL 兜住（错误顺序会一直服务到过期）。副作用是登录
访客的详情响应必须 `Cache-Control: private, no-store` + `Vary: Cookie`（已有
断言 `reaction_cache_scope_test.go`），匿名响应保持可公共缓存 —— 匿名的预览由
`viewerUserID = 0` 决定，对所有匿名访客一致。

被否的替代方案：把预览放进缓存并在命中后重排 —— 需要把完整点赞者列表塞进
缓存体，体积与失效成本都不可接受。

### 2. 两处顺序不同（有意为之）

| 位置 | 顺序 | 理由 |
| --- | --- | --- |
| 头像组预览（≤3） | 自己优先，其余 `created_at, id` **最早**在前 | 头像是「我参与过」的即时反馈；剩余位置表达「最早支持者」 |
| 名单弹层（20/页） | `created_at DESC, id DESC` **最近**优先 | 活跃度视图，最新参与者更相关 |

匿名预览里 `(r.user_id = $3)` 对全部行恒为假，`ORDER BY ... DESC` 自然退化为
纯最早优先，不需要额外分支。

代价：同一批人会有两种顺序，用户对比两处会看到顺序不同。接受该代价，因为两处
回答的是不同问题（「我在其中吗」vs「最近谁在赞」）。

被否的替代方案：统一为「最近优先」。这样自己经常不在前三个里，头像组的核心
价值（自己的参与反馈）就丢了。

### 3. 名单的有界性与去重

`offset` 上限 1000、`limit` 上限 50（服务端 clamp）；客户端 20/页、按 `id`
去重合并。`offset` 分页在「最近优先」下新赞会插到第 0 页，去重兜重复；服务端在
`offset` 越界时返回 `total=0`（窗口计数无行可依），前端 `forumReactionRosterTotal`
只增不减，避免「共 N 人」被抹掉。

## 影响

- 新增 `ReactionPreviewStore` 窄接口，不并入 `Store`/`ReactionStore`（四个测试
  fake 不必改签名）。
- 预览 SQL 复用 `content_reactions_roster_idx`，**没有新迁移**；排序首键是计算
  表达式，每目标仍需一次小排序（每目标 ≤3 行）。
- fail-open：预览查询失败只告警，公开读仍 200，头像组退化为 `+N`。
