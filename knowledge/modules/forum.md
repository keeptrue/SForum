# Forum Module

## Purpose

Owns category groups/categories, tags, topics, tree comments, shared content,
accepted revisions, lifecycle states, public read models, and forum policy.

## Current Status

- **点赞（2026-10-28 落地，2026-10-29 补齐，2026-11-09 补头像组预览）：** `content_reactions` 单 like、
  多态目标、计数反规范化到 `topics.like_count` / `comments.like_count`；`hot_score`
  收敛为 `TopicHotScoreSQL` 一处公式（评论/浏览/点赞三信号，评论点赞不计入）。
  读模型新增 `likeCount`、请求级 `likedByMe` 与点赞者头像组预览 `likedBy`
  （两者都只在 `PublicReadService` 缓存之后装饰，永不进缓存）。`ReactionService` 是聚焦协作者，未增长 `Service` 接收者方法。开关
  `features.reactions` 默认开，关闭时 6 条路由 403；写端点复用全局写限流
  （默认 30 次/分/IP）+ CSRF。迁移已在 dev 库应用（goose `202610280001`），稳定
  视图 `sforum_core_v1.forum_reactions` 与 HostQuery
  `sforum.core.public_reactions.list` 均已交付并有允许/拒绝测试。细节、残留与
  计数对账 SQL 见下方 Reactions 段、`../sessions/2026-10-28-content-reactions.md`
  与 `../decisions/2026-10-28-content-reactions.md`。浏览器 QA 通过（flat）；
  tree 不可达，见下方产品缺口；开关关闭证据已采集
  （`storage/qa/reactions-flag-evidence.txt`）。头像组预览的口径、缓存边界与
  两处排序差异见 `../sessions/2026-11-09-likes-avatar-stack.md` 与
  `../decisions/2026-11-09-reaction-preview-ordering.md`。
- Core taxonomy, topic/comment creation and lifecycle, public/admin UI, runtime
  settings, moderation integration, search projection, and million-scale read
  path M0-M7 are implemented.
- All `topic.*` observe events retain their legacy flat identifiers and now
  include a public-safe topic snapshot: operator-configured `url`, stable
  `path`, topic state, author username/display name, category name, and resolved
  tag names. Outbound webhooks inherit the same payload through the existing
  event bridge. Snapshot/tag lookup and live URL-option resolution are
  fail-open after the owning mutation, so enrichment cannot make a successful
  topic write fail; no email, raw content, edit reason, or private avatar data
  enters the payload.
- The shared full editor uploads images through the existing attachment API
  from either the toolbar picker or drag-and-drop. A mapped ProseMirror
  placeholder preserves the original selection/drop position while uploads run
  asynchronously, even when the author continues editing elsewhere.
- Uploaded image nodes carry Host-issued attachment identity and emit explicit
  `content.attachmentIds`. Topic/comment create and update validate the node
  URL, public ID, attachment ID, active/public state, and owner-or-existing-
  resource authority before replacing references transactionally.
- Image-only editor documents are valid topic/comment bodies after Host
  normalization. Meaningful-content validation recognizes accepted image nodes
  without inventing searchable plain text; empty documents, empty paragraphs,
  and decorative-only horizontal rules remain invalid. Comment quick reply,
  advanced reply, and edit use the same native-node-aware presence check, so
  empty text serializers cannot silently discard an image-only submission.
- Native image nodes also carry bounded dimensions and a `compact`, `standard`,
  or `wide` display mode. Topic and comment surfaces cap inline images at
  separate reading widths, while each surface opens as its own PhotoSwipe
  gallery; the inline URL stays optimized and the authorized original route is
  requested only after an explicit viewer action.
- Logged-in users with reply permission can select text in topic or comment
  content and open the existing reply drawer through a compact `引用并回复`
  action. Topic selections create top-level drafts; comment selections retain
  the direct parent so Notification V2 reaches that comment author. The action
  is anchored in topic-scroll document coordinates rather than fixed to the
  viewport, and selected text enters the editor as an HTML-escaped Markdown
  blockquote capped at 500 Unicode characters.
- Topic and comment edit save actions keep validation-blocked states visually
  disabled but clickable. A save attempt now reports semantic no-op content,
  missing cross-author reasons, empty bodies, and other required-field errors;
  only active submissions remain natively disabled. Edit reasons still count
  toward the leave-page guard but cannot enable a no-op content revision.
- A custom image sticker platform is in approved design, not implementation.
  Core will own pack/item state, immutable asset revisions, a dedicated
  `sforumSticker` editor-document node, rendering/retention, admin authority,
  and a cacheable effective catalog. Forum Canvas (direction 01) is selected
  for refinement from the standalone editor/sticker demos under
  `../../tmp/demos/sforum-editor-sticker-directions-20260730/`. See
  `../plans/2026-07-30-image-sticker-platform.md` and
  `../decisions/2026-07-30-image-sticker-catalog.md`.
- Built-in Shortcodes M0-M9 are complete and M10A (legacy conversion fixtures,
  deterministic report, and bilingual user/operator/author documentation) is
  complete; M10B remains the final lifecycle and release gate. The accepted V1
  contract covers public `user`, `topic`,
  `comment`, `category`, and `friend-links` references plus actor-sensitive
  `login`, `reply`, and comment-only `only-author`. It freezes Host-owned
  structured nodes, standalone block syntax, Markdown API conversion,
  fail-closed projection/fallback, and exact version/limit rules. Host
  validation, Goldmark/Tiptap import/paste, shared conformance fixtures, and
  lifecycle-independent disabled fallback implement that substrate. Production
  Forum post-body rendering now builds exact active `render_filter`/`sanitizer`
  bindings by registry revision and dispatches Protocol V2 under an exact
  artifact runtime lease. Host schema validation, permission recheck,
  sanitization, budgets, quarantine, source-free traces, and deterministic Host
  HTML fallback remain authoritative; plain/search/excerpt/hash never come from
  plugin output. Core Query Registry now owns seven presentation-safe V1
  projections for public users/topics/comments/categories, ordered enabled
  friend links, author decisions, and reply eligibility. Public visibility and
  actor decisions execute in Host SQL; opaque exact-runtime delegations keep
  actor identity server-side and never grant raw DB/session/content authority.
  ID batches are canonical, bounded to 32, and use one PostgreSQL array query;
  empty content graphs still keep ordinary content unchanged. M5 added the
  protected built-in `sforum-shortcodes` package with `user`, `category`
  (frozen `topic-tag` import alias), and `friend-links`: strict schemas, typed
  Protocol V2 render, one delegated query per reference call, localized labels,
  and zero public output for empty friend links. Independent review added
  parsed-DOM link/sanitizer coverage, strict path/URL/Unicode/empty-field/error
  boundaries, and exact legacy lifecycle Content publication; fresh full Go,
  staging/release, admin disable/enable/restart, cold Safe Mode, exact catalog,
  and selected-theme Browser evidence are retained in the archived M5 handoff.
  M6 added the production per-node dispatcher plus only the `topic` and
  `comment` v1.1.0 declarations/typed handlers. Public topic and comment reads
  compose safe SSR output after visibility-authorized base reads; plugin calls
  receive the strict node value, never surrounding raw source, actor, session,
  permissions, or private metadata. Resource-key stacks stop self/cyclic
  references before plugin execution, while depth, total-reference, batch,
  output, call-time, and total-time budgets bound the request. Duplicate IDs
  resolve once, public source loading batches by resource type, comments
  recheck owning-topic visibility through the frozen M4 projection, and
  topic/comment/visibility cache generations advance on authoritative state
  changes. Exact v1.1.0 runtime, disable, cold Safe Mode/recovery, raw SSR,
  source-secrecy, desktop/mobile selected-theme, and full-gate evidence is in
  `../sessions/archive/2026-08/2026-08-23-builtin-shortcodes-m6-handoff.md`.
  M7 adds the authenticated `GET /api/v1/composer/references` contract for
  ordinary topic/comment composers. It returns only minimal recognizable
  labels for public users/topics/comments/categories, plus safe user avatar
  views and category icon/color presentation metadata. User email never enters
  the response; the server may use it only while deriving the existing safe
  avatar view. The endpoint bounds query/limit/
  selected-ID input, and applies Host-owned active/public/owning-topic
  visibility in PostgreSQL. It neither exposes M4 Query Registry projections
  nor sends delegation tokens to the browser. Topic 145 and comment 368 prove
  five-node create, public render, authorized edit-source restore, update, and
  reload through the real product workflow. See
  `../sessions/2026-08-28-builtin-shortcodes-m7-handoff.md` and
  `../decisions/2026-08-22-structured-shortcodes-public-source-secrecy.md` and
  `../plans/2026-08-22-builtin-shortcodes.md`.
- Topic and comment create cooldowns remain independently configurable. A
  cooldown rejection now returns HTTP `429` with standard `Retry-After` plus
  `retryAfterSeconds` / `retryAt`; topic creation and both comment composers
  show a server-authoritative countdown while preserving editable drafts.
- Top-level quick replies use the compact inline editor and submit without
  opening an overlay. Advanced topic reply, comment reply, and comment edit
  share one responsive `USlideover` composer that opens from the bottom on
  desktop and mobile and exposes a pointer/keyboard height handle. The former
  standalone advanced-reply route is compatibility-only and redirects into
  this drawer without changing create/update authorization, revision CAS,
  moderation, or cross-author audit-reason rules. Inside the drawer, the full
  editor uses a stable toolbar/canvas/status grid: extra drawer height expands
  only the canvas, while compact heights scroll the drawer body instead of
  clipping the editor status row.
- Topic detail exposes public **contributors** (author + body edit/restore
  actors, max 5 + count) and `GET /topics/{id}/contribution-timeline` for a
  header-only publish/edit timeline. Staff actors are fully exposed by default;
  full revision source remains `topic.revision.view_any` only.
- Topic detail and comment rows expose optional `editedAt` from the current
  accepted `post_revisions` entry when the corresponding edit-mark setting is
  enabled. The public detail UI shows relative publish/edit times through one
  month and forces older values to site-timezone `Y-m-d H:i:s`; resource
  `updatedAt` remains lifecycle/counter metadata and is not used as edit time.
- Comment avatars and author names now open a compact public-profile preview
  before navigation. The card reads the existing public profile contract,
  exposes only real topic/comment counts and profile data, and keeps the
  canonical `/u/:username` navigation inside the card.
- Soft-deleted comments now render a localized, bordered deletion notice in the
  shared public comment component instead of an empty content block.
- The flat comment stream is a chronological timeline: `view=flat` orders by
  `created_at ASC, id ASC` (index `comments_topic_created_idx`), so a new
  comment always appends at the bottom floor and existing floor numbers never
  move because of someone else's reply. Reply relationships are carried by each
  row's `replyTo` reference card (clickable, cross-page jumps reuse the
  comment-page resolve endpoint), and the client lands the viewport on the new
  comment after a successful publish (`useTopicCommentAnchor`). The keyset
  cursor, `CountCommentsBefore`, `ResolveCommentPage`, and profile-activity
  `commentPage` all share that single ordering key; `view=tree` keeps
  `path_key` hierarchy for nested consumers. See
  `../decisions/2026-10-03-comment-stream-chronological-order.md`.
- The flat stream loads new comments without a reload: `topics.comment_revision`
  is maintained by statement-level `comments` triggers (one bump per statement,
  only for publicly visible changes) that also `pg_notify` the topic id;
  `GET /topics/:topicID/comments/revision` reads that revision plus the tail
  comment without touching the comment-list cache; and
  `GET /topics/:topicID/comments/stream` pushes `event: revision` frames with a
  10s heartbeat, a 15s reconcile tick, a 60s connection lifetime, and per-process
  connection budgets (1024 total / 256 per topic / 8 per client IP, over-budget
  connections get 429 and fall back to polling). Payloads carry only revision
  facts; bodies, viewer-dependent tombstones, and permissions stay with
  `ListComments`. The web client shares one EventSource per browser per topic
  (BroadcastChannel + Web Locks), appends silently only when the reader is at
  the bottom of the last page, and otherwise shows a notice pill
  (`useTopicCommentLive`, `SFCommentStreamNotice`). The reader-position signal
  measures the comment stream's viewport rect (the desktop center column is its
  own scroll container) and the pill is sticky; the SSE resume cursor is kept
  separate from the revision the page has actually reconciled. See
  `../decisions/2026-10-03-topic-comment-live-stream.md`.
- Comment actions keep the full authorized set on desktop. At `640px` and
  below, reply and permalink stay inline while edit, delete, report, and
  extension actions move into an accessible menu beside the public floor
  number. This is presentation-only; existing API authorization and action
  dispatch remain authoritative.
- Default-theme topic creation now has a production UI shell that reuses the
  existing create-topic API, category/tag policy, content limits, permission
  check (`topic.create`), `SFEditor`, field errors, Toast feedback, successful
  redirect, and unsaved-content guard. The UI adds live publish summary and
  pre-publish checks only; it does not change API semantics.
- Content revisions V1 is **complete**: authenticated topic/comment edit, lazy
  history/detail, diff/preview, restore, super-admin redaction, stale-CAS,
  mobile diff, allowed/denied API checks, development backfill, query evidence,
  audit-retention proof, and operator documentation are closed. The unrelated
  concurrent `ExtensionManifest` gate failure is tracked outside this module.
- PostgreSQL site search is the protected default. Meilisearch is optional and
  must not be described as the required/default forum read path.

Active revision sources:

- Plan: `../plans/archive/2026-07/2026-07-22-forum-content-revisions-v1.md`
- M0 contract matrix:
  `../plans/archive/2026-07/2026-07-22-forum-content-revisions-v1-m0-contract-tests.md`
- Decision: `../decisions/2026-07-22-forum-content-revisions-ledger.md`
- Final handoff: `../sessions/archive/2026-07/2026-07-23-forum-content-revisions-v1-m7-handoff.md`

## Domain Model

- Category groups contain ordered categories. Current category visibility is
  `public` or `hidden`; role/category ACL is deferred.
- Topics own title, slug, category, author, state, counters, activity, tags,
  and a shared `content_id`.
- Comments form arbitrary-depth trees through parent/root/path metadata and own
  a shared `content_id`.
- `posts` stores raw content, sanitized HTML, plain text, format/editor/render
  versions, hash, and current accepted revision.
- `post_revisions` is an immutable accepted-version ledger. Derived HTML,
  excerpt, and plain text are recomputed rather than duplicated as historical
  authority.
- `topic_revision_snapshots` freezes topic metadata needed to reconstruct an
  accepted revision without turning the hot topic row into history storage.
- Topic states are `active`, `locked`, `hidden`, and `deleted`; tags are
  `active`, `pending`, or `disabled`.

## Content Revisions V1

Migration `202607220052` added `posts.current_revision`, numbered nullable
ledger metadata, concurrent `(post_id, revision_no)` indexes, and topic
revision snapshots. New topic/comment creation writes accepted revision 1 in
the same transaction.

Mixed legacy reads expose effective `currentRevision >= 1`; edited state is
effective revision `> 1`, so the initial accepted row does not mark all content
as edited. Backfill command:

```text
sforum revisions backfill --batch=N [--loop]
```

It claims `current_revision=0` batches with `FOR UPDATE SKIP LOCKED`, preserves
legacy row order, inserts the current snapshot, and is idempotent.

M2 added `topic.revision.view_any` and `post.revision.view_any`, permission
migration `202607220053`, moderator template/frontend labels, and read-only
history/admin content surfaces. Topic/comment revision list/detail are
service-gated by the matching history permission: lists return summary headers
only, while detail returns raw source only after authorization and renders a
safe historical preview on demand. Admin topic/comment content list/detail
require `admin.access` plus matching edit-any or history-view permission and do
not add admin mutation routes.

M3 makes PATCH versioned: `expectedRevision` is required in the public API,
the service rejects already-stale requests before synchronous filters, and the
PostgreSQL write transaction locks the resource/post and repeats CAS before any
write. Effective edits append exactly one accepted final snapshot and increment
`posts.current_revision`; semantic no-ops leave timestamps, ledger, audit,
cache, and search untouched. Cross-author edits require a trimmed reason of at
most 500 runes and append generic audit in the same transaction. The public
editors submit their loaded token. `comment.before_update` / `comment.updated`
now complement enriched safe `topic.updated` revision metadata.

V1 boundaries:

- M2 complete: view permissions, revision list/detail read models, admin
  content list/detail, tests, and OpenAPI.
- M3 complete: mandatory `expectedRevision`, two-stage CAS, final accepted edit
  snapshots, no-op detection, reason/audit rules, and comment update hooks/events.
- M4 complete: canonical topic/comment restore append new `restore` revisions,
  re-run filters/rendering/moderation/cache/search, validate current taxonomy and
  historical attachment ownership/availability, and redaction tombstones with
  transaction-bound audit. Legacy incomplete snapshots restore content only.
- M5 complete: admin management UI loads protected content through read models
  and sends canonical PATCH edits with the loaded revision token.
- M6 implementation complete: authorized staff get a summary-only timeline,
  lazy raw-detail reads, sanitized historical preview, source/metadata diff,
  restore confirmations, and super-admin-only irreversible redaction. The
  reviewed `diff` 9.0.0 BSD-3-Clause dependency is direct. Public history and
  force overwrite remain out of scope. The M6 authenticated browser and API
  release matrix is complete; preserve these boundaries for M7.
- M7 rollout evidence: the local development database backfill reached zero
  pending posts; revision-list explain uses its dedicated index; audit cleanup
  is regression-tested not to affect ledger rows; admin list indexes are staged
  in migration `202607231000`. Production keeps the compatibility read until
  every database has a recorded zero-pending backfill proof. See
  `../reports/2026-07-23-forum-content-revisions-v1-m7.md`.
- Use npm package `diff` 9.0.0 (BSD-3-Clause) for the diff UI; do not install
  npm `jsdiff`.
- Collaboration, CRDT, drafts, notifications, retention controls, and public
  revision browsing remain outside V1.
- Revision query/mutation, raw history content, reason text, IPs, and attachment
  provider data remain closed to plugins in V1.

## Content And Rendering Rules

- Backend accepts Markdown, HTML, and `editor-document` source formats. The
  structured format stores Host-accepted native Tiptap JSON in `raw_content`;
  the ambiguous legacy `json` format is rejected by both runtime and schema.
- Native `orderedList.start` values are normalized as integers before storage
  and rendering. Default `1` is omitted; zero and negative integers are
  preserved in both normalized JSON and sanitized `<ol start>` HTML, while
  fractional and string values fall back to the default. The sanitizer allows
  only a signed decimal integer on `ol[start]`.
- Markdown renders with `goldmark` plus GFM extensions; display HTML is
  sanitized with `bluemonday`. Since 2026-10-19 the Forum markdown path uses an
  explicit GFM extender whose table renderer emits `align` attributes
  (`extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)`),
  because `bluemonday` strips inline `style` — so GFM column alignment now
  survives sanitization on both the markdown and editor-document paths.
- `editor-document` accepts Host-owned table nodes `table` / `tableRow` /
  `tableHeader` / `tableCell`. Cell attributes are limited to
  `colspan`/`rowspan`/`align` (`colwidth` is deliberately not stored);
  `normalizeTableStructure` repairs hostile shapes instead of rejecting the
  post: non-row children are dropped, rows keep only cells, inline cell content
  is wrapped in a paragraph, empty cells get an empty paragraph, nested tables
  are flattened to text, spans clamp to `[1,20]`, `align` accepts only
  `left|center|right`, and tables cap at 200 rows / 50 effective columns.
  Repairs append `table` / `tableRow` to `Accepted.Fallbacks`. Rendered HTML is
  `<table><thead>/<tbody>` with no `style` or `colgroup`; `RenderMarkdown`
  projects GFM pipe tables (alignment separators, `\|` escaping, `<br>` for
  cell line breaks, `colspan` padded with empty cells). Protected shortcode
  blocks admit tables (`shortcodeBlockChildType`), and goldmark `Table` AST
  nodes convert to the same native shape on the markdown compatibility path.
  Column merges are out of scope: GFM cannot express them and comment drafts
  round-trip through Markdown.
- Sanitized `<pre><code>` content is progressively enhanced in the browser with
  a language label, line numbers, syntax highlighting, and copy feedback. A
  declared supported language selects its grammar; an unknown declared
  language keeps its honest label without guessed tokens, and an undeclared
  block is always localized plain text rather than auto-detected.
- `RenderVersion` is `goldmark-bluemonday-v2`; existing rows keep earlier HTML
  until a later edit or explicit migration.
- Content/excerpt limits are Unicode-rune based. Excerpts derive at read time
  from `plain_text` using `forum.reading.excerpt_rune_limit`.
- Topic/comment writes may provide `content.attachmentIds`. Explicit arrays
  replace references transactionally, omission preserves them on edit, and an
  empty array clears them. Native editor image nodes must name an attachment in
  that same array and use its matching `/media/attachments/{publicId}` or
  compatible historical API URL; forged or partial identity fails closed.
- Hidden/deleted/moderation-only content stays out of public SSR, sitemap, and
  search indexes.
- Planned custom image stickers are distinct from Unicode `sforumEmoji` and
  generic images. Accepted content will snapshot a stable sticker ID plus exact
  asset digest, while Core rendering will cap display at `128x128` CSS pixels
  on desktop/tablet and `96x96` on mobile. This contract is approved but not
  yet implemented.
- Shortcode V1 uses Host nodes `sforumShortcodeRef` (block atom) and
  `sforumShortcodeBlock` (nested block container), with exact `id`,
  `contractVersion`, and typed `arguments` attributes. Canonical text syntax is
  `sforum.shortcode-text@1`; `[friend-links]` is canonical,
  `[friend_links]` remains literal, and `topic-tag` is the sole import alias.
  A Markdown API submission containing an admitted standalone shortcode is
  normalized to canonical `editor-document` source; malformed, unknown,
  escaped, code/raw-HTML, placement-invalid, and over-budget input stays on the
  literal Markdown path. Go and TypeScript consume
  `contracts/fixtures/shortcode-text-v1.json` and, since M10A, the classified
  `contracts/fixtures/shortcode-legacy-conversion-v1.json` report
  (converted/literal/invalid/unsupported/over-limit plus structured
  rejections, `topic-tag` alias, and deferred `password`). Structured nodes fail closed on
  identity/version/attrs/arguments/placement/resource/count/depth/cycle input.
  Disabled refs render bounded labels (friend links omit output), while
  protected blocks emit one Host notice without traversing children into HTML,
  plain text, excerpts, search, or mention fanout. Canonical normalized native
  JSON owns the content hash and revision no-op identity.
- Public shortcode semantics are Host-owned but their concrete presentation is
  plugin-owned: after plugin HTML passes the execution sanitizer, the dispatcher
  wraps each reference with fixed `sf-shortcode`/type classes, and Forum
  protected composition wraps authorized or fallback blocks at the same final
  boundary. The active exact `sforum-shortcodes` artifact supplies the CSS for
  those wrappers through `core.surface.forum-content`; Core contains no
  shortcode visual rules. Plugin-provided class/style/data attributes remain
  disallowed. The wrapper is presentation-only and does not alter shortcode
  cache keys, authorization, or source projections.
- Composer reference previews may include the existing safe avatar view for a
  user and the stored icon plus validated six-digit hex color for a category.
  These fields are editor presentation metadata only; they do not expand the
  public shortcode projection or expose raw email/attachment authority.
- Public topic/comment bodies use `PublicRenderedContent` only: `id`, sanitized
  `htmlContent`, `plainText`, derived `excerpt`, and `renderVersion`. Public SQL,
  Redis JSON, HTTP responses, Page Registry models, and Nuxt payloads do not
  contain canonical source, editor metadata, attachment IDs, or source-derived
  hashes.
- Canonical editor reloads use uncached `GET /topics/:topicID/edit-source` and
  `GET /comments/:commentID/edit-source`. `EditableSourceService` applies the
  same active actor, edit-own/edit-any, author-window, resource-state, and
  owning-topic checks as mutation policy before loading source. Anonymous
  callers receive 401; unrelated, hidden, and deleted resources collapse to
  404. Existing authorized admin and revision projections retain full
  `RenderedContent` and remain independently permissioned.

- Fenced rich content (` ```mermaid `, ` ```chart `) and math delimiters (`$…$`,
  `$$…$$`) are **ordinary content**, not shortcodes: they stay code blocks / text in
  `raw_content` and in the sanitized HTML, and are rendered client-side by
  `apps/web/app/utils/content/`. No Host node, schema, storage, API, or shortcode
  identity changed for them. Their source therefore reaches `plainText` and excerpts
  exactly like any code block — an accepted tradeoff recorded in
  `decisions/2026-10-20-rich-content-fences.md`; if excerpt noise becomes a problem,
  the follow-up is a Host-side marker that excludes those blocks from plain text.

## Public URLs And Reads

| Resource | URL |
| --- | --- |
| Categories | `/categories`, `/c/:categorySlug` |
| Tags | `/tags`, `/tags/:tagSlug` |
| Topics | `/t/<path>` |

The public `/categories` index is a grouped directory, not a fabricated
dashboard. It keeps empty public groups visible, filters out hidden groups and
categories defensively, sorts categories only inside their owning group, and
derives totals/distribution/activity from the category DTO counters.

The `/tags` index is a public read surface when
`forum.tags.public_pages=enabled`. Its heat overview, directory filters, and
right-rail stats are derived only from active tag `topicCount`, `createdAt`,
name, slug, description, and status fields returned by the public tag API; there
are no fabricated likes, follows, trends, or weekly activity counters.

`seo.topic_url_mode` controls topic paths:

| Mode | Shape | Lookup |
| --- | --- | --- |
| `id_slug` | `/t/123/hello-world` | topic ID |
| `id` | `/t/123` | topic ID |
| `slug` | `/t/hello-world` | globally unique slug |

The catch-all topic route recognizes old shapes and redirects to the canonical
path. Only a 404 advances to the next lookup candidate; network/API failures
are not swallowed.

Comment pagination uses a path segment `/page/N` (N>1 appended, N=1 omitted) so
`forumTopicPath(topic, mode, page)` and `parseTopicPath` share one parser. Old
`?page=N` query links still resolve and are normalized to the path segment on
the client after hydration (never an SSR redirect, to preserve zero-flash
rendering of the resolved page).

Cross-page `#comment-{id}` anchors resolve with zero flash: when the URL has a
target anchor and no explicit page, `SFTopicShowPage` resolves the page
server-side via the comment-page endpoint, then SSR-renders that page so the
target comment is present in first-paint HTML and the browser scrolls natively.
Slug/mode mismatches still 301/replace, but page-segment normalization is
client-only.

The deep-link highlight (`:target` / `.sf-comment--flash`) must never clip its
comment. The sweep band animates an in-box `background-position` instead of a
translated pseudo-element, so the rule keeps `overflow: visible` and the
absolutely positioned author preview card inside that comment is not cut off at
the comment's bottom edge — `:target` stays on the URL for the whole visit, so a
`overflow: hidden` there is permanent, not a 3.2s flash.

Public reads expose only active/locked topics. Locked topics remain readable
but reject new comments. Viewer-aware deleted-comment tombstones never expose
body fields or deleted-parent reply excerpts.

Shortcode public projections will exclude protected descendants before
deriving HTML, `plain_text`, excerpt, search, SEO, events, or shared cache
values. Stored placeholders use the site default locale; a later default-locale
change requires re-render and search reindex. Personalized shortcode HTML is
composed after the actor-independent cache and is `private, no-store`.

## Pagination, Cache, And Scale

The completed task book is archived at
`../plans/archive/2026-07/2026-07-21-million-scale-read-path.md`; durable results
live in `../reports/` and
`../decisions/2026-07-21-read-replica-and-api-horizontal-scale.md`.

- Public topic and flat-comment lists accept shallow page pagination and opaque
  keyset `after`; cursor wins over page. Responses return `hasMore` and
  `nextCursor` where applicable.
- Topic keysets include pin state as the first ordering dimension.
- Public list ordering is server-owned: `GET /topics?sort=latest|active|hot`
  (created / last activity / `hot_score`), empty `sort` resolves to the site
  option `forum.list.default_sort` (recommended default `active`, aligned with
  `topics_public_activity_idx`), and the cursor is bound to the chosen sort.
  Public labels are 最近活跃 (`active`) / 最新发表 (`latest`) / 热门 (`hot`).
  Empty or invalid `sort` resolves to the site default; when that default is
  missing or invalid it falls back to `active`, never implicitly to `latest`.
  The admin default-sort select lists `active` first and marks it as recommended.
  The public feed must not re-sort loaded rows client-side — that would only
  cover the loaded page and break pin-first ordering plus cursor continuation.
  Homepage sort lives in the URL (`?sort=`) and in `forumHomeFeedKey`, so
  switching it resets pagination and the cursor. The shared segmented control
  (homepage feed, category directory, tag filters) is styled at the Core layer
  in `apps/web/app/assets/css/sforum-sort-control.css`; every selector covers
  both the Core fallback shell and `.sf-page.sf-theme--default`.
- Tree comments page root comments and cap descendants per root using
  `forum.comments.tree_descendants_per_root` (default 50). Truncated roots set
  `hasMoreChildren` and load more through the replies endpoint.
- `CachedStore` caches taxonomy, topic detail, topic lists, and eligible public
  comment lists through Redis-backed generation keys.
- Moderation approval has an explicit post-commit invalidation boundary:
  topic approval clears detail, global/category/tag list, and taxonomy
  derivatives; comment approval also clears that topic's comment-list
  generation. The Nuxt homepage itself is `no-store` so browser/CDN HTML cannot
  outlive the API generation authority.
- Topic-list invalidation uses global/category/tag scoped generations; writes
  bump only affected scopes plus global.
- Topic detail caches support ID and slug lookup with reverse-map invalidation.
  There is deliberately no composite topic-page cache.
- Nuxt `/t/**` disables whole-page caching because SSR embeds live comment and
  permission-aware payloads. Anonymous responses use `public, no-cache` for
  mandatory revalidation; session/edit responses use `private, no-store`.
  Topic/detail/comment scale remains owned by the API's topic-scoped Redis
  caches and generation invalidation.
- Horizontal default remains one PostgreSQL primary plus shared Redis. Read
  replicas are deferred to measured thresholds; no `DATABASE_READ_URL` runtime
  path exists yet.
- Comment page resolve (`GET /topics/:topicID/comments/:commentID/page`) returns
  the flat-view page holding a comment, for cross-page `#comment-{id}` anchor
  deep-linking. The service reuses `GetCommentSummary` and counts active
  comments ordered before `(path_key, id)` (strict alignment with
  `listCommentsFlat`). Only active comments resolve; soft-deleted, cross-topic,
  or hidden-topic targets return 404 without leaking status. The endpoint is
  not cached: it is a primary-key lookup plus an indexed COUNT, and caching
  would introduce drift when comments are deleted.

## Policy And Runtime Settings

Forum options cover default category, tag creation/public pages, tag limits,
pagination, content limits, cooldown/daily caps, edit windows, comment depth,
tree descendant cap, excerpt length, guest read mode, list sort/hot window,
author close/delete rules, edit marks, duplicate-title policy, soft-delete
visibility, and mention limits.

- Recommended defaults are configurable and resettable in the multi-tab forum
  settings UI.
- `forum.guest.read=login_required` makes public taxonomy/topic/search/comment
  endpoints return 401 to anonymous users.
- New-user trust options may tighten cooldowns/daily caps and outbound-link
  policy during the configured trust window.
- Tag modes are `controlled`, `review`, and `open`.
- Public topic/comment page sizes default to 20, accept 1-100, and remain
  server-authoritative when callers omit `perPage`.

## Notification Fanout

- Active top-level comments notify the topic author; nested comments notify
  only the direct parent author. Self and inactive recipients are skipped.
- Topic and comment creates parse mentions from the stored, filtered source via
  goldmark. Inline/fenced code is ignored; case variants and duplicates collapse
  per recipient while reply and mention remain distinct intents. The token
  grammar is `@` + letters/numbers/`_`/`-` up to 64 characters, so it matches
  what `identity.UsernamePolicy` allows. `editor-document` archives store native
  Tiptap JSON, so mention parsing runs on the Markdown restored by the
  EditorDocument pipeline (`MentionedUsernamesFromSource`) instead of the raw
  JSON; parse failures fall back to raw-source parsing.
- Public rendering of mentions is a client-side contract: the backend HTML keeps
  `@name` as plain text, and `v-mention` (`app/utils/forum/forumMentions.ts`)
  linkifies visible text into `a.sf-mention` outside links and code blocks.
  `SFMentionContent` hosts the decoration and click handling, and the page-level
  `SFUserMentionPreview` renders one shared `SFCommentUserPreview` card anchored
  to the clicked mention. Pages without that host keep ordinary link behavior.
- Pending topic/comment approval loads stored source and target context inside
  the decision transaction, then writes moderation plus eligible reply/mention
  projections exactly once. Rejection writes only the author's moderation result.
- Editor autocomplete for mentions (2026-10-10): `SFEditor` gains an
  `enableMentions` prop; forum hosts (topic composer/edit, comment drawer)
  forward `limits.mentionsEnabled` from the public `forum.mentions.enabled`
  option. The editor extension lives in `app/utils/editor/mentions.ts`: a
  Tiptap `Suggestion` (char `@`, `minQueryLength: 1`, debounce 150ms) opens a
  vanilla-DOM candidate menu fed by the existing authenticated
  `GET /composer/references?kind=user` endpoint (reuse, no new API), inserting
  plain text `@username `; a separate decoration plugin highlights tokens with
  `.sf-mention-inline` using `forumMentionTokens` so editor highlight matches
  backend parsing exactly (code blocks/spans excluded). No storage-format or
  backend changes: stayed with plain-text mentions on purpose.
- New pending topics/comments and edits requeued by moderation transactionally
  notify every active effective `moderation.review` holder except the
  submitter/editor. Each row links to the protected pre-publication workbench;
  read-time permission checks prevent a former reviewer from opening it.
- Notification/outbox failure rolls back the owning content or moderation
  transaction. Topic/comment edits do not emit new mention notices in V2.
- Notification target resolution reuses public Forum visibility: topic must be
  active/locked in a public category; comment must also be active.

## Authorization

- Topic create: login plus `topic.create`.
- Comment create: login plus `post.create`.
- Own/any topic edit: `topic.edit_own` / `topic.edit_any`.
- Own/any topic delete: `topic.delete_own` / `topic.delete_any`.
- Topic lock/unlock: `topic.lock`; pin/unpin: `topic.pin`.
- Own/any comment edit: `post.edit_own` / `post.edit_any`.
- Own/any comment delete: `post.delete_own` / `post.delete_any`.
- Category administration: `category.manage`; tag administration:
  `tag.manage`.
- Forum settings writes are restricted to the permission owning the changed
  setting family.
- API policy checks are authoritative. Frontend visibility is only UX.

## Taxonomy Administration

- Admin listing and public listing are one store method with an explicit
  visibility scope: `ListCategories(ctx, includeHidden)` /
  `ListCategoryGroups(ctx, includeHidden)`. Public read paths and page view
  models pass `false`; `GET /admin/forum/categories|category-groups` passes
  `true`, so hidden groups and hidden categories stay visible and maintainable
  instead of disappearing from the console. The cached store keeps the two
  scopes in separate keys (`forum:cats` / `forum:cats:all`) and invalidates
  both together.
- Deletion is explicit and guarded: `DELETE /admin/forum/categories/:categoryID`
  and `DELETE /admin/forum/category-groups/:groupID` require `category.manage`
  and map database RESTRICT failures to business errors — a category with
  topics returns `forum.category_not_empty`, a group with categories returns
  `forum.category_group_not_empty`. Deleting the current default posting
  category is refused with `forum.category_is_default` before any store write,
  because `createTopic` falls back to `forum.default_category_slug`.
- Delete maintenance lives in the focused `TaxonomyService` collaborator
  (`apps/api/app/Models/Forum/service_taxonomy.go`) exposed on the Forum
  controller, not as new receiver methods on the legacy `Service`. Its error
  codes and sentinels live in the same file; keep them out of `types.go`, which
  sits at its file-size baseline.
- Deleting a category or an empty group emits the observed
  `category.deleted` event.

## Extension Boundary

- Core owns canonical forum tables, policy, revision authority, core handlers,
  and default read models.
- Plugins use declared hooks/events, contributions, providers, queries,
  navigation/regions, and other versioned registries.
- Trusted route/guard/query replacements are possible only through V3 exact-
  artifact grants and own the declared authorization contract. Ordinary
  integrations must not request raw core database access or whole-route power.
- Search indexing, notification fanout, analytics, and provider integrations
  project from stable Core contracts.
- Regenerate the Forum Extension Surface Matrix when revision events/catalogs
  change.
- Planned plugin sticker packs use a generated exact catalog and Core import;
  no executable registration or remote media URL is required. Disable,
  upgrade, rollback, uninstall, and Safe Mode must leave accepted historical
  sticker content renderable.
- The protected `sforum-shortcodes` plugin will own declarations, localization,
  editor commands, and typed rendering only. Core owns node admission, public/
  editable DTOs, actor/resource decisions, exact execution, search/cache/event
  projection, and fallback. Sync `topic/comment.before_create/update` filters
  remain trusted authoring processors; observe events/webhooks remain body-
  free. See
  `../reports/2026-08-22-builtin-shortcodes-m0-threat-inventory.md`.

M8 protected reads load actor-independent store/cache data before an optional
Host authorizer composes a request-only accepted child fragment. M9 supplies
the Host policy projection and exact protected handlers: `login` is active
actor-only, `reply` is topic-author or same-topic public accepted-comment
eligible, and `only-author` is comment-only for comment/topic authors. Protected
composition is internal `json:"-"` metadata and direct Forum responses use
`private, no-store`; shared Redis/process/CDN/Nuxt/browser caches never receive
viewer-sensitive output. The production reference dispatcher skips protected
declarations until M9. Default-locale fallback copy is Host-owned, and
moderation approval mention replay uses the public side-effect projection so
protected descendants have no notification side effect. The write path still
applies source size, attachment identity, sanitizer, dangerous-link, new-user
outbound-link, and moderation/publication policy to the full accepted source.
Only public mention text fans out; observe/webhook payloads remain body-free,
and search enqueue carries only the topic ID. Real Redis tests prove that a
shared base reused by denied and allowed viewers contains neither child source,
viewer identity, nor authorized output.

## Important Paths

| Path | Responsibility |
| --- | --- |
| `apps/api/app/Models/Forum` | Domain services, stores, policy, cache decorator |
| `apps/api/app/Http/Controllers/Forum` | Public/admin HTTP handlers |
| `apps/api/database/queries/forum.sql` | sqlc forum queries |
| `contracts/openapi/paths/forum.yaml` | Forum HTTP contract |
| `contracts/openapi/schemas/forum.yaml` | Forum schemas |
| `apps/web/app/components/forum` | Reusable forum UI |
| `apps/web/app/pages/t/[...path].vue` | Topic route host |

## Post/Comment Link Policy (2026-10-16)

- Every link rendered into a topic body or comment is forced to
  `target="_blank"` with canonical `rel="nofollow noreferrer noopener"`, and
  absolute `http(s)` links are stored as `/outbound?u=<base64url>`. The rewrite
  happens in the sanitize seam (`Support/ContentLinks`), so it also covers the
  Markdown/HTML compatibility path and plugin render segments.
- The interstitial decision (enabled / allowlisted / same-site) is made at
  click time from `forum.links.external_interstitial` and
  `forum.links.trusted_domains`; stored content never depends on the current
  configuration.
- `ForumSettings.ExternalInterstitial` / `TrustedDomains` are part of the forum
  settings contract (admin DTO, route guard, OpenAPI, forum reading tab).

## Reactions (Likes) — 2026-10-28

- 数据面：`content_reactions`（唯一约束 `(user_id, target_type, target_id, reaction)`，
  `target_type ∈ {topic, comment}`，`reaction='like'`）；计数列
  `topics.like_count` / `comments.like_count`；稳定视图
  `sforum_core_v1.forum_reactions`（`security_barrier` / `security_invoker`，无 PII）
  + HostQuery `sforum.core.public_reactions.list`（结果 schema
  `sforum.core.public_reaction`）。
- 写路径：`ReactionService` → `PostgresStore.ApplyReaction`
  （`ON CONFLICT ... DO NOTHING`），同事务写计数、热门分、通知投影；事件
  `reaction.created` / `reaction.removed` 提交后发出，fail-open。
- 读路径：计数来自列；`likedByMe` 在 `PublicReadService` 缓存之后按请求装饰
  （递归子评论，最多两次批量查询）。
- 评论点赞位置：控件在评论 footer 动作行 `.sf-comment__actions` 首位（不在头部
  `.sf-comment__floor-actions`）；动作行 `v-if` 为
  `actions.length || (comment && reactionsEnabled)`；`.72` 透明度只下沉给文字
  按钮，点赞计数保持不透明。
- 残留（详见 `../decisions/2026-10-28-content-reactions.md` 的 Known tradeoffs）：
  硬删用户后 `like_count` 偏高（CASCADE 只删关系行）；`roster.Total` 只算 active
  用户，可能小于计数；可见性校验与写入不同事务（TOCTOU，最多一条孤立关系行）；
  未验证邮箱也可点赞并触发通知；名单读无独立限速；`forum_reactions` 无
  `topic_id`，评论点赞不能按主题聚合。
- 产品缺口：默认主题只有 flat 评论视图（`SFTopicShowPage.vue:148` 硬编码
  `ref<'flat'>('flat')`，`SFCommentStreamControls` 全仓无引用）；tree 由服务端
  `ListComments(tree)` 覆盖，本切片不加切换 UI。
- QA：功能与视觉证据见 `storage/qa/reactions-qa-report.md` 与
  `storage/qa/2026-10-29-visual-responsive-audit.md`；四项视觉待修与三项 token
  债务见 handoff。
- 计数对账（运营者手动执行，非定时任务）：

```sql
-- 先按关系表重算计数，再按统一公式重算 hot_score。
UPDATE topics t
SET like_count = counts.n,
    hot_score = t.comment_count * 5 + t.view_count + counts.n * 3
FROM (
  SELECT t2.id AS topic_id, COUNT(r.id) AS n
  FROM topics t2
  LEFT JOIN content_reactions r
    ON r.target_type = 'topic' AND r.target_id = t2.id AND r.reaction = 'like'
  GROUP BY t2.id
) counts
WHERE counts.topic_id = t.id AND t.like_count <> counts.n;

UPDATE comments c
SET like_count = counts.n
FROM (
  SELECT c2.id AS comment_id, COUNT(r.id) AS n
  FROM comments c2
  LEFT JOIN content_reactions r
    ON r.target_type = 'comment' AND r.target_id = c2.id AND r.reaction = 'like'
  GROUP BY c2.id
) counts
WHERE counts.comment_id = c.id AND c.like_count <> counts.n;
```

### 点赞者头像组预览（likedBy，2026-11-09）

- 形状：`TopicDetail.likedBy` 与 `Comment.likedBy`（含嵌套子评论）为
  `[]UserSummary`，**最多 3 个**，`ReactionPreviewLimit = 3`；无点赞的目标不出现在
  store 返回的 map 里，字段 `omitempty`，所以匿名响应不会出现空数组。
- 排序（预览）：**当前访客自己优先，其余按 `created_at` / `id` 最早在前**。理由：
  头像组是「我参与过这条内容」的即时反馈，自己不在最前面就看不出来；剩余位置用
  最早者表达「最早支持这条内容的人」。匿名（`$3 = 0`）时 `(r.user_id = $3)` 对
  所有行恒为假，自然退化为纯最早优先。
- 排序（名单 `GET /topics/{id}/likes`、`GET /comments/{id}/likes`）：**最近优先**
  （`created_at DESC, id DESC`）。理由：20 条一页的「谁赞了」是活跃度视图，最新
  参与者更相关；而 `OFFSET` 分页在「最近优先」下新赞会插到第 0 页，客户端按
  `id` 去重兜住重复（`forumReactionRosterMerge`）。
- 缓存边界：预览与 `likedByMe` 都**在缓存之后**装饰。`CachedStore.GetTopic` /
  `ListComments` 只用 actor 无关的值为键落缓存（JSON 序列化发生在装饰之前），
  返回的是值拷贝，装饰就地改副本不会回写。`CachedStore.ReactionPreviews` 是纯
  转发（`ReactionPreviewStore` 窄接口），**不缓存**；登录访客的响应带
  `Cache-Control: private, no-store` + `Vary: Cookie`，匿名响应保持可公共缓存
  （匿名预览与访客无关，由 `$3 = 0` 决定）。
- fail-open：`ReactionService.decoratePreviews` 查询失败只 `slog.WarnContext` 并
  返回，公开读仍是 200；`PublicReadService.decorateTopic/decorateComments` 忽略
  装饰错误。代价是「预览查询失败」时头像组为空、只剩 `+N`，而不是 5xx。
- SQL：`PostgresStore.ReactionPreviews` 一条语句批次取主题 + 评论
  （`ROW_NUMBER() OVER (PARTITION BY target_id ORDER BY (user_id = $3) DESC,
  created_at, id) <= $5`），JOIN `users` 时过滤 `status = 'active'`，因此非活跃
  用户既不占位也不被展示。复用既有 `content_reactions_roster_idx`
  `(target_type, target_id, reaction, created_at DESC, id DESC)`，**没有新迁移**；
  由于排序首键是计算表达式，每个 target 仍需一次小排序（当前每目标 ≤3 行，可接受）。
- 前端：`SFReactionStack`（纯展示，首屏零请求，`total = 0` 不渲染，`count > 0`
  且 `likedBy` 为空时只画 `+N`）、`SFReactionRoster`（弹层内容，20/页，按 id
  去重，`hasMore = false` 或 `offset >= 1000` 收起「加载更多」，错误内联 + 重试）、
  `SFReactionControl`（心形 + 头像组触发器 + 弹层，乐观切换后以服务端真值回填）。
  头像全部装饰化：`SFAvatarGroup` 传 `decorative`，组 `aria-hidden="true"`、
  头像 `alt=""`；可读名只出现在触发器 `aria-label`（`forum.reaction.rosterAria`，
  含人数）与 `aria-haspopup="dialog"` 上。尺寸阶梯（单调）：
  `2xs 18 < xs 24 < sm 32 < list 36 < md 40`。
- 主题动作行（实测）：`xs`（24px）+ `max=3` + 重叠 `-6px`，3 头像 + chip 时组宽
  **60px**、chip **24×24**、控件总宽约 **132px**；触发器 `min-height: 40px`
  —— 24px 头像后内容只有 28px，`box-sizing: border-box` 下取 38 会把动作行从
  **59px 压到 57px**，故取 40；动作行高 59px，头像与心形中心对齐。
  评论行动作行仍用 `2xs`（18px）+ `max=2` + `chip=false`（计数就在心形旁，chip 是
  重复信息）；弹层名单行内头像同步 `xs`（24px），行高仍 32px；
  `≤479px` 时头像组降到 2 个并隐藏 chip。
- 「加载更多」的加载态：可见性只由 `hasMore && offset < ROSTER_OFFSET_CAP(1000)`
  决定，**加载中按钮保持挂载**，`loading` 只负责 `disabled` / `aria-busy` / spinner
  （否则焦点掉 `body`、弹层高度跳 32px）。`total` 走
  `forumReactionRosterTotal`：首页取服务端真值，追加页只增不减 —— 服务端在
  `offset` 越界时回 `total=0`，不能把已知人数抹掉。
- 焦点：加载中**不用原生 `disabled`**（浏览器会在两帧后异步移除焦点），改用
  `aria-disabled="true"` + `loadMore()` 内 `forumReactionRosterCanLoadMore` guard
  （连按 Enter 只发一次请求），样式挂在 `.sf-roster__more[aria-disabled='true']`。
  实测键盘 Enter 触发后焦点留在按钮上，`aria-busy="true"`、spinner 可见、弹层
  高度不跳；末页按钮移除后焦点自然回落到 `body`（该步无可避免）。

## Bookmarks (收藏) — 2026-11-10

- 数据面：`content_bookmarks`（唯一约束 `(user_id, target_type, target_id)`，
  `target_type ∈ {topic, comment}`，`visibility ∈ {private, public}` 默认 `private`）；
  计数列 `topics.public_bookmark_count` / `comments.public_bookmark_count`
  （只统计 `visibility='public'`，服务层同事务维护）；提醒列 `remind_at` /
  `reminded_at` 属于行主私有计划数据；稳定视图 `sforum_core_v1.forum_bookmarks`
  只输出公开行，不输出提醒列。迁移
  `apps/api/database/migrations/202611100001_content_bookmarks.sql`
  （`+sforum OnlineSafe`，带 `lock_timeout`/`statement_timeout`）。
- 周期任务：`forum.bookmark_reminders`（`Interval: time.Minute`，队列 maintenance），
  注册点在 `apps/api/bootstrap/worker.go` 的 `coreScheduleConstructors`
  （经 `registerForumBookmarkRemindersWorker` 一次装好 worker + schedule）；
  目标不可见的行只写 `reminded_at`、不发通知，日志体现为
  `claimed` 与 `notified` 的差额。
- 产品开关：`features.bookmarks`（`web_options`，默认 `enabled`）。关闭后 6 条路由
  （PUT/DELETE topics|comments bookmark、`GET /profile/bookmarks`、
  `GET /profiles/{username}/bookmarks`）全部 403。注意 Options 有 30s 进程内缓存，
  切库后要等一个 TTL 才生效。
- QA 证据：`storage/qa/bookmarks-flag-evidence.txt`（开关 200/403 实测）、
  `storage/qa/bookmarks-reminder-evidence.txt`（提醒链路）、
  `storage/qa/bookmarks-backup/`（回滚前快照）。
- 计数对账（运营者手动执行，非定时任务）：

```sql
-- 公开收藏计数按关系表重算。只算 visibility='public' 的行，
-- 与读路径 `topics.public_bookmark_count` / `comments.public_bookmark_count` 同口径。
UPDATE topics t
SET public_bookmark_count = counts.n
FROM (
  SELECT t2.id AS topic_id, COUNT(b.id) AS n
  FROM topics t2
  LEFT JOIN content_bookmarks b
    ON b.target_type = 'topic' AND b.target_id = t2.id AND b.visibility = 'public'
  GROUP BY t2.id
) counts
WHERE counts.topic_id = t.id AND t.public_bookmark_count <> counts.n;

UPDATE comments c
SET public_bookmark_count = counts.n
FROM (
  SELECT c2.id AS comment_id, COUNT(b.id) AS n
  FROM comments c2
  LEFT JOIN content_bookmarks b
    ON b.target_type = 'comment' AND b.target_id = c2.id AND b.visibility = 'public'
  GROUP BY c2.id
) counts
WHERE counts.comment_id = c.id AND c.public_bookmark_count <> counts.n;
```

- 只查差异（不写库，先看再决定）：

```sql
SELECT 'topic' AS target, t.id, t.public_bookmark_count AS stored, COUNT(b.id) AS actual
FROM topics t
LEFT JOIN content_bookmarks b
  ON b.target_type = 'topic' AND b.target_id = t.id AND b.visibility = 'public'
GROUP BY t.id, t.public_bookmark_count
HAVING t.public_bookmark_count <> COUNT(b.id)
UNION ALL
SELECT 'comment', c.id, c.public_bookmark_count, COUNT(b.id)
FROM comments c
LEFT JOIN content_bookmarks b
  ON b.target_type = 'comment' AND b.target_id = c.id AND b.visibility = 'public'
GROUP BY c.id, c.public_bookmark_count
HAVING c.public_bookmark_count <> COUNT(b.id);
```

- 路由（6 条）：`PUT|DELETE /topics/{id}/bookmark`、`PUT|DELETE /comments/{id}/bookmark`
  在 Forum 控制器；`GET /profile/bookmarks`、`GET /profiles/{username}/bookmarks`
  在 Profile 控制器。`PUT` 是全量状态写入：`visibility`、`remindAt`、`clearReminder`
  三态（同时给 `remindAt` 与 `clearReminder` → 422 conflict）；`DELETE` 不读 body。
- 提醒列 owner-only，三层闸：列表 SQL 在 `includePrivate=false` 时用
  `CASE WHEN $4` 把两列置 NULL；store 组装 entry 时再清一次；`ForumBookmarkLister`
  适配器在 `viewerID != ownerUserID` 时第三次清空。删掉层 3 会让测试变红，层 1/2
  互相遮蔽（行为被锁、层分不出来），终审已记录为已知性质。
- 提醒时间窗口：晚于 `now - 60s`、早于 `now + 730d`；单用户未到期上限 200（软上限，
  同事务计数但不串行化，并发可略超，注释里写明这是取舍）。
- 计数只在 store 单事务内维护（行写入 + 计数 delta 同一 `pgx.Tx`，已存在行走
  `SELECT ... FOR UPDATE` 后取 delta），service 只表达意图。
- `hot_score` 不吸收收藏：公式仍是评论 + 浏览 + 主题点赞。
- 决策：`../decisions/2026-11-10-content-bookmarks.md`；会话：
  `../sessions/2026-11-10-content-bookmarks.md`。

## Badges And Activity Counters — 2026-11-10

徽章与用户活动统计是一个**独立数据域**（`app/Models/Badges/`，模块笔记
`badges.md`），Forum 只消费它：

- **读模型**：`TopicSummary` / `TopicDetail` / `Comment` 各加 `topBadge`
  （`*BadgeRef`，四字段：`slug` / `name` / `tier` / `icon`）。数据源是
  `users.top_badge_id` 的单列 JOIN，一次查询覆盖整页，无 N+1、无 `DISTINCT ON`。
- **装饰位置**：`BadgeReader` 由 Forum 定义接口、Badges 实现、bootstrap 注入。
  徽章 **actor 无关**，所以装饰结果**可以进 `CachedStore`**——这一点与
  `likedByMe` / `bookmarkedByMe` 相反（那两个必须在缓存之后逐请求装饰、永不进缓存）。
- **写路径 hook**：创建主题/评论、点赞/取消、收藏/取消都在**既有事务内**同步累加
  `user_activity` 的对应列，事务提交后入队 `badges.evaluate_user`。统计不异步，
  因为异步漂移对 `topic_count` / `comment_count` 没有真值可对账。
- **NULL 路径**：`BadgeRef` 四个字段全部可能为 NULL（LEFT JOIN 未匹配），扫描必须用
  `sql.NullString` 接再转，且必须有「用户没有徽章」的用例——本地数据都有徽章时这条
  路径测不出来。
- **列清单只有一份**：`topicSummaryColumns` 同时供 `topic_summary_sql.go`、
  `list_topics.go` hydrate、`search_live.go` 使用。本刀在这里踩过一次 P0（抄了三份
  列清单、两处漏列导致首页全站 500），细节见计划 §12 T1–T5。

## Topic Confirm Dialog（公共话题页确认弹窗）— 2026-11-14

公共话题详情页 `apps/web/app/components/forum/SFTopicShowPage.vue` 的 **4 处浏览器原生
`window.confirm` 全部换成站内 Nuxt UI Modal**（评论删除、话题删除、话题扩展动作、评论扩展动作）。
纯前端改动：后端、OpenAPI、管理员页与编辑器/导航编辑器的原生 confirm 一律未动。

- **组件** `apps/web/app/components/forum/SFConfirmDialog.vue`：通用确认弹窗（`UModal`，
  title/description 走 props，按钮放 `#footer`）。props `request: ConfirmDialogRequest`，
  emits `settle: [boolean]`。底部顺序固定「取消（`neutral`/`ghost`）在前、确认
  （`error`/`solid`）在后」；组件**不给任何元素设置自动聚焦**，破坏性按钮因此不会成为
  默认焦点。ESC / 点遮罩 / 右上角关闭统一 emit `settle(false)`（按取消结算）。
- **组合式** `apps/web/app/composables/forum/useConfirmDialog.ts`：Promise 语义的确认状态机。
  `confirm(options): Promise<boolean>`、`resolveConfirm(value)`、`request`（reactive 的
  `{ open, title, description, confirmLabel, cancelLabel }`）。同一时刻只允许一个待结算请求：
  连续两次 `confirm` 时**前一个按 false 结算**，不让旧 Promise 悬空；结算后 `request.open` 归 false。
  纯逻辑、不依赖 i18n，文案由调用方给。
- **页面接线**：两个新文件在页面里**显式 import**（嵌套目录组件不能靠自动导入改名），
  模板只挂一次 `<SFConfirmDialog :request="confirmRequest" @settle="resolveConfirm" />`，
  4 处 `await confirm({...})`。删除流程其余逻辑（`deletingCommentId` 守卫、软删、刷新列表、
  Toast、失败报错）原样保留。
- **i18n**：新增 `topicDetail.confirmDialog.commentDeleteTitle` / `topicDeleteTitle` /
  `extensionActionTitle` 三键，zh-CN 与 en-US 成对；正文与按钮复用既有键
  （`topicDetail.confirmCommentDelete` / `confirmDelete` / `confirmExtensionAction` /
  `cancel` / `delete`），未新增无消费者的死键。
- **架构基线（双向棘轮）**：为让出净空，`mergeCommentReplies` 整段挪到
  `apps/web/app/utils/forum/forumCommentTree.ts`（新），三个扩展动作 helper
  （`topicExtensionActionKey` / `commentExtensionActionKey` / `topicExtensionActionLabel`）挪到
  `apps/web/app/utils/forum/forumTopicPresentation.ts`。`SFTopicShowPage.vue` 由 **1127 行降到
  1124 行**，`tests/architecture-boundaries-baseline.json` 的 `legacyLargeFiles` 同项同步下调为
  **1124**（与实际行数严格相等）。helper 的标签签名改为显式传 locale，工具层不依赖运行时。
- **验收**：`cd apps/web && bun test` **3070 pass / 0 fail**（全量，改了公共页 island 不能跑切片）、
  `bun run typecheck` exit 0、`node tests/validate-architecture-boundaries.mjs` passed、
  `bun run build` exit 0；测试文件 `apps/web/tests/forum/topicConfirmDialog.test.ts`（13 用例）。
  浏览器实测（127.0.0.1:3000 话题页）：站内 Modal 出现、浏览器原生对话框未触发、
  取消/ESC/X/点遮罩四条关闭路径均生效且数据未变、初始焦点在关闭按钮（破坏性按钮不是默认焦点）。
- 会话：`../sessions/2026-11-14-topic-confirm-modal.md`。

## Next Steps

1. Keep comment composer behavior consolidated in
   `useTopicCommentComposerDrawer`; do not reintroduce page-local edit/reply
   state or a standalone advanced-reply editor.
2. Built-in Shortcodes M10B parser conformance is reconciled and focused
   security evidence is green, but the plan remains active/blocked until the
   staged exact artifact is promoted through normal super_admin activation and
   the full lifecycle/browser/secret-marker gate is rerun.
3. Complete the new editor product design before starting the custom image
   sticker platform implementation.
4. Preserve the accepted revision ledger, CAS, authorization, and redaction
   boundaries when sticker references enter topic/comment content.
5. Keep OpenAPI, allowed/denied policy tests, module status, and Extension
   Surface Matrix synchronized at each milestone.
6. Treat role-scoped category ACL and reactions/bookmarks as separate future
   tracks rather than sticker scope.
