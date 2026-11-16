# Decision: Permission Deny — the expanded set from the API is the only frontend authority

Date: 2026-11-17
Status: accepted
Related: `knowledge/decisions/2026-10-18-extension-data-surface-policy.md`
(identity surface), `knowledge/modules/identity.md` (Permission-Aware
Development Rules)

## Context

`Actor.Can()` resolves a permission by looking up the actor's permission map and
falling back to parent → child expansion; an explicit `false` entry (written by
`applyDenied`) beats that fallback, so an administrator can deny
`settings.mail.manage` while the role still holds the legacy parent
`settings.manage` (P1-1, `permission_compat.go`).

The frontend had its own copy of that expansion: `usePermissions().can()`
matched the delivered `permissions` array and, on a miss, walked a hardcoded
`LEGACY_PERMISSION_CHILDREN` table so a session holding only the parent key
would still light up child menus.

That copy could not be correct, because the session payload has no deny
information at all — `CurrentUser.permissions` is a flat array of effective
keys. Any deny applied by an administrator therefore disappeared at the frontend
boundary: the parent key was still in the array, the frontend re-expanded it,
the menu entry rendered, and the page it opened answered 403. The UI promised a
capability the API refused.

## Decision

Option ② — **the API's expanded, deny-subtracted set is the single authority,
and the frontend degrades to a pure lookup.**

- `ExpandEffectivePermissions(keys, denied)` owns expansion *and* subtraction.
  It keeps the granted parent key, adds its children, and skips any child that
  was explicitly denied.
- `canWithUser(user, permission)`
  (`apps/web/app/utils/identity/permissionCheck.ts`) is the only frontend
  implementation: `super_admin` bypass, then a plain membership test on
  `permissions`. `usePermissions().can()`, `useAuthSession().can` and
  `hasPermission()` all forward to it.
- The frontend must not expand parent keys, in any form: no re-introduced
  expansion table, no `can(child) || can(parent)` fallback in a page/tab gate,
  and no parent key inside an admin page's `requiredPermissions` candidates.
  A parent key is an expansion *source*, never a gate.

## Evidence

Both materialization paths expand and subtract, so the delivered array already
agrees with `Actor.Can()`:

- `apps/api/app/Models/Identity/postgres_store_roles.go:344` —
  `loadCurrentUserAccess` (the single materialization point for both
  `Actor.Permissions` and `CurrentUser.Permissions`, D13) calls
  `ExpandEffectivePermissions(effectivePermissionKeys(decisions), denied)`.
- `apps/api/app/Models/Identity/postgres_store.go:423` — `LoadActor` rebuilds
  the actor map from that same set and then applies `applyDenied` so `Can()`
  sees the explicit `false` entries.

Contract: `contracts/openapi/schemas/identity.yaml` documents the semantics on
`CurrentUser.permissions` and `AdminUserDetail.permissions`.

Tests: `apps/web/tests/identity/permissionDenyExpansion.test.ts` (allowed and
denied paths plus a regression gate that a parent-only session yields `false`),
`apps/web/tests/admin/adminNavPermissionCandidates.test.ts` (menu candidates),
`apps/web/tests/admin/adminManageGateCandidates.test.ts` (rendered page/tab
controls).

## Known side effects (accepted)

- A client still holding a session issued before the backend expansion existed —
  i.e. one carrying only parent keys — loses the child menu entries until the
  session refreshes. This is fail-closed and intended: the frontend can no longer
  distinguish "granted via parent" from "denied just now", so it must not guess.
- Any deployment that changes the expansion table changes what the frontend sees
  without a frontend change. That is the point: one table, one owner.

## Conditions that would reopen this decision

Re-introducing client-side expansion requires **both** of:

1. the session payload (and its OpenAPI contract) ships the denied key set, so a
   client can apply the same "explicit deny beats parent expansion" rule the API
   uses; and
2. a decision record supersedes this one, naming the contract field, the client
   helper that consumes it, and the allowed/denied tests that keep it honest.

Absent those, any parent-key expansion in the frontend is a defect, not a
compatibility convenience.
