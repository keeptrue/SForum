# Trusted Admin Components

SForum settings have three presentation levels:

| Need | Contract | Author JavaScript | Operator build |
| --- | --- | --- | --- |
| Ordinary fields and layout | Schema UI | No | No |
| Provider probes and host operations | Schema + Settings Actions | No UI code | No |
| Complex interactive workflow | Prebuilt Admin Micro-frontend API v1 | Fully trusted prebuilt module | No |

Use Schema UI unless a workflow genuinely needs custom client behavior. There
is no runtime Vue SFC compilation, static admin registry, remote script URL, or
extension-triggered Nuxt build.

Ordinary plugin-owned admin pages use the same prebuilt artifact boundary. The
Host owns the admin layout, sidebar, topbar, tabs, route middleware, and page
heading; the component mounts only inside the page body.

## Package contract

Declare component mode in the versioned Settings Document and keep `fields` as
the required Schema fallback:

```json
{
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
  "fields": [
    { "key": "message", "label": "Message", "type": "text", "default": "Hello" }
  ]
}
```

The files must already be built when the package is created. Bundle framework
dependencies into the module; operators never install extension frontend
dependencies. Entry/CSS paths are package-relative, path-contained, size
bounded, and restricted to `frontend/admin/dist/*.mjs|*.css`.

### Plugin-owned admin page

Declare `view: component` on an `admin.pages[]` item. A plugin may declare
multiple pages, but every component id must be unique inside the package:

```json
{
  "admin": {
    "entry": "/dashboard",
    "pages": [
      {
        "path": "/dashboard",
        "label": "Plugin dashboard",
        "icon": "i-lucide-layout-dashboard",
        "view": "component",
        "menu": true,
        "permission": "acme.dashboard.view",
        "component": {
          "id": "dashboard",
          "apiVersion": 1,
          "entry": "frontend/admin/dist/dashboard.mjs",
          "css": "frontend/admin/dist/dashboard.css"
        }
      }
    ]
  }
}
```

The entry exports the same `apiVersion` and `mount(target, bridge)` shape. Its
page bridge supplies `page`, namespaced `request`, `toast`, `t`, `navigate`,
locale, and appearance. `request` resolves to your extension route's own JSON
body (not a Host envelope) — see "`bridge.request` return contract" below. It
deliberately does not expose settings draft methods.
Plugin authors may compile Vue SFC source to this module; production SForum
loads only the prebuilt output.

### Host actions on an admin page

A `view: component` page may additionally ask the Host to perform an action it
owns. Two actions exist: `extension.install` stages a package, and
`extension.install.enable` installs it and then lets the Host enable or upgrade
it through its own lifecycle flow:

```json
{
  "path": "/catalog",
  "label": "Catalog",
  "view": "component",
  "hostActions": ["extension.install"],
  "component": { "id": "catalog", "apiVersion": 1, "entry": "frontend/admin/dist/catalog.mjs" }
}
```

Rules, enforced at manifest validation:

- `hostActions` is rejected on `about` / `settings` pages — an action needs a
  mounted component to call it.
- Only known action ids are accepted: `extension.install` (stage only) and
  `extension.install.enable` (install, then let the Host enable or upgrade).
  Unknown ids reject the whole manifest.
- Each declared action injects its own bridge member, so a page that declares
  only `extension.install.enable` never receives `installExtension` by accident,
  and vice versa. A page that wants both declares both.
- At most 8 entries; empty strings are dropped and duplicates collapse
  (trimmed, lowercased).
- Hosts older than this field reject the manifest (`additionalProperties:
  false`), and a Host that predates `extension.install.enable` rejects the whole
  manifest as an unknown action id. Either way, publishers must state a minimum
  Host version in their release notes.

Declaring an action is disclosed: the plugin detail card, the theme detail card
and the enable confirmation dialog show "this plugin can request extension
installs", plus a second line for `extension.install.enable` explaining that the
Host may also enable what it installs. The disclosure is a separate line item
with its own i18n keys — it is **not** folded into capability risk levels,
because it is a Host action rather than a process capability the plugin itself
gains.

### `bridge.installExtension`

Present **only** when the current page declared `extension.install`; otherwise
the member is `undefined` and plugins must feature-detect:

```ts
const installed = await bridge.installExtension?.({
  url: 'https://cdn.example.com/sforum.store-1.4.0.zip',
  sha256: '9f2c…',            // 64 lowercase hex, required
  expectedExtensionId: 'sforum.store',
  expectedType: 'plugin',     // selects extension.plugin.manage / extension.theme.manage
  expectedVersion: '1.4.0'    // optional
})
```

What the Host does:

1. Validates the request, then pre-checks the permission for the declared type.
   A caller without it gets `install_permission_denied` and sees no dialog.
2. Shows its own confirmation dialog: URL host (full URL expandable), SHA-256,
   declared id/version/type, required permission, a `super_admin` note, and an
   explicit "the Host verifies after download" line. Cancelling sends nothing.
3. Sends one `POST /admin/extensions/install-from-url` with a **120 s** budget
   and the declared identity plus an audit `source` label
   (`admin-page:<extension id>`). The 30 s default of `useApiClient` would
   abort a healthy slow download, so the bridge passes the budget explicitly.
4. Compares the parsed manifest against the declared identity **after** parsing
   and **before** the type permission switch. Any mismatch aborts with
   `extension.expected_mismatch` (the response carries the manifest's actual
   id/type/version) instead of staging a different package.

What it deliberately does not do:

- No enable, trust, activate, or upgrade. A resolved promise means **staged**.
  The administrator finishes trust/enable on the core plugin/theme pages, which
  the success state links to. For install-and-enable, declare
  `extension.install.enable` and use `bridge.installAndEnableExtension` below.
- No queue, intent row, or worker. The call is synchronous; navigating away
  cancels the request (the browser disconnect cancels the Go context) and the
  promise rejects with `install_cancelled`. The dialog says so while installing.
- One dialog and one install at a time per page; a second concurrent call is
  rejected with `install_in_progress`.

Rejection codes: `install_permission_denied`, `install_cancelled`,
`install_in_progress`, `install_invalid_request`, `install_failed`. Match on
`error.code`, never on the text.

**This dialog is not a sandbox.** A trusted admin page runs same-origin in the
Host DOM and can call the install route directly. What actually bounds
installation is the service-layer authorization, the `super_admin` requirement
for untrusted executable backends, digest pinning, and the fact that an install
only stages an inert package. The dialog exists so the normal path is explicit
and reviewable.

### `bridge.installAndEnableExtension`

Present **only** when the current page declared `extension.install.enable`:

```ts
const result = await bridge.installAndEnableExtension?.({
  url: 'https://cdn.example.com/sforum.store-1.4.0.zip',
  sha256: '9f2c…',            // 64 lowercase hex, required
  expectedExtensionId: 'sforum.store',
  expectedType: 'plugin',
  expectedVersion: '1.4.0'    // optional
})
result?.outcome // 'enabled' | 'upgraded' | 'staged'
```

It takes the same request shape and runs the same Host install path as
`bridge.installExtension`, then continues into the Host's own enable flow. The
resolved value carries the install identity block (`extensionId`, `version`,
`type`, `packageDigest`, `upgraded`, `requiresTrust`) plus `outcome`, which is a
report of what the Host reached and never a promise:

| `outcome` | Meaning |
| --- | --- |
| `enabled` | A new package was installed and is now running. |
| `upgraded` | A staged replacement was installed and the upgrade/restart lifecycle action succeeded. |
| `staged` | The Host installed the package and deliberately stopped. |

`staged` covers three honest stops: a theme (the operator activates themes on
the theme page), an **untrusted** executable backend with a non-super-admin
operator, and an operator who closed the impact review. The stage-only member has
no `outcome` field at all — a resolved `installExtension` already means staged. A
front-end-only package, or an artifact that is already trusted, enables
immediately.

The trust review happens inside the same dialog: the Host renders its impact
report (`sforum.trust-impact@2`) and mints the one-use confirmation before
calling the lifecycle route. If the backend still answers
`extension.trust_challenge_required` to a tokenless enable, the Host falls back
to that review instead of failing the call.

Failure codes are the same set as `bridge.installExtension`. `install_failed`
additionally covers an enable that failed after a successful install: the
package stays installed, and the retry repeats only the enable — it never
re-downloads the package.

The enable step calls the ordinary lifecycle routes
(`POST /admin/extensions/{id}/enable`, or the same upgrade/restart action the
extension page uses) with an `Idempotency-Key`. There is no second enable
implementation behind the bridge and no trust bypass.

### Dialog layering (host dialog above plugin overlays)

The Host dialog is usually not the only modal on the page: a catalog page has
its own overlay. Three facts decide the stack:

- Nuxt UI 4's `UModal` renders its overlay and content with **no z-index**, so it
  relies on DOM order.
- A plugin page's overlay is a positioned `body` child. At `z-index: 40` it
  paints above the Host dialog whatever the DOM order is.
- While a reka modal layer is open, `DismissableLayer` sets
  `body.style.pointerEvents = 'none'` and restores `auto` only on its own layer
  content. An overlay above the Host dialog therefore swallows every click: the
  dialog is invisible and the page looks frozen.

The contract, as implemented by the install/confirm dialog that opens above a
plugin page:

| Layer | z-index |
| --- | --- |
| Host install dialog overlay and content | `ADMIN_HOST_DIALOG_Z_INDEX` = 80 (literal `z-[80]`) |
| Plugin overlays | `≤ z-40` |
| Toasts | `z-100` |

The number has one name in code: `ADMIN_HOST_DIALOG_Z_INDEX`, exported from
`apps/web/app/utils/admin/extensions/hostActions.ts`. The component keeps the
literal `z-[80]` because Tailwind has no safelist here and cannot see an
interpolated candidate; a unit test keeps the two in sync. The `≤ 40` side is a
cross-repository convention and is not machine-checked.

Do not raise a plugin overlay above the Host dialog. The earlier workaround —
dropping your own overlay to `z-index: 0` while an install is pending — is gone:
the plugin overlay keeps its `z-index: 40` and never rewrites it during an
install.

## Vue authoring with Plugin UI SDK v1

For a first-class page, start with the supported scaffold instead of writing
the mount adapter or CSS by hand:

```bash
go run ./cmd/sforum make:plugin ... --vue-admin-page
sforum extension build --allow-scaffold <package-root>
```

The scaffold consumes the public `@sforum/admin-sdk@1` and
`@sforum/plugin-ui@1` npm packages. Existing Vue workspaces can install the
same contracts with `bun add @sforum/admin-sdk@^1 @sforum/plugin-ui@^1`.

### Registry release ownership (maintainers)

The tag-driven Release workflow publishes both packages through npm Trusted
Publishing with provenance. It is idempotent when an existing version holds the
same content: the published tarball and the freshly packed tarball are compared
by unpacked member digests, not by tarball bytes, because gzip output differs
between Node/npm toolchains. Genuinely changed content under an existing version
fails the release before image promotion.

An npm package must exist before its Trusted Publisher can be configured. The
one-time namespace bootstrap was completed on 2026-08-20: the `sforum` owner
used interactive 2FA to publish the reviewed tarballs:

```bash
node scripts/ci/pack-web-sdks.mjs /tmp/sforum-web-sdks
npm publish /tmp/sforum-web-sdks/sforum-admin-sdk-1.0.0.tgz --access public
npm publish /tmp/sforum-web-sdks/sforum-plugin-ui-1.0.0.tgz --access public
```

Both package settings are now bound to the GitHub Actions Trusted Publisher
organization `keeptrue`, repository `SForum`, workflow filename
`release.yml`, no environment, allowed action `npm publish`. Do not repeat the
bootstrap or add a long-lived npm token to the repository. Later releases use
`.github/workflows/release.yml` exclusively. Both packages use npm's strict
publishing-access option: interactive publication requires 2FA and bypass-2FA
tokens are disallowed; Trusted Publishing remains available. The interactive
`1.0.0` bootstrap versions have no provenance; verify provenance on the next
SDK version that the OIDC Release job actually publishes.

`@sforum/plugin-ui@1` currently provides:

- layout: `SPluginPage`, `SPluginSection`;
- forms: `SPluginButton`, `SPluginField`, `SPluginInput`, `SPluginSelect`;
- feedback: `SPluginAlert`, `SPluginEmptyState`;
- data: `SPluginTable`;
- icons: `SPluginIcon`.

The generated Vite build bundles Vue and these components into the plugin's
own ESM/CSS. The SDK reads stable SForum appearance variables with standalone
fallbacks, so authors normally write no CSS. It does not import Nuxt UI, Nuxt
composables, Host route modules, or private `SF*` components. Those remain
implementation details rather than a plugin ABI.

The generated `admin.ts` mounts through `definePluginPage`, which injects the
Host icon resolver. `AdminDashboard.vue` receives the typed
`AdminPageBridgeV1`, uses SDK components like ordinary Vue components, and can
call namespaced APIs, Host Toasts, translation, and navigation through that
bridge.

### Host icons

`SPluginIcon` (and the `icon` prop on `SPluginButton`, `SPluginSection`,
`SPluginAlert`, `SPluginEmptyState`, and `SPluginTable` columns) uses the Host
icon set instead of a plugin-bundled one:

```vue
<SPluginSection title="Queue" icon="i-lucide-inbox">
  <SPluginEmptyState icon="i-lucide-inbox" title="Nothing queued" />
</SPluginSection>
```

- Names use the Host grammar `i-(lucide|tabler)-<kebab-name>`. A name outside
  that grammar is ignored locally and never reaches the Host.
- The Host renders and sanitizes the SVG; plugins supply a name only. This is
  why a plugin never bundles an icon package and never inlines its own SVG.
- `SPluginIcon` is decorative by default. Pass `label` when the icon carries
  meaning, and the element becomes `role="img"` with that accessible name.
- An unknown name, a Host that exposes no `icons` resolver, or a failed request
  degrades to an empty placeholder plus a single console warning. It never
  throws and never blocks the page from mounting.

`definePluginPage` is the only supported mount adapter because it is what wires
the resolver; a hand-written `createApp` call still works but silently loses
icons.

## Module API

```js
export const apiVersion = 1

export async function mount(target, bridge) {
  const button = document.createElement('button')
  button.textContent = bridge.t('save')
  button.onclick = () => bridge.settings.save()
  target.append(button)

  return () => button.remove()
}
```

The v1 bridge supplies:

- extension id/version, locale, and appearance tokens;
- declared settings metadata, current draft values, update/save/reset;
- namespaced extension API requests;
- host Toasts, translation, and admin navigation;
- an optional `icons` resolver (`bridge.icons?.resolve(names)`), present from
  `@sforum/admin-sdk@1.1.0`. It is a presentation convenience over public
  static data, not an authorization grant, and a plugin must tolerate its
  absence.
- an optional `installExtension(request)`, present from `@sforum/admin-sdk@1.2.0`
  and only on pages that declared `hostActions: ["extension.install"]` — see
  "Host actions on an admin page" above.
- an optional `installAndEnableExtension(request)`, present from
  `@sforum/admin-sdk@1.3.0` (the SDK version that also adds `bridge.host`) and
  only on pages that declared `hostActions: ["extension.install.enable"]`. It
  resolves with the install identity plus `outcome: 'enabled' | 'upgraded' |
  'staged'`.
- an optional `host` identity (`{ coreVersion, buildVersion, valid }`), present
  from `@sforum/admin-sdk@1.3.0`. `coreVersion` is the only compatibility
  authority; `buildVersion` is display-only (`dev-abc12`). The field is sourced
  from the same `page-bootstrap` payload that already carries the trust state,
  so reading it costs no extra request. A Host that predates it omits it — treat
  the absence as unknown and never guess a version.

It does not expose Cookie or Authorization values. The extension API remains
permission checked by the backend; bridge access is not an authorization grant.

### `bridge.request` return contract

`bridge.request(path, options)` resolves to **the JSON response body of your
extension route itself** — the body your own response schema declares, not a
Host `{ code, message, data }` envelope:

```js
const catalog = await bridge.request('/api/store/catalog')
catalog.items // the array your route returned at the top level
```

The Host unwraps its own envelope for its own routes. Extension routes have no
envelope, so the page bridge asks the request layer for the raw body. Practical
consequences:

- Respond with the payload directly (for example `200` plus
  `{ items, total, fetchedAt }`). If you wrap it in `{ code, message, data }`,
  your page receives the wrapper, not `data`.
- Non-2xx responses still reject, with the Host's usual error mapping, CSRF
  double-submit handling, and timeouts — including unsafe methods such as the
  `POST` that stages an install.
- `options` (`method`, `body`, `headers`, `timeout`) is passed through to the
  Host request layer. The response mode is a Host-owned contract and cannot be
  overridden from the plugin side.

## Trust and loading

An uploaded component is not loaded until an active `super_admin` completes the
one-use confirmation. The challenge is bound to actor, extension id, version,
API version, component id, and digest and expires after five minutes.

The durable technical boundary is the stored digest grant, not the confirmation
code. It binds extension id, version, API version, component id,
`adminFrontendDigest`, and package identity. Changed entry/CSS bytes invalidate
old trust. Assets are served only from the installed package through:

```text
GET /_sforum/private-assets/extensions/{id}/{digest}/entry
GET /_sforum/private-assets/extensions/{id}/{digest}/style
GET /_sforum/private-assets/extensions/{id}/{digest}/{componentId}/entry
GET /_sforum/private-assets/extensions/{id}/{digest}/{componentId}/style
```

The Host resource namespace streams to the authenticated Go asset handler; the
legacy `/api/v1/admin/extensions/{id}/frontend/assets/{digest}/{asset}` path is
an internal/compatibility upstream, not the browser-facing URL. Responses use
immutable private caching, exact
digest verification, MIME checks, `nosniff`, and
`Cross-Origin-Resource-Policy: same-origin`.

## Host icon surface

`/_sforum/icons` is the Host-owned, versioned source of plugin-facing icons. It
serves only public static data generated from the Host's own bundled
`@iconify-json` collections; no authorization is involved, and no plugin ever
supplies markup.

```text
GET /_sforum/icons?names=i-lucide-inbox,i-tabler-x   # ≤32 names, JSON { items: [{ name, svg }] }
GET /_sforum/icons/{lucide|tabler}/{name}.svg        # one standalone document
```

- Names must match `i-(lucide|tabler)-<kebab-name>`. Anything else is rejected;
  in the batch response an unknown or malformed name is simply omitted, so one
  bad name cannot blank a component.
- Every body is rebuilt through an element/attribute allowlist before it leaves
  the process: `script`/`style` blocks, `on*` handlers, non-allowlisted
  elements, and non-fragment references are dropped, and fragment ids are
  namespaced per icon. The current source is a first-party build-time
  dependency; the sanitizer keeps a future icon-source change from silently
  becoming an XSS surface.
- Responses are `1em`-sized inline SVG that inherits `currentColor`, with
  immutable public caching, `nosniff`, and `Cross-Origin-Resource-Policy:
  same-origin` on the document route.
- Consumers: `AdminPageBridgeV1.icons` / `PublicFrontendBridgeV1.icons`,
  `@sforum/plugin-ui`'s `SPluginIcon` and component `icon` props, and the theme
  template `{{ icon "…" }}` helper via the Host client runtime.

## Failure behavior

Missing, revoked, or invalidated trust never blocks settings. Import failure,
API mismatch, CSS failure, invalid cleanup, mount failure, or repeated
browser-session failures disposes the component and renders Schema UI. The
administrator may explicitly return to Schema UI; doing so does not delete
settings, secrets, backend state, or the package.

Component code is fully trusted after approval and is not sandboxed. Package
provenance, explicit approval, immutable digest identity, backend permissions,
namespaced APIs, error isolation, quarantine, and Schema fallback are the
controls.

Source convenience does not change the production trust boundary: uploaded
`.vue` files are never compiled, and package-local Vite/Nuxt configuration is
never executed by SForum. Release zips should be made with `--exclude-source`.

For ordinary admin pages, the same failures preserve the Host admin shell and
show a retryable page-local error. A page component is not served while its
extension is disabled or when the actor lacks its declared permission.
