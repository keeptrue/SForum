# Trusted admin pages get host-mediated install actions, not a new capability

**Date:** 2026-10-24
**Revised:** 2026-10-26 (two actions: stage-only install, and install-then-enable)
**Status:** accepted
**Area:** Extension platform / trusted admin components
**Related:** `2026-10-24-app-store-out-of-tree.md`,
`2026-10-23-admin-extension-url-install.md`

## Context

An out-of-tree catalog plugin needs to hand a package to the Host and get back
"staged or failed". Three shapes were considered:

1. **A new process capability** (`extensions.install`) plus a Host Command and
   its command engine surface. A plugin backend could then install packages
   asynchronously, without a human present.
2. **A new async pipeline**: intent table, River worker, status endpoint,
   polling client.
3. **A page-level host action**: the trusted admin page asks the Host to run its
   own install path, with a Host-rendered confirmation step.

Relevant facts:

- A plugin admin page is **same-origin, trusted ES module code mounted in the
  Host DOM** — not an iframe. It can already `fetch` admin APIs with the
  administrator's cookie. `bridge.request` is a namespaced ABI, not a sandbox.
- The real install gate is the service layer: `canManagePlugins/Themes(actor)`,
  plus `super_admin` for untrusted executable backends, plus digest pinning.
  Any pre-check in the UI is a gate, never the authority.
- `POST /admin/extensions/install-from-url` already implements the whole download
  path (public HTTPS only, redirect re-validation, 50 MiB, SSRF guard, optional
  pinned SHA-256, inert install).
- A store package is typically a few MiB; the measured install is a synchronous
  download + unzip + digest, not a multi-minute job.

### 2026-10-26: why a second action

The stage-only action solved the catalog's first problem — hand a package to the
Host — and left the second one open: the operator still had to leave the catalog,
find the extension, and enable it. A catalog that can install but not enable
turns installation into a two-page chore, and the page cannot honestly report
whether the extension ended up running.

Enabling is the one step that cannot move onto the plugin page. The trust
confirmation is a Host surface: the impact report (`sforum.trust-impact@2`) and
the one-use, actor-bound, five-minute challenge are minted and rendered by the
Host, and the lifecycle routes are Host-owned. A plugin page that "enabled" a
package itself would either reimplement that review or skip it.

The first end-to-end attempt also exposed a rendering defect that made the
confirmation dialog unusable inside the catalog. The fix belongs to the contract,
not to the catalog:

- Nuxt UI 4's `UModal` renders reka's `DialogOverlay`/`DialogContent` with no
  z-index at all (`#build/ui/modal`), so the dialog relies on DOM order.
- A plugin page's own overlay is a positioned `body` child with `z-index: 40`
  (the catalog's `.sfstore__overlay`), which paints above the Host dialog
  regardless of DOM order.
- While a reka modal layer is open, `DismissableLayer` sets
  `body.style.pointerEvents = 'none'` and restores `auto` only on its own layer
  content. With the plugin overlay on top, nothing under it receives pointer
  events — the dialog is invisible and the whole page looks frozen.

## Decision

Adopt **shape 3**:

- Manifest: `admin.pages[].hostActions: [...]` drawn from `AdminHostActions()`
  (at first `extension.install` only; `extension.install.enable` was added on
  2026-10-26, see the revision below), allowed **only** on `view: "component"`
  pages, unknown action ids rejected at manifest validation.
- Bridge: optional `AdminPageBridgeV1.installExtension({ url, sha256,
  expectedExtensionId, expectedType, expectedVersion? })`. Present only when the
  current page declared the action; absent (`undefined`) otherwise, so plugins
  feature-detect.
- Host flow: permission pre-check → Host-rendered confirmation dialog → a single
  `POST /admin/extensions/install-from-url` with `timeout: 120000` → result.
  Cancel sends nothing. One dialog/install per page at a time (`install_in_progress`).
- Identity binding: the request carries the expected extension id / type /
  version; the service compares them **after** parsing the manifest and
  **before** the type permission switch, so a mismatched package reports
  `extension.expected_mismatch` rather than a confusing permission error.
- Only staging: the action calls `InstallOrUpgradeArchive` and nothing else. No
  enable, no trust, no activate, no upgrade. The administrator completes trust
  and enable in the core UI.

### Revision 2026-10-26: a second, separately declared action

- Manifest: `extension.install` and `extension.install.enable` are two
  independently declared ids in the same `hostActions` list.
  `AdminHostActions()` and `knownAdminHostAction` remain the single vocabulary;
  unknown ids still reject the manifest, `view: "component"` is still required,
  and the normalize/limit rules are unchanged.
- Bridge: each declared action injects its own member. A page that declares only
  `extension.install` keeps `installExtension` and gets nothing else; a page that
  declares only `extension.install.enable` gets
  `installAndEnableExtension({url, sha256, expectedExtensionId, expectedType,
  expectedVersion?})` and no stage-only member. Declaring both exposes both.
- Result: the install identity block plus `outcome: 'enabled' | 'upgraded' |
  'staged'`. The extra field reports what the Host reached; it is never a
  promise, and `staged` is an honest stop rather than a failure.
- Enable path: after the same `install-from-url` call the Host runs its own
  lifecycle route — `POST /admin/extensions/{id}/enable` for a new package, or
  the same upgrade/restart action the extension page uses for a staged
  replacement — with an `Idempotency-Key`. No new route, no second enable
  implementation, no trust bypass.
- Gated fallbacks, all reported as `outcome: 'staged'` with a reason: `theme`
  (the operator activates themes on the theme page), `super_admin` (the package
  declares an **untrusted** executable backend and the operator is not a super
  administrator), `declined` (the operator closed the impact review). The shared
  state machine also carries `stage` for the install-only path, which has no
  `outcome` field — a resolved `installExtension` already means staged. A
  front-end-only package, or an artifact that is already trusted, enables
  immediately.
- Trust review stays inside the dialog: an untrusted executable backend shows the
  Host's impact report and mints the one-use confirmation there. If the backend
  still answers `extension.trust_challenge_required` to a tokenless enable, the
  Host downgrades to that review instead of failing the call.
- Failure: `install_failed` also covers an enable that failed after a successful
  install. The package stays installed, the failing phase is reported, and a
  retry repeats only the enable — never the download.

**No** new capability, Host Command, intent table, or worker.

### Why not the capability (shape 1)

An `extensions.install` capability would let any enabled plugin backend install
packages with no human in the loop. It also drags in the whole command engine
surface (authority declaration, audit, allow/deny tests, docs) for an action
that is intrinsically an administrator's decision in a UI. The page action keeps
authority where it already is — the administrator's session and the service
layer's policy check — and adds only a typed ABI plus a confirmation step.

### Why not async (shape 2), yet

Synchronous is acceptable while packages are small. The costs of async are an
intent table, a worker, a status endpoint, and a cleanup policy for abandoned
intents — infrastructure that only pays off with volume or long installs.

The install promise is deliberately specified as "resolves when staged or
failed", so the upgrade is invisible to plugins: the Host may later implement
`installExtension` as `POST /admin/extensions/install-intents` + polling. The
2026-10-26 enable action adds a post-install phase to the same promise — it does
not add a second transport or a second polling surface. Triggers for the async
upgrade: median package > 10 MiB, median install > 30 s, batch installs,
install-outlives-page, or progress/retry requirements.

## Honest limits (do not overstate this as a sandbox)

- The confirmation dialog is **not** a security boundary. A trusted admin page
  can bypass the bridge and `fetch` the install route directly. What stops abuse
  is the service-layer authorization, the `super_admin` requirement for
  untrusted executable backends, digest pinning, and the fact that an install
  only stages an inert package.
- The dialog exists so the *normal* path is explicit, reviewable, and shows the
  exact URL, digest, and declared identity before anything downloads.
- v1 is synchronous: navigating away aborts the install (browser disconnect
  cancels the Go context). The UI must say so, and the operator retries.
- Host-side single-flight is per page, in-process. It is not a cross-node lock;
  concurrent installs of the same extension from different nodes or from
  plugins bypassing the bridge are serialized only by the existing in-process
  `assetPublicationMu`. Cross-node serialization belongs to the async stage.
- The enable action is **not** a sandbox either. A trusted admin page can call
  the lifecycle routes directly; what bounds it is the service-layer
  authorization, the exact-artifact `super_admin` gate for untrusted executable
  backends, and audit. The dialog exists so the normal path is reviewable and so
  the caller gets an honest `outcome`.
- `outcome: 'staged'` is the only truthful answer for a theme, or for an
  **untrusted** executable backend when the operator is not a super
  administrator. A trusted artifact enables for any operator holding
  `extension.plugin.manage`, and the Host never fakes activation to make a
  catalog look finished.

## Consequences

- The catalog plugin owns discovery, signing, and download hosting; the Host owns
  download, digest verification, identity binding, staging, and audit.
- `hostActions` is disclosed in the plugin detail area and the enable
  confirmation dialog as its own line item — it is deliberately **not** folded
  into capability risk levels, because it is not a process capability.
- The manifest field is additive but breaks older Hosts' strict schema
  (`additionalProperties: false`); publishers must state a minimum Host version.
- The install route gains optional `expectedExtensionId` / `expectedVersion` /
  `expectedType` / `source` fields. Manual URL installs from the core panel keep
  working without them; the bridge always sends the identity fields.
- `hostActions.enum` in OpenAPI now lists both ids. The embedded JSON Schema
  keeps `items: string`; the Go vocabulary stays the rejecting authority.
- The second action is additive: publishers that declare only
  `extension.install` are untouched. A Host that predates the new id rejects the
  whole manifest, so publishers must state a minimum Host version — the same rule
  the first action already imposed.
- Dialog layering is part of the contract: the Host dialog pins its overlay and
  content to `z-[80]`, above plugin overlays (`≤ z-40`) and below toasts
  (`z-100`). Plugin pages must not raise their overlay above the Host dialog, and
  the temporary "drop my overlay to 0 while installing" workaround is no longer
  needed.
