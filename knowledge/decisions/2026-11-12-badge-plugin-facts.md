# Decision: Plugin badge facts ride `fact.count`, not a plugin condition type

Date: 2026-11-12
Status: accepted (M3 contract frozen in
`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`; manifest +
candidate catalog shipped, Host command + SDK helper land in M3)
Plan: `knowledge/plans/2026-11-12-badge-rule-engine.md` §8
Module note: `knowledge/modules/badges.md`
Reference pages: `docs/extensions/badge-plugin-facts.md` (canonical),
`docs/zh-CN/extensions/badge-plugin-facts.md`

## Context

The rule engine (M1) can already express `all` / `any` / `not` over a fixed
whitelist of 13 conditions. M3 has to let a **plugin** contribute an earning
condition without Core learning that plugin's vocabulary.

The obvious shape — “a plugin declares a condition type” — collides with three
existing invariants:

1. `ruleengine.Catalog` is a closed whitelist; adding a condition type per
   plugin makes the whitelist grow with the installed catalogue.
2. `rule_health` decides “is this reference still alive” from
   `Params[].RefKind`; a per-plugin condition type would need a second
   existence check keyed by condition type.
3. `Conditions` metadata is served to the admin editor as Core text; a plugin
   condition would put plugin-authored strings into Core's condition names.

At the same time the data model already has the right substrate: `badge_facts`
is an append-only delta log and `user_fact_counters` is its aggregate, and M1
already shipped the `fact.count` condition plus a `RefKind: "fact"` objectRef.

This record captures the five decisions that are expensive to reverse and that
a reader of the manifest field would otherwise have to reverse-engineer. The
operational detail lives in the reference page.

## Decision 1 — `badgeConditions[]` registers candidates for `fact.count`

A rule leaf that uses plugin data is **always**
`{"type":"fact.count","params":{"factKey":"…","variant":"…","window":{…}},"cmp":{…}}`.
`badgeConditions[]` declares the legal `factKey` values (and their `variant`
candidates, bilingual labels, units, and allowed comparisons) for that one
existing condition.

Consequences:

- the engine whitelist stays at 13 conditions regardless of how many plugins
  are installed;
- `rule_health` reuses the `RefKind == "fact"` pass it already had, so a
  disabled plugin shows up through the same “invalid reference” machinery as a
  deleted category;
- the condition name an operator sees stays Core text (“Plugin fact count”);
  the plugin's text is a **candidate label** in the dropdown, so the two can
  never overwrite each other;
- the admin condition catalog and the fact candidates are returned as separate
  fields (`items` + `factCandidates`) rather than mixed into one list.

### Alternatives rejected

- **One condition type per plugin** (`type: "acme.onboarding"`). Rejected: the
  whitelist would grow with the catalogue, health checking would need a second
  existence mechanism, and plugin text would enter Core's condition namespace.
  It also buys nothing the fact table does not already provide.
- **Plugin-supplied RPC predicate** (evaluate by calling the plugin). Deferred,
  not forbidden: it needs timeout, isolation and batching design. The fact
  table covers the earning semantics users actually asked for (“did X happen N
  times”) without putting plugin code on the evaluation path.

## Decision 2 — A missing fact is *unknown*, never `false` and never `0`

When a rule references a `factKey` that no enabled plugin currently declares
(disabled, uninstalled, or declaration rejected), resolution returns an
**error**, the engine turns the leaf into `unknown`, and the badge does not
grant (`Matched=false, Incomplete=true`).

Two fail-open paths make any other choice unacceptable:

- `cmp: {op:"lte", value:5}` would treat a swallowed `0` as “satisfied”, so an
  uninstalled plugin's rule would fire for **every** user;
- `not <fact leaf>` would invert a `false` into a **grant**.

Returning `0` is therefore banned at the resolver, and `not unknown` staying
`unknown` is what makes negation safe. This is the same rule the `conditions`
package already states for query failures: return an error, never swallow into
zero.

The corollary is a product promise: rules are operator assets. Disable and
uninstall **never** delete a rule, rewrite it to `not`, or strip a leaf. The
operator sees a health-list entry; reinstalling the same `key` restores
evaluation automatically because candidates are merged from currently enabled
packages, with no backfill job.

## Decision 3 — No `valueKind`; booleans are `gte 1`

Plugin conditions have count semantics only. “Has this ever happened” is
`compare: ["gte"]` on the declaration plus `cmp: {op:"gte", value:1}` in the
rule.

Adding a boolean kind would cost three things for one convenience:

- a second struct↔schema pair to keep in sync in a repository that has **no
  reflective alignment test** between `ManifestBadgeCondition` and
  `manifest-v3.schema.json`;
- a second evaluation branch in the resolver (count vs boolean) whose
  interactions with `not` need their own truth table;
- a definitional trap: with a boolean kind, `not` and `lte 0` stop meaning what
  the operator thinks they mean.

The count-only shape also matches the substrate: `badge_facts` rows are deltas,
so “count of occurrences” is the native read.

## Decision 4 — The command returns `total` clamped at zero

`user_fact_counters.total` is allowed to go negative (a deduction can exceed
what was earned inside a window), and the read paths clamp with
`GREATEST(total, 0)`. The Host command repeats that clamp in its **result
document** and encodes it as a decimal string — `RecordFact` returns the raw
counter, so the clamp is the command layer's obligation, not the store's.

Why clamp in the projection rather than expose the raw counter:

- the counter's sign is an internal accounting detail; a plugin that reasons
  about `-3` will eventually build a rule assumption Core does not honour;
- the public/read surfaces already clamp, so an unclamped command result would
  be the only place reporting a different number for the same fact;
- a negative value cannot be represented by the SDK's decimal-string parser
  (`decimalStringField` accepts digits only, no sign), so shipping an
  unclamped total would produce “command succeeded, result unreadable”.

The fact row itself keeps the sign: only the reported total is clamped.

## Decision 5 — `variant` is an engine open set with domain-level candidate checks

`fact.count`'s `variant` parameter is declared as `ParamEnum` with
`Options: nil`, and the engine now treats `Options == nil` as an **open set**:
it validates the value's shape only and leaves membership to the domain layer.
`Models/Badges` enforces membership at save time against the currently declared
candidates (`conditions.VariantDeclared`), and a violation is a reference error
(`forum.badge_rule_reference_invalid`), not an unknown-condition error.

Why not let the engine carry the domain shape:

- the engine is a pure package that imports nothing from `Models/`; teaching it
  about `FactCandidate`, plugin ids and enable/disable state would invert that
  dependency;
- the candidate set changes at runtime (enable, disable, uninstall), so any
  engine-side copy would need invalidation the engine has no business owning;
- the rule engine must stay usable without a database.

Two details are load-bearing and easy to get wrong:

1. **`nil` and empty slice mean different things.** `nil` = open set (shape
   check only); `[]string{}` = closed empty set (nothing is accepted). Other
   `ParamEnum` parameters keep concrete option lists and are unaffected.
2. **Save-time and evaluate-time fail in opposite directions on purpose.** A
   `factKey` with no current candidates still **saves** (rules are operator
   assets; the plugin may be mid-upgrade), but a `variant` that the declared key
   does not list is **rejected** (a typo is a rule defect, not a plugin state).
   Collapsing the two would either block operators during plugin upgrades or
   let typos through.

### Alternatives rejected

- **Fill the `variant` enum options from candidates at request time.** Rejected:
  `ruleengine.Validate` takes a `Catalog`, so the catalog would have to become
  per-request and stateful, and the same validation also runs on the backfill
  and health paths that have no HTTP request to scope it to.
- **`ParamString` for `variant`.** Rejected: it loses the shape check entirely,
  so a `variant` containing spaces or uppercase would sail into the fact table
  (which has no CHECK on the column) and silently never match a delta.

## Implementation-time decisions (M3.2–M3.4)

Four choices were settled while implementing the frozen contract. They are
recorded here because each one constrains future work more than the contract
text alone suggests.

### 6. `variant` stayed an engine open set — with a domain-side fallback

Decision 5 was implemented as written: `fact.count`'s `variant` remains
`ParamEnum` with `Options == nil`, the engine validates shape only, and
`Models/Badges` checks membership against the currently declared candidates
(`conditions.VariantDeclared`) at save time.

One constraint surfaced during implementation and is now load-bearing:
**`nil` and `[]string{}` must not be conflated.** `nil` means “open set,
candidates supplied by the domain”; `[]string{}` means “closed empty set,
nothing is accepted”. A future refactor that normalises one into the other
silently either opens every enum or closes the variant parameter entirely.

### 7. `userId` is a decimal string on the wire, and never inferred from context

The command input carries the subject explicitly as `userId` (decimal string;
`BIGINT` on the row, `double` in the protobuf `Struct`), and the SDK's
`BadgeFactInput.UserID` is an `int64` that is encoded to a string.

Inferring the subject from the parent request context was rejected: the
supported pattern is an **asynchronous observe handler**, which has no
end-user request to derive an actor from. Inferring would therefore produce
either a wrong subject or a hard failure at the exact place the plugin can do
nothing about it.

### 8. Audit is split by rejection class

Two rejection classes behave differently, on purpose:

- **`capability_denied` writes no audit row.** The capability check runs in the
  shared command gate before the transaction and returns a protocol error, so
  there is no command scope to audit against. Making this path audit would mean
  a second, hand-rolled audit shape for a condition every command shares.
- **Domain rejections write `extension.badge_fact_record.denied`.** These are
  “this exact plugin asked for something it may not do”, which is exactly the
  class operators need to see. The write is **best effort**: an audit failure
  does not change the returned code, because upgrading an audit failure into a
  Host error turns “plugin sent a bad parameter” into “the host is broken”.

### 9. The JSON Schema got its own `if/then` clause

The manifest is validated twice — by `ValidateV3JSONSchema` and by the Go
validator — and the two paths must agree on what a `badgeConditions` package
looks like. The schema therefore carries an explicit conditional (elided: the exact
`type`/`minItems` wrapper on `badgeConditions` in `$defs`):

```json
{ "if":  { "required": ["badgeConditions"], "properties": { "badgeConditions": { "minItems": 1 } } },
  "then": { "required": ["backend"],
            "properties": { "type": { "const": "plugin" },
                            "backend": { "required": ["entry", "protocolVersion"],
                                         "properties": { "protocolVersion": { "const": 2 } } } } } }
```

Without it, a theme or a plugin without a backend would pass schema validation
and fail Go validation (or vice versa), which is the “two validation paths
disagree” failure this surface already calls out for struct↔schema drift.

## Consequences / follow-ups

- The `key` pattern is a three-layer contract (manifest schema, manifest Go
  check, `badge_facts` CHECK). The Go check is deliberately stricter; the
  divergence is documented in the validator rather than “fixed” by loosening it.
- `key` is a stable identifier: renaming it breaks every saved rule. There is no
  migration path by design; add a new key instead.
- The fact candidate cache has a short TTL and keeps the last snapshot on
  refresh failure, so a database hiccup cannot turn every plugin-conditioned
  badge into `unknown` site-wide.
- The Host command needs the same input bounds discipline as the other families
  (`delta` non-zero, decimal string, own-namespace `factKey`). Committed calls
  are audited through the existing `extension.host_command.committed` trail;
  rejected calls follow Decision 8 (domain rejections get their own
  `extension.badge_fact_record.denied` row, capability denials get none).
