# Decision: Badge rules as a boolean AST over a hybrid fact layer

Date: 2026-11-12
Status: accepted (M1 shipped; M3 plugin surface frozen in
`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`)
Plan: `knowledge/plans/2026-11-12-badge-rule-engine.md`
Module note: `knowledge/modules/badges.md`

## Context

Badge grants were `metric + threshold` (`topic_count >= 10`). That shape is a
column pair on `badges`, a CHECK constraint, and a six-value whitelist on
`metric` — three places that must agree before a new earning rule can exist.
Operators kept asking for the shapes the schema forbade: "10 topics **or** 50
comments", "in this category", "linked GitHub **and** avatar set", "not
hidden". Each was a schema change plus an evaluation change.

Two pressures forced a decision rather than one more metric value:

1. **Composition.** The product needs conjunctions, disjunctions, and negation
   over heterogeneous predicates, with save-time validation and an admin editor.
2. **Plugins.** The extension platform needs a way for a plugin to contribute an
   earning condition without core learning that plugin's vocabulary.

The rule engine is the answer to both. This record captures the four decisions
that are expensive to reverse; the module note carries the operational detail.

## Decision 1 — A boolean AST, not more metric columns

`badges.rule` is JSONB holding `{"v":1,"root":<node>}`. Nodes are either groups
(`all` / `any` / `not`) or leaves (`{type, params, cmp?}`). `Support/RuleEngine`
is a pure package: decode, validate (depth ≤ 5, leaves ≤ 32, nodes ≤ 64,
`days` 1..3650, `value` 1..1e9), evaluate. It imports nothing from `Models/`.

`rule` becomes the **only** decision source for automatic badges. `metric` and
`threshold` stay as legacy read-only columns (needed by the down migration) and
get `COMMENT ON COLUMN` saying so.

### Alternatives rejected

- **Add columns per predicate** (`min_topic_count`, `min_comment_count`,
  `require_oauth`, …). Rejected: each new predicate is a migration + a CHECK +
  an evaluator branch, and the combinations still cannot express `any`. The
  schema would keep growing while the product asks for the same thing
  (composition) in a new disguise.
- **Store the predicate as SQL text.** Rejected on authority grounds: badge
  rules are operator input, and an operator-editable string that reaches the
  query planner is a privilege-escalation surface. The AST keeps the SQL in
  code (`conditions/resolve_*`), parameterized by validated leaves.
- **A generic expression DSL with arithmetic / field comparisons.** Rejected:
  every extra capability is a capability the admin editor must render and the
  validator must bound. v1 is deliberately a boolean tree over a closed
  condition catalog.

### Consequences

- Validation is the only gate: anything that passes `ruleengine.Validate` must
  be evaluable, and anything rejected must never reach the DB. A shape that
  decodes but fails validation is `422 forum.badge_rule_*`.
- `Eval` must **not** trust the caller to have validated first — a rule written
  by a manual DB edit or a future import tool reaches `Eval` unvalidated. Hence
  `Eval` treats empty groups and malformed nodes as `unknown`, never as `true`
  (an empty `all` is vacuously true in two-valued logic and would grant the
  badge to every user with zero errors logged).

## Decision 2 — Hybrid fact layer: Core reads truth tables, plugins write `badge_facts`

Core conditions read the authoritative relation tables directly
(`topics`, `comments`, `content_reactions`, `content_bookmarks`, `user_visits`,
`user_activity`, `user_roles`, …). Plugin conditions read a separate
append-only `badge_facts` plus a denormalized `user_fact_counters`.

### Alternatives rejected

- **Plugins write into core tables.** Rejected: `raw_core` is a signed escape
  hatch, not a data-distribution channel
  (`decisions/2026-10-18-extension-data-surface-policy.md`). A plugin counting
  "onboarding tasks completed" has no business owning rows in `user_activity`.
- **Plugins expose an RPC predicate that core calls during evaluation.**
  Rejected for v1: evaluation happens in batches (10k-user backfill); a
  per-user RPC would put plugin latency, timeouts, and failure isolation inside
  the grant loop, and a hung plugin would stall the whole sweep. Facts are
  pushed ahead of time instead, so evaluation stays a pure local read. (The RPC
  "A path" is an explicit residual, not a rejected idea forever.)
- **One unified fact table for core *and* plugins.** Rejected: core counts have
  precise truth tables that must stay recomputable (`Recompute`); folding them
  into an event log would trade exact recomputation for uniformity.

### Consequences

- The two layers have **opposite failure semantics**, and they must stay
  opposite: a missing referenced object (deleted topic/category/tag/role/
  provider/badge) is a **determinate 0/false** ("never did it"); an undeclared
  `factKey` is an **error → unknown** ("cannot judge"). Returning 0 for the
  latter makes `lte` pass and `not` invert into a grant.
- `user_fact_counters.total` may go negative (facts can be deductions); every
  read path clamps with `GREATEST(total, 0)`.

## Decision 3 — `badgeConditions[]` registers `factKey` candidates, it does not add condition types

A plugin's manifest declares `badgeConditions[]`; those declarations populate the
candidate list for **one** core condition, `fact.count` (params: `factKey`,
optional `variant`, `window`). Rules always read
`{"type":"fact.count","params":{"factKey":"<ext>.<name>"}}`.

### Alternatives rejected

- **Each plugin key becomes its own leaf `type`.** Rejected: it makes the
  *shape of a stored rule* depend on which plugins happen to be installed. The
  `ruleengine` whitelist would grow with the plugin count; `rule_health` would
  need a second notion of "does this condition type still exist"; and an
  uninstalled plugin would make existing rules unvalidatable rather than merely
  unsatisfiable. Registration keeps the whitelist at 13 and reuses the existing
  `RefKind` existence check.
- **Let plugins declare arbitrary JSON-schema params.** Rejected: the admin
  editor renders from `ParamSpec`; free-form schemas would mean a second
  renderer and a second validator.

### Consequences — the two faces must not be confused

| Face | Consumer | Contains `fact.count`? |
| --- | --- | --- |
| `conditions.Core()` (the `ruleengine.Catalog`) | save-time validation + evaluation | **always** (13 entries) |
| `PublishableDefinitions(factKeys)` | `GET /admin/badges/conditions` (editor data) | **only when candidates exist** |

Deleting `fact.count` from `Core()` makes every M3-authored rule fail
validation with `unknown_condition`; routing validation through
`PublishableDefinitions` makes every stored rule unsavable the moment a plugin
is disabled. Both are the same error: **letting a display policy leak into the
persistence contract.**

## Decision 4 — The down migration is allowed to refuse

`202611120002` adds `badges.rule` and converts the 10 built-in automatic badges
to single-condition rules. Its Down section first checks whether every automatic
badge still holds a rule that can be inverted back into `metric`/`threshold`
(single `activity.count` + `window.all` + `gte` + a whitelisted metric). If any
rule is more complex, it raises an exception with a HINT instead of guessing.

### Alternatives rejected

- **Best-effort inversion** (take the first leaf, or the strictest one).
  Rejected: it silently rewrites operator-configured earning conditions with no
  error anywhere. A rollback that changes business rules is worse than a
  rollback that stops and asks.
- **Keep `metric`/`threshold` in sync from the write path** so down can always
  restore them. Rejected: two sources of truth for the same rule, and the
  write path would have to define "the metric equivalent of `any(a,b)`" — the
  same inversion problem, moved into every save.
- **Snapshot every rule at migration time and restore blindly.** Rejected as
  the *only* mechanism: it would revert rules operators edited after the
  migration. `badge_rules_rollback` is therefore compared against the current
  rule (same → restore legacy; different but invertible → invert current;
  different and not invertible → refuse).

### Consequences

- `scripts/verify-migration-reversible.sh` must carry a `202611120002` branch
  with three stages: single-condition down/up, X-04b (multi-condition rule →
  down refused, no half-rolled-back state), X-04c (rule edited but invertible →
  down succeeds using the current rule).
- New automatic badges created after the migration have no rollback snapshot;
  they invert from their own rule, and refuse if they cannot.

## Decision 5 — Kleene three-valued evaluation, not fail-to-false

`Eval` distinguishes `true` / `false` / `unknown`. A condition that fails to
resolve yields `unknown`; `not(unknown)` is `unknown`; a root that is `unknown`
becomes `Matched=false, Incomplete=true` with the failures collected in
`Result.Errors`.

### Alternatives rejected

- **Errors collapse to `false`.** Rejected: `not(false) = true`, so a failing
  fact query would *grant* the badge under `not` — the exact opposite of
  fail-closed. This is not hypothetical: it is the shape that a naive
  implementation produces, and P-03/P-12 exist to keep it from coming back.
- **Errors abort the whole evaluation.** Rejected: one bad condition would stop
  a 10k-user backfill, and the operator would see "job failed" instead of "this
  badge has an unhealthy rule" (`rule_health` reports the latter).

### Consequences

- `Incomplete=true` is observable evidence, not noise: it is how "this rule
  depends on something we could not judge" reaches the caller.
- Batch evaluation keeps going: a single user's bad condition never aborts the
  sweep.
