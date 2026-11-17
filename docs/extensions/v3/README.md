# Trusted Plugin And Theme Platform V3

> Operator/developer handbooks: [中文](../../zh-CN/README.md) ·
> [English](../../en-US/README.md) · [Docs hub](../../README.md)

V3 is the active extension-platform direction. The accepted product boundary is
the ADR at `knowledge/decisions/2026-07-13-trusted-plugin-theme-platform-v3.md`;
the implementation order and exit gates are in
`knowledge/plans/2026-07-13-trusted-plugin-theme-platform-v3.md`.

The P0 files in this directory freeze the migration baseline. They describe the
current implementation and the accepted target; their presence does not imply
that a later runtime phase is implemented.

## Reviewed Contracts

- `governance.md` - namespace, versioning, migration flags, recovery, raw DB,
  and custom-guard policy.
- `performance-baseline.md` - reproducible current-runtime benchmarks and live
  SSR/process samples.
- `performance-p6-route-registry.md` - same-run v1/V3 route comparison,
  composed-chain evidence, allocation gates, and remaining regression risks.
- `performance-p8-theme-compiler.md` - small/large production compiler and
  render samples, memory gates, and the required P13 rerun boundary.
- `extension-surface-matrix.md` - current/open/planned/closed status for every
  core module across all eleven required surface families.
- `docs/extensions/v3/catalogs/traceability.md`
  - the authoritative 99-row target mapping.

## Generated Catalogs

`catalogs/` inventories every current API registration, Nuxt page/component,
event, contribution point, provider slot, core schedule, job kind, cache,
content/data surface, and WordPress-equivalent admin surface marker.

`catalogs/public-page-surfaces.{json,md}` is the runtime surface of the public
page layer: every Page Registry page an extension can target, with its path,
access, contract, replaceable flag, required features, the body `views` a
`theme.json` page may declare (`template` / `component` / `schema`), the `layout`
values (`site` / `bare`), the regions it exposes, and the Host islands themes may
mount. It is generated from the same single handwritten sources the runtime uses
(`Pages.coreCatalog`, `RegionCatalog.pageRegionMatrix`, the `PageView` vocabulary
and `allowedHostIslands`), so a new page, view, layout, region or island cannot
drift away from what authors can actually target.

`catalog-identities.json` is the reviewed stable identity map. It is not an
ordinary generated output: source moves must update its route/source locator
while retaining the published ID and contract unless a reviewed versioned
replacement/deprecation is intended. The generator rejects missing and stale
mappings. UI rows also freeze their `kind`, explicit public/admin `owners`, and
`state`. When a UI surface is removed, change its row to `retired` rather than
deleting it, and append the exact ID/contract pair to the separately reviewed
`catalog-retired-identities.json` tombstone ledger. Retired rows leave the
active generated catalogs while the append-only ledger makes accidental row
deletion or ID/contract reuse fail generation through the LTS/deprecation
window. The append-only check scans reachable Git history, so catalog generation
and checks require a full (non-shallow) clone. The
`catalog-retired-identities.json` path is itself immutable; do not rename or
relocate the ledger. Manifest V3 component targets bind both the stable
`targetId` and `targetContractVersion`; Core targets must exactly match the
active Host catalog.

Regenerate after changing a cataloged source:

```bash
node scripts/v3-catalog/generate.mjs
```

Check drift without writing:

```bash
node tests/validate-v3-p0-catalogs.mjs
```

Stable IDs in these catalogs are migration identities. Renaming a source file,
handler, route path, or Vue component does not authorize silently changing its
stable ID or contract version. Update the reviewed mapping deliberately and
record compatibility impact.

## Developer-Only Route Surfaces

Six admin-prefixed routes have **no admin entry in `apps/web`**. They exist for
the developer CLI and external tooling, and are listed here so the generated
route catalog is not read as "every `/admin/...` route has a backend screen":

| Route | Consumer |
|---|---|
| `POST /admin/extensions/dev-trust-sessions` | `sforum extension dev-trust create` |
| `GET /admin/extensions/dev-trust-sessions` | `sforum extension dev-trust list` |
| `DELETE /admin/extensions/dev-trust-sessions/{id}` | `sforum extension dev-trust revoke` |
| `POST /admin/extensions/{id}/dev-trust-refresh` | `sforum extension dev --auto-trust` |
| `GET /admin/extensions/openapi/aggregate` | external generated clients / SDK packaging |
| `GET /admin/extensions/openapi/generated-client` | external generated clients / SDK packaging |

The dev-trust routes are development-only: the service is not injected outside
`APP_ENV=development`, so they do not exist in production. Removing them would
break the CLI (`sforum extension dev-trust *` would 404), which is why they keep
their contract entries. Every one of the six carries the same note in its
OpenAPI path item, and `docs/zh-CN/development/cli.md` /
`docs/en-US/development/cli.md` state the same thing for operators.

`POST /admin/extensions/{id}/settings/reset` was **retired** in #217: `apps/web`'s
"restore defaults" submits recommended values through `PUT /settings`, and a
server-side reset that clears stored values is a second, conflicting semantic
with no callers.

## Phase State

- P0–P12 phase checklists are complete, but this is not a stable-production
  completion claim. Cross-phase RuntimeRollout, Marketplace/Privacy consumer,
  CompatFarm, and commerce Dispatcher residuals remain in the active
  production-rewire remediation plan.
- P13 protocol migration is complete: packages require Manifest V3 and
  executable backends require Protocol V2. The remaining LTS residual is the
  independent request-time theme loader. Fail-closed `SFPageOutlet` is never
  fully removed by design.
- Catalog inventory gate: **253** routes / **145** UI surfaces / **99**
  traceability rows (`tests/validate-v3-p0-catalogs.mjs`).
- Progress authority: `knowledge/plans/2026-07-13-trusted-plugin-theme-platform-v3-progress.md`
  and `knowledge/plans/2026-07-13-trusted-plugin-theme-platform-v3.md`.
- Release scope: keep distributions prerelease until production remediation
  M3/M5/M6/M7 and the joined M8 gate close.
- Large deferred (not LTS, not thin wiring): Protocol-leased content filter
  dispatch, Media Plan/Execute/Receipt product authority, EntityStore I/O.
- Published compatibility surfaces other than the removed extension protocol
  remain governed by their own APILTS removal gates.
