# One-click install and update

[← Production deployment](../deployment.md)

This page covers three release scripts in depth: `sforum-bootstrap.sh`,
`deploy.sh`, and `upgrade.sh`, plus the prefetch wrapper `sforum-latest.sh`,
which ships as a Release asset too. Responsibilities first, then the execution
chains, and finally state files and failure semantics.

## Deliverables and responsibilities

| File | Responsibility |
| --- | --- |
| `sforum-install.sh` | One-line entry (Release asset): resolves an immutable tag, verifies that tag's `sforum-bootstrap.sh`, promotes it, and execs it; carries no install logic |
| `sforum-bootstrap.sh` | Online entry point: resolves an immutable tag, verifies and refreshes itself and the complete toolkit, then hands off to `deploy.sh` or `upgrade.sh` |
| `sforum-deploy.tar.gz` | The complete deploy toolkit for the same Release |
| `sforum-latest.sh` | Prefetch wrapper (standalone Release asset, not inside the bundle): resolves the target Release, verifies its complete deploy bundle, then hands off to the bootstrap of the same tag |
| `sforum-latest.sh.sha256` | SHA-256 checksum of that script, a separate asset of the same Release |
| `deploy.sh` | Install/update console, plus backup, restore, status, logs, restart, and stop |
| `upgrade.sh` | Blue/green updater for existing installs |
| `deploy/scripts/configure-production.sh` | `.env.production` configuration wizard and key generation |
| `deploy/scripts/backup-postgres.sh` | Encrypted archive of the database plus the attachment and extension-artifact volumes; writes a temporary file first and then publishes it as `0600` |
| `deploy/scripts/restore-postgres.sh` | Separate temporary database plus single-transaction restore; swaps the database name only after the core tables, row counts, and API readiness pass, and rolls back on failure |
| `deploy/scripts/postgres-common.sh` | Shared backup/restore library: configuration reads, topology and Compose assembly, readiness waits, database content validation |
| `deploy/scripts/postgres-backup-format.sh` | Shared backup/restore library: archive format, encryption, volume archiving, and retention |
| `deploy/scripts/wait-for-health.sh` | Health-check wait |
| `compose.yaml` + `compose.prod.yaml` + `compose.release.yaml` | Direct production topology and release images |
| `compose.zero-downtime.yaml` | Blue/green ingress and dual-slot topology |
| `deploy/caddy/Caddyfile` | Host Caddy reverse-proxy example |
| `deploy/router/Caddyfile.example` | Blue/green ingress Caddy example |
| `VERSION` | The exact version the deploy bundle belongs to |

`sforum-latest.sh` and its `.sha256` ship with each Release from this version
on, but they are **not** part of `sforum-deploy.tar.gz` and are not refreshed
by the bootstrap: the wrapper has to reach the network before any local script
runs, so it can only be a separately downloaded layer. Each run resolves the
target Release, verifies that Release's complete deploy bundle, and only then
hands off to the bootstrap of the same tag.

Call graph:

```text
curl .../sforum-install.sh | bash ──> sforum-bootstrap.sh install|upgrade
sforum-bootstrap.sh install ──> deploy.sh --action deploy
sforum-bootstrap.sh upgrade ──> upgrade.sh
sforum-latest.sh            ──> sforum-bootstrap.sh upgrade
./deploy.sh                  ──> interactive menu / --action ...
```

## How to obtain

### Option A: one-line loader (recommended)

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash
```

It asks for the install directory first (default `./sforum`, shown as an absolute
path), and Enter accepts it; a directory that already has `.env.production` and a
versioned `.deployrc` updates instead. `-d DIR` or `SFORUM_INSTALL_DIR` sets the
target without asking, `--version <TAG>` pins a tag, `--channel prerelease`
selects a prerelease, `--yes` skips prompts, and `SFORUM_NONINTERACTIVE=1` never
prompts at all (default directory plus `--yes` for the bootstrap). To update
inside an existing installation without trusting the local bootstrap:

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash -s -- upgrade
```

#### Install directory

The prompt looks like this; the brackets hold the absolute path:

```text
SForum install directory
  Current directory: /srv/apps
  Install directory [ /srv/apps/sforum ]:
```

- Enter accepts the default. Relative answers resolve against the current
  directory, and `~/` expands.
- `/`, an existing regular file, and an unwritable location are refused and the
  question repeats.
- A non-empty directory that is not an installation asks once more with
  `Install here anyway? [y/N]`; declining returns to the directory question
  instead of aborting.
- When the current directory is already an installation, the default is that
  directory, so a nested second `./sforum` is never created by accident.
- Non-interactive runs (`--yes`, `SFORUM_NONINTERACTIVE=1`, no terminal) never
  ask: they use the default or `--dir`, and still print
  `Install directory: /abs/path`.
- After a successful install or update, `deploy.sh` / `upgrade.sh` print the
  install directory, configuration file, backup directory, and access URLs
  again, and `./deploy.sh --action status` shows the same three paths.

The loader stays narrow: it picks the install directory (writing no install
state), resolves an immutable tag, downloads that tag's `sforum-bootstrap.sh`
and `SHA256SUMS`, verifies the exact filename entry (with optional
`gh attestation verify` and an `SFORUM_INSTALL_SHA256` pin), then
`install -m 0755` and execs the bootstrap. It only reads `.env.production` /
`.deployrc` for existence and the recorded version that selects install versus
upgrade; it never modifies them, `deploy/runtime/`, or data volumes.

The loader's own bytes are not checksummed — the same first-layer tradeoff as
`get.docker.com` and rustup. The compensations are its small auditable size, the
`SFORUM_INSTALL_SHA256` pin, the bootstrap's own re-verification, and the
auditable command below when even the first layer must be verified.

#### Auditable equivalent

Download, verify, then execute — the same semantics as the loader:

```sh
(
  set -eu
  mkdir -p sforum
  cd sforum
  bootstrap_dir="$(mktemp -d .sforum-bootstrap.XXXXXX)"
  trap 'rm -rf "$bootstrap_dir"' EXIT HUP INT TERM
  curl -fsSLo "$bootstrap_dir/sforum-bootstrap.sh" \
    https://github.com/keeptrue/SForum/releases/latest/download/sforum-bootstrap.sh
  curl -fsSLo "$bootstrap_dir/SHA256SUMS" \
    https://github.com/keeptrue/SForum/releases/latest/download/SHA256SUMS
  (
    cd "$bootstrap_dir"
    awk '$2 == "sforum-bootstrap.sh" { print }' SHA256SUMS > sforum-bootstrap.sha256
    test "$(wc -l < sforum-bootstrap.sha256 | tr -d '[:space:]')" = 1
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c sforum-bootstrap.sha256
    else
      shasum -a 256 -c sforum-bootstrap.sha256
    fi
  )
  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh
  rm -rf "$bootstrap_dir"
  trap - EXIT HUP INT TERM
  ./sforum-bootstrap.sh install
)
```

To pin a version, replace both rolling URLs with the same tag:

```sh
https://github.com/keeptrue/SForum/releases/download/<TAG>/sforum-bootstrap.sh
https://github.com/keeptrue/SForum/releases/download/<TAG>/SHA256SUMS
```

### Option B: full deploy bundle

For offline audit, for keeping the whole bundle, or when the bootstrap should
not go online to refresh itself first:

```sh
(
  set -eu
  mkdir -p sforum
  cd sforum
  curl -fsSLo sforum-deploy.tar.gz \
    https://github.com/keeptrue/SForum/releases/latest/download/sforum-deploy.tar.gz
  curl -fsSLo SHA256SUMS \
    https://github.com/keeptrue/SForum/releases/latest/download/SHA256SUMS
  awk '$2 == "sforum-deploy.tar.gz" { print }' SHA256SUMS > sforum-deploy.sha256
  test "$(wc -l < sforum-deploy.sha256 | tr -d '[:space:]')" = 1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c sforum-deploy.sha256
  else
    shasum -a 256 -c sforum-deploy.sha256
  fi
  if command -v gh >/dev/null 2>&1; then
    gh attestation verify sforum-deploy.tar.gz --repo keeptrue/SForum
  fi
  tar -xzf sforum-deploy.tar.gz --strip-components=1
  ./sforum-bootstrap.sh install
)
```

The bundle root carries `README.md` and `VERSION`. The extracted directory is
the install directory; later updates, backups, and restores all run there.

## Install execution chain

`sforum-bootstrap.sh install` is not "download and then just run"; it verifies
and hands off in stages.

1. Check `curl`, `tar`, and whether the current directory already has
   `.env.production` or `.deployrc`. Either one makes install refuse and
   suggest upgrade instead.
2. Resolve the target tag. The default `stable` queries only GitHub
   `/releases/latest`; only an explicit `--channel prerelease` or
   `--version <TAG>` allows prereleases. The result must be an immutable tag of
   the form `vX.Y.Z`.
3. Download the target tag's `sforum-bootstrap.sh` and `SHA256SUMS`, take only
   the exact checksum entry for that filename, verify it, and then run a
   `bash -n` syntax check.
4. Overwrite itself with the new bootstrap, then re-execute; this step
   guarantees that all later logic comes from the target Release rather than
   the stale local script.
5. Download and verify `sforum-deploy.tar.gz`: the archive contains only
   `sforum-deploy/` paths, has no path traversal, its `VERSION` matches the
   target tag, the managed files are complete, and the three entry scripts are
   syntactically valid.
6. Back up the current managed tooling files to `.sforum/tooling-backups/`,
   then replace them one by one atomically. A failed replacement restores the
   previous toolkit and exits.
7. Hand off to `./deploy.sh --action deploy --version <TAG>`.
8. `deploy.sh` starts the configuration wizard when `.env.production` is
   missing; when the file exists it only runs `chmod 600` and never overwrites
   secrets.
9. Preflight: Docker, daemon, Compose version, `curl`, the environment-variable
   contract, Compose expansion, and loopback port conflicts.
10. Pull the `migrate`, `api`, and `web` images; verify the build identity of
    `api` and `migrate` with `docker run --rm <image> sforum-api --version` /
    `sforum-migrate --version`.
11. Start `postgres` and `redis`. An existing database is backed up first; a
    fresh database skips the backup.
12. Stop the old app. The blue/green topology stops `edge` and both slots; the
    direct topology stops only `api` and `web`. Leftover standalone `worker`
    containers are then removed.
13. Run migrations with the target image; both migration and startup use
    `--pull never`, so nothing depends on the registry again after the old app
    stops.
14. Start `postgres redis api web` with `--no-build --pull never`.
15. Check `http://127.0.0.1:${API_PORT}/api/v1/ready` and
    `http://127.0.0.1:${WEB_PORT}/health` to confirm all four managed services
    are running and stable.
16. Write `.deployrc` and print the reverse-proxy target, the site address, and
    the admin panel address.

A failure at any step never marks the instance successful. When migration,
startup, or health checks fail, `.deployrc` records
`status=recovery_required`.

## The `deploy.sh` console

Running it without arguments opens an interactive menu:

| Menu | Action |
| --- | --- |
| 1 | Install or update (recommended) |
| 2 | Create a PostgreSQL backup |
| 3 | Restore a PostgreSQL backup |
| 4 | Show service status |
| 5 | Show logs |
| 6 | Restart services |
| 7 | Stop services |
| 0 | Exit |

For scripting, use `--action`:

| Action | Behavior |
| --- | --- |
| `deploy` / `install` / `update` | Resolve the target version, back up an existing database, migrate, start, health-check, and record `.deployrc` |
| `backup` | Start `postgres`, create an encrypted archive (database + attachment volume + extension-artifact volume), and print its path; fails without `SFORUM_BACKUP_KEY_FILE` |
| `restore` | Prompt for `RESTORE`, then decrypt and validate the archive before restoring; any failure rolls back. The source must be the file the operator **names explicitly**: the script never walks the backup directory and never picks the newest archive for you. Plaintext payloads without integrity evidence (a legacy `.sql`, a manifest-less `tar.gz`, a manifest missing `encryption=`) are refused by default; to restore one anyway, set `SFORUM_RESTORE_ALLOW_UNVERIFIED=1` explicitly — the script then warns and continues |
| `status` | Run `docker compose ps` with the currently recorded version |
| `logs` | Follow all service logs |
| `restart` | Restart services and re-run the health checks |
| `stop` | Stop managed services |
| `rollback` | Print rollback guidance only; does not change the version |

Version resolution has two paths:

- **Target version**: used by `deploy`/`install`/`update`. An explicit
  `--version` wins; otherwise `--channel` resolves `latest`. The old version in
  `.deployrc` is never treated as the target.
- **Current version**: used by `status`/`logs`/`restart`/`stop`/`backup`/`restore`.
  Reads `.deployrc` by default, then falls back to `SFORUM_VERSION` in
  `.env.production`; maintenance actions never contact GitHub just to display
  status.

`--lang zh|en` sets the interface language; `--yes`/`--defaults` accept the
recommended configuration. The deployment lock is `.deploy.lock`; the same
directory cannot run two deploy actions concurrently.

## Update execution chain

### Entry point: `sforum-bootstrap.sh upgrade`

Upgrade requires both `.env.production` and `.deployrc` to exist. It first
refreshes itself and the complete toolkit, then hands off to the local
`upgrade.sh`. Refreshing does not modify `.env.production`, `.deployrc`,
`deploy/runtime/`, or data volumes.

A blue/green switch backs up the database first, so configure
`SFORUM_BACKUP_KEY_FILE` (mode `0600`) before upgrading exactly as for a
deploy; otherwise `upgrade.sh` stops at the backup step.

### Prefetch wrapper: `sforum-latest.sh` (optional)

The bootstrap's self-refresh is initiated by the local old script. The prefetch
wrapper moves network resolution and verification ahead of any local script:

1. Resolve the target Release. Interactive runs ask for the channel first
   (stable, newest published release, or explicit tag); for stable it reads the
   tag from the `releases/latest` 302 `Location`, spending no GitHub API quota;
   prereleases use the `/releases` API.
2. Download that tag's `sforum-deploy.tar.gz` and `SHA256SUMS`, verify the exact
   entry, the archive paths, and `VERSION`; when the local `gh` supports
   `attestation`, verify provenance as well.
3. Extract the authoritative managed-file list from the bundle's bootstrap and
   atomically replace every missing or stale `deploy.sh`, `upgrade.sh`, Compose
   file, `deploy/scripts` entry, Caddy example, and `VERSION`; the previous
   files are backed up under `.sforum/tooling-backups/`.
4. Run `sforum-bootstrap.sh install|upgrade --version <TAG>` with the pinned
   tag. When `.env.production` exists but `.deployrc` has no usable version
   record, run `deploy.sh --action deploy --version <TAG>` instead to rebuild the
   managed state.

Run this inside the install directory:

```sh
# Latest stable Release (recommended): script and checksum are two assets of
# the same tag.
curl -fsSLO https://github.com/keeptrue/SForum/releases/latest/download/sforum-latest.sh
curl -fsSLO https://github.com/keeptrue/SForum/releases/latest/download/sforum-latest.sh.sha256
{ sha256sum -c sforum-latest.sh.sha256 2>/dev/null || shasum -a 256 -c sforum-latest.sh.sha256; } && \
chmod 0755 sforum-latest.sh && ./sforum-latest.sh --check

# Pinned version (fully immutable): replace <TAG> with a concrete tag such as
# the vX.Y.Z shown on the release page.
VERSION=<TAG>
curl -fsSLO "https://github.com/keeptrue/SForum/releases/download/$VERSION/sforum-latest.sh"
curl -fsSLO "https://github.com/keeptrue/SForum/releases/download/$VERSION/sforum-latest.sh.sha256"
{ sha256sum -c sforum-latest.sh.sha256 2>/dev/null || shasum -a 256 -c sforum-latest.sh.sha256; } && \
chmod 0755 sforum-latest.sh && ./sforum-latest.sh --check --version "$VERSION"
```

Drop `--check` to run the install or update.

`--check` performs only the first two steps plus the comparison and prints the
target version, the bundle SHA-256, which files are missing or stale, and the
planned action — useful before a maintenance window. A target older than the
`.deployrc` record is reported as a warning first. `--lang` applies only to
install while `--bootstrap` applies only to upgrade. In a Git checkout, syncing
requires the explicit `--allow-git` flag.

#### Verification chain

1. **`.sha256` anchors the script bytes**: the checksum and the script are two
   Release assets of the same tag and must come from the same publication. If
   the check fails, stop and do not run the script.
2. **The script anchors the deploy bundle**: it resolves the tag once and then
   downloads `SHA256SUMS` and `sforum-deploy.tar.gz` from **that tag**, verifying
   the exact bundle listing, the in-archive paths, and `VERSION`. Any mismatch
   fails the run, so a script from one version never drives tooling from
   another.
3. **Build provenance is verifiable**:
   `gh attestation verify sforum-latest.sh --repo keeptrue/SForum` proves the
   script came out of this repository's Release workflow; the deploy bundle can
   be verified the same way.

Earlier Releases do not carry these two assets; for those versions the only
path is the raw `main` address, where the script and its checksum live on the
same mutable branch and the checksum anchors nothing. Use a Release that ships
both assets when the download must be immutable.

### Updater: `upgrade.sh`

1. Read the current version from `.deployrc`. When it is missing or invalid,
   look it up from the running `api-blue` / `api-green` image tags; if it still
   cannot be determined, require `./deploy.sh` to run first.
2. Interactively confirm the channel and target version; `--yes` skips the
   confirmation. The target likewise resolves to an immutable tag.
3. Acquire `.deploy.lock`. The lock records a PID; a lock whose owning process
   has exited, or that is older than one hour, is cleaned up automatically.
4. If `.deployrc` has no `active_slot`, perform the one-time ingress
   conversion: resolve and pull the target images (see below) and Caddy, start
   PostgreSQL/Redis, check migration compatibility, back up, migrate, start
   `api-blue`/`web-blue`, health-check inside the slot, write
   `deploy/runtime/Caddyfile`, stop the old `api`/`web`, start `edge`, and then
   health-check the ingress. Interactive mode requires typing `BOOTSTRAP`;
   unattended runs require an explicit `--bootstrap`.
5. If an `active_slot` already exists, perform an online update: resolve the
   digests, then pull and verify the three target images (see below); check
   that all target migrations can run online; back up the database; apply the
   declared compatible Core migrations; start the standby slot; check
   `api/v1/ready`, `/health`, and `api/v1/ready` forwarded through Web inside
   the slot; atomically switch Caddy to the new slot; stop the old slot; record
   the new state.
   The in-slot check retries every 2 seconds for up to
   `SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS` (default 120) seconds: probes fail
   while the standby slot cold-starts, so the script only reports the final
   outcome, and a timeout prints the last probe output plus that slot's
   container state and the last 60 log lines.
6. When migrations cannot run online, enter a maintenance window: interactive
   mode asks whether to continue; `--yes` mode never stops services
   automatically — it refuses and prints
   `./upgrade.sh --version <TAG> --confirm-maintenance`. That command must be
   run directly in the install directory; the bootstrap does not forward this
   flag.
7. Failure handling: a failed maintenance-window migration restarts the old
   slot; a failed new-slot health check stops the new slot and restarts the old
   slot; a failed ingress switch rolls the Caddy configuration back to the old
   slot. A failed standby slot during an online update never affects current
   traffic.

### Image identity: content digest, not a tag

`upgrade.sh` does not trust a tag; it verifies identity before and after the
pull:

1. **Resolve before pulling**: `docker buildx imagetools inspect` turns every
   target tag into a manifest digest, which is recorded. This step needs the
   `docker buildx` plugin; when it is missing the script fails closed **before
   any pull** with `Could not resolve the manifest digest of <image>`, plus a
   note that Docker with the buildx plugin (`docker buildx imagetools`) is
   required to verify image identity.
2. **Verify after pulling**: the local image's `RepoDigests` must still contain
   that digest. A re-pushed tag (different content, different digest) dies with
   `<image> image content identity mismatch: <reference> does not resolve to
   <digest>`, so a different build never runs under the target version. The
   version label is still checked, but only as a release-line self-test — it no
   longer carries identity.
3. **Caddy accepts a digest only**: an `SFORUM_CADDY_IMAGE` override must be
   written as `name@sha256:...`. That check runs in the one-time ingress
   conversion: the script pulls the ingress image, then verifies `RepoDigests`;
   a bare tag dies with
   `SFORUM_CADDY_IMAGE must pin a digest (name@sha256:...), got '<value>'.` —
   by then the three application images may already be pulled. An online update
   does not recreate `edge`, so it does not re-check the value; after changing
   the override, confirm the format before the next ingress conversion. The
   default is
   `caddy:2.11-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0`,
   byte-identical to `compose.zero-downtime.yaml`; the old drift where
   `upgrade.sh` pulled `caddy:2.10.2-alpine` while Compose used `2.11-alpine`
   is gone.

Upgrade hosts therefore have one more prerequisite: a Docker with the `buildx`
plugin (`docker buildx version` prints output). `deploy.sh` and the direct
topology do not need it.

Long-lived WebSocket connections may need to reconnect automatically during a
switch. The standby slot and the old slot both embed their Worker, so they may
briefly consume the queue at the same time during the switch; job claims and
retries are guaranteed by River's database locks, and the old slot stops after
the switch, leaving no standalone Worker behind.

## Legacy install compatibility

The `upgrade.sh` shipped with `v3.0.0-alpha.12` and earlier cannot start the
database compatibility preflight correctly, and the old `latest` semantics
included prereleases. A legacy install directory must first download the fixed
script from the immutable `v3.0.0-alpha.13` tag, verify its SHA-256, and then
update; later updates use the bootstrap.

```sh
(
  set -eu
  curl -fsSLo upgrade.sh \
    https://raw.githubusercontent.com/keeptrue/SForum/v3.0.0-alpha.13/upgrade.sh
  printf '%s  %s\n' ae186e13ca9551014e21ce7f77a7335413791268d97fb0b72f6be9820dedfe13 upgrade.sh > upgrade.sh.sha256
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c upgrade.sh.sha256
  else
    shasum -a 256 -c upgrade.sh.sha256
  fi
  chmod 0755 upgrade.sh
  ./upgrade.sh v3.0.0-alpha.13
)
```

That immutable tag is historical fact and will not be moved to `main` or
`latest`.

## Versions and channels

- `stable`: resolves only the latest stable Release.
- `prerelease`: allows prereleases, but requires an explicit
  `--channel prerelease` or a concrete tag.
- Every choice ultimately becomes a `vX.Y.Z` tag; production Compose does not
  run a floating `latest` image.
- `.deployrc` stores the resolved concrete version, never `latest`.
- When deployments must be repeatable, pin the version explicitly:

```sh
./sforum-bootstrap.sh install --version <TAG>
./sforum-bootstrap.sh upgrade --version <TAG>
```

## State files and locks

| Path | Purpose |
| --- | --- |
| `.env.production` | Production configuration, `0600`; the wizard never overwrites an existing file |
| `.deployrc` | On success records `lang`, `version`, `mode` |
| `.deployrc` (blue/green) | Adds `topology=blue-green`, `active_slot`, `blue_version`, `green_version` |
| `.deployrc` (failure) | `status=recovery_required`, `attempted_version`, `previous_version`, `reason`, `backup` |
| `.deploy.lock` | Deployment lock for `deploy.sh`/`upgrade.sh` |
| `.sforum-bootstrap.lock` | Lock held by the bootstrap while it refreshes the toolkit |
| `.sforum-latest.lock` | Lock held by `sforum-latest.sh` while it prefetches and replaces the local bootstrap |
| `.sforum/tooling-backups/` | Backups of replaced managed tooling files |
| `backups/` | PostgreSQL backup archives (database + attachment volume + extension-artifact volume, encrypted), `0600` by default; change the path with `SFORUM_BACKUP_DIR`. Legacy plaintext `<db>-<timestamp>.sql` dumps from before 2026-11-15 do not match the `sforum-backup-*` retention prefix and are **never cleaned automatically** — review and delete them yourself |
| `deploy/runtime/` | Blue/green Caddy configuration; do not edit by hand |

## Failure semantics

`status=recovery_required` in `.deployrc` means the old app has already stopped
but the new version has not been declared healthy. `reason` has four values:

| `reason` | Meaning | First step |
| --- | --- | --- |
| `migration_failed` | Migration failed | Check `./deploy.sh --action logs` and the migration output; fix and retry, or restore `backup` |
| `startup_failed` | Migration succeeded but containers did not come up | Check `docker compose ps` and the API logs; fix the configuration/image problem and retry |
| `health_failed` | Containers are up but health checks did not pass | Check `/api/v1/ready`, `/health`, and the dependency services; fix and retry |
| `recovery_only` | The new API booted into recovery-only mode: only `/health` and `/ready` answer, every other path returns 503 | This is not a slow start and waiting will not help; fix the artifact named in the `/ready` recovery block, then run `sforum extension recovery recheck` |

The `backup` field is the path of the backup created automatically before this
deployment. To restore:

```sh
SFORUM_CONFIRM_RESTORE=RESTORE ./deploy.sh --action restore
```

Common edge cases:

- **install refused**: the current directory already has `.env.production` or
  `.deployrc`. This prevents overwriting an existing install; use `upgrade`
  instead.
- **upgrade refused**: `.env.production` or `.deployrc` is missing. A
  half-failed install can run
  `./deploy.sh --action deploy --version <TAG>` once to complete the state, then
  return to bootstrap upgrade.
- **maintenance window refused**: unattended runs never stop services
  automatically. Run
  `./upgrade.sh --version <TAG> --confirm-maintenance` locally as prompted.
- **version rollback**: after confirming the database migrations are backward
  compatible, redeploy an earlier immutable tag. When migrations are
  incompatible, restore the pre-deployment backup instead of only rolling back
  images.

## Terminal colors

Every script in the one-click chain shares one semantic color layer:
`sforum-install.sh`, `sforum-bootstrap.sh`, `deploy.sh`, `upgrade.sh`,
`sforum-latest.sh`, plus the configuration wizard, health probe, and restore
helpers under `deploy/scripts/`. Color is applied only at the print site:
`.deployrc`, `.env.production`, the deployment-lock log, the backup script's
stdout, and every value captured through `$(...)` stay free of escape bytes, so
machines can keep parsing them.

Six semantic classes:

| Marker | Meaning | Typical content |
| --- | --- | --- |
| `== … ==` | Title / long action start | Console title, install-directory wizard, channel menu |
| `==> …` | Phase progress | Pulling images, starting dependencies, migrating, health checks |
| `[ok] …` | Success terminal state | Deployment complete, zero-downtime update complete, slot ready |
| `[!] …` | Warning that does not stop the run | Backfilled keys, skipped steps, reclaimed stale lock |
| `[x] …` | Failure | Bad option, contract failure, recovery-only, health timeout |
| Unmarked emphasis line | Key/value or next command | Install directory, access URLs, version, backup path |

Switches and precedence (first match wins):

1. `SFORUM_COLOR=never`, `NO_COLOR` (non-empty), or `TERM=dumb` — any one of them
   disables color;
2. `SFORUM_COLOR=always`, or stdout is a terminal — color is enabled;
3. everything else (redirected output, CI log capture) — no color.

In other words **CI logs and redirected output are plain text by default**; set
`SFORUM_COLOR=always` explicitly when you want color inside a pipe. Conversely
`NO_COLOR=1` is a hard opt-out that wins over `SFORUM_COLOR=always`. Machine
prefixes (`sforum-install: `, `sforum-bootstrap: `, `sforum-latest: `) always stay
at the first byte of the line; color wraps only the body after the prefix.

Example output (deliberately without escape sequences — this is what a
redirected file contains):

```text
== SForum deployment console ==
==> Checking Docker and production configuration...
==> Starting the managed PostgreSQL and Redis services...
==> Existing installation detected; creating a PostgreSQL backup...
[ok] Deployment completed successfully.
Release version: vX.Y.Z
Install directory: /srv/sforum
```

## Verification

```sh
./deploy.sh --action status
./deploy.sh --action logs
docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml ps
curl -fsS http://127.0.0.1:${API_PORT:-18080}/api/v1/ready
curl -fsS http://127.0.0.1:${WEB_PORT:-3000}/health
```

More detailed incident handling is in [Troubleshooting](./troubleshooting.md).
