# 决策：信任等级的条件求值走「合成根只 Prepare」，逐档 Eval 复用 memo

日期：2026-11-14
状态：已采纳（M2 落地，M5 收口前补记决策记录）

## 背景

条件求值的入口是 `conditions.Resolver`（`app/Models/Conditions/batch.go:53` 的 `NewResolver`）。
它的 `Prepare` **不读 memo**：每调用一次就按条件类型各查一遍
（`app/Models/Conditions/batch.go:78-104`，`memo` 只在 `Resolve` 时命中，`:115`）。

信任等级一次装载要判定**多个档位**，每档可能有自己的一棵规则树。若逐档 `Prepare`，
同一个条件类型会被查 N 次（计划 E6 记的就是这件事）。而 `conditions.Resolver` 的公共面已经验收，
为信任等级改它的形状要动一个跨域的已验收接口。

## 裁决

1. **合成根只 `Prepare` 一次。** `TrustLevels` 侧把「本次将要判定的那几棵规则树」拼成一棵
   `all` 根，只调用一次 `eval.Prepare(ctx, pending)`
   （`app/Models/TrustLevels/level.go:87`），随后逐档 `Eval` 各自那棵树，命中同一份 memo
   （`app/Models/Conditions/batch.go:115`）。这条契约写在 `RuleEvaluator` 接口注释里
   （`app/Models/TrustLevels/rule_eval.go:34-42`：调用方必须把所有可能被求值的树一次传进来）。
2. **合成根只装「数值预筛已过 且 带 rule」的档位。** 入参在预筛循环里累积
   （`app/Models/TrustLevels/level.go:76-88`），未过数值门槛的档位连 `rule` 都不会被放进去。
3. **合成根不进入任何返回值或字段。** 它是 `levelOfWithRules` 内的局部切片（`app/Models/TrustLevels/level.go:77`），
   `RuleEvaluator` 的接口只有 `Prepare` / `Match` / `LeafConditions`（`app/Models/TrustLevels/rule_eval.go:34-42`），
   没有任何方法把合成根交回调用方；公开面 DTO 里也根本没有规则字段
   （`app/Http/Controllers/TrustLevels/dto.go:36-53` 的 `publicLevelDefinition` 字段全集）。
4. **合成根只走 `Prepare`，`Validate` 的适用对象是单条规则。** `Prepare` 按 type 分组建查询、
   **不做任何上限检查**（`app/Models/Conditions/batch.go:78-104`）；上限检查在 `Validate` 里
   （`app/Support/RuleEngine/validate.go:17-18` 的 `MaxLeaves = 32` 与 `:163` 的超限判定）。
   两者服务的是不同对象：`MaxLeaves` 判「**一条**规则是否合法」，合成根是「一批规则的并集」，
   对合成根跑 `Validate` 等于把 32 叶上限套到「全站所有档位叶子之和」上。

## 被否方案

**按「所有带规则档位」建合成根。** 那样数值本轮根本不过的用户也会触发全部条件查询：

- 数值快筛失效：`app/Models/TrustLevels/level.go:79-81` 的预筛就白写了；
- §5 的成本表（无规则 0 条 / 数值未过 0 条 / 实际判定才按 type 查）整体失效；
- 验收矩阵第 1 条失去判据——它断的就是「该档数值不过时，该档独有的条件类型**一条 SQL 都不产生**」
  （`app/Models/TrustLevels/rule_eval_postgres_integration_test.go:275`
  的 `TestNumericFailureSkipsUniqueConditionType`，以及无库版
  `app/Models/TrustLevels/level_rules_test.go:223` 的 `TestNumericFailureSkipsRuleEvaluation`）。

**给合成根加 `Validate` 与「合计上限」。** 那会凭空造出第 7 个错误码和一条运营永远碰不到的失败路径：
运营修一条规则时，能做的只有「删条件/减嵌套」，而「一档合法、合起来不合法」这个状态无法归因到
任何一条规则，运营拿不到可执行的动作。`MaxLeaves = 32` 的语义（`app/Support/RuleEngine/validate.go:17-18`）
本来就锚在「单条规则」上。

**把合成根作为诊断信息暴露出去。** 需要「本档准备过哪些 type」时，应该给一个显式的诊断结构；
合成根是求值实现的中间产物，它的形状随实现变，暴露出去就等于把实现细节变成契约。

## 为什么「外部拿不到合成根」由编译器保证，而不是测试保证

合成根作为局部变量存在于 `levelOfWithRules`（`app/Models/TrustLevels/level.go:77-88`），没有字段、没有返回值、
没有导出类型承载它；要把它拿出去，必须先改类型或改接口签名，那一步在编译期就会拦住，
并且必定过一次 review。

测试能断的是**效果**，不是「结构不会被改坏」：公开面不出现规则 AST 有 `TestPublicPayloadNeverCarriesRule`
做值级断言（`app/Http/Controllers/TrustLevels/rule_controller_test.go:95`），数值不过时不产生查询有
上面那两个测试。这类断言负责发现回归，类型边界负责让回归写不出来。

## 降回条件

满足任一条即可重新设计这一段（届时本记录作废，需新记录）：

- `conditions.Resolver` 的公共面提供 `PrepareAll` 或 memo-only 的批量入口：合成根这套拼接可以退休，
  直接调新入口，少一层「拼了再拆」的转换。
- 出现「必须逐档校验规则」的需求（例如按档的求值预算、按档的错误归属）：那是求值预算模型的问题，
  要重新设计预算与错误归因，**不要**给合成根加 `Validate`——加了之后「合计超限」会成为新的失败状态，
  而它无法指向任何一条具体规则。
- `MaxLeaves` 的语义改成「一次求值内所有叶子的合计上限」：那时合成根就必须参与校验，
  且成本表要按「合计叶子数」重算，§5.1 的证据一并重做。

## 后果与已知取舍

- **拼接成本只落在有用的档位上**：无规则站点 `pending` 为空，`Prepare` 根本不调用
  （`app/Models/TrustLevels/level.go:86-88` 的 `len(pending) > 0` 闸）；数值不过的档位不参与拼接。所以这笔成本与
  「有规则且判到」成正比，不是与档数成正比。
- **一个 type 查询失败只影响引用它的档位**：`Prepare` 把错误按 type 记下（`app/Models/Conditions/batch.go:93-96`），
  对应叶子在 `Resolve` 时返回 error，引擎转成 unknown → 该档 fail-closed，整次求值不会失败。
- **跨档复用靠「一次装载内共用同一个 resolver 实例」**：memo 的生命周期是 `conditions.Resolver` 的
  生命周期（`app/Models/Conditions/batch.go:99` 写入、`:115` 命中）。谁把 resolver 拆成每档一个，memo 就退化成每档一份——
  这正是接口注释（`app/Models/TrustLevels/rule_eval.go:34-42`）要求「一次传全部树」的原因。
