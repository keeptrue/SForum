# Backend Module

## Purpose

Owns the Fiber API, domain rules, persistence, sessions, search indexing, and
background work.

## Current Status

Foundation scaffold exists under `apps/api`.
Jobs and queues architecture has been accepted. River backed by PostgreSQL is
the first durable queue foundation.
Backend HTTP composition now has a Laravel-style but Go-explicit
implementation: `bootstrap` assembles the API runtime, `app/Http` registers an
ordered route-provider list, `app/Http/Controllers/*` owns thin controllers and
route declarations, `app/Providers` owns provider wiring, and `app/Models/*`
owns domain logic.

The 2026-07-28 architecture debt program split API assembly into focused
infrastructure, extension restore/platform, domain, and HTTP stages; split the
large Identity, Forum, and Options files by responsibility; and standardized
Forum construction on `ServiceConfig`. Stable extension contracts now live in
`Support/ExtensionRuntime`, `ExtensionProtocol`, `ExtensionDatabase`, and
`ExtensionComposition`. Models cannot import the legacy concrete runtime;
bootstrap remains the concrete assembly owner. See
`decisions/2026-07-28-extension-stable-package-boundaries.md`.
API startup output now keeps Fiber's useful listen metadata but replaces the
default Fiber ASCII banner with an SForum API banner through Fiber's
`OnPreStartupMessage` hook.
Backend HTTP controllers now have a Go-explicit Laravel-style abort helper in
`app/Http`: `Abort`, `AbortIf`, and `AbortUnless`. These helpers return the
existing `*APIError` type instead of panicking, so Fiber's centralized error
handler continues to emit localized SForum API envelopes with `code`,
`message`, and `data.reason`.
Architecture guidance now treats SForum core as a host framework. Optional
vertical systems such as outbound mail delivery, notification channels,
analytics, and vendor-specific integrations should be built as plugins or
explicit provider-slot implementations by default. When a vertical needs shared
state across plugins, such as payments, core should define the framework model
and provider interfaces while plugins implement provider/vendor behavior.
Admin overview is a core read-only backend module under
`app/Models/AdminOverview`, `app/Http/Controllers/AdminOverview`, and
`app/Providers/admin_overview.go`. `GET /api/v1/admin/overview` requires an
authenticated actor with `admin.access`, combines PostgreSQL aggregate counts
with a Go runtime snapshot, and returns one stable payload for the admin home:
runtime memory/heap/GC/goroutines, pgx pool stats, community counts,
attachments, moderation, extensions, 7-day trends, top categories, and
server-generated safe action summaries. F1.2 adds `runtime.worker` (Redis
heartbeat last_seen / stale) and `runtime.queueLag` (cheap River aggregates).
The protected runtime payload also includes SForum build identity from
`apps/api/version`: the unified SForum version, Git commit, deterministic build
time, Go version, dirty state, and source URL. The same package is the single
authority for `--version` output from the API, worker, migrator, and developer
CLI processes.

Runtime resource accounting lives in `app/Support/ProcessMemory` and is shared
by the admin overview. Linux release containers sample procfs directly, because
Alpine BusyBox `ps` does not implement the process-table flags used on macOS.
The collector reads PID/PPID, command, RSS, optional `smaps_rollup` PSS and
`AnonHugePages`, plus adjacent-frame CPU ticks. It keeps 60-second rolling RSS
and complete-PSS medians for the API, owned backend plugins, each extension,
and totals without host PID access or a Docker socket. Other platforms leave
PSS absent instead of inventing a value. The API-owned
Worker is represented through `WithWorkerRuntime` mode and concurrency rather
than a fictional separate memory line.

The optional `app/Support/RuntimeDiagnostics` server owns loopback-only pprof
listeners. `PPROF_ENABLED` and `WORKER_PPROF_ENABLED` default to false; the API
profile already covers an embedded Worker. The Go runtime's `GOMEMLIMIT` remains
an operator-provided soft heap target, not a replacement for plugin/container
limits. The primary recurring allocation source found during profiling was
streamed away from `io.ReadAll` in extension artifact digest validation.

Process probes (F1.2): `GET /api/v1/health` is cheap liveness; `GET
/api/v1/ready` evaluates dependencies via `app/Support/Health` (PostgreSQL
required; Redis and Meilisearch failures are degraded-ready).

Release automation (2026-07-29) uses reusable GitHub Actions CI plus a
tag-driven GHCR pipeline. `sforum-api`, `sforum-migrate`, and `sforum-web` are
built for `linux/amd64` and `linux/arm64`; commit-addressed
candidates must pass Trivy and an exact-image PostgreSQL/Redis Compose smoke
before version and stable aliases move. `compose.release.yaml` and
`deploy.sh --version vX.Y.Z` consume one immutable application version without
building on the operator host. GitHub does not deploy to operator-owned hosts.
Core, Web, and all shipped Go processes share one `SFORUM_VERSION`; local builds
display `dev-<commit5>` when Git metadata is available (otherwise `dev`), while
release tags replace it together with the exact commit and commit timestamp.
Database migration and high-risk extension compatibility fences resolve the
exact local `dev` sentinel to the existing `1.0.0` development Core baseline;
release builds continue to use their injected semantic version unchanged.
Maintainers trigger this pipeline through `scripts/release.sh`. Its Chinese-
default bilingual interface supports interactive and non-interactive use,
validates a clean synchronized `main`, rejects duplicate or development tags,
runs only Git and tag safety checks by default, and pushes one annotated release
tag. It returns immediately after the push; explicit `--wait` retains synchronous
terminal monitoring. Interactive mode requires an explicit alpha, beta, or stable
selection before the base version; it suggests the next base and prerelease number
from the latest valid remote release tags, while explicit input remains
authoritative. Optional one-line `--notes` or multi-line `--notes-file` highlights
are stored in the annotated tag and prepended to GitHub's generated release notes.
GitHub verifies that the tag commit is reachable from `main`, then
waits for the exact commit's successful `main` push CI instead of rerunning the
same repository gate. The waiter treats GitHub's empty in-progress conclusion
as pending and uses a non-whitespace field separator so Bash cannot collapse
empty API fields and misread the commit SHA. Release builds restore both CI and release cache scopes
before scan, exact-image smoke, and promotion. GitHub Release then publishes
four Linux/macOS amd64/arm64 CLI archives, two Linux backend bundles
whose service binaries and protected built-ins come from the scanned candidate
images, one checksum manifest, and provenance attestations. Backend bundles do
not claim to include the Nuxt Web runtime, PostgreSQL, or Redis; version-matched
Compose images remain the complete production distribution. `--local-checks`
opts into the redundant local gate when the required local services are
available, and `--dry-run` creates no tag.
GitHub Release bodies come from a tested repository script: annotated-tag
highlights (or automatic cleaned commit summaries) are followed by exact Docker
image pulls, Compose install/update commands, asset guidance, versioned
documentation, and a compare link. This avoids GitHub's occasionally empty
generated-note body and gives operators one complete release page.
After promotion, a separate job uses an empty Docker credential directory to
pull all four version tags. GitHub Release creation depends on that anonymous
distribution check, so private or mislinked GHCR packages fail before public
release metadata is created.
See `decisions/2026-07-29-ghcr-multi-platform-release-pipeline.md`.

The clean-host deployment entrypoint now defaults to immutable GHCR images and
managed Compose PostgreSQL/Redis. Its bilingual wizard creates a mode-`0600`
production environment with secure generated secrets and working local
defaults; external database mode remains deliberately unavailable until its
dependency, health, and backup semantics are honest. The deployment state
machine pulls all four target images before database changes, starts and waits
for infrastructure, backs up only an existing installation, stops the old app,
runs the target migrator, starts with `--no-build`, verifies API/Web plus all
five long-running services, and only then persists the successful version. A
portable deployment lock prevents concurrent mutation; configuration, ports,
and Go image identities fail before database work; migration and startup use
`--pull never`; post-stop failures persist an explicit `recovery_required`
record with the attempted/previous versions and backup path.
For a fresh installation, the default `latest` choice is resolved through the
latest stable GitHub Release API before configuration or image pulls. Only the
resulting immutable tag is written to `.env.production` and `.deployrc`;
operators can still pass an exact release tag for repeatable rollouts.
The first-run production wizard asks for a safe admin route prefix and writes
it to `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX`; successful deploy and restart actions
print both loopback reverse-proxy targets plus the public site and admin URLs.
The production migration service receives the same mandatory security secrets
validated by the shared configuration loader; a real Compose render test keeps
that release/runtime contract aligned.
Backup and restore helpers parse only the exact database keys from dotenv and
never execute the configuration as shell input.

Production installation and updates now enter through the Release-owned
`sforum-install.sh` one-line loader. It asks once for the install directory
(default `./sforum`, absolute path shown, current directory when that is already
an installation), resolves an immutable tag, downloads that tag's
`sforum-bootstrap.sh` and `SHA256SUMS`, requires exactly one matching filename
entry, verifies the hash (plus optional `gh attestation verify` and an
`SFORUM_INSTALL_SHA256` pin), promotes the bootstrap, and execs it. The loader
is deliberately the only entry allowed to run from a pipe: it carries no
install logic and never modifies `.env.production`, `.deployrc`, router runtime
state, data, or volumes — it only reads their existence and recorded version to
choose install versus upgrade. `-d DIR` / `SFORUM_INSTALL_DIR` skip the prompt;
`--yes`, `SFORUM_NONINTERACTIVE=1`, and a missing terminal skip it too, and the
knob also forwards `--yes` to the bootstrap. `deploy.sh` and `upgrade.sh` print
the install directory, configuration file, and backup directory on success and
on `status`, and `.deployrc` records `install_dir=` so a moved installation is
reported instead of silently followed. The bootstrap then refreshes and re-executes the
verified bootstrap from that tag, verifies the matching complete deploy bundle,
then hands the concrete version to `deploy.sh` or `upgrade.sh`. Existing
instances retain `.env.production`, `.deployrc`, router runtime state, data, and
volumes; only the explicit Release-managed toolkit is promoted, with the prior
files backed up under `.sforum/tooling-backups/`. Status, logs, backup, restore,
restart, and stop remain local `deploy.sh` operations so recovery does not
depend on GitHub availability. See
`knowledge/decisions/2026-08-18-verified-release-bootstrap.md` and
`knowledge/decisions/2026-10-27-one-line-install-loader.md`.

An optional repository-side prefetch wrapper, `sforum-latest.sh`, resolves the
newest Release before any local script runs, downloads and verifies that
Release's bootstrap, and only then delegates to install or upgrade. It is
repository tooling rather than a Release asset, so the bootstrap does not
refresh it.

Normal production deployment uses one API process with an embedded River
worker. Worker ownership is not configurable; Compose exposes no standalone
Worker service. Queue concurrency and shutdown controls go to the API-owned
consumer, which shares the API database pool, SettingsLifecycle, SecretStore,
and extension runtime. `deploy.sh` and `upgrade.sh` remove legacy standalone
Worker containers by exact Compose labels.

`upgrade.sh` owns guarded blue/green updates after installation. A
stable Caddy edge switches between API/Web slots only after internal readiness;
each slot API owns its Worker, and River coordinates the brief consumer overlap
until the old API stops. The target migrator first performs a
read-only exact Core and River migration check. Pending Core SQL may run while
the active slot serves only when every migration declares
`-- +sforum OnlineSafe`, stays transactional, and defines bounded lock and
statement timeouts; River must already be exact. The migrator image advertises
that check through `io.sforum.migrations.online-safe-check=v1`, so older images
never receive an unknown check argument. The updater backs up, migrates, proves
the exact target schema, and only then starts the candidate. Undeclared,
unbounded, non-transactional, or River changes fail closed to `deploy.sh`.
That maintenance path recognizes the blue/green edge as the managed port owner,
backs up before stopping all slots and the edge, then migrates and starts direct
target services. The first conversion from direct host ports has a short
maintenance window; later compatible HTTP switches are continuous, while
WebSockets may reconnect. `latest` resolves to an immutable tag before
confirmation and state persistence. Candidate and stable Web readiness checks
use `/health`; they do not render or warm the cached homepage through the
internal loopback origin.
See `decisions/2026-08-01-compose-blue-green-updates.md` and
`decisions/2026-08-02-declared-online-core-migrations.md`.

Update channel semantics (2026-08-16): `deploy.sh` and `upgrade.sh` default to
the **stable** channel; `latest` resolves through the GitHub `/releases/latest`
API (stable releases only). Prereleases are never selected implicitly: pass
`--channel prerelease` (resolves the newest published Release, including
prereleases, via `releases?per_page=1`) or an explicit immutable tag such as
`v3.0.0-alpha.13`. Every choice resolves to a concrete `vX.Y.Z` tag before
image pulls; production Compose never runs a floating `latest` image tag. When
no stable Release exists, `latest` fails closed with a channel hint.
`deploy.sh` distinguishes **current** from **target** versions: deploy/install/
update resolve the target (explicit `--version` or channel-resolved `latest`,
never the stored `.deployrc`/`.env.production` version), while maintenance
actions (status/logs/restart/stop/backup/restore/rollback) default to the
current deployed version and never contact GitHub just to show status; the
interactive menu resolves per menu item. The requested CLI/environment version
is stored separately from the resolved working version, so a maintenance action
cannot leak its current version into a later interactive deploy selection
(review fix 2026-08-16, third pass).

Release deploy assets (2026-08-16): each GitHub Release also publishes a
fixed-name `sforum-deploy.tar.gz` (Compose files, `deploy.sh`, `upgrade.sh`,
`.env.production.example`, `deploy/scripts/*`, Caddy examples, VERSION) plus
standalone `upgrade.sh` and `sforum-install.sh` assets, built by
`scripts/ci/build-deploy-asset.sh` and verified by
`scripts/ci/finalize-release-assets.sh` into `SHA256SUMS` before attestation
(the expected matrix is 10 assets, so `SHA256SUMS` carries 10 entries).
`https://github.com/keeptrue/SForum/releases/latest/download/
sforum-deploy.tar.gz` always points at the latest stable bundle; docs and the
root README use that rolling entry instead of hard-coded version numbers.
Documented download flow (review fix 2026-08-16): download the archive first,
download `SHA256SUMS`, select exactly one filename field for
`sforum-deploy.tar.gz`, verify it, optionally run `gh attestation verify`, and
extract only after verification passes. Runnable examples use a `set -eu`
subshell, so any required download/checksum/provenance failure prevents
extraction or execution; they never rely on suffix-only `grep` or
`--ignore-missing`. The
updater refresh entry uses `releases/latest/download/upgrade.sh` (never
floating `main` raw content) with the same exact-entry verification; explicit
versions use `releases/download/<TAG>/upgrade.sh`, and the historical
`v3.0.0-alpha.13` compatibility download has an immutable SHA-256 pin.

Compose dependency governance (2026-08-16): root Compose tracks explicit
PostgreSQL, Redis, Meilisearch, Caddy, and Mailpit release tags; no Compose
dependency uses a floating `latest` tag. Dependabot's root `docker-compose`
entry (rather than the Kubernetes-oriented `docker` YAML fetcher) and the
go.mod entries for Core, protected built-ins, `tools/proto`, and `tests/compat`
provide reviewed update PRs; contract-only fixture modules remain explicitly
excluded. API/Web Dockerfiles remain under their own `docker` ecosystem entries.

Documentation contract governance (2026-08-16): `tests/validate-docs.mjs`
extracts every explicit HTTP method/path pair from the rolling bilingual docs,
root README, and extension READMEs. Each pair must exist both in the modular
OpenAPI entrypoint/path item and in the generated Go Core Route Catalog; the
failure-path suite independently proves OpenAPI drift and Go registration
drift are rejected.

Deployment documentation contract (2026-10-19): the production handbook is one
hub plus four task pages per locale (`deployment/one-click.md`,
`deployment/manual.md`, `deployment/environment.md`,
`deployment/troubleshooting.md`). `validateDeploymentGuides()` requires every
page and hub link in both locales, every `.env.production.example` key to appear
in both environment pages, the documented `TRUST_PROXY*` and
`SFORUM_SAFE_MODE` variables to be passed through `compose.yaml`, the
install/update/manual/troubleshooting command markers to survive edits, and the
old “four GHCR images”/“five long-running services” counts to stay absent.
`tests/validate-docs_test.sh` carries a failure case for each rule.

Client IP trust model (2026-10-27): `app/Support/ClientIP` separates
`isTrustedPeer` (explicit proxies, private ranges, loopback, link-local; TCP peer
only) from `isProxyAddr` (explicit proxies plus loopback/link-local, never
RFC1918; `X-Forwarded-For` entries), so trusting a private network no longer
strips an intranet client out of the chain. `Config.TrustProxy` is a master
switch and the zero value fails closed: `NewResolver(Config{})` reads no
forwarded header, and the package-level default resolver sets
`TrustProxy: true` explicitly. Startup logs one effective-policy line. The
write-rate-limiter key goes through `clientip.FromCtx` instead of Fiber's
`c.IP()`, whose left-most fallback disagrees with the resolver and can be forged.
`compose.yaml`, `deploy/scripts/configure-production.sh`, and
`.env.production.example` ship `TRUST_PROXY=true`,
`TRUST_PROXY_PRIVATE=true`, `TRUST_PROXY_LOOPBACK=false`, and an empty
`TRUSTED_PROXIES`, because the API peer in Compose is the `web` container; the
Nuxt layer resolves the single client IP and overwrites the outbound headers
first. `config` keeps its fail-closed built-in defaults for hand-written
bare-metal env files. See `decisions/2026-07-12-client-real-ip.md` (2026-10-27
revision) and `plans/2026-10-27-client-real-ip-production.md`.

The admin release checker caches both successful and failed upstream results
for five minutes per API process. Forced checks still bypass that cache.

The CI quality job provisions PostgreSQL 17 on the same host port `15432` used
by the repository's required database-backed tests, runs the embedded migrator
before the repository gate, and passes a separate
`SFORUM_COMPAT_DATABASE_URL` only to the compatibility farm. It deliberately
does not export the broad `DATABASE_URL` or `SFORUM_TEST_DATABASE_URL` during
`go test ./...`, so opt-in destructive integration suites remain opt-in.
The Web package runs Nuxt's standard `nuxt prepare` postinstall lifecycle so a
fresh CI install creates `.nuxt/tsconfig.json` before Bun loads tests that use
Nuxt's `~` and `@` aliases. Typechecking keeps its separate
`.nuxt-typecheck` build directory.

Code scanning remediation (2026-08-20) bounds Protocol V2 storage chunk input
to the existing 1 MiB limit before narrowing it to `int`, and removes an
overflow-prone `len + 1` admin-component allocation hint. The contextual route
catalog test also tracks the component-specific admin asset route while keeping
its inherited guard fail-closed because component permissions are target-owned.

Performance hardening (2026-07-08) covers the network and connection layers
beyond the earlier search/cache read-path work:

- **Fiber config**: `server.go` now sets `ReadTimeout`/`WriteTimeout`/
  `IdleTimeout`/`BodyLimit` and registers `compress` (brotli/gzip) and `limiter`
  middleware. Limiter skips GET/HEAD/OPTIONS and rate-limits writes per IP via
  Redis storage. All defaults are env-tunable (`HTTP_READ_TIMEOUT`,
  `COMPRESS_LEVEL`, `LIMITER_WRITE_MAX`, etc.).
- **Redis**: `humanverify.NewRedisClient` accepts `RedisClientOptions`
  (PoolSize/MinIdleConns/Dial/Read/Write/ConnMaxIdleTime/ConnMaxLifetime).
  `bootstrap/app.go` merges the humanverify and forum-cache clients into one
  `sharedRedisClient`; session storage stays separate. Close-chain leak fixed.
- **PostgreSQL**: `postgres.NewPoolWithOptions` + `PoolOptions` expose
  MinConns/MaxConnIdleTime/MaxConnLifetime/ConnectTimeout for both API and
  worker pools. API and worker min/max connection environment values are parsed
  directly into the positive `int32` range; invalid or overflowing values use
  the environment-specific defaults instead of wrapping negative.
- **Meilisearch**: `search.NewClientWithTimeout` injects `http.Client.Timeout`
  (default 5s); `go mod tidy` promoted `meilisearch-go` to a direct dependency.
- Decision record: `decisions/2026-07-08-performance-hardening.md`.

## Planned Stack

- Go 1.26.10+ (the toolchain anchor is `apps/api/go.mod`; docs and CI must
  follow it).
- Go Fiber v3.
- PostgreSQL with `pgx/v5`.
- `sqlc` for query generation.
- `goose` for migrations.
- Redis through `redis/go-redis/v9`.
- ALTCHA human verification through the official Go library, wrapped behind a
  small provider interface.
- Meilisearch through `meilisearch-go`.
- `go-playground/validator/v10` for validation.
- `log/slog` for structured logging.
- Backend locale configuration for `zh-CN` default and `en-US` support.
- River for durable PostgreSQL-backed jobs and worker queues.

## Planned Boundaries

- `identity`: users, credentials, sessions, profiles, registration, password
  reset, email verification, roles, permissions, human-verification enforcement,
  and policy helpers.
- `forum`: categories, topics, posts, revisions, visibility, slugs.
- `moderation`: reports, staff actions, audit trail, soft deletion.
- `search`: Meilisearch settings, indexing jobs, rebuilds, search endpoints.
- `jobs`: River-backed durable queue framework, dispatcher, worker runtime,
  retry behavior, and shared job conventions.
- `localization`: locale negotiation, supported locale config, server-owned
  localized templates, and translation key conventions.
- `humanverify`: shared provider boundary for ALTCHA challenge generation,
  server-side verification, stable result codes, and later provider swaps.
- `options`: runtime site-facing settings stored in `web_options`, with typed
  service validation, short-lived backend cache, public read endpoints, and
  permission-protected admin updates.
- `attachments`: uploaded file metadata, storage provider settings, upload
  validation, provider adapters, admin governance, attachment references, and
  orphan cleanup.
- `database`: core admin-only database table inspection for the current
  PostgreSQL database. It is read-only in v1, uses catalog metadata to validate
  table/column identifiers, excludes system schemas, masks sensitive values,
  and requires `database.manage`.
- `notifications`: Core owns recipient/fanout policy, registry, preferences,
  inbox/revisions, safe presentation, generic delivery attempts, queue names,
  subscription ownership, and provider selection. External transport remains
  plugin-owned; the protected Web Push reference proves the boundary.
- `payments`: core framework boundary when product scope requires it. Core
  should define provider-neutral payment intents, transactions, refunds,
  webhook-delivery/idempotency records, entitlement checks, events, provider
  slots, policy helpers, and admin provider selection/reset contracts. Gateway
  integrations, provider-specific transaction mapping, checkout/session flows,
  invoice rendering, webhook payload verification, and vendor settings belong
  in plugins.

## HTTP Bootstrap And Routing

Borrow Laravel's organization where it helps humans navigate the backend:
small entrypoints, a clear bootstrap layer, service providers, route files,
middleware groups, and thin controllers. Keep the implementation Go-native and
explicit; do not introduce a dynamic dependency container before the codebase
needs one.

Target ownership:

- `cmd/api/main.go` starts and stops the process only.
- `bootstrap` wires config, logging, PostgreSQL, Redis, sessions, providers,
  route providers, jobs, and cleanup hooks.
- `config` owns environment parsing and typed settings.
- `app/Http` owns Fiber app construction, global middleware, `/api/v1`,
  health/system routes, JSON error shape, and route-provider interfaces.
- `app/Http/Controllers/*` declares routes and keeps request DTOs, response
  DTOs, and thin controller methods.
- `app/Providers` builds each module from shared dependencies.
- `app/Models/*` owns domain types, services, policies, repository interfaces,
  and persistence adapters.
- `app/Support/*` wraps external systems and reusable infrastructure clients.
- `database/*` owns migrations, handwritten SQL, generated `sqlc` code, and
  the shared Goose migrator.
- API and worker processes run embedded Goose migrations at startup when
  `MIGRATE_ON_STARTUP=true`. The shared migrator uses Goose's PostgreSQL table
  lock, so parallel process starts serialize safely.
- Development Compose and production deploys may still run the same migration
  binary explicitly as a visible pre-start check; startup migration should then
  be an idempotent no-op.
- `scripts/dev.sh` runs the development `migrate` service with `run --rm`.
  Therefore `api` and `worker` must not declare that one-shot container as a
  long-lived Compose dependency: Docker Desktop resumes projects with
  `compose start`, which cannot resolve a container intentionally removed after
  success. Their durable startup dependencies remain healthy PostgreSQL and
  Redis, while API/worker startup migrations provide the process-level guard.

Route registration rules:

- Use an explicit ordered provider list assembled in bootstrap.
- Prefer a small `http.RouteProvider` interface over one dependency field per
  module.
- A module becomes reachable only when its provider is added to bootstrap.
- Do not register routes from `cmd/*`, platform clients, database stores,
  service constructors, package `init` functions, or filesystem scanning.
- Put middleware at the narrowest useful level: global, API group, or route
  group.
- For every new non-public core-owned route, mutation, admin operation, export, or
  background action trigger, decide and implement the required authorization
  boundary in the API. Frontend guards may mirror the same permission for
  usability, but backend policy checks remain authoritative for core-owned
  handlers. V3 trusted replacement handlers/custom guards own the explicitly
  declared policy contract and require trust disclosure, audit, and tests.

## API Contract

`contracts/openapi.yaml` is the stable OpenAPI entrypoint for documentation and
future generated clients. The handwritten contract source is split by module:

- `contracts/openapi/paths/` owns route operations.
- `contracts/openapi/schemas/` owns reusable request/response/domain schemas.
- `contracts/openapi/components/` owns shared parameters and reusable error
  responses.

Keep route files aligned with `app/Http/Controllers/*` ownership. When an API
endpoint changes, update its module path file, schemas, shared responses or
parameters when needed, permission/security documentation, and frontend
consumers/tests that depend on the shape. Run
`ruby scripts/validate-openapi-refs.rb` after editing contract files.

## Jobs And Queues

- Use River with PostgreSQL as the primary durable queue.
- Do not use Redis as the first durable job store.
- Enqueue jobs transactionally with domain writes when the job represents a
  side effect of that write.
- Keep job payloads small and ID-based.
- Application jobs live under `app/Jobs/*`.
- Shared queue runtime and dispatch helpers live under `app/Support/Jobs`.
- Initial queue names are `critical`, `default`, `search`, `mail`,
  `notifications`, and `maintenance`.

## Open Questions

- Final deployment target and runtime process model.
- Whether backend email or notification contracts need full English translation
  in MVP before any mail/notification plugin ships.
- Exact username, email, password, and email-verification rules for open
  registration.

## Next Steps

- Add PostgreSQL/Redis/Meilisearch connectivity after the health-check
  foundation.
- Add supported-locale config and a user locale preference field during identity
  schema design.
- Use one user system with open registration, first-user `super_admin`
  bootstrapping, default `member` assignment, and admin-managed custom
  roles/user groups.
- Use ALTCHA by default for human verification, backed by Redis rate limits and
  single-use challenge tracking.
- Keep the modular OpenAPI contract synchronized with route files and future
  generated frontend clients.
- Keep admin database management read-only unless a separate write-operation
  design covers audit events, confirmations, and permission boundaries.
- Add River and `app/Support/Jobs` after the jobs design is reviewed.
- Implement the accepted API response envelope: every JSON API response uses
  integer `code`, backend-localized `message`, and `data`; `code` equals the
  HTTP status code, and stable machine-readable reason keys live in
  `data.reason`.
