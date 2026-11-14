# 实施事实核实简报：信任等级条件化门槛

Status: **已核实（只读核验，未改生产代码）**。核验时间 2026-11-14 11:15–11:40 (+08:00)。
被核计划：`knowledge/plans/2026-11-14-trust-level-conditions.md`（下称「计划」）。

## 0. 三条必须先读的结论（含二次复核）

1. **M0 已经落地，计划把它当待做**。核心核验时 HEAD 为 `e4f8c3b1f refactor(badges): 条件包迁到
   Models/Conditions`（57 文件、18 个包文件迁移、41 处引用面改写），二次复核时为 `322f088e5`
   （仅文档提交）。二者都**不是**计划写的 `6502fb25b`。计划 §0 表、§4.1「M0」、§8 里程碑 M0 行
   需要改写为「已完成」。
   证据：`git log --oneline -3`、`git reflog -5`、`git show --stat e4f8c3b1f`、
   `git ls-tree -r HEAD --name-only | grep -c "Models/Conditions"` → 18。
2. **本简报下文的行号一律以 `6502fb25b`（计划基线）为准，路径同时给两版**：M0 只是 `git mv` +
   import 字符串替换，**行号逐个不变**（实测 7 个关键文件新旧行数完全一致，见 §1-A1），
   所以 `apps/api/app/Models/Badges/conditions/x.go:N` = `apps/api/app/Models/Conditions/x.go:N`。

3. **二次复核（2026-11-14 11:52，HEAD `322f088e5`）**：按 §1 的命令在当前工作树重跑行号敏感项，
   全部**无漂移**：tripwire `registry_test.go:534`、`useTrustLevelSaveErrors.ts:138/157`、
   `dto.go:16/23/129`、`batch.go:78`（`Prepare`）/`:107`（`Resolve`）、`requirement_text_render.go:28/87`。
   但**修了一个我自己的行号错**：`type levelDefinition` 在 `dto.go:23`，不是 `:35`（`:16` 是
   `publicPayload.Levels`），计划 §0.1 的 E4 行沿用了这个错号，需同批改。
4. **三次修订（2026-11-14 11:58）：撤回一条我自己加的伪约束**。我先前写「合成根让各档叶子叠加 →
   保存期必须按合计判上限，否则装载期 Prepare 才越界」，**实测不成立**：`Prepare` 无任何上限检查
   （`Conditions/batch.go:78-104` 只做 `collectLeaves` → type 排序 → 每 type 一次 `load`），
   `Eval` 也不校验（`RuleEngine/eval.go:23` 注释自述「Eval 不要求先 Validate」）；上限只在
   `Validate`（`validate.go:16-20 / 107-164`）。正确口径见 §2-13 与 §4 的 D19 行（计划 D19-b）。

核验方式：只读命令（`rg` / `sed` / `git show` / `git ls-tree` / `python3 -c`），未运行任何 Go 测试、
未连库、未跑 `sqlc generate`、未改生产代码。

---

## 1. 事实表

### A. 判定与装载链路

| # | 断言 | 实测值 | 核验命令 | 文件:行号 | 与计划 |
| --- | --- | --- | --- | --- | --- |
| A1 | M0 迁移是否行数中立 | 行数逐个相同（phrases 269 / registry 359 / refnames 229 / refcandidates 210 / facts 193 / resolve_identity 225 / batch 292） | `for f in ...; do git show 6502fb25b:...Badges/conditions/$f \| wc -l; wc -l < ...Conditions/$f; done` | 两版同名文件 | 一致（计划未断言，但下游依赖它） |
| A2 | `Requirements` 定义 | 6 个 int64，JSON 名 = 指标名，AND 语义 | `sed -n '118,131p' apps/api/app/Models/TrustLevels/types.go` | `types.go:118` | 一致 |
| A3 | `Metrics` 定义 | `var Metrics []string`（6 项，稳定顺序） | `sed -n '133,141p' types.go` | `types.go:133` | 计划未给位置（见 §2-6） |
| A4 | `Requirements.Value()` | `func (r Requirements) Value(metric string) (int64, bool)` | `sed -n '143,160p' types.go` | `types.go:143` | 计划未给位置 |
| A5 | `Activity.Value()` | 同上签名，读行为统计 | `rg -n "func \(a Activity\) Value"` | `types.go:186` | 计划未提 |
| A6 | `satisfies` 六字段 AND | 55 行起 6 个 `>=` | `sed -n '48,56p' level.go` | `level.go:49` | 一致 |
| A7 | `LevelOf` 两段式 | 账龄闸（`NewUserDays>0 && age<nd`）→ `for rank := lastRank; rank >= LevelMember; rank--` | `cat -n level.go` | `level.go:22`（闸 29-34，遍历 35-39） | 一致 |
| A8 | `LoadActor(` 调用点 | **55** | `rg -o "LoadActor\(" apps/api -g '*.go' -g '!*_test.go' \| wc -l` | 全仓 | 一致 |
| A9 | `applyTrustLevel` 位置 | `postgres_store_roles.go:228` | `rg -n "func .*applyTrustLevel"` | `postgres_store_roles.go:228` | 一致 |
| A10 | 信任链固定 SQL 增量 | **+2**（`users` 1 条 + `user_activity` 1 条），另有 0~1 条条件读 | 见 §1-A10 明细 | `:240`（users）、`:250`→`resolver.go:81`（activity）、`:282`（overrides） | **不一致**（计划写「固定 +1」） |
| A10b | 求值真正发生的函数 | `GetCurrentUser`（`postgres_store.go:135` 调 `loadCurrentUserAccess`）；`LoadActor` 只是复用结果（`:394-397`，注释自称不重算） | `sed -n '120,140p;394,420p' postgres_store.go` | `postgres_store.go:135` | **表述不准**（计划写「`LoadActor` 装载期」） |
| A11 | options 三次读是否落 SQL | 走同一份 TTL 缓存 map，命中时 0 SQL | `sed -n '632,660p' apps/api/app/Models/Options/service.go` | `service.go:632 loadMap` / `:642 loadMapWithFreshness` | 计划未提 |
| A12 | `TrustLevelSource` 接口 | 2 方法：`Grants(ctx, userID, kind, createdAt) (level int, earned []string, err error)`、`BaselineGrantsFor(level int) []string` | `sed -n '17,41p' apps/api/app/Models/Identity/trust_levels.go` | `identity/trust_levels.go:24` | 一致（计划 §4.3 未给签名） |
| A13 | 事务路径 loader | `LoadEffectiveActorTx(ctx, tx, userID, trust)`，`:232` 调同一个 `resolveTrustGrants` | `sed -n '113,240p' actor_postgres_tx.go` | `actor_postgres_tx.go:113`、`:232` | 一致 |
| A14 | 池路径失败姿态 | 未注入/关闭/读失败 → `ok=false` → 保持未解析 | `trust_levels.go:48-62` | 同左 | 一致 |
| A15 | tx 路径 `LoadCurrentUserAccess` 是否并等级 | **不并**（只跑 `loadCurrentUserAccess`，无 `applyTrustLevel`） | `sed -n '286,292p' postgres_store_roles.go` | `:288` | 计划未提（影响「两条 loader」表述） |

**A10 明细（代码路径推导，非 PG 侧实测）**：

| 序 | SQL | 位置 | 是否无条件 |
| --- | --- | --- | --- |
| 0 | `GetCurrentUser` 主查询（users ⋈ appearance ⋈ profiles ⋈ attachments） | `postgres_store.go:120-133` | 是（每条 `GetCurrentUser`） |
| 1 | `ListUserRoleKeys` | `postgres_store_roles.go:331` | 是 |
| 2 | `ListUserPermissions` | `:335` | 是 |
| 3 | `SELECT created_at, kind FROM users WHERE id=$1` | `:240-243` | **是**（`trustLevels == nil` 也读） |
| 4 | `user_activity` 单行（`badges/activity_store.go:60-65`） | `resolver.go:81` | 仅产品开启且非 bot |
| 5 | options `List`（`loadMap`） | `Options/service.go:660` | TTL 未命中才读 |
| 6 | permission overrides（`listPermissionOverrides`） | `:282` ← `:285` | 仅 `needsBaselineDeduction`（`trust_level_grants.go:46`） |

实测脚本（未执行，交 M2 用）：
```
EXPLAIN (ANALYZE, BUFFERS) 上述 3/4 两条；或 pg_stat_statements 按 query 前缀聚合，
对比 features.trust_levels 开/关各 1k 次已认证请求的 calls 差值。
```

### B. 条件目录（13 条，逐条）

`key` / ValueKind / 可比较 / params / 真值来源（SQL 常量位置）：
文件在 M0 前后同内容，下表用**新路径** `apps/api/app/Models/Conditions/`。

| key | ValueKind | AllowedCompare | params（`ParamsSchema.Specs`） | 真值来源 / SQL |
| --- | --- | --- | --- | --- |
| `activity.count` | count | gte / lte | `metric` enum(6 指标, 必填) + `window`(必填) | `resolve_activity.go:33 activityCountSQL`：`topics` / `comments` / `content_reactions` / `content_bookmarks` / `user_visits`，按 metric 分支 |
| `activity.streak` | count | gte / lte | `minDays` int 1..`MaxWindowDays` | `resolve_activity.go:168 activityStreakSQL`：`user_visits` |
| `object.engagement` | boolean | —（boolean 禁止 cmp） | `action` enum(like/bookmark/comment) + `topicId` int≥1 | `resolve_object.go:19`：`content_reactions` / `content_bookmarks` / `comments` |
| `object.authoring` | count | gte / lte | `targetKind` enum(category/tag) + `targetSlug` objectRef(RefKind `category`) + `window` | `resolve_object.go:89`：`topics`（按 kind 选 slug 列） |
| `identity.account_age` | count | gte / lte | 无 | `resolve_identity.go:37 accountAgeSQL`：`users.created_at`，**天数在 Go 侧折算**（`calendarDaysBetween`，`:64`） |
| `identity.email_verified` | boolean | — | 无 | `resolve_identity.go:80`：`users.email_verified_at IS NOT NULL` |
| `identity.oauth_link` | boolean | — | `providerId` objectRef(RefKind `provider`) | `resolve_identity.go:102`：`identity_external_links` 且 `status='active'` |
| `identity.role` | boolean | — | `roleKey` objectRef(RefKind `role`) | `resolve_identity.go:155`：`user_roles ⋈ roles`，按 `roles.key` 且 `is_enabled` |
| `identity.avatar` | boolean | — | 无 | `resolve_identity.go:205`：`user_profiles.avatar_attachment_id IS NOT NULL` |
| `content.pinned` | count | gte / lte | 无 window（等价全部时间） | **`resolve_content.go:15 pinnedSQL`**：`topics WHERE author_user_id=$1 AND is_pinned`（不过滤软删） |
| `content.hidden` | count | gte / lte | 无 window | **`resolve_content.go:21 hiddenSQL`**：`topics ... AND status='hidden'`（**唯一**按 status 判定） |
| `badge.owned` | boolean | — | `slug` objectRef(RefKind `badge`) | `resolve_content.go:27 badgeOwnedSQL`：`user_badges ⋈ badges`，不看 `badges.enabled` |
| `fact.count` | count | gte / lte | `factKey` objectRef(RefKind `fact`) + `variant` enum(**Options nil = 开放集**) + `window` | `resolve_facts.go:25`：累计读 `user_fact_counters`，窗口读 `badge_facts` |

其他已核事实：

| 断言 | 实测值 | 命令 / 位置 |
| --- | --- | --- |
| 条件条数 | **13** | `Conditions/registry.go:214-351`（`newCoreRegistry` 13 个 `Definition`）；`Conditions/conditions_postgres_integration_test.go:264` 断言 13 |
| `window` 参数 | `{kind: all\|recent_days, days: N}`，`days` ∈ `MinWindowDays..MaxWindowDays` | `RuleEngine/validate.go:325-341` |
| 软删口径 | 除 `content.hidden` 外一律不过滤 status/deleted_at | `registry.go:8-12` 包注释 |
| 条件包是否含 ref 查询 SQL | **不含**（0 条 SQL） | `rg -l "SELECT\|FROM " Conditions/*.go \| grep -v _test` → 只有 `resolve_*.go` |
| 引用候选的查询入口 | `(*badges.PostgresStore).RefCandidates(ctx)`、`(*badges.PostgresStore).LoadRefNames(ctx, refs)` | `Badges/ref_candidates.go:31`、`Badges/ref_names_postgres.go:45` |
| `Phrase` 签名 | `func(params map[string]any, cmp *ruleengine.Compare, names RefNames) (zh, en string)` | `Conditions/phrases.go:24`（`PhraseFunc`） |
| `RefNames` 零值安全 | 是：8 个 map，所有访问器未命中回落泛称（`fallbackCategory` 等） | `Conditions/refnames.go:17-43, 152-197` |
| 逐叶调用 `Phrase` 的前置数据 | **可选**：`conditions.NewRefNames()` 即可产出完整句子（泛称）；要真名需 `badges.CollectRefs` + `LoadRefNames`（每类一次 SQL） | `refnames.go:65`、`Badges/ref_names.go:71/152` |
| 整段文案入口 | `BuildRuleRequirementText(rule, names) RequirementText`；**整段 ≥160 rune 会降级**成「满足 N 个条件，其中最关键的是…」 | `Badges/requirement_text.go:40`、`requirement_text_render.go:28/75/87/100` |

### C. 配置、错误码、契约、路由

| # | 断言 | 实测值 | 命令 / 位置 | 与计划 |
| --- | --- | --- | --- | --- |
| C1 | 错误码条数 | **33** | `sed -n '110,142p' config.go`（33 行）+ `config_test.go:411-445` | 一致 |
| C2 | 常量块位置 | 5 个分组：`schema/JSON` 67-73、`tier/id/icon/color/name` 75-83、`requirement/limits/earnable` 85-94、`level_*` 96-99、`deleted_*` 103-105 | `sed -n '60,107p' config.go` | 计划只写「常量块」 |
| C3 | `configErrorReasonCodes` | 109-143（复制出口 `ConfigErrorReasonCodes()` 147） | 同左 | 一致 |
| C4 | 稳定性测试 | `TestConfigErrorReasonCodesAreStable` @ `config_test.go:410`，字面量 411-445 | `rg -n "TestConfigErrorReasonCodesAreStable" -A 40` | 计划未给行号 |
| C5 | OpenAPI enum | `contracts/openapi/schemas/trust_levels.yaml:361-393`（33 项，`enum:` 在 361） | `sed -n '350,400p'` | 计划未给行号 |
| C6 | 前端 `BACKEND_CODES` | `tests/admin/forum/trustLevels.test.ts:551`（33 项 `as const`） | `rg -n "const BACKEND_CODES"` | 一致 |
| C7 | 前端三处长度断言 | `:595` 33 / `:596` 15 / `:597` 18（describe 标题在 `:549`） | `sed -n '593,600p'` | **行号略偏**（计划 595-600，实际 595-597；549 的标题串也要改） |
| C8 | 两个码表的**定义**位置 | `apps/web/app/composables/admin/forum/trustLevels/useTrustLevelSaveErrors.ts:138`（specific 15）、`:157`（bucket 18） | `sed -n '130,180p'` | **计划漏文件**（见 §2-3） |
| C9 | locale 路径与断言 | `apps/web/i18n/locales/zh-CN.json`、`en-US.json`，键路径 `admin.forum.trustLevels.errors.code.<code>`；两边各 15 条 | `python3 -c "json.load(...)['admin']['forum']['trustLevels']['errors']['code']"` → 15/15 | 一致 |
| C10 | `TierConfig` 形状 | 8 字段：`id` / `name` / `icon` / `color,omitempty` / `requirements` / `limits`(RawMessage) / `earnable`(RawMessage) / `retired` | `sed -n '185,205p' config.go` | 一致（计划示例 JSON 与字段名逐字相同） |
| C11 | `DecodeConfig` 是否无 ctx/DB | `func DecodeConfig(raw string) (Policy, error)`，纯读 | `config.go:348` | 一致 |
| C12 | `DecodeConfig` 读路径调用点 | `Providers/forum.go:285`、`Providers/profile.go:39`、`Options/trust_level_options.go:46`、`bootstrap/trust_levels_wiring.go:38` | `rg -n "trustlevels.DecodeConfig"` | 一致（计划说「等」） |
| C13 | `FieldError` 形状 | `Rank Level` + `TierID string` + …，rank=0 表示不可定位 | `config.go:224-260` | 一致 |
| C14 | 路由 tripwire（三处） | `registry_test.go:534`（`!= 433`，注释 528）、`catalog-identities.json` `routes` 长度 **433**、`core_catalog_gen.go` **433** 条 `CoreRoute` | `grep -n "!= 433"`、`python3 -c "len(d['routes'])"`、`rg -c 'ID:\s+"' core_catalog_gen.go` | **行号错**（计划 536） |
| C15 | `ui` 身份数 | **434** | `python3 -c "len(d['ui'])"` | 计划未提（新组件要动它） |
| C16 | 信任等级现有路由 | 4 条（公开 1 + 后台 3，含 restore-defaults） | `controller.go:77-80`；`catalog-identities.json` 4 条 `core.route.trust_levels.*` | 一致（+2 → 435 可行） |
| C17 | `TrustLevelViewer` schema | `trust_levels.yaml:178`，现有 `level/levelSlug/nextLevel/bottleneck` | `sed -n '170,200p'` | 一致 |
| C18 | 公开/后台是否共用 DTO | **共用** `levelDefinition`：`publicPayload.Levels []levelDefinition`（`:16`）、类型定义 `:23`、`adminPayload.Levels []levelDefinition`（`:129`），且 `buildAdminPayload` 的返回类型也是 `publicPayload`（`payload.go:18`） | `dto.go:16,23,129`、`payload.go:18-22` | **冲突**（见 §2-4） |
| C19 | `rule-text` 端点 | `admin.Post("/rule-text", …)` | `controller.go:123` | 一致 |
| C20 | 多条件文案连接符 | `，并且` / `，或者`（`renderNode`/`joinClauses`），纯标点连接 | `requirement_text_render.go:157-159` | 一致 |

### D. 插件面、事件、job

| # | 断言 | 实测值 | 命令 / 位置 | 与计划 |
| --- | --- | --- | --- | --- |
| D1 | `ManifestPermissionDefinition` 字段 | `key` / `contractVersion` / `label` / `description` / `recommendedRoles,omitempty` / `assignmentPolicy` | `v3_platform_types.go:217-224` | 一致（新增 `trustLevelEarnable` 落这里） |
| D2 | 权限声明校验函数 | `(*v3Validator).validateIdentityAndPermissions`（`v3_validate_platform.go:648`）；要求 label/description 非空、`assignmentPolicy=="host"`、`recommendedRoles ≤ 上限` 且不含 `super_admin` | `sed -n '648,672p'` | 计划未给位置 |
| D3 | manifest schema 是否封死新字段 | `$defs/permissionDefinitions` `additionalProperties:false` + `required`（`schemas/manifest-v3.schema.json:2573-2588`） | `rg -n "permissionDefinitions" -A 40 schemas/manifest-v3.schema.json` | 一致（「只改 struct 会被 schema 拒」成立） |
| D4 | 是否存在 struct↔schema 的 reflect 对齐测试 | **不存在**（`ExtensionManifest` 的 reflect 断言只比对个别夹具字段） | `rg -n "reflect" apps/api/app/Support/ExtensionManifest/*_test.go` | 一致 |
| D5 | `catalog-identities.json` 是否涉及权限声明 | **不涉及**：顶层只有 `schemaVersion` / `routes` / `ui` | `python3 -c "list(d.keys())"` | 计划问「是否涉及」→ 否 |
| D6 | 信任披露弹窗 | `apps/web/app/components/admin/SFAdminExtensionEnableDialog.vue`（211 行），渲染 `capabilityGrants` 的 **label + key**，无 description | `rg -n "capabilityGrants" -A 6` | 基本一致（计划说只渲染 label；实际还渲染 key） |
| D7 | `badgesFactCatalog` 位置/可见性 | `bootstrap/badges_fact_catalog.go:25`，类型**未导出**、TTL `5s`（`:44`）、失败回退旧快照 | `sed -n '14,60p'` | 一致 |
| D8 | 构造顺序 | catalog 在 **domains** 阶段 `api_assembly_domains.go:640` 才建；`newTrustLevelSource` 在 **infra** 阶段 `api_assembly_infrastructure.go:242` | `sed -n '118,140p' api_assembly.go`（infra→extension→domains） | **阻塞**（见 §2-5） |
| D9 | 依赖方向 | `TrustLevels` 只 import `Identity` + `Support/Palette`；`Badges`、`Identity` **不** import `TrustLevels` | `rg -n "keeptrue/sforum" TrustLevels/*.go` | 一致（TrustLevels→Badges 无环） |
| D10 | `conditions.NewResolver` 签名 | `NewResolver(pool *pgxpool.Pool, userID int64, factKeys ...string) (*Resolver, error)` | `Conditions/batch.go:53` | 一致 |
| D11 | `Prepare` 是否可重复调用 / 是否 memo 命中 | 可重复调用，但**每次都按 type 重新查**，不检查 memo → 每档各 Prepare 一次会重复同一 type | `batch.go:78-104` | **与 §5「≤3 次 Prepare」冲突**（见 §2-7） |
| D12 | `ruleengine` 求值接口 | `Resolver{ Resolve(ctx, Leaf) (Value, error) }`；`Eval(ctx, node, resolver)` | `RuleEngine/types.go:101`、`eval.go:28` | 一致（引擎接口不用改） |
| D13 | 引擎上限 | `MaxDepth=5` / `MaxLeaves=32` / `MaxNodes=64` / `MinCompareValue=1` / `MaxCompareValue=1e9` | `RuleEngine/validate.go:15-26` | 一致 |
| D14 | 引擎已有稳定码 | 17 个：`decode_failed` / `version_unsupported` / `shape_invalid` / `too_deep` / `too_many_nodes` / `too_many_leaves` / `unknown_condition` / `compare_missing` / `compare_invalid` / `compare_not_allowed` / `compare_forbidden` / `value_out_of_range` / `param_unknown` / `param_missing` / `param_invalid` / `catalog_invalid` / `resolve_failed` | `validate.go:22-41` | **计划未提**（5 个新码是聚合，见 §2-8） |
| D15 | 事件目录位置 | 常量 `Support/Events/catalog.go:3-46`，定义表 `definitions`（`:50`），`Definitions()/FindDefinition` `:148/:157` | `sed -n '1,60p' catalog.go` | 计划未给位置 |
| D16 | 事件是否有条数 tripwire | 无（只有 SDK 层 `KnownEvent` 走 `events.Known`） | `rg -n "len\(events.Definitions" apps/api` | 计划未提 → 新增事件不撞计数 |
| D17 | badge 评估 job 形态 | `Jobs/Badges/evaluate_user.go`：`EnqueueOptions()` 返回 `Unique: river.UniqueOpts{ByArgs: true}` | `sed -n '30,60p' evaluate_user.go` | 一致 |
| D18 | job 注册位置 | `bootstrap/worker_forum_jobs.go:119` 调 `registerBadgesEvaluateUserWorker`（定义 `bootstrap/badges_wiring.go:119`） | `rg -n "registerBadgesEvaluateUserWorker"` | 计划未给位置 |
| D19 | 入队点数量 | 6 处：topic `postgres_store_topic_mutations.go:122`、comment `postgres_store_comment_writes.go:134`、reaction `postgres_store_reactions.go:62,63`、bookmark `postgres_store_bookmarks.go:47`、visit `badges/visits.go:136` | `rg -n "enqueueBadgeEvaluation"` | 一致（0「6 处」成立） |
| D20 | 延迟绑定先例 | `deferredBackfillEnqueuer.Bind(dispatcher)`（注释明确「先注册后绑定」） | `badges_wiring.go:145-160` | 计划未提（D8 的解法先例） |

---

## 2. 计划纠错清单（核心）

> 格式：**计划原文** → 实测 → 正确说法 → 证据。

**2-1｜M0 被当成待做（结论级错误）**
计划 §0 表、§4.1（整段）、§8 M0 行当 M0 未做；计划基线 `6502fb25b` 已被 `e4f8c3b1f` 超越。
正确说法：M0 已于 `e4f8c3b1f` 完成（18 文件迁移 + 41 处引用面改写，`rule_impact.go` / `rule_health.go` 留在
`Models/Badges`），M1 起一切落点用 `Models/Conditions/`；任何以 `6502fb25b` 为准的行号引用都需要
一次「基线 +1 commit」的确认。证据：`git reflog -5`、`git show --stat e4f8c3b1f`、
`git ls-tree -r HEAD | grep -c Models/Conditions` → 18。

**2-2｜「固定 +1 条 `user_activity` 读」漏算 `users` 行**
计划 §0 表「求值时机：固定 +1 条 `user_activity` 读」。
实测：`applyTrustLevel` 第一件事是**无条件** `SELECT created_at, kind FROM users`（`:240-243`，`trustLevels==nil`
也读），再加 `user_activity` 一条 → 信任链固定 **+2**；`baselineAllowOverrides` 条件 +1。
正确说法：一次已认证请求 = `GetCurrentUser` 主查询 1 条 + RBAC 2 条 + `users(created_at,kind)` 1 条 +
`user_activity` 1 条（+ 0~1 overrides）= **5 条固定**，信任等级**自身**新增 2 条；§5 的「现状 1 条」应改为
「信任链 2 条，其中 1 条是与判定无关的 `users` 行读」。另：求值发生在 `GetCurrentUser`
（`postgres_store.go:135`），`LoadActor`（`:394`）只是复用结果——计划把求值时机写成 `LoadActor` 装载期，
对「哪些入口会触发送等级计算」的推断是错的（`LoadActor` 55 个调用点 ≠ 55 个求值点，`GetCurrentUser`
非测试调用点 11 个）。证据：`postgres_store.go:120-140, 394-420`、`postgres_store_roles.go:217-288`。

**2-3｜§3.4 前端落点只点名测试文件，实际两张码表定义在 composable**
计划 §3.4 第 3 条：「`trustLevels.test.ts:595-600` 的 `BACKEND_CODES` 38 / `SPECIFIC_ERROR_CODES` 20 /
`CONFIG_BUCKET_ERROR_CODES` 18」。
实测：`SPECIFIC_ERROR_CODES` 与 `CONFIG_BUCKET_ERROR_CODES` **不是**在测试里定义的，而是
`useTrustLevelSaveErrors.ts:138` / `:157` 导出，测试只 `import` + 断言长度（`:595-597`）+ 标题串（`:549`
写死了「33 backend codes, 15 specific + 18 bucket」）。
正确说法：5 条新码要改 **4 处前端**：composable 的 specific 数组（15→20）、测试的 `BACKEND_CODES`
（33→38）、测试的断言字面量与 describe 标题、两份 locale 各 5 条文案。只改测试 → 编译/断言双红。
证据：`useTrustLevelSaveErrors.ts:130-180`、`trustLevels.test.ts:549-600`。

**2-4｜`+rule` 不能加在共用的 `levelDefinition` 上（最难修的一条，给具体做法）**
计划 §4.2：「`dto.go`、`payload.go` 后台读面下发 `rule`；公开面不下发」。
实测：公开面与后台面**共用同一个** `levelDefinition`（`dto.go:16` / `:23` / `:129`），而且
`buildAdminPayload` 返回的**就是** `publicPayload`（`payload.go:18`）——「后台字段」现在靠
`buildPayload(policy, includeLimits bool)` 的**运行期 bool** 把 `Limits` 置 nil 来区分（`payload.go:26-55`）。
即：今天的「后台独有字段」根本没有类型边界，只有一条「记得置 nil」的纪律。

正确做法（类型级隔离，wire 不变）：

1. `dto.go` 拆两个形状，公开形状**结构上不存在** `rule`（也不存在 `limits`）：
   ```go
   // publicLevelDefinition 是公开面唯一形状：没有 limits、没有 rule。
   type publicLevelDefinition struct {
       Level        int                     `json:"level"`
       LevelSlug    string                  `json:"levelSlug"`
       DisplayName  trustLevelName          `json:"displayName"`
       Icon         string                  `json:"icon"`
       Color        string                  `json:"color"`
       Requirements trustlevelsRequirement `json:"requirements"`
       Earnable     []string                `json:"earnable"`
       Retired      bool                    `json:"retired"`
       Editable     bool                    `json:"editable"`
   }

   // adminLevelDefinition 只多两个后台字段；匿名字段在 JSON 里被提升（flatten）。
   type adminLevelDefinition struct {
       publicLevelDefinition
       Limits *trustLevelLimits `json:"limits,omitempty"`
       Rule   *ruleengine.Node  `json:"rule,omitempty"` // 只有后台读面带 `rule`
   }
   ```
2. `publicPayload.Levels` → `[]publicLevelDefinition`；`adminPayload.Levels` → `[]adminLevelDefinition`。
   `payload.go` 的 `buildPayload(policy, includeLimits)` 拆成 `buildPublicLevels(policy)` 与
   `buildAdminLevels(policy)`（公共部分一个 `toPublicLevel(rank, tier, policy)`），
   `newcomerDefinition()`（`payload.go:66`）返回 `publicLevelDefinition`。
3. `controller.go:160-162`：`adminPayload.Levels` 改成吃 `buildAdminLevels(policy)`，不再从
   `buildAdminPayload(policy).Levels` 借类型。
4. **值级断言**（不是键存在性）：在 `controller_test.go` 造一份**带 `rule` 的** policy，
   序列化公开响应后断言字节里不含 `"rule"` / `"children"` / `"cmp"` / `"params"` / `"type"`；
   同时断言后台响应**含** `rule`（否则「拆完把 rule 弄丢了」也会绿）。徽章那刀的教训：只断言
   「字段不存在」时，一旦有人把 `omitempty` 去掉就会静默通过。

为什么不能只靠 `omitempty`：`rule` 一旦进了共用结构，公开面的唯一保护就是「投影时别忘了不赋值」，
而 `buildPayload` 已经证明这种纪律会被下一个改 `includeLimits` 的人顺手破坏；类型拆开之后，公开面
**没有** `Rule` 字段可赋值，漏泄在编译期就不可能。

代价与注意：`encoding/json` 对匿名字段的提升是标准库行为（本简报未跑测试实测）；两个形状会让
`payload.go` 多一层投影代码（约 20 行）。**wire 影响面**：公开面 JSON 与今天逐字相同（`limits`
本来就在公开面恒 nil+omitempty，拆掉后不会少字段）；后台面只多一个 `rule`。证据：`dto.go:16,23,129`、
`payload.go:18-80`、`controller.go:160-162`。

**2-5｜`badgesFactCatalog` 在装配顺序上晚于 `newTrustLevelSource`**
计划 §4.3：「`bootstrap/trust_levels_wiring.go` 注入 evaluator 工厂（pool + factKeys 来源 = 既有
`badgesFactCatalog` 的 5s 快照，不建第二份）」。
实测：装配顺序 infra → extensionPlatform → domains（`api_assembly.go:124-137`）。`newTrustLevelSource` 在
infra（`api_assembly_infrastructure.go:242`），`badgesFactCatalog` 在 domains（`api_assembly_domains.go:640`，
依赖 `extensionService`）。类型未导出（同包可用），无循环依赖（`TrustLevels` 只 import `Identity`）。
正确说法：「直接传实例」在现有顺序下不成立。可选：把 catalog 提前到 extensionPlatform 阶段；或在
domains 阶段用**延迟绑定/闭包**注入 `func() []string`（既有先例 `deferredBackfillEnqueuer.Bind`，
`badges_wiring.go:145`）。这一条要给下游明确写法，否则 M2 会卡在装配上。证据同上。

**2-6｜§0 表两条「位置」写得不完整/偏移**
- 「可挣得权限白名单：`registry.go:33`」→ 字面量是 `:29-37`，`:33` 只是列表中间一行；出口
  `EarnableWhitelist()` `:40`、`IsWhitelisted()` `:45`、`Validate()` `:110`。
- 「路由 tripwire `registry_test.go:536`」→ 实际断言在 **`:534`**（`if len(catalog) != 433`），注释块 `:528-533`；
  另两处不是数字断言：`catalog-identities.json` 的 `routes` 数组（433 项）与 `core_catalog_gen.go`
  的 `generatedCoreRouteCatalog`（433 个 `CoreRoute` 字面量）。
- 「`Metrics` / `Value()`」计划没给位置，实际 `types.go:133` / `types.go:143`。
证据：见 §1 A3/A4/C14。

**2-7｜§5「同一 evaluator 跨档复用，≤3 次 Prepare」依赖一个不存在的性质**
计划 §4.3 / §5：「同一 per-user evaluator 在一次装载内跨档复用（memo 命中，同条件不重复查）」+
「8 档全配规则（最坏）≤ 3 次 Prepare」。
实测：`Resolver.Prepare` 只 `collectLeaves(root)` 后**逐 type 重查**，不读 memo 判断「这个 type 已经查过」
（`batch.go:78-104`）；`memo` 只在 `Resolve` 与写回时使用。所以「每档 Prepare 一次」= 同 type 被查 N 次。
正确说法：要拿到 §5 的预算，M2 必须**先把所有带规则档位的树合成一棵 `all` 根再 Prepare 一次**
（或给 `Prepare` 加「已加载 type 跳过」语义）。这是一个必须写进实现说明的约束，不是可选项。
证据：`Conditions/batch.go:78-104`（`Prepare`）、`:107-136`（`Resolve`）。

**2-8｜5 个新错误码是 17 个引擎码的聚合，计划没有映射表**
计划 §3.4 直接列 5 个码，未提 `ruleengine` 已有 17 个稳定码。
实测：`rule_too_complex` 对应引擎的 `too_deep`/`too_many_nodes`/`too_many_leaves`/`value_out_of_range`；
`rule_unknown_condition` ← `unknown_condition`；`rule_shape_invalid` ← `shape_invalid`/`compare_missing`/
`compare_not_allowed`/`compare_forbidden`/`param_missing`/`param_invalid`/`param_unknown`/`version_unsupported`/
`decode_failed`；`rule_reference_invalid` 引擎**没有**（属于写路径 DB 校验）；`rule_condition_not_available`
也**没有**（属于目录子集校验）。引擎 `Error` 自带 `Path`（`root.children[1].cmp` 形状），可直接喂
`FieldError.field`/`params.path`。
正确说法：M1 需要一张显式映射表 + 一条表驱动测试；`Validate` 只返回**首个**错误，所以「精确 422」的粒度是
「首个错误 + path」。证据：`RuleEngine/validate.go:22-41, 55-75`。

**2-9｜新增条件不止「写一句 Phrase」**
计划 §3.3 同步事务只列 `TestEveryConditionHasPhrase`。
实测该测试（`requirement_text_test.go:166`）有三重断言：① `len(definitions)==13`（`:168`）；
② 每个 `Phrase != nil`；③ **每个 key 在 `phraseSamples()`（`:55`）里有 golden 样本**，否则 fatal
「没有 golden 样本」。另有两处条数断言：`Conditions/conditions_postgres_integration_test.go:264`（13）
与 `:291`（publishable = 全量 − 1）。
正确说法：加 `identity.profile` 要同时改 4 处：`registry.go` 的 key 常量 + 定义 +
`phrases.go` 的句子函数 + `phraseSamples()` 的样本 + 两个 13→14 断言。
证据：`requirement_text_test.go:166-192`、`conditions_postgres_integration_test.go:264,291`。

**2-10｜`identity.profile` 的 params 取值与列名不一致（歧义）**
计划 §3.3 写 `field: enum(bio/signature/website)`，§9 假设 2 写 `website_url`。
实测 `user_profiles` 三列是 `bio` / `signature` / **`website_url`**
（`database/migrations/202607070004_user_profiles.sql:4-7`）。
正确说法：契约里是哪个字面量必须由 Lead 拍一次（见 §4），否则前后端各写一个，线上就是「选项保存成功、
永远不满足」。另外三列都是 `TEXT NOT NULL DEFAULT ''`，判据只能是「非空串」而不是 `IS NOT NULL`。

**2-11｜「§0 条件包被引用文件数 41」的口径**
41 是**文件数**（38 条真实 import + 3 处仅注释，M0 提交说明亦如此写）；`Models/Conditions"` 现为 38 个
import 文件、旧路径 0 个。正确说法：M1 之后若再动包路径，受影响面是 38 条 import + 3 处注释。
证据：`rg -l 'Models/(Badges/)?Conditions"' apps/api -g '*.go' | wc -l` → 38。

**2-12｜计划未写但会咬人的三处（补）**
- `renderRule` 有 **160 rune 硬上限**（`requirement_text_render.go:28/87`），超限降级成
  「满足 N 个条件，其中最关键的是：…」。§3.5「整档文案直接调 `BuildRuleRequirementText`」必须写明降级行为，
  否则前端会以为总能拿到完整 AND 句。
- `ruleengine` 对 **boolean 条件禁止 `cmp`**（`validate.go:175` 起）；`content.pinned/hidden` 是 count 且
  **没有 window 参数**（`batch.go:223` 注释），所以「把 content.hidden 包进 `not`」时不要给它补 window。
- `fact.count` 的 `variant` 是 `Options: nil` = **开放集**（`registry.go:344`）；信任子集裁剪时不能把
  nil 当「空候选」处理（§1 门禁纪律第 7 条同款）。

**2-13｜（自我纠错，2026-11-14 11:58）「合计上限」是我编的，实测不成立**
我早前在 §3-M2 与 §4 写过：「合成根让各档叶子叠加 → 保存期必须按合计判上限，否则装载期 `Prepare` 才炸」。
实测三项，全部否定这个推理：

| 检查点 | 实测 | 位置 |
| --- | --- | --- |
| `Prepare` 是否查上限 | **不查**：`collectLeaves` → type 排序 → 每 type 一次 `r.load` → 写 memo | `Conditions/batch.go:78-104` |
| `Eval` 是否查上限 | **不查**，且注释明确「不要求先 Validate，自己兜住畸形树」 | `RuleEngine/eval.go:23-27` |
| 上限在哪里 | **只在 `Validate`**：`MaxDepth=5` / `MaxLeaves=32` / `MaxNodes=64` 在 `:107/:163/:111` 处判定 | `RuleEngine/validate.go:16-20, 107-164` |

结论：叠加不构成越界风险（256 叶 `Prepare` 照跑，SQL 次数 = type 数）；若按我原说法加「合计判定」，
反而要造第 7 个错误码并拒掉每档都合法的配置（8 档 × 16 叶）。**真实风险是有人对合成根调 `Validate`**
（会撞 32/64 且报「太复杂」）。正确口径 = 计划 §0.2 的 **D19-b**：合成根只许 `Prepare`、禁止 `Validate`；
保存期仍按单档校验；可断言判据改成「8 档各 32 叶时 `Prepare` 成功且 SQL 次数 = 出现过的 type 数（与档数无关）」。
补一条让「SQL 次数 = type 数」可断言的证据：每个 loader **恰好一条** SQL
（`resolve_activity.go:91/189`、`resolve_content.go:48/67`、`resolve_facts.go:64`、
`resolve_identity.go:41/84/116/169/213`、`resolve_object.go:49/121`），而 `Prepare` 对每个 type 只调一次
`r.load`（`batch.go:92-103`）。
证据：`sed -n '78,106p' apps/api/app/Models/Conditions/batch.go`、`grep -n "r.pool.Query" Conditions/resolve_*.go`、
`sed -n '16,27p;100,170p' apps/api/app/Support/RuleEngine/{validate,eval}.go`。

---

## 3. 落点清单（按依赖顺序）

### M0（已完成，`e4f8c3b1f`）

无待办。新增代码一律落 `apps/api/app/Models/Conditions/`（package 仍 `conditions`）。

### M1：配置 / 校验 / 目录 / 契约

顺序（前者是后者前提）：

1. `Models/Conditions/registry.go`：`identity.profile` 的 key 常量（`:22-38` 区块）+ `Definition`（`newCoreRegistry` 内，`:327` 前插一条）+ `phrases.go` 新增 `phraseIdentityProfile`；`Definition.Phrase` 指向它。
2. `Models/Conditions/resolve_identity.go`：`loadIdentity` 的 `switch`（`:12-27`）加 case + 一条 SQL（`user_profiles` 三列非空）；`batch.go:150` 的 `load` 分派 `case` 列表要加。
3. `Models/Badges/requirement_text_test.go:55`（`phraseSamples` 补样本）、`:168`（13→14）；
   `Models/Conditions/conditions_postgres_integration_test.go:264`（13→14，`:291` 的「全量−1」自动成立）。
4. `Models/TrustLevels/config.go`：`TierConfig`（`:185`）加 `Rule`；`Code*` 常量（`:67-105`）+ `configErrorReasonCodes`（`:109`）；
   `DecodeConfig`（`:348`）内走 `ruleengine.Decode` + `Validate(subsetCatalog)`；`config_test.go:411-445` 字面量 +5。
5. 新增 `Models/TrustLevels/rule_catalog.go`：`conditions.Core().Definitions()` 过滤 `content.pinned` / `content.hidden` → 12 条；暴露 `Catalog()`（实现 `ruleengine.Catalog`）与 `Allowed(key)`。
6. 新增 `Models/TrustLevels/rule_reference.go`：引用存在性校验。**依赖方向按 D17**：在 `TrustLevels` 声明窄接口（例如 `RefCandidateSource`，方法只取 `context.Context` 与规则树、返回 `conditions.RefCandidateSet`），bootstrap 注入 `*badges.PostgresStore`；**不得** import `Models/Badges`。
7. `Http/Controllers/TrustLevels/controller.go:77-80` 加 2 条静态段（`/admin/trust-levels/conditions`、`/admin/trust-levels/rule-impact`），注册顺序必须在 `/admin/trust-levels` 之前（Fiber 顺序匹配，参照 `Badges/controller.go:120-123` 的教训注释）。
8. `Http/Controllers/TrustLevels/dto.go`：拆 `levelDefinition`（公开）/`adminLevelDefinition`（含 `rule`）；`viewerTrustLevel` 加 `conditions[]`（仅本人、仅下一档、仅扁平 `all`）。
9. 契约：`contracts/openapi/schemas/trust_levels.yaml`（`TrustLevelDefinition` :59、`TrustLevelViewer` :178、新增 2 个 schema、enum :361-393 +5）；
   `contracts/openapi/paths/trust_levels.yaml` 加 2 个 path item。收口跑 `ruby scripts/validate-openapi-refs.rb`。
10. 前端码表：`useTrustLevelSaveErrors.ts:138`（+5 specific）+ `tests/admin/forum/trustLevels.test.ts:549/551/595-597` + 两份 locale。
11. 路由 tripwire 三处同批：`Support/Routes/registry_test.go:534`（433→435）、`docs/extensions/v3/catalog-identities.json` 的 `routes`、`apps/api/app/Support/Routes/core_catalog_gen.go`（用 `node scripts/v3-catalog/generate.mjs` 生成，`--check` 校验）。注意 `ui` 计数（434）只在新组件时动。

### M2：求值链路

1. `Models/TrustLevels/level.go`：`LevelOfWithRules(ctx, activity, accountAge, policy, eval) Level` + 薄包装 `LevelOf`（单一遍历；账龄闸分支里 TL1 的规则同样要 eval）。
2. 新增 `Models/TrustLevels/rule_eval.go`：`RuleEvaluator` 接口 + 适配 `conditions.NewResolver(pool, userID, factKeys...)` → **所有档位树合成一棵 `all` 根 → Prepare 一次** → 逐档 `ruleengine.Eval`（§2-7 / **D19**）。
   **合成根的硬纪律（D19-b）**：只许 `Prepare`，**禁止 `Validate` / `Decode` / `Eval`**。原因：每档单独 ≤32 叶/≤64 节点，但合计会超（8×32=256），而 `Prepare` 根本不查上限、对 256 叶照跑；一旦有人顺手对合成根调 `Validate` 就会报「太复杂」，而每一档其实都合法。保存期校验**仍按单档**，不引入「合计」口径（否则要造第 7 个错误码，且 8 档 × 16 叶就被拒）。
   钉这条纪律的断言不是「合计不越界」，而是性能性质：**8 档各 32 叶时 `Prepare` 成功，且 SQL 次数 = 出现过的 type 数（与档数无关）**。
   **另一个必须写进实现的约束（否则 §3.2「数值不过不查规则」与 §5 预算表同时失效）**：合成根的 children
   只能来自「**数值预筛已通过**且带 rule」的档位——先在内存里跑一遍 `satisfies()` 把候选档位筛出来，
   再合并它们的 rule。若图省事用「所有带规则档位」建根，则数值根本不过的用户也会触发全部条件查询，
   §5 的「有规则、数值未过 → 仍 1 条」立刻变成「+N 条」，且验收矩阵第 1 条变异（数值预筛失效 → 查询计数断言红）
   会失去判据。
3. `Models/TrustLevels/resolver.go:67 Grants`：先 policy（`:77`）；只有存在带规则档位时才构造 evaluator；规则失败 → 该档不成立（fail-closed），**不**把整次解析降级。`incomplete` **不**从 `Grants` 外传（D18）：它只出现在 `TrustLevels` 自己的 `Evaluate()` 返回值里，供 M4 job 与进度读面使用。
4. `bootstrap/trust_levels_wiring.go:84`：注入 evaluator 依赖（pool + factKeys 提供者，**按 D20 用延迟绑定闭包**；未 `Bind` 时 factKeys 为空 → `fact.count` unknown）；`api_assembly_infrastructure.go:242` 是唯一构造点，注入点在 `api_assembly_domains.go:327/479`。
5. `Http/Controllers/TrustLevels/controller.go`：`viewerPayload` 的 `conditions[]`，逐叶 `Definition.Phrase(params, cmp, names)`；`names` 用 `conditions.NewRefNames()`（泛称）即可；要真名再走 `badges.CollectRefs`+`LoadRefNames`，**依赖方向按 D17**（`TrustLevels` 声明窄接口，bootstrap 注入，不 import `Badges`）。

### M4：插件面 / 事件 / 读面

1. manifest：`v3_platform_types.go:217`（`ManifestPermissionDefinition` + `TrustLevelEarnable bool`、`badgeConditions`→加 `userFactConditions`）、
   `Manifest` 结构（`manifest.go:133` 的 `BadgeConditions`，`:124` 的 `PermissionDefinitions`）、`v3_validate_platform.go:648`（校验）+ `v3_include`/normalize 同步、
   `schemas/manifest-v3.schema.json`（`properties` `:172`、`$defs badgeConditions`、`$defs permissionDefinitions` `:2573`，两处都要改，`additionalProperties:false` 会拒）。
2. `Models/TrustLevels/registry.go:29-47`：运行时白名单 = 核心 7 键 ∪ 已启用插件声明；`Earnable.Grants`（`:58`）过滤；解码期不再硬拒（`config.go` 的 `CodeEarnableNotWhitelisted` 语义改为写路径码）。
3. 事件：`Support/Events/catalog.go` 常量区（`:3-46`）+ `definitions`（`:50`）加 `observe(TrustLevelChanged, …)`；SDK `KnownEvent` 自动跟随（`sdk/plugin/catalogs.go:139` → `events.Known`）。
4. job：新增 `app/Jobs/TrustLevels/recompute_user.go`，照 `Jobs/Badges/evaluate_user.go`（`EnqueueOptions()` + `ByArgs: true`）；注册照 `bootstrap/badges_wiring.go:119`，调用点 `bootstrap/worker_forum_jobs.go:119`。job 内求值**按 D18 走 `trustlevels.Evaluate()`**（拿 `Incomplete`），不经过 `TrustLevelSource`。
5. 账本表 + 视图：迁移里 `DROP VIEW IF EXISTS` + `CREATE VIEW`，owner 自对齐（`public.badges` 读 `pg_get_userbyid(relowner)`）。
6. HostQuery 2 条 + 5 处注册 + 2 份密封清单（`properties` 与 `Fields` 等长）。
7. 前端披露弹窗 `components/admin/SFAdminExtensionEnableDialog.vue`（现在渲染 label+key，`:163-180`）。

---

## 4. 裁决结果（D17–D20 已定；原 8 个问题收敛为 3 个残留）

裁决载于计划 §0.2（2026-11-14）。下表是「已裁决 + 一句要义 + 对落点的影响」，实施期按此执行，不再讨论。

| # | 原问题 | 裁决 | 要义与对落点的影响 |
| --- | --- | --- | --- |
| **D17** | `Models/TrustLevels` 能否 import `Models/Badges` | **不许**。在 `TrustLevels` 声明窄接口，由 bootstrap 注入 | 与 `ActivityReader`（`resolver.go:25`）、`Forum.TrustPolicyResolver` 同一套路。真实查询留在 `Badges`（`ref_candidates.go:31`、`ref_names_postgres.go:45`），**不写第二份 SQL**；`badges.PostgresStore` 是 God object，直接依赖会让后续 trust 专用查询往它身上堆方法，撞 AGENTS.md 非增长基线。落点：M1 的 `rule_reference.go` 依赖 `RefCandidateSource` 接口（不是 `*badges.PostgresStore`）；M2 的真名解析同款 `RefNameSource`；注入点都在 bootstrap（`trust_levels_wiring.go`）。 |
| **D18** | `TrustLevelSource` 是否加 `incomplete` | **不加**。接口保持 `Grants(level, earned, err)` | 需要 `incomplete` 的只有 M4 事件 job 与进度读面，它们走 **TrustLevels 自己的** `Evaluate()`（返回 `{Level, TierID, Incomplete, Errors}`），不经过 Identity；扩接口要动两个 loader + 全部 fake，收益为零。落点：M2 的 `Grants` 内「规则失败 → 该档不成立」不外传状态；M4 的 `recompute_user` job 调 `Evaluate()` 而不是 `TrustLevelSource`。注意：这意味着**事件抑制只对 job 路径有效**，HTTP 装载路径永远不发事件（它本来也不发）。 |
| **D19 / D19-b** | `conditions.NewResolver` 是否加参数 / `PrepareAll` | **都不加**。TrustLevels 侧构造合成 `all` 根（children = 所有带规则档位的 rule），`Prepare` 一次，再逐档 `Eval`；**合成根只许 `Prepare`，禁止 `Validate`** | 消除 §2-7 的 N 次重查，补上 §5「≤3 次 Prepare」的真实依据；不动 `Conditions` 公共面，徽章侧零回归。**上限口径（D19-b，纠正本报告早前版本）**：叶/节点/depth 上限**只**由 `Validate` 检查（`validate.go:16-20`），`Prepare` 与 `Eval` 都不查 → 合计叠加**不构成越界风险**，`Prepare` 对 256 叶照跑且 SQL 次数仍 = type 数。因此保存期**保持单档校验**，**不要**新增「合计」口径（那会凭空多一个「scope: aggregate」错误码，并拒掉 8 档 × 16 叶这种每档都合法的配置）。真实风险有两个：① 有人对合成根调 `Validate` → 立刻撞 32/64；② 合成根用「所有带规则档位」而不是「数值预筛通过的档位」构建 → 数值不过的用户也会查规则，§5 的「数值未过仍 1 条」失效。钉法都在 §3-M2 第 2 条（8×32 叶 Prepare 成功 + 查询数 = type 数；以及「先 `satisfies()` 预筛再建根」）。 |
| **D20** | `badgesFactCatalog` 复用形态 | **延迟绑定闭包**（`Bind`，先例 `deferredBackfillEnqueuer.Bind`，`badges_wiring.go:145`） | 保留现有装配顺序（infra 造 resolver、domains 造 catalog，§2-5）；未绑定时 factKeys 为空 → `fact.count` 恒 unknown，与「未声明即无法判定」的既有语义天然一致，是 **fail-closed 而不是 fail-open**。落点：M2 第 4 条（注入 `func() []string`）+ M4 第 4 条（job 走同一条闭包）。**验收要加一条**：未调用 `Bind` 时构造 resolver 不得 panic，且 `fact.count` 叶子必须 unknown（`lte` 不得误判成立）。 |
| **Q5** | `identity.profile` 的 `field` 字面量 | **枚举值 = `website`，列 = `website_url`**（计划 §0.1 小数修正已定） | 判据是三列 `TEXT NOT NULL DEFAULT ''` 的**非空串**，不是 `IS NOT NULL`（§2-10）。OpenAPI 的 enum 只写 `website`。 |

### 4.1 仍然未决（3 条，实施前需要回答）

| # | 问题 | 现状 | 选项与代价 |
| --- | --- | --- | --- |
| Q6 | 5 个新码 ↔ 17 个引擎码的**映射表放哪** | §2-8：不建表，「精确 422」必退化成桶码 | (a) `TrustLevels` 内私有表（徽章零影响，推荐）；(b) 提到 `RuleEngine` 做通用映射（会改徽章既有对外码，回归面大）。**建议 (a)**，但要 Lead 一句确认，因为它决定 `FieldError` 的 `params` 里放 `ruleengine` 的 `Path` 还是自己的。 |
| Q7 | `TierConfig.Rule` 的 Go 类型 | `limits`/`earnable` 都是 `json.RawMessage`（`config.go:198-199`） | (a) `json.RawMessage` + 解码后 `ruleengine.Decode`/`Validate`：与既有两字段一致、错误定位走 `FieldError`，且 `DecodeConfig` 仍是纯函数；(b) `*ruleengine.Node` 直接解：`DecodeConfig` 会拿到半解析结构，`DisallowUnknownFields` 的外层纪律与内层 `Decode` 打架。**建议 (a)**。 |
| Q8 | 计划正文是否**本体**改基线 | 计划 §0.1/§0.2 已加覆盖声明，但第 3 行仍写 `基线 HEAD 6502fb25b`，§0 表里 E1/E2/E4 那几行仍是旧错值（靠 §0.1 覆盖读） | 风险是「先读到错的、没读到 §0.1」。最小改法：第 3 行改成 `基线 6502fb25b；M0 已落地 e4f8c3b1f；实测纠错见 §0.1（冲突以 §0.1 为准）`，并在 §0 每行错值后加 `→ §0.1 Ex`。另：§0.1 的 E4 行写 `dto.go:35`，正确是 `:23`（`:16` 是 `publicPayload.Levels`），见本简报 §0 第 3 条。 |

---

## 5. 未能验证 / 口径限制

1. **性能数字全部未实测**：`P95 增量 ≤ 5ms`、`10k 夹具`、`≤3 次 Prepare` 的实测值本简报没有产出（只读、无库、不跑测试）。§5 的查询条目是**代码路径推导**，不是 PG 侧计数。
2. **`EXPLAIN (ANALYZE, BUFFERS)` 未跑**：需要 `scripts/dev.sh` 起的库与 10k 夹具，属 M2 验收动作。
3. **Go 测试未跑**（含 `./scripts/test-api-isolated.sh`）：本任务只读，门禁绿否未取证。
4. **前端 `bun test` / typecheck 未跑**：同上。
5. **`catalog-identities.json` 的 `ui` 条目与真实组件目录是否一一对应**未全量对拍（只确认 `ui` 长度 434 与 trust 相关 18 条）。
6. **`rule_store_postgres_integration_test.go:457`**：计划 §0 引的「多条件文案已生成」用例我定位到 `:450-466`（`并且` 断言在 `:463`），行号口径为 `:457` 附近，属**未逐字对齐**（同一用例内，不影响结论）。
7. **并行改动已复核**：核验后出现两个提交——`e4f8c3b1f`（M0）与 `322f088e5`（`docs(badges): 修正条件包旧路径引用`，含 `knowledge/modules/badges.md` 与验收脚手架）。2026-11-14 11:52 在 HEAD `322f088e5` 重跑行号敏感项：tripwire `:534`、`useTrustLevelSaveErrors.ts:138/157`、`dto.go:16/23/129`、`batch.go:78/107`、`requirement_text_render.go:28/87` **全部无漂移**。核验结束时工作树仍有未提交改动：`M knowledge/modules/badges.md`（Lead 的 `:706` 修正）与未跟踪的 `.dsh/`、`work-team/`、计划、本简报、pm/ui 两份 spec。**再往后若有提交，§1 的数字需重跑命令确认**（尤其 §1-D19 的入队点与 §1-C14 的路由计数）。
