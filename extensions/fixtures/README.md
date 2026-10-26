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
