# Decision: Content reactions (likes) — one table, denormalized counts, single hot-score formula

Date: 2026-10-28
Status: accepted (first slice implemented 2026-10-29; extension data surface landed)
Related: `plans/2026-07-12-iteration-a-engagement-loop.md`,
`decisions/2026-10-12-remove-gamification-badges-growth.md`

## Context

SForum had no like/bookmark/follow/DM surface. Discourse ships likes,
bookmarks, badges, and trust levels; the operator asked which to build first.
The ordering argument was dependency, not preference:

- Likes are the only one of the six that needs **no new public page**. A Page
  Registry page costs catalog + route shell + ViewModel + Host island + theme
  template + completeness tests + theme rebuild/activation. Likes only touch the
  existing topic page and comment row.
- The plumbing already exists in the same shape: Redis view dedup + flush job +
  `topics.hot_score`, Notification V2 projections, the observe-event catalog,
  `/me/*` conventions, and `features.*` flags.
- Likes are the write-model template for bookmarks/follows/reads: one
  `(user, target)` row, a counter, a reversible toggle.
- Badges and trust levels were explicitly removed on 2026-10-12; rebuilding them
  is a product decision first, and trust levels would collide with RBAC.

## Decision

1. **One polymorphic table** `content_reactions` keyed by
   `(user_id, target_type, target_id, reaction)` with `reaction` fixed to
   `like` in v1. `target_id` deliberately has no FK (same posture as
   `entity_meta`): content deletion is soft, and the service owns integrity.
   `user_id` cascades so a hard user delete cannot leave orphans.
2. **Counts are denormalized** into `topics.like_count` and
   `comments.like_count`, maintained inside the same transaction as the
   relation row. Drift therefore requires a bug, not a race.
3. **`hot_score` gains likes at weight 3**, and its formula becomes a single
   SQL expression (`TopicHotScoreSQL`). Before this change six write sites each
   spelled `comment_count*5 + view_count`; adding a signal to five of them would
   have let the next moderation approval or comment delete silently erase the
   like contribution. The Go mirror (`ComputeHotScore`) takes the same three
   inputs.
4. **`likedByMe` is request-scoped and never cached.** `CachedStore` caches
   actor-independent details/lists; decoration happens in `PublicReadService`
   after the cache read, in at most two batched queries (one topic id, one page
   of comment ids including nested children).
5. **One notification type** `reaction` (category `engagement`): in-app on,
   email and web push off by default. Self-likes never notify. The dedupe key
   includes the actor, so unlike-then-relike does not produce a second notice.
6. **`features.reactions`** is a public product flag (default on). Disabled
   means: write endpoints 403 `forum.reactions_disabled`, roster endpoints 403,
   and the UI hides the controls. Historical counts stay readable.
7. **No per-target rate limiter in v1.** Write endpoints already pass through the
   global write limiter (default `LIMITER_WRITE_MAX=30` per minute per IP) and
   CSRF; the unique constraint caps single-target inflation. Roster reads are
   unthrottled. Recorded as a residual, not a missing control.

## Consequences

- The like write path is one transaction: relation row → counter → hot score →
  notification projection. Events (`reaction.created` / `reaction.removed`) are
  emitted after commit and are fail-open; they are the future badge plugin's
  only data source, so no write path needs to change later.
- `sforum_core_v1.forum_reactions` is delivered in the same migration and
  mirrors the `forum_topics` / `forum_comments` visibility predicates verbatim,
  with `security_barrier = true` / `security_invoker = false` and no PII. The
  stable HostQuery projection (`sforum.core.public_reactions.list`, result
  schema `sforum.core.public_reaction`) landed in the same slice, with
  allowed/denied tests.
- Comment likes do not feed `hot_score`; only topic likes do.
- A hard user delete cascades reaction rows without decrementing counters.
  `like_count` can then be high. The stable view exposes the truth
  (`COUNT(*)` per target), and the operator-run repair SQL is recorded in
  `modules/forum.md`. No automatic reconciliation job in v1.
- Removing likes later requires an explicit data decision; the columns and
  table are additive and can be dropped with the migration's Down section.

## Known tradeoffs and residuals (2026-10-29)

1. **Login is the only gate.** Unverified-email accounts can like and trigger
   notifications, unlike topic creation which requires a verified email. Kept
   deliberately for this slice; revisit if abuse appears.
2. **Dedupe key includes the actor** (`reaction:{type}:{id}:{actor}`), so
   unlike-then-relike never produces a second notification. This is the intended
   product rule, not an accident.
3. **`forum_reactions` has no `topic_id`**, so comment likes cannot be
   aggregated per topic through the stable view. Adding it later is a view
   change, not a data change.
4. **Roster reads are unthrottled** and use `COUNT(*) OVER ()`; `offset` is
   capped at 1000, `limit` at 50. Write endpoints rely on the global limiter.
5. **TOCTOU:** visibility is checked before the write transaction, so a target
   hidden between check and commit can still receive one reaction row. The read
   side filters it; no repair needed.
6. **Hard user delete leaves `like_count` high** (CASCADE removes relation rows
   only). Repair SQL lives in `modules/forum.md`.
7. **`roster.Total` counts active users only**, so it can be smaller than
   `like_count` when likers are banned or deleted. Displayed total may differ
   from the counter by design.
8. **Open question (do not change in this slice):** `sforum_core_v1.forum_topics`
   does not check `category_groups.visibility`, while `public_categories` does.
   Recorded for a separate decision.
9. **Default theme only exposes the flat comment view.**
   `SFTopicShowPage.vue:148` hard-codes `ref<'flat'>('flat')` and
   `SFCommentStreamControls` has no call site. Tree rendering remains covered
   server-side (`ListComments(tree)`); adding a switch UI is a separate product
   decision, not part of this slice.

## Alternatives rejected

- **Generic `reactions` with multiple emoji in v1** — no product need yet; the
  column exists, the picker does not.
- **Counts computed on read** — every list/detail query would pay a join or
  subquery; the denormalized column matches how `comment_count`/`view_count`
  already work.
- **Generated column for `hot_score`** — it would forbid the existing
  `UPDATE ... SET hot_score` statements; a shared SQL expression keeps one
  source of truth without rewriting six write paths into a new shape.
- **Caching `likedByMe`** — cross-viewer contamination; the field must stay
  per-request.
