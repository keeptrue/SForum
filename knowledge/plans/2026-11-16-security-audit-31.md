# 2026-11-16 安全审计 31 条修复台账

- **状态**：active（2026-11-17 凌晨更新：批次 1 / 2 / 3 全部完成并提交；31 条里已修 22 / 不修 5 / 延后 2 / 转跟进 2。剩余工作见「新增跟进项」F-3 ~ F-8）
- **来源报告**：`~/Code/bug/SForum/2026-11-15_16-43.md`（31 条独立发现：P0 = 0 / P1 = 2 / P2 = 7 / P3 = 22）
- **核实基线**：commit `e0ec3bce6`（`git rev-parse --short HEAD`，2026-11-16；核实期间工作树干净）
- **进展基线**：commit `39fb04e3e`（台账状态以此为准；其后若有新提交，按「当前状态」列自行补行）
- **核实方法**：逐行代码验证 + 3 路只读核查代理（三个时间点独立重验同一批断言；核查代理只读，不改仓库）
- **核实结论**：属实 19 / 部分属实 7 / 不属实 5
- **实施结论**：已修 22 / 不修 5 / 延后 2 / 转跟进 2 = 31（2026-11-17 凌晨口径；批次 3 后端 7 条与本轮基线回归一并收口）
- **与主审计档的关系**：本批与 `~/Code/bug/SForum/00-SForum-audit-master.md` 零重叠。主审计线已「收口 15 轮」，但这 31 条没有任何台账跟踪——**本文件是这批发现唯一的跟踪记录**。

> 本台账只记录核实结论、批次与关闭条件，不重述报告全文。动手改任何一条前，先读报告对应小节（机制、证据行号、修复建议都在那里）。

## 结论口径

| 结论 | 含义 |
| --- | --- |
| 属实 | 报告描述的机制在 `e0ec3bce6` 上成立 |
| 部分属实 | 机制成立，但报告的量级 / 返回码 / 作用域 / 行号有偏差（见「报告自身的 4 处偏差」） |
| 不属实 | 在 `e0ec3bce6` 上已不成立：已修，或实现本就具备该防护 |

当前状态列：`已修` = 已提交或核实前已具备防护（附 commit）；`在办` = 已派单未完成（附 task 号与 owner）；`待派` = 已确认要修但尚无 owner；`不修` = 明确不做（理由见「不修」）；`延后` = 触发条件驱动（见「延后项」）。

批次列：`1/2/3` = 修复批次；`0` = 本台账；`—` = 已修或不修，不排批次；`延后` = 触发条件驱动。

## 31 条台账

| 编号 | 标题 | 核实结论 | 当前状态 | 修复批次 | 关闭条件 |
| --- | --- | --- | --- | --- | --- |
| P1-1 | 用户级 deny 覆盖被父权限展开复活（细粒度子权限 deny 全部失效） | 属实 | 已修 · `c382e366b` + `fcdc86eda` | 1 | deny 子权限 + 角色持有父权限时 `Actor.Can(child) == false` 且 detail.Permissions 不含该键；`roles_test.go` 新增该用例并通过 |
| P1-2 | legacy 前缀 `/api/v1/extensions/:id/*` 绕过 custom/raw_request 守卫，raw 权威还转发登录凭证 | 属实 | 已修 · `97659db29` + `10f80ccf1` | 1 | legacy 前缀 × 四种守卫类型的 allowed/denied 测试通过；raw_request 凭证只在守卫通过后转发 |
| P2-1 | 登录人机验证被客户端可控字段 `issueApiToken` 一键豁免 | 部分属实 | 已修 · `39fb04e3e` | 2 | 带 `issueApiToken` 的登录仍受 `RequiresVerification` 约束；decision `2026-10-22` 第 5 条与代码一致（或 decision 同步改写） |
| P2-2 | PAT 完全不受 token_version 失效与「强制下线全部设备」约束 | 属实 | 在办 · task-13（backend） | 2 | 密码重置 / 强制下线后旧 PAT 鉴权失败；管理员代撤销端点有 allowed + denied 测试 |
| P2-3 | 举报处置（HideAndClose/DeleteAndClose）直写状态但不触发公开缓存失效 | 属实 | 在办 · task-12（backend） | 2 | 处置提交后公开读接口在 TTL 内立即不可见（列表 + 详情双断言） |
| P2-4 | Idempotency pending 无租约与崩溃清理（同 Key 最长 24h 全部 409） | 部分属实 | 在办 · task-11（backend） | 2 | panic 后同一 `Idempotency-Key` 在短 TTL 后可重试；Complete/Abort 走 CAS，只有 lease 持有者能收敛 |
| P2-5 | 主题生命周期计数多路径口径不一致（locked 双计 / 漏计 / 永久少计） | 属实 | 在办 · task-12（backend） | 2 | 「锁帖 → 作者编辑触发预审 → 审核通过」与「删除 locked 帖」后 `ListTopics` total 与真实可见数一致 |
| P2-6 | mail_deliveries 无终态收口：重试耗尽后永久停在 sending，邮件静默丢失 | 部分属实 | 待派 | 2 | 重试耗尽写 `DeliveryDead`；sweeper 把超龄 sending/queued 转 dead；replay 端点可用 |
| P2-7 | en-US 语言包整棵缺失 `userAppearanceSettings` 子树（28 key） | 不属实(已修) | 已修 · 核实前基线 `e0ec3bce6` | — | 无需动作：en-US 已有 28 个 leaf key，且 `apps/web/tests/framework/i18nCatalog.test.ts` 已棘轮 |
| P3-1 | Bearer PAT 校验每请求无条件写库刷新 last_used_at（无节流） | 属实 | 已修 · `c0ab8d80d` + `7218be6c6` | 3 | 同一 token 高频请求不再产生逐请求 `UPDATE`，写入按阈值节流且 last_used_at 语义不变（PG 集成 `TestTouchLastUsedThrottlePostgres` 四子用例 + 变异必红；严格 `<` 边界与「跨语句等值必然过期」已钉成断言） |
| P3-2 | LoginLockout 计数键 TTL 依赖单次 Expire 成功 | 属实 | 已修 · `191c4e441` + `56da3585d` + `2de3e6ff3` | 3 | 首写与 TTL 压进单次 EVAL 原子完成，`PTTL<0` 自愈历史无 TTL 键；真实 Redis 用例在 CI 口径下**不再 skip**（无 `SFORUM_TEST_REDIS_ADDR` 时 `t.Fatalf`），Lua 语法错误在脚本口径下必红 |
| P3-3 | post_risk 人机验证场景全链路存在但发帖/回帖路径从未执行 | 属实 | 已修 · `70c1ab86f` + `f297f8bea` + `8e483b623` + `1c9747aa8` | 3 | 选 A 接线：发帖/回帖在写入前调 `Verify(PurposePostRisk)`，开关关闭零校验；装配收口到 `finalizeAPIForumProvider`（三重防线 + AST tripwire，删调用或传 nil 都红）；前端三处入口按开关渲染 ALTCHA，关闭时中间 DTO 连键都没有 |
| P3-4 | ALTCHA 人机验证令牌可跨场景复用 | 属实 | 已修 · `5e472aca0` + `22b5332bd` | 3 | replay 键改 `sha256(provider + "\x00" + token)`（全局单次消费）+ purpose 绑进 challenge 签名载荷；兼容分支有 decision record 与失效时间点（2026-11-30 / 下一 minor，先到者），剥 data 必失配（审查员探针实证） |
| P3-5 | HumanVerify Redis 限速 INCR/EXPIRE 非原子，失败后 IP 永久限流 | 不属实 | 不修 | — | 无需动作：`rateLimitScript` 已用一次 EVAL 原子完成自增 + 首次 TTL（`Support/HumanVerify/redis_store.go:114-119`） |
| P3-6 | core.guard.public 未显式声明 Access 时三条派发路径语义不一致 | 部分属实 | 已修 · `97659db29` + `10f80ccf1`（随 P1-2 收口） | 3 | 三处推导收口到 `DeriveRouteAccess` 单一函数：legacy 前缀与 declared 视图共用同一答案，不可代理路由两边都不服务（不再各推一次） |
| P3-7 | 前端信任挑战状态为进程内存表，多副本下 challenge→grant 必失败 | 属实 | 延后 | 延后 | 触发条件满足时复用持久化挑战存储；单实例部署下可延后（见「延后项」） |
| P3-8 | `app/Support/IdentityDelegation` 整包为无生产调用方的死代码 | 属实 | 已修 · `98d4b6629` | 3 | 删除整包（−313 行）：代码/脚本/契约 `rg` 零命中，两处 module note 改写为「委派走 HostAPI + plugin_session_state_delegation」；`test-db-isolation-baseline` 同步降（`totalTests 6500→6498`、`dbPackages 136→135`）；历史 handoff 与台账行按溯源保留 |
| P3-9 | 扩展 observe 事件 delivery 行创建与 River 入队非原子 | 属实 | 已修 · `efcf5a9f9` + `c1c47b020` + `564981186` + `d1cfab7f8`（口径改写见右） | 3 | **关闭口径已改写**：原写「同事务 或 sweeper + replay」，实测两条都不完整——事务分支生产不可达（`ManagerConfig` 无 Dispatcher/Pool），sweeper 无 replay（DDL 无 payload 列）。本轮达成的是「四失败点全回滚 + 超龄 `queued`/`running` 由 10 分钟调度收成 `failed`（判据 `updated_at`），崩溃窗口内投递不可恢复但不再留永久中途行」。replay 与生产接线转为跟进 F-6 |
| P3-10 | TrustLevels 账本读-比-写非原子，并发重算可发出错序 / 重复事件 | 属实 | 已修 · `e3575177e` + `b2474ef23` + `8b8358655` | 3 | 账本加 `revision` 乐观锁（迁移 `202611160002`，`CHECK (revision > 0)` 用 DO 块 + `pg_constraint` 存在性检查**幂等登记**、`NOT VALID` + 单独 `VALIDATE`、成对 Down）；CAS 冲突时**重读账本 + 重新求值**再写，重试用尽不发事件。并发用例 + 确定性反例用例双覆盖，旧实现（只重读不重算）必红；`IntegrationTests` 73 PASS / 0 FAIL |
| P3-11 | CachePolicy Service 未接线，tag 索引为进程内内存 | 属实 | 延后 | 延后 | 首次有调用方按 tag 失效前必须先改 Redis generation 方案（见「延后项」） |
| P3-12 | 公告横幅与管理端公告预览的 v-html 未接前端二次净化 | 部分属实 | 已修 · `0d1dc4eb6` + 棘轮 `f73419157` / `423e0db79` / `85045fbe0` | 3 | 两处包 `sanitizeHtml()`；同批收口其余未净化 v-html（偏差第 4 条） |
| P3-13 | Admin Surface 插件输出 `view.download.url` 直接进 `window.location.assign` 与 `<a href>` | 不属实 | 不修 | — | 无需动作：`normalizeDownload`（`apps/web/app/utils/admin/adminSurfaces.ts:359-364`）已强制同源绝对路径，拒绝 `//`、反斜杠与控制字符 |
| P3-14 | `common.cancel` 两种语言包均不存在 | 不属实(已修) | 已修 · 核实前基线 `e0ec3bce6` | — | 无需动作：两种语言均有 `common.cancel`，i18n 棘轮测试覆盖 |
| P3-15 | `common.more` 缺失，插件管理页移动端按钮渲染原始 key | 不属实(已修) | 已修 · 核实前基线 `e0ec3bce6` | — | 无需动作：两种语言均有 `common.more`，i18n 棘轮测试覆盖 |
| P3-16 | 扩展贡献治理徽章 i18n key 名错位 | 部分属实(降级可选) | 已修 · `0d1dc4eb6`（可选清理已顺手做完） | — | 键名与值已对齐，两种语言下被禁用贡献项的徽章渲染正常文案 |
| P3-17 | 两个 @deprecated 兼容符号已无任何调用方（死代码） | 属实 | 已修 · `0d1dc4eb6` | 3 | 两个符号删除，typecheck + `bun test` 绿 |
| P3-18 | compose.prod.yaml 对 POSTGRES_PASSWORD/REDIS_PASSWORD/MEILI_MASTER_KEY 缺 `:?` 硬闸 | 属实 | 已修 · `4b4352d48` | 3 | 三个密码补 `${KEY:?...}`，缺值即 fail-fast，弱默认不再生效 |
| P3-19 | 蓝绿拓扑上 `deploy.sh --action deploy` 静默降级为单拓扑并抹掉槽位 / 回滚元数据 | 属实 | 已修 · `4b4352d48` | 3 | 检测到 `topology=blue-green` 时拒绝，或完整重写状态文件（含 topology / slot 字段） |
| P3-20 | sforum-bootstrap.sh 未守护空数组展开（bash 3.2 + `set -u` 崩溃） | 部分属实 | 已修 · `4b4352d48` | 3 | 空数组展开统一 `"${ARR[@]+"${ARR[@]}"}"` 写法；bash 3.2 + `set -u` 下 install 不崩 |
| P3-21 | `SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS` 非数值在 ingress 已切换后触发 `set -e` 中断 | 属实 | 已修 · `4b4352d48` | 3 | 复用数字消毒 helper，非法值不再中断在 ingress 切换后、`persist_state` 之前 |
| P3-22 | 备份加密使用无完整性认证的 AES-256-CBC（encrypt-only） | 属实 | 已修 · `4b4352d48` | 3 | 改 encrypt-then-MAC（或 age/GPG），manifest 记录 scheme 版本 |

## 本轮进展快照（2026-11-17 凌晨）

| 批次 | 范围 | 状态 | 提交 |
| --- | --- | --- | --- |
| 0 | 本台账 + `plans/README.md` 索引 + 仓库外核实档 | 完成 | — |
| 1 | P1-1 / P1-2 | 完成（各含一次审查补刀） | `c382e366b` `fcdc86eda` / `97659db29` `10f80ccf1` |
| 2 | P2-1 ~ P2-6 | 完成 | `39fb04e3e` / task-11 · 12 · 13 系列 / P2-6 系列（`42aa744f3` 等） |
| 3 前端 | P3-12 / P3-16 / P3-17 | 完成 | `0d1dc4eb6` + 棘轮 `f73419157` `423e0db79` `85045fbe0` |
| 3 运维 | P3-18 ~ P3-22 | 完成 | `4b4352d48` |
| 3 后端 | P3-1 / P3-2 / P3-3 / P3-4 / P3-8 / P3-9 / P3-10 | 完成（P3-6 随 P1-2 收口） | `c0ab8d80d` `7218be6c6` / `191c4e441` `56da3585d` `2de3e6ff3` / `70c1ab86f` `f297f8bea` `8e483b623` `1c9747aa8` / `5e472aca0` `22b5332bd` / `98d4b6629` / `efcf5a9f9` `c1c47b020` `564981186` `d1cfab7f8` / `e3575177e` `b2474ef23` |
| 基线回归（本轮顺手修） | Notifications 测试 fake 编译失败 / 路由计数 tripwire 435 vs 437 / 调度目录清单与 `schedules.md` / 插件 env 探针静态误报 | 完成 | `b2819a4c2` / `17d106214` / `6a77f6556` `d91a1f80e` / 见 F-7 |

统计：已修 22 / 不修 5 / 延后 2 / 转跟进 2 = 31。本轮共 22 个提交（`b23a658dd..HEAD`）。

## 新增跟进项（本轮产生，8 条）

| 编号 | 标题 | 来源 | 状态 | 关闭条件 / 触发条件 |
| --- | --- | --- | --- | --- |
| F-1 | 前端 `usePermissions.can()` 的父→子展开与后端 deny 不同构：菜单可见、点进去 403 | P1-1 修复的副作用面（task-9） | 待派 | UX 瑕疵，非越权。触发条件：下次动前端权限菜单时顺手对齐，或再出现「菜单可见但 403」的用户反馈；关闭条件 = 同一账号的菜单可见性与后端 `Can()` 判定一致 |
| F-2 | 无清单明文 `.sql` 恢复通道的威胁模型缺口 | P3-22 修复后的残留（task-10） | 待决策 | 需要一次决策记录：要么给明文通道加显式确认 + 审计留痕，要么拒绝备份目录之外的明文载荷；关闭条件 = 决策落 `knowledge/decisions/` 且实现与之一致 |
| F-3 | HumanVerify 的 Options 读失败静默降级、无日志 | P3-3 复审（task-23） | 待派 | `RuntimeService.service()` 把 Options 读错误整个吞掉：**部分**故障（语句超时 / 连接池饱和 / 坏计划）会让「运营开了 post_risk 却零校验」且无任何日志。关闭条件 = 读失败打 warn（带 purpose 与 fallback 方向）+ 一条 decision record 写明 fail-open 的前提与反驳论点 |
| F-4 | 扩展 observe 投递的崩溃窗口 replay（需 payload 列） | P3-9 对抗审查 F1（task-24） | 待决策 | 先判定 observe 事件丢失的业务后果；要 replay 就得给 delivery 行加 payload 列 + 管理面重投端点（对齐 Notifications `ReplayDelivery`），并评估 payload 脱敏；不要 replay 就把取舍写进 decision。附带判定要不要把 `Dispatcher`/`Pool` 接进生产装配（那是行为变更，需独立 decision） |
| F-5 | P3-3 post_risk 卡片的真机浏览器 QA | P3-3 前端复审（task-29） | 待派 | 逻辑主张已由真实 SFC 挂载 + 变异覆盖；视觉/可用性主张仍需 Playwright：`.sf-forum-altcha` 的 `boundingBox()` 非零可见、能解出一题、真实 `POST /api/v1/topics` 请求体键有无、390×844 无横向溢出。不阻塞 P3-3 关门 |
| F-6 | 对外 reason 键翻译完整性棘轮（原 task-11） | P2-2 审查 | 待派 | 后端所有对外 reason 键与 `Localization/messages.go` 做差 + 中英同键集 + 棘轮测试。**注意**：`extension.delivery_*` 四个键经核实**不进** HTTP 错误码/reason 枚举/翻译表（`delivery_enqueue_failed` 走版本化 hook 才落账本，在管理面作为自由文本可见），不要误加 |
| F-7 | 调度成员表镜像（`Support/Jobs/core_schedules_test.go` ⇄ `Models/Jobs/service_test.go`） | 本轮两次漂移（路由计数、调度清单） | 已修一半 | `Models/Jobs` 侧已改属性断言（`d91a1f80e`，`Schedules()` 集合 == `NewCoreScheduleRegistry(nil).Views()` 集合），新增调度零改动。`Support/Jobs` 侧仍是唯一手写闸口，保留是刻意的（它是「目录成员变了」的审查信号） |
| F-8 | `t.Fatalf` 让 Auth 包在裸 `go test` 下红，需文档说明 | P3-2 复审（task-22） | 待派 | 在 `AGENTS.md` 测试命令段补一句「Auth 包的 `TestLoginLockoutTTLAgainstRedis` 需要 `SFORUM_TEST_REDIS_ADDR`，由 `scripts/test-api-isolated.sh` 提供」，让第一次撞红的人不用读源码 |
| F-9 | 全仓 coverage 门跨包共享测试库导致偶发 `users_pkey` 冲突 | 批次 3 收尾门禁 | 待派 | 计数已达标（7723 ≥ 6498），但 `Models/Notifications` 的 `TestListCelebrationsDropsUnavailableTargets` 在全仓跑时偶发 `duplicate key value violates unique constraint "users_pkey"`，单包跑绿。关闭条件 = `--coverage-only` 连跑两次 `exit 0`；首选让每个测试包的 schema 真正隔离（`testdb.IsolatedURL` 只做「拒绝开发库」校验，不做 schema 隔离） |

## 本轮审查发现并当场修掉的问题

这一节是本台账最有价值的部分：**它记录设计假设在哪里错了**，而不是记录改了几行。每条都走完「假设 → 实际 → 修法 → 教训」。

1. **「显式 false 会顺 `APITokens.RestrictActor` 自动传播」——假设错了。** 设计说明里判断：deny 产生的 `false` 条目会随 actor 构建链路自然传到 PAT 路径。实际：`RestrictActor` 重建权限 map 时只复制 `true` 条目，`false` 被丢掉，于是持有 `settings.manage` 的 PAT 仍能绕过子权限 deny。已修 `fcdc86eda`：`RestrictActor` 保留显式 `false`，并补 `service_test.go` 用例。**教训：`false` 是数据，不是默认值——任何重建权限 map 的地方都必须显式搬运它。** P1-1 修好了 SQL 层与展开层，第三个重建点照样是洞，说明「修主路径」不等于「修完」。

2. **guest 守卫「退化成 login」不是保守，是放宽。** 假设：守卫无法求值时按 `login` 执行属于 fail-closed。实际：`core.guard.guest` 的声明语义是「仅匿名可访问」，按 `login` 执行等于把「仅匿名」放宽成「已登录即可」，本应只给匿名访客的端点向全体登录用户敞开。已修 `10f80ccf1`：legacy 前缀上收紧为严格 **404**，同步 `route_access.go` 与三份路由文档。**教训：fail-closed 不是「挑一个更严的守卫」，而是「拒绝服务」。** 在授权语义里换一个守卫等于换一份合同。

3. **备份 v2「清单记 v2、载荷是明文」只告警不拒绝 = 不需要密钥的 MAC 绕过。** 假设：格式不匹配时告警足以提示运维。实际：把 manifest 的 scheme 改成 v2、载荷保持明文，restore 侧就接受了一个「声称有完整性认证、实际没有」的包——P3-22 要的真实性保证被整条绕过，且不需要任何密钥。已修 `4b4352d48`：该分支改 `sforum_die` 硬失败。**教训：完整性校验的失败路径只能是拒绝；告警是留给「可继续但降级」的，这里没有可接受的降级态。**

4. **P3-12 的棘轮测试自身被绕过三次。** `f73419157` 堵「注释里出现 `sanitizeHtml` 冒充调用」，`423e0db79` 把 derived 规则改成连通性判定（堵「无关净化 + 重名定义」），`85045fbe0` 要求 `set` 的值参数必须就是净化调用本身。**教训：静态棘轮只能证明「形状对」，不能证明「语义对」；每收紧一次都要写下它挡的是哪种具体绕过**，否则下一轮换个写法照样绕开。

5. **「CI 里 skip 等于没写」——P3-2 的真实 Redis 用例被 `t.Skip` 掉，Lua 语法错误也能全绿。** 审查员实测：把 `loginBumpScript` 改成 `return count +`（语法错误），`test-api-isolated.sh` 口径下 `ok … 0.005s` 全绿——因为 `TestLoginLockoutTTLAgainstRedis` 需要 `SFORUM_TEST_REDIS_ADDR`，而脚本只起 PostgreSQL、CI 只声明 postgres service，`t.Skip` 在非 `-v` 输出里只留一行 `ok`。**修法两层**：`2de3e6ff3` 让 isolated 脚本同时起一次性 Redis（含「宿主侧 `/dev/tcp` PING 连续两次」的就绪判定，否则端口映射未生效会让首个 EVAL 失败被 fail-open 吞掉，表现为假红），`56da3585d` 把 skip 改成 `t.Fatalf`。**教训：gated 测试要么让依赖真的存在，要么显式失败；「跳过」在证据链里等于「不存在」。**

6. **「重试只重读账本、不重算」——P3-10 的 bug 被换了个位置复现。** 第一版把 CAS 重试写成「冲突 → 重读账本 → 用旧 `evaluation` 重建 next」，员工论证「求值读活动统计与策略、与账本无关」。审查员指出：输入独立只证明**输入**不受账本影响，推不出**结果在重试窗口内不变**——旧求值可能比并发赢家的求值更旧，于是 CAS 成功也把更旧的 level 写回去。反例跑通：赢家写 `4/leader`，落败方写回 `3/veteran` 并发 `4→3` 降级事件。**教训：`ledger.go` 的接口注释写「调用方必须重读账本再算」，实现就必须真的「再算」；乐观锁只保证「没被别人改过」，不保证「我算得对」。**

7. **「字符串断言冒充渲染证据」——前端 18 条用例全绿，把开关改成恒 false 也不红。** P3-3 前端第一版对发帖页/抽屉只做 `readFileSync().toContain()`：把 `postRiskEnabled` 换成 `computed(() => false)`（模板字符串原样不动）仍然 18 pass / 0 fail。返工改成真实 SFC 挂载（`compileSfcSource` + `<Suspense>` + 真实 `createPostRiskVerification`），同一变异立刻 5 pass / 2 fail。返工还顺手抓到一处真问题：发帖页写 `humanVerification: postRiskPayload()`，关闭时中间 DTO 里**键存在但值是 undefined**，只在线缆层被摘掉。**教训：与第 4 条同源——「文件里有那串字」不是行为证据；UI 断言必须挂真实组件 + 真实状态机。**

8. **「修了一条生产上不可达的路径」——P3-9 的原子化分支生产永不执行。** 第一版把 `CreateEventDelivery` 与 `Enqueue` 合进事务，实现与测试都对；但 `bootstrap/app.go:73` 与 `bootstrap/worker.go:344` 的 `ManagerConfig` 只有 `Starter` + `DeliveryStore`，**没有 Dispatcher、没有 Pool**，`emitObserve` 永远走 in-process 分支，而那条分支仍会留永久 `queued` 孤儿行且无 sweeper。**教训：动手前先证明要修的分支在生产可达（`rg` 一遍装配点）；否则你只是给测试修了个 bug。** 返工补了 stale sweeper（复用既有 `coreScheduleConstructors`，无新 goroutine/ticker），并把「replay 需 payload 列」转成跟进 F-4。

9. **手工清单类 tripwire 连续漂移两次（路由计数 421→423→428→433→435→437、调度清单 10→12）。** 两处的期望值都是硬编码，每次新增成员都要人肉记得改，于是上一会话两次漏改把基线弄红。**修法不是补数字，而是消除手工镜像**：路由侧改成三份产物互比（条数 + `method path id contractVersion` 身份键），调度侧把 `Models/Jobs` 换成属性断言（`Schedules()` 集合 == 注册表 `Views()` 集合）、只保留 `Support/Jobs` 一处手写闸口。**教训：同一份成员表存在两处，就一定会漂移；tripwire 要保留（它是审查信号），镜像要消灭。**

10. **「迁移在 schema 隔离测试里会被重复应用」——P3-10 的 `ADD CONSTRAINT` 把 3 条集成测试弄红。** 新迁移 `202611160002` 沿用 `202611140002` 的写法，把表钉在 `public.user_trust_levels`；schema 隔离的集成测试会把整条链反复应用到同一个 public 表。`ADD COLUMN IF NOT EXISTS` 天然幂等，而 **PG 没有 `ADD CONSTRAINT IF NOT EXISTS`** → 第二次应用报 `SQLSTATE 42710`，`IntegrationTests` 70 PASS / 3 FAIL（基线 73）。这个洞是 Lead 用 `--coverage-only` 全仓门抓到的，不是任何单包测试。**修法**：约束改 `DO $$ ... IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = … AND conrelid = …) THEN ADD CONSTRAINT … NOT VALID; END IF; $$`，`VALIDATE` 留在块外（对已验证约束重复执行安全）；不要用 `DROP CONSTRAINT IF EXISTS` 前置（会在真库短暂移除约束）。**教训：钉在 `public` 的表，其 DDL 必须整条可重放——列用 `IF NOT EXISTS`、约束用存在性检查；判定同类迁移（`202611150001` 的 `moderation_reports` 不钉 public，随 search_path 落隔离 schema，故无此风险）。**

## 修复批次

### 批次 0 — 本台账（完成，待 commit）

本文件 + `knowledge/plans/README.md` 索引行 + 仓库外核实档 `~/Code/bug/SForum/2026-11-16-verify-31.md`。不含任何生产代码改动。

### 批次 1 — P1（完成）

两条都是「防护代码已写、装载 / 派发层没接上」：

- **P1-1** `c382e366b`：deny 优先于父权限展开；补刀 `fcdc86eda` 处理 PAT 的 `RestrictActor`（见上节第 1 条）。
- **P1-2** `97659db29`：legacy 前缀对非 `core.guard.*` 守卫 fail-closed；返工 `10f80ccf1` 把 guest 收紧为 404（见上节第 2 条）。

### 批次 2 — P2（在办）

P2-1 ~ P2-6。P2-7 已修，不在本批次。**P2-1 已落 `39fb04e3e`**（失败锁定判定前移，decision 2026-10-22 第 5 / 8 条同步改写）；task-11 剩 P2-4；task-12 = P2-3 + P2-5；task-13 = P2-2；P2-6（邮件终态 sweeper + replay 端点 + 前端重试按钮）待派。

### 批次 3 — P3（三路，后端未开工）

- **后端（待派 7 条）**：P3-1、P3-2、P3-3、P3-4、P3-8、P3-9、P3-10。P3-6（三条派发路径语义不一致）已随 P1-2 收口到 `DeriveRouteAccess`，不在待派里。
- **前端（完成）**：P3-12、P3-17，连带把 P3-16 的可选清理一起做掉（`0d1dc4eb6` + 三道棘轮）。
- **运维（完成）**：P3-18 ~ P3-22（`4b4352d48`，含 meilisearch 硬闸的 backfill 前置接线）。
- **延后**：P3-7、P3-11。

## 不修

| 编号 | 理由 |
| --- | --- |
| P2-7 | 已修。en-US 已补齐 28 个 `userAppearanceSettings` leaf key（与 zh-CN 对齐），并已有棘轮测试 `apps/web/tests/framework/i18nCatalog.test.ts` 锁死 key 对齐，回归会被测试挡住。 |
| P3-5 | 已不成立。限速走 `rateLimitScript`（`Support/HumanVerify/redis_store.go:114-119`），自增与首次 TTL 在同一次 EVAL 内完成；`Incr`/`Expire` 接口保留只为让「拆成两条命令」的回归能被测试直接看见。 |
| P3-13 | 已不成立。`normalizeDownload`（`apps/web/app/utils/admin/adminSurfaces.ts:359-364`）已要求 URL 为同源绝对路径：必须 `/` 开头，拒绝 `//`、反斜杠与控制字符，非合规值直接丢弃。 |
| P3-14 | 已修。两种语言包均有 `common.cancel`，i18n 棘轮测试覆盖；原报告的「双语言共缺 3 个 key」已被该测试收口。 |
| P3-15 | 已修。两种语言包均有 `common.more`，i18n 棘轮测试覆盖。 |

P3-16 原列在「不修 / 可选清理」，本轮已顺手做完（`0d1dc4eb6`），状态改为已修，不再留在本节。

## 报告自身的 4 处偏差

核实过程中发现报告有 4 处描述与 `e0ec3bce6` 不符，**修复时以本节为准**，不要照抄报告原文：

1. **P2-3 缓存 TTL 上限实为 60s，不是 45s。** 报告写「TTL 30-45s」。实测 `Models/Forum/cached_store.go:62-67`：主题列表 15s、列表第一页 45s、taxonomy 60s、主题详情 30s、评论 20s、评论第一页 40s。因此公开残留窗口上限是 **60s（分类 / 分组 / 标签列表）**；「列表第一页更长」的说法在 45s 这一档成立，但整体上限要按 60s 描述。
2. **P3-6 实际返回 403，不是 503。** 报告写「declared_route 视图 = 503」。三个时间点重验均为 **403**：未声明 / 未知 Access 时 `declaredExtensionRouteGuardEvaluator` 返回 `ErrCoreGuardEvaluatorUnavailable`（`app/Http/core_guard_authorizer.go:373-406`），最终落 `ErrRouteGuardUnavailable`。fail-closed 结论不变，只有状态码与报告不符——写测试时按 403 断言。注：该路径现已被 P3-6 的收口改掉，不可代理路由不再进 declared catalog（`freezeDeclaredRouteGuards` 直接 `continue`）。
3. **P3-19/20/21 共 6 处行号偏移。** 报告引用的 `deploy.sh:897-905`、`:921`、`:691-699` 与 `upgrade.sh:661`、`:583-584`、`:706` 在核实基线上均已漂移（`:897-905` 现落在 restore 尾部、`:921` 是 `check_env_contract`）。可复核锚点：`deploy.sh` 的 `persist_successful_state()` 在 **733**；`upgrade.sh` 的 `persist_state()` 在 **807**、`wait_stable_ingress()` 在 **765**、`SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS` 的读取在 **683 / 769**。`sforum-bootstrap.sh:169` 与 `:374` 仍然准确。
4. **P3-12「全库其余 v-html 都走净化」不成立。** 除报告点名的两处公告渲染外，至少还有 `components/forum/SFMentionContent.vue:56`（`v-html="html"`）与 `components/legal/SFLegalDocumentPage.vue:54`（`v-html="body"`）未包 `sanitizeHtml`，`components/forum/SFComment.vue:105` 的注释也明说「后端已 sanitize，前端可直接 v-html」。报告点名的两处仍是真问题，但「只有这两处缺口」的前提不成立——修 P3-12 时要连其余未净化点一起收口，或为每处写下依赖后端净化的理由。

## 延后项

**P3-16 已移出本节**：本轮顺手做完（`0d1dc4eb6`），从可选清理转为已修。剩余三条仍**不是不做**，是等触发条件：

- **P3-7 前端信任挑战持久化。** 触发条件：部署拓扑变为多副本 / 蓝绿，或任一 `super_admin` 在非单实例环境被 `invalid-challenge` 卡住。单实例部署下可延后；一旦触发，复用可执行信任挑战的持久化存储（migration `202607130006`），不要另起一套。
- **P3-11 CachePolicy 接线。** 触发条件：首次有调用方需要按 tag 失效。接线前必须先改 Redis 版本号 / generation 方案——进程内 `tagIndex` 在多进程下按 tag 失效天然不完整，直接接线等于把「失效不完整」当默认行为发布。
- **评论计数口径的同构风险（P2-5 的同源问题）。** 触发条件：修 P2-5 时只改 forum 侧、不改 moderation 侧，或未来新增第三条写主题计数的路径（批量迁移、后台重算、插件命令）。届时必须把计数辅助函数收敛成单一实现；否则口径会再次分叉，而这次分叉不会有报告来提醒。

## 验收与关闭

- 每条关闭时，在本表「当前状态」列写「已修 · commit」，并把验证方式写进关闭条件列的括注（测试名 / 命令 / 浏览器断言）。
- 批次 1 的 allowed + denied 两侧测试已随 `c382e366b` / `fcdc86eda` / `97659db29` / `10f80ccf1` 提交。
- 批次 2 / 3 的缓存、计数、状态机类改动必须有「改动后立即读到新值 / 新状态」的断言，不接受「等 TTL 过期后自然一致」。
- **本轮的验收口径（批次 3 起固定下来）**：①一个改动一个 commit；②交付附真实命令输出（`go build` + `SFORUM_TEST_SHARDS=1 ./scripts/test-api-isolated.sh <包>`），口头汇报不算；③**每条都要变异验证**——去掉修复后对应用例必须红，红/绿两次输出都要贴；④代码改动一律由 reviewer 看 diff，且**要求他自己注入绕过**，不采信员工报的数字。
- **对抗审查的实际价值**（本轮数字）：7 条 P3 里 **4 条被打回返工**（P3-2 证据链、P3-3 装配无证据、P3-9 生产不可达、P3-10 重试不重算），其中 P3-10 是**真 bug**（审查员构造出 `4→3` 降级事件反例）、P3-9 是**真缺口**（修的分支生产永不执行）。没有这一层，这四条都会以「测试全绿」的形式合入。
- **本轮基线红灯的成因**（都不在 31 条里，是上一会话验收口径漏掉的）：上一会话以「聚焦包测试通过」验收，没有跑全仓门禁，于是留下四处——`Controllers/Notifications` 测试包**编译失败**（接口加方法、fake 没跟上）、路由计数 tripwire 435 vs 437、调度目录清单 10 vs 12 + `schedules.md` 漂移、插件 env 探针被静态扫描误判。四处已在本轮修掉；**教训：接口变更必须 `go vet ./...` 一遍所有实现方（含测试 fake），目录类清单要么派生要么只留一处手写闸口。**
- **收尾时的门禁状态（如实记录，别当成全绿）**：`go build ./...` OK；`node tests/validate-architecture-boundaries.mjs` passed（2310 生产文件）；`ruby scripts/validate-openapi-refs.rb` 3800 refs OK；`node tests/validate-test-db-isolation.mjs --static-only` exit 0；`--coverage-only` 的**计数要求满足**（7723 个测试 / 155 包 ≥ 基线 6498），但连续两次跑都因**跨包共享测试库**偶发失败（`app/Models/Notifications` 的 `TestListCelebrationsDropsUnavailableTargets` 报 `users_pkey` 冲突；第二次还带一条 `app/Support/Extensions` 的 `TestPostgresExtensionDatabaseHighRiskLeaseObservesMigratedCoreFence`）。两个文件在本轮零改动，**单独跑包都绿**（`ok … 1.069s` / `ok … 0.547s`），判定为既有隔离缺陷，已立跟进 F-9。
- 全部 31 条收敛后，本文件移入 `knowledge/plans/archive/2026-11/`，并在 `README.md` 的 Archived Plans 段落留索引。
