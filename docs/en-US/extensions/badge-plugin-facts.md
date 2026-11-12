# Plugin badge facts

> **Language:** the canonical English technical reference lives under the
> path-stable extension surface:
> [Plugin badge facts (canonical)](../../extensions/badge-plugin-facts.md).
> This page exists so `docs/zh-CN` and `docs/en-US` stay structurally parallel.
>
> 中文版：[插件徽章事实（中文）](../../zh-CN/extensions/badge-plugin-facts.md)

Author and operator reference for the **plugin fact surface** of the badge rule
engine: Manifest V3 `badgeConditions[]`, the Host Command `badges.fact.record`,
the `events[]` + SDK `RecordBadgeFact` pattern, and what happens to saved badge
rules when a plugin is disabled, uninstalled, or reinstalled.

Contents (canonical page):

- What the surface is (and is not): `badgeConditions` registers `factKey` /
  `variant` candidates for the Core condition `fact.count`; it does not add a
  condition type, so the engine whitelist never grows with plugins
- `badgeConditions[]` field reference — `key`, `contractVersion`, `label`,
  `unit`, `compare`, `variants`; the 32-entry and 16-variant caps
- The `key` pattern as a database contract (identical to the `badge_facts`
  CHECK), and the three-layer length bounds
- How a saved rule references a fact (always `type: "fact.count"`, `cmp`
  required, `variant` optional, window shapes)
- Host Command `badges.fact.record`: ID without a version suffix, separate
  version `1`, `@1` only inside schema refs and the policy ID; `delta` as a
  decimal string; `inserted: false` is not an error; `total` clamped at zero;
  `dedupeKey` idempotency; the `badges.fact.write` capability
- Event → fact as the supported equivalent of the C path (no core subscribe bus)
- Operator workflow, including disable / uninstall / reinstall semantics and the
  health list
- Pitfalls: unknown is not false, no boolean variant kind, key renames break
  rules
