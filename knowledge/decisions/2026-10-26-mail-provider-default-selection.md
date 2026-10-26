# Decision: Mail Provider Default And Selection Lifetime

## Status

Accepted

## Context

`mail_provider_selection` is the only source of truth for outbound delivery.
Two behaviors made mail silently stop working:

1. A fresh database had no selection row. The admin select visually showed the
   first enabled provider, but the backend reported `configured=false` and
   `mail.deliver` marked every message `skipped/provider_unavailable`.
   `AdoptLegacyMail` also deleted the row on a fresh install because the legacy
   `mail.provider` value was absent.
2. A restart was implemented as disable -> enable. The disable step deleted the
   selection row, and enable never restored it. The recurring trigger is a
   built-in SMTP update: automatic upgrade skips a selected provider
   (`provider_in_use`), the operator restarts it manually, and the selection
   disappears.

The product expectation is that SMTP is the recommended default and that an
explicit choice survives a restart.

## Decision

1. **Recommended default is resolved server-side.** When no selection row
   exists, the registry prefers the enabled built-in `sforum.smtp`, then a
   single enabled `mail.provider` plugin. Multiple third-party providers
   without SMTP remain ambiguous and require an explicit choice. API and worker
   startup persist the default through an idempotent `EnsureDefault` repair;
   read-time resolution is the safety net.
2. **A selection is a preference, not a runtime registration.** Normal disable
   and restart keep the mail provider row. The registry reports it
   `unavailable` while the plugin is disabled and it becomes effective again on
   enable. Uninstall and explicit missing-artifact cleanup clear it.
3. **Delivery distinguishes states.** `unconfigured` is a terminal
   `skipped/provider_unconfigured`; `unavailable` is retried and ends as
   `failed` after the job attempt budget. A restart window no longer turns
   queued mail into a terminal skip.
4. **Reset means recommended default.** Clearing the explicit row falls back to
   the resolved SMTP default; it is not an implicit "stop sending mail" switch.
   Operators who need to pause mail disable the provider plugin or the
   notification channel.
5. **Fresh installs keep no row** so the default repair owns the first
   selection. Legacy sites that explicitly used `dev_log`/`noop`/unknown keep
   their unconfigured migration behavior.

## Consequences

- No schema migration. Existing broken databases are repaired on the next
  API/worker startup when SMTP is enabled.
- `dev_log`/`noop` legacy sites whose SMTP plugin is enabled now default to
  SMTP. This is the intended "works out of the box" change; a strict opt-out
  would need a separate explicit flag.
- A permanently disabled provider causes bounded retries and a `failed`
  delivery instead of an immediate terminal skip. Delivery history exposes the
  reason.
- The built-in auto-update `provider_in_use` guard stays. SMTP updates still
  require a manual restart, but the restart no longer loses the selection.
