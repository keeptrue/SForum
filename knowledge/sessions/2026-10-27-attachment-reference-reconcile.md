# 2026-10-27 Attachment Reference Count Reconciliation

## Changed

- `POST /api/v1/admin/attachments/reconcile` (`reconcileAdminAttachmentReferences`)
  is new: body `{dryRun}`, response `AdminAttachmentRecountResult`
  (`lowered` / `raised` / `unchanged` / `applied` / `samples` ≤ 20 attachment ids).
  `dryRun` defaults to `true`, including for an empty body; a dry run issues only
  `SELECT`s, apply corrects only drifting rows (idempotent) and returns
  `applied: true`.
- Backend files: `Models/Attachments/reconcile.go` (service + `RecountResult` +
  optional `ReferenceCountReconciler`, so `Store` was not widened),
  `reconcile_postgres.go` (single-snapshot CTE stats + the idempotent UPDATE),
  `Http/Controllers/Attachments/controller_reconcile.go` (+ `decodeReconcileRequest`).
  Route identity `core.route.attachments.reconcile_admin` is registered in
  `docs/extensions/v3/catalog-identities.json`; generated guard is
  `permission_any` + `attachment.manage` (never `service-policy`/`public`).
- `CleanupResult` now reports `skipped` (zero-reference rows still inside the
  grace window) and `retentionDays` (the window used by this run), so an
  `attachment.manage`-only operator can explain the wait without calling
  `GET /admin/attachment-settings` (`attachment.settings.manage`).
- `cleanupAdmin` decoding hardened: `limit` is a pointer, an empty body falls
  through to the service default, and malformed JSON returns
  `400 attachments.cleanup_body_invalid`. Before this, `_ = c.Bind().Body()`
  swallowed the error into `limit = 0`, which the service normalizes to 100
  rows of deletion — a fail-open.
- Admin overview `OverviewAction.Advisory` (`advisory` in the OpenAPI schema,
  optional, not required) marks `orphan_attachments` as informational so the
  actionable total and alert tone ignore it while it stays in the summary list.

## Decisions

- Reference counts are maintained by write paths only. Global recount is an
  operator action, never part of `cleanupOrphans` or any job: historical content
  may legitimately carry a count with no reference row, and a silent recount
  would let the retention window delete attachments that are still in use.
- `Store` deliberately stays narrow; maintenance capabilities use optional
  interface assertions (`ReferenceCountReconciler`,
  `CountDeferredCleanupCandidates`), matching the existing variants pattern.
- Request bodies that can trigger destructive work use pointer fields plus an
  explicit empty-body branch; bind errors surface as 400 instead of degrading
  into a zero value.
- `samples` stays `[]int64` (ids only). Showing per-row before/after counts was
  rejected for this round because it would widen the contract.

## Next

- `apps/api/app/Http/Controllers/Attachments/controller.go` grew 623 → 642 lines
  (already above the 500-line review threshold, not in the legacy baseline).
  Next change near `decodeCleanupRequest` should extract the whole cleanup
  decode/handler block into `controller_cleanup.go` instead of growing that file.
- Frontend still needs its half verified in a browser: KPI excludes advisory
  rows, the cleanup toast reads `skipped` + `retentionDays`, and the reconcile
  dialog renders the id list from `samples`.
- `knowledge/index.md` Latest Handoff pointer is intentionally untouched (user
  has in-flight edits there); the Lead owns that update.

## Open Questions

- The admin overview orphan KPI counts `reference_count = 0 AND status <> 'deleted'`,
  which includes `disabled` rows, but cleanup candidates are only `deleted`
  (past `deleted_at`) and `active` (past `created_at`). A disabled
  zero-reference attachment is therefore counted forever and never auto-cleaned.
  Either the KPI or the cleanup predicate needs a product decision.
- The dev database was reported to hold 28 rows with `reference_count = 1` and
  no reference row. The reconcile endpoint makes the drift observable and
  correctable, but the write path that produced it was not reproduced; treat the
  endpoint as mitigation, not root-cause closure.
