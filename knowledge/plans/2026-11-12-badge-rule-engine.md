# 徽章规则引擎（条件表达式 + 插件事实层）

Status: **implemented（2026-11-12，M1 + M2 + 第一刀（回扫进度可见）+ M3 全部完成并独立验收；FAIL 0）**
上游：`2026-11-10-badges-and-user-activity.md`（数据域与口径）、`2026-11-11-badges-public-catalog.md`（公开面）
消费方：backend 老后（M1）、frontend 小前（M2）、reviewer 小审（验收矩阵）、researcher 小研（落点核实）

本刀把 `badges` 从「单指标 ≥ 阈值」升级为「布尔条件树 + 插件事实层」。19 项用户裁决见 §0，
任何一条在实施中想改，必须回来改这份文件并重新拍板。

---

## 0. 用户已拍板的 19 项决策

| # | 决策 |
| --- | --- |
| 1 | 表达能力：**完整布尔表达式树**（AND / OR / NOT 任意嵌套 + 括号） |
| 2 | v1 条件范围：计数 6 指标、时间窗口/连续、定向对象、身份/账户、质量类**只做已有字段**（置顶/隐藏） |
| 3 | 采纳 / 精华 / 热榜：**留到下一刀**（需 Forum 域新增字段） |
| 4 | 举报类条件：**不做**（审核数据不进徽章） |
| 5 | 计数口径：每个条件自带窗口，`累计` 或 `最近 N 天` |
| 6 | 追溯：保存即异步回扫存量用户，保存前弹窗告知预计影响人数 |
| 7 | 查看类：**v1 不采集**，不建 `user_topic_reads`，条件槽位保留 |
| 8 | 规则存储：表达式树 JSON，落 `badges.rule` 列 |
| 9 | 插件机制：B + C（事实表主干 + 事件转事实），A 回调判定器留 v2 |
| 10 | 事实形态：事实行 + 聚合计数列 |
| 11 | 编辑器元数据：插件在 manifest 声明条件元数据，后台自动渲染 |
| 12 | 失败策略：fail-closed（条件为假，不授予，记可观测事件） |
| 13 | 编辑器：可视化树 + 只读 JSON 预览 |
| 14 | 公开面：运营填的自然语言描述 + earned 状态，不暴露 AST |
| 15 | 旧字段：迁移转规则，`metric`/`threshold` 降为 legacy 只读，下一刀删 |
| 16 | 引擎独立成包，本刀只接徽章，trust levels 不动 |
| 17 | 对象引用：稳定标识 + 失效即不满足 + 后台健康提示 |
| 18 | 分期：M1 引擎 → M2 编辑器 → M3 插件扩展 |
| 19 | 事实层覆盖：混合（Core 条件读真值表，事实表只服务插件） |

## 1. 目标与验收标准

**目标**：`badges` 从「单指标 ≥ 阈值」升级为「布尔条件树」，条件可指向具体对象（主题/分类/标签）与身份事实（含 OAuth2 绑定），并让插件通过事实行贡献自定义条件；同时保证现有内置徽章行为逐人一致（**10 枚自动**逐人等价 + **2 枚手动**行为不变）。

**验收**（M1 结束时必须全部成立）：

1. 迁移后对全库用户跑一次评估，授予结果与迁移前逐人一致（快照比对 `user_badges` 与 `users.top_badge_id`）。
2. 通过 API 建一枚 `A AND (B OR C) AND NOT D` 的自动徽章，用户达标后由 `badges.evaluate_user` 正确授予，且重复评估幂等。
3. 保存规则触发的回扫走 River job，HTTP 请求 P99 不因规则复杂度线性增长。
4. 迁移 up/down 由 `scripts/verify-migration-reversible.sh` 覆盖；含多条件规则时 down 明确拒绝并给 HINT。
5. 新增错误码在 zh/en 两份 `Localization/messages.go` 里都有文案（模块笔记陷阱 #13）。
6. 路由计数 tripwire 同步更新（陷阱 #12）。
7. `./scripts/test.sh --fast` 三线 exit 0；`node tests/validate-architecture-boundaries.mjs` 0；`ruby scripts/validate-openapi-refs.rb` 0。

## 2. 现状与硬约束（读码核实，2026-11-12）

- `badges` 现为 `grant_mode` + `metric` + `threshold`，`badges_rule_check` 与 `badges_metric_check` 把形状写死在库里；`evaluate.go:ruleSatisfied` 只做 `counters.Value(metric) >= threshold`。
- `user_activity` 六项计数已有；`user_visits(user_id, visited_on)` 可算连续天数。
- 窗口计数索引已够用：`topics_author_created_idx`、`comments_author_created_idx`、`content_reactions_user_idx`。**窗口「获赞」没有作者侧索引**，只能按「本人主题 → 逐主题索引查找」，成本随本人主题数增长，需在 M1 用 1 万用户夹具实测。
- 定向对象事实源齐备：`content_reactions`、`content_bookmarks`、`comments(topic_id)`、`topics(category_id)`、`topic_tags`。
- 身份事实源齐备：`users.created_at`、`users.email_verified_at`、`identity_external_links(provider_id,status='active')`、`user_roles`+`roles`、`user_profiles.avatar_attachment_id`。
- 质量类只有：`topics.is_pinned`、`topics.status IN ('hidden','deleted')`。采纳/精华无字段。
- **事件总线没有 core 侧 Subscribe 面**（`Support/Events` 只有 Emit / Definition / 插件 listener 投递）。因此决策 9 的「C 事件转事实」落地为：**插件在自己的 observe handler 里调用一行 Host 命令**（SDK 封装），不新增订阅总线。这是对选项文案的诚实修正。
- 仓库硬约束：新增 Core 数据域必须同批交付 `sforum_core_v1` 只读视图 + 至少一条 HostQuery（含 allowed/denied 测试）；插件数据只走 view/query/command/event 四个面。
- **路由计数现为 418**（`Support/Routes/registry_test.go` 的 `len(catalog) != 418`，滚动增量账在 487–513 行）。本刀加 3 条后台路由 → 421，且必须同批改 `catalog-identities.json`、OpenAPI，并重生成 `core_catalog_gen.go`，否则 `core_catalog_gen.go` 与目录不一致会红。
- **稳定身份登记的四步顺序（2026-11-12 实测修正）**：① Go 路由注册（`core_catalog_gen.go` 由目录生成）→ ② `docs/extensions/v3/catalog-identities.json` 的 `routes` 数组加条目（`{id, contractVersion, method, path}`，`path` 与 Go 注册逐字一致）→ ③ `node scripts/v3-catalog/generate.mjs` → ④ `registry_test.go` 计数 418→421（与 `routes` 数组条数对齐）。
  **路由清单的来源是 Go 路由目录，不是 OpenAPI**——报错 `new or moved route needs a reviewed stable identity mapping` 的真实含义是「Go 侧有这条路由，但 identities 的 `routes` 里没登记」，**看起来像缺配置文件，实际是缺一条登记**。
  **OpenAPI 是独立的一步，不能省**：`tests/validate-docs.mjs` 的 `validateDocumentedHttpEndpoints` 只校验「**被文档提到的**端点必须同时出现在 OpenAPI 与 Go 路由目录里」，所以**新增路由缺 OpenAPI 不会红任何门禁**——这正是不写就会漏掉的原因。
- **可逆脚本是物理跑**：一次性 PG → 全链 up → 只回滚「最新已应用那条」→ 逐项断言对象消失（不符 `exit 6`）→ 再 up → 断言重建。新迁移必须显式传 `SFORUM_MIGRATION_TARGET_VERSION=202611120002` 并新增 `elif` 分支，否则落进 `else` 的旧徽章断言、报假失败。
- **卡片抽取已完成，本刀不重做**：`18cbc5e06`（2026-11-11 14:32）已把卡片抽成 `apps/web/app/components/forum/badges/SFBadgeCard.vue`（309 行），`SFProfileBadgeTab.vue` 降到 253 行，两个 variant 都经 `:variant` 走同一张卡，props/emit 与抽取前逐字一致。模块笔记残留 #15 的「514 行 / 未抽」是过期数字（已修）。M2 直接在现有卡片上接 `requirementText`。

## 3. 架构总览

```
规则定义 badges.rule (JSONB)
        │
        ▼
Support/RuleEngine        纯 AST：Decode → Validate → Eval(Resolver)
        │                                    ▲
        │ Resolver 接口                       │ 取值
        ▼                                    │
Models/Badges/conditions   条件类型注册表 + 每类解析器（批量取数，按类型一次查询）
        │                                    │
        ├── 真值表（user_activity / topics / comments / reactions / bookmarks /
        │           identity_* / user_roles / user_profiles；moderation 不接）
        └── badge_facts + user_fact_counters（插件事实）
                                             ▲
                     Host 命令 sforum.badges.fact.record@1（M3）
```

## 4. 数据模型（迁移 `202611120002_badge_rules.sql`）

### 4.1 `badges.rule`

```sql
ALTER TABLE badges ADD COLUMN IF NOT EXISTS rule JSONB;
ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_zh TEXT NOT NULL DEFAULT '';
ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_en TEXT NOT NULL DEFAULT '';
```

- 迁移把 **10 枚**自动徽章转成单条件规则（`activity.count` + `window.all` + `gte`）。12 枚内置里 `community-contribution` / `quality-answer` 是 **manual**，不转、`rule` 保持 NULL——「12 枚逐人一致」的验收口径是「10 枚自动等价 + 2 枚手动行为不变」，不是「12 枚都被转换」。
- 删除 `badges_rule_check`，改为：`automatic → rule IS NOT NULL` / `manual → rule IS NULL`。
- `badges_metric_check` 与两列**保留但只读**（legacy 回滚依据，下一刀删），加 `COMMENT ON COLUMN` 写明「写路径不再维护」。
- 部分索引 `badges_enabled_auto_idx` 保留（快路径仍按它扫）。

### 4.2 插件事实

```sql
CREATE TABLE badge_facts (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL CHECK (fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$'),
  variant TEXT NOT NULL DEFAULT '',
  delta BIGINT NOT NULL CHECK (delta <> 0),
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source_extension_id TEXT NOT NULL DEFAULT '',
  dedupe_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX badge_facts_dedupe_idx
  ON badge_facts (user_id, fact_key, variant, dedupe_key) WHERE dedupe_key <> '';
CREATE INDEX badge_facts_window_idx
  ON badge_facts (user_id, fact_key, variant, occurred_at DESC);

CREATE TABLE user_fact_counters (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '',
  total BIGINT NOT NULL DEFAULT 0,
  last_occurred_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, fact_key, variant)
);
```

写入恒为同一事务内的 `INSERT fact` + `INSERT ... ON CONFLICT DO UPDATE total = total + delta`；`total` 允许为负，读侧 `GREATEST(total, 0)`。

### 4.3 回滚支持表

```sql
CREATE TABLE badge_rules_rollback (
  badge_id BIGINT PRIMARY KEY,
  rule JSONB NOT NULL,
  legacy_metric TEXT,
  legacy_threshold BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

只在 down 迁移里读。理由：多条件规则无法反推 `metric`/`threshold`，down 必须能明确拒绝或还原。

### 4.4 视图与 HostQuery（AGENTS.md 硬要求）

- `sforum_core_v1.forum_badges` 追加输出 `rule`、`requirement_summary_zh`、`requirement_summary_en`（`metric`/`threshold` 保留一版）。
  **必须 `DROP VIEW IF EXISTS` + `CREATE VIEW`，不能用 `CREATE OR REPLACE VIEW`**（2026-11-12 实测 42P16）：`sforum_core_v1` 是固定 schema、不随测试隔离，schema 隔离的测试库会在同一对象上重放整条迁移链——第二次重放到更早的那条迁移时，它用旧列集替换已是 14 列的视图，Postgres 报 `cannot drop columns from view`。与 `202610180001_stable_core_views_e10.sql` 的写法一致。
- 新增 `sforum_core_v1.forum_user_badge_facts`：只输出 active 用户的 `user_id / username / display_name / fact_key / variant / total`，**不输出 delta 行、不输出 dedupe_key、不输出 source_extension_id**。
- 新增 HostQuery `sforum.core.public_user_badge_facts.list` + schema `sforum.core.public_user_badge_fact`，配套 allowed/denied 测试。**两个名字都要登记且不是同一个字段**：常量层用 `sforum.core.*`（`v2_query_runtime.go`），Query Registry 映射层用 `core.query.*`（`query_registry_core.go`）。注册一条 HostQuery 共 5 处代码 + 2 份硬编码密封清单（`query_catalog_test.go` / `sealed_query_catalog_test.go`，两处各自独立会红）；schema 的 `properties` 与 `Fields` 必须等长。
- **`rule` 本刀只进视图，不进 `QueryPublicBadgesList` 的 `Fields`**：JSONB 进 HostQuery 要先定 `normalizeProtocolV2QueryValue` 的传输形态（现只认 `[]byte/string/int/time`），那是协议层决定，留 M3 与 `badgeConditions` 同批。插件要读规则走 `sforum_core_v1.forum_badges` 视图即可。

## 5. 规则 AST 与条件目录

### 5.1 AST（`Support/RuleEngine`）

```json
{
  "v": 1,
  "root": {
    "op": "all",
    "children": [
      {"op": "any", "children": [
        {"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},
         "cmp":{"op":"gte","value":10}},
        {"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"recent_days","days":30}},
         "cmp":{"op":"gte","value":50}}
      ]},
      {"op": "not", "children": [
        {"type":"content.hidden","params":{},"cmp":{"op":"gte","value":1}}
      ]},
      {"type":"identity.oauth_link","params":{"providerId":"sforum.auth-github"}}
    ]
  }
}
```

- 节点两类：组节点 `{op: all|any|not, children[]}`（`not` 恰好 1 个子节点；**`all`/`any` 至少 1 个子节点，空组必须拒绝**——空 `all` 是 vacuously true，会让一枚徽章瞬间发给全站，空 `any` 是永不发）；叶子 `{type, params, cmp?}`。
- 组节点不接受 `type`/`params`/`cmp`；叶子不接受 `children`。
- 硬上限：深度 ≤ 5、叶子 ≤ 32、节点 ≤ 64、`days` ∈ 1..3650、`value` ∈ 1..1e9。超限 `422 forum.badge_rule_too_complex`。
- `Support/RuleEngine` 只依赖 `Resolver` 接口，**不 import 任何 domain 包**，纯函数可穷举测试。

### 5.2 条件目录（Core 内置 v1）

| key | params | 取值 | 真值来源 |
| --- | --- | --- | --- |
| `activity.count` | `metric`(6 项)、`window`(all/recent_days+days) | 计数 gte/lte | `user_activity`；窗口走 topics/comments/reactions 的 `created_at`、`user_visits` |
| `activity.streak` | `minDays` | 计数 gte | `user_visits` 连续天数（以今天或昨天收尾） |
| `object.engagement` | `action`(like/bookmark/comment)、`topicId`(int) | 布尔 | `content_reactions` / `content_bookmarks` / `comments` |
| `object.authoring` | `targetKind`(category/tag)、`targetSlug`(objectRef)、`window` | 计数 gte | `topics` + `topic_tags` |
| `identity.account_age` | 无 | 计数 gte | `users.created_at` |
| `identity.email_verified` | — | 布尔 | `users.email_verified_at` |
| `identity.oauth_link` | `providerId` | 布尔 | `identity_external_links` status='active' |
| `identity.role` | `roleKey` | 布尔 | `user_roles` + `roles` |
| `identity.avatar` | — | 布尔 | `user_profiles.avatar_attachment_id` |
| `content.pinned` | — | 计数 gte | `topics.is_pinned AND author_user_id=$u` |
| `content.hidden` | — | 计数 gte | `topics.status='hidden' AND author_user_id=$u`（负向信号，仅 NOT 下有意义） |
| `badge.owned` | `slug` | 布尔 | `user_badges` + `badges`（保存时做环检测） |

口径统一：软删内容仍计入「做过什么」（与 `topic_count` 成就口径一致）；`content.hidden` 是唯一看 `status` 的条件，文案必须写明。

**自评论口径（P-11 裁决）**：`object.engagement{action:"comment"}` **算**「自己评论自己的主题」——与 `comment_count` 含自评论一致，不设隐藏例外（隐藏例外会造成「规则看着满足却不发」的困惑，且规则层无法表达「主题作者 ≠ 我」来排除）。后台条件说明文案必须写明「含自己主题下的评论」。

**参数形状以 §5.2.1 为准**：本表只列 key、取值类型与真值来源；两表若有冲突，§5.2.1 胜（它是冻结表）。

### 5.2.1 参数的精确形状（已冻结，前后端同一份）

编辑器与后端校验都按这张表，**不要再各猜一套**：

| key | params（名称: kind） | 备注 |
| --- | --- | --- |
| `activity.count` | `metric`: enum(6 项)；`window`: window | `window = {kind:"all"} \| {kind:"recent_days", days:int}` |
| `activity.streak` | `minDays`: int(1..3650) | |
| `object.engagement` | `action`: enum(like/bookmark/comment)；`topicId`: int(min 1) | 布尔；**topic 用 int，不走 objectRef** |
| `object.authoring` | `targetKind`: enum(category/tag)；`targetSlug`: objectRef；`window`: window | objectRef 的候选由 `targetKind` 决定 |
| `identity.account_age` | **无参数** | 计数 = 账户年龄（天），比较走 `cmp`。**不要加 `days` 参数**——那会造出 `days:7` 与 `cmp.gte 7` 两种写法表达同一件事 |
| `identity.email_verified` / `identity.avatar` | 无 | 布尔 |
| `identity.oauth_link` | `providerId`: objectRef(refKind=provider) | 布尔；值必须能对上 `identity_external_links.provider_id` 或 identity registry 的已声明 provider |
| `identity.role` | `roleKey`: objectRef | 布尔；用 `roles.key` 不用 `alias` |
| `content.pinned` / `content.hidden` | 无 | 计数 |
| `badge.owned` | `slug`: objectRef | 布尔；保存时做环检测 |
| `fact.count`（**插件条件的唯一入口**） | `factKey`: objectRef(refKind=fact)；`variant`: enum(扩展声明的 variants)，可选；`window`: window | 计数；`factKey` 候选来自扩展的 `badgeConditions[]`，**未声明即失效引用** |

**`objectRef` 的取值恒为字符串 slug**，语义是「**有候选列表的引用**」——编辑器据此渲染下拉，`rule_health` 据此判存在性。`identity.role` 的 `roleKey`、`identity.oauth_link` 的 `providerId`、`badge.owned` 的 `slug` 都属这一类（分别 `refKind = role / provider / badge`），**不要**把它们退化成自由文本 `string`：能选就别让人手打，手打的 id 打错了只会得到「保存成功、永不命中」。

`ParamSpec` 需带 `refKind?: "category"|"tag"|"role"|"provider"|"badge"|"fact"`。**数字一律用 `int`，绝不混进 objectRef**。

**`fact.count` 在 M1 的暴露规则**：它必须留在 `ruleengine` 白名单里（否则 M3 的规则校验不过），但 `GET /admin/badges/conditions` 在 `factKey` 候选为空时**不返回它**——M1 没有任何扩展声明，暴露一个空下拉只会让运营点进死路。判据是「有候选才出现」，不是「有定义就出现」。

**kind 全集 = `int | enum | window | objectRef | string | bool`**。`string` / `bool` 是给 M3 插件条件预留的（插件可能有自由文本或开关型事实），**内置 12 条条件一条都不用**；`bool` 目前无消费者。

### 5.2.2 口径裁决（小研核实后，2026-11-12）

以下六条是读码 + 真库 EXPLAIN 之后的裁决，**推翻任何与之冲突的直觉写法**：

1. **窗口「获赞」必须排除自赞**（`r.user_id <> $1`）。写路径 `activityDeltaForReaction` 对自赞返回 `0,0`，所以 `like_received_count` 本来就不含自赞；窗口版若含自赞，同一枚徽章的「累计」与「最近 30 天」会互相矛盾。**P-11 的「自评论算」不适用于获赞**——自赞在计数层已被排除，那是既有口径，不是新例外。
2. **所有「按日」的窗口下界都必须由 Go 传入，不许在 SQL 里取日界**（不只连续天数）。`user_visits.visited_on` 的日界是 **API 进程本地时区**（`visits.go` 用 `now: time.Now`，装配处无时钟注入），而 `site.timezone` 选项全仓除 Options 包外**零消费**。所以 `activity.streak` 与 `days_visited` 的 `recent_days` 窗口下界都要收 Go 算好的 `$n::date`。
   **`CURRENT_DATE` 与 `(now() - interval)::date` 是同一类错误**——断言按「失败类」写（日界是否来自 PG 会话时区），不按字面量写，否则换个等价写法就绕过去了。反例：API 设 `TZ=Asia/Shanghai`、PG 是 UTC 时，跨零点 8 小时里会把刚记的今天排除掉，连续 7 天在零点后立刻误判断签，而窗口计数会多算/少算一天。
3. **`content.hidden` 只统计主题、只认 `status='hidden'`**。核实结论：`hidden` 是纯管理员动作（`canManageTopicVisibility` 要 `topic.delete_any`；作者自删走 `status='deleted'`），但 `topics.status` 全集比原计划宽（还有 `pending` / `rejected`），条件只认 `hidden`。**评论被隐藏不计入**（v1 刻意收窄，口径要一句话说得清）。**没有精确版**：`moderation_reports` 无 action 列、`audit_events` 无内容目标列，所以它只能是「被管理员隐藏」的代理指标，文案必须写「主题被管理员隐藏的次数」，**不许**写成「违规次数」。
4. **`content.pinned` 是「当前仍被置顶的主题数」**，不是「曾经被置顶过」——`is_pinned` 只有当前布尔值，无 `pinned_at`、无审计列，「曾经」这个口径没有任何数据来源。文案照此写。
5. **`object.authoring` 不按 `tags.status` 过滤**。下架/待审标签不让「当时用过」的事实凭空消失（与成就口径一致）；规则改严也不回收已授予（§7）。
6. **`identity.role` 用 `roles.key` 且必须加 `r.is_enabled`**。`key` 有 UNIQUE、`alias` 可改（有测试钉着）；`is_enabled` 与授权判定保持一致。
7. **`object.engagement{action:"bookmark"}` 不按 `visibility` 过滤**（2026-11-12 裁决）。写路径 `bumpUserActivityTx` 按 `relationDelta` 计数、与可见性无关（改可见性 `relationDelta=0` 天然 no-op）；`Recompute` 也数全部行。**`content_bookmarks.visibility` 默认是 `private`**，只算 public 会让「收藏家」徽章几乎不可达。模块笔记里「`bookmark_count` 只算 `visibility='public'`」那句是错的，已修。
8. **`fact.count` 的 `factKey` 未声明时 resolver 必须返 error**（fail-closed）。M1 没有任何扩展声明 factKey，所以 resolver 要接一个「已声明 factKey 集合」参数，M1 传空集 → 任何 `fact.count` 叶子求值都变 unknown。**不许**因为「查不到事实行」就返回 0——`lte` 会把 0 判成满足，扩展卸载后一条 `lte` 规则会对全站成立。M3 用已启用扩展的 `badgeConditions[]` 填充该集合。

**批量策略**：同 type 的多叶子**参数化合并**（否则 32 叶子最坏 32 次往返）；`window.all` 走计数器与 `recent_days` 走业务表是两个数据源，不可合并但可并发；`badge.owned` 与 `object.engagement` 不需要多查（环检测在保存期，不在评估期）。

### 5.3 取数策略

- 一次评估先把所有启用自动规则的叶子按 `type` 分组，**每个 type 一次查询**，结果 memo 在该用户本次评估内。
- **快路径**：规则是单个 `activity.count` + `window.all` 叶子时，走现有 `ListEnabledAutomaticBadges` + 一次 `GetCounters`，SQL 与今天完全一致（保证 10 枚内置自动徽章零回归、零性能变化）。
- 复杂规则才进入引擎路径。

## 6. 后端模块拆分

| 文件 | 责任 |
| --- | --- |
| `app/Support/RuleEngine/ast.go` | 节点结构与 JSON 解码（未知字段报错，不静默） |
| `app/Support/RuleEngine/validate.go` | 形状/上限/`type` 是否在注册表内/`cmp` 是否被该条件允许 |
| `app/Support/RuleEngine/eval.go` | `Eval(ctx, node, Resolver)`，三值逻辑：解析失败按 fail-closed 记为 false 并回传错误清单 |
| `app/Models/Badges/conditions/registry.go` | 条件元数据（key/取值类型/参数 schema/compare 白名单/i18n label key） |
| `app/Models/Badges/conditions/resolve_activity.go` | 计数与连续天数（含窗口 SQL） |
| `app/Models/Badges/conditions/resolve_object.go` | 定向对象（topic/category/tag） |
| `app/Models/Badges/conditions/resolve_identity.go` | 账户/角色/OAuth2/头像 |
| `app/Models/Badges/conditions/resolve_content.go` | 置顶/隐藏/已拥有徽章 |
| `app/Models/Badges/conditions/resolve_facts.go` | 插件事实（`badge_facts` + 计数器） |
| `app/Models/Badges/rule_store.go` | 规则读写、保存校验（含环检测）、引用健康检查 |
| `app/Models/Badges/rule_eval.go` | store 侧把 resolver 接进 `EvaluateRule`（**必须把已声明 factKey 传给 resolver**，见 §13.13） |
| `app/Models/Badges/rule_health.go` | 失效引用清单（topic/category/tag/role/provider/badge slug/factKey），与保存校验共用同一份引用检查 |
| `app/Models/Badges/rule_impact.go` | 影响人数估算（按叶子取计数，AND 取最小、OR 取和，返回「≈」语义） |
| `app/Models/Badges/facts_store.go` | 事实写入（幂等 dedupe）+ 计数维护 |
| `app/Models/Badges/evaluate.go`（改） | 保留签名，内部改走「快路径 / 引擎」双分支 |
| `app/Jobs/Badges/backfill_rules.go` | 回扫 job：`{badgeId, cursorUserId, batch}`，游标可续，`ByArgs` 去重 |
| `app/Http/Controllers/Badges/controller.go`（改） | 新端点；新逻辑拆 `rule_controller.go` |
| `app/Http/Controllers/Badges/dto.go`（改） | `rule` 三态（缺省=不改 / null=清空 / 对象=替换），沿用现有 metric 三态写法 |

## 7. 追溯与回扫

- 保存/修改自动徽章规则 → 同步写库 → **异步**入队 `badges.backfill_rules`（HTTP 内只做估算 + 入队，绝不跑全量）。
- 保存前 `GET /admin/badges/:id/rule-impact` 返回 `{estimated, method}`，前端弹两步确认。
- 回扫按 `user_id` 游标分批（默认 500），每批 `EvaluateUser`，写 `granted` 计数；job 失败可重入，幂等靠 `ON CONFLICT DO NOTHING`。
- **明确不做**：不做「规则改严后自动收回已授予」——徽章是历史成就，收回只能手动。UI 提示与模块笔记都要写明。

## 8. 插件扩展契约（M3）

### 8.1 manifest 声明（Manifest V3 新顶层数组）

**定位（小研核对代码后修正，比原设计更省）：`badgeConditions` 不是「新增条件类型」，而是给 Core 已有的 `fact.count` 注册合法的 `factKey` 与 `variant` 候选。**

- 规则叶子恒为 `{"type":"fact.count","params":{"factKey":"<扩展id>.<名字>","variant":"…","window":{…}},"cmp":{…}}`。
- `ruleengine` 白名单**仍是 13 条**（12 内置 + `fact.count`），不随插件数量膨胀。
- `rule_health` 直接复用既有的 `RefKind` 检查（`factKey` 的 `RefKind = "fact"`），不用再造一套扩展条件检查。
- **插件条件只有 count 语义**（`boolean` 用 `cmp: {gte,1}` 表达）——事实表本来就是 delta 累加，布尔化只是多一条代码路径。

```json
"badgeConditions": [{
  "key": "acme.onboarding.tasks",
  "contractVersion": "acme.onboarding.tasks@1",
  "label": {"zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks"},
  "unit": {"zh-CN": "个", "en-US": "tasks"},
  "variants": [{"value": "tutorial", "label": {"zh-CN": "教程任务", "en-US": "Tutorial"}}]
}]
```

- **`key` 的 pattern 必须逐字等于 DB CHECK** `badge_facts.fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$'`，否则「清单校验通过、上报时 500」。
- **强制 `contractVersion == key + "@" + N`**：`versionedID` 只校验 pattern 不校验等式，不强制就会出现原示例那种漂移（`acme.onboarding.condition.tasks@1` ≠ `acme.onboarding.tasks@1`）。
- **`badgeConditions` 要求扩展有 `backend`**（事实只能经 Host 命令写入），与 `aiTools` 的既有纪律一致。
- 上限：`badgeConditions ≤ 32`（对齐 `MaxLeaves`）、`variants ≤ 16` 且 value 非空、`compare ⊆ {gte,gt,lte,lt,eq}`（无 `ne`）。
- 校验落在 `ExtensionManifest/v3_validate_*.go`；规则里出现未声明的 `factKey` → 保存时 `422 forum.badge_rule_reference_invalid`（引用类，不是 unknown_condition——`fact.count` 本身永远在目录里）。
- 落点：新增一个顶层数组共 **6 个生产文件 + 1 份生成文档**；会红的既有测试是 `docsgen_test.go:93-101` 与 `TestCommittedCatalogDocsInSync`。
- **最大隐性坑**：`ExtensionManifest` 没有任何 reflect 式 struct↔schema 对齐测试。只改 Go struct 不改 `manifest-v3.schema.json`，单测可能全绿，运行时才被 `additionalProperties:false` 拒包——改 struct 必须同批改 schema。
- 回归夹具用 `sforum-query-surface-reference`（静态 V3 清单，真进 `reference-packages-gate.sh`）；**不要**用 `sforum-ai-tool-reference`（只有 `.tmpl`，被门禁跳过）。
- **卸载后是 unknown，不是 false**：规则保留，`fact.count` 的 resolver 查不到已声明的 `factKey` 时**返回 error**（引擎转 unknown），**绝不能返回 0**——`lte` 会把 0 判成满足，`not` 会把 false 判成 true，两者都是 fail-open。后台健康提示列出失效引用，不静默删规则。
- **重装自动恢复**：规则里存的是字符串 key、候选目录实时合并，扩展一启用失效引用立刻消失；`contractVersion` 升级不阻断（Core 只读计数，与插件 payload 契约无关），但 `rule_health` 显示版本差异。

### 8.2 Host 命令

Host 命令按引擎的 `{ID, Version}` 双字段登记（**ID 不带 `@N`**，既有 ID 一律如此：`notifications.emit` + `"1"`）：

- `ID = "badges.fact.record"`，`Version = "1"`
- schema ref：`sforum.badges.fact.record.input@1` / `sforum.badges.fact.record.result@1`（`@1` 只出现在 PolicyId / schema ref 位置）

```json
{"factKey":"acme.onboarding.tasks","variant":"tutorial","delta":1,"occurredAt":"...","dedupeKey":"task-42"}
```

- `delta` 在 protobuf Struct 里是 double，**必须编码成十进制字符串**（对照 `sdk/plugin/v2/events.go` 的 `EventEmitResultValues` 注释）；`inserted:false` 走正常 `CommandResult.Output`，拒绝走 `ErrorDetail.reason`。
- 新能力 `badges.fact.write`，落 `protocolV2CommandDefinition.RequiredCapability`；必须在 `capabilities[]` 声明，未声明 → 拒绝并进审计。**能力 label 必须自解释**：启用弹窗只渲染 `LabelZH`/`LabelEN`，不渲染 description（`SFAdminExtensionEnableDialog.vue`）。
- `factKey` 必须属于调用方扩展的 `badgeConditions[].key`，否则拒绝。
- 幂等：`dedupeKey` 非空时唯一索引兜底，重复调用返回 `inserted:false`。
- 信任披露：安装/启用弹窗的 capability 清单追加该能力的中英文说明。

### 8.3 「事件转事实」的落地形态

不新增订阅总线。SDK 提供一行式封装（**`sdk/plugin/v2` 里没有 `Client` 类型，只有 `Host`**）：

```go
// sdk/plugin/v2/badges.go
func (h *Host) RecordBadgeFact(ctx context.Context, parent *protocolwire.RequestContext, in BadgeFactInput) (*hostwire.CommandResult, error)
func (h *Host) RecordBadgeFactRequest(parent *protocolwire.RequestContext, in BadgeFactInput) (*hostwire.CommandRequest, error)
```

文档给出「声明 `events[]` + 在 handler 里调 `RecordBadgeFact`」的标准范式，并注明这是 C 路径的等价实现。

## 9. API 与契约

| 端点 | 说明 |
| --- | --- |
| `GET /admin/badges/conditions` | 条件目录（Core 内置 + 已安装扩展贡献），编辑器唯一数据源 |
| `GET /admin/badges/:id/rule-impact` | 影响人数估算 |
| `GET /admin/badges/rule-health` | 失效引用清单（形状见下） |
| `POST /admin/badges` / `PATCH /admin/badges/:id` | 新增 `rule` 三态字段 + `requirementSummary{zh,en}`（**两者都是整体替换**：字段缺省=不动，存在=整个写） |
| `POST /admin/badges/recompute` | 保留，内部改走引擎 |

公开面：`PublicBadge` 增 `requirementText{zh,en}`（运营填的优先，**逐语言独立回落**；为空且是**单条件**规则时服务端按模板生成，句子只含「指标名 + 阈值」；**多条件规则不生成**——宁可空，也不把内部结构拼成读不懂的话）。**`Progress` v1 不变**（仍 `{current, threshold}`，只出 numeric）：用户决策 #14 是「只给自然语言描述 + earned 状态」，且资料页徽章墙一次渲染 N 枚，逐徽章跑条件树与「徽章墙一次读取」的既有设计冲突。条件式进度（`satisfied/total`）留到后续版本，本刀不扩契约。

**`rule` 三态的实现口径**：复用既有 `patchField[T]`（`controller.go` 已有：缺省 → `Set=false`；显式 null → `Set=true, Value=nil`；对象 → `Set=true, Value=&v`），**不要自造 `*json.RawMessage` 判空**。SQL 用 `CASE WHEN $n THEN $m::jsonb ELSE rule END`——**`jsonb` 相等不代表 `::text` 相等**（jsonb 写入时规范化键序与空白），所以「未提供时」必须**不写**而不是写回同值，否则字节级断言会红。

**M2 配套要求**：规则是**多条件**且 zh/en 两个 `requirementSummary` 都空时，编辑弹窗必须给一条**字段级提示**（提醒而非阻止保存）——否则公开面会出现一枚没有解释的徽章。前端提交 `requirementSummary` 时必须**同时给两种语言**（整体替换语义）。

**M1 新错误码（zh/en 双份文案）**：`forum.badge_rule_too_complex`、`forum.badge_rule_unknown_condition`、`forum.badge_rule_cycle`、`forum.badge_rule_reference_invalid`、`forum.badge_backfill_unavailable`（有复杂徽章但入队入口没接上时必须报错，**不许**报「0 人获得」）。
**M3 才加**：`forum.badge_fact_write_denied`（M1 没有调用方，提前写文案等于给不存在的路径写测试）。

**错误码拆分要求**：`ruleengine` 的失败都包在 `ErrBadgeRuleInvalid` 里，HTTP 层必须按 `ruleengine.CodeOf(err)` 拆开——形状/上限错 → `_too_complex`，未知条件 → `_unknown_condition`。合成一个码，前端就无法区分「规则写复杂了」和「引用了不存在的条件」，而这两种的修法完全不同。

**`GET /admin/badges/rule-health` 响应形状（2026-11-12 冻结，前后端同一份）**：

```json
{
  "items": [
    {
      "badgeId": 12,
      "slug": "veteran",
      "name": "元老",
      "issues": [
        {"kind": "missing_topic", "path": "root.children[0].params.topicId",
         "ref": "123", "label": "主题 #123"},
        {"kind": "missing_fact_key", "path": "root.children[1].params.factKey",
         "ref": "acme.onboarding.tasks", "label": "acme.onboarding.tasks"}
      ]
    }
  ]
}
```

- `kind` 枚举：`missing_topic` / `missing_category` / `missing_tag` / `missing_role` / `missing_provider` / `missing_badge_slug` / `missing_fact_key` / `undecodable_rule` / `invalid_rule_shape`。
- `undecodable_rule`：`RuleRaw` 非空但 **Decode 失败**。`path` 空串、`ref` 放原始 JSON 前 200 字符、`label` 用 slug。
- `invalid_rule_shape`：**Decode 成功但 `ruleengine.Validate` 失败**（典型：`{"op":"all"}` 空组——它能解码，但评估期恒 unknown，等于「永远不发」）。`path` = 首个错误的 `Error.Path`，`ref` = 首个错误的**错误码**（如 `shape_invalid` / `compare_forbidden`），`label` 留空——前端用已有的 `admin.badges.rule.errors.<code>` 文案渲染。
- **两者不能合并**：`undecodable_rule` 的修法是「重新编辑规则」，`invalid_rule_shape` 的修法是「补上缺的条件」。合并成一个 kind、把原因塞进 `ref` 的自由文本里，前端就只能靠解析文案来区分，那是把契约变成字符串匹配。
- `path` 与 `ruleengine.Error.Path` 同一套坐标（`root.children[0].params.x`），前端可直接定位到节点。
- **只列「引用/条件失效」，不列「零授予」**——健康清单不回答「有没有人达标」。这一条单独重要：混进去会让运营把「暂时没人达标」当成配置坏了。
- 空 `items` 是正常结果（全站规则都健康），HTTP 200，不是 404。

**`GET /admin/badges/:id/rule-impact` 响应形状（2026-11-12 冻结）**：

```json
{"estimated": 120, "method": "strictest_leaf_partial", "partial": true,
 "unsupportedConditions": ["object.authoring"]}
```

- `estimated` **可为 `null`**：`null` = 无法估算，`0` = 估算结果确实是零人。**两者不能混**——用 0 表示「算不出来」会让运营读成「没人能达标」，然后去改一条本来正确的规则。
- `method` ∈ `single_leaf` / `strictest_leaf` / `strictest_leaf_partial` / `sum_of_leaves` / `complement_of_leaf` / `unsupported_condition`。
- **纯 AND 且部分叶子不可估 → 仍给上界**：`true ≤ |∩supported| ≤ min(supported)`，取已支持叶子的最小值是合法上界，`method = strictest_leaf_partial` + `partial = true`。真实规则大多是 AND，这比「无法估算」有用。
- **OR / NOT 含不可估叶子 → `estimated: null`**：OR 的「总用户数」虽是技术上界，但对决策零信息量，还会占一个看起来确定的数字位。
- `unsupportedConditions` 列出不可估算的条件 key，前端据此渲染「含 N 个不可估算的条件」。
- 估算走**聚合 SQL**（`count(DISTINCT …)`），**不逐用户跑引擎**——它发生在运营点开弹窗的瞬间，逐用户求值会把只读接口变成全表扫描。

**`recompute` 响应新增 `queued`（2026-11-12 批准，审计后补进计划）**：`{granted, refreshed, queued}`，`queued` = **本次转入异步评估的徽章数**（不是用户数、不是 job 数）。复杂规则无法在批量 SQL 里求值，必须走 `badges.backfill_rules`；`queued > 0` 时前端 toast 必须说明「部分徽章在后台评估」，不许只报「完成」。`queued == 0` 是正常路径。

## 10. 前端（M2）

- `SFBadgeCard.vue` **已于 `18cbc5e06` 抽取完成**（tab 253 行 / card 309 行，两个 variant 都走卡，props/emit 未变），本刀不重做。M2 直接在现有卡片上接 `requirementText`（复用现有多行描述位）与 numeric 进度；条件式进度不出条，`forumBadgeHasProgress` 单点 gate 不动。
- 新增 `components/admin/badges/rule/SFAdminBadgeRuleEditor.vue`（树）、`SFAdminBadgeRuleGroup.vue`、`SFAdminBadgeRuleLeaf.vue`、`SFAdminBadgeRuleJsonPreview.vue`（只读 + 复制）、`SFAdminBadgeRuleImpactDialog.vue`。
- 纯函数 `utils/admin/badgeRule.ts`：节点增删/移动/嵌套、`validateRuleDraft`（与后端同规则的前端镜像）、`describeRule`。
- `composables/admin/badges/useAdminBadgeConditions.ts` 拉目录；`useAdminBadges.ts` 增加 `ruleHealth`。
- 健康提示落在 `SFAdminBadgesBadgesTab.vue` 筛选区上方一行 `UAlert`（可关闭），不新增卡片、不新增页头。
- i18n 两处同步，并更新 `adminBadgesI18n.test.ts` 的 `DYNAMIC_ENUMERATIONS`（`conditions.` / `rule.compare.` / `rule.ops.`）。

## 11. 三期里程碑

**M1 引擎（后端，可独立验收）**

1. 迁移 + 视图 + HostQuery + 回滚表。
2. `Support/RuleEngine`（纯函数 + 穷举测试）。
3. `conditions` 注册表与五组解析器（含窗口 SQL）。
4. `evaluate.go` 双分支 + 快路径回归测试（10 枚内置自动徽章逐人一致 + 2 枚手动不变）。
5. 规则保存校验（含环检测）、影响估算、健康检查、回扫 job。
6. OpenAPI + DTO + 错误码 + 路由计数 tripwire。

**M2 编辑器与公开面**

1. `SFBadgeCard` 抽取 + 公开面 `requirementText` / 进度新形态。
2. 规则树编辑器 + JSON 预览 + 影响确认弹窗。
3. 健康提示、i18n 双向断言。
4. 浏览器 QA：桌面 + 390×844，覆盖新建/嵌套/取反/保存/失效提示。

**M3 插件扩展**

1. manifest `badgeConditions[]` 校验 + 条件目录合并 + 卸载后失效语义。
2. `sforum.badges.fact.record@1` + `badges.fact.write` 能力 + 审计 + 信任披露。
3. SDK 封装 + 事件 handler 范式文档。
4. 夹具插件 `extensions/fixtures/plugins/sforum-badge-facts/` 端到端。
5. `docs/extensions/` 参考页 + `knowledge/decisions/2026-11-12-badge-rule-engine.md`。

## 12. 测试与验收矩阵

- **纯函数**：AST 解码/校验/求值真值表（all/any/not、嵌套、上限、未知类型、非法 cmp）。
- **PG 集成**：每个条件类型 allowed/denied；窗口 vs 累计；软删口径；`identity_external_links` 只认 `status='active'`；事实幂等与计数一致性。
- **迁移**：up/down 可逆（单条件规则）、down 在多条件规则下拒绝并给 HINT。
- **HTTP**：规则校验 422 各码、影响估算、健康清单、`rule` 三态语义、公开面不泄露 AST。
- **job**：回扫可续、幂等、计数正确；大批量不阻塞 HTTP。
- **前端**：`bun test` 覆盖 `badgeRule.ts`、编辑器组件行为、i18n 双向断言；`bun run typecheck`。
- **门禁**：`./scripts/test.sh --fast`、`node tests/validate-architecture-boundaries.mjs`、`ruby scripts/validate-openapi-refs.rb`。

## 13. 风险与明确残留

1. **窗口「获赞」成本（已由实测两次修正）**：**不要**加 `content_reactions (target_type, target_id, created_at)` 索引。第一次 EXPLAIN（未含自赞排除）显示 Index Only Scan；加上 `r.user_id <> $1` 后，因为 `user_id` 不在 `roster_idx` 里，计划退成 **Index Scan + heap 过滤**——这是正确的、预期的代价，不是缺陷。10k 夹具实测：默认 1.98ms、`enable_seqscan=off` 2.46ms。**验收判据因此改为「计划中出现 `content_reactions_roster_idx` 且 P95 在预算内」，不再要求 Index Only Scan**（要求 Index Only Scan 会假红）。
2. **影响人数是估算**：AND 取最严叶子、OR 取和，弹窗文案必须写「≈」，不能当承诺。
3. **`RecomputeAll` 仍在 HTTP 内同步**（残留 7，旧编号 #17，见 `modules/badges.md`）：本刀不改，但规则保存触发的回扫一律走 job，不再叠加同步路径。
4. **规则改严不自动收回**：已授予保留，属产品语义，UI 与模块笔记都要写明。
5. **插件判定器（A 路径）未做**：`fail-closed` 本刀只作用于「事实查询失败」；v2 引入 RPC 判定器时超时/隔离/批量接口需重新设计。
6. **查看类条件未做**：不建 `user_topic_reads`；插件可用事实行自行提供等价条件。
7. **采纳/精华/热榜条件未做**：需 Forum 域字段，下一刀。
8. **举报类条件不做**：审核数据不进徽章公开面。
9. **trust levels 未共用引擎**：引擎已独立成包，将来可平移，但本刀不动 `LevelOf` 纯函数与配置。
10. **公开面自然语言是双列**：运营填 zh/en；只填一种时另一种回落到规则自动生成句。
11. **`PublishableDefinitions(factKeys []string)` 不携带 variants**（M1 已存在的 seam）：M1 没有扩展声明，`fact.count` 因此不发布；M3 需要按 factKey 渲染 variant 下拉时，签名要扩成 `FactCandidate{Key, Variants}` 或 `map[string][]string`。这牵动 M2 组件契约，故**留到 M3 落地时定**；M2 只需保证「`variant` 的候选可能为空，不得把空候选渲染成空下拉并禁止保存」。
12. **`site.timezone` 全仓除 Options 包外零消费**（小研核实）：`user_visits` 的日界是 API 进程本地时区。这是站点级缺陷，影响所有按「日」统计的功能，**本刀不修**，只要求 streak 收 Go 传的日期。应单开一刀。
13. **`EvaluateRule` 必须把「已声明 factKey 集合」透传给 resolver**（小研偏差审计发现，`rule_eval.go` 曾漏传 → 生产路径恒传空 → 任何 `fact.count` 叶子恒 unknown）。M1 没有扩展声明，空集是**正确**行为；但这条链路必须真的接通，否则 M3 一上线，所有插件事实条件都会「规则存得下、永远不满足」——最难查的那类。M3 只需提供该集合的来源，不改这条链路。
14. **集成测试的 `search_path` 带 `public`，fixture 漏建表会静默借真实 schema**（本刀实测，比想象的严重）：
   - **写侧**：badge fixture 原先没有 `user_fact_counters` / `badge_facts`，不带前缀的查询落到 `public` 的真实表，撞外键才暴露。已补进 fixture。**dev 库未被污染**（`testdb` 哨兵守卫让测试只跑在一次性集群上，`public` 是那个集群里迁移过的 schema，用完即销毁），实测两张事实表 0 行。
   - **读侧才是真问题**：把 `public` 摘掉后立刻炸出 `badges` fixture 缺 `badge_tiers`、`badges_tiers` fixture 缺 `badge_groups`——**过去这些测试的绿有一半建立在迁移后的 public schema 上**。不污染数据，但意味着「fixture 是否自洽」永远测不出来：少一张表、少一列，测试照样过。
   - 全仓 `search_path = schema + ",public"` 有 **86 处**，属既有模式，**本刀不重构**（摘掉 `public` 会连锁影响几十个包，无法在本刀验证）。补 `badge_tiers` / `badge_groups` 也不行——补表必须同时种下 3 行 tier，否则 `top_badge_id` 全 NULL、一批断言变假红。**单开一刀**：fixture 建 schema 后 `SET search_path TO <schema>` + 让「被查询表清单」与 DDL 对齐，漏表立刻 `relation does not exist`。
15. ~~**回扫进度不可见**~~ —— **已闭（2026-11-12 第一刀，见 §15.3）**。`badge_rule_backfill_runs` 表 + `GET /admin/badges/backfill-status` + 后台筛选区上方一行三态；失败带 `badgeId` + `failedCursorUserId`；`recompute` 响应形状未动。残余只有 4 条**测试缺口**（GAP-1..4，task-12 跟踪），无行为缺口。
16. **新增 Core 表/视图必须让 owner 与既有 Core 对象同源**（本刀实测踩到，报错极具误导性）：kernel 校验要求 `public` 与 `sforum_core_v1` 里**每个**对象的 owner 等于 Core owner role；而收敛过滤器只搬 `relowner = roleName` 的对象，所以 owner 不同的新对象永远进不了候选集。报错是 `extension database physical resource conflict`——**看不出是 owner 问题**，会让人先怀疑表名冲突或扩展资源清单。修法（本刀采用）：迁移末尾用 `DO` 块从同域既有表（`public.badges`）读 `pg_get_userbyid(relowner)`，再 `ALTER … OWNER TO` 新对象——自对齐，不需要知道 ownerRole 是谁。
17. **`CREATE OR REPLACE VIEW` 保留原 owner，`DROP + CREATE` 会重置 owner**：任何为了加宽/收窄视图而改成 DROP+CREATE 的迁移，都必须同批补 owner 对齐（本刀 `forum_badges` 就是这么被重置的）。
18. **本刀改了一条已应用的迁移 `202611100002`**（`CREATE OR REPLACE VIEW` → `DROP VIEW IF EXISTS` + `CREATE VIEW`）：schema 隔离的测试库会在**固定的** `sforum_core_v1` 上重放整条迁移链，第二次重放时旧迁移用 11 列定义替换已是 14 列的视图 → `42P16 cannot drop columns from view`。
    - **为什么可接受**：goose 不校验 checksum，已迁移的库不会重跑该文件，所以对存量环境是惰性的；对新环境两种写法终态等价。
    - **更干净的长期修法**（留给 fixture 那一刀）：测试装配在重放前 `DROP SCHEMA sforum_core_v1 CASCADE`，而不是让旧迁移去兼容更宽的未来视图。**改历史迁移是例外，不是惯例**——下次遇到同类问题先想「fixture 是否该清场」。
19. **新增错误码必须同批加进 `mapBadgeError`**（本刀实测：三个新错误没有 case → 落到 `default` → **HTTP 500**）。而且 `ErrBadgeRuleInvalid` 只笼统映射成 `badge_rule_invalid` 会让「层级太深」与「条件未知」在前端显示成同一句话——必须按 `ruleengine.CodeOf(err)` 细分。判据：每个新码都要有一条「喂进映射函数 → 断言 422 + 精确码」的测试。**踩到的细节**：`ruleengine.CodeOf` 走 `errors.As`，用 `fmt.Errorf("%w: %s", …)` 拼字符串取不到码，测试里必须包真正的 `*ruleengine.Error`。
20. **键存在性断言必须配值级断言**（本刀变异测试抓到）：DTO 字段没有 `omitempty` 时，漏赋值输出的是 `{"zh":"","en":""}`——**键在、值空**。所以「required 里的键都在」和「字段是 object 类型」两条断言都会假绿。值级判据：按规则形状挑出**单条件** slug，断言其双语文本长度 > 0，且不含 `{`/`cmp`/`children`/`params`。
21. **验证「重建是否生效」不能用「grep 不到字段」**：`air` 重建窗口会返回 HTTP 000，grep 当然也 grep 不到。判据必须写成「**http==200 且字段数==0**」，否则会把「服务还没起来」当成「重建生效」。
22. **一次性验收脚本不进任何门禁 → 代码一改就腐坏，且腐坏时没有信号**（本刀实测）：`scripts/verify-badge-rules-baseline.sh` 里 `store.GrantFromRules(ctx)` 是两值接收，而 `task-4` 把它改成了 `(int64, []int64, error)`；脚本从此 `exit 8` 编译不过，但没人知道——因为脚本不在 `test.sh` 也不在 `tests/validate-*`。老后当初报的「迁移前 57 条 / `granted=57 refreshed=29`」是真的，只是那次跑在签名变更之前。
    - **教训**：脚本类证据的有效期 = 它最后一次成功运行之后的代码没变。收口报告引用脚本结果时，必须注明「复跑于哪个 commit/时间」。
    - 长期修法（单开一刀）：给 `scripts/verify-*.sh` 加一个「至少能编译/`--help` 跑通」的门禁，或在 CI 里跑一次 `--dry-run`。
23. **比签名漂移更深的一层：脚本里的「旧逻辑」会随实现重写而语义失效**（本刀实测，同一脚本第二炸）。修好签名后脚本仍 `exit 5`：`column rules.rule does not exist`——因为 `GrantFromRules` 已被重写成规则驱动（读 `rules.rule`），而迁移前的 schema 里根本没有 `rule` 列。**签名漂移至少会编译报错，语义漂移只在运行时炸在一个与验收目标无关的地方**（`SQLSTATE 42703`），极易被当成环境问题跳过。
    - **结论：基线脚本不该调用当前 store，它得自带旧语义。** 本刀的做法是**复用脚本里已经存在的 legacy 谓词 SQL**（产出 `should_have_but_missing=57` 的那段）做 `INSERT … SELECT` + 按 `topBadgeOrderSQL` 刷 `top_badge_id`——不引入第二份手写实现，也不依赖旧代码能否构建。

## 14. 知识库更新（收口必做）

- 重写 `knowledge/modules/badges.md`：规则引擎、条件目录、事实层、扩展面、残留。
- 新增 `knowledge/decisions/2026-11-12-badge-rule-engine.md`（含「为何混合事实层而非统一事实层」「为何不做 core 事件订阅总线」「为何 down 迁移可拒绝」）。
- 更新本文件 Status 与 `knowledge/plans/README.md` 状态行。
- 更新 `knowledge/index.md` Latest Handoff 指向新 session 笔记。
- `docs/extensions/` 增加 `badgeConditions` 与 `sforum.badges.fact.record` 参考。

## 15. 实施进度（滚动更新）

**M1 后端收口（2026-11-12）**：七片全部落地并验收，偏差审计见
`reports/2026-11-12-badge-rule-engine-m1-deviation-audit.md`（§4/§5 全绿，未实现项已清零）。
**M2 编辑器与接线完成**（含规则编辑器组件、纯函数与接线）。**第一刀（回扫进度可见）与 M3（插件扩展）均已收口**（见 §15.3 / §15.4），代码提交 `4457106e2`（apps）/ `082f98624`（web）/ `4e1876331`（contracts）。

| 切片 | 共享任务 | 状态 |
| --- | --- | --- |
| M1.1 迁移 + 视图 + HostQuery + 逆迁移分支 | `task-1` | **完成**：`CMD-REV` 真跑 exit 0（down 11 项 + up 18 项），另加 X-04b（多条件规则下 down 被拒且无半回滚）、X-04c（可逆但不同 → 按当前规则反推）；新集群重放基线 `should_have_but_missing=0` / `recompute {0,0}` |
| M1.2 `Support/RuleEngine` 纯 AST | `task-2` | **完成**：小审 7/7 PASS |
| M1.2b P-12（Eval 空组/错形 → unknown） | `task-5` | **完成**：独立探针实测六个畸形形状全 `matched=false incomplete=true shape_invalid`，正常路径未误伤 |
| M1.3 条件注册表 + 五组解析器 | `task-3` | **完成**：13 条条件、`Prepare` 每 type 一条 SQL、`Resolve` 只读 memo；六条硬约束都有测试 |
| M1.3b 三处口径修正 | `task-6` | **完成**：窗口日界改 Go 传参（`$3::date`）、`bookmark` 去 `visibility` 过滤、`factKey` 未声明返 error；各带一条回归测试 |
| M1.4 规则存储/求值/回扫/HTTP/契约 | `task-4` | **完成**：`rule_store`（含环检测）/ `rule_impact` / `rule_health` / `facts_store` / `backfill_rules` / `rule_controller` / `dto` 三态 + `requirementText`；路由计数 418→**421**；4 个错误码双语文案；`EvaluateRule` 已透传 factKey |
| M1 落点核实简报 | 小研 | **完成**：`reports/2026-11-12-badge-rule-engine-impl-facts.md` |
| 12 条件参考 SQL（M1.3 输入） | 小研 | **完成**：`reports/2026-11-12-badge-rule-engine-condition-sql.md`（含 dev 库实测 EXPLAIN） |
| M1 验收矩阵 + 可执行脚本 | reviewer | **完成**：61 条断言 + `scripts/verify-badge-rules-acceptance.sh`（ok/FAIL/pend，不接门禁） |
| M1 偏差审计（计划 vs 实现） | 小研 | **完成**：`reports/2026-11-12-badge-rule-engine-m1-deviation-audit.md` |
| M2 规则编辑器 + 接线 | 前端 | **完成**：`components/admin/badges/rule/*`（Editor/Group/Leaf/JsonPreview）+ `utils/admin/{badgeRule,badgeRuleParams}.ts`；`bun test` 覆盖纯函数与编辑器行为 |
| M3 插件扩展 | task-5/6、task-10 | **完成**。M3.1 manifest `badgeConditions[]` + 能力 `badges.fact.write` + 事实候选目录 + `factCandidates` DTO **已落地并验收**（最终口径 `ok 48 / FAIL 0 / pend 0`，12 条变异 + 覆盖自检全红过，报告 `reports/2026-11-12-badge-m3-manifest-acceptance.md`）；M3.2 命令**已验收**（`ok 33 / FAIL 0 / pend 0`，14 条变异全红过，报告 `reports/2026-11-12-badge-fact-record-acceptance.md`）；M3.3 SDK / M3.4 夹具 / M3.5 文档+决策记录均已落（`docs/extensions/badge-plugin-facts.md` 双语 + `decisions/2026-11-12-badge-plugin-facts.md`）。契约：`reports/2026-11-12-badge-conditions-manifest-spec.md`（含 Errata 2/3/4） |

**收口遗留**：~~`M1-EXIT-PERF` 仍是 pend~~ —— **已闭（2026-11-12）**：默认 10k 规模（1,999,800 行）真跑，`PERF-01` 走 `Index Scan using content_reactions_roster_idx`（`Filter: user_id <> 1`，0.189ms）、`PERF-03` p50 2.19ms / **p95 3.47ms**（预算 50ms）。**P-01「不加索引」在 10k 规模成立**。

### 15.1 M1 收口结论（2026-11-12）

**验收**：`ok 74 / FAIL 0 / pend 9`，无阻塞项。pend 全部是人工门（需一次性集群 / 管理员会话）或 M3 码。

| 证据 | 结果 | 谁跑的 |
| --- | --- | --- |
| 迁移等价性（新集群重放） | 迁移前缺口 57 → 旧逻辑 `granted=57 refreshed=29` → 迁移后 `{0,0}`；双向对账 0/0；`top_badge_id` 差集 0；`user_activity` 逐行一致 | 老后首跑、**小审独立复跑一致** |
| 迁移可逆性 `CMD-REV` | exit 0，44 条 ok（down 11 + up 18 + X-04b 拒绝段 + X-04c 按当前规则反推） | 老后首跑、**小审独立复跑** |
| 10k 性能门 | `Index Scan` + roster_idx，p95 3.47ms | 小审 |
| 全仓门禁 `test.sh --fast` | api / web / static 三线 PASS | 老后 |

**收口补口（`task-8`）又挖出三个真实缺陷**，都不是功能错，而是**返回结构与错误路径**：
1. `{"rule":null}` 落到自动徽章撞 CHECK → **500**（改为按生效值校验 → 422）。
2. `mapBadgeError` 三个新码无 case → **500**；`ErrBadgeRuleInvalid` 笼统映射让「层级太深」与「条件未知」显示同一句话。
3. `queued` 被控制器响应映射丢掉（OpenAPI `required` 有、wire 没有）——M2 的重算 toast 会显示 `undefined`。

**唯一未执行的证据**：完整 HTTP 往返（`POST /admin/badges/recompute` 的 401→200 全链路），缺管理员会话。store 语义与响应映射两层均已验证。

### 15.2 M2 收口结论（2026-11-12，剩余五项 + 三个落地缺陷）

M2 的编辑器主体此前已合入；本轮把**剩余五项**做完，并在过程中挖出三个实现缺陷（都不是新功能，是接线漏项）。

| 项 | 结论 | 证据 |
| --- | --- | --- |
| 1 `requirementText` 接卡片 | **完成** | 两处公开面真库值级有值；`SFBadgeCard` 描述位改两行 clamp；bun test / typecheck / admin-fw / v3-p0 / arch 全 exit 0 |
| 2 影响确认弹窗 | **完成** | `0` 与 `null` 反向互查、method 六条显式分支、失败可「仍然保存」、新建路径不请求 `/rule-impact` |
| 3 `queued` toast | **完成** | 三数不再相加；`queued>0` 必出「N 枚徽章已转入后台评估」；全 0 返回 `undefined` |
| 4 多条件 summary 字段提示 | **完成** | 判据镜像服务端 `fastPathRuleForText`（14 条真值表）；提醒不阻止保存；两语言成对提交 |
| 5 公开面 + 后台 390 视觉 QA | **完成** | 公开面 `scrollWidth == innerWidth`（390/1440）、clamp 实测 3 行→2 行；后台表格单滚动容器、聚焦可滚 182px/80px、越界控件 0 |

**本轮修掉的三个缺陷**（都是「键在值空」或「接线掉了」这类静默失败）：

1. **资料页 `requirementText` 恒为空串**：`postgres_store.go` 的 `ListUserBadges` 既没 select `rule`/`requirement_summary_*`，也没走 `Badge.Public()`，于是目录页有文案、资料页没有。修法是补三列 + 走 `Public()`，并把「可空 metric + 脏规则解码」抽成 `applyScannedBadgeFields` 让两处同口径。验收脚本 `:583` 旧判据只验 `type == "object"`，所以一直假绿（已由小审改成值级双向，`scripts/verify-badge-rules-acceptance.sh:576-673`）。
2. **`POST /admin/badges` 丢 `rule` 与 `requirementSummary`**：`CreateBadge` 的 INSERT 少三列 → automatic 建徽章撞 `badges_rule_check` 变 **500**，manual 的 summary 静默丢；同时 `validateBadgeInput` 仍要求 automatic 必带 legacy `metric`/`threshold`，纯 rule 徽章（`metric IS NULL`）根本建不出来。修法：INSERT 补三列（`$12::jsonb`）+ 新增 `normalizeNewBadge` 把「automatic 必须有规则」提到服务层（**422 而非 500**）+ `validateBadgeInput` 降级为字段级校验。真库实测：manual+summary 201 且回显、automatic 只给 rule 201、无 rule 422。
3. **后台表 390 裁切的第一次修法是假的**：先在 `<UTable>` 外包了一层 `overflow-x-auto` + `tabindex`，实测**没修好**——UTable 主题的 `slots.root` 自带 `overflow-auto`（`ui.BfHSFiIO.mjs:6514`），于是出现两个滚动容器：外层 `scrollW==clientW==326`（滚不动），内层才是真 scroller（`508/326`）。焦点落在外层，方向键什么都不会发生。正确修法是把 `tabindex="0"` / `role="region"` / `aria-label` 直接挂到 `<UTable>`（它 `v-bind="$attrs"`，`Table.vue:482`），只留一个滚动容器。groups 表的 UTable 同批补齐。

**两处契约/登记变更（留痕）**：

- **后台 wire 增 `rule` + `requirementSummary{zh,en}`**（`adminBadgeResponse`，恒下发、无 omitempty）：第 2、4 项都长在编辑弹窗里，而编辑器此前读不到既有规则（打开自动徽章显示「规则缺失」），summary 整体替换也会把已填文案清空。`rule` 用 `json.RawMessage` 原样透出 `RuleRaw`——重新序列化 `*ruleengine.Node` 会丢未知键、把脏规则变成 `null`。
- **UI 身份登记 4 → 6**：新增两个组件（抽出的 `SFAdminBadgeRuleSection` 与 `SFAdminBadgeRuleImpactDialog`）都要在 `docs/extensions/v3/catalog-identities.json` 的 `ui` 里登记，`ui-surfaces` 396 → 398。生成器「首个未登记项即中止」，所以只报一条时别以为只缺一条。
  `grep -o 's_f_admin_badge_rule_[a-z_]*' docs/extensions/v3/catalogs/ui-surfaces.json | sort -u | wc -l` 现在是 **6**（4 既有 + 2 新增），不是 4；要它等于 5 只能撤掉 Section 登记，那样校验立刻回红。

**影响估算口径（用户拍板 A）**：不新增草稿估算端点。`GET /admin/badges/:id/rule-impact` 估的是**库里已保存**的规则，所以弹窗必须写明「按当前已保存规则估算」，新建徽章没有 id → 不弹、不编数字。三态呈现互斥：有值（含 0）→「≈ N 人」；`null` → 「无法估算」+ 逐个列不可估条件且**不出现 ≈**；`partial` → 追加「只算了部分条件，是上界」。

### 15.3 第一刀收口结论：回扫进度可见（2026-11-12，残留 #15 关闭）

**结论**：`accepted`。验收载体 `scripts/verify-badge-backfill-progress.sh`（ok/FAIL/pend，**不接门禁**）+ 变异表 `scripts/verify-badge-backfill-mutations.tsv`；全量跑 **exit 0**；`pend` 已全部转 `ok`：最终口径（一次性集群 + 超管会话 + `--mutate`）**`ok 41 / FAIL 0 / pend 1`**（唯一 pend 是 `G-05`，脚本不代跑浏览器，由 390 实测补实）；同脚本另有 `ok 29`（带 DB+会话、无变异）、`ok 37`（无 DB/会话 + `--mutate`）、`ok 25`（基线）三种条件口径，**不可相加**（对照表见 `reports/qa-2026-11-12-badge-pend-conversion/mutations/README.md`）。**无行为级 FAIL**。报告：`reports/2026-11-12-badge-backfill-progress-acceptance.md`（40 条断言 = 37 ok + 3 pend，+ 11 条变异样本 + 溯源：代码基线 `4e1876331`，全量复跑 2026-11-12T20:5x+0800）。

| 面 | 落地 |
| --- | --- |
| 状态表 | 迁移 `202611120003_badge_backfill_runs.sql`：`badge_rule_backfill_runs(badge_id FK CASCADE, status running/completed/failed, last_user_id, failed_cursor_user_id, batch, error, started_at, updated_at, finished_at)` + 两索引 + owner 自对齐（照 §13#16）；Down 段物理回滚 |
| 状态机 | `Models/Badges/backfill_runs.go`：入队建 `running` 行 → 每页推进 `last_user_id` → 末页 `completed`+`finished_at` → 页错 `failed`+`failed_cursor_user_id`（river 仍会重试，故 `finished_at` 留空）→ 重试成功翻回 `running` 且清空游标；`Enqueuer==nil` 且未完成 → `failed`（消灭「静默停在半路」） |
| 端点 | `GET /admin/badges/backfill-status`（`backfill_controller.go`，静态段在 `/:badgeID` 之前）→ `{"items":[{badgeId,slug,name,status,lastUserId,failedCursorUserId,startedAt,updatedAt,finishedAt,error}]}`；空 `[]` + 200 + `no-store`；读侧以 runs 为驱动表 JOIN badges，**不碰 `user_badges`** |
| 契约 | 路由 421→**422**（`registry_test.go` tripwire + `route_order_test.go` 顺序断言）；OpenAPI `adminBadgeBackfillStatus` + `BadgeBackfillRun(List)`；identities `routes` 422 / `ui` 399；生成物重跑 |
| 前端 | `SFAdminBadgeBackfillRow.vue` + `SFAdminBadgesBadgesTab.vue` 筛选区**上方一行**（不新增卡片/页头）；三态文案 + 失败显示徽章名与用户 id + 刷新按钮（`aria-label`/`title`）；`queued>0` 后自动刷一次；i18n 双向 |
| `recompute` | 响应形状**未变**：`{granted,refreshed,queued}` 恰好三键（变异 MUT-6 把 `queued` 改名 → 断言红，证明三键被钉住） |

**Lead 记下的四个决定**（都属于「换个写法就埋雷」）：

1. **状态表照 `search_reindex_runs` 的既有形态**（runs 行 + status + 游标），不读 `river_job` 内部表：river 的完成行会被清理，且 `river_job.args` 不是「job 自己写的进度」而是入参。
2. **失败 ≠ 结束**：`failed` 不写 `finished_at`（river 还会重试），重试成功必须把 `failed_cursor_user_id` 清 0。错法：失败即写 `finished_at`，运营看到「已结束」而队列还在重试。
3. **不新增 `sforum_core_v1` 视图**：这是运维态进度（谁能回扫、卡在第几个用户），插件没有消费面；按 AGENTS.md「刻意缺失的读面需在模块笔记写明理由」——理由记在 `modules/badges.md`。
4. **`Runs == nil` 走「退化但不停摆」**：只记日志、不 panic、不让页任务失败；信号落在 `bootstrap/badges_wiring_test.go`（装配守卫断言 `Runs != nil`），因为静默退化正是本刀要消灭的形态。

**顺手修掉的既有漏项（M1 发货时就漏了）**：`contracts/openapi.yaml` 的 `paths` 没引用 `adminListBadgeConditions` / `adminBadgeRuleHealth` / `adminBadgeRuleImpact` 三条 path item —— 拼装出的文档里根本没有这三条，而 `validate-docs` 只校验「被文档提到的端点」，所以一直没红。已补 `$ref`（`validate-openapi-refs` 3296 refs 绿）。

**4 条测试缺口（task-12 已全部关闭，明细见报告 §5 台账）**：GAP-1 新端点缺 `no-store`/401/403 断言；GAP-2 缺「有授予、无 run 行 → `items` 为空」用例（「不从授予数反推」的反向证据）；GAP-3 缺「`done=true` 且 `Enqueuer=nil` → completed」与「入队下一页失败 → 返错且仍 running」；GAP-4 `queued>0` 自动刷新缺 loader 调用次数断言。

### 15.4 M3 收口结论：插件扩展（2026-11-12）

**结论**：`accepted`。三片实现 + 三份独立验收矩阵，`FAIL 0`；全仓门禁 `./scripts/test.sh --fast` 三线 **PASS**（api 452s / web 50s / static 16s，总 exit 0），四条静态闸各 exit 0（架构边界 2171 文件 / docs / V3 P0 422 routes·399 UI surfaces·99 rows / OpenAPI 3301 refs·65 文件）。

| 片 | 交付 | 验收 |
| --- | --- | --- |
| **M3.1** 声明层 | `ExtensionManifest/v3_badge_conditions.go`（struct + `BadgeConditionsMaximum=32` + `VariantsMaximum=16` + `rule #1..#6` + 独立的 `contractVersion drift` 报错）、`manifest.go` 字段、`schemas/manifest-v3.schema.json`（`properties` + `$defs` + **`allOf` if/then：有 badgeConditions 必有 backend**）、`json_schema.go` fragments、`v3_validate_platform.go` 挂载、`v3_normalize.go` 归一、能力 `badges.fact.write`、事实候选目录（`conditions/facts.go` + `bootstrap/badges_fact_catalog.go` 5s TTL 快照 + 冲突只记日志）、`PublishableDefinitions(candidates)`、`factCandidates` 顶层 DTO | `reports/2026-11-12-badge-m3-manifest-acceptance.md`：最终口径 **ok 48 / FAIL 0 / pend 0**（带会话 + `--mutate`），12 条变异 + 覆盖自检全红过 |
| **M3.2** Host 命令 | `Support/HostAPI/v2_command_badges.go`：ID `badges.fact.record`（**无 `@N`**）/ `Version "1"` / schema ref `…input@1`·`…result@1` / **PolicyId `sforum.badges.fact.record@1`**；能力闸门 `RequiredCapability=badges.fact.write`；输入 `userId/factKey/variant/delta/occurredAt/dedupeKey`（`userId`·`delta` 十进制字符串）；六 reason 逐字；域层拒绝落 `extension.badge_fact_record.denied`（best effort、不改返回码）；写路径复用 `RecordFact` | `reports/2026-11-12-badge-fact-record-acceptance.md`：基线 **ok 19 / FAIL 0 / pend 1**，`--mutate` 最终口径 **ok 34 / FAIL 0 / pend 0**（14 条变异全红过，含 PolicyId 去 `@1`、`ResourceId` 换成命令 ID、空 `dedupeKey` 落固定键三条自加变异） |
| **M3.3** SDK | `sdk/plugin/v2/badges.go`：`Host.RecordBadgeFact` / `RecordBadgeFactRequest` + `BadgeFactInput{UserID int64, …}`；命令 ID 常量无 `@N`、schema ref 带 `@1`；`delta`/`userId` 十进制编码、负数保留 `-`（SDK 不 clamp） | `go test ./sdk/...` exit 0；变异（改 float 编码）→ 断言红 |
| **M3.4** 夹具 | `sforum-query-surface-reference` 补两条 `badgeConditions` + `badges.fact.write` + `host_commands`；新夹具 `sforum-badge-facts`（静态清单 + `events[]` observe `reaction.created` + handler 调 SDK、`DedupeKey=deliveryId`、主体取负载 `actorUserId`） | `reference-packages-gate.sh` exit 0（`checked: 15`，两夹具都 `backend built → capabilities.resolved → manifest.ok → digest+validate+test passed`）；`reports/2026-11-12-badge-m34-fixtures-and-trust-qa.md` |
| **M3.5** 文档 | `docs/extensions/badge-plugin-facts.md`（canonical）+ `docs/zh-CN/…`（全文）+ `docs/en-US/…`（平行 stub）+ 三处索引登记；决策记录 `decisions/2026-11-12-badge-plugin-facts.md` | `validate-docs` exit 0 |
| **语义红线证明** | `rule_health` 的 `missing_fact_key` 原本**全仓零测试**，本轮补齐：失效引用进清单（`kind`/`path`/`ref` 值级）+ 规则 **`rule::text` 逐字节相等且 `updated_at` 不变** + 重装后 issue 消失且**不跑任何 job**；`rule_impact` 只回 `["fact.count"]`（不含 factKey）；`Progress` 仍 `{current,threshold}` 两数字键、无 `mode/satisfied/total` | task-15，两处变异各命中对应断言 |
| **信任披露视觉** | 产品路径（上传安装 → 启用 → 信任弹窗）390×844：`data-testid="extension-capabilities"` 段两条能力、`badges.fact.write` label 与中英披露句可见、`scrollWidth 390<=390`；`GET /admin/badges/conditions` 200，`factCandidates` 两条双语 label 非空、`variants` 值非空、`fact.count` 在 `items` 里 | `qa-2026-11-12-extension-trust-390/` 截图 + JSON |

**M3 期间 Lead 裁决（都已写进冻结 spec 的 Errata 2/3/4，正文未改）**：

1. `variant` 保存校验：引擎把 `ParamEnum + Options==nil` 定为**开放集**（`[]string{}` 是关闭空集，两者不能混），域层按已声明 variants 兜底；卸载后仍可保存。
2. 候选形状：`conditions.FactCandidate{Key,LabelZh,LabelEn,UnitZh,UnitEn,Compare,Variants}`；`GET /admin/badges/conditions` 增顶层 `factCandidates`（`items` 不动）。
3. `badges.fact.record` 输入补 **`userId`**（十进制字符串）：事实按用户行、调用方是扩展身份，异步 handler 里没有终端用户请求可推断。
4. 六个 `ErrorDetail.reason` 字面量冻结；`dedupeKey` 命中 → `inserted:false` 走 `Output`，不是错误。
5. 审计分层：`capability_denied` 由共享闸门在事务前返回协议错误（**不落审计**，与全仓一致）；域层拒绝落 `extension.badge_fact_record.denied`（best effort，失败不改返回码）。
6. `total` 回执 clamp 到 0，存储保留真实负值。
7. `rule #1`（非 plugin）不可达 → **不重排校验顺序**，按纵深防御保留；schema 补 `if/then` 使两条校验路径各自自洽。
8. key 长度：schema/DB 逐字 121、Go `manifestIDPattern` 更严 81（子集，安全侧偏差，注释写明）。

**顺手修掉的既有漏项**：`badges.fact.record` 的 `forum.badge_fact_write_denied` → 403 曾**没有生产者**（死映射）。改成命令用 sentinel（`FactWriteDenial`）构造拒绝、协议 reason 从错误链投影，并有 controller 级 403 断言——顶层码与 reason 从此同源。

**范围外观察（不在本刀处理，留给扩展平台）**：`DELETE /admin/extensions/:id` 返回 `uninstalled:true`，而库里仍留 `status=disabled` 墓碑 + `extension_versions` 1 行 + `plugin_runtime_publication_members` 1 行（该表 `BEFORE DELETE OR UPDATE` 触发器禁改，FK RESTRICT 钉住版本行）。这是「卸载保留不可变身份」的既有设计，语义易误读但**不可用 `DISABLE TRIGGER` 去清**（那是为让证据好看而破坏完整性）。

**新增残留**：`rule_health.label` 整屏中文硬编码（`RuleIssue.Label` 无 locale 维度），英文界面靠 `kind/path/ref` 定位；修法要 `labelKey` + 前端 i18n，属跨前后端契约变更，独立开单（见模块笔记残留 24）。
