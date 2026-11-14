# Trust Levels — plugin surface

[简体中文](../zh-CN/extensions/trust-levels.md) · English

Trust levels (`TL0..TLn`) are derived from a user's activity plus optional boolean rules.
This page covers **what a plugin can declare today**. It is deliberately narrow: events and
read queries for trust levels are not implemented yet (see *Not here yet*).

## 1. Fact conditions (`userFactConditions`)

A plugin that reports facts through the Host command `badges.fact.record` declares the legal
fact keys in its manifest. The same declaration feeds both badge rules and trust-level rules.

```json
{
  "userFactConditions": [
    {
      "key": "acme.wiki.pages",
      "contractVersion": "acme.wiki.pages@1",
      "label": { "zh-CN": "页面数", "en-US": "Pages" },
      "unit": { "zh-CN": "个", "en-US": "pages" },
      "compare": ["gte", "lte"]
    }
  ]
}
```

- `userFactConditions` is the neutral name and the canonical one.
- `badgeConditions` is a **compatibility alias**: the older name still works, and both arrays
  are validated by the same rules with one shared cap.
- Rules that apply to both arrays: at most **32 declared facts in total**, `key` unique across
  both arrays, `contractVersion` exactly `key@N`, and the extension must have a
  `backend` with `protocolVersion: 2`.
- Declaring a fact does not create data. A fact key with no rows evaluates to `0` **only when
  it is declared**; an undeclared key makes the leaf *unknown*, so the tier it belongs to does
  not match (fail-closed).

Stop a plugin and its keys disappear from the candidate list within the 5-second catalog
refresh; rules that referenced them stay decodable and simply stop matching.

## 2. Grantable permissions (`trustLevelEarnable`)

A plugin may allow one of **its own** permissions to be granted by a trust level:

```json
{
  "permissionDefinitions": [
    {
      "key": "acme.wiki.pages.read",
      "contractVersion": "acme.wiki.pages.read@1",
      "label": { "zh-CN": "阅读页面", "en-US": "Read pages" },
      "description": { "zh-CN": "阅读受限页面", "en-US": "Read restricted pages" },
      "assignmentPolicy": "host",
      "trustLevelEarnable": true
    }
  ]
}
```

- Default is `false`. Only keys declared `true` become selectable in the admin UI.
- The flag lives on the permission definition, so "declared" means "declared by this
  manifest" by construction; there is no second declaration channel.
- Core permission keys always win: a plugin declaration for a core key is ignored rather than
  overriding it.
- Disabling a plugin does **not** invalidate saved configurations. The key becomes `inactive`:
  it is kept in the config, not granted, and shown as inactive in the admin UI.

## 3. States and error codes

| state | meaning | granted? | savable? |
| --- | --- | --- | --- |
| `valid` | core key, or declared by an enabled plugin | yes | yes |
| `inactive` | declared by a disabled/installed-only plugin | no | yes |
| `unknown` | never declared by anything | no | no → `422 earnable_not_declared` |

`earnable_not_declared` carries a `params.variant`:

- `undeclared` — nothing ever declared the key: remove the checkbox;
- `plugin_disabled` — the plugin that declares it is not enabled: enable it (or remove the
  checkbox if the plugin is gone for good).

Decoding never rejects an undeclared key. If it did, one disabled plugin would make the whole
`trust.levels` value undecodable and the site would silently fall back to the default policy.

## 4. Not here yet

- `trust_level.changed` events (M4b).
- HostQuery surfaces for the tier catalog / a user's current level (M4b).
- Any runtime RPC that lets a plugin evaluate rule conditions.
