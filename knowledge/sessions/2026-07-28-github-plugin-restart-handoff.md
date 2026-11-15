# 2026-07-28 GitHub Plugin Restart Handoff

## Changed

- Added the Host-owned plugin restart API, controller route, audit action, and
  OpenAPI contract.
- Restart now preflights the exact target and authority before downtime,
  quarantines the active runtime through normal disable, and uses deterministic
  phase idempotency keys.
- Legacy active plus staged Lifecycle V2 is bridged by exact CAS promotion
  while disabled. Failed bridges remain disabled and can resume.
- Built-in trust preview now returns `trustRequired=false` instead of the
  migration-gate 409.
- Admin restart uses `/restart`; staged targets reuse the trust/capability
  review dialog.
- Restart recovery explicitly requests trust status and challenges with
  `target=staged`, so a disabled plugin's token binds the staged version and
  digest rather than the old active artifact.
- Disabled plugins with an exact staged artifact remain restartable in the
  admin UI, and every legacy/V2 branch checks changed-target capability
  confirmation before reaching runtime downtime.

## Decisions

- Restart is lifecycle orchestration, not enable aliasing. See
  `../decisions/2026-07-28-host-owned-plugin-restart.md`.
- Do not enable `SFORUM_V3_TRUST_CHALLENGES` to work around built-in trust
  status, and do not mutate immutable package or registry history.

## Verification

- Focused Extensions restart/trust tests, the full Extensions controller
  package, Audit tests, Go build, 26 Bun extension/trust tests, OpenAPI refs,
  and `git diff --check` passed.
- Nuxt typecheck reaches an unrelated pre-existing error in the admin settings
  shell (`app/pages/admin/settings/index.vue`; the old `:1066` anchor no longer
  exists after that page was rewritten as a route shell): its click handler
  returned a string instead of `void | Promise<void>`. The restart composable
  tests pass.
- The broader Extensions model package still has pre-existing
  `extension.artifact_missing` failures from unfinished artifact-presence
  fixture updates; restart-focused tests pass.
- Development API restarted on `8081`; the user-owned Web server on `3000`
  was not restarted.
- Authenticated Browser verification enabled and restarted
  `sforum.auth-github` without a trust 409 or new exact-fence conflict.
- PostgreSQL recorded lifecycle operations `3955` disable and `3956` enable as
  succeeded, audit event `3833` as `extension.restart`, and Identity Registry
  revision `7` active for exact version id `15357` and digest
  `9f0f850e0f38e7c5e6d7c1d18027694bb57007e29ba0f41af55b597c81c4cf2a`.

## Next

- Preserve the existing independent R1-R7 review request; this repair does not
  self-close that review.
- Finish the separate artifact-presence fixture updates so the complete
  Extensions model package returns green.

## Open Questions

- None for the restart repair.
