# Decision: HumanVerify 配置读失败时的降级方向与可观测性（F-3）

## Status

Accepted（2026-11-17）。本条只做两件事：**留痕（结构化 warn）+ 留档**。降级方向不改，
也不新增开关、不迁移配置。

## Context

`RuntimeService`（`apps/api/app/Support/HumanVerify/runtime_service.go`）每次
`Challenge` / `Verify` 都从 `ConfigSource`（生产 = Options 服务）读当前配置，读失败时
退回构造时传入的 `fallback`（生产来源 = env，见 `bootstrap/app.go` 的
`humanVerifyConfigFromConfig`）。修复前的 `service()` 是：

```go
if current, err := s.source.HumanVerificationConfig(ctx); err == nil {
    cfg = current
}
```

`err` 被整个吞掉。问题不是「降级」本身（方向是 P3-3 审查已经论证过的设计），而是
**降级无声**，且两种故障的后果完全不同：

- **全故障**（PostgreSQL 整体不可用）：同一请求的业务写入也失败，用户拿到 500，
  降级分支几乎不产生实际效果。
- **部分故障**（单条语句超时、连接池饱和、坏执行计划、只读副本切换）：Options 读取失败
  而写路径成功 → 运营明确开了 `post_risk`，实际却**零校验**，且日志里没有任何痕迹。

第二种才是这条 finding 的真实内容。reviewer 的反驳也正打在这里：部分故障下攻击面真实存在，
不能拿「全故障时写库也会失败」当挡箭牌。

## Decision

### 1. 降级方向保持两向，由 `fallback.Provider` 决定

| `fallback.Provider` | 语义 | 方向 | 前提 |
| --- | --- | --- | --- |
| 空 / `disabled` | 该次请求零校验（`NewDisabledService()`） | fail-open | provider 只在 Options 里配置；Options 读不出来 = 该部署本来就没启用 |
| `altcha` | fallback 的 `PurposeEnabled` 为 nil，而 `Service.purposeEnabled` 对 nil map 返回 true → **所有场景继续强制** | fail-closed | env 已把该部署声明成「启用了人机验证」（`HUMAN_VERIFICATION_PROVIDER=altcha` + secret），Options 抖动不该把它降级成零校验 |

`normalizeRuntimeConfig` 只做两件事：`"" → disabled`，以及 lower/trim；它**不校验成员**。
「只有这两个取值」由配置校验保证，不由 normalize 保证——见 Consequences 的第三态
（未知 provider 会原样穿过，方向标注因此失真）。

### 2. 读失败必须留痕，且只有一个日志点

`service()` 在 `source` 返回错误时写一条结构化 warn（`slog`）：

```text
humanverify: read runtime config failed; using fallback
  purpose=<login_risk|post_risk|register|password_reset|email_verification>
  fallback=<altcha_enforced|disabled_zero_verification>
  error=<原始 err>
```

- **只打一处**：`service()` 是唯一解析配置的地方；`Challenge` / `Verify` 不各自打日志，
  否则一次登录会重复多条。
- `purpose` 进日志：同一降级对 `post_risk`（发帖）与 `login_risk`（登录）的后果不同，
  运维要能判断先修哪条路径。
- `fallback` 方向进日志：一眼区分「继续强制」与「零校验」，这两个字符串互斥。
- 通过 `WithLogger` 注入，缺省 `slog.Default()`；测试用探针 logger 断言 warn 真的被调用
  （`TestRuntimeServiceOptionsFailureKeepsAltchaFallbackEnforced` /
  `TestRuntimeServiceOptionsFailureWithDisabledFallbackSkips`）。

### 3. 为什么不在读失败时强制 fail-closed

1. **不增加真实安全性**：`fallback=disabled` 的部署没有 secret、没有可用 challenge
   （`NewDisabledService()` 的 `Challenge` 返回 disabled provider）。此时强制「先完成人机
   验证」等于要求一个**永远拿不到**的 token，把注册/发帖变成死路，而不是加了一道门槛。
2. **把 DB 抖动放大成写路径 500**：一次 Options 语句超时本来只影响一个读，改成
   fail-closed 就升级成注册、发帖、密码重置全量失败。可用性代价确定，安全收益为零。
3. **真正需要 fail-closed 的部署已经 fail-closed**：`altcha` 部署走 §1 第二行。担心的对象
   只剩「Options 里开了 provider 但 env 没声明」这种错配部署，而它在读失败前也无法完成
   challenge（provider 配置只存在于 Options 里），强制校验同样只会挡住合法用户。
4. 该取舍是 P3-3 审查的结论；本轮只补可观测面，不重开方向。

## Consequences

- **残留风险（reviewer 的反驳论点，已接受）**：`fallback=disabled` 的部分故障窗口内，
  「运营开了 `post_risk`」与「实际零校验」不一致，且该窗口内攻击者确实可以不带 token
  提交。这不是理论问题——只要 Options 读取失败而写入成功，窗口就是开放的。
- **已知边界（第三态，F3-1）**：`HUMAN_VERIFICATION_PROVIDER` 没有任何白名单校验
  （`config/config.go` 直接 `env(...)` 取值，`normalizeRuntimeConfig` 只 trim/lower）。
  配成 `hcaptcha` 这类未知值时：①`fallbackDirection` 只看 `Provider == altcha`，会把方向
  标成 `disabled_zero_verification`（日志说的是「零校验」）；②`NewConfiguredService` 走
  `default:` 返回 `unsupported human verification provider`，而该错误不是
  `ErrRequired`/`ErrInvalid` 这类哨兵，`MapHumanVerificationError` 会原样上抛，
  最终由 `errorHandler` 变成 **每个 Challenge/Verify 请求 500**。
  也就是说这条日志在这种情况下会同时「方向标错」且「每请求都刷」。本轮**不修**：
  给 config 加白名单是行为变更（会让启动期失败而不是运行期 500），属于另一条决策；
  这里只把边界写清楚，避免把 normalize 的职责说过头。
- **缓解手段就是 §2 的这条日志**，不是代码分支：它把「静默 fail-open」变成「有声
  fail-open」，让运营在窗口内可发现、可事后归因。选择留痕而非 fail-closed 的理由见 §3。
- 若将来要把该窗口彻底关掉，正确做法是让「`post_risk` 是否启用」拥有 Options 之外的
  独立可信来源（例如与 secret 同源的 env 声明），而不是在读失败时盲猜 fail-closed；
  那属于新决策，不在本条范围内。
- 行为面无变化：`disabled` 与 `altcha` 两条既有用例保持原语义，只多出日志断言。
