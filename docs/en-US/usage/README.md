# Usage

[← English docs home](../README.md)

For **operators**: registration, admin configuration, forum operations, extensions.  
For contributors, see [Development](../development/README.md).

## Chapters

| Doc | Topic |
| --- | --- |
| [First registration & super admin](./first-login.md) | First user, `super_admin`, sessions |
| [Account & security](./account-security.md) | Email verification, passwords, sessions, PATs, external logins, appearance and notification settings |
| [Admin control panel](./admin.md) | Users/roles/permissions, forum & taxonomy, moderation, attachments, mail, SEO, webhooks, updates & jobs |
| [Forum day-to-day](./forum.md) | Taxonomy, posting, moderation |
| [Trust levels](./trust-levels.md) | Seven newcomer-limit keys, five tiers with per-tier limits, per-tier earned capabilities (off by default), restoring defaults |
| [Shortcodes](./shortcodes.md) | Built-in syntax, legacy compatibility, protected content and search/cache/SEO exclusion |
| [Rich content](./rich-content.md) | Diagrams, charts, formulas: syntax, limits, fallbacks, CSP, AI behavior |
| [Notifications](./notifications.md) | Inbox, preferences, Web Push, operator policy |
| [Search](./search.md) | Site search vs optional Meilisearch |
| [Extensions & themes](./extensions.md) | Install, enable, trust, activate |
| [GitHub login methods](./github-login.md) | Built-in GitHub setup, lifecycle, troubleshooting |

## Operator principles

1. **Recommended defaults first**, with one-click restore where configurable.  
2. **API authorization is authoritative**; hidden UI is UX only.  
3. **Plugin-first** for mail transport, optional search engines, storage vendors, etc.  
4. **Auditable trust** for executable plugins (exact-artifact super-admin confirmation).

## Common entry points (dev defaults)

| Entry | Path |
| --- | --- |
| Public site | `/` |
| Login / register | theme routes (typically `/login`, `/register`) |
| Admin | `/control-panel` (`NUXT_PUBLIC_ADMIN_ROUTE_PREFIX`) |
| Public profile | `/u/:user` |
| Profile settings | `/settings/profile` |
