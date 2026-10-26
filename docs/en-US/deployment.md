# Production deployment

[← English docs home](./README.md)

SForum runs `web`, `api` with its embedded River Worker, PostgreSQL, and Redis
in production under Docker Compose. Only Linux `amd64`/`arm64` is supported;
Windows is not. Only loopback ports are published; TLS is terminated by the
host reverse proxy.

## Choose your path

| Your situation | Use this | Read more |
| --- | --- | --- |
| Brand-new server, fastest way live | `./sforum-bootstrap.sh install` | [One-click install and update](./deployment/one-click.md) |
| Existing install, update to a new version | `./sforum-bootstrap.sh upgrade` | [One-click install and update](./deployment/one-click.md) |
| Existing install, re-verify the official bootstrap on every run | the loader's `upgrade` subcommand | [One-click install and update](./deployment/one-click.md) |
| Existing install, re-sync the whole toolkit from a Release | `./sforum-latest.sh` | [One-click install and update](./deployment/one-click.md) |
| Target version has migrations that require downtime | `./upgrade.sh --version <TAG> --confirm-maintenance` | [One-click install and update](./deployment/one-click.md) |
| No scripts, write your own Compose | Download, migrate, and start manually | [Manual Compose deployment](./deployment/manual.md) |
| Only backup, restore, logs, restart | `./deploy.sh --action ...` | [Troubleshooting](./deployment/troubleshooting.md) |
| Configure variables, secrets, proxy trust | `.env.production` | [Environment variables and secrets](./deployment/environment.md) |

## Prerequisites

- Docker Engine, and Docker Compose `2.24.4` or newer.
- `curl`, `tar`, and the standard `install` command.
- Access to GitHub and `ghcr.io`; for offline or mirrored environments see
  [Environment variables and secrets](./deployment/environment.md).
- Loopback ports `3000` and `18080` free, or different ports chosen in the
  install wizard.
- A domain already pointing at the server, plus Caddy/Nginx on the host.

Git is not required, and you do not need to clone the repository.

## Quick install (latest stable)

One line installs the latest stable Release into `./sforum`:

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash
```

`SFORUM_INSTALL_DIR` or `-d DIR` changes the target, `--version <TAG>` pins an
immutable tag, and `--yes` skips prompts. That address always points at the
latest stable Release, so no version number needs maintaining. The loader
resolves the tag, verifies that tag's `sforum-bootstrap.sh`, then hands off; the
bootstrap refreshes the complete deploy toolkit for the same target version. The
install wizard explains each setting and automatically generates the PostgreSQL,
Redis, session, verification, identity HMAC, sensitive-option encryption, and
Marketplace verifier keys. Pressing Enter for every question yields a runnable
local configuration; the generated `.env.production` is mode `0600` and secrets
are never printed to the terminal.

### Verification rules (required)

- **Only the loader may run from a pipe**: it carries no install logic, just
  resolution, verification, and handoff. `sforum-bootstrap.sh` itself always
  runs from disk and never receives piped content.
- The loader verifies only the exact `sforum-bootstrap.sh` entry in
  `SHA256SUMS`, and requires that the result is exactly one line; the bootstrap
  applies the same rule to itself and to the complete deploy bundle internally.
- A failed download, a missing or duplicate entry, or a checksum or provenance
  failure aborts before any remote script executes. The loader's own bytes are
  not checksummed; use `SFORUM_INSTALL_SHA256` to pin the expected hash of
  `sforum-bootstrap.sh` when that first layer must be fixed too.
- `gh attestation verify` is an optional provenance check (it needs the GitHub
  CLI, authenticated). The loader and the bootstrap both verify when `gh` is
  present; `SFORUM_SKIP_ATTESTATION=true` skips it explicitly, but only where
  `gh` cannot be installed.
- The fully auditable equivalent — download, verify, then execute — stays in
  [One-click install and update](./deployment/one-click.md).

### Channels and pinned versions

The default channel is `stable`: it resolves only the newest **stable**
Release. Interactive runs ask for the channel first; pressing Enter means
stable. Prereleases are never selected implicitly and must be chosen
explicitly:

```sh
./sforum-bootstrap.sh install --channel prerelease
./sforum-bootstrap.sh install --version <TAG>
./sforum-bootstrap.sh install --version <TAG> --lang zh --yes
```

Whatever the choice, the script resolves a concrete `vX.Y.Z` tag before pulling
images. Production Compose never runs a floating `latest` image.

## Update an existing install

Existing installs should update through the bootstrap. Every run first
refreshes itself to the target Release, then verifies and refreshes that
Release's complete deploy toolkit, and finally hands off to `upgrade.sh`:

```sh
./sforum-bootstrap.sh upgrade
./sforum-bootstrap.sh upgrade --version <TAG>
./sforum-bootstrap.sh upgrade --channel prerelease
./sforum-bootstrap.sh upgrade --yes
```

### Built-in plugin auto-update

On the first API startup after an upgrade, the Host automatically promotes
protected built-in plugins to the version shipped in that release. The upgrade
scripts need no extra flags; behavior and the off switch are documented in
[Extensions & themes (operators)](./usage/extensions.md#built-in-plugin-auto-update).
To review before allowing the first pass, start with
`SFORUM_SAFE_MODE=true`, turn off “Auto-update built-in plugins” in the admin,
then restart normally.

### Re-fetch the official toolkit on every run (optional)

The bootstrap's self-refresh is initiated by the local old script. When an
update must prove that the bootstrap came from GitHub, use the loader's
`upgrade` subcommand:

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash -s -- upgrade
```

When the whole toolkit must be re-synced as well (`deploy.sh`, Compose files,
`deploy/scripts`, Caddy examples, `VERSION`), use the repository-side
`sforum-latest.sh` wrapper. Each run resolves the newest Release, downloads and
verifies its complete `sforum-deploy.tar.gz`, syncs every managed toolkit file to
that version, backs up the previous files under `.sforum/tooling-backups/`, and
only then delegates to install or upgrade. `--check` lists exactly which files
are missing or stale without writing anything; in a Git checkout, syncing
requires the explicit `--allow-git` flag so source files are not replaced by
accident. Full usage and failure semantics are in
[One-click install and update](./deployment/one-click.md).

When adopting the bootstrap for the first time, download and verify it inside
the existing install directory from the **latest stable Release asset** (do not
use floating content from the `main` branch):

```sh
(
  set -eu
  cd /path/to/sforum
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
    if command -v gh >/dev/null 2>&1; then
      gh attestation verify sforum-bootstrap.sh --repo keeptrue/SForum
    fi
  )
  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh
  rm -rf "$bootstrap_dir"
  trap - EXIT HUP INT TERM
  ./sforum-bootstrap.sh upgrade
)
```

Refreshing the toolkit does not modify `.env.production`, `.deployrc`,
`deploy/runtime/`, or data volumes; the managed tooling files replaced by the
refresh are backed up to `.sforum/tooling-backups/`.

The first update from a legacy direct-port topology asks you to confirm the
one-time blue/green ingress conversion (type `BOOTSTRAP`, or pass
`--bootstrap`). This step briefly stops the old services and starts the stable
Caddy ingress. After conversion, versions with an unchanged database or only
declared backward-compatible Core migrations can switch over while staying
online over HTTP; an undeclared Core migration, any River migration, or a
legacy migration image that fails the online safety check needs a maintenance
window. See [One-click install and update](./deployment/one-click.md) for the
full semantics.

## Target shape

| Service | Responsibility |
| --- | --- |
| `web` | Nuxt production output; proxies ordinary HTTP API on the same origin |
| `api` | Fiber API; extension runtime; embedded River Worker; optional WebSocket ingress |
| `postgres` | Application data, River queue, migration version |
| `redis` | Server-side sessions, cache, rate limiting |

The direct topology publishes only two loopback ports:

| Purpose | Default |
| --- | --- |
| Web | `127.0.0.1:${WEB_PORT:-3000}` |
| API / WebSocket Upgrade | `127.0.0.1:${API_PORT:-18080}` |

Ordinary `/api/v1/*` is forwarded same-origin by Nuxt to `api:8080`. Host
Caddy/Nginx examples live in `deploy/caddy/Caddyfile` in the deploy bundle; the
WebSocket Upgrade can go straight to the API port. Client IP handling already
matches this topology by default (private-network peers trusted, loopback not
trusted), so a host reverse proxy needs no `TRUSTED_PROXIES` entry of its own.
With a CDN in front, put its egress ranges into Caddy's `servers.trusted_proxies`;
Caddy deletes the CDN headers and recomputes the real client, so the app side
stays empty. Never trust every network range.

## Maintainer releases

Maintainers create a release from a clean `main` workspace that is already
synced to `origin/main`:

```sh
./scripts/release.sh
```

The script pushes the tag and returns immediately by default; only `--wait`
keeps the terminal attached to the result. Interactive releases accept a
one-line highlight, or multiple lines of Markdown via `--notes-file`. The
Release reuses the `main` CI already triggered by the same commit and builds,
scans, and promotes images only after the exact-SHA push CI succeeds. The
GitHub Release publishes:

- the `sforum` management CLI for `linux/amd64`, `linux/arm64`,
  `darwin/amd64`, and `darwin/arm64`; Windows is not supported.
- `linux/amd64` and `linux/arm64` backend runtime bundles with the API,
  migrator, CLI, and the exact built-in extensions extracted from the same
  scanned candidate image.
- the fixed-name `sforum-bootstrap.sh`, `sforum-deploy.tar.gz`, and the
  compatibility `upgrade.sh`.
- `SHA256SUMS` covering every archive, plus GitHub build provenance.

The backend bundle is not a complete site installer: it has no Nuxt Web,
PostgreSQL, or Redis. Production still uses the three version-matched Docker
images. After downloading, verify with
`gh attestation verify <file> --repo keeptrue/SForum` and `SHA256SUMS`.

## Read further

- [One-click install and update](./deployment/one-click.md): the complete
  execution chains, parameters, state files, and failure semantics of
  bootstrap, deploy.sh, and upgrade.sh.
- [Manual Compose deployment](./deployment/manual.md): no scripts — from
  downloading the deploy bundle to the first administrator, reverse proxy,
  updates, and rollback.
- [Environment variables and secrets](./deployment/environment.md): how
  `.env.production` is loaded, required entries, generation and rotation, and
  the Compose pass-through boundary.
- [Troubleshooting](./deployment/troubleshooting.md): symptoms, diagnosis,
  remediation, and the `recovery_required` recovery path.

## After go-live checklist

1. Register the first account; it becomes a protected `super_admin`.
2. Confirm the site name, HTTPS URL, language, and appearance in site settings.
3. Configure mail (the built-in SMTP plugin) so password resets and
   notifications work.
4. Choose an attachment storage provider.
5. Search uses the site PostgreSQL FTS by default; enable the Meili plugin only
   if needed.
6. Rehearse extension trust and Safe Mode once.
7. Check `/health`, `/api/v1/health`, `/api/v1/ready`.
8. Set a backup retention period and keep backups on host disk or off-site
   storage.

## Related

- Local development: [Getting started](./getting-started.md)
- Site operator: [Usage](./usage/README.md)
- Archived long draft: `docs/archive/legacy-root/development-and-deployment.md`
