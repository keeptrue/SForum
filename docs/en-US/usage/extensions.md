# Extensions & themes (operators)

[← Usage](./README.md)

## Plugins vs themes

| Type | Role | Typical flow |
| --- | --- | --- |
| **Plugin** | Capabilities: mail, storage, search, policies, admin entries | Install → trust (if needed) → enable → configure |
| **Theme** | Presentation via Page Registry (L0/L1, optional L2) | Install → activate (no Nuxt rebuild) |

## Security model

1. Uploading a ZIP is **inert** validation and storage—code does not run yet.  
2. First executable enable requires super-admin **exact-artifact** trust.  
3. High-risk powers (route replace, raw DB, …) must be declared and disclosed.  
4. **Safe Mode** and recovery CLI are host-owned and non-overridable.  

## Built-in GitHub login

The protected built-in `sforum.auth-github` adapts GitHub OAuth only. Built-in
discovery stages the artifact; it does **not** auto-trust, enable, or publicly
activate login/registration/link. Operator setup and troubleshooting:
[GitHub login methods](./github-login.md).

## Settings UI levels

| Level | Mechanism | Operator frontend build? |
| --- | --- | --- |
| Ordinary fields | Host Schema renderer | No |
| Probes / actions | Schema + Settings Actions | No |
| Complex admin UI | Author-prebuilt, digest-bound load | No |

## Package locations

| Path | Meaning |
| --- | --- |
| `extensions/builtin/` | Protected built-ins synced at boot |
| `extensions/optional/` | Optional packages shipped in-repo |
| `extensions/dev/` | Local experiments (not boot-listed by default) |
| `EXTENSION_ROOT` | Uploaded runtime packages |
| `EXTERNAL_EXTENSION_ROOTS` | Comma-separated source collections containing `plugins/` and/or `themes/` |

API startup validates external source packages and copies immutable snapshots
into `EXTENSION_ROOT`. First discovery remains installed and inert;
changes become staged candidates. Scanning never enables code, inherits trust,
selects a provider, or uninstalls a package whose source disappeared. Docker
deployments must use container paths and mount each source collection read-only
into the API container.

## Update states and actions

Extension lists explain “what is here, and what still needs one click” with state badges:

| Badge | Meaning | What to click |
| --- | --- | --- |
| Upgrade available v{version} | A higher version is already downloaded locally; it just has not been switched to yet | “Upgrade now” |
| Restart to apply | The version is unchanged; the built-in package content changed (same version, different digest) | “Restart” |
| Activate to apply | The theme's same-version content changed; it has not been re-applied yet | “Activate” |

One rule decides everything: **a higher version number is an upgrade; the same version with
changed content is a restart or an activation.** Plugins restart, themes activate; neither
rebuilds the site frontend.

“Upgrade all (N)” counts only extensions that really move to a higher version. Same-version
content updates are not restarted in bulk; the toolbar reports “N built-in updates waiting for
restart” instead (N counts only built-in plugins maintained with the release; built-in themes
are not included), and you decide when.

### Store update hints

With the store plugin installed, a row may also show the store's “可更新至 vX” badge: the store
has a newer release — **the store catalog holds a higher version than what is installed or
staged locally**. Core does not parse that version number and downloads nothing for you, so the
hint is a secondary link: it opens the store's product page, and only after you download there
does the row show “Upgrade available vX”, where “Upgrade now” performs the switch.

No hint appears when the store is not configured or temporarily unreachable; a missing hint
does not mean the extension has no update.

### Only with updates

The “Only with updates” toolbar filter is off by default. Turn it on to keep only rows with any
update hint (upgrade, restart to apply, activate to apply, store hint) — handy when dozens of
extensions need triage.

### Glossary

| Term | Meaning on this page |
| --- | --- |
| Update | Anything newer than what is running; no promise that the version number changes |
| Upgrade | Switching to a higher version number; only when the number really goes up |
| Restart to apply | Same version, changed built-in content; applied after a restart |
| Activate to apply | Same theme version, changed content; applied after re-activation |

## Built-in plugin auto-update

After SForum is upgraded or freshly installed, the API startup pass promotes
**project-maintained protected built-in plugins** to the version shipped in the
current release. Operators no longer click “Upgrade now” for each one. Scope is
limited to `extensions/builtin/plugins/**` with `source=builtin`; uploaded
packages, optional packages, fixtures, and themes are excluded.

- Only enabled plugins with a staged newer version are considered. Manually
  disabled plugins stay disabled.
- Forward-only: a staged version older than the active one is ignored. A
  same-version artifact with changed content is not restarted by this pass: the
  row shows “Restart to apply”, and an operator restarts when convenient.
- A built-in currently selected as a mail, storage, search, or notification
  provider is skipped so operator selections are not reset. The row keeps
  showing “Restart to apply”; an operator can restart it manually.
- Operators can turn off “Auto-update built-in plugins” under
  Extensions → Plugins. The toolbar's manual “Upgrade all (N)” flow remains for
  version upgrades.
- Failures never block startup. The event log records
  `builtin_auto_update_failed`; the old version keeps serving or the plugin
  stays in the safe disabled state for an explicit retry/rollback.
- To review before allowing the first pass, boot in Safe Mode, turn the option
  off in the admin, then restart normally.

Built-in themes are out of scope. The protected default theme keeps its
first-install/startup-repair rule, and a new theme digest still requires one
explicit admin activation; a same-version theme content change shows
“Activate to apply”.

## Theme activation

Activation switches Page Registry bindings and L0 skin—**not** a full site rebuild.  
The active theme also supplies L1 presentation for Host-selected system error
pages (403, 404, 429, and 5xx). The Host still owns status, cache, SEO, retry
behavior, and the emergency fallback; plugins and public L2 widgets cannot
replace those `system.*` pages.
Details: [Runtime themes](../../extensions/runtime-themes.md).

## Developer docs

- [Developer CLI](../development/cli.md) (`make:plugin`, digest, package, seed)  
- [Authoring guide](../../extensions/authoring-guide.md)  
- [Scenario map](../../extensions/scenario-map.md)  
- [Host API v2](../../extensions/host-api-v2.md)  
- [V3 platform](../../extensions/v3/README.md)  
