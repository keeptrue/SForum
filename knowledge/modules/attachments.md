# Attachments Module

## Purpose

Owns user-uploaded files, storage provider configuration, attachment metadata,
derived display variants, admin governance, and orphan cleanup.

## Current Status

Attachment system foundation is implemented.

- **Attachment reference uniqueness is scope-aware (2026-10-24)**: topic/comment
  body references are multi-value (one post may bind many attachments, guarded
  by the base-table `UNIQUE (attachment_id, resource_type, resource_id, context)`),
  while site/seo/user/extension slots stay one-image-per-slot through the
  partial `attachment_references_slot_context_unique` index. The former global
  `(resource_type, resource_id, context)` index from `202607110015` made every
  multi-image post fail with an undiagnosable 500; see
  `../decisions/2026-10-24-attachment-reference-uniqueness-scope.md`. Forum-side
  writes live in `app/Models/Forum/attachment_references_postgres.go` with
  idempotent `ON CONFLICT DO NOTHING`, ordered row locking, recount-based
  `reference_count` maintenance, and typed 422 `forum.attachment_unavailable` /
  409 `forum.attachment_conflict` mappings instead of raw 500s.
- General attachment uploads now resolve an authoritative typed upload policy:
  `attachment.upload` controls eligibility, a user-specific per-file limit
  overrides role limits, and multiple upload-enabled roles use their largest
  configured or inherited limit. The result is always capped by the site
  setting and `HTTP_BODY_LIMIT` minus a 1 MiB multipart reserve. Avatar, SEO,
  and brand upload paths retain their specialized policies.
- Attachment Configuration includes an Upload Permissions tab. Operators with
  `attachment.upload_policy.manage` can set/reset role and user size limits;
  existing `role.manage` and `user.permission_override` remain required for
  changing the corresponding RBAC upload grants. User search additionally
  requires `user.view`.
- The approved custom image sticker design will reuse the selected attachment
  storage adapter and Media Registry through a dedicated `sticker` purpose.
  Sticker assets will not become user-owned post attachments, and their
  revision-history retention remains owned by the future sticker domain. See
  `../plans/2026-07-30-image-sticker-platform.md`.
- Backend module `apps/api/app/Models/Attachments` owns upload rules,
  metadata, settings DTO conversion, admin list/detail/status/delete actions,
  and orphan cleanup.
- HTTP controller `apps/api/app/Http/Controllers/Attachments` exposes user and
  admin APIs under `/api/v1`.
- Storage provider adapters live under `apps/api/app/Support/Storage`.
- Migration `202607050004_attachments.sql` creates `attachments`,
  `attachment_references`, and the attachment permissions. Migration
  `202607300001_attachment_storage_instances.sql` adds Host-owned named storage
  instances with revisioned configuration and probe state. Migration
  `202607300003_attachment_image_compression.sql` adds display variants and the
  durable compression-task ledger. Migration
  `202607310002_attachment_upload_policies.sql` adds role/user upload-size
  policies plus the dedicated management permission.
- Admin UI uses independent permission-aware routes for Basic Configuration
  and Compression Configuration under Attachment Configuration
  (`/attachments/settings`), plus Attachment Management
  (`/attachments/manager`). The legacy `/attachments` route redirects to the
  requested or first accessible child. Their implementations remain
  independent components under `components/admin/settings/attachments/tabs/`.
- Basic Configuration retains the storage/upload form. Image Optimization owns
  the Host JPEG/PNG display policy, recommended-default reset, adjacent size
  estimate, task and savings statistics, and explicit history backfill.
- PNG display variants keep pixel values lossless. The processor uses the
  highest standard-library Deflate level at the recommended strength and
  converts images with at most 256 exact RGBA colors to indexed PNG, including
  transparency; high-color PNGs retain true color and transparently fall back
  to the original when the configured savings threshold is not met.
- Ordinary proxied JPEG/PNG metadata returns an authorized `display` variant
  URL. Missing, stale, disabled, unreadable, or unprofitable variants fall back
  to the immutable original; explicit site-public CDN assets keep their direct
  provider URL.
- Forum editor image uploads return the stable browser-facing
  `/media/attachments/{publicId}` URL. Nuxt proxies this alias, including the
  current session, to the authorized `display` variant endpoint; the API keeps
  variant-to-original fallback authoritative. Historical `/api/v1` attachment
  content URLs remain accepted when existing editor documents are loaded.
- Rich-content viewers use the same actor-authorized boundary through
  `/media/attachments/{publicId}/original`. The alias is only opened by an
  explicit viewer action and never exposes provider URLs or bypasses visibility
  checks.
- The production Route Guard applies the same attachment/reference authority to
  metadata, original-content, and display-variant routes. Public forum media is
  anonymously readable only when `forum.guest.read=public`; private, pending,
  hidden-category, disabled, and login-required resources remain protected.
- The stable `core.component.page.admin.attachments` Admin Surface placement is
  mapped to Attachment Management so existing governance extensions continue
  to render after the route split.
- Settings owns provider selection, named multi-instance configuration,
  connection probes, one-click writer switching, secret-preserving edits, and
  the beginner-friendly local-upload defaults.
- Multi-instance provider manifests are configuration schemas, not directly
  selectable writers. The Admin provider picker excludes their bare
  `plugin:<extensionId>` value and only accepts a configured
  `instance:<uuid>`; the API enforces the same rule.
- Multi-instance storage plugins expose a **Configure storage** action in the
  plugin list. It deep-links to Attachment Configuration and opens the
  provider-specific instance editor after candidates load. New built-in
  storage providers start installed rather than enabled, while existing
  statuses and historical filesystem-plugin attachment readers are preserved.
- Basic Configuration labels the selector as the current write target and
  groups built-in storage, configured instances, and compatibility plugins.
  When no instance exists, the selector explains whether the operator must
  enable a multi-instance plugin or add an instance instead of implying that
  the bare S3 plugin is directly selectable.
- Storage-provider fields now preserve the request locale from Attachment
  Settings through the provider Schema projection. Provider manifests may
  declare required fields; Admin marks and validates them before save or draft
  probe, while the API remains authoritative. Draft probes use unsaved values,
  display a persistent in-dialog result, and write/read/delete a temporary
  object without first creating the instance.
- Storage-instance strings live under `admin.attachments.storageInstances` in
  both frontend catalogs. Probe responses keep `reason` as a stable machine
  code, return a request-locale `message`, and persist a raw diagnostic only
  for the historical probe record.
- Storage-instance deletion returns the standard `200` API envelope with
  `data.deleted = true`; it must not return `204` because the shared Nuxt API
  client expects every successful mutation to include an envelope.
- The storage-instance section consumes the same current writer value as the
  main settings form: its local-switch command is hidden for `local`, and a
  successful writer switch updates the form selector before server refresh.
- Basic Configuration keeps guidance visible below every input, displays MB
  and day units inline, documents list formats and path-template tokens, and
  explains provider defaults, public URLs, and credential retention.
- Manager owns filters, server-backed button pagination, detail/reference
  loading, status changes, soft delete, orphan cleanup, and URL copy. Filters
  restart at page one, while cleanup recovers to the last available page when
  the current page becomes empty.
- Site brand uploads reuse the attachment provider and validation pipeline
  through `POST /admin/site/brand-assets`, but are authorized by
  `settings.site.manage` and forced to active public images. Saving the matching
  web option establishes the existing `site` attachment reference.
- Brand SVG input is capped at 2 MB, parsed with `oksvg`, rendered through
  `rasterx` to a non-empty transparent PNG with a maximum 1024-pixel edge, and
  only then enters storage. The global active-content SVG rejection remains in
  force for every ordinary attachment path.
- OpenAPI contract includes upload, metadata, content, admin settings, provider
  test, list, detail, update, soft delete, and cleanup endpoints.

## Permissions

- `attachment.upload`: upload attachments. Granted to the default `member`
  role and to `super_admin`.
- `attachment.manage`: list, inspect, disable, restore, soft-delete, and clean
  up attachments. Granted to `super_admin`.
- `attachment.settings.manage`: manage storage provider and upload settings.
  Granted to `super_admin`.
- `attachment.upload_policy.manage`: manage per-role and per-user upload size
  limits. Granted to `super_admin` and the built-in `operator` template.

Role upload grants still require `role.manage`; direct user allow/deny
exceptions still require `user.permission_override`. A size-policy manager
cannot grant upload permission through the attachment policy APIs.

Backend policy checks are authoritative. Frontend visibility only mirrors these
permissions for navigation and tab usability.

## Data Model

- `attachments` stores public id, owner, provider, object key, original file
  name, MIME type, extension, size, SHA-256, optional image dimensions,
  visibility, status, reference count, timestamps, and soft-delete timestamp.
- `attachment_references` records business references such as post, topic,
  avatar, or future resource types.
- `reference_count > 0` blocks physical deletion. Referenced attachments can be
  disabled/hidden, but not physically removed by cleanup.
- `attachment_storage_instances` stores a UUID, owning extension, display name,
  revisioned non-secret settings plus SecretStore references, probe state, and
  audit timestamps. An instance cannot be deleted while selected or referenced
  by any attachment.
- `attachment_variants` stores provider/object identity, size, checksum,
  dimensions, source checksum, and policy digest for named derived bytes.
- `attachment_compression_tasks` persists pending/running/succeeded/skipped/
  failed state independently from River delivery and deduplicates a policy run.
- `attachment_role_upload_policies` and `attachment_user_upload_policies`
  store positive byte limits with target and updater foreign keys. Policy
  changes are actor-bound and append audit events in the same transaction.

## Provider Slot (F3.5 → E6)

Host contract slot: `attachment.storage.provider` (`Support/Storage.ProviderSlot`).

**Decision (E6.0 accepted):**
`knowledge/decisions/2026-07-12-attachment-storage-plugin-provider.md`

| Stage | Status |
| --- | --- |
| E6.0 contract + selection encoding | **Done** — `plugin:<extensionId>` helpers in `Support/Storage` |
| E6.1 resolver / candidates / restore | **Done** — settings `candidates[]`, options accept `plugin:`, disable fallback to `local` |
| E6.2 chunked storage RPC | **Done** — PluginProtocol Storage* + `PluginStorageAdapter` + Manager gate/timeout |
| E6.3 admin select/test/settings polish | **Done** — plugin panel (no core secret forms), Probe `reason`, toast detail |
| E6.4 reference storage plugin | **Done** — builtin `sforum.storage-fs` (filesystem; S3-shaped RPC, no cloud SDK) |
| E6.5 named instances + S3-compatible builtin | **Done** — `instance:<uuid>`, Host SecretStore, one-click writer switching, protected `sforum.storage-s3` |

**Runtime today (L4–L6 for storage slot):** Core permanently owns `local` plus
the provider contract. Legacy single-provider plugins use
`plugin:<extensionId>`; plugins declaring `multiInstance: true` use named
Host-owned instances selected as `instance:<uuid>`. Every attachment stores the
exact selection used for its write, so switching the current writer affects
only new attachments and historical reads continue through their original
instance. Disabling the selected plugin falls back new writes to `local`.
Cross-instance copy/migration and browser direct upload remain out of scope.

## Runtime Configuration

Attachment product configuration is stored in `web_options`, including the
local provider filesystem root.

- `attachment.local.root` defines the local provider filesystem boundary. It
  defaults to `storage/app/attachments`; relative paths resolve from the API
  process working directory.
- The recommended beginner configuration is local storage, uploads enabled,
  20 MB per file, common image/PDF/TXT/ZIP allow-lists, public visibility, and
  30-day orphan retention.
- Admins with `attachment.settings.manage` can configure the local root, object
  path templates, and public URL prefixes. The API rejects empty paths,
  traversal segments, control characters, and angle brackets.
- `attachment.local.public_prefix` is an advanced static-direct-link option,
  not a required local-storage setting. Its recommended default is empty. Set
  it only when Caddy, Nginx, or a CDN already maps a public URL to the local
  object tree; authorized forum media never uses this prefix.
- Secret options are masked in admin responses. Blank secret updates keep the
  existing value.
- Public web options expose only upload knobs needed by the frontend:
  `attachment.upload.enabled`, `attachment.max_file_size_mb`,
  `attachment.allowed_extensions`, and `attachment.allowed_mime_types`.

Important runtime options:

- `attachment.provider`
- `attachment.upload.enabled`
- `attachment.path_template`
- `attachment.public_base_url`
- `attachment.max_file_size_mb`
- `attachment.allowed_extensions`
- `attachment.allowed_mime_types`
- `attachment.default_visibility`
- `attachment.cleanup_orphan_after_days`
- `attachment.local.root`
- `attachment.local.public_prefix`
- `attachment.compression.enabled`
- `attachment.compression.strength`
- `attachment.compression.max_dimension`
- `attachment.compression.min_size_kb`
- `attachment.compression.min_savings_percent`

## Storage Providers

SForum owns a small `StorageAdapter` interface:

- `Put`
- `Open`
- `Delete`
- `Stat`
- `Exists`
- `PublicURL`
- `SignedURL`
- `Probe`

Supported providers:

- `local`: permanent zero-configuration Core filesystem fallback under
  `attachment.local.root`.
- `sforum.storage-fs`: protected single-instance filesystem reference plugin.
- `sforum.storage-s3`: protected multi-instance AWS SDK v2 plugin for AWS S3,
  MinIO, Cloudflare R2, and compatible endpoints. It supports endpoint/region,
  tri-state path-style addressing (auto/true/false), prefix, public base URL,
  static credentials, session tokens, and the AWS default credential chain.
  Auto addressing resolves Path-style for any configured non-AWS endpoint and
  Virtual-host for AWS; request/response checksums are computed only when the
  API requires them because R2 rejects the SDK default CRC32 request checksum;
  an empty region resolves to `auto` for R2 endpoints; object read bodies are
  no longer bounded by an overall deadline (connect/TLS/header timeouts live on
  the shared HTTP transport). See
  `../decisions/2026-10-24-s3-compatible-auto-addressing.md`.

The former protected FTP and SFTP built-ins are removed. Vendor credentials for
named instances are encrypted by Host SecretStore; instance documents and API
responses never contain plaintext secrets.

The first version uses server-mediated multipart upload only. Browser direct
upload and presigned upload credentials are intentionally deferred.

## Upload And Cleanup

- Upload requires a logged-in active actor with `attachment.upload`.
- The API enforces configured max size, allowed extensions, and allowed MIME
  types.
- Avatar uploads reuse the attachment storage pipeline, but apply avatar
  runtime rules (`avatar.max_size_kb`, `avatar.max_dimension`,
  `avatar.allow_gif`, `avatar.compress_enabled`, `avatar.target_dimension`,
  and `avatar.compress_quality`) instead of the general attachment allow-list.
  Avatar attachments are always stored with public visibility.
- Compressed JPEG/PNG avatars use `golang.org/x/image/draw` for center-crop and
  resize. A bounded EXIF Orientation read preserves phone-photo rotation, while
  the transform decoder rejects every format except JPEG/PNG and does not
  register TIFF support. GIF bypass behavior remains governed by
  `avatar.allow_gif`.
- The service generates a random public id and object key from the configured
  path template, streams the object to the provider, and computes SHA-256 during
  upload.
- Eligible JPEG/PNG uploads enqueue a durable `display` task after metadata
  creation. Processing is bounded to 64 MB and 80 million source pixels,
  applies EXIF orientation, optionally resizes proportionally, and stores output
  only when the configured minimum savings is reached.
- River job `attachments.compress_image` processes a claimed task. The
  one-minute `attachments.reconcile_compression` schedule compensates failed
  queue insertion and stale 15-minute leases; three attempts leave an
  observable terminal failure.
- If metadata insert fails after object upload, the service best-effort deletes
  the remote object.
- Admin delete is soft delete. Orphan cleanup only physically deletes rows with
  `status = deleted`, `reference_count = 0`, and `deleted_at` older than
  `attachment.cleanup_orphan_after_days`.
- **Cleanup result is explicit about what it did not do (2026-10-27)**:
  `CleanupResult` carries `deleted`, `failed`, `skipped`, and `retentionDays`.
  `skipped` counts zero-reference rows still inside the grace window
  (`status <> 'deleted' AND created_at > cutoff`), so "deleted 0" is never
  mistaken for "no orphans"; `retentionDays` repeats the configured window in
  the response, so an operator holding only `attachment.manage` can explain the
  wait without calling `GET /admin/attachment-settings` (which needs
  `attachment.settings.manage`). Default window stays **30 days**.
- Two different orphan scopes exist and must not be conflated:
  the admin overview KPI counts `reference_count = 0 AND status <> 'deleted'`
  (so it **includes `disabled` rows**), while cleanup only removes `deleted`
  rows past `deleted_at` and `active` rows past `created_at`. A `disabled`
  zero-reference row is therefore counted as an orphan but is never a cleanup
  candidate — it stays until an operator acts.
- The cleanup request body is decoded defensively (2026-10-27): `limit` is a
  pointer, an empty body falls through to the service default, and malformed
  JSON returns `400 attachments.cleanup_body_invalid` instead of being swallowed
  into `limit = 0` (which the service normalizes to 100 rows of deletion).
- Profile avatar replacement writes `attachment_references` with resource
  `user` and context `avatar`, decrementing the old avatar reference and
  incrementing the new one so referenced avatars are not orphan-cleaned.
- Browser-rendered avatars use the Host-owned
  `/media/avatars/{publicId}` route. Nuxt proxies it without request
  credentials to the authoritative attachment content endpoint; only a
  backend-authorized anonymous `200` receives immutable public caching.
- Forum editor images use the existing upload API and policy, including RBAC,
  effective size caps, type allow-lists, selected storage provider, and image
  compression. Topic/comment content submits explicit
  `content.attachmentIds`; the native image node also stores the matching
  attachment ID and public ID. The API validates that the node URL identity and
  reference list agree before the forum transaction replaces references and
  updates `reference_count`.
- Cross-author edits may retain attachments already referenced by that same
  topic/comment, but cannot bind another user's unrelated attachment. Deleting
  a topic/comment clears its references. Active uploads that never become
  referenced are eligible for the same retention-based orphan cleanup as
  explicitly deleted attachments; migration
  `202607310003_attachment_active_orphan_cleanup.sql` indexes this path.
- Forum content revisions V1 can expose historical attachment ID summaries
  through authorized revision detail reads. Revision rows store IDs only, never
  bytes, URLs, credentials, object keys, checksums, or provider internals.
  Restore only rebinds IDs from the selected immutable revision that are still
  active and valid for the original resource
  author/current visibility policy; unavailable IDs fail closed with
  `forum.revision_attachment_unavailable` and no partial write.
- Public attachment reads resolve real topic/comment/post status, category
  visibility, author, and reviewer permission. Pending is author/reviewer-only;
  hidden/deleted or hidden-category media is reviewer-only. Forum and
  unreferenced media always use the API proxy, while avatar/SEO/site assets may
  use permanent provider URLs.
- Logo/favicon/apple-touch-icon options atomically maintain `site` references;
  migration `202607130001_site_attachment_references.sql` backfills valid
  historical option values.
- `attachments.cleanup_orphans` is defined as a River maintenance job contract;
  worker wiring can be enabled when the worker process begins registering
  module jobs.
- Extensions reuse Core attachments through three Host Commands
  (`sforum.attachments.extension.claim|release|list@1`, see below). The public
  read predicate is split: extension references are anonymously readable but
  never use a permanent storage URL.

## API

User-facing:

- `POST /api/v1/attachments`
- `POST /api/v1/profile/avatar` for avatar-specific image upload and profile
  attachment.
- `GET /api/v1/attachments/:publicId`
- `GET /api/v1/attachments/:publicId/content`
- `GET /api/v1/attachments/:publicId/variants/:variant/content`
- `GET /api/v1/attachments/upload-policy`

Admin:

- `GET /api/v1/admin/attachment-settings`
- `PUT /api/v1/admin/attachment-settings`
- `POST /api/v1/admin/attachment-settings/test`
- `GET /api/v1/admin/attachment-upload-policies/roles`
- `PUT|DELETE /api/v1/admin/attachment-upload-policies/roles/:roleKey`
- `GET|PUT|DELETE /api/v1/admin/attachment-upload-policies/users/:userID`
- `GET /api/v1/admin/attachment-compression-settings`
- `PUT /api/v1/admin/attachment-compression-settings`
- `GET /api/v1/admin/attachments`
- `GET /api/v1/admin/attachments/compression-stats`
- `POST /api/v1/admin/attachments/compression/backfill`
- `GET /api/v1/admin/attachments/:id`
- `PATCH /api/v1/admin/attachments/:id`
- `DELETE /api/v1/admin/attachments/:id`
- `POST /api/v1/admin/attachments/cleanup`
- `POST /api/v1/admin/attachments/reconcile` (`attachment.manage`; body
  `{dryRun}` defaults to a dry run when omitted)

Public attachment DTOs omit storage-only fields such as object key and SHA-256.
Admin DTOs include object key, checksum, visibility, deleted timestamp, and
references for governance.

## Reference Count Reconciliation (2026-10-27)

`attachments.reference_count` stays correct because **write paths** maintain it:
forum topic/comment writes replace reference rows and recount inside the same
transaction, avatar/SEO/site replacements delete-then-decrement in one
statement, and the extension claim path recounts under a per-extension advisory
lock. There is no background recomputation.

Historical drift can still exist (for example a row with `reference_count = 1`
but no `attachment_references` row). Such a row is exempt from orphan cleanup
forever and never shows up in any orphan count, so it is invisible debt.

`Service.ReconcileReferenceCounts` is the only correction path:

- operator-triggered only, `attachment.manage` at the service layer **and**
  `permission_any` at the HTTP layer (catalog-generated route guard);
- `dryRun` defaults to `true`, including for an empty request body, and a dry
  run issues only `SELECT`s inside its transaction;
- apply corrects only drifting rows (`WHERE a.reference_count <> live.actual`),
  so repeated runs are idempotent; `updated_at` is touched only for those rows;
- it returns `lowered` / `raised` / `unchanged` / `applied` / `samples`
  (up to 20 drifting ids, ids only — no per-row before/after values).

**Hard constraint: never call this from `cleanupOrphans` or any background
job.** Historical content may legitimately have a stored count with no
reference row (a live reference written before the reference table existed).
Silently recounting would drop those counts to zero and let the retention
window physically delete attachments that are still in use.

## Tests

Current focused coverage includes:

- attachment option permission checks, normalization, invalid path templates,
  MIME/extension validation, secret mask, and blank-secret retention;
- local adapter put/open/stat/delete and unsafe key rejection;
- upload metadata creation, SHA-256 calculation, remote rollback on DB failure,
  permission denial, invalid extension, avatar JPEG compression/public
  visibility, all eight EXIF orientations, TIFF transform rejection, GIF
  rejection when disabled, and cleanup retention cutoff;
- existing backend HTTP/options/identity tests;
- compression policy permissions and invalid input, JPEG/PNG transforms, EXIF
  orientation, minimum savings, durable task dedupe/claim/lease recovery,
  variant upsert/statistics/backfill, authorization, and original fallback;
- frontend model/Bun tests plus desktop and 390x844 Chrome rendering checks.
- upload-policy precedence, permission denial, protected super-admin targets,
  transport caps, fail-closed assembly, migration structure, 413 mapping, and
  frontend permission-override preservation;
- M6 attachment/mail focused tests, architecture validation, Nuxt typecheck,
  and production build pass. Full repository and browser closeout remain
  tracked in the architecture debt plan.
- Reference-count reconciliation (2026-10-27): dry run leaves stored counts
  untouched, apply restores the whole-table invariant
  `reference_count == count(attachment_references)`, repeat runs are idempotent,
  a missing permission yields `identity.ErrPermissionDenied`, and the
  invariant helper is count-based so an empty table passes instead of silently
  "passing" through `pgx.ErrNoRows`.
- Cleanup request decoding (2026-10-27): an empty body falls through to the
  service default, while malformed JSON returns 400 before the service is
  reached (proved at handler level by a counting store).

## Next Steps

- Wire `attachments.cleanup_orphans` into `bootstrap.NewWorker` once module job
  registration has a dependency assembly pattern.
- Add attachment reference writes from post/topic/avatar features when those
  modules land.
- Add virus scanning as a separate background capability. Consider WebP/AVIF,
  thumbnails, and plugin transforms only after a dedicated Media Pipeline
  contract decision.
- Consider WebDAV/rclone and direct browser upload only after the first
  provider set is stable.

## Extension-Owned Assets (2026-10-18)

Plugins that need an operator- or member-supplied image (application icons,
branding, integration logos) reuse Core attachments instead of inventing a
parallel store. The capability lives in
`apps/api/app/Models/Attachments/ExtensionReference`; the Host exposes it as
three Host Commands (`sforum.attachments.extension.claim|release|list@1`) and as
an uninstall hook.

- Storage contract: `resource_type = 'extension'`, `resource_id = 0`,
  `context = "<extensionId>:<pluginContext>"`. The extension id always comes from
  the verified runtime identity, never from plugin input.
- Only `image/png`, `image/jpeg`, `image/webp` under
  `min(attachment.max_file_size_mb, 512 KiB)` are claimable. The asset becomes
  anonymously readable, so SVG/HTML stay excluded.
- Claim forces `visibility = public`, refuses attachments already referenced by
  forum/avatar/SEO/site, replaces the row for the same context (a unique index
  allows one row per context globally), and enforces
  `attachment.extension_reference_limit` (default `500`).
- `reference_count` is recomputed from the reference rows under a per-extension
  `pg_advisory_xact_lock`, never blindly incremented. The engine's own advisory
  key includes the idempotency key and therefore does not serialize two
  different requests writing the same context; without the extra lock a lost
  insert still increments the count and produces an attachment that is counted
  but unreferenced — permanently exempt from orphan cleanup.
- Namespace prefix checks use `LEFT(context, length($n) + 1) = $n || ':'`, not
  `LIKE`: extension ids may contain `_`, which LIKE would treat as a wildcard.
- Read policy is split in `Models/Attachments/access.go`:
  `isAnonymousReadableReference` (extension images included, MIME re-checked at
  read time) decides visibility, while `isPermanentURLReference` (site assets
  only) decides whether the storage public URL may be returned. Extension
  references stay on the proxied `/media/attachments/<publicId>` path so admin
  lists and the attachment API never leak object-storage URLs.
- Uninstall releases every reference the extension owns
  (`WithExtensionAttachmentReferenceReaper`, wired in bootstrap); disable does
  not. Release only deletes reference rows — the stored object still waits for
  `attachment.cleanup_orphan_after_days`, which doubles as the accidental-release
  grace window.

## SEO Assets (2026-07-11)

- `POST /api/v1/admin/seo/assets` requires `seo.manage`, accepts image uploads,
  reuses the configured attachment storage provider, and always creates public
  attachments.
- SEO contexts use `attachment_references` with resource type `seo`, resource
  id `0`, and context such as `seo/home-og-image`. Replacement atomically
  decrements the prior attachment reference count and increments the new one.
- A unique `(resource_type, resource_id, context)` index prevents concurrent
  replacements from leaving multiple active references for one SEO field.
- Restoring SEO recommended defaults clears form URLs but does not delete
  uploaded attachments; the UI states this explicitly.
