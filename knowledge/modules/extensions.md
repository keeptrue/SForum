# Extensions Module

## Purpose

Owns installable plugins and themes, their exact-artifact trust, lifecycle,
runtime registries, provider slots, settings, admin surfaces, and recovery.
Core stays the host framework; deployment/vendor behavior belongs in plugins.

Plugins are multi-enable executable or declarative packages. Exactly one theme
is active. Public theme activation swaps Page Registry/L0/L1 runtime state and
does not rebuild Nuxt.

## Current Program State

- **Host coreVersion is exposed and manifest compatibility is enforced
  (2026-10-25).** `apps/api/version.Identity()` is the single authority
  (`coreVersion` = strict semver release identity, `buildVersion` = display
  label). It reaches v2 backend plugins through
  `HandshakeRequest.host_build` / `Server.HostBuild()`, admin page and settings
  bridges through `page-bootstrap`'s `host`, public L2 components through the
  descriptor's `coreVersion`, and ops through `GET /api/v1/health`. Manifest
  `sforumVersion` is now actually evaluated: install rejects with 422, enable /
  theme activation / upgrade / rollback / restart with 409, both carrying
  `extension.core_incompatible`. Built-in packages are exempt and an invalid
  core label skips enforcement with a startup warning; prerelease cores are also
  evaluated against their release base so alpha/beta releases do not reject
  every package. The Host API v1 `Ping` surface is *not* used for this: production
  never injects `SFORUM_HOST_API_*` into plugin subprocesses. See
  `../decisions/2026-10-25-core-version-surface.md`.

- **Every shipped package carries brand assets (2026-10-25).** 12 extensions
  (8 built-in plugins, the default theme, 3 independent-repository plugins) plus
  the main repository now ship `brand/icon.svg` (512 viewBox), `brand/icon.png`
  (512×512) and `brand/social-preview.png` (1280×640, Chinese primary card),
  generated from one template by `scripts/brand/` and read by each README's
  first screen. The contract is `docs/extensions/brand-assets.md`; the broader
  plugin repository standard is `docs/{zh-CN,en-US}/development/plugin-standard.md`.
  Enforcement is `sforum extension brand check` plus a Go test over
  `extensions/builtin/**` and `extensions/optional/**`. Brand files stay out of
  `packageFiles` (inert, not runtime artifacts) but remain inside the package
  tree digest, so adding them is a normal artifact change for the same version.
  No manifest `icon`/`socialPreview` field and no admin-list icon slot yet — the
  decision record explains why. See
  `../decisions/2026-10-25-extension-brand-assets.md`.

- **Protected built-in plugin auto-update is implemented (2026-10-23).**
  API startup runs `BuiltinPluginAutoUpdater` after domain services and the
  plugin runtime coordinator are ready. It promotes enabled
  `source=builtin` plugins with a newer staged SemVer through the existing
  `Upgrade`/`Restart` lifecycle paths, using a credential-less `builtin-auto`
  account bound to the `builtin_auto` system role and the `builtin` lifecycle
  authority (no trust grant). It skips disabled/installed plugins, downgrades,
  same-version digest drift, Safe Mode/recovery, open operations, and any
  legacy plugin currently selected as a provider (to avoid resetting
  mail/storage/search/notification selections). A Postgres advisory lock makes
  the pass single-owner across API nodes; failures emit
  `builtin_auto_update_failed` and never block boot. The admin switch is
  `extension.builtin_auto_update` (default enabled). See
  `../decisions/2026-10-23-builtin-plugin-auto-update.md`.

- **Plugin session state (S9) is complete and production-wired (2026-10-17).**
  The Gateway implements the S9 issuer that `ProtocolStarter` resolves by type
  assertion (before the fix, every scoped Host-initiated invocation failed at
  mint — tests had masked it by injecting the authority directly). The M9
  reference package `sforum-session-checkout-reference` exercises it through a
  compiled fixture binary over the real go-plugin transport, and the joined
  test `TestPluginSessionStateJoinedHostDispatchToStore` replaces the earlier
  stub. Erasure hooks have their own real-PostgreSQL (Identity) and lifecycle
  (Extensions) tests; the Identity matrix covers revoke / revoke-others /
  revoke-all / device-cap eviction / password reset with
  exactly-the-ended-sid erasure, best-effort semantics, and no-invalidator
  resilience. The privacy-erase entry is the `privacy:erase-user` CLI
  (`bootstrap.NewPrivacyRegistryForOutband` registers the Host-owned
  plugin-session-state contribution through the same path as the API assembly;
  Redis unreachable = named fail-closed). Anonymous plugin session state is
  unavailable by construction (decision D1). Session state's Redis keys follow
  `sforum:plugin-session:v1:installation:<id>:…`; erasure invalidates through
  per-session/per-extension index sets.
- **True two-node convergence verified on published images (2026-10-17, M10
  row 7).** Each node's boot lease (`plugin_runtime_nodes`: hostname + role +
  boot id, 45s lease / 10s heartbeat) and per-revision acknowledgements work in
  a real two-node topology sharing one PostgreSQL+Redis: an admin disable on
  node A converges on node B inside the 5s poll window (plugin subprocess
  drained), an enable revives it from the byte-identical immutable artifact
  path, a departing node's lease expires within 45s while the survivor serves
  on, and a rejoining node registers a fresh boot id and acks the latest
  revision immediately. Cold-start guidance (sequential first boot / migrate
  before parallel start) is in the release-image runbook after two measured
  first-boot races.

- Manifest V3, trust/recovery, lifecycle ledger, Host API v2, registry
  families, Page Registry themes, buildless settings UI, catalogs, and P0-P12
  phase gates are present.
- Manifest V3 admin pages may use `view: component` with a package-prebuilt
  `.mjs` entry and optional CSS. The Host retains the admin route, chrome,
  heading, authorization, exact-artifact trust, and failure quarantine while
  the plugin mounts only the page body. Settings and page components share one
  aggregate `adminFrontendDigest`; component IDs are package-unique and assets
  use component-specific immutable private URLs. Production still does not
  compile Vue SFCs or load Nuxt Layers.
- `GET /admin/extensions/{id}/page-bootstrap` now also returns `frontend` (the
  exact-artifact `FrontendStatus`) whenever the resolved page declares the
  `component` or `settings` view. The admin shell is SSR + no-store, and the
  trust state decides what the first frame may render, so leaving it to a
  post-hydration request made "trust not read yet" render as "trust not
  granted": the warning banner was written into the server HTML and replaced by
  the real component a moment later. The field is nullable and every failure
  (no trusted runtime wired, permission denied, unreadable trust store)
  degrades to `null` instead of failing the bootstrap; the Host frontend then
  falls back to its own `/frontend` request. `about` and undeclared pages do not
  pay for the lookup.
- `useAdminFrontendTrust` models trust as a tri-state (`resolved` separates
  "not read yet" from "read and not granted"), seeds once from that bootstrap
  value after verifying extension id and digest ownership, and no longer awaits
  `/trust` on the critical path — `/trust` only decides the authorization UI
  shape (V3 exact-artifact vs legacy grant table).
- `make:plugin --vue-admin-page` now turns that runtime contract into a complete
  Vue authoring loop: typed bridge adapter, Vite workspace, immediately valid
  placeholder dist, exact-artifact manifest entries, and build/digest/validate/
  test instructions. It works with simple or included manifests and can coexist
  with `--prebuilt-settings` without erasing its output.
- `extension build` is the beginner-facing author command for that loop. It
  optionally runs Bun in `frontend/admin`, then directly reuses the digest,
  runtime-template validation, and Host contract-test implementations. Upload,
  install, enable, and production runtime never execute package scripts.
- `@sforum/admin-sdk@1.0.0` and `@sforum/plugin-ui@1.0.0` are public npm
  packages. Their reviewed registry tarballs match the local release artifacts,
  and both packages trust only `zhuchunshu/SForum` workflow `release.yml` for
  OIDC `npm publish`. Interactive publication requires 2FA and bypass-2FA
  tokens are disallowed. The first interactive bootstrap versions have no
  provenance; the next SDK version published by Release must provide it.
- The shared storage provider SDK now bounds untrusted Protocol V2 chunk-size
  input before integer narrowing. Because shared runtime source is part of every
  protected built-in release identity, all seven plugin patch versions and the
  release baseline were advanced together; rebuilt staging artifacts pass
  exact digest refresh and `extension test`.
- `AttachmentStorageProviderCatalog` directly owns storage provider candidate
  and availability reads; aggregate `Service` and `CatalogService` forwarding
  methods were removed. Settings projection lives with settings lifecycle, and
  Manifest setting declarations live with the settings document contract; the
  corresponding architecture baselines were lowered.
- Custom image sticker contributions are in approved design, not
  implementation. Plugin authors will maintain conventional sticker
  directories; the extension CLI will generate one exact catalog and Manifest
  V3 will reference that catalog once rather than listing every image. See
  `../plans/2026-07-30-image-sticker-platform.md` and
  `../decisions/2026-07-30-image-sticker-catalog.md`.
- Built-in Shortcodes M0-M9 are complete; after independent M5 review, the
  protected `sforum-shortcodes` package (id `sforum-shortcodes`, version
  1.3.0) now ships eight
  `content.runtime@1` shortcode declarations
  (`sforum-shortcodes.user@1`, `sforum-shortcodes.category@1`,
  `sforum-shortcodes.friend-links@1`, `sforum-shortcodes.topic@1`,
  `sforum-shortcodes.comment@1`, `sforum-shortcodes.login@1`,
  `sforum-shortcodes.reply@1`, and `sforum-shortcodes.only-author@1`) with strict draft-07 schemas,
  Protocol V2 typed render handlers, localized labels, and zero public output
  for empty friend links. Each reference render does exactly one delegated
  Host query; the M4 projections stay the only data surface. Real-subprocess
  Host-chain integration tests, staging build (`build-builtin-plugins.sh`),
  the 8-plugin release baseline, and live SyncBuiltins/admin/Safe Mode/catalog
  evidence are in the M5 archive and current
  `../sessions/archive/2026-08/2026-08-23-builtin-shortcodes-m6-handoff.md`. The accepted ADR freezes two Host nodes, eight
  exact V1 declarations (including canonical `friend-links`), Markdown API
  conversion, typed limits, authorization, versioning, locale, cache, trace,
  and fallback boundaries. Core public Forum/Page Registry/Nuxt/cache
  projections are raw-free, uncached edit-source reads require Host mutation-
  equivalent authorization, and Core now owns both structured nodes, strict
  grammar/validation, shared Go/TypeScript conformance, deterministic fallback,
  and normalized hashes. Production now offers optional `content.runtime@1`,
  dispatches the exact active manifest declaration through the exact artifact
  runtime under a Host admission lease, and fails explicitly when invocation is
  absent. Plugin-side DTOs live in `sdk/plugin/v2` without importing Host
  Content Registry types. Lifecycle replay/remove covers disable, Safe Mode,
  restart, upgrade, and rollback. Legacy plugins without `manifest.lifecycle`
  now use the same exact Content Registry publication/quarantine boundary with
  rollback compensation, so runtime process and catalog artifact cannot split
  across compatibility enable/disable/restart. Forum fallback remains
  Host-owned. The shared
  SDK contract change patch-bumped all seven existing built-ins and refreshed
  the release baseline. Query Registry now publishes the exact seven sealed V1
  shortcode projections with a 32-ID bound and Host-owned visibility/actor
  SQL. Only the exact `sforum-shortcodes` content runtime receives their opaque
  one-use delegations; other content runtimes receive none, and RequestContext
  still exposes neither actor nor raw authority. The SDK validates the frozen
  seven-query set and supplies canonical ID filters through the existing
  delegated query client. M6's production per-node dispatcher binds only the
  five exact v1.1.0 declarations and fails closed across disable, crash,
  timeout, Safe Mode, visibility denial, and resource-key recursion. Canonical
  duplicates execute once in bounded batches; topic/comment cards reuse only
  the frozen M4 public projections, comments recheck owning-topic visibility,
  and generation-tagged reference caches are invalidated by authoritative
  resource and visibility mutations. The live immutable artifact digest is
  `70d43c0e81a515c22feb6477f3582d51b7d94fc3b1593587dfd4d17bdd3d00f8`.
  M7 adds one `sforum-shortcodes.editor.command.references@1` command and one
  `sforum-shortcodes.editor.toolbar.references@1` contribution through the
  existing trusted Editor Registry L2 catalog. The immutable editor module
  digest is `a3fe4c2594e7a5090fd90896b33d7c9fefe9c4461e49e2e2ba3f98c3864c15e5`;
  public asset delivery remains exact-artifact and editor-only, and empty
  `nodes`/`marks` serialize as canonical arrays so strict clients do not
  quarantine command-only modules. Normal admin activation, disable, L2 load
  failure, and Safe Mode fail closed without a second toolbar or untrusted
  business UI. Its exact catalog, raw-SSR output, no-source/private-data scans,
  and desktop/mobile evidence pass. See
  `../sessions/2026-08-28-builtin-shortcodes-m7-handoff.md`,
  `../decisions/2026-08-22-structured-shortcodes-public-source-secrecy.md` and
  `../plans/2026-08-22-builtin-shortcodes.md`.
- Forum-content presentation now follows the same plugin lifecycle. Manifest V3
  accepts exact `style` assets scoped to `core.surface.forum-content`; the
  provider-neutral `GET /extensions/runtime/content-styles` catalog publishes
  only enabled, trusted, healthy, digest-bound, same-origin blocking CSS and
  fails closed for Safe Mode, drift, invalid CSP, or script dependencies.
  Public Nuxt pages load the catalog with SRI while admin routes clear it.
  `sforum-shortcodes@1.3.3` owns its published, protected/fallback, editor
  NodeView, and reference-dialog CSS in
  `frontend/public/shortcodes.css`; Core owns no shortcode visual rules. See
  `../decisions/2026-08-29-plugin-owned-forum-content-styles.md`.
- M9 activates the three protected declarations through the Host Forum policy
  facade: `login` requires a current active actor; `reply` requires the topic
  author or a current public accepted comment on the same topic; and
  `only-author` is comment-only and limited to the comment or topic author.
  M4 `author_decisions` and `reply_eligibility` projections remain
  authoritative, so inactive, banned, anonymous, unrelated, staff, and
  super-admin viewers are denied on public pages. The exact
  `sforum-shortcodes@1.3.0` artifact includes strict schemas and typed
  Protocol V2 protected renderers that receive only an allowed decision and
  accepted fragment. Editor L2 reuses the existing shortcode menu and
  supports create, wrap, edit type, delete, unwrap, preview, publish, and
  re-edit for topic/comment scope; empty bodies and topic `only-author` nodes
  are rejected by Host validation. Protected timeout, crash, disable, Safe
  Mode, invalid-output, cache, and secret-marker tests remain closed. M10A
  (legacy conversion fixture `shortcode-legacy-conversion-v1.json`, the
  deterministic converted/literal/invalid/unsupported/over-limit report, and
  the bilingual user/operator plus Reference 6 authoring documentation) is
  complete. M10B reconciled parser parity and reran focused lifecycle/security
  gates, but live catalogs still expose active `1.2.0` while `1.3.0` is staged;
  normal super_admin activation remains the release blocker.
- Package installation accepts only Manifest V3. Executable packages accept
  only Protocol V2 with a valid Host API V2 declaration; V1 loaders, runtime
  adapters, SDK entry points, built-in artifacts, fixtures, and rollback paths
  have been removed before the first public release.
- All 18 tracked extension fixtures were re-audited against that baseline.
  Static packages now pass the same SDK `LoadAndTest` path used by the CLI as a
  single inventory gate; Page Registry templates, plugin page schemas, and
  prebuilt admin assets carry current exact `packageFiles` declarations. The
  nine executable reference packages pass their real Protocol V2 integration
  tests.
- The formal SEO ZIP lifecycle chain creates and migrates a disposable
  PostgreSQL database before publishing runtime revisions. Executable fixture
  package paths are temporary, so this test must never fall back to the shared
  development database and leave a boot revision pointing at deleted files.
- Executable process bootstrap is deliberately separate from application
  protocol negotiation: Host and SDK share the fixed HashiCorp go-plugin
  Bootstrap ABI v1 cookie, while only the post-launch gRPC application contract
  is Protocol V2. Cross-built compatibility coverage prevents those version
  axes from drifting together.
- **Operator-bound database grant provisioning (2026-10-12).** The extension
  database runtime lease has two issuers: host (no operator present — boot
  reconcile, recovery) which may only reuse an existing exact grant, and actor
  (operator-initiated enable/upgrade) which may self-provision it.
  `ProtocolStarter` previously hardcoded the host issuer, so no `own_schema`
  plugin could be enabled through the normal flow. The authority now travels on
  the start context (`extensions.WithDatabaseGrantAuthority`): the legacy
  `ActivationCoordinator.Start` mints one `extension.database_grant` audit row
  and attaches `{actor, auditEventID}` before `runtime.Start`, and the Lifecycle
  V2 host attaches the same authority from the durable lifecycle ledger
  (`LifecycleCoordinatorGateRequest`) before staging. An extension that only
  declares `database.grants: ["host_commands"]` gets no anchor — that is a
  command declaration, not a runtime lease. Failure to write the anchor degrades
  to host authority (and therefore fails closed on a missing grant) rather than
  creating an unattributable grant row. Revocation stays host-authorized. See
  `decisions/2026-10-12-extension-database-grant-authority.md`. Integration
  coverage today: `extension_database_raw_core_integration_test.go` and
  `protocol_database_lease_test.go` pin the lease-issuance authority matrix
  (actor-bound issuance, host issuance refused without an exact grant, forged
  artifact rejected, revoked grant refused). The end-to-end
  `ActivationCoordinator.Start` → provision pair that used the gamification
  plugin as its only subject was removed with that plugin on 2026-10-12 and is
  **not yet re-pointed at a reference fixture** — see
  `decisions/2026-10-12-remove-gamification-badges-growth.md` (known coverage
  gap, not a regression in behavior).
- Architecture debt M0-M12 is complete.
- The 28 public-contract and joined-runtime black-box test files now live in
  `Support/Extensions/IntegrationTests`; the legacy package root retains only
  one allowlisted external test helper needed by database-lease subprocess
  tests. Focused recursive test commands must use
  `go test ./app/Support/Extensions/...`.
- The legacy `Models/Extensions` facade delegates to Catalog, Lifecycle,
  Theme, and Settings collaborators. The runtime `Manager` delegates to
  RuntimeSupervisor, InstanceAdmission, RuntimeInvoker, and
  RuntimeEventsProviders. Both packages retain one mutable owner per state
  family and stay under their ratcheted file/receiver caps.
- M6 full gate and browser QA passed. M7 focused tests, typecheck, build,
  architecture validation, and V3 catalog validation passed.
- The 2026-07-31 release-blocker repair migrates historical `enc::` secret
  settings into SecretStore before any SettingsLifecycle read/write, passes
  SystemTier priority into the real plugin full-set start plan, and rejects
  CompatFarm RPC errors. The local full repository gate is green.
- RuntimeRollout no longer fabricates a healthy `api-local` acknowledgement.
  Until real node-bound health proof is wired before active publication, the
  ordinary extension lifecycle deliberately does not bind this post-terminal
  rollout hook. The real rollout gate and the Privacy operator surface remain
  open work rather than advertised release features.
- The Marketplace reserve is **gone** (2026-10-24): `app/Support/Marketplace`,
  its boot construction, `MARKETPLACE_ED25519_*` config, the `/extensions/store*`
  admin pages, and the placeholder shelf were deleted without a consumer ever
  existing. The app store is an out-of-tree product (`sforum-app-store`); core
  keeps only `install-from-url` and the host-mediated install action. See
  `../decisions/2026-10-24-app-store-out-of-tree.md`.
- **Trusted-admin-page install action (2026-10-24)**: `admin.pages[]` on a
  `view: component` page may declare `hostActions: ["extension.install"]`
  (`ManifestAdminPage.HostActions`, validated in `admin_micro_frontend.go`,
  schema mirrored in `manifest-v3.schema.json`). The page bridge then exposes
  optional `bridge.installExtension({url, sha256, expectedExtensionId,
  expectedType, expectedVersion?})`; it is `undefined` otherwise. Host flow:
  permission pre-check (`extension.plugin.manage` / `extension.theme.manage`) →
  Host confirmation dialog (URL host/full URL, SHA-256, declared identity,
  `super_admin` note, "Host verifies after download") → one
  `POST /admin/extensions/install-from-url` with `timeout: 120000` → result.
  Cancel sends nothing; one dialog/install per page (`install_in_progress`).
  The route gained optional `expectedExtensionId/expectedVersion/expectedType/
  source`; `InstallOrUpgradeArchive` compares them after `validateManifest`
  and **before** the type permission switch, so a mismatch reports
  `extension.expected_mismatch` (422, with the manifest's actual id/type/version)
  instead of a permission error. `mergeArchiveSourceMetadata` records a
  sanitized `sourceLabel` (`[a-z0-9._:-]`, ≤64). **Only staging**: no enable,
  trust, activate, or upgrade — mechanics are tested in
  `Models/Extensions/archive_expectations_test.go`,
  `ExtensionManifest/admin_host_actions_test.go`, the route tests, and the web
  suites `tests/admin/extensionHostInstall.test.ts` /
  `tests/admin/adminPageInstallDialog.test.ts`. No new capability, Host
  Command, intent table, or worker. Decisions:
  `../decisions/2026-10-24-admin-page-host-action.md`.
- **Install-action disclosure**: the plugin detail card, the theme detail card,
  and `SFAdminExtensionEnableDialog` all render
  `SFAdminExtensionHostActionsNotice`, which shares one i18n key
  (`admin.extensions.hostActions.*`) and reads the same manifest field the
  bridge uses. It is deliberately a separate line item, not part of capability
  risk levels.
- The generic `account-settings` navigation contract now carries through
  Manifest V3 lifecycle publication with exact runtime filtering. The Manifest V3
  contract is aligned end to end (2026-10-15): `navigation[].kind` accepts
  `account-settings` in the embedded JSON Schema, the Go platform validator, and
  the OpenAPI `ExtensionManifestNavigation` mirror, and navigation items may
  declare the optional `visibility` (`public|anonymous|authenticated`) and
  `ownerResource` fields that the Go struct and Navigation Registry already
  supported. Omitted or `public` visibility on an `account-settings` contribution
  is raised to `authenticated` by lifecycle publication, so the slot can never
  leak to guests; `href` is still limited to `/settings/*` or `/extensions/*`.
  Core also owns the `IdentityDelegation` and `ConsentBridge` capability
  boundaries for optional identity providers; these packages expose no raw Core
  database or session authority and are not OAuth-specific.
- Public roadmaps now label M3/M5/M6/M7 as prerelease residuals. P0-P12 phase
  checklist completion is not presented as stable production completion until
  those rows and the joined M8 gate close.
- Compatibility facades remain only for exact allowlisted consumers and
  tighten as those consumers migrate.
- Enabled Lifecycle V2 plugins now use Host-owned exact disable/enable
  orchestration after a settings mutation. The Host preflights both phases
  before changing the SettingsLifecycle document; delegated provider-settings
  permissions remain scoped to the owning auth/mail plugin. Preflight failure
  leaves settings untouched, while a post-persistence restart failure returns
  an explicit recovery error instead of a generic 500.
- Lifecycle registry compensation reverses source and target fences before
  reconciling durable Identity state. Backend-only plugins with zero page
  contributions are absent from Page Registry and ThemeRuntime; legacy startup
  registration clears stale state instead of creating a blank artifact that
  can never satisfy the Lifecycle V2 exact fence.
- The settings restart coordinator now lives with the existing settings
  lifecycle owner instead of adding another file to the legacy flat package;
  role-suggestion decisions likewise live in a focused Identity Registry file.
  Architecture ratchets remain non-growth constraints rather than being raised
  to accommodate these changes.
- The lifecycle page reconciliation loop now lives with exact page runtime
  staging, Protocol V2 provider invocation lives with provider-slot execution,
  and Page Registry operator binding management has its own focused file. The
  corresponding large-file ratchets were lowered after these extractions.
- Stable contracts now live in `Support/ExtensionRuntime`,
  `ExtensionProtocol`, `ExtensionDatabase`, and `ExtensionComposition`.
  Product Models cannot import the legacy runtime package. The legacy package
  retains named Manager, ProtocolStarter, Protocol V2 Host, lifecycle, and
  SDK/CLI consumers under an exact architecture allowlist.
  Decision: `../decisions/2026-07-28-extension-stable-package-boundaries.md`.
- Host icons are now a plugin-facing surface (2026-10-16, M1 of the plugin
  frontend experience program). `GET /_sforum/icons?names=…` (batch JSON, ≤32
  names) and `GET /_sforum/icons/{lucide|tabler}/{name}.svg` serve sanitized
  inline SVG generated from the already-bundled `@iconify-json` collections —
  no new dependency, and plugins never supply markup. `AdminPageBridgeV1.icons`
  and `PublicFrontendBridgeV1.icons` are optional additive members
  (`@sforum/admin-sdk@1.1.0`); both the admin page, admin settings, and public
  L2 hosts supply the shared client resolver in
  `apps/web/app/utils/icons/hostIcons.ts`. `@sforum/plugin-ui@1.1.0` adds
  `SPluginIcon`, `icon` props on button/section/alert/empty-state, table column
  icons, and `definePluginPage`/`mountPluginPage`, which inject the resolver so
  a hand-written `createApp` can no longer silently drop icons. All 8399
  bundled lucide/tabler icons (aliases included) sanitize losslessly; only
  fragment ids are namespaced per icon. Decision:
  `../decisions/2026-10-16-plugin-frontend-surface-contract.md`.
- Page regions have one hand-edited source and a generated web projection
  (2026-10-16, M2/M3 of the plugin frontend experience program).
  `RegionCatalog.pageRegionMatrix` now exposes 14 regions over 11 pages —
  `content_before`/`content_after`/`sidebar` plus `list_before`/`list_after`,
  `detail_before`/`detail_after`, `comments_before`/`comments_after`,
  `sidebar_top`/`sidebar_bottom`, `profile_header_after` — and
  `scripts/v3-catalog/generate.mjs` emits
  `apps/web/app/config/pageRegionCatalog.gen.ts` from it, replacing the
  hand-maintained whitelist in `usePageRegions.ts`.
  `apps/web/tests/framework/pageRegions.test.ts` compares the generated matrix
  against `<SFRegionOutlet>` occurrences in the host components in both
  directions, so a region cannot be declared without a real render site; that
  check exposed a pre-existing dead declaration for `forum.topic.reply` (a
  redirect placeholder) which was removed, and rejected `hero` and
  `composer_after` as non-distinct slots. `MAX_REGIONS` is derived from the
  generated catalog instead of a hardcoded 8. `NavigationRegistry` was
  deliberately **not** merged into this path: it composes navigation chrome,
  not page content. Decision:
  `../decisions/2026-10-16-plugin-frontend-surface-contract.md`.
- Extension authoring contracts frozen (2026-10-16, M0 of
  `../plans/2026-08-22-extension-authoring-speed-and-controlled-power.md`).
  Two premises were disproved by reading production code, and the corrections
  matter more than the milestone:
  (a) **Custom authorization guards are live, not closed.** Manifest admission
  accepts `kind: custom` (`app/Support/ExtensionManifest/v3_validate.go:106`),
  production assembly injects `NewRuntimePluginRouteGuardEvaluator`
  (`bootstrap/api_assembly_domains.go:618`), and
  `app/Http/plugin_guard_runtime.go:32` actually invokes plugin code behind exact
  artifact/version/digest/trust/Safe-Mode gates and a `RuntimeCallGuard` lease,
  failing closed unconditionally. The public docs
  (`docs/extensions/routes.md`, `docs/zh-CN/extensions/routes.md`,
  `docs/en-US/extensions/routes.md`, `docs/extensions/authoring-guide.md`)
  claimed the executable guard runtime is a later wave; **corrected 2026-10-16**
  (M4 closure pass), together with the exact gate list and the fail-closed
  failure modes.
  `custom` and `raw_request` are different states. **Correction:** an earlier
  revision of this note said "raw-session credential forwarding is genuinely
  still closed". It is not: `core.guard.raw_request` requires an empty plugin
  guard binding, `app/Http/plugin_guard_runtime.go:53-56` then returns allow
  without running any guard code, that step resolves to `RequestAuthorityRaw`
  (`app/Support/Routes/request_authority.go:189-191`), and
  `app/Support/Extensions/protocol_v2_request_authority.go:129-131` stops
  filtering credential headers in that mode, so `cookie` / `authorization` /
  `x-api-key` / `x-auth-token` reach the plugin handler.
  **Resolved 2026-10-16 (decision R1):** the operator judged this a
  vulnerability. `core.guard.raw_request` is now **refused at manifest
  validation** (`v3_validate.go` no longer lists it as an acceptable route guard)
  and every runtime mapping fails closed (`Routes/request_authority.go`,
  `Routes/prepare.go`, `Routes/core_guard_authorizer.go`,
  `Extensions/protocol_v2_request_authority.go`). Raw request authority is only
  reachable through a package-declared guard with `kind: raw_request`
  (`entry` + `digest` + `contractVersion`), which the Host discloses at trust
  time. Recorded as Revision 1 in
  `../decisions/2026-10-16-extension-authoring-contracts-freeze.md`.
  (b) **Descriptor contribution points are not content surfaces.** Only page
  regions mount L2 components (`SFExtensionWidget` is used in just
  `SFRegionOutlet.vue` and `SFThemeTemplate.vue`); every other point is a
  link/action/badge descriptor, so accepting `l2Widget` there would create a
  validated, documented, never-rendered capability.
  Also frozen: public plugin pages gain `layout: site|bare` with **`bare` as the
  backward-compatible default** (current plugin templates mount no global chrome,
  and artifacts are digest-bound, so defaulting to `site` would retroactively add
  chrome); M1 implements the shell by reusing
  `SFPageOutletRender.vue`/`SFHostPublicChrome.vue` on the plugin `action=add`
  path, which today bypasses them and hardcodes `max-w-4xl` with no chrome
  guarantee. Plugin session state and the dev-loop trust model are **not** frozen.
  Decision: `../decisions/2026-10-16-extension-authoring-contracts-freeze.md`;
  inventory:
  `../sessions/2026-10-16-m0-threat-and-inventory-report.md`.

- Extension authoring/shell closure pass (2026-10-16, second session; M1 contract
  complete, M2 partially blocked, M4 complete as re-scoped, `make:theme` fixed):
  - **Loader Gateway has no transport for Protocol V2 runtimes.** The gateway's
    only transport is a plugin loopback HTTP base
    (`app/Support/Pages/loader_gateway.go:98-108`, denied when empty by
    `bootstrap/app.go` `pageRouteTargetAdapter`), and Protocol V2 deliberately
    exposes none (`app/Support/Extensions/protocol_v2_client.go:463`; V1 is
    `ErrUnsupportedProtocol`). A `dataSource: plugin` page contribution therefore
    always falls back with `view_model_unavailable`. Pinned by
    `IntegrationTests/TestPageLoaderGatewayHasNoTransportForProtocolV2Runtime`
    (real V2 subprocess + real production target source + real gateway).
  - **Theme activation exactness.** A `components[]` entry whose
    `propsSchema` is a Host core page schema can never satisfy the component
    registry's exact-declaration rule
    (`app/Support/Extensions/provider_slot_schemas.go:59`), which is why a
    `make:theme` scaffold and the "minimal theme" fixture could not activate even
    though the CLI passed. Manifest validation now enforces the same rule
    (`app/Support/ExtensionManifest/v3_validate_ui.go`, components loop), the
    scaffold no longer declares theme components, and
    `docs/extensions/runtime-themes.md` documents what activation checks.
  - **Theme-authored public shell (Route A).** Layers 1-5 verified on the real
    path (`layout: site|bare` default `site`; `theme.json.shell`; `shellHtml` +
    exact `shellProvider`/`shellVersion`/`shellPackageDigest`; `sf-plugin-page-body`
    island registered on both API tables and both web maps). Layer 6 now asserts
    the composed shell carries exactly one navbar/footer/body island, that the
    geometry classes survive, that `bare` never receives a shell, and that a
    declared-but-unreadable shell fails open to the Host shell.
  - **Footer ownership, measured and fixed.** Footer visibility is owned by the
    **Host** stylesheet `apps/web/app/assets/css/sforum-theme.css`, not by the
    theme: it suppresses the default theme's page-level shell footer because a
    theme page's visible footer comes from `.sforum-content-footer` inside the
    content column, and a Page Registry `add` page has no content column - so
    plugin pages had no visible footer at all. A complementary
    `:not(:has(.sforum-content-footer))` rule now shows the shell footer for
    plugin pages while theme pages keep exactly one visible owner. Verified in a
    real headless Chrome over CDP at 390x844 (dpr 3) and 1440x900 (dpr 1):
    exactly one visible navbar and one visible footer on plugin pages, none on a
    `bare` page, no horizontal overflow, no clipped controls, no page console
    errors. Active theme artifact moved `4691a605…` -> `85ddda6a…` through
    rebuild + admin reactivation, and the source tree matches the staged copy.
  - The shell's `data-*` markers (`data-theme-region`, `data-theme-owned`,
    `data-page`, `data-theme-shell`) are stripped from `shellHtml` by the legacy
    L1 string sanitizer (`LoadTemplate` -> `SanitizeTemplateHTML`); classes and
    the `SFThemeTemplate` wrapper's `data-extension-id` survive, which is what
    the built-in theme's CSS keys on.

- Release image smoke (2026-10-16, M10 row 9): the candidate api, migrate and web
  images build (`--target api|migrate -f apps/api/Dockerfile`, `--target prod -f
  apps/web/Dockerfile`); the migrate image fills an empty database, the api image
  serves `/api/v1/health` 200 under `APP_ENV=production`, and the web image serves
  `/` 200 with live SSR — stopping the api container makes that request fail with no
  response, which is the control that proves the composition. Against a **seeded**
  isolated database the images serve real content: `/` 200 with 8 seeded topic links
  and 4 seeded category links, plus `/c/<slug>` and `/t/<id>/<slug>` pages. The image's
  boot also syncs its protected built-ins into the database (8 plugins + 1 theme) and
  starts their runtimes from immutable digest paths; the enabled search plugin indexes
  the seeded topics. Uploaded packages install (201, inert) and enable (200) through
  the same images, and their public pages render through the web image with the layout
  contract intact. Executable trust confirmation applies only to packages that ship an
  executable artifact: for such a package the release image issues a 64-char
  exact-artifact challenge (5-minute TTL, `sforum.trust-impact@2` impact payload),
  refuses enable without it (`extension.trust_challenge_required`), enables with it and
  starts the backend from the immutable digest path, and refuses a consumed token after
  `DELETE …/trust` (`extension.trust_challenge_replayed`). The grant itself persists per
  exact artifact, so a disable/enable cycle needs no new confirmation. Production
  requires absolute `EXTENSION_ROOT` (compose and the image set
  `/var/lib/sforum/extensions`), plus the documented secrets; a missing or relative
  `EXTENSION_ROOT` now fails with a named config error instead of
  `mkdir /storage: permission denied`.
- Runtime-missing and disabled-owner behaviour (2026-10-16, A2): already covered by
  `TestResolvePathReportsLayoutRegardlessOfBodyRender` (layout independent of body
  render), `TestLoaderUnavailableWhenPluginRuntimeMissing` (page resolves with a
  `loaderError`; the always-path for V2 plugins), the lifecycle page publication
  tests (exact empty publication clears pages, removal is fenced by exact artifact
  and runtime instance), and `TestResolvePath404`. No synonymous test was added.
- Theme activation rejection diagnosis (2026-10-16):
  `knowledge/runbooks/2026-10-16-theme-activation-rejection-diagnosis.md` maps the
  failing preflight layer (page / component registry / asset registry) to its family,
  remedy and durability. Both state-dependent paths are observed in tests: a third
  theme runtime outside the transition fence, and a stale third-theme asset
  publication.
- Theme activation preflight (2026-10-16): page coverage is **not** checked. The
  page preflight validates package loading, skin CSS, template and shell
  loadability, contribution normalization and path-signature conflicts only. The
  state-dependent rejections are the component-registry transition fence and the
  asset-publication transition; both are usually leftover state, and the
  `extension.build_failed` copy now names that family in both locales.
  See `decisions/2026-10-16-theme-activation-preflight-truth.md`.
- Host approval of the protected default theme (2026-10-23): a fresh install
  used to render the Core fallback until an admin re-activated the shipped
  theme, because Core replacement approval was read only from
  `page_provider_bindings.approved_by` (FK to `users`) and a new database has
  no users. Approval provenance is now explicit
  (`approval_source = admin | host` on `page_provider_bindings` and
  `theme_runtime_publications`); only the protected built-in default theme can
  be `host`-approved, only on startup-repair/legacy-genesis activations, and a
  staged new digest still needs one explicit admin activation. Runtime proof:
  empty database → one `startup_repair` revision with `approval_source=host`,
  34 host bindings, `/pages/resolve?id=forum.home` →
  `provider=sforum.default-theme`. See
  `decisions/2026-10-23-host-approved-default-theme.md`.
- Route A composition check (2026-10-16): `tests/registry-page-shell-runtime-evidence.mjs`
  is the persistent runtime check for the public shell path — theme shell vs host
  fallback vs explicit bare, exactly one navbar owner and one footer, layout vs shell
  source attributes, and shell identity agreement with `/site/active-theme/skin`.
  It skips when the dev servers are down unless `SFORUM_EVIDENCE_REQUIRE_LIVE=1`.
  The theme-switch procedure is `knowledge/runbooks/2026-10-16-theme-switch-shell-verification.md`.
  Note: `bun test` cannot render `.vue` components (no SFC transform), so component
  composition is proven by runtime evidence plus the source-contract tests.
- Release-gate status (2026-10-16, M10): the joined acceptance matrix lives in
  `knowledge/sessions/2026-10-16-m10-joined-release-gate-matrix.md`. Evidenced:
  compatibility farm (`./scripts/test-api-isolated.sh --compat` → ok) and APILTS
  contract statuses; SSR and browser chrome counts for `site`/`bare`/default at the
  carrier's 500px width; selected provider/digest and shell provider/digest from
  `/pages/resolve-path` agreeing with `/site/active-theme/skin`. Blocked: plugin
  body loader data (transport decision) and plugin session state (no contract).
- Author documentation (2026-10-16, M9): `docs/extensions/migrating-from-sforum-v2.md`
  is the canonical SForum-old → V3 mapping (Controller → `routes[]`, middleware →
  `before`/`after` route patches with `core.guard.inherit`, Blade → Page Registry
  three-way templates, `Itf` hook → `hooks[]` + typed events/queries/providers,
  helpers → Protocol V2 SDK), the closed-operation list (raw Fiber, Core session
  mutation, arbitrary Nuxt Layer, pre-plugin middleware, actor minting, client-IP
  claims), and verified quick starts. There is **no** session contract and guards
  have **no** challenge outcome — both are recorded as contract gaps.
- Build child processes (2026-10-16, M8): `runExtensionBuildProcess` runs every
  build child (`bun install`, `bun run build`) in its **own process group** and
  kills the group when the context is cancelled, so grandchildren cannot survive
  an interrupted loop. `extension dev` also accepts `--skip-install` to reuse
  existing frontend dependencies.
- Built-in discovery (2026-10-16, M8/ADR 6.2.5): `sforum extension builtins
  [--root <dir>] [--json]` is the single source of truth for the built-in package
  list; `scripts/build-builtin-plugins.sh` consumes it and hardcodes no id or
  directory. Discovery reads manifest metadata (not `LoadPackage`, because the
  protected source tree has no backend binary) and reports the Go-backend flag so
  themes and frontend-only packages are skipped. The staging root override must
  stay at the same directory depth as `storage/builtin-dev` because built-in
  `go.mod` files use a relative `replace`.
- Browser acceptance modes (2026-10-16, M10 row 8): the 390x844 harness also runs
  with `SFORUM_DISABLE_JS=1`, which disables script execution over CDP and asserts
  that the chrome and page body still come from the server render (one navbar and one
  footer on `site`/`default`, zero on `bare`, no overflow, SSR body text present).
- Public page body source (2026-10-16, adjacent plan M6 stages 1-2): a public page
  declared in `theme.json` may set `view: component` with `component.id`, meaning the
  body is mounted from a public L2 component declared in `manifest.components[]`. The
  verdict is validated once in `LoadThemePackage` (shared by the CLI and runtime
  activation), carried through `PageContribution` → `ResolvedPage` → the resolve
  response, and mounted by the reviewed body exit (`SFRegistryPageBody.vue` →
  `SFExtensionWidget`, i.e. the descriptor endpoint + release loader + quarantine and
  lease). A component page is exempt from the "no template ⇒ runtime unavailable"
  fallback rule; `view: template` (default) behaves exactly as before.
- Modifier phase order (2026-10-16, M10 row 3): a single plan-order list decides the
  chain — `global → before → filter → wrap → handler → after` — with exactly one
  handler step. `global_middleware` is exempt from method matching, so it appears in
  both safe and unsafe plans, while `before`/`filter`/`wrap`/`after` appear only in
  the plan for the method they declare; a modifier's `targetId` must match its own
  method or the registry rejects the input.
- Evidence-harness self-check (2026-10-16): the three browser/runtime evidence
  scripts only claim success when they actually measured something; a SKIP path
  prints `skipped: no assertion ran` instead of a success line (a false positive was
  found and fixed), and `SFORUM_EVIDENCE_REQUIRE_LIVE=1` turns a skip into a failure.
- Development scaffold roots are refused in production (2026-10-16):
  `config.DevelopmentScaffoldRoot` classifies `…/extensions/dev…` and
  `validateProductionSecrets` panics on such an `EXTERNAL_EXTENSION_ROOTS` entry, so the
  "never scan the scaffold tree" rule (ADR 6.2.6) is enforced at load time instead of
  being a convention. Covered by `TestProductionRejectsDevelopmentScaffoldExternalRoots`
  (refused and not-misclassified cases).
- Public page runtime surface catalog (2026-10-16):
  `docs/extensions/v3/catalogs/public-page-surfaces.{json,md}` is generated from
  `Pages.coreCatalog`, `RegionCatalog.pageRegionMatrix`, the `PageView`/`PageLayout`
  vocabularies and `allowedHostIslands`, so authors can see per page which body views
  and layouts are legal, which regions exist and which Host islands a theme may mount.
  `tests/validate-public-page-surface-catalog.mjs` (in `scripts/ci/contracts-gate.sh`)
  pins the vocabularies and the two-way page-set equality with the Go catalog.
- `--schema-page` author tooling (2026-10-16): the generator emits a declarative
  `view: schema` public page (bounded descriptor + package JSON Schema contract, both
  `kind: schema` with a version), rejects unknown kinds / unsafe slugs / a slug shared
  with `--public-page` before writing, and passes the shared CLI+activation preflight
  without hand-editing. Bilingual CLI docs and the authoring guide document the bounds
  and the read-only `form` limitation.
- Component package runtime convergence (2026-10-16): the legacy enable/disable paths
  now publish/remove the component package runtime
  (`ComponentRegistry.PublishComponentPackageRuntime` /
  `RemoveComponentPackageRuntime`, instance id derived in the Support layer), so a package
  whose only executable surface is a public L2 component becomes reachable immediately
  after enable instead of only after the next Host start. Failures are recorded as events
  and degrade to the previous behaviour rather than failing the enable.
- Admin activation automation (2026-10-16, M8): `tests/admin-extension-activation.sh`
  drives the approved flow (install → trust challenge → enable) over HTTP with a
  super_admin session, requires the confirmation token for executable packages,
  accepts `trust_not_required` for packages that ship no executable artifact, and can
  uninstall afterwards.
- Dev-loop failure scenarios (2026-10-16, M10 row 6):
  `tests/dev-loop-failure-scenarios.sh` exercises the real loop against a package
  failure, a frontend compile failure, an API restart, and SIGINT, asserting that
  failures keep the last healthy digest, the loop survives, and the restart probe
  leaves `catalog.go` byte-identical.
- Development loop (2026-10-16, M8): `sforum extension dev <package-root>` watches
  package-contained source inputs and runs one build transaction at a time by
  calling `runExtensionBuild` (digest refresh + validate + contract tests) and
  then `extensionpackage.DigestTree`. It is gated on
  `APP_ENV=development` through the same predicate as the DevHygiene reaper, is
  refused for protected built-in trees, never bypasses exact-artifact trust (each
  cycle prints the new digest plus a `reload_required` next step), and keeps the
  last healthy artifact on failure. `fsnotify` is a direct module dependency.
  Rebuild loops are prevented by a per-file **content stamp** snapshot re-taken
  after every transaction, because the transaction itself rewrites the manifest.
- Dev trust sessions (2026-10-17): session-level dev auto-trust closes the M8
  manual-confirmation gap. super_admin confirms once via
  `POST /admin/extensions/dev-trust-sessions` (`confirm: "true"`, per-package
  single active session, TTL 8h default / 24h cap); `sforum extension dev
  --auto-trust` then calls `POST /admin/extensions/:id/dev-trust-refresh` per
  build, which writes an idempotent exact live grant through the existing
  `EnsureLiveGrant` (per-digest audit) when an active session, matching actor,
  matching digest, non-builtin source, development env, and non-Safe-Mode all
  hold — otherwise `ErrTrustChallengeRequired`, byte-identical to the manual
  path. Production assembly never injects the service (routes 503 fail closed);
  session creation additionally requires the package root under `extensions/dev`
  (`devTrustRootAllowed`). Safe Mode startup revokes all sessions one-shot
  (`RevokeAllOnSafeMode`); revoked sessions never resurrect. Store:
  `extension_dev_trust_sessions` (unique partial index on
  `revoked_at IS NULL` only — partial index predicates must be IMMUTABLE, so
  expiry is enforced by query predicates plus create-time recycling, never
  `now()` in the index). Admin UI list + revoke and CLI
  `extension dev-trust create/list/revoke` share the
  `SFORUM_API_BASE`/`SFORUM_SESSION_COOKIE` conventions of
  `tests/admin-extension-activation.sh`. Routes: 4 new reviewed identities
  (363 → 367). Decision: `../decisions/2026-10-17-dev-trust-session.md`.
- Dev trust sessions R2 — external source roots (2026-10-17, same day): the
  session key is now the **extension ID** (server-derived; the client-sent slug
  is ignored and `rootPath` is audit metadata only), and the root allowlist is
  config-driven: `WithDevSourceRoots(cfg.ExternalExtensionRoots)` (development
  assembly only) ∪ the scaffold default `extensions/dev` tree, matched
  lexically via `filepath.Rel` — no symlink resolution (the snapshot pipeline
  rejects symlinks anyway). `Create` reads the manifest under `rootPath` and
  refuses unless its ID matches the extension, so the package root must be
  reachable from the API (same host or mounted). `GrantDevArtifact` now
  prefers the **staged** artifact (`StagedArtifact`, non-disabled) when
  building the TrustImpact, fixing the mismatch where `SyncExternalSources`
  stores content-addressed snapshots under `EXTENSION_ROOT` and dev-loop
  digests only ever enter staged — the grant now matches the artifact the
  subsequent upgrade consumes. Tests cover external-root acceptance, unknown
  root refusal, manifest-ID mismatch, and staged-over-active granting.
- Dev live-activation chain (2026-10-17): closes the remaining manual gaps in
  the dev loop so an external plugin repo behaves like a watched scaffold:
  ① the CLI (`extension dev`, `dev-trust create`) now accepts packages under
  `EXTERNAL_EXTENSION_ROOTS` (`devExternalRoots`), aligning with the R2 server
  allowlist instead of being stricter than it; protected built-in trees are
  still refused. ② `GrantDevArtifact` runs `SyncExternalSources` **before**
  reading the artifact identity (optional `WithSourceSyncer`, development
  assembly only) — a freshly rebuilt digest is staged into the running API
  without an API restart, and sync failures surface their real reason instead
  of folding into `trust_challenge_required`. ③ `--auto-upgrade` (implies
  `--auto-trust`) lets the loop call the existing `POST /upgrade` route after
  the trust refresh, so the full chain is save → build/digest/validate/test →
  stage → trust → upgrade with the plugin subprocess restarting at the new
  digest; failure phases (`dev_trust_failed`/`auto_upgrade_failed`) never stop
  the loop. ④ `extension digest --write` now recomputes `assets[].integrity`
  (SRI) for assets that already declare it — stale SRI after CSS/JS edits no
  longer requires hand scripts (prepare-package.sh-style), while undeclared
  integrity stays untouched so package baselines do not churn. Verified live on
  the external `sforum-oauth2-provider` repo: external root accepted, one build
- Dev zero-command activation (2026-10-17, superseding the wrapper flow): the
  host dev assembly runs its own fsnotify watcher over `EXTERNAL_EXTENSION_ROOTS`
  and a `DevAutoRuntime` that performs sync → exact per-digest grant → enable /
  upgrade with a credential-less `dev-auto` system account (real UserID for the
  V2 authority chain; no password credential, cannot log in). Fresh databases
  with zero admins work; production and Safe Mode never assemble the watcher
  (fail closed); install/enable/upgrade HTTP routes keep their super_admin
  gates. The wrapper script and CLI credential layers were removed. Decision:
  `../decisions/2026-10-17-dev-zero-command.md`. The dev pipeline also builds
  `frontend/admin` (bun run build when package.json exists) and recomputes
  declared `assets[].integrity` SRI during digest. Dev upgrades use deterministic
  Idempotency-Keys (`dev-auto/<id>/<digest>`), auto-recover one-shot from a
  failed-upgrade fence, and record promote failures in `extension_events`;
  startup reconcile runs inside the watcher goroutine (serialized with the build
  so `SyncExternalSources` never snapshots a half-written `backend/plugin`).
  **contractVersion auto-bump and asset republish were both added then REMOVED
  (2026-10-18)**: editing content (CSS/JS/backend/settings/routes) does not touch
  component declarations, so the registry rule ("same contractVersion forbids
  declaration change") never fires and no bump is needed; only a real component
  interface change (props/schema shape, different `l2Component`, added/removed
  id) needs a manual bump — the correct human checkpoint. Auto-bumping (in-memory
  view rewrite, then source-manifest write) fought the exact-artifact system at
  four layers (trust impact / authority snapshot / route-schema byte-compare /
  component registry) and was dropped. `RepublishPublicAssetsForDev` was removed
  because `bindAssetRegistryConsumers` makes `frontendService.publicAssets` the
  SAME registry the lifecycle writes via fenced `ReplaceAllIfRevision`; the
  unfenced `ReplaceAll` republish was a second writer that bumped the revision
  and broke the next upgrade ("asset registry revision changed during
  replacement"). The lifecycle upgrade publishes public assets itself.
  Agent contract: the `AGENTS.md` plugin-development rule mandates this
  zero-command chain (never restart the dev API to stage, never wrap iteration
  in a credential/auth step, never hand-roll SRI scripts, never automate the
  contractVersion bump). There is NO wrapper script and NO credential to
  declare — the host watcher does everything on file save; the old
  `scripts/dev-plugin-loop.sh` and the `extension dev-trust` credential flow are
  retired (dev-trust remains only as a legacy credential-gated helper). The
  bilingual handbook `docs/extensions/build-and-load.md` + `docs/zh-CN` teach the
  chain and when a manual contractVersion bump is genuinely required. Decision:
  `../decisions/2026-10-17-dev-zero-command.md`.
- Dev artifact self-heal and give-up rules (2026-10-19): stage idempotency now
  requires the recorded `package_path` to equal the immutable snapshot root that
  `SnapshotExternalSourceOwned` just verified with `DigestTree`, and
  `ensureExtensionVersion` repairs `package_path` for an existing
  `(extension_id, version, package_digest)` row, so a deleted artifact directory
  or a changed `EXTENSION_ROOT` no longer parks a row in permanent `missing`.
  `DevAutoRuntime` only auto-recovers a failed upgrade whose `PackageDigest`
  equals the current staged digest, refuses when `stagedArtifactUsable` says the
  frozen artifact cannot load, and gives up after
  `devAutoMaxFailedUpgradeAttempts` (3) attempts, writing one `dev_auto_skipped`
  event per artifact instead of pushing the operation to attempt=98. Lifecycle
  summaries expose `closed` (`completed_at != nil`) as the single terminal
  predicate — `state` is only the last phase (`uninstall` ends in
  `uninstalling`, `disable` in `draining`), which is why the CLI's
  already-closed error now prints `state`/`terminal`/`completedAt`. See
  `../sessions/2026-10-19-dev-artifact-self-heal.md`.
- Request-context contract (2026-10-16, M7): no new request/response field was
  admitted. The filtered header projection (with `x-sforum-*`, hop-by-hop and
  credential blocking, plus `httpguts` name/value validation),
  `locale`/`trace`/`deadline`/`idempotency_key`, Host-validated response headers
  and lossless query values already cover every candidate with a real consumer;
  client IP/proxy classification, UA/device summary, plugin-owned response
  cookies, redirect/cache/rate-limit hints are rejected with named evidence
  (`decisions/2026-10-16-m7-narrow-typed-request-conveniences-contract.md`).
  Proxy/client identity headers are forwarded as the client's own claim and the
  Host vouches for none of them.
- Durable route runtime incidents (2026-10-16, M5): the crash-loop quarantine
  ledger is readable at `GET /api/v1/admin/extensions/route-incidents` (bounded
  to 200 rows, redacted view + status summary, `extension.view` or
  `extension.manage`), and the Route Inspector renders it with loading / error /
  stale / empty / quarantined / recovery states. A new read route must also enter
  `app/Http/core_guard_authorizer.go` `requireExtensionsReadAuthority` — without
  that entry the production authorizer answers "guard unavailable" at runtime.
  The incident table is append-only (`resolve_once` / `no_truncate` triggers) and
  each row must be backed by an audit event with action
  `routes.runtime_incident` carrying the matching `incidentKey`.
- Guard authorization disclosure (2026-10-16, M5): the trust status read path
  exposes `guardAuthorization` (`TrustGuardAuthorizationView`) and every Route
  Inspector step exposes `guardRisk` / `guardRequiresTrust`, joining route scope,
  guard kind/entry/digest/permissions, executable digests, fallback, and the
  declared mutable fields, with the risk vocabulary `host_owned` /
  `extension_code` / `raw_request` from
  `app/Support/ExtensionManifest/guard_risk.go`. This is a **derived** view: it
  must not be moved into `TrustImpact`, whose digest is the live trust-grant
  identity. Admin review dialogs render it with a raw-request warning.
- M3 authoring scaffolds (2026-10-16): `make:plugin` / `make:theme` gained
  `--public-page`, `--public-page-layout site|bare`, and `--route-middleware`
  through one composed generator path
  (`apps/api/cmd/sforum/generator_authoring_workflow.go`). Generated public pages
  are static and honour the three-way identity contract; generated middleware
  inherits the target guard and declares only its own mutable pointers, never raw
  request authority. Interactive forms expose the same options. Scaffolds pass
  `extension build --allow-scaffold`, validate, contract test, and exact-template
  runtime preflight without hand-editing, and a generated theme was additionally
  packaged, activated and rendered in the dev instance (390x844 in real Chrome):
  its generated `layout: bare` page renders with zero chrome, its own home
  template owns exactly one navbar and one footer. Two scaffold defects found by
  that render are fixed: the theme home template previously mounted no chrome,
  and an island tag inside a template HTML comment breaks the exact-template
  preflight (the island extractor scans comments).

Authoritative sources:

- Architecture: `../decisions/2026-07-13-trusted-plugin-theme-platform-v3.md`
- Parent task book:
  `../plans/archive/2026-07/2026-07-28-architecture-boundary-debt-repayment.md`
- Progress/residual ledger:
  `../plans/archive/2026-07/2026-07-28-architecture-boundary-debt-repayment.md`
- Production remediation:
  `../plans/2026-07-22-v3-production-rewire-honesty-remediation.md`
- Generated traceability: `../../docs/extensions/v3/catalogs/traceability.md`
- Manifest/protocol decision:
  `../decisions/2026-07-29-manifest-v3-protocol-v2-only.md`

## Production Remediation Status

Do not close residuals from Support-only tests. Require the production
bootstrap binding plus durable/restart/multi-node evidence defined by the
remediation plan.

| Finding | Status | Plan milestone |
| --- | --- | --- |
| Legacy `enc::` values can be dropped | **closed** by production binding + PostgreSQL first-write regression | M1 |
| Production Marketplace key missing from deploy inputs | **obsolete** — the Marketplace reserve and its key were deleted (2026-10-24); production no longer requires the key | M2 |
| Runtime rollout used fictional `api-local` after activation | fictional Ack removed and unsafe post-terminal binding disabled; real gate still **open** | M3 |
| SystemTier order discarded before startup | **closed**; real full-set starter consumes priority order | M4 |
| Marketplace/Privacy lack real product consumers | Marketplace **removed** as dead reserve code; Privacy has a real erase consumer, its operator surface still **open** | M5 |
| CompatFarm could soft-pass RPC errors and runs a narrow/repeated matrix | RPC soft-pass **closed**; matrix and single-run residual **open** | M6 |
| Commerce uses Dispatcher only for `add` | **open** | M7 |
| Full gates/catalog/web residual | local gates and admin overview QA **closed**; release-image and Page Registry live smoke pending | M8 |

Prior partial evidence, not closure:
`../sessions/2026-07-22-p11-p12-p13-production-rewire-handoff.md`.

## Package Sources And Storage

- Protected built-ins are discovered only under `extensions/builtin/` and are
  boot-synchronized through `SyncBuiltins`.
- Optional ship-with-repository packages live under the tracked
  `extensions/optional/` tree. The directory README is part of the repository;
  only generated plugin backend binaries and package archives are ignored.
- A protected built-in removed from the release tree is atomically removed
  from the latest plugin runtime desired set, disabled, and hidden behind a
  Host catalog tombstone. Immutable extension/version rows referenced by
  historical publications are retained. If the same built-in ID ships again,
  synchronization clears the tombstone but does not silently re-enable it.
- Built-in source, staged immutable version, and active immutable version are
  distinct states. A theme source edit reaches runtime only after built-ins are
  rebuilt, the API restarts and stages the new digest, and an authorized admin
  activates that exact version. Browser QA must inspect the resolved
  provider/digest; checking repository templates alone cannot prove activation.
- Both protected built-in themes declare every `theme.json` page template as an
  exact Manifest V3 `templates` + `packageFiles` pair. The CLI regression gate
  validates both source packages so a page mapping cannot ship without its
  immutable template identity and digest. The source-to-activation workflow is
  now a repository rule and is documented in `extensions/README.md` and the
  extension authoring/CLI guides.
- Uploaded packages are immutable snapshots under `EXTENSION_ROOT`; they are
  separate from public attachments.
- The admin extension folder now exposes a dedicated **Install extension** page
  (`/extensions/install`). It accepts a drag/drop or file-picked ZIP and an
  HTTPS URL pull (`POST /admin/extensions/install-from-url`). URL installs use
  the shared SSRF-safe outbound client: HTTPS only, public addresses only, no
  userinfo, at most three redirects, a 90-second timeout, and the same 50 MB
  archive ceiling as uploads. An optional `sha256` pins the downloaded ZIP
  bytes and is checked before the package is stored. Authorization is evaluated
  before the outbound request, and both install paths record `sourceUrl`
  (userinfo/query/fragment stripped) plus `sourceDigest` in the install audit.
  URL installs are inert exactly like uploads: no enable, activation, runtime
  start, or trust grant.
- `EXTERNAL_EXTENSION_ROOTS` accepts comma-separated collection roots whose
  children are `plugins/*` and `themes/*`. The API scans them after protected
  built-ins.
- External discovery reuses the manifest loader and canonical snapshotter. New
  packages are inert `source=uploaded` installs. Changed packages become staged
  versions; discovery never promotes, trusts, enables, or selects them.
- Invalid roots/packages and duplicate IDs produce structured diagnostics.
  Store/database uncertainty is fatal so boot cannot reconcile against unknown
  lifecycle state.
- Removing a source directory does not uninstall its stored snapshot.
- Admin list/detail responses derive `artifactState` from the active immutable
  package path. A missing package remains visible for operator diagnosis and
  retained lifecycle history, but enable, theme activation, settings
  read/write/reset, settings actions, and runtime restart fail closed. Admin
  plugin/theme/overview pages label the record as artifact-missing and exclude
  it from the settings catalog.
- Super admins can explicitly batch-uninstall uploaded, deletable, disabled
  extensions whose exact package paths remain missing. The Host validates the
  whole batch and records catalog tombstones in one transaction without
  invoking unavailable extension code. Operators choose whether to retain or
  delete Host-owned settings; lifecycle history and plugin-owned business data
  remain in both modes. Re-uploading the same extension ID clears its tombstone
  and restores its catalog identity.
- A missing artifact's per-row uninstall action uses that same tombstone cleanup
  with only the selected extension ID. Ordinary lifecycle uninstall rejects a
  missing package before runtime dispatch, so immutable runtime-publication
  history cannot produce a misleading success followed by catalog reappearance.
- Containers must mount every external collection read-only into the API.

Decision: `../decisions/2026-07-22-external-extension-source-roots.md`.

## Trust And Lifecycle Boundaries

- Static install validates, previews, and stores an inert package. It never
  executes package code, migrations, frontend imports, or external effects.
- First executable enable requires a one-use, actor-bound `super_admin`
  challenge over the exact digest and complete canonical impact document.
- Grants bind package, backend, admin frontend, migrations, declarations,
  authorities, Host/Frontend contracts, dependencies, and runtime epoch.
- Uploading a new digest stages a candidate; it does not replace active bytes,
  inherit trust, stop the active process, or change provider selection.
- Lifecycle operations use a durable PostgreSQL operation/step ledger with CAS,
  checkpoints, attempts, actor/audit snapshots, and restart recovery.
- Plugin restart is a dedicated Host workflow (`POST
  /admin/extensions/{id}/restart`), not an alias for enable. It preflights the
  exact target and any capability/trust confirmation before downtime, fully
  disables the current runtime, and then re-enables it with deterministic
  phase idempotency keys. A legacy active artifact with a staged Lifecycle V2
  target is bridged through disable, exact staged CAS promotion, and V2 enable;
  a failed bridge remains disabled and can resume the same exact target. Trust
  status and challenge calls use `target=staged` during this recovery so the
  token cannot bind the older active digest; the admin UI keeps this
  disabled-plus-staged state restartable.
  Protected built-ins and declarative artifacts return a normal trust preview
  with `trustRequired=false` even when the uploaded-artifact trust migration
  gate is off.
- Disable/upgrade closes admission before draining routes, services, jobs, and
  schedules. Recovery supports retry, safe skip where declared, rollback, and
  out-of-band CLI disable. Initial API plugin convergence failure enters a
  Host-owned recovery-only HTTP mode: health remains live, readiness reports
  the immutable desired revision/artifacts, and every product route returns
  `503` without changing active or staged database authority. The recovery
  readiness and guard paths do not re-read the failed extension catalog, so a
  malformed or incompatible artifact cannot block the fallback HTTP surface a
  second time.
- Orphan lifecycle operations (an executing process died mid-operation, so the
  durable row can never reach a terminal state) are reclaimed by the Host. The
  repository-level `ReclaimOrphanedLifecycleOperations` closes an open
  operation to `terminal_result=cancelled` only when no step holds an
  unexpired lease (fail-closed; live leases always keep blocking the runtime
  fence). It is attempted from the bootstrap genesis retry loop on
  `ErrLifecycleOperationInProgress` (log-only on failure) and from the
  plugin-runtime coordinator's reconcile fence before reporting pending, so
  recovery happens automatically in both boot and steady-state paths. The
  out-of-band CLI `extension recovery lifecycle-close --operation-id <id>
  --reason "<why>" [--force]` performs the same single-transaction close with
  an explicit operator reason; both paths append audit evidence
  (`extension.lifecycle_orphan_reclaim` / `extension.cli_recovery`) and land
  on a terminal state that `RecoverLifecycleOperation` accepts, so retries
  reuse the existing recovery flow. Verified 2026-10-17 on the dev instance:
  a planted orphan was auto-reclaimed within one poll interval
  (`extension_lifecycle_operations` id 238) and readiness stayed `ready=true`.
- **Version GC (2026-10-17) reclaims historical version rows and their disk
  directories explicitly, audited, and fail-closed.** `sforum extension gc`
  dry-runs by default (candidate list + retain/block reasons + a one-time
  confirmation token bound to the exact plan digest, 15-minute TTL) and
  executes with `--confirm-token`. A version is reclaimable only when it is
  neither active nor staged, no open lifecycle operation targets its tuple,
  no `plugin_runtime_publication_members` row references it, it is beyond the
  retention window (default active + 1 most recent historical version;
  `--retain N` / `--retain-days D`), and its directory exists. Execution is
  disk-first per candidate (atomic staging rename → in-lock recheck of every
  criterion → delete row + `extension_version_gc_removals` anchor +
  `extension.version_gc` audit in one transaction → remove staging), so a
  failed item keeps both ledger row and directory. Because the publication
  ledger is append-only (`202607160027` triggers), versions referenced by any
  publication are permanently pinned (`blocked=publication_history`) and the
  plan-A "delete expired publications" idea from the task book was recorded
  as a conflict and dropped —
  `decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`.
  Consequence: plugin versions that were ever enabled stay forever; the real
  reclaim surface is theme history plus never-published plugin versions. Dev
  instance dry-run evidence: 225 historical rows → 139 reclaimable /
  63 publication-pinned / 13 within-retention / 10 disk-missing. The admin
  entry (stage 5) shipped the same day (2026-10-17): `GET
  /admin/extensions/version-gc/candidates` (read-preview, never mints tokens),
  `POST .../version-gc/confirmations` (plan-bound one-time token) and
  `POST .../version-gc/execute` behind the extensions read guard (GET) /
  super_admin guard (POSTs) plus an authoritative super_admin service boundary
  (`VersionGCAdminService`) that binds the actor into `mode=api` audit anchors;
  the three-phase `SFAdminVersionGcDialog.vue` lives on the admin extensions
  overview toolbar. Browser QA passed on desktop and 390x844; the dev instance
  QA deliberately never executes a real sweep.
- Ordinary uploaded packages remain recoverable through `extension disable`.
  A protected built-in/system executable requires `extension quarantine` with
  the exact active version and 64-character digest; the command atomically
  disables that artifact and appends immutable runtime and audit evidence.
- Safe mode, pre-plugin boot health, immutable snapshot rollback, and CLI
  recovery are Host-owned and non-overridable.
- Raw request/session authority and raw core database access are independent
  high-risk powers, never implied by ordinary extension trust.

### Uninstall Cleanup Convergence (2026-10-24)

- V2 uninstall stays two-phase: durable terminal state first, host physical
  cleanup second (`finalizeLifecycleUninstall`). Failures surface as 503
  `extension.lifecycle_cleanup_failed` with structured
  `data.details` (`operationId`, `failedStep`, `retriable`) via the typed
  `LifecycleCleanupFailure`; the `errors.Join` chain and sentinel mapping are
  unchanged.
- Convergence is owned by `CleanupConvergenceService` (focused collaborator
  embedding `*Service`): a River periodic sweep
  (`extensions.finalize_uninstall_cleanup`, 5m + RunOnStart, batch 10) replays
  pending tombstones from the failed step; a keyless super_admin endpoint
  `POST /admin/extensions/:id/uninstall-cleanup/finalize` converges one
  extension on demand; the admin list projects a `pendingUninstallCleanup`
  badge. Safety rests on the existing terminal validation, purge fence, `FOR
  UPDATE` lock, finalized replay shortcut, and CleanupID-idempotent purger —
  never skips receipts.
- The sweep worker registers only on the embedded worker
  (`workerRuntimeDeps.UninstallCleanupSweeper`); standalone `cmd/worker`
  leaves it nil on purpose (full host boundary is API-assembled). Structured
  retry and the keyless endpoint work in every topology.
- Frontend: uninstall idempotency keys persist in sessionStorage per
  extension (`useAdminUninstallCleanup.ts`), the confirm dialog offers retry
  with the failed-step name, and a "finish cleanup now" button covers the
  lost-key path. See
  `knowledge/decisions/2026-10-24-uninstall-cleanup-convergence.md`.

## Permissions

- `extension.view`: inspect packages, runtime state, events, and catalogs.
- `extension.plugin.manage`: plugin install, trust, enable/disable, settings,
  upgrade, and uninstall.
- `extension.theme.manage`: theme install, trust, settings, activation, and
  rollback.
- `extension.manage` remains a parent compatibility alias; authoritative
  handlers use the fine-grained permissions.
- Executable trust confirmation remains `super_admin` only even when lifecycle
  administration is delegated.
- Plugin-declared permission keys and recommended role mappings never grant
  themselves. Host role/permission administration remains authoritative.
- Plugin permission `label` and `description` accept `LocalizedText`. The
  exact artifact owns those translations; the Host persists and resolves them
  for permission catalog APIs without adding plugin keys to Core i18n files.
- Role and user permission catalogs include only extension permissions whose
  latest Identity Registry declaration is active. Disabled or uninstalled
  declaration tombstones remain durable but are not assignable.

## Manifest And Registries

- Root manifest: `sforum.extension.json`; complex V3 packages may shard through
  `includes` while simple packages keep a single file.
- The planned sticker contract deliberately does not use a handwritten
  `stickerPacks` array or `includes.stickers`. One generated catalog exact-lists
  directory-discovered media, and one constant-size Manifest reference binds
  that catalog. Static install validates it without executing plugin code;
  lifecycle activation imports bytes into Core-owned immutable storage.
- Manifest V3 also carries the plugin-facing AI surface: `aiTools[]` (plugin-only,
  requires `backend.entry` + protocol version 2, namespaced `id`/`handler`,
  `contractVersion == id@1`, model-safe unique `name`, JSON-Schema `parameters`,
  explicit `timeoutMs`) and `jobs[].queue` (closed set `default`/`ai`, mapped by
  `jobs.QueueForPluginDeclaration`). A plugin that calls any Host Command must
  declare `database.grants: ["host_commands"]`; the reviewed impact document must
  match it exactly.
- Validation covers identity/version/compatibility, exact package-file hashes,
  dependencies/conflicts/provides, declarations, entry paths, migrations,
  capabilities, admin pages, theme assets/templates, and unsafe archive paths.
- Uploaded ZIP entry names are rejected at the archive-reader boundary before
  any snapshot operation when they contain `..`, NUL, an absolute/UNC prefix,
  or a Windows drive prefix. Snapshot normalization independently rejects
  traversal segments, symlinks, special files, duplicates, and file/directory
  collisions before using `filepath.Join`.
- Manifest locale fallback is exact locale, language prefix, then root display
  fields. Admin micro-frontend locales are separate from manifest identity and
  settings/contribution labels.
- Versioned registries cover routes, hooks, services, providers, jobs,
  schedules, commands, admin surfaces, queries, identity/permission/profile,
  media, navigation/regions, content, cache, assets, and packages.
- Shortcode V1 uses Content Registry declarations but does not delegate storage
  or authorization to them. The Host owns `sforumShortcodeRef` and
  `sforumShortcodeBlock`, exact argument/placement validation, public-source
  redaction, bounded batch planning, and closed protected fallbacks. Plugin
  declarations own product schemas and typed output. The production Forum
  bridge dispatches strict per-node calls through exact-artifact Protocol V2;
  ordinary content remains unchanged when no active declaration is bound.
- `/_sforum` is a Host-reserved resource namespace. Public package bytes use
  content-addressed `/_sforum/assets`; authenticated prebuilt admin assets use
  `/_sforum/private-assets`. Route Registry contributions cannot claim either.
- Public asset publication preallocates only the fixed 256-declaration output
  limit and fails closed above that limit; untrusted manifest slice lengths are
  never added together to determine an allocation size.
- Route Registry supports add, alias, redirect, rewrite, before/after/filter,
  wrap/replace, global middleware, uploads, opaque streams, SSE, and WebSocket
  on declared public/admin/API methods and paths. Final redirect output accepts
  only absolute-path references without query, fragment, CR/LF, or backslash;
  declaration paths, normalized request paths, and final `Location` values
  explicitly reject a second `/` or `\\` so browser network-path
  normalization remains impossible even for restored or prebuilt plans.
- A `core.guard.login` failure on a `stream`-mode route is translated into a
  browser navigation (`GET`/`HEAD` plus an `Accept` containing `text/html`)
  as `302 /login?redirect=<path+query>`; the target is built only from the
  current request path and its raw query string (never `Referer`, `Host`, or
  `X-Forwarded-*`), a non-site-absolute target degrades to `Location: /login`,
  and the redirect carries `Cache-Control: no-store` because the URL may hold a
  one-time token. API clients, SSE, WebSocket/multipart/http modes, and unsafe
  methods keep the `401 auth.required` envelope. Guard-failure traces are still
  published by `Open()`. See
  `plans/archive/2026-10/2026-10-18-route-stream-login-redirect.md`.
- Nuxt plugin-route proxy integration fixtures capture forwarded method, query,
  and body server-side and return a fixed `text/plain` + `nosniff` response;
  attacker-shaped request material is never reflected by the test HTTP server.
  Production proxying still preserves the trusted plugin's declared response
  bytes and media type rather than applying a Host HTML sanitizer.
- Core-owned handlers keep authoritative policy checks. A trusted replacement
  handler or custom guard owns only the authorization contract it explicitly
  declares and must remain inspectable/auditable.
- Registry conflicts, selected providers, grants, active artifacts, replacement
  handlers, and rollback snapshots must be visible to operators.

Generated author catalogs live under `docs/extensions/catalogs/`; V3 runtime
catalogs and governance live under `docs/extensions/v3/`. Route discovery uses
an explicit map for the fixed Fiber registration-method domain; `All` receives
the stable method identity `all`, ordinary methods have fixed lowercase
identities, and unsupported or wildcard-shaped values fail closed. Stable
catalog IDs do not rely on string replacement as sanitization.

## Host API And Runtime

- Protocol V2 uses HashiCorp go-plugin gRPC/AutoMTLS and exact
  Manifest-selected contracts. No alternate executable transport or silent
  downgrade is supported.
- Bootstrap ABI v1, application Protocol V2, and Host API V2 are distinct
  contracts. Startup diagnostics classify cookie, CPU/exec format, dynamic
  dependency, and executable-permission failures without exposing raw child
  stderr through public readiness responses.
- Each process receives a runtime-scoped Host broker bound to token, artifact,
  grant, epoch, instance, authority, deadline, locale, and trace. Plugin-supplied
  actor identity is rejected unless Host-attested delegation exists.
- The supported Go authoring surface is `apps/api/sdk/plugin`; CLI validation is
  `sforum extension test [path]`.
- Author-facing docs added 2026-08-16: `docs/extensions/routes.md` (declared
  HTTP routes: manifest `routes[]` semantics, core guards, Protocol V2
  `InvokeRoute`/`RouteStream`, ingress probe + Nuxt proxy, limits, testing) and
  `docs/extensions/build-and-load.md` (backend go.mod `replace` wiring, frontend
  prebuild, digest/validate/test/package, dev load via `EXTERNAL_EXTENSION_ROOTS`
  or upload, trust + iteration loop). `sforum-custom-content` is the canonical
  route fixture; authoring-guide gained a Reference 4 pointing at it and
  cross-links from both new pages. Both pages have full Chinese translations
  under `docs/zh-CN/extensions/` (routes.md, build-and-load.md); `docs/en-US/extensions/`
  holds short pointer pages so the zh/en handbook trees stay structurally
  parallel (`validate-docs.mjs` enforces the file-list parity).
- Runtime resilience includes per-extension concurrency, deadlines, circuit
  state, crash/restart reaping, admission fencing, and inspectable degraded
  state.
- Versioned River jobs execute only against the exact live artifact/grant/job
  contract and payload schema. Upgrade policy chooses execute, drain, declared
  migration, or cancellation.
- Unsafe bounded plugin HTTP routes may use Host-owned idempotency replay;
  streaming modes cannot request replay.
- Stable plugin read surface (2026-10-18, E10): nine plugin-callable stable
  HostQuery definitions (safe user by id/username, safe users batch with live
  avatar metadata, public topics list/by id, public comments list, public
  categories list, public attachment metadata, site stats) backed by
  `sforum_core_v1` stable views (`site_stats`, `public_categories`,
  `safe_user_avatars` added by `202610180001`). `core_views` is the required
  authority for both the RPC path and direct view SQL; the query authority
  resolver expands Manifest V3 `database.grants` (legacy `authority` strings
  still accepted) and requires the trust impact document's canonical grant
  set to equal the live manifest's. Catalog growth is deliberate: explicit
  Query Registry mapping, sealed digest, and the
  `bootstrap/extension_lifecycle_test.go` count tripwire move together.
  Policy: `decisions/2026-10-18-extension-data-surface-policy.md`.

## Provider And Product Boundaries

- Core defines stable provider slots, settings/reset/probe UX, permissions,
  no-op or protected defaults, and typed contracts.
- Vendor-specific mail, storage, search, payment, analytics, notification, and
  external-integration behavior belongs in plugins.
- Site PostgreSQL search is the protected default; Meilisearch is an optional
  external plugin.
- SMTP is a protected built-in plugin. Attachment storage keeps Core `local`
  as the safe default and supports legacy single-provider selections plus
  Host-owned named plugin instances. The protected `sforum.storage-s3` plugin
  implements AWS S3, MinIO, Cloudflare R2, and compatible services without
  putting vendor SDK behavior in Core; the former FTP/SFTP built-ins are gone.
- GitHub social login V1 is planned as the protected built-in plugin
  `sforum.auth-github`. Built-in discovery stages it but does not automatically
  trust, enable, configure, or publicly activate it. Core retains all account,
  callback, link, risk/session, and browser-session authority. See
  `../plans/2026-07-27-github-social-login-builtin-plugin.md`. M0 is
  implemented; **T1A** closed Host callback authorization ordering, legacy
  complete bypass, live exact-artifact recheck, PKCE + trusted absolute
  callback URL, and redirectHint validation. **T1B** closed stable
  `IDENTITY_SUBJECT_HMAC_SECRET` config + production fail-closed validation,
  bootstrap digest injection (no process-random), concurrent TTL-aligned
  hashed callback/ticket stores, and registration ticket binding/timestamps.
  **T1C** closed atomic CAS Host activation (exact live package digest bind),
  effective public catalog (Safe Mode / artifact drift / disable remove
  availability), actor-bound activation audit, and truthful probe pending
  (`ok=false`, never `probe_pending` as success). **T1D** closed session-bound
  recent-auth, unlink ownership/revision/TX last-method, password
  upsert/setup, authoritative external registration, and canonical
  CurrentUser session claims. **T1E** closed modular OpenAPI, Core Route
  Catalog reserved callback (closed to Route Registry replacement), controller
  HTTP allowed/denied + lifecycle/two-provider tests, and M0 ADR Host ownership
  correction. Full M1R Host foundation is complete. **T2/M2A** landed the
  protected built-in package `extensions/builtin/plugins/sforum-auth-github`
  (Manifest V3, identity.runtime@1 auth provider, settings/schemas, fake
  GitHub + protocol tests, build-builtin-plugins entry). **T3/M2B** proved
  SyncBuiltins exact-artifact staging without Host public activation, release
  container packaging for `sforum-auth-github`, and Protocol V2 headless E2E
  (plugin runtime → Host login/registration ticket/link). Default remains not
  Host-activated for public login. **T4/M3** admin Login Methods UI complete
  (`identity.provider.manage` may manage auth-plugin settings; page at
  `/admin/settings/login-methods`). **T5/M4A** public login/callback/
  registration UI complete: plugin declares identity provider `label`/`icon`;
  Host public catalog injects localized presentation; Core shells stay vendor-
  agnostic. **T6/M4B** account-security UI implemented. **T7/M5** added the
  lifecycle/security matrix, Identity Extension Surface Matrix, bilingual
  operator/author docs, and start/callback rate limits. Independent review
  rejected program closure. **T8A done:** registration commit fencing
  (activation + live artifact recheck, in-TX policy, `user.registered` +
  transaction-owned audit). **T8B done:** versioned `provider.probe` identity
  operation + GitHub Probe wiring; admin directory merges package catalog
  discovery with live Registry (pre-enable discovered, disabled/drifted
  inspectable; trust/enable authority unchanged); Core admin UI uses Host
  label/icon only. **T8C done:** production Host/plugin refuse fake-GitHub
  endpoint overrides; Redis external-auth rate limit TTL is atomic; auth
  start fails closed on partial wiring; migration 057 no longer touches
  `password_hash`. **T8D evidence prepared:** retained lifecycle/API/browser
  evidence, hard 429 assertions, and migration 058 which downgrades only a
  historical GitHub built-in marked enabled without durable Identity Registry
  evidence. It preserves the immutable package while resetting executable
  status and requires normal actor-bound enable. Independent re-review remains
  mandatory; see
  `../reports/2026-07-27-github-social-login-t8d-requirements-matrix.md`.
  **R1 remediation (2026-07-27):** the Host repeats generic exact-contribution
  and activation validation after auth-provider completion and at the admitted
  browser-session effect boundary. This protects all executable auth-provider
  contributions from Safe Mode, activation, and artifact changes without
  adding vendor branches or changing protected built-in ownership.
  **R2 remediation (2026-07-27):** the Host's external-registration mutation
  reads the operator registration policy from the same PostgreSQL transaction
  as user/link/audit creation. This is Core-owned policy enforcement and does
  not expose any option or database authority to provider plugins.
  **R3 remediation (2026-07-27):** external-registration operator audit no
  longer stores the owning artifact digest. The host preserves extension
  artifact history in its dedicated immutable records; redacting an account
  registration audit must not mutate that separate lifecycle evidence.
  **R4 remediation (2026-07-27):** executable auth-provider disable uses the
  normal Lifecycle V2 service path. The GitHub reference contributes only a
  generic no-side-effect lifecycle stream; Host lifecycle ownership drains the
  exact runtime and retires its live and durable Identity Registry publication.
  **R5 remediation (2026-07-27):** legacy startup repair checks lifecycle
  operation and audit evidence as well as the current durable root. This keeps
  immutable artifacts and never auto-enables code; ambiguous partial history is
  preserved for explicit operator recovery rather than silently downgraded.
- Public navigation completion is planned through the existing V3
  Navigation/Region authority. Plugins declare exact-artifact contributions;
  Core owns operator placement/defaults/backup and themes own presentation.
  Direct plugin writes to operator navigation tables or raw DOM injection stay
  closed. See
  `../plans/2026-07-27-configurable-public-navigation-platform.md`.
- Core may own shared payment/entitlement semantics, but gateways and vendor
  webhook behavior remain plugins.

Every core module maintains an Extension Surface Matrix. A closed surface needs
an explicit security, integrity, or ownership reason; ordinary integrations
should use dedicated registries instead of raw database or whole-route power.

Host build identity (`coreVersion` / `buildVersion`) is a read-only surface with
one authority and four projections: v2 handshake (`host_build` → `Server.HostBuild()`),
admin bridges (`page-bootstrap.host`), the public L2 descriptor (`coreVersion`),
and `GET /api/v1/health`. It is frozen per process; there is no query, command, or
event for it, and `buildVersion`/`commit` never enter a public response.

## Themes And Page Registry

- Themes are buildless runtime packages with `theme.json`, templates, and
  assets. They cannot declare backend runtime, migrations, jobs, or provider
  capabilities.
- L0 owns CSS/tokens/fonts/images/locales; L1 owns reviewed server-rendered
  templates; trusted author-prebuilt L2 is digest-authorized and contract-bound.
- Page Registry matching is deterministic: static before parameter before
  catch-all. Access declarations fail closed.
- Activation verifies exact artifacts, prewarms runtime state, replaces page
  contributions/skin atomically, and preserves rollback.
- Theme runtime publications retain their immutable historical actor ID. When
  another node replays Page Registry bindings after that user has been deleted,
  the mutable `approved_by` foreign key resolves to `NULL`, matching its
  existing `ON DELETE SET NULL` contract without changing publication history.
- Theme packages declare a validated `navigationLocations` capability using
  only the four stable v1 location IDs. The runtime projects capability from
  the exact active immutable snapshot; unsupported locations retain operator
  configuration, while Core emergency fallback reports all v1 locations as
  supported. The built-in default theme declares all four locations. The
  former Nocturne/Night Harbor theme is no longer shipped as a built-in.
- Template validation and runtime island binding are separate gates. Every
  Host island used by a theme template must be present in both
  `allowedHostIslands` and `productionThemeIslandBindings`; paired public
  surfaces such as topic create/edit require completeness tests across all
  builtin themes.
- CSS safety matching operates on full declaration names. Dangerous legacy
  `behavior:` remains rejected, while standard suffix properties such as
  `overscroll-behavior:` must not be rejected by substring matching.
- The selected theme owns public presentation. Core output is emergency-only
  when the runtime cannot safely resolve/render the selected artifact.
- Theme-defined system error pages are **completed** for 403, 404, 429, and
  500/502/503/504. They are virtual Page Registry surfaces selected only by
  the Nuxt error boundary; they have no ordinary public path match. The active
  theme may provide L0/L1 presentation through exact-artifact activation, while
  Host owns status, safe copy/actions, retry policy, cache/robots, and Core
  emergency fallback.
- Plugins cannot replace `system.*` surfaces, and public L2 widgets are
  rejected on system error templates. This closed surface is intentional for
  availability and information-disclosure reasons; reviewed Host islands are
  the only dynamic elements allowed inside theme templates.

Relevant plans:

- `../plans/2026-07-22-theme-consistent-public-resource-404.md`
- `../plans/2026-07-22-theme-defined-system-error-pages.md`

## Settings And Admin UI

### External Auth R7 Recovery

- The plugin runtime coordinator defers application of an older desired full
  set while a durable lifecycle operation remains open. A lifecycle operation
  is therefore the only producer of its exact cross-registry transition.
- Built-in synchronization stages a changed artifact for an enabled executable
  plugin; it never advances `active_version_id` or emits a runtime publication.
  Normal enable/upgrade lifecycle remains the sole activation authority.
- Two narrowly scoped legacy GitHub recovery migrations preserve immutable
  history while restoring a pre-lifecycle audited active artifact and appending
  one matching runtime full-set revision. They do not apply when successful
  lifecycle evidence exists, do not auto-enable code, and do not relax the
  Identity Registry exact-artifact fence.
- Lifecycle registry publication now appends durable Identity revisions in the
  same Serializable PostgreSQL transaction as the aggregate registry phase.
  A later registry-family failure therefore cannot leave an Identity tombstone
  committed while the aggregate phase remains at source. Startup also repairs
  the pre-fix shape only when the exact enabled artifact, latest failed disable,
  uncommitted publication, source state/registry phases, actor, and audit event
  all agree; ambiguous or drifted history remains fail-closed.
- R1-R7 remediation runtime evidence was completed against isolated PostgreSQL:
  real lifecycle disable retired the exact provider publication and made both
  catalog and login start fail closed; real enable, probe, and activation then
  restored the exact package digest to the public catalog. Artifact drift and
  Safe Mode also publish no provider. This is **整改完成，等待独立复审**; it is
  not a self-declared program closure. See
  `../reports/2026-07-27-external-auth-r1-r7-requirements-evidence-matrix.md`.

- Plugins/themes use one versioned settings document. Recommended defaults and
  one-click reset are Host-rendered.
- Schema UI supports tabs, groups, columns, callouts, common field types,
  secrets, validation, and allowlisted settings actions without an extension
  frontend build.
- Settings actions come in two kinds. `provider_probe` requires a declared
  provider slot (storage providers reuse `StorageProbe`); `ai_tool_probe`
  requires a declared `aiTools[].id` and no provider slot. Both start the same
  restricted short-lived runtime seeded with the operator's draft values, run
  while the plugin is disabled, and are gated on the exact artifact being
  available. The AI tool path dispatches `sforum.ai.tool`/`invoke` with the
  draft values as JSON arguments and maps `isError` to the action result; it
  carries no `details`/`suggestions` (the tool result only has `content`). See
  `../decisions/2026-10-15-ai-tool-probe-settings-action.md`.
- Complex settings may use an author-prebuilt immutable admin micro-frontend
  only after exact-artifact trust. Import, API, mount, CSS, cleanup, or
  quarantine failure falls back to Schema UI.
- Ordinary plugin admin pages may use the same trusted prebuilt component
  contract. They are served only for enabled plugins, outside Safe Mode, to an
  actor with `extension.view` and the page's optional declared permission;
  failures preserve the Host admin shell and render a page-local retry state.
- Public plugin pages inheriting the active theme shell remain a separate
  follow-up and are not part of the admin page-body implementation.
- Provider probes run in restricted short-lived processes without a Host API
  token or runtime registrations.
- Public contributions gated by settings bump
  `site.public_surface_revision`. Topic HTML no longer consumes that revision
  because `/t/**` disables whole-page caching; each SSR request resolves current
  Page Registry and contribution state without requiring theme reactivation.
- Page Registry separates behavior replacement from theme presentation:
  `replaceable` admits trusted plugin replacement after approval, while
  `themeable` admits only selected-theme L1 templates around reviewed Host
  islands. Existing replaceable pages remain themeable; `moderation.review` is
  the first themeable/non-replaceable Core surface.
- Admin surfaces include overview, plugin/theme lists and details, settings,
  event log, extension points, Page Registry, lifecycle progress/recovery, and
  provider inspection. There is no store surface in core: the app store is
  out-of-tree, and a catalog plugin installs through `install-from-url` or the
  host-mediated install action of its own trusted admin page.
- Admin extension overview and plugin management label `stagedVersion` as a
  pending upgrade rather than a generic candidate, while retaining the exact
  current/staged version explanation. Each staged plugin has an Upgrade action;
  the bulk action runs candidates sequentially and pauses on any required
  exact-artifact or legacy capability review. Enabled Lifecycle V2 source and
  target artifacts use the native upgrade ledger, while legacy or recoverably
  disabled sources use the existing restart bridge. Every item keeps its own
  idempotency key and Host preflight; bulk UI is not a bypass or batch backend.
- List/detail runtime RSS is best-effort and attributes only owned backend
  plugin children of the current API or its PID-namespace-sharing Worker.
  Linux release images use procfs, including the production extension root
  `/var/lib/sforum/extensions`; running backends without a current sample show
  "not sampled yet" instead of the false "no independent process" state. The
  admin overview orders per-plugin rows by RSS and reports transient same-owner
  overlap instead of silently inflating one plugin's value.
- Protected built-in backend binaries use `-ldflags="-s -w"` in the development,
  Docker, and release build paths. This removes linker/debug payloads from the
  shipped executable without changing the plugin protocol; the current seven
  built-ins save roughly 69 MiB in aggregate on the local build host.
- Plugin-side Protocol V2 serving is owned by `sdk/plugin/v2`; dependency
  direction is Host to SDK, and an architecture ratchet rejects any renewed SDK
  import of the legacy runtime. The SDK dependency graph fell from 396 to 164
  non-standard packages. Storage helpers now live in the lightweight
  `sdk/plugin/storageprovider` package: FS builds with 166 non-standard
  dependencies and S3 with 256 including AWS SDK v2.
- The storage extraction reduces stripped Linux binaries from 22,589,602 to
  15,564,962 bytes for FS and from 31,088,802 to 24,948,898 bytes for S3.
  Matched idle Linux PSS falls by 7,040 and 5,304 KiB respectively.
- Plugin child processes receive `GODEBUG=disablethp=1` to prevent per-process
  Linux heap THP over-allocation. Isolated SMTP evidence fell from 27,360 KiB to
  19,284 KiB PSS after dependency slimming with the same THP setting; production
  acceptance remains a post-release 60-second median PSS measurement. The admin
  resource endpoint now exposes that median per plugin and reports current
  `AnonHugePages`, so production acceptance no longer depends on instantaneous
  RSS screenshots. See
  `../decisions/2026-08-20-plugin-runtime-memory.md`.
- Plugin child processes also receive fixed GC defaults `GOGC=50` and
  `GOMEMLIMIT=48MiB` (soft limit) injected in `buildPluginProcessEnv`
  (`Support/Extensions/plugin_command_transport.go`); deployed instances get
  them with zero configuration. The host may override the limit per instance
  via `SFORUM_PLUGIN_GOMEMLIMIT` in its own environment (empty = default).
  Measured PSS averaged ~15.2 MiB/plugin across 19 dev plugin processes
  (~20% below the 08-20 baseline). See
  `../decisions/2026-10-17-plugin-process-memory-limits.md` and
  `../reports/2026-10-17-plugin-memory-limits-recheck.md`.
- Extension trust digest verification streams SHA-256 through a fixed-size
  buffer. It preserves exact-root, stable-file, size, and regular-file checks
  while avoiding an `io.ReadAll` allocation proportional to a 20-30 MiB plugin
  artifact on every GuardPolicy refresh.

## Important Paths

| Path | Responsibility |
| --- | --- |
| `apps/api/app/Models/Extensions` | Package, trust, lifecycle, registries, settings |
| `apps/api/app/Models/Extensions/lifecycle_repository_orphan.go` | Orphan lifecycle operation reclaim (repository + store wrapper) |
| `apps/api/app/Models/Extensions/postgres_store_version_gc.go` | Version GC candidate criteria, confirmation tokens, disk-first sweep |
| `apps/api/app/Models/Extensions/artifact_retention.go` | Shared artifact health / retention judgment (GC + missing-artifact cleanup) |
| `apps/api/app/Support/StableFile` | Cross-platform stable file-open primitive (extracted from Extensions, 2026-10-17) |
| `apps/api/app/Support/ExtensionRuntime` | Plugin process/runtime coordination |
| `apps/api/app/Support/Extensions/LifecycleRecovery` | Exact-evidence durable publication startup recovery |
| `apps/api/app/Support/PluginBootstrap` | Shared Host/SDK process bootstrap ABI and startup diagnostics |
| `apps/api/app/Support/HostAPI` | Host broker and compatibility API |
| `apps/api/sdk/plugin` | Public Go plugin SDK |
| `apps/web/app/pages/admin/extensions` | Admin extension surfaces |
| `apps/web/app/components/extensions` | Shared extension admin UI |
| `extensions/builtin` | Protected packages scanned at boot |
| `extensions/optional` | Ship-with-repo operator-installed packages |
| `extensions/fixtures` | Contract and CI fixtures |
| `docs/extensions` | Authoring, contracts, catalogs, V3 evidence |

### Notifications Extension Surface

- Manifest V3 `notificationTypes` is inert exact-artifact data. A declaration
  owns only ids below its extension namespace and binds a digest-checked schema
  package file; plugins cannot declare required notices.
- Host API v2 `notifications.emit@1` is the sole plugin-to-Core emission path.
  Existing broker authentication and Host Command scope resolution bind exact
  artifact, trust grant, runtime epoch, and instance before notification policy
  or persistence runs.
- The Go SDK constructs actorless typed requests. Host broker validation rejects
  raw actor/session authority; command validation rejects foreign namespaces,
  schema/version drift, undeclared targets, inactive users, oversized/bulk
  recipient sets, rate overflow, and expired deadlines.
- Raw Core notification, preference, and delivery tables remain closed. Safe
  mode/disable/uninstall close new admission while historical notification rows
  retain Host-owned fallback rendering.
- `notification.channel.web_push` is an independently selected provider slot.
  Core owns subscription identity, policy, projection, River retries, and the
  redacted ledger; the exact plugin artifact owns VAPID/Web Push transport.
- The protected built-in `sforum.web-push` is never auto-enabled, configured,
  or selected by discovery. Secret settings remain redacted and the Host-owned
  service worker cannot import provider code.

M8 protected shortcode execution is a separate Host-only request path. M9
production activates exact protected declarations only after Host policy
authorization. Private executions emit
no Content Registry cache key/tags and suppress Protocol V2 Host query
delegations. Plugin input is limited to an allowed decision plus the exact
accepted child fragment. It contains no session, actor identity, permission
table, email, IP, moderation data, surrounding body, or database/query
capability. Test and production bindings prove allowed rendering; disable,
crash, timeout, invalid output, and Safe Mode return Host-owned closed
fallback, and trace/error serialization contains neither fragment nor output.
Production bootstrap wires the Host policy and the built-in manifest registers
only the exact `login`, `reply`, and `only-author` handlers.

## Verification

- Go: `cd apps/api && go test ./...`
- Web: `cd apps/web && bun run typecheck`
- Extension/V3 gates are wired through `./scripts/test.sh`; use the narrower
  commands named in the active remediation plan while iterating.
- After catalog changes, regenerate/check with the documented generator rather
  than editing generated files by hand.
- After changing a built-in public theme: run
  `./scripts/build-builtin-plugins.sh`, restart the API, activate the staged
  digest through `/control-panel/extensions/themes`, confirm the Page Registry
  bindings for every affected page, then perform browser geometry and
  interaction checks on the active artifact.

## Next Steps

1. Complete the new editor product design before implementing the image sticker
   generated-catalog and lifecycle contract.
2. Built-in Shortcodes M10B is active/blocked: complete normal super_admin
   exact-artifact activation and the final lifecycle/browser/leakage matrix;
   do not broaden the exact L2 or server-internal Query Registry boundaries.
3. Execute `../plans/2026-07-22-v3-production-rewire-honesty-remediation.md`
   M0-M8 with production-path evidence.
4. Execute public navigation M0 before extending `forum.nav.items`; prove and
   document its production bridge to Navigation Registry instead of adding
   another contribution stack.
5. Keep APILTS compatibility shims until their removal gate, date, and live
   zero-use evidence all pass.
6. Keep system error pages plugin-closed and L0/L1-only when adding future
   status families or browser-facing producers.
7. Keep sensitive Core workbenches plugin-closed unless behavior replacement is
   an explicit product decision; use `themeable` only for constrained Host-island
   presentation.
8. Keep new product integrations on stable provider/registry contracts and
   regenerate the affected Extension Surface Matrix.
