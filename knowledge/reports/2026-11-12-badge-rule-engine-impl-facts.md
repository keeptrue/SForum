# 徽章规则引擎：M1.4 / M3 落点核实简报

Status: **read-only recon**（只读核实，未改任何生产代码）
基线：`d502f19a3`（`git status` 仅 `Support/RuleEngine/` 未跟踪 + 两份知识库文档）
计划：`knowledge/plans/2026-11-12-badge-rule-engine.md`（10:35 创建，`??` 未跟踪）
核实方式：读码 + 实跑 `go test ./app/Support/Routes/ -run TestCoreRouteCatalogHasExactReviewedGuardParity`（ok, 0.013s）

每条格式固定：**结论 → 证据（路径:行号 + 片段）→ 对 M1.4/M3 的影响**。找不到的直接写「未找到」。

---

## 0. 与计划的三处口径差异（先看这个，会直接影响 M3 的接口签名）

| 计划写法 | 代码事实 | 处理 |
| --- | --- | --- |
| `func (c *Client) RecordBadgeFact(...)`（§8.3） | `apps/api/sdk/plugin/v2` 里没有 `Client` 类型，只有 `Host`（`host.go:40`） | 落成 `func (h *Host) RecordBadgeFact(...)` |
| Host 命令叫 `sforum.badges.fact.record@1`（§8.2） | 引擎以 `{ID, Version}` 双字段为键（`v2_command.go:137-140`），既有 ID 一律不带 `@N`：`notifications.emit` + `"1"`（`sdk/plugin/v2/notifications.go:16-17`） | `ID="badges.fact.record"`, `Version="1"`；`sforum.badges.fact.record@1` 只能出现在 `PolicyId` / schema ref 位置（对照 `v2_command_notifications.go:384`） |
| HostQuery `sforum.core.public_user_badge_facts.list`（§4.4） | 常量层确实用 `sforum.core.*`（`v2_query_runtime.go:33`），但 Query Registry 映射层用 `core.query.*`（`query_registry_core.go:412`） | 两个名字都要登记，且不是同一个字段 |

---

## 1. `Support/Localization/messages.go`：错误码文案登记方式

### 结论

单个 `var messages = map[string]map[string]string`，两个语言键各一张 map：`"zh-CN"`（第 10 行起，到第 420 行 `},`）与 `"en-US"`（第 421 行起，到第 828 行 `}`）。另有第三张**独立** map `trustedRuntimeMessages`（831–846），只放扩展运行期可信码（`extension.frontend_*`），徽章码不进那里。

`Message()` 的查找顺序是 `trustedRuntimeMessages → messages[locale] → messages[DefaultLocale] → key`（848–869），所以「只加 zh、忘加 en」时 `Message("en-US", code)` 会拿到中文而不是 key —— 这正是两条自动发现测试的盲区。

### 证据

`apps/api/app/Support/Localization/messages.go:8-11`
```go
var messages = map[string]map[string]string{
	"zh-CN": {
		"ok":                         "OK",
```

`messages.go:420-425`（两张 map 的接缝）
```go
	},
	"en-US": {
		...
		// Cross-domain fix: the badges workstream (2026-11-10) missed the bilingual
```

`messages.go:57-66`（zh 侧徽章块，含历史事故注脚）
```go
		// 跨域修复：徽章那刀（2026-11-10 badges-and-user-activity）漏了这六个
		// 错误码的双语文案，导致 TestAPIErrorCodesHaveLocalizedMessages 长期红。
		"forum.badge_rule_invalid":    "徽章规则无效：请检查指标与阈值的组合。",
```

`messages.go:429`（en 侧同码）
```go
		"forum.badge_rule_invalid":        "Invalid badge rule: check the metric and threshold combination.",
```

发现机制：`apps/api/app/Support/Localization/locales_test.go:126` `TestAPIErrorCodesHaveLocalizedMessages` 与 `:161` `TestAPIErrorCodesAreLocalizedInEveryLocale`，都调用 `collectAPIErrorCodes`（约 190 行起），后者 AST 扫 `apps/api` 下所有非 `_test.go` 文件里的 `const Code* = "domain.key"`（正则 `apiErrorCodeLiteral`，188 行：`^[a-z][a-z0-9]*(?:\.[a-z0-9_]+)+$`），再补扫 `fiber.NewError/NewError/Abort` 的字面量 reason，并跳过 `Support/Localization` 自身。

### 新增一个 `forum.badge_rule_*` 码的完整改法

1. `apps/api/app/Models/Badges/errors.go` 的既有 `Code*` 块（34–46 行）追加常量，例如 `CodeBadgeRuleUnknownCondition = "forum.badge_rule_unknown_condition"`。**只有 `Code` 前缀 + `domain.key` 形态才会被 AST 扫到**，写成 `ErrBadgeRule...` 或纯字符串字面量不会被发现，测试也就不会提醒你漏文案。
2. `messages.go` 的 `"zh-CN"` map（10–420）里加一条，建议紧挨 57–73 的徽章块；再在 `"en-US"` map（421–828）里加同码，建议紧挨 425–435。
3. 两处文案必须**不同**：`badge_tier_messages_test.go:37` 的既有断言 `zh-CN` 与 `en-US` 相同即 `t.Fatalf`，新增显式清单测试时照抄这个模式（`TestBadgeTierErrorCodesAreLocalized`，8–50 行）能额外钉住「码还在」——自动发现只能证明「没漏」。
4. 不需要动 `trustedRuntimeMessages`。

### 对 M1.4 / M3 的影响

M1.4 每新增一个对外错误码（`too_complex` / `unknown_condition` / `condition_unavailable` 之类），必须同批三处：`Models/Badges/errors.go` 常量 + zh + en。漏 en 不会被 `TestAPIErrorCodesHaveLocalizedMessages` 抓到（有 zh 回退），只会被 `...InEveryLocale` 抓到 —— 两条都要跑。

---

## 2. `Support/Routes/registry_test.go`：路由计数

### 结论

确切数字是 **418**，断言在 `TestCoreRouteCatalogHasExactReviewedGuardParity` 里，写法是**硬编码等值 + 一段滚动更新账注释**。

### 证据

`apps/api/app/Support/Routes/registry_test.go:485-486`
```go
func TestCoreRouteCatalogHasExactReviewedGuardParity(t *testing.T) {
	catalog := CoreRouteCatalog()
```

`registry_test.go:514-516`
```go
	if len(catalog) != 418 {
		t.Fatalf("generated core route count = %d", len(catalog))
	}
```

`registry_test.go:487-489`（注释块开头，487–513 是逐条增量账）
```go
	// 计数由 scripts/v3-catalog/generate.mjs 与 catalog-identities 共同约束；
	// 新增路由必须先进入 identities + OpenAPI + Guard 审核矩阵，再同步此处。
	// 363 = 362 + core.route.extensions.inspect_route_incidents（M5 隔离账本读取）。
```

`registry_test.go:507-512`（最近一次 +8 的记账方式）
```go
	// 418 = 410 + 八条徽章等级表路由（202611120001 badge_tiers）：
	//   公开 1 条（GET /badges/tiers，actor 无关、可共享缓存）
	//   后台 5 条等级 CRUD/排序（读 badge.view、写 badge.manage）
```

另一个相关断言只做下界，不会因新增而红：`registry_test.go:373`
```go
	if len(core) < 200 || len(snapshot.Routes) != len(core) || len(snapshot.Conflicts) != 0 {
```

418 不是手写的：`CoreRouteCatalog()` 在 `apps/api/app/Support/Routes/core_catalog_gen.go:427`，由 `scripts/v3-catalog/generate.mjs` 生成，身份源是 `docs/extensions/v3/catalog-identities.json`，门禁是 `tests/validate-v3-p0-catalogs.mjs`。

### 对 M1.4 / M3 的影响

新增 N 条徽章规则路由 → 数字变 `418+N`，且必须在注释块末尾补一行同格式的增量账（谁 + 为什么）。顺序是：`contracts/openapi/paths/badges.yaml` → `docs/extensions/v3/catalog-identities.json` → `node scripts/v3-catalog/generate.mjs` 重生成 `core_catalog_gen.go` → 改 514 行数字。只改数字不重生成，`tests/validate-v3-p0-catalogs.mjs` 会红；只重生成不改数字，这条会红。

---

## 3. `Support/HostAPI`：注册一条新 HostQuery 的完整落点

### 结论

一条稳定 Core 查询要同时落 **5 处代码 + 3 处测试清单**，少一处就有独立红灯。

### 证据（以徽章授予关系查询为范例）

**① 常量**：`apps/api/app/Support/HostAPI/v2_query_runtime.go:33` 与 `:51`
```go
	QueryPublicUserBadgesList = "sforum.core.public_user_badges.list"
	QueryPublicUserBadgeSchemaID     = "sforum.core.public_user_badge"
```

**② 查询定义**：`apps/api/app/Support/HostAPI/v2_query_postgres.go:393` `func stableCoreProtocolV2QueryDefinitions()`；徽章条目在 `667-700`
```go
		{
			ID: QueryPublicUserBadgesList, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicUserBadgeSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.forum_user_badges AS stable",
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"},
```
```go
			// 授予关系按用户读：没有 user_id 就是全站枚举，必须拒绝。
			RequiredFilters: []string{"user_id"},
```

**③ Query Registry 映射**：`apps/api/app/Support/HostAPI/query_registry_core.go:318` `func queryRegistryCoreMappingFor`，徽章 case 在 `409-416`
```go
	case QueryPublicUserBadgesList:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicUserBadgesList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_user_badges.list", ContractVersion: "core.query.public_user_badges.list@1",
			Entity: "core.query.public_user_badge", PlanVersion: "core.query.public_user_badges.list.plan@1",
			ResultSchema:     QueryPublicUserBadgeSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		}
```

**④ 结果 schema**：`apps/api/app/Support/HostAPI/query_registry_core_schema.go:172` `case QueryPublicUserBadgeSchemaID`；`properties` 与 `Fields` **必须等长**，否则构造期报错（同文件 `226` 行 `if len(definition.Fields) != len(properties)`）。

**⑤ 稳定读测试**：`apps/api/app/Support/HostAPI/query_registry_core_badges_test.go`
- `:16 TestBadgeQueriesAreStablePublicReads`（ID 稳定 / schema 对齐 / `From` 必须以 `sforum_core_v1.` 开头 / mapping 是 public + offset）
- `:76 TestBadgeQuerySchemasExcludeAuditAndUncollectedFields`（禁字段清单 `granted_by, grant_reason, posts_read_count, read_time_seconds, email, email_lower`）
- `:95 TestBadgeUserQueriesRequireUserFilter`（按用户读必须 `RequiredFilters == ["user_id"]`）

**⑥ 两份硬编码密封清单**（各自独立红，注释明说「加查询必须同批改」）
- `apps/api/app/Http/Controllers/Extensions/query_catalog_test.go:69 var sealedQueryCatalogIDs`；注释在 `61-66`：「构成：15 条 stable + 7 条 shortcode = 22 …… **加查询必须同批改这里**，否则本测试红。」
- `apps/api/bootstrap/sealed_query_catalog_test.go:22 var sealedCoreQueryCatalogIDs`

**denied 范例**：公开策略的查询没有「拒绝」路径，本族的拒绝断言就是上面的 `RequiredFilters`；如果 M3 的查询要 login 策略，照抄 `apps/api/app/Support/HostAPI/v2_query_inventory_test.go:51-59`（构造无授权 runtime，断言 `ERROR_CODE_PERMISSION_DENIED`）。

### 对 M1.4 / M3 的影响

M3 的 `sforum.core.public_user_badge_facts.list` 按上面 5 处 + 2 份清单落，共 7 个文件。`From` 指向计划 §4.4 的新视图 `sforum_core_v1.forum_user_badge_facts`，`Fields` 与 schema `properties` 严格等长（计划列的 6 列：`user_id/username/display_name/fact_key/variant/total`），禁字段清单里要加上 `delta`、`dedupe_key`、`source_extension_id`。Catalog 侧测试（`query_registry_core_test.go:12/541`）全是相对断言，不会因多一条而红。

---

## 4. `ExtensionManifest` V3 新增顶层数组 `badgeConditions`

### 结论

**6 个生产文件 + 1 份生成文档**，另有 2 条 docsgen 测试会直接红。

### 证据与改点

| # | 文件:行 | 现状片段 | 要做什么 |
| --- | --- | --- | --- |
| 1 | `apps/api/app/Support/ExtensionManifest/manifest.go:132` | `AITools []ManifestAITool \`json:"aiTools,omitempty"\`` | 同位置加 `BadgeConditions []ManifestBadgeCondition \`json:"badgeConditions,omitempty"\``；类型定义照 `v3_ai_tools.go` 另开 `v3_badge_conditions.go` |
| 2 | `.../schemas/manifest-v3.schema.json` | 根 `additionalProperties: false`，52 个 properties，`$defs` 60 项；`aiTools` 写法是 `{"$ref": "#/$defs/aiTools"}` | 加 `properties.badgeConditions` → `$ref` + `$defs.badgeConditions`。**不加 schema，`ValidateV3JSONSchema` 会因 additionalProperties=false 直接拒包** |
| 3 | `.../json_schema.go:92` | `fragments := []string{"", "guards", …, "aiTools", …}` | 加 `"badgeConditions"`，否则 `validateV3JSONSchemaFragment(body, "badgeConditions")` 报 `unknown manifest schema fragment` |
| 4 | `.../v3_validate_platform.go:36-76` `validatePlatform` | 链尾 `if err := v.validateAITools(); err != nil`（73） | 加 `v.validateBadgeConditions()`；实现照 `validateAITools`（`81` 起） |
| 5 | `.../v3_normalize.go:32` | `for index := range manifest.AITools {` | 加 `badgeConditions` 归一化循环（trim key/contractVersion/valueKind，compare 小写） |
| 6 | `.../v3_validate.go:366-367` `hasV3Declarations` | `len(manifest.Guards)+…+len(manifest.PackageFiles) > 0` | 把 `len(manifest.BadgeConditions)` 加进求和。**当前没有测试钉这一项，漏了不会红，但只声明 badgeConditions 的清单就不会被当作 V3 声明** |
| 7 | `docs/extensions/catalogs/manifest-v3.md` | 生成文件，头部有 `Generated by` 标记 | `cd apps/api && go run ./cmd/sforum extension docs generate` 重生成 |

### 会红的既有测试

1. `apps/api/sdk/plugin/docsgen_test.go:93-101`（在 `TestGenerateCatalogDocs` 内）：
```go
	fields, _, _, _ := manifestV3SchemaFields()
	...
	for _, field := range fields {
		if !strings.Contains(docs.Files[DocManifestV3], "`"+field+"`") {
			t.Fatalf("manifest-v3.md should list schema field %s", field)
```
→ schema 里一加 `badgeConditions`，文档没重生成就红。

2. `apps/api/sdk/plugin/docsgen_test.go:133 TestCommittedCatalogDocsInSync`：比对仓库内 `docs/extensions/catalogs/` 与现场生成结果，同一个原因红，且报错会直接给重生成命令。

3. `TestManifestV3JSONSchemaRejectsUnknownFields`（`json_schema_test.go:25`）**不会红**（根 additionalProperties 本来就是 false）；`TestManifestV3JSONSchemaValidatesCanonicalContract`（`:10`）也不会红（新数组可选）。**没有任何 reflect 式的「Go struct 字段 ↔ schema properties」对齐测试** —— 这是本项最大的坑：只改 Go struct 不改 schema，单测可能全绿，运行时装载才炸。

### 回归夹具建议

首选 `extensions/fixtures/plugins/sforum-query-surface-reference`：
- 有静态 `sforum.extension.json`，`scripts/ci/reference-packages-gate.sh:53` 只遍历带该文件（非 `.tmpl`）的目录，所以它会真的过 build → digest → validate → test；
- `manifestVersion: 3`，已声明 `backend`（protocol 2）+ `capabilities: ["host.api"]` + `database` + `openapi` + `routes` + 带真实 digest 的 `packageFiles`。

次选 `sforum-consented-claim-reference`：同为静态 V3 清单，且已声明非默认能力 `users.email.read`，适合把 `badges.fact.write` 与 `badgeConditions` 放在同一个包里一起验披露。

**不要用** `sforum-ai-tool-reference`：它只有 `sforum.extension.json.tmpl`，`reference-packages-gate.sh:53` 的 `[ -f "$source/sforum.extension.json" ] || continue` 会直接跳过，等于没进 CI。

### 对 M1.4 / M3 的影响

M3 的 manifest 契约（§8.1）落点就是上表 7 项；M1.4 只要读 `badgeConditions`（用于条件目录合成与 `unknown_condition` 判定）就不需要再动 schema，但**必须**确认 `hasV3Declarations` 已覆盖，否则纯条件包的行为会飘。

---

## 5. `sdk/plugin/v2/`：Host 命令封装写法与 `RecordBadgeFact` 接口

### 结论

现成模板是 notifications 族，不是 content（content 是 provider slot 调用，不是 Host 命令）。Host 侧「常量 + `protocolV2CommandDefinition` + 进密封目录」；SDK 侧「常量 + 输入结构 + `XxxRequest` 构造 + `Xxx` 执行」。

### 证据

Host 侧常量：`apps/api/app/Support/HostAPI/v2_command_notifications.go:29-32`
```go
	CommandNotificationsEmitID            = "notifications.emit"
	CommandNotificationsEmitInputSchema   = "sforum.notifications.emit.input"
	CommandNotificationsEmitOutputSchema  = "sforum.notifications.emit.result"
```

Host 侧定义：同文件 `:70` 起，带能力门的是 events 那条 —— `v2_command_events.go:52-58`
```go
	definition := protocolV2CommandDefinition{
		ID: CommandEventsEmitID, Version: CommandEventsEmitVersion,
		InputSchemaID: CommandEventsEmitInputSchema, InputSchemaVersion: CommandEventsEmitSchemaVersion,
		OutputSchemaID: CommandEventsEmitOutputSchema, OutputSchemaVersion: CommandEventsEmitSchemaVersion,
		ActorMode:          protocolV2CommandActorService,
		RequiredCapability: capabilities.EventsEmit,
	}
```

密封目录登记点：`apps/api/app/Support/HostAPI/v2_command_runtime_postgres.go:50-65`
```go
	definitions := []protocolV2CommandDefinition{
		identityCommand,
		...
		newProtocolV2NotificationEmitCommandDefinition(config.Pool, config.Jobs),
		newProtocolV2EventsEmitCommandDefinition(config.Events),
```
（引擎不可变，构造期一次性收口，`v2_command.go:182-247`；重复 ID+Version 直接构造失败。）

SDK 侧：`apps/api/sdk/plugin/v2/notifications.go:47-117`
```go
func (h *Host) NotificationEmitRequest(parent *protocolwire.RequestContext, input NotificationEmitInput) (*hostwire.CommandRequest, error) {
	...
	return &hostwire.CommandRequest{
		Context: requestContext, CommandId: HostNotificationEmitCommandID,
		CommandVersion: HostNotificationEmitCommandVersion,
		IdempotencyKey: input.IdempotencyKey, Input: document,
	}, nil
}

func (h *Host) EmitNotification(ctx context.Context, parent *protocolwire.RequestContext, input NotificationEmitInput) (*hostwire.CommandResult, error) {
	request, err := h.NotificationEmitRequest(parent, input)
	if err != nil {
		return nil, err
	}
	return h.Commands.Execute(ctx, request)
}
```
同款：`events.go:99-106`。SDK 的分层纪律写在 `events.go:17-19`：「SDK 只依赖契约，不 import Host 实现；边界值在这里重复声明一次，Host 侧另有等价常量，两侧由各自测试钉住。」

### 对 M3 的接口定稿

新建 `apps/api/sdk/plugin/v2/badges.go`：
```go
const (
	HostBadgeFactRecordCommandID       = "badges.fact.record"
	HostBadgeFactRecordCommandVersion  = "1"
	HostBadgeFactRecordInputSchemaRef  = "sforum.badges.fact.record.input@1"
	HostBadgeFactRecordResultSchemaRef = "sforum.badges.fact.record.result@1"
)

type BadgeFactInput struct {
	FactKey    string
	Variant    string
	Delta      int64
	OccurredAt string // RFC3339；空表示由 Host 取 now()
	DedupeKey  string
}

func (h *Host) RecordBadgeFactRequest(parent *protocolwire.RequestContext, input BadgeFactInput) (*hostwire.CommandRequest, error)
func (h *Host) RecordBadgeFact(ctx context.Context, parent *protocolwire.RequestContext, input BadgeFactInput) (*hostwire.CommandResult, error)
```
要点：`delta` 在 protobuf Struct 里是 double，`int64` 必须编码成十进制字符串（`events.go:107-113` 的 `EventEmitResultValues` 注释就是这条约束）；`inserted:false` 走正常 `CommandResult` + `Output`，拒绝走 `ErrorDetail.reason`（`v2_command_contract.go:23-45`）。

---

## 6. `capabilities`：声明、校验、信任披露

### 结论

`host.api` / `settings.own` / `audit.append` 的**声明**在 `Support/Capabilities/catalog.go`（常量 15–35 + 目录条目 69–161）；**清单校验**在 `ExtensionManifest/manifest.go:485-493`；**运行时校验**分两条路：Host API v1 走 `HostAPI/v1.go` 的按方法 `caps.Require`，Protocol v2 命令走定义上的 `RequiredCapability` 字段。新增 `badges.fact.write` 应落在后者，不需要动 v1 的 switch。

### 证据

`apps/api/app/Support/Capabilities/catalog.go:18/25/26`
```go
	SettingsOwn      = "settings.own"
	AuditAppend    = "audit.append"
	HostAPI        = "host.api"
```

目录条目（披露文案的唯一来源）：`catalog.go:70-76`（HostAPI）、`77-83`（SettingsOwn）、`98-104`（AuditAppend）
```go
	{
		Key:         AuditAppend,
		Risk:        RiskMedium,
		LabelZH:     "追加审计记录",
		LabelEN:     "Append audit events",
		Description: "Append structured audit events under this extension's namespace.",
	},
```

清单校验：`apps/api/app/Support/ExtensionManifest/manifest.go:485-493`
```go
	// F2.1：capabilities 必须落在宿主目录内；主题禁止声明。
	if len(manifest.Capabilities) > 0 {
		if manifest.Type == TypeTheme {
			return ErrInvalidManifest
		}
		if err := capabilities.ValidateKeys(manifest.Capabilities); err != nil {
```
（`ValidateKeys` 本体在 `catalog.go:192`，未知 key → `ErrUnknown`。）

宿主推断：`apps/api/app/Support/Capabilities/resolve.go:36-52`（`HasBackend→HostAPI`、`HasSettings→SettingsOwn`、`HasJobs→JobsEnqueue`、`mail.provider→NetOutbound`）。

运行时 —— Host API v1：`apps/api/app/Support/HostAPI/v1.go:218/228/238`
```go
	case MethodPing:
		if err := caps.Require(capabilities.HostAPI); err != nil {
	case MethodGetSettings:
		if err := caps.Require(capabilities.SettingsOwn); err != nil {
	case MethodAppendAudit:
		if err := caps.Require(capabilities.AuditAppend); err != nil {
```
运行时 —— Protocol v2 命令：`apps/api/app/Support/HostAPI/v2_command.go:125`
```go
	// RequiredCapability 是可选的进程能力（非人类 RBAC）。空表示不额外要求。
	RequiredCapability string
```
既有用例：`v2_command_events.go:56`（`capabilities.EventsEmit`）、`v2_command_ai.go:102`（`AIInvoke`）、`v2_command_extensions_manage.go:69/272/383/575`（`ExtensionsManage`）。

信任披露文案的落点：
- 后端字段：`catalog.go:51-57` 的 `Definition.LabelZH/LabelEN/Description`（JSON `labelZh/labelEn/description`）。
- 前端渲染：`apps/web/app/components/admin/SFAdminExtensionEnableDialog.vue:163-183`，只渲染 `labelZh|labelEn` + 风险徽章 + `grant.key`：
```vue
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium">{{ locale.startsWith('zh') ? grant.labelZh : grant.labelEn }}</span>
```
  类型：`apps/web/app/utils/admin/adminExtensions.ts:374-375`（`labelZh` / `labelEn`）、`:401`（`capabilityGrants?`）。
- 生成文档：`docs/extensions/catalogs/capabilities.md`，由 `apps/api/sdk/plugin/docsgen.go:196-210` `renderCapabilities()` 逐行输出 `Key | Risk | Label(EN) | Label(ZH) | Description`。

**注意**：启用弹窗**不渲染 `description`**。运营在确认框里实际读到的「披露文案」就是 label 那一行 —— 所以 `badges.fact.write` 的中英文 label 必须自解释（例如「记录徽章事实」/「Record badge facts」），把「会写用户维度计数」的信息放进 label 而不是只放 description。

### 新增 `badges.fact.write` 要改哪里

1. `catalog.go:15-35` 加常量 `BadgeFactWrite = "badges.fact.write"`。
2. `catalog.go` 的 `catalog` 切片加 `Definition`（建议 `RiskMedium`：写用户维度事实，不改授权、不出站；对照 `audit.append` 同为 medium）。
3. `apps/api/app/Support/HostAPI/v2_command_badges.go`（新文件）的 `protocolV2CommandDefinition` 上加 `RequiredCapability: capabilities.BadgeFactWrite`。
4. 重生成 `docs/extensions/catalogs/capabilities.md`（否则 `docsgen_test.go:133 TestCommittedCatalogDocsInSync` 红）。
5. 计划 §8.2 要求「未声明 → 拒绝并进审计」：引擎在 `RequiredCapability` 未满足时返回拒绝（`v2_command_authority_gate_test.go` 覆盖同族），审计由命令后端 `AppendAudit` 在事务内写（`v2_command.go:59`），无需额外接线。

**不会红但要注意**：`Capabilities/catalog_test.go:8` 只做下界与已知性断言，加能力不会红；`ExtensionManifest/capabilities_test.go` 也不会红。也就是说「加了常量忘了加目录条目」会静默变成未知 key 被 `ValidateKeys` 拒 —— 报错发生在插件安装，不是测试。

---

## 7. `scripts/verify-migration-reversible.sh`：可逆判定机制

### 结论

它不「静态分析 SQL」，而是**在一次性 PostgreSQL 里物理跑一遍**：完整 up → 单步 down（只允许回滚最新那条）→ 断言目标对象全部消失 → 再 up → 断言对象全部重建。任何阶段失败有独立退出码。

### 证据

判定流程（脚本头 4-15 行即声明退出码契约）
```
#   0  全部通过
#   3  目标迁移文件不存在（task-1 还没落地）
#   5  单文件 down 失败（goose 或版本前置条件）
#   6  down 之后的对象断言失败（有对象残留）
#   8  up 之后的对象断言失败（对象没重建全）
```

目标解析：`scripts/verify-migration-reversible.sh:26` `TARGET_VERSION="${SFORUM_MIGRATION_TARGET_VERSION:-202611100002}"`，按版本号 glob `database/migrations/${TARGET_VERSION}_*.sql`；命中 0 或 >1 个 → exit 3（`36-45`）。

down 怎么跑：现场生成一个最小 Go 程序（`~215-268`），用 `migrations.Files()` 建 goose provider，先校验「当前最新已应用版本 == 目标」，再 `provider.Down(ctx)`：
```go
		// 只回滚「最新已应用」的那一条：目标不是最新时不能假装验证过。
		if current != target {
			fail("latest applied migration is %d, expected %d; the verified migration must be the newest applied one", current, target)
```
失败 → exit 5。

什么算失败：down 之后按版本分支断言对象消失，任一不符即累计 `down_failures` → exit 6：
```go
if [[ "$down_failures" -ne 0 ]]; then
  die 6 "down 之后有 $down_failures 项残留，迁移不可逆（Down 段漏删）。"
```
断言样例（`202611120001` 分支，`281-292`）：
```bash
  assert_value '表 badge_tiers 已删除' "SELECT to_regclass('public.badge_tiers') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '旧 CHECK badges_tier_check 已恢复' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_tier_check' AND contype = 'c')" t || down_failures=$((down_failures + 1))
```
再 up：`go run ./cmd/migrate` → exit 7；up 断言失败 → exit 8。开发库防护：命中 `public.sforum_dev_guard` 直接 exit 2（`~180-184`）。

### 对 M1.4 的直接影响（`202611120002_badge_rules.sql`）

1. **必须显式传目标版本**：默认还是 `202611100002`；要跑
   `SFORUM_MIGRATION_TARGET_VERSION=202611120002 SFORUM_MIGRATION_PREVIOUS_VERSION=202611120001 bash scripts/verify-migration-reversible.sh`
2. **必须新增一个 `elif [[ "$TARGET_VERSION" == "202611120002" ]]` 分支**，down 断言块（~281 起）和 up 断言块（~330 起）各一个。不加分支会落进 `else` 的旧徽章分支，那里断言「表 badges 已删除」—— 新迁移不会删旧表，脚本直接 exit 6，报的是假失败。
3. down 断言至少要覆盖：`badge_facts`、`user_fact_counters`、`badge_rules_rollback` 三表 + 两个索引（`badge_facts_dedupe_idx` / `badge_facts_window_idx`）+ 新视图 `sforum_core_v1.forum_user_badge_facts` 消失 + `forum_badges` 视图回到不含 `rule`/`requirement_summary_zh`/`requirement_summary_en` 的形态 + `goose max(version_id)` 回退到 `202611120001`。
4. 脚本只验证「最新已应用那条」。若 002 之后又落了别的迁移，002 就再也无法用这个脚本验证 —— 顺序敏感，M1.4 应在 002 合入后立刻跑。
5. `EXPECTED_BADGES=12` / `EXPECTED_GROUPS=3`（`48-49`）是共享默认；若新迁移动了内置徽章或分组数量，两个分支的计数断言都要跟着改。

---

## 8. 未找到 / 需计划方确认

- **未找到**「Go struct 字段 ↔ manifest-v3.schema.json properties」的自动对齐测试（无 reflect 式断言，`rg "reflect"` 在 `ExtensionManifest/*_test.go` 无命中）。第 4 项的 schema/struct 配对只能靠人。
- **未找到** `sforum.badges.fact.record@1` 作为命令 ID 的既有先例（既有 ID 全是不带 `@N` 的 `notifications.emit` / `sforum.events.emit` / `sforum.ai.complete` 形态）。见 §0。
- **未找到** `docs generate --check` 在 `scripts/test.sh` / `scripts/ci/*` 里的独立调用；防漂移靠 Go 测试 `TestCommittedCatalogDocsInSync` 而不是 CI 脚本步骤。
- 计划 §8.2 说「未声明 `badges.fact.write` → 拒绝并进审计」，审计归属需要确认：`RequiredCapability` 未满足是在 `plan`/`execute` 早期拒绝（`v2_command.go:268` `definition()`），此时是否已有事务承载 `AppendAudit`，需 M3 实现时用一条集成测试钉住（既有对照：`v2_command_authority_gate_test.go`）。
