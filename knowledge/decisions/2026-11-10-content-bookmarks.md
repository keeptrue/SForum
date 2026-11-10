# Decision: Content bookmarks — private by default, public-only counts, owner-only reminders

Date: 2026-11-10
Status: accepted (implemented in the same change)
Related: `plans/2026-07-12-iteration-a-engagement-loop.md` (Workstream 3),
`decisions/2026-10-28-content-reactions.md`,
`decisions/2026-10-12-remove-gamification-badges-growth.md`

## Context

After likes shipped on 2026-10-28, the operator asked which Discourse-style gap
to close next: bookmarks, follows, badges, trust levels, or private messages.
The answer was **bookmarks**, on dependency grounds rather than preference:

- It is the only one whose design was already written — Workstream 3 of the
  Iteration A engagement plan had the table, routes, response fields, UI
  entry points, and acceptance criteria.
- Its write model is isomorphic to likes: one `(user, target)` row, a
  reversible toggle, a denormalized counter. Every mechanism likes established
  transfers directly (stable view, HostQuery, request-scoped `...ByMe`
  decoration, observe events, `features.*` flag, global write limiter).
- It needs no notification fanout. Nothing is sent to the bookmark owner's
  target author, so Notification V2 stays untouched.
- Badges and trust levels were deliberately removed on 2026-10-12 and would
  require reversing that decision; trust levels in particular would introduce a
  second permission authority beside RBAC. Private messages are the largest
  subsystem and Discourse gates them by trust level, which SForum does not
  have.

The operator also chose two things that go **beyond** Discourse: a per-bookmark
public/private visibility switch, and a public "bookmarks" tab on the member
profile. Discourse bookmarks are strictly private with no public wall and no
public count. Those choices therefore needed their own rules instead of an
imitation.

## Decision

1. **One polymorphic table** `content_bookmarks` keyed by
   `(user_id, target_type, target_id)` with `target_type ∈ {topic, comment}`.
   No FK on `target_id` (same posture as `content_reactions`): content deletion
   is soft and the service owns integrity. `user_id` cascades.
2. **Visibility is per bookmark**, `private | public`, defaulting to `private`.
   Public is an explicit choice, never a side effect of pressing the button.
3. **Only public bookmarks are counted.** `topics.public_bookmark_count` and
   `comments.public_bookmark_count` are maintained inside the same transaction
   as the row write and are adjusted on every visibility transition in both
   directions. Private bookmarks never enter a counter, so a counter can never
   leak "someone quietly saved this".
4. **Reminders are part of the bookmark, not a separate feature.** The same
   `PUT` carries `visibility`, `remindAt`, and `clearReminder` as a full state
   replace. Three-state semantics are explicit:
   - `clearReminder: true` → `remind_at = NULL, reminded_at = NULL`;
   - `remindAt` present → set, and reset `reminded_at`;
   - neither → leave both columns untouched.
   Sending both `clearReminder` and `remindAt` is a 422 rather than a guessed
   precedence. The tri-state exists because `remindAt: null` cannot express
   "clear" through a Go `*time.Time` — the earlier shape silently kept the old
   value while the UI reported success.
5. **Reminder window and cap.** A reminder must be later than `now - 60s`
   (clock skew) and earlier than `now + 730 days`; 200 pending reminders per
   user. The cap is a **soft** limit: counting happens inside the write
   transaction but is not serialized, so concurrent requests can overshoot by a
   few rows. Introducing a per-user advisory lock would make the high-frequency
   bookmark path wait on itself, and the reminder job is a batch scan that
   simply processes a few extra rows next round. This is recorded as a tradeoff,
   not as strong consistency.
6. **Reminder delivery is one notification type** `bookmark_reminder`
   (`engagement`), in-app **and email on by default**, web push off. A reminder
   the user explicitly scheduled is worthless if it lands nowhere; this is the
   one place where email defaults on while reaction defaults off.
7. **Reminder columns are owner-only, enforced in three layers.** The list query
   wraps both columns in `CASE WHEN $4` so private scheduling data never enters
   the DTO, the store clears them again when `includePrivate=false`, and the
   `ForumBookmarkLister` adapter clears them a third time when
   `viewerID != ownerUserID`. The first layer exists so the leak cannot happen;
   the other two exist so a future query edit cannot reintroduce it silently.
8. **Reminders never re-fire and never fire into a void.** Due rows are claimed
   with `UPDATE ... WHERE reminded_at IS NULL RETURNING`; a target that is no
   longer publicly visible gets `reminded_at` written but **no** notification,
   otherwise every scan would retry it forever. Claim and notification
   projection share one transaction, so a failed projection rolls back and the
   next scan retries instead of marking a reminder delivered that never was.
9. **`bookmarkedByMe` is request-scoped and never cached**, decorated in
   `PublicReadService` after the cache read with one batched query per page —
   the same rule `likedByMe` follows, for the same cross-viewer contamination
   reason.
10. **No new RBAC permission key.** Login plus active account is the whole gate,
    matching likes. `features.bookmarks` is a public product flag (default on);
    disabled means all six routes return 403 `forum.bookmarks_disabled`, the
    profile tab is not rendered at all, and historical counters stay readable.
11. **Bookmarks do not feed `hot_score`.** The formula stays "comments + views +
    topic likes". Bookmarks are a private intent signal, not a popularity one.
12. **The stable read surface** is `sforum_core_v1.forum_bookmarks` (public rows
    on visible targets only, predicate mirrored verbatim from
    `forum_reactions`, no reminder columns) plus HostQuery
    `core.query.public_bookmarks.list`, both delivered in the same migration.

## Consequences

- The write path is one transaction: relation row → counter delta → event. The
  counter uses `GREATEST(x - 1, 0)` with a non-negative CHECK as the second
  gate; existing rows are read under `SELECT ... FOR UPDATE` so the delta is
  computed from the locked value, not from a stale read.
- `bookmark.created` / `bookmark.removed` are emitted after commit and are
  fail-open. Together with `reaction.created` they are the intended data source
  for any future badge plugin, so no write path has to change later.
- `sforum_core_v1.forum_bookmarks` mirrors `forum_reactions` visibility
  predicates verbatim (active user, active/locked topic, soft-delete excluded,
  public category) and adds only `visibility = 'public'`. Any future relaxation
  of that predicate is a privacy regression, not a simplification.
- Hiding or deleting a target drops it from the public list immediately (view
  predicate) while `public_bookmark_count` does **not** fall back. This matches
  the known `like_count` tradeoff; operator-run reconciliation SQL lives in
  `modules/forum.md`.
- A hard user delete cascades bookmark rows without decrementing counters. Same
  posture as reactions.
- `ListUserBookmarks` counts with the **same** visibility and target-visibility
  predicates as the page query. Counting on visibility alone produced a `Total`
  larger than the page and therefore a phantom "load more".
- `DELETE /topics/{id}/bookmark` and `DELETE /comments/{id}/bookmark` do not read
  a request body at all. A malformed body on DELETE is client misuse, not a 422;
  the contract declares no request body and no 422 for those two operations.
- The profile page's fourth core tab is `bookmarks`, ordered topics / replies /
  bookmarks / about. It is viewer-dependent on one route: the owner sees private
  entries with a lock marker, everyone else sees only public entries. The
  reminder chip is owner-only in the template *and* in its tooltip.
- `forum.go` provider file size was protected during the change by moving the
  bookmark attachment into `Providers/forum_bookmarks.go` and renaming
  `attachForumReactions` to `attachForumEngagement`; the provider keeps a single
  `BookmarkService` instance so the feature gate stays on the same chain.

## Alternatives rejected

- **Copying Discourse literally (private-only bookmarks)** — the operator wants
  a public collections surface; copying would have silently dropped a requested
  product feature.
- **All bookmarks public by default** — destroys "quietly save this" and creates
  the largest leak surface for later-hidden content.
- **Showing a total bookmark count including private rows** — leaks other
  people's private intent to authors and readers.
- **`remindAt: null` meaning clear** — cannot be distinguished from "field
  absent" through a `*time.Time`; the ambiguity is what produced the original
  "cleared but bounced back" bug.
- **A separate reminder table or endpoint** — the reminder has no identity
  independent of the bookmark; a second table would need its own cascade,
  limit, and consistency rules for no gain.
- **Per-user advisory lock for the reminder cap** — would serialize the common
  bookmark write path to enforce a soft limit on a rare feature.
- **Putting bookmarks in a new `app/Models/Bookmarks` package** — `Forum` needs
  `bookmarkedByMe` decoration while the bookmark list needs Forum's summary
  types; a separate package creates an import cycle. Bookmarks live as focused
  files inside `Forum`, exactly like reactions.
- **Caching `bookmarkedByMe`** — cross-viewer contamination.
