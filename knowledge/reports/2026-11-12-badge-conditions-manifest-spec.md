# `badgeConditions` Manifest V3 契约规范（M3 前置）

Status: **FROZEN 2026-11-12**（M3 开工照此实现；改动须回到 lead 重新裁决）
基线：`d502f19a3` + 工作区未跟踪的 `Support/RuleEngine/`、`Models/Badges/conditions/`、`database/migrations/202611120002_badge_rules.sql`
上游：计划 §8.1（已按本文件重写）/ §8.2 / §8.3、§5.2.2 六条口径裁决
下游：`task-3` rev 5
本版两处结构性简化：**去掉 `valueKind`（插件条件只有 count 语义）**、**夹具改为 count-only**；另新增 §5（resolver 判据 + 目录暴露规则）。裁决清单见 §7.1。

## Errata（2026-11-12，勘误不改正文）

冻结后核实到两处与正文不符。**正文保持冻结原样，以本节为准。**

1. **§7.2 #1「`fact.count` 的 resolver 未实现」不准确。** resolver 已实现：`apps/api/app/Models/Badges/conditions/resolve_facts.go` 的 `loadFacts` 读 `user_fact_counters`（累计）与 `badge_facts` 的 delta 求和（窗口），两侧 `GREATEST(..., 0)`。**真正缺的是「声明校验」**——`loadFacts` 里没有「该 `factKey` 是否被已启用扩展声明」的判据，查不到行一律 `countValue(0)`，而文件头注释（`:10-17`）承诺的是「未声明 → 返 error」。§5.1 的判据不变，仍是 M3 的硬要求。
2. **§5.1 的 fail-closed 由 resolver 构造参数承担，不是求值期查目录。** 正在落地的改法（`task-6` ③）：`conditions.NewResolver` 增加「已声明 factKey 集合」参数（签名待定，倾向 `NewResolver(pool, userID, declaredFactKeys []string)`），求值时 `factKey ∉ 集合 → 返回 error`，引擎转 unknown。**M1 传空集**，于是任何 `fact.count` 叶子即 unknown —— 这是正确行为（M1 没有插件条件）；M3 只需把集合填成「已启用扩展声明的 factKey」，resolver 与注释就一致了。
3. **不冲突澄清**：§5.2 的「`fact.count` 恒在 `ruleengine` 白名单」与 `task-4` 硬约束的「`fact.count` 不进 Core 目录」说的是两个面——前者是 `Catalog` / `ConditionMeta`（保存期校验必须认它，否则 M3 存下的规则通不过校验），后者是 `GET /admin/badges/conditions` 的返回集合（`PublishableDefinitions` 无候选时过滤掉）。§5.2 的两面表已写明这一点，无需改动。

---

## 0. 先纠一条设计定位（会改掉 §8.1 的隐含模型）

**`badgeConditions[]` 不是「新增条件类型」，而是给 Core 已有的 `fact.count` 条件注册合法的 `factKey` 与 `variant` 候选。**

M1 已经落地了这个桥：

`apps/api/app/Models/Badges/conditions/registry.go:35-37`
```go
	// KeyFactCount 是插件事实的 Core 侧读面（M1 只有这一个事实条件；M3 由扩展
	// 声明各自的 variant）。累计读 user_fact_counters，窗口读 badge_facts。
	KeyFactCount = "fact.count"
```

`registry.go:293-305`
```go
		{
			Key:       KeyFactCount,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "factKey", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "fact"},
				{Name: "variant", Kind: ruleengine.ParamEnum, Required: false, Options: nil},
				windowParam(),
			}},
```

**推论（M3 必须照此实现，不要发明 `type: "<plugin key>"`）**：

1. 规则里引用插件事实的叶子**恒为** `{"type":"fact.count","params":{"factKey":"<扩展 id>.<名字>","variant":"…","window":{…}},"cmp":{…}}`。
2. `badgeConditions[].key` 是 `factKey` 的合法取值，进的是 **`RefKind:"fact"` 的候选目录**，不是 `ruleengine.Catalog`。
3. 因此**不需要**为每个插件条件生成 `ConditionMeta`，也不会让 `ruleengine` 的条件白名单随插件数量膨胀 —— 白名单仍只有 13 条（12 内置 + `fact.count`）。
4. 计划 §8.1 的示例 `"key": "acme.onboarding.tasks"` 与 `"contractVersion": "acme.onboarding.condition.tasks@1"` 只有前者能当 `factKey`；`contractVersion` 不参与规则 JSON，只用于版本化与审计。

**为什么这样更好**：`rule_health` 的失效引用检查已经按 `Params` 里的 `RefKind` 设计（`registry.go:72-74`「`rule_health` 的失效引用检查（Params 里的 objectRef RefKind）」），事实条件天然接进同一套；若每个插件 key 都变成独立条件类型，`rule_health` 得再造一套「条件类型是否存在」的检查。

---

## 1. JSON Schema 片段

### 1.1 根 `properties` 新增（一个键）

```json
"badgeConditions": { "$ref": "#/$defs/badgeConditions" }
```

### 1.2 `$defs` 新增（成员片段，粘进 `$defs` 对象里；不是独立文档）

```json
"badgeConditions": {
  "type": ["array", "null"],
  "maxItems": 32,
  "items": {
    "type": "object",
    "additionalProperties": false,
    "required": ["key", "contractVersion", "label", "compare"],
    "properties": {
      "key": {
        "type": "string",
        "minLength": 2,
        "maxLength": 121,
        "pattern": "^[a-z0-9][a-z0-9._-]{1,120}$"
      },
      "contractVersion": { "$ref": "#/$defs/contract" },
      "label": {
        "type": "object",
        "minProperties": 2,
        "maxProperties": 8,
        "required": ["zh-CN", "en-US"],
        "additionalProperties": { "$ref": "#/$defs/nonEmpty" }
      },
      "unit": {
        "type": "object",
        "minProperties": 2,
        "maxProperties": 8,
        "required": ["zh-CN", "en-US"],
        "additionalProperties": { "$ref": "#/$defs/nonEmpty" }
      },
      "compare": {
        "type": "array",
        "minItems": 1,
        "maxItems": 5,
        "uniqueItems": true,
        "items": { "enum": ["gte", "gt", "lte", "lt", "eq"] }
      },
      "variants": {
        "type": ["array", "null"],
        "maxItems": 16,
        "items": {
          "type": "object",
          "additionalProperties": false,
          "required": ["value", "label"],
          "properties": {
            "value": {
              "type": "string",
              "minLength": 1,
              "maxLength": 64,
              "pattern": "^[a-z0-9][a-z0-9._-]{0,63}$"
            },
            "label": {
              "type": "object",
              "minProperties": 2,
              "maxProperties": 8,
              "required": ["zh-CN", "en-US"],
              "additionalProperties": { "$ref": "#/$defs/nonEmpty" }
            }
          }
        }
      }
    }
  }
}
```

**没有 `valueKind` 字段，这是刻意的**（2026-11-12 裁决）：插件条件只有 **count 语义**，事实表本身就是 delta 累加。想要「是否发生过」就用 `compare: ["gte"]` + 规则里 `cmp: {op:"gte", value:1}` 表达，不要为布尔形态再造一条代码路径 —— 多一个 `valueKind` 就多一个 struct↔schema 失配点（本仓没有 reflect 对齐测试，见 §3.2），还多一个 `not` 语义坑。

### 1.3 字段表：必填 / 可选 / 上限 / 取值来源

| 字段 | 必填 | 类型与约束 | 上限/取值 | 依据 |
| --- | --- | --- | --- | --- |
| `key` | 是 | `^[a-z0-9][a-z0-9._-]{1,120}$`（2–121 字符） | 必须 `<manifest.id>.` 前缀 | **必须逐字等于 DB CHECK**：`badge_facts.fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$'`（`202611120002_badge_rules.sql:71`）。manifest 放行的 key 若被 DB 拒，是「保存成功、上报失败」 |
| `contractVersion` | 是 | `^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`（≤256） | **强制等于 `key@N`**（已裁决） | `$defs/contract` = `v3_validate.go:15` 的 `contractVersionPattern`；长度上限 `manifest.go:51 ContractVersionMaximumLength = 256` |
| `label` | 是 | 对象，必须含 `zh-CN` + `en-US`，值非空 | ≤8 个 locale | 与 `descriptionI18n` 的 `maxProperties: 8` 对齐；双语是硬要求，因为条件目录是服务端下发给 M2 编辑器渲染的 |
| `unit` | 否 | 同上 | ≤8 | 对应 `conditions.Definition.UnitZh/UnitEn`（`registry.go:88-89`） |
| `compare` | **是**（至少 1 项） | 数组，`uniqueItems` | ⊆ `gte,gt,lte,lt,eq`，≤5 | `CompareOp` 全集（`types.go:56-61`）；**没有 `ne`**，注释说明「需要未达成请用 `not` 包住 `gte`」。空 `compare` 的条件永远不可用（规则里任何 `cmp` 都会被拒），所以 schema 直接要求非空 |
| `variants` | 否 | 数组，`value` 唯一、非空 | ≤16 | `variant TEXT NOT NULL DEFAULT ''`（迁移 `:72`）无 CHECK，所以 manifest 必须自己收口；`''` 表示「无 variant」，故 `variants[].value` 不得为空串 |
| `badgeConditions[]` 整体 | 否 | 数组 | ≤32 | 与 `ruleengine.MaxLeaves = 32` 对齐（`validate.go:17`）：一条规则的叶子数上限就是事实条件数的自然上界 |

**`compare` 上限为什么是 5 而不是 2**：内置条件用 `countCompare()` 只给 `gte/lte`（`registry.go:172-176`），但那是内置条件的**产品选择**，不是引擎限制。插件条件允许全集，否则「恰好 5 次」这类需求无法表达。若之后要统一收窄成 `gte/lte`，改 schema 的 enum 一处即可。

**布尔语义的等价写法**（供 M3 文档与 M2 编辑器提示使用）：条件 `compare: ["gte"]` + 规则 `cmp:{op:"gte", value:1}` 等价于「这件事至少发生过一次」。不要为此新增 `valueKind`，也不要让插件声明「这是布尔条件」。

### 1.4 Go 侧结构（与 schema 一一对应）

新建 `apps/api/app/Support/ExtensionManifest/v3_badge_conditions.go`：

```go
// ManifestBadgeCondition 声明一个插件事实条件。它不是规则条件类型：规则里
// 永远写 type:"fact.count"，这里声明的是 factKey 的合法取值与 variant 候选。
type ManifestBadgeCondition struct {
	Key             string                          `json:"key"`
	ContractVersion string                          `json:"contractVersion"`
	Label           map[string]string               `json:"label"`
	Unit            map[string]string               `json:"unit,omitempty"`
	Compare         []string                        `json:"compare,omitempty"`
	Variants        []ManifestBadgeConditionVariant `json:"variants,omitempty"`
}

type ManifestBadgeConditionVariant struct {
	Value string            `json:"value"`
	Label map[string]string `json:"label"`
}

// BadgeConditionsMaximum 与 ruleengine.MaxLeaves 对齐：一条规则的叶子数上限
// 就是事实条件数的自然上界，多声明的条件永远进不了任何规则。
const BadgeConditionsMaximum = 32

// BadgeConditionVariantsMaximum 是单个条件的 variant 上限。
const BadgeConditionVariantsMaximum = 16
```

---

## 2. 命名空间校验规则

### 2.1 复用 `OwnsEventName` 的语义，但不要直接调用它

`apps/api/app/Support/Events/catalog_namespace.go:8-15`
```go
func OwnsEventName(extensionID, eventName string) bool {
	extensionID = strings.TrimSpace(extensionID)
	eventName = strings.TrimSpace(eventName)
	if extensionID == "" || eventName == "" || eventName == extensionID {
		return false
	}
	return strings.HasPrefix(eventName, extensionID+".") && len(eventName) > len(extensionID)+1
}
```

**语义相同**（严格 `<id>.` 前缀、且不等于 id 本身），**实现不同**：`ExtensionManifest` 不能 import `Support/Events` 来复用（会引入一个只为 8 行字符串判断的包依赖，且 Events 包有自己的初始化副作用）。校验器里用已有的同类写法：

`v3_validate.go:444-446`
```go
func knownOrNamespacedContract(extensionID string, value string, known bool) bool {
	return known || (manifestIDPattern.MatchString(value) && strings.HasPrefix(value, extensionID+"."))
}
```

### 2.2 落点：直接复用 `versionedID`

`v3_validate.go:49-60`
```go
func (v *v3Validator) versionedID(id string, contractVersion string, family string) error {
	if !manifestIDPattern.MatchString(id) || !strings.HasPrefix(id, v.manifest.ID+".") ||
		!validContractVersion(contractVersion) {
		return fmt.Errorf("%w: versionedID pattern/ownership failed (family=%s id=%s)", ErrInvalidManifest, family, id)
	}
	if _, duplicate := v.ids[id]; duplicate {
		return fmt.Errorf("%w: duplicate versioned id %s (family=%s)", ErrInvalidManifest, id, family)
	}
	v.ids[id] = family
	return nil
}
```

`v.versionedID(condition.Key, condition.ContractVersion, "badgeConditions")` 一次拿到三件事：`manifestIDPattern` 形态校验、`<manifest.id>.` 前缀校验、**跨 family 的 id 唯一性**（`v.ids` 是全清单共享的，所以一个 `badgeConditions[].key` 不可能同时是某个 route id）。

### 2.3 三条补充校验（`versionedID` 不覆盖）

1. **`contractVersion` 必须等于 `key@N`**（已裁决，2026-11-12）。`versionedID` 只校验 pattern，不校验等式。全仓既有 family 一律是 `id@N`（夹具实例：`sforum-query-surface-reference.route.stats` / `sforum-query-surface-reference.route.stats@1`），这里必须显式加等式校验：
```go
if condition.ContractVersion != condition.Key+"@"+versionDigits(condition.ContractVersion) { … }
```
理由：`contractVersion` 会进审计与 `rule_health` 展示；两个名字漂移后，运维看到「条件 `a.b` / 契约 `a.condition.b@1`」无法判断是不是同一个东西。旧版计划 §8.1 的示例正是这种漂移形态，已按 `key@1` 修正。
2. **`key` 不得与 Core 内置条件重名**。但这条**不能写在 `ExtensionManifest` 里**：`Support/ExtensionManifest` 是 Support 层，`Models/Badges/conditions` 是 Models 层，Support → Models 是反向依赖（`AGENTS.md` 的「Backend rules」明确 package 依赖方向）。**落点应在事实目录的合并处**（`Models/Badges/conditions` 侧新增的 fact catalog），校验顺序：合并时发现 key 已存在于 `conditions.Core()` → 拒绝该扩展的声明并记审计。前缀规则让冲突在实践中极罕见，但不能只靠前缀（插件 id 理论上可以叫 `activity`）。
3. **`label` 必含双语**。`versionedID` 不管文案；漏 en 会让 M2 编辑器在英文界面显示中文。

### 2.4 其余 Go 侧校验清单

| # | 规则 | 拒绝方式 |
| --- | --- | --- |
| 1 | 仅 `type: "plugin"` 可声明（主题禁止） | `fmt.Errorf("%w: v3 badgeConditions rule #1", ErrInvalidManifest)` |
| 2 | 必须有 backend 且 `ProtocolVersion == 2`（事实只能由 Host 命令写入，没有 backend 就没有调用方） | `rule #2`，与 `validateAITools` 的 `rule #2` 同形（`v3_validate_platform.go:89-91`） |
| 3 | `len(BadgeConditions) <= 32` | `rule #3` |
| 4 | `compare` 非空、去重、⊆ 5 个 op | `rule #4` |
| 5 | `variants` ≤ 16、`value` 唯一且匹配 `^[a-z0-9][a-z0-9._-]{0,63}$` | `rule #5` |
| 6 | `label` 含 zh-CN + en-US 且非空 | `rule #6` |

**不再有 `valueKind` 相关校验**（原 #4/#5/#7 的布尔分支已删）。校验器只需按上表线性检查，没有任何 `if valueKind == …` 的分叉。

---

## 3. 6 个生产文件 + 1 份生成文档：精确改动清单

| # | 文件:行 | 现状 | 改动 |
| --- | --- | --- | --- |
| 1 | `apps/api/app/Support/ExtensionManifest/manifest.go:132` | `AITools []ManifestAITool \`json:"aiTools,omitempty"\`` | 紧随其后加 `BadgeConditions []ManifestBadgeCondition \`json:"badgeConditions,omitempty"\`` |
| 2 | `apps/api/app/Support/ExtensionManifest/schemas/manifest-v3.schema.json` | 根 `additionalProperties: false`，52 个 properties，`$defs` 60 项 | 加 `properties.badgeConditions`（§1.1）+ `$defs.badgeConditions`（§1.2） |
| 3 | `apps/api/app/Support/ExtensionManifest/json_schema.go:92` | `fragments := []string{"", "guards", …, "aiTools", …}` | 追加 `"badgeConditions"`，否则 `validateV3JSONSchemaFragment(body, "badgeConditions")` 报 `unknown manifest schema fragment` |
| 4 | `apps/api/app/Support/ExtensionManifest/v3_validate_platform.go:73` | `if err := v.validateAITools(); err != nil {` | 之后加 `if err := v.validateBadgeConditions(); err != nil { return err }`；实现放新文件 `v3_badge_conditions.go`，照 `validateAITools`（同文件 `:81`） |
| 5 | `apps/api/app/Support/ExtensionManifest/v3_normalize.go:32` | `for index := range manifest.AITools {` | 加 `badgeConditions` 归一化循环：`Key`/`ContractVersion`/`ValueKind` trim；`Compare` 逐项 `strings.ToLower(strings.TrimSpace(...))`；`Variants[].Value` trim；`Label`/`Unit` 的键 trim 并统一大小写 |
| 6 | `apps/api/app/Support/ExtensionManifest/v3_validate.go:366-367` | `len(manifest.Guards)+…+len(manifest.PackageFiles) > 0` | 求和里加 `len(manifest.BadgeConditions)`。**当前无测试钉这一项**，漏了不会红，但只声明 badgeConditions 的清单不会被识别为使用 V3 声明 |
| 7 | `docs/extensions/catalogs/manifest-v3.md` | 生成文件（头部 `Generated by` 标记） | `cd apps/api && go run ./cmd/sforum extension docs generate` |

### 3.1 会红的既有测试（与改点一一对应）

| 测试 | 位置 | 何时红 |
| --- | --- | --- |
| `TestGenerateCatalogDocs` | `apps/api/sdk/plugin/docsgen_test.go:93-101` | schema 加了 `badgeConditions` 但文档没重生成：`fields, _, _, _ := manifestV3SchemaFields()` 后逐个断言 `` `field` `` 出现在 `manifest-v3.md` |
| `TestCommittedCatalogDocsInSync` | 同文件 `:133` | 仓库内 `docs/extensions/catalogs/` 与现场生成不一致（同一原因，报错直接给重生成命令） |
| `TestManifestV3JSONSchemaRejectsUnknownFields` | `.../json_schema_test.go:25` | **不会红**（根 `additionalProperties` 本来就是 false） |
| `TestManifestV3JSONSchemaValidatesCanonicalContract` | 同文件 `:10` | **不会红**（新数组可选） |

### 3.2 必须写进校验器注释的告警（原文可用）

```go
// validateBadgeConditions 校验插件事实条件声明。
//
// **改了 Go struct 就必须同批改 schemas/manifest-v3.schema.json。**
// 本仓没有任何 reflect 式的「struct 字段 ↔ schema properties」对齐测试：
//   - 只加 struct 字段不改 schema → ValidateV3JSONSchema 因根 additionalProperties:false
//     直接拒包，但纯 Go 的 Validate(manifest) 路径照样放行，单测可能全绿，
//     只有装载真实 .zip 时才炸；
//   - 只加 schema 不改 struct → 声明被 JSON 解析静默丢弃（encoding/json 忽略未知键），
//     更危险：插件以为声明成功，事实条件却不在目录里。
// 两边的连接点只有人工纪律 + 这条注释，别指望测试兜住。
//
// 另一条纪律：key 的 pattern 必须与 badge_facts.fact_key 的 CHECK 逐字一致
// （database/migrations/202611120002_badge_rules.sql）。放宽任何一侧都会得到
// 「清单通过、上报 500」或「上报通过、清单拒绝」的半死状态。
//
// 第三条纪律：**不要给 badgeConditions 加回 valueKind**。插件条件只有 count 语义，
// 「是否发生过」用 compare:["gte"] + 规则 cmp:{gte,1} 表达。加回它等于同时增加
// 一个 struct↔schema 失配点、一条 boolean 求值分支、和一个 not 反转坑（2026-11-12 裁决）。
```

**为什么必须写**：`apps/api/app/Support/ExtensionManifest` 里 60 个 `$defs` 与 52 个 properties 全靠人手同步，`json_schema_test.go` 只覆盖「canonical 清单能通过」「未知字段被拒」两件事，没有任何一条断言「struct 的 json tag 集合 == schema 的 properties 集合」。这是本仓在扩展清单这块唯一的系统性缺口。

---

## 4. 卸载 / 重装语义

### 4.1 卸载：规则保留、条件失效、评估不授予

计划 §8.1 写的是「条件恒 false」，**但实现语义更准确的是 unknown**，两者不等价：

`apps/api/app/Support/RuleEngine/eval.go:15-20`
```go
// 三值语义：内部区分 true / false / unknown，`not unknown` 仍是 unknown。若按二值
// 处理（把 unknown 当 false 再取反），「查询失败」会在 `not` 下面变成「条件成立」，
// 那是 fail-open。最终 unknown 收敛为 Matched=false 且 Incomplete=true。
```

`eval.go:11-14`
```go
// 条件求值失败（查询超时、行不存在、参数不匹配）不进
// 顶层 error，而是作为该叶子按 false 计并收进 Result.Errors
```
（注：注释里「按 false 计」指的是**收敛后的结果**，叶子层实际返回 unknown。代码证据在 `eval.go:152-160`，resolver 报错时记 `CodeResolveFailed` 并 `return triUnknown`：
```go
	value, err := e.resolver.Resolve(e.ctx, Leaf{Type: n.Type, Params: n.Params})
	if err != nil {
		...
		e.record(path, CodeResolveFailed, fmt.Sprintf("条件 %q 求值失败：%v", n.Type, err))
		return triUnknown
	}
```
所以「失效条件」在引擎里是 unknown，不是 false。）

**所以「失效条件」的正确实现是：resolver 查不到该 `factKey` 时返回 error（不是 0），`ruleengine` 记 unknown。**
- 叶子在 `all` 下 → 整个 `all` unknown → `Matched=false, Incomplete=true` → **不授予**（正确）。
- 叶子在 `not` 下 → `not unknown` = unknown → 同样不授予（**正确**；若真按「恒 false」实现，`not` 会变 true 而误发）。
- **绝不能返回 0**：`lte` 条件会把 0 判成满足（`conditions` 包注释原话：「查询失败一律返 error …… **绝不**把错误吞成 0——那会让 `lte` 条件把『查不到』判成『满足』」，`conditions/registry.go:9-12`）。

### 4.2 `rule_health` 列出失效引用

`GET /admin/badges/rule-health`（计划 §9 表格）按 `Params.RefKind == "fact"` 取出规则里用到的 `factKey`，逐个查合并目录（Core 条件 + 已启用扩展的 `badgeConditions`）。缺失项 = 失效引用，输出到后台健康面板。

**必须注意的坑**：`factKey` 候选目录是「**当前已启用**扩展的声明」，不是历史声明。所以：
- 扩展**停用**（disable）与**卸载**（uninstall）都会让条件失效 —— 两种状态都要在 `rule_health` 里区分展示（停用可恢复、卸载需重装）。
- 不要静默删规则、也不要把规则自动改成 `not` 或删叶子：规则是运营资产，静默改写等于篡改审计。

### 4.3 重装后是否自动恢复 —— 建议：**是，按 key 匹配自动恢复**

理由（三条，都可验证）：

1. **规则里存的就是字符串 key**：`badges.rule` 是 JSONB，叶子写的是 `"factKey":"sforum.query-surface-reference.onboarding.tasks"`（`202611120002_badge_rules.sql` 的 `rule` 列 + `RuleEngine/decode.go:60-64` 的 `Params map[string]any`）。没有数字外键、没有指纹，重装后同一个 key 天然指回同一个条件。
2. **候选目录是实时合并的**：`rule_health` 与 resolver 都按当前启用的扩展现算，所以扩展一启用，失效引用立刻消失，不需要任何回填 job。
3. **`contractVersion` 变化要不要阻断恢复**：建议**不阻断**，但要可见。`fact.count` 的语义是「读计数」，`key` 相同即同一个事实维度；`contractVersion` 从 `@1` 升到 `@2` 只表示插件侧的 payload 契约变了，不影响 Core 侧读 `user_fact_counters`。若 lead 要更严（版本不匹配就标失效），代价是每次插件升级都要人工修规则 —— 我建议不做，但**要在 `rule_health` 里显示「当前 contractVersion」与「规则写入时的 contractVersion」的差异**（后者需要在规则保存时把用到的 factKey→contractVersion 快照进 `badges.rule` 的旁路字段或审计事件，属于 M3 可选增强）。

**唯一需要人工介入的情况**：扩展改了 `key`（重命名）。此时旧 key 永久失效，`rule_health` 会持续列出，运营必须手动改规则 —— 这正是「`key` 是稳定标识、改名即破坏契约」的那条注释（`conditions/registry.go:21`：「这些字符串会写进 `badges.rule` 的 JSON 与 HTTP 契约，改名即破坏契约」）要传达的意思。

---

## 5. 运行时：resolver 判据与目录暴露规则

这一节不是 manifest 校验，而是 M3 落地时最容易做错的两处运行时行为。两条都是「判据」而不是「建议」。

### 5.1 resolver 查不到已声明之外的 `factKey` 必须返回 error，绝不能返回 0

**判据**：`fact.count` 的 resolver 在 `factKey` 不在当前合并候选目录里时（未声明 / 扩展停用 / 扩展卸载 / 声明被撤销），返回一个 error。

**为什么不能返回 0**：`cmp.op = lte` 会把 0 判成「满足」。一个被卸载的扩展留下的 `lte 5` 规则会在每次评估里对**所有用户**成立 —— 这是静默误发，而且发生在「扩展已经不在」这种最不容易被联想的情境里。`conditions` 包开头的注释已经把这条钉死（`registry.go:9-12`：「查询失败一律返 error …… **绝不**把错误吞成 0」）。

**为什么也不能返回「false 值」**：`fact.count` 是 `ValueKind: count`，没有布尔形态（§1.2）。它只能返回计数或 error，中间态不存在。

**错误会被引擎怎么处理**（已实现，不需要 M3 再做）：

`apps/api/app/Support/RuleEngine/eval.go:152-160`
```go
	value, err := e.resolver.Resolve(e.ctx, Leaf{Type: n.Type, Params: n.Params})
	if err != nil {
		if e.ctx.Err() != nil {
			// 取消是整次评估的问题，交给顶层转成 error，不记为「条件失败」。
			return triUnknown
		}
		e.record(path, CodeResolveFailed, fmt.Sprintf("条件 %q 求值失败：%v", n.Type, err))
		return triUnknown
	}
```

→ `triUnknown` → 最终 `Result{Matched: false, Incomplete: true}`（`eval.go:40-43`）。`not unknown` 仍是 unknown（`eval.go:126-138`），所以**不存在「失效条件被 `not` 反转成 true」的路径**。

**验收断言**（写进 M3 的集成测试）：停用一个声明了 factKey 的扩展后，同一规则评估必须给出 `Matched=false, Incomplete=true`，且 `Errors` 里有一条 `CodeResolveFailed`。若拿到 `Incomplete=false`，说明 resolver 把错误吞了。

### 5.2 目录暴露：`fact.count` 恒在 `ruleengine` 白名单，但 `factKey` 候选为空时不出现在后台条件目录

**判据**：「有候选才出现」，不是「有定义就出现」。

两个面必须分开看，它们是两个不同的消费者：

| 面 | 消费者 | `fact.count` 是否出现 | 依据 |
| --- | --- | --- | --- |
| `ruleengine.Catalog`（`conditions.Core()`） | **保存期校验** | **恒出现** | 规则里 `type:"fact.count"` 必须能被 `ConditionMeta` 命中，否则 M3 规则会被 `unknown_condition` 拒（`validate.go:184` 的 `AllowedCompare` 检查也依赖它） |
| `GET /admin/badges/conditions` | **M2 编辑器渲染** | **仅当合并后的 `factKey` 候选非空** | 没有候选时渲染一个空下拉 = 运营点进死路；而 M1 阶段没有扩展，必然为空 |

**实现落点：这个 seam 已经存在，不要另造一个。**

`conditions.Registry` 有两个方法，差别只有一条：

`apps/api/app/Models/Badges/conditions/registry.go:133-139`
```go
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, key := range r.order {
		out = append(out, r.defs[key])
	}
	return out
}
```

`registry.go:141-157`
```go
// PublishableDefinitions 是 `GET /admin/badges/conditions` 的返回集合。
//
// 与 `Definitions` 的差别只有一条：**objectRef 参数没有候选的条件不出现**。
// M1 的 `fact.count` 就是这种——没有扩展声明 factKey 时它是空下拉，暴露出去只会
// 让运营点进死路；M3 有扩展声明候选后它自动出现。校验/求值仍然认它（`Catalog`
// 不过滤），否则 M3 存下来的规则会通不过校验。
func (r *Registry) PublishableDefinitions(factKeys []string) []Definition {
```

`registry.go:159-170` 的 `hasCandidates(def, factKeys)` 是判据实现：遍历 `def.Params.Specs`，遇到 `Kind == ParamObjectRef` 且 `RefKind == "fact"` 且 `len(factKeys) == 0` 时返回 false。

**M3 要做的不是新增方法，而是把 `factKeys` 填对**：
1. 从当前**已启用**扩展的 `badgeConditions[]` 合并出 `factKey` 集合；
2. 传给 `PublishableDefinitions`；
3. HTTP 控制器 `GET /admin/badges/conditions` 只调 `PublishableDefinitions`，**不许**回退到 `Definitions()`。

**为什么不能把候选存进 Registry 字段**：`Registry` 是只读单例（`registry.go:92-94` 注释：「插件事实候选不进字段（那会变成全局可变状态 + 数据竞争），而是作为 `PublishableDefinitions` 的参数传入」）。扩展启停会让候选集合变化，存进单例字段就得加锁并处理失效，参数化是对的。

### 5.3 两个容易踩的语义细节

1. **`variant` 的 `Options: nil` 不等于「没有候选」**。`fact.count` 的参数声明是 `{Name:"variant", Kind:ParamEnum, Required:false, Options:nil}`（`registry.go:299`）—— `Options: nil` 在这里表示「候选由事实目录在运行时填」，而不是「空下拉」。M2 编辑器若把 `nil` 直接渲染成空下拉，运营会以为不能选 variant。`DefinitionsForEditor` 的职责就是把这个 `nil` 换成实际的 variant 列表。
2. **`fact.count` 的 `label` 是 Core 文案，不是插件文案**。条件目录里 `fact.count` 显示「插件事实计数 / Plugin fact count」（`registry.go:302-304`），插件自己的 `label{zh-CN,en-US}` 属于**候选值**（下拉项），两者不能互相覆盖。M2 渲染时是「条件名 = Core 文案，下拉项 = 插件 label」。

---

## 6. 夹具计划：`sforum-query-surface-reference`

### 6.1 为什么是它

- 有**静态** `sforum.extension.json`（不是 `.tmpl`），`scripts/ci/reference-packages-gate.sh:53` 的 `[ -f "$source/sforum.extension.json" ] || continue` 会真的把它纳入 build → digest → validate → test。
- `manifestVersion: 3`，已声明 `backend`（protocol 2）+ `capabilities: ["host.api"]` + `database` + `openapi` + `routes` + 带真实 digest 的 `packageFiles` —— 满足 §2.4 第 2 条（必须有 backend/protocol 2）的前置。
- 它的 `id` 是 `sforum.query-surface-reference`，带点、带连字符，能顺带验证 key 的字符集与 `versionedID` 前缀匹配。

**不要用** `sforum-ai-tool-reference`：只有 `sforum.extension.json.tmpl`，被上述门禁跳过。

### 6.2 最小可跑 diff

在 `extensions/fixtures/plugins/sforum-query-surface-reference/sforum.extension.json` 的 `"capabilities"` 之后插入（下面是成员片段，末尾逗号按上下文保留）：

```json
  "badgeConditions": [
    {
      "key": "sforum.query-surface-reference.onboarding.tasks",
      "contractVersion": "sforum.query-surface-reference.onboarding.tasks@1",
      "label": {
        "zh-CN": "完成新手任务数",
        "en-US": "Completed onboarding tasks"
      },
      "unit": {
        "zh-CN": "个",
        "en-US": "tasks"
      },
      "compare": ["gte", "lte"],
      "variants": [
        {
          "value": "tutorial",
          "label": { "zh-CN": "教程任务", "en-US": "Tutorial tasks" }
        },
        {
          "value": "profile",
          "label": { "zh-CN": "资料完善任务", "en-US": "Profile tasks" }
        }
      ]
    },
    {
      "key": "sforum.query-surface-reference.verified_reports",
      "contractVersion": "sforum.query-surface-reference.verified_reports@1",
      "label": {
        "zh-CN": "已提交的校验报告数",
        "en-US": "Submitted verified reports"
      },
      "unit": {
        "zh-CN": "份",
        "en-US": "reports"
      },
      "compare": ["gte"]
    }
  ],
```

两个条件刻意覆盖两种形态，且**都在 count 语义内**：
- 第一个带 `variants` 与 `compare: ["gte","lte"]` —— 验证 variant 候选、窗口与双向比较；
- 第二个只有 `compare: ["gte"]`、没有 variants —— 它就是「是否发生过」的表达方式（规则侧写 `cmp:{op:"gte",value:1}`）。**不要**为它引入 `valueKind:"boolean"`。

**digest 不用手改**：`badgeConditions` 不是 package file，`packageFiles[].digest` 不受影响；artifact 级 digest 由门禁的 `digest --write` 重算（`scripts/ci/reference-packages-gate.sh` 的暂存副本流程）。该 fixture 的 digest 目前没有在别处硬编码（`rg 72c20606…` 只命中它自己的清单）。

### 6.3 引用它的徽章规则（M3 联调用）

```json
{
  "v": 1,
  "root": {
    "op": "all",
    "children": [
      {
        "type": "fact.count",
        "params": {
          "factKey": "sforum.query-surface-reference.onboarding.tasks",
          "variant": "tutorial",
          "window": { "kind": "recent_days", "days": 30 }
        },
        "cmp": { "op": "gte", "value": 3 }
      },
      {
        "type": "fact.count",
        "params": {
          "factKey": "sforum.query-surface-reference.verified_reports",
          "window": { "kind": "all" }
        },
        "cmp": { "op": "gte", "value": 1 }
      }
    ]
  }
}
```

三个刻意点：
1. **两个叶子都带 `cmp`**：插件条件只有 count 语义，所以不存在「布尔叶子」。第二个叶子用 `gte 1` 表达「至少发生过一次」—— 这正是取代 `valueKind:"boolean"` 的写法。计数条件缺 `cmp` 会在求值期报 `compare_missing` 并判 unknown（`RuleEngine/eval.go` 的 `case ValueCount` 分支），不是「默认成立」。
2. `variant` 是**可选**参数：第一条带、第二条不带（不带 = 汇总该 factKey 的所有 variant，对应 `user_fact_counters` 的 `(user_id, fact_key, variant)` 主键 —— 不带 variant 时需要 `SUM(total)` 聚合，M1 的 `fact.count` 参数声明里 `variant` 是 `Required: false`）。
3. `window.kind=all` **不许带 days**（`validate.go:314-317` 明确拒绝「全部时间 + 30 天」这种自相矛盾输入）。

### 6.4 配套断言（建议一并加进 `task-3`）

- `extension validate` 通过（schema + Go 校验双路径）。
- 一条**否定**用例：把 `key` 改成不带扩展前缀（如 `onboarding.tasks`）→ 必须 `ErrInvalidManifest`。
- 一条**否定**用例：`compare: []`（空数组）→ 必须被拒（schema `minItems: 1` + Go 校验 #4，两条路径都要有测试）。理由：没有比较运算符的条件永远无法被任何规则使用，放行它只会让插件作者以为声明成功了。
- 一条**否定**用例：`contractVersion` 写成 `key` 之外的形态（如 `acme.onboarding.condition.tasks@1`，即旧版计划 §8.1 的示例）→ 按 §2.3 第 1 条应被拒。
- 一条**运行时**断言（不在 manifest 层）：扩展停用后，同一规则评估结果为 `Matched=false, Incomplete=true` 且 `Errors` 含 `resolve_failed`；**不得**是 `Matched=false, Incomplete=false`，更不得是 `Matched=true`（见 §5.1）。

---

## 7. 已裁决 / 仍未找到

### 7.1 已裁决（2026-11-12，lead 采纳后冻结）

| # | 议题 | 裁决 | 落点 |
| --- | --- | --- | --- |
| 1 | `badgeConditions` 是否等于「新增条件类型」 | **否**：只是给 `fact.count` 注册 factKey/variant 候选 | §0 |
| 2 | `contractVersion == key@N` 是否强制 | **强制**。旧示例 `acme.onboarding.condition.tasks@1` 是漂移样本 | §2.3 第 1 条 |
| 3 | `badgeConditions` 是否要求 backend | **要求**（`ProtocolVersion == 2`），与 `aiTools` 同纪律 | §2.4 #2 |
| 4 | 失效条件的语义 | **unknown，不是 false**；resolver 必须返 error，绝不返 0 | §4.1、§5.1 |
| 5 | 插件条件是否有 `valueKind` | **没有**，只有 count 语义；布尔用 `compare:["gte"]` + `cmp:{gte,1}` | §1.2、§1.3 |
| 6 | `fact.count` 的目录暴露 | **有候选才出现**；`ruleengine` 白名单恒含它 | §5.2 |

**本文件自此冻结**：M3 开工直接照此实现，改动需回到 lead 重新裁决。

### 7.2 仍未找到 / 仍待定

1. **`fact.count` 的 resolver 与事实候选目录都还没有实现**。`app/Models/Badges/conditions/` 目前只有 `registry.go`（元数据 + `PublishableDefinitions` 的候选判据）与一份集成测试骨架；`RefKind:"fact"` 的**候选来源**（扩展清单的哪份快照、停用/卸载如何反应、候选集合从哪里取）没有任何实现可参照。M3 需要新增「事实目录」协作方（建议落 `app/Models/Badges/conditions/facts.go`），但其数据来源要等 M1.3 解析器 API 定稿 —— 我未找到既有代码可抄。
2. **`compare` 是否统一收窄为 `gte/lte`**。内置条件用 `countCompare()` 只给 `gte/lte`（`registry.go:172-176`）；本规范按「插件允许全集」写入 schema（§1.3）。若要统一收窄，改 enum 一处即可，但需明确「恰好 N 次」这类需求是否要支持。
3. **停用（disable）与卸载（uninstall）在 `rule_health` 里是否要区分展示** —— §4.2 建议区分（停用可恢复、卸载需重装），未找到既有 `rule_health` 实现可参照（计划 §9 只定义了路由 `GET /admin/badges/rule-health`）。
4. **`variant` 候选的填充时机** —— `PublishableDefinitions` 的签名只接 `factKeys []string`，没有接 variants。若 M2 需要按 factKey 渲染 variant 下拉（§5.3 第 1 条），签名要扩成 `map[string][]string`（factKey → variants）或一个 `FactCandidate` 结构。这是 M3 落地时必须改的一处签名，本规范不预先定，因为它牵动 M2 的组件契约。

## 8. Errata 2（Lead 裁决，2026-11-12，正文仍冻结，以本节为准）

M3 开工前小研核实代码（`../reports/2026-11-12-badge-m3-impl-facts.md`）挖出 4 处与正文不符 + 2 项正文未定项，Lead 裁决如下。

1. **正文 §7.2 #4（`variant` 候选）/ §6.3 联调规则`存不进去`**：`fact.count` 的 `variant` 是 `ParamEnum + Options:nil`，而保存期 `ruleengine.Validate` 对 enum 做成员校验（`RuleEngine/validate.go:228-236`），`nil` 必然不含任何值 → **带 variant 的规则 100% 422**。
   **裁决**：`ParamEnum` 且 `Options == nil` 在引擎里语义定为「**开放集**」——跳过成员校验，只做非空/长度/字符集；**域层兜底**：`Models/Badges/rule_store.go` 保存时若该 `factKey` 有已声明 variants，则 `variant` 必须 ∈ variants（否则 `422 forum.badge_rule_reference_invalid`）；候选为空（扩展停用/卸载）时**放行保存**，只在求值期 unknown。
   理由：引擎保持纯包（不引入域形状）；卸载后运营仍能保存/编辑规则，且不会因为「目录没了」把一条合法规则判成非法。
2. **正文 §7.2 #4（候选形状）**：**裁决** `conditions.FactCandidate{Key, LabelZh, LabelEn, UnitZh, UnitEn, Compare []string, Variants []FactVariant{Value, LabelZh, LabelEn}}`，`PublishableDefinitions(candidates []FactCandidate)`；`GET /admin/badges/conditions` **新增顶层 `factCandidates`**（`items` 不动、additive）。理由：插件声明了双语 `label`，扁平 `map[string][]string` 会让它白声明（下拉只能显示 key 原文），而 key→variants 的归属关系在扁平 `Options []string` 里无法表达。
3. **正文 §3 第 3 项（`json_schema.go:92` fragments）失败模式写反**：fragments 只服务 include shard，不加不会报 `unknown fragment`；真雷是「加 fragments 不加 `$defs`」→ init 编译失败、全仓清单校验一起挂。**裁决**：fragments 与 `$defs` **必须同批**，注释写明。
4. **正文 §3 第 6 项（`v3_validate.go:366` 的 `hasV3Declarations` 求和）**：该函数全仓零调用（`aiTools` 也不在求和里），**裁决：不动**，属死代码清理，不在本刀。
5. **`key` 长度三方不齐**（DB 2–121 / schema 2–121 / Go `manifestIDPattern` 2–81）：**裁决**：schema 与 DB CHECK 的 pattern 保持**逐字一致** `^[a-z0-9][a-z0-9._-]{1,120}$`（正文 §1.3 的要求不放宽）；Go 侧更严的 81 字符**保留**（是子集，安全侧偏差），在 `validateBadgeConditions` 注释写明「82–121 字符能过 schema/DB，但会被 manifest 的 `versionedID` 拒」。
6. **`total` 可为负 + SDK 十进制编码器不吃负号**：**裁决**：`badges.fact.record` 的 `result.total` 回 **clamp 后（≥0）** 的值，与条件读取侧 `GREATEST(total,0)` 完全一致；不给 SDK 加有符号编码路径。文档写明「扣减到负数时回执为 0」。
7. **拒绝审计 action**：**裁决** 复用既有能力/命令拒绝审计路径（`v2_command.go` 的 capability gate），不新造 action；若确实没有可复用的通用拒绝审计，才新增 `extension.badge_fact_record.rejected`，并在实现回报里说明依据。

## 9. Errata 3（Lead 裁决，2026-11-12，`badges.fact.record` 输入补齐）

正文 §8.2 的输入样例只有 `factKey/variant/delta/occurredAt/dedupeKey`，**漏了「记给哪个用户」**。事实是按用户的行（`RecordFact` 要求 `UserID > 0`），而命令的调用方是**扩展运行身份**（`SourceExtensionID`），不是被记账的用户。不补这个字段，命令无法实现。

1. **新增必填输入 `userId`**：**十进制字符串**（BIGINT 走字符串，避免 protobuf Struct 的 double 精度问题；与 `delta` 同一编解码纪律）。缺失/非数字/≤0 → 拒绝，reason `invalid_user_id`。
   - 不采用「从父请求上下文推断当前用户」：插件的事实往往在**异步 observe handler** 里产生（`events[]` 投递时没有终端用户请求），推断会在最关键路径上取不到人；显式传反而让插件作者必须回答「这条事实属于谁」。
2. **`ErrorDetail.reason` 字面量冻结**（前端/SDK/测试从此对齐，不许各写一套）：
   | reason | 触发 |
   | --- | --- |
   | `capability_denied` | 调用方扩展未在 `capabilities[]` 声明 `badges.fact.write` |
   | `fact_key_not_declared` | `factKey` 不属于**调用方自己**的 `badgeConditions[].key`（含冒用他人 key） |
   | `invalid_user_id` | `userId` 缺失/非十进制/≤0 |
   | `invalid_delta` | `delta` 缺失/非十进制/为 0 |
   | `invalid_occurred_at` | `occurredAt` 非 RFC3339 |
   | `fact_write_failed` | 存储层失败（唯一索引冲突之外的真实错误） |
   顶层错误码仍是 `forum.badge_fact_write_denied`（`Localization/messages.go` 双语文案），reason 是它的 `data.reason` 细节。
3. **`dedupeKey` 命中唯一索引 → `inserted:false` 是正常结果**（`Output`），**不得**映射成上面任何 reason。
4. **拒绝留痕的分层**（Lead 裁决，核对代码后）：
   - **能力缺失（`capability_denied`）不落审计**：全仓所有 Host 命令的能力闸门都在**事务之前**返回协议错误（`v2_command.go:307-330` → `host.command_capability_denied`），既有测试一律断言 `GetReason()==...` 而不读审计行。给单条命令开特例会让「拒绝是否留痕」变成每个命令各一套，**不做**。该例外必须写进 `docs/extensions/badge-plugin-facts.md`。
   - **域层拒绝要留痕**：`fact_key_not_declared` / `invalid_user_id` / `invalid_delta` / `invalid_occurred_at` / `fact_write_failed` 由 badge 命令自己**best-effort** 追加审计 `extension.badge_fact_record.denied`（字段：`extensionId`、`factKey`、`reason`，`userId` 可空）。审计写入失败**不得**改变返回码（拒绝仍然是拒绝，不许变成 500，也不许因为审计失败而放行）。
   - 理由：能力闸门是共享契约，域命名空间是插件可滥用的面；后者必须可追责。

## 10. Errata 4（Lead 裁决，2026-11-12，M3.1 验收挖出的两处）

小审在 M3.1 独立验收里挖出两处「规范与实现覆盖面不一致」，裁决如下：

1. **`rule #1`（非 `plugin` 类型）实际不可达**：`type: "theme"` 且声明 `backend` 的组合会先被主题校验拒掉（错误文案不带 `badgeConditions rule #1` 编号）。
   **裁决：不重排校验顺序**（把类型检查前移会改变所有 family 的报错优先级，风险远大于收益）。`rule #1` 按**纵深防御**保留，规范与测试都写明：该条只能断言「Go 拒绝」，不能断言拒绝原因是 `rule #1`。测试若已按「错因绑定 rule 编号」写，改成断言「Go 路径拒绝」+ 注释说明为什么拿不到编号。
2. **schema 单独不强制 `backend`**：`manifest-v3.schema.json` 根 `required` 不含 `backend`（它由 `allOf` 对 `content/seo/identity` 触发）。今天装包安全（`load.go` 在 schema 之后必跑 Go `Validate`），但这依赖**调用顺序**，属「今天是巧合、明天是漏洞」。
   **裁决：补 schema 条件**——在 `badgeConditions` 存在时要求 `backend`（`if/then` 形态，与既有 `allOf` 同款），使两条路径各自自洽。同批重生成 `docs/extensions/catalogs/manifest-v3.md` 并跑 `TestCommittedCatalogDocsInSync`。
   顺带修正实现方测试注释里「`backend.entry` 有 `minLength:1`」的错误说法（`$defs.backend` 没有这条，拒绝来自 `allOf` 的 then 分支）。

执行纪律（不涉及契约，但会制造假证据）：**不要与正在改同一批文件的同事并行跑 `--mutate`**。M3.1 验收时后端正在改 `Jobs/Badges/**` 与 `Models/Badges/**`，导致变异头部一条断言瞬时 `[build failed]`（单跑同命令 exit 0）。变异整轮只能在没有其他写者时跑，且每步恢复后 `cmp` 验字节。
