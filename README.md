# SForum

Maintainable, plugin-first open-source forum framework.

Core is the host (identity, forum primitives, permissions, extension runtime, contracts). Deployment-specific behavior—mail transport, optional search engines, storage vendors, and similar—lives in extensions.

![SForum 社交卡：插件优先的开源论坛框架](./docs/assets/brand/social-preview.png)

## Screenshots

### Forum home
Topic feed, category navigation, and live site statistics.

![SForum forum home with topic feed, category rail, and site statistics](./docs/assets/screenshots/home.png)

### Categories
Every board grouped by section, with topic and reply counts.

![Category browser grouped by board with topic and reply counts](./docs/assets/screenshots/categories.png)

### Topic and discussion
Article body, tags, and threaded replies, with theme-injected extensions in the right rail.

![Topic page with article body, tags, and threaded replies](./docs/assets/screenshots/topic-detail.png)

### Composer
Rich-text toolbar, draft controls, pre-publish checks, and publishing settings on one screen.

![Topic composer with rich-text toolbar and pre-publish checklist](./docs/assets/screenshots/composer.png)

### Sign in and sign up
Account entry with password strength checks, locale, and appearance controls.

![Sign-in page with brand panel and account form](./docs/assets/screenshots/login.png)

![Sign-up page with account fields and password strength meter](./docs/assets/screenshots/register.png)

### Account security
Login devices, active sessions, and sign-in history for the current member.

![Account security page listing login devices and sign-in history](./docs/assets/screenshots/account-security.png)

### Admin · Extension overview
Plugin and theme status, host capability declarations, and ZIP upload from the control panel.

![Admin control panel extension overview with plugin and theme status](./docs/assets/screenshots/admin-extensions.png)

## Documentation

| Language | Start here |
| --- | --- |
| **简体中文** | [`docs/zh-CN/README.md`](./docs/zh-CN/README.md) |
| **English** | [`docs/en-US/README.md`](./docs/en-US/README.md) |
| Hub | [`docs/README.md`](./docs/README.md) |

Quick paths:

- [快速开始](./docs/zh-CN/getting-started.md) / [Getting started](./docs/en-US/getting-started.md)
- [使用说明](./docs/zh-CN/usage/README.md) / [Usage](./docs/en-US/usage/README.md)
- [开发指南](./docs/zh-CN/development/README.md) / [Development](./docs/en-US/development/README.md)
- [生产部署](./docs/zh-CN/deployment.md) / [Deployment](./docs/en-US/deployment.md)
- Extension technical reference: [`docs/extensions/`](./docs/extensions/)

## Repository map

| Path | Role |
| --- | --- |
| `apps/web` | Nuxt 4 frontend |
| `apps/api` | Go Fiber API with embedded River worker, CLI |
| `contracts/` | OpenAPI + Protobuf |
| `extensions/` | Built-in / optional / dev packages |
| `docs/` | Bilingual handbooks + extension reference |
| `knowledge/` | Decisions, module notes, session handoffs |
| `scripts/` | Dev and test helpers |
| `deploy.sh`, `upgrade.sh`, `sforum-latest.sh` + `compose*.yaml` | Production orchestration, updates, and the verified-latest entry |

## Local development

```sh
./scripts/dev.sh                 # PostgreSQL, Redis, Mailpit + migrations
./scripts/api-dev.sh             # API (embeds worker in dev)
cd apps/web && bun run dev       # Nuxt on :3000
```

Background jobs: the API process always embeds the River worker in development
and production. Worker ownership is not configurable, so saved settings,
SecretStore access, extension runtimes, and queue consumers share one process.

Useful URLs:

- Web: http://127.0.0.1:3000  
- API health: http://127.0.0.1:3000/api/v1/health  
- API ready: http://127.0.0.1:3000/api/v1/ready  
- Mailpit: http://127.0.0.1:18025  

Meilisearch is **optional** (`docker compose --profile search up -d meilisearch`). Default search is built-in site PostgreSQL FTS.

Full steps: [docs/zh-CN/getting-started.md](./docs/zh-CN/getting-started.md) or [docs/en-US/getting-started.md](./docs/en-US/getting-started.md).

## Production

One line installs the latest stable Release into `./sforum`:

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash
```

`SFORUM_INSTALL_DIR` or `-d DIR` changes the target, `--version <TAG>` pins an
immutable tag, and `--yes` skips prompts. The loader resolves the tag, verifies
that tag's `sforum-bootstrap.sh` against `SHA256SUMS` (plus GitHub build
provenance when `gh` is available), promotes it, and hands off. The bootstrap
then refreshes the complete matching deploy toolkit before the install wizard
starts.

Running the same line inside an existing installation updates it instead. To
update without trusting the bootstrap already on disk:

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash -s -- upgrade
```

Only the loader may run from a pipe; it contains no install logic. The
auditable equivalent — download the bootstrap and its `SHA256SUMS`, select
exactly one filename entry, verify, then execute — stays documented in
[docs/en-US/deployment/one-click.md](./docs/en-US/deployment/one-click.md)
([中文](./docs/zh-CN/deployment/one-click.md)).

A local `./sforum-bootstrap.sh upgrade` behaves the same way: every run
refreshes the bootstrap and the target Release's complete deploy toolkit before
handing off to `upgrade.sh`. The default resolves to the newest **stable**
Release and is confirmed before any change (`--yes` skips prompts). Prereleases
are never selected implicitly: pass `--channel prerelease` or an explicit tag.
Every choice resolves to an immutable `vX.Y.Z` tag — floating `latest` images
are never used in production Compose.

For toolkit repair — re-syncing every managed file from a Release instead of
only the bootstrap — use the repository-side `sforum-latest.sh` wrapper
documented in
[docs/en-US/deployment/one-click.md](./docs/en-US/deployment/one-click.md).
It resolves the newest Release, downloads and verifies its complete
`sforum-deploy.tar.gz`, syncs every managed toolkit file (`deploy.sh`,
`upgrade.sh`, Compose files, `deploy/scripts`, Caddy examples, `VERSION`) to that
version, backs up the previous files under `.sforum/tooling-backups/`, and only
then delegates to install or upgrade. `--check` lists exactly which files are
missing or stale without writing anything.

Production uses one API process with an embedded River worker, sharing the
database pool, SecretStore-backed settings, and extension runtime. Existing
standalone Worker containers from older releases are removed during an update;
legacy environment settings cannot disable the API-owned worker.

The first blue/green ingress conversion has a short maintenance window. Later
releases keep API/Web HTTP traffic available when the database is unchanged or
every pending Core migration explicitly declares backward-compatible online
execution; WebSockets may reconnect while durable River jobs remain protected
by the queue during the API slot handoff. Undeclared Core and all River
migrations use the blue/green-aware `deploy.sh` maintenance path.

Full guides:

- [Deployment overview](./docs/en-US/deployment.md) / [中文](./docs/zh-CN/deployment.md)
- [One-click install and update](./docs/en-US/deployment/one-click.md)
- [Manual Compose deployment](./docs/en-US/deployment/manual.md)
- [Environment and secrets](./docs/en-US/deployment/environment.md)
- [Troubleshooting](./docs/en-US/deployment/troubleshooting.md)

## Community

- Report vulnerabilities privately according to [`SECURITY.md`](./SECURITY.md).
- SForum is available under the [`MIT License`](./LICENSE).

## Contributing / agents

1. Read [`AGENTS.md`](./AGENTS.md)  
2. Read [`docs/`](./docs/README.md) for usage and development  
3. Read [`knowledge/index.md`](./knowledge/index.md) for current project memory  
4. Keep OpenAPI, tests, and knowledge notes updated with code changes  
