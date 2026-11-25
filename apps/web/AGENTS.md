# SForum Web Agent Guide

Read the repository root `AGENTS.md` first. This file owns Nuxt, Vue, Page
Registry, admin presentation, public chrome, and browser verification rules.

## Frontend Boundaries

- Keep `apps/web/app/pages/**` as route shells: metadata, route/query state,
  permission selection, SSR orchestration, and composition. Product forms,
  tables, inspectors, and substantial surfaces belong in feature components.
- Public page title, canonical, robots, and social metadata belong to
  `useSForumSeo`/`resolveSEO`. Do not reintroduce per-route `useSeoMeta`
  ownership unless the route intentionally bypasses the resolver and matching
  tests document why.
- For routes with an index and nested detail pages, use
  `pages/<domain>/index.vue` plus `pages/<domain>/[id].vue`. Do not combine
  `pages/<domain>.vue` with `pages/<domain>/[id].vue` unless the parent
  intentionally renders `<NuxtPage>`.
- New product components belong under a domain folder such as `admin/`,
  `forum/`, `identity/`, `settings/`, or `themes/`. Only approved shared `SF*`
  primitives and compatibility entrypoints belong at the component root.
- Apply the same domain grouping to `composables/`, `utils/`, and
  `apps/web/tests/`. Use explicit imports when a move would change a Nuxt
  auto-imported component name.
- Prefer rendered behavior and exported model helpers in tests. Do not prove a
  contract by reading a whole route file and matching implementation strings.

## Page Registry

- A Page Registry page is incomplete until the catalog, route shell,
  ThemeCompiler ViewModel registry, `PageViewModels.Source.Populate`, Host
  island binding/allowlist, built-in theme templates, and catalog completeness
  tests all use the same stable page ID and contract.
- Public pages that share a product shell share one geometry contract: topbar
  tracks, viewport insets, sidebar/right-rail widths and padding, center-column
  scroll ownership, and responsive collapse points. A Core fallback must keep
  the same geometry.
- Public chrome has one owner per render path. A Page Registry template that
  mounts `sf-navbar` or `sf-footer` must not duplicate the same chrome inside
  its Host body island. Browser evidence must show exactly one global navbar
  and one global footer.
- Theme files have a fail-closed three-way identity contract. Every
  `theme.json.pages[].template` path must have exactly one matching Manifest V3
  `templates[]` declaration and one matching `packageFiles[]` entry of kind
  `template`; paths and digests must agree.
- After changing a template, run `extension digest --write`,
  `extension validate`, and `extension test` on the source package before
  staging or activation.
- Editing a built-in runtime theme under `extensions/builtin/themes/**` does
  not update the active immutable artifact. Rebuild built-ins, restart the API
  so `SyncBuiltins` stages the digest, activate through the normal admin flow,
  and verify `/site/active-theme/skin` plus `/pages/resolve`.
- A fresh install is the exception: startup repair host-approves the protected
  built-in default theme. A newly staged digest still requires explicit
  activation.
- For every new or changed Page Registry route, rendered evidence must show the
  expected `data-provider` and `data-template="1"`. Core fallback
  (`provider="core"`, `data-template="0"`, or a visible fallback notice) is not
  theme-runtime completion.
- When adding a template, add every Host island to both the production binding
  map and template-validator allowlist. Add completeness tests for paired
  surfaces such as create/edit.

## Admin Settings And Tabs

- Fixed Core-owned tabs keep each substantial tab in its own component under a
  domain path such as `components/admin/settings/<area>/tabs/`. Form state,
  validation, save/reset behavior, and focused tests stay with that tab or its
  domain composable.
- Core-owned admin settings pages follow the geometry in
  `apps/web/app/pages/admin/settings/index.vue`: compact `text-xl` title with
  the registry icon, standard bordered `UDashboardToolbar`,
  `SFAdminFixedTabNav`, and one active content panel in a `min-w-0` column.
- The route shell owns page title, toolbar, query-synchronized tab selection,
  and active-tab refresh. A tab must not render another page title, toolbar, or
  sibling tab content.
- Do not add a marketing-style hero, second page intro, separate tab system, or
  several competing top-level cards. A deliberate layout departure requires
  user confirmation and a decision record.
- On narrow screens, toolbar commands may collapse to icons, but must keep
  `aria-label` and `title`.
- Runtime-defined extension/provider tabs use the generic Schema/provider
  renderer. Do not add vendor-specific Core files to satisfy the one-file-per-
  tab rule.
- New or materially changed admin settings pages require rendered browser QA
  at desktop and `390x844`, covering title/toolbar/tab alignment, active-panel
  transition, accessible compact actions, and no horizontal overflow or
  clipped controls.

## UI Feedback

- Do not use emoji as icons, decorative symbols, status markers, or action
  indicators. Use the existing Tabler/Lucide/Nuxt Icon integration; do not
  hand-roll inline SVG when an approved icon exists.
- Use Toast feedback for user-triggered success, completion, background-task
  start, copy, reset, save, upload, export, and queued-job actions.
- Toast success styling must follow active SForum appearance/theme tokens and
  admin personalization, not Nuxt UI's default success green.
- Keep blocking errors, field validation, and required guidance next to the
  relevant form/page state. Error Toasts may report non-blocking failures but
  must not replace field-level messages.
- Non-error alerts and Toasts auto-dismiss after 5 seconds. Error alerts and
  Error Toasts stay until dismissed or resolved.

## CSP And Browser Gates

- Every public page must emit the document CSP. A public page without it is a
  regression.
- `node tests/validate-document-csp.mjs` runs in real Chrome via CDP and checks:
  the policy is present, zero `securitypolicyviolation` events occur,
  `--ui-primary` and at least one inline style take effect, `script-src` carries
  exactly one nonce used by the rendered inline script, and `style-src` carries
  no nonce/hash.
- Read the nonce from the same navigation response the browser rendered. If
  that response is a 304 or lacks CSP, re-read through a cache-bypassing request,
  report `headerSource=cache-bypass`, and skip the cross-request nonce
  comparison.
- The gate needs web and API. Default target is `/`; `SFORUM_CSP_PATHS` accepts
  a comma-separated list. Exit `0` passes, `1` fails, and `2` means environment
  not ready. A reachable page without CSP is a failure, never a skip.
- Shared SEO, Page Registry route-shell, public chrome/navigation, theme island,
  and admin settings shell changes require `bun test`, typecheck, and relevant
  browser evidence. Focused unit tests alone can miss static contract tests.
