# Badges And User Activity Module

## Purpose

Owns badge definitions, tiers, groups, grants, and the per-user activity counters
that badges are computed from. Counters are deliberately a separate data domain
from badges: they are the substrate trust levels will read, and badges are just
the first consumer.

## Current Status

三刀已落地，全部通过独立验收：

| 刀 | 日期 | 结果 |
| --- | --- | --- |
| 徽章 + 用户活跃 | 2026-11-10 | 84 条后端断言 0 FAIL、16 条浏览器结构断言；双门禁绿 |
| 等级表（`badge_tiers`） | 2026-11-12 | 8 条门禁全绿；P1/P2 修复后复验通过 |
| **规则引擎（布尔条件树 + 插件事实层）** | **2026-11-12** | **M1 后端全片落地**（迁移/引擎/条件/存储/回扫/HTTP/契约）；**M2 全部完成**（编辑器 + 剩余五项：`requirementText` 接卡片、影响确认弹窗、`queued` toast、summary 字段提示、公开面/后台 390 QA）；**第一刀「回扫进度可见」已收口**（`badge_rule_backfill_runs` + `backfill-status` 端点 + 后台三态行，§13 残留 #15 关闭）；**M3 插件扩展已收口**（manifest 候选 + `badges.fact.record` + SDK 封装 + 夹具，见扩展面段）；**二轮编辑器 UX 收口**（单一参数控件 + 简单/高级双模式，见「条件规则编辑器」节） |
| **获得条件自动解释器** | **2026-11-13** | **落地（未提交）**：公开说明改为服务端从规则生成，13 条条件措辞注册测试**实际调用**、手工/脏规则/任意形状都不空白；后台两个 textarea → 只读预览 + `POST /admin/badges/rule-text`；`requirement_summary_*` 降级为回滚兼容。见「获得条件的自动解释器」节 |

规则引擎把「单指标 ≥ 阈值」升级成 JSONB 布尔条件树：`badges.rule` 成为自动徽章的**唯一**判定来源，`metric`/`threshold` 降为 legacy 只读（只给 down 迁移还原用）。

代码提交：`4457106e2`（apps，规则引擎 + 插件事实层）、`082f98624`（web，规则编辑器 / 回扫进度行 / 信任披露）、`4e1876331`（contracts，路由 422 与生成物）。

相关文档：计划 `../plans/2026-11-12-badge-rule-engine.md`、验收矩阵 `../reports/2026-11-12-badge-rule-engine-acceptance.md`、口径 SQL `../reports/2026-11-12-badge-rule-engine-condition-sql.md`、M3 契约（冻结）`../reports/2026-11-12-badge-conditions-manifest-spec.md`、偏差审计 `../reports/2026-11-12-badge-rule-engine-m1-deviation-audit.md`、决策 `../decisions/2026-11-12-badge-rule-engine.md`。

## 规则引擎分层

七块协作方，**依赖方向是单向的**，加功能前先认领正确的层：

| 层 | 位置 | 职责 | 不许做 |
| --- | --- | --- | --- |
| 纯 AST | `Support/RuleEngine/`（`types`/`decode`/`validate`/`eval`/`catalog`） | 解码（未知字段报错）、校验（深度≤5、叶子≤32、节点≤64、`days`∈1..3650、`value`∈1..1e9）、**Kleene 三值求值** | import 任何 `Models/` 包、任何 SQL、任何 i18n |
| 条件取数 | `Models/Badges/conditions/`（`registry` + `batch` + 5 个 `resolve_*`） | 13 条条件元数据；`Resolver.Prepare` 按 `type` 分组**每类一条 SQL** 并 memo，`Resolve` 逐叶子读 memo | 自己判「树是否成立」（那是 eval 的事） |
| 规则存储 | `Models/Badges/rule_store.go` | 规则读写 + 保存校验（AST + 引用存在性 + `badge.owned` **环检测 DFS**） | 在 HTTP 里跑全量 |
| 求值接线 | `Models/Badges/rule_eval.go` | `EvaluateRule` 把 `Resolver` 接进 store，并把**已声明 factKey 集合**透传给 `NewResolver(pool, userID, factKeys...)` | 吞掉 resolver 的错误 |
| 健康检查 | `Models/Badges/rule_health.go` | 失效引用清单（保存校验共用同一份引用检查） | 把「零授予」当失效 |
| 影响估算 | `Models/Badges/rule_impact.go` | `{estimated, method}`，AND 取最严叶子 / OR 取和，**上界语义（≈）** | 把估算当承诺 |
| 事实写入 | `Models/Badges/facts_store.go` | 同事务 `INSERT fact` + 计数累加；`dedupe_key` 唯一索引兜幂等 | 在 Core 里判定插件语义 |

回扫：`app/Jobs/Badges/backfill_rules.go`（`{badgeId, cursorUserId, batch}`，游标可续，`ByArgs` 去重）。**保存规则只入队，绝不在 HTTP 内跑全量**（残留 7，旧编号 #17，不得扩大）。

`evaluate.go` 保留签名，内部双分支：**快路径**只认「单 `activity.count` + `window.all` + `gte`」直接读计数器（10 枚内置徽章零回归、零额外查询）；其余走引擎。**判 `Rule == nil` 而不是判 `Metric == nil`**——纯 rule 徽章的 `metric` 是 NULL，先过旧 `ruleSatisfied` 会让新徽章「保存成功、达标、永不发、零报错」（P-04）。

## 条件目录（13 条 = 12 内置 + `fact.count`）

元数据在 `conditions/registry.go`，文案（双语 label/describe/unit）**直接写 Go**，不进 locale JSON——条件目录是服务端下发的数据，走 `t()` 会撞 `adminBadgesI18n.test.ts` 的双向断言。

| key | 取值 | 参数 |
| --- | --- | --- |
| `activity.count` | 计数 gte/lte | `metric` enum(6) + `window` |
| `activity.streak` | 计数 | `minDays` int(1..3650) |
| `object.engagement` | 布尔 | `action` enum(like/bookmark/comment) + `topicId` **int**（不是 objectRef） |
| `object.authoring` | 计数 | `targetKind` enum + `targetSlug` objectRef(category) + `window` |
| `identity.account_age` | 计数 | **无参数**（值就是年龄，比较走 `cmp`；不要加 `days`） |
| `identity.email_verified` / `identity.avatar` | 布尔 | 无 |
| `identity.oauth_link` | 布尔 | `providerId` objectRef(provider) |
| `identity.role` | 布尔 | `roleKey` objectRef(role) |
| `content.pinned` / `content.hidden` | 计数 | 无 |
| `badge.owned` | 布尔 | `slug` objectRef(badge)，保存时做环检测 |
| **`fact.count`** | 计数 | `factKey` objectRef(**fact**) + `variant` enum(可选) + `window` |

**`fact.count` 是插件条件的唯一入口**，M3 的 `badgeConditions[]` 不新增条件类型，只给它注册 `factKey`/`variant` 候选。两个面必须分开：

- `conditions.Core()` = **校验与求值全集**（13 条，含 `fact.count`）。从这里删掉它 → M3 存下的规则保存时 `unknown_condition`。
- `PublishableDefinitions(factKeys)` = **展示子集**（后台 `GET /admin/badges/conditions`）。M1 传 nil → `fact.count` 不出现（空 options 的 enum 是陷阱）。控制器只调它，**绝不回退 `Definitions()`**。

`ParamSpec.RefKind` ∈ `category|tag|role|provider|badge|fact`；数字一律 `int`，**绝不混进 objectRef**。`variant` 的 `Options: nil` 表示「候选由事实目录运行时填」，不是空下拉。

## 获得条件的自动解释器（2026-11-13）

公开面「获得条件」从**运营手填**改成**服务端从规则生成**。产品口径因此变了：以前写「运营手填优先」的地方全部作废，`requirement_summary_*` 只作为回滚兼容保留。

**唯一实现（两个文件，别在别处拼句子）**：

- `conditions/phrases.go`（269 行）：13 种条件各一个 `PhraseFunc`，签名 `(params, cmp, names RefNames) (zh, en string)`，经 `Definition.Phrase` 注册。注册测试 `TestEveryConditionHasPhrase` 会**用最小合法参数实际调用每个条件**并断言双语非空——只判 `Phrase != nil` 会把 `return "", ""` 放过去，所以它连「实现改成返回空串」一起咬住；条件数不是 13 也直接红。
- `requirement_text_render.go`（244 行）：树遍历。中文用**标点**连接（`，并且` / `，或者`，连接符**不带尾空格**）——`≥ 5并且` 是粘连，`并且 账户年龄` 又像多打了空格。组内子句超过 `maxListedClauses = 4` 压成「满足全部 N 个条件，例如：A、B、C」（保留 `listedHead = 3` 条），`any` 压成「满足其中任一条件（共 N 个），例如：…」；单句超 `maxLeafRunes = 100` 的叶子降级为「满足『条件名』条件」，整段超 `maxTextRunes = 160` 降级为「满足 N 个条件，其中最关键的是：…」。**先结论后举例**是有意的：「A 并且 B…等 N 个条件」在中文里没有断句点，读者分不清 N 是总数还是第 N 条。

**永不空白**（产品口径，不是兜底）：手工徽章（`Rule == nil`）→「由管理员手动授予」/「Granted manually by an admin」；`Rule == nil && RuleBroken`（脏规则）→ 泛称句「满足一个自定义条件」，**不能**谎称「由管理员手动授予」——那等于把脏数据说成运营决定；组缺句子 →「满足若干条件」。任何形状都有 zh + en；终审穷举 936 种形状组合 0 失败（审阅侧证据，不在仓内测试里）。

**引用真名**：`CollectRefs` 从规则收集 `RefSet` → `RefNameLoader`（窄接口，`RefNameLoaderOf` 从 store 取）→ `ResolveRefNames`，每类引用**一次** `= ANY($1)` 批量解析，解析不到回落泛称并记一条 warn（fail-soft）。provider 名来自 identity registry 的插件 label（装配点 `bootstrap/api_assembly_domains.go`）。渲染本身是纯函数，「N 枚徽章 = N 次名字查询」在结构上不可能发生。

**预览端点**：`POST /admin/badges/rule-text`（路由 `controller.go:107`，handler `rule_controller.go:60`）。权限是 **`badge.view`**，不要按 method 一刀切改成 `badge.manage`：它不写库、不审计、不入队，同一批管理员本就能从 `GET /admin/badges` 拿到规则原文，改 manage 只会让只读管理员看不到自己有权读的东西。非法规则返回与保存**同一个** 422 码（预览与保存共用一条校验链）。

**前端**：两个 textarea → 只读预览 +「由规则自动生成」；`useBadgeRuleTextPreview` 300ms 防抖 + 序号丢弃过期响应 + 失败保留旧文案且**不阻断保存**；`toAdminBadgeInput` 不再发 `requirementSummary`；`badgeRuleNeedsSummary` / `AUTO_SUMMARY_METRICS` 已删除（测试断言源码里不再出现这两个名字）。

**保留但不读**：`requirement_summary_zh/en` 列、`POST`/`PATCH` 的 `requirementSummary` 字段、后台 wire 的回显都还在（旧客户端与回滚用）——服务端仍会接受并落库，**但没有任何文案读路径再用它**：公开目录、资料页徽章墙、后台列表与编辑弹窗全部走 `RequirementText`（`Public()` 现算，不拿结构里缓存的旧文案）。回滚 = 恢复读列 + 回退该 commit；列与数据都在，不需要迁移。

## 两条相反的失效语义（本模块最容易被「统一」掉的地方）

`conditions/resolve_facts.go:10-17` 有原文，判定时**看返回策略，不看注释**：

| 情形 | 返回 | 理由 |
| --- | --- | --- |
| 引用对象不存在（主题/分类/标签/角色/provider/徽章 slug 没了） | **确定值 0/false** | 「没做过」，不是「判不了」；返 error 会让整棵树 unknown，徽章永远不发 |
| 条件本身失效（`factKey` 无任何已启用扩展声明） | **error**（引擎转 unknown） | 0 会被 `lte` 判满足、被 `not` 判成 true，两条都是 fail-open |

三值链路：`resolver` 报错 → `eval.leaf` 记 `CodeResolveFailed` 并返回 `triUnknown` → `not(unknown)=unknown` → 最终 `Matched=false, Incomplete=true`。**所以「失效条件」不会经 `not` 反转成授予**。M1 的 `EvaluateRule` 传空 factKeys，因此任何 `fact.count` 叶子在 M1 恒 unknown——这是正确行为，M3 只需把集合填上。

`rule_health` 的两个 kind 对应用户能做的两件事：

- `undecodable_rule`：`badges.rule` 解不开（手工改库 / 旧格式）→ 该徽章**永不授予**，需人工修数据。
- `invalid_rule_shape`：能解但校验不过（超上限、未知条件、参数不合法）→ 保存路径本应拦住，出现即说明绕过（导入工具/直改库）。

另有 `missing_{topic,category,tag,role,provider,badge_slug,fact_key}` 表示「引用对象/事实键不在了」。**判 provider 必须接受「identity registry 已声明但尚无用户绑定」**，否则新装插件一上线全站规则先红一轮；**零授予不算失效**。

## Data Model

九张表 + 一个反规范化列。基础五张来自 `202611100002`，`badge_tiers` 来自 `202611120001`，规则引擎三张来自 `202611120002`：

| 对象 | 作用 |
| --- | --- |
| `user_activity` | 每用户一行六项计数 + 预留阅读字段；`user_id` 主键，懒创建 |
| `user_visits` | `(user_id, visited_on)` 主键，`days_visited` 的真值来源 |
| `badge_groups` | 分组（`activity`/`content`/`honor`），`position` 决定资料页分区顺序 |
| `badges` | 定义 + `rule` JSONB（自动徽章唯一判定来源）+ `requirement_summary_{zh,en}`；`metric`/`threshold` 为 legacy 只读 |
| `badge_tiers` | 等级档位（`key` 主键 + 双语名 + 图标 + 颜色 + `position` + `enabled`） |
| `user_badges` | 授予行，`UNIQUE (user_id, badge_id)`，`granted_by` 可空 |
| **`badge_facts`** | 插件事实流水（append-only）；`delta <> 0`；`fact_key` 正则 CHECK；`dedupe_key` 部分唯一索引兜幂等 |
| **`user_fact_counters`** | 事实累计，PK `(user_id, fact_key, variant)`；`total` **允许为负**（扣减），读侧统一 `GREATEST(...,0)` |
| **`badge_rules_rollback`** | 迁移前 `metric`/`threshold` 快照，**只在 Down 段读** |
| `users.top_badge_id` | 反规范化「最高级 1 个」，列表页据此单列 JOIN |

`badges.tier` 外键 `ON UPDATE CASCADE ON DELETE RESTRICT`——删一档必须先给 `migrateTo`，不让数据库替运营决定徽章归到哪。

**为什么 `user_activity` 是独立表**：`users` 是读路径最热的 JOIN 目标，把计数放进去会让每次发帖/点赞都 touch 一张高频读表的 `updated_at`，污染任何按它做的缓存失效判断。

**六项计数的可重算性不对称**（运维最容易误解）：

| 计数 | 真值位置 | 能否精确重算 |
| --- | --- | --- |
| `like_received` / `like_given` | `content_reactions` 关系行 | **能**（关系行删了就没了） |
| `bookmark_count` | `content_bookmarks`（**数全部行，不按 visibility**） | **能** |
| `days_visited` | `user_visits` 行 | **能** |
| `topic_count` / `comment_count` | 内容行（软删后仍在） | **不能精确**：按 `status<>'deleted'` 会算少，按全量会算多 |

所以 `topic_count` 与资料页实时 `COUNT(*)` 在软删后**故意不一致**（前者是成就，后者是「我现在有多少帖」）。看到不一致不要当 bug 修。

## Dependency Direction

```
bootstrap ─→ Badges ─→ Identity（读 users / actor 校验）
                  ├──→ Support/Jobs（入队评估 / 回扫）
                  └──→ Support/RuleEngine（纯 AST，无反向依赖）
Forum ─╳→ Badges      （Forum 不认识徽章域）
```

`Badges` 不 import `Forum`；`Forum` 声明 `BadgeReader` 接口，由 `Badges` 实现、`bootstrap` 注入。**唯一例外**：`badge_reader.go` 为实现该接口会 import Forum 的 `TopicSummary`/`Comment` 类型——这是「实现别人的接口」所必需，不是先例。

## Grant Sources — 三来源判别式

| 来源 | `granted_by` | `grant_reason` | 触发路径 |
| --- | --- | --- | --- |
| 规则自动 | `NULL` | `''` | `badges.evaluate_user` job |
| 管理员手动 | 操作者 user_id | 操作者填的理由 | `POST /admin/badges/:id/grants` |
| 批量重算 | `NULL` | `'recompute'` | `POST /admin/badges/recompute` → `GrantFromRules` |

幂等靠 `ON CONFLICT (user_id, badge_id) DO NOTHING`，但**两条路径对外语义相反**：手动授予重复是 `409 forum.badge_already_granted`；手动收回不存在是 `200 {"revoked": false}`。自动徽章不可手动收回（`422 forum.badge_manual_only`）——规则下次评估就重发，收回只是短暂假象。

## `top_badge_id` 失真窗口

三个「现象与数据库不一致」的来源，全是设计内行为：

1. **授予后缓存延迟**：帖子旁 chip 最多 20–30s；资料页 ≤10s（不走 Forum 缓存）。两个时限不同，别用同一窗口判。
2. **删最高级徽章后变 NULL**：`ON DELETE SET NULL` 只置空，不会自动挑次高级，直到下一次评估/重算。
3. **停用徽章要触发重算**：`enabled`/`tier`/`position` 变化时对受影响用户重算；改名/换图标/改色**不重算**（不改变「谁被选中」）。

排序规则单源：`postgres_store.go` 的 `topBadgeOrderSQL`——`badge_tiers.position` 升序（0 最高）→ `badges.position` 升序 → `user_badges.granted_at` 降序。**不要**再用 `CASE` 映射 gold/silver/bronze（等级是运营可增删的数据）。每条用它的查询必须自带 `JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`（别名固定 `t`，用 JOIN 不用 LEFT JOIN）：停用一档等价于「这一档不存在」。

## Cache Semantics

| 对象 | 有没有缓存 | 事实 |
| --- | --- | --- |
| `Badges` 的 store | **没有** | 装配是裸 `badges.NewPostgresStore(pool)`，无装饰器；`CachedStore` 全仓只在 `app/Models/Forum` |
| 读模型里的 `BadgeRef` | **有** | 随主题/评论进 Forum 的 `CachedStore`；带 `slug`/`name`/`tier`/`icon` + 等级外观，**actor 无关** |
| `GET /api/v1/badges`（公开目录） | **明确不缓存** | 响应含查看者自己的 `earned`，头 `private, no-store` |
| `GET /profiles/:username/badges`（资料页徽章墙） | **共享缓存 30s** | 实测 `Cache-Control: public, max-age=30` + `Vary: Cookie`。同一句话（`requirementText`）目录页秒更、资料页最长滞后 **30s**——不要按 10s 估算（旧笔记写的 ≤10s 是错的）。该组合对登录用户命中率≈0 且键无界增长，属已知残留 |

`BadgeRef` 可以进缓存是因为 actor 无关；这与 `likedByMe`/`bookmarkedByMe` **相反**（那两个必须逐请求装饰、永不进缓存）。照 reactions 抄成「每请求重算徽章」是错的——列表页每个作者会多一次查询。

**公开目录禁止复用 `setPublicCacheable`**（它写 `public, max-age=30`）：`/api/v1/badges` 对同一 URL 返回不同人的 `earned`，写 `public` 会把 A 的 earned 投给 B。也不靠 `Vary` 补救——控制器路由注册不设 `Vary`。

## Feature Flag And Permissions

- 开关 `features.badges`（`public: true`，推荐开）。关闭后**全部徽章端点** 403 `forum.badges_disabled`、资料页徽章 tab 与帖子旁 chip 消失，**统计采集与统计数字照常**（统计不属于徽章产品面）。
- 权限键 `badge.view`（读后台）/ `badge.manage`（全部写）。**校验在领域层**：`AdminService` 每个方法都收 `identity.Actor` 并自行校验；路由目录里的权限声明只是目录，不是运行时守卫。
- `badge.view` 单独存在是**死权限**：可达性前提是 `admin.access`。

## 管理台徽章页（`/control-panel/badges`，四分区）

四个固定分区（`badges`/`groups`/`tiers`/`grants`），tab 在 URL query `?tab=`，非法值回落 `badges`。几何按 `pages/admin/settings/index.vue` 契约（紧凑标题 + `UDashboardToolbar` + `SFAdminFixedTabNav` + 单内容列），不另起 tab 系统。

- 壳 `pages/admin/badges.vue` 只承担编排：tab/query 同步、三态告警、toolbar。`blocked` 非空时四 tab 一起禁用；**加载失败态不禁用**（留着重试）。
- **唯一数据层实例在壳里**：`useAdminBadges()` 只调一次，`store` 下发四分区。分区里再调一次会得到第二份列表。
- `KeepAlive` 保留的是**组件实例级局部状态**（grants 的选中徽章/页码、groups 的表单草稿、badges 的弹窗开关）；筛选与排序**不是**它的功劳——七个 query 键从 `route.query` 计算。
- `badges` 分区七键筛选/排序全部进 URL（`q`/`group`/`tier`/`grantMode`/`enabled`/`sort`/`dir`），**读侧有白名单归一**：`group` 只认 `/^\d+$/` 或 `'none'`，其余归 `''`。
- `tiers` 分区：CRUD + 调序；**删除必须选迁移目标**；「最后一档」的开关禁用（前后端各拦一道）。
- `grants` 分区：先选徽章再懒加载，来源列按三来源着色，收回只对手动来源行渲染。
- 授予弹窗三态（`granted`/`skipped`/`failed`）**不能合并成一个成功 toast**；`skipped` 是「已持有」不是错误；失败项才给「重试这一批」。
- 规则编辑器（2026-11-12 新增，同日二轮改造）：`components/admin/badges/rule/SFAdminBadgeRule{Editor,Group,Leaf,ParamField,SimpleForm,JsonPreview}.vue` + 纯函数 `utils/admin/badgeRule.ts` / `badgeRuleParams.ts` / `badgeRuleSentence.ts`。编辑器只渲染 `PublishableDefinitions` 下发的目录。
- **回扫进度行（2026-11-12 第一刀）**：`components/admin/badges/SFAdminBadgeBackfillRow.vue` 渲染在 `badges` 分区**筛选区上方一行**（不新增卡片/页头），`data-testid="badge-backfill-progress"`。三态：进行中「已扫描到用户 #N」/ 已完成 / **失败于用户 #N**（失败必须带徽章名 + 用户 id）；刷新按钮 `aria-label`/`title` 齐全；`queued>0` 的 recompute 后自动刷一次、`queued==0`/`undefined` 不刷。端点失败 → `[]` + 可见提示（`admin.badges.backfill.unavailable`），筛选区与表格照常渲染。数据源是 `GET /admin/badges/backfill-status`。

### 条件规则编辑器（2026-11-12 二轮改造）

用户报的两个运营可见 bug 都出在「同一种控件存在两条实现」上，修法统一到**单一实现**。

**1. 参数控件只有一个实现**：`SFAdminBadgeRuleParamField.vue`（306 行）是参数控件的唯一实现，内部是一条 `v-if/v-else-if` 链，按 `kind` 决定 `int`/`enum`/`window`/`objectRef`/`string`/`bool` 控件。`SFAdminBadgeRuleLeaf.vue`（445 → 247 行）与 `SFAdminBadgeRuleSimpleForm.vue`（482 → 332 行）都改为渲染它。

> **不要在任何调用方另起一条条件链。** bug 1 的成因就是 `Leaf` 里的 `objectRef` 分支另起了 `v-if` 链，尾部兜底 `<input v-else type="text">` 对 `enum`/`window` 重复渲染——于是「指标 / 时间窗口」下方各多出一个输入框（显示 `days_visited`、`[object Object]`）。新增参数类型只加在 `ParamField` 里，调用方只传值。

**2. 双模式：简单模式只表达「扁平 all 条件列表」**。`badgeRuleSentence.ts` 提供 `isSimpleRule` / `flattenAllLeaves` / `buildFlatAllRule` / `leafSentence` / `ruleSentence`，纯函数、`t`/`te` 由调用方注入，不 import 组件。`SFAdminBadgeRuleEditor.vue` 持有 `data-rule-mode` 切换、常显的一句话预览，以及在规则含 `any`/`not`/嵌套组时按 `rule.modeAdvancedOnly` 只读提示降级。

**3. `options` 是字符串数组，不是对象**。wire 形状是 `conditionParamDTO.Options []string`（`dto.go:415-423`），与 OpenAPI `BadgeConditionParam.options: array<string>` 一致。前端类型曾写成 `{value,labelZh,labelEn}[]`，于是每个 `<option>` 的 `value` 与文案都是 `undefined`——bug 2「指标下拉点开空白」的直接原因。**测试没抓到是因为夹具自己按对象形状写的**（夹具跟着错类型走，是最难发现那类假绿）。

选项文案走 `badgeRuleParams.ts` 的**三级 i18n 键回退**（均在 `admin.badges` 命名空间下）：`conditions.params.<param>.options.<value>` → `metrics.<value>`（同名指标只写一份） → `conditions.enums.<value>` → 原始值。读入口 `useAdminBadgeConditions.normalizeConditionsPayload` 把 `params[].options` 归一成字符串数组，是唯一的放宽点。

**4. 没有展示位的字段错误不再静默丢失**：`utils/admin/badgeFieldSlots.ts` 的 `FIELD_SLOT_KEYS`（23 行）声明哪些字段有行内展示位；`metric`/`threshold` 等无展示位的字段错误合并进通用提示。

**5. 弹窗去噪音**：删掉顶部「指标 / 阈值」只读两栏与 `form.legacyRuleHint`（连带 `form.metric`/`form.threshold` 三个 i18n 死键）；规则 JSON 改成默认折叠的原生 `<details>`「开发者视图」，复制按钮放在 `<summary>` 行上，折叠态一次点击可达。

**6. 视觉层：单位是行内后缀，删除按钮在卡片头（用户截图报「UI 有问题」后补）**。量真实 DOM 定位三处缺陷：① 带单位的阈值字段比其它字段高 **21px**、label 不齐（`align-items: flex-end` + 单位当第三行渲染）；② 单位「次 / 篇」掉到 input 下方单独一行；③ 删除按钮被 `flex-wrap` 挤到控件行下面，成了孤立图标。修法：`.sf-simple-field__value` / `.sf-rule-field__value` 包住 input + unit（**两种模式同一套语言**，`days_visited` 仍不出单位，因为「访问天数 ≥ 7 次」读不通）；简单模式 `.sf-simple-rule__controls` 改 `align-items: flex-start`，字段按「条件 `2 1 180px` : 比较 `1 1 132px` : 阈值 `0 1 104px`」分层（阈值字段窄、不抢行动力）；删除按钮移进 `.sf-simple-rule__head`，与人话同一行，`.sf-simple-rule__controls` 里**不再放非控件元素**。高级模式同构：`.sf-rule-leaf__head` 仍是单层 flex 行，单位同样走 `.sf-rule-field__value`（组头 `.sf-rule-group__head` 的 `flex-end` 是操作符行，不属这条）。结构锁：**单位必须与 input 同父**、单位不能是字段的直接子元素、删除按钮在头不在控件行——三条都配了正向断言 + 反向确认，把单位改回独立行或把按钮挪回控件行会立刻红。

**本轮三条决策**（不属跨前后端契约变更，记在这里而不单开 ADR）：

1. **简单模式绝不压平 `any`/`not`/嵌套组**。表达不了就降到高级模式并给只读提示——压平会改变判定，等于给不符合条件的人**误发徽章**。这是本模块最贵的一条：任何「为了让两种模式都可用而自动归一」的改动都必须先证明判定集合不变。
2. **`initialMode` 是首次渲染的种子，不随 prop / 规则变化**（`SFAdminBadgeRuleEditor.vue:51` 的 `ref`）。切换模式只改视图，**不改 AST、不 emit**；误用（比如用 watch 同步 `initialMode`）的表现是「运营切到简单模式后规则被悄悄重写」。
3. **叶子根规则的行内错误按「本规则是叶子根」映射到第 0 行**。迁移后的现网徽章（如 `regular-visitor`）根节点就是 `{root:{type:'activity.count',…}}`，此时 `error.path` 是 `[]`（空路径），只有这种情况可当作第 0 行；其余空路径错误走通用提示，否则会指向一条不存在的行。

### objectRef 候选（2026-11-13，`refCandidates`）

**这一节的存在理由**：本模块曾经有一个「看起来能用、实际配不完」的控件活了整整一刀。
计划把 `objectRef` 定义成「有候选列表的引用」，但候选只接了 `fact` 一类，
`category/tag/role/provider/badge` 五类从未接通 → 前端渲染**只有占位 option 的空下拉**
（`admin.badges.conditions.candidatesPending`「候选列表待后端下发」）→
`badge.owned` / `object.authoring` / `identity.role` / `identity.oauth_link`
四条条件的必填参数恒空 → 保存必报「缺少必填参数。」。
决策与未采用方案见 `decisions/2026-11-13-badge-ref-candidates.md`。

**下发面**：`GET /admin/badges/conditions` 现在有三个 additve 顶层键，**三个面不要合并**：
`items`（规则 type 维）/ `factCandidates`（factKey/variant 维）/ `refCandidates`（RefKind 维）。
`refCandidates` 恒五项、固定顺序 `badge → category → tag → role → provider`，
每项 `{kind, candidates:[{value,label,flags}], unavailable, truncated}`。

| 环节 | 实现 | 换写法就埋雷的地方 |
| --- | --- | --- |
| 领域类型 | `conditions/refcandidates.go`：`RefCandidateSet` / `RefCandidateGroup` / `RefCandidate`，`refKindOrder` 是**唯一**顺序定义 | 在 DTO、补齐逻辑、测试三处各写一遍顺序 → 迟早有一处漂 |
| 读口 | `PostgresStore.RefCandidates(ctx)`：四类各一条 SQL（`categories`/`tags`/`roles`/`badges`，`LIMIT 501`），provider 读既有的 `providerNames` 注入 | 走 Forum/Identity service 当候选：分页 + 权限 + 视图模型全绑进来；先例是 `ref_names_postgres.go` |
| 窄接口 | `badges.RefCandidateStore`（`AdminService` 用类型断言） | 往 `Store` 上加方法 → 每个测试 fake 都得实现一份它们不关心的查询 |
| 失败语义 | 单类失败只让该类 `unavailable`；`error` 时**保留部分结果**，端点始终 200，错误只 `slog.Warn` | 整体退化成五项全空，会把一次 tags 抖动放大成「所有候选都没了」 |
| flag | `hidden`（隐藏分类 / 非 active 标签）、`disabled`（停用角色 / 停用徽章）；**标了仍然可选** | 把标记项过滤掉 → 运营无法引用隐藏分类，而「当时发在该分类」是历史事实；禁用 → 让人以为规则写不出来 |
| 上限 | `RefCandidatesLimit = 500` + `truncated`；截断靠 `LIMIT 501` 探测 | 用 `COUNT` 判断截断：多一次查询，还把「恰好 500」与「还有第 501」混为一谈 |
| 控件 | `SFAdminBadgeRuleParamField.vue`：有候选 → `USelectMenu`（`search-input` + `filter-fields=['label','value']` + `value-key`/`label-key`）；候选不可用或为空 → `input[data-ref-manual]` + 提示 | **任何情况下都不许渲染只有占位项的空下拉**——它表达的是「不能选」，而运营需要的是「先手填」 |
| 自引用 | `SFAdminBadgeEditDialog.vue` 用 `excludeBadgeSlug` 剔掉当前徽章自身的 slug | 放到后端做 → 端点得知道「正在编辑哪枚」，签名被绑死；两层都不做 → 规则能存但保存期环检测报 422 |

**`unavailable` 与「空候选」刻意分开记录、渲染相同、提示不同（2026-11-13 残留收口）**：
对运营后果一致（都没有可选项），所以都退文本输入；但**提示文案必须分开**——把正常状态
说成故障会让运营去查一个没坏的后端。读侧是三态纯函数
`refKindMode(map, kind): 'select' | 'unavailable' | 'empty'`（`utils/admin/badgeRefCandidates.ts`），
渲染仍只有两叉，文本框带 `data-ref-manual-state="empty|unavailable"` 供断言定位
（**不要只靠文案字符串断言**）。两句文案：`conditions.refEmptyHint`「该类型目前没有任何候选项…」
与 `conditions.refUnavailableHint`「候选取不到…」；旧的合并键 `refManualHint` 已删除，
`adminBadgesI18n.test.ts` 的「缺键 / 死键」双向断言会拦住残留。

**目录诊断分三条，不许合并（同批收口）**：

| 情况 | 判据（纯函数） | 表现 |
| --- | --- | --- |
| 响应**整个没有** `refCandidates`（老后端） | `refCandidatesMissingKinds` 返回 `[]` | 合法静默：逐类退文本输入，不报任何错 |
| 响应**有数组却缺**目录已声明的 kind | `refCandidatesMissingKinds(items, raw)` 非空（按 `BADGE_REF_KINDS` 顺序） | 规则区横幅 `data-ref-candidates-incomplete`（`conditions.refMissingKinds`，带 `{kinds}`） |
| `refKind` **不在** `BADGE_REF_KINDS` 白名单 | `declaredRefKinds(items).unknown` 非空 | 参数控件出 `data-ref-unknown-kind`（`conditions.refUnknownKind`，带 `{kind}`），**并压掉**那句笼统的「候选取不到」——根因先说话 |

`fact` 不进这两个桶（它走 `factCandidates`，是另一个面）。`refCandidatesInvariantBroken`
仍**只作非 UI 记账**（「整个字段都没有」），不升级成失败：内置条件恒带 objectRef，
老后端必然命中（与 `factCandidatesInvariantBroken` 的方向相反，那边「声称有 fact.count
却没给候选」是真回归）。

**手动徽章的 `rule-impact` 是中性事实，不是错误（2026-11-13）**：`grant_mode='manual'` 时库里
`rule` 恒为 NULL，`AdminService.EstimateRuleImpact` 直接返回
`{estimated:null, method:"unsupported_condition", partial:false, unsupportedConditions:[]}`（200），
不再走 `GetBadgeRule` 的 `ErrBadgeRuleInvalid` → 422。前端**也不问**：判据是纯函数
`shouldLoadRuleImpact({isEdit, badgeId, draftMode, savedMode})`，读**库里**这枚徽章的授予方式，
不是草稿的 `grantMode`（原缺陷就是拿草稿 `automatic` 去问库里 `rule IS NULL` 的徽章）。
呈现上 `status='error'`（红，「获取失败」）与 `status='manual'`（中性，「无法估算」+ `impact.manualHint`）
互斥，`0` / `null` / `manual` / `error` 四态各自独立。
**别去 `GetBadgeRule` 里放宽**：它对 NULL 规则报 `ErrBadgeRuleInvalid` 是「读不到规则」的正确语义，
中性返回只加在知道授予方式的 service 层。

**`forum.badge_rule_mode_conflict`（2026-11-13 新增）**：`grantMode` 与 `rule` 自相矛盾
（`manual` 却带 `rule`）原本与「规则本身不合法」共用 `forum.badge_rule_invalid`，
文案是「请检查指标与阈值的组合」——指标根本没问题，运维会去改错地方。现在分开：

| 码 | 触发条件 | 前端/文案键 |
| --- | --- | --- |
| `forum.badge_rule_mode_conflict` | `POST`/`PATCH` 提交 `grantMode:"manual"` 且 `rule` 非空（创建路径 `normalizeNewBadge`、更新路径 `UpdateBadge` 的 mode×rule 组合校验） | API 文案 `Support/Localization/messages.go`（zh/en）；HTTP 422；`ErrorCode()` 与 `mapBadgeError` 都**排在 `ErrBadgeRuleInvalid` 之前**（`errors.Is` 顺序敏感） |
| `forum.badge_rule_invalid` | `automatic` 没有规则、规则解不开、形状/指标/图标非法 | 同上；这句「请检查指标与阈值的组合」只对回归里的这一类成立 |

判据三条同批有测试：`TestManualBadgeWithRuleIsModeConflict`（三组合 ①mode_conflict ②rule_invalid ③200）、
`TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid`（同时包两条链时 mode 冲突必须胜出）、
真机 `POST` 双码 curl。**新错误码不与 `mapBadgeError` 同批 = 落到 default → 500**（plan §13 #19）。

**`identity.account_age` 的日界（2026-11-13 修，本刀挖出的历史缺陷）**：
旧写法 `GREATEST($2::date - users.created_at::date, 0)` 把**Go 进程本地日**和
`created_at` 在 **PG 会话时区**里的日历日相减——两个日界不同源。后果不是「凌晨几小时」，
而是**永久**：凡注册本地时间落在偏移窗口（CST 00:00–08:00 且 PG 会话 UTC）的用户，
年龄恒多 1 天，30 天门槛提前一天满足。实测：进程 CST、会话 UTC，真实日历差 10 天算出 11。
现在 SQL 只取 `created_at`，折算搬到 Go 的 `calendarDaysBetween`，日界只有一个来源
（resolver 的 `nowTime()` / `today()`）。`calendarDaysBetween` 不用 `Sub/Hours/24`
（DST 日 23/25 小时会把 1 天算成 0 或 1.x），而是各自折成该时区日历日 → 映射 UTC 午夜相减。
**这条与本模块的「按日取界必须 Go 传参」是同一条纪律的两个面**：传参只解决「哪一天」，
不解决「把 timestamptz 折成 date 时用的是谁的表」。
验收教训：**只跑 UTC + Asia/Shanghai 会放过它**——America/New_York（负偏移）才暴露
「日期在会话时区里落到午夜、进程再位移一天」的二阶错误。三时区是硬判据。

**provider 候选与保存期判据必须同源（2026-11-13 修）**：候选来自 registry 的**已声明**快照
（插件刚装、零绑定也在列），保存期若只查 `identity_external_links`（只记「有人绑定过」），
就会出现「候选里唯一那个 provider 必存不进去」——422 还说「可能已被删除」。
两边必须共用 `knownProviders`（绑定表 ∪ 已声明）。**失效语义交给 `rule_health`**，
不要靠收紧保存期来「防错」，那会把运营挡在门外而一个错都没防住。

**USelectMenu 是全仓首用（`@nuxt/ui@4.9`）**：弹层走 reka-ui `ComboboxPortal`（`portal: true` 默认），
teleport 到 body，不受 Modal `overflow` 裁切——这是读组件源码得出的结论，**真实弹层行为仍以浏览器证据为准**。
若某天它在 modal/390 下被裁切且 `:content`/`:ui` 收不回来，退路是「原生 select + 过滤输入框」。

## 回扫进度（`badge_rule_backfill_runs`）的四条硬口径

规则改严触发的回扫是「一页一个 job」的链（`Jobs/Badges/backfill_rules.go`），链断在半路时运营只拿到过入队回执。进度表把它变成可查询的一行——**四条换写法就埋雷**：

1. **只由 job 侧写，绝不从授予数反推**。入队建 `running` 行 → 每页推 `last_user_id` → 末页 `completed`+`finished_at` → 页错 `failed`+`failed_cursor_user_id`。错法：读端点 JOIN `user_badges` 算「已处理人数」——规则不满足时进度会**倒退**，而真实回扫在前进。验收里有一条反向证据：徽章**有授予行但没有 run 行**时 `items` 必须为空。
2. **`failed` 不是「结束」**：失败**不写** `finished_at`（river 还会重试），重试成功要把 `failed_cursor_user_id` 清 0 并翻回 `running`。错法：失败即写 `finished_at`，运营看到「已结束」而队列还在重试。
3. **`Enqueuer == nil` 也要可见**：这一支原来是 `return nil`（静默停在半路），现在落 `failed` + `error`。装配漏接的信号落在 `bootstrap/badges_wiring_test.go`（断言 `Runs != nil`），**不在**生产路径 panic。
4. **`recompute` 的响应形状不动**：`{granted, refreshed, queued}` 恰好三键，进度是**另一个面**。变异依据：把 `queued` 改名即有一条断言红。

**刻意不给 `badge_rule_backfill_runs` 建 `sforum_core_v1` 视图**（AGENTS.md 要求「刻意缺失的读面写明理由」）：这是运维态（谁触发、卡在第几个用户、为什么失败），插件没有任何正当消费面；把它暴露成插件可读的稳定面等于给「谁在回扫谁」发一个契约。插件要读的是**授予结果**（`forum_user_badges`）与**事实累计**（`forum_user_badge_facts`），不是 Core 内部 job 的游标。

## 六个新陷阱（规则引擎这一刀长出来的）

每条都「同环境测不出来」，夹具必须显式构造，dev 库现有数据不算证据。

1. **窗口获赞必须排除自赞**（`r.user_id <> $1`）。累计口径 `activityDeltaForReaction` 对自赞返 `0,0`；窗口版漏了这条，同一枚徽章的「累计」与「最近 30 天」会互相打脸。**P-11「自评论算」不适用于获赞。**
2. **按日取界必须 Go 传参**（`$n::date`），SQL 不许出现 `CURRENT_DATE` / `(now()-interval)::date`。`user_visits.visited_on` 按 **API 进程本地日**写入（`visits.go` 用 `time.Now`，装配无时钟注入），而 `CURRENT_DATE` 是 PG 会话时区。API `TZ=Asia/Shanghai` + PG UTC 时跨零点 8 小时会把刚记的今天排除 → 连续 7 天零点后立刻误判断签。**时间戳比较可以在 SQL 里取 `now()`，日界不行。**
3. **`bookmark_count` 数全部行，不按 visibility**。写路径按 `relationDelta` 计数、`Recompute` 数全部行；`content_bookmarks.visibility` 默认 `private`，只算 public 会让「收藏家」几乎不可达。`object.engagement{bookmark}` 同口径。
4. **`content.hidden` 只认 `topics.status='hidden'`，只统计主题**。`hidden` 是纯管理员动作（`topic.delete_any`；作者自删走 `deleted`）；`deleted`/`pending`/`rejected` 不计；评论被隐藏不计。文案写「被管理员隐藏」，**不许**写「违规/处罚」——库里没有处罚记录（`moderation_reports` 无 action 列、`audit_events` 无内容目标列）。
5. **`content.pinned` 是「当前仍被置顶」**，不是「曾经」。库里只有 `is_pinned` 布尔值，无 `pinned_at`、无审计列。
6. **`identity.role` 用 `roles.key` + `r.is_enabled`**。`key` 有 UNIQUE，`alias` 无 UNIQUE 且可改（改名后规则会静默失效）；`is_enabled` 与授权判定一致。

## M2 收口长出来的四个陷阱（2026-11-12）

跟前面六个一样：都是「测试看着对、运行时静默错」，夹具必须显式构造。

1. **资料页与目录页是两条取数路径，`requirementText` 只在目录页算过**（已修）。`public_catalog` 走 `Badge.Public()`（`types.go:121` 是 `BuildRequirementText` 的唯一调用点），而资料页的 `ListUserBadges` 曾经自己 Scan 一份 `PublicBadge` → 文案恒 `{"zh":"","en":""}`。**键在值空**，任何「键存在 / 类型是 object」的断言都抓不到。判据必须是值级（zh/en 长度 > 0），且单条件与多条件两个方向都验。
2. **建徽章路径（`CreateBadge`）与改徽章路径不是同一套写逻辑**（已修）。`UpdateBadge` 有三态 `CASE WHEN`，`CreateBadge` 当时只 INSERT 老列 → `rule` 与 `requirement_summary_*` 全丢：automatic 撞 `badges_rule_check` 变 **500**、manual 的 summary 静默丢。**加列加字段时先问「创建路径也有这条吗」**，别只改 UPDATE。
3. **UTable 自己有滚动容器，外面再包一层就是两个 scroller**（已修）。`@nuxt/ui` 的 `slots.root` 默认 `relative overflow-auto`；外层再 `overflow-x-auto` 后，外层 `scrollW == clientW`（滚不动）、内层才是真 scroller，而 `tabindex` 落在外层 → **键盘可达性是假的**。正确做法：`<UTable tabindex="0" role="region" :aria-label>`（它 `v-bind="$attrs"` 到 root），保证全页只有一个可聚焦滚动容器。判据要写成「聚焦后 `scrollLeft` 能变」，不能只判 `document.activeElement`。
4. **`queued` 与 `granted`/`refreshed` 是三个不同单位**（已修）。`queued` = **转入异步评估的徽章数**，`granted` = 新插入授予行数，`refreshed` = `top_badge_id` 变化的用户数。前端曾经把它们相加当「影响量」——那会造出一个不存在的数字。文案必须分别说，且 `queued>0` 不能被摘要吃掉。

**影响估算的呈现纪律**（`SFAdminBadgeRuleImpactDialog.vue`）：端点估的是**已保存**规则（`EstimateRuleImpact(ctx, actor, badgeID)`），所以弹窗必须写「按当前已保存规则估算，不是本次编辑的草稿」；`estimated: 0`（读数确实是零人）与 `estimated: null`（无法估算）**不许互相顶替**；`partial: true` 必须追加「只算了部分条件，是上界」；不可估条件要逐个列出（后端 `unsupportedConditions` 只回条件 key），不能只报个数。

## 已知陷阱：`SelectItem` 不接受空字符串 value

**症状**：打开后台徽章页控制台抛 `A <SelectItem /> must have a value prop that is not an empty string.`，四个筛选下拉全坏——**一个 item 抛错会污染整个 `SelectContent` 的 update**。

**根因**：Reka 的 `SelectItem` 在 setup 里 `if (props.value === '') throw`（空串保留给「清空/placeholder」语义）。与 Nuxt UI 无关。

**修法**：哨兵值只活在组件里（`ALL_FILTER = 'all'`，写回前翻成 `''`）。**URL query 契约不变**，也不要在 `filterBadges` 里加 `'all'` 分支——那会把 UI 哨兵泄漏进数据契约。回归锚点：`apps/web/tests/admin/adminBadgesTabs.test.ts` 断言源码不含 `value: ''`。

## 假绿清单（本刀长出来的七种形态）

验收时逐条问一遍；命中任何一条，「全绿」都不能推出「可用」。

| 形态 | 本刀的真实例子 | 怎么防 |
| --- | --- | --- |
| **写死字面量** | M1.1-03 首版写死 `{root,children,0,...}`，在真库上误报 10/10 不等价 | 断言从**数据库当前值**构造，不抄计划示例 |
| **写死实现细节** | 断言依赖「`root` 是组节点」而迁移生成的是叶子；两种都合法，断言只认一种 | 断言语义（生效叶子），不认形状 |
| **只验单点不验多面** | 只验累计获赞会漏掉窗口版的错 | 同一口径的每个变体各一条 |
| **验证对象为空** | 迁移前基线若夹具为空，`should_have_but_missing = 0` 是空断言 | 先断言「迁移前必须非空」，再断言迁移后归零 |
| **断言集覆盖不到横切面** | 徽章那刀 84 条 0 FAIL，但 i18n 键完整性/路由计数/错误码本地化三样都不在断言集里 | 固定横切面清单：i18n 键存在性、目录计数 tripwire、错误码本地化、迁移可逆、开关关闭态、空数据与失败路径 |
| **测试借了环境** | dev 库已迁移，迁移前快照补不回来（M1.1-11/12 长期 pend）；dev 429 限流 | 需要「迁移前」的证据只能在一次性集群重放；429 记 pend 不判 FAIL |
| **只验「该拒的拒了」** | `Validate` 拒绝空组，但 `Eval` 对空 `all` 返回 `matched=true`（P-12）→ 绕过保存校验的脏规则会给全库发徽章 | 拒绝路径与放行路径都要验；求值层要独立兜底，不依赖「调用方应该先 Validate」 |

> 共同注脚：这七种的共同点是**证据看起来像证据**。判据要写成「删掉实现 → 断言必须红」，而不是「跑过了 → 通过」。

## Known Residuals（不要当 bug 修）

> **2026-11-13 收口**：`reports/2026-11-13-badge-ref-candidates-acceptance-run.md` §9-A 的
> R1（两句提示合并）、R2（未知 refKind 静默 + 目录不变量只查数组）、R3（手动徽章 rule-impact 422）、
> R4（mode 冲突用泛码）与 R5 的两条覆盖缺口（A5-c 长列表滚动、A3-b 服务端真失败）**全部已闭**，
> 证据与变异记录在 `reports/2026-11-13-badge-ref-candidates-residuals.md`。
> 下面清单里仍然有效的是与它们无关的条目；本刀新发现的两条是 32 / 33。

1. `Recompute` 的 `granted`（新插入行数）与 `refreshed`（`top_badge_id` 实际变化人数）相互独立，`{0,0}` 是正常幂等结果。
2. `revoked: false` 表示本来就没持有，不是失败。
3. 删当前最高级徽章后 chip 消失直到重算（见失真窗口 2）。
4. `badge.view` 是死权限（见权限节）。
5. 缓存延迟 20–30s（见缓存节）。
6. dev 的 admin 写端点有 IP 限流，连续跑验收脚本会撞 429：**退避重试 → 仍 429 记 pend**，不判 FAIL。
7. `RecomputeAll` 在 HTTP 请求内同步执行（改序/启停/删档三处触发）。规模上来后应改投 River job。**规则触发的回扫已走 job**，不要再叠同步路径。
8. 无自动对账周期 job；统计漂移靠下面两段 SQL 人工核对。
9. 阅读字段 `posts_read_count` / `read_time_seconds` 预留未采集，不进稳定视图。
10. **规则改严不自动收回**已授予（徽章是历史成就，收回只能手动）。UI 与文案都要写明。
11. **影响人数是估算**（AND 取最严叶子、OR 取和），弹窗文案必须写「≈」，`estimated: null` 表示**无法估算**而非 0 人。
    **估算对象是已保存规则**：`GET /admin/badges/:id/rule-impact` 不接受草稿，改规则时弹窗显示的是旧规则的上界（文案已明写）；新建徽章没有 id → 不弹、不编数字。若将来要估草稿，得新增端点（动路由计数 421→422 + OpenAPI + identities 四步走），这是**已评估后暂不采用**的方案。
11b. **`recompute` 的三个数不许相加**：`{granted, refreshed, queued}` 单位不同（授予行 / 用户 / 徽章）。`queued>0` 时 toast 必须说「N 枚徽章已转入后台评估」，全 0 时返回 `undefined`（正常幂等，不编假成功）。
11c. **后台表在 390 下靠单一滚动容器 + 可聚焦 region**：`scrollW 508 / clientW 326`、聚焦后 `scrollLeft` 能到 182px，操作列才够得到。别再在外面包第二层 `overflow-x-auto`（外层滚不动，键盘白给）。
11d. **建徽章路径必须与改徽章路径同口径**：`CreateBadge` 的 INSERT 要写 `rule` / `requirement_summary_zh` / `requirement_summary_en`；「automatic 必须有规则」的 422 判定在服务层（`normalizeNewBadge`），不要退回 `validateBadgeInput` 里靠 legacy `metric`/`threshold` 兜。
12. **`site.timezone` 全仓除 Options 包外零消费**：`visited_on` 的日界是 API 进程本地时区。站点级缺陷，影响所有按日统计的功能，**本刀不修**，应单开一刀。
13. 插件 RPC 判定器（A 路径）未做；`fail-closed` 目前只覆盖「事实查询失败」。
14. 查看类 / 采纳精华热榜 / 举报类条件未做（分别缺 `user_topic_reads`、Forum 域字段、审核数据不进公开面）。
15. trust levels 未共用引擎（引擎已独立成包，可平移）。
16. ~~公开面自然语言是双列（zh/en）；只填一种时另一种回落规则自动生成句。~~ —— **已被 2026-11-13 取代**：
    公开说明只由服务端生成，运营不再填任何一种语言（见「获得条件的自动解释器」节）。保留原条目只为对照旧口径。
17. ~~`PublishableDefinitions(factKeys []string)` **不携带 variants**~~ —— **M3 已闭**：签名落成 `PublishableDefinitions(candidates []FactCandidate)`，候选自带 `Variants`（见扩展面段）。保留原条目以便对照当时的 M2 约束。
18. 等级改名/改色的缓存延迟：帖子 chip 20–30s、资料页 ≤10s。
19. 批量授予单次上限 50 人（`admin.badges.grant.search.max`）；候选搜索上限 20 条是另一回事。
20. 等级 `key` 不可变（`PATCH` 只改展示字段）。
21. 「最后一档」保护只防误点，不防对抗性并发（跨档窄窗口，见 `tiers_store.go` 注释）。
22. 新建等级缺省 `position` 走 `TierPositionAppend`（-1）由 store 解析成末尾——**不要把「没传」当 0**，0 是最高位次。
23. `SFComment.vue` 仍在 500 行以上（本模块只抽了 `SFCommentAuthorRow`）。
24. **`rule_health` 的 `label` 是整屏中文硬编码**：`RuleIssue` 只有 `Kind`/`Path`/`Ref`/`Label`，
   没有 locale 维度，英文界面下用户看到中文（靠 `kind`/`path`/`ref` 定位）。修法要给
   `RuleIssue` 加 `labelKey` + 前端 i18n 文案表，属跨前后端契约变更 → 独立开单，别在本刀顺手加字段。
25. **`badgeConditions` 的 `rule #1`（非 plugin 类型）不可达**：theme 带 backend 会先被主题校验拒，
   走不到这条分支。按纵深防御保留，测试只能断言「Go 拒绝」，**拿不到 `rule #1` 编号**
   （spec §10 Errata 4）。
26. **`badgeConditions.key` 在 Go 侧比 schema 更严**：Go 复用 `manifestIDPattern` → 上限 **81** 字符，
   而 schema 与 `badge_facts.fact_key` 的 CHECK 都是 **121**。82–121 字符能过 schema 与 DB，但会被
   manifest 拒 —— 安全侧偏差，已在 `v3_badge_conditions.go` 的注释写明，不要为「对齐」而放宽 Go 侧。

27. **`fact.count` 的句子不带计量单位**：单位只在 `conditions.FactCandidate.UnitZh/UnitEn`（插件清单里声明），
    而 `PhraseFunc` 的入参是 `(params, cmp, names RefNames)`、`RefNames.Fact` 只有双语 label ——
    `phraseFactCount` 拿不到单位，所以公开句写「『点赞』累计 ≥ 3」而不是「≥ 3 次」。
    **别把「字段里显示单位」当成这条已修**：输入框单位是前端视觉层，句子是服务端生成器。
    要带单位得把单位并进 `RefNames` 或给 `PhraseFunc` 加候选入参（契约变更），别在 `phrases.go` 里硬编码单位表。

28. **`object.engagement.topicId` 仍是 int 自由数字**（主题不可下拉）：主题数量无上限，
    候选列表做不出「有用的下拉」，而帖子 ID 由运营从 URL 抄最直接。这一点与 objectRef 其余
    四类不同，**不要为了「统一」把它改成 objectRef**。
29. **`fact.count` 的 `factKey`/`variant` 仍用原生 `<select>`**，未跟进 `USelectMenu`：
    它的候选带双语 label 与 variants（M3 已验收），本刀刻意不碰以免重开那一刀的验收面。
    控件族统一（四类 objectRef + fact 都用一个可搜索控件）是下一刀的事。
30. **候选是后台读面，刻意不进 `sforum_core_v1`、不新增 HostQuery**：插件没有消费面
    （插件读规则走 `forum_badges` 视图即可）。若将来有插件要枚举「分类候选」，先想清楚
    它要的是**规则可选值**还是**站点分类列表**——后者应该走 Forum 的公开/查询面，别复制这里。
31. **`badge.view` 持有者可见隐藏分类/标签名、全部角色 `key+alias`、全部徽章 `slug+name`（含停用）、
    已声明 auth provider 的 `id+label`**。这是「配规则必须知道可选什么」的代价，**不含**凭据与用户数据。
    收紧权限时不要只改路由守卫——领域层 `BadgeConditions` 自己就校验 `badge.view`。
28. **指标清单仍是多处同源手抄（2026-11-13 起少一处）**：`AUTO_SUMMARY_METRICS` 已随手填口径删除，
    剩下的手抄点是 `adminBadgesI18n.test.ts` 的指标常量与服务端条件表（`conditions/registry.go` 的
    `activity.count` 六个 `metric` 枚举）。改指标名仍要两地同改，应收敛到单一来源（生成物或 OpenAPI 派生）。
29. **`normalizeConditionsPayload` 会静默丢弃非字符串 `options`**：后端若真把 `options` 改成对象型，
    表现是**空下拉**而不是显式报错；眼下唯一兜底是 OpenAPI 契约 + 测试。改 wire 形状时先改这里。
30. **`Badges/conditions` 有两条在干净 HEAD 也红的日期敏感测试**：`TestIdentityConditions` 与
    `TestActivityStreakEndsTodayOrYesterday`。与本刀（解释器）无关，终审已复核；**别为了跑绿去改断言**，
    要单独修（真实原因是「按日取界」对系统时钟/时区的依赖，与残留 12 的 `site.timezone` 同源）。

32. **鉴权层与 `roles` 表强耦合，导致「单类候选失败」无法用改表名注入**（2026-11-13 实测）：
    `ALTER TABLE roles RENAME TO roles_qa_tmp` 之后 `GET /api/v1/profile` 直接 500
    （`identity.sql` 的 `ListUserRoleKeys` / `ListUserPermissions` 每请求都 `JOIN roles`），
    于是「只有 role 组 unavailable、其余照常」在这条注入下结构上不可达。等价注入是
    「改名 + 缺 `alias` 列的同名视图顶替」：鉴权只读 `id/key/is_enabled`，照常工作；
    只有候选那条 `SELECT key, alias, is_enabled FROM roles` 报 42703，
    端到端就能看到 `role unavailable:true` 而其余四类照常有值（证据：
    `reports/2026-11-13-badge-ref-candidates-residuals.md` §5.2）。**别再把「改表名」当成
    「制造单类故障」的通用手法**——先确认鉴权路径读了哪些表。
33. **手动徽章的 `rule-impact` 中性返回只加在 service 层**（见上「objectRef 候选」节）：
    `PostgresStore.GetBadgeRule` 对 NULL 规则仍返回 `ErrBadgeRuleInvalid`。那是「读不到规则」
    的正确语义，**不要为了「统一语义」在 store 里放宽**——放宽后任何调用方都拿不到
    「这条规则不存在」这个信号了。

## Reconcile SQL（对账靠这两段）

```sql
-- 1) 关系行三项 + 访问天数：可精确对账，任何差异都是漂移
--    注意列名是 topics.author_user_id / comments.author_user_id（不是 author_id）
WITH truth AS (
  SELECT u.id AS user_id,
         COALESCE((SELECT count(*) FROM content_reactions r
                     JOIN topics t ON t.id = r.target_id
                    WHERE r.target_type = 'topic' AND r.reaction = 'like'
                      AND t.author_user_id = u.id), 0)
       + COALESCE((SELECT count(*) FROM content_reactions r
                     JOIN comments c ON c.id = r.target_id
                    WHERE r.target_type = 'comment' AND r.reaction = 'like'
                      AND c.author_user_id = u.id), 0) AS like_received,
         COALESCE((SELECT count(*) FROM content_reactions r
                    WHERE r.user_id = u.id AND r.reaction = 'like'), 0) AS like_given,
         COALESCE((SELECT count(*) FROM content_bookmarks cb
                    WHERE cb.user_id = u.id), 0) AS bookmarks,
         COALESCE((SELECT count(*) FROM user_visits v WHERE v.user_id = u.id), 0) AS days
  FROM users u
)
SELECT ua.user_id,
       ua.like_received_count, truth.like_received,
       ua.like_given_count,    truth.like_given,
       ua.bookmark_count,      truth.bookmarks,
       ua.days_visited,        truth.days
FROM user_activity ua JOIN truth ON truth.user_id = ua.user_id
WHERE ua.like_received_count <> truth.like_received
   OR ua.like_given_count    <> truth.like_given
   OR ua.bookmark_count      <> truth.bookmarks
   OR ua.days_visited        <> truth.days;

-- 2) 授予 vs 规则：找出「按当前规则该有却没有」的用户（重算会补的就是这些）
--    2026-11-12 起 rule 是唯一判定来源；纯 SQL 只能覆盖「单 activity.count + window.all + gte」
--    这一种形状，复杂规则必须走 Go 侧求值（这条 SQL 仅作快路径对账）。
SELECT b.slug, ua.user_id
FROM badges b
JOIN user_activity ua
  ON b.enabled AND b.grant_mode = 'automatic'
 AND b.rule->'root'->>'type' = 'activity.count'
 AND b.rule->'root'->'params'->'window'->>'kind' = 'all'
 AND b.rule->'root'->'cmp'->>'op' = 'gte'
 AND CASE b.rule->'root'->'params'->>'metric'
       WHEN 'topic_count'         THEN ua.topic_count
       WHEN 'comment_count'       THEN ua.comment_count
       WHEN 'like_received_count' THEN ua.like_received_count
       WHEN 'like_given_count'    THEN ua.like_given_count
       WHEN 'bookmark_count'      THEN ua.bookmark_count
       WHEN 'days_visited'        THEN ua.days_visited::bigint
     END >= (b.rule->'root'->'cmp'->>'value')::bigint
WHERE NOT EXISTS (
  SELECT 1 FROM user_badges ub WHERE ub.user_id = ua.user_id AND ub.badge_id = b.id
);
```

`topic_count` / `comment_count` 的差异要按「含软删」口径解释，不要直接判为漂移。

## Extension Surface

| 视图 | HostQuery | 结果 schema |
| --- | --- | --- |
| `sforum_core_v1.forum_badges` | `sforum.core.public_badges.list` | `sforum.core.public_badge` |
| `sforum_core_v1.forum_user_badges` | `sforum.core.public_user_badges.list` | `sforum.core.public_user_badge` |
| `sforum_core_v1.forum_user_activity` | `sforum.core.public_user_activity.list` | `sforum.core.public_user_activity` |
| `sforum_core_v1.forum_user_badge_facts` | `sforum.core.public_user_badge_facts.list` | `sforum.core.public_user_badge_fact` |

四个视图都 `security_barrier` + 按 `users.status='active'` 过滤。`forum_badges` 用 `WHERE badges.enabled` 过滤，并输出 `rule` + `requirement_summary_{zh,en}`（`metric`/`threshold` 保留一版）。事实视图**只输出累计值**：不输出 delta 行、不输出 `dedupe_key`、不输出 `source_extension_id`。

### 插件事实条件（M3）

`badgeConditions[]` **不是新条件类型**：它是给 Core 已有的 `fact.count` 注册合法 `factKey`
与 `variant` 候选。规则叶子恒为 `{"type":"fact.count","params":{"factKey","variant","window"}}`，
引擎白名单仍是 13 条，不随插件数量增长。

| 层 | 落点 | 行为 |
| --- | --- | --- |
| 清单校验 | `Support/ExtensionManifest/v3_badge_conditions.go` | 仅 `type:plugin` + Protocol V2 backend；`≤32` 条、`variants≤16`；`compare` 非空且 ⊆ 五 op（无 `ne`）；`contractVersion == key@N`；`label`（给了 `unit` 时同）双语必填 |
| 候选模型 | `Models/Badges/conditions/facts.go` | `FactCandidate` / `FactVariant` / `FactDeclaration` / `MergeFactDeclarations` / `FactConflict`；输出按 `Key` 排序（下拉顺序要稳定） |
| 合并规则 | 同上 `MergeFactDeclarations` | 只取**已启用**插件（`StatusEnabled` + `TypePlugin`）；三条拒绝：与 Core 条件重名、缺 `<id>.` 前缀、重复 key（保留第一条，其余进 `FactConflict`） |
| 候选来源 | `bootstrap/badges_fact_catalog.go` | 5s TTL 快照；读失败**回退旧快照**（一次 DB 抖动不该让全站插件条件变 unknown）；冲突只 `slog.Warn`，不覆盖 |
| 发布面 | `conditions.Registry.PublishableDefinitions(candidates)` | 有候选才发布 `fact.count`（12 条内置恒在）；HTTP `items` + **additive** `factCandidates`，两个面不混 |
| 保存期 | `rule_store.go` `checkRuleReferences` | `factKey` 无候选**允许保存**（规则是运营资产）；`variant` 拼错 → `forum.badge_rule_reference_invalid`（`conditions.VariantDeclared`） |

`variant` 是**引擎开放集 + 域层兜底**：`fact.count` 的 `variant` 声明为 `ParamEnum` 且
`Options == nil`，引擎只查形态，成员资格由 `Models/Badges` 按当前候选判。`RuleEngine`
里 `nil` 与 `[]string{}` 语义不同（开放集 vs 封闭空集），不要混。

**Host 命令 `badges.fact.record`**（`Support/HostAPI/v2_command_badges.go`）：`ID` 不带 `@N`、
`Version` 是独立字段 `1`、schema ref `sforum.badges.fact.record.{input,result}@1`、
PolicyId `sforum.badges.fact.record@1`、`RequiredCapability = badges.fact.write`、actorless service。

| 项 | 口径 |
| --- | --- |
| 输入六字段 | `userId` / `factKey` / `variant` / `delta` / `occurredAt` / `dedupeKey`；`userId` 与 `delta` 都是**十进制字符串**（BIGINT 与 64 位整数不过 protobuf `Struct` 的 `double`） |
| 六个 reason（逐字） | `capability_denied`、`fact_key_not_declared`、`invalid_user_id`、`invalid_delta`、`invalid_occurred_at`、`fact_write_failed` |
| 审计分层 | `capability_denied` 由共享能力闸门在事务**前**返回，**不落审计**；其余五个域层拒绝落 `extension.badge_fact_record.denied`（best effort，审计失败不改返回码） |
| 幂等 | `dedupeKey` 非空才幂等：命中部分唯一索引 → `inserted:false`（正常输出，不是错误）；**空串两次都插** |
| 计数 | 回执 `total` clamp 到 0；存储 `user_fact_counters.total` **保留真实负值**（扣减没被吞） |

**事件转事实**：插件在 `events[]` 声明 observer，在 handler 里调 SDK
`Host.RecordBadgeFact`（`BadgeFactInput{UserID int64, FactKey, Variant, Delta int64, OccurredAt, DedupeKey}`，
wire 上 `userId` 是字符串）。**不新增 core 事件订阅总线** —— 这是计划里 C 路径的等价实现。
observe 至少投递一次，幂等真值位置是 `dedupeKey`（用业务标识派生，不要用时间戳）。

**卸载语义**：规则保留 → `fact.count` resolver 返 error（`resolve_facts.go`）→ 引擎 `unknown`
（`Matched=false, Incomplete=true`）→ `rule_health` 出 `missing_fact_key` → 同 key 重装**自动恢复**
（候选实时合并，不需要回填 job）。**不静默删/改规则**：禁用/卸载链路上没有任何写 `badges` 的代码，
`rule_health` 只读；失败语义的证据是 `reports/2026-11-12-badge-m3-manifest-acceptance.md` 的
D-01（未声明 → unknown 成对）与 D-02（variant 三态，含「卸载后仍可存」）。

通知类型 `badge_granted`（category `engagement`，站内默认开、邮件/推送默认关），邮件模板键 `forum.badge_granted`。管理员给自己授予**会**发通知——徽章是公开荣誉，不是互动噪音（与自赞跳过的口径不同）。

## Important Paths

- 规则引擎：`apps/api/app/Support/RuleEngine/`（`types` / `decode` / `validate` / `eval` / `catalog`）
- 条件与规则：`apps/api/app/Models/Badges/conditions/`（`registry` / `batch` / `phrases`（13 条双语措辞）/ `resolve_{activity,object,identity,content,facts}`）、`rule_store.go`、`rule_eval.go`、`rule_health.go`、`rule_impact.go`、`facts_store.go`
- 公开说明生成：`requirement_text.go`（`BuildRequirementText` / `BuildRuleRequirementText` 唯一入口）、`requirement_text_render.go`（树遍历 + 压缩 + 降级）、`ref_names.go`（`CollectRefs` / `RefSet` / `RefNameLoader` / `ResolveRefNames`）、`admin_requirement_text.go`（预览端点域层）
- 领域：`apps/api/app/Models/Badges/`（`types.go` / `service.go` / `admin_service.go` / `evaluate.go` / `postgres_store.go` / `activity_store.go` / `visits.go` / `badge_reader.go` / `tiers_store.go` / `tiers.go` / `grant_candidates.go`）
- 回扫 job：`apps/api/app/Jobs/Badges/backfill_rules.go`
- HTTP：`apps/api/app/Http/Controllers/Badges/`（`controller.go` 路由 + `dto.go` wire 形状唯一来源 + `rule_controller.go`）
- 装配：`apps/api/bootstrap/badges_wiring.go`、`apps/api/app/Providers/badges.go`
- 前端（论坛面）：`components/profile/SFProfileBadgeTab.vue`、`components/forum/badges/SFBadgeCard.vue`、`components/forum/SFBadgeChip.vue`
- 前端（后台）：壳 `pages/admin/badges.vue`；四分区 `components/admin/badges/tabs/SFAdminBadges{Badges,Groups,Tiers,Grants}Tab.vue`；共享 `SFAdminBadge{Table,EditDialog,GrantDialog,UserPicker}.vue`；规则区 `SFAdminBadgeRuleSection.vue`（从编辑弹窗抽出）；规则编辑器与影响弹窗 `components/admin/badges/rule/{Editor,Group,Leaf,ParamField,SimpleForm,JsonPreview,ImpactDialog}.vue`；数据层 `composables/admin/badges/{useAdminBadges,useBadgeRuleDraft,useBadgeRuleTextPreview,useAdminBadgeConditions,useAdminBadgeRuleHealth,useAdminBadgeRuleImpact}.ts` + 纯函数 `utils/admin/{adminBadges,badgeRule,badgeRuleParams,badgeRuleSentence,badgeFieldSlots}.ts`
- 前端（公开面）：`components/forum/badges/{SFBadgeCard,SFBadgeCatalogPage}.vue`、`components/profile/SFProfileBadgeTab.vue`、`utils/forum/forumBadge.ts`（`forumBadgeRequirementText` 是公开面唯一句子解析入口，只选语言、不生成句子）
- 验收与证据：`scripts/verify-badge-rules-acceptance.sh`（值级双向，`requirementText` 按规则形状取样）、`knowledge/reports/2026-11-12-badge-m2-remaining-acceptance.md`、`knowledge/reports/2026-11-12-badge-m2-visual-qa.md` + `qa-2026-11-12-badge-m2/`
- 配色：`apps/api/app/Support/Palette`（9 色唯一来源）+ `apps/web/app/utils/theme/tierColor.ts` + `assets/css/sforum-trust-colors.css`
- 契约：`contracts/openapi/paths/badges.yaml`、`contracts/openapi/schemas/badges.yaml`

## Open Questions / Next

- **M2 已结项（2026-11-12）**：五项落实，两处契约变更（后台 wire 增 `rule`/`requirementSummary`、UI 身份 4→6）见计划 §15.2。
- **M3 插件扩展已收口（2026-11-12）**：契约冻结在 `../reports/2026-11-12-badge-conditions-manifest-spec.md`
  （含 Errata 1–4），实现与扩展面见本文件「插件事实条件（M3）」节，文档面向作者的是
  `../../docs/extensions/badge-plugin-facts.md`。三处硬判据仍然成立：`key` pattern 与
  `badge_facts.fact_key` 的 CHECK 逐字一致、`contractVersion == key@N`、未声明 factKey 返 error 而非 0。
- ~~**`M1-EXIT-PERF` 仍未取证**~~ —— **已闭（2026-11-12）**：10k 规模（1,999,800 行）已真跑，`PERF-01` 走 `Index Scan using content_reactions_roster_idx`（0.189ms）、`PERF-03` p50 2.19ms / p95 3.47ms（预算 50ms）；见计划 §15「收口遗留」。
- **`site.timezone` 零消费**（残留 12）是站点级缺陷，影响所有按日统计的功能，应单开一刀，
  不要塞进徽章刀。
- ~~**`PublishableDefinitions` 的 variants 签名**（残留 17）留到 M3~~ —— **已闭**：
  落成 `PublishableDefinitions(candidates []FactCandidate)`，候选自带 `Variants` 与双语 label/unit。
- 条件目录若要扩（查看类 / 采纳精华热榜 / 举报类），先确认数据面是否存在——
  三者分别缺 `user_topic_reads`、Forum 域字段、以及「审核数据不进公开面」的既有裁决。
- **二轮编辑器 UX 的窄屏证据只有结构级（2026-11-12）**：390×844 实机未验（当次浏览器载体不支持
  视口模拟），键盘可达性同理。**后续那次 340px 是「行宽模拟」，只证明行内几何，不等于 390×844 视口**——
  别把它当中窄屏证据划掉本条。桌面侧已在真实 dev server（`:3000` + `:8080`）验过：简单模式
  「累计访问天数 ≥ 7」、指标下拉 6 项全中文、指标与时间窗口各 1 个控件、无 `[object Object]`、
  模式来回切换规则 JSON 字节稳定、无横向溢出；340px 行宽下控件按「条件 → 比较 / 阈值 → 指标 / 时间窗口」
  三行落、行内 `scrollWidth == clientWidth`、删除按钮 y 不随换行变化。
