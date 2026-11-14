# Modules

One living note per feature area. Prefer these over session archive prose when
implementing or debugging.

## Active module notes

| File | Owns |
| --- | --- |
| `frontend.md` | Nuxt app, Page Registry themes, admin UI, SF components, SSR |
| `backend.md` | Go API layout, bootstrap, cross-cutting HTTP/runtime, security & dependency governance |
| `forum.md` | Topics, comments, taxonomy, content model, public read models |
| `identity.md` | Users, sessions, RBAC, registration/login, verification |
| `profile.md` | Public profiles and profile settings |
| `attachments.md` | Uploads, storage providers, governance, orphan cleanup |
| `options.md` | Runtime `web_options`, personalization, site chrome |
| `search.md` | Search framework, site PG FTS default, optional Meili |
| `mail.md` | Mail provider slot and core mail framework |
| `notifications.md` | In-app notifications + email projection |
| `moderation.md` | Reports, pre-publication review, moderator workbench |
| `jobs.md` | River queue, schedules, worker ops |
| `localization.md` | i18n rules, locales, language packs |
| `ai.md` | AI gateway contracts, credentials, admin console |
| `extensions.md` | Plugins/themes, Manifest V3, trust, Host registries |
| `badges.md` | Badge definitions/grants, per-user activity counters, `top_badge_id` |
| `trust-levels.md` | Activity-derived trust tiers on top of RBAC (planned; not shipped) |

## Note structure

Each module note should include:

- Purpose
- Current status
- Important files
- Dependencies
- Open questions
- Next steps

## Conventions

- Update the module note when finishing work in that area.
- Do not invent parallel module files for sub-features; nest sections instead.
- Generated or giant catalogs belong under `docs/`, not here.
