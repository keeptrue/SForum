# Decision: 插件子进程的信任边界（EX2-01 / EX2-04b）与链接安装的摘要必填（RV-12）

## Status

Accepted（2026-11-15，簇 12-A）

**2026-11-16 追加**：EX2-01 由「进程级加固」升级为「内核级隔离」——插件子进程经
沙箱启动器（Landlock 文件系统 + 可选网络、cgroup v2 限额、`no_new_privs`）启动。
见下方「EX2-01b」。

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

### EX2-01b：内核级隔离（2026-11-16 收尾）

#### 机制：启动器（launcher）

宿主用 `os.Executable()` 重新 exec 自己，argv 重排为
`{__sforum_plugin_sandbox_exec, specJSON, pluginArgv...}`。`cmd/api` 与 `cmd/worker`
的 `main` **第一条语句**调 `enterPluginSandboxLauncher()`：命中保留 argv[0] 就
`os.Exit(pluginprocess.RunLauncher(os.Args))`，否则 `pluginprocess.EnableLauncher()`
声明「本二进制可作为启动器被重新 exec」。

**为什么用显式声明而不是「猜自己在测试/生产」**：没有 `main` 拦截的二进制（典型是
`go test` 的测试二进制）会把 spec JSON 当成命令行参数解释，产生一堆难查的启动失败。
声明让这种错配变成「降级为直接 exec + 结构化原因」。

启动器内的顺序是有因果的，不能调换：

1. `runtime.LockOSThread()`：Landlock 的 `restrict_self` 按**线程**生效，必须在同一条
   线程上完成 restrict 与 `execve`；`execve` 会替换整个进程，域随之被插件继承；
2. per-plugin TMPDIR（`<宿主TMPDIR>/sforum-plugin-sandbox/<slug>-<pid>`，0700）并
   `setenv TMPDIR`：插件自身临时文件与**潜在 unix socket**都落在这里，钉住之后
   沙箱只需要给这一个目录写权限。
   事实精度：go-plugin v1.8.0 在非 Windows 上把 RPC 监听建成 unix socket
   （`server.go:528-536` 的 `serverListener → serverListener_unix`，路径由
   `os.CreateTemp(os.TempDir(), "plugin")` 决定），SForum 没有设置
   `UnixSocketConfig`——它只影响 socket 目录/属组的选择，不改变传输方式。
   探针里 `unix-socket=ok` 就是在这条链路上验证的（不是假设它存在）；
3. cgroup（建组 → 写限额 → 搬进自己），必须在 Landlock 之前：之后就拿不到
   `/sys/fs/cgroup` 的写权限了；
4. rlimit（`NOFILE`/`CORE`/可选 `NPROC`）——这一步把「限额从第一条指令起生效」
   真正兑现，父进程的 `prlimit` 只能约束稳态（见上方 `Limit` 的说明）；
5. `PR_SET_NO_NEW_PRIVS`（Landlock 的前提，且它本身就有价值）；
6. Landlock ruleset（FS，可选 NET）→ `landlock_restrict_self`；
7. 把能力报告写成一行 JSON 到 `spec.ReportFD` 并**关闭写端**；
8. `execve(spec.Entry, pluginArgv, env)`。`spec.Entry` 在生产路径上仍然是
   `/proc/self/fd/3`：EX2-04b / RV-53 的「校验与使用是同一个 inode」穿过启动器
   依然成立（`TestSandboxLauncherExecutesPinnedInode` 守这条）。

报告管道：`ExtraFiles` 里排在已校验 inode 之后，fd 号由
`3 + len(ExtraFiles)` 算出并写进 spec（不硬编码 4），启动器写完即关闭，因此插件的
fd 表里不会留宿主的报告 fd。

**取报告的三个位置（P1-1）**：成功路径在 `applyPluginProcessLimits`（子进程已出现、
插件尚未发布）；**失败路径**在 `abortPluginStart`（启动失败的唯一出口），先取报告
再回收进程，因此「这次启动到底有没有被内核约束」在失败时同样可回答；再叠一个
`defer pluginprocess.DrainLaunchReport(cmd)` 兜底——它挂在 `defer executable.Close()`
旁边、**早于** `issueProtocolDatabaseLease`：租约失败也是提前 return，defer 放晚了
那条分支就只能等 10 分钟定时器（P2-1 复审点）。`DrainLaunchReport` 幂等：
`TakeLaunchReport` 先关宿主的写端再读，所以「命令从未 Start」的分支会立刻拿到 EOF，
不会为了等一个不存在的写端阻塞（初版这里有 2 秒超时等待，实测拖慢失败路径）。
10 分钟定时器只作为最后的 fd 回收兜底。

**env 白名单只有一个入口（P1-2）**：`plugin.ClientConfig.SkipHostEnv = true`。
go-plugin 默认会在 Start 时 `append(cmd.Env, os.Environ()...)`，那会让
`buildPluginProcessEnv` 的白名单形同虚设（DATABASE_URL、会话签名键、SMTP 凭据都会
进插件）。关掉之后，插件 env 只剩白名单（PATH/HOME/TMPDIR/LANG/TZ/APP_ENV/
`SFORUM_SETTING_*` 等）加上 go-plugin 自己追加的握手变量。

**CA 与代理（本轮 P1）**：`SSL_CERT_FILE`、`SSL_CERT_DIR`、`HTTPS_PROXY`、
`HTTP_PROXY`、`NO_PROXY`（含小写形式）属于**部署级非密钥配置**，必须一起放行：
内置的 smtp / storage-s3 / auth-github / web-push 都直连外部端点，而插件侧没有
CA 或代理的设置项；不放行就等于「只能挂自签 CA」的部署直接
`x509: certificate signed by unknown authority`。它们与 Landlock 里那两条 CA 只读
规则**成对存在**：env 负责把路径交给插件，FS 规则负责让那个路径可读——缺任何一边
都是死路（有变量读不到、或能读却拿不到变量）。`TestPluginClientConfigSkipsHostEnvironment`
同时断言「CA/代理存活」与「DATABASE_URL 仍被挡」。

#### 文件系统（默认开启）

| 类别 | 路径 | 权限 |
|---|---|---|
| 制品目录（后端入口所在目录） | `<package>/backend` | 读 + 执行 |
| 包内容根 | `<package root>` | 只读 |
| per-plugin TMPDIR | `<宿主TMPDIR>/sforum-plugin-sandbox/<slug>-<pid>` | 读/写/建/删 |
| 配置数据目录（P0） | `EXTRA_RW` + `SFORUM_SETTING_*` 里的绝对路径值 | 读/写/建/删 |
| 运行时 | `/usr` `/lib` `/lib64` `/bin` `/sbin` `/etc/ssl` `/etc/pki` `/etc/ca-certificates` `/etc/resolv.conf` `/etc/hosts` `/etc/nsswitch.conf` `/etc/passwd` `/etc/group` `/etc/localtime` `/proc/self` `/sys/devices/system/cpu` `/sys/kernel/mm/transparent_hugepage` | 只读 |
| 动态链接器 | `/lib` `/lib64` `/usr/lib` `/usr/lib64` | 读 + 执行 |
| 字符设备 | `/dev/null` `/dev/zero` `/dev/full` `/dev/urandom` `/dev/random` | 读 + 写 |

其余路径**默认拒写、默认拒读**。handled access 位按 ABI 裁剪：base（ABI1）+
`REFER`（ABI≥2）+ `TRUNCATE`（ABI≥3）；`IOCTL_DEV`（ABI5）与 `RESOLVE_UNIX`（ABI8）
**刻意不处理**——处理它们会打断 `/dev/null` 等既有用法，而在「插件本就能读自己包根」
的前提下收益不成立。

两个已踩实、必须留档的细节：

- **动态链接器需要 EXECUTE**。第一版只给 `/usr` `/lib` 只读，结果 execve 直接
  EACCES：内核对「程序解释器」的检查发生在 execve 里。实测
  `access(X_OK)`/`open(O_RDONLY)`/`open(O_PATH)` 全绿而 execve 返回 EACCES 就是
  这个签名。现在只给库目录 EXECUTE，**不给 `/bin` 与 `/usr/bin`**。
- **单条规则失败不放弃整个规则集**：容器里没有 `/lib64`、或某路径不存在时跳过即可，
  但规则集里「制品目录」缺失会让插件连 exec 都做不了——所以探针直接断言
  「制品目录内文件可读 + 可执行」。

**可写面（P0 修复点）**：默认策略下插件唯一可写的地方是自己那棵包根之外的
「配置数据目录」，它由两条来源合成：

1. `SFORUM_PLUGIN_SANDBOX_EXTRA_RW`（冒号分隔的绝对路径）：运维显式放开的目录；
2. **设置项里的绝对路径值**：宿主把该插件的设置以 `SFORUM_SETTING_*` 注入子进程时，
   凡是形如绝对路径的值（`root_path` / `data_dir` / 未来的键，一律不认名字）都自动
   并入可写面。

两条来源走**同一份收敛函数** `sandboxWritableSettingPath`（`EXTRA_RW` 也过它，
所以相对路径不会被按启动器 cwd 解析、`. ` 不会把制品目录变成可写面）。拒绝规则
（都是「看起来像绝对路径但不是数据目录」的真实形态）：

| 规则 | 挡住什么 |
|---|---|
| 非绝对 / 空 / 根目录 | 相对路径、`/` 整盘可写 |
| 含 `..` 路径段 | 穿越 |
| 含 `://` | `public_base_url` 这类 URL（Unix 下也能过 `IsAbs`） |
| 以 `//` 开头 | POSIX 上等价单斜杠，但也是 URL 被冒号切碎后的残骸（`EXTRA_RW=https://cdn/x` → `https` + `//cdn/x`，后者能过 `IsAbs`） |
| 深度 < 2（只有一级组件） | `/files` 这类 URL path 形态 |
| 已存在且**不是目录** | `ca_bundle=/etc/ssl/certs/ca.pem` 这类只读文件设置被升格成可写 |

stat 失败（不存在）时放行：首次创建数据目录是预期路径，启动器会 `MkdirAll` 出来。
**代价（明确记录）**：单组件数据根（例如 `/data`）两个入口都不接受，需要下钻一层
（`/data/sforum`）——这是「按形态判断」换来的确定性，等有了显式的数据目录声明字段
再放开。

威胁模型：第 2 条不是「给插件任意路径的授权」，而是把**运维通过设置表显式交给这个
插件的数据目录**变成可写面。值来自宿主设置表，插件自己改不了；规则通用（不认插件名
也不认键名），所以它是「插件的数据目录」而不是「插件的自选路径」。

可写面在进 Landlock **之前**由启动器 `MkdirAll` 出目录本身：规则挂在 inode 上，
路径不存在就加不进去，而插件里的 `MkdirAll(root)` 又需要 root 的父目录可写——那正是
我们不给的。只建目录，内容仍由插件自己管。

运维逃生口（只读放宽 / 需要 exec 外部工具 / 需要写盘，三者独立）：
`SFORUM_PLUGIN_SANDBOX_EXTRA_RO`、`SFORUM_PLUGIN_SANDBOX_EXTRA_EXEC`、
`SFORUM_PLUGIN_SANDBOX_EXTRA_RW`（均冒号分隔）。

**内置写盘插件的现状（修正上一版的错误断言）**：`sforum-storage-fs` 的
`SFORUM_SETTING_ROOT_PATH` 通过第 2 条自动可写（探针 ⑥ 在默认 env 下实测
`MkdirAll + WriteFile + CreateTemp` 全通）；其他要写盘的插件走 `EXTRA_RW`。
上一版写的「内置插件都是 Go 二进制，不受影响」是错的——它把「静态链接」和
「不需要写盘」混为一谈。

#### 网络（默认关闭，显式 opt-in）

`SFORUM_PLUGIN_SANDBOX_NETWORK=off|bind|all`（默认 `off`）。ABI≥4 才支持；
`bind` 处理 `BIND_TCP`，`all` 再处理 `CONNECT_TCP`；规则集里**不加任何端口规则**，
因此是默认拒绝。

**为什么默认不是 deny（这是一个有证据的产品判断，不是漏做）**：

- `RouteTarget` 契约要求插件**自报 loopback http(s) 地址**（`validatePluginRouteTarget`），
  也就是插件必须能 `listen` 一个 TCP 端口给宿主代理；
- 内置插件里**四个**直接 dial 外部服务：`sforum-smtp`（SMTP）、
  `sforum-storage-s3`（S3）、`sforum-auth-github`（GitHub OAuth）、
  `sforum-web-push`（`backend/provider.go:81` 的推送端点）。

默认 deny 会让这两类插件集体失效，而本轮还没有「插件声明自己需要网络」的清单——
那正是 seccomp 与网络 namespace 的前置条件（见「未来切入点」）。机制完整、默认关闭，
按部署意图显式开启；`all` 模式下探针实测 `connect` 返回 EACCES，策略关闭时同一
监听可达。

#### cgroup v2（默认开启）

本机实测（不要重猜）：

- 本进程所在 `dsh-subprocess-*.scope` 的 `cgroup.controllers` 是 `memory pids`，
  但 `cgroup.subtree_control` 为空；往有进程的 cgroup 里写控制器得到 **EBUSY**
  （no-internal-process 规则）。因此「在自己的 scope 下建子组」拿不到
  `memory.max`/`pids.max`；
- 最近的祖先 `app.slice` 已由 systemd 把 `memory pids` 放进 `subtree_control`、
  自身**没有进程**、属主是当前用户：在它下面建组可以真正写入并生效
  （实测 `memory.max=100663296`、`pids.max=48`、`memory.swap.max=0`，进程也被
  搬进去了）。

因此建组策略是「就地优先、就近兜底」：先试自己的 cgroup（只有在它没有进程时才可能
成功开启控制器），再自下而上找第一个**已经委派好 memory+pids** 的祖先；都没有就
降级并给出结构化原因（含 errno）。刻意不往祖先的 `subtree_control` 里写控制器：
那是一次影响整个层级的动作，机器上已经有 systemd 在管。

限额：`memory.max` 默认 `512M`、`pids.max` 默认 `512`、`memory.swap.max=0`（否则
可换出逃逸）。报告里的 `cgroup_limits` 是**内核读回值**而不是我们写入的字符串。
组名 `sforum-plugin-<slug>-<hash8>`：上一代已退出的空组会在下次启动时被 `rmdir`
回收；上一代还活着（升级重叠）时 `rmdir` 失败，退到带序号的备用名字。

#### 降级语义与观测点

- 三层各自独立三态：`enforced` / `degraded` / `disabled`；**任何一层不可用都不拒绝
  启动**。`degraded` 一律带原因（含 errno 文本），`disabled` 表示策略关闭；
- 观测点是宿主**现有**的 slog 审计落点，一个插件启动一条记录，三种事件：
  `plugin.sandbox.enforced`、`plugin.sandbox.degraded`、`plugin.sandbox.unavailable`
  （最后一类是「启动器都没接上」，例如宿主二进制没有声明启动器）；
- 字段：`landlock_abi`、`filesystem`/`filesystem_reason`、
  `network`/`network_policy`/`network_reason`、`cgroup`/`cgroup_path`/`cgroup_reason`、
  `no_new_privs`、`temp_dir`；
- 三层总开关：`SFORUM_PLUGIN_SANDBOX=off`（含 FS/网络/cgroup），另有
  `SFORUM_PLUGIN_SANDBOX_FS`、`SFORUM_PLUGIN_SANDBOX_CGROUP` 单独关。**拼错**
  一律落到更严格的一侧（`FS=of` 不会静默关掉沙箱；`NETWORK=of` 会落到 `all`）。

#### 运行时证据（不是源码断言）

`pluginprocess` 里的六个探针把测试二进制自己当启动器重新 exec，因此断言的是内核
行为而不是代码形状：

| 探针 | 断言 | 本机实测 |
|---|---|---|
| ① 宿主文件不可读 | `/etc/shadow` 被拒 **且** 宿主可读的 canary 在插件里 EACCES | `shadow=EACCES canary=EACCES` |
| ② 制品可读 + 私有 TMPDIR 可写 | 制品目录内文件 `ok`，`CreateTemp` 与 TMPDIR 里的 unix socket 都 `ok`；插件 fd 表里没有宿主的 pipe | `artifact=ok:artifact temp=ok unix-socket=ok` |
| ③ 出站 TCP 被拒 | 策略 `all` 时 `connect=EACCES`，策略 `off` 的同一监听 `connect=ok` | `all→EACCES / off→ok` |
| ④ cgroup 限额落地 | `memory.max`/`pids.max` 文件值 + 插件 pid 在 `cgroup.procs` 里 + 插件自述 `/proc/self/cgroup` 一致 | `memory.max=100663296 pids.max=48 live-procs=[pid]` |
| ⑤ Landlock 不可用 | 注入 ABI=0 → 两侧 `degraded` 且有原因，插件**照常启动**、`no_new_privs` 仍置位 | `filesystem=degraded reason="landlock is unavailable (injected ABI 0)"` |
| ⑥ 可写面（P0 回归） | 默认策略下能 `MkdirAll+WriteFile+CreateTemp` 到设置项给的数据目录，同时宿主目录/`/etc/hosts`/`$HOME` 都写不了 | `data-dir=ok host-dir=EACCES etc-hosts=EACCES home=EACCES` |
| 附加：钉住 inode | exec 目标仍是 `/proc/self/fd/3`，argv[0] 是可读入口名，制品目录可读 | `exec=/proc/self/fd/3 argv0=pinned-plugin artifact=ok:artifact` |

判别式说明：`/etc/shadow` 对非 root 本来就是 EACCES（DAC），单看它证明不了任何事；
真正的判别式是「宿主能读、插件不能读」的 canary。`requireLandlock` 刻意区分
「内核不支持 → skip」与「策略要了却没生效 → fail」，否则摘掉 Landlock 应用时
探针会跟着 skip 而不是变红。

变异验证（做完即复原）：

- 摘掉 `landlock_restrict_self` → 探针 ① 变红（`canary=ok:host-only`）、③ 变红
  （`connect=ok`）；
- 摘掉 cgroup 限额写入 → 探针 ④ 变红（`memory.max="max"`，即内核默认无限制）；
- 摘掉「设置项路径 → 可写面」这条规则 → 探针 ⑥ 变红（`data dir: "EACCES"`）；
- 摘掉 `SkipHostEnv: true` → env 哨兵用例变红
  （`host environment leaked into the plugin process: SFORUM_TEST_HOST_SECRET=leak`）；
- 摘掉 `SSL_CERT_FILE` 的白名单项 → env 用例变红
  （`SSL_CERT_FILE did not survive the allowlist`，同时可看到 DATABASE_URL 仍然不在）；
- 摘掉 `EXTRA_RW` 的过滤（退回 `splitPathList`）→ 变红
  （保留 `." /files / https` 等原始条目，`//cdn/x` 残骸也在里面）。

#### 行数余量（下一轮的硬约束）

`protocol.go` 与 `protocol_v2_client.go` 停在上限的**精确值**（897/897、1003/1003，
`tests/architecture-boundaries-baseline.json`）：闸门是双向的，涨一行失败、少一行也
要求同轮下调 baseline。这两轮的新增都是靠「行内注释 + 内联局部变量」才塞进去的。
**下一轮任何增量请先抽文件**（`newPluginClientConfig` 的版本开关、`startProtocolInstanceLocked`
的失败分支都是干净的抽出边界），不要再用行内压缩换空间。

#### 未做与残余（不得当作已解决）

- **没有 uid 映射 / user namespace**：本机 `unshare -m/-n/-p` 与写
  `/proc/self/uid_map` 都是 EPERM（AppArmor），mount/net/user namespace 不能作为
  必需路径；生产容器的能力集又各不相同（`SysProcAttr.Credential` 只在 euid==0 时
  可用），所以「按插件降 uid」需要一整套制品属主/chmod 模型，本轮没有交付。
- **同 uid 互发信号仍可行**：ABI≥6 有 `LANDLOCK_SCOPE_SIGNAL` 可以切断，但它同时
  会让插件无法向域外任何进程发信号（含运维脚本），本轮不打开。
- **seccomp 未上**：第三方二进制没有能力声明，白名单只能在「插件声明自己用哪些
  系统调用」之后才有意义；先上会大面积破坏未知插件（Go 版本不同，syscall 面也不同）。
- **文件系统的残余**：插件仍可读自己的包根与运行时目录；但它**不再能**读宿主
  `storage/`、`.env`、其它插件的制品目录。代价是行为变更：插件写自己二进制旁边、
  写 `HOME`、exec `/bin/sh` 与 `/usr/bin/*` 现在都会失败（脚本型后端需要
  `EXTRA_EXEC`）。**写盘能力的边界**：`sforum-storage-fs` 的 `root_path` 通过
  「设置项绝对路径 → 可写面」自动可写；其他写盘插件走 `EXTRA_RW`。上一版这里写的
  「内置插件都是 Go 二进制，不受影响」把「静态链接」和「不需要写盘」混为一谈，已删。
- **网络残余**：默认 `off`，即出站/监听仍由插件自行决定（见上面的产品判断）。
- **cgroup 残余**：没有委派祖先的机器（多数非 systemd 容器、CI）会走 `degraded`。
  空组目录会留到下一次同插件启动时回收；插件被永久删除后目录只剩几个空 inode。
- **cgroup 落点（运维后果）**：控制器只能从「已经委派 memory+pids 且自身没有进程」
  的祖先拿，所以组常常落在 `app.slice`（本机就是这样），**不是**服务自己的 scope。
  后果：`systemctl status` / `systemd-cgtop` 里看到的是 `app.slice/sforum-plugin-*`；
  按服务做 cgroup 级统计/清理时需要知道这一层偏移。若运维希望组落在服务 scope 内，
  需要给该 unit 加 `Delegate=yes`（此时自己的 scope 就能开控制器）。
- **同插件两实例并发建组（rmdir 竞态）**：升级重叠窗口里两个实例可能同时想用同一个
  组名。`rmdir` 失败时退到 `-2/-3` 后缀，所以不会互相覆盖；但这意味着**短时间内存
  在两个组**，限额各自独立、统计需要按前缀汇总。极端情况下（前一代实例的进程恰好
  在这两步之间全部退出）会留下一个空组，由下一次启动回收。
- **临时目录回收按 24h mtime**（P2 遗留）：启动器会清理 `sforum-plugin-sandbox/`
  下超过 24 小时没被改动过的目录。触发条件：一个**长跑且不写临时文件**的插件，
  其 TMPDIR 目录 mtime 会一直不变，于是可能被**下一个插件的启动器**删掉。删掉之后
  该插件已有的打开 fd 仍然有效，只是新 `CreateTemp`/socket 路径会失败。要彻底修需要
  按 pid 存活判断（例如记录启动时间戳 + 检查进程），本轮不做。
- **`GUARD_HELPERS` 同名取最后一个**（契约侧遗留）：路由 guard 的 helper 目前是
  「同名后写覆盖」，没有冲突检测；插件并发注册同名 helper 时先注册者被静默覆盖，
  排查成本高。修法是注册期拒绝重复名，属契约/注册表轮次，不在本轮。
- **设备 ioctl / unix socket 路径解析**：`IOCTL_DEV` 与 `RESOLVE_UNIX` 未处理。
- **启动器本身不是特权边界**：它只能约束「它要 exec 的那个进程」。同 uid 的进程
  可以自己 exec 宿主二进制并带一个宽松 spec，但那不会得到它原本没有的权限。

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

**残余风险（2026-11-16 更新：前三条已由 EX2-01b 收窄，仍未消除的部分保留）**：

- 文件系统：**已由 Landlock 收窄**（默认拒读/拒写其它路径）。仍未覆盖的是
  「插件可以读自己的包根、运行时目录、以及 TMPDIR 里的残留」；EX2-04b 的
  「伪造 size+mtime」之所以在理论上仍可达，仍然因为插件与宿主同 uid——这正是
  exec 前必须按内容重算的理由（与隔离措施互补，不是替代）。
- 网络：**机制已就位、默认关闭**（`SFORUM_PLUGIN_SANDBOX_NETWORK=off`）。默认
  不 deny 的原因见 EX2-01b：RouteTarget 要求插件监听 loopback TCP，内置 smtp /
  storage-s3 / auth-github 需要出站 dial。
- 子进程：仍不受限（`RLIMIT_NPROC` 默认关闭；cgroup `pids.max` 只限数量，不限
  「能不能 spawn」）。插件可以 spawn 任意进程，这些进程继承同一 uid、同一份 env
  白名单，以及**同一份 Landlock 域**（域随 fork/exec 继承）。
- 内核级隔离的缺口：user/mount/network namespace（本机 EPERM）、seccomp（缺插件
  能力声明）、uid 映射（缺制品属主模型）仍未实现；`LANDLOCK_SCOPE_SIGNAL` 未启用，
  同 uid 互发信号仍可行。
- 内置包（`SourceBuiltin`）的运行时身份不走 grant 查询；它的持久锚点是构建期写入
  的声明摘要。若某个内置包没有声明摘要，缓存被污染后本轮只剩「exec 前重算」这一层保护。

未来做真隔离的切入点（按代价从低到高；2、3 已在 EX2-01b 交付）：

1. 构建期/安装期把后端入口的声明摘要变成硬性要求（对 executable 一律必填），
   这样 ③ 成为所有来源的持久锚点；
2. ~~launcher（`fork → setrlimit/prctl → exec`）~~ **已交付**（EX2-01b）；
3. ~~cgroup v2 按插件建组，限制内存/PID 数~~ **已交付**（EX2-01b；CPU 未限，
   留作按插件调参时的下一步）；
4. user namespace + mount namespace 只读挂载插件自己的制品目录，配合 uid 映射做
   真正的文件系统边界（需要先解决本机 EPERM 与容器能力差异）；
5. 最后才是 seccomp 白名单与网络 namespace（需要先补齐插件对网络/子进程的声明式
   能力列表，否则会大面积破坏现有插件）——网络那一半已经以「默认关闭的 Landlock
   网络规则」提前落地，等的就是这份能力清单；
6. `LANDLOCK_SCOPE_SIGNAL`（ABI≥6）：切断同 uid 互发信号，代价是插件无法向域外
   进程发信号，需要先确认没有插件依赖这条路径。

## References

- 审计项：EX2-01、EX2-04b、RV-12（簇 12-A）
- `knowledge/decisions/2026-10-19-package-verification-memoization.md`（指纹缓存的
  原始取舍；本决策把它的「TTL 兜底」前提收紧为「exec 前必须重算」）
- `knowledge/decisions/2026-10-17-dev-zero-command.md`（开发期零命令通道，与
  RV-12 的「不留静默豁免」相关）
