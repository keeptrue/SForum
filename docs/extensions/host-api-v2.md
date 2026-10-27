# Host API v2 and non-Go runtimes

Host API v2 is SForum's typed backend plugin protocol. The wire schema is
language-neutral Protobuf, but process startup is also part of the contract: a
plugin is not compatible merely because it implements the generated gRPC
services. It must preserve the HashiCorp go-plugin handshake, AutoMTLS, broker,
exact-artifact identity, runtime-token, authority, and resource-limit rules in
this document.

The Go SDK is the first supported SDK. A non-Go implementation should use a
small Go launcher based on that SDK unless it also has a tested, pinned
implementation of the go-plugin v1.8.0 core protocol. SForum does not currently
publish a stable non-Go launcher or promise compatibility with an independently
reimplemented go-plugin control plane.

## Authoritative contracts

The source schemas are split into three versioned packages:

| Package | Direction and ownership |
| --- | --- |
| `sforum.protocol.v2` | Shared handshake, identity, request/response context, limits, health, readiness, errors, and progress |
| `sforum.plugin.v2` | Host-to-plugin runtime, route, hook, file, job, lifecycle, provider, and service calls |
| `sforum.host.v2` | Plugin-to-host query/command, database, cache, job, schedule, discovery, secret, file, HTTP, admin, identity, permission, media, navigation, audit, and tracing calls |

The `.proto` files under `contracts/proto/sforum` are authoritative. Generated
Go files under `apps/api/sdk/plugin/v2/gen` are build products, not a second
schema source. Handwritten policy and SDK behavior live outside `gen`.

Generate and verify the Go SDK from the repository root:

```bash
./scripts/proto.sh generate
./scripts/proto.sh check
```

The script builds the pinned Buf and official Go generators from
`tools/proto/go.mod`; no system `protoc` or remote generation service is needed.
`check` runs Buf lint, regenerates from a clean output directory, and fails when
the checked-in Go SDK differs.

For another language, pin the official Protobuf and gRPC generators in that
language's own build, generate all files below `contracts/proto`, and preserve
the package and fully qualified service names exactly. Do not translate field
names into a handwritten JSON/HTTP protocol. The generated messages still need
the process and trust integration described below.

## Manifest selection and no downgrade

A v2 backend declares the exact pair:

```json
{
  "backend": {
    "entry": "backend/plugin",
    "rpc": "hashicorp-go-plugin",
    "protocolVersion": 2,
    "hostApiVersion": "sforum.host@2"
  }
}
```

Executable packages must select the current contracts explicitly:

| Identifier | Meaning |
| --- | --- |
| `sforum.host@2` | Manifest V3 `backend.hostApiVersion` |
| `sforum.host-api@2` | Accepted Manifest alias for the same V2 contract; new packages should use `sforum.host@2` |
| `sforum.host/v2` | Wire value in `HandshakeRequest.host_api_version` and generated SDK constants |

The `sforum.host-api@2` alias does not enable Protocol V1 or an older Host API.
The host maps either Manifest identifier to the canonical wire
`host_api_version`; a plugin handshake must select `sforum.host/v2`. Only gRPC
Protocol V2 is accepted. Installation or startup fails on a missing or invalid
Manifest V3 declaration, any `protocolVersion` other than `2`, an unsupported
Host API contract, or an incompatible handshake response. There is no runtime
downgrade or legacy transport fallback.

## Go runtime

Embed the default v2 server, implement only the generated methods the plugin
owns, and use the explicit v2 entry point:

```go
package main

import (
  pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

type server struct {
  *pluginv2.Server
}

func main() {
  pluginv2.Serve(&server{Server: pluginv2.NewServer()})
}
```

`pluginv2.Server` validates the typed handshake, binds the exact runtime, and
provides health/readiness defaults. After a successful handshake,
`Server.Host()` exposes generated Host API clients. Build every plugin-to-host
request with `Host.RequestContext(parent)`; it copies actor, locale, and trace
context where present while replacing runtime-owned identity and authority and
clamping the deadline.

## Process handshake and AutoMTLS

The host starts only the executable declared and digest-bound by the Manifest.
HashiCorp go-plugin then supplies the process control plane:

1. The process must accept the magic cookie
   `SFORUM_PLUGIN=sforum-plugin-v1` and select application protocol version `2`
   from `PLUGIN_PROTOCOL_VERSIONS`.
2. Protocol v2 is gRPC-only. The first protocol line on stdout follows the
   go-plugin core format and reports core protocol `1`, application protocol
   `2`, listener network/address, transport `grpc`, and the AutoMTLS server
   certificate. Plugin logs belong on stderr; output before this line breaks
   startup.
3. The host enables go-plugin `AutoMTLS`. The plugin consumes the one-process
   client certificate in `PLUGIN_CLIENT_CERT`, creates a one-process server
   certificate, requires and verifies the client certificate, and returns the
   server leaf certificate in the protocol line. TLS 1.2 is the minimum. Plain
   gRPC is not a valid fallback.
4. The main gRPC server must expose the go-plugin controller, stdio, and
   bidirectional `plugin.GRPCBroker` control services as well as
   `sforum.plugin.v2.PluginRuntimeService`.
5. SForum sends a typed `HandshakeRequest`. The plugin must select
   `sforum.plugin` major `2`, minor `0`, accept `sforum.host/v2`, and reject an
   incomplete identity, token shorter than 32 bytes, or missing limits.

The process handshake and typed `Handshake` RPC are separate and both are
required. The magic cookie prevents accidental execution as an ordinary CLI;
it is not authentication. AutoMTLS authenticates this process connection, and
the typed runtime binding supplies the exact trust identity.

## Host build identity

`HandshakeRequest.host_build` carries the frozen Host build identity:

- `core_version` is the **only** compatibility authority (strict semver).
  Manifest `sforumVersion` constraints are evaluated against it by the Host.
- `build_version` is display and troubleshooting only (`dev`, `dev-abc12`).
  Never compare it to a constraint.

`pluginv2.Server` captures the value at the first successful handshake and
exposes it as `Server.HostBuild()`. The boolean is false before the handshake
and when the Host predates the field, so treat a missing identity as *unknown*
instead of guessing or hardcoding a version. A repeated handshake carrying a
different identity is rejected with `protocol.stale_runtime`, exactly like
token, artifact, or broker drift.

## Host broker and runtime token

The host allocates a runtime-scoped go-plugin `GRPCBroker` service before the
typed handshake and sends its nonzero `host_broker_id` in `HandshakeRequest`.
The plugin uses that id through the existing `plugin.GRPCBroker/StartStream`
control stream to dial the host-owned gRPC services over the same AutoMTLS trust
relationship. `host_broker_id` is an opaque per-process locator, not a network
port, credential, or reusable service id.

The handshake also carries a random `runtime_token`. Store it only in process
memory. Every unary call and stream opened toward the Host API must send the
raw token as the single binary gRPC metadata value:

```text
x-sforum-runtime-token-bin
```

Do not hex/base64 encode that metadata value, persist it, place it in a message
payload, command line, environment variable, URL, or log, or forward it to a
different process. The `-bin` metadata key carries the token bytes using the
language's gRPC binary-metadata API. The host compares a SHA-256 digest of the
received value in constant time and rejects missing, duplicate, or stale
tokens. A process restart receives a new token, broker id, runtime epoch, and
instance id; old channels must not reconnect.

## Exact request context

Every v2 request and each streaming open frame carries
`sforum.protocol.v2.RequestContext`. Its typed `ExtensionIdentity`,
`AuthorityGrant`, and `Actor` messages must remain generated wire values:

| Field | Rule |
| --- | --- |
| `request_id` | Required and unique within the runtime instance |
| `trace` | Propagate the host-provided trace; create a new child span rather than replacing correlation |
| `actor` | Propagate the authenticated actor supplied by the host; an autonomous plugin call has no user actor |
| `locale` | Propagate the call locale; use `und` only when none exists |
| `deadline` | Required, valid, and in the future; also set the native gRPC deadline |
| `extension` | Exact handshake `extension_id`, version, artifact digest, trust grant id, runtime epoch, and instance id |
| `granted_authority` | Exact ordered handshake disclosure; receiving or echoing it never creates authority |
| `idempotency_key` | Required where the invoked command contract requires idempotency |

For plugin-to-host calls, SForum compares `extension` and
`granted_authority` to its runtime binding before invoking a service. Do not
construct, reorder, add, or drop authority entries. Host service adapters still
perform the authoritative permission, resource, declaration, and policy check;
wire actor and authority fields are disclosure context, not bearer grants.

For host-to-plugin calls, bind the first accepted handshake identity in memory
and reject later health, readiness, and business requests carrying a different
runtime identity. A repeated handshake is valid only when token, identity, and
`host_broker_id` all match the existing binding.

## Route invocation contract

`InvokeRoute` executes one Host-selected stage of an immutable Route Registry
plan. The plugin must treat `route_action`, `invocation_stage`, schema refs, and
mutable-field lists as frozen inputs rather than selecting or widening them.
The accepted stage/action and response shapes are:

| `invocation_stage` | Route actions | Valid plugin result |
| --- | --- | --- |
| `ROUTE_INVOCATION_STAGE_HANDLER` | `add`, `replace` | A terminal status, headers, and typed body, or a validated `stream_follows` preflight. Patches, `prior_response`, and mutable-field lists are forbidden. |
| `ROUTE_INVOCATION_STAGE_REQUEST` | `global_middleware`, `before`, `filter`, `wrap` | `request_patch` only. Terminal status/headers/body, `stream_follows`, `prior_response`, and `response_patch` are forbidden. |
| `ROUTE_INVOCATION_STAGE_RESPONSE` | `filter`, `wrap`, `after` | `response_patch` only, against the Host-supplied `prior_response`. Terminal status/headers/body, `stream_follows`, and `request_patch` are forbidden. |

`mutable_request_fields` and `mutable_response_fields` are the exact ordered
RFC 6901 allowlists frozen with the route declaration. Every patch path must
match an entry byte-for-byte in the corresponding list; changing or reordering
the transmitted allowlist, patching an undeclared path, or returning the wrong
patch direction fails the invocation closed. The Host validates and applies
the ordered RFC 6902 `add`, `replace`, and `remove` subset authoritatively.
`Location` and `Link` are Host-owned response-mutation fields and cannot appear
in a response mutation allowlist. An `add` or `replace` terminal handler may
return `Location` as part of its complete response; declarative redirects and
response modifiers cannot author it. `Link` is stricter: plugin terminal and
streaming responses have the complete header removed. Canonical, preload,
pagination, and other link relations must use a versioned Host surface; route
alias, redirect, rewrite, and SEO policy remain the only canonical authorities.

For patch values, `RoutePatchOperation.value_json` is the authoritative wire
representation. `add` and `replace` require the bytes of one complete valid
JSON value, while `remove` requires `value_json` to be empty. Do not set the
deprecated `google.protobuf.Value value` field: the Host rejects any legacy
`value`, even when `value_json` is also present, because the legacy field can
round JSON integers through IEEE-754 doubles.

Queries have two compatible wire representations. Field 17
`query_parameter_values` is lossless: the Host sorts entries by key and
preserves every key's value order, including empty-string values. Legacy field
8 `query_parameters` remains populated with each key's first value. A
legacy-only query is emitted in both forms; when both forms are supplied, their
first values must match, and a key with no values is invalid.

An unsafe buffered HTTP operation that declares `required.24h@1` may compose
with request modifiers. The Host records every request stage, including an
empty patch, as an encrypted, plan-bound transcript. A replay evaluates the
current guard and request schema, reapplies only the previously Host-validated
patches under the current allowlists, and returns the stored response without
invoking any modifier or handler plugin again. The order of repeated query
values is part of the request fingerprint.

Credential-bearing request fields (`Cookie`, `Authorization`, proxy
authorization, API/auth/CSRF tokens, and `Idempotency-Key`) cannot be mutable in
a required-replay chain. Lifecycle publication and the runtime Dispatcher both
reject that combination before plugin execution. Mutation transcripts use a
dedicated HKDF-derived AES-256-GCM key rooted in `APP_OPTION_ENC_KEY`; plaintext
patch values are never stored in the replay record. Rotating that key makes
existing encrypted mutation transcripts fail closed until their 24-hour TTL
expires or the records are removed. The Host never re-executes plugins to
recover them. Response-only V1/V2 replay records contain no mutation transcript
and remain readable across this key rotation.

The request `path` is the immutable normalized transport path selected by the
execution plan. A Host-authorized `/params/*` mutation changes only subsequent
`path_parameters`; it never rewrites `path` or triggers route selection again.
Plugin handlers may therefore receive the original `path` with a different,
Host-proven parameter value. Core, alias, redirect, and rewrite terminals reject
parameter mutation, and the private Host provenance bit is never sent on wire.

Streaming composition is not available. `stream_follows` is valid only for an
exact handler-stage plugin route and cannot accompany a buffered body. The Host
rejects composed streaming chains and rejects terminal or streaming fields from
request/response mutation stages, so `before`, `filter`, `wrap`, `after`, and
global middleware cannot be composed around a stream.

## Consent-gated reads (`users.email.read`)

Sensitive identity fields are **not** part of the stable core views or the
public HostQuery catalog. Reading the signed-in actor's own primary email goes
through one capability- and actor-gated query instead.

### When the Host delivers a delegation

On a Host-initiated **route or admin-surface invocation** where the request has
a live actor and the exact artifact declares the `users.email.read`
capability, the Host mints one single-use query actor delegation and puts it on
the invocation's `RequestContext.host_query_delegations`. The delegation:

- is bound to the exact runtime identity (extension id/version, artifact
  digest, trust grant id, runtime epoch, instance id) and to the query id,
  contract version, plan version, and result schema;
- carries `scope = "route"` and has a short (two-minute) lifetime;
- is delivered for that one invocation only and is retired when the invocation
  ends.

Anonymous callers, jobs/schedules/queue calls, and artifacts that do not
declare the capability receive no delegation at all. The Host never hands out a
delegation "so the query can reject it later". Plugin-to-Host calls still send
`actor: null`; there is no request field for a user id anywhere on this path.

### Query contract

| Field | Value |
| --- | --- |
| Query id | `core.query.identity.self_email` |
| Contract version | `core.query.identity.self_email@1` |
| Plan version | `core.query.identity.self_email.plan@1` |
| Result schema | `sforum.core.self_email@1` |
| Fields | `email` (string), `email_verified` (bool), `updated_at` (string, RFC3339Nano UTC) |
| Filters / sorts | none (the actor is bound by the token, not by a parameter) |
| Pagination | none; send `page.limit = 1` or omit `page` |
| Cache | never cached |

Rows are always the actor's own row. A disabled/inactive account or an empty
email returns an empty row set (`rows: []`) rather than an error. The email is
returned in its stored display form; the uniqueness/login-match column is never
exposed.

### SDK usage

```go
host, err := s.Host()
request, err := host.SelfEmailQueryRequest(incomingContext) // matches the delivered delegation
response, err := host.Queries.Execute(ctx, request)
// response.rows[0] = {email, email_verified, updated_at}
```

`SelfEmailQueryRequest` fails closed with `pluginsdk.ErrHostQueryDelegationUnavailable`
when the current invocation carries no matching delegation, so a plugin that
also serves anonymous traffic degrades instead of fabricating data. If you need
to attach fields or pagination yourself, `Host.DelegatedQueryRequest` selects
the same token explicitly.

### Enforcement and failure semantics

Every successful read appends one `identity.email.consent.read` audit event
(actor, extension, artifact, query id, request id, row count — never a value),
and reads are limited to 60 per minute per (actor, extension). Traces and slow
query logs record only the query id, plan version, shape digest, row count,
duration, and outcome. Error reasons:

| Reason | Code | Meaning |
| --- | --- | --- |
| `host.query_actor_delegation_invalid` | `UNAUTHENTICATED` | Missing, expired, forged, or differently bound delegation (also returned by the actorless stable query id) |
| `host.query_actor_delegation_replayed` | `CONFLICT` | The single-use token was already consumed |
| `host.query_capability_denied` | `PERMISSION_DENIED` | The exact artifact does not declare `users.email.read` |
| `host.query_rate_limited` | `RATE_LIMITED` | The per-(actor, extension) consent quota was reached |
| `host.query_runtime_stale` | `FAILED_PRECONDITION` / `STALE_RUNTIME` | The exact artifact or trust grant is no longer active |
| `host.query_replay_ledger_unavailable` | `UNAVAILABLE` | The replay ledger reached its safe bound |
| `host.query_consent_gate_unavailable` | `UNAVAILABLE` | Capability/audit/limiter dependencies are not configured |

Calling the stable id `sforum.core.identity.self_email` directly is always
refused with `host.query_actor_delegation_invalid`: the delegated Query
Registry projection above is the only executable outlet for this definition.

## Declared secrets (`SecretService`)

A secret setting is a manifest field with `type: "secret"`. Its plaintext lives in
the Secret Store; the settings document keeps only a `sforum.secret://` reference
and a `secretSet` flag, which is why the admin settings page can show "configured"
without ever holding the value.

`SecretService` is the only runtime surface for that store:

| RPC | Effect |
| --- | --- |
| `Resolve(secret_id, purpose)` | Returns a short-lived lease with the decrypted value (existing read half) |
| `Put(secret_id, value, media_type)` | Encrypts and stores one declared secret field, preserving every other field |
| `Clear(secret_id)` | Removes one declared secret field, preserving every other field |

Both write RPCs require:

1. a broker-attested exact runtime (the same identity check as `Resolve`);
2. the `settings.own` capability — the same grant that lets the plugin read its
   own settings document, because writing a credential is the same power;
3. a `secret_id` that is a **bare** settings key the manifest declares as
   `type: "secret"`. The `sforum.secret://namespace/id` form is refused: a
   runtime may only address its own document, never another extension's
   namespace;
4. `value` at most 64 KiB (`SecretStore` plaintext ceiling). `Put` with an empty
   value is refused — use `Clear`, so a truncated request body cannot silently
   delete a working credential.

Writes go through the settings lifecycle, so they carry a settings document
revision: the response reports `secret_set` and `version`. The write is a
compare-and-set on that revision, and `version` lets a plugin notice that an
operator saved the settings page in between. `media_type` is advisory metadata
(defaults to `text/plain`) and is never logged.

Audit records the namespace, the settings key, the actor, and the revision — never
the value, the lease, or the reference. `Clear` removes the settings document
reference (the ciphertext history stays in the Secret Store, exactly like an
operator emptying the field on the settings page). The plugin sees the plaintext
again only through its own runtime settings (`settings.own`), as if an operator
had typed it in.

Failure reasons:

| Reason | Code | Meaning |
| --- | --- | --- |
| `host.runtime_unattested` | `PERMISSION_DENIED` | The call is not bound to an exact runtime identity |
| `host.runtime_stale` | `STALE_RUNTIME` | The claimed extension identity does not match the attested runtime |
| `host.capability_unavailable` | `UNAVAILABLE` | The Host capability resolver is not configured or failed |
| `host.secret_denied` | `PERMISSION_DENIED` | The exact artifact does not hold `settings.own` |
| `host.secret_field_not_declared` | `INVALID_ARGUMENT` | The manifest does not declare this key as a secret setting |
| `host.secret_request_invalid` | `INVALID_ARGUMENT` | Empty or namespaced `secret_id`, empty `Put` value, or a value over 64 KiB |
| `host.secret_settings_unavailable` | `FAILED_PRECONDITION` | The extension has no registered settings schema |
| `host.settings_conflict` | `CONFLICT` | The settings document changed underneath the write; retry once (`retryable: true`) |
| `host.secret_unavailable` | `INVALID_ARGUMENT` | Secret Store or settings lifecycle is not wired |

There is deliberately no API for arbitrary secrets, for rotating a secret
without a value, or for reading another extension's secret: those are Tier 3
questions and would each need their own grant.

## Limits, deadlines, and cancellation

### Notification emission command

`notifications.emit@1` is an actorless Host Command for one exact plugin
artifact's declared notification types. The Go SDK exposes
`NotificationEmitRequest` and `EmitNotification`; no protobuf service addition
is needed because the command uses `HostCommandService` and typed documents.

The Host binds the call to the authenticated artifact, grant, epoch, instance,
locale, deadline, and trace. It then validates namespace ownership, active type
and payload versions, the exact schema file/digest and value, a maximum of 50
explicit active recipients, the declared target, a 16 KiB payload, idempotency,
effective recipient policy, and 60 committed requests per rolling minute.
Plugin actor/session evidence is rejected before dispatch. Bulk broadcast and
raw Core notification table access are absent. Audits contain type identity,
sizes/counts, and stable reasons only; payload values and recipient ids are not
logged.

Current host-owned defaults are part of the compatibility test surface:

| Limit | Current value |
| --- | --- |
| Maximum gRPC send or receive message | 16 MiB (`16777216` bytes) — must stay at or above the Host AI input budget (12 MiB), so a single completion request always fits one transport frame |
| Active calls per gRPC server | 16 across unary calls and streams |
| Default RPC deadline | 5 seconds |
| Handshake/health deadline | 5 seconds |

The host advertises limits in `HandshakeRequest.limits`; a non-Go runtime must
apply the lower of its own reviewed limit and the advertised host limit. Apply
limits to the main plugin server, the Host broker client, and every stream
message, not only unary calls. Large files and HTTP bodies use bounded
`DataChunk` streams rather than one oversized message.

Honor a caller's shorter native gRPC deadline. If no deadline is present, add
the advertised default; never extend a deadline beyond the host default.
Propagate cancellation to work, broker calls, and stream producers, release the
concurrency slot when a call ends, and stop sending after cancellation. Map
expected failures to gRPC status plus the stable `ErrorDetail` contract where
the response defines it.

### Host Command prerequisite: the `host_commands` authority grant

Any plugin that calls a Host Command — `notifications.emit@1`,
`sforum.ai.complete@1`, `sforum.events.emit@1`, and the rest — must declare the
`host_commands` database grant in its manifest:

```json
"database": {
  "contractVersion": "vendor.plugin.database@1",
  "grants": ["host_commands"],
  "backup": { "required": false, "strategy": "pg_dump" },
  "retention": { "onDisable": "retain", "onUninstall": "retain" }
}
```

The declaration is what the exact-artifact trust review approves, and the
reviewed impact document must match it exactly: the Host compares the manifest's
`database.grants` with the live grant's recorded powers and refuses when they
differ. Without the grant, every command call fails with
`host.command_authority_denied` before the command body runs — the plugin's code
never executes, the gateway is never entered, and nothing is billed.

### AI completion command

`sforum.ai.complete@1` is the only way a plugin requests an AI completion. The
Go SDK exposes `AICompletionRequest` and `Complete`. It requires the `ai.invoke`
process capability and runs under actorless service authority.

A plugin declares a **purpose** and a **cost class** (`economy`, `standard`, or
`premium`); the Host resolves which provider profile serves that cost class from
site configuration. There is no profile, model, or endpoint field: letting a
plugin address a profile would move the final decision on cost and quality out
of the operator's console.

The Host sets `Metadata.CallerExtensionID` from the verified runtime identity,
never from request input, so per-extension quota and usage always attribute to
the caller that actually ran. `subjectUserId` is accepted only against a
Host-attested delegated actor for the same user; this command is service-mode
and therefore rejects it today. A plugin cannot spend an arbitrary member's
daily quota.

Every call passes through the site switch, the monthly budget, the site rate
limit, and the per-extension daily quota before any outbound request exists, and
each attempt — including failures — writes an execution trace. Stable failure
reasons are `ai.gateway_disabled`, `ai.purpose_invalid`,
`ai.cost_class_invalid`, `ai.profile_unavailable`, `ai.credential_missing`,
`ai.request_invalid`, `ai.quota_exceeded`, `ai.budget_exceeded`,
`ai.rate_limited`, `ai.provider_failed`, and `ai.unsafe_target`, plus the
assembly-time `ai.gateway_unavailable` (the Host AI gateway is not wired in this
process role).

Result integers (token counts, `configRevision`, `latencyMs`) are encoded as
decimal strings because protobuf `Struct` holds only doubles and would lose
64-bit precision. `Plan` output carries purpose, cost class, and sizes only —
prompt text and message content never enter the plan, the impact summary, or the
audit record.

The command deadline is the standard Host RPC deadline. A slow model can exhaust
it; a plugin doing long AI work should enqueue a job (preferably on the `ai`
queue, see the authoring guide) rather than block a request-path call.

Prompt content is subject to the site's outbound redaction settings (AI console →
内容脱敏): when enabled, the Host replaces recognisable email addresses, phone
numbers, and IP addresses in `system`, message text, and tool-call arguments with
explicit placeholders (`[redacted-email]`, `[redacted-phone]`, `[redacted-ip]`)
before the request reaches the provider, and the result cache is keyed on the
redacted body. Do not rely on prompt text round-tripping byte-for-byte, and do not
put secrets in a prompt expecting the site to hide them — redaction covers
recognisable personal data, not credentials.

Completions are also cached for a short TTL. The cache key covers the caller
extension, purpose, profile, model, prompt version, config revision, tools, and the
redacted request body, so two calls share an answer only when the provider would
have received the same bytes. A served-from-cache result carries
`cacheHit: true`; it consumed no quota and no budget, and it is not a fresh
provider call — do not use a completion as a liveness probe for the vendor.

### AI tool contributions (plugins as tool providers)

A plugin can contribute tools that the Host's AI completion loop may call. The
declaration is static: it lives in the manifest, so the Host knows which tools
exist before a plugin process starts, and a tool list cannot change in the
middle of one completion.

```json
{
  "type": "plugin",
  "backend": { "entry": "backend/plugin", "protocolVersion": 2 },
  "aiTools": [
    {
      "id": "vendor.plugin.order_lookup",
      "contractVersion": "vendor.plugin.order_lookup@1",
      "name": "vendor-order-lookup",
      "description": "Look up an order by its public number.",
      "descriptionI18n": { "zh-CN": "按公开订单号查询一条订单。" },
      "parameters": { "type": "object", "properties": { "orderNumber": { "type": "string" } }, "required": ["orderNumber"] },
      "handler": "vendor.plugin.order_lookup",
      "timeoutMs": 1000,
      "context": ["locale", "viewer"]
    }
  ]
}
```

Declaration rules (manifest validation rejects anything else):

- `aiTools[]` is plugin-only; it requires `backend.entry` and protocol version 2.
  At most 16 declarations per plugin.
- `name` is what the model sees: `[a-z0-9_-]`, no dot, at most 64 bytes, and
  unique inside the plugin. `id` is the stable dispatch identity and must be a
  versioned id in the plugin's own namespace, with
  `contractVersion == id + "@1"`.
- `description` is the only text the model reads, and it is rendered into the
  system prompt byte-for-byte. `descriptionI18n` is optional and console-only:
  a locale map (at most 8 locales, 1024 bytes per value) the AI console shows to
  operators. It never enters a model request, the result cache key, or the
  prompt version, so translating it cannot change what the model is told. Keys
  must already be canonical locale tags (`en-US`, `zh-CN`, `es-419`); `en-us` is
  rejected rather than rewritten.
- `handler` must also be owned by the plugin: a plugin cannot register another
  plugin's handler.
- `parameters` is a JSON Schema object, at most 8192 bytes. The Host validates
  the shape (valid JSON object) only and never re-interprets the schema, so the
  model and the plugin cannot end up with two different readings of it.
- `timeoutMs` is required (`1..30000`) and is never defaulted: the number in the
  manifest is the bound the operator sees. It is a ceiling, not a wait — the Host
  sets the call deadline from it, and the plugin's own budget decides how long
  the call actually takes. Keep it close to what the tool really needs: tool time
  is AI-worker time, and the AI queue runs with very low concurrency, so an
  inflated ceiling turns one slow plugin into site-wide reply latency. Sites have
  no separate tool-budget gate, so this declaration is the only upper bound an
  operator can see.
- `context` lists the conversation facts this tool needs: `locale`, `topic`,
  `viewer`. It is a whitelist, not a hint — a fact that is not declared never
  reaches the plugin process. Omitted means `["locale"]`, because "search in the
  language of the site" is the common case and discloses nothing about a member.
  `viewer` is the id of the member whose message triggered the call; declare it
  only when the tool genuinely behaves differently per member (per-user quotas,
  personalisation). Values are matched exactly: `Locale` or `ip` is rejected
  rather than normalised.

Transport reuses the versioned `ProviderCall` family on the reserved slot
`sforum.ai.tool` with operation `invoke`: request schema
`sforum.ai.tool.invoke.request@1` carries `{name, arguments}` and response
schema `sforum.ai.tool.invoke.response@1` carries `{content, isError}`. The Host
dispatches only to a declaration frozen in that runtime's manifest, and the
per-call deadline must equal the declared `timeoutMs` — a plugin cannot ask for
a longer window than the manifest promised.

Declared context facts are additive fields in the same request document:
`locale` (BCP-47 string), `topicId` (integer) and `viewerUserId` (integer). They
are written only when the declaration listed them, so a tool that declares
nothing receives exactly the `{name, arguments}` shape it always did. The schema
version therefore stays at `@1`, matching the same additive-field rule the
completion contract used when image parts were introduced. On the SDK side they
surface as `AIToolCall.Locale`, `AIToolCall.TopicID` and
`AIToolCall.ViewerUserID`; undeclared facts stay at their zero value, and a
plugin must not assume they are populated.

Tool results are untrusted data in the same sense as any other tool output:
`isError` distinguishes a failure the model may retry from a failure of the call
itself. A Go error from the handler is recorded by the Host and the model only
receives a generic line; plugin error text is never published to the community
through the model.

Contributing a tool does **not** require the `ai.invoke` capability. The tool
provider does not spend site AI budget: the purpose that runs the completion
loop does, and it keeps its own gates, quota, and traces. What a tool
declaration does require is the normal exact-artifact trust review and Protocol
V2.

Being registered is not the same as being visible. Every declared tool enters
the Host tool registry — one enumerable answer to "which AI tools exist on this
site" — but the model only sees what the running purpose allows. Purposes carry
their own allowlist (`RunInput.ToolNames`), and plugin tools never shadow a Core
tool name.

For the reply purpose that allowlist lives in the site's AI settings document
under `purposes["forum.reply"].tools.allowed`: leaving it unset keeps the
purpose's declared Core list, and listing names — Core or plugin — makes exactly
those visible. The legacy `reply.tools.allowed` field is still accepted and is
normalized into the same entry; new configurations should use the purpose map. A plugin tool
therefore never becomes reachable just by installing the plugin; an operator has
to name it. Names that are not registered at request time are skipped, so a
configuration may be written before the plugin is installed. The tool registry is
rebuilt whenever the Host applies a new plugin runtime set, so enabling or
disabling a plugin changes this catalog without an API restart; the
assembly-time registration only covers the window before the first apply.

The console does not make operators guess those names. `GET /admin/ai/settings`
returns `availableTools[]` — every registered tool with its `name`,
`description` (the model-visible text), optional `descriptionI18n`
(console-only copies), `origin` (`kind: core | plugin`, plus `extensionId` and
`version` for plugin tools), the declared `timeoutMs` for plugin tools, and a
bounded `parameters` summary (top-level names and required entries, not the
whole schema). It is a global fact, deliberately separate from any purpose's
allowlist: the catalog answers "what exists here", the allowlist answers "what
this purpose may use". Disabled plugins are not registered at all, so their
tools disappear from the catalog instead of lingering as entries that fail on
every call.

### Event publish command

`sforum.events.emit@1` lets a plugin publish an event under its own namespace.
It requires the `events.emit` process capability and runs under actorless
service authority. Input is `sforum.events.emit.input@1` (`name`, `kind`,
`payload`); output is `sforum.events.emit.result@1` (`delivered`, `failed`).

Manifest V3 already allows a plugin to declare custom event names that start
with `<extensionId>.`; the previous publish path only resolved the Core catalog,
which produced a "can declare, cannot publish" split. Publish now resolves a
caller-namespaced name as well, with two independent gates:

- the command layer rejects the call unless the name is owned by the verified
  runtime extension id, and unless the request's self-declared extension id
  matches that runtime identity;
- the event layer resolves either a Core catalog name or a caller-namespaced
  name, and never lets one plugin publish another plugin's namespace or a
  Core-owned name.

`kind` is `observe` or `filter`. Limits: event name up to 160 bytes, payload up
to 64 KiB with at most 64 top-level keys. The plan/preview view reports only the
name and counts — payload values never enter the plan, the UI, or the audit
record. Stable failure reasons are `events.namespace_forbidden`,
`events.kind_invalid`, `events.payload_invalid`, and
`events.publisher_unavailable` (a publish that cannot reach the event bus is
reported as unavailable, never as success).

The Go SDK exposes the typed helpers `EventEmitRequest` / `EmitEvent` plus
`EventEmitDelivery`, which reads the delivery counters (decimal strings) and
reports `ok=false` when the result document is missing or malformed instead of
treating an unreadable result as a successful publish. The SDK validates only
what an author can know locally (name shape, kind, payload size, idempotency
key); namespace ownership stays a Host decision, so a plugin cannot "fix" a
foreign name on its side.

### Extension attachment commands

Three Host Commands let a plugin reuse Core's attachment system (upload,
object storage, image delivery, variants, reference counting, orphan
reclamation) instead of storing images in its own tables. They are the only
supported way to turn an uploaded file into a plugin-owned public asset.

| Command | Purpose |
| --- | --- |
| `sforum.attachments.extension.claim@1` | Register an uploaded attachment as this extension's public asset |
| `sforum.attachments.extension.release@1` | Delete this extension's references so orphan reclamation applies again |
| `sforum.attachments.extension.list@1` | Read-only reconciliation of this extension's live references |

All three run under delegated actor authority and require
`attachment.upload` in `RequiredPermissions`, so a principal with no upload
capability never receives a delegation. They require the `host_commands`
database grant like every other Host Command.

**Claim** input is `{attachmentPublicId, context}`; `expectedRevision` travels in
the request envelope and must equal the attachment's current revision, exactly
like `sforum.attachments.status.set@1`. `context` is the plugin's own key
(`[a-z0-9._:-]{1,64}`, for example `oauth2-client:sfo_client_xxx`); the Host
stores it as `<extensionId>:<context>` using the verified runtime identity, never
plugin input, so one extension can never overwrite or release another's
references. Output carries `publicId`, a host-relative `url`
(`/media/attachments/<publicId>`), `contentType`, dimensions, size, `sha256`, and
`referenceCount`. Claim:

- accepts only `image/png`, `image/jpeg`, and `image/webp` under
  `min(site upload limit, 512 KiB)` — SVG/HTML are never claimable because the
  asset becomes anonymously readable;
- requires the delegated actor to own the attachment, unless it holds
  `attachment.manage`;
- refuses attachments already referenced by forum, avatar, SEO, or site assets,
  so a file in active use cannot be republished as a plugin asset;
- forces `visibility = public` (uploads default to `attachment.default_visibility`,
  which an operator may have set to private);
- replaces the reference in the same `context` (delete-then-insert, because a
  unique index allows exactly one row per context globally);
- recomputes `reference_count` from the reference rows instead of incrementing,
  under a per-extension advisory lock, so concurrent claims cannot leave a
  counted-but-unreferenced attachment that would never be reclaimed;
- enforces `attachment.extension_reference_limit` (default `500`) live
  references per extension.

**Release** takes exactly one of `{context}` or `{attachmentPublicId}` and only
deletes rows inside the caller's own namespace. It is idempotent (`released: 0`
when nothing matches). Deleting references does not delete the stored object:
the normal `attachment.cleanup_orphan_after_days` retention window reclaims it,
which is also the grace period for an accidental release.

**List** returns this extension's live references with `context`, `publicId`,
`url`, `contentType`, dimensions, size, `sha256`, and `createdAt`, so a plugin can
recover its own display state after a business-table loss. It never exposes
another extension's references or the storage object key.

Uninstalling the extension releases every reference it owns; disabling it does
not, because a disabled plugin's icons are expected to keep rendering.

On a Host without these commands, `DelegatedCommandRequest` fails with
`ErrHostActorDelegationUnavailable`. Map that to a readable message ("this Host
version does not support icon upload; upgrade SForum or use an image URL") and
never silently fall back to an inline base64 blob. Stable failure reasons are
`host.attachment_not_found`, `host.attachment_extension_forbidden`,
`host.attachment_extension_too_large`, `host.attachment_extension_reference_limit`,
`host.attachment_extension_invalid`, `host.attachment_extension_conflict`, and
`host.attachment_revision_conflict`.

## Plugin session state (S9)

`PluginSessionService` holds small bounded workflow state for the **current Host
session** on behalf of one plugin. It is Host-owned: the state never enters the
Core session payload, is never keyed by the raw session id, and never carries
session authority.

```text
rpc Get(PluginSessionGetRequest) returns (PluginSessionGetResponse)
rpc Set(PluginSessionSetRequest) returns (PluginSessionSetResponse)
rpc Delete(PluginSessionDeleteRequest) returns (PluginSessionDeleteResponse)
```

The request messages carry **only** `context` plus `namespace_key` (and `value` /
`ttl_ms` on `PluginSessionSetRequest`). There is no extension id, user id, or
session id field — the extension identity comes from the runtime-attested broker
context, and the session comes from the Host. A caller-supplied identity is not
merely ignored: it does not exist in the request shape, so cross-plugin and
cross-session access are denied by construction.

### How the session scope reaches the plugin

The plugin→Host channel attests the **runtime**, not the request: the host-v2
gRPC server is built per long-lived plugin process. The Host therefore mints a
`HostSessionDelegation` — a Host-signed, **single-invocation** capability — and
delivers it on the invocation's `RequestContext.host_session_delegation`. The
plugin echoes that same value back on the session-state calls. The Host then
validates signature, actor/runtime/artifact binding, expiry and its own mint
record, and derives the session scope from that record.

Consequences worth knowing before you design around it:

- The token is opaque. It carries no session identifier and no session
  fingerprint, so decoding it tells a plugin nothing about the session. This is
  why `Actor.session_id` stays unpopulated: a plugin never receives a session
  correlation handle.
- The Host retires the delegation when the invocation that minted it settles, so
  a token cannot be hoarded and reused for a later request. Reuse fails closed.
- The Host mints it only where it initiated the invocation itself inside an HTTP
  request. Background work (jobs, schedules, queue consumers) has no session
  scope and receives no delegation.
- A plugin must treat "no delegation" as an ordinary outcome, not a fault. It is
  what an anonymous request, a background call, or an erased session looks like.

### Lifetime and limits

The Host answers `Set` with the effective TTL it actually applied:

```text
effective = min(requested, 24h, remaining idle lifetime, remaining absolute lifetime)
```

Plugin state can therefore never outlive — let alone extend — the Core session.
`ttl_ms = 0` means "session-scoped" and still honours the 24-hour ceiling. A
request that outlives the session is tightened, never widened.

| Limit | Value | Failure |
| --- | --- | --- |
| `namespace_key` | ≤ 512 bytes | `host.plugin_session_key_too_long` |
| `value` | ≤ 1 MiB, non-empty | `host.plugin_session_value_too_large` |
| `ttl_ms` | 1 ms … 24 h | `host.plugin_session_ttl_out_of_range` |
| keys per (extension, session) | 64 | `host.plugin_session_key_limit` |
| keys per extension installation | 4096 | `host.plugin_session_extension_limit` |

Updating an existing key is always allowed, including at a ceiling: a plugin must
never be locked out of its own data. Values are opaque bytes with no Host-side
schema and no migration guarantee — this is ephemeral workflow state, and a
plugin must tolerate a miss. Put your own version byte in the payload if you
need one.

### Failure and lifecycle policy

| Condition | Result |
| --- | --- |
| No delegation, retired delegation, tampered or expired token, binding drift (runtime, artifact, trust grant, epoch), caller-supplied actor | `host.plugin_session_scope_unattested` (denied) |
| Runtime draining, quarantined, or no longer active | `host.plugin_session_runtime_stale` |
| Host Safe Mode | reads miss silently; writes are refused with `host.plugin_session_safe_mode` |
| Storage unavailable | `host.plugin_session_unavailable` (retryable, no backend detail) |

Session state is dropped on logout, on a fresh login (the replaced session's
namespace), on revoke and device-cap eviction, on extension disable/uninstall,
and by the privacy erase hook. A 24-hour cookie rotation keeps the same `sid`, so
state survives rotation without any migration step.

### SDK usage

```go
// Inside a Host-initiated invocation; ctx is the invocation context.
state, err := host.SessionState(ctx)
if errors.Is(err, pluginv2.ErrPluginSessionDelegationUnavailable) {
    // No session scope for this call (background work, anonymous request, erased
    // session). Degrade instead of failing.
    return nil
}
if _, err := state.Set(ctx, "checkout/step", payload, 10*time.Minute); err != nil {
    return err
}
value, found, err := state.Get(ctx, "checkout/step")
```

## Non-Go implementation checklist

A non-Go plugin is compatible only when its integration test proves all of the
following against a real SForum host subprocess:

- generated code comes from the committed v2 Protobuf sources and preserves
  fully qualified package/service names and streaming shapes;
- the go-plugin protocol line, magic cookie, exact version `2`, gRPC transport,
  AutoMTLS certificate exchange, control services, and broker stream are
  compatible with the host's pinned go-plugin core;
- the typed handshake rejects incomplete identity, incompatible version,
  missing limits, and stale repeat handshakes;
- `host_build` is read from the handshake request and an absent value is
  treated as unknown, never as a guessed version;
- `host_broker_id` is dialed through the broker control stream and every Host
  call carries exactly one raw `x-sforum-runtime-token-bin` value;
- request contexts preserve actor/locale/trace while exact identity, ordered
  authority, request id, and deadline pass host validation;
- 4 MiB message bounds, 16-call concurrency, timeout, cancellation, health,
  readiness, crash, restart, and stale-token behavior match the Go SDK;
- v2 mismatch fails closed and never starts or reconnects through v1.

The recommended first non-Go architecture is a thin Go launcher built against
the generated Go SDK. It owns go-plugin, AutoMTLS, broker, token, and context
enforcement and talks to language-specific business code through a private,
bounded local interface. That launcher and every executable it starts must be
declared and digest-bound in the package. A direct implementation in another
language is acceptable only after its go-plugin control-plane compatibility
suite is pinned and maintained; using ordinary gRPC alone is insufficient.
