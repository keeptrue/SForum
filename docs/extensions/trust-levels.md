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

**Catalog today.** The shared condition catalog holds **13** entries, of which trust levels can
use **11** (`content.pinned` and `content.hidden` serve badges only). Counting assertions in
`apps/api/app/Models/Badges/requirement_text_test.go` and
`apps/api/app/Models/TrustLevels/config_rule_test.go` pin those two numbers.
`identity.profile` — "the profile field is filled in", over `bio` / `signature` / `website` /
`location` — is planned for the shared catalog and would turn that into **14 / 12**. It is
**not here yet**; ownership is recorded in
`knowledge/plans/2026-11-14-trust-level-conditions.md` §3.3.

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

- Default is `false`. A plugin key must be declared `true` before an operator can check it into
  a tier.
- The selectable list has a second source: the core permission whitelist
  (`trustlevels.EarnableWhitelist()`, seven keys). Core keys do not pass through this field.
- The flag lives on the permission definition, so "declared" means "declared by this
  manifest" by construction; there is no second declaration channel.
- Core permission keys always win: a plugin declaration for a core key is ignored rather than
  overriding it.
- Disabling a plugin does **not** invalidate saved configurations. The key becomes `inactive`:
  it is kept in the config, not granted, and shown as inactive in the admin UI.

State at the time of this check: **no** bundled plugin declares `trustLevelEarnable`, so on a
default install the selectable list is exactly those seven core keys. A plugin key reaches the
matrix only after some plugin declares it.

## 3. States and error codes

| state | meaning | granted? | savable? |
| --- | --- | --- | --- |
| `valid` | core key, or declared by an enabled plugin | yes | yes |
| `inactive` | declared by a disabled/installed-only plugin | no | yes |
| `unknown` | never declared by anything | no | no → `422 earnable_not_declared` |

`earnable_not_declared` has exactly **one cause**: nothing currently declares the key (and it
is not a core permission). Fix: remove the checkbox. There is no `plugin_disabled` variant —
a disabled plugin only makes the key `inactive`, which stays savable.

Decoding never rejects an undeclared key: rejecting it would mean one disabled plugin makes the
whole `trust.levels` value undecodable. A decode failure leaves the whole site **unresolved** —
no earned permissions at all (effectively TL0), no tier card on profile pages, and one warn per
resolution attempt on the `identity` side.

## 4. Events and remaining surfaces

Landing plan and ownership: `knowledge/plans/2026-11-14-trust-level-conditions.md`.

Provided (M4b):

- `trust_level.changed` — observe, fail-open. Payload: `userId`, `fromLevel`, `toLevel`,
  `fromTierId`, `toTierId`, `computedAt`. Emitted only when a complete recompute differs from
  the change ledger; an incomplete evaluation emits nothing and does not touch the ledger, and a
  user's first sighting records the ledger without emitting.

Not here yet:

- HostQuery surfaces for the tier catalog / a user's current level (M4b, plan §4.5).
- Any runtime RPC that lets a plugin evaluate rule conditions (plan §6 item 4: evaluation stays
  in the Host).

## 5. Rolling back

`trust.levels` is a single JSON value decoded with `DisallowUnknownFields`: a field the running
binary does not know makes it reject the **whole** value, not just that one tier. So when rolling
back from a rule-capable build to one that predates `rule`, first clear `rule` on **every** tier
and save with the new build, then swap the binary. `color` and `deletedTierIds` follow the same
rule against older builds (`apps/api/app/Models/TrustLevels/config.go` records this). What a
decode failure does to the site is described in §3: everything unresolved, effectively TL0.
