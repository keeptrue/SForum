# 构建、摘要与加载你的扩展（Build, digest, and load）

> **语言切换：** [English (canonical)](../../extensions/build-and-load.md) · 本文为中文翻译。
> 命令、字段名与错误码以英文原文和代码为准；中文版若与代码不一致，以代码为准。

本页讲述带可执行后端（以及可选前端资产）的插件的完整作者循环：脚手架、
Go 模块接线、构建二进制与前端产物、刷新精确摘要、打包，以及把包加载到
运行中的 SForum 实例。

配套文档：[插件路由（中文版）](./routes.md) · [插件编写指南（英文）](../../extensions/authoring-guide.md)

## 已构建包的样子

```text
my-plugin/
├── sforum.extension.json      # Manifest V3
├── README.md
├── backend/
│   ├── go.mod                 # 你的 Go 模块（见下文）
│   ├── go.sum
│   ├── *.go                   # Protocol V2 server（pluginv2.Serve）
│   └── plugin                 # 构建出的可执行文件（backend.entry）
├── frontend/
│   └── admin/
│       ├── src/               # 可选 Vue 作者源码
│       └── dist/              # 生产环境加载的不可变 ESM/CSS
└── schemas/
    └── *.json                 # packageFiles kind "schema"（路由文档等）
```

`make:plugin` 会生成 manifest、README、占位的 `backend/plugin` stub，以及
（带 `--backend` 时）说明最小 SDK 程序的 `backend/README.md`。加上
`--vue-admin-page` 会得到真实 Vue 页面工作区与立即可校验的占位产物。
脚手架**不会**生成 Go 模块——`go.mod` 需要你自己创建。

```bash
cd apps/api
go run ./cmd/sforum make:plugin \
  --id acme.notes --name "Acme Notes" --description "…" \
  --url https://example.com/acme-notes --author-name Acme \
  --backend --no-interaction --out /tmp/acme.notes

# 继承 Host 后台壳层、适合新手的 Vue dashboard
go run ./cmd/sforum make:plugin \
  --id acme.dashboard --name "Acme Dashboard" --description "…" \
  --url https://example.com/acme-dashboard --author-name Acme \
  --vue-admin-page --no-interaction --out /tmp/acme.dashboard
```

本地实验时脚手架默认落到 `extensions/dev/`（gitignored，永不被自动注册）。
需要可丢弃路径用 `--out`，需要受保护内置包用 `--builtin`（落在
`extensions/builtin/`）。

## 1. 接好后端 Go 模块

后端 import 公共 SDK，而 SDK 位于宿主模块
`github.com/keeptrue/sforum/apps/api` 内。该模块没有发布为带版本号的依赖，
因此仓库内每个包都用 `replace` 指令指向本地 checkout。工具链版本以宿主
`go.mod` 为准（当前 Go 1.26.9）。

`backend/go.mod`（仓库内包）：

```go
module github.com/keeptrue/sforum/extensions/builtin/plugins/sforum-smtp/backend

go 1.26.9

require github.com/keeptrue/sforum/apps/api v0.0.0-00010101000000-000000000000

// 指向包含 apps/api 的仓库 checkout。
replace github.com/keeptrue/sforum/apps/api => ../../../../../apps/api
```

- 仓库内：相对 `replace` 路径必须从你的 `backend/` 目录数出足够层级的 `..`
  到达仓库根的 `apps/api`。
- 仓库外：把 SForum clone 到任意位置，让 `replace` 指向
  `<你的-checkout>/apps/api`（绝对或相对路径均可）。宿主模块很大，
  `go mod tidy`/`go build` 会下载大量传递依赖——中国大陆网络先设置本地
  代理（见 `AGENTS.md`）。
- require 行的版本只是占位（仓库内实际使用零版本号，被 `replace` 覆盖），
  解析完全由 `replace` 决定，可写任意合法版本号。
- 仓库外插件的 `module` 路径任选，但请保持稳定。

最小 `main.go`：

```go
package main

import pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"

func main() {
	pluginv2.Serve(pluginv2.NewServer())
}
```

按需在 server 类型上添加 route、hook、provider、query、job 或 notification
处理器（见 [插件路由（中文版）](./routes.md) 与
[插件编写指南（英文）](../../extensions/authoring-guide.md)）。

```bash
cd <package-root>/backend
go mod tidy
go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin .
```

可执行文件必须落在 `backend.entry` 声明的路径（惯例是 `backend/plugin`）。
二进制缺失时 `extension test` 会失败（脚手架阶段可用
`--skip-backend-binary` / `--allow-scaffold`）。

## 2. 构建前端资产（仅在需要时）

宿主**从不**编译上传的源码。运行时挂载的一切——admin 预构建组件、公共 L2
模块、editor L2 节点——必须是**最终、自包含的 ESM/CSS 字节**，按声明路径
提交进包：

- `--prebuilt-settings` 会生成可用的 `frontend/admin/dist/settings.mjs` +
  `.css`，并保留必需的 Schema 回退。
- `--vue-admin-page` 会生成 `AdminDashboard.vue`、Vite 配置、
  `@sforum/admin-sdk` 与 `@sforum/plugin-ui` 依赖。页面自动继承 Host 的
  sidebar、topbar、tabs、标题、路由守卫和权限检查，作者通常不必写 CSS。
- fixture 的 L2 模块（如 `sforum-custom-content/frontend/editor/vote.mjs`）
  是手写的单文件 ES module。
- 你可以用任意打包器（esbuild、rolldown、vite 等）产出最终单文件 `.mjs`；
  包只携带输出。手写模块不要求 `package.json`；Vue 脚手架中的 package
  文件只服务作者本地构建。

Vue 页面脚手架的完整循环：

```bash
sforum extension build --allow-scaffold <package-root>
```

这个作者侧命令会安装并构建 `frontend/admin`、刷新精确摘要、校验包并执行契约
测试。依赖已经安装时可加 `--skip-install`。它会执行插件本地 package scripts，
因此上传、安装、启用和生产运行时绝不会调用该命令。

脚手架自带可用的占位 `dist`，因此没安装 Bun 时插件包也能先通过校验。
Vite 构建会替换 dashboard 产物，并保留同目录的预构建设置组件。
`extension package --exclude-source` 会去掉 `.vue`、`.ts`、Vite 配置、
package 元数据与锁文件，只留下最终 `.mjs`/`.css`；生产环境不会执行构建脚本。

每个产物都要在 `packageFiles` 中声明：`kind: "frontend"`（或按需 `asset` /
`schema` / `template`）+ 精确路径。所有摘要由 `extension digest --write`
刷新。

## 3. 刷新精确摘要

manifest 引用的每个打包文件——后端可执行文件、前端资产、schemas、模板——
都以 SHA-256 绑定在 `packageFiles` 中。任何文件变更后：

```bash
cd apps/api
go run ./cmd/sforum extension digest --write <package-root>
```

带作者前端的包应优先使用 `extension build`；它已包含这一步以及后续校验和
契约测试。

这会重写内联摘要（包括内联模板摘要）并重新校验整个包。Page Registry 主题
需要**先**补好对应的 `templates[]` 声明与 `packageFiles[]` 条目再运行它；
digest 刷新绝不会从 `theme.json` 推断模板身份或成员关系。

## 4. 校验与契约测试

```bash
go run ./cmd/sforum extension validate <package-root>            # schema + 预检
go run ./cmd/sforum extension test <package-root>                # host 契约检查
go run ./cmd/sforum extension test --json <package-root>
```

后端二进制还没构建时，`extension test --allow-scaffold`（同
`--skip-backend-binary`）可让契约检查保持通过。

## 5. 打包分发

```bash
go run ./cmd/sforum extension package <package-root> --exclude-source \
  -o /tmp/my-plugin.sforum.zip
```

`--exclude-source` 会剔除 Go/JS/TS 源码、进行中的 manifest、`testdata`
等非运行文件；zip 保留可执行文件、预构建前端、schemas 与 README。输出为
`<name>.sforum.zip` + 旁路 `.sbom.json`。默认（不带 flag）打包除
`.git`/`node_modules`/`vendor` 与既有 zip 外的几乎所有内容。

## 6. 加载到运行中的实例

| 方式 | 做法 | 后台列表 |
| --- | --- | --- |
| 外部源码集合 | 把包放到 `<root>/plugins/<id>/`（主题为 `<root>/themes/<id>/`），在 `.env` 设置 `EXTERNAL_EXTENSION_ROOTS=<root>`，重启 API | 自动出现快照；仍需信任 + 启用 |
| 上传 | 后台 → 扩展 → 插件 → 安装 zip | 已安装；信任 + 启用 |
| 内置（仓库内包） | `make:plugin --builtin`，重启 API 让 `SyncBuiltins` 注册 | 自动 |
| `extensions/dev/` | 永不扫描——用上面任一方式 | — |

`extensions/dev/` 只是临时草稿区。日常本地迭代推荐**外部源码集合**：

```sh
# .env
EXTERNAL_EXTENSION_ROOTS=/abs/path/to/sforum-plugins
```

```text
/abs/path/to/sforum-plugins/
  plugins/acme.notes/          # 你的包源码
```

- API/worker 启动时扫描每个根，把合法包以**不可变快照**复制进
  `EXTENSION_ROOT`。内容变化只产生**待审核版本**；永远不会自动提升或继承
  信任。
- 日常迭代用 **dev live-reload 链**（见下）——不需要为了看到新快照而重启 API；
  `dev-trust-refresh` 会在开发装配下重扫 `EXTERNAL_EXTENSION_ROOTS`，把刚构建的
  新摘要直接暂存进运行中实例。

### 日常迭代：零命令 dev live-reload（推荐）

**不需要任何命令。** 开发装配（`APP_ENV=development` 且非 Safe Mode）下，host
进程自己用 fsnotify 监视 `EXTERNAL_EXTENSION_ROOTS`。你只要**保存文件**：

```text
保存 → host 自动：前端 bun run build（如有）+ 后端 go build → digest（含 SRI 重算）
→ 暂存新摘要 → 逐摘要写入精确制品授予 → 自动 enable/upgrade → 插件子进程按新摘要
重启 → lifecycle 发布新公开资产
```

- **零登录、零密码、零信任会话、零人工确认**；空库（0 管理员）也可用——一个无凭据的
  `dev-auto` 系统账号作为审计归因（在后台禁用/删除它即关闭本通道）。
- 首次安装也自动：把包放进外部根，host 自动 install → enable。
- **改内容不提版**：改 CSS/JS、后端逻辑、settings、routes 都直接生效。只有改组件
  **接口声明**（props/schema 形状、换 `l2Component` 引用、增删组件 ID）才需手动把
  `components[].contractVersion` 提版（如 `@3`→`@4`）——这是正确的人工检查点，
  不应自动化（自动提版会与精确制品体系冲突，已被移除）。
- 生产与 Safe Mode 不装配 watcher（fail closed）；HTTP install/enable/upgrade 路由的
  super_admin 门原样保留。

> 遗留：CLI `sforum extension dev <root> --auto-upgrade` 与 `extension dev-trust *`
> 仍在，但只是带凭据的旧路径；日常开发用上面的零命令 host 通道即可。
> 详见 `docs/zh-CN/development/cli.md`。

### 启用与信任

1. 后台 → 扩展 → 插件 → 打开你的插件。
2. **启用**会为可执行内容触发信任流程（后端二进制、迁移、guard、L2 前端、
   声明的路由注册表贡献）。`super_admin` 基于精确制品（版本 + 摘要）确认。
   影响文档会列出已声明的权力、依赖与 Host/Frontend 契约版本。
3. 启用后，声明的路由、hook 与设置生效。用
   `curl http://127.0.0.1:8080/<path>` 或 Web 同源冒烟测试路由。

### 迭代循环

```text
改源码 → 保存 → watcher 自动：构建/前端 → digest（含 SRI 重算）→ validate → test
→ dev-trust-refresh 重扫外部源暂存 → 逐摘要自动授予 → 自动 upgrade → 插件子进程重启
```

首次安装与**首次启用**仍走后台流程（安装 inert 快照 → 精确制品信任，活跃
dev trust 会话会自动完成逐摘要授予）。首次启用成功后，后续迭代由 dev 循环接管
（`upgrade` 前置条件是 enabled）。不要用 `SFORUM_V3_TRUST_CHALLENGES=false`
之类 v1 兼容开关代替本链：实测它并不省掉 super_admin 凭据，只会把逐摘要审计
退化成幂等兼容 grant（见 knowledge/decisions/2026-10-17-dev-live-activation-chain.md）。
若某个插件导致 API 无法启动，带外恢复：
```bash
go run ./cmd/sforum extension disable <extension-id>
go run ./cmd/sforum extension disable-all
go run ./cmd/sforum extension list --json
```

## 内置包（仓库内）

`extensions/builtin/` 由 `SyncBuiltins` 启动扫描。`api-dev.sh` /
`worker-dev.sh` 把它暂存到 `storage/builtin-dev` 并运行
`scripts/build-builtin-plugins.sh`，该脚本**只构建硬编码在脚本里的 id**，
并在暂存树里刷新摘要（绝不改写被 git 跟踪的 manifest）。

新内置包在存在于活动内置根之后，重启即被 `SyncBuiltins` 注册，但它的后端
**不会**自动构建——直到你把 id 加进 `scripts/build-builtin-plugins.sh`
（或自己构建 + `extension digest --write`）。手动构建内置后端：

```bash
(cd extensions/builtin/plugins/<id>/backend && go test ./... && go build -o plugin .)
```

不要直接改 `storage/builtin-dev/` 或 `storage/extensions/**`；把它们当作
生成状态。

## 排障

**组件契约版本何时手动提版？** 改 CSS/JS 内容、改后端逻辑、改 settings/routes 都
不需要——保存即自动生效。只有当你改了**组件的接口声明**（改 props/schema 形状、
换引用的 l2Component、新增/删除组件 ID）时，才需要把 `components[].contractVersion`
提版（如 `@3` → `@4`）。提版 = 告诉宿主「接口变了，请重新审批准入」；不提版 =
「只改了内容，宿主无需重新审查」。

| 现象 | 原因 / 处理 |
| --- | --- |
| `extension test` 后端失败 | `backend.entry` 处缺二进制；构建它，或脚手架阶段用 `--skip-backend-binary` |
| 启用返回 `extension.build_failed` | 某声明文件摘要或模板身份过期；用 dev 循环（`--once`）或 `extension digest --write` 修复后让 dev-trust-refresh 重新暂存 |
| 路由 404 / 探测报 `miss` | 路径未被任何已启用插件路由认领；查后台路由检查器与冲突页 |
| 不安全路由 CSRF 403 | 浏览器调用缺 `X-Csrf-Token`（与 `csrf_` cookie double-submit）；用应用的 `useApiClient` 或 Bearer PAT |
| 路由超时 | 处理器超过路由 `timeoutMs`（默认 3 秒）；把大工作流化或调高声明 |
| 外部根变更不可见 | 走 dev 循环链；`dev-trust-refresh` 会重扫外部源暂存新摘要，无需重启（未接 dev 循环时才需要重启） |
| 每次迭代都弹信任确认 | 说明 dev trust 会话缺失/过期——跑 `extension dev-trust create --ensure`（或一键 wrapper）恢复自动授予 |
| 坏插件导致 API 无法启动 | 带外 CLI：`extension disable-all` / `quarantine`，再选择性启用 |

## 相关

- [插件路由（中文版）](./routes.md) · [英文原文](../../extensions/routes.md)
- [插件编写指南（英文）](../../extensions/authoring-guide.md) — 契约、设置、参考包
- [Manifest V3 目录（生成）](../../extensions/catalogs/manifest-v3.md)
- [`extensions/README.md`](../../../extensions/README.md) — 包目录地图
- CLI 参考：[开发者 CLI（中文）](../development/cli.md) · [Developer CLI（英文）](../../en-US/development/cli.md)
