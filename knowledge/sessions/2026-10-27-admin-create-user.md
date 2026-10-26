# 2026-10-27 Admin Create User

## Changed

- Added `POST /api/v1/users` (`user.manage`): one transaction creates the user,
  Argon2id credential, exact role set, and `user.admin_create` audit event.
  `password` and `generatePassword` are mutually exclusive; the generated
  password is returned once as `generatedPassword` and never persisted in
  plaintext.
- Added `AdminUserCreator` (focused collaborator) plus
  `GenerateCompliantPassword`; `Service` receiver-method cap stays at 47.
- Added `SFAdminUserCreateDialog.vue` (manual/random password, role multi-select,
  email-verified checkbox, field-level 422, one-time password result panel) and
  the title-row action on `/control-panel/users`.
- Extracted the read-only preview inspector into
  `SFAdminUserPreviewModal.vue`; `apps/web/app/pages/admin/users.vue` dropped from
  1516 to 1138 lines and the architecture baseline was lowered to match.
- Added OpenAPI `AdminCreateUserInput` / `AdminCreateUserResult`, V3 route identity
  `core.route.identity.create_user`, and admin component identities; regenerated
  the 18 V3 catalog files.
- Added `auth.password_required` / `auth.password_mode_conflict` localized
  messages, zh/en i18n keys, Go service/controller tests, and a web payload test.

## Decisions

- Reuse `user.manage`; no new permission key, migration, or role-template change.
- Server-side random password so it always satisfies the configured policy; the
  response is the only place the plaintext appears.
- Admin creation bypasses `identity.registration.enabled` and
  `user.before_register`, but still applies host username/email/password policy.
- Role set is exact; empty falls back to `member`; `super_admin` requires a super
  admin actor.
- No welcome or verification mail in v1.
- Created accounts are `human`, `active`, non-initial-super-admin, locale from the
  site default.

## Next

- Optional invite-link flow (one-time password setup) and welcome mail.
- Optional `user.create` split if operators should create without editing.
- Browser QA created `qa_manual_1027` and `qa_generated_1027` in the dev database;
  anonymize or erase them if the fixture should stay clean.

## Open Questions

- Should the dialog expose locale, status, and account kind, or keep them in the
  detail panel?
