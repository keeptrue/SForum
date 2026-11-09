# Iteration A — Engagement Loop Checklist

Status: **in progress** — Workstream 1 (view count) shipped; Workstream 2
(likes) landed on 2026-10-29 (see `../sessions/2026-10-28-content-reactions.md`:
migration applied, HostQuery + PG integration green, browser QA passed on flat,
switch-off evidence collected, final gate 9 PASS + `test.sh --fast` green);
Workstream 3 (bookmarks) not started  
Date: 2026-07-12  
Last status pass: 2026-07-21 knowledge cleanup  
Goal: turn “can read/post” into “can operate a living community” without
starting payments, marketplace, or horizontal scale work.

**Parent strategy:** `knowledge/plans/2026-07-12-development-directions.md`
(why Track 1 / Iteration A is first; Tracks 2–5, effort mix, and depriorities).

## Product Goal

A seeded forum should support:

1. Browse topics with **real view counts**
2. **Like / react** to topics and comments
3. **Bookmark** topics and find them later
4. Moderators continue to use **lock / pin / hide** (already largely shipped)

## Current Baseline (do not rebuild)

These are **already implemented**. Iteration A only polishes gaps.

| Area | Status | Evidence |
| --- | --- | --- |
| Topic lock / unlock / pin / unpin / hide / restore | **API + policy Done** | `POST /topics/:id/{lock,unlock,pin,unpin,hide,restore}`, `ApplyTopicAction` |
| Topic edit / delete | **API Done** | `PATCH/DELETE /topics/:id` |
| Topic detail action menu UI | **Mostly Done** | default theme `SFTopicActionMenu` + `buildTopicActionMenuItems` + `applyTopicAction` |
| Permissions | **Done** | `topic.lock`, `topic.pin`, `topic.edit_any`, `topic.delete_any`, author edit via `post.edit_own` |
| `topics.view_count` column + read display | **Schema + UI Done** | migration + list/detail JSON + `SFHomeTopicRow` |
| View count **increment** | **Missing** | no Redis counter / write path |
| Likes / reactions | **Landed (2026-10-29)** | `content_reactions` + `topics/comments.like_count` + 6 routes + `SFReactionControl`/`SFTopicReactionBar`; HostQuery `sforum.core.public_reactions.list`; migration applied in dev; PG integration + web 1837 green; browser QA passed (flat; tree unreachable); switch-off evidence collected; final gate green |
| Bookmarks / favorites | **Missing** | only extension contribution demo uses bookmark icon |

## Out Of Scope For Iteration A

- Attachment-in-editor wiring (Iteration B)
- Plugin uninstall / upgrade (Iteration C)
- Second provider slot
- Payments, PM, follows, OAuth
- Load-test suite (optional tiny spike only if time left)
- Category-scoped moderators
- Email verification gates

---

## Workstream 0 — Lifecycle Polish (small, first if needed)

Only if QA finds holes. Prefer fixing before building likes.

### 0.1 Permission / UX audit

- [ ] Verify super_admin and a custom role with `topic.lock` / `topic.pin` /
  `topic.delete_any` see the correct menu items on topic detail
- [ ] Verify member without those permissions does **not** see lock/pin/hide
- [ ] Verify author sees edit (own) and not pin/lock unless granted
- [ ] Confirm Toast success + error non-auto-dismiss for failures
- [ ] Confirm locked topic rejects new comments (API + UI composer disabled)

### 0.2 Optional small fixes (only if broken)

- [ ] Align frontend permission keys with backend if any drift
- [ ] After pin/lock, list pages show badges without full reload (cache TTL is
  OK; optional optimistic local state)
- [ ] Hidden topic not in public list/search; restore re-indexes (already
  covered by index jobs — re-verify)

**Exit criteria:** moderator can manage a topic from the public detail page
without using admin SQL.

---

## Workstream 1 — View Count (cheap, high visibility)

**Also required by** `plans/archive/2026-07/2026-07-21-million-scale-read-path.md` **M2**
(load acceptance + `hot_score` coupling). Implement product behavior here once;
do not fork a second view-count design.

### Design decisions (recommended defaults)

| Decision | Recommendation | Why |
| --- | --- | --- |
| Count what | Public topic detail successful load | Matches displayed `viewCount` |
| Who counts | Anonymous + logged-in | Forum convention |
| Dedup window | Per visitor key, **30 minutes** per topic | Avoid refresh spam |
| Visitor key | Session id if logged in; else hashed IP+UA cookie/fingerprint server-side | No client trust |
| Write path | Redis `INCR` + periodic flush job to PG | Avoid write storm on PG |
| Failure mode | Redis down → skip count (do not fail page) | Detail read is more important |
| Search index | Flush updates `view_count` in PG; optional reindex batch later | Not every view |

### 1.1 Schema / jobs

- [x] Confirm `topics.view_count` remains source of truth after flush
- [x] Add River job kind e.g. `forum.flush_view_counts` (or reuse maintenance
  queue) that:
  - reads Redis keys `forum:topic:views:delta:{topicID}` (or a set of dirty IDs)
  - `UPDATE topics SET view_count = view_count + $delta`
  - clears delta keys
  (also `hot_score += delta` for million-scale M2)
- [x] Register job in worker bootstrap
- [x] Schedule: every 30–60s or on N dirty keys threshold (**45s**)

### 1.2 API

- [x] On `GET /topics/:id` and `GET /topics/by-slug/:slug` **after** successful
  public detail resolve, call `RecordTopicView(ctx, topicID, visitorKey)`
- [x] Do **not** count admin-only / non-public statuses
  (only after public GetTopic success; search/list/admin not wired)
- [x] Optional: return still-stale `viewCount` (pre-flush) — acceptable
- [x] OpenAPI: no new endpoint required if counting is side effect of GET;
  document behavior in path description
- [x] No `POST /topics/:id/view` in v1 (D3)

**Recommended:** server-side record inside GET detail for SSR + client
navigations that hit the API once. **Done.**

### 1.3 Dedup store

- [x] Redis key `forum:topic:viewed:{topicID}:{visitorHash}` TTL 30m
- [x] Only INCR delta when SETNX succeeds

### 1.4 Tests

- [x] Unit: first view increments delta; second within TTL does not
- [x] Unit: flush applies sum to store
- [x] Integration-style service test with fake Redis/cache (MemoryTopicViewCounter)
- [x] Ensure detail still 200 when Redis unavailable (FailRecord / nil recorder)

### 1.5 Frontend

- [x] No UI change required beyond existing eye icon + count
- [x] Optional: after navigation, show count+1 optimistic only if product wants
  (default: no optimism; wait for next fetch)
  (useAsyncData single detail GET; no POST /view)

**Exit criteria:** refreshing a topic within 30m does not +N; after flush job,
PG `view_count` and list UI increase for unique visitors.
**Met** (see `reports/2026-07-21-perf-m2-view-hot.md`; landed with million-scale M2).

---

## Workstream 2 — Reactions (Like v1)

### Design decisions (recommended defaults)

| Decision | Recommendation | Why |
| --- | --- | --- |
| Scope v1 | Single reaction type: **like** | Ship fast; schema allows expansion |
| Targets | `topic` and `comment` | Matches feed + detail UI |
| Multiplicity | One like per user per target; toggle off | Simple UX |
| Counts | Denormalized `like_count` on topics/comments | List performance |
| Auth | Login required | Prevent spam bots without CAPTCHA complexity |
| Permission | Reuse login + not banned; no new permission key in v1 | Beginner-friendly |
| Events | Observe event `reaction.created` / `reaction.removed` | Plugin hooks later |
| Notifications | **Out of scope A** (no “X liked your post” yet) | Avoid fanout scope creep |

### 2.1 Data model

Migration (Goose), suggested:

```sql
-- reactions: one row per user per target
CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  target_type TEXT NOT NULL CHECK (target_type IN ('topic', 'comment')),
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like' CHECK (reaction IN ('like')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, target_type, target_id, reaction)
);
CREATE INDEX content_reactions_target_idx
  ON content_reactions (target_type, target_id, reaction);
```

- [ ] Add `like_count BIGINT NOT NULL DEFAULT 0` to `topics` and `comments`
  (or a single generic counter table — prefer columns for v1 speed)
- [ ] CHECK `like_count >= 0`

### 2.2 Domain API

Suggested routes (modular OpenAPI under `forum`):

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/api/v1/topics/:topicID/like` | Idempotent like |
| `DELETE` | `/api/v1/topics/:topicID/like` | Unlike |
| `POST` | `/api/v1/comments/:commentID/like` | Idempotent like |
| `DELETE` | `/api/v1/comments/:commentID/like` | Unlike |

Response shape (envelope):

```json
{
  "code": 200,
  "message": "...",
  "data": {
    "liked": true,
    "likeCount": 12,
    "targetType": "topic",
    "targetId": 1
  }
}
```

- [ ] Service: permission = authenticated + active user
- [ ] Transaction: insert/delete reaction + update counter
- [ ] 404 if target missing or not publicly visible
- [ ] 401 if anonymous
- [ ] Emit observe events (optional but preferred)
- [ ] Invalidate forum cache generation / topic detail cache on count change

### 2.3 Read model

- [ ] `TopicSummary` / `TopicDetail` include `likeCount`
- [ ] `Comment` include `likeCount`
- [ ] When actor present, include `likedByMe: boolean` on detail + comment list
  (batch query reactions for page of comment IDs — no N+1)
- [ ] Search documents: optional `likeCount` field later; not required for A

### 2.4 OpenAPI / permissions / i18n

- [ ] `contracts/openapi/paths/forum.yaml` + schemas
- [ ] `ruby scripts/validate-openapi-refs.rb`
- [ ] No new RBAC permission unless product wants `reaction.create` later
- [ ] zh-CN / en-US strings for buttons and toasts

### 2.5 Frontend

- [ ] Topic detail: like button near heading/actions (icon library, no emoji)
- [ ] Comment row: like toggle
- [ ] Homepage row: show `likeCount` if cheap; click-through like optional
  (prefer detail-only write in v1 to keep list simple)
- [ ] Toast on success; keep liked state optimistic with rollback on error
- [ ] `useForumApi` helpers: `likeTopic`, `unlikeTopic`, `likeComment`, …

### 2.6 Tests

- [ ] Service: like increments; second like no double count; unlike decrements
- [ ] Service: cannot like hidden/deleted target
- [ ] Controller: 401/404/200 paths
- [ ] Frontend unit for presentation helpers if extracted
- [ ] `go test` forum package + relevant validate script if added

**Exit criteria:** logged-in user can like/unlike topic and comment; counts
stable under double-click; anonymous gets 401.

---

## Workstream 3 — Bookmarks (Favorites)

### Design decisions (recommended defaults)

| Decision | Recommendation | Why |
| --- | --- | --- |
| Scope | Topics only in A | Comments bookmarks rare |
| Auth | Login required | Personal list |
| List page | `/my/bookmarks` or profile tab | Discoverability |
| Order | `created_at DESC` | Simple |
| Permission | No new key | Login enough |
| Events | `bookmark.created` / `bookmark.removed` observe | Optional |

### 3.1 Data model

```sql
CREATE TABLE topic_bookmarks (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  topic_id BIGINT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, topic_id)
);
CREATE INDEX topic_bookmarks_user_created_idx
  ON topic_bookmarks (user_id, created_at DESC);
```

### 3.2 API

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/api/v1/topics/:topicID/bookmark` | Idempotent add |
| `DELETE` | `/api/v1/topics/:topicID/bookmark` | Remove |
| `GET` | `/api/v1/me/bookmarks` | Paginated topic summaries |

- [ ] Only bookmark publicly visible topics
- [ ] List reuses topic summary shape + `bookmarkedAt`
- [ ] Detail includes `bookmarkedByMe` when actor present
- [ ] OpenAPI + i18n + tests mirror reactions quality bar

### 3.3 Frontend

- [ ] Topic action menu or heading: bookmark toggle (`i-lucide-bookmark`)
- [ ] Navbar or user menu entry → `/my/bookmarks`
- [ ] Empty state with plain-language CTA to browse home
- [ ] Toast on add/remove

**Exit criteria:** user bookmarks 3 topics, sees them on list page, removes one.

---

## Workstream 4 — Integration Hardening

- [ ] Seed script or manual QA script using `seed:forum` data
- [ ] Permission matrix smoke: member / moderator-like role / super_admin
- [ ] Cache: after like/bookmark/view flush, list/detail not stuck wrong forever
  (generation bump or short TTL acceptable)
- [ ] Update `knowledge/modules/forum.md` Current Status
- [ ] Short session handoff when A ships
- [ ] Revisit maturity audit Part A/B only if scores materially move

### Manual QA script (copy for testers)

1. Register/login member A and B  
2. A opens topic → view_count +1 within flush window; refresh no +1  
3. A likes topic and a comment; B sees counts; A unlikes  
4. A bookmarks topic; opens My Bookmarks; removes bookmark  
5. Moderator locks topic → B cannot comment; unlock restores  
6. Moderator pins topic → appears pinned on home  
7. Deny paths: logged-out like/bookmark → 401  

---

## Suggested Implementation Order

```text
Day 1     Workstream 0 audit + Workstream 1 view_count skeleton
Day 1–2   View flush job + detail wiring + tests
Day 2–4   Workstream 2 reactions (schema → service → API → UI)
Day 4–5   Workstream 3 bookmarks
Day 5     Workstream 4 QA + knowledge + handoff
```

If time-boxed to a shorter slice, ship in this cut order:

1. **View count** (visible, small)  
2. **Topic like only** (defer comment like)  
3. **Bookmarks**  
4. Comment like as fast follow  

---

## File Touch Map (expected)

### Backend

- `apps/api/database/migrations/YYYYMMDDHHMM_*.sql`
- `apps/api/app/Models/Forum/{types,store,postgres_store,service,cached_store}.go`
- `apps/api/app/Http/Controllers/Forum/{controller,routes}.go`
- `apps/api/app/Jobs/Forum/` (view flush; optional)
- `apps/api/app/Support/Events/catalog.go` (new observe events)
- `apps/api/bootstrap/{app,worker}.go` (job registration)
- `contracts/openapi/paths/forum.yaml`, `schemas/forum.yaml`

### Frontend / theme

- `apps/web/app/composables/useForumApi.ts`
- `apps/web/app/utils/forumTaxonomy.ts` (types)
- `apps/web/app/utils/forumTopicPresentation.ts` (menu items if bookmark joins menu)
- `apps/web/i18n/locales/{zh-CN,en-US}.json` (or project locale paths)
- `extensions/builtin/themes/sforum-default/layer/app/pages/t/[...path].vue`
- `extensions/builtin/themes/sforum-default/layer/app/components/*`
- new page e.g. `.../pages/my/bookmarks.vue`
- tests under `apps/web/tests/` and `apps/api/.../*_test.go`

### Knowledge

- `knowledge/modules/forum.md`
- `knowledge/sessions/YYYY-MM-DD-iteration-a-engagement.md`
- optionally tick this plan’s checkboxes

---

## Definition Of Done (Iteration A)

- [ ] Unique topic views eventually persist to `topics.view_count`
- [ ] Logged-in like/unlike for topics **and** comments (or documented cut)
- [ ] Logged-in topic bookmarks + list page
- [ ] Existing lifecycle actions still green in tests
- [ ] OpenAPI refs validate
- [ ] `go test` for touched packages green
- [ ] Frontend typecheck or targeted tests for new helpers
- [ ] Toasts + i18n for user-facing mutations
- [ ] Module note + session handoff updated

## Non-Goals Reminder

Do not expand A into:

- reaction notifications
- multi-emoji reaction picker
- comment bookmarks
- reputation/score economy
- attachment composer pipeline

Those belong to later iterations.
