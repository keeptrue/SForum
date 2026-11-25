# Agent Guide

SForum is a maintainable, plugin-first, open-source forum framework. Core is
the host framework; product verticals and provider/vendor behavior belong in
extensions. Read this file before starting work. For subtree work, also read:

- `apps/api/AGENTS.md` for Go, database, extension runtime, and artifact rules.
- `apps/web/AGENTS.md` for Nuxt, Page Registry, admin UI, and browser QA rules.

More specific repository instructions take precedence within their directory.

## Reading Discipline

- If `.codegraph/` exists, use `codegraph explore "<symbol or question>"` before
  `rg`/`find` when locating or understanding code.
- For docs and configuration, use `rg -n` to locate candidates, then
  `sed -n 'start,endp'` to read only the relevant slice.
- Do not run concurrent whole-file `cat` or `nl`, bulk-load the knowledge tree,
  or read sessions/reports/archive files as current context.
- Inspect `git status` before editing. Preserve unrelated user changes.

## Project Intent

Core owns stable host contracts, security boundaries, lifecycle, and generic
forum behavior. Payments, mail delivery, notification channels, analytics,
external integrations, and provider-specific behavior belong in plugins or
out-of-tree services. Core exposes events, provider slots, typed payloads,
permissions, admin selection/reset surfaces, SDK helpers, no-op defaults, and
development adapters so extensions do not need raw host authority.

## Working Principles

- Prefer mature libraries and framework-native solutions before custom
  infrastructure. Check maintenance, license, ecosystem fit, and complexity.
- Keep responsibilities explicit and changes scoped. Do not refactor unrelated
  areas because they are nearby.
- Split long implementations by responsibility. Treat 500 lines as a cohesion
  review prompt and 1000 lines as a hard warning unless documented.
- Use clear names, small inputs/outputs, and comments only for non-obvious
  intent, constraints, or tradeoffs.
- Verify behavior with focused tests, then run the repository gates relevant to
  the changed surface. Never claim a check that was not run.
- Record durable architecture/product decisions in `knowledge/decisions/`.

## Stack And Directory Map

Monorepo with a Go API, Nuxt web app, shared contracts, extensions, knowledge,
tests, docs, and deployment assets.

- `apps/api` - Go 1.26.11 API and embedded worker; module
  `github.com/keeptrue/sforum/apps/api`, toolchain anchored in
  `apps/api/go.mod`.
  - `cmd/` - `api`, `migrate`, `sforum` CLI; standalone worker startup is
    unsupported.
  - `bootstrap/` - runtime assembly only.
  - `app/Http/` - HTTP kernel, controllers, and route registration.
  - `app/Models/` - domain logic and services.
  - `app/Providers/` - provider wiring.
  - `app/Support/` - cross-cutting infrastructure.
  - `database/` - Goose migrations, embedded migrator, sqlc output, and SQL.
  - `config/` - runtime configuration. `config.Load` does not read `.env`.
- `apps/web` - Nuxt 4, Vue 3, Nuxt UI 4, Bun, SSR-first, `zh-CN` default and
  `en-US` secondary. App code lives under `apps/web/app/`.
- `contracts/` - modular OpenAPI. `openapi.yaml` is only the entrypoint index.
- `extensions/` - `builtin/`, `optional/`, `dev/`, and `fixtures/` package
  trees. Only `builtin/` is boot-scanned.
- `knowledge/` - project memory: `index.md`, `modules/`, `decisions/`,
  `plans/`, `sessions/`, `reports/`, `runbooks/`, and `glossary.md`.
- `tests/` and `scripts/` - repository validation and orchestration.
- `docs/` - bilingual handbooks plus stable extension references.
- `deploy/`, Compose files, and deployment scripts - operations.

PostgreSQL, Redis, and Mailpit run through Compose in development.
Meilisearch is optional via the `search` profile and an external plugin.

## Commands

Run from the repository root unless noted. Before `go get`, `go mod tidy`,
`bun install`, or `bun add`, export:

```sh
export https_proxy=http://127.0.0.1:7897 http_proxy=http://127.0.0.1:7897 all_proxy=socks5://127.0.0.1:7897
```

`apps/web` uses Bun only. Do not run `pnpm install` there.

Development:

- `./scripts/dev.sh` - start PostgreSQL, Redis, and Mailpit, then migrate.
  `--build` rebuilds; `--no-migrate` skips migrations.
- `cd apps/web && bun run dev` - Nuxt dev server. The user owns port 3000; do
  not kill it.
- `./scripts/api-dev.sh` - API with `air` and the embedded worker.
- `./scripts/worker-dev.sh` - compatibility error only; standalone workers are
  unsupported.

Build and test:

- `cd apps/web && bun run build`
- `cd apps/web && bun run typecheck`
- `cd apps/web && bun test` - full web suite. Shared SEO, Page Registry,
  public chrome, theme island, and admin settings contract changes require the
  full suite.
- `cd apps/api && go build ./...`
- `./scripts/test-api-isolated.sh` - isolated Go tests. It provisions
  disposable PostgreSQL and Redis, exports `DATABASE_URL`,
  `SFORUM_TEST_DATABASE_URL`, and `SFORUM_TEST_REDIS_ADDR`, and refuses to run
  against the development database sentinel.
- After base migrations, the isolated runner creates
  `sforum_test_template`; override it with
  `SFORUM_TEST_FULL_TEMPLATE_DATABASE`. `internal/testdb` clones this template,
  prepares package-local fixtures, and fails closed if it is missing. It must
  never fall back to `DATABASE_URL`.
- The isolated runner defaults to `min(nproc, 6)` shards. Fixed ports may fall
  back to ephemeral ports without taking another test container's port.
- The isolated runner defaults to no-cache evidence mode: `-count=1` and
  ephemeral host ports. Local daily runs may use `--cache` or
  `SFORUM_TEST_CACHE=1` to reuse the Go test cache and fixed ports.
  `CI=true` and `SFORUM_TEST_NO_CACHE=1` always force no-cache.
- `SFORUM_TEST_REDIS_ADDR` is mandatory for the real lockout TTL test. Keep
  that hard failure; do not restore `t.Skip`.
- `node tests/validate-architecture-boundaries.mjs` - architecture ratchets.
- `node tests/validate-document-csp.mjs` - browser document-CSP gate; requires
  both web and API. Exit `2` means environment not ready. Details are in
  `apps/web/AGENTS.md`.
- `./scripts/test.sh` - full three-line repository gate. Defaults to no-cache
  Go evidence mode; `--cache` opts into the local test cache. Logs land in
  `tests/.logs/`.
- `./scripts/test.sh --fast` - daily loop without production web build,
  compatibility farm, or release/deploy tooling. It enables the Go cache by
  default; use `--no-cache` when the run itself is evidence.
- `ruby scripts/validate-openapi-refs.rb` - validate OpenAPI `$ref`s after
  contract edits.

CLI:

- `cd apps/api && go run ./cmd/sforum` - scaffold plugins/themes and run
  commands such as `seed:forum`. `seed:forum` is append-only, emits no events,
  and reads `DATABASE_URL` from the environment or `--database-url`.

## Cross-Stack Red Lines

- Put product code in its owning domain subdirectory. Root shared directories
  are reserved for cross-domain primitives, compatibility facades, and stable
  entrypoints.
- Architecture baselines are ratchets. Extract before growing a baseline, and
  lower or remove capacity when a legacy file or directory shrinks.
- Keep route shells, domain workflows, persistence, validation, side effects,
  and presentation at clear ownership boundaries.
- Identify actor, action, and protected resource for every route, mutation,
  admin screen, background action, or export. API policy is authoritative;
  frontend guards and hidden controls are UX only.
- Reuse permission keys before adding one. New keys require seed data, catalog
  copy, contract updates when relevant, allowed/denied tests, and knowledge
  updates. Plugins may declare permissions but must never grant them.
- Keep `contracts/openapi.yaml` modular. Update path, schemas, errors, security,
  frontend types, and tests together. Run the OpenAPI reference validator.
- Operator-facing features need safe defaults, clear recovery paths, and
  one-click restoration to recommended defaults where configurable.
- Do not hard-code deployment-specific categories, tags, themes, providers, or
  public-page availability into services. Expose stable settings or contracts.

## Plugin-First Summary

- Core keeps generic framework semantics; vendor logic and optional product
  verticals ship as extensions.
- Development plugin iteration is zero-command. The host watches
  `EXTERNAL_EXTENSION_ROOTS`, builds, digests, stages, grants exactly that
  digest, and activates it without login or a manual trust session.
- Do not add credential steps, restart the dev API for a digest, or hand-roll
  SRI/digest scripts. A real component interface change still requires a manual
  `components[].contractVersion` bump.
- Upload/static install stores an inert package. Executable install requires
  exact-artifact, actor-bound `super_admin` trust before process start,
  migration, frontend import, or external side effects.
- Published artifact directories are immutable. Never edit, add, or delete
  files inside `<extension-id>/<version>/<digest>` to make boot succeed.
  Publish a self-consistent artifact and repoint durable state instead.
- Publication is fail-closed. Never publish an unverified desired set and
  repair it later. Development self-healing is allowed only outside Safe Mode
  and must use the same publish gate.
- Recovery-only failures must expose typed codes and coordinates, with an
  out-of-band CLI recovery path. Keep the Extension Surface Matrix and stable
  view/query/command/event surfaces current.
- Detailed API and artifact rules live in `apps/api/AGENTS.md`.

## Knowledge Workflow

Start with `knowledge/index.md`, the relevant module note, and only the active
plan or hot handoff needed for the task. Read decisions when changing
architecture, trust, permissions, data surfaces, or public contracts. Use
archives only to recover historical evidence.

When finishing:

1. Update the relevant `knowledge/modules/` note.
2. Add or replace one short hot handoff per active workstream.
3. Keep `knowledge/index.md` limited to current workstreams and navigation.
4. Add a decision record for durable choices.
5. Update plan status and `knowledge/plans/README.md` when a task book changes
   state.

Keep `knowledge/index.md` under 150 lines. Do not append changelogs or completed
history to it.
