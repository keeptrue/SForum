# 徽章规则引擎 M1：计划 vs 实现 偏差审计

Status: **read-only audit**（未改任何代码、未改计划）
审计时刻：2026-11-12 11:10 左右，工作区 `d502f19a3` + 未跟踪的 M1 文件
审计范围：计划 §4 / §5.2 / §5.2.1 / §5.2.2 / §6 / §7 / §9 / §12
判定只有四种：**一致** / **已批准偏离**（注明批准来源） / **未实现**（注明归属） / **偏离未报备**

> 说明：`task-6` 的三项修正（窗口日界、bookmark 可见性、factKey 声明校验）在审计时刻**代码中已落地且带测试**，但看板仍显示 `in_progress`。本报告按代码现状判定，不按看板。

---

## 0. 判定汇总

| 计划章节 | 条款数 | 一致 | 已批准偏离 | 未实现 | 偏离未报备 |
| --- | --- | --- | --- | --- | --- |
| §4.1 `badges.rule` | 5 | 5 | 0 | 0 | 0 |
| §4.2 插件事实表 | 2 表 + 3 索引 | 全部 | 0 | 0 | 0 |
| §4.3 回滚表 | 1 | 1 | 0 | 0 | 0 |
| §4.4 视图 + HostQuery | 3 | 3 | 0 | 0 | 0 |
| §5.2 条件目录（12 条） | 12 | 12 | 0 | 0 | 0（但计划内部 3 行 stale，见 §8） |
| §5.2.1 参数形状（冻结表） | 13 | 13 | 0 | 0 | 0 |
| §5.2.2 口径裁决（6 + 批量策略） | 7 | 7 | 0 | 0 | 0 |
| §6 模块拆分 | 16 文件 + 1 附注 + 3 计划外 | 9 | 3 | 7 | 1 |
| §7 追溯与回扫 | 4 | 1（政策） | 0 | 3 | 0 |
| §9 API 与契约 | 9 | 3 | 0 | 6 | 0 |
| §12 验收矩阵 | 7 类 | 4 | 0 | 3 | 0 |

**只有 1 条代码级偏离未报备**（§6 的 `rule_eval.go`），另有 1 条**接线缺口**需要派活（见 §7.2）。其余全部落在「未实现（task-4）」或「计划内部矛盾」。

---

## 1. §4 数据模型

| 计划条款 | 实现现状 | 判定 |
| --- | --- | --- |
| §4.1 三列 `rule` / `requirement_summary_zh` / `requirement_summary_en` | `202611120002_badge_rules.sql:22-24`，`rule JSONB` 可空、两个 summary `NOT NULL DEFAULT ''` | 一致 |
| §4.1 迁移 **10 枚** automatic 转单条件规则 | `:42` `WHERE grant_mode='automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND rule IS NULL`，`jsonb_build_object` 生成 `{"v":1,"root":{type,params:{metric,window:{kind:"all"}},cmp:{op:"gte",value}}}`。内置 12 枚里 2 枚 manual，实际命中 10 | 一致（但迁移内联注释写「12 枚」，见 §7.3） |
| §4.1 CHECK 改为 `automatic → rule IS NOT NULL` / `manual → rule IS NULL` | `:49-53` 先 `DROP CONSTRAINT IF EXISTS badges_rule_check` 再 `ADD CONSTRAINT`，表达式与计划逐字一致 | 一致 |
| §4.1 `badges_metric_check` 与两列保留只读 + `COMMENT` | `metric_check` 未动；`:60-65` 四条 `COMMENT ON COLUMN`，其中 metric/threshold 明写 `LEGACY: not maintained by the write path since 202611120002` | 一致 |
| §4.1 部分索引 `badges_enabled_auto_idx` 保留 | 迁移内零命中该索引名（既不删也不重建）＝保留原样 | 一致 |
| §4.2 `badge_facts` 列与约束 | `:68-79`：`fact_key` 正则 CHECK、`delta <> 0`、`source_extension_id`、`dedupe_key`、`created_at` 与计划 §4.2 逐列一致 | 一致 |
| §4.2 两个索引 | `:81-87`：`badge_facts_dedupe_idx (user_id,fact_key,variant,dedupe_key) WHERE dedupe_key<>''`（部分唯一）、`badge_facts_window_idx (user_id,fact_key,variant,occurred_at DESC)` | 一致 |
| §4.2 `user_fact_counters` | `:90-100`：PK `(user_id,fact_key,variant)`、`total` 无 CHECK（允许负）、`last_occurred_at`、`updated_at` | 一致 |
| §4.3 `badge_rules_rollback` | `:103-110`：`badge_id PK`、`rule JSONB NOT NULL`、`legacy_metric`、`legacy_threshold`、`created_at`；`:112-116` 从 `rule IS NOT NULL` 回填 | 一致 |
| §4.4 `forum_badges` 追加三列 | `:120-128`：在末尾追加 `rule`、`requirement_summary_zh`、`requirement_summary_en`，`metric/threshold` 保留。列顺序与计划「末尾追加」一致 | 一致 |
| §4.4 `forum_user_badge_facts` | `:137-145`：`user_id/username/display_name/fact_key/variant/GREATEST(total,0)`，只 JOIN `users.status='active'`，无 delta/dedupe_key/source_extension_id | 一致 |
| §4.4 HostQuery `sforum.core.public_user_badge_facts.list` + schema | `v2_query_postgres.go:723-750`（6 Fields 与视图同序、`From` 指向新视图、`RequiredFilters:["user_id"]`）；`query_registry_core_schema.go:197-207`（6 个 string 属性，与 Fields 等长） | 一致 |

---

## 2. §5.2 / §5.2.1 / §5.2.2 条件与口径

### 2.1 条件目录（§5.2 的 12 行）

`conditions/registry.go` 注册 13 条 = 12 内置 + `fact.count`（§5.2.1 明确 `fact.count` 是「插件条件的唯一入口」，计划 §5.2 表未列它）。12 条的 key、`ValueKind`、`AllowedCompare` 与 §5.2 表逐条一致：

| 计划行 | 实现 | 判定 |
| --- | --- | --- |
| `activity.count`（6 metric、window） | `registry.go:185-195`，`metric` enum = `Metrics()` 六项，`window` 必填 | 一致 |
| `activity.streak`（minDays） | `:197-206`，`minDays` int(1..3650) | 一致 |
| `object.engagement`（布尔） | `:208-216`，`action` enum 三项 + `topicId` int(min 1)，`ValueBoolean` | 一致 |
| `object.authoring`（计数 gte/lte） | `:218-229`，`targetKind` enum + `targetSlug` objectRef(refKind=category) + `window` | 一致 |
| `identity.account_age`（计数） | `:231-237`，**无参数** | 一致（以 §5.2.1 为准；§5.2 表写 `days` 是 stale，见 §8） |
| `identity.email_verified` / `avatar`（布尔、无参） | `:239-243`、`:263-267` | 一致 |
| `identity.oauth_link`（providerId） | `:245-252`，objectRef(refKind=provider) | 一致 |
| `identity.role`（roleKey） | `:254-261`，objectRef(refKind=role) | 一致 |
| `content.pinned` / `content.hidden`（计数、无参） | `:269-283`，均 `countCompare()` = {gte,lte} | 一致 |
| `badge.owned`（slug） | `:285-292`，objectRef(refKind=badge) | 一致 |

### 2.2 参数形状（§5.2.1 冻结表）

13 行逐条核对，**全部一致**，含三条容易做错的：`object.engagement` 的 `topicId` 是 `int` 不是 objectRef（`registry.go:208-216`）；`ParamSpec` 带 `RefKind`（`RuleEngine/catalog.go:55`）；`fact.count` 的 `variant` 是 `Required:false` 的 enum（`registry.go:299`）。

### 2.3 口径裁决（§5.2.2）—— 逐条看 SQL 的 WHERE，不看注释

| # | 计划条款 | 实现（可核对的 SQL 位置） | 判定 |
| --- | --- | --- | --- |
| 1 | 窗口获赞排除自赞 | `resolve_activity.go:45` 与 `:51` 两个获赞子查询均 `AND r.user_id <> $1` | 一致 |
| 2 | 连续天数不用 `CURRENT_DATE` | `resolve_activity.go:173-180` 用 `$2::date`（`visited_on <= $2` 与 `visited_on >= $2-1` 的收尾判定）；`days_visited` 窗口 `:65` 用 `$3::date`；`identity.account_age` `resolve_identity.go:32` 同样 `$2::date` | 一致 |
| 3 | `content.hidden` 只认 topics 的 `status='hidden'` | `resolve_content.go:23` `WHERE author_user_id = $1 AND status = 'hidden'`，无 `<> 'active'`、无 comments JOIN | 一致 |
| 4 | `content.pinned` = 当前仍置顶 | `resolve_content.go:17` `WHERE author_user_id = $1 AND is_pinned` | 一致 |
| 5 | `object.authoring` 不按 `tags.status` 过滤 | `resolve_object.go:99-102`：`JOIN tags tg ... WHERE t.author_user_id = $1 AND tg.slug = a.slug`，无 status 谓词 | 一致 |
| 6 | `identity.role` 用 `roles.key` + `is_enabled` | `resolve_identity.go:132` `WHERE ur.user_id = $1 AND r.key = candidates.role_key AND r.is_enabled` | 一致 |
| 7 | 批量策略：同 type 一次查询 | `batch.go` 的 `Prepare` 按 type 分组 + `unnest` 参数化；`TestPrepareServesFromMemo` 用「Prepare 后关掉 pool 仍能出结论」证明评估期零查询 | 一致 |

**额外核实（不在 §5.2.2 但已裁决）**：`bookmark` 的 `visibility='public'` 已按 lead 裁决移除（`resolve_object.go:26-28` 现在只有 `user_id/target_type/target_id`），并有 `TestObjectEngagementBookmarkIgnoresVisibility` 钉住；`factKey` 未声明返 error 已落地（`resolve_facts.go:54-57` + `TestFactCountUndeclaredKeyIsUnknown`）。这两条属于「计划 §5.2.2 之后的新裁决」，计划尚未回写（见 §8）。

---

## 3. §6 模块拆分（文件清单）

| 计划文件 | 实际 | 判定 |
| --- | --- | --- |
| `RuleEngine/ast.go` | 拆成 `types.go`(118) + `decode.go`(115) | 已批准偏离（`task-2` 描述即如此拆，「职责未变，仅文件更细」） |
| `RuleEngine/validate.go` | 存在（311 行） | 一致 |
| `RuleEngine/eval.go` | 存在（208 行，含 P-12 形状兜底） | 一致 |
| （计划未列）`RuleEngine/catalog.go` | 存在（71 行） | 已批准偏离（`task-2` 交付清单列了它） |
| `conditions/registry.go` | 存在（280 行） | 一致（责任描述差异见 §8.3） |
| `conditions/resolve_activity.go` | 存在（193 行） | 一致 |
| `conditions/resolve_object.go` | 存在（168 行） | 一致 |
| `conditions/resolve_identity.go` | 存在（197 行） | 一致 |
| `conditions/resolve_content.go` | 存在（100 行） | 一致 |
| `conditions/resolve_facts.go` | 存在（96 行） | 一致 |
| （计划未列）`conditions/batch.go` | 存在（258 行） | 已批准偏离（`task-3` 交付清单列了它） |
| `Badges/rule_store.go` | **不存在** | 未实现 → `task-4` |
| `Badges/rule_impact.go` | **不存在** | 未实现 → `task-4` |
| `Badges/facts_store.go` | **不存在** | 未实现 → `task-4` |
| `Badges/evaluate.go`（改） | 已改：`satisfied` 双分支（`evaluate.go:103-127`），快路径只认「单 `activity.count` + `window.all` + `gte`」（`:132-152`），`Rule == nil` 直接 false（避开 P-04 的 metric 陷阱） | 一致 |
| （计划未列）`Badges/rule_eval.go` | 存在（36 行） | **偏离未报备**（见 §7.1） |
| `Jobs/Badges/backfill_rules.go` | **不存在**（`Jobs/Badges/` 只有既有 `evaluate_user.go`） | 未实现 → `task-4` |
| `Controllers/Badges/controller.go`（改） | 未改 | 未实现 → `task-4` |
| `Controllers/Badges/dto.go`（改） | 未改（`dto.go:91-93` 仍是旧的 `Progress` 渲染） | 未实现 → `task-4` |
| 附注：新逻辑拆 `rule_controller.go` | **不存在** | 未实现 → `task-4` |
| （计划 §6 无 `rule_health.go`，`task-4` 有） | 不存在 | 计划内部矛盾（见 §8.3） |

---

## 4. §7 追溯与回扫

| 计划条款 | 实现现状 | 判定 |
| --- | --- | --- |
| 保存规则 → 同步写库 → **异步**入队 `badges.backfill_rules` | 无规则保存路径、无 job | 未实现 → `task-4` |
| `GET /admin/badges/:id/rule-impact` 返回 `{estimated, method}` | 端点与估算都不存在 | 未实现 → `task-4` |
| 回扫按 `user_id` 游标分批（默认 500），幂等靠 `ON CONFLICT DO NOTHING` | 不存在 | 未实现 → `task-4` |
| **明确不做**：规则改严不自动收回 | 现无任何收回逻辑；`GrantFromRules` 只增不减 | 一致（政策未被违反） |

---

## 5. §9 API 与契约

| 计划条款 | 实现现状 | 判定 |
| --- | --- | --- |
| `GET /admin/badges/conditions` | 路由不存在；`conditions.Core()` 的 `PublishableDefinitions` 已备好，但无人调用 | 未实现 → `task-4` |
| `GET /admin/badges/:id/rule-impact` | 不存在 | 未实现 → `task-4` |
| `GET /admin/badges/rule-health` | 不存在 | 未实现 → `task-4` |
| `POST /admin/badges` / `PATCH /:id` 的 `rule` 三态 + `requirementSummary` | `dto.go` 无 `rule`/`requirementSummary` 字段；`Badge` 结构体内部已有 `Rule`/`RuleRaw`/`RequirementSummary*`（均 `json:"-"`） | 未实现 → `task-4` |
| `POST /admin/badges/recompute` 保留、内部走引擎 | 端点未动；`RecomputeAll` 仍是全量同步路径 | 一致（「保留」已满足；「走引擎」随 `task-4`） |
| 公开面 `PublicBadge` 增 `requirementText{zh,en}` | `types.go:99-112` 无该字段 | 未实现 → `task-4` |
| **`Progress` v1 不变** | `PublicBadge` 无 Progress 字段，DTO 层 `dto.go:91-93` 未改 | 一致 |
| 新错误码 5 个（`too_complex` / `unknown_condition` / `cycle` / `reference_invalid` / `badge_fact_write_denied`） | `errors.go` 只有既有的 `forum.badge_rule_invalid`；`messages.go` 只有它 | 未实现 → `task-4`（其中 `badge_fact_write_denied` 实为 M3，见 §8.4） |
| 公开面不泄露 AST | `Badge.Rule`/`RuleRaw` 均 `json:"-"`；`PublicBadge` 无 rule 字段 | 一致（M1.4-13 已 PASS） |

---

## 6. §12 验收矩阵

| 矩阵行 | 实际存在 | 判定 |
| --- | --- | --- |
| 纯函数：AST 解码/校验/求值真值表 | `RuleEngine/` 三个测试文件 32 个顶层用例（decode 6 / validate 11 / eval 15） | 一致 |
| PG 集成：每个条件类型 allowed/denied | `conditions/` 三个集成测试 13 个用例，覆盖 12 内置 + `fact.count`，含自赞、软删窗口、bookmark 可见性、未声明 factKey | 一致 |
| 迁移：up/down 可逆（单条件）/ 多条件下拒绝并给 HINT | `badge_rules_migration_test.go`（静态契约）+ `CMD-REV` 真跑（down 11 项、up 18 项、X-04b 拒绝、X-04c 反推） | 一致 |
| HTTP：422 各码 / 影响估算 / 健康清单 / `rule` 三态 / 公开面不泄露 | 仅「公开面不泄露」有（M1.4-13 PASS），其余无端点 | 未实现 → `task-4` |
| job：回扫可续、幂等、计数正确 | 无 job | 未实现 → `task-4` |
| 前端：`badgeRule.ts` / 编辑器 / i18n 双向断言 | 属 M2，`badgeRule.ts` 与 `components/admin/badges/rule/*` 已在工作区（M2 进行中） | 未实现（不属 M1）→ M2 |
| 门禁：`test.sh --fast` / 架构边界 / OpenAPI refs | 已按 `task-1`~`task-5` 完成记录跑过 | 一致 |

---

## 7. 偏离未报备（需要 lead 处理）

### 7.1 `Badges/rule_eval.go` 不在计划 §6 的文件清单里

- **事实**：36 行新文件，职责是「把 `conditions.Resolver` 接进 `PostgresStore.EvaluateRule`」，即 `Store` 侧对 `RuleResolver` 接口的实现。
- **影响**：低。计划 §6 只列了 `evaluate.go`（改），没写这条接线落在哪个文件。
- **建议**：在计划 §6 补一行，或并入 `rule_store.go`（`task-4` 落地时决定）。

### 7.2 `EvaluateRule` 没有把「已声明 factKey 集合」传给 resolver（接线缺口）

`app/Models/Badges/rule_eval.go:21`
```go
	resolver, err := conditions.NewResolver(s.pool, userID)
```
而 resolver 的签名是 `NewResolver(pool, userID, factKeys ...string)`（`batch.go:53`），第三个参数正是 `task-6` ③ 用来区分「未声明 → error」的集合。

- **后果**：生产路径上 `factKeys` 恒为空 → **任何 `fact.count` 叶子都会变 unknown**。M1 没有插件条件，所以**当前无害且 fail-closed**；但 M3 一旦上线 `badgeConditions`，所有插件事实条件都会恒 unknown，徽章永不发，且现象是「规则存得下、永远不满足」——最难查的那类。
- **归属**：`task-4`（`rule_store.go` 落规则保存路径时顺手接）或 M3 第一件事。**建议现在就在 `task-4` 的验收里加一条断言**：`EvaluateRule` 能接受并透传 declared factKeys。
- **判定**：未实现（接线），非计划偏离——计划没有写这一层，但 `task-6` 的修复只有在接上之后才对生产生效。

### 7.3 迁移内联注释写「12 枚内置自动徽章」

`202611120002_badge_rules.sql:34` 附近注释：「12 枚内置自动徽章转成单条件规则」，而同一迁移的 `WHERE grant_mode='automatic'`（`:42`）实际命中 **10** 枚，计划 §4.1 也写 10 枚（2 枚 manual 不转）。

- **影响**：文档级。`task-1` 完成记录已声明「实际是 10 枚」，但迁移里的注释没跟着改，下一个人读到会以为漏转 2 枚。
- **判定**：偏离未报备（注释级）。建议随下一次动该文件的迁移一起改；不要为它单独开迁移。

---

## 8. 计划内部矛盾（需 lead 决定改计划，不是改代码）

### 8.1 §5.2 表与 §5.2.1 冻结表冲突（3 行）

| 条件 | §5.2 表 | §5.2.1 冻结表 | 实现 |
| --- | --- | --- | --- |
| `identity.account_age` | params `days` | **无参数**（并明写「不要加 days 参数」） | 无参数 |
| `object.engagement` | `target{topic,id}` | `action` + `topicId` | 按 §5.2.1 |
| `object.authoring` | `target{category\|tag, slug}` | `targetKind` + `targetSlug` + `window` | 按 §5.2.1 |

§5.2.1 是后写的冻结版且明确「前后端同一份」，实现跟随它是对的。**但 §5.2 表没同步**，`task-3` 当时也为此单独提过 `identity.account_age` 的疑问。建议把 §5.2 表三行改成与 §5.2.1 一致，或直接标注「参数形状见 §5.2.1，本表只列语义」。

### 8.2 §5.2.2 未回写两条新裁决

`bookmark` 去掉 `visibility` 过滤、`factKey` 未声明返 error，都是 §5.2.2 写完之后才裁决的（`task-6` ①②③）。§5.2.2 现在仍只有 6 条 + 批量策略，新裁决只活在任务描述与代码注释里。建议补成第 7、8 条。

### 8.3 §6 的 `rule_health` 归属与 `registry.go` 责任描述

- §6 没有 `rule_health.go` 这一行，把「引用健康检查」挂在 `rule_store.go` 的责任里；`task-4` 却单列 `rule_health.go`。两者不一致，实现方按任务描述走没问题，但计划读起来会以为漏了一个文件。
- §6 写 `registry.go` 的责任含「i18n label key」，实现是**双语文案直接写 Go**（`registry.go:84-89` 的 `LabelZh/LabelEn/DescribeZh/DescribeEn/UnitZh/UnitEn`），理由是避免撞 `adminBadgesI18n.test.ts` 的双向断言（`registry.go:76-78` 注释 + 验收 X-01「M1 未加 locale 键，符合约定」）。**判定：已批准偏离**，但计划文字未改。

### 8.4 `forum.badge_fact_write_denied` 混在 M1 的错误码清单里

§9 把 5 个新错误码一起列在 M1，但 `badge_fact_write_denied` 是 `sforum.badges.fact.record@1` 的拒绝码，属 M3。M1 实际只需前 4 个。建议在 §9 标注归属，免得 `task-4` 为它写一套没有调用方的文案。

### 8.5 `queued` 字段在计划里找不到

你在审计要求里提到「`queued` 加字段」是已批准偏离的例子，但我全文检索计划（`rg queued`）**零命中**，代码里也没有。若它指的是「保存规则后响应里带 `queued: true`」，那是一条**计划未覆盖的新契约**，需要补进 §9 而不是当成偏离记录。

---

## 9. 已批准偏离（备查，不需处理）

| 偏离 | 批准来源 |
| --- | --- |
| `ast.go` 拆成 `types.go` + `decode.go` | `task-2` 任务描述即如此拆分（「职责未变，仅文件更细」） |
| 新增 `RuleEngine/catalog.go` | `task-2` 交付清单 |
| 新增 `conditions/batch.go` | `task-3` 交付清单 |
| `registry.go` 文案写 Go 而非 i18n key | 验收 X-01 + `registry.go:76-78` 注释；**计划未同步**（§8.3） |
| `badgeConditions` 改为「给 `fact.count` 注册候选」而非新条件类型 | lead 2026-11-12 裁决（`reports/2026-11-12-badge-conditions-manifest-spec.md` §7.1） |
| `Progress` v1 不扩 | 计划 §9 本身即如此（用户决策 #14），实现未动 |
| `metric/threshold` 保留只读一版而非立即删除 | 计划 §4.1 本身即如此 |

---

## 10. 审计未覆盖

- `task-4` 正在写的文件（`rule_store.go` / `rule_impact.go` / `rule_health.go` / `facts_store.go` / `backfill_rules.go` / `rule_controller.go` / `dto.go`）在审计时刻不存在，无法比对实现质量，只能比对「是否存在」。
- 前端 M2 不在本次范围（工作区已有 `badgeRule.ts` 等，属另一条线）。
- 性能数值不重测；沿用 `task-3` 的 10k 实测（1.98ms / 2.46ms）与 `M1-EXIT-PERF` 的 pend 状态。
