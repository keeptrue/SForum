# Decision: observe 投递崩溃窗口不补 replay；队列化接线单独立项

## Status

Accepted（2026-11-17，安全审计 31 轮 F-4 判定）。判定「不补 replay、不建 payload 列、不加管理面重投
端点」；队列化接线（`Dispatcher` + `Pool` + worker 注册）判定为独立 decision，本轮不接。注释与台账
口径实现待派（老后）。

## Context

P3-9 的关闭口径已在台账里改写为：「四失败点全回滚 + 超龄 `queued`/`running` 由 10 分钟调度收成
`failed`（判据 `updated_at`），崩溃窗口内投递不可恢复但不再留永久中途行」。F-4 要判定的是那个
「不可恢复」是否可接受。

三件事先钉死（均已实读/实测）：

**① 事务分支生产不可达。** `ManagerConfig`（`apps/api/app/Support/Extensions/manager.go:32-45`）有
`Dispatcher` / `Pool` 两个字段，但生产装配只填 `Starter` + `DeliveryStore`：
`apps/api/bootstrap/app.go:73-79` 与 `apps/api/bootstrap/worker.go:344-350`。于是 `emitObserve`
（`event_jobs.go:142`）永远走 `m.dispatcher == nil` 的进程内分支，`:175-199` 的
`Begin → CreateEventDeliveryTx → EnqueueTx → Commit` 永不执行。

**② 队列路径连 worker 都没注册。** `RegisterEventDeliveryWorker`（`event_jobs.go:94`）全仓**零调用
点**（`rg RegisterEventDeliveryWorker` 只命中定义与 `event_jobs_test.go` 里直接构造的
`EventDeliveryWorker`）。所以「接上 Dispatcher」不是填一个字段：入队之后没有消费者。

**③ 账本无 payload 列。** `apps/api/database/migrations/202607060004_extension_event_deliveries.sql`
的列是 `id / extension_id / event_name / event_kind / status / reason / message / correlation_id /
attempt_count / created_at / updated_at / completed_at`，没有 payload。sweeper
（`app/Models/Extensions/deliverysweep/sweep.go`）只把超龄行改成 `failed`（`ReasonStaleSwept`），
不重投；调度在 `app/Support/Jobs/core_schedules.go:135-143`（10 分钟、maintenance 队列、阈值 15 分钟）。

### 业务后果：仓库内零个有副作用的生产订阅方

`HookBus.Listeners`（`app/Support/Extensions/hooks.go:386`）只返回清单里声明了该 hook 的扩展
（`declaresHook` 读 `manifest.Events`）。逐层筛：

- `extensions/builtin/plugins/` 八个内置插件里只有 `sforum-content-policy` 声明 `events`，且 `kind`
  是 `filter`（`sforum.extension.json:19-27`）——filter 走 `emitSync`，不进 observe 投递账本。
- observe 订阅方全部在 `extensions/fixtures/plugins/`：`sforum-badge-facts`（`reaction.created` →
  `badges.fact.record` 命令，**有真实副作用**：徽章事实计数）、`sforum-contract-events`（契约测试）。
  fixtures 是 CI 资产，不是生产功能。
- 宿主侧两个「看起来像订阅方」的东西其实不是账本消费者：出站 webhook
  （`app/Models/Webhooks/bridge.go:15-26`）与 AI 回复触发（`app/Models/AIReply/subscriber.go:20-52`）
  都是**发布者装饰器**，在 `Emit` 同步链上执行，不读 `extension_event_deliveries`。它们另有自己的
  丢失窗口（且 webhook 扇出只在 `result.OK` 时触发，见 `bridge.go:21`）。

结论：**今天没有任何仓库内的 observe 订阅方会因为崩溃窗口丢业务数据。**

### 更常见的丢失不是崩溃窗口，是插件没在跑

`RuntimeEventsProviders.Deliver`（`manager.go:628-648`）：`runningExtension` 找不到时立刻
`finishDelivery(..., DeliveryFailed, ...)` 返回 `extension.runtime_unavailable`，`attempt_count`
硬编码为 1，无重试。即插件进程没起来（重启中、被隔离、Safe Mode）时，它的 observe 事件**当场判
死**，不会留在 `queued` 等 sweeper。replay 救不了这一类：行里就算有 payload，插件当时仍不在跑，
重投只会再失败一次。

## Decision

**不补 replay（不建 payload 列、不加管理面重投端点），并把「崩溃窗口内不可恢复」写死为已接受取舍。
队列化接线单独立项，本轮不接。**

三条理由：

1. **仓库内受益面为零，无法验证正确性。** 仓库内没有生产 observe 订阅方（上节），replay 的唯一
   可测对象是 fixture。为一个没有消费者的通道新建一列含用户内容的持久化数据，收益是假设的，
   代价是确定的。（限定词是「仓库内」：仓库外已安装的第三方插件可能已经在 observe 通道上做审计，
   它们不在本决策的样本内，也不能用它们证明这条通道重要到值得加一列持久化 payload。）
2. **replay 的形态是错的。** payload 本来就在 `EventDeliveryArgs.Payload`（`event_jobs.go:43`）里，
   而 River 自己持久化 job args 并带 `MaxAttempts: 3`（`:56-62`）。要「可重投」，正确做法是让投递走
   队列（那时 payload 由 River 存、由 River 重试），而不是把 payload 复制一份进账本再手写一个重投
   端点。对齐 `Notifications.ReplayDelivery`（`app/Models/Notifications/postgres_store.go:465`）之所
   以成立，是因为邮件有 provider 幂等键与「用户可见承诺」；observe 是 fail-open 的旁路观测，两者
   的失败代价不同级。
3. **payload 敏感性。** observe 载荷含帖子正文片段、`actorUserId`、资源 id。宿主侧发布点集中在
   `Forum`（`service.go` 3 处、`bookmarks_service.go`、`reactions_service.go`、`service_ops.go`）、
   `Identity`（`service.go`、`external_registration.go`）、`TrustLevels/recompute.go`、
   `Attachments/upload.go` —— 非测试 Go 文件里 `rg KindObserve` 共 23 行命中，其中 10 行是发布点，
   其余是常量、catalog 与守卫。把它落进账本，就同时要定保留期、脱敏规则与访问控制（现有
   `GET /admin/extensions/event-deliveries` 由 `core.guard.extensions.read` 守），这是一整套新数据
   面，不该作为「补个 replay」的附带产物。

因此注释与台账要显式写下「未接线」：

- `event_jobs.go:15-36` 的装配说明与 `:168-174` 的 `delivery_atomic_unavailable` 分支标注「**当前
  生产未接线**：`ManagerConfig` 无 `Dispatcher`/`Pool`、`RegisterEventDeliveryWorker` 零调用点；
  `:175-199` 的事务路径与本节均为将来队列化预留」。
- `deliverysweep/sweep.go` 的包注释补一句与台账同口径的话：「崩溃窗口（建行后、写终态前进程死亡）
  内的事件不可恢复；sweeper 只收口状态，不重投 —— 已接受取舍，见
  `knowledge/decisions/2026-11-17-observe-delivery-crash-window.md`」。

## 队列化接线：独立 decision，触发条件与代价先写下来

不接的理由是它**改变行为语义**，不是做不到。接线点已经齐：`jobDispatcher`
（`bootstrap/api_assembly_infrastructure.go:313`）与 `pool` 都在 `api_assembly_extensions.go` 作用域
内，`JobDispatcherAdapter`（`event_jobs.go:100`）与 `*pgxpool.Pool`（满足 `TxBeginner`，
`manager.go:159-162`）都是现成的。

要接必须同时满足四条，缺一条就会造出「行与 job 都在、但没人处理」的新坑：

1. **worker 侧注册** `RegisterEventDeliveryWorker`（`event_jobs.go:94`，目前零调用点），否则入队的
   job 永远停在 available。
2. **API 与 worker 两处装配都要填** `Dispatcher` + `Pool`（`bootstrap/app.go:73`、
   `bootstrap/worker.go:344`）。只填一边，另一半进程会继续走同步分支，形成两套语义。
3. **接受同步→异步的语义变化**：`Emit` 返回 `OK` 时插件还没跑（现在是跑完才返回）；observe 的顺序
   保证从「发布顺序」变成「River 调度顺序」；`MaxAttempts: 3` 让同一个 observe 最多投 3 次（当前
   `attempt_count` 恒为 1）。依赖 observe 做「恰好一次」的第三方插件会因此改变行为。
4. **重新定义去重键**：`:60` 用 `Unique.ByArgs: true`，而 args 含 `DeliveryID`（每行唯一）——队列化
   后这层去重实际不起作用，不重新定义就是含糊的重投语义。

触发条件（任一成立再开独立 decision）：仓库内或官方插件出现有真实副作用的 observe 订阅方；或运维
报告 observe 丢失造成可观测的业务后果；或插件 SDK 承诺 observe 至少一次投递。

## Consequences

- **F-4 关闭**：不需要 payload 列、不需要重投端点。台账里「要 replay 就得给 delivery 行加 payload
  列」那句应改为「已判定不补 replay，理由见 decision」。
- **`delivery_stale_swept` 的语义要在管理面写清**：被 sweep 的行**确定没投出去**（与邮件
  `stale_swept` 可能已发出相反，见 `Notifications/postgres_store.go` 的 `StaleDeliveryThreshold`
  注释），所以它是一条「这里丢了一个 observe」的诚实信号，不是噪声。文案属前端工单，不在本记录范围。
- **`runtime_unavailable` 是比崩溃窗口更大的洞**：它当场判死且不重试。真要提升 observe 可靠性，
  优先级在「插件不可用时的投递缓冲」，不在「崩溃窗口 replay」。这条留给将来，不新开编号。
- **本记录不改生产代码**。`event_jobs.go:15-36` 现在读起来像「有队列就原子」，实际生产没队列 ——
  这是最容易误读的一处，实现落地时务必改掉。
- **行号会漂**。本记录行号取样于 `6f577659e`；判据一律以「文件 + 符号名」为准，行号只作导航。
