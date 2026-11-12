# sforum.badge-facts

M3 活样本，不是产品插件。

它把「核心事件 → 徽章事实计数」这条链固定成一个静态夹具，让
`scripts/ci/reference-packages-gate.sh` 每次都能真实地 build → digest →
validate → test 它：

- `events[0]`：observe `reaction.created`，handler `sforum.badge-facts.observe`，
  input/result schema 都在 `schemas/` 里。
- `backend/main.go` 的 `InvokeHook` 从事件负载取真实主体 `actorUserId`（不是把
  用户写死、也不是用扩展身份），用投递 id 当 `dedupeKey`，经 SDK
  `Host.RecordBadgeFact` 上报 `sforum.badge-facts.reactions.given`。
- 清单声明 `capabilities: ["host.api", "badges.fact.write"]` 与
  `database.grants: ["host_commands"]`——少任何一个都调不通 Host 命令。
- 回执 `inserted:false` 是幂等重放的正常结果，不是错误；`delta` 的 clamp 只发生在
  Host 侧（SDK 不 clamp）。

## 验证

```bash
bash scripts/ci/reference-packages-gate.sh
cd apps/api && go run ./cmd/sforum extension validate ../../extensions/fixtures/plugins/sforum-badge-facts
```
