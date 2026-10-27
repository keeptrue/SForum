# P1 审查口径：recovery 退出通道必须 fail-closed

适用：task-3 的 recovery 相关 diff（`apps/api/cmd/sforum/recovery*.go`、
`apps/api/app/Support/Extensions/LifecycleRecovery/*`，以及它们的测试）。
口径只有一句：**任何不确定都落到拒绝侧；没有"尽力修一下"。**

## 判定原则

1. 拒绝必须发生在副作用之前：参数/artifact/地址校验在建立连接与开事务之前完成。
2. 拒绝必须无痕：事务里任何一步判定失败，整笔回滚，库里不能留下半修状态。
3. 成功必须有审计：成功修复写 `audit_events`，写失败则回滚修复本身。
4. 退出码是契约：定向修复找不到/不匹配 → 非 0；幂等巡检无候选 → 0 且打印显式 no-op。
5. 恢复通道是带外工具，不得装载应用配置或插件代码（否则等于绕过启动健康检查）。

## 检查项

| # | 必须 | 判据 | 现有证据（改前） |
|---|---|---|---|
| R1 | 必须 | `--reason` 缺失、全空白、超 4096 字符 → 拒绝，且拒绝发生在连库之前 | `recovery_lifecycle_close.go:67-78`；`TestExtensionRecoveryLifecycleCloseValidatesArgumentsWithoutDatabase` |
| R2 | 必须 | 非法 artifact 参数（缺 `--expect-version/-digest`、digest 非 64 位小写 hex、`--expect-revision/-version-id` ≤ 0）→ 拒绝且不触库 | `recovery.go:154-158`、`recovery.go:319`、`recovery_stale_builtin.go:34-38` |
| R3 | 必须 | artifact 与库内证据不一致 → `errRecoveryArtifactMismatch`，不得"降级为尽力修"；逐字段比对 revision/versionId/version/digest | `recovery_stale_builtin.go:59-62`、`:70-110` |
| R4 | 必须 | 未过期 lease → `errLifecycleCloseLeaseActive`，且不写库；`--force` 是唯一放行口，必须进审计 metadata | `recovery_lifecycle_close.go:180-190`、`:224-235`；`TestExtensionRecoveryLifecycleCloseRejectsActiveLeaseWithoutForce` |
| R5 | 必须 | 超时判定用数据库时钟 `statement_timestamp()`，且 `lease_expires_at == now` 视为已过期（严格 `>`）；不能用 Go 时间 | `recovery_lifecycle_close.go:183` |
| R6 | 必须 | 并发重复触发只允许一方成功：operation 用 revision CAS（`:214`），activation 恢复用 `FOR UPDATE` + 加锁后重判 predicate（`activation_state.go:305`） | 代码在；**无并发测试** |
| R7 | 必须 | 证据漂移 → 整笔回滚并 no-op（`RowsAffected != 1` 时返回 false 而不是部分提交） | `activation_state.go:231`、`:246` |
| R8 | 必须 | 退出码：定向修复（quarantine / recover-stale-builtin / lifecycle-close）找不到或不匹配 → 进程 exit 1；仅 activation-state 0 候选可 exit 0，且必须打印 "no interrupted activation publication matched…" | `cmd/sforum/main.go:8-13`；`recovery_activation_state.go:44-46` |
| R9 | 必须 | 成功修复写审计且 metadata 带 reason/operationId/extensionId/force；审计写失败回滚修复 | `recovery_lifecycle_close.go:235-243`；成功路径有断言（`recovery_lifecycle_close_test.go:221-229`），**审计失败回滚无测试** |
| R10 | 必须 | 恢复路径只连 PostgreSQL：不得 `config.Load`、不得启动 plugin runtime / 子进程 | `withRecoveryPool`（`:126-131`）、`withRecoveryRepository` |
| R11 | 必须 | 保护边界不放宽：`disable` 拒绝 builtin/system（`recovery.go:241`）；`quarantine` 只接受 builtin/system（`:343`）；`recover-stale-builtin` 要求 builtin/system + `status=installed` + artifact 已漂移（`recovery_stale_builtin.go:103-112`）；`--force` 只放宽 lease | 代码在 |
| R12 | 必须 | 数据库地址来源：`--database-url` 优先，空则 `DATABASE_URL`，两者都空 → 拒绝；不提供隐式默认 | `recovery.go:184-191`、`recovery_lifecycle_close.go:124-131` |
| R13 | 建议 | 拒绝错误文案要能对上现场：already-closed 必须同时报 `state`/`terminal`/`completedAt`（否则与观测到的阶段名互相矛盾） | `recovery_lifecycle_close.go:110-119`；`TestExtensionRecoveryLifecycleCloseRejectsClosedOrMissingOperation` |
| R14 | 建议 | 幂等：第二次运行 no-op，不重复写审计 | `TestRecoverInterruptedActivationsRestoresExactSourceAndIsIdempotent` |

## 现有实现的已知缺口（三条都是**阻塞项**，task-3 交付时缺一即判不通过）

Lead 决定（2026-10-27）：这三条不再按"建议"记录，必须补齐，否则 task-3 不予通过。

- R6 缺真并发测试：两个 pool / 两个 goroutine 同时关闭同一 operation，断言只成功一次、审计只一行、失败方拿到 already-closed/concurrent 错误。
- R9 缺"审计写失败 → 修复回滚"的测试（quarantine 有 commit-failure 回滚测试，lifecycle-close 没有对应项）。
- R8 缺进程级退出码断言：只测 RunE 返回 error 不够，要覆盖"定向修复找不到 → exit 1"与"activation-state 0 候选 → exit 0 + no-op 文案"两种进程行为。

## 复跑命令

```bash
cd apps/api
go test -count=1 -run 'TestExtensionRecovery|TestPostgresRecoveryRepository|TestProtectedQuarantine|TestRecoverInterruptedActivations' ./cmd/sforum ./app/Support/Extensions/LifecycleRecovery
```

需要真库时走隔离集群（不要用开发库）：

```bash
./scripts/test-api-isolated.sh -count=1 -run 'TestExtensionRecovery|TestPostgresRecoveryRepository|TestRecoverInterruptedActivations' ./cmd/sforum ./app/Support/Extensions/LifecycleRecovery
```
