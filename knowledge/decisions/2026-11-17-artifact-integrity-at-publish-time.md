# Decision: 制品完整性在发布期 fail-closed，dev 自愈只在 development，republish 只写 durable 事实

## Status

Accepted（2026-11-17，issue #256，事故 `recovery_required` 之后）。

本文只固定**取舍与撤销条件**。实现进度（2026-11-17 当日）：P1（#253 可诊断性：
`plugin_artifact_incomplete`）已落 `5f857cc38` / `ae2487303`；只读自检与恢复通道已落
`c7e80dd51`（#254，`sforum extension verify-artifacts` +
`sforum recovery republish-artifact`）；**D1 的发布期部分已落地**（本 PR 落地：
`insertPluginRuntimePublication` 在写 publication/member 之前，按成员的
`extension_versions.package_path` 逐个跑 `ArtifactIntegrity.Verify`，落点
`apps/api/app/Models/Extensions/plugin_runtime_publication_postgres.go:36`，9 个生产
发布调用点全部继承、无 skip 开关与 reason 豁免）；**D2 dev 自愈已落地**（本 PR 落地：
入口 `maybeHealPluginRuntimePublication`，开关
`devhygiene.ShouldEnableDevelopmentOrphanReaper(cfg.AppEnv) && !cfg.SafeMode`，候选源含
内置源 `storage/builtin-dev`；生产与 Safe Mode 不调用自愈，见下）。规范条文见 `AGENTS.md` 的
"Extension artifact directories are **read-only immutable facts**" 起五条，运行时现状见
`knowledge/modules/extensions.md` 的 "Artifact Integrity And Recovery"。

## Context

2026-11-17 全站 `/api/v1/*` 返回 503 `plugin_runtime_convergence_failed`，四次启动
（17:12 / 17:29 / 17:34 / 18:57）全部复现，`plugin_runtime_nodes.last_applied_revision` 恒为 0。

已核实的现场事实：

- `plugin_runtime_publications` 最新 `revision=1540`（2026-11-15 19:14:51），16 个成员，其中
  `sforum-shortcodes@1.3.10` 的 `package_digest = b4f399bd…d6d74`。
- 该制品目录在盘，但**缺 `backend/plugin`**：28 个文件在、1 个不在（`backend/` 下 12 个
  `.go`/`go.mod`/`go.sum` 都在，缺的是清单声明的入口二进制）。`backend/` 子目录 mtime
  `2026-11-17 17:34:53`，其余文件 mtime `2026-11-15 19:14:09` —— 即**发布时是完整的，
  17:34:53 被带外删除**，正对上第三次启动失败的时刻。
- 失败原文（P1 取证）：
  `apply plugin runtime revision 1540 attempt 1: stage sforum-shortcodes@1.3.10: backend entry backend/plugin is not available`
- 该原文当时**没有类型化分支**，落进
  `pluginRuntimeRecoveryMessage`（`apps/api/bootstrap/api_assembly_extension_restore.go`）的
  default，对外只剩 "inspect API logs for the exact cause"。所以本次事故的第一层缺陷不是
  「文件被删」，而是**删掉一个文件就能让整个站点不可诊断地停摆**。
- 生命周期表最新 10 条全 `failed`、无开放 operation；不是 fence 卡死那一类。

### 关键引理：坏制品不能「补文件」

`b4f399bd…` 的 manifest 声明 `backend.digest = b29b4775581dc792d1135f1bfd8ecf4bc7bb02e6b03f77bd083f9ec26f1726f4`。
实测该包全部版本（1.3.4–1.3.10，共 35 个 `backend/plugin`）的 sha256 无一等于该值；
1.3.10 的另外三个 digest（`241123ac…` / `3456483a…` / `70e02fb2…`）各自的
`backend.digest` 分别是 `ed8d1f55…` / `2003044e…` / `bb535bb1…`，都与 `b29b4775…` 不同
（同一 digest 目录内 `backend.digest` == 该目录 `backend/plugin` 的 sha256，已验证）。

推论：**从别的目录拷一个二进制进 `b4f399bd…` 既不会等于 `backend.digest`，也不会让目录树
摘要回到 `b4f399bd…`**，stage 依旧 fail-closed。唯一合法的修复是「发布一个自洽的制品，并把
durable 事实重指向它」。这条引理决定了下面三条决策的形态——尤其是 D3：republish 必须是
「写 durable 事实」，不能是「修盘」。

## Decision

### D1 完整性校验放在**发布期**，apply 期校验保留但不作主闸门

发布期 = 写 `plugin_runtime_publications` / `..._members` 之前；apply 期 = 启动收敛把
publication 落到运行时的过程。

理由：

1. **发布期是唯一还能「不写」的时刻。** publication 是追加式不可变表（immutable trigger），
   写进去就只能追加新 revision 覆盖，不能原地改。校验放在写入前，坏制品的代价是「这次发布
   失败、上一 revision 继续当权」；放在 apply 期，代价是「durable 事实永久指向坏制品」，
   每次启动都重新失败——本次正是后者。
2. **失败半径不同。** 发布期失败是局部、可回退的（旧 revision 仍权威，站点继续跑旧版本）；
   apply 期失败是全局 fail-closed（recovery-only，全站 503）。同一份校验，放在前者的失败
   成本低一个数量级。
3. **apply 期校验不能替代发布期校验。** 两者查的不是同一件事：发布期保证「写进去的都是好的」，
   apply 期保证「盘上现在还是好的」。带外删除、磁盘损坏、rsync 漏文件都发生在发布之后，
   只有 apply 期能看见。所以是**纵深防御**，不是二选一。
4. 代价：发布路径多一次目录遍历 + 摘要计算（16 个成员量级，可接受）；且发布必须能失败——
   不能再有「先写进去，坏了下次再说」的路径。

### D2 dev 自愈只在 `APP_ENV=development`

自愈 = 检测到「最新 publication 指向坏制品」时自动重建 stage 或自动重写 publication 指向
自洽制品，不要求人工介入。

理由：

1. **自愈绕过的是「写之前人看一眼」这个 checkpoint。** dev 的制品是本地 `go build` /
   `bun build` 产物，随时可重建，坏制品是开发噪音，自动重建收益高、风险低。
2. **生产里 publication 是「当前全站到底在跑什么」的权威记录。** 让进程自动改写它，等于让
   被审计者自己改审计事实：事故复盘时无法回答「什么时候、谁把哪个 digest 换掉了」。生产侧
   的等价能力必须是 CLI（人工、显式、可审计），不能是启动时的自动行为。
3. **与既有 dev 通道的授权前提一致。** `decisions/2026-10-17-dev-zero-command.md` 已确定：
   dev 零命令通道的授权前提是「能写 dev source root ≈ 操作员」，且该通道**从不注入到
   `APP_ENV != development`**（Safe Mode 也拦）。自愈必须挂在同一道闸门上——不能新开一条
   「只在 dev 生效、但不受该闸门约束」的旁路，否则等于在 dev 与生产之间加了一条未经论证的
   权限落差。
4. 代价：dev 与生产的启动行为有分叉。接受——分叉点只有一个（是否自愈），且有
   `APP_ENV=development` 这一个可断言的开关，不是散落的隐式行为。

### D3 `republish` 只写 durable 事实，不做热恢复

`republish` 的语义（已实现，`c7e80dd51`）：`sforum recovery republish-artifact <extension-id>
--reason TEXT [--confirm-token TOKEN]` → 只读 durable 行 + 盘上制品，发现该成员不可装载 →
在**同一版本**的其它 `extension_versions` 行里找自洽候选 → 追加一条新 publication revision
把该成员重指向候选 digest → 退出。它不启动插件、不重建运行时、不热修当前进程；不带
`--confirm-token` 时只报告（dry-run，exit 1），写入只发生在带 token 的那一次事务里。

理由：

1. **它要修的是 durable 事实，不是内存状态。** 坏的是「publication 指向了不存在的文件」，
   修法是让 publication 自洽（见「关键引理」：补文件不可能成功）。热恢复既修不了 durable
   事实，也不改变下次启动的结果。
2. **收敛失败时运行时已被判定不可信。** 进程处于 recovery-only 时，runtime 状态正是那个
   「验证失败」的对象；在一个已判定不可信的运行时上重建运行时，等于用坏掉的尺子量坏掉的桌子。
3. **热恢复会引入第二个分歧来源。** 本次事故的形态就是「durable 事实」与「磁盘现实」分歧。
   如果 republish 顺手热恢复，就会再造一个「durable 事实 vs 运行中状态」的分歧点，
   而后者没有审计记录。
4. **可验证性。** 「重启后能否过启动闸门」是一个二值断言，有现成的 fail-closed 路径和日志；
   「热恢复后是否真的健康」需要另一套断言，而那套断言恰好是被判定不可信的那个组件提供的。
5. 与既有原则一致：`AGENTS.md` 已声明 safe mode / pre-plugin boot health / out-of-band CLI
   recovery 是 host-owned 且不可覆盖；CLI 恢复的价值就在于它不依赖插件运行时。
6. 代价：republish 之后必须重启一次。接受——恢复场景本来就在停机/恢复态，重启不是额外成本。

## Alternatives Rejected

- **只在 apply 期校验（现状）。** 已由本次事故否证：durable 事实写坏后每次启动都失败，
  且失败对外的 message 是 default 分支。
- **发布期与 apply 期只留一个。** 两者查的是不同时间点的不同事实（写入时 vs 现在），
  去掉任一个都会留下盲区。
- **republish 直接修盘（补文件 / 从别的 digest 拷二进制）。** 由「关键引理」否证：
  两个摘要都不可能对上，stage 仍失败，而且会把「目录内容」改成第三个不自洽状态。
- **dev 自愈扩到所有环境（或加一个 `AUTO_HEAL=1` 通用开关）。** 会让进程改写自己的审计事实；
  生产需要的是可归因的 CLI 动作。
- **republish 后热恢复。** 见 D3：修不了 durable 事实，且在不可信运行时上重建运行时。
- **给 stage 失败加「跳过该成员继续启动」。** 等于把 exact-artifact 变成 best-effort：
  跳过一个成员就跳过了它声明的路由/权限/数据契约，站点会以「少了一半插件」的状态对外服务，
  比 503 更难诊断。

## Consequences

- 发布路径新增一次全成员完整性校验；校验失败必须 abort，不能写半条 revision。
- 启动路径保留 fail-closed，但 artifact 类失败从此命中 `plugin_artifact_incomplete`
  （`/api/v1/ready` 的 `recovery.code` 与 503 body 的 `data.code`）；旧 code
  `plugin_runtime_convergence_failed` 仍是兜底。
- 运维多两条动作：发布前/CI 跑只读自检
  `sforum extension verify-artifacts [--root apps/api]`（exit 0/1/2）；坏制品用
  `sforum recovery republish-artifact <id> --reason TEXT [--confirm-token TOKEN]` 重写 durable
  事实后重启（不带 token 是 dry-run）。
- dev 自愈（已实现，落点 `apps/api/bootstrap/plugin_runtime_dev_heal.go` +
  `api_assembly_extension_restore.go` 的 `maybeHealPluginRuntimePublication`）只允许在
  `devhygiene.ShouldEnableDevelopmentOrphanReaper(cfg.AppEnv) && !cfg.SafeMode` 时被调用；
  生产与 Safe Mode 下 healer 方法根本不会被调用（有探针测试断言调用数为 0）。它只做
  「找到同 id+version 的另一份自洽制品 → 追加一条 reason=recovery 的 revision」：
  不往任何制品目录写文件、不改既有 `extension_versions` 行（盘上有候选而 durable 行没有时，
  只为新 digest 补一行），并且新 revision 仍要过 D1 的发布期闸门；没有候选（含
  `build-builtin-plugins.sh` 的 `--exclude` 构建窗口）时保持 fail-closed。审计归因取
  dev-auto 系统账号；该账号不存在时 actor 退化为 NULL——自愈**不受**「管理端删除
  dev-auto」这条开关约束（那条开关只关闭 0 命令自动激活通道），它也不会把账号重新建
  出来。
- 已知限制（F2，发布期闸门的代价）：多个受保护成员同时损坏时，quarantine/repair 的
  「发布剩余全量」会互相绊住——把 A 恢复成自洽制品后，发布剩余集合仍要校验 B，B 还坏就
  照样拒绝，于是两台坏制品不能被同一条路径逐个修完。缓解路径是第三方扩展用
  `recovery disable-all` 整体绕开（先摘除全部第三方成员），builtin 则依赖启动时
  `SyncBuiltins` 从源码重铺制品后再恢复。**不许**为此给发布期校验加 reason 豁免或
  verify/skip 开关：那会把 fail-closed 降级成 best-effort，等于退回本次事故的形态。
  dev 自愈受同一条约束，而且更直接：闸门校验整份 full-set，所以「两个成员同坏、只有
  一个有候选」时第一次 repair 就会被闸门以兄弟成员缺失拒绝（实测 healed=0），一次
  启动最多只可能成功自愈**一个**成员，且前提是同 revision 里没有别的坏成员。要覆盖
  多成员，正确方向是「一条 revision 批量替换所有可自愈成员」，不是 reason 豁免。
- 制品目录彻底变成只读事实：**任何**「进目录改一改让它启动」的直觉都是错的，包括
  「只是把文件拷回来」。

## 什么时候可以撤销这条决策

按条撤销，不需要整条推翻：

1. **D1（发布期校验）**：如果 publication 不再是追加式不可变表（改成可变行 + 独立审计日志），
   「发布期是唯一可拒绝时刻」这个前提消失，可以重新评估。撤销前提：同时给出证据证明
   「写坏后可原地修正，且不会把一次写坏放大成每次启动失败」。
2. **D1（发布期校验）**：如果 stage 变成真正的事务（写入即校验、失败自动回滚到上一 revision，
   且磁盘状态与 DB 在同一事务边界内），发布期预校验可降级为性能优化而不再是正确性闸门。
3. **D1（发布期校验）**：如果出现「发布期无法校验、apply 期可校验」的合法制品形态（例如制品
   由外部对象存储按需拉取、发布时尚未落盘），必须先重写本条并给出该形态的恢复通道，
   不能为了兼容它而在发布期放行。
4. **D2（dev 自愈范围）**：只有在 publication 获得不可伪造的归因（actor 绑定 + 签名），
   且自动改写会写入独立审计表的前提下，才允许把自愈扩到非 dev。在那之前不许扩。
5. **D3（不热恢复）**：如果 coordinator 能在「明确不信任当前 runtime」的前提下重建 runtime，
   并通过**与启动完全同一套**收敛断言（同断言、同 fail-closed、失败仍停在 recovery-only），
   且有测试证明「热恢复失败不会半启动」，才可以重新讨论热恢复。
6. 本条决策整体撤销的前提：`sforum extension verify-artifacts` 与
   `sforum recovery republish-artifact` 被移除或改名到无法承担上述职责——那时必须先有
   替代的只读自检入口与不依赖插件运行时的恢复通道。

## Evidence

- 失败原文：`apply plugin runtime revision 1540 attempt 1: stage sforum-shortcodes@1.3.10: backend entry backend/plugin is not available`
- 坏制品：`storage/extensions/sforum-shortcodes/1.3.10/b4f399bd…d6d74/`，28 文件在、
  `backend/plugin` 不在；`backend/` mtime `2026-11-17 17:34:53`，同级文件 mtime `2026-11-15 19:14:09`。
- manifest：`backend.entry=backend/plugin`、`backend.digest=b29b4775…`（全包无同 sha256 副本）。
- 发布：`plugin_runtime_publications` `revision=1540`，16 成员，`2026-11-15 19:14:51`。
- 启动：`plugin_runtime_nodes` 四次注册（17:12 / 17:29 / 17:34 / 18:57），
  `last_applied_revision=0`，`last_seen_at == first_seen_at`。
- 代码坐标：`apps/api/bootstrap/api_assembly_extension_restore.go` 的
  `pluginRuntimeRecoveryMessage`（default 分支）、`apps/api/app/Support/Health/recovery.go`
  的 `PluginRuntimeRecoveryCode` / `PluginArtifactIncompleteCode`。
- 已落地实现：`ae2487303` + `5f857cc38`（#253 类型化 `plugin_artifact_incomplete`，
  四坐标 message）、`c7e80dd51`（#254 只读自检 + republish；退出码 0/1/2 见
  `apps/api/cmd/sforum/artifact_integrity.go` 的 `artifactExitOK/Defect/Usage`）。
