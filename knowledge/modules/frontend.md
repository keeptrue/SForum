# Frontend Module

## Purpose

Owns the Nuxt application, SSR shells, routing, metadata, browser interactions,
admin UI, reusable `SF*` components, and runtime theme presentation bridge.
Business rules, persistence, authorization, and search indexing remain API
responsibilities.

## Stack And Boundaries

- Nuxt 4, Vue 3, Nuxt UI 4, Tailwind CSS, and Bun.
- SSR-first public pages; `zh-CN` default and `en-US` secondary locale.
- Public pages render typed Fiber API read models. Nuxt server middleware may
  proxy same-origin/SSR requests but does not own domain policy.
- Same-origin API proxy retries failed `GET`/`HEAD` requests at most nine times
  after the initial attempt (500 ms apart) so an API Air reload does not drop
  immutable theme assets. Unsafe requests are never retried.
- Host resource delivery keeps public immutable theme bytes under
  `/_sforum/assets`; trusted admin component bytes load through the
  digest-bound `/api/v1/admin/extensions/{id}/frontend/assets/{digest}/{asset}`
  compatibility endpoint or the component-specific
  `/api/v1/admin/extensions/{id}/frontend/assets/{digest}/{component}/{asset}`
  endpoint.
- Public read-only assets (`/_nuxt/**`, `/_sforum/assets/**`, `/_sforum/icons/**`)
  are published as cross-origin readable: the app itself sets
  `Access-Control-Allow-Origin: *` and `Cross-Origin-Resource-Policy: cross-origin`
  (Nitro route rules for build assets, proxy `onResponse` for proxied extension
  assets), because origin-rewriting middleboxes (mobile QQ X5 cloud acceleration,
  CDNs) otherwise reject module scripts. Extension stylesheets keep `integrity`
  but not `crossorigin`, so a rewritten response degrades to a no-cors load
  instead of losing the stylesheet. Private
  `/_sforum/private-assets/**` stays `same-origin`.
- User-visible copy uses i18n keys. Public web options contain only frontend-
  safe state; secret metadata and writes use admin-only endpoints.
- Build/typecheck use `.nuxt-build` and `.nuxt-typecheck` so they do not disturb
  the user's port-3000 dev server.
- `@sforum/admin-sdk@1` is the public typed bridge contract and
  `@sforum/plugin-ui@1` is the public Vue presentation layer for trusted plugin
  page bodies. Plugin UI v1 supplies compact layout, form, feedback, empty, and
  table primitives using semantic Host variables plus standalone fallbacks. It
  has no Nuxt UI, router, composable, or private `SF*` dependency; Vite bundles
  Vue and the SDK into each plugin's immutable ESM/CSS artifact.
- Both SDKs are npm-pack verified with their license and public metadata. The
  package major must match the admin bridge API, scaffold dependency versions
  must match the package versions, and an offline tarball consumer build must
  emit usable ESM/CSS. Tag releases publish through npm OIDC only after image
  smoke and asset verification; image promotion waits for SDK publication.
  Existing-version retries compare tarball SHA-512 first, then a canonical
  path/mode/size/content digest when Node runtime differences alter archive
  encoding. Canonically different content under one version remains fatal.
- The offline SDK consumer's Vite toolchain is a direct Web dev dependency, and
  the Web Docker dependency stage copies both `admin-sdk` and `plugin-ui`
  workspace manifests before its frozen install. This keeps local, CI, and
  container workspace resolution deterministic.

## Runtime Site URL

- Production Site Config and i18n canonical URLs are runtime values. The Web
  image declares `runtimeConfig.site.url` and
  `runtimeConfig.public.i18n.baseUrl`, while the top-level i18n module config
  deliberately omits `baseUrl` so `nuxt-site-config` cannot bake the image
  build origin into its equal-priority stack.
- `production-entrypoint.sh` resolves both slots from the deployment's
  canonical `APP_URL`. A versioned Web image can therefore serve the correct
  SEO URL behind loopback/Compose ingress without host-side Caddy edits or a
  deployment-specific frontend rebuild.
- Web readiness must probe `/health`, not `/`; probing the homepage performs
  SSR work and can warm deployment caches with an internal request origin.

## Production Runtime (Bun)

- The web image runs the Nitro output under Bun, not Node. The `prod` Docker
  stage uses `oven/bun:1.3.14-alpine` (same fixed version as deps/build),
  installs no Alpine Node.js package, and starts with
  `CMD ["bun", ".output/server/index.mjs"]`.
- `nuxt.config.ts` sets `nitro.preset: 'bun'`. This is required: the default
  `node-server` output externalizes `srvx` (via `ipx`/`h3`) with only the
  `node` adapter, but `srvx`'s `exports` map matches Bun's `bun` condition
  first and points at a tree-shaken `dist/adapters/bun.mjs`, so Bun fails with
  `Cannot find package 'srvx'`. The `bun` preset bundles the bun adapter and
  serves via `Bun.serve()` honoring the existing `HOST`/`PORT` contract.
- The Bun image's `node` is a symlink to `bun` (CLI compatibility wrapper), not
  a real Node.js install; do not treat that link as Node.
- `@nuxt/image` keeps `ipx` + sharp; sharp `linux-x64` binaries are included in
  the build. Image-optimization requests are the residual runtime-verification
  surface not exercised end-to-end by this migration.
- See `decisions/2026-08-14-web-production-bun-runtime.md`.

## SEO Title Ownership

- Public page titles are resolved once by `useSForumSeo` and
  `utils/seo/seoResolver.ts`, including the configured SEO site name and
  content-type template.
- Public route-shell source guards must track that same authority. When a
  shared SEO migration touches public pages, run `cd apps/web && bun test` in
  addition to focused SEO slices so stale cross-domain route tests cannot reach
  CI first.
- `app.vue` only supplies the site name when a page has no title. It must not
  apply `seo.meta_title_template` or append the site name to an already
  resolved title, otherwise SSR emits values such as `SForum - SForum`.
- `seo.meta_title_template` remains readable as a legacy fallback for
  `seo.page.title_template`; the admin field is compatibility-only and must
  not become a second root-level title transform.
- Authentication, account settings, workbench, and external-auth continuation
  pages use the same resolver with `noindex` (and `nofollow` where required)
  so they retain one title authority while remaining out of search results.

## Active Work

### Admin overview runtime resource cards

- `/control-panel` now groups backend memory, API CPU, and 1/5/15-minute
  system load in one resource row, followed by a separate row for posts, users,
  and pending work. System load is the host run queue average, not a second CPU
  percentage.
- Resource data comes from the lightweight admin overview resource endpoint and
  refreshes every 5 seconds. The cards prefer the backend's 60-second RSS
  median, show a separate 60-second PSS median only when complete Linux samples
  exist, and expose per-plugin PSS medians plus current `AnonHugePages` in the
  details popover ordered by current process RSS.
- Missing process or filesystem samples render as localized unavailable states,
  and an embedded Worker is shown as included in API usage with its slot/running
  count instead of a fabricated standalone MiB value.
- The narrow toolbar stacks its metadata and commands; the refresh command
  collapses to an icon with an accessible label/title so `/control-panel` has no
  horizontal overflow at 390px.
- Resource gauges use a 72x72 coordinate system with a centered radius-30 ring;
  percentage input is clamped to 0..100 and dash offset decreases from empty to
  full, so CPU/memory status is neither clipped nor visually reversed.
- The CPU card uses the gauge's backward-compatible large presentation: a
  144px narrow-screen / 160px desktop ring, rounded progress cap, stronger
  percentage typography, CPU-blue progress, and a flex-filled body that keeps
  the gauge centered against the taller memory card.
- Backend focused tests, frontend helper tests, locale parsing, OpenAPI
  reference validation, Nuxt typecheck, and Browser QA passed at 1440x900 and
  390x844 before the large-gauge visual follow-up. The follow-up has focused
  SFC compiler and diff checks; rendered desktop/mobile confirmation is left
  for operator manual QA.

### Admin overview real-time updates

- Resources (processes, disk, and system load) now auto-refresh every **5 seconds** via dedicated lightweight endpoint.
- Full overview / KPI cards refresh every **30 seconds**.
- Polling respects page visibility and KeepAlive; manual refresh unchanged.
- Omitted resource fields preserve previous values (no flash to "unavailable").

### Admin user list sorting

- `/control-panel/users` exposes compact field and direction selectors for
  registration time, update time, username, display name, email, and account
  status. Changes reset pagination to page 1 and request server-side sorting,
  so the operator is never shown a client-only reordering of the current page.
- `SFAdminUserListToolbar` owns the search, status/group filters, sort controls,
  refresh action, and count badges. Extracting it reduced the legacy route shell
  from 1595 to 1570 lines and the architecture ratchet was lowered accordingly.
- Component interaction tests and Nuxt typecheck pass. Rendered desktop/mobile
  QA remains pending because the in-app browser had no admin session and the
  logged-in Chrome tab timed out twice while being claimed by Browser control.

### Forum code blocks

- Sanitized topic, comment, moderation, and editor-preview code blocks share the
  Demo 02 paper-line presentation: compact language header, sticky line-number
  gutter, internal horizontal scrolling, and an accessible copy command with
  localized success/error Toast feedback.
- The client directive uses the existing `highlight.js` package through its
  common language bundle plus Dart, Dockerfile, HTTP, Nginx, and PowerShell.
  Forum aliases resolve to canonical grammars, while unknown explicit labels
  remain visible without guessed highlighting and unspecified blocks remain
  localized plain text. Enhancement is idempotent across directive updates.
- Light and dark themes use SForum semantic code tokens. The enhanced figure is
  the only framed surface; nested `pre` elements must stay transparent,
  borderless, and square so selected-theme prose rules cannot reintroduce a
  second rounded border.

### Rich content fences (mermaid / chart / math)

- `v-highlight` now owns prose enhancement end to end: after `enhanceCodeBlocks`,
  `enhanceRichContent` takes over `figure.sf-code-block[data-language="mermaid|chart"]`
  and rewrites `$…$` / `$$…$$` text nodes. Authoritative limits live in
  `contracts/fixtures/rich-content-v1.json`; TS mirror and types in
  `apps/web/app/utils/content/`.
- Each fence becomes `figure.sf-rich[data-sf-rich][data-sf-rich-state]` with a canvas,
  a `<details>` holding the original code figure, and a note. Blocks already in the
  viewport paint immediately; the rest are lazy (IntersectionObserver, 300px) and capped
  per kind (6 per document); oversize, over-quota, parse failures and aborts fail open
  to the source block. A pass that is cleaned up by the next `updated` must not leave a
  block stuck on its skeleton, which is why the viewport fast path exists.
- No-flash skeleton: an inline head script in `nuxt.config.ts` marks
  `html[data-sf-rich="pending"]` before hydration (with an 8s self-clearing fallback for
  blocked scripts), CSS covers rich fences with a shimmer while keeping the source in the
  DOM, and the first client pass flips the flag to `ready`. The wrapper's canvas gets the
  original code block's height as `minHeight`, so the diagram does not push the page.
- Mermaid: `securityLevel:'strict'`, `htmlLabels:false`, `secure` locks security **and
  theme keys** (`themeCSS`/`themeVariables` would otherwise let a diagram write user CSS
  into the in-SVG `<style>`); output passes DOMPurify (svg profile) and then a scrub that
  removes `<script>`, `on*`, and **unwraps every `<a>`** — mermaid 11 renders `click`
  URLs as anchors even in strict mode, which would bypass the outbound-link interstitial.
- Chart: strict JSON (6 keys, 5 types, ≤6 series, ≤60 labels, finite |v| ≤ 1e12),
  Chart.js canvas (fixed 280px height, `maintainAspectRatio:false`), colors from
  `--sf-rich-text` / `--sf-accent`, reduced motion disables animation. Aborting a pass
  must **not** destroy a chart whose canvas is still in the document — only a canvas that
  left the DOM (re-render replaced the wrapper) is disposed; otherwise the chart vanished
  milliseconds after rendering. Math: KaTeX `output:'mathml'` (no inline styles → survives the public page
  document CSP), `maxExpand` 1000, block vs inline via `data-math-display`.
- CSP degradation: pages carrying `[data-document-policy-digest]` render mermaid as
  source with a note; math (MathML) and charts (canvas) still render. Color-mode changes
  re-render fences via `rerenderRichContent`; math spans are left alone because MathML
  inherits `currentColor`.
- Mermaid theming: `theme:'base'` + site tokens (`--sf-rich-node-bg`, `--sf-rich-border`,
  `--sf-rich-text`, `--sf-rich-edge`) and a **concrete font family** — mermaid measures
  text with the configured family, so `inherit` produced nodes too small for CJK labels.
  `rich-content.css` then overrides node fills/strokes and edge width, because mermaid's
  classic theme paints 3px strokes in the fill colour.
- The mention linkifier skips whole SVG and MathML subtrees: its tag allowlist compares
  uppercase `tagName`, while SVG elements (`<style>`, `<text>`) are lowercase — mermaid's
  `@keyframes` was being linkified into `/u/keyframes` and corrupted the diagram.
- Tests: `apps/web/tests/content/*` (fence wrap/fallback/quota, mermaid security and
  anchor unwrapping, chart schema, math delimiters, fixture parity) plus
  `tests/forum/forumMentions.test.ts` for the SVG/MathML skip.

### Responsive public sidebar source

- Desktop and mobile public left navigation now consume
  `public.sidebar.primary`; the web runtime no longer requests or exposes a
  separate mobile item list.
- `SFPublicSidebarContent` owns compose behavior, ordinary links, active state,
  dynamic category placement, counts, contextual slots, and the About entry.
  `SFResponsivePublicSidebar` renders each page's one sidebar DOM as the
  desktop rail above 980px and as the shared left drawer at narrow widths.
- `usePublicSidebarDrawer` is the single serializable open/owner authority.
  `SFNavbar` only toggles it: a claimed page owner opens the contextual page
  sidebar, while pages without a desktop left rail use
  `SFPublicMobileNavigation` as the generic fallback.
- Home/search, category index/detail, tag index/detail, topic detail/create/edit,
  profile, settings, notification index/detail, moderation, and system error
  sidebars use this contract. They do not keep separate mobile left-sidebar
  markup or legacy page-specific menu state. Independent right information
  drawers remain page-owned.
- Notification index/detail, account settings, and moderation no longer render
  duplicate drawer triggers inside their mobile content headers. The shared
  navbar remains the single mobile entry point while each page continues to
  own the drawer content selected by the shared state.
- For authenticated users, that navbar entry point renders the current
  `SFAvatar` instead of a panel icon. `usePublicUserMenu` is the shared action
  authority for the desktop avatar dropdown and the account section prepended
  to page-owned mobile right drawers; public profile, profile settings,
  permission-aware admin and moderation entries, email-verification resend, and
  logout therefore cannot drift between viewports. The admin entry is gated on
  `admin.access` — the same permission the API guards every `/admin/*` endpoint
  with, so `super_admin` (via `can()`'s role bypass) plus the `moderator`,
  `operator`, and `tech_admin` templates qualify while `member` does not — and
  resolves its href through `useAdminRoutes` so it follows the operator's
  `adminRoutePrefix` instead of a hardcoded `/control-panel`. `useAdminRoutes().path()`
  localizes through i18n's `localePath`; under the `no_prefix` strategy it never
  emits a `/<locale><prefix>` URL that only a full-page load could 301 away, so
  admin links resolve identically for every locale. The authenticated
  drawer heading is "个人中心" rather than repeating the page-owned rail heading,
  and its account actions are collapsed by default behind the accessible
  identity-row toggle.
  Guests do not receive the page-owned right-rail entry or a synthetic avatar;
  mobile renders one login/registration action instead, reduced to login when
  registration is closed.
- Personalization exposes topbar, sidebar, and footer as editable locations and
  states that mobile follows sidebar. `public.mobile.primary` remains readable
  in V1 documents, snapshots, and imports for compatibility but is not rendered
  or independently edited by the current product.
- The authenticated right-rail greeting is time-of-day aware rather than fixed
  copy: `utils/forum/forumHomeGreeting.ts` buckets the local hour (05 morning /
  11 noon / 13 afternoon / 18 evening / 23 late night, half-open ranges) and
  `useForumHomeGreeting` renders it from `site.timezone` during SSR, then
  switches to the visitor's own timezone after mount so the reader's clock wins.
  The first-frame timestamp is pinned with `useState`, so the hydration text
  matches SSR and the correction never triggers a mismatch. `welcomeUserTitle`
  survives only as the missing-key fallback.

### Mobile comment actions

- `SFComment` uses a compact mobile hierarchy: author identity and the
  `#floor`/overflow cluster share the first row, publication metadata occupies
  the second row, and the bottom action strip retains only reply and permalink.
- The overflow menu reuses the existing permission-filtered action array, so
  edit, delete, report, and extension actions keep their current handlers;
  desktop continues to render the complete inline action strip.
- Browser QA passed on the active default-theme template at `402x905` and
  `1280x720`, including menu expansion, zero horizontal overflow, and clean
  console output.

### Default theme topic readability

- Default-theme topic pages now use a semantic 12/14/14/16px typography scale
  for captions, metadata, controls, and reading text. Topic and comment bodies
  render at 16px; publication metrics, comment metadata, composer guidance,
  and right-rail navigation no longer use 10-11px text or faint color for
  meaningful information.
- Source-theme digest, validation, contract tests, and the focused typography
  regression test pass. The rebuilt immutable artifact is active; Chrome
  desktop QA confirmed the selected theme template and the expected computed
  sizes with no console errors or warnings.
- The mobile discussion heading keeps the desktop left-title/right-latest row
  instead of stacking both controls vertically. At `<=640px` the default theme
  reduces the section lead-in from 48px to 24px and uses a stable 44px heading
  row, while Core fallback preserves the same horizontal geometry.

### Shared tab geometry

- `SFTabs` tracks and items use non-shrinking flex geometry. This keeps the
  42px track intact inside fixed-height, scrollable public content columns;
  narrow layouts scroll the track horizontally instead of clipping tab labels.

### Shared public page headings

- `SFPublicPageHeader` is the single typography owner for Core public list and
  workbench headers. The `page` level provides the compact 22px/700 desktop and
  20px mobile title used by taxonomy, notifications, account settings, and
  moderation; the `section` level preserves the 20px/600 home and search feed
  heading. Both levels consume semantic tokens from `sforum-theme.css`.
- Forum home/search, category index/detail, tag index/detail, notification
  index/detail, account settings, and moderation queue shells use the shared
  header. Their domain styles own only geometry, counters, and actions; they
  must not reintroduce page-local `h1` font sizing or weight.
- Post titles, composers, profiles, authentication, legal documents, and error
  pages have different content semantics and remain outside this shared list-
  page header contract.

### Authentication account flow

- `SFAuthShell` is the shared Host chrome for login, registration, and password
  recovery: its runtime `siteLogoUrl`, `siteName`, and `siteTagline` match the
  public navbar, and its token-based desktop/mobile layout owns the color-mode
  control, brand region, and recovery progress rail. Login, registration,
  request, and confirmation content remain focused Identity components, not
  route-shell implementations.
- Authentication utilities use two quiet inline dropdowns instead of framed
  square controls: language and appearance both expose their current value,
  appearance offers explicit automatic/light/dark choices, and very narrow
  screens retain icon-only triggers without changing the persisted authorities.
- Both protected built-in themes mount one `sf-footer` after each authentication
  body island, so copyright, navigation, and friend links remain operator-owned
  through the existing public footer contract.
- The request view provides field validation, optional ALTCHA, non-enumerating
  completion, masked email, resend cooldown, success Toast, and a help route
  back to the community. It does not expose private `site.admin_email`; a
  direct administrator contact action requires a separate public contact
  contract.
- The confirmation view consumes the runtime password policy, renders a
  segmented readiness meter and requirement list, supports password visibility,
  and has explicit invalid-link and completion states.
- Existing authentication Page Registry IDs, Host islands, route shells, and API
  contracts remain authoritative. The shared-shell follow-up awaits manual
  desktop and mobile theme verification.

### Comment user preview

- `SFComment` treats an eligible avatar or author name as a preview trigger;
  the first click opens `SFCommentUserPreview` instead of navigating directly.
- The non-modal card is anchored to its comment node, remains 340px wide when
  space allows, scrolls away with the comment, and closes on outside pointer
  input or Escape. Escape restores focus to the last trigger.
- The card uses `GET /profiles/:username`, caches successful public profiles
  per Nuxt state, and keeps the existing comment identity plus `/u/:username`
  entry available when profile details cannot load. It does not fabricate
  follow, message, or other unsupported actions.

### Personalization brand asset upload

- Core ships `/brand/sforum-logo.svg` as the public default for both the navbar
  logo and document favicon. Empty runtime Logo/favicon options resolve to this
  asset without materializing the fallback into `web_options`; operator URLs
  remain authoritative.
- The Brand tab exposes compact click-or-drop upload controls for the site
  logo, favicon, and Apple Touch icon. Successful uploads fill both the public
  URL and numeric attachment ID, show a small preview, and remain draft state
  until the operator saves the brand options.
- Manual URL edits clear the prior attachment ID so presentation and attachment
  lifecycle ownership cannot silently diverge. Replace/remove, loading, field
  error, and ten-second success toast states are explicit.
- File selection accepts SVG in addition to JPG, PNG, GIF, and WebP. The API
  returns the safe rasterized PNG URL and attachment ID for the same preview and
  save flow.

### Canonical public search route

- Public keyword search now owns `/search?q=...` and the replaceable Page
  Registry identity `forum.search`; the homepage no longer serves as the
  canonical search result URL.
- The search route reuses the existing forum feed Host implementation while
  retaining an independent route shell, ViewModel contract, Host island,
  region surface, built-in theme templates, SEO metadata, and component catalog
  identity. Empty search stays an inert prompt rather than loading the home
  topic feed.
- Navbar, system-error recovery, and Schema.org `SearchAction` target `/search`.
  Legacy `/?q=...` links redirect to the canonical route with filters and page
  preserved.
- The shared navbar search handles both real Enter key submission and its
  accessible search-icon command. It submits the current trimmed input rather
  than depending on a delayed parent-model update.
- Homepage category chips are bounded and ellipsized inside the fixed 96px
  category track, so long operator-defined category names cannot cover reply
  counts or recent activity at desktop widths.
- Registration terms and privacy links resolve through locale-aware `/terms`
  and `/privacy` routes instead of inert fragment targets.

### Announcement authoring

- Personalization announcements use the shared `SFEditor` basic-field preset:
  Markdown-backed Tiptap editing with undo/redo, emphasis, links, and lists,
  without image, emoji, code, mode-switching, submit, or trusted L2 surfaces.
- The admin create form exposes labeled bilingual content, style, destination,
  order, active window, dismissibility, and initial enabled state with local
  validation. Public and admin previews render only server-derived sanitized
  HTML and retain a plain-text compatibility fallback during rolling updates.
- Rendered desktop/mobile QA is intentionally pending for the operator.

### Configurable public navigation M6

- The Personalization Navigation tab edits one local revisioned document for
  topbar, sidebar, mobile, and footer. It reuses `SFIconPicker`, uses FormKit
  drag plus accessible ordering buttons, and includes source/state badges,
  typed safe links, transfer controls, dirty-route protection, and one batch
  save against SiteChrome.
- The navigation editor separates ordinary location editing from recovery and
  transfer as two local fixed-tab cards. Operator link creation and editing use
  one shared modal, leaving the location list focused on ordering and actions.
- The same document owner now exposes confirmed one/all-location defaults,
  actor-attributed snapshot history/detail/restore, backend-document JSON
  export, and fenced merge/replace import. Structured changes and inert
  extension warnings remain next to the workflow; dirty editor state disables
  recovery actions.
- Public topbar and mobile now consume one canonical actor/locale-sensitive
  `/site/navigation` payload. `SFNavbar` no longer merges `/site/nav-items`,
  extension items, or hardcoded fallback links; ordinary navigation fails
  closed while Host utility controls remain available.
- SiteChrome now carries configured topbar placement order through Navigation
  Registry composition. Registry normalization no longer falls back to source-
  key ordering for Core/operator items; the Host-owned search control remains
  independent and duplicate `/search` links stay suppressed by design.
- Focused navigation components own safe internal/external rendering and the
  independent mobile drawer. Topbar shows four items at most and projects the
  remainder into the existing Nuxt UI overflow menu. Explicit imports avoid
  Nuxt hydration ambiguity. Language-menu state now belongs to
  `navigation/useNavbarLanguageMenu.ts`, reducing the architecture ratchet for
  `SFNavbar.vue` to 851 lines.
- Authenticated selected-theme Browser QA passed at desktop and `390x844` with
  `data-provider="sforum.default-theme"`, `data-template="1"`, one visible
  navbar/footer, no fallback notice, no overflow, and no console errors.
- `SFHomeNavigation` now renders ordinary sidebar links and the bounded
  `core.dynamic.categories` block in resolver order. Forum remains the sole
  owner of taxonomy names, visibility, icons, counts, and ordering; filter and
  route modes, compose authorization, shell consumers, and safe external links
  are preserved.
- `SFFooter` consumes the canonical footer location while copyright and friend
  links keep their existing owners. One actor/locale-sensitive request supplies
  all four public locations.
- Mobile homepage QA at `390x844` proved the category selector is visible,
  constrained to the content width, and navigates `general` to `/c/general`
  without overflow. M7 owns the final lifecycle and release matrix.

### Architecture boundary debt repayment

Archived plan:
`../plans/archive/2026-07/2026-07-28-architecture-boundary-debt-repayment.md`
(**completed M0-M12**). Handoff:
`../sessions/2026-07-28-architecture-debt-m12-handoff.md`.

- Fixed Core settings tabs now use the shared `SFAdminFixedTabNav`, query-synced
  dynamic components, and `KeepAlive`.
- Core admin settings pages share the Site Settings geometry contract: compact
  registry-icon title, standard toolbar, fixed-tab navigation, and one active
  `min-w-0` panel. Mail and Notification settings have a focused regression
  test wired into the repository gate, while desktop plus `390x844` Browser QA
  remains mandatory for visual completion.
- Site, forum, SEO, personalization, attachments, and mail use independent tab
  components under `components/admin/settings/<area>/tabs/`.
- Route shells are below 150 lines for the six migrated areas except where
  recorded otherwise; all six inline-tab architecture baseline entries are
  removed.
- M7 root-directory placement completed with explicit imports and domain moves.
- M7 typecheck, production build, architecture and V3 catalog validation, and
  the focused tests wired into the repo gate passed.
- Frontend milestones M1-M7 and backend milestones M8-M12 are complete.

### Built-in GitHub social login (public auth + account security UI)

Plan: `../plans/2026-07-27-github-social-login-builtin-plugin.md`
(**active; final review failed; T8A done**, T8B-T8D remain). Handoff:
`../sessions/2026-07-27-github-social-login-final-review-handoff.md`.

- Public login/register Host islands consume `GET /auth/providers` via
  `useAuthProviders` (SSR-safe). Executable visibility is Host
  `activatedOperations` only — never hard-coded per vendor.
- Vendor presentation (`label`, `icon`) is plugin Identity declaration data
  injected through the Host catalog. Core i18n holds only generic shell
  templates (`Continue with {name}`) and Host stable `ext_auth` reasons.
- Callback feedback uses minimized `ext_auth` query reasons; opaque external
  registration continues at fixed `/register?ticket=…` without a password field.
- Login methods (`/settings/login-methods`) owns external identities through
  `SFLinkedAccountsSection`: redacted `GET /auth/external-identities`,
  Host-gated link via `linkProviders`, unlink + last-method/recent-auth UX,
  inert status, and no Core GitHub brand strings.
- Local password (`/settings/password`) owns password setup/change through the
  existing recent-auth-gated `POST /auth/password` Host API, with policy
  feedback and confirmation validation on an independent settings page.
- Operator docs: `docs/zh-CN/usage/github-login.md`,
  `docs/en-US/usage/github-login.md`.
- Admin Login Methods still contains GitHub ID-based title/icon branches and
  lacks a generic discovered-before-enable directory. T8B must consume Host
  presentation metadata and add real rendered interaction coverage.

### Tri-state color mode reliability

Plan: `../plans/archive/2026-07/2026-07-27-tristate-color-mode-reliability.md`
(**completed M0-M5**).
Decision: `../decisions/2026-07-27-tristate-color-mode-preference.md`.
Final report: `../reports/2026-07-27-tristate-color-mode-reliability-final.md`.

- The approved personal preference model is `system | light | dark`, presented
  as Automatic (recommended), Light, and Dark.
- Stored preference and resolved `light | dark` are separate; themes and
  extensions continue consuming only the resolved appearance.
- Browser diagnosis proved that `localhost:3000` and `127.0.0.1:3000` keep
  independent `localStorage` preferences. Same-origin hard refresh and client
  navigation retained the selected mode.
- V1 keeps the existing Nuxt Color Mode storage key and browser-local
  persistence. Cookie/server persistence is closed until anonymous SWR/SSR
  cache isolation is designed.
- `useColorModePreference` is the shared authority for normalized stored
  preference, resolved mode, option metadata, and writes. Public/admin no longer
  keep DOM observers or local resolution branches; extension bridges consume
  only readonly resolved `light | dark`.
- Keep Nuxt Color Mode 4.0.1 for storage, OS listening, and document classes.
  Nuxt UI's button/switch are binary; its select proves native three-state
  support, while SForum's explicit recommended-description menu will use the
  existing `UDropdownMenu` plus one domain composable.
- Public Host and admin surfaces use an explicit Nuxt UI checkbox menu ordered
  Automatic/Light/Dark with monitor/sun/moon icons, checked state, localized
  accessible names, and Automatic's recommended system-following description.
- Development-only H3 middleware uses fixed, validated loopback `APP_URL` as
  redirect authority. Only loopback-alias HTML GET/HEAD documents redirect;
  API, Nuxt/HMR, `_sforum`, health, unsafe, malformed, and canonical requests do
  not.
- Authenticated browser QA selected all three modes. Dark survived hard refresh
  and client navigation; Automatic displayed as the stored preference and
  resolved to the current system-light environment. `localhost` redirected to
  `127.0.0.1` with path/query intact and no relevant console errors.
- Anonymous shared HTML remains mode-neutral, retains the identical early
  `nuxt-color-mode` local-storage bootstrap, and sets no preference cookie.
  Errors remain `no-store`; shared pages retain their existing SWR headers.
- Focused aggregate verification passed 45 tests / 300 expectations; typecheck,
  production build, OpenAPI references, architecture boundary validation, Go
  tests, and diff whitespace checks passed. The final report records unrelated
  full-web failures, missing compat-farm database env, and the lack of rendered
  OS-dark/live emulation in the available browser control surface.

### Current HEAD regression remediation

Plan: `../plans/2026-07-22-current-head-regression-remediation.md`.

- Status: closed 2026-07-23. Search/frontend/Page Registry and extension-gate
  regressions from the current-HEAD book are remediated.
- `forum.topic.reply` has production Page ViewModel/controller resolve coverage,
  not only catalog/template assertions.
- Query-bearing `forum.home` and replaceable `system.not_found` remain selected
  theme surfaces; Core remains a bounded emergency fallback.
- Shared Page Registry and error-flow files are released to the focused
  selected-theme public 404 task.

### Theme-consistent public resource 404

Plan: `../plans/2026-07-22-theme-consistent-public-resource-404.md` (**completed
2026-07-23**).

- Missing/hidden/deleted public resources and unknown routes resolve
  `system.not_found` while retaining the selected theme's navbar, responsive
  sidebar/body structure, and footer.
- Semantic 404 is attempted once and preserves its reason. Hard SSR returns
  404 with `no-store` and `noindex,nofollow`, without success canonical or
  structured data.
- `SFSystemThemeTemplate` renders reviewed L0/L1 error AST through statically
  compiled Host islands so production SSR and hydration agree. Core is a
  complete local emergency page only for actual theme/runtime/API failure.
- The final 2026-07-23 gate passed all Bun tests (`546/546`, `3303` assertions),
  Nuxt typecheck and production build, full Go tests, OpenAPI validation,
  repository validators, HTTP/cache probes, and the development/production
  desktop/mobile browser matrix.
- A 2026-07-23 follow-up removed the remaining hard-refresh half-state: the
  error boundary now fetches L1/L0/options/session through the explicit SSR
  internal API path, stages exact-artifact L0 and L1 candidates, and commits
  them together only when extension/version/digest/node revision all agree.
  Any request, identity, revision, or renderer-source failure commits a complete
  Core emergency payload and clears theme identity plus CSS links. Hydration
  reuses that serialized decision without a second startup fetch.
- Error-boundary Host islands use direct component imports. General Page
  Registry islands use explicit `defineAsyncComponent(() => import(...))`
  wrappers rather than runtime `resolveComponent('Lazy...')` registry lookups,
  so HMR cannot hand `undefined` to vnode creation.
- Nuxt payload extraction is disabled. Request-local `no-store` policy and
  Nuxt 4.4 extracted payload routing can otherwise turn `_payload.json` into an
  HTML response and cancel client navigation. Inline SSR payload keeps
  development and production behavior aligned without a second cache surface.
- Default-theme 404 always keeps its footer: current source artifacts scope the
  homepage footer suppression away from `.sf-page--not-found`, and Host CSS
  provides the same compatibility for installed older artifacts. In
  development, only the Nuxt overlay entry attached to an ordinary body-marked
  404 is hidden; after successful home/back/retry recovery its resolved entry
  is removed. Real runtime and 5xx diagnostics remain visible while active.
- Handoff:
  `../sessions/2026-07-22-theme-consistent-public-resource-404-plan-handoff.md`.

### Theme-defined system error pages

Plan: `../plans/2026-07-22-theme-defined-system-error-pages.md`
(**completed**). Completion handoff:
`../sessions/2026-07-23-theme-defined-system-error-pages-completion-handoff.md`.

- `error.vue` maps 403, 404, 429, and 500/502/503/504 to virtual Page Registry
  surfaces (`system.forbidden`, `system.not_found`, `system.rate_limited`,
  `system.server_error`). Other statuses stay on the generic Host fallback;
  browser 401 remains login/redirect behavior.
- `useSystemErrorPageResolve` performs one short attempt
  (800 ms dev / 1000 ms prod) and `useSystemErrorPagePresentation` commits L0
  skin links plus L1 render output only after exact extension/version/package
  digest/node revision identity matches. Transport/options/auth/artifact
  failure clears active theme identity and renders the Core emergency page.
- Reviewed Host islands own safe semantics and recovery behavior: details,
  actions, recovery search, sidebar, and rail. Theme L1 owns layout/chrome;
  plugins and public L2 remain closed on `system.*`.
- The default built-in theme provides themed forbidden,
  not-found, rate-limited, and server-error templates/styles. A final default
  theme mobile CSS fix collapses the hidden-sidebar system-error layout to one
  full-width column below 960 px.
- A 2026-07-30 follow-up moved effective appearance resolution into one shared
  composable used by `app.vue` and `error.vue`. Authenticated 403/404/429/5xx
  hard refreshes now honor user accent and daytime-background overrides, and
  both public-resource and system-error Core fallbacks restore public options
  plus session state before rendering their final appearance.
- Focused browser QA covered real unknown-route 404, selected-theme markers,
  status preservation, recovery search, dark mode, mobile, and English locale.
  Current QA data has no natural full-page 429/5xx producer, so those families
  are covered by mapping/resolver/fallback tests rather than committed
  test-only routes.

## Runtime Themes And Page Registry

- Nuxt remains the SSR host. Themes are buildless runtime packages; they do not
  extend Nuxt or replace the deployment artifact.
- Stable Page Registry IDs select page providers and ViewModel contracts.
- L0 supplies CSS/tokens/fonts/images/locales. L1 supplies reviewed
  server-rendered templates. Trusted author-prebuilt L2 is digest-authorized and
  contract-bound, and it is **always enabled**: `SFORUM_V3_PUBLIC_L2` was removed
  (2026-10-23), so only Safe Mode and exact-artifact trust can close it. The
  trust gate is also the mechanism that validates the artifact, so
  `SFORUM_V3_TRUST_CHALLENGES=false` closes public L2 instance-wide; that is the
  non-production default in `.env.example` and `compose.yaml`.
- Public body content, metadata, canonical links, structured data, links, and
  pagination must be complete in initial HTML without L2 or hydration.
- Dynamic add routes enter through `pages/[...sfRegistryPage].vue` and
  `/pages/resolve-path`. Plugin page loader data is SSR-only.
- `SFPageOutlet` and `SFHostPublicChrome` are Host emergency surfaces. Component
  failure preserves L1/SSR content and may quarantine only the failing L2.
- `SFHostPublicChrome` preserves the selected theme's public shell geometry
  during a Core page fallback. Its `fullwidth-3col` contract uses the same
  topbar grid, 24 px viewport inset, 230 px left rail, 270 px right rail,
  column padding, and per-column sticky scroll ownership as the default theme:
  left, center, and right columns each own `overflow-y: auto` at viewport height
  minus the topbar, with `overscroll-behavior: contain`, so scrolling one rail
  never moves another. Pages that keep their footer inside the center column
  leave the document itself non-scrollable.
  Runtime fallback may change ownership/content, not page geometry.
- Page-level 3col CSS must not hard-code horizontal insets. `sforum-theme.css`
  owns `--sf-public-edge-inset` for home/topic/notifications/settings/moderation
  in the Core fallback path, so a fallback page cannot drift off the topbar
  tracks. Left/right column dividers are the rails' own 1 px border; the legacy
  ±12 px pseudo-element line shipped by an installed theme artifact is disabled
  in `sforum-topic.css` until that theme package is rebuilt.
- Public chrome islands such as `SFNavbar` and `SFFooter` must stay statically
  reachable from runtime theme templates so critical scoped CSS is present
  before browser back/forward restores the page.
- Activation prewarms and atomically swaps exact theme runtime state. Active
  skin links are emitted by root `useHead` during SSR.

Architecture sources:

- `../decisions/2026-07-13-runtime-page-registry-themes.md`
- `../decisions/2026-07-13-trusted-plugin-theme-platform-v3.md`
- `../plans/2026-07-13-trusted-plugin-theme-platform-v3.md`
- `../../docs/extensions/page-catalog.md`

## Resolve And Cache Invariants

- Page resolve state is keyed by page ID, path, query, locale, and actor. Never
  reuse rendered output across those boundaries.
- Transient `/pages/resolve` failure may use one bounded retry. Semantic 404
  must not retry.
- Any fail-closed/runtime fallback response disables Nitro cache/SWR and sets
  `Cache-Control: no-store` so anonymous cache cannot share Core emergency
  HTML.
- Resolve payloads preserve provider/artifact/revision identity and distinguish
  authoritative Core ownership from transient runtime failure.
- Last-good theme skin/settings cache is allowed only for the exact
  `extensionId + packageDigest + nodeRevision`, with a short TTL and validated
  same-origin asset URLs. A known theme change cannot reuse prior-theme state.
- Theme asset URLs place `packageDigest` in the path rather than a query so CSS
  relative fonts/images retain the same immutable identity.
- Anonymous public pages may be shared/SWR cached only when their route contract
  permits stale HTML. `/categories`, `/tags`, `/u/**`, `/c/**`, `/tags/**`, and
  `/t/**` all disable Nitro whole-page caching while retaining SSR because
  their first render depends on actor-sensitive navigation, permissions, or
  live/query-specific data. This must be a static route rule: request-time
  middleware cannot bypass Nitro's startup-wrapped cached handler.
- Requests carrying `sforum_session` restore auth during SSR and their HTML or
  Nuxt payload responses use `Cache-Control: private, no-store`. Do not vary a
  shared HTML cache by Cookie and never cache authenticated HTML.
- `site.public_surface_revision` remains the Host revision fact for extension
  contribution changes, but no longer keys topic HTML because `/t/**` is not
  whole-page cached.

## Public Forum UI

- The default theme uses a flat, responsive three-column shell: navigation,
  primary content, and contextual right rail. It collapses the right rail
  before the left navigation.
- Native scrollbars are themed globally from `main.css` with neutral
  `--sf-scrollbar-*` tokens derived from public surface, text-muted, and border
  colors. Keep them quiet rather than accent-colored; page-local scroll
  containers should use the global contract unless they deliberately use
  `.no-scrollbar`.
- The default flat comment stream does not draw bottom borders between rows.
  Those borders resembled scrollbar tracks and could be partially repainted by
  the browser while moving the pointer across comment content. Spacing remains
  the row boundary; tree-mode branch separators are unchanged.
- `/categories` uses the default-theme grouped directory surface: real
  `ForumCategoryGroup`/`ForumCategory` DTOs, URL-backed group focus via
  `?group=<group.id>`, page-local category filtering, group-local sorting, and
  a derived right rail. Group focus lives in the center toolbar instead of
  repeating every group in the left rail. The left rail and mobile navigation
  drawer expose the same all/hot/week/A-Z category filters as the center
  toolbar; hot uses the visible scope's real topic-count upper quartile and
  week means categories created in the last seven days. The right rail is
  limited to the directory overview and the five most active categories.
  Topbar `SFSearch` remains global forum search.
- The notifications page follows the same default-theme shell and shared
  mobile drawer state as the forum homepage/navbar. Type/category/unread filters
  are server-authoritative; the global unread total remains API-owned. One
  shared EventSource coalesces revision-only refresh signals and falls back to
  REST/manual refresh.
  Desktop-hidden right rails must explicitly opt back into display inside a
  mobile drawer.
- `forum.topic.create` now uses the same default-theme three-column native flow:
  left rail reuses `SFHomeNavigation`, the center column keeps the real
  `SFTopicComposerPage` form and `SFEditor`, and the right rail derives publish
  summary/settings/checks from live form state. Desktop uses a fixed bottom
  publish dock with extra content padding; mobile collapses to a single column
  and keeps category, tags, draft, errors, and publish controls available.
- `SFHomeNavigation` owns shared public left-rail links and the bounded dynamic
  category block. Homepage, topic, and taxonomy pages reuse it inside desktop
  sidebars and mobile left drawers.
- The dynamic category block reads its placement-level `maxItems` value from
  canonical public navigation. `0` keeps the historical show-all behavior;
  `1..100` limits both the desktop category list and its mobile selector, while
  retaining the currently selected category inside that bound. When the limit
  hides categories, both render modes expose a localized link to `/categories`.
- Default-theme public pages must still render the global `SFFooter` from L1
  chrome. Configurable footer navigation starts empty; copyright and friend
  links remain independently owned, and operator-created footer links render
  alongside them when configured.
- On desktop, the default-theme full-width three-column shell keeps the navbar
  sticky while the viewport-height center column starts in normal flow and
  retains its independent content scroll. Do not apply the navbar height again
  as a sticky `top` offset: the theme chrome already places the body below the
  navbar. Left navigation and the right rail use natural height without inline
  vertical scrolling; when either grows beyond the viewport, the document
  scrolls to expose it. Grid rail boxes stretch to the row height so their
  divider lines remain continuous without constraining the rail content. Mobile
  keeps ordinary document scrolling plus drawer scrolling to avoid scroll
  traps.
- Homepage, topic detail, category detail, and notifications place the public
  footer inside the center content column through `SFContentColumnFooter`;
  full-width L1 and Host fallback footers are hidden when the body island owns a
  content-column footer, so it scrolls with content instead of occupying global
  chrome. Footer-owning center columns use a shared flex contract so short
  content pushes the footer to the viewport bottom while long content keeps it
  after the complete content stream. On desktop its top border reaches the
  center scrollport edges while adjacent rail dividers sit on those same edges,
  keeping the three lines joined without horizontal overflow or moving footer
  text.
- `/tags` is the `forum.tag.index` Page Registry body island. It renders the
  default-theme three-column heat overview from real `listTags` and
  `listCategoryGroups` API data, with all/hot/week/A-Z filters, localized empty
  states, and the shared navbar mobile drawers for left navigation and tag
  context.
- The `/moderation` Host body island mirrors that flat shell with scoped
  `sforum-moderation` CSS instead of changing default-theme shared CSS; its
  source/filter/page/review state is query-backed for SSR/client recovery, and
  review actions move into mobile drawers as soon as the right rail collapses.
- Homepage/topic/taxonomy data is real API state. Do not render fabricated
  likes, bookmarks, unread state, participant stacks, or ranking.
- `/u/{username}` is also part of the default-theme three-column public shell:
  left rail reuses `SFHomeNavigation`, center renders member summary plus
  locale-aware daily public activity groups, and right rail renders real public
  profile details, public stats, and recent topics. The same rail is reused in
  the mobile information drawer. Keep social/portfolio/gamified actions out
  until the API owns those contracts. Public-reply activity links use
  `<NuxtLink :to>` (not `<a href>`) so in-app navigation preserves the
  `#comment-{id}` hash and the topic page can resolve the page client-side;
  same-page comment anchors elsewhere stay native `<a href="#comment-{id}">`.
- Homepage and eligible lists SSR page 1 and continue with keyset infinite
  scroll. URL-backed filters and stale-response guards remain authoritative.
- Topic detail ships complete topic/comments/navigation in initial SSR HTML;
  client navigation may let secondary data fill through explicit pending
  states.
- Reply editor mounts after explicit interaction; advanced reply uses
  `forum.topic.reply` and draft handoff.
- Topic editing is a standalone page `forum.topic.edit` at
  `/topics/:topicId/edit` (id-based because editing may change the slug);
  `SFTopicShowPage` only navigates there — the old `?edit=1` inline mode is
  removed. Its Host editor island uses the same responsive three-column
  composer shell and taxonomy/editor controls as topic creation. Both builtin
  themes must declare the same `fullwidth-3col` shell for
  `forum.topic.create` and `forum.topic.edit`, and the Host fallback must keep
  the same rails/topbar while an immutable theme update is staged but not yet
  active. Editing does not persist a local draft. Self edits require no reason;
  cross-author edits
  require the API's bounded audit reason. Unsaved changes are guarded, route
  reuse clears all topic-scoped editor state, stored `editor-document` JSON is
  restored via `forumEditorInitialContent` → `SFEditor.initialContent` (never
  seeded into Markdown `v-model` as `rawContent`), and save returns to
  `forumTopicPath`; canonical redirect fixes slug. Comment inline edit and
  admin topic/comment editors share the same load/save contract.
- Topic create draft persistence is currently local `sessionStorage` under the
  composer page because there is no create-topic draft API. Publishing still
  uses the canonical `POST /topics` flow and keeps API authorization
  authoritative.
- `SFAvatar` is the required first-party avatar renderer. Pass shared
  `AvatarView`; do not fork initials/URL handling.
- Comment tree presentation has bounded indentation; mobile clears recursive
  inset. Visible floor labels are list positions while anchors remain stable
  `#comment-<id>` targets. Cross-page anchors resolve with zero flash: when
  the URL carries `#comment-<id>` and no explicit page, `SFTopicShowPage`
  resolves the page server-side (`resolveCommentPage` API), SSR-renders that
  page, and lets the browser scroll natively to the target; a client-side
  watch also scrolls on client navigations. Comment pagination uses a path
  segment `/page/N` (old `?page=N` still resolves and is normalized
  client-side after hydration).
- `.sf-prose` is the shared sanitized rich-content surface. Code blocks use the
  idempotent client enhancement described above plus a no-op SSR directive
  registration; server-rendered source remains readable before hydration.
- Runtime appearance owns accent and dark-mode tokens. The default theme must
  not pin a product-specific accent.
- `appearance.light_background` exposes 12 curated light palettes through
  Personalization. Root and error documents project the selected key as
  `data-sforum-light-background`; `html:not(.dark)` maps it to public tokens and
  the admin shell's semantic/Nuxt UI surface tokens. A scoped compatibility
  bridge covers legacy Core admin white/slate utilities without touching any
  `.dark` rule. Preset cards use visible `button[role=radio]` controls rather
  than off-screen native radio inputs so focus cannot resize or scroll the fixed
  admin shell. The recommended `pure_white` preset preserves the prior visual
  default. While the Appearance tab is active, preset and custom-color edits
  feed an admin-only in-memory preview consumed by the root head resolver. The
  preview adds reduced-motion-aware color transitions, is cleared on
  deactivation/unmount, and never replaces the persisted public option until
  save succeeds.
- `/settings/appearance` is the dedicated account appearance surface and Page
  Registry page `forum.settings.appearance`. It uses `SFSettingsShell`, has its
  own account-sidebar item, and offers explicit site-inheritance or personal
  override modes. Selection updates the root appearance tokens immediately;
  reload discards unsaved edits, while save updates `CurrentUser.appearance`.
  Saving site inheritance clears the user row instead of copying current site
  values, so later operator changes continue to flow through. `app.vue` and
  theme-defined `error.vue` documents share this same resolver rather than
  maintaining separate precedence branches.
- Pinned rows in the homepage/category/tag topic list are presentation-only
  state: `SFHomeTopicRow` adds `sf-home-topic-row--pinned` (plus
  `sf-home-topic-row__pin-badge` on the badge) and
  `assets/css/sforum-home-pinned.css` owns the tint, left accent bar, and hover
  values. That stylesheet must load after `sforum-home.css` because the default
  theme skin forces `.sf-home-topic-row` backgrounds to `transparent`; the
  pinned rules use the same specificity but later order so Core fallback and
  theme-skin rendering agree. Colors come from `--sf-accent` /
  `--sf-accent-dark` with `--sf-public-row-pinned-*` override hooks instead of
  hard-coded grays, and the 1000-line budget keeps `sforum-home.css` free of
  new pinned-only rules.

## Editor And Content UI

- `SFEditor` uses Tiptap while preserving Markdown `v-model` integration. It
  emits HTML, Markdown, native JSON, plain text, counts, and empty state.
- 2026-10-19 tables: `app/utils/editor/table.ts` registers
  `@tiptap/extension-table@3.31.3` (`Table`/`TableRow`/`TableHeader`/`TableCell`)
  in the `full` preset only. `SForumTable`/`SForumTableCell`/`SForumTableHeader`
  replace the vendor defaults so serialized HTML carries `align` instead of
  inline `style` (both DOMPurify and bluemonday strip `style`), emits no
  `colgroup`, and drops `colwidth` (`resizable: false`; column widths are not
  persisted). `insertSForumTable` refuses nested tables and appends a trailing
  paragraph when the table becomes the last block. Toolbar exposes a 6×6
  `SFEditorTablePicker` (hover preview, keyboard arrows/Home/End) and
  `SFEditorTableMenu` is a BubbleMenu mounted on `document.body` because the
  editor shell is `overflow: hidden`; it is hidden in preview mode and offers
  row/column insert+delete, header-row toggle, per-cell alignment, delete table.
  Cell merge/split is deliberately absent (GFM cannot express it and comment
  drafts round-trip through Markdown). Table typography lives in
  `sforum-content-semantics.css` (editor wraps, preview mirrors `.sf-prose`
  nowrap + horizontal scroll); `.sf-prose` table rules stay in `main.css`.
  Copy lives under `composer.table.*` in both locales.
- Built-in Shortcodes M2 adds the Host-owned `sforumShortcodeRef` and
  `sforumShortcodeBlock` Tiptap nodes through
  `app/utils/editor/shortcodes.ts`. Initial Markdown and plain-text paste use
  Tiptap Markdown tokenizers for the frozen standalone grammar; unknown,
  malformed, escaped, inline, code/raw-HTML, scope-invalid, and over-budget
  syntax stays literal. `SFEditor` derives `topic`/`comment` scope from its
  existing `imageSurface` contract. Protected descendants remain available in
  authorized native JSON/Markdown but are never included in client fallback
  HTML.
- Built-in Shortcodes M9 reuses the existing full `SFEditorToolbar` and trusted
  Editor Registry L2 loader. The exact `sforum-shortcodes 1.2.0` module requests
  the Host `openReferenceMenu` command; `SFEditor` retains the editor instance,
  dialog, API calls, insertion/update/delete, focus, Toast, and preview
  authority. One compact project-icon action opens searchable recognizable
  user/topic/comment/category choices, while friend-links inserts directly.
  Stable accessible NodeViews show inline loading/unavailable states. Their
  selected state is expressed by the node outline rather than an embedded
  action cluster: reference nodes reopen the existing edit flow on double-click
  or Enter, while protected bodies remain directly editable. User references
  render `SFAvatar`; category references reuse the category's validated icon and
  six-digit hex color. Escape returns focus to the editor; dialog
  errors remain inline, non-error feedback auto-dismisses, and zh-CN/en-US are
  complete. Canonical paste/input and the `topic-tag` alias normalize into the
  frozen nodes and Markdown export, with no source JSON/Markdown mode or syntax
  teaching UI. Plugin disable, L2 quarantine/load failure, and Safe Mode leave
  the Core editor usable with safe fallback. Real topic 145/comment 368 and
  exact 390x844 Browser QA cover publish, edit, reload, keyboard focus, stable
  dialog geometry, and zero horizontal overflow.
  The M9 protected-block workflow extends this same menu for `login`, `reply`,
  and comment-only `only-author`: selected content can be wrapped, an existing
  block can change type, be deleted or unwrapped, and its accepted body remains
  editable through the native editor document. The UI is presentation-only;
  Host validation rejects empty bodies and topic `only-author` placement.
- 2026-10-10 fix: protected-block insert/change runs on a candidate transaction
  built by `buildSForumProtectedTransaction` (`app/utils/editor/shortcodes.ts`)
  and is only dispatched when the Host document inspection passes — Tiptap
  unconditionally dispatches non-chained `commands.command` transactions, so a
  false return cannot roll a mutation back. `inspectSForumShortcodeDocument`
  now reports grouped failure reasons (empty/placement/declaration/commentOnly/
  count/depth) that map to actionable i18n messages; placement distinguishes new
  violations from pre-existing misplaced nodes (e.g. a reference inserted
  inside a list item poisoning the document). Same-day stage two (user
  approved): the wrap target is fixed to the top-level blocks spanned by the
  selection (a caret inside a list/quote wraps that whole structure, so new
  placement violations can no longer occur), and fresh reference inserts are
  preflighted by `sforumCanInsertReference` (`$from`/`$to` container must be
  `doc` or a protected block; retyping an existing node is exempt), closing the
  list-item poisoning hole. Stage three (user approved) splits editing validity
  from publish validity: inserting on an empty line now yields an empty
  protected scaffold (`allowEmptyProtectedBody` inspection for inserts, so an
  empty block neither raises an insert-time message nor blocks later shortcode
  operations), the `SFShortcodeProtectedNodeView` renders an in-block
  placeholder from the same body rule as the Host, and the publish gate
  (`sforumDocumentPublishIssue` + `sforumPublishFailureKey`) reports a
  field-level message on every submit path (comment reply, comment edit, topic
  composer, topic edit page, `SFTopicEditor`) while the Host
  `ErrInvalidShortcode` stays authoritative. Empty scaffolds never enter
  Markdown (`renderProtectedMarkdown` returns empty for an empty body) because
  the frozen grammar keeps an empty protected body literal and a draft
  round-trip would otherwise degrade it into visible `[reply]` text. Regression
  coverage lives in
  `tests/framework/editorShortcodes.test.ts` (builder suite asserting the
  document stays byte-identical on failure, top-level wrap shapes, the
  reference preflight matrix, empty-scaffold insert/emission and the publish
  gate) and `editorCanvas.test.ts` (dispatch wiring).
  Decision:
  `decisions/2026-10-10-editor-validation-candidate-transaction.md`.
- M10A adds the legacy conversion/report corpus
  (`contracts/fixtures/shortcode-legacy-conversion-v1.json`) consumed by both
  the Go and TypeScript conformance suites with identical classification,
  canonical output, and fallback codes; client inspection of structured nodes
  is verified only where the Host and client agree (`authority: both`), while
  Host-only rejections are asserted by the Go conformance test. Bilingual
  user/operator documentation lives in `docs/{zh-CN,en-US}/usage/shortcodes.md`.
- M10B fixes TypeScript protected Markdown list-to-heading canonical newlines
  to match Goldmark/Go byte-for-byte. Shared fixture and Web conformance tests
  pass; final Browser completion waits for staged immutable artifact promotion.
- `SFEditor` compares raw incoming Markdown with its last emitted value before
  shortcode import preparation. This prevents Tiptap's normal `\[` Markdown
  escape during direct typing from being re-imported as visible `&#91;` text.
- M6 selected-theme Browser QA for the real evidence topic and comment resolves
  `data-provider="sforum.default-theme"` with `data-template="1"`. The active
  exact plugin renders safe `user`, `category`, `friend-links`, `topic`, and
  `comment` output directly in raw SSR HTML; disable, Safe Mode, self/cycle, and
  invisible-target paths use Host fallback. Public JSON, raw HTML, Nuxt
  payload, DOM, plain text, and trace scans expose no raw content, content hash,
  source shortcode syntax, actor/session/permission metadata, internal errors,
  or plugin path. Desktop 1280x720 and mobile 390x844 have no horizontal
  overflow.
- Editor write, client preview, and formal `.sf-prose` output share
  `sforum-content-semantics.css` for paragraph, heading, list, quote, code,
  link, and horizontal-rule presentation. This file explicitly restores list
  markers removed by Tailwind Preflight; editor container geometry and
  topic/theme-specific presentation remain in their owning stylesheets.
- Client list regression tests exercise Tiptap's rendered write DOM and preview
  HTML separately from the DOMPurify allowlist. Do not use happy-dom's
  DOMPurify serialization as browser-parity evidence: it can remove an
  outermost list wrapper that real browsers preserve.
- The full editor now uses the selected Forum Canvas geometry: a focused
  `SFEditorToolbar`, 48px white toolbar, 34px icon commands, quiet root focus,
  generous document padding, and horizontally scrollable mobile tools. The
  compact and `basic-field` presets keep their denser 14px content padding.
- Topic create and edit share a page-local responsive canvas contract. Its
  desktop `360px` and mobile `330px` minimums apply to both the editor body and
  its content/preview/loading surfaces. Their page-specific main-column rule
  also outranks the later shared home padding and preserves `112px` desktop /
  `118px` mobile space below the form, so the editor status row can scroll
  completely above the fixed publish dock.
- The full toolbar exposes paragraph/H2/H3, marks, lists, quote, code, link,
  image, and write/preview modes. Markdown source editing and native JSON
  inspection are no longer exposed; the editor still emits both formats for
  the existing persistence contract. The Unicode emoji picker was removed,
  while the historical `sforumEmoji` node remains admitted so old editor
  documents still load. Trusted digest-verified L2 extensions keep the existing
  fail-closed loading path.
- The image command opens an upload modal instead of the native picker. Its
  drop zone accepts clicks and dragged files; pasted raster images upload at the
  active selection. Toolbar, modal, paste, and exact-position drag paths reuse
  the attachment upload policy and place a mapped ProseMirror decoration before
  starting async work, so later edits do not move the insertion point. Pending
  uploads block submit; success and persistent failure feedback follow the
  shared Toast contract.
- Uploaded native image nodes retain `attachmentId` and
  `attachmentPublicId` as non-rendered attributes. Editor payloads deduplicate
  those IDs into `content.attachmentIds`, while rendered `src` values use the
  stable `/media/attachments/{publicId}` route instead of exposing `/api/v1`.
- The production toolbar deliberately has no sticker command yet. It must be
  added with the Host-owned catalog and immutable `sforumSticker` node rather
  than inserting a generic image or bundling a client-only pack.
- **Edit load path:** `sourceFormat=editor-document` stores Tiptap native JSON in
  canonical `rawContent`. Public topic/comment DTOs never carry this source.
  Topic and comment edit entrypoints must first load the authorized
  `/topics/:topicID/edit-source` or `/comments/:commentID/edit-source` response,
  then pass `forumEditorInitialContent(source)` as `initialContent` (object →
  JSON doc; string → Markdown). Never assign `rawContent` to the Markdown
  `v-model` or recover it from a public SSR/Nuxt payload.
- **Edit save path:** prefer `forumContentFromEditorPayload` so native JSON is
  re-submitted as `editor-document` (same as create/reply).
- Client HTML is preview-only; the API regenerates and sanitizes stored output.
- Topic/comment writes send the backend `content` contract, including optional
  attachment IDs.
- Tag input accepts Unicode letters/numbers plus hyphens, matching backend tag
  validation.
- 2026-10-23 link/code-block paste fix: `app/utils/editor/link.ts` exports
  `SForumLink` with `inclusive: false` (Tiptap v3 otherwise ties link
  inclusivity to `autolink`, so typing after a pasted/inserted link kept
  extending it). `SFEditor#setLink` no longer calls `setMark` on an empty
  selection; it inserts the normalized URL as link text via `insertSForumLink`
  and leaves the cursor outside the mark. Code-block paste is now a Host
  contract in `app/utils/editor/clipboard.ts`: inside a code block every
  multi-line paste is inserted as literal text (`text/plain` preferred,
  HTML→text fallback), and a clipboard containing only `<pre>` code containers
  is collapsed into one `<pre><code>` (language from `language-*`/`data-language`,
  text escaped). `app/utils/editor/codeBlock.ts` extends `CodeBlock` so
  `toggleCodeBlock` merges a top-level multi-textblock selection into one code
  block instead of one block per paragraph; nested/list/table selections and
  existing-code-block toggling keep native behavior. Direct dependency
  `@tiptap/extension-code-block@3.31.3`; StarterKit's codeBlock is disabled and
  the Host extension re-adds it only for the `full` preset. Regression suites:
  `tests/framework/editorLinkBehavior.test.ts`,
  `tests/framework/editorCodeBlockPaste.test.ts`, shared harness in
  `tests/helpers/editorClipboard.ts`.

## Admin And Trusted Frontend Runtime

- `SFExtensionSettingsRenderer` is the normal plugin/theme path and renders
  Schema forms, tabs, groups, columns, callouts, and allowlisted actions without
  extension JavaScript.
- Complex settings may load an authenticated immutable `.mjs`/`.css`
  micro-frontend only after whole-artifact trust. It is client-only and falls
  back to Schema UI on import/API/mount/CSS/cleanup/quarantine failure.
- `SFTrustedAdminPageComponent` applies the same boundary to ordinary plugin
  admin pages. The existing `layout: admin` route owns sidebar, topbar, tabs,
  heading, permissions, and inactive states; the plugin receives a page bridge
  and mounts only into the body target. Its bridge includes locale,
  appearance, page identity, namespaced requests, Toasts, translation, and
  admin navigation, but no settings draft authority.
- There is no runtime SFC compiler, extension dependency installer, Nuxt build
  supervisor, host-peer resolver, or frontend release supervisor.
- The admin home renders protected SForum build identity in
  `SFAdminOverviewRuntimeCard`: commit/build time, Go version, uptime,
  worker/queue/GC/database diagnostics, and the canonical source link. The
  unified Core/Web version is displayed once beside the SForum brand in the
  admin sidebar header; local builds show `dev-<commit5>` when Git metadata is
  available (otherwise `dev`), and release images receive the tag version
  through the shared build argument.
- The admin shell refreshes release status every five minutes while open. A new
  release opens a localized modal once, then suppresses repeat prompts in that
  browser for six hours while retaining the sidebar update badge.
- Admin route middleware must stay narrow and avoid component-context-only
  composables such as direct `useI18n()` calls.

## Public Chrome Geometry

- `SFNavbar` owns its scoped layout rules. Host fallback chrome passes the
  explicit `layout="fullwidth-3col"` variant; global theme CSS must not try to
  override the component's base `display` mode by stylesheet order. Theme L1
  paths continue to own their navbar geometry through exact theme assets.
- `SFNavbar` is the public topbar orchestrator rather than the owner of every
  narrow-screen surface. `SFPublicMobileSearchBar` owns the mobile search and
  permission-aware compose row; `SFPublicMobileBottomNavigation` owns the fixed
  home/post/notification dock and unread badge. This split keeps the shared
  navbar below the 1000-line architecture gate without weakening its existing
  desktop/mobile contract.
- Mobile public chrome follows the approved G1 white-flat direction. At narrow
  widths the topbar keeps only search, appearance, sidebar, and avatar controls;
  notification previews, language, and compose controls remain desktop-only.
  The mobile search input moved to a topbar search icon that opens a fixed
  slide-down search panel (search field + compose + close), so the mobile
  navbar is a single 54px row instead of a 108px topbar+search stack. A fixed
  three-item navigation exposes home, post, and notifications, with the shared
  unread count on the notification item.
- Mobile topic rows are a flat white list (hairline separators, no rounded
  cards, no shadows). Titles wrap naturally with badges before the title. The
  meta row shows `[avatar] author ← lastReply · lastActivity` when the topic has
  replies, otherwise `[avatar] author · created`; the category pill follows, and
  the reply count sits on the right as a neutral count badge. Tags are no longer
  shown in mobile rows.
  The inline mobile author avatar is 24px; the shared list avatar size remains
  unchanged.
- The shared list shell (`assets/css/sforum-home.css` `.sforum-home__layout`)
  owns the page canvas surface for the home/category/tag/search list family and
  paints `--sf-public-surface` at **every** width. Transparent G1/C1 rows sit on
  that surface, so the declaration must never be gated to a desktop breakpoint:
  when it lived only inside `@media (min-width: 981px)`, narrow screens fell
  through to `--sf-public-bg` and the mobile list background diverged from
  desktop.
- Desktop topic rows use the approved C1 flat list: a single white row with
  hairline separators (no five-column table grid). Pinned/hot badges sit on
  their own line above the title; the title line is `[category pill] title`;
  the meta row shows `[avatar] author ← lastReply · lastActivity` on the left
  and up to three tags plus the neutral reply badge on the right. When a topic
  has no replies the meta shows `author · created`. Mobile rows keep the G1
  layout (badges inline before the title, category pill in the meta row, no
  tags). The desktop inline meta avatar is 28px.
- Public mobile drawers use a viewport-wide fixed backdrop (`inset: 0`) and a
  drawer starting at the viewport top (`top: 0`), so opening the sidebar
  covers the topbar and search region instead of appearing underneath it.
  Core fallback and the selected default runtime theme must keep these two
  declarations aligned; `publicMobileDrawerGeometry.test.ts` guards both CSS
  owners so an older theme rule cannot reintroduce a topbar offset.
- Mobile public layouts clear the desktop `min-height: calc(100vh - topbar)`
  contract. Short topic, notification, settings, moderation, and profile
  surfaces now flow directly into their footer instead of leaving a viewport-
  sized blank block after the last content row.
- A Core-owned product surface may be `themeable` without being `replaceable`.
  In that case the theme controls only the reviewed L1 shell and must mount the
  required Host island.
- Account settings now expose separate Page Registry-backed route shells for
  personal appearance (`/settings/appearance`), login methods
  (`/settings/login-methods`), local password
  (`/settings/password`), account security (`/settings/security`), personal
  access tokens (`/settings/tokens`), and notification preferences. Keep the
  shared `SFSettingsShell` three-column geometry and account sidebar contract
  aligned across these pages.

## Request And SSR Regression Rules

### CSRF

Cookie-authenticated unsafe calls go through `useApiClient().request`. It primes
the CSRF cookie when absent, adds `X-Csrf-Token`, and retries once after
`csrf.invalid`. Do not use raw `$fetch` for those writes.

### API Connection Failure UX

`useApiConnectionError()` is a thin facade; the buffering/recovery engine lives
in `composables/errors/useApiConnectionRecovery.ts` (module-level singleton,
client-only, zero Nuxt composables at probe time — probe base URL is passed in
by `useApiClient`). Connection failures enter a pending buffer and probe
`GET {apiBaseUrl}/health` with 1s→2s→4s (4s cap) backoff; the modal opens only
after probes keep failing past `GRACE_MS` (~15s, tuned for slow deployments —
probes allow 4s so a slow-but-alive /health never counts as down) and
auto-closes when `/health` recovers (5s cadence, `recoveredAt` drives the
recovered toast). Safe methods (GET/method-less) get one in-place retry inside
`useApiClient` before being buffered; unsafe methods never retry in place.
Default request timeouts: GET 15s, unsafe 30s (slow-site friendly); caller-provided `timeout` always wins. Errors are classified by
`errors/apiConnectionErrorClassify.ts` — keep business-reason 5xx out of the
connection modal. `/_nuxt/**` chunk and dynamic-import failures are handled by
`plugins/assetReload.client.ts` (toast + delayed reload with a 10s
sessionStorage cooldown) and must never reach the connection-error modal.

### Authentication And SWR

Never serialize user auth into shared anonymous public payloads. Session-bearing
SSR restores auth before render and disables cache; protected/admin middleware
remains a UX guard while API policy is authoritative.

Anonymous SSR requests without an `sforum_session` cookie initialize auth as
`guest` before rendering; the client may still revalidate in the background.
This keeps login/register chrome deterministic without adding a shared session
request or serializing user data.

Public navbar, authentication-shell, and admin chrome controls that retain a
`ClientOnly` boundary for Nuxt UI/Reka hydration safety must render a visible,
geometry-equivalent SSR fallback. The fallback is presentation-only: mark it
`aria-hidden`, remove it from tab order, and disable pointer events. The
hydrated control remains the sole interaction owner.

### Rendered-content directives

Every directive used by SSR templates needs a server registration. Keep the
real code-block client plugin and no-op `highlight.server.ts` with
`getSSRProps`.

## Important Paths

| Path | Responsibility |
| --- | --- |
| `apps/web/app/app.vue` | Root startup, SSR head, session/theme boot |
| `apps/web/app/components` | Uppercase `SF*` component library |
| `apps/web/app/composables` | API, auth, options, Page Registry clients |
| `apps/web/app/pages` | Public and admin route shells |
| `apps/web/app/middleware` | Route/cache/session behavior |
| `apps/web/app/plugins` | Client/server integrations and directives |
| `apps/web/app/assets/css` | Host component/theme baselines |
| `extensions/builtin/themes/sforum-default` | Protected default L0/L1 package |

### Protected shortcode SSR/cache boundary

M9 public topic/comment JSON and Page Registry view-models receive only the
Host public projection. Any request-level protected composition is excluded
from Nuxt payload reuse and is served with `private, no-store`; client code
must not infer authorization from placeholder markup. Nitro middleware applies
`private, no-store` and `Vary: Cookie, Authorization, Accept-Language` to every
`/t/**` HTML/payload response because it cannot know whether the resolved topic
contains protected slots before the API read. Browser evidence confirms the
unique marker is absent from SSR HTML, hydrated DOM, `__NUXT_DATA__`, metadata,
OpenGraph, and JSON-LD while the localized Host fallback remains visible.

### Shortcode presentation hooks

Core emits bounded semantic wrappers but owns no shortcode-specific CSS. The
enabled and trusted `sforum-shortcodes` exact artifact declares
`frontend/public/shortcodes.css` for `core.surface.forum-content`; that one
plugin stylesheet owns published wrappers, protected/fallback states, editor
NodeViews, and the reference dialog. `usePublicContentStyles` loads the generic
Host catalog during public SSR/navigation with immutable URLs and SRI, and
clears it on admin routes. Disable, Safe Mode, trust revocation, artifact drift,
or catalog failure removes the plugin style instead of leaving Core-owned
presentation behind. See
`../decisions/2026-08-29-plugin-owned-forum-content-styles.md`.

### Shortcode editor previews

Reference and protected NodeViews use the approved editor-preview geometry:
one quiet bordered surface with a floating type/contract label, compact content
identity, and selection outline. They deliberately do not render replace,
locate, unwrap, or delete buttons inside the node. Reference selection remains
available through the existing dialog and keyboard/double-click editing flow;
protected content remains a native `NodeViewContent` region. The dialog and
reference cache preserve only presentation-safe avatar/category icon metadata
returned by the composer reference endpoint.

## Verification

- Unit tests: `cd apps/web && bun test`
- Typecheck: `cd apps/web && bun run typecheck`
- Build: `cd apps/web && bun run build`
- Architecture preflight: `node tests/validate-architecture-boundaries.mjs`.
  Run it before committing or pushing after adding, moving, or materially
  growing production files; an unbaselined handwritten file must not exceed
  1000 lines.
- Use the in-app browser for SSR/interaction/responsive checks. Do not kill the
  user's port-3000 dev server.
- For auth/cache/Page Registry changes, verify hard refresh, client navigation,
  authenticated public SSR, anonymous SSR, response status/cache headers,
  console output, and no-JavaScript content where relevant.

## Outbound Link Policy (2026-10-16)

- Server authority: `Support/ContentLinks.Apply` forces `target="_blank"` and the
  canonical `nofollow noreferrer noopener` rel on every managed anchor, and
  rewrites absolute `http(s)` hrefs to `/outbound?u=<base64url>`. It runs after
  sanitization in the four content seams; `target` must stay allowed in all four
  bluemonday policies or the guarantee (and the SSR fixed point check) breaks.
- `/outbound` is a Page Registry page (`site.outbound`) with
  `Replaceable:false`; it therefore renders through `SFHostPublicChrome` and
  cannot be replaced or themed away. Its page ID, `ViewModelUtility` kind, data
  source, factory, and V3 catalog identities are all wired.
- Client layer: `utils/forum/forumOutboundLinks.ts` owns decode/encode,
  same-site/allowlist classification, and idempotent DOM decoration;
  `v-outbound` (`plugins/outbound.{client,server}.ts`) applies it to
  `SFMentionContent.vue`, so topic bodies and comments share one seam. Mentions
  (`a.sf-mention`) and image-viewer triggers are explicitly skipped.
- Option ownership: `forum.links.external_interstitial` and
  `forum.links.trusted_domains` live with the Wave 1 community policy options;
  both are public (the click-time decision needs them) and gated by
  `forum.settings.manage` for writes.
- Interstitial presentation: the origin side renders the operator-configured
  brand icon (`siteLogoUrl` + `siteName` from web options, same source as
  `SFNavbar`), the destination side uses warning tokens, and the confirmation
  checks use success tokens. Never hardcode a brand badge in a Host surface.

## Browser Evidence Harnesses (narrow-screen and shell rules)

- Real `390x844` means `tests/browser-public-390x844.mjs`, which launches a real
  Chrome over CDP and asserts that `Emulation.setDeviceMetricsOverride` actually
  took effect (it FAILs when `innerWidth !== 390`). The `ego_*` sidebar carrier is
  **not** a narrow-screen carrier: the override is accepted but never takes effect
  there (`innerWidth` is always 500), so a 500-wide measurement must never be
  published as a 390 one. The admin equivalent is
  `tests/browser-admin-390x844.mjs` (session cookie injected via `Network.setCookie`,
  because the cookie is httpOnly).
- **A settle heuristic must not be "the page text stopped changing".** The public
  harness originally decided a page had settled when `document.body.innerText.length`
  stopped changing for two polls. That condition is satisfied by the *SSR* text
  **before hydration**, so it exited at ~2 s while a client-mounted public L2
  component appears at ~5.4 s in the same context — producing a false negative that
  read as "the component does not mount and no descriptor request is issued".
  The loop now waits for real hydration (`#__nuxt.__vue_app__`), refuses to accept
  stability before hydration, requires a **terminal** public-L2 state
  (`mounted|fallback|quarantined`, never `idle|loading`) for paths passed in
  `SFORUM_L2_PATHS`, and fails when hydration never happens. Any new client-rendered
  surface added to a harness needs the same treatment: wait for the signal that the
  client work finished, not for text to stop moving.
- Assertions requiring a mount are opt-in via `SFORUM_L2_PATHS` and were verified
  non-vacuous with a negative control (pointing it at a page with no L2 widget makes
  the run FAIL).

## Next Steps

1. Execute the tri-state color-mode task book from M0. It must freeze the
   native dependency/control contract, SSR/cache behavior, safe canonical
   origin mechanism, and tests before implementation.
2. Continue configurable public navigation at M7 only: execute the plugin/theme
   lifecycle, recovery, documentation, full repository, and final Browser
   release matrix. Keep the user's port-3000 server untouched.
3. Keep theme-defined system error pages SSR-complete, permission-aware,
   cache-safe, localized, and covered whenever new browser-facing error
   producers are added.
4. Keep new pages SSR-complete, permission-aware, cache-safe, localized, and
   registered through stable Page Registry/component contracts.

## Open Questions

- Whether `en-US` copy must be complete for first public release.
- Which remaining `SF*` wrappers provide enough shared behavior to justify
  wrapping Nuxt UI primitives.
