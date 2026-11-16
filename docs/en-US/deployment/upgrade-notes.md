# Upgrade notes

[← Deployment](../deployment.md)

This page collects only the changes that alter what an operator has to do across
a version boundary. Every entry uses the same three fields: what the old
behaviour was, what it is now, and what to do when upgrading. Version numbers
come from the Release tag (`$SFORUM_VERSION`); this page rolls with the release
line and never pins one.

The three-line version: **configure the backup passphrase before upgrading**,
**send a sha256 with `install-from-url`** from any script you own, and **re-verify
any plugin holding a `raw_core` grant** after the upgrade.

## At a glance

| # | Entry | Who is affected | When |
| --- | --- | --- | --- |
| 1 | `install-from-url` requires a sha256 | Your own scripts, SDK callers | Before upgrading |
| 2 | `raw_core` narrowed to an explicit allowlist | Plugins holding that grant | Re-verify after |
| 3 | Plugin `global_middleware` scope is declared | Plugins declaring global middleware | Re-verify after |
| 4 | Plugin subprocess cwd pinned to the artifact directory | Third-party plugins | Re-verify after |
| 5 | Backup passphrase is a hard prerequisite | Every installation | Before upgrading |
| 6 | Anonymous `/ready` returns a summary only | Scripts parsing `/ready` | Adjust if needed |
| 7 | 401 and 503 are now distinct | API clients, monitoring | Regenerate clients if needed |
| 8 | Backup/restore validation is stricter and fails earlier | Whoever runs backups and restores | Rehearse once |

## 1. `install-from-url` requires a sha256

**Old behaviour**: `sha256` was optional. A package could be installed without
it, and the Host never checked that the bytes it downloaded were the bytes you
meant.

**New behaviour**: `POST /api/v1/admin/extensions/install-from-url` treats the
sha256 as the only provenance signal. A missing field is rejected with 422
`extension.remote_digest_required` **before any outbound request**; a value that
is not 64 lowercase hex characters is 422 `validation.invalid`; a mismatch after
download is 422 `extension.remote_digest_mismatch`. The admin URL-install form
already required it.

**What to do**: if a script, CI job, or SDK calls this endpoint, hash first:

```sh
sha256sum sforum-plugin.zip
```

The request body carries at least these two fields (add authentication the way
you already do):

```json
{"url": "https://example.com/sforum-plugin.zip", "sha256": "<the 64-char hex from sha256sum>"}
```

An `extension.remote_digest_required` on the first call after upgrading means
this was not updated.

## 2. `raw_core` narrowed to an explicit allowlist

**Old behaviour**: issuing a `raw_core` lease snapshotted `pg_class` and granted
`SELECT` / `INSERT` / `UPDATE` / `DELETE` on every Core table in the public schema
(165 tables and 89 sequences) — including `users`, `user_credentials`,
`user_sessions`, `api_tokens`, `secret_store`, `extension_trust_grants`, and
`audit_events`. What had been granted was neither enumerable nor reproducible.

**New behaviour**: only three rebuildable projection tables are granted —
`search_documents`, `search_index_state`, `search_reindex_runs` — plus the
sequence `search_reindex_runs_id_seq`. Every other Core table has **zero ACL and
zero effective privileges**; out-of-scope access fails on first use with
PostgreSQL's `ERROR: permission denied for table <name>` (SQLSTATE `42501`). The
allowlist is code (the `rawcore` subpackage, one read-only copy shared by grant
and validation), so a new derived Core table waits for a release and must land
with tests and a decision record. `core_views` reads and `own_schema` tables are
unaffected.

**What to do**: after upgrading, run any third-party plugin holding `raw_core`
and watch its log for `42501`. To see what it can still write:

```sql
SELECT n.nspname AS schema, c.relname AS relation
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname IN ('public', 'sforum_core_v1') AND c.relkind IN ('r','p','v')
  AND pg_catalog.has_table_privilege(current_user, c.oid, 'INSERT')
ORDER BY 1, 2;
```

Migration order for a plugin that overreaches: reads move to `sforum_core_v1`
views or a HostQuery; content writes move to a Host Command or an event plus its
own projection table in `own_schema`; configuration writes move to the options /
admin API; a genuinely rebuildable projection is a proposal to extend the
allowlist. There is no "restore whole-database DML" switch — do not look for one.

## 3. Plugin `global_middleware` scope is declared, not implied

**Old behaviour**: `global_middleware` joined every matching contribution list
unconditionally, so any plugin's global middleware wrapped every Core `/api/v1`
terminal. The only isolation on the Core side was the fact that a route was a
Core route — no declaration, audit, or test constrained it.

**New behaviour**: scope comes from `targetId`, with exactly two forms: empty
means the plugin's own terminals only; non-empty means precisely that one
terminal (Core routes opt in explicitly). `path` and `methods` stay forbidden
(global middleware is not prefix matching); `core.route.system.health`,
`core.route.system.ready`, and `core.route.notifications.stream` cannot be
targeted, and manifest validation rejects the attempt.

**What to do**: a plugin that implemented its feature by wrapping Core routes
will **silently stop applying** after the upgrade (the direction is narrower, not
wider). Review each plugin's `global_middleware` entries: add `targetId` where a
Core route must be covered, otherwise it now applies only to that plugin's own
terminals. Afterwards, run the plugin's own tests or watch the header/field it
injects to confirm the middleware still runs.

## 4. Plugin subprocess cwd is pinned to the artifact directory

**Old behaviour**: plugin subprocesses inherited the host process cwd, so
relative paths in a plugin resolved against the directory the Host was started
from.

**New behaviour**: cwd is fixed to the directory holding the backend entry
(conventionally `<package-root>/backend`), and the exec target comes from the
same resolution as an absolute path. An entry outside the package content root,
or a host working tree inside the plugin cwd, refuses to start. Under the
filesystem sandbox, writing next to the plugin's own binary is `EACCES`.

**What to do**: `./x` and `os.ReadFile("config.json")` now resolve inside the
package directory, not the start directory. Write through absolute paths
(`os.UserCacheDir()`, a Host API, an operator-configured directory), never next
to the binary. All eight built-in plugins were checked: none depended on relative
paths into the host working directory. The authoring contract is in
[Build and load](../../extensions/build-and-load.md), section "Runtime process
contract".

## 5. Backup passphrase is a hard prerequisite for upgrades

**Old behaviour**: the `.env.production` template had no backup passphrase
section and `deploy.sh` read the process environment only. A fresh install that
went straight to `update` stopped at the backup step with nothing in the wizard
or template warning about it.

**New behaviour**: the template carries a commented `SFORUM_BACKUP_KEY_FILE` /
`SFORUM_BACKUP_KEY` block (commented so Compose never injects it),
`configure-production.sh` prints the generation command at the end, and
`deploy.sh` reads the value from `.env.production` (shell environment wins). A
missing key still fails hard and **never produces a plaintext backup**.
`upgrade.sh` calls the backup script directly and still reads the process
environment only.

**What to do**: create the key file before upgrading, and configure both places:

```sh
install -m 600 /dev/null /etc/sforum/backup.key
openssl rand -base64 32 > /etc/sforum/backup.key
```

Uncomment and point at it in `.env.production` (this covers `deploy.sh` backup and
update):

```sh
SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key
```

Then `export` the same variable before running `upgrade.sh` — it cannot see that
line. Keep the key off the disk that holds the archives. After a key rotation,
older archives need the older key, so keep it until a new backup restores
successfully.

## 6. Anonymous `/ready` returns a summary only

**Old behaviour**: an anonymous request to `/api/v1/ready` returned the full
readiness report: in recovery mode it carried `recovery.artifacts[]` (the
extensionId / version / package digest of every installed plugin) and
`publicationRevision`; `components[].error` echoed the raw plugin startup failure,
which can contain artifact paths, versions, and digests.

**New behaviour**: only `status`, `ready`, `checkedAt`, `components`, and
`recovery{code,component,message}`. `artifacts` and `publicationRevision` are
gone, `components[].error` collapses to a fixed summary, and the contract marks
these schemas `additionalProperties: false`. Recovery mode still answers 503 with
`ready:false`, just without the fingerprint.

**What to do**: consumers that read only the status code (Compose healthchecks,
`release-smoke.sh`) are unaffected; `wait-for-health.sh` and `upgrade.sh` also
read the `recovery` block from the body, which is still in the whitelist, so
their decision logic is unchanged. Scripts that scraped the artifact list move to
the out-of-band channels:

```sh
sforum extension recovery list --json
sforum extension recovery recheck --reason "why it failed"
```

The `publication_revision` and artifact count in the API startup log remain. The
same batch changed the probe: health waiting is now three-state (`0` ready /
`3` recovery-only / `1` timeout), `deploy.sh` records `reason=recovery_only` and
points at the recovery recheck, and `upgrade.sh` aborts and rolls back on the
first recovery-only probe instead of burning the whole timeout.

## 7. 401 and 503 are now distinct

**Old behaviour**: the same read endpoint answered "no subject" five different
ways — every error became 401 (a Redis blip was reported as "your login is
invalid"), errors were swallowed into an anonymous 200, errors were re-thrown as
500, some endpoints stayed deliberately anonymous, and `Pages` skipped the viewer
branch. On authenticated endpoints, 19 direct `LoadActor` call sites turned
dependency failures into 500 as well.

**New behaviour**: five classes — A no credential → 401 on authenticated
endpoints and anonymous 200 on public reads (unchanged); B credential present but
unparseable/expired → indistinguishable from A (the session layer folds it into
`(0,false,nil)`); C subject missing / disabled / banned → 401
`auth.required` on public reads, while write paths keep answering 403
`permission.denied` from the domain layer; D dependency failure → 503
`service.not_ready` with `Retry-After: 1`; E unrecognized → 503 (fail-closed,
never widened to anonymous). All 19 direct call sites across 18 controller
packages now share one gate, and the contract gained 188 `503` blocks measured by
the reachability gate (plus 5 the gate cannot see, added by hand).

**What to do**: treat 503 with `Retry-After` as **retryable** — do not clear
cookies on it and do not alert it as "login broken"; 401 is the one that means
sign in again. Projects generating clients from `contracts/openapi.yaml` must
rerun the generator (the status-code set changed). Known boundary: B is not
observable at the HTTP layer, so a bad token and an anonymous request currently
get the same answer; the field-level `Retry-After` declaration is not yet part of
the parity gate.

## 8. Backup/restore validation is stricter and fails earlier

**Old behaviour**: volume restore only checked that archive entries existed in
the volume, so a half-written or truncated file passed; the topology guard only
covered "declared single but blue/green slots are running"; blue/green slots
shared one deadline; temporary work directories left behind by SIGKILL (holding a
`600` plaintext `database.sql`) were never cleaned; and a cold start treated every
`up -d --wait` failure as a slow start.

**New behaviour**: four changes together — volume validation compares **sizes**
per entry (missing entries and size mismatches both fail, and a listing that
parses to nothing is a failure, not a pass); the topology guard covers both
directions (declared blue-green while the single-topology `api` / `worker` runs
also stops before touching anything); each slot gets its own deadline (the second
slot no longer inherits a zero-second budget); backup work directories
`.sforum-backup-*` older than 24 hours (`SFORUM_BACKUP_STALE_WORKDIR_HOURS`,
minimum 1) and restore work directories `$TMPDIR/sforum-restore.*` older than a
day are swept. A cold start now separates "slow start" (downgraded to a warning
and handed to `pg_isready`) from "the container is not running" (fails
immediately, naming the container state).

**What to do**: the first restore after upgrading may **fail earlier** than it
used to — that is the point; do not work around it. When it fails, take a fresh
backup instead of forcing an old archive. Before restoring, confirm the
`topology` in `.deployrc` matches the services actually running, otherwise the
script stops before touching services (which is exactly what it should do). The
empty-core-table warning now prints once per restore; the second validation
passes quietly.

**One-time cleanup after upgrading (plaintext dumps)**: before the archive
format landed on 2026-11-15, production backups were plaintext
`<db>-<timestamp>.sql` files under `backups/`. Retention only matches the
`sforum-backup-*` prefix, so those legacy plaintext dumps are **never cleaned
automatically** — and they are full-database plaintext dumps containing user
data. After upgrading, review them and delete the ones you no longer need
yourself; the script will not do it for you (deleting an operator's historical
backups is more dangerous than keeping them). To restore one of them, note that
the restore side now refuses payloads without integrity evidence (a legacy
`.sql`, a manifest-less `tar.gz`, a manifest missing `encryption=`); set
`SFORUM_RESTORE_ALLOW_UNVERIFIED=1` explicitly and the script warns before
continuing. The source is always the file you name — the script never walks the
backup directory to pick the newest one.

## If you must roll back

Rolling back brings the old behaviour back with it: `/ready` echoes the artifact
list and `publicationRevision` again, the restore script stops comparing sizes,
`global_middleware` wraps Core routes unconditionally again, and 401/503 collapse
back into 401/500. Confirm that before rolling back, especially the `/ready`
surface.

## Verification

```sh
./deploy.sh --action status
curl -fsS "http://127.0.0.1:${API_PORT:-18080}/api/v1/ready" | jq '{status, ready, recovery}'
sforum extension recovery list --json
```

The first shows deployment state; the second confirms `/ready` returns a summary
only (no `artifacts` or `publicationRevision` in the response); the third
confirms the recovery artifact list is still reachable out of band.

## Related

- [One-click install/update](./one-click.md): the full `deploy.sh` / `upgrade.sh`
  execution chain and failure semantics.
- [Environment and secrets](./environment.md): how `.env.production` is loaded
  and which keys are required.
- [Deployment troubleshooting](./troubleshooting.md): what to do for each
  `recovery_required` `reason`.
