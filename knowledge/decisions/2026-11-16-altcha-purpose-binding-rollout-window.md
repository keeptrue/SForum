# Decision: ALTCHA 场景绑定与滚动发布兼容分支（P3-4）

## Status

Accepted（2026-11-16）。含一条**限时兼容分支**：无 `data.purpose` 的历史 challenge 仍被
放行。它的移除条件与日期写在 §3，不是「以后再说」。

## Context

修复前 replay 键是 `sha256(purpose + provider + token)`
（`apps/api/app/Support/HumanVerify/service.go` 的 `replayKey`），但 ALTCHA provider 的
`Challenge(ctx, purpose, subject)` 不用 purpose、`Verify` 也不比对
（`altcha_provider.go`）。后果：同一个解出的 token 可以在 `register` /
`password_reset` / `email_verification` / `login_risk` / `post_risk` **各消费一次**，
反自动化强度被摊薄——尤其「注册」与「密码重置」共用一个 token 时，门槛明显低于设计意图。

修复分两步：①replay 键去掉 purpose，一个 token 全局只能消费一次；②把 purpose 绑进
challenge 的签名载荷，让 token 与签发场景强绑定。

## Decision

### 1. 绑定机制：`parameters.data` 在 HMAC 覆盖范围内

| 环节 | 事实 | 依据 |
| --- | --- | --- |
| 签发 | `altcha.CreateChallengeOptions.Data` 写进 `ChallengeParameters.Data`，`signChallenge` 对 `canonicalJSON(challenge.Parameters)` 做 HMAC —— **data 参与签名** | `altcha-lib-go/v2@v2.0.0-20260512100103-f14102c7e9bd/altcha.go` 的 `CreateChallenge` / `signChallenge` |
| 校验 | `VerifySolution` 对**收到的** parameters（含 data）重算签名并常量时间比对 | 同上 `VerifySolution` |
| 客户端 | `altcha@3.5.0`（`apps/web/package.json`）v2 payload 分支是 `{challenge:{parameters: challenge.parameters, signature}, solution}`，**parameters 原样回传** | `apps/web/node_modules/altcha/dist/main/altcha.js` 的 payload 构造 |

因此把 `purpose` 放进 `parameters.data` 后：客户端改不动它，**剥掉它会让签名失配**。
仓库内四处 challenge URL 的 purpose 与后端 verify 的 purpose 一一对应
（`register` / `password_reset` / `email_verification` / `login_risk`），绑定不破坏任何
现有流程。

实现：`altcha_provider.go` 的 `Challenge` 写
`Data: {"purpose": string(purpose)}`；`Verify` 在签名与解校验通过后比对
`data.purpose` 与 `req.Purpose`，不匹配返回 `CodeInvalid`。

### 2. 兼容分支：为什么放行「没有 data」的历史 challenge

滚动发布期间旧实例仍在签发不含 `data` 的 challenge（签名有效，只是不带场景信息）。
若新代码一律拒绝，窗口内**已经打开的表单**会全部失败（用户重载才会拿到新 challenge）。

- 窗口上限 = 一个 `ChallengeTTL`：`NewService` 默认 10 分钟（`service.go`），生产按
  `human_verification.altcha.challenge_ttl` 可配。
- 判定实现：`boundPurpose` 返回 `ok=false` 表示「无绑定」，走放行；字段存在但不是字符串
  返回 `("", true)`，按**不匹配**处理——形态异常绝不放行。

**放行不构成绕过**：签名覆盖 `data`，攻击者无法从一个带 `data` 的 token 上把它剥掉
（`TestAltchaProviderRejectsStrippedChallengeDataSignature` 钉住：剥掉 data 后重新编码，
签名失配）。同时跨场景复用本身已由第 ① 步的全局单次消费兜住
（`TestServiceRejectsReplayedTokenAcrossPurposes`）。

### 3. 失效时间点与移除条件

| 项 | 值 |
| --- | --- |
| 发布日 | 2026-11-16 |
| 兼容窗口上限 | 一个 `ChallengeTTL`（默认 10 分钟） |
| **可删除该分支的日期** | **2026-11-30 之后**（发布日 + 14 天，远大于一个 ChallengeTTL，留足滚动发布与回滚余量）；或**下一个 minor 版本发布时**——两者取先到者 |

可执行判定（两条同时成立即可删）：

1. 线上所有实例版本 ≥ 本次发布版本且已持续运行满一个 `ChallengeTTL`；
2. `rg -n "boundPurpose" apps/api` 只剩 `Verify` 的比对调用，不再需要 `ok=false` 分支。

删除范围：`altcha_provider.go` 里 `boundPurpose` 的「无 data 即放行」语义（改成
`data.purpose` 缺失即 `CodeInvalid`）、`Verify` 注释中的兼容段落、以及
`TestAltchaProviderAcceptsLegacyChallengeWithoutPurposeBinding`。
**保留** `TestAltchaProviderRejectsStrippedChallengeDataSignature`——「签名覆盖 data」
这一性质不随兼容分支消失。

## Consequences

- 兼容窗口内（≤ 一个 ChallengeTTL）由旧实例签发的 token 不受场景绑定保护，只受全局单次
  消费保护。窗口结束后不存在此类 token。
- 旧 replay 键格式（含 purpose）仍留在 Redis；新代码只写新键，切换窗口内旧 token 最多可能
  被再消费一次。旧键随 `MarkUsed` 的 TTL 自然过期，**不需要清理**（`replayKey` 注释已写明）。
- 客户端契约要求：ALTCHA payload 必须**原样回传** `challenge.parameters`。参考实现
  （altcha@3.5.0）满足；若有第三方/原生客户端自行重建 parameters，会因签名失配而失败。
  这是场景绑定的代价，属已知契约约束，不在服务端放宽。
- 场景绑定不替代全局单次消费：两者各守一面（绑定守「不能换场景」，单次消费守「不能重放」），
  任一单独存在都不够。

## 相关

- 实现：`apps/api/app/Support/HumanVerify/service.go`（`replayKey`）、
  `apps/api/app/Support/HumanVerify/altcha_provider.go`（`Challenge` / `Verify` / `boundPurpose`）。
- 测试：`apps/api/app/Support/HumanVerify/service_test.go`
  —— `TestServiceRejectsReplayedTokenAcrossPurposes`（跨场景重放）、
  `TestReplayKeyScopesProviderButNotPurpose`（键不含 purpose、仍按 provider 分域）、
  `TestAltchaProviderBindsChallengeToPurpose`（场景比对）、
  `TestAltchaProviderAcceptsLegacyChallengeWithoutPurposeBinding`（兼容分支，§3 一并删除）、
  `TestAltchaProviderRejectsStrippedChallengeDataSignature`（签名覆盖 data）。
- 台账：`knowledge/plans/2026-11-16-security-audit-31.md`（P3-4）。
