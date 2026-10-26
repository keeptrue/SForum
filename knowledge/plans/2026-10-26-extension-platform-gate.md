# 插件/主题架构兼容：商城预判 + Core 硬闸门

Status: **landed（2026-10-26）** —— 见 `../sessions/2026-10-26-extension-platform-gate-handoff.md`。
残留：运行阶段 409 与旧服务端回落无真机证据；Core 平台文案在用户 dev server 上显示 raw
i18n key（未重启其进程）；全仓 `test.sh --fast` 两条红属另外两批未提交改动。
Repos: `SForum`（Core）+ `sforum-app-store`（商城服务端 + `sforum.store` 插件）
Related:
`decisions/2026-10-25-core-version-surface.md`、
`sforum-app-store/knowledge/decisions/2026-10-26-install-gate.md`、
`sforum-app-store/knowledge/decisions/2026-10-26-catalog-card-dialog.md`

## 0. 工作区现状（开工前必读）

两个仓库都有**未提交**的改动，必须在其之上叠加，**不得回退**：

- `SForum`：55 个已修改文件（coreVersion/dev tag、admin modules、布局、测试等）。
  本计划要改的 `Models/Extensions/lifecycle.go`、`types.go`、
  `Http/Controllers/Extensions/controller.go`、`Support/Localization/messages.go`、
  `apps/web/app/utils/admin/extensions/hostActions.ts`、两份 locale、`contracts/openapi/paths/extensions.yaml`
  **均未被修改**，可以直接改。
- `sforum-app-store`：今日「更新说明 / 作者信息 / 安装门禁」批次尚未提交，已改
  `catalog.ts`（973 行）、`logic.ts`（915）、`view.ts`（592）、`copy.ts`（371）、
  `types.ts`（373）、`mapping.go`（601）、`handlers.go`（319）、两份 response schema、
  `contracts/store-api.yaml`、`service/src/lib/catalog/query.ts`、`dist/*`。
  **本计划要在这些改动之上继续**，禁止 `git checkout/stash/reset` 或任何形式的回退。
  新增的 `install-gate.test.ts`、`dialog.ts`、`decisions/2026-10-26-install-gate.md` 是同一批工作的产物。

## 1. 现状（已逐行核实）

1. 商城目录**已按架构过滤**：插件后端把 `runtime.GOOS/GOARCH` 作为 `os`/`arch` 发给商城
   （`plugin/.../backend/upstream.go:202-219`），商城在
   `service/src/lib/catalog/query.ts` 的 `buildSummary` 里丢掉「没有任何版本支持该平台」的产品。
   完全不支持的产品今天不会出现在列表里。
2. 详情弹窗返回**全部版本**（`loadProductDetail` → `buildReleases(versions, null)`），
   版本下拉能看到所有平台。
3. 选到不支持的版本时平台会**静默回退**：`logic.ts` 的 `defaultPlatform()` 在找不到精确匹配时
   `return wildcard ?? release.platforms[0]`，会把异架构包交给宿主，安装按钮照常可点。
4. Core 只在**进程启动**时发现架构不对：`Support/PluginBootstrap/diagnostics.go` 能识别
   `exec format error`，但那是「启用之后」，安装阶段完全放行。

## 2. 目标与验收

目标：安装前就能看出「这个包能不能在你这台机器上跑」，Core 永远不接受错架构制品。

验收：
1. 列表默认行为不变（只看支持当前架构）；关掉新开关后不支持的产品出现，卡片有
   「不支持当前架构 / 最新版不支持当前架构」徽标。
2. 弹窗：最新版不支持、旧版支持时自动选最新的可安装版本并说明；选到不支持版本时安装按钮禁用并说明；
   平台下拉只列「当前架构 + any」。
3. Core 安装错架构包 → `422 extension.platform_incompatible`（字段 `required`/`host`/`entry`）；
   启用/激活/升级/回滚 → `409` 同 reason。
4. 宿主原生包、主题、脚本型后端照旧。
5. 两仓 typecheck / 单测 / 构建 / 架构边界门禁全绿。

## 3. 决策

| # | 决策 | 结论 |
| --- | --- | --- |
| D1 | 范围 | 两仓都改，Core 先落地 |
| D2 | 列表 | 加「只看支持当前架构」开关，默认开（=现状）；关掉走商城 `platform=all` |
| D3 | 弹窗 | 自动选「最新且支持当前架构」的版本 + 顶部说明 |
| D4 | Core 严格度 | 安装拒绝 422 + 运行阶段拒绝 409 |
| D5 | 判定依据 | 读包内可执行文件头（Go buildinfo 优先，ELF/Mach-O/PE 兜底），不要求清单声明 |
| D6 | 平台下拉 | 只列「当前架构」和 `any/any`；没有则隐藏并禁用安装 |
| D7 | 卡片形态 | 卡片是整卡按钮（点击开详情），**不加独立安装按钮**，只加徽标 |
| D8 | 未知即放行 | 探测不出目标平台 → 不拦截，交给 Core 原有启动诊断 |
| D9 | 不做 | 不改清单 schema、不加 `backend.platforms`、不改公开商城站点、不自动停用已启用扩展 |

## 4. 方案总览

```
商城目录(service) ──platform=all?──▶ 插件后端(Go，知道宿主 GOOS/GOARCH)
        │                                   │
        └── latestPlatforms ───────────────▶ 管理页：卡片徽标 + installGate 新原因 + 默认选版本
                                            │
                                     bridge.installExtension
                                            ▼
                              Core：装包时读 backend 可执行头 → 不匹配 422
                                    启用/激活/升级/回滚 → 409
```

商城负责「让你看见并选择」，Core 负责「最终裁判」。

## 5. Core（SForum）

### 5.1 探测（新文件，无平铺上限）

`apps/api/app/Support/ExtensionPackage/platform.go`：

```go
type Target struct{ OS, Arch string }
func (t Target) String() string              // "linux/amd64"
func DetectTarget(r io.ReaderAt) (Target, bool)
```

顺序：`debug/buildinfo.Read` → `debug/elf`（unix 家族）→ `debug/macho`（含 fat，任一 arch 匹配）
→ `debug/pe`；都不识别返回 `false`。

### 5.2 安装闸门

`Models/Extensions/lifecycle.go` 的 `InstallOrUpgradeArchive`：`ensureCoreCompatible` 之后、
类型权限 switch 之前。`ReadArchive` 已把文件读进内存（`extensionpackage.File.Body`），
**落盘前拒绝**。仅 `plugin && Backend.Entry != ""` 生效，主题跳过。

### 5.3 运行闸门

`requireEnablePreflight`（`lifecycle.go:736`，启用/主题激活/升级/回滚/重启共用）在
`requireCoreCompatibleRun` 后追加 `requireExecutablePlatform(manifest, packagePath)`。
实现放 `Models/Extensions/artifact_retention.go`（现 122 行，已持有 `requireArtifactAvailable`），
打开文件只读头。内置包不豁免。

### 5.4 错误码/映射/本地化/契约

- `types.go`：`CodePlatformIncompatible`、`ErrExecutablePlatformMismatch`、
  `ExecutablePlatformMismatchError{Entry, Required, Host, Stage}`。
- **新建** `Controllers/Extensions/errors_compatibility.go`：把 `mapCoreIncompatibleError`
  从 `controller.go`（999 行）搬出，新增 `mapExecutablePlatformError`；安装 422、运行 409，
  `data.fields={required,host,entry}`。`controller.go` 只留一行调用（→ 约 986）。
- `Support/Localization/messages.go`：zh/en 各一条。
- `contracts/openapi/paths/extensions.yaml`：`install-from-url` 422 补 reason；
  `enable/activate/upgrade/rollback/restart` 409 补 reason 与字段。

### 5.5 前端提示

- `apps/web/app/utils/admin/extensions/hostActions.ts`：`INSTALL_ERROR_KEYS` 加
  `'extension.platform_incompatible': 'platformIncompatible'`；`adminExtensionInstallErrorMessage`
  用 `apiErrorFields` 取 `required`/`host` 插值。
- 两份 locale 的 `admin.extensionInstall.errors` 加：
  `"这个扩展包的后端是为 {required} 构建的，当前系统是 {host}，已停止安装。"`

### 5.6 文件预算

`lifecycle.go` 956 → ~966；`types.go` 753 → ~783；`artifact_retention.go` 122 → ~158；
`controller.go` 999 → ~986（先搬后加）。

## 6. 商城服务端（`sforum-app-store/service`）

1. `contracts/store-api.yaml`：
   - `ProductSummary` 增加必填 `latestPlatforms: Platform[]`（卡片指向的那个版本）；
     `platforms` 保持「所有候选版本并集」。
   - 目录查询新增 `platform: strict|all`（默认 `strict`）。
2. `src/lib/catalog/query.ts`：`buildSummary` 用 `latestRow` 算 `latestPlatforms`；
   平台过滤只在 `strict` 生效；`queryFingerprint` 加 `platform`；类型补字段。
3. `bun run contract:generate` 重新生成 `src/lib/api-types.ts`。
4. 测试：`query.test.ts` 补「latestPlatforms 取最新版」「`platform=all` 保留不支持产品」
   「strict 行为不变」。

> 为什么不「不传 os/arch」：`compatibility` 判定也要用 os/arch；新增只放宽列表的 `platform`
> 维度语义更干净。

## 7. 商城插件（`plugin/plugins/sforum.store`）

### 7.1 页面契约与 schema（`additionalProperties: false`，必须同步）

`schemas/catalog.response.json`、`schemas/product.response.json` 的 item/product 增加
`latestPlatforms`（`{os,arch}` 数组，可为空）。

### 7.2 后端

- `handlers.go`：`catalogQueryFromRequest` 解析 `platform`（`all`/`strict`，非法 400）。
- `upstream.go`：`catalogQuery.PlatformAll`；`values()` 在 `PlatformAll` 时传 `platform=all`。
- `mapping.go`：`catalogItemValues`/`productResponseValues` 输出 `latestPlatforms`；
  上游缺字段时回落 `platforms`。
- 测试：参数转发与字段映射。

### 7.3 管理页

- **新建** `frontend/admin/src/platform.ts`（纯函数 + `platform.test.ts`）：
  `supportsTarget(platforms, target): boolean | null`（`any/any` 通配；`target` 缺失为 null）、
  `releaseSupportsTarget(release, target)`、`preferredRelease(releases, target)`。
- `logic.ts`：`defaultPlatform` **去掉 `?? release.platforms[0]` 回退**；
  `installGate(view, release, target)` 新增 `platformUnsupported` 原因（**平台先于版本判定**）；
  `installGateText` 增加该原因；`toCatalogItemView`/`toProductView`/`toReleaseView` 接收
  `target` 并输出 `supported`。
- `types.ts`：`CatalogItemDto.latestPlatforms?`、`CatalogItemView.supported`、
  `ReleaseView.supported`、`InstallGate.reason` 加 `"platformUnsupported"`。
- `catalog.ts`（现 973 行，**必须先减行**）：
  - 把 `renderFoot`（约 60 行）抽到新 `foot.ts`（→ 约 910）；
  - 状态加 `platformOnly`（默认 true）与 `target`；工具条加「只看支持当前架构」开关，
    关闭时请求带 `platform=all`；
  - 详情加载用 `preferredRelease`；最新版不支持且选了旧版时在通知槽说明；
  - `foot.ts`：版本选项带「不支持当前架构」后缀、平台下拉只留宿主可选、安装按钮禁用 + 原因。
- `view.ts`：卡片徽标（`warn` 已存在）：最新版不支持但旧版支持 →「最新版不支持当前架构」；
  全部不支持 →「不支持当前架构」。
- `copy.ts`：zh/en 新增 `platformFilterAction`、`platformUnsupportedBadge`、
  `platformLatestUnsupportedBadge`、`platformUnsupportedCardTitle`、
  `detailPlatformUnsupported`、`detailNoSupportedRelease`、`installBlockedPlatform`、
  `versionOptionUnsupported`。

### 7.4 构建

`bash plugin/scripts/build.sh`：Go 构建 → 管理页 `bun build` → `sync-digests.mjs` → 清单校验。
改了 schema 与 dist 之后必须跑。

## 8. 兼容矩阵

| Core | 插件 | 服务 | 结果 |
| --- | --- | --- | --- |
| 新 | 新 | 新 | 全功能 |
| 新 | 旧 | 旧 | 旧 UI 可能递错包，Core 422 拦住 |
| 旧 | 新 | 新 | UI 正确禁用；绕过只在进程启动时失败（现状） |
| 任意 | 新 | 旧（无 `latestPlatforms`） | 回落 `platforms` 并集；不误拦 |

## 9. 测试与验收

**Core（Go）**：`platform_test.go`（`os.Executable()` 真实二进制 + 合成 ELF/Mach-O/fat/PE +
脚本/空）；安装拒绝且未落盘；运行阶段拒绝；Controller 422/409 + fields；内置插件回归。

**商城**：service `latestPlatforms`/`platform=all`/ETag；插件后端参数与映射；插件前端
`platform.test.ts` + `install-gate.test.ts`（新原因）+ `catalog.test.ts`（徽标/默认版本/禁用）；
`bun run typecheck`、`bun run test`、`contract:generate` 无 diff、`build.sh` 通过。

**门禁**：`go build ./... && go test ./...`；`node tests/validate-architecture-boundaries.mjs`；
`ruby scripts/validate-openapi-refs.rb`；`cd apps/web && bun run typecheck && bun test`；
`./scripts/test.sh --fast`。

**浏览器 QA**：1633×810 与 390×844；用商城 fixture 造「仅 darwin/arm64」产品在 linux/amd64 宿主验证
strict/all、徽标、禁用。跑不起商城服务时退化为 DOM harness 并记录。

## 10. 分工

| 阶段 | 内容 | 负责人 |
| --- | --- | --- |
| 1 | Core Go（探测/闸门/映射/契约/本地化） | 老后 |
| 2 | Core 前端错误映射与文案 | 小前 |
| 3 | 商城服务端契约 + query + 生成类型 | 老后 |
| 4 | 商城插件后端 + schema | 老后 |
| 5 | 商城管理页（platform.ts / logic / view / foot / copy） | 小前 |
| 6 | 全量测试、构建、摘要、浏览器 QA | 小审 |
| 7 | 决策记录、模块文档、热交接、最终门禁 | Lead + 小审 |

## 11. 风险与不做

- 探测是启发式：非 Go、非标准格式判「未知」放行（D8）。
- `sforum.store` 自己也是带 Go 后端的插件，必须用匹配平台的包安装；本闸门同样作用于它。
- `platform=all` 必须进 ETag 指纹，否则缓存串页。
- 改 schema/dist 后不跑 `build.sh`，摘要不符会拒绝启用。
- 不做：清单 schema 变更、Core 列表平台徽标持久化、自动停用、公开商城站点、卡片级安装按钮。
