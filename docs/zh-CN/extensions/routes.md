# 插件路由（Plugin routes）

> **语言切换：** [English (canonical)](../../extensions/routes.md) · 本文为中文翻译。
> 字段名、端点与错误码以英文原文和代码为准；中文版若与代码不一致，以代码为准。

本页是**插件声明式 HTTP 路由**的作者指南：如何在 Manifest V3 中声明路由、
如何用 Protocol V2 Go SDK 实现处理器、有哪些 guard 与 fallback，以及请求
如何从浏览器到达你的插件进程。

配套文档：

- [插件编写指南（authoring-guide，英文）](../../extensions/authoring-guide.md)
- [构建与加载循环（中文版）](./build-and-load.md) · [英文原文](../../extensions/build-and-load.md)
- [Manifest V3 字段目录（生成）](../../extensions/catalogs/manifest-v3.md)
- [核心路由目录（生成，是 Core 声明了什么，不是教你声明自己的）](../../extensions/v3/catalogs/routes.md)

> **权威示例：** `extensions/fixtures/plugins/sforum-custom-content/` 是完整
> 的路由参考包：`routes[]` 声明见 `sforum.extension.json.tmpl`，处理器实现见
> `backend/routes.go`，schema 绑定见 `schemas/` 下的 `packageFiles`。

---

## 插件路由如何工作

1. 包在 `routes[]` 中声明条目：稳定 id、HTTP 路径、方法、guard、mode 与
   fallback 策略。
2. 启用时，宿主 Route Registry 接收声明并发布到版本化路由注册表。与现有
   路由冲突会在准入阶段 fail-closed（管理后台的路由检查器与 route-provider
   冲突页面可查看）。
3. 入站请求先到达 API 入口，入口根据注册表构建执行计划。若命中插件步骤，
   dispatcher 调用你的后端进程（Protocol V2 `InvokeRoute` RPC，流式模式走
   route stream）并回写响应。
4. 在 Web 应用中，`apps/web/server/middleware/plugin-route-proxy.ts` 决定某个
   同源路径是否属于插件：它向 API 发送带 `X-SForum-Internal-Route-Probe: v1`
   与 `X-SForum-Internal-Route-Method: <METHOD>` 的 `HEAD` 探测请求。API 命中
   插件路由时返回 `204` + `X-SForum-Internal-Route-Result: plugin`，未命中返回
   `404` + `miss`。只有命中路径会被代理到 API；其余路径继续走 Nuxt 渲染。

以下保留路径**永不**代理到 API：

- `/api/v1` 与 `/api/v1/**`（Core API 是权威上游）
- `/_sforum/**`、`/_nuxt/**`
- `/media/avatars/**`、`/media/attachments/**`

若探测本身失败（API 不可用）：`GET`/`HEAD` 回退到普通 Nuxt 渲染；不安全方法
返回 `503`，因为不能让它们落到另一个潜在写入者。

---

## 最小示例

Manifest（`sforum.extension.json`）：

```json
{
  "manifestVersion": 3,
  "id": "acme.notes",
  "name": "Acme Notes",
  "description": "Example plugin with declared routes",
  "url": "https://example.com/acme-notes",
  "author": { "name": "Acme" },
  "version": "1.0.0",
  "type": "plugin",
  "sforumVersion": "^1.0.0",
  "capabilities": ["host.api"],
  "backend": {
    "entry": "backend/plugin",
    "rpc": "hashicorp-go-plugin",
    "protocolVersion": 2,
    "hostApiVersion": "sforum.host@2"
  },
  "routes": [
    {
      "id": "acme.notes.route.list",
      "contractVersion": "acme.notes.route.list@1",
      "action": "add",
      "path": "/api/acme-notes",
      "methods": ["GET"],
      "guard": "core.guard.public",
      "fallback": "closed",
      "mode": "http",
      "handler": "acme.notes.route.list",
      "responseSchema": "acme.notes.route.list.response@1"
    },
    {
      "id": "acme.notes.route.create",
      "contractVersion": "acme.notes.route.create@1",
      "action": "add",
      "path": "/api/acme-notes",
      "methods": ["POST"],
      "guard": "core.guard.login",
      "fallback": "closed",
      "mode": "http",
      "handler": "acme.notes.route.create",
      "requestSchema": "acme.notes.route.create.request@1",
      "responseSchema": "acme.notes.route.create.response@1"
    }
  ],
  "packageFiles": [
    { "id": "acme.notes.file.backend", "kind": "executable", "path": "backend/plugin", "digest": "…" },
    { "id": "acme.notes.schema.list-response", "kind": "schema", "path": "schemas/list-response.json", "version": "1", "digest": "…" },
    { "id": "acme.notes.schema.create-request", "kind": "schema", "path": "schemas/create-request.json", "version": "1", "digest": "…" },
    { "id": "acme.notes.schema.create-response", "kind": "schema", "path": "schemas/create-response.json", "version": "1", "digest": "…" }
  ]
}
```

后端（`backend/plugin.go`）——在你的 server 上实现 `InvokeRoute` RPC：

```go
package main

import (
	"context"
	"net/http"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	listResponseSchema   = "acme.notes.route.list.response@1"
	createRequestSchema  = "acme.notes.route.create.request@1"
	createResponseSchema = "acme.notes.route.create.response@1"
)

type notesServer struct{ *pluginv2.Server }

func (s *notesServer) InvokeRoute(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	switch request.GetRouteId() {
	case "acme.notes.route.list":
		return s.list(request)
	case "acme.notes.route.create":
		return s.create(request)
	default:
		return routeError(request, "acme_notes.route_unknown"), nil
	}
}

func (s *notesServer) list(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	body, err := pluginv2.NewTypedDocument(listResponseSchema, map[string]any{
		"notes": []any{},
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *notesServer) create(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	if request.GetBody() == nil {
		return routeError(request, "acme_notes.request_required"), nil
	}
	// 宿主已在分发前按 createRequestSchema 校验过请求体。
	body, err := pluginv2.NewTypedDocument(createResponseSchema, map[string]any{"created": true})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusCreated, Body: body,
	}, nil
}

func routeError(request *pluginwire.RouteRequest, reason string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: reason, Message: reason,
		},
	}
}

func routeResponseContext(ctx *protocolwire.RequestContext) *protocolwire.ResponseContext {
	// 把请求身份回写到响应。宿主要求 ResponseContext，并按 RequestId/Trace
	// 关联回本次调用。
	if ctx == nil {
		return &protocolwire.ResponseContext{ServerTime: timestamppb.New(time.Now().UTC())}
	}
	return &protocolwire.ResponseContext{
		RequestId:  ctx.GetRequestId(),
		Trace:      proto.Clone(ctx.GetTrace()).(*protocolwire.TraceContext),
		Extension:  proto.Clone(ctx.GetExtension()).(*protocolwire.ExtensionIdentity),
		ServerTime: timestamppb.New(time.Now().UTC()),
	}
}

func main() {
	pluginv2.Serve(&notesServer{Server: pluginv2.NewServer()})
}
```

构建、刷新摘要、校验并加载（完整循环见 [构建与加载循环](./build-and-load.md)）：

```bash
cd apps/api
go run ./cmd/sforum extension digest --write <package-root>
go run ./cmd/sforum extension validate <package-root>
go run ./cmd/sforum extension test <package-root>
```

---

## Manifest 声明参考

`routes` 是数组；每项必填 `id`、`contractVersion`、`action`、`path`、
`methods`、`fallback`、`mode`。权威字段形状是内嵌的 Draft 2020-12 schema
（`apps/api/app/Support/ExtensionManifest/schemas/manifest-v3.schema.json`）。

| 字段 | 含义 |
| --- | --- |
| `id` | 稳定命名空间路由 id（`[a-z0-9][a-z0-9._-]{1,80}`），如 `acme.notes.route.list`。运行时会把它作为 `RouteId` 传给 `InvokeRoute`，按它做 switch。 |
| `contractVersion` | 版本化契约，如 `acme.notes.route.list@1`。路由契约每次变更都必须升版本。 |
| `action` | 路由如何加入注册表：`add`（新增）、`alias`、`redirect`、`rewrite`、`before`、`after`、`filter`、`wrap`、`replace`（配 `targetId`）、`global_middleware`。默认 `add`。 |
| `targetId` | 该 action 指向的稳定路由 id（`replace` 等定向 action 必填）。 |
| `path` | 规范 HTTP 路径：静态段、`:name` 参数、末尾 `*name` 通配段（单独的 `*` 命名为 `path`）。路径必须规范（无 `..`、无 `//`）。 |
| `methods` | 本声明覆盖的 HTTP 方法。 |
| `guard` | guard id：`core.guard.public`、`core.guard.login`、`core.guard.permission`（配 `permission`）、`core.guard.guest`、`core.guard.inherit`，或自定义 guard id（原始请求权威只能经包内显式声明的 `kind: raw_request` 守卫获得；`core.guard.raw_request` 已被拒绝）。见 [Guards](#guards)。 |
| `access` | 便捷简写，映射到 core guard：`public` → `core.guard.public`、`login` → `core.guard.login`、`permission` → `core.guard.permission`（配 `permission`）。设置了 `guard` 时忽略。 |
| `permission` | `core.guard.permission`（或 `access: "permission"`）需要的权限键。 |
| `priority` | 重叠声明的排序提示（整数）。 |
| `fallback` | 失败策略：`closed`（默认，请求 fail-closed）、`not_found`、`readonly_core`。`not_found`/`readonly_core` 只在 `GET`/`HEAD` 且尚未开始响应或副作用时回退。 |
| `mode` | 传输模式：`http`（默认）、`sse`、`websocket`、`stream`、`multipart`。 |
| `destination` | `redirect`/`rewrite` action 的目标。 |
| `statusCode` | 重定向状态码：`301` 或 `308`（默认 `308`）。 |
| `handler` | 你的处理器名（任意稳定字符串；运行时按 `id` 识别路由，不按此字段）。 |
| `requestSchema` | 对请求体做校验的 schema 引用。路由声明了任何不安全方法（`POST`/`PUT`/`PATCH`/`DELETE`）时**必填**；预检会拒绝没有合法引用的路由。 |
| `responseSchema` | 对结构化 HTTP 响应做校验的 schema 引用。处理器路由（`add`/`alias`/`wrap`/…）**必填**；预检会拒绝没有合法引用的路由。 |
| `mutableRequestFields` / `mutableResponseFields` | 处理器可通过 `RequestPatch`/`ResponsePatch` 修补的 RFC 6901 路径；宿主对冻结的 allowlist 逐操作校验。 |
| `timeoutMs` | 每步 RPC 超时。省略时 dispatcher 默认 3 秒。 |

Schema 引用可以是版本化 schema id（`name@N`，经包内 schema 目录解析），也
可以是包内相对路径 `schemas/foo.json`。两者都必须解析到 `packageFiles` 中
`kind: "schema"` 且摘要精确匹配的条目；目录里没有该条目时声明会被拒绝，
而不是把 id 当散文处理。

### 路由 action

| Action | 含义 |
| --- | --- |
| `add` | 认领新的路径+方法组合。 |
| `alias` | 让同一处理器在额外路径下提供服务。 |
| `redirect` | 以 `301`/`308` 响应 `destination`。 |
| `rewrite` | 内部把请求路径重写为 `destination` 后继续匹配。 |
| `before` / `after` | 在目标路由（见 `targetId`）前后链式执行。 |
| `filter` | 检查/修改目标路由的请求/响应文档。 |
| `wrap` | 包裹目标路由的执行。 |
| `replace` | 替换目标路由（core 或插件）——高风险，必须显式声明并经信任确认。 |
| `global_middleware` | 对所有 registry 管理路径生效。 |

替换与链式目标都是**显式声明**的，从不推断。Core 路由 id 见生成的
[核心路由目录](../../extensions/v3/catalogs/routes.md)。管理后台的
route-provider 选择与冲突页面（`/api/v1/admin/extensions/route-providers/*`）
展示当前每个路径由谁拥有；冲突在准入阶段被拒绝。

---

## Guards

| Guard id | 何时放行 | 说明 |
| --- | --- | --- |
| `core.guard.public` | 所有人，无需登录 | 公开读接口的常见选择。 |
| `core.guard.login` | 任意活跃已登录用户 | |
| `core.guard.permission` | 持有 `permission` 的已登录用户 | |
| `core.guard.guest` | 仅游客（未登录） | |
| `core.guard.inherit` | 未显式声明 guard 的定向 action | 继承目标路由的 guard；目标不可解析前保持关闭。 |
| `core.guard.raw_request` | **已拒绝** | **2026-10-16 移除。** 它过去允许「插件 guard 绑定为空、零 guard 代码」的路由拿到原始请求权威，并把 `cookie`、`authorization`、`x-api-key`、`x-auth-token` 转发给你的处理器。该行为被判定为漏洞，现在在清单校验阶段直接拒绝。确实需要原始权威时，请自行声明 `kind: raw_request` 的守卫（`entry` + `digest` + `contractVersion`）并引用它，宿主会在信任确认时披露该高风险。 |
| 自定义 guard id | 只能经受信任的 guard 运行时 | guard 二进制通过 `guards[]` 族声明（`kind: custom` / `kind: raw_request`，精确 `entry` + `digest`，可选 `permissions`）。可执行 guard 运行时**在生产中是活的**：由 `bootstrap/api_assembly_domains.go` 注入，`Support/Routes/core_guard_authorizer.go` 分发。 |

普通路由请用 `core.guard.public` 与 `core.guard.login`。宿主在调用你的处理器
**之前**完成 guard 检查，并在 RPC 上下文中携带已解析的权威信息；你的处理器
永远不要用原始请求数据重新实现登录或权限检查。

### 自定义 guard 已上线，门禁与其他插件代码一致

`guards[]` 中 `kind: custom` 的声明会解析成一个经 Protocol V2 运行的插件
处理器（以 guard id 作为 route id 调用 `InvokeRoute`），它只能回答放行/拒绝，
不得返回 body。宿主在调用前要求全部满足：

- 精确路由/guard 绑定（声明的 guard id、非空 contractVersion、与执行计划步骤
  一致的制品）；
- 精确制品对应的运行实例：扩展 id、插件类型、`enabled` 状态、声明版本与精确
  包摘要全部一致；
- 实例不在 Safe Mode；需要信任或请求原始权威时，该精确制品已受信任；
- 该实例存在有效的运行准入租约。

任一条件失败（无评估器、绑定非法、运行实例缺失/未受信任/处于 Safe Mode，或
guard 侧报错）都会拒绝请求；不存在绕过 guard 直接执行处理器的回退路径。

以下三点**只有校验与披露，没有强制也没有定论**，不要依赖：

- guard 声明上的 `permissions[]` 属于信任披露的一部分，宿主不据此强制；判断
  权在 guard 自己。
- 线协议里不存在 `challenge` 结果（跳转/二次验证）。guard 只能放行或拒绝。
- `core.guard.raw_request` **不再是可用守卫**（2026-10-16 移除）。它过去在
  **零 guard 代码**的前提下把原始请求权威交给路由；运维方判定这是漏洞，因此
  清单校验现在拒绝该 id，运行时也对其 fail closed。原始权威只能通过包内**显式
  声明**的 `kind: raw_request` 守卫获得，宿主会在精确制品信任确认时把它作为
  高风险披露。

---

## Protocol V2 处理器

### 缓冲 HTTP（`mode: http`）

在传给 `pluginv2.Serve` 的 gRPC 服务 server 上实现
`InvokeRoute(ctx, *RouteRequest) (*RouteResponse, error)`。宿主把入站 HTTP
请求整理为 `RouteRequest`：

| 字段 | 内容 |
| --- | --- |
| `RouteId` | 命中的 manifest 路由 `id`（按它做 switch）。 |
| `ContractVersion` | 命中路由的契约版本。 |
| `Method` / `Path` | HTTP 方法与（规范）路径。 |
| `Headers` | 宿主传输看到的请求头。在 `filtered` 权威模式（默认）下，凭证头（`cookie`、`authorization`、`x-api-key`、`x-auth-token`）、`x-sforum-*` 头、`host`/`content-length`/`x-csrf-token` 及连接逐跳头会在插件看到前被剥离；只有 `raw` 请求模式会转发（单独声明的高风险权力）。 |
| `PathParameters` | 从 `:name` 段捕获的值。 |
| `QueryParameters` / `QueryParameterValues` | 查询字符串值。 |
| `Body` | `TypedDocument`（schema id + 版本 + 值），当请求携带 JSON 文档时存在；进入你的处理器前已按 `requestSchema` 校验。 |
| `Context` | `RequestContext`：`RequestId`、`Trace`、`Actor`、`Locale`、`Deadline`、`Extension`、`GrantedAuthority`、`IdempotencyKey`，以及已认证调用委托（`HostCommandDelegations`、`HostQueryDelegations`）。 |
| `RequestAuthorityMode` | `filtered`（默认）或 `raw`（raw 请求所有权是声明的高风险权力）。 |
| `GuardKind` | 放行本请求的 guard。 |
| `RouteAction` / `InvocationStage` | 链中本步的 action 与阶段。 |
| `MutableRequestFields` / `MutableResponseFields` | 本路由冻结的 RFC 6901 修补 allowlist。 |
| `PriorResponse` | 链式执行时上一步的响应。 |

返回 `RouteResponse`：

| 字段 | 内容 |
| --- | --- |
| `StatusCode` | 要发送的 HTTP 状态码。 |
| `Headers` | 响应头（宿主应用 allowlist）。 |
| `Body` | 用 `pluginv2.NewTypedDocument(schemaRef, values)` 构造的 `TypedDocument`；声明了 `responseSchema` 时会被校验。 |
| `Error` | 结构化 `ErrorDetail{Code, Reason, Message}`——需要稳定机器可读错误时优先用它，而不是只给 HTTP 错误。 |
| `RequestPatch` / `ResponsePatch` | 限定在冻结 `MutableRequestFields`/`MutableResponseFields` allowlist 内的有序 RFC 6902 操作；宿主逐操作校验并应用。 |
| `StreamFollows` | 响应将继续以 route stream 形式输出时设置。 |

### 流式模式（`sse`、`websocket`、`stream`、`multipart`）

用 `WithRuntimeStreams` 安装流处理器并服务 `RouteStream`（`StreamRoute`）；
首帧是 `RouteStreamOpen`，携带与缓冲 `RouteRequest` 相同的身份字段，随后是
`DataChunk` 帧与收尾帧。宿主在处理器看到流之前完成认证。帧契约见
`apps/api/sdk/plugin/v2/streams_bidi.go` 与 stream dispatcher 测试。

---

## 限制与时机

- **缓冲响应大小：** 默认 8 MiB（`defaultRouteResponseLimit`）；更大响应失败
  并返回 `ErrRouteResponseTooLarge`。
- **RPC 超时：** 每步默认 3 秒；用声明里的 `timeoutMs` 覆盖。流式模式对
  open 应用相同的每步超时。
- **请求体：** API 全局的 `HTTP_BODY_LIMIT` 作用于入站请求；宿主在其内
  流式或缓冲。
- **Schema 校验：** 分发前按 `requestSchema` 校验请求体（处理器路由只要含
  不安全方法就必须声明）；响应按 `responseSchema` 校验（处理器路由必填）。
  校验要求恰好一个 JSON `Content-Type`。

---

## 从浏览器调用插件路由

1. 同源请求声明的路径（如 `/api/acme-notes`）。Nuxt server middleware 探测
   API，命中时把请求代理过去。
2. 会话 cookie 与 `csrf_` cookie 会被转发。不安全方法必须发送与 `csrf_`
   cookie 匹配的 `X-Csrf-Token`（double-submit），与 Core API 完全一致——
   Web 应用的 `useApiClient` 会自动处理。携带有效
   `Authorization: Bearer sft_…` PAT 的请求跳过 CSRF。
3. 探测无法证明命中时，`GET`/`HEAD` 回退到 Nuxt 渲染；不安全方法返回 `503`
   而不是到达另一个写入者。

脚本/测试等直连调用可以直接带相同路径、方法与请求头访问 API loopback 端口。

---

## 测试你的路由

- `extension validate` — manifest schema、includes、摘要与模板预检。
- `extension test` — host 契约检查（capabilities、events、contributions、
  providers、jobs、backend entry）。
- 参考包自带真实端到端集成测试：
  `apps/api/app/Support/Extensions/IntegrationTests/custom_content_reference_plugin_integration_test.go`
  把包作为子进程构建，发布注册表，并断言路由分发与禁用/移除回退。
- 开发环境手动冒烟：后台启用插件后

```bash
curl -i http://127.0.0.1:8080/api/acme-notes          # 直连 API
curl -i http://127.0.0.1:3000/api/acme-notes          # 走 Web 代理
```

没有插件声明的路径会表现为 404 / `miss`（或 Nuxt 页面）。

---

## 检查清单

1. 每条路由都有命名空间稳定的 `id` 与显式 `contractVersion`。
2. `path` 规范；`methods` 显式；`mode` 显式（默认 `http`）。
3. guard 选自关闭集合（`public`/`login`/`permission`/`guest`）。`inherit` 只在
   目标可解析时生效；`raw_request` 与自定义 guard 都已上线但属高风险：自定义
   guard 运行受信任门禁约束的精确制品插件代码，而 `raw_request` 不需要任何
   guard 代码却会转发凭据头。
4. 带文档的不安全请求都配 `requestSchema`；结构化响应配 `responseSchema`；
   两者都解析到 `packageFiles` 中 `kind: "schema"` 条目。
5. `fallback` 显式（默认 `closed`）；不安全方法永不回退。
6. 改动包内文件后运行 `extension digest --write`；重新校验；在后台重新启用
   （制品变化会使既有信任失效）。
7. 处理器绝不根据原始请求数据重新实现授权；依赖 guard +
   `RequestContext.Actor` + `RequestAuthorityMode`。

## 相关

- [构建与加载循环（中文版）](./build-and-load.md) · [英文原文](../../extensions/build-and-load.md)
- [插件编写指南（英文）](../../extensions/authoring-guide.md)
- [Manifest V3 目录（生成）](../../extensions/catalogs/manifest-v3.md)
- [核心路由目录（生成）](../../extensions/v3/catalogs/routes.md)
- 参考包：`extensions/fixtures/plugins/sforum-custom-content/`
