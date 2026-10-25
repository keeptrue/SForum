# 2026-10-25 宿主 coreVersion 暴露 + 清单兜底 Handoff

## Changed

- **版本权威**：`apps/api/version` 新增 `HostIdentity` / `Identity()`
  （`coreVersion` = `CoreCompatibilityVersion()`、`buildVersion` =
  `Get().Version`、`valid` = 严格 semver）。`cmd/api` 启动日志打印
  `coreVersion`，非法标签时 WARN。原有 `CoreCompatibilityVersion()`/`Get()` 未改。
- **protocol v2**：`control.proto` 新增 `HostBuild` 与
  `HandshakeRequest.host_build = 8`，重新生成 SDK。宿主在
  `protocol_v2_identity_runtime.go::protocolV2HandshakeRequest` 填入
  `version.Identity()`；SDK `Server.HostBuild()` 返回 `(value, ok)`，未握手/旧宿主
  `ok=false`，重复握手身份漂移按 `protocol.stale_runtime` 拒绝。为守住
  `protocol_v2_client.go` 的 1022 legacy 上限，握手字面量外移，该文件 1022 → 1003，
  baseline 同步下调。
- **清单兜底**：新增 `app/Support/ExtensionManifest/compatibility.go`
  （`CheckSForumVersion`）；`Models/Extensions/lifecycle.go` 新增
  `CoreIncompatibleError` / `ensureCoreCompatible` / `requireEnablePreflight`。
  安装 → 422，启用/主题激活/升级/回滚/重启 → 409，统一
  `extension.core_incompatible` + `required`/`coreVersion`/`buildVersion`。
  4 处 `requireArtifactAvailable` 就地替换（行数不变，绕开 92 文件上限与
  `service_lifecycle.go` 999 行）。
- **HTTP**：`/api/v1/health` 加 `coreVersion`；公开 L2 描述符加 `coreVersion`；
  `page-bootstrap` 加 `host`；OpenAPI `Health` / `PublicFrontendComponent` 同步；
  V3 routes catalog 因 `server.go` 行号位移重新生成。
- **前端**：`@sforum/admin-sdk` 1.2.0 → 1.3.0（`AdminHostIdentity` + 两个 bridge 的
  可选 `host`）；两个受信组件透传 page-bootstrap 的 `host`；公开 L2
  `PublicFrontendBridgeV1.host.coreVersion` 由 `SFExtensionWidget` 组装。
- **文档/知识库**：`docs/extensions/{host-api-v2,authoring-guide,trusted-admin-components}.md`、
  `knowledge/modules/extensions.md`、`knowledge/index.md`、决策
  `decisions/2026-10-25-core-version-surface.md`、伴生计划 D9 标 superseded、
  `tests/validate-host-api-v2-docs.mjs` 跟随新文件位置并新增 `host_build` 不变量。

## Decisions

- 一个权威两个字段：`coreVersion` 是兼容判定唯一权威，`buildVersion` 只做展示。
- 后端面走 v2 握手而非 v1 `Ping`：生产路径从不注入 `SFORUM_HOST_API_*`（已实测）。
- 内置包豁免（默认主题当前 `^1.0.0`）；非法 core 标签跳过兜底并告警；
  预发布 core 按发行基线再求值一次（Masterminds 默认不匹配预发布）。
- 公开面只给 `coreVersion`；`buildVersion`/commit 只在管理面。

## Evidence

- `go build ./...` + 全部受影响包 `go test` 全绿（`Models/Extensions` 120.6s 全量）；
  新增 10 个针对性用例（Identity 三分支、约束通过/拒绝/不可求值/预发布、
  安装不落库、dev 通过 vs 3.x 拒绝、启用拒绝不运行、内置豁免、非法 core 跳过、
  握手冻结与漂移、health、page-bootstrap、公开描述符），另有 3 处既有断言扩展。
- `bun run typecheck` exit 0；`bun test` 1619 pass / 0 fail。
- `node tests/validate-architecture-boundaries.mjs` 通过（baseline 棘轮下调）。
- `ruby scripts/validate-openapi-refs.rb` 2907 refs OK；`scripts/proto.sh generate`
  幂等，且在临时提交树上 `scripts/proto.sh check` 通过（脏工作区必然报 drift）。
- contracts-gate 其余静态项逐条手动跑过：host-api-v2 docs、staged contracts、
  V3 trust、WS proxy、docs、identity UI、homepage、SEO ×2、moderation、theme
  runtime/activation、signal garden、SF components、page registry、V3 catalogs、
  公开页面 surface、tzdata、admin framework 全部通过。

## Next

- **商城仓**：`plugin/plugins/sforum.store/backend/upstream.go` 在 SForum 发版含本面、
  且其 `go.mod` 指向的 SDK 带上 `host_build` 之后，才转发 `sforum`
  （=`coreVersion`）/`sforumBuild`（=`buildVersion`）；同步改 `handlers.go` 注释与
  `handlers_test.go` 的丢弃断言。
  **已落地（2026-10-25，商城仓，SForum 侧 `host_build` 仍在工作区未提交）**：后端只认
  `Server.HostBuild()`，`compat` 开关默认 `compatible`；同时补了 `lifecycle V2` 声明。
  真机遇到宿主侧残留：非 V2 包换摘要后注册表不重发，路由 `admitted=false` → 503；
  补 lifecycle 后启用停在 `extension lifecycle registry publication exact fence conflict`，
  需复位该扩展的注册表发布行后重跑。证据见商城仓
  `knowledge/sessions/2026-10-25-store-menu-version-inventory.md`。
- **浏览器回归（部分）**：dev 服务在跑，实测 `/api/v1/health`（直连 8080 与经
  3000 代理）都返回 `coreVersion: "1.0.0"`，首页 SSR 正常；但管理页需要登录
  （未提供凭据），且当前实例没有已装的公开 L2 组件，所以 `bridge.host` 的运行时
  路径只做了源级契约 + Go/前端单测，没有真实浏览器证据。下次补齐：1633×810 打开
  storage-s3 设置页与任一公开 L2 页面，确认组件挂载、`bridge.host` 有值、无 console 报错。
- **默认主题约束**：随其下次版本 bump 把 `sforumVersion` 从 `^1.0.0` 订正到目标发行线。

## Open Questions

- core 升级后是否自动禁用已启用的不兼容扩展、或只在管理列表加兼容徽标？
  自动禁用会在无人值守时停掉论坛功能，需要独立政策决策；本轮明确不做。
- 公开 `/api/v1/health` 的 `coreVersion` 是否需要 CDN/反代缓存隔离？
  版本随进程固定，沿用现有缓存策略，暂不特殊处理。
