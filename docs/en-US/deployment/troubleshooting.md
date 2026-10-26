# Deployment troubleshooting

[← Deployment](../deployment.md)

Collect three things before changing anything:

```sh
./deploy.sh --action status
./deploy.sh --action logs
cat .deployrc
```

`status=recovery_required` in `.deployrc` is the key signal for "the old
application stopped and the new version never became healthy". When it is
absent, check `docker compose ps` and the API logs first.

If you use `sforum-latest.sh`, run `./sforum-latest.sh --check` first: it
resolves the target Release, verifies the deploy bundle, lists the managed
toolkit files that need updating, and shows the planned action (install,
upgrade, or a deploy repair) without writing anything.

Commands on this page run in the install directory and reuse this array:

```sh
COMPOSE=(docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml)
```

## Host and Docker

| Symptom | Diagnosis | Action |
| --- | --- | --- |
| `docker: command not found` | Docker is not installed | Install Docker Engine; do not substitute Podman |
| `Cannot connect to the Docker daemon` | the daemon is not running | Start the Docker service and retry |
| `Docker Compose 2.24.4 or newer is required.` | Compose is too old | Upgrade the Compose plugin; confirm with `docker compose version --short` |
| `permission denied ... docker.sock` | the current user has no Docker permission | Run as a user that has permission, or add the user to the `docker` group and log in again |
| `curl is required for the final health check.` | the host is missing `curl` | Install `curl`; the script uses it to probe health endpoints |

## Download and verification

| Symptom | Diagnosis | Action |
| --- | --- | --- |
| `could not query the latest stable Release` | the GitHub API is unreachable, or no stable Release exists yet | Check network/DNS; for a prerelease use `--channel prerelease` or an explicit `--version <TAG>` |
| `SHA256SUMS must contain exactly one ... entry` | the checksum file is missing, duplicated, or malformed | Re-download `SHA256SUMS` for the same tag; do not edit it by hand |
| `checksum verification failed for ...` | the download is corrupt or the asset does not match | Delete the file and download it again; do not skip verification |
| `provenance verification failed for ...` | `gh attestation verify` failed | Confirm the tag and repository are correct; if `gh` cannot be installed you may set `SFORUM_SKIP_ATTESTATION=true` explicitly, but verify the checksum first |
| `deployment bundle VERSION does not match ...` | bundle contents do not match the target tag | Re-download `sforum-deploy.tar.gz` for that tag |
| `deployment bundle is missing ...` | the bundle is incomplete | Download it again; do not assemble files from an old directory |
| `deployment bundle contains path traversal` | the bundle contains an abnormal path | Stop using that bundle and download it again from the Release |
| `another bootstrap operation is already running` | the bootstrap lock was not released | Wait for the other operation to finish; once no process remains, delete `.sforum-bootstrap.lock` |
| `another deployment or update operation is running` | the deployment lock was not released | Wait for the other operation; `upgrade.sh` cleans up automatically when the holding process exits or the lock is older than one hour |

## Install and update state

| Symptom | Diagnosis | Action |
| --- | --- | --- |
| `an existing installation was detected; use the upgrade command` | the directory already contains `.env.production` or `.deployrc` | Use `./sforum-bootstrap.sh upgrade`; do not delete data volumes just to reinstall |
| `.deployrc is missing; run install first` | the installation only completed halfway | Run `./deploy.sh --action deploy --version <TAG>` first to record state, then return to bootstrap upgrade |
| `The currently deployed version is unknown; re-run ./deploy.sh to restore state.` | `.deployrc` has no valid `version=vX.Y.Z` and there is no blue/green slot container to infer it from | Run `./deploy.sh --action deploy --version <TAG>` to rebuild the state; `./sforum-latest.sh` detects this and takes that path |
| `core upgrade is incompatible ... target=... is older than migrated Core=...` | the database has already migrated to a higher Core version and the target is older | Use `./sforum-latest.sh --version <migrated Core version>` or `--channel prerelease`; to truly roll back, restore the pre-deployment backup instead of only rolling back images |
| the same error on a **local dev build** (`target=1.0.0`) | `git describe --tags --match 'v[0-9]*' --long --dirty --always` degraded to a bare sha (usually a shallow clone hiding the `v*` tags), so the dev build cannot derive its release line and falls back to the `1.0.0` sentinel — "this build does not know what it is" was read as "this build wants to downgrade" | Run `git rev-parse --is-shallow-repository`; when it prints `true`, run `git fetch --unshallow`, then confirm the `git describe` output above starts with `v`. From this version on an undeclared target no longer takes part in the downgrade fence, and the log carries a `Core version is unknown for this build` warning |
| `.env.production is missing; run install first` | the configuration was moved away or installation never finished | Restore the configuration or rerun the install wizard; do not invent a database password |
| `The production configuration is incomplete or unsafe. ... <KEY>` | the environment variable contract is not satisfied | Fix the named key; the rules are in [environment variables and secrets](./environment.md) |
| `A required loopback port is already used by another process:` | `WEB_PORT`/`API_PORT` is taken | Stop the process holding it, or change the ports in `.env.production`; blue/green topologies must also check `edge` |
| `Could not pull ghcr.io/... after 3 attempts.` | the registry is unreachable, packages are private, or the pull is rate limited | Confirm all packages are publicly readable; run `docker login ghcr.io` if needed; retry later |
| `image version is '...', expected '...'` | the image tag does not match the build identity | Do not continue; confirm `SFORUM_VERSION` matches a tag in the registry |
| `Standby slot failed internal health checks; live traffic was not changed.` | the standby slot failed its internal checks | Live traffic is unaffected; read the new slot's logs, fix the cause, and retry |
| `Ingress switch failed and was rolled back to the active slot.` | the Caddy switch failed and was rolled back | Check `deploy/runtime/Caddyfile` and the `edge` logs; traffic is still on the old slot |
| `Migration failed. The previous slot was restarted...` | the maintenance-window migration failed | The old slot was restarted; fix the migration problem, or restore the backup recorded in `.deployrc` |
| `Target slot failed its health checks after migration...` | the migration succeeded but the new slot is unhealthy | The old slot was restarted; check the new version's configuration and dependencies, then retry |
| `The target release has migrations that are not safe online...` | the target migrations require downtime | Run `./upgrade.sh --version <TAG> --confirm-maintenance` in the install directory; bootstrap does not forward that flag |
| First conversion asks for `BOOTSTRAP` | a legacy direct-port topology must be converted to blue/green | Type `BOOTSTRAP` interactively, or add `--bootstrap`; this step has a short maintenance window |

## `recovery_required` decision tree

1. Read `reason` and `backup` in `.deployrc`.
2. `migration_failed`: read the migration logs first; fix the SQL/permission problem and retry the deployment. If it cannot be fixed, restore `backup`.
3. `startup_failed`: check `docker compose ps` and the API logs; the usual causes are environment variables, volume permissions, and image tags.
4. `health_failed`: check `http://127.0.0.1:${API_PORT}/api/v1/ready` and
   `http://127.0.0.1:${WEB_PORT}/health` separately; then confirm PostgreSQL/Redis health.
5. If retries keep failing and the database has already been migrated, restore the pre-deployment backup instead of only rolling back the images.

Restore command:

```sh
SFORUM_CONFIRM_RESTORE=RESTORE ./deploy.sh --action restore
```

The restore script imports into a separate temporary database in a single
transaction and atomically swaps database names only after validating the
application tables; any SQL error leaves the original database in place.

## Runtime issues

| Symptom | Diagnosis | Action |
| --- | --- | --- |
| Reverse proxy returns 502/504 | wrong proxy target, or `web`/`api` is not running | Check `WEB_PORT`/`API_PORT`, `docker compose ps`, and the container logs |
| Browser API returns 403 (CSRF) | `Origin` does not match `CSRF_TRUSTED_ORIGINS` | Put the public origin into `CSRF_TRUSTED_ORIGINS`; separate multiple sites with commas |
| Admin panel returns 404 | the path used does not match `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` | Use `APP_URL + prefix`; restart `web` after changing the prefix |
| WebSocket cannot connect | the proxy does not forward Upgrade, or the API port is wrong | Point Upgrade at `API_PORT` as `deploy/caddy/Caddyfile` does |
| Uploaded attachments disappear | the volume is not mounted or was recreated | Check the `attachment_uploads` volume; confirm the volume before restoring a backup |
| An extension breaks startup | a plugin throws during startup | Disable it out of band with the Safe Mode flow below |
| Backup file is empty/unreadable | `pg_dump` failed or the disk is full | Read the backup script output; fix the cause and rerun; never publish a partial backup |

## Safe Mode out-of-band recovery

When an extension stops the API from starting, use Safe Mode to bring the host
up first, then disable the offending extension.

1. Add this to `.env.production`:

```sh
SFORUM_SAFE_MODE=true
```

2. Restart only `api`:

```sh
"${COMPOSE[@]}" up -d --no-build --pull never api
```

3. Extract the `sforum` binary from the Release's
   `sforum-cli_<VERSION>_linux_<arch>.tar.gz` and place it in the install
   directory. Compose does not publish `5432` to the host, so run the CLI
   inside the `postgres` network namespace:

```sh
POSTGRES_USER="$(sed -n 's/^POSTGRES_USER=//p' .env.production)"
POSTGRES_DB="$(sed -n 's/^POSTGRES_DB=//p' .env.production)"
POSTGRES_PASSWORD="$(sed -n 's/^POSTGRES_PASSWORD=//p' .env.production)"
POSTGRES_USER="${POSTGRES_USER:-sforum}"
POSTGRES_DB="${POSTGRES_DB:-sforum}"

sforum_cli() {
  docker run --rm -i \
    --network "container:$("${COMPOSE[@]}" ps -q postgres)" \
    -v "$PWD/sforum:/usr/local/bin/sforum:ro" \
    -e "DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@127.0.0.1:5432/${POSTGRES_DB}?sslmode=disable" \
    alpine:3.22 /usr/local/bin/sforum "$@"
}
```

4. Based on the result, disable or isolate with `sforum extension disable`,
   `sforum extension disable-all`, or `sforum extension quarantine`:

```sh
# Show extension status
sforum_cli extension list

# Disable a single third-party extension
sforum_cli extension disable <extension-id>

# Disable every third-party extension
sforum_cli extension disable-all

# Quarantine an exactly matching built-in/system artifact
sforum_cli extension quarantine <extension-id> \
  --expect-version <exact-version> \
  --expect-digest <64-character-hex-digest>
```

5. Set `SFORUM_SAFE_MODE` back to `false`, then restart `api`.

`quarantine` isolates only an exactly matching artifact; a version or digest
mismatch is refused, which prevents accidental quarantining.

## Runtime resources

The `/control-panel` resource card reads CPU, RSS, and available PSS for the
API and its directly owned plugin processes through
`GET /api/v1/admin/overview/resources`. Linux images read `/proc` directly and
do not need the host PID namespace or the Docker socket. Samples are shared at
most every 5 seconds; RSS and complete PSS show the median over the last 60
seconds; systems without PSS do not fabricate an "effective usage". Linux also
reports `AnonHugePages` so you can confirm whether a plugin still has
transparent-hugepage amplification. Plugin rows are ordered by RSS and only
attribute processes directly owned by the current API/Worker. The API row is
marked "includes Worker"; the Worker row shows only embedded concurrency slots
and running jobs, never a fictional standalone Worker MiB.

## pprof and GOMEMLIMIT

These two variables are not in the default Compose `environment:` list, so
writing them into `.env.production` has no effect. Add
`compose.diagnostics.yaml`:

```yaml
services:
  api:
    environment:
      PPROF_ENABLED: "true"
      PPROF_ADDR: "127.0.0.1:6060"
      GOMEMLIMIT: "512MiB"
```

Apply the override:

```sh
DIAG=(docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml \
  -f compose.diagnostics.yaml)

"${DIAG[@]}" up -d --no-build --pull never api
```

pprof listens only on the container loopback and must not be exposed through
`ports:`. Reach it with a one-shot container that shares the network
namespace:

```sh
docker run --rm --network "container:$("${DIAG[@]}" ps -q api)" \
  alpine:3.22 wget -qO- http://127.0.0.1:6060/debug/pprof/
```

Delete the override and restart `api` once collection is done. Never publish
the pprof port publicly or hand it to the reverse proxy.

## Collecting evidence

Before filing an issue, keep at least:

```sh
./deploy.sh --action status > status.txt
./deploy.sh --action logs > logs.txt 2>&1
cat .deployrc > deployrc.txt
"${COMPOSE[@]}" ps > ps.txt
```

Logs and `.deployrc` may contain internal addresses; redact them before
sharing, and never paste `.env.production`, database passwords, or secrets.
