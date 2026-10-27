# Developer CLI (`sforum`)

[← Development guide](./README.md)

`sforum` is SForum's developer console (in the spirit of Laravel Artisan),
implemented in `apps/api/cmd/sforum`. It covers scaffolding, extension
validation, exact digests, packaging, contract tests, out-of-band recovery,
account/data maintenance, and fake-data seeding.

Run commands from `apps/api`:

```sh
cd apps/api
go run ./cmd/sforum --help
go run ./cmd/sforum --version
```

## Command map

| Command | Purpose |
| --- | --- |
| `version` | Print build information (version, commit, build time) |
| `make:plugin` | Scaffold a plugin |
| `make:theme` | Scaffold a theme |
| `seed:forum` | Bulk-write fake forum data (small profile) |
| `seed:perf` | Million-scale read-path seed (alias of `seed:forum --profile=perf-1m`) |
| `users:reset-password` | Reset a user's password (interactive by default; `--email/--password/--yes` for automation; alias `user:reset-password`) |
| `privacy:erase-user` | Erase a user's privacy data (production entry of `privacy.Registry.EraseUser`; `--actor-user-id` must be super_admin / `user.manage`; `--user-id`/`--user-email` pick one; automation requires `--yes`) |
| `revisions backfill` | Backfill the forum content revision ledger in batches |
| `extension build` | Build author frontend assets, refresh digests, and run all package gates |
| `extension validate` | Validate an extension package (includes/template preflight) |
| `extension digest` | Inspect or refresh Manifest V3 `packageFiles` digests |
| `extension test` | Host contract checks (capabilities, events, entrypoints) |
| `extension package` | Build a zip + SBOM stub |
| `extension brand check` | Verify brand assets (sizes and byte budgets of brand/icon.svg, brand/icon.png, brand/social-preview.png) |
| `extension docs generate` | Generate host docs from the Go catalogs |
| `extension list` | Out-of-band extension recovery state (no plugin code) |
| `extension disable` / `disable-all` | Out-of-band disable of third-party extensions |
| `extension quarantine` | Out-of-band quarantine of an exact built-in/system artifact |
| `extension recovery lifecycle-close` | Out-of-band close of one open lifecycle operation to the cancelled terminal state (audited) |
| `extension recovery activation-state` | Restore an interrupted activation publication to its exact source artifact (durable evidence only; no-op when conditions drift) |
| `extension recovery recheck` | Rebuild the desired plugin runtime full set, compare it with the latest publication, and publish a `reason=recovery` revision on an exact match (no hot recovery) |
| `recovery` | Top-level out-of-band recovery entry sharing the same subcommands as `extension recovery` |
| `extension gc` | Version garbage collection: dry-run candidate list by default plus one-time-token execution (disk-first, audited) |
| `extension command list/run` | List / run trusted plugin commands |
| `extension api-lts` | Print Host/Frontend API LTS and shim telemetry |
| `extension system-tier list/upsert/disable` | Out-of-band system tier management (no package code) |
| `extension dev` | Development edit loop: watch package source inputs and rebuild (requires `APP_ENV=development`) |
| `extension builtins` | Discover the built-in package list from the manifest tree (single source for the build script) |
| `dev:cleanup-orphan-plugins` | Stop reparented extension backend plugin processes (safe for live `sforum-api` children) |
| `ai:eval` | Run one real AI reply generation over fixed fixtures and emit diffable baseline snapshots (`--diff` / `--only` / `--confirm`; never publishes a comment) |
| `ai:reply` | Run one real reply generation against a live topic and print every deciding fact (`--topic-id`, `--trigger-comment-id`, `--confirm`; result cache disabled, publishes nothing, writes no outcome rows) |
| `ai:derived` | Manually trigger AI-derived content (`run --topic-id --purpose digest\|tour`, artifact lands as a draft in `ai_derived_artifacts`; `list` to inspect) |

> This table is the authoritative command list; adding or removing commands must
> update this table and `docs/zh-CN/development/cli.md`
> (`tests/validate-docs.mjs` verifies coverage).

---

## Build identity: `version`

```sh
go run ./cmd/sforum version
go run ./cmd/sforum --version
```

Prints the unified build identity: SForum version, Git commit, and build time
(the same build arguments injected into the release images).

---

## Scaffolding: `make:plugin` / `make:theme`

Interactive (asks for ID, name, backend stub, etc.):

```sh
go run ./cmd/sforum make:plugin
go run ./cmd/sforum make:theme
```

Non-interactive examples:

```sh
# Local experiment (default → extensions/dev/, gitignored, not in admin list)
go run ./cmd/sforum make:plugin \
  --id acme.demo \
  --name "Acme Demo" \
  --description "…" \
  --backend \
  --no-interaction

# Protected built-in package (→ extensions/builtin/, scanned by SyncBuiltins)
go run ./cmd/sforum make:plugin \
  --id sforum.foo \
  --name "Foo" \
  --description "…" \
  --backend \
  --builtin \
  --no-interaction

# Custom output directory
go run ./cmd/sforum make:plugin \
  --id acme.demo --name "Acme Demo" --description "…" \
  --backend --no-interaction --out /tmp/acme.demo
```

### Common flags

| Flag | Applies to | Notes |
| --- | --- | --- |
| `--id` | both | Stable extension ID, e.g. `acme.demo` |
| `--name` / `--description` | both | Display name and summary |
| `--url` / `--author-*` | both | Website and author info |
| `--out` | both | Output directory; omitted follows dev/builtin rules |
| `--builtin` | both | Write under `extensions/builtin/` instead of `dev/` |
| `--no-interaction` | both | Disable interactive prompts |
| `--backend` | plugin | Generate a `backend/plugin` stub and README |
| `--complex` | plugin | Multi-file manifest (includes + langs + settings shards) |
| `--prebuilt-settings` | both | Author-prebuilt Admin settings component + Schema fallback |
| `--vue-admin-page` | plugin | Vue/Vite admin page workspace using the Plugin UI SDK |
| `--provider-slot` | plugin | Declare a provider slot + `provider_probe` (requires `--backend`) |
| `--public-page` | both | Add a public Page Registry page (path `/<slug>`, L1 template, exact declarations). The slug is lowercase alphanumeric plus `-` |
| `--public-page-layout` | both | Page layout: `site` (default; the theme owns the site shell) or `bare` (the page owns its chrome) |
| `--schema-page` | both | Add a declarative public page (path `/<slug>`, `view: schema`, a bounded in-package descriptor plus a JSON Schema contract). Same slug rules as `--public-page` |
| `--schema-page-kind` | both | Descriptor kind: `list` (default) | `detail` | `form` | `notice` (requires `--schema-page`) |
| `--route-middleware` | plugin | Add a before request patch and an after response patch route (requires `--backend`); the guard is always `core.guard.inherit` |

`--public-page` writes the `theme.json` page mapping and adds the template to both
`templates[]` and `packageFiles[]` (three-way identity, equal digests). The page it
generates is **static**: plugin page data sources (`dataSource`/`dataRoute`) have no
usable transport today, so the Loader Gateway would fall back and the scaffold does not
generate one. `--schema-page` generates a **declarative** public page: `theme.json` only names the
descriptor file id and the exact schema reference it must satisfy
(`<schemaFileId>@1`), and both are `kind: schema` with a `version` in the manifest. The
Host renders the body - there is no Vue, no Go template, and no build step in the
package. The Host enforces bounded rules: `kind` must be one of
`list`/`detail`/`form`/`notice`, field types are limited to
`text`/`textarea`/`number`/`boolean`/`select`, and the caps are 24 fields, 64 items,
64 options and 64 cells. `form` renders read-only today: submission needs the page's
declared `dataRoute` binding, which is still blocked by the loader-transport gap (the
same blocker as `dataSource`). Sharing a slug with `--public-page` is rejected outright
so one path cannot carry two declarations.

`--route-middleware` writes only the mutable fields it declares, never
requests raw request authority, and never restates `access`/`permission` - a patch
inherits the target route's authorization.

### After scaffolding

1. Implement the backend and compile the executable to the Manifest's
   `backend.entry` (usually `backend/plugin`).
2. When `frontend/admin/package.json` exists, run `extension build`; it builds the frontend, refreshes digests, and runs every package gate.
3. Without an author frontend, run `extension digest --write`, `extension validate`, and `extension test` directly.
4. Run `extension package` when distributing.

Third-party plugins use the public SDK (`apps/api/sdk/plugin`) and must **not**
import host business packages such as `app/Models/*`. Full authoring rules:
[plugin authoring guide](../../extensions/authoring-guide.md).

---

## Package helpers: `extension …`

### One-command author build — `build`

```sh
go run ./cmd/sforum extension build <package-root>
go run ./cmd/sforum extension build --allow-scaffold <package-root>
go run ./cmd/sforum extension build --skip-install <package-root>
go run ./cmd/sforum extension build --sdk-dir apps/web/packages <package-root>
```

When `frontend/admin/package.json` exists, the command runs `bun install` and
`bun run build`. A `bun.lock` or `bun.lockb` automatically enables
`--frozen-lockfile`. It then refreshes exact digests, performs full Manifest
and template validation, and runs Host contract tests. Packages without an
author frontend skip Bun but still run all three package gates.

Preflight hashes only what would reach a release artifact: `node_modules`,
`.git`, and `vendor` are skipped exactly as `extension package` already skips
them, so an author tree with installed dependencies still validates. The install
path keeps the strict whole-tree digest and still rejects every symlink.

- `--skip-install` skips `bun install` when dependencies already exist; it does not skip the build.
- `--allow-scaffold` permits only a missing backend binary during contract tests; other errors still fail.
- `--sdk-dir <dir>` (or `SFORUM_SDK_DIR`): **development only.** Rewrites the
  workspace's `@sforum/*` dependencies to the local SDK packages in that
  directory before building, so a new SDK version (for example the
  `@sforum/plugin-ui` icon components) can be exercised before Release publishes
  it. `frontend/admin/package.json` and the Bun lockfile are restored byte for
  byte afterwards; if the build fails or is killed, a
  `package.json.sforum-sdk-backup` file is left for manual recovery. Release and
  production flows never set it.

This is an author-invoked local command and executes the plugin's own
`package.json` scripts. Upload, install, enable, and production runtime never
call it. Production continues to load only exact-manifest `.mjs` / `.css`
artifacts.

### Validate — `validate`

```sh
go run ./cmd/sforum extension validate <package-root>
go run ./cmd/sforum extension validate <package-root> --json   # merged Manifest
```

Loads the package, resolves `includes`, validates Manifest V3, and preflights
page templates for explicit V3 packages.

Page Registry themes follow the fail-closed three-way identity rule: every
`theme.json.pages[].template` path must match exactly one Manifest V3
`templates[]` declaration and one `kind: "template"` `packageFiles[]` entry;
paths and SHA-256 digests must agree. `theme.json` only maps pages and cannot
replace exact-artifact declarations. Missing or stale digests fail validation
and activation with `extension.build_failed`.

### Exact digests — `digest`

Manifest V3 binds executables, frontends, and migrations with `packageFiles`
SHA-256 digests. **Refresh after touching package files**:

```sh
go run ./cmd/sforum extension digest <package-root>           # check only
go run ./cmd/sforum extension digest --write <package-root>   # write back + validate
```

When adding theme templates, declare their identity, contract, ViewModel, and
`packageFiles[]` entries first, then run `digest --write`. The command refreshes
`packageFiles[]` and declared inline template digests but never infers template
identity or file membership from `theme.json`.

### Contract tests — `test`

```sh
go run ./cmd/sforum extension test <package-root>
go run ./cmd/sforum extension test --allow-scaffold <package-root>  # scaffold stage may lack the backend binary
go run ./cmd/sforum extension test --skip-backend-binary <package-root>
go run ./cmd/sforum extension test --json <package-root>
```

Checks capabilities, events, contribution points, providers, jobs, and backend
entrypoints against the host catalog.
`--allow-scaffold` is an alias of `--skip-backend-binary`.

After modifying a built-in theme, run from `apps/api`:

```sh
go run ./cmd/sforum extension digest --write ../../extensions/builtin/themes/<dir>
go run ./cmd/sforum extension validate ../../extensions/builtin/themes/<dir>
go run ./cmd/sforum extension test ../../extensions/builtin/themes/<dir>
```

Then run `./scripts/build-builtin-plugins.sh` at the repo root, restart the API
so `SyncBuiltins` stages the new digests, and activate through the admin UI.
Never edit `storage/builtin-dev/` or `storage/extensions/**` directly.

### Package — `package`

Builds a zip of the extension root plus an SBOM stub.

```sh
# Default: nearly all root files go into the zip
go run ./cmd/sforum extension package <package-root>

# Release package: skip common source/dev files
go run ./cmd/sforum extension package <package-root> --exclude-source

# Custom output path
go run ./cmd/sforum extension package <package-root> \
  --exclude-source \
  -o /tmp/acme.demo.sforum.zip
```

| Behavior | Notes |
| --- | --- |
| Included by default | Nearly all files under the package root |
| Always skipped | `.git/`, `node_modules/`, `vendor/`, existing `*.sforum.zip` |
| `--exclude-source` additionally skips | `*.go`, `go.mod`/`go.sum`, `*.vue`/`*.ts`/`*.tsx`, Sass, source maps, `package.json`/`tsconfig` configs, `testdata/` / `__tests__/`, etc. |
| Usually kept for release | `sforum.extension.json`, manifest shards, `backend/plugin`, prebuilt `.mjs`/`.css`, `README.md` |
| Default output | `<package-root>/<dirname>.sforum.zip` + adjacent `.sbom.json` |

Package validation runs first. Example output:

```text
package	…/acme.demo.sforum.zip
digest	…
sbom	…/acme.demo.sforum.zip.sbom.json
files	12
skipped	8	(source/dev files)   # only with --exclude-source and skips
```

**Important:**

- Runtime installation does **not** need sources, but the default `package`
  does **not** strip them; add `--exclude-source` for distribution, or pack from
  a clean release directory.
- `--exclude-source` is a heuristic filter, not "only the `packageFiles` list".
- Uploaded zips are lazily validated and stored; first enable of executable
  logic requires an exact-artifact trust confirmation. Operator guidance:
  [extensions & themes](../usage/extensions.md).

Recommended release loop:

```sh
# 1. Compile the backend to backend/plugin
# 2. Build frontend assets (if present) + refresh digests + run package gates
go run ./cmd/sforum extension build <package-root>
# 3. Build the release zip
go run ./cmd/sforum extension package <package-root> --exclude-source -o /tmp/my-plugin.sforum.zip
```

### Host docs — `docs generate`

After changing host surfaces (events, capabilities, contribution points,
provider slots, schedules, etc.):

```sh
go run ./cmd/sforum extension docs generate
go run ./cmd/sforum extension docs generate --check   # CI: fail on drift
```

Writes to `docs/extensions/catalogs/` by default (`--out` overrides).

### Plugin commands — `command`

```sh
go run ./cmd/sforum extension command list
go run ./cmd/sforum extension command run <command-id>
go run ./cmd/sforum extension command run <command-id> --input '{"key":"value"}'
go run ./cmd/sforum extension command run <command-id> --input-file /tmp/input.json
```

Requires a usable `DATABASE_URL` (or `--database-url`). Add `--safe-mode` to
enforce Safe Mode on top of `SFORUM_SAFE_MODE`. `list --json` emits
machine-readable output.

### Out-of-band recovery

Without starting the SForum main process or executing plugin code:

```sh
go run ./cmd/sforum extension list
go run ./cmd/sforum extension list --json
go run ./cmd/sforum extension disable <extension-id>
go run ./cmd/sforum extension disable-all
go run ./cmd/sforum extension quarantine <extension-id> \
  --expect-version <exact-version> \
  --expect-digest <64-hex-digest>
```

`quarantine` only isolates an **exact** built-in/system artifact:
`--expect-version` and `--expect-digest` must match the active version to avoid
mis-quarantines.

`extension recovery lifecycle-close --operation-id <id> --reason "<why>"`
moves one open lifecycle operation (`completed_at IS NULL`) to the `cancelled`
terminal state in a single transaction (open steps become `failed`, with an
`extension.cli_recovery` audit row). It is fail-closed: an operation whose
steps still hold an unexpired lease is refused unless `--force` is passed
(operator takes responsibility). Use it when the executing process died
mid-operation and the durable row can never reach a terminal state; restart
the API afterwards to resume convergence. The startup path also reclaims
automatically: orphan operations whose every step lease has expired are
advanced to `cancelled` by the host during genesis retries and coordinator
polls (`extension.lifecycle_orphan_reclaim` audit), no manual step required.

`recovery` is the top-level entry for the same out-of-band subcommands:
`sforum recovery <subcommand>` is equivalent to
`sforum extension recovery <subcommand>`, which shortens the command at an
incident scene. Both paths only read durable rows; neither starts the main
process nor loads plugin code.

`extension recovery activation-state` handles the interruption window where
the state transaction already committed its target while the publication did
not: it calls the same collaborator as the API startup repair and restores the
publication to its exact source artifact from durable evidence only, turning
into a no-op as soon as conditions drift. `--limit` caps the candidates
inspected in one run (0 means the default) and `--json` prints the recovered
operations (extension, state publication, from/to versions, audit event ID).

`extension recovery recheck --reason "<why>"` is the explicit recheck channel
for recovery-only: it rebuilds the desired plugin runtime full set from durable
rows and compares it with the latest immutable publication. On an exact match
it publishes a new immutable `reason=recovery` revision; on any mismatch it
prints every offending artifact and exits non-zero instead of "fixing" it. The
command only records "re-converge" as a durable fact — it does **not** hot
recover: the running process stays in recovery-only and the Host applies the
revision at its next process start. `--reason` is required and audited
(≤4096 bytes).

### Version garbage collection — `gc`

```bash
# Dry-run is the default: print candidates plus retain/block reasons and mint
# a one-time confirmation token (15 minutes, bound to this exact plan).
go run ./cmd/sforum extension gc --json
# Execute (the token is consumed once; repeat the same policy options).
go run ./cmd/sforum extension gc --confirm-token <token from dry-run>
# Policy knobs: retained historical versions per extension / retention days / one extension.
go run ./cmd/sforum extension gc --retain 3 --retain-days 30 --extension-id <id> --json
```

`extension gc` reclaims **historical extension version rows and their disk
directories** (default policy: keep the active version plus the most recent
historical one; adjustable via `--retain` / `--retain-days`). Three fail-closed
lines: a candidate must be neither active nor staged, must not be targeted by
an open lifecycle operation, must not be referenced by the append-only
publication ledger, must be beyond the retention window, and its directory
must exist; execution deletes disk first (atomic staging rename) and the
ledger second, and any failure rolls that item back with the directory
restored. Versions referenced by `plugin_runtime_publication_members` are
permanently undeletable at the SQL layer (`blocked=publication_history`), so
**plugin versions that were ever enabled are retained forever** — the real
reclaim surface is theme history plus plugin versions that never entered a
publication. `blocked=disk_missing` rows belong to the admin missing-artifact
cleanup flow, not GC. The confirmation token is one-time and bound to the
plan digest: any criteria/list/option drift between dry-run and execution
invalidates it. Every removal writes an `extension.version_gc` audit event and
an `extension_version_gc_removals` anchor row. Criteria and boundaries:
[version GC operator reference](../../extensions/version-gc.md) and
`knowledge/decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`.

### Development edit loop — `dev`

```bash
cd apps/api
APP_ENV=development go run ./cmd/sforum extension dev ../../extensions/dev/acme.demo
```

Development-only: production, staging and test are refused at command entry (the same
predicate the orphan reaper uses). The package must live under `extensions/dev`,
`extensions/optional`, `extensions/fixtures`, or an external plugin repository root
declared in `EXTERNAL_EXTENSION_ROOTS` (subdirectories included) — standalone plugin
repositories do not need to be copied into the host checkout; protected
`extensions/builtin` source trees are refused because built-ins must go through
`scripts/build-builtin-plugins.sh` plus the admin activation flow.

The loop watches **package-contained source inputs only**: `dist/`, `node_modules/`,
`.git/`, archives, editor swap files and temp files are ignored, and the manifest the
build transaction itself rewrites does not count as an author edit (content-stamp
snapshot), so the loop cannot trigger itself.

| flag | Effect |
| --- | --- |
| `--once` | Run one build transaction and exit (no watcher; CI friendly) |
| `--json` | One JSON object per phase for editor integrations |
| `--debounce` | Event coalescing window (default `400ms`) |
| `--skip-install` | Skip `bun install` and reuse existing frontend dependencies |
| `--auto-trust` | After each successful build, auto-refresh dev trust sessions (see below) |
| `--auto-upgrade` | After the trust refresh succeeds, upgrade the staged candidate through the existing lifecycle route (implies `--auto-trust`). With a dev trust session this completes the save-to-live chain: save → build/digest/validate/test → stage → trust → upgrade. When the extension is disabled the cycle reports `auto_upgrade_failed`; enable it once in the admin flow and the chain resumes |

**It does not bypass trust.** Each cycle prints the new package digest and the next
step: the exact artifact still has to be confirmed by an operator in the admin
extensions flow before it is staged or activated. A failed build or preflight keeps
the last healthy artifact and reports the failing stage and reason.

### Development trust sessions — `dev-trust` (development only)

Each development session needs exactly **one** super_admin confirmation; afterwards
every digest built by the same watch loop is auto-granted as an exact live trust
grant (per-digest, audited, revocable at any time). Production has no dev package
roots and the routes fail closed; the trust model is unchanged.

```bash
cd apps/api
APP_ENV=development go run ./cmd/sforum extension dev-trust create ../../extensions/dev/acme.demo
APP_ENV=development go run ./cmd/sforum extension dev-trust list
APP_ENV=development go run ./cmd/sforum extension dev-trust revoke <sessionID>
```

- `create`: creates a session for a package under an allowed development source
  root (default TTL 8 hours, one active session per package); the call itself is
  the human confirmation and must carry `confirm: true`.
  Allowed roots = the `EXTERNAL_EXTENSION_ROOTS` external plugin repositories
  (including subdirectories) ∪ the scaffold default `extensions/dev` tree; the
  server reads the manifest under that directory and verifies the extension ID
  before creating the session, so external repositories need no (and cannot use
  a) symlink. The session key is the extension ID — the source root is decoupled
  from the stored snapshot path, and digests staged by `SyncExternalSources`
  are auto-granted as well.
- `--ensure`: idempotent confirmation — a `409 extension.dev_trust_session_exists`
  response (an active session already exists) counts as success.

> **Legacy note**: the CLI dev loop in this section (`extension dev
> --auto-trust/--auto-upgrade`) and `dev-trust` are the **credential-gated old
> path**. Everyday plugin development now uses the **zero-command host channel** —
> under the development assembly the host watches `EXTERNAL_EXTENSION_ROOTS`
> itself and, on file save, auto build → stage → grant → upgrade with no login,
> no credentials, no session, and no wrapper script (the old
> `scripts/dev-plugin-loop.sh` was removed). See the "zero-command dev
> live-reload" section in `docs/extensions/build-and-load.md`.
- `extension dev --auto-trust`: calls `POST /admin/extensions/{id}/dev-trust-refresh`
  after each successful build and prints `dev_trusted digest=…`; without a session
  or credentials it falls back to `reload_required`, identical to the manual path.
  With the development assembly the endpoint first re-scans the
  `EXTERNAL_EXTENSION_ROOTS` external sources (`SyncExternalSources`), staging the
  freshly rebuilt digest into the running API — no dev API restart is needed to see
  a new artifact anymore.
- `extension dev --auto-upgrade`: after the trust refresh succeeds it calls the
  existing `POST /admin/extensions/{id}/upgrade` (the exact-artifact trust grant is
  already in place), promoting the staged candidate to the active version and
  restarting the plugin backend subprocess at the new digest. Failures print
  `auto_upgrade_failed` with the reason and never stop the loop: the full chain is
  save → build/digest/validate/test → stage → trust → upgrade.
- Credential resolution order: `SFORUM_SESSION_COOKIE` / `SFORUM_DEV_AGENT_PAT`
  → the **agent credential cache**
  (`$XDG_CACHE_HOME/sforum/dev/agent-credentials.json`, mode 0600, override with
  `SFORUM_DEV_AGENT_CACHE`; never stores passwords) →
  `SFORUM_ADMIN_EMAIL`+`SFORUM_ADMIN_PASSWORD` (same convention as
  `tests/admin-extension-activation.sh`), which logs in and caches the session
  pair automatically → a one-time interactive prompt in a terminal (password not
  echoed) that caches on success.
- Before the first non-interactive run (for example an agent session), the
  operator runs `extension dev-trust create --ensure <package-root>` once in a
  terminal (or simply starts the dev loop) so the prompt caches the session;
  afterwards every wrapper / agent run is credential-free and interaction-free.
- Both cached forms are revocable: PAT (`{"kind":"pat",...}`, audited as
  `api_token.*`, revocable in the admin) or a session pair
  (`{"kind":"session",...}`, revoked on logout / admin session teardown). The
  cache belongs to this dev machine only; production never reads it (the dev
  trust service is not injected there).
- Starting Safe Mode revokes all sessions immediately; they do not resurrect after
  Safe Mode ends.
- The admin surface lists active sessions with one-click revocation
  (`/admin/extensions/dev-trust-sessions`).

### Built-in discovery — `builtins`

```bash
cd apps/api
go run ./cmd/sforum extension builtins
go run ./cmd/sforum extension builtins --json
```

Walks the built-in root (default `extensions/builtin`) and reports every Manifest V3
package with its `id`, relative path, backend entry and whether it has a Go backend.
`scripts/build-builtin-plugins.sh` consumes that output, so adding or renaming a
built-in no longer requires syncing a hardcoded id list in shell.

### API LTS status

```sh
go run ./cmd/sforum extension api-lts
go run ./cmd/sforum extension api-lts --json
```

### System tier — `system-tier`

Manage SystemTier membership (load order and recovery semantics) without
loading package code:

```sh
go run ./cmd/sforum extension system-tier list
go run ./cmd/sforum extension system-tier upsert <extension-id> \
  --role infra --priority 100 --enabled true
go run ./cmd/sforum extension system-tier disable <extension-id>
```

`upsert --role` accepts `auth|cache|storage|infra`; lower `--priority` loads
first. All subcommands accept `--database-url` and `--json`.

---

## Seed data: `seed:forum` and `seed:perf`

```sh
# config.Load does not read .env; export the variables first
set -a; . ../../.env; set +a   # adjust the path from apps/api as needed

go run ./cmd/sforum seed:forum
go run ./cmd/sforum seed:forum --count=100 --users=20 --comments-max=3
go run ./cmd/sforum seed:forum --count=400 --users=60 --comments-max=8 --tags=16 --pinned=4
go run ./cmd/sforum seed:forum --categories=0 --tags=0 --pinned=0   # users/topics/comments only
go run ./cmd/sforum seed:forum --profile=perf-1m --dry-run
go run ./cmd/sforum seed:forum --database-url 'postgres://…'
```

| Feature | Notes |
| --- | --- |
| Generates | Users and public profiles, category groups/categories, tags, topics (tags/pinned/views), nested comments |
| Writes | Append-only; repeatable (existing categories/tags are skipped) |
| Events | Does not trigger domain events |
| Environment | **development/test only**, never against production |
| Dependency | `DATABASE_URL` or `--database-url` |
| Permissions | Categories/tags need a `super_admin`; on an empty database the first seeded user becomes one |

Categories and tags come from a built-in catalog (site → tech → life). It exists
only for fake data; operators can rename or delete it in the admin UI.

Key `seed:forum` flags (see `--help` for the full list):

| Flag | Default | Notes |
| --- | --- | --- |
| `--profile` | `small` | `small` or `perf-1m` |
| `--count` | 1000 | Topic count (perf-1m default 1,000,000) |
| `--users` | 50 | Fake users (perf-1m default 200) |
| `--comments-max` | 5 | Max comments per regular topic (perf-1m default 0) |
| `--categories` | 0 | small: first N built-in catalog categories (default 6, with groups); perf-1m: category count (default 20) |
| `--tags` | 0 | small: built-in tags to create (default 12) |
| `--pinned` | 0 | small: pinned topics (default 3, placed in the announcement category) |
| `--views-max` | 0 | small: view-count ceiling for regular topics (default 500; pinned topics use `[max, 2*max)`) |
| `--hot-comments` | 0 | perf-1m hot-thread comments (default 50000) |
| `--hot-slug` | empty | perf-1m hot-thread slug (default `perf-hot-thread`) |
| `--category-slug` | empty | small: pin every topic to this category (default: distributed across the catalog) |
| `--batch` | 20 | Log/batch size (perf-1m default 5000) |
| `--dry-run` | false | Print the plan only |
| `--confirm-perf-db` | false | Required for non-dry-run perf-1m writes |
| `--database-url` | env | Override `DATABASE_URL` |

In the `small` profile, passing an explicit `0` for
`--categories/--tags/--pinned/--views-max` disables that part of the dataset.

`seed:perf` is an alias of `seed:forum --profile=perf-1m` with the same perf
parameters (`--count` 1,000,000, `--users` 200, `--categories` 20,
`--hot-comments` 50000, `--batch` 5000, …) and also requires
`--confirm-perf-db` to write.

---

## Account maintenance: `users:reset-password`

Resets any user's password (reads only `DATABASE_URL`/`--database-url`; no other app
config is loaded). The default flow is interactive; automation (CI, release-image
smoke) uses the non-interactive mode:

```sh
# Interactive
go run ./cmd/sforum users:reset-password
go run ./cmd/sforum users:reset-password --database-url 'postgres://…'
# Alias:
go run ./cmd/sforum user:reset-password

# Non-interactive (requires --yes; a missing flag fails before touching the database)
go run ./cmd/sforum users:reset-password --database-url 'postgres://…' \
  --email admin@example.com --password '<password meeting the site policy>' --yes
```

Non-interactive mode uses the same site password policy and the same
`AdminSetUserPassword` path as the interactive flow: a weak password is still
rejected, and the reset still bumps the token version and revokes the user's active
sessions.

---

## Content revisions: `revisions backfill`

Backfills the forum content revision ledger in bounded batches:

```sh
go run ./cmd/sforum revisions backfill
go run ./cmd/sforum revisions backfill --batch 1000 --loop   # until pending=0
go run ./cmd/sforum revisions backfill --database-url 'postgres://…'
```

`--batch` is the posts processed per batch (default 100); `--loop` keeps going
until nothing is pending.

---

## Dev process cleanup: `dev:cleanup-orphan-plugins`

Stops reparented/orphaned SForum extension backend plugin processes (safe for
live `sforum-api` children; PID-allowlist filtered):

```sh
go run ./cmd/sforum dev:cleanup-orphan-plugins
go run ./cmd/sforum dev:cleanup-orphan-plugins --dry-run   # list PIDs, no signals
```

---

## Package layout (quick map)

| Directory | Use | Boot-scanned into admin |
| --- | --- | --- |
| `extensions/dev/` | Local experiments (scaffold default) | No |
| `extensions/builtin/` | Shipped, protected built-ins | Yes (`SyncBuiltins`) |
| `extensions/optional/` | In-repo optional, operator-installed | No |
| `extensions/fixtures/` | CI / contract fixtures | No |
| Runtime `EXTENSION_ROOT` | Operator-uploaded installs | No |
| `EXTERNAL_EXTENSION_ROOTS` | Independent source trees | Yes (lazy snapshot, never auto-enabled) |

Full map: [extensions/README.md](../../../extensions/README.md).
Mechanics and trust model: [plugin authoring guide](../../extensions/authoring-guide.md), [operator extensions guide](../usage/extensions.md).

---

## Related docs

- [API usage](./api.md)
- [Daily workflow](./workflow.md)
- [Environment setup](./setup.md)
- [Testing & gates](./testing.md)
- [Plugin authoring guide](../../extensions/authoring-guide.md)
- [Host API v2](../../extensions/host-api-v2.md)
