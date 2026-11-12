# 2026-11-12 徽章规则引擎 M1 交接

Status: **M1 收口完成（七片全落地）、M2 全部完成并验收（含剩余五项）、第一刀「回扫进度可见」与 M3 插件扩展均已收口并独立验收**。代码提交：`4457106e2`（apps）/ `082f98624`（web）/ `4e1876331`（contracts）。计划：`knowledge/plans/2026-11-12-badge-rule-engine.md`（§15 进度表最准）。验收矩阵：`knowledge/reports/2026-11-12-badge-rule-engine-acceptance.md`。口径 SQL 参考：`knowledge/reports/2026-11-12-badge-rule-engine-condition-sql.md`。M3 契约（已冻结）：`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`。

## Changed

一句话：徽章从「单一 metric + threshold」升级成「JSONB 规则树 + 13 条条件目录 + 三值求值」，迁移 `202611120002_badge_rules.sql` 把 10 枚内置 automatic 徽章等价转换成规则（2 枚 manual 不动），并新增插件事实表。

五个协作方的职责边界与落地状态（`rule_store` 一行合并了 `rule_store`/`rule_impact`/`rule_health`/`facts_store` 四个文件）：

| 协作方 | 位置 | 职责 | 状态 |
| --- | --- | --- | --- |
| `RuleEngine` | `apps/api/app/Support/RuleEngine/`（types/decode/validate/eval/catalog） | 纯函数：AST 解码、校验（深度 5/叶子 32/节点 64/days 1..3650）、Kleene 三值求值。**不 import 任何 domain 包** | 已落地，7 条断言全绿 |
| `conditions` | `apps/api/app/Models/Badges/conditions/`（registry + batch + resolve_{activity,content,identity,object,facts}） | 把叶子翻译成 SQL；`Resolver.Prepare` 按 type 分组**每类一次查询**并 memo，单叶可直查 | 已落地，13 条定义（12 内置 + `fact.count`） |
| `rule_store`（校验/持久化） | `Models/Badges/{rule_store,rule_impact,rule_health,facts_store}.go` | 规则保存校验（含 `badge.owned` 环检测）、`badges.rule` 读写、影响估算 | **已落地**：`rule_store.go`（读写 + 保存校验 + 环检测）/ `rule_impact.go` / `rule_health.go` |
| `evaluate` | `apps/api/app/Models/Badges/evaluate.go` + `rule_eval.go` | 双分支：`fastPathRule` 识别「单 `activity.count` + `window.all` + `gte`」直接读计数器（10 枚内置零回归）；其余走 `ruleengine.Eval`；`EvaluateRule` 透传已声明 factKey | 已落地 |
| `backfill`（回扫） | `app/Jobs/Badges/backfill_rules.go` | 规则变更后按用户回扫、可续、幂等 | **已落地**：`Jobs/Badges/backfill_rules.go`（游标可续 + `ByArgs` 去重）；`GrantFromRules` 仍是全量同步路径 |

> **状态提示（2026-11-12 收口）**：`task-4`（rule_store/backfill/HTTP/错误码）与 `task-6`（三处口径）均已合入。`resolve_object.go` 的 `bookmark` 已无 `visibility` 谓词、`resolve_facts.go` 对未声明 factKey 返 error、`days_visited` 窗口下界走 Go 传参——三处都有回归测试，可以当定稿读。

## Decisions（七个「看起来对但会错」的口径）

每条都给了「错法长什么样」，改之前先读 `knowledge/modules/badges.md` 的对应注脚。

1. **窗口获赞必须排除自赞** `r.user_id <> $1`。错法：照抄累计口径漏掉这一条 → 窗口版比计数器多算自赞，「累计 vs 最近 30 天」互相打脸。权威形状抄 `activity_store.go:156-171` 的 `Recompute`。
2. **streak 的「今天」必须由 Go 传 `$n::date`**。错法：SQL 写 `CURRENT_DATE` —— 那是 PG 会话时区，而 `visited_on` 是 API 进程本地时区（`visits.go:86-90` 的 `now: time.Now`，装配无时钟注入）。API 设 `TZ=Asia/Shanghai` 而 PG 是 UTC 时，跨零点 8 小时会把刚记的今天排除 → 连续 7 天零点后立刻误判断签。
3. **`content.hidden` 只认 `topics.status='hidden'`，且只统计主题**。错法：写成 `status <> 'active'`（把 `deleted`/`pending`/`rejected` 也算进去）、或 JOIN `comments`（评论的 hidden 是另一个信号，v1 刻意不收）。语义是「被管理员隐藏」，**不是**「违规次数」——`moderation_reports` 无 action 列、`audit_events` 无内容目标列，精确版不存在。
4. **`content.pinned` 是「当前仍被置顶」**。错法：文案写「曾被置顶」。库里只有 `is_pinned` 布尔值，无 `pinned_at`、无审计列，历史口径没有数据来源。
5. **`object.authoring` 不按 `tags.status` 过滤**。错法：加 `g.status='active'` → 运营下架一个标签，已发徽章的依据凭空消失，同一规则两次评估结论不同。
6. **`identity.role` 用 `roles.key` 且必须带 `r.is_enabled`**。错法：用 `alias`（无 UNIQUE、可改，`roles_test.go:9` 钉着「alias 可改」）→ 某次改名后规则静默失效；漏 `is_enabled` 则与授权判定不一致（`postgres_store_permissions.go:26-29`）。
7. **两种失效语义相反，不要「统一」**（`conditions/resolve_facts.go:10-17` 有原文）：引用对象（主题/分类/标签/角色/provider/徽章）不存在 → 返**确定值 0/false**（「没做过」）；条件本身失效（factKey 无扩展声明）→ **必须返 error**（0 会被 `lte` 判满足、被 `not` 判 true，都是 fail-open）。错法：把两者都返 0，或都返 error（后者会让整棵树 unknown，徽章永远不发）。

## 验收怎么跑

三个脚本都**不接** `./scripts/test.sh`，也**不是** `tests/validate-*` 门禁——它们靠 `pend` 表达「切片未落地」，接进门禁会把整条门禁变红。

| 脚本 | 验什么 | 现在的结果 |
| --- | --- | --- |
| `./scripts/verify-badge-rules-acceptance.sh` | 三态 ok/FAIL/pend 的断言矩阵（M1.1/1.2/1.3/1.4 + 横切 X + P）。FAIL 只留给「代码在、行为错」，缺文件/缺夹具一律 pend | 最近一次实跑 `ok 19 / FAIL 0 / pend 32`（报告 §9.1）；但汇总表里 M1.3 仍记 1 FAIL（软删窗口夹具缺）+ 2 pend，未复验 |
| `./scripts/verify-badge-rules-baseline.sh` | 一次性集群重放迁移前基线：造边界夹具 → 快照 → 迁移 → 比对（M1.1-11/12） | 需一次性集群，dev 库已迁移无法补拍 |
| `./scripts/verify-badge-rules-perf.sh` | 10k 用户夹具的 EXPLAIN + P50/P95 + 回扫吞吐 | `--strict` 才把 pend 转 exit 4 |

**~~`M1-EXIT-PERF` 是收口门~~ —— 已闭（2026-11-12）**：默认 10k 规模（1,999,800 行）已真跑，`PERF-01` 走 `Index Scan using content_reactions_roster_idx`（0.189ms）、`PERF-03` p50 2.19ms / p95 3.47ms（预算 50ms），原文见计划 §15「收口遗留」。它不是日常循环（几分钟）。判据已按实测放宽为「计划中出现 `content_reactions_roster_idx` 且 P95 在预算内」，**不要**要求 Index Only Scan——加了 `r.user_id <> $1` 之后那不可能，会假红。

其余门禁：`./scripts/test.sh --fast`、`node tests/validate-architecture-boundaries.mjs`、`ruby scripts/validate-openapi-refs.rb`、`cd apps/web && bun test && bun run typecheck`。

## 已知残留（计划 §13 的 12 条，压缩）

1. 窗口获赞**不要**加 `content_reactions(target_type,target_id,created_at)` 索引；现有计划（roster_idx + heap 过滤）是正确代价，10k 实测 1.98ms。
2. 影响人数是估算（AND 取最严叶子、OR 取和），文案必须写「≈」。
3. `RecomputeAll` 仍在 HTTP 内同步（既有残留），规则触发的回扫必须走 job。
4. 规则改严**不自动收回**已授予，属产品语义。
5. 插件 RPC 判定器（A 路径）未做，fail-closed 只覆盖「事实查询失败」。
6. 查看类条件未做（不建 `user_topic_reads`）。
7. 采纳/精华/热榜条件未做（需 Forum 域字段）。
8. 举报类条件不做。
9. trust levels 未共用引擎（引擎已独立成包，可平移）。
10. 公开面自然语言是双列（zh/en），缺一种时回落自动生成句。
11. `PublishableDefinitions(factKeys []string)` 不带 variants → M3 需扩成 `FactCandidate{Key, Variants}`。
12. `site.timezone` 全仓零消费（`visited_on` 用进程本地时区）——站点级缺陷，**本刀不修**，应单开一刀。

## M1 收口结果（2026-11-12）

七片全部落地：迁移/视图/HostQuery（`task-1`）、纯 AST（`task-2`）、P-12 形状兜底（`task-5`）、
条件目录与五组解析器（`task-3`）、三处口径修正（`task-6`）、规则存储/回扫/HTTP/契约（`task-4`）、
偏差审计（`reports/2026-11-12-badge-rule-engine-m1-deviation-audit.md`）。

- **路由计数 418 → 421**（conditions / rule-health / rule-impact 三条）。
- **错误码 4 个**双语文案：`_too_complex` / `_unknown_condition` / `_cycle` / `_reference_invalid`。
- **两个面已分离**：`Core()` 13 条（校验/求值）vs `PublishableDefinitions(factKeys)`（展示）。
- **偏差审计的接线缺口已补**：`EvaluateRule` 现在把已声明 factKey 透传给 `NewResolver`。
- ~~**仍未取证**：`M1-EXIT-PERF`~~ —— **已闭（2026-11-12）**：10k 规模（1,999,800 行）真跑的 `EXPLAIN (ANALYZE, BUFFERS)` + P50/P95 已落地，见计划 §15「收口遗留」。
- 口径与陷阱已固化进 `modules/badges.md`；四个决定（布尔树 / 混合事实层 / `fact.count` 注册模型 /
  down 可拒绝）见 `decisions/2026-11-12-badge-rule-engine.md`。

## M2 剩余五项收口（2026-11-12）

五项全部落地：`requirementText` 接卡片、影响确认弹窗、`queued` toast、多条件 summary 字段提示、公开面 + 后台 390 视觉 QA。
过程中挖出并修掉**三个实现缺陷**（都不是新功能，是接线漏项）：

1. **资料页 `requirementText` 恒空串**：`ListUserBadges` 没 select `rule`/`requirement_summary_*`、没走 `Badge.Public()`。目录页有、资料页没有。验收脚本 `:583` 旧判据只验 `type=="object"` 所以一直假绿（已改值级双向）。
2. **`POST /admin/badges` 丢 `rule` 与 `requirementSummary`**：`CreateBadge` INSERT 少三列 → automatic 变 **500**、manual 的 summary 静默丢；`validateBadgeInput` 还要求 automatic 必带 legacy `metric`/`threshold`，纯 rule 徽章建不出来。已修：INSERT 补列 + `normalizeNewBadge` 出 422。
3. **后台表 390 裁切的第一次修法是假修复**：外层再包 `overflow-x-auto` 变成两个 scroller（外层滚不动），`tabindex` 落错元素。正确做法是把 `tabindex/role/aria-label` 挂到 `<UTable>` 自身（它 `v-bind="$attrs"` 到 root）。

**两处契约/登记变更**：后台 wire 增 `rule` + `requirementSummary{zh,en}`（恒下发，`rule` 用 `RawMessage` 原样透出）；`catalog-identities.json` 的 `ui` 补 2 条（`_rule_section`、`_rule_impact_dialog`），`ui-surfaces` 396→398，那条 grep 现在是 **6** 不是 4。

**证据**：`reports/2026-11-12-badge-m2-remaining-acceptance.md`（独立复核 + 假绿修复）、`reports/2026-11-12-badge-m2-visual-qa.md` + `qa-2026-11-12-badge-m2/`（截图与实测数字）。门禁：`bun test` / `typecheck` / `validate-admin-framework` / `validate-v3-p0-catalogs` / `validate-architecture-boundaries` 全 exit 0；`test-api-isolated.sh`（Models/Badges + conditions + Http/Controllers/Badges）exit 0。

**环境注意**：本机 `air` 监督器曾连续失败重建（`apps/api/tmp/build-errors.log` 只有 `exit status 1`），旧二进制继续服务会让「改了但没生效」看起来像代码错。判据：`ps -o lstart= -p $(pgrep -f tmp/sforum-api)` 的启动时间必须晚于改动时间；确认不了就 `go build -o tmp/sforum-api ./cmd/api` 后手动起。

## Next

**当前唯一未闭项 = 6 条 pend 的收口转换**（人工门，前置已到齐过半）：真实库 DDL 实测 `A-01` / `B-04`（`ddl/` 证据）、390 视觉 QA `G-05`（`390/` 证据、需在 `4e1876331` 之后重跑）、HTTP 值级 `F-01`（`factcandidates/`）、两份矩阵的变异复跑、M3.2 基线那 1 条。证据目录 `knowledge/reports/qa-2026-11-12-badge-pend-conversion/`，收口人：小审（三份矩阵 + 本文件 + `index.md` + 计划 + `modules/badges.md` 的锚点与数字）。

**M2（编辑器与公开面，已结项 2026-11-12）**：入口 `apps/web/app/utils/admin/badgeRule.ts`、`badgeRuleParams.ts`、`apps/web/app/components/admin/badges/rule/`（Editor/Group/Leaf/JsonPreview）、测试 `apps/web/tests/admin/adminBadgeRule*.test.ts`。硬约束：`SFBadgeCard` 抽取、公开面不泄露 AST、i18n 双向断言（`adminBadgesI18n.test.ts` 会同时抓 missing 与 extra）、桌面 + 390×844 浏览器 QA。

**M3（插件扩展，已结项 2026-11-12）**：先读已冻结的 `knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`，照它实现，重点三处：manifest `badgeConditions[]`（6 生产文件 + 1 生成文档）、`sforum.badges.fact.record@1` + `badges.fact.write` 能力、SDK `RecordBadgeFact`。入口文件：`app/Support/ExtensionManifest/v3_badge_conditions.go`（新建）、`app/Support/HostAPI/v2_command_badges.go`（新建）、`sdk/plugin/v2/badges.go`（新建）、`app/Models/Badges/conditions/facts.go`（新建，事实候选目录）。夹具 `extensions/fixtures/plugins/sforum-query-surface-reference`。

## Open Questions

1. **【已裁决 → `task-6` ②】`object.engagement{action:"bookmark"}` 与 `bookmark_count` 口径不一致**：前者 SQL 带 `visibility='public'`（`conditions/resolve_object.go:26-28`，注释称「与 `bookmark_count` 同口径」），但 `Recompute` 的 `bookmark_count` 数**全部**行（`activity_store.go:153`，无 visibility 过滤）。**裁决：resolver 错、计数对**（写路径按 `relationDelta` 计数、`Recompute` 数全部行，两处都不看可见性）。**已修复**：`resolve_object.go` 的 `bookmark` 分支去掉 `visibility` 谓词，`TestObjectEngagementBookmarkIgnoresVisibility` 钉住。
2. **【已裁决 → `task-6` ③】`factKey` 失效目前仍是 0，不是 error**：`resolve_facts.go` 的 `loadFacts` 没有「该 factKey 是否被已启用扩展声明」的判据，查不到行一律按 0 返回（注释说该返 error，实现缺声明来源）。**裁决：现在就修，不等 M3**。**已修复**：`NewResolver(pool, userID, factKeys ...string)` + `rule_eval.go` 透传；未声明即 error → unknown，`TestFactCountUndeclaredKeyIsUnknown` 钉住。M1 传空集，M3 只需填集合。
3. **M1 无法指定 variant**：`fact.count` 的 `variant` 是 `enum` 且 `Options: nil`，M1 没有任何候选 → 规则里带 `variant` 会被校验拒绝。M3 落地后才会出现合法 variant，届时 M2 的「空候选不得渲染成空下拉并禁止保存」才开始起作用。

## 第一刀 + M3 收口（2026-11-12 晚，FAIL 0）

**Status**：M1 / M2 / **第一刀（回扫进度可见）** / **M3（插件扩展）** 全部落地并独立验收；全仓 `./scripts/test.sh --fast` 三线 PASS。

| 交付 | 验收 |
| --- | --- |
| 回扫进度可见（§13 残留 #15 关闭） | 逐条表 `40 ok / 0 FAIL / 0 pend`；脚本最终口径（集群 + 会话 + `--mutate`）`ok 41 / FAIL 0 / pend 1`（唯一 pend 为 G-05，由 390 实测补实）。报告 `reports/2026-11-12-badge-backfill-progress-acceptance.md`；390 证据 `reports/qa-2026-11-12-badge-pend-conversion/390/` |
| M3.1 声明层 + 候选目录 | 最终口径 `ok 48 / FAIL 0 / pend 0`（带会话 + `--mutate`，`F-01 facts=2`），`reports/2026-11-12-badge-m3-manifest-acceptance.md` |
| M3.2 Host 命令 `badges.fact.record` | `ok 34 / FAIL 0 / pend 0`（14 条变异 + 覆盖自检），`reports/2026-11-12-badge-fact-record-acceptance.md` |
| M3.3 SDK + M3.4 夹具/门禁 + M3.5 文档 | `reference-packages-gate.sh` exit 0（`checked: 15`，两夹具都在内）；`reports/2026-11-12-badge-m34-fixtures-and-trust-qa.md`；`docs/extensions/badge-plugin-facts.md` 双语 |

**这一轮新长出来的坑（都已在 `modules/badges.md` 留痕）**：

1. **进度不许从授予数反推** —— 规则不满足时「反推的进度」会倒退，而真实回扫在前进；反向证据：徽章**有授予行但没有 run 行**时 `items` 必须为空。
2. **`failed` 不是「结束」** —— 失败不写 `finished_at`（river 还会重试），重试成功要把 `failed_cursor_user_id` 清 0 并翻回 `running`。
3. **`Options == nil` 与 `Options == []string{}` 语义相反**：前者「候选运行时填（开放集）」，后者「没有候选」。混用会让带 `variant` 的规则 100% 保存失败（`slices.Contains(nil, v)` 恒 false）。
4. **schema 与 Go 是两条校验路径，覆盖面不同**：`badgeConditions` 的 key 前缀与 `contractVersion == key@N` 只有 Go 判；`maxItems`/`minItems`/双语 required 只有 schema 判。「改一侧即绿」的假象靠双侧断言 + 删一侧必须红的变异才能破。
5. **`fact.write` 的 `userId` 不能从上下文推断** —— 事实多在异步 observe handler 里产生，那时没有终端用户请求；用十进制字符串而非 double（BIGINT 精度）。
6. **能力拒绝不落审计、域层拒绝落审计**：共享闸门在事务前返回协议错误（全仓一致），域层拒绝才写 `extension.badge_fact_record.denied`（best effort，审计失败不改返回码）。
7. **空 `dedupeKey` 不是幂等键**：部分唯一索引 `WHERE dedupe_key <> ''`，两次空 key 必须都插入（要同时验行数与 `total`）。
8. **`missing_fact_key` 有实现但零测试**（本轮补齐）：健康清单 + 「规则 `rule::text` 逐字节相等 + `updated_at` 不变」+ 重装后 issue 消失且不跑任何 job。
9. **`ErrBadgeFactWriteDenied` 曾是死映射**（有 `mapBadgeError` → 403，无生产者）：改成命令用 sentinel 构造拒绝、reason 从错误链投影，并补 403 断言。

**环境**：本轮机器 20:23 重启过，之后 3000（web dev）与 8080（API）都是老运新起的（API 带新端点）；`air` 依旧不可用。

**未清零（刻意保留）**：`extensions`1（disabled 墓碑）/ `extension_versions`1 / `plugin_runtime_publication_members`1（追加写表，`BEFORE DELETE OR UPDATE` 触发器 + FK RESTRICT）。这是「卸载保留不可变身份」的既有设计，**不许**用 `DISABLE TRIGGER` 清。范围外观察：`DELETE /admin/extensions/:id` 返回 `uninstalled:true` 而库里仍 `disabled`，语义易误读，留给扩展平台那一刀。
