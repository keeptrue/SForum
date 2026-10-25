# 应用商城 · SForum 侧：清算预留代码 + 管理页安装动作

Status: **landed（2026-10-24）** — P0 = `381182cc7`
`chore(extensions): remove dormant marketplace scaffolding`；P1 = 管理页安装动作
（清单 `hostActions` + bridge + 确认框 + 期望校验 + 120s + 单飞 + 披露）。
证据与残留见 `../sessions/2026-10-24-app-store-core-handoff.md`。
Started: 2026-10-24
Companion:
`2026-10-24-app-store-service.md`（服务端 + 商城插件，独立仓库 `sforum-app-store`）
Decision records（批准实施时创建）:
`decisions/2026-10-24-app-store-out-of-tree.md`、
`decisions/2026-10-24-admin-page-host-action.md`

**本轮范围**：只动 `/home/inkedus/Code/github/SForum`。不创建
`sforum-app-store`、不写商城插件、不写服务端；companion 计划只用于共享契约参考。

## 1. 为什么拆成两份计划

应用商城实际上是两个独立产品：

- **SForum 宿主侧**（本仓）：删除商城预留代码；给受信任的管理页一个**宿主中介的安装动作**，让任何目录型插件都能复用。
- **`sforum-app-store`**（新仓）：Next.js 服务端 + SForum 商城插件 `sforum.store`。

两份计划共享一个**唯一契约**：管理页 bridge 的 `installExtension` 语义与 `install-from-url` 的期望字段（本文件 §6.2）。服务端与插件不得依赖本仓其它内部实现。

## 2. 已核实的事实（决定了方案形状）

1. `apps/api/app/Support/Marketplace/`（8 文件 1492 行）在 `bootstrap/p12_ops_services.go` 构造，**没有任何 HTTP/CLI 消费者**；`knowledge/modules/extensions.md` 自述 “no supported product consumer”。这是可安全删除的死代码。
2. `POST /admin/extensions/install-from-url` 已存在且完整：public HTTPS、可选 `sha256` 钉住、SSRF 安全下载器（重定向复检、50 MiB、90 s）、惰性安装。前台 `SFAdminExtensionInstallPanel` 已支持 URL + checksum。
3. 前端 `useApiClient` 对 POST 的默认超时是 **30 秒**（`apps/web/app/composables/useApiClient.ts:33`）；服务端 `HTTP_WRITE_TIMEOUT` 默认 20 秒，但 fasthttp 是在 handler 返回**之后**才设置写 deadline（`server.go` 的 `s.Handler(ctx)` 之后才 `SetWriteDeadline`），所以它**不限制 handler 执行时间**。真正会掐掉同步安装的是前端 30 秒默认值，必须对这一次调用显式放宽到 ~120 秒。
4. 插件管理页是**同源、挂在宿主 DOM 里的受信任 ES 模块**，不是 iframe；它今天就可以用管理员 Cookie 直接 `fetch` 管理 API。`bridge.request` 的命名空间是 ABI 约定，不是沙箱。
5. `InstallOrUpgradeArchive` 的最终授权在服务层：`canManagePlugins/Themes(actor)`，未信任的可执行后端还要求 `super_admin`。任何前置校验都只是闸门，不能替代它。
6. 管理页清单类型是 `ManifestAdminPage`（`apps/api/app/Support/ExtensionManifest/manifest.go:171`），`view` 的校验在 `admin_micro_frontend.go:106`；`ArchiveInput` 在 `Models/Extensions/types.go:651`，来源审计在 `lifecycle.go:205` 的 `mergeArchiveSourceMetadata`。

## 3. 目标与非目标

### 目标

- 删除 SForum 中所有商城专属代码、配置、部署校验、前端页面、生成物与文档表述。
- 给受信任管理页一个**宿主中介的安装动作**：清单声明 `hostActions`、bridge 暴露 `installExtension`、宿主弹确认框、复用现有 `install-from-url` 的下载/摘要/惰性安装路径。
- 保持现有信任模型：**只下载并暂存**，永不启用、永不信任、永不激活。
- 让任何目录型插件（官方商城、私有目录、企业内部分发）都能复用该动作。

### 非目标

- 不做目录、不做商城 UI、不做签名索引、不做 GitHub 集成。
- 不创建 `sforum-app-store` 仓库、不写 `sforum.store` 插件、不写 Next.js 服务端。
- 不删除 `install-from-url`（它是通用原语，与商城无关）。
- 不新增 `extensions.install` capability、Host Command、意图表、worker（见 §4 D8 与 §6.9 的升级路径）。
- 不新增 `hostVersion` 到 bridge（见 §4 D9；该条随后被
  `knowledge/decisions/2026-10-25-core-version-surface.md` supersede）。
- 不改扩展信任/启用/升级/回滚语义。

## 4. 关键决策

- **D1 删除而不是保留**：Marketplace 包无消费者，保留只会让后续会话误以为 core 有商城能力。整包删除，含测试。
- **D2 保留 `install-from-url`**：它是“精确 URL + 精确摘要”的通用安装入口，商城只是它的调用者之一。
- **D3 页面级 host action，不新增进程能力**：安装是管理员在 UI 上的一次动作，不是插件后台的自动化能力。插件管理页本来就是同源受信任代码；bridge 带来的是**类型化 ABI + 宿主确认框 + 错误映射**，不是新权限。避免新增 `extensions.install` 与命令引擎那一层。
- **D4 复用现有同步路由**：只有一条安装路径，避免两套语义漂移；给路由增加可选的 `expectedExtensionId/expectedVersion/expectedType`，由服务层在解析后校验。bridge 对这一次 POST 显式传 ~120 秒超时。
- **D5 只暂存**：只调用 `InstallOrUpgradeArchive`，不调用 enable/trust/activate/upgrade。启用/信任仍由管理员在 core 页面完成。
- **D6 人工确认，但不假装是沙箱**：宿主确认框的“安装”按钮是真实点击，安装只发生在这一步；宿主同一时间只开一个确认框（单飞）。插件仍可绕过 bridge 直接 fetch，真正的闸门是启用信任 + 未信任可执行插件的 `super_admin` + 摘要钉住。
- **D7 身份绑定**：插件声明期望的扩展 ID/版本/类型；宿主下载解析后不一致就**中止**，不是只警告。
- **D8 v1 不做异步**：没有意图表、没有 worker、没有状态命令。管理员离开页面会取消请求，结果丢失；页面必须明确写“安装期间不要关闭页面”。bridge 的 promise 语义固定为“resolve 时已暂存或失败”，将来换成“意图 + worker”时插件页面无需改动（§6.9）。
- **D9 暂不做 `hostVersion`**：v1 由商城目录展示每个 release 的 `sforumVersion` 约束，core 在安装时兜底拒绝。确需“兼容”徽标时再单开。
  **2026-10-25 已 supersede**：另行落地了 `coreVersion`/`buildVersion` 暴露与
  清单兜底，见 `knowledge/decisions/2026-10-25-core-version-surface.md`；商城
  `upstream.go` 的重开条件写在该记录第 5 节。
- **D10 清单披露**：`admin.pages[].hostActions: ["extension.install"]` 只允许出现在 `view: component` 的管理页；插件详情/信任页显示“此插件可请求安装扩展包”。

## 5. P0：清算预留代码

P0 是纯删除，可与 P1 分开提交、分开发布。

### 5.1 删除

| 对象 | 动作 |
| --- | --- |
| `apps/api/app/Support/Marketplace/**` | 整包删除（含 `service_test.go`） |
| `apps/web/app/pages/admin/extensions/store/**` | 3 个页面删除 |
| `apps/web/app/components/admin/SFAdminExtensionStoreShelf.vue` | 删除（432 行硬编码占位目录） |

### 5.2 编辑

| 对象 | 动作 |
| --- | --- |
| `apps/api/bootstrap/p12_ops_services.go` | 删 marketplace import、`productionP12Ops.Marketplace/Installer`、`newProductionMarketplace`、`newHostMarketplaceInstaller`、`isProdLike`；保留 Rollout/SystemTier/Privacy；清理失效 import |
| `apps/api/config/config.go` | 删 `MarketplaceEd25519PublicKeyHex`、`MarketplaceEd25519KeyID` 与两处 `env(...)` |
| `apps/api/bootstrap/api_assembly_extensions.go` | 删除注释中的 Marketplace 措辞 |
| `apps/api/cmd/sforum/package_extension.go` | 删除 “marketplace provenance” 注释 |
| `apps/web/app/config/adminModules.ts` | 删 `/extensions/store`、`/extensions/store/themes`、`/extensions/store/plugins` 三条 page 定义 + 侧栏一级文件夹 |
| `apps/web/i18n/locales/zh-CN.json`、`en-US.json` | 删 `admin.nav.extensionStore*` 与整个 `admin.extensions.store` 块 |
| `tests/validate-admin-framework.ts` | 移除 store 专属页面/侧栏/货架断言，保留框架校验 |
| `.env.example`、`.env.production.example` | 删 `MARKETPLACE_ED25519_*` |
| `compose.yaml`、`compose.prod.yaml` | 删 `MARKETPLACE_ED25519_*` 透传 |
| `deploy.sh` | 删 marketplace key 读取与格式校验 |
| `deploy/scripts/configure-production.sh`、`configure-production_test.sh` | 删 key 生成、写入与断言，调整相关行数/键集合断言 |
| `scripts/ci/release-smoke.sh` | 删两个 `MARKETPLACE_*` export |
| `knowledge/runbooks/2026-10-16-release-image-stack-verification.md` | 删 marketplace key 两处 |
| `docs/zh-CN/roadmap.md`、`docs/en-US/roadmap.md` | 更新 Marketplace 表述 |
| `knowledge/modules/extensions.md` | 更新 Marketplace / App Store 的历史表述 |
| `knowledge/decisions/2026-07-22-marketplace-ed25519-signing.md` | 标记 superseded，指向新决策 |
| `AGENTS.md` | “plugin-first” 列表加入应用商城；写明 core 只保留安装原语 |

### 5.3 重新生成

```bash
node scripts/v3-catalog/generate.mjs
node scripts/v3-catalog/generate.mjs --check
```

影响：`apps/web/app/config/adminSurfaceCatalog.gen.ts`、`docs/extensions/v3/catalogs/ui-surfaces.json`、`docs/extensions/v3/catalogs/admin-surfaces.md`、必要的 traceability 行。

### 5.4 验证

```bash
cd apps/api && go build ./... && go test ./...
cd apps/web && bun run typecheck && bun test
node tests/validate-architecture-boundaries.mjs
node tests/validate-v3-p0-catalogs.mjs
ruby scripts/validate-openapi-refs.rb
./scripts/test.sh --fast
rg -n -i "marketplace" apps/api apps/web --glob '!**/*_test.go'   # 期望 0
rg -n "MARKETPLACE_ED25519" . --glob '!knowledge/**' --glob '!docs/**'  # 期望 0
```

### 5.5 回滚

P0 无数据库迁移；整包删除 + 配置删除可用一次 `git revert` 完整回滚。

## 6. P1：管理页安装动作

### 6.1 清单：`admin.pages[].hostActions`

- Go：`ManifestAdminPage` 增加 `HostActions []string \`json:"hostActions,omitempty"\``。
- Schema：`manifest-v3.schema.json` 的 `$defs/adminPages/items/properties` 增加 `hostActions`（array of string，unique，max 8）。
- 校验（`admin_micro_frontend.go`）：
  - 只允许 `view == "component"`；
  - 只允许已知动作 ID：首版仅 `extension.install`；
  - 去重、trim；未知动作直接拒绝清单。
- 披露：**插件详情页 + 启用确认弹窗都展示**“此插件可请求安装扩展包”；单独一条提示，不并入 capability 风险等级（它不是进程能力）。落点：`apps/web/app/components/admin/SFAdminExtensionEnableDialog.vue`（启用确认）+ `apps/web/app/pages/admin/extensions/plugins.vue` / `themes.vue` 的详情区域；两处共用同一个 i18n key。
- 文档：`docs/extensions/trusted-admin-components.md`、`docs/extensions/catalogs/manifest-v3.md`。

### 6.2 Bridge 契约（对商城插件稳定的唯一面）

`@sforum/admin-sdk` 的 `AdminPageBridgeV1` 增加**可选**方法（插件必须 feature-detect）：

```ts
type AdminHostInstallRequest = {
  url: string                 // public HTTPS
  sha256: string              // 64 lowercase hex
  expectedExtensionId: string // required: binds the package identity
  expectedType: 'plugin' | 'theme' // required: selects the permission gate
  expectedVersion?: string
}

type AdminHostInstallResult = {
  extensionId: string
  version: string
  type: 'plugin' | 'theme'
  packageDigest: string
  upgraded: boolean
  requiresTrust: boolean       // 未信任可执行后端时为 true
}

bridge.installExtension?: (request: AdminHostInstallRequest) => Promise<AdminHostInstallResult>
```

语义：

- 只有当前页在清单里声明了 `extension.install` 时，宿主才注入该方法；否则为 `undefined`。
- 宿主先做权限预检（`expectedType` 对应的 `extension.plugin.manage` / `extension.theme.manage`）；不满足直接 reject `install_permission_denied`，不弹确认框。
- 宿主再弹确认框；用户确认后才发请求；取消则 reject 一个稳定的 `install_cancelled`。
- 确认框内容：URL host + 完整 URL（可展开）、SHA-256、插件声明的 ID/版本/类型、所需权限、`super_admin` 提示；并明确写“宿主将在下载后校验，不一致即中止”。
- 宿主不要求调用瞬间处于用户手势（页面可能先 await 后端取签名地址）；真正的确认是确认框里的“安装”按钮。宿主对同一页面同一时间只允许一个确认框/安装（`install_in_progress`）。
- 宿主内部对这一次 POST 传 `timeout: 120000`；不要吃 `useApiClient` 的 30 秒默认值。

### 6.3 宿主确认框与页面状态

- 组件位置：`apps/web/app/components/extensions/admin/`（例如 `SFAdminPageInstallDialog.vue`），由 `SFTrustedAdminPageComponent.vue` 承载。
- 状态机：`idle → confirming → installing → success | error | cancelled`。
- `installing` 期间：显示 spinner + “安装期间不要关闭页面；关闭会中止本次安装”。
- 成功：显示扩展 ID/版本/摘要 + “去插件/主题页完成信任与启用”按钮（`bridge.navigate('/extensions/plugins')` 或 `/extensions/themes`）。
- 失败：按稳定错误码映射文案；保留“重试”按钮。
- 窄屏（390×844）下确认框全宽、按钮不溢出；桌面 1633×810 下居中。

### 6.4 路由与服务层改动

**`ArchiveInput`**（`apps/api/app/Models/Extensions/types.go`）增加可选字段：

```go
ExpectedExtensionID string
ExpectedVersion     string
ExpectedType        string // plugin|theme
SourceLabel         string // audit-only, e.g. admin-page:sforum.store
```

**`InstallOrUpgradeArchive`**（`Models/Extensions/lifecycle.go`）：

- `ReadArchive` 与 `validateManifest` 成功后、按 `manifest.Type` 做权限 switch 之前，校验期望值；不一致返回新的稳定错误 `ErrArchiveExpectationMismatch`（带实际 ID/版本/类型，便于 UI 显示）。这样“类型不符”报的是期望不匹配，而不是权限错误。
- `mergeArchiveSourceMetadata` 增加 `sourceLabel`（清洗为 `[a-z0-9._:-]`、≤64 字符）。

**`POST /admin/extensions/install-from-url`**（`Controllers/Extensions/remote_install.go`）：

- 输入增加 `expectedExtensionId/expectedVersion/expectedType/source`（路由层可选，兼容手工 URL 安装；bridge 会强制 `expectedExtensionId` 与 `expectedType`）；
- 校验：expected type ∈ {plugin,theme}、expected ID 符合清单 ID 规则、source 长度/字符；
- 透传到 `ArchiveInput`；
- 映射 `ErrArchiveExpectationMismatch` 为 `extension.expected_mismatch`（422）；
- OpenAPI 同步（`contracts/openapi/paths/extensions.yaml` 与 schemas）。

**单飞/限流**：

- **v1 只做宿主侧单飞**（一个页面同时只允许一个确认框/安装），不加服务端计数。
- **注意**：`assetPublicationMu` 是进程内锁，不是跨节点锁；多节点或插件绕过 bridge 并发安装同一扩展的串行行为**本次未验证**，首版不承诺跨节点串行，也不新增数据库锁。若后续需要，随“意图 + worker”一起用 advisory lock 或队列解决。
- 不对全局 `HTTP_WRITE_TIMEOUT` 做改动。

### 6.5 前端接线

- `apps/web/app/composables/admin/useAdminExtensionInstall.ts`：`installFromUrl` 增加可选 `expectations` 与 `timeout`；**bridge 与现有安装面板共用这个 transport、错误映射与结果展示**。
- `apps/web/app/components/extensions/admin/SFTrustedAdminPageComponent.vue`：按清单声明注入 `installExtension`；实现确认框、单飞、错误映射。确认框只属于 bridge；现有面板流程保持不变（面板的“安装”按钮本身就是确认）。
- `apps/web/app/runtime/admin-extensions/types.ts` 与 `apps/web/packages/admin-sdk/src/index.ts`：补类型；SDK 做 minor bump。
- `apps/web/app/utils/admin/adminExtensions.ts`：`AdminExtensionAdminPage` 增加 `hostActions?: string[]`，披露 UI 与 bridge 判断共用同一字段。
- 信任披露：**插件详情页 + 启用确认弹窗**都展示 `hostActions`（单独一条提示，不并入 capability 风险等级）。
- i18n：`zh-CN` / `en-US` 补确认框、进度、错误、成功文案。

### 6.6 失败与取消语义

| 场景 | 行为 |
| --- | --- |
| 无对应 manage 权限 | 权限预检失败；reject `install_permission_denied`；不弹框 |
| 用户取消确认框 | 不发请求；reject `install_cancelled` |
| 已有确认框/安装在跑 | 拒绝 `install_in_progress` |
| 下载失败/超时 | 路由返回 `extension.remote_fetch_failed`；页面可重试 |
| 摘要不符 | `extension.remote_digest_mismatch`；不安装 |
| 期望 ID/版本/类型不符 | `extension.expected_mismatch`；不安装 |
| 管理员离开页面 | 浏览器断开 → Go context 取消 → 安装中止；页面重进后需重试 |
| 非 super_admin 装可执行插件 | 服务层拒绝；确认框提前提示 |

### 6.7 测试

- **Go**：
  - 清单校验：`hostActions` 只允许 component 页、未知动作拒绝、去重；
  - `ArchiveInput` 期望校验：ID/版本/类型分别不符都返回 `ErrArchiveExpectationMismatch`；
  - 路由映射：`extension.expected_mismatch`、`extension.remote_digest_mismatch`；
  - `sourceLabel` 清洗；
  - 断言没有 enable/trust/activate 副作用。
- **Web**：
  - 未声明 `hostActions` 时 `installExtension` 为 `undefined`；
  - 插件详情页与启用确认弹窗都显示 `hostActions` 提示，且不混入 capability 风险等级；
  - 确认框展示 URL host/摘要/声明值；取消不发请求；
  - 单飞拒绝；
  - 120 秒超时参数被传入；
  - 面板与 bridge 共用同一个 transport/错误映射（同一错误码文案一致）；
  - 成功/失败/取消三种状态渲染。
- **浏览器 QA**：插件管理页 + 确认框在 1633×810 与 390×844 下无溢出、焦点管理正确。
- **端到端**：真实包通过 bridge 安装 → `staged` → 去 core 完成信任/启用。

### 6.8 文档

- `docs/extensions/trusted-admin-components.md`：bridge 新方法、清单 `hostActions`、信任披露、同步/取消语义。
- `docs/extensions/catalogs/manifest-v3.md`：`hostActions` 字段。
- `knowledge/modules/extensions.md`：安装动作小节。
- `knowledge/decisions/2026-10-24-admin-page-host-action.md`：为什么不是 capability/命令/worker；为什么确认框不是沙箱；何时升级到异步。

### 6.9 后续升级到“意图 + worker”（不改 bridge）

触发条件（任一）：包体积中位数 > 10 MiB、安装耗时中位数 > 30 秒、需要批量安装、需要离开页面继续、需要进度/重试。

升级方式：

- `installExtension` 的 promise 语义不变：resolve 时“已暂存或失败”。
- 宿主内部改为 `POST /admin/extensions/install-intents` + 轮询 `GET /admin/extensions/install-intents/:id`。
- 新增意图表 + River worker；**不新增 capability、不改插件页面、不改清单**。
- 这一步是独立计划，不在本计划范围。

## 7. 验收

- P0 验证全绿，`marketplace` 符号归零。
- P1：声明了 `hostActions` 的插件页可以经宿主确认框安装一个真实包并得到 `staged` 结果；未声明时方法不存在；取消/单飞/期望不符都按 §6.6 拒绝；`hostActions` 在插件详情页与启用确认弹窗都可见。
- 安装后仍需管理员在 core 完成信任/启用；商城不代劳。
- 文档、清单 schema、SDK 类型、OpenAPI 一致。
- 架构边界门禁、catalog `--check`、OpenAPI refs 全绿。

## 8. 风险与未决

- **同步取消**：管理员离开页面会中止安装；这是 v1 的已知限制，页面必须明确提示。若真实使用中不可接受，按 §6.9 升级。
- **30 秒默认超时**：bridge 必须显式覆盖；部署侧反代（Caddy/Nginx）也要确认读超时 ≥120 秒。
- **确认框不是沙箱**：受信任插件可绕过 bridge 直接 fetch；真正的闸门是信任确认 + `super_admin` + 摘要。文档必须诚实。
- **清单兼容**：旧宿主会因 `additionalProperties: false` 拒收带 `hostActions` 的清单；插件需在发布说明里写最低宿主版本。
- **大包**：core 上限 50 MiB；商城侧建议展示体积并对 >20 MiB 的包给出提示。
- **未决**：是否给 `install-from-url` 增加 `source` 之外的来源标签展示（首版只记 `sourceLabel`）；详情页/启用弹窗两处披露的文案是否共用同一个 i18n key（建议共用，避免漂移）。

## 9. 明确不做

- 不做目录/搜索/详情/截图/版本历史（服务端职责）。
- 不做 GitHub 集成、签名短链、私有仓库（服务端职责）。
- 不做目录签名、镜像、离线包（后续独立计划）。
- 不做自动更新/后台静默安装。
- 不新增 `extensions.install` capability、Host Command、意图表、worker（见 §6.9）。

## 10. 实施清单（按顺序）

### 10.0 开工前

- 读 `AGENTS.md`、本计划、§2 引用的代码点；确认工作树状态，不碰用户未提交的改动。
- 建 todo list，P0 / P1 分两段；P0 完成后先跑验证再进 P1。

### 10.1 P0：清算

- [x] 删除 `apps/api/app/Support/Marketplace/**`（含测试）。
- [x] 清 `bootstrap/p12_ops_services.go`：marketplace import、字段、两个构造函数、`isProdLike`、失效 import（签名收窄为 `(cfg, pool, logger)`）。
- [x] 清 `config/config.go` 两个字段与 env 读取。
- [x] 删 `apps/web/app/pages/admin/extensions/store/**` 与 `SFAdminExtensionStoreShelf.vue`。
- [x] 清 `adminModules.ts` 三条 page 定义 + 侧栏一级文件夹。
- [x] 清两份 i18n 的 `admin.nav.extensionStore*` 与 `admin.extensions.store` 块。
- [x] 改 `tests/validate-admin-framework.ts` 的 store 断言。
- [x] 清 `.env*`、compose、`deploy.sh`、`configure-production*.sh`、`release-smoke.sh` 的 `MARKETPLACE_*`。
- [x] 更新 roadmap、`knowledge/modules/extensions.md`、runbook、旧决策 superseded、`AGENTS.md`。
- [x] `node scripts/v3-catalog/generate.mjs` 重新生成（四个 store 身份转 retired + 墓碑账本）。
- [x] 跑 §5.4 全部验证；`rg marketplace` 归零。
- [x] 提交 P0（`chore(extensions): remove dormant marketplace scaffolding` → `381182cc7`）。

### 10.2 P1：管理页安装动作

- [x] 清单：`ManifestAdminPage.HostActions` + schema + `admin_micro_frontend.go` 校验 + 单测（`admin_host_actions_test.go`）。
- [x] 披露 UI：插件详情页 + 启用确认弹窗，共用同一个 i18n key；不并入 capability 风险等级（`SFAdminExtensionHostActionsNotice`）。
- [x] SDK：`apps/web/packages/admin-sdk` 增加 `installExtension` 类型 + minor bump（1.1.0 → 1.2.0）+ README。
- [x] 前端类型：`AdminExtensionAdminPage.hostActions`（`utils/admin/adminExtensions.ts`）+ `extensionAdminPages` 透传；披露 UI 与 bridge 共用同一字段。
- [x] 服务层：`ArchiveInput` 增加期望字段/`SourceLabel`；`InstallOrUpgradeArchive` 在 manifest 校验后、类型权限 switch 前比对；新增 `ErrArchiveExpectationMismatch`（带实际值）；`mergeArchiveSourceMetadata` 增加 `sourceLabel` 清洗。
- [x] 路由：`install-from-url` 增加可选期望字段/`source`，校验、透传、错误映射 `extension.expected_mismatch`（含实际清单值）；OpenAPI 同步。
- [x] 前端 transport：`useAdminExtensionInstall.installFromUrl` 增加 `expectations`/`timeout`；面板与 bridge 共用（含错误映射）。
- [x] bridge：`SFTrustedAdminPageComponent.vue` 按 `hostActions` 注入 `installExtension`；状态机抽到 `useAdminPageHostInstall`（权限预检、确认框、单飞、120 秒超时、错误映射、卸载中止）。
- [x] 确认框组件：桌面 1633×810 / 窄屏 390×844；焦点管理；取消/成功/失败状态（`SFAdminPageInstallDialog.vue`）。
- [x] 测试：§6.7 的 Go / Web / 浏览器 / 端到端（端到端 staged 只有路由/服务层证明，见下）。
- [x] 文档：`docs/extensions/trusted-admin-components.md`、`docs/extensions/authoring-guide.md`（`manifest-v3.md` 是生成页，只列根字段，故嵌套字段写在这里）、`knowledge/modules/extensions.md`；两份决策记录。
- [x] 跑 §7 验收 + `./scripts/test.sh --fast`。
- [x] 提交 P1。
- [x] 更新 `knowledge/index.md` 与 hot handoff。

### 10.3 Definition of Done

- [x] P0/P1 验证全绿；`rg -i marketplace apps/api apps/web` 为 0。
- [ ] **未完成**：真实公网包经 bridge 到 `staged` 只做到路由/服务层证明（本机无可公网直链的合法包；`codeload` 只验证下载+摘要+拒绝）。
- [x] 未声明 `hostActions`、取消、单飞、期望不符都按 §6.6 拒绝。
- [x] `hostActions` 在插件详情页与启用确认弹窗都可见。
- [x] 没有新增 capability、Host Command、意图表、worker。
- [x] 没有创建/修改 `sforum-app-store`、商城插件或服务端。

