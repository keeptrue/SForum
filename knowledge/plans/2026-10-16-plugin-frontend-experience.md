# Plugin Frontend Experience - Task Book

Status: **active** - M0 contract frozen; M1 (host icon service + theme
template helper) and M1b (author build-loop repair) done; M2 (single region
source + drift gate) and M3 batch 1 (region density) done and verified
end-to-end. M4 cancelled (premise disproved; `forum.profile.tabs` partially
reopened for `l2Widget` on 2026-10-26, see the M4 section), M5 delivered by the
authoring-speed plan,
M7 `view: schema` done, M9 closed, and **M6 closed (2026-10-16)** — the last open
item ("the component does not appear in the 390x844 headless context") was
**resolved as a measurement artefact**, not a client gap: see the M6 section.
M8 remains and delegates public navigation to the navigation platform plan M7.

Date: 2026-10-16
Last updated: 2026-10-26 - M4 partially reopened for `forum.profile.tabs` only

Goal: make plugin frontend development feel like ordinary Vue work without
introducing Nuxt Layers, runtime SFC compilation, Host-private component
imports, or package build-script execution. Three author-reported gaps drive
this program: a plugin public page cannot inherit the theme/layout/menu, plugin
code cannot use Host icons, and there are too few injection points.

## Execution Rule

Execute one milestone at a time. Every milestone must leave the repository
buildable, update its durable evidence, and satisfy its exit gate before the
next starts.

The two adjacent programs are dependencies, not duplicates:

- `2026-08-22-extension-authoring-speed-and-controlled-power.md` **M1/M2 owns
  the public `layout: site|bare` shell**. This task book must not reimplement
  it.
- `2026-07-27-configurable-public-navigation-platform.md` **M7 owns the public
  navigation runtime switch**. This task book must not reimplement it.

## Required Reading

1. `AGENTS.md`
2. `knowledge/index.md`
3. `knowledge/modules/extensions.md`
4. `knowledge/modules/frontend.md`
5. `knowledge/decisions/2026-10-16-plugin-frontend-surface-contract.md`
6. `docs/extensions/authoring-guide.md`
7. `docs/extensions/trusted-admin-components.md`
8. `docs/extensions/page-catalog.md`
9. `docs/extensions/runtime-themes.md`

## Problem Statement

| Gap | Evidence |
| --- | --- |
| No public shell or declarative view for plugin pages | Plugin public pages need a Go SSR template (`templates[]`, `RuntimeTemplatePlugin`) plus a hand-written DOM L2 module. There is no `layout` field anywhere. Admin pages inherit the shell but only through `view: component`. |
| Host icons unreachable | Nuxt Icon uses `provider: 'server'` with `serverBundle` for `lucide`/`tabler`, so the set exists only inside the Nuxt component tree. Neither bridge exposes icons; `@sforum/plugin-ui` had no icon component. |
| Sparse, fragmented slots | Three parallel placement systems: eleven descriptor contribution points, `forum.page.regions` (3 regions over 12 pages), and the V3 `NavigationRegistry` (5 navigation + 5 region targets) that public chrome does not yet read. |

Admin is already dense (12 `adminSurfaces` kinds over 49 placements); the public
side is where the gap is.

## Milestones

| Milestone | Outcome | Status |
| --- | --- | --- |
| M0 | Freeze the frontend surface contract and write the decision record | **done** |
| M1 | Host icon service: versioned endpoint, bridge capability, SDK components, scaffold | **done** (surface-matrix entry open) |
| M1b | Repair the author build loop found while proving M1 end to end | **done** |
| M2 | Page-region single source + generated web projection + render-site contract | **done (rescoped, see below)** |
| M3 | Region density: semantic regions instead of three generic ones | **batches 1-2 done and verified end-to-end** |
| M4 | `l2Widget` accepted on every region and descriptor point | **cancelled** - the descriptor points are link/action shaped, not content surfaces. **Partially reopened 2026-10-26:** `forum.profile.tabs` accepts `l2Widget` because a tab selects a host-rendered panel; every other point stays closed (`decisions/2026-10-26-profile-tab-l2widget.md`) |
| M5 | Public `layout: site` shell - **delegates to the authoring-speed plan M1/M2** | **delivered by that plan** - `layout: site\|bare` (default `site`), Host fallback shell and the theme-authored shell are implemented and verified on the real path; this task book must not reimplement them. Remaining caveat: `dataSource: plugin` pages are blocked by the Loader Gateway transport gap. **Corrected 2026-10-16:** the earlier caveat "the built-in theme hides the shell footer" no longer holds — the Host stylesheet's `:has()` suppression was complemented by a `:not(:has(.sforum-content-footer))` restoration rule in `apps/web/app/assets/css/sforum-theme.css` and the active theme artifact moved `4691a605…` -> `85ddda6a…` through rebuild + admin reactivation. Runtime evidence at the current artifact: the shell template `templates/plugin-page-shell.html` mounts `<sf-footer>`, SSR of `/probe-site` and `/probe-default` contains exactly one `<footer class="sf-footer">`, a real browser at 500px reports `visibleFooters=1`, and `/probe-bare` reports 0. See `sessions/2026-10-16-route-a-layer6-and-loader-transport-handoff.md` and `tests/registry-page-shell-runtime-evidence.mjs` |
| M6 | Public Plugin UI entry + public `view: component` pages | **closed (2026-10-16)** - fixture + resolve (`view=component`, `fallback=false`) + descriptor endpoint 200 (`trustNotice=fully_trusted_browser_code`) + the component mounting in a real browser session inside the reviewed body exit with exactly one header/footer, **and now a measured 390x844 mount on real Chrome over CDP** (`l2=mounted`, own content + host trust notice rendered, `headers=1 footers=1 overflow=false clipped=0 consoleErrors=0`). The previously-open item ("in the headless CDP context the component does not appear and no descriptor request is issued at all, at any width or settle time") turned out to be a **defect in the harness's settle heuristic**, not a client gap — see "M6 closed" below. |
| M7 | `view: schema` declarative public pages (list/detail/form/notice) | **done** - declaration + shared CLI/runtime preflight, resolve pass-through, public renderer, catalog identity, fixture and browser evidence. `form` renders read-only, no faked submit; **update 2026-10-16**: decision R2 chose a Protocol V2 data-fetch method for the loader gap, so form submission becomes buildable once that method lands |
| M8 | Navigation unification - **delegates to the navigation plan M7** | blocked by that plan |
| M9 | Author tooling (`--public-page`, `--schema-page`, `extension dev`), runtime surface catalog, bilingual docs | **done (2026-10-16)** - every listed item is delivered and evidenced; attribution is split honestly: `--public-page` came from the authoring-speed plan M3, `extension dev` was already shipped by that plan's M8, and this session delivered `--schema-page` plus the public page runtime surface catalog and the bilingual docs for both |

## M0 - Contract Freeze

Status: **done**.

Deliverable: `knowledge/decisions/2026-10-16-plugin-frontend-surface-contract.md`
freezes the icon endpoint contract, the additive optional `icons` bridge member,
the registry-consolidation direction, and the `view: schema` descriptor shape,
and explicitly delegates the public shell to the authoring-speed plan.

Exit gate met: the decision names every affected surface and its owner, and the
icon contract it froze is implemented and tested in M1.

## M1 - Host Icon Service

Status: **done**. See `knowledge/sessions/2026-10-16-plugin-host-icons-m1-handoff.md`.

Delivered:

- `apps/web/server/utils/iconSvg.ts` - name resolution (alias chains included)
  plus allowlist-based markup rebuild.
- `apps/web/server/routes/_sforum/icons/index.get.ts` (batch, ≤32 names) and
  `apps/web/server/routes/_sforum/icons/[collection]/[name].get.ts` (one SVG).
- `apps/web/app/utils/icons/hostIcons.ts` - shared browser resolver with batch
  merge, in-flight dedupe, and negative caching.
- `AdminPageBridgeV1.icons`, `AdminMicroFrontendBridgeV1.icons`,
  `PublicFrontendBridgeV1.icons` (optional, `@sforum/admin-sdk@1.1.0`).
- `@sforum/plugin-ui@1.1.0`: `SPluginIcon`, `icon` props on
  button/section/alert/empty-state, `SPluginTableColumn.icon`,
  `definePluginPage` / `mountPluginPage`.
- Scaffold and docs updated; 36 new web tests plus 3 Go helper tests.

Delivered (continued):

- `ThemeCompiler` `icon` helper for theme templates, emitting
  `<span class="sf-host-icon" data-sf-icon="…" aria-hidden="true"></span>`.
  Unsupported names emit a diagnosable HTML comment instead of failing the
  render. `icon` joined `ThemeCompiler.allowedFunctions`.
- `apps/web/app/plugins/hostIcons.client.ts` fills those placeholders on
  `app:mounted` and `page:finish` with the shared resolver; `.sf-host-icon`
  styling ships in `apps/web/app/assets/css/sforum-components.css`.
- Rendered browser evidence: a real trusted plugin page
  (`acme.iconprobe@0.1.0`) installed through the admin flow resolved 6/6 host
  icons, and the live `page:finish` hook filled a template placeholder with SVG.

Remaining inside M1:

1. Register `/_sforum/icons/*` in the V3 surface matrix or write the decision
   that it stays documentation-only.

Exit gate: the helper renders in a real theme template path with a JS-disabled
fallback, and the surface-matrix question is decided rather than left open.

## M1b - Repair The Author Build Loop

Status: **done**. Discovered by running the documented loop end to end; without
these fixes `make:plugin --vue-admin-page` could not be built or validated at
all, so M1 could not have been verified.

1. The scaffold's `vite.config.ts` pinned `vite: ^8.0.0` while forcing
   `minify: 'esbuild'`. Vite 8 is rolldown/oxc-based and needs a separately
   installed `esbuild` for that path, so `bun run build` aborted. Removed the
   forced minifier and the non-Vite-8 `output.codeSplitting`.
2. `PreflightExactTemplateRuntime` hashed the whole author tree with
   `DigestTree`, which rejects symlinks anywhere — including the
   `node_modules/.bin/*` symlinks every `bun install` creates. Added
   `extensionpackage.DigestAuthorTree` (skips `.git`/`node_modules`/`vendor`,
   exactly what `extension package` excludes) for the author preflight only. The
   trust-identity `DigestTree` is unchanged and still backs install/snapshot
   verification, with a test pinning that it still rejects those symlinks.
3. Added `extension build --sdk-dir <dir>` (`SFORUM_SDK_DIR`) so a new SDK
   version can be built against before Release publishes it. It restores
   `package.json` and the Bun lockfile byte for byte.

Exit gate met: `make:plugin --vue-admin-page` → `extension build --sdk-dir` →
`extension package --exclude-source` → admin upload → `super_admin` exact-artifact
trust → enable → a rendered admin page with 6/6 host icons resolved.

## M2 - Page Region Single Source

Status: **done, rescoped after inspection.**

The plan assumed `NavigationRegistry` should become the single authority for all
presentation placement. Inspecting the runtime disproved that: `RegionCatalog` +
`forum.page.regions` places content into *page areas*, while
`NavigationRegistry` composes *navigation chrome* from
`manifest.navigation[]`/`regions[]`. Different questions, different runtimes;
merging them would be a large trust-adjacent refactor with no author benefit.
The decision record carries the correction.

Delivered instead:

1. `RegionCatalog.pageRegionMatrix` is the only hand-edited region source.
2. `scripts/v3-catalog/generate.mjs` parses it and emits
   `apps/web/app/config/pageRegionCatalog.gen.ts` (`PAGE_REGION_PAGES`,
   `PAGE_REGION_IDS`, `PAGE_REGION_KINDS`), replacing the hand-maintained mirror
   that used to live in `usePageRegions.ts`. `--check` gates drift.
3. `apps/web/tests/framework/pageRegions.test.ts` compares the generated matrix
   against `<SFRegionOutlet>` occurrences in the host components in both
   directions, and asserts every declared page has a mapped component.
4. Go-side fail-closed helpers (`UndeclaredRegions`, `PageRegionIDs`) plus tests
   for undeclared, unreachable, and order/aliasing invariants.

Exit gate met: adding a region now means editing one Go file, regenerating, and
placing an outlet — and the contract test refuses a declaration without a slot.

## M3 - Region Density

Status: **batch 1 done and verified end-to-end.** More pages/regions remain open.

Vocabulary added (11 new ids, 14 total): `list_before`, `list_after`,
`detail_before`, `detail_after`, `comments_before`, `comments_after`,
`sidebar_top`, `sidebar_bottom`, `profile_header_after`.

Render sites wired: `forum.topic.show` (all nine regions), `forum.home`
(`sidebar_top`/`sidebar_bottom`), `forum.profile.show`
(`profile_header_after`), `forum.notifications` (`list_before`/`list_after`).

Rejected during implementation, because a region that is not a distinct slot is
a lie:

- `hero` — would have sat ~10 lines from the existing `content_before` on the
  home page.
- `composer_after` — adjacent to the existing `content_after` on the composer
  pages.
- `forum.topic.reply` — its component is a redirect placeholder with no slots;
  the matrix had declared three regions for it that could never render. Removed.

`MAX_REGIONS` in `usePageRegions.ts` is now derived from the generated catalog
(it was hardcoded to 8, which would have silently rejected a fully populated
`forum.topic.show` with 9 regions).

Rendered evidence: a probe plugin contributing to `detail_before`,
`comments_before`, and `sidebar_top` on `forum.topic.show` was uploaded,
trust-confirmed, upgraded, and all three cards rendered in the correct
positions with their host icons.

Batch 2 added `list_before`/`list_after` to `forum.home`, `forum.search`,
`forum.category.index`, `forum.category.show`, `forum.tag.index`, and
`forum.tag.show`. `detail_before`/`detail_after` for category detail was
rejected (it would have duplicated `list_before`, since a category page's header
is immediately followed by the topic list).

Runtime verification for batch 2: `/` renders `list_before` above
`#feed-list-container`; `/c/announcement` renders `list_before` above its list;
`/tags/tooling` renders `list_after` between the list and the pagination. Each
was confirmed in the SSR HTML by position (not merely by presence) and in the
browser DOM via `compareDocumentPosition`, with the card text present.

Remaining: admin-page regions (`admin.page.before`/`after`), which need a
separate renderer, and `detail_before`/`detail_after` on pages that actually
have a detail body distinct from their list.

## M4 - `l2Widget` On Every Slot — CANCELLED (premise disproved)

Status: **cancelled after inspection. Do not implement as written.**

> **Partial reopening (2026-10-26).** `forum.profile.tabs` now accepts
> `type: l2Widget`. That one point differs from the points examined below:
> selecting a tab swaps a panel the host already renders in the profile content
> column, so it is a container with a host-owned render site, not a
> link/action descriptor. Only that point, and only for `l2Widget`; every
> conclusion below still holds for the others. The "`SFExtensionWidget` is used
> in exactly two files" count below is now three - `SFProfileShowPage.vue` is
> the third mount site. Decision:
> `decisions/2026-10-26-profile-tab-l2widget.md`.

The milestone assumed the descriptor points are content surfaces that merely
lack widget support. They are not content surfaces. Evidence gathered before
writing any code:

1. `SFExtensionWidget` — the only host primitive that mounts an L2 component —
   is used in exactly two files: `SFRegionOutlet.vue` and `SFThemeTemplate.vue`.
   No other render site has L2 mounting capability at all.
2. Every named point is link/action/badge shaped, not a container:
   `forum.topic.actions` → `TopicActionContributionPayload` (`type` +
   `method`/`path`), `forum.topic.sidebar` → `TopicSidebarContributionPayload`
   (`type` + `method`/`path`/`href`), `forum.profile.tabs` →
   `ProfileTabContributionPayload` (same shape), `forum.nav.items` →
   `NavItemContributionPayload` (same shape),
   `forum.topic.badges`/`forum.topic.list.badges` → badge tone enum,
   `forum.composer.toolbar` → an `extensionRoute` only.
3. Their view models carry a `URL` and a `Kind` of `extensionRoute | hostLink`
   (`ProfileExtensionTab`, `TopicExtensionSidebarItem`), and their render sites
   emit `<NuxtLink>` / `<a>` / buttons. `SFProfileRightRail.vue` renders the
   profile tabs under a heading that literally reads "extensionLinks".
4. Regions are the only content surface, and they already accept
   `hostLink | extensionRoute | l2Widget`.

Accepting `l2Widget` on those points would therefore have produced declarations
that validate, are documented for authors, and silently render nothing — the
same dead-slot failure mode that M3 rejected three times (`hero`,
`composer_after`, `forum.topic.reply`), except worse because authors would read
it in the reference docs.

The injection surface is already complete relative to what each point's shape
can express: links and actions are expressed as descriptors, and rich plugin
content is placed in a region (widget-capable) or behind the plugin's own
`/extensions/{id}` route.

Consequence for M5-M9: rich embedded content in a public page is now only
reachable through regions or a whole page owned by the plugin, which is exactly
what M5 (public `layout: site`) and M6 (public Plugin UI entry) deliver — so
those become the only remaining route to the capability M4 was reaching for.
(Updated 2026-10-26: a profile tab panel is now a third, bounded route to that
capability - see the partial-reopening note above.)

## M5-M9

M5 (`layout: site`), M6 (public Plugin UI + `view: component`), M7
(`view: schema`), M8 (navigation unification), and M9 (tooling and docs) follow
the frozen contract. M5 and M8 execute the two adjacent plans named in
**Execution Rule**; do not start them here.

## M7 - `view: schema` declarative public pages

Status: **complete (stages 1 and 2 implemented, green, and verified end to end).**

### Interpretation of the frozen contract - **confirmed 2026-10-16 (decision R6)**

The operator confirmed the naming below (`schema.descriptor` + `schema.contract`); the
implementation, scaffold and docs stay as delivered. Recorded in
`knowledge/decisions/2026-10-16-extension-authoring-contracts-freeze.md` (Revision 1, R6).

### Interpretation of the frozen contract (recorded, because it is ambiguous)

The decision record fixes the shape in one sentence: a public page may declare
`view: "schema"` with "a bounded page descriptor (`list`, `detail`, `form`,
`notice`) reusing the existing `AdminSurfaceViewModel`/`AdminSurfaceField`
shapes, a `dataRoute`, and a package-declared JSON Schema". It does not say how
the declaration names those pieces. This task book implements the minimal
reading that satisfies every clause, and **flags the naming as an open
confirmation item** rather than silently inventing a new contract:

```json
{
  "id": "demo.theme.page.orders", "action": "add", "path": "/orders",
  "layout": "site", "view": "schema",
  "schema": { "descriptor": "demo.theme.file.page.list",
              "contract": "demo.theme.schema.page.list@1" }
}
```

- `schema.descriptor` — id of a `kind: schema` `packageFiles[]` entry whose JSON
  body **is** the bounded page descriptor.
- `schema.contract` — exact package schema reference (`<schemaId>@<version>`)
  that the descriptor must satisfy; the same `<id>@<version>` ↔ `packageFiles[]`
  matching the provider-slot machinery already uses.
- `data.dataRoute` (existing field) remains the declared data route; binding it
  at runtime is still blocked by the loader-transport gap, so stage 1 does not
  depend on it.

### What stage 1 delivers (verified)

- `Pages` accepts and validates the declaration shape: `view=schema` requires a
  descriptor **and** a contract, and is mutually exclusive with `template` and
  `component` (both directions). Unknown views still fail closed. Cases live in
  `theme_page_view_test.go`.
- The descriptor/contract **files** are validated in
  `apps/api/sdk/plugin/schema_view.go`, reached from
  `PreflightExactTemplateRuntime` — the same entry point the CLI (`extension
  validate` / `extension test`) and the runtime activation path already share, so
  a schema-view page cannot pass the CLI and fail at activation.
- Checks: declared `kind: schema` file with a matching digest (exact artifact,
  path-escape and symlink refusal, 1 MiB bound), descriptor JSON parses with no
  trailing content, the descriptor satisfies the compiled contract
  (Draft 2020-12, `AssertFormat`), and the **bounded** rules — `kind` must be one
  of `list|detail|form|notice`, `tone` one of the five Host tones, field types
  one of `text|textarea|number|boolean|select`, with caps of 24 fields, 64 items,
  64 options, 64 cells, 200-char labels and 2000-char text.

### CLI evidence (the shared-validation property, measured)

```text
$ go run ./cmd/sforum extension validate /tmp/svpkg
OK  /tmp/svpkg

$ # after replacing the descriptor with {"kind":"dashboard"}
$ go run ./cmd/sforum extension validate /tmp/svpkg
Error: invalid package at /tmp/svpkg: schema view preflight: page demo.theme.page.orders:   schema descriptor kind "dashboard" is not one of list|detail|form|notice
$ go run ./cmd/sforum extension test /tmp/svpkg
extension test failed: 1 error(s), 0 warning(s)
```

### Stage 2 (delivered and verified)

- **Resolve pass-through.** `resolveResponse.schemaView` carries the parsed bounded
  descriptor. The loader is a single implementation
  (`Controller.schemaView` in `controller_pages_transport.go`) used by both the
  binding path and the add path, so the two cannot diverge; it reuses
  `pluginsdk.LoadSchemaViewDescriptor`, i.e. the same read the preflight validated.
  A read failure returns nil and the body falls back to the empty state rather than
  rendering a partial page.
- **Body-provided exemption generalised.** Both resolve paths had a
  "no template ⇒ runtime unavailable" fallback that fired for schema pages, because
  their `templateHtml` is legitimately empty. The condition is now
  `!pageBodyProvidedByView(...)` / `!responseBodyProvidedByView(...)`, covering
  `view=component` **and** `view=schema`.
- **Public renderer.** `apps/web/app/components/extensions/public/SFPublicSchemaView.vue`
  renders `list` / `detail` / `notice` fully and `form` as **read-only** fields with a
  `data-schema-form-pending` marker — submission needs the page's declared
  `dataRoute`, which is still blocked by the loader-transport gap, so the renderer
  does not fake a submit. It is deliberately not the admin outlet: a test forbids the
  admin identifiers (`commandSurfaceId`, `invokable`, `visibleResourceIds`,
  `SFAdminSurfaceOutlet`) from appearing in the file.
- **Body exit + wiring.** `SFRegistryPageBody` gained a `schemaView` branch **before**
  the template branch (with an explicit sibling import), and both catch-all route
  shells pass `schemaView` through.
- **Catalog identity.** `core.component.shared.sf_public_schema_view`
  (`owners: ["public"]`) registered in `docs/extensions/v3/catalog-identities.json`;
  the generator refused to run until it existed.
- **Fixture.** `extensions/fixtures/plugins/sforum-page-schema-reference` — a
  components-free, code-free package whose page body is a list descriptor plus its
  contract; `extension validate` / `extension test` pass on it.

### Browser evidence (real Chrome, desktop viewport, fixture installed and enabled)

```text
/page-schema-reference/list
  shell=site shellSource=theme
  visibleDesktopNavbars=1 visibleMobileNavbars=0 visibleFooters=1
  data-schema-kind=list data-schema-tone=neutral
  data-schema-extension=sforum.page-schema-reference
  title="Schema view reference"
  description="Bounded page descriptor rendered by the Host without any package code."
  itemLabels=["Plan","Seats"] itemValues=["pro","12"] cellKeys=["owner","state"]
  insideShell=true horizontalOverflow=false clippedControls=0
```

## M9 - Author tooling: `--schema-page` (delivered 2026-10-16)

Status: **`--schema-page` delivered; the remaining M9 items (`extension dev`, runtime
surface catalog) are not started.** `--public-page` already existed from the
authoring-speed plan's M3, so this milestone's missing piece for the new view was the
generator flag for `view: schema`.

Delivered:

- `make:plugin` / `make:theme` gained `--schema-page <slug>` and
  `--schema-page-kind list|detail|form|notice` (default `list`). The scaffold writes
  `schemas/page-<slug>.json` (a bounded descriptor using only allowed fields) and
  `schemas/page-<slug>-contract.json`, then merges the `theme.json` page mapping
  (`view: schema` plus the `schema.descriptor` / `schema.contract` pair) and declares
  both files as `kind: schema` **with `version: "1"`** in `packageFiles[]` — the
  version is what makes the exact `<schemaFileId>@1` reference resolvable.
- Rejections happen before anything is written: unknown kind, `--schema-page-kind`
  without `--schema-page`, an unsafe slug, and a slug shared with `--public-page`
  (one path must not carry two declarations).
- The generated scaffold passes the **shared** preflight without hand-editing:
  `extension validate` → `OK`, `extension test` → 0 errors, and
  `PreflightExactTemplateRuntime` (the same call the activation path makes) succeeds
  for all four kinds. Tests: `TestSchemaPageScaffoldPassesSharedPreflight` (4 kinds),
  `TestSchemaPageScaffoldDeclaresBoundedDescriptorAndContract`,
  `TestSchemaPageScaffoldRejectsUnsafeFlags`.
- Bilingual docs updated: `docs/zh-CN/development/cli.md`,
  `docs/en-US/development/cli.md`, and the "What `--schema-page` generates" section in
  `docs/extensions/authoring-guide.md`, including the bounded-rule caps and the honest
  note that `form` renders read-only until the `dataRoute` transport exists.

Measured CLI evidence:

```text
$ sforum make:plugin --id acme.schema.demo ... --schema-page orders --schema-page-kind list
Created plugin scaffold at /tmp/sfschema      # schemas/page-orders.json, schemas/page-orders-contract.json, theme.json, sforum.extension.json
$ sforum extension validate /tmp/sfschema
OK  /tmp/sfschema
$ sforum extension test /tmp/sfschema
  errors: 0  warnings: 0  checks: 3
$ # rejections
schema-page-kind requires --schema-page
schema-page-kind must be one of list, detail, form, notice
schema-page and public-page cannot share the slug docs
```

## M9 - Close-out (2026-10-16)

Status: **done.** Item by item, with where each one came from:

| Item | State | Evidence |
| --- | --- | --- |
| `--public-page` | delivered earlier by the authoring-speed plan M3 | `makeOptions.PublicPage` + `TestPluginPublicPageScaffoldPassesContractAndRuntimePreflight`, `TestPluginPublicPageScaffoldDeclaresExactThreeWayIdentity`, `TestThemePublicPageScaffoldPassesRuntimeThemePreflight` |
| `--schema-page` | delivered 2026-10-16 (this task book) | see "M9 - `--schema-page`" above: 3 tests incl. all four kinds, CLI evidence (`validate` OK / `test` 0 errors), rejections before write |
| `extension dev` | already shipped by the authoring-speed plan M8; verified here | `apps/api/cmd/sforum/dev_extension.go` (573 lines) + 12 passing tests (`TestExtensionDevRefusesNonDevelopmentEnvironments`, `TestExtensionDevRefusesProtectedAndForeignRoots`, `TestExtensionDevWatchSetIgnoresBuildOutputs`, `TestExtensionDevLoopCoalescesAndRunsOneTransactionAtATime`, `TestExtensionDevLoopKeepsLastHealthyDigestOnFailure`, `TestExtensionDevOnceRunsOneRealBuildTransaction`, `TestExtensionDevLoopIgnoresItsOwnBuildWrites`, `TestExtensionDevLoopRebuildsOnDeletedSourceInput`, plus the child-process group-kill pair and the orphan-cleanup pair) |
| runtime surface catalog | delivered 2026-10-16 (this task book) | see "M9 - Public page runtime surface catalog" below: generated `public-page-surfaces.{json,md}`, `--check` fresh at 18 files, `tests/validate-public-page-surface-catalog.mjs` wired into `scripts/ci/contracts-gate.sh` |
| bilingual docs | delivered | `docs/zh-CN/development/cli.md` and `docs/en-US/development/cli.md` both document `extension dev`, `--public-page`, `--public-page-layout`, `--schema-page` and `--schema-page-kind`; the migration pair `docs/extensions/migrating-from-sforum-v2.md` / `docs/zh-CN/extensions/migrating-from-sforum-v2.md` exists; the authoring guide carries the "Where to look before you declare a page" and "What `--schema-page` generates" sections |

Honest attribution: two of the five items were not built in this task book. `--public-page`
belongs to the authoring-speed plan's M3 and `extension dev` to its M8; this session
verified both rather than re-implementing them, and delivered the other three.

## M9 - Public page runtime surface catalog (delivered 2026-10-16)

Status: **delivered.** This is the "runtime surface catalog" item of M9: a generated
inventory of the public page layer an extension can actually target.

`docs/extensions/v3/catalogs/public-page-surfaces.{json,md}` is emitted by
`scripts/v3-catalog/generate.mjs` (so `--check` keeps it fresh) from the same single
handwritten sources the runtime reads:

- `Pages.coreCatalog` — page id, path pattern, access, contract, core component,
  `replaceable`, `requiresFeatures`, `virtual` (pathless error-boundary pages).
- `RegionCatalog.pageRegionMatrix` — the regions each page exposes.
- the `PageView` vocabulary in `theme_package.go` — the body sources a `theme.json`
  page may declare (`template`, `component`, `schema`).
- the `PageLayout` vocabulary — `site`, `bare`.
- `allowedHostIslands` in `template.go` — the 41 Host islands themes may mount,
  including the reviewed body exit `sf-plugin-page-body` and the public chrome islands.

Measured output: **36 pages, 12 regions, 3 views, 2 layouts, 41 host islands.**

`tests/validate-public-page-surface-catalog.mjs` (new, wired into
`scripts/ci/contracts-gate.sh`) pins what the generator will not reject on its own: the
view vocabulary is exactly `component|schema|template`, the layout vocabulary is exactly
`bare|site`, the page set equals `Pages.coreCatalog` in both directions, every page's
regions exist, the reviewed body/chrome islands are present, the markdown mentions every
vocabulary value, and the catalogs are fresh.

Docs: `docs/extensions/v3/README.md` (Generated Catalogs) and the authoring guide's
"Where to look before you declare a page" section.

## M6 - Public Plugin UI entry + public `view: component` pages

Status: **stages 1-2 implemented; stage 3 (end-to-end evidence) pending.** M5 (the public `layout: site|bare` shell) was
delivered by the authoring-speed plan, which was M6's only listed blocker, so M6
can start. The inventory below was taken from the code on 2026-10-16 so the next
session does not have to re-discover it.

### What already exists (verified by inspection)

| Piece | Where | Note |
| --- | --- | --- |
| Public L2 mount mechanism | `apps/web/app/runtime/public-extensions/mount.ts`, `types.ts` (`PublicFrontendBridgeV1`, `mount`/`unmount`) | Already mounts a public plugin component with a bridge; today it is driven by `apps/web/app/components/SFExtensionWidget.vue` for region widgets. |
| Reviewed public body exit | `apps/web/app/components/extensions/public/SFRegistryPageBody.vue` | Renders `templateHtml` / `renderOutput` today; this is the single reviewed exit a `view: component` page should mount into. |
| Theme shell wrapping | `SFRegistryPageShell.vue` + the `sf-plugin-page-body` island (API `allowedHostIslands` + `productionThemeIslandBindings`, web `islandComponents` + `legacyIslandBindings`) | A component page inherits the same shell/island plumbing as a template page; no new chrome owner is needed. |
| Page resolve contract | `apps/api/app/Support/Pages/` (`ThemePageDecl`, `resolvePath` → `templateHtml`/`renderOutput`/`shellHtml` + identity) | Resolution returns a Go SSR template today. |

### What is missing (the actual M6 work) — sharpened 2026-10-16 after reading the runtime

**Correction to the first draft of this section.** The first pass said "no public
frontend declaration exists". That is true of the *page* declaration but false for
*components*: public L2 components are already declared in
`manifest.components[]` (`ManifestComponent{id, contractVersion, action, targetId,
l2Component, propsSchema, …}`, `v3_registry_types.go:56-69`), admitted by
`publicManifestComponent` (`app/Models/Extensions/public_frontend.go:362-370`), served
as a digest-addressed descriptor by
`GET /extensions/runtime/{extensionId}/components/{componentId}`
(`publicComponentPath`, `public_frontend.go:45-110`) after publication snapshot, asset
plan, executable-trust validation and admission, and mounted on the web side by
`SFExtensionWidget.vue` through `loadPublicFrontendRelease` +
`mountPublicFrontendModule` with quarantine and lease monitoring. A fixture already
exercises it (`extensions/fixtures/plugins/sforum-region-demo`, component
`sforum.region.demo.component.card`, `action: add`).

So the machinery exists; what is missing is only the **page-level binding**:

1. **`ThemePageDecl` has no `view`.** Public pages are declared in `theme.json`
   (`app/Support/Pages/theme_package.go:33-46`) with
   `id/action/target/path/template/contract/access/permission/layout/data`; `template`
   is effectively required, so "this page's body is an L2 component" cannot be
   expressed. `view`/`component` exist only for admin pages
   (`admin_micro_frontend.go:105-108`).
2. **`ResolvedPage` has no component field.** It carries
   `TemplatePath/TemplateHTML/DataSource/DataRoute/DataSchema/Version/PackageDigest/…`
   (`app/Support/Pages/catalog.go:96-115`) and `CoreComponent` exists only on the core
   catalog. `resolvePath` therefore cannot hand the body island a component
   descriptor.
3. **The body island has no component branch.** `SFRegistryPageBody.vue` renders
   `loadFailed` → `SFThemeTemplate` (`renderOutput || templateHtml`) → `SFEmptyState`;
   a fourth branch (mount the page's L2 component through the existing public mount
   path) is what M6 adds.

### Delivered 2026-10-16 (stages 1-2)

- **Declaration + validation (Go).** `ThemePageDecl` gained `view` (`template`
  default | `component`) and `component.id`; `PageView`/`NormalizePageView`/
  `ValidateThemePageViews` live in `app/Support/Pages/theme_package.go`, and the check
  runs at the single read point `LoadThemePackage`, so the CLI (`extension validate`,
  `extension test`) and runtime activation share one verdict. Unknown `view` values,
  `view: component` with a template, `view: component` without a component id, and a
  component on a template page are all rejected.
- **Pass-through.** `PageContribution` carries `view` + `componentId`
  (`ContributionsFromTheme` normalizes the view and trims the component id);
  `ResolvedPage` carries `view` + `component`; the transport response exposes both, and
  a component page is **exempt** from the "no template ⇒ runtime unavailable" fallback
  rule (`controller.go`), because an empty `TemplateHTML` is its normal state.
- **Render path (web).** `SFRegistryPageBody.vue` gained a component branch that mounts
  the page's public L2 component through the existing `SFExtensionWidget` (descriptor
  endpoint → `loadPublicFrontendRelease` → `mountPublicFrontendModule`, with quarantine
  and lease). The payload type (`app/utils/pageResolve.ts`), the body contract
  (`useRegistryPageBody.ts`) and both catch-all route shells pass `view`/`component`
  through, so the theme shell's `sf-plugin-page-body` island receives them via
  `v-bind="body"`. No second chrome owner: the component mounts inside the reviewed
  body exit, not around it.
- **Tests.** Go: `TestThemePageViewDeclarationContract` (7 cases) and
  `TestContributionsFromThemeCarryPageViewAndComponent`. Web:
  `mounts a page-provided public component through the reviewed body exit` in
  `apps/web/tests/framework/pageOutlet.test.ts` (branch order, no chrome in the body,
  pass-through in both route shells and the payload type).
- **Architecture ratchet.** Two baselines were **lowered**, never raised:
  `Support/Pages/registry.go` 1017 → 1008 (the snapshot helper moved to
  `registry_snapshot.go`) and `Http/Controllers/Pages/controller.go` 1027 → 1023 (the
  fallback provider/action normalization moved to `externalProvider` in the transport
  file).

### Stage 3 - what is now evidenced, and what is still open (2026-10-16)

**Fixture added:** `extensions/fixtures/plugins/sforum-page-component-reference` declares
one `view: component` public page (`/page-component-reference/component`) whose body is
the package's public L2 component, **next to** an ordinary template page
(`/page-component-reference/template`) so both body sources can be compared on one
package. It passes `extension digest --write` / `validate` / `test` (0 errors, including
`template.runtime_exact`). Writing it surfaced the exact declaration rules a public L2
component needs: a `propsSchema` versioned ref (with a matching `kind: schema`
packageFile), an `l2Component` handle that resolves to a `kind: frontend` packageFile,
and `theme.json` declared as a `kind: asset` packageFile.

**Resolve-path evidence (real HTTP, dev instance):** the fixture was installed and
enabled through the approved flow (`tests/admin-extension-activation.sh`), and the live
resolve response was:

```text
GET /api/v1/pages/resolve-path?path=/page-component-reference/component
  → {"view":"component","component":"sforum.page-component-reference.component.body",
     "fallback":false,"reason":null,"provider":"sforum.page-component-reference",
     "templateHtml":null,"templatePath":null,"layout":"site"}
```

Notably the activation script issued an **exact-artifact challenge** for this package
(64-char token, accepted) even though it ships no Go backend — declaring public L2
browser code is itself an executable surface, which is the correct gate.

### Stage 3 evidence, refreshed 2026-10-16 (later pass) - the blocker is gone, one item stays open

The open question recorded below ("a `view: component` public page cannot be admitted for a
package with no runtime instance") was **fixed** in this program's later pass: components-only
packages now publish their component runtime at enable time. Re-verified end to end on the dev
instance:

```text
GET /api/v1/pages/resolve-path?path=/page-component-reference/component
  → view=component  component=sforum.page-component-reference.component.body
    provider=sforum.page-component-reference  fallback=false  reason=null
    layout=site  templateHtml=null

GET /api/v1/extensions/runtime/sforum.page-component-reference/components/sforum.page-component-reference.component.body
  → 200 {"schemaVersion":"sforum.public-frontend-component@1","apiVersion":1,
          "trustNotice":"fully_trusted_browser_code","extensionId":"sforum.page-component-reference",
          "extensionVersion":"1.0.0","packageDigest":"e46154d9…"}      (this was the 404 blocker)
```

The fixture was installed and enabled through the approved flow (`tests/admin-extension-activation.sh`
→ install 201 inert → exact-artifact challenge → enable 200; declaring public L2 browser code is
itself an executable surface, so the challenge is correct).

**Desktop browser evidence (real session, 1238px):** the component **mounts inside the reviewed body
exit** and renders its own content plus the host trust notice:

```text
body text: "… 此扩展在浏览器中以完全信任方式运行 sforum.page-component-reference v1.0.0 的公开组件
可使用当前浏览器会话与页面权限调用其命名空间 API。这不是沙箱：仅在你信任该扩展时启用公开 L2。
page component reference body extension=sforum.page-component-reference api=1 © 2026 SForum。保留所有权利。"
shell=site  shellSource=theme  visible headers=1  visible footers=1
```

The SSR HTML confirms the client-only nature: it contains the body island
(`sf-plugin-page-body`) and the descriptor (`sforum.page-component-reference.component.body`) but
**not** the component's own text or the trust notice - those arrive from the client mount.

**390x844 (real Chrome CDP, `tests/browser-public-390x844.mjs`):** chrome/overflow/clipping/console
assertions pass for the component page (`shell=site/theme headers=1 footers=1 overflow=false
clipped=0 consoleErrors=0`), and the harness now waits for the body text to settle and scrolls the
page first.

### M6 closed (2026-10-16): the headless reading was a measurement artefact

**RESOLVED: that reading was a harness defect, not a client gap.**
The paragraph above recorded, honestly, that in the headless CDP context the component content did
not appear and that no descriptor request was issued at all, reproducibly at 390/500/900 widths and
even with a 25 s settle. The cause is now measured:

- `tests/browser-public-390x844.mjs` decided the page had "settled" by waiting for
  `document.body.innerText.length` to stop changing. **The SSR text already satisfies that before
  hydration**, so the exit condition (two consecutive stable 250 ms polls) fired at **≈1 982 ms** —
  before the client had even finished hydrating.
- The component actually mounts at **≈5 427 ms** in the same context, page and width, and issues the
  descriptor requests at that point (`200 /api/v1/extensions/runtime/sforum.page-component-reference/components/sforum.page-component-reference.component.body`, plus `content-styles` and a lease revalidation).
- That is also why raising `SFORUM_SETTLE_MS` to 25 s never helped: the loop exited on "2 stable
  polls", never on the budget.

So the "does not mount in headless" observation was an artefact of measuring too early, and the
"regular session mounts, headless does not, at every width" discriminator is fully explained by it.
The same headless CDP context mounts the component at 390x844 once it is measured after hydration.

**Harness fix (durable, so this cannot recur silently):** the settle loop now (1) waits for real
hydration (`#__nuxt.__vue_app__`, with its own bounded budget), (2) refuses to accept stability before
hydration, (3) requires a **terminal public-L2 state** (`mounted|fallback|quarantined`, never
`idle|loading`) for paths listed in the new `SFORUM_L2_PATHS`, and (4) can assert a mount outright —
`data-l2-state=mounted`, a non-empty L2 target container, and the component's own text — failing
loudly (including when hydration never happens) instead of reporting a pass it did not measure. A
negative control (`SFORUM_L2_PATHS=/probe-site`, a page with no L2 widget) was run and does FAIL.

**Measured 390x844 evidence (real Chrome 154 over CDP, dpr 3, five public paths):**

```text
$ SFORUM_PUBLIC_PATHS='/page-component-reference/component,/page-component-reference/template,/loader-articles/hello,/probe-site,/probe-bare' \
  SFORUM_L2_PATHS='/page-component-reference/component' SFORUM_QA_DIR=storage/qa \
  node tests/browser-public-390x844.mjs
/page-component-reference/component: 390x844 dpr=3 shell=site/theme headers=1 footers=1 overflow=false clipped=0 consoleErrors=0
  hydrated=true@1874ms settle=2642ms l2=mounted/kids=1 trustNotice=true marker="page component reference body"
/page-component-reference/template:  390x844 dpr=3 shell=site/theme headers=1 footers=1 ... l2=null/kids=-1
/loader-articles/hello:              390x844 dpr=3 shell=site/theme headers=1 footers=1 ... text="plugin-loader: Loaded from the plugin runtime: hello"
/probe-site:                         390x844 dpr=3 shell=site/theme headers=1 footers=1 ...
/probe-bare:                         390x844 dpr=3 shell=bare/none  headers=0 footers=0 ...
all public 390x844 acceptance assertions passed
screenshots: storage/qa/2026-10-16-public-390x844-{page-component-reference-component,page-component-reference-template,loader-articles-hello,probe-site,probe-bare}.png
   + JSON: storage/qa/2026-10-16-public-390x844-evidence.json
```

The `view: template` control page on the same package stays `l2=null` (no widget at all), so the two
views remain cleanly separated at 390x844 as well. This closes T7 of the close-out checklist: the
component page's 390x844 mount evidence uses the same assertion set (chrome counts, overflow,
clipping, console errors) that the other paths use.

**Superseded reading, kept for the audit trail:** in that headless CDP context the component **content did
not appear** in the *revision-1* harness run — no descriptor request was recorded (network log showed only theme/extension CSS
200s; no `FAILED` entries; `consoleErrors=0`), reproducibly at 390/500/900 widths, with a 25s settle,
and with or without an injected session cookie. The same page **did** mount in the regular browser
session. So it was taken to be either a headless-context client-mount artefact or a real client-side gap,
and the 390x844 component-mount evidence was **not claimed** at the time. It is claimed now, on the
measurement above.

**Still open (precise blocker).** The browser half is not done, because the descriptor
endpoint the body island calls returns:

```text
GET /api/v1/extensions/runtime/sforum.page-component-reference/components/…component.body
  → 404 {"reason":"extension.frontend_trust_not_found"}
GET /api/v1/admin/extensions/sforum.page-component-reference/frontend
  → 200 {"kind":"none","trustState":"none"}
```

**Refined in the follow-up pass (and correcting the first wording).** That 404 code is
what `mapPublicFrontendError` returns for **`ErrPublicFrontendUnavailable`**
(`app/Http/Controllers/Extensions/public_frontend.go:163-167`), folded on purpose so a
public caller cannot distinguish disabled / revoked / stale / changed / undeclared
artifacts. So the 404 does **not** prove a missing frontend trust grant, and the first
pass's hint at the admin frontend-trust flow as the likely gate was not substantiated:
`FrontendService.Challenge` and `Grant` both require `primaryAdminComponent(extension)`
(`frontend_service.go:130,170`), i.e. an `admin.entry`, so that flow is admin-only by
construction and irrelevant here.

**Root cause found in the follow-up pass (three runtime inspections, dev instance).**

1. The dev instance already has both migration flags on
   (`SFORUM_V3_TRUST_CHALLENGES=true`, `SFORUM_V3_PUBLIC_L2=true` — read from the running
   process environment), and `component-inspector` reports `safeMode: false`. So neither
   the trust flag nor Safe Mode is the cause.
2. The **asset publication is created correctly** for a components-only package:
   `asset-inspector` while the fixture was enabled showed publication #3 for
   `sforum.page-component-reference` with handle
   `sforum.page-component-reference.component.body.l2.entry` (type `script`). So
   `BuildPublicAssetPublication` is not the problem.
3. The gap is in the **component registry**: the resolve response for the page carries
   `selectedRuntimeInstanceId: null` (the package ships no backend, so it has no runtime
   instance), `component-inspector` shows `contributionCount: 1` with **no trace
   mentioning this package**, and `ComponentRegistry.AdmitPublicComponent`
   (`app/Support/Extensions/component_registry.go:439-465`) requires both a matching
   `registrations[extension.ID]` (by instance id) and a published
   `contributionsByID[component.ID]` whose artifact matches the extension tuple.
   Without them `PublicComponent` returns `ErrPublicFrontendUnavailable`, which the
   public handler folds into the 404 seen earlier.

So a `view: component` public page currently cannot be admitted for a package that has
**no runtime instance** (i.e. no executable backend). Note that
`componentPackageRuntimeInstanceID` (`component_registry.go:597-607`) derives a
*package-level* instance id from type + id + version + digest, which suggests the
intended registration source is a package publication rather than a backend runtime —
so this may be a **gap** (a components-only package should get a package-level
registration) rather than an intentional gate. That question is **open** and needs a
human decision before either fixing it or documenting the constraint; the next step is
to read the registration call sites (`ReplaceRuntime` / `RestoreRuntimes`) and see which
lifecycle step is expected to register a package that never starts a backend.

What the working path needs is demonstrated in Go:
`TestPublicFrontendRequiresLiveExactArtifactTrust` calls `PublicComponent` with a
components-only manifest and gets `ErrPublicFrontendUnavailable` **before** the trust
grant, then a full descriptor **after** `grantPublicFrontend` + `publishTrustedPublicAssets`
— i.e. (a) the live exact-artifact trust grant (the enable flow did grant this: the
activation script completed a 64-char challenge for this package) and (b) a **published
public asset publication**. The dev-instance failure therefore sits in the
publication/admission branches of `PublicComponent`, and because the public error is
folded, the next diagnostic has to run **inside** the process: reproduce the dev
bootstrap in a Go test (or temporarily unwrap the error) rather than reading it from the
endpoint. The Go suites cover the pipeline extensively (`public_frontend_test.go`,
`public_l2_p9_joined_matrix_test.go`), so this is a lifecycle/publication wiring
question, not a missing capability — still **unverified**.

Measured browser state while the fixture was enabled: theme shell `site`/`theme`,
exactly one visible navbar and one visible footer, no overflow, and
`componentMounted=false`.

### Stage 3 - closed, with two real defects found on the way (2026-10-16, same day)

The browser half is **verified**. Two defects had to be cleared first, and both are
recorded here because they are durable:

**Defect 1 - the enable path does not register a components-only package. Root cause
found:** `Service.Enable` routes to Lifecycle V2 only when
`usesLifecycleV2(extension)` is true, and that predicate requires
`Manifest.Backend.ProtocolVersion == 2` **and** a declared `manifest.lifecycle`
contract (`app/Models/Extensions/service_lifecycle_v2.go:64-67`). A package whose only
executable surface is a public L2 component satisfies neither, so it takes the legacy
enable path, which publishes the asset publication (measured: the `.l2.entry` handle is
present) but **never reconciles the component registry** — `reconcileComponents`
(`lifecycle_registry_publication_components.go:44`) only runs inside the Lifecycle V2
publication, and its sibling `validateComponentTransition` (`:10`) is likewise
V2-only. The intended end state is produced at Host start by `RestoreRuntimes`
(`component_registry.go:245-266`), which registers every enabled V3 package with
components under `componentPackageRuntimeInstanceID`.

**FIXED 2026-10-16 (same day).** The convergence is implemented and verified:

- `ComponentRegistry` gained two exported methods
  (`app/Support/Extensions/component_registry.go`):
  `PublishComponentPackageRuntime(extension)` — no-op unless the package is enabled and
  declares components, otherwise `ReplaceRuntime` with
  `componentPackageRuntimeInstanceID` — and `RemoveComponentPackageRuntime(extensionID)`
  (idempotent; `false` when nothing is registered). Keeping the instance-id derivation in
  the Support layer avoids a Models→Support import cycle and a second source of truth.
- The legacy enable/disable paths call free functions
  `publishLegacyComponentRuntime(ctx, s, extension)` /
  `removeLegacyComponentRuntime(ctx, s, extensionID)` in
  `app/Models/Extensions/legacy_plugin_runtime_publication.go`. They are free functions,
  not `serviceCore` methods, because the package's receiver-method cap is 79 and the
  first attempt pushed it to 81 — the gate caught it. They reach the registry through a
  narrow interface (`componentPackageRuntimePublisher`) so existing `ComponentRegistry`
  fakes keep compiling, and they run **after** the enable is committed, so a publication
  failure degrades to the previous "next Host start" behaviour and records an event
  instead of failing the enable.
- `service_lifecycle.go` went 994 → 996 lines (still ≤ the 1000 hard limit); the
  architecture gate passes with no baseline change.
- Tests: `component_registry_package_runtime_test.go` (startup/lifecycle instance-id
  convergence, no-op cases, idempotent removal) and
  `legacy_component_runtime_publication_test.go` (the legacy call sites publish only for
  enabled component packages, skip otherwise, and swallow publication failure).

Runtime verification (dev instance, **no Host restart**): install → enable → the
descriptor endpoint answered **200** (it answered the folded 404 before the fix) and the
browser reported `l2State=mounted` with the component inside the theme shell, exactly one
navbar and one footer, no overflow. After `disable` + `uninstall` the component registry
returned to `contributionCount: 1` and the extension ledger had no row.

**Original fix design, with the constraints that shaped it.** The legacy enable
path must converge to the state `RestoreRuntimes` produces: publish the component runtime
(`ComponentRegistry.ReplaceRuntime(extension, componentPackageRuntimeInstanceID(extension))`
— a public method, `component_registry.go:151-153`) for an enabled V3 package with
`len(manifest.components) > 0`, and remove it on disable. `serviceCore` already holds
`componentRegistry ComponentRegistry` (`service.go:63-64`), so no new wiring is needed.

What makes this more than a small patch, measured on 2026-10-16:

- `app/Models/Extensions` is at **exactly** its flat-directory cap (95/95 non-test `.go`
  files) and `app/Support/Extensions` is likewise at 148/148, so neither a helper file nor
  a new collaborator can be added without extracting first.
- `app/Models/Extensions/service_lifecycle.go` is **994 lines** against the 1000-line hard
  limit, so even the three-line call site needs a small extraction in the same change
  (the repo's ratchet rule), or the helper must live in an existing Support file with
  headroom (`component_registry.go`) plus a call site that fits.
- It also changes **when** approved public L2 browser code goes live: today an uploaded
  package's public component becomes reachable only after a Host restart, and the fix makes
  it reachable immediately after enable. The exact-artifact trust grant is still required
  (`publicRuntimeGates`), so this is convergence, not a relaxation — but it is a behaviour
  change worth an explicit decision.

The test to add is a disable/enable/restore matrix asserting that the component registry
snapshot after enable equals the snapshot after a Host restart for a components-only
package.

**Original measurement.**
`ComponentRegistry.RestoreRuntimes` (`component_registry.go:245-266`) registers exactly
the intended set at Host start — every enabled V3 plugin/theme with
`len(manifest.components) > 0`, keyed by `componentPackageRuntimeInstanceID`
(type + id + version + digest, `:597-607`). The **enable** path does not converge to that
same state: measured with `component-inspector`, right after install + enable the
registry reported `contributionCount: 1` with no trace for this package, and
`/extensions/runtime/{id}/components/{componentId}` answered the folded 404. After one
Host restart the same endpoint answered **200** with the full descriptor and
`contributionCount` became 2. So a package whose only executable surface is a public L2
component is unusable until the next restart. This is a **defect**, not a design gate
(startup converges to the state the enable path should have produced).

**Defect 2 - the public L2 module contract requires named exports. FIXED 2026-10-16:**
`extensions/fixtures/plugins/sforum-region-demo/frontend/region-card.mjs` and
`extensions/fixtures/plugins/sforum-commerce-workflow/frontend/order-card.mjs` were both
`export default`; both now use named `apiVersion` + `mount`. The commerce module was also
a Vue-style `{name, props}` descriptor with no mount at all, so it could never have
rendered; it now mounts a minimal order card from `bridge.props`. The region-demo package
digest was refreshed (`extension digest --write` → `validate` OK → `test` OK); the
commerce fixture substitutes digests from the files at test time, so nothing needed
refreshing there. `go test ./sdk/... ./app/Support/Extensions/` is green.

**Original measurement.**
`isPublicFrontendModule` (`apps/web/app/runtime/public-extensions/assets.ts:219-224`)
checks the **module namespace** (`module.apiVersion === 1 && typeof module.mount ===
'function'`). The fixture initially used `export default { apiVersion, mount }`, which the
loader rejected as `public component module contract mismatch` and the widget fell back
(`data-l2-state="fallback"`). Switching to named `export const apiVersion = 1` +
`export function mount(...)` mounted it. Note that
`extensions/fixtures/plugins/sforum-region-demo/frontend/region-card.mjs` still uses
`export default`, so that fixture's widget could never mount in a browser — worth fixing
or documenting separately.

**Browser evidence** (fixture installed and enabled, real Chrome, desktop viewport):

```text
/page-component-reference/component
  shell=site shellSource=theme
  visibleDesktopNavbars=1 visibleMobileNavbars=0 visibleFooters=1
  l2State=mounted componentMounted=true
  componentId=sforum.page-component-reference.component.body
  title="page component reference body"
  note="extension=sforum.page-component-reference api=1"   (bridge injected)
  insideShell=true        (mounted inside the reviewed exit, not around it)
  horizontalOverflow=false clippedControls=0
```

So a `view: component` public page mounts its package component inside the theme shell
with exactly one navbar and one footer and no layout damage.

**CORRECTION (2026-10-16, follow-up pass): the `view: template` control page renders
correctly.** The earlier "renders no body content" note was a **measurement error**: the
probe looked for `[data-layout]`, an attribute the API never emits. Measured with the
fixture installed and enabled:

```text
/page-component-reference/template  (browser)
  body text: "Site layout reference" / "layout: site — the Host supplies exactly one navbar and one footer."
  main innerHTML: <h1>Site layout reference</h1><p>layout: site — …</p>
  .sf-theme-template blocks: 2 (theme shell + page body)   no widget   no empty state
```

The template renders its HTML, the theme shell owns the chrome, and no `view=component`
widget appears on a template page — i.e. the two views are cleanly separated.

What the probe *did* find, as a real (by-design) constraint: the API's `templateHtml`
**drops `data-*` attributes**. The fixture file declares
`<p data-layout="site">…</p>`, and the served `templateHtml` is `<p>…</p>`.
`themeTemplatePolicy()` (`app/Support/Pages/template.go:100-108`) allows only a fixed
attribute allowlist — `class`, `id`, `title`, `role`, `aria-*` globally plus a few
element-specific attributes — so `data-*` hooks are stripped by the sanitizer. Plugin page
templates must use `class`/`id` hooks instead. Worth documenting for theme authors; it is
also why the round-38 probe misread the page.

Remaining stage-3 work: decide what to do about the `export default` fixtures, investigate
the template control page, and add catalog identity entries if a new UI surface turns out
to need one.

### Staged implementation (each stage must leave the repo green)

- **Stage 1 (contract + validation, fail-closed).** Add an optional public
  frontend declaration and a `view` discriminator on public pages
  (`template` default | `component`), with validation that
  `view: component` requires a resolvable component entry and that
  `template`/`component` are mutually exclusive. Unknown `view` values fail
  closed at package preflight, matching the `layout` precedent. **Do not ship
  this stage without Stage 2**: a declared-but-never-rendered capability is
  explicitly worse than an absent one (see the M4 cancellation in the frozen
  contract), so Stages 1 and 2 must land together or not at all.
- **Stage 2 (resolution + render).** Add a component field to `ResolvedPage`
  (extensionId/componentId/contractVersion/packageDigest/impactDigest/propsSchema) and
  fill it in the HTTP layer for `view: component` pages, reusing
  `FrontendService.PublicComponent`'s pipeline (publication snapshot → asset plan →
  executable-trust validation → admission) rather than a second path. Then add the
  fourth branch to `SFRegistryPageBody.vue`: mount the page component through the same
  `loadPublicFrontendRelease` + `mountPublicFrontendModule` + quarantine/lease helpers
  `SFExtensionWidget.vue` uses (extract the shared loader into a composable instead of
  copying it). CSP must aggregate the page component's module exactly as region widgets
  already do, and the page keeps exactly one chrome owner (the shell), so the mounted
  component must not render navbar/footer.
- **Stage 3 (evidence).** Go tests for accept/reject on the declaration, a web
  test for the mount path, a **browser** landing on a `view: component` public
  page showing the theme shell + exactly one navbar/footer, and the catalog
  identity entries for any new UI surface.

### Constraints carried from the frozen contract

- L2 code stays fully trusted after exact-artifact approval; it is not
  sandboxed, and no package build script runs at runtime.
- Nuxt Layers, runtime SFC compilation, and Host-private component imports stay
  unsupported.
- The public page keeps one chrome owner: the theme shell (or the Host fallback)
  wraps the body island; a component page must not mount its own navbar/footer.

## Verification Commands

```bash
cd apps/web && bun test
cd apps/web && bun run typecheck
cd apps/api && go build ./... && go test ./...
node tests/validate-architecture-boundaries.mjs
cd apps/api && go run ./cmd/sforum extension docs generate --check
node scripts/v3-catalog/generate.mjs --check
./scripts/test.sh
```

## Known Pre-Existing Failure

`node scripts/v3-catalog/generate.mjs --check` fails on
`apps/web/app/components/admin/ai/blocks/SFAdminAIIdentityBlock.vue` (tracked
and unmodified) because it needs a reviewed stable identity mapping. That
generator only scans `apps/web/app/components`; it is unrelated to this program
and must be fixed by whoever owns that surface.
