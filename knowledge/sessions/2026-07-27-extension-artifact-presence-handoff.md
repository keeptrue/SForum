# 2026-07-27 Extension Artifact Presence Handoff

## Changed

- Extension list/detail and admin page bootstrap now expose active and staged
  `artifactState` as `available` or `missing`, derived from the stored package
  path without scanning package trees.
- Missing artifacts fail closed for enable, theme activation, settings
  read/write/reset, and settings actions with
  `extension.artifact_missing`.
- Extension overview, plugin, and theme pages show a visible artifact-missing
  state and disable package-dependent actions. Settings catalog entries from a
  missing artifact are excluded.
- The extension overview now offers super admins one batch uninstall action for
  uploaded, deletable, disabled missing artifacts. Its required Modal lists
  every exact target and asks whether to preserve Host settings or delete only
  those settings.
- Cleanup is Host-owned and atomic. It does not execute missing plugin/theme
  code, retains immutable lifecycle history and plugin-owned business data, and
  records a catalog tombstone. Re-uploading the same extension ID removes the
  tombstone and restores the catalog identity.

## Decisions

- PostgreSQL remains authoritative for install/lifecycle/audit state; file
  presence is authoritative only for whether the exact artifact can be used.
- Missing records are not silently hidden or deleted. Lifecycle history stays
  inspectable, and cleanup remains an explicit super-admin action.
- Plugin-owned databases are never deleted by missing-artifact cleanup because
  the absent artifact cannot safely run export or custom cleanup hooks.

## Verification

- `go test ./app/Models/Extensions -run 'TestService(CleanupMissingArtifacts|ListMarksMissingArtifact)' -count=1`
- `bun test tests/adminExtensions.test.ts`
- `ruby scripts/validate-openapi-refs.rb`
- `bun run typecheck` reaches `vue-tsc` but currently fails on the unrelated
  existing handler return type in the admin settings shell
  (`app/pages/admin/settings/index.vue`; the old `:1066` line anchor is gone —
  that page has since been rewritten as a 262-line route shell, look for the
  tab-nav click handler).
- User verified the rendered admin behavior against the running development
  environment.
- Chrome QA on `/control-panel/extensions` confirmed 11 current candidates,
  the full target list, both data modes, mode-specific warning, cancel flow,
  and zero fresh console warnings/errors. No uninstall was submitted.

## Next

- The development database still contains seven historical `P4 lifecycle E2E`
  extension rows. The verified Modal currently includes those plus four other
  eligible missing-artifact fixtures. No tombstone was created during QA.
- Make the lifecycle E2E cleanup report SQL failures instead of discarding
  every cleanup error, so future test pollution is visible.
