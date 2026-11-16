# 2026-11-16 安全审计 31 条修复台账

- **状态**：**completed（2026-11-17 凌晨第二轮收尾）**。批次 1 / 2 / 3 全部完成；**31 条全部终态：已修 29 / 不修 2 / 延后 0**；两条延后项（P3-7 / P3-11）已转正并修完；跟进项 F-1 ~ F-9 **全部收口**（F-2 / F-4 以「决策 + 实现」双段关闭）。零在办、零待派。
- **来源报告**：`~/Code/bug/SForum/2026-11-15_16-43.md`（31 条独立发现：P0 = 0 / P1 = 2 / P2 = 7 / P3 = 22）
- **核实基线**：commit `e0ec3bce6`（`git rev-parse --short HEAD`，2026-11-16；核实期间工作树干净）
- **进展基线**：commit `39fb04e3e`（台账状态以此为准；其后若有新提交，按「当前状态」列自行补行）
- **核实方法**：逐行代码验证 + 3 路只读核查代理（三个时间点独立重验同一批断言；核查代理只读，不改仓库）
- **核实结论**：属实 19 / 部分属实 7 / 不属实 5
- **实施结论**：已修 29 / 不修 2 / 延后 0 = 31（2026-11-17 第二轮收尾口径；P3-7 / P3-11 由延后转正并修完，统计随之改口径）
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
| P2-2 | PAT 完全不受 token_version 失效与「强制下线全部设备」约束 | 属实 | 已修 · `65787967f` + `0db6c3888` + `b23a658dd`（前端 `469295770` `3a1e0afa7`） | 2 | 密码重置 / 强制下线后旧 PAT 鉴权失败；管理员代撤销端点有 allowed + denied 测试；`RestrictActor` 保留显式 `false`（见「审查发现」第 1 条） |
| P2-3 | 举报处置（HideAndClose/DeleteAndClose）直写状态但不触发公开缓存失效 | 属实 | 已修 · `25390c34d` | 2 | 处置提交后公开读接口在 TTL 内立即不可见（列表 + 详情双断言） |
| P2-4 | Idempotency pending 无租约与崩溃清理（同 Key 最长 24h 全部 409） | 部分属实 | 已修 · `ed76a58c8` | 2 | panic 后同一 `Idempotency-Key` 在短 TTL 后可重试；Complete/Abort 走 CAS，只有 lease 持有者能收敛 |
| P2-5 | 主题生命周期计数多路径口径不一致（locked 双计 / 漏计 / 永久少计） | 属实 | 已修 · `459381289` + `5ceed2dc3` | 2 | 「锁帖 → 作者编辑触发预审 → 审核通过」与「删除 locked 帖」后 `ListTopics` total 与真实可见数一致 |
| P2-6 | mail_deliveries 无终态收口：重试耗尽后永久停在 sending，邮件静默丢失 | 部分属实 | 已修 · `d3b36d46d` + `42aa744f3` | 2 | 重试耗尽写 `DeliveryDead`；sweeper 把超龄 sending/queued 转 dead；replay 端点可用；控制器测试 fake 补齐（`b2819a4c2`，见 F 系列） |
| P2-7 | en-US 语言包整棵缺失 `userAppearanceSettings` 子树（28 key） | 不属实(已修) | 已修 · 核实前基线 `e0ec3bce6` | — | 无需动作：en-US 已有 28 个 leaf key，且 `apps/web/tests/framework/i18nCatalog.test.ts` 已棘轮 |
| P3-1 | Bearer PAT 校验每请求无条件写库刷新 last_used_at（无节流） | 属实 | 已修 · `c0ab8d80d` + `7218be6c6` | 3 | 同一 token 高频请求不再产生逐请求 `UPDATE`，写入按阈值节流且 last_used_at 语义不变（PG 集成 `TestTouchLastUsedThrottlePostgres` 四子用例 + 变异必红；严格 `<` 边界与「跨语句等值必然过期」已钉成断言） |
| P3-2 | LoginLockout 计数键 TTL 依赖单次 Expire 成功 | 属实 | 已修 · `191c4e441` + `56da3585d` + `2de3e6ff3` | 3 | 首写与 TTL 压进单次 EVAL 原子完成，`PTTL<0` 自愈历史无 TTL 键；真实 Redis 用例在 CI 口径下**不再 skip**（无 `SFORUM_TEST_REDIS_ADDR` 时 `t.Fatalf`），Lua 语法错误在脚本口径下必红 |
| P3-3 | post_risk 人机验证场景全链路存在但发帖/回帖路径从未执行 | 属实 | 已修 · `70c1ab86f` + `f297f8bea` + `8e483b623` + `1c9747aa8` | 3 | 选 A 接线：发帖/回帖在写入前调 `Verify(PurposePostRisk)`，开关关闭零校验；装配收口到 `finalizeAPIForumProvider`（三重防线 + AST tripwire，删调用或传 nil 都红）；前端三处入口按开关渲染 ALTCHA，关闭时中间 DTO 连键都没有 |
| P3-4 | ALTCHA 人机验证令牌可跨场景复用 | 属实 | 已修 · `5e472aca0` + `22b5332bd` | 3 | replay 键改 `sha256(provider + "\x00" + token)`（全局单次消费）+ purpose 绑进 challenge 签名载荷；兼容分支有 decision record 与失效时间点（2026-11-30 / 下一 minor，先到者），剥 data 必失配（审查员探针实证） |
| P3-5 | HumanVerify Redis 限速 INCR/EXPIRE 非原子，失败后 IP 永久限流 | 不属实 | 不修 | — | 无需动作：`rateLimitScript` 已用一次 EVAL 原子完成自增 + 首次 TTL（`Support/HumanVerify/redis_store.go:114-119`） |
| P3-6 | core.guard.public 未显式声明 Access 时三条派发路径语义不一致 | 部分属实 | 已修 · `97659db29` + `10f80ccf1`（随 P1-2 收口） | 3 | 三处推导收口到 `DeriveRouteAccess` 单一函数：legacy 前缀与 declared 视图共用同一答案，不可代理路由两边都不服务（不再各推一次） |
| P3-7 | 前端信任挑战状态为进程内存表，多副本下 challenge→grant 必失败 | 属实 | 已修 · `3a70e0e80` + `abb0c11bf` + `a3756b1ef`（延后项转正） | 转正 | 复用 migration `202607130006` 的 `extension_trust_challenges`（**零 migration**：表已有 `actor_user_id`/`expires_at`/`consumed_at`/`invalidated_at`/`token_hash UNIQUE`，`action` 域已含 `frontend_import`）。新子包 `app/Models/Extensions/frontendtrust`（receiver cap 92 未动）：`token_hash=sha256("…challenge:v1:"+ChallengeID)`、确认码只存摘要；消费是**一条原子 UPDATE**（八个绑定条件 + 未消费/未失效/未过期全在 WHERE + `RETURNING id`），16 goroutine 并发实测 `ok=1 invalid=15`；`FrontendService` 删掉内存 map 与互斥锁，nil store **fail closed**；存储故障映射 503 `extension.frontend_trust_unavailable`（挑战无效仍 409）。补**包级状态 AST 守卫**（拒 `sync.Map`/空 map/`make(map)`/结构体字面量藏 map，4 个静态表按「非空字面量」放行，唯一豁免 `pluginRuntimeCoordinatorBootOwners` 带 liveness 断言）——因为反射守卫只能覆盖结构体字段，而 reviewer 证明「包级门控缓存」变异能全绿通过。`a3756b1ef` 再补**具名类型**（`type h struct{m map…}` 的 `h{}`/`&h{}`/`new(h)`/`var x h` 四种形态，含传递闭包）并把扫根递归到 `app/Models/Extensions/**` 全部子包（`frontendtrust`/`deliverysweep`/`settingssurface`/`systemactor`/`builtinauto`） |
| P3-8 | `app/Support/IdentityDelegation` 整包为无生产调用方的死代码 | 属实 | 已修 · `98d4b6629` | 3 | 删除整包（−313 行）：代码/脚本/契约 `rg` 零命中，两处 module note 改写为「委派走 HostAPI + plugin_session_state_delegation」；`test-db-isolation-baseline` 同步降（`totalTests 6500→6498`、`dbPackages 136→135`）；历史 handoff 与台账行按溯源保留 |
| P3-9 | 扩展 observe 事件 delivery 行创建与 River 入队非原子 | 属实 | 已修 · `efcf5a9f9` + `c1c47b020` + `564981186` + `d1cfab7f8`（口径改写见右） | 3 | **关闭口径已改写**：原写「同事务 或 sweeper + replay」，实测两条都不完整——事务分支生产不可达（`ManagerConfig` 无 Dispatcher/Pool），sweeper 无 replay（DDL 无 payload 列）。本轮达成的是「四失败点全回滚 + 超龄 `queued`/`running` 由 10 分钟调度收成 `failed`（判据 `updated_at`），崩溃窗口内投递不可恢复但不再留永久中途行」。replay 与生产接线转为跟进 F-6 |
| P3-10 | TrustLevels 账本读-比-写非原子，并发重算可发出错序 / 重复事件 | 属实 | 已修 · `e3575177e` + `b2474ef23` + `8b8358655` | 3 | 账本加 `revision` 乐观锁（迁移 `202611160002`，`CHECK (revision > 0)` 用 DO 块 + `pg_constraint` 存在性检查**幂等登记**、`NOT VALID` + 单独 `VALIDATE`、成对 Down）；CAS 冲突时**重读账本 + 重新求值**再写，重试用尽不发事件。并发用例 + 确定性反例用例双覆盖，旧实现（只重读不重算）必红；`IntegrationTests` 73 PASS / 0 FAIL |
| P3-11 | CachePolicy Service 未接线，tag 索引为进程内内存 | 属实 | 已修 · `dc9191a1f` + `692f7265e`（延后项转正） | 转正 | **顺序按要求先 generation 再接线**：`GenerationStore`（`Current`/`Bump`，键 `sforum:cachepolicy:gen:<tag>`，TTL 7d > MaxTTL 24h）；条目信封 `{"schema":1,"value":…,"generations":{tag:n}}`，`Get` 逐项比对、不等即删+miss；`Invalidate` 权威动作改原子 `INCR`，本地 `tagIndex` **降为纯加速索引**；故障姿态显式 fail-closed（写入读不到→不写、读取读不到→返回错误不当命中、递增失败→不声称成功）。decision `2026-11-17-cache-policy-tag-generation.md`。**已知边界（写进 decision）**：本轮之后 `.Policy` 仍无生产消费方（`cachepolicy.New` 非测试只有 `bootstrap/host_cache.go:73`；`.Policy` 唯一读取方是 `host_cache_test.go`），「机制就绪 ≠ 已接上」；多 tag 逐项比对由 `TestEntryIsRejectedWhenAnyOfItsTagsMoves` 钉住（单 tag 用例挡不住「任一相等即命中」） |
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

统计：**已修 27 / 不修 2 / 延后 2 = 31，零在办、零待派**。两段提交：上一会话 `e0ec3bce6..b23a658dd`（22 个，批次 1 + 2），本会话 `b23a658dd..HEAD`（28 个，批次 3 + 基线回归 + 台账）。

## 新增跟进项（F-1 ~ F-9 已全部收口；本轮另新产生 F-10 ~ F-14）

| 编号 | 标题 | 来源 | 状态 | 结论 / 关闭证据 |
| --- | --- | --- | --- | --- |
| F-1 | 前端 `usePermissions.can()` 的父→子展开与后端 deny 不同构：菜单可见、点进去 403 | P1-1 修复的副作用面 | **已修** · `f3e95d837` + `f499325f7` + `d7b42925e` | 取方案②：**后端本来就下发「已展开且已扣除」的集合**（`postgres_store_roles.go:344` 与 `postgres_store.go:423` 都走 `ExpandEffectivePermissions(keys, denied)`），前端那份自建展开表纯属多余。前端 `can()` 退化为纯查表 + `super_admin` 绕过（唯一权威 `utils/identity/permissionCheck.ts`），删除 `LEGACY_PERMISSION_CHILDREN`，契约注明语义。**顺带清掉同源残余**：页面/菜单把父键当子权限兜底（`/entity-meta`、`/webhooks`、`/users`、`/permissions`、5 个 forum tab、`layouts/admin.vue` 的 `extension.manage`）——父键始终在集合里，deny 子键后入口仍显示。变异：`canWithUser` 恒 true → 11/5 红；把展开塞回 `can()` → 12/4 红；单 tab 门加回父键 → 15/1 红（逐文件绑定成立）。decision `2026-11-17-permission-deny-frontend-authority.md` |
| F-2 | 无清单明文 `.sql` 恢复通道的威胁模型缺口 | P3-22 修复后的残留 | **已修** · 决策 `9fd9e563c` + 实现（部署脚本） | **关键判定：不存在「自动扫描目录挑最新文件」**（`restore-postgres.sh:53-57` 用 `[ ! -f "$1" ]` 拒目录，实测传 `backups/` 与 `backups` 都 exit 1；`deploy.sh:965` 是 `read -r backup_file` 交互输入），所以残留不升为自动绕过。但残留比原描述**更宽**：不是「`.sql` 后缀」而是「无清单 / 清单缺 `encryption=`」一整类（`:276` 的 `[ -f "$manifest" ] || return 0` 零校验零告警；删掉 manifest 的 `encryption=` 行即可绕过 P3-22）。结论：**保留通道但默认拒绝**，复用 `SFORUM_RESTORE_ALLOW_UNVERIFIED=1`，不新增变量；附「历史明文全库转储永不清理」的升级提示 |
| F-3 | HumanVerify 的 Options 读失败静默降级、无日志 | P3-3 复审 | **已修** · `658b6a503` + `d723d394e` | 读失败在 `service()` 一处打结构化 warn（`purpose` + `fallback` 方向 `altcha_enforced`/`disabled_zero_verification` + 原始 err），行为不变；decision `2026-11-17-human-verify-fallback-fail-open.md` 写入 reviewer 的反驳（部分故障下攻击面真实存在）与「彻底关窗要靠 Options 之外的独立可信来源」。变异：删 warn → 双红；`fallbackDirection` 恒返回同一值 → 红（方向被负向断言钉住）；读失败改强制校验 → 红（「不改行为」有守卫）。**精度修正**：`normalizeRuntimeConfig` 只做 `""→disabled` + lower/trim，**不校验成员**，未知 provider 是第三态（日志方向标错 + 每请求 500） |
| F-4 | 扩展 observe 投递的崩溃窗口 replay（需 payload 列） | P3-9 对抗审查 | **已修** · 决策 `9fd9e563c` + 注释 `d73e920b7` | **判定：不补 replay、不建 payload 列、不加端点。** 依据：仓库内零个有副作用的生产 observe 订阅方（八个 builtin 只有 `sforum-content-policy` 声明 events 且是 `kind: filter`）；更常见的丢失是插件未运行时 `Deliver` 立即 `finishDelivery(failed)`（`attempt_count` 恒 1），replay 救不了；payload 已在 `EventDeliveryArgs` 且 River 已持久化，要可重投应把投递接到队列而非复制 payload。`event_jobs.go` 装配注释写明「当前生产不可达 + `RegisterEventDeliveryWorker` 零调用点」，并写下 **`Unique.ByArgs` + 每行唯一 `DeliveryID` → 队列化后去重实际失效** |
| F-5 | P3-3 post_risk 卡片的真机浏览器 QA | P3-3 前端复审 | **已修** · `afee8662b` + `41a54f9f3` + `cd83a9e5b` | 真机 Chrome + 真 API：off 态 `.sf-forum-altcha` **不在 DOM**、真实 `POST /api/v1/topics` 请求体 `bodyKeys:["title","categorySlug","content"]` 且 `humanVerification: null`；on 态 widget 可见 `boundingBox 780×48`、`customElements.get('altcha-widget')===true`、挑战真实下发 `PBKDF2/SHA-256 cost=1000`，并用 Node 复现 `passwordWithCounter` **真解出**（counter=274、171ms）且**服务端接受**（`422 forum.topic_invalid` 而非 `human_verification.*`）；390×844 `scrollWidth==clientWidth`、dock fixed。**未证**：浏览器内带 token 的请求体（headless 下 ALTCHA closed shadow root + 2 个 Web Worker 让 renderer `exit 13`），替代证据标注为代码级（`postRiskField` 三分支单测）。**最有价值的产出**：dev 模式 `/topics/new` 水合 12–30 秒、期间 `ClientOnly` 停在 SSR fallback，「卡片在、widget 不在」是真实中间态；水合前 `Input.insertText` 只改 DOM 值、组件计数器仍 0/100 → 提交按钮永远 disabled。happy-dom 里这两条都不存在 |
| F-6 | 对外 reason 键翻译完整性棘轮 | P2-2 审查 | **已修** · `9395e73f8` + `b829bf837` | **员工纠正了任务描述的错前提**：`api_token.super_admin_locked` 并不缺（早就在 messages.go），真正的问题是发现口径 `locales_test.go` 的 `^[a-z][a-z0-9]*(?:\.[a-z0-9_]+)+$` 首段不允许下划线 → `api_token.*`/`trust_levels.*`/`entity_meta.*`/`human_verification.*`/`rate_limit.*` 整族在发现阶段消失，且只认字面量实参。Go AST 做差：429 个对外 reason 键、缺 16 个，全部补齐（中英各 451）；`messages.go` 已 997 行，新文案按既有做法单独成文件。**reviewer 抓到的必修绕过**：原收集器只走顶层 `token.CONST` → 函数内局部 const 与包级 var 两种写法**全绿漏报**；改 `ast.Inspect` 全树 + 收 `token.VAR` 后四条变异全红。已知盲区写进头注释（构造器按方法名匹配不校验包路径；`fmt.Sprintf`/拼接/map 取值静默跳过；`extensions/` 不在扫描根且插件 reason 不进核心 envelope） |
| F-7 | 调度成员表镜像（`Support/Jobs/core_schedules_test.go` ⇄ `Models/Jobs/service_test.go`） | 本轮两次漂移 | 保留（已修一半，刻意） | `Models/Jobs` 侧已改属性断言（`d91a1f80e`），新增调度零改动；`Support/Jobs` 侧仍是唯一手写闸口，保留是「目录成员变了」的审查信号 |
| F-8 | `t.Fatalf` 让 Auth 包在裸 `go test` 下红，需文档说明 | P3-2 复审 | **已修** · `6f577659e` | `AGENTS.md` 的 Build/test 段补一段：`SFORUM_TEST_REDIS_ADDR` 由 `scripts/test-api-isolated.sh` 提供，裸 `go test ./app/Support/Auth/` 显式失败是**刻意的 gate**，不要改回 `t.Skip`（跳过在非 `-v` 输出里只留一行 `ok`） |
| F-9 | 全仓 coverage 门跨包共享测试库导致偶发 `users_pkey` 冲突 | 批次 3 收尾门禁 | **已修** · `959653261` + `090bf8472` + `9dad8ba84` + `043cfd44a` | **连跑两次 `--coverage-only` 都 exit 0**（7752 测试 / 156 包 / 735s 与 651s，两次计数一致 ≥ 6498）。**三处根因**：① `app/Models/Extensions` 的 version-GC fixture 显式 `id=42` 落行却不推进 `users_id_seq`（实测跑完 users=2/max=42/seq=17），下一个走默认 id 的包在 nextval 到 42 时撞已存在行 → 「每次命中不同包」；修法是 `internal/testdb` 的 `InsertUserWithExplicitID`/`AlignSerial`（对齐到 max(id)、只前进不回拨），受害者 `Notifications` 一行未改。② Core 版本串由 `git describe --dirty` 运行时派生，migrate 写入与 go test 读回是两段式，HEAD 一动必然不等（触发器证明写入方就是 migrator 自己）；修法是门禁用 `-ldflags -X version.Current=<pin>` 把两侧钉成同一读数。③ owner 断言自己没钉 namespace（`pg_class t, pg_class b WHERE …relname='badges'` 匹配到别的包私有 schema 里的同名表，返回 3 行 `true/false/false`）；修法是两侧钉 `public`/`sforum_core_v1`，**没有放宽断言**。**收口后又补两道防线**：`9dad8ba84` 把 `bootstrap` 的 `repairUsersIDSequence` 委托给 `AlignSerial`，并修掉旧实现 `setval(GREATEST(max(id),1))` **无条件回拨**的隐患（会把已发出的 id 重发）；`043cfd44a` 加**静态守卫**——解析 migrations 得出 public 里 86 张序列主键表，`_test.go` 里显式 id 插入若既非 `DEFAULT`/`nextval`、又无 `search_path`/`CREATE SCHEMA`、又未用 helper 即红并点名（8 条豁免全带 file:line 理由 + liveness；第一版用 `strings.Contains(src,"search_path")` 会被注释骗过，已改为 AST 只认代码 token） |
| F-10 | 登录验证的 fail-open 比注释宽（Redis 故障跳过全部策略判定） | P2-1 审查的非阻塞观察 | **已修** · `540637064` + `3dd5b9d9d` + `bc43e62ed` | `loginLockout.RequiresVerification` 出错时从 `return false, nil` 改为「只跳过 lockout 那一支、继续策略链」；两处降级各一条可区分 warn（`degradation=lockout_unavailable` / `policy_unavailable`），第三处设备指纹失败也补了 `device_fingerprint_unavailable`（原来既不上抛也不打日志）。**reviewer 抓到用例只钉 `Always`**：构造「丢弃 new_device/privileged」变异时整包全绿 → 补两条表驱动用例后该变异双红 |
| F-11 | `cmd/api` 从未 `slog.SetDefault`，app 层 warn 绕开 JSON 日志管道 | F-3 审查（reviewer F3-2） | 待派 | `cmd/api/main.go:26` 建了 `slog.NewJSONHandler` 却从未 `slog.SetDefault`（全仓非测试零命中），`bootstrap` 构造 `humanVerifier` 也没 `.WithLogger(logger)` → app 层所有 `slog.Warn`（含 F-3 / F-10 新加的降级 warn）以 text 落 stderr、绕开 `cfg.LogLevel` 与 JSON 管道。**不是本轮引入**，是仓库既有形态。要做：判定是否 `SetDefault`（行为变更需 decision）+ 给 `humanVerifier` 补 `.WithLogger`，或明确「app 层应改用注入 logger」的迁移方向 |
| F-12 | 测试包创建私有 schema 后不清理 | F-9 定位（ops） | 待派 | `database/migrations` 的 badge/celebration 测试留下约 20 个私有 schema（`badge_*`、`celebration_*`、`lifecycle_close_*`），正是让 namespace-blind 断言变随机的土壤；库也会累积对象。不阻塞门禁，但同类随机红会再出现 |
| F-13 | `pluginprocess` 两条用例硬依赖 `/tmp` 可用空间 | F-9 定位（ops） | 待评估 | `TestSandboxProbeAllowsArtifactReadAndTempWrite` / `TestSandboxProbeWritesOnlyConfiguredDataDir` 必须用 `/tmp`（`shortTempRoot()` 的 per-plugin TMPDIR 受内核 `sun_path` 108 字节限制，`t.TempDir()` 会先撞 EINVAL），所以 `/tmp` 打满时它们会以 `EACCES` / `disk quota exceeded` 假红。改成可配置路径是测试基础设施改动，需单独评估 |
| F-14 | 前端权限判定的残余收敛点 | F-1 / 同源残余审查 | 待派 | ① `FORUM_PERMISSIONS.settingsManage` 仍导出父键常量（当前无 gating 调用点），应标注「不得用于门」或删除；② 6 处内联 `roleKeys.includes('super_admin')`（`content.vue`、`extensions/pages.vue`、`provider-slots.vue`、`route-providers.vue`、`useAdminExtensionsManager.ts`、`SFTrustedAdminPageComponent.vue`）与 `canWithUser` 的绕过等价，建议收敛；③ `tests/admin/adminForum.test.ts:220` 是字面数组快照，合法候选增删会误报，建议改「候选 ∩ 父键集 = ∅」规则断言；④ `extension.manage` 在 `layouts/admin.vue` 被重新内联时无单测会红（已接受空档，仅靠 review） |

### 第二轮收尾的教训（新增 5 条）

1. **「棘轮能过」不等于「棘轮能拦」。** F-6 的翻译棘轮第一版只扫顶层 `token.CONST`：把 reason 改成函数内局部 `const` 或包级 `var`，**整包全绿**。而它自己的注释还写着「这些键只以常量/变量形式出现」——变量正是漏的那类。**教训：收集面（scope）必须与它声称覆盖的写法一致；写完棘轮先问「换一种等价写法还红吗」。**
2. **「反射守卫」只能证结构体字段。** P3-7 的 `TestFrontendServiceHoldsNoInProcessChallengeState` 声称包级内存权威会被 DB 落账用例兜住；reviewer 构造「包级 `sync.Map` 门控缓存（未命中即拒、仍调 store.Consume）」变异 → **整包全绿**，而这个变异在生产上正是 P3-7 要修的缺陷（B 副本拒绝 A 副本的合法挑战）。**修法是换成 AST 结构守卫**（拒包级 `sync.Map`/空 map/`make(map)`/结构体字面量藏 map），并把注释改成真话。**教训：注释里的「已被 X 兜住」必须有人真的按那句话去构造反例；否则它只是自我安慰。**
3. **跨包共享测试库的缺陷有三副面孔，且都不是「隔离缺失」。** `users_pkey` 是**显式 id 插入不推进 BIGSERIAL 序列**（landmine 在插入方，不在受害者）；Core 版本断言是**版本串运行时派生 + HEAD 在动**（migrate 与 test 两个读数）；owner 断言是**断言自己没钉 namespace**（同名对象在别人私有 schema 里）。**教训：全仓门红灯先问「这个断言在测它声称的那个对象吗」，再问「谁在我之前改了这个共享状态」；`seq >= max(id)` 是一次观测不是不变量，landmine 要按类扫干净。**
4. **并发 agent 共享一个工作区，中间态会毒化别人的门禁。** 一次未提交的 `declared and not used: at` 让 `cmd/sforum` 建不起来，老运的 996 秒全仓门因此 exit 1（`cmd/sforum: 0 < 基线 126`）。**教训：变异实验放到 `/tmp` 副本上做，或做完立刻还原+提交；跑门禁前先自己 `go build ./...`。**
5. **`/tmp` 是 tmpfs + usrquota，浏览器 QA 的 CDP dump 会把它打满。** 一次 300M 的 `dd` 直接 `Disk quota exceeded`，而 `df` 的「可用」是假象；两条 sandbox 用例因此假红。**教训：浏览器类 QA 收尾必须清理自己的 CDP 目录；门禁类长跑前先验一次 `dd` 可写。**

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

**本节已清空两条**：P3-7（`3a70e0e80` + `abb0c11bf`）与 P3-11（`dc9191a1f` + `692f7265e`）本轮转正并修完，31 条表的「当前状态」已改为「已修」。P3-16 更早已移出（`0d1dc4eb6`）。**仅剩一条非 31 条内的同源风险**：

- **评论计数口径的同构风险（P2-5 的同源问题）。** 触发条件：未来新增第三条写主题计数的路径（批量迁移、后台重算、插件命令），或有人只改 forum 侧不改 moderation 侧。届时必须把计数辅助函数收敛成单一实现；否则口径会再次分叉，而这次分叉不会有报告来提醒。

## 验收与关闭

- 每条关闭时，在本表「当前状态」列写「已修 · commit」，并把验证方式写进关闭条件列的括注（测试名 / 命令 / 浏览器断言）。
- 批次 1 的 allowed + denied 两侧测试已随 `c382e366b` / `fcdc86eda` / `97659db29` / `10f80ccf1` 提交。
- 批次 2 / 3 的缓存、计数、状态机类改动必须有「改动后立即读到新值 / 新状态」的断言，不接受「等 TTL 过期后自然一致」。
- **本轮的验收口径（批次 3 起固定下来）**：①一个改动一个 commit；②交付附真实命令输出（`go build` + `SFORUM_TEST_SHARDS=1 ./scripts/test-api-isolated.sh <包>`），口头汇报不算；③**每条都要变异验证**——去掉修复后对应用例必须红，红/绿两次输出都要贴；④代码改动一律由 reviewer 看 diff，且**要求他自己注入绕过**，不采信员工报的数字。
- **对抗审查的实际价值**（本轮数字）：7 条 P3 里 **4 条被打回返工**（P3-2 证据链、P3-3 装配无证据、P3-9 生产不可达、P3-10 重试不重算），其中 P3-10 是**真 bug**（审查员构造出 `4→3` 降级事件反例）、P3-9 是**真缺口**（修的分支生产永不执行）。没有这一层，这四条都会以「测试全绿」的形式合入。
- **本轮基线红灯的成因**（都不在 31 条里，是上一会话验收口径漏掉的）：上一会话以「聚焦包测试通过」验收，没有跑全仓门禁，于是留下四处——`Controllers/Notifications` 测试包**编译失败**（接口加方法、fake 没跟上）、路由计数 tripwire 435 vs 437、调度目录清单 10 vs 12 + `schedules.md` 漂移、插件 env 探针被静态扫描误判。四处已在本轮修掉；**教训：接口变更必须 `go vet ./...` 一遍所有实现方（含测试 fake），目录类清单要么派生要么只留一处手写闸口。**
- **第二轮收尾时的门禁状态（全绿，含真实输出）**：
  - `cd apps/api && go build ./...` → 退出 0
  - `node tests/validate-architecture-boundaries.mjs` → `Architecture boundary validation passed (2313 production files scanned, 222 files remain above the 500-line review threshold)`
  - `ruby scripts/validate-openapi-refs.rb` → `OpenAPI references OK: checked 3800 refs across 65 files` + `OpenAPI route bindings OK: 14 annotated operations, 10 required ids`
  - `node tests/validate-test-db-isolation.mjs --static-only` → `验收通过：静态 / 运行时 / 覆盖率三层证据齐备（exit 0）`（**收尾时曾一度红**：另一会话的 `a938b11ab`（R7-OPS-02 复审）在 `apps/api/config/config_production_test.go` 里用 `t.Setenv("DATABASE_URL", …)` 验证生产配置 panic 文案与脱敏，被扫描器误判为 `direct-dev-db-read`。该文件与既有 `config_test.go（non-test-database）` 同类、全程不连库，已在 `tests/test-db-isolation-allowlist.json` 登记例外并写明来源，`2ec85ccdf`）
  - `node tests/validate-test-db-isolation.mjs --coverage-only` → **连跑两次 exit 0**：`实际执行：7752 个测试，156 个包 / 基线 6498（735s）` 与 `（651s）`，两次计数一致
  - `cd apps/web && bun run typecheck` → exit 0；`bun test` 全量 → `3449 pass / 0 fail / 27967 expect() / 336 files`
  - 本轮并发很重（多个 agent 同时跑测试 + dev server + 另一会话提交），门禁 735s/651s 慢于基线 500–700s，**不是门禁退化**
- 全部 31 条收敛后，本文件移入 `knowledge/plans/archive/2026-11/`，并在 `README.md` 的 Archived Plans 段落留索引。（本轮已把状态改为 completed，归档动作留给下一次清理会话——同目录还有两份并行台账在办，避免同时搬动 `README.md` 造成冲突。）
