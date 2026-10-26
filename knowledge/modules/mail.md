# Mail Module

Core owns a provider-neutral asynchronous mail framework. It does not implement
SMTP, log delivery, no-op delivery, authentication, or TLS.

## Core

- `mail.provider` is a first-class extension provider slot.
- `mail_deliveries` records `queued/sending/sent/failed/skipped` state without
  storing provider credentials.
- Core renders password-reset, registration-welcome, reply, mention, and
  moderation email as paired text/HTML transactional templates. The HTML
  structure follows the mail template studio's restrained branded card layout;
  untrusted names, review notes, and URLs are escaped before insertion.
- Core also renders `identity.email_verification` as a branded, localized
  text/HTML template. Explicit requests from the verification waiting page enqueue it
  best-effort through the existing outbox/River pipeline; delivery failure does
  not roll back account creation. The verification token is never included in
  persistence or logs except as a one-use URL value delivered to the recipient.
- Core snapshots the active public brand with each delivery: configured
  `site.logo_url` is preferred, then `site.favicon_url`, then the first visible
  character of `site.name`; `appearance.theme` supplies the accent, soft, and
  border colors. Relative brand URLs become absolute from `site.url`, so mail
  clients never need to infer the forum origin.
- Every queued Core template stores its resolved `locale`. Password-reset and
  registration flows choose the request browser language first, then the
  account language, then `site.default_locale`; recipient notifications carry
  the recipient's saved language snapshot because they are emitted outside the
  recipient's request, and fall back to `site.default_locale` when a legacy
  account locale is empty. A later preference or site-default change never
  changes an already queued email.
- `mail.welcome.enabled` is controlled from Mail settings, defaults to
  `disabled`, and is authorized by `settings.mail.manage` (with the existing
  settings compatibility grant). Welcome delivery is best-effort after a
  successful password or external registration, so a queue outage cannot block
  account creation.
- River job `mail.deliver` carries only a delivery ID and runs on the `mail`
  queue. Standalone workers reconcile enabled plugin runtimes themselves;
  when the worker is embedded in the API it reuses the API extension runtime
  (single plugin process per enabled backend extension).
- No resolvable provider produces `skipped/provider_unconfigured`; a selected
  provider that is temporarily disabled or restarting is retried instead of
  being terminally skipped. In-app notifications and forum writes remain
  successful.
- Password-reset token, delivery, and River job are inserted in one PostgreSQL
  transaction. The public request remains enumeration-safe.
- Explicit password-recovery and email-verification sends share the
  server-authoritative `identity.mail_resend.*` policy configured under Site
  Settings → Account security. SMTP test mail is an operator diagnostic and is
  deliberately excluded from that end-user resend policy.
- `settings.manage` protects provider selection, reset, recent deliveries, test
  mail, and mail-provider plugin settings. `extension.manage` still controls
  plugin enable/disable. Disabling the selected plugin keeps the mail provider
  preference: it is reported unavailable while disabled and becomes effective
  again on enable. Uninstall clears it.
- Core admin route `/settings/mail` is the unified Mail and Notifications
  settings surface. It owns mail provider configuration, mail delivery history,
  notification type policy, and external channels; existing `/admin/mail/policy`
  routes remain compatibility projections over the Notification resolver.
- The admin Mail surface follows the shared settings geometry: compact page
  title, standard refresh toolbar, fixed Overview/Mail/Delivery/Type
  Policy/External Channels tabs, and one settings panel per tab. Tabs are
  filtered by the actor's independent mail and notification permissions.
  Overview guidance is a vertical status and setup flow inside the panel rather
  than a separate hero or card grid.
- Notification email is opt-in by default. Restoring the notification policy
  enables in-app delivery but disables reply, mention, and moderation email;
  operators can enable email per notification type, after which members may
  choose their own email preference.
- Provider selection and settings navigation remain extension-generic.
  Deliveries owns status, template, and reason localization. Queued test mail
  is not presented as synchronously delivered.
- `POST /admin/mail/test` recipient resolution: explicit JSON `recipient` first,
  otherwise `site.admin_email` via `Options.AdminEmail`. Both empty → `422
  mail.test_recipient_required`. Response `data.recipient` echoes the resolved
  address. The admin UI prefills from admin web-options when available.

## SMTP Plugin

Protected built-in plugin ID: `sforum.smtp`, under
`extensions/builtin/plugins/sforum-smtp`.

The plugin exclusively owns SMTP host/port/sender settings, password,
SMTP/STARTTLS/implicit TLS, AUTH PLAIN, MIME message assembly, and transport
error classification. Plugin secrets live in `extension_settings`, are masked
from API responses, are preserved by empty updates, and reach only that plugin
process through `SFORUM_SETTING_*` environment variables.
SettingsLifecycle-backed secret references are resolved through SecretStore for
runtime settings and provider probes; API responses keep `value` empty while
preserving `secretSet=true` so operators can tell that a password/app password
is saved.

The API always owns `mail.deliver` and reuses the same reconciled extension
runtime as provider probes. The retired standalone Worker topology could read
legacy `extension_settings` while probes read SettingsLifecycle, causing a
successful SMTP probe followed by stale-credential AUTH failures. Deployment
updates remove those legacy Worker containers.

`scripts/build-builtin-plugins.sh` builds the local subprocess before API or
worker dev startup. The API Dockerfile builds the Linux executable into the
built-in package.

SMTP is RPC/provider-only: `RouteTarget` is empty (no HTTP proxy base). The
host treats empty/`disabled`/`none` as “no route target” so SSRF loopback
validation does not mark the runtime failed.

The plugin owns all SMTP product copy through the shared buildless Settings
Document and Provider Probe contract:

- Manifest `settings` labels/descriptions/placeholders/groups/options use
  multi-locale maps (`zh-CN` / `en-US`); the host settings API resolves the
  document via `Accept-Language` and `SFExtensionSettingsRenderer` renders it.
- The declared `provider_probe` Settings Action calls the plugin's bounded
  `ProviderProbe` RPC through host permission/lifecycle/input/timeout/audit
  policy. A disabled plugin uses the restricted short-lived Probe runtime and
  does not register normal routes, jobs, hooks, events, or provider slots.
- SMTP ships no executable admin frontend or custom settings SFC. Core chrome
  has no SMTP-specific field or port branching; secrets stay
  encrypted/masked, blank updates preserve them, and recommended restore is
  still host-owned.
- When no explicit selection row exists, the host resolves the enabled built-in
  `sforum.smtp` as the recommended default and persists it during API/worker
  startup. The admin select reflects the resolved selection; reset clears the
  explicit row and falls back to that default.

## Compatibility

Startup runs idempotent migration marker `mail_provider_plugin_v1` after
built-in sync. Legacy `mail.smtp.*` values are copied without overwriting newer
plugin settings. Legacy `mail.provider=smtp` enables/selects `sforum.smtp`; a
legacy site with `dev_log`, `noop`, blank, or unknown values stays unconfigured.
A fresh database keeps no selection row and the startup repair selects the
recommended SMTP default.

## Architecture Debt Checkpoint

M6 focused Bun tests, architecture validation, Nuxt typecheck, production
build, full repository gate, and desktop/mobile browser closeout passed. The
completed program is archived at
`../plans/archive/2026-07/2026-07-28-architecture-boundary-debt-repayment.md`.
