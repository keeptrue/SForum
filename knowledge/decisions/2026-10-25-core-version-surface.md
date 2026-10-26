# 决策：向插件暴露 coreVersion（D9 重开）

Status: accepted
Date: 2026-10-25
Updated: 2026-10-26 — dev 构建的 `coreVersion` 改为从最近可达 git tag 派生（无 tag 仍回落 `1.0.0`），见 `2026-10-26-dev-core-version-from-git.md`；本文其余结论不变。
Supersedes: `2026-10-24-admin-page-host-action.md` D9（「暂不做 hostVersion」）
Related: `2026-10-24-app-store-out-of-tree.md`

## 背景

应用商城的目录需要按每个 release 的 `sforumVersion` 约束过滤，而 SForum 侧一直
没有把「运行中的 core 是哪个版本」暴露给插件。上一轮以 D9 明确暂缓，商城因此
一直按 `compatibility=unknown` 不过滤，也不转发任何版本参数。

本轮把这条面打开，并同时补上宿主自己的兜底：`manifest.sforumVersion` 此前
**只校验语法**（`v3_validate.go` 仅 `semver.NewConstraint`），从未与运行中的
core 比较。也就是说 D9 当时写的「core 在安装时兜底拒绝」并不存在。

## 决策

### 1. 一个权威，两个字段

`apps/api/version` 新增 `HostIdentity` 与 `Identity()`：

| 字段 | 来源 | 语义 |
| --- | --- | --- |
| `coreVersion` | `CoreCompatibilityVersion()` | **严格 semver**，兼容判定唯一权威。dev 构建为 `1.0.0` |
| `buildVersion` | `Get().Version` | 展示与排障（`dev-abc12`）。**永不参与约束求值** |
| `valid` | 严格 semver 解析 | `coreVersion` 能否用于求值 |

`CoreCompatibilityVersion()` 与 `Get()` 保持原样；`Identity()` 只做组合，不引入
第二个版本来源。

### 2. 按受众裁剪的暴露矩阵

| 消费方 | 面 | 字段 |
| --- | --- | --- |
| v2 后端插件 | `HandshakeRequest.host_build` → `Server.HostBuild()` | `coreVersion` + `buildVersion` |
| 管理页 / 设置页组件 | `AdminPageBridgeV1.host` / `AdminMicroFrontendBridgeV1.host` | `coreVersion` + `buildVersion` + `valid` |
| 公开 L2 组件 | `PublicFrontendBridgeV1.host` | 仅 `coreVersion` |
| 运维 / 探针 | `GET /api/v1/health` | 仅 `coreVersion` |
| 管理员 | 总览 runtime card（既有） | 全量 `BuildInfo`（不变） |

公开面只给 release 级 `coreVersion`：commit、`builtAt`、dirty、dev 标签都不出现。
发版号本来就在 GitHub 公开，额外泄露的边际信息只有「这台实例的确切补丁级别」，
换来的收益是运维探针与公开组件无需猜版本。

### 3. 后端读取面只能是 protocol v2

商城伴生计划 Q6 原本写「`host.api` Ping 加两个字段」。该假设在本仓不成立：
`HostAPIRegistrar.RegisterExtension`（唯一会注入 `SFORUM_HOST_API_URL/TOKEN`
的地方）在生产路径**没有调用者**，v2 插件走 gRPC broker + `runtime_token`，
根本拿不到 v1 的 HTTP 环境变量。因此本轮：

- 握手请求携带 `host_build`，SDK 在 `Server.HostBuild()` 暴露，`ok=false` 表示
  「旧宿主没给」；
- 重复握手携带不同身份按 `protocol.stale_runtime` 拒绝，与 token/identity 同款
  不变量（版本在一个进程生命周期里不可能变）；
- **不给 v1 `Ping.data` 加字段**：那条面今天对插件不可达，加了就是死代码。

### 4. 兜底范围与豁免

`ensureCoreCompatible` 在两类入口求值：

- 安装（`InstallOrUpgradeArchive`，只暂存）→ 422 `extension.core_incompatible`；
- 让制品跑起来的所有入口（启用、主题激活、升级、回滚、重启）→ 409，同一个
  reason 码，带 `required` / `coreVersion` / `buildVersion` 结构化字段。

**内置包豁免**（`source == builtin`）。内置包与 core 是同一个发行物，约束写错时
运维没有任何修复手段；把默认主题（当前声明 `^1.0.0`）一起判死只会让发行版在
自己的版本上装不上。默认主题的约束订正留到它下一次版本 bump，避免为了纯文案
改 digest、触发一次不必要的重新激活。

**`coreVersion` 非法时跳过兜底并告警**（启动日志 WARN）。构建标签写成 `v3.0.16`
是打包缺陷，不是安全边界；fail closed 会让一次标签失误把全部安装打死。

**预发布 core 按发行基线再求值一次**：Masterminds 默认不匹配预发布版本，
`3.0.13-alpha.1` 连 `>=0.1.0` 都会被判 false。发行版里 alpha/beta 是常态，
若照字面拒绝，每次预发布都会让所有第三方包无法安装。规则固定为「精确值或
同发行基线（`3.0.13`）任一满足即可」，不会放宽到别的版本线。

### 5. 商城侧重开条件

`sforum-app-store` 的 `plugin/plugins/sforum.store/backend/upstream.go` 只有在
同时满足下面两条之后，才允许把 `sforum`（= `coreVersion`）与 `sforumBuild`
（= `buildVersion`）转发给目录查询：

1. SForum 发布版包含本节版本面（v2 握手 `host_build`）；
2. 商城插件的 `go.mod` 指向的 SForum SDK 已带上该字段。

此前插件**不得自行推断或硬编码宿主版本**，目录保持 `compatibility=unknown`、
不过滤、不展示「兼容」徽标。core 在安装/启用时的拒绝仍是最终裁判。

若将来恢复 Host API v1 的 `SFORUM_HOST_API_*` 注入，`Ping.data` 必须补同样的
两个字段，并同步本记录——不允许出现两条语义不同的版本面。

## 代价与已知限制

- 公开 `/api/v1/health` 从「无版本」变成「有 release 级版本」，多了一点指纹面；
  接受，因为 release 号本就公开，且不带 commit。
- core 升级后**不会**自动禁用已启用的不兼容扩展，也不在管理列表里加兼容徽标。
  那需要一条独立的生命周期政策（自动禁用会在无人值守时停掉论坛功能），另行决策。
- 每次启用/重启都会调用一次 `version.Identity()`（`debug.ReadBuildInfo`），
  频率低、无 IO，不引入缓存。

## 重开条件

- 需要「随时重读、而非只在握手时冻结」的版本面（例如插件热更新后想重新判定）：
  单开 `hostv2.PlatformInfoService.Get`，不要往 `ResponseContext` 里塞字段。
- 需要管理列表展示兼容状态：先定自动禁用政策，再改列表契约。
- 需要公开面提供完整构建信息：必须先论证指纹风险可接受，并区分管理/公开两个面。
