# 2026-10-16 Plugin Frontend Surface Contract

## Status

Accepted. M0 of the plugin frontend experience program. The icon service
(M1) is implemented under this contract; public shell, region density, and the
declarative page view follow in later milestones.

**Partially superseded (2026-10-26):** `forum.profile.tabs` now accepts
`l2Widget` because its render site gained real L2 mounting; every other
descriptor point keeps the boundary below. See
`2026-10-26-profile-tab-l2widget.md`.

## Context

Plugin authors report three concrete frontend authoring gaps:

1. A plugin public page cannot inherit the active theme shell. It must ship a
   Go SSR template through `templates[]` (`RuntimeTemplatePlugin`) and add a
   hand-written DOM L2 module for interactivity, with no `layout` concept at
   all. Admin pages already inherit the Host shell but only through
   `view: component`, so there is no declarative page either.
2. Host icons are unreachable. The Host renders icons through Nuxt Icon with
   `provider: 'server'` and `serverBundle` for `lucide` and `tabler`, so the
   set only exists inside the Nuxt component tree. Neither
   `AdminPageBridgeV1` nor `PublicFrontendBridgeV1` exposes any icon
   capability, and none of the nine `@sforum/plugin-ui` components accepts an
   icon.
3. Placement is fragmented across three parallel systems: eleven descriptor
   contribution points, the three-region `forum.page.regions` catalog over
   twelve pages, and the V3 `NavigationRegistry` (five navigation targets plus
   five region targets) whose composition is currently reachable only from the
   Inspector, not from public chrome.

The Host already owns the data needed to close (2) without a new dependency:
`apps/web/server/utils/iconCatalog.ts` loads both `@iconify-json` collections
into memory. It only ever projected icon *names*, never bodies.

## Decision

### Icon service

- Expose the Host icon set as a versioned platform surface under the existing
  `/_sforum/` namespace, not under the app's `/api/` BFF prefix:
  - `GET /_sforum/icons?names=i-lucide-x,i-tabler-y` — batch JSON, at most 32
    names, `{ items: [{ name, svg }] }` where `svg` is a complete inlinable
    document sized `1em`.
  - `GET /_sforum/icons/{lucide|tabler}/{name}.svg` — one SVG document.
- This surface is documented in `docs/extensions/trusted-admin-components.md`
  alongside the other `/_sforum/` plugin-facing routes and is deliberately
  **not** an `extension-surface-matrix` row: that matrix enumerates per-module
  extension points (routes, hooks, queries, registries), while this is a
  Host-owned static asset namespace with no per-module extension semantics.
  Its stability is enforced by contract tests on the name grammar, batch bound,
  sanitizer, and cache headers rather than by a generated matrix entry.
- Collection membership, name grammar, and the batch bound are validated
  server-side. The accepted grammar is exactly
  `^i-(lucide|tabler)-[a-z0-9]+(?:-[a-z0-9]+)*$`, which is the grammar already
  used by `adminSurfaces.ts` and the Go contribution validator.
- Every body is sanitized before it leaves the process. The current source is a
  first-party build-time dependency, but the response crosses into plugin code
  that the Host does not control; sanitizing keeps a future icon-source change
  from silently becoming an XSS surface.
- Unknown, malformed, or over-limit names are omitted from the batch response
  instead of failing it, so one bad name in a component cannot blank a page.
  The single-icon route returns 404.

### Bridge capability

- Add an **optional** `icons` member to both page bridges:
  `AdminPageBridgeV1.icons` and `PublicFrontendBridgeV1.icons`, each
  `{ resolve(names: string[]): Promise<Record<string, string>> }`.
- The field is additive and optional so that existing API v1 modules keep
  mounting. `@sforum/admin-sdk` moves to `1.1.0`; the major version and the
  `apiVersion: 1` wire contract are unchanged.
- The Host supplies the resolver. It is a presentation convenience, never an
  authorization grant, and it reads only public static data.

### Plugin UI SDK

- `@sforum/plugin-ui` gains `SPluginIcon` and an `icon` prop on
  `SPluginButton`, `SPluginSection`, `SPluginAlert`, `SPluginEmptyState`, plus
  `SPluginTableColumn.icon`. Authors still write no CSS.
- The SDK gains a `mountPluginPage` adapter so that the icon resolver is
  injected by the SDK instead of by every generated `admin.ts`. Scaffolds call
  it; authors never wire icons by hand.
- When a bridge has no `icons` member, `SPluginIcon` renders an empty
  decorative element and warns once. It never throws and never blocks mount.

### Theme template icons

- `ThemeCompiler.restrictedFuncMap` gains an `icon` helper that emits
  `<span class="sf-host-icon" data-sf-icon="…" aria-hidden="true"></span>`
  rather than inlining SVG. Embedding both collections into the Go binary is not
  acceptable, and the existing Host runtime already resolves icons client-side
  with the same grammar. Icons are decorative and `aria-hidden`, so a JS-disabled
  render loses decoration only.
- An unsupported name emits an HTML comment instead of failing the render: a
  typo in one icon must not turn a page into a 500, but it must stay
  discoverable in the rendered source. The echoed name has every character
  outside `[A-Za-z0-9._:-]` replaced, so it cannot close the comment early.
- `apps/web/app/plugins/hostIcons.client.ts` fills the placeholders on app mount
  and on every `page:finish`, using the same resolver as the plugin bridges.
  Placeholders are marked once they are processed, so a failed resolution is not
  retried on every navigation.

### Placement registry consolidation (revised after inspection)

The original draft of this decision said `NavigationRegistry` would become the
single authority for all presentation placement. **Inspecting the runtime
disproved the premise.** `RegionCatalog` + `forum.page.regions` places content
into *page areas*; `NavigationRegistry` composes *navigation chrome* (topbar,
breadcrumb, header, footer, sidebar, account-settings) from
`manifest.navigation[]` / `manifest.regions[]`. They answer different questions,
are consumed by different runtimes (`Providers.ExtensionPageRegionProvider`
versus `Models/SiteChrome`), and merging them would be a large, trust-adjacent
refactor that buys authors nothing. Corrected decision:

- `RegionCatalog` remains the single authority for page regions, and it is the
  only hand-edited source: `pageRegionMatrix` in
  `apps/api/app/Support/RegionCatalog/catalog.go`.
- The web whitelist is **generated** from it into
  `apps/web/app/config/pageRegionCatalog.gen.ts` by
  `scripts/v3-catalog/generate.mjs`, which removes the previous hand-maintained
  mirror in `usePageRegions.ts` (the exact drift this milestone existed to kill).
- A declared page×region must have a real render site.
  `apps/web/tests/framework/pageRegions.test.ts` compares the generated matrix
  against `<SFRegionOutlet>` occurrences in the host components in both
  directions, so "declare first, render later" is impossible.
- `ContributionPoints` stays the typed descriptor catalog whose results resolve
  onto regions; it is not a second placement system.
- Author-facing confusion is addressed by documentation and a generated
  catalog, not by merging two different registries.

### Descriptor points are not content surfaces (M4 cancelled)

The plan proposed accepting `l2Widget` on the descriptor points that today take
only `extensionRoute`/`hostLink` (`forum.topic.sidebar`, `forum.profile.tabs`,
`forum.nav.items`, `forum.topic.actions`, `forum.comment.actions`). Inspection
disproved the premise and the milestone is cancelled rather than implemented.

The boundary, recorded here because it is durable: **page regions are the only
content surfaces; every other contribution point is a link, action, or badge
descriptor.** A descriptor states *where to go or what to trigger*
(`type` + `method`/`path`/`href`), and the host renders it as a link or button.
A region states *what to put here*, and only `SFRegionOutlet` /
`SFThemeTemplate` can mount an L2 component for it.

**Recorded exception (2026-10-26):** `forum.profile.tabs` is now a second kind of
content surface. Selecting a profile tab swaps a panel the host already renders
in the profile content column, so that point satisfies the condition stated
below ("the point's render site gains real L2 mounting plus membership in the
page's single CSP aggregation"). `forum.topic.sidebar`, `forum.nav.items`,
`forum.topic.actions`, `forum.comment.actions`, the badge points, and
`forum.composer.toolbar` remain closed. Decision:
`2026-10-26-profile-tab-l2widget.md`.

Consequences to keep:

- Do not add `l2Widget` (or any embedded-content variant) to a descriptor point
  unless that point's render site gains real L2 mounting plus membership in the
  page's single CSP aggregation. Otherwise it is a validated, documented,
  never-rendered capability, which is worse than an absent one.
- Do not add a new descriptor point when the need is "render rich plugin content
  in this page area" — add or reuse a region instead.
- Rich content at a descriptor location is reached by *navigating* to the
  plugin's own `/extensions/{id}` route, which is what the `extensionRoute`
  descriptor kind is for.

### Page region density

The page region vocabulary grows from three generic areas to semantic
positions, so a plugin can put content *inside* a page rather than only above or
below it: `list_before`/`list_after`, `detail_before`/`detail_after`,
`comments_before`/`comments_after`, `sidebar_top`/`sidebar_bottom`,
`profile_header_after`. Two candidate regions were rejected during
implementation because they would not have been distinct slots: `hero` (would
sit ~10 lines from `content_before`) and `composer_after` (adjacent to
`content_after`). `forum.topic.reply` was removed from the matrix entirely
because its component is a redirect placeholder with no render site — a
pre-existing declaration that could never have rendered.

The web-side `MAX_REGIONS` bound is now derived from the generated catalog
instead of a hardcoded 8, which would otherwise have silently rejected a fully
populated `forum.topic.show` (9 regions).

### Declarative page view (contract fixed here, implemented later)

- A plugin public page may declare `view: "schema"` with a bounded page
  descriptor (`list`, `detail`, `form`, `notice`) reusing the existing
  `AdminSurfaceViewModel`/`AdminSurfaceField` shapes, a `dataRoute`, and a
  package-declared JSON Schema. No Vue, no Go template, no build step.

## Consequences

- Plugins get Host icons without bundling an icon set and without importing
  Host-private components. The cost is one cached same-origin request per
  icon-name batch per session.
- `/_sforum/icons/*` is a supported plugin-facing contract and must be tracked
  in the extension surface matrix and regenerated catalogs. It is public static
  data and needs no authorization.
- The optional bridge member preserves the v1 compatibility promise; a plugin
  built against `admin-sdk@1.1.0` still runs on a Host that only implements
  `icons` as absent.
- Page regions now have one hand-edited source plus a generated web projection
  and a two-way render-site contract test. Adding a region means editing the Go
  catalog, regenerating, and placing an outlet; it no longer means keeping two
  lists in sync by hand.
- `l2Widget` acceptance on the descriptor points that today take only
  `extensionRoute`/`hostLink` is still open (M4); nothing in this change
  touches it. **Updated 2026-10-26:** `forum.profile.tabs` now accepts it; the
  other descriptor points remain closed (`2026-10-26-profile-tab-l2widget.md`).
- Nuxt Layers, runtime SFC compilation, Host-private component imports, and
  package build-script execution remain unsupported. L2 code remains fully
  trusted after exact-artifact approval, not sandboxed.
