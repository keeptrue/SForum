# 2026-10-26 Mail Provider Default Selection Handoff

## Changed

- `MailProviderRegistry` resolves a recommended default (`sforum.smtp`, then a
  single enabled `mail.provider` plugin) and persists it at API/worker startup
  through `EnsureDefault`. `Resolve` returns `ready | unavailable |
  unconfigured`; `Selected` remains a compatibility wrapper.
- `mail.deliver`: `unconfigured` -> `skipped/provider_unconfigured`;
  `unavailable` -> retry with `sending/provider_unavailable`, then `failed`
  after the attempt budget. A restart window no longer produces terminal skips.
- Lifecycle: normal disable/restart keeps the mail provider preference;
  uninstall, missing-artifact cleanup, builtin prune, and CLI recovery clear it.
  Legacy uninstall with retained identity also clears it.
- Fresh installs no longer delete the selection row in `AdoptLegacyMail`; real
  legacy sites keep their old unconfigured behavior.
- Admin API/UI: `selected` now serializes as `{extensionId,label}`, the
  providers response exposes `state`, the selected-but-disabled provider stays
  visible, reset re-reads the resolved default, and the provider/overview tabs
  render ready/unavailable/unconfigured with new i18n copy and the
  `provider_unconfigured` delivery reason.
- Tests: registry default matrix, delivery states, disable/uninstall preference,
  legacy migration, and web contract assertions.
- Docs: `knowledge/modules/mail.md`, new decision record, OpenAPI
  providers/reset schemas.

## Decisions

- A mail selection is a preference, not a runtime registration: disable keeps
  it, uninstall clears it.
- Reset means "restore the recommended default", not "stop sending mail".
- Legacy `dev_log`/`noop` sites whose SMTP plugin is enabled now default to
  SMTP. This is the intended works-out-of-the-box tradeoff.

## Next

- Run `./scripts/test.sh --fast` and the full gate before merge.
- Browser QA `/settings/mail` at desktop and `390x844`: ready/unavailable/
  unconfigured states, reset, and test-button gating.
- Consider relaxing the built-in auto-update `provider_in_use` guard now that a
  restart preserves the selection.

## Open Questions

- Do we need an explicit "pause mail" toggle instead of disabling the provider
  plugin?
