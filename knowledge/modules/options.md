# Runtime Options Module

## Purpose

Owns self-hosted, operator-editable site settings that can safely change at
runtime without rebuilding or restarting the application.

## Current Status

Initial runtime option support is implemented.

- **客户端版本策略（2026-10-02）：** 新增 `client.minimum_version`、
  `client.recommended_version`、`client.update_notice` 三个 public 选项（管理权限
  `settings.site.manage`），由 `client_version_options.go` 定义、归一化、脏数据回退与
  校验，默认值全部为空即「不限制」。原生 App 冷启动读 `GET /web-options` 即可决定是否
  展示强制升级页；服务端不代客户端判定升级，也不上报客户端版本。浏览器前台忽略这三
  个键。后台入口为站点设置 → 客户端（`SFAdminSiteClientTab.vue`，含一键恢复推荐默认）。
  决策：`decisions/2026-10-02-client-version-policy-via-public-options.md`。

The 2026-07-28 architecture debt program moved option normalization into
focused site, identity, forum, SEO, attachment, and avatar files while keeping
`service_normalize.go` as the dispatcher. Fixed admin settings tabs own their
form lifecycle and submit only their allowed option subset; route pages remain
SSR/query/permission shells.

- PostgreSQL migration `202607050001_web_options.sql` creates
  `web_options(name, value)` and seeds `site.name = SForum`.
- Backend module `apps/api/app/Models/Options` exposes a typed service with
  public runtime reads, admin-only reads, single-option compatibility updates,
  and batch updates.
- The backend service caches option values for a short TTL and invalidates the
  cache after admin updates.
- API routes:
  - `GET /api/v1/web-options` for public, frontend-safe options.
  - `GET /api/v1/web-options/:name` for a public option.
  - `PUT /api/v1/web-options` for the original single-option update path.
  - `GET /api/v1/admin/web-options` for all admin-manageable options with
    secret values masked.
  - `PUT /api/v1/admin/web-options` for batch admin updates.
- Updating non-SEO options requires the existing `settings.manage` permission;
  `seo.*` options require the independent `seo.manage` permission, and
  attachment options require `attachment.settings.manage`.
- Nuxt composable `useWebOptions()` provides `webOption()`, `siteName`,
  `siteUrl`, `siteDomain`, `defaultLocale`, `supportedLocales`, `humanVerificationProvider`,
  `appearanceTheme`, footer content helpers, `refresh()`, `save()`, and admin
  batch helpers.
- Operator appearance remains the anonymous and inherited account default.
  Logged-in users may save a private accent/background override through the
  Identity module; clearing that override resumes live inheritance, including
  future operator changes. The root appearance resolver applies admin preview,
  user preview, saved user preference, then operator default in that order.
- Public site URL resolution is `site.url` operator override, then deployment
  `APP_URL`. An empty override remains empty in storage instead of materializing
  the environment value; public/API consumers receive the effective URL, while
  admin option metadata exposes `overrideValue`, `fallbackValue`, and
  `inherited`. OAuth callbacks use this same effective URL for newly started
  flows; CSRF origins and cookie security remain environment/startup concerns.
- `site.domain` is the public, display-only website domain used for profile URL
  prefixes. Admin input strips `http://`/`https://` and trailing slashes before
  save; the Options service repeats normalization authoritatively and rejects
  paths, queries, fragments, credentials, and non-HTTP schemes. Its initial
  default is derived from the trusted `site.url` host rather than a product
  domain literal.
- `site.about_url` and `site.about_open_in_new_tab` drive the public left
  sidebar's bottom "About {siteName}" entry. Empty URL keeps the entry as
  inert explanatory text; configured internal paths or HTTP(S) URLs make it a
  link, with the new-tab flag applied at render time.
- Admin page `apps/web/app/pages/admin/settings/index.vue` uses page-level tabs
  for basic site settings, account security (password + sessions + login
  lockout + manual identity-mail resend limits), registration/username policy, newcomer trust limits, maintenance
  mode, and CAPTCHA/human-verification settings.
- Admin page `apps/web/app/pages/admin/personalization.vue` is the unified
  personalization hub under the System folder: appearance preset + footer
  (requires `settings.appearance.manage`), plus brand/nav/announcements/legal/
  friend-links tabs (requires `settings.site.manage`). Page access is `any` of
  those two permissions; tabs filter client-side. Legacy `/site-chrome` redirects
  to `/personalization?tab=…`.
- Admin page `apps/web/app/pages/admin/seo.vue` manages runtime SEO settings
  across meta/social, indexing/robots, sitemap, structured data, verification,
  and diagnostics tabs.
- Admin page `apps/web/app/pages/admin/attachments.vue` manages attachment
  storage provider settings, upload limits, path templates, public URL
  settings, secret-bearing provider credentials, and attachment governance.
- Admin page `apps/web/app/pages/admin/settings/avatar.vue` manages avatar
  default source, Gravatar base URL/hash algorithm, upload/GIF switches,
  upload size and dimension limits, compression target size/quality, and
  one-click restoration to recommended defaults. It is guarded by
  `settings.manage`.
- Forum taxonomy options now include the public default posting category slug
  and public tag-policy controls. Updating `forum.default_category_slug`
  requires `category.manage`; updating `forum.tags.*` options requires
  `tag.manage`.
- Forum pagination options are public runtime values named
  `forum.pagination.topics_per_page` and
  `forum.pagination.comments_per_page`. Both default to 20, accept 1-100,
  require `settings.manage` to update, and participate in the forum settings
  one-click reset.
- Forum content-limit options are also public runtime values under
  `forum.topics.*`, `forum.comments.*`, `forum.tags.min_per_topic`, and
  `forum.reading.excerpt_rune_limit`. They require `settings.manage` (tags
  min/max require `tag.manage`) and participate in the forum settings reset.
- Wave 1 community policy pack (2026-07-12) lives mainly in
  `community_policy_options.go` (init-appended definitions) plus forum/identity
  adapters:
  - Registration: `identity.registration.mode`
    (`open|invite|approval|closed`), email-verify flags; non-`open` modes
    close public self-registration for now (invite/approval flows later).
  - Username: min/max length, charset, reserved names.
  - Login lockout: `identity.login.max_failures` /
    `identity.login.lockout_minutes` (Redis-backed).
  - Newcomer trust: `trust.new_user_*` cooldowns, daily caps, outbound-link /
    attachment forbids.
  - Maintenance: `site.maintenance.enabled|message` (middleware blocks non-admin
    writes; auth + `/admin/*` remain available to turn it off).
  - Forum reading/behavior: `forum.guest.read`, list default sort/hot window,
    author close/delete, edit marks, duplicate title policy, soft-delete
    visibility, mentions.
  Recommended defaults + validation + public exposure follow the same
  beginner-friendly pattern as other option groups.
- Wave 2 brand & legal (2026-07-12) lives in `site_brand_options.go`:
  - Brand assets (public): `site.logo_url` / `site.logo_attachment_id`,
    favicon and apple-touch URL + attachment id pairs. Empty logo/favicon
    values resolve to the Core `/brand/sforum-logo.svg` asset in the Web
    presentation layer; Apple Touch icon remains unset when empty.
  - Legal Markdown stubs (public): `legal.terms|privacy|guidelines.body.zh-CN|en-US`
    with recommended short stubs and 50k-rune cap.
  - Structured public chrome (nav, friend links, announcements) is **not** in
    `web_options`; see SiteChrome module / migration
    `202607120003_site_chrome.sql` and admin personalization tabs (panel
    `apps/web/app/components/admin/SFAdminSiteChromePanel.vue`).
  - Announcement bodies remain Markdown in `body_zh_cn` / `body_en_us`.
    SiteChrome derives `bodyHtmlZhCN` / `bodyHtmlEnUS` with the shared safe
    Markdown support renderer for API presentation; no client-authored HTML is
    trusted and no announcement data migration is required.
- Configurable public navigation is a Core-owned multi-location document, not
  a theme setting. M1 added additive migration `202607280072` for definitions,
  placements, document revision, and future snapshots; it migrates legacy
  `site_nav_items` into `public.topbar.primary` without changing that table or
  its API-LTS endpoint. The canonical `GET /site/navigation` resolver returns
  four locations with locale/actor/permission filtering, exact-artifact
  Navigation Registry contributions, and active-theme support status. M6 binds
  this status to validated location metadata from the exact active theme
  runtime snapshot; Core emergency fallback supports all v1 locations. M2 now
  provides `settings.site.manage`-guarded document CAS,
  defaults/import previews fenced by bounded actor-bound server tokens,
  newest-20 snapshots, restore, and portable backup. `Options` exposes this
  cross-domain write only through `NewPublicSurfaceRevisionTxBumper`: its
  transaction update commits with SiteChrome and the Options cache invalidates
  only after success. M4 adds nullable snapshot `actor_user_id` attribution and
  exposes these commands through the existing Personalization document owner;
  old snapshots remain valid and unattributed. M5 makes the public navigation
  response actor-sensitive and explicitly `private, no-store`; Page ViewModels
  and Web topbar/mobile now consume the same canonical resolver rather than
  the legacy flat projection. M6 adds canonical sidebar/footer consumers and
  keeps the dynamic taxonomy payload in Forum. Migration `202607290074`
  materializes missing topbar/sidebar/mobile recommended placements so admin
  and public reads share stored placement truth; the resolver no longer injects
  missing defaults. Footer navigation defaults to empty while copyright and
  friend links retain their existing owners. The Personalization editor lets
  operators override a Core link's location-scoped label, icon, and visibility
  without changing its route identity, explicitly suppress its icon, and clear
  those overrides back to the code-owned defaults from the same edit dialog. See
  `../decisions/2026-07-27-operator-owned-public-navigation.md` and
  `../plans/2026-07-27-configurable-public-navigation-platform.md`.
  The 2026-07-31 responsive-sidebar amendment makes
  `public.sidebar.primary` the shared desktop/mobile product source and removes
  the independent mobile location from the operator editor and Web request.
  Stored `public.mobile.primary` data remains accepted but inert for V1
  compatibility; see `../decisions/2026-07-31-responsive-public-sidebar.md`.

## Feature flags (F4.5)

- Host catalog under `features.*` (search, registration, attachments, mentions,
  public_profiles, webhooks). Distinct from RBAC: flags kill product surfaces;
  permissions answer who may act when the surface is on.
- Admin: `GET/PUT /admin/features`, `POST /admin/features/restore-defaults`.
- Public `GET /web-options` includes only `public: true` flags (webhooks is
  admin-only).
- Plugins may declare `requiresFeatures`; enable fails if any flag is off.

## Page Registry / runtime theme flags

Registered in `apps/api/app/Models/Options/pages_registry_options.go`. Distinct
from `features.*` product surfaces. Public keys so the host can decide outlet /
skin behavior without admin session.

| Key | Default (post-P5) | Role |
| --- | --- | --- |
| `pages.registry_enabled` | `true` | Page catalog resolve + admin Pages UI |
| `themes.runtime_l0_enabled` | `true` | L0 CSS/assets without Nuxt rebuild |
| `themes.runtime_l1_enabled` | `true` | L1 template replace/add path |

## Boundaries

- Runtime options are for self-hosted operator-managed settings such as site
  identity, site URL, enabled locales, default locale, registration policy,
  content policy, appearance preferences, and CAPTCHA provider configuration.
- Do not move infrastructure or secret settings into `web_options`: database
  URLs, Redis passwords, Meilisearch master keys, worker counts, ports, and
  build-time route prefixes should remain in environment config.
- Admin-managed secrets in `web_options` are limited to approved runtime
  provider secrets such as ALTCHA and attachment storage credentials. They are
  never returned by public APIs and are masked in admin API responses; admins
  only see whether a secret is configured.
- Keep the table intentionally simple (`name`, `value`). Add typed validation in
  the Options service instead of adding per-option columns prematurely.
- Expose only settings that are safe for the frontend to read through public
  `GET /web-options` responses.
- Avatar settings are product behavior, not attachment-provider settings. They
  live in `web_options` under `avatar.*`, use `settings.manage`, and keep
  provider-specific storage behavior in the attachment module.
- 插件设置面的**治理覆盖**（哪个插件 tab / 字段在哪个表面可见、排第几）刻意**不**放进
  `web_options`，而是独立表 `settings_surface_items`（2026-11-13）。三条理由：
  (1) options 是「一个键一整块值」的写入，两个管理员同时关两个不同插件的 tab 会互相覆盖，
  独立表是行级写入；(2) 治理是宿主治理态（谁还能看见什么），没有公开读面、不该被
  「恢复默认设置」顺手清掉；(3) 行上要记 `updated_by_user_id` 并允许悬空（插件升级删掉
  一个 tab 后旧覆盖行不阻断升级，读取侧自然失效）。
  该表是 **sparse overlay**：无行=manifest 默认，`enabled=false`=运营关掉，删行=恢复默认，
  因此永远可以安全清空，也不需要任何初始化投影。
  不建 `sforum_core_v1` 视图、不建 HostQuery —— 它不是要给插件读的产品数据，
  与 `decisions/2026-10-18-extension-data-surface-policy.md` 同口径。
  决策：`decisions/2026-11-13-settings-surface-contributions.md`。
- 与之相邻但不同层：插件 Settings Document（站点级配置，走插件设置存储、有推荐默认与
  一键恢复默认）仍然不是 `web_options`；`web_options` 继续只装宿主自己拥有的运行期选项。

## Implementation Notes

- Current public options are `site.name`, `site.url`, `site.domain`, `site.tagline`,
  `site.default_locale`, `site.supported_locales`, `site.timezone`,
  `site.date_format`, `site.time_format`, `site.start_of_week`,
  `human_verification.provider`,
  `human_verification.scenarios.register`,
  `human_verification.scenarios.password_reset`,
  `human_verification.scenarios.email_verification`,
  `human_verification.scenarios.login_risk`,
  `human_verification.scenarios.post_risk`,
  `human_verification.login_risk.always`,
  `human_verification.altcha.widget.type`,
  `human_verification.altcha.widget.auto`,
  `human_verification.altcha.widget.display`,
  `human_verification.altcha.widget.hide_logo`,
  `human_verification.altcha.widget.hide_footer`,
  `human_verification.altcha.widget.workers`,
  `human_verification.altcha.widget.min_duration_ms`, `identity.password.min_length`,
  `identity.password.max_length`, `identity.password.require_lowercase`,
  `identity.password.require_uppercase`, `identity.password.require_number`,
  `identity.password.require_symbol`, `identity.registration.enabled`,
  `appearance.theme`,
  `footer.copyright.zh-CN`, `footer.copyright.en-US`, `footer.links`,
  `forum.default_category_slug`, `forum.tags.creation_mode`,
  `forum.tags.public_pages`, `forum.tags.max_per_topic`,
  `attachment.upload.enabled`, `attachment.max_file_size_mb`,
  `attachment.allowed_extensions`, `attachment.allowed_mime_types`,
  `avatar.allow_upload`, `avatar.default_provider`,
  `avatar.gravatar_base_url`, `avatar.gravatar_hash_algorithm`,
  `avatar.max_size_kb`, `avatar.allow_gif`, and
  `avatar.compress_enabled`.

- 昵称策略（2026-10-20）：`profile.nickname.allow_user_change`（默认开）、
  `profile.nickname.cooldown_minutes`（默认 `10080` = 7 天，`0` = 不限，精确到分钟）、
  `profile.nickname.min_length` / `max_length`（默认 2 / 24，按 rune）、
  `profile.nickname.history_public`（默认关）。五项全部 public，由
  `settings.site.manage` 管理，后台入口在站点设置 → 注册与用户名（昵称区块）。
  归一化保证 `max_length >= min_length`，否则双双回落默认。保留词不另立选项，
  直接复用 `identity.username.reserved`。决策：
  `decisions/2026-10-20-nickname-policy.md`。

- `human_verification.scenarios.password_reset` and
  `human_verification.scenarios.email_verification` default to enabled across
  the API, public-client fallback, and admin form. The verification provider
  itself remains disabled until an operator configures ALTCHA and its secret,
  so these defaults do not block a fresh installation.
- Login hardening triggers default to disabled and are gated by
  `human_verification.scenarios.login_risk`. `human_verification.rate_limit.per_minute`
  is bounded to 1–600 with a recommended default of 60 and replaces the former
  hardcoded challenge/verify limit. Decision:
  `decisions/2026-10-22-login-human-verification-triggers.md`.
- Current admin-only options are `site.admin_email` (operator contact, not
  SMTP From and not public), `human_verification.altcha.secret`,
  `human_verification.altcha.challenge_ttl`,
  `human_verification.altcha.cost`,
  `human_verification.login_risk.new_device`,
  `human_verification.login_risk.privileged`,
  `human_verification.rate_limit.per_minute`, attachment provider selection, path
  template, public base URL, default visibility, cleanup retention, all
  provider-specific credential/connection options, and avatar-only processing
  knobs `avatar.default_static_url`, `avatar.max_dimension`,
  `avatar.target_dimension`, and `avatar.compress_quality`.
- Manual identity-mail limits are admin-only options under
  `identity.mail_resend.*`: `cooldown_seconds=30`, `window_minutes=60`,
  `max_per_target=3`, and `max_per_ip=10`. They are resolved at request time by
  a focused Options adapter, apply to explicit password-recovery and
  email-verification sends, and are intentionally absent from public
  `web-options`.
- Startup environment values are treated as first-run defaults/fallbacks.
  `bootstrap.NewAPI` calls `EnsureDefaults` so missing option rows are inserted
  without overwriting existing admin-managed values.
- `web_options` uses `name` as the primary key. This makes single-option reads
  and upserts index-backed.
- The Options service returns defaults when rows are missing, so fresh installs
  and partially migrated databases still have a usable site name.
- The frontend root component loads public options once and falls back to
  defaults if the API is temporarily unavailable.
- Human-verification scenario options are public because pages need to decide
  whether to render the ALTCHA widget. API verification remains authoritative:
  the runtime verifier skips disabled purposes and enforces enabled purposes.
- ALTCHA widget behavior options are public frontend configuration for the
  web component only. They cover safe display/trigger knobs (`type`, `auto`,
  `display`, hide logo/footer, workers, and minimum duration); server
  challenge creation and token verification are still controlled by the API.
- SEO settings are frontend-safe public options under `seo.*`. They cover
  default meta/social tags, search platform verification tokens, robots
  additions, sitemap generation, and structured-data toggles. Runtime output
  still applies local/preview noindex protection based on `site.url`.
- Attachment settings use `web_options` for product runtime behavior,
  secret-masked provider credentials, and the local provider filesystem root.
  `attachment.local.root` defaults to `storage/app/attachments`; deployments
  should mount storage there or update the option to a prepared writable path.
- Locale settings can only enable built-in locale catalogs (`zh-CN`, `en-US`);
  adding a new locale still requires adding frontend and backend translations.
- Site datetime display options are public runtime values managed under
  Site Settings → Basic. Defaults: `site.timezone=UTC`,
  `site.date_format=Y-m-d`, `site.time_format=H:i`, `site.start_of_week=1`
  (Monday). Timezone must be a valid IANA name resolved through
  `app/Support/Timezone` (`timezone.Load`); date and time formats are
  whitelist presets. Storage remains UTC; frontend `useSiteDateTime()` /
  `utils/siteDateTime.ts` format timestamps for display. Admin UI supports
  one-click restore of recommended datetime defaults.
  **Runtime invariant (2026-10-18):** the `Timezone` package blank-imports
  `time/tzdata`, so API/worker binaries keep resolving non-UTC zones inside
  images without `/usr/share/zoneinfo` (alpine/scratch/distroless). Host-side
  code must call `timezone.Load`, never `time.LoadLocation` directly; without
  the embedded database every non-UTC timezone save fails with HTTP 422
  `options.invalid` in containers. Guard:
  `tests/validate-runtime-tzdata.mjs`. Decision:
  `decisions/2026-10-18-embed-tzdata.md`.
- `site.tagline` is optional public short text (max 160 runes) for navbar/auth
  branding; it is not SEO description (`seo.home.description`). Default theme
  `SFNavbar` shows it under the site name when non-empty.
  `site.admin_email` is optional admin-only contact email (RFC address form,
  max 254), distinct from mail provider From address. It is the default
  recipient for `POST /admin/mail/test` when the request omits `recipient`,
  and the mail admin UI prefills that field from admin web-options.
- Admin datetime surfaces (attachments, search rebuild, extension events /
  releases) format timestamps via `useSiteDateTime()` so they follow
  `site.timezone` / date / time presets instead of browser `toLocaleString`.
- Appearance preset settings use `appearance.theme` as a single public option.
  The stored key is unchanged for compatibility, but user-facing UI/docs should
  call it "配色预设 / appearance preset" rather than "theme". It accepts preset
  keys (`pine_teal`, `ocean_blue`, `violet`, `rose`, or `amber`) and the
  controlled custom format `custom:#rrggbb`.
- Daytime background remains a Core-owned Personalization option named
  `appearance.light_background`; it is not part of a built-in theme's settings.
  The 12 accepted presets are `pure_white` (recommended), `porcelain`, `paper`,
  `parchment`, `mist_gray`, `cool_frost`, `cloud_blue`, `mint_mist`, `sage`,
  `sakura`, `lilac_mist`, and `morning_apricot`. The same light palette drives
  public and admin surfaces, while resolved dark mode keeps its existing theme
  tokens unchanged. The Personalization appearance tab previews accent and
  daytime-background edits through client-only admin state. This preview never
  updates `web_options`, is ignored outside admin routes, and is cleared when
  the appearance surface deactivates; only the explicit save action persists
  and publishes the selected values.
- Footer settings are frontend-safe public options: copyright text supports
  `{year}` and `{siteName}`, and `footer.links` stores the fixed Terms,
  Privacy, and Guidelines links as normalized JSON.
- Forum taxonomy defaults are frontend-safe public options:
  `forum.default_category_slug=general`,
  `forum.tags.creation_mode=controlled`, `forum.tags.public_pages=enabled`,
  and `forum.tags.max_per_topic=5`. The Options service validates tag creation
  mode as `controlled`, `review`, or `open`; public tag pages as
  enabled/disabled; max tags per topic as `0..10`; and default category as a
  slug-shaped string.
- Password policy defaults are frontend-safe public options:
  `identity.password.min_length=12`, `identity.password.max_length=128`, and
  all composition requirements disabled. The Options service validates minimum
  length as `8..128`, maximum length as `64..512`, requires
  `max_length >= min_length`, and normalizes composition toggles to
  `enabled`/`disabled`.
- Open registration is `identity.registration.enabled` (public, default
  `enabled`), managed under Site Settings → Account security. When disabled,
  `POST /auth/register` returns `403 auth.register_disabled` after the site
  already has users. Bootstrap (no users yet) always allows registration so a
  fresh install cannot lock itself out. Public `GET /auth/registration-status`
  exposes `registrationEnabled` with that bootstrap override; it still never
  leaks the super-admin bootstrap window via `nextUserIsInitialSuperAdmin`.
  The admin selector gives a valid `identity.registration.mode` precedence; for
  legacy rows without that key it derives `open` or `closed` from the existing
  `identity.registration.enabled` value.
- Avatar defaults are upload enabled, local initials fallback, Gravatar base
  URL `https://gravatar.com/avatar/`, Gravatar hash `sha256`, GIF disabled,
  compression enabled, max upload 2048 KB, source max edge 2048 px, output
  256x256, and JPEG quality 85. `static` fallback is valid only when
  `avatar.default_static_url` is configured.

## Next Steps

- Configurable public navigation M0-M6 is complete. The production HTTP API
  and Page Registry share one `pageSiteChromeService`, and its mutation path
  uses an isolated transaction-capable Options collaborator rather than adding
  another legacy `Options.Service` receiver. Preserve `/site/nav-items` and
  `site_nav_items` as API-LTS compatibility surfaces. The Personalization
  editor now owns one local revisioned document across all four locations,
  with a shared icon picker and one explicit batch save. Its recovery panel
  now uses the same M2 API for confirmed defaults, actor-attributed snapshots,
  portable export, and fenced merge/replace import. Topbar, mobile, and Core
  fallback now consume one canonical actor/locale-sensitive SSR authority.
  Sidebar dynamic categories, footer links, exact active-theme location
  capabilities, and both built-in declarations are complete; M7 owns the final
  lifecycle, recovery, and release gate. See
  `../sessions/2026-07-28-public-navigation-platform-handoff.md`.
- The `core.dynamic.categories` placement accepts `maxItems=0..100` in the
  public-navigation document. `0` is the recommended backward-compatible
  default and means show all categories; positive values bound the shared
  sidebar category renderer. Only dynamic block placements may use a non-zero
  value, and the database column defaults to zero for existing documents.
- Wave 3+ richness blueprint: engagement switches, category policy, safety
  depth (see `knowledge/plans/2026-07-12-admin-settings-richness.md`).
- If settings need audit history, write changes to the existing audit event
  pattern instead of adding columns to `web_options`.
- When `sqlc` is available in the local toolchain, generate typed query methods
  for `database/queries/options.sql` and replace the small pgx adapter queries.

## SEO Workbench V2 P0 (2026-07-11)

- Product identity and search identity are separate. `site.name` remains the UI
  application name; `seo.site.inherit_site_name` and `seo.site.name` resolve the
  SEO brand name.
- Homepage settings use `seo.home.{title,description,keywords,og_title,
  og_description,og_image_url}`. Inner-page fallbacks use `seo.page.*`.
- Category, tag, topic, profile, and static-page policies use
  `seo.content_type.<type>.*` with title template, description sources, default
  image, index mode, sitemap inclusion, and schema type.
- Unknown template variables and invalid policy enums are rejected by the
  Options service. Existing v1 meta values remain compatible fallbacks.
- The v2 recommended default enables forum-content Sitemap generation. Existing
  stored operator values are not overwritten.
- `seo.content_type.*.schema_type` uses `normalizeStringChoice` (EqualFold +
  canonical PascalCase). Do not use `normalizeChoice` here: that helper
  lowercases the input and exact-matches against the allow-list, so legal
  Schema.org values like `CollectionPage` were always rejected and blocked
  every admin SEO save (`options.invalid`).
