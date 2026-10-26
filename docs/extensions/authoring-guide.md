# Plugin authoring guide

> **Handbooks (bilingual):**  
> [中文 · 扩展运营侧](../zh-CN/usage/extensions.md) ·
> [English · extensions (ops)](../en-US/usage/extensions.md) ·
> [中文开发](../zh-CN/development/README.md) ·
> [English development](../en-US/development/README.md) ·
> [Docs hub](../README.md)

This guide is the hand-written **technical** entry point for building SForum
plugins (Wave F4.2). Generated host catalogs live under
[catalogs/](./catalogs/README.md) — always regenerate those from code after
changing events, capabilities, contribution points, provider slots, or core
schedules.

**Focused guides:** [declared HTTP routes](./routes.md) · [build, digest, and load](./build-and-load.md)
(中文： [插件路由](../zh-CN/extensions/routes.md) · [构建与加载](../zh-CN/extensions/build-and-load.md))

## What you should depend on

| Surface | Use for |
| --- | --- |
| Manifest V3 `sforum.extension.json` (+ optional `includes`) | Versioned Registry/platform declarations and exact package-file identity |
| Go SDK `github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2` | New typed backend server, generated Host clients, exact runtime binding, health/readiness |
| Host API v2 wire `sforum.host/v2` | Typed queries/commands and versioned platform services over the runtime-scoped Host broker |
| go-plugin gRPC Protocol V2 | The only executable process protocol accepted by the Host; no fallback or downgrade |
| Published catalogs | Which events/points/slots/capabilities exist |

**Do not** import `app/Models/*` or other host business packages from a
third-party plugin. Built-in SMTP still uses internal packages for historical
reasons; new plugins should follow the SDK path shown by the Host API fixture.

Forum revision payloads are deliberately not a plugin query or mutation surface.
Plugins may observe the safe revision metadata on `topic.updated` and
`comment.updated` (`revisionNo`, operation, changed fields, and restore origin),
but never raw historical source, staff reasons, IPs, attachment-provider data,
or a bypass for Core's history, restore, redaction, or CAS policy.

### Identity / external auth providers

Reference package: `extensions/builtin/plugins/sforum-auth-github` (Manifest V3
`identity.runtime@1`).

| Plugin may | Host alone owns |
| --- | --- |
| Build authorize URL from Host `state` / PKCE challenge / absolute callback | OAuth `state`, PKCE verifier, callback transaction, absolute callback URL |
| Exchange code with bounded timeouts; return raw `providerSubject` in typed complete | Subject HMAC digest, users, links, roles, risk/session, browser sessions |
| Discard access tokens after identity proof | Public activation CAS, rate limits, audit, public error mapping |

**Closed surfaces (do not replace):** Core `GET /api/v1/auth/providers/{id}/callback`,
AuthSession issue/renew/destroy, callback state store, registration tickets.
Route Registry replacement of the callback is forbidden for integrity reasons.

External auth providers must ship complete operator setup guidance in their
Settings Document: where to create provider credentials, which public site and
callback URLs to enter, how secrets are stored, and the order for save, probe,
and Host activation. Use callout `linkLabel` + `linkUrl` for the official
application or documentation page; the Host validates HTTP(S) links and renders
them without vendor-specific Core branches.

Operator docs: [zh-CN](../zh-CN/usage/github-login.md) ·
[en-US](../en-US/usage/github-login.md).

## Where to put your package

Repository layout lives under [`extensions/`](../../extensions/README.md).
**Only `extensions/builtin/` is boot-scanned into the admin extension list.**

| Goal | Directory | Boot scan | Notes |
| --- | --- | --- | --- |
| Protected built-in (ship in product, auto list) | `extensions/builtin/plugins/<dir>/` | **Yes** (`SyncBuiltins`) | Themes: `…/themes/`. Backend build is **not** automatic for new ids — see below. |
| Local experiment (default scaffold) | `extensions/dev/plugins/<id>/` | No | Gitignored; never appears in admin by itself. |
| Ship in git, operator installs | `extensions/optional/plugins/<dir>/` | No | See [`extensions/optional/README.md`](../../extensions/optional/README.md). |
| Independent source collection | `<external-root>/{plugins,themes}/<dir>/` | Yes, when configured | Set `EXTERNAL_EXTENSION_ROOTS`; discovery remains inert. |
| CI / contract lock | `extensions/fixtures/…` | No | Test inputs only. |
| Production site | Admin upload → `EXTENSION_ROOT` | No | Operator trust + enable path. |

Every shipped package (built-in, optional, or an independent repository) also
carries `brand/icon.svg`, `brand/icon.png`, and `brand/social-preview.png`.
Sizes, budgets, the visual template, and the `extension brand check` gate are
specified in [`brand-assets.md`](./brand-assets.md).

### Auto scan vs auto build

- **Built-in register:** API/worker boot runs `SyncBuiltins` over
  `BUILTIN_EXTENSION_ROOT` (default `extensions/builtin`, or
  `storage/builtin-dev` when using `./scripts/api-dev.sh`). Every
  `plugins/*` and `themes/*` child directory with a valid package is
  written into the extension store and shows up under admin Themes/Plugins.
- **External discovery:** API/worker boot scans each comma-separated
  `EXTERNAL_EXTENSION_ROOTS` collection. Valid packages are copied into
  `EXTENSION_ROOT` as immutable uploaded snapshots. New packages stay inert;
  changes become staged candidates and never inherit trust or auto-promote.
- **Auto build (dev only):** `scripts/build-builtin-plugins.sh` (invoked by
  `api-dev` / `worker-dev`) stages builtins into `storage/builtin-dev` and
  `go build`s **only the plugin ids hard-coded in that script**. A new
  builtin package is scanned after restart once it is present under the
  active builtin root, but you must either extend the script or build +
  `extension digest --write` yourself before enable.

Scaffold defaults:

```bash
cd apps/api
# Local scratch (gitignored) — does NOT auto-register
go run ./cmd/sforum make:plugin --id acme.demo --name "Acme Demo" \
  --description "…" --backend --no-interaction
# → ../../extensions/dev/plugins/acme.demo

# Protected built-in — SyncBuiltins will pick it up after restart
go run ./cmd/sforum make:plugin --id sforum.foo --name "Foo" \
  --description "…" --backend --builtin --no-interaction
# → ../../extensions/builtin/plugins/sforum.foo
```

Full CLI reference (make, digest, validate, test, package `--exclude-source`,
seed, recovery):  
[中文 · 开发者 CLI](../zh-CN/development/cli.md) ·
[English · Developer CLI](../en-US/development/cli.md).

Full map: [`extensions/README.md`](../../extensions/README.md).

## Quick start

```bash
# Scaffold (from apps/api). Prefer --out for a disposable path, or omit --out
# to land under extensions/dev (gitignored). Use --builtin for SyncBuiltins.
cd apps/api
go run ./cmd/sforum make:plugin \
  --id acme.demo \
  --name "Acme Demo" \
  --description "Example plugin" \
  --url https://example.com/acme-demo \
  --author-name Acme \
  --backend \
  --no-interaction \
  --out /tmp/acme.demo

# Contract checks (catalogs, capabilities, backend entry)
go run ./cmd/sforum extension test --allow-scaffold /tmp/acme.demo

# After changing packaged files, refresh exact digests and revalidate
go run ./cmd/sforum extension digest --write /tmp/acme.demo

# After you change host catalogs, refresh docs
go run ./cmd/sforum extension docs generate
go run ./cmd/sforum extension docs generate --check
```

Scaffolds use versioned Schema UI by default. Optional buildless variants:

```bash
# Provider + host-rendered Probe action (known provider slot; backend required)
go run ./cmd/sforum make:plugin ... --backend --provider-slot mail.provider

# Framework-neutral author-prebuilt component with Schema fallback
go run ./cmd/sforum make:plugin ... --prebuilt-settings
go run ./cmd/sforum make:theme ... --prebuilt-settings

# First-class Vue admin page using the stable Plugin UI SDK
go run ./cmd/sforum make:plugin ... --vue-admin-page

# Public Page Registry page (path /docs) inheriting the active site shell
go run ./cmd/sforum make:theme ... --public-page docs
go run ./cmd/sforum make:plugin ... --backend --public-page docs --public-page-layout site

# Declarative public page (path /orders) rendered by the Host from a bounded descriptor
go run ./cmd/sforum make:plugin ... --schema-page orders
go run ./cmd/sforum make:plugin ... --schema-page orders --schema-page-kind notice

# Route middleware: one before request patch and one after response patch
go run ./cmd/sforum make:plugin ... --backend --route-middleware
```

### What `--public-page` generates

- `theme.json` page mapping: `action: add`, path `/<slug>`, `access: public`,
  `contract: sforum.page.<id>_<slug>@1`, `layout: site|bare` (default `site`).
- `templates/page-<slug>.html`, declared in **both** manifest `templates[]` and
  `packageFiles[]` (kind `template`) with equal digests, and its
  `viewModelSchema` is the same exact reference as the page `contract`. That
  three-way identity is what the activation preflight checks; do not hand-edit
  one without the other.
- A static page only. Plugin-owned page data (`dataSource` + `dataRoute` +
  `dataSchema`) is deliberately **not** generated, because the Page Loader
  Gateway has no transport for Protocol V2 runtimes today and such a page would
  always fall back to the Host with `view_model_unavailable`.
- `layout: site` means the active theme (or the Host fallback shell) owns navbar
  and footer: never repeat them inside your template. `layout: bare` means the
  page owns all of its chrome and must not rely on the shell.

### Where to look before you declare a page

`docs/extensions/v3/catalogs/public-page-surfaces.md` is the generated runtime
surface for public pages: per page it lists the path, access, contract, whether the
Core page is replaceable, the body views you may declare (`template`, `component`,
`schema`), the layout values (`site`, `bare`), the regions the page exposes, and the
Host islands a theme may mount. It is generated from the same sources the runtime
reads, so it cannot claim a surface the runtime does not have. Regenerate and check
it with `node scripts/v3-catalog/generate.mjs` / `--check`.

### What `--schema-page` generates

- `theme.json` page mapping: `action: add`, path `/<slug>`, `access: public`,
  `layout: site`, `view: schema`, and a `schema` block naming exactly two things:
  the descriptor file id and the exact JSON Schema reference it must satisfy.
- `schemas/page-<slug>.json` (the bounded descriptor) and
  `schemas/page-<slug>-contract.json` (the package-declared contract), both
  declared as `kind: schema` **with a `version`** in `packageFiles[]`. The exact
  reference in the page declaration (`<schemaFileId>@1`) is matched against those
  declarations, so the digest and version must agree.
- No Vue, no Go template, no build step: the Host renders the descriptor. That is
  the point of this view - a page that needs neither a frontend build nor a
  runtime.
- Host-enforced bounds (the package cannot exceed them): `kind` is one of
  `list` / `detail` / `form` / `notice`; field types are `text` / `textarea` /
  `number` / `boolean` / `select`; caps are 24 fields, 64 items, 64 options,
  64 cells, 200-character labels and 2000-character text.
- `form` renders **read-only** today. Submission needs the page's declared
  `dataRoute` binding, and plugin page data loading is still blocked by the
  loader-transport gap (the same blocker as `dataSource`), so the scaffold and the
  renderer do not pretend a submit exists.
- The scaffold is validated by the same preflight the CLI runs
  (`extension validate` / `extension test` / activation): a fresh scaffold passes
  without hand-editing. Duplicate ids or paths, an unknown kind, or a slug shared
  with `--public-page` are rejected before anything is written.

### What `--route-middleware` generates

- Two routes against the reviewed core read route `core.route.forum.topics`
  (`GET /api/v1/topics`): `action: before` with `mutableRequestFields` and
  `action: after` with `mutableResponseFields`.
- `guard: core.guard.inherit` and `fallback: closed` on both, and no
  `access`/`permission` restatement: a patch inherits the target route's
  authorization and must not re-implement login or permission checks.
- `backend/routes_example.go`, an example handler carrying `//go:build ignore`
  so it never breaks your build. Merge it into your backend module (create
  `go.mod` first) and write only the pointers the manifest declares; anything
  else is rejected by the Host.

Minimal backend using the public SDK:

```go
package main

import pluginv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2"

func main() {
	pluginv2.Serve(pluginv2.NewServer())
}
```

Build the executable to the path declared in `backend.entry` (usually
`backend/plugin`) before enabling the plugin in admin.

Scaffolds and built-in references use Protocol V2. Follow
[Host API v2 and non-Go runtimes](./host-api-v2.md) for the generated Go SDK,
AutoMTLS broker, exact trust binding, and non-Go interoperability rules.

## Manifest V3 package contract

All new CLI scaffolds emit explicit `manifestVersion: 3`. The complete generated
root-field catalog is [manifest-v3.md](./catalogs/manifest-v3.md); the embedded
Draft 2020-12 schema and Go semantic validator are both authoritative.

Version handling is fail-closed. Every package must declare
`manifestVersion: 3`; omitted, V1, V2, and future versions are rejected during
inert installation preflight. Executable packages must additionally declare
`backend.protocolVersion: 2` and `backend.hostContract: sforum.host@2`. The Host
never normalizes an older package or falls back to an older process protocol.

Large manifests may shard supported families through `includes`. A family must
be declared either at the root or through its include, never both. Include paths
are package-relative JSON files/directories and are resolved during inert
preflight; loading them never executes package code.

Every V3 package declares exact SHA-256 identities in `packageFiles`. Supported
kinds are `executable`, `frontend`, `locale`, `schema`, `migration`, `template`,
`asset`, and `openapi`. Backend, guard, migration, template, asset, OpenAPI, and
L2 references must resolve to the matching declared file kind and digest.

Page Registry themes have an additional three-way identity invariant. Every
`theme.json.pages[].template` path must resolve to exactly one Manifest V3
`templates[]` declaration and one `packageFiles[]` entry of kind `template`.
The Manifest template and package-file paths must be identical, and their
SHA-256 digests must match the file. `theme.json` defines page mappings only; it
does not replace either exact-artifact declaration. A missing or stale entry is
rejected by `extension validate` and causes activation to fail closed with
`extension.build_failed`.

### Core compatibility (`sforumVersion`)

`sforumVersion` is a required Masterminds semver constraint, and the Host now
**evaluates it** instead of only parsing it. The single authority is
`coreVersion`: the release identity reported by `/api/v1/health`, the v2
handshake (`Server.HostBuild()`), and the admin bridge. Never compare it to
`buildVersion` — that label (`dev`, `dev-abc12`) exists for display only.

- Installation (staged) rejects an out-of-range package with
  `extension.core_incompatible` (HTTP 422, structured `required`,
  `coreVersion`, `buildVersion` fields).
- Every path that lets an artifact run — enable, theme activation, upgrade,
  rollback, restart — rejects it with the same reason code and HTTP 409.
- Development builds derive `coreVersion` from the nearest reachable `v*` git
  tag, so a local checkout can install packages that target its own release
  line: a `^3.0.0` package is no longer rejected on a dev build derived from
  `3.0.16`. When the checkout is ahead of that tag, the commit distance, short
  SHA, and a dirty marker go into build metadata (`3.0.16+13.g6f94488ad`,
  `3.0.16+13.g6f94488ad.dirty`) — never into a prerelease segment — so the
  constraints evaluate exactly as they do for the tag. Without a reachable tag
  they report `1.0.0`. Prefer ranges that describe the release line you
  actually support.
- Prerelease Hosts (`3.0.13-alpha.1`) are evaluated against both the exact
  version and its release base (`3.0.13`), so an ordinary `>=3.0.0` range keeps
  working across alpha/beta releases.
- Built-in packages are exempt: they ship with the Host, so a stale constraint
  there cannot be fixed by the operator.
- If the Host build label itself is not strict semver, the Host logs a warning
  and skips enforcement; treat that as an unknown Host version, not permission
  to assume one.

### Navigation and region kinds

`navigation[]` accepts these `kind` values: `menu`, `item`, `breadcrumb`,
`header`, `footer`, `sidebar`, and `account-settings`. `regions[]` accepts
`menu`, `widget`, `header`, `footer`, `sidebar`, and `content`.

`account-settings` is the Host-owned personal settings sidebar slot, rendered by
the private `GET /site/account-navigation` projection rather than public site
chrome:

- `href` must start with `/settings/` or `/extensions/`. Any other path — even a
  Host-safe one such as `/private` — is dropped by the projection.
- Contributions default to `authenticated`: an omitted `visibility` (or an
  explicit `public`) is raised to `authenticated` during lifecycle publication,
  so this slot can never leak to guests.
- `permission` and `ownerResource` are optional visibility filters. A
  contribution with neither is visible to every signed-in user; with either (or
  both) set, it appears only for actors holding the declared permission key or a
  Host-owned retained-resource key.
- `visibility` accepts `public`, `anonymous`, or `authenticated`. For the six
  public chrome kinds it defaults to `public`.

Core publishes no `account-settings` contribution; the slot exists for plugins.

```json
"navigation": [
  {
    "id": "acme.oauth.navigation.apps",
    "contractVersion": "acme.oauth.navigation.apps@1",
    "kind": "account-settings",
    "action": "add",
    "label": "OAuth2 apps",
    "href": "/settings/oauth-apps",
    "permission": "acme.oauth.create_clients",
    "order": 50
  }
]
```

### Notification types and Host emission

Executable plugins may declare inert `notificationTypes`. Each type id must be
under the plugin id namespace, bind `contractVersion: <type>@1`, and reference
one exact `packageFiles` JSON Schema through `payloadSchema`. A plugin type
cannot be `required`; only Core may own non-configurable required notices.

At runtime use Host API v2 `notifications.emit@1` through
`(*pluginv2.Host).EmitNotification`. The request contains one declared type and
payload version, a structured payload, at most 50 explicit active user ids, a
declared target descriptor, and a visible-ASCII idempotency key. The Host
validates the exact artifact/grant/epoch/instance, schema digest and value,
target, recipient policy, 16 KiB payload bound, count, rolling rate, and
deadline in its transaction. It writes recipient-owned projections and
redacted audit evidence; it never accepts a plugin actor, session, cookie,
arbitrary URL, required type, raw notification-table write, or bulk broadcast.

See the generated [notification contract](./catalogs/notifications.md) and
`extensions/fixtures/plugins/sforum-notification-reference`.

```bash
cd apps/api
go run ./cmd/sforum extension digest --write <package-root>
go run ./cmd/sforum extension validate <package-root>
go run ./cmd/sforum extension test <package-root>
```

`digest --write` updates inline `packageFiles` after local file changes,
synchronizes digest-bearing inline declarations such as `templates[]`, and then
revalidates the whole package. Keep `packageFiles` inline when using this helper;
included package-file shards remain valid but must be refreshed by the package
authoring pipeline. For themes, add both the corresponding `templates[]`
declaration and `packageFiles[]` entry before running the helper; digest refresh
cannot infer Page Registry template identity, contract version, ViewModel
schema, or package-file membership from `theme.json`.

Dependency entries use exactly one of `id` or `capability`:

| Kind | Meaning |
| --- | --- |
| `required` | A matching extension/capability and semantic version must exist before activation |
| `optional` | Orders activation when present, but absence is allowed |
| `conflict` | A matching installed version blocks the graph |
| `provides` | Publishes one capability at an exact semantic version |

The host rejects missing required versions, ambiguous capability providers,
cycles, duplicate extension identities, and ambiguous replacement providers.
Activation order is deterministic.

Static install is always inert. Uploaded packages with backend code, migrations,
custom/raw guards, raw/kernel database authority, executable lifecycle hooks,
admin code, or public L2 files require an actor-bound `super_admin` confirmation
for the exact artifact before first execution. The impact document binds the
Manifest contract, declarations, real executable bytes, authorities,
dependencies, and Host/Frontend contracts. Any relevant change invalidates the
grant. Capability declarations describe and shape trusted code; they are not a
sandbox.

### Public L2 honesty

Public L2 (package-local prebuilt ESM/CSS mounted by `SFExtensionWidget`) is
**fully trusted browser code**, not a sandbox. When a contribution mounts, the
Host bridge sets `trust: fully_trusted_browser_code` and the public widget
carries the same value as a machine-readable `data-l2-trust` attribute; no
in-page trust banner is rendered. Public L2 is always enabled: trust only
extensions whose browser code you accept. SSR/L1 slot content remains the
fallback when L2 fails, is quarantined, or is disabled by Safe Mode.

The mount bridge also carries `host.coreVersion` (release identity, strict
semver). It is optional: a Host that predates the field omits it, so detect it
and treat a missing value as unknown. The public descriptor intentionally never
carries `buildVersion`, `commit`, or a `dev-` label.

There is no per-package switch, but two prerequisites gate the mount: the
extension must be enabled, and exact-artifact trust confirmation must be on
(`SFORUM_V3_TRUST_CHALLENGES=true`). `.env.example` and `compose.yaml` default it
to `false` for local development, so a fresh checkout shows only the SSR/L1
fallback until you turn it on. Production Compose defaults it to `true`.

Themes remain presentation-only. They may declare templates, assets,
components, navigation/regions, settings, package files, and other presentation
contracts, but cannot own plugin business/runtime declarations. A plugin's
versioned business data contract does not change when a theme overrides its
presentation.

System error pages are a narrower theme-only surface. A theme may replace
`system.forbidden`, `system.not_found`, `system.rate_limited`, and
`system.server_error` with L0/L1 presentation, but the Host owns the HTTP
status, public copy, cache/robots policy, and home/back/retry actions. Plugins
cannot replace `system.*` pages, and system error templates cannot mount public
L2 widgets.

Manifest V3 currently defines and validates the full target contract. Runtime
implementations land phase by phase (Host API v2, lifecycle/database, Route
Registry, platform registries, themes, and L2); a valid declaration is not proof
that a later runtime family is active in the current host release.

## Buildless settings UI: choose the smallest capable mode

Plugins and themes share one versioned Settings Document. Existing array-form
`settings` remains compatible, but new packages should emit `schemaVersion: 1`.

| Need | Manifest mode | Author JS | Operator host build |
| --- | --- | --- | --- |
| Fields, tabs, groups, columns, callouts | Schema | None | None |
| Provider connection test or other catalogued operation | Schema + Actions | None | None |
| Truly complex interaction | Prebuilt component + Schema fallback | Fully trusted `.mjs` | None |

### Schema field types and width

Host Schema UI supports these `fields[].type` values:

| Type | Control |
| --- | --- |
| `text` / `string` | Single-line input |
| `number` | Numeric input |
| `boolean` | Checkbox |
| `select` | Select when `options` is present (options also force select for other free-text types) |
| `secret` | Password input; empty save preserves the stored secret |
| `textarea` | Multi-line autoresizing textarea |

Optional `fields[].width`:

| Value | Behavior |
| --- | --- |
| `default` (or omitted) | Control is capped (`max-w-xl`) — good for short values |
| `full` | Control fills the available column width — prefer for long copy and `textarea` |

Example:

```json
{
  "key": "home.notice.zh-CN",
  "label": { "zh-CN": "首页提示条", "en-US": "Homepage notice" },
  "type": "textarea",
  "width": "full",
  "default": "",
  "groupId": "home-copy"
}
```

### Reference A — theme tabs/groups, no JavaScript

This is the default for new themes. Upload, configure, activate through Page
Registry, and save values without building Nuxt:

```json
{
  "id": "acme.reading-theme",
  "name": "Acme Reading Theme",
  "description": "Readable runtime theme.",
  "url": "https://example.com/theme",
  "author": { "name": "Acme" },
  "version": "1.0.0",
  "type": "theme",
  "sforumVersion": ">=0.1.0",
  "admin": {
    "entry": "/settings",
    "pages": [{ "path": "/settings", "label": "Theme settings", "view": "settings" }]
  },
  "settings": {
    "schemaVersion": 1,
    "ui": {
      "mode": "schema",
      "layout": "tabs",
      "tabs": [
        { "id": "home", "label": { "zh-CN": "首页", "en-US": "Home" }, "groups": ["hero", "layout"] }
      ],
      "groups": [
        { "id": "hero", "label": { "zh-CN": "主视觉", "en-US": "Hero" }, "columns": 2 },
        { "id": "layout", "label": { "zh-CN": "布局", "en-US": "Layout" } }
      ],
      "callouts": [{
        "id": "instant",
        "tone": "info",
        "title": { "zh-CN": "保存后立即生效", "en-US": "Applies immediately after save" },
        "tab": "home"
      }]
    },
    "fields": [
      { "key": "hero.title", "label": { "zh-CN": "标题", "en-US": "Title" }, "type": "text", "default": "Welcome", "groupId": "hero", "column": 1 },
      { "key": "hero.compact", "label": { "zh-CN": "紧凑模式", "en-US": "Compact" }, "type": "boolean", "default": "false", "groupId": "hero", "column": 2 },
      { "key": "hero.blurb", "label": { "zh-CN": "简介", "en-US": "Blurb" }, "type": "textarea", "width": "full", "default": "", "groupId": "hero" }
    ]
  }
}
```

See `extensions/builtin/themes/sforum-default/manifest/settings.json` and
`extensions/fixtures/themes/sforum-schema-theme/`.

#### Admin menu placement

`admin.pages[]` entries with `"menu": true` reach the admin sidebar. There are
two placement modes:

- **Flat (default).** The page is listed under *Extensions* and only shows in
  professional mode.
- **Shared section.** `"group": "<section-id>"` joins a Core-registered
  first-level menu. `ai-tools` is the current example: Core owns the container
  (label, icon, position) plus its own console page, and any plugin page that
  declares `group: "ai-tools"` is appended to the same menu. Core never learns
  plugin ids, and a plugin cannot invent a new top-level container — an
  unregistered `group` value falls back to the flat behaviour.

Section membership lives in the manifest, so it is an exact-artifact change: an
already-installed plugin picks it up through the normal upgrade path.

A `view: component` page may also declare `"hostActions": ["extension.install"]`
to receive the Host-mediated `bridge.installExtension` action (it stages a
package, never enables it). The field is rejected on `about`/`settings` pages
and on unknown action ids; older Hosts reject the manifest entirely
(`additionalProperties: false`), so declare a minimum Host version in your
release notes. See `trusted-admin-components.md`.

> Manifest field reference is generated from the embedded schema
> (`sforum extension docs generate`). The catalog page lists root fields only,
> so nested `admin.pages[]` members such as `hostActions` are documented here
> and in `trusted-admin-components.md` rather than edited into the generated
> catalog by hand.

### Reference B — provider Schema + Probe action

SMTP is the production reference. Operators can install → configure while
disabled → test with the restricted probe runtime → enable. The probe cannot
register normal routes, events, jobs, schedules, or providers and never sends
test mail:

```json
{
  "backend": { "entry": "backend/plugin", "rpc": "hashicorp-go-plugin", "protocolVersion": 1 },
  "providers": [{ "slot": "mail.provider", "label": "SMTP", "timeoutMs": 15000 }],
  "settings": {
    "schemaVersion": 1,
    "ui": {
      "mode": "schema",
      "layout": "tabs",
      "tabs": [{ "id": "connection", "label": "Connection", "groups": ["server", "credentials"] }],
      "groups": [
        { "id": "server", "label": "Server", "columns": 2 },
        { "id": "credentials", "label": "Credentials", "columns": 2 }
      ]
    },
    "fields": [
      { "key": "host", "label": "Host", "type": "text", "default": "127.0.0.1", "groupId": "server" },
      { "key": "port", "label": "Port", "type": "number", "default": "1025", "groupId": "server" },
      { "key": "username", "label": "Username", "type": "text", "default": "", "groupId": "credentials" },
      { "key": "password", "label": "Password", "type": "secret", "default": "", "groupId": "credentials" }
    ],
    "actions": [{
      "id": "probe",
      "kind": "provider_probe",
      "label": { "zh-CN": "测试连接", "en-US": "Test connection" },
      "placement": "footer",
      "useDraftValues": true,
      "fields": ["host", "port", "username", "password"]
    }]
  }
}
```

Implement SDK `ProviderProbe`. The host owns permission checks, declared-field
and secret preserve/replace semantics, 15-second timeout, response bounds, and
credential-free audit. Storage providers reuse `StorageProbe` through the same
Action catalog.

### Reference B2 — AI tool Probe action

A plugin that already declares an AI tool (`aiTools[]`) can offer a "test"
button for it. The host starts the same restricted short-lived runtime, seeds it
with the operator's **draft** settings values as `SFORUM_SETTING_*` env vars,
and dispatches one call through the reserved slot `sforum.ai.tool`/`invoke`:

```json
{
  "backend": { "entry": "backend/plugin", "rpc": "hashicorp-go-plugin", "protocolVersion": 2 },
  "aiTools": [{
    "id": "demo.plugin.ai.order_lookup",
    "contractVersion": "demo.plugin.ai.order_lookup@1",
    "name": "demo-order-lookup",
    "description": "Look up an order by its public number.",
    "parameters": { "type": "object", "properties": { "orderNumber": { "type": "string" } } },
    "handler": "demo.plugin.ai.order_lookup",
    "timeoutMs": 1000
  }],
  "settings": {
    "schemaVersion": 1,
    "ui": { "mode": "schema", "layout": "form" },
    "fields": [
      { "key": "base_url", "label": "Base URL", "type": "text", "default": "https://api.example.com" },
      { "key": "token", "label": "Token", "type": "secret", "default": "" }
    ],
    "actions": [{
      "id": "probe",
      "kind": "ai_tool_probe",
      "label": { "zh-CN": "测试工具", "en-US": "Test tool" },
      "placement": "footer",
      "useDraftValues": true,
      "tool": "demo.plugin.ai.order_lookup",
      "fields": ["base_url", "token"]
    }]
  }
}
```

Rules for this kind:

- `tool` must be one of this extension's own `aiTools[].id` (namespaced under
  the extension id) and `fields` must be non-empty; otherwise the manifest is
  rejected. Unlike `provider_probe`, this kind does **not** require a
  `providers[]` slot — the declaration surface is the AI tool.
- The draft values are passed to the tool as its JSON `arguments` object, so the
  tool reads its own configuration from the process env and receives the same
  facts as its call arguments. A secret field must be supplied explicitly
  (`preserve` or `replace`); it is never read from storage implicitly.
- The call deadline is exactly the declared `timeoutMs`. A tool result of
  `{ "content": "...", "isError": false }` becomes a success and
  `isError: true` becomes a failure; the whole human-readable explanation should
  be written into `content`, because the tool result contract carries nothing
  else.
- The probe runtime still cannot register routes, events, jobs, schedules, or
  providers, and it runs even while the plugin itself is disabled, so operators
  can test before enabling.
- A probe call carries the same declared `context` facts as a model-driven call.
  With no declaration that means `locale`; a probe that reports "no locale" is
  telling you the site has no default locale configured, not that the Host
  dropped the field.

### Reference C — trusted prebuilt complex component

Use this only when Schema + Actions cannot express the interaction. The author
ships final `.mjs`/`.css` bytes; SForum never compiles uploaded Vue SFCs and
never accepts remote script URLs. Schema fields remain mandatory fallback:

```json
{
  "settings": {
    "schemaVersion": 1,
    "ui": {
      "mode": "component",
      "layout": "form",
      "component": {
        "id": "settings",
        "apiVersion": 1,
        "entry": "frontend/admin/dist/settings.mjs",
        "css": "frontend/admin/dist/settings.css"
      }
    },
    "fields": [{ "key": "message", "label": "Message", "type": "text", "default": "Hello" }]
  }
}
```

```js
export const apiVersion = 1
export function mount(target, bridge) {
  const input = document.createElement('input')
  input.value = bridge.settings.values().message || ''
  const onInput = () => bridge.settings.updateValue('message', input.value)
  input.addEventListener('input', onInput)
  target.append(input)
  return () => {
    input.removeEventListener('input', onInput)
    input.remove()
  }
}
```

An active super administrator confirms a one-use actor/version/API/component/
digest-bound challenge. Import, contract, mount, CSS, cleanup, or quarantine
failure automatically returns to Schema UI. See
`extensions/fixtures/plugins/sforum-prebuilt-settings/` and
[trusted-admin-components.md](./trusted-admin-components.md).

## Reference 1 — built-in SMTP (`sforum.smtp`)

Path: `extensions/builtin/plugins/sforum-smtp/`

Use this package when you need a **provider-slot** plugin with settings and
outbound network:

| Area | What SMTP does |
| --- | --- |
| Manifest | `providers: [{ slot: "mail.provider", ... }]`, explicit `capabilities` |
| Multi-file layout | Thin root + `includes` for langs, versioned settings, and admin |
| Backend | Implements `SendMail` over go-plugin; no public HTTP `RouteTarget` |
| Settings | Host injects `SFORUM_SETTING_*` env vars into the child process |
| Admin UI | Host-rendered tabs/groups/callouts + settings actions (`provider_probe`, `ai_tool_probe`); no frontend package |

Key manifest ideas (simplified):

```json
{
  "id": "sforum.smtp",
  "type": "plugin",
  "capabilities": ["host.api", "settings.own", "net.outbound"],
  "backend": {
    "entry": "backend/plugin",
    "rpc": "hashicorp-go-plugin",
    "protocolVersion": 1
  },
  "providers": [{ "slot": "mail.provider", "label": "SMTP", "timeoutMs": 15000 }]
}
```

Contract-test it without requiring a rebuilt binary:

```bash
cd apps/api
go run ./cmd/sforum extension test --skip-backend-binary \
  ../../extensions/builtin/plugins/sforum-smtp
```

Read the full package README and `manifest/` files for settings groups/actions
and Chinese/English identity langs.

## Reference 2 — content policy workflow (`sforum.content-policy`)

Path: `extensions/builtin/plugins/sforum-content-policy/`

Use this package when you need a **non-provider workflow** plugin: sync
filters, settings, and public UI contributions (Wave E5). SMTP remains the
**mail provider** reference; this package is the **filter + contribution**
reference.

| Area | What content policy does |
| --- | --- |
| Manifest | Explicit `capabilities`, three filter `events`, multi-file `includes` |
| Backend | Public SDK `Serve` + `Noop`; implements `InvokeHook` only |
| Filters | `topic.before_create`, `topic.before_update`, `comment.before_create` |
| Settings | Keywords, mode (`reject` / `tag`), force tag, scan toggles, optional public badge |
| Contributions | Optional `forum.topic.badges` (`show_topic_badge`, default off) + `forum.topic.sidebar` → `/guidelines` |
| Providers | None (not a slot plugin) |

Operator path (fresh contributor):

1. Admin → Extensions → Plugins → enable **SForum Content Policy** (confirm
   capabilities: `host.api`, `settings.own`, `audit.append`).
2. Manage settings → add keywords (one per line; `#` comments allowed).
3. Keep mode **Reject publish** (recommended default).
4. Publish a topic/reply containing a keyword → API `422` with reason
   `content_policy.keyword_blocked`.
5. Topic detail shows a guidelines sidebar card. The title badge stays hidden
   until **Show badge under topic title** is enabled in plugin settings.

Author rules demonstrated by this package:

- Filters stay **cheap** (substring match on env-injected settings only).
- No Host API / network / River jobs inside `InvokeHook`.
- `mode=tag` only patches `tagSlugs` on topics; comments still reject.
- Force-tag requires the slug to already exist under host tag policy.

```bash
cd apps/api
go run ./cmd/sforum extension test --skip-backend-binary \
  ../../extensions/builtin/plugins/sforum-content-policy

# Build binary (also run by scripts/build-builtin-plugins.sh)
(cd ../../extensions/builtin/plugins/sforum-content-policy/backend && \
  go test ./... && go build -o plugin .)
```

See package `README.md` for settings env names and force-tag notes.

## Reference 3 — filesystem storage (`sforum.storage-fs`)

Path: `extensions/builtin/plugins/sforum-storage-fs/`

Use this package when you need a **storage provider-slot** plugin (Wave E6).
SMTP remains the **mail** provider reference; this package is the
**attachment.storage.provider** reference (no cloud credentials required).

| Area | What filesystem storage does |
| --- | --- |
| Manifest | `providers: [{ slot: "attachment.storage.provider", ... }]`, `settings.own` |
| Backend | Public SDK `Serve` + `Noop`; implements chunked `Storage*` RPCs |
| Settings | Absolute `root_path`, optional `public_base_url` (env `SFORUM_SETTING_*`) |
| Operator loop | Enable → Attachment settings select `plugin:sforum.storage-fs` → configure → Test connection → upload |

Key RPC methods (host streams ~1 MiB chunks by default):

| RPC | Role |
| --- | --- |
| `StoragePutBegin` / `StoragePutChunk` | Write object by key (Final commits) |
| `StorageOpen` / `StorageGetChunk` | Read object bytes |
| `StorageClose` | Abort put or release read session |
| `StorageDelete` / `StorageStat` / `StorageExists` | Lifecycle helpers |
| `StoragePublicURL` / `StorageSignedURL` | Optional URL strings (ACL still host-owned) |
| `StorageProbe` | Admin “Test connection” |

Minimal author shape:

```go
func main() {
	pluginv2.Serve(newStorageServer())
}
```

`newStorageServer` returns a Protocol V2 server with the attachment storage
provider registry attached. The generated storage scaffold and
`sforum-storage-fs` built-in are the executable references.

```bash
cd apps/api
go run ./cmd/sforum extension test --skip-backend-binary \
  ../../extensions/builtin/plugins/sforum-storage-fs

(cd ../../extensions/builtin/plugins/sforum-storage-fs/backend && \
  go test ./... && go build -o plugin .)
```

Rules demonstrated by this package:

- Object **keys** are host-generated; never invent alternate namespaces.
- Secrets/settings live in `extension_settings` (not `attachment.*` core options).
- Fail closed on bad config / I/O; host maps failures to
  `attachment.storage_unavailable` for uploads.
- Prefer a dedicated root directory; do not share core `local` root.
- For S3/MinIO/R2, keep the **same RPC surface** and put the vendor SDK only
  inside your plugin (core stays free of new cloud SDKs).

See package `README.md` for the full operator path.

## Reference 4 — declared HTTP routes (`sforum-custom-content`)

Path: `extensions/fixtures/plugins/sforum-custom-content/`

Use this package when your plugin **owns HTTP routes** under the host proxy
(`routes[]` + Protocol V2 `InvokeRoute`). It is the executable reference for
the full route lifecycle: manifest declarations, handler dispatch, request
schema validation, and package-file schema binding. The author-facing
contracts are documented in [routes.md](./routes.md); the walkthrough for
building and loading the package is in [build-and-load.md](./build-and-load.md).

| Area | What custom-content does |
| --- | --- |
| Manifest | Nine `routes[]` declarations: public `GET` reads, `POST` writes behind `core.guard.login`, `requestSchema`/`responseSchema` references |
| Backend | `contentServer.InvokeRoute` switch on `RouteId`; `TypedDocument` bodies; structured `ErrorDetail` |
| Guards | `core.guard.public` / `core.guard.login` only. The custom guard runtime is live in production, and raw request authority is available through a **package-declared** guard with `kind: raw_request` (the Host-owned `core.guard.raw_request` was removed on 2026-10-16); this fixture deliberately stays on the closed set |
| Schemas | `packageFiles` entries of kind `schema` referenced by route schemas |
| Extra registries | entities/taxonomy/fields, editor L2, navigation/regions, and Query Registry (beyond the route story) |

```bash
cd apps/api
go run ./cmd/sforum extension test --skip-backend-binary \
  ../../extensions/fixtures/plugins/sforum-custom-content

# Real subprocess integration test (Go; needs the dev PostgreSQL,
# defaults to postgres://sforum:sforum@127.0.0.1:15432/sforum):
cd apps/api && go test ./app/Support/Extensions/IntegrationTests/ -run TestReferenceCustomContentPlugin
```

## Reference 5 — Host API v2 fixtures

Use the focused Protocol V2 reference that matches the Host-owned capability
you need:

| Package | What it proves |
| --- | --- |
| `extensions/fixtures/plugins/sforum-notification-reference/` | Typed `EmitNotification` through the runtime-scoped Host broker |
| `extensions/fixtures/plugins/sforum-query-reference/` | Query Registry invocation and result filtering through Protocol V2 |
| `extensions/fixtures/plugins/sforum-commerce-workflow/` | Joined hooks, jobs, services, database operations, and lifecycle behavior; its `replace` route against `core.route.forum.create_topic` declares its **own** `kind: raw_request` guard (`sforum.commerce-workflow.guard.raw`) because raw request authority must be package-declared |
| `extensions/fixtures/plugins/sforum-session-checkout-reference/` | Plugin-namespaced session state: a two-route checkout workflow over `Host.SessionState(ctx)` — cross-request persistence through a real V2 subprocess, host-side erasure, and single-invocation delegation replay denial (`delegationEcho` is a documented integration-test surface) |

CI builds these packages and starts them through the real Protocol V2 starter.
The removed `sforum.contract.hostapi` package was a Protocol V1 ping fixture and
must not be used as an authoring reference.

Related fixtures:

- `sforum-contract-events` — events + `forum.topic.actions` contribution only
- `sforum-contract-schedules` — documents that schedules stay host-owned

## Reference 6 — shortcode declarations

The protected built-in `extensions/builtin/plugins/sforum-shortcodes`
(`sforum-shortcodes`) is the only shortcode runtime in V1. It ships eight
Manifest V3 `content` declarations (`kind: shortcode`) with strict draft-07
schema package files, Protocol V2 typed render handlers, and one trusted L2
editor command. If you are authoring content plugins, this section is what you
may and may not do around shortcodes.

### Host-owned nodes and grammar

- The Host owns exactly two Tiptap node types:
  `sforumShortcodeRef` (block atom, no content) and `sforumShortcodeBlock`
  (block container holding admitted block content, including nested
  shortcodes up to level four).
- Both nodes carry exactly three attributes: `id`, `contractVersion`, and
  `arguments` (never omitted; client HTML, snapshots, authorization results,
  rendered cards, or secrets are never attributes).
- The textual grammar is `sforum.shortcode-text@1`: a tag must occupy a
  standalone logical line (at most three spaces of indentation), opening and
  closing tags are case-sensitive, references require an explicit matching
  closing tag and an empty body, protected blocks require a body, canonical
  arguments are `key="value"` (import also accepts an unquoted positive
  integer), `\[name]` escapes, and code blocks, inline code, raw HTML blocks,
  and tables are opaque. Unknown, malformed, mismatched, repeated-argument,
  over-limit, and placement-invalid text stays literal.
- Frozen limits: 32 shortcode nodes per document, nesting depth 4, 16
  arguments per node, argument key ≤ 64 ASCII bytes, scalar string ≤ 512
  Unicode code points, no arrays/nested objects, render recursion depth 4,
  deterministic `(resource type, ID)` cycle detection.
- Placement: block content only (complete document block or complete block
  inside `sforumShortcodeBlock`); never inside a paragraph, heading, inline
  mark, table cell, or list item text. `only-author` is comment-only.
- A structured API document that falsely claims an identity, version, argument
  schema, placement, or limit fails validation; it is not converted to text.
- V1 admits only the eight frozen Host identities
  (`sforum-shortcodes.user|topic|comment|category|friend-links|login|reply|only-author@1`).
  A brand-new product shortcode requires a Host node/validation/declaration
  change first — a plugin manifest entry alone cannot extend the accepted
  identity set.

### Manifest, Content Registry, and typed render boundaries

- Declarations live under the manifest `content[]` entries with
  `kind: shortcode`, an exact `contractVersion`, a `schema` package file, and
  an exact `handler`; package files must be the current exact digests after
  `extension digest --write` / `extension validate` / `extension test`.
- Schemas are stricter than the Host generic scalar envelope: the four ID
  references require exactly one positive int64, `friend-links` requires an
  empty object, and the three protected blocks require an empty object with a
  non-empty body enforced by Host node validation.
- Renderers emit typed, bounded, sanitized segments through Protocol V2: the
  plugin receives the strict node value (declared arguments), the minimal Host
  projection, and — for an authorized protected render — the exact accepted
  child fragment. It never receives the whole Forum `RenderedContent`,
  raw document, actor, session, permission table, email, IP, moderation
  state, or surrounding body.
- The plugin cannot be an authorization or content authority: the Host owns
  visibility, policy, cache, search projection, and fallback. Disabled,
  uninstall, Safe Mode, stale-artifact, timeout, crash, and invalid-output
  behavior must never depend on the plugin being present; Host fallback copy
  stays available without the exact artifact.

### Closed data surfaces

A shortcode runtime must not:

- read Core database tables or raw content outside the declared Host
  projections;
- receive or construct raw actor/session authority;
- receive Query Registry delegation tokens beyond the exact frozen
  `sforum-shortcodes` seven-query bundle (other content runtimes receive
  none);
- use loopback HTTP, client-side authorization, or client-provided object
  snapshots as authority;
- log or echo arguments, child text, rendered output, actor identity, raw
  errors, or cache values (bounded identity/version/outcome/duration/counts
  only).

See `docs/extensions/host-api-v2.md` for the Host API contract and
`extensions/builtin/plugins/sforum-shortcodes/backend/` for the reference
implementation.

### Fallback, schema, version, and alias rules

- Fallback uses stable Host-owned reason codes
  (`shortcode.user.unavailable`, `shortcode.topic.unavailable`,
  `shortcode.comment.unavailable`, `shortcode.category.unavailable`,
  `shortcode.friend_links.omitted`, `shortcode.protected.unavailable`,
  `shortcode.login.required`, `shortcode.reply.required`,
  `shortcode.only_author.private`). Public metadata exposes only these codes,
  never runtime details. `friend-links` omits public output entirely.
- A failed reference may link to a stable Core URL only after a current Host
  public-visibility recheck; otherwise it is a non-interactive label.
- A declaration version changes when its argument schema, placement,
  authorization meaning, visible success semantics, or fallback secrecy
  changes; copy-only localization does not change it. New declaration
  versions must remain interpretable together with old accepted source.
- Grammar changes require a new `sforum.shortcode-text` version plus paired
  Go/TypeScript fixtures.
- `topic-tag` is the only V1 legacy alias and imports as `category`
  (`tag_id` → `categoryId`); it never survives canonical storage or export.
  `friend_links` is not an alias and stays literal. `password` is explicitly
  deferred/unsupported and must not be implemented as a conversion or
  credential behavior.

### How to add a fixture and conformance case

- Grammar/import corpus: `contracts/fixtures/shortcode-text-v1.json`
  (M2; cases assert `activated`, canonical Markdown, and node summaries for
  topic/comment resources).
- Legacy/conversion report corpus:
  `contracts/fixtures/shortcode-legacy-conversion-v1.json` (M10A; cases carry
  an `expected` classification — `converted`, `literal`, `invalid`,
  `unsupported`, `over-limit` — plus optional canonical output and node
  summaries; structured cases carry `authority: both|host`).
- Add a case entry (valid JSON, classification present, converted cases need
  `canonical` and `nodes`), then run both consumers:
  - Go: `cd apps/api && go test ./app/Support/EditorDocument/`
  - Web: `cd apps/web && bun test tests/framework/editorShortcodes.test.ts tests/framework/editorShortcodeLegacyConformance.test.ts`
- The two consumers must produce identical classification and canonical
  output. If they disagree, do not weaken the fixture: stop and reconcile the
  parsers (or the frozen grammar) first, exactly as recorded for the M10A
  conformance findings.

## Scenario map (which mechanism?)

| I want to… | Use |
| --- | --- |
| Run code after a topic is created | observe `topic.created` (+ optional Host API job) |
| Change or reject a new topic | filter `topic.before_create` |
| Change or reject a topic edit | filter `topic.before_update` |
| Change or reject a new reply | filter `comment.before_create` |
| Reject registration | validate `user.before_register` |
| Reject an upload by metadata | validate `attachment.before_upload` |
| Add a topic detail button | contribution `forum.topic.actions` |
| Add topic badges / sidebar / list pills | `forum.topic.badges` / `sidebar` / `list.badges` |
| Add public nav entries | contribution `forum.nav.items` |
| Swap outbound mail transport | provider `mail.provider` (see `sforum.smtp`) |
| Swap attachment storage | provider `attachment.storage.provider` (see `sforum.storage-fs`) |
| Swap full-text search | provider `search.provider` (Wave E7) |
| Store per-topic/plugin structured data | entity meta (F4.4 / E3) |
| Own HTTP API under the host proxy | manifest `routes` + backend `InvokeRoute`/`RouteTarget` — see [routes.md](./routes.md) |
| Build a backend binary / frontend assets and load them | [build-and-load.md](./build-and-load.md) |
| Call host from the plugin process | Host API + declared `capabilities` |
| Request an AI completion | Host Command `sforum.ai.complete@1` + `ai.invoke` (never a direct vendor call) |
| Give the model a tool of your own | manifest `aiTools[]` + `sforum.ai.tool` provider slot; the operator then lists the tool name in the site AI settings (see [host-api-v2.md](./host-api-v2.md)) |
| Publish an event under your own namespace | Host Command `sforum.events.emit@1` + `events.emit` |
| Run slow, model-calling background work | declare `jobs[].queue: "ai"` so the job uses the site's low-concurrency AI lane |
| End-to-end **workflow** sample | enable `sforum.content-policy` (this section) |
| End-to-end **mail provider** sample | enable `sforum.smtp` + select in Mail settings |
| End-to-end **storage provider** sample | enable `sforum.storage-fs` + select in Attachment settings |

## Manifest checklist

1. **Contract** — explicit `manifestVersion: 3` for new packages; stable `id`,
   semantic `version`, `sforumVersion`, URL, author, and description.
2. **Type** — `plugin` vs presentation-only `theme`; never place business or
   runtime declarations in a theme.
3. **Stable identities** — every V3 declaration uses a namespaced stable id and
   explicit `contractVersion`; replacement targets are declared, not inferred.
   System error page targets are theme-only and L0/L1-only.
4. **Package files** — every referenced executable/frontend/migration/template/
   asset/OpenAPI file has the correct kind and exact SHA-256 digest.
5. **Routes and guards** — explicit action, methods, mode, fallback, schemas,
   contract version, and core/custom guard. Raw request ownership is high-risk.
6. **Database and lifecycle** — authority tier, compatibility range where raw,
   backup/retention, plan/execute, progress, and checkpoint contracts.
7. **Permissions** — definitions use `assignmentPolicy: host`; recommended role
   mappings are advisory and extension code never self-grants. `label` and
   `description` accept either a default string or an extension-owned locale map
   such as `{ "zh-CN": "管理扩展", "en-US": "Manage extension" }`.
8. **Dependencies** — required/optional/conflict/provides semantics resolve to
   one deterministic, acyclic graph.
9. **Host catalogs** — capabilities, events, contributions, providers, and jobs
   use the published generated catalogs.
10. **Notifications** — namespaced inert types bind an exact schema file;
    runtime emission uses `notifications.emit@1`, never raw tables or broadcast.
11. **AI calls** — request completions through `sforum.ai.complete@1` with the
    `ai.invoke` capability so the call enters the site gates, usage accounting,
    and execution trace. Opening your own outbound connection to a model vendor
    bypasses all of it and leaves the operator unable to explain AI cost.
12. **AI tools** — declare tool contributions in `aiTools[]` with a namespaced
    `id`/`handler`, a versioned `contractVersion` matching `id@1`, a model-safe
    `name` (`[a-z0-9_-]`, no dot), a JSON-Schema `parameters` object, and an
    explicit `timeoutMs`. Optional `descriptionI18n` adds console-only localized
    descriptions (a locale map, at most 8 locales, 1024 bytes per value); the
    model still reads `description`. Answer them through the reserved
    `sforum.ai.tool` slot; the declared timeout is the only runtime freedom.
    Contributing a tool needs no `ai.invoke` — the purpose that runs the loop
    pays — and a declared tool is not automatically visible to the model.
    Declare `context` (`locale`/`topic`/`viewer`) when the tool needs
    conversation facts: omitting it means `["locale"]`, and `viewer` is an
    identity disclosure that has to be written down explicitly.
13. **Events** — declare custom event names in your own namespace and publish
    them through `sforum.events.emit@1`; never publish another plugin's
    namespace or a Core-owned name.
14. **Host Commands** — anything that calls a Host Command needs the
    `host_commands` database grant in the manifest, and the reviewed impact
    document must match it exactly.
15. **Operator defaults** — settings include safe defaults and reset-friendly
    recommended values so first-time operators are not blocked.
16. **Brand assets** — `brand/icon.svg` (512 viewBox), `brand/icon.png`
    (512×512), and `brand/social-preview.png` (1280×640) exist and pass
    `extension brand check`. They are not `packageFiles`; they are still part of
    the package tree digest. See [`brand-assets.md`](./brand-assets.md).

## Host API methods (v1)

Version id: `sforum.host/v1`. Env injected by the host:

- `SFORUM_HOST_API_URL`
- `SFORUM_HOST_API_TOKEN`
- `SFORUM_EXTENSION_ID`
- `SFORUM_HOST_API_VERSION`

| Method | Capability | Purpose |
| --- | --- | --- |
| `Ping` | `host.api` | Connectivity probe |
| `GetSettings` | `settings.own` | Read this extension's settings |
| `CheckPermission` | `permissions.check` | Ask if a user holds a permission |
| `EnqueueOwnJob` | `jobs.enqueue` | Enqueue a **declared** job kind |
| `AppendAudit` | `audit.append` | Append namespaced audit events |
| `GetUserSafe` | `users.read` | Read non-secret user fields |

SDK helpers: `pluginsdk.Ping`, `GetSettings`, `CheckPermission`, `EnqueueOwnJob`,
`AppendAudit`, `GetUserSafe`.

> **The v1 HTTP surface is not the protocol-v2 runtime path.** The Host currently
> issues no `SFORUM_HOST_API_*` variables to plugin subprocesses, so a v2 plugin
> cannot reach these methods at runtime; use the typed Host API v2 client
> instead. In particular, `Ping` does not expose the Host version — read
> `Server.HostBuild()` from the handshake (see
> [`host-api-v2.md`](./host-api-v2.md#host-build-identity)).

## Stable Host Queries (Protocol V2)

Stable core reads are served by `HostQueryService` over Protocol V2 RPC — no
raw SQL, no core connection. Calling any stable query requires the exact
active artifact **and** the `core_views` database grant in the manifest
(`database.grants: ["core_views"]`); the reviewed impact document must carry
the same grant set. Rows arrive as schema-validated typed documents: BIGINT
columns are decimal strings, INTEGER columns are numbers, nullable columns may
be null.

| Query id | Shape | Filters (eq only) | Sorts | Notes |
| --- | --- | --- | --- | --- |
| `sforum.core.safe_user.by_id` | single | `id` (required) | — | id, username, display name, timestamps |
| `sforum.core.safe_user.by_username` | single | `username` (required) | — | same fields as `by_id` |
| `sforum.core.safe_users.batch` | list | `user_id` (required, id list) | `user_id` | adds live avatar metadata; users without a live avatar keep null avatar columns |
| `sforum.core.public_topics.list` | list | `category_id`, `author_user_id` | `id`, `created_at`, `updated_at`, `last_activity_at` | public rendered content plus comment/view counters; default order `last_activity_at DESC, id DESC` |
| `sforum.core.public_topic.by_id` | single | `id` (required) | — | same fields as the list |
| `sforum.core.public_comments.list` | list | `topic_id` (required), `author_user_id` | `id`, `created_at`, `updated_at`, `depth` | default order `created_at ASC, id ASC`, matching the public comment stream |
| `sforum.core.public_categories.list` | list | `slug` | `id` | public categories only (group visibility enforced) |
| `sforum.core.public_attachment.by_public_id` | single | `public_id` (required) | — | public metadata only, never storage internals |
| `sforum.core.site_stats.current` | single | — | — | topic / comment / user / public-attachment totals |

Shared execution bounds: eq filters only; default page 20 rows, maximum 100;
5s deadline; Host-owned cost budget; results are not cached yet (no
entity-event invalidator). An id-list filter takes a **canonical JSON array**:
strictly ascending, unique, positive int64 ids, at most 100 — e.g.
`"[1,5,9]"`. Missing ids simply do not return a row.

### Capability-gated consent read (own email)

One identity field is deliberately outside that table: the signed-in actor's
own email. It is **never** in `sforum_core_v1`, never in a public query, and
never addressable by a user id. Declare the `users.email.read` capability and
read it through the Host-delivered consent delegation:

| Query id | Fields | Notes |
| --- | --- | --- |
| `core.query.identity.self_email` (`@1`, plan `.plan@1`) | `email`, `email_verified`, `updated_at` | Result schema `sforum.core.self_email@1`; single row; actor comes only from the Host-issued delegation; no filters/sorts; never cached |

The Host mints the single-use delegation on route/admin invocations that have a
live actor, and only when the exact artifact declares the capability. Call
`host.SelfEmailQueryRequest(incomingContext)` (or `DelegatedQueryRequest`) with
the incoming `RequestContext`; the request itself stays actorless. Anonymous
calls, background jobs, and artifacts without the capability receive no
delegation. Every successful read is audited (`identity.email.consent.read`,
never with the value) and rate limited per (actor, extension). See
`docs/extensions/host-api-v2.md` → "Consent-gated reads" for the full contract,
error reasons, and the reference fixture
`extensions/fixtures/plugins/sforum-consented-claim-reference`.

A need the catalog cannot express goes through the surface-request process in
`knowledge/decisions/2026-10-18-extension-data-surface-policy.md` — never
through raw-core grants or per-plugin RPC methods. The same read models are
also exposed as stable SQL views under `sforum_core_v1`
(`safe_users`, `forum_topics`, `forum_comments`, `public_categories`,
`safe_user_avatars`, `site_stats`, `public_attachment_metadata`,
`public_entity_meta`) for plugins holding the `core_views` lease that prefer
writing their own SQL against their own schema connection.

## Event rules of thumb

- **filter / validate** — default `fail_closed`, short timeout (see catalog). Never send mail, call remote APIs, or reindex inside a filter; enqueue a job.
- **observe** — async; failures should not break the write path.
- Prefer Host API or River jobs for anything that can take more than a few hundred milliseconds.

### Which filter for which scenario

| Scenario | Event | Kind | Can patch | Notes |
| --- | --- | --- | --- | --- |
| Block or rewrite a new topic (title/tags/category/body) | `topic.before_create` | filter | `categorySlug`, `tagSlugs`, `title`, `content` | Runs after permission check; host re-validates after patch |
| Block or rewrite a topic edit (title/tags/category/body) | `topic.before_update` | filter | `categorySlug`, `tagSlugs`, `title`, `content` | After edit permission + author edit window; payload only includes fields present in the request; plugins may still patch missing fields (e.g. force tags). Host re-validates after patch |
| Block or rewrite a new reply body | `comment.before_create` | filter | `content` only | After auth + topic active check; before content limits / render / commit. `parentId` is payload-only (not patchable in v1) |
| Block registration (e.g. disposable email) | `user.before_register` | validate | — (reject-only) | After host field/password policy parse, before password hash / user row. Payload: `username`, `email`, `locale` only — **never password**. Prefer stable `reason` codes; minimize PII in plugin logs |
| Block upload the core allowlist would accept | `attachment.before_upload` | validate | — (reject-only) | After host MIME sniff / size / extension policy, before storage `Put`. Payload: `actorUserId`, `contentType`, `sizeBytes`, `filename` only — **never raw file bytes** |
| Side effects after commit | `user.registered`, `topic.created`, `topic.updated`, `comment.created`, `attachment.uploaded`, … | observe | — | Async; do not use for rejection |

Reject with a stable `reason` code (mapped to the API error envelope as 422). Do not put passwords, raw file bytes, or stack traces in filter/validate payloads or messages.

## Job queues

Declared jobs run on the `default` River queue unless the manifest says
otherwise:

```json
{
  "jobs": [
    {
      "id": "vendor.plugin.job.summarize",
      "contractVersion": "vendor.plugin.job.summarize@1",
      "name": "vendor.plugin.summarize",
      "handler": "vendor.plugin.job.summarize",
      "payloadSchema": "vendor.plugin.job.summarize.payload@1",
      "retryPolicy": "bounded",
      "queue": "ai"
    }
  ]
}
```

- `queue` is optional and its accepted values are a deliberately closed set:
  omit it (or write `"default"`) for ordinary work, `"ai"` for slow,
  model-calling work. Nothing else is accepted — `critical`, `mail`,
  `notifications`, `search`, and `maintenance` carry Core's own delivery, and a
  plugin must not be able to decide how long a site waits for its mail or
  notifications.
- The `ai` lane is the same queue Core's AI jobs use, so its concurrency is
  capped by `JOB_QUEUE_AI_WORKERS` (default 2). Choosing it means your jobs stop
  competing with ordinary work for the default workers — it does not mean more
  parallelism.
- Queue choice changes nothing else: admission, exact-artifact contract checks,
  retry policy, and the per-extension concurrency limit all still apply.
- Scheduled jobs inherit the queue declared by the job they tick, so a scheduled
  model-calling job does not fall back onto the default queue.

## Schedules

Do not start `time.Ticker` / cron loops in the plugin process. Core schedules
are listed in [schedules.md](./catalogs/schedules.md). Plugin-owned periodics
will go through the host registry in a later wave.

## Admin / trusted components

Prefer Schema or Settings Actions. For complex settings, ship the prebuilt
Admin Micro-frontend API v1 contract. It is **full trust** on the admin origin
after exact digest approval. Runtime Vue SFC compilation, executable admin
slots, remote scripts, and extension-triggered host builds are unsupported. Read
[trusted-admin-components.md](./trusted-admin-components.md).

Plugins may also declare full page-body components with
`admin.pages[].view: component`. These pages inherit the Host admin layout and
may be authored as Vue SFCs compiled to package-local `.mjs`/`.css`; the
production Host never rebuilds Nuxt. Use a page-specific permission when the
surface represents a distinct grantable capability.

`make:plugin --vue-admin-page` supplies the typed mount adapter, Vite build,
and `@sforum/plugin-ui@1` page/section/form/table/feedback/icon components. The
SDK is bundled into the plugin output and deliberately does not expose
Host-private Nuxt UI or `SF*` imports as a compatibility contract.

### Host icons in plugin pages

Plugin page bodies and public L2 components can use the Host icon set without
bundling one. Pass a Host icon name to `SPluginIcon` or to the `icon` prop of
`SPluginButton`, `SPluginSection`, `SPluginAlert`, `SPluginEmptyState`, and
`SPluginTable` columns:

```vue
<SPluginSection title="Queue" icon="i-lucide-inbox">
  <SPluginButton icon="i-lucide-refresh" @click="reload">Refresh</SPluginButton>
</SPluginSection>
```

- The name grammar is `i-(lucide|tabler)-<kebab-name>`, matching the Host icon
  picker and the descriptor `icon` fields used by contributions.
- The SVG is rendered and sanitized by the Host and delivered through the
  versioned `/_sforum/icons` surface; the plugin supplies a name only, so it
  never ships an icon package and never inlines its own SVG.
- Icons are decorative unless you pass `label` to `SPluginIcon`.
- Unknown names, a Host without the icons bridge, and failed requests all
  degrade to an empty placeholder with one console warning — they never throw
  and never block the page from mounting.
- Mount pages through `definePluginPage` (from `@sforum/plugin-ui`), which
  injects the resolver. The scaffold does this for you.

### Author frontend SDK version

A `--vue-admin-page` workspace depends on `@sforum/admin-sdk@^1.1.0` and
`@sforum/plugin-ui@^1.1.0`: the icon bridge member, `SPluginIcon`, the `icon`
props, and `definePluginPage` all arrive in 1.1.0. The repository publishes both
packages from the tag-driven Release workflow, so the version you pin must
already exist on npm before `bun install` can resolve it.

To build against the SDK packages in a local SForum checkout — for example to
use an unreleased component — pass the development-only override:

```bash
cd apps/api
go run ./cmd/sforum extension build --sdk-dir ../../apps/web/packages <package-root>
```

`--sdk-dir` (or `SFORUM_SDK_DIR`) rewrites the workspace dependencies to the
local packages for that build only, then restores `frontend/admin/package.json`
and the Bun lockfile byte for byte. Never use it for a release build.

## Validation and packaging commands

```bash
# Parse + merge includes, print summary
go run ./cmd/sforum extension validate <package-root>

# Build author frontend assets, refresh digests, validate, and contract-test
go run ./cmd/sforum extension build <package-root>

# Refresh inline Manifest V3 packageFiles digests and revalidate
go run ./cmd/sforum extension digest --write <package-root>

# Host contract report (exit 1 on errors)
go run ./cmd/sforum extension test <package-root>
go run ./cmd/sforum extension test --json <package-root>

# Release zip (omit common source/dev files)
go run ./cmd/sforum extension package <package-root> --exclude-source

# Catalog documentation drift
go run ./cmd/sforum extension docs generate --check
```

Full flag tables and release loop:  
[中文 · 开发者 CLI](../zh-CN/development/cli.md) ·
[English · Developer CLI](../en-US/development/cli.md).  
End-to-end build, digest, load, and iteration walkthrough:
[build-and-load.md](./build-and-load.md).

## Contribution points (F4.3 / E2)

Declare only host-owned points (see [contribution-points.md](./catalogs/contribution-points.md)).
Payloads are JSON descriptors — **never executable code**.

| Point | Payload type | Runtime consumer |
| --- | --- | --- |
| `forum.topic.actions` | `extensionRoute` | Topic detail `extensionActions` |
| `forum.topic.sidebar` | `topicSidebarCard` (`extensionRoute` \| `hostLink`) | Topic detail `extensionSidebar` (default theme side cards) |
| `forum.topic.badges` | `topicBadge` (`tone` + optional host `href`) | Topic detail `extensionBadges` (under title) |
| `forum.comment.actions` | `extensionRoute` (+ optional `requiresAuth`) | Comment list `extensionActions` (row menus) |
| `forum.nav.items` | `navItem` (`hostLink` \| public `extensionRoute` GET) | `GET /site/nav-items` → `extensionItems` (default theme navbar) |
| `forum.topic.list.badges` | `topicBadge` | Topic list `extensionListBadges` (list-row pills) |
| `forum.composer.toolbar` | `extensionRoute` | `GET /composer/toolbar` + composer UI |
| `forum.profile.tabs` | `profileSection` (`extensionRoute` \| `hostLink`) | Public profile `extensionTabs` |
| `admin.dashboard.widgets` | `dashboardLink` (`adminLink` + admin route) | Admin overview `extensionWidgets` |
| `system.health.checks` | `healthDescriptor` (`static` \| `extensionRuntime`) | Merged into `GET /ready` without plugin RPC |

`forum.page.regions` places content into a named area of a public page
(`content_before`, `detail_before`, `comments_before`, `sidebar_top`, …). The
per-page whitelist and what each region means are in
[page-catalog.md](./page-catalog.md#standard-page-regions-forumpageregions);
the web-side whitelist is generated from the Go catalog, so never hand-edit it.

### Topic secondary surfaces (E2.1)

- **Sidebar** title/icon come from contribution `label` / `icon` (approved
  `i-lucide-*` / `i-tabler-*` only). Payload chooses target only.
- **Badges** use host `tone` enum: `neutral|info|success|warning|danger`.
  Optional `href` is a site-relative path (not `/api`, not external).
- Themes must render empty-safe: omit UI blocks when arrays are empty.
- No public executable extension injection; actions still go through the extension route
  proxy and host policy.

### Comment row actions (E2.2)

- Same payload spirit as `forum.topic.actions`: `extensionRoute` with
  `POST|PUT|PATCH|DELETE` only (no GET).
- Optional `requiresAuth: true` is a **UX hint** — the default theme hides the
  control for guests; the extension route proxy still enforces login/policy.
- Host returns descriptors once on `CommentList.extensionActions` (not per
  comment row). Clients attach them to each row menu and POST
  `{ topicId, commentId }` to the proxy path.

### Public navigation (E2.3)

- Point: `forum.nav.items`, payload type `navItem`.
- **Merge order (documented):** core / operator-configured `items` first;
  contribution `extensionItems` second (by contribution `order`).
- `hostLink`: site-relative path only — **not** `/api`, **not** `/admin`.
- `extensionRoute`: **GET only** (public page open via extension route proxy).
- Response: `GET /site/nav-items` returns
  `{ items: SiteNavItem[], extensionItems?: SiteExtensionNavItem[] }`.

### Topic list badges (E2.4)

- Same `topicBadge` payload as detail badges (`tone` + optional host `href`).
- Host returns descriptors once on `TopicList.extensionListBadges` (not per
  topic row). Themes attach the same set to each list row; no per-row plugin
  RPC and no custom components.

## Entity meta / custom fields (F4.4)

Host-owned EAV fields on `user` and `topic` — **no** per-plugin `ALTER` on core
tables.

- Admin defines fields (`entity_meta.manage`): key, type
  (`string|text|number|boolean`), visibility (`public|owner|admin`).
- Values: `GET/PUT /api/v1/entity-meta/{entityType}/{entityId}` (visibility
  filtered).
- Observe event: `entity_meta.updated` after successful value writes.

## Feature flags vs permissions (F4.5)

| Concern | Mechanism |
| --- | --- |
| Who may act | RBAC permission keys |
| Whether a product surface is on | `features.*` runtime options |

Declare product prerequisites on plugins only:

```json
"requiresFeatures": ["features.search"]
```

Enable fails with `extension.features_required` if any listed flag is disabled.
Themes must not declare `requiresFeatures`. Public `GET /web-options` exposes
only safe public flags; restore defaults via
`POST /admin/features/restore-defaults`.

## Related docs

- [Host catalogs (generated)](./catalogs/README.md)
- [Manifest V3 schema catalog (generated)](./catalogs/manifest-v3.md)
- [Extension platform v2](../extension-platform-v2.md)
- [Trusted admin components](./trusted-admin-components.md)
- [Migrating from SForum-old to V3 contracts](./migrating-from-sforum-v2.md)
- Fixtures README: `extensions/fixtures/README.md`
