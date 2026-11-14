# 信任等级：条件化门槛（复用徽章规则引擎）+ 插件扩展面

Status: **实施中**（2026-11-14）。M0 已收口（`e4f8c3b1f` + `322f088e5`）；M1 进行中。
前置计划：`2026-11-10-trust-levels.md`（D1–D32）、`2026-11-11-trust-levels-n-tiers.md`、
`2026-11-12-badge-rule-engine.md`（规则引擎本体，已收口）。

---

## 0. 本计划的立足点（全部于 2026-11-14 在 HEAD 上实测）

| 事实 | 值 | 核验命令 |
| --- | --- | --- |
| 信任等级当前门槛形状 | 每档 6 个 int64，AND（`Requirements`） | `rg -n "type Requirements" -A 8 apps/api/app/Models/TrustLevels/types.go` |
| 判定 | 纯函数 `LevelOf`，两段式（账龄闸 + 从高往低找第一个满足） | `apps/api/app/Models/TrustLevels/level.go` |
| 求值时机 | `LoadActor` 装载期，固定 +1 条 `user_activity` 读 | `Identity/postgres_store_roles.go:228` |
| `LoadActor` 调用点 | **55** | `rg -o "LoadActor\(" apps/api -g '*.go' -g '!*_test.go' \| wc -l` |
| 徽章规则引擎 | 纯 AST，深度 ≤5 / 叶子 ≤32 / 节点 ≤64，Kleene 三值 | `apps/api/app/Support/RuleEngine/` |
| 徽章条件目录 | **13** 条 | `rg -o 'Key[A-Za-z]+ += +"' apps/api/app/Models/Badges/conditions/registry.go \| wc -l` |
| 条件包被引用文件数 | **41** | `rg -l "Models/Badges/conditions" apps/api --glob '*.go' \| wc -l` |
| 路由 tripwire 现值 | **433** | `apps/api/app/Support/Routes/registry_test.go:536` |
| 配置错误码 | **33**（前端分桶断言 33 / 15 / 18） | `config.go` + `apps/web/tests/admin/forum/trustLevels.test.ts:595-600` |
| 多条件规则文案 | **已生成**（`renderRule` 用「并且/或」连接，手填已移除） | `requirement_text_render.go:75`、`rule_store_postgres_integration_test.go:457` |
| 草稿规则文案预览端点 | 已有 `POST /admin/badges/rule-text` | `Http/Controllers/Badges/controller.go:123` |
| 后台 i18n 键族（**实测，纠正模块笔记**） | `admin.forum.trustLevels.*` = **243** 叶键；`admin.settings.trustLevels.*` = **0**，且被断言必须 `undefined` | `node -e` 结构化计数 + `trustLevels.test.ts:833-839` |
| 等级域插件暴露 | **零**（模块笔记明确 `TODO(核实)`） | `knowledge/modules/trust-levels.md` §Extension Surface |
| 可挣得权限白名单 | **硬编码 7 个核心键**，插件权限永不可能 | `Models/TrustLevels/registry.go:33` |
| 阅读行为数据源 | **不存在**（无 `user_topic_reads`） | 迁移全表扫描 |

### 0.1 研究刀实测纠错（2026-11-14，`e4f8c3b1f` 上核；**与上文冲突处以本节为准**）

来源：`knowledge/reports/2026-11-14-trust-level-conditions-impl-facts.md` §2。九条会改变下游写法的：

| # | 计划原写法（错/旧） | 实测 |
| --- | --- | --- |
| E1 | 「求值在 `LoadActor`，55 个调用点」 | 求值在 **`GetCurrentUser`**（`postgres_store.go:135` → `loadCurrentUserAccess` → `applyTrustLevel`）；`LoadActor:394` 只复用。`LoadActor` 55 个调用点 ≠ 55 个求值点；`GetCurrentUser` 非测试调用点 **11** 个 |
| E2 | §5「现状 1 条 SQL」 | 信任链自身 **+2**：`applyTrustLevel` **无条件**先读 `SELECT created_at, kind FROM users`（`postgres_store_roles.go:240-243`，`trustLevels==nil` 也读），再 +1 `user_activity`。整条已认证路径固定 **5** 条（主查询 + 2 RBAC + users + user_activity），另 0~1 overrides |
| E3 | §3.4「改测试即可」 | `SPECIFIC_ERROR_CODES`(15) / `CONFIG_BUCKET_ERROR_CODES`(18) 定义在 **`useTrustLevelSaveErrors.ts:138/157`**；测试只是 import 并在 `:595-597` 断言长度、`:549` 标题串写死「33/15/18」。**前端共 4 处** |
| E4 | 「`TrustLevelDefinition` + `rule`」 | 公开面与后台面**共用同一个 `levelDefinition`**（`dto.go:23` 与 `:129`；`:16` 是 `publicPayload.Levels`）→ 必须**拆两个 wire 形状**，否则公开面自动带 `rule`，直接违反验收 5。更糟的是后台字段今天**没有任何类型边界**：`buildAdminPayload` 返回的就是 `publicPayload`（`payload.go:18`），靠 `buildPayload(policy, includeLimits bool)` 的运行期 bool 置 nil 来区分（`payload.go:26-55`） |
| E5 | §4.3「注入 factKeys（`badgesFactCatalog`）」 | 装配顺序 infra→extension→domains：`newTrustLevelSource` 在 infra（`api_assembly_infrastructure.go:242`），`badgesFactCatalog` 在 domains（`api_assembly_domains.go:640`）→ 必须**延迟绑定**（先例 `deferredBackfillEnqueuer.Bind`，`badges_wiring.go:145`） |
| E6 | §5「≤3 次 Prepare，memo 跨档复用」 | `Resolver.Prepare` **不查 memo**（`Conditions/batch.go:78-104`），每档 Prepare 一次 = 同 type 查 N 次。必须把**所有带规则档位合成一棵 `all` 根、只 Prepare 一次**，再逐档 `Eval` 复用 memo |
| E7 | §4.2「复用 `conditions` 的候选查询」 | `Conditions/refcandidates.go` / `refnames.go` **零 SQL**（只有形状与回落访问器）；真查询是 `(*badges.PostgresStore).RefCandidates`（`ref_candidates.go:31`）与 `LoadRefNames`（`ref_names_postgres.go:45`）→ 依赖方向见 D17 |
| E8 | §3.5「整档文案直接复用」 | `renderRule` 有 **160 rune 硬上限**（`requirement_text_render.go:28/87`），超限降级成「满足 N 个条件，其中最关键的是…」。另 `identity.profile` 不止写 Phrase：`phraseSamples()`（`requirement_text_test.go:55`）要 golden 样本，`:166` 与 `conditions_postgres_integration_test.go:264`/`:291` 三处计数同批 |
| E9 | §3.4「5 个新码」 | 引擎已有 17 个细分码（`validate.go:22-41`：`too_deep`/`too_many_nodes`/`too_many_leaves`/`unknown_condition`/`shape_invalid`/`compare_*`/`param_*`…），5 个新码是**聚合**。`rule_reference_invalid` 与 `rule_condition_not_available` 引擎侧没有 → 需**显式映射表 + 表驱动测试**，否则「精确 422」退化成桶码 |

小数修正：路由 tripwire 在 `registry_test.go:534`（不是 536）；`ui` 计数 434；可挣得白名单 `registry.go:29-37`；`Metrics`/`Value()` 在 `types.go:133/143`；列名是 **`website_url`**（条件枚举值仍叫 `website`，判据是非空串）。

### 0.2 研究刀四条架构裁决（D17–D20）

| # | 问题 | 裁决 | 理由 |
| --- | --- | --- | --- |
| D17 | `Models/TrustLevels` 能否 import `Models/Badges` 做引用校验/真名解析 | **不许**。在 TrustLevels 声明窄接口，bootstrap 注入（与 `ActivityReader`、`Forum.TrustPolicyResolver` 同一套路） | 保住 D18「Identity/TrustLevels 不与 Badges 硬耦合」；fakes 可测；真实查询留在 badges store，不写第二份 SQL |
| D18 | `TrustLevelSource` 是否加第二方法暴露 `incomplete` | **不加**。接口保持 `Grants(level, earned, err)` | 需要 `incomplete` 的是 M4 事件 job 与进度读面，它们走 **TrustLevels 自己的** `Evaluate()`（返回 `{Level, TierID, Incomplete, Errors}`），不经过 Identity。加接口方法会波及两个 loader 与全部 fake，收益为零 |
| D19 | `conditions.NewResolver` 是否加参数 / 加 `PrepareAll` | **都不加**。在 TrustLevels 侧构造合成 `all` 根（children = 所有带规则档位的 rule），`Prepare` 一次，再逐档 `Eval` | 消除 E6 的 N 次重查；不动已验收的 Conditions 公共面，零回归风险 |
| D19-b | 合成根会不会撞 `MaxLeaves=32`/`MaxNodes=64`？ | **不会，且不许给它加合计上限**。合成根**只许 `Prepare`、禁止 `Validate`** | 实测 `Prepare`（`Conditions/batch.go:78-104`）只按 type 收集叶子各查一次，**没有任何上限检查**；`Validate` 才有。所以各档叶子叠加不构成越界，也不需要「合计判定」——那会凭空造出第 7 个错误码和一条运营永远碰不到的失败路径。**每档仍各自 ≤32 叶子/≤64 节点**（保存期按单档校验，这一点不变）。可断言的判据：8 档各 32 叶 → `Prepare` 成功且**查询数 = 出现的 type 数**（与档数无关） |
| D19-c | 合成根装哪些档位？ | **只装「`satisfies()` 数值预筛已通过 且 带 rule」的档位**，逐个用户动态构造；helper 命名 `prepareOnlyRoot`（未导出），且**合成根不进任何返回值/字段/参数**，只在 `Evaluate()` 内部构造并立刻喂给 `Prepare` | 若图省事按「所有带规则档位」建根，数值根本不过的用户也会触发全部条件查询，§3.2「数值不过不查规则」与 §5 的成本表**同时失效**，且验收矩阵第 1 条变异（数值预筛失效 → 查询计数红）失去判据。**「外部拿不到合成根」由编译器保证、不由测试保证**：未导出 + 不出包 + 不留字段，想顺手 `Validate` 也无从取得它；将来真要暴露会强制过一次 review。可断言判据：某档数值不过时，该档的 rule 叶子**不产生**任何 SQL（用它独有条件类型做探针，例如只在该档出现的 `object.authoring`） |
| D20 | `badgesFactCatalog` 复用形态 | **延迟绑定闭包**（`Bind`，先例 `deferredBackfillEnqueuer`） | 保留现有装配顺序；未绑定时 factKeys 为空 → `fact.count` 恒 unknown，与「未声明即无法判定」的既有语义**天然一致**（fail-closed，不会误判成立） |

---

## 1. 目标与验收标准

**目标**：把每档「6 个数值字段 AND」升级为「数值快筛 + 可选布尔规则树」，条件目录与徽章**同 key、同 SQL 口径**；同时补齐等级域的插件扩展面。

**验收标准**（缺一条不算完成）：

1. 未配置 `rule` 的档位判定与今天**逐人一致**：现有 `level_test.go` 全绿 + 新增一条等价性回归（同一批 Activity/Policy 快照，`LevelOf` 与 `LevelOfWithRules(nil)` 结果逐个相等）。
2. 配置了 `rule` 的档位：`数值 AND 规则` 同时满足才成立；规则求值失败 → 该档**不成立**（fail-closed），不抛错、不静默放行。
3. 装载期查询增量：无规则站点 **0 条新增**；有规则站点典型 **+0~2 条**；P95 装载增量 **≤ 5ms**（10k 夹具实测）。
4. 后台可编辑规则树、保存前看到「≈ N 人」估算、两步确认后落库；非法规则返回**精确** 422 码，不是笼统 `invalid_config`。
5. 公开面只下发「扁平 AND」的逐条句子，`any`/`not`/嵌套只给 satisfied/total；**任何情况下不下发 AST**（值级断言：公开响应里不出现 `children`/`cmp`/`params`/`type`）。
6. 插件可以：声明事实条件并被等级规则引用；声明权限可被等级授予（进信任披露）；订阅 `trust_level.changed`；通过 HostQuery 读等级目录与单用户等级。
7. 门禁全绿：`./scripts/test.sh --fast`、`node tests/validate-architecture-boundaries.mjs`、`ruby scripts/validate-openapi-refs.rb`、`node scripts/v3-catalog/generate.mjs --check`。

---

## 2. 已裁决口径（16 条，实施期不再讨论）

| # | 决定 | 含义 |
| --- | --- | --- |
| D1 | **数值快筛 + 可选规则树** | `requirements` 保留为快路径与进度来源；新增可选 `rule` 作为额外 AND 闸门。要纯布尔表达就把数值清零。 |
| D2 | **热路径实时求值** | 不反规范化授权值、不用 Redis 缓存等级；仍在装载期算。 |
| D3 | **立即降级** | 条件不再满足，下次装载即按新结果算，已挣得权限随等级收回。 |
| D4 | 条件族 v1 | 活动计数（含窗口）、streak、定向对象互动、身份与资料、已拥有徽章、插件事实。**不含**审核信号、内容质量、阅读行为。 |
| D5 | 目录复用 | 同 key、同 SQL 口径，信任等级只做子集裁剪。 |
| D6 | 进度展示 | 数值给数字（现有 `progress` 不动），规则条件给清单。 |
| D7 | 新人窗口 | 保留两段式：账龄闸封顶 TL1，规则不能突破。 |
| D8 | 影响面 | 保存前估算 + 两步确认。 |
| D9 | 事实层 | 复用 `badge_facts` / `user_fact_counters` / `fact.count`；manifest 加通用别名 `userFactConditions`，`badgeConditions` 保留兼容。 |
| D10 | 插件权限 | 允许被等级授予，但必须 manifest 显式声明 + 进信任披露弹窗。 |
| D11 | 编辑器 | 抽共享组件，徽章侧只改 import。 |
| D12 | 事件 | 新增 `trust_level.changed`。 |
| D13 | 只读面 | 等级目录 + 单用户当前等级（现算）。 |
| D14 | 重扩展面 | 本刀不做 RPC 条件求值器 / 晋升闸门 / 接管等级来源。 |
| D15 | 进度契约 | 服务端下发双语句子（复用 `conditions.Definition.Phrase`）。 |
| D16 | 后台形态 | 阈值 tab 内每档折叠面板，不新增 tab。 |

---

## 3. 数据模型与判定语义

### 3.1 配置形状（`trust.levels` 单 JSON，`schemaVersion` **保持 2**）

```json
{"schemaVersion":2,"tiers":[{
  "id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","color":"teal",
  "requirements":{"days_visited":3,"comment_count":5},
  "rule":{"v":1,"root":{"op":"all","children":[
    {"type":"identity.oauth_link","params":{"providerId":"sforum.auth-github"}},
    {"type":"fact.count","params":{"factKey":"acme.onboarding.tasks"},
     "cmp":{"op":"gte","value":3}}]}},
  "limits":null,"earnable":[],"retired":false
}]}
```

- `rule` **可选 + `omitempty`**：不配规则的档位写出字节与今天完全一致。
- **不升 schemaVersion**：与 `color` / `deletedTierIds` 同款回滚口径——没用过新功能的配置旧二进制可读。
- **回滚代价（必须写进模块笔记与回滚说明）**：一旦某档写过 `rule`，旧二进制的 `DisallowUnknownFields` 会**整份拒绝**这份配置 → 等级判定退回「未解析」。回滚必须**先清空全部 `rule` 字段**再换二进制。
- `rule` 解码走 `ruleengine.Decode`（未知字段报错），`Config` 外层仍 `DisallowUnknownFields`。

### 3.2 判定语义

```
tierSatisfied(t) = satisfies(activity, t.requirements)
                 && (t.rule == nil || ruleMatched(t.rule))
```

顺序（关键：**数值不过不查规则**）：

1. 账龄闸：`accountAge < NewUserDays` → 只判 TL1（TL1 的 `rule` 同样生效），否则 TL0。
2. 从 `LastRank()` 往下：数值不过 → 跳过该档，**不构造求值**；数值过 + 无规则 → 直接返回；数值过 + 有规则 → 求值，成立返回，不成立继续降档。
3. 遍历到 TL1 仍不成立 → TL1（与今天一致：过了新人窗口至少 TL1）。

**只有一份遍历实现**：`LevelOf` 改为 `LevelOfWithRules(ctx, activity, accountAge, policy, eval) Level` 的薄包装（传 nil evaluator）。禁止复制第二份比较逻辑——这是徽章那刀「两份 deny CTE」的同形风险。

### 3.3 条件目录

共享目录 = 徽章现有 **13 条 + 新增 1 条** = 14；信任等级取子集 **12 条**，排除 `content.pinned` / `content.hidden`。

新增（补齐「身份与资料」里的简介/签名，徽章也同时获得）：

| key | params | 取值 | 真值来源 |
| --- | --- | --- | --- |
| `identity.profile` | `field`: enum(`bio`/`signature`/`website`/`location`) | 布尔 | `user_profiles` 对应列非空 |

同步事务（漏一处即红）：
- `TestEveryConditionHasPhrase`（每条件必须有句子）；
- 条件目录文档与 `GET /admin/badges/conditions` 条数断言 13 → 14；
- 前端 `badgeRule.ts` 的 `enum` 候选无需改（参数由服务端下发）。

### 3.4 错误码（33 → 39）

新增 6 条，**全部是 tier 级**（带 `rank` + `tierId`），所以全部进「specific」桶：

| code | 触发 |
| --- | --- |
| `rule_too_complex` | 深度/叶子/节点/days/value 越界 |
| `rule_unknown_condition` | `type` 不在共享目录 |
| `rule_shape_invalid` | 空 `all`/`any`、`not` 子节点数 ≠1、叶子带 `children`、条件不带必需的 `cmp` |
| `rule_reference_invalid` | 引用的 topic/category/tag/role/provider/badge/factKey 不存在（**仅写路径**） |
| `rule_condition_not_available` | 条件在共享目录但不在信任等级子集（如 `content.hidden`） |
| `earnable_not_declared` | 该档 `earnable` 里含一个没有任何已启用扩展声明的权限键（M4.2 的写期拒绝；解码期**不**拒） |

三处必须同批（顺序即纪律）：
1. `config.go` 的常量 + `configErrorReasonCodes` + `TestConfigErrorReasonCodesAreStable` 字面量；
2. `contracts/openapi/schemas/trust_levels.yaml` 的 enum；
3. `apps/web/tests/admin/forum/trustLevels.test.ts:595-600` 的 `BACKEND_CODES` **39** / `SPECIFIC_ERROR_CODES` **21** / `CONFIG_BUCKET_ERROR_CODES` 18（并集 39，不重不漏）+ 两份 locale 的 6 条文案（`admin.forum.trustLevels.*` 族）。

**注意两条口径（2026-11-14 实测纠正）**：
- 后台键族是 **`admin.forum.trustLevels.*`**，不是 `admin.settings.trustLevels.*`——后者被 `trustLevels.test.ts:833-839` 断言必须 `undefined`，写成那样会全量 miss 且不会被双向断言兜住。`knowledge/modules/trust-levels.md` 的旧说法是错的，M5 必须改。
- 「多出来的键会被双向断言判红」只在 `adminBadgesI18n.test.ts` 成立，**不覆盖** trustLevels。trustLevels 侧真正会红的是 4 处：`606-613`（specific 码两份 locale 都要有）、`615-643`（桶内码 locale **不得**有文案）、`803-804`（metric 键集合 toEqual 六项）、`833-839`（禁第二族）。

### 3.5 契约变更（OpenAPI）

| 位置 | 变更 |
| --- | --- |
| `TrustLevelDefinition` | + `rule`（object / nullable，**仅后台读面下发**） |
| `TrustLevelViewer` | + `conditions`（`[{satisfied, text:{zh,en}}]`，nullable；仅本人、仅下一档、仅扁平 AND） |
| 新增 `TrustLevelConditionCatalogResponse` | 12 条条件定义 + `factCandidates` + `refCandidates` |
| 新增 `TrustLevelRuleImpact*` | 请求 `{tiers:[{tierId, requirements, rule}]}` → `{tiers:[{tierId, estimated\|null, method, partial, unsupportedConditions}]}` |

路由 **433 → 435**（`GET /admin/trust-levels/conditions`、`POST /admin/trust-levels/rule-impact`）。三者同一批：`registry_test.go` 计数 + `catalog-identities.json` 的 `routes` + 生成物。

**复用而非新造**：整档文案直接调 `BuildRuleRequirementText(rule, names)`（与徽章同一实现，多条件已支持「并且/或」连接）；逐条清单直接调 `conditions.Definition.Phrase(params, cmp, names)`。**不新写渲染器，不加 `rule-text` 端点**（编辑器侧本来就用 `ruleSentence` 本地预览）。

---

## 4. 后端改动

### 4.1 M0：条件包去徽章化（机械改动，独立可验）

- `apps/api/app/Models/Badges/conditions/` → `apps/api/app/Models/Conditions/`，**package 名仍 `conditions`**，`git mv` 保留历史。
- **41 个文件**的 import 路径机械改写。
- 不搬 `rule_impact.go` / `rule_health.go`：它们是 `badges.PostgresStore` 的方法，issue kind 含 `missing_badge_slug`，属徽章专有。
- 顺带导出 `renderRule` 的入口已够（`BuildRuleRequirementText` 已 public）；**不导出** `renderLeaf`，逐条清单走 `Definition.Phrase`。
- 验收：`go build ./...` exit 0 + 徽章全量测试 exit 0（行为零变化）。**单独一个 commit**，不与逻辑改动混。

### 4.2 M1：配置、校验、目录

| 文件 | 改动 |
| --- | --- |
| `Models/TrustLevels/config.go` | `TierConfig` + `Rule json.RawMessage`；解码后 `ruleengine.Decode` + `Validate`（信任子集 catalog）；5 个新 reasonCode |
| `Models/TrustLevels/rule_catalog.go`（新） | 信任子集：`conditions.Core()` 滤掉 `content.pinned`/`content.hidden`；`Catalog()` + `Allowed(key)` |
| `Models/TrustLevels/rule_reference.go`（新） | 写路径引用存在性检查，复用 `conditions` 的候选查询；返回 `[]RuleIssue{kind,path,ref,label}`（形状与徽章一致，前端可复用错误渲染） |
| `Http/Controllers/TrustLevels/controller.go` | 写路径加引用检查（422 `rule_reference_invalid`） |
| `Http/Controllers/TrustLevels/rule_controller.go`（新） | conditions / rule-impact 两个端点 |
| `Http/Controllers/TrustLevels/dto.go`、`payload.go` | 后台读面下发 `rule`；公开面不下发 |
| OpenAPI 4 份文件 | 见 §3.5 |

**校验分层（硬约束）**：`DecodeConfig` 无 ctx、无 DB，**只做结构 + 白名单 + 上限**（读路径会调它：`Providers/forum.go`、`Providers/profile.go` 等）；引用存在性**只在写路径**。所以主题被删后旧规则仍能解码，运行时该叶子返回确定值 0/false（与徽章一致）。

### 4.3 M2：求值链路

| 文件 | 改动 |
| --- | --- |
| `Models/TrustLevels/level.go` | `LevelOfWithRules(...)`；`LevelOf` 变薄包装（单一遍历实现） |
| `Models/TrustLevels/rule_eval.go`（新） | `RuleEvaluator` 接口 + 适配器：`conditions.NewResolver(pool, userID, factKeys...)` → `Prepare` → `ruleengine.Eval`；**同一 per-user evaluator 在一次装载内跨档复用**（memo 命中，同条件不重复查） |
| `Models/TrustLevels/resolver.go` | `Grants` 先读 policy；**只有存在带规则档位时才构造 evaluator**；规则失败 → 该档不成立 + `incomplete`，**不**把整次解析降级为「未解析」 |
| `bootstrap/trust_levels_wiring.go` | 注入 evaluator 工厂（pool + factKeys 来源 = 既有 `badgesFactCatalog` 的 5s 快照，不建第二份） |
| `Http/Controllers/TrustLevels/controller.go` | `viewerPayload` 追加 `conditions[]`：仅下一档、仅扁平 `all`、逐叶 `Resolve` + `Phrase` |

**失败姿态（逐条要有测试）**：
- `user_activity` 读失败 → 整次解析未解析（与今天一致：不减扣、不发事件）。
- 规则求值失败（查询错 / 未声明 factKey / 脏规则）→ 该档不成立（fail-closed）+ `incomplete=true`；**`incomplete` 时不发 `trust_level.changed`**，避免 DB 抖动造成全站降级事件洪水。

### 4.4 M3：后台 UI

> **规范更正（2026-11-14，M3b 实施期实测；与 ui-spec / copy-spec 冲突处以此为准）**
>
> | # | 规范原写法 | 实测后 |
> | --- | --- | --- |
> | S1 | ui-spec Q-02「恰好 1 个纵向滚动容器」 | **全站不成立**：`/control-panel/users`（未改动页）同样有 2 个（侧栏导航列 + `.sforum-admin-scroll-area`），390 下侧栏收起才恰好 1 个。判据改为「**面板不给 shell 基线新增 scroller**」（shell 基线可能是 1 或 2，取决于断点与侧栏状态）。 |
> | S2 | ui-spec Q-03「面板子树内 `overflow≠visible` 元素数为 0」 | 会误判：面板子树里唯一 `overflow:auto` 是共享编辑器 JSON 预览的 `<pre>`，它在默认收起的 `<details>` 里（M3a 原样搬来）。改它＝改徽章编辑器行为。判据改为「排除收起 `<details>` 子树后为 0」。 |
> | S3 | copy-spec §3.2 的 `panel.title` / `empty.title` / `impact.row` | **不落地**：ui-spec §1.9 明令面板不渲染自己的标题（避免一档两个视觉起点），`impact.row` 无消费者。三条落地即死键，而死键判据（§4.3）没有自动护栏 → 直接从文案表删除。 |
> | S4 | 「数字全屏只出现一次」 | 语义澄清：**每个档位的数字只出现一次**，不是「整个弹窗只有一个数字」。多档带规则时各档各有自己的数字是正确行为（弹窗只列**带规则**的档，纯数值档已滤掉，否则会出现 4 个数字）。 |

### 4.4-b M3b 未覆盖项（**M5 收口必补，不许沉掉**）

三项浏览器 QA 因「要写 dev 库」而未做，实施方已诚实标注。补法已给，M5 收口单必须原样带上：

| # | 项 | 现在为什么测不了 | 补法 |
| --- | --- | --- | --- |
| U1 | Q-10 深色派生（自定义 hex） | dev 库 4 档都是 token 色，无自定义 hex；改它要写库 | 夹具档设 `#7c3aed` → `html.classList.add('dark')` → 断言 `panel.borderLeftColor` 浅/深两值不等 |
| U2 | Q-12 深度 5 嵌套不溢出（390 期望最内层 `clientWidth ≈ 170`） | 需要往 `trust.levels` 写一棵 5 层 `any/not` 树 | 用 `rule-impact` 同一套草稿体构造，或在 QA 环境写一档再回滚 |
| U3 | Q-14/Q-16 的 `null` 与 `0` 现场 | 真实数据造不出 `estimated: null`（要不可估条件如 `object.engagement` 指向某主题）；`0` 要求全站无人满足 | QA 环境加一档只含 `object.engagement` 的规则 → 断言无 `≈`；另一档阈值调到无人够 → 断言 `≈ 0 人` 而非「无法估算」 |

> 两态目前由 SFC 夹具钉死（`trustLevelRuleImpact.test.ts` 的 CP-1/CP-2），但**夹具不等于现场**：`0` 与 `null` 走的是同一条三元分支，只有真数据能证明归一化没把 0 变成 null。

### 4.4-c 存量死键 9 条（**M5 收口必判定，不许无主悬着**）

`admin.forum.trustLevels` 族 360 键中 351 有消费者，9 条无。**棘轮测试已落**（`tests/admin/forum/trustLevelI18nDeadKeys.test.ts`，断言 `unused === KNOWN_DEAD_KEYS` 集合相等）：新增会红，**接上消费者却忘销号也会红**——登记表只许缩短。

| 键 | 实测依据 | M5 处置方向 |
| --- | --- | --- |
| `errors.tierTitle` | 全仓 `app/` 除 locale 外零引用（连注释都没有）；copy-spec 说「复用」，可能有人打算接 | 判定「接消费者 or 删文案」 |
| `icons.label` / `tiers.icon` | 同上，零引用 | 同上 |
| `earnable.aria.cumulative` | 零引用；**注意有易混邻居 `earnable.aria.cell` 在用**，处置时别连带伤到 | 同上 |
| `level.{newcomer,member,resident,veteran,leader}` | 档位名已确认走服务端 payload（`builtinTierI18nKey()` → `profile.trustLevel.name.<slug>`，`useTrustLevel.ts:227-229`），`trustLevelName()` 只作回落 ⇒ 与档位名读面无耦合 | 仍需判定：删了是否影响其它读面，不该由重构拍板 |

- **抽共享编辑器**：`components/admin/badges/rule/*` → `components/admin/rules/*`（Editor / Group / Leaf / ParamField / JsonPreview / SimpleForm），props 已是宿主注入（`catalog`/`refCandidates`/`factCandidates`），**props 形状不变**。`utils/admin/badgeRule.ts` → `utils/admin/rule.ts`，`badgeRule.ts` 保留为 re-export 兼容层。徽章侧只改 import，**行为零变化**，用现有 `bun test` 回归。
- `SFAdminTrustLevelThresholdsTab.vue`：每档行加「更多条件（N）」折叠面板；不新增 tab、不新增卡片、不改页头几何（遵循 `pages/admin/settings/index.vue` 的视觉基线）。
- 新组件需登记 `catalog-identities.json` 的 `ui` + 生成物：`SFAdminTrustLevelRuleSection.vue`、`SFAdminTrustLevelRuleImpactDialog.vue`。
- 新 composable：`useAdminTrustLevelConditions.ts`、`useTrustLevelRuleDraft.ts`（前端镜像校验，与后端同规则）。
- 保存流程：编辑 → 保存 → `POST /admin/trust-levels/rule-impact`（**草稿请求体**）→ 确认 → `PUT`。
  - **新档也估算**：估算端点吃草稿、不依赖已保存 id，所以「新建档位不弹估算」这条**作废**（那是徽章的 GET-by-id 形态，等级域不适用）。
  - **数字全屏只出现一次**：`≈ N 人` 只在数字位渲染，说明句**不得**再含 `{n}`，否则运营会以为有两个口径。
  - `estimated: null` → 「无法估算」+ 逐个列不可估算条件，**全屏不出现 ≈**，且 `partial` 行不渲染（否则「无法估算」与「只算了部分」互相否定）；`partial: true` → 追加「只算了部分条件，这个数字是上界」。
  - **三态互斥用判据不用措辞**：`estimated === null || undefined` 才是「无法估算」，禁止 `estimated ? … : …`（0 在 JS 是 falsy，这正是「0 被写成无法估算」的唯一成因）。
  - 语义是「**满足该档条件的估算上界**」，不是「会有 N 人升级」：文案不许承诺升降人数。
  - **保存闸门（D21，2026-11-14 由小设顶回后细化）**：`保存不可用 ⇔ canManage=false ∨ saving ∨ (anyTierRuleDirty ∧ ¬catalogUsable)`。
    - `anyTierRuleDirty` = 本次会话**编辑过**至少一档的规则草稿。未完稿的档位**原样回传**读面拿到的 `rule` 原始 JSON（前端不解码、不重建），由后端 PUT 做权威校验——所以「目录挂了就连阈值都不能改」是多余的自我封锁，不采纳。
    - `¬catalogUsable` 四条任一：① 请求失败/非 2xx；② `conditions.items` 为空；③ 载荷含 `fact.count` 而 `factCandidates` 为空；④ 载荷含 `refKind=k` 的 objectRef 而 `refCandidates[k]` 缺失。后两条是「无法校验」不是「本来就没有」。
    - 禁用必须给**可见**原因（`disabled` 按钮不可聚焦，只挂 `title` 读不到），固定在面板内首行。
  - 停用档**允许**配规则（与现状一致：保留配置、判定跳过，支持「先配好再启用」）。
- 视觉 QA：桌面 + 390×844，覆盖展开/折叠、嵌套规则、保存 422 行内错误、影响弹窗。

### 4.5 M4：插件扩展面

**M4.1 manifest**
- `userFactConditions[]`（新 canonical）+ `badgeConditions[]`（兼容）；合并校验：合计 ≤32、key 全局唯一、`contractVersion == key@N`、都要求 `backend`。
- `manifest-v3.schema.json` 的 `properties` / `$defs` / `if-then` 同批改。**没有 reflect 对齐测试：只改一侧会静默**（只改 struct → schema 拒包；只改 schema → 声明被静默丢弃）。
- `permissionDefinitions[].trustLevelEarnable: bool`（新）。校验：该 key 必须在本 manifest 的 `permissionDefinitions` 内；不得与核心权限键冲突。
- 信任披露：`SFAdminExtensionEnableDialog.vue` 增加「可被信任等级授予的权限」段（该弹窗只渲染 label，不渲染 description——文案必须自解释）。

**M4.2 可挣得权限白名单（修订 D24，需新决策记录）**
- 运行时白名单 = 核心 7 键 ∪ 已启用插件声明键；`Earnable.Grants` 按此过滤。
- 解码期**不再硬拒**未声明键（否则一次插件停用 → 整份配置解码失败 → 全站掉回默认策略，灾难性失败）；改为：解码保留 + 运行时惰性 + **写路径 422 `earnable_not_declared`** + 后台读面标「失效」。
- **D22（DTO 状态名，2026-11-14 裁决）**：后台读面每个 earnable 键下发 `state ∈ {valid, inactive, unknown}`。
  用 **`inactive`** 而不是 `orphan`——既有 i18n 已有 `earnable.orphan.*`（含义是「键不在白名单、保存时会被移除」，实际等价 `unknown`），两个域共用 `orphan` 一词会让 `unknown` 的提示写成「等插件启用」，修法彻底错。`inactive` 对应新 i18n 族 `earnable.inactive.*`，既有 `orphan.*` 与它的消费者 `SFAdminTrustLevelEarnableMatrix.vue` 一字不动。
- 决策记录：`knowledge/decisions/2026-11-14-trust-level-earnable-plugin-keys.md`（写明为何从「解码期硬拒」改为「运行期过滤 + 写期拒绝」）。

**M4.3 事件与状态账本**
- 新表 `user_trust_levels(user_id PK, level, tier_id, computed_at)`：**只做变更检测，不参与授权**。
- 新 job `trustlevels.recompute_user`：现算 → 与账本比较 → 变化则 upsert + 发 `trust_level.changed`；`incomplete` 时不发。
- 触发点：复用现有 `badgeEvaluation` 入队点（主题/评论/点赞/收藏/访问共 6 处）再入队一次；策略保存后按游标批量入队；每日 sweep 兜底（覆盖角色变更、OAuth 解绑、徽章收回等无活动事件的变化）。
- 视图 `sforum_core_v1.forum_user_trust_levels`（文档标注「快照账本，非权威判定」）。**必须 `DROP VIEW IF EXISTS` + `CREATE VIEW`**（schema 隔离测试库会重放整条迁移链，`CREATE OR REPLACE` 会 42P16）＋ owner 自对齐（从同域既有表读 owner，`ALTER … OWNER TO`）。
- HostQuery：
  - `sforum.core.public_trust_level_catalog.list`
  - `sforum.core.public_user_trust_level.get`（**现算**，权威；等级本身已是资料页公开信息）
  - 各配 allowed/denied 测试；schema `properties` 与 `Fields` 等长；注册共 5 处代码 + 2 份密封清单。

**M4.4 文档**：`docs/extensions/trust-levels.md`（双语）+ `docs/extensions/badge-plugin-facts.md` 补「同一事实也被信任等级消费」+ 索引登记。

### 4.6 M5：收口

- 重写 `knowledge/modules/trust-levels.md` 的 Extension Surface（现为 `TODO(核实)`）+ 新增决策记录 2 份 + 更新 `knowledge/index.md` 与 handoff + 计划 Status。
- 验收矩阵（脚本 + 变异）。至少 6 条变异必须转红：
  1. 数值预筛失效（规则在数值未过时也被查询）→ 查询计数断言红；
  2. 规则错误被吞成成立 → fail-closed 断言红；
  3. 公开面泄露 AST → 值级断言红；
  4. 未声明 factKey 返回 0 而非 unknown → `lte` 误判断言红；
  5. `incomplete` 时仍发事件 → 事件计数断言红；
  6. 插件键绕过运行时白名单 → 授予断言红。

---

## 5. 性能预算与实测口径

> **基线更正（2026-11-14，研究刀 E2）**：本表原来的「现状 1 条」是错的。信任链自身是 **+2**（`applyTrustLevel` 无条件先读 `users.created_at,kind`，再 +1 `user_activity`），整条已认证路径固定 **5** 条（主查询 + 2 RBAC + users + user_activity），另 0~1 overrides。

> **事实更正（2026-11-14，M5 收口）**：本计划早期版本写过「`features.trust_levels` 这个开关不存在」——**错**。
> 错因：当时只 grep 了 `apps/api/config/*.go` 与 `apps/api/app/Support/Features/*.go` 两个**根本不存在的路径**，
> 把「路径写错」当成了「开关不存在」。由老后（task-1）独立核实推翻：开关真实存在，常量 `NameFeatureTrustLevels = "features.trust_levels"`，
> 定义在 `apps/api/app/Models/Options/feature_flags.go:35`（运行时 options 行；`Models/TrustLevels/resolver.go:11` 的 `ErrDisabled` 注释亦点名）。
> **核这类「某开关/某键是否存在」，入口是 `app/Models/Options/`**，别从 `config/` 或 `Support/Features/` 起手。

| 场景 | 规则侧新增（相对无规则） | 目标 |
| --- | --- | --- |
| 无规则站点 | 0 | **0 条条件 SQL**（闸在构造求值器之前） |
| 有规则、数值未过 | 0 | **0 条**（合成根不含该档 → 其独有 type 不查） |
| 有规则、数值过但规则未过 | +1 次 `Prepare` | 按 type 分组，**SQL 次数 = 出现的 type 数**（与档数无关） |
| 8 档全配规则（最坏） | 1 次 `Prepare` | 仍是 type 数（memo 跨档复用，**不是** 8 次） |

### 5.1 已证到哪、还差什么（**M5 收口更新**）

| 判据 | 状态 | 证据 |
| --- | --- | --- |
| 无规则 → 0 条条件 SQL | **已证** | `TestNoRuleSiteIssuesNoConditionQueries`（tracer）+ `TestNoRulesNeverBuildsEvaluator`（工厂 0 次调用） |
| 8 档×32 叶 → SQL 次数 = type 数（4） | **已证** | `TestPrepareIssuesOneQueryPerConditionType`，tracer 挂 `pgxpool.Config.ConnConfig.Tracer` 统计整池、**含 Prepare** |
| 数值不过 → 该档独有 type 零 SQL | **已证** | `TestNumericFailureSkipsUniqueConditionType`（`object.authoring` 全策略仅 TL2 一处） |
| 规则求值增量 P95 | **已证 1.6ms**（真库 2000 用户） | `TestRuleEvaluationLatency` |
| **装载路径 P95 增量 ≤ 5ms** | **已证**（区间口径，不写点值） | 真 loader（`GetCurrentUser` 全路径）+ 10k 夹具，8 次全量运行共同见证 **ΔP95 ∈ [−0.41ms, +1.0ms]**，最坏仍有 5 倍余量。机制另有一条不依赖时延样本的证据：常开段 `TestLoaderPathSQLCounts` 的 `B − A == 1`（+1 条主键点查）。抽样口径与失效姿态见下。 |

**已证到什么程度（逐条，别压缩成一句「已证」）**

1. **判据已证**：真 loader（`GetCurrentUser` 全路径）+ 10k 夹具，8 次全量运行的 ΔP95 全部落在 **[−0.41ms, +1.0ms]**，最坏值距 5ms 判据仍有 5 倍余量（区间上界 1.0ms 对 5ms）。为什么只认区间不认点值：单轮 16 个采样里 P50 与 P95 都出现过反向（同一配置跑两遍，差值符号会翻），**效应量与这台机器的尾噪同量级**；所以能引用的是区间与机制，不是某一轮的差值。夹具规模/样本数属实测条件，**复跑会漂**，不当规格写。
2. **机制（首选证据，不依赖时延样本）**：系统成本 = 规则侧新增的条件类型数（本档夹具 1 个 type ⇒ **+1 条主键点查** `SELECT users.created_at`），由常开段 `TestLoaderPathSQLCounts` 的 `B − A == 1` 与「带规则档位 3 → 1 时条数不变」两条断言钉死。
3. **A/B 只能按策略形状做**：`features.trust_levels` 开关存在（见 §5 顶部更正），但关掉它是 `ErrDisabled`——policy 与 activity 两段都短路，实测关闭态 **4** 条 SQL，比侧 A（5 条）少的正是 `FROM user_activity`。那是「本功能上线前」的基线，不是「有等级但无规则」；拿它当对照量出来的是「功能开 vs 关」，与本里程碑（加规则）的增量无关。侧 A / B / B1 三种策略形状由 `loaderPolicies` 提供。

**反 stub：已证 ≠ 已加固（必须与上面并列读）**

- **当时的防线状态**：那两个性能断言挂在人工环境变量后（`loader_latency_postgres_integration_test.go:37` 的 `SFORUM_TRUST_PERF=1`，今天仍是可选段）。M3 收口时「已证 ≤5ms」的成立条件是「有人手动跑过」，**CI 里没有任何一条会红**。
- **现已补上常开段**：`TestLoaderPathSQLCounts`（`loader_queries_postgres_integration_test.go`）不读任何 `SFORUM_TRUST_PERF*`、随数据库测试套件跑，断 **A=5 / B=6 / B1=6 / 关闭态=4** 与「带规则档位 3→1 条数不变」「关闭态恰好少一条且不含 `FROM user_activity`」。20 个用户夹具足够——条数是**常数**，20 与 10k 逐字相同；整套 0.52s（单次实测值，随机器漂）。
- **它挡的正是这个反例**：把 `bootstrap` 的 `trustLevelActivityReader` 换成缓存读（做性能优化时最自然的动作）→ SQL 5 掉到 4、`FROM user_activity` 整条消失；若这条断言还在 env 后面，CI 全绿，而**线上全员等级静默掉 TL0**。保险丝不能装进抽屉。
- 所以措辞口径是：判据**已证**，防线**到 M5 才常开**。「已证」写的是「有人量过并留下区间」，不是「此后 CI 一直守着」。
- **超了怎么办（口径不变，别因为「已证」就丢掉）**：先查缺失索引，**不许**加缓存绕过——Cache 包明确不缓存鉴权状态；而且今天的常开断言正是靠「每次装载都真的读那 5 条」才成立，加缓存等于先把断言变成假的，再让它继续绿。

**装配类交付的教训（同一类静默失效，留着不删）**

- M4b-2 第 1 段落地时，入队点（`bootstrap/badges_wiring.go`）、job 定义、事件载荷（`Support/Events/catalog.go` 的 `trust_level.changed`，`observe` 型）全在，唯独 **`RegisterRecomputeUser` / `RegisterRecomputeBatch` 只有定义、没有调用点**：job 入队成功、无消费者、`trust_level.changed` 运行期永不发出，**CI 全绿**。pm 独立核到时按「定义存在」搜了一遍，找不到任何调用者。
- 现已补齐：`bootstrap/trust_levels_worker.go:116-127` 是 recompute 链路**唯一**注册面，`bootstrap/worker.go:748` 调用它。
- 教训留在这里：**装配类交付必须配一条能红的注册/消费断言。** 今天能红的只有入队侧（`bootstrap/user_evaluations_enqueuer_test.go` 断言两种 job kind 都被入队）；**消费侧仍无断言**——把 `worker.go:748` 删掉，CI 依旧全绿。这一条与上面的「env 后面的断言」是同一个病：装配错误不归任何测试管。

**措辞纪律**：任何报告/注释里出现「装载 P95 = 1.6ms」都是错的——那是规则求值成本。已据此要求修 `rule_eval_postgres_integration_test.go:305-309` 的仓库内注释（task-22）。

---

## 6. 明确不做（写进模块笔记，别当 TODO 捡）

1. 审核信号条件（举报/隐藏/删除）——负向闸门需要时用 `not` 包现有条件。
2. 内容质量条件（`content.pinned` / `content.hidden`）——不在信任等级子集。
3. 阅读行为（`user_topic_reads`）——不建表；插件可用事实层自供。
4. RPC 条件求值器 / 晋升闸门 / 接管等级来源（D14）。
5. 等级反规范化列——授权仍实时算，`user_trust_levels` 只是变更账本。
6. 多条件规则的公开「逐条清单」——只做扁平 AND；`any`/`not`/嵌套给 satisfied/total。
7. 规则改严后的追溯收回——立即降级已覆盖。

---

## 7. 风险与已知陷阱

1. **回滚**：写过 `rule` 的配置旧二进制解不开，回滚须先清空。同 `color` 的先例，文档写明。
2. **diff 噪声**：41 个 import 属机械改动，必须单独 M0 commit 并独立跑徽章全量测试。
3. **错误码三处同步**：漏一处 → 门禁红或 en-US 站点显示中文。
4. **`Options == nil` vs `[]string{}`**（徽章踩过）：`variant` 是开放集；空候选不得渲染成空下拉并禁止保存。
5. **解码路径不许查库**：`DecodeConfig` 被资料页/论坛 provider 读路径调用，任何 DB 校验放进去都会拖慢或拖挂。
6. **公开面泄露 AST**：`rule` 只进后台读面；用值级（不是键存在性）断言钉住——徽章那刀已经证明「键在值空」会假绿。
7. **事件洪水**：DB 抖动会让规则全失败；`incomplete` 禁发事件，必须有变异测试。
8. **插件键孤儿**：插件停用后配置仍含其键，必须惰性失效而非解码失败。
9. **owner 与视图**：新表/新视图的 owner 必须与既有 Core 对象同源，否则 kernel 报 `extension database physical resource conflict`（错得极具误导性）。用 `DO` 块从 `public.badges` 读 owner 自对齐。

---

## 8. 里程碑与分工

| 里程碑 | 内容 | 角色 | 验收载体 | 可独立回滚 |
| --- | --- | --- | --- | --- |
| M0 | 条件包迁移（41 import） | backend | `go build ./...` + 徽章全量测试 | 是 |
| M1 | 配置/校验/目录/契约 | backend + pm（5 条错误码文案） | 单测 + OpenAPI refs + 路由 435 | 是 |
| M2 | 求值链路 + 公开进度 | backend | 真值表 + PG 集成 + 性能实测 | 是 |
| M3 | 共享编辑器 + 后台 UI + 影响弹窗 | frontend + designer | `bun test` + typecheck + 桌面/390 QA | 是 |
| M4 | 插件四面 + 事件 + 读面 | backend + researcher（契约核实） | manifest 双侧断言 + 命令/查询 allowed-denied + 夹具 | 是 |
| M5 | 知识库 + 验收矩阵 + 门禁 | reviewer + ops | 矩阵 ok/FAIL/pend + 6 条变异全红 | — |

M4 延期不影响 M0–M3 上线（规则树与 UI 独立可用）。

---

## 9. 假设

1. 徽章条件 SQL 的口径（软删仍计入、自赞排除、日界由 Go 传参）对信任等级同样成立——同一份实现，不另立口径。
2. `user_profiles` 的 `bio` / `signature` / `website_url` 三列是资料编辑器的真实写入口（`identity.profile` 条件依赖它）。
3. 现 `badgesFactCatalog`（5s TTL）可直接作信任等级的 factKey 来源，不新建第二份快照。
4. 运营接受「规则条件不参与数值进度条」：资料页仍显示数值瓶颈，规则条件以清单呈现。

---

## 10. 实施备注

- 团队当前**零成员**（`team_status` size 0 / capacity 8）。批准后先 `team_start` 补齐 pm/frontend/backend/reviewer/researcher/designer/ops，再 `dispatch_tasks`。
- M0 与 M1 之间必须有一道「徽章全绿」的门，因为 M0 是纯机械迁移。
- 每个里程碑单独 `team_task_create` 写清目标/验收/涉及文件，由 owner 认领；完工必须带证据（命令 + 输出 / diff / 测试结果）才能 `accept_task`。
