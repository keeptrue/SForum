# Moderation Module

Configurable pre-publication review, user reports, admin management, and the
frontend moderator workbench.

## Permissions And Entry Points

- `moderation.manage` controls the admin **Moderation management** page under
  the Forum management sidebar folder. It can read/update/reset policy settings
  and read the complete audit history, but it does not grant review actions.
- `moderation.review` controls the frontend `/moderation` workbench and all
  review actions. It does not grant policy management.
- `moderation.view_ip` reveals full client IP fields (`ipAddress`,
  `lastEditIp`) on workbench pending items, report items, and review context.
  Without it, the API strips those fields even for reviewers. The moderator
  role template includes it by default.
- The review and policy permissions are intentionally independent.
  `super_admin` still passes them through the existing super-admin policy.

## Backend

- `moderation_reports` stores individual user reports. The same reporter cannot
  create multiple open reports for one target.
- Report create/update mutations return the same enriched row projection as
  report reads. Their writable CTEs keep the committed database result and API
  response aligned, including reporter and reviewer display names.
- `moderation_settings` is a singleton policy record. Modes are `off`,
  `rules`, and `all`; `off` is the upgrade-safe stored default while the admin
  UI recommends `rules` for ordinary operation.
- Built-in rule mode can review new-user content and explicit external HTTP(S)
  links. The site hostname is excluded. More advanced risk/sensitive-content
  detection belongs behind stable filters/events or plugins.
- Topics and comments support `pending` and `rejected`. Neither status is
  returned by public reads, counted in public statistics, or indexed. Approval
  updates counters and search derivatives after the decision transaction.
- A successful pre-publication approval invalidates Forum topic detail,
  global/category/tag lists, taxonomy, and comment-list derivatives only after
  the decision transaction commits. Rejection does not advertise a public
  cache generation.
- Forum content revisions V1 is complete: topic/comment creation writes version
  1; authorized history remains limited to `topic.revision.view_any` /
  `post.revision.view_any`; edits use CAS and accepted snapshots; restore is
  append-only. The moderation boundary remains unchanged: restore can re-render
  and re-evaluate the current pipeline but must not publish or change `pending`,
  `rejected`, `hidden`, or `deleted` lifecycle state. Rejected/failed edits
  create no revision or audit success record.
- `moderation_decisions` stores immutable action, reviewer, note, trigger
  snapshot, target, source, and timestamp audit records.

### Endpoints

- `POST /api/v1/moderation/reports` remains the login-required report entry.
- `/api/v1/admin/moderation/settings`, `/settings/reset`, and `/decisions`
  require `moderation.manage`.
- `/api/v1/moderation/workbench/*` exposes counts, pending content, enriched
  reports, history, complete safe-rendered context, and explicit decisions;
  all endpoints require `moderation.review`.
- `GET /api/v1/me/content-review` is login-required and returns only the
  current author's pending/rejected topics and comments with rejection notes.

## Frontend

- `moderation.review` remains a Core-owned, non-replaceable workbench. It is
  separately `themeable`: the active theme may provide the public navbar/body/
  footer L1 shell only when it embeds the required `sf-moderation-review` Host
  island. Middleware, permissions, state, API calls, rendering safety, and
  decisions stay inside `SFModerationReviewPage` and Core APIs.
- Admin sidebar places **Moderation management** under the Forum management
  folder (`admin.nav.forum`), alongside categories, tags, and forum settings.
- The admin page is policy management plus a read-only complete audit table.
- The frontend `/moderation` workbench has **Pending publication**, **User
  reports**, and **History** sources. Layout reuses the public three-column
  chrome from home/notifications: left shell is `SFHomeNavigation` (route mode)
  plus workbench sources/type filters in `#after-navigation`; right rail uses
  the same section stack language (large overview number + `dl` stats + help
  copy in queue mode; decision rail in review mode). The left navigation uses
  `SFResponsivePublicSidebar` and the shared `usePublicSidebarDrawer`
  controller, so desktop and mobile render one sidebar instance; the separate
  right information drawer continues to use `forum-mobile-info-open`.
  Queue mode is URL-backed by source, content type, and page; review mode adds
  stable item query fields and keeps navigation inside the current
  source/filter/page, including history items whose source is a report. Rows
  are fully clickable, context uses the existing safe-rendered HTML path, and
  history remains read-only.
- The workbench center column uses `--sf-public-surface`; the surrounding page
  canvas keeps `--sf-public-bg`. This matches profile settings and preserves
  the active theme's intended foreground/background contrast.
- Workbench CSS is registered in Nuxt's initial global stylesheet set rather
  than the async theme island, so a hard refresh cannot briefly inherit the
  generic oversized `h1` rule before the moderation styles arrive.
- Destructive actions require a review note. Successful actions use the active
  theme color and auto-dismiss after 10 seconds; blocking errors stay visible.
- Pending topic creation shows a toast and returns the author home; review
  outcomes arrive via notifications. `GET /api/v1/me/content-review` remains
  available for API/clients (no first-party `/my` page). Pending comments show
  review-submitted feedback and are not inserted into the public comment list
  before approval.
- After an approval action, both workbench decision surfaces clear Nuxt's
  homepage async-data entries and persisted `useState` feed authority. The next
  homepage navigation therefore reads the newly published API generation in
  the same browser. `/` uses `Cache-Control: no-store`; Redis generation caches
  remain the server-side scale boundary.
- The public report dialog remains available on topics and comments.

### Public Report Dialog Reactivity Regression (2026-11-14)

- **Symptom**: on the topic detail page, both the per-comment **Report**
  action and the topic header dropdown **Report** action did nothing — no
  dialog, no error, no network call.
- **Root cause**: `apps/web/app/composables/moderation/useModerationReportDialog.ts`
  returned its state machine wrapped in `reactive({...})`. `reactive` unwraps
  refs on property read, so the caller
  (`apps/web/app/components/forum/SFTopicShowPage.vue`, which destructures the
  returned object) received **one-shot snapshots** instead of refs:
  `target` was permanently `null`, `reason`/`body`/`submitting`/`error`/
  `success`/`reasonOptions` were frozen at their initial values. The compiled
  template emitted `open: Boolean(_unref(reportingTarget))` (always `false`),
  so `SFReportDialog.vue`'s root `v-if="open"` never rendered the dialog.
  The same unwrapping also compiled `@update:body` into
  `reportBody.value = $event` while `reportBody` was the plain string `''`,
  which throws a `TypeError` in strict mode.
- **Regression origin**: commit `60fe97305` (2026-10-03) extracted the inline
  page state machine into the composable. The feature never worked after that
  refactor; the breakage predates any recent change.
- **Fix**: the composable now returns the ref contract directly
  (`target`, `reason`, `body`, `submitting`, `error`, `success`,
  `reasonOptions`, `open`, `close`, `setReason`, `submit`) with no `reactive`
  wrapper. The page's existing destructuring keeps working, and its template
  bindings became live again. No behavior, field name, or reason allowlist
  changed; the page diff is a comment correction only.
- **Regression guard**: `apps/web/tests/moderation/moderationReportDialog.test.ts`
  — contract level (the destructured values must stay refs; `open()` must set
  `target.value`), render level (a harness that mirrors the page's template
  bindings must open the dialog, accept a reason and body, and submit the
  correct `{targetType, targetId, reasonCode, body}` payload into the success
  state, asserted as the real outbound shape `path '/moderation/reports'` +
  `method 'POST'` + that body), the `canReport() === false` no-op case, and a
  guard that the test double refuses to overwrite a foreign stub.
- **Verified end to end**: on a real dev server the comment-row Report action
  opens the dialog, `POST /api/v1/moderation/reports` returns 201, the success
  state closes the dialog after 2 seconds, a duplicate report returns the
  business error "你已经举报过该内容。" inline while the dialog stays open, the
  topic header dropdown opens the same dialog for a topic target, and both
  records appear in the `/moderation` workbench's **User reports** tab (count
  2, status "进入审阅").

### Reusable Rule: Composable State Machines Return Refs

- A composable that exposes a **state machine** must return refs. Wrapping the
  result in `reactive()` and then letting callers destructure it is forbidden:
  `reactive` unwraps refs on property read, so the destructured values become
  one-shot snapshots and every later mutation is invisible to the template.
- When a caller destructures a composable's return value, the composable must
  return refs (`const { target } = useSomething()` keeps `target` reactive).
  The failure mode is silent: the UI renders, but reads the initial value
  forever (e.g. a dialog whose `v-if="open"` never becomes true).
- Fix pattern: drop the `reactive()` wrapper and return the refs directly, or
  return a `reactive` object that callers must **not** destructure. Never mix
  the two. State the chosen contract in the composable's header comment and
  guard it with a test that asserts `isRef(...)` after destructuring.
- Repo-wide sweep (2026-11-14): `return reactive(` occurs in only two places
  under `apps/web/app/composables` and `apps/web/app/utils` —
  `useModerationReportDialog.ts` (fixed here) and `useTopicCommentLive.ts`.
  The latter's consumers hold the object as a whole
  (`const commentLive = useTopicCommentLive({...})` in
  `SFTopicShowPage.vue:470`; `SFCommentStreamNotice.vue` only imports the
  type), so its ref unwrapping is safe — **no second instance of this bug** —
  and its header now states the positive rule: a composable that returns a
  `reactive` object must always be held as a whole and never destructured.

## Non-Goals

- No task claiming, assignment, internal moderator discussion, SLA, custom
  sensitive-word engine, machine-learning risk score, or rate-limit auto-hide.
