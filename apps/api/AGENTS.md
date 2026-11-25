# SForum API Agent Guide

Read the repository root `AGENTS.md` first. This file owns Go package
boundaries, persistence, extension runtime, artifact integrity, and isolated
test rules.

## Package And Layer Boundaries

- A Go subdirectory is a package boundary, not a visual folder. Before creating
  one, define its public responsibility, dependency direction, transaction
  ownership, and test boundary. Avoid re-export packages and import cycles.
- HTTP controllers own decoding/encoding and transport concerns. Domain
  services own authorization-aware workflows. Stores own persistence.
  `bootstrap` owns assembly only. Do not mix SQL, request parsing, policy,
  process control, and response formatting in one type or function.
- Do not add responsibilities to legacy `app/Support/Extensions`
  (`extensionsruntime`) or `app/Models/Extensions` flat packages. Extract a
  focused collaborator first. Their `Manager`/`Service` receiver counts are
  non-growth baselines.
- A `Service`, `Manager`, `Controller`, or `Store` spanning independent
  capabilities must become a compatibility facade over focused collaborators.
  Do not keep adding receiver methods or `NewServiceWith...` permutations.
- Split a large file inside its current package before changing package
  boundaries. Extract a new package only after callers depend on a stable
  minimal interface.
- Prefer external black-box tests (`package foo_test`) for public behavior.
  Same-package tests are for private invariants only. Permission or trust
  boundaries need allowed and denied behavior tests.

## Isolated Tests

- Use `./scripts/test-api-isolated.sh` for Go tests. It provisions disposable
  PostgreSQL and Redis and exports `DATABASE_URL`, `SFORUM_TEST_DATABASE_URL`,
  and `SFORUM_TEST_REDIS_ADDR`.
- Database tests resolve their connection only through `internal/testdb`,
  which reads `SFORUM_TEST_DATABASE_URL` and refuses the development database
  identified by `public.sforum_dev_guard`. Never run database packages with a
  plain `go test` in a shell that exports the development `DATABASE_URL`.
- After base migrations, the runner creates `sforum_test_template`; override
  it with `SFORUM_TEST_FULL_TEMPLATE_DATABASE`. Template helpers clone this
  database, prepare package-local fixtures, and fail closed when the template
  is absent. They must never fall back to `DATABASE_URL`.
- The runner defaults to `min(nproc, 6)` shards. A fixed-port collision may
  fall back to an ephemeral port, but must never take another test container's
  port.
- Default mode is no-cache evidence: `-count=1` and ephemeral host ports.
  Local daily runs may use `--cache` or `SFORUM_TEST_CACHE=1` to reuse Go's test
  cache and fixed ports. `CI=true` and `SFORUM_TEST_NO_CACHE=1` always force
  no-cache.
- `TestLoginLockoutTTLAgainstRedis` is a hard gate on
  `SFORUM_TEST_REDIS_ADDR`. Bare `go test ./app/Support/Auth/` fails by design.
  Keep that failure; never restore `t.Skip`.
- Use `cd apps/api && go build ./...` for build validation. Run
  `node tests/validate-architecture-boundaries.mjs` after adding, moving, or
  materially growing production files.

## Core Versus Extension

- Core owns stable framework contracts, lifecycle, security, and generic forum
  behavior. Provider/vendor logic belongs in an extension package, including
  protected built-in plugins.
- Core should expose explicit events, provider slots, typed payloads,
  permission checks, admin selection/reset UI, SDK helpers, scaffolding,
  tests, no-op defaults, and development adapters.
- A new core product module needs a written reason why a plugin, provider slot,
  or event is insufficient. Record the choice in `knowledge/decisions/`.
- Payments and similar shared semantics may have core lifecycle types,
  idempotency, entitlement checks, provider interfaces, and admin contracts.
  Real provider behavior still belongs in extensions.

## Development Plugin Loop

- Plugin development is zero-command. The host watches
  `EXTERNAL_EXTENSION_ROOTS` in development, then builds frontend/backend,
  computes digest and SRI, stages, grants that exact digest, and enables or
  upgrades without login, password, dev trust session, or manual confirmation.
- A credential-less `dev-auto` system account provides audit attribution.
  Disabling or deleting it disables the channel.
- Editing plugin content needs no version bump. A real component interface
  change (props/schema shape, `l2Component`, added/removed component ID)
  requires a manual `components[].contractVersion` bump. Do not automate it.
- Do not add auth steps to plugin iteration, restart the dev API to pick up a
  digest, or hand-roll SRI/digest scripts.
- The dev watcher and dev-auto grants exist only in `APP_ENV=development`.
  Safe Mode blocks them. Never weaken production gates.

## Installation And Trust

- Upload/static install validates, previews, and stores an inert package. It
  must not execute package code.
- Executable install hooks wait for first enable and require exact-artifact,
  actor-bound `super_admin` confirmation before process start, migration,
  frontend import, or external side effects.
- Executable plugins may compose or replace declared routes, guards, services,
  and behavior only through versioned registries after trust confirmation.
  Raw request/session authority and raw core database access are separately
  declared high-risk powers.
- Install/enable/upgrade routes stay behind delegated management permissions:
  `extensions.plugin.manage` or `extensions.manage`; archive install also
  accepts `extensions.theme.manage`.
- `super_admin` gates executable trust and frontend-import surfaces. Any unmet
  condition fails closed to `ErrTrustChallengeRequired` or the manual challenge
  path.
- Accepted development tradeoff: any process able to write a dev source root
  effectively acts as the operator. See
  `knowledge/decisions/2026-10-17-dev-zero-command.md`.

## Immutable Artifacts

- Artifact directories are immutable facts:
  `<EXTENSION_ROOT>/<extension-id>/<version>/<digest>/`.
- The directory digest must equal the package tree digest. The manifest's
  `backend.digest` must equal the SHA-256 of `backend.entry`.
- Never add, replace, or delete a file inside a published artifact directory to
  make boot succeed. Build, digest, validate, and test from the source package,
  then publish a self-consistent artifact and repoint durable state.
- Use:

  ```sh
  sforum extension verify-artifacts [--database-url URL] [--root DIR] \
    [--publication-revision N] [--json]
  ```

  It checks every member of the latest publication or requested revision,
  reports `checked N artifact(s)` and one coordinate-rich `DEFECT` line per
  defect, exits `0` all loadable / `1` defects / `2` usage error, and never
  starts SForum, loads plugin code, or writes state.

## Publication And Recovery

- Publication is fail-closed. Write a publication revision and member rows
  only after every member passes directory, tree digest, entry presence, and
  backend digest checks.
- A failed verification leaves the previous revision authoritative. Never
  publish an unverified desired set and repair it later; publications are
  append-only.
- All producers must use the shared publish gate. Empty genesis sets are valid.
- Automatic repair is allowed only in development outside Safe Mode. It must
  search read-only for a self-consistent artifact and republish through the
  same gate. It must never write into an artifact directory or edit an existing
  `extension_versions` row.
- Production and Safe Mode never self-heal. Use:

  ```sh
  sforum recovery republish-artifact <extension-id> --reason TEXT \
    [--confirm-token TOKEN] [--root DIR] [--database-url URL] [--json]
  ```

  Without `--confirm-token`, it is a dry run and exits `1`; usage errors exit
  `2`; already loadable or successfully repaired exits `0`. It repoints one
  member to another self-consistent digest of the same version and appends a
  revision. It never starts SForum, loads plugin code, or hot-recovers.
- Every durable-state to artifact reference shape needs an out-of-band recovery
  path in the same change. "Restart until it works" is not recovery.

## Failure Diagnosis

- Every failure that can push the API into recovery-only must return a typed
  code and a diagnostic message carrying coordinates.
- Artifact failures include `extensionId`, `version`, `digest`, relative
  `path`, and publication revision. Use `plugin_artifact_incomplete`.
- `plugin_runtime_convergence_failed` is only for genuinely unclassified
  causes; its generic "inspect API logs" text means "not yet typed".
- A new stage/apply failure ships with its health-code constant, message
  mapping, and mapping test in the same change. Do not leave it in the default
  recovery branch.
- Reproduce extension boot failures against a cloned database on a separate
  port and capture the raw error first. Record commands and relevant values
  from `plugin_runtime_publications`,
  `plugin_runtime_publication_members`, and
  `plugin_runtime_nodes.last_applied_revision`.
- Do not write `.env`, business tables, or extension directories while
  diagnosing. Do not kill an operator-owned dev process.

## Extension Surfaces

- Trusted Route Registry contributions may claim declared public, admin, or API
  paths and HTTP methods. Theme overrides are presentation-only and must not
  alter versioned business data contracts.
- Every core module maintains an Extension Surface Matrix across routes, hooks,
  queries, admin/public components, identity/permissions, media,
  navigation/regions, cache invalidation, jobs, and lifecycle. A closed surface
  needs a documented security, integrity, or ownership reason.
- A new core data domain ships with a stable read-only view under
  `sforum_core_v1` and at least one stable HostQuery with allowed/denied tests,
  or a documented reason the read surface is absent.
- Plugin data grows only through view, query, command, or event per
  `knowledge/decisions/2026-10-18-extension-data-surface-policy.md`. `raw_core`
  is a signed escape hatch, not a distribution channel.
- Prefer dedicated Admin Surface, Query, Identity/Permission, Media Pipeline,
  and Navigation/Region contracts over replacing whole routes or using raw
  database access.
- Safe mode, pre-plugin boot health, and out-of-band CLI recovery are
  host-owned and non-overridable. Registry conflicts, selected providers, trust
  grants, and replacement handlers must remain inspectable and auditable.
