# 2026-11-15 审计簇 12 收口（扩展信任链）

簇 12 可执行集 8 条，来自 10 条减掉 `INC-01` / `INC-02`（已由
`decisions/2026-11-15-audit-noise-items-accepted.md` 摘出缺陷池，分母 71 → 61）。
本轮 7 提交、HEAD `230464ebd`：**6 条闭环，2 条部分**（EX2-01 / RV-03）。

## 升级前必读：四项行为变更

1. **`install-from-url` 的 `sha256` 改必填**：缺值在任何出站请求之前 422
   `extension.remote_digest_required`；契约 `ExtensionRemoteInstallInput.required` 已加，后台面板
   同步字段级拦截（不发请求）。没有静默豁免开关。
2. **`raw_core` 的 DML 面从 public 165 表 + 89 序列收到 3 张派生投影表 + 1 条序列**
   （`search_documents` / `search_index_state` / `search_reindex_runs` +
   `search_reindex_runs_id_seq`）。对已装插件是行为变更：越界读写首次使用即
   `permission denied for table <name>`（SQLSTATE **42501**）。迁移方向：读走 `sforum_core_v1`
   视图或 HostQuery，写内容走 Host Command 或自有投影表，写配置走 options / admin API。
3. **插件 `global_middleware` 不再默认覆盖 Core terminal**：`targetId` 留空 = 只盖插件自己的
   terminal；要包住某条 Core 路由必须显式写 `targetId`（仍禁 Path / Methods）。
4. **插件子进程 cwd 变了**：从宿主 cwd 改为 `backend.entry` 所在目录。内置插件 0 处依赖宿主 cwd，
   **第三方插件受影响**（`./cache`、`os.ReadFile("config.json")` 现在落在包目录内）。这条连同进程组 /
   rlimit / env 白名单 / 文件系统·网络·子进程不受限，写进了 `docs/extensions/build-and-load.md` 与
   `docs/zh-CN/extensions/build-and-load.md` 的「Runtime process contract / 运行时进程契约」。

## Changed

- 执行信任链（EX2-04b / RV-12 / EX2-01 部分）：`VerifyRuntimeExecutableDigest` 在 exec 前对齐
  trusted / fresh（`digestStableExtensionFile` 按内容重算 + 句柄前后比对）/ declared（`manifest.Backend.Digest`）；
  ①≠② 视为指纹被伪造（拒绝启动 + `InvalidateVerification`），③≠② 视为制品非声明副本。复核点固定在
  `verifyPluginExecutableArtifact`（grant 解析后、租约签发前）。
- 进程加固：新子包 `pluginprocess`（`Apply` / `GroupID` / `KillGroup` / `Limit`）——独立进程组 +
  整组回收、`RLIMIT_NOFILE=4096`、`RLIMIT_CORE=0`、`SFORUM_PLUGIN_NPROC` opt-in、cwd 钉制品目录；
  回收顺序收成一个 helper（先 `KillGroup(-pgid)` 再 `client.Kill()`），启动失败分支与
  `Stop` / `Discard` 共用，一条测试守住两条路径。
- 链接安装（RV-12，web）：`normalizeSha256` 空值不再合法并按 `empty` / 格式区分，摘要错误渲染到
  `checksumError` 字段且不发请求，`installFromUrl` 摘要改必填并始终写入 body，`INSTALL_ERROR_KEYS`
  补 `extension.remote_digest_required`；删掉 `checksumVerified`，徽标改由 `resultSource === 'url'`
  归因。中英文案去掉「可选 / optional」。
- `raw_core` 白名单（EX2-02）：`extensionDatabaseRawCoreWritable{Tables,Sequences}` 是唯一来源；
  授权侧按名字核对 relkind 与 owner（`sforum_core_o_*`）fail closed，校验侧仍枚举 public 全量做安全网
  （白名单外零 ACL）。给 `users` 补一条 INSERT 会让租约签发失败
  （`extension database physical resource conflict`），不会悄悄生效。
- 视图 REVOKE 棘轮（EX2-07）：新迁移 `202611150002_stable_core_views_public_revoke.sql`（不改历史
  迁移，Down 显式 no-op）；`stable_core_views_public_revoke_test.go` = 扫全部迁移 Up 段的静态棘轮 +
  实库 `has_table_privilege('public', …)` 全 false 且 `pg_relation_is_updatable(...) = 0`。
- 恢复态不回显（RV-05）：`recoveryPublicView`（code / component / message），`artifacts[]` 与
  `publicationRevision` 只出现在 `/api/v1/ready` 或认证管理面；用白名单结构体而非删字段。
- 守卫目录可兑现（RV-03 部分）：revision restore 绑 `requireAllDeclaredCoreGuardPermissions`（AND）、
  `comment_edit` 复用 `forumCommentWriteGuardEvaluator`；启动期 `ValidateCoreGuardCatalogCoverage`
  让 boot 失败，测试期 `TestProductionCoreGuardCatalogCoverage` 要求
  `unboundCatalogEvaluatorAllowlist` 为空；绑定数 34 → 37。
- global 作用域（EX2-05）：`registry.Match`、`BuildExecutionPlan` 的 global 分支、
  `policy_snapshot.routePolicyContributionApplies` 共用 `globalMiddlewareScopeApplies`；
  `v3_validate.go` / `prepare.go` 允许 global 带 `targetId`，Host 保留路由
  （`core.route.system.health` / `core.route.system.ready` / `core.route.notifications.stream`）仍不可指向。

## Decisions

- exec 前按内容复核，不缩 TTL 也不砍缓存：`GuardPolicyCatalog` 每 5 秒遍历全部扩展，缓存是刚需；
  只把唯一的不可逆动作（exec）设成内容级复核点，不新增第二套校验。
- 摘要必填做成硬拒绝，不做「按响应内容计算比对」：拿响应证明响应挡不住投毒的下载链路，只会给调用方
  一个「已校验」的错觉；也不留能被默认打开、忘记关闭的豁免开关。
- `raw_core` 准入标准只有一条：表里的行是可由宿主或插件重建的派生数据。所以只有 3 张搜索投影表进
  白名单，内容表与配置表一律不放——扩大爆炸半径必须是一次显式可审计的代码变更。
- RV-03 选「实现」而不是从目录删声明；同时明确不把 core step 接进 dispatcher 裁决，否则会出现
  「同一请求有插件修饰器时被裁决、没有时不裁决」的分裂边界。
- global 作用域用显式 `targetId` 而不用 path 前缀：按前缀排除正是审计指出的问题，等于把安全边界寄托
  在字符串上。没做 uid 切换（`SysProcAttr.Credential` 只在 euid==0 可用，且要整套属主/chmod 模型）。

## Next

- EX2-01 剩余：内核级隔离（user / mount / network namespace、seccomp、cgroup v2）与 exec 路径 TOCTOU
  闭环（`O_PATH` + `fexecve`、只读 bind mount + 不可变制品目录、launcher 前移 rlimit +
  `no_new_privs`）；按代价从低到高的 5 级路线在决策记录末尾。
- RV-05b：恢复态匿名 `GET /api/v1/ready` 仍返回 `artifacts[]`（`ReadyReport.Recovery` 内嵌
  `RecoveryRequirement`），`/ready` 被 `recoveryOnlyMiddleware` 放行，部署链（Caddy → Web → API）
  匿名可及。`TestRecoveryOnlyModeExposesOnlyHostHealthAndReadiness` 目前**正向断言**该行为，所以收紧
  它是一次契约变更，不是补测。
- EX2-05b：`TrustGuard` 只有 Path / Methods / Access / Permission，没有 `routeAction` 与 `targetId`，
  管理员批准时看不到插件的 global middleware 会包住哪条 Core 路由。
- `manifestRouteTargetForbidden` 的 `RouteActionGlobalMiddleware` 分支（global 带 Host 保留路由
  `targetId`）没有专门用例，现有 forbidden-target 断言都在 replace / 可变字段路径上；补一条 fixture。
- 审计总表 §11 点算待更新：本轮 6 闭环 / 2 部分，另新登记 RV-05b、EX2-05b（尚未进 113 条编号）。

## 验证

- `./scripts/test.sh --fast` exit=0：api 255s、web 55s（3128 pass）、static 15s（44 tests）。
- `node tests/validate-architecture-boundaries.mjs` passed(2276)；contracts-gate 全过；
  catalog `--check` 18 files current。

## Open Questions

- `/api/v1/ready` 要不要分级？匿名只回 `status`、完整报告要求认证或仅内网——会牵动 deploy 的就绪
  探针，要一起改。
- 插件与宿主同 uid 的现状要不要排期解决？收益是「插件能写别的插件目录」这类问题从根上消失，代价是
  制品属主 / chmod 模型 + launcher。
- 内置包（`SourceBuiltin`）不走 grant 查询，未声明摘要时只剩 exec 前重算一层；要不要把
  「executable 一律要求声明摘要」变成硬性要求？
- `knowledge/index.md` 还剩两处同类漂移，本轮只改数字与指针所以没碰：`## Active Workstreams`
  审计段的「唯一未闭环项：RV-04 / UX2-11 运行时验收」是上一轮口径（实际还有 55 条待修）；`## Latest
  Handoff` 里 2026-11-11 及更早的 17 条 bullet 仍带「，最新」。下次动 index 时顺手清。
- `isPluginRouteTargetNone` / `validatePluginRouteTarget` 落在 `plugin_command_transport.go` 里，
  与文件主题命名失配；纯函数迁移会牵动 `protocol_test.go`，等下次动路由目标校验时一并拆。
