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
| Existing install, re-verify the official bootstrap on every run | `./sforum-latest.sh` | [One-click install and update](./deployment/one-click.md) |
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

The command below downloads and verifies the **latest stable**
`sforum-bootstrap.sh`; the bootstrap then refreshes the complete deploy toolkit
for that same target version:

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
    if command -v gh >/dev/null 2>&1; then
      gh attestation verify sforum-bootstrap.sh --repo keeptrue/SForum
    fi
  )
  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh
  rm -rf "$bootstrap_dir"
  trap - EXIT HUP INT TERM
  ./sforum-bootstrap.sh install
)
```

That address always points at the latest stable Release, so no version number
needs maintaining. The install wizard explains each setting and automatically
generates the PostgreSQL, Redis, session, verification, identity HMAC,
sensitive-option encryption, and Marketplace verifier keys.
Pressing Enter for every question yields a runnable local configuration; the
generated `.env.production` is mode `0600` and secrets are never printed to the
terminal.

### Verification rules (required)

- **Never pipe a remote response directly into a shell**: save the bootstrap
  first, then download the checksum file.
- Verify only the exact `sforum-bootstrap.sh` entry in `SHA256SUMS`, and require
  that the result is exactly one line; the bootstrap applies the same rule to
  the complete deploy bundle internally.
- The whole command runs in a `set -eu` subshell; a failed download, a missing
  or duplicate entry, or a checksum or provenance failure aborts before any
  remote script executes.
- `gh attestation verify` is an optional provenance check (it needs the GitHub
  CLI, authenticated). When the bootstrap detects `gh`, it also verifies the
  downloaded bootstrap and deploy bundle automatically;
  `SFORUM_SKIP_ATTESTATION=true` skips it explicitly, but only where `gh`
  cannot be installed.

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
update must prove that the bootstrap came from GitHub, run this one command
inside the install directory; it verifies the wrapper's own SHA-256 before
executing it:

```sh
# Run inside the install directory. Rerunning replaces the wrapper atomically;
# no manual deletion is needed.
tmp="$(mktemp ./.sforum-latest.XXXXXX)" && \
curl -fsSLo "$tmp" "https://raw.githubusercontent.com/keeptrue/SForum/main/sforum-latest.sh?$(date +%s)" && \
curl -fsSLo "$tmp.sha256" "https://raw.githubusercontent.com/keeptrue/SForum/main/sforum-latest.sh.sha256?$(date +%s)" && \
mv -f "$tmp.sha256" sforum-latest.sh.sha256 && \
mv -f "$tmp" sforum-latest.sh && \
{ sha256sum -c sforum-latest.sh.sha256 2>/dev/null || shasum -a 256 -c sforum-latest.sh.sha256; } && \
chmod 0755 sforum-latest.sh && \
./sforum-latest.sh
```

Each run resolves the newest Release, downloads and verifies its complete
`sforum-deploy.tar.gz`, syncs every managed toolkit file (`deploy.sh`,
`upgrade.sh`, Compose files, `deploy/scripts`, Caddy examples, `VERSION`) to
that version, backs up the previous files under `.sforum/tooling-backups/`, and
only then delegates to install or upgrade. `--check` lists exactly which files
are missing or stale without writing anything. Interactive runs ask for the
channel first: stable, the newest published release including alpha/beta, or an
explicit tag; `--channel prerelease` and `--version <TAG>` skip the prompt. A
target older than the version recorded in `.deployrc` is caught before any
download, and a migration failure prints the migrated Core version with the
command that matches it. If the database has already migrated to a higher
version, an older stable tag stops at the migration step; follow
[troubleshooting](./deployment/troubleshooting.md) to pick a matching version.
When `.env.production` exists but `.deployrc` has no
usable version record (for example a manual-Compose install), the wrapper runs
`./deploy.sh --action deploy --version <TAG>` once to rebuild the managed state
instead of stopping inside `upgrade.sh`. `--yes`, `--channel prerelease`, and
`--version <TAG>` match the bootstrap semantics.
The script is repository tooling: it is not a Release asset and the bootstrap
does not refresh it. Only this wrapper file comes from `main`; the bootstrap and
the complete toolkit still come from the Release asset. In a Git checkout,
syncing requires the explicit `--allow-git` flag so source files are not
replaced by accident.

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
WebSocket Upgrade can go straight to the API port. After configuring the
reverse proxy, remember to set `TRUST_PROXY` and a precise `TRUSTED_PROXIES`;
do not trust every network range.

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
