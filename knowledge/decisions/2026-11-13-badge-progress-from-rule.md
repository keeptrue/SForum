# 决策：徽章进度从**规则**派生（含对 D37 的显式修订）

日期：2026-11-13
状态：accepted（修订 `decisions/2026-11-11-badges-public-catalog-and-series-closeout.md` 的 **D37**，
该文件原文未改动，只加了一行指向本文件的索引）
上游：`decisions/2026-11-12-badge-rule-engine.md`（`badges.rule` 是自动徽章的唯一判定来源）、
`decisions/2026-11-11-badges-public-catalog-and-series-closeout.md`（D37）

## 背景：进度条曾经读的是 legacy 两列

`progressFor(badge PublicBadge, counters ActivityCounters)`（HEAD `app/Models/Badges/service.go:88-96`，调用在 `:77`）
按 `badge.Metric` / `badge.Threshold` 与 `user_activity` 的六项计数算一格进度。
规则引擎落地后 `badges.rule` 成了**唯一**判定来源，`metric` / `threshold` 降为 legacy 只读
（纯规则徽章这两列是 NULL），于是这个函数给出的是「键在值空」的进度条：
`0/0` 或者一个与真实阈值无关的数字，前端照它画一条永远不满的空条。

同时它还有两个结构问题：`ListUserBadges` 为了算进度必须多读一次 `ActivityCounters`；
而目录页 `/badges` 完全没有进度（只有 `earned`）。

## 裁决

### D1 `PublicBadge.Measure` 由 `Public()` 从规则派生，`json:"-"`

`ProgressMeasure{Leaf, Threshold}`（`progress_measure.go`）是内部结构，
`PublicBadge.Measure *ProgressMeasure` 带 `json:"-"`：**绝不进公开响应、也不进插件 schema**。

`measureForRule(root)` 的接受条件恰好四条，缺一条就返回 nil：

| 条件 | 为什么 |
| --- | --- |
| 根是**单叶**（`!IsGroup() && IsLeaf()`） | 一个进度条无法表达合取或析取；求和或取最大都会给出错误的「还差多少」 |
| 条件目录里是 **count 型**（`ValueKind == ValueCount`） | 布尔条件没有可比的数值 |
| `cmp.op == "gte"` | 进度条的语义是「还差多少」，只有「达到某条线」可表达；`lte`/`lt`/`eq` 表达不了 |
| `cmp.value > 0` | `0` 阈值没有「差多少」，`0/0` 会把「判不了」伪装成「0%」 |

放在领域结构里而不是 HTTP 层现算，是为了让「能不能测」在投影时就定下来，
HTTP 层不必再理解一遍规则 AST。`Public()` 是唯一派生点（`types.go:153`）。

**这里不拼自然语言**：公开文案的唯一实现是 `requirement_text_render.go`。
两套文案必然漂移，而且它输出的是句子、不是测量输入。

### D2 `ProgressMeasurer` 是**窄接口**，签名里没有 error

```go
type ProgressMeasurer interface {
    MeasureProgress(ctx context.Context, userID int64, measures map[string]ProgressMeasure) map[string]BadgeProgress
}
```

没有 error 是刻意的：进度是装饰，测量失败只该让对应徽章没有进度条，不该让整个资料页/目录页
500。未装配（nil）时同样静默留空。`Service` 本身没有 pool、也不该有——测量是存储层的批量查询。

装配走 `Service.SetProgressMeasurer`（`bootstrap/api_assembly_domains.go:646`），
`NewService(store, activity)` 的签名与字段保持不变（外部装配与既有测试仍按该签名构造），
但 `activity` **不再是徽章进度的来源**。

### D3 一次 `Prepare`，每类条件一条 SQL；单类型失败 fail-soft

`(*PostgresStore).MeasureProgress` 把待测徽章的叶子先合成一个临时 `all` 组，
`resolver.Prepare(ctx, root)` 按条件类型分组、每类一条 SQL，然后逐徽章 `Resolve` 读 memo。
同类型的十几枚徽章共享一次往返，**禁止按徽章逐条查**（那就是 N+1，目录页一次要测十几枚）。

失败语义：

- 单类型查询失败 → 记在 `Prepare` 的 groupErrs 里 → `Resolve` 把错误还给调用方 →
  **只有该类条件的徽章没有进度**；
- resolver 建不起来（pool 为 nil / userID 非法 / ctx 取消）→ 返回空 map，不逐条重试
  （那会把一次失败放大成 N 次往返）。

slug 先 `sort.Strings` 再拼树：同一批徽章每次都以相同顺序拼树，SQL 参数与日志可复现。

### D4 `ListUserBadges` 不再读 `ActivityCounters`，且**一律覆盖** `Progress`

```go
measured := s.measureProgress(ctx, userID, userBadges)
for i := range userBadges {
    userBadges[i].Progress = measured[userBadges[i].Badge.Slug]
}
```

「一律覆盖」是裁决：没有测量结果的行必须是**零值**。留任何 legacy 阈值都会让 `0/7` 这种
假进度条出现在规则徽章上——这正是本仓的历史坑。

只测「未获得 + 有 `Measure`」的徽章：已获得的行 DTO 根本不输出进度键，测了只是白跑。

### D5 目录页新增 `ViewerCatalogProgress`：只对「未获得 + 自动 + 可测」下发两个数字

`GET /api/v1/badges` 的条目新增 `current` / `threshold`（`*int64` + `omitempty`）：

- 只在 `earned == false` 且 `threshold > 0` 时出现；
- `current: 0` 是**有效值**，照常下发，不是省略；
- 匿名（`userID <= 0`）返回空 map 且**一次库都不查**；
- 签名里没有 error（同 D2）。

一次 `ListUserBadges` 同时拿到「启用徽章 × 该用户」的规则与 earned，不需要再单独读一遍 earned。
缓存头仍是 `private, no-store`（`setViewerScopedNoStore`）。

wire 形状有值级断言钉住（`badge_progress_shape_test.go`）：`progress` 只有 `current`/`threshold`
两个数字键；没有测量结果时**不产出** `mode` / `satisfied` / `total` 之类的新键。

## 显式修订 D37

D37 的裁决本身（`GET /api/v1/badges` 的 `Cache-Control` 必须是 `private`，禁止 `public`）**不变**，
而且现在更强：响应里 actor 相关的面从「`earned` 一个字段」扩到了「`earned` + 进度」。
本文件修订的是 D37 的**隐含前提**，以及由它衍生的一条旧口径。

**被修订的三条**

1. **D37 的表格里「`GET /api/v1/badges` 返回**查看者自己**的 earned」这句不再完整。**
   现在返回的是 earned **与** 进度。结论方向没变（`private, no-store` 是唯一正确的级别），
   但「actor 相关的只有 earned」不能再被当成事实引用——按这句话做缓存决策的人会低估泄露面。
2. **`PublicCatalogEntry` 的注释「阈值是运营调参，公开即不可撤回」（`public_catalog.go:13-15`）
   被收窄，不再作为「阈值一个数字都不能下发」的依据。**
   新的口径是：**阈值的「结构」不公开，阈值的「读数」可以**。
   - 不公开：`rule` AST、`metric` / `threshold` 列、`cmp` / `params` / `children` / `op`。
     这些一旦进公开响应就变成事实上的公开契约，且无法撤回。
   - 可公开：`current` / `threshold` 两个数字，且只在「未获得 + 自动 + 可测」的条目上。
3. **「目录页只给 earned」不再是产品口径。** 目录页现在回答「我还差多少」，
   与资料页徽章墙同源（同一个 `measureForRule` + 同一个测量器）。

**为什么这不构成新的运营信息泄露**（两条，缺一不可）

- **阈值本来就已经以自然语言出现在 `requirementText` 里**。公开面早就写着
  「累计发表主题 ≥ 10」——数字已经在页面上。把同一个数字从句子搬到进度条上，
  不增加任何新的运营信息；不搬反而让用户在两处读到不一致的口径（一个能算、一个不能算）。
  换句话说：**这次下发的是「已经公开的数字的一个更可用的形式」，不是新数字。**
- **响应本来就是 `private, no-store`**（D37 的裁决）。进度是查看者相关的，
  它进不了任何共享缓存；`Vary` 也不参与正确性论证——`no-store` 是零假设方案。

**为什么不是「把阈值藏起来只给百分比」**：百分比需要同一个分母，而分母就是阈值；
给百分比等于给 `current/threshold` 的比值，反推一次就得到阈值。真正要藏的东西
（规则结构、运营调参意图）本来就藏住了；藏一个能反推的数字只会让前端多一次猜。

**仍然不做的事**

- 不给 `lte` / 布尔 / 多条件组编一个「进度」。那不是「暂时没做」，是**语义上不该有**：
  一个进度条无法表达合取或析取，硬给会让用户以为「再发 3 帖就拿到」而实际条件在别处。
- 不把 `Measure` 下发进插件 schema / 稳定视图 / HostQuery。插件读的是授予结果与事实累计，
  不是「Core 怎么算进度」。
- 不改既有 13 条徽章端点的缓存头（D37 的同一条裁决）。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| 保留 `progressFor(badge, counters)`，只把 legacy 列填回去 | 规则是唯一判定来源，legacy 两列在纯规则徽章上是 NULL；回填等于造第二份真值 |
| 在 HTTP 层解析规则算进度 | 每个 handler 都要理解 AST；`Public()` 是唯一投影点，进度必须在那里定型 |
| 按徽章逐条查库算进度 | 目录页一次十几枚 → N+1（D3） |
| 测量失败上抛 | 进度是装饰，一次抖动会让整个资料页 500 |
| 不可测的徽章给 `0/0` | 把「判不了」伪装成「0%」，前端会画一条空进度条 |
| 目录页只给百分比 | 反推一次就是阈值；且百分比需要一个前端不认识的分母 |
| 给 `any` / `all` 组求和或取最大当进度 | 会给出错误的「还差多少」，用户按它努力却拿不到徽章 |
| 把 `Measure` 放进 `PublicBadge` 的 json | 规则结构/阈值意图会变成公开契约，且不可撤回 |
| 目录页为了「省一次查询」复用 `earned` 的读 | `ListUserBadges` 一次就同时给出规则与 earned，多读一次是纯浪费 |

## 陷阱（现象 → 根因 → 判据）

**T1 进度条永远 0% 或 0/0**
现象：规则徽章上出现 `0/0`，或进度条恒空。
根因：读的是 legacy `metric`/`threshold`（纯规则徽章为 NULL），或测量器没装配而
`ListUserBadges` 保留了旧 `Progress`。
判据：`ListUserBadges` 必须**一律覆盖**（D4）。断言要构造「规则有阈值、legacy 列为 NULL」的
夹具，并同时验「未装配测量器时零值」——只验有测量器的那条会漏掉装配缺失。

**T2 `lte` 规则被画成进度条**
现象：运营配了「发帖数 ≤ 5」的徽章，进度条显示 3/5，用户以为再发 2 帖就拿到（实际是相反）。
根因：`measureForRule` 只判了 `ValueCount` 没判 `gte`。
判据：`TestMeasureForRuleOnlyAcceptsSingleCountGTE` 的 14 个用例里，`lte`/`lt`/`eq`/`gte zero`
必须全部返回 nil；**拒绝路径与放行路径都要验**。

**T3 目录页匿名也查库**
现象：未登录访问 `/badges` 时多了一次 `user_badges` 查询（或更糟：400/500）。
根因：`ViewerCatalogProgress` 的匿名分支写在 `store` 之后，或 controller 把进度算在
`viewer` 判断之外。
判据：匿名时 `ViewerCatalogProgress` 返回空 map **且不触达 store**（用「store 会 panic 的
fake」断言没被调用）；controller 侧进度只在 `if actor, ok := h.viewer(c)` 分支内。

**T4 已获得的行也带进度**
现象：徽章墙上已获得的徽章下面多一条满格进度条。
根因：`measureProgress` 没跳过 `Earned`，或 DTO 没判 `granted`。
判据：两侧都要拦——领域层跳过 `Earned`，DTO 在 `!granted` 才写两个键。只拦一侧时
另一侧会在重构中失守。

**T5 「按类型失败」被做成「整体失败」**
现象：标签类条件查询抖一下，目录页所有徽章的进度全没了。
根因：`Prepare` 的 groupErrs 被当成整体失败上抛，或 `MeasureProgress` 在第一个
`Resolve` 出错时 `return`。
判据：注入「单类 SQL 失败」后，其它类型条件的徽章仍有进度（fail-soft，与
`refCandidates` 的「单类 unavailable」同一姿态）。

**T6 用 `requirementText` 反解进度**
现象：有人为了「省一次测量」从公开句子正则里抠数字当阈值。
根因：句子是给人读的，含压缩与降级（「满足全部 N 个条件，例如…」），数字不一定出现。
判据：进度只认 `Measure`（结构），句子只认 `RequirementText`（文案），两条链路永不互读。

## 复核点

1. 新增条件类型时，若它是 count 型，`measureForRule` 会自动接受它——**先确认它的语义确实是
   「达到某条线」**，否则要显式排除（`lte` 型条件就是靠 op 判据被挡住的）。
2. 任何把 `Measure` 塞进 wire/schema/视图的改动都要回到 D1 与「显式修订 D37」。
3. 改 `/badges` 的缓存头之前先读 D37：`private, no-store` 是正确性前提，不是性能选项。
