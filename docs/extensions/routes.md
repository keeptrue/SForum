# Plugin routes

> **Handbooks (bilingual):**  
> [中文 · 插件路由](../zh-CN/extensions/routes.md) ·
> [English · Plugin routes](../en-US/extensions/routes.md)

This page is the author-facing guide for **plugin-declared HTTP routes**: how to
declare them in Manifest V3, how to implement the handler with the Protocol V2
Go SDK, which guards and fallbacks exist, and how a request travels from the
browser to your plugin process.

It complements:

- [authoring-guide.md](./authoring-guide.md) — the general plugin authoring guide
- [build-and-load.md](./build-and-load.md) — compiling and loading your package
- [catalogs/manifest-v3.md](./catalogs/manifest-v3.md) — the generated Manifest V3 field catalog
- [v3/catalogs/routes.md](./v3/catalogs/routes.md) — the **core** Route Catalog (what Core claims, not how to declare your own)

> **Working reference:** `extensions/fixtures/plugins/sforum-custom-content/`
> is the complete route example: `routes[]` declarations in
> `sforum.extension.json.tmpl`, handler implementations in `backend/routes.go`,
> and package-file schema references under `schemas/`.

---

## How a plugin route works

1. The package declares a `routes[]` entry: a stable id, the HTTP path, the
   methods, a guard, a mode, and a fallback policy.
2. On enable, the Host Route Registry admits the declaration and publishes it
   into the versioned route registry. Conflicts with existing routes fail
   closed at admission time (inspectable in admin via the route inspector and
   route-provider conflict surfaces).
3. An incoming request first hits the API ingress, which builds an execution
   plan from the registry. If a plugin step matches, the dispatcher invokes
   your backend process (Protocol V2 `InvokeRoute` RPC, or a route stream for
   streaming modes) and streams the response back.
4. In the web app, `apps/web/server/middleware/plugin-route-proxy.ts` decides
   whether a same-origin path belongs to a plugin: it probes the API with a
   `HEAD` request carrying `X-SForum-Internal-Route-Probe: v1` and
   `X-SForum-Internal-Route-Method: <METHOD>`. The API answers `204` +
   `X-SForum-Internal-Route-Result: plugin` when a plugin route matches, or
   `404` + `miss` otherwise. Only matching paths are proxied to the API; all
   other paths continue to Nuxt rendering.

Reserved paths are never proxied to the API:

- `/api/v1` and `/api/v1/**` (Core API is the authoritative upstream)
- `/_sforum/**`, `/_nuxt/**`
- `/media/avatars/**`, `/media/attachments/**`

If the probe itself fails (API unavailable), `GET`/`HEAD` fall back to normal
Nuxt rendering; unsafe methods fail with `503` because they must not land on a
second potential writer.

---

## Minimal example

Manifest (`sforum.extension.json`):

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

Backend (`backend/plugin.go`) — implement the `InvokeRoute` RPC on your server:

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
	// The Host already validated the body against createRequestSchema.
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
	// Echo the request identity back into the response. The Host requires a
	// ResponseContext and matches RequestId/Trace back to the invocation.
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

Build, digest, validate, and load it (full loop in
[build-and-load.md](./build-and-load.md)):

```bash
cd apps/api
go run ./cmd/sforum extension digest --write <package-root>
go run ./cmd/sforum extension validate <package-root>
go run ./cmd/sforum extension test <package-root>
```

---

## Manifest declaration reference

`routes` is an array; each item requires `id`, `contractVersion`, `action`,
`path`, `methods`, `fallback`, and `mode`. The authoritative shape is the
embedded Draft 2020-12 schema (`apps/api/app/Support/ExtensionManifest/schemas/manifest-v3.schema.json`).

| Field | Meaning |
| --- | --- |
| `id` | Stable namespaced route id (`[a-z0-9][a-z0-9._-]{1,80}`), e.g. `acme.notes.route.list`. This id is what the runtime sends as `RouteId` in `InvokeRoute`; switch on it. |
| `contractVersion` | Versioned contract, e.g. `acme.notes.route.list@1`. Every change to the route contract must bump the version. |
| `action` | How the route joins the registry: `add` (new route), `alias`, `redirect`, `rewrite`, `before`, `after`, `filter`, `wrap`, `replace` (with `targetId`), `global_middleware`. Default `add`. |
| `targetId` | The stable route id this action targets (required for targeting actions such as `replace`). |
| `path` | Canonical HTTP path: static segments, `:name` parameters, and a final `*name` catch-all (`*` alone names the segment `path`). Paths must be canonical (no `..`, no `//`). |
| `methods` | HTTP methods for this declaration. |
| `guard` | Guard id: `core.guard.public`, `core.guard.login`, `core.guard.permission` (plus `permission`), `core.guard.guest`, `core.guard.inherit`, or a custom guard id (a package-declared `kind: raw_request` guard is the only route to raw request authority; `core.guard.raw_request` is refused). See [Guards](#guards). |
| `access` | Convenience shorthand mapped to core guards: `public` → `core.guard.public`, `login` → `core.guard.login`, `permission` → `core.guard.permission` (plus `permission`). Ignored when `guard` is set. |
| `permission` | Permission key required by `core.guard.permission` (or `access: "permission"`). |
| `priority` | Ordering hint for overlapping declarations (integer). |
| `fallback` | Failure policy: `closed` (default — request fails closed), `not_found`, `readonly_core`. `not_found`/`readonly_core` only ever fall back on `GET`/`HEAD` while no response or side effect has begun. |
| `mode` | Transport mode: `http` (default), `sse`, `websocket`, `stream`, `multipart`. |
| `destination` | Target for `redirect`/`rewrite` actions. |
| `statusCode` | Redirect status: `301` or `308` (default `308`). |
| `handler` | Your handler name (any stable string; the runtime identifies the route by `id`, not by this field). |
| `requestSchema` | Schema reference validated against the request body. **Required** whenever the route declares any unsafe method (`POST`/`PUT`/`PATCH`/`DELETE`); preflight rejects routes without a valid reference. |
| `responseSchema` | Schema reference validated against structured HTTP responses. **Required** for handler routes (`add`/`alias`/`wrap`/…); preflight rejects routes without a valid reference. |
| `mutableRequestFields` / `mutableResponseFields` | RFC 6901 paths the handler may patch via `RequestPatch`/`ResponsePatch`; the Host validates every operation against the frozen allowlist. |
| `timeoutMs` | Per-step RPC timeout. The dispatcher default is 3 seconds when omitted. |

Schema references are either a versioned schema id (`name@N`, resolved through
the package's schema catalog) or a package-relative `schemas/foo.json` path.
Both must resolve to a declared `packageFiles` entry of kind `schema` with the
exact digest; a missing catalog entry rejects the declaration instead of
treating the id as prose.

### Route actions

| Action | Meaning |
| --- | --- |
| `add` | Claim a new path+method pair. |
| `alias` | Serve the same handler under an additional path. |
| `redirect` | Respond `301`/`308` to `destination`. |
| `rewrite` | Internally rewrite the request path to `destination` and continue matching. |
| `before` / `after` | Chain around a targeted route (see `targetId`). |
| `filter` | Inspect/modify a targeted route's request/response documents. |
| `wrap` | Wrap a targeted route's execution. |
| `replace` | Replace a targeted route (core or plugin) — high-risk, requires explicit declaration and trust confirmation. |
| `global_middleware` | Run for every registry-managed path. |

Replacement and chaining targets are declared, never inferred. Core route ids
come from the generated [core Route Catalog](./v3/catalogs/routes.md). The
admin route-provider selection and conflict surfaces
(`/api/v1/admin/extensions/route-providers/*`) expose which provider currently
owns each path; conflicts are rejected at admission.

---

## Guards

| Guard id | Allowed when | Notes |
| --- | --- | --- |
| `core.guard.public` | Everyone, no auth required | The common choice for public read endpoints. |
| `core.guard.login` | Any active authenticated user | |
| `core.guard.permission` | Authenticated user holding `permission` | |
| `core.guard.guest` | Guests only (not authenticated) | |
| `core.guard.inherit` | Targeting actions without an explicit guard | Inherits the targeted route's guard; closed until the target is resolvable. |
| `core.guard.raw_request` | **Rejected** | **Removed 2026-10-16.** It used to allow a route with an *empty* plugin guard binding — no guard code at all — and grant raw request authority, forwarding `cookie`, `authorization`, `x-api-key` and `x-auth-token` to your handler. That was judged a vulnerability and is refused at manifest validation. Declare your own guard with `kind: raw_request` (`entry` + `digest` + `contractVersion`) and reference it when you genuinely need raw authority; the Host discloses it at trust time. |
| custom guard ids | Only through the trusted guard runtime | Guard binaries are declared via the `guards[]` family (`kind: custom` / `kind: raw_request`, exact `entry` + `digest`, optional `permissions`). The executable guard runtime **is live in production**: it is injected by `bootstrap/api_assembly_domains.go` and dispatched by `Support/Routes/core_guard_authorizer.go`. |

For ordinary routes use `core.guard.public` and `core.guard.login`. The Host
performs the guard check *before* invoking your handler, and passes the
resolved authority in the RPC context; your handler never re-implements login
or permission checks from raw request data.

### Custom guards are live, and gated exactly like other plugin code

A `guards[]` declaration with `kind: custom` resolves to a plugin handler that
runs over Protocol V2 (`InvokeRoute` with the guard id as the route id) and must
answer allow/deny with no body. Before that call the Host requires all of:

- an exact route/guard binding (declared guard id, non-empty contract version,
  and an artifact that matches the plan step);
- a runtime instance for the exact artifact — extension id and plugin type,
  `enabled` status, the declared version, and the exact package digest;
- that the instance is not in safe mode, and that the exact artifact is trusted
  whenever trust is required or raw authority is requested;
- a live runtime admission lease for that instance.

Any failure — no evaluator, an invalid binding, a missing/untrusted/safe-mode
runtime, or a guard-side error — denies the request. There is no fallback that
runs the handler unguarded.

Three properties are **validated and disclosed but not enforced or decided**,
so do not rely on them:

- `permissions[]` on a guard declaration is part of the trust disclosure, but
  the Host does not use it for enforcement; the guard owns that decision.
- A `challenge` outcome (redirect/step-up) does not exist in the wire contract.
  A guard can only allow or deny.
- `core.guard.raw_request` **no longer exists as a usable guard** (removed
  2026-10-16). It handed raw request authority to a route **without any guard code
  running**; the operator judged that a vulnerability, so manifest validation now
  refuses the id and the runtime fails closed for it. Raw authority is only
  available through a package-declared guard with `kind: raw_request`, which the
  Host discloses as high risk during exact-artifact trust confirmation.

---

## The Protocol V2 handler

### Buffered HTTP (`mode: http`)

Implement `InvokeRoute(ctx, *RouteRequest) (*RouteResponse, error)` on the
gRPC service server passed to `pluginv2.Serve`. The Host marshals the incoming
HTTP request into a `RouteRequest`:

| Field | Contents |
| --- | --- |
| `RouteId` | The manifest route `id` that matched (switch on this). |
| `ContractVersion` | The matched route's contract version. |
| `Method` / `Path` | The HTTP method and (canonical) path. |
| `Headers` | Request headers as seen by the Host transport. In `filtered` authority mode (default) credential headers (`cookie`, `authorization`, `x-api-key`, `x-auth-token`), `x-sforum-*` headers, `host`/`content-length`/`x-csrf-token`, and connection-hop headers are stripped before the plugin sees them; only `raw` request mode forwards them (a separately declared high-risk power). |
| `PathParameters` | Values captured from `:name` segments. |
| `QueryParameters` / `QueryParameterValues` | Query string values. |
| `Body` | `TypedDocument` (schema id + version + value) when the request carried a JSON document; validated against `requestSchema` before your handler runs. |
| `Context` | `RequestContext`: `RequestId`, `Trace`, `Actor`, `Locale`, `Deadline`, `Extension`, `GrantedAuthority`, `IdempotencyKey`, plus authenticated invocation delegations (`HostCommandDelegations`, `HostQueryDelegations`). |
| `RequestAuthorityMode` | `filtered` (default) or `raw` (raw request ownership is a declared high-risk power). |
| `GuardKind` | Which guard admitted the request. |
| `RouteAction` / `InvocationStage` | Action and stage of this step in the chain. |
| `MutableRequestFields` / `MutableResponseFields` | The frozen RFC 6901 patch allowlists for this route. |
| `PriorResponse` | Prior step's response when chaining. |

Return a `RouteResponse`:

| Field | Contents |
| --- | --- |
| `StatusCode` | HTTP status to send. |
| `Headers` | Response headers (Host applies an allowlist). |
| `Body` | `TypedDocument` built with `pluginv2.NewTypedDocument(schemaRef, values)`; validated against `responseSchema` when declared. |
| `Error` | Structured `ErrorDetail{Code, Reason, Message}` — prefer this over an HTTP-only error for stable machine-readable failures. |
| `RequestPatch` / `ResponsePatch` | Ordered RFC 6902 operations limited to the frozen `MutableRequestFields`/`MutableResponseFields` allowlists; the Host validates and applies each one. |
| `StreamFollows` | Set when the response continues as a route stream. |

### Streaming modes (`sse`, `websocket`, `stream`, `multipart`)

Install stream handlers with `WithRuntimeStreams` and serve the
`RouteStream` (`StreamRoute`); the first frame is a `RouteStreamOpen`
carrying the same identity fields as a buffered `RouteRequest`, followed by
`DataChunk` frames and a closing frame. The Host authenticates the stream
before your handler sees it. See `apps/api/sdk/plugin/v2/streams_bidi.go`
and the stream dispatcher tests for the frame contract.

---

## Limits and timing

- **Buffered response size:** 8 MiB default (`defaultRouteResponseLimit`); a
  larger response fails with `ErrRouteResponseTooLarge`.
- **RPC timeout:** 3 s default per step; override with `timeoutMs` in the
  declaration. Stream modes apply the same per-step timeout to the open.
- **Request body:** the API-wide `HTTP_BODY_LIMIT` applies to incoming
  requests; the Host streams or buffers within it.
- **Schema validation:** request bodies are validated against `requestSchema`
  before dispatch (handler routes with any unsafe method must declare one);
  responses are validated against `responseSchema` (required for handler
  routes). Validation requires exactly one JSON `Content-Type`.

---

## Calling a plugin route from the browser

1. Fetch the declared path same-origin (e.g. `/api/acme-notes`). The Nuxt
   server middleware probes the API and proxies the request when the route
   matches.
2. Session cookies and the `csrf_` cookie are forwarded. Unsafe methods must
   send `X-Csrf-Token` matching the `csrf_` cookie (double-submit), exactly
   like Core API calls — the web app's `useApiClient` does this automatically.
   Requests carrying a valid `Authorization: Bearer sft_…` PAT skip CSRF.
3. `GET`/`HEAD` requests fall back to Nuxt rendering when the probe cannot
   prove a match; unsafe methods return `503` instead of reaching another
   writer.

Direct API calls (scripts, tests) may hit the API loopback port directly with
the same path, method, and headers.

---

## Testing your routes

- `extension validate` — manifest schema, includes, digests, and template
  preflight.
- `extension test` — host contract checks (capabilities, events,
  contributions, providers, jobs, backend entry).
- The reference package ships a real end-to-end integration test:
  `apps/api/app/Support/Extensions/IntegrationTests/custom_content_reference_plugin_integration_test.go`
  builds the package as a subprocess, publishes the registries, and asserts
  route dispatch plus disable/remove fallback.
- Manual smoke test in dev: enable the plugin in admin, then

```bash
curl -i http://127.0.0.1:8080/api/acme-notes          # direct API
curl -i http://127.0.0.1:3000/api/acme-notes          # through the web proxy
```

Expect `404` + `miss`-style behavior (or a Nuxt page) for paths no plugin
declares.

---

## Checklist

1. Namespaced stable `id` + explicit `contractVersion` per route.
2. Canonical `path`; `methods` explicit; `mode` explicit (`http` default).
3. Guard chosen from the closed set (`public`/`login`/`permission`/`guest`).
   `inherit` resolves only when the target is resolvable; `raw_request` and
   custom guards are live but high-risk — custom guards run exact-artifact,
   trust-gated plugin code, and `raw_request` needs no guard code at all while
   forwarding credential headers.
4. `requestSchema` on every unsafe document-bearing request;
   `responseSchema` on structured responses; both resolve to `packageFiles`
   kind `schema` entries.
5. `fallback` explicit (`closed` default); unsafe methods never fall back.
6. Run `extension digest --write` after touching package files; re-validate;
   re-enable through admin (artifact changes invalidate existing trust).
7. Handlers never re-implement authz from raw request data; rely on the
   guard + `RequestContext.Actor` + `RequestAuthorityMode`.

## Related

- [Build, digest, and load](./build-and-load.md)
- [authoring-guide.md](./authoring-guide.md)
- [Manifest V3 catalog](./catalogs/manifest-v3.md)
- [Core Route Catalog](./v3/catalogs/routes.md)
- Fixture: `extensions/fixtures/plugins/sforum-custom-content/`
