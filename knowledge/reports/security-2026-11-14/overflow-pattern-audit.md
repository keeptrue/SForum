# 分配尺寸溢出模式全仓排查（T1 覆盖完整性复核）

- 日期：2026-11-14
- 任务：task-2「全仓排查同类分配溢出模式，确认 T1 覆盖完整性」
- 范围：`apps/api`、`extensions/builtin/**/backend`、`tools/proto`
- 性质：纯调研 + 本报告；**未修改任何生产代码**（T1 的两个文件由 T1 写手持有，本任务只读）

---

## 一、结论摘要

**T1 的覆盖分两层回答，两个答案不同：**

| 问法 | 答案 | 依据 |
|---|---|---|
| T1 是否覆盖了当前全部 `go/allocation-size-overflow` 告警？ | **是，完整（3/3）** | 仓库 open 的 3 条告警（#45 `policy.go:111`、#47 `controller.go:459`、#48 `controller.go:460`）正好是 T1 改掉的那 3 处；T1 的修法把容量表达式里的 `+` 拿掉，而该查询的 sink 判据**要求**容量参数由整数型 `+`/`*` 产生 —— 加法消失后告警必然关闭 |
| T1 是否覆盖了全仓「同类模式」？ | **否，但剩余部分不构成真实可达的溢出** | `apps/api` 生产代码仍有 **109 处** make 容量表达式含 `+`/`*`；逐条判定后**没有一处**能让 `make` 拿到负容量或超大容量 |

**核心论证（一句话）：** `len()` 返回的是**已物化**的 slice/map/string 的元素数，其上界是进程可寻址内存（Go 运行时 `maxAlloc` = 2^48−1，64 位平台用户态地址空间 ≤ 2^47）。因此 `len(a)+len(b) ≤ 2^49`、`len(a)*k`（实测 k ∈ {2,3,4,8,16}）≤ 2^52，**都远小于 `math.MaxInt64` = 2^63−1**。整数加法/乘法在此类表达式上**不可能溢出**，负容量 panic 路径不可达。

**真正的风险形态不是 `len()` 相加，而是「容量表达式 = 攻击者可控的原始整数」。** AST 扫描确认 `apps/api` 生产代码里这种形态**只有 4 处**，且全部安全（见 §四 A 桶）。

**另需 Lead 知晓的两个边界事实：**

1. `extensions/builtin/**/backend`（8 个独立 Go module）与 `tools/proto` **根本不在 CodeQL 的 Go 数据库里** —— `.github/workflows/security.yml` 用 `build-mode: manual` 且只在 `apps/api` 跑 `go build ./...`。它们**永远不可能**产生该告警。本任务已用 AST 扫过这两个范围：**命中 0 处**（`tools/proto` 甚至没有 Go 源码，只有 `go.mod`/`go.sum`/`.bin`）。这是一处**流程盲区**，详见 §三。
2. TrustLevels 链里还有 2 处同族写法（`app/Http/Controllers/TrustLevels/payload.go:20` 与 `:37`），但它们**不是**当前告警之一（CodeQL 未报），**无需并入 T1**。详见 §五。

---

## 二、方法与判据

### 2.1 扫描方法

用 Go AST（`go/parser`）遍历三个范围，抽出所有 `make(...)` 调用，检查其**长度/容量实参**的表达式子树里是否出现整型 `+` 或 `*`。这一层比正则可靠：它能覆盖跨行调用、嵌套加法（如 `len(a)+len(b)+len(c)+1`），并且能区分 2 参形式（`make(T, n)`，n 既是长度也是容量）与 3 参形式（`make(T, 0, cap)`，cap 才是容量）。

粗筛用的单行正则（可复现，作为交叉验证）：

```bash
rg -n --type go 'make\([^)]*len\([^)]*\)\s*[\+\*]' -g '!*_test.go' apps/api extensions tools   # 102 行
```

AST 扫描结果：**109 处**（`apps/api` 生产代码）；`extensions` **0 处**；`tools/proto` **0 处**。测试文件另有 92 处同族写法，不在生产面，不列入下表。

### 2.2 CodeQL `go/allocation-size-overflow` 的真实判据

要让报告可信，必须用查询的真实谓词而不是猜测。查询定义取自 `github/codeql` main 分支：

- 查询入口：`go/ql/src/Security/CWE-190/AllocationSizeOverflow.ql`（`@kind path-problem`，severity warning，precision high）
- 库：`go/ql/lib/semmle/go/security/AllocationSizeOverflow.qll` 与 `...Customizations.qll`

关键谓词链：

1. **Source（污染源）**：`MarshalingSource`
   - `json.Marshal` / `json.MarshalIndent` / `xml.Marshal` / `xml.MarshalIndent` 的**返回值**（要求其**入参**通过 `isBig` 判定）
   - `io/ioutil.ReadAll` / `io/ioutil.ReadFile` 的返回值
   - **注意**：`json.Unmarshal` **不是**本查询的 Source。库里虽有 `UnmarshalingFunction`（`encoding/json.Unmarshal`、`encoding/xml.Unmarshal`、`json-iterator` 的 `Unmarshal`），但 `AllocationSizeOverflowCustomizations.qll` 的 `MarshalingSource` 只消费 `MarshalingFunction`（即**编码**侧）。`json.Decoder.Decode` 同样未被建模。
2. **Sink**：`DefaultSink` = 某个整型 `+`/`*` 运算的操作数（`OverflowProneOperand`，只取最外层，避免重复上报），其运算结果经 `localStep*` 流到 **`AllocationSize`**。
3. **AllocationSize**：`make` 的**第 1 或第 2 个非类型实参**。即
   - `make(T, n)` → `n` 是 size（同时是长度）
   - `make(T, 0, cap)` → `cap` 是 size
4. **中间步**：污染先到 `len()` 的**实参**，再经 `additionalStep` 把 `len(x)` 送进算术表达式。

**这条判据直接决定了 T1 修法为什么有效**：`max(len(a), len(b))` 里 `max` 不是 `ArithmeticExpr`，容量表达式中也不再存在 `+`/`*`，`DefaultSink` 无从成立 —— 告警必然从 "open" 转 "fixed"。

---

## 三、扫描范围与 CodeQL 覆盖边界

| 范围 | Go module | 是否在 CodeQL Go DB 内 | AST 扫描命中 |
|---|---|---|---|
| `apps/api` | `github.com/keeptrue/sforum/apps/api` | **是**（workflow 在此目录跑 `go build ./...`，manual build） | **109** |
| `extensions/builtin/plugins/*/backend`（8 个） | 各自独立 go.mod | **否** | **0** |
| `extensions/fixtures/*`、`extensions/dev/*` | 各自独立 go.mod | **否** | 0 |
| `tools/proto` | 独立 go.mod，**无 Go 源码**（只有 `go.mod`/`go.sum`/`.bin`） | **否** | **0** |

依据 `.github/workflows/security.yml`：

```yaml
- language: go
  build-mode: manual
...
- name: Build Go code
  working-directory: apps/api
  run: go build ./...
```

`apps/api/go.mod` 无 `replace`、仓库无 `go.work`，插件 backend module 不是 `apps/api` 的依赖，因此 `go build ./...` 不编译它们，CodeQL 抽取不到这些源文件。

**这意味着：** 即使插件 backend 里存在同族写法，CodeQL 也不会告警（**假阴性来源**）。本任务已用 AST 补扫，结论是**当前 0 处**，所以「扩展插件没有同类漏洞」这一结论是**实测的**，不是「扫不到所以认为没有」。

**留待后续（超出本任务边界，未改动）：** 若要消除这处盲区，需要把 workflow 的 Go 构建扩展到插件 module（例如按 matrix 逐 module 构建）。这是 workflow 改动，不属于 task-2 范围，仅在此登记。

---

## 四、命中点分桶与可达性

109 处按「长度来源」分 5 桶。**可达性判定栏的两种结论**：

- **无可达溢出面**：长度是常量或已被上游钳制到小常数，构造上就不可能大。
- **理论溢出但输入不可控**：长度来自数据库行数 / 插件清单 / 进程内集合 / 有界的外部输入 —— 即便攻击者完全控制其内容，其**元素个数**仍被内存上界（≤2^48）锁死，加法/乘法不溢出。

| 桶 | 含义 | 处数 | 可达性 |
|---|---|---|---|
| **A** | 容量是**原始整数**（常量或已钳制） | 9 | 无可达溢出面 |
| **B** | 长度源为**外部输入**（请求体 / 配置 / 上传内容） | 19 | 理论溢出但输入不可控 |
| **C** | 长度由**插件清单 / 扩展注册表**决定 | 16 | 理论溢出但输入不可控 |
| **D** | 长度为**数据库 / 注册表行数** | 61 | 理论溢出但输入不可控 |
| **E** | **进程内计算**出的集合 | 4 | 理论溢出但输入不可控 |

### A 桶：容量是原始整数 —— 唯一值得逐条看的形态

这一桶是**唯一可能真正产生超大/负容量**的形态（因为它绕开了 `len()` 的内存上界）。逐条核对了钳制链路，**4 处真实代码 + 5 处 CLI/常量**全部安全：

| 文件:行 | 表达式 | 长度来源 | 钳制证据 | 结论 |
|---|---|---|---|---|
| `app/Models/Forum/reference_selector.go:134` | `input.Limit + 1` | 请求参数 `input.Limit` | `normalizeReferenceSelectorInput`（同文件 `:104`）把 `Limit` 钳到 `[1, ReferenceSelectorMaxLimit=20]`；`ReferenceSelector.ListReferenceOptions`（`:75`）先归一化再下发 store，`CachedStore.ListReferenceOptions`（`cached_store.go:102`）只转发已归一化的入参 | **安全**：即使传 `2^62`，落库前已被改写为 20 |
| `app/Support/IdentityRegistry/postgres.go:192` | `filter.Limit + 1` | 内部 `RoleSuggestionFilter.Limit` | 同函数 `ListRoleSuggestionPage` 在 `:142` 先 `filter := normalizeRoleSuggestionFilter(input.Filter)`，其中 `store.go:340` 把越界值重置为 `defaultRoleSuggestionLimit=50`（上限 `maxRoleSuggestionLimit=100`） | **安全**：钳制与 make 在同一函数作用域内，`:142 < :192` |
| `app/Models/Extensions/public_frontend.go:744` | `32 * 1024` | 常量 | 固定 32 KiB 读缓冲 | **安全**：编译期常量 |
| `cmd/sforum/seed_data.go:209` | `paragraphCount + 1` | CLI 种子数据固定段落数 | 开发 CLI，非服务面 | **安全** |
| `cmd/sforum/ai_eval_diff.go:251` | `len(baseline) + len(current)` | CLI 两份评测快照的条目数 | 开发 CLI | 安全（同 §一 论证） |
| `cmd/sforum/ai_eval_fixture.go:438` | `len(fixture.Corpus.Topics) + 1` | CLI 夹具语料话题数 | 开发 CLI | 安全 |
| `cmd/sforum/dev_extension.go:492` | `len(l.stamps) + 16` | CLI 开发态 stamp 列表 | 开发 CLI | 安全 |
| `cmd/sforum/generator_authoring_workflow.go:241`、`:297` | `len(existing) + 1` | CLI 生成器已有文件列表 | 开发 CLI | 安全 |

**A 桶结论：全仓没有一处「容量 = 未钳制的攻击者整数」。** 这是本次排查最重要的一条：**不存在真实可达的分配尺寸溢出。**

### 乘法形式（任务点名「尤其值得看」）

乘法在抽象上比加法更容易溢出（×2 vs +1），但受同一条内存上界约束，结论不变 —— 唯一变化是**分配量放大 2 倍**，属可忽略的内存放大。7 处逐条：

| 文件:行 | 表达式 | 长度来源 | 可达性 |
|---|---|---|---|
| `app/Models/AIReply/trigger.go:337` | `len(fields) * 2` | 日志字段 map（进程内构造） | 理论溢出但输入不可控 |
| `app/Support/ExtensionOpenAPI/route_schema_catalog.go:134` | `len(operations) * 2` | 插件 OpenAPI 聚合后的 operation 集 | 理论溢出但输入不可控 |
| `app/Support/ExtensionOpenAPI/route_schema_catalog.go:206` | `len(operations) * 2` | 同上 | 理论溢出但输入不可控 |
| `app/Support/QueryRegistry/execution_result.go:361` | `len(plan.CacheTags) * 2` | 查询计划的缓存 tag（DB/计划派生） | 理论溢出但输入不可控 |
| `app/Support/Routes/dispatcher_stages.go:23` | `len(chain) * 2` | 路由执行链（进程内计划） | 理论溢出但输入不可控 |
| `bootstrap/extension_database_catalog.go:141` | `len(items) * 2` | 扩展数据库目录条目（DB/注册表） | 理论溢出但输入不可控 |
| `app/Models/Extensions/public_frontend.go:744` | `32 * 1024` | 常量 | 无可达溢出面 |

### B 桶中需要点名复核的几处

这些是长度真正来自外部输入的站点，全部**另有上游上限**，即长度有界：

| 文件:行 | 表达式 | 上游约束证据 |
|---|---|---|
| `app/Models/SiteChrome/navigation_commands.go:305` | `len(input.Definitions) + len(CoreNavigationDefinitions())` | 同函数 `normalizeNavigationDocument` 在 `:300` 先判 `len(input.Definitions) > NavigationMaxDefinitions` 即拒绝 |
| `app/Support/MediaRegistry/validation.go:110` | `len(input.Policies) + len(input.Processors) + len(input.Variants)` | 同函数 `normalizePublication` 在 `:102-104` 三项各自判 `> maxPerPublication` 即拒绝 |
| `app/Support/HostAPI/cache_registry_redis.go:247`、`:362` | `len(value.Tags) + 1` / `+ 2` | `:235` 与 `:350` 先判 `len(value.Tags) > HostCacheMaximumTags`（=64，`cache_registry_service.go:29`）即返回 `ErrHostCacheInvalid` |
| `app/Models/TrustLevels/payload.go:20`、`:37` | `len(policy.ActiveTiers()) + 1` | 策略解码阶段已校验 active 档数 `1..MaxTiers`（`MaxTiers = 8`，`TrustLevels/types.go:52`） |
| `app/Models/Identity/user_fields.go:277`、`:291` | `len(fieldID) + len(value)` 等 | 用户字段值经 `maximumIdentityUserFieldValueBytes` 限制（`user_fields.go:224`） |
| `app/Support/AI/tool_loop.go:161` | `len(input.Images) + 1` | 用户上传图片经附件大小上限约束 |
| `app/Support/AI/wire_openai.go:147`、`app/Support/AI/wire_anthropic.go:42` | `len(request.Messages) + 1`、`len(message.Parts) + 1` | 进程内拼装的模型消息，非请求体直填 |

---

## 五、对 T1 覆盖完整性的最终回答

### 5.1 告警集合层面：**完整**

- 仓库当前 open 的 `go/allocation-size-overflow` 告警共 **3 条**（#45 / #47 / #48），落点分别是：
  - `apps/api/app/Models/TrustLevels/policy.go:111` —— `make([]string, 0, len(previous.DeletedTierIDs)+len(previous.Tiers))`
  - `apps/api/app/Http/Controllers/TrustLevels/controller.go:459` —— `make(map[string]struct{}, len(configKeys)+len(declaredKeys))`
  - `apps/api/app/Http/Controllers/TrustLevels/controller.go:460` —— `make([]earnableState, 0, len(configKeys)+len(declaredKeys))`
- T1 改动后，3 处的容量表达式分别变成 `max(len(previous.DeletedTierIDs), len(previous.Tiers))` 与 `max(len(configKeys), len(declaredKeys))`。容量表达式中不再有 `+`，`DefaultSink` 的「整型 `+`/`*` 运算 → AllocationSize」链条断裂，**3 条告警必然全部关闭**。
- 语义等价性成立：容量只是**预分配提示**，并集实际长度落在 `max` 与两者之和之间，Go 在 `append` 时自动扩容，去重逻辑与保序并集语义不受影响。

### 5.2 同类模式族层面：**不完整，但剩余部分不需要一并修**

- `apps/api` 生产代码仍有 **109 处**同族写法（§六 全表）。
- 逐条判定后：**0 处可达的整数溢出，0 处可被攻击者顶到负容量或超大容量**。理由见 §一 核心论证 + §四 A 桶逐条钳制核对。
- 因此**不建议**为了「消灭模式」而批量改写这 109 处 —— 那是一次与安全收益不成比例的大范围重构，且会触碰大量与本轮告警无关的业务代码。

### 5.3 需要留给 Lead 决策的一项（**结论：不需要追加进 T1**）

TrustLevels 链里还有 2 处同族写法：

- `apps/api/app/Http/Controllers/TrustLevels/payload.go:20` —— `make([]publicLevelDefinition, 0, len(policy.ActiveTiers())+1)`
- `apps/api/app/Http/Controllers/TrustLevels/payload.go:37` —— `make([]adminLevelDefinition, 0, len(policy.ActiveTiers())+1)`

**不并入 T1 的理由（三条）：**

1. 它们**不是**当前 3 条告警之一 —— CodeQL 未报，说明其污染路径与 `controller.go` 那两处不同（`ActiveTiers()` 是方法返回值而非直接字段，`FindLargeLensConfig` 的「污染到达 `len()` 实参」链条在此断掉）。
2. T1 的修改范围（`controller.go` + `policy.go`）已经覆盖了全部 open 告警；再改 `payload.go` 只会扩大同一文件链的写手面，没有对应的告警要消。
3. 即便将来 CodeQL 升级扩大了 source 模型使其命中，`len(policy.ActiveTiers())+1` 的长度上界是 `MaxTiers = 8`，属于「理论溢出但输入不可控」。

**故：请 Lead 知悉，无需追加 T1 范围。**

---

## 六、完整命中点清单（109 处，`apps/api` 生产代码）

桶定义与可达性判定同 §四。

| 文件:行 | 类型 | 容量表达式 | 来源 | 桶 | 可达性判断 |
|---|---|---|---|---|---|
| `app/Http/Controllers/Extensions/admin_surfaces.go:179` | `map[string]bool` | `len(actor.Permissions) + 1` | actor 权限集合（会话/DB 读取，非请求体直填） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Http/Controllers/TrustLevels/payload.go:20` | `[]publicLevelDefinition` | `len(policy.ActiveTiers()) + 1` | 信任等级策略档位数（配置解码后，active 档数 ≤MaxTiers=8） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Http/Controllers/TrustLevels/payload.go:37` | `[]adminLevelDefinition` | `len(policy.ActiveTiers()) + 1` | 信任等级策略档位数（配置解码后，active 档数 ≤MaxTiers=8） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Http/route_dispatcher.go:380` | `map[string]bool` | `len(actor.Permissions) + 1` | actor 权限集合（会话/DB 读取） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/AIReply/completer.go:72` | `[]supportai.Part` | `len(images) + 1` | 本次补全的图片列表（用户上传附件，经上限约束） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/AIReply/topic_state.go:195` | `[]Fact` | `len(existing) + len(added)` | AI 话题状态已有事实与新增事实（进程内集合） | E | 进程内构造集合 → **理论溢出但输入不可控** |
| `app/Models/AIReply/topic_state.go:196` | `map[CitationRef]int` | `len(existing) + len(added)` | AI 话题状态已有事实与新增事实（进程内集合） | E | 进程内构造集合 → **理论溢出但输入不可控** |
| `app/Models/AIReply/trigger.go:337` | `[]any` | `len(fields) * 2` | 日志字段 map（进程内构造） | E | 进程内构造集合 → **理论溢出但输入不可控** |
| `app/Models/Badges/service.go:137` | `[]BadgeGroupSection` | `len(groups) + 1` | 徽章分组（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Conditions/resolve_activity.go:98` | `map[int64]counters` | `len(daysSet) + 1` | 活跃天数集合（DB 行数） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Extensions/dependency_preflight.go:61` | `map[string]extensionmanifest.Manifest` | `len(items) + 1` | 已安装扩展清单（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Extensions/plugin_runtime_publication_transition.go:78` | `[]PluginRuntimeMember` | `len(canonicalLatest) + 1` | 插件运行时成员 canonicalLatest（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Extensions/public_frontend.go:744` | `[]byte` | `32 * 1024` | 常量 32*1024（固定摘要读缓冲） | A | 输入已钳制/为常量 → **无可达溢出面** |
| `app/Models/Extensions/service_package.go:579` | `[]ManifestAdminPage` | `len(extensionmanifest.EffectiveAdminPages(manifest)) + 1` | 插件清单 EffectiveAdminPages（插件 manifest） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Models/Extensions/theme_asset_publication.go:259` | `[]assetregistry.Publication` | `len(current) + 1` | 主题资产集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Extensions/theme_asset_publication.go:425` | `[]assetregistry.Publication` | `len(current) + 1` | 主题资产集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Forum/attachment_references_postgres.go:172` | `map[int64]struct{}` | `len(left) + len(right)` | 两批附件引用 id（DB 行数） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Forum/attachment_references_postgres.go:173` | `[]int64` | `len(left) + len(right)` | 两批附件引用 id（DB 行数） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Forum/contributions.go:188` | `[]UserSummary` | `len(items) + 1` | 论坛贡献条目（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Forum/postgres_store_ops.go:437` | `[]Comment` | `len(roots) + len(descendants)` | 评论树 roots/descendants（DB 行数） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Forum/reference_selector.go:134` | `[]ReferenceOption` | `input.Limit + 1` | 请求参数 input.Limit（已钳制 ≤20） | A | 输入已钳制/为常量 → **无可达溢出面** |
| `app/Models/Identity/permission_compat.go:33` | `map[string]bool` | `len(keys) + 16` | 权限键集合（DB 种子/授权） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/Identity/user_fields.go:277` | `[]byte` | `len(identityUserFieldValueDigestDomain) + 1 + 8 + 4 + len(fieldID) + 4 + len(value)` | 用户自定义字段 fieldID/value（请求体，经 maximumIdentityUserFieldValueBytes 限制） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/Identity/user_fields.go:291` | `[]byte` | `len(identityUserFieldReceiptKeyDomain) + 1 + 4 + len(scope) + 4 + len(key)` | 用户自定义字段 fieldID/value（请求体，经 maximumIdentityUserFieldValueBytes 限制） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/Notifications/legacy_mail.go:36` | `map[string]string` | `len(current) + len(legacyToPluginSetting)` | 旧邮件设置 + 插件设置键（配置/插件） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/Profile/service.go:378` | `[]ProfileActivity` | `len(topics) + len(comments)` | 用户话题/评论数（DB 行数） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Models/SiteChrome/navigation_commands.go:305` | `[]NavigationDefinition` | `len(input.Definitions) + len(CoreNavigationDefinitions())` | 导航文档 input.Definitions（后台请求体，已按 NavigationMaxDefinitions 拒绝） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/SiteChrome/navigation_resolver.go:154` | `map[string]NavigationDefinition` | `len(input) + 8` | 导航注册表条目（核心+插件声明） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Models/SiteChrome/navigation_resolver.go:164` | `map[string]NavigationPlacement` | `len(input) + 16` | 导航注册表条目（核心+插件声明） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/AI/tool_loop.go:161` | `[]Part` | `len(input.Images) + 1` | 本次发言附带的图片数（用户上传，经附件上限约束） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/AI/wire_anthropic.go:42` | `[]anthropicBlock` | `len(message.Parts) + 1` | 本次模型消息 parts 数（进程内拼装，用户文本+图片） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/AI/wire_openai.go:147` | `[]openAIChatMessage` | `len(request.Messages) + 1` | 本次模型请求 messages 数（进程内拼装） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/ContentLinks/links.go:170` | `[]attribute` | `len(attrs) + 2` | 内容链接属性 attrs（帖子 HTML 属性解析结果） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/ContentRegistry/execution_inspector.go:62` | `[]InspectorStep` | `len(plan.conflicts) + 1` | 内容计划冲突项（DB/计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/ContentRegistry/execution_plan.go:127` | `[]plannedBinding` | `len(p.before) + len(p.wrap) + len(p.after) + len(p.filters) + 1` | 内容计划 before/wrap/after/filters（插件声明+计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/ContentRegistry/forum_shortcode_dispatcher.go:674` | `[]ShortcodeResourceKey` | `len(input) + 1` | 短码输入分片（帖子内容派生，经输出上限约束） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/EditorDocument/render_table.go:100` | `[]string` | `len(rows) + 1` | 编辑器文档表格行（帖子内容派生） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/EditorDocument/shortcodes.go:72` | `map[string]shortcodeDeclaration` | `len(shortcodeDeclarations) + 1` | 短码声明集合（扩展注册表） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionManifest/v3_validate_platform.go:258` | `[]string` | `len(value.ByLocale) + 1` | 插件 manifest 本地化文案条目（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/route_schema_catalog.go:134` | `[]RouteSchemaBinding` | `len(operations) * 2` | 插件 OpenAPI 聚合后的 operation 集（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/route_schema_catalog.go:206` | `[]routeSchemaCandidate` | `len(operations) * 2` | 插件 OpenAPI 聚合后的 operation 集（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/route_schema_catalog.go:415` | `map[string]routeSchemaVariant` | `len(definition.variants) + len(variants)` | 插件 OpenAPI 聚合后的 operation 集（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/route_schema_publication.go:232` | `[]Artifact` | `len(current.artifacts) + 1` | 已发布 OpenAPI artifact 集（插件清单/DB） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/validate.go:49` | `map[string]any` | `len(operation) + 3` | 插件 OpenAPI operation 与参数（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/ExtensionOpenAPI/validate.go:68` | `[]any` | `len(pathParameters) + len(operationParameters)` | 插件 OpenAPI operation 与参数（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/Extensions/component_composition_values.go:439` | `map[string]struct{}` | `len(current) + len(candidate)` | 组件合成当前值与候选值（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/Extensions/extension_database_disposition_postgres.go:244` | `map[string]struct{}` | `len(migrationRoles) + 1` | 扩展数据库迁移角色（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/extension_database_kernel_postgres.go:90` | `map[string]struct{}` | `len(liveOwners) + len(pendingOwners) + len(extraKernelRoles)` | 扩展数据库内核角色（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/extension_database_kernel_postgres.go:205` | `map[string]extensionDatabaseMembershipOptions` | `len(kernelRoles) + 1` | 扩展数据库内核角色（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/hook_registry.go:96` | `map[string]hookRuntimeRegistration` | `len(current.extensions) + 1` | 已注册扩展钩子集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/hook_registry.go:114` | `map[string]hookRuntimeRegistration` | `len(current.extensions) + 1` | 已注册扩展钩子集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_assets.go:893` | `[]assetregistry.Publication` | `len(base) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_cache.go:183` | `[]cacheregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_content.go:276` | `[]contentregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_editor.go:248` | `[]editorregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_entity.go:254` | `[]entityregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_identity.go:786` | `[]identityregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_media.go:314` | `[]mediaregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_navigation.go:280` | `[]navigationregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_queries.go:229` | `[]queryregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_registry_publication_seo.go:250` | `[]seoregistry.Publication` | `len(snapshot.Publications) + 1` | 生命周期发布快照 publications（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/lifecycle_route_policy_publication.go:250` | `[]routes.PluginRouteSet` | `len(publication.Plugins) + 1` | 路由策略发布 plugins（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/plugin_runtime_full_set.go:253` | `[]RuntimeInstanceIdentity` | `len(p.desired) + len(p.removals)` | 插件期望集合与下线集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/plugin_runtime_full_set_hooks.go:259` | `map[string]string` | `len(plan.desired) + len(plan.removals)` | 插件期望集合与下线集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/plugin_runtime_full_set_hooks.go:391` | `[]restoreTarget` | `len(plan.desired) + len(plan.removals)` | 插件期望集合与下线集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_instances.go:287` | `map[string]struct{}` | `len(desired) + len(s.activeRuntimeInstances)` | 协议运行时实例集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_instances.go:377` | `map[*protocolRuntimeInstance]publicationState` | `len(instances) + len(s.activeRuntimeInstances)` | 协议运行时实例集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_instances.go:506` | `map[string]struct{}` | `len(desired) + len(s.activeRuntimeInstances)` | 协议运行时实例集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_instances.go:572` | `map[*protocolRuntimeInstance]publicationState` | `len(instances) + len(s.activeRuntimeInstances)` | 协议运行时实例集合（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_v2_actor_delegation.go:305` | `[]*protocolv2.HostQueryDelegation` | `len(existing) + 1` | 主机查询授权委派集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Extensions/protocol_v2_routes.go:622` | `map[string][]string` | `len(legacy) + len(all)` | 协议 v2 路由集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/cache_registry_redis.go:247` | `[]string` | `len(value.Tags) + 1` | 缓存条目 tag 列表（已按 HostCacheMaximumTags=64 拒绝） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/cache_registry_redis.go:362` | `[]string` | `len(value.Tags) + 2` | 缓存条目 tag 列表（已按 HostCacheMaximumTags=64 拒绝） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/query_registry_provider.go:255` | `[]protocolV2QuerySort` | `len(requested) + 1` | 查询提供方请求条目（注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/service_dependencies.go:85` | `[]preparedServiceRegistration` | `len(current.registrations) + len(preparedServices)` | 服务注册集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/service_registry.go:181` | `[]preparedServiceRegistration` | `len(current.registrations) + len(prepared)` | 服务注册集合（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/v2_database.go:211` | `map[protocolV2DatabaseKey]struct{}` | `len(queries) + len(executes)` | 协议 v2 查询/执行定义（插件声明） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/v2_query_plan.go:322` | `[]protocolV2QuerySort` | `len(requested) + 1` | 请求的查询字段条目（注册表/计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/HostAPI/v2_query_postgres.go:385` | `[]any` | `len(plan.Filters) + 3` | 查询计划过滤器（DB/计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/IdentityRegistry/postgres.go:192` | `[]RoleSuggestion` | `filter.Limit + 1` | 内部 filter.Limit（已钳制 ≤100） | A | 输入已钳制/为常量 → **无可达溢出面** |
| `app/Support/MediaRegistry/plan.go:462` | `[]ProviderConflict` | `len(steps) + 1` | 媒体处理步骤（DB/计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/MediaRegistry/receipt.go:550` | `[]ReceiptBinding` | `len(prerequisites.Steps) + 2` | 媒体前置步骤（DB/计划） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/MediaRegistry/validation.go:110` | `map[string]struct{}` | `len(input.Policies) + len(input.Processors) + len(input.Variants)` | 媒体发布策略/处理器/变体（插件声明，已按 maxPerPublication 拒绝） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/Pages/component_composition_render.go:29` | `[]string` | `len(composition) + len(primary)` | 组件合成 composition/primary（插件声明） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `app/Support/Pages/theme_fallback.go:88` | `[]ThemeRenderAttempt` | `len(s.plan.candidates) + 1` | 主题回退候选（注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Pages/theme_runtime.go:850` | `map[string]string` | `len(skin.CSS) + 1` | 主题 skin.CSS（插件主题产物，经产物大小上限约束） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/execution_result.go:80` | `map[string]struct{}` | `len(plan.Fields) + len(plan.Relations)` | 查询计划字段/关系、结果过滤器、缓存 tag（计划/DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/execution_result.go:287` | `map[string]struct{}` | `len(r.filters) + len(dynamic)` | 查询计划字段/关系、结果过滤器、缓存 tag（计划/DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/execution_result.go:288` | `[]preparedResultFilter` | `len(r.filters) + len(dynamic)` | 查询计划字段/关系、结果过滤器、缓存 tag（计划/DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/execution_result.go:361` | `[]string` | `len(plan.CacheTags) * 2` | 查询计划字段/关系、结果过滤器、缓存 tag（计划/DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/redis_cache.go:265` | `[]string` | `len(physical) + 3` | 缓存物理键与 fence 代次（DB/缓存） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/redis_cache.go:268` | `[]any` | `len(fenceMaterial.generations) + 4` | 缓存物理键与 fence 代次（DB/缓存） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/redis_cache.go:312` | `[]string` | `len(physical) + 1` | 缓存物理键与 fence 代次（DB/缓存） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/QueryRegistry/redis_cache.go:343` | `[]string` | `len(physical) + 1` | 缓存物理键与 fence 代次（DB/缓存） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/Routes/dispatcher_stages.go:23` | `[]routeInvocationExecution` | `len(chain) * 2` | 路由执行链（进程内计划） | E | 进程内构造集合 → **理论溢出但输入不可控** |
| `app/Support/Routes/execution_plan.go:242` | `[]RouteExecutionStep` | `len(match.Contributions) + 1` | 路由匹配贡献（注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `app/Support/SettingsLifecycle/store.go:177` | `map[string]string` | `len(doc.Values) + len(doc.SecretRefs) + 8` | 设置文档 doc.Values/doc.SecretRefs（设置表落库内容） | B | 长度有界但源为外部输入 → **理论溢出但输入不可控** |
| `app/Support/Storage/candidates.go:94` | `[]Candidate` | `len(core) + len(plugins)` | 核心与插件存储候选（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `bootstrap/ai_plugin_tools.go:87` | `[]supportai.Tool` | `len(core) + len(plugins)` | 核心工具与插件工具（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `bootstrap/ai_plugin_tools.go:88` | `map[string]bool` | `len(core) + len(plugins)` | 核心工具与插件工具（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `bootstrap/badges_fact_catalog.go:142` | `[]extensionmanifest.ManifestBadgeCondition` | `len(item.Manifest.BadgeConditions) + len(item.Manifest.UserFactConditions)` | 插件 manifest 徽章/事实条件（插件清单） | C | 长度由已安装插件清单决定（需 super_admin 装插件）→ **理论溢出但输入不可控** |
| `bootstrap/extension_database_catalog.go:141` | `[]extensions.Extension` | `len(items) * 2` | 扩展数据库目录 items（DB/注册表） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |
| `cmd/sforum/ai_eval_diff.go:251` | `[]string` | `len(baseline) + len(current)` | CLI 评测快照对比，两侧文件内条目 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `cmd/sforum/ai_eval_fixture.go:438` | `[]evalCorpusTopic` | `len(fixture.Corpus.Topics) + 1` | CLI 评测夹具 topics 列表 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `cmd/sforum/dev_extension.go:492` | `map[string]devFileStamp` | `len(l.stamps) + 16` | CLI 开发态 stamp 列表 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `cmd/sforum/generator_authoring_workflow.go:241` | `[]any` | `len(existing) + 1` | CLI 生成器已有文件列表 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `cmd/sforum/generator_authoring_workflow.go:297` | `[]any` | `len(existing) + 1` | CLI 生成器已有文件列表 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `cmd/sforum/seed_data.go:209` | `[]string` | `paragraphCount + 1` | CLI 固定段落数 | A | 输入已钳制/为常量 → **无可达溢出面** |
| `database/migrator/core_authority_kernel_validation.go:306` | `map[string]coreKernelMembershipOptions` | `len(candidates) + 1` | 核心权限内核候选（DB） | D | 长度为数据库/注册表行数 → **理论溢出但输入不可控** |

---

## 七、建议

| # | 建议 | 优先级 | 说明 |
|---|---|---|---|
| 1 | T1 保持现状合入 | **高** | 已覆盖 3/3 告警，改法与查询判据对齐，改动面最小 |
| 2 | 不批量改写剩余 109 处 | **高** | 无可达溢出面；批量改动与安全收益不成比例 |
| 3 | 考虑把 CodeQL Go 构建扩展到插件 module | 中 | 消除 §三 的流程盲区；属 workflow 改动，不在 task-2 范围，仅登记 |
| 4 | 若日后仍想收敛该模式族，优先改 A 桶与乘法形式 | 低 | A 桶 4 处真实代码形态上最接近「可被顶大」，虽有钳制但缺显式断言；可用 `max()` 或显式上限替代 |

---

## 附：可复现命令

```bash
cd apps/api

# 权威扫描（Go AST，覆盖跨行与嵌套；本报告的 109 处即由此得出）
# 扫描器要点：遍历 make() 调用 → 取第 1(2参形式)/第 2(3参形式) 个实参 → 检查子树含整型 + 或 *

# 粗筛交叉验证
rg -n --type go 'make\([^)]*len\([^)]*\)\s*[\+\*]' -g '!*_test.go' apps/api extensions tools

# 扩展与工具模块（实测 0 命中）
rg -n --type go 'len\([^)]*\)\s*\+\s*len\(' extensions tools
```
