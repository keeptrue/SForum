# Decision: 插件子进程的信任边界（EX2-01 / EX2-04b）与链接安装的摘要必填（RV-12）

## Status

Accepted（2026-11-15，簇 12-A）

## Context

扩展运行时把插件后端当成一个独立进程启动（`ProtocolStarter.startProtocolInstanceLocked`
→ `exec.Command` → hashicorp/go-plugin 握手）。审计提出三条互相咬合的问题：

1. **EX2-04b**：扩展包校验有 5 分钟的元数据指纹缓存（路径集合 + 大小 + mtime +
   权限位，不含内容）。指纹没变就复用上一次的摘要。指纹信息可伪造，所以「同时改
   大小与 mtime」的篡改在 TTL 窗口内不会被发现；而 exec 之前从不重算摘要。
2. **EX2-01**：插件子进程只有 env 白名单，文件系统、网络、子进程都没有约束；代码
   里也没有把「安装/启用已经过 exact-artifact + super_admin 确认」这个前提写死。
3. **RV-12**：`POST /admin/extensions/install-from-url` 的 `sha256` 是可选字段，
   空值直接放过。契约里也写着 optional，所以「来源可追溯」这件事实际取决于调用方
   的自觉。

三者的共同点是**信任链的终点没有被兑现**：授权结论建立在一次可能过期的读取上，
而进程启动时不再复核。

## Decision

### EX2-04b：exec 前按内容重算摘要

`extensions.VerifyRuntimeExecutableDigest`（`app/Models/Extensions/trust_service.go`）
在 exec 前把三份摘要对齐：

| 摘要 | 来源 | 作用 |
|---|---|---|
| ① trusted | `digestInstalledFile`（信任链实际用过的值，可命中指纹缓存） | 授权结论的依据 |
| ② fresh | `digestStableExtensionFile`（按内容重算，句柄前后 TOCTOU 比对） | 内容的真相 |
| ③ declared | `manifest.Backend.Digest` | 构建期写入的 exact artifact 声明 |

①≠② 说明指纹被伪造：拒绝启动，并 `InvalidateVerification` 丢弃被污染的条目。
③≠② 说明制品不是被声明的那一份：拒绝启动。

**为什么不是「把 TTL 调小」或「干脆不要缓存」**：缓存是刚需（后台 GuardPolicyCatalog
每 5 秒遍历全部扩展，整树哈希含数十 MB 后端二进制）。正确做法是让缓存在**读路径**
继续当快路径，而在**唯一不可逆的动作（exec）**前做一次内容级复核。这里也不新增
第二套校验：复核点就是 `startProtocolInstanceLocked` 里 exec 之前的唯一门。

**为什么不需要额外要求「必须声明摘要」**：对 uploaded 来源，授权查询本身是持久的
锚点——内容变了，重算出的 impact digest 不再匹配 grant 行，`ErrTrustGrantNotFound`
会先一步拒绝；声明摘要（②vs③）是第二道独立锚点；内置包的摘要由构建期
`extension digest --write` 写入。本轮不额外要求声明摘要，是为了不改变既有安装/
开发通道的语义（见「残余风险」里对内置包的说明）。

### EX2-01：进程级加固 + 把信任前提写成可读断言

`verifyPluginExecutableArtifact`（`app/Support/Extensions/plugin_command_transport.go`）
是 exec 前的唯一门，也是信任模型自述的地方（插件按设计拥有哪些能力、有哪些残余
风险）。它固定在 `newPluginClientConfig`（受信 grant 解析完成）之后、数据库租约
签发之前执行，因此摘要不一致时连锁带租约都不会签发。

平台级机制抽出到 `app/Support/Extensions/pluginprocess`：

- `Apply`：`Setpgid` 独立进程组（宿主信号广播不再顺带打到插件；整组回收有句柄）；
- `GroupID` / `KillGroup`：`Stop`、`Discard` 与启动失败兜底都按 `-pgid` 回收，
  插件派生的子进程不会变成孤儿；
- `Limit`：`RLIMIT_NOFILE=4096`、`RLIMIT_CORE=0`（崩溃时不把含 host token 的
  内存落盘）。`RLIMIT_NPROC` 默认不设，因为 Linux 按 real uid 计数，对「与宿主
  同 uid」的插件施加它就是给整个用户设上限；运维可用 `SFORUM_PLUGIN_NPROC`
  显式 opt-in；
- cwd 钉在制品目录（后端入口所在目录），并拒绝让它等于或包含宿主工作树——避免
  插件的相对路径写入落进源码树。

**内存不走 rlimit**：Go 运行时会预留大段虚拟地址空间，`RLIMIT_AS` 会直接把插件
打死，`RLIMIT_RSS` 在 Linux 上不生效。内存上限继续用 `GOMEMLIMIT`（软上限，宿主
注入，已有实现）。

**复核到 exec 之间仍有窗口（补记，2026-11-15 返工）**：`verifyPluginExecutableArtifact`
在 exec 之前按内容重算摘要，但「复核完成」与内核真正 `execve` 之间仍有毫秒级窗口。
同 uid 的另一个插件进程可以在这一段里替换掉入口文件（它是同一 uid 的普通文件，
没有 immutable 位、没有 mount namespace）。也就是说本轮的 TOCTOU 保证只覆盖
**读侧**（打开句柄前后比对、审计用的摘要与内容一致），**exec 路径没有闭环**。
要闭环需要内核级手段：`O_PATH` 打开后 `fexecve`、只读 bind mount + 不可变制品目录，
或 namespace 隔离——都超出本轮范围，写在这里是为了不让人把「exec 前重算」误读成
「exec 的字节就是被校验的字节」。

**rlimit 的时序诚实说明**：Go 的 `exec.Cmd` 没有 pre-exec hook，`prlimit(2)` 只能
在 fork/exec 之后由父进程施加，所以它约束的是稳态而不是最初几微秒。要拿到「从第
一条指令起就受限」，需要 launcher 或 namespace 级隔离。

**cwd 语义变了（补记，2026-11-15 返工）**：加固之前插件子进程继承宿主进程的 cwd；
现在 cwd 固定为 `backend.entry` 所在目录（惯例是 `<package-root>/backend`）。所以
插件里的相对路径基准从「宿主启动目录」变成「制品目录」：

- 仓库内 8 个内置插件的后端 `grep` 里 **0 处**依赖相对路径读宿主工作目录，内置包
  不受影响；
- **第三方插件会受影响**：任何 `./x` / `os.ReadFile("config.json")` 之类写法现在
  落在包目录内。写文件要改用绝对路径（`os.UserCacheDir()`、运维配置项、Host API），
  不要写在自己二进制旁边。
- 同一段契约（cwd / 进程组 / rlimit / env 白名单 / 文件系统·网络·子进程不受限）
  已写进作者文档：`docs/extensions/build-and-load.md` 与
  `docs/zh-CN/extensions/build-and-load.md` 的「Runtime process contract / 运行时
  进程契约」小节。

**exec 目标必须与 cwd 同源（补记，2026-11-15 二次返工）**：钉 cwd 的同时，exec 的
目标必须与 `cmd.Dir` 来自**同一次** `filepath.Abs`——`preparePluginCommand` 现在用
`resolvePluginCommandPaths` 一次解析出 `{WorkDir, Entry}`，`exec.Command` 拿 `Entry`。
原因是 `execve` 对相对路径以**子进程 cwd** 为基准解析，而不是宿主 cwd：dev 的
`EXTENSION_ROOT` 是相对路径（`../../storage/extensions`，`config.go` 的缺省值），
`installedFilePath` 因此给出相对入口；只钉 cwd 而不绝对化 exec，内核就会在制品目录
下面再拼一次相对前缀 → `fork/exec ../../storage/.../backend/plugin: no such file or
directory`，插件一起不来 Host 即进 recovery-only，全站 `/api/v1/*` 503
`plugin_runtime_convergence_failed`。回归守卫只有
`TestPreparePluginCommandExecsAbsoluteEntryForRelativePackage`（字符串层断言 `cmd.Path`
绝对，与路径深度无关；`go test -overlay` 还原 exec 一行即 FAIL）。
**端到端冷启动不能当守卫**：POSIX 在 `/` 处钳制多余的 `..`，相对入口的 `..` 段数恒等于
宿主 cwd 层数，只有「制品目录层数 > `..` 段数」才会 ENOENT——一条依赖 checkout 深度的
用例会让不成立的断言冒充证据（首版就这样错过一次）。RV-53 接线后 exec 走
`pinVerifiedExecutable` + `/proc/self/fd/3`，该端点在结构上更不可能再红。
触发面仅限 dev——生产 `config.go` 的启动闸门对相对 `EXTENSION_ROOT` 直接 panic。

**本边界仍未覆盖的测试缺口（2026-11-15 评审记录，非本轮交付）**：`resolvePluginCommandPaths`
的「包内容根为空」与「拒绝 `/` 与 `.`」两条 deny 至今没有对应用例；路径包含性校验里的第二次
`filepath.Abs(root)` 之所以没有窗口，是因为生产代码里 `os.Chdir` 命中数为 0、同包测试串行执行
（`_test.go` 里 5 处 `t.Chdir` 会改进程 cwd，但串行且自动还原）。一旦引入并行测试或在生产路径
里 `Chdir`，这两处要一起补。

**没有做 uid 切换**：`SysProcAttr.Credential` 只在 euid==0 时可用，而非 root 环境
会直接失败；更关键的是插件需要读自己的制品目录与宿主签发的凭证，降到无特权 uid
需要一整套制品属主/chmod 模型，本轮没有交付。盲目在 root 下启用会让所有插件起不
来，所以这里不做「看起来更安全」的假动作。

### RV-12：链接安装必须给出 sha256

`install-from-url` 缺 `sha256` 直接 422（`extension.remote_digest_required`），
在任何出站请求之前拒绝；契约里 `ExtensionRemoteInstallInput.required` 加入
`sha256`。

**为什么选「拒绝」而不是「按响应内容计算并比对」**：后者是拿响应证明响应——同一
份字节既当被校验对象又当校验依据，挡不住投毒的下载链路，只会给调用方一个「已校验」
的错觉。这条路由的全部价值就是调用方手里的外部锚点。

**没有留静默豁免开关**：一个能被默认打开/忘记关闭的开关，等于把审计项原样搬回
默认路径。需要豁免的通道（人工、开发）必须显式构造请求并自带摘要；这比多一个
运行时开关更可审计。代价是管理面板的「手填 URL」调用方必须提供摘要（服务端 422
带双语文案），这属于调用方需要适配的契约变更。

## Consequences

已交付的加固：

- exec 前内容级摘要复核（含 TOCTOU 稳定的重算与缓存污染清理）；
- 独立进程组 + 整组回收；cwd 固定在制品目录；env 白名单（既有）保持不变；
- `RLIMIT_NOFILE` / `RLIMIT_CORE`（+ 可选 `RLIMIT_NPROC`）；
- 链接安装的摘要必填与契约同步；
- 架构基线随提取下调：`protocol.go` 941→899，`protocol_instances.go` 1052→1050
  （RouteTarget 校验、`kill`、`protocolInstanceNotFound` 移入
  `plugin_command_transport.go`；平台机制移入新子包 `pluginprocess`）。

**回收顺序（补记，2026-11-15 返工）**：启动失败的所有分支与 `Stop`/`Discard` 都走同一个
helper：先 `KillGroup(-pgid)`（连同插件派生的子进程），再 `client.Kill()`（回收组长并
Wait）。反过来会在组长被回收后让 `kill(-pgid)` 失去锚点，组内子进程留成孤儿。

**已知遗留（本轮不动，记录在案）**：`isPluginRouteTargetNone` /
`validatePluginRouteTarget` 这对 SSRF 约束落在 `plugin_command_transport.go` 里，
与文件主题（子进程边界）是命名失配。它们是纯函数、只被同包调用，迁移会牵动
`protocol_test.go`；等下一次动路由目标校验时一并拆到独立文件或子包。

**残余风险（明确不在本轮范围内，不得当作已解决）**：

- 文件系统仍不受限：插件与宿主同 uid，能读写其它插件的制品目录、`storage/` 与
  宿主可见的任何路径。正因如此 EX2-04b 的「伪造 size+mtime」才在理论上可达，
  也正因如此 exec 前必须按内容重算。
- 网络仍不受限：插件可以连任意地址（出站 SSRF 面由插件自身决定）。
- 子进程仍不受限：`RLIMIT_NPROC` 默认关闭；插件可以 spawn 任意进程，这些进程
  继承同一 uid 与同一份 env 白名单。
- 内核级隔离（user/mount/network namespace、seccomp、cgroup）未实现，所以上述
  三条都还是宿主信任域内的问题。
- 内置包（`SourceBuiltin`）的运行时身份不走 grant 查询；它的持久锚点是构建期写入
  的声明摘要。若某个内置包没有声明摘要，缓存被污染后本轮只剩「exec 前重算」这一层保护。

未来做真隔离的切入点（按代价从低到高）：

1. 构建期/安装期把后端入口的声明摘要变成硬性要求（对 executable 一律必填），
   这样 ③ 成为所有来源的持久锚点；
2. `pluginprocess` 里加 launcher（`fork → setrlimit/prctl → exec`），把 rlimit 与
   `no_new_privs` 前移到第一条指令之前；
3. cgroup v2 按插件建组，限制 CPU/内存/PID 数（可观测、可分插件调参）；
4. user namespace + mount namespace 只读挂载插件自己的制品目录，配合 uid 映射做
   真正的文件系统边界；
5. 最后才是 seccomp 白名单与网络 namespace（需要先补齐插件对网络/子进程的声明式
   能力列表，否则会大面积破坏现有插件）。

## References

- 审计项：EX2-01、EX2-04b、RV-12（簇 12-A）
- `knowledge/decisions/2026-10-19-package-verification-memoization.md`（指纹缓存的
  原始取舍；本决策把它的「TTL 兜底」前提收紧为「exec 前必须重算」）
- `knowledge/decisions/2026-10-17-dev-zero-command.md`（开发期零命令通道，与
  RV-12 的「不留静默豁免」相关）
