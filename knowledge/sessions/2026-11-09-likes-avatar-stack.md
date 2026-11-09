# 2026-11-09 Session Handoff — 点赞头像组（likedBy）与「谁赞了」名单

## Changed

- 后端：`TopicDetail.likedBy` / `Comment.likedBy`（含嵌套子评论）新增点赞者预览，
  最多 3 个；`ReactionPreviewStore` 窄接口 + `PostgresStore.ReactionPreviews`
  一条批次 SQL（CTE + `ROW_NUMBER() OVER (PARTITION BY target_id ...)`）；
  `CachedStore.ReactionPreviews` 纯转发不缓存；装饰在 `PublicReadService` 缓存之后。
- 前端：`SFAvatar` 新增 `xs=20` / `2xs=18`；`SFAvatarGroup` ring 变量化 +
  `decorative`（组 `aria-hidden`、头像 `alt=""`）；新增 `SFReactionStack.vue`
  （纯展示、首屏零请求）与 `SFReactionRoster.vue`（弹层名单，20/页）；
  重写 `SFReactionControl.vue`（218 行，心形 + 头像组触发器 + 弹层）；
  主题动作行抽到 `SFTopicReactionBar.vue`；`SFTopicShowPage.vue` 保持 1127 行。
- 契约与目录：`contracts/openapi/schemas/forum.yaml` 为 Topic/Comment 补
  `likedBy`；`forumTaxonomy.ts` 从 1000 行降到 925（抽出
  `forumTaxonomyContent.ts`，`export *` 保持导入路径不变）。
- V3 稳定身份：新增两条组件身份
  `core.component.shared.sf_reaction_stack` /
  `core.component.shared.sf_reaction_roster`
  （`docs/extensions/v3/catalog-identities.json`），重新生成
  `docs/extensions/v3/catalogs/**`；`node tests/validate-v3-p0-catalogs.mjs`
  → `386 routes, 353 UI surfaces, 99 traceability rows`，exit 0。
- 审查后收口（2026-11-09 晚）：`SFReactionRoster` 的「加载更多」可见性改为
  `showLoadMore = canLoadMore(hasMore, false, offset, cap)` —— 加载中按钮保持挂载，
  `loading` 只负责 `disabled` / `aria-busy` / spinner；`total` 用
  `forumReactionRosterTotal` 首页取真值、追加页只增不减；`SFAvatarGroup.__item`
  改 `display: flex` 修真实 `<img>` 头像的 descender 撑高。
- 按老板视觉反馈把主题行头像组 36px → 24px（`xs`），尺寸阶梯
  `2xs 18 < xs 24 < sm 32 < list 36` 保持单调；触发器 `min-height` 随之取 40px
  （38 会把动作行压到 57px），动作行仍 59px、头像与心形中心对齐；弹层名单行内
  头像同步 24px、行高仍 32px。
- 焦点收口（2026-11-09 晚，最后一条）：`SFReactionRoster` 的「加载更多」不再用原生
  `disabled`（浏览器会在两帧后异步移除焦点），改为 `aria-disabled` + `loadMore()`
  内 `forumReactionRosterCanLoadMore` guard + `.sf-roster__more[aria-disabled='true']`
  样式。实测键盘 Enter 触发后焦点仍在按钮上、`aria-disabled="true"`、
  `aria-busy="true"`、spinner 可见、弹层高度 252 不跳；连按两次 Enter 只发一次请求。
- 回归测试：`TestReactionServiceDecorateKeepsPreviewWhenLikedByMeFails`、
  `TestCachedCommentListNeverCachesLikedByPreview`，并把 topic 用例的缓存原文断言
  加强为不含 `"likedBy"`（同时覆盖 `likedBy` 与 `likedByMe`）。
- 知识库：`knowledge/modules/forum.md` 新增「点赞者头像组预览」小节；
  新增 `knowledge/decisions/2026-11-09-reaction-preview-ordering.md`。

## Decisions

- 预览**不进缓存**：`likedByMe` 与 `likedBy` 都是请求级/访客相关字段，只在
  `CachedStore` 之后装饰；匿名响应的公共缓存语义不变。
- **两处排序不同**：头像组「自己优先 + 其余最早」（参与感 / 最早支持者），
  名单「最近优先」（活跃度视图）。详见决策记录。
- 预览查询 fail-open：失败只告警，公开读仍 200，头像组退化为 `+N`。

## Next

- 评论动作行文字对比度 2.59:1 是既有欠账，与本次无关（保留一行备注，不进结论）。
- 评论 tree 视图仍不可达：默认主题 `SFTopicShowPage.vue:148` 硬编码
  `ref<'flat'>('flat')`，`SFCommentStreamControls` 全仓无引用；服务端
  `ListComments(tree)` 覆盖，本切片不加切换 UI（既有产品缺口）。
- 预览 SQL 若上调 `ReactionPreviewLimit`（现 3）需重估索引：排序首键
  `(r.user_id = $3) DESC` 是计算表达式，只复用
  `content_reactions_roster_idx` 的谓词/分区，每目标仍有一次小排序。
- 既有 flake（**与点赞改动无关**，别当成自己的锅去查）：`./scripts/test.sh --fast`
  首次跑时 static 线红在 `tests/identity/authRouteRendering.test.ts >
  offers existing-account and registration continuations…`（5s 超时；同文件上一条在
  三线并发高负载下跑了 77s）；单跑该文件 11 pass / 0 fail / 868ms，紧接着完整
  `--fast` 三线全绿（`PASS api 33s / PASS web 31s / PASS static 14s`）。判断：并发
  负载下该用例 5s 超时偏紧，属既有 flaky（2026-11-09 由小前复现）。
- 后端 `ListReactionUsers` 在 `offset` 越界时返回 `total=0`（窗口计数无行可依），
  OpenAPI 未描述；前端已用 `forumReactionRosterTotal` 兜住，契约层待收敛。

## Open Questions

- 头像组预览与名单两处顺序不同（自己优先/最早 vs 最近优先）是否需要产品侧
  文案提示，取决于后续是否收到「顺序不一致」反馈。
