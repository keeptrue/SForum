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

**rlimit 的时序诚实说明**：Go 的 `exec.Cmd` 没有 pre-exec hook，`prlimit(2)` 只能
在 fork/exec 之后由父进程施加，所以它约束的是稳态而不是最初几微秒。要拿到「从第
一条指令起就受限」，需要 launcher 或 namespace 级隔离。

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
