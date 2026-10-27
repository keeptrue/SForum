# 2026-10-27 Recovery-only 的显式退出通道

## 背景

07:25:44 生产日志出现 `plugin runtime convergence failed; entering Host recovery-only mode`
（publication revision 934），随后全站 503 `service.recovery_required`。当时唯一的退出手段
是重启 API 进程（07:51:40 air 重启后自动恢复 200）。

`health.RecoveryRequirement` 原先的契约是 **immutable after bootstrap**：进入 recovery-only
后协调器被 `stopPluginRuntimeCoordinator()` 停掉，503 由 `effectiveSafeMode` 与 ready 检查
共同决定，进程生命周期内不会再变。这个契约的本意是 fail-closed——真实故障不许被观测面藏起来。

代价是：一次已经修好的故障（运维把非法制品 disable/quarantine 掉之后）仍然要求重启进程，
而重启会中断所有在途请求，并且没有留下"谁在什么时候要求重试"的审计。

## 决定

引入一条**显式、单路径、可审计**的退出通道，把"重启"换成"显式 recheck + 首轮收敛成功"：

1. `sforum recovery recheck --reason <...>` 从 durable 行重建 desired full set，与最新不可变
   publication 精确比对；一致则推进一条 `reason='recovery'` 的新 revision，不一致则原样打印
   失败制品清单并非零退出且不写库。
2. （第二阶段）API 在 recovery-only 状态下按退避轮询 `plugin_runtime_publications`，命中后重跑
   一次收敛；收敛成功才撤销 recovery-only 并写审计。

## 当前阶段边界（第一阶段，已实现）

**recheck 不热恢复。** 它只做"重新校验 desired set + 推进 revision"，把"该重新收敛了"写成
durable 事实；生效点仍然是**进程下一次启动**。运行中的进程不会因为执行了 recheck 就退出
recovery-only，503 会一直保持到重启后首轮收敛成功为止。CLI 的成功输出必须明确写这一点
（`this command only advances the revision` / `the Host applies it at the next process start`）。

**热恢复是第二阶段，前置条件写死**：必须先有能在隔离环境复现"完整 API 从 recovery-only
恢复到 200"的 e2e 夹具，且断言 200 形态下的 provider 集合与路由集与冷启动一致，才允许动
ready 读取点（`RecoveryRequirement` 改成可变 holder）与二次 `RestoreRoutePublications`。
二次恢复本身是允许的（它是"重启等价"的必要条件），但必须配幂等证明。

**为什么先停在第一阶段**：只关 503 而不重跑路由恢复，进程会以 200 的形态对外服务
`effectiveSafeMode=true` 时期恢复出来的安全模式路由集——一个"看起来好了但 provider/路由集
是错的"的 fail-open 状态，比继续 503 难查得多。宁可让运维多点一次重启，也不要把已知的
fail-closed 换成未知的 fail-open。

第一阶段的退出闸门（`bootstrap/pluginRuntimeRecoveryExitGate`）已经实现并测试：它把三条约束
落成纯逻辑，供第二阶段接线直接复用，不构成对当前 503 路径的任何行为改动。

**为什么必须可变**：把"重启"改写成进程内动作，等价于承认 `RecoveryRequirement` 必须是
可撤销的观测面。不可变契约无法表达"同一进程内故障已被显式修复"这一状态转换。

## 撤销的唯一路径（三条硬约束）

1. **只认一个来源**：`reason='recovery'` 且 `revision >` 进入 recovery-only 时那条 publication
   的 revision。人工改库、启动重放、admin 端点、任何其它 reason 一律不得触发撤销。
2. **收敛成功才撤销**：发布只是触发；撤销条件是重建后的协调器首轮 ready 通过。
3. **同一 revision 只能撤销一次**：消费过的 revision 永久作废。撤销后若再次收敛失败，重新
   进入 recovery-only，且必须等一次**新的** recheck 才能再次撤销——没有这条，间歇性失败的
   扩展会让 503/200 反复横跳，比不退出更糟。

撤销本身写审计：`actor=sforum-cli`、`revision`、收敛证据、`reason` 原文。撤销失败或未撤销时
一律保持 503，不放行。

## 不做什么

- 不做自动退出：desired 集合合法也必须有显式 recheck 这个人类动作。自动退出等于把真实故障
  藏起来，这正是 recovery-only 存在的理由。
- 不给 admin 端点开通道：HTTP 层的任何入口都可能被 CSRF/越权利用，带外 CLI 才是运维口径。
- 不把 `plugin_runtime_publications.actor_user_id` 编成一个假账号：schema 允许 NULL，CLI 是
  Host 级动作，NULL + 审计 metadata 比编造 id 更诚实。

## 兜底

- 撤销前收敛失败 → 保持 recovery-only，记录失败原因，不降级为放行。
- （第二阶段）轮询只读 `plugin_runtime_publications` 的 revision 与 reason（不扫全表），断连按
  退避重试。
- 进程重启后重新进入 bootstrap：recovery-only 判定与退出通道各自独立生效，不依赖上一次进程
  的内存状态。第一阶段下这就是唯一的生效路径。

## 影响面

- `health.RecoveryRequirement` 的 "immutable after bootstrap" 契约被本次决定替换为
  "可撤销，但只经上述单一路径"。这是架构级变更，故记录于此。
- 代码落点：`apps/api/cmd/sforum/recovery_recheck.go`（CLI）、
  `apps/api/app/Models/Extensions/plugin_runtime_recovery_postgres.go`
  （`RecheckPluginRuntimeRecoveryTx`）、`apps/api/bootstrap/`（撤销闸门与轮询）。
