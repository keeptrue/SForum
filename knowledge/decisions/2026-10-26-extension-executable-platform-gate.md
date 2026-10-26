# 决策：可执行后端的架构闸门（安装 422 / 运行 409）

Status: accepted
Date: 2026-10-26
Related: `2026-10-25-core-version-surface.md`（同一条「安装 vs 运行」判定轴）、
兄弟仓 `sforum-app-store` 的对应决策记录 `2026-10-26-platform-availability.md`（商城侧）
Plan: `../plans/2026-10-26-extension-platform-gate.md`

## 背景

Core 在**进程启动**时才会发现架构不对：`Support/PluginBootstrap/diagnostics.go`
能识别 `exec format error`，但那时扩展已经装好、已经启用。安装阶段完全放行，
于是「装了一个永远起不来的扩展」只能靠运维在启动日志里发现。

商城的目录能按宿主 `GOOS/GOARCH` 过滤，但详情弹窗会列出全部版本，选到不支持的
版本时平台还会静默回退到 `platforms[0]`，把异架构包递给宿主。也就是说**两侧都
没有最终裁判**。

## 决策

### 1. 判据读文件头，不读清单声明

`Support/ExtensionPackage.DetectTarget(io.ReaderAt) (Target, bool)`，顺序：

1. **fat Mach-O**（universal 二进制里任一架构都能跑，必须优先挑中宿主架构；
   交给 `debug/buildinfo` 先看只会读 `Arches[0]`，会把「先 amd64 后 arm64」的包
   在 arm64 宿主上误判成不兼容）
2. `debug/buildinfo`（Go 二进制里 `GOOS/GOARCH` 是链接器写死的精确事实）
3. `debug/elf` → `debug/macho` → `debug/pe` 兜底

不要求清单声明 `backend.platforms`：

- 声明是**承诺**，文件头是**事实**。一个手误写成 `any` 的清单会让闸门失效，而
  文件头不会说谎。
- 加声明字段就要改 manifest schema、加校验、加迁移、加文档，还要处理「声明与
  实际不符」的第二套错误码。当前问题不需要这些。
- 代价是探测是启发式的：非 Go、非标准格式判「未知」。这是 D8 接受的边界。

### 2. 安装与运行两阶段都拒绝

| 阶段 | 入口 | 状态码 | 理由 |
| --- | --- | --- | --- |
| 安装 | `InstallOrUpgradeArchive` | **422** | 这个包本身不可用；拒绝发生在落盘前，不留下永远起不来的候选 |
| 运行 | `requireEnablePreflight`（启用/主题激活/升级/回滚/重启共用） | **409** | 当前状态冲突：制品已存在，但不能在本机跑 |

两阶段共用 `ExecutablePlatformMismatchError{Entry, Required, Host, Stage}`，`Stage`
复用 `CoreCompatibilityStageInstall` / `CoreCompatibilityStageRuntime` —— 与
coreVersion 闸门是同一条判定轴，不引入第二套阶段常量。API 回传
`data.fields = {required, host, entry}`（均为字符串数组）。

安装阶段读的是 `ReadArchive` 已经解进内存的 `packageFiles`，不额外落盘再读；
运行阶段 `requireExecutablePlatform` 只 `os.Open` 读文件头。

### 3. 只作用于「插件 + 声明了 backend.entry」

主题、纯前端插件、`Backend.Entry` 为空的脚本型后端一律跳过。**内置包不豁免**：
内置包对 coreVersion 豁免是因为「约束写错时运维没有修复手段」，而架构错是构建
产物问题，不是声明问题；错架构的内置包同样跑不起来。

### 4. 未知即放行（D8）

`Target.CompatibleWith` 对任一维度的空值都返回 true：

- ELF 的 `OSABI_NONE` 被 linux/android/solaris/illumos/dragonfly 共用，猜成 linux
  会在非 linux 的 ELF 宿主上把合法包判死，所以它表示「不知道」。
- 探测失败、文件缺失、入口不在包里，都放行，把最终裁判交回 Core 原有的启动诊断
  （`exec format error`）。

宁可漏拦一次，也不要制造假阳性——假阳性会让运维看到「明明能跑的包装不上」。

### 5. 与 coreVersion 闸门的关系

两者是**并列**的安装/运行闸门，不是替代：

| | coreVersion 闸门 | 架构闸门 |
| --- | --- | --- |
| 依据 | 清单 `sforumVersion` 声明 | 可执行文件头事实 |
| 未知时 | 无效 core 标签 → 跳过并告警 | 探测不出 → 放行 |
| 内置包 | 豁免 | 不豁免 |

执行顺序（安装）：`validateManifest` → `checkArchiveExpectations` →
`ensureCoreCompatible` → `requireArchiveExecutablePlatform` → 类型权限 switch。
先判 core 再判架构：清单层面的问题比制品层面的问题更早暴露。

### 6. 跨仓边界（商城侧两条偏离，详见商城决策记录）

- `logic.ts` 的 `defaultPlatform`：`target` **已知**时无 exact/wildcard 即返回
  `null`（不再静默回退到 `platforms[0]`）；`target` **缺失**时保留旧的
  `wildcard ?? platforms[0]`。老商城不发 `target`，删掉回退会把安装按钮凭空锁死。
- `catalog.ts` 仍保留 `renderFoot` 适配器（节点构造已抽到 `foot.ts` 的
  `footNodes`），文件 975 行 < 1000。计划原写「抽到约 910 行」，实际只抽了
  节点构造；硬上限未破，接受该偏离。

## 不做

- 不改清单 schema、不加 `backend.platforms`。
- 不改公开商城站点、不加卡片级安装按钮。
- 不自动停用已启用的错架构扩展（留给启动诊断与运维）。

## 重开条件

1. 需要表达**跨平台多架构单包**（一个 zip 内含多份后端）时：当前 `DetectTarget`
   只返回单一目标，需要改成集合判定。
2. 需要拦截非 Go、非标准容器格式的后端（脚本、WASM、自定义字节码头）时：现在
   一律放行，等于没有闸门。
3. 探测出现真实假阳性/假阴性案例时：先补该格式的解析，再考虑清单声明作为
   补充证据（声明永远不能覆盖文件头事实）。

## 证据

- `apps/api/app/Support/ExtensionPackage/platform_test.go`：真实 `os.Executable()` +
  合成 ELF/Mach-O/fat/PE/脚本/空/截断。
- `apps/api/app/Models/Extensions/platform_gate_test.go`：安装拒绝且未落盘、
  主题/无入口/未知格式放行、运行阶段拒绝、内置包不豁免。
- `apps/api/app/Http/Controllers/Extensions/errors_compatibility_test.go`：422/409
  分流与三键字段。
- 真机：UI 上传 darwin/arm64 包 → 422 `extension.platform_incompatible`，
  `fields={entry:[backend/plugin], host:[linux/amd64], required:[darwin/arm64]}`；
  同路径上传 linux/amd64 包 → 201 installed。
