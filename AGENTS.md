# Agent Guide

This file is the shared working agreement for SForum. Read it before starting
any new session. It is intentionally self-contained: do not assume contributors
have the same personal or global Codex instructions. Personal defaults may
supplement this file, while more specific repository instructions take
precedence within their directory scope.

## Project Intent

SForum is a maintainable, plugin-first, open-source forum framework. Core is
the host framework; product verticals and provider/vendor behavior live in
extensions. The repository is past scaffolding and actively implementing core
forum, identity, admin, and extension systems.

## Working Principles

- Prefer mature third-party libraries before building custom infrastructure. Before starting a module or feature, briefly check whether established libraries already solve the core problem well.
- Avoid messy, tightly coupled code. Keep responsibilities clear, name things plainly, and make the easy path maintainable.
- Split long implementations across multiple focused files. Avoid placing more than 1000 lines in one file unless there is a strong, documented reason.
- Keep changes scoped to the current task. Do not refactor unrelated areas just because they are nearby.
- Record decisions in the knowledge base when they will matter to future sessions.

## Stack And Directory Map

Monorepo with a Go API plus a Nuxt web app, plus shared contracts, extensions,
knowledge base, and tests.

- `apps/api` — Go 1.26.10 API + background worker (toolchain anchored in
  `apps/api/go.mod`; do not let docs lag behind it). Module path
  `github.com/keeptrue/sforum/apps/api`. Laravel-style Go layout:
  - `cmd/` — `api` (HTTP server), `worker` (River queue worker), `migrate`
    (embedded Goose migrator), `sforum` (developer CLI; scaffolds
    plugins/themes, runs `seed:forum`).
  - `bootstrap/` — runtime assembly; `NewWorker` for the worker process.
  - `app/Http/` — HTTP kernel, controllers, and route registration
    (`app/Http/Controllers/*`).
  - `app/Models/` — domain logic and services.
  - `app/Providers/` — provider wiring (mail provider slot, attachment
    storage adapters, search, cache, etc.).
  - `app/Support/` — cross-cutting infra: `Jobs` (River durable queue),
    `Search` (host framework + site PG FTS; optional Meili plugin), `Cache` (Redis CachedStore decorator).
  - `database/` — `migrations/` (Goose SQL), `migrator/` (shared embedded
    migrator), `queries/` + `sqlc/` (sqlc-generated code; config in
    `sqlc.yaml`).
  - `config/` — runtime config loaders. `config.Load` does NOT read `.env`;
    the dev scripts source `.env` and export vars explicitly.
- `apps/web` — Nuxt 4 / Vue 3 / Nuxt UI 4 frontend (package name
  `@sforum/web`). SSR-first (no `ssr: false` remains anywhere). Key deps:
  Bun, Tailwind, `@nuxtjs/i18n`, Tiptap, DOMPurify, altcha,
  `@iconify-json/tabler` + `@iconify-json/lucide`. App code lives under
  `apps/web/app/{components,composables,pages,layouts,middleware,plugins,config}`
  with the uppercase `SF` prefix component library, and admin pages under
  `apps/web/app/pages/admin`. i18n default locale `zh-CN`, secondary `en-US`.
- `contracts/` — modular OpenAPI contract. `openapi.yaml` is the entrypoint
  index only; paths in `openapi/paths/<module>.yaml`, schemas in
  `openapi/schemas/<module>.yaml`, shared components in `openapi/components/`.
- `extensions/` — package trees; only `builtin/` is boot-scanned into the
  admin list (`SyncBuiltins`). Also `dev/` (gitignored scaffolds),
  `optional/` (ship-with-repo, operator install), `fixtures/` (CI).
  Layout map: `extensions/README.md`. Container images copy built-ins to
  `/app/extensions/builtin`.
- `knowledge/` — project memory: `index.md`, `modules/`, `decisions/`,
  `sessions/`, `glossary.md`, `research.md`. Read before sensitive work.
- `tests/` — repo-level validation scripts (Playwright/Node) that hit the
  running dev servers.
- `scripts/` — dev/test orchestration shell scripts (see Commands below).
- `docs/` — bilingual handbooks (`zh-CN/`, `en-US/`) plus path-stable extension
  reference under `docs/extensions/`; historical material in `docs/archive/`.
- `deploy/`, `compose*.yaml`, `deploy.sh` — deployment and ops.
- Runtime deps via Compose: PostgreSQL, Redis, Mailpit (Meilisearch optional via `--profile search`).

## Commands

Run from the repo root unless noted. Before any `go get`/`go mod tidy`/
`bun install`/`bun add` network command, set the local proxy:
`export https_proxy=http://127.0.0.1:7897 http_proxy=http://127.0.0.1:7897 all_proxy=socks5://127.0.0.1:7897`

`apps/web` uses bun only (`bun.lock`; Docker and CI run
`bun install --frozen-lockfile`). Do not run `pnpm install` there: a second
package manager rewrites `node_modules` links and leaves two copies of `vue` /
`vue-i18n` on disk. If a package-manager switch or a reinstall happened while a
dev server was running, delete `apps/web/.nuxt` and
`apps/web/node_modules/.vite` and restart it — a live process keeps the modules
it already loaded while newly resolved ones come from the new tree, which
surfaces as unrelated runtime errors (for example a plugin that never installs).

Development (the user runs `apps/web` dev server manually on port 3000 — do
not kill it):

- `./scripts/dev.sh` — start only dev dependencies (PostgreSQL, Redis,
  Mailpit; Meilisearch only with compose profile `search`) via Compose and run migrations. `--build` rebuilds,
  `--no-migrate` skips migrations. Stops old Compose-managed frontend/backend
  containers first.
- `cd apps/web && bun run dev` — plain Nuxt dev. Public themes use Page
  Registry + L0/L1; prebuilt admin settings components load by immutable digest.
  There is no theme/admin frontend build supervisor.
- `./scripts/api-dev.sh` — run the API with `air` (hot reload). On start it
  reclaims only leftover `sforum-api` processes on `HTTP_PORT` via
  `scripts/free-api-dev-port.sh`; if the port is held by docker or another
  non-sforum process it refuses and does not kill it. Air's `pre_cmd` only
  clears orphan `sforum-api` (not the currently managed instance). In dev the
  API always embeds and owns the worker.
- `./scripts/worker-dev.sh` — retained only as a compatibility error; standalone
  Worker startup is unsupported.

Build, typecheck, lint, test:

- `cd apps/web && bun run build` — Nuxt build (uses `NUXT_BUILD_DIR=.nuxt-build`).
- `cd apps/web && bun run typecheck` — Nuxt typecheck (uses
  `NUXT_BUILD_DIR=.nuxt-typecheck`).
- `cd apps/web && bun test` — full web unit/regression suite. Run it before
  merging shared frontend authority changes such as SEO title ownership, Page
  Registry route shells, public chrome/navigation, theme island binding, or
  admin settings shell contracts; focused slices alone can miss stale static
  contract tests that CI will still execute.
- `cd apps/api && go build ./...` / `go test ./...` — Go build and tests.
- `node tests/validate-architecture-boundaries.mjs` — enforce file-size,
  flat-directory, fixed-tab, and legacy God-object non-growth guardrails.
- `./scripts/test.sh` — full repo test gate, run as three concurrent lines:
  Go tests plus the compatibility farm and `go build ./...`; Nuxt typecheck,
  `bun test`, and the production build; the static contract/release gates and
  the product `tests/validate-*` scripts (demos remain optional offline
  checks). Per-line logs land in `tests/.logs/`.
- `./scripts/test.sh --fast` — local daily loop: the same Go tests and Nuxt
  typecheck plus `bun test`, but skips the production web build, the
  compatibility farm, and the release/deploy tooling gate, which CI already
  covers in full. Without the flag the gate keeps its full meaning.
- `ruby scripts/validate-openapi-refs.rb` — validate OpenAPI `$ref`s after
  editing any contract file.

CLI:

- `cd apps/api && go run ./cmd/sforum` — developer console: scaffold
  plugins/themes, `seed:forum` fake data. `seed:forum` is append-only,
  triggers no events, reads `DATABASE_URL` from env or `--database-url`.

## Architecture And Module Boundaries

Directory placement is part of the design, not cleanup deferred until a file
becomes unmanageable. Before adding a file, identify the owning product domain,
layer, and stable package/component boundary.

General rules:

- New product code must live in a domain subdirectory. Do not add another
  unrelated file to a crowded root directory because auto-imports or a shared
  package make it convenient.
- Root-level shared directories are reserved for genuinely cross-domain
  primitives, compatibility facades, and framework entry points. A
  product-specific component, composable, utility, test, handler, or store does
  not qualify as shared merely because two callers currently use it.
- When a change would grow an architecture baseline in
  `tests/architecture-boundaries-baseline.json`, first extract or move an
  existing responsibility. Raising a baseline requires an accepted decision
  record explaining why a better boundary is not currently viable and naming
  the removal/reduction condition.
- Architecture baselines are ratchets. When a legacy file, flat directory,
  inline-tab page, or receiver-method count shrinks, lower or remove its
  baseline in the same change; never leave reclaimed capacity available for
  later growth.
- Moving files without changing ownership is not a sufficient refactor.
  Separate state, validation, persistence, side effects, and transport at a
  boundary that can be tested independently.

Frontend rules:

- Keep `apps/web/app/pages/**` as route shells: page metadata, route/query
  state, permission selection, SSR orchestration, and composition. Product
  forms, tables, inspectors, and other substantial surfaces belong in feature
  components.
- Public page title, canonical, robots, and social metadata belong to
  `useSForumSeo`/`resolveSEO`. Do not reintroduce per-route `useSeoMeta` for
  public page title or robots ownership unless the route intentionally bypasses
  the public SEO resolver and the reason is documented with matching tests.
- When a route has both an index and nested detail pages, use
  `pages/<domain>/index.vue` plus `pages/<domain>/[id].vue`. Do not combine
  `pages/<domain>.vue` with `pages/<domain>/[id].vue` unless the parent route
  intentionally renders `<NuxtPage>`; otherwise Nuxt keeps the parent mounted
  and the child page never renders.
- Adding a Page Registry page is incomplete until the catalog, route shell,
  ThemeCompiler ViewModel registry, `PageViewModels.Source.Populate`, Host
  island binding/allowlist, built-in theme templates, and catalog completeness
  tests all recognize the same stable page ID and contract.
- For fixed Core-owned tabs, each tab's substantial content must be a separate
  component file under a domain path such as
  `components/admin/settings/<area>/tabs/`. Keep its form state, validation,
  save/reset behavior, and focused tests with that tab or its domain
  composable. The route shell owns only tab selection and shared page-level
  orchestration.
- Core-owned admin settings pages must follow the visual geometry established
  by `apps/web/app/pages/admin/settings/index.vue`: a compact `text-xl` page
  title with the registry icon, the standard bordered `UDashboardToolbar`,
  `SFAdminFixedTabNav` for fixed sections, and one active content panel in a
  `min-w-0` column. Do not introduce a marketing-style hero heading, a second
  page intro, a separate tab system, or several competing top-level cards.
- The route shell owns page title, toolbar, query-synchronized tab selection,
  and active-tab refresh. A tab component must not render another page title,
  toolbar, or sibling tab's content. On narrow screens, toolbar commands may
  collapse to icons only, but must retain `aria-label` and `title` text.
- A deliberate admin-settings layout departure requires user confirmation and
  a decision record explaining why the shared settings contract is
  insufficient. Convenience or independently designed feature UI is not a
  sufficient reason.
- Runtime-defined extension/provider tabs are the deliberate exception: render
  them through the generic Schema/provider renderer. Do not create
  vendor-specific Core files merely to satisfy the one-file-per-tab rule.
- New product components must not be placed directly in
  `apps/web/app/components`; use domain folders such as `admin/`, `forum/`,
  `identity/`, `settings/`, `themes/`, or another clear owner. Keep only
  approved shared `SF*` primitives and compatibility entry points at the root.
- Apply the same domain grouping to `composables`, `utils`, and `apps/web/tests`.
  Use explicit imports when moving into a subdirectory would otherwise change a
  Nuxt auto-imported component name.
- Do not test component organization by reading a whole route file and matching
  implementation strings. Prefer rendered behavior, exported model helpers, or
  the focused tab/component source only when a static contract is unavoidable.

Backend rules:

- A Go subdirectory is a package boundary, not a visual folder. Before creating
  it, define its public responsibility, dependency direction, transaction
  ownership, and test boundary; avoid packages that only re-export a parent
  package or create import cycles.
- HTTP controllers own decoding/encoding and transport concerns; domain
  services own authorization-aware business workflows; stores own persistence;
  `bootstrap` owns assembly only. Do not mix SQL, request parsing, policy,
  process control, and response formatting in one function or type.
- Do not add new responsibilities to the legacy
  `app/Support/Extensions` (`extensionsruntime`) or
  `app/Models/Extensions` flat packages. Extract a focused collaborator or
  package first. These packages and their `Manager`/`Service` receiver counts
  are non-growth baselines.
- A `Service`, `Manager`, `Controller`, or `Store` spanning multiple
  independent capabilities must become a compatibility facade over focused
  collaborators. Do not keep adding receiver methods or
  `NewServiceWith...` constructor permutations to a God object; prefer one
  config/options constructor and small capability interfaces.
- Split large files inside the current package before changing package
  boundaries when that safely reduces review risk. Extract a new package only
  after callers depend on a stable minimal interface.
- Prefer external black-box tests (`package foo_test`) for public behavior.
  Use same-package tests only when verifying a private invariant that cannot be
  covered through the public contract. New package boundaries must include
  allowed and denied behavior tests where permissions or trust are involved.

## Core Framework And Plugin-First Development

SForum core is the host framework, not a place to accumulate every optional
product vertical. New capabilities must be designed around stable framework
contracts first.

- Treat payments, outbound mail delivery, notification channels, analytics,
  external integrations, app-store catalogs and their release services, and
  similar deployment-specific systems as plugins or out-of-tree services by
  default. Core keeps only the generic primitives such an integration needs —
  for extension catalogs that is `install-from-url` plus the host-mediated
  install action of trusted admin pages, never a catalog, index, or store UI
  (see `knowledge/decisions/2026-10-24-app-store-out-of-tree.md`).
- Core should expose the stable contracts that make plugins easy to build:
  explicit events, provider slots, typed payloads, permission checks, admin
  selection/reset UI, SDK helpers, scaffolding, tests, no-op defaults, and
  development adapters.
- When a product area needs shared semantics, such as payments, core may define
  the framework model and lifecycle: payment intents, transaction records,
  entitlement checks, webhook idempotency, provider interfaces, events, and
  admin configuration contracts. Provider/vendor behavior still belongs in
  plugins.
- Real provider or vendor logic should live in an extension package, including
  protected built-in plugins when SForum needs a bundled default.
- Plugin development is **zero-command** in development: the host process itself
  watches `EXTERNAL_EXTENSION_ROOTS` (dev assembly only), and on every source
  change runs frontend (bun) + backend (go) builds → digest (incl. SRI
  recompute) → stage → exact per-digest grant → enable/upgrade, with the plugin
  subprocess restarting and the lifecycle publishing the new public assets — no
  login, no password, no dev trust session, no manual confirmation, and it works
  on a fresh database with zero admin accounts (a dedicated credential-less
  `dev-auto` system account is the audit attribution; disabling or deleting it
  in the admin disables the channel). Editing content (CSS/JS/backend logic/
  settings/routes) needs no version bump; only a real component **interface**
  change (props/schema shape, a different `l2Component` ref, added/removed
  component id) requires a manual `components[].contractVersion` bump — that is
  the correct human checkpoint and must NOT be automated (auto-bumping fights
  the exact-artifact system and was removed). Agents must NOT wrap plugin
  iteration in any credential/auth step, must NOT restart the dev API to pick
  up a digest, and must NOT hand-roll SRI/digest scripts. The old
  `scripts/dev-plugin-loop.sh` and per-digest trust CLI flows are retired;
  `sforum extension dev-trust *` remains only as a legacy credential-gated
  helper. Do not weaken production: the dev watcher and dev-auto grants are
  never injected outside `APP_ENV=development` (Safe Mode also blocks them),
  install/enable/upgrade HTTP routes keep their super_admin gates, and any
  unmet condition must fail closed to `ErrTrustChallengeRequired` / the manual
  trust challenge path. Accepted tradeoff (operator decision, 2026-10-17): on
  the dev machine, any process able to write a dev source root effectively acts
  as the operator. See `knowledge/decisions/2026-10-17-dev-zero-command.md`.
- Upload/static installation only validates, previews, and stores an inert
  package; it must not execute package code. Existing delegated extension
  managers may perform that operation. Executable install hooks are deferred to
  the first enable and require exact-artifact, actor-bound `super_admin`
  confirmation before any process start, migration, frontend import, or
  external side effect.
- Executable plugins may compose or replace core routes, guards, services, and
  other declared behavior only through versioned registries after an exact-
  artifact, actor-bound `super_admin` trust confirmation. Raw request/session
  authority and raw core database access are separately declared high-risk
  powers. Do not add undocumented monkey-patching or implicit override paths.
- Trusted Route Registry contributions may claim any declared public, admin, or
  API path and HTTP method. Theme overrides are presentation-only and must not
  alter a plugin's versioned business data contract.
- Every core module must maintain its Extension Surface Matrix across routes,
  hooks, queries, admin/public components, identity/permissions, media,
  navigation/regions, cache invalidation, jobs, and lifecycle. A deliberately
  closed surface needs a documented security, integrity, or ownership reason.
- A migration that adds a new core data domain must deliver, in the same
  change, the stable read-only view under `sforum_core_v1` and at least one
  stable HostQuery definition for its public data (with allowed and denied
  tests); a deliberately absent read surface needs a documented reason in the
  module note. Plugin data needs grow only through the four sanctioned
  surfaces (view / query / command / event) per
  `knowledge/decisions/2026-10-18-extension-data-surface-policy.md` — never
  through per-plugin RPC special cases, and `raw_core` is a signed escape
  hatch, not a data-distribution channel.
- Prefer dedicated Admin Surface, Query, Identity/Permission, Media Pipeline,
  and Navigation/Region contracts over forcing plugins to replace whole routes
  or use raw database access for ordinary integrations.
- Safe mode, pre-plugin boot health, and out-of-band CLI recovery are host-owned
  and non-overridable. Registry conflicts, selected providers, trust grants,
  and replacement handlers must remain inspectable and auditable.
- Before adding a core module for a new product area, write down why a plugin,
  provider slot, or event is insufficient. Record architectural choices in
  `knowledge/decisions/`.

## Beginner-Friendly Defaults

Every new feature, admin screen, configuration flow, and user-facing workflow
must be friendly to first-time operators and non-expert users from the first
version.

- Provide safe, working recommended defaults instead of requiring users to
  understand every technical setting before the feature can be used.
- Make the recommended path visually obvious in the UI with plain language,
  concise helper text, and familiar controls.
- Support one-click restoration to the recommended defaults for configurable
  features. If restoring defaults would preserve secrets, credentials, or other
  sensitive state, state that clearly in the UI.
- Avoid empty required configuration screens unless the feature truly cannot
  work without external credentials. In that case, explain the missing
  credential and keep non-credential defaults filled in.
- Add or update tests for default resolution and reset behavior when the
  feature stores runtime options, preferences, or other configurable state.

## Open-Source Framework Defaults

SForum is an open-source forum framework for different operators. Core forum
features must provide safe recommended defaults, but product behavior should
remain configurable unless a security or integrity rule requires a hard
boundary. Do not hard-code deployment-specific category names, tag policies,
theme assumptions, provider choices, or public-page availability into services.
Expose stable settings, events, provider slots, or admin controls instead, and
support one-click restoration to recommended defaults for operator-facing
configuration.

## Permission-Aware Development

The permission system is now part of the baseline architecture. When developing
any new feature, keep authorization in mind from the design stage instead of
adding it after the UI or endpoint is already complete.

- Identify the actor, action, and protected resource for every new route,
  mutation, admin screen, background action, or data export.
- Decide whether the behavior is public, login-required, role/permission
  protected, or reserved for `super_admin`.
- Reuse existing permission keys and policy helpers before adding new
  permissions. Add a new permission only when it represents a distinct product
  capability that admins should be able to grant or deny.
- Keep API policy checks authoritative for core-owned handlers. An explicitly
  trusted replacement handler or custom guard owns the authorization contract
  it declares, and must be covered by trust disclosure, audit, and allowed plus
  denied tests. Frontend route guards, hidden buttons, disabled controls, and
  permission-aware navigation are only user-experience helpers.
- Plugins may declare permission keys and recommended role mappings, but plugin
  install/enable code must never assign those permissions. Existing Host role/
  permission management policy remains authoritative for grants.
- For unsafe requests and admin operations, add or update tests that cover both
  allowed and denied access paths.
- When a feature adds new permission keys, update seed data, permission catalog
  display text, OpenAPI/contracts when relevant, and the knowledge base.

## API Contract Workflow

OpenAPI is the shared contract between the Go API, Nuxt consumers, tests, and
future generated clients. Keep it modular as the product surface grows.

- Treat `contracts/openapi.yaml` as the entrypoint and index only. Do not grow
  it back into one giant handwritten contract file.
- Put route operations in `contracts/openapi/paths/<module>.yaml`, reusable
  schemas in `contracts/openapi/schemas/<module>.yaml`, and shared parameters
  or responses in `contracts/openapi/components/`.
- Split contract files by product/module ownership, such as identity, options,
  attachments, extensions, and system health.
- Use relative `$ref` values from the file that owns the reference. When moving
  a schema or path item, update references at the same time instead of relying
  on a later cleanup pass.
- For every new or changed endpoint, update the OpenAPI path, request/response
  schemas, error responses, permission/security notes when relevant, and any
  frontend API typing or tests that depend on the shape.
- Run `ruby scripts/validate-openapi-refs.rb` after editing OpenAPI files. Run
  `./scripts/test.sh` when the contract change is part of feature work.

## Frontend UI Conventions

- Do not use emoji as UI icons, decorative symbols, status markers, or action indicators.
- Use icons from an icon library whenever an icon is needed. Current approved choices are Tabler Icons and Nuxt Icon.
- Prefer the project's existing icon integration before adding a new icon package. Do not hand-roll inline SVG icons when an approved library icon exists.
- Public pages that share a product shell must share one geometry contract:
  topbar tracks, viewport edge insets, sidebar/right-rail widths and padding,
  center-column scroll ownership, and responsive collapse points. A Page
  Registry Core fallback must preserve that geometry; fallback is not
  permission to introduce a second layout.
- Public chrome has one owner per render path. A Page Registry template that
  mounts `sf-navbar` or `sf-footer` must not duplicate the same chrome inside
  its Host body island. Browser completion evidence must confirm there is only
  one visible global navbar and one visible global footer.
- Page Registry theme files have a fail-closed three-way identity contract.
  Every `theme.json.pages[].template` path must have exactly one matching
  Manifest V3 `templates[]` declaration and one matching
  `packageFiles[]` entry of kind `template`; all three paths and both digests
  must agree. `theme.json` is only the page mapping and never substitutes for
  these exact-artifact declarations. After adding or changing a template, run
  `extension digest --write`, `extension validate`, and `extension test` on
  the source package before staging or activation.
- Editing a built-in runtime theme under `extensions/builtin/themes/**` does
  not update the active immutable artifact. Before calling theme UI work
  complete, rebuild built-ins, restart the API so `SyncBuiltins` stages the
  exact digest, activate that staged version through the normal admin flow,
  and verify both `/site/active-theme/skin` and `/pages/resolve` identify the
  expected provider/digest. Source-only unit tests are not runtime evidence.
  A *fresh install* is the exception and needs no manual step: startup repair
  host-approves the protected built-in default theme's Core replacements
  (`approval_source = 'host'`), so the theme renders on first boot; a staged
  new digest still requires the explicit activation above.
- For every new or changed Page Registry route, rendered Browser evidence must
  also show the expected `data-provider` and `data-template="1"`. A page that
  merely opens through `provider="core"`, `data-template="0"`, or the visible
  fallback notice is not theme-runtime completion, even when the active skin
  digest is correct.
- When adding a Page Registry template, verify its Host islands are present in
  both the production binding map and the template validator allowlist. Add a
  completeness test for paired surfaces (for example create/edit) so one
  cannot silently fall back to Core while the other uses the selected theme.
- New or materially changed admin settings pages require rendered Browser QA
  against `/control-panel/settings` at desktop and `390x844` mobile widths.
  Completion evidence must cover title/toolbar/tab alignment, the active-panel
  transition, accessible compact actions, and absence of horizontal overflow
  or clipped controls. Source inspection and unit tests alone are not visual
  completion evidence.
- Use Toast feedback generously for user-triggered actions that succeed,
  complete, start a background task, copy data, reset settings, or save
  changes. Authentication success, create/update/delete success, restore
  defaults, uploads, exports, and queued jobs should normally show a Toast.
- Toast success styling must follow the active SForum appearance/theme tokens
  and admin personalization settings instead of Nuxt UI's default success
  green.
- Keep blocking errors, field-level validation, and guidance that the user must
  read next to the relevant form or page state. Error Toasts are appropriate for
  non-blocking operation failures, but must not replace field-level messages.
- Alerts and toast-style feedback must support automatic dismissal after 5
  seconds for non-error states. Error alerts and error Toasts must not
  auto-close; keep them visible until the user dismisses them or resolves the
  blocking issue.

## Implementation Discipline

The following rules are repository-level instructions for AI coding agents:

- 以瞎猜接口为耻，以认真查询为荣。
- 以模糊执行为耻，以寻求确认为荣。
- 以臆想业务为耻，以人类确认为荣。
- 以创造接口为耻，以复用现有为荣。
- 以跳过验证为耻，以主动测试为荣。
- 以破坏架构为耻，以遵循规范为荣。
- 以假装理解为耻，以诚实无知为荣。
- 以盲目修改为耻，以谨慎重构为荣。
- 以遗忘权限为耻，以主动建模为荣。

Before implementing a module or feature:

1. Define the problem precisely and inspect the existing code, documentation,
   and real interfaces.
2. Check mature libraries and framework-native solutions before building
   custom infrastructure.
3. Compare maintenance status, documentation, license, ecosystem fit, and
   added complexity.
4. Record the choice in `knowledge/decisions/` when it changes architecture or
   establishes a durable dependency.

While implementing:

- Keep straightforward logic straightforward. Do not over-validate,
  over-abstract, add wrappers for one-off behavior, or build nested helper
  chains without meaningful reuse.
- Keep routing, business logic, persistence, validation, presentation, and
  side effects in clear ownership boundaries once the codebase needs them.
- Avoid global mutable state, copy-pasted branches, and functions that silently
  combine unrelated responsibilities.
- Use small functions with explicit inputs and outputs. Extract shared behavior
  when duplication or complexity becomes meaningful, not before.
- Write comments for non-obvious intent, constraints, business rules, and
  tradeoffs. Prefer Chinese for project code unless surrounding code or an
  external API convention makes English clearer; do not narrate obvious code.
- Treat 500 lines as a prompt to review cohesion and 1000 lines as a hard
  warning. Split by responsibility, not arbitrary size. Keep unavoidable
  generated files clearly identified and handwritten logic elsewhere.
- Before adding behavior to a handwritten production file already above 500
  lines, check its current line count and identify a focused extraction
  boundary. An unbaselined file must not cross 1000 lines, and a legacy file
  must not grow past its recorded cap. Do not raise a baseline merely to make a
  feature pass; follow the decision-record requirement above when an exception
  is genuinely unavoidable.
- Run `node tests/validate-architecture-boundaries.mjs` after adding, moving, or
  materially growing production files and before committing or pushing. Run
  this fast gate before slower broad suites; focused feature tests do not
  replace it.

## Network And Dependency Commands

The primary development environment may be in mainland China. The required
local proxy for all network-dependent package commands (`go get`,
`go mod tidy`, `bun install`, `bun add`, etc.) is listed at the top of the
Commands section above — set it before running such commands and re-apply it
when retrying network, DNS, registry, or module-download failures.

## Knowledge Base Workflow

The `knowledge/` directory is the project memory. It exists so a new AI session or human contributor can quickly understand where the project stands. Keep `knowledge/index.md` slim; do not append long changelogs there.

When starting work:

1. Read `knowledge/index.md` (Latest Handoff + Current Project State).
2. Read the relevant module note under `knowledge/modules/`.
3. Read **hot** handoffs under `knowledge/sessions/` (not the full archive).
4. For active programs, open the plan listed in `knowledge/plans/README.md`.
5. Read relevant decisions under `knowledge/decisions/` when changing architecture.
6. Use `knowledge/sessions/archive/` only to recover historical evidence—not as current status.

When finishing work:

1. Update the relevant `knowledge/modules/` note when a feature area changes.
2. Add or replace a short **hot** handoff under `knowledge/sessions/`; keep only
   actionable workstreams at the top level (see `knowledge/sessions/README.md`).
3. Point `knowledge/index.md` Latest Handoff at the new hot handoff; remove
   completed intermediate bullets instead of stacking history.
4. Add a decision record for important technical/product choices.
5. Update plan **Status** in the plan file and `knowledge/plans/README.md` when
   a task book completes, cancels, or is superseded.
6. After multi-day checkpoint spam, move intermediate sessions to
   `knowledge/sessions/archive/YYYY-MM/`.

Recommended handoff format:

```md
# YYYY-MM-DD Session Handoff

## Changed

- ...

## Decisions

- ...

## Next

- ...

## Open Questions

- ...
```

## Current Status

- Actively implementing core forum, identity/RBAC, admin, attachment,
  extension (plugin/theme), SEO, mail, moderation, and search systems. See
  `knowledge/index.md` for the authoritative, frequently-updated status.
- Stack is decided and in place: Nuxt 4/Vue 3/Nuxt UI/Bun frontend; Go Fiber
  v3, PostgreSQL, Redis backend (Meilisearch optional plugin); River durable queue; Goose
  migrations; sqlc; Redis-backed server sessions.
- The user manually starts the `apps/web` dev server (port 3000) during
  development. When port 3000 is occupied, assume it is the user's own
  running server — do not kill it without asking.
- Always read `knowledge/index.md` and the relevant `knowledge/modules/`
  note before changing sensitive areas, and update the knowledge base when
  finishing work (see Knowledge Base Workflow).
