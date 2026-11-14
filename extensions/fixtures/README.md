# Extension fixtures

These packages are repository-owned test inputs used by CI, `go test`, CLI
validation, and production-style browser tests. They lock extension contracts
and must remain reproducible from a fresh clone.

Parent layout map (not product install): [../README.md](../README.md).

| Package | Purpose |
| --- | --- |
| `plugins/page-registry-demo` | Page Registry lifecycle, templates, outlets, and L1 fallback |
| `plugins/sforum-admin-surface-reference` | Complete Protocol V2 Admin Surface reference plugin |
| `plugins/sforum-commerce-workflow` | Joined routes, hooks, jobs, services, database, cache, OpenAPI, and L2 component workflow |
| `plugins/sforum-commerce-workflow-ext` | Required dependency plus cross-plugin hook and service extension |
| `plugins/sforum-contract-events` | Manifest-only events + `forum.topic.actions` contribution |
| `plugins/sforum-contract-schedules` | Manifest-only reminder that schedules stay host-owned |
| `plugins/sforum-events-command-reference` | Plugin-namespaced `sforum.events.emit@1` publication through the real Host event bus, plus foreign/Core namespace refusals |
| `plugins/sforum-custom-content` | Entity, Content, Editor, Query, Navigation, and Region registries |
| `plugins/sforum-media-optimize` | Media Pipeline MIME policy, transforms, background jobs, and fallback |
| `plugins/sforum-membership-reference` | Identity, permission, auth, profile, recovery, session, and risk surfaces |
| `plugins/sforum-notification-reference` | Namespaced notification declaration and Host API v2 emission |
| `plugins/sforum-plugin-page-business-e2e` | Plugin-owned page data contract and presentation-only theme override target |
| `plugins/sforum-prebuilt-settings` | Digest-trusted, author-prebuilt admin settings ESM/CSS |
| `plugins/sforum-profile-tab-demo` | `forum.profile.tabs` shapes: public `l2Widget` tab, permission-gated `l2Widget` tab, and legacy `hostLink` tab |
| `plugins/sforum-query-reference` | Host-owned Query Registry execution and cross-plugin result filtering |
| `plugins/sforum-region-demo` | Public page region placements, setting gates, and prebuilt L2 widget |
| `plugins/sforum-settings-surface-demo` | `settingsSection` contributions: a `fields` profile tab (per-user entity-meta values), a `component` profile tab, and a `settings` site settings tab |
| `plugins/sforum-seo-reference` | Protocol V2 SEO Registry transport, Host-policy fallback, and trace attribution |
| `themes/sforum-plugin-override-e2e-theme` | Presentation-only override of a plugin-owned page template |
| `themes/sforum-schema-theme` | Buildless schema settings and theme manifest validation |
| `themes/sforum-public-l2-e2e-theme` | Production upload/trust/mount/restart/revoke test for author-prebuilt public ESM/CSS |

## Git tracking policy

- Track manifests, schemas, templates, source code, lock files, and immutable
  author-prebuilt assets required by a test.
- Prebuilt fixture files such as `frontend/**/dist/*.mjs` and `*.css` are
  intentional package inputs. They prove that an operator can upload and run an
  already-built package without Node, Bun, or a package-local build step; do not
  ignore or regenerate them during installation.
- Do not track local browser reports, temporary ZIPs, test databases, process
  logs, or platform-specific binaries produced while running a test. Tests must
  write those outputs to `t.TempDir()` or another ignored workspace path.
- Add a narrow `.gitignore` rule only when a repeatable local command creates a
  disposable path. Never ignore `extensions/fixtures/` as a whole.

Validate / test from repo root:

```bash
cd apps/api
go run ./cmd/sforum extension test ../../extensions/fixtures/plugins/sforum-contract-events
```

The public L2 production path is covered by
`apps/api/bootstrap/public_l2_production_e2e_test.go`; the fixture intentionally
contains no `package.json` and no package-local build command.

Published catalogs (generated from the same Go sources):

```bash
cd apps/api
go run ./cmd/sforum extension docs generate --check
```

See `docs/extensions/authoring-guide.md` and `docs/extensions/catalogs/`.

## Replaying a fixture into the dev database

A fixture that only exists as a browser/API sample is one-shot evidence: when the
dev database is wiped, the sample is gone. `scripts/dev-fixture-install.sh` makes
it repeatable without restarting the API.

```bash
./scripts/dev-fixture-install.sh sforum.badge-facts          # install + wait for enable
./scripts/dev-fixture-install.sh --status sforum.badge-facts # read back only
```

How it works: the package is synced into the dev source root (`extensions/dev/`,
gitignored) and the **already running** dev API does the rest — build → digest →
stage → exact-artifact grant → enable (`DevAutoRuntime`, zero-command dev mode).
No API restart, no login, and no write to `trust.levels` / `user_trust_levels`.

Preconditions: `.env` exists, and `:8080` runs with `APP_ENV=development` with
`extensions/dev` in the source-root set (`EXTERNAL_EXTENSION_ROOTS`).

Idempotent by construction: the content signature ignores `backend/plugin*` build
outputs and the manifest digests the build recomputes, so a second run copies
nothing and emits no file event; host-side `BuildBackend`/`WriteDigest` also skip
rewriting when bytes are unchanged. Both runs print the same single `RESULT` line
and produce no second artifact.

A cold copy logs one **transient** `external.package_invalid` WARN: between the
`rsync` finishing and the host build finishing, the manifest still declares
`backend/plugin` while that file does not exist yet, so the source scan reports
`read package file backend/plugin: no such file`. It self-heals on the next build
(seconds), the digest does not change, and the extension stays enabled — this is
the deliberate consequence of not copying the author-built binary.

Read back the positive sample with an admin session:

```bash
curl -s -b cookies.txt \
  http://127.0.0.1:8080/api/v1/admin/extensions/sforum.badge-facts/trust
```

Known boundaries (measured 2026-11-14):

- An **explicitly disabled** extension is left alone by dev auto (`promoteOne`
  respects `disabled`); enable it from the admin first, then replay.
- A **non-lifecycle-V2 package** cannot switch from an old digest to a new one
  automatically: dev auto logs `requires a manual admin restart bridge`. Use the
  admin restart / trust-challenge path for that one transition.
- Admin **uninstall** leaves a `disabled` row whose artifacts were purged, and
  dev auto will not adopt it again. Clean it with
  `POST /admin/extensions/missing-artifacts/uninstall` and replay; the script
  never deletes database rows itself.
- The script runs under `set -uo pipefail` **without `-e`** (known, not fixed):
  most failures are caught with `|| die`, but a failing `mkdir`/`touch` falls
  through and surfaces as a generic timeout instead of the real cause.
- `read_status` hides CLI stderr (`2>/dev/null`) (known, not fixed): when the CLI
  cannot reach the database the run waits the full `--wait` before failing
  instead of reporting the connection error immediately.
- `RESULT` is printed before the enabled check, so the **exit code is
  authoritative**: a line such as `RESULT ... status=installed` still exits 1.
