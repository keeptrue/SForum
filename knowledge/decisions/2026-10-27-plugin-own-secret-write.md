# Decision: let a plugin write its own declared secret settings

## Status

Accepted (2026-10-27)

## Context

`settings.own` is described as "read/write your own settings", but only the read
half existed on the runtime boundary: `sforum.extensions.settings.own` returns
the decrypted document, `SecretService.Resolve` reads one secret, and
`SettingsLifecycle.Put` (which owns the Secret Store write, the
`sforum.secret://` reference and the CAS revision) was reachable only from the
admin console.

A plugin that obtains a credential on its own — the concrete case is the app
store plugin holding a store account token after an OAuth-style approval — had
two bad options: store the bearer in its own database or file (plaintext at
rest, survives backups, visible to anyone with read access), or ask the operator
to copy it into the settings page by hand (impossible when the plugin is the one
that received it, and it puts a long-lived secret in a clipboard).

The Host already had every ingredient: `SecretStore` encrypts, the settings
document stores only a reference plus `secretSet`, the admin page masks the
field, and the runtime reads plaintext back through `RuntimeValues`.

## Decision

Add `Put` and `Clear` to `SecretService` (host API v2):

- `Put(secret_id, value, media_type)` writes one declared secret field through
  `SettingsLifecycle`, returning `secret_set` and the document revision;
  `Clear(secret_id)` removes that field. Both preserve every other field,
  including sibling secrets.
- Gates, in order: broker-attested exact runtime → `settings.own` capability →
  bare `secret_id` (no `sforum.secret://` namespace form) → ≤ 64 KiB →
  `HasSecretField(extID, key)` against the schema registered from the manifest.
- An empty `Put` is refused; removal has exactly one spelling (`Clear`), so a
  truncated body cannot silently delete a working credential.
- Audit comes from the Secret Store (`put` events with namespace, key, actor,
  version) and never carries the value, the lease, or the reference.
- `SettingsLifecycle` gains `HasSecretField` and `PutSecret`; the existing
  `Put` keeps its signature and behavior by delegating to the same internal
  write path, so admin saves and plugin writes cannot drift on CAS or migration.

## Consequences

- A plugin can now provision its own credential into the encrypted store, and
  the operator still sees and can revoke it on the settings page. No new
  storage, no new secret format, no product-specific hook in core.
- The write is scoped by the manifest: an undeclared key, a plain field, or
  another extension's namespace is refused. A manifest that stops declaring the
  field makes the write fail closed (`host.secret_field_not_declared`).
- `settings.own` now means what its description says. A plugin without that
  capability still cannot write, and the resolver is late-bound, so a wiring
  mistake denies writes instead of failing them open.
- Deliberately not opened: arbitrary secret namespaces, rotation without a
  value, lease/expiry control, and cross-extension reads. Each would need its
  own grant and audit story; they stay Tier 3 in the app-store plan.
- The Host still has no actor context on this boundary, so the audit actor is
  `extension:<id>`, not the administrator who triggered the write. Attributing
  an install to a specific admin needs Tier 2 (actor context + instance
  assertions), which is out of scope here.
