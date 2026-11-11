# 2026-11-11 P0：`host_commands` 授权闸门被 Go 方法提升静默丢弃

Status: **accepted（已修复，`57a3de5ad`）**
发现于：`../plans/2026-11-11-trust-levels-closing.md` 的收尾验收（M13 提交前）
引入于：基线 `f63ad2cf9`（上一刀「徽章 + 用户活动统计、信任等级」）
影响面：**所有已启用扩展 × 所有域命令**，其中 `sforum.entitlements.mutate` 无第二道防线。

## 缺陷

基线把装配写成一行链式调用（`v2_command_runtime_postgres.go:68`，`f63ad2cf9` 的原文）：

```go
// f63ad2cf9 的写法（已修，勿恢复；行号见 `git show 57a3de5ad` 的 hunk）
NewPostgresProtocolV2HostCommandBackend(pool).WithTrustLevelSource(src)
```

`WithTrustLevelSource` 只定义在**被嵌入的中性类型**上，且返回中性类型
（`Support/HostAPI/v2_command_postgres.go:51`，接收者 `*PostgresProtocolV2CommandBackend`）。
`PostgresProtocolV2HostCommandBackend` 嵌入它，于是 Go 的**方法提升**让上面那行返回
**中性 backend**——wrapper 被丢弃，`newProtocolV2CommandEngineWithInvalidationJobs` 拿到的是
不做 `host_commands` 授权检查的实现。

后果链：`ResolveScope` 的判定条件是
`requiredAuthority != "" && !hasDatabaseGrant(...)`（`v2_command_postgres.go:162`），
而中性 backend 的 `requiredAuthority` 恒为 `""` ⇒ 整个闸门对**全部**命令放行。

**它是静默的**：没有报错、没有日志、没有编译错误——返回值仍然满足下游接口，类型系统不拦。
这类缺陷只能靠**语义断言**抓，不能靠类型检查。

## 最锋利的影响

`sforum.entitlements.mutate` 的定义（`Support/HostAPI/v2_command_entitlements.go:66-70`）：

- `ActorMode: protocolV2CommandActorService`（无人类 RBAC 可依），
- **不设 `RequiredCapability`**（字段见 `v2_command.go:124-125`，此处留空）。

⇒ `host_commands` 闸门是它**唯一**的防线。闸门失效时，**任何已启用扩展**——哪怕 manifest 只声明
`own_schema`、从未申请 `host_commands`——都能 grant/revoke 权益。

**生产与测试走同一个构造器**：`bootstrap/host_command_runtime.go:250` 的
`NewPostgresProtocolV2CommandRuntime(...)` → `v2_command_runtime_postgres.go` 里的装配。
所以这不是「测试环境的问题」，是线上等价的问题。

## 发现路径（这一节比结论重要）

1. 本刀验收矩阵 **124 条全绿**的同时，整仓门禁**一直红着**。
2. 提交前跑 `./scripts/test.sh --fast` 才暴露（`app/Support/HostAPI` 红）。
3. 用两个 worktree 二分：`f63ad2cf9~1` → `ok … HostAPI 3.323s`；`HEAD` → 断言
   `want COMMAND_STATE_REJECTED` 失败。定性为**基线引入**，不是本刀改坏。

**为什么基线没发现**：`f63ad2cf9` 的验证写的是「八包全绿」，而
`app/Support/HostAPI` 与 `bootstrap` **都不在那八包里**；同一刀还漏了
`Controllers/Extensions` 一条红。**「全绿」的边界比听起来窄**——这是本仓第三次同形实例
（前两次：fixture base schema、错误映射副本）。

## 修法（三层，缺一层就会复发）

1. **让错误写法写不出来**：在 wrapper 上重新声明返回 **wrapper** 的 `WithTrustLevelSource`
   （`v2_command_postgres.go:75`）。链式调用从此只能得到 wrapper——
   「记住别这么写」变成「这么写不出来」。
2. **装配点显式两步**（`v2_command_runtime_postgres.go:67-73`：注释 67-69、两步 70-71、入引擎 72-73），并留注释说明为什么不做链式：
   读代码的人不必自己推理方法提升。
3. **两条 tripwire + 一条语义守卫**：
   - `TestPostgresCommandRuntimeKeepsHostCommandAuthorityBackend`——类型快照，装配返回的必须是 wrapper；
   - `TestHostCommandWrapperKeepsWrapperThroughChainedTrustLevelSource`——链式写法仍得 wrapper；
   - `TestPostgresProtocolV2EntitlementCommandDeniesExtensionWithoutHostCommandsGrant`——语义：
     只有 `own_schema` 的扩展执行 `entitlements.mutate` 必须 `REJECTED`（且不留权益事实与回执），
     **并带对照组**：持 `host_commands` 的扩展必须 `COMMITTED`。
     没有对照组，「一律拒绝」也会让断言绿——那就是假守卫。

## 可复用判据

1. **验收矩阵的绿 ≠ 仓库的绿。** 矩阵只证明「声明过的条目有证据」，不证明「没声明的地方没问题」。
   下一刀的验收必须**顺带跑一次整仓门禁**，且矩阵的 `0 FAIL` **不得单独作为可提交依据**。
2. **「八包全绿」这类说法必须写清是哪八包。** 验证结论的适用范围就是它跑过的包，多一个都不算。
3. **嵌入 + 链式 fluent API 是这类 bug 的高发形状。** 判据：返回类型必须与接收者同型；
   跨类型返回的「同名方法」要么改名，要么在子类型上重新声明。
4. **新增 `ActorMode: service` 且无 `RequiredCapability` 的命令时，先回答「谁在挡它」。**
   答不上来就说明你正在新增一条没有防线的写路径。

## 实施要点（给下一位）

- 闸门判定点只有一处：`ResolveScope`（`v2_command_postgres.go:121` 收 `requiredAuthority`，
  `:162` 判定）。改装配前后都从这里读，不要从调用方猜。
- 看到「扩展能执行它没申请的命令」先查装配有没有丢 wrapper，而不是先查 manifest 解析。
- 类型快照守卫**单独不够**：它能证明「装的是 wrapper」，证明不了「闸门真的会拒」。两者都要。
