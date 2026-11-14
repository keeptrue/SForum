# Plugin badge facts: `badgeConditions` and `badges.fact.record`

Author and operator reference for the **plugin fact surface** of the badge rule
engine: how a plugin declares a countable fact, how the Host writes it from a
plugin process, and what happens to badge rules when the plugin is disabled or
removed.

Related pages:

- [Authoring guide](./authoring-guide.md) — package layout, digests, trust review
- [Host API v2](./host-api-v2.md) — the Host Command model this page extends
- [Manifest V3 field catalog (generated)](./catalogs/manifest-v3.md)
- [Capability catalog (generated)](./catalogs/capabilities.md) — `badges.fact.write`
- [Core event catalog (generated)](./catalogs/events.md) — names usable in `events[]`

## 1. What this surface is

`fact.count` is a **Core** badge condition. Plugins do not add condition types;
they register the legal values of that condition:

| Concept | Owner | Where it lives |
| --- | --- | --- |
| `fact.count` condition type | Core | `Models/Conditions` registry (one of the 13 whitelisted conditions) |
| Legal `factKey` values + `variant` candidates | Plugin | Manifest V3 `badgeConditions[]` |
| Fact rows + running totals | Core | `badge_facts` (append-only) + `user_fact_counters` (aggregate) |
| Write entry point | Core | Host Command `badges.fact.record` |

Three consequences worth knowing before writing a plugin:

1. The rule engine condition whitelist does **not** grow with the number of
   plugins. A rule always references plugin facts through
   `type: "fact.count"`.
2. Rule health checks reuse the existing `objectRef` machinery: a `factKey`
   carries `RefKind: "fact"`, so an unmatched key is reported by the same
   “invalid reference” pass that handles deleted categories and renamed roles.
3. A plugin never receives table access to badge data. It reports a fact and
   the Host performs the write inside its own transaction.

## 2. `badgeConditions[]`

`badgeConditions` is an optional top-level array. It is only accepted on
`type: "plugin"` packages that declare a Protocol V2 `backend` — a theme has no
caller, and a plugin without a backend has no writer, so either declaration
would only produce a permanently-zero candidate.

```json
"capabilities": ["badges.fact.write"],
"badgeConditions": [
  {
    "key": "acme.onboarding.tasks",
    "contractVersion": "acme.onboarding.tasks@1",
    "label": { "zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks" },
    "unit": { "zh-CN": "个", "en-US": "tasks" },
    "compare": ["gte", "lte"],
    "variants": [
      { "value": "tutorial", "label": { "zh-CN": "教程任务", "en-US": "Tutorial tasks" } },
      { "value": "profile", "label": { "zh-CN": "资料完善任务", "en-US": "Profile tasks" } }
    ]
  }
]
```

### 2.1 Fields

| Field | Required | Type / limit | Notes |
| --- | --- | --- | --- |
| `key` | yes | string, pattern `^[a-z0-9][a-z0-9._-]{1,120}$` | The `factKey`. Must start with `<manifest.id>.` and must be a **stable identifier** — it is written into `badges.rule` JSON, so renaming it breaks every rule that used it. Also participates in the manifest-wide versioned-id uniqueness set, so a fact key can never collide with a route, cache, or query id. |
| `contractVersion` | yes | string, `^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`, max 256 | **Must be exactly `key + "@" + N`.** A drifted pair (`a.b` declared with `a.condition.b@1`) is rejected: the audit trail and the health list would otherwise be unable to tell whether two names denote the same fact. |
| `label` | yes | object, `zh-CN` + `en-US` required, values non-empty | The condition catalog is served to the admin editor, so both locales are mandatory — a missing locale renders the wrong language in that UI. |
| `unit` | no | object, same bilingual rule when present | Rendered next to the number (`个` / `tasks`). |
| `compare` | yes | array, 1..5 entries, unique | Subset of `gte`, `gt`, `lte`, `lt`, `eq`. There is **no `ne`**: express “not reached” by wrapping `gte` in `not`. An empty array is rejected because a condition with no comparison operator can never be used by any rule. |
| `variants` | no | array, max 16 | Each entry is `{value, label}`. `value` uses `^[a-z0-9][a-z0-9._-]{0,63}$` and must be unique; the empty string is reserved for “no variant”. |

Array limits: **at most 32 `badgeConditions` entries** and **at most 16 variants
per entry**. The 32 cap mirrors the rule engine leaf limit (`MaxLeaves = 32`):
a rule can never reference more fact conditions than it can hold leaves, so
anything declared beyond that ceiling is dead weight that only expands review
surface.

### 2.2 Validation rules

| # | Rule | Failure mode |
| --- | --- | --- |
| 1 | Only `type: "plugin"` may declare it | manifest invalid |
| 2 | A Protocol V2 backend must exist | manifest invalid |
| 3 | At most 32 entries | manifest invalid |
| 4 | `compare` non-empty, de-duplicated, ops from the five-op set | manifest invalid |
| 5 | `variants` ≤ 16, values unique and pattern-conforming | manifest invalid |
| 6 | `label` (and `unit`, when present) carries `zh-CN` + `en-US`, both non-empty | manifest invalid |
| — | `contractVersion == key + "@N"` | manifest invalid (drift check) |

**Change the Go struct and the JSON Schema in the same commit.** There is no
reflective “struct fields vs schema properties” test in this repository:

- struct changed, schema not → pure-Go `Validate` still passes, but
  `ValidateV3JSONSchema` rejects the package at load time because the root
  object sets `additionalProperties: false`;
- schema changed, struct not → the declaration is silently dropped by
  `encoding/json`, and the plugin believes it declared a condition that does
  not exist in the catalog.

### 2.3 The `key` pattern is a database contract

The pattern is copied verbatim from the migration that creates the fact table:

```sql
fact_key TEXT NOT NULL CHECK (fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$')
```

Copy it character for character. The pattern contains no backslashes, so the
SQL literal, the Go value and the JSON Schema value are byte-identical:

```json
"pattern": "^[a-z0-9][a-z0-9._-]{1,120}$"
```

`regexp.QuoteMeta`-style escaping is unnecessary here (`.`, `_` and `-` are all
literal inside the character class, and `-` is last). Writing `\\.` “to be
safe” would instead accept a literal backslash and reject valid keys.

Go-side manifest validation does not carry a second copy of this pattern: it
reuses the manifest id pattern through the shared versioned-id check, which is
why the effective Go limit below is stricter than the schema.

Length bounds are deliberately not identical across the three layers:

| Layer | Accepted `key` length |
| --- | --- |
| `badge_facts.fact_key` CHECK | 2–121 |
| `badgeConditions[].key` schema | 2–121 |
| manifest versioned id (Go) | **2–81** |

The Go side reuses the manifest id pattern, so it is the stricter of the three
by design: a 82–121 character key passes schema and the CHECK but is refused by
manifest validation. Keep keys short rather than relying on the outer bound.
Relaxing either side produces the split-brain state this page exists to
prevent: “manifest accepted, insert fails” or “insert works, manifest refused”.

## 3. Referencing a fact from a badge rule

A plugin fact is always a count leaf:

```json
{
  "v": 1,
  "root": {
    "op": "all",
    "children": [
      {
        "type": "fact.count",
        "params": {
          "factKey": "acme.onboarding.tasks",
          "variant": "tutorial",
          "window": { "kind": "recent_days", "days": 30 }
        },
        "cmp": { "op": "gte", "value": 3 }
      },
      {
        "type": "fact.count",
        "params": {
          "factKey": "acme.onboarding.tasks",
          "window": { "kind": "all" }
        },
        "cmp": { "op": "gte", "value": 1 }
      }
    ]
  }
}
```

- **Every count leaf carries `cmp`.** Without it the leaf has no comparison and
  evaluates to unknown, so the badge never fires. Plugin conditions have no
  boolean form: “has happened at least once” is `"cmp": {"op": "gte", "value": 1}`.
- **`variant` is optional.** Omitted means “aggregate over every variant of this
  fact key”.
- **`window` is either `{"kind": "all"}` or `{"kind": "recent_days", "days": N}`**
  with `N` in 1..3650. `kind: "all"` must not carry `days`.

Save-time behaviour:

| Situation | Result |
| --- | --- |
| `factKey` is not declared by any enabled plugin | **Save succeeds.** Rules are operator assets; a manifest may be temporarily disabled or being upgraded. |
| `variant` is not among the variants the key declares | **Save is rejected** with a reference-invalid error (`forum.badge_rule_reference_invalid`). A typo is a rule defect, not a plugin state. |
| `factKey` declares no variants | Any `variant` value is accepted at save time (there is nothing to check against). |
| Unknown condition type / bad compare value | Rejected by the rule engine (`forum.badge_rule_*`). |

## 4. Host Command `badges.fact.record`

| Property | Value |
| --- | --- |
| Command ID | `badges.fact.record` — **the ID carries no version suffix** |
| Command version | `1` |
| Input schema ref | `sforum.badges.fact.record.input@1` |
| Result schema ref | `sforum.badges.fact.record.result@1` |
| Policy ID | `sforum.badges.fact.record@1` |
| Process capability | `badges.fact.write` |
| Actor mode | service (actorless): the caller identity is the verified extension runtime, never a request field |

Note the asymmetry, it is intentional and easy to get wrong: the **command ID
and its version are two separate fields** (`"badges.fact.record"` + `"1"`),
while **schema refs and the policy ID carry `@1` inline**. There is no
`badges.fact.record@1` command id.

### 4.1 Input

```json
{
  "userId": "42",
  "factKey": "acme.onboarding.tasks",
  "variant": "tutorial",
  "delta": "1",
  "occurredAt": "2026-11-12T12:00:00Z",
  "dedupeKey": "task-42:completed"
}
```

| Field | Required | Notes |
| --- | --- | --- |
| `userId` | yes | The user the fact belongs to, as a **decimal string** (the column is `BIGINT` and the payload is a protobuf `Struct`, whose only numeric type is `double` — see §9.1). Must parse as a positive integer. Facts are per-user rows; a fact about “the site” is not representable. |
| `factKey` | yes | Must be one of **this plugin's own** `badgeConditions[].key` values. A key owned by another plugin is rejected. |
| `variant` | no | Empty/absent means the “no variant” bucket. |
| `delta` | yes | **A decimal string, not a JSON number.** The command payload is a protobuf `Struct`, whose numeric type is `double`; integers beyond 2^53 lose precision there. The value must be non-zero; negative deltas are legal and represent a deduction. |
| `occurredAt` | no | RFC 3339 timestamp; defaults to the Host clock. It drives window queries (`recent_days`). |
| `dedupeKey` | no | Idempotency key. When non-empty, a repeated call with the same `(user, factKey, variant, dedupeKey)` writes nothing and returns `inserted: false`. When empty, every call writes a new row. |

### 4.2 Result

| Field | Type | Notes |
| --- | --- | --- |
| `inserted` | boolean | `false` means the dedupe key matched an earlier write. **This is not an error** — do not retry, do not log it as a failure. |
| `total` | decimal string | The running total after the write, **clamped at `0` in the receipt only**. The stored counter (`user_fact_counters.total`) keeps the real value and may be negative — a deduction is applied, not swallowed — but no read surface, including this receipt, reports a negative count. See §9.5. |

The Host writes the fact row and the aggregate counter in **one transaction**.
A failure part-way leaves neither; there is no “row without counter” or
“counter without row” state.

### 4.3 Rejections

Field names above are the command input contract (`sforum.badges.fact.record.input@1`).
Six fields, nothing else is accepted, and no field may be omitted by abbreviating
its name. The SDK wrapper is `Host.RecordBadgeFactRequest` /
`Host.RecordBadgeFact` (input `BadgeFactInput`, where `UserID` is an `int64`);
§5 shows the wrapper and the wire document it builds, which uses these six names
verbatim.

Rejections are normal command results with a frozen reason in
`ErrorDetail.reason`; they are not transport errors. The checks, in order, are:
the runtime identity matches the claimed extension artifact, the capability is
granted, the input matches its schema, the `factKey` belongs to this plugin’s
declarations, and `delta` is a non-zero integer string. The six reason literals,
the top-level error code, and exactly which rejections leave an audit row are
frozen in §9.2 and §9.3.

### 4.4 Capability and operator disclosure

Declare the capability explicitly:

```json
"capabilities": ["badges.fact.write"]
```

An undeclared capability means every call is refused with
`host.command_capability_denied`, so a plugin that “works in dev” but forgot the
declaration fails only after install. The capability is risk **medium** and its
label is what the operator actually reads in the first-enable confirmation
dialog — the dialog renders the label only, never the description. Keep the
label self-explanatory (“Write plugin fact counters”), and put the detail in
your own plugin description.

## 5. Event → fact: the `events[]` + command pattern

Core has no plugin-subscribable event bus that can write facts on your behalf.
The supported pattern is equivalent and explicit: **declare an observer for an
event, and call the Host command from that handler.**

```json
"events": [
  {
    "id": "acme.onboarding.event.topic-created",
    "contractVersion": "acme.onboarding.event.topic-created@1",
    "name": "topic.created",
    "kind": "observe",
    "handler": "acme.onboarding.event.topic-created",
    "inputSchema": "acme.onboarding.topic-created.input@1"
  }
]
```

```go
func (s *server) HandleEvent(ctx context.Context, req *hostv2.EventRequest) (*hostv2.EventResponse, error) {
	payload := req.GetPayload().GetValue().AsMap()
	authorID, _ := payload["authorUserId"].(string)
	topicID, _ := payload["topicId"].(string)
	userID, err := strconv.ParseInt(authorID, 10, 64)
	if err != nil || topicID == "" {
		// A payload we cannot attribute is not a fact: drop it instead of guessing.
		return &hostv2.EventResponse{}, nil
	}

	// Preferred: the SDK wrapper. BadgeFactInput.UserID is an int64 here and
	// becomes the decimal string userId on the wire (§9.1).
	_, err = s.host.RecordBadgeFact(ctx, req.GetContext(), pluginv2.BadgeFactInput{
		UserID:    userID,
		FactKey:   "acme.onboarding.tasks",
		Variant:   "tutorial",
		Delta:     1,
		DedupeKey: "topic:" + topicID,
	})
	return &hostv2.EventResponse{}, err
}
```

`Host.RecordBadgeFactRequest` builds the request when you need to inspect or
batch it; `Host.RecordBadgeFact` is the build-and-execute convenience above.

What the wrapper constructs is exactly the `TypedDocument` form below — the same
six fields, nothing added and nothing inferred from the request context:

```go
document, err := pluginv2.NewTypedDocument("sforum.badges.fact.record.input@1", map[string]any{
	"userId":    authorID,      // decimal string in the wire document
	"factKey":   "acme.onboarding.tasks",
	"variant":   "tutorial",
	"delta":     "1",           // decimal string; int64 in BadgeFactInput
	"dedupeKey": "topic:" + topicID,
})
if err != nil {
	return nil, err
}
requestContext := s.host.RequestContext(req.GetContext())
if requestContext == nil {
	return nil, errors.New("request context unavailable")
}
result, err := s.host.Commands.Execute(ctx, &hostwire.CommandRequest{
	Context:        requestContext,
	// Command ID carries no version suffix; the version is its own field.
	CommandId:      "badges.fact.record",
	CommandVersion: "1",
	Input:          document,
})
if err != nil {
	return nil, err
}
_ = result // inserted/total live in the result document; see §4.2
```

Both forms are valid; prefer the wrapper unless you are building requests
without executing them (queued jobs, tests).

Notes:

- `kind: "observe"` handlers run **asynchronously** with a default 5 s timeout.
  Witness events are delivered at least once, so `dedupeKey` is the idempotency
  authority — derive it from a stable event identity (a topic id, a job id), not
  from a timestamp.
- Keep the handler cheap. If the fact needs derived work (aggregation over
  several rows), enqueue a River job and write the fact there.
- The SDK helper pair is `Host.RecordBadgeFactRequest` (build a request) and
  `Host.RecordBadgeFact` (execute it), mirroring the other command families.
  The plugin SDK has no `Client` type: the entry point is always `*Host`.

## 6. Operator workflow

1. **Install and enable the plugin.** Enabling requires confirming the declared
   capabilities, including `badges.fact.write`.
2. **The condition catalog gains candidates.** The admin condition catalog
   returns the Core condition list plus a separate `factCandidates` array.
   `fact.count` only appears in the condition list once at least one enabled
   plugin declares a key; with no candidates it is omitted deliberately, since
   an empty dropdown is a dead end.
3. **Build the rule.** Pick `fact.count`, choose the `factKey`, optionally a
   `variant` and a window, then set the comparison. Saving shows an estimated
   affected-user count before you commit, and the backfill runs as a background
   job.
4. **Watch the health list.** Any `factKey` referenced by a saved rule but not
   currently declared is listed as a missing fact key.

### 6.1 Disable, uninstall, reinstall

| State | Rules referencing the fact | Evaluation | What operators see |
| --- | --- | --- | --- |
| Plugin enabled | unchanged | Counts read normally | Candidate present |
| Plugin **disabled** | **kept as-is** | Fact leaves resolve to *unknown* → the badge does not fire (`Matched=false, Incomplete=true`). Nothing is granted, nothing is silently revoked. | Health list reports the missing key |
| Plugin **uninstalled** | **kept as-is** | Same as disabled | Health list reports the missing key |
| Plugin reinstalled / re-enabled with the same `key` | **recovered automatically** | Counts read again; no backfill job is needed because candidates are merged from currently enabled packages | Candidate and health entry disappear |

Rules are operator assets. Nothing in this path deletes a rule, rewrites it to
`not`, or strips a leaf — a silent rewrite would be a tampering event with no
audit trace. The only manual intervention is when a plugin **renames** a `key`:
the old key stays invalid forever and the operator must edit the rule, which is
exactly why `key` is documented as a stable identifier.

Candidate lookups are cached for a few seconds, so a freshly enabled plugin can
take up to that long to appear in evaluation. The cache deliberately keeps the
previous snapshot when a refresh fails: a database hiccup must not turn every
plugin-conditioned badge into “unknown” site-wide.

## 7. Pitfalls

- **Unknown is not false.** A missing fact resolves to *unknown*, so it is not
  granted under `not` either. Returning `0` instead would make an `lte 5` rule
  fire for every user of an uninstalled plugin, and `not` would flip a failure
  into a grant.
- **No boolean variant kind.** There is no `valueKind` field. “Has this ever
  happened” is `compare: ["gte"]` plus `cmp: {op: "gte", value: 1}`. A boolean
  form would add a second evaluation branch and a `not` inversion trap.
- **`delta` is a string.** Sending it as a JSON number loses precision in the
  protobuf payload.
- **`inserted: false` is success.** Dedupe is doing its job.
- **Renaming a key is a breaking change.** Add a new key instead, and let the
  old one fall into the health list until operators migrate their rules.

## 8. Reference

- Manifest field catalog (generated): [catalogs/manifest-v3.md](./catalogs/manifest-v3.md)
- Capability catalog (generated): [catalogs/capabilities.md](./catalogs/capabilities.md)
- Host Command model: [host-api-v2.md](./host-api-v2.md)
- Fact store implementation: `apps/api/app/Models/Badges/facts_store.go`
- Condition registry and fact candidates: `apps/api/app/Models/Conditions/`
- Contract decision record (internal): `knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`

## 9. Errata 3 (contract clarifications)

Three corrections to the first publication of this page. They are normative —
the tables below are the frozen literals, and code, tests and operator runbooks
quote them verbatim.

### 9.1 Final input field names

| Field | Wire type | Exact meaning |
| --- | --- | --- |
| `userId` | **decimal string** | Target user. `BIGINT` on the row, transported as a string; `int64` in the SDK's `BadgeFactInput.UserID`. |
| `factKey` | string | One of this plugin's own `badgeConditions[].key` values. |
| `variant` | string | Optional variant bucket; empty means “no variant”. |
| `delta` | **decimal string** | Non-zero integer delta; negative means deduction. |
| `occurredAt` | string | Optional RFC 3339 timestamp; defaults to the Host clock. |
| `dedupeKey` | string | Optional idempotency key; empty disables dedupe (§9.4). |

`userId` and `delta` are strings for the same reason: the command payload is a
protobuf `Struct`, whose numeric type is `double`, and a 64-bit integer loses
precision there above 2^53. Never send either as a JSON number; a plugin that
does will see a rejected or silently wrong write.

### 9.2 Frozen rejection reasons

| `reason` (verbatim) | Meaning |
| --- | --- |
| `capability_denied` | The caller does not hold `badges.fact.write`. |
| `fact_key_not_declared` | `factKey` is empty, or not declared by this exact extension artifact. |
| `invalid_user_id` | `userId` is not a positive decimal integer string. |
| `invalid_delta` | `delta` is not a non-zero decimal integer string. |
| `invalid_occurred_at` | `occurredAt` is present but not RFC 3339. |
| `fact_write_failed` | The write could not be recorded (store failure, unwired recorder, unresolvable declaration set). |

The consumer-facing top-level code for a denied fact write is
**`forum.badge_fact_write_denied`** (HTTP status 403), and the exact cause is
carried as `data.reason` — one of the six literals above. Display the top-level
code's localized message; branch on `reason`.

Note `fact_key_not_declared` covers **empty as well as undeclared**: ownership is
decided against the extension's declared key set, not by string prefix. A prefix
only proves a namespace, not that this extension ever declared that fact.

### 9.3 Audit behaviour differs by rejection class

| Rejection class | Audit row |
| --- | --- |
| `capability_denied` | **None.** The capability check runs in the shared command gate *before* the transaction, so it returns a protocol error without touching `audit_events`. Do not build alerting on an audit row that will never appear. |
| Domain rejections (`fact_key_not_declared`, `invalid_user_id`, `invalid_delta`, `invalid_occurred_at`, `fact_write_failed`) | `extension.badge_fact_record.denied`, with `extensionId`, `factKey`, `reason`, and `userId` (as a string) in the metadata. |

The denial audit is **best effort**: if the audit insert fails, the returned
code does not change. A rejection stays a rejection, and an audit failure is not
upgraded into a Host error — the alternative would turn “plugin sent a bad
parameter” into “the host is broken”.

### 9.4 Dedupe is keyed, not implicit

- A non-empty `dedupeKey` that matches the partial unique index
  `(user_id, fact_key, variant, dedupe_key)` makes the second call write nothing
  and return `inserted: false`. That is **normal output, not an error** — do not
  retry it, do not log it as a failure.
- An **empty** `dedupeKey` disables dedupe entirely: two identical calls insert
  **two** rows, and the total increases twice. The empty string is not an
  idempotency key.

Derive `dedupeKey` from a stable business identity (task id, topic id, order
id). If you cannot name one, you are reporting a genuine repeat occurrence and
should leave it empty.

### 9.5 `total` is clamped in the receipt, not in storage

`user_fact_counters.total` allows negative values (a deduction can exceed what a
user earned inside a window), and the read paths clamp with
`GREATEST(total, 0)`. The command receipt repeats that clamp — `total` is never
negative — while **storage keeps the real number**.

So a plugin that reports `+1` then `-3` sees `total: "0"` in the second receipt,
but the stored counter is `-2`; a later `+1` yields a stored `-1` and still
reports `total: "0"`. Nothing is swallowed: the arithmetic continues from the
stored value, and the receipt is only the clamped projection.
