# 决策：HostQuery 增加「计算形态」（2026-11-14）

## 背景

M4b-3 要给插件两条公开读面：

- `sforum.core.public_trust_level_catalog.list`（信任等级档位目录）
- `sforum.core.public_user_trust_level.get`（单用户当前等级）

而 HostQuery v2 的定义形状（`protocolV2QueryDefinition`）只有 SQL 一种：`From` /
`Fields{Name, Expression}` / `Filters{Expression}` / `Sorts`，执行器一律建
`SELECT … FROM <view>`。这两条都不成立：

1. **档位目录没有表**：真源是 `web_options['trust.levels']` 单键 JSON，解码后才得到
   档位数组（名字/图标/颜色/六个数值门槛）。
2. **单用户等级必须现算**：等级是「行为统计 + 策略 + 条件规则」的派生值，唯一权威是
   `trustlevels.Resolver.Evaluate()`。变更账本 `public.user_trust_levels` 只是快照
   （迁移注释写着 NOT authoritative），读它就是「库里 2 档、现算 3 档」的双真相。

## 决定

给 `protocolV2QueryDefinition` 增加一种**计算形态**：

```go
Compute func(ctx, plan, deps) ([]map[string]any, error) // json:"-"，不进 catalog digest
```

- 计算函数**只负责取值**（字段名 → 未归一化值）；
- 归一化（`normalizeProtocolV2QueryValue`）、按 `Fields` 投影、`Offset`/`FetchLimit`
  截断、过滤条件校验、错误码、trace、结果 schema 校验**全部复用 SQL 侧同一份实现**；
- 计算定义不需要 `From`/`Expression`，但字段名仍然必须唯一非空（`schema.properties`
  与 `Fields` 等长那条断言对它同样成立）；
- 未注入读面时返回错误而不是空表——「注册了但没装配」必须是显式失败。

## 为什么不选「按 query ID 预分派」

预分派（在 `execute()` 里命中两条 ID 就直接构造响应）代码更少，但它会**多出第二条
造响应的路**：分页、过滤校验、schema 校验、trace、错误码各写一遍，两边早晚分叉。
本仓反复出现的就是这类「两份」。宁可给引擎加一种形态，也不给响应装配加一个入口。

## 降回条件

若将来出现**第三条**计算查询，先重新评估它是否其实该做成 Provider 槽（
`search.provider` / `mail.provider` 那种由扩展提供的读面），而不是继续往 Core
加计算定义；若计算定义开始需要自己的分页/缓存/权限语义（不再能复用共享实现），
同样说明这个形态被误用了，应改为 Provider 槽 + 数据表。

## 已知取舍

- 计算查询的过滤条件只有 `RequiredFilters` 语义可用，不能像 SQL 那样下推到视图；
  单用户面靠 `user_id` 必填 + 读面显式取该值实现。
- `public_user_trust_level.get` 用 `state ∈ {resolved, incomplete, disabled}` 表达
  「算不出来」；`incomplete`/`disabled` 时 `level`/`tier_id` 为 `null`。不能用 0
  冒充——「等级是 0」与「求不出来」在调用方那里必须可区分。
