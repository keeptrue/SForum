# 插件徽章事实（`badgeConditions` 与 `badges.fact.record`）

> **语言切换：** [English (canonical)](../../extensions/badge-plugin-facts.md) · 本文为中文翻译。
> 字段名、命令、错误码以英文原文和代码为准；中文版若与代码不一致，以代码为准。

本页是徽章规则引擎**插件事实面**的作者与运营参考：插件如何声明一个可计数的事实、
宿主如何从插件进程写入事实，以及插件被停用或卸载后徽章规则会发生什么。

配套文档：

- [插件编写指南（authoring-guide，英文）](../../extensions/authoring-guide.md)
- [Host API v2（英文）](../../extensions/host-api-v2.md) —— 本页扩展的 Host 命令模型
- [Manifest V3 字段目录（生成）](../../extensions/catalogs/manifest-v3.md)
- [能力目录（生成）](../../extensions/catalogs/capabilities.md) —— `badges.fact.write`
- [核心事件目录（生成）](../../extensions/catalogs/events.md) —— `events[]` 里可用的名字

## 1. 这个面是什么

`fact.count` 是**核心侧**的徽章条件。插件不新增条件类型，只登记这个条件的合法取值：

| 概念 | 归属 | 位置 |
| --- | --- | --- |
| `fact.count` 条件类型 | 核心 | `Models/Conditions` 注册表（14 条白名单条件之一） |
| 合法 `factKey` 与 `variant` 候选 | 插件 | Manifest V3 的 `badgeConditions[]` |
| 事实行与累计计数 | 核心 | `badge_facts`（append-only）+ `user_fact_counters`（聚合） |
| 写入入口 | 核心 | Host 命令 `badges.fact.record` |

写插件前需要知道的三条推论：

1. 规则引擎的条件白名单**不随插件数量增长**。规则引用插件事实永远走
   `type: "fact.count"`。
2. 规则健康检查复用既有的 `objectRef` 机制：`factKey` 带 `RefKind: "fact"`，
   所以失效的 key 与「分类被删、角色改名」走同一套失效引用检查。
3. 插件始终拿不到徽章数据表的访问权。它上报一个事实，写入由宿主在自己的事务里完成。

## 2. `badgeConditions[]`

`badgeConditions` 是可选顶层数组。只接受 `type: "plugin"` 且声明了 Protocol V2
`backend` 的包 —— 主题没有调用方，没有 backend 的插件没有写入方，两种声明都只会
产生一个永远为 0 的死候选。

```json
"capabilities": ["badges.fact.write"],
"badgeConditions": [
  {
    "key": "acme.onboarding.tasks",
    "contractVersion": "acme.onboarding.tasks@1",
    "label": { "zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks" },
    "unit": { "zh-CN": "个", "en-US": "tasks" },
    "compare": ["gte", "lte"],
    "variants": [
      { "value": "tutorial", "label": { "zh-CN": "教程任务", "en-US": "Tutorial tasks" } },
      { "value": "profile", "label": { "zh-CN": "资料完善任务", "en-US": "Profile tasks" } }
    ]
  }
]
```

### 2.1 字段

| 字段 | 必填 | 类型 / 上限 | 说明 |
| --- | --- | --- | --- |
| `key` | 是 | 字符串，pattern `^[a-z0-9][a-z0-9._-]{1,120}$` | 即 `factKey`。必须以 `<manifest.id>.` 开头，且**是稳定标识** —— 它会写进 `badges.rule` 的 JSON，改名即破坏所有用过它的规则。它同时参与清单级的 versioned id 唯一性集合，所以一个事实 key 不可能与某个 route / cache / query 的 id 撞名。 |
| `contractVersion` | 是 | 字符串，`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`，≤256 | **必须恰好等于 `key + "@" + N`。** 漂移的写法（`a.b` 配 `a.condition.b@1`）会被拒：否则审计与健康清单无法判断两个名字是不是同一个事实。 |
| `label` | 是 | 对象，必须含 `zh-CN` + `en-US`，值非空 | 条件目录是服务端下发给后台编辑器渲染的，缺一个语种就会在该语言的界面里显示错语言。 |
| `unit` | 否 | 对象，给了就守同一条双语规则 | 显示在数字旁边（`个` / `tasks`）。 |
| `compare` | 是 | 数组，1..5 项，不可重复 | 取值 ⊆ `gte`、`gt`、`lte`、`lt`、`eq`。**没有 `ne`**：「未达成」请用 `not` 包住 `gte`。空数组会被拒 —— 没有任何比较运算符的条件永远无法被规则使用。 |
| `variants` | 否 | 数组，≤16 | 每项 `{value, label}`。`value` 用 `^[a-z0-9][a-z0-9._-]{0,63}$` 且不可重复；空串保留给「无 variant」。 |

上限：**`badgeConditions` 最多 32 条**、**每条最多 16 个 variant**。32 对齐规则
引擎的叶子上限（`MaxLeaves = 32`）：一条规则不可能引用超过叶子数的插件条件，多声
明的部分只是扩大评审面而永远不会生效。

### 2.2 校验规则

| # | 规则 | 失败表现 |
| --- | --- | --- |
| 1 | 只有 `type: "plugin"` 可声明 | 清单非法 |
| 2 | 必须有 Protocol V2 的 backend | 清单非法 |
| 3 | 最多 32 条 | 清单非法 |
| 4 | `compare` 非空、去重、取值来自五个 op | 清单非法 |
| 5 | `variants` ≤ 16、`value` 唯一且符合 pattern | 清单非法 |
| 6 | `label`（以及给了的 `unit`）必须有 `zh-CN` + `en-US` 且都非空 | 清单非法 |
| — | `contractVersion == key + "@N"` | 清单非法（漂移检查） |

**改 Go struct 必须同批改 JSON Schema。** 本仓没有 reflect 式的「struct 字段 ↔
schema properties」对齐测试：

- 只改 struct 不改 schema → 纯 Go 的 `Validate` 仍通过，但装载时
  `ValidateV3JSONSchema` 因根对象 `additionalProperties: false` 直接拒包；
- 只改 schema 不改 struct → 声明被 `encoding/json` 静默丢弃，插件以为声明成功，
  目录里却没有这个条件。

### 2.3 `key` 的 pattern 就是数据库契约

这个 pattern 逐字抄自创建事实表的迁移：

```sql
fact_key TEXT NOT NULL CHECK (fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$')
```

逐字符照抄。该 pattern 里没有反斜杠，所以 SQL 字面量、Go 值与 JSON Schema 值完全
相同：

```json
"pattern": "^[a-z0-9][a-z0-9._-]{1,120}$"
```

这里不需要做 `regexp.QuoteMeta` 式转义（字符类内 `.`、`_`、`-` 都是字面量，且 `-`
在末尾）。为「保险」写成 `\\.` 反而会接受反斜杠并拒掉合法 key。

Go 侧清单校验不再保存这份 pattern 的第二份副本：它通过共享的 versioned id 检查复用
manifest id pattern，这就是下表中 Go 的实际上限比 schema 更严的原因。

三层长度界限**刻意不完全相同**：

| 层 | 接受的 `key` 长度 |
| --- | --- |
| `badge_facts.fact_key` 的 CHECK | 2–121 |
| `badgeConditions[].key` 的 schema | 2–121 |
| 清单 versioned id（Go） | **2–81** |

Go 侧复用了 manifest id pattern，因此是三处里最严的：82–121 字符的 key 能过 schema
与 CHECK，却会被清单校验拒绝。请保持 key 简短，不要依赖外层上界。放宽任何一侧都会
得到本页想避免的半死状态：「清单通过、插入失败」或「插入通过、清单拒绝」。

## 3. 在徽章规则里引用事实

插件事实永远是计数叶子：

```json
{
  "v": 1,
  "root": {
    "op": "all",
    "children": [
      {
        "type": "fact.count",
        "params": {
          "factKey": "acme.onboarding.tasks",
          "variant": "tutorial",
          "window": { "kind": "recent_days", "days": 30 }
        },
        "cmp": { "op": "gte", "value": 3 }
      },
      {
        "type": "fact.count",
        "params": {
          "factKey": "acme.onboarding.tasks",
          "window": { "kind": "all" }
        },
        "cmp": { "op": "gte", "value": 1 }
      }
    ]
  }
}
```

- **每个计数叶子都必须带 `cmp`。** 没有比较就不成其为条件，求值期该叶子是 unknown，
  徽章永远不发。插件条件没有布尔形态：「至少发生过一次」写成
  `"cmp": {"op": "gte", "value": 1}`。
- **`variant` 可选。** 省略表示「汇总该 factKey 的所有 variant」。
- **`window` 只有两种**：`{"kind": "all"}` 或 `{"kind": "recent_days", "days": N}`
  （`N` 在 1..3650）。`kind: "all"` 不许带 `days`。

保存期行为：

| 情形 | 结果 |
| --- | --- |
| `factKey` 没有被任何已启用插件声明 | **保存成功。** 规则是运营资产：清单可能临时被禁用，也可能正在升级。 |
| `variant` 不在该 key 已声明的候选里 | **保存被拒**，返回引用类错误（`forum.badge_rule_reference_invalid`）。拼错是规则缺陷，不是插件状态。 |
| `factKey` 没有声明任何 variant | 任何 `variant` 值在保存期都放行（没有可对照的候选）。 |
| 条件类型未知 / 比较值非法 | 由规则引擎拒绝（`forum.badge_rule_*`）。 |

## 4. Host 命令 `badges.fact.record`

| 属性 | 值 |
| --- | --- |
| 命令 ID | `badges.fact.record` —— **ID 不带版本后缀** |
| 命令版本 | `1`（独立字段） |
| 输入 schema ref | `sforum.badges.fact.record.input@1` |
| 结果 schema ref | `sforum.badges.fact.record.result@1` |
| Policy ID | `sforum.badges.fact.record@1` |
| 进程能力 | `badges.fact.write` |
| actor 模式 | service（无 actor）：调用方身份是已验证的扩展运行时，永远不是请求里的自称字段 |

这处不对称是刻意的、也最容易写错：**命令 ID 与它的版本是两个独立字段**
（`"badges.fact.record"` + `"1"`），而 **schema ref 与 Policy ID 把 `@1` 内联**。
不存在 `badges.fact.record@1` 这样的命令 ID。

### 4.1 输入

```json
{
  "userId": "42",
  "factKey": "acme.onboarding.tasks",
  "variant": "tutorial",
  "delta": "1",
  "occurredAt": "2026-11-12T12:00:00Z",
  "dedupeKey": "task-42:completed"
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `userId` | 是 | 事实归属的用户，**十进制字符串**（列是 `BIGINT`，载荷是 protobuf `Struct`，其唯一数值类型是 `double` —— 见 §9.1）。必须能解析为正整数。事实是**按用户**的行，「关于站点」的事实无法表达。 |
| `factKey` | 是 | 必须是**本插件自己**的 `badgeConditions[].key` 之一。声明属于别的插件的 key 会被拒。 |
| `variant` | 否 | 空/缺省表示「无 variant」桶。 |
| `delta` | 是 | **十进制字符串，不是 JSON 数字。** 命令载荷是 protobuf `Struct`，其数值类型是 `double`，超过 2^53 的整数会在那里丢精度。值必须非零；负数是合法的，表示扣减。 |
| `occurredAt` | 否 | RFC 3339 时间戳，缺省取宿主时钟。它决定窗口查询（`recent_days`）落在哪一天。 |
| `dedupeKey` | 否 | 幂等键。非空时，同一 `(用户, factKey, variant, dedupeKey)` 的重复调用不写入，返回 `inserted: false`。为空时每次调用都写一行新事实。 |

字段名以命令输入契约（`sforum.badges.fact.record.input@1`）为准。只有这六个字段，
多一个都不接受，字段名也不能缩写。SDK 封装是 `Host.RecordBadgeFactRequest` /
`Host.RecordBadgeFact`（入参 `BadgeFactInput`，其中 `UserID` 是 `int64`）；§5 同时给了
封装与其构造出的线上文档，线上就是这六个名字。

### 4.2 结果

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `inserted` | 布尔 | `false` 表示 dedupe 命中此前的写入。**这不是错误** —— 不要重试，也不要记成失败。 |
| `total` | 十进制字符串 | 写入后的累计值，**只在回执里下限截到 0**。存储侧的计数（`user_fact_counters.total`）保留真实值、可以为负 —— 扣减是真扣了，不是被吞了 —— 但任何读面（含这条回执）都不下发负数。见 §9.5。 |

宿主在**同一个事务**里写事实行与累计计数。中途失败两者都不留，不存在「有行没计数」
或「有计数没行」的状态。

### 4.3 拒绝

拒绝是正常的命令结果，原因是 `ErrorDetail.reason` 里**冻结的字面量**，不是传输层
错误。检查顺序：运行时身份与该制品一致 → 能力已授予 → 输入符合其 schema →
`factKey` 属于本插件的声明 → `delta` 是非零整数字符串。六个 reason 字面量、顶层
错误码，以及「哪些拒绝会留下审计行」，都在 §9.2 与 §9.3 冻结。

### 4.4 能力与运营披露

必须显式声明能力：

```json
"capabilities": ["badges.fact.write"]
```

未声明时每次调用都会以 `host.command_capability_denied` 被拒 —— 于是「开发时能跑、
忘了声明」的插件只在安装之后才失败。该能力是**中风险**，它的 label 是运营在首次启用
确认弹窗里真正读到的那一行文案（弹窗只渲染 label，不渲染 description）。请把 label
写成自解释的（「写入插件事实计数」），细节放进你自己的插件描述里。

## 5. 事件转事实：`events[]` + 命令范式

核心没有「插件可订阅、并能代你写事实」的事件总线。受支持的范式是等价且显式的：
**声明一个事件的观察者，在处理函数里调用 Host 命令。**

```json
"events": [
  {
    "id": "acme.onboarding.event.topic-created",
    "contractVersion": "acme.onboarding.event.topic-created@1",
    "name": "topic.created",
    "kind": "observe",
    "handler": "acme.onboarding.event.topic-created",
    "inputSchema": "acme.onboarding.topic-created.input@1"
  }
]
```

```go
func (s *server) HandleEvent(ctx context.Context, req *hostv2.EventRequest) (*hostv2.EventResponse, error) {
	payload := req.GetPayload().GetValue().AsMap()
	authorID, _ := payload["authorUserId"].(string)
	topicID, _ := payload["topicId"].(string)
	userID, err := strconv.ParseInt(authorID, 10, 64)
	if err != nil || topicID == "" {
		// 无法归因的载荷不是事实：丢弃，不要猜。
		return &hostv2.EventResponse{}, nil
	}

	// 首选：SDK 封装。这里的 BadgeFactInput.UserID 是 int64，
	// 到线上才变成十进制字符串 userId（§9.1）。
	_, err = s.host.RecordBadgeFact(ctx, req.GetContext(), pluginv2.BadgeFactInput{
		UserID:    userID,
		FactKey:   "acme.onboarding.tasks",
		Variant:   "tutorial",
		Delta:     1,
		DedupeKey: "topic:" + topicID,
	})
	return &hostv2.EventResponse{}, err
}
```

需要先拿到请求对象（批量、入队、测试里断言形状）时用
`Host.RecordBadgeFactRequest`；上面的 `Host.RecordBadgeFact` 是构造 + 执行的便捷封装。

封装构造出来的**就是**下面这份 `TypedDocument` —— 同样六个字段，不多一个，也不从请求
上下文里推导任何东西：

```go
document, err := pluginv2.NewTypedDocument("sforum.badges.fact.record.input@1", map[string]any{
	"userId":    authorID,      // 线上是十进制字符串
	"factKey":   "acme.onboarding.tasks",
	"variant":   "tutorial",
	"delta":     "1",           // 十进制字符串；BadgeFactInput 里是 int64
	"dedupeKey": "topic:" + topicID,
})
if err != nil {
	return nil, err
}
requestContext := s.host.RequestContext(req.GetContext())
if requestContext == nil {
	return nil, errors.New("request context unavailable")
}
result, err := s.host.Commands.Execute(ctx, &hostwire.CommandRequest{
	Context:        requestContext,
	// 命令 ID 不带版本后缀，版本是独立字段。
	CommandId:      "badges.fact.record",
	CommandVersion: "1",
	Input:          document,
})
if err != nil {
	return nil, err
}
_ = result // inserted / total 在结果文档里，见 §4.2
```

两种写法都合法；除非你只构造不执行（入队、测试），否则优先用封装。

注意：

- `kind: "observe"` 的处理函数**异步**执行，缺省超时 5 秒。观察类事件是至少投递一次
  的，因此 `dedupeKey` 是幂等的真值位置 —— 用稳定的业务标识（主题 id、任务 id）派生，
  不要用时间戳。
- 处理函数要便宜。若事实需要派生计算（跨多行聚合），请入队 River 任务，在任务里写事实。
- SDK 助手是一对：`Host.RecordBadgeFactRequest`（构造请求）与
  `Host.RecordBadgeFact`（执行）。插件 SDK 里**没有 `Client` 类型**，入口永远是 `*Host`。

## 6. 运营侧怎么用

1. **安装并启用插件。** 启用时要确认声明的能力，包括 `badges.fact.write`。
2. **条件目录出现候选。** 后台条件目录返回核心条件清单，另加一个独立的
   `factCandidates` 数组。只有至少一个已启用插件声明了 key 之后，`fact.count`
   才会出现在条件清单里；没有候选时它被刻意隐藏 —— 空下拉只会让运营点进死路。
3. **建规则。** 选 `fact.count`，选 `factKey`，可选 `variant` 与窗口，再设比较值。
   保存前会给出预计影响人数，保存触发的回扫走后台任务。
4. **盯健康清单。** 已保存规则引用、但当前没有声明的 `factKey` 会作为失效引用列出。

### 6.1 停用 / 卸载 / 重装

| 状态 | 引用该事实的规则 | 求值 | 运营看到什么 |
| --- | --- | --- | --- |
| 插件已启用 | 不变 | 正常读计数 | 候选存在 |
| 插件**停用** | **原样保留** | 事实叶子解析为 **unknown** → 徽章不发（`Matched=false, Incomplete=true`）。不授予、也不静默收走。 | 健康清单报失效 key |
| 插件**卸载** | **原样保留** | 与停用相同 | 健康清单报失效 key |
| 插件用同一个 `key` 重装 / 重新启用 | **自动恢复** | 计数重新可读；不需要任何回填任务，因为候选是从当前已启用的包实时合并的 | 候选与健康条目都消失 |

规则是运营资产。这条链路上不会删除规则、不会把规则改写成 `not`、也不会摘掉叶子 ——
静默改写等于留下一次没有任何审计痕迹的篡改。唯一需要人工介入的情况是插件**改名
（rename）了 `key`**：旧 key 会永久失效，运营必须手动改规则；这正是本页把 `key`
定义为稳定标识的原因。

候选查询有几秒缓存，因此刚启用的插件最多要等这段时间才会进入求值；刷新失败时缓存
会**刻意保留上一次快照** —— 一次数据库抖动不应该让全站所有插件条件瞬间变成 unknown。

## 7. 常见坑

- **unknown 不是 false。** 缺失的事实解析为 unknown，所以在 `not` 下面同样不成立。
  若返 0，一个被卸载插件留下的 `lte 5` 规则会对所有用户成立，`not` 也会把失败翻成
  授予。
- **没有布尔形态。** 不存在 `valueKind` 字段。「是否发生过」用 `compare: ["gte"]`
  加 `cmp: {op: "gte", value: 1}`。加一个布尔形态等于同时增加一条求值分支和一个
  `not` 反转坑。
- **`delta` 是字符串。** 写成 JSON 数字会在 protobuf 载荷里丢精度。
- **`inserted: false` 是成功。** 那是 dedupe 在工作。
- **改 `key` 名是破坏性变更。** 请新增一个 key，让旧 key 落到健康清单，等运营自己
  迁移规则。

## 8. 参考

- Manifest 字段目录（生成）：[catalogs/manifest-v3.md](../../extensions/catalogs/manifest-v3.md)
- 能力目录（生成）：[catalogs/capabilities.md](../../extensions/catalogs/capabilities.md)
- Host 命令模型：[host-api-v2.md](../../extensions/host-api-v2.md)
- 事实存储实现：`apps/api/app/Models/Badges/facts_store.go`
- 条件注册表与事实候选：`apps/api/app/Models/Conditions/`
- 契约决策记录（内部）：`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`

## 9. Errata 3（契约澄清）

对本页首次发布的三处更正。以下表格**是规范**：代码、测试与运维手册逐字引用这些
字面量。

### 9.1 输入字段最终名

| 字段 | 线上类型 | 确切含义 |
| --- | --- | --- |
| `userId` | **十进制字符串** | 目标用户。行上是 `BIGINT`，跨进程用字符串传；SDK 的 `BadgeFactInput.UserID` 是 `int64`。 |
| `factKey` | 字符串 | 本插件自己的 `badgeConditions[].key` 之一。 |
| `variant` | 字符串 | 可选变体桶；空表示「无 variant」。 |
| `delta` | **十进制字符串** | 非零整数增量；负数是扣减。 |
| `occurredAt` | 字符串 | 可选 RFC 3339 时间戳；缺省取宿主时钟。 |
| `dedupeKey` | 字符串 | 可选幂等键；为空即关闭去重（§9.4）。 |

`userId` 与 `delta` 用字符串是同一个理由：命令载荷是 protobuf `Struct`，其数值类型是
`double`，64 位整数超过 2^53 会在那里丢精度。两者都不要写成 JSON 数字 —— 写了会得到
一次被拒的写入，或者一次静默写错的写入。

### 9.2 冻结的拒绝原因

| `reason`（逐字） | 含义 |
| --- | --- |
| `capability_denied` | 调用方没有 `badges.fact.write`。 |
| `fact_key_not_declared` | `factKey` 为空，或未由这个确切制品声明。 |
| `invalid_user_id` | `userId` 不是正整数的十进制字符串。 |
| `invalid_delta` | `delta` 不是非零整数的十进制字符串。 |
| `invalid_occurred_at` | `occurredAt` 存在但不是 RFC 3339。 |
| `fact_write_failed` | 写入未能记录（存储失败、写口未装配、声明集合无法解析）。 |

消费方可见的顶层错误码是 **`forum.badge_fact_write_denied`**（HTTP 403），
具体原因作为 `data.reason` 下发 —— 取值就是上表六个字面量之一。展示用顶层码的
本地化文案；分派用 `reason`。

注意 `fact_key_not_declared` 同时覆盖**空值与未声明**：所有权是按扩展的已声明 key
集合判定的，不是按字符串前缀。前缀只能证明命名空间，不能证明这个扩展真的声明过这条
事实。

### 9.3 审计行为按拒绝类别分裂

| 拒绝类别 | 审计行 |
| --- | --- |
| `capability_denied` | **没有。** 能力检查发生在共享命令闸门、且在事务**之前**，因此直接返回协议错误，不产生 `audit_events` 行。不要基于一条永远不会出现的审计行做告警。 |
| 域层拒绝（`fact_key_not_declared`、`invalid_user_id`、`invalid_delta`、`invalid_occurred_at`、`fact_write_failed`） | 落 `extension.badge_fact_record.denied`，metadata 含 `extensionId`、`factKey`、`reason`、`userId`（字符串形态）。 |

拒绝审计是**尽力而为**：审计写入失败不改变返回码。拒绝仍是拒绝，审计失败不会被升级
成宿主错误 —— 否则就会把「插件参数写错」变成「宿主坏了」。

### 9.4 去重是按 key 的，不是隐式的

- 非空 `dedupeKey` 命中部分唯一索引 `(user_id, fact_key, variant, dedupe_key)` 时，
  第二次调用不写入任何行，返回 `inserted: false`。这是**正常输出，不是错误** ——
  不要重试，也不要记成失败。
- **空** `dedupeKey` 等同于完全关闭去重：两次同样的调用会插入**两行**，累计值加两次。
  空串不是幂等键。

`dedupeKey` 请用稳定的业务标识派生（任务 id、主题 id、订单 id）。如果实在找不出这样
的标识，说明你上报的本来就是「再次发生」，那就该留空。

### 9.5 `total` 在回执里截断，在存储里不截断

`user_fact_counters.total` 允许为负（扣减可能超过用户在某窗口内的净增量），读面用
`GREATEST(total, 0)` 截断。命令回执重复同一处截断 —— `total` 永不为负 —— 但
**存储保留真实数字**。

于是先报 `+1` 再报 `-3` 的插件，第二次回执里看到 `total: "0"`，而存储里是 `-2`；
再来一次 `+1`，存储是 `-1`，回执仍然是 `total: "0"`。没有任何东西被吞掉：算术继续
从存储值推进，回执只是截断后的投影。
